package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A merge request head is fetched into the target repository, and the
// target's push jobs run against it there, so a fork's merge request has
// ci/<job> statuses for require-checks to gate on. Builds used to queue
// only for branch pushes to the pushed repository, which left a fork's
// merge request unbuildable and, under require-checks, unmergeable (#98).
// A head from another repository runs without the target's secrets.
func TestForkMRHeadIsBuilt(t *testing.T) {
	inst := startInstance(t)
	inst.runner = buildRunner(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	runnerKey := inst.newKey(t, "ci")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub",
		"--email", "alice@example.test", "--verified")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	inst.admin(t, "admin", "user", "create", "ci", "--key", runnerKey+".pub", "--admin")

	// alice/app: two push jobs, a secret, require-checks.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.MkdirAll(filepath.Join(dir, ".gitbay"), 0o755)
	os.WriteFile(filepath.Join(dir, ".gitbay", "ci.yml"), []byte(
		"jobs:\n  one:\n    steps:\n      - test -z \"$TOKEN\"\n  two:\n    steps:\n      - echo two\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	// The branch push queued two builds for main; clear them so the
	// queue holds only what the merge request adds.
	for _, n := range []string{"1", "2"} {
		if _, _, code := inst.ssh(t, aliceKey, "", "build", "cancel", "alice/app", n); code != 0 {
			t.Fatalf("build cancel %s failed", n)
		}
	}
	if _, _, code := inst.ssh(t, aliceKey, "s3cret\n", "repo", "secret", "set", "alice/app", "TOKEN"); code != 0 {
		t.Fatal("secret set failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "settings", "require-checks", "alice/app", "on"); code != 0 {
		t.Fatal("require-checks failed")
	}

	// bob forks, pushes a branch to the fork, opens the merge request.
	if _, errOut, code := inst.ssh(t, bobKey, "", "repo", "fork", "alice/app"); code != 0 {
		t.Fatalf("fork: %s", errOut)
	}
	bwork := t.TempDir()
	benv := inst.gitEnv(bobKey)
	mustGit(t, bwork, benv, "clone", inst.sshURL("bob/app"), "w")
	bdir := filepath.Join(bwork, "w")
	mustGit(t, bdir, benv, "checkout", "-q", "-b", "feat")
	os.WriteFile(filepath.Join(bdir, "f.txt"), []byte("y\n"), 0o644)
	mustGit(t, bdir, benv, "add", ".")
	mustGit(t, bdir, benv, "commit", "-q", "-m", "change")
	mustGit(t, bdir, benv, "push", "-q", "origin", "feat")
	head := strings.TrimSpace(mustGit(t, bdir, benv, "rev-parse", "HEAD"))
	// The fork carries the same ci.yml, so bob's push queued its own
	// builds; cancel them so the runner's next claim is the target's.
	for _, n := range []string{"1", "2"} {
		if _, _, code := inst.ssh(t, bobKey, "", "build", "cancel", "bob/app", n); code != 0 {
			t.Fatalf("build cancel bob/app %s failed", n)
		}
	}
	if _, errOut, code := inst.ssh(t, bobKey, "", "mr", "create", "alice/app",
		"--source", "bob/app:feat", "--target", "main", "--title", "change"); code != 0 {
		t.Fatalf("mr create: %s", errOut)
	}

	// Two builds queued in the target, at the merge request ref.
	out, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app", "--json")
	if strings.Count(out, `"status":"pending"`) != 2 || !strings.Contains(out, `"ref":"refs/merge-requests/1/head"`) {
		t.Fatalf("expected two pending builds at the MR ref:\n%s", out)
	}
	if strings.Count(out, head[:10]) < 2 {
		t.Fatalf("builds are not for the MR head %s:\n%s", head[:10], out)
	}

	// The claim carries no secrets for a head from another repository.
	out, errOut, code := inst.ssh(t, runnerKey, "", "runner", "next", "alice/app", "--json")
	if code != 0 {
		t.Fatalf("runner next: %s", errOut)
	}
	var claim struct {
		Data struct {
			ID      int64             `json:"id"`
			Job     string            `json:"job"`
			Secrets map[string]string `json:"secrets"`
		} `json:"data"`
	}
	json.Unmarshal([]byte(out), &claim)
	if claim.Data.Job != "one" || len(claim.Data.Secrets) != 0 {
		t.Fatalf("fork build claimed with secrets or wrong job:\n%s", out)
	}
	if _, _, code := inst.ssh(t, runnerKey, "", "runner", "done", fmt.Sprint(claim.Data.ID), "success"); code != 0 {
		t.Fatal("runner done failed")
	}

	// The real runner fetches the merge request ref and runs the second job.
	log := inst.runnerOnce(t, runnerKey)
	if !strings.Contains(log, "two") {
		t.Fatalf("runner did not run the second job:\n%s", log)
	}
	out, errOut, _ = inst.ssh(t, aliceKey, "", "status", "list", "alice/app", head, "--json")
	if strings.Count(out, `"state":"success"`) != 2 {
		builds, _, _ := inst.ssh(t, aliceKey, "", "build", "list", "alice/app", "--json")
		t.Fatalf("statuses on the MR head:\n%s%s\nbuilds:\n%s\nrunner log:\n%s", out, errOut, builds, log)
	}

	// require-checks is satisfied by the builds on the head.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "mr", "merge", "alice/app", "1"); code != 0 {
		t.Fatalf("merge under require-checks: %s", errOut)
	}
}
