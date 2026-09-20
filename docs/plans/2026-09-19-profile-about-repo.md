# Profile about in a repository — implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move the user/org profile about text out of the `users`/`orgs`
columns and into `profile/README.{md,org}` on the default branch of a
repository named `.gitbay` under the owner's namespace.

**Architecture:** The about becomes a file, read the way wiki pages are
read — no store rows, access derived from the parent repository. The
control command's JSON keeps its `about`/`about_format` fields, so the
API and the iOS client do not move. Writes stop going through
`profile set`; the file is written by a push or `repo commit-file`. A
SQL migration parks the existing text in a holding table and drops the
columns; a `gitbayd admin` one-shot drains the table into repositories.

**Tech Stack:** Go, SQLite (modernc.org/sqlite), `git` subprocesses via
`internal/gitutil`, `html/template`.

**Spec:** `docs/specs/2026-09-19-profile-about-repo-design.md`

## Global Constraints

- Repository name `.gitbay`; file `profile/README` plus one of `.md`,
  `.org`, `.markdown`, resolved in that order.
- `ProfileOut.About` and `ProfileOut.AboutFormat` keep their JSON names.
  `AboutFormat` is `org` for a `.org` file, `md` otherwise.
- A repository the caller cannot read yields an empty about, never an
  error — the private-repo rule is 404-shaped, and a profile must not
  confirm a namespace.
- Blob reads are capped at `maxCommitFileBytes` (1MB), already defined
  in `internal/control/commitfile.go`.
- Never attribute anything to an assistant or model, anywhere.
- Commit messages reference the issue: `Ref #236`, and the last one
  `Closes #236`.
- Branch is `profile-about-236`; never push to `main`.

---

### Task 1: Repository names may start with a dot

`.gitbay` is an invalid repository name today: `namePat` requires a
leading alphanumeric. Relax it, and close the hole that lets a
repository be named exactly `.git`.

**Files:**
- Modify: `internal/policy/names.go:37` (namePat), `:52-69` (ValidateName)
- Test: `internal/policy/names_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `policy.ValidateName(name string) error` accepts a single
  leading dot. Task 6 relies on `.gitbay` validating.

- [ ] **Step 1: Write the failing test**

Append to `internal/policy/names_test.go`:

```go
func TestValidateNameLeadingDot(t *testing.T) {
	for _, name := range []string{".gitbay", ".dotfiles", ".a"} {
		if err := ValidateName(name); err != nil {
			t.Errorf("ValidateName(%q) = %v, want nil", name, err)
		}
	}
	for _, name := range []string{".", "..", ".git", "repo.git", "..a", ".-a"} {
		if err := ValidateName(name); err == nil {
			t.Errorf("ValidateName(%q) = nil, want error", name)
		}
	}
	// The ceiling is 63 characters, dot included.
	if err := ValidateName("." + strings.Repeat("a", 62)); err != nil {
		t.Errorf("63-character dotted name rejected: %v", err)
	}
	if err := ValidateName("." + strings.Repeat("a", 63)); err == nil {
		t.Error("64-character dotted name accepted")
	}
}
```

Add `"strings"` to that file's imports if it is not already there.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/policy/ -run TestValidateNameLeadingDot -v`
Expected: FAIL — `ValidateName(".gitbay")` returns an invalid-name error.

- [ ] **Step 3: Relax the pattern**

In `internal/policy/names.go`, replace the `namePat` declaration and its
comment:

```go
// namePat matches valid user, org, and repo names: lowercase alphanumerics,
// dot, dash, underscore; must start with an alphanumeric, or with a single
// dot before one. A leading dot marks a repository as infrastructure rather
// than a project — .gitbay holds an owner's profile content. Dots are
// further restricted by ValidateName to avoid "." / ".." and ".git".
var namePat = regexp.MustCompile(`^\.?[a-z0-9][a-z0-9._-]{0,61}$`)
```

- [ ] **Step 4: Refuse `.git` exactly, not just as a suffix**

In `ValidateName`, replace the suffix check:

```go
	if len(name) > 4 && name[len(name)-4:] == ".git" {
		return fmt.Errorf("invalid name %q: must not end in .git", name)
	}
```

with:

```go
	// A name of exactly ".git" is now reachable through the leading-dot
	// rule, and a bare .git directory in the namespace is not a thing to
	// allow; HasSuffix covers both it and "repo.git".
	if strings.HasSuffix(name, ".git") {
		return fmt.Errorf("invalid name %q: must not end in .git", name)
	}
```

`strings` is already imported there.

- [ ] **Step 5: Run the package's tests**

Run: `go test ./internal/policy/`
Expected: PASS, including the pre-existing `TestValidateName`.

- [ ] **Step 6: Commit**

```bash
git add internal/policy/names.go internal/policy/names_test.go
git commit -m "policy: a repository name may start with a dot

Ref #236"
```

---

### Task 2: A first commit into an empty repository

`CommitFileChange` resolves the branch and fails when it does not exist,
so committing the first file into a freshly created `.gitbay` is
impossible. Task 4's web button and Task 6's backfill both need it.

Allow a root commit **only when the repository has no refs at all**, so
that a typo'd branch name in a repository with history still fails the
way it does today rather than silently starting an orphan branch.

**Files:**
- Modify: `internal/gitutil/merge.go:188-239` (CommitFileChange)
- Test: `internal/gitutil/merge_test.go` (create if absent)

**Interfaces:**
- Consumes: `gitutil.ResolveRef(dir, ref) (string, error)`,
  `gitutil.CommitTree(dir, tree string, parents []string, name, email, message string) (string, error)`,
  `gitutil.UpdateRefCAS(dir, ref, newSHA, oldSHA string) error`.
