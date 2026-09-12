package e2e

import (
	"net/url"
	"strings"
	"testing"
)

// The label set itself is managed from the browser: create, recolour and
// remove, each through the label command the CLI runs (#163).
func TestLabelsWeb(t *testing.T) {
	inst := startInstanceWith(t, "[web]\nmode = \"accounts\"\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", "one")
	inst.ssh(t, aliceKey, "", "issue", "label", "alice/app", "1", "--add", "bug")

	alice := inst.login(t, aliceKey)
	base := inst.base() + "/alice/app"

	// The existing label is listed with its use count.
	status, page := browserGet(t, alice, base+"/labels")
	if status != 200 || !strings.Contains(page, "bug") {
		t.Fatalf("labels page: %d\n%s", status, page)
	}

	// Creating one with a colour, and recolouring the existing one.
	if status, _ := browserPost(t, alice, base+"/labels", url.Values{
		"name": {"docs"}, "color": {"1f6feb"}}); status != 200 {
		t.Fatal("label create failed")
	}
	if status, _ := browserPost(t, alice, base+"/labels", url.Values{
		"name": {"bug"}, "color": {"cf222e"}}); status != 200 {
		t.Fatal("label recolour failed")
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "label", "list", "alice/app", "--json")
	if !strings.Contains(out, `{"name":"bug","color":"#cf222e","issues":1}`) ||
		!strings.Contains(out, `{"name":"docs","color":"#1f6feb","issues":0}`) {
		t.Fatalf("labels not as posted:\n%s", out)
	}

	// A bad colour comes back as an error on the page, not a silent no-op.
	_, body := browserPost(t, alice, base+"/labels", url.Values{
		"name": {"docs"}, "color": {"red"}})
	if !strings.Contains(body, `class="error"`) {
		t.Errorf("bad colour accepted:\n%s", body)
	}

	// Removing a label needs its name typed; a bare post is refused and
	// the label stays.
	_, body = browserPost(t, alice, base+"/labels", url.Values{
		"action": {"remove"}, "name": {"bug"}})
	if !strings.Contains(body, "type bug to confirm") {
		t.Fatalf("unconfirmed remove was not refused:\n%s", body)
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "label", "list", "alice/app", "--json"); !strings.Contains(out, `"name":"bug"`) {
		t.Fatalf("label removed without confirmation: %s", out)
	}
	if status, _ := browserPost(t, alice, base+"/labels", url.Values{
		"action": {"remove"}, "name": {"bug"}, "confirm": {"bug"}}); status != 200 {
		t.Fatal("label remove failed")
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "1", "--json"); strings.Contains(out, `"bug"`) {
		t.Fatalf("issue still carries the removed label:\n%s", out)
	}

	// A reader sees the set and none of the controls.
	bob := inst.login(t, bobKey)
	_, p := browserGet(t, bob, base+"/labels")
	if !strings.Contains(p, "docs") {
		t.Fatalf("reader cannot see the labels:\n%s", p)
	}
	if strings.Contains(p, "New label") || strings.Contains(p, "Remove") {
		t.Fatal("reader sees a label control")
	}
	browserPost(t, bob, base+"/labels", url.Values{"name": {"sneak"}})
	if out, _, _ := inst.ssh(t, aliceKey, "", "label", "list", "alice/app", "--json"); strings.Contains(out, "sneak") {
		t.Fatal("reader created a label")
	}
}
