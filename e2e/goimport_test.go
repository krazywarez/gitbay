package e2e

import (
	"strings"
	"testing"
)

func TestGoImportVanity(t *testing.T) {
	inst := startInstanceWith(t, "[go_import]\n\"127.0.0.1/tool\" = \"alice/tool\"\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/tool"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	want := `<meta name="go-import" content="127.0.0.1/tool git https://gitbay.test/alice/tool.git">`
	status, body := inst.get(t, "/tool?go-get=1")
	if status != 200 || !strings.Contains(body, want) {
		t.Fatalf("module root: %d\n%s", status, body)
	}
	// Subpackages resolve to the same module.
	status, body = inst.get(t, "/tool/cmd/x?go-get=1")
	if status != 200 || !strings.Contains(body, want) {
		t.Fatalf("subpackage: %d\n%s", status, body)
	}
	// Prefix boundary: /toolbox is not under the module.
	if _, body = inst.get(t, "/toolbox?go-get=1"); strings.Contains(body, "go-import") {
		t.Fatal("prefix leak: /toolbox matched")
	}
	// Without go-get the path routes normally (no owner "tool" -> 404).
	if status, _ = inst.get(t, "/tool"); status != 404 {
		t.Fatalf("normal routing broken: %d", status)
	}
}
