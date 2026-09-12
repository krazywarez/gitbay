# Web UI/UX sweep

Closes #182. The findings posted on that issue after walking every route in
`internal/httpd/routes.go` on gitbay.org at v1.20.1, anonymous and logged
in, and the rules chosen to fix them.

## Rules

- **Confirmation.** A control that destroys data nothing else holds asks
  the person to type the object's name into a text field beside the
  button; the handler refuses with a flash line when the text differs.
  No JavaScript: the instance CSP is `script-src 'none'`. Covered: release
  delete (the tag), snippet delete (the id), snippet file remove (the file
  name), team delete (the team name), label remove (the label), SSH key
  remove (the 8 characters after `SHA256:` in the fingerprint; a label
  can be empty), email remove (the address), PGP key remove (the first
  8 characters of the fingerprint). Reversible state keeps a
  plain button: close/reopen, merge, protect/unprotect, attach/detach,
  resolve, cancel, make primary, org member remove.
- **Refusal wording.** Control-command messages a web form can trigger
  must not name a flag or a CLI command. The message is rewritten at the
  source, in `internal/control`, since the CLI reads the same text; no
  rewrite layer in the web.
- **Login return-to.** `requireUser` stores the requested local path in a
  short-lived cookie; the login page says where the person is going; the
  emailed-link and `web login` paths both redirect there once and clear it.
  Only a path starting with a single `/` is honoured.
- **Dates.** One absolute format on every page: `2006-01-02 15:04 UTC`
  through the existing `when` helper. Tree and blob listings keep their
  relative time with the absolute one in a `title` attribute.
- **Tags.** Version-aware order on the refs page, newest first; a tag that
  does not parse as a version sorts after the ones that do, by name.
- **Editor.** When the repository requires signed commits, or the ref
  refuses direct pushes, the edit page explains that and shows no form.
- **Repository settings.** Every Save names its field.
- **Empty states.** Sidebars use "none yet" for things and "nobody yet"
  for people; lists keep their sentence and, where a command creates the
  thing, name it.

## Findings and their fixes

1. Destructive controls without confirmation: the rule above, applied to
   `account.html`, `settings.html` (no change: unprotect and detach are
   reversible), `owner.html` (team delete only), `labels.html`,
   `releases.html`, `snippet.html`.
2. Refusals verbatim: audit and rewrite.
3. `account.html` says `gitbay auth token mint`; the command is
   `auth token create`.
4. Login return-to.
5. Four date formats.
6. Refs page sorts tags as strings.
7. Editor offered where it cannot succeed.
8. Twelve unlabelled Save buttons on repository settings.
9. Labels page: colour column shown when no label has a colour; the
   column and the per-row colour form appear only when a label has a
   colour or the viewer can write.
10. Empty-state wording.
11. Issue close-event line reads "closed by commit X by Y"; becomes
    "closed by Y in commit X" with the time inline.
12. Issue rows repeat the state chip under a single-state filter; the
    milestone reads like a label. The chip appears only under "all"; the
    milestone renders as "in <milestone>".
13. Merged MR page: "at" becomes "merged at"; a source branch that no
    longer exists is marked "branch deleted".
14. Global search shows a count and the query. No context line.
15. Repository home shows both clone URLs, HTTPS and SSH.
16. Landing page: "mint a browser session" becomes "log in from your
    terminal". The account page's markup picker already sits beside its
    label; that finding was wrong and nothing changes.
17. Left alone: the anonymous "1 bookmark" stat, since the count is public
    by design (the bookmarks page says so).

## Out of scope

A context line on global search results (a search backend change), the
POST-only routes, `/settings/export`, archives, badges and Atom feeds.
