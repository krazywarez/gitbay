# Web design foundation

Closes #218. A rewrite of the stylesheet around a measured token set, one
control family with authored states, content widths chosen per page, and
four reference pages recomposed: landing, repository overview, merge
request, repository settings. The copy fixes from the September 2026 UI
review ride along. No JavaScript: the instance CSP is `script-src 'none'`
and stays so.

The review's findings came from dark-scheme screenshots. The stylesheet is
light-first with dark through `prefers-color-scheme`. Both schemes are
designed here, dark as the audited one, and every token is measured in
each.

Mockups of the four pages, with the tokens and components below, were
reviewed in the browser before this was written. They are the visual
reference for the implementation, not the templates: the implementation
keeps the template structure and class names where a component survives.

## Rules

- **Two accents, two jobs.** Blue is what you can do: links, primary
  buttons, focus, pressed toggles. Orange is where you are and what wants
  you: the current tab's bar, rail counts, pending state, unverified
  signatures. Orange is never a button ground.
- **Links and fills are different tokens.** `--link` is text on a ground
  and must clear 4.5:1 on canvas, surface and hover. `--fill` is a
  button ground and must carry white at 4.5:1 and stand 3:1 against
  surface. One blue for both fails one of the two jobs in each scheme.
- **Every colour is a token on `:root`**, redefined in the dark media
  query. No colour defined only in the media query. A test parses the
  two blocks out of `style.css` and enforces the floors in the table
  below; the hex values are starting points and the test is the contract.
- **Every class in a template has a selector.** A test parses every
  template for class names and fails on one with no rule in `style.css`.
  Classes that chroma emits and classes tests pin are on an allowlist
  only if they have no styling; today none qualifies.
- **States are authored, never hover-only.** Hover, focus, active,
  pressed, disabled, empty, success and error each have a rule. Essential
  meaning is never in a `title` attribute alone.
- **The product name is lowercase** in templates, docs and the
  CHANGELOG. The instance title is `[web] title` in the daemon's
  config; the Admin wiki page says to set it lowercase.

## Tokens

Colour. Light and dark per token; floors are WCAG contrast ratios.

| Token | Role | Light | Dark | Floor |
| --- | --- | --- | --- | --- |
| `--canvas` | page | `#ffffff` | `#101114` | |
| `--surface` | cards, lists, aside groups | `#f6f7f9` | `#17191d` | distinct from canvas: luminance ratio at least 1.15 |
| `--inset` | inputs, code, terminal blocks | `#ffffff` | `#0b0c0e` | distinct from surface |
| `--hover` | row and control hover | `#eef0f3` | `#1e2126` | |
| `--line` | control and card borders | `#d0d5db` | `#2f343b` | 3:1 against canvas |
| `--faint` | row separators | `#e6e9ed` | `#22252b` | |
| `--fg` | text | `#1c1f24` | `#e9eaec` | 7:1 on canvas and surface |
| `--muted` | metadata | `#5a6270` | `#a3a9b3` | 4.5:1 on canvas and surface |
| `--link` | text links | `#0033d6` | `#8b9bff` | 4.5:1 on canvas, surface, hover |
| `--fill` | primary button ground | `#0000f0` | `#3b4fe8` | white on it 4.5:1; 3:1 against surface |
| `--mark` | current-tab bar, counts | `#e4572e` | `#ff6b3d` | 3:1 non-text |
| `--warn` | the same hue at text size | `#b8400f` | `#ff8a63` | 4.5:1 |
| `--ok` | success | `#0a7d3c` | `#3fce7a` | 4.5:1 on canvas and surface |
| `--bad` | failure, destructive | `#c62828` | `#ff6b6b` | 4.5:1 on canvas and surface |
| `--done` | merged | `#6b21a8` | `#c084fc` | 4.5:1 on canvas and surface |
| `--neutral` | closed, draft, unknown | `--muted` | `--muted` | as muted |
| `--focus` | ring | `--link` | `--link` | 3:1 against canvas |

The rail keeps `--shell-*`: black ground and its own text, muted, line,
hover and mark values in both schemes. The dark values above are neutral
greys so the pane and the black rail read as two grounds.

The syntax palettes and the diff tints stay as they are; the existing
`TestSyntaxPaletteContrast` pins them. The chroma grounds it measures
against become `--inset` and the diff tints.

Type. Base 16px. Atkinson Hyperlegible Next and Mono, self-hosted, as
now. Sizes `--fs-0` to `--fs-6`: 12, 13, 14, 16, 20, 24, 32px. Weights
400, 500, 600 only. Body line-height 1.5, headings 1.25. Metadata that
carries a decision, such as a branch, a timestamp or a check result, is
never below 13px. Monospace is for commands, hashes, paths, refs and
code; rail labels, dates and section labels use the sans face.

