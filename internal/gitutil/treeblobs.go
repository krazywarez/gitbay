package gitutil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/toolpath"
)

// ListBlobs lists every blob in a tree, recursively, with its size. Name
// is the path from the tree's root. Paths are read NUL-separated, so no
// file name is quoted or split.
func ListBlobs(ctx context.Context, dir, tree string) ([]TreeEntry, error) {
	out, err := exec.CommandContext(ctx, toolpath.Look("git"), "-C", dir,
		"ls-tree", "-r", "-l", "-z", "--end-of-options", tree).Output()
	if err != nil {
		return nil, fmt.Errorf("ls-tree %s: %w", tree, err)
	}
	var entries []TreeEntry
	for _, rec := range bytes.Split(out, []byte{0}) {
		meta, name, ok := strings.Cut(string(rec), "\t")
		if !ok {
			continue
		}
		f := strings.Fields(meta)
		if len(f) != 4 || f[1] != "blob" {
			continue
		}
		// ls-tree prints a size of BAD for a blob it cannot read.
		size, err := strconv.ParseInt(f[3], 10, 64)
		if err != nil {
			return nil, fmt.Errorf("ls-tree %s: cannot read %s", tree, name)
		}
		entries = append(entries, TreeEntry{Mode: f[0], Type: f[1], SHA: f[2], Size: size, Name: name})
	}
	return entries, nil
}

// CatBlobs reads blobs by id through one `git cat-file --batch`, calling
// fn with each one's index in shas and its contents, in order. fn
// returning false stops the read. Cancelling parent stops it too, and is
// reported as parent's error.
func CatBlobs(parent context.Context, dir string, shas []string, fn func(i int, data []byte) bool) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	cmd := exec.CommandContext(ctx, toolpath.Look("git"), "-C", dir, "cat-file", "--batch")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	go func() {
		w := bufio.NewWriter(stdin)
		for _, s := range shas {
			if _, err := fmt.Fprintln(w, s); err != nil {
				break
			}
		}
		w.Flush()
		stdin.Close()
	}()
	r := bufio.NewReader(stdout)
	readErr := func() error {
		for i := range shas {
			header, err := r.ReadString('\n')
			if err != nil {
				return err
			}
			// <sha> <type> <size>, or <sha> missing
			f := strings.Fields(header)
			if len(f) != 3 {
				return fmt.Errorf("cat-file: %s", strings.TrimSpace(header))
			}
			size, err := strconv.ParseInt(f[2], 10, 64)
			if err != nil {
				return fmt.Errorf("cat-file: %s", strings.TrimSpace(header))
			}
			data := make([]byte, size+1) // the object and its trailing newline
			if _, err := io.ReadFull(r, data); err != nil {
				return err
			}
			if !fn(i, data[:size]) {
				return errStopped
			}
		}
		return nil
	}()
	if readErr != nil {
		cancel()
	}
	io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	switch {
	case readErr == errStopped:
		return nil
	case parent.Err() != nil:
		return parent.Err()
	case readErr != nil:
		return readErr
	}
	return waitErr
}

var errStopped = errors.New("stopped")
