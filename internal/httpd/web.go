package httpd

import (
	"bytes"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"os"
	"path/filepath"

	"gitbay.org/gitbay/internal/policy"
	"html/template"
	"net/http"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/microcosm-cc/bluemonday"
	"github.com/niklasfasching/go-org/org"
	"github.com/yuin/goldmark"
	highlighting "github.com/yuin/goldmark-highlighting/v2"
	"github.com/yuin/goldmark/extension"

	"gitbay.org/gitbay/internal/autolink"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/sig"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
)

const maxRenderBytes = 1 << 20 // largest blob rendered inline

func (s *Server) render(w http.ResponseWriter, page string, data any) {
	var buf bytes.Buffer
	if err := web.Render(&buf, page, data); err != nil {
		http.Error(w, "template error: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	buf.WriteTo(w)
}

// siteName is the instance's display name: the operator's [web] title,
// or the site host when they have not set one.
func (s *Server) siteName() string {
	if t := strings.TrimSpace(s.cfg.Web.Title); t != "" {
		return t
	}
	h := strings.TrimPrefix(strings.TrimPrefix(s.cfg.Server.SiteURL, "https://"), "http://")
	return strings.TrimSuffix(h, "/")
}

func (s *Server) stylesheet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write(web.StyleCSS)
	w.Write(chromaCSS)
}

func (s *Server) favicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write(web.FaviconSVG)
}

// font serves the embedded Atkinson Hyperlegible subsets. Same-origin,
// so the CSP's default-src 'self' covers it — no font CDN.
func (s *Server) font(w http.ResponseWriter, r *http.Request) {
	data, err := web.FontFS.ReadFile("static" + r.URL.Path[len("/static"):])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "font/woff2")
	w.Header().Set("Cache-Control", "public, max-age=604800, immutable")
	w.Write(data)
}

// notFound renders the designed 404 page with a 404 status. Falls back to
// the stock plain-text response if the template fails.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	if err := web.Render(&buf, "404.html", s.base(r)); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	buf.WriteTo(w)
}

// describedRepo pairs a repo with the listing metadata: description,
// topics, license, and last-updated date.
type describedRepo struct {
	store.Repo
	Desc    string
	Topics  []string
	License string
	Updated string
}

func (s *Server) describeAll(repos []store.Repo) []describedRepo {
	var out []describedRepo
	for _, r := range repos {
		dir := control.RepoDir(s.cfg.Server.Root, r.OwnerName, r.Name)
		d := describedRepo{
			Repo:    r,
			Desc:    gitutil.ReadDescription(dir),
			License: detectLicense(dir, r.DefaultBranch),
			Updated: gitutil.LastCommitDate(dir, r.DefaultBranch),
		}
		d.Topics, _ = s.st.ListTopics(r.ID)
		out = append(out, d)
	}
	return out
}

// index is the homepage: a dashboard for logged-in users, a landing page
// for everyone else. The full public listing lives at /explore.
func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if s.cfg.Web.Mode == "accounts" {
		if viewer := s.viewer(r); viewer.ID != 0 {
			s.dashboard(w, r, viewer)
			return
		}
	}
	host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(
		s.cfg.Server.SiteURL, "https://"), "http://"), "/")
	s.render(w, "landing.html", struct {
		basePage
		Host     string
		Accounts bool
		Signup   bool
	}{basePage{Site: s.siteName(), Host: s.cfg.SiteHost()}, host, s.cfg.Web.Mode == "accounts",
		s.cfg.Web.Mode == "accounts" && s.cfg.Registration.Mode != "closed"})
}

func (s *Server) dashboard(w http.ResponseWriter, r *http.Request, viewer store.User) {
	pinned, _ := s.st.PinnedRepos(viewer.ID)
	var visible []store.Repo
	for _, rp := range pinned {
		grant, _ := s.st.AccessRole(rp.ID, viewer.ID)
		if policy.CanRead(viewer, rp, grant) {
			visible = append(visible, rp)
		}
	}
	mrs, _ := s.st.DashboardMRs(viewer.ID)
	issues, _ := s.st.DashboardIssues(viewer.ID)
	reviews, _ := s.st.ReviewQueue(viewer.ID)
	assigned, _ := s.st.AssignedIssues(viewer.ID)
	events, _ := s.st.RecentEvents(viewer.ID, 20, 0)
	s.render(w, "dashboard.html", struct {
		basePage
		Pinned   []store.Repo
		Reviews  []store.DashboardItem
		Assigned []store.DashboardItem
		MRs      []store.DashboardItem
		Issues   []store.DashboardItem
		Feed     []feedLine
	}{s.baseFor(viewer), visible, reviews, assigned, mrs, issues, feedLines(events)})
}

