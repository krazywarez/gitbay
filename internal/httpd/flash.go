package httpd

import (
	"net/http"
	"net/url"
	"strings"
)

// A form action that fails redirects back to the page it came from with
// the reason. The reason used to ride the URL as ?e=, so it survived a
// reload and landed in history and bookmarks. It rides a one-shot cookie
// now: set on the redirect, read and cleared by the page that renders it
// (#119).
const flashCookie = "gitbay_notice"

// setFlash queues msg for the next page render. An empty msg sets
// nothing.
func (s *Server) setFlash(w http.ResponseWriter, msg string) {
	if msg == "" {
		return
	}
	if len(msg) > 300 {
		msg = msg[:300]
	}
	http.SetCookie(w, &http.Cookie{
		Name: flashCookie, Value: url.QueryEscape(msg), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.cfg.HTTP.TLS != "off",
		MaxAge: 60,
	})
}

// takeFlash returns the queued message, if any, and clears it.
func (s *Server) takeFlash(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(flashCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	http.SetCookie(w, s.clearCookie(flashCookie, http.SameSiteLaxMode))
	msg, err := url.QueryUnescape(c.Value)
	if err != nil {
		return ""
	}
	return msg
}

const nextCookie = "gitbay_next"

// setNext remembers the local path an anonymous visitor asked for, so
// the login that follows can return there. Only a GET path is stored:
// a POST must not be replayed.
func (s *Server) setNext(w http.ResponseWriter, path string) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || len(path) > 300 {
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name: nextCookie, Value: url.QueryEscape(path), Path: "/",
		HttpOnly: true, SameSite: http.SameSiteLaxMode,
		Secure: s.cfg.HTTP.TLS != "off", MaxAge: 600,
	})
}

// takeNext returns the remembered path once and clears it. Anything
// that is not a local path comes back empty.
func (s *Server) takeNext(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(nextCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	http.SetCookie(w, s.clearCookie(nextCookie, http.SameSiteLaxMode))
	p, err := url.QueryUnescape(c.Value)
	if err != nil || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return ""
	}
	return p
}

// peekNext reads the remembered path without clearing it, for the
// login page to say where the visitor is going.
func (s *Server) peekNext(r *http.Request) string {
	c, err := r.Cookie(nextCookie)
	if err != nil {
		return ""
	}
	p, err := url.QueryUnescape(c.Value)
	if err != nil || !strings.HasPrefix(p, "/") || strings.HasPrefix(p, "//") {
		return ""
	}
	return p
}

// clearCookie is the expiring twin of a Set-Cookie, carrying the same
// attributes the setting call used.
//
// Deletion works without them — a cookie is identified by name, domain
// and path, not by its flags — so this is consistency rather than a live
// bug. It is worth having because a reviewer comparing the set and clear
// paths should not have to work out whether the difference is deliberate,
// and because a scanner will otherwise flag the bare form every time
// (go:S2092, go:S3330, #153).
func (s *Server) clearCookie(name string, sameSite http.SameSite) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: "", Path: "/",
		HttpOnly: true, SameSite: sameSite,
		Secure: s.cfg.HTTP.TLS != "off",
		MaxAge: -1,
	}
}
