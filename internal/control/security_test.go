package control

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/ci"
	"gitbay.org/gitbay/internal/codescan"
	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

const testSARIF = `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"opengrep"}},"results":[
	{"ruleId":"go.lang.security.audit.sqli","level":"error","message":{"text":"SQL built from input"},
	 "locations":[{"physicalLocation":{"artifactLocation":{"uri":"store/q.go"},"region":{"startLine":12}}}]}]}]}`

// The whole round trip without a runner process: enable, queue, claim,
// upload, and the issue that results in.
func TestSecurityScanRoundTrip(t *testing.T) {
	st, repo, u, root := browseFixture(t)
	p := repo.Path()
	sec := config.Security{Image: "localhost/gitbay-security:1", Schedule: "0 4 * * *", Steps: config.DefaultSecuritySteps}

	c, out := runnerCtx(st, u.ID, root)
	if code := runSecurityEnable(c, []string{p}); code != protocol.ExitFailure || !strings.Contains(out.String(), "no code scanning image") {
		t.Fatalf("enable without an image: exit %d %s", code, out)
	}
	run := func(f func(*Ctx, []string) int, stdin string, args ...string) (int, string) {
		t.Helper()
		c, out := runnerCtx(st, u.ID, root)
		c.Cfg.Security = sec
		c.Cfg.Mail.SMTPHost = "localhost:587"
		c.Stdin = strings.NewReader(stdin)
		c.JSON = true
		return f(c, args), out.String()
	}
	if code, out := run(runSecurityEnable, "", p); code != protocol.ExitOK {
		t.Fatalf("enable: %d %s", code, out)
	}
	if code, out := run(runSecurityRun, "", p); code != protocol.ExitOK {
		t.Fatalf("run: %d %s", code, out)
	}
	if code, out := run(runSecurityRun, "", p); code == protocol.ExitOK || !strings.Contains(out, "already pending") {
		t.Fatalf("second run: %d %s", code, out)
	}

	st.SetBuildSecret(repo.ID, "DEPLOY_TOKEN", "s3cret")
	code, claim := run(runRunnerNext, "")
	if code != protocol.ExitOK || !strings.Contains(claim, `"job":"`+ci.SecurityJob+`"`) || !strings.Contains(claim, `"results":true`) {
		t.Fatalf("claim: %d %s", code, claim)
	}
	if strings.Contains(claim, "s3cret") {
		t.Fatalf("the scan was handed the repository's secrets: %s", claim)
	}
	var claimed struct {
		Data struct {
			ID  int64  `json:"id"`
			SHA string `json:"sha"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(claim), &claimed)
	id := claimed.Data.ID

	if code, out := run(runRunnerResults, testSARIF, itoa(id)); code != protocol.ExitOK {
		t.Fatalf("results: %d %s", code, out)
	}
	scan, _ := st.SecurityScanFor(repo.ID)
	if scan.IssueNumber == 0 || scan.LastResults == "" {
		t.Fatalf("scan = %+v", scan)
	}
	issue, _ := st.IssueByNumber(repo.ID, scan.IssueNumber)
	if issue.Title != codescan.IssueTitle || !strings.Contains(issue.Body, "`store/q.go:12` go.lang.security.audit.sqli (error)") {
		t.Errorf("issue %q:\n%s", issue.Title, issue.Body)
	}
	if code, out := run(runRunnerDone, "", itoa(id), "success"); code != protocol.ExitOK {
		t.Fatalf("done: %d %s", code, out)
	}
	if statuses, _ := st.CommitStatusesFor(repo.ID, []string{claimed.Data.SHA}); len(statuses[claimed.Data.SHA]) != 0 {
		t.Errorf("the scan set a commit status: %+v", statuses)
	}

	var status struct {
		Data SecurityOut `json:"data"`
	}
	json.Unmarshal([]byte(dispatchIn(t, st, u, root, "", "repo", "security", "status", p, "--json")), &status)
	if !status.Data.Enabled || len(status.Data.Findings) != 1 || status.Data.Findings[0].Line != 12 {
		t.Errorf("status = %+v", status.Data)
	}
	screen := atTerminalIn(t, st, u, root, "repo", "security", "status", p)
	if !strings.Contains(screen, "Findings (1)") || !strings.Contains(screen, "store/q.go:12") {
		t.Errorf("screen:\n%s", screen)
	}
	checkLegend(t, screen)

	// A clean scan closes the issue.
	run(runSecurityRun, "", p)
	_, claim = run(runRunnerNext, "")
	json.Unmarshal([]byte(claim), &claimed)
	if code, out := run(runRunnerResults, `{"version":"2.1.0","runs":[]}`, itoa(claimed.Data.ID)); code != protocol.ExitOK {
		t.Fatalf("clean results: %d %s", code, out)
	}
	if closed, _ := st.IssueByNumber(repo.ID, scan.IssueNumber); closed.State != "closed" {
		t.Errorf("issue state after a clean scan = %q", closed.State)
	}

	// A job of the repository's own may not upload.
	other, err := st.CreateBuild(repo.ID, "unit", claimed.Data.SHA, "main", "[]", "", "", true)
	if err != nil {
		t.Fatal(err)
	}
	if code, out := run(runRunnerResults, testSARIF, itoa(other)); code != protocol.ExitDenied {
		t.Errorf("results from another job: %d %s", code, out)
	}
}

// scopeRepo commits files to a bare repository and returns its directory
// and the commit.
func scopeRepo(t *testing.T, files map[string]string) (string, string) {
	t.Helper()
	root := t.TempDir()
	src, dir := filepath.Join(root, "src"), filepath.Join(root, "bare.git")
	git := func(cwd string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir, cmd.Env = cwd, gitTestEnv()
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	for name, body := range files {
		os.MkdirAll(filepath.Join(src, filepath.Dir(name)), 0o755)
		os.WriteFile(filepath.Join(src, name), []byte(body), 0o644)
	}
	git(root, "init", "-q", "-b", "main", "src")
	git(src, "add", ".")
	git(src, "commit", "-q", "-m", "first")
	git(root, "clone", "-q", "--bare", src, dir)
	return dir, git(dir, "rev-parse", "HEAD")
}

func TestScanScope(t *testing.T) {
	dir, sha := scopeRepo(t, map[string]string{
		"requirements.txt":            "flask>=3\n",
		"web/package.json":            `{"dependencies":{"react":"^19"}}`,
		"node_modules/x/package.json": `{"dependencies":{"jquery":"3"}}`,
		".gitattributes":              "Sources/Vendored/** linguist-vendored\n*.min.js linguist-generated\ndocs/** -linguist-vendored\n",
		"app.py":                      "",
	})
	repo := store.Repo{Settings: store.RepoSettings{SecurityIgnore: []string{"testdata/**"},
		SecurityExcludeRules: []string{"swift.lang.crypto.insecure-random"}}}
	f := func(rule, path string) store.SecurityFinding {
		return store.SecurityFinding{Rule: "opt.opengrep-rules." + rule, Path: path}
	}
	in := []store.SecurityFinding{
		f("python.flask.security.xss.x", "app.py"),                         // flask is used
		f("python.django.security.django-no-csrf-token", "t.html"),         // django is not
		f("javascript.jquery.security.audit.prohibit-jquery-html", "a.ts"), // only node_modules has it
		f("typescript.react.security.audit.x", "web/a.tsx"),                // web/package.json
		f("typescript.react.portability.i18next.jsx-not-internationalized", "web/a.tsx"),
		f("yaml.semgrep.missing-language-field", "rules.yaml"),
		f("python.lang.security.audit.x", "testdata/a.py"),
		f("swift.lang.crypto.insecure-random", "A.swift"),
		f("c.lang.security.insecure-use-memset", "Sources/Vendored/a.c"),
		f("javascript.browser.security.insecure-innerhtml", "lib/x.min.js"),
		f("javascript.browser.security.insecure-innerhtml", "docs/x.js"),
	}
	kept, aside, truncated := scanScope(dir, repo, sha).Apply(in)
	var got []string
	for _, k := range kept {
		got = append(got, k.Path)
	}
	if strings.Join(got, " ") != "app.py web/a.tsx docs/x.js" || truncated {
		t.Errorf("kept %v truncated %v", got, truncated)
	}
	if aside.Ignored != 4 || aside.Unused["django"] != 1 || aside.Unused["jquery"] != 1 ||
		aside.Unused["i18next"] != 1 || aside.Unused["semgrep"] != 1 || len(aside.Unused) != 4 {
		t.Errorf("aside = %+v", aside)
	}

	// A repository that authors rules keeps the rule lints.
	dir, sha = scopeRepo(t, map[string]string{".semgrep/rules.yaml": "rules: []\n"})
	if kept, _, _ := scanScope(dir, store.Repo{}, sha).Apply(in[5:6]); len(kept) != 1 {
		t.Errorf("rule lints set aside in a repository with .semgrep")
	}
}

const twoFindings = `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"opengrep"}},"results":[
	{"ruleId":"go.lang.security.audit.sqli","message":{"text":"SQL built from input"},
	 "locations":[{"physicalLocation":{"artifactLocation":{"uri":"store/q.go"},"region":{"startLine":12}}}]},
	{"ruleId":"go.lang.security.audit.sqli","message":{"text":"SQL built from input"},
	 "locations":[{"physicalLocation":{"artifactLocation":{"uri":"store/q.go"},"region":{"startLine":40}}}]}]}]}`

// Dismissing takes a finding out of the issue and closes it once none are
// open; reopening brings it back.
func TestSecurityDismiss(t *testing.T) {
	st, repo, u, root := browseFixture(t)
	p := repo.Path()
	sec := config.Security{Image: "localhost/gitbay-security:1", Schedule: "0 4 * * *", Steps: config.DefaultSecuritySteps}
	run := func(f func(*Ctx, []string) int, stdin string, args ...string) (int, string) {
		t.Helper()
		c, out := runnerCtx(st, u.ID, root)
		c.Cfg.Security = sec
		c.Stdin = strings.NewReader(stdin)
		c.JSON = true
		return f(c, args), out.String()
	}
	run(runSecurityEnable, "", p)
	run(runSecurityRun, "", p)
	_, claim := run(runRunnerNext, "")
	var claimed struct {
		Data struct {
			ID int64 `json:"id"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(claim), &claimed)
	if code, out := run(runRunnerResults, twoFindings, itoa(claimed.Data.ID)); code != protocol.ExitOK {
		t.Fatalf("results: %d %s", code, out)
	}
	scan, _ := st.SecurityScanFor(repo.ID)
	findings, _ := st.ReportedFindings(repo.ID)
	if len(findings) != 2 {
		t.Fatalf("two hits of one rule in one file stored as %d", len(findings))
	}
	issue, _ := st.IssueByNumber(repo.ID, scan.IssueNumber)
	first, second := codescan.ByPlace(findings)[0].Fingerprint, codescan.ByPlace(findings)[1].Fingerprint
	if !strings.Contains(issue.Body, codescan.Short(first)) || !strings.Contains(issue.Body, "repo security dismiss "+p) {
		t.Fatalf("issue:\n%s", issue.Body)
	}

	dismiss := func(args ...string) (int, string) {
		t.Helper()
		var out strings.Builder
		c, buf := runnerCtx(st, u.ID, root)
		code := runSecurityDismiss(c, args)
		out.WriteString(buf.String())
		return code, out.String()
	}
	if code, out := dismiss(p, codescan.Short(first), "--reason", "nope"); code != protocol.ExitUsage {
		t.Errorf("bad reason: %d %s", code, out)
	}
	if code, out := dismiss(p, first[:4], "--reason", "test"); code != protocol.ExitNotFound || !strings.Contains(out, "too short") {
		t.Errorf("short prefix: %d %s", code, out)
	}
	if code, out := dismiss(p, codescan.Short(first), "--reason", "false-positive"); code != protocol.ExitOK {
		t.Fatalf("dismiss: %d %s", code, out)
	}
	issue, _ = st.IssueByNumber(repo.ID, scan.IssueNumber)
	if strings.Contains(issue.Body, codescan.Short(first)) || !strings.Contains(issue.Body, codescan.Short(second)) ||
		!strings.Contains(issue.Body, "1 dismissed") || issue.State != "open" {
		t.Errorf("after one dismissal (%s):\n%s", issue.State, issue.Body)
	}

	var status struct {
		Data SecurityOut `json:"data"`
	}
	json.Unmarshal([]byte(dispatchIn(t, st, u, root, "", "repo", "security", "status", p, "--json")), &status)
	if len(status.Data.Findings) != 1 || len(status.Data.Dismissed) != 1 ||
		status.Data.Dismissed[0].Reason != "false-positive" || status.Data.Dismissed[0].DismissedBy != u.Username {
		t.Errorf("status = %+v", status.Data)
	}
	screen := atTerminalIn(t, st, u, root, "repo", "security", "status", p)
	if !strings.Contains(screen, "Dismissed (1)") {
		t.Errorf("screen:\n%s", screen)
	}
	checkLegend(t, screen)

	// The same set from the next scan leaves the issue alone.
	run(runSecurityRun, "", p)
	_, claim = run(runRunnerNext, "")
	json.Unmarshal([]byte(claim), &claimed)
	run(runRunnerResults, twoFindings, itoa(claimed.Data.ID))
	if again, _ := st.IssueByNumber(repo.ID, scan.IssueNumber); again.Body != issue.Body || again.State != "open" {
		t.Errorf("an unchanged scan rewrote the issue:\n%s", again.Body)
	}

	if code, out := dismiss(p, second, "--reason", "wont-fix"); code != protocol.ExitOK {
		t.Fatalf("dismiss second: %d %s", code, out)
	}
	if closed, _ := st.IssueByNumber(repo.ID, scan.IssueNumber); closed.State != "closed" {
		t.Errorf("issue with nothing open is %s", closed.State)
	}
	c, out := runnerCtx(st, u.ID, root)
	if code := runSecurityReopen(c, []string{p, codescan.Short(second)}); code != protocol.ExitOK {
		t.Fatalf("reopen: %d %s", code, out)
	}
	reopened, _ := st.SecurityScanFor(repo.ID)
	if reopened.IssueNumber == scan.IssueNumber || reopened.IssueNumber == 0 {
		t.Errorf("reopening a finding should open a fresh issue, got #%d", reopened.IssueNumber)
	}
}
