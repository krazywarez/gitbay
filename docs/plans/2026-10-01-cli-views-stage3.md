# CLI views implementation plan (stage 3)

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Every command that still draws its terminal output through `table` or `view` draws a screen, and `view` keeps only its piped behaviour.

**Architecture:** Stages 1 and 2 built `screen` (`internal/control/screen.go`), `emitView`/`emitPageView`, and eleven screens. This stage adds one helper, `listScreen`, and migrates the remaining 70 commands noun by noun: a builder per command (or per shared emitter), the command switched to `emitView`/`emitPageView`, its piped output pinned before and after. The last task strips `view`'s terminal branches, so a caller missed here falls back to the plain layout rather than a half-styled one.

**Tech Stack:** Go, `internal/control`.

**Spec:** `docs/specs/2026-10-01-cli-views-design.md`. Stage 1–2 plan: `docs/plans/2026-10-01-cli-views.md` (its "Corrections" section applies here too).

**Issues:** #316 (tracking), #319 (this stage).

## Global Constraints

- Piped output and `--json` byte-identical for every migrated command; prove it with `pinPlain` before touching the command.
- Rendering stays in `internal/control`; nothing in `cmd/gitbay` changes.
- Colour is never the only signal (`stripSGR(colour) == colourless` holds for every screen; the renderer guarantees it if builders use cells, never raw SGR).
- Every legend and `more` argv passes `checkActions` (registered path, declared flags).
- Never put in a cell or legend: webhook secrets, API token values, push device tokens beyond `ShortToken`, mirror tokens, mailbox credentials, `web login` URLs, base64 page content. A legend command that reads a secret takes it from stdin and carries no value (`repo secret set <path> <NAME>`, never `--secret`).
- Destructive commands appear in a legend without their confirmation flag (`release delete <repo> <tag>`, never `--yes`).
- Commit messages end `Ref #319`; the last commit of the stage `Closes #319`. No attribution lines. Signed commits, `--strategy ff`.
- Locally: `go build ./... && go vet ./... && go test ./internal/control/`. CI runs e2e, including `e2e/readonly_test.go`, which runs every read command at 60 columns and fails on a line wider than 60 (suggested commands are exempt only in unit tests; keep legend argv short).

## Reference

### Helpers that exist

| Name | Where | Use |
|---|---|---|
| `screen`, `field`, `section{title,n,note,rows,more,empty}`, `row`, `rowOf`, `action{group,argv}` | `screen.go` | the model |
| `c.emitView(data, plain, build)` / `c.emitPageView(p, items, next, plain, build)` | `control.go`, `cursor.go` | route terminal to `build` |
| `discussion(cs)`, `events(cs)` | `screen.go` | comment and system-event sections |
| `cRef cGlyph cYou cFlex cText cMeta cAge cNum cSize cSwatch cState cLink cMark` | `table.go` | cells |
| `glyph(state)` | `term.go` | `✓` success/ok/approved/verified/passed/merged, `✗` failure/failed/error/bad sig, `◐` pending/running/queued, `○` closed/draft/canceled/skipped |
| `labelsMark`, `checksMark`, `reviewMark` | `marks.go` | row marks |
| `keyValues`, `c.usedText`, `c.expiresText` | `audit.go`, `identity.go` | reuse in builders |
| `checkActions`, `pinPlain`, `screenCtx`, `renderString`, `sectionCounts`, `actionArgvs`, `actionVerbs` | `*_test.go` | tests |

### Migration recipe (every command)

