package httpd

import (
	"html/template"
	"net/http"
	"path"
	"sort"
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
	entries, err := gitutil.ListTreeRecursive(dir, branch, wikiTreePath)
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
// .org fallbacks, and a name may be a path into a subfolder. Rendering
// reuses the same sanitized pipeline as READMEs.
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
			Nav      []wikiNavGroup
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
		isPage := map[string]bool{}
		for _, pg := range pages {
			isPage[pg] = true
		}
		isFile := map[string]bool{}
		dir, branch := s.wikiDir(p.Repo)
		if entries, err := gitutil.ListTreeRecursive(dir, branch, wikiTreePath); err == nil {
			for _, e := range entries {
				isFile[e.Name] = e.Type == "blob"
			}
		}
		pageHTML = rewriteWikiLinks(renderReadme(shown.File, []byte(shown.Content)), p, page,
			func(t string) bool { return isPage[t] }, func(t string) bool { return isFile[t] })
	}
	s.render(w, "wiki.html", struct {
		repoPage
		Page     string
		PageHTML template.HTML
		Nav      []wikiNavGroup
		Missing  bool
	}{p, page, pageHTML, wikiNav(pages), false})
}

// wikiNavGroup is one folder of the wiki sidebar; Dir is empty for the
// top level, which comes first.
type wikiNavGroup struct {
	Dir   string
	Pages []wikiNavPage
}

type wikiNavPage struct {
	Name  string // the page's path, as routed
	Label string // its last segment
}

// wikiNav groups page names by folder, keeping the listing's order within
// each folder.
func wikiNav(pages []string) []wikiNavGroup {
	var groups []wikiNavGroup
	at := map[string]int{}
	for _, pg := range pages {
		d := path.Dir(pg)
		if d == "." {
			d = ""
		}
		i, ok := at[d]
		if !ok {
			i = len(groups)
			at[d] = i
			groups = append(groups, wikiNavGroup{Dir: d})
		}
		groups[i].Pages = append(groups[i].Pages, wikiNavPage{Name: pg, Label: path.Base(pg)})
	}
	sort.SliceStable(groups, func(a, b int) bool {
		if (groups[a].Dir == "") != (groups[b].Dir == "") {
			return groups[a].Dir == ""
		}
		return groups[a].Dir < groups[b].Dir
	})
	return groups
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
	ct := "application/octet-stream"
	if t, ok := imageTypes[strings.ToLower(path.Ext(rel))]; ok {
		ct = t
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Write(data)
}

// wikiResolve maps a relative link on page to a path inside the wiki. A
// page in a subfolder looks in its own folder first and then at the wiki
// root, so [[Admin]] still reaches a top-level page; when neither exists
// the folder-relative path is kept, so a broken link points where its
// author wrote it. A top-level page resolves exactly as before.
func wikiResolve(page, v string, exists func(string) bool) (string, bool) {
	dir := path.Dir(page)
	local := path.Clean(path.Join(dir, v))
	if local == ".." || strings.HasPrefix(local, "../") {
		return "", false
	}
	if dir == "." || exists(local) {
		return local, true
	}
	if root := path.Clean(v); root != ".." && !strings.HasPrefix(root, "../") && exists(root) {
		return root, true
	}
	return local, true
}

// trimPageExt drops a page extension from a link target.
func trimPageExt(target string) string {
	switch strings.ToLower(path.Ext(target)) {
	case ".md", ".org", ".markdown", ".html":
		return strings.TrimSuffix(target, path.Ext(target))
	}
	return target
}

// rewriteWikiLinks makes relative links resolve inside the wiki: page
// links (with or without .md/.org/.html extensions) go to /wiki/<page>,
// other relative targets (images) to the wiki raw route. Targets resolve
// from the current page's folder, as wikiResolve describes.
func rewriteWikiLinks(rendered template.HTML, p repoPage, page string, isPage, isFile func(string) bool) template.HTML {
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
				if isSrc {
					if target, ok := wikiResolve(page, v, isFile); ok {
						n.Attr[i].Val = base + "/_raw/" + target
					}
					continue
				}
				// A plain link is usually to another page, but a link to
				// an existing non-page file (an .svg, .txt, .pdf) must
				// go to _raw the same as an image src, or it 404s
				// against the page route (#283).
				if target, ok := wikiResolve(page, trimPageExt(v), isPage); ok {
					if raw, rok := wikiResolve(page, v, isFile); rok && isFile(raw) && !isPage(target) {
						n.Attr[i].Val = base + "/_raw/" + raw
					} else {
						n.Attr[i].Val = base + "/" + target
					}
				}
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
