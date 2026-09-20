package gitutil

import "testing"

func TestCommitFileChangeEmptyRepo(t *testing.T) {
	dir := t.TempDir()
	if err := InitBare(dir, "main", ""); err != nil {
		t.Fatal(err)
	}
	sha, err := CommitFileChange(dir, "main", "profile/README.md",
		[]byte("# hello\n"), "alice", "alice@example.org", "add about")
	if err != nil {
		t.Fatalf("first commit into an empty repository: %v", err)
	}
	if sha == "" {
		t.Fatal("no sha returned")
	}
	raw, err := ReadBlob(dir, "main", "profile/README.md", 1<<20)
	if err != nil {
		t.Fatalf("reading it back: %v", err)
	}
	if string(raw) != "# hello\n" {
		t.Errorf("read back %q", raw)
	}

	// The second commit still takes the parented path.
	if _, err := CommitFileChange(dir, "main", "profile/README.md",
		[]byte("# hello again\n"), "alice", "alice@example.org", "edit"); err != nil {
		t.Fatalf("second commit: %v", err)
	}

	// An unknown branch in a repository that has history is a typo, not a
	// new orphan branch.
	if _, err := CommitFileChange(dir, "nope", "x.md",
		[]byte("x"), "alice", "alice@example.org", "x"); err == nil {
		t.Error("committing to an unknown branch of a non-empty repository succeeded")
	}
}
