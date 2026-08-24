// Package web holds the server-rendered templates and static assets for the
// read-only UI. No JavaScript, no build step.
package web

import (
	"embed"
	"html/template"
	"io"
	"runtime/debug"
	"sync"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/style.css
var StyleCSS []byte

//go:embed static/favicon.svg
var FaviconSVG []byte

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

var funcs = template.FuncMap{
	"gitbayVersion": func() string { return version() },
	// short abbreviates a commit SHA for display.
	"short": func(s string) string {
		if len(s) > 10 {
			return s[:10]
		}
		return s
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
