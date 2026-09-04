package control

import (
	"fmt"
	"io"
	"slices"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"search"},
		Summary:  "find repositories, issues and merge requests across the instance",
		Usage:    "search <query> [--kind repo|issue|mr]",
		ReadOnly: true, Run: runSearch})
}

// searchLimit caps each kind of result. A query that hits more than this
// wants narrowing, not a longer page.
const searchLimit = 50

// SearchResult is one match, in the shape all three kinds share.
type SearchResult struct {
	Kind      string `json:"kind"` // repo, issue, or mr
	Repo      string `json:"repo"`
	Number    int64  `json:"number,omitempty"`
	Title     string `json:"title"`
	Author    string `json:"author,omitempty"`
	State     string `json:"state,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

// Search runs a query for a user, or for an anonymous visitor when userID
// is 0, in which case only public repositories are reached. kinds names
// which of repo, issue and mr to look at; empty means all three. It is
// exported because the web's /search page renders it for readers who have
// no session to dispatch a command as.
func Search(st *store.Store, root string, userID int64, q string, kinds []string) ([]SearchResult, error) {
	want := func(kind string) bool { return len(kinds) == 0 || slices.Contains(kinds, kind) }
	out := []SearchResult{}
	if want("repo") {
		repos, err := st.VisibleRepos(userID)
		if err != nil {
			return nil, err
		}
		for _, r := range repos {
			desc := gitutil.ReadDescription(RepoDir(root, r.OwnerName, r.Name))
			topics, _ := st.ListTopics(r.ID)
			if !MatchesRepo(q, r.Path(), desc, topics) {
				continue
			}
			out = append(out, SearchResult{Kind: "repo", Repo: r.Path(), Title: desc})
			if len(out) == searchLimit {
				break
			}
		}
	}
	for _, k := range []struct {
		kind  string
		query func(int64, string, int) ([]store.DashboardItem, error)
	}{{"issue", st.SearchIssues}, {"mr", st.SearchMRs}} {
		if !want(k.kind) {
			continue
		}
		items, err := k.query(userID, q, searchLimit)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			out = append(out, SearchResult{Kind: k.kind, Repo: it.RepoPath, Number: it.Number,
				Title: it.Title, Author: it.Author, State: it.State, UpdatedAt: it.UpdatedAt})
		}
	}
	return out, nil
}

func runSearch(c *Ctx, args []string) int {
	const usage = "search <query> [--kind repo|issue|mr]"
	f, err := parseFlags(args, flagSpec{Multi: []string{"--kind"}, MaxPos: 1, Usage: usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if len(f.Pos) != 1 {
		return c.fail(protocol.ExitUsage, "usage: %s", usage)
	}
	if err := validQuery(f.Pos[0]); err != nil {
		return c.failErr(err)
	}
	kinds := f.List("--kind")
	for _, k := range kinds {
		if k != "repo" && k != "issue" && k != "mr" {
			return c.fail(protocol.ExitUsage, "--kind must be repo, issue or mr")
		}
	}
	results, err := Search(c.Store, c.Cfg.Server.Root, c.User.ID, f.Pos[0], kinds)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(results, func(w io.Writer) {
		for _, r := range results {
			switch r.Kind {
			case "repo":
				fmt.Fprintf(w, "repo\t%s\t%s\n", r.Repo, r.Title)
			default:
				fmt.Fprintf(w, "%s\t%s%s%d\t%s\t%s\n", r.Kind, r.Repo,
					SearchMarker(r.Kind), r.Number, r.State, r.Title)
			}
		}
	})
}

// SearchMarker is the sigil a result's number carries, shared with the web
// so a hit reads the same in both places.
func SearchMarker(kind string) string {
	if kind == "mr" {
		return "!"
	}
	return "#"
}
