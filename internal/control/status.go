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
		Summary: "report a commit status (CI)",
		Usage:   "status set <owner/name> <sha> --context <c> --state pending|success|failure|error [--description <d>] [--url <u>]",
		Flags: []Flag{
			{"--context", "<c>", "the check this status reports for; ci/ is reserved for the instance's builds", ""},
			{"--state", "pending|success|failure|error", "the check's outcome", ""},
			{"--description", "<d>", "short text shown beside the state", ""},
			{"--url", "<u>", "link to the check's own output", ""},
		},
		Examples: []string{
			"status set krz/gitbay a1b2c3d --context ext/lint --state success",
		},
		Run: runStatusSet})
	register(Command{Path: []string{"status", "list"},
		Summary:  "statuses on a commit",
		Usage:    "status list <owner/name> <sha>",
		Examples: []string{"status list krz/gitbay a1b2c3d"},
		ReadOnly: true, Run: runStatusList})
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
		return c.usage()
	}
	// ci/<job> statuses are the build subsystem's: queued, reused,
	// skipped and finished by the server itself. A writer who could post
	// one could mark ci/test green on their own head before, or instead
	// of, the build (#258). Case-folded, so CI/test is no way around it.
	if strings.HasPrefix(strings.ToLower(context), "ci/") {
		return c.fail(protocol.ExitDenied, "the ci/ prefix is reserved for the instance's builds; report under another name, such as ext/%s",
			strings.TrimPrefix(strings.ToLower(context), "ci/"))
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
	if code := refuseArchived(c, repo); code >= 0 {
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
	TryQueuedMergesAt(c.Store, c.Cfg, repo.ID, full)
	return c.emit(map[string]string{"sha": full, "context": context, "state": state}, func(w io.Writer) {
		fmt.Fprintf(w, "%s on %.10s: %s\n", context, full, state)
	})
}

func runStatusList(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.usage()
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
	return c.emitView(d, func(w io.Writer) {
		tb := c.table(w, "CONTEXT", "STATE", "DESCRIPTION")
		tb.row(cText("combined"), cState(orNone(d.Combined)), cText(fmt.Sprintf("%.10s", d.SHA)))
		for _, x := range ds {
			tb.row(cText(x.Context), cState(x.State), cFlex(x.Description))
		}
		tb.flush()
	}, func() screen {
		combined := []cell{cGlyph(d.Combined), cState(orNone(d.Combined))}
		statuses := section{title: "Statuses", n: len(ds)}
		for _, x := range ds {
			statuses.rows = append(statuses.rows, rowOf(cGlyph(x.State), cText(x.Context), cFlex(x.Description), cMeta(x.Creator)))
		}
		return screen{fields: []field{
			{"Commit", []cell{cRef(fmt.Sprintf("%.10s", d.SHA))}},
			{"Combined", combined},
		}, sections: []section{statuses}, actions: []action{
			{"Report", []string{"status", "set", repo.Path(), d.SHA, "--context", "<context>", "--state", "success"}},
		}}
	})
}

func combinedOf(statuses []store.CommitStatus) string { return store.CombinedStatus(statuses) }

func orNone(s string) string {
	if s == "" {
		return "no statuses"
	}
	return s
}
