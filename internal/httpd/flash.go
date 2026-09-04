package httpd

import (
	"net/http"
	"net/url"
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
	http.SetCookie(w, &http.Cookie{Name: flashCookie, Value: "", Path: "/", MaxAge: -1})
	msg, err := url.QueryUnescape(c.Value)
	if err != nil {
		return ""
	}
	return msg
}
