package control

import (
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

var updatePlain = flag.Bool("update-plain", false, "rewrite testdata/plain from the current output")

var plainStamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[ T]\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z| UTC)?`)

// pinPlain compares a command's piped output with the copy taken before
// its terminal screen was rewritten. Timestamps are masked, since
// fixtures are created at test time.
func pinPlain(t *testing.T, name, got string) {
	t.Helper()
	got = plainStamp.ReplaceAllString(got, "<time>")
	path := filepath.Join("testdata", "plain", name+".txt")
	if *updatePlain {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v: capture it with -update-plain before changing the command", err)
	}
	if got != string(want) {
		t.Errorf("piped output changed:\n--- want\n%s--- got\n%s", want, got)
	}
}