1. **Pin.** In the task's test file, add `Test<Cmd>PlainPinned`: build the fixture, `Dispatch` the command with no `Term`, `pinPlain(t, "<cmd-slug>", out.String())`. Run with `-update-plain`, then without. Commit nothing yet. If the screen tests in the same file do not compile yet, keep them out of the file until the pin exists (stage 2 lost a file juggling this; copy the full file aside with `cp` and check `grep -c 'func Test'` after restoring).
2. **Screen test.** Construct the command's data value directly (no store), call the builder, assert section titles and counts, the lead glyph of a representative row, the action argv, and `checkActions(t, s)`.
3. **Builder.** Write `<cmd>Screen(c *Ctx, ...) screen` next to the command, from the spec row below.
4. **Wire.** Replace `c.emit(d, plain)` with `c.emitView(d, plain, func() screen { return <cmd>Screen(...) })` (`emitPage` → `emitPageView`). Inside `plain`, delete every `c.Term.Cols > 0` branch and keep the `== 0` path unconditionally; a terminal no longer reaches `plain`. Move terminal-only data loads out of `plain` to a `c.Term.Cols > 0 && !c.JSON` block before the emit.
5. **Orphans.** `grep` every helper the deleted branches called; delete those now unused, with their tests.
6. **Run** `go test ./internal/control/ -run '<Cmd>'`, then the package. Pins must still match.

### Worked example: a list with a shared emitter (`repo topics`, `repo topics add`, `repo topics remove`)

`runRepoTopics` (`repo.go:963`) and `editTopics` (`repo.go:987`) both print `[]string` through a one-column table.

Test (`internal/control/stage3repo_test.go`):

```go
func TestRepoTopicsPlainPinned(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	owner := store.User{ID: uid, Username: "alice"}
	c, out, errOut := mrTestCtx(st, owner)
	if code := Dispatch(c, []string{"repo", "topics", "add", repo.Path(), "cli", "forge"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	pinPlain(t, "repo-topics-add", out.String())
	c, out, errOut = mrTestCtx(st, owner)
	if code := Dispatch(c, []string{"repo", "topics", repo.Path()}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	pinPlain(t, "repo-topics", out.String())
}

func TestTopicsScreen(t *testing.T) {
	s := topicsScreen(screenCtx(80, false), screenRepo, []string{"cli", "forge"})
	if n := sectionCounts(s); n["Topics"] != 2 {
		t.Errorf("sections = %v", n)
	}
	if got := actionVerbs(s); !slices.Equal(got, []string{"topics", "topics"}) {
		t.Errorf("actions = %q", got)
	}
	checkActions(t, s)
}
```

Helper and builder (`screen.go` and `repo.go`):

```go
// listScreen is a list command's terminal screen: one section of rows
// and the commands that apply.
func listScreen(title string, rows []row, actions ...action) screen {
	return screen{sections: []section{{title: title, n: len(rows), rows: rows}}, actions: actions}
}
```

```go
// topicsScreen is a repository's topics at a terminal, after a read or
// an edit.
func topicsScreen(c *Ctx, repo store.Repo, topics []string) screen {
	rows := make([]row, len(topics))
	for i, t := range topics {
		rows[i] = rowOf(cRef(t))
	}
	return listScreen("Topics", rows,
		action{"Edit", []string{"repo", "topics", "add", repo.Path(), "<topic>"}},
		action{"Edit", []string{"repo", "topics", "remove", repo.Path(), "<topic>"}},
	)
}
```

Both `runRepoTopics` and `editTopics` call `c.emitView(topics, plain, func() screen { return topicsScreen(c, repo, topics) })` (`now` in `editTopics`). An empty list still prints "nothing to list" on stderr: `emit` handles that before `build` runs.

### Worked example: a show (`org show`, also serving `org members list`)

```go
// orgShowScreen is org show at a terminal: the organisation and its
// members, and where to go from here.
func orgShowScreen(c *Ctx, d orgShowOut) screen {
	members := section{title: "Members", n: len(d.Members)}
	for _, m := range d.Members {
		members.rows = append(members.rows, rowOf(cRef(m.User), cState(m.Role)))
	}
	return screen{
		fields:   []field{{"Org", []cell{cLink(d.Org, c.siteURL(d.Org))}}},
		sections: []section{members},
		actions: []action{
			{"Members", []string{"org", "members", "add", d.Org, "<user>"}},
			{"Org", []string{"org", "team", "list", d.Org}},
			{"Org", []string{"org", "label", "list", d.Org}},
			{"Org", []string{"org", "milestone", "list", d.Org}},
		},
	}
}
```

