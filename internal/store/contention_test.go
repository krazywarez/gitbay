package store

import (
	"sync"
	"sync/atomic"
	"testing"
)

// TestConcurrentWritersDoNotFail runs the shape every write transaction in
// this package has — read a row, write it back, inside one Begin — from
// several goroutines at once.
//
// Deferred transactions take the write lock at their first write, by which
// point another writer may hold it. SQLite answers SQLITE_BUSY and does
// not invoke the busy handler for that case, so busy_timeout cannot help
// and the transaction simply fails. Measured before the fix, 44% of these
// failed. The DSN opens transactions as immediate, which takes the lock up
// front where busy_timeout applies (#121).
//
// This has to be a real file: :memory: gives each connection its own
// database, so nothing contends.
func TestConcurrentWritersDoNotFail(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("cmc", true)
	if err != nil {
		t.Fatal(err)
	}
	repoID, err := s.CreateRepo("user", uid, "lib", "public")
	if err != nil {
		t.Fatal(err)
	}

	const writers, each = 8, 40
	var failures int64
	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				if err := func() error {
					tx, err := s.DB.Begin()
					if err != nil {
						return err
					}
					defer tx.Rollback()
					var n int
					if err := tx.QueryRow(
						"SELECT issue_counter FROM repos WHERE id = ?", repoID).Scan(&n); err != nil {
						return err
					}
					if _, err := tx.Exec(
						"UPDATE repos SET issue_counter = ? WHERE id = ?", n+1, repoID); err != nil {
						return err
					}
					return tx.Commit()
				}(); err != nil {
					atomic.AddInt64(&failures, 1)
				}
			}
		}()
	}
	wg.Wait()

	if failures != 0 {
		t.Fatalf("%d of %d write transactions failed", failures, writers*each)
	}
	// Serialised writers each read what the last one committed, so no
	// increment is lost.
	var got int
	if err := s.DB.QueryRow("SELECT issue_counter FROM repos WHERE id = ?", repoID).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != writers*each {
		t.Fatalf("counter = %d, want %d: an increment was lost", got, writers*each)
	}
}
