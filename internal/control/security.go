package control

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/botissue"
	"gitbay.org/gitbay/internal/ci"
	"gitbay.org/gitbay/internal/codescan"
	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/deps"
	"gitbay.org/gitbay/internal/gitutil"
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
	register(Command{Path: []string{"repo", "security", "dismiss"},
		Summary: "dismiss code scanning findings, which then stay out of the issue",
		Usage:   "repo security dismiss <owner/name> <fingerprint>... --reason false-positive|wont-fix|test",
		Flags: []Flag{
			{"--reason", "false-positive|wont-fix|test", "why: not a problem, accepted, or only in tests", ""},
		},
		Examples: []string{"repo security dismiss krz/gitbay 3f9a0c1d2e --reason false-positive"},
		Run:      runSecurityDismiss})
	register(Command{Path: []string{"repo", "security", "reopen"},
		Summary:  "take back dismissals of code scanning findings",
		Usage:    "repo security reopen <owner/name> <fingerprint>...",
		Examples: []string{"repo security reopen krz/gitbay 3f9a0c1d2e"},
		Run:      runSecurityReopen})
	register(Command{Path: []string{"repo", "settings", "security-ignore"},
		Summary:  "paths whose code scanning findings are set aside",
		Usage:    "repo settings security-ignore <owner/name> [<glob>...] (none clears the list)",
		Examples: []string{"repo settings security-ignore krz/gitbay 'testdata/**' 'web/templates/*.html'"},
		Run:      runSecurityIgnore})
	register(Command{Path: []string{"repo", "settings", "security-exclude-rule"},
		Summary:  "code scanning rules, or families of them, whose findings are set aside",
		Usage:    "repo settings security-exclude-rule <owner/name> [<rule>...] (none clears the list)",
		Examples: []string{"repo settings security-exclude-rule krz/gitbay swift.lang.crypto.insecure-random"},
		Run:      runSecurityExcludeRule})
	register(Command{Path: []string{"repo", "settings", "security-frequency"},
		Summary:  "how often the code scan runs, within what the instance allows",
		Usage:    "repo settings security-frequency <owner/name> daily|weekly|monthly",
		Examples: []string{"repo settings security-frequency krz/gitbay weekly"},
		Run:      runSecurityFrequency})
	register(Command{Path: []string{"runner", "results"},
		Summary:    "upload a code scan's SARIF from stdin",
		Usage:      "runner results <build-id>",
		Examples:   []string{"runner results 431 < results.sarif"},
		ReadsStdin: true, Run: runRunnerResults})
}

// SecurityOut is what `repo security status` emits: the last scan's open
// findings, the ones dismissed, and how many it set aside.
type SecurityOut struct {
	Enabled     bool              `json:"enabled"`
	Frequency   string            `json:"frequency,omitempty"`
	NextRun     string            `json:"next_run,omitempty"`
	LastResults string            `json:"last_results,omitempty"`
	IssueNumber int64             `json:"issue_number,omitempty"`
	Findings    []SecurityFinding `json:"findings"`
	Dismissed   []SecurityFinding `json:"dismissed"`
	Aside       codescan.Aside    `json:"aside"`
}

