package httpd

import (
	"bytes"
	"fmt"
	"html/template"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/alecthomas/chroma/v2/formatters/html"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
	"github.com/yuin/goldmark"

	"github.com/krazywarez/forge/internal/control"
	"github.com/krazywarez/forge/internal/gitutil"
	"github.com/krazywarez/forge/internal/sig"
	"github.com/krazywarez/forge/internal/store"
	"github.com/krazywarez/forge/internal/web"
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

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	repos, err := s.st.ListPublicRepos()
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	s.render(w, "index.html", struct {
		Site  string
		Repos []store.Repo
	}{s.siteName(), repos})
}

// repoPage is the shared context for repo-scoped pages.
type repoPage struct {
	Site     string
	Repo     store.Repo
	Ref      string
	CloneURL string
	Dir      string
}

// repoFor resolves the repo for a web request; false means 404 was sent.
// The anonymous web sees public repos only — private and missing repos are
// indistinguishable.
func (s *Server) repoFor(w http.ResponseWriter, r *http.Request, ref string) (repoPage, bool) {
	repo, ok := s.publicRepo(r.PathValue("owner"), r.PathValue("repo"))
	if !ok {
		http.NotFound(w, r)
		return repoPage{}, false
	}
	if ref == "" {
		ref = repo.DefaultBranch
	}
	return repoPage{
		Site:     s.siteName(),
		Repo:     repo,
		Ref:      ref,
		CloneURL: s.cfg.Server.SiteURL + "/" + repo.Path() + ".git",
		Dir:      control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name),
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

func (s *Server) repoHome(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	s.renderTree(w, r, p, "")
}

func (s *Server) tree(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, r.PathValue("ref"))
	if !ok {
		return
	}
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
			ReadmeHTML template.HTML
		}{repoPage: p})
		return
	}
	entries, err := gitutil.ListTree(p.Dir, p.Ref, dirPath)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	prefix := ""
	if dirPath != "" {
		prefix = dirPath + "/"
	}

	var readmeHTML template.HTML
	for _, e := range entries {
		if e.Type != "blob" {
			continue
		}
		lower := strings.ToLower(e.Name)
		if lower == "readme" || lower == "readme.md" || lower == "readme.markdown" {
			raw, err := gitutil.ReadBlob(p.Dir, p.Ref, prefix+e.Name, maxRenderBytes)
			if err == nil {
				var buf bytes.Buffer
				if strings.HasSuffix(lower, ".md") || strings.HasSuffix(lower, ".markdown") {
					// goldmark's default renderer drops raw HTML: safe.
					if goldmark.Convert(raw, &buf) == nil {
						readmeHTML = template.HTML(buf.String())
					}
				} else {
					readmeHTML = template.HTML("<pre>" + template.HTMLEscapeString(string(raw)) + "</pre>")
				}
			}
			break
		}
	}

	s.render(w, "tree.html", struct {
		repoPage
		Crumbs     []crumb
		Prefix     string
		Entries    []gitutil.TreeEntry
		ReadmeHTML template.HTML
	}{p, crumbs(p, "tree", dirPath), prefix, entries, readmeHTML})
}

func (s *Server) blob(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, r.PathValue("ref"))
	if !ok {
		return
	}
	filePath := strings.Trim(r.PathValue("path"), "/")
	data, err := gitutil.ReadBlob(p.Dir, p.Ref, filePath, maxRenderBytes+1)
	if err != nil {
		http.NotFound(w, r)
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
		http.NotFound(w, r)
		return
	}
	// Serve inert: never let repo content execute in the forge's origin.
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(data)
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
	const pageSize = 50
	shas, err := gitutil.RevList(p.Dir, p.Ref, pageSize+1)
	if err != nil {
		http.NotFound(w, r)
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
	sha := r.PathValue("sha")
	full, err := gitutil.ResolveRef(p.Dir, sha)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	v, parsed := s.sigFor(p.Repo, p.Dir, full)
	if parsed == nil {
		http.NotFound(w, r)
		return
	}
	patch, _ := gitutil.ShowPatch(p.Dir, full, 4<<20)
	type diffLine struct {
		Class string
		Text  string
	}
	var lines []diffLine
	for _, l := range strings.Split(patch, "\n") {
		class := ""
		switch {
		case strings.HasPrefix(l, "+++"), strings.HasPrefix(l, "---"), strings.HasPrefix(l, "diff "), strings.HasPrefix(l, "index "):
			class = "meta"
		case strings.HasPrefix(l, "@@"):
			class = "hunk"
		case strings.HasPrefix(l, "+"):
			class = "add"
		case strings.HasPrefix(l, "-"):
			class = "del"
		}
		lines = append(lines, diffLine{class, l})
	}
	committerEmail := ""
	if parsed.CommitterEmail != parsed.AuthorEmail {
		committerEmail = parsed.CommitterEmail
	}
	msg := ""
	if i := bytes.Index(parsed.Payload, []byte("\n\n")); i >= 0 {
		msg = string(parsed.Payload[i+2:])
	}
	s.render(w, "commit.html", struct {
		repoPage
		SHA, ShortSHA, AuthorName, AuthorEmail, CommitterEmail, Date, Message string
		Sig                                                                   sigView
		DiffLines                                                             []diffLine
	}{p, full, full[:10], parsed.AuthorName, parsed.AuthorEmail, committerEmail,
		time.Unix(parsed.AuthorUnix, 0).UTC().Format(time.RFC3339), msg, v, lines})
}

func (s *Server) refs(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
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
		http.NotFound(w, r)
		return
	}
	if _, err := gitutil.ResolveRef(p.Dir, ref); err != nil {
		http.NotFound(w, r)
		return
	}
	prefix := fmt.Sprintf("%s-%s", p.Repo.Name, ref)
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", prefix+".tar.gz"))
	gitutil.Archive(p.Dir, ref, prefix, w)
}
