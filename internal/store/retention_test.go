package store

import (
	"testing"
	"time"
)

func retentionFixture(t *testing.T) (*Store, int64) {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	return s, uid
}

func TestSweepRemovesExpiredSessionsAndTokens(t *testing.T) {
	s, uid := retentionFixture(t)
	if err := s.CreateWebSession("live", uid, time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateWebSession("dead", uid, -time.Hour); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateLoginToken(uid, "stale", -time.Minute); err != nil {
		t.Fatal(err)
	}

	// Retention is unset: expired rows still go, since nothing keeps them
	// meaningful once they cannot authenticate.
	got, err := s.Sweep(Retention{}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got["web_sessions"] != 1 || got["login_tokens"] != 1 {
		t.Fatalf("swept %v", got)
	}
	var n int
	s.DB.QueryRow("SELECT COUNT(*) FROM web_sessions").Scan(&n)
	if n != 1 {
		t.Fatalf("%d sessions remain, want the live one", n)
	}
	if _, err := s.WebSessionUser("live"); err != nil {
		t.Fatalf("live session swept: %v", err)
	}
}

// Zero retention keeps forever: deleting an audit trail is not a default.
func TestSweepKeepsWhenUnconfigured(t *testing.T) {
	s, uid := retentionFixture(t)
	s.Audit(uid, "cmd repo create", map[string]any{"argv": []string{"a/b"}})
	s.DB.Exec("UPDATE audit_log SET created_at = '2020-01-01T00:00:00.000Z'")

	if _, err := s.Sweep(Retention{}, time.Now()); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&n)
	if n != 1 {
		t.Fatalf("audit row swept with retention unset (%d rows)", n)
	}

	if _, err := s.Sweep(Retention{Audit: 24 * time.Hour}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.DB.QueryRow("SELECT COUNT(*) FROM audit_log").Scan(&n)
	if n != 0 {
		t.Fatalf("audit row survived its retention (%d rows)", n)
	}
}

// A delivery still being retried is live state, however old its first
// attempt; only finished ones age out.
func TestSweepKeepsUnfinishedWork(t *testing.T) {
	s, uid := retentionFixture(t)
	repoID, err := s.CreateRepo("user", uid, "lib", "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddWebhook(repoID, "https://example.test/h", "", "*"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := s.RecordEvent(repoID, uid, "push", "{}"); err != nil {
			t.Fatal(err)
		}
	}
	s.DB.Exec("UPDATE webhook_deliveries SET created_at = '2020-01-01T00:00:00.000Z'")
	s.DB.Exec("UPDATE webhook_deliveries SET delivered_at = '2020-01-02T00:00:00.000Z' WHERE id = 1")
	s.DB.Exec("UPDATE webhook_deliveries SET failed_at = '2020-01-02T00:00:00.000Z' WHERE id = 2")

	got, err := s.Sweep(Retention{WebhookDeliveries: time.Hour}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got["webhook_deliveries"] != 2 {
		t.Fatalf("swept %v, want the delivered and the failed one", got)
	}
	var n int
	s.DB.QueryRow("SELECT COUNT(*) FROM webhook_deliveries").Scan(&n)
	if n != 1 {
		t.Fatalf("%d deliveries remain, want the one still being retried", n)
	}
}

// events.id is the parent of webhook_deliveries.event_id under ON DELETE
// CASCADE, so sweeping events would take a queued delivery with it. The
// sweep skips any event that still has one.
func TestSweepEventsSpareQueuedDeliveries(t *testing.T) {
	s, uid := retentionFixture(t)
	repoID, err := s.CreateRepo("user", uid, "lib", "public")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.AddWebhook(repoID, "https://example.test/h", "", "*"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordEvent(repoID, uid, "push", "{}"); err != nil {
		t.Fatal(err)
	}
	s.DB.Exec("UPDATE events SET created_at = '2020-01-01T00:00:00.000Z'")
	s.DB.Exec("UPDATE webhook_deliveries SET created_at = '2020-01-01T00:00:00.000Z'")

	// The delivery has neither delivered_at nor failed_at: still queued.
	got, err := s.Sweep(Retention{Events: time.Hour, WebhookDeliveries: time.Hour}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if got["events"] != 0 || got["webhook_deliveries"] != 0 {
		t.Fatalf("swept %v, want nothing while the delivery is queued", got)
	}
	var n int
	s.DB.QueryRow("SELECT COUNT(*) FROM webhook_deliveries").Scan(&n)
	if n != 1 {
		t.Fatal("queued delivery cascaded away with its event")
	}

	// Once it finishes, both go.
	s.DB.Exec("UPDATE webhook_deliveries SET delivered_at = '2020-01-02T00:00:00.000Z'")
	if _, err := s.Sweep(Retention{Events: time.Hour, WebhookDeliveries: time.Hour}, time.Now()); err != nil {
		t.Fatal(err)
	}
	s.DB.QueryRow("SELECT COUNT(*) FROM events").Scan(&n)
	if n != 0 {
		t.Fatalf("%d events remain after the delivery finished", n)
	}
}