func (s *Server) explore(w http.ResponseWriter, r *http.Request) {
	repos, err := s.st.ListPublicRepos()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var viewer store.User
	if s.cfg.Web.Mode == "accounts" {
		viewer = s.viewer(r)
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	s.render(w, "explore.html", struct {
		basePage
		Query string
		Repos []describedRepo
	}{s.baseFor(viewer), q, s.filterRepos(q, s.describeAll(repos))})
}

// privacy renders the privacy page: what the gitbay software does with
// data, plus this instance's operator-provided notes.
func (s *Server) privacy(w http.ResponseWriter, r *http.Request) {
	s.render(w, "privacy.html", struct {
		basePage
		Host   string
		Notice string
	}{s.base(r), s.cfg.SiteHost(), s.cfg.Web.PrivacyNotice})
}

// filterRepos keeps repos whose path, description, or topics contain the
// query, case-insensitively. An empty query keeps everything.
func (s *Server) filterRepos(q string, repos []describedRepo) []describedRepo {
	if q == "" {
		return repos
	}
	q = strings.ToLower(q)
	var out []describedRepo
	for _, d := range repos {
		if strings.Contains(strings.ToLower(d.Path()), q) ||
			strings.Contains(strings.ToLower(d.Desc), q) {
			out = append(out, d)
			continue
		}
		for _, t := range d.Topics {
			if strings.Contains(t, q) {
				out = append(out, d)
				break
			}
		}
	}
	return out
}

// repoPage is the shared context for repo-scoped pages.
type repoPage struct {
	basePage
	Desc     string
	Repo     store.Repo
	Ref      string
	CloneURL string
	Dir      string
	Tab      string // active tab in the repo header
	Topics   []string
	Pinned   bool // by the viewer
	HasWiki  bool
	Host     string
	Mirrors  []mirrorLine // repo admins only
	CanAdmin bool         // gates the settings tab
	// OpenIssues and OpenMRs are the counts on the header tabs.
	OpenIssues int
	OpenMRs    int
	// RepoHome asks the layout for the full header — description, topics,
	// website, mirrors. Every other page gets identity and tabs only, so a
	// repo describes itself once rather than on all twelve of its pages.
	RepoHome bool
}

// mirrorLine is the admin-only mirror status shown in the repo header.
// It carries no credentials: the stored URL is credential-free.
type mirrorLine struct {
	Direction string
	URL       string
	Target    string // URL without the scheme, for display
	Synced    string
	Error     string
}

// syncedAt trims a stored sync timestamp (2026-08-25T03:39:19.994Z) to a
// readable "2026-08-25 03:39 UTC".
func syncedAt(ts string) string {
	if len(ts) < 16 {
		return ts
	}
	return ts[:10] + " " + ts[11:16] + " UTC"
}

// repoFor resolves the repo for a web request; false means 404 was sent.
// Anonymous visitors see public repos only; in accounts mode a logged-in
// viewer additionally sees repos their grants allow. Private and missing
// repos are indistinguishable either way.
func (s *Server) repoFor(w http.ResponseWriter, r *http.Request, ref string) (repoPage, bool) {
	var repo store.Repo
	var viewer store.User
	if s.cfg.Web.Mode == "accounts" {
		viewer = s.viewer(r)
	}
	repo, err := s.st.RepoByPath(r.PathValue("owner") + "/" + r.PathValue("repo"))
	ok := err == nil
	grant := ""
	if ok {
		if viewer.ID != 0 {
			grant, _ = s.st.AccessRole(repo.ID, viewer.ID)
		}
		ok = policyCanRead(viewer, repo, grant)
	}
	if !ok {
		s.notFound(w, r)
		return repoPage{}, false
	}
	if ref == "" {
		ref = repo.DefaultBranch
	}
	topics, _ := s.st.ListTopics(repo.ID)
	pinned := false
	if viewer.ID != 0 {
		pinned = s.st.IsPinned(viewer.ID, repo.ID)
	}
	canAdmin := viewer.ID != 0 && policy.CanAdmin(viewer, repo, grant)
	var mirrors []mirrorLine
	if canAdmin {
		ms, _ := s.st.ListMirrors(repo.ID)
		for _, m := range ms {
			mirrors = append(mirrors, mirrorLine{
				Direction: m.Direction,
				URL:       m.URL,
				Target:    strings.TrimPrefix(strings.TrimPrefix(m.URL, "https://"), "http://"),
				Synced:    syncedAt(m.LastSync),
				Error:     m.LastError,
			})
		}
	}
	openIssues, openMRs := s.st.OpenCounts(repo.ID)
	return repoPage{
		basePage:   s.baseFor(viewer),
		CanAdmin:   canAdmin,
		Mirrors:    mirrors,
		Pinned:     pinned,
		HasWiki:    s.wikiDir(repo.OwnerName, repo.Name) != "",
		Host:       s.cfg.SiteHost(),
		Desc:       gitutil.ReadDescription(control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name)),
		Repo:       repo,
		Ref:        ref,
		CloneURL:   s.cfg.Server.SiteURL + "/" + repo.Path() + ".git",
		Dir:        control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name),
		Topics:     topics,
		OpenIssues: openIssues,
		OpenMRs:    openMRs,
	}, true
}

type crumb struct {
	Name string
	URL  string
}

func crumbs(p repoPage, kind, filePath string) []crumb {
	var cs []crumb
	base := "/" + p.Repo.Path() + "/" + kind + "/" + p.Ref + "/"
	acc := ""
	for _, part := range strings.Split(filePath, "/") {
		if part == "" {
			continue
		}
		acc = path.Join(acc, part)
		cs = append(cs, crumb{Name: part, URL: base + acc})
	}
	return cs
}

// ownerPage renders /{owner} for users and orgs: the repositories the
// viewer may see, org membership either direction. Owner names are not
// secret (they are on every commit); repository visibility rules hold.
func (s *Server) ownerPage(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("owner")
	var viewer store.User
	if s.cfg.Web.Mode == "accounts" {
		viewer = s.viewer(r)
	}

	kind := "user"
	var ownerID int64
	var members []store.OrgMember
	var orgs []store.OrgMember
	if u, err := s.st.UserByUsername(name); err == nil {
		ownerID = u.ID
		orgs, _ = s.st.ListOrgsForUser(u.ID)
	} else if o, err := s.st.OrgByName(name); err == nil {
		kind, ownerID = "org", o.ID
		members, _ = s.st.OrgMembers(o.ID)
	} else {
		s.notFound(w, r)
		return
	}
	profile, _ := s.st.OwnerProfile(kind, ownerID)

	all, err := s.st.ListReposForOwner(kind, ownerID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var visible []store.Repo
	for _, repo := range all {
		grant := ""
		if viewer.ID != 0 {
			grant, _ = s.st.AccessRole(repo.ID, viewer.ID)
		}
		if policy.CanRead(viewer, repo, grant) {
			visible = append(visible, repo)
		}
	}
	var counts map[string]int
	if kind == "user" {
		counts, _ = s.st.ActivityByDay(ownerID, activitySince())
	} else {
		counts, _ = s.st.OrgActivityByDay(ownerID, activitySince())
	}
	weeks, activityTotal := activityGrid(counts)

	teams, canAdmin := s.orgAdminView(viewer, kind, name)
	s.render(w, "owner.html", struct {
		basePage
		Owner         string
		Kind          string
		Profile       store.Profile
		AboutHTML     template.HTML
		Repos         []describedRepo
		Members       []store.OrgMember
		Orgs          []store.OrgMember
		Activity      []activityWeek
		ActivityTotal int
		Teams         []teamView
		CanAdmin      bool
		Notice        string
	}{s.baseFor(viewer), name, kind, profile, aboutHTML(profile),
		s.describeAll(visible), members, orgs,
		weeks, activityTotal, teams, canAdmin, r.URL.Query().Get("e")})
}

func (s *Server) repoHome(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "files"
	p.RepoHome = true
	s.renderTree(w, r, p, "")
}

func (s *Server) tree(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, r.PathValue("ref"))
	if !ok {
		return
	}
	p.Tab = "files"
	s.renderTree(w, r, p, strings.Trim(r.PathValue("path"), "/"))
}

