package httpd

import (
	"html/template"
	"path"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"gitbay.org/gitbay/internal/gitutil"
)

// rewriteRelativeLinks makes relative hrefs and srcs in rendered repo
// content resolve on the forge: links go to blob pages, images to raw.
// go-org exports .org links as .html, so an .html target whose .org (or
// .md) source exists in the tree maps back to the source file.
func rewriteRelativeLinks(rendered template.HTML, p repoPage, baseDir string) template.HTML {
	return rewriteRelativeTargets(rendered, func(v string, src bool) (string, bool) {
		target := path.Clean(path.Join(baseDir, v))
		if strings.HasPrefix(target, "..") {
			return "", false
		}
		if src {
			return "/" + p.Repo.Path() + "/raw/" + p.Ref + "/" + target, true
		}
		return "/" + p.Repo.Path() + "/blob/" + p.Ref + "/" + exportSource(p, target), true
	})
}

// exportSource maps an .html target back to the .org or .md file in the
// tree it was exported from, if there is one.
func exportSource(p repoPage, target string) string {
	stem, ok := strings.CutSuffix(target, ".html")
	if !ok {
		return target
	}
	for _, ext := range []string{".org", ".md"} {
		if _, err := gitutil.ReadBlob(p.Dir, p.Ref, stem+ext, 1); err == nil {
			return stem + ext
		}
	}
	return target
}

// rewriteRelativeTargets passes each relative link href and img, video or
// source src in rendered to rewrite (src is true for the media), and sets
// the attribute to what it returns when ok.
func rewriteRelativeTargets(rendered template.HTML, rewrite func(v string, src bool) (string, bool)) template.HTML {
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(string(rendered)), ctx)
	if err != nil {
		return rendered
	}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			for i, a := range n.Attr {
				isHref := a.Key == "href" && n.Data == "a"
				isSrc := a.Key == "src" && (n.Data == "img" || n.Data == "video" || n.Data == "source")
				if (!isHref && !isSrc) || !isRelativeTarget(a.Val) {
					continue
				}
				if v, ok := rewrite(a.Val, isSrc); ok {
					n.Attr[i].Val = v
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

// isRelativeTarget is false for empty targets, absolute URLs, rooted
// paths, fragments, mailto: and data:.
func isRelativeTarget(v string) bool {
	if v == "" || strings.Contains(v, "://") {
		return false
	}
	for _, prefix := range []string{"/", "#", "mailto:", "data:"} {
		if strings.HasPrefix(v, prefix) {
			return false
		}
	}
	return true
}
