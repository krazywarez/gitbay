package httpd

import (
	"fmt"
	"net/http"
	"strings"
)

// Status badges. A badge is a small SVG served for public repositories,
// so a README on any host can show whether the latest build passed.
// Private repositories 404 like everywhere else — a badge must not leak
// that a repo exists, let alone its state.

// badgeColors are the shield fills per build state.
var badgeColors = map[string]string{
	"success": "#2da44e",
	"failure": "#cf222e",
	"running": "#bf8700",
	"pending": "#bf8700",
	"unknown": "#6b7280",
}

// badgeWidth approximates Verdana 11px advance so the pill fits its text
// without shipping a font metric table.
func badgeWidth(s string) int {
	w := 0
	for _, r := range s {
		switch {
		case strings.ContainsRune("iljtIJ1.,:;'", r):
			w += 4
		case strings.ContainsRune("mwMW", r):
			w += 10
		case r >= 'A' && r <= 'Z':
			w += 8
		default:
			w += 7
		}
	}
	return w + 10
}

// badgeSVG renders a two-part pill: a grey label and a coloured state.
func badgeSVG(label, state string) string {
	color := badgeColors[state]
	if color == "" {
		color = badgeColors["unknown"]
	}
	lw, sw := badgeWidth(label), badgeWidth(state)
	total := lw + sw
	return fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="20" role="img" aria-label="%s: %s">
<title>%s: %s</title>
<rect width="%d" height="20" rx="3" fill="#444d56"/>
<rect x="%d" width="%d" height="20" rx="3" fill="%s"/>
<rect x="%d" width="4" height="20" fill="%s"/>
<g fill="#fff" text-anchor="middle" font-family="Verdana,DejaVu Sans,sans-serif" font-size="11">
<text x="%d" y="14">%s</text>
<text x="%d" y="14">%s</text>
</g>
</svg>`, total, label, state, label, state,
		total, lw, sw, color, lw, color, lw/2, label, lw+sw/2, state)
}

// buildBadge answers GET /{owner}/{repo}/badge/build.svg[?job=name].
func (s *Server) buildBadge(w http.ResponseWriter, r *http.Request) {
	label, state, ok := s.badgeState(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	badgeHeaders(w, "image/svg+xml; charset=utf-8")
	fmt.Fprint(w, badgeSVG(label, state))
}

// buildBadgePNG answers GET /{owner}/{repo}/badge/build.png[?job=name], for
// readers that cannot decode SVG.
func (s *Server) buildBadgePNG(w http.ResponseWriter, r *http.Request) {
	label, state, ok := s.badgeState(r)
	if !ok {
		http.NotFound(w, r)
		return
	}
	out, err := badgePNG(label, state)
	if err != nil {
		http.Error(w, "badge unavailable", http.StatusInternalServerError)
		return
	}
	badgeHeaders(w, "image/png")
	w.Write(out)
}

// badgeState resolves the repo and its latest build. Private repositories
// report not ok — a badge must not leak that a repo exists.
func (s *Server) badgeState(r *http.Request) (label, state string, ok bool) {
	repo, ok := s.publicRepo(r.PathValue("owner"), strings.TrimSuffix(r.PathValue("repo"), ".git"))
	if !ok {
		return "", "", false
	}
	job := r.URL.Query().Get("job")
	label = "build"
	if job != "" {
		label = job
	}
	state = "unknown"
	if b, err := s.st.LatestBuild(repo.ID, job); err == nil {
		state = b.Status
	}
	return label, state, true
}

func badgeHeaders(w http.ResponseWriter, contentType string) {
	w.Header().Set("Content-Type", contentType)
	// Badges are read by other people's caches; keep them briefly fresh
	// rather than pinned to a stale result.
	w.Header().Set("Cache-Control", "max-age=60, must-revalidate")
	w.Header().Set("X-Content-Type-Options", "nosniff")
}
