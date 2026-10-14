package control

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/symbols"
)

func init() {
	register(Command{Path: []string{"repo", "symbols"},
		Summary: "find where a name is defined, from the default branch's symbol index",
		Usage:   "repo symbols <owner/name> [--ref <ref>] [--kind <kind>] [--limit <n>] [--cursor <c>] <query>",
		Flags: []Flag{
			{"--ref", "<ref>", "a ref whose tree is the indexed one; only the default branch is indexed", "the default branch"},
			{"--kind", "<kind>", "only this kind: " + strings.Join(symbols.Kinds, ", "), ""},
			{"--limit", "<n>", "rows per page", ""},
			{"--cursor", "<c>", "continue from the previous page", ""},
		},
		Examples: []string{
			"repo symbols krz/gitbay Dispatch",
			"repo symbols krz/gitbay --kind method Ctx.",
		},
		ReadOnly: true, Run: runRepoSymbols})
	register(Command{Path: []string{"repo", "definitions"},
		Summary: "where each name a file uses is defined, from the symbol index",
		Usage:   "repo definitions <owner/name> <path> [--ref <ref>]",
		Flags: []Flag{
			{"--ref", "<ref>", "a ref whose tree is the indexed one; only the default branch is indexed", "the default branch"},
		},
		Examples: []string{"repo definitions krz/gitbay internal/control/control.go"},
		ReadOnly: true, Run: runRepoDefinitions})
	register(Command{Path: []string{"admin", "symbols", "reindex"},
		Summary:  "rebuild a repository's symbol index, even when its tree is already indexed (instance admins)",
		Usage:    "admin symbols reindex <owner/name>",
		Examples: []string{"admin symbols reindex krz/gitbay"},
		Run:      runAdminSymbolsReindex})
}

// symbolsUnpaged caps a listing given without --limit or --cursor.
const symbolsUnpaged = maxPageLimit

type symbolOut struct {
	Name string `json:"name"`
	Kind string `json:"kind"`
	Path string `json:"path"`
	Line int    `json:"line"`
}

