package control

import (
	"encoding/json"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/ci"
	"gitbay.org/gitbay/internal/codescan"
	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
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
