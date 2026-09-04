// Package store owns SQLite access and schema migrations.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strconv"
	"strings"

	"modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct {
	DB *sql.DB
}

// Open opens (creating if needed) the database at path with WAL mode and
// foreign keys enforced. Use ":memory:" in tests.
//
// _txlock=immediate is what serialises writers. Every transaction in this
// package writes, and a deferred one takes the write lock only when it
// reaches its first write — by which point another writer may hold it.
// SQLite answers that with SQLITE_BUSY and does not invoke the busy
// handler, because waiting would deadlock two transactions each holding a
// read lock the other needs; busy_timeout cannot help. Measured with
// eight concurrent read-then-write transactions, 44% of them failed.
// Beginning immediate takes the write lock up front, where busy_timeout
// does apply, so a second writer waits its turn: the same load runs with
// no failures, and readers, which WAL keeps out of the way, are
// unaffected (#121).
func Open(path string) (*Store, error) {
	dsn := path + "?_txlock=immediate&_pragma=journal_mode(WAL)&_pragma=foreign_keys(ON)&_pragma=busy_timeout(5000)"
	if path == ":memory:" {
		dsn = ":memory:?_txlock=immediate&_pragma=foreign_keys(ON)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.Ping(); err != nil {
		db.Close()
		return nil, err
	}
	// SQLite creates the file 0666&~umask, so it lands 0644 by default. The
	// directory above it is the real boundary, but the file holds token
	// hashes, addresses and private repo names and has no business being
	// world-readable on its own.
	if path != ":memory:" {
		if err := os.Chmod(path, 0o640); err != nil && !errors.Is(err, fs.ErrNotExist) {
			db.Close()
			return nil, err
		}
	}
	return &Store{DB: db}, nil
}

func (s *Store) Close() error { return s.DB.Close() }

type migration struct {
	version int
	name    string
	up      string
	down    string
}

func loadMigrations() ([]migration, error) {
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return nil, err
	}
	byVersion := map[int]*migration{}
	for _, e := range entries {
		name := e.Name()
		// <version>_<name>.<up|down>.sql
		base, ok := strings.CutSuffix(name, ".sql")
		if !ok {
			return nil, fmt.Errorf("migration %q: not .sql", name)
		}
		var dir string
		if b, ok := strings.CutSuffix(base, ".up"); ok {
			base, dir = b, "up"
		} else if b, ok := strings.CutSuffix(base, ".down"); ok {
			base, dir = b, "down"
		} else {
			return nil, fmt.Errorf("migration %q: missing .up/.down", name)
		}
		verStr, rest, ok := strings.Cut(base, "_")
		if !ok {
			return nil, fmt.Errorf("migration %q: missing version prefix", name)
		}
		ver, err := strconv.Atoi(verStr)
		if err != nil {
			return nil, fmt.Errorf("migration %q: bad version: %w", name, err)
		}
		m := byVersion[ver]
		if m == nil {
			m = &migration{version: ver, name: rest}
			byVersion[ver] = m
		}
		sqlBytes, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, err
		}
		if dir == "up" {
			m.up = string(sqlBytes)
		} else {
			m.down = string(sqlBytes)
		}
	}
	var ms []migration
	for _, m := range byVersion {
		if m.up == "" || m.down == "" {
			return nil, fmt.Errorf("migration %d %q: missing up or down file", m.version, m.name)
		}
		ms = append(ms, *m)
	}
	sort.Slice(ms, func(i, j int) bool { return ms[i].version < ms[j].version })
	for i, m := range ms {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration versions not contiguous at %d", m.version)
		}
	}
	return ms, nil
}

// Version returns the current schema version (0 = empty database).
func (s *Store) Version() (int, error) {
	var v int
	err := s.DB.QueryRow("PRAGMA user_version").Scan(&v)
	return v, err
}

// MigrateUp applies all pending migrations.
func (s *Store) MigrateUp() error { return s.migrateTo(-1) }

// MigrateTo migrates up or down to the given version. 0 empties the schema.
func (s *Store) MigrateTo(target int) error { return s.migrateTo(target) }

func (s *Store) migrateTo(target int) error {
	ms, err := loadMigrations()
	if err != nil {
		return err
	}
	if target < 0 {
		target = len(ms)
	}
	if target > len(ms) {
		return fmt.Errorf("no such schema version %d (max %d)", target, len(ms))
	}
	cur, err := s.Version()
	if err != nil {
		return err
	}
	step := func(sqlText string, newVersion int) error {
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err := tx.Exec(sqlText); err != nil {
			return err
		}
		if _, err := tx.Exec(fmt.Sprintf("PRAGMA user_version = %d", newVersion)); err != nil {
			return err
		}
		return tx.Commit()
	}
	for cur < target {
		m := ms[cur]
		if err := step(m.up, m.version); err != nil {
			return fmt.Errorf("migration %d up: %w", m.version, err)
		}
		cur = m.version
	}
	for cur > target {
		m := ms[cur-1]
		if err := step(m.down, m.version-1); err != nil {
			return fmt.Errorf("migration %d down: %w", m.version, err)
		}
		cur = m.version - 1
	}
	return nil
}

// IsInternal reports whether err is the database or the I/O beneath it
// failing, as opposed to a sentinel or a message about the caller's
// input. Callers map it to a failure exit rather than a usage error.
func IsInternal(err error) bool {
	var sqlErr *sqlite.Error
	var pathErr *fs.PathError
	return errors.As(err, &sqlErr) || errors.As(err, &pathErr) ||
		errors.Is(err, sql.ErrTxDone) || errors.Is(err, sql.ErrConnDone) ||
		errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