// treePage is shared by the populated and empty-repository renders: two
// anonymous structs drifted apart once already.
type treePage struct {
	repoPage
	Crumbs      []crumb
	Prefix      string
	DirPath     string
	RefKind     string
	Entries     []gitutil.TreeEntry
	Branches    []gitutil.Ref
	ReadmeName  string
	ReadmeHTML  template.HTML
	LastCommits map[string]namedCommit
	Tip         namedCommit
	Facts       repoFacts
}

func (s *Server) renderTree(w http.ResponseWriter, r *http.Request, p repoPage, dirPath string) {
	if _, err := gitutil.ResolveRef(p.Dir, p.Ref); err != nil {
		// Empty repo: render the page with no entries rather than 404.
		s.render(w, "tree.html", treePage{repoPage: p, RefKind: "tree"})
		return
	}
	entries, err := gitutil.ListTree(p.Dir, p.Ref, dirPath)
	if err != nil {
		s.notFound(w, r)
		return
	}
	// Directories first. git's tree order interleaves them with files, but
	// a listing is scanned by shape before name. Stable, so each group
	// keeps the ordering git gave it.
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Type == "tree" && entries[j].Type != "tree"
	})
	prefix := ""
	if dirPath != "" {
		prefix = dirPath + "/"
	}

	var readmeHTML template.HTML
	readmeName := pickReadme(entries)
	if readmeName != "" {
		if raw, err := gitutil.ReadBlob(p.Dir, p.Ref, prefix+readmeName, maxRenderBytes); err == nil {
			readmeHTML = rewriteRelativeLinks(renderReadme(readmeName, raw), p, dirPath)
		}
	}

	branches, _ := gitutil.Refs(p.Dir, "heads")
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	// The facts bar is about the repository, not this directory, so it is
	// computed once at the root and left off subdirectory listings.
	var facts repoFacts
	if dirPath == "" {
		facts = s.factsFor(p)
	}
	s.render(w, "tree.html", treePage{p, crumbs(p, "tree", dirPath), prefix, dirPath, "tree", entries, branches,
		readmeName, readmeHTML,
		s.namedCommits(gitutil.LastCommits(p.Dir, p.Ref, dirPath, names)),
		s.namedTip(gitutil.TipCommit(p.Dir, p.Ref)), facts})
}

func (s *Server) blob(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, r.PathValue("ref"))
	if !ok {
		return
	}
	p.Tab = "files"
	filePath := strings.Trim(r.PathValue("path"), "/")
	data, err := gitutil.ReadBlob(p.Dir, p.Ref, filePath, maxRenderBytes+1)
	if err != nil {
		s.notFound(w, r)
		return
	}
	binary := gitutil.IsBinary(data) || len(data) > maxRenderBytes
	_, image := imageTypes[strings.ToLower(path.Ext(filePath))]

	var codeHTML template.HTML
	if !binary && !image {
		codeHTML = highlight(filePath, data)
	}
	cs := crumbs(p, "blob", filePath)
	base := ""
	if len(cs) > 0 {
		base = cs[len(cs)-1].Name
		cs = cs[:len(cs)-1]
	}
	branches, _ := gitutil.Refs(p.Dir, "heads")
	lines := 0
	if !binary && !image && len(data) > 0 {
		lines = bytes.Count(data, []byte("\n"))
		if data[len(data)-1] != '\n' {
			lines++
		}
	}
	// The file listing leads with the last commit now, so the facts about
	// the file itself are reported here instead.
	entry, _ := gitutil.StatPath(p.Dir, p.Ref, filePath)
	s.render(w, "blob.html", struct {
		repoPage
		Crumbs   []crumb
		Base     string
		Path     string
		DirPath  string
		RefKind  string
		Binary   bool
		Image    bool
		Size     int
		Lines    int
		Exec     bool
		Symlink  bool
		Branches []gitutil.Ref
		CodeHTML template.HTML
	}{p, cs, base, filePath, filePath, "blob", binary, image, len(data), lines,
		entry.Mode == "100755", entry.Mode == "120000", branches, codeHTML})
}

