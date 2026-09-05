package httpd

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
)

// accountKey is one SSH key as the settings page shows it: enough to
// recognise which key this is without printing the whole blob.
type accountKey struct {
	Fingerprint string
	Algo        string
	Scope       string
}

type accountPGP struct {
	Fingerprint string
	UIDs        []string
	Expired     bool
	Revoked     bool
}

// accountForm renders the account's own settings: keys, addresses, and the
// commands for everything that stays on SSH.
func (s *Server) accountForm(w http.ResponseWriter, r *http.Request, u store.User) {
	var keys []accountKey
	if list, err := s.st.ListSSHKeys(u.ID); err == nil {
		for _, k := range list {
			keys = append(keys, accountKey{Fingerprint: k.Fingerprint, Algo: k.Algo, Scope: k.Scope})
		}
	}
	var pgp []accountPGP
	if list, err := s.st.ListPGPKeys(u.ID); err == nil {
		for _, k := range list {
			var uids []string
			json.Unmarshal([]byte(k.UIDsJSON), &uids)
			pgp = append(pgp, accountPGP{
				Fingerprint: k.Fingerprint, UIDs: uids,
				Expired: k.ExpiresAt != nil, Revoked: k.RevokedAt != nil,
			})
		}
	}
	emails, _ := s.st.ListEmails(u.ID)

	var profile control.ProfileOut
	s.runControlInto(u, []string{"profile", "show"}, &profile)

	s.render(w, "account.html", struct {
		basePage
		Tab       string // marks the rail's Settings row as current
		Keys      []accountKey
		PGP       []accountPGP
		Emails    []store.Email
		Profile   control.ProfileOut
		LinksText string
		Host      string
		Notice    string
		Message   string
	}{s.baseFor(u), "account", keys, pgp, emails, profile, profileLinksText(profile.Links), s.cfg.SiteHost(),
		s.takeFlash(w, r), r.URL.Query().Get("m")})
}

// profileLinksText turns a profile's links into the form the textarea
// shows and reads back: one per line, "label|url" when there is a label
// and the bare url otherwise.
func profileLinksText(links []store.ProfileLink) string {
	lines := make([]string, len(links))
	for i, l := range links {
		if l.Label != "" {
			lines[i] = l.Label + "|" + l.URL
		} else {
			lines[i] = l.URL
		}
	}
	return strings.Join(lines, "\n")
}

// profileLinkArgs turns the textarea back into the --link values profile
// set expects: one per non-blank line, or a single empty one to clear the
// list when the field was emptied.
func profileLinkArgs(raw string) []string {
	var links []string
	for _, line := range strings.Split(raw, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			links = append(links, line)
		}
	}
	if links == nil {
		return []string{""}
	}
	return links
}

// accountSubmit routes the account forms to their commands. Keys,
// addresses and the profile are the whole surface — no secret is accepted
// over the web.
func (s *Server) accountSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	back := func(msg, note string) {
		q := ""
		if note != "" {
			q = "?m=" + url.QueryEscape(note)
		}
		s.setFlash(w, msg)
		http.Redirect(w, r, "/settings"+q, http.StatusSeeOther)
	}

	switch r.FormValue("field") {
	case "key-add":
		body := strings.TrimSpace(r.FormValue("key"))
		if body == "" {
			back("paste a public key in authorized_keys format", "")
			return
		}
		argv := []string{"keys", "add"}
		if scope := r.FormValue("scope"); scope == "git" {
			argv = append(argv, "--scope", "git")
		}
		if msg, ok := s.runControlStdin(u, argv, body+"\n"); !ok {
			back(msg, "")
			return
		}
		back("", "key registered")
	case "key-remove":
		if _, msg, ok := s.runControl(u, []string{"keys", "remove", r.FormValue("fingerprint")}); !ok {
			back(msg, "")
			return
		}
		back("", "key removed")
	case "pgp-add":
		body := strings.TrimSpace(r.FormValue("key"))
		if body == "" {
			back("paste an armored OpenPGP public key", "")
			return
		}
		if msg, ok := s.runControlStdin(u, []string{"pgp", "add"}, body+"\n"); !ok {
			back(msg, "")
			return
		}
		back("", "PGP key registered")
	case "pgp-remove":
		if _, msg, ok := s.runControl(u, []string{"pgp", "remove", r.FormValue("fingerprint")}); !ok {
			back(msg, "")
			return
		}
		back("", "PGP key removed")
	case "email-add":
		if _, msg, ok := s.runControl(u, []string{"email", "add", strings.TrimSpace(r.FormValue("address"))}); !ok {
			back(msg, "")
			return
		}
		back("", "check that inbox for a verification code")
	case "email-verify":
		if _, msg, ok := s.runControl(u, []string{"email", "verify", strings.TrimSpace(r.FormValue("code"))}); !ok {
			back(msg, "")
			return
		}
		back("", "address verified")
	case "profile":
		format := r.FormValue("format")
		if format != "org" {
			format = "md"
		}
		argv := []string{"profile", "set",
			"--description", r.FormValue("description"),
			"--website", r.FormValue("website"),
			"--about-format", format,
			"--file", "-",
		}
		for _, link := range profileLinkArgs(r.FormValue("links")) {
			argv = append(argv, "--link", link)
		}
		if msg, ok := s.runControlStdin(u, argv, r.FormValue("about")); !ok {
			back(msg, "")
			return
		}
		back("", "profile updated")
	default:
		back("unknown form", "")
	}
}
