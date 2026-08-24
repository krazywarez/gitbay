package store

import (
	"database/sql"
	"errors"
	"fmt"
)

type Release struct {
	ID        int64
	RepoID    int64
	Tag       string
	Title     string
	Notes     string
	Author    string
	CreatedAt string
	Assets    []ReleaseAsset
}

type ReleaseAsset struct {
	ID         int64
	Name       string
	Size       int64
	SHA256     string
	UploadedAt string
}

func (s *Store) CreateRelease(repoID int64, tag, title, notes string, authorID int64) (int64, error) {
	res, err := s.DB.Exec(
		"INSERT INTO releases (repo_id, tag, title, notes, author_id) VALUES (?, ?, ?, ?, ?)",
		repoID, tag, title, notes, authorID)
	if err != nil {
		if isUniqueErr(err) {
			return 0, fmt.Errorf("a release for tag %q already exists", tag)
		}
		return 0, err
	}
	return res.LastInsertId()
}

const releaseSelect = `
	SELECT r.id, r.repo_id, r.tag, r.title, r.notes, COALESCE(u.username, ''), r.created_at
	FROM releases r LEFT JOIN users u ON u.id = r.author_id`

func (s *Store) releaseAssets(rel *Release) error {
	rows, err := s.DB.Query(
		"SELECT id, name, size, sha256, uploaded_at FROM release_assets WHERE release_id = ? ORDER BY name",
		rel.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var a ReleaseAsset
		if err := rows.Scan(&a.ID, &a.Name, &a.Size, &a.SHA256, &a.UploadedAt); err != nil {
			return err
		}
		rel.Assets = append(rel.Assets, a)
	}
	return rows.Err()
}

func (s *Store) ReleaseByTag(repoID int64, tag string) (Release, error) {
	var r Release
	err := s.DB.QueryRow(releaseSelect+" WHERE r.repo_id = ? AND r.tag = ?", repoID, tag).
		Scan(&r.ID, &r.RepoID, &r.Tag, &r.Title, &r.Notes, &r.Author, &r.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	if err != nil {
		return r, err
	}
	return r, s.releaseAssets(&r)
}

// ListReleases returns releases newest-first, assets included.
func (s *Store) ListReleases(repoID int64) ([]Release, error) {
	rows, err := s.DB.Query(releaseSelect+" WHERE r.repo_id = ? ORDER BY r.created_at DESC", repoID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Release
	for rows.Next() {
		var r Release
		if err := rows.Scan(&r.ID, &r.RepoID, &r.Tag, &r.Title, &r.Notes, &r.Author, &r.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := s.releaseAssets(&out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) DeleteRelease(id int64) error {
	res, err := s.DB.Exec("DELETE FROM releases WHERE id = ?", id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *Store) AddReleaseAsset(releaseID int64, name string, size int64, sha256 string) error {
	_, err := s.DB.Exec(
		"INSERT INTO release_assets (release_id, name, size, sha256) VALUES (?, ?, ?, ?)",
		releaseID, name, size, sha256)
	if isUniqueErr(err) {
		return fmt.Errorf("asset %q already exists on this release", name)
	}
	return err
}

func (s *Store) RemoveReleaseAsset(releaseID int64, name string) error {
	res, err := s.DB.Exec(
		"DELETE FROM release_assets WHERE release_id = ? AND name = ?", releaseID, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
