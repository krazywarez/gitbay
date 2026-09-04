package e2e

import (
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
)

// TestPrivateRepoIsInvisible walks every read surface as a stranger and
// as an anonymous visitor, and asserts a private repository is
// indistinguishable from one that does not exist.
//
// The threat model states this as one rule — "every surface answers 'not
// found' identically" — but it was only ever tested per feature, in
// whichever test happened to think of it. A surface added later leaks
// without anything failing. This is the cross-cutting version: the list
// of surfaces is the thing under test, so adding a route without adding
// it here is the omission that shows up.
func TestPrivateRepoIsInvisible(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	ownerKey := inst.newKey(t, "owner")
	strangerKey := inst.newKey(t, "stranger")
	inst.admin(t, "admin", "user", "create", "owner", "--key", ownerKey+".pub")
	inst.admin(t, "admin", "user", "create", "stranger", "--key", strangerKey+".pub")

	if _, errOut, code := inst.ssh(t, ownerKey, "", "repo", "create", "owner/secret", "--private"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	// Distinctive strings: if any of these reach a stranger through any
	// surface, the grep below finds it whatever shape the leak took.
	const (
		repoWord  = "zulqarnain"      // in the description
		titleWord = "brontosaurus"    // in an issue title
		bodyWord  = "quinquagenarian" // in an issue body only
		fileWord  = "pterodactyl"     // in a file only
	)
	inst.ssh(t, ownerKey, "", "repo", "settings", "description", "owner/secret", "'"+repoWord+"'")
	inst.ssh(t, ownerKey, "", "repo", "topics", "add", "owner/secret", repoWord)
	if _, errOut, code := inst.ssh(t, ownerKey, "", "issue", "create", "owner/secret",
		"--title", "'"+titleWord+"'", "--body", "'"+bodyWord+"'"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}

	env := inst.gitEnv(ownerKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("owner/secret"), "w")
	dir := work + "/w"
	os.WriteFile(dir+"/notes.txt", []byte(fileWord+"\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "secret work")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "checkout", "-q", "-b", "feat")
	mustGit(t, dir, env, "commit", "-q", "--allow-empty", "-m", "more")
	mustGit(t, dir, env, "push", "-q", "origin", "feat")
	inst.ssh(t, ownerKey, "", "mr", "create", "owner/secret",
		"--source", "feat", "--target", "main", "--title", "'"+titleWord+" mr'")

	secrets := []string{repoWord, titleWord, bodyWord, fileWord}
	// The repository's own name is not secret in the same way — a name is
	// guessable — but its content must never appear.
	webPaths := []string{
		"/owner/secret", "/owner/secret/issues", "/owner/secret/issues/1",
		"/owner/secret/mrs", "/owner/secret/mrs/1", "/owner/secret/refs",
		"/owner/secret/tree/main/", "/owner/secret/blob/main/notes.txt",
		"/owner/secret/raw/main/notes.txt", "/owner/secret/log",
		"/owner/secret/releases", "/owner/secret/builds", "/owner/secret/wiki",
		"/owner/secret/compare/main...feat", "/owner/secret/milestones",
		"/owner/secret/archive/main.tar.gz", "/owner/secret/badge/build.svg",
		"/owner/secret/search?q=" + fileWord,
		"/owner", "/explore", "/explore?q=" + repoWord,
		"/search?q=" + repoWord, "/search?q=" + titleWord, "/search?q=" + bodyWord,
		"/owner/secret/info/refs?service=git-upload-pack",
	}

	// Anonymous.
	for _, p := range webPaths {
		status, body := inst.get(t, p)
		checkNoLeak(t, "anonymous "+p, p, status, body, secrets)
	}
	// A logged-in stranger.
	browser := inst.login(t, strangerKey)
	for _, p := range webPaths {
		status, body := browserGet(t, browser, inst.base()+p)
		checkNoLeak(t, "stranger "+p, p, status, body, secrets)
	}

	// Control commands, as the stranger. Each must be exit 3 (not found)
	// or return nothing about the repository — never exit 4, which would
	// confirm the namespace exists.
	cmds := [][]string{
		{"repo", "show", "owner/secret"},
		{"issue", "list", "owner/secret"},
		{"issue", "show", "owner/secret", "1"},
		{"mr", "list", "owner/secret"},
		{"mr", "show", "owner/secret", "1"},
		{"mr", "diff", "owner/secret", "1"},
		{"repo", "grep", "owner/secret", fileWord},
		{"repo", "log", "owner/secret"},
		{"repo", "refs", "owner/secret"},
		{"build", "list", "owner/secret"},
		{"release", "list", "owner/secret"},
		{"wiki", "list", "owner/secret"},
		{"repo", "download", "owner/secret"},
	}
	for _, argv := range cmds {
		out, errOut, code := inst.ssh(t, strangerKey, "", argv...)
		label := "stranger " + strings.Join(argv, " ")
		if code == 4 {
			t.Errorf("%s: exit 4 (denied) confirms the repository exists; want 3", label)
		}
		checkNoLeakText(t, label, out+errOut, secrets)
	}

	// Listings a stranger legitimately reaches must not carry it either.
	for _, argv := range [][]string{
		{"repo", "list"}, {"explore"}, {"search", repoWord}, {"search", titleWord},
		{"search", bodyWord}, {"feed"}, {"dashboard"}, {"profile", "show", "owner"},
	} {
		out, errOut, _ := inst.ssh(t, strangerKey, "", argv...)
		checkNoLeakText(t, "stranger "+strings.Join(argv, " "), out+errOut, secrets)
	}

	// The owner can still see all of it, so the assertions above are
	// measuring access control and not a broken fixture.
	out, _, code := inst.ssh(t, ownerKey, "", "search", bodyWord, "--json")
	if code != 0 || !strings.Contains(out, titleWord) {
		t.Fatalf("owner cannot find their own issue by body; the fixture is wrong, not the ACL: %s", out)
	}
}

// checkNoLeak asserts a response carries nothing about the private
// repository. A search page echoes the query into its own form and filter
// links, so a term that appears only because the prober typed it is not a
// leak — those are dropped, and the surviving signals are a *different*
// secret appearing, or a link to the repository, either of which can only
// come from a result row.
func checkNoLeak(t *testing.T, label, path string, status int, body string, secrets []string) {
	t.Helper()
	if status == http.StatusForbidden {
		t.Errorf("%s: 403 confirms the namespace exists; want 404", label)
	}
	echoed := ""
	if i := strings.Index(path, "q="); i >= 0 {
		echoed = path[i+2:]
	}
	var forbidden []string
	for _, s := range secrets {
		if s != echoed {
			forbidden = append(forbidden, s)
		}
	}
	checkNoLeakText(t, fmt.Sprintf("%s (status %d)", label, status), body, forbidden)
	// A listing that found the repository would link it. The repo's own
	// pages are excluded: the URL under test is that link.
	if !strings.HasPrefix(path, "/owner/secret") && strings.Contains(body, `href="/owner/secret`) {
		t.Errorf("%s: links the private repository", label)
	}
}

func checkNoLeakText(t *testing.T, label, body string, secrets []string) {
	t.Helper()
	for _, s := range secrets {
		if strings.Contains(body, s) {
			t.Errorf("%s leaked %q", label, s)
		}
	}
}
