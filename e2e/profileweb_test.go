package e2e

import (
	"net/url"
	"strings"
	"testing"
)

// TestProfileSettingsWeb covers profile set from the account settings page
// (#161): description, website, links and about round-trip through the
// form, and emptying a field actually clears it rather than being skipped.
func TestProfileSettingsWeb(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	alice := inst.login(t, aliceKey)
	settingsURL := inst.base() + "/settings"

	// The form is on the page, every input labelled.
	_, body := browserGet(t, alice, settingsURL)
	for _, want := range []string{
		`<label for="p-description">`, `<label for="p-website">`,
		`<label for="p-links">`, `<label for="p-about">`, `<label for="format">`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("profile form missing %q:\n%s", want, body)
		}
	}

	// Setting every field lands where the CLI reads it.
	status, _ := browserPost(t, alice, settingsURL, url.Values{
		"field":       {"profile"},
		"description": {"builds small tools"},
		"website":     {"https://alice.example"},
		"links":       {"Mastodon|https://fosstodon.example/@alice\nhttps://alice.example/now"},
		"about":       {"hello there"},
		"format":      {"md"},
	})
	if status != 200 && status != 303 {
		t.Fatalf("profile post: %d", status)
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "profile", "show", "--json")
	for _, want := range []string{
		`"description":"builds small tools"`, `"website":"https://alice.example"`,
		`"label":"Mastodon"`, `"url":"https://fosstodon.example/@alice"`,
		`"url":"https://alice.example/now"`, `"about":"hello there"`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("profile set missing %q: %s", want, out)
		}
	}

	// The page shows what was just saved.
	_, body = browserGet(t, alice, settingsURL)
	for _, want := range []string{
		"builds small tools", "https://alice.example", "Mastodon|https://fosstodon.example/@alice",
		"https://alice.example/now", "hello there",
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("settings page did not round-trip %q:\n%s", want, body)
		}
	}

	// The whole form resubmits every time, the way a settings page does:
	// each step below carries the fields already in place and changes one.
	links := "Mastodon|https://fosstodon.example/@alice\nhttps://alice.example/now"

	// The org choice is honoured on the profile page.
	if status, _ := browserPost(t, alice, settingsURL, url.Values{
		"field": {"profile"}, "description": {"builds small tools"},
		"website": {"https://alice.example"}, "links": {links},
		"about": {"a /note/ in org"}, "format": {"org"},
	}); status != 200 && status != 303 {
		t.Fatalf("profile post (org): %d", status)
	}
	if _, page := browserGet(t, alice, inst.base()+"/alice"); !strings.Contains(page, "<em>note</em>") {
		t.Fatalf("about did not render as org:\n%s", page)
	}

	// Emptying the website clears it, not leaves it alone.
	if status, _ := browserPost(t, alice, settingsURL, url.Values{
		"field": {"profile"}, "description": {"builds small tools"},
		"website": {""}, "links": {links}, "about": {"a /note/ in org"}, "format": {"org"},
	}); status != 200 && status != 303 {
		t.Fatalf("profile post (clear website): %d", status)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "profile", "show", "--json")
	if strings.Contains(out, "alice.example") && strings.Contains(out, `"website"`) {
		t.Fatalf("website not cleared: %s", out)
	}
	if !strings.Contains(out, "builds small tools") {
		t.Fatalf("clearing website clobbered the description: %s", out)
	}
	if !strings.Contains(out, `"label":"Mastodon"`) {
		t.Fatalf("clearing website clobbered the links: %s", out)
	}

	// Emptying the links field clears the whole list.
	if status, _ := browserPost(t, alice, settingsURL, url.Values{
		"field": {"profile"}, "description": {"builds small tools"},
		"links": {""}, "about": {"a /note/ in org"}, "format": {"org"},
	}); status != 200 && status != 303 {
		t.Fatalf("profile post (clear links): %d", status)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "profile", "show", "--json")
	if strings.Contains(out, "Mastodon") || strings.Contains(out, `"links"`) {
		t.Fatalf("links not cleared: %s", out)
	}
}
