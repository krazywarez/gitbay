package control

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
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
		Summary:    "import issue and PR history from GitHub or Forgejo",
		Usage:      "repo import-issues <owner/name> --from <owner/repo> [--token-stdin] [--api-base <url>]",
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
	ClosedAt    string                  `json:"closed_at"`
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
	// forgejo is set when the API base answers /version, which GitHub
	// does not. Forgejo (and Gitea, and so Codeberg) mirror GitHub's
	// issue, pull and comment shapes but not its query parameters:
	// pages are sized by `limit`, order is `sort=oldest`, and the
	// comments endpoint has no pages at all — it ignores `page` and
	// returns everything every time, which paged the old loop forever.
	forgejo bool
}

func (g *ghClient) detect() {
	var v struct{ Version string }
	g.forgejo = g.get("/version", &v) == nil && v.Version != ""
}

// issuesQuery lists every issue and pull request oldest first, so local
// numbers come out in the source's order.
func (g *ghClient) issuesQuery(from string, page int) string {
	if g.forgejo {
		return fmt.Sprintf("/repos/%s/issues?state=all&sort=oldest&limit=50&page=%d", from, page)
	}
	return fmt.Sprintf("/repos/%s/issues?state=all&sort=created&direction=asc&per_page=100&page=%d", from, page)
}

