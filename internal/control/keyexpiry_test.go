package control

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// authorizedKey is a fresh public key as an authorized_keys line.
func authorizedKey(t *testing.T, comment string) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sp))) + " " + comment + "\n"
}

func TestKeysAddTTLAndList(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	user := store.User{ID: uid, Username: "alice"}
	run := func(stdin string, argv ...string) (string, string, int) {
		c, errOut := pruneCtx(st, t.TempDir(), user)
		c.Cfg.Limits.WriteRate = -1
		c.Stdin = strings.NewReader(stdin)
		code := Dispatch(c, argv)
		return c.Stdout.(*bytes.Buffer).String(), errOut.String(), code
	}
	if _, errOut, code := run(authorizedKey(t, "laptop"), "keys", "add", "--ttl", "1h"); code != protocol.ExitOK {
		t.Fatalf("keys add --ttl: %d %s", code, errOut)
	}
	if _, errOut, code := run(authorizedKey(t, "ci"), "repo", "deploy-key", "add", repo.Path(), "--ttl", "2d"); code != protocol.ExitOK {
		t.Fatalf("deploy-key add --ttl: %d %s", code, errOut)
	}
	if _, _, code := run(authorizedKey(t, "x"), "keys", "add", "--ttl", "soon"); code != protocol.ExitUsage {
		t.Fatalf("bad ttl: exit %d", code)
	}

	keys, err := st.ListSSHKeys(uid)
	if err != nil || len(keys) != 2 {
		t.Fatalf("keys: %+v %v", keys, err)
	}
	for _, k := range keys {
		if k.ExpiresAt == nil || k.ExpiresAt.Before(time.Now()) || k.ExpiresAt.After(time.Now().Add(49*time.Hour)) {
			t.Errorf("%s expires %v", k.Label, k.ExpiresAt)
		}
	}
	out, _, _ := run("", "keys", "list")
	if !strings.Contains(out, "\tlaptop\tnever used\texpires ") {
		t.Fatalf("keys list:\n%s", out)
	}
	out, _, _ = run("", "repo", "deploy-key", "list", repo.Path())
	if !strings.Contains(out, "\tci\tnever used\texpires ") {
		t.Fatalf("deploy-key list:\n%s", out)
	}
}
