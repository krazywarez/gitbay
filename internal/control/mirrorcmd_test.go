package control

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
)

// mirror add refuses a host sync would refuse as numeric, on an
// instance that allows local targets or not, and stores nothing (#298).
func TestMirrorAddRefusesNumericHost(t *testing.T) {
	for _, allowLocal := range []bool{true, false} {
		for _, host := range []string{"127.1", "2130706433", "0x7f.1"} {
			c, errOut, st, _ := importCtx(t, allowLocal)
			code := Dispatch(c, []string{"repo", "mirror", "add", "alice/app", "https://" + host + "/x.git", "--direction", "pull"})
			if code != protocol.ExitUsage || !strings.Contains(errOut.String(), "numeric address") {
				t.Fatalf("%s (allow_local %v): exit %d, %q", host, allowLocal, code, errOut.String())
			}
			repo, err := st.RepoByPath("alice/app")
			if err != nil {
				t.Fatal(err)
			}
			if ms, err := st.ListMirrors(repo.ID); err != nil || len(ms) != 0 {
				t.Fatalf("%s: mirrors %v, %v", host, ms, err)
			}
		}
	}
}
