package httpd

import (
	"path"
	"sort"

	"gitbay.org/gitbay/internal/gitutil"
)

// fileNav is the column beside a file: its directory's entries, the file
// marked, and a link up. It is the tree page's listing rendered as a
// list, so reading a repository does not mean going back for each file.
type fileNav struct {
	Title   string // the directory, or the repository name at the root
	Parent  string // URL of the parent tree; "" at the root
	Entries []fileNavEntry
}

type fileNavEntry struct {
	Name    string // directories carry a trailing slash
	URL     string
	Dir     bool
	Current bool
}

// sortDirsFirst orders a listing by shape before name, stably, so each
// group keeps the order git gave it. The tree page and the navigator
// share it.
func sortDirsFirst(entries []gitutil.TreeEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].Type == "tree" && entries[j].Type != "tree"
	})
}

// navDir is the directory ListTree wants for filePath: "" at the root.
func navDir(filePath string) string {
	if d := path.Dir(filePath); d != "." {
		return d
	}
	return ""
}

// fileNavFor builds the navigator for filePath from its directory's
// entries. repoPath is owner/name.
func fileNavFor(repoPath, ref, filePath string, entries []gitutil.TreeEntry) fileNav {
	dir := path.Dir(filePath)
	if dir == "." {
		dir = ""
	}
	base := "/" + repoPath
	nav := fileNav{Title: dir}
	if dir == "" {
		nav.Title = path.Base(repoPath)
	} else if up := path.Dir(dir); up == "." {
		nav.Parent = base + "/tree/" + ref
	} else {
		nav.Parent = base + "/tree/" + ref + "/" + up
	}
	sortDirsFirst(entries)
	for _, e := range entries {
		full := path.Join(dir, e.Name)
		ent := fileNavEntry{Name: e.Name, Dir: e.Type == "tree", Current: full == filePath}
		if ent.Dir {
			ent.Name += "/"
			ent.URL = base + "/tree/" + ref + "/" + full
		} else {
			ent.URL = base + "/blob/" + ref + "/" + full
		}
		nav.Entries = append(nav.Entries, ent)
	}
	return nav
}