- Produces: `gitutil.CommitFileChange(dir, branch, path string, content []byte, name, email, message string) (string, error)`
  — unchanged signature, now succeeding on an empty repository.

- [ ] **Step 1: Write the failing test**

Create or append to `internal/gitutil/merge_test.go`:

```go
func TestCommitFileChangeEmptyRepo(t *testing.T) {
	dir := t.TempDir()
	if err := InitBare(dir, "main", ""); err != nil {
		t.Fatal(err)
	}
	sha, err := CommitFileChange(dir, "main", "profile/README.md",
		[]byte("# hello\n"), "alice", "alice@example.org", "add about")
	if err != nil {
		t.Fatalf("first commit into an empty repository: %v", err)
	}
	if sha == "" {
		t.Fatal("no sha returned")
	}
	raw, err := ReadBlob(dir, "main", "profile/README.md", 1<<20)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if string(raw) != "# hello\n" {
		t.Errorf("read back %q", raw)
	}
	// A second commit still takes the normal parented path.
	if _, err := CommitFileChange(dir, "main", "profile/README.md",
		[]byte("# hello again\n"), "alice", "alice@example.org", "edit"); err != nil {
		t.Fatalf("second commit: %v", err)
	}
	// A branch that does not exist in a repository that has history is
	// still an error, not a new orphan branch.
	if _, err := CommitFileChange(dir, "nope", "x.md",
		[]byte("x"), "alice", "alice@example.org", "x"); err == nil {
		t.Error("committing to an unknown branch of a non-empty repository succeeded")
	}
}
```

Check `InitBare`'s signature in `internal/gitutil` before running; if
its third parameter is not an optional hooks directory, pass what the
existing callers in `internal/control/repo.go:217` pass.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./internal/gitutil/ -run TestCommitFileChangeEmptyRepo -v`
Expected: FAIL — `branch main: unknown ref "refs/heads/main"`.

- [ ] **Step 3: Add the unborn-branch path**

In `internal/gitutil/merge.go`, replace the opening of
`CommitFileChange`:

```go
	branchRef := "refs/heads/" + branch
	parent, err := ResolveRef(dir, branchRef)
	if err != nil {
		return "", fmt.Errorf("branch %s: %w", branch, err)
	}
```

with:

```go
	branchRef := "refs/heads/" + branch
	parent, err := ResolveRef(dir, branchRef)
	if err != nil {
		// An unborn branch is only a root commit in a repository with no
		// refs at all. Anywhere else an unresolvable branch is a typo, and
		// starting an orphan branch for it would be worse than refusing.
		if !isEmptyRepo(dir) {
			return "", fmt.Errorf("branch %s: %w", branch, err)
		}
		parent = ""
	}
```

Replace the `read-tree` block:

```go
	rt := exec.Command(toolpath.Look("git"), "-C", dir, "read-tree", parent+"^{tree}")
	rt.Env = env
	if out, err := rt.CombinedOutput(); err != nil {
		return "", fmt.Errorf("read-tree: %v\n%s", err, out)
	}
```

with:

```go
	arg := parent + "^{tree}"
	if parent == "" {
		arg = "--empty"
	}
	rt := exec.Command(toolpath.Look("git"), "-C", dir, "read-tree", arg)
	rt.Env = env
	if out, err := rt.CombinedOutput(); err != nil {
		return "", fmt.Errorf("read-tree: %v\n%s", err, out)
	}
```

Replace the `CommitTree` call:

```go
	sha, err := CommitTree(dir, tree, []string{parent}, name, email, message)
```

with:

```go
	var parents []string
	if parent != "" {
		parents = []string{parent}
	}
	sha, err := CommitTree(dir, tree, parents, name, email, message)
```

`UpdateRefCAS` already omits the old value when `parent` is empty, so
the tail of the function is unchanged.

- [ ] **Step 4: Add the emptiness check**

Add below `CommitFileChange` in the same file:

```go
// isEmptyRepo reports whether dir has no refs at all — a repository
// created but never pushed to.
func isEmptyRepo(dir string) bool {
	out, err := exec.Command(toolpath.Look("git"), "-C", dir, "rev-list", "-n1", "--all").Output()
	return err == nil && strings.TrimSpace(string(out)) == ""
}
```

- [ ] **Step 5: Run the tests**

Run: `go test ./internal/gitutil/`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/gitutil/merge.go internal/gitutil/merge_test.go
git commit -m "gitutil: commit-file writes the first commit of an empty repository

Ref #236"
```

---

### Task 3: Read the about from the repository

**Files:**
- Modify: `internal/control/profile.go` (constants, `ownerAbout`, `ProfileOut`, `runProfileShow`)
- Modify: `internal/httpd/web.go` (`aboutHTML`, the profile handler at ~446)
- Test: `e2e/profileabout_test.go` (create)

**Interfaces:**
- Consumes: `control.RepoDir(root, owner, name) string`,
  `gitutil.ReadBlob(dir, ref, path string, limit int64) ([]byte, error)`,
  `policy.CanRead(u store.User, r store.Repo, grant string) bool`,
  `c.Store.RepoByPath(path) (store.Repo, error)`,
  `c.Store.AccessRole(repoID, userID int64) (string, error)`,
  `maxCommitFileBytes` from `internal/control/commitfile.go`.
- Produces:
  - `const ProfileRepoName = ".gitbay"` and `const AboutBase = "profile/README"` in `internal/control/profile.go` — Task 4, 5 and 6 use them.
  - `func ownerAbout(c *Ctx, owner string) (text, format, path string)`.
  - `ProfileOut.AboutPath string \`json:"about_path,omitempty"\`` — Task 4's template links to it.
  - `func aboutHTML(text, format string) template.HTML` in `internal/httpd/web.go`.

