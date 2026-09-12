package control

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// A snippet already at maxSnippetFiles refuses a new name but still
// accepts a replacement of one it already holds.
func TestSnippetFileSetRefusesThe65thFile(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	snID, err := st.CreateSnippet(uid, "abc123abc123", "", "unlisted", "f0", []byte("x\n"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i < maxSnippetFiles; i++ {
		if err := st.SetSnippetFile(snID, fmt.Sprintf("f%d", i), []byte("x\n")); err != nil {
			t.Fatal(err)
		}
	}

	ctx := func() (*Ctx, *bytes.Buffer) {
		var out bytes.Buffer
		return &Ctx{
			User:   store.User{ID: uid, Username: "alice"},
			Scope:  "full",
			Store:  st,
			Cfg:    config.Config{Limits: config.Limits{MaxSnippetBytes: 1 << 20}},
			Stdin:  strings.NewReader("x\n"),
			Stdout: &out,
			Stderr: &out,
		}, &out
	}

	c, out := ctx()
	if code := runSnippetFileSet(c, []string{"abc123abc123", "new.txt"}); code != protocol.ExitUsage ||
		!strings.Contains(out.String(), strconv.Itoa(maxSnippetFiles)) {
		t.Fatalf("new file at the cap: exit %d, want %d naming %d: %s", code, protocol.ExitUsage, maxSnippetFiles, out.String())
	}

	c, out = ctx()
	if code := runSnippetFileSet(c, []string{"abc123abc123", "f0"}); code != protocol.ExitOK {
		t.Fatalf("replacing an existing file at the cap: exit %d: %s", code, out.String())
	}
}
