// Package web holds the server-rendered templates and static assets for the
// read-only UI. No JavaScript, no build step.
package web

import (
	"embed"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"reflect"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/style.css
var StyleCSS []byte

//go:embed static/favicon.svg
var FaviconSVG []byte

//go:embed static/fonts/*.woff2
var FontFS embed.FS

// version returns the short VCS revision baked into the binary, or "" when
// built outside a checkout. Used by the layout footer.
var version = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" && len(s.Value) >= 10 {
			return s.Value[:10]
		}
	}
	return ""
})

// fullVersion is the complete VCS revision, for linking the footer hash
// to the upstream commit page.
var fullVersion = sync.OnceValue(func() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return s.Value
		}
	}
	return ""
})

var funcs = template.FuncMap{
	"gitbayVersion": func() string { return version() },
	"gitbayCommit":  func() string { return fullVersion() },
	// paragraphs splits plain text on blank lines for safe rich display.
	"paragraphs": func(s string) []string {
		var out []string
		for _, p := range strings.Split(s, "\n\n") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
		return out
	},
	// short abbreviates a commit SHA for display.
	"short": func(s string) string {
		if len(s) > 10 {
			return s[:10]
		}
		return s
	},
	// dict builds a map for {{template}} calls that need several values.
	"dict": func(pairs ...any) map[string]any {
		m := map[string]any{}
		for i := 0; i+1 < len(pairs); i += 2 {
			if k, ok := pairs[i].(string); ok {
				m[k] = pairs[i+1]
			}
		}
		return m
	},
	"add": func(a, b int) int { return a + b },
	"sub": func(a, b int) int { return a - b },
	// topTab maps a page's Tab to the repo header tab that should read as
	// current. Everything that browses the tree or its history sits under
	// code, whose own toolbar carries the log, search, and refs links.
	"topTab": func(tab string) string {
		switch tab {
		case "files", "log", "refs", "search":
			return "code"
		case "issues":
			return "issues"
		case "merge requests":
			return "mrs"
		}
		return tab
	},
	// initial is the avatar letter for a username.
	"initial": func(s string) string {
		for _, r := range s {
			return strings.ToUpper(string(r))
		}
		return "?"
	},
	// str is field, narrowed to strings: missing or non-string fields
	// yield "", which comparisons handle without erroring.
	"str": func(v any, name string) string {
		rv := reflect.ValueOf(v)
		for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
			rv = rv.Elem()
		}
		if rv.Kind() != reflect.Struct {
			return ""
		}
		f := rv.FieldByName(name)
		if !f.IsValid() || f.Kind() != reflect.String {
			return ""
		}
		return f.String()
	},
	// field reads a field the page struct may not have, so the layout can
	// render repo navigation without every page carrying repo context.
	// Missing fields yield nil, which templates treat as absent.
	"field": func(v any, name string) any {
		rv := reflect.ValueOf(v)
		for rv.Kind() == reflect.Ptr || rv.Kind() == reflect.Interface {
			rv = rv.Elem()
		}
		if rv.Kind() != reflect.Struct {
			return nil
		}
		f := rv.FieldByName(name)
		if !f.IsValid() || !f.CanInterface() {
			return nil
		}
		return f.Interface()
	},
	// sigLabel turns a stored signature state into words. The state names
	// are the CLI's vocabulary and belong in its output; a reader of the
	// web UI needs to know what is wrong, not what the column is called.
	"sigLabel": func(state string) string {
		switch state {
		case "verified":
			return "Verified"
		case "signed_unknown_key":
			return "Unregistered key"
		case "signed_email_mismatch":
			return "Email mismatch"
		case "signed_key_expired":
			return "Key expired"
		case "signed_key_revoked":
			return "Key revoked"
		case "bad_signature":
			return "Bad signature"
		case "unsigned":
			return "Unsigned"
		}
		return state
	},
	// ago renders a time as a coarse relative age, which is what a
	// listing column is actually read for. The zero time yields "".
	// slug turns a language name into a CSS class suffix, so the palette
	// lives in the stylesheet rather than in inline styles the CSP would
	// have to allow.
	"slug": func(s string) string {
		var b strings.Builder
		for _, r := range strings.ToLower(s) {
			switch {
			case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
				b.WriteRune(r)
			default:
				b.WriteByte('-')
			}
		}
		return b.String()
	},
	// pct renders a share to one decimal, dropping a trailing ".0".
	"pct": func(f float64) string {
		s := strconv.FormatFloat(f, 'f', 1, 64)
		return strings.TrimSuffix(s, ".0")
	},
	"ago": func(t time.Time) string {
		if t.IsZero() {
			return ""
		}
		d := time.Since(t)
		if d < 0 {
			d = 0
		}
		plural := func(n int, unit string) string {
			if n == 1 {
				return "1 " + unit + " ago"
			}
			return strconv.Itoa(n) + " " + unit + "s ago"
		}
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return plural(int(d/time.Minute), "minute")
		case d < 24*time.Hour:
			return plural(int(d/time.Hour), "hour")
		case d < 30*24*time.Hour:
			return plural(int(d/(24*time.Hour)), "day")
		case d < 365*24*time.Hour:
			return plural(int(d/(30*24*time.Hour)), "month")
		}
		return plural(int(d/(365*24*time.Hour)), "year")
	},
	// when formats a stored RFC3339 timestamp for display; unparseable
	// values pass through unchanged.
	"when": func(s string) string {
		t, err := time.Parse(time.RFC3339Nano, s)
		if err != nil {
			return s
		}
		return t.UTC().Format("2006-01-02 15:04")
	},
}

// pages holds every page template parsed once with the layout, at
// start-up: a template that does not parse fails the process before it
// serves anything, rather than the first visit to a rarely-hit page, and
// a request no longer re-parses the layout (#116).
var pages = func() map[string]*template.Template {
	entries, err := fs.ReadDir(templateFS, "templates")
	if err != nil {
		panic(err)
	}
	m := map[string]*template.Template{}
	for _, e := range entries {
		name := e.Name()
		if name == "layout.html" || !strings.HasSuffix(name, ".html") {
			continue
		}
		layout := template.Must(template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html"))
		m[name] = template.Must(layout.ParseFS(templateFS, "templates/"+name))
	}
	return m
}()

// Pages lists the page template names, for tests that render each one.
func Pages() []string {
	names := make([]string, 0, len(pages))
	for name := range pages {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Render executes the named page template with the shared layout.
func Render(w io.Writer, page string, data any) error {
	t, ok := pages[page]
	if !ok {
		return fmt.Errorf("no page template %q", page)
	}
	return t.ExecuteTemplate(w, "layout", data)
}
