package control

import (
	"slices"
	"strings"

	"gitbay.org/gitbay/internal/protocol"
)

// EventKinds is every event this forge records, and so every value a
// webhook's `--events` filter may name. It is the published list: the API
// wiki page renders it, and TestEventKindsAreRecorded asserts the code
// still emits exactly these, so the documentation cannot drift from the
// server (#112).
//
// Not here, deliberately: repo.deleted. events.repo_id and
// webhooks.repo_id both cascade from repos, so recording one would delete
// it and every webhook that could have received it in the same statement.
var EventKinds = []string{
	"build.cancelled",
	"build.failure",
	"build.success",
	"issue.assigned",
	"issue.closed",
	"issue.commented",
	"issue.created",
	"issue.edited",
	"issue.labeled",
	"issue.milestoned",
	"issue.open",
	"mr.closed",
	"mr.commented",
	"mr.created",
	"mr.draft",
	"mr.edited",
	"mr.merged",
	"mr.milestoned",
	"mr.retargeted",
	"mr.reviewed",
	"push",
	"release.created",
	"release.deleted",
	"repo.archived",
	"repo.imported",
	"repo.unarchived",
	"status",
}

// checkEventNames refuses a --events list naming something this forge
// never emits. "*" is every kind. -1 means proceed.
func checkEventNames(c *Ctx, events string) int {
	if events == "*" {
		return -1
	}
	for _, k := range strings.Split(events, ",") {
		k = strings.TrimSpace(k)
		if k == "" {
			return c.fail(protocol.ExitUsage, "empty event name in --events")
		}
		if !slices.Contains(EventKinds, k) {
			return c.fail(protocol.ExitUsage,
				"%q is not an event this forge records; known events are: %s",
				k, strings.Join(EventKinds, ", "))
		}
	}
	return -1
}
