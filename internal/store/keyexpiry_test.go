package store

import (
	"testing"
	"time"
)

func TestKeyExpiry(t *testing.T) {
	s, uid, _ := revokeFixture(t)
	past, future := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	for fp, exp := range map[string]*time.Time{"SHA256:old": &past, "SHA256:new": &future, "SHA256:ever": nil} {
		if err := s.AddSSHKeyFrom(uid, fp, "ssh-ed25519", []byte(fp), "full", "", KeyOrigin{ExpiresAt: exp}); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now()
	ids := map[string]int64{}
	for _, fp := range []string{"SHA256:old", "SHA256:new", "SHA256:ever"} {
		k, err := s.SSHKeyByFingerprint(fp)
		if err != nil {
			t.Fatal(err)
		}
		ids[fp] = k.ID
		byID, err := s.SSHKeyByID(k.ID)
		if err != nil || (byID.ExpiresAt == nil) != (k.ExpiresAt == nil) {
			t.Fatalf("%s by id: %+v %v", fp, byID, err)
		}
		if got, want := k.Expired(now), fp == "SHA256:old"; got != want {
			t.Errorf("%s Expired = %v, want %v", fp, got, want)
		}
	}
	live, err := s.LiveSSHKeys([]int64{ids["SHA256:old"], ids["SHA256:new"], ids["SHA256:ever"]})
	if err != nil {
		t.Fatal(err)
	}
	if live[ids["SHA256:old"]] || !live[ids["SHA256:new"]] || !live[ids["SHA256:ever"]] {
		t.Fatalf("live = %v", live)
	}
	keys, err := s.ListSSHKeys(uid)
	if err != nil || len(keys) != 3 {
		t.Fatalf("list: %+v %v", keys, err)
	}
	for _, k := range keys {
		if k.Fingerprint == "SHA256:ever" && k.ExpiresAt != nil {
			t.Fatalf("list: %s should have nil ExpiresAt: %+v", k.Fingerprint, k)
		}
	}
}
