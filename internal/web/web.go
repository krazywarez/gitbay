// Package web holds the server-rendered templates and static assets for the
// read-only UI. No JavaScript, no build step.
package web

import (
	"embed"
	"html/template"
	"io"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static/style.css
var StyleCSS []byte

// Render executes the named page template with the shared layout.
func Render(w io.Writer, page string, data any) error {
	t, err := template.Must(
		template.ParseFS(templateFS, "templates/layout.html"),
	).ParseFS(templateFS, "templates/"+page)
	if err != nil {
		return err
	}
	return t.ExecuteTemplate(w, "layout", data)
}
