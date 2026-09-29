package control

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"query", "save"},
		Summary: "save an issue and merge request query across repositories under a name",
		Usage:   "query save <name> <query>... [--force]",
		Flags: []Flag{
			{"--force", "", "replace a query of the same name", ""},
		},
		Examples: []string{
			"query save mine is:open assignee:@me",
			`query save triage "repo:krz/*" is:issue is:open no:label`,
			`query save v2 owner:krz is:open label:bug label:"needs review" milestone:v2`,
			"query save mine-merged is:merged author:@me --force",
		},
		Run: runQuerySave})
	register(Command{Path: []string{"query", "list"},
		Summary:  "list your saved queries",
		Usage:    "query list",
		Examples: []string{"query list"},
		ReadOnly: true, Run: runQueryList})
	register(Command{Path: []string{"query", "show"},
		Summary:  "show a saved query and how many rows it matches",
		Usage:    "query show <name>",
		Examples: []string{"query show mine"},
		ReadOnly: true, Run: runQueryShow})
	register(Command{Path: []string{"query", "run"},
		Summary: "list the issues and merge requests a saved query matches",
		Usage:   "query run <name> [--limit <n>] [--cursor <c>]",
		Flags: []Flag{
			{"--limit", "<n>", "rows per page", strconv.Itoa(queryDefaultLimit)},
			{"--cursor", "<c>", "continue from the previous page", ""},
		},
		Examples: []string{"query run mine --limit 20"},
		ReadOnly: true, Run: runQueryRun})
	register(Command{Path: []string{"query", "remove"},
		Summary:  "delete a saved query",
		Usage:    "query remove <name>",
		Examples: []string{"query remove mine"},
		Run:      runQueryRemove})
	register(Command{Path: []string{"query", "pin"},
		Summary:  "show a saved query on your dashboard",
		Usage:    "query pin <name>",
		Examples: []string{"query pin mine"},
		Run:      func(c *Ctx, args []string) int { return runQueryPin(c, args, true) }})
	register(Command{Path: []string{"query", "unpin"},
		Summary:  "take a saved query off your dashboard",
		Usage:    "query unpin <name>",
		Examples: []string{"query unpin mine"},
		Run:      func(c *Ctx, args []string) int { return runQueryPin(c, args, false) }})
}

// queryDefaultLimit is a page when --limit is not given. A query spans
// every repository the caller reads, so its listing is always paged.
const queryDefaultLimit = 50

// dashboardQueryItems is how many rows of each pinned query the
// dashboard carries.
const dashboardQueryItems = 5

// Every pinned query is run on each dashboard read, so an account keeps
// a bounded number of each.
const (
	maxSavedQueries  = 50
	maxPinnedQueries = 10
)

var queryNamePat = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// SavedQueryOut is one saved query. Count is filled by query show.
type SavedQueryOut struct {
	Name   string `json:"name"`
	Query  string `json:"query"`
	Pinned bool   `json:"pinned"`
	Count  *int   `json:"count,omitempty"`
}