// releases lists tag-anchored releases with notes and assets.
func (s *Server) releases(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "releases"
	rels, err := s.st.ListReleases(p.Repo.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	md := s.ugcFor(r, p.Repo)
	type relView struct {
		store.Release
		NotesHTML template.HTML
	}
	var views []relView
	for _, rel := range rels {
		views = append(views, relView{rel, md(rel.Notes)})
	}
	// Tags without a release yet are what a create form can offer.
	released := map[string]bool{}
	for _, rel := range rels {
		released[rel.Tag] = true
	}
	var freeTags []string
	if tags, err := gitutil.Refs(p.Dir, "tags"); err == nil {
		for _, tg := range tags {
			if !released[tg.Name] {
				freeTags = append(freeTags, tg.Name)
			}
		}
	}
	s.render(w, "releases.html", struct {
		repoPage
		Releases []relView
		FreeTags []string
		CanWrite bool
		Notice   string
	}{p, views, freeTags, s.canWriteRepo(r, p.Repo), r.URL.Query().Get("e")})
}

// releaseAsset streams one uploaded asset. Tags containing '/' are not
// reachable here (single path segment); SSH download always works.
func (s *Server) releaseAsset(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	rel, err := s.st.ReleaseByTag(p.Repo.ID, r.PathValue("tag"))
	if err != nil {
		s.notFound(w, r)
		return
	}
	name := r.PathValue("name")
	found := false
	for _, a := range rel.Assets {
		if a.Name == name {
			found = true
		}
	}
	if !found {
		s.notFound(w, r)
		return
	}
	f, err := os.Open(filepath.Join(control.RepoDir(s.cfg.Server.Root, p.Repo.OwnerName, p.Repo.Name),
		"gitbay-releases", strconv.FormatInt(rel.ID, 10), name))
	if err != nil {
		s.notFound(w, r)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	if fi, err := f.Stat(); err == nil {
		w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	}
	io.Copy(w, f)
}

// milestones lists a repo's milestones with progress.
func (s *Server) milestones(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "issues"
	state := r.URL.Query().Get("state")
	if state != "closed" && state != "all" {
		state = "open"
	}
	ms, err := s.st.ListMilestones(p.Repo.ID, state)
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
	s.render(w, "milestones.html", struct {
		repoPage
		State      string
		Milestones []msView
	}{p, state, views})
}

// search runs a bounded literal git grep over the repo's default branch.
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "search"
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	type matchView struct {
		Path     string
		Line     int
		TextHTML template.HTML
	}
	var matches []matchView
	var queryErr string
	if q != "" {
		if len(q) < 2 || len(q) > 200 {
			queryErr = "query must be 2 to 200 characters"
		} else if _, err := gitutil.ResolveRef(p.Dir, p.Ref); err == nil {
			raw, err := gitutil.Grep(p.Dir, p.Ref, q, 200)
			if err != nil {
				http.Error(w, "internal error", http.StatusInternalServerError)
				return
			}
			for _, m := range raw {
				matches = append(matches, matchView{m.Path, m.Line, markMatch(m.Text, q)})
			}
		}
	}
	s.render(w, "search.html", struct {
		repoPage
		Query    string
		QueryErr string
		Matches  []matchView
		Capped   bool
	}{p, q, queryErr, matches, len(matches) == 200})
}

// markMatch escapes a matched line and wraps case-insensitive occurrences
// of the query in <mark>.
func markMatch(text, q string) template.HTML {
	lower, lq := strings.ToLower(text), strings.ToLower(q)
	var b strings.Builder
	pos := 0
	for {
		i := strings.Index(lower[pos:], lq)
		if i < 0 {
			break
		}
		i += pos
		b.WriteString(template.HTMLEscapeString(text[pos:i]))
		b.WriteString("<mark>")
		b.WriteString(template.HTMLEscapeString(text[i : i+len(q)]))
		b.WriteString("</mark>")
		pos = i + len(q)
	}
	b.WriteString(template.HTMLEscapeString(text[pos:]))
	return template.HTML(b.String())
}

func (s *Server) blame(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, r.PathValue("ref"))
	if !ok {
		return
	}
	p.Tab = "files"
	filePath := strings.Trim(r.PathValue("path"), "/")

	// Blame is a control command; the web renders what it returns rather
	// than shelling out to git itself, so all three surfaces agree.
	page := 1
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n >= 1 {
		page = n
	}
	from := (page-1)*control.BlameSpan + 1

	var out struct {
		From       int `json:"from"`
		To         int `json:"to"`
		TotalLines int `json:"total_lines"`
		Hunks      []struct {
			SHA         string   `json:"sha"`
			AuthorName  string   `json:"author_name"`
			AuthorEmail string   `json:"author_email"`
			Date        string   `json:"date"`
			Summary     string   `json:"summary"`
			StartLine   int      `json:"start_line"`
			Lines       []string `json:"lines"`
		} `json:"hunks"`
	}
	argv := []string{"repo", "blame", p.Repo.Path(), filePath,
		"--ref", p.Ref, "--from", strconv.Itoa(from), "--to", strconv.Itoa(from + control.BlameSpan - 1)}
	var viewer store.User
	if s.cfg.Web.Mode == "accounts" {
		viewer = s.viewer(r)
	}
	msg, ok := s.runControlInto(viewer, argv, &out)

	// A binary or empty file is a refusal, not a 404: the page still
	// renders and says why there is nothing to attribute.
	binary := false
	if !ok {
		if strings.Contains(msg, "is binary") {
			binary = true
		} else {
			s.notFound(w, r)
			return
		}
	}

	type hunkView struct {
		gitutil.BlameHunk
		ShortSHA string
		Date     string
		Sig      sigView
		Numbered []numberedLine
	}
	var hunks []hunkView
	sigs := map[string]sigView{}
	for _, h := range out.Hunks {
		v, seen := sigs[h.SHA]
		if !seen {
			v, _ = s.sigFor(p.Repo, p.Dir, h.SHA)
			sigs[h.SHA] = v
		}
		date := h.Date
		if t, err := time.Parse(time.RFC3339, h.Date); err == nil {
			date = t.Format("2006-01-02")
		}
		hv := hunkView{
			BlameHunk: gitutil.BlameHunk{SHA: h.SHA, AuthorName: h.AuthorName,
				AuthorEmail: h.AuthorEmail, Summary: h.Summary,
				StartLine: h.StartLine, Lines: h.Lines},
			ShortSHA: h.SHA[:min(10, len(h.SHA))], Date: date, Sig: v,
		}
		for i, l := range h.Lines {
			hv.Numbered = append(hv.Numbered, numberedLine{h.StartLine + i, l})
		}
		hunks = append(hunks, hv)
	}

	pages := (out.TotalLines + control.BlameSpan - 1) / control.BlameSpan
	if pages == 0 {
		pages = 1
	}
	if page > pages {
		page = pages
	}

	cs := crumbs(p, "blame", filePath)
	base := ""
	if len(cs) > 0 {
		base = cs[len(cs)-1].Name
		cs = cs[:len(cs)-1]
	}
	s.render(w, "blame.html", struct {
		repoPage
		Crumbs      []crumb
		Base        string
		Path        string
		Binary      bool
		Hunks       []hunkView
		Page, Pages int
	}{p, cs, base, filePath, binary, hunks, page, pages})
}

type numberedLine struct {
	N    int
	Text string
}

// chromaFormatter emits class-based markup (no inline colors), so the
// stylesheet can swap palettes with the color scheme.
var chromaFormatter = html.New(html.WithClasses(true),
	html.WithLineNumbers(true), html.LineNumbersInTable(false),
	html.WithLinkableLineNumbers(true, "L"))

func highlight(filePath string, data []byte) template.HTML {
	lexer := lexers.Match(filePath)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	iterator, err := lexer.Tokenise(nil, string(data))
	if err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(string(data)) + "</pre>")
	}
	var buf bytes.Buffer
	if err := chromaFormatter.Format(&buf, styles.Get(lightStyle), iterator); err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(string(data)) + "</pre>")
	}
	return template.HTML(buf.String())
}

// chromaCSS is both syntax palettes, each scoped to the scheme it is for.
// The light one cannot be left unscoped: the two palettes do not name the
// same token set, and every token github-dark omits would keep its
// light-theme colour on a black ground — NameAttribute landed at 2.97:1.
// Scoped, an unnamed token inherits the wrapper's colour instead, which is
// readable in both. The site's --code-bg stays the background either way.
// lightStyle and darkStyle are chosen on measured contrast against the
// grounds code actually sits on here — page, code block, and the diff
// tints. friendly, the chroma default, put 61 token/ground pairs under
// 4.5:1; xcode puts one.
const (
	lightStyle = "xcode"
	darkStyle  = "github-dark"
)