Spacing. `--sp-1` to `--sp-7`: 4, 8, 12, 16, 24, 32, 48px. Between
sections 32, within a group 8 or 12, page padding 24 by 32.

Radius. 4px on controls and chips, 6px on cards, lists and code blocks.
One shadow, on the ref menu dropdown only.

## Components

Buttons. All 32px tall at 14px, 500 weight, same horizontal padding, so
they align in a row. Four treatments:

- Primary: `--fill` ground, white text. One per form group, the action
  the page exists for: Create account, Comment, Merge, the identity
  form's Save.
- Secondary (`.btn`): canvas ground, `--line` border, `--fg` text, hover
  to `--hover`. Pin, Watch, Bookmark, Fork, Filter, per-field Save.
- Quiet (`.linklike`): no ground or border, `--link` text, underline on
  hover. Inline actions: clear, remove, make primary.
- Destructive (`.danger`): canvas ground, `--bad` border and text; hover
  fills `--bad` with white text. Delete, close without merging,
  unprotect, archive. Beside the typed confirmation field where one
  exists today.

Toggles keep `aria-pressed`. Pressed: `--surface` ground, `--link` border
and text, label in the participle as now.

Links: `--link`, underline on hover; inside rendered prose always
underlined. Row titles in lists stay `--fg` and turn `--link` on hover.

Focus: one ring, 2px `--focus` with 2px offset, on every focusable
element including inputs. The rail uses `--shell-mark`.

Chips: 4px radius, 12px, 500 weight, 1px border at 40% of the state
colour, 10% tint ground. Variants as now; topic chips use `--link`.

Tabs: current tab `--fg` at 600 with the 2px `--mark` bar. Counts in
tabs are plain `--muted` text. The rail's count keeps the orange ground.

Forms: inputs on `--inset`, `--line` border, 32px tall, 14px. Label above
the field at 500 weight; the hint in `--muted` under the label. Checkbox
16px, label to its right on the same line. `::placeholder` in `--muted`.

Feedback: `.error` and `.notice` become one `.flash` family with a
`role`, a 3px left edge in `--bad`, `--ok` or `--warn`, a 6% tint ground
and `--fg` text. `class="error"` stays on the error variant, since e2e
tests pin it.

Containers: one recipe for lists, tables and cards: `--surface` ground,
`--line` border, 6px radius, `--faint` row separators, `--hover` row
hover. Table headers 13px, 500, `--muted`. A card is a bounded group with
a header: README, a comment, an aside group. Bordered boxes around whole
page regions go.

Code and diffs: `--inset` ground, existing gutters and chroma classes.

## Shell and layout

Rail: 14rem, black, same sections. Labels sans, 12px, uppercase, tracked.
Under 52rem it becomes the top strip it is today, search kept, pinned
group hidden.

Content width: a class on `main`, set from the page struct, one of:

- `wide`, no maximum: tree, blob, blame, log, commit and MR diffs,
  builds, code search.
- `reading`, 72rem: dashboard, issue and MR lists and pages, explore,
  profile, releases, wiki, notifications, bookmarks, snippets. The
  default.
- `bounded`, 48rem: landing, login, register, new repository, new issue,
  new MR, repository settings, account settings, admin.

Two-column pages (issue, MR, dashboard): the aside is 18rem, the gap
32px, its top aligned with the first content block below the page
header. Prose in the conversation column is capped at 78ch. Under 62rem
the aside stacks above the conversation. The dashboard's pinned group
goes; the rail has it.

Repository header: two levels. The identity row holds owner/name, the
visibility and archived chips, and the toggles at the right. On the code
tab it also shows the description with the topic chips on the same line,
one `--muted` line for website and mirrors, and for a signed-in viewer
one `--muted` line explaining Pin, Watch and Bookmark. On every other tab
only the identity row and the tab bar render. The header is identical on
every page within a tab.

Page header: `h1` at 24px, page controls on the same row at the right,
24px before content. Filters are secondary buttons in a group with the
active one pressed, each a GET link; a text filter has an explicit Search
button and a result count line above the list.

Responsive: the four reference pages are checked at 375px, 768px, and
1280px at 200% zoom. Tables under 40rem drop the last-commit column and
scroll inside their wrapper. Clone commands are two labelled code blocks
that wrap.

## Reference pages

Landing, `bounded`. Order: mark and name as the title; proposition;
quickstart; picture; three facets; routes; sign-in line. Copy:

