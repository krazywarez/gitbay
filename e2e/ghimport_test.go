package e2e

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeGitHub serves just enough of the GitHub REST API for the importer.
func fakeGitHub(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	auth := func(w http.ResponseWriter, r *http.Request) bool {
		if r.Header.Get("Authorization") != "Bearer sekrit" {
			w.WriteHeader(401)
			return false
		}
		return true
	}
	mux.HandleFunc("/repos/octo/legacy/issues", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		if r.URL.Query().Get("page") != "1" {
			fmt.Fprint(w, "[]")
			return
		}
		fmt.Fprint(w, `[
		 {"number":1,"title":"old bug","body":"it crashed","state":"closed",
		  "created_at":"2019-03-04T10:00:00Z","user":{"login":"octofan"},
		  "labels":[{"name":"bug"}],"comments":0},
		 {"number":2,"title":"add feature","body":"the patch","state":"closed",
		  "created_at":"2020-06-01T10:00:00Z","user":{"login":"drive-by"},
		  "labels":[],"comments":1,"pull_request":{}},
		 {"number":3,"title":"still open","body":"discuss","state":"open",
		  "created_at":"2021-01-01T10:00:00Z","user":{"login":"octofan"},
		  "labels":[],"comments":2}
		]`)
	})
	mux.HandleFunc("/repos/octo/legacy/pulls/2", func(w http.ResponseWriter, r *http.Request) {
		if !auth(w, r) {
			return
		}
		fmt.Fprint(w, `{"merged_at":"2020-06-02T10:00:00Z",
		 "head":{"sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","ref":"feature"},
		 "base":{"sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","ref":"main"}}`)
	})
	comments := func(payload string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !auth(w, r) {
				return
			}
			if r.URL.Query().Get("page") != "1" {
				fmt.Fprint(w, "[]")
				return
			}
			fmt.Fprint(w, payload)
		}
	}
	mux.HandleFunc("/repos/octo/legacy/issues/2/comments", comments(
		`[{"id":101,"body":"nice patch","created_at":"2020-06-01T11:00:00Z","user":{"login":"maintainer"}}]`))
	mux.HandleFunc("/repos/octo/legacy/issues/3/comments", comments(
		`[{"id":102,"body":"me too","created_at":"2021-01-02T10:00:00Z","user":{"login":"other"}},
		  {"id":103,"body":"still happening","created_at":"2021-02-01T10:00:00Z","user":{"login":"octofan"}}]`))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestGitHubIssueImport(t *testing.T) {
	// allow_local lets --api-base reach the loopback fake; a default
	// instance refuses it (see the SSRF check at the end).
	inst := startInstanceWith(t, "[webhooks]\nallow_local = true\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")

	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}
	work := t.TempDir()
	env := inst.gitEnv(aliceKey)
	mustGit(t, work, env, "clone", inst.sshURL("alice/app"), "w")
	dir := filepath.Join(work, "w")
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o644)
	mustGit(t, dir, env, "checkout", "-q", "-b", "main")
	mustGit(t, dir, env, "add", ".")
	mustGit(t, dir, env, "commit", "-q", "-m", "base")
	mustGit(t, dir, env, "push", "-q", "origin", "main")

	gh := fakeGitHub(t)
	out, errOut, code := inst.ssh(t, aliceKey, "sekrit\n", "repo", "import-issues", "alice/app",
		"--from", "octo/legacy", "--token-stdin", "--api-base", gh.URL)
	if code != 0 {
		t.Fatalf("import: %s", errOut)
	}
	if !strings.Contains(out, "imported 2 issues, 1 merge requests, 3 comments") {
		t.Fatalf("summary: %s", out)
	}

	// Issue #1 (GitHub #1): closed, labeled, attributed.
	out, _, _ = inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "1", "--json")
	if !strings.Contains(out, "old bug") || !strings.Contains(out, `"state":"closed"`) ||
		!strings.Contains(out, `"labels":["bug"]`) ||
		!strings.Contains(out, "imported issue github.com/octo/legacy#1") ||
		!strings.Contains(out, "@octofan, 2019-03-04") {
		t.Fatalf("issue 1: %s", out)
	}
	// Issue #2 (GitHub #3): open, two attributed comments.
	out, _, _ = inst.ssh(t, aliceKey, "", "issue", "show", "alice/app", "2", "--json")
	if !strings.Contains(out, "still open") || !strings.Contains(out, `"state":"open"`) ||
		!strings.Contains(out, "me too") || !strings.Contains(out, "@other, 2021-01-02") {
		t.Fatalf("issue 2: %s", out)
	}
	// MR !1 (GitHub PR #2): merged, discussion imported.
	out, _, _ = inst.ssh(t, aliceKey, "", "mr", "show", "alice/app", "1", "--json")
	if !strings.Contains(out, "add feature") || !strings.Contains(out, `"state":"merged"`) ||
		!strings.Contains(out, "imported pull request github.com/octo/legacy#2") ||
		!strings.Contains(out, "nice patch") {
		t.Fatalf("mr 1: %s", out)
	}

	// Re-running imports nothing new — fully resumable.
	out, _, code = inst.ssh(t, aliceKey, "sekrit\n", "repo", "import-issues", "alice/app",
		"--from", "octo/legacy", "--token-stdin", "--api-base", gh.URL)
	if code != 0 || !strings.Contains(out, "imported 0 issues, 0 merge requests, 0 comments (3 items already imported)") {
		t.Fatalf("re-run: %s", out)
	}
	out, _, _ = inst.ssh(t, aliceKey, "", "issue", "list", "alice/app", "--state", "all")
	if strings.Count(out, "\n") != 2 {
		t.Fatalf("issues duplicated:\n%s", out)
	}

	// A wrong token surfaces the API error.
	if _, errOut, code := inst.ssh(t, aliceKey, "wrong\n", "repo", "import-issues", "alice/app",
		"--from", "octo/legacy", "--token-stdin", "--api-base", gh.URL); code == 0 || !strings.Contains(errOut, "401") {
		t.Fatalf("bad token: exit %d, %s", code, errOut)
	}
}

func TestGitHubImportSSRFGuard(t *testing.T) {
	inst := startInstance(t) // allow_local off: default posture
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatal("repo create failed")
	}
	_, errOut, code := inst.ssh(t, aliceKey, "", "repo", "import-issues", "alice/app",
		"--from", "octo/legacy", "--api-base", "http://127.0.0.1:9999")
	if code != 2 || !strings.Contains(errOut, "SSRF") {
		t.Fatalf("local api-base allowed: exit %d, %s", code, errOut)
	}
}
