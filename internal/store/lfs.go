package store

import (
	"database/sql"
	"errors"
)

// LFSSecret returns the instance's LFS token-signing secret, minting and
// persisting one on first use. gen supplies the new value so this package
// stays free of crypto choices.
func (s *Store) LFSSecret(gen func() string) (string, error) {
	var v string
	err := s.DB.QueryRow("SELECT value FROM settings WHERE key = 'lfs_secret'").Scan(&v)
	if err == nil {
		return v, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	v = gen()
	// A concurrent first use may win the insert; read back the winner.
	if _, err := s.DB.Exec(
		"INSERT INTO settings (key, value) VALUES ('lfs_secret', ?) ON CONFLICT (key) DO NOTHING", v); err != nil {
		return "", err
	}
	err = s.DB.QueryRow("SELECT value FROM settings WHERE key = 'lfs_secret'").Scan(&v)
	return v, err
}
