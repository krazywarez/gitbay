package e2e

import (
	"encoding/json"
	"fmt"
	"net/http"
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
			"[abs](https://example.org/x) here\n\n![logo](img/logo.png)\n"+
			"![ext](https://example.org/pic.png)\n\n"+
			"| flag | effect |\n|------|--------|\n| `-v` | verbose |\n\n"+
			"```go\nfunc main() {}\n```\n"), 0o644)
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
		`href="/alice/site/blob/main/docs/guide.md"`,  // relative link
		`href="/alice/site/blob/main/docs/paper.org"`, // .html mapped to .org source
		`src="/alice/site/raw/main/img/logo.png"`,     // relative image via raw
		`src="https://example.org/pic.png"`,           // remote image untouched
		`href="https://example.org/x"`,                // absolute untouched
		"<table>", "<td>verbose</td>",                 // GFM table renders
		`<span class="kd">func</span>`,                         // fenced code highlighted via classes
		`href="/alice/site/blob/main/README.md">README.md</a>`, // clickable card header
		`<th scope="col">name</th>`,                            // file table column headers
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q", want)
		}
	}
	// Directories sort ahead of files, whatever git's own tree order was:
	// docs/ and img/ precede LICENSE and README.md despite sorting after
	// them byte-wise.
	for _, dir := range []string{"docs", "img"} {
		d := strings.Index(body, `/tree/main/`+dir+`">`)
		f := strings.Index(body, `/blob/main/README.md">`)
		if d < 0 || f < 0 {
			t.Fatalf("listing missing %s/ or README.md", dir)
		}
		if d > f {
			t.Errorf("%s/ listed after README.md; directories should come first", dir)
		}
	}
	// Branch dropdown lists branches.
	if !strings.Contains(body, `class="refmenu"`) || !strings.Contains(body, ">All refs") {
		t.Error("branch dropdown missing")
	}
	// Per-file history: ?path= filters the log; blob pages link to it.
	os.WriteFile(filepath.Join(dir, "docs", "notes.txt"), []byte("n\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "touch only the notes")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	if _, body := inst.get(t, "/alice/site/log?path=docs/notes.txt"); !strings.Contains(body, "touch only the notes") ||
		strings.Contains(body, ">base<") || !strings.Contains(body, "history of") {
		t.Fatalf("per-file log wrong:\n%s", body)
	}
	if _, body := inst.get(t, "/alice/site/log?path=no/such/file"); !strings.Contains(body, "nothing touches") {
		t.Fatalf("empty per-file log:\n%s", body)
	}
	if _, body := inst.get(t, "/alice/site/blob/main/docs/guide.md"); !strings.Contains(body, `log/main?path=docs%2fguide.md">history</a>`) {
		t.Fatalf("blob history link missing:\n%s", body)
	}
	// CLI parity: repo log --path.
	logOut, _, _ := inst.ssh(t, aliceKey, "", "repo", "log", "alice/site", "--path", "docs/notes.txt", "--json")
	if !strings.Contains(logOut, "touch only the notes") || strings.Contains(logOut, `"subject":"base"`) {
		t.Fatalf("repo log --path wrong:\n%s", logOut)
	}
	// Raw serves images with their real type (nosniff otherwise blocks
	// <img>); everything else stays inert text/plain.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/alice/site/raw/main/img/logo.png", inst.httpPort))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "image/png" {
		t.Errorf("raw png content-type = %q", ct)
	}
	resp, err = http.Get(fmt.Sprintf("http://127.0.0.1:%d/alice/site/raw/main/README.md", inst.httpPort))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("raw md content-type = %q", ct)
	}
	// Blob pages preview images inline.
	if _, body := inst.get(t, "/alice/site/blob/main/img/logo.png"); !strings.Contains(body, `<img src="/alice/site/raw/main/img/logo.png"`) {
		t.Errorf("blob image preview missing:\n%s", body)
	}
	// Highlighting is class-based so the palette follows the color scheme:
	// no inline colors on code, and the stylesheet carries both palettes.
	if _, body := inst.get(t, "/alice/site/blob/main/README.md"); !strings.Contains(body, `class="chroma"`) ||
		strings.Contains(body, "style=\"color") {
		t.Errorf("highlighting not class-based:\n%.2000s", body)
	}
	if _, css := inst.get(t, "/static/style.css"); strings.Count(css, "/* Background */") < 2 ||
		!strings.Contains(css, ".chroma, .bg { background: transparent") {
		t.Error("stylesheet missing dual syntax palettes")
	}
	// Explore rows carry topics, license, and updated date.
	inst.ssh(t, aliceKey, "", "repo", "topics", "add", "alice/site", "web")
	// Bare 0BSD grant (no notice-retention clause), wrapped mid-sentence.
	os.WriteFile(filepath.Join(dir, "LICENSE"), []byte("Permission to use, copy, modify,\nand/or distribute this software for any\npurpose with or without fee is hereby granted.\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "license")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	_, body = inst.get(t, "/explore")
	for _, want := range []string{`href="/explore?q=web"`, "0BSD", "updated 20"} {
		if !strings.Contains(body, want) {
			t.Errorf("explore row missing %q", want)
		}
	}
	// The repo header renders the same on every tab of the repo. It sits
	// above the tab bar, so anything that appears on one tab and not
	// another moves the navigation between clicks of that navigation.
	for _, page := range []string{"", "/issues", "/mrs", "/releases"} {
		_, body := inst.get(t, "/alice/site"+page)
		if !strings.Contains(body, `href="/explore?q=web"`) {
			t.Errorf("repo header topics missing on /alice/site%s", page)
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
	if !strings.Contains(body, "★ Pinned") {
		t.Fatal("repo header not pinned")
	}
	if _, body = browserGet(t, browser, inst.base()+"/"); !strings.Contains(body, ">theorg/</span>webborn") {
		t.Fatal("dashboard missing pinned repo")
	}
	browserPost(t, browser, inst.base()+"/theorg/webborn/pin", url.Values{})
	if _, body = browserGet(t, browser, inst.base()+"/theorg/webborn"); !strings.Contains(body, "☆ Pin") {
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

// TestAuthorNamesResolve checks that a commit whose author address is
// verified here displays the account's name rather than whatever git
// config carried, and that an unknown address keeps its own name.
func TestAuthorNamesResolve(t *testing.T) {
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

	// One commit from the account's verified address under a different
	// display name, one from an address nobody has proven.
	known := append(append([]string{}, env...),
		"GIT_AUTHOR_NAME=Alice Q. Longname", "GIT_AUTHOR_EMAIL=alice@example.test",
		"GIT_COMMITTER_NAME=Alice Q. Longname", "GIT_COMMITTER_EMAIL=alice@example.test")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, known, "checkout", "-q", "-b", "main")
	mustGit(t, dir, known, "add", ".")
	mustGit(t, dir, known, "commit", "-q", "-m", "from the account")
	stranger := append(append([]string{}, env...),
		"GIT_AUTHOR_NAME=Outside Person", "GIT_AUTHOR_EMAIL=outside@nowhere.test",
		"GIT_COMMITTER_NAME=Outside Person", "GIT_COMMITTER_EMAIL=outside@nowhere.test")
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b\n"), 0o644)
	mustGit(t, dir, stranger, "add", ".")
	mustGit(t, dir, stranger, "commit", "-q", "-m", "from a stranger")
	// The tip is the account's, so the bar above the listing shows a link.
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("c\n"), 0o644)
	mustGit(t, dir, known, "add", ".")
	mustGit(t, dir, known, "commit", "-q", "-m", "back to the account")
	mustGit(t, dir, known, "push", "-q", "origin", "main")

	// The log shows the account name for the verified address only.
	_, body := inst.get(t, "/alice/app/log")
	if strings.Contains(body, "Alice Q. Longname") {
		t.Fatalf("log showed the git config name for a known address:\n%s", body)
	}
	if !strings.Contains(body, "Outside Person") {
		t.Fatalf("log lost an unknown author's name:\n%s", body)
	}
	if !strings.Contains(body, "alice") {
		t.Fatalf("log missing the account name:\n%s", body)
	}
	// A resolved name links to the profile; an unknown one stays text.
	if !strings.Contains(body, `class="authorlink" href="/alice"`) {
		t.Fatalf("account name is not a link:\n%s", body)
	}
	if strings.Contains(body, `href="/Outside Person"`) {
		t.Fatalf("unknown author was linked:\n%s", body)
	}
	// The tipbar resolves and links the same way when the tip is an account's.
	if _, tree := inst.get(t, "/alice/app"); !strings.Contains(tree, `class="authorlink" href="/alice"`) {
		t.Fatalf("tipbar name is not a link:\n%s", tree)
	}
}