// SecurityFinding is one result of the last scan.
type SecurityFinding struct {
	Fingerprint string `json:"fingerprint"`
	Tool        string `json:"tool"`
	Rule        string `json:"rule"`
	Level       string `json:"level"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Message     string `json:"message"`
	Reason      string `json:"reason,omitempty"`
	DismissedBy string `json:"dismissed_by,omitempty"`
}

// Reasons a finding is dismissed for.
var dismissReasons = []string{"false-positive", "wont-fix", "test"}

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
	next := ci.SecurityNextRun(c.Cfg.Security, repo.Settings.SecurityFrequency, time.Now())
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
	dismissed, err := c.Store.SecurityDismissals(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	freq := c.Cfg.Security.Frequency(repo.Settings.SecurityFrequency)
	out := SecurityOut{Enabled: true, Frequency: freq, NextRun: scan.NextRun, LastResults: scan.LastResults,
		IssueNumber: scan.IssueNumber, Findings: []SecurityFinding{}, Dismissed: []SecurityFinding{}}
	json.Unmarshal([]byte(scan.Aside), &out.Aside)
	for _, f := range codescan.ByPlace(reported) {
		o := SecurityFinding{Fingerprint: f.Fingerprint, Tool: f.Tool, Rule: f.Rule, Level: f.Level,
			Path: f.Path, Line: f.Line, Message: f.Message}
		if d, ok := dismissed[f.Fingerprint]; ok {
			o.Reason, o.DismissedBy = d.Reason, d.By
			out.Dismissed = append(out.Dismissed, o)
			continue
		}
		out.Findings = append(out.Findings, o)
	}
	return c.emitView(out, func(w io.Writer) {
		tracked := ""
		if scan.IssueNumber != 0 {
			tracked = fmt.Sprintf("#%d", scan.IssueNumber)
		}
		v := c.view(w)
		v.fields(
			"scanning", "on",
			"frequency", freq,
			"next scan", scan.NextRun,
			"last results", c.when(scan.LastResults),
			"tracked in", tracked,
			"set aside", asideText(out.Aside),
		)
		if len(out.Findings) > 0 {
			v.section("findings")
			tb := c.table(w, "FINGERPRINT", "TOOL", "RULE", "LEVEL", "PATH", "LINE")
			for _, f := range out.Findings {
				tb.row(cRef(codescan.Short(f.Fingerprint)), cText(f.Tool), cText(f.Rule), cText(f.Level), cRef(f.Path), cNum(int64(f.Line)))
			}
			tb.flush()
		}
		if len(out.Dismissed) > 0 {
			v.section("dismissed")
			tb := c.table(w, "FINGERPRINT", "REASON", "BY", "RULE", "PATH", "LINE")
			for _, f := range out.Dismissed {
				tb.row(cRef(codescan.Short(f.Fingerprint)), cText(f.Reason), cText(f.DismissedBy), cText(f.Rule), cRef(f.Path), cNum(int64(f.Line)))
			}
			tb.flush()
		}
	}, func() screen {
		s := screen{fields: []field{
			{"Scanning", []cell{cGlyph("ok"), cText("on")}},
			{"Frequency", []cell{cText(freq)}},
			{"Next scan", []cell{cText(scan.NextRun)}},
			{"Last results", []cell{cAge(scan.LastResults)}},
		}}
		if scan.IssueNumber != 0 {
			n := strconv.FormatInt(scan.IssueNumber, 10)
			s.fields = append(s.fields, field{"Tracked in", []cell{cLink("#"+n, c.siteURL(repo.Path(), "issues", n))}})
		}
		if a := asideText(out.Aside); a != "" {
			s.fields = append(s.fields, field{"Set aside", []cell{cMeta(a)}})
		}
		found := section{title: "Findings", n: len(out.Findings)}
		for _, f := range out.Findings {
			found.rows = append(found.rows, rowOf(cRef(f.Path+":"+strconv.Itoa(f.Line)), cText(f.Rule), cFlexRef(codescan.Short(f.Fingerprint))))
		}
		s.sections = []section{found}
		if len(out.Dismissed) > 0 {
			gone := section{title: "Dismissed", n: len(out.Dismissed)}
			for _, f := range out.Dismissed {
				gone.rows = append(gone.rows, rowOf(cRef(f.Path+":"+strconv.Itoa(f.Line)), cState(f.Reason), cFlexRef(codescan.Short(f.Fingerprint))))
			}
			s.sections = append(s.sections, gone)
		}
		s.actions = []action{{"Security", []string{"repo", "security", "run", repo.Path()}}}
		if len(out.Findings) > 0 {
			s.actions = append(s.actions, action{"Security", []string{"repo", "security", "dismiss", repo.Path(), "<fp>", "--reason", "<why>"}})
		}
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
	all, overflow, err := codescan.Parse(raw)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	repo, err := c.Store.RepoByID(b.RepoID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	findings, aside, truncated := scanScope(RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name), repo, b.SHA).Apply(all)
	previous, err := c.Store.ReportedFindings(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	asideJSON := ""
	if aside.Ignored > 0 || len(aside.Unused) > 0 {
		j, _ := json.Marshal(aside)
		asideJSON = string(j)
	}
	changed := !codescan.Same(previous, codescan.ByFingerprint(findings)) || asideJSON != scan.Aside
	err = maintainFindings(c, repo, scan, findings, aside, truncated || overflow, changed,
		func() error { return c.Store.ReplaceFindings(repo.ID, findings, asideJSON) })
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

// maintainFindings brings the findings issue in line with a set of
// findings: the open ones listed, the dismissed and set-aside ones
// counted, closed when none are open.
func maintainFindings(c *Ctx, repo store.Repo, scan store.SecurityScan, findings []store.SecurityFinding,
	aside codescan.Aside, truncated, changed bool, save func() error) error {
	dismissed, err := c.Store.SecurityDismissals(repo.ID)
	if err != nil {
		return err
	}
	var open []store.SecurityFinding
	for _, f := range findings {
		if _, ok := dismissed[f.Fingerprint]; !ok {
			open = append(open, f)
		}
	}
	return botissue.Maintain(c.Store, c.Cfg, repo, botissue.Tracked{
		Title:   codescan.IssueTitle,
		Number:  scan.IssueNumber,
		Record:  func(n int64) error { return c.Store.SetSecurityIssue(repo.ID, n) },
		Empty:   len(open) == 0,
		Changed: changed,
		Save:    save,
		Body: func() string {
			return codescan.Body(codescan.Report{Repo: repo.Path(), Branch: repo.DefaultBranch, Open: codescan.ByPlace(open),
				Dismissed: len(findings) - len(open), Aside: aside, Truncated: truncated})
		},
	})
}

// manifestLimit bounds a manifest or .gitattributes read for a scan's scope.
const manifestLimit = 1 << 20

// maxManifestDirs bounds how many directories' manifests a scope reads.
const maxManifestDirs = 50

// scanScope is what a repository sets aside from a scan of commit sha: its
// settings, what .gitattributes marks vendored or generated, and findings
// from rules about libraries no manifest at that commit names. Manifests are
// read in every directory that has one, outside dependency directories, so
// a monorepo's web/package.json counts.
func scanScope(dir string, repo store.Repo, sha string) codescan.Scope {
	sc := codescan.Scope{Ignore: repo.Settings.SecurityIgnore, ExcludeRules: repo.Settings.SecurityExcludeRules}
	if raw, err := gitutil.ReadBlob(dir, sha, ".gitattributes", manifestLimit); err == nil {
		sc.Vendored = codescan.Vendored(raw)
	}
	root := map[string]bool{}
	if entries, err := gitutil.ListTree(dir, sha, ""); err == nil {
		for _, e := range entries {
			root[e.Name] = true
		}
	}
	dirs := []string{""}
	if entries, err := gitutil.ListTreeRecursive(dir, sha, ""); err == nil {
		seen := map[string]bool{"": true}
		for _, e := range entries {
			d := path.Dir(e.Name)
			if seen[d] || len(dirs) == maxManifestDirs || !slices.Contains(deps.ManifestFiles, path.Base(e.Name)) || dependencyDir(d) {
				continue
			}
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	uses := map[string]bool{}
	for _, d := range dirs {
		read := func(p string) ([]byte, error) {
			if d != "" {
				p = d + "/" + p
			}
			return gitutil.ReadBlob(dir, sha, p, manifestLimit)
		}
		for _, dep := range deps.Names(read) {
			uses[dep.Ecosystem+":"+dep.Name] = true
		}
	}
	sc.Uses = func(packages []string) bool {
		for _, p := range packages {
			if name, ok := strings.CutPrefix(p, "root:"); ok {
				if root[name] {
					return true
				}
				continue
			}
			if strings.HasPrefix(p, "go:") && strings.HasSuffix(p, "/") {
				for u := range uses {
					if strings.HasPrefix(u, p) {
						return true
					}
				}
				continue
			}
			if uses[p] {
				return true
			}
		}
		return false
	}
	return sc
}

// dependencyDir reports whether a directory holds other projects' code
// rather than the repository's own.
func dependencyDir(d string) bool {
	for _, part := range strings.Split(d, "/") {
		if part == "node_modules" || part == "vendor" || part == "third_party" {
			return true
		}
	}
	return false
}

// asideText is what the last scan set aside, in a line.
func asideText(a codescan.Aside) string {
	var parts []string
	if a.Ignored > 0 {
		parts = append(parts, fmt.Sprintf("%d ignored", a.Ignored))
	}
	libs := make([]string, 0, len(a.Unused))
	for l := range a.Unused {
		libs = append(libs, l)
	}
	sort.Strings(libs)
	for _, l := range libs {
		parts = append(parts, fmt.Sprintf("%d %s (not used)", a.Unused[l], l))
	}
	return strings.Join(parts, ", ")
}

func runSecurityDismiss(c *Ctx, args []string) int {
	f, err := c.parseArgs(args, flagSpec{Values: []string{"--reason"}, MaxPos: -1, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if len(f.Pos) < 2 {
		return c.usage()
	}
	reason := f.Value("--reason")
	if !slices.Contains(dismissReasons, reason) {
		return c.fail(protocol.ExitUsage, "--reason is one of %s", strings.Join(dismissReasons, ", "))
	}
	return changeDismissals(c, f.Pos[0], f.Pos[1:], func(repoID int64, fps []string) error {
		return c.Store.DismissFindings(repoID, c.User.ID, fps, reason)
	}, "dismissed")
}

func runSecurityReopen(c *Ctx, args []string) int {
	if len(args) < 2 {
		return c.usage()
	}
	return changeDismissals(c, args[0], args[1:], c.Store.ReopenFindings, "reopened")
}

// changeDismissals resolves fingerprints, given in full or as the issue
// prints them, against the last scan's findings, applies change, and
// rewrites the issue to match. Writers may do it: it decides what the
// issue lists, as editing the issue would.
func changeDismissals(c *Ctx, name string, given []string, change func(int64, []string) error, verb string) int {
	repo, code := resolveRepo(c, name, policy.CanWrite)
	if code >= 0 {
		return code
	}
	scan, err := c.Store.SecurityScanFor(repo.ID)
	if errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitFailure, "code scanning is off on %s (repo security enable %s)", repo.Path(), repo.Path())
	} else if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	findings, err := c.Store.ReportedFindings(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	var fps []string
	for _, g := range given {
		fp, msg := resolveFingerprint(findings, g)
		if msg != "" {
			return c.fail(protocol.ExitNotFound, "%s", msg)
		}
		if !slices.Contains(fps, fp) {
			fps = append(fps, fp)
		}
	}
	if err := change(repo.ID, fps); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	var aside codescan.Aside
	json.Unmarshal([]byte(scan.Aside), &aside)
	err = maintainFindings(c, repo, scan, findings, aside, len(findings) >= codescan.MaxFindings, true,
		func() error { return nil })
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{verb: fps}, func(w io.Writer) {
		fmt.Fprintf(w, "%s %d on %s\n", verb, len(fps), repo.Path())
	})
}

// resolveFingerprint finds the one finding a fingerprint or a prefix of at
// least codescan.ShortLen names, or says why not.
func resolveFingerprint(findings []store.SecurityFinding, given string) (string, string) {
	if len(given) < codescan.ShortLen {
		return "", fmt.Sprintf("fingerprint %q is too short; give at least %d characters", given, codescan.ShortLen)
	}
	match := ""
	for _, f := range findings {
		if strings.HasPrefix(f.Fingerprint, given) {
			if match != "" {
				return "", fmt.Sprintf("fingerprint %q names more than one finding", given)
			}
			match = f.Fingerprint
		}
	}
	if match == "" {
		return "", fmt.Sprintf("no finding %q in the last scan", given)
	}
	return match, ""
}

// runSecurityFrequency sets how often the repository is scanned and, when
// scanning is on, moves the next scan to match.
func runSecurityFrequency(c *Ctx, args []string) int {
	if len(args) != 2 || !slices.Contains(config.ScanFrequencies, args[1]) {
		return c.usage()
	}
	freq := args[1]
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if allowed := c.Cfg.Security.Frequencies(); !slices.Contains(allowed, freq) {
		return c.fail(protocol.ExitFailure, "this instance scans %s", strings.Join(allowed, ", "))
	}
	if _, err := c.Store.UpdateRepoSettings(repo.ID, func(s *store.RepoSettings) { s.SecurityFrequency = freq }); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	next := ""
	if _, err := c.Store.SecurityScanFor(repo.ID); err == nil {
		next = ci.SecurityNextRun(c.Cfg.Security, freq, time.Now())
		if err := c.Store.SetSecurityNext(repo.ID, next); err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
	}
	return c.emit(map[string]any{"frequency": freq, "next_run": next}, func(w io.Writer) {
		if next == "" {
			fmt.Fprintf(w, "%s scans %s once scanning is on\n", repo.Path(), freq)
			return
		}
		fmt.Fprintf(w, "%s scans %s; next scan %s\n", repo.Path(), freq, next)
	})
}

// maxSecurityList bounds the ignore and exclude-rule settings.
const maxSecurityList = 50

func runSecurityIgnore(c *Ctx, args []string) int {
	return setSecurityList(c, args, "a glob", func(s *store.RepoSettings, xs []string) { s.SecurityIgnore = xs }, "ignores")
}

func runSecurityExcludeRule(c *Ctx, args []string) int {
	return setSecurityList(c, args, "a rule", func(s *store.RepoSettings, xs []string) { s.SecurityExcludeRules = xs }, "excludes")
}

// setSecurityList replaces one of the code scanning lists, which apply from
// the next scan.
func setSecurityList(c *Ctx, args []string, what string, set func(*store.RepoSettings, []string), verb string) int {
	if len(args) < 1 {
		return c.usage()
	}
	var xs []string
	for _, x := range args[1:] {
		if x == "" || len(x) > 200 || strings.ContainsAny(x, " \t\r\n") {
			return c.fail(protocol.ExitUsage, "%s is 1 to 200 characters with no spaces: %q", what, x)
		}
		if !slices.Contains(xs, x) {
			xs = append(xs, x)
		}
	}
	if len(xs) > maxSecurityList {
		return c.fail(protocol.ExitUsage, "at most %d entries", maxSecurityList)
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	s, err := c.Store.UpdateRepoSettings(repo.ID, func(s *store.RepoSettings) { set(s, xs) })
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(s, func(w io.Writer) {
		if len(xs) == 0 {
			fmt.Fprintf(w, "code scanning %s cleared on %s\n", verb, repo.Path())
			return
		}
		fmt.Fprintf(w, "code scanning %s on %s: %s; from the next scan (repo security run %s)\n",
			verb, repo.Path(), strings.Join(xs, ", "), repo.Path())
	})
}
