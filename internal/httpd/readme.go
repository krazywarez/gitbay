package httpd

import (
	"html/template"
	"path"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"gitbay.org/gitbay/internal/gitutil"
)

// licenseNames maps content markers to display names, checked in order.
var licenseChecks = []struct{ marker, name string }{
	{"MIT License", "MIT"},
	{"Permission is hereby granted, free of charge", "MIT"},
	{"Apache License", "Apache-2.0"},
	{"GNU AFFERO GENERAL PUBLIC LICENSE", "AGPL-3.0"},
	{"GNU GENERAL PUBLIC LICENSE", "GPL"},
	{"GNU LESSER GENERAL PUBLIC LICENSE", "LGPL"},
	{"Mozilla Public License", "MPL-2.0"},
	{"BSD 3-Clause", "BSD-3-Clause"},
	{"BSD 2-Clause", "BSD-2-Clause"},
	{"Redistribution and use in source and binary forms", "BSD"},
	{"BSD Zero Clause License", "0BSD"},
	{"Zero Clause BSD", "0BSD"},
	// ISC is the 0BSD grant plus the notice-retention clause; match on the
	// clause so title-less 0BSD files don't read as ISC.
	{"hereby granted, provided that the above copyright notice", "ISC"},
	{"Permission to use, copy, modify, and/or distribute", "0BSD"},
	{"This is free and unencumbered software", "Unlicense"},
	{"CC0", "CC0"},
}

// detectLicense reports the repo's license name from a conventional file
// at the ref root, or "".
func detectLicense(dir, ref string) string {
	for _, name := range []string{"LICENSE", "LICENSE.md", "LICENSE.txt", "COPYING", "UNLICENSE"} {
		raw, err := gitutil.ReadBlob(dir, ref, name, 2048)
		if err != nil {
			continue
		}
		// Collapse whitespace so markers match across line wraps.
		text := strings.Join(strings.Fields(string(raw)), " ")
		for _, c := range licenseChecks {
			if strings.Contains(text, c.marker) {
				return c.name
			}
		}
		return "license"
	}
	return ""
}

// rewriteRelativeLinks makes relative hrefs and srcs in rendered repo
// content resolve on the forge: links go to blob pages, images to raw.
// go-org exports .org links as .html, so an .html target whose .org (or
// .md) source exists in the tree maps back to the source file.
func rewriteRelativeLinks(rendered template.HTML, p repoPage, baseDir string) template.HTML {
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
				if !isHref && !isSrc {
					continue
				}
				v := a.Val
				if v == "" || strings.Contains(v, "://") || strings.HasPrefix(v, "/") ||
					strings.HasPrefix(v, "#") || strings.HasPrefix(v, "mailto:") ||
					strings.HasPrefix(v, "data:") {
					continue
				}
				target := path.Clean(path.Join(baseDir, v))
				if strings.HasPrefix(target, "..") {
					continue
				}
				if isSrc {
					n.Attr[i].Val = "/" + p.Repo.Path() + "/raw/" + p.Ref + "/" + target
					continue
				}
				// .html from org/markdown exports maps back to the source.
				if strings.HasSuffix(target, ".html") {
					stem := strings.TrimSuffix(target, ".html")
					for _, ext := range []string{".org", ".md"} {
						if _, err := gitutil.ReadBlob(p.Dir, p.Ref, stem+ext, 1); err == nil {
							target = stem + ext
							break
						}
					}
				}
				n.Attr[i].Val = "/" + p.Repo.Path() + "/blob/" + p.Ref + "/" + target
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