// QueryItem is one row of a cross-repository listing, naming its
// repository.
type QueryItem struct {
	Kind      string `json:"kind"` // issue or mr
	Repo      string `json:"repo"`
	Number    int64  `json:"number"`
	Title     string `json:"title"`
	State     string `json:"state"`
	Draft     bool   `json:"draft,omitempty"`
	Author    string `json:"author"`
	Milestone string `json:"milestone,omitempty"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Ref is the item as a person writes it: owner/name#n or owner/name!n.
func (it QueryItem) Ref() string {
	return fmt.Sprintf("%s%s%d", it.Repo, SearchMarker(it.Kind), it.Number)
}

// DashboardQuery is a pinned query on the dashboard: its first rows and
// how many it matches in all. Error is set, and the rest empty, when the
// saved text no longer parses.
type DashboardQuery struct {
	Name  string      `json:"name"`
	Query string      `json:"query"`
	Count int         `json:"count"`
	Items []QueryItem `json:"items"`
	Error string      `json:"error,omitempty"`
}

func queryItems(items []store.Item) []QueryItem {
	out := make([]QueryItem, 0, len(items))
	for _, it := range items {
		out = append(out, QueryItem{Kind: it.Kind, Repo: it.RepoPath, Number: it.Number, Title: it.Title,
			State: it.State, Draft: it.Draft, Author: it.Author, Milestone: it.Milestone,
			CreatedAt: it.CreatedAt, UpdatedAt: it.UpdatedAt})
	}
	return out
}

// PinnedQueries runs each of user's pinned queries for the dashboard. It
// is exported for the web dashboard, which reads the store directly.
func PinnedQueries(st *store.Store, user store.User) ([]DashboardQuery, error) {
	saved, err := st.SavedQueries(user.ID, true)
	if err != nil {
		return nil, err
	}
	out := []DashboardQuery{}
	for _, sq := range saved {
		d := DashboardQuery{Name: sq.Name, Query: sq.Query, Items: []QueryItem{}}
		q, err := ParseItemQuery(sq.Query)
		if err != nil {
			d.Error = err.Error()
			out = append(out, d)
			continue
		}
		f := q.Filter(user.Username, true, true)
		if d.Count, err = st.CountItems(user.ID, f); err != nil {
			return nil, err
		}
		items, err := st.QueryItems(user.ID, f, nil, dashboardQueryItems)
		if err != nil {
			return nil, err
		}
		d.Items = queryItems(items)
		out = append(out, d)
	}
	return out, nil
}

func encodeItemCursor(k store.ItemCursor) string {
	return encodeCursor("query", fmt.Sprintf("%s|%d|%d", k.CreatedAt, k.Kind, k.ID))
}

func decodeItemCursor(key string) (*store.ItemCursor, error) {
	parts := strings.Split(key, "|")
	if len(parts) != 3 {
		return nil, errors.New("bad cursor")
	}
	kind, err1 := strconv.Atoi(parts[1])
	id, err2 := strconv.ParseInt(parts[2], 10, 64)
	if err1 != nil || err2 != nil || (kind != 0 && kind != 1) || parts[0] == "" {
		return nil, errors.New("bad cursor")
	}
	return &store.ItemCursor{CreatedAt: parts[0], Kind: kind, ID: id}, nil
}

// savedQuery loads one of the caller's queries and parses it.
func savedQuery(c *Ctx, name string) (store.SavedQuery, ItemQuery, int) {
	sq, err := c.Store.SavedQueryByName(c.User.ID, name)
	if errors.Is(err, store.ErrNotFound) {
		return sq, ItemQuery{}, c.fail(protocol.ExitNotFound, "no saved query %q; query list shows yours", name)
	}
	if err != nil {
		return sq, ItemQuery{}, c.fail(protocol.ExitFailure, "%v", err)
	}
	q, err := ParseItemQuery(sq.Query)
	if err != nil {
		return sq, q, c.fail(protocol.ExitFailure, "saved query %s no longer parses (%v); save it again with --force", name, err)
	}
	return sq, q, -1
}

// runItemQuery lists what q matches in the tables issues and mrs allow,
// one page at a time. The output is always the paged shape.
func runItemQuery(c *Ctx, q ItemQuery, issues, mrs bool, p page) int {
	var after *store.ItemCursor
	if p.key != "" {
		var err error
		if after, err = decodeItemCursor(p.key); err != nil {
			return c.fail(protocol.ExitUsage, "bad cursor")
		}
	}
	if p.limit == 0 {
		p.limit = queryDefaultLimit
	}
	p.active = true
	items, err := c.Store.QueryItems(c.User.ID, q.Filter(c.User.Username, issues, mrs), after, p.queryLimit())
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	next := ""
	if len(items) > p.limit {
		items = items[:p.limit]
		next = encodeItemCursor(items[len(items)-1].Cursor())
	}
	ds := queryItems(items)
	return c.emitPage(p, ds, next, func(w io.Writer) {
		tb := c.table(w, "REF", "STATE", "TITLE", "AUTHOR")
		for _, d := range ds {
			state := d.State
			if d.Draft {
				state = "draft"
			}
			tb.row(cRef(d.Ref()), cState(state), cFlex(d.Title), cText(d.Author))
		}
		tb.flush()
	})
}

// usesQuery reports whether issue list or mr list was given a query,
// which changes the cursor they page with.
func usesQuery(args []string) bool {
	for _, a := range args {
		if a == "--query" || a == "--q" {
			return true
		}
	}
	return false
}

// listByQuery is issue list and mr list given --query or --q: the query
// spans repositories, so it takes none as an argument and no other
// filter flag. kind is the command's noun.
func listByQuery(c *Ctx, fl flags, kind string, p page) int {
	if fl.Has("--query") && fl.Has("--q") {
		return c.usageWith("--query and --q are two ways to give one query; pass one")
	}
	if len(fl.Pos) > 0 {
		return c.usageWith("a query spans repositories; name them in it (repo:" + fl.Pos[0] + ") rather than as an argument")
	}
	for _, f := range []string{"--state", "--label", "--assignee", "--author", "--milestone", "--search"} {
		if fl.Has(f) {
			return c.usageWith(f + " does not combine with a query; put it in the query")
		}
	}
	var q ItemQuery
	if fl.Has("--query") {
		var code int
		if _, q, code = savedQuery(c, fl.Value("--query")); code >= 0 {
			return code
		}
	} else {
		var err error
		if q, err = ParseItemQuery(fl.Value("--q")); err != nil {
			return c.fail(protocol.ExitUsage, "%v", err)
		}
	}
	issues, mrs := q.Selects()
	if kind == "issue" && !issues {
		return c.usageWith("that query matches only merge requests; use mr list or query run")
	}
	if kind == "mr" && !mrs {
		return c.usageWith("that query matches only issues; use issue list or query run")
	}
	return runItemQuery(c, q, kind == "issue", kind == "mr", p)
}

func savedQueryOut(sq store.SavedQuery) SavedQueryOut {
	return SavedQueryOut{Name: sq.Name, Query: sq.Query, Pinned: sq.Pinned}
}

func runQuerySave(c *Ctx, args []string) int {
	fl, err := c.parseArgs(args, flagSpec{Bools: []string{"--force"}, MaxPos: -1, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if len(fl.Pos) < 2 {
		return c.usage()
	}
	name := fl.Pos[0]
	if !queryNamePat.MatchString(name) {
		return c.fail(protocol.ExitUsage, "invalid query name %q: lowercase letters, digits, '.', '-', '_'; must start with a letter or digit; max 64 chars", name)
	}
	q, err := ParseItemQuery(fl.Pos[1:]...)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if _, err := c.Store.SavedQueryByName(c.User.ID, name); errors.Is(err, store.ErrNotFound) {
		saved, _, err := c.Store.CountSavedQueries(c.User.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if saved >= maxSavedQueries {
			return c.fail(protocol.ExitUsage, "saved query limit reached (%d); remove one first", maxSavedQueries)
		}
	} else if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	err = c.Store.SaveQuery(c.User.ID, name, q.String(), fl.Has("--force"))
	if errors.Is(err, store.ErrExists) {
		return c.fail(protocol.ExitFailure, "you already have a query named %s; pass --force to replace it", name)
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	sq, err := c.Store.SavedQueryByName(c.User.ID, name)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(savedQueryOut(sq), func(w io.Writer) {
		fmt.Fprintf(w, "saved %s: %s\n", sq.Name, sq.Query)
	})
}

func runQueryList(c *Ctx, args []string) int {
	if len(args) != 0 {
		return c.usage()
	}
	saved, err := c.Store.SavedQueries(c.User.ID, false)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	ds := []SavedQueryOut{}
	for _, sq := range saved {
		ds = append(ds, savedQueryOut(sq))
	}
	return c.emit(ds, func(w io.Writer) {
		tb := c.table(w, "NAME", "PINNED", "QUERY")
		for _, d := range ds {
			pinned := ""
			if d.Pinned {
				pinned = "pinned"
			}
			tb.row(cRef(d.Name), cText(pinned), cFlex(d.Query))
		}
		tb.flush()
	})
}

func runQueryShow(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.usage()
	}
	sq, q, code := savedQuery(c, args[0])
	if code >= 0 {
		return code
	}
	n, err := c.Store.CountItems(c.User.ID, q.Filter(c.User.Username, true, true))
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	d := savedQueryOut(sq)
	d.Count = &n
	return c.emit(d, func(w io.Writer) {
		v := c.view(w)
		v.title(d.Name, "", "")
		pinned := "no"
		if d.Pinned {
			pinned = "yes"
		}
		v.fields("query", d.Query, "matches", strconv.Itoa(n), "pinned", pinned)
	})
}

func runQueryRun(c *Ctx, args []string) int {
	rest, p, code := parsePageFlags(c, args, "query", false)
	if code >= 0 {
		return code
	}
	if len(rest) != 1 {
		return c.usage()
	}
	_, q, code := savedQuery(c, rest[0])
	if code >= 0 {
		return code
	}
	return runItemQuery(c, q, true, true, p)
}

func runQueryRemove(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.usage()
	}
	err := c.Store.RemoveSavedQuery(c.User.ID, args[0])
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "no saved query %q; query list shows yours", args[0])
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"removed": args[0]}, func(w io.Writer) {
		fmt.Fprintf(w, "removed %s\n", args[0])
	})
}

func runQueryPin(c *Ctx, args []string, pinned bool) int {
	if len(args) != 1 {
		return c.usage()
	}
	sq, err := c.Store.SavedQueryByName(c.User.ID, args[0])
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "no saved query %q; query list shows yours", args[0])
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if pinned && !sq.Pinned {
		_, n, err := c.Store.CountSavedQueries(c.User.ID)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if n >= maxPinnedQueries {
			return c.fail(protocol.ExitUsage, "pinned query limit reached (%d); unpin one first", maxPinnedQueries)
		}
	}
	err = c.Store.PinSavedQuery(c.User.ID, args[0], pinned)
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitNotFound, "no saved query %q; query list shows yours", args[0])
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if sq, err = c.Store.SavedQueryByName(c.User.ID, args[0]); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(savedQueryOut(sq), func(w io.Writer) {
		verb := "unpinned"
		if pinned {
			verb = "pinned"
		}
		fmt.Fprintf(w, "%s %s\n", verb, sq.Name)
	})
}
