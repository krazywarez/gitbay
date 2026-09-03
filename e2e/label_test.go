package e2e

import (
	"strings"
	"testing"
)

// Labels get colours: set from the CLI, listed with their use, painted
// on the web, and removed from every issue at once.
func TestLabelColors(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "create", "alice/app", "--title", "one"); code != 0 {
		t.Fatal("issue create failed")
	}
	// issue label still creates a colourless label on the fly.
	if _, _, code := inst.ssh(t, aliceKey, "", "issue", "label", "alice/app", "1", "--add", "bug"); code != 0 {
		t.Fatal("issue label failed")
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "label", "list", "alice/app"); !strings.Contains(out, "bug\t\t1") {
		t.Fatalf("list after issue label:\n%s", out)
	}
	if _, _, code := inst.ssh(t, bobKey, "", "label", "set", "alice/app", "bug", "--color", "cf222e"); code != 4 {
		t.Fatal("reader set a colour")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "", "label", "set", "alice/app", "bug", "--color", "red"); code != 2 || !strings.Contains(errOut, "rrggbb") {
		t.Fatalf("bad colour accepted: exit %d %s", code, errOut)
	}
	if out, _, code := inst.ssh(t, aliceKey, "", "label", "set", "alice/app", "bug", "--color", "CF222E"); code != 0 || !strings.Contains(out, "bug is #cf222e") {
		t.Fatalf("set colour: exit %d %s", code, out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "label", "set", "alice/app", "docs"); code != 0 {
		t.Fatal("create without colour failed")
	}
	out, _, _ := inst.ssh(t, aliceKey, "", "label", "list", "alice/app", "--json")
	if !strings.Contains(out, `{"name":"bug","color":"#cf222e","issues":1}`) || !strings.Contains(out, `{"name":"docs","issues":0}`) {
		t.Fatalf("list json:\n%s", out)
	}
	// The web paints the chip with the stored colour.
	if _, body := inst.get(t, "/alice/app/issues"); !strings.Contains(body, "--chip:#cf222e") {
		t.Fatalf("issue list does not carry the colour:\n%s", body)
	}
	// Clearing the colour keeps the label; removing it unlinks the issue.
	if out, _, _ := inst.ssh(t, aliceKey, "", "label", "set", "alice/app", "bug", "--color", "''"); !strings.Contains(out, "no colour set") {
		t.Fatalf("clear colour:\n%s", out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "label", "remove", "alice/app", "bug"); code != 0 {
		t.Fatal("remove failed")
	}
	if out, _, _ := inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "1", "--json"); strings.Contains(out, `"bug"`) {
		t.Fatalf("issue still carries the removed label:\n%s", out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "label", "remove", "alice/app", "bug"); code != 3 {
		t.Fatal("second remove should be not found")
	}
}
