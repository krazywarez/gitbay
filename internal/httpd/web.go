package httpd

import (
	"bytes"
	"fmt"
	"hash/fnv"

	"gitbay.org/gitbay/internal/policy"
	"html/template"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/microcosm-cc/bluemonday"
	"github.com/niklasfasching/go-org/org"
	"github.com/yuin/goldmark"

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

func (s *Server) siteName() string {
	h := strings.TrimPrefix(strings.TrimPrefix(s.cfg.Server.SiteURL, "https://"), "http://")
	return strings.TrimSuffix(h, "/")
}

func (s *Server) stylesheet(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	w.Write(web.StyleCSS)
}

func (s *Server) favicon(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "image/svg+xml")
	w.Write(web.FaviconSVG)
}

// notFound renders the designed 404 page with a 404 status. Falls back to
// the stock plain-text response if the template fails.
func (s *Server) notFound(w http.ResponseWriter, r *http.Request) {
	var buf bytes.Buffer
	if err := web.Render(&buf, "404.html", struct{ Site string }{s.siteName()}); err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	buf.WriteTo(w)
}

// describedRepo pairs a repo with its description for listings.
type describedRepo struct {
	store.Repo
	Desc string
}

func (s *Server) describeAll(repos []store.Repo) []describedRepo {
	var out []describedRepo
	for _, r := range repos {
		out = append(out, describedRepo{r, gitutil.ReadDescription(control.RepoDir(s.cfg.Server.Root, r.OwnerName, r.Name))})
	}
	return out
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	repos, err := s.st.ListPublicRepos()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var viewer store.User
	var mine []store.Repo
	if s.cfg.Web.Mode == "accounts" {
		if viewer = s.viewer(r); viewer.ID != 0 {
			all, err := s.st.ListReposForUser(viewer.ID)
			if err == nil {
				for _, rp := range all {
					if rp.Visibility == "private" {
						mine = append(mine, rp)
					}
				}
			}
		}
	}
	s.render(w, "index.html", struct {
		Site   string
		Viewer string
		Repos  []describedRepo
		Mine   []describedRepo
	}{s.siteName(), viewer.Username, s.describeAll(repos), s.describeAll(mine)})
}

// repoPage is the shared context for repo-scoped pages.
type repoPage struct {
	Site     string
	Viewer   string
	Desc     string
	Repo     store.Repo
	Ref      string
	CloneURL string
	Dir      string
	Tab      string // active tab in the repo header
	Topics   []string
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
	if ok {
		grant := ""
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
	return repoPage{
		Site:     s.siteName(),
		Viewer:   viewer.Username,
		Desc:     gitutil.ReadDescription(control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name)),
		Repo:     repo,
		Ref:      ref,
		CloneURL: s.cfg.Server.SiteURL + "/" + repo.Path() + ".git",
		Dir:      control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name),
		Topics:   topics,
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
	s.render(w, "owner.html", struct {
		Site    string
		Viewer  string
		Owner   string
		Kind    string
		Profile store.Profile
		Repos   []describedRepo
		Members []store.OrgMember
		Orgs    []store.OrgMember
	}{s.siteName(), viewer.Username, name, kind, profile, s.describeAll(visible), members, orgs})
}