- [ ] **Step 1: Write the failing e2e test**

Create `e2e/profileabout_test.go`:

```go
package e2e

import (
	"strings"
	"testing"
)

// The about text is a file in <owner>/.gitbay, read on every surface
// with the reader's own access.
func TestProfileAboutFromRepo(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/.gitbay"); code != 0 {
		t.Fatal("creating alice/.gitbay failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "# alice\n\nhello from a file\n",
		"repo", "commit-file", "alice/.gitbay", "profile/README.md",
		"--ref", "main", "--file", "-"); code != 0 {
		t.Fatal("committing the about failed")
	}

	out, _, code := inst.ssh(t, bobKey, "", "profile", "show", "alice", "--json")
	if code != 0 {
		t.Fatalf("profile show: %d", code)
	}
	if !strings.Contains(out, "hello from a file") {
		t.Errorf("about not read from the repository: %s", out)
	}
	if !strings.Contains(out, `"about_format":"md"`) {
		t.Errorf("about_format not md: %s", out)
	}
	if !strings.Contains(out, `"about_path":"profile/README.md"`) {
		t.Errorf("about_path missing: %s", out)
	}

	_, body := inst.get(t, "/alice")
	if !strings.Contains(body, "hello from a file") {
		t.Error("web profile does not render the about")
	}
}

// .org wins nothing over .md, and a private .gitbay keeps the about to
// the people who can read it.
func TestProfileAboutFormatAndPrivacy(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	inst.ssh(t, aliceKey, "", "repo", "create", "alice/.gitbay", "--private")
	inst.ssh(t, aliceKey, "* heading\n\norg text here\n",
		"repo", "commit-file", "alice/.gitbay", "profile/README.org",
		"--ref", "main", "--file", "-")

	out, _, _ := inst.ssh(t, aliceKey, "", "profile", "show", "alice", "--json")
	if !strings.Contains(out, "org text here") || !strings.Contains(out, `"about_format":"org"`) {
		t.Errorf("owner cannot read their own private about: %s", out)
	}
	out, _, code := inst.ssh(t, bobKey, "", "profile", "show", "alice", "--json")
	if code != 0 {
		t.Fatalf("profile show for an outsider should succeed: %d", code)
	}
	if strings.Contains(out, "org text here") {
		t.Errorf("private about leaked to an outsider: %s", out)
	}

	// A .md beside the .org wins: it is first in the resolution order.
	inst.ssh(t, aliceKey, "markdown wins\n",
		"repo", "commit-file", "alice/.gitbay", "profile/README.md",
		"--ref", "main", "--file", "-")
	out, _, _ = inst.ssh(t, aliceKey, "", "profile", "show", "alice", "--json")
	if !strings.Contains(out, "markdown wins") {
		t.Errorf(".md did not win resolution: %s", out)
	}
}
```

- [ ] **Step 2: Run them and watch them fail**

Run: `go test ./e2e/ -run 'TestProfileAbout' -v -timeout 10m`
Expected: FAIL — `repo create alice/.gitbay` succeeds after Task 1, but
`profile show` reports no about, and `about_path` is absent.

- [ ] **Step 3: Add the resolution helper**

In `internal/control/profile.go`, after the `maxProfileLinks` constant:

```go
// ProfileRepoName is the repository that holds an owner's profile
// content. A dot-repo because it is infrastructure rather than a
// project: later per-owner configuration goes beside the about text,
// and the leading dot keeps it out of listings.
const ProfileRepoName = ".gitbay"

// AboutBase is the about file's path in that repository, without its
// extension.
const AboutBase = "profile/README"

// aboutExts are the formats the about is read from, in resolution
// order — the wiki's order, for the same reason.
var aboutExts = []string{".md", ".org", ".markdown"}

// ownerAbout reads an owner's about text from <owner>/.gitbay. Anything
// missing — the repository, the branch, the file — is an empty about,
// and so is a repository this caller cannot read: a profile must not
// confirm a private namespace. path is the file it came from, so a
// client can link to it.
func ownerAbout(c *Ctx, owner string) (text, format, path string) {
	repo, err := c.Store.RepoByPath(owner + "/" + ProfileRepoName)
	if err != nil {
		return "", "", ""
	}
	grant, err := c.Store.AccessRole(repo.ID, c.User.ID)
	if err != nil || !policy.CanRead(c.User, repo, grant) {
		return "", "", ""
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	for _, ext := range aboutExts {
		raw, err := gitutil.ReadBlob(dir, repo.DefaultBranch, AboutBase+ext, maxCommitFileBytes)
		if err != nil || len(raw) == 0 {
			continue
		}
		f := "md"
		if ext == ".org" {
			f = "org"
		}
		return string(raw), f, AboutBase + ext
	}
	return "", "", ""
}
```

- [ ] **Step 4: Add `AboutPath` and read through the helper**

In `ProfileOut`, below `AboutFormat`:

```go
	// AboutPath is where the about was read from in <owner>/.gitbay, so a
	// client can link to the file rather than guess its extension.
	AboutPath string `json:"about_path,omitempty"`
```

In `runProfileShow`, replace the `d := ProfileOut{...}` literal:

```go
	about, aboutFormat, aboutPath := ownerAbout(c, name)
	d := ProfileOut{Name: name, Kind: kind, Description: p.Description, Website: p.Website,
		About: about, AboutFormat: aboutFormat, AboutPath: aboutPath,
		Links: p.Links, Repos: []ProfileRepo{}}
```

- [ ] **Step 5: Render from the text, not from a store struct**

