# Web UI/UX sweep: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the seventeen findings posted on #182 under the rules in the spec, one commit per pattern, on one branch. Closes #182.

**Architecture:** Every change is in the web layer (`internal/httpd`, `internal/web/templates`, `internal/web/static/style.css`) except three message rewrites at their source in `internal/control` and one sort helper in `internal/gitutil`. No new commands, no schema change. Web forms keep dispatching control commands; the typed confirmation is a check in the handler before dispatch.

**Tech Stack:** Go, Go `html/template`, the control registry, the e2e harness in `e2e/` (real sshd and HTTP against a temp instance; `startInstanceWith(t, "[web]\nmode = \"accounts\"\n")`, `inst.login(t, key)`, `browserGet`, `browserPost`, `inst.get`).

**Spec:** `docs/specs/2026-09-11-web-ux-sweep-design.md`

## Global Constraints

- No JavaScript in templates: the instance CSP is `script-src 'none'`.
- Every `<input>`/`<textarea>`/`<select>` a person uses carries an `aria-label` or a `<label for>` (`internal/httpd/inputlabels_test.go`); one `<h1>` per page.
- Every `Mutating: true` route stays wrapped in `checkOrigin`; no new routes in this plan.
- Web handlers never reimplement a rule; a refused command's message reaches the page through the flash (`s.setFlash` / `s.done` / `backTo`).
- Never mention an assistant or model anywhere: commit messages, comments, docs.
- Commit messages: imperative subject, a body only where a why is needed, `Ref #182` as the last line; the docs task's commit ends `Closes #182`.
- Run locally: `go build ./... && go vet ./...`, `go test ./internal/httpd ./internal/web ./internal/control ./internal/gitutil`, and only the e2e tests the task names. CI on bay1 runs the whole suite.
- Before changing any user-visible string, grep `e2e/` and `internal/httpd/*_test.go` for it and update the assertions in the same commit; a task's step says which strings.
- Plain-sentence comments; match the surrounding code.
- Work on branch `ux-sweep` in the worktree `/Users/cmc/git/krz/gitbay-ux`, which already holds the spec.

Facts every task can rely on (from reading the tree at 3e09d58):

- `s.setFlash(w, msg)` / `s.takeFlash(w, r)` / `s.clearCookie(name, sameSite)` are in `internal/httpd/flash.go`; pages render the flash as `{{if .Notice}}<p class="error" role="alert">{{.Notice}}</p>{{end}}`.
- `s.done(w, r, code, msg, redirectFn)` in `internal/httpd/control.go:57`; `s.backTo(w, r, page, msg)` in `internal/httpd/releaseactions.go:15` redirects to `/{owner}/{repo}/{page}` with the flash.
- Template helpers live in `internal/web/web.go` (`funcs`, line 62): `when(s string) string` parses RFC3339Nano and formats `2006-01-02 15:04`; `ago(t time.Time) string` is relative.
- `repoPage` is built in `repoFor` (`internal/httpd/web.go:289`, fields set around line 341: `Host: s.cfg.SiteHost()`, `CloneURL: s.cfg.Server.SiteURL + "/" + repo.Path() + ".git"`).

---

### Task 1: Typed confirmation on destructive controls

**Files:**
- Create: `internal/httpd/confirm.go`
- Modify: `internal/web/templates/account.html:34,64,90`; `internal/httpd/account.go:150-182`
- Modify: `internal/web/templates/releases.html:35-38`; `internal/httpd/releaseactions.go:34-38`
- Modify: `internal/web/templates/snippet.html:22-25,46-48`; `internal/httpd/snippets.go:215,227`
- Modify: `internal/web/templates/owner.html:98-102`; `internal/httpd/orgweb.go:82`
- Modify: `internal/web/templates/labels.html:15-19`; `internal/httpd/labels.go:50-53`
- Modify: `internal/web/static/style.css` (one rule)
- Test: `e2e/accountweb_test.go:78`, `e2e/releaseweb_test.go:102`, `e2e/snippetweb_test.go:141,148,173`, `e2e/labelweb_test.go:56`, `e2e/orgweb_test.go` (new team-delete steps)

