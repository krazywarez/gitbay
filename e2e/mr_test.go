package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"gitbay.org/gitbay/internal/sig"
)

type mrShow struct {
	Number  int64  `json:"number"`
	State   string `json:"state"`
	Source  string `json:"source"`
	HeadSHA string `json:"head_sha"`
	Reviews []struct {
		Reviewer string `json:"reviewer"`
		Verdict  string `json:"verdict"`
		Stale    bool   `json:"stale"`
	} `json:"reviews"`
}

func (i *instance) mrShow(t *testing.T, key, repo, n string) mrShow {
	t.Helper()
	out, errOut, code := i.ssh(t, key, "", "mr", "show", repo, n, "--json")
	if code != 0 {
		t.Fatalf("mr show: exit %d, %s", code, errOut)
	}
	var env struct {
		Data mrShow `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil {
		t.Fatalf("mr show JSON: %v\n%s", err, out)
	}
	return env.Data
}

func TestMergeRequests(t *testing.T) {
	inst := startInstance(t)

	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob",
		"--key", bobKey+".pub", "--email", "bob@example.test", "--verified")

	// Alice's upstream repo with an initial commit.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/lib"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	aliceEnv := inst.gitEnv(aliceKey)
	aliceWork := t.TempDir()
	mustGit(t, aliceWork, aliceEnv, "clone", inst.sshURL("alice/lib"), "w")
	aliceDir := filepath.Join(aliceWork, "w")
	os.WriteFile(filepath.Join(aliceDir, "lib.txt"), []byte("v1\n"), 0o644)
	mustGit(t, aliceDir, aliceEnv, "checkout", "-q", "-b", "main")
	mustGit(t, aliceDir, aliceEnv, "add", ".")
	mustGit(t, aliceDir, aliceEnv, "commit", "-q", "-m", "base")
	mustGit(t, aliceDir, aliceEnv, "push", "-q", "origin", "main")

	// Bob forks and pushes a feature branch to his fork.
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "fork", "alice/lib"); code != 0 {
		t.Fatalf("fork: %s", errOut)
	}
	bobEnv := inst.gitEnv(bobKey)
	bobWork := t.TempDir()
	mustGit(t, bobWork, bobEnv, "clone", inst.sshURL("bob/lib"), "w")
	bobDir := filepath.Join(bobWork, "w")
	mustGit(t, bobDir, bobEnv, "checkout", "-q", "-b", "feature", "origin/main")
	os.WriteFile(filepath.Join(bobDir, "feature.txt"), []byte("bob's work\n"), 0o644)
	mustGit(t, bobDir, bobEnv, "add", ".")
	mustGit(t, bobDir, bobEnv, "commit", "-q", "-m", "add feature")
	mustGit(t, bobDir, bobEnv, "push", "-q", "origin", "feature")

	// MR from the fork into alice/lib.
	out, errOut, code := inst.ssh(t, bobKey, "", "mr", "create", "alice/lib",
		"--source", "bob/lib:feature", "--target", "main", "--title", "'add feature'", "--json")
	if code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}
	if !strings.Contains(out, `"number":1`) {
		t.Fatalf("mr create output: %s", out)
	}

	// The MR head ref is fetchable from the TARGET repo by a reader.
	fetchDir := t.TempDir()
	mustGit(t, fetchDir, aliceEnv, "clone", "-q", inst.sshURL("alice/lib"), "c")
	mustGit(t, filepath.Join(fetchDir, "c"), aliceEnv, "fetch", "-q", "origin", "refs/merge-requests/1/head")

	// Alice approves.
	if _, errOut, code = inst.ssh(t, aliceKey, "", "mr", "review", "alice/lib", "1", "--approve"); code != 0 {
		t.Fatalf("review: %s", errOut)
	}
	show := inst.mrShow(t, aliceKey, "alice/lib", "1")
	if len(show.Reviews) != 1 || show.Reviews[0].Stale {
		t.Fatalf("fresh review wrong: %+v", show.Reviews)
	}
	firstHead := show.HeadSHA

	// Bob force-pushes the source branch: the MR head updates and the
	// review goes stale.
	mustGit(t, bobDir, bobEnv, "commit", "-q", "--amend", "-m", "add feature (amended)")
	mustGit(t, bobDir, bobEnv, "push", "-q", "--force", "origin", "feature")
	show = inst.mrShow(t, aliceKey, "alice/lib", "1")
	if show.HeadSHA == firstHead {
		t.Fatal("MR head not updated after force-push")
	}
	if len(show.Reviews) != 1 || !show.Reviews[0].Stale {
		t.Fatalf("review not marked stale: %+v", show.Reviews)
	}

	// Target advances, so fast-forward is impossible: default merge makes a
	// merge commit authored by the merging user.
	mustGit(t, aliceDir, aliceEnv, "commit", "-q", "--allow-empty", "-m", "mainline moves on")
	mustGit(t, aliceDir, aliceEnv, "push", "-q", "origin", "main")
	out, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/lib", "1", "--json")
	if code != 0 {
		t.Fatalf("merge: exit %d, %s", code, errOut)
	}
	if !strings.Contains(out, `"strategy":"merge"`) {
		t.Fatalf("expected merge-commit strategy: %s", out)
	}
	if inst.mrShow(t, aliceKey, "alice/lib", "1").State != "merged" {
		t.Fatal("MR not marked merged")
	}
	mustGit(t, aliceDir, aliceEnv, "pull", "-q", "origin", "main")
	if _, err := os.Stat(filepath.Join(aliceDir, "feature.txt")); err != nil {
		t.Fatal("merged content missing from main")
	}
	// The merge commit carries the merging user's identity and is unsigned.
	tip := strings.TrimSpace(mustGit(t, aliceDir, aliceEnv, "log", "-1", "--format=%an <%ae>"))
	if tip != "alice <alice@example.test>" {
		t.Fatalf("merge commit identity: %q", tip)
	}
	logOut, _, _ := inst.ssh(t, aliceKey, "", "repo", "log", "alice/lib", "--limit", "1")
	if !strings.Contains(logOut, "unsigned") {
		t.Fatalf("merge commit should display unsigned:\n%s", logOut)
	}

	// --- require_signed_commits: push-time and merge-time policy ---

	if _, errOut, code = inst.ssh(t, aliceKey, "", "repo", "create", "alice/sec"); code != 0 {
		t.Fatalf("create sec: %s", errOut)
	}
	if _, errOut, code = inst.ssh(t, aliceKey, "", "repo", "settings", "require-signed", "alice/sec", "on"); code != 0 {
		t.Fatalf("require-signed: %s", errOut)
	}
	secWork := t.TempDir()
	mustGit(t, secWork, aliceEnv, "clone", inst.sshURL("alice/sec"), "w")
	secDir := filepath.Join(secWork, "w")

	// Unsigned push is rejected at pre-receive.
	os.WriteFile(filepath.Join(secDir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, secDir, aliceEnv, "checkout", "-q", "-b", "main")
	mustGit(t, secDir, aliceEnv, "add", ".")
	mustGit(t, secDir, aliceEnv, "commit", "-q", "-m", "unsigned attempt")
	pushOut, pushCode := gitRun(t, secDir, aliceEnv, "push", "origin", "main")
	if pushCode == 0 {
		t.Fatal("unsigned push accepted into require-signed repo")
	}
	if !strings.Contains(pushOut, "requires signed commits") {
		t.Fatalf("unsigned push message:\n%s", pushOut)
	}

	// SSHSIG-signed commits go through.
	raw, _ := os.ReadFile(aliceKey)
	signer, err := ssh.ParsePrivateKey(raw)
	if err != nil {
		t.Fatal(err)
	}
	signAlice := func(p []byte) string {
		s, err := sig.MarshalSSHSig(signer, p)
		if err != nil {
			t.Fatal(err)
		}
		return string(s)
	}
	buildCommits(t, secDir, aliceEnv, []commitSpec{
		{authorEmail: "alice@example.test", subject: "signed base", sign: signAlice},
	})
	mustGit(t, secDir, aliceEnv, "push", "-q", "origin", "main")

	// A signed feature branch and a same-repo MR.
	base := strings.TrimSpace(mustGit(t, secDir, aliceEnv, "rev-parse", "main"))
	tree := strings.TrimSpace(mustGit(t, secDir, aliceEnv, "rev-parse", "main^{tree}"))
	buildChain(t, secDir, aliceEnv, tree, base, []commitSpec{
		{authorEmail: "alice@example.test", subject: "signed feature", sign: signAlice},
	})
	// buildChain moved refs/heads/main; restore and use a feature branch.
	feat := strings.TrimSpace(mustGit(t, secDir, aliceEnv, "rev-parse", "main"))
	mustGit(t, secDir, aliceEnv, "update-ref", "refs/heads/main", base)
	mustGit(t, secDir, aliceEnv, "update-ref", "refs/heads/feat", feat)
	mustGit(t, secDir, aliceEnv, "push", "-q", "origin", "feat")

	if _, errOut, code = inst.ssh(t, aliceKey, "", "mr", "create", "alice/sec",
		"--source", "feat", "--target", "main", "--title", "'signed work'"); code != 0 {
		t.Fatalf("sec mr create: %s", errOut)
	}

	// An explicit merge-commit strategy is refused with exit 4 and rebase
	// instructions.
	_, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/sec", "1", "--strategy", "merge")
	if code != 4 {
		t.Fatalf("merge-commit on require-signed: exit %d (want 4), %s", code, errOut)
	}
	if !strings.Contains(errOut, "only fast-forward") || !strings.Contains(errOut, "rebase") {
		t.Fatalf("refusal message: %s", errOut)
	}

	// Fast-forward merge of verified commits succeeds.
	out, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/sec", "1", "--json")
	if code != 0 {
		t.Fatalf("ff merge: %s", errOut)
	}
	if !strings.Contains(out, `"strategy":"ff"`) {
		t.Fatalf("expected ff: %s", out)
	}

	// --- fork deletion leaves the MR diff intact ---

	mustGit(t, bobDir, bobEnv, "checkout", "-q", "-b", "second", "origin/main")
	os.WriteFile(filepath.Join(bobDir, "second.txt"), []byte("more\n"), 0o644)
	mustGit(t, bobDir, bobEnv, "add", ".")
	mustGit(t, bobDir, bobEnv, "commit", "-q", "-m", "second feature")
	mustGit(t, bobDir, bobEnv, "push", "-q", "origin", "second")
	if _, errOut, code = inst.ssh(t, bobKey, "", "mr", "create", "alice/lib",
		"--source", "bob/lib:second", "--target", "main", "--title", "'second'"); code != 0 {
		t.Fatalf("mr 2 create: %s", errOut)
	}
	if _, errOut, code = inst.ssh(t, bobKey, "", "repo", "delete", "bob/lib", "--yes"); code != 0 {
		t.Fatalf("fork delete: %s", errOut)
	}
	show = inst.mrShow(t, aliceKey, "alice/lib", "2")
	if show.State != "source_gone" {
		t.Fatalf("MR 2 state after fork deletion: %s", show.State)
	}
	diffOut, errOut, code := inst.ssh(t, aliceKey, "", "mr", "diff", "alice/lib", "2")
	if code != 0 || !strings.Contains(diffOut, "second.txt") {
		t.Fatalf("diff after fork deletion: exit %d\n%s%s", code, diffOut, errOut)
	}
	// And it can still be merged: the target owns the objects.
	if _, errOut, code = inst.ssh(t, aliceKey, "", "mr", "merge", "alice/lib", "2"); code != 0 {
		t.Fatalf("merge after fork deletion: %s", errOut)
	}

	// Merged MRs keep their historical diff: after the fast-forward the
	// live merge-base equals the head, so the recorded base must be used.
	diffOut2, _, code := inst.ssh(t, aliceKey, "", "mr", "diff", "alice/lib", "1")
	if code != 0 || !strings.Contains(diffOut2, "feature.txt") {
		t.Fatalf("post-merge diff empty: %d\n%s", code, diffOut2)
	}

	// Web read views.
	status, body := inst.get(t, "/alice/lib/mrs?state=all")
	if status != 200 || !strings.Contains(body, "add feature") || !strings.Contains(body, "second") {
		t.Fatalf("mrs page: %d\n%s", status, body)
	}
	status, body = inst.get(t, "/alice/lib/mrs/1")
	if status != 200 || !strings.Contains(body, "stale") || !strings.Contains(body, "merged") {
		t.Fatalf("mr detail: %d\n%s", status, body)
	}
	if !strings.Contains(body, "feature.txt") {
		t.Fatalf("merged MR web diff empty:\n%s", body)
	}
	// The MR page lists the commits it carries, linked to commit pages.
	if !strings.Contains(body, ">commits <") || !strings.Contains(body, "/alice/lib/commit/") {
		t.Fatalf("mr commits section missing:\n%s", body)
	}
	// So does mr show, human and JSON.
	showOut, _, code := inst.ssh(t, aliceKey, "", "mr", "show", "alice/lib", "1")
	if code != 0 || !strings.Contains(showOut, "commit: ") {
		t.Fatalf("mr show missing commits: %d\n%s", code, showOut)
	}
	showJSON, _, _ := inst.ssh(t, aliceKey, "", "mr", "show", "alice/lib", "1", "--json")
	if !strings.Contains(showJSON, `"commits":[`) || !strings.Contains(showJSON, `"subject"`) {
		t.Fatalf("mr show json missing commits: %s", showJSON)
	}
}
