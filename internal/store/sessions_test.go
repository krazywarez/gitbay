package store

import (
	"database/sql"
	"testing"
	"time"
)

func TestCountLoginTokensSince(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		_, hash, err := NewToken()
		if err != nil {
			t.Fatal(err)
		}
		if err := s.CreateLoginToken(uid, hash, time.Minute); err != nil {
			t.Fatal(err)
		}
	}

	n, err := s.CountLoginTokensSince(uid, time.Now().Add(-time.Hour))
	if err != nil || n != 3 {
		t.Fatalf("count in the last hour = %d, %v; want 3", n, err)
	}

	// A window that opens in the future sees none of them, which is what
	// makes the hourly bound a window rather than a lifetime total.
	if n, err := s.CountLoginTokensSince(uid, time.Now().Add(time.Hour)); err != nil || n != 0 {
		t.Fatalf("count in a future window = %d, %v; want 0", n, err)
	}

	// One account's requests must not spend another account's budget.
	other, err := s.CreateUser("kim", false)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := s.CountLoginTokensSince(other, time.Now().Add(-time.Hour)); err != nil || n != 0 {
		t.Fatalf("other account count = %d, %v; want 0", n, err)
	}
}

func sessionFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", false)
	if err != nil {
		t.Fatal(err)
	}
	return s, uid
}

func sessionTimes(t *testing.T, s *Store, hash string) (expires, absolute time.Time) {
	t.Helper()
	var e, a string
	if err := s.DB.QueryRow("SELECT expires_at, absolute_expires_at FROM web_sessions WHERE token_hash = ?", hash).Scan(&e, &a); err != nil {
		t.Fatal(err)
	}
	return *parseTime(sql.NullString{String: e, Valid: true}), *parseTime(sql.NullString{String: a, Valid: true})
}

func TestWebSessionIdleExpiry(t *testing.T) {
	s, uid := sessionFixture(t)
	if err := s.CreateWebSession("h", uid, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	exp, abs := sessionTimes(t, s, "h")
	if d := time.Until(exp); d < WebSessionIdle-time.Minute || d > WebSessionIdle {
		t.Fatalf("a new session expires in %s, want %s", d, WebSessionIdle)
	}
	if d := time.Until(abs); d < 7*24*time.Hour-time.Minute {
		t.Fatalf("absolute cap in %s", d)
	}
	// Idle past the window: gone.
	old := fmtTime(time.Now().Add(-time.Second))
	s.DB.Exec("UPDATE web_sessions SET expires_at = ? WHERE token_hash = 'h'", old)
	if _, err := s.WebSessionUser("h"); err != ErrNotFound {
		t.Fatalf("idle session: %v", err)
	}
}

func TestWebSessionRenewsUpToTheCap(t *testing.T) {
	s, uid := sessionFixture(t)
	if err := s.CreateWebSession("h", uid, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	// Last used two minutes ago, one minute left: a request renews it.
	s.DB.Exec("UPDATE web_sessions SET last_used_at = ?, expires_at = ? WHERE token_hash = 'h'",
		fmtTime(time.Now().Add(-2*time.Minute)), fmtTime(time.Now().Add(time.Minute)))
	if _, err := s.WebSessionUser("h"); err != nil {
		t.Fatal(err)
	}
	if exp, _ := sessionTimes(t, s, "h"); time.Until(exp) < WebSessionIdle-time.Minute {
		t.Fatalf("not renewed: expires in %s", time.Until(exp))
	}
	// Near the cap, renewal stops at it.
	capAt := time.Now().Add(time.Hour)
	s.DB.Exec("UPDATE web_sessions SET last_used_at = ?, absolute_expires_at = ? WHERE token_hash = 'h'",
		fmtTime(time.Now().Add(-2*time.Minute)), fmtTime(capAt))
	if _, err := s.WebSessionUser("h"); err != nil {
		t.Fatal(err)
	}
	if exp, _ := sessionTimes(t, s, "h"); exp.After(capAt) {
		t.Fatalf("renewed past the cap: %s > %s", exp, capAt)
	}
	list, err := s.ListWebSessions(uid)
	if err != nil || len(list) != 1 || list[0].LastUsedAt == "" {
		t.Fatalf("list: %+v %v", list, err)
	}
}

// A session's sign-in time is its creation; using the session renews
// its idle expiry and leaves the sign-in time alone (#297).
func TestWebSessionUserSignedInAt(t *testing.T) {
	s, uid := sessionFixture(t)
	_, hash, err := NewToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.CreateWebSession(hash, uid, 7*24*time.Hour); err != nil {
		t.Fatal(err)
	}
	u, err := s.WebSessionUser(hash)
	if err != nil {
		t.Fatal(err)
	}
	if age := time.Since(u.SignedInAt); age < 0 || age > time.Minute {
		t.Fatalf("fresh session signed in %v ago", age)
	}

	signedIn := time.Now().Add(-2 * time.Hour)
	if _, err := s.DB.Exec("UPDATE web_sessions SET created_at = ?, last_used_at = ? WHERE token_hash = ?",
		fmtTime(signedIn), fmtTime(signedIn), hash); err != nil {
		t.Fatal(err)
	}
	u, err = s.WebSessionUser(hash)
	if err != nil {
		t.Fatal(err)
	}
	var last string
	if err := s.DB.QueryRow("SELECT last_used_at FROM web_sessions WHERE token_hash = ?", hash).Scan(&last); err != nil {
		t.Fatal(err)
	}
	if last == fmtTime(signedIn) {
		t.Fatal("using the session did not renew it")
	}
	if want := signedIn.UTC().Truncate(time.Millisecond); !u.SignedInAt.Equal(want) {
		t.Fatalf("SignedInAt = %v, want %v", u.SignedInAt, want)
	}
}
