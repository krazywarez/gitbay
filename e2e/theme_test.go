package e2e

import (
	"net/url"
	"strings"
	"testing"
)

// web theme set fixes the colour scheme the layout stamps on <html>; the
// account page shows the same setting and changes it (#232).
func TestWebTheme(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	key := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", key+".pub")

	if out, _, code := inst.ssh(t, key, "", "web", "theme", "show", "--json"); code != 0 || !strings.Contains(out, `"theme":"system"`) {
		t.Fatalf("theme show: %d %s", code, out)
	}
	if _, _, code := inst.ssh(t, key, "", "web", "theme", "set", "blue"); code != 2 {
		t.Fatalf("bad value accepted: %d", code)
	}
	if out, _, code := inst.ssh(t, key, "", "web", "theme", "set", "dark", "--json"); code != 0 || !strings.Contains(out, `"theme":"dark"`) {
		t.Fatalf("theme set: %d %s", code, out)
	}

	alice := inst.login(t, key)
	set := inst.base() + "/settings"
	_, body := browserGet(t, alice, set)
	if !strings.Contains(body, `<html lang="en" data-theme="dark">`) {
		t.Fatalf("page is not stamped dark:\n%s", body[:200])
	}
	if !strings.Contains(body, `<option value="dark" selected>`) {
		t.Fatalf("account page does not show dark selected")
	}
	if status, _ := browserPost(t, alice, set, url.Values{"field": {"theme"}, "theme": {"system"}}); status != 200 {
		t.Fatalf("settings post: %d", status)
	}
	if out, _, _ := inst.ssh(t, key, "", "web", "theme", "show", "--json"); !strings.Contains(out, `"theme":"system"`) {
		t.Fatalf("web form did not reset the theme: %s", out)
	}
	if _, body := browserGet(t, alice, set); strings.Contains(body, "data-theme") {
		t.Fatalf("system stamps the page")
	}
}
