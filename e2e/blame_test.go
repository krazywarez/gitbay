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

	// Blame is a control command, so the CLI and the JSON API attribute
	// lines too — the web is one rendering of it, not the only one.
	out, errOut, code := inst.ssh(t, aliceKey, "", "repo", "blame", "alice/app", "f.txt", "--json")
	if code != 0 {
		t.Fatalf("repo blame: %s", errOut)
	}
	for _, want := range []string{
		`"total_lines":3`, `"start_line":1`,
		`"summary":"first lines"`, `"summary":"change line two"`,
		`"TWO CHANGED"`, `"author_name":"t"`, // the fixture's git author
	} {
		if !strings.Contains(out, want) {
			t.Errorf("repo blame output missing %q: %s", want, out)
		}
	}

	// A line range is a window on the same attribution.
	out, _, code = inst.ssh(t, aliceKey, "", "repo", "blame", "alice/app", "f.txt",
		"--from", "2", "--to", "2", "--json")
	if code != 0 || !strings.Contains(out, `"from":2`) || !strings.Contains(out, `"to":2`) {
		t.Fatalf("ranged blame: %s", out)
	}
	if strings.Contains(out, `"one"`) || strings.Contains(out, `"three"`) {
		t.Errorf("ranged blame leaked lines outside the window: %s", out)
	}

	// Binary files have nothing to attribute, and say so rather than 500.
	os.WriteFile(filepath.Join(dir, "logo.bin"), []byte{0, 1, 2, 0, 3}, 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "binary")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "blame", "alice/app", "logo.bin"); code == 0 ||
		!strings.Contains(errOut, "binary") {
		t.Errorf("binary blame: exit %d, %s", code, errOut)
	}

	// A stranger cannot attribute a private repository's lines.
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "bob",
		"--key", bobKey+".pub", "--email", "bob@example.test", "--verified")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "visibility", "alice/app", "private"); code != 0 {
		t.Fatal("visibility private")
	}
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "blame", "alice/app", "f.txt"); code == 0 {
		t.Error("a stranger blamed a private repository's file")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "visibility", "alice/app", "public"); code != 0 {
		t.Fatal("visibility public")
	}

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
