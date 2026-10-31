package leaks

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/toolpath"
)

// Bounds on one scan. A push or a history bigger than these is scanned in
// part and reported as truncated rather than holding the push open: the
// hook runs while the pusher waits.
const (
	MaxBlobBytes = 1 << 20  // a larger file is skipped: generated, vendored, or data
	MaxScanBytes = 64 << 20 // total content read
	MaxBlobs     = 20000
	MaxFindings  = 100
)

// Finding is one match: which commit added the content, where, and which
// rule matched. Never the matched text.
type Finding struct {
	Commit string `json:"commit"`
	Path   string `json:"path"`
	Line   int    `json:"line"`
	Rule   string `json:"rule"`
}

// Report is the outcome of one scan. Truncated means a bound or the
// deadline stopped it before every blob was read; Error that git failed.
type Report struct {
	Findings  []Finding `json:"findings"`
	Blobs     int       `json:"blobs"`
	Truncated bool      `json:"truncated,omitempty"`
	Error     string    `json:"error,omitempty"`
}

type origin struct{ commit, path string }

// Scan reads the blobs the commits selected by revs add or change, as
// `git log` arguments (`<tip> --not --all` for a push, `--all` for a
// history), and reports where they match a rule. Merge commits are not
// diffed, so a secret introduced only in a conflict resolution is missed.
// The deadline on ctx ends the scan early as a truncation, not an error.
func Scan(ctx context.Context, dir string, revs ...string) Report {
	oids, origins, truncated, err := changedBlobs(ctx, dir, revs)
	rep := Report{Truncated: truncated}
	if err != nil {
		rep.Error = err.Error()
		return rep
	}
	if len(oids) == 0 {
		return rep
	}
	more, err := readBlobs(ctx, dir, oids, func(oid string, data []byte) bool {
		rep.Blobs++
		if binary(data) {
			return true
		}
		o := origins[oid]
		for _, h := range Find(data) {
			if len(rep.Findings) >= MaxFindings {
				rep.Truncated = true
				return false
			}
			rep.Findings = append(rep.Findings, Finding{Commit: o.commit, Path: o.path, Line: h.Line, Rule: h.Rule})
		}
		return true
	})
	rep.Truncated = rep.Truncated || more
	if err != nil {
		rep.Error = err.Error()
	}
	sort.Slice(rep.Findings, func(i, j int) bool {
		a, b := rep.Findings[i], rep.Findings[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})
	return rep
}

// changedBlobs lists the distinct blobs the selected commits add or
// modify, with the commit and path each was first seen under. Symlinks and
// submodules carry no content worth reading.
func changedBlobs(ctx context.Context, dir string, revs []string) ([]string, map[string]origin, bool, error) {
	args := append([]string{"-C", dir, "-c", "core.quotePath=false", "log", "--raw", "--no-abbrev",
		"--no-renames", "--format=commit %H"}, revs...)
	cmd := exec.CommandContext(ctx, toolpath.Look("git"), args...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, false, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, false, fmt.Errorf("git log: %w", err)
	}
	var (
		oids      []string
		origins   = map[string]origin{}
		commit    string
		truncated bool
	)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if sha, ok := strings.CutPrefix(line, "commit "); ok {
			commit = sha
			continue
		}
		meta, path, ok := strings.Cut(line, "\t")
		if !ok || !strings.HasPrefix(meta, ":") {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 5 || f[4] == "D" || (f[1] != "100644" && f[1] != "100755") {
			continue
		}
		if strings.HasPrefix(path, `"`) {
			if p, err := strconv.Unquote(path); err == nil {
				path = p
			}
		}
		oid := f[3]
		if _, seen := origins[oid]; seen {
			continue
		}
		if len(oids) >= MaxBlobs {
			truncated = true
			break
		}
		origins[oid] = origin{commit, path}
		oids = append(oids, oid)
	}
	// Stopping early leaves git writing into a pipe nobody reads; killing it
	// is how the early stop ends it, so that exit is not an error.
	if truncated {
		cmd.Process.Kill()
	}
	waitErr := cmd.Wait()
	switch {
	case ctx.Err() != nil:
		return oids, origins, true, nil
	case truncated:
		return oids, origins, true, nil
	case sc.Err() != nil:
		return nil, nil, false, sc.Err()
	case waitErr != nil:
		return nil, nil, false, fmt.Errorf("git log: %w", waitErr)
	}
	return oids, origins, false, nil
}

// readBlobs streams oids through one `cat-file --batch`, handing each blob
// within MaxBlobBytes to fn until fn returns false. It reports whether it
// stopped before the end, on a bound or the deadline.
func readBlobs(ctx context.Context, dir string, oids []string, fn func(oid string, data []byte) bool) (bool, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(ctx, toolpath.Look("git"), "-C", dir, "cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return false, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("cat-file --batch: %w", err)
	}
	defer cmd.Wait() // after cancel, which ends git on an early return
	// Fed from another goroutine: the pipe holds fewer names than a large
	// push has, and git blocks writing output nobody is reading yet.
	go func() {
		defer stdin.Close()
		w := bufio.NewWriter(stdin)
		for _, oid := range oids {
			if _, err := fmt.Fprintln(w, oid); err != nil {
				return
			}
		}
		w.Flush()
	}()
	r := bufio.NewReader(stdout)
	var total int64
	for range oids {
		header, err := r.ReadString('\n')
		if err != nil {
			if ctx.Err() != nil {
				return true, nil
			}
			return true, fmt.Errorf("cat-file --batch: %w", err)
		}
		f := strings.Fields(header)
		if len(f) == 2 && f[1] == "missing" {
			continue
		}
		if len(f) != 3 {
			return true, fmt.Errorf("cat-file --batch: unexpected %q", strings.TrimSpace(header))
		}
		size, err := strconv.ParseInt(f[2], 10, 64)
		if err != nil {
			return true, fmt.Errorf("cat-file --batch: bad size in %q", strings.TrimSpace(header))
		}
		if size > MaxBlobBytes {
			if _, err := r.Discard(int(size) + 1); err != nil {
				return true, readErr(ctx, err)
			}
			continue
		}
		if total += size; total > MaxScanBytes {
			return true, nil
		}
		data := make([]byte, size)
		if _, err := io.ReadFull(r, data); err != nil {
			return true, readErr(ctx, err)
		}
		if _, err := r.Discard(1); err != nil {
			return true, readErr(ctx, err)
		}
		if !fn(f[0], data) {
			return true, nil
		}
	}
	return false, nil
}

// readErr is nil when the deadline caused the failed read: a truncation.
func readErr(ctx context.Context, err error) error {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return nil
	}
	return fmt.Errorf("cat-file --batch: %w", err)
}
