package control

import (
	"encoding/base64"
	"fmt"
	"io"
	"path"
	"strings"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{
		Path:     []string{"wiki", "list"},
		Summary:  "list a repository's wiki pages",
		Usage:    "wiki list <owner/name>",
		ReadOnly: true,
		Run:      runWikiList,
	})
	register(Command{
		Path:     []string{"wiki", "show"},
		Summary:  "print a wiki page",
		Usage:    "wiki show <owner/name> [<page>]",
		ReadOnly: true,
		Run:      runWikiShow,
	})
}

// Wiki pages are files under .gitbay/wiki on the repository's default
// branch, alongside ci.yml and CODEOWNERS. They have no store row of
// their own — access derives from the parent, exactly as it does for any
// other path in the repository.
//
// Writing is a push, or repo commit-file, like any other file in the
// repository. There is no wiki-specific write command.

// wikiExts are the page formats the web renders, in resolution order.
var wikiExts = []string{".md", ".org", ".markdown"}

// wikiTreePath is where wiki pages live in a repository's tree.
const wikiTreePath = ".gitbay/wiki"

// wikiDir resolves the parent, checks read access, and returns its
// directory and default branch. A parent you cannot read has no wiki you
// can read.
func wikiDir(c *Ctx, spec string) (repo store.Repo, dir, branch string, code int) {
	parent, code := resolveRepo(c, spec, policy.CanRead)
	if code >= 0 {
		return store.Repo{}, "", "", code
	}
	return parent, RepoDir(c.Cfg.Server.Root, parent.OwnerName, parent.Name), parent.DefaultBranch, -1
}

// wikiPages lists the page names under .gitbay/wiki, without extensions.
func wikiPages(dir, branch string) []string {
	entries, err := gitutil.ListTree(dir, branch, wikiTreePath)
	if err != nil {
		return nil // no .gitbay/wiki tree on this branch
	}
	var pages []string
	for _, e := range entries {
		if e.Type != "blob" {
			continue
		}
		ext := strings.ToLower(path.Ext(e.Name))
		for _, want := range wikiExts {
			if ext == want {
				pages = append(pages, strings.TrimSuffix(e.Name, path.Ext(e.Name)))
				break
			}
		}
	}
	return pages
}

func runWikiList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: wiki list <owner/name>")
	}
	repo, dir, branch, code := wikiDir(c, args[0])
	if code >= 0 {
		return code
	}
	pages := wikiPages(dir, branch)
	type out struct {
		Path  string   `json:"path"`
		Home  string   `json:"home,omitempty"`
		Pages []string `json:"pages"`
	}
	d := out{Path: repo.Path(), Home: wikiHome(pages), Pages: pages}
	if d.Pages == nil {
		d.Pages = []string{}
	}
	return c.emit(d, func(w io.Writer) {
		for _, p := range d.Pages {
			fmt.Fprintln(w, p)
		}
	})
}

// wikiHome picks the landing page the way the web does: a conventional
// name if one exists, otherwise the first page.
func wikiHome(pages []string) string {
	for _, home := range []string{"Home", "home", "README", "index"} {
		for _, p := range pages {
			if p == home {
				return home
			}
		}
	}
	if len(pages) > 0 {
		return pages[0]
	}
	return ""
}

func runWikiShow(c *Ctx, args []string) int {
	if len(args) < 1 || len(args) > 2 {
		return c.fail(protocol.ExitUsage, "usage: wiki show <owner/name> [<page>]")
	}
	repo, dir, branch, code := wikiDir(c, args[0])
	if code >= 0 {
		return code
	}
	pages := wikiPages(dir, branch)
	page := wikiHome(pages)
	if len(args) == 2 {
		page = strings.TrimSuffix(args[1], path.Ext(args[1]))
	}
	if page == "" {
		return c.fail(protocol.ExitNotFound, "%s has no wiki pages", repo.Path())
	}
	// A page name is a file name, so it must not climb out of the repo.
	if cleaned, ok := cleanRepoPath(page); !ok || cleaned != page {
		return c.fail(protocol.ExitUsage, "page must be a name inside the wiki")
	}

	for _, ext := range wikiExts {
		raw, err := gitutil.ReadBlob(dir, branch, wikiTreePath+"/"+page+ext, c.Cfg.Limits.MaxBlobBytes)
		if err != nil {
			continue
		}
		binary := gitutil.IsBinary(raw)
		type out struct {
			Path    string `json:"path"`
			Page    string `json:"page"`
			File    string `json:"file"`
			Size    int    `json:"size"`
			Binary  bool   `json:"binary,omitempty"`
			Content string `json:"content,omitempty"`
			Base64  string `json:"base64,omitempty"`
		}
		d := out{Path: repo.Path(), Page: page, File: page + ext,
			Size: len(raw), Binary: binary}
		if binary {
			d.Base64 = base64.StdEncoding.EncodeToString(raw)
		} else {
			d.Content = string(raw)
		}
		return c.emit(d, func(w io.Writer) {
			fmt.Fprint(w, d.Content)
		})
	}
	return c.fail(protocol.ExitNotFound, "no wiki page %q in %s", page, repo.Path())
}
