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
// command the CLI runs; the page only groups them. Delete and transfer
// ask for the repository's path to be typed first.

type settingsPage struct {
	repoPage
	Topics      []string
	Branches    []gitutil.Ref
	DepsEnabled bool
	Deps        control.DepsOut
	Runners     []store.RepoRunner
	Access      []accessRow
	Hooks       []hookRow
	Deliveries  []deliveryRow
	Notice      string
	Saved       bool
	Reauth      bool // Notice is the stale-session refusal: link to sign in
	Submitted   map[string]string
}

type accessRow struct {
	User   string `json:"user"`
	Role   string `json:"role"`
	Source string `json:"source"`
}

type hookRow struct {
	ID     int64  `json:"id"`
	URL    string `json:"url"`
	Events string `json:"events"`
	Secret bool   `json:"has_secret"`
}

type deliveryRow struct {
	ID         int64  `json:"id"`
	URL        string `json:"url"`
	Event      string `json:"event"`
	Status     string `json:"status"`
	Attempts   int    `json:"attempts"`
	LastStatus int    `json:"last_status"`
	LastError  string `json:"last_error"`
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
	var access []accessRow
	s.runControlInto(u, []string{"repo", "access", "list", repo.Path()}, &access)
	var hooks []hookRow
	s.runControlInto(u, []string{"webhook", "list", repo.Path()}, &hooks)
	var deliveries []deliveryRow
	s.runControlInto(u, []string{"webhook", "deliveries", repo.Path(), "--limit", "20"}, &deliveries)
	var subm map[string]string
	if submitted != nil {
		subm = map[string]string{
			"description": submitted.Get("description"),
			"website":     submitted.Get("website"),
			"topics":      submitted.Get("topics"),
			"key":         submitted.Get("key"),
			"user":        submitted.Get("user"),
			"role":        submitted.Get("role"),
			"url":         submitted.Get("url"),
			"events":      submitted.Get("events"),
			"name":        submitted.Get("name"),
			"new-owner":   submitted.Get("new-owner"),
		}
	}
	s.render(w, "settings.html", settingsPage{
		repoPage: p, Topics: topics, Branches: branches,
		DepsEnabled: deps.Enabled, Deps: deps,
		Runners: runners,
		Access:  access, Hooks: hooks, Deliveries: deliveries,
		Notice:    notice,
		Saved:     strings.HasPrefix(notice, "Saved "),
		Reauth:    s.reauthNotice(w, notice, r.URL.Path),
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
	row, ok := s.repoForUser(w, r, u, policyCanAdmin)
	if !ok {
		return
	}
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
	case "require-contexts":
		argv = append([]string{"repo", "settings", "require-contexts", repo}, strings.Fields(v("contexts"))...)
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
		removed := false
		if len(remove) > 0 {
			if _, msg, ok := s.runControl(u, append([]string{"repo", "topics", "remove", repo}, remove...)); !ok {
				s.settingsFormWith(w, r, u, msg, r.Form)
				return
			}
			removed = true
		}
		if len(add) > 0 {
			argv = append([]string{"repo", "topics", "add", repo}, add...)
		} else {
			s.settingsRedirect(w, r, "Saved the topics.")
			return
		}
		if removed {
			if _, msg, ok := s.runControl(u, argv); !ok {
				s.settingsFormWith(w, r, u, "Removed "+strings.Join(remove, ", ")+"; "+msg, r.Form)
				return
			}
			s.settingsRedirect(w, r, "Saved the "+fieldLabel(field)+".")
			return
		}
	case "runner-add":
		body := v("key")
		if body == "" {
			s.settingsRedirect(w, r, "paste the runner's public key")
			return
		}
		msg, ok := s.runControlStdin(u, []string{"repo", "runner", "add", repo}, body+"\n")
		if !ok {
			s.settingsFormWith(w, r, u, msg, r.Form)
			return
		}
		s.settingsRedirect(w, r, "Saved the runner.")
		return
	case "runner-remove":
		argv = []string{"repo", "runner", "remove", repo, v("fingerprint")}
	case "access-grant":
		argv = []string{"repo", "access", "grant", repo, v("user"), v("role")}
	case "access-revoke":
		argv = []string{"repo", "access", "revoke", repo, v("user")}
	case "webhook-add":
		// The secret goes to the command on stdin and nowhere else: not
		// argv, not the re-rendered form, not the notice.
		argv = []string{"webhook", "add", repo, v("url")}
		if ev := strings.ReplaceAll(v("events"), " ", ""); ev != "" {
			argv = append(argv, "--events", ev)
		}
		var stdin string
		if secret := strings.TrimRight(r.FormValue("secret"), "\r\n"); secret != "" {
			argv = append(argv, "--secret", "-")
			stdin = secret + "\n"
		}
		msg, ok := s.runControlStdin(u, argv, stdin)
		if !ok {
			s.settingsFormWith(w, r, u, msg, r.Form)
			return
		}
		s.settingsRedirect(w, r, "Saved the webhook.")
		return
	case "webhook-remove":
		argv = []string{"webhook", "remove", repo, v("id")}
	case "webhook-redeliver":
		if _, msg, ok := s.runControl(u, []string{"webhook", "redeliver", repo, v("delivery")}); !ok {
			s.settingsFormWith(w, r, u, msg, r.Form)
			return
		}
		s.settingsRedirect(w, r, "Queued the delivery again.")
		return
	case "rename":
		if _, msg, ok := s.runControl(u, []string{"repo", "rename", repo, v("name")}); !ok {
			s.settingsFormWith(w, r, u, msg, r.Form)
			return
		}
		s.setFlash(w, "Saved the name.")
		http.Redirect(w, r, "/"+r.PathValue("owner")+"/"+v("name")+"/settings", http.StatusSeeOther)
		return
	case "transfer":
		if ok, msg := confirmed(r, repo); !ok {
			s.settingsFormWith(w, r, u, msg, r.Form)
			return
		}
		if _, msg, ok := s.runControl(u, []string{"repo", "transfer", repo, v("new-owner")}); !ok {
			s.settingsFormWith(w, r, u, msg, r.Form)
			return
		}
		s.setFlash(w, "Saved the owner: transferred to "+v("new-owner")+".")
		http.Redirect(w, r, "/"+v("new-owner")+"/"+r.PathValue("repo")+"/settings", http.StatusSeeOther)
		return
	case "delete":
		if ok, msg := confirmed(r, repo); !ok {
			s.settingsFormWith(w, r, u, msg, r.Form)
			return
		}
		if _, msg, ok := s.runControl(u, []string{"repo", "delete", repo, "--yes"}); !ok {
			s.settingsFormWith(w, r, u, msg, r.Form)
			return
		}
		s.setFlash(w, "Deleted "+repo+".")
		http.Redirect(w, r, "/"+r.PathValue("owner"), http.StatusSeeOther)
		return
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
	case "require-contexts":
		return "required contexts"
	case "require-approvals":
		return "approvals"
	case "require-resolved":
		return "review threads"
	case "require-codeowners":
		return "CODEOWNERS"
	case "require-mr":
		return "merge request requirement"
	case "require-signed":
		return "signed commits"
	case "protect", "unprotect":
		return "protected branch"
	case "protect-tag", "unprotect-tag":
		return "protected tag"
	case "deps":
		return "dependency scanning"
	case "archive":
		return "archived state"
	case "topics":
		return "topics"
	case "runner-add", "runner-remove":
		return "runner"
	case "access-grant", "access-revoke":
		return "access"
	case "webhook-remove":
		return "webhook"
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
