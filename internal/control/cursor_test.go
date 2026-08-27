package control

import "testing"

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
