package e2e

import (
	"encoding/json"
	"strings"
	"testing"
)

// issueNumbers runs issue list with the given flags and returns the numbers.
func issueNumbers(t *testing.T, inst *instance, key string, args ...string) []int64 {
	t.Helper()
	out, errOut, code := inst.ssh(t, key, "", append([]string{"issue", "list"}, append(args, "--json")...)...)
	if code != 0 {
		t.Fatalf("issue list: %s", errOut)
	}
	var env struct {
		Data []struct {
			Number int64 `json:"number"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out)
	}
	var ns []int64
	for _, d := range env.Data {
		ns = append(ns, d.Number)
	}
	return ns
}

func TestIssueSearch(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	mk := func(title, body string) {
		t.Helper()
		if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app",
			"--title", "'"+title+"'", "--body", "'"+body+"'"); code != 0 {
			t.Fatalf("issue create: %s", errOut)
		}
	}
	mk("memory leak in the parser", "resident size climbs forever")
	mk("flaky test", "the runner times out sometimes")
	mk("docs typo", "spelled parser wrong")

	// Title match.
	if got := issueNumbers(t, inst, aliceKey, "alice/app", "--search", "leak"); len(got) != 1 || got[0] != 1 {
		t.Fatalf("title search = %v, want [1]", got)
	}
	// Body match — the half a title-only search could never reach.
	if got := issueNumbers(t, inst, aliceKey, "alice/app", "--search", "climbs"); len(got) != 1 || got[0] != 1 {
		t.Fatalf("body search = %v, want [1]", got)
	}
	// One word in two issues, one in a title and one in a body.
	got := issueNumbers(t, inst, aliceKey, "alice/app", "--search", "parser")
	if len(got) != 2 {
		t.Fatalf("search across title and body = %v, want two issues", got)
	}
	// Terms are ANDed, not ORed.
	if got := issueNumbers(t, inst, aliceKey, "alice/app", "--search", "'parser runner'"); len(got) != 0 {
		t.Fatalf("terms ORed rather than ANDed: %v", got)
	}
	// Search composes with the other filters.
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "close", "alice/app", "1"); code != 0 {
		t.Fatal("close failed")
	}
	if got := issueNumbers(t, inst, aliceKey, "alice/app", "--search", "parser"); len(got) != 1 || got[0] != 3 {
		t.Fatalf("search ignored --state open: %v", got)
	}
	if got := issueNumbers(t, inst, aliceKey, "alice/app", "--search", "parser", "--state", "all"); len(got) != 2 {
		t.Fatalf("--state all with search = %v", got)
	}

	// An edit moves the issue in the index rather than leaving the old
	// words matching.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "edit", "alice/app", "2",
		"--title", "'renamed entirely'", "--body", "'nothing of the original remains'"); code != 0 {
		t.Fatalf("issue edit: %s", errOut)
	}
	if got := issueNumbers(t, inst, aliceKey, "alice/app", "--search", "flaky", "--state", "all"); len(got) != 0 {
		t.Fatalf("edited-away words still match: %v", got)
	}
	if got := issueNumbers(t, inst, aliceKey, "alice/app", "--search", "renamed", "--state", "all"); len(got) != 1 {
		t.Fatalf("new words not indexed: %v", got)
	}

	// FTS5 operators typed by a person are terms, not syntax errors.
	// Single characters are refused for length before they get this far,
	// which the "x" case below covers.
	for _, q := range []string{"c++", "AND", "NOT", "foo:", "a AND", "b*", "()"} {
		if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "list", "alice/app",
			"--search", "'"+q+"'", "--json"); code != 0 {
			t.Fatalf("search %q failed: %s", q, errOut)
		}
	}
	// Too short is a usage error, the same as every other query.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "list", "alice/app", "--search", "x"); code != 2 ||
		!strings.Contains(errOut, "2 to 200 characters") {
		t.Fatalf("short search: exit %d, %s", code, errOut)
	}

	// The instance-wide search reaches bodies too now.
	out, errOut, code := inst.ssh(t, aliceKey, "", "search", "climbs", "--json")
	if code != 0 {
		t.Fatalf("search: %s", errOut)
	}
	if !strings.Contains(out, "alice/app") || !strings.Contains(out, "memory leak") {
		t.Fatalf("global body search: %s", out)
	}
}
