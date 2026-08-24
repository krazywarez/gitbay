package e2e

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadmeRelativeLinks(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/site"); code != 0 {
		t.Fatal("repo create failed")
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/site"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, "docs"), 0o755)
	os.MkdirAll(filepath.Join(dir, "img"), 0o755)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte(
		"# site\n\n[guide](docs/guide.md) and [export](docs/paper.html) and "+
			"[abs](https://example.org/x) here\n\n![logo](img/logo.png)\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "guide.md"), []byte("# guide\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "docs", "paper.org"), []byte("* paper\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "img", "logo.png"), []byte{0x89, 0x50}, 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	status, body := inst.get(t, "/alice/site")
	if status != 200 {
		t.Fatalf("tree: %d", status)
	}
	for _, want := range []string{
		`href="/alice/site/blob/main/docs/guide.md"`,   // relative link
		`href="/alice/site/blob/main/docs/paper.org"`,  // .html mapped to .org source
		`src="/alice/site/raw/main/img/logo.png"`,      // relative image via raw
		`href="https://example.org/x"`,                 // absolute untouched
		`href="/alice/site/blob/main/README.md">README.md</a>`, // clickable card header
		`<th>name</th>`, // file table column headers
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	// Branch dropdown lists branches.
	if !strings.Contains(body, `class="refmenu"`) || !strings.Contains(body, ">all refs") {
		t.Error("branch dropdown missing")
	}
	// Explore rows carry topics, license, and updated date.
	inst.ssh(t, aliceKey, "", "repo", "topics", "add", "alice/site", "web")
	os.WriteFile(filepath.Join(dir, "LICENSE"), []byte("Permission to use, copy, modify, and/or distribute this software...\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "license")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	_, body = inst.get(t, "/explore")
	for _, want := range []string{`href="/explore?q=web"`, "ISC", "updated 20"} {
		if !strings.Contains(body, want) {
			t.Errorf("explore row missing %q", want)
		}
	}
}

func TestWebInteractions(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	if _, _, code := inst.ssh(t, aliceKey, "", "org", "create", "theorg"); code != 0 {
		t.Fatal("org create failed")
	}

	out, _, code := inst.ssh(t, aliceKey, "", "web", "login", "--json")
	if code != 0 {
		t.Fatal("web login failed")
	}
	var env2 struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &env2)
	browser := newBrowser(t)
	browserGet(t, browser, inst.base()+env2.Data.URL[strings.Index(env2.Data.URL, "/login"):])

	// Create a repo under the org through the web form.
	if status, _ := browserPost(t, browser, inst.base()+"/new",
		url.Values{"owner": {"theorg"}, "name": {"webborn"}, "visibility": {"public"}}); status != 200 {
		t.Fatalf("org repo via web: %d", status)
	}
	if out, _, code := inst.ssh(t, aliceKey, "", "repo", "show", "theorg/webborn"); code != 0 {
		t.Fatalf("org repo missing: %s", out)
	}

	// Pin from the web; the repo header reflects it and the dashboard
	// lists it; a second toggle unpins.
	if status, _ := browserPost(t, browser, inst.base()+"/theorg/webborn/pin", url.Values{}); status != 200 {
		t.Fatal("pin toggle failed")
	}
	_, body := browserGet(t, browser, inst.base()+"/theorg/webborn")
	if !strings.Contains(body, "★ pinned") {
		t.Fatal("repo header not pinned")
	}
	if _, body = browserGet(t, browser, inst.base()+"/"); !strings.Contains(body, "theorg<span") {
		t.Fatal("dashboard missing pinned repo")
	}
	browserPost(t, browser, inst.base()+"/theorg/webborn/pin", url.Values{})
	if _, body = browserGet(t, browser, inst.base()+"/theorg/webborn"); !strings.Contains(body, "☆ pin") {
		t.Fatal("unpin failed")
	}

	// New issue with labels through the form; label chips filter.
	if status, _ := browserPost(t, browser, inst.base()+"/theorg/webborn/issues/new",
		url.Values{"title": {"styled"}, "body": {"b"}, "labels": {"bug ui"}}); status != 200 {
		t.Fatal("issue via web failed")
	}
	if status, _ := browserPost(t, browser, inst.base()+"/theorg/webborn/issues/new",
		url.Values{"title": {"plain"}}); status != 200 {
		t.Fatal("second issue failed")
	}
	_, body = browserGet(t, browser, inst.base()+"/theorg/webborn/issues?label=bug")
	if !strings.Contains(body, "styled") || strings.Contains(body, ">plain<") {
		t.Fatalf("label filter wrong:\n%s", body)
	}
	_, body = browserGet(t, browser, inst.base()+"/theorg/webborn/issues/1")
	if !strings.Contains(body, `href="/theorg/webborn/issues?label=bug"`) ||
		!strings.Contains(body, `href="/alice">alice</a>`) {
		t.Fatal("issue page chips/author not linked")
	}
}

func TestCommitParentLinks(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "first")
	first := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("b\n"), 0o644)
	mustGit(t, dir, env, "commit", "-qam", "second")
	head := strings.TrimSpace(mustGit(t, dir, env, "rev-parse", "HEAD"))
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	_, body := inst.get(t, "/alice/app/commit/"+head)
	if !strings.Contains(body, `href="/alice/app/commit/`+first+`"`) {
		t.Fatal("parent commit not linked")
	}
	_, body = inst.get(t, "/alice/app/commit/"+first)
	if strings.Contains(body, ">parent") {
		t.Fatal("root commit shows a parent")
	}
}
