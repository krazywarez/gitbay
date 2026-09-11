# Org labels, milestones and cross-repository closes: implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Labels and milestones an org defines once for every repository under it, and `Closes owner/name#N` acting on another repository the actor can write to. Closes #203.

**Architecture:** The existing `labels` and `milestones` tables gain an `org_id` beside a now-nullable `repo_id` (migration 0052), so `issue_labels` and the two `milestone_id` columns keep their ids. Store lookups take the `store.Repo` and match `repo_id = ? OR org_id = ?` for org-owned repositories. Seven `org label` / `org milestone` control commands manage org rows; the web gets two read pages under `/{org}/-/`. The closing-keyword pattern in `commitrefs.go` accepts an `owner/name` prefix and acts when the actor holds write on the target.

**Tech Stack:** Go, SQLite via modernc (hand-written SQL, no ORM), Go `html/template`, the control registry in `internal/control`, the e2e harness in `e2e/`.

**Spec:** `docs/specs/2026-09-11-org-labels-milestones-closes-design.md`

## Global Constraints

- Every capability lands as a control command first; the CLI, web and API dispatch into it. New commands need a `pass()` row in `cmd/gitbay/main.go` (a coverage test enforces this) and, if `ReadOnly`, a row in `readArgs` in `e2e/readonly_test.go`.
- Hand-written SQL only. No ORM. Migrations are `internal/store/migrations/NNNN_name.up.sql` and `.down.sql`, embedded, run one per transaction.
- Private repositories return not-found, never a denial that confirms a namespace. Org existence is public (`org show` answers anyone).
- Never mention an assistant or model anywhere: commit messages, comments, docs.
- Commit messages reference the issue: `Ref #203` on each task, `Closes #203` on the last.
- Run locally: `go build ./... && go vet ./...` and the unit tests of the touched packages. Run at most the one e2e test you write (`go test ./e2e -run TestOrgLabels`); CI on bay1 runs the full suite.
- Style: plain sentences in comments, no dramatic framing. Match the surrounding code.
- Work on branch `org-scope`, which already holds the spec.

One deviation from the spec's wording: the org pages get their own small templates (`orglabels.html`, `orgmilestones.html`) rather than reusing `labels.html` and `milestones.html`, whose every URL and field is a `repoPage`. Behaviour is as specified.

---

### Task 1: Migration 0052 and the scoped structs

**Files:**
- Create: `internal/store/migrations/0052_org_scope.up.sql`
- Create: `internal/store/migrations/0052_org_scope.down.sql`
- Modify: `internal/store/labels.go:1-10` (struct)
- Modify: `internal/store/milestones.go:9-20` (struct)
- Test: `internal/store/store_test.go`

**Interfaces:**
- Produces: `labels(id, repo_id NULL, org_id NULL, name, color)` and `milestones(id, repo_id NULL, org_id NULL, title, description, due_date, state, created_at)` with `CHECK ((repo_id IS NULL) <> (org_id IS NULL))` and partial unique indexes `labels_repo_name`, `labels_org_name`, `milestones_repo_title`, `milestones_org_title`.
- Produces: `store.Label{Name, Color, Org bool, Issues}` and `store.Milestone{..., RepoID, OrgID, ...}`.

- [ ] **Step 1: Write the failing migration test**

Append to `internal/store/store_test.go`:

