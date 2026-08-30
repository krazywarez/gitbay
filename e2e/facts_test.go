package e2e

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestRepoFacts covers the repository summary: counts, license, languages,
// and contributors resolved to accounts where the email is verified.
func TestRepoFacts(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	aliceEnv := append(append([]string{}, env...),
		"GIT_AUTHOR_NAME=Alice", "GIT_AUTHOR_EMAIL=alice@example.test")
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")

	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "run.sh"), []byte("#!/bin/sh\necho hi\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "LICENSE"), []byte(zeroBSD), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, aliceEnv, "commit", "-q", "-m", "one")
	// A second commit by someone with no account here.
	os.WriteFile(filepath.Join(dir, "extra.go"), []byte("package main\n"), 0o644)
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "two")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	mustGit(t, dir, env, "tag", "v0.1.0")
	mustGit(t, dir, env, "push", "-q", "origin", "v0.1.0")

	status, body := inst.get(t, "/alice/app")
	if status != 200 {
		t.Fatalf("repo home: %d", status)
	}
	for _, want := range []string{
		"<strong>2</strong> commit", // both commits counted
		"<strong>1</strong> branch",
		"<strong>1</strong> tag",
		"0BSD",  // license detected and surfaced
		"Go",    // language census
		"Shell", // and it is not single-language
		"2 contributors",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("repo home missing %q", want)
		}
	}
	// The contributor with a verified email links to their profile; the
	// other is named without inventing an account for them.
	if !strings.Contains(body, `href="/alice"`) {
		t.Error("verified contributor not linked to their profile")
	}
	if strings.Contains(body, `href="/t"`) {
		t.Error("unknown contributor linked to a profile that does not exist")
	}

	// Subdirectory listings are about the directory, not the repository.
	mustGit(t, dir, env, "rm", "-q", "extra.go")
	os.MkdirAll(filepath.Join(dir, "sub"), 0o755)
	os.WriteFile(filepath.Join(dir, "sub", "x.go"), []byte("package sub\n"), 0o644)
	mustGit(t, dir, env, "add", "-A")
	mustGit(t, dir, env, "commit", "-q", "-m", "sub")
	mustGit(t, dir, env, "push", "-q", "origin", "main")
	if _, body := inst.get(t, "/alice/app/tree/main/sub"); strings.Contains(body, "contributor") {
		t.Error("facts bar rendered on a subdirectory listing")
	}

	// A repository with no commits has no facts to state, and must still
	// render — the empty case renders through the same page type.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/empty"); code != 0 {
		t.Fatal("repo create failed")
	}
	if status, body := inst.get(t, "/alice/empty"); status != 200 {
		t.Fatalf("empty repo home: %d", status)
	} else if strings.Contains(body, "contributor") {
		t.Error("empty repository claims contributors")
	}
}

const zeroBSD = `Permission to use, copy, modify, and/or distribute this software for any
purpose with or without fee is hereby granted.

THE SOFTWARE IS PROVIDED "AS IS" AND THE AUTHOR DISCLAIMS ALL WARRANTIES
WITH REGARD TO THIS SOFTWARE.
`

// TestContributorIdentity covers the two ways several addresses fold into
// one contributor: the repository's own .mailmap, and the addresses an
// account has verified here. Addresses with neither stay distinct, even
// when they carry the same name.
func TestContributorIdentity(t *testing.T) {
	smtp := startFakeSMTP(t)
	inst := startInstanceWith(t, fmt.Sprintf(
		"[mail]\nsmtp_host = %q\nfrom = \"noreply@gitbay.test\"\n", smtp.addr))
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")
	// A second address on the same account, verified without the mail
	// round trip — this test is about what the addresses mean, not how
	// they got proven.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "email", "add", "alice2@example.test"); code != 0 {
		t.Fatalf("email add: %s", errOut)
	}
	inst.admin(t, "admin", "email", "verify", "alice", "alice2@example.test")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/ident"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	as := func(name, email string) []string {
		return append(append([]string{}, env...),
			"GIT_AUTHOR_NAME="+name, "GIT_AUTHOR_EMAIL="+email)
	}
	mustGit(t, work, env, "clone", inst.sshURL("alice/ident"), "w")
	dir := filepath.Join(work, "w")
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")

	// The mailmap claims the old address; nothing else does.
	os.WriteFile(filepath.Join(dir, ".mailmap"),
		[]byte("Alice <alice@example.test> <alice@old.test>\n"), 0o644)
	mustGit(t, dir, env, "add", ".mailmap")
	mustGit(t, dir, as("Alice", "alice@example.test"), "commit", "-q", "-m", "mailmap")

	commits := []struct{ name, email string }{
		{"Alice", "alice@example.test"},
		{"Alice", "alice@example.test"},
		{"A. Cleberg", "alice2@example.test"}, // verified here, not in the mailmap
		{"A. Cleberg", "alice2@example.test"},
		{"Alice Old", "alice@old.test"}, // in the mailmap, no account
		{"Alice Old", "alice@old.test"},
		{"Pat", "pat1@example.test"}, // same name, different people
		{"Pat", "pat2@example.test"},
	}
	for i, c := range commits {
		mustGit(t, dir, as(c.name, c.email),
			"commit", "-q", "--allow-empty", "-m", strconv.Itoa(i))
	}
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	status, body := inst.get(t, "/alice/ident")
	if status != 200 {
		t.Fatalf("repo home: %d", status)
	}
	// Scope the positives to the contributors bar: the tip bar names an
	// author too, and links it to the same profile.
	_, contribs, ok := strings.Cut(body, `<p class="contribs">`)
	if !ok {
		t.Fatal("no contributors bar")
	}
	contribs, _, _ = strings.Cut(contribs, "</p>")
	for _, want := range []string{
		"3 contributors",
		// Three addresses, one account, one row, named by the account.
		`title="alice@example.test · 7 commits">alice</a>`,
		// One commit is one commit.
		`title="pat1@example.test · 1 commit"`,
		`title="pat2@example.test · 1 commit"`,
	} {
		if !strings.Contains(contribs, want) {
			t.Errorf("contributors bar missing %q", want)
		}
	}
	if n := strings.Count(contribs, `href="/alice"`); n != 1 {
		t.Errorf("account listed as %d contributors, want 1", n)
	}
	for _, gone := range []string{"alice@old.test", "alice2@example.test", "A. Cleberg", "Alice Old"} {
		if strings.Contains(body, gone) {
			t.Errorf("collapsed identity %q still shown", gone)
		}
	}
}
