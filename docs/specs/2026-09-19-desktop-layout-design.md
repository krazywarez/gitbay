# Desktop layout

Ref #226. The phone layout landed in v1.23.0 through v1.29.0; this is
the desktop half. Measured on gitbay.org at 1a40b22 (v1.30.0 head) at
1600 and 1920 wide, and compared against GitHub, GitLab, Codeberg and
SourceHut dashboards at the same window.

## Findings

Three content widths, all pinned to the left edge. Nothing centers and
nothing grows past its cap, so a wide screen puts every page in its
top-left corner.

| Width class | Pages | Content edge at 1920 | Used |
|---|---|---|---|
| `reading` (72rem, the default) | dashboard, issue and MR lists, explore, notifications, profile, refs, releases, milestones, labels | 1216px | 63% |
| `reading`, then 48rem comments + 18rem aside | issue, MR conversation | 1184px | 62% |
| `bounded` (48rem) | account settings, admin, new repository, login, 404 | 640px | 33% |
| `wide` | tree, blob, blame, log, commit, compare, diff, builds, build, search | 1920px | 100% |

- Text columns are at the right measure. A comment at 48rem is about 90
  characters a line; wider hurts. Issue and MR pages need centering and
  a stronger aside, not wider text.
- Lists are laid out as prose. Issue, MR, build and explore rows are two
  to four lines inside the 72rem cap: 14 issues a screen at 1080 tall,
  9 repositories on explore.
- The dashboard is one column. Queues stack, the feed follows at 809px,
  below the fold at 1080 tall.
- The repository header is 182px on the code tab (identity, description,
  website, the toggles hint, tabs) and 110px on task tabs.
- Account settings at 48rem wraps SSH fingerprints onto two lines beside
  1280px of unused width.
- The overview README card is 1216px wide while its prose caps at 78ch,
  so paragraphs and code blocks have different right edges.

SourceHut uses the same share of the window as gitbay and reads as
designed, because it centers and splits into two columns. GitLab's stat
tiles are gitbay's three "0" lines done as a strip. Codeberg's one-line
feed rows are the row format the lists should adopt.

## Rules

1. **One centered container.** `--container: 100rem`. The repository
   header's inner content, `main` and the footer share it, so all three
   align at every width. `reading` (72rem) and `bounded` (48rem) stay as
   caps inside it, centered. `wide` is the container.
2. **A left column is a feature.** A page gets a left column only when
   the column carries something the page already has as links, query
   parameters or sections. No site navigation in it; the top bar is the
   navigation. Every column is 15rem, sticky, and reads as one system.
   No column persists across pages.
3. **Text keeps its measure.** Comments, descriptions, README prose and
   wiki pages stay at 48rem or 78ch. Width goes to tables and lists.
4. **Lists are rows.** Above 64rem an issue, MR, build, explore, search
   and notification row is one line: title, then labels, then the meta
   pushed right, state or check badges at the far edge. The log keeps
   two lines; subject over author and date is the convention there.
5. **Counts are tiles.** Where the dashboard now prints an empty queue
   as a heading with a zero, a strip of four tiles carries the counts
   and links to the lists. A non-zero count uses `--warn`: what wants
   you. Only non-empty queues list rows.
6. **The repository header is two rows.** Row one: owner/name, the
   description with topics and website inline in muted text, the
   Pin/Watch/Bookmark/Fork buttons at the right. Row two: the tabs. The
   toggles hint moves to `title` text on the three buttons.
7. **Below 62rem** a facet or section column stacks after the content,
   the way the issue aside does; the file navigator disappears (the
   tree page exists). **Below 80rem** the dashboard's pinned column
   returns to the chip row. Nothing the phone layout fixed moves.

## Pages

### With a left column

