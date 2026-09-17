package httpd

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

// Repository settings for repo admins. Every control dispatches the
// command the CLI runs; the page only groups them. Destructive lifecycle
// — delete and transfer — stays on the CLI, where a typed confirmation
// is the norm.

type settingsPage struct {
	repoPage
	Topics      []string
	Branches    []gitutil.Ref
	DepsEnabled bool
	Deps        control.DepsOut
	Runners     []store.RepoRunner
	Notice      string
	Saved       bool
	Submitted   map[string]string
}

func (s *Server) settingsForm(w http.ResponseWriter, r *http.Request, u store.User) {
	s.settingsFormWith(w, r, u, s.takeFlash(w, r), nil)
}

// settingsFormWith renders the page with the given notice. submitted is
// nil on a plain GET; on a failed POST it carries the values the visitor
// typed, so a rejected value is not silently dropped.
func (s *Server) settingsFormWith(w http.ResponseWriter, r *http.Request, u store.User, notice string, submitted url.Values) {
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
	// The toggle's state comes from the store; what the last run found
	// comes from the command, so the page shows the same report the CLI
	// prints (#164).
	var deps control.DepsOut
	s.runControlInto(u, []string{"repo", "deps", "status", repo.Path()}, &deps)
	var runners []store.RepoRunner
	s.runControlInto(u, []string{"repo", "runner", "list", repo.Path()}, &runners)
	var subm map[string]string
	if submitted != nil {
		subm = map[string]string{
			"description": submitted.Get("description"),
			"website":     submitted.Get("website"),
			"topics":      submitted.Get("topics"),
		}
	}
	s.render(w, "settings.html", settingsPage{
		repoPage: p, Topics: topics, Branches: branches,
		DepsEnabled: deps.Enabled, Deps: deps,
		Runners:   runners,
		Notice:    notice,
		Saved:     strings.HasPrefix(notice, "Saved "),
		Submitted: subm,
	})
}

func (s *Server) settingsRedirect(w http.ResponseWriter, r *http.Request, msg string) {
	dest := fmt.Sprintf("/%s/%s/settings", r.PathValue("owner"), r.PathValue("repo"))
	s.setFlash(w, msg)
	http.Redirect(w, r, dest, http.StatusSeeOther)
}

// settingsSubmit routes one form to its command. Keeping the mapping in
// one place makes what the page can reach obvious.
func (s *Server) settingsSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	repo := r.PathValue("owner") + "/" + r.PathValue("repo")
	v := func(k string) string { return strings.TrimSpace(r.FormValue(k)) }
	field := r.FormValue("field")

	var argv []string
	switch field {
	case "description":
		argv = []string{"repo", "settings", "description", repo, v("description")}
	case "website":
		argv = []string{"repo", "settings", "website", repo, v("website")}
	case "visibility":
		argv = []string{"repo", "settings", "visibility", repo, v("visibility")}
	case "default-branch":
		argv = []string{"repo", "settings", "default-branch", repo, v("default-branch")}
	case "git-daemon":
		argv = []string{"repo", "settings", "git-daemon", repo, onOff(v("git-daemon"))}
	case "require-checks":
		argv = []string{"repo", "settings", "require-checks", repo, onOff(v("require-checks"))}
	case "require-resolved":
		argv = []string{"repo", "settings", "require-resolved", repo, onOff(v("require-resolved"))}
	case "require-codeowners":
		argv = []string{"repo", "settings", "require-codeowners", repo, onOff(v("require-codeowners"))}
	case "require-mr":
		argv = []string{"repo", "settings", "require-mr", repo, onOff(v("require-mr"))}
	case "require-signed":
		argv = []string{"repo", "settings", "require-signed", repo, onOff(v("require-signed"))}
	case "require-approvals":
		argv = []string{"repo", "settings", "require-approvals", repo, v("approvals")}
	case "protect":
		argv = []string{"repo", "settings", "protect", repo, v("branch")}
	case "unprotect":
		argv = []string{"repo", "settings", "unprotect", repo, v("branch")}
	case "protect-tag":
		argv = []string{"repo", "settings", "protect-tag", repo, v("glob")}
	case "unprotect-tag":
		argv = []string{"repo", "settings", "unprotect-tag", repo, v("glob")}
	case "deps":
		verb := "disable"
		if v("deps") == "on" {
			verb = "enable"
		}
		argv = []string{"repo", "deps", verb, repo}
	case "archive":
		verb := "archive"
		if v("archive") != "on" {
			verb = "unarchive"
		}
		argv = []string{"repo", verb, repo}
	case "topics":
		row, err := s.st.RepoByPath(repo)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		want := map[string]bool{}
		var order []string
		for _, t := range strings.Split(v("topics"), ",") {
			if t = strings.ToLower(strings.TrimSpace(t)); t != "" && !want[t] {
				want[t] = true
				order = append(order, t)
			}
		}
		have, err := s.st.ListTopics(row.ID)
		if err != nil {
			s.settingsRedirect(w, r, err.Error())
			return
		}
		var add, remove []string
		for _, t := range order {
			if !slices.Contains(have, t) {
				add = append(add, t)
			}
		}
		for _, t := range have {
			if !want[t] {
				remove = append(remove, t)
			}
		}
		if len(remove) > 0 {
			if _, msg, ok := s.runControl(u, append([]string{"repo", "topics", "remove", repo}, remove...)); !ok {
				s.settingsFormWith(w, r, u, msg, r.Form)
				return
			}
		}
		if len(add) > 0 {
			argv = append([]string{"repo", "topics", "add", repo}, add...)
		} else {
			s.settingsRedirect(w, r, "Saved the topics.")
			return
		}
	case "runner-add":
		body := v("key")
		if body == "" {
			s.settingsRedirect(w, r, "paste the runner's public key")
			return
		}
		msg, ok := s.runControlStdin(u, []string{"repo", "runner", "add", repo}, body+"\n")
		if ok {
			msg = ""
		}
		s.settingsRedirect(w, r, msg)
		return
	case "runner-remove":
		argv = []string{"repo", "runner", "remove", repo, v("fingerprint")}
	default:
		s.settingsRedirect(w, r, "unknown setting")
		return
	}

	_, msg, ok := s.runControl(u, argv)
	if ok {
		s.settingsRedirect(w, r, "Saved the "+fieldLabel(field)+".")
		return
	}
	s.settingsFormWith(w, r, u, msg, r.Form)
}

// fieldLabel names a settings field for the saved flash and, on
// rejection, the error notice — lower case, matching the label beside
// its control.
func fieldLabel(field string) string {
	switch field {
	case "description":
		return "description"
	case "website":
		return "website"
	case "visibility":
		return "visibility"
	case "default-branch":
		return "default branch"
	case "git-daemon":
		return "git:// serving"
	case "require-checks":
		return "required checks"
	case "require-approvals":
		return "approvals"
	case "require-resolved":
		return "review threads"
	case "require-codeowners":
		return "CODEOWNERS"
	case "require-mr":
		return "require-MR"
	case "require-signed":
		return "signed commits"
	case "protect", "unprotect":
		return "protected branch"
	case "protect-tag", "unprotect-tag":
		return "protected tag"
	case "deps":
		return "dependency scanning"
	case "archive":
		return "archive"
	case "topics":
		return "topics"
	case "runner-add", "runner-remove":
		return "runner"
	default:
		return field
	}
}

// onOff normalises a checkbox to the on|off the commands take.
func onOff(v string) string {
	if v == "on" || v == "true" {
		return "on"
	}
	return "off"
}