In `internal/httpd/web.go`, replace `aboutHTML`:

```go
// aboutHTML renders a profile's about text. The format comes from the
// file it was read from: org is org, anything else markdown.
func aboutHTML(text, format string) template.HTML {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	name := "about.md"
	if format == "org" {
		name = "about.org"
	}
	return renderReadme(name, []byte(text))
}
```

In the profile handler near line 446, drop the about from the
`store.Profile` literal:

```go
	profile := store.Profile{Description: d.Description, Website: d.Website, Links: d.Links}
```

and update the `AboutHTML` field's value in the `s.render` struct
literal to `aboutHTML(d.About, d.AboutFormat)`. Find its current call
with `grep -n 'aboutHTML' internal/httpd/web.go` and change that
argument list.

- [ ] **Step 6: Build, vet, and run the tests**

Run: `go build ./... && go vet ./... && go test ./e2e/ -run 'TestProfileAbout' -v -timeout 10m`
Expected: PASS. `go vet` matters here — `aboutHTML`'s signature changed
and `go build` does not compile `_test.go` callers.

- [ ] **Step 7: Commit**

```bash
git add internal/control/profile.go internal/httpd/web.go e2e/profileabout_test.go
git commit -m "profile: read the about text from <owner>/.gitbay

Ref #236"
```

---

### Task 4: Stop writing the about through `profile set`

There is no about-specific write command, for the reason the wiki has
none: the content is a file, written the way files are written.

**Files:**
- Modify: `internal/control/profile.go` (`register` usages, `profileEdit`, `parseProfileFlags`, `applyProfile`, `runProfileSet`, `runOrgProfile`)
- Modify: `internal/httpd/account.go:42-87` (page struct), `:251-266` (the `profile` form case)
- Modify: `internal/web/templates/account.html:25,34-36`
- Modify: `internal/httpd/routes.go` if a new form case needs no route (it does not — `/settings` already takes the POST)
- Test: `e2e/profileabout_test.go` (append)

**Interfaces:**
- Consumes: `control.ProfileRepoName`, `control.AboutBase` from Task 3;
  `ProfileOut.AboutPath` from Task 3;
  `s.runControl(u store.User, argv []string) (int, string, bool)` and
  `s.runControlStdin(u store.User, argv []string, stdin string) (string, bool)`
  in `internal/httpd` — confirm their exact signatures with
  `grep -n 'func (s \*Server) runControl' internal/httpd/*.go` before use.
- Produces: `profile set` and `org profile` with no `--about`,
  `--about-format` or `--file`, and `ReadsStdin` unset.

- [ ] **Step 1: Write the failing test**

Append to `e2e/profileabout_test.go`:

```go
// The about is not settable through profile set any more: it is a file.
func TestProfileSetHasNoAbout(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	_, _, code := inst.ssh(t, aliceKey, "", "profile", "set", "--about", "'inline text'")
	if code == 0 {
		t.Error("profile set --about still accepted")
	}
	// The flags that stay still work.
	if _, _, code := inst.ssh(t, aliceKey, "",
		"profile", "set", "--description", "'a line'", "--link", "'site|https://example.org'"); code != 0 {
		t.Fatalf("profile set --description --link: %d", code)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "profile", "show", "alice", "--json")
	if !strings.Contains(out, "a line") || !strings.Contains(out, "https://example.org") {
		t.Errorf("description or link not saved: %s", out)
	}
}
```

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./e2e/ -run TestProfileSetHasNoAbout -v -timeout 10m`
Expected: FAIL — `profile set --about` exits 0.

- [ ] **Step 3: Drop the flags from the command registrations**

In `internal/control/profile.go`'s `init`, replace the two
registrations:

```go
	register(Command{Path: []string{"profile", "set"},
		Summary: "set your profile",
		Usage:   "profile set [--description <d>] [--website <url>] [--link <label|url>]... ('' clears)", Run: runProfileSet})
	register(Command{Path: []string{"org", "profile"},
		Summary: "show or set an org's profile",
		Usage:   "org profile <org> [--description <d>] [--website <url>] [--link <label|url>]...", Run: runOrgProfile})
```

`ReadsStdin` is gone from both; `TestStdinCommandsReadStdin` enforces
that a command that no longer reads stdin does not claim to.

- [ ] **Step 4: Drop the fields from the edit struct and the parser**

In `profileEdit`, delete the `About` and `AboutFormat` fields. Update
`empty()`:

```go
func (e profileEdit) empty() bool {
	return e.Description == nil && e.Website == nil && e.Links == nil
}
```

In `parseProfileFlags`, delete the `about`, `file` and `sawAbout`
locals, the `--about` / `--about-format` / `--file` entries from
`flagSpec.Values`, the `--about-format` case from the loop over the
value flags, the two `if f.Has(...)` blocks that set them, and the
`if sawAbout { ... bodyFrom ... }` block. The signature keeps its
`*Ctx` parameter — `parseFlags` errors still flow through `c` in the
callers — but if the compiler reports `c` unused, rename it to `_` in
the parameter list and update both call sites.

In `applyProfile`, delete the `e.About` and `e.AboutFormat` blocks.

In `runProfileSet`, change the "nothing to set" message:

```go
		return c.fail(protocol.ExitUsage, "nothing to set: pass --description, --website and/or --link")
