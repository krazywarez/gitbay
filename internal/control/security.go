package control

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"time"

	"gitbay.org/gitbay/internal/botissue"
	"gitbay.org/gitbay/internal/ci"
	"gitbay.org/gitbay/internal/codescan"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	// Code scanning: the instance's own job ([security] in the config),
	// scheduled per repository that opts in, reporting through an issue.
	// Opt-in because the build runs on a runner the repository is attached
	// to, which is the owner's arrangement to make.
	register(Command{Path: []string{"repo", "security", "enable"},
		Summary:  "scan the default branch on the instance's code scanning schedule",
		Usage:    "repo security enable <owner/name>",
		Examples: []string{"repo security enable krz/gitbay"},
		Run:      runSecurityEnable})
	register(Command{Path: []string{"repo", "security", "disable"},
		Summary:  "stop code scanning",
		Usage:    "repo security disable <owner/name>",
		Examples: []string{"repo security disable krz/gitbay"},
		Run:      runSecurityDisable})
	register(Command{Path: []string{"repo", "security", "run"},
		Summary:  "queue a code scan of the default branch now",
		Usage:    "repo security run <owner/name>",
		Examples: []string{"repo security run krz/gitbay"},
		Run:      runSecurityRun})
	register(Command{Path: []string{"repo", "security", "status"},
		Summary:  "show code scanning state and the last scan's findings",
		Usage:    "repo security status <owner/name>",
		Examples: []string{"repo security status krz/gitbay"},
		ReadOnly: true, Run: runSecurityStatus})
	register(Command{Path: []string{"runner", "results"},
		Summary:    "upload a code scan's SARIF from stdin",
		Usage:      "runner results <build-id>",
		Examples:   []string{"runner results 431 < results.sarif"},
		ReadsStdin: true, Run: runRunnerResults})
}

// SecurityOut is what `repo security status` emits.
type SecurityOut struct {
	Enabled     bool              `json:"enabled"`
	NextRun     string            `json:"next_run,omitempty"`
	LastResults string            `json:"last_results,omitempty"`
	IssueNumber int64             `json:"issue_number,omitempty"`
	Findings    []SecurityFinding `json:"findings"`
}

// SecurityFinding is one result of the last scan.
type SecurityFinding struct {
	Tool    string `json:"tool"`
	Rule    string `json:"rule"`
	Level   string `json:"level"`
	Path    string `json:"path"`
	Line    int    `json:"line"`
	Message string `json:"message"`
}

func runSecurityEnable(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.usage()
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if c.Cfg.Security.Image == "" {
		return c.fail(protocol.ExitFailure, "this instance has no code scanning image; an operator sets [security] image")
	}
	next := ci.NextRun(c.Cfg.Security.Schedule, time.Now())
	if err := c.Store.EnableSecurityScan(repo.ID, next); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"enabled": true, "next_run": next}, func(w io.Writer) {
		fmt.Fprintf(w, "code scanning enabled on %s; first scan %s\n", repo.Path(), next)
	})
}

func runSecurityDisable(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.usage()
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if err := c.Store.DisableSecurityScan(repo.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"enabled": false}, func(w io.Writer) {
		fmt.Fprintf(w, "code scanning disabled on %s\n", repo.Path())
	})
}

func runSecurityRun(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.usage()
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if _, err := c.Store.SecurityScanFor(repo.ID); errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitFailure, "code scanning is off on %s (repo security enable %s)", repo.Path(), repo.Path())
	} else if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if c.Cfg.Security.Image == "" {
		return c.fail(protocol.ExitFailure, "this instance has no code scanning image; an operator sets [security] image")
	}
	n, err := ci.QueueSecurityScan(c.Store, c.Cfg.Security, RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name), repo)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"build": n}, func(w io.Writer) {
		fmt.Fprintf(w, "queued build %d: %s on %s\n", n, ci.SecurityJob, repo.Path())
	})
}