```go
// Migration 0052 rebuilds labels and milestones with an org scope. The
// rebuild renames the old tables; since SQLite 3.26 a rename rewrites the
// children's foreign keys, so issue_labels and the milestone_id columns
// would follow labels_old unless legacy_alter_table is on for the script.
// This checks the ids, the memberships and the foreign keys all survive.
func TestMigration0052KeepsMembershipsAndForeignKeys(t *testing.T) {
	s := open(t)
	if err := s.MigrateTo(51); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	rid, err := s.CreateRepo("user", uid, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	iid, err := s.CreateIssue(rid, uid, "one", "", "md")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO labels (repo_id, name, color) VALUES (?, 'bug', '#ff0000')", rid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO issue_labels (issue_id, label_id) SELECT ?, id FROM labels WHERE name = 'bug'", iid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO milestones (repo_id, title) VALUES (?, 'v1')", rid); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("UPDATE issues SET milestone_id = (SELECT id FROM milestones WHERE title = 'v1') WHERE id = ?", iid); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateTo(52); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM issue_labels il JOIN labels l ON l.id = il.label_id
		WHERE il.issue_id = ? AND l.name = 'bug' AND l.repo_id = ? AND l.org_id IS NULL`, iid, rid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("label membership after 0052: %d, %v", n, err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM issues i JOIN milestones m ON m.id = i.milestone_id
		WHERE i.id = ? AND m.title = 'v1' AND m.repo_id = ?`, iid, rid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("milestone attachment after 0052: %d, %v", n, err)
	}
	rows, err := s.DB.Query("PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if rows.Next() {
		t.Fatal("foreign_key_check reported a violation after 0052")
	}
	// The scope CHECK holds: a row with neither or both scopes is refused.
	if _, err := s.DB.Exec("INSERT INTO labels (name) VALUES ('neither')"); err == nil {
		t.Fatal("label with no scope was accepted")
	}
	if _, err := s.DB.Exec("INSERT INTO labels (repo_id, org_id, name) VALUES (?, 1, 'both')", rid); err == nil {
		t.Fatal("label with both scopes was accepted")
	}
	// Down refuses while an org-scoped row exists, and works once it is gone.
	if _, err := s.DB.Exec("INSERT INTO orgs (name) VALUES ('acme')"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("INSERT INTO labels (org_id, name) VALUES ((SELECT id FROM orgs WHERE name = 'acme'), 'org-only')"); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateTo(51); err == nil {
		t.Fatal("down migration accepted an org-scoped label")
	}
	if _, err := s.DB.Exec("DELETE FROM labels WHERE org_id IS NOT NULL"); err != nil {
		t.Fatal(err)
	}
	if err := s.MigrateTo(51); err != nil {
		t.Fatalf("down migration: %v", err)
	}
	if err := s.DB.QueryRow(`SELECT COUNT(*) FROM issue_labels il JOIN labels l ON l.id = il.label_id WHERE il.issue_id = ?`, iid).Scan(&n); err != nil || n != 1 {
		t.Fatalf("label membership after down: %d, %v", n, err)
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/store/ -run TestMigration0052 -v`
Expected: FAIL, "no such schema version 52".

- [ ] **Step 3: Write the up migration**

`internal/store/migrations/0052_org_scope.up.sql`:

```sql
-- Labels and milestones scoped to a repository or to an org (#203).
-- Exactly one of repo_id and org_id is set. Uniqueness is per scope, as
-- two partial indexes; the app refuses a repo name the org already holds.
--
-- Both tables have children (issue_labels, issues.milestone_id,
-- merge_requests.milestone_id). Since SQLite 3.26 renaming a parent
-- rewrites the children's foreign keys to follow it, which would bind them
-- to the *_old tables. legacy_alter_table keeps the children naming labels
-- and milestones, which the new tables then are. foreign_keys stays on:
-- nothing references the *_old tables, so dropping them cascades nothing.
PRAGMA legacy_alter_table = ON;

ALTER TABLE labels RENAME TO labels_old;
CREATE TABLE labels (
    id      INTEGER PRIMARY KEY,
    repo_id INTEGER REFERENCES repos(id) ON DELETE CASCADE,
    org_id  INTEGER REFERENCES orgs(id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    color   TEXT NOT NULL DEFAULT '',
    CHECK ((repo_id IS NULL) <> (org_id IS NULL))
);
INSERT INTO labels (id, repo_id, name, color)
    SELECT id, repo_id, name, color FROM labels_old;
DROP TABLE labels_old;
CREATE UNIQUE INDEX labels_repo_name ON labels(repo_id, name) WHERE repo_id IS NOT NULL;
CREATE UNIQUE INDEX labels_org_name  ON labels(org_id, name)  WHERE org_id  IS NOT NULL;

ALTER TABLE milestones RENAME TO milestones_old;
CREATE TABLE milestones (
    id          INTEGER PRIMARY KEY,
    repo_id     INTEGER REFERENCES repos(id) ON DELETE CASCADE,
    org_id      INTEGER REFERENCES orgs(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    due_date    TEXT NOT NULL DEFAULT '',
    state       TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','closed')),
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    CHECK ((repo_id IS NULL) <> (org_id IS NULL))
);
INSERT INTO milestones (id, repo_id, title, description, due_date, state, created_at)
    SELECT id, repo_id, title, description, due_date, state, created_at FROM milestones_old;
DROP TABLE milestones_old;
CREATE UNIQUE INDEX milestones_repo_title ON milestones(repo_id, title) WHERE repo_id IS NOT NULL;
CREATE UNIQUE INDEX milestones_org_title  ON milestones(org_id, title)  WHERE org_id  IS NOT NULL;

PRAGMA legacy_alter_table = OFF;
```

- [ ] **Step 4: Write the down migration**

`internal/store/migrations/0052_org_scope.down.sql`. The copy into a `NOT NULL repo_id` column fails on any org-scoped row, which is the refusal.

```sql
-- Back to per-repository rows. An org-scoped row has no repository to go
-- to; the NOT NULL on repo_id refuses the copy, which fails the migration.
PRAGMA legacy_alter_table = ON;

ALTER TABLE labels RENAME TO labels_old;
CREATE TABLE labels (
    id      INTEGER PRIMARY KEY,
    repo_id INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    name    TEXT NOT NULL,
    color   TEXT NOT NULL DEFAULT '',
    UNIQUE (repo_id, name)
);
INSERT INTO labels (id, repo_id, name, color)
    SELECT id, repo_id, name, color FROM labels_old;
DROP TABLE labels_old;

ALTER TABLE milestones RENAME TO milestones_old;
CREATE TABLE milestones (
    id          INTEGER PRIMARY KEY,
    repo_id     INTEGER NOT NULL REFERENCES repos(id) ON DELETE CASCADE,
    title       TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    due_date    TEXT NOT NULL DEFAULT '',
    state       TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','closed')),
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (repo_id, title)
);
INSERT INTO milestones (id, repo_id, title, description, due_date, state, created_at)
    SELECT id, repo_id, title, description, due_date, state, created_at FROM milestones_old;
DROP TABLE milestones_old;

PRAGMA legacy_alter_table = OFF;
```

- [ ] **Step 5: Add the struct fields**

In `internal/store/labels.go` replace the `Label` struct:

```go
// Label is an issue label with its colour, "" when none was set (the web
// then derives one from the name), and how many issues carry it. Org is
// true for a label the repository sees through its org.
type Label struct {
	Name   string `json:"name"`
	Color  string `json:"color,omitempty"`
	Org    bool   `json:"org,omitempty"`
	Issues int64  `json:"issues"`
}
```

In `internal/store/milestones.go` add `OrgID int64 // set instead of RepoID for an org milestone` after `RepoID`.

- [ ] **Step 6: Run the test to verify it passes**

Run: `go test ./internal/store/ -run TestMigration0052 -v`
Expected: PASS. Then `go build ./...` still compiles (only fields were added).

- [ ] **Step 7: Commit**

```bash
git add internal/store/migrations/0052_org_scope.up.sql internal/store/migrations/0052_org_scope.down.sql internal/store/labels.go internal/store/milestones.go internal/store/store_test.go
git commit -m "store: migration 0052 scopes labels and milestones to a repo or an org

Ref #203"
```

---

### Task 2: Store: labels by scope, org labels, promote

**Files:**
- Modify: `internal/store/labels.go`
- Modify: `internal/store/issues.go:297-346` (`LabelColors`, `SetIssueLabel`)
- Create: `internal/store/scope.go`
- Test: `internal/store/labels_test.go` (new)

**Interfaces:**
- Produces in `scope.go`: `var ErrOrgScoped = errors.New("held by the org")`; `func scopeClause(alias string, repo Repo) (string, []any)`; `func inClause(ids []int64) (string, []any)`.
- Produces in `labels.go`:
  - `func (s *Store) ListLabels(repo Repo, readable []int64) ([]Label, error)` — org rows first then repo rows, each by name; `Issues` counts only issues in `readable`.
  - `func (s *Store) LabelByName(repo Repo, name string) (Label, error)` — `ErrNotFound` when neither scope has it.
  - `func (s *Store) SetLabel(repo Repo, name, color string) error` — `ErrOrgScoped` when the org holds the name.
  - `func (s *Store) DeleteLabel(repo Repo, name string) error` — `ErrOrgScoped` for an org row, `ErrNotFound` for none.
  - `func (s *Store) ListOrgLabels(orgID int64, readable []int64) ([]Label, error)`
  - `func (s *Store) SetOrgLabel(orgID int64, name, color string) (folded int, err error)` — promotes same-named repo labels under the org; `folded` is how many repositories were folded in.
  - `func (s *Store) DeleteOrgLabel(orgID int64, name string) error` — `ErrNotFound` when absent.
- Produces in `issues.go`: `func (s *Store) LabelColors(repo Repo) (map[string]string, error)`; `func (s *Store) SetIssueLabel(repo Repo, issueID int64, name string, add bool) error` — add resolves the org row first, else creates the repo row.
- Consumes: Task 1's schema. Note that every `ON CONFLICT (repo_id, name)` must name the partial index's predicate: `ON CONFLICT (repo_id, name) WHERE repo_id IS NOT NULL`.

- [ ] **Step 1: Write the failing tests**

`internal/store/labels_test.go`:

```go
package store

import (
	"errors"
	"testing"
)

// acmeFixture: org acme owned by alice with repos acme/core and
// acme/site, an issue in each, and alice's own alice/app.
type acmeFixture struct {
	s          *Store
	alice      int64
	org        int64
	core, site Repo
	app        Repo
	coreIssue  int64
	siteIssue  int64
}

func newAcme(t *testing.T) acmeFixture {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	var f acmeFixture
	f.s = s
	var err error
	if f.alice, err = s.CreateUser("alice", false); err != nil {
		t.Fatal(err)
	}
	if f.org, err = s.CreateOrg("acme", f.alice); err != nil {
		t.Fatal(err)
	}
	mk := func(kind string, owner int64, name string) Repo {
		id, err := s.CreateRepo(kind, owner, name, "public")
		if err != nil {
			t.Fatal(err)
		}
		r, err := s.RepoByID(id)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	f.core = mk("org", f.org, "core")
	f.site = mk("org", f.org, "site")
	f.app = mk("user", f.alice, "app")
	if f.coreIssue, err = s.CreateIssue(f.core.ID, f.alice, "c1", "", "md"); err != nil {
		t.Fatal(err)
	}
	if f.siteIssue, err = s.CreateIssue(f.site.ID, f.alice, "s1", "", "md"); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f acmeFixture) orgRepos() []int64 { return []int64{f.core.ID, f.site.ID} }

func TestOrgLabelSeenByEveryOrgRepo(t *testing.T) {
	f := newAcme(t)
	if _, err := f.s.SetOrgLabel(f.org, "bug", "#ff0000"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetLabel(f.site, "docs", ""); err != nil {
		t.Fatal(err)
	}
	// site sees the org's bug first, then its own docs; core sees only bug;
	// alice/app, user-owned, sees nothing.
	got, err := f.s.ListLabels(f.site, f.orgRepos())
	if err != nil || len(got) != 2 || got[0].Name != "bug" || !got[0].Org || got[1].Name != "docs" || got[1].Org {
		t.Fatalf("site labels = %+v, %v", got, err)
	}
	if got, _ := f.s.ListLabels(f.core, f.orgRepos()); len(got) != 1 || got[0].Name != "bug" {
		t.Fatalf("core labels = %+v", got)
	}
	if got, _ := f.s.ListLabels(f.app, []int64{f.app.ID}); len(got) != 0 {
		t.Fatalf("app labels = %+v", got)
	}
	colors, _ := f.s.LabelColors(f.core)
	if colors["bug"] != "#ff0000" {
		t.Fatalf("core colours = %v", colors)
	}
}

func TestIssueLabelResolvesOrgRowFirst(t *testing.T) {
	f := newAcme(t)
	if _, err := f.s.SetOrgLabel(f.org, "bug", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetIssueLabel(f.core, f.coreIssue, "bug", true); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetIssueLabel(f.site, f.siteIssue, "bug", true); err != nil {
		t.Fatal(err)
	}
	// One org row, no repo rows were created on the fly.
	var n int
	f.s.DB.QueryRow("SELECT COUNT(*) FROM labels WHERE name = 'bug'").Scan(&n)
	if n != 1 {
		t.Fatalf("labels named bug: %d, want 1", n)
	}
	// The count spans the org's readable repos.
	got, _ := f.s.ListOrgLabels(f.org, f.orgRepos())
	if len(got) != 1 || got[0].Issues != 2 {
		t.Fatalf("org labels = %+v", got)
	}
	got, _ = f.s.ListOrgLabels(f.org, []int64{f.core.ID})
	if got[0].Issues != 1 {
		t.Fatalf("org labels over core only = %+v", got)
	}
	// A label neither scope has is still created on the fly in the repo.
	if err := f.s.SetIssueLabel(f.core, f.coreIssue, "adhoc", true); err != nil {
		t.Fatal(err)
	}
	if l, err := f.s.LabelByName(f.core, "adhoc"); err != nil || l.Org {
		t.Fatalf("adhoc = %+v, %v", l, err)
	}
	// Removing by name works for the org row too.
	if err := f.s.SetIssueLabel(f.core, f.coreIssue, "bug", false); err != nil {
		t.Fatal(err)
	}
	got, _ = f.s.ListOrgLabels(f.org, f.orgRepos())
	if got[0].Issues != 1 {
		t.Fatalf("after detach: %+v", got)
	}
}

func TestRepoLabelRefusedWhenOrgHoldsName(t *testing.T) {
	f := newAcme(t)
	if _, err := f.s.SetOrgLabel(f.org, "bug", ""); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetLabel(f.core, "bug", "#00ff00"); !errors.Is(err, ErrOrgScoped) {
		t.Fatalf("SetLabel over org name: %v, want ErrOrgScoped", err)
	}
	if err := f.s.DeleteLabel(f.core, "bug"); !errors.Is(err, ErrOrgScoped) {
		t.Fatalf("DeleteLabel of org row: %v, want ErrOrgScoped", err)
	}
	if err := f.s.DeleteLabel(f.core, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DeleteLabel of nothing: %v, want ErrNotFound", err)
	}
	// A user-owned repo is unaffected by any org.
	if err := f.s.SetLabel(f.app, "bug", ""); err != nil {
		t.Fatal(err)
	}
}

func TestSetOrgLabelPromotesRepoLabels(t *testing.T) {
	f := newAcme(t)
	if err := f.s.SetIssueLabel(f.core, f.coreIssue, "bug", true); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetIssueLabel(f.site, f.siteIssue, "bug", true); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetLabel(f.app, "bug", "#123456"); err != nil {
		t.Fatal(err)
	}
	folded, err := f.s.SetOrgLabel(f.org, "bug", "#ff0000")
	if err != nil || folded != 2 {
		t.Fatalf("SetOrgLabel folded %d, %v; want 2", folded, err)
	}
	var n int
	f.s.DB.QueryRow("SELECT COUNT(*) FROM labels WHERE name = 'bug' AND org_id = ?", f.org).Scan(&n)
	if n != 1 {
		t.Fatalf("org rows named bug: %d", n)
	}
	f.s.DB.QueryRow("SELECT COUNT(*) FROM labels WHERE name = 'bug' AND repo_id IN (?, ?)", f.core.ID, f.site.ID).Scan(&n)
	if n != 0 {
		t.Fatalf("repo rows named bug left under the org: %d", n)
	}
	got, _ := f.s.ListOrgLabels(f.org, f.orgRepos())
	if len(got) != 1 || got[0].Issues != 2 || got[0].Color != "#ff0000" {
		t.Fatalf("after promote: %+v", got)
	}
	// alice/app's own bug is another owner's and stays.
	if l, err := f.s.LabelByName(f.app, "bug"); err != nil || l.Color != "#123456" {
		t.Fatalf("app bug = %+v, %v", l, err)
	}
	// A second set only recolours.
	if folded, err := f.s.SetOrgLabel(f.org, "bug", "#0000ff"); err != nil || folded != 0 {
		t.Fatalf("second set folded %d, %v", folded, err)
	}
	if err := f.s.DeleteOrgLabel(f.org, "bug"); err != nil {
		t.Fatal(err)
	}
	if err := f.s.DeleteOrgLabel(f.org, "bug"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	f.s.DB.QueryRow("SELECT COUNT(*) FROM issue_labels").Scan(&n)
	if n != 0 {
		t.Fatalf("memberships after org delete: %d", n)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/store/ -run 'OrgLabel|IssueLabelResolves|RepoLabelRefused' -v`
Expected: build failure, `SetOrgLabel`, `ListOrgLabels`, `LabelByName`, `DeleteOrgLabel`, `ErrOrgScoped` undefined.

- [ ] **Step 3: Write `scope.go`**

```go
package store

import (
	"errors"
	"strings"
)

// ErrOrgScoped is returned when a repository-level write names a label or
// milestone its org holds; the org commands manage those.
var ErrOrgScoped = errors.New("held by the org")

// scopeClause selects the label or milestone rows a repository sees: its
// own, and its org's when an org owns it. alias is the table alias in the
// query.
func scopeClause(alias string, repo Repo) (string, []any) {
	if repo.OwnerKind == "org" {
		return "(" + alias + ".repo_id = ? OR " + alias + ".org_id = ?)", []any{repo.ID, repo.OwnerID}
	}
	return alias + ".repo_id = ?", []any{repo.ID}
}

// inClause renders ids as a parenthesised placeholder list. An empty set
// yields (NULL), which matches nothing.
func inClause(ids []int64) (string, []any) {
	if len(ids) == 0 {
		return "(NULL)", nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	return "(" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")", args
}
```

- [ ] **Step 4: Rewrite `labels.go` below the struct**

```go
// labelRows lists labels under where, with use counted over the issues of
// the readable repositories only, so a private repository's issues do not
// show in a count someone outside it can see.
func (s *Store) labelRows(where string, args []any, readable []int64) ([]Label, error) {
	in, inArgs := inClause(readable)
	q := `SELECT l.name, l.color, l.org_id IS NOT NULL,
		(SELECT COUNT(*) FROM issue_labels il JOIN issues i ON i.id = il.issue_id
		 WHERE il.label_id = l.id AND i.repo_id IN ` + in + `)
		FROM labels l WHERE ` + where + ` ORDER BY l.org_id IS NULL, l.name`
	rows, err := s.DB.Query(q, append(inArgs, args...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Label
	for rows.Next() {
		var l Label
		if err := rows.Scan(&l.Name, &l.Color, &l.Org, &l.Issues); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ListLabels lists the labels a repository sees: its org's first, then its
// own, each by name.
func (s *Store) ListLabels(repo Repo, readable []int64) ([]Label, error) {
	where, args := scopeClause("l", repo)
	return s.labelRows(where, args, readable)
}

// ListOrgLabels lists an org's labels.
func (s *Store) ListOrgLabels(orgID int64, readable []int64) ([]Label, error) {
	return s.labelRows("l.org_id = ?", []any{orgID}, readable)
}

// LabelByName resolves a name the way attaching does: the org's row when
// the org has it, else the repository's.
func (s *Store) LabelByName(repo Repo, name string) (Label, error) {
	where, args := scopeClause("l", repo)
	var l Label
	err := s.DB.QueryRow(`SELECT l.name, l.color, l.org_id IS NOT NULL FROM labels l
		WHERE `+where+` AND l.name = ? ORDER BY l.org_id IS NULL LIMIT 1`,
		append(args, name)...).Scan(&l.Name, &l.Color, &l.Org)
	if errors.Is(err, sql.ErrNoRows) {
		return l, ErrNotFound
	}
	return l, err
}

// orgHoldsLabel reports whether the repository's org has a label of that
// name; always false for a user-owned repository.
func orgHoldsLabel(q interface {
	QueryRow(string, ...any) *sql.Row
}, repo Repo, name string) (bool, error) {
	if repo.OwnerKind != "org" {
		return false, nil
	}
	var n int
	err := q.QueryRow("SELECT COUNT(*) FROM labels WHERE org_id = ? AND name = ?", repo.OwnerID, name).Scan(&n)
	return n > 0, err
}

// SetLabel creates the repository's label or sets its colour. A name the
// org holds is refused with ErrOrgScoped.
func (s *Store) SetLabel(repo Repo, name, color string) error {
	if held, err := orgHoldsLabel(s.DB, repo, name); err != nil || held {
		if err != nil {
			return err
		}
		return ErrOrgScoped
	}
	_, err := s.DB.Exec(`INSERT INTO labels (repo_id, name, color) VALUES (?, ?, ?)
		ON CONFLICT (repo_id, name) WHERE repo_id IS NOT NULL DO UPDATE SET color = excluded.color`,
		repo.ID, name, color)
	return err
}

// DeleteLabel removes the repository's label and takes it off every issue.
// An org's label is ErrOrgScoped; no label at all is ErrNotFound.
func (s *Store) DeleteLabel(repo Repo, name string) error {
	res, err := s.DB.Exec("DELETE FROM labels WHERE repo_id = ? AND name = ?", repo.ID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		return nil
	}
	if held, err := orgHoldsLabel(s.DB, repo, name); err != nil || held {
		if err != nil {
			return err
		}
		return ErrOrgScoped
	}
	return ErrNotFound
}

// SetOrgLabel creates the org's label or sets its colour. Repositories
// under the org that hold the name are folded in: their issues move to
// the org's row and their rows go. folded is how many were.
func (s *Store) SetOrgLabel(orgID int64, name, color string) (int, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO labels (org_id, name, color) VALUES (?, ?, ?)
		ON CONFLICT (org_id, name) WHERE org_id IS NOT NULL DO UPDATE SET color = excluded.color`,
		orgID, name, color); err != nil {
		return 0, err
	}
	var orgRow int64
	if err := tx.QueryRow("SELECT id FROM labels WHERE org_id = ? AND name = ?", orgID, name).Scan(&orgRow); err != nil {
		return 0, err
	}
	rows, err := tx.Query(`SELECT l.id FROM labels l JOIN repos r ON r.id = l.repo_id
		WHERE r.owner_kind = 'org' AND r.owner_id = ? AND l.name = ?`, orgID, name)
	if err != nil {
		return 0, err
	}
	var repoRows []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, err
		}
		repoRows = append(repoRows, id)
	}
	rows.Close()
	for _, id := range repoRows {
		// OR IGNORE: an issue cannot carry both today, but the primary key
		// makes the move safe if it ever did.
		if _, err := tx.Exec("UPDATE OR IGNORE issue_labels SET label_id = ? WHERE label_id = ?", orgRow, id); err != nil {
			return 0, err
		}
		if _, err := tx.Exec("DELETE FROM labels WHERE id = ?", id); err != nil {
			return 0, err
		}
	}
	return len(repoRows), tx.Commit()
}

