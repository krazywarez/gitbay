package control

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/leaks"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	// The push-time scan runs in pre-receive (internal/hookd); these set
	// what it does and scan what is already in the history.
	register(Command{Path: []string{"repo", "settings", "secret-scan"},
		Summary:  "scan pushes for credentials: off, warn the pusher, or block the push",
		Usage:    "repo settings secret-scan <owner/name> off|warn|block",
		Examples: []string{"repo settings secret-scan krz/gitbay block"},
		Run:      runSecretScanMode})
	register(Command{Path: []string{"repo", "settings", "secret-scan-allow"},
		Summary:  "paths whose secret scan findings are ignored",
		Usage:    "repo settings secret-scan-allow <owner/name> [<glob>...] (none clears the list)",
		Examples: []string{"repo settings secret-scan-allow krz/gitbay 'testdata/**' internal/mailin/dkim_test.go"},
		Run:      runSecretScanAllow})
	register(Command{Path: []string{"repo", "secret-scan"},
		Summary:  "scan the whole history for credentials",
		Usage:    "repo secret-scan <owner/name>",
		Examples: []string{"repo secret-scan krz/gitbay"},
		ReadOnly: true, Run: runSecretScanHistory})
}

// maxSecretScanAllow bounds the allowlist, as maxRequiredContexts bounds
// the contexts.
const maxSecretScanAllow = 50

func runSecretScanMode(c *Ctx, args []string) int {
	if len(args) != 2 || !slices.Contains([]string{leaks.ModeOff, leaks.ModeWarn, leaks.ModeBlock}, args[1]) {
		return c.usage()
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	s, err := c.Store.UpdateRepoSettings(repo.ID, func(s *store.RepoSettings) { s.SecretScan = args[1] })
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(s, func(w io.Writer) {
		fmt.Fprintf(w, "secret_scan %s on %s\n", args[1], repo.Path())
	})
}

func runSecretScanAllow(c *Ctx, args []string) int {
	if len(args) < 1 {
		return c.usage()
	}
	var globs []string
	for _, g := range args[1:] {
		if g == "" || len(g) > 200 || strings.ContainsAny(g, "\t\r\n") {
			return c.fail(protocol.ExitUsage, "a glob is 1 to 200 characters on one line: %q", g)
		}
		if !slices.Contains(globs, g) {
			globs = append(globs, g)
		}
	}
	if len(globs) > maxSecretScanAllow {
		return c.fail(protocol.ExitUsage, "at most %d allowed paths", maxSecretScanAllow)
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	s, err := c.Store.UpdateRepoSettings(repo.ID, func(s *store.RepoSettings) { s.SecretScanAllow = globs })
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(s, func(w io.Writer) {
		if len(globs) == 0 {
			fmt.Fprintf(w, "secret scan allowlist cleared on %s\n", repo.Path())
			return
		}
		fmt.Fprintf(w, "secret scan allows on %s: %s\n", repo.Path(), strings.Join(globs, ", "))
	})
}

// historyScanDeadline bounds a history scan, which runs while the caller
// waits on the session.
const historyScanDeadline = 2 * time.Minute

// SecretScanOut is what `repo secret-scan` emits.
type SecretScanOut struct {
	Findings  []leaks.Finding `json:"findings"`
	Blobs     int             `json:"blobs"`
	Truncated bool            `json:"truncated"`
}

// runSecretScanHistory scans every blob reachable from any ref, for what
// arrived before the push-time scan or never passed through it: imports,
// pull mirrors, and server-side writes. Admin only, since a scan reads up
// to leaks.MaxScanBytes and a reader could otherwise ask for one on every
// call. The allowlist applies, as it does at push time.
func runSecretScanHistory(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.usage()
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	ctx, cancel := context.WithTimeout(context.Background(), historyScanDeadline)
	defer cancel()
	rep := leaks.Scan(ctx, RepoDir(c.Cfg.Server.Root, repo.OwnerName, repo.Name), "--all")
	if rep.Error != "" {
		return c.fail(protocol.ExitFailure, "secret scan: %s", rep.Error)
	}
	out := SecretScanOut{Findings: leaks.Allowed(rep.Findings, repo.Settings.SecretScanAllow),
		Blobs: rep.Blobs, Truncated: rep.Truncated}
	if out.Findings == nil {
		out.Findings = []leaks.Finding{}
	}
	return c.emitView(out, func(w io.Writer) {
		v := c.view(w)
		v.fields(
			"blobs", strconv.Itoa(out.Blobs),
			"findings", strconv.Itoa(len(out.Findings)),
			"truncated", strconv.FormatBool(out.Truncated),
		)
		if len(out.Findings) > 0 {
			tb := c.table(w, "COMMIT", "PATH", "LINE", "RULE")
			for _, f := range out.Findings {
				tb.row(cText(shortCommit(f.Commit)), cRef(f.Path), cNum(int64(f.Line)), cText(f.Rule))
			}
			tb.flush()
		}
	}, func() screen {
		s := screen{fields: []field{
			{"Repo", []cell{cLink(repo.Path(), c.siteURL(repo.Path()))}},
			{"Blobs", []cell{cText(strconv.Itoa(out.Blobs))}},
		}}
		if out.Truncated {
			s.fields = append(s.fields, field{"Truncated", []cell{cMark("stopped at the size limit", sgrRed)}})
		}
		found := section{title: "Findings", n: len(out.Findings)}
		for _, f := range out.Findings {
			found.rows = append(found.rows, rowOf(cRef(f.Path+":"+strconv.Itoa(f.Line)), cText(f.Rule), cMeta(shortCommit(f.Commit))))
		}
		s.sections = []section{found}
		s.actions = []action{{"Settings", []string{"repo", "settings", "secret-scan-allow", repo.Path(), "<glob>"}}}
		return s
	})
}

func shortCommit(sha string) string { return sha[:min(len(sha), 10)] }
