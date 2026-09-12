package store

import (
	"database/sql"
	"errors"
)

type Snippet struct {
	ID          int64
	PublicID    string
	OwnerID     int64
	OwnerName   string
	Description string
	Visibility  string // public | unlisted | private
	CreatedAt   string
	UpdatedAt   string
	// Files carries names and sizes. Content is filled by SnippetFiles and
	// SnippetFile only, so a listing does not read every body.
	Files []SnippetFile
}

type SnippetFile struct {
	Name    string
	Size    int64
	Content []byte
}

const snippetSelect = `
	SELECT s.id, s.public_id, s.owner_id, u.username, s.description, s.visibility, s.created_at, s.updated_at
	FROM snippets s JOIN users u ON u.id = s.owner_id`

func scanSnippet(row interface{ Scan(...any) error }) (Snippet, error) {
	var sn Snippet
	err := row.Scan(&sn.ID, &sn.PublicID, &sn.OwnerID, &sn.OwnerName, &sn.Description, &sn.Visibility, &sn.CreatedAt, &sn.UpdatedAt)
	return sn, err
}

// CreateSnippet inserts the snippet and its first file in one transaction.
// A public_id collision is ErrExists so the caller can draw another.
func (s *Store) CreateSnippet(ownerID int64, publicID, description, visibility, name string, content []byte) (int64, error) {
	tx, err := s.DB.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		"INSERT INTO snippets (public_id, owner_id, description, visibility) VALUES (?, ?, ?, ?)",
		publicID, ownerID, description, visibility)
	if err != nil {
		if isUniqueErr(err) {
			return 0, ErrExists
		}
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	if _, err := tx.Exec("INSERT INTO snippet_files (snippet_id, name, content, size) VALUES (?, ?, ?, ?)",
		id, name, content, len(content)); err != nil {
		return 0, err
	}
	return id, tx.Commit()
}

func (s *Store) SnippetByPublicID(publicID string) (Snippet, error) {
	sn, err := scanSnippet(s.DB.QueryRow(snippetSelect+" WHERE s.public_id = ?", publicID))
	if errors.Is(err, sql.ErrNoRows) {
		return sn, ErrNotFound
	}
	if err != nil {
		return sn, err
	}
	sn.Files, err = s.snippetFileNames(sn.ID)
	return sn, err
}

func (s *Store) snippetFileNames(id int64) ([]SnippetFile, error) {
	rows, err := s.DB.Query("SELECT name, size FROM snippet_files WHERE snippet_id = ? ORDER BY name", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SnippetFile
	for rows.Next() {
		var f SnippetFile
		if err := rows.Scan(&f.Name, &f.Size); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SnippetFiles returns every file with its content, by name.
func (s *Store) SnippetFiles(id int64) ([]SnippetFile, error) {
	rows, err := s.DB.Query("SELECT name, size, content FROM snippet_files WHERE snippet_id = ? ORDER BY name", id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SnippetFile
	for rows.Next() {
		var f SnippetFile
		if err := rows.Scan(&f.Name, &f.Size, &f.Content); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (s *Store) SnippetFile(id int64, name string) (SnippetFile, error) {
	var f SnippetFile
	err := s.DB.QueryRow("SELECT name, size, content FROM snippet_files WHERE snippet_id = ? AND name = ?", id, name).
		Scan(&f.Name, &f.Size, &f.Content)
	if errors.Is(err, sql.ErrNoRows) {
		return f, ErrNotFound
	}
	return f, err
}

// ListSnippets lists an owner's snippets newest first. all=false keeps
// public ones only. afterID is the keyset cursor: rows older than it.
// Ids grow with creation, so ordering by id is creation order.
func (s *Store) ListSnippets(ownerID int64, all bool, limit int, afterID int64) ([]Snippet, error) {
	q := snippetSelect + " WHERE s.owner_id = ?"
	args := []any{ownerID}
	if !all {
		q += " AND s.visibility = 'public'"
	}
	if afterID > 0 {
		q += " AND s.id < ?"
		args = append(args, afterID)
	}
	q += " ORDER BY s.id DESC"
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}
	rows, err := s.DB.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Snippet
	for rows.Next() {
		sn, err := scanSnippet(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, sn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// One query per row for the names. Command callers page at 200 rows
	// or fewer; the web list page is uncapped, which the per-account
	// snippet limit bounds.
	for i := range out {
		if out[i].Files, err = s.snippetFileNames(out[i].ID); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Store) CountSnippets(ownerID int64, all bool) (int, error) {
	q := "SELECT COUNT(*) FROM snippets WHERE owner_id = ?"
	if !all {
		q += " AND visibility = 'public'"
	}
	var n int
	err := s.DB.QueryRow(q, ownerID).Scan(&n)
	return n, err
}

func (s *Store) UpdateSnippet(id int64, description, visibility string) error {
	_, err := s.DB.Exec(
		"UPDATE snippets SET description = ?, visibility = ?, updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?",
		description, visibility, id)
	return err
}

func (s *Store) DeleteSnippet(id int64) error {
	_, err := s.DB.Exec("DELETE FROM snippets WHERE id = ?", id)
	return err
}

// SetSnippetFile adds the file or replaces one of the same name.
func (s *Store) SetSnippetFile(id int64, name string, content []byte) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`INSERT INTO snippet_files (snippet_id, name, content, size) VALUES (?, ?, ?, ?)
		ON CONFLICT (snippet_id, name) DO UPDATE SET content = excluded.content, size = excluded.size`,
		id, name, content, len(content)); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE snippets SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) RemoveSnippetFile(id int64, name string) error {
	tx, err := s.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	res, err := tx.Exec("DELETE FROM snippet_files WHERE snippet_id = ? AND name = ?", id, name)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec("UPDATE snippets SET updated_at = strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id = ?", id); err != nil {
		return err
	}
	return tx.Commit()
}