func runRepoSymbols(c *Ctx, args []string) int {
	args, p, code := parsePageFlags(c, args, "symbol", false)
	if code >= 0 {
		return code
	}
	f, err := c.parseArgs(args, flagSpec{Values: []string{"--ref", "--kind"}, MaxPos: 2, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	path, query, ref, kind := f.pos(0), f.pos(1), f.Value("--ref"), f.Value("--kind")
	if path == "" || query == "" {
		return c.usage()
	}
	if len(query) < MinSymbolQuery || len(query) > maxQueryLen {
		return c.fail(protocol.ExitUsage, "query must be %d to %d characters", MinSymbolQuery, maxQueryLen)
	}
	if kind != "" && !symbols.ValidKind(kind) {
		return c.fail(protocol.ExitUsage, "--kind must be one of %s", strings.Join(symbols.Kinds, ", "))
	}
	repo, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
		return code
	}
	idx, err := c.Store.SymbolIndexFor(repo.ID)
	if errors.Is(err, store.ErrNotFound) {
		if f, ferr := c.Store.SymbolFailureFor(repo.ID); ferr == nil {
			return c.fail(protocol.ExitFailure, "the symbol index of %s could not be built: %s", repo.Path(), f.Note)
		}
		return c.fail(protocol.ExitNotFound, "%s has no symbol index yet; one is built after a push to %s", repo.Path(), repo.DefaultBranch)
	} else if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if ref != "" && ref != repo.DefaultBranch {
		dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
		sha, err := gitutil.ResolveRef(dir, ref)
		if err != nil || !IndexedTree(dir, sha, idx) {
			return c.fail(protocol.ExitNotFound, "only the default branch, %s, is indexed; %s is not at the indexed tree", repo.DefaultBranch, ref)
		}
	}
	var after int64
	if p.key != "" {
		cursorIdx, id, ok := ParseSymbolCursor(p.key)
		if !ok {
			return c.fail(protocol.ExitUsage, "bad cursor")
		}
		if cursorIdx != idx.ID {
			return c.fail(protocol.ExitUsage, "%s", StaleSymbolCursor)
		}
		after = id
	}
	limit := p.queryLimit()
	if !p.active {
		limit = symbolsUnpaged + 1
	}
	rows, err := c.Store.SearchSymbols(idx.ID, query, kind, limit, after)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	capped := !p.active && len(rows) > symbolsUnpaged
	if capped {
		rows = rows[:symbolsUnpaged]
	}
	rows, next := trimPage(p, rows, "symbol", func(r store.SymbolRow) string {
		return SymbolCursor(idx.ID, r.ID)
	})
	var ds []symbolOut
	for _, r := range rows {
		ds = append(ds, symbolOut{r.Name, r.Kind, r.Path, r.Line})
	}
	return c.emitPageView(p, ds, next, func(w io.Writer) {
		tb := c.table(w, "NAME", "KIND", "LOCATION")
		for _, d := range ds {
			tb.row(cRef(d.Name), cText(d.Kind), cFlex(fmt.Sprintf("%s:%d", d.Path, d.Line)))
		}
		tb.flush()
		if capped {
			fmt.Fprintf(c.Stderr, "first %d matches; page with --limit and --cursor\n", symbolsUnpaged)
		}
		if idx.State == "partial" {
			fmt.Fprintf(c.Stderr, "the index is partial: %s\n", idx.Note)
		}
	}, func() screen {
		rows := make([]row, len(ds))
		for i, d := range ds {
			rows[i] = rowOf(cRef(d.Name), cText(d.Kind), cFlex(fmt.Sprintf("%s:%d", d.Path, d.Line)))
		}
		if capped {
			fmt.Fprintf(c.Stderr, "first %d matches; page with --limit and --cursor\n", symbolsUnpaged)
		}
		if idx.State == "partial" {
			fmt.Fprintf(c.Stderr, "the index is partial: %s\n", idx.Note)
		}
		s := listScreen(fmt.Sprintf("Symbols matching %q", query), rows)
		if len(ds) > 0 {
			cat := []string{"repo", "cat", repo.Path(), ds[0].Path}
			if ref != "" {
				cat = append(cat, "--ref", ref)
			}
			s.actions = []action{{"Read", cat}}
		}
		return s
	})
}

// MinSymbolQuery is the shortest query repo symbols and the results page
// take: one character matches too much of an index to be worth a page.
const MinSymbolQuery = 2

// StaleSymbolCursor is the refusal for a cursor taken from an index that
// has since been replaced.
const StaleSymbolCursor = "the symbol index was rebuilt since that cursor; start again without it"

// SymbolCursor is the paging key after row id of index indexID, the form
// ParseSymbolCursor reads.
func SymbolCursor(indexID, id int64) string {
	return strconv.FormatInt(indexID, 10) + "." + strconv.FormatInt(id, 10)
}

// ParseSymbolCursor reads "<index id>.<row id>". The index id makes a
// cursor from before a rebuild fail rather than page through the new
// index from an unrelated row.
func ParseSymbolCursor(key string) (int64, int64, bool) {
	a, b, ok := strings.Cut(key, ".")
	if !ok {
		return 0, 0, false
	}
	idx, err1 := strconv.ParseInt(a, 10, 64)
	id, err2 := strconv.ParseInt(b, 10, 64)
	return idx, id, err1 == nil && err2 == nil && id > 0
}

// IndexedTree reports whether commit sha has the tree idx was built from:
// the default branch's head when the index is current, or any other
// commit with the same content. The blob view uses it to decide whether
// its names can link into the index.
func IndexedTree(dir, sha string, idx store.SymbolIndex) bool {
	if sha == idx.Commit {
		return true
	}
	tree, err := gitutil.ResolveTree(dir, sha)
	return err == nil && tree == idx.Tree
}

func runAdminSymbolsReindex(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.usage()
	}
	repo, code := adminRepo(c, args[0])
	if code >= 0 {
		return code
	}
	if err := c.Store.RequestSymbolIndex(repo.ID, true); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"repo": repo.Path(), "state": "queued"}, func(w io.Writer) {
		fmt.Fprintf(w, "queued a rebuild of the symbol index of %s\n", repo.Path())
	})
}

