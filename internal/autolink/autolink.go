// Package autolink rewrites cross-references in rendered HTML: #N and !N
// to the repository's issues and merge requests, owner/name#N (and !N)
// across repositories, and @user to owner pages. It operates on the HTML
// produced by the markdown/org pipeline, walking text nodes with a real
// parser so nothing inside <a>, <code>, or <pre> is ever touched, and only
// references that actually resolve become links.
package autolink

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// Resolver answers whether a reference target exists and where it lives.
// Empty return means "not a real target: leave the text alone".
type Resolver interface {
	// RefURL resolves issue (#) or merge request (!) number n in
	// owner/name; kind is '#' or '!'.
	RefURL(owner, name string, kind byte, n int64) string
	// UserURL resolves a user or org name to its owner page.
	UserURL(name string) string
}

var (
	// owner/name#N or owner/name!N
	crossRefPat = regexp.MustCompile(`([a-z0-9][a-z0-9._-]*)/([a-z0-9][a-z0-9._-]*)([#!])([0-9]+)`)
	// #N or !N with a boundary before, so a1b2#3 in a hash stays text
	bareRefPat = regexp.MustCompile(`(^|[\s([{])([#!])([0-9]+)\b`)
	// @user with a boundary before
	mentionPat = regexp.MustCompile(`(^|[\s([{])@([a-z0-9][a-z0-9._-]*)`)
)

// skip lists elements whose text must never be rewritten.
var skip = map[string]bool{"a": true, "code": true, "pre": true, "script": true, "style": true}

// Rewrite processes an HTML fragment, linking references relative to
// defaultOwner/defaultName. On any parse failure the input is returned
// unchanged.
func Rewrite(fragment, defaultOwner, defaultName string, r Resolver) string {
	ctx := &html.Node{Type: html.ElementNode, Data: "div", DataAtom: atom.Div}
	nodes, err := html.ParseFragment(strings.NewReader(fragment), ctx)
	if err != nil {
		return fragment
	}
	var out strings.Builder
	for _, n := range nodes {
		walk(n, defaultOwner, defaultName, r)
		if err := html.Render(&out, n); err != nil {
			return fragment
		}
	}
	return out.String()
}

func walk(n *html.Node, owner, name string, r Resolver) {
	if n.Type == html.ElementNode && skip[n.Data] {
		return
	}
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		if c.Type == html.TextNode {
			if repl := rewriteText(c.Data, owner, name, r); repl != nil {
				for _, rn := range repl {
					n.InsertBefore(rn, c)
				}
				n.RemoveChild(c)
			}
		} else {
			walk(c, owner, name, r)
		}
		c = next
	}
}

type span struct {
	start, end int
	url, text  string
}

// rewriteText returns replacement nodes for a text node, or nil when no
// reference resolved.
func rewriteText(text, owner, name string, r Resolver) []*html.Node {
	var spans []span

	for _, m := range crossRefPat.FindAllStringSubmatchIndex(text, -1) {
		o, rep := text[m[2]:m[3]], text[m[4]:m[5]]
		kind := text[m[6]]
		n, _ := strconv.ParseInt(text[m[8]:m[9]], 10, 64)
		if url := r.RefURL(o, rep, kind, n); url != "" {
			spans = append(spans, span{m[0], m[1], url, text[m[0]:m[1]]})
		}
	}
	for _, m := range bareRefPat.FindAllStringSubmatchIndex(text, -1) {
		kind := text[m[4]]
		n, _ := strconv.ParseInt(text[m[6]:m[7]], 10, 64)
		if overlaps(spans, m[4], m[7]) {
			continue
		}
		if url := r.RefURL(owner, name, kind, n); url != "" {
			spans = append(spans, span{m[4], m[7], url, text[m[4]:m[7]]})
		}
	}
	for _, m := range mentionPat.FindAllStringSubmatchIndex(text, -1) {
		if overlaps(spans, m[4]-1, m[5]) {
			continue
		}
		who := text[m[4]:m[5]]
		url := r.UserURL(who)
		if url == "" {
			// Names may legally contain ._- but a sentence-ending
			// "@alice." usually means the user, not "alice.".
			trimmed := strings.TrimRight(who, "._-")
			if trimmed != "" && trimmed != who {
				if u := r.UserURL(trimmed); u != "" {
					who, url = trimmed, u
				}
			}
		}
		if url != "" {
			spans = append(spans, span{m[4] - 1, m[4] + len(who), url, "@" + who})
		}
	}
	if len(spans) == 0 {
		return nil
	}
	sortSpans(spans)

	var nodes []*html.Node
	pos := 0
	for _, s := range spans {
		if s.start < pos {
			continue // overlap safety
		}
		if s.start > pos {
			nodes = append(nodes, &html.Node{Type: html.TextNode, Data: text[pos:s.start]})
		}
		a := &html.Node{Type: html.ElementNode, Data: "a",
			Attr: []html.Attribute{{Key: "href", Val: s.url}, {Key: "class", Val: "xref"}}}
		a.AppendChild(&html.Node{Type: html.TextNode, Data: s.text})
		nodes = append(nodes, a)
		pos = s.end
	}
	if pos < len(text) {
		nodes = append(nodes, &html.Node{Type: html.TextNode, Data: text[pos:]})
	}
	return nodes
}

func overlaps(spans []span, start, end int) bool {
	for _, s := range spans {
		if start < s.end && end > s.start {
			return true
		}
	}
	return false
}

func sortSpans(spans []span) {
	for i := 1; i < len(spans); i++ {
		for j := i; j > 0 && spans[j].start < spans[j-1].start; j-- {
			spans[j], spans[j-1] = spans[j-1], spans[j]
		}
	}
}

// Format helpers shared with the resolver implementation.
func IssueURL(owner, name string, n int64) string {
	return fmt.Sprintf("/%s/%s/issues/%d", owner, name, n)
}

func MRURL(owner, name string, n int64) string {
	return fmt.Sprintf("/%s/%s/mrs/%d", owner, name, n)
}
