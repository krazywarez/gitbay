# Desktop Layout Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the web UI use a desktop screen: one centered container, per-page left columns that each carry a feature, one-line list rows, a three-column dashboard, and a two-row repository header.

**Architecture:** Every change is CSS on existing tokens plus template markup, with three handler additions: a directory listing beside a file, facet counts beside a list, and per-repository counts beside the pinned list. No new control command, no migration. Each page keeps working at phone widths because the columns stack below 64rem.

**Tech Stack:** Go 1.2x, `html/template`, one stylesheet (`internal/web/static/style.css`), `go test` unit tests in `internal/httpd` and `internal/web`, e2e tests in `e2e/` against a real instance.

**Spec:** `docs/specs/2026-09-19-desktop-layout-design.md`

## Global Constraints

- Branch `desktop-layout-spec` already holds the spec commit; all work lands on it. Never push to `main`; the MR at the end merges with `--strategy ff` (signed commits required).
- Commit subjects follow the log: `web: ...`, `httpd: ...`, `e2e: ...`, `CHANGELOG: ...`, lowercase after the prefix, no trailer, no attribution of any kind. Reference `Ref #226` in bodies.
- `--container: 100rem`. Every left column is `15rem`, sticky. Text stays at 48rem / 78ch. Columns stack below `64rem`.
- Colors and spacing use existing tokens only (`--sp-*`, `--fs-*`, `--surface`, `--line`, `--faint`, `--muted`, `--mark`, `--warn`, `--fg`, `--link`, `--hover`, `--r-ctl`, `--r-card`). Never a hex value in a rule.
- `TestEveryTemplateClassHasARule` (`internal/web/classes_test.go`) fails on any class a template uses that no `style.css` selector names. Add the rule in the same step as the markup.
- Local verification per task: `go build ./... && go vet ./... && go test ./internal/web/ ./internal/httpd/`, plus at most the one e2e test the task touches (`go test ./e2e -run TestName -count=1`). The full suite runs in CI on push.
- Do not mention Claude, LLMs or assistants anywhere: commits, comments, CHANGELOG, wiki.

---

### Task 1: One centered container

**Files:**
- Modify: `internal/web/static/style.css:366-388` (shell section), `:389-395` (repohead), `:1481-1489` (52rem breakpoint)
- Modify: `internal/web/templates/layout.html:39-69` (repohead)
- Test: `internal/web/layout_test.go` (create)

**Interfaces:**
- Produces: the `.repohead .wrap` element every later header change lives in; `--container` token used by Task 4 and Task 9.

- [ ] **Step 1: Write the failing test**

```go
package web

import (
	"strings"
	"testing"
)

// The repository header, main and footer share one centered container:
// the header's inner content is wrapped, and the stylesheet caps and
// centers all three on the same token (desktop layout spec).
func TestSharedCenteredContainer(t *testing.T) {
	layout, err := templateFS.ReadFile("templates/layout.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(layout), `<header class="repohead">\n<div class="wrap">`) &&
		!strings.Contains(string(layout), "<header class=\"repohead\">\n<div class=\"wrap\">") {
		t.Fatalf("repohead is not wrapped in .wrap")
	}
	css := string(StyleCSS)
	for _, want := range []string{
		"--container: 100rem;",
		"main.content, footer { max-width: calc(var(--container) + 2 * var(--sp-6)); margin: 0 auto; }",
		".repohead .wrap { max-width: var(--container); margin: 0 auto; }",
		"main.reading { max-width: calc(72rem + 2 * var(--sp-6)); margin: 0 auto; }",
		"main.bounded { max-width: calc(48rem + 2 * var(--sp-6)); margin: 0 auto; }",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("style.css lacks %q", want)
		}
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/web/ -run TestSharedCenteredContainer`
Expected: FAIL, "repohead is not wrapped in .wrap"

- [ ] **Step 3: Add the token and the container rules**

In `style.css`, inside `:root {` after the `--rail-mark-box: 32px;` line (around line 111), add:

```css
  /* the one page container: header content, main and footer align on it */
  --container: 100rem;
```

Replace lines 371-377 (the `main.content` rule and the three width rules) with:

```css
main.content {
  flex: 1;
  width: 100%;
  padding: var(--sp-5) var(--sp-6) var(--sp-7);
}
main.content, footer { max-width: calc(var(--container) + 2 * var(--sp-6)); margin: 0 auto; }
main.wide { max-width: calc(var(--container) + 2 * var(--sp-6)); }
main.reading { max-width: calc(72rem + 2 * var(--sp-6)); margin: 0 auto; }
main.bounded { max-width: calc(48rem + 2 * var(--sp-6)); margin: 0 auto; }
```

Replace the `.repohead` rule (line 390-393) with:

```css
.repohead {
  padding: var(--sp-4) 0 0;
  border-bottom: 1px solid var(--line);
}
.repohead .wrap { max-width: var(--container); margin: 0 auto; padding: 0 var(--sp-6); }
```

In the `@media (max-width: 52rem)` block, change `.repohead { padding: var(--sp-3) var(--sp-4) 0; }` to:

```css
  .repohead { padding: var(--sp-3) 0 0; }
  .repohead .wrap { padding: 0 var(--sp-4); }
```

- [ ] **Step 4: Wrap the header content**

In `layout.html`, line 39 `<header class="repohead">` becomes:

```html
<header class="repohead">
<div class="wrap">
```

and line 69 `</header>` becomes:

```html
</div>
</header>
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/web/ ./internal/httpd/`
Expected: PASS (the classes test sees `.wrap` in the stylesheet)

- [ ] **Step 6: Commit**

```bash
git add internal/web/static/style.css internal/web/templates/layout.html internal/web/layout_test.go
git commit -m "web: one centered container for header, main and footer

Ref #226"
```

---

### Task 2: Repository header on two rows

**Files:**
- Modify: `internal/web/templates/layout.html:41-58`
- Modify: `internal/web/static/style.css:396-425` (identity, repodesc, repometa, toggles rules)
- Test: `internal/httpd/repohead_test.go` (create)

**Interfaces:**
- Consumes: `testRepoPage()` from `internal/httpd/buildpages_test.go:12`.

- [ ] **Step 1: Write the failing test**

```go
package httpd

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/web"
)

// The repository header is two rows: identity with the description and
// the buttons, then the tabs. The toggles hint is title text on the
// buttons, not a line of its own (desktop layout spec).
func TestRepoHeaderTwoRows(t *testing.T) {
	var sb strings.Builder
	p := testRepoPage()
	p.Viewer = "alice"
	p.Desc = "A CLI-first git forge."
	p.Topics = []string{"cli"}
	p.Tab = "files"
	err := web.Render(&sb, "builds.html", struct {
		repoPage
		Builds      []control.BuildOut
		Jobs        []control.JobOut
		Runs        []buildRun
		Filter      buildFilter
		FilterLinks []buildFilterLink
		Refs        []string
		CanWrite    bool
		Notice      string
	}{p, nil, nil, nil, buildFilter{}, nil, nil, true, ""})
	if err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	if strings.Contains(out, `class="toggles"`) || strings.Contains(out, "Pinned shows on your dashboard.") {
		t.Error("the toggles hint still renders as a line")
	}
	for _, want := range []string{
		`title="Pinned repositories show on your dashboard"`,
		`title="Watching sends every issue, request and build to your inbox"`,
		`title="Bookmarked lists it under Bookmarks"`,
		`<p class="repodesc">A CLI-first git forge.`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("header lacks %q", want)
		}
	}
	// the description sits inside the identity row, before the buttons
	if strings.Index(out, `class="repodesc"`) > strings.Index(out, `action="/krz/gitbay/pin"`) {
		t.Error("description renders after the buttons; it belongs in the identity row")
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/httpd/ -run TestRepoHeaderTwoRows`
Expected: FAIL on the `title=` strings

- [ ] **Step 3: Rewrite the identity row**

Replace `layout.html` lines 41-58 (from `<div class="identity">` through the `{{end}}` that closes `{{if eq $top "code"}}`) with:

```html
  {{$top := topTab (str $ "Tab")}}
  <div class="identity">
    {{if field $ "RepoHome"}}<h1 class="repotitle"><a class="owner" href="/{{.OwnerName}}">{{.OwnerName}}</a><span class="sep">/</span>{{.Name}}</h1>
    {{else}}<p class="repotitle"><a class="owner" href="/{{.OwnerName}}">{{.OwnerName}}</a><span class="sep">/</span><a href="/{{.OwnerName}}/{{.Name}}">{{.Name}}</a></p>{{end}}
    {{if eq .Visibility "private"}}<span class="chip">Private</span>{{end}}
    {{if .Settings.Archived}}<span class="chip">Archived</span>{{end}}
    {{/* Description, topics and website belong to the code tab, inline
         with the name so the header is two rows on every page. */}}
    {{if eq $top "code"}}{{if or (field $ "Desc") (field $ "Topics") $.Repo.Settings.Website}}<p class="repodesc">{{with field $ "Desc"}}{{.}}{{end}} {{with field $ "Topics"}}{{range .}}<a class="chip topic" href="/explore?q={{.}}">{{.}}</a> {{end}}{{end}}{{with $.Repo.Settings.Website}}<a class="site" href="{{.}}" rel="nofollow">{{.}}</a>{{end}}</p>{{end}}{{end}}
    <span class="grow"></span>
    {{if $.Viewer}}<form method="post" action="/{{.OwnerName}}/{{.Name}}/pin" class="inline"><button type="submit" class="btn" aria-pressed="{{if field $ "Pinned"}}true{{else}}false{{end}}" title="Pinned repositories show on your dashboard"><span aria-hidden="true">{{if field $ "Pinned"}}★{{else}}☆{{end}}</span> {{if field $ "Pinned"}}Pinned{{else}}Pin{{end}}</button></form>
    <form method="post" action="/{{.OwnerName}}/{{.Name}}/watch" class="inline"><button type="submit" class="btn" aria-pressed="{{if eq (str $ "Watch") "watching"}}true{{else}}false{{end}}" title="Watching sends every issue, request and build to your inbox">{{if eq (str $ "Watch") "watching"}}Watching{{else}}Watch{{end}}</button></form>
    <form method="post" action="/{{.OwnerName}}/{{.Name}}/bookmark" class="inline"><button type="submit" class="btn" aria-pressed="{{if field $ "Marked"}}true{{else}}false{{end}}" title="Bookmarked lists it under Bookmarks">{{if field $ "Marked"}}Bookmarked{{else}}Bookmark{{end}}</button></form>
    <form method="post" action="/{{.OwnerName}}/{{.Name}}/fork" class="inline"><button type="submit" class="btn">Fork</button></form>{{end}}
  </div>
  {{if eq $top "code"}}{{if field $ "Mirrors"}}<p class="repometa">{{range $i, $m := field $ "Mirrors"}}{{if $i}} · {{end}}{{if eq $m.Direction "push"}}mirrors to{{else}}mirrors from{{end}} <a href="{{$m.URL}}" rel="nofollow">{{$m.Target}}</a>{{if $m.Error}}, <span class="bad">sync error: {{$m.Error}}</span>{{else if $m.Synced}}, synced {{$m.Synced}}{{end}}{{end}}</p>{{end}}{{end}}
```

