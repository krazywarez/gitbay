package deps

import (
	"context"
	"go/parser"
	"go/token"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/store"
)

// maxGoFiles bounds the Go files read for their imports. A repository with
// more is not judged: its Go advisories are all reported.
const maxGoFiles = 5000

// GoImports returns the packages the Go files at sha import, tests
// included, outside vendor and testdata. ok is false when they could not
// all be read.
func GoImports(ctx context.Context, dir, sha string) (map[string]bool, bool) {
	entries, err := gitutil.ListBlobs(ctx, dir, sha)
	if err != nil {
		return nil, false
	}
	var shas []string
	for _, e := range entries {
		if !strings.HasSuffix(e.Name, ".go") || underAny(e.Name, "vendor", "testdata") {
			continue
		}
		shas = append(shas, e.SHA)
	}
	if len(shas) > maxGoFiles {
		return nil, false
	}
	out := map[string]bool{}
	fset := token.NewFileSet()
	err = gitutil.CatBlobs(ctx, dir, shas, func(_ int, data []byte) bool {
		f, err := parser.ParseFile(fset, "", data, parser.ImportsOnly)
		if err != nil {
			return true // a file that does not parse does not build either
		}
		for _, imp := range f.Imports {
			if p, err := strconv.Unquote(imp.Path.Value); err == nil {
				out[p] = true
			}
		}
		return true
	})
	if err != nil {
		return nil, false
	}
	return out, true
}

func underAny(path string, dirs ...string) bool {
	for _, part := range strings.Split(path, "/") {
		for _, d := range dirs {
			if part == d {
				return true
			}
		}
	}
	return false
}

// MarkNotImported sets NotImported on each Go advisory that names packages
// none of which imported holds.
func MarkNotImported(matched []store.DepAdvisory, imported map[string]bool) {
	for i, a := range matched {
		if a.Ecosystem != EcoGo || len(a.Imports) == 0 {
			continue
		}
		used := false
		for _, p := range a.Imports {
			if imported[p] {
				used = true
				break
			}
		}
		matched[i].NotImported = !used
	}
}
