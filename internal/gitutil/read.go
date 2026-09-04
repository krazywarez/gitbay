package gitutil

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/toolpath"
)

type TreeEntry struct {
	Mode string
	Type string // blob | tree
	SHA  string
	Size int64 // -1 for trees
	Name string
}

// ListTree lists one level of the tree at ref:path.
func ListTree(dir, ref, path string) ([]TreeEntry, error) {
	spec := ref
	if path != "" {
		spec = ref + ":" + path
	}
	cmd := exec.Command(toolpath.Look("git"), "-C", dir, "ls-tree", "-l", "--end-of-options", spec)
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("ls-tree %s: %w", spec, err)
	}
	var entries []TreeEntry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		// <mode> <type> <sha> <size>\t<name>
		meta, name, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 4 {
			continue
		}
		size := int64(-1)
		if f[3] != "-" {
			size, _ = strconv.ParseInt(f[3], 10, 64)
		}
		entries = append(entries, TreeEntry{Mode: f[0], Type: f[1], SHA: f[2], Size: size, Name: name})
	}
	return entries, nil
}

// ReadBlob returns the contents of ref:path, capped at limit bytes.
func ReadBlob(dir, ref, path string, limit int64) ([]byte, error) {
	cmd := exec.Command(toolpath.Look("git"), "-C", dir, "cat-file", "blob", "--end-of-options", ref+":"+path)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(stdout, limit))
	io.Copy(io.Discard, stdout) // drain so git exits cleanly
	if werr := cmd.Wait(); werr != nil {
		return nil, fmt.Errorf("cat-file blob %s:%s: %w", ref, path, werr)
	}
	return data, err
}

// ResolveRef resolves a ref or sha to a full commit sha; errors if absent.
func ResolveRef(dir, ref string) (string, error) {
	cmd := exec.Command(toolpath.Look("git"), "-C", dir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref+"^{commit}")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("unknown ref %q", ref)
	}
	return strings.TrimSpace(string(out)), nil
}

type Ref struct {
	Name string
	SHA  string
}

// Refs lists branches or tags; kind is "heads" or "tags".
func Refs(dir, kind string) ([]Ref, error) {
	cmd := exec.Command(toolpath.Look("git"), "-C", dir, "for-each-ref",
		"--format=%(refname:short) %(objectname)", "refs/"+kind)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var refs []Ref
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if name, sha, ok := strings.Cut(line, " "); ok {
			refs = append(refs, Ref{Name: name, SHA: sha})
		}
	}
	return refs, nil
}

// Bounds on one archive: a request cannot hold git and a goroutine for
// longer than this, or stream more than this, however large the
// repository or however slowly the client reads (#124).
const archiveTimeout = 2 * time.Minute

var MaxArchiveBytes int64 = 512 << 20

var ErrArchiveTooLarge = errors.New("archive exceeds the size limit")

// Archive streams a tar.gz of ref to w, within archiveTimeout and
// MaxArchiveBytes. Past either, git is killed and the error says which.
func Archive(dir, ref, prefix string, w io.Writer) error {
	ctx, cancel := context.WithTimeout(context.Background(), archiveTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, toolpath.Look("git"), "-C", dir, "archive", "--format=tar.gz", "--prefix="+prefix+"/", "--end-of-options", ref)
	lw := &cappedWriter{w: w, left: MaxArchiveBytes, stop: cancel}
	cmd.Stdout = lw
	err := cmd.Run()
	switch {
	case lw.exceeded:
		return ErrArchiveTooLarge
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return fmt.Errorf("git archive: timed out after %s", archiveTimeout)
	}
	return err
}

// cappedWriter passes bytes through until the cap, then stops the
// producer instead of writing a truncated tail.
type cappedWriter struct {
	w        io.Writer
	left     int64
	stop     func()
	exceeded bool
}

func (c *cappedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > c.left {
		c.exceeded = true
		c.stop()
		return 0, ErrArchiveTooLarge
	}
	c.left -= int64(len(p))
	return c.w.Write(p)
}

// ShowPatch returns the stat+patch text for one commit.
func ShowPatch(dir, sha string, limit int64) (patch string, truncated bool, err error) {
	cmd := exec.Command(toolpath.Look("git"), "-C", dir, "show", "--stat", "--patch", "--format=", "--end-of-options", sha)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", false, err
	}
	if err := cmd.Start(); err != nil {
		return "", false, err
	}
	// One byte past the limit says whether there was more.
	data, _ := io.ReadAll(io.LimitReader(stdout, limit+1))
	io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		return "", false, fmt.Errorf("show %s: %w", sha, err)
	}
	data, truncated = cutAtLine(data, limit)
	return string(data), truncated, nil
}

// IsBinary reports whether data looks like binary content.
func IsBinary(data []byte) bool {
	return bytes.IndexByte(data, 0) >= 0
}