**Interfaces:**
- Produces `func confirmed(r *http.Request, want string) (ok bool, msg string)` in `internal/httpd/confirm.go`: `ok` when `strings.TrimSpace(r.FormValue("confirm")) == want`; otherwise `msg` is `type ` + want + ` to confirm`.
- Produces the template partial `confirmfield` in `internal/web/templates/layout.html`: `{{define "confirmfield"}}<input type="text" name="confirm" aria-label="Type {{.}} to confirm" placeholder="type {{.}} to confirm" size="{{len .}}" autocomplete="off">{{end}}`, called as `{{template "confirmfield" "v1.0"}}`.

What each control asks for (the `want` value), from the spec:

| control | template | want |
|---|---|---|
| SSH key remove | account.html:34 | the 8 characters after `SHA256:` in the fingerprint |
| email remove | account.html:64 | the address |
| PGP key remove | account.html:90 | the first 8 characters of the fingerprint |
| release delete | releases.html:35 | the tag |
| snippet delete | snippet.html:46 | the snippet's public id |
| snippet file remove | snippet.html:22 | the file name |
| team delete | owner.html:98 | the team name |
| label remove | labels.html:15 | the label |

Note the spec says the SSH key's label; a key's label can be empty, so the fingerprint prefix is used instead and the spec's Rules section is amended in this task (one line).

- [ ] **Step 1: Write the failing e2e assertions**

In `e2e/labelweb_test.go` around line 56, replace the label-remove post with two posts:

```go
	// Removing a label needs its name typed; a bare post is refused and
	// the label stays.
	_, body := browserPost(t, alice, base+"/labels", url.Values{
		"action": {"remove"}, "name": {"bug"}})
	if !strings.Contains(body, "type bug to confirm") {
		t.Fatalf("unconfirmed remove was not refused:\n%s", body)
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "label", "list", "alice/app", "--json"); !strings.Contains(out, `"name":"bug"`) {
		t.Fatalf("label removed without confirmation: %s", out)
	}
	if status, _ := browserPost(t, alice, base+"/labels", url.Values{
		"action": {"remove"}, "name": {"bug"}, "confirm": {"bug"}}); status != 200 {
		t.Fatal("label remove failed")
	}
```

In `e2e/releaseweb_test.go` around line 102, the same shape: a post without `confirm` gets a body containing `type v1.0 to confirm` and `release list --json` still lists `v1.0`; then the post with `"confirm": {"v1.0"}` succeeds and the existing "still listed after delete" assertion stays.

In `e2e/accountweb_test.go` around line 78, the key-remove post: first without `confirm`, assert the response body contains `to confirm` and `keys list --json` still lists the fingerprint; then with `"confirm": {prefix}` where `prefix := strings.TrimPrefix(fp, "SHA256:")[:8]`; keep the existing assertion that the key is gone.

In `e2e/snippetweb_test.go`: line 141 (file remove `b.txt`) adds `"confirm": {"b.txt"}`; line 148 (the last-file refusal) adds `"confirm": {"notes.md"}` so the refusal under test is still the command's; line 173 (delete) becomes `url.Values{"confirm": {created}}`; and before line 173 add a post with no `confirm` asserting the body contains `type `+created+` to confirm` and `snippet show` still exits 0.

In `e2e/orgweb_test.go` after the team-revoke step (around line 86), add: a post with `"field": {"team-delete"}, "team": {"builders"}` and no `confirm` whose body contains `type builders to confirm`, then `org team show acme builders --json` still exits 0; then the same post with `"confirm": {"builders"}`, after which `org team show` exits 3.

- [ ] **Step 2: Run them to see them fail**

Run: `go test ./e2e -run 'TestLabelsWeb$|TestReleaseAndBuildWeb$|TestAccountSettingsWeb$|TestSnippetsWeb$|TestOrgManagementWeb$'`
Expected: each new "unconfirmed … was not refused" assertion fails, because the handlers act without a confirm field.

- [ ] **Step 3: The helper and the partial**

`internal/httpd/confirm.go`:

```go
package httpd

import (
	"net/http"
	"strings"
)

// confirmed reports whether the form typed want into its confirm field.
// It guards controls that destroy data nothing else holds; the person
// is already authorised, so this is a check against a slip, not a
// permission.
func confirmed(r *http.Request, want string) (bool, string) {
	if strings.TrimSpace(r.FormValue("confirm")) == want {
		return true, ""
	}
	return false, "type " + want + " to confirm"
}
```

Add the `confirmfield` partial to `internal/web/templates/layout.html` next to `formatpicker` (line 132), exactly as in Interfaces.

- [ ] **Step 4: Handlers**

Each handler checks before dispatch and reports through the page's existing failure path:

- `internal/httpd/account.go`: in `case "key-remove"` compute `want := strings.TrimPrefix(r.FormValue("fingerprint"), "SHA256:")`, `if len(want) > 8 { want = want[:8] }`; `if ok, msg := confirmed(r, want); !ok { back(msg, ""); return }` before `s.runControl`. `case "pgp-remove"`: `want` is the fingerprint's first 8 characters. `case "email-remove"`: `want` is `r.FormValue("address")`. Read `back`'s signature at account.go:150 and call it as the other failures do.
- `internal/httpd/releaseactions.go:34`: in the delete branch, `if ok, msg := confirmed(r, tag); !ok { back(w, r, msg); return }`.
- `internal/httpd/snippets.go`: in `snippetDeleteSubmit` check against `r.PathValue("id")`; in `snippetFileRemoveSubmit` against the trimmed `name`. On refusal `s.setFlash(w, msg)` and redirect to the snippet page, as `snippetAction`'s back closure does.
- `internal/httpd/orgweb.go:82`: in `case "team-delete"` check against `team`; on refusal `back(msg); return`.
- `internal/httpd/labels.go:51`: inside `if r.FormValue("action") == "remove"`, check against `name`; on refusal `s.backTo(w, r, "labels", msg); return`.

- [ ] **Step 5: Templates**

Put `{{template "confirmfield" X}}` immediately before the button in each form, with X the same value the handler wants:

- account.html:34 key remove: `{{template "confirmfield" (slice (trimSHA .Fingerprint) 0 8)}}` needs no new helper if you compute the prefix in Go instead: add `Confirm string` beside `Fingerprint` in the key row struct the account page builds (find it in `internal/httpd/account.go`'s GET handler) and use `{{template "confirmfield" .Confirm}}`. Same for the PGP row (`Confirm` = first 8 of the fingerprint). Email: `{{template "confirmfield" .Address}}`.
- releases.html:35: `{{template "confirmfield" $rel.Tag}}`.
- snippet.html:22: `{{template "confirmfield" .Name}}`; :46: `{{template "confirmfield" .Snippet.PublicID}}`.
- owner.html:98: `{{template "confirmfield" .Name}}` (the team's name in that range).
- labels.html:15: `{{template "confirmfield" .Name}}`.

Style: in `internal/web/static/style.css` add `input[name="confirm"] { width: auto; margin-right: var(--sp-2); }` near the other form rules (grep `.inline` to find them; use the spacing token the neighbours use).

- [ ] **Step 6: Spec line**

In `docs/specs/2026-09-11-web-ux-sweep-design.md`, Rules, change "SSH key remove (the key's label)" to "SSH key remove (the 8 characters after `SHA256:` in the fingerprint; a label can be empty)".

- [ ] **Step 7: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/httpd && go test ./e2e -run 'TestLabelsWeb$|TestReleaseAndBuildWeb$|TestAccountSettingsWeb$|TestSnippetsWeb$|TestOrgManagementWeb$'`
Expected: PASS. `internal/httpd`'s input-label test sees the new input's `aria-label`.

- [ ] **Step 8: Commit**

```bash
git add internal/httpd/confirm.go internal/httpd/account.go internal/httpd/releaseactions.go internal/httpd/snippets.go internal/httpd/orgweb.go internal/httpd/labels.go internal/web/templates/layout.html internal/web/templates/account.html internal/web/templates/releases.html internal/web/templates/snippet.html internal/web/templates/owner.html internal/web/templates/labels.html internal/web/static/style.css docs/specs/2026-09-11-web-ux-sweep-design.md e2e/accountweb_test.go e2e/releaseweb_test.go e2e/snippetweb_test.go e2e/labelweb_test.go e2e/orgweb_test.go
git commit -m "web: type the name to confirm a destructive control

Release delete, snippet delete and file remove, team delete, label
remove, and SSH key, email and PGP key removal ask for the object's
name in a text field; the handler refuses a mismatch with a flash.
Reversible controls keep a plain button.

Ref #182"
```

---

### Task 2: Login returns to the page that asked for it

**Files:**
- Modify: `internal/httpd/flash.go` (two helpers)
- Modify: `internal/httpd/accounts.go:48-57` (`requireUser`), `:117-145` (`login`), and `renderLogin`
- Modify: `internal/web/templates/login.html:3-4`
- Test: `e2e/websessions_test.go` (extend `TestWebSessionsListRevoke`)

**Interfaces:**
- Produces `setNext(w, path string)` and `takeNext(w, r) string` in `flash.go`, cookie name `gitbay_next`, `MaxAge: 600`, same flags as the flash cookie. `takeNext` returns `""` unless the value starts with `/` and not `//`.
- `renderLogin` gains a `next string` argument rendered as `Next`.

- [ ] **Step 1: Write the failing e2e test**

Append to the session test in `e2e/websessions_test.go`, using its existing instance and key (read the file first; it starts an accounts-mode instance and mints a login link):

```go
	// An anonymous visit to a page that needs a session lands on the
	// login page, which says where the visitor was going; the login
	// link then returns them there.
	anon := newBrowser(t)
	status, body := browserGet(t, anon, inst.base()+"/settings")
	if status != 200 || !strings.Contains(body, "continue to <code>/settings</code>") {
		t.Fatalf("login page without the destination: %d\n%s", status, body)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "web", "login", "--json")
	var env struct {
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &env)
	link := inst.base() + env.Data.URL[strings.Index(env.Data.URL, "/login"):]
	if status, body := browserGet(t, anon, link); status != 200 || !strings.Contains(body, "Account settings") {
		t.Fatalf("login did not return to /settings: %d\n%s", status, body)
	}
	// The destination is used once.
	if _, body := browserGet(t, anon, inst.base()+"/login"); strings.Contains(body, "continue to") {
		t.Fatal("next survived its use")
	}
```

Adjust `aliceKey` to the key variable the test already has, and add `encoding/json` to the imports if missing.

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./e2e -run 'TestWebSessionsListRevoke$'`
Expected: FAIL at "login page without the destination".

- [ ] **Step 3: Cookie helpers**

In `internal/httpd/flash.go`, after `takeFlash`:

```go
const nextCookie = "gitbay_next"

// setNext remembers the local path an anonymous visitor asked for, so
// the login that follows can return there. Only a GET path is stored:
// a POST must not be replayed.
func (s *Server) setNext(w http.ResponseWriter, path string) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || len(path) > 300 {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: nextCookie, Value: url.QueryEscape(path), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.cfg.HTTP.TLS != "off", MaxAge: 600,
	})
}