If `runOrgShow`'s output type is a local `type out struct`, hoist it to package level as `orgShowOut` (same JSON tags) as stage 2 did for `repoShowOut`. `org members list` delegates to `runOrgShow`, so it migrates with it.

### Worked example: a stream with a header (`repo commit`)

The screen has no diff part. `runRepoCommit` (`sig.go:250`) keeps `c.emit` and branches itself:

```go
	return c.emit(d, func(w io.Writer) {
		if c.Term.Cols == 0 {
			// existing plain header and diff, unchanged
			return
		}
		c.render(w, commitScreen(c, repo, d, body))
		fmt.Fprintf(w, "\n%s", c.Term.diff(d.Diff))
	})
```

`commitScreen` fields: `Commit` (`cRef(sha10)`, `cText(subject)`), `Author` (`cText(name+" <"+email+">")`, `cAge(date)`), `Signature` (`cGlyph(state)`, `cState(state)`, `cMeta(signer, fingerprint)`), `URL` when `!c.Term.Links`; body = message without subject, format `"text"`; section `Checks` (`cGlyph(state)`, `cFlex(context)`); actions `Read: repo log <path> --ref <sha>`, `Read: repo tree <path> --ref <sha>`. The legend prints before the diff, which is what a reader scrolling up from the end of a long patch expects to find above it.

## Spec rows

Columns: **title** of the section (or "fields" for a show), **lead** (the glyph cell second in the row, after the ref, or none), **row** cells after the lead, **actions** (`Group: argv`), **notes**. `<p>` is `repo.Path()`.

---

## Task 1: `listScreen` and the repository lists (MR 3a)

**Files:** `screen.go` (`listScreen`), `repo.go`, `pagescmd.go`, `deploykey.go`, `build.go` (secrets), `mirrorcmd.go`, `runnerrepo.go`, test `stage3repo_test.go`, pins under `testdata/plain/`.

| Command | Title | Lead | Row | Actions | Notes |
|---|---|---|---|---|---|
| `repo topics` / `topics add` / `topics remove` | Topics | none | `cRef(t)` | worked example | shared `topicsScreen` |
| `repo domain list` (`pagescmd.go:196`) | Pages domains | `cGlyph(state)` (`expired` → `"failed"`) | `cRef(domain)`, `cState(state)` | `Domains: repo domain add <p> <domain>`, `repo domain verify <p> <domain>` | |
| `repo deploy-key list` (`deploykey.go:93`) | Deploy keys | none | `cRef(fp)`, `cState(mode)`, `cFlex(label)`, `cMeta(algo, "used "+usedText, expiresText)` | `Keys: repo deploy-key add <p>`, `repo deploy-key remove <p> <fingerprint>` | key arrives on stdin |
| `repo secret list` (`build.go:555`) | Build secrets | none | `cRef(name)` | `Secrets: repo secret set <p> <NAME>`, `repo secret remove <p> <NAME>` | names only, never values |
| `repo access list` (`repo.go:753`) | Access | none | `cRef(user)`, `cState(role)`, `cMeta("via "+source)` | `Access: repo access grant <p> <user> write`, `repo access revoke <p> <user>` | |
| `repo mirror list` (`mirrorcmd.go:99`) | Mirrors | `cGlyph("error"/"pending"/"ok")` from LastError/Pending | `cRef(id)`, `cFlex(url)`, `cMeta(direction, "synced "+relAge(LastSync))`, `cMark(LastError, sgrRed)` | `Mirrors: repo mirror sync <p>`, `repo mirror remove <p> <id>` | never the token |
| `repo runner list` (`runnerrepo.go:102`) | Runners | `cGlyph("running")` when BuildNumber≠0 | `cRef(fp)`, `cFlex(username)`, `cMeta(algo, seen, "building "+repo+" #n "+job)` | `Runners: repo runner remove <p> <fingerprint>` | |
| `repo bookmarks` (`repo.go:1225`) | Bookmarks | none | `cLink(path, url)`, `cState(vis)`, `cFlex(desc)`, `cMeta(n+" bookmarks")` | `Read: repo show <first path>` | drop `cNum` so no header row |
| `repo search` (`repo.go:1045`) | Repositories matching "q" | none | `cLink`, `cState(vis)`, `cFlex(desc)`, `cMeta(topics joined)` | `Read: repo show <first path>` | |
| `repo list` (`repo.go:339`) | Repositories | none | `cLink`, `cState(vis[, archived])`, `cFlex(desc)` | `Read: repo show <first path>`, `New: repo create <owner>/<name>` | `emitPageView` |

