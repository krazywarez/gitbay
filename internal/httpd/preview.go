package httpd

import (
	"html/template"
	"net/http"
	"net/url"
	"path"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// A markup form's Preview button posts to the form's own action with
// preview set. The handler renders the body and hands back the page it
// came from instead of writing anything, so what appears above the
// textarea is the rendering the thread will show. There is no
// JavaScript on this site (script-src 'none'), so a preview is a round
// trip, the way the blob page's rendered/source toggle is (#235).
//
// Form names the textarea the draft belongs to, because a page can
// carry more than one: an issue has both an edit form and a compose
// box, and only the one that was submitted gets the preview. The other
// fields of that form ride along in vals so the page can put them back.
type draft struct {
	Form   string
	Body   string
	Format string
	HTML   template.HTML
	vals   url.Values
}

// Is reports whether this draft belongs to the named form. Templates
// call it to place the preview and open the right box; a nil draft
// answers false, so a page renders unchanged when nothing was
// previewed.
func (d *draft) Is(form string) bool { return d != nil && d.Form == form }

// Or is what a field of the named form should show: what was typed when
// this draft is that form's, and the stored value otherwise. It is how
// a previewed form comes back with everything still in it.
func (d *draft) Or(form, field, fallback string) string {
	if !d.Is(form) {
		return fallback
	}
	return d.vals.Get(field)
}

// wantsPreview reports whether the submission asked to see the markup
// rather than save it.
func wantsPreview(r *http.Request) bool { return r.FormValue("preview") != "" }

// draftFor renders a repository-scoped body the way the thread renders
// it, autolinks and all. The caller supplies the format rather than the
// helper reading the picker, because only the create forms have one: an
// edit keeps the item's stored format, and a comment is markdown, which
// is what the command does with no --format.
func (s *Server) draftFor(r *http.Request, repo store.Repo, form, field, format string) *draft {
	return s.draftWith(r, form, format, r.FormValue(field), s.ugcFor(r, repo))
}

// draftWith is draftFor for text with no repository behind it: a
// profile's about, or a file in the editor. The renderer is the one
// that surface uses, so the preview matches where the text will land.
func (s *Server) draftWith(r *http.Request, form, format, body string, render ugcRenderer) *draft {
	r.ParseForm() // Or reads r.Form; a handler may not have touched it yet.
	return &draft{Form: form, Body: body, Format: format,
		HTML: render(strings.TrimSpace(body), format), vals: r.Form}
}

// markupFile reports whether a path is one the forge renders: the blob
// page's rendered view and the editor's preview take the same set.
func markupFile(filePath string) bool {
	switch path.Ext(strings.ToLower(filePath)) {
	case ".md", ".markdown", ".org":
		return true
	}
	return false
}

// bodyFormat reads the format picker, defaulting to markdown the way
// every submit handler does.
func bodyFormat(r *http.Request) string {
	if r.FormValue("format") == "org" {
		return "org"
	}
	return "md"
}
