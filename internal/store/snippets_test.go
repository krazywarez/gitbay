package store

import (
	"errors"
	"testing"
)

func TestSnippets(t *testing.T) {
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	alice, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	id, err := s.CreateSnippet(alice, "abcdef012345", "a log", "unlisted", "build.log", []byte("ok\n"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSnippet(alice, "abcdef012345", "", "public", "x", []byte("x")); !errors.Is(err, ErrExists) {
		t.Fatalf("duplicate public id: %v", err)
	}
	sn, err := s.SnippetByPublicID("abcdef012345")
	if err != nil {
		t.Fatal(err)
	}
	if sn.ID != id || sn.OwnerName != "alice" || sn.Visibility != "unlisted" || sn.Description != "a log" {
		t.Fatalf("snippet: %+v", sn)
	}
	if len(sn.Files) != 1 || sn.Files[0].Name != "build.log" || sn.Files[0].Size != 3 || sn.Files[0].Content != nil {
		t.Fatalf("files on lookup: %+v", sn.Files)
	}

	// Set adds, then replaces; remove drops; the file read carries content.
	if err := s.SetSnippetFile(id, "notes.txt", []byte("one\n")); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSnippetFile(id, "notes.txt", []byte("two\n")); err != nil {
		t.Fatal(err)
	}
	f, err := s.SnippetFile(id, "notes.txt")
	if err != nil || string(f.Content) != "two\n" || f.Size != 4 {
		t.Fatalf("file after replace: %+v %v", f, err)
	}
	files, err := s.SnippetFiles(id)
	if err != nil || len(files) != 2 || files[0].Name != "build.log" || string(files[1].Content) != "two\n" {
		t.Fatalf("files: %+v %v", files, err)
	}
	if err := s.RemoveSnippetFile(id, "notes.txt"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveSnippetFile(id, "notes.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("remove missing file: %v", err)
	}
	if _, err := s.SnippetFile(id, "notes.txt"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("read removed file: %v", err)
	}

	// Listing: public only unless all; newest first; keyset by id.
	pub, err := s.CreateSnippet(alice, "000000000001", "", "public", "a", []byte("a"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.CreateSnippet(alice, "000000000002", "", "private", "b", []byte("b")); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListSnippets(alice, false, 0, 0)
	if err != nil || len(got) != 1 || got[0].ID != pub {
		t.Fatalf("public list: %+v %v", got, err)
	}
	got, err = s.ListSnippets(alice, true, 0, 0)
	if err != nil || len(got) != 3 || got[0].PublicID != "000000000002" || got[2].ID != id {
		t.Fatalf("all list: %+v %v", got, err)
	}
	got, err = s.ListSnippets(alice, true, 2, got[0].ID)
	if err != nil || len(got) != 2 || got[0].ID != pub {
		t.Fatalf("paged list: %+v %v", got, err)
	}
	if n, err := s.CountSnippets(alice, false); err != nil || n != 1 {
		t.Fatalf("public count: %d %v", n, err)
	}
	if n, err := s.CountSnippets(alice, true); err != nil || n != 3 {
		t.Fatalf("all count: %d %v", n, err)
	}

	// Update, delete, and the owner cascade.
	if err := s.UpdateSnippet(id, "renamed", "public"); err != nil {
		t.Fatal(err)
	}
	sn, _ = s.SnippetByPublicID("abcdef012345")
	if sn.Description != "renamed" || sn.Visibility != "public" {
		t.Fatalf("after update: %+v", sn)
	}
	if err := s.DeleteSnippet(id); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SnippetByPublicID("abcdef012345"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after delete: %v", err)
	}
	if err := s.DeleteUser(alice); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.DB.QueryRow("SELECT COUNT(*) FROM snippet_files").Scan(&n); err != nil || n != 0 {
		t.Fatalf("files after user delete: %d %v", n, err)
	}
}
