package httpd

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/symbols"
)

// nameSpan is a name token as chroma writes it with classes: n, nx, nf,
// nc and the rest of the Name family. Keywords, literals and punctuation
// carry other classes and are never linked.
var nameSpan = regexp.MustCompile(`<span class="n[a-z]?">([\p{L}_$][\p{L}\p{N}_$]*)</span>`)

// escapePath escapes each segment of a slash-separated path for a URL.
func escapePath(p string) string {
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// blobSymbols finds the viewed file's symbols and links the names in its
// highlighted source to their definitions, when commit, the ref being
// viewed as the page resolved it, has the indexed tree. A name defined once links to that line at the same
// ref; one defined more than once links to the results page. Names the
// index does not hold stay plain. The whole page costs one lookup of the
// file's distinct names, however many times each appears.
func (s *Server) blobSymbols(p repoPage, commit, filePath string, code template.HTML) (template.HTML, []store.SymbolRow) {
	idx, err := s.st.SymbolIndexFor(p.Repo.ID)
	if err != nil || !control.IndexedTree(p.Dir, commit, idx) {
		return code, nil
	}
	list, _ := s.st.SymbolsInFile(idx.ID, filePath)
	if code == "" {
		return code, list
	}
	seen := map[string]bool{}
	var keys []string
	for _, m := range nameSpan.FindAllStringSubmatch(string(code), -1) {
		if !seen[m[1]] {
			seen[m[1]] = true
			keys = append(keys, m[1])
		}
	}
	if len(keys) == 0 {
		return code, list
	}
	targets, err := s.st.SymbolTargets(idx.ID, keys)
	if err != nil || len(targets) == 0 {
		return code, list
	}
	repoPath := "/" + p.Repo.Path()
	ref := escapePath(p.Ref)
	linked := nameSpan.ReplaceAllStringFunc(string(code), func(span string) string {
		name := nameSpan.FindStringSubmatch(span)[1]
		t, ok := targets[name]
		if !ok {
			return span
		}
		href := repoPath + "/symbols?q=" + url.QueryEscape(name)
		if t.Count == 1 {
			href = repoPath + "/blob/" + ref + "/" + escapePath(t.Path) + "#L" + strconv.Itoa(t.Line)
		}
		return `<a class="sym" href="` + template.HTMLEscapeString(href) + `">` + span + `</a>`
	})
	return template.HTML(linked), list
}

// symbolsPageSize is the results page's page length.
const symbolsPageSize = 100

// symbolsPage lists the definitions matching a query, ranked as `repo
// symbols` ranks them, from the same store query.
func (s *Server) symbolsPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.repoFor(w, r, "")
	if !ok {
		return
	}
	p.Tab = "search"
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	kind := r.URL.Query().Get("kind")
	if !symbols.ValidKind(kind) {
		kind = ""
	}
	var (
		rows    []store.SymbolRow
		next    string
		problem string
		note    string
	)
	idx, err := s.st.SymbolIndexFor(p.Repo.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		problem = "no symbol index yet; one is built after a push to " + p.Repo.DefaultBranch
		if f, ferr := s.st.SymbolFailureFor(p.Repo.ID); ferr == nil {
			problem = "the symbol index could not be built: " + f.Note
		}
	case err != nil:
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	case q == "":
	case len(q) < control.MinSymbolQuery || len(q) > 200:
		problem = fmt.Sprintf("query must be %d to 200 characters", control.MinSymbolQuery)
	default:
		if idx.State == "partial" {
			note = "the index is partial: " + idx.Note
		}
		var after int64
		if a := r.URL.Query().Get("after"); a != "" {
			cursorIdx, id, ok := control.ParseSymbolCursor(a)
			switch {
			case !ok:
				problem = "bad cursor"
			case cursorIdx != idx.ID:
				problem = control.StaleSymbolCursor
			}
			after = id
		}
		if problem != "" {
			break
		}
		rows, err = s.st.SearchSymbols(idx.ID, q, kind, symbolsPageSize+1, after)
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		if len(rows) > symbolsPageSize {
			rows = rows[:symbolsPageSize]
			v := url.Values{"q": {q}, "after": {control.SymbolCursor(idx.ID, rows[len(rows)-1].ID)}}
			if kind != "" {
				v.Set("kind", kind)
			}
			next = "/" + p.Repo.Path() + "/symbols?" + v.Encode()
		}
	}
	s.render(w, "symbols.html", struct {
		repoPage
		Query   string
		Kind    string
		Kinds   []string
		Problem string
		Note    string
		Rows    []store.SymbolRow
		Next    string
		Commit  string
	}{p, q, kind, symbols.Kinds, problem, note, rows, next, idx.Commit})
}
