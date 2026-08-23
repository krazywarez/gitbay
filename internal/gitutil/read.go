package gitutil

import (
	"bytes"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
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
	cmd := exec.Command("git", "-C", dir, "ls-tree", "-l", spec)
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
	cmd := exec.Command("git", "-C", dir, "cat-file", "blob", ref+":"+path)
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
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--verify", "--quiet", ref+"^{commit}")
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
	cmd := exec.Command("git", "-C", dir, "for-each-ref",
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

// Archive streams a tar.gz of ref to w.
func Archive(dir, ref, prefix string, w io.Writer) error {
	cmd := exec.Command("git", "-C", dir, "archive", "--format=tar.gz", "--prefix="+prefix+"/", ref)
	cmd.Stdout = w
	return cmd.Run()
}

// ShowPatch returns the stat+patch text for one commit.
func ShowPatch(dir, sha string, limit int64) (string, error) {
	cmd := exec.Command("git", "-C", dir, "show", "--stat", "--patch", "--format=", sha)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	data, _ := io.ReadAll(io.LimitReader(stdout, limit))
	io.Copy(io.Discard, stdout)
	if err := cmd.Wait(); err != nil {
		return "", fmt.Errorf("show %s: %w", sha, err)
	}
	return string(data), nil
}

// IsBinary reports whether data looks like binary content.
func IsBinary(data []byte) bool {
	return bytes.IndexByte(data, 0) >= 0
}
