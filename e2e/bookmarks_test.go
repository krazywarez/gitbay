package e2e

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Bookmarks are the public "saved for later", separate from pins: they
// are something you do to someone else's repository, and the count is a
// signal of what people found worth returning to (#146).
func TestBookmarks(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/secret", "--private"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	// The facts bar, where the count shows, needs a repository with
	// commits: an empty one has nothing to size up.
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Bob bookmarks someone else's repository; read access is enough.
	if out, errOut, code := inst.ssh(t, bobKey, "", "repo", "bookmark", "alice/app"); code != 0 {
		t.Fatalf("bookmark: %s%s", out, errOut)
	}
	if out, _, _ := inst.ssh(t, bobKey, "", "repo", "bookmarks", "--json"); !strings.Contains(out, `"path":"alice/app"`) ||
		!strings.Contains(out, `"bookmarks":1`) {
		t.Fatalf("bookmark not listed with its count:\n%s", out)
	}
	// Bookmarking twice is not an error and does not double the count.
	inst.ssh(t, bobKey, "", "repo", "bookmark", "alice/app")
	if out, _, _ := inst.ssh(t, bobKey, "", "repo", "bookmarks", "--json"); !strings.Contains(out, `"bookmarks":1`) {
		t.Fatalf("a second bookmark changed the count:\n%s", out)
	}

	// A private repository a stranger cannot read is not found, the same
	// as everywhere.
	if _, _, code := inst.ssh(t, bobKey, "", "repo", "bookmark", "alice/secret"); code != 3 {
		t.Errorf("bookmarking an invisible repository exited %d, want 3", code)
	}

	// Pins stay separate: bookmarking does not pin.
	if out, _, _ := inst.ssh(t, bobKey, "", "dashboard", "--json"); strings.Contains(out, "alice/app") {
		t.Errorf("a bookmark showed up as a pin:\n%s", out)
	}

	// The count is public and shows on the repository page.
	if _, body := inst.get(t, "/alice/app"); !strings.Contains(body, "1</strong> bookmark") {
		t.Errorf("count not on the repo page:\n%s", body)
	}

	// The web toggles it, through the same command.
	bob := inst.login(t, bobKey)
	if status, _ := browserPost(t, bob, inst.base()+"/alice/app/bookmark", url.Values{}); status != 200 {
		t.Fatal("web unbookmark failed")
	}
	if out, _, _ := inst.ssh(t, bobKey, "", "repo", "bookmarks", "--json"); strings.Contains(out, "alice/app") {
		t.Fatalf("still bookmarked after the toggle:\n%s", out)
	}
	if status, _ := browserPost(t, bob, inst.base()+"/alice/app/bookmark", url.Values{}); status != 200 {
		t.Fatal("web bookmark failed")
	}
	if _, page := browserGet(t, bob, inst.base()+"/bookmarks"); !strings.Contains(page, "alice/app") {
		t.Fatalf("bookmarks page does not list it:\n%s", page)
	}

	// Unbookmarking something that is not bookmarked says so.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "unbookmark", "alice/app"); code != 3 {
		t.Errorf("unbookmarking what was never bookmarked exited %d, want 3", code)
	}
}

// A repository bookmarked while public and since made private drops out
// of the listing rather than leaking its existence.
func TestBookmarkOfRepoGonePrivate(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.ssh(t, aliceKey, "", "repo", "create", "alice/app")
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "bookmark", "alice/app"); code != 0 {
		t.Fatalf("bookmark: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "visibility", "alice/app", "private"); code != 0 {
		t.Fatalf("visibility: %s", errOut)
	}
	if out, _, _ := inst.ssh(t, bobKey, "", "repo", "bookmarks", "--json"); strings.Contains(out, "alice/app") {
		t.Fatalf("a repository gone private is still listed:\n%s", out)
	}
}