| Page | Column | Source of its contents |
|---|---|---|
| Dashboard | Pinned repositories with open issue count, open MR count, last build state | `Rail.Pinned` plus per-repository counts (new store read) |
| Blob, blame, edit | File navigator: the file's directory, parent link, current file marked with `aria-current` | `gitutil.ListTree` on the directory, the call the tree page makes |
| Issues list, MR list | State; labels with counts; milestones with open counts | The `state`, `label`, `milestone`, `assignee` parameters `activeFilters` already handles; counts from the labels and milestones pages' reads |
| Builds list | Status; job; branch | The `status`, `job`, `ref` parameters `buildFilter` already handles; branches from the refs read |
| Explore | Topics with counts | `?q=<topic>`, the link the topic chips already use |
| Site search | Kind: repositories, issues, merge requests | `?kind=repo\|issue\|mr` |
| Repository settings | Section nav: Identity, Access, Merge gates, Protected branches, Protected tags, Dependencies, Runners, Lifecycle | Anchors on the existing `h2` headings |
| Account settings | Section nav: Profile, SSH keys, Email addresses, OpenPGP keys, Notifications, Appearance, Export | Same |
| Admin | Section nav: Webhook deliveries, Mail, Mirrors, Builds, Dependency checks | Same |
| Wiki | Page nav | Already there; adopt the 15rem width |

Facet links carry the other active filters, the way `activeFilters`
builds its clear links. The milestones and labels pages stay for
management; the "milestones · labels" links leave the list head.

### Layout of the pages that gain a column

- **Dashboard**: `15rem | 1fr | 20rem`. Pinned column, then the tile
  strip and queue rows, then the activity feed as a sticky aside with
  two-line entries (the aside is too narrow for one).
- **Blob, blame, edit**: `15rem | 1fr`. Navigator entries in mono, the
  current file on a `--surface` ground with a 2px `--mark` edge.
- **Lists**: `15rem | 1fr`. Rows fill the container.
- **Settings and admin**: `15rem | minmax(0, 56rem)`. Forms and tables
  at up to 56rem, so the key table stops wrapping.

### Without one

- **Repository overview**: the facts column stays on the right at
  20rem; the README card caps at 88ch so prose and code share an edge.
- **Issue, MR**: centered at 72rem; text at 48rem; the aside widens to
  20rem so the two sit balanced in the container.
- **Log, commit, compare, refs, releases, build, code search**: the
  container width, no column.
- **Notifications**: the container width, no column; rows one line.
- **Bookmarks, snippets, profile, org**: centered `reading`.
- **Landing, login, register, new repository, 404, privacy**: centered
  `bounded`.

## Implementation

- **CSS only where it can be.** The container, centering, header rows,
  row format, tiles, column grid and breakpoints are `style.css` rules
  on tokens that exist. New tokens: `--container` only.
- **Templates** touched: `layout.html` (header rows, container wrapper),
  `dashboard.html`, `issues.html`, `mrs.html`, `builds.html`,
  `explore.html`, `globalsearch.html`, `blob.html`, `blame.html`,
  `edit.html`, `settings.html`, `account.html`, `admin.html`,
  `notifications.html`, `tree.html` (README cap). Width classes: the
  list pages and the dashboard move from the default `reading` to
  `wide`.
- **Handlers**: `blob`, `blame` and the edit form gain the directory
  listing; the issue, MR and build list handlers gain facet counts; the
  dashboard gains per-pinned-repository counts. Every new read is a
  store query or a `gitutil` call that exists; no new control command,
  since none of this is a capability the CLI lacks.
  `TestReadOnlyCommandsWriteNothing` is unaffected.
- **Facet counts** span only what the viewer can read, the same rule
  `control.ReadableOrgRepoIDs` applies to org label counts.

## Tests

- `TestEveryTemplateClassHasARule` covers every new class.
- A template test asserts the width class of each list page and the
  dashboard is `wide`, and that `layout.html` wraps the header in the
  container.
- e2e: the blob page's navigator lists the file's directory and marks
  the file; a facet link on the issues list keeps `state` and the other
  active filters; the dashboard's tile count equals the queue length.
- Recapture the `.claude/screenshots` set at 1280 and 1920 and rerun the
  axe scan; zero violations is the bar the last release set.

## Out of scope

- A persistent context rail (option 2 in the mockups). Revisit if the
  dashboard's pinned column proves worth having on every page.
- A density preference.
- One-line log rows.
- Explore facets beyond topics; an `owner` parameter would be new.

## Mockups

`.claude/mock/desktop/` in the main checkout, served by the `mockups`
entry in `.claude/launch.json`: `option1-dashboard.html`,
`option1-blob.html`, the rejected `option2-*` pair, and `layouts.html`
with one wireframe per page type. `desktop.css` there is the override
layer the rules above were checked against.
