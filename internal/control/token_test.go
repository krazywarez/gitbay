package control

import (
	"bytes"
	"slices"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// The minting commands, pinned: adding one to the list, or dropping
// one, is a decision this test makes someone take.
func TestMintingCommandsMarked(t *testing.T) {
	want := []string{
		"admin email verify", "admin invite", "admin user create", "email verify",
		"keys add", "repo deploy-key add", "repo runner add", "token create", "web login",
	}
	var got []string
	for _, cmd := range Commands() {
		if cmd.MintsCredential {
			got = append(got, joinPath(cmd.Path))
		}
	}
	slices.Sort(got)
	if !slices.Equal(got, want) {
		t.Fatalf("MintsCredential on %q, want %q", got, want)
	}
}

// Dispatch refuses before the command runs, so no arguments are needed.
func TestExpiringCredentialCannotMint(t *testing.T) {
	exp := time.Now().Add(time.Hour)
	for _, cmd := range Commands() {
		if !cmd.MintsCredential {
			continue
		}
		var out, errOut bytes.Buffer
		c := &Ctx{User: store.User{ID: 1, Username: "root", IsAdmin: true}, Scope: "full", Expires: &exp, Stdout: &out, Stderr: &errOut}
		if code := Dispatch(c, cmd.Path); code != protocol.ExitDenied || !strings.Contains(errOut.String(), "expires") {
			t.Errorf("%s: exit %d %q, want %d and the reason", joinPath(cmd.Path), code, errOut.String(), protocol.ExitDenied)
		}
	}
}

// #286: at a terminal, a token expiring in the future must not render
// through relAge, which clamps a future time to zero and prints
// "expires just now".
func TestTokenListFutureExpiryAtTerminal(t *testing.T) {
	st, _, uid := newQueueTestRepo(t)
	exp := time.Now().Add(90 * 24 * time.Hour)
	if err := st.CreateAPIToken(uid, "laptop", "h-laptop", "read", &exp, 0); err != nil {
		t.Fatal(err)
	}
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice"})
	c.Term = Term{Cols: 80}
	var out bytes.Buffer
	c.Stdout = &out
	if code := Dispatch(c, []string{"token", "list"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	if strings.Contains(out.String(), "just now") {
		t.Fatalf("token list at a terminal printed \"just now\" for a future expiry:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "expires ") {
		t.Fatalf("token list:\n%s", out.String())
	}
}

func TestTokenCreateDefaultsToReadAndRecordsCreator(t *testing.T) {
	st, _, uid := newQueueTestRepo(t)
	if err := st.CreateAPIToken(uid, "parent", "h-parent", "full", nil, 0); err != nil {
		t.Fatal(err)
	}
	_, parent, err := st.APITokenUser("h-parent")
	if err != nil {
		t.Fatal(err)
	}
	c, errOut := pruneCtx(st, t.TempDir(), store.User{ID: uid, Username: "alice"})
	c.Cfg.Limits.WriteRate = -1
	c.TokenID = parent.ID
	if code := Dispatch(c, []string{"token", "create", "--name", "child"}); code != protocol.ExitOK {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	toks, err := st.ListAPITokens(uid)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, tk := range toks {
		if tk.Name != "child" {
			continue
		}
		found = true
		if tk.Scope != "read" || tk.CreatedBy != "parent" {
			t.Fatalf("child: %+v", tk)
		}
	}
	if !found {
		t.Fatal(`no token named "child"`)
	}
}