Steps: recipe per command; one commit for `listScreen` + topics, one per row group of three or four, each `Ref #319`.

## Task 2: Repository browsing (MR 3a)

**Files:** `sig.go` (log), `read.go` (tree, refs), `symbols.go`, test `stage3repo_test.go`.

| Command | Title | Lead | Row | Actions | Notes |
|---|---|---|---|---|---|
| `repo log` (`sig.go:144`) | Commits on `<ref>` (+ " touching `<path>`") | `cGlyph(sig state)` | `cRef(sha10)`, `cFlex(subject)`, `cMeta(author, relAge(date))` | `Read: repo commit <p> <first sha>`, `repo tree <p> --ref <ref>` | delete the in-closure terminal table |
| `repo tree` (`read.go:281`) | `<p>[/dir] at <ref>` | none | dirs `cMark(name+"/", sgrBlue)`, files `cText(name)`; `cSize`; `cMeta(sha10)` | `Read: repo cat <p> <dir/first file> --ref <ref>`, `repo log <p> --ref <ref> --path <dir>` | `sgrBlue` replaces the last `sgrCyan` in `read.go` |
| `repo refs` (`read.go:~85`) | two sections, Branches and Tags | none | `cRef(name)`, `cMeta(sha10[, "default"])` | `Read: repo log <p> --ref <default>` | default branch first; tags from `sorted` |
| `repo symbols` (`symbols.go:50`) | Symbols matching "q" | none | `cRef(name)`, `cText(kind)`, `cFlex(path:line)` | `Read: repo cat <p> <first path> --ref <ref>` | keep the two stderr notes; `emitPageView` |

## Task 3: Repository shows (MR 3a, then open it)

**Files:** `repo.go` (settings), `deps.go`, `sig.go` (commit), test `stage3repo_test.go`.

| Command | Fields | Sections | Actions | Notes |
|---|---|---|---|---|
| `repo settings show` (`repo.go:783`) | `Repo` (`cRef(p)`), then one field per setting, `on`/`off` via `cState`; add the four not shown today (approvals, resolved, codeowners, website) to the **screen only** | none | `Protect: repo settings protect <p> <branch>`, `repo settings require-mr <p> on` (check the real arg form) | piped output unchanged |
| `repo deps status` (`deps.go:85`) | `Checks` (`cGlyph("ok")`/`cGlyph("failed")` when LastError, `cText("on")`), `Last check` (`cAge`), `Error` (`cMark(…, sgrRed)`), `Tracked in` (`cLink("#n", …"issues"…)`) | Behind: `cRef(name)`, `cText(ecosystem)`, `cMeta(current+" → "+latest)` | `Deps: repo deps disable <p>` | the disabled branch is its own `emitView` with one field `Checks: off` and `Deps: repo deps enable <p>` |
| `repo commit` (`sig.go:250`) | worked example | Checks | worked example | stream: keep `emit` |

End of task: CHANGELOG line under Unreleased, push `cli-views-3a`, `gitbay mr create --source cli-views-3a --target main --title "control: repository screens (CLI views stage 3a)"`.

## Task 4: Account (MR 3b)

**Files:** `identity.go`, `register.go`, `sig.go` (pgp), `token.go`, `web.go`, `profile.go`, test `stage3account_test.go`.

