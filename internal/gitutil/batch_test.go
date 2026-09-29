package gitutil

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestBlobBatch(t *testing.T) {
	dir := t.TempDir()
	run := func(stdin string, args ...string) string {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
		cmd.Stdin = strings.NewReader(stdin)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("", "init", "-q", "--bare")
	a := run("one\ntwo", "hash-object", "-w", "--stdin")
	b := run("", "hash-object", "-w", "--stdin")
	tree := run("100644 blob "+a+"\tf\n", "mktree")
	batch, err := NewBlobBatch(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer batch.Close()
	for _, c := range []struct{ oid, want string }{{a, "one\ntwo"}, {b, ""}, {a, "one\ntwo"}} {
		got, err := batch.Read(c.oid, 100)
		if err != nil || string(got) != c.want {
			t.Fatalf("Read(%s) = %q, %v; want %q", c.oid, got, err, c.want)
		}
	}
	if _, err := batch.Read(a, 3); err == nil {
		t.Error("a blob over the limit was read")
	}
	if got, err := batch.Read(b, 100); err != nil || string(got) != "" {
		t.Fatalf("read after a blob over the limit = %q, %v", got, err)
	}
	if _, err := batch.Read(tree, 100); err == nil {
		t.Error("a tree was read as a blob")
	}
	if _, err := batch.Read(strings.Repeat("0", 40), 100); err == nil {
		t.Error("a missing object was read")
	}
	if got, err := batch.Read(a, 100); err != nil || string(got) != "one\ntwo" {
		t.Fatalf("read after refusals = %q, %v", got, err)
	}
}
