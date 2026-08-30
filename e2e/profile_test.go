package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnerProfiles(t *testing.T) {
	inst := startInstance(t)
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")

	// Self-service user profile; website validated.
	if _, errOut, code := inst.ssh(t, aliceKey, "",
		"profile", "set", "--description", "'builds small tools'", "--website", "https://alice.example"); code != 0 {
		t.Fatalf("profile set: %s", errOut)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "profile", "set", "--website", "gopher://nope"); code != 2 {
		t.Fatal("bad website scheme accepted")
	}
	out, _, _ := inst.ssh(t, bobKey, "", "profile", "show", "alice", "--json")
	if !strings.Contains(out, `"description":"builds small tools"`) || !strings.Contains(out, `"website":"https://alice.example"`) {
		t.Fatalf("profile show: %s", out)
	}

	// A profile is the whole page's worth: repositories the caller may
	// see, org membership, and the activity window — all previously
	// readable only by the web, which went straight to the store.
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/public-tool"); code != 0 {
		t.Fatal("repo create")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/secret", "--private"); code != 0 {
		t.Fatal("private repo create")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "org", "create", "toolmakers"); code != 0 {
		t.Fatal("org create")
	}

	out, _, _ = inst.ssh(t, aliceKey, "", "profile", "show", "alice", "--json")
	for _, want := range []string{`"path":"alice/public-tool"`, `"path":"alice/secret"`,
		`"name":"toolmakers"`, `"activity_total"`} {
		if !strings.Contains(out, want) {
			t.Errorf("own profile missing %q: %s", want, out)
		}
	}

	// A profile's repository rows carry the listing metadata the web
	// shows: topics, license, default branch, last commit. Without them
	// the web had to decorate the rows itself, which is what kept it
	// reading the store (krz/gitbay#47).
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "topics", "add", "alice/public-tool", "cli", "go"); code != 0 {
		t.Fatalf("topics add: %s", errOut)
	}
	env := inst.gitEnv(aliceKey)
	work := t.TempDir()
	mustGit(t, work, env, "clone", inst.sshURL("alice/public-tool"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "LICENSE"), []byte("MIT License\n\nPermission is hereby granted, free of charge\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "add license")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	out, _, _ = inst.ssh(t, bobKey, "", "profile", "show", "alice", "--json")
	for _, want := range []string{`"cli"`, `"go"`, `"license":"MIT"`, `"default_branch":"main"`, `"updated"`} {
		if !strings.Contains(out, want) {
			t.Errorf("profile repo row missing %q: %s", want, out)
		}
	}
	// The owner page renders those rows, having dispatched the same
	// command rather than assembling them from the store again.
	status, body := inst.get(t, "/alice")
	if status != 200 {
		t.Fatalf("owner page: %d", status)
	}
	for _, want := range []string{">public-tool<", "?q=cli", "MIT", "updated "} {
		if !strings.Contains(body, want) {
			t.Errorf("owner page missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, "secret") {
		t.Fatal("owner page leaks a private repo")
	}

	// A stranger sees the public repository and not the private one.
	out, _, _ = inst.ssh(t, bobKey, "", "profile", "show", "alice", "--json")
	if !strings.Contains(out, `"path":"alice/public-tool"`) {
		t.Errorf("stranger cannot see a public repo on a profile: %s", out)
	}
	if strings.Contains(out, `"alice/secret"`) {
		t.Errorf("a private repository leaked onto a profile: %s", out)
	}

	// An org profile lists its members rather than org memberships.
	out, _, _ = inst.ssh(t, aliceKey, "", "profile", "show", "toolmakers", "--json")
	if !strings.Contains(out, `"kind":"org"`) || !strings.Contains(out, `"name":"alice"`) {
		t.Errorf("org profile members: %s", out)
	}

	// Partial update leaves the other field untouched; empty clears.
	if _, _, code := inst.ssh(t, aliceKey, "", "profile", "set", "--description", "'tinkerer'"); code != 0 {
		t.Fatal("partial set failed")
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "profile", "show", "--json")
	if !strings.Contains(out, "tinkerer") || !strings.Contains(out, "alice.example") {
		t.Fatalf("partial update clobbered website: %s", out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "profile", "set", "--website", "''"); code != 0 {
		t.Fatal("clear failed")
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "profile", "show", "--json")
	if strings.Contains(out, "alice.example") {
		t.Fatalf("website not cleared: %s", out)
	}

	// Org profile: admin sets, member cannot; no flags shows.
	if _, _, code := inst.ssh(t, aliceKey, "", "org", "create", "workshop"); code != 0 {
		t.Fatal("org create failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "org", "members", "add", "workshop", "bob"); code != 0 {
		t.Fatal("member add failed")
	}
	if _, errOut, code := inst.ssh(t, aliceKey, "",
		"org", "profile", "workshop", "--description", "'where things get made'", "--website", "https://workshop.example"); code != 0 {
		t.Fatalf("org profile set: %s", errOut)
	}
	if _, _, code := inst.ssh(t, bobKey, "", "org", "profile", "workshop", "--description", "hax"); code != 4 {
		t.Fatal("member set org profile")
	}
	out, _, _ = inst.ssh(t, bobKey, "", "org", "profile", "workshop")
	if !strings.Contains(out, "where things get made") {
		t.Fatalf("org profile show: %s", out)
	}

	// About: long-form markdown, set inline or piped, rendered on the page.
	if _, errOut, code := inst.ssh(t, aliceKey, "# Hello\n\nI maintain *small tools*.\n",
		"profile", "set", "--file", "-"); code != 0 {
		t.Fatalf("about from stdin: %s", errOut)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "profile", "show", "alice", "--json")
	if !strings.Contains(out, "I maintain *small tools*.") {
		t.Fatalf("about not stored: %s", out)
	}
	if !strings.Contains(out, "tinkerer") {
		t.Fatalf("about clobbered the description: %s", out)
	}

	// Org-mode about: the stored format picks the renderer.
	if _, errOut, code := inst.ssh(t, aliceKey, "* Tools\n\nI maintain /small tools/.\n",
		"profile", "set", "--file", "-", "--about-format", "org"); code != 0 {
		t.Fatalf("org about: %s", errOut)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "profile", "show", "alice", "--json")
	if !strings.Contains(out, `"about_format":"org"`) {
		t.Fatalf("about format not stored: %s", out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "profile", "set", "--about-format", "rst"); code != 2 {
		t.Fatal("unknown about format accepted")
	}
	// Org emphasis parsed, not left as literal slashes the way the
	// markdown renderer would.
	_, body = inst.get(t, "/alice")
	if !strings.Contains(body, "<em>small tools</em>") || strings.Contains(body, "/small tools/") {
		t.Fatalf("org about not rendered as org: %s", body)
	}

	// Back to markdown for the rest of the checks.
	if _, _, code := inst.ssh(t, aliceKey, "# Hello\n\nI maintain *small tools*.\n",
		"profile", "set", "--file", "-", "--about-format", "md"); code != 0 {
		t.Fatal("markdown about")
	}

	// Links: free-form, labelled or bare, capped, cleared by an empty one.
	if _, errOut, code := inst.ssh(t, aliceKey, "", "profile", "set",
		"--link", "'Mastodon|https://fosstodon.example/@alice'",
		"--link", "https://alice.example/now"); code != 0 {
		t.Fatalf("links: %s", errOut)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "profile", "show", "alice", "--json")
	if !strings.Contains(out, `"label":"Mastodon"`) ||
		!strings.Contains(out, `"url":"https://fosstodon.example/@alice"`) ||
		!strings.Contains(out, `"url":"https://alice.example/now"`) {
		t.Fatalf("links not stored: %s", out)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "profile", "set", "--link", "'x|gopher://nope'"); code != 2 {
		t.Fatal("bad link scheme accepted")
	}
	sixth := []string{"profile", "set"}
	for i := 0; i < 6; i++ {
		sixth = append(sixth, "--link", "https://example.org/"+string(rune('a'+i)))
	}
	if _, _, code := inst.ssh(t, aliceKey, "", sixth...); code != 2 {
		t.Fatal("more than five links accepted")
	}

	// Owner pages render description and website link.
	status, body = inst.get(t, "/alice")
	if status != 200 || !strings.Contains(body, "tinkerer") {
		t.Fatalf("user page profile: %d", status)
	}
	// About renders as markdown between the header and the activity graph;
	// links render as chips.
	if !strings.Contains(body, "<em>small tools</em>") {
		t.Fatalf("about not rendered: %s", body)
	}
	if !strings.Contains(body, `href="https://fosstodon.example/@alice"`) ||
		!strings.Contains(body, ">Mastodon<") {
		t.Fatalf("links not rendered: %s", body)
	}
	if strings.Index(body, "<em>small tools</em>") > strings.Index(body, `class="activity"`) {
		t.Error("about renders below the activity graph")
	}

	// Clearing works the same way as the other fields.
	if _, _, code := inst.ssh(t, aliceKey, "", "profile", "set", "--link", "''"); code != 0 {
		t.Fatal("clear links failed")
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "profile", "set", "--about", "''"); code != 0 {
		t.Fatal("clear about failed")
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "profile", "show", "alice", "--json")
	if strings.Contains(out, "fosstodon") || strings.Contains(out, "small tools") {
		t.Fatalf("about or links not cleared: %s", out)
	}
	status, body = inst.get(t, "/workshop")
	if status != 200 || !strings.Contains(body, "where things get made") ||
		!strings.Contains(body, `href="https://workshop.example"`) {
		t.Fatalf("org page profile: %d\n%s", status, body)
	}
}
