package control

import (
	"bytes"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// TestCommandParseFailures runs argv that every command must refuse
// before touching the store: an unknown flag, a flag with no value, too
// many arguments, a value outside its range. Each is exit 2 with the
// command's usage in the message (#129, #96).
func TestCommandParseFailures(t *testing.T) {
	admin := store.User{Username: "root", IsAdmin: true}
	cases := []struct {
		argv []string
		user store.User
		want string // substring of the message
	}{
		{[]string{"issue", "create", "a/b", "--bogus"}, store.User{}, "unknown flag"},
		{[]string{"issue", "create", "a/b", "--title"}, store.User{}, "requires a value"},
		{[]string{"issue", "list", "a/b", "c/d"}, store.User{}, "unexpected argument"},
		{[]string{"mr", "create", "a/b", "--source"}, store.User{}, "requires a value"},
		{[]string{"mr", "merge", "a/b", "1", "--strategy"}, store.User{}, "requires a value"},
		{[]string{"mr", "review", "a/b", "1", "--bogus"}, store.User{}, "usage"},
		{[]string{"repo", "grep", "a/b", "x", "y"}, store.User{}, "unexpected argument"},
		{[]string{"repo", "create", "a/b", "--visibility", "x"}, store.User{}, "unknown flag"},
		{[]string{"repo", "log", "a/b", "--limit", "0"}, store.User{}, "--limit must be"},
		{[]string{"keys", "add", "--scope"}, store.User{}, "requires a value"},
		{[]string{"token", "create", "extra"}, store.User{}, "unexpected argument"},
		{[]string{"webhook", "deliveries", "a/b", "--limit", "500"}, store.User{}, "--limit must be"},
		{[]string{"admin", "repo", "list", "--visibility", "secret"}, admin, "--visibility requires"},
		{[]string{"admin", "user", "list", "extra"}, admin, "unexpected argument"},
		{[]string{"audit", "--limit", "0"}, admin, "--limit must be"},
		{[]string{"account", "import-bundle", "x"}, store.User{}, "unexpected argument"},
	}
	for _, tc := range cases {
		var out, errOut bytes.Buffer
		c := &Ctx{User: tc.user, Scope: "full", Stdout: &out, Stderr: &errOut}
		code := Dispatch(c, tc.argv)
		if code != protocol.ExitUsage {
			t.Errorf("%v: exit %d, want %d (%s)", tc.argv, code, protocol.ExitUsage, strings.TrimSpace(errOut.String()))
			continue
		}
		if !strings.Contains(errOut.String(), tc.want) {
			t.Errorf("%v: message %q lacks %q", tc.argv, strings.TrimSpace(errOut.String()), tc.want)
		}
	}
}
