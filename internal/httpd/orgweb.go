package httpd

import (
	"net/http"
	"net/url"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// teamView is a team with the two things an admin needs to see about it:
// who is in it and what it reaches.
type teamView struct {
	Name    string
	Members []string
	Grants  []store.TeamGrant
}

// orgAdminView gathers the management state for an organization page. It
// returns ok=false for user pages and for viewers who do not administer the
// org, so the page renders read-only for everyone else.
func (s *Server) orgAdminView(viewer store.User, kind, name string) (teams []teamView, ok bool) {
	if kind != "org" || viewer.ID == 0 {
		return nil, false
	}
	org, err := s.st.OrgByName(name)
	if err != nil {
		return nil, false
	}
	if role, _ := s.st.OrgRole(org.ID, viewer.ID); role != "admin" {
		return nil, false
	}
	list, _ := s.st.ListTeams(org.ID)
	for _, t := range list {
		members, _ := s.st.TeamMembers(t.ID)
		grants, _ := s.st.TeamGrants(t.ID)
		teams = append(teams, teamView{Name: t.Name, Members: members, Grants: grants})
	}
	return teams, true
}

// orgSubmit routes the organization forms on an owner page. Every branch
// dispatches the matching control command, so membership rules and audit
// entries stay in one implementation.
func (s *Server) orgSubmit(w http.ResponseWriter, r *http.Request, u store.User) {
	owner := r.PathValue("owner")
	back := func(msg string) {
		dest := "/" + owner
		if msg != "" {
			dest += "?e=" + url.QueryEscape(msg)
		}
		http.Redirect(w, r, dest, http.StatusSeeOther)
	}
	field := r.FormValue("field")
	team := strings.TrimSpace(r.FormValue("team"))
	user := strings.TrimSpace(r.FormValue("user"))

	var argv []string
	switch field {
	case "member-add":
		argv = []string{"org", "members", "add", owner, user}
		if role := r.FormValue("role"); role == "admin" || role == "member" {
			argv = append(argv, "--role", role)
		}
	case "member-remove":
		argv = []string{"org", "members", "remove", owner, user}
	case "team-create":
		argv = []string{"org", "team", "create", owner, team}
	case "team-delete":
		argv = []string{"org", "team", "delete", owner, team}
	case "team-add":
		argv = append([]string{"org", "team", "add", owner, team}, strings.Fields(user)...)
	case "team-remove":
		argv = append([]string{"org", "team", "remove", owner, team}, strings.Fields(user)...)
	case "team-grant":
		argv = []string{"org", "team", "grant", owner, team,
			strings.TrimSpace(r.FormValue("repo")), r.FormValue("role")}
	case "team-revoke":
		argv = []string{"org", "team", "revoke", owner, team, strings.TrimSpace(r.FormValue("repo"))}
	default:
		back("unknown form")
		return
	}
	if _, msg, ok := s.runControl(u, argv); !ok {
		back(msg)
		return
	}
	back("")
}
