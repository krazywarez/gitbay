package policy

import (
	"testing"

	"gitbay.org/gitbay/internal/store"
)

var (
	owner    = store.User{ID: 1, Username: "alice"}
	stranger = store.User{ID: 2, Username: "bob"}
	priv     = store.Repo{ID: 10, OwnerKind: "user", OwnerID: 1, OwnerName: "alice", Name: "p", Visibility: "private"}
	pub      = store.Repo{ID: 11, OwnerKind: "user", OwnerID: 1, OwnerName: "alice", Name: "q", Visibility: "public"}
)

func TestAccessMatrix(t *testing.T) {
	cases := []struct {
		name  string
		user  store.User
		repo  store.Repo
		grant string
		read  bool
		write bool
		admin bool
	}{
		{"owner private", owner, priv, "", true, true, true},
		{"stranger private no grant", stranger, priv, "", false, false, false},
		{"stranger private read", stranger, priv, "read", true, false, false},
		{"stranger private write", stranger, priv, "write", true, true, false},
		{"stranger private admin", stranger, priv, "admin", true, true, true},
		{"stranger public no grant", stranger, pub, "", true, false, false},
		{"stranger public write", stranger, pub, "write", true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanRead(tc.user, tc.repo, tc.grant); got != tc.read {
				t.Errorf("CanRead = %v, want %v", got, tc.read)
			}
			if got := CanWrite(tc.user, tc.repo, tc.grant); got != tc.write {
				t.Errorf("CanWrite = %v, want %v", got, tc.write)
			}
			if got := CanAdmin(tc.user, tc.repo, tc.grant); got != tc.admin {
				t.Errorf("CanAdmin = %v, want %v", got, tc.admin)
			}
		})
	}
}

func TestScopeAllowsGit(t *testing.T) {
	cases := []struct {
		scope string
		repo  string
		write bool
		want  bool
	}{
		{"full", "a/b", true, true},
		{"git", "a/b", true, true},
		{"deploy:7:ro", "a/b", false, false}, // deploy keys never pass the account path
		{"", "a/b", false, false},
	}
	for _, tc := range cases {
		if got := ScopeAllowsGit(tc.scope, tc.repo, tc.write); got != tc.want {
			t.Errorf("ScopeAllowsGit(%q, %q, write=%v) = %v, want %v", tc.scope, tc.repo, tc.write, got, tc.want)
		}
	}
}

func TestDeployScopeAllows(t *testing.T) {
	cases := []struct {
		scope  string
		repoID int64
		write  bool
		want   bool
	}{
		{"deploy:7:ro", 7, false, true},
		{"deploy:7:ro", 7, true, false},
		{"deploy:7:rw", 7, true, true},
		{"deploy:7:rw", 8, false, false}, // wrong repo
		{"deploy:7", 7, false, false},    // malformed
		{"full", 7, false, false},        // not a deploy scope
	}
	for _, tc := range cases {
		if got := DeployScopeAllows(tc.scope, tc.repoID, tc.write); got != tc.want {
			t.Errorf("DeployScopeAllows(%q, %d, write=%v) = %v, want %v", tc.scope, tc.repoID, tc.write, got, tc.want)
		}
	}
}

func TestCheckPush(t *testing.T) {
	repo := store.Repo{Settings: store.RepoSettings{ProtectedBranches: []string{"main"}}}
	cases := []struct {
		name    string
		updates []RefUpdate
		denied  bool
	}{
		{"normal push to protected", []RefUpdate{{Ref: "refs/heads/main"}}, false},
		{"force to protected", []RefUpdate{{Ref: "refs/heads/main", IsForce: true}}, true},
		{"delete protected", []RefUpdate{{Ref: "refs/heads/main", IsDelete: true}}, true},
		{"force to unprotected", []RefUpdate{{Ref: "refs/heads/dev", IsForce: true}}, false},
		{"delete unprotected", []RefUpdate{{Ref: "refs/heads/dev", IsDelete: true}}, false},
		{"mr namespace", []RefUpdate{{Ref: "refs/merge-requests/1/head"}}, true},
		{"tag alongside protected", []RefUpdate{{Ref: "refs/tags/v1"}, {Ref: "refs/heads/main"}}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msg := CheckPush(repo, tc.updates)
			if (msg != "") != tc.denied {
				t.Errorf("CheckPush = %q, denied should be %v", msg, tc.denied)
			}
		})
	}
}
