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
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/secretive", "--private"); code != 0 {
		t.Fatal("private repo create failed")
	}

	// No wiki yet: the tab is absent, the page shows the missing hint, and
	// listing reports no wiki rather than erroring.
	_, body := inst.get(t, "/alice/app")
	if strings.Contains(body, ">Wiki<") {
		t.Fatal("wiki tab shown with no wiki")
	}
	_, body = inst.get(t, "/alice/app/wiki")
	if !strings.Contains(body, "no wiki yet") {
		t.Fatal("missing-wiki hint absent")
	}
	if out, errOut, code := inst.ssh(t, aliceKey, "", "wiki", "list", "alice/app", "--json"); code != 0 {
		t.Fatalf("wiki list on a repo without one: %s", errOut)
	} else if !strings.Contains(out, `"pages":[]`) {
		t.Errorf("wiki list on a repo without one returned pages: %s", out)
	}

	// Pages are ordinary files: pushing them creates the wiki.
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "init", "-q", "-b", "main", "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay", "wiki"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "wiki", "Home.md"), []byte(
		"# welcome\n\nsee [Setup](Setup.md) and ![shot](shot.png)\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitbay", "wiki", "Setup.org"), []byte("* setup\n\nsteps here\n"), 0o644)
	os.WriteFile(filepath.Join(dir, ".gitbay", "wiki", "shot.png"), []byte{0x89, 0x50, 0x4e, 0x47}, 0o644)
	os.WriteFile(filepath.Join(dir, "top.txt"), []byte("not part of the wiki\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "wiki start")
	mustGit(t, dir, env, "push", "-q", inst.sshURL("alice/app"), "main")

	benv := inst.gitEnv(bobKey)
	if out, code := gitRun(t, dir, benv, "push", inst.sshURL("alice/app"), "main"); code == 0 && !strings.Contains(out, "denied") {
		t.Fatalf("reader pushed the repository: %d\n%s", code, out)
	}

	// Rendering: home resolves, tab appears, links rewrite to wiki pages
	// and images to the wiki raw route; org pages render too.
	_, body = inst.get(t, "/alice/app")
	if !strings.Contains(body, ">Wiki<") {
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
	// The raw route cannot climb out of .gitbay/wiki. A literal ".." is
	// caught by the mux's own path cleaning, which would make this pass
	// vacuously; percent-encoding it reaches the handler with real ".."
	// segments in PathValue, which is what the guard has to refuse.
	if status, body := inst.get(t, "/alice/app/wiki/_raw/%2e%2e/%2e%2e/top.txt"); status == 200 {
		t.Fatalf("wiki raw escaped .gitbay/wiki: %d\n%s", status, body)
	}

	// A wiki is readable from every surface, not just a browser: the
	// commands are what the web dispatches, and what the CLI and the
	// JSON API reach.
	out, errOut, code := inst.ssh(t, aliceKey, "", "wiki", "list", "alice/app", "--json")
	if code != 0 {
		t.Fatalf("wiki list: %s", errOut)
	}
	if !strings.Contains(out, `"Home"`) || !strings.Contains(out, `"Setup"`) {
		t.Errorf("wiki list pages: %s", out)
	}
	if !strings.Contains(out, `"home":"Home"`) {
		t.Errorf("wiki list did not name the landing page: %s", out)
	}
	// shot.png is not a page.
	if strings.Contains(out, "shot") {
		t.Errorf("wiki list included a non-page file: %s", out)
	}

	// Named page, and the landing page when none is named.
	out, _, code = inst.ssh(t, aliceKey, "", "wiki", "show", "alice/app", "Setup", "--json")
	if code != 0 || !strings.Contains(out, "steps here") {
		t.Errorf("wiki show Setup: %s", out)
	}
	out, _, code = inst.ssh(t, aliceKey, "", "wiki", "show", "alice/app", "--json")
	if code != 0 || !strings.Contains(out, "welcome") {
		t.Errorf("wiki show default page: %s", out)
	}
	// An extension is accepted and ignored, as the web's routes do.
	if _, _, code := inst.ssh(t, aliceKey, "", "wiki", "show", "alice/app", "Setup.org"); code != 0 {
		t.Error("wiki show rejected a page named with its extension")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "wiki", "show", "alice/app", "Nope"); code == 0 {
		t.Error("a missing wiki page resolved")
	}
	// A page name cannot climb out of the wiki.
	if _, _, code := inst.ssh(t, aliceKey, "", "wiki", "show", "alice/app", "../../etc/passwd"); code == 0 {
		t.Error("wiki show escaped the repository")
	}
	// A repository with no wiki says so rather than failing oddly, even
	// for its owner.
	if out, errOut, code := inst.ssh(t, aliceKey, "", "wiki", "list", "alice/secretive", "--json"); code != 0 {
		t.Fatalf("wiki list on a repo without one: %s", errOut)
	} else if !strings.Contains(out, `"pages":[]`) {
		t.Errorf("wiki list on a repo without one returned pages: %s", out)
	}
	// Wiki access derives from the parent: a stranger gets nothing.
	if _, _, code := inst.ssh(t, bobKey, "", "wiki", "list", "alice/secretive"); code == 0 {
		t.Error("a stranger listed a private repository's wiki")
	}

	// 404-parity: a private repo's wiki is invisible, over web and git.
	if status, _ := inst.get(t, "/alice/secretive/wiki"); status != 404 {
		t.Fatalf("private wiki page: %d", status)
	}

	// Pushing to <name>.wiki.git is refused now that the companion route
	// is gone; there is no such repository.
	if out, code := gitRun(t, t.TempDir(), env, "clone", inst.sshURL("alice/app.wiki"), "x"); code == 0 {
		t.Fatalf("cloned a nonexistent companion: %s", out)
	} else if !strings.Contains(out, "not found") {
		t.Fatalf("clone of alice/app.wiki: %s", out)
	}

	// A repository may now be named something.wiki: the suffix is no
	// longer reserved.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/notes.wiki"); code != 0 {
		t.Fatalf("something.wiki repo name refused: %s", errOut)
	}

	// repo commit-file writes a page on a repository that permits
	// server-authored commits: it is the command behind the web editor,
	// and there is no wiki-specific write command.
	if _, errOut, code := inst.ssh(t, aliceKey, "written by commit-file\n",
		"repo", "commit-file", "alice/app", ".gitbay/wiki/Extra.md",
		"--ref", "main", "--message", "'add a page'", "--file", "-"); code != 0 {
		t.Fatalf("repo commit-file: %s", errOut)
	}
	out, _, code = inst.ssh(t, aliceKey, "", "wiki", "show", "alice/app", "Extra", "--json")
	if code != 0 || !strings.Contains(out, "written by commit-file") {
		t.Errorf("wiki show Extra: %s", out)
	}

	// A repository requiring verified signatures refuses repo commit-file,
	// since the server cannot sign on the user's behalf.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "require-signed", "alice/app", "on"); code != 0 {
		t.Fatal("require-signed failed")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "blocked\n",
		"repo", "commit-file", "alice/app", ".gitbay/wiki/Blocked.md",
		"--ref", "main", "--file", "-"); code == 0 || !strings.Contains(errOut, "requires signed commits") {
		t.Errorf("repo commit-file not refused on a signed-commits repo: %d %s", code, errOut)
	}
}
