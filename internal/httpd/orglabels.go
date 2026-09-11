package httpd

import (
	"html/template"
	"net/http"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
)

// orgScope resolves the org for its labels or milestones page. Members
// see it; anyone else only when some repository under the org is
// readable. Everything else is not found, the same answer as for a
// user owner or an unknown name.
func (s *Server) orgScope(w http.ResponseWriter, r *http.Request) (store.Org, store.User, []int64, bool) {
	viewer := s.viewer(r)
	org, err := s.st.OrgByName(r.PathValue("owner"))
	if err != nil {
		s.notFound(w, r)
		return org, viewer, nil, false
	}
	readable, err := control.ReadableOrgRepoIDs(s.st, viewer, org.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return org, viewer, nil, false
	}
	role := ""
	if viewer.ID != 0 {
		role, _ = s.st.OrgRole(org.ID, viewer.ID)
	}
	if role == "" && len(readable) == 0 {
		s.notFound(w, r)
		return org, viewer, nil, false
	}
	return org, viewer, readable, true
}

func (s *Server) orgLabels(w http.ResponseWriter, r *http.Request) {
	org, viewer, readable, ok := s.orgScope(w, r)
	if !ok {
		return
	}
	labels, err := s.st.ListOrgLabels(org.ID, readable)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	stored := make(map[string]string, len(labels))
	for _, l := range labels {
		stored[l.Name] = l.Color
	}
	s.render(w, "orglabels.html", struct {
		basePage
		Org         string
		Labels      []store.Label
		LabelColors map[string]template.CSS
	}{s.baseFor(viewer), org.Name, labels, colorStyles(stored)})
}

func (s *Server) orgMilestones(w http.ResponseWriter, r *http.Request) {
	org, viewer, readable, ok := s.orgScope(w, r)
	if !ok {
		return
	}
	state := r.URL.Query().Get("state")
	if state != "closed" && state != "all" {
		state = "open"
	}
	ms, err := s.st.ListOrgMilestones(org.ID, state, readable)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	type msView struct {
		store.Milestone
		Percent int
	}
	var views []msView
	for _, m := range ms {
		v := msView{Milestone: m}
		if total := m.OpenItems + m.ClosedItems; total > 0 {
			v.Percent = m.ClosedItems * 100 / total
		}
		views = append(views, v)
	}
	s.render(w, "orgmilestones.html", struct {
		basePage
		Org        string
		State      string
		Milestones []msView
	}{s.baseFor(viewer), org.Name, state, views})
}
