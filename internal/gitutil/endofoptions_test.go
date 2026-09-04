package gitutil

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// A ref reaching gitutil is user input: a URL segment or an argument. Every
// git invocation ends option parsing before it, so a ref shaped like an
// option is a bad revision, never a flag (#135). The payload here is an
// option several subcommands accept, whose effect would be a file.
func TestRefsAreNotOptions(t *testing.T) {
	dir := t.TempDir()
	git(t, dir, "init", "-q", "-b", "main")
	write(t, dir, "f.txt", "hi\n")
	git(t, dir, "add", ".")
	git(t, dir, "commit", "-q", "-m", "base")
	pwn := filepath.Join(t.TempDir(), "pwned")
	ref := "--output=" + pwn

	var sink bytes.Buffer
	calls := map[string]func() error{
		"CountCommits": func() error { CountCommits(dir, ref); return nil },
		"Contributors": func() error { Contributors(dir, ref); return nil },
		"Languages":    func() error { Languages(dir, ref, func(string) string { return "x" }); return nil },
		"RevList":      func() error { _, err := RevList(dir, ref, 1); return err },
		"RevListPath":  func() error { _, err := RevListPath(dir, ref, "f.txt", 1); return err },
		"PeelToCommit": func() error { _, err := PeelToCommit(dir, ref); return err },
		"ReadCommit":   func() error { _, err := ReadCommit(dir, ref); return err },
		"ListTree":     func() error { _, err := ListTree(dir, ref, ""); return err },
		"ReadBlob":     func() error { _, err := ReadBlob(dir, ref, "f.txt", 1<<20); return err },
		"ResolveRef":   func() error { _, err := ResolveRef(dir, ref); return err },
		"Archive":      func() error { return Archive(dir, ref, "x", &sink) },
		"Grep":         func() error { _, err := Grep(dir, ref, "hi", 10); return err },
		"MergeBase":    func() error { _, err := MergeBase(dir, ref, "main"); return err },
		"Diff":         func() error { _, _, err := Diff(dir, ref, "main", 1<<20); return err },
		"DiffFiles":    func() error { _, err := DiffFiles(dir, ref, "main"); return err },
		"RevListRange": func() error { _, err := RevListRange(dir, "main", ref); return err },
		"Blame":        func() error { _, err := Blame(dir, ref, "f.txt", 1, 1); return err },
		"TipCommit":    func() error { TipCommit(dir, ref); return nil },
		"StatPath":     func() error { StatPath(dir, ref, "f.txt"); return nil },
	}
	for name, call := range calls {
		call()
		if _, err := os.Stat(pwn); err == nil {
			t.Fatalf("%s: ref %q was parsed as an option and wrote a file", name, ref)
		}
	}
	if _, err := ResolveRef(dir, ref); err == nil {
		t.Fatal("option-shaped ref resolved")
	}
}
