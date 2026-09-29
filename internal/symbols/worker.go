package symbols

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"time"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

// Bounds on one index run, and how it writes.
const (
	// DefaultMaxSymbols is where a repository's index stops growing.
	DefaultMaxSymbols = 200_000
	// DefaultMaxBytes bounds the names, keys and paths one index holds, so
	// a tree of long names cannot fill the database within the count.
	DefaultMaxBytes = 32 << 20
	// DefaultMaxTime is how long one run may take.
	DefaultMaxTime = 2 * time.Minute
	// DefaultChunkRows is how many symbols one write transaction carries.
	DefaultChunkRows = 5000
	// DefaultBackoff is how long a failed tree waits before it is tried
	// again.
	DefaultBackoff = time.Hour
)

// Worker builds the index for each repository that has asked for one:
// post-receive and the merge path ask after the default branch moves, and
// `admin symbols reindex` asks with force. One repository at a time.
type Worker struct {
	St         *store.Store
	RepoDir    func(owner, name string) string
	Tick       time.Duration
	MaxSymbols int
	MaxBytes   int
	MaxTime    time.Duration
	ChunkRows  int
	Backoff    time.Duration
	// chunkHook runs after each chunk is written; tests read the index
	// mid-build through it.
	chunkHook func(indexID int64)
}

func New(st *store.Store, repoDir func(owner, name string) string) *Worker {
	tick := 5 * time.Second
	if v := os.Getenv("GITBAY_SYMBOLS_TICK"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			tick = d
		}
	}
	return NewWith(st, repoDir, tick)
}

// NewWith is New with the tick given, and every bound at its default.
func NewWith(st *store.Store, repoDir func(owner, name string) string, tick time.Duration) *Worker {
	return &Worker{St: st, RepoDir: repoDir, Tick: tick,
		MaxSymbols: DefaultMaxSymbols, MaxBytes: DefaultMaxBytes, MaxTime: DefaultMaxTime,
		ChunkRows: DefaultChunkRows, Backoff: DefaultBackoff}
}

// Run sweeps until ctx ends.
func (w *Worker) Run(ctx context.Context) {
	t := time.NewTicker(w.Tick)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.Sweep(ctx)
		}
	}
}

// Sweep handles every due request once. A request is cleared whatever
// the outcome, except that a first failure is kept for one retry after
// Backoff: a failure is retried once, not in a loop, and again only when
// something asks.
func (w *Worker) Sweep(ctx context.Context) {
	reqs, err := w.St.SymbolRequests()
	if err != nil {
		slog.Error("symbols: listing requests", "err", err)
		return
	}
	for _, req := range reqs {
		if ctx.Err() != nil {
			return
		}
		failed, err := w.Index(ctx, req.RepoID, req.Force)
		if ctx.Err() != nil {
			return // shutting down: the request stays for the next start
		}
		if err != nil {
			slog.Warn("symbols: indexing", "repo", req.RepoID, "err", err)
		}
		if failed && req.Attempts == 0 {
			w.St.DeferSymbolRequest(req, int(w.Backoff.Seconds()))
			continue
		}
		w.St.DoneSymbolRequest(req)
	}
}