// takeNext returns the remembered path once and clears it. Anything
// that is not a local path comes back empty.
func (s *Server) takeNext(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(nextCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	http.SetCookie(w, s.clearCookie(nextCookie, http.SameSiteLaxMode))
	p, err := url.QueryUnescape(c.Value)
	if err != nil || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return ""
	}
	return p
}

// peekNext reads the remembered path without clearing it, for the
// login page to say where the visitor is going.
func (s *Server) peekNext(r *http.Request) string {
	c, err := r.Cookie(nextCookie)
	if err != nil {
		return ""
	}
	p, err := url.QueryUnescape(c.Value)
	if err != nil || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return ""
	}
	return p
}
```

Add `"strings"` to the file's imports if absent.

- [ ] **Step 4: requireUser and login**

`requireUser` (accounts.go:48): before the redirect, `if r.Method == http.MethodGet { s.setNext(w, r.URL.RequestURI()) }`.

`login` (accounts.go:117): the no-token branch passes `s.peekNext(r)` into `renderLogin`; the success branch replaces `http.Redirect(w, r, "/", ...)` with:

```go
	dest := s.takeNext(w, r)
	if dest == "" {
		dest = "/"
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
```

`renderLogin` gains `next string` and puts it in the page struct as `Next`; update its other callers (`loginSubmit` passes `""`).

`login.html` after the `<h1>`: `{{if .Next}}<p class="meta">Log in to continue to <code>{{.Next}}</code>.</p>{{end}}`.

- [ ] **Step 5: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/httpd && go test ./e2e -run 'TestWebSessionsListRevoke$|TestEmailLogin'` (the six `TestEmailLogin*` tests consume links too).
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/httpd/flash.go internal/httpd/accounts.go internal/web/templates/login.html e2e/websessions_test.go
git commit -m "web: login returns to the page that needed it

requireUser remembers a GET path in a short-lived cookie; the login
page names it and both login paths redirect there once.

Ref #182"
```

---

### Task 3: One date format

**Files:**
- Modify: `internal/web/web.go:220-227` (`when`)
- Modify: `internal/web/templates/commit.html:8`, `log.html:15`, `compare.html:9`, `mr.html:65`, `blame.html:16`, `snippets.html:12`, `snippet.html:5`, `settings.html:147,163`
- Modify: `internal/httpd/web.go:1516,1564,1890`, `internal/httpd/compare.go:72`, `internal/httpd/web.go:860-862` (blame)
- Test: `internal/web/web_test.go` (create if absent) and the e2e assertions the grep in Step 1 finds

**Interfaces:**
- `when` renders `2006-01-02 15:04 UTC`; input stays RFC3339/RFC3339Nano.

- [ ] **Step 1: Find every assertion on the old formats**

Run: `grep -rn '[0-9]\{4\}-[0-9]\{2\}-[0-9]\{2\} [0-9]\{2\}:[0-9]\{2\}' e2e/ internal/httpd/*_test.go internal/web/*_test.go | grep -v 'Z"'` and `grep -rn '"when"\|when(' internal/web/*_test.go`. List the hits in the report; each is updated in Step 5.

- [ ] **Step 2: Write the failing unit test**

`internal/web/web_test.go` (append, or create with `package web`):

```go
func TestWhenNamesTheZone(t *testing.T) {
	got := funcs["when"].(func(string) string)("2026-09-12T02:18:07.123Z")
	if got != "2026-09-12 02:18 UTC" {
		t.Fatalf("when: %q", got)
	}
	if got := funcs["when"].(func(string) string)("not a time"); got != "not a time" {
		t.Fatalf("passthrough: %q", got)
	}
}
```

Run: `go test ./internal/web -run TestWhenNamesTheZone` → FAIL (`2026-09-12 02:18`).

- [ ] **Step 3: The helper**

In `internal/web/web.go:226` change the format to `"2006-01-02 15:04 UTC"`.

- [ ] **Step 4: The pages**

- `commit.html:8`: `{{when .Date}}` (the value is already RFC3339 from `web.go:1564`).
- `log.html:15`, `compare.html:9`, `mr.html:65`, `blame.html:16`: `{{when .Date}}`, and change the four producers to emit RFC3339 instead of `2006-01-02`: `web.go:1516`, `compare.go:72`, `web.go:1890`, `web.go:860-862` (each is a `time.Unix(...).UTC().Format("2006-01-02")` or a re-parse; make it `.Format(time.RFC3339)`). Read each site; if one already carries a `time.Time`, format it once.
- `snippets.html:12` → `{{when .UpdatedAt}}`; `snippet.html:5` → `updated {{when .Snippet.UpdatedAt}}`.
- `settings.html:147` → `{{when .Deps.LastCheck}}`; `:163` → `last poll {{when .LastSeen}}`.
- Tree and blob listings keep `ago`; where `ago` is used in `tree.html`, add `title="{{when .When}}"` on the element if the row carries the raw time (read the row struct; if it only has a `time.Time`, add a `whenT` helper: `"whenT": func(t time.Time) string { return t.UTC().Format("2006-01-02 15:04 UTC") }` and use it in the title). Skip the title if the tree row has no time value at all; say so in the report.

- [ ] **Step 5: Update the assertions from Step 1, run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/web ./internal/httpd && go test ./e2e -run '<the tests whose assertions changed>|TestSnippetsWeb$'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/web/web.go internal/web/web_test.go internal/web/templates internal/httpd e2e
git commit -m "web: one timestamp format, with the zone named

when renders 2006-01-02 15:04 UTC on every page; commit, log,
compare, blame, snippet and settings pages use it instead of
date-only, ISO, or raw stored strings.

Ref #182"
```

---

### Task 4: Version-aware tag order

**Files:**
- Create: `internal/gitutil/versions.go`, `internal/gitutil/versions_test.go`
- Modify: `internal/httpd/web.go:1958-1969` (`refs`) and `:639-646` (`FreeTags` for the release form)

**Interfaces:**
- Produces `func SortVersions(refs []Ref)` in `internal/gitutil`: in place, newest version first; refs that do not parse as a version follow, by name ascending.

- [ ] **Step 1: Write the failing unit test**

`internal/gitutil/versions_test.go`:

```go
package gitutil

import "testing"

func TestSortVersionsNewestFirst(t *testing.T) {
	refs := []Ref{{Name: "v1.2.0"}, {Name: "v1.10.0"}, {Name: "nightly"}, {Name: "v1.2.1"}, {Name: "v0.9"}, {Name: "beta"}, {Name: "2.0.0"}}
	SortVersions(refs)
	var got []string
	for _, r := range refs {
		got = append(got, r.Name)
	}
	want := []string{"2.0.0", "v1.10.0", "v1.2.1", "v1.2.0", "v0.9", "beta", "nightly"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
}
```

Run: `go test ./internal/gitutil -run TestSortVersionsNewestFirst` → compile error, `SortVersions` undefined.

- [ ] **Step 2: The helper**

`internal/gitutil/versions.go`:

```go
package gitutil

import (
	"sort"
	"strconv"
	"strings"
)

// version parses "v1.2.3" or "1.2" into numeric parts. Anything else is
// not a version.
func version(name string) ([]int, bool) {
	s := strings.TrimPrefix(name, "v")
	if s == "" {
		return nil, false
	}
	var parts []int
	for _, p := range strings.Split(s, ".") {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		parts = append(parts, n)
	}
	return parts, true
}

func versionLess(a, b []int) bool {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

// SortVersions orders refs newest version first. Names that are not
// versions follow, by name.
func SortVersions(refs []Ref) {
	sort.SliceStable(refs, func(i, j int) bool {
		vi, oki := version(refs[i].Name)
		vj, okj := version(refs[j].Name)
		switch {
		case oki && okj:
			return versionLess(vj, vi)
		case oki != okj:
			return oki
		}
		return refs[i].Name < refs[j].Name
	})
}
```

- [ ] **Step 3: Use it**

In `refs` (`internal/httpd/web.go:1958`): after `tags, _ := gitutil.Refs(p.Dir, "tags")` add `gitutil.SortVersions(tags)`. In the release form's tag list (`web.go:639-646`), sort the tags the same way before filtering so the select offers the newest first.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/gitutil -run TestSortVersionsNewestFirst && go build ./... && go vet ./... && go test ./e2e -run 'TestWebUI$|TestReleaseAndBuildWeb$'` (if an assertion depends on the old order, update it).
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/gitutil/versions.go internal/gitutil/versions_test.go internal/httpd/web.go
git commit -m "web: refs and the release form order tags by version

Ref #182"
```

---

### Task 5: The editor says no before the textarea

**Files:**
- Modify: `internal/httpd/accounts.go:476-494` (`editForm`)
- Modify: `internal/web/templates/edit.html`
- Test: `e2e/accounts_test.go` (`TestWebAccounts` is the test that drives the file editor at `/edit/`; extend it)

**Interfaces:**
- The edit page struct gains `Blocked string`; when set, the template renders it and no form.

- [ ] **Step 1: Write the failing e2e test**

Append to `TestWebAccounts` in `e2e/accounts_test.go`, after its existing successful edit and using its instance, key and logged-in client:

```go
	// With signed commits required the editor cannot succeed, so the page
	// says so instead of offering a textarea.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "settings", "require-signed", "alice/app", "on"); code != 0 {
		t.Fatalf("require-signed: %s", errOut)
	}
	status, body := browserGet(t, alice, inst.base()+"/alice/app/edit/main/README.md")
	if status != 200 || !strings.Contains(body, "requires signed commits") || strings.Contains(body, "<textarea") {
		t.Fatalf("edit page under require-signed: %d\n%s", status, body)
	}
```

Use the repository path, file and variable names the test already has; the setting's command name is in `internal/control/repo.go` (grep `require-signed`).

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./e2e -run 'TestWebAccounts$'` → FAIL: the page still has a textarea.

- [ ] **Step 3: The handler**

In `editForm`, after `repo` is resolved and before reading the blob, compute:

```go
	blocked := ""
	switch {
	case repo.Settings.RequireSignedCommits:
		blocked = repo.Path() + " requires signed commits and the web editor cannot sign; edit locally and push a signed commit."
	case repo.Settings.RequireMR && slices.Contains(repo.Settings.ProtectedBranches, ref):
		blocked = "branch " + ref + " accepts changes through merge requests only; edit on another branch and open one."
	}
```

Pass `Blocked: blocked` in the page struct. Still read the blob so a missing file is a 404 either way. Add `"slices"` to the imports.

- [ ] **Step 4: The template**

`edit.html`: wrap the `<form>` in `{{if .Blocked}}<p class="empty-note">{{.Blocked}}</p>{{else}} … {{end}}`, keeping the `<h1>` and the error line outside.

- [ ] **Step 5: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/httpd && go test ./e2e -run 'TestWebAccounts$'`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/httpd/accounts.go internal/web/templates/edit.html e2e/accounts_test.go
git commit -m "web: the editor explains a refusal before the textarea

Ref #182"
```

---

### Task 6: Messages a page shows

**Files:**
- Modify: `internal/control/mr.go:1057,1132,1178`
- Modify: `internal/control/commitrefs.go:243`
- Modify: `internal/web/static/style.css` (`.syscomment`)
- Test: the e2e assertions the greps find (`grep -rn 'strategy merge\|--strategy\|closed by commit' e2e/ internal/`)

Two commits: the merge messages, then the close-event line.

- [ ] **Step 1: Grep the assertions**

Run the grep above; list the hits.

- [ ] **Step 2: Rewrite the three merge refusals in `internal/control/mr.go`**

- line 1057: `"fast-forward not possible: %s has diverged from the MR head; merge with the merge strategy, or rebase and push again"`
- line 1132: `"the MR contains merge commit %.10s; a rebase merge needs linear history — choose the merge or squash strategy"`
- line 1178: keep the sentence about the stack and replace `--strategy ff or merge` with `the fast-forward or merge strategy`.

Update the assertions found in Step 1 (they are substring checks; match the new wording). Run `go test ./internal/control && go test ./e2e -run '<the tests that assert them>'` and commit:

```bash
git commit -am "control: merge refusals name the strategy, not the flag

The same text reaches the web merge form, which has no flags.

Ref #182"
```

- [ ] **Step 3: The close-event line**

`internal/control/commitrefs.go:243`: `fmt.Sprintf("closed by %s in commit %s: %s", author, link, subject)`. Update any assertion on `closed by commit` (Step 1). In `style.css`, find `.syscomment` and add `.syscomment p { display: inline; margin: 0; }` so the rendered body and the `when` span sit on one line. Run `go test ./internal/control && go test ./e2e -run 'TestCommitMessageIssueActions$'` and commit:

```bash
git commit -am "control, web: the close event reads closed by <who> in commit <sha>

Ref #182"
```

---

### Task 7: Small template fixes, one commit each

**Files:**
- `internal/web/templates/account.html:129`; `landing.html:16`
- `internal/web/templates/settings.html` (the Save buttons listed below)
- `internal/web/templates/labels.html:6-13`; `internal/httpd/labels.go:31-37`
- `internal/web/templates/issue.html:53,65,77`; `mr.html:132,144`
- `internal/web/templates/issues.html:19-26`; `mrs.html` (the matching row)
- `internal/web/templates/globalsearch.html:4-19`
- Tests: `grep -rn` for each changed string in `e2e/` and `internal/httpd/*_test.go`, updated per commit

Do these in order, each its own commit with `Ref #182`:

- [ ] **Step 1: Copy.** `account.html:129`: `gitbay auth token mint --name laptop` → `gitbay auth token create --name laptop`. `landing.html:16`: "have an account? mint a browser session from your terminal:" → "have an account? log in from your terminal:". Commit `web: the settings page names the real token command`.

- [ ] **Step 2: Save buttons name their field.** In `settings.html` change each plain `Save` to: line 12 `Save description`, 18 `Save website`, 26 `Save default branch`, 46 `Save visibility`, 52 `Save git://`, 61 `Save checks`, 67 `Save approvals`, 73 `Save threads`, 79 `Save CODEOWNERS`, 85 `Save signing`, 112 `Save merge-only`, 140 `Save dependency checks`, 185 `Save archive`. Grep `>Save<` in `e2e/settingsweb_test.go` and the httpd tests first; a test that finds the button by its text needs the new text. Commit `web: every Save on repository settings names its field`.

- [ ] **Step 3: Labels colour column only when it means something.** In `labels.go` add `AnyColor bool` to the page struct, true when any label's `Color != ""`. In `labels.html` render the `colour` header and cell only `{{if or $.CanWrite $.AnyColor}}`. Commit `web: the labels page hides an empty colour column`.

- [ ] **Step 4: Empty states.** `issue.html:53` → `none yet`, `:65` → `nobody yet`, `:77` → `none yet`; `mr.html:132` → `nobody yet`, `:144` → `none yet`. Grep `None yet\|Nobody yet\|Nobody asked yet\|No reviews yet` in tests first. Commit `web: one shape for empty sidebars`.

- [ ] **Step 5: Issue and MR rows.** In `issues.html:25` wrap the state chip in `{{if eq $.State "all"}} … {{end}}`; in the meta line change `· <a …>{{.Milestone}}</a>` to `· in <a …>{{.Milestone}}</a>`. Apply the same two changes to the row in `mrs.html`. Grep `chip-open` in tests. Commit `web: list rows show the state only under all, and name the milestone`.

- [ ] **Step 6: Search count.** In `globalsearch.html` after the `<nav>`: `{{if .Query}}<p class="meta">{{len .Results}} {{if eq (len .Results) 1}}result{{else}}results{{end}} for <q>{{.Query}}</q>{{if .Kind}} in {{.Kind}}{{end}}</p>{{end}}`. Keep the existing `no matches` empty note. Commit `web: global search counts its results and echoes the query`.

- [ ] **Step 7: Run the checks once at the end**

`go build ./... && go vet ./... && go test ./internal/httpd && go test ./e2e -run 'TestRepoSettingsWeb$|TestLabelsWeb$|TestIssueWebTriage$|TestMRWebReviewLoop$|TestGlobalSearchAndNotificationsWeb$'`.

---

### Task 8: MR source line and both clone URLs

**Files:**
- Modify: `internal/httpd/web.go:1815-1920` (`mr` handler) and `internal/web/templates/mr.html:162-166`
- Modify: `internal/httpd/web.go:341-345` (`repoPage` in `repoFor`) and `internal/web/templates/tree.html:13`
- Test: `e2e/mrweb_test.go`, `e2e/web_test.go` (extend)

Two commits.

- [ ] **Step 1: MR source line.** In the `mr` handler compute `SourceGone bool`: true when `m.State == "source_gone"`, or when `m.SourcePath == ""` and `gitutil.ResolveRef(p.Dir, "refs/heads/"+m.SourceRef)` errors. Pass it in the page struct. In `mr.html:165` render: `into <code>{{.MR.TargetRef}}</code> · {{if eq .MR.State "merged"}}merged at{{else}}head{{end}} <code>{{short .MR.HeadSHA}}</code>{{if .SourceGone}} · <span class="chip chip-neutral">branch deleted</span>{{end}}`. Test: in the MR web test after a merge, delete the source branch over git (`git push origin --delete <branch>` with the test's env) and assert the MR page contains `branch deleted` and `merged at`. Commit `web: a merged MR shows its merged head and a deleted source branch`.

- [ ] **Step 2: Both clone URLs.** In `repoFor` add `SSHCloneURL` to `repoPage`: `"ssh://git@" + s.cfg.SiteHost() + port + "/" + repo.Path() + ".git"` where `port` is `""` when `s.cfg.SSH.Port == 22` and `":" + strconv.Itoa(port)` otherwise. In `tree.html:13`: `Clone: <code>git clone {{.SSHCloneURL}}</code> · <code>git clone {{.CloneURL}}</code>`. Test: in `e2e/web_test.go`'s repo-home test assert the body contains `ssh://git@127.0.0.1:` followed by the instance's ssh port (the harness knows it; read `startInstanceWith` for the field). Commit `web: the repository home shows the SSH clone URL beside HTTPS`.

- [ ] **Step 3: Run** `go build ./... && go vet ./... && go test ./internal/httpd && go test ./e2e -run 'TestMRWebReviewLoop$|TestWebUI$'`.

---

### Task 9: Changelog and the issue

**Files:**
- Modify: `CHANGELOG.org`

- [ ] **Step 1:** Above `* v1.20.1 — 2026-09-11` add:

```org
* v1.21.0 — unreleased

The web UI/UX sweep (#182).

- Destructive controls ask for the object's name typed beside the
  button: release delete, snippet delete and file remove, team delete,
  label remove, and SSH key, email and PGP key removal. Reversible
  controls keep a plain button.
- Login returns to the page that asked for it, and says so.
- One timestamp format everywhere, =2006-01-02 15:04 UTC=.
- Tags on the refs page and in the release form are in version order,
  newest first.
- The file editor explains up front when signed commits or
  merge-requests-only protection would refuse the commit.
- Merge refusals name the strategy rather than the flag; an issue
  closed by a commit reads "closed by <who> in commit <sha>".
- Repository settings: every Save names its field. Labels: the colour
  column appears only when it means something. Sidebars use one shape
  for empty. List rows show the state only under "all" and say "in
  <milestone>". Global search counts its results. A merged MR shows
  its merged head and a deleted source branch. The repository home
  shows the SSH clone URL beside HTTPS. The settings page names
  =auth token create=.
```

- [ ] **Step 2: Commit**

```bash
git add CHANGELOG.org
git commit -m "CHANGELOG: web UI/UX sweep

Closes #182"
```
