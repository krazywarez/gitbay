package control

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/deps"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	// Dependency update and advisory checks. Opt-in per repository: the
	// check tells a public registry and OSV what the repository depends on,
	// which is the owner's disclosure to make, not the instance's.
	register(Command{Path: []string{"repo", "deps", "enable"},
		Summary:  "check dependencies for updates and advisories",
		Usage:    "repo deps enable <owner/name>",
		Examples: []string{"repo deps enable krz/gitbay"},
		Run:      runDepsEnable})
	register(Command{Path: []string{"repo", "deps", "disable"},
		Summary:  "stop checking dependencies",
		Usage:    "repo deps disable <owner/name>",
		Examples: []string{"repo deps disable krz/gitbay"},
		Run:      runDepsDisable})
	register(Command{Path: []string{"repo", "deps", "status"},
		Summary:  "show dependency check state",
		Usage:    "repo deps status <owner/name>",
		Examples: []string{"repo deps status krz/gitbay"},
		ReadOnly: true, Run: runDepsStatus})
	register(Command{Path: []string{"repo", "deps", "dismiss"},
		Summary: "dismiss dependency advisories, which then stay out of the issue",
		Usage:   "repo deps dismiss <owner/name> <advisory>... --reason not-affected|wont-fix|dev-only",
		Flags: []Flag{
			{"--reason", "not-affected|wont-fix|dev-only", "why: does not apply, accepted, or only in development", ""},
		},
		Examples: []string{"repo deps dismiss krz/gitbay GO-2026-5932 --reason not-affected"},
		Run:      runDepsDismiss})
	register(Command{Path: []string{"repo", "deps", "reopen"},
		Summary:  "take back dismissals of dependency advisories",
		Usage:    "repo deps reopen <owner/name> <advisory>...",
		Examples: []string{"repo deps reopen krz/gitbay GO-2026-5932"},
		Run:      runDepsReopen})
}

// Reasons an advisory is dismissed for.
var advisoryReasons = []string{"not-affected", "wont-fix", "dev-only"}

// DepsOut is what `repo deps status` emits: the check's state and what
// the last run found behind or matching an advisory. Named so the web
// decodes the same struct the command writes.
type DepsOut struct {
	Enabled             bool          `json:"enabled"`
	LastCheck           string        `json:"last_check,omitempty"`
	LastError           string        `json:"last_error,omitempty"`
	IssueNumber         int64         `json:"issue_number,omitempty"`
	AdvisoryIssueNumber int64         `json:"advisory_issue_number,omitempty"`
	Behind              []DepBehind   `json:"behind"`
	Advisories          []DepAdvisory `json:"advisories"`
	// Dismissed and NotImported are matching advisories left out of the
	// issue: dismissed by a writer, or Go advisories for packages no file
	// imports.
	Dismissed   []DepAdvisory `json:"dismissed"`
	NotImported []DepAdvisory `json:"not_imported"`
}

// DepBehind is one dependency with a newer release than the manifest pins.
type DepBehind struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Current   string `json:"current"`
	Latest    string `json:"latest"`
}

// DepAdvisory is one published advisory matching a resolved dependency.
type DepAdvisory struct {
	Ecosystem string `json:"ecosystem"`
	Name      string `json:"name"`
	Version   string `json:"version"`
	ID        string `json:"id"`
	Summary   string `json:"summary"`
	Severity  string `json:"severity,omitempty"`
	Fixed     string `json:"fixed,omitempty"`
	Reason    string `json:"reason,omitempty"`
	By        string `json:"dismissed_by,omitempty"`
}

