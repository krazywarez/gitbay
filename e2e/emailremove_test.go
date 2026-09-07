package e2e

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// An address can be removed and the primary moved, within the rules the
// store holds: the primary stays until another is primary, and the last
// verified address stays (#181).
func TestEmailRemoveAndPrimary(t *testing.T) {
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n", smtp.addr))
	key := inst.newKey(t, "gus")
	inst.admin(t, "admin", "user", "create", "gus",
		"--key", key+".pub", "--email", "gus@primary.test", "--verified")
	for _, a := range []string{"typo@example.test", "gus@next.test"} {
		if _, errOut, code := inst.ssh(t, key, "", "email", "add", a); code != 0 {
			t.Fatalf("email add %s: %s", a, errOut)
		}
	}

	if _, errOut, code := inst.ssh(t, key, "", "email", "remove", "gus@primary.test"); code != 4 || !strings.Contains(errOut, "primary") {
		t.Fatalf("removing the primary: exit %d %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, key, "", "email", "remove", "nobody@example.test"); code != 3 {
		t.Fatalf("removing an absent address: exit %d %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, key, "", "email", "primary", "gus@next.test"); code != 4 || !strings.Contains(errOut, "not verified") {
		t.Fatalf("unverified address as primary: exit %d %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, key, "", "email", "remove", "typo@example.test"); code != 0 {
		t.Fatalf("removing an unverified address: exit %d %s", code, errOut)
	}

	inst.admin(t, "admin", "email", "verify", "gus", "gus@next.test")
	if _, errOut, code := inst.ssh(t, key, "", "email", "primary", "gus@next.test"); code != 0 {
		t.Fatalf("email primary: exit %d %s", code, errOut)
	}
	if _, errOut, code := inst.ssh(t, key, "", "email", "remove", "gus@primary.test"); code != 0 {
		t.Fatalf("removing the former primary: exit %d %s", code, errOut)
	}

	out, errOut, code := inst.ssh(t, key, "", "email", "list", "--json")
	if code != 0 {
		t.Fatalf("email list: exit %d %s", code, errOut)
	}
	var env struct {
		Data []struct {
			Address  string `json:"address"`
			Verified bool   `json:"verified"`
			Primary  bool   `json:"primary"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("email list --json: %v\n%s", err, out)
	}
	if len(env.Data) != 1 || env.Data[0].Address != "gus@next.test" || !env.Data[0].Verified || !env.Data[0].Primary {
		t.Fatalf("addresses after the changes: %+v", env.Data)
	}
}
