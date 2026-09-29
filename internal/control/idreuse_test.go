package control

import (
	"fmt"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/policy"
)

// A deploy key names its repository by id in its scope. Deleting the
// repository removes the key, and the next repository does not take the
// id, so the key opens nothing either way (#306).
func TestDeployKeyOfDeletedRepository(t *testing.T) {
	st, _, uid := newQueueTestRepo(t)
	goneID, err := st.CreateRepo("user", uid, "gone", "private")
	if err != nil {
		t.Fatal(err)
	}
	scope := fmt.Sprintf("deploy:%d:rw", goneID)
	if err := st.AddSSHKey(uid, "SHA256:deploy", "ssh-ed25519", []byte("d"), scope, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteRepo(goneID); err != nil {
		t.Fatal(err)
	}
	nextID, err := st.CreateRepo("user", uid, "next", "private")
	if err != nil {
		t.Fatal(err)
	}
	if policy.DeployScopeAllows(scope, nextID, false) {
		t.Fatalf("the deleted repository's deploy key reads repository %d", nextID)
	}
}

// A grant names its account by id. Deleting the account removes the
// grant, and the next account does not take the id (#306).
func TestGrantOfDeletedAccount(t *testing.T) {
	st, repo, _ := newQueueTestRepo(t)
	carol, err := st.CreateUser("carol", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.GrantAccess(repo.ID, carol, "admin"); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteUser(carol); err != nil {
		t.Fatal(err)
	}
	dave, err := st.CreateUser("dave", false)
	if err != nil {
		t.Fatal(err)
	}
	if role, err := st.AccessRole(repo.ID, dave); err != nil || role != "" {
		t.Fatalf("new account's role on the repository: %q, %v", role, err)
	}
}

// A merge queued with a key that is then removed stays refused when a
// new key is added: key_id is set to NULL on removal and the new key has
// an id of its own (#289, #306).
func TestQueuedMergeKeyRemovedThenNewKey(t *testing.T) {
	st, repo, uid := newQueueTestRepo(t)
	if err := st.AddSSHKey(uid, "SHA256:k1", "ssh-ed25519", []byte("k1"), "full", ""); err != nil {
		t.Fatal(err)
	}
	k1, err := st.SSHKeyByFingerprint("SHA256:k1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMR(repo.ID, uid, repo.ID, "feature", "main", "t", "", "abc", "md", false); err != nil {
		t.Fatal(err)
	}
	mr, err := st.MRByNumber(repo.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	mrID := mr.ID
	if err := st.QueueMerge(mrID, uid, "", k1.ID, 0); err != nil {
		t.Fatal(err)
	}
	if err := st.RemoveSSHKey(uid, "SHA256:k1"); err != nil {
		t.Fatal(err)
	}
	if err := st.AddSSHKey(uid, "SHA256:k2", "ssh-ed25519", []byte("k2"), "full", ""); err != nil {
		t.Fatal(err)
	}
	if k2, _ := st.SSHKeyByFingerprint("SHA256:k2"); k2.ID == k1.ID {
		t.Fatalf("new key took the removed key's id %d", k1.ID)
	}
	reason, err := queueCredentialLapsed(st, mrID, time.Now())
	if err != nil || reason != "the key it was queued with was removed" {
		t.Fatalf("lapsed = %q, %v", reason, err)
	}
}
