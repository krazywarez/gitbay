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
	repo, ok := s.publicRepo(r.PathValue("owner"), strings.TrimSuffix(r.PathValue("repo"), ".git"))
	if !ok {
		http.NotFound(w, r)
		return
	}
	job := r.URL.Query().Get("job")
	label := "build"
	if job != "" {
		label = job
	}
	state := "unknown"
	if b, err := s.st.LatestBuild(repo.ID, job); err == nil {
		state = b.Status
	}
	writeBadge(w, label, state)
}

func writeBadge(w http.ResponseWriter, label, state string) {
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	// Badges are read by other people's caches; keep them briefly fresh
	// rather than pinned to a stale result.
	w.Header().Set("Cache-Control", "max-age=60, must-revalidate")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	fmt.Fprint(w, badgeSVG(label, state))
}
