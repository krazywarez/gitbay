package control

import (
	"testing"
	"time"

	// Aliased: this package already has a top-level function named notify
	// (notifications.go), which the default import name would collide with.
	mailqueue "gitbay.org/gitbay/internal/notify"
)

// Nothing ties notify's retry parameters to loginLinkTTL: someone tuning
// mail retries for a slow relay has no reason to think about login links,
// and the failure if they drift apart is silent — a link delivered after
// it has already expired, refused with no indication why. This computes
// the mailer's worst-case delivery time from notify's own named constants
// (attempts 1..MaxAttempts-1 each wait RetryBase<<(attempt-1); the
// MaxAttempts'th failure is dead-lettered immediately, per notify.Mailer.Run)
// rather than a copy of the numbers, so either side moving breaks this test.
func TestLoginLinkOutlivesMailerRetries(t *testing.T) {
	var worst time.Duration
	for attempt := 1; attempt < mailqueue.DefaultMaxAttempts; attempt++ {
		worst += mailqueue.DefaultRetryBase << (attempt - 1)
	}
	if worst >= loginLinkTTL {
		t.Fatalf("mailqueue.DefaultRetryBase=%s, mailqueue.DefaultMaxAttempts=%d: worst-case "+
			"delivery is %s, not less than loginLinkTTL=%s — a retried login link mail can "+
			"arrive after the link it carries has expired",
			mailqueue.DefaultRetryBase, mailqueue.DefaultMaxAttempts, worst, loginLinkTTL)
	}
}
