package httpd

import (
	"html/template"
	"net/http"
	"path"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

// wikiTreePath is where wiki pages live in a repository's tree.
const wikiTreePath = ".gitbay/wiki"

// wikiExts are the page formats that count as wiki content.
var wikiExts = []string{".md", ".org", ".markdown"}

// wikiDir returns repo's directory and default branch, where wiki pages
// are resolved from .gitbay/wiki.
func (s *Server) wikiDir(repo store.Repo) (dir, branch string) {
	return control.RepoDir(s.cfg.Server.Root, repo.OwnerName, repo.Name), repo.DefaultBranch
}

// hasWiki reports whether repo's default branch holds a wiki page.
func (s *Server) hasWiki(repo store.Repo) bool {
	dir, branch := s.wikiDir(repo)
	entries, err := gitutil.ListTree(dir, branch, wikiTreePath)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.Type == "blob" && wikiExtMatch(e.Name) {
			return true
		}
	}
	return false
}

// wikiExtMatch reports whether name has one of the wiki page extensions.
func wikiExtMatch(name string) bool {
	ext := strings.ToLower(path.Ext(name))
	for _, want := range wikiExts {
		if ext == want {
			return true
		}
	}
	return false
}

// wiki renders a page from the repo's .gitbay/wiki tree. The home page is
// Home.<ext> (or README.<ext>); /wiki/<name> resolves <name> with .md and
// .org fallbacks. Rendering reuses the same sanitized pipeline as READMEs.
func (s *Server) wiki(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "wiki"
	var listing struct {
		Home  string   `json:"home"`
		Pages []string `json:"pages"`
	}
	var viewer store.User
	if s.cfg.Web.Mode == "accounts" {
		viewer = s.viewer(r)
	}
	_, listed := s.runControlInto(viewer, []string{"wiki", "list", p.Repo.Path()}, &listing)
	if !listed || len(listing.Pages) == 0 { // no wiki, or no commits yet
		s.render(w, "wiki.html", struct {
			repoPage
			Page     string
			PageHTML template.HTML
			Pages    []string
			Missing  bool
		}{repoPage: p, Missing: true})
		return
	}
	pages := listing.Pages

	page := strings.Trim(r.PathValue("page"), "/")
	if page == "" {
		page = listing.Home
	}
	var pageHTML template.HTML
	if page != "" {
		var shown struct {
			File    string `json:"file"`
			Content string `json:"content"`
		}
		if _, ok := s.runControlInto(viewer,
			[]string{"wiki", "show", p.Repo.Path(), page}, &shown); !ok {
			s.notFound(w, r)
			return
		}
		pageHTML = rewriteWikiLinks(renderReadme(shown.File, []byte(shown.Content)), p)
	}
	s.render(w, "wiki.html", struct {
		repoPage
		Page     string
		PageHTML template.HTML
		Pages    []string
		Missing  bool
	}{p, page, pageHTML, pages, false})
}

// wikiRaw serves non-page files from the wiki (images referenced by pages).
func (s *Server) wikiRaw(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	dir, branch := s.wikiDir(p.Repo)
	rel := path.Join(wikiTreePath, strings.Trim(r.PathValue("path"), "/"))
	if !strings.HasPrefix(rel, wikiTreePath+"/") {
		s.notFound(w, r)
		return
	}
	data, err := gitutil.ReadBlob(dir, branch, rel, s.cfg.Limits.MaxBlobBytes)
	if err != nil {
		s.notFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(data)
}

// rewriteWikiLinks makes relative links resolve inside the wiki: page
// links (with or without .md/.org/.html extensions) go to /wiki/<page>,
// other relative targets (images) to the wiki raw route.
func rewriteWikiLinks(rendered template.HTML, p repoPage) template.HTML {
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(string(rendered)), ctx)
	if err != nil {
		return rendered
	}
	base := "/" + p.Repo.Path() + "/wiki"
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for i, a := range n.Attr {
				isHref := a.Key == "href" && n.Data == "a"
				isSrc := a.Key == "src" && (n.Data == "img" || n.Data == "video" || n.Data == "source")
				if !isHref && !isSrc {
					continue
				}
				v := a.Val
				if v == "" || strings.Contains(v, "://") || strings.HasPrefix(v, "/") ||
					strings.HasPrefix(v, "#") || strings.HasPrefix(v, "mailto:") ||
					strings.HasPrefix(v, "data:") {
					continue
				}
				target := path.Clean(v)
				if strings.HasPrefix(target, "..") {
					continue
				}
				if isSrc {
					n.Attr[i].Val = base + "/_raw/" + target
					continue
				}
				ext := strings.ToLower(path.Ext(target))
				switch ext {
				case ".md", ".org", ".markdown", ".html":
					target = strings.TrimSuffix(target, path.Ext(target))
				}
				n.Attr[i].Val = base + "/" + target
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	var out strings.Builder
	for _, n := range nodes {
		walk(n)
		if err := html.Render(&out, n); err != nil {
			return rendered
		}
	}
	return template.HTML(out.String())
}
