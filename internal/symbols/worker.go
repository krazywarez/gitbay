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

// Bounds on one index run.
const (
	// DefaultMaxSymbols is where a repository's index stops growing.
	DefaultMaxSymbols = 200_000
	// DefaultMaxTime is how long one run may take.
	DefaultMaxTime = 2 * time.Minute
)

// Worker builds the index for each repository that has asked for one:
// post-receive and the merge path ask after the default branch moves, and
// `admin symbols reindex` asks with force. One repository at a time.
type Worker struct {
	St         *store.Store
	RepoDir    func(owner, name string) string
	Tick       time.Duration
	MaxSymbols int
	MaxTime    time.Duration
}

func New(st *store.Store, repoDir func(owner, name string) string) *Worker {
	tick := 5 * time.Second
	if v := os.Getenv("GITBAY_SYMBOLS_TICK"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			tick = d
		}
	}
	return &Worker{St: st, RepoDir: repoDir, Tick: tick,
		MaxSymbols: DefaultMaxSymbols, MaxTime: DefaultMaxTime}
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

// Sweep handles every waiting request once. A request is cleared whatever
// the outcome, so a repository whose index fails is not retried until
// it is asked for again.
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
		err := w.Index(ctx, req.RepoID, req.Force)
		if ctx.Err() != nil {
			return // shutting down: the request stays for the next start
		}
		if err != nil {
			slog.Warn("symbols: indexing", "repo", req.RepoID, "err", err)
		}
		w.St.DoneSymbolRequest(req)
	}
}

// Index brings one repository's index up to its default branch's head.
// A head whose tree is already indexed is left alone unless force is set.
// The error is for the log: an index that could not be built is recorded
// as failed, and one cut short by a bound as partial.
func (w *Worker) Index(ctx context.Context, repoID int64, force bool) error {
	repo, err := w.St.RepoByID(repoID)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	dir := w.RepoDir(repo.OwnerName, repo.Name)
	commit, err := gitutil.ResolveRef(dir, "refs/heads/"+repo.DefaultBranch)
	if err != nil {
		return nil // no default branch yet: nothing to index
	}
	tree, err := gitutil.ResolveTree(dir, commit)
	if err != nil {
		return err
	}
	if cur, err := w.St.SymbolIndexFor(repo.ID); err == nil && cur.Tree == tree && !force {
		return nil
	}
	x := store.SymbolIndex{RepoID: repo.ID, Commit: commit, Tree: tree, State: "ok"}
	syms, files, runErr := w.collect(ctx, dir, tree)
	if ctx.Err() != nil {
		return ctx.Err()
	}
	x.Files = files
	switch {
	case errors.Is(runErr, errSymbolCap):
		x.State, x.Note = "partial", fmt.Sprintf("stopped at %d symbols", w.MaxSymbols)
	case errors.Is(runErr, context.DeadlineExceeded):
		x.State, x.Note = "partial", fmt.Sprintf("stopped after %s", w.MaxTime)
	case runErr != nil:
		x.State, x.Note, syms = "failed", runErr.Error(), nil
	}
	if _, err := w.St.ReplaceSymbolIndex(x, syms); err != nil {
		return err
	}
	if x.State != "ok" {
		return fmt.Errorf("%s %s: %s", repo.Path(), x.State, x.Note)
	}
	return nil
}

var errSymbolCap = errors.New("symbol cap reached")

// collect reads every indexable blob in tree and extracts its symbols,
// stopping at the symbol cap or the time bound with what it has.
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
	files := 0
	capped := false
	err = gitutil.CatBlobs(ctx, dir, shas, func(i int, data []byte) bool {
		if gitutil.IsBinary(data) {
			return true
		}
		files++
		for _, s := range Extract(paths[i], data) {
			if len(out) >= w.MaxSymbols {
				capped = true
				return false
			}
			out = append(out, store.SymbolRow{Name: s.Name, Key: s.Key, Kind: s.Kind, Path: paths[i], Line: s.Line})
		}
		return true
	})
	if capped {
		return out, files, errSymbolCap
	}
	if err != nil && ctx.Err() == nil {
		return nil, files, err
	}
	return out, files, err
}
