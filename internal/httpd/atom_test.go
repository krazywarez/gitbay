package httpd

import (
	"encoding/xml"
	"net/http/httptest"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

func TestWriteAtom(t *testing.T) {
	s := &Server{cfg: config.Config{Server: config.Server{SiteURL: "https://forge.test/"}}}
	rec := httptest.NewRecorder()
	s.writeAtom(rec, "/alice/app/releases.atom", "alice/app releases", []atomEntry{
		{ID: "https://forge.test/alice/app/releases#v1", Title: "v1: <first>", Updated: "2026-09-07T10:00:00Z",
			Author: &atomAuthor{Name: "alice"}, Link: atomLink{Rel: "alternate", Href: "https://forge.test/alice/app/releases#v1"},
			Content: &atomContent{Type: "text", Text: "notes & more"}},
	})
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/atom+xml") {
		t.Fatalf("content type %q", ct)
	}
	var got atomFeed
	if err := xml.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("not XML: %v\n%s", err, rec.Body.String())
	}
	if got.ID != "https://forge.test/alice/app/releases.atom" || got.Updated != "2026-09-07T10:00:00Z" || len(got.Entries) != 1 {
		t.Fatalf("feed: %+v", got)
	}
	if e := got.Entries[0]; e.Title != "v1: <first>" || e.Content.Text != "notes & more" || e.Author.Name != "alice" {
		t.Fatalf("entry: %+v", e)
	}
	alt := ""
	for _, l := range got.Links {
		if l.Rel == "alternate" {
			alt = l.Href
		}
	}
	if alt != "https://forge.test/alice/app/releases" {
		t.Fatalf("alternate link %q", alt)
	}

	// No entries: still a feed, updated now.
	rec = httptest.NewRecorder()
	s.writeAtom(rec, "/alice/activity.atom", "alice activity", nil)
	if err := xml.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Updated == "" {
		t.Fatalf("empty feed: %v %+v", err, got)
	}
}

func TestEventTitle(t *testing.T) {
	cases := []struct {
		ev          store.FeedEvent
		title, link string
	}{
		{store.FeedEvent{RepoPath: "alice/app", Actor: "bob", Kind: "issue.created", Data: `{"number":3}`},
			"bob: issue.created alice/app#3", "/alice/app/issues/3"},
		{store.FeedEvent{RepoPath: "alice/app", Actor: "bob", Kind: "mr.merged", Data: `{"number":7}`},
			"bob: mr.merged alice/app!7", "/alice/app/mrs/7"},
		{store.FeedEvent{RepoPath: "alice/app", Actor: "alice", Kind: "release.created", Data: `{"tag":"v1.0"}`},
			"alice: release.created alice/app v1.0", "/alice/app/releases#v1.0"},
		{store.FeedEvent{RepoPath: "alice/app", Kind: "repo.imported", Data: `{"from":"x"}`},
			"repo.imported in alice/app", "/alice/app"},
	}
	for _, tc := range cases {
		title, link := eventTitle(tc.ev)
		if title != tc.title || link != tc.link {
			t.Errorf("%s: got %q %q, want %q %q", tc.ev.Kind, title, link, tc.title, tc.link)
		}
	}
}