- Proposition: "A git forge you drive from the terminal. Repositories,
  issues, merge requests and CI over SSH, with a fast, readable web view
  of the same state."
- Quickstart: `ssh git@{host} help` with the comment "every command, no
  client to install", and `git clone ssh://git@{host}/owner/repo.git`.
- Picture: a `<picture>` with a dark source under
  `(prefers-color-scheme: dark)` and a light `img`, of the krz/gitbay
  merge request page at 1280 wide, captured from gitbay.org after the
  redesign deploys. Files under `internal/web/static/img/`, routes
  generated from the directory as fonts are, served with the same cache
  headers as the stylesheet.
- Facets. Read: "Browse and clone any public repository over HTTPS or
  git://, no account. Every commit shows whether its signature
  verified." Write: "Push over SSH with the key you already have. Create
  a repository, file an issue, open and merge a request, all as
  commands." Review: "Read a diff, comment on a line, approve, merge, in
  the browser or the terminal."
- Routes: primary "Explore repositories"; secondary "Create an account"
  when registration is open.
- Sign-in line, `--muted`: "Have an account? Sign in with an emailed
  link, or run `ssh git@{host} web login`." When email login is off the
  first clause is dropped.

Register, `bounded`. Labels above fields, one primary button. Key field
label "SSH public key", hint "Paste the contents of your public key
file, usually ~/.ssh/id_ed25519.pub. It starts with ssh-ed25519 or
ssh-rsa." and a link to the wiki page `SSH-keys`, new, which shows
`ssh-keygen -t ed25519` and where the public half lands. The terminal
alternative stays as one line at the bottom.

Repository overview, `wide`. Under the header: the path bar with the ref
menu, crumbs, and at the right Search code, History, Download as
secondary buttons. The file table with the tip commit as its first row.
The README card. Then, at reading width, two columns: Clone on the left
as two labelled blocks, SSH first; About on the right with commits,
branches, tags, contributors, licence, latest release, build state in a
four-column grid, then the language bar with its legend. The Find file
link becomes Search code, since the route searches contents.

Merge request, `reading`. Title row: title, number in `--muted`. Next
line: state chip, then "opened by X on DATE", "merged by X on DATE" or
"closed without merging by X on DATE", then source into target in code.
Subtabs as now with plain counts. Aside order: Merge, Checks, Reviews,
Reviewers, Source and target, Milestone. The diff view's empty case
renders "No changes between the source and target." followed, when the
head is an ancestor of the target, by "The source branch was already
merged or fast-forwarded into TARGET." A related-request link needs a
data model and is not in this spec.

Repository settings, `bounded`. A flash at the top after a save, "Saved
the FIELD.", rendered on the redirected GET through the existing flash
cookie; an error renders the flash error variant and the field keeps the
submitted value. A row of anchor links to the sections. Each section an
`h2` and a stack of rows. A row is a grid: label column 14rem with the
hint beneath the label, control column bounded at 28rem, Save at the
end. Every control with a consequence carries one hint sentence:
visibility, git:// serving, default branch, each merge gate, require-MR,
archive. The identity form's Save is primary; every other Save is
secondary; Unprotect and Archive are destructive. Topics become one
prefilled text field, comma separated.

## Copy

Buttons and headings in sentence case. "Sign in" and "Log out". "Search"
as the rail field's placeholder and every search button's label. The
homepage no longer calls `repo create` the whole onboarding. The wiki's
Users page records the vocabulary.

## Landing in order

Four merge requests onto `main`, each green on bay1 before merging, each
referencing #218:

1. Stylesheet rewrite: tokens, components, shell, and every template
   adjusted where a component's markup changes. The contrast test and
   the class-coverage test land here. Every page family screenshotted at
   1280 and 375 in both schemes before merge.
2. Layout and pages: the `main` width class, the two-level repository
   header, and the four reference pages. An e2e test posts a settings
   form and checks the flash and the retained value on error.
3. Copy and the `SSH-keys` wiki page. Rides with 2 if the diff stays
   readable.
4. Screenshots, captured after 2 deploys, committed with their routes
   and the landing `<picture>`.

Release v1.23.0. The CHANGELOG and the Admin wiki page note the
lowercase title.

## Verification

Before each merge: build, vet, the unit tests of `internal/httpd` and
`internal/web`, and the e2e tests that pin class names. Then in the
browser: a keyboard walk of the four reference pages, a 200% zoom pass,
and a comparison against the mockups.

## Not in this spec

Triage and builds (D01, D02, D04, D05, M01, M04), profiles (P01 to
P03), and the benchmark (B08). Each is filed as its own issue.
