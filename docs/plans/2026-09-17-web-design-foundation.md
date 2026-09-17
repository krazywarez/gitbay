# Web design foundation implementation plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the stylesheet with a measured token set and one control family, give pages content widths, and recompose the landing, repository overview, merge request and repository settings pages, with the review's copy fixes.

**Architecture:** One embedded stylesheet (`internal/web/static/style.css`) styles server-rendered Go templates under `internal/web/templates/`, each parsed together with `layout.html`. Two Go tests pin the stylesheet: a token contrast test and a template-class coverage test. Handlers in `internal/httpd` change only where a page needs new data (the MR empty-diff flag, the settings success flash, the prefilled topics field, image routes).

**Tech Stack:** Go 1.2x, `html/template`, hand-written CSS, no JavaScript (CSP `script-src 'none'`), e2e tests against a real `gitbayd`.

**Spec:** `docs/specs/2026-09-17-web-design-foundation-design.md`. Mockups of the four pages with the target tokens: `.claude/mock/` (serve with the `mockups` entry in `.claude/launch.json`, or `python3 -m http.server --directory .claude/mock`). Before-screenshots of every page: `.claude/screenshots/before/`, and `.claude/screenshots/README.md` says how to capture the after set.

## Global constraints

- Tracking issue is #218; every commit message ends with `Ref #218` (the last commit of MR 4 says `Closes #218`).
- Never push to `main`; each MR is a branch off `main`, merged with `gitbay mr merge <n> --strategy ff` after bay1 CI is green. Commits are signed (the repository requires it).
- No attribution to any assistant or model anywhere.
- No JavaScript, no inline `<style>` blocks, no external requests. `style="width:..%"` on the language bar and label colours are the only inline styles and stay.
- Product name lowercase, `gitbay`, in templates, docs and the CHANGELOG.
- Colours exist only as tokens on `:root`, each redefined in `@media (prefers-color-scheme: dark)`.
- Class names that tests pin stay: `error`, `notice`, `railuser`, `actgraph`, `xref`, `blamehunk`, `lineno`, `code`, `syscomment`, `add`, `del`, `difftable`, `chroma`, `authorlink`, `refmenu`, `thread`, `difffold`, `ln`, `src`, `contribs`, `activity`, `line`.
- Locally: `go build ./... && go vet ./...`, unit tests of touched packages, and at most the one e2e test being written. The full suite runs on bay1.
- Copy is sentence case; "Sign in" / "Log out"; "Search" for search buttons and the rail placeholder.

---

## MR 1: stylesheet rewrite

Branch `design-stylesheet` off `main`. Tasks 1 to 6.

### Task 1: token contrast test

**Files:**
- Create: `internal/web/tokens_test.go`

**Interfaces:**
- Produces: `parseTokens(css []byte) (light, dark map[string]string)` and `contrastHex(a, b string) float64`, package `web`, reused by nothing else but named here so Task 2 does not redefine them.

- [ ] **Step 1: Write the failing test**

```go
package web

import (
	"math"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// parseTokens returns the --name: value pairs of the light :root block
// and of the :root block inside the dark media query. A value that is
// itself a var() is resolved one level deep within the same scheme.
func parseTokens(css []byte) (light, dark map[string]string) {
	s := string(css)
	rootRe := regexp.MustCompile(`(?s):root\s*\{(.*?)\}`)
	darkIdx := strings.Index(s, "@media (prefers-color-scheme: dark)")
	if darkIdx < 0 {
		return nil, nil
	}
	parse := func(block string) map[string]string {
		m := map[string]string{}
		lineRe := regexp.MustCompile(`--([a-z0-9-]+)\s*:\s*([^;]+);`)
		for _, mm := range lineRe.FindAllStringSubmatch(block, -1) {
			v := strings.TrimSpace(mm[2])
			if i := strings.Index(v, "/*"); i >= 0 {
				v = strings.TrimSpace(v[:i])
			}
			m[mm[1]] = v
		}
		for k, v := range m {
			if strings.HasPrefix(v, "var(--") {
				ref := strings.TrimSuffix(strings.TrimPrefix(v, "var(--"), ")")
				if rv, ok := m[ref]; ok {
					m[k] = rv
				}
			}
		}
		return m
	}
	lm := rootRe.FindStringSubmatch(s[:darkIdx])
	dm := rootRe.FindStringSubmatch(s[darkIdx:])
	if lm == nil || dm == nil {
		return nil, nil
	}
	light = parse(lm[1])
	dark = parse(dm[1])
	for k, v := range light {
		if _, ok := dark[k]; !ok && strings.HasPrefix(v, "#") {
			dark[k] = v // a light-only colour is a bug; keep it visible below
		}
	}
	return light, dark
}

func hexChannel(h string) float64 {
	n, _ := strconv.ParseUint(h, 16, 8)
	c := float64(n) / 255
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

func luminanceHex(hex string) float64 {
	hex = strings.TrimPrefix(hex, "#")
	if len(hex) == 3 {
		hex = string([]byte{hex[0], hex[0], hex[1], hex[1], hex[2], hex[2]})
	}
	return 0.2126*hexChannel(hex[0:2]) + 0.7152*hexChannel(hex[2:4]) + 0.0722*hexChannel(hex[4:6])
}

func contrastHex(a, b string) float64 {
	la, lb := luminanceHex(a), luminanceHex(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// TestTokenContrast is the contract for the colour tokens in style.css:
// every text colour clears WCAG AA on every ground it lands on, in both
// schemes. The hex values in the stylesheet are free to move as long as
// this passes.
func TestTokenContrast(t *testing.T) {
	light, dark := parseTokens(StyleCSS)
	if light == nil || dark == nil {
		t.Fatal("could not find the light and dark :root blocks in style.css")
	}
	type check struct {
		fg, bg string
		floor  float64
	}
	checks := []check{
		{"fg", "canvas", 7}, {"fg", "surface", 7},
		{"muted", "canvas", 4.5}, {"muted", "surface", 4.5},
		{"link", "canvas", 4.5}, {"link", "surface", 4.5}, {"link", "hover", 4.5},
		{"fill-fg", "fill", 4.5}, {"fill", "surface", 3},
		{"line", "canvas", 3},
		{"mark", "canvas", 3},
		{"warn", "canvas", 4.5}, {"warn", "surface", 4.5},
		{"ok", "canvas", 4.5}, {"ok", "surface", 4.5},
		{"bad", "canvas", 4.5}, {"bad", "surface", 4.5},
		{"done", "canvas", 4.5}, {"done", "surface", 4.5},
		{"focus", "canvas", 3},
		{"shell-fg", "shell-bg", 7}, {"shell-muted", "shell-bg", 4.5}, {"shell-mark", "shell-bg", 3},
	}
	for name, scheme := range map[string]map[string]string{"light": light, "dark": dark} {
		for _, tok := range []string{"canvas", "surface", "inset", "hover", "line", "faint", "fg", "muted", "link", "fill", "fill-fg", "mark", "warn", "ok", "bad", "done", "neutral", "focus", "shell-bg", "shell-fg", "shell-muted", "shell-mark"} {
			v, ok := scheme[tok]
			if !ok || !strings.HasPrefix(v, "#") {
				t.Errorf("%s: --%s is missing or not a hex colour (%q)", name, tok, v)
			}
		}
		if t.Failed() {
			continue
		}
		for _, c := range checks {
			if got := contrastHex(scheme[c.fg], scheme[c.bg]); got < c.floor {
				t.Errorf("%s: --%s (%s) on --%s (%s) is %.2f:1, want >= %.1f", name, c.fg, scheme[c.fg], c.bg, scheme[c.bg], got, c.floor)
			}
		}
		// The surface ladder must be visible: canvas, surface and inset
		// are three grounds, not one.
		lc, ls, li := luminanceHex(scheme["canvas"]), luminanceHex(scheme["surface"]), luminanceHex(scheme["inset"])
		if r := (math.Max(lc, ls) + 0.05) / (math.Min(lc, ls) + 0.05); r < 1.15 {
			t.Errorf("%s: canvas %s and surface %s are %.2f apart, want >= 1.15", name, scheme["canvas"], scheme["surface"], r)
		}
		if scheme["inset"] == scheme["surface"] {
			t.Errorf("%s: inset and surface are the same colour %s", name, scheme["inset"])
		}
	}
}
```

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/web -run TestTokenContrast -v`
Expected: FAIL with lines like `light: --canvas is missing or not a hex colour ("")`, because the current stylesheet has `--bg`, not `--canvas`.

- [ ] **Step 3: Commit the test alone**

```bash
git add internal/web/tokens_test.go
git commit -m "web: token contrast test