| Command | Title / fields | Lead | Row | Actions | Notes |
|---|---|---|---|---|---|
| `keys list` (`identity.go:117`) | SSH keys | `cYou()` on `c.Source`'s key | `cRef(fp)`, `cState(scope)`, `cFlex(label)`, `cMeta(algo, usedText, expiresText)` | `Keys: keys label <fingerprint> <text>`, `keys remove <fingerprint>` | `●` replaces the "this session" mark; add `"this session"` to its meta so the glyph is not the only signal |
| `email list` (`register.go:58`) | Emails | `cYou()` when unverified | `cRef(addr)`, `cState(verified\|unverified)`, `cMeta("primary"?, VerifiedBy)` | `Email: email add <address>`, `email primary <address>` | |
| `pgp list` (`sig.go:80`) | OpenPGP keys | `cGlyph("failed")` when revoked or expired | `cRef(fp)`, `cFlex(emails parsed from UIDsJSON)`, `cMeta("revoked"/"expires …")` | `Keys: pgp remove <fingerprint>` | parse UIDs for the screen only |
| `token list` (`token.go:103`) | API tokens | `cGlyph("failed")` when expired | `cRef(name)`, `cState(scope)`, `cMeta("by "+createdBy, expiresText)` | `Tokens: token revoke <name>` | never a value |
| `web sessions list` (`web.go:34`) | Browser sessions | none | `cRef(id)`, `cMeta("since "+relAge, "until "+relAge, usedText)` | `Sessions: web sessions revoke <id>` | never `web login` |
| `whoami` (`identity.go:75`) | fields `User`, `Role` (admin only), `Instance`, `Key` (label or fp prefix), `Scope` | | | `Account: keys list`, `token list`, `email list` | move the `SSHKeyByFingerprint` lookup before emit |
| `profile show`, `profile set`, `org profile` (shared `emitProfile`, `profile.go:254`) | fields `Profile` (`cText(name)`, `cMeta(kind, description)`), `Website`, `Activity`; body About | Links, Orgs, Members, Repos (`cLink`, `cState`, `cFlex`) | `Edit: profile set --description <text>` (own user profile only); `Edit: org profile <org> --description <text>` (org) | one builder; empty sections drop out after `set` |

## Task 5: Notifications and webhooks (MR 3b)

**Files:** `notifications.go`, `webhook.go`, test `stage3notify_test.go`.

