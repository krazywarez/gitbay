package hookd

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/leaks"
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/store"
)

func noScan() leaks.Report { return leaks.Report{} }

// The hook scans; the daemon decides. Each mode is the same scan answered
// three ways, and the allowlist and a failed scan change the answer.
func TestPreReceiveSecretScan(t *testing.T) {
	sock, st, repoID, uid := serveSocket(t)
	token, err := st.CreatePushToken(repoID, uid, "full")
	if err != nil {
		t.Fatal(err)
	}
	req := Request{Hook: "pre-receive", RepoID: repoID, UserID: uid, Scope: "full", Token: token,
		Updates: []policy.RefUpdate{{Old: strings.Repeat("0", 40), New: strings.Repeat("a", 40), Ref: "refs/heads/main"}}}
	finding := leaks.Report{Blobs: 3, Findings: []leaks.Finding{
		{Commit: strings.Repeat("b", 40), Path: "config/prod.env", Line: 4, Rule: "aws-access-key-id"}}}
	set := func(mode string, allow ...string) {
		t.Helper()
		if _, err := st.UpdateRepoSettings(repoID, func(s *store.RepoSettings) {
			s.SecretScan, s.SecretScanAllow = mode, allow
		}); err != nil {
			t.Fatal(err)
		}
	}
	ask := func(rep leaks.Report) (Response, bool) {
		t.Helper()
		scanned := false
		resp, err := Ask(sock, req, nil, func() leaks.Report { scanned = true; return rep })
		if err != nil {
			t.Fatal(err)
		}
		return resp, scanned
	}

	// The default is warn: accepted, with the finding for the pusher.
	resp, scanned := ask(finding)
	if !scanned || !resp.Allow || !strings.Contains(resp.Message, "bbbbbbbbbb config/prod.env:4 aws-access-key-id") {
		t.Fatalf("warn: scanned=%v %+v", scanned, resp)
	}
	if strings.Contains(resp.Message, "AKIA") {
		t.Error("message carries a matched value")
	}

	set(leaks.ModeBlock)
	if resp, _ := ask(finding); resp.Allow || !strings.Contains(resp.Message, "refused, 1 possible secret") {
		t.Fatalf("block: %+v", resp)
	}
	if resp, _ := ask(leaks.Report{Error: "git log: exit 128"}); resp.Allow {
		t.Fatalf("block on a failed scan: %+v", resp)
	}
	if resp, _ := ask(noScan()); !resp.Allow || resp.Message != "" {
		t.Fatalf("block, clean: %+v", resp)
	}

	set(leaks.ModeBlock, "config/**")
	if resp, _ := ask(finding); !resp.Allow || resp.Message != "" {
		t.Fatalf("allowlisted: %+v", resp)
	}

	set(leaks.ModeWarn)
	if resp, _ := ask(leaks.Report{Error: "git log: exit 128"}); !resp.Allow || !strings.Contains(resp.Message, "secret scan failed") {
		t.Fatalf("warn on a failed scan: %+v", resp)
	}

	set(leaks.ModeOff)
	if resp, scanned := ask(finding); scanned || !resp.Allow {
		t.Fatalf("off: scanned=%v %+v", scanned, resp)
	}

	rows, err := st.AuditEntries(store.AuditFilter{ActionPrefix: "push.secrets", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Errorf("warned pushes audited %d times, want 1", len(rows))
	}
}
