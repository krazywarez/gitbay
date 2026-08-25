package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWikis(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/secretive", "--private"); code != 0 {
		t.Fatal("private repo create failed")
	}

	// No wiki yet: the tab is absent, the page shows the push hint, and
	// cloning the companion says so.
	_, body := inst.get(t, "/alice/app")
	if strings.Contains(body, ">wiki<") {
		t.Fatal("wiki tab shown with no wiki")
	}
	_, body = inst.get(t, "/alice/app/wiki")
	if !strings.Contains(body, "no wiki yet") {
		t.Fatal("missing-wiki hint absent")
	}
	env := inst.gitEnv(aliceKey)
	if out, code := gitRun(t, t.TempDir(), env, "clone", inst.sshURL("alice/app.wiki"), "w"); code == 0 || !strings.Contains(out, "no wiki yet") {
		t.Fatalf("clone of absent wiki: %d\n%s", code, out)
	}

	// First push creates the wiki. A reader without write cannot push it.
	work := t.TempDir()
	mustGit(t, work, env, "init", "-q", "-b", "main", "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "Home.md"), []byte(
		"# welcome\n\nsee [Setup](Setup.md) and ![shot](shot.png)\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "Setup.org"), []byte("* setup\n\nsteps here\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "shot.png"), []byte{0x89, 0x50, 0x4e, 0x47}, 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "wiki start")
	mustGit(t, dir, env, "push", "-q", inst.sshURL("alice/app.wiki"), "main")

	benv := inst.gitEnv(bobKey)
	if out, code := gitRun(t, dir, benv, "push", inst.sshURL("alice/app.wiki"), "main"); code == 0 && !strings.Contains(out, "denied") {
		t.Fatalf("reader pushed the wiki: %d\n%s", code, out)
	}

	// Rendering: home resolves, tab appears, links rewrite to wiki pages
	// and images to the wiki raw route; org pages render too.
	_, body = inst.get(t, "/alice/app")
	if !strings.Contains(body, ">wiki<") {
		t.Fatal("wiki tab missing after push")
	}
	_, body = inst.get(t, "/alice/app/wiki")
	if !strings.Contains(body, "welcome") ||
		!strings.Contains(body, `href="/alice/app/wiki/Setup"`) ||
		!strings.Contains(body, `src="/alice/app/wiki/_raw/shot.png"`) {
		t.Fatalf("wiki home rendering:\n%s", body)
	}
	_, body = inst.get(t, "/alice/app/wiki/Setup")
	if !strings.Contains(body, "steps here") {
		t.Fatal("org wiki page missing")
	}
	if status, _ := inst.get(t, "/alice/app/wiki/Nope"); status != 404 {
		t.Fatalf("missing page: %d", status)
	}
	// The raw route serves the image bytes.
	status, raw := inst.get(t, "/alice/app/wiki/_raw/shot.png")
	if status != 200 || !strings.HasPrefix(raw, "\x89PNG") {
		t.Fatalf("wiki raw: %d", status)
	}

	// 404-parity: a private repo's wiki is invisible, over web and git.
	mustGit(t, dir, env, "push", "-q", inst.sshURL("alice/secretive.wiki"), "main")
	if status, _ := inst.get(t, "/alice/secretive/wiki"); status != 404 {
		t.Fatalf("private wiki page: %d", status)
	}
	if out, code := gitRun(t, t.TempDir(), benv, "clone", inst.sshURL("alice/secretive.wiki"), "x"); code == 0 || !strings.Contains(out, "not found") {
		t.Fatalf("private wiki clone by outsider: %d\n%s", code, out)
	}

	// Repo names ending .wiki are refused (companion namespace).
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/notes.wiki"); code != 2 || !strings.Contains(errOut, "reserved") {
		t.Fatalf(".wiki name allowed: %d %s", code, errOut)
	}

	// Deleting the repo removes the wiki companion.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "delete", "alice/secretive", "--yes"); code != 0 {
		t.Fatal("repo delete failed")
	}
	if _, err := os.Stat(filepath.Join(inst.root, "repos", "alice", "secretive.wiki.git")); err == nil {
		t.Fatal("wiki survived repo delete")
	}
}
