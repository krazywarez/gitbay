package control

import (
	"errors"
	"fmt"
	"io"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	// Dependency update checks. Opt-in per repository: the check tells a
	// public registry what the repository depends on, which is the owner's
	// disclosure to make, not the instance's.
	register(Command{Path: []string{"repo", "deps", "enable"},
		Summary: "check dependencies for updates: repo deps enable <owner/name>", Run: runDepsEnable})
	register(Command{Path: []string{"repo", "deps", "disable"},
		Summary: "stop checking dependencies: repo deps disable <owner/name>", Run: runDepsDisable})
	register(Command{Path: []string{"repo", "deps", "status"},
		Summary: "show dependency check state: repo deps status <owner/name>", ReadOnly: true, Run: runDepsStatus})
}

func runDepsEnable(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo deps enable <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if err := c.Store.EnableDepCheck(repo.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"enabled": true}, func(w io.Writer) {
		fmt.Fprintf(w, "dependency checks enabled on %s\n", repo.Path())
	})
}

func runDepsDisable(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo deps disable <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if err := c.Store.DisableDepCheck(repo.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"enabled": false}, func(w io.Writer) {
		fmt.Fprintf(w, "dependency checks disabled on %s\n", repo.Path())
	})
}

func runDepsStatus(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo deps status <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	check, err := c.Store.DepCheckFor(repo.ID)
	if errors.Is(err, store.ErrNotFound) {
		return c.emit(map[string]any{"enabled": false}, func(w io.Writer) {
			fmt.Fprintf(w, "dependency checks are off for %s (repo deps enable %s)\n", repo.Path(), repo.Path())
		})
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	reports, err := c.Store.ReportedDeps(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	type behind struct {
		Ecosystem string `json:"ecosystem"`
		Name      string `json:"name"`
		Current   string `json:"current"`
		Latest    string `json:"latest"`
	}
	out := struct {
		Enabled     bool     `json:"enabled"`
		LastCheck   string   `json:"last_check,omitempty"`
		LastError   string   `json:"last_error,omitempty"`
		IssueNumber int64    `json:"issue_number,omitempty"`
		Behind      []behind `json:"behind"`
	}{Enabled: true, LastCheck: check.LastCheck, LastError: check.LastError,
		IssueNumber: check.IssueNumber, Behind: []behind{}}
	for _, r := range reports {
		out.Behind = append(out.Behind, behind{r.Ecosystem, r.Name, r.Current, r.Latest})
	}
	return c.emit(out, func(w io.Writer) {
		fmt.Fprintf(w, "checks on, last %s\n", orDash(check.LastCheck))
		if check.LastError != "" {
			fmt.Fprintf(w, "last error: %s\n", check.LastError)
		}
		if check.IssueNumber != 0 {
			fmt.Fprintf(w, "tracked in #%d\n", check.IssueNumber)
		}
		for _, b := range out.Behind {
			fmt.Fprintf(w, "%s\t%s\t%s\t-> %s\n", b.Ecosystem, b.Name, b.Current, b.Latest)
		}
	})
}
