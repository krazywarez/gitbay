package sshd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/store"
)

// execStatus runs cmd on a new session and returns its exit status and
// stderr; -1 when the session could not run.
func execStatus(client *ssh.Client, cmd string) (int, string) {
	sess, err := client.NewSession()
	if err != nil {
		return -1, err.Error()
	}
	defer sess.Close()
	var stderr bytes.Buffer
	sess.Stderr = &stderr
	err = sess.Run(cmd)
	var exit *ssh.ExitError
	switch {
	case err == nil:
		return 0, stderr.String()
	case errors.As(err, &exit):
		return exit.ExitStatus(), stderr.String()
	}
	return -1, err.Error()
}

// waitClosed fails unless the server closes the client's connection
// within five seconds.
func waitClosed(t *testing.T, client *ssh.Client) {
	t.Helper()
	done := make(chan struct{})
	go func() { client.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the connection stayed open")
	}
}

// Each exec reads the key again. The rows change behind the store's
// back here, so no revocation is announced and the connection stays up:
// what refuses the command is the per-exec check alone.
func TestExecRevalidatesKey(t *testing.T) {
	ts := newTestServer(t)
	if code, errOut := execStatus(ts.client, "whoami"); code != 0 {
		t.Fatalf("whoami: %d %s", code, errOut)
	}
	if _, err := ts.st.DB.Exec("UPDATE ssh_keys SET scope = 'git' WHERE id = ?", ts.keyID); err != nil {
		t.Fatal(err)
	}
	if code, errOut := execStatus(ts.client, "whoami"); code != 4 || !strings.Contains(errOut, "does not allow control commands") {
		t.Fatalf("whoami after re-scope: %d %q", code, errOut)
	}
	if _, err := ts.st.DB.Exec("DELETE FROM ssh_keys WHERE id = ?", ts.keyID); err != nil {
		t.Fatal(err)
	}
	if code, errOut := execStatus(ts.client, "whoami"); code != 4 || !strings.Contains(errOut, "no longer registered") {
		t.Fatalf("whoami after delete: %d %q", code, errOut)
	}
}

// Removing the key cuts the connection, ending a command running on it.
func TestRemoveKeyCutsConnection(t *testing.T) {
	ts := newTestServer(t)
	withBuild(t, ts)
	var stderr bytes.Buffer
	sess := startFollow(t, ts.client, &stderr)
	if err := ts.st.RemoveSSHKey(ts.uid, ts.fp); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- sess.Wait() }()
	select {
	case err := <-waited:
		if err == nil {
			t.Fatal("the follow exited cleanly after its key was removed")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the follow outlived its key")
	}
	waitClosed(t, ts.client)
}

func TestDisableCutsConnection(t *testing.T) {
	ts := newTestServer(t)
	if err := ts.st.SetUserDisabled(ts.uid, true); err != nil {
		t.Fatal(err)
	}
	waitClosed(t, ts.client)
}

// A revocation made by another process is not announced here; the
// sweep finds it. A live key survives the sweep.
func TestSweepCutsOutOfProcessRevocation(t *testing.T) {
	ts := newTestServer(t)
	ts.srv.sweepOnce()
	if code, errOut := execStatus(ts.client, "whoami"); code != 0 {
		t.Fatalf("the sweep cut a live key: %d %s", code, errOut)
	}
	if _, err := ts.st.DB.Exec("UPDATE users SET disabled = 1 WHERE id = ?", ts.uid); err != nil {
		t.Fatal(err)
	}
	ts.srv.sweepOnce()
	waitClosed(t, ts.client)
}

// A key that expires while connected: the next exec is refused, and
// the sweep closes the connection.
func TestExpiredKeyRefusedAndCut(t *testing.T) {
	ts := newTestServer(t)
	past := time.Now().Add(-time.Second).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := ts.st.DB.Exec("UPDATE ssh_keys SET expires_at = ? WHERE id = ?", past, ts.keyID); err != nil {
		t.Fatal(err)
	}
	if code, errOut := execStatus(ts.client, "whoami"); code != 4 || !strings.Contains(errOut, "expired") {
		t.Fatalf("whoami with an expired key: %d %q", code, errOut)
	}
	ts.srv.sweepOnce()
	waitClosed(t, ts.client)
}

// An expiring key may not mint.
func TestExpiringKeyCannotMint(t *testing.T) {
	ts := newTestServer(t)
	future := time.Now().Add(time.Hour).UTC().Format("2006-01-02T15:04:05.000Z")
	if _, err := ts.st.DB.Exec("UPDATE ssh_keys SET expires_at = ? WHERE id = ?", future, ts.keyID); err != nil {
		t.Fatal(err)
	}
	if code, errOut := execStatus(ts.client, "token create --name x"); code != 4 || !strings.Contains(errOut, "expires") {
		t.Fatalf("token create with an expiring key: %d %q", code, errOut)
	}
}

func TestExpiredKeyRefusedAtAuth(t *testing.T) {
	ts := newTestServer(t)
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	pub := signer.PublicKey()
	past := time.Now().Add(-time.Minute)
	if err := ts.st.AddSSHKeyFrom(ts.uid, ssh.FingerprintSHA256(pub), pub.Type(), pub.Marshal(), "full", "", store.KeyOrigin{ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	_, err = ssh.Dial("tcp", ts.client.RemoteAddr().String(), &ssh.ClientConfig{
		User:            "git",
		Auth:            []ssh.AuthMethod{ssh.PublicKeys(signer)},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         5 * time.Second,
	})
	if err == nil {
		t.Fatal("an expired key authenticated")
	}
}
