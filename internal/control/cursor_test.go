package control

import (
	"bytes"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func TestShellWord(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"plain", "plain"},
		{"foo-bar", "foo-bar"},
		{"123", "123"},
		{"foo bar", "'foo bar'"},
		{"it's", "'it'\\''s'"},
		{"a'b'c", "'a'\\''b'\\''c'"},
		{"", "''"},
	}
	for _, tt := range tests {
		if got := shellWord(tt.input); got != tt.want {
			t.Errorf("shellWord(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	cur := encodeCursor("issue", "42")
	key, err := decodeCursor("issue", cur)
	if err != nil || key != "42" {
		t.Fatalf("decode = %q, %v", key, err)
	}
	if _, err := decodeCursor("mr", cur); err == nil {
		t.Fatal("cursor accepted under the wrong kind")
	}
	if _, err := decodeCursor("issue", "not base64!"); err == nil {
		t.Fatal("garbage cursor accepted")
	}
	if _, err := decodeCursor("issue", encodeCursor("issue", "")); err == nil {
		t.Fatal("empty key accepted")
	}
}

func TestTrimPage(t *testing.T) {
	key := func(n int) string { return "k" }
	// No probe row: page as-is, no next.
	items, next := trimPage(page{limit: 3}, []int{1, 2, 3}, "issue", key)
	if len(items) != 3 || next != "" {
		t.Fatalf("full page: %v next=%q", items, next)
	}
	// Probe row present: trimmed, next minted.
	items, next = trimPage(page{limit: 2}, []int{1, 2, 3}, "issue", key)
	if len(items) != 2 || next == "" {
		t.Fatalf("trimmed page: %v next=%q", items, next)
	}
	// Unpaged: untouched.
	items, next = trimPage(page{}, []int{1, 2, 3}, "issue", key)
	if len(items) != 3 || next != "" {
		t.Fatalf("unpaged: %v next=%q", items, next)
	}
}

func TestEmitPageHintsTheNextPageAtATerminal(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	for i := 0; i < 3; i++ {
		if _, err := st.CreateBuild(repo.ID, "unit", "aaa", "main", `["true"]`, "", "", true); err != nil {
			t.Fatal(err)
		}
	}
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid})
	c.Term = Term{Cols: 100}
	if code := Dispatch(c, []string{"build", "list", repo.Path(), "--limit", "2"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if strings.Contains(c.Stdout.(*bytes.Buffer).String(), "next\t") {
		t.Errorf("cursor row on stdout at a terminal")
	}
	stderr := errOut.String()
	want := "more: gitbay build list " + repo.Path() + " --limit 2 --cursor "
	if !strings.Contains(stderr, want) {
		t.Errorf("stderr = %q, want %q…", stderr, want)
	}
	// Ensure no double space in the output.
	if strings.Contains(stderr, "  ") {
		t.Errorf("stderr contains double space: %q", stderr)
	}
}
