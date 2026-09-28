package store

import (
	"slices"
	"testing"
)

func revokeFixture(t *testing.T) (*Store, int64, *[]Revoked) {
	t.Helper()
	s := open(t)
	if err := s.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, err := s.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	var got []Revoked
	s.OnRevoke(func(r Revoked) { got = append(got, r) })
	return s, uid, &got
}

func keyID(t *testing.T, s *Store, fp string) int64 {
	t.Helper()
	k, err := s.SSHKeyByFingerprint(fp)
	if err != nil {
		t.Fatal(err)
	}
	return k.ID
}

func TestRemovalsAnnounceTheirKeys(t *testing.T) {
	s, uid, got := revokeFixture(t)
	if err := s.AddSSHKey(uid, "SHA256:a", "ssh-ed25519", []byte("a"), "full", ""); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSSHKey(uid, "SHA256:d", "ssh-ed25519", []byte("d"), "deploy:7:ro", ""); err != nil {
		t.Fatal(err)
	}
	a, d := keyID(t, s, "SHA256:a"), keyID(t, s, "SHA256:d")

	if err := s.RemoveSSHKey(uid, "SHA256:a"); err != nil {
		t.Fatal(err)
	}
	if err := s.RemoveDeployKey(7, "SHA256:d"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserDisabled(uid, true); err != nil {
		t.Fatal(err)
	}
	if err := s.SetUserDisabled(uid, false); err != nil {
		t.Fatal(err)
	}
	want := []Revoked{{KeyIDs: []int64{a}}, {KeyIDs: []int64{d}}, {UserID: uid}}
	if !slices.EqualFunc(*got, want, func(x, y Revoked) bool {
		return slices.Equal(x.KeyIDs, y.KeyIDs) && x.UserID == y.UserID
	}) {
		t.Fatalf("announced %+v, want %+v (enabling announces nothing)", *got, want)
	}
	// A removal that found nothing announces nothing.
	if err := s.RemoveSSHKey(uid, "SHA256:a"); err != ErrNotFound {
		t.Fatalf("second remove: %v", err)
	}
	if len(*got) != 3 {
		t.Fatalf("a miss was announced: %+v", *got)
	}
}

func TestDeleteUserAnnounces(t *testing.T) {
	s, uid, got := revokeFixture(t)
	if err := s.DeleteUser(uid); err != nil {
		t.Fatal(err)
	}
	if len(*got) != 1 || (*got)[0].UserID != uid {
		t.Fatalf("announced %+v", *got)
	}
}

func TestLiveSSHKeys(t *testing.T) {
	s, uid, _ := revokeFixture(t)
	bob, err := s.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []struct {
		uid int64
		fp  string
	}{{uid, "SHA256:a"}, {bob, "SHA256:b"}} {
		if err := s.AddSSHKey(k.uid, k.fp, "ssh-ed25519", []byte(k.fp), "full", ""); err != nil {
			t.Fatal(err)
		}
	}
	a, b := keyID(t, s, "SHA256:a"), keyID(t, s, "SHA256:b")
	if _, err := s.DB.Exec("UPDATE users SET disabled = 1 WHERE id = ?", bob); err != nil {
		t.Fatal(err)
	}
	live, err := s.LiveSSHKeys([]int64{a, b, 999})
	if err != nil {
		t.Fatal(err)
	}
	if !live[a] || live[b] || live[999] {
		t.Fatalf("live = %v; want only %d", live, a)
	}
	if live, err := s.LiveSSHKeys(nil); err != nil || len(live) != 0 {
		t.Fatalf("no ids: %v %v", live, err)
	}
}