func (s *Server) repoHome(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "files"
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

func (s *Server) renderTree(w http.ResponseWriter, r *http.Request, p repoPage, dirPath string) {
	if _, err := gitutil.ResolveRef(p.Dir, p.Ref); err != nil {
		// Empty repo: render the page with no entries rather than 404.
		s.render(w, "tree.html", struct {
			repoPage
			Crumbs     []crumb
			Prefix     string
			Entries    []gitutil.TreeEntry
			ReadmeName string
			ReadmeHTML template.HTML
		}{repoPage: p})
		return
	}
	entries, err := gitutil.ListTree(p.Dir, p.Ref, dirPath)
	if err != nil {
		s.notFound(w, r)
		return
	}
	prefix := ""
	if dirPath != "" {
		prefix = dirPath + "/"
	}

	var readmeHTML template.HTML
	readmeName := pickReadme(entries)
	if readmeName != "" {
		if raw, err := gitutil.ReadBlob(p.Dir, p.Ref, prefix+readmeName, maxRenderBytes); err == nil {
			readmeHTML = renderReadme(readmeName, raw)
		}
	}

	s.render(w, "tree.html", struct {
		repoPage
		Crumbs     []crumb
		Prefix     string
		Entries    []gitutil.TreeEntry
		ReadmeName string
		ReadmeHTML template.HTML
	}{p, crumbs(p, "tree", dirPath), prefix, entries, readmeName, readmeHTML})
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

	var codeHTML template.HTML
	if !binary {
		codeHTML = highlight(filePath, data)
	}
	cs := crumbs(p, "blob", filePath)
	base := ""
	if len(cs) > 0 {
		base = cs[len(cs)-1].Name
		cs = cs[:len(cs)-1]
	}
	s.render(w, "blob.html", struct {
		repoPage
		Crumbs   []crumb
		Base     string
		Path     string
		Binary   bool
		Size     int
		CodeHTML template.HTML
	}{p, cs, base, filePath, binary, len(data), codeHTML})
}

// blamePageSize caps how many lines one blame page renders; blame is a
// per-line subprocess cost, so large files paginate.
const blamePageSize = 1000

func (s *Server) blame(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, r.PathValue("ref"))
	if !ok {
		return
	}
	p.Tab = "files"
	filePath := strings.Trim(r.PathValue("path"), "/")
	data, err := gitutil.ReadBlob(p.Dir, p.Ref, filePath, s.cfg.Limits.MaxBlobBytes)
	if err != nil {
		s.notFound(w, r)
		return
	}
	total := bytes.Count(data, []byte("\n"))
	if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
		total++
	}
	binary := gitutil.IsBinary(data)

	type hunkView struct {
		gitutil.BlameHunk
		ShortSHA string
		Date     string
		Sig      sigView
		Numbered []numberedLine
	}
	var hunks []hunkView
	page, pages := 1, (total+blamePageSize-1)/blamePageSize
	if pages == 0 {
		pages = 1
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("page")); err == nil && n >= 1 && n <= pages {
		page = n
	}
	if !binary && total > 0 {
		start := (page-1)*blamePageSize + 1
		end := min(total, page*blamePageSize)
		raw, err := gitutil.Blame(p.Dir, p.Ref, filePath, start, end)
		if err != nil {
			s.notFound(w, r)
			return
		}
		sigs := map[string]sigView{}
		for _, h := range raw {
			v, ok := sigs[h.SHA]
			if !ok {
				v, _ = s.sigFor(p.Repo, p.Dir, h.SHA)
				sigs[h.SHA] = v
			}
			hv := hunkView{BlameHunk: h, ShortSHA: h.SHA[:10],
				Date: time.Unix(h.AuthorUnix, 0).UTC().Format("2006-01-02"), Sig: v}
			for i, l := range h.Lines {
				hv.Numbered = append(hv.Numbered, numberedLine{h.StartLine + i, l})
			}
			hunks = append(hunks, hv)
		}
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

func highlight(filePath string, data []byte) template.HTML {
	lexer := lexers.Match(filePath)
	if lexer == nil {
		lexer = lexers.Fallback
	}
	style := styles.Get("friendly")
	formatter := html.New(html.WithLineNumbers(true), html.LineNumbersInTable(false))
	iterator, err := lexer.Tokenise(nil, string(data))
	if err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(string(data)) + "</pre>")
	}
	var buf bytes.Buffer
	if err := formatter.Format(&buf, style, iterator); err != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(string(data)) + "</pre>")
	}
	return template.HTML(buf.String())
}

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
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(data)
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

