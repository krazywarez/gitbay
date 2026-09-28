package store

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

func chainStore(t *testing.T) *Store {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestAuditChainIntact(t *testing.T) {
	s := chainStore(t)
	s.Audit(0, "a", map[string]any{"n": 1})
	s.Audit(0, "b", nil)
	s.Audit(0, "c", map[string]any{"n": 3})
	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows != 3 || res.BrokenAt != 0 || res.First != 1 || res.Last != 3 || len(res.LastHash) != 64 {
		t.Fatalf("%+v", res)
	}
}

// Concurrent writers each read the last hash and insert in one
// transaction; none may chain to a predecessor another already took.
func TestAuditChainConcurrentWriters(t *testing.T) {
	s := chainStore(t)
	const writers, each = 8, 25
	var wg sync.WaitGroup
	for w := range writers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range each {
				s.Audit(0, fmt.Sprintf("w%d-%d", w, i), nil)
			}
		}()
	}
	wg.Wait()
	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatal(err)
	}
	if res.Rows != writers*each || res.Unchained != 0 || res.BrokenAt != 0 {
		t.Fatalf("%+v", res)
	}
}

func TestAuditChainDetectsAnEditedRow(t *testing.T) {
	for _, set := range []string{
		"action = 'x'",
		"created_at = '2020-01-01T00:00:00.000Z'",
		`data_json = '{"n":2}'`,
		"actor_ref = 7",
	} {
		t.Run(set, func(t *testing.T) {
			s := chainStore(t)
			for _, a := range []string{"a", "b", "c"} {
				s.Audit(0, a, nil)
			}
			if _, err := s.DB.Exec("UPDATE audit_log SET " + set + " WHERE id = 2"); err != nil {
				t.Fatal(err)
			}
			res, err := s.VerifyAuditChain()
			if err != nil {
				t.Fatal(err)
			}
			if res.BrokenAt != 2 || !strings.Contains(res.Reason, "contents") {
				t.Fatalf("%+v", res)
			}
		})
	}
}

// Blanking the hash of the oldest chained rows would make them read as
// rows from before the migration.
func TestAuditChainDetectsBlankedHashes(t *testing.T) {
	s := chainStore(t)
	for _, a := range []string{"a", "b", "c"} {
		s.Audit(0, a, nil)
	}
	if _, err := s.DB.Exec("UPDATE audit_log SET hash = '', action = 'x' WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatal(err)
	}
	if res.BrokenAt != 2 {
		t.Fatalf("%+v", res)
	}
}

// actor_id is not hashed; it must agree with actor_ref or be NULL.
func TestAuditChainDetectsAChangedActorID(t *testing.T) {
	s := chainStore(t)
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	s.Audit(0, "a", nil)
	s.Audit(0, "b", nil)
	if _, err := s.DB.Exec("UPDATE audit_log SET actor_id = ? WHERE id = 2", uid); err != nil {
		t.Fatal(err)
	}
	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatal(err)
	}
	if res.BrokenAt != 2 || !strings.Contains(res.Reason, "actor_id") {
		t.Fatalf("%+v", res)
	}
}

// A clock stepped back leaves created_at out of id order; retention
// still removes a prefix of the table, so the chain stays intact.
func TestAuditChainSurvivesRetentionWithClockStep(t *testing.T) {
	s := chainStore(t)
	for _, a := range []string{"a", "b", "c", "d"} {
		s.Audit(0, a, nil)
	}
	// Rewrite created_at and the hashes as the rows would have been
	// written: row 2 stamped after row 3, both older than the cutoff.
	stamps := map[int64]string{
		1: "2020-01-01T00:00:00.000Z",
		2: "2020-01-03T00:00:00.000Z",
		3: "2020-01-02T00:00:00.000Z",
		4: "2099-01-01T00:00:00.000Z",
	}
	prev := ""
	for id := int64(1); id <= 4; id++ {
		var action, data string
		if err := s.DB.QueryRow("SELECT action, data_json FROM audit_log WHERE id = ?", id).Scan(&action, &data); err != nil {
			t.Fatal(err)
		}
		h := auditHash(prev, id, 0, action, stamps[id], data)
		if _, err := s.DB.Exec("UPDATE audit_log SET created_at = ?, prev_hash = ?, hash = ? WHERE id = ?",
			stamps[id], prev, h, id); err != nil {
			t.Fatal(err)
		}
		prev = h
	}
	if _, err := s.Sweep(Retention{Audit: time.Hour}, mustTime(t, "2020-01-02T12:00:00.000Z")); err != nil {
		t.Fatal(err)
	}
	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatal(err)
	}
	if res.BrokenAt != 0 || res.First != 4 || res.Rows != 1 {
		t.Fatalf("%+v", res)
	}
}

func mustTime(t *testing.T, v string) time.Time {
	t.Helper()
	tm, err := time.Parse(time.RFC3339, v)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestAuditChainDetectsARemovedRow(t *testing.T) {
	s := chainStore(t)
	for _, a := range []string{"a", "b", "c"} {
		s.Audit(0, a, nil)
	}
	if _, err := s.DB.Exec("DELETE FROM audit_log WHERE id = 2"); err != nil {
		t.Fatal(err)
	}
	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatal(err)
	}
	if res.BrokenAt != 3 || !strings.Contains(res.Reason, "previous hash") {
		t.Fatalf("%+v", res)
	}
}

// Retention removes the oldest rows, and deleting an account nulls
// actor_id; neither is tampering.
func TestAuditChainSurvivesRetentionAndAccountDeletion(t *testing.T) {
	s := chainStore(t)
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	s.Audit(0, "a", nil)
	s.Audit(uid, "b", nil)
	s.Audit(0, "c", nil)
	if _, err := s.DB.Exec("DELETE FROM audit_log WHERE id = 1"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec("DELETE FROM users WHERE id = ?", uid); err != nil {
		t.Fatal(err)
	}
	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatal(err)
	}
	if res.BrokenAt != 0 || res.First != 2 || res.Last != 3 {
		t.Fatalf("%+v", res)
	}
}

// Rows written before migration 0064 carry no hash; the chain starts
// after them, and a hashless row after that start is a break.
func TestAuditChainLegacyRows(t *testing.T) {
	s := chainStore(t)
	if _, err := s.DB.Exec("INSERT INTO audit_log (action) VALUES ('legacy')"); err != nil {
		t.Fatal(err)
	}
	s.Audit(0, "a", nil)
	res, err := s.VerifyAuditChain()
	if err != nil {
		t.Fatal(err)
	}
	if res.Unchained != 1 || res.BrokenAt != 0 || res.First != 2 {
		t.Fatalf("%+v", res)
	}
	if _, err := s.DB.Exec("INSERT INTO audit_log (action) VALUES ('injected')"); err != nil {
		t.Fatal(err)
	}
	if res, _ = s.VerifyAuditChain(); res.BrokenAt != 3 {
		t.Fatalf("hashless row after the chain: %+v", res)
	}
}

func TestAuditJournal(t *testing.T) {
	s := chainStore(t)
	var buf bytes.Buffer
	s.AuditJournal = slog.New(slog.NewTextHandler(&buf, nil))
	s.Audit(0, "cmd repo create", map[string]any{"argv": []string{"a/b"}})
	line := buf.String()
	for _, want := range []string{"msg=audit", "action=\"cmd repo create\"", "id=1", "hash="} {
		if !strings.Contains(line, want) {
			t.Fatalf("journal line %q lacks %q", line, want)
		}
	}
}