```

In both `runProfileSet` and `runOrgProfile`, drop `About:` and
`AboutFormat:` from the `ProfileOut` literals they emit.

- [ ] **Step 5: Build and fix what falls out**

Run: `go build ./... && go vet ./...`
Expected: errors in `internal/httpd/account.go` and possibly
`internal/control/migrate.go`. `internal/control/migrate.go` uses
`store.Profile` as a whole and needs no change until Task 6. Fix only
`account.go` here, per Step 6.

- [ ] **Step 6: Point the account page at the file**

In `internal/httpd/account.go`, in the `profile` case of the settings
form handler, replace the whole case body:

```go
	case "profile":
		argv := []string{"profile", "set",
			"--description", r.FormValue("description"),
			"--website", r.FormValue("website"),
		}
		for _, link := range profileLinkArgs(r.FormValue("links")) {
			argv = append(argv, "--link", link)
		}
		if _, msg, ok := s.runControl(u, argv); !ok {
			back(msg, "")
			return
		}
		back("", "profile updated")
	case "profile-repo":
		// The about text is a file. Create the repository that holds it and
		// commit a starter README, so the file editor has a branch to open.
		path := u.Username + "/" + control.ProfileRepoName
		if _, msg, ok := s.runControl(u, []string{"repo", "create", path}); !ok {
			back(msg, "")
			return
		}
		starter := "# " + u.Username + "\n\nThis is your profile's about text.\n"
		if msg, ok := s.runControlStdin(u,
			[]string{"repo", "commit-file", path, control.AboutBase + ".md",
				"--ref", "main", "--message", "add profile about", "--file", "-"}, starter); !ok {
			back(msg, "")
			return
		}
		back("", "profile repository created")
```

`runControl`'s return shape is `(code int, msg string, ok bool)` in the
`theme` case above — match it exactly. In `accountPage`, add two fields to the anonymous page struct after
`LinksText`:

```go
		AboutRepo    string // "<user>/.gitbay", the repository that holds the about
		AboutEdit    string // the file editor's URL, empty when the repository has no about yet
```

and compute them before `s.render`:

```go
	aboutRepo := u.Username + "/" + control.ProfileRepoName
	aboutEdit := ""
	if profile.AboutPath != "" {
		aboutEdit = "/" + aboutRepo + "/edit/main/" + profile.AboutPath
	}
```

then add `aboutRepo, aboutEdit` to the struct literal's value list in
the same position as the fields.

- [ ] **Step 7: Replace the textarea with the pointer**

In `internal/web/templates/account.html`, delete line 25
(`{{if .Draft.Is "about"}}...{{end}}`), the About `<label>` and
`<textarea>`, and the `formatpicker` line. Replace the button group's
`{{template "previewbtn"}}` with nothing, leaving:

```html
  <span class="btngroup"><button type="submit" class="btn">Save profile</button></span>
</form>
<p class="meta">Your about text is a file: <code>{{.AboutRepo}}</code> ·
<code>profile/README.md</code>. {{if .AboutEdit}}<a href="{{.AboutEdit}}">Edit it</a>.{{else}}
It has no repository yet.{{end}}</p>
{{if not .AboutEdit}}<form method="post" action="/settings" class="setform">
  <input type="hidden" name="field" value="profile-repo">
  <button type="submit" class="btn">Create {{.AboutRepo}}</button>
</form>{{end}}
```

- [ ] **Step 8: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/httpd/ ./internal/control/ && go test ./e2e/ -run 'TestProfile' -v -timeout 10m`
Expected: PASS. If `TestMainWidthClass` fails, a template was added —
it was not, so investigate rather than paper over it.

- [ ] **Step 9: Commit**

```bash
git add internal/control/profile.go internal/httpd/account.go internal/web/templates/account.html e2e/profileabout_test.go
git commit -m "profile: the about text is written as a file, not a flag

Ref #236"
```

---

### Task 5: Hide dot-repos from explore and profile listings

Hiding the repository is what a dot-repo buys over `cmc/cmc`; without
this the move trades one visible single-purpose repository for another.

