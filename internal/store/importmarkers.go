package store

import (
	"database/sql"
	"errors"
)

// ImportMarker returns the stored value for an import progress key, and
// whether it exists. Markers make history imports resumable: items and
// comments already imported are skipped on re-run.
func (s *Store) ImportMarker(repoID int64, key string) (string, bool, error) {
	var v string
	err := s.DB.QueryRow(
		"SELECT value FROM import_markers WHERE repo_id = ? AND key = ?", repoID, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return v, err == nil, err
}

func (s *Store) SetImportMarker(repoID int64, key, value string) error {
	_, err := s.DB.Exec(
		"INSERT INTO import_markers (repo_id, key, value) VALUES (?, ?, ?) ON CONFLICT (repo_id, key) DO UPDATE SET value = excluded.value",
		repoID, key, value)
	return err
}
