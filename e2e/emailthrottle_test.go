package e2e

import (
	"fmt"
	"strings"
	"testing"
)

// email add mails a verification code. SSH auth and registration are
// rate-limited; this path was not, so an authenticated account could
// enqueue mail without bound (#136).
func TestEmailAddThrottled(t *testing.T) {
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n", smtp.addr))
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	for i := 0; i < 5; i++ {
		if _, errOut, code := inst.ssh(t, aliceKey, "", "email", "add", fmt.Sprintf("alice%d@example.test", i)); code != 0 {
			t.Fatalf("email add %d: exit %d %s", i, code, errOut)
		}
	}
	_, errOut, code := inst.ssh(t, aliceKey, "", "email", "add", "alice5@example.test")
	if code != 4 || !strings.Contains(errOut, "last hour") {
		t.Fatalf("sixth email add in an hour: exit %d %s", code, errOut)
	}
}
