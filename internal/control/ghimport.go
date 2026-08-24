package control

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/webhook"
)

func init() {
	register(Command{Path: []string{"repo", "import-issues"},
		Summary:    "import GitHub issue and PR history: repo import-issues <owner/name> --from <ghowner/ghrepo> [--token-stdin] [--api-base <url>]",
		ReadsStdin: true, Run: runImportIssues})
}

// GitHub API shapes, minimal.
type ghUser struct {
	Login string `json:"login"`
}
type ghIssue struct {
	Number      int64                   `json:"number"`
	Title       string                  `json:"title"`
	Body        string                  `json:"body"`
	State       string                  `json:"state"`
	CreatedAt   string                  `json:"created_at"`
	User        ghUser                  `json:"user"`
	Labels      []struct{ Name string } `json:"labels"`
	PullRequest *struct{}               `json:"pull_request"`
	Comments    int                     `json:"comments"`
}
type ghPull struct {
	MergedAt string `json:"merged_at"`
	Head     struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"head"`
	Base struct {
		SHA string `json:"sha"`
		Ref string `json:"ref"`
	} `json:"base"`
}
type ghComment struct {
	ID        int64  `json:"id"`
	Body      string `json:"body"`
	CreatedAt string `json:"created_at"`
	User      ghUser `json:"user"`
}

type ghClient struct {
	base  string
	token string
	http  *http.Client
}

func (g *ghClient) get(path string, out any) error {
	req, err := http.NewRequest("GET", g.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if g.token != "" {
		req.Header.Set("Authorization", "Bearer "+g.token)
	}
	resp, err := g.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return fmt.Errorf("GitHub API %s: %s: %.200s", path, resp.Status, body)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func ghDate(iso string) string {
	if t, err := time.Parse(time.RFC3339, iso); err == nil {
		return t.UTC().Format("2006-01-02")
	}
	return iso
}

// attribution heads every imported body: foreign authors have no local
// account, so the original author and date live in the text.
func attribution(src string, n int64, kind, login, date string) string {
	return fmt.Sprintf("> imported %s %s#%d — @%s, %s\n\n", kind, src, n, login, ghDate(date))
}

func runImportIssues(c *Ctx, args []string) int {
	var path, from, apiBase string
	tokenStdin := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--from", "--api-base":
			if i+1 >= len(args) {
				return c.fail(protocol.ExitUsage, "%s requires a value", args[i])
			}
			if args[i] == "--from" {
				from = args[i+1]
			} else {
				apiBase = args[i+1]
			}
			i++
		case "--token-stdin":
			tokenStdin = true
		default:
			if path != "" {
				return c.fail(protocol.ExitUsage, "unexpected argument %q", args[i])
			}
			path = args[i]
		}
	}
	if path == "" || from == "" {
		return c.fail(protocol.ExitUsage, "usage: repo import-issues <owner/name> --from <ghowner/ghrepo> [--token-stdin]")
	}
	// Accept a bare owner/repo or a full github.com URL.
	from = strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(from, "https://"), "github.com/"), ".git")
	if parts := strings.Split(from, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return c.fail(protocol.ExitUsage, "--from must be <ghowner>/<ghrepo> (or the github.com URL)")
	}
	if apiBase == "" {
		apiBase = "https://api.github.com"
	} else if err := webhook.ValidateURL(apiBase, c.Cfg.Webhooks.AllowLocal); err != nil {
		// A writer-supplied API base is the same SSRF surface as a
		// webhook target; same rules apply.
		return c.fail(protocol.ExitUsage, "--api-base: %v", err)
	}
	repo, code := resolveRepo(c, path, policy.CanWrite)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	token := ""
	if tokenStdin {
		// Same discipline as repo import: token on stdin, never argv,
		// never stored.
		line, err := bufio.NewReader(c.Stdin).ReadString('\n')
		if err != nil && line == "" {
			return c.fail(protocol.ExitUsage, "--token-stdin: no token on stdin")
		}
		token = strings.TrimSpace(line)
	}
	g := &ghClient{base: apiBase, token: token, http: &http.Client{Timeout: 30 * time.Second}}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	src := "github.com/" + from

	var issues, mrs, comments, skipped int
	for page := 1; ; page++ {
		var items []ghIssue
		q := fmt.Sprintf("/repos/%s/issues?state=all&sort=created&direction=asc&per_page=100&page=%d", from, page)
		if err := g.get(q, &items); err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if len(items) == 0 {
			break
		}
		for _, it := range items {
			key := fmt.Sprintf("gh:%d", it.Number)
			val, seen, err := c.Store.ImportMarker(repo.ID, key)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			var localN int64
			isPR := it.PullRequest != nil
			if seen {
				localN, _ = strconv.ParseInt(strings.TrimPrefix(strings.TrimPrefix(val, "issue:"), "mr:"), 10, 64)
				skipped++
			} else if isPR {
				var pr ghPull
				if err := g.get(fmt.Sprintf("/repos/%s/pulls/%d", from, it.Number), &pr); err != nil {
					return c.fail(protocol.ExitFailure, "%v", err)
				}
				body := attribution(src, it.Number, "pull request", it.User.Login, it.CreatedAt) + it.Body
				localN, err = c.Store.CreateMR(repo.ID, c.User.ID, repo.ID, pr.Head.Ref, pr.Base.Ref, it.Title, body, pr.Head.SHA)
				if err != nil {
					return c.fail(protocol.ExitFailure, "%v", err)
				}
				mr, err := c.Store.MRByNumber(repo.ID, localN)
				if err != nil {
					return c.fail(protocol.ExitFailure, "%v", err)
				}
				if pr.MergedAt != "" {
					c.Store.MarkMerged(mr.ID, pr.Base.SHA)
				} else {
					c.Store.SetMRState(mr.ID, "closed")
				}
				// Point the MR head ref at the PR head when the mirror
				// already holds the objects (refs/pull backups).
				if pr.Head.SHA != "" && gitutil.HasCommit(dir, pr.Head.SHA) {
					gitutil.UpdateRefCAS(dir, fmt.Sprintf("refs/merge-requests/%d/head", localN), pr.Head.SHA, "")
				}
				c.Store.SetImportMarker(repo.ID, key, fmt.Sprintf("mr:%d", localN))
				mrs++
			} else {
				body := attribution(src, it.Number, "issue", it.User.Login, it.CreatedAt) + it.Body
				localN, err = c.Store.CreateIssue(repo.ID, c.User.ID, it.Title, body)
				if err != nil {
					return c.fail(protocol.ExitFailure, "%v", err)
				}
				iss, err := c.Store.IssueByNumber(repo.ID, localN)
				if err != nil {
					return c.fail(protocol.ExitFailure, "%v", err)
				}
				for _, l := range it.Labels {
					c.Store.SetIssueLabel(repo.ID, iss.ID, l.Name, true)
				}
				if it.State != "open" {
					c.Store.SetIssueState(iss.ID, "closed")
				}
				c.Store.SetImportMarker(repo.ID, key, fmt.Sprintf("issue:%d", localN))
				issues++
			}
			if it.Comments > 0 && localN > 0 {
				n, err := importComments(c, g, repo, from, src, it.Number, localN, isPR)
				if err != nil {
					return c.fail(protocol.ExitFailure, "%v", err)
				}
				comments += n
			}
			fmt.Fprintf(c.Stderr, "%s#%d -> %s%d\n", src, it.Number, map[bool]string{true: "!", false: "#"}[isPR], localN)
		}
	}
	d := map[string]any{"issues": issues, "mrs": mrs, "comments": comments, "already_imported": skipped}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "imported %d issues, %d merge requests, %d comments (%d items already imported)\n",
			issues, mrs, comments, skipped)
	})
}

func importComments(c *Ctx, g *ghClient, repo store.Repo, from, src string, ghN, localN int64, isPR bool) (int, error) {
	var localIssueID, localMRID int64
	if isPR {
		mr, err := c.Store.MRByNumber(repo.ID, localN)
		if err != nil {
			return 0, err
		}
		localMRID = mr.ID
	} else {
		iss, err := c.Store.IssueByNumber(repo.ID, localN)
		if err != nil {
			return 0, err
		}
		localIssueID = iss.ID
	}
	imported := 0
	for page := 1; ; page++ {
		var cs []ghComment
		q := fmt.Sprintf("/repos/%s/issues/%d/comments?per_page=100&page=%d", url.PathEscape(from), ghN, page)
		q = strings.ReplaceAll(q, "%2F", "/")
		if err := g.get(q, &cs); err != nil {
			return imported, err
		}
		if len(cs) == 0 {
			return imported, nil
		}
		for _, cm := range cs {
			key := fmt.Sprintf("ghc:%d", cm.ID)
			if _, seen, err := c.Store.ImportMarker(repo.ID, key); err != nil {
				return imported, err
			} else if seen {
				continue
			}
			body := fmt.Sprintf("> @%s, %s\n\n%s", cm.User.Login, ghDate(cm.CreatedAt), cm.Body)
			var err error
			if isPR {
				err = c.Store.AddMRComment(localMRID, c.User.ID, body)
			} else {
				err = c.Store.AddIssueComment(localIssueID, c.User.ID, body)
			}
			if err != nil {
				return imported, err
			}
			c.Store.SetImportMarker(repo.ID, key, "")
			imported++
		}
	}
}
