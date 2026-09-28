package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// An expiring token cannot mint a credential that outlives it, and
// revoking a token can take what it created with it (#257).
func TestTokenDelegation(t *testing.T) {
	t.Parallel()
	inst := startInstanceWith(t, "[api]\nenabled = true\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	mint := func(args ...string) (token, scope string) {
		t.Helper()
		out, errOut, code := inst.ssh(t, aliceKey, "", append([]string{"token", "create", "--json"}, args...)...)
		if code != 0 {
			t.Fatalf("token create %v: %s", args, errOut)
		}
		var env struct {
			Data struct {
				Token string `json:"token"`
				Scope string `json:"scope"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(out), &env); err != nil {
			t.Fatalf("token create output: %v %s", err, out)
		}
		return env.Data.Token, env.Data.Scope
	}
	if _, scope := mint("--name", "plain"); scope != "read" {
		t.Fatalf("default scope %q, want read", scope)
	}
	brief, _ := mint("--name", "brief", "--scope", "full", "--ttl", "1h")
	lasting, _ := mint("--name", "lasting", "--scope", "full")

	spare := inst.newKey(t, "spare")
	pub, err := os.ReadFile(spare + ".pub")
	if err != nil {
		t.Fatal(err)
	}
	status, body := inst.apiCall(t, brief, []string{"keys", "add"}, string(pub))
	if status != 403 || !strings.Contains(fmt.Sprint(body["error"]), "expires") {
		t.Fatalf("expiring token added a key: %d %v", status, body)
	}
	if status, _ := inst.apiCall(t, brief, []string{"whoami"}, ""); status != 200 {
		t.Fatalf("expiring token refused a read: %d", status)
	}
	if status, body := inst.apiCall(t, lasting, []string{"keys", "add"}, string(pub)); status != 200 {
		t.Fatalf("keys add: %d %v", status, body)
	}
	if status, body := inst.apiCall(t, lasting, []string{"token", "create", "--name", "child"}, ""); status != 200 {
		t.Fatalf("token create: %d %v", status, body)
	}
	if _, errOut, code := inst.ssh(t, spare, "", "whoami"); code != 0 {
		t.Fatalf("the added key does not work: %s", errOut)
	}

	out, errOut, code := inst.ssh(t, aliceKey, "", "token", "revoke", "lasting", "--created")
	if code != 0 || !strings.Contains(out, "token child") || !strings.Contains(out, fingerprint(t, spare+".pub")) {
		t.Fatalf("revoke --created: exit %d\n%s%s", code, out, errOut)
	}
	if _, _, code := inst.ssh(t, spare, "", "whoami"); code == 0 {
		t.Fatal("a key the revoked token created still works")
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "token", "list"); strings.Contains(out, "child") {
		t.Fatalf("the child token survived:\n%s", out)
	}
}