func runDepsEnable(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.usage()
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
		return c.usage()
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
		return c.usage()
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	check, err := c.Store.DepCheckFor(repo.ID)
	if errors.Is(err, store.ErrNotFound) {
		return c.emitView(map[string]any{"enabled": false}, func(w io.Writer) {
			v := c.view(w)
			v.fields("checks", fmt.Sprintf("off (repo deps enable %s)", repo.Path()))
		}, func() screen {
			return screen{
				fields:  []field{{"Checks", []cell{cText("off")}}},
				actions: []action{{"Deps", []string{"repo", "deps", "enable", repo.Path()}}},
			}
		})
	}
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	reports, err := c.Store.ReportedDeps(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	advisories, err := c.Store.ReportedAdvisories(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	dismissed, err := c.Store.AdvisoryDismissals(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	out := DepsOut{Enabled: true, LastCheck: check.LastCheck, LastError: check.LastError,
		IssueNumber: check.IssueNumber, AdvisoryIssueNumber: check.AdvisoryIssueNumber,
		Behind: []DepBehind{}, Advisories: []DepAdvisory{}, Dismissed: []DepAdvisory{}, NotImported: []DepAdvisory{}}
	for _, r := range reports {
		out.Behind = append(out.Behind, DepBehind{r.Ecosystem, r.Name, r.Current, r.Latest})
	}
	for _, a := range advisories {
		o := DepAdvisory{Ecosystem: a.Ecosystem, Name: a.Name, Version: a.Version, ID: a.ID,
			Summary: a.Summary, Severity: a.Severity, Fixed: a.Fixed}
		switch d, ok := dismissed[a.ID]; {
		case a.NotImported:
			out.NotImported = append(out.NotImported, o)
		case ok:
			o.Reason, o.By = d.Reason, d.By
			out.Dismissed = append(out.Dismissed, o)
		default:
			out.Advisories = append(out.Advisories, o)
		}
	}
	return c.emitView(out, func(w io.Writer) {
		tracked, advised := "", ""
		if check.IssueNumber != 0 {
			tracked = fmt.Sprintf("#%d", check.IssueNumber)
		}
		if check.AdvisoryIssueNumber != 0 {
			advised = fmt.Sprintf("#%d", check.AdvisoryIssueNumber)
		}
		v := c.view(w)
		v.fields(
			"checks", "on",
			"last check", c.when(check.LastCheck),
			"last error", check.LastError,
			"tracked in", tracked,
			"advisories in", advised,
		)
		if len(out.Behind) > 0 {
			v.section("behind")
			tb := c.table(w, "ECOSYSTEM", "NAME", "CURRENT", "LATEST")
			for _, b := range out.Behind {
				tb.row(cText(b.Ecosystem), cRef(b.Name), cText(b.Current), cText(b.Latest))
			}
			tb.flush()
		}
		if len(out.Advisories) > 0 {
			v.section("advisories")
			tb := c.table(w, "ECOSYSTEM", "NAME", "VERSION", "ADVISORY", "FIXED")
			for _, a := range out.Advisories {
				tb.row(cText(a.Ecosystem), cRef(a.Name), cText(a.Version), cText(a.ID), cText(a.Fixed))
			}
			tb.flush()
		}
		if len(out.Dismissed) > 0 {
			v.section("dismissed")
			tb := c.table(w, "ADVISORY", "NAME", "REASON", "BY")
			for _, a := range out.Dismissed {
				tb.row(cText(a.ID), cRef(a.Name), cText(a.Reason), cText(a.By))
			}
			tb.flush()
		}
		if len(out.NotImported) > 0 {
			v.section("not imported")
			tb := c.table(w, "ADVISORY", "NAME", "VERSION")
			for _, a := range out.NotImported {
				tb.row(cText(a.ID), cRef(a.Name), cText(a.Version))
			}
			tb.flush()
		}
	}, func() screen {
		state := []cell{cGlyph("ok"), cText("on")}
		if check.LastError != "" {
			state = []cell{cGlyph("failed"), cText("on")}
		}
		s := screen{fields: []field{
			{"Checks", state},
			{"Last check", []cell{cAge(check.LastCheck)}},
			{"Error", []cell{cMark(check.LastError, sgrRed)}},
		}}
		if check.IssueNumber != 0 {
			n := strconv.FormatInt(check.IssueNumber, 10)
			s.fields = append(s.fields, field{"Tracked in", []cell{cLink("#"+n, c.siteURL(repo.Path(), "issues", n))}})
		}
		if check.AdvisoryIssueNumber != 0 {
			n := strconv.FormatInt(check.AdvisoryIssueNumber, 10)
			s.fields = append(s.fields, field{"Advisories in", []cell{cLink("#"+n, c.siteURL(repo.Path(), "issues", n))}})
		}
		behind := section{title: "Behind", n: len(out.Behind)}
		for _, b := range out.Behind {
			behind.rows = append(behind.rows, rowOf(cRef(b.Name), cText(b.Ecosystem), cMeta(b.Current+" → "+b.Latest)))
		}
		advised := section{title: "Advisories", n: len(out.Advisories)}
		for _, a := range out.Advisories {
			fix := a.Version
			if a.Fixed != "" {
				fix += " → " + a.Fixed
			}
			advised.rows = append(advised.rows, rowOf(cRef(a.Name), cMark(a.ID, sgrRed), cMeta(fix)))
		}
		s.sections = []section{behind, advised}
		if len(out.Dismissed) > 0 {
			gone := section{title: "Dismissed", n: len(out.Dismissed)}
			for _, a := range out.Dismissed {
				gone.rows = append(gone.rows, rowOf(cRef(a.Name), cText(a.ID), cState(a.Reason)))
			}
			s.sections = append(s.sections, gone)
		}
		if len(out.NotImported) > 0 {
			aside := section{title: "Not imported", n: len(out.NotImported)}
			for _, a := range out.NotImported {
				aside.rows = append(aside.rows, rowOf(cRef(a.Name), cText(a.ID), cMeta(a.Version)))
			}
			s.sections = append(s.sections, aside)
		}
		s.actions = []action{{"Deps", []string{"repo", "deps", "disable", repo.Path()}}}
		if len(out.Advisories) > 0 {
			s.actions = append(s.actions, action{"Deps", []string{"repo", "deps", "dismiss", repo.Path(), "<id>", "--reason", "<why>"}})
		}
		return s
	})
}

func runDepsDismiss(c *Ctx, args []string) int {
	f, err := c.parseArgs(args, flagSpec{Values: []string{"--reason"}, MaxPos: -1, Usage: c.Cmd.Usage})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if len(f.Pos) < 2 {
		return c.usage()
	}
	reason := f.Value("--reason")
	if !slices.Contains(advisoryReasons, reason) {
		return c.fail(protocol.ExitUsage, "--reason is one of %s", strings.Join(advisoryReasons, ", "))
	}
	return changeAdvisoryDismissals(c, f.Pos[0], f.Pos[1:], func(repoID int64, ids []string) error {
		return c.Store.DismissAdvisories(repoID, c.User.ID, ids, reason)
	}, "dismissed")
}

func runDepsReopen(c *Ctx, args []string) int {
	if len(args) < 2 {
		return c.usage()
	}
	return changeAdvisoryDismissals(c, args[0], args[1:], c.Store.ReopenAdvisories, "reopened")
}

// changeAdvisoryDismissals checks advisory ids against the last check's
// matches, applies change, and rewrites the issue to match. Writers may do
// it, as for code scanning.
func changeAdvisoryDismissals(c *Ctx, name string, ids []string, change func(int64, []string) error, verb string) int {
	repo, code := resolveRepo(c, name, policy.CanWrite)
	if code >= 0 {
		return code
	}
	if _, err := c.Store.DepCheckFor(repo.ID); errors.Is(err, store.ErrNotFound) {
		return c.fail(protocol.ExitFailure, "dependency checks are off on %s (repo deps enable %s)", repo.Path(), repo.Path())
	} else if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	matched, err := c.Store.ReportedAdvisories(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	var known []string
	for _, a := range matched {
		known = append(known, a.ID)
	}
	var unique []string
	for _, id := range ids {
		if !slices.Contains(known, id) {
			return c.fail(protocol.ExitNotFound, "no advisory %q in the last check", id)
		}
		if !slices.Contains(unique, id) {
			unique = append(unique, id)
		}
	}
	if err := change(repo.ID, unique); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if err := deps.MaintainAdvisories(c.Store, c.Cfg, repo, matched, true, func() error { return nil }); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]any{verb: unique}, func(w io.Writer) {
		fmt.Fprintf(w, "%s %d on %s\n", verb, len(unique), repo.Path())
	})
}
