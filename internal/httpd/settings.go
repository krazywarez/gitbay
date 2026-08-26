package httpd

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

// Repository settings for repo admins. Every control dispatches the
// command the CLI runs; the page only groups them. Destructive lifecycle
// — delete and transfer — stays on the CLI, where a typed confirmation
// is the norm.

type settingsPage struct {
	repoPage
	Topics   []string
	Branches []gitutil.Ref
	Notice   string
}

func (s *Server) settingsForm(w http.ResponseWriter, r *http.Request, u store.User) {
	repo, ok := s.repoForUser(w, r, u, policyCanAdmin)
	if !ok {
		return
	}
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "settings"
	topics, _ := s.st.ListTopics(repo.ID)
	branches, _ := gitutil.Refs(p.Dir, "heads")
	s.render(w, "settings.html", settingsPage{
		repoPage: p, Topics: topics, Branches: branches,
		Notice: r.URL.Query().Get("e"),
	})
}

func (s *Server) settingsRedirect(w http.ResponseWriter, r *http.Request, msg string) {
	dest := fmt.Sprintf("/%s/%s/settings", r.PathValue("owner"), r.PathValue("repo"))
	if msg != "" {
		if len(msg) > 300 {
			msg = msg[:300]
		}
		dest += "?e=" + url.QueryEscape(msg)
	}
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// settingsSubmit routes one form to its command. Keeping the mapping in
// one place makes what the page can reach obvious.
func (s *Server) settingsSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }

	var argv []string
	switch r.FormValue("field") {
	case "description":
		argv = []string{"repo", "settings", "description", repo, v("description")}
	case "website":
		argv = []string{"repo", "settings", "website", repo, v("website")}
	case "visibility":
		argv = []string{"repo", "settings", "visibility", repo, v("visibility")}
	case "git-daemon":
		argv = []string{"repo", "settings", "git-daemon", repo, onOff(v("git-daemon"))}
	case "require-checks":
		argv = []string{"repo", "settings", "require-checks", repo, onOff(v("require-checks"))}
	case "require-resolved":
		argv = []string{"repo", "settings", "require-resolved", repo, onOff(v("require-resolved"))}
	case "require-signed":
		argv = []string{"repo", "settings", "require-signed", repo, onOff(v("require-signed"))}
	case "require-approvals":
		argv = []string{"repo", "settings", "require-approvals", repo, v("approvals")}
	case "protect":
		argv = []string{"repo", "settings", "protect", repo, v("branch")}
	case "unprotect":
		argv = []string{"repo", "settings", "unprotect", repo, v("branch")}
	case "archive":
		verb := "archive"
		if v("archive") != "on" {
			verb = "unarchive"
		}
		argv = []string{"repo", verb, repo}
	case "topics":
		if add := strings.Fields(v("add")); len(add) > 0 {
			argv = append([]string{"repo", "topics", "add", repo}, add...)
		} else if rm := strings.Fields(v("remove")); len(rm) > 0 {
			argv = append([]string{"repo", "topics", "remove", repo}, rm...)
		} else {
			s.settingsRedirect(w, r, "name at least one topic")
			return
		}
	default:
		s.settingsRedirect(w, r, "unknown setting")
		return
	}

	_, msg, ok := s.runControl(u, argv)
	if ok {
		msg = ""
	}
	s.settingsRedirect(w, r, msg)
}

// onOff normalises a checkbox to the on|off the commands take.
func onOff(v string) string {
	if v == "on" || v == "true" {
		return "on"
	}
	return "off"
}