var chromaCSS = func() []byte {
	var buf bytes.Buffer
	buf.WriteString("@media (prefers-color-scheme: light) {\n")
	chromaFormatter.WriteCSS(&buf, styles.Get(lightStyle))
	// xcode's NameAttribute is its one token under 4.5:1 against the diff
	// tints (4.51 on additions, 4.38 on deletions); darkened it clears both.
	buf.WriteString(".chroma .na { color: #6f5a21 }\n")
	buf.WriteString("}\n@media (prefers-color-scheme: dark) {\n")
	chromaFormatter.WriteCSS(&buf, styles.Get(darkStyle))
	buf.WriteString("}\n.chroma, .bg { background: transparent !important; }\n")
	// Line numbers take the site's own gutter colour in both schemes. Left
	// alone they are github-dark's #6e7681 (4.31:1 on the page) in dark and
	// chroma's built-in #7f7f7f (3.67:1 on a code block) in light — the
	// latter is a formatter fallback, not a style entry, so no palette test
	// can see it.
	buf.WriteString(".chroma .lnt, .chroma .ln { color: var(--muted) }\n")
	return buf.Bytes()
}()

func (s *Server) raw(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, r.PathValue("ref"))
	if !ok {
		return
	}
	filePath := strings.Trim(r.PathValue("path"), "/")
	data, err := gitutil.ReadBlob(p.Dir, p.Ref, filePath, s.cfg.Limits.MaxBlobBytes)
	if err != nil {
		s.notFound(w, r)
		return
	}
	// Serve inert: never let repo content execute in the forge's origin.
	// Images get their real type so <img> works under nosniff; SVG script
	// is dead on arrival because the instance CSP is script-src 'none'.
	ct := "text/plain; charset=utf-8"
	if t, ok := imageTypes[strings.ToLower(path.Ext(filePath))]; ok {
		ct = t
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(data)
}

// imageTypes are the formats raw serves with a real content type and blob
// pages preview inline.
var imageTypes = map[string]string{
	".png": "image/png", ".jpg": "image/jpeg", ".jpeg": "image/jpeg",
	".gif": "image/gif", ".webp": "image/webp", ".avif": "image/avif",
	".svg": "image/svg+xml", ".ico": "image/x-icon",
}

// readmeRank orders competing README files: richer renderers win.
var readmeRank = map[string]int{".md": 1, ".markdown": 1, ".org": 2, ".html": 3, ".htm": 3}

// pickReadme returns the best README-ish blob in a tree listing: any file
// named "readme" or "readme.<ext>" (case-insensitive), preferring formats
// we can render richly.
func pickReadme(entries []gitutil.TreeEntry) string {
	best, bestRank := "", 1<<30
	for _, e := range entries {
		if e.Type != "blob" {
			continue
		}
		lower := strings.ToLower(e.Name)
		if lower != "readme" && !strings.HasPrefix(lower, "readme.") {
			continue
		}
		rank, ok := readmeRank[path.Ext(lower)]
		if !ok {
			rank = 10 // plaintext fallback
		}
		if rank < bestRank {
			best, bestRank = e.Name, rank
		}
	}
	return best
}

// markdown is the shared renderer: GFM (tables, strikethrough, autolinks,
// task lists) on top of CommonMark, with class-based fence highlighting
// (the palette lives in the stylesheet, per scheme). Raw HTML is still
// dropped.
var markdown = goldmark.New(goldmark.WithExtensions(extension.GFM,
	highlighting.NewHighlighting(highlighting.WithFormatOptions(html.WithClasses(true)))))

// fenceHighlight renders one code block with chroma classes, for org and
// anything else outside goldmark. Unknown languages fall back to plain.
func fenceHighlight(source, lang string) string {
	lexer := lexers.Get(lang)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	iterator, err := lexer.Tokenise(nil, source)
	if err != nil {
		return "<pre>" + template.HTMLEscapeString(source) + "</pre>"
	}
	var buf bytes.Buffer
	f := html.New(html.WithClasses(true))
	if err := f.Format(&buf, styles.Get(lightStyle), iterator); err != nil {
		return "<pre>" + template.HTMLEscapeString(source) + "</pre>"
	}
	return buf.String()
}

// mdHTML renders user-authored markdown (issue and MR bodies, comments).
// goldmark's default renderer drops raw HTML, so this is safe as-is.
func mdHTML(raw string) template.HTML {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var buf bytes.Buffer
	if markdown.Convert([]byte(raw), &buf) != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(raw) + "</pre>")
	}
	return template.HTML(buf.String())
}

// aboutHTML renders a profile's about text. It has no filename to
// dispatch on, so the stored format picks the extension; anything other
// than org is markdown.
func aboutHTML(p store.Profile) template.HTML {
	if strings.TrimSpace(p.About) == "" {
		return ""
	}
	name := "about.md"
	if p.AboutFormat == "org" {
		name = "about.org"
	}
	return renderReadme(name, []byte(p.About))
}

// webResolver answers autolink lookups for one viewer. Cross-repo
// references to repositories the viewer cannot read stay plain text, per
// the enumeration rule: a link would confirm the repo exists.
type webResolver struct {
	s      *Server
	viewer store.User
}

func (r webResolver) RefURL(owner, name string, kind byte, n int64) string {
	repo, err := r.s.st.RepoByPath(owner + "/" + name)
	if err != nil {
		return ""
	}
	grant := ""
	if r.viewer.ID != 0 {
		grant, _ = r.s.st.AccessRole(repo.ID, r.viewer.ID)
	}
	if !policy.CanRead(r.viewer, repo, grant) {
		return ""
	}
	if kind == '#' {
		if _, err := r.s.st.IssueByNumber(repo.ID, n); err != nil {
			return ""
		}
		return autolink.IssueURL(repo.OwnerName, repo.Name, n)
	}
	if _, err := r.s.st.MRByNumber(repo.ID, n); err != nil {
		return ""
	}
	return autolink.MRURL(repo.OwnerName, repo.Name, n)
}

func (r webResolver) UserURL(name string) string {
	if _, err := r.s.st.UserByUsername(name); err == nil {
		return "/" + name
	}
	if _, err := r.s.st.OrgByName(name); err == nil {
		return "/" + name
	}
	return ""
}

