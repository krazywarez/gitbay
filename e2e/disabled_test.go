package e2e

import (
	"encoding/json"
	"testing"
)

// Disabling an account ends every way in, not just SSH. The API used to
// keep answering a disabled account's bearer token, because only the SSH
// listener checked the flag and disabling deleted sessions but not tokens
// (#95).
func TestDisabledAccountAPI(t *testing.T) {
	inst := startInstanceWith(t, "[api]\nenabled = true\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	out, errOut, code := inst.ssh(t, aliceKey, "", "token", "create", "--name", "ci", "--json")
	if code != 0 {
		t.Fatalf("token create: %s", errOut)
	}
	var env struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("token create output: %v %s", err, out)
	}
	token := env.Data.Token
	if status, _ := inst.apiCall(t, token, []string{"whoami"}, ""); status != 200 {
		t.Fatalf("token before disable: %d", status)
	}

	inst.admin(t, "admin", "user", "disable", "alice")
	if status, _ := inst.apiCall(t, token, []string{"whoami"}, ""); status != 401 {
		t.Fatalf("disabled account's token still answers: %d, want 401", status)
	}
	if status, _ := inst.apiCall(t, token, []string{"repo", "create", "alice/late"}, ""); status != 401 {
		t.Fatalf("disabled account's token still writes: %d, want 401", status)
	}

	// Re-enabling restores the account, not the token: it was revoked.
	inst.admin(t, "admin", "user", "enable", "alice")
	if status, _ := inst.apiCall(t, token, []string{"whoami"}, ""); status != 401 {
		t.Fatalf("revoked token answers after enable: %d, want 401", status)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "whoami"); code != 0 {
		t.Fatalf("re-enabled account refused over ssh: exit %d", code)
	}
}