// mdHTML renders user-authored markdown (issue and MR bodies, comments).
// goldmark's default renderer drops raw HTML, so this is safe as-is.
func mdHTML(raw string) template.HTML {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var buf bytes.Buffer
	if goldmark.Convert([]byte(raw), &buf) != nil {
		return template.HTML("<pre>" + template.HTMLEscapeString(raw) + "</pre>")
	}
	return template.HTML(buf.String())
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
	BodyHTML  template.HTML
}

func renderComments(cs []store.IssueComment, md func(string) template.HTML) []renderedComment {
	var out []renderedComment
	for _, c := range cs {
		out = append(out, renderedComment{c.Author, c.CreatedAt, md(c.Body)})
	}
	return out
}

// ugcPolicy sanitizes rendered repo content before it enters the forge's
// origin: markdown is already safe (goldmark drops raw HTML), but org-mode
// output and repo-authored HTML are not.
var ugcPolicy = bluemonday.UGCPolicy()

// renderReadme renders a README by extension: markdown, org-mode, and
// (sanitized) HTML richly; everything else as escaped plaintext.
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
		if goldmark.Convert(raw, &buf) != nil {
			return plain()
		}
		return template.HTML(buf.String())
	case ".org":
		doc := org.New().Parse(bytes.NewReader(raw), name)
		html, err := doc.Write(org.NewHTMLWriter())
		if err != nil {
			return plain()
		}
		return template.HTML(ugcPolicy.Sanitize(html))
	case ".html", ".htm":
		return template.HTML(ugcPolicy.Sanitize(string(raw)))
	default:
		return plain()
	}
}

type diffLine struct {
	Class   string
	Text    string
	Path    string // file this line belongs to
	NewLine int64  // line number in the new file (0 when absent)
	OldLine int64  // line number in the old file (0 when absent)
	Threads []diffThread
}

var hunkPat = regexp.MustCompile(`^@@ -(\d+)(?:,\d+)? \+(\d+)(?:,\d+)? @@`)