// ugcFor returns a renderer for user-authored markdown on one repo's pages:
// mdHTML plus cross-reference and mention autolinking for this viewer.
func (s *Server) ugcFor(r *http.Request, repo store.Repo) func(string) template.HTML {
	viewer := store.User{}
	if s.cfg.Web.Mode == "accounts" {
		viewer = s.viewer(r)
	}
	res := webResolver{s, viewer}
	return func(raw string) template.HTML {
		h := mdHTML(raw)
		if h == "" {
			return h
		}
		return template.HTML(autolink.Rewrite(string(h), repo.OwnerName, repo.Name, res))
	}
}

// renderedComment pairs a comment with its rendered body for templates.
type renderedComment struct {
	Author    string
	CreatedAt string
	Kind      string
	BodyHTML  template.HTML
}

func renderComments(cs []store.IssueComment, md func(string) template.HTML) []renderedComment {
	var out []renderedComment
	for _, c := range cs {
		out = append(out, renderedComment{c.Author, c.CreatedAt, c.Kind, md(c.Body)})
	}
	return out
}

// ugcPolicy sanitizes rendered repo content before it enters the forge's
// origin: markdown is already safe (goldmark drops raw HTML), but org-mode
// output and repo-authored HTML are not. Chroma's highlighting classes
// must survive; the pattern admits only short token codes, not the site's
// own class names.
var ugcPolicy = func() *bluemonday.Policy {
	p := bluemonday.UGCPolicy()
	p.AllowAttrs("class").
		Matching(regexp.MustCompile(`^(chroma|[a-z0-9]{1,3})( (chroma|[a-z0-9]{1,3}))*$`)).
		OnElements("span", "pre", "code", "div")
	return p
}()

// renderReadme renders a README by extension: markdown, org-mode, and
// (sanitized) HTML richly; everything else as escaped plaintext.
// orgConfig is the go-org configuration for rendering untrusted org.
//
// go-org's default reads #+INCLUDE: and #+SETUPFILE: targets off disk with
// os.ReadFile. Everything rendered here is content someone pushed — a README, a
// wiki page, a profile — so both keywords are refused outright: the file is
// never opened and the keyword stays the inert text it is. There is no safe
// subset to allow instead. An absolute path skips go-org's relative-path join,
// a relative one resolves against the daemon's working directory, and a repo
// has no directory to scope to anyway because the content came from a git
// object rather than a checkout.
//
// The default logger writes parse warnings to stderr, which would let pushed
// content write to the server's log; discard them.
func orgConfig() *org.Configuration {
	c := org.New()
	c.ReadFile = func(string) ([]byte, error) {
		return nil, errOrgIncludeDisabled
	}
	c.Log = log.New(io.Discard, "", 0)
	return c
}

var errOrgIncludeDisabled = errors.New("org: #+INCLUDE and #+SETUPFILE are disabled")

func renderReadme(name string, raw []byte) template.HTML {
	plain := func() template.HTML {
		return template.HTML("<pre>" + template.HTMLEscapeString(string(raw)) + "</pre>")
	}
	if gitutil.IsBinary(raw) {
		return ""
	}
	switch path.Ext(strings.ToLower(name)) {
	case ".md", ".markdown":
		var buf bytes.Buffer
		if markdown.Convert(raw, &buf) != nil {
			return plain()
		}
		return template.HTML(buf.String())
	case ".org":
		doc := orgConfig().Parse(bytes.NewReader(raw), name)
		writer := org.NewHTMLWriter()
		writer.HighlightCodeBlock = func(source, lang string, inline bool, params map[string]string) string {
			if inline {
				return "<code>" + template.HTMLEscapeString(source) + "</code>"
			}
			return fenceHighlight(source, lang)
		}
		out, err := doc.Write(writer)
		if err != nil {
			return plain()
		}
		return template.HTML(ugcPolicy.Sanitize(out))
	case ".html", ".htm":
		return template.HTML(ugcPolicy.Sanitize(string(raw)))
	default:
		return plain()
	}
}

type diffThread struct {
	ID       int64
	Resolved string
	Stale    bool
	Comments []renderedComment
}

// attachThreads injects review threads under their anchored diff lines;
// threads whose anchor no longer appears (stale after force-push, or on a
// context line outside the current diff) are returned separately.
func attachThreads(files []diffFile, comments []store.DiffComment, headSHA string, md func(string) template.HTML) ([]diffFile, []diffThread) {
	type anchor struct {
		path string
		side string
		line int64
	}
	threads := map[int64]*diffThread{}
	anchors := map[int64]anchor{}
	var order []int64
	for _, cm := range comments {
		if cm.ReplyTo == 0 {
			threads[cm.ID] = &diffThread{ID: cm.ID, Resolved: cm.ResolvedBy, Stale: cm.HeadSHA != headSHA,
				Comments: []renderedComment{{Author: cm.Author, CreatedAt: cm.CreatedAt, BodyHTML: md(cm.Body)}}}
			anchors[cm.ID] = anchor{cm.Path, cm.Side, cm.Line}
			order = append(order, cm.ID)
		} else if th, ok := threads[cm.ReplyTo]; ok {
			th.Comments = append(th.Comments, renderedComment{Author: cm.Author, CreatedAt: cm.CreatedAt, BodyHTML: md(cm.Body)})
		}
	}
	placed := map[int64]bool{}
	for f := range files {
		lines := files[f].Lines
		for i := range lines {
			for _, id := range order {
				if placed[id] || threads[id].Stale {
					continue
				}
				a := anchors[id]
				if lines[i].Path != a.path {
					continue
				}
				if (a.side == "new" && lines[i].NewLine == a.line && lines[i].Class != "del") ||
					(a.side == "old" && lines[i].OldLine == a.line && lines[i].Class == "del") {
					lines[i].Threads = append(lines[i].Threads, *threads[id])
					files[f].Threads++
					files[f].Open = true
					placed[id] = true
				}
			}
		}
	}
	var unplaced []diffThread
	for _, id := range order {
		if !placed[id] {
			unplaced = append(unplaced, *threads[id])
		}
	}
	return files, unplaced
}

type sigView struct {
	State       string
	Signer      string
	Fingerprint string
}

func (s *Server) sigFor(repo store.Repo, dir, sha string) (sigView, *sig.Commit) {
	raw, err := gitutil.ReadCommit(dir, sha)
	if err != nil {
		return sigView{State: "unsigned"}, nil
	}
	parsed, err := sig.ParseCommit(raw)
	if err != nil {
		return sigView{State: "unsigned"}, nil
	}
	res, err := control.VerifyCommitCached(s.st, repo, parsed, sha)
	if err != nil {
		return sigView{State: "unsigned"}, parsed
	}
	v := sigView{State: string(res.State), Fingerprint: res.KeyFingerprint}
	if res.SignerUserID != 0 {
		if u, err := s.st.UserByID(res.SignerUserID); err == nil {
			v.Signer = u.Username
		}
	}
	return v, parsed
}