Ref #218"
```

The test stays red until Task 3.

### Task 2: template class coverage test

**Files:**
- Create: `internal/web/classes_test.go`

- [ ] **Step 1: Write the test**

```go
package web

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// TestEveryTemplateClassHasARule fails on a class a template uses that
// no selector in style.css mentions. Class tokens containing template
// actions ({{...}}) are composed at render time and skipped; their
// prefixes (chip-, badge-, check-, lang-) are covered by the rules for
// the concrete values.
func TestEveryTemplateClassHasARule(t *testing.T) {
	css := string(StyleCSS)
	// Every ".name" that appears in a selector position: outside braces.
	depth := 0
	var sel strings.Builder
	for _, r := range css {
		switch r {
		case '{':
			depth++
		case '}':
			depth--
		default:
			if depth == 0 {
				sel.WriteRune(r)
			}
		}
	}
	// Media queries wrap rules one level deeper; strip their headers and
	// scan again at depth one.
	depth = 0
	inMedia := false
	for i := 0; i < len(css); i++ {
		if strings.HasPrefix(css[i:], "@media") {
			inMedia = true
		}
		switch css[i] {
		case '{':
			depth++
			if depth == 1 && !inMedia {
				// ordinary rule; already scanned above
			}
		case '}':
			depth--
			if depth == 0 {
				inMedia = false
			}
		default:
			if inMedia && depth == 1 {
				sel.WriteByte(css[i])
			}
		}
	}
	classRe := regexp.MustCompile(`\.([a-zA-Z_][a-zA-Z0-9_-]*)`)
	styled := map[string]bool{}
	for _, m := range classRe.FindAllStringSubmatch(sel.String(), -1) {
		styled[m[1]] = true
	}

	attrRe := regexp.MustCompile(`class="([^"]*)"`)
	missing := map[string][]string{}
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		src, err := templateFS.ReadFile("templates/" + e.Name())
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range attrRe.FindAllStringSubmatch(string(src), -1) {
			for _, c := range strings.Fields(m[1]) {
				if strings.Contains(c, "{{") || strings.Contains(c, "}}") {
					continue
				}
				if !styled[c] {
					missing[c] = append(missing[c], e.Name())
				}
			}
		}
	}
	var names []string
	for c := range missing {
		names = append(names, c)
	}
	sort.Strings(names)
	for _, c := range names {
		t.Errorf("class %q in %s has no rule in style.css", c, strings.Join(uniq(missing[c]), ", "))
	}
}

