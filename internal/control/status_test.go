package control

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// ci/<job> statuses are the build subsystem's. A writer who could post
// one could mark ci/test green on their own head before, or instead of,
// the build (#258).
func TestStatusSetRefusesReservedContext(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	for _, ctx := range []string{"ci/test", "CI/test", "ci/"} {
		c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice"})
		code := Dispatch(c, []string{"status", "set", repo.Path(), "abc1234", "--context", ctx, "--state", "success"})
		if code != protocol.ExitDenied || !strings.Contains(errOut.String(), "reserved") {
			t.Errorf("--context %s: exit %d, %s", ctx, code, errOut.String())
		}
	}
	if has, err := st.RepoHasStatuses(repo.ID); err != nil || has {
		t.Fatalf("a refused status was stored: %v %v", has, err)
	}
}
