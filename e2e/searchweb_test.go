package e2e

import (
	"net/url"
	"strings"
	"testing"
)

// TestGlobalSearchAndNotificationsWeb covers the two web surfaces #118
// still lacked: /search across the instance, and the notification inbox.
func TestGlobalSearchAndNotificationsWeb(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/widget"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/secret", "--private"); code != 0 {
		t.Fatalf("private repo create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, bobKey, "", "issue", "create", "alice/widget",
		"--title", "'widget leaks memory'", "--body", "'it climbs'"); code != 0 {
		t.Fatalf("issue create: %s", errOut)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/secret",
		"--title", "'widget in the private repo'"); code != 0 {
		t.Fatalf("private issue create: %s", errOut)
	}

	// Anonymous search reaches the public repository and its issue, and
	// neither the private repository nor its issue.
	status, body := inst.get(t, "/search?q=widget")
	if status != 200 {
		t.Fatalf("anonymous search: %d", status)
	}
	if !strings.Contains(body, `href="/alice/widget"`) ||
		!strings.Contains(body, "widget leaks memory") ||
		!strings.Contains(body, `href="/alice/widget/issues/1"`) {
		t.Fatalf("anonymous search missing public hits:\n%s", body)
	}
	if strings.Contains(body, "alice/secret") || strings.Contains(body, "private repo") {
		t.Fatal("anonymous search leaked a private repository")
	}

	// Filtering by kind keeps the repository row and drops the issue.
	_, body = inst.get(t, "/search?q=widget&kind=repo")
	if !strings.Contains(body, `href="/alice/widget"`) || strings.Contains(body, "widget leaks memory") {
		t.Fatalf("kind=repo filter:\n%s", body)
	}
	_, body = inst.get(t, "/search?q=x")
	if !strings.Contains(body, "2 to 200 characters") {
		t.Fatal("short query not refused")
	}

	// Alice sees her private repository and its issue in the same page.
	browser := inst.login(t, aliceKey)
	status, body = browserGet(t, browser, inst.base()+"/search?q=widget")
	if status != 200 || !strings.Contains(body, "alice/secret") ||
		!strings.Contains(body, "widget in the private repo") {
		t.Fatalf("owner search misses private rows:\n%s", body)
	}

	// The rail carries the unread badge and the notifications page lists
	// the issue bob opened.
	status, body = browserGet(t, browser, inst.base()+"/notifications")
	if status != 200 || !strings.Contains(body, "bob opened issue #1") ||
		!strings.Contains(body, `href="/alice/widget/issues/1"`) {
		t.Fatalf("notifications page:\n%s", body)
	}
	if !strings.Contains(body, `href="/notifications"`) {
		t.Fatal("rail has no notifications link")
	}

	// Marking all read empties the unread list but not the full one.
	if status, _ := browserPost(t, browser, inst.base()+"/notifications", url.Values{}); status != 200 {
		t.Fatalf("mark all read: %d", status)
	}
	_, body = browserGet(t, browser, inst.base()+"/notifications")
	if !strings.Contains(body, "nothing unread") {
		t.Fatalf("unread list after sweep:\n%s", body)
	}
	_, body = browserGet(t, browser, inst.base()+"/notifications?all=1")
	if !strings.Contains(body, "bob opened issue #1") {
		t.Fatalf("all list after sweep:\n%s", body)
	}

	// Watching from the repository header is the same state the CLI sets.
	if status, _ := browserPost(t, browser, inst.base()+"/alice/widget/watch", url.Values{}); status != 200 {
		t.Fatalf("watch toggle: %d", status)
	}
	out, errOut, code := inst.ssh(t, aliceKey, "", "notifications", "list", "--all", "--json")
	if code != 0 {
		t.Fatalf("notifications list: %s", errOut)
	}
	if !strings.Contains(out, "alice/widget") {
		t.Fatalf("inbox over ssh: %s", out)
	}
	_, body = browserGet(t, browser, inst.base()+"/alice/widget")
	if !strings.Contains(body, ">Watching</button>") {
		t.Fatalf("repo header does not show the watch state:\n%s", body)
	}

	// Anonymous visitors get the search page; the inbox sends them to log
	// in. http.Get follows the redirect, so the login page is the proof.
	if status, body := inst.get(t, "/notifications"); status != 200 || !strings.Contains(body, "Sign in") {
		t.Fatalf("anonymous /notifications: %d\n%s", status, body)
	}
}
