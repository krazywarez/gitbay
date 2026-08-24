package control

import (
	"fmt"
	"io"
	"strings"

	"gitbay.org/gitbay/internal/gitutil"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"status", "set"},
		Summary: "report a commit status (CI): status set <owner/name> <sha> --context <c> --state pending|success|failure|error [--description <d>] [--url <u>]",
		Run:     runStatusSet})
	register(Command{Path: []string{"status", "list"},
		Summary: "statuses on a commit: status list <owner/name> <sha>", ReadOnly: true, Run: runStatusList})
}

var validStatusState = map[string]bool{"pending": true, "success": true, "failure": true, "error": true}

func runStatusSet(c *Ctx, args []string) int {
	var path, sha, context, state, description, url string
	rest := args
	for i := 0; i < len(rest); i++ {
		switch rest[i] {
		case "--context", "--state", "--description", "--url":
			if i+1 >= len(rest) {
				return c.fail(protocol.ExitUsage, "%s requires a value", rest[i])
			}
			v := rest[i+1]
			switch rest[i] {
			case "--context":
				context = v
			case "--state":
				state = v
			case "--description":
				description = v
			case "--url":
				url = v
			}
			i++
		default:
			if path == "" {
				path = rest[i]
			} else if sha == "" {
				sha = rest[i]
			} else {
				return c.fail(protocol.ExitUsage, "unexpected argument %q", rest[i])
			}
		}
	}
	if path == "" || sha == "" || context == "" || !validStatusState[state] {
		return c.fail(protocol.ExitUsage, "usage: status set <owner/name> <sha> --context <c> --state pending|success|failure|error")
	}
	if url != "" && !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return c.fail(protocol.ExitUsage, "--url must be http(s)")
	}
	// Reporting a status is a write: CI identities need write access (an
	// API token with full scope, or an account grant).
	repo, code := resolveRepo(c, path, policy.CanWrite)
	if code >= 0 {
		return code
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	full, err := gitutil.ResolveRef(dir, sha)
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no commit %s in %s", sha, repo.Path())
	}
	if err := c.Store.SetCommitStatus(repo.ID, full, context, state, description, url, c.User.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, "status",
		fmt.Sprintf(`{"sha":%q,"context":%q,"state":%q}`, full, context, state))
	return c.emit(map[string]string{"sha": full, "context": context, "state": state}, func(w io.Writer) {
		fmt.Fprintf(w, "%s on %.10s: %s\n", context, full, state)
	})
}

func runStatusList(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: status list <owner/name> <sha>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	dir := RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name)
	full, err := gitutil.ResolveRef(dir, args[1])
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no commit %s in %s", args[1], repo.Path())
	}
	statuses, err := c.Store.ListCommitStatuses(repo.ID, full)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type out struct {
		Context     string `json:"context"`
		State       string `json:"state"`
		Description string `json:"description,omitempty"`
		URL         string `json:"url,omitempty"`
		Creator     string `json:"creator,omitempty"`
	}
	var ds []out
	for _, s := range statuses {
		ds = append(ds, out{s.Context, s.State, s.Description, s.TargetURL, s.Creator})
	}
	d := struct {
		SHA      string `json:"sha"`
		Combined string `json:"combined"`
		Statuses []out  `json:"statuses"`
	}{full, combinedOf(statuses), ds}
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "%.10s: %s\n", d.SHA, orNone(d.Combined))
		for _, x := range ds {
			extra := ""
			if x.Description != "" {
				extra = "\t" + x.Description
			}
			fmt.Fprintf(w, "  %s\t%s%s\n", x.Context, x.State, extra)
		}
	})
}

func combinedOf(statuses []store.CommitStatus) string { return store.CombinedStatus(statuses) }

func orNone(s string) string {
	if s == "" {
		return "no statuses"
	}
	return s
}
