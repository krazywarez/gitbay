package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// pageOf unmarshals a paged --json response.
type pageOf[T any] struct {
	Data struct {
		Items []T    `json:"items"`
		Next  string `json:"next"`
	} `json:"data"`
}

func decodePage[T any](t *testing.T, out string) ([]T, string) {
	t.Helper()
	var p pageOf[T]
	if err := json.Unmarshal([]byte(out), &p); err != nil {
		t.Fatalf("bad json: %v\n%s", err, out)
	}
	return p.Data.Items, p.Data.Next
}

// Cursor pagination on the list commands: opaque cursors, stable pages,
// and the bare-array shape untouched when the flags are absent.
func TestCursorPagination(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	for _, name := range []string{"'one'", "'two'", "'three'"} {
		if _, _, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", name); code != 0 {
			t.Fatal("issue create failed")
		}
	}

	// Bare list: still a plain array.
	out, _, code := inst.ssh(t, aliceKey, "", "issue", "list", "alice/app", "--json")
	if code != 0 {
		t.Fatal("issue list failed")
	}
	var bare struct {
		Data []struct {
			Number int64 `json:"number"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &bare); err != nil || len(bare.Data) != 3 {
		t.Fatalf("bare issue list: %v\n%s", err, out)
	}

	type numbered struct {
		Number int64 `json:"number"`
	}
	// Page 1: newest two, with a cursor onward.
	out, _, code = inst.ssh(t, aliceKey, "", "issue", "list", "alice/app", "--limit", "2", "--json")
	if code != 0 {
		t.Fatal("paged issue list failed")
	}
	items, next := decodePage[numbered](t, out)
	if len(items) != 2 || items[0].Number != 3 || items[1].Number != 2 || next == "" {
		t.Fatalf("page 1: %+v next=%q", items, next)
	}
	// Page 2: the remainder, no cursor.
	out, _, code = inst.ssh(t, aliceKey, "", "issue", "list", "alice/app", "--limit", "2", "--cursor", next, "--json")
	if code != 0 {
		t.Fatal("cursor follow failed")
	}
	items, next = decodePage[numbered](t, out)
	if len(items) != 1 || items[0].Number != 1 || next != "" {
		t.Fatalf("page 2: %+v next=%q", items, next)
	}

	// A cursor from one command is refused by another.
	out, _, code = inst.ssh(t, aliceKey, "", "issue", "list", "alice/app", "--limit", "2", "--json")
	items, next = decodePage[numbered](t, out)
	if _, _, code := inst.ssh(t, aliceKey, "", "mr", "list", "alice/app", "--cursor", next); code != 2 {
		t.Fatal("foreign cursor accepted")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "list", "alice/app", "--cursor", "garbage!"); code != 2 {
		t.Fatal("garbage cursor accepted")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "list", "alice/app", "--limit", "0"); code != 2 {
		t.Fatal("limit 0 accepted")
	}

	// MR pagination pages by number the same way.
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	for i := 1; i <= 2; i++ {
		branch := fmt.Sprintf("feat%d", i)
		mustGit(t, dir, env, "checkout", "-q", "-b", branch, "main")
		os.WriteFile(filepath.Join(dir, "a.txt"), []byte(fmt.Sprintf("a\n%d\n", i)), 0o644)
		mustGit(t, dir, env, "add", ".")
		mustGit(t, dir, env, "commit", "-q", "-m", branch)
		mustGit(t, dir, env, "push", "-q", "origin", branch)
		if _, _, code := inst.ssh(t, aliceKey, "", "mr", "create", "alice/app",
			"--source", branch, "--target", "main", "--title", branch); code != 0 {
			t.Fatal("mr create failed")
		}
	}
	out, _, code = inst.ssh(t, aliceKey, "", "mr", "list", "alice/app", "--limit", "1", "--json")
	if code != 0 {
		t.Fatal("paged mr list failed")
	}
	items, next = decodePage[numbered](t, out)
	if len(items) != 1 || items[0].Number != 2 || next == "" {
		t.Fatalf("mr page 1: %+v next=%q", items, next)
	}
	out, _, code = inst.ssh(t, aliceKey, "", "mr", "list", "alice/app", "--limit", "1", "--cursor", next, "--json")
	items, next = decodePage[numbered](t, out)
	if len(items) != 1 || items[0].Number != 1 || next != "" {
		t.Fatalf("mr page 2: %+v next=%q", items, next)
	}

	// Repo pagination pages by path.
	for _, name := range []string{"alice/butter", "alice/cheese"} {
		if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", name); code != 0 {
			t.Fatal("repo create failed")
		}
	}
	type pathed struct {
		Path string `json:"path"`
	}
	out, _, code = inst.ssh(t, aliceKey, "", "repo", "list", "--limit", "2", "--json")
	if code != 0 {
		t.Fatal("paged repo list failed")
	}
	rItems, next := decodePage[pathed](t, out)
	if len(rItems) != 2 || rItems[0].Path != "alice/app" || rItems[1].Path != "alice/butter" || next == "" {
		t.Fatalf("repo page 1: %+v next=%q", rItems, next)
	}
	out, _, code = inst.ssh(t, aliceKey, "", "repo", "list", "--limit", "2", "--cursor", next, "--json")
	rItems, next = decodePage[pathed](t, out)
	if len(rItems) != 1 || rItems[0].Path != "alice/cheese" || next != "" {
		t.Fatalf("repo page 2: %+v next=%q", rItems, next)
	}

	// The feed pages by event id, newest first.
	type feedRow struct {
		Kind string `json:"kind"`
		Repo string `json:"repo"`
	}
	out, _, code = inst.ssh(t, aliceKey, "", "feed", "--limit", "2", "--json")
	if code != 0 {
		t.Fatal("paged feed failed")
	}
	fItems, next := decodePage[feedRow](t, out)
	if len(fItems) != 2 || next == "" {
		t.Fatalf("feed page 1: %+v next=%q", fItems, next)
	}
	if fItems[0].Kind != "mr.created" || fItems[0].Repo != "alice/app" {
		t.Fatalf("feed head: %+v", fItems[0])
	}
	seen := len(fItems)
	for next != "" {
		out, _, code = inst.ssh(t, aliceKey, "", "feed", "--limit", "2", "--cursor", next, "--json")
		if code != 0 {
			t.Fatal("feed follow failed")
		}
		fItems, next = decodePage[feedRow](t, out)
		seen += len(fItems)
		if seen > 20 {
			t.Fatal("feed cursor loop")
		}
	}
	// 3 issues + 2 MRs created above.
	if seen != 5 {
		t.Fatalf("feed walked %d events", seen)
	}

	// Bare feed: plain array, most recent first.
	out, _, code = inst.ssh(t, aliceKey, "", "feed", "--json")
	if code != 0 {
		t.Fatal("feed failed")
	}
	var bareFeed struct {
		Data []feedRow `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &bareFeed); err != nil || len(bareFeed.Data) != 5 {
		t.Fatalf("bare feed: %v\n%s", err, out)
	}
}
