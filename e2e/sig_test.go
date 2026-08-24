package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ProtonMail/go-crypto/openpgp"
	"github.com/ProtonMail/go-crypto/openpgp/armor"
	"github.com/ProtonMail/go-crypto/openpgp/packet"
	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/sig"
)

// --- fixture key helpers -------------------------------------------------

func newPGPKey(t *testing.T, name, email string, cfg *packet.Config) *openpgp.Entity {
	t.Helper()
	e, err := openpgp.NewEntity(name, "", email, cfg)
	if err != nil {
		t.Fatal(err)
	}
	return e
}

func armorPub(t *testing.T, e *openpgp.Entity) string {
	t.Helper()
	var buf bytes.Buffer
	w, err := armor.Encode(&buf, openpgp.PublicKeyType, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Serialize(w); err != nil {
		t.Fatal(err)
	}
	w.Close()
	return buf.String()
}

func pgpSign(t *testing.T, e *openpgp.Entity, payload []byte, cfg *packet.Config) string {
	t.Helper()
	var buf bytes.Buffer
	if err := openpgp.ArmoredDetachSign(&buf, e, bytes.NewReader(payload), cfg); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// --- fixture commit construction ----------------------------------------

type commitSpec struct {
	authorEmail    string
	committerEmail string
	subject        string
	sign           func(payload []byte) string // "" = unsigned
}

// buildCommits writes a chain of hand-constructed commit objects into the
// clone at dir and points refs/heads/main at the tip.
func buildCommits(t *testing.T, dir string, env []string, specs []commitSpec) []string {
	t.Helper()
	tree := strings.TrimSpace(mustGit(t, dir, env, "mktree"))
	return buildChain(t, dir, env, tree, "", specs)
}

// buildChain constructs signed commit objects on top of parent ("" for a
// root commit) using the given tree, and points refs/heads/main at the tip.
func buildChain(t *testing.T, dir string, env []string, tree, parent string, specs []commitSpec) []string {
	t.Helper()
	base := time.Now().Add(-time.Duration(len(specs)) * time.Minute).Unix()
	var shas []string
	for i, spec := range specs {
		if spec.committerEmail == "" {
			spec.committerEmail = spec.authorEmail
		}
		ts := base + int64(i)*60
		var b strings.Builder
		fmt.Fprintf(&b, "tree %s\n", tree)
		if parent != "" {
			fmt.Fprintf(&b, "parent %s\n", parent)
		}
		fmt.Fprintf(&b, "author T <%s> %d +0000\n", spec.authorEmail, ts)
		fmt.Fprintf(&b, "committer T <%s> %d +0000\n", spec.committerEmail, ts)
		payloadTail := fmt.Sprintf("\n%s\n", spec.subject)
		payload := b.String() + payloadTail

		full := payload
		if spec.sign != nil {
			sigText := spec.sign([]byte(payload))
			var sigHeader strings.Builder
			for j, line := range strings.Split(strings.TrimSuffix(sigText, "\n"), "\n") {
				if j == 0 {
					sigHeader.WriteString("gpgsig " + line + "\n")
				} else {
					sigHeader.WriteString(" " + line + "\n")
				}
			}
			full = b.String() + sigHeader.String() + payloadTail
		}

		cmd := exec.Command("git", "hash-object", "-t", "commit", "-w", "--stdin")
		cmd.Dir = dir
		cmd.Env = env
		cmd.Stdin = strings.NewReader(full)
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("hash-object: %v", err)
		}
		parent = strings.TrimSpace(string(out))
		shas = append(shas, parent)
	}
	mustGit(t, dir, env, "update-ref", "refs/heads/main", parent)
	return shas
}

// --- the M4 milestone test ----------------------------------------------

type logEntry struct {
	SHA            string `json:"sha"`
	Subject        string `json:"subject"`
	AuthorEmail    string `json:"author_email"`
	CommitterEmail string `json:"committer_email"`
	Signature      struct {
		State  string `json:"state"`
		Signer string `json:"signer"`
	} `json:"signature"`
}

func (i *instance) repoLog(t *testing.T, key, repo string) map[string]logEntry {
	t.Helper()
	out, errOut, code := i.ssh(t, key, "", "repo", "log", repo, "--json")
	if code != 0 {
		t.Fatalf("repo log: exit %d, %s", code, errOut)
	}
	var env struct {
		Data []logEntry `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("repo log JSON: %v\n%s", err, out)
	}
	byShaOrSubject := map[string]logEntry{}
	for _, e := range env.Data {
		byShaOrSubject[e.Subject] = e
	}
	return byShaOrSubject
}

func TestSignatureVerification(t *testing.T) {
	inst := startInstance(t)

	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	// bob: registered SSH key, email NOT yet verified.
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "bob",
		"--key", bobKey+".pub", "--email", "bob@example.test")

	// PGP keys.
	now := time.Now()
	aliceEnt := newPGPKey(t, "Alice", "alice@example.test", nil)
	malloryEnt := newPGPKey(t, "Mallory", "mallory@example.test", nil)

	past := now.Add(-2 * time.Hour)
	expiredCfg := &packet.Config{Time: func() time.Time { return past }, KeyLifetimeSecs: 3600}
	expiredEnt := newPGPKey(t, "Alice Old", "alice@example.test", expiredCfg)

	// The "revoked" key signs its commit first and is revoked before
	// registration: go-crypto (correctly) refuses to sign with a revoked key.
	revokedEnt := newPGPKey(t, "Alice Revoked", "alice@example.test", nil)

	// Register alice's current and expired keys on her account.
	for _, ent := range []*openpgp.Entity{aliceEnt, expiredEnt} {
		_, errOut, code := inst.ssh(t, aliceKey, armorPub(t, ent), "pgp", "add")
		if code != 0 {
			t.Fatalf("pgp add: %s", errOut)
		}
	}

	// Alice's SSH signer for SSHSIG commits; bob's too.
	aliceSSHRaw, _ := os.ReadFile(aliceKey)
	aliceSigner, err := ssh.ParsePrivateKey(aliceSSHRaw)
	if err != nil {
		t.Fatal(err)
	}
	bobSSHRaw, _ := os.ReadFile(bobKey)
	bobSigner, err := ssh.ParsePrivateKey(bobSSHRaw)
	if err != nil {
		t.Fatal(err)
	}

	// Repo + working clone.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/sig"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/sig"), "w")
	dir := filepath.Join(work, "w")

	sigCfg := &packet.Config{}
	expiredSigCfg := &packet.Config{Time: func() time.Time { return past.Add(10 * time.Minute) }}
	var verifiedPayloadSig string // captured to build the bad-signature commit

	specs := []commitSpec{
		{authorEmail: "alice@example.test", subject: "unsigned"},
		{authorEmail: "alice@example.test", subject: "verified-pgp", sign: func(p []byte) string {
			verifiedPayloadSig = pgpSign(t, aliceEnt, p, sigCfg)
			return verifiedPayloadSig
		}},
		{authorEmail: "mallory@example.test", subject: "unknown-key", sign: func(p []byte) string {
			return pgpSign(t, malloryEnt, p, sigCfg)
		}},
		{authorEmail: "eve@example.test", subject: "email-mismatch", sign: func(p []byte) string {
			return pgpSign(t, aliceEnt, p, sigCfg)
		}},
		{authorEmail: "alice@example.test", subject: "expired-key", sign: func(p []byte) string {
			return pgpSign(t, expiredEnt, p, expiredSigCfg)
		}},
		{authorEmail: "alice@example.test", subject: "revoked-key", sign: func(p []byte) string {
			return pgpSign(t, revokedEnt, p, sigCfg)
		}},
		{authorEmail: "alice@example.test", subject: "bad-signature", sign: func(p []byte) string {
			return verifiedPayloadSig // valid armor, wrong payload
		}},
		{authorEmail: "alice@example.test", committerEmail: "other@example.test", subject: "verified-sshsig", sign: func(p []byte) string {
			s, err := sig.MarshalSSHSig(aliceSigner, p)
			if err != nil {
				t.Fatal(err)
			}
			return string(s)
		}},
		{authorEmail: "bob@example.test", subject: "sshsig-unverified-email", sign: func(p []byte) string {
			s, err := sig.MarshalSSHSig(bobSigner, p)
			if err != nil {
				t.Fatal(err)
			}
			return string(s)
		}},
	}
	buildCommits(t, dir, env, specs)
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	// Now revoke the key and register it: revocation predates verification,
	// which is what the revoked state is about.
	if err := revokedEnt.RevokeKey(packet.KeyCompromised, "test", nil); err != nil {
		t.Fatal(err)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, armorPub(t, revokedEnt), "pgp", "add"); code != 0 {
		t.Fatalf("pgp add revoked: %s", errOut)
	}

	// Golden state check: one commit per state.
	want := map[string]struct {
		state  string
		signer string
	}{
		"unsigned":                {"unsigned", ""},
		"verified-pgp":            {"verified", "alice"},
		"unknown-key":             {"signed_unknown_key", ""},
		"email-mismatch":          {"signed_email_mismatch", "alice"},
		"expired-key":             {"signed_key_expired", "alice"},
		"revoked-key":             {"signed_key_revoked", "alice"},
		"bad-signature":           {"bad_signature", "alice"},
		"verified-sshsig":         {"verified", "alice"},
		"sshsig-unverified-email": {"signed_email_mismatch", "bob"},
	}
	check := func(log map[string]logEntry, subjects ...string) {
		t.Helper()
		for _, subj := range subjects {
			e, ok := log[subj]
			if !ok {
				t.Fatalf("commit %q missing from log", subj)
			}
			w := want[subj]
			if e.Signature.State != w.state || e.Signature.Signer != w.signer {
				t.Errorf("%s: state=%s signer=%q, want state=%s signer=%q",
					subj, e.Signature.State, e.Signature.Signer, w.state, w.signer)
			}
		}
	}
	log := inst.repoLog(t, aliceKey, "alice/sig")
	subjects := make([]string, 0, len(want))
	for s := range want {
		subjects = append(subjects, s)
	}
	check(log, subjects...)

	// Committer email surfaces only when it differs from the author.
	if log["verified-sshsig"].CommitterEmail != "other@example.test" {
		t.Errorf("differing committer email not surfaced: %+v", log["verified-sshsig"])
	}
	if log["unsigned"].CommitterEmail != "" {
		t.Errorf("identical committer email should be omitted: %+v", log["unsigned"])
	}

	// Epoch transition 1: registering mallory (key + verified email)
	// upgrades the cached signed_unknown_key row to verified.
	malloryKey := inst.newKey(t, "mallory")
	inst.admin(t, "admin", "user", "create", "mallory",
		"--key", malloryKey+".pub", "--email", "mallory@example.test", "--verified")
	if _, errOut, code := inst.ssh(t, malloryKey, armorPub(t, malloryEnt), "pgp", "add"); code != 0 {
		t.Fatalf("mallory pgp add: %s", errOut)
	}
	want["unknown-key"] = struct {
		state  string
		signer string
	}{"verified", "mallory"}
	check(inst.repoLog(t, aliceKey, "alice/sig"), "unknown-key")

	// Epoch transition 2: verifying bob's email upgrades his SSHSIG commit.
	inst.admin(t, "admin", "email", "verify", "bob", "bob@example.test")
	want["sshsig-unverified-email"] = struct {
		state  string
		signer string
	}{"verified", "bob"}
	check(inst.repoLog(t, aliceKey, "alice/sig"), "sshsig-unverified-email")

	// Epoch transition 3: removing alice's PGP key downgrades her verified
	// commit; re-adding restores it.
	fpr := fmt.Sprintf("%x", aliceEnt.PrimaryKey.Fingerprint)
	if _, errOut, code := inst.ssh(t, aliceKey, "", "pgp", "remove", fpr); code != 0 {
		t.Fatalf("pgp remove: %s", errOut)
	}
	if got := inst.repoLog(t, aliceKey, "alice/sig")["verified-pgp"].Signature.State; got != "signed_unknown_key" {
		t.Errorf("after key removal: verified-pgp state = %s, want signed_unknown_key", got)
	}
	if _, errOut, code := inst.ssh(t, aliceKey, armorPub(t, aliceEnt), "pgp", "add"); code != 0 {
		t.Fatalf("pgp re-add: %s", errOut)
	}
	check(inst.repoLog(t, aliceKey, "alice/sig"), "verified-pgp")

	// Cross-check payload reconstruction against git itself, when gpg is
	// available: git verify-commit must agree the signature is valid.
	if gpgPath, err := exec.LookPath("gpg"); err == nil {
		gnupgHome := t.TempDir()
		gpgEnv := append(env, "GNUPGHOME="+gnupgHome)
		imp := exec.Command(gpgPath, "--batch", "--import")
		imp.Env = gpgEnv
		imp.Stdin = strings.NewReader(armorPub(t, aliceEnt))
		// gpg exits nonzero if it cannot reach its agent, even when the
		// import itself succeeded; trust the summary line instead.
		if out, err := imp.CombinedOutput(); err != nil && !strings.Contains(string(out), "imported: 1") {
			t.Fatalf("gpg import: %v\n%s", err, out)
		}
		var sha string
		for _, e := range inst.repoLog(t, aliceKey, "alice/sig") {
			if e.Subject == "verified-pgp" {
				sha = e.SHA
			}
		}
		vc := exec.Command("git", "verify-commit", sha)
		vc.Dir = dir
		vc.Env = gpgEnv
		if out, err := vc.CombinedOutput(); err != nil {
			t.Errorf("git verify-commit disagrees with gitbay verification: %v\n%s", err, out)
		}
	} else {
		t.Log("gpg not installed; skipping git verify-commit cross-check")
	}
}