// DeleteOrgLabel removes an org's label from the org and from every issue
// under it.
func (s *Store) DeleteOrgLabel(orgID int64, name string) error {
	res, err := s.DB.Exec("DELETE FROM labels WHERE org_id = ? AND name = ?", orgID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
```

Add `"database/sql"` and `"errors"` to the imports of `labels.go`.

- [ ] **Step 5: Update `LabelColors` and `SetIssueLabel` in `issues.go`**

Replace both functions (lines 297-346):

```go
// LabelColors returns the colours of the labels a repository sees, keyed
// by name. Labels with no stored colour map to "".
func (s *Store) LabelColors(repo Repo) (map[string]string, error) {
	where, args := scopeClause("l", repo)
	rows, err := s.DB.Query("SELECT l.name, l.color FROM labels l WHERE "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var name, color string
		if err := rows.Scan(&name, &color); err != nil {
			return nil, err
		}
		out[name] = color
	}
	return out, rows.Err()
}

// SetIssueLabel attaches (add) or detaches a label by name. Adding
// resolves the org's row when the org has the name, else the repository's,
// creating that on first use.
func (s *Store) SetIssueLabel(repo Repo, issueID int64, name string, add bool) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	where, args := scopeClause("l", repo)
	if add {
		if held, err := orgHoldsLabel(tx, repo, name); err != nil {
			return err
		} else if !held {
			if _, err := tx.Exec(`INSERT INTO labels (repo_id, name) VALUES (?, ?)
				ON CONFLICT (repo_id, name) WHERE repo_id IS NOT NULL DO NOTHING`, repo.ID, name); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`INSERT INTO issue_labels (issue_id, label_id)
			SELECT ?, l.id FROM labels l WHERE `+where+` AND l.name = ?
			ORDER BY l.org_id IS NULL LIMIT 1
			ON CONFLICT DO NOTHING`, append(append([]any{issueID}, args...), name)...); err != nil {
			return err
		}
	} else {
		res, err := tx.Exec(`DELETE FROM issue_labels WHERE issue_id = ? AND label_id IN
			(SELECT l.id FROM labels l WHERE `+where+` AND l.name = ?)`,
			append(append([]any{issueID}, args...), name)...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return fmt.Errorf("label %q: %w", name, ErrNotFound)
		}
	}
	return tx.Commit()
}
```

SQLite needs a `WHERE` before `ON CONFLICT` after an `INSERT ... SELECT` to disambiguate; the `SELECT` above has one, so the upsert parses. If the parser rejects `ORDER BY ... LIMIT` before `ON CONFLICT`, wrap the select: `SELECT * FROM (SELECT ?, l.id FROM labels l WHERE ... ORDER BY l.org_id IS NULL LIMIT 1) WHERE true ON CONFLICT DO NOTHING`.

- [ ] **Step 6: Run the store tests**

Run: `go test ./internal/store/`
Expected: the four new tests PASS; existing store tests still PASS. `go build ./...` now fails in `control` and `httpd` on the changed signatures, which Task 4 fixes. Do not fix them here.

- [ ] **Step 7: Commit**

```bash
git add internal/store/scope.go internal/store/labels.go internal/store/issues.go internal/store/labels_test.go
git commit -m "store: labels resolve through the repository's org

Ref #203"
```

---

### Task 3: Store: milestones by scope, org milestones, promote

**Files:**
- Modify: `internal/store/milestones.go`
- Test: `internal/store/milestones_test.go` (new)

**Interfaces:**
- Produces:
  - `func (s *Store) CreateMilestone(repo Repo, title, description, due string) (int64, error)` — `ErrOrgScoped` when the org holds the title; "already exists" error on a repo duplicate as today.
  - `func (s *Store) MilestoneByTitle(repo Repo, title string) (Milestone, error)` — org row first.
  - `func (s *Store) ListMilestones(repo Repo, state string, readable []int64) ([]Milestone, error)` — org rows first.
  - `func (s *Store) CreateOrgMilestone(orgID int64, title, description, due string) (id int64, folded int, err error)`
  - `func (s *Store) OrgMilestoneByTitle(orgID int64, title string) (Milestone, error)`
  - `func (s *Store) ListOrgMilestones(orgID int64, state string, readable []int64) ([]Milestone, error)`
  - `SetMilestoneState`, `SetIssueMilestone`, `SetMRMilestone` unchanged.
- Consumes: `scopeClause`, `inClause`, `ErrOrgScoped` from Task 2; `Milestone.OrgID` from Task 1.

- [ ] **Step 1: Write the failing tests**

`internal/store/milestones_test.go`:

```go
package store

import (
	"errors"
	"testing"
)

func TestOrgMilestoneSpansRepos(t *testing.T) {
	f := newAcme(t)
	id, folded, err := f.s.CreateOrgMilestone(f.org, "v1", "first", "2027-01-01")
	if err != nil || folded != 0 || id == 0 {
		t.Fatalf("CreateOrgMilestone: %d, %d, %v", id, folded, err)
	}
	// Resolves from either repo, not from alice/app.
	m, err := f.s.MilestoneByTitle(f.core, "v1")
	if err != nil || m.OrgID != f.org || m.RepoID != 0 {
		t.Fatalf("core resolves v1 = %+v, %v", m, err)
	}
	if _, err := f.s.MilestoneByTitle(f.app, "v1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("app resolves v1: %v", err)
	}
	if err := f.s.SetIssueMilestone(f.coreIssue, id); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetIssueMilestone(f.siteIssue, id); err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetIssueState(f.siteIssue, "closed"); err != nil {
		t.Fatal(err)
	}
	ms, err := f.s.ListOrgMilestones(f.org, "open", f.orgRepos())
	if err != nil || len(ms) != 1 || ms[0].OpenItems != 1 || ms[0].ClosedItems != 1 {
		t.Fatalf("org list = %+v, %v", ms, err)
	}
	// Counts stop at what the caller can read.
	ms, _ = f.s.ListOrgMilestones(f.org, "open", []int64{f.core.ID})
	if ms[0].OpenItems != 1 || ms[0].ClosedItems != 0 {
		t.Fatalf("org list over core = %+v", ms)
	}
	// A repo's list shows the org milestone first, then its own.
	if _, err := f.s.CreateMilestone(f.core, "core-only", "", ""); err != nil {
		t.Fatal(err)
	}
	ms, _ = f.s.ListMilestones(f.core, "open", f.orgRepos())
	if len(ms) != 2 || ms[0].Title != "v1" || ms[0].OrgID != f.org || ms[1].Title != "core-only" || ms[1].RepoID != f.core.ID {
		t.Fatalf("core list = %+v", ms)
	}
	if _, err := f.s.OrgMilestoneByTitle(f.org, "core-only"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("org resolves a repo milestone: %v", err)
	}
}

func TestRepoMilestoneRefusedWhenOrgHoldsTitle(t *testing.T) {
	f := newAcme(t)
	if _, _, err := f.s.CreateOrgMilestone(f.org, "v1", "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.CreateMilestone(f.core, "v1", "", ""); !errors.Is(err, ErrOrgScoped) {
		t.Fatalf("CreateMilestone over org title: %v", err)
	}
	if _, err := f.s.CreateMilestone(f.app, "v1", "", ""); err != nil {
		t.Fatalf("user repo unaffected: %v", err)
	}
	if _, _, err := f.s.CreateOrgMilestone(f.org, "v1", "", ""); err == nil {
		t.Fatal("duplicate org milestone accepted")
	}
}

func TestCreateOrgMilestonePromotes(t *testing.T) {
	f := newAcme(t)
	cid, err := f.s.CreateMilestone(f.core, "v1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := f.s.CreateMilestone(f.site, "v1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetIssueMilestone(f.coreIssue, cid); err != nil {
		t.Fatal(err)
	}
	mrID, err := f.s.CreateMR(f.site.ID, f.alice, f.site.ID, "feat", "main", "t", "", "abc", "md", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.SetMRMilestone(mrID, sid); err != nil {
		t.Fatal(err)
	}
	id, folded, err := f.s.CreateOrgMilestone(f.org, "v1", "org wide", "2027-06-01")
	if err != nil || folded != 2 {
		t.Fatalf("promote: folded %d, %v", folded, err)
	}
	var n int
	f.s.DB.QueryRow("SELECT COUNT(*) FROM milestones WHERE title = 'v1'").Scan(&n)
	if n != 1 {
		t.Fatalf("milestones named v1: %d", n)
	}
	f.s.DB.QueryRow("SELECT COUNT(*) FROM issues WHERE milestone_id = ?", id).Scan(&n)
	if n != 1 {
		t.Fatalf("issues on org milestone: %d", n)
	}
	f.s.DB.QueryRow("SELECT COUNT(*) FROM merge_requests WHERE milestone_id = ?", id).Scan(&n)
	if n != 1 {
		t.Fatalf("mrs on org milestone: %d", n)
	}
	ms, _ := f.s.ListOrgMilestones(f.org, "open", f.orgRepos())
	if len(ms) != 1 || ms[0].OpenItems != 2 || ms[0].Description != "org wide" || ms[0].DueDate != "2027-06-01" {
		t.Fatalf("after promote: %+v", ms)
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/store/ -run 'OrgMilestone|RepoMilestoneRefused' -v`
Expected: build failure, `CreateOrgMilestone` and friends undefined.

- [ ] **Step 3: Rewrite `milestones.go` from `CreateMilestone` through `ListMilestones`**

```go
// orgHoldsMilestone reports whether the repository's org has a milestone
// of that title; always false for a user-owned repository.
func (s *Store) orgHoldsMilestone(repo Repo, title string) (bool, error) {
	if repo.OwnerKind != "org" {
		return false, nil
	}
	var n int
	err := s.DB.QueryRow("SELECT COUNT(*) FROM milestones WHERE org_id = ? AND title = ?", repo.OwnerID, title).Scan(&n)
	return n > 0, err
}

// CreateMilestone creates the repository's milestone. A title the org
// holds is refused with ErrOrgScoped.
func (s *Store) CreateMilestone(repo Repo, title, description, due string) (int64, error) {
	if held, err := s.orgHoldsMilestone(repo, title); err != nil || held {
		if err != nil {
			return 0, err
		}
		return 0, ErrOrgScoped
	}
	res, err := s.DB.Exec(
		"INSERT INTO milestones (repo_id, title, description, due_date) VALUES (?, ?, ?, ?)",
		repo.ID, title, description, due)
	if err != nil {
		if isUniqueErr(err) {
			return 0, fmt.Errorf("milestone %q already exists", title)
		}
		return 0, err
	}
	return res.LastInsertId()
}

// CreateOrgMilestone creates the org's milestone. Repositories under the
// org that hold the title are folded in: their issues and merge requests
// move to the org's row and their rows go. folded is how many were.
func (s *Store) CreateOrgMilestone(orgID int64, title, description, due string) (int64, int, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		"INSERT INTO milestones (org_id, title, description, due_date) VALUES (?, ?, ?, ?)",
		orgID, title, description, due)
	if err != nil {
		if isUniqueErr(err) {
			return 0, 0, fmt.Errorf("milestone %q already exists", title)
		}
		return 0, 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, 0, err
	}
	rows, err := tx.Query(`SELECT m.id FROM milestones m JOIN repos r ON r.id = m.repo_id
		WHERE r.owner_kind = 'org' AND r.owner_id = ? AND m.title = ?`, orgID, title)
	if err != nil {
		return 0, 0, err
	}
	var repoRows []int64
	for rows.Next() {
		var rid int64
		if err := rows.Scan(&rid); err != nil {
			rows.Close()
			return 0, 0, err
		}
		repoRows = append(repoRows, rid)
	}
	rows.Close()
	for _, rid := range repoRows {
		for _, table := range []string{"issues", "merge_requests"} {
			if _, err := tx.Exec("UPDATE "+table+" SET milestone_id = ? WHERE milestone_id = ?", id, rid); err != nil {
				return 0, 0, err
			}
		}
		if _, err := tx.Exec("DELETE FROM milestones WHERE id = ?", rid); err != nil {
			return 0, 0, err
		}
	}
	return id, len(repoRows), tx.Commit()
}

// milestoneQuery selects milestones with their progress, counting only
// items in the readable repositories. Its args come first in any query
// built on it.
func milestoneQuery(readable []int64) (string, []any) {
	in, args := inClause(readable)
	q := `
	SELECT m.id, COALESCE(m.repo_id, 0), COALESCE(m.org_id, 0), m.title, m.description, m.due_date, m.state, m.created_at,
	       (SELECT COUNT(*) FROM issues i WHERE i.milestone_id = m.id AND i.state = 'open' AND i.repo_id IN ` + in + `)
	     + (SELECT COUNT(*) FROM merge_requests r WHERE r.milestone_id = m.id AND r.state IN ('open','source_gone') AND r.repo_id IN ` + in + `),
	       (SELECT COUNT(*) FROM issues i WHERE i.milestone_id = m.id AND i.state = 'closed' AND i.repo_id IN ` + in + `)
	     + (SELECT COUNT(*) FROM merge_requests r WHERE r.milestone_id = m.id AND r.state IN ('merged','closed') AND r.repo_id IN ` + in + `)
	FROM milestones m`
	all := make([]any, 0, 4*len(args))
	for i := 0; i < 4; i++ {
		all = append(all, args...)
	}
	return q, all
}

func scanMilestone(row interface{ Scan(...any) error }) (Milestone, error) {
	var m Milestone
	err := row.Scan(&m.ID, &m.RepoID, &m.OrgID, &m.Title, &m.Description, &m.DueDate, &m.State,
		&m.CreatedAt, &m.OpenItems, &m.ClosedItems)
	return m, err
}

// milestoneByTitle resolves a title under where. The org's row comes
// first when both scopes are in play; creation keeps that from happening.
func (s *Store) milestoneByTitle(where string, args []any) (Milestone, error) {
	q, qargs := milestoneQuery(nil)
	m, err := scanMilestone(s.DB.QueryRow(q+" WHERE "+where+" ORDER BY m.org_id IS NULL LIMIT 1", append(qargs, args...)...))
	if errors.Is(err, sql.ErrNoRows) {
		return m, ErrNotFound
	}
	return m, err
}

// MilestoneByTitle resolves a title the way attaching does: the org's
// milestone when the org has it, else the repository's. Progress counts
// are not populated here; list for those.
func (s *Store) MilestoneByTitle(repo Repo, title string) (Milestone, error) {
	where, args := scopeClause("m", repo)
	return s.milestoneByTitle(where+" AND m.title = ?", append(args, title))
}

func (s *Store) OrgMilestoneByTitle(orgID int64, title string) (Milestone, error) {
	return s.milestoneByTitle("m.org_id = ? AND m.title = ?", []any{orgID, title})
}

func (s *Store) listMilestones(where string, args []any, state string, readable []int64) ([]Milestone, error) {
	q, qargs := milestoneQuery(readable)
	q += " WHERE " + where
	qargs = append(qargs, args...)
	if state != "all" {
		q += " AND m.state = ?"
		qargs = append(qargs, state)
	}
	q += " ORDER BY m.org_id IS NULL, m.due_date = '', m.due_date, m.title"
	rows, err := s.DB.Query(q, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Milestone
	for rows.Next() {
		m, err := scanMilestone(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ListMilestones lists the milestones a repository sees, the org's first,
// with progress counted over the readable repositories.
func (s *Store) ListMilestones(repo Repo, state string, readable []int64) ([]Milestone, error) {
	where, args := scopeClause("m", repo)
	return s.listMilestones(where, args, state, readable)
}

// ListOrgMilestones lists an org's milestones with progress across the
// readable repositories under it.
func (s *Store) ListOrgMilestones(orgID int64, state string, readable []int64) ([]Milestone, error) {
	return s.listMilestones("m.org_id = ?", []any{orgID}, state, readable)
}
```

Delete the old `milestoneSelect` constant. Keep `SetMilestoneState`, `SetIssueMilestone`, `SetMRMilestone`, `setItemMilestone` as they are.

- [ ] **Step 4: Run the store tests**

Run: `go test ./internal/store/`
Expected: all PASS, including `TestMigration0052...` and the label tests.

- [ ] **Step 5: Commit**

```bash
git add internal/store/milestones.go internal/store/milestones_test.go
git commit -m "store: milestones resolve through the repository's org

Ref #203"
```

---

### Task 4: Callers compile; repo-level refusals; readable-scope helper

**Files:**
- Create: `internal/control/scope.go`
- Modify: `internal/control/label.go:33-119`
- Modify: `internal/control/milestone.go:44-66, 68-88, 118-141, 181-190`
- Modify: `internal/control/issue.go:391, 396`
- Modify: `internal/control/ghimport.go:259`
- Modify: `internal/control/migrate.go:250`
- Modify: `internal/httpd/labels.go:20, 31`
- Modify: `internal/httpd/web.go:705, 1606-1618, 1675, 1700, 1713`
- Test: `internal/control/orgscope_test.go` (new)

**Interfaces:**
- Produces in `internal/control/scope.go`:
  - `func ReadableOrgRepoIDs(st *store.Store, user store.User, orgID int64) ([]int64, error)` — ids of the org's repositories `user` can read (`policy.CanRead` with `AccessRole`; user with ID 0 is anonymous).
  - `func ReadableScope(st *store.Store, user store.User, repo store.Repo) ([]int64, error)` — `ReadableOrgRepoIDs` for an org-owned repo, `[]int64{repo.ID}` otherwise.
  - `func orgScopedMsg(repo store.Repo, noun, name, cmd string) string` — the refusal text: `"%s is an org %s of %s; manage it with org %s %s %s"`.
- Consumes: Task 2 and Task 3 signatures.

- [ ] **Step 1: Write the failing tests**

`internal/control/orgscope_test.go`:

```go
package control

import (
	"bytes"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// orgFixture: alice admins org acme with acme/core (public) and acme/priv
// (private); bob is a plain member; carol is outside. alice also owns
// alice/app.
type orgFixture struct {
	st                 *store.Store
	alice, bob, carol  int64
	org                int64
	core, priv, app    store.Repo
}

func newOrgFixture(t *testing.T) orgFixture {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	var f orgFixture
	f.st = st
	user := func(name string) int64 {
		id, err := st.CreateUser(name, false)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.alice, f.bob, f.carol = user("alice"), user("bob"), user("carol")
	if f.org, err = st.CreateOrg("acme", f.alice); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgMember(f.org, f.bob, "member"); err != nil {
		t.Fatal(err)
	}
	repo := func(kind string, owner int64, name, vis string) store.Repo {
		id, err := st.CreateRepo(kind, owner, name, vis)
		if err != nil {
			t.Fatal(err)
		}
		r, _ := st.RepoByID(id)
		return r
	}
	f.core = repo("org", f.org, "core", "public")
	f.priv = repo("org", f.org, "priv", "private")
	f.app = repo("user", f.alice, "app", "public")
	return f
}

func (f orgFixture) ctx(uid int64) (*Ctx, *bytes.Buffer) {
	var out bytes.Buffer
	name := map[int64]string{f.alice: "alice", f.bob: "bob", f.carol: "carol"}[uid]
	return &Ctx{
		User:   store.User{ID: uid, Username: name},
		Scope:  "full",
		Source: "SHA256:session",
		Store:  f.st,
		Cfg:    config.Config{Server: config.Server{SiteURL: "https://x.test"}},
		Stdin:  strings.NewReader(""),
		Stdout: &out,
		Stderr: &out,
		JSON:   true,
	}, &out
}

func TestReadableOrgRepoIDs(t *testing.T) {
	f := newOrgFixture(t)
	ids, err := ReadableOrgRepoIDs(f.st, store.User{ID: f.bob, Username: "bob"}, f.org)
	if err != nil || len(ids) != 2 {
		t.Fatalf("member reads %v, %v; want both", ids, err)
	}
	ids, _ = ReadableOrgRepoIDs(f.st, store.User{ID: f.carol, Username: "carol"}, f.org)
	if len(ids) != 1 || ids[0] != f.core.ID {
		t.Fatalf("outsider reads %v; want core only", ids)
	}
	ids, _ = ReadableOrgRepoIDs(f.st, store.User{}, f.org)
	if len(ids) != 1 || ids[0] != f.core.ID {
		t.Fatalf("anonymous reads %v; want core only", ids)
	}
	ids, _ = ReadableScope(f.st, store.User{ID: f.alice, Username: "alice"}, f.app)
	if len(ids) != 1 || ids[0] != f.app.ID {
		t.Fatalf("user repo scope %v; want itself", ids)
	}
}

func TestRepoLabelCommandsRefuseOrgNames(t *testing.T) {
	f := newOrgFixture(t)
	if _, err := f.st.SetOrgLabel(f.org, "bug", ""); err != nil {
		t.Fatal(err)
	}
	c, out := f.ctx(f.alice)
	if code := runLabelSet(c, []string{"acme/core", "bug", "--color", "ff0000"}); code != protocol.ExitFailure ||
		!strings.Contains(out.String(), "org label set acme bug") {
		t.Fatalf("label set over org name: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runLabelRemove(c, []string{"acme/core", "bug"}); code != protocol.ExitFailure ||
		!strings.Contains(out.String(), "org label remove acme bug") {
		t.Fatalf("label remove of org row: exit %d %s", code, out.String())
	}
	out.Reset()
	// issue label --add resolves to the org row, and label list marks it.
	iid, _ := f.st.CreateIssue(f.core.ID, f.alice, "c1", "", "md")
	_ = iid
	if code := runIssueLabel(c, []string{"acme/core", "1", "--add", "bug"}); code != protocol.ExitOK {
		t.Fatalf("issue label: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runLabelList(c, []string{"acme/core"}); code != protocol.ExitOK ||
		!strings.Contains(out.String(), `"org":true`) || !strings.Contains(out.String(), `"issues":1`) {
		t.Fatalf("label list: exit %d %s", code, out.String())
	}
}

func TestRepoMilestoneCommandsRefuseOrgTitles(t *testing.T) {
	f := newOrgFixture(t)
	if _, _, err := f.st.CreateOrgMilestone(f.org, "v1", "", ""); err != nil {
		t.Fatal(err)
	}
	c, out := f.ctx(f.alice)
	if code := runMilestoneCreate(c, []string{"acme/core", "v1"}); code != protocol.ExitFailure ||
		!strings.Contains(out.String(), "org milestone create acme v1") {
		t.Fatalf("milestone create over org title: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runMilestoneClose(c, []string{"acme/core", "v1"}); code != protocol.ExitFailure ||
		!strings.Contains(out.String(), "org milestone close acme v1") {
		t.Fatalf("milestone close of org row: exit %d %s", code, out.String())
	}
	out.Reset()
	// Attaching by title from a repo resolves the org milestone.
	f.st.CreateIssue(f.core.ID, f.alice, "c1", "", "md")
	if code := runIssueMilestone(c, []string{"acme/core", "1", "v1"}); code != protocol.ExitOK {
		t.Fatalf("issue milestone: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runMilestoneList(c, []string{"acme/core"}); code != protocol.ExitOK ||
		!strings.Contains(out.String(), `"org":true`) || !strings.Contains(out.String(), `"open":1`) {
		t.Fatalf("milestone list: exit %d %s", code, out.String())
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/control/ -run 'ReadableOrgRepoIDs|RefuseOrg' -v`
Expected: build failure (`ReadableOrgRepoIDs` undefined, plus the package does not compile against Task 2/3 signatures yet).

- [ ] **Step 3: Write `internal/control/scope.go`**

```go
package control

import (
	"fmt"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/store"
)

// ReadableOrgRepoIDs is the org's repositories user may read. Counts on
// org labels and milestones are taken over these, so a private
// repository's issues never show in a number someone outside it sees. A
// zero user is anonymous.
func ReadableOrgRepoIDs(st *store.Store, user store.User, orgID int64) ([]int64, error) {
	repos, err := st.ListReposForOwner("org", orgID)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for _, r := range repos {
		grant := ""
		if user.ID != 0 {
			if grant, err = st.AccessRole(r.ID, user.ID); err != nil {
				return nil, err
			}
		}
		if policy.CanRead(user, r, grant) {
			ids = append(ids, r.ID)
		}
	}
	return ids, nil
}

// ReadableScope is the set a repository's label and milestone counts
// span: its org's readable repositories, or just itself when a user owns
// it. The caller has already been allowed to read repo.
func ReadableScope(st *store.Store, user store.User, repo store.Repo) ([]int64, error) {
	if repo.OwnerKind == "org" {
		return ReadableOrgRepoIDs(st, user, repo.OwnerID)
	}
	return []int64{repo.ID}, nil
}

// orgScopedMsg names the org command that manages a row a repository
// command was asked to change.
func orgScopedMsg(repo store.Repo, noun, name, verb string) string {
	return fmt.Sprintf("%s is an org %s of %s; manage it with org %s %s %s %s", name, noun, repo.OwnerName, noun, verb, repo.OwnerName, name)
}
```

- [ ] **Step 4: Update `label.go`**

In `runLabelList`, replace the `ListLabels` call:

```go
	readable, err := ReadableScope(c.Store, c.User, repo)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	labels, err := c.Store.ListLabels(repo, readable)
```

and print the mark in the plain output: `fmt.Fprintf(w, "%s\t%s\t%d%s\n", l.Name, l.Color, l.Issues, map[bool]string{true: "\torg"}[l.Org])`.

In `runLabelSet`, replace the colour-keeping block and the store call:

```go
	if !colorSet {
		// Keep the colour it has, if any; this is "make sure it exists".
		if l, err := c.Store.LabelByName(repo, name); err == nil && !l.Org {
			color = l.Color
		}
	}
	if err := c.Store.SetLabel(repo, name, color); err != nil {
		if errors.Is(err, store.ErrOrgScoped) {
			return c.fail(protocol.ExitFailure, "%s", orgScopedMsg(repo, "label", name, "set"))
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
```

In `runLabelRemove`:

```go
	if err := c.Store.DeleteLabel(repo, args[1]); err != nil {
		if errors.Is(err, store.ErrOrgScoped) {
			return c.fail(protocol.ExitFailure, "%s", orgScopedMsg(repo, "label", args[1], "remove"))
		}
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no label %q in %s", args[1], repo.Path())
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
```

- [ ] **Step 5: Update `milestone.go`**

`runMilestoneCreate`:

```go
	if _, err := c.Store.CreateMilestone(repo, title, description, due); err != nil {
		if errors.Is(err, store.ErrOrgScoped) {
			return c.fail(protocol.ExitFailure, "%s", orgScopedMsg(repo, "milestone", title, "create"))
		}
		return c.failErr(err)
	}
```

`runMilestoneList`: compute `readable` with `ReadableScope` as in label list, call `c.Store.ListMilestones(repo, state, readable)`, add `Org bool `json:"org,omitempty"`` to the `out` struct after `State`, fill it with `m.OrgID != 0`, and append `\torg` to the plain line when set.

`setMilestoneState`, after `MilestoneByTitle(repo, args[1])`:

```go
	if m.OrgID != 0 {
		return c.fail(protocol.ExitFailure, "%s", orgScopedMsg(repo, "milestone", m.Title, verb))
	}
```

`setItemMilestone`: `c.Store.MilestoneByTitle(repo, title)`.

- [ ] **Step 6: Update the remaining callers**

- `internal/control/issue.go:391,396`: `c.Store.SetIssueLabel(repo, issue.ID, l, true)` / `false`.
- `internal/control/ghimport.go:259` and `internal/control/migrate.go:250`: `SetIssueLabel(repo, iss.ID, ...)`. Check each has a `repo store.Repo` in scope; both do, it is what `repo.ID` came from.
- `internal/httpd/web.go:1606`: `func (s *Server) labelColors(repo store.Repo) map[string]template.CSS` with `s.st.LabelColors(repo)`; callers at 1675 and 1713 pass `p.Repo`. Split the colour derivation into `func colorStyles(stored map[string]string) map[string]template.CSS` (the loop body as it stands) so Task 8 can reuse it for the org page; `labelColors` becomes `stored, _ := s.st.LabelColors(repo); return colorStyles(stored)`.
- `internal/httpd/web.go:705`: `readable, _ := control.ReadableOrgRepoIDs(...)` is wrong for a repo page; use `readable, err := control.ReadableScope(s.st, s.viewer(r), p.Repo)` then `s.st.ListMilestones(p.Repo, state, readable)`. Same at 1700 (`"open"`).
- `internal/httpd/labels.go:20`: `readable, err := control.ReadableScope(s.st, s.viewer(r), p.Repo)` then `s.st.ListLabels(p.Repo, readable)`; line 31 `s.labelColors(p.Repo)`.

`httpd` already imports `control`; `web.go` needs no new import.

- [ ] **Step 7: Build, vet, test**

Run: `go build ./... && go vet ./... && go test ./internal/control/ ./internal/httpd/ ./internal/store/`
Expected: all PASS. `TestReadableOrgRepoIDs`, `TestRepoLabelCommandsRefuseOrgNames`, `TestRepoMilestoneCommandsRefuseOrgTitles` PASS.

- [ ] **Step 8: Commit**

```bash
git add internal/control/scope.go internal/control/label.go internal/control/milestone.go internal/control/issue.go internal/control/ghimport.go internal/control/migrate.go internal/httpd/labels.go internal/httpd/web.go internal/control/orgscope_test.go
git commit -m "control, web: repository commands see org labels and milestones and refuse to change them

Ref #203"
```

---

### Task 5: `org label set|list|remove`

**Files:**
- Create: `internal/control/orglabel.go`
- Modify: `cmd/gitbay/main.go:642-670` (org group)
- Modify: `e2e/readonly_test.go:85+` (`readArgs`)
- Test: `internal/control/orglabel_test.go` (new)

**Interfaces:**
- Produces: commands `org label set <org> <label> [--color rrggbb|'']`, `org label list <org>` (ReadOnly), `org label remove <org> <label>`; `runOrgLabelSet`, `runOrgLabelList`, `runOrgLabelRemove`.
- Produces: `func orgReader(c *Ctx, name string) (store.Org, []int64, int)` — resolves an org for a read: not-found when absent; members pass; an outsider passes only if some repository under it is readable, else `ExitDenied` "labels and milestones of %s are visible to its members". Returns the readable ids.
- Consumes: `orgAdmin` from `org.go`, `ReadableOrgRepoIDs` from Task 4, store functions from Task 2, `labelColorPat` from `label.go`.

- [ ] **Step 1: Write the failing tests**

`internal/control/orglabel_test.go`:

```go
package control

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
)

func TestOrgLabelSetListRemove(t *testing.T) {
	f := newOrgFixture(t)
	// Two repos already hold bug; the org set folds them in.
	f.st.SetLabel(f.core, "bug", "")
	f.st.SetLabel(f.priv, "bug", "")
	c, out := f.ctx(f.alice)
	if code := runOrgLabelSet(c, []string{"acme", "bug", "--color", "ff0000"}); code != protocol.ExitOK ||
		!strings.Contains(out.String(), `"folded":2`) {
		t.Fatalf("set: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runOrgLabelList(c, []string{"acme"}); code != protocol.ExitOK ||
		!strings.Contains(out.String(), `"name":"bug"`) || !strings.Contains(out.String(), `"color":"#ff0000"`) {
		t.Fatalf("list: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runOrgLabelRemove(c, []string{"acme", "bug"}); code != protocol.ExitOK {
		t.Fatalf("remove: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runOrgLabelRemove(c, []string{"acme", "bug"}); code != protocol.ExitNotFound {
		t.Fatalf("second remove: exit %d %s", code, out.String())
	}
}

func TestOrgLabelWritesNeedOrgAdmin(t *testing.T) {
	f := newOrgFixture(t)
	c, out := f.ctx(f.bob)
	if code := runOrgLabelSet(c, []string{"acme", "bug"}); code != protocol.ExitDenied {
		t.Fatalf("member set: exit %d %s", code, out.String())
	}
	if code := runOrgLabelRemove(c, []string{"acme", "bug"}); code != protocol.ExitDenied {
		t.Fatalf("member remove: exit %d %s", code, out.String())
	}
	c, out = f.ctx(f.alice)
	if code := runOrgLabelSet(c, []string{"nope", "bug"}); code != protocol.ExitNotFound {
		t.Fatalf("missing org: exit %d %s", code, out.String())
	}
	if code := runOrgLabelSet(c, []string{"acme", "bug", "--color", "zz"}); code != protocol.ExitUsage {
		t.Fatalf("bad colour: exit %d %s", code, out.String())
	}
}

func TestOrgLabelListVisibility(t *testing.T) {
	f := newOrgFixture(t)
	f.st.SetOrgLabel(f.org, "bug", "")
	// Members read; an outsider reads because acme/core is public.
	for _, uid := range []int64{f.bob, f.carol} {
		c, out := f.ctx(uid)
		if code := runOrgLabelList(c, []string{"acme"}); code != protocol.ExitOK {
			t.Fatalf("user %d list: exit %d %s", uid, code, out.String())
		}
	}
	// With every repo private, the outsider is refused, not told the org
	// is missing.
	f.st.SetRepoVisibility(f.core.ID, "private")
	c, out := f.ctx(f.carol)
	if code := runOrgLabelList(c, []string{"acme"}); code != protocol.ExitDenied ||
		!strings.Contains(out.String(), "visible to its members") {
		t.Fatalf("outsider list: exit %d %s", code, out.String())
	}
}
```

- [ ] **Step 2: Run them to verify they fail**

Run: `go test ./internal/control/ -run OrgLabel -v`
Expected: build failure, `runOrgLabelSet` undefined.

- [ ] **Step 3: Write `orglabel.go`**

```go
package control

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"org", "label", "set"},
		Summary: "create an org label every org repository sees, or set its colour; folds in same-named repo labels",
		Usage:   "org label set <org> <label> [--color rrggbb|'']", Run: runOrgLabelSet})
	register(Command{Path: []string{"org", "label", "list"},
		Summary: "list an org's labels with use across the repositories you can read",
		Usage:   "org label list <org>", ReadOnly: true, Run: runOrgLabelList})
	register(Command{Path: []string{"org", "label", "remove"},
		Summary: "remove an org label from the org and from every issue under it",
		Usage:   "org label remove <org> <label>", Run: runOrgLabelRemove})
}

// orgReader resolves an org for a read of its labels or milestones.
// Members read; an outsider reads when some repository under the org is
// readable, and is refused rather than told the org is missing otherwise,
// since an org's existence is public anyway. The readable ids come back
// because every read counts over them.
func orgReader(c *Ctx, name string) (store.Org, []int64, int) {
	org, err := c.Store.OrgByName(name)
	if errors.Is(err, store.ErrNotFound) {
		return org, nil, c.fail(protocol.ExitNotFound, "no organization %q", name)
	}
	if err != nil {
		return org, nil, c.fail(protocol.ExitFailure, "%v", err)
	}
	readable, err := ReadableOrgRepoIDs(c.Store, c.User, org.ID)
	if err != nil {
		return org, nil, c.fail(protocol.ExitFailure, "%v", err)
	}
	role, err := c.Store.OrgRole(org.ID, c.User.ID)
	if err != nil {
		return org, nil, c.fail(protocol.ExitFailure, "%v", err)
	}
	if role == "" && len(readable) == 0 {
		return org, nil, c.fail(protocol.ExitDenied, "labels and milestones of %s are visible to its members", name)
	}
	return org, readable, -1
}

func runOrgLabelSet(c *Ctx, args []string) int {
	const usage = "usage: org label set <org> <label> [--color rrggbb|'']"
	f, err := parseFlags(args, flagSpec{Values: []string{"--color"}, MaxPos: 2, Usage: usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	orgName, name := f.pos(0), f.pos(1)
	color, colorSet := strings.ToLower(f.Value("--color")), f.Has("--color")
	if orgName == "" || name == "" {
		return c.fail(protocol.ExitUsage, usage)
	}
	if name == "" || len(name) > 50 {
		return c.fail(protocol.ExitUsage, "a label is 1 to 50 characters")
	}
	if colorSet && color != "" {
		if !labelColorPat.MatchString(color) {
			return c.fail(protocol.ExitUsage, "--color takes rrggbb (with or without #), or '' to clear")
		}
		color = "#" + strings.TrimPrefix(color, "#")
	}
	org, code := orgAdmin(c, orgName)
	if code >= 0 {
		return code
	}
	if !colorSet {
		// Keep the colour it has, if any; this is "make sure it exists".
		if labels, err := c.Store.ListOrgLabels(org.ID, nil); err == nil {
			for _, l := range labels {
				if l.Name == name {
					color = l.Color
				}
			}
		}
	}
	folded, err := c.Store.SetOrgLabel(org.ID, name, color)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(struct {
		Name   string `json:"name"`
		Color  string `json:"color,omitempty"`
		Folded int    `json:"folded"`
	}{name, color, folded}, func(w io.Writer) {
		if color == "" {
			fmt.Fprintf(w, "org label %s on %s, no colour set", name, org.Name)
		} else {
			fmt.Fprintf(w, "org label %s on %s is %s", name, org.Name, color)
		}
		if folded > 0 {
			fmt.Fprintf(w, "; folded in %d repositor%s", folded, map[bool]string{true: "y", false: "ies"}[folded == 1])
		}
		fmt.Fprintln(w)
	})
}

func runOrgLabelList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: org label list <org>")
	}
	org, readable, code := orgReader(c, args[0])
	if code >= 0 {
		return code
	}
	labels, err := c.Store.ListOrgLabels(org.ID, readable)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(labels, func(w io.Writer) {
		for _, l := range labels {
			fmt.Fprintf(w, "%s\t%s\t%d\n", l.Name, l.Color, l.Issues)
		}
	})
}

func runOrgLabelRemove(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: org label remove <org> <label>")
	}
	org, code := orgAdmin(c, args[0])
	if code >= 0 {
		return code
	}
	if err := c.Store.DeleteOrgLabel(org.ID, args[1]); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "no org label %q on %s", args[1], org.Name)
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"removed": args[1]}, func(w io.Writer) {
		fmt.Fprintf(w, "removed org label %s from %s\n", args[1], org.Name)
	})
}
```

`orgAdmin` in `org.go` uses `c.fail(protocol.ExitNotFound, ...)` for a missing org and `ExitDenied` for a non-admin; both tests above rely on that.

- [ ] **Step 4: Add the CLI rows**

In `cmd/gitbay/main.go`, inside `orgCmd()`'s `group("org", ...)` after the `members` group:

```go
		group("label", "labels every org repository sees",
			pass("set", "create an org label or set its colour: <org> <label> [--color rrggbb|'']", passOpts{server: []string{"org", "label", "set"}}),
			pass("list", "list org labels with use across readable repositories: <org>", passOpts{server: []string{"org", "label", "list"}}),
			pass("remove", "remove an org label everywhere: <org> <label>", passOpts{server: []string{"org", "label", "remove"}}),
		),
```

In `e2e/readonly_test.go` add to `readArgs` after `"org team show"`:

```go
		"org label list":              {"theorg"},
```

- [ ] **Step 5: Run the tests**

Run: `go build ./... && go test ./internal/control/ -run 'OrgLabel' -v && go test ./cmd/gitbay/`
Expected: PASS, including the CLI coverage test.

- [ ] **Step 6: Commit**

```bash
git add internal/control/orglabel.go internal/control/orglabel_test.go cmd/gitbay/main.go e2e/readonly_test.go
git commit -m "control, cli: org label set, list, remove

Ref #203"
```

---

### Task 6: `org milestone create|list|close|reopen`

**Files:**
- Modify: `internal/control/orglabel.go` (append)
- Modify: `cmd/gitbay/main.go` (org group)
- Modify: `e2e/readonly_test.go` (`readArgs`)
- Test: `internal/control/orglabel_test.go` (append)

**Interfaces:**
- Produces: `org milestone create <org> <title> [--description <d>] [--due YYYY-MM-DD]`, `org milestone list <org> [--state open|closed|all]` (ReadOnly), `org milestone close|reopen <org> <title>`; `runOrgMilestoneCreate`, `runOrgMilestoneList`, `runOrgMilestoneClose`, `runOrgMilestoneReopen`.
- Consumes: `orgReader` and `orgAdmin`; `duePat` from `milestone.go`; store functions from Task 3.

- [ ] **Step 1: Write the failing tests**

Append to `internal/control/orglabel_test.go`:

```go
func TestOrgMilestoneLifecycle(t *testing.T) {
	f := newOrgFixture(t)
	f.st.CreateMilestone(f.core, "v1", "", "")
	c, out := f.ctx(f.alice)
	if code := runOrgMilestoneCreate(c, []string{"acme", "v1", "--due", "2027-01-01"}); code != protocol.ExitOK ||
		!strings.Contains(out.String(), `"folded":1`) {
		t.Fatalf("create: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runOrgMilestoneCreate(c, []string{"acme", "v1"}); code != protocol.ExitFailure {
		t.Fatalf("duplicate create: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runOrgMilestoneCreate(c, []string{"acme", "v2", "--due", "soon"}); code != protocol.ExitUsage {
		t.Fatalf("bad due: exit %d %s", code, out.String())
	}
	out.Reset()
	// An issue in each repo attaches by title; progress spans both.
	f.st.CreateIssue(f.core.ID, f.alice, "c1", "", "md")
	f.st.CreateIssue(f.priv.ID, f.alice, "p1", "", "md")
	runIssueMilestone(c, []string{"acme/core", "1", "v1"})
	runIssueMilestone(c, []string{"acme/priv", "1", "v1"})
	out.Reset()
	if code := runOrgMilestoneList(c, []string{"acme"}); code != protocol.ExitOK ||
		!strings.Contains(out.String(), `"open":2`) || !strings.Contains(out.String(), `"due":"2027-01-01"`) {
		t.Fatalf("list: exit %d %s", code, out.String())
	}
	out.Reset()
	// carol reads only the public repo's count.
	cc, cout := f.ctx(f.carol)
	if code := runOrgMilestoneList(cc, []string{"acme"}); code != protocol.ExitOK || !strings.Contains(cout.String(), `"open":1`) {
		t.Fatalf("outsider list: exit %d %s", code, cout.String())
	}
	if code := runOrgMilestoneClose(c, []string{"acme", "v1"}); code != protocol.ExitOK {
		t.Fatalf("close: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runOrgMilestoneList(c, []string{"acme"}); code != protocol.ExitOK || strings.Contains(out.String(), `"title":"v1"`) {
		t.Fatalf("closed still listed as open: %s", out.String())
	}
	out.Reset()
	if code := runOrgMilestoneReopen(c, []string{"acme", "v1"}); code != protocol.ExitOK {
		t.Fatalf("reopen: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runOrgMilestoneClose(c, []string{"acme", "nope"}); code != protocol.ExitNotFound {
		t.Fatalf("close missing: exit %d %s", code, out.String())
	}
	bc, bout := f.ctx(f.bob)
	if code := runOrgMilestoneClose(bc, []string{"acme", "v1"}); code != protocol.ExitDenied {
		t.Fatalf("member close: exit %d %s", code, bout.String())
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/control/ -run OrgMilestoneLifecycle -v`
Expected: build failure, `runOrgMilestoneCreate` undefined.

- [ ] **Step 3: Append to `orglabel.go`**

Add to `init()`:

```go
	register(Command{Path: []string{"org", "milestone", "create"},
		Summary: "create an org milestone spanning every org repository; folds in same-titled repo milestones",
		Usage:   "org milestone create <org> <title> [--description <d>] [--due YYYY-MM-DD]", Run: runOrgMilestoneCreate})
	register(Command{Path: []string{"org", "milestone", "list"},
		Summary: "list an org's milestones with progress across the repositories you can read",
		Usage:   "org milestone list <org> [--state open|closed|all]", ReadOnly: true, Run: runOrgMilestoneList})
	register(Command{Path: []string{"org", "milestone", "close"},
		Summary: "close an org milestone",
		Usage:   "org milestone close <org> <title>", Run: runOrgMilestoneClose})
	register(Command{Path: []string{"org", "milestone", "reopen"},
		Summary: "reopen an org milestone",
		Usage:   "org milestone reopen <org> <title>", Run: runOrgMilestoneReopen})
```

And the functions:

```go
func runOrgMilestoneCreate(c *Ctx, args []string) int {
	const usage = "usage: org milestone create <org> <title> [--description <d>] [--due YYYY-MM-DD]"
	f, err := parseFlags(args, flagSpec{Values: []string{"--description", "--due"}, MaxPos: 2, Usage: usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	orgName, title, description, due := f.pos(0), f.pos(1), f.Value("--description"), f.Value("--due")
	if orgName == "" || title == "" {
		return c.fail(protocol.ExitUsage, usage)
	}
	if due != "" && !duePat.MatchString(due) {
		return c.fail(protocol.ExitUsage, "--due must be YYYY-MM-DD")
	}
	org, code := orgAdmin(c, orgName)
	if code >= 0 {
		return code
	}
	_, folded, err := c.Store.CreateOrgMilestone(org.ID, title, description, due)
	if err != nil {
		return c.failErr(err)
	}
	return c.emit(struct {
		Milestone string `json:"milestone"`
		Folded    int    `json:"folded"`
	}{title, folded}, func(w io.Writer) {
		fmt.Fprintf(w, "created org milestone %q on %s", title, org.Name)
		if folded > 0 {
			fmt.Fprintf(w, "; folded in %d repositor%s", folded, map[bool]string{true: "y", false: "ies"}[folded == 1])
		}
		fmt.Fprintln(w)
	})
}

func runOrgMilestoneList(c *Ctx, args []string) int {
	f, err := parseFlags(args, flagSpec{Values: []string{"--state"}, MaxPos: 1, Usage: "org milestone list <org> [--state open|closed|all]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	state, orgName := "open", f.pos(0)
	if f.Has("--state") {
		state = f.Value("--state")
	}
	if orgName == "" || (state != "open" && state != "closed" && state != "all") {
		return c.fail(protocol.ExitUsage, "usage: org milestone list <org> [--state open|closed|all]")
	}
	org, readable, code := orgReader(c, orgName)
	if code >= 0 {
		return code
	}
	ms, err := c.Store.ListOrgMilestones(org.ID, state, readable)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		Title       string `json:"title"`
		Description string `json:"description,omitempty"`
		Due         string `json:"due,omitempty"`
		State       string `json:"state"`
		Open        int    `json:"open"`
		Closed      int    `json:"closed"`
	}
	var ds []out
	for _, m := range ms {
		ds = append(ds, out{m.Title, m.Description, m.DueDate, m.State, m.OpenItems, m.ClosedItems})
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			due := d.Due
			if due == "" {
				due = "-"
			}
			fmt.Fprintf(w, "%s\t%s\tdue %s\t%d open, %d closed\n", d.Title, d.State, due, d.Open, d.Closed)
		}
	})
}

func runOrgMilestoneClose(c *Ctx, args []string) int  { return setOrgMilestoneState(c, args, "closed") }
func runOrgMilestoneReopen(c *Ctx, args []string) int { return setOrgMilestoneState(c, args, "open") }

func setOrgMilestoneState(c *Ctx, args []string, state string) int {
	verb := "close"
	if state == "open" {
		verb = "reopen"
	}
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: org milestone %s <org> <title>", verb)
	}
	org, code := orgAdmin(c, args[0])
	if code >= 0 {
		return code
	}
	m, err := c.Store.OrgMilestoneByTitle(org.ID, args[1])
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "no org milestone %q on %s", args[1], org.Name)
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := c.Store.SetMilestoneState(m.ID, state); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"milestone": m.Title, "state": state}, func(w io.Writer) {
		fmt.Fprintf(w, "%sd org milestone %q on %s\n", verb, m.Title, org.Name)
	})
}
```

- [ ] **Step 4: CLI rows and the read-only table**

In `orgCmd()` after the `label` group:

```go
		group("milestone", "milestones spanning an org's repositories",
			pass("create", "create an org milestone: <org> <title> [--description d] [--due YYYY-MM-DD]", passOpts{server: []string{"org", "milestone", "create"}}),
			pass("list", "list org milestones with progress: <org> [--state open|closed|all]", passOpts{server: []string{"org", "milestone", "list"}}),
			pass("close", "close an org milestone: <org> <title>", passOpts{server: []string{"org", "milestone", "close"}}),
			pass("reopen", "reopen an org milestone: <org> <title>", passOpts{server: []string{"org", "milestone", "reopen"}}),
		),
```

In `e2e/readonly_test.go` `readArgs`: `"org milestone list": {"theorg"},`.

- [ ] **Step 5: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/control/ ./cmd/gitbay/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/control/orglabel.go internal/control/orglabel_test.go cmd/gitbay/main.go e2e/readonly_test.go
git commit -m "control, cli: org milestone create, list, close, reopen

Ref #203"
```

---

### Task 7: Cross-repository closes

**Files:**
- Modify: `internal/control/commitrefs.go`
- Modify: `internal/control/commitrefs_test.go`

**Interfaces:**
- Produces: `type closeRef struct { Path string; N int64 }`; `func closingRefs(text string) []closeRef`; `func closeTarget(st *store.Store, source store.Repo, actorID int64, path string) (store.Repo, bool)`; `func actOnIssue(st *store.Store, source, target store.Repo, actorID int64, sha string, number int64, close bool, subject, author string)`.
- `ProcessCommitMessages` and `ProcessMRDescription` keep their signatures; callers in `internal/hookd/hookd.go:240` and `internal/control/mr.go:1211-1212` do not change.

- [ ] **Step 1: Update the unit test and add the cross-repo cases**

Replace `internal/control/commitrefs_test.go`:

```go
package control

import (
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

// The same keyword set has to work wherever the intent is written: a
// commit message, or a merge request title or body.
func TestClosingRefs(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []closeRef
	}{
		{"closes", "Closes #50", []closeRef{{"", 50}}},
		{"lowercase and fix", "fixes #7", []closeRef{{"", 7}}},
		{"resolved", "resolved: #12", []closeRef{{"", 12}}},
		{"several", "Closes #1\n\nAlso fixes #2 and resolves #3", []closeRef{{"", 1}, {"", 2}, {"", 3}}},
		{"repeats collapse", "closes #4, closes #4", []closeRef{{"", 4}}},
		{"bare references do not close", "see #9 for context", nil},
		{"cross-repo carries the path", "closes krz/other#3", []closeRef{{"krz/other", 3}}},
		{"same number in two repos", "closes #3, closes krz/other#3", []closeRef{{"", 3}, {"krz/other", 3}}},
		{"keyword must be its own word", "unclosed #5", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := closingRefs(tc.text)
			slices.SortFunc(got, func(a, b closeRef) int {
				if a.Path != b.Path {
					return strings.Compare(a.Path, b.Path)
				}
				return int(a.N - b.N)
			})
			if !slices.Equal(got, tc.want) {
				t.Errorf("closingRefs(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// A merged merge request's description closes an issue in another
// repository only when the merger holds write there. This drives the
// same target resolution the commit path uses, without needing git.
func TestMRDescriptionClosesAcrossRepos(t *testing.T) {
	f := newOrgFixture(t)
	libIssue, _ := f.st.CreateIssue(f.priv.ID, f.alice, "in priv", "", "md")
	appIssue, _ := f.st.CreateIssue(f.app.ID, f.alice, "in app", "", "md")
	_ = libIssue
	_ = appIssue
	mr := func(n int64, title string) store.MR {
		return store.MR{Number: n, Title: title, Body: ""}
	}
	// carol cannot write acme/priv: the issue stays open and no comment
	// lands.
	ProcessMRDescription(f.st, f.app, mr(1, "Closes acme/priv#1"), f.carol)
	if iss, _ := f.st.IssueByNumber(f.priv.ID, 1); iss.State != "open" {
		t.Fatal("outsider closed a private repo's issue")
	}
	// alice can: it closes with a comment naming the source repository.
	ProcessMRDescription(f.st, f.app, mr(2, "Closes acme/priv#1"), f.alice)
	iss, _ := f.st.IssueByNumber(f.priv.ID, 1)
	if iss.State != "closed" {
		t.Fatal("writer did not close across repos")
	}
	comments, _ := f.st.ListIssueComments(iss.ID)
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "(/alice/app/mrs/2)") {
		t.Fatalf("close comment = %+v", comments)
	}
	// An unknown path is text; a bare #N still acts in the source repo.
	ProcessMRDescription(f.st, f.app, mr(3, "Closes nobody/nothing#1 and closes #1"), f.alice)
	if iss, _ := f.st.IssueByNumber(f.app.ID, 1); iss.State != "closed" {
		t.Fatal("bare #N stopped working")
	}
}
```

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./internal/control/ -run 'ClosingRefs|MRDescriptionCloses' -v`
Expected: build failure, `closeRef` undefined.

- [ ] **Step 3: Change `commitrefs.go`**

Replace the pattern comment and vars:

```go
// closePat matches closing keywords, with an optional owner/name before
// the number for an issue in another repository; refPat matches any bare
// same-repo reference. A cross-repo close acts only when the actor holds
// write on the target (closeTarget); a bare cross-repo reference stays
// display-only.
var (
	closePat = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)[ :]+(?:([a-z0-9][a-z0-9._-]*/[a-z0-9][a-z0-9._-]*))?#(\d+)\b`)
	refPat   = regexp.MustCompile(`(^|[\s([{:])#(\d+)\b`)
)

// closeRef is one closing reference: Path is "" for the same repository.
type closeRef struct {
	Path string
	N    int64
}
```

Replace `closingRefs`:

```go
// closingRefs returns the references a text closes, in no order.
func closingRefs(text string) []closeRef {
	seen := map[closeRef]bool{}
	var out []closeRef
	for _, g := range closePat.FindAllStringSubmatch(text, -1) {
		n, err := strconv.ParseInt(g[2], 10, 64)
		if err != nil {
			continue
		}
		ref := closeRef{Path: strings.ToLower(g[1]), N: n}
		if seen[ref] {
			continue
		}
		seen[ref] = true
		out = append(out, ref)
	}
	return out
}

// closeTarget resolves where a closing reference acts: the source
// repository for a bare #N, or the named repository when the actor holds
// write there. false means the reference stays text; nothing is logged
// above debug, since a refusal must not confirm the target exists.
func closeTarget(st *store.Store, source store.Repo, actorID int64, path string) (store.Repo, bool) {
	if path == "" {
		return source, true
	}
	target, err := st.RepoByPath(path)
	if err != nil {
		return store.Repo{}, false
	}
	actor, err := st.UserByID(actorID)
	if err != nil {
		return store.Repo{}, false
	}
	grant, err := st.AccessRole(target.ID, actorID)
	if err != nil {
		return store.Repo{}, false
	}
	if !policy.CanWrite(actor, target, grant) {
		slog.Debug("commit refs: cross-repo close refused", "source", source.Path(), "target", path)
		return store.Repo{}, false
	}
	return target, true
}
```

Add `"gitbay.org/gitbay/internal/policy"` to the imports.

In `ProcessCommitMessages`, replace the body of the per-message loop:

```go
	for _, m := range msgs {
		closes := closingRefs(m.Message)
		local := map[int64]bool{}
		for _, ref := range closes {
			if ref.Path == "" {
				local[ref.N] = true
			}
		}
		refs := map[int64]bool{}
		for _, g := range refPat.FindAllStringSubmatch(m.Message, -1) {
			if n, err := strconv.ParseInt(g[2], 10, 64); err == nil && !local[n] {
				refs[n] = true
			}
		}
		subject, _, _ := strings.Cut(m.Message, "\n")
		author := authorLink(st, m.AuthorName, m.AuthorEmail)
		for _, ref := range closes {
			target, ok := closeTarget(st, repo, actorID, ref.Path)
			if !ok {
				continue
			}
			actOnIssue(st, repo, target, actorID, m.SHA, ref.N, true, subject, author)
		}
		for n := range refs {
			actOnIssue(st, repo, repo, actorID, m.SHA, n, false, subject, author)
		}
	}
```

In `ProcessMRDescription`, replace the loop:

```go
	for _, ref := range closingRefs(mr.Title + "\n" + mr.Body) {
		target, ok := closeTarget(st, repo, actorID, ref.Path)
		if !ok {
			continue
		}
		issue, err := st.IssueByNumber(target.ID, ref.N)
		if err != nil || issue.State != "open" {
			continue // no such issue, or a commit already closed it
		}
		fresh, err := st.TryRecordCommitRef(issue.ID, mrRefKey(mr.Number))
		if err != nil || !fresh {
			continue // this merge request already acted on this issue
		}
		if err := st.SetIssueState(issue.ID, "closed"); err != nil {
			slog.Error("mr refs: closing issue", "issue", ref.N, "err", err)
			continue
		}
		link := fmt.Sprintf("[!%d](/%s/mrs/%d)", mr.Number, repo.Path(), mr.Number)
		st.AddIssueSystemComment(issue.ID, actorID,
			fmt.Sprintf("closed by merge request %s: %s", link, mr.Title))
		st.RecordEvent(target.ID, actorID, "issue.closed",
			fmt.Sprintf(`{"number":%d,"mr":%d}`, ref.N, mr.Number))
	}
```

`mrRefKey` is per merge request number; a merge request that closes issues in two repositories records `mr-N` against each issue id, which is distinct rows, so the dedup still holds.

Change `actOnIssue` to take `source, target store.Repo`: the issue lookup and the event use `target.ID`; the commit link uses `source.Path()`:

```go
func actOnIssue(st *store.Store, source, target store.Repo, actorID int64, sha string, number int64, close bool, subject, author string) {
	issue, err := st.IssueByNumber(target.ID, number)
	if err != nil {
		return // no such issue: the reference is just text
	}
	fresh, err := st.TryRecordCommitRef(issue.ID, sha)
	if err != nil || !fresh {
		return
	}
	short := sha
	if len(short) > 10 {
		short = short[:10]
	}
	// Informational system entries, not comments from the pusher; the
	// linked sha renders clickable on the web.
	link := fmt.Sprintf("[%s](/%s/commit/%s)", short, source.Path(), sha)
	if close && issue.State == "open" {
		if err := st.SetIssueState(issue.ID, "closed"); err != nil {
			slog.Error("commit refs: closing issue", "issue", number, "err", err)
			return
		}
		st.AddIssueSystemComment(issue.ID, actorID, fmt.Sprintf("closed by commit %s by %s: %s", link, author, subject))
		st.RecordEvent(target.ID, actorID, "issue.closed", fmt.Sprintf(`{"number":%d,"sha":%q}`, number, sha))
		return
	}
	st.AddIssueSystemComment(issue.ID, actorID, fmt.Sprintf("referenced in commit %s by %s: %s", link, author, subject))
}
```

- [ ] **Step 4: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/control/ -run 'ClosingRefs|MRDescriptionCloses|CommitRef' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/control/commitrefs.go internal/control/commitrefs_test.go
git commit -m "control: Closes owner/name#N acts on a repository the actor can write to

Ref #203"
```

---

### Task 8: Web: org pages and the org mark

**Files:**
- Create: `internal/httpd/orglabels.go`
- Create: `internal/web/templates/orglabels.html`
- Create: `internal/web/templates/orgmilestones.html`
- Modify: `internal/httpd/routes.go:58-74` (two GET routes)
- Modify: `internal/web/templates/labels.html`
- Modify: `internal/web/templates/milestones.html`
- Modify: `internal/web/templates/owner.html:3-9`
- Test: `httpd` has no in-process server fixture; the e2e in Task 10 exercises these pages, and this task's check is `go build` plus `go test ./internal/httpd/`, which parses the template set.

**Interfaces:**
- Produces: `GET /{owner}/-/labels` → `s.orgLabels`, `GET /{owner}/-/milestones` → `s.orgMilestones`; 404 for a user owner, an unknown org, or an org the viewer is not a member of with no readable repository.
- Consumes: `control.ReadableOrgRepoIDs`, `colorStyles` from Task 4, `store.ListOrgLabels`, `store.ListOrgMilestones`, `OrgRole`.

- [ ] **Step 1: Routes**

In `internal/httpd/routes.go` after the `/{owner}/activity.atom` route:

```go
		Route{Method: "GET", Pattern: "/{owner}/-/labels", Handler: s.orgLabels},
		Route{Method: "GET", Pattern: "/{owner}/-/milestones", Handler: s.orgMilestones},
```

`-` cannot start a repository name (`policy.namePat`), so these shadow nothing and `TestReservedNames...` needs no change.

- [ ] **Step 2: Handlers**

`internal/httpd/orglabels.go`:

```go
package httpd

import (
	"html/template"
	"net/http"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
)

// orgScope resolves the org for its labels or milestones page. Members
// see it; anyone else only when some repository under the org is
// readable. Everything else is not found, the same answer as for a
// user owner or an unknown name.
func (s *Server) orgScope(w http.ResponseWriter, r *http.Request) (store.Org, store.User, []int64, bool) {
	viewer := s.viewer(r)
	org, err := s.st.OrgByName(r.PathValue("owner"))
	if err != nil {
		s.notFound(w, r)
		return org, viewer, nil, false
	}
	readable, err := control.ReadableOrgRepoIDs(s.st, viewer, org.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return org, viewer, nil, false
	}
	role := ""
	if viewer.ID != 0 {
		role, _ = s.st.OrgRole(org.ID, viewer.ID)
	}
	if role == "" && len(readable) == 0 {
		s.notFound(w, r)
		return org, viewer, nil, false
	}
	return org, viewer, readable, true
}

func (s *Server) orgLabels(w http.ResponseWriter, r *http.Request) {
	org, viewer, readable, ok := s.orgScope(w, r)
	if !ok {
		return
	}
	labels, err := s.st.ListOrgLabels(org.ID, readable)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	stored := make(map[string]string, len(labels))
	for _, l := range labels {
		stored[l.Name] = l.Color
	}
	s.render(w, "orglabels.html", struct {
		basePage
		Org         string
		Labels      []store.Label
		LabelColors map[string]template.CSS
	}{s.baseFor(viewer), org.Name, labels, colorStyles(stored)})
}

func (s *Server) orgMilestones(w http.ResponseWriter, r *http.Request) {
	org, viewer, readable, ok := s.orgScope(w, r)
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	if state != "closed" && state != "all" {
		state = "open"
	}
	ms, err := s.st.ListOrgMilestones(org.ID, state, readable)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type msView struct {
		store.Milestone
		Percent int
	}
	var views []msView
	for _, m := range ms {
		v := msView{Milestone: m}
		if total := m.OpenItems + m.ClosedItems; total > 0 {
			v.Percent = m.ClosedItems * 100 / total
		}
		views = append(views, v)
	}
	s.render(w, "orgmilestones.html", struct {
		basePage
		Org        string
		State      string
		Milestones []msView
	}{s.baseFor(viewer), org.Name, state, views})
}
```

- [ ] **Step 3: Templates**

`internal/web/templates/orglabels.html`:

```html
{{define "title"}}labels · {{.Org}}{{end}}
{{define "content"}}
<h1><a href="/{{.Org}}">{{.Org}}</a> labels</h1>
<p class="meta">Every repository under {{.Org}} sees these beside its own. Managed with <code>gitbay org label set {{.Org}} &lt;label&gt;</code>; counts span the repositories you can read.</p>
{{if .Labels}}<div class="tablewrap"><table class="keys">
<tr class="cols"><th scope="col">label</th><th scope="col">colour</th><th scope="col">issues</th></tr>
{{range .Labels}}<tr>
  <td><span class="chip label" style="{{index $.LabelColors .Name}}">{{.Name}}</span></td>
  <td><span class="mono">{{if .Color}}{{.Color}}{{else}}—{{end}}</span></td>
  <td>{{.Issues}}</td>
</tr>
{{end}}</table></div>
{{else}}<p class="none">No org labels yet.</p>{{end}}
{{end}}
```

`internal/web/templates/orgmilestones.html`:

```html
{{define "title"}}milestones · {{.Org}}{{end}}
{{define "content"}}
<div class="listhead">
  <h1><a href="/{{.Org}}">{{.Org}}</a> milestones</h1>
  <nav class="filters">
    <a {{if eq .State "open"}}class="active" aria-current="page" {{end}}href="?state=open">open</a>
    <a {{if eq .State "closed"}}class="active" aria-current="page" {{end}}href="?state=closed">closed</a>
    <a {{if eq .State "all"}}class="active" aria-current="page" {{end}}href="?state=all">all</a>
  </nav>
</div>
<p class="meta">Progress spans the repositories under {{.Org}} you can read.</p>
<ul class="milestonelist">
{{range .Milestones}}<li>
  <div class="msmain">
    <p class="title">{{.Title}} <span class="chip {{if eq .State "open"}}chip-open{{else}}chip-done{{end}}">{{.State}}</span></p>
    {{if .Description}}<p class="desc">{{.Description}}</p>{{end}}
    <p class="meta">{{if .DueDate}}due {{.DueDate}} · {{end}}{{.ClosedItems}} closed, {{.OpenItems}} open · {{.Percent}}%</p>
    <div class="progress"><div class="bar" style="width: {{.Percent}}%"></div></div>
  </div>
</li>
{{else}}<li class="empty">no {{if ne .State "all"}}{{.State}} {{end}}org milestones — create one with <code>gitbay org milestone create {{.Org}} "v1.0"</code></li>{{end}}
</ul>
{{end}}
```

In `labels.html`, mark org rows and drop their forms. Replace the `{{range .Labels}}<tr>` row with:

```html
{{range .Labels}}<tr>
  <td><a class="chip label" style="{{index $.LabelColors .Name}}" href="/{{$.Repo.OwnerName}}/{{$.Repo.Name}}/issues?label={{.Name}}">{{.Name}}</a>{{if .Org}} <span class="chip chip-neutral">org</span>{{end}}</td>
  <td>{{if and $.CanWrite (not .Org)}}<form method="post" action="/{{$.Repo.OwnerName}}/{{$.Repo.Name}}/labels" class="inline">
    <input type="hidden" name="name" value="{{.Name}}">
    <input type="text" name="color" value="{{.Color}}" aria-label="Colour for {{.Name}}" placeholder="rrggbb" size="8">
    <button type="submit" class="btn">Save</button>
  </form>{{else}}<span class="mono">{{if .Color}}{{.Color}}{{else}}—{{end}}</span>{{end}}</td>
  <td>{{.Issues}}</td>
  <td class="act">{{if and $.CanWrite (not .Org)}}<form method="post" action="/{{$.Repo.OwnerName}}/{{$.Repo.Name}}/labels" class="inline">
    <input type="hidden" name="action" value="remove">
    <input type="hidden" name="name" value="{{.Name}}">
    <button type="submit" class="linklike">Remove</button>
  </form>{{else if .Org}}<a href="/{{$.Repo.OwnerName}}/-/labels">org</a>{{end}}</td>
</tr>
```

In `milestones.html`, in the `<p class="title">` line, after the state chip add `{{if .OrgID}} <span class="chip chip-neutral">org</span>{{end}}`.

In `owner.html`, after the `{{if .Members}}...{{end}}` line inside `profilehead`:

```html
{{if eq .Kind "org"}}<p class="meta"><a href="/{{.Owner}}/-/labels">labels</a> · <a href="/{{.Owner}}/-/milestones">milestones</a></p>{{end}}
```

- [ ] **Step 4: Build and run the httpd tests**

Run: `go build ./... && go test ./internal/httpd/`
Expected: PASS. The template set parses at start-up, so a syntax error surfaces here.

- [ ] **Step 5: Commit**

```bash
git add internal/httpd/orglabels.go internal/httpd/routes.go internal/web/templates/orglabels.html internal/web/templates/orgmilestones.html internal/web/templates/labels.html internal/web/templates/milestones.html internal/web/templates/owner.html
git commit -m "web: org label and milestone pages under /{org}/-/, org mark on repository pages

Ref #203"
```

---

### Task 9: Docs

**Files:**
- Modify: `.gitbay/wiki/Users.org:350-360` (after the milestones block) and the commit-references paragraph ending "Same repository only." (near line 340)
- Modify: `.gitbay/wiki/Parity.org:99-125`

- [ ] **Step 1: Users**

Replace the sentence `Same repository only.` in the commit-references paragraph with:

```
=Closes owner/name#N= closes an issue in another repository when
you hold write there; otherwise it stays a plain link. A bare
=owner/name#N= links and does nothing.
```

After the milestones `#+end_src` block add:

```
An org holds labels and milestones every repository under it sees
beside its own. =issue label --add=, =issue milestone= and =mr
milestone= resolve the org's row first; a repository cannot create a
label or milestone with a name its org holds. Creating an org label or
milestone whose name repositories under the org already use folds them
in: their issues and merge requests move to the org's row. Org admins
manage them; counts span the repositories you can read.

#+begin_src sh
gitbay org label set acme bug --color cf222e
gitbay org label list acme / remove acme bug
gitbay org milestone create acme v2 --due 2027-03-01
gitbay org milestone list acme [--state open|closed|all]
gitbay org milestone close acme v2 / reopen acme v2
#+end_src

On the web: =/acme/-/labels= and =/acme/-/milestones=, read-only.
```

- [ ] **Step 2: Parity**

After the `| milestone create, close, reopen | yes | no | yes |` row add:

```
| org labels: set, list, remove | yes | list | no |
| org milestones: create, list, close, reopen | yes | list | no |
| closes across repositories | yes | yes | yes |
```

("list" in the web column means the read page only.) After the paragraph that starts `Labels are created on the fly` add:

```
Org labels and milestones are managed on the CLI and API only;
=/<org>/-/labels= and =/<org>/-/milestones= show them. The repository
label page's form exists for colour alone, and three org forms nobody
asked for were not worth their handlers.
```

- [ ] **Step 3: Commit**

```bash
git add .gitbay/wiki/Users.org .gitbay/wiki/Parity.org
git commit -m "wiki: org labels, milestones and cross-repository closes

Ref #203"
```

---

### Task 10: End-to-end test

**Files:**
- Create: `e2e/orglabels_test.go`

**Interfaces:**
- Consumes: the harness in `e2e/ssh_test.go` (`startInstance`, `inst.newKey`, `inst.admin`, `inst.ssh`, `inst.get`, `inst.gitEnv`, `inst.sshURL`, `mustGit`).

- [ ] **Step 1: Write the test**

```go
package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// An org's labels and milestones reach every repository under it; a
// commit in one repository closes an issue in another; the org pages
// answer members and outsiders as their access allows.
func TestOrgLabelsMilestonesAndCrossRepoCloses(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	carolKey := inst.newKey(t, "carol")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "carol", "--key", carolKey+".pub", "--email", "carol@example.test", "--verified")
	must := func(key string, args ...string) string {
		t.Helper()
		out, errOut, code := inst.ssh(t, key, "", args...)
		if code != 0 {
			t.Fatalf("%v: exit %d %s", args, code, errOut)
		}
		return out
	}
	must(aliceKey, "org", "create", "acme")
	must(aliceKey, "repo", "create", "acme/lib")
	must(aliceKey, "repo", "create", "acme/widget", "--private")
	must(aliceKey, "issue", "create", "acme/lib", "--title", "'lib one'")
	must(aliceKey, "issue", "create", "acme/widget", "--title", "'widget one'")

	// Repo labels in both, then the org set folds them in.
	must(aliceKey, "issue", "label", "acme/lib", "1", "--add", "bug")
	must(aliceKey, "issue", "label", "acme/widget", "1", "--add", "bug")
	out := must(aliceKey, "org", "label", "set", "acme", "bug", "--color", "ff0000", "--json")
	if !strings.Contains(out, `"folded":2`) {
		t.Fatalf("org label set: %s", out)
	}
	out = must(aliceKey, "label", "list", "acme/lib", "--json")
	if !strings.Contains(out, `"org":true`) || !strings.Contains(out, `"issues":2`) {
		t.Fatalf("lib label list: %s", out)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "label", "set", "acme/lib", "bug"); code == 0 || !strings.Contains(errOut, "org label set acme bug") {
		t.Fatalf("repo label set over org name: exit %d %s", code, errOut)
	}

	// An org milestone attaches from both repositories and counts across.
	must(aliceKey, "org", "milestone", "create", "acme", "v1", "--due", "2027-01-01")
	must(aliceKey, "issue", "milestone", "acme/lib", "1", "v1")
	must(aliceKey, "issue", "milestone", "acme/widget", "1", "v1")
	out = must(aliceKey, "org", "milestone", "list", "acme", "--json")
	if !strings.Contains(out, `"open":2`) {
		t.Fatalf("org milestone list: %s", out)
	}
	out = must(aliceKey, "issue", "list", "acme/lib", "--milestone", "v1", "--json")
	if !strings.Contains(out, `"number":1`) {
		t.Fatalf("issue list filtered by org milestone: %s", out)
	}

	// A push to acme/lib closes acme/widget#1 and leaves a comment there.
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("acme/lib"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "fix the widget\n\nCloses acme/widget#1")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	out = must(aliceKey, "issue", "show", "acme/widget", "1", "--json")
	if !strings.Contains(out, `"state":"closed"`) || !strings.Contains(out, "](/acme/lib/commit/") {
		t.Fatalf("widget#1 after cross-repo close: %s", out)
	}
	out = must(aliceKey, "org", "milestone", "list", "acme", "--json")
	if !strings.Contains(out, `"open":1`) || !strings.Contains(out, `"closed":1`) {
		t.Fatalf("org milestone progress after close: %s", out)
	}

	// carol is outside: she reads the org pages because acme/lib is public,
	// and the counts stop at it.
	out = must(carolKey, "org", "milestone", "list", "acme", "--json")
	if !strings.Contains(out, `"open":1`) || !strings.Contains(out, `"closed":0`) {
		t.Fatalf("outsider progress: %s", out)
	}
	if status, body := inst.get(t, "/acme/-/labels"); status != 200 || !strings.Contains(body, ">bug<") {
		t.Fatalf("org labels page: %d", status)
	}
	if status, body := inst.get(t, "/acme/-/milestones"); status != 200 || !strings.Contains(body, "v1") || !strings.Contains(body, "1 closed, 1 open") {
		t.Fatalf("org milestones page: %d\n%s", status, body)
	}
	if status, body := inst.get(t, "/acme/lib/labels"); status != 200 || !strings.Contains(body, `chip-neutral">org<`) {
		t.Fatalf("repo labels page lacks the org mark: %d", status)
	}
	// carol cannot close into the private repo from a repo she owns.
	must(carolKey, "repo", "create", "carol/own")
	must(aliceKey, "issue", "create", "acme/widget", "--title", "'widget two'")
	cwork := t.TempDir()
	cenv := inst.gitEnv(carolKey)
	mustGit(t, cwork, cenv, "clone", inst.sshURL("carol/own"), "w")
	cdir := filepath.Join(cwork, "w")
	os.WriteFile(filepath.Join(cdir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, cdir, cenv, "checkout", "-q", "-b", "main")
	mustGit(t, cdir, cenv, "add", ".")
	mustGit(t, cdir, cenv, "commit", "-q", "-m", "sneaky\n\nCloses acme/widget#2")
	mustGit(t, cdir, cenv, "push", "-q", "origin", "main")
	out = must(aliceKey, "issue", "show", "acme/widget", "2", "--json")
	if !strings.Contains(out, `"state":"open"`) || strings.Contains(out, "sneaky") {
		t.Fatalf("outsider acted on a private repo's issue: %s", out)
	}
	// With the public repo gone private, the org pages are not found for
	// an anonymous reader.
	must(aliceKey, "repo", "settings", "visibility", "acme/lib", "private")
	if status, _ := inst.get(t, "/acme/-/labels"); status != 404 {
		t.Fatalf("private org labels page for anonymous: %d", status)
	}
}
```

- [ ] **Step 2: Run it**

Run: `go test ./e2e -run TestOrgLabelsMilestonesAndCrossRepoCloses -v`
Expected: PASS. It needs real `git`, `ssh` and `sshd`, as every e2e test does. Fix whatever it finds in the earlier tasks; adjust JSON field assertions to the actual output rather than loosening them.

- [ ] **Step 3: Commit**

```bash
git add e2e/orglabels_test.go
git commit -m "e2e: org labels, milestones and a cross-repository close

Closes #203"
```

---

### Task 11: Merge request

- [ ] **Step 1: Rebase and push**

```bash
git fetch -q origin && git rebase origin/main && git push -u origin org-scope
```

- [ ] **Step 2: Open the MR**

```bash
gitbay mr create --source org-scope --target main --title "Org labels, milestones and cross-repository closes" --file - <<'EOF'
Migration 0052 scopes `labels` and `milestones` to a repository or an org. Every repository under an org sees the org's rows beside its own; `org label set|list|remove` and `org milestone create|list|close|reopen` manage them, folding in same-named repository rows on create. `Closes owner/name#N` in a commit on the default branch or a merged merge request closes that issue when the actor holds write there. Read pages at `/{org}/-/labels` and `/{org}/-/milestones`.

Spec: docs/specs/2026-09-11-org-labels-milestones-closes-design.md

Closes #203
EOF
```

- [ ] **Step 3: CI, then merge**

Wait for the `build` and `test` jobs on bay1. Then `gitbay mr merge <n> --strategy ff` and delete the branch locally and on the forge. The CHANGELOG entry is written at release time under the next minor version, as v1.18.1's was.