// siteFromAPI turns an API base into the site that serves git and the
// name attribution carries: api.github.com is github.com, a GitHub
// Enterprise or Forgejo base drops its /api/vN suffix.
func siteFromAPI(apiBase string) string {
	u, err := url.Parse(apiBase)
	if err != nil {
		return apiBase
	}
	if u.Host == "api.github.com" {
		u.Host = "github.com"
		u.Path = ""
	}
	u.Path = strings.TrimSuffix(strings.TrimSuffix(u.Path, "/"), "/api/v1")
	u.Path = strings.TrimSuffix(u.Path, "/api/v3")
	u.RawQuery, u.Fragment = "", ""
	return u.String()
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
	f, err := parseFlags(args, flagSpec{Values: []string{"--from", "--api-base"}, Bools: []string{"--token-stdin"}, MaxPos: 1,
		Usage: "repo import-issues <owner/name> --from <owner/repo> [--api-base <url>] [--token-stdin]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	path, from, apiBase, tokenStdin := f.pos(0), f.Value("--from"), f.Value("--api-base"), f.Has("--token-stdin")
	if path == "" || from == "" {
		return c.fail(protocol.ExitUsage, "usage: repo import-issues <owner/name> --from <owner/repo> [--token-stdin] [--api-base <url>]")
	}
	if apiBase == "" {
		apiBase = "https://api.github.com"
	} else if err := webhook.ValidateURL(apiBase, c.Cfg.Webhooks.AllowLocal); err != nil {
		// A writer-supplied API base is the same SSRF surface as a
		// webhook target; same rules apply.
		return c.fail(protocol.ExitUsage, "--api-base: %v", err)
	}
	site := siteFromAPI(apiBase)
	host := strings.TrimPrefix(strings.TrimPrefix(site, "https://"), "http://")
	// Accept a bare owner/repo or the repository's URL on that site.
	from = strings.TrimPrefix(from, site+"/")
	from = strings.TrimPrefix(from, host+"/")
	from = strings.TrimSuffix(from, ".git")
	if parts := strings.Split(from, "/"); len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return c.fail(protocol.ExitUsage, "--from must be <owner>/<repo> (or the repository URL on %s)", site)
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
	g.detect()
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)

	// Pull heads first, so every merge request below has objects to point
	// at. A mirror made with the default refspecs does not carry
	// refs/pull/*, which is why imported pull requests used to have no
	// head and `mr diff` could only fail on them (#128). Best-effort: an
	// import of issues from a repository whose git data is not here yet
	// is a legitimate thing to do, and the merge requests still arrive
	// with their head SHA recorded.
	fetchedPullHeads := fetchPullHeads(c, dir, site+"/"+from+".git", token)
	src := host + "/" + from

	var issues, mrs, comments, skipped, headed int
	for page := 1; ; page++ {
		var items []ghIssue
		if err := g.get(g.issuesQuery(from, page), &items); err != nil {
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
				localN, err = c.Store.CreateMR(repo.ID, c.User.ID, repo.ID, pr.Head.Ref, pr.Base.Ref, it.Title, body, pr.Head.SHA, "md", false)
				if err != nil {
					return c.fail(protocol.ExitFailure, "%v", err)
				}
				mr, err := c.Store.MRByNumber(repo.ID, localN)
				if err != nil {
					return c.fail(protocol.ExitFailure, "%v", err)
				}
				if pr.MergedAt != "" {
					c.Store.MarkMerged(mr.ID, pr.Base.SHA, 0, pr.MergedAt)
				} else {
					c.Store.MarkClosed(mr.ID, 0, it.ClosedAt)
				}
				// Point the MR head ref at the PR head, from the fetch
				// above or from objects a mirror already had.
				if pr.Head.SHA != "" && gitutil.HasCommit(dir, pr.Head.SHA) {
					gitutil.UpdateRefCAS(dir, fmt.Sprintf("refs/merge-requests/%d/head", localN), pr.Head.SHA, "")
					headed++
				}
				c.Store.SetImportMarker(repo.ID, key, fmt.Sprintf("mr:%d", localN))
				mrs++
			} else {
				body := attribution(src, it.Number, "issue", it.User.Login, it.CreatedAt) + it.Body
				localN, err = c.Store.CreateIssue(repo.ID, c.User.ID, it.Title, body, "md")
				if err != nil {
					return c.fail(protocol.ExitFailure, "%v", err)
				}
				iss, err := c.Store.IssueByNumber(repo.ID, localN)
				if err != nil {
					return c.fail(protocol.ExitFailure, "%v", err)
				}
				for _, l := range it.Labels {
					c.Store.SetIssueLabel(repo, iss.ID, l.Name, true)
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
	d := map[string]any{"issues": issues, "mrs": mrs, "comments": comments,
		"already_imported": skipped, "mrs_with_head": headed, "pull_heads_fetched": fetchedPullHeads}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "imported %d issues, %d merge requests, %d comments (%d items already imported)\n",
			issues, mrs, comments, skipped)
		if mrs > headed {
			fmt.Fprintf(w, "%d merge request(s) have no head objects; `mr diff` cannot render them.\n", mrs-headed)
			if !fetchedPullHeads {
				fmt.Fprintln(w, "refs/pull/* could not be fetched — re-run with --token-stdin if the repository is private.")
			}
		}
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
		// Forgejo returns every comment in one unpaged reply.
		if g.forgejo && page > 1 {
			return imported, nil
		}
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
				err = c.Store.AddMRComment(localMRID, c.User.ID, body, "md")
			} else {
				err = c.Store.AddIssueComment(localIssueID, c.User.ID, body, "md")
			}
			if err != nil {
				return imported, err
			}
			c.Store.SetImportMarker(repo.ID, key, "")
			imported++
		}
	}
}

// ghAskpass answers git's credential prompts from the environment, so a
// token never appears in argv where /proc would expose it. Same shape the
// mirror worker uses.
const ghAskpass = `#!/bin/sh
case "$1" in
  Username*) echo "x-access-token" ;;
  *)         echo "${GITBAY_GH_TOKEN}" ;;
esac
`

// fetchPullHeads brings refs/pull/*/head into refs/gh-pull/*; GitHub and
// Forgejo both publish pull heads under that name. Reports whether it
// worked; a failure is not fatal, since importing issues from a
// repository whose git data is not here yet is a reasonable thing to do.
func fetchPullHeads(c *Ctx, dir, remote, token string) bool {
	env := []string{"GIT_TERMINAL_PROMPT=0", "HOME=" + c.Cfg.Server.Root}
	if token != "" {
		askpass := filepath.Join(c.Cfg.Server.Root, "gh-import-askpass.sh")
		if err := os.WriteFile(askpass, []byte(ghAskpass), 0o700); err != nil {
			return false
		}
		env = append(env, "GIT_ASKPASS="+askpass, "GITBAY_GH_TOKEN="+token)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if err := gitutil.FetchPullHeads(ctx, dir, remote, io.Discard, env); err != nil {
		return false
	}
	return true
}