Delete the old `{{$top := ...}}` line that followed the identity block (it now sits above it) and the `<p class="toggles">` line.

- [ ] **Step 4: Style the inline description and remove the toggles rule**

In `style.css` replace the `.repodesc` rule (lines 412-417) and delete the `.toggles` rule (line 419):

```css
.repodesc {
  margin: 0 0 0 var(--sp-2);
  color: var(--muted);
  font-size: var(--fs-2);
  display: inline-flex; align-items: center; gap: var(--sp-2); flex-wrap: wrap;
  min-width: 0;
}
.repodesc a.site { color: var(--muted); }
.repodesc a.site:hover { color: var(--link); }
```

Add under the `@media (max-width: 52rem)` block:

```css
  /* a phone shows the description under the name, not beside it */
  .repodesc { flex-basis: 100%; margin-left: 0; }
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/web/ ./internal/httpd/ && go test ./e2e -run 'TestWebUI|TestTreeSearchCodeAndClone' -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/web/templates/layout.html internal/web/static/style.css internal/httpd/repohead_test.go
git commit -m "web: the repository header is two rows

Description, topics and website sit beside the name; the toggles hint is
title text on the buttons.

Ref #226"
```

---

### Task 3: One-line list rows and wide list pages

**Files:**
- Modify: `internal/web/templates/issues.html:24-32`, `mrs.html:19-32`, `notifications.html:12-21`, `globalsearch.html:22-31`, `dashboard.html:2-12`, `explore.html:1`
- Modify: `internal/web/static/style.css:892-907` (issuelist), `:867-877` (repolist), `:104-109` of `layout.html` (reporow)
- Test: `internal/web/widths_test.go` (create)

**Interfaces:**
- Produces: `ul.issuelist.rows` and `ul.repolist.rows`, the row format every list task after this reuses.

- [ ] **Step 1: Write the failing test**

```go
package web

import (
	"strings"
	"testing"
)

// List pages and the dashboard render at the container width; text pages
// keep the reading cap (desktop layout spec).
func TestListPagesAreWide(t *testing.T) {
	wide := []string{"dashboard.html", "issues.html", "mrs.html", "explore.html", "notifications.html", "globalsearch.html", "builds.html"}
	for _, name := range wide {
		src, err := templateFS.ReadFile("templates/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(string(src), `{{define "width"}}wide{{end}}`) {
			t.Errorf("%s does not declare width wide", name)
		}
	}
	for _, name := range []string{"issue.html", "wiki.html", "owner.html"} {
		src, _ := templateFS.ReadFile("templates/" + name)
		if strings.Contains(string(src), `{{define "width"}}wide{{end}}`) {
			t.Errorf("%s is a text page and must not be wide", name)
		}
	}
	for _, name := range []string{"issues.html", "mrs.html", "notifications.html", "globalsearch.html", "dashboard.html"} {
		src, _ := templateFS.ReadFile("templates/" + name)
		if !strings.Contains(string(src), `<ul class="issuelist rows">`) {
			t.Errorf("%s does not use one-line rows", name)
		}
	}
	if src, _ := templateFS.ReadFile("templates/explore.html"); !strings.Contains(string(src), `<ul class="repolist rows">`) {
		t.Error("explore.html does not use one-line rows")
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/web/ -run TestListPagesAreWide`
Expected: FAIL for every listed template

- [ ] **Step 3: Add the row rules**

After the `ul.issuelist .title a:hover` rule (line 907) add:

```css
/* one-line rows: title, labels, then the meta pushed right. Above 64rem
   a list is a table, not prose (desktop layout spec). */
ul.issuelist.rows li { align-items: center; padding: var(--sp-2) var(--sp-4); }
ul.issuelist.rows .issuemain { display: flex; align-items: baseline; gap: var(--sp-3); min-width: 0; }
ul.issuelist.rows .title { flex: 1 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
ul.issuelist.rows .title .chip { margin-left: var(--sp-1); }
ul.issuelist.rows .meta { flex: none; color: var(--muted); font-size: var(--fs-1); white-space: nowrap; }
ul.issuelist.rows .meta .repo { color: var(--fg); }
```

After the `ul.repolist .topics, ul.repolist .meta` rule (line 877) add:

```css
ul.repolist.rows li { display: flex; align-items: baseline; gap: var(--sp-3); padding: var(--sp-2) var(--sp-4); }
ul.repolist.rows .reponame { flex: none; font-size: var(--fs-2); }
ul.repolist.rows .desc { flex: 1 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--muted); font-size: var(--fs-2); margin: 0; }
ul.repolist.rows .topics, ul.repolist.rows .meta { flex: none; margin: 0; white-space: nowrap; }
ul.repolist.rows .meta { font-size: var(--fs-1); }
```

After the `ul.loglist .commitside` rule (line 865) add, for the builds page (Task 7 applies it):

```css
ul.loglist.rows li { align-items: center; padding: var(--sp-2) var(--sp-4); }
ul.loglist.rows .commitmain { display: flex; align-items: baseline; gap: var(--sp-3); min-width: 0; }
ul.loglist.rows .meta { flex: none; color: var(--muted); font-size: var(--fs-1); white-space: nowrap; }
```

In the `@media (max-width: 62rem)` block add:

```css
  /* rows go back to two lines where one does not fit */
  ul.issuelist.rows .issuemain, ul.repolist.rows li, ul.loglist.rows .commitmain { display: block; }
  ul.issuelist.rows .title, ul.repolist.rows .desc { white-space: normal; }
```

- [ ] **Step 4: Change the templates**

`issues.html`: line 1 becomes two lines:

```
{{define "width"}}wide{{end}}
{{define "title"}}issues · {{.Repo.OwnerName}}/{{.Repo.Name}}{{end}}
```

and `<ul class="issuelist">` becomes `<ul class="issuelist rows">`. The `<li>` body is unchanged: the CSS makes it one line.

`mrs.html`: same two changes (`merge requests ·` title).

`notifications.html`: add `{{define "width"}}wide{{end}}` as line 1; `<ul class="issuelist">` → `<ul class="issuelist rows">`.

`globalsearch.html`: `<ul class="issuelist">` → `<ul class="issuelist rows">` (already wide).

`dashboard.html`: add `{{define "width"}}wide{{end}}` as line 1; in the `itemlist` define, `<ul class="issuelist">` → `<ul class="issuelist rows">`, and the meta line becomes:

```html
    <p class="meta"><span class="repo">{{.RepoPath}}{{if eq $.Kind "mrs"}}!{{else}}#{{end}}{{.Number}}</span> · <a href="/{{.Author}}">{{.Author}}</a> · {{when .UpdatedAt}}{{if eq .State "source_gone"}} · <span class="chip chip-source_gone">source gone</span>{{end}}</p>
```

`explore.html`: add `{{define "width"}}wide{{end}}` as line 1; `<ul class="repolist">` → `<ul class="repolist rows">`.

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/web/ ./internal/httpd/ && go test ./e2e -run 'TestMRListRows|TestIssueWebTriage|TestDashboard$' -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/web
git commit -m "web: one-line rows on the list pages, at the container width

Ref #226"
```

---

### Task 4: Dashboard as three columns with count tiles and pinned counts

**Files:**
- Modify: `internal/httpd/web.go:185-200` (dashboard handler)
- Create: `internal/httpd/dashpins.go`, `internal/httpd/dashpins_test.go`
- Modify: `internal/web/templates/dashboard.html`
- Modify: `internal/web/static/style.css` (after the `.pinned` rules, line 994; after `.feed` rules, line 1030)
- Modify: `e2e/dashboard_test.go:95-119`, `:354-357`

**Interfaces:**
- Consumes: `s.st.PinnedRepos(userID int64) ([]store.Repo, error)`, `s.st.OpenCounts(repoID int64) (issues, mrs int)`, `s.st.ListBuilds(repoID int64, f store.BuildFilter, limit int) ([]store.Build, error)`, `policy.CanRead(viewer, repo, grant)`, `s.st.AccessRole(repoID, userID)`.
- Produces: `pinnedRow` and `func (s *Server) pinnedRows(viewer store.User) []pinnedRow`.

- [ ] **Step 1: Write the failing test**

`internal/httpd/dashpins_test.go`:

```go
package httpd

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
)