func (s *Server) log(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")
	p, ok := s.repoFor(w, r, ref)
	if !ok {
		return
	}
	p.Tab = "log"
	const pageSize = 50
	// ?path= filters to commits touching one file or directory.
	filePath := strings.Trim(path.Clean("/"+r.URL.Query().Get("path")), "/")
	if filePath == "." {
		filePath = ""
	}
	var shas []string
	var err error
	if filePath != "" {
		shas, err = gitutil.RevListPath(p.Dir, p.Ref, filePath, pageSize+1)
	} else {
		shas, err = gitutil.RevList(p.Dir, p.Ref, pageSize+1)
	}
	if err != nil {
		s.notFound(w, r)
		return
	}
	next := ""
	if len(shas) > pageSize {
		next = shas[pageSize]
		shas = shas[:pageSize]
	}
	type row struct {
		SHA, ShortSHA, Subject, AuthorName, AuthorEmail, AuthorUser, Date string
		Sig                                                               sigView
		Check                                                             string // combined status, "" when none ran
	}
	names := s.authorNames()
	checks, _ := s.st.CombinedStatusFor(p.Repo.ID, shas)
	var rows []row
	for _, sha := range shas {
		v, parsed := s.sigFor(p.Repo, p.Dir, sha)
		rw := row{SHA: sha, ShortSHA: sha[:10], Sig: v, Check: checks[sha]}
		if parsed != nil {
			rw.Subject = parsed.Subject
			rw.AuthorName = names.name(parsed.AuthorEmail, parsed.AuthorName)
			rw.AuthorUser, _ = names.account(parsed.AuthorEmail)
			rw.AuthorEmail = parsed.AuthorEmail
			rw.Date = time.Unix(parsed.AuthorUnix, 0).UTC().Format("2006-01-02")
		}
		rows = append(rows, rw)
	}
	s.render(w, "log.html", struct {
		repoPage
		Commits  []row
		NextSHA  string
		FilePath string
	}{p, rows, next, filePath})
}

func (s *Server) commit(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "log"
	sha := r.PathValue("sha")
	full, err := gitutil.ResolveRef(p.Dir, sha)
	if err != nil {
		s.notFound(w, r)
		return
	}
	v, parsed := s.sigFor(p.Repo, p.Dir, full)
	if parsed == nil {
		s.notFound(w, r)
		return
	}
	patch, _ := gitutil.ShowPatch(p.Dir, full, 4<<20)
	files := parseDiff(patch)
	committerEmail := ""
	if parsed.CommitterEmail != parsed.AuthorEmail {
		committerEmail = parsed.CommitterEmail
	}
	checks, _ := s.st.ListCommitStatuses(p.Repo.ID, full)
	commitNames := s.authorNames()
	commitUser, _ := commitNames.account(parsed.AuthorEmail)
	msg := ""
	if i := bytes.Index(parsed.Payload, []byte("\n\n")); i >= 0 {
		msg = string(parsed.Payload[i+2:])
	}
	s.render(w, "commit.html", struct {
		repoPage
		SHA, ShortSHA, AuthorName, AuthorEmail, AuthorUser, CommitterEmail, Date, Message string
		Parents                                                                           []string
		Sig                                                                               sigView
		Checks                                                                            []store.CommitStatus
		DiffFiles                                                                         []diffFile
	}{p, full, full[:10], commitNames.name(parsed.AuthorEmail, parsed.AuthorName), parsed.AuthorEmail, commitUser, committerEmail,
		time.Unix(parsed.AuthorUnix, 0).UTC().Format(time.RFC3339), msg,
		gitutil.Parents(p.Dir, full), v, checks, files})
}

// labelPalette provides default label chip colors: mid-tone hues that stay
// legible on light and dark backgrounds.
var labelPalette = []string{
	"#0969da", "#1a7f37", "#9a6700", "#cf222e",
	"#8250df", "#b93a86", "#0b6c80", "#bf5b16",
}

var hexColorPat = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// labelColors returns a complete label-name -> chip color map for a repo:
// the stored labels.color when it is a valid hex color, otherwise a
// stable default picked from the palette by name hash.
func (s *Server) labelColors(repoID int64) map[string]template.CSS {
	stored, _ := s.st.LabelColors(repoID)
	out := make(map[string]template.CSS, len(stored))
	for name, color := range stored {
		if !hexColorPat.MatchString(color) {
			h := fnv.New32a()
			h.Write([]byte(name))
			color = labelPalette[h.Sum32()%uint32(len(labelPalette))]
		}
		out[name] = template.CSS("--chip:" + color)
	}
	return out
}

func (s *Server) issues(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "issues"
	state := r.URL.Query().Get("state")
	if state != "closed" && state != "all" {
		state = "open"
	}
	issues, err := s.st.ListIssues(p.Repo.ID, state, 0, 0)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if labels, err := s.st.ListIssueLabels(p.Repo.ID); err == nil {
		for i := range issues {
			issues[i].Labels = labels[issues[i].ID]
		}
	}
	// ?label=x narrows to issues carrying that label (chips link here).
	labelFilter := r.URL.Query().Get("label")
	if labelFilter != "" {
		var kept []store.Issue
		for _, iss := range issues {
			for _, l := range iss.Labels {
				if l == labelFilter {
					kept = append(kept, iss)
					break
				}
			}
		}
		issues = kept
	}
	s.render(w, "issues.html", struct {
		repoPage
		State       string
		Label       string
		Issues      []store.Issue
		LabelColors map[string]template.CSS
	}{p, state, labelFilter, issues, s.labelColors(p.Repo.ID)})
}

