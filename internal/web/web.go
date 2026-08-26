// Package web holds the server-rendered templates and static assets for the
// read-only UI. No JavaScript, no build step.
package web

import (
	"embed"
	"html/template"
	"io"
	"reflect"
	"runtime/debug"
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

// Render executes the named page template with the shared layout.
func Render(w io.Writer, page string, data any) error {
	t, err := template.Must(
		template.New("layout.html").Funcs(funcs).ParseFS(templateFS, "templates/layout.html"),
	).ParseFS(templateFS, "templates/"+page)
	if err != nil {
		return err
	}
	return t.ExecuteTemplate(w, "layout", data)
}
