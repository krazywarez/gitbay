package control

import (
	"testing"
	"time"
)

func TestParseTerm(t *testing.T) {
	cases := map[string]Term{
		"120":       {Cols: 120},
		"120,color": {Cols: 120, Color: true},
		"40":        {Cols: 40},
		"39":        {},
		"":          {},
		"abc":       {},
		"80,blink":  {},
		"80,":       {},
		"5000":      {},
	}
	for in, want := range cases {
		if got := ParseTerm(in); got != want {
			t.Errorf("ParseTerm(%q) = %+v, want %+v", in, got, want)
		}
	}
}

func TestCells(t *testing.T) {
	cases := map[string]int{
		"abc":                   3,
		"日本":                    4,
		"é":                     1,
		"é":               1,
		"\x1b[32mopen\x1b[0m":   4,
		"":                      0,
	}
	for in, want := range cases {
		if got := cells(in); got != want {
			t.Errorf("cells(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestClip(t *testing.T) {
	if got := clip("Dependency updates available", 14); got != "Dependency up…" {
		t.Errorf("clip = %q", got)
	}
	if got := clip("short", 14); got != "short" {
		t.Errorf("clip = %q", got)
	}
	if got := clip("日本語のタイトル", 7); got != "日本語…" {
		t.Errorf("clip wide = %q", got)
	}
}

func TestStampAndRelAge(t *testing.T) {
	if got := stamp("2026-09-23T23:26:00.570Z"); got != "2026-09-23T23:26:00Z" {
		t.Errorf("stamp = %q", got)
	}
	if got := stamp("2026-09-23 23:26:00"); got != "2026-09-23T23:26:00Z" {
		t.Errorf("stamp sqlite = %q", got)
	}
	if got := stamp("garbage"); got != "garbage" {
		t.Errorf("stamp garbage = %q", got)
	}
	now := time.Date(2026, 9, 23, 12, 0, 0, 0, time.UTC)
	cases := map[string]string{
		"2026-09-23T11:59:30Z": "just now",
		"2026-09-23T11:55:00Z": "5m ago",
		"2026-09-23T10:00:00Z": "2h ago",
		"2026-09-20T12:00:00Z": "3d ago",
		"2026-09-02T12:00:00Z": "3w ago",
		"2026-06-01T12:00:00Z": "2026-06-01",
		"2026-09-24T12:00:00Z": "just now",
		"not a time":           "not a time",
	}
	for in, want := range cases {
		if got := relAge(in, now); got != want {
			t.Errorf("relAge(%q) = %q, want %q", in, got, want)
		}
	}
}