**Files:**
- Modify: `internal/control/explore.go:54-` (the listing loop)
- Modify: `internal/control/profile.go` (`runProfileShow`'s repo loop)
- Test: `e2e/profileabout_test.go` (append)

**Interfaces:**
- Consumes: `store.Repo.Name`.
- Produces: nothing new.

- [ ] **Step 1: Write the failing test**

Append to `e2e/profileabout_test.go`:

```go
// A dot-repo is infrastructure: it stays out of explore and off the
// profile's repository list, and stays in the owner's own inventory.
func TestDotReposHiddenFromListings(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.ssh(t, aliceKey, "", "repo", "create", "alice/.gitbay")
	inst.ssh(t, aliceKey, "", "repo", "create", "alice/app")

	out, _, _ := inst.ssh(t, aliceKey, "", "explore", "--json")
	if strings.Contains(out, ".gitbay") {
		t.Errorf("dot-repo listed in explore: %s", out)
	}
	if !strings.Contains(out, "alice/app") {
		t.Errorf("ordinary repo missing from explore: %s", out)
	}

	out, _, _ = inst.ssh(t, aliceKey, "", "profile", "show", "alice", "--json")
	if strings.Contains(out, `"path":"alice/.gitbay"`) {
		t.Errorf("dot-repo listed on the profile: %s", out)
	}

	out, _, _ = inst.ssh(t, aliceKey, "", "repo", "list", "--json")
	if !strings.Contains(out, "alice/.gitbay") {
		t.Errorf("dot-repo missing from the owner's own inventory: %s", out)
	}

	// It is still reachable at its URL.
	if status, _ := inst.get(t, "/alice/.gitbay"); status != 200 {
		t.Errorf("dot-repo page returned %d", status)
	}
}
```

Check `inst.get`'s return shape against `e2e/commentmigrate_test.go`
(`_, body := inst.get(...)`) and adjust the status assertion to match.

- [ ] **Step 2: Run it and watch it fail**

Run: `go test ./e2e/ -run TestDotReposHiddenFromListings -v -timeout 10m`
Expected: FAIL — `.gitbay` appears in explore and on the profile.

- [ ] **Step 3: Filter the two listings**

In `internal/control/explore.go`, inside the `for _, repo := range repos`
loop, above the cursor check:

```go
		// A dot-repo is infrastructure, not a project; .gitbay holds an
		// owner's profile content and has nothing to explore.
		if strings.HasPrefix(repo.Name, ".") {
			continue
		}
```

Add `"strings"` to that file's imports if absent.

In `internal/control/profile.go`, inside `runProfileShow`'s
`for _, repo := range all` loop, above the access check:

```go
		if strings.HasPrefix(repo.Name, ".") {
			continue
		}
```

- [ ] **Step 4: Run the tests**

Run: `go build ./... && go test ./e2e/ -run 'TestProfile|TestDotRepos' -v -timeout 10m`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/control/explore.go internal/control/profile.go e2e/profileabout_test.go
git commit -m "explore, profile: dot-repos stay out of the listings

Ref #236"
```

---

### Task 6: Migration and backfill

A SQL migration cannot write git objects, and `gitbayd` runs
`MigrateUp` at startup — so a backfill that reads the columns must not
run after the migration that drops them. The migration parks the text
in a holding table; a one-shot drains it.

**Files:**
- Create: `internal/store/migrations/0058_profile_about_out.up.sql`
- Create: `internal/store/migrations/0058_profile_about_out.down.sql`
- Create: `internal/store/aboutbackfill.go`
- Create: `cmd/gitbayd/adminabout.go`
- Modify: `internal/store/orgs.go:226-280` (`Profile`, `OwnerProfile`, `SetOwnerProfile`)
- Modify: `cmd/gitbayd/main.go:390-402` (register the command)
- Test: `e2e/aboutbackfill_test.go` (create)

**Interfaces:**
- Consumes: `control.ProfileRepoName`, `control.AboutBase` (Task 4),
  `control.RepoDir(root, owner, name) string`,
  `gitutil.InitBare(dir, branch, hooksDir string) error`,
  `gitutil.CommitFileChange(...)` (Task 2),
  `store.CreateRepo(ownerKind string, ownerID int64, name, visibility string) (int64, error)`.
- Produces:
  - `func (s *Store) PendingAboutBackfill() ([]AboutRow, error)` and
    `func (s *Store) ClearAboutBackfill(kind string, id int64) error`,
    with `type AboutRow struct { OwnerKind string; OwnerID int64; OwnerName string; About string; Format string }`.
  - `gitbayd admin migrate-profile-about`.

- [ ] **Step 1: Write the migration**

`internal/store/migrations/0058_profile_about_out.up.sql`:

```sql
-- The about text moves into profile/README.* in <owner>/.gitbay. A SQL
-- migration cannot write git objects, so the text is parked here and
-- `gitbayd admin migrate-profile-about` drains the table into
-- repositories. A later release drops the emptied table.
CREATE TABLE profile_about_backfill (
  owner_kind   TEXT    NOT NULL,
  owner_id     INTEGER NOT NULL,
  about        TEXT    NOT NULL,
  about_format TEXT    NOT NULL,
  PRIMARY KEY (owner_kind, owner_id)
);

INSERT INTO profile_about_backfill (owner_kind, owner_id, about, about_format)
SELECT 'user', id, about, about_format FROM users WHERE about <> '';

INSERT INTO profile_about_backfill (owner_kind, owner_id, about, about_format)
SELECT 'org', id, about, about_format FROM orgs WHERE about <> '';

ALTER TABLE users DROP COLUMN about;
ALTER TABLE users DROP COLUMN about_format;
ALTER TABLE orgs  DROP COLUMN about;
ALTER TABLE orgs  DROP COLUMN about_format;
```

`internal/store/migrations/0058_profile_about_out.down.sql`:

```sql
ALTER TABLE users ADD COLUMN about TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN about_format TEXT NOT NULL DEFAULT 'md';
ALTER TABLE orgs  ADD COLUMN about TEXT NOT NULL DEFAULT '';
ALTER TABLE orgs  ADD COLUMN about_format TEXT NOT NULL DEFAULT 'md';

UPDATE users SET about = (SELECT about FROM profile_about_backfill
    WHERE owner_kind = 'user' AND owner_id = users.id),
  about_format = (SELECT about_format FROM profile_about_backfill
    WHERE owner_kind = 'user' AND owner_id = users.id)
  WHERE id IN (SELECT owner_id FROM profile_about_backfill WHERE owner_kind = 'user');

UPDATE orgs SET about = (SELECT about FROM profile_about_backfill
    WHERE owner_kind = 'org' AND owner_id = orgs.id),
  about_format = (SELECT about_format FROM profile_about_backfill
    WHERE owner_kind = 'org' AND owner_id = orgs.id)
  WHERE id IN (SELECT owner_id FROM profile_about_backfill WHERE owner_kind = 'org');

DROP TABLE profile_about_backfill;
```

- [ ] **Step 2: Run the migration round-trip test**

Run: `go test ./internal/store/ -run TestMigrateUpDown -v`
Expected: FAIL to compile — `OwnerProfile` still selects the dropped
columns. Proceed to Step 3, then re-run.

- [ ] **Step 3: Take the about out of the store's profile**

In `internal/store/orgs.go`, delete the `About` and `AboutFormat` fields
from `Profile`, and update its doc comment:

```go
// Profile is the presentational half of a user or org. The about text
// is not here: it is a file in <owner>/.gitbay, read through the
// control layer.
type Profile struct {
	Description string        `json:"description,omitempty"`
	Website     string        `json:"website,omitempty"`
	Links       []ProfileLink `json:"links,omitempty"`
}
```

In `OwnerProfile`, drop the two columns from the SELECT and the two
scan targets:

```go
	err := s.DB.QueryRow(
		"SELECT description, website, links FROM "+table+" WHERE id = ?", id).
		Scan(&p.Description, &p.Website, &linksJSON)
```

In `SetOwnerProfile`, drop the `AboutFormat` defaulting block and the
two columns from the UPDATE:

```go
	_, err := s.DB.Exec(
		"UPDATE "+table+" SET description = ?, website = ?, links = ? WHERE id = ?",
		p.Description, p.Website, links, id)
```

- [ ] **Step 4: Run build, vet and the store tests**

Run: `go build ./... && go vet ./... && go test ./internal/store/`
Expected: PASS. `internal/control/migrate.go` embeds `store.Profile` in
its account bundle; the fields simply disappear from that JSON, and an
older bundle carrying them still imports because `encoding/json` ignores
unknown fields. No bundle version bump.

- [ ] **Step 5: Write the failing backfill test**

Create `e2e/aboutbackfill_test.go`:

```go
package e2e

import (
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

func TestMigrateProfileAbout(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	// Seed the holding table the way migration 0058 would have.
	dbPath := filepath.Join(inst.root, "gitbay.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.DB.Exec(
		"INSERT INTO profile_about_backfill (owner_kind, owner_id, about, about_format) "+
			"VALUES ('user', (SELECT id FROM users WHERE username='alice'), ?, 'org')",
		"* alice\n\ntext from the database\n")
	st.Close()
	if err != nil {
		t.Fatal(err)
	}

	inst.admin(t, "admin", "migrate-profile-about")

	out, _, code := inst.ssh(t, aliceKey, "", "profile", "show", "alice", "--json")
	if code != 0 {
		t.Fatalf("profile show: %d", code)
	}
	if !strings.Contains(out, "text from the database") {
		t.Errorf("about not moved into the repository: %s", out)
	}
	if !strings.Contains(out, `"about_path":"profile/README.org"`) {
		t.Errorf("about not written at the recorded format: %s", out)
	}

	// Idempotent: a second run is a no-op and leaves the table empty.
	inst.admin(t, "admin", "migrate-profile-about")
	st, _ = store.Open(dbPath)
	var n int
	st.DB.QueryRow("SELECT count(*) FROM profile_about_backfill").Scan(&n)
	st.Close()
	if n != 0 {
		t.Errorf("holding table still has %d row(s)", n)
	}
}
```

`inst.admin`'s first argument is the admin subcommand path as used in
`e2e/commentmigrate_test.go` — confirm the exact call shape there
(`inst.admin(t, "admin", "user", "create", ...)`) and match it.

- [ ] **Step 6: Run it and watch it fail**

Run: `go test ./e2e/ -run TestMigrateProfileAbout -v -timeout 10m`
Expected: FAIL — no such subcommand.

- [ ] **Step 7: Read and clear the holding table**

Create `internal/store/aboutbackfill.go`:

```go
package store

// AboutRow is one owner's parked about text, waiting to become a file
// in <owner>/.gitbay. Migration 0058 fills the table; the backfill
// command drains it.
type AboutRow struct {
	OwnerKind string
	OwnerID   int64
	OwnerName string
	About     string
	Format    string
}

// PendingAboutBackfill lists the owners whose about text has not been
// written to a repository yet, resolving each one's name.
func (s *Store) PendingAboutBackfill() ([]AboutRow, error) {
	rows, err := s.DB.Query(`
		SELECT b.owner_kind, b.owner_id, b.about, b.about_format,
		       COALESCE(u.username, o.name)
		  FROM profile_about_backfill b
		  LEFT JOIN users u ON b.owner_kind = 'user' AND u.id = b.owner_id
		  LEFT JOIN orgs  o ON b.owner_kind = 'org'  AND o.id = b.owner_id
		 ORDER BY b.owner_kind, b.owner_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AboutRow
	for rows.Next() {
		var r AboutRow
		var name *string
		if err := rows.Scan(&r.OwnerKind, &r.OwnerID, &r.About, &r.Format, &name); err != nil {
			return nil, err
		}
		if name == nil {
			continue // the owner is gone; the row goes with them
		}
		r.OwnerName = *name
		out = append(out, r)
	}
	return out, rows.Err()
}

// ClearAboutBackfill drops one owner's row once its file exists.
func (s *Store) ClearAboutBackfill(kind string, id int64) error {
	_, err := s.DB.Exec(
		"DELETE FROM profile_about_backfill WHERE owner_kind = ? AND owner_id = ?", kind, id)
	return err
}
```

- [ ] **Step 8: Write the one-shot**

Create `cmd/gitbayd/adminabout.go`:

```go
package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

// adminMigrateProfileAboutCmd drains profile_about_backfill: each
// owner's parked about text becomes profile/README.* in <owner>/.gitbay.
// Idempotent — an owner who already has the file keeps it and loses the
// row.
func adminMigrateProfileAboutCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "migrate-profile-about",
		Short: "write parked profile about text into each owner's .gitbay repository",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load(configPath)
			if err != nil {
				return err
			}
			st, err := openStore(cfg)
			if err != nil {
				return err
			}
			defer st.Close()
			rows, err := st.PendingAboutBackfill()
			if err != nil {
				return err
			}
			n := 0
			for _, row := range rows {
				written, err := writeAbout(cfg, st, row)
				if err != nil {
					return fmt.Errorf("%s: %w", row.OwnerName, err)
				}
				if err := st.ClearAboutBackfill(row.OwnerKind, row.OwnerID); err != nil {
					return err
				}
				if written {
					n++
				}
			}
			fmt.Printf("wrote %d profile about file(s)\n", n)
			return nil
		},
	}
}

// writeAbout creates <owner>/.gitbay if it does not exist and commits
// the about at the recorded format. It reports whether it wrote
// anything: an owner who already has the file is left alone.
func writeAbout(cfg *config.Config, st *store.Store, row store.AboutRow) (bool, error) {
	path := row.OwnerName + "/" + control.ProfileRepoName
	repo, err := st.RepoByPath(path)
	if err != nil {
		id, cerr := st.CreateRepo(row.OwnerKind, row.OwnerID, control.ProfileRepoName, "public")
		if cerr != nil {
			return false, cerr
		}
		dir := control.RepoDir(cfg.Server.Root, row.OwnerName, control.ProfileRepoName)
		if ierr := gitutil.InitBare(dir, "main", control.HooksDir(cfg.Server.Root)); ierr != nil {
			st.DeleteRepo(id)
			return false, ierr
		}
		if repo, err = st.RepoByPath(path); err != nil {
			return false, err
		}
	}
	dir := control.RepoDir(cfg.Server.Root, repo.OwnerName, repo.Name)
	ext := ".md"
	if row.Format == "org" {
		ext = ".org"
	}
	file := control.AboutBase + ext
	if _, err := gitutil.ReadBlob(dir, repo.DefaultBranch, file, 1); err == nil {
		return false, nil // already there
	}
	email := row.OwnerName + "@users.noreply." + cfg.SiteHost()
	if row.OwnerKind == "user" {
		if addr, _ := st.PrimaryVerifiedEmail(row.OwnerID); addr != "" {
			email = addr
		}
	}
	_, err = gitutil.CommitFileChange(dir, repo.DefaultBranch, file,
		[]byte(row.About), row.OwnerName, email, "move profile about out of the database")
	return err == nil, err
}
```

Confirm `cfg.SiteHost()`, `control.HooksDir`, `st.PrimaryVerifiedEmail`
and `st.DeleteRepo` exist with those names:
`grep -rn 'func.*SiteHost\|func HooksDir\|func (s \*Store) PrimaryVerifiedEmail\|func (s \*Store) DeleteRepo' internal/ | head`.
Adjust the calls to what is actually there rather than adding shims.

- [ ] **Step 9: Register the subcommand**

In `cmd/gitbayd/main.go`, in the `admin.AddCommand(` list that already
contains `adminMigrateCommitRefsCmd(),` (around line 401), add:

```go
		adminMigrateProfileAboutCmd(),
```

- [ ] **Step 10: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/store/ && go test ./e2e/ -run 'TestMigrateProfileAbout|TestProfile|TestDotRepos' -v -timeout 15m`
Expected: PASS.

- [ ] **Step 11: Commit**

```bash
git add internal/store/migrations/0058_profile_about_out.up.sql \
        internal/store/migrations/0058_profile_about_out.down.sql \
        internal/store/aboutbackfill.go internal/store/orgs.go \
        cmd/gitbayd/adminabout.go cmd/gitbayd/main.go \
        e2e/aboutbackfill_test.go
git commit -m "store: move the parked about text into each owner's .gitbay

Ref #236"
```

---

### Task 7: Documentation

**Files:**
- Modify: `.gitbay/wiki/Parity.md` (the profile row)
- Modify: `.gitbay/wiki/Users.md` (the profile section)
- Modify: `CHANGELOG.org`

**Interfaces:**
- Consumes: everything above.
- Produces: nothing.

- [ ] **Step 1: Find the rows to change**

Run: `grep -n 'about\|profile' .gitbay/wiki/Parity.md .gitbay/wiki/Users.md | head -30`

- [ ] **Step 2: Update Parity**

The profile row's capability changes: the about is no longer a
`profile set` flag. Add or amend a row reading that the about text is
`profile/README.{md,org}` in `<owner>/.gitbay`, written by push or
`repo commit-file`, readable on every surface. Keep the table's existing
column order and marker vocabulary — read the surrounding rows first and
match them.

- [ ] **Step 3: Update Users**

In the profile section, replace the `--about` documentation with where
the file lives, the resolution order (`.md`, `.org`, `.markdown`), that
a private `.gitbay` keeps the about private, and that `.gitbay` does not
appear in explore or on the profile's repository list. Match the page's
existing voice.

- [ ] **Step 4: Update the changelog**

Add an entry under the current unreleased heading in `CHANGELOG.org`,
matching the file's existing entry style:

```
- Profile about text moved into =profile/README.{md,org}= on the default
  branch of =<owner>/.gitbay=. It is written by a push or
  =repo commit-file=; =profile set --about= is gone. Run
  =gitbayd admin migrate-profile-about= after upgrading to write each
  owner's existing text into their repository. Repository names may now
  start with a dot, and dot-repos stay out of explore and profile
  listings.
```

- [ ] **Step 5: Run the full local check**

Run: `go build ./... && go vet ./... && go test ./internal/... `
Expected: PASS. The full e2e suite belongs to CI on bay1.

- [ ] **Step 6: Commit**

```bash
git add .gitbay/wiki/Parity.md .gitbay/wiki/Users.md CHANGELOG.org
git commit -m "docs: the profile about text lives in a repository

Closes #236"
```

---

## Notes for whoever runs the deploy

The migration and the backfill are one release but two steps. After
`make deploy` has restarted `gitbayd` (which runs `MigrateUp`), run:

```bash
ssh -p 2222 root@gitbay.org gitbayd admin migrate-profile-about
```

Until it runs, profiles that had an about show none. The holding table
keeps the text, so nothing is lost in between.
