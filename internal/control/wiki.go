package control

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
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
		Summary:  "list a repository's wiki pages: wiki list <owner/name>",
		ReadOnly: true,
		Run:      runWikiList,
	})
	register(Command{
		Path:     []string{"wiki", "show"},
		Summary:  "print a wiki page: wiki show <owner/name> [<page>]",
		ReadOnly: true,
		Run:      runWikiShow,
	})
}

// A wiki lives in a companion bare repo beside its parent, so prose
// edits stay out of the code repository's history, its protected
// branches and its builds. The companion has no store row of its own —
// access derives from the parent, exactly as it does for git over SSH —
// which is why it needs commands rather than being addressable as a
// repository.
//
// Editing stays a push to <repo>.wiki.git. That is the whole write
// interface, on every surface, and there is nothing for a command to
// add.

// wikiExts are the page formats the web renders, in resolution order.
var wikiExts = []string{".md", ".org", ".markdown"}

// wikiDir resolves the parent, checks read access, and returns the
// companion's path. A parent you cannot read has no wiki you can read.
func wikiDir(c *Ctx, spec string) (repo store.Repo, dir string, code int) {
	parent, code := resolveRepo(c, spec, policy.CanRead)
	if code >= 0 {
		return store.Repo{}, "", code
	}
	d := RepoDir(c.Cfg.Server.Root, parent.OwnerName, parent.Name+".wiki")
	if _, err := os.Stat(d); err != nil {
		return store.Repo{}, "", c.fail(protocol.ExitNotFound, "%s has no wiki", parent.Path())
	}
	return parent, d, -1
}

// wikiPages lists the page names in the companion, without extensions.
func wikiPages(dir string) []string {
	entries, err := gitutil.ListTree(dir, "main", "")
	if err != nil {
		return nil // the companion exists but has no commits yet
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
	repo, dir, code := wikiDir(c, args[0])
	if code >= 0 {
		return code
	}
	pages := wikiPages(dir)
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
	repo, dir, code := wikiDir(c, args[0])
	if code >= 0 {
		return code
	}
	pages := wikiPages(dir)
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
		raw, err := gitutil.ReadBlob(dir, "main", page+ext, c.Cfg.Limits.MaxBlobBytes)
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
