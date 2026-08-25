package httpd

import (
	"html/template"
	"net/http"
	"os"
	"path"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/gitutil"
)

// wikiDir returns the companion repo path, or "" when the repo has none.
func (s *Server) wikiDir(owner, name string) string {
	dir := control.RepoDir(s.cfg.Server.Root, owner, name+".wiki")
	if _, err := os.Stat(dir); err != nil {
		return ""
	}
	return dir
}

// wiki renders a page from the repo's wiki companion. The home page is
// Home.<ext> (or README.<ext>); /wiki/<name> resolves <name> with .md and
// .org fallbacks. Rendering reuses the same sanitized pipeline as READMEs.
func (s *Server) wiki(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "wiki"
	dir := s.wikiDir(p.Repo.OwnerName, p.Repo.Name)
	if dir == "" {
		s.render(w, "wiki.html", struct {
			repoPage
			Page     string
			PageHTML template.HTML
			Pages    []string
			Missing  bool
		}{repoPage: p, Missing: true})
		return
	}
	entries, err := gitutil.ListTree(dir, "main", "")
	if err != nil { // wiki repo exists but has no commits yet
		s.render(w, "wiki.html", struct {
			repoPage
			Page     string
			PageHTML template.HTML
			Pages    []string
			Missing  bool
		}{repoPage: p, Missing: true})
		return
	}
	var pages []string
	for _, e := range entries {
		if e.Type != "blob" {
			continue
		}
		ext := strings.ToLower(path.Ext(e.Name))
		if ext == ".md" || ext == ".org" || ext == ".markdown" {
			pages = append(pages, strings.TrimSuffix(e.Name, path.Ext(e.Name)))
		}
	}

	page := strings.Trim(r.PathValue("page"), "/")
	if page == "" {
		for _, home := range []string{"Home", "home", "README", "index"} {
			for _, pg := range pages {
				if pg == home {
					page = home
				}
			}
			if page != "" {
				break
			}
		}
		if page == "" && len(pages) > 0 {
			page = pages[0]
		}
	}
	var pageHTML template.HTML
	if page != "" {
		fileName, raw := "", []byte(nil)
		for _, ext := range []string{".md", ".org", ".markdown"} {
			if b, err := gitutil.ReadBlob(dir, "main", page+ext, maxRenderBytes); err == nil {
				fileName, raw = page+ext, b
				break
			}
		}
		if fileName == "" {
			s.notFound(w, r)
			return
		}
		pageHTML = rewriteWikiLinks(renderReadme(fileName, raw), p)
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
	dir := s.wikiDir(p.Repo.OwnerName, p.Repo.Name)
	if dir == "" {
		s.notFound(w, r)
		return
	}
	data, err := gitutil.ReadBlob(dir, "main", strings.Trim(r.PathValue("path"), "/"), s.cfg.Limits.MaxBlobBytes)
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
