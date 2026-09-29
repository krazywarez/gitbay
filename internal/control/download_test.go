package control

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/packlimit"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// repo download takes a pack slot: busy while the limiter is full, and
// the slot is free again once git archive has exited.
func TestRepoDownloadTakesAPackSlot(t *testing.T) {
	st, repo, root, _ := prunedRepo(t)
	alice, err := st.UserByUsername("alice")
	if err != nil {
		t.Fatal(err)
	}
	packs := packlimit.New(1, 0, 0, time.Second)
	hold, err := packs.Acquire(nil, "ip:elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	c, errOut := pruneCtx(st, root, alice)
	c.Packs = packs
	if code := Dispatch(c, []string{"repo", "download", repo.Path()}); code != protocol.ExitFailure ||
		!strings.Contains(errOut.String(), "limit of concurrent clones") {
		t.Fatalf("busy: exit %d: %q", code, errOut.String())
	}
	hold()

	c, errOut = pruneCtx(st, root, alice)
	c.Packs = packs
	if code := Dispatch(c, []string{"repo", "download", repo.Path()}); code != protocol.ExitOK {
		t.Fatalf("free: exit %d: %s", code, errOut.String())
	}
	if c.Stdout.(*bytes.Buffer).Len() == 0 {
		t.Fatal("no archive written")
	}
	hold, err = packs.Acquire(nil, "ip:elsewhere")
	if err != nil {
		t.Fatalf("slot not released after the download: %v", err)
	}
	hold()
}

// A download the caller may not make is refused before the limiter.
func TestRepoDownloadRefusalStaysOffLimiter(t *testing.T) {
	st, repo, root, _ := prunedRepo(t)
	if err := st.SetRepoVisibility(repo.ID, "private"); err != nil {
		t.Fatal(err)
	}
	bobID, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	packs := packlimit.New(1, 0, 0, time.Second)
	hold, err := packs.Acquire(nil, "ip:elsewhere")
	if err != nil {
		t.Fatal(err)
	}
	defer hold()
	c, errOut := pruneCtx(st, root, store.User{ID: bobID, Username: "bob"})
	c.Packs = packs
	if code := Dispatch(c, []string{"repo", "download", repo.Path()}); code != protocol.ExitNotFound {
		t.Fatalf("exit %d: %q", code, errOut.String())
	}
}