// The dashboard is three columns: pinned repositories with counts, the
// tile strip and queue rows, the activity feed. Tiles carry every queue's
// count; only a non-empty queue lists rows (desktop layout spec).
func TestDashboardTilesAndPins(t *testing.T) {
	var sb strings.Builder
	var base basePage
	base.Viewer = "alice"
	err := web.Render(&sb, "dashboard.html", struct {
		basePage
		Tab      string
		Pins     []pinnedRow
		Reviews  []store.DashboardItem
		Assigned []store.DashboardItem
		MRs      []store.DashboardItem
		Issues   []store.DashboardItem
		Feed     []feedLine
	}{base, "dashboard", []pinnedRow{{Owner: "krz", Name: "gitbay", Issues: 3, MRs: 0, Build: "success"}}, nil, nil, nil,
		[]store.DashboardItem{{RepoPath: "krz/gitbay", Number: 1, Title: "one", Author: "alice", State: "open"}}, nil})
	if err != nil {
		t.Fatal(err)
	}
	out := sb.String()
	for _, want := range []string{
		`<div class="dashgrid">`,
		`<aside class="dashpins" aria-label="Pinned repositories">`,
		`<span class="owner">krz/</span>gitbay</a>`,
		`<b class="wants">3</b>`, `<span class="dot ok"></span>`,
		`<a class="tile wants" href="#issues"><b>1</b><span>open issues</span></a>`,
		`<div class="tile"><b>0</b><span>waiting on your review</span></div>`,
		`<div class="tile"><b>0</b><span>assigned to you</span></div>`,
		`<div class="tile"><b>0</b><span>open merge requests</span></div>`,
		`<h2 id="issues">Open issues <span class="count">1</span></h2>`,
		`<aside class="feedcol" aria-label="Recent activity">`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("dashboard lacks %q", want)
		}
	}
	if strings.Contains(out, `<h2 class="empty">`) {
		t.Error("an empty queue still renders as a heading; the tile carries it")
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/httpd/ -run TestDashboardTilesAndPins`
Expected: FAIL, "undefined: pinnedRow"

- [ ] **Step 3: The pinned rows read**

`internal/httpd/dashpins.go`:

```go
package httpd

import (
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/store"
)

// pinnedRow is one pinned repository on the dashboard with the counts
// that say whether it wants attention: open issues, open merge requests
// and the newest build's status ("" when it has none).
type pinnedRow struct {
	Owner  string
	Name   string
	Issues int
	MRs    int
	Build  string
}

// pinnedRows reads the viewer's pinned repositories the way railFor does,
// then adds the counts. Three reads per pinned repository, on the
// dashboard only.
func (s *Server) pinnedRows(viewer store.User) []pinnedRow {
	pinned, _ := s.st.PinnedRepos(viewer.ID)
	var rows []pinnedRow
	for _, rp := range pinned {
		grant, _ := s.st.AccessRole(rp.ID, viewer.ID)
		if !policy.CanRead(viewer, rp, grant) {
			continue
		}
		row := pinnedRow{Owner: rp.OwnerName, Name: rp.Name}
		row.Issues, row.MRs = s.st.OpenCounts(rp.ID)
		if builds, err := s.st.ListBuilds(rp.ID, store.BuildFilter{}, 1); err == nil && len(builds) > 0 {
			row.Build = builds[0].Status
		}
		rows = append(rows, row)
	}
	return rows
}
```

In `web.go` `dashboard`, add `Pins []pinnedRow` to the struct after `Tab` and pass `s.pinnedRows(viewer)`:

```go
	s.render(w, "dashboard.html", struct {
		basePage
		Tab      string
		Pins     []pinnedRow
		Reviews  []store.DashboardItem
		Assigned []store.DashboardItem
		MRs      []store.DashboardItem
		Issues   []store.DashboardItem
		Feed     []feedLine
	}{s.baseFor(viewer), "dashboard", s.pinnedRows(viewer), reviews, assigned, mrs, issues, feedLines(events)})
```

- [ ] **Step 4: The template**

Replace `dashboard.html` from `{{define "queue"}}` to the end with:

```
{{define "queue"}}
<h2 id="{{$.ID}}">{{$.Title}} <span class="count">{{len $.Items}}</span></h2>
{{if $.Hint}}<p class="hint">{{$.Hint}}</p>{{end}}
{{template "itemlist" dict "Items" $.Items "Kind" $.Kind "Empty" $.Empty}}
{{end}}
{{define "tile"}}{{if $.N}}<a class="tile wants" href="#{{$.ID}}"><b>{{$.N}}</b><span>{{$.Label}}</span></a>{{else}}<div class="tile"><b>0</b><span>{{$.Label}}</span></div>{{end}}{{end}}
{{define "content"}}
<div class="dashgrid">

<aside class="dashpins" aria-label="Pinned repositories">
  <h2 class="colhead">Pinned</h2>
  {{if .Pins}}<ul class="pins">
  {{range .Pins}}<li><a href="/{{.Owner}}/{{.Name}}"><span class="owner">{{.Owner}}/</span>{{.Name}}</a><span class="n" title="{{.Issues}} open issue{{if ne .Issues 1}}s{{end}}, {{.MRs}} open merge request{{if ne .MRs 1}}s{{end}}{{with .Build}}, last build {{.}}{{end}}">{{if .Issues}}<b class="wants">{{.Issues}}</b>{{else}}<b>0</b>{{end}} {{if .MRs}}<b class="wants">{{.MRs}}</b>{{else}}<b>0</b>{{end}} <span class="dot{{if eq .Build "success"}} ok{{else if eq .Build "failure"}} bad{{else if .Build}} pend{{end}}"></span></span></li>
  {{end}}</ul>
  <p class="meta">issues · merge requests · last build</p>
  {{else}}<p class="none">Nothing pinned yet. Press Pin on a repository.</p>{{end}}
</aside>

<section class="dashmain">
<h1>Dashboard</h1>
<div class="tiles">
  {{template "tile" dict "N" (len .Issues) "ID" "issues" "Label" "open issues"}}
  {{template "tile" dict "N" (len .Reviews) "ID" "reviews" "Label" "waiting on your review"}}
  {{template "tile" dict "N" (len .Assigned) "ID" "assigned" "Label" "assigned to you"}}
  {{template "tile" dict "N" (len .MRs) "ID" "mrs" "Label" "open merge requests"}}
</div>
{{if .Reviews}}{{template "queue" dict "ID" "reviews" "Title" "Waiting on your review" "Items" .Reviews "Kind" "mrs" "Empty" "Nothing waiting on you"}}{{end}}
{{if .Assigned}}{{template "queue" dict "ID" "assigned" "Title" "Assigned to you" "Items" .Assigned "Kind" "issues" "Empty" "Nothing assigned to you"}}{{end}}
{{if .MRs}}{{template "queue" dict "ID" "mrs" "Title" "Open merge requests" "Items" .MRs "Kind" "mrs" "Empty" "No open merge requests" "Hint" "Yours anywhere, and every one in a repository you can write to."}}{{end}}
{{if .Issues}}{{template "queue" dict "ID" "issues" "Title" "Open issues" "Items" .Issues "Kind" "issues" "Empty" "No open issues" "Hint" "Yours anywhere, and every one in a repository you can write to."}}{{end}}
{{if not (or .Reviews .Assigned .MRs .Issues)}}<p class="none">Nothing open anywhere you can write to.</p>{{end}}
</section>

<aside class="feedcol" aria-label="Recent activity">
  <h2 class="colhead">Recent activity</h2>
  {{range .Feed}}<p class="feedline">{{if eq .State "failure"}}<span class="dot bad"></span>{{else if eq .State "success"}}<span class="dot ok"></span>{{else if .State}}<span class="dot pend"></span>{{end}}<a href="/{{.Actor}}">{{.Actor}}</a> {{.Verb}} <a href="{{.URL}}"{{if .Jobs}} title="{{join .Jobs ", "}}"{{end}}>{{.Ref}}</a><br><span class="none">{{.Repo}} · <span title="{{whenT .WhenT}}">{{ago .WhenT}}</span></span></p>
  {{else}}<p class="none">No activity yet</p>{{end}}
</aside>

</div>
{{end}}
```

The `dict` helper takes key/value pairs; `len .Issues` inside `dict` needs parentheses, as written.

- [ ] **Step 5: The stylesheet**

Replace the `.pinned` rules (lines 992-994) with:

```css
/* ---- dashboard: pinned column, tiles and queues, feed ---- */
.dashgrid { display: grid; grid-template-columns: 15rem minmax(0, 1fr) 20rem; gap: var(--sp-6); align-items: start; }
.dashgrid h1 { margin-top: 0; }
.dashgrid > aside { position: sticky; top: var(--sp-5); }
.dashgrid .dashmain h2 { margin-top: var(--sp-5); }
.dashgrid .dashmain .tiles + h2 { margin-top: 0; }
.colhead {
  margin: 0 0 var(--sp-2);
  font-size: var(--fs-0);
  font-weight: 500;
  letter-spacing: 0.08em;
  text-transform: uppercase;
  color: var(--muted);
}
.pins { list-style: none; margin: 0; padding: 0; font-size: var(--fs-2); }
.pins li { display: flex; align-items: center; gap: var(--sp-2); padding: 6px 0; border-bottom: 1px solid var(--faint); }
.pins li:last-child { border-bottom: 0; }
.pins a { color: var(--fg); min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.pins a:hover { color: var(--link); }
.pins .owner { color: var(--muted); }
.pins .n { margin-left: auto; flex: none; display: flex; gap: var(--sp-2); font-size: var(--fs-1); color: var(--muted); font-variant-numeric: tabular-nums; }
.pins .n b { font-weight: 600; color: var(--fg); }
.pins .n b.wants { color: var(--warn); }
.pins .dot { margin: 0; }
/* count tiles: the queues' sizes in one strip; a non-zero count is orange,
   what wants you */
.tiles { display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: var(--sp-3); margin: var(--sp-4) 0 var(--sp-5); }
.tile {
  display: block;
  background: var(--surface);
  border: 1px solid var(--line);
  border-radius: var(--r-card);
  padding: var(--sp-3) var(--sp-4);
  color: var(--fg);
}
a.tile:hover { background: var(--hover); text-decoration: none; }
.tile b { display: block; font-size: var(--fs-5); font-weight: 600; line-height: 1.2; }
.tile.wants b { color: var(--warn); }
.tile span { font-size: var(--fs-1); color: var(--muted); }
.feedcol .feedline { font-size: var(--fs-2); margin-bottom: var(--sp-2); }
.feedcol .feedline .none { font-size: var(--fs-1); }
```

Delete the `.feed` rules (lines 1026-1030): nothing uses `.feed` any more. Keep `.feedline`.

Add to the `@media (max-width: 62rem)` block:

```css
  .dashgrid { grid-template-columns: 1fr; }
  .dashgrid > aside { position: static; }
  /* the pinned column goes back to a chip row on a phone */
  .pins { display: flex; flex-wrap: wrap; gap: var(--sp-2); }
  .pins li { border: 1px solid var(--line); border-radius: var(--r-ctl); padding: var(--sp-1) var(--sp-3); }
  .pins .n, .dashpins .meta { display: none; }
  .tiles { grid-template-columns: repeat(2, minmax(0, 1fr)); }
```

And a new block, above the 62rem one:

```css
@media (max-width: 80rem) {
  .dashgrid { grid-template-columns: minmax(0, 1fr) 20rem; }
  .dashgrid > .dashpins { grid-column: 1 / -1; position: static; }
}
```

- [ ] **Step 6: Update the e2e assertions**

In `e2e/dashboard_test.go` replace lines 110-118 (the `emptyHeading` block) with:

```go
	// The empty queue is a tile with a zero; a populated one is a tile
	// that links to its rows, which render in the middle column.
	if !strings.Contains(body, `<div class="tile"><b>0</b><span>assigned to you</span></div>`) {
		t.Fatalf("dashboard missing the zero tile for assigned:\n%s", body)
	}
	if !strings.Contains(body, `<a class="tile wants" href="#reviews"><b>1</b><span>waiting on your review</span></a>`) {
		t.Fatalf("dashboard missing the review tile:\n%s", body)
	}
	if !strings.Contains(body, `<h2 id="reviews">Waiting on your review`) || strings.Contains(body, `<h2 class="empty">`) {
		t.Fatalf("queues do not render as tiles plus rows:\n%s", body)
	}
```

Replace lines 354-357 with:

```go
	if !strings.Contains(after, `<div class="tile"><b>0</b><span>waiting on your review</span></div>`) {
		t.Fatalf("reviewed MR still waiting:\n%s", after)
	}
```

In the `want` list at line 96-100, keep every entry; add `` `class="pins"` ``.

- [ ] **Step 7: Run the tests**

Run: `go test ./internal/web/ ./internal/httpd/ && go test ./e2e -run 'TestDashboard' -count=1`
Expected: PASS

- [ ] **Step 8: Commit**

```bash
git add internal/httpd/dashpins.go internal/httpd/dashpins_test.go internal/httpd/web.go internal/web e2e/dashboard_test.go
git commit -m "web: the dashboard is three columns

Pinned repositories with counts, a tile per queue, the feed as an aside.

Ref #226"
```

---

### Task 5: File navigator beside blob, blame and edit

**Files:**
- Create: `internal/httpd/filenav.go`, `internal/httpd/filenav_test.go`
- Modify: `internal/httpd/web.go:512-527` (extract the sort), `:559-604` (blob), `:813-896` (blame)
- Modify: `internal/httpd/accounts.go:474-518` (editPage, editForm)
- Modify: `internal/web/templates/layout.html` (new `filenav` partial), `blob.html`, `blame.html`, `edit.html`
- Modify: `internal/web/static/style.css` (after the `.pathbar` rules, line 1287)
- Create: `e2e/filenav_test.go`

**Interfaces:**
- Consumes: `gitutil.ListTree(dir, ref, path string) ([]gitutil.TreeEntry, error)`, `gitutil.TreeEntry{Type, Name}`, `store.Repo.Path() string`.
- Produces: `fileNav`, `func fileNavFor(repoPath, ref, filePath string, entries []gitutil.TreeEntry) fileNav`, `func sortDirsFirst(entries []gitutil.TreeEntry)`.

- [ ] **Step 1: Write the failing unit test**

`internal/httpd/filenav_test.go`:

```go
package httpd

import (
	"testing"

	"gitbay.org/gitbay/internal/gitutil"
)

// The navigator lists the file's directory, directories first, links each
// entry to its tree or blob page, marks the file itself, and links the
// parent (the tree root when the file is at the top).
func TestFileNavMarksCurrentAndLinksParent(t *testing.T) {
	entries := []gitutil.TreeEntry{
		{Type: "blob", Name: "main.go"},
		{Type: "tree", Name: "sub"},
		{Type: "blob", Name: "util.go"},
	}
	nav := fileNavFor("krz/gitbay", "main", "cmd/gitbay/util.go", entries)
	if nav.Title != "cmd/gitbay" {
		t.Errorf("title = %q", nav.Title)
	}
	if nav.Parent != "/krz/gitbay/tree/main/cmd" {
		t.Errorf("parent = %q", nav.Parent)
	}
	if len(nav.Entries) != 3 || nav.Entries[0].Name != "sub/" || !nav.Entries[0].Dir {
		t.Fatalf("entries not directories-first: %+v", nav.Entries)
	}
	if nav.Entries[0].URL != "/krz/gitbay/tree/main/cmd/gitbay/sub" {
		t.Errorf("dir url = %q", nav.Entries[0].URL)
	}
	if nav.Entries[2].Name != "util.go" || !nav.Entries[2].Current || nav.Entries[2].URL != "/krz/gitbay/blob/main/cmd/gitbay/util.go" {
		t.Errorf("current entry: %+v", nav.Entries[2])
	}
	if nav.Entries[1].Current {
		t.Error("main.go marked current")
	}

	root := fileNavFor("krz/gitbay", "main", "Makefile", []gitutil.TreeEntry{{Type: "blob", Name: "Makefile"}})
	if root.Title != "gitbay" || root.Parent != "" {
		t.Errorf("root nav: title %q parent %q", root.Title, root.Parent)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/httpd/ -run TestFileNavMarksCurrentAndLinksParent`
Expected: FAIL, "undefined: fileNavFor"

- [ ] **Step 3: The navigator**

`internal/httpd/filenav.go`:

```go
package httpd

import (
	"path"
	"sort"

	"gitbay.org/gitbay/internal/gitutil"
)

// fileNav is the column beside a file: its directory's entries, the file
// marked, and a link up. It is the tree page's listing rendered as a
// list, so reading a repository does not mean going back for each file.
type fileNav struct {
	Title   string // the directory, or the repository name at the root
	Parent  string // URL of the parent tree; "" at the root
	Entries []fileNavEntry
}

type fileNavEntry struct {
	Name    string // directories carry a trailing slash
	URL     string
	Dir     bool
	Current bool
}

// sortDirsFirst orders a listing by shape before name, stably, so each
// group keeps the order git gave it. The tree page and the navigator
// share it.
func sortDirsFirst(entries []gitutil.TreeEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Type == "tree" && entries[j].Type != "tree"
	})
}

// fileNavFor builds the navigator for filePath from its directory's
// entries. repoPath is owner/name.
func fileNavFor(repoPath, ref, filePath string, entries []gitutil.TreeEntry) fileNav {
	dir := path.Dir(filePath)
	if dir == "." {
		dir = ""
	}
	base := "/" + repoPath
	nav := fileNav{Title: dir}
	if dir == "" {
		nav.Title = path.Base(repoPath)
	} else if up := path.Dir(dir); up == "." {
		nav.Parent = base + "/tree/" + ref
	} else {
		nav.Parent = base + "/tree/" + ref + "/" + up
	}
	sortDirsFirst(entries)
	for _, e := range entries {
		full := path.Join(dir, e.Name)
		ent := fileNavEntry{Name: e.Name, Dir: e.Type == "tree", Current: full == filePath}
		if ent.Dir {
			ent.Name += "/"
			ent.URL = base + "/tree/" + ref + "/" + full
		} else {
			ent.URL = base + "/blob/" + ref + "/" + full
		}
		nav.Entries = append(nav.Entries, ent)
	}
	return nav
}
```

In `web.go` `renderTree` replace the inline `sort.SliceStable(...)` call and its comment (lines 522-527) with `sortDirsFirst(entries)`. Remove the `sort` import if nothing else in the file uses it (check with `go build`).

- [ ] **Step 4: Attach it to blob, blame and edit**

In `blob` (`web.go`), after `branches, _ := gitutil.Refs(p.Dir, "heads")` add:

```go
	navEntries, _ := gitutil.ListTree(p.Dir, p.Ref, navDir(filePath))
	nav := fileNavFor(p.Repo.Path(), p.Ref, filePath, navEntries)
```

and add `Nav fileNav` as the last field of the anonymous struct, passing `nav` last. Add to `filenav.go`:

```go
// navDir is the directory ListTree wants for filePath: "" at the root.
func navDir(filePath string) string {
	if d := path.Dir(filePath); d != "." {
		return d
	}
	return ""
}
```

In `blame`, before the render add the same two lines and add `Nav fileNav` to its struct, passing `nav`.

In `accounts.go`, add `Nav fileNav` to `editPage` and in `editForm`, before `s.render`, add:

```go
	navEntries, _ := gitutil.ListTree(dir, "refs/heads/"+ref, navDir(filePath))
	nav := fileNavFor(repo.Path(), ref, filePath, navEntries)
```

and `Nav: nav,` in the `editPage{...}` literal.

- [ ] **Step 5: The partial and the templates**

In `layout.html` after the `refmenu` define add:

```
{{define "filenav"}}<nav class="filenav" aria-label="Files">
  <h2 class="colhead">{{.Title}}</h2>
  <ul>
  {{with .Parent}}<li><a class="up" href="{{.}}">..</a></li>{{end}}
  {{range .Entries}}<li><a{{if .Dir}} class="dir"{{end}}{{if .Current}} aria-current="page"{{end}} href="{{.URL}}">{{.Name}}</a></li>
  {{end}}</ul>
</nav>{{end}}
```

`blob.html`: after `<h1 class="vh">{{.Path}}</h1>` insert `<div class="blobgrid">` then `{{template "filenav" .Nav}}` then `<div class="blobmain">`; before the closing `{{end}}` of the content define add `</div>\n</div>`.

`blame.html`: same wrapping around everything after the `<h1 class="vh">`.

`edit.html`: same wrapping around the `.pathbar`/form block; the `.Nav` field is on `editPage`.

- [ ] **Step 6: The stylesheet**

After the `.crumbs strong` rule add:

```css
/* ---- file navigator: the directory beside a file ---- */
.blobgrid { display: grid; grid-template-columns: 15rem minmax(0, 1fr); gap: var(--sp-6); align-items: start; }
.blobmain { min-width: 0; }
.filenav { position: sticky; top: var(--sp-4); font-size: var(--fs-2); }
.filenav ul { list-style: none; margin: 0; padding: 0; }
.filenav li a {
  display: block;
  padding: 3px var(--sp-2);
  border-radius: var(--r-ctl);
  color: var(--fg);
  font-family: var(--mono);
  font-size: var(--fs-1);
  overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
}
.filenav li a:hover { background: var(--hover); text-decoration: none; }
.filenav li a[aria-current] { background: var(--surface); box-shadow: inset 2px 0 0 var(--mark); font-weight: 600; }
.filenav li a.dir { color: var(--link); }
.filenav li a.up { color: var(--muted); }
```

In the `@media (max-width: 62rem)` block add:

```css
  /* the tree page is the navigator on a phone */
  .blobgrid { grid-template-columns: 1fr; }
  .filenav { display: none; }
```

- [ ] **Step 7: The e2e test**

`e2e/filenav_test.go`:

```go
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A file page lists its directory beside the file, marks the file, and
// links up (desktop layout spec).
func TestFileNavigator(t *testing.T) {
	inst := startInstance(t)
	key := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", key+".pub")
	if _, errOut, code := inst.ssh(t, key, "", "repo", "create", "alice/nav"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	// the same clone-commit-push shape TestWebUI uses (e2e/web_test.go:41-50)
	work := t.TempDir()
	env := inst.gitEnv(key)
	mustGit(t, work, env, "clone", inst.sshURL("alice/nav"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, "cmd", "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# nav\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "cmd", "main.go"), []byte("package main\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "cmd", "sub", "x.go"), []byte("package sub\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "one")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	status, body := inst.get(t, "/alice/nav/blob/main/cmd/main.go")
	if status != 200 {
		t.Fatalf("blob: %d", status)
	}
	for _, want := range []string{
		`<nav class="filenav" aria-label="Files">`,
		`<h2 class="colhead">cmd</h2>`,
		`<a class="up" href="/alice/nav/tree/main">..</a>`,
		`<a class="dir" href="/alice/nav/tree/main/cmd/sub">sub/</a>`,
		`<a aria-current="page" href="/alice/nav/blob/main/cmd/main.go">main.go</a>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("blob page lacks %q", want)
		}
	}
	_, body = inst.get(t, "/alice/nav/blame/main/README.md")
	if !strings.Contains(body, `<h2 class="colhead">nav</h2>`) || !strings.Contains(body, `<a aria-current="page" href="/alice/nav/blob/main/README.md">README.md</a>`) {
		t.Errorf("blame page lacks the root navigator:\n%s", body)
	}
}
```

`mustGit(t, dir, env, args...) string`, `inst.gitEnv(key) []string` and `inst.sshURL(repo) string` are in `e2e/git_test.go`; `gitEnv` sets the author and committer, so the commit needs no `-c user.*` flags.

- [ ] **Step 8: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/web/ ./internal/httpd/ && go test ./e2e -run 'TestFileNavigator|TestWebUI' -count=1`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/httpd/filenav.go internal/httpd/filenav_test.go internal/httpd/web.go internal/httpd/accounts.go internal/web e2e/filenav_test.go
git commit -m "web: a file navigator beside blob, blame and edit

Ref #226"
```

---

### Task 6: Facet column on the issue and merge request lists

**Files:**
- Create: `internal/httpd/facets.go`, `internal/httpd/facets_test.go`
- Modify: `internal/httpd/web.go:1690-1735` (issues), `:1814-1866` (mrs)
- Modify: `internal/web/templates/layout.html` (new `sidecol` partial), `issues.html`, `mrs.html`
- Modify: `internal/web/static/style.css` (after the `.filenav` rules from Task 5)
- Modify: `e2e/labelweb_test.go` (add assertions at the end of `TestLabelsWeb`)

**Interfaces:**
- Consumes: `control.ReadableScope(st, user, repo) ([]int64, error)`, `s.st.ListLabels(repo, readable) ([]store.Label, error)` with `Label{Name, Issues, MRs}`, `s.st.ListMilestones(repo, "open", readable) ([]store.Milestone, error)` with `Milestone{Title, OpenItems}`, `s.labelColors(repo)`.
- Produces: `facetGroup`, `facetItem`, `func facetHref(base url.Values, key, value string) string`, `func listFacets(base url.Values, states []string, state string, labels []store.Label, ms []store.Milestone, forMRs bool) []facetGroup`. Task 7 and Task 8 reuse `facetGroup`.

- [ ] **Step 1: Write the failing test**

`internal/httpd/facets_test.go`:

```go
package httpd

import (
	"net/url"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

// A facet link keeps every other active filter, sets its own, and clears
// its own when it is already active (desktop layout spec).
func TestFacetHrefKeepsOtherFilters(t *testing.T) {
	base := url.Values{"state": {"open"}, "label": {"bug"}, "q": {"crash"}}
	if got := facetHref(base, "milestone", "v2"); got != "?label=bug&milestone=v2&q=crash&state=open" {
		t.Errorf("set: %q", got)
	}
	if got := facetHref(base, "label", ""); got != "?q=crash&state=open" {
		t.Errorf("clear: %q", got)
	}
	if got := facetHref(base, "state", "closed"); got != "?label=bug&q=crash&state=closed" {
		t.Errorf("replace: %q", got)
	}
}

func TestListFacetsGroups(t *testing.T) {
	base := url.Values{"state": {"open"}, "label": {"bug"}}
	labels := []store.Label{{Name: "bug", Issues: 2, MRs: 1}, {Name: "docs", Issues: 0, MRs: 3}}
	ms := []store.Milestone{{Title: "v2", OpenItems: 4}}
	groups := listFacets(base, []string{"open", "closed", "all"}, "open", labels, ms, false)
	if len(groups) != 3 || groups[0].Title != "State" || groups[1].Title != "Labels" || groups[2].Title != "Milestones" {
		t.Fatalf("groups: %+v", groups)
	}
	st := groups[0].Items
	if !st[0].Active || st[0].Href != "?label=bug&state=open" || st[1].Active || st[1].Href != "?label=bug&state=closed" {
		t.Errorf("state items: %+v", st)
	}
	lb := groups[1].Items
	if lb[0].Label != "bug" || lb[0].Count != 2 || !lb[0].Active || lb[0].Href != "?state=open" {
		t.Errorf("active label clears itself: %+v", lb[0])
	}
	if lb[1].Label != "docs" || lb[1].Count != 0 || lb[1].Active || lb[1].Href != "?label=docs&state=open" {
		t.Errorf("inactive label: %+v", lb[1])
	}
	if m := groups[2].Items[0]; m.Label != "v2" || m.Count != 4 || m.Href != "?label=bug&milestone=v2&state=open" {
		t.Errorf("milestone: %+v", m)
	}
	// on the MR list a label's count is its MR count
	mr := listFacets(base, []string{"open"}, "open", labels, nil, true)
	if mr[1].Items[1].Count != 3 {
		t.Errorf("mr count: %+v", mr[1].Items[1])
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/httpd/ -run 'TestFacetHref|TestListFacets'`
Expected: FAIL, "undefined: facetHref"

- [ ] **Step 3: The facets**

`internal/httpd/facets.go`:

```go
package httpd

import (
	"net/url"

	"gitbay.org/gitbay/internal/store"
)

// facetItem is one link in a list page's side column: a value the list
// narrows to. Clicking an active item clears it.
type facetItem struct {
	Label  string
	Count  int64
	Href   string
	Active bool
}

// facetGroup is one heading in the column: State, Labels, Milestones.
type facetGroup struct {
	Title string
	Items []facetItem
}

// facetHref returns "?..." with every parameter of base kept, key set to
// value, or dropped when value is "". url.Values encodes sorted, so the
// tests and the links agree byte for byte.
func facetHref(base url.Values, key, value string) string {
	q := url.Values{}
	for k, vs := range base {
		if k == key || len(vs) == 0 || vs[0] == "" {
			continue
		}
		q.Set(k, vs[0])
	}
	if value != "" {
		q.Set(key, value)
	}
	return "?" + q.Encode()
}

// listFacets builds the issue or merge request list's column from the
// active parameters, the states the page offers, and the repository's
// labels and open milestones. Counts are the rows' own: a label's issue
// count on the issue list, its MR count on the MR list.
func listFacets(base url.Values, states []string, state string, labels []store.Label, ms []store.Milestone, forMRs bool) []facetGroup {
	var st facetGroup
	st.Title = "State"
	for _, s := range states {
		st.Items = append(st.Items, facetItem{Label: s, Href: facetHref(base, "state", s), Active: s == state})
	}
	lb := facetGroup{Title: "Labels"}
	for _, l := range labels {
		n := l.Issues
		if forMRs {
			n = l.MRs
		}
		active := base.Get("label") == l.Name
		href := facetHref(base, "label", l.Name)
		if active {
			href = facetHref(base, "label", "")
		}
		lb.Items = append(lb.Items, facetItem{Label: l.Name, Count: n, Href: href, Active: active})
	}
	mg := facetGroup{Title: "Milestones"}
	for _, m := range ms {
		active := base.Get("milestone") == m.Title
		href := facetHref(base, "milestone", m.Title)
		if active {
			href = facetHref(base, "milestone", "")
		}
		mg.Items = append(mg.Items, facetItem{Label: m.Title, Count: int64(m.OpenItems), Href: href, Active: active})
	}
	return []facetGroup{st, lb, mg}
}
```

- [ ] **Step 4: Wire the handlers**

In `issues` (`web.go`), after the `if labels, err := s.st.ListIssueLabels(p.Repo)` block, add:

```go
	base := url.Values{"state": {state}, "label": {f.Label}, "assignee": {f.Assignee}, "author": {f.Author}, "milestone": {f.Milestone}, "q": {f.Search}}
	readable, _ := control.ReadableScope(s.st, s.viewer(r), p.Repo)
	allLabels, _ := s.st.ListLabels(p.Repo, readable)
	openMS, _ := s.st.ListMilestones(p.Repo, "open", readable)
	facets := listFacets(base, []string{"open", "closed", "all"}, state, allLabels, openMS, false)
```

add `Facets []facetGroup` to the render struct after `Filters`, passing `facets`. Add `"net/url"` to the imports if absent.

In `mrs`, after the `labels, err := s.st.ListMRLabels(p.Repo)` block, add the same with:

```go
	base := url.Values{"state": {state}, "label": {mf.Label}, "author": {mf.Author}, "milestone": {mf.Milestone}, "q": {mf.Search}}
	readable, _ := control.ReadableScope(s.st, s.viewer(r), p.Repo)
	allLabels, _ := s.st.ListLabels(p.Repo, readable)
	openMS, _ := s.st.ListMilestones(p.Repo, "open", readable)
	facets := listFacets(base, []string{"open", "merged", "closed", "all"}, state, allLabels, openMS, true)
```

and `Facets []facetGroup` in its struct.

- [ ] **Step 5: The partial and the templates**

In `layout.html` after the `filenav` define add:

```
{{define "sidecol"}}<nav class="sidecol" aria-label="Filters">
  {{range .}}{{if .Items}}<div class="grp">
  <h2 class="colhead">{{.Title}}</h2>
  <ul>{{range .Items}}<li><a{{if .Active}} aria-current="page"{{end}} href="{{.Href}}">{{.Label}}{{if .Count}} <i>{{.Count}}</i>{{end}}</a></li>{{end}}</ul>
  </div>{{end}}{{end}}
</nav>{{end}}
```

`issues.html`: replace the `content` define body with:

```html
<div class="withcol">
{{template "sidecol" .Facets}}
<div class="colmain">
<div class="listhead">
  <h1>Issues</h1>
  <form method="get" class="searchform compact">
    <input type="search" name="q" aria-label="Search issues" value="{{.Query}}" placeholder="search title and body">
    <button type="submit" class="btn">Search</button>
    <input type="hidden" name="state" value="{{.State}}">
  </form>
  {{range .Filters}}<p class="meta">{{.Key}}: {{if eq .Key "label"}}<span class="chip label" style="{{index $.LabelColors .Value}}">{{.Value}}</span>{{else}}<b>{{.Value}}</b>{{end}} <a href="{{.Clear}}">clear</a></p>{{end}}
  <span class="spacer"></span>
  <p class="meta"><a href="/{{.Repo.OwnerName}}/{{.Repo.Name}}/milestones">milestones</a> · <a href="/{{.Repo.OwnerName}}/{{.Repo.Name}}/labels">labels</a>{{if .Viewer}} · <a href="/{{.Repo.OwnerName}}/{{.Repo.Name}}/issues/new">new issue</a>{{end}}</p>
</div>
<ul class="issuelist rows">
... the existing {{range .Issues}} block, unchanged ...
</ul>
{{if .Older}}<p class="pager"><a href="{{.Older}}">older →</a></p>{{end}}
</div>
</div>
```

The `nav.filters` block moves into the column; the state links there are the facet group.

`mrs.html`: the same shape. Keep its `{{range .MRs}}` block; drop its `nav.filters`.

- [ ] **Step 6: The stylesheet**

After the `.filenav` rules add:

```css
/* ---- side column: facets or sections beside a list or a form ---- */
.withcol { display: grid; grid-template-columns: 15rem minmax(0, 1fr); gap: var(--sp-6); align-items: start; }
.withcol.narrow { grid-template-columns: 15rem minmax(0, 56rem); }
.colmain { min-width: 0; }
.sidecol { position: sticky; top: var(--sp-4); font-size: var(--fs-2); }
.sidecol .grp { margin-bottom: var(--sp-4); }
.sidecol ul { list-style: none; margin: 0; padding: 0; }
.sidecol li a {
  display: flex; align-items: baseline; gap: var(--sp-2);
  padding: 4px var(--sp-2);
  border-radius: var(--r-ctl);
  color: var(--fg);
}
.sidecol li a:hover { background: var(--hover); text-decoration: none; }
.sidecol li a[aria-current] { background: var(--surface); box-shadow: inset 2px 0 0 var(--mark); font-weight: 500; }
.sidecol li a i { margin-left: auto; font-style: normal; color: var(--muted); font-size: var(--fs-1); font-variant-numeric: tabular-nums; }
.sidecol form.searchform { margin-top: var(--sp-2); }
.sidecol form.searchform input[type="text"] { min-width: 0; width: 100%; }
```

In the `@media (max-width: 62rem)` block add:

```css
  /* the column follows the content on a phone, the way the aside does */
  .withcol, .withcol.narrow { grid-template-columns: 1fr; }
  .sidecol { position: static; order: 2; }
  .sidecol .grp { display: inline-block; vertical-align: top; margin-right: var(--sp-5); }
```

- [ ] **Step 7: The e2e assertion**

At the end of `TestLabelsWeb` in `e2e/labelweb_test.go`, after the label management assertions, add:

```go
	// The issue list's column lists the label with its count and a link
	// that keeps the state (desktop layout spec).
	status, page = browserGet(t, alice, base+"/issues?state=open")
	if status != 200 || !strings.Contains(page, `<nav class="sidecol" aria-label="Filters">`) {
		t.Fatalf("issues page lacks the side column: %d", status)
	}
	if !strings.Contains(page, `href="?label=bug&amp;state=open">bug <i>1</i></a>`) {
		t.Fatalf("issues column lacks the bug facet:\n%s", page)
	}
	status, page = browserGet(t, alice, base+"/issues?state=open&label=bug")
	if status != 200 || !strings.Contains(page, `aria-current="page" href="?state=open">bug <i>1</i></a>`) {
		t.Fatalf("active facet does not clear itself:\n%s", page)
	}
```

If `TestLabelsWeb` removes the `bug` label before its end, place the block before that removal.

- [ ] **Step 8: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/web/ ./internal/httpd/ && go test ./e2e -run 'TestLabelsWeb|TestMRListRows|TestMRWebLabels' -count=1`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/httpd/facets.go internal/httpd/facets_test.go internal/httpd/web.go internal/web e2e/labelweb_test.go
git commit -m "web: a facet column on the issue and merge request lists

State, labels with counts and open milestones beside the rows; a facet
keeps the other filters and clears itself when active.

Ref #226"
```

---

### Task 7: Facet column on the builds list

**Files:**
- Modify: `internal/httpd/builds.go:12-60`, `:147-178`
- Modify: `internal/httpd/builds_test.go` (add one test), `internal/httpd/buildpages_test.go:22-58`
- Modify: `internal/web/templates/builds.html:4-17`
- Modify: `internal/httpd/repohead_test.go` (Task 2's struct gains the field)

**Interfaces:**
- Consumes: `facetGroup`, `facetItem` from Task 6; `filterLinks`, `distinctRefs`, `buildStatuses` in `builds.go`.
- Produces: `func buildFacets(f buildFilter, jobs []control.JobOut, refs []string) []facetGroup`.

- [ ] **Step 1: Write the failing test**

Append to `internal/httpd/builds_test.go`:

```go
// The builds column groups the same links filterLinks makes: "all" and
// the statuses, then the jobs, then the branches seen (desktop layout spec).
func TestBuildFacetsGroups(t *testing.T) {
	f := buildFilter{Ref: "main", Status: "success"}
	groups := buildFacets(f, []control.JobOut{{Name: "lint"}}, []string{"main", "dev"})
	if len(groups) != 3 || groups[0].Title != "Status" || groups[1].Title != "Jobs" || groups[2].Title != "Branches" {
		t.Fatalf("groups: %+v", groups)
	}
	if groups[0].Items[0].Label != "all" || groups[0].Items[0].Href != "?ref=main" || groups[0].Items[0].Active {
		t.Errorf("all: %+v", groups[0].Items[0])
	}
	if s := groups[0].Items[3]; s.Label != "success" || !s.Active {
		t.Errorf("success: %+v", s)
	}
	if j := groups[1].Items[0]; j.Label != "lint" || j.Href != "?job=lint&ref=main&status=success" || j.Active {
		t.Errorf("lint: %+v", j)
	}
	if b := groups[2].Items[0]; b.Label != "main" || !b.Active || b.Href != "?status=success" {
		t.Errorf("active branch clears itself: %+v", b)
	}
	if b := groups[2].Items[1]; b.Label != "dev" || b.Active || b.Href != "?ref=dev&status=success" {
		t.Errorf("dev: %+v", b)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/httpd/ -run TestBuildFacetsGroups`
Expected: FAIL, "undefined: buildFacets"

- [ ] **Step 3: The grouping**

Append to `builds.go`:

```go
// buildFacets is the builds page's side column: filterLinks' rows split
// into their groups, plus one link per branch seen, which keeps status
// and job and clears itself when active.
func buildFacets(f buildFilter, jobs []control.JobOut, refs []string) []facetGroup {
	links := filterLinks(f, jobs)
	n := 1 + len(buildStatuses)
	status := facetGroup{Title: "Status"}
	for _, l := range links[:n] {
		status.Items = append(status.Items, facetItem{Label: l.Label, Href: l.Href, Active: l.Active})
	}
	job := facetGroup{Title: "Jobs"}
	for _, l := range links[n:] {
		job.Items = append(job.Items, facetItem{Label: l.Label, Href: l.Href, Active: l.Active})
	}
	branch := facetGroup{Title: "Branches"}
	base := url.Values{"ref": {f.Ref}, "status": {f.Status}, "job": {f.Job}}
	for _, ref := range refs {
		active := ref == f.Ref
		href := facetHref(base, "ref", ref)
		if active {
			href = facetHref(base, "ref", "")
		}
		branch.Items = append(branch.Items, facetItem{Label: ref, Href: href, Active: active})
	}
	return []facetGroup{status, job, branch}
}
```

In `builds()` add `Facets []facetGroup` to the render struct after `FilterLinks`, passing `buildFacets(filter, jobs, distinctRefs(builds, filter.Ref))`. Update the two test structs that render `builds.html` (`buildpages_test.go:30-40`, `repohead_test.go`) to carry `Facets []facetGroup` in the same position, passing `nil`.

- [ ] **Step 4: The template**

Replace `builds.html` lines 4-17 (the `listhead`) with:

```html
<div class="withcol">
<nav class="sidecol" aria-label="Filters">
  {{range .Facets}}{{if .Items}}<div class="grp">
  <h2 class="colhead">{{.Title}}</h2>
  <ul>{{range .Items}}<li><a{{if .Active}} aria-current="page"{{end}} href="{{.Href}}">{{.Label}}</a></li>{{end}}</ul>
  </div>{{end}}{{end}}
  <form method="get" class="searchform compact">
    <label for="ref" class="colhead">Branch</label>
    <input type="text" id="ref" name="ref" value="{{.Filter.Ref}}" list="buildrefs">
    <datalist id="buildrefs">{{range .Refs}}<option value="{{.}}">{{end}}</datalist>
    <input type="hidden" name="status" value="{{.Filter.Status}}">
    <input type="hidden" name="job" value="{{.Filter.Job}}">
    <button type="submit" class="btn">Filter</button>
  </form>
</nav>
<div class="colmain">
<div class="listhead">
  <h1>Builds</h1>
```

and close `</div>\n</div>` before the content define's `{{end}}`. The rest of the page (status badge button, run count, the `loglist`) stays inside `.colmain`. Change its `<ul class="loglist">` to `<ul class="loglist rows">`: sha, branch and date on one line, the badges right (rule 4 of the spec; the CSS landed in Task 3).

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/web/ ./internal/httpd/ && go test ./e2e -run 'TestBuildCancelWeb|TestRunnerSettingsWeb' -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/httpd/builds.go internal/httpd/builds_test.go internal/httpd/buildpages_test.go internal/httpd/repohead_test.go internal/web/templates/builds.html
git commit -m "web: the builds page filters in a side column

Ref #226"
```

---

### Task 8: Facet columns on explore and site search

**Files:**
- Create: `internal/httpd/topics.go`, `internal/httpd/topics_test.go`
- Modify: `internal/httpd/web.go:202-220` (explore)
- Modify: `internal/web/templates/explore.html`, `globalsearch.html:4-18`

**Interfaces:**
- Consumes: `describedRepo{Topics []string}`, `facetGroup`, `facetItem`.
- Produces: `func topicFacets(repos []describedRepo, q string) facetGroup`.

- [ ] **Step 1: Write the failing test**

`internal/httpd/topics_test.go`:

```go
package httpd

import "testing"

// Explore's column counts topics across the visible repositories, most
// used first then by name, capped at twenty, each linking to ?q=<topic>
// and the active one clearing the query (desktop layout spec).
func TestTopicFacets(t *testing.T) {
	repos := []describedRepo{
		{Topics: []string{"cli", "git"}},
		{Topics: []string{"git", "swift"}},
		{Topics: []string{"git"}},
	}
	g := topicFacets(repos, "cli")
	if g.Title != "Topics" || len(g.Items) != 3 {
		t.Fatalf("group: %+v", g)
	}
	if g.Items[0].Label != "git" || g.Items[0].Count != 3 || g.Items[0].Href != "/explore?q=git" {
		t.Errorf("git: %+v", g.Items[0])
	}
	if g.Items[1].Label != "cli" || !g.Items[1].Active || g.Items[1].Href != "/explore" {
		t.Errorf("cli: %+v", g.Items[1])
	}
	if g.Items[2].Label != "swift" || g.Items[2].Count != 1 {
		t.Errorf("swift: %+v", g.Items[2])
	}
	var many []describedRepo
	for i := 0; i < 30; i++ {
		many = append(many, describedRepo{Topics: []string{string(rune('a' + i))}})
	}
	if n := len(topicFacets(many, "").Items); n != 20 {
		t.Errorf("cap: %d", n)
	}
}
```

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/httpd/ -run TestTopicFacets`
Expected: FAIL, "undefined: topicFacets"

- [ ] **Step 3: The counter**

`internal/httpd/topics.go`:

```go
package httpd

import (
	"net/url"
	"sort"
)

// topicFacets counts the topics across repos for explore's column. The
// links are the ones topic chips already use, ?q=<topic>; the active
// topic links to explore with no query.
func topicFacets(repos []describedRepo, q string) facetGroup {
	counts := map[string]int64{}
	for _, r := range repos {
		for _, t := range r.Topics {
			counts[t]++
		}
	}
	names := make([]string, 0, len(counts))
	for t := range counts {
		names = append(names, t)
	}
	sort.Slice(names, func(i, j int) bool {
		if counts[names[i]] != counts[names[j]] {
			return counts[names[i]] > counts[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > 20 {
		names = names[:20]
	}
	g := facetGroup{Title: "Topics"}
	for _, t := range names {
		item := facetItem{Label: t, Count: counts[t], Href: "/explore?q=" + url.QueryEscape(t), Active: t == q}
		if item.Active {
			item.Href = "/explore"
		}
		g.Items = append(g.Items, item)
	}
	return g
}
```

In `explore` (`web.go`), compute `described := s.describeAll(repos)` once, add `Facets []facetGroup` to the struct after `Query`, and pass `[]facetGroup{topicFacets(described, q)}` and `s.filterRepos(q, described)`. The counts cover every public repository, not the filtered set, so the column stays stable while narrowing.

- [ ] **Step 4: The templates**

`explore.html` content define:

```html
<div class="withcol">
{{template "sidecol" .Facets}}
<div class="colmain">
<div class="headrow">
<h1>Explore</h1>
<form method="get" action="/explore" class="searchform compact">
  <input type="search" name="q" aria-label="Filter repositories" value="{{.Query}}" placeholder="filter by name, description, topic">
  <button type="submit" class="btn">Search</button>
</form>
<span class="spacer"></span>
</div>
<ul class="repolist rows">
{{range .Repos}}{{template "reporow" .}}
{{else}}<li class="empty">no public repositories yet</li>{{end}}
</ul>
</div>
</div>
```

`globalsearch.html`: wrap the content in `.withcol`; the column is the kinds, static:

```html
<div class="withcol">
<nav class="sidecol" aria-label="Filters">
  <div class="grp"><h2 class="colhead">Kind</h2>
  <ul>
    <li><a{{if eq .Kind ""}} aria-current="page"{{end}} href="?q={{.Query}}">everything</a></li>
    <li><a{{if eq .Kind "repo"}} aria-current="page"{{end}} href="?q={{.Query}}&amp;kind=repo">repositories</a></li>
    <li><a{{if eq .Kind "issue"}} aria-current="page"{{end}} href="?q={{.Query}}&amp;kind=issue">issues</a></li>
    <li><a{{if eq .Kind "mr"}} aria-current="page"{{end}} href="?q={{.Query}}&amp;kind=mr">merge requests</a></li>
  </ul></div>
</nav>
<div class="colmain">
<div class="listhead">
  <h1>Search</h1>
  {{if and .Query (not .QueryErr)}}<p class="meta">... unchanged ...</p>{{end}}
</div>
<form method="get" action="/search" class="searchform"> ... unchanged ... </form>
... the results list, unchanged ...
</div>
</div>
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/web/ ./internal/httpd/ && go test ./e2e -run 'TestWebUI|TestGlobalSearchAndNotificationsWeb' -count=1`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/httpd/topics.go internal/httpd/topics_test.go internal/httpd/web.go internal/web/templates/explore.html internal/web/templates/globalsearch.html
git commit -m "web: topic and kind columns on explore and search

Ref #226"
```

---

### Task 9: Section nav column on repository settings, account settings and admin

**Files:**
- Modify: `internal/web/templates/settings.html:1-7`, `account.html`, `admin.html`
- Modify: `internal/web/static/style.css:694` (`nav.sections`)
- Modify: `internal/web/widths_test.go` (extend)

**Interfaces:**
- Consumes: `.withcol.narrow` and `.sidecol` from Task 6.

- [ ] **Step 1: Extend the failing test**

Append to `TestListPagesAreWide` in `internal/web/widths_test.go`:

```go
	// Settings pages carry a section column: every section id has a link
	// in the column, and the page is wide with the narrow grid.
	for _, name := range []string{"settings.html", "account.html", "admin.html"} {
		src, _ := templateFS.ReadFile("templates/" + name)
		s := string(src)
		if !strings.HasPrefix(s, `{{define "width"}}wide{{end}}`) || !strings.Contains(s, `<div class="withcol narrow">`) {
			t.Errorf("%s lacks the narrow column layout", name)
		}
		for _, m := range regexp.MustCompile(`<section id="([a-z]+)"`).FindAllStringSubmatch(s, -1) {
			if !strings.Contains(s, `href="#`+m[1]+`"`) {
				t.Errorf("%s: section %q has no link in the column", name, m[1])
			}
		}
	}
```

Add `"regexp"` to the imports.

- [ ] **Step 2: Run it to see it fail**

Run: `go test ./internal/web/ -run TestListPagesAreWide`
Expected: FAIL, "settings.html lacks the narrow column layout"

- [ ] **Step 3: Repository settings**

`settings.html` line 1 → `{{define "width"}}wide{{end}}`. Replace line 7 (`<nav class="sections" ...>`) with:

```html
<div class="withcol narrow">
<nav class="sidecol" aria-label="Sections">
  <div class="grp"><h2 class="colhead">Sections</h2>
  <ul>
    <li><a href="#identity">Identity</a></li>
    <li><a href="#access">Access</a></li>
    <li><a href="#gates">Merge gates</a></li>
    <li><a href="#branches">Protected branches</a></li>
    <li><a href="#tags">Protected tags</a></li>
    <li><a href="#deps">Dependencies</a></li>
    <li><a href="#runners">Runners</a></li>
    <li><a href="#lifecycle">Lifecycle</a></li>
  </ul></div>
</nav>
<div class="colmain">
```

and add `</div>\n</div>` before the content define's final `{{end}}`. Delete the `nav.sections` rule from `style.css` (line 694): nothing uses it.

- [ ] **Step 4: Account settings**

`account.html` line 1 → `{{define "width"}}wide{{end}}`. After the notice lines (line 6) insert the same `withcol narrow` opener with links `#profile` Profile, `#keys` SSH keys, `#emails` Email addresses, `#pgp` OpenPGP keys, `#notifications` Notifications, `#appearance` Appearance, `#export` Export, `#ssh` On SSH only. Wrap each block from its `<h2>` to the line before the next `<h2>` in `<section id="...">` … `</section>` with those ids in order. Close `</div>\n</div>` before the final `{{end}}`.

- [ ] **Step 5: Admin**

`admin.html` line 1 → `{{define "width"}}wide{{end}}`. After the `<p class="meta">Server build` line insert the opener with links `#webhooks` Webhook deliveries, `#mail` Mail, `#mirrors` Mirrors, `#builds` Builds, `#deps` Dependency checks. Inside each `{{with .Queues.X}}` block wrap the content in `<section id="...">` … `</section>`. Close `</div>\n</div>` before the final `{{end}}`.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/web/ ./internal/httpd/ && go test ./e2e -run 'TestRepoSettingsWeb|TestAccountSettingsWeb|TestWebTheme' -count=1`
Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add internal/web
git commit -m "web: section columns on settings, account and admin

Ref #226"
```

---

### Task 10: The pages without a column

**Files:**
- Modify: `internal/web/static/style.css:997` (`.withaside`), `:832-836` (`.readme`), `:1441` (`.wikinav`)
- Modify: `docs/specs/2026-09-19-desktop-layout-design.md` (rule 7)

- [ ] **Step 1: The rules**

`.withaside` grid: `minmax(0, 1fr) 18rem` → `minmax(0, 1fr) 20rem`.

After `.readme .cardbody { max-width: 78ch; }` add:

```css
/* the overview README caps as a card, so prose and code share an edge */
.overview .readme { max-width: 88ch; }
```

`.wikinav { flex: none; width: 14rem; }` → `width: 15rem;`.

- [ ] **Step 2: Align the spec with what shipped**

In the spec, rule 7 reads "a facet or section column folds into a `details` element above the content". Without JavaScript a `details` cannot open on desktop and close on a phone from one markup, so the column stacks after the content instead, the way the issue aside does since #232. Replace that rule with:

```
7. **Below 64rem** a facet or section column stacks after the content,
   the way the issue aside does; the file navigator disappears (the
   tree page exists); the dashboard's pinned column returns to the chip
   row. Nothing the phone layout fixed moves.
```

In the spec's page table, the issue list row reads "State; labels with counts; milestones with open counts; assignees (issues)". No store read counts issues per assignee, and adding one is a feature the CLI does not have, so drop "assignees (issues)" from that row and from the "Source of its contents" cell. The `assignee` parameter still works from an author link; it is not a facet.

- [ ] **Step 3: Run the tests and commit**

Run: `go test ./internal/web/ ./internal/httpd/`
Expected: PASS

```bash
git add internal/web/static/style.css docs/specs/2026-09-19-desktop-layout-design.md
git commit -m "web: wider aside, capped README card, 15rem wiki nav

Ref #226"
```

---

### Task 11: Captures, axe scan, CHANGELOG

**Files:**
- Modify: `CHANGELOG.org:7-9`
- Create (gitignored, not committed): `.claude/screenshots/desktop/`

- [ ] **Step 1: Start a local instance with this branch**

Run from the repository root:

```bash
sh .claude/screenshots/local.sh
```

It prints `base http://127.0.0.1:8090` and a login URL. Keep the URL.

- [ ] **Step 2: Capture at 1920 and 1280, dark and light**

```bash
cd .claude/screenshots
python3 shoot.py desktop/dark-1920 dark 1920 local.txt "<login url>"
python3 shoot.py desktop/light-1920 light 1920 local.txt "<login url>"
python3 shoot.py desktop/dark-1280 dark 1280 local.txt "<login url>"
python3 shoot.py desktop/mobile dark 375 local-mobile.txt "<login url>"
```

Add these lines to `local.txt` first if absent:

```
dashboard http://127.0.0.1:8090/
blob http://127.0.0.1:8090/krz/gitbay/blob/main/Makefile
builds http://127.0.0.1:8090/krz/gitbay/builds
settings http://127.0.0.1:8090/settings
repo-settings http://127.0.0.1:8090/krz/gitbay/settings
```

Open each PNG and check: no horizontal scroll at 375, the column after the content at 375, the container centered at 1920, one-line rows at 1280 and 1920, the navigator marking `Makefile`.

- [ ] **Step 3: Axe scan**

```bash
python3 audit/audit.py desktop/axe dark 1280 local.txt
python3 audit/audit.py desktop/axe-mobile dark 375 local-mobile.txt
python3 audit/summ.py desktop/axe desktop/axe-mobile
```

Expected: zero violations on every page except the 404 numeral already recorded. A `link-name` or `landmark` finding on the new columns is a defect in the markup, not a scan quirk: fix it and rescan.

- [ ] **Step 4: CHANGELOG**

Under `* v1.30.0 — unreleased` in `CHANGELOG.org`, before the existing bullet list, add a paragraph and bullets:

```
The desktop layout (#226): the web UI uses a wide screen.

- One centered container at 100rem; the repository header, main and
  footer align on it. Text keeps its measure.
- The repository header is two rows: name, description and buttons,
  then the tabs.
- Issue, merge request, build, explore, search and notification rows
  are one line above 64rem, and those pages render at the container
  width.
- The dashboard is three columns: pinned repositories with open issue,
  merge request and last-build counts; a tile per queue with the queue
  rows below; the activity feed.
- A file navigator beside blob, blame and edit pages lists the file's
  directory and marks the file.
- Side columns: state, labels and open milestones on the issue and
  merge request lists; status, jobs and branches on builds; topics on
  explore; kinds on search; sections on repository settings, account
  settings and admin.
- Below 64rem every column stacks after its content; the navigator
  hides, since the tree page is the navigator on a phone.
```

- [ ] **Step 5: Commit**

```bash
git add CHANGELOG.org
git commit -m "CHANGELOG: the desktop layout

Ref #226"
```

---

### Task 12: Push, CI, merge request

- [ ] **Step 1: Rebase onto main and push**

```bash
git fetch origin && git rebase origin/main && git push -u origin desktop-layout-spec
```

- [ ] **Step 2: Open the merge request**

```bash
gitbay mr create --source desktop-layout-spec --target main --title "web: the desktop layout" --file - <<'EOF'
Ref #226. Spec docs/specs/2026-09-19-desktop-layout-design.md, plan
docs/plans/2026-09-19-desktop-layout.md.

One centered container, a two-row repository header, one-line list rows,
a three-column dashboard with count tiles and pinned counts, a file
navigator on blob/blame/edit, facet columns on the issue, MR, builds,
explore and search lists, section columns on the settings pages.
EOF
```

- [ ] **Step 3: Wait for CI**

Poll once every few minutes, one ssh call per tick:

```bash
gitbay build list --json | head -c 2000
```

Expected: `test` and `build` succeed on the branch head. A failure: `gitbay build log <n>`, fix on the branch, push, wait again.

- [ ] **Step 4: Merge and clean up**

```bash
gitbay mr merge <n> --strategy ff
git checkout main && git pull && git branch -d desktop-layout-spec && git push origin --delete desktop-layout-spec
```

If the merge reports the branch is behind, rebase, push, merge again.

- [ ] **Step 5: Deploy**

```bash
make deploy
```

Then recapture the live site at 1920 with `shoot.py` against `public.txt` and compare with the local captures from Task 11.
