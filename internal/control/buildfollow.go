package control

import (
	"fmt"
	"sync"
	"time"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// maxFollows is how many build log follows one account holds open at
// once. Signed-out web viewers are account 0 and share it.
const maxFollows = 8

var (
	// followPoll bounds a wait with no wake. A write from another process
	// (gitbayd admin, or any session under gitbayd shell) wakes nobody;
	// this is how its bytes still arrive.
	followPoll = 2 * time.Second
	// followSettle is how long a follow keeps reading after the build has
	// an outcome: a cancel appends its line after the status changes, and
	// a cancelled runner's stream runs on until its next check.
	followSettle = time.Second
	// followQueued bounds how long a follow waits on a build that stays
	// pending: nothing reaps a queued build (ReapStaleBuilds only reaps
	// running builds), and a running one is already bounded by the
	// reaper's deadline, so a follow needs its own limit for the queued
	// case or it never ends.
	followQueued = 10 * time.Minute
)

var (
	followMu sync.Mutex
	follows  = map[int64]int{}
)

func takeFollow(uid int64) bool {
	followMu.Lock()
	defer followMu.Unlock()
	if follows[uid] >= maxFollows {
		return false
	}
	follows[uid]++
	return true
}

func dropFollow(uid int64) {
	followMu.Lock()
	defer followMu.Unlock()
	if follows[uid]--; follows[uid] <= 0 {
		delete(follows, uid)
	}
}

// followBuildLog writes the build's log as it grows and returns once the
// build has an outcome and its last bytes are written. The outcome goes
// to stderr, so stdout is the log byte for byte.
func followBuildLog(c *Ctx, b store.Build) int {
	if !takeFollow(c.User.ID) {
		return c.fail(protocol.ExitDenied, "%d follows are already open for this account; close one and retry", maxFollows)
	}
	defer dropFollow(c.User.ID)

	var off int64
	var settleBy time.Time
	var queuedSince time.Time
	for {
		wake := c.Store.BuildLogWait(b.ID)
		status, chunk, err := c.Store.BuildLogFrom(b.ID, off)
		if err != nil {
			return c.fail(protocol.ExitFailure, "%v", err)
		}
		if len(chunk) > 0 {
			if _, err := c.Stdout.Write(chunk); err != nil {
				return protocol.ExitFailure
			}
			off += int64(len(chunk))
		}
		if status == "pending" {
			if queuedSince.IsZero() {
				queuedSince = time.Now()
			}
		} else {
			queuedSince = time.Time{}
		}
		wait := followPoll
		if status == "pending" {
			left := queuedSince.Add(followQueued).Sub(time.Now())
			if left <= 0 {
				fmt.Fprintf(c.Stderr, "build %d is still queued; nothing claimed it in %v. Follow again once a runner has.\n", b.Number, followQueued)
				return protocol.ExitFailure
			}
			wait = min(wait, left)
		}
		if status != "pending" && status != "running" {
			if settleBy.IsZero() {
				settleBy = time.Now().Add(followSettle)
			}
			left := time.Until(settleBy)
			if left <= 0 && len(chunk) == 0 {
				fmt.Fprintf(c.Stderr, "build %d %s\n", b.Number, status)
				return protocol.ExitOK
			}
			wait = min(wait, max(left, 0))
		}
		t := time.NewTimer(wait)
		select {
		case <-wake:
		case <-t.C:
		case <-c.Done:
			t.Stop()
			return protocol.ExitFailure
		}
		t.Stop()
	}
}
