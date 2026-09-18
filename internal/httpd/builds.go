package httpd

import (
	"net/http"
	"net/url"
	"strconv"

	"gitbay.org/gitbay/internal/control"
)

// buildFilter is the builds page's GET filter: branch, status and job,
// each optional and independent (#224).
type buildFilter struct {
	Ref    string
	Status string
	Job    string
}

// buildFilterLink is one entry in the nav.filters row above the build
// list: a status or a job, with the other two parameters carried along so
// clicking one never drops another.
type buildFilterLink struct {
	Label  string
	Href   string
	Active bool
}

// buildStatuses is the fixed vocabulary a build's status takes, in the
// order the nav.filters row offers them.
var buildStatuses = []string{"pending", "running", "success", "failure", "cancelled"}

// filterLinks builds the nav.filters row: "all" (clears status and job),
// one link per status, and one per job the repository's CI config names.
// Each link keeps the filter's other two parameters and net/url encodes
// them, so a branch name or job name with an odd character does not break
// the query string it lands in.
func filterLinks(f buildFilter, jobs []control.JobOut) []buildFilterLink {
	href := func(status, job string) string {
		q := url.Values{}
		if f.Ref != "" {
			q.Set("ref", f.Ref)
		}
		if status != "" {
			q.Set("status", status)
		}
		if job != "" {
			q.Set("job", job)
		}
		return "?" + q.Encode()
	}
	links := []buildFilterLink{
		{Label: "all", Href: href("", ""), Active: f.Status == "" && f.Job == ""},
	}
	for _, s := range buildStatuses {
		links = append(links, buildFilterLink{Label: s, Href: href(s, f.Job), Active: f.Status == s})
	}
	for _, j := range jobs {
		links = append(links, buildFilterLink{Label: j.Name, Href: href(f.Status, j.Name), Active: f.Job == j.Name})
	}
	return links
}

// distinctRefs lists each ref among builds once, in order, plus the
// current filter value if it is not already there. It backs the branch
// field's <datalist> suggestions, not a claim about what branches exist:
// a ref that matched nothing under the current status/job filter still
// belongs in the list the person typed it from.
func distinctRefs(builds []control.BuildOut, current string) []string {
	seen := map[string]bool{}
	var refs []string
	add := func(ref string) {
		if ref != "" && !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	for _, b := range builds {
		add(b.Ref)
	}
	add(current)
	return refs
}

// buildRun is one commit's builds, grouped for display: the builds tab
// reads by commit, not by job, so a push that runs three jobs shows as one
// row with three chips rather than three unrelated rows (#224).
type buildRun struct {
	SHA       string
	Ref       string
	CreatedAt string
	Status    string
	Builds    []control.BuildOut
}

// runStatusPriority orders combinedStatus's worst-first check: a run reads
// as its least finished or least successful build.
var runStatusPriority = []string{"failure", "cancelled", "running", "pending"}

// combinedStatus is the run's status: the worst of its builds' statuses,
// success only when every one of them is.
func combinedStatus(builds []control.BuildOut) string {
	statuses := make([]string, len(builds))
	for i, b := range builds {
		statuses[i] = b.Status
	}
	return worstStatus(statuses)
}

// worstStatus is combinedStatus's ordering rule, factored out so the
// dashboard feed can apply the same worst-first precedence to a folded
// build run (D04). A status outside runStatusPriority (a future state
// such as "skipped") is still not "success": it is returned unchanged
// rather than falling through and reading as green.
func worstStatus(statuses []string) string {
	has := map[string]bool{}
	for _, s := range statuses {
		has[s] = true
	}
	for _, s := range runStatusPriority {
		if has[s] {
			return s
		}
	}
	for _, s := range statuses {
		if s != "success" {
			return s
		}
	}
	return "success"
}

// groupRuns folds consecutive builds of the same commit into one run.
// build list orders builds newest first, so one push's jobs are adjacent;
// this does not sort or otherwise assume anything beyond that adjacency.
func groupRuns(builds []control.BuildOut) []buildRun {
	var runs []buildRun
	for _, b := range builds {
		if n := len(runs); n > 0 && runs[n-1].SHA == b.SHA {
			runs[n-1].Builds = append(runs[n-1].Builds, b)
			continue
		}
		runs = append(runs, buildRun{SHA: b.SHA, Ref: b.Ref, CreatedAt: b.CreatedAt, Builds: []control.BuildOut{b}})
	}
	for i := range runs {
		runs[i].Status = combinedStatus(runs[i].Builds)
	}
	return runs
}

func (s *Server) builds(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "builds"
	viewer := s.webViewer(r)

	qv := r.URL.Query()
	filter := buildFilter{Ref: qv.Get("ref"), Status: qv.Get("status"), Job: qv.Get("job")}
	argv := []string{"build", "list", p.Repo.Path()}
	if filter.Ref != "" {
		argv = append(argv, "--ref", filter.Ref)
	}
	if filter.Status != "" {
		argv = append(argv, "--status", filter.Status)
	}
	if filter.Job != "" {
		argv = append(argv, "--job", filter.Job)
	}

	var builds []control.BuildOut
	s.runControlInto(viewer, argv, &builds)

	// The jobs a trigger can name. A repo without a CI config has none;
	// that is not an error for this page.
	var jobs []control.JobOut
	s.runControlInto(viewer, []string{"build", "jobs", p.Repo.Path()}, &jobs)

	s.render(w, "builds.html", struct {
		repoPage
		Builds      []control.BuildOut
		Jobs        []control.JobOut
		Runs        []buildRun
		Filter      buildFilter
		FilterLinks []buildFilterLink
		Refs        []string
		CanWrite    bool
		Notice      string
	}{p, builds, jobs, groupRuns(builds), filter, filterLinks(filter, jobs), distinctRefs(builds, filter.Ref),
		s.canWriteRepo(r, p.Repo), s.takeFlash(w, r)})
}

func (s *Server) build(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "builds"
	if _, err := strconv.ParseInt(r.PathValue("n"), 10, 64); err != nil {
		s.notFound(w, r)
		return
	}
	n := r.PathValue("n")
	viewer := s.webViewer(r)

	var b control.BuildOut
	if _, ok := s.runControlInto(viewer, []string{"build", "show", p.Repo.Path(), n}, &b); !ok {
		s.notFound(w, r)
		return
	}
	log, _, _ := s.runControl(viewer, []string{"build", "log", p.Repo.Path(), n})

	s.render(w, "build.html", struct {
		repoPage
		Build    control.BuildOut
		Log      string
		CanWrite bool
		Notice   string
	}{p, b, log, s.canWriteRepo(r, p.Repo), s.takeFlash(w, r)})
}