| Command | Title / fields | Lead | Row | Actions | Notes |
|---|---|---|---|---|---|
| `notifications list` (`notifications.go:452`) | Notifications | `cYou()` when unread | `cRef(repo)`, `cFlex(actor+" "+summary)`, `cMeta(kind, relAge)` | `Inbox: notifications read --all`; `Read: issue show`/`mr show <repo> <n>` for the first issue/MR row (number from the path's last segment) | keep the early empty-page exit; `emitPageView` |
| settings `show`/`mail`/`reply`/`watch`/`push` (shared `emitNotificationSettings`, `:254`) | fields Mail, Reply, Watch, Push, each `cState("on"/"off")` | | | one action per setting offering the opposite value: `Settings: notifications settings mail off`, …; `Devices: notifications device list` | one builder serves five commands |
| `notifications device list` (`:387`) | Push devices | none | `cRef(id)`, `cFlex(label)`, `cMeta(ShortToken, relAge(added))` | `Devices: notifications device remove <id>` | `ShortToken` only |
| `webhook list` (`webhook.go:104`) | Webhooks | `cGlyph("ok")` active / `cGlyph("closed")` inactive | `cRef(id)`, `cFlex(url clipped to scheme+host+"/…")`, `cMeta(events, "signed"?)` | `Hooks: webhook deliveries <p>` | elide the URL path on the screen; piped unchanged |
| `webhook deliveries` (`webhook.go:159`) | Deliveries | `cGlyph` with `delivered`→`"ok"` | `cRef(id)`, `cFlex(event)`, `cMeta(n+" attempts", "HTTP "+status)`, `cMark(LastError, sgrRed)` | `Retry: webhook redeliver <p> <first failed id>` | |

## Task 6: Organisations, labels and milestones (MR 3b)

**Files:** `org.go`, `orglabel.go`, `teams.go`, `label.go`, `milestone.go`, test `stage3org_test.go`.

| Command | Title / fields | Lead | Row | Actions | Notes |
|---|---|---|---|---|---|
| `org list` (`org.go:91`) | Organizations | none | `cLink(org)`, `cState(role)` | `Read: org show <first org>` | |
| `org show` + `org members list` | worked example | | | | |
| `org label list` (`orglabel.go:142`) and `label list` (`label.go:41`) | Labels | none | `cSwatch(color)`, `cRef(name)`, `cMeta(n+" issues", m+" MRs", "org"?)` | org: `Labels: org label set <org> <label> --color rrggbb`; repo: `Labels: label set <p> <label> --color rrggbb`, `Read: issue list <p> --label <first>` | one `labelsScreen(c, labels, actions...)`; dropping `cNum` drops the header row |
| `milestone list` + `org milestone list` (shared `emitMilestones`, `milestone.go:123`) | Milestones | `cGlyph(state)`; `cYou()` when open and overdue | `cRef(title)`, `cMeta(due or "overdue …", "c/t closed", "org"?)` | caller passes its actions: repo `Milestones: milestone create <p> <title>`, `milestone list <p> --state all`; org the `org milestone` equivalents | give `emitMilestones` an `actions []action` parameter |
| `org team list` (`teams.go:141`) | Teams | none | `cRef(name)` | `Read: org team show <org> <first team>` | |
| `org team show` (`teams.go:166`) | fields `Team` (`cLink(org/team)`), `Members` | Grants: `cLink(repo)`, `cState(role)` | `Team: org team add <org> <team> <user>`, `org team grant <org> <team> <owner/name> write` | |

End of task: CHANGELOG line, push `cli-views-3b`, MR "control: account, notification and organisation screens (CLI views stage 3b)".

## Task 7: Work items and content (MR 3c)

**Files:** `query.go`, `search.go`, `dashboard.go` (feed), `mr.go` (revisions), `milestone.go` (templates), `build.go` (jobs), `status.go`, `release.go`, `snippet.go`, `wiki.go`, `explore.go`, `audit.go`, test `stage3work_test.go`.

| Command | Title / fields | Lead | Row | Actions | Notes |
|---|---|---|---|---|---|
| `query run`, `issue list --query`, `mr list --query` (shared `runItemQuery`, `query.go:196`) | query name, or "Results" from `listByQuery` | `cGlyph(state or "draft")` | `cLink(ref)`, `cFlex(title)`, `cMeta(author, milestone, relAge(updated))` | `Read: issue show`/`mr show <repo> <n>` for the first row by kind | one builder, title passed in |
| `query list` (`query.go:326`) | Saved queries | none | `cRef(name)`, `cFlex(query)`, `cMeta("pinned"?)` | `Queries: query run <first>`, `query pin <first>` | |
| `query show` (`query.go:351`) | fields Query, Matches, Pinned | | | `Queries: query run <name>`, `query pin`/`unpin <name>` | |
| `search` (`search.go:92`) | sections Repositories, Issues, Merge requests | `cGlyph(state)` for issue/MR rows | repo `cLink`, `cFlex(title)`; item `cLink(ref)`, `cFlex(title)`, `cMeta(author)` | `Narrow: search <q> --kind issue` | `writeSearchTable` stays for piped |
| `feed` (`dashboard.go:340`) | Activity | none | `l.termCells(c)` | none | `emitPageView`; "Next page" covers paging |
| `mr revisions` (`mr.go:2080`) | Revisions | `cMark("●", "")` for the current one, plain dot, not `cYou` | `cRef("v"+n)`, `cRef(sha10)`, `cAge` | `Compare: mr range-diff <p> <n>` | keep the stderr note |
| `issue templates` (`milestone.go:288`) | Issue templates | none | `cRef(name)` | `New: issue create <p> --title <title>` | never the body |
| `build jobs` (`build.go:443`) | Jobs | none | `cRef(name)`, `cMeta(when)` | `Run: build trigger <p> <first job>` | |
| `status list` (`status.go:107`) | fields Commit, Combined (`cGlyph`+`cState`) | Statuses: `cGlyph(state)`, `cText(context)`, `cFlex(desc)`, `cMeta(creator)` | `Report: status set <p> <sha> --context <context> --state success` | drop the pseudo-row on the screen |
| `release list` (`release.go:253`) | Releases | none | `cLink(tag)`, `cFlex(subtitle)`, `cMeta(n+" assets", relAge)` | `Read: release show <p> <first tag>` | `emitPageView` |
| `release show` (`release.go:317`) | fields Release (`cRef(tag)`, `cText(title)`), Author, Released; body Notes | Assets: `cRef(name)`, `cSize`, `cMeta(sha12)` | `Get: release asset get <p> <tag> <first asset>`, `Edit: release edit <p> <tag> --title <title>` | rebind `repo` (it is `_` today) |
| `snippet list` (`snippet.go:244`) | Snippets | none | `cRef(id)`, `cState(vis)`, `cFlex(desc)`, `cMeta(files, relAge)` | `Read: snippet show <first id>` | `emitPageView` |
| `snippet show` (`snippet.go:212`) | fields Snippet, Owner, Visibility, Updated | Files: `cRef(name)`, `cSize` | `Get: snippet file get <id> <first file>`, `Edit: snippet edit <id> --visibility public` | never file content |
| `wiki list` (`wiki.go:83`) | Wiki | none | `cLink(page)`, `cMeta("home"?)` | `Read: wiki show <p> <home>` | |
| `wiki show` (`wiki.go:126`) | fields Page, File, URL; body Content (format md/org); binary pages get a `Binary` field and no body | | `Read: wiki list <p>` | never Base64 |
| `explore` (`explore.go:60`) | Explore | none | `cLink(path)`, `cFlex(desc)`, `cMeta(topics, "archived"?)` | `Read: repo show <first path>` | `emitPageView` |
| `audit` (`audit.go:35`) | Audit | none | `cAge`, `cText(actor)`, `cText(action)`, `cFlex(keyValues(data))` | `Filter: audit --since 24h` | |

## Task 8: Administration (MR 3c)

**Files:** `admin.go`, `adminhost.go`, `adminmail.go`, test `stage3admin_test.go`.

| Command | Title / fields | Lead | Row | Actions | Notes |
|---|---|---|---|---|---|
| `admin user list` (`admin.go:134`) | Accounts | `cYou()` when pending | `cRef(name)`, `cState(state)`, `cMeta("admin"?, "seen "+relAge)` | `Filter: admin user list --state pending` | `emitPageView` |
| `admin user show` (`admin.go:175`) | fields User (`cRef`, `cState`, `cMeta("admin"?)`), Seen, Repos (+limit), Sessions | Keys, Emails, PGP keys, Orgs, API tokens | `Manage: admin user disable <name>` or `enable`, `admin user promote`/`demote <name>` by current state | token names and scopes only |
| `admin runners` (`admin.go:540`) | fields Queue (`cText(n+" pending")`, `cMeta(claimed, wait avg, wait max, reaped)`) | Runners: lead `cGlyph("running")` when holding; `cRef(user)`, `cMeta(scope, seen, held)` | `Prune: admin runners remove <fingerprint>` (first idle runner) | never `forget` |
| `admin repo list` (`admin.go:407`) | Repositories | none | `cLink(path)`, `cState(vis[, archived])`, `cSize(bytes)`, `cMeta("pushed "+relAge)` | `Filter: admin repo list --visibility private` | keeps the header (size column) |
| `admin mr prune` (`admin.go:616`) | Pruned | `cGlyph("ok")` / `cGlyph("skipped")` for "already gone" | `cLink("!n")`, `cRef(head)` or `cMeta("already gone")` | none | |
| `admin stats` (`adminhost.go:291`) | fields Users, Orgs, Repos, Issues (+open), MRs (+open), Disk (`cSize(db)`, `cMeta(repos, lfs)`) | Repositories: top 20 by bytes, `cLink(path)`, `cSize`; `more: admin repo list` | `Admin: admin user list`, `admin runners` | the dashboard's Problems line points here |
| `admin mail inbound check` (`adminmail.go:25`) | fields Inbound (server / mailbox), Messages (`cMeta(unseen)`), Auth (`cMark("✗ …", sgrRed)` on warning, else `cMark("✓ dkim", sgrGreen)`) | | none | off: one field `Inbound: off` |

## Task 9: `view` becomes piped-only; docs; MR 3c

**Files:** `view.go`, `view_test.go`, `.gitbay/wiki/Users.org`, `CHANGELOG.org`.

- [ ] **Step 1: Check nothing is left.** Run the stage-2 inventory again:

```bash
cd internal/control && grep -lE 'c\.table\(|c\.view\(' *.go | grep -v _test
```

and, for each file, confirm every `c.view(`/`c.table(` sits inside a `plain` closure passed to `emitView`/`emitPageView`, or behind `c.Term.Cols == 0`. List any exception in the MR body.

- [ ] **Step 2: Failing test.**

```go
// view is the piped writer: at a terminal it writes what it writes
// piped, so a caller that never migrated falls back to the plain layout.
func TestViewIgnoresTerminal(t *testing.T) {
	render := func(term Term) string {
		var b bytes.Buffer
		c := &Ctx{Term: term}
		v := c.view(&b)
		v.title("#1", "A title", "open")
		v.fields("author", "alice")
		v.section("files")
		return b.String()
	}
	if got, want := render(Term{Cols: 40, Color: true}), render(Term{}); got != want {
		t.Errorf("terminal view differs from piped:\n%q\n%q", got, want)
	}
}
```

- [ ] **Step 3: Strip `view`'s terminal branches.** In `view.go`: `title`, `section`, `fields`, `body`, `event`, `comment` keep only their `Cols == 0` paths; `opts` returns `termtext.Options{Base: …}` without width or colour; `when` keeps its terminal format only if a screen still calls it (`grep -n 'c.when(' *.go`), otherwise its piped form only. `safe()` calls stay: they are no-ops piped.

- [ ] **Step 4: Run** the package; fix any test that asserted a terminal `view` (each is now a screen test or a piped test).

- [ ] **Step 5: Docs.** Users wiki: replace the "other `show` views" bullet with "every list and show is a screen; `--json` and piped output are unchanged", and drop the per-command lists added in stage 2. CHANGELOG: one line for 3c.

- [ ] **Step 6: Verify, commit `Closes #319`, push `cli-views-3c`, MR** "control: work item and admin screens; view is piped-only (CLI views stage 3c)".

## Self-review notes

- Coverage: the 70 commands from the inventory (25 nouns) appear in tasks 1–8, the eleven `view` callers outside the original list included (`admin mail inbound check`, `repo deps status`, `org show`, `admin stats`, `repo commit`, `org team show`, `emitProfile`, notification settings, `repo settings show`, `admin user show`, `admin runners`); task 9 removes `view`'s terminal branches.
- Shared emitters each get one builder: `editTopics`, `emitProfile`, `emitNotificationSettings`, `emitMilestones`, `runOrgShow`, `runItemQuery`, label lists.
- Rows give exact cells and actions; code is written out in full for the helper and the three patterns (list with shared emitter, show, stream with header). The per-command builders follow those patterns from their table rows; that is a deliberate trade against 70 near-identical code blocks.
- Argument forms marked "check the real arg form" (`repo settings require-mr`) are verified by `checkActions` in the screen test; adjust the argv to whatever passes.
