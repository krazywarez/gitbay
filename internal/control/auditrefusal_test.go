package control

import (
	"slices"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestRefusedWritesAreAudited(t *testing.T) {
	refusals = &refusalLimiter{seen: map[int64]*refusalWindow{}}
	st, repo, _ := newQueueTestRepo(t)
	bobID, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	bob := store.User{ID: bobID, Username: "bob"}

	c, _ := pruneCtx(st, t.TempDir(), bob)
	if code := Dispatch(c, []string{"repo", "delete", repo.Path(), "--yes"}); code != protocol.ExitDenied {
		t.Fatalf("exit %d, want %d", code, protocol.ExitDenied)
	}
	// A refused read is not a write attempt.
	c, _ = pruneCtx(st, t.TempDir(), bob)
	if code := Dispatch(c, []string{"audit"}); code != protocol.ExitDenied {
		t.Fatalf("audit: exit %d", code)
	}
	got, err := st.AuditEntries(store.AuditFilter{ActionPrefix: "refused", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Action != "refused repo delete" || got[0].Actor != "bob" {
		t.Fatalf("entries: %+v", got)
	}
}

func TestRefusalAuditIsRateLimited(t *testing.T) {
	refusals = &refusalLimiter{seen: map[int64]*refusalWindow{}}
	st, repo, _ := newQueueTestRepo(t)
	bobID, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	for range refusalsPerMinute + 5 {
		c, _ := pruneCtx(st, t.TempDir(), store.User{ID: bobID, Username: "bob"})
		Dispatch(c, []string{"repo", "delete", repo.Path(), "--yes"})
	}
	refused, _ := st.AuditEntries(store.AuditFilter{ActionPrefix: "refused ", Limit: 100})
	throttled, _ := st.AuditEntries(store.AuditFilter{ActionPrefix: "refused.throttled", Limit: 100})
	if len(refused) != refusalsPerMinute || len(throttled) != 1 {
		t.Fatalf("%d refused rows, %d throttled rows", len(refused), len(throttled))
	}
}

// The #257 refusal of a minting command under an expiring credential is
// a refused write like any other.
func TestExpiringMintRefusalIsAudited(t *testing.T) {
	refusals = &refusalLimiter{seen: map[int64]*refusalWindow{}}
	st, _, _ := newQueueTestRepo(t)
	bobID, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	exp := time.Now().Add(time.Hour)
	c, _ := pruneCtx(st, t.TempDir(), store.User{ID: bobID, Username: "bob"})
	c.Expires = &exp
	if code := Dispatch(c, []string{"token", "create", "--name", "x"}); code != protocol.ExitDenied {
		t.Fatalf("exit %d, want %d", code, protocol.ExitDenied)
	}
	got, err := st.AuditEntries(store.AuditFilter{ActionPrefix: "refused ", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Action != "refused token create" {
		t.Fatalf("entries: %+v", got)
	}
}

// A gate refuses before parseFlags, so the row must not keep a value
// glued to its flag, a value that looks like a flag, or a positional
// past the target.
func TestRefusalRowKeepsNoValues(t *testing.T) {
	refusals = &refusalLimiter{seen: map[int64]*refusalWindow{}}
	st, repo, _ := newQueueTestRepo(t)
	bobID, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, argv := range [][]string{
		{"issue", "create", repo.Path(), "--body=hunter2"},
		{"issue", "create", repo.Path(), "--title", "--body=hunter2"},
		{"repo", "secret", "set", repo.Path(), "NAME", "hunter2"},
	} {
		c, _ := pruneCtx(st, t.TempDir(), store.User{ID: bobID, Username: "bob"})
		c.ReadOnly = true
		if code := Dispatch(c, argv); code != protocol.ExitDenied {
			t.Fatalf("%q: exit %d, want %d", argv, code, protocol.ExitDenied)
		}
	}
	got, err := st.AuditEntries(store.AuditFilter{ActionPrefix: "refused ", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("entries: %+v", got)
	}
	for _, e := range got {
		if strings.Contains(e.Data, "hunter2") || strings.Contains(e.Data, "NAME") {
			t.Errorf("%s kept a value: %s", e.Action, e.Data)
		}
		if !strings.Contains(e.Data, repo.Path()) {
			t.Errorf("%s lost its target: %s", e.Action, e.Data)
		}
	}
}

func TestRefusalArgs(t *testing.T) {
	for _, tc := range []struct{ in, want []string }{
		{[]string{"o/r", "NAME", "value"}, []string{"o/r"}},
		{[]string{"o/r", "--body=x", "--title", "--label=y"}, []string{"o/r", "--body", "--title", "--label"}},
		{[]string{"o/r", "--title", "t", "--", "a", "b"}, []string{"o/r", "--title"}},
	} {
		if got := refusalArgs(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("refusalArgs(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestRefusalLimiterWindowResets(t *testing.T) {
	l := &refusalLimiter{seen: map[int64]*refusalWindow{}}
	now := time.Unix(1_000_000, 0)
	for i := range refusalsPerMinute {
		if v := l.allow(1, now); v != refusalRecord {
			t.Fatalf("refusal %d: %d", i, v)
		}
	}
	if v := l.allow(1, now); v != refusalThrottleActor {
		t.Fatalf("first past the limit: %d", v)
	}
	if v := l.allow(1, now.Add(59*time.Second)); v != refusalDrop {
		t.Fatalf("second past the limit: %d", v)
	}
	if v := l.allow(2, now); v != refusalRecord {
		t.Fatalf("another actor: %d", v)
	}
	if v := l.allow(1, now.Add(time.Minute)); v != refusalRecord {
		t.Fatalf("next minute: %d", v)
	}
}

func TestRefusalLimiterGlobalCeiling(t *testing.T) {
	l := &refusalLimiter{seen: map[int64]*refusalWindow{}}
	now := time.Unix(1_000_000, 0)
	var rec, global int
	for actor := range int64(refusalsPerMinuteGlobal/refusalsPerMinute + 10) {
		for range refusalsPerMinute {
			switch l.allow(actor, now) {
			case refusalRecord:
				rec++
			case refusalThrottleGlobal:
				global++
			case refusalThrottleActor:
				t.Fatal("actor throttled under its own limit")
			}
		}
	}
	if rec != refusalsPerMinuteGlobal || global != 1 {
		t.Fatalf("%d recorded, %d global throttle rows", rec, global)
	}
	if v := l.allow(9999, now.Add(time.Minute)); v != refusalRecord {
		t.Fatalf("next minute: %d", v)
	}
}

func TestRefusalLimiterPrunes(t *testing.T) {
	l := &refusalLimiter{seen: map[int64]*refusalWindow{}}
	now := time.Unix(1_000_000, 0)
	for actor := range int64(4097) {
		l.seen[actor] = &refusalWindow{start: now, n: 1}
	}
	l.allow(5000, now.Add(time.Minute))
	if len(l.seen) != 1 {
		t.Fatalf("%d windows after prune, want 1", len(l.seen))
	}
	for actor := range int64(4097) {
		l.seen[actor] = &refusalWindow{start: now.Add(time.Minute), n: 1}
	}
	l.allow(6000, now.Add(time.Minute+time.Second))
	if len(l.seen) != 4099 {
		t.Fatalf("%d windows, want 4099: a live window was pruned", len(l.seen))
	}
}
