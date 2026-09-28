package backuplock

import (
	"errors"
	"testing"
	"time"
)

func TestTrySharedRefusedWhileHeld(t *testing.T) {
	root := t.TempDir()
	release, err := Hold(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryShared(root); !errors.Is(err, ErrBusy) {
		t.Fatalf("TryShared during a backup: %v", err)
	}
	release()
	r, err := TryShared(root)
	if err != nil {
		t.Fatalf("TryShared after the backup: %v", err)
	}
	r()
}

func TestSharedHoldersCoexist(t *testing.T) {
	root := t.TempDir()
	a, err := TryShared(root)
	if err != nil {
		t.Fatal(err)
	}
	defer a()
	b, err := TryShared(root)
	if err != nil {
		t.Fatalf("second shared holder: %v", err)
	}
	b()
}

// A backup waits for a delete already under way.
func TestHoldWaitsForSharedHolder(t *testing.T) {
	root := t.TempDir()
	shared, err := TryShared(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make(chan struct{})
	go func() {
		release, err := Hold(root)
		if err != nil {
			t.Error(err)
			close(got)
			return
		}
		close(got)
		release()
	}()
	select {
	case <-got:
		t.Fatal("Hold returned while a shared holder was in")
	case <-time.After(100 * time.Millisecond):
	}
	shared()
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("Hold never returned after the shared holder left")
	}
}
