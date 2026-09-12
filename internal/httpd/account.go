package httpd

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// accountKey is one SSH key as the settings page shows it: enough to
// recognise which key this is without printing the whole blob.
type accountKey struct {
	Fingerprint string
	Algo        string
	Scope       string
	Label       string
	Confirm     string // the 8 characters after SHA256: — a label can be empty
}

type accountPGP struct {
	Fingerprint string
	UIDs        []string
	Expired     bool
	Revoked     bool
	Confirm     string // the fingerprint's first 8 characters
}

// accountForm renders the account's own settings: keys, addresses, and the
// commands for everything that stays on SSH.
func (s *Server) accountForm(w http.ResponseWriter, r *http.Request, u store.User) {
	var keys []accountKey
	if list, err := s.st.ListSSHKeys(u.ID); err == nil {
		for _, k := range list {
			confirm := prefix8(strings.TrimPrefix(k.Fingerprint, "SHA256:"))
			keys = append(keys, accountKey{Fingerprint: k.Fingerprint, Algo: k.Algo, Scope: k.Scope, Label: k.Label, Confirm: confirm})
		}
	}
	var pgp []accountPGP
	if list, err := s.st.ListPGPKeys(u.ID); err == nil {
		for _, k := range list {
			var uids []string
			json.Unmarshal([]byte(k.UIDsJSON), &uids)
			confirm := prefix8(k.Fingerprint)
			pgp = append(pgp, accountPGP{
				Fingerprint: k.Fingerprint, UIDs: uids,
				Expired: k.ExpiresAt != nil, Revoked: k.RevokedAt != nil, Confirm: confirm,
			})
		}
	}
	emails, _ := s.st.ListEmails(u.ID)

	var profile control.ProfileOut
	s.runControlInto(u, []string{"profile", "show"}, &profile)
	mailOn, _ := s.st.MailEnabled(u.ID)
	watchOn, _ := s.st.WatchEnabled(u.ID)

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
		MailOn    bool
		WatchOn   bool
	}{s.baseFor(u), "account", keys, pgp, emails, profile, profileLinksText(profile.Links), s.cfg.SiteHost(),
		s.takeFlash(w, r), r.URL.Query().Get("m"), mailOn, watchOn})
}

// accountExport hands the browser the same bundle `account export`
// writes. The command is ReadOnly, so a GET is enough; the response is an
// attachment rather than a page because the bundle is a file to keep.
func (s *Server) accountExport(w http.ResponseWriter, r *http.Request, u store.User) {
	out, msg, code := s.runControlCode(u, []string{"account", "export"})
	if code != protocol.ExitOK {
		s.setFlash(w, msg)
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", u.Username+".bundle"))
	io.WriteString(w, out)
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
		if label := strings.TrimSpace(r.FormValue("label")); label != "" {
			argv = append(argv, "--label", label)
		}
		if msg, ok := s.runControlStdin(u, argv, body+"\n"); !ok {
			back(msg, "")
			return
		}
		back("", "key registered")
	case "key-remove":
		want := prefix8(strings.TrimPrefix(r.FormValue("fingerprint"), "SHA256:"))
		if ok, msg := confirmed(r, want); !ok {
			back(msg, "")
			return
		}
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
		fp := r.FormValue("fingerprint")
		want := prefix8(fp)
		if ok, msg := confirmed(r, want); !ok {
			back(msg, "")
			return
		}
		if _, msg, ok := s.runControl(u, []string{"pgp", "remove", fp}); !ok {
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
	case "email-remove":
		address := r.FormValue("address")
		if ok, msg := confirmed(r, address); !ok {
			back(msg, "")
			return
		}
		if _, msg, ok := s.runControl(u, []string{"email", "remove", address}); !ok {
			back(msg, "")
			return
		}
		back("", "address removed")
	case "email-primary":
		if _, msg, ok := s.runControl(u, []string{"email", "primary", r.FormValue("address")}); !ok {
			back(msg, "")
			return
		}
		back("", "primary address changed")
	case "notify-mail", "notify-watch":
		pref := strings.TrimPrefix(r.FormValue("field"), "notify-")
		state := "off"
		if r.FormValue(pref) == "on" {
			state = "on"
		}
		if _, msg, ok := s.runControl(u, []string{"notifications", "settings", pref, state}); !ok {
			back(msg, "")
			return
		}
		back("", "notification preferences saved")
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