// definitionOut is one name a file uses and where it is defined: the
// first definition, and Count of them. A client links a name defined once
// to its line and one defined more often to repo symbols, as the blob page
// does.
type definitionOut struct {
	Name  string `json:"name"`
	Count int    `json:"count"`
	Path  string `json:"path"`
	Line  int    `json:"line"`
}

// identifier is a name as the index keys it. The blob page takes names
// from the highlighter's spans; a command has no highlighter, so it takes
// every identifier, and keywords fall out because nothing defines them.
var identifier = regexp.MustCompile(`[\p{L}_$][\p{L}\p{N}_$]*`)

// maxDefinitionNames bounds the lookup for one file.
const maxDefinitionNames = 5000

func runRepoDefinitions(c *Ctx, args []string) int {
	f, err := c.parseArgs(args, flagSpec{Values: []string{"--ref"}, MaxPos: 2, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	path, file, ref := f.pos(0), f.pos(1), f.Value("--ref")
	if path == "" || file == "" {
		return c.usage()
	}
	filePath, ok := cleanRepoPath(file)
	if !ok || filePath == "" {
		return c.fail(protocol.ExitUsage, "path must stay inside the repository")
	}
	repo, code := resolveRepo(c, path, policy.CanRead)
	if code >= 0 {
		return code
	}
	idx, err := c.Store.SymbolIndexFor(repo.ID)
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "%s has no symbol index yet; one is built after a push to %s", repo.Path(), repo.DefaultBranch)
	} else if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if ref == "" {
		ref = repo.DefaultBranch
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	sha, err := gitutil.ResolveRef(dir, ref)
	if err != nil || !IndexedTree(dir, sha, idx) {
		return c.fail(protocol.ExitNotFound, "only the default branch, %s, is indexed; %s is not at the indexed tree", repo.DefaultBranch, ref)
	}
	data, err := gitutil.ReadBlob(dir, sha, filePath, c.Cfg.Limits.MaxBlobBytes)
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no such file %q in %s at %s", filePath, repo.Path(), ref)
	}
	var keys []string
	if !gitutil.IsBinary(data) {
		seen := map[string]bool{}
		for _, name := range identifier.FindAllString(string(data), -1) {
			if !seen[name] && len(keys) < maxDefinitionNames {
				seen[name] = true
				keys = append(keys, name)
			}
		}
	}
	targets, err := c.Store.SymbolTargets(idx.ID, keys)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	ds := make([]definitionOut, 0, len(targets))
	for name, t := range targets {
		ds = append(ds, definitionOut{name, t.Count, t.Path, t.Line})
	}
	sort.Slice(ds, func(i, j int) bool { return ds[i].Name < ds[j].Name })
	rowsOf := func() []row {
		rows := make([]row, len(ds))
		for i, d := range ds {
			rows[i] = rowOf(cRef(d.Name), cText(strconv.Itoa(d.Count)), cFlex(fmt.Sprintf("%s:%d", d.Path, d.Line)))
		}
		return rows
	}
	return c.emitView(ds, func(w io.Writer) {
		tb := c.table(w, "NAME", "DEFINED", "FIRST")
		for _, d := range ds {
			tb.row(cRef(d.Name), cText(strconv.Itoa(d.Count)), cFlex(fmt.Sprintf("%s:%d", d.Path, d.Line)))
		}
		tb.flush()
	}, func() screen {
		s := listScreen("Definitions used in "+filePath, rowsOf())
		if len(ds) > 0 {
			s.actions = []action{{"Find", []string{"repo", "symbols", repo.Path(), ds[0].Name}}}
		}
		return s
	})
}