// classifyDiff parses a unified diff into rendered lines, tracking the
// file and old/new line numbers so review threads can anchor inline.
func classifyDiff(patch string) []diffLine {
	var lines []diffLine
	path := ""
	var oldN, newN int64
	for _, l := range strings.Split(patch, "\n") {
		d := diffLine{Text: l}
		switch {
		case strings.HasPrefix(l, "+++ "):
			d.Class = "meta"
			path = strings.TrimPrefix(strings.TrimPrefix(l, "+++ "), "b/")
		case strings.HasPrefix(l, "--- "), strings.HasPrefix(l, "diff "), strings.HasPrefix(l, "index "):
			d.Class = "meta"
		case strings.HasPrefix(l, "@@"):
			d.Class = "hunk"
			if m := hunkPat.FindStringSubmatch(l); m != nil {
				oldN, _ = strconv.ParseInt(m[1], 10, 64)
				newN, _ = strconv.ParseInt(m[2], 10, 64)
			}
		case strings.HasPrefix(l, "+"):
			d.Class, d.Path, d.NewLine = "add", path, newN
			newN++
		case strings.HasPrefix(l, "-"):
			d.Class, d.Path, d.OldLine = "del", path, oldN
			oldN++
		default:
			d.Path, d.OldLine, d.NewLine = path, oldN, newN
			oldN++
			newN++
		}
		lines = append(lines, d)
	}
	return lines
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
func attachThreads(lines []diffLine, comments []store.DiffComment, headSHA string, md func(string) template.HTML) ([]diffLine, []diffThread) {
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
				Comments: []renderedComment{{cm.Author, cm.CreatedAt, md(cm.Body)}}}
			anchors[cm.ID] = anchor{cm.Path, cm.Side, cm.Line}
			order = append(order, cm.ID)
		} else if th, ok := threads[cm.ReplyTo]; ok {
			th.Comments = append(th.Comments, renderedComment{cm.Author, cm.CreatedAt, md(cm.Body)})
		}
	}
	placed := map[int64]bool{}
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
				placed[id] = true
			}
		}
	}
	var unplaced []diffThread
	for _, id := range order {
		if !placed[id] {
			unplaced = append(unplaced, *threads[id])
		}
	}
	return lines, unplaced
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
	shas, err := gitutil.RevList(p.Dir, p.Ref, pageSize+1)
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
		SHA, ShortSHA, Subject, AuthorName, AuthorEmail, Date string
		Sig                                                   sigView
	}
	var rows []row
	for _, sha := range shas {
		v, parsed := s.sigFor(p.Repo, p.Dir, sha)
		rw := row{SHA: sha, ShortSHA: sha[:10], Sig: v}
		if parsed != nil {
			rw.Subject = parsed.Subject
			rw.AuthorName = parsed.AuthorName
			rw.AuthorEmail = parsed.AuthorEmail
			rw.Date = time.Unix(parsed.AuthorUnix, 0).UTC().Format("2006-01-02")
		}
		rows = append(rows, rw)
	}
	s.render(w, "log.html", struct {
		repoPage
		Commits []row
		NextSHA string
	}{p, rows, next})
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
	lines := classifyDiff(patch)
	committerEmail := ""
	if parsed.CommitterEmail != parsed.AuthorEmail {
		committerEmail = parsed.CommitterEmail
	}
	checks, _ := s.st.ListCommitStatuses(p.Repo.ID, full)
	msg := ""
	if i := bytes.Index(parsed.Payload, []byte("\n\n")); i >= 0 {
		msg = string(parsed.Payload[i+2:])
	}
	s.render(w, "commit.html", struct {
		repoPage
		SHA, ShortSHA, AuthorName, AuthorEmail, CommitterEmail, Date, Message string
		Sig                                                                   sigView
		Checks                                                                []store.CommitStatus
		DiffLines                                                             []diffLine
	}{p, full, full[:10], parsed.AuthorName, parsed.AuthorEmail, committerEmail,
		time.Unix(parsed.AuthorUnix, 0).UTC().Format(time.RFC3339), msg, v, checks, lines})
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
	issues, err := s.st.ListIssues(p.Repo.ID, state)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if labels, err := s.st.ListIssueLabels(p.Repo.ID); err == nil {
		for i := range issues {
			issues[i].Labels = labels[issues[i].ID]
		}
	}
	s.render(w, "issues.html", struct {
		repoPage
		State       string
		Issues      []store.Issue
		LabelColors map[string]template.CSS
	}{p, state, issues, s.labelColors(p.Repo.ID)})
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
	s.render(w, "issue.html", struct {
		repoPage
		Issue       store.Issue
		BodyHTML    template.HTML
		Comments    []renderedComment
		LabelColors map[string]template.CSS
	}{p, iss, md(iss.Body), renderComments(comments, md), s.labelColors(p.Repo.ID)})
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
	mrs, err := s.st.ListMRs(p.Repo.ID, state)
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
	var lines []diffLine
	base := m.MergedBase
	if base == "" {
		if b, err := gitutil.MergeBase(p.Dir, "refs/heads/"+m.TargetRef, headRef); err == nil {
			base = b
		}
	}
	if base != "" {
		if patch, err := gitutil.Diff(p.Dir, base, headRef, 4<<20); err == nil {
			lines = classifyDiff(patch)
		}
	}
	md := s.ugcFor(r, p.Repo)
	var detachedThreads []diffThread
	lines, detachedThreads = attachThreads(lines, diffComments, m.HeadSHA, md)
	s.render(w, "mr.html", struct {
		repoPage
		MR              store.MR
		BodyHTML        template.HTML
		Checks          []store.CommitStatus
		Combined        string
		Comments        []renderedComment
		Reviews         []store.MRReview
		DiffLines       []diffLine
		DetachedThreads []diffThread
	}{p, m, md(m.Body), checks, store.CombinedStatus(checks), renderComments(comments, md), reviews, lines, detachedThreads})
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

func policyCanRead(u store.User, repo store.Repo, grant string) bool {
	return policy.CanRead(u, repo, grant)
}