func uniq(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
```

- [ ] **Step 2: Run it against the current stylesheet**

Run: `go test ./internal/web -run TestEveryTemplateClassHasARule -v`
Expected: either PASS, or a list of classes the old stylesheet never styled. Record the list in the commit message. Any class listed that is purely a hook with no visual meaning is removed from the template in Task 4; every other one gets a rule in Task 3. Do not add an allowlist.

- [ ] **Step 3: Commit**

```bash
git add internal/web/classes_test.go
git commit -m "web: every template class has a stylesheet rule

Ref #218"
```

### Task 3: write the new stylesheet

**Files:**
- Modify: `internal/web/static/style.css` (replace whole file)
- Reference: `.claude/mock/mock.css` (the token block and the components for the four mocked pages), the class inventory below.

**Interfaces:**
- Produces: the token names in Task 1; the classes `primary`, `btn`, `linklike`, `danger` on buttons; `wide`, `reading`, `bounded` on `main`; `flash` is not introduced (see below).

- [ ] **Step 1: Start from the mock stylesheet**

Copy `.claude/mock/mock.css` over `internal/web/static/style.css`, keep the three `@font-face` blocks from the old file at the top (the mock falls back to system fonts), and replace the header comment with:

```css
/* gitbay stylesheet. Tokens first, on :root and again in the dark media
   query; components use tokens only. Two accents with separate jobs:
   blue is what you can do (links, primary buttons, focus, pressed
   toggles), orange is where you are and what wants you (current tab,
   rail counts, pending state, unverified signatures). Orange is never a
   button ground. internal/web/tokens_test.go measures every token;
   internal/web/classes_test.go checks every template class has a rule.
   No external requests: fonts are served from this origin. */
```

Change `html { font-size: 16px; }` to stay, remove the `.mono` and `.small` helper classes unless a template uses them after Task 4, and change `--fill-fg` to be defined in both scheme blocks.

- [ ] **Step 2: Port every component the mock did not cover**

Work through the old stylesheet from line 150 to the end, and for each selector group write the new rule with tokens. The inventory, with the treatment each gets:

- Focus: `:focus-visible { outline: 2px solid var(--focus); outline-offset: 2px; }` on everything, `.rail :focus-visible { outline-color: var(--shell-mark); }`. Remove the old `input:focus { border-color }` rule.
- Skip link `.skip`: as before, on `--fill` with `--fill-fg` when focused.
- Rail: `.rail`, `.brand`, `.mark`, `.railbody`, `.railsearch`, `.raillist` (+ `.wide`, `.railpinned`), `.raillabel`, `.railgroup`, `.count`, `.railfoot`, `.railuser`, `.avatar`, `.uname`, `.owner`. Values from the mock. The 52rem breakpoint keeps the old strip behaviour: `.shell` block, rail `flex-direction: row`, `border-bottom` instead of `border-right`, `.railgroup { display: none }`, `.raillist { display: flex }` with the current marker as a bottom border.
- Shell: `.shell`, `.pane`, `main.content` with `padding: var(--sp-5) var(--sp-6) var(--sp-7)`, and the three width classes `main.wide { max-width: none }`, `main.reading { max-width: calc(72rem + 2 * var(--sp-6)) }`, `main.bounded { max-width: calc(48rem + 2 * var(--sp-6)) }`. `footer` as in the mock.
- Repository header: `.repohead`, `.identity`, `.repotitle` (+ `.owner`, `.sep`), `.grow`, `.chip` beside the title, `.repodesc`, `.topics`, `.repometa`, `.toggles`, `nav.tabs a` (+ `[aria-current]`, `i`). From the mock.
- Page heads: `h1` 24px, `.pagehead`, `.listhead` (the h1 + filters row on issues, MRs, builds, explore): `display: flex; align-items: center; gap: var(--sp-3); flex-wrap: wrap; margin-bottom: var(--sp-5)`. `nav.filters a`: secondary button geometry, `.active` gets the pressed treatment (`--surface` ground, `--link` border and text). `form.searchform`: input plus a secondary "Search" button, `gap: var(--sp-2)`. `.logfilter`, `.compareform`: same.
- Buttons: `button` and `.button` base (32px, 14px, 500, 4px radius); `button.primary`, `button.btn` (+ `[aria-pressed="true"]`), `button.linklike`, `button.danger`; `.btngroup`; `.inline` forms `display: inline`. `button:disabled { opacity: .55; cursor: default }`. A bare `<button>` with no class renders as primary so untouched templates keep working; Task 4 adds `btn` or `danger` where the spec says.
- Forms: inputs, `textarea`, `select` (with the chevron `--chev` data URI kept, colours swapped to `--muted`), `label`, `.hint`, `.field`, `.check`, custom checkbox 16px drawn as before with `--fill` when checked, `::placeholder`, `::selection`, `input:user-invalid { border-color: var(--bad) }`, `fieldset.segmented` (checked span on `--fill`), `.branchpick`, `.options`, `.editform`, `.commentform` (textarea plus a row: primary submit, quiet cancel), `details.editbox` (summary styled as a secondary button, body a card), `.confirmfield` input (no class today; it is `input[name=confirm]`).
- Settings: `form.setform` becomes the grid from the mock's `.setrow` but keeps the name `setform`: `grid-template-columns: 14rem minmax(0, 28rem) auto`, `align-items: start`, `padding: var(--sp-3) 0`, `border-bottom: 1px solid var(--faint)`; `.setform label { margin-top: 6px }`, `.setform .hint`, `.setform.stack { grid-template-columns: 1fr }`, under 40rem one column. `nav.sections` for the anchor links. `ul.protlist` rows as list rows with the action at the right.
- Messages: `.error` and `.notice` share one rule: 3px left edge, 6% tint ground, `--fg` text, 4px right radii, `margin-bottom: var(--sp-4)`, 14px; `.error` edge and tint `--bad`, `.notice` edge and tint `--ok`. No `.flash` class is added: the spec's "flash family" is these two selectors, and `class="error"` keeps its name because e2e tests match it. `.empty-note`, `p.none`, `.empty` in `--muted`.
- Containers: `.card`, `.cardhead`, `.cardbody`, `ul.list`; `table`, `th`, `td`, `tr.cols`, `.tablewrap`; `table.tree` (+ `td.name`, `.dir`, `td.lastcommit`, `td.age`), `table.refs`, `table.keys` (flush: no outer border, header without ground), `.tipbar` as the first row on `--surface`, `.readme`, `.code`, `.bigcode`, `ul.loglist` (+ `.commitmain`, `.commitside`, `.subject`, `.sha`, `.when`), `ul.repolist` (+ `.reponame`, `.desc`, `.meta`), `ul.issuelist` (+ `.issuemain`, `.title`, `.meta`), `ul.milestonelist` (+ `.msmain`, `.progress`, `.bar`), `article.release` (+ `.releasehead`, `.assets`), `ul.pinlist`, `.matchlist`, `.matchpath`, `.matchline`, `.pager`, `.snippetfile`, `.filefacts`, `.dmeta`, `.notfound`, `.buildlog`. All on the one container recipe; rows `--faint` separators, `--hover` hover; the row title `--fg` turning `--link` on hover.
- Two-column: `.withaside`, `.mainside`, `aside.aside`, `.grp`, `.row`, `.sub`, `.dot` (+ `.ok`, `.bad`, `.pend`), `form.actions` (buttons wrap, `select` full width), `.prose { max-width: 78ch }`. Under 62rem: one column with `.aside { order: -1 }`.
- Comments and threads: `article.comment`, `.commenthead`, `.commentbody`/`.rendered` inside it, `.syscomment`, `.thread` (+ `.resolved`, `.pending`, `.stale`, `.composing`, `.threadrow`, `.threadstate`, `.threadact`, `.threadreply`), `nav.subtabs` (+ `[aria-current]`, `i`).
- Diffs and code: `.diffstat` (+ `.add`, `.del`), `details.difffold` (+ `summary`, `.fpath`, `.fstat`), `table.difftable` (+ `td.ln`, `td.src`, `tr.add`, `tr.del`, `.hunk`, `.cmt`), `.chroma` container, `.blame`, `.blameinfo`, `.blamehunk`, `.blamecode`, `.lineno`, `.blobimage`, `.plain`, `.fullsha`. Grounds `--inset`; diff row tints stay the four old values as tokens `--diff-add`, `--diff-del`.
- Chips and badges: `.chip`, `.badge` (same rule), `.chip-open`, `.chip-closed`, `.chip-merged`, `.chip-done`, `.chip-neutral`, `.chip-source_gone`, `.chip-stale`, `.chip.topic`, `.chip.label` (colour from its inline style), `.badge-verified`, `.badge-unsigned`, `.badge-signed_key_expired`, `.badge-bad_signature`, `.badge.check-*` and `.chip.check-*` for `success`, `pending`, `failure`, `error`, `span.check-*` text colours, `.memberchip`, `.refchip`, `.refmenu` + `.refdrop` + `.allrefs` (dropdown on `--canvas` with `--line` border and `--shadow`), `.crumbs`, `.pathbar`, `.act` links become `.button.btn`.
- Repository facts: `.facts` two-column grid at reading width, `.clone` with `label` and `pre`, `.factgrid`, `.counts`, `.fact`, `.langbar`, `.lang`, `.langs`, `.lang-name`, `.contribs`, `.label`. From the mock.
- Landing: `.landing`, `.lede`, `pre.quickstart`, `.shot` (+ `img`), `.facets`, `.routes`, `.explorelink` (removed in Task 8; keep the rule until then), `.signupform`.
- Profile: `.profilehead`, `.desc`, `.activity`, `.actgraph-scroll`, `.actgraph`, `.actweek`, `.actday` and `.l0` to `.l4` levels as tints of `--link`, `.teambody`.
- Wiki: `.wikilayout`, `.wikinav` (14rem, list rows), `.wikipage`, under 40rem stacked.
- Misc: `.meta`, `.muted`, `.vh`, `.spacer`, `.mono`, `.compact`, `.headrow`, `.message` (a `pre` for a command to copy), `.size`, `.role`, `.was`, `.who`, `.age`, `.name`, `.icon`, `.xref` (link colour, no underline), `.sigbadge`.
- Breakpoints, all of them: 62rem (aside stacks), 52rem (rail strip, content padding `var(--sp-4)`, tabs wrap), 40rem (facets, wiki, tree columns hidden: `td.lastcommit, th.lastcommit { display: none }`, `.clone pre` wraps with `white-space: pre-wrap; word-break: break-all`, settings rows one column).

Every rule uses tokens; `grep -n '#[0-9a-fA-F]\{3,6\}' internal/web/static/style.css` must match only inside the two `:root` blocks and the `--chev` data URIs.

- [ ] **Step 3: Run the two stylesheet tests and the existing web tests**

Run: `go test ./internal/web -v`
Expected: `TestTokenContrast` PASS, `TestEveryTemplateClassHasARule` PASS (or a list that Task 4 resolves by editing templates; anything still listed after Task 4 is a rule missing here), `TestHeaderRowsAreLeftAligned` PASS. Adjust hex values until the contrast test passes; do not lower a floor.

- [ ] **Step 4: Run the stylesheet-dependent httpd tests**

Run: `go test ./internal/httpd -run 'TestStylesheetFontsAreServed|TestStylesheetRevalidates|TestSyntaxPaletteContrast' -v`
Expected: PASS. `TestSyntaxPaletteContrast` measures against hard-coded grounds; update its `page` ground for dark to `#101114` and `code` to `#0b0c0e`, light `code` to `#ffffff`, and rerun. If a chroma token fails on the new ground, exempt nothing: pick the ground so the palette passes, then set `--inset` to that value and rerun Task 1's test.

- [ ] **Step 5: Commit**

```bash
git add internal/web/static/style.css internal/httpd/palette_test.go
git commit -m "web: rewrite the stylesheet on measured tokens

Ref #218"
```

### Task 4: template markup for the new components

**Files:**
- Modify: every template under `internal/web/templates/` that the list below names.

The stylesheet is class-compatible; these are the markup changes the new components need. Do not change page composition here (that is MR 2).

- [ ] **Step 1: Button classes**

In every template: a `<button type="submit">` that is the page's one main action keeps no class (renders primary). Every other button gets `class="btn"`: the header toggles already have it; add it to per-field Save buttons in `settings.html` and `account.html`, to filter and search submit buttons, to Approve / Request changes / Convert to draft / Ready for review in `mr.html`, and to `admin.html` row actions. Buttons that destroy or refuse get `class="danger"`: Close without merging (`mr.html`), Unprotect / Detach / Remove / Delete this team / Archive (`settings.html`, `owner.html`, `account.html`, `labels.html`, `snippet.html`, `releases.html`). Buttons that were `class="linklike"` and are destructive become `class="danger"` only when they sit beside a `confirmfield`; the rest stay quiet.

- [ ] **Step 2: Search and filter forms**

`explore.html`, `issues.html`, `mrs.html`, `builds.html`, `globalsearch.html`, `search.html`, `log.html`: the text filter form gets `<button type="submit" class="btn">Search</button>` after the input (`log.html` and `search.html` already have a button; relabel to "Search" and add `btn`), and when the handler passes a count the list is preceded by `<p class="meta">{{.Count}} result{{if ne .Count 1}}s{{end}}</p>`; where the page struct has no count, leave it (do not add handler work in this MR).

- [ ] **Step 3: Copy vocabulary**

`layout.html`: rail search `placeholder="Search"` (already), footer unchanged, `Sign in` (already), `Log out` (already). `login.html`: `<h1>Sign in</h1>`, button "Email me a link" stays. `register.html`: labels "Username", "Email", "SSH public key" in sentence case. `new.html`, `issuenew.html`, `mrnew.html`, `snippetnew.html`: headings and buttons in sentence case. `{{.Site}}` renders the configured title; templates that hardcode `gitbay` keep it lowercase.

- [ ] **Step 4: Run the template tests and the class coverage test**

Run: `go test ./internal/web ./internal/httpd -run 'TestEveryPageTemplateParses|TestEveryTemplateClassHasARule|TestEveryInputHasAnAccessibleName|TestHeaderRowsAreLeftAligned' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/web/templates
git commit -m "web: button treatments, explicit search buttons, sentence case

Ref #218"
```

### Task 5: visual check of every page family

**Files:**
- Create: `.claude/screenshots/local.sh` (ignored by git; a helper, not a deliverable)

- [ ] **Step 1: Write the local instance script**

```bash
#!/bin/sh
# Start a local gitbayd with this checkout pushed into krz/gitbay, for
# screenshots. Prints the HTTP base and a browser login URL.
set -eu
ROOT=${ROOT:-/tmp/gitbay-local}
rm -rf "$ROOT"; mkdir -p "$ROOT"
go build -o "$ROOT/gitbayd" ./cmd/gitbayd
cat > "$ROOT/config.toml" <<EOF
[server]
root = "$ROOT/data"
site_url = "http://127.0.0.1:8090"
[ssh]
port = 8022
[http]
addr = "127.0.0.1:8090"
tls = "off"
[web]
mode = "accounts"
title = "gitbay"
EOF
"$ROOT/gitbayd" --config "$ROOT/config.toml" serve >"$ROOT/log" 2>&1 &
echo $! > "$ROOT/pid"
sleep 1
ssh-keygen -q -t ed25519 -N "" -f "$ROOT/key"
"$ROOT/gitbayd" --config "$ROOT/config.toml" admin user create cmc --key "$ROOT/key.pub" --email cmc@example.test --verified
"$ROOT/gitbayd" --config "$ROOT/config.toml" admin user promote cmc
S="ssh -p 8022 -i $ROOT/key -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null git@127.0.0.1"
$S repo create krz/gitbay >/dev/null
GIT_SSH_COMMAND="ssh -i $ROOT/key -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null" \
  git push -q "ssh://git@127.0.0.1:8022/krz/gitbay.git" HEAD:refs/heads/main
$S issue create krz/gitbay --title "ios notifications" --file - <<'EOF'
research options for ios notification support for builds, MRs, assignments, etc.
EOF
echo "base http://127.0.0.1:8090"
$S web login
```

Run it with `sh .claude/screenshots/local.sh`, open the printed login URL in the browser pane, then walk every page family in both schemes at desktop and 375px: landing, login, register, explore, profile, repository code/tree/blob/blame/log/commit/refs/releases/wiki/search, issues list and page, MR list and page (three views), builds, settings, account, admin, dashboard, notifications, bookmarks, snippets, 404. Compare each against `.claude/screenshots/before/` and the mockups. Fix rules in `style.css` for anything broken; commit each fix as `web: <what>`. Stop the instance with `kill $(cat /tmp/gitbay-local/pid)`.

- [ ] **Step 2: Keyboard and zoom pass**

In the browser pane at 1280 wide: Tab through the landing, register, repository overview, an MR and settings; every stop shows the 2px ring, the order follows the document. Set the pane to 200% zoom (the app's zoom, or 640px width as a proxy) and confirm no horizontal page scroll on those five pages.

- [ ] **Step 3: Commit any fixes, then vet**

Run: `go build ./... && go vet ./... && go test ./internal/web ./internal/httpd`
Expected: PASS.

### Task 6: open and merge MR 1

- [ ] **Step 1: Push and open the MR**

```bash
git push -u origin design-stylesheet
gitbay mr create --source design-stylesheet --target main --title "web: stylesheet rewrite on measured tokens" --file - <<'EOF'
Replaces style.css: tokens on :root in both schemes with a contrast test, one control family (primary, secondary, quiet, destructive), one container recipe, explicit search buttons, sentence case. Template class coverage is tested.

Ref #218
EOF
```

- [ ] **Step 2: Wait for bay1, merge, delete the branch**

`gitbay build list --json` until the MR head's `build` and `test` jobs report success (one poll per minute at most). Then:

```bash
gitbay mr merge <n> --strategy ff
git switch main && git pull --ff-only && git branch -d design-stylesheet && git push origin --delete design-stylesheet
```

If the merge reports the branch is behind, `git rebase main`, re-push, merge again.

---

## MR 2: layout and the four reference pages

Branch `design-pages` off `main` after MR 1 merges. Tasks 7 to 12.

### Task 7: content width per page

**Files:**
- Modify: `internal/web/templates/layout.html` (the `<main>` line and a new default block)
- Modify: `tree.html`, `blob.html`, `blame.html`, `log.html`, `commit.html`, `compare.html`, `builds.html`, `build.html`, `search.html`, `globalsearch.html` (wide); `landing.html`, `login.html`, `register.html`, `registered.html`, `new.html`, `issuenew.html`, `mrnew.html`, `settings.html`, `account.html`, `admin.html`, `edit.html`, `snippetnew.html`, `privacy.html`, `404.html` (bounded)
- Test: `internal/web/web_test.go`

- [ ] **Step 1: Write the failing test**

Append to `internal/web/web_test.go`:

```go
// TestMainWidthClass renders every page against an empty struct and
// checks main carries exactly one width class, and that the pages the
// spec calls wide or bounded say so.
func TestMainWidthClass(t *testing.T) {
	wide := map[string]bool{"tree.html": true, "blob.html": true, "blame.html": true, "log.html": true, "commit.html": true, "compare.html": true, "builds.html": true, "build.html": true, "search.html": true, "globalsearch.html": true}
	bounded := map[string]bool{"landing.html": true, "login.html": true, "register.html": true, "registered.html": true, "new.html": true, "issuenew.html": true, "mrnew.html": true, "settings.html": true, "account.html": true, "admin.html": true, "edit.html": true, "snippetnew.html": true, "privacy.html": true, "404.html": true}
	for _, name := range Pages() {
		src, err := TemplateSource(name)
		if err != nil {
			t.Fatal(err)
		}
		has := strings.Contains(src, `{{define "width"}}`)
		switch {
		case wide[name] && !strings.Contains(src, `{{define "width"}}wide{{end}}`):
			t.Errorf("%s: want width wide", name)
		case bounded[name] && !strings.Contains(src, `{{define "width"}}bounded{{end}}`):
			t.Errorf("%s: want width bounded", name)
		case !wide[name] && !bounded[name] && has:
			t.Errorf("%s: defines a width but the spec calls it reading", name)
		}
	}
	layout, err := TemplateSource("layout.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(layout, `<main id="content" class="content {{template "width" .}}">`) {
		t.Error("layout.html: main does not carry the width block")
	}
	if !strings.Contains(layout, `{{define "width"}}reading{{end}}`) {
		t.Error("layout.html: no default width")
	}
}
```

Add `"strings"` to the imports if missing.

- [ ] **Step 2: Run it to verify it fails**

Run: `go test ./internal/web -run TestMainWidthClass -v`
Expected: FAIL on every wide and bounded page and on the layout lines.

- [ ] **Step 3: Implement**

In `layout.html`, change the main line to `<main id="content" class="content {{template "width" .}}">` and add, after the `mark` definition, `{{define "width"}}reading{{end}}`. In each wide page add `{{define "width"}}wide{{end}}` as its first line; in each bounded page `{{define "width"}}bounded{{end}}`. Because each page is parsed after the layout, a page's definition replaces the default.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/web -v`
Expected: PASS, including `TestEveryPageTemplateParses`.

- [ ] **Step 5: Commit**

```bash
git add internal/web
git commit -m "web: content width chosen per page

Ref #218"
```

### Task 8: two-level repository header and the landing, register and login pages

**Files:**
- Modify: `internal/web/templates/layout.html:63-103` (the `repohead` block), `landing.html`, `register.html`, `login.html`
- Modify: `internal/web/static/style.css` (`.toggles`, `.routes`, `.shot`)
- Test: `e2e/design_test.go` (extend `TestReadmeRelativeLinks`)

- [ ] **Step 1: Write the failing e2e assertions**

In `e2e/design_test.go`, inside `TestReadmeRelativeLinks` after the existing checks that the topic chips appear on `/issues`, `/mrs` and `/releases`, invert those three: the description and topic chips must appear on the repo home and must not appear on `/issues`, `/mrs`, `/releases`:

```go
	for _, p := range []string{"/alice/app/issues", "/alice/app/mrs", "/alice/app/releases"} {
		if _, body := inst.get(t, p); strings.Contains(body, `class="chip topic"`) {
			t.Errorf("%s: header still carries topics on a task tab", p)
		}
	}
	if _, body := inst.get(t, "/alice/app"); !strings.Contains(body, `class="chip topic"`) {
		t.Error("repo home lost its topics")
	}
```

Read the existing function first: it asserts the chips on those pages today, so delete those assertions when adding the inverted ones.

Add a new test in the same file:

```go
// TestLandingRoutes checks the landing page's copy and the two routes.
func TestLandingRoutes(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n[registration]\nmode = \"open\"\n")
	_, body := inst.get(t, "/")
	for _, want := range []string{
		"A git forge you drive from the terminal.",
		`class="button primary" href="/explore">Explore repositories</a>`,
		`class="button btn" href="/register">Create an account</a>`,
		"web login</code>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("landing lacks %q", want)
		}
	}
	if strings.Contains(body, "is the whole onboarding") {
		t.Error("landing still calls repo create the whole onboarding")
	}
	_, reg := inst.get(t, "/register")
	if !strings.Contains(reg, "Paste the contents of your public key file") || !strings.Contains(reg, "/krz/gitbay/wiki/SSH-keys") {
		t.Error("register page lacks the key hint or the wiki link")
	}
}
```

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./e2e -run 'TestReadmeRelativeLinks|TestLandingRoutes' -v`
Expected: FAIL on the topic assertions and the landing strings.

- [ ] **Step 3: Rewrite the header block**

In `layout.html` the `repohead` block becomes:

```gotemplate
<header class="repohead">
  <div class="identity">
    {{if field $ "RepoHome"}}<h1 class="repotitle"><a class="owner" href="/{{.OwnerName}}">{{.OwnerName}}</a><span class="sep">/</span>{{.Name}}</h1>
    {{else}}<p class="repotitle"><a class="owner" href="/{{.OwnerName}}">{{.OwnerName}}</a><span class="sep">/</span><a href="/{{.OwnerName}}/{{.Name}}">{{.Name}}</a></p>{{end}}
    {{if eq .Visibility "private"}}<span class="chip">Private</span>{{end}}
    {{if .Settings.Archived}}<span class="chip">Archived</span>{{end}}
    <span class="grow"></span>
    {{if $.Viewer}}<form method="post" action="/{{.OwnerName}}/{{.Name}}/pin" class="inline"><button type="submit" class="btn" aria-pressed="{{if field $ "Pinned"}}true{{else}}false{{end}}"><span aria-hidden="true">{{if field $ "Pinned"}}★{{else}}☆{{end}}</span> {{if field $ "Pinned"}}Pinned{{else}}Pin{{end}}</button></form>
    <form method="post" action="/{{.OwnerName}}/{{.Name}}/watch" class="inline"><button type="submit" class="btn" aria-pressed="{{if eq (str $ "Watch") "watching"}}true{{else}}false{{end}}">{{if eq (str $ "Watch") "watching"}}Watching{{else}}Watch{{end}}</button></form>
    <form method="post" action="/{{.OwnerName}}/{{.Name}}/bookmark" class="inline"><button type="submit" class="btn" aria-pressed="{{if field $ "Marked"}}true{{else}}false{{end}}">{{if field $ "Marked"}}Bookmarked{{else}}Bookmark{{end}}</button></form>
    <form method="post" action="/{{.OwnerName}}/{{.Name}}/fork" class="inline"><button type="submit" class="btn">Fork</button></form>{{end}}
  </div>
  {{$top := topTab (str $ "Tab")}}
  {{/* Description, topics and metadata belong to the code tab. On task
       tabs only the identity row and the tab bar render, and the header
       is identical on every page within a tab. */}}
  {{if eq $top "code"}}
  <p class="repodesc">{{with field $ "Desc"}}{{.}}{{end}} {{with field $ "Topics"}}{{range .}}<a class="chip topic" href="/explore?q={{.}}">{{.}}</a> {{end}}{{end}}</p>
  {{if or $.Repo.Settings.Website (field $ "Mirrors")}}<p class="repometa">{{with $.Repo.Settings.Website}}<a href="{{.}}" rel="nofollow">{{.}}</a>{{end}}{{range field $ "Mirrors"}} · {{if eq .Direction "push"}}mirrors to{{else}}mirrors from{{end}} <a href="{{.URL}}" rel="nofollow">{{.Target}}</a>{{if .Error}}, <span class="bad">sync error: {{.Error}}</span>{{else if .Synced}}, synced {{.Synced}}{{end}}{{end}}</p>{{end}}
  {{if $.Viewer}}<p class="toggles">Pinned shows in your rail. Watching sends every issue, request and build to your inbox. Bookmarked lists it under Bookmarks.</p>{{end}}
  {{end}}
  <nav class="tabs" aria-label="Repository">
    ...unchanged...
  </nav>
</header>
```

`topTab` (`internal/web/web.go:97`) maps `files`, `log`, `refs` and `search` to `"code"`, and the tree, blob, blame, commit and compare pages all set `Tab: "files"`, so `$top` is `"code"` on every code page.

- [ ] **Step 4: Rewrite landing.html**

```gotemplate
{{define "title"}}{{.Site}}{{end}}
{{define "width"}}bounded{{end}}
{{define "content"}}
<div class="landing">
<h1>{{template "mark"}}{{.Site}}</h1>
<p class="lede">A git forge you drive from the terminal. Repositories, issues, merge requests and CI over SSH, with a fast, readable web view of the same state.</p>
<pre class="quickstart">ssh git@{{.Host}} help                        # every command, no client to install
git clone ssh://git@{{.Host}}/owner/repo.git</pre>
{{if .Picture}}<div class="shot"><picture>
  <source srcset="/static/img/mr-dark.png" media="(prefers-color-scheme: dark)">
  <img src="/static/img/mr-light.png" width="1280" height="900" alt="A merge request page: the conversation on the left, checks and reviewers on the right.">
</picture></div>{{end}}
<div class="facets">
  <section><h2>Read</h2><p>Browse and clone any public repository over HTTPS or <code>git://</code>, no account. Every commit shows whether its signature verified.</p></section>
  <section><h2>Write</h2><p>Push over SSH with the key you already have. Create a repository, file an issue, open and merge a request, all as commands.</p></section>
  <section><h2>Review</h2><p>Read a diff, comment on a line, approve, merge, in the browser or the terminal.</p></section>
</div>
<div class="routes"><a class="button primary" href="/explore">Explore repositories</a>{{if .Signup}}<a class="button btn" href="/register">Create an account</a>{{end}}</div>
{{if .Accounts}}<p class="meta">Have an account? {{if .EmailLogin}}<a href="/login">Sign in with an emailed link</a>, or run{{else}}Run{{end}} <code>ssh git@{{.Host}} web login</code>.</p>{{end}}
</div>
{{end}}
```

`Picture` and `EmailLogin` are new fields on the anonymous struct in `index` (`internal/httpd/web.go:162-168`, today `basePage`, `Host`, `Accounts`, `Signup`); add `Picture bool` set to `true` when both `/static/img/mr-dark.png` and `/static/img/mr-light.png` exist in `web.ImageFS` (Task 15 adds the FS; until then set it from a package variable `landingPicture = false` defined next to the handler and flipped in Task 15), and `EmailLogin bool` set from `s.emailLoginEnabled()`, as the login page does (`internal/httpd/accounts.go:91`). The `{{template "mark"}}` in the h1 renders the existing 19px SVG; add `.landing h1 .mark { width: 32px; height: 32px }` to the stylesheet.

- [ ] **Step 5: Rewrite register.html**

```gotemplate
{{define "title"}}register · {{.Site}}{{end}}
{{define "width"}}bounded{{end}}
{{define "content"}}
<h1>Create an account</h1>
{{if eq .Mode "invite"}}<p class="lede">This instance is invite-only. You need an invite code from an admin.</p>
{{else}}<p class="lede">Open registration. Your account activates once you verify your email.</p>{{end}}
{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}
<form method="post" action="/register" class="signupform">
<div class="field"><label for="username">Username</label><input type="text" id="username" name="username" value="{{.Username}}" required autofocus></div>
{{if eq .Mode "invite"}}<div class="field"><label for="invite">Invite code</label><input type="text" id="invite" name="invite" required></div>
{{else}}<div class="field"><label for="email">Email</label><input type="text" id="email" name="email" required></div>{{end}}
<div class="field"><label for="key">SSH public key</label>
<p class="hint">Paste the contents of your public key file, usually <code>~/.ssh/id_ed25519.pub</code>. It starts with <code>ssh-ed25519</code> or <code>ssh-rsa</code>. No key yet? <a href="/krz/gitbay/wiki/SSH-keys">Make one</a>.</p>
<textarea id="key" name="key" rows="3" required placeholder="ssh-ed25519 AAAA... you@host"></textarea></div>
<p><button type="submit">Create account</button></p>
</form>
<p class="meta">Prefer the terminal? <code>ssh git@{{.Host}} register --username you {{if eq .Mode "invite"}}--invite &lt;code&gt;{{else}}--email you@example.org{{end}}</code></p>
{{end}}
```

The wiki link points at this repository's wiki on the instance; on another instance the page may not exist, which the spec accepts. In `login.html`, change the heading to `Sign in`, wrap the input in `<div class="field">`, and keep everything else.

- [ ] **Step 6: Run the tests**

Run: `go test ./internal/web ./internal/httpd && go test ./e2e -run 'TestReadmeRelativeLinks|TestLandingRoutes|TestAccounts' -v`
Expected: PASS. (`TestAccounts` in `e2e/accounts_test.go` renders the landing with a title; it must still find the title string.)

- [ ] **Step 7: Commit**

```bash
git add internal/web internal/httpd e2e/design_test.go
git commit -m "web: two-level repository header, landing and register copy

Ref #218"
```

### Task 9: repository overview

**Files:**
- Modify: `internal/web/templates/tree.html:5-34`
- Test: `e2e/facts_test.go` (it reads `<p class="contribs">`; keep that markup)

- [ ] **Step 1: Write the failing test**

Append to `e2e/design_test.go`:

```go
// TestTreeSearchCodeAndClone: the overview links "Search code", not
// "Find file", and shows two labelled clone blocks after the file table.
func TestTreeSearchCodeAndClone(t *testing.T) {
	inst := startInstance(t)
	key := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", key+".pub")
	inst.ssh(t, key, "", "repo", "create", "alice/app")
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), inst.gitEnv(key)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	os.WriteFile(filepath.Join(dir, "README.md"), []byte("# app\n"), 0o644)
	run("add", "."); run("-c", "user.name=a", "-c", "user.email=a@example.test", "commit", "-q", "-m", "init")
	run("push", "-q", inst.sshURL("alice/app"), "main")
	_, body := inst.get(t, "/alice/app")
	if strings.Contains(body, ">Find file<") || !strings.Contains(body, ">Search code<") {
		t.Error("overview still says Find file")
	}
	if i, j := strings.Index(body, `<table class="tree">`), strings.Index(body, `<div class="clone">`); i < 0 || j < i {
		t.Error("clone block does not follow the file table")
	}
	if !strings.Contains(body, `<label>SSH</label>`) || !strings.Contains(body, `<label>HTTPS</label>`) {
		t.Error("clone blocks are not labelled")
	}
}
```

Add `os`, `os/exec`, `path/filepath` to the imports. Copy the push helper shape from an existing e2e test that pushes (grep `gitEnv` in `e2e/git_test.go`) if this shape differs from the harness.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./e2e -run TestTreeSearchCodeAndClone -v`
Expected: FAIL on "Find file".

- [ ] **Step 3: Recompose tree.html**

Replace lines 5-34 with:

```gotemplate
<div class="pathbar">
  {{template "refmenu" .}}
  <span class="crumbs"><a href="/{{.Repo.OwnerName}}/{{.Repo.Name}}">{{.Repo.Name}}</a>/{{range .Crumbs}}<a href="{{.URL}}">{{.Name}}</a>/{{end}}</span>
  <div class="acts">
    <a class="button btn" href="/{{.Repo.OwnerName}}/{{.Repo.Name}}/search">Search code</a>
    <a class="button btn" href="/{{.Repo.OwnerName}}/{{.Repo.Name}}/log/{{.Ref}}">History</a>
    <a class="button btn" href="/{{.Repo.OwnerName}}/{{.Repo.Name}}/archive/{{.Ref}}.tar.gz">Download</a>
  </div>
</div>
```

Move the `tipbar` block inside the table as its first row: in the `<table class="tree">`, before `<tr class="cols">`, render

```gotemplate
{{with .Tip}}{{if .SHA}}<tr class="tipbar"><td colspan="3"><span class="who">{{template "authorname" dict "Name" .Author "User" .User "Email" .Email}}</span> <a class="subject" href="/{{$.Repo.OwnerName}}/{{$.Repo.Name}}/commit/{{.SHA}}">{{.Subject}}</a><span class="spacer"></span><a class="sha" href="/{{$.Repo.OwnerName}}/{{$.Repo.Name}}/commit/{{.SHA}}"><code>{{short .SHA}}</code></a> <span class="age"{{if not .When.IsZero}} title="{{whenT .When}}"{{end}}>{{ago .When}}</span></td></tr>{{end}}{{end}}
```

and delete the old `<div class="tipbar">`. Keep `<tr class="cols">` with headers `Name`, `Last commit`, `Updated`.

After the README section (the `section.readme` at the end of the file), when `.Entries` is non-empty, add:

```gotemplate
{{if .Entries}}<div class="facts">
  <div class="clone">
    <h2>Clone</h2>
    <label>SSH</label><pre><code>git clone {{.SSHCloneURL}}</code></pre>
    <label>HTTPS</label><pre><code>git clone {{.CloneURL}}</code></pre>
  </div>
  {{if .Facts.Commits}}{{$r := printf "/%s/%s" .Repo.OwnerName .Repo.Name}}<div class="about">
    <h2>About</h2>
    <div class="factgrid">
      <a href="{{$r}}/log/{{.Ref}}"><b>{{.Facts.Commits}}</b> commit{{if ne .Facts.Commits 1}}s{{end}}</a>
      <a href="{{$r}}/refs"><b>{{.Facts.Branches}}</b> branch{{if ne .Facts.Branches 1}}es{{end}}</a>
      <a href="{{$r}}/refs"><b>{{.Facts.Tags}}</b> tag{{if ne .Facts.Tags 1}}s{{end}}</a>
      {{if .Facts.Bookmarks}}<span><b>{{.Facts.Bookmarks}}</b> bookmark{{if ne .Facts.Bookmarks 1}}s{{end}}</span>{{end}}
      {{with .Facts.License}}<span class="fact">{{.}}</span>{{end}}
      {{with .Facts.Release}}<a href="{{$r}}/releases">latest <b>{{.}}</b></a>{{end}}
      {{with .Facts.Build}}<a href="{{$r}}/builds">build <span class="badge badge-{{.}}">{{.}}</span></a>{{end}}
    </div>
    {{with .Facts.Languages}}<p class="langbar" aria-hidden="true">{{range .}}<span class="lang lang-{{slug .Name}}" style="width:{{pct .Percent}}%"></span>{{end}}</p>
    <p class="langs">{{range .}}<span class="lang-name"><span class="dot lang-{{slug .Name}}"></span>{{.Name}} <span class="muted">{{pct .Percent}}%</span></span>{{end}}</p>{{end}}
    {{with .Facts.Contributors}}<p class="contribs"><span class="label">{{len .}} contributor{{if ne (len .) 1}}s{{end}}</span>{{range .}}{{template "authorname" dict "Name" .Name "User" .User "Email" .Title}}{{end}}</p>{{end}}
  </div>{{end}}
</div>{{end}}
```

Delete the old `<p class="clone">` and the old `<div class="facts">` block. The labels `Name`, `Last commit`, `Updated` are what `TestHeaderRowsAreLeftAligned` reads; check it still passes. Add `.about` to the stylesheet beside `.clone`.

- [ ] **Step 4: Run the tests**

Run: `go test ./internal/web && go test ./e2e -run 'TestTreeSearchCodeAndClone|TestFacts|TestReadme' -v`
Expected: PASS. `e2e/facts_test.go` cuts the body at `<p class="contribs">`, which is preserved.

- [ ] **Step 5: Commit**

```bash
git add internal/web e2e/design_test.go
git commit -m "web: repository overview leads with the files

Ref #218"
```

### Task 10: merge request page

**Files:**
- Modify: `internal/httpd/web.go:1820-1960` (the `mr` handler struct and the diff computation)
- Modify: `internal/web/templates/mr.html`
- Test: `e2e/mrweb_test.go`

**Interfaces:**
- Produces: `HeadMerged bool` on the MR page struct: the head is already reachable from the target, so the diff is empty by construction.

- [ ] **Step 1: Write the failing test**

In `e2e/mrweb_test.go` add a test that opens an MR, merges its branch into the target by fast-forward through git (push the head to `main` directly with a key that has write access, in a repository without require-MR), then loads `?view=diff` and expects the explanation:

```go
// TestMRDiffEmptyExplained: a merge request whose head was fast-forwarded
// into the target outside the request shows why its diff is empty.
func TestMRDiffEmptyExplained(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	key := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", key+".pub", "--email", "alice@example.test", "--verified")
	inst.ssh(t, key, "", "repo", "create", "alice/app")
	dir := pushInitial(t, inst, key, "alice/app") // reuse the helper this file already has for creating a repo with a main branch; if it is named differently, use that name
	gitIn(t, dir, key, "checkout", "-q", "-b", "feature")
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("one\n"), 0o644)
	gitIn(t, dir, key, "add", "."); gitIn(t, dir, key, "commit", "-q", "-m", "one")
	gitIn(t, dir, key, "push", "-q", inst.sshURL("alice/app"), "feature")
	if _, errOut, code := inst.ssh(t, key, "", "mr", "create", "alice/app", "--source", "feature", "--target", "main", "--title", "one"); code != 0 {
		t.Fatal(errOut)
	}
	gitIn(t, dir, key, "push", "-q", inst.sshURL("alice/app"), "feature:main")
	_, body := inst.get(t, "/alice/app/mrs/1?view=diff")
	if !strings.Contains(body, "No changes between the source and target.") ||
		!strings.Contains(body, "already merged or fast-forwarded into <code>main</code>") {
		t.Fatalf("empty diff unexplained:\n%s", body)
	}
}
```

Before writing it, read the top of `e2e/mrweb_test.go` and `e2e/git_test.go` for the helper names that create a repository with an initial commit and run git with the key's environment; use those exact names in place of `pushInitial` and `gitIn`. If none exist, write `gitIn(t, dir, key, args...)` in this file with the `exec.Command("git", args...)` shape from Task 9.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./e2e -run TestMRDiffEmptyExplained -v`
Expected: FAIL: the body has `0 files changed` and no explanation.

- [ ] **Step 3: Handler: compute HeadMerged**

In `web.go`'s `mr` handler, after `files, diffTruncated` are computed, add:

```go
	headMerged := false
	if len(files) == 0 && m.HeadSHA != "" {
		if targetSHA, err := gitutil.ResolveRef(p.Dir, "refs/heads/"+m.TargetRef); err == nil {
			if ok, err := gitutil.IsAncestor(p.Dir, m.HeadSHA, targetSHA); err == nil {
				headMerged = ok
			}
		}
	}
```

`gitutil.IsAncestor(dir, old, new)` reports whether `old` is an ancestor of `new`. Add `HeadMerged bool` to the anonymous struct and set `HeadMerged: headMerged`. The handler already resolves the target SHA for `Gates`; reuse that variable if it is in scope.

- [ ] **Step 4: Template: title line, diff empty case, aside order**

In `mr.html`:

Title block (replace the `h1.issuetitle`, `p.issuemeta` and the stacked notes):

```gotemplate
<h1 class="issuetitle">{{.MR.Title}} <span class="issuenumber">!{{.MR.Number}}</span></h1>
<p class="issuemeta"><span class="chip chip-{{.MR.State}}">{{if eq .MR.State "source_gone"}}source gone{{else}}{{.MR.State}}{{end}}</span>{{if .MR.Draft}} <span class="chip chip-neutral">draft</span>{{end}}
  {{if eq .MR.State "merged"}}merged by <a href="/{{.MR.MergedBy}}">{{.MR.MergedBy}}</a> on {{when .MR.MergedAt}}{{else if eq .MR.State "closed"}}closed without merging by <a href="/{{.MR.ClosedBy}}">{{.MR.ClosedBy}}</a> on {{when .MR.ClosedAt}}{{else}}opened by <a href="/{{.MR.Author}}">{{.MR.Author}}</a> on {{when .MR.CreatedAt}}{{end}}
  · <code>{{.MR.SourceRef}}</code> into <code>{{.MR.TargetRef}}</code></p>
{{with .StackedOn}}<p class="meta">stacked on <a href="{{$base | dir}}/{{.Number}}">!{{.Number}}</a></p>{{end}}
```

`store.MR` has `Author`, `SourceRef`, `TargetRef`, `Title`, `MergedBy`, `MergedAt`, `ClosedBy`, `ClosedAt`, `CreatedAt` (`internal/store/mrs.go:9-37`). Keep the existing stacked-on/stacked-children paragraphs as they are if their expressions differ from the sketch above.

Diff arm (replace lines 76-80):

```gotemplate
{{else}}
{{if .DiffFiles}}<p class="diffstat">{{.Stat.Files}} file{{if ne .Stat.Files 1}}s{{end}} changed, <span class="add">+{{.Stat.Adds}}</span> <span class="del">−{{.Stat.Dels}}</span>{{if .DiffTruncated}} · shown up to 4 MiB; the counts and the last file are partial{{end}}</p>
{{if .DiffTruncated}}<p class="error" role="alert">This diff is larger than 4 MiB and is cut off below. Fetch the branch to see all of it.</p>{{end}}
{{template "difffiles" dict "Files" .DiffFiles "Base" $base "Viewer" .Viewer}}
{{else}}<p class="empty-note">No changes between the source and target.{{if .HeadMerged}} The source branch was already merged or fast-forwarded into <code>{{.MR.TargetRef}}</code>.{{end}}</p>{{end}}
{{end}}
```

Aside: reorder the `div.grp` blocks to Review (when open and can write), Merge, Merge gates, Checks, Reviews, Reviewers, Source and target (merge the old `Target` and `Source` groups into one `h2` "Source and target" with the source ref, target ref, head and base SHAs, and the delete-branch form), Milestone. Every button in the aside: Merge is primary; Approve, Request changes, Ready for review, Convert to draft are `btn`; Close without merging and Delete branch are `danger`. Wrap the conversation column's contents in `<div class="prose">`.

- [ ] **Step 5: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/web ./internal/httpd && go test ./e2e -run 'TestMRDiffEmptyExplained|TestMRWeb|TestDiffWeb|TestDiffComment' -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/httpd/web.go internal/web/templates/mr.html e2e/mrweb_test.go
git commit -m "web: merge request page states its resolution and explains an empty diff

Ref #218"
```

### Task 11: repository settings

**Files:**
- Modify: `internal/httpd/settings.go:18-26, 53-57, 111-120, 139-144`
- Modify: `internal/web/templates/settings.html`
- Test: `e2e/settingsweb_test.go`

**Interfaces:**
- Produces: `Saved bool` on `settingsPage`; success flash text `Saved the <field>.`; the `topics` form field `topics` (comma separated) replacing `add`/`remove`.

- [ ] **Step 1: Write the failing test**

In `e2e/settingsweb_test.go`, inside `TestRepoSettingsWeb` after the `post` closure, add:

```go
	if body := post(url.Values{"field": {"description"}, "description": {"a thing"}}); !strings.Contains(body, `class="notice" role="status">Saved the description.`) {
		t.Fatalf("no success flash after saving the description:\n%s", body)
	}
	if body := post(url.Values{"field": {"topics"}, "topics": {"cli, forge"}}); !strings.Contains(body, `value="cli, forge"`) {
		t.Fatalf("topics field is not prefilled after save:\n%s", body)
	}
	if body := post(url.Values{"field": {"topics"}, "topics": {"forge"}}); strings.Contains(body, `>cli<`) || !strings.Contains(body, `value="forge"`) {
		t.Fatalf("removing a topic through the field failed:\n%s", body)
	}
	if body := post(url.Values{"field": {"website"}, "website": {"javascript:alert(1)"}}); !strings.Contains(body, `class="error"`) || !strings.Contains(body, `value="javascript:alert(1)"`) {
		t.Fatalf("error does not keep the submitted website:\n%s", body)
	}
```

Delete the older website assertion at lines 91-93 since this one supersedes it, and any assertion that posts `add`/`remove` topics.

- [ ] **Step 2: Run to verify it fails**

Run: `go test ./e2e -run TestRepoSettingsWeb -v`
Expected: FAIL on the success flash.

- [ ] **Step 3: Handler**

`settingsPage` gains `Saved bool` and `Submitted map[string]string`. In `settingsForm`, after `Notice: s.takeFlash(w, r)`, set `Saved: strings.HasPrefix(notice, "Saved ")` (take the flash into a local first). Retaining a submitted value on error: the redirect loses the form, so the error path re-renders instead. Change the tail of `settingsSubmit`:

```go
	_, msg, ok := s.runControl(u, argv)
	if ok {
		s.settingsRedirect(w, r, "Saved the "+fieldLabel(field)+".")
		return
	}
	s.settingsFormWith(w, r, msg, r.Form)
```

where `fieldLabel` maps the `field` value to its label in lower case (`description`, `website`, `visibility`, `default branch`, `git:// serving`, `required checks`, `approvals`, `review threads`, `CODEOWNERS`, `require-MR`, `signed commits`, `protected branch`, `protected tag`, `dependency scanning`, `archive`, `topics`, `runner`), and `settingsFormWith(w, r, notice string, submitted url.Values)` is `settingsForm` refactored to take the notice and a map of submitted values that the template prefers over stored ones: `Submitted: map[string]string{"description": submitted.Get("description"), "website": submitted.Get("website"), "topics": submitted.Get("topics")}`. `settingsForm` calls `settingsFormWith(w, r, s.takeFlash(w, r), nil)`.

Topics case:

```go
	case "topics":
		want := map[string]bool{}
		var order []string
		for _, t := range strings.Split(v("topics"), ",") {
			if t = strings.ToLower(strings.TrimSpace(t)); t != "" && !want[t] {
				want[t] = true
				order = append(order, t)
			}
		}
		have, err := s.st.ListTopics(p.Repo.ID)
		if err != nil {
			s.settingsRedirect(w, r, err.Error())
			return
		}
		var add, remove []string
		for _, t := range order {
			if !slices.Contains(have, t) {
				add = append(add, t)
			}
		}
		for _, t := range have {
			if !want[t] {
				remove = append(remove, t)
			}
		}
		if len(remove) > 0 {
			if _, msg, ok := s.runControl(u, append([]string{"repo", "topics", "remove", repo}, remove...)); !ok {
				s.settingsFormWith(w, r, msg, r.Form)
				return
			}
		}
		if len(add) > 0 {
			argv = append([]string{"repo", "topics", "add", repo}, add...)
		} else {
			s.settingsRedirect(w, r, "Saved the topics.")
			return
		}
```

`p.Repo.ID` is however the handler names the resolved repository; read the top of `settingsSubmit` for the variable. `s.st.ListTopics` already exists (used in `settingsForm`).

- [ ] **Step 4: Template**

Rewrite `settings.html` after the title line:

```gotemplate
{{define "width"}}bounded{{end}}
{{define "content"}}
{{$base := printf "/%s/%s/settings" .Repo.OwnerName .Repo.Name}}
<h1>Settings</h1>
{{if .Notice}}{{if .Saved}}<p class="notice" role="status">{{.Notice}}</p>{{else}}<p class="error" role="alert">{{.Notice}}</p>{{end}}{{end}}
<nav class="sections" aria-label="Sections"><a href="#identity">Identity</a><a href="#access">Access</a><a href="#gates">Merge gates</a><a href="#branches">Protected branches</a><a href="#tags">Protected tags</a>{{if .DepsEnabled}}<a href="#deps">Dependencies</a>{{end}}<a href="#runners">Runners</a><a href="#lifecycle">Lifecycle</a></nav>

<section id="identity"><h2>Identity</h2>
<form method="post" action="{{$base}}" class="setform">
  <input type="hidden" name="field" value="description">
  <div><label for="description">Description</label></div>
  <div><input type="text" id="description" name="description" value="{{or (index .Submitted "description") .Desc}}" placeholder="one line, shown in listings"></div>
  <div><button type="submit">Save</button></div>
</form>
<form method="post" action="{{$base}}" class="setform">
  <input type="hidden" name="field" value="website">
  <div><label for="website">Website</label></div>
  <div><input type="text" id="website" name="website" value="{{or (index .Submitted "website") .Repo.Settings.Website}}" placeholder="https://example.org"></div>
  <div><button type="submit" class="btn">Save</button></div>
</form>
<form method="post" action="{{$base}}" class="setform">
  <input type="hidden" name="field" value="topics">
  <div><label for="topics">Topics</label><p class="hint">Comma separated, lower case.</p></div>
  <div><input type="text" id="topics" name="topics" value="{{or (index .Submitted "topics") (join .Topics ", ")}}"></div>
  <div><button type="submit" class="btn">Save</button></div>
</form>
{{if .Branches}}<form method="post" action="{{$base}}" class="setform">
  <input type="hidden" name="field" value="default-branch">
  <div><label for="default-branch">Default branch</label><p class="hint">What a clone checks out and what CI builds on trigger.</p></div>
  <div><select id="default-branch" name="default-branch">{{$cur := .Repo.DefaultBranch}}{{range .Branches}}<option value="{{.Name}}"{{if eq .Name $cur}} selected{{end}}>{{.Name}}</option>{{end}}</select></div>
  <div><button type="submit" class="btn">Save</button></div>
</form>{{end}}
</section>
```

`join` is a new template func: add `"join": strings.Join` to `funcs` in `internal/web/web.go`. `.Submitted` is nil on a plain GET; `index` on a nil map returns the zero string, and `or` falls through to the stored value.

Then the remaining sections in the same three-column shape, each control's hint from the spec:

- Access: Visibility (radio Public / Private; hint "Private repositories answer not found to everyone without access, including in search and on your profile."), git:// serving (checkbox; hint "Unauthenticated, unencrypted read access on port 9418. Public repositories only.").
- Merge gates: Required checks ("A request waits until every CI job that would report on its head has succeeded."), Approvals (number, "Approvals from people with write access. Zero means none required."), Review threads ("Every thread on the diff must be resolved before merging."), CODEOWNERS ("Owners of every touched path must approve."), Signed commits ("Every commit in the request must carry a verified signature. Squash and merge strategies are refused, since both mint an unsigned commit.").
- Protected branches: each protected branch a row with its name, a hint "Direct pushes refused, merge requests only." when require-MR is on, and `Unprotect` as `danger`; the protect form; the require-MR checkbox with hint "Every protected branch changes through a merge request. A direct push is refused in pre-receive."
- Protected tags: same shape with `danger` Unprotect.
- Dependencies, Runners: rows as today, `Detach` as `danger`, the attach textarea in a `setform stack`.
- Lifecycle: Archive with hint "Read-only for everyone. Issues and requests close to new activity. Reversible." and the `confirmfield` beside a `danger` button.

Keep every `name=` and `field` value exactly as `settingsSubmit` reads them (list in `internal/httpd/settings.go:67-133`).

- [ ] **Step 5: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/web ./internal/httpd && go test ./e2e -run TestRepoSettingsWeb -v`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/httpd/settings.go internal/web
git commit -m "web: settings as bounded rows with consequences and a saved flash

Ref #218"
```

### Task 12: dashboard aside, visual check, MR 2

**Files:**
- Modify: `internal/web/templates/dashboard.html` (remove the Pinned `div.grp` from the aside)
- Test: `e2e/dashboard_test.go` (if it asserts the pinned group in the aside, change it to assert the rail's pinned list instead: `class="raillist" aria-labelledby="rail-pinned"`)

- [ ] **Step 1: Remove the duplicate pinned group and run the dashboard test**

Run: `go test ./e2e -run TestDashboard -v`
Expected: PASS after the assertion change.

- [ ] **Step 2: Visual and keyboard check**

Rerun Task 5's script and walk: landing, register, login, repository overview, MR (three views), settings, dashboard, issues, explore at 1280 and 375, both schemes, and the keyboard pass on the five reference pages. Compare against the mockups. Commit fixes as `web: <what>`.

- [ ] **Step 3: Push, open MR 2, merge**

```bash
git push -u origin design-pages
gitbay mr create --source design-pages --target main --title "web: content widths and the four reference pages" --file - <<'EOF'
A width class per page; the repository header trimmed on task tabs; landing, register, repository overview, merge request and repository settings recomposed per the design spec; Search code replaces Find file; settings report a saved flash and keep a rejected value; topics are one field.

Ref #218
EOF
```

Wait for green, `gitbay mr merge <n> --strategy ff`, delete the branch both places.

---

## MR 3: wiki and docs

Branch `design-docs` off `main`. Task 13.

### Task 13: SSH-keys page, Admin title, Users vocabulary, CHANGELOG

**Files:**
- Create: `.gitbay/wiki/SSH-keys.org`
- Modify: `.gitbay/wiki/Admin.org:84-89`, `.gitbay/wiki/Users.org:53-77`, `.gitbay/wiki/Home.org`, `CHANGELOG.org`

- [ ] **Step 1: Write SSH-keys.org**

```org
#+title: SSH keys

Your key is your identity on gitbay: there are no passwords. Registering
pastes the public half of a key pair; the private half never leaves your
machine.

* Make a key

#+begin_src sh
ssh-keygen -t ed25519 -C "you@host"
#+end_src

Accept the default path. Two files land in =~/.ssh/=: =id_ed25519= is
private, keep it; =id_ed25519.pub= is public, paste it.

* Get the public half

#+begin_src sh
cat ~/.ssh/id_ed25519.pub
#+end_src

One line, starting =ssh-ed25519 AAAA…= and ending with your comment. That
whole line is what the registration form and =register= want.

* Check it works

#+begin_src sh
ssh git@<host> whoami
#+end_src

More keys, labels and scopes: [[Users][the user guide]] under "SSH keys".
```

- [ ] **Step 2: Edit the other pages**

`Users.org` under `* SSH keys`: add as the first paragraph "New to SSH keys? [[SSH-keys]] makes one in two commands." `Home.org`: add `- [[SSH-keys][SSH keys]] — make a key and find its public half` after the Quickstart line. `Admin.org` under `** [web]`: add bullets `- =title= — the instance's display name in the rail and page titles; empty falls back to the site host. Lower case is the convention for gitbay itself.` and `- =privacy_notice= — operator text shown on =/privacy= under the fixed statement.`

Users.org under "Output rules" or a new `* Web vocabulary` heading: "Sign in / Log out, Search, sentence-case headings and buttons, product name lowercase."

- [ ] **Step 3: CHANGELOG entry**

Insert above `* v1.22.1`:

```org
* v1.23.0 — <date of the release>

The web design foundation (#218). Tokens measured in both schemes, one
control family, content widths per page, and the landing, repository
overview, merge request and settings pages recomposed. Set =[web]
title= to =gitbay= in lower case; the templates already are.

- =style.css= rewritten on tokens defined on =:root= and again for dark;
  =TestTokenContrast= enforces the floors and
  =TestEveryTemplateClassHasARule= that every template class has a rule.
- Buttons are primary, secondary, quiet or destructive; one focus ring on
  everything; success and error flashes share one shape.
- Every page picks a width: wide for code, reading for lists and threads,
  bounded for forms.
- The repository header shows description and topics on the code tab
  only; the overview leads with the file table and puts clone commands
  and facts after the README; "Find file" is "Search code".
- A merge request states who merged or closed it under its title and
  explains an empty diff.
- Repository settings are bounded rows with a consequence beside each
  control, a saved flash, a rejected value kept, and topics as one field.
- Landing copy says what the product is and where to go; the register
  form says to paste the contents of the key file and links [[SSH-keys]].
```

Fill the date when the release is cut.

- [ ] **Step 4: Commit, MR, merge**

```bash
git add .gitbay/wiki CHANGELOG.org
git commit -m "wiki: SSH keys page, web title, design vocabulary; CHANGELOG v1.23.0

Ref #218"
git push -u origin design-docs
gitbay mr create --source design-docs --target main --title "wiki: SSH keys page and the v1.23.0 changelog" --file - <<'EOF'
Ref #218
EOF
```

The `test` job skips wiki-only changes; the `build` job still runs. Merge with ff, delete the branch.

---

## MR 4: deploy, screenshots, release

Branch `design-picture` off `main`. Tasks 14 and 15.

### Task 14: deploy and capture

- [ ] **Step 1: Deploy main**

```bash
git switch main && git pull --ff-only && make deploy
```

Then the user sets `title = "gitbay"` under `[web]` in `/etc/gitbay/config.toml` on bay1 and restarts; hand that to them, do not ssh to bay1.

- [ ] **Step 2: Capture the after set**

Follow `.claude/screenshots/README.md` (`python3 shoot.py after/...`). Compare `after/` with `before/` page by page; anything wrong is a fix on a new branch before the release.

- [ ] **Step 3: Capture the landing picture**

```bash
cd .claude/screenshots
printf 'mr https://gitbay.org/krz/gitbay/mrs/315\n' > picture.txt
python3 shoot.py pic/dark dark 1280 picture.txt
python3 shoot.py pic/light light 1280 picture.txt
```

`shoot.py` captures full page; crop each to 1280x900 from the top (`sips -c 900 1280 pic/dark/mr.png --out mr-dark.png`, same for light), then `pngquant`-free size check: each under 400KB (`ls -l`); if larger, `sips -s format png` at `--resampleWidth 1280` is already the size, so reduce with `sips -s formatOptions 70` to JPEG only if PNG cannot get under 1MB. Copy to `internal/web/static/img/mr-dark.png` and `mr-light.png`.

### Task 15: serve the images and wire the picture

**Files:**
- Modify: `internal/web/web.go` (embed), `internal/httpd/routes.go:52-56`, `internal/httpd/web.go:95-107`, `internal/httpd/web.go` (landing struct `Picture`)
- Test: `internal/httpd/fonts_test.go` (extend), `e2e/design_test.go` (extend `TestLandingRoutes`)

- [ ] **Step 1: Write the failing tests**

Append to `internal/httpd/fonts_test.go`:

```go
// TestLandingImagesAreServed: every file under static/img has a route
// that answers 200 with an image type and the stylesheet's cache policy.
func TestLandingImagesAreServed(t *testing.T) {
	s := newTestServer(t) // the constructor the file's other test uses
	entries, err := fs.ReadDir(web.ImageFS, "static/img")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("no images embedded")
	}
	for _, e := range entries {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/static/img/"+e.Name(), nil))
		if rec.Code != 200 || !strings.HasPrefix(rec.Header().Get("Content-Type"), "image/") {
			t.Errorf("%s: %d %s", e.Name(), rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}
```

Use the same server construction and request shape as `TestStylesheetFontsAreServed` in that file. In `e2e/design_test.go`'s `TestLandingRoutes` add `"/static/img/mr-dark.png"` and `<picture>` to the wanted strings.

- [ ] **Step 2: Run to verify they fail**

Run: `go test ./internal/httpd -run TestLandingImagesAreServed`
Expected: FAIL to compile on `web.ImageFS`.

- [ ] **Step 3: Implement**

`internal/web/web.go`: add

```go
//go:embed static/img/*.png
var ImageFS embed.FS
```

`routes.go`, after the font loop:

```go
	images, _ := fs.ReadDir(web.ImageFS, "static/img")
	for _, f := range images {
		routes = append(routes, Route{Method: "GET", Pattern: "/static/img/" + f.Name(), Handler: s.image})
	}
```

`web.go` beside `font`:

```go
// image serves the embedded landing pictures with the font cache policy.
func (s *Server) image(w http.ResponseWriter, r *http.Request) {
	data, err := web.ImageFS.ReadFile("static" + r.URL.Path[len("/static"):])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	w.Write(data)
}
```

In the landing handler replace the `landingPicture` variable from Task 8 with a check that both files exist in `web.ImageFS` (`fs.Stat`), computed once at server construction.

- [ ] **Step 4: Run the tests**

Run: `go build ./... && go vet ./... && go test ./internal/web ./internal/httpd && go test ./e2e -run 'TestLandingRoutes|TestTopLevelRouteWordsAreReserved' -v`
Expected: PASS; `static` is already reserved.

- [ ] **Step 5: Commit, MR, merge, release**

```bash
git add internal/web internal/httpd e2e/design_test.go
git commit -m "web: landing picture, light and dark

Closes #218"
git push -u origin design-picture
gitbay mr create --source design-picture --target main --title "web: landing picture" --file - <<'EOF'
Closes #218
EOF
```

After the merge: fill the CHANGELOG date in a one-line commit on a branch if it was left blank, then follow the release steps the previous versions used (`deploy/release.sh`, tag `v1.23.0` on main, `make deploy`, bump the tap). Capture the after set once more so `.claude/screenshots/after/` shows the released state.

---

## Self-review against the spec

- Rules: two accents (Task 3), link/fill split and token test (Tasks 1, 3), class coverage (Task 2), authored states (Task 3), lowercase name (Tasks 4, 13, 14).
- Tokens, type, spacing, radius: Task 3.
- Components: Task 3 rules, Task 4 markup; flash family resolved as shared `.error`/`.notice` rules.
- Shell and layout: widths (Task 7), aside 18rem and stacking above (Task 3), rail (Task 3), header (Task 8), page head and filters (Tasks 3, 4), responsive (Tasks 3, 5).
- Reference pages: landing and register (Task 8), overview (Task 9), MR (Task 10), settings (Task 11), dashboard pinned (Task 12).
- Copy: Tasks 4, 8, 9, 13.
- Landing order: MRs 1 to 4. Verification: Tasks 5, 12, 14. Not in spec: filed as issues when MR 4 merges.