// Index brings one repository's index up to its default branch's head.
// A head whose tree is already indexed is left alone unless force is set,
// and so is one whose tree failed within Backoff. The new index is
// written in chunks while no read can see it, then published in one
// short transaction. A run cut short by a bound publishes what it found
// as partial; one that cannot build records the failure and leaves the
// current index in place, reporting failed. The error is for the log.
func (w *Worker) Index(ctx context.Context, repoID int64, force bool) (failed bool, err error) {
	repo, err := w.St.RepoByID(repoID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	} else if err != nil {
		return false, err
	}
	// A run that stopped part way left its building index behind.
	if err := w.St.PurgeSymbolIndexes(repo.ID); err != nil {
		return false, err
	}
	dir := w.RepoDir(repo.OwnerName, repo.Name)
	commit, err := gitutil.ResolveRef(dir, "refs/heads/"+repo.DefaultBranch)
	if err != nil {
		return false, nil // no default branch yet: nothing to index
	}
	tree, err := gitutil.ResolveTree(dir, commit)
	if err != nil {
		return false, err
	}
	if !force {
		if cur, err := w.St.SymbolIndexFor(repo.ID); err == nil && cur.Tree == tree {
			return false, nil
		}
		if recent, err := w.St.SymbolFailureRecent(repo.ID, tree, int(w.Backoff.Seconds())); err != nil || recent {
			return false, err
		}
	}
	x := store.SymbolIndex{RepoID: repo.ID, Commit: commit, Tree: tree, State: "ok"}
	syms, files, runErr := w.collect(ctx, dir, tree)
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	x.Files = files
	switch {
	case errors.Is(runErr, errSymbolCap):
		x.State, x.Note = "partial", fmt.Sprintf("stopped at %d symbols", w.MaxSymbols)
	case errors.Is(runErr, errByteBudget):
		x.State, x.Note = "partial", fmt.Sprintf("stopped at %d bytes of names and paths", w.MaxBytes)
	case errors.Is(runErr, context.DeadlineExceeded):
		x.State, x.Note = "partial", fmt.Sprintf("stopped after %s", w.MaxTime)
	case runErr != nil:
		if err := w.St.RecordSymbolFailure(repo.ID, tree, runErr.Error()); err != nil {
			return true, err
		}
		return true, fmt.Errorf("%s failed: %v", repo.Path(), runErr)
	}
	if x.ID, err = w.St.BeginSymbolIndex(repo.ID, commit, tree); err != nil {
		return false, err
	}
	for len(syms) > 0 {
		if ctx.Err() != nil {
			return false, ctx.Err() // the next run purges what was written
		}
		n := min(len(syms), w.ChunkRows)
		if err := w.St.AddSymbols(x.ID, syms[:n]); err != nil {
			return false, err
		}
		syms = syms[n:]
		if w.chunkHook != nil {
			w.chunkHook(x.ID)
		}
	}
	if err := w.St.PublishSymbolIndex(x); err != nil {
		return false, err
	}
	if err := w.St.PurgeSymbolIndexes(repo.ID); err != nil {
		return false, err
	}
	if x.State != "ok" {
		return false, fmt.Errorf("%s %s: %s", repo.Path(), x.State, x.Note)
	}
	return false, nil
}

var (
	errSymbolCap  = errors.New("symbol cap reached")
	errByteBudget = errors.New("byte budget reached")
)

// collect reads every indexable blob in tree and extracts its symbols,
// stopping at the symbol cap, the byte budget or the time bound with what
// it has.
func (w *Worker) collect(ctx context.Context, dir, tree string) ([]store.SymbolRow, int, error) {
	ctx, cancel := context.WithTimeout(ctx, w.MaxTime)
	defer cancel()
	blobs, err := gitutil.ListBlobs(ctx, dir, tree)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, ctx.Err()
		}
		return nil, 0, err
	}
	var paths, shas []string
	for _, b := range blobs {
		if b.Mode == "120000" || b.Mode == "160000" || Skip(b.Name, b.Size) {
			continue
		}
		paths = append(paths, b.Name)
		shas = append(shas, b.SHA)
	}
	var out []store.SymbolRow
	files, used := 0, 0
	var stop error
	err = gitutil.CatBlobs(ctx, dir, shas, func(i int, data []byte) bool {
		if gitutil.IsBinary(data) {
			return true
		}
		files++
		for _, s := range Extract(paths[i], data) {
			if len(out) >= w.MaxSymbols {
				stop = errSymbolCap
				return false
			}
			// The key is counted too: it is stored beside the name.
			n := len(s.Name) + len(s.Key) + len(paths[i])
			if used+n > w.MaxBytes {
				stop = errByteBudget
				return false
			}
			used += n
			out = append(out, store.SymbolRow{Name: s.Name, Key: s.Key, Kind: s.Kind, Path: paths[i], Line: s.Line})
		}
		return true
	})
	if stop != nil {
		return out, files, stop
	}
	if err != nil && ctx.Err() == nil {
		return nil, files, err
	}
	return out, files, err
}