func (s *Server) issue(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "issues"
	n, err := strconv.ParseInt(r.PathValue("n"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	iss, err := s.st.IssueByNumber(p.Repo.ID, n)
	if err != nil {
		s.notFound(w, r)
		return
	}
	comments, err := s.st.ListIssueComments(iss.ID)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	md := s.ugcFor(r, p.Repo)
	milestones, _ := s.st.ListMilestones(p.Repo.ID, "open")
	s.render(w, "issue.html", struct {
		repoPage
		Issue       store.Issue
		BodyHTML    template.HTML
		Comments    []renderedComment
		CanEdit     bool
		CanWrite    bool
		Milestones  []store.Milestone
		Notice      string
		LabelColors map[string]template.CSS
	}{p, iss, md(iss.Body), renderComments(comments, md),
		s.canEditItem(r, p.Repo, iss.Author), s.canWriteRepo(r, p.Repo),
		milestones, r.URL.Query().Get("e"), s.labelColors(p.Repo.ID)})
}

// canEditItem: the author or anyone with write access may edit.
// canWriteRepo reports whether the browser session may push to the repo,
// which is what gates the review and merge controls.
func (s *Server) canWriteRepo(r *http.Request, repo store.Repo) bool {
	if s.cfg.Web.Mode != "accounts" {
		return false
	}
	u := s.viewer(r)
	if u.ID == 0 {
		return false
	}
	grant, _ := s.st.AccessRole(repo.ID, u.ID)
	return policy.CanWrite(u, repo, grant)
}

func (s *Server) canEditItem(r *http.Request, repo store.Repo, author string) bool {
	if s.cfg.Web.Mode != "accounts" {
		return false
	}
	u := s.viewer(r)
	if u.ID == 0 {
		return false
	}
	if u.Username == author {
		return true
	}
	grant, _ := s.st.AccessRole(repo.ID, u.ID)
	return policy.CanWrite(u, repo, grant)
}

func (s *Server) mrs(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "merge requests"
	state := r.URL.Query().Get("state")
	if state == "" {
		state = "open"
	}
	valid := map[string]bool{"open": true, "merged": true, "closed": true, "source_gone": true, "all": true}
	if !valid[state] {
		state = "open"
	}
	mrs, err := s.st.ListMRs(p.Repo.ID, state, 0, 0)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, "mrs.html", struct {
		repoPage
		State string
		MRs   []store.MR
	}{p, state, mrs})
}

func (s *Server) mr(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "merge requests"
	n, err := strconv.ParseInt(r.PathValue("n"), 10, 64)
	if err != nil {
		s.notFound(w, r)
		return
	}
	m, err := s.st.MRByNumber(p.Repo.ID, n)
	if err != nil {
		s.notFound(w, r)
		return
	}
	comments, _ := s.st.ListMRComments(m.ID)
	reviews, _ := s.st.ListMRReviews(m.ID)
	checks, _ := s.st.ListCommitStatuses(p.Repo.ID, m.HeadSHA)
	diffComments, _ := s.st.ListDiffComments(m.ID)

	headRef := fmt.Sprintf("refs/merge-requests/%d/head", m.Number)
	var files []diffFile
	base := m.MergedBase
	if base == "" {
		if b, err := gitutil.MergeBase(p.Dir, "refs/heads/"+m.TargetRef, headRef); err == nil {
			base = b
		}
	}
	if base != "" {
		if patch, err := gitutil.Diff(p.Dir, base, headRef, 4<<20); err == nil {
			files = parseDiff(patch)
		}
	}
	md := s.ugcFor(r, p.Repo)
	var detachedThreads []diffThread
	files, detachedThreads = attachThreads(files, diffComments, m.HeadSHA, md)
	stat := statOf(files)
	// The commits this MR carries: base..head, the same range as the diff.
	type commitRow struct {
		SHA, ShortSHA, Subject, AuthorName, AuthorUser, Date string
		Sig                                                  sigView
	}
	mrNames := s.authorNames()
	var commits []commitRow
	if base != "" {
		const maxMRCommits = 100
		shas, _ := gitutil.RevListRange(p.Dir, base, headRef)
		if len(shas) > maxMRCommits {
			shas = shas[:maxMRCommits]
		}
		for _, sha := range shas {
			v, parsed := s.sigFor(p.Repo, p.Dir, sha)
			cr := commitRow{SHA: sha, ShortSHA: sha[:10], Sig: v}
			if parsed != nil {
				cr.Subject = parsed.Subject
				cr.AuthorName = mrNames.name(parsed.AuthorEmail, parsed.AuthorName)
				cr.AuthorUser, _ = mrNames.account(parsed.AuthorEmail)
				cr.Date = time.Unix(parsed.AuthorUnix, 0).UTC().Format("2006-01-02")
			}
			commits = append(commits, cr)
		}
	}
	// The diff is the reason most people open a merge request, so it gets
	// its own view rather than a fold at the foot of the conversation.
	// A query parameter keeps this working without JavaScript.
	unresolved, _ := s.st.UnresolvedThreadCount(m.ID)
	view := r.URL.Query().Get("view")
	if view != "commits" && view != "diff" {
		view = "conversation"
	}
	s.render(w, "mr.html", struct {
		repoPage
		MR              store.MR
		View            string
		BodyHTML        template.HTML
		Checks          []store.CommitStatus
		Combined        string
		Comments        []renderedComment
		Reviews         []store.MRReview
		DiffFiles       []diffFile
		Stat            diffStat
		Commits         []commitRow
		CanEdit         bool
		CanWrite        bool
		Unresolved      int
		Notice          string
		DetachedThreads []diffThread
	}{p, m, view, md(m.Body), checks, store.CombinedStatus(checks), renderComments(comments, md),
		reviews, files, stat, commits, s.canEditItem(r, p.Repo, m.Author),
		s.canWriteRepo(r, p.Repo), unresolved, r.URL.Query().Get("e"), detachedThreads})
}

func (s *Server) refs(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "refs"
	branches, _ := gitutil.Refs(p.Dir, "heads")
	tags, _ := gitutil.Refs(p.Dir, "tags")
	s.render(w, "refs.html", struct {
		repoPage
		Branches, Tags []gitutil.Ref
	}{p, branches, tags})
}

func (s *Server) archive(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	file := r.PathValue("file")
	ref, ok := strings.CutSuffix(file, ".tar.gz")
	if !ok {
		s.notFound(w, r)
		return
	}
	if _, err := gitutil.ResolveRef(p.Dir, ref); err != nil {
		s.notFound(w, r)
		return
	}
	prefix := fmt.Sprintf("%s-%s", p.Repo.Name, ref)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", prefix+".tar.gz"))
	gitutil.Archive(p.Dir, ref, prefix, w)
}

func policyCanAdmin(u store.User, repo store.Repo, grant string) bool {
	return policy.CanAdmin(u, repo, grant)
}

func policyCanRead(u store.User, repo store.Repo, grant string) bool {
	return policy.CanRead(u, repo, grant)
}
