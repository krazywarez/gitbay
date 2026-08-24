package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBlameView(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")

	// Two commits attributing different lines of the same file.
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\ntwo\nthree\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "first lines")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\nTWO CHANGED\nthree\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "change line two")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	status, body := inst.get(t, "/alice/app/blame/main/f.txt")
	if status != 200 {
		t.Fatalf("blame page: %d", status)
	}
	// Three hunks (first commit / second commit / first commit), each with
	// its commit subject linked and a signature badge.
	if got := strings.Count(body, `class="blamehunk"`); got != 3 {
		t.Fatalf("expected 3 hunks, got %d", got)
	}
	for _, want := range []string{
		">first lines</a>", ">change line two</a>",
		`<span class="lineno">2</span>TWO CHANGED`,
		`<span class="lineno">3</span>three`,
		`badge badge-unsigned`,
		"/alice/app/commit/",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q on blame page", want)
		}
	}

	// The blob page links to blame.
	_, blob := inst.get(t, "/alice/app/blob/main/f.txt")
	if !strings.Contains(blob, `/alice/app/blame/main/f.txt">blame</a>`) {
		t.Error("blob page missing blame link")
	}

	// Pagination: 1001 lines means two pages; page 2 holds only the last line.
	var big strings.Builder
	for i := 1; i <= 1001; i++ {
		fmt.Fprintf(&big, "line %d\n", i)
	}
	os.WriteFile(filepath.Join(dir, "big.txt"), []byte(big.String()), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "big file")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	_, body = inst.get(t, "/alice/app/blame/main/big.txt")
	if !strings.Contains(body, "page 1 of 2") || !strings.Contains(body, `<span class="lineno">1000</span>line 1000`) ||
		strings.Contains(body, ">line 1001") {
		t.Fatal("page 1 wrong")
	}
	_, body = inst.get(t, "/alice/app/blame/main/big.txt?page=2")
	if !strings.Contains(body, `<span class="lineno">1001</span>line 1001`) ||
		strings.Contains(body, ">line 1000<") || !strings.Contains(body, "earlier lines") {
		t.Fatal("page 2 wrong")
	}

	// Nonexistent path 404s.
	if status, _ := inst.get(t, "/alice/app/blame/main/nope.txt"); status != 404 {
		t.Fatalf("missing file blame: %d", status)
	}
}