func runSecurityStatus(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.usage()
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	scan, err := c.Store.SecurityScanFor(repo.ID)
	if errors.Is(err, store.ErrNotFound) {
		return c.emitView(map[string]any{"enabled": false}, func(w io.Writer) {
			c.view(w).fields("scanning", fmt.Sprintf("off (repo security enable %s)", repo.Path()))
		}, func() screen {
			return screen{
				fields:  []field{{"Scanning", []cell{cText("off")}}},
				actions: []action{{"Security", []string{"repo", "security", "enable", repo.Path()}}},
			}
		})
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	reported, err := c.Store.ReportedFindings(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	out := SecurityOut{Enabled: true, NextRun: scan.NextRun, LastResults: scan.LastResults,
		IssueNumber: scan.IssueNumber, Findings: []SecurityFinding{}}
	for _, f := range reported {
		out.Findings = append(out.Findings, SecurityFinding{f.Tool, f.Rule, f.Level, f.Path, f.Line, f.Message})
	}
	return c.emitView(out, func(w io.Writer) {
		tracked := ""
		if scan.IssueNumber != 0 {
			tracked = fmt.Sprintf("#%d", scan.IssueNumber)
		}
		v := c.view(w)
		v.fields(
			"scanning", "on",
			"next scan", scan.NextRun,
			"last results", c.when(scan.LastResults),
			"tracked in", tracked,
		)
		if len(out.Findings) > 0 {
			v.section("findings")
			tb := c.table(w, "TOOL", "RULE", "LEVEL", "PATH", "LINE")
			for _, f := range out.Findings {
				tb.row(cText(f.Tool), cText(f.Rule), cText(f.Level), cRef(f.Path), cNum(int64(f.Line)))
			}
			tb.flush()
		}
	}, func() screen {
		s := screen{fields: []field{
			{"Scanning", []cell{cGlyph("ok"), cText("on")}},
			{"Next scan", []cell{cText(scan.NextRun)}},
			{"Last results", []cell{cAge(scan.LastResults)}},
		}}
		if scan.IssueNumber != 0 {
			n := strconv.FormatInt(scan.IssueNumber, 10)
			s.fields = append(s.fields, field{"Tracked in", []cell{cLink("#"+n, c.siteURL(repo.Path(), "issues", n))}})
		}
		found := section{title: "Findings", n: len(out.Findings)}
		for _, f := range out.Findings {
			found.rows = append(found.rows, rowOf(cRef(f.Path+":"+strconv.Itoa(f.Line)), cText(f.Rule), cMeta(f.Level)))
		}
		s.sections = []section{found}
		s.actions = []action{{"Security", []string{"repo", "security", "run", repo.Path()}}}
		return s
	})
}

// runRunnerResults takes a security build's SARIF from the runner that
// claimed it, replaces the repository's findings with it, and brings the
// findings issue in line. Only the instance's own job may upload: its
// steps are the operator's, where any other job's are the repository's.
func runRunnerResults(c *Ctx, args []string) int {
	key, code := runnerSession(c)
	if code >= 0 {
		return code
	}
	if len(args) != 1 {
		return c.usage()
	}
	id, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		return c.fail(protocol.ExitUsage, "bad build id %q", args[0])
	}
	b, err := c.Store.BuildByID(id)
	if err != nil {
		return c.fail(protocol.ExitNotFound, "no build %d", id)
	}
	if ok, err := runnerMayBuild(c, key, b.RepoID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	} else if !ok {
		return c.fail(protocol.ExitDenied, "this key is not attached to the build's repository; a repository admin attaches it with repo runner add")
	}
	if b.Job != ci.SecurityJob {
		return c.fail(protocol.ExitDenied, "results are accepted from the %s job only", ci.SecurityJob)
	}
	if b.Status != "running" {
		return c.fail(protocol.ExitFailure, "build %d is %s", id, b.Status)
	}
	scan, err := c.Store.SecurityScanFor(b.RepoID)
	if errors.Is(err, store.ErrNotFound) {
		// Disabled while the build ran: nothing to report into.
		return c.emit(map[string]any{"findings": 0}, func(w io.Writer) {})
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	raw, err := io.ReadAll(io.LimitReader(c.Stdin, codescan.MaxUpload+1))
	if err != nil {
		return c.fail(protocol.ExitFailure, "reading results: %v", err)
	}
	if len(raw) > codescan.MaxUpload {
		return c.fail(protocol.ExitFailure, "results are over %d bytes", codescan.MaxUpload)
	}
	findings, truncated, err := codescan.Parse(raw)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	repo, err := c.Store.RepoByID(b.RepoID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	previous, err := c.Store.ReportedFindings(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	err = botissue.Maintain(c.Store, c.Cfg, repo, botissue.Tracked{
		Title:   codescan.IssueTitle,
		Number:  scan.IssueNumber,
		Record:  func(n int64) error { return c.Store.SetSecurityIssue(repo.ID, n) },
		Empty:   len(findings) == 0,
		Changed: !codescan.Same(previous, codescan.ByFingerprint(findings)),
		Save:    func() error { return c.Store.ReplaceFindings(repo.ID, findings) },
		Body:    func() string { return codescan.Body(repo.DefaultBranch, findings, truncated) },
	})
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	// Stamped whether or not the set changed: the status says when results
	// last arrived.
	if err := c.Store.StampSecurityResults(repo.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{"findings": len(findings), "truncated": truncated}, func(w io.Writer) {
		fmt.Fprintf(w, "%d findings recorded for build %d\n", len(findings), b.Number)
	})
}
