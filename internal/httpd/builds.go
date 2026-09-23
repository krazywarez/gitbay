package httpd

import (
	"bytes"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
)

// buildFilter is the builds page's GET filter: branch, status and job,
// each optional and independent (#224).
type buildFilter struct {
	Ref    string
	Status string
	Job    string
}

// buildFilterLink is one of the links buildFacets splits into the side
// column's Status and Jobs groups: a status or a job, with the other two
// parameters carried along so clicking one never drops another.
type buildFilterLink struct {
	Label  string
	Href   string
	Active bool
}

// buildStatuses is the fixed vocabulary a build's status takes, in the
// order the Status group offers them. Its length is also where buildFacets
// cuts filterLinks' rows apart.
var buildStatuses = []string{"pending", "running", "success", "failure", "cancelled"}

// filterLinks builds the status and job rows: "all" (clears status and
// job), one link per status, and one per job the repository's CI config
// names. Each link keeps the filter's other two parameters and net/url encodes
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

// maxBranchFacets caps the Branches group, the way topicFacets caps
// topics: a page of builds names as many refs as it likes and the column
// is not a branch listing. The field below the column takes any ref.
const maxBranchFacets = 10

// buildFacets is the builds page's side column: filterLinks' rows split
// into their groups, plus one link per branch seen, which keeps status
// and job and clears itself when active.
func buildFacets(f buildFilter, jobs []control.JobOut, refs []string) []facetGroup {
	links := filterLinks(f, jobs)
	n := 1 + len(buildStatuses)
	status := facetGroup{Title: "Status"}
	for _, l := range links[:n] {
		status.Items = append(status.Items, facetItem{Label: l.Label, Href: l.Href, Active: l.Active})
	}
	job := facetGroup{Title: "Jobs"}
	for _, l := range links[n:] {
		job.Items = append(job.Items, facetItem{Label: l.Label, Href: l.Href, Active: l.Active})
	}
	branch := facetGroup{Title: "Branches"}
	base := url.Values{"ref": {f.Ref}, "status": {f.Status}, "job": {f.Job}}
	shown := refs
	if len(shown) > maxBranchFacets {
		shown = shown[:maxBranchFacets]
		// the ref in force belongs in the group wherever it sits, or the
		// filter it set cannot be cleared from the column. The reslice
		// caps the capacity so the append copies instead of writing
		// through to refs.
		if i := slices.Index(refs, f.Ref); i >= maxBranchFacets {
			shown = append(shown[:maxBranchFacets-1:maxBranchFacets-1], refs[i])
		}
	}
	for _, ref := range shown {
		active := ref == f.Ref
		href := facetHref(base, "ref", ref)
		if active {
			href = facetHref(base, "ref", "")
		}
		branch.Items = append(branch.Items, facetItem{Label: ref, Href: href, Active: active})
	}
	return []facetGroup{status, job, branch}
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
// row with three chips rather than three unrelated rows (#224). A run is
// one queueing of a commit, not the commit — see groupRuns (#240).
type buildRun struct {
	SHA       string
	Subject   string
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

// groupRuns folds consecutive builds of the same commit and the same
// created_at into one run. build list orders builds newest first, so one
// push's jobs are adjacent; this does not sort or otherwise assume anything
// beyond that adjacency.
//
// The commit alone is not the run. A scheduled job fires against the same
// sha every tick for as long as the branch tip does not move, so keying on
// the sha collapsed a week of daily runs into one row carrying the newest
// timestamp and status (#240). What separates them is when they were
// queued: a push's jobs are queued in one loop and share a created_at to
// the second, a schedule's are hours or days apart. A queue loop that
// straddles a second boundary shows as two rows for one push.
func groupRuns(builds []control.BuildOut) []buildRun {
	var runs []buildRun
	for _, b := range builds {
		if n := len(runs); n > 0 && runs[n-1].SHA == b.SHA && runs[n-1].CreatedAt == b.CreatedAt {
			runs[n-1].Builds = append(runs[n-1].Builds, b)
			continue
		}
		runs = append(runs, buildRun{SHA: b.SHA, Subject: b.Subject, Ref: b.Ref, CreatedAt: b.CreatedAt, Builds: []control.BuildOut{b}})
	}
	for i := range runs {
		runs[i].Status = combinedStatus(runs[i].Builds)
	}
	return runs
}

// buildsPerPage is how many builds one page of the builds tab asks for.
// Fewer than the command's own default, because the page folds them into
// runs and a run is several builds tall (#244).
const buildsPerPage = 30

// olderBuilds is the link to the page after this one: the command's own
// keyset cursor with the three filters carried along, so paging never
// drops a filter and a filter never lands on page two.
func olderBuilds(f buildFilter, next string) string {
	if next == "" {
		return ""
	}
	q := url.Values{"cursor": {next}}
	for k, v := range map[string]string{"ref": f.Ref, "status": f.Status, "job": f.Job} {
		if v != "" {
			q.Set(k, v)
		}
	}
	return "?" + q.Encode()
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
	argv := []string{"build", "list", p.Repo.Path(), "--limit", strconv.Itoa(buildsPerPage)}
	if filter.Ref != "" {
		argv = append(argv, "--ref", filter.Ref)
	}
	if filter.Status != "" {
		argv = append(argv, "--status", filter.Status)
	}
	if filter.Job != "" {
		argv = append(argv, "--job", filter.Job)
	}
	if cursor := qv.Get("cursor"); cursor != "" {
		argv = append(argv, "--cursor", cursor)
	}

	var page struct {
		Items []control.BuildOut `json:"items"`
		Next  string             `json:"next"`
	}
	s.runControlInto(viewer, argv, &page)
	builds := page.Items

	// The jobs a trigger can name. A repo without a CI config has none;
	// that is not an error for this page.
	var jobs []control.JobOut
	s.runControlInto(viewer, []string{"build", "jobs", p.Repo.Path()}, &jobs)

	refs := distinctRefs(builds, filter.Ref)

	s.render(w, "builds.html", struct {
		repoPage
		Builds   []control.BuildOut
		Jobs     []control.JobOut
		Runs     []buildRun
		Filter   buildFilter
		Facets   []facetGroup
		Refs     []string
		Older    string
		CanWrite bool
		Notice   string
	}{p, builds, jobs, groupRuns(builds), filter, buildFacets(filter, jobs, refs), refs,
		olderBuilds(filter, page.Next), s.canWriteRepo(r, p.Repo), s.takeFlash(w, r)})
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
	v := buildView{repoPage: p, Build: b, CanWrite: s.canWriteRepo(r, p.Repo), Notice: s.takeFlash(w, r)}
	if (b.Status == "pending" || b.Status == "running") && r.URL.Query().Get("follow") != "0" && r.Method == http.MethodGet {
		s.streamBuild(w, r, v, viewer, n)
		return
	}
	v.Log, _, _ = s.runControl(viewer, []string{"build", "log", p.Repo.Path(), n})
	s.render(w, "build.html", v)
}

type buildView struct {
	repoPage
	Build    control.BuildOut
	Log      string
	Live     bool
	CanWrite bool
	Notice   string
}

// liveLogMarker stands in for the log when build.html is rendered for a
// live build; streamBuild splits the page there and streams the log into
// the gap. Git refs, paths and job names cannot hold the control byte.
const liveLogMarker = "\x1elive-log\x1e"

// streamBuild writes the build page with the log following the build:
// the page up to the log, then build log --follow escaped and flushed as
// it arrives, then the outcome and the rest of the page.
func (s *Server) streamBuild(w http.ResponseWriter, r *http.Request, v buildView, viewer store.User, n string) {
	v.Live, v.Log = true, liveLogMarker
	var buf bytes.Buffer
	if err := web.Render(&buf, "build.html", v); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	head, tail, ok := strings.Cut(buf.String(), liveLogMarker)
	if !ok || !strings.HasPrefix(tail, "</pre>") {
		http.Error(w, "template error: build.html has no live log slot", http.StatusInternalServerError)
		return
	}
	tail = strings.TrimPrefix(tail, "</pre>")

	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	rc := http.NewResponseController(w)
	io.WriteString(w, head)
	rc.Flush()

	path := v.Repo.Path()
	msg, code := s.runControlStream(viewer, []string{"build", "log", path, n, "--follow"},
		htmlStream{w: w, rc: rc}, r.Context().Done())
	if r.Context().Err() != nil {
		// The client left; nothing more to write.
		return
	}
	if code == protocol.ExitDenied {
		// The follow cap: the stored log once, and why it is not live.
		log, _, _ := s.runControl(viewer, []string{"build", "log", path, n})
		template.HTMLEscape(w, []byte(log))
	}
	io.WriteString(w, "</pre>")
	switch {
	case code == protocol.ExitOK:
		var b control.BuildOut
		if _, ok := s.runControlInto(viewer, []string{"build", "show", path, n}, &b); ok {
			fmt.Fprintf(w, `<p class="notice" role="status">build finished: %s</p>`, template.HTMLEscapeString(b.Status))
		}
	case code == protocol.ExitDenied:
		if viewer.ID == 0 {
			msg = "Too many signed-out viewers are watching live builds. This is the log so far; reload to try again, or sign in."
		}
		fmt.Fprintf(w, `<p class="error" role="alert">%s</p>`, template.HTMLEscapeString(msg))
	case code == protocol.ExitFailure && msg != "":
		fmt.Fprintf(w, `<p class="notice" role="status">%s</p>`, template.HTMLEscapeString(msg))
	}
	io.WriteString(w, tail)
}

// htmlStream escapes each chunk of a streamed log into the page and
// flushes it, so the browser draws it as it arrives.
type htmlStream struct {
	w  io.Writer
	rc *http.ResponseController
}

func (h htmlStream) Write(p []byte) (int, error) {
	var buf bytes.Buffer
	template.HTMLEscape(&buf, p)
	if _, err := h.w.Write(buf.Bytes()); err != nil {
		return 0, err
	}
	if err := h.rc.Flush(); err != nil {
		return 0, err
	}
	return len(p), nil
}
