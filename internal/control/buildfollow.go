package control

import (
	"errors"
	"fmt"
	"sync"
	"time"

	"gitbay.org/gitbay/internal/policy"
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
	// followCoalesce is the pause between a wake and the read it causes.
	// A runner appends a chunk per read of its output, often a line, and
	// each read loads the whole stored log; one read after a short pause
	// takes a burst of appends together.
	followCoalesce = 200 * time.Millisecond
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

// mayStillRead reports whether the follower can still read the build's
// repository. It looks the repository up by id, so a rename mid-follow
// does not end the follow.
func mayStillRead(c *Ctx, repoID int64) (bool, error) {
	repo, err := c.Store.RepoByID(repoID)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	grant, err := c.Store.AccessRole(repo.ID, c.User.ID)
	if err != nil {
		return false, err
	}
	return policy.CanRead(c.User, repo, grant), nil
}

// followBuildLog writes the build's log as it grows and returns once the
// build has an outcome and its last bytes are written. The outcome goes
// to stderr, so stdout is the log byte for byte. Read access is checked
// again every followPoll: a repository made private, or a grant revoked,
// ends the follow with the answer a new request would get.
func followBuildLog(c *Ctx, repo store.Repo, b store.Build) int {
	if !takeFollow(c.User.ID) {
		return c.fail(protocol.ExitDenied, "%d follows are already open for this account; close one and retry", maxFollows)
	}
	defer dropFollow(c.User.ID)

	var off int64
	var settleBy time.Time
	var queuedSince time.Time
	checked := time.Now()
	for {
		if time.Since(checked) >= followPoll {
			ok, err := mayStillRead(c, b.RepoID)
			if err != nil {
				return c.fail(protocol.ExitFailure, "%v", err)
			}
			if !ok {
				return c.fail(protocol.ExitNotFound, "repository %s not found", repo.Path())
			}
			checked = time.Now()
		}
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
			t.Reset(followCoalesce)
			select {
			case <-t.C:
			case <-c.Done:
				t.Stop()
				return protocol.ExitFailure
			}
		case <-t.C:
		case <-c.Done:
			t.Stop()
			return protocol.ExitFailure
		}
		t.Stop()
	}
}
