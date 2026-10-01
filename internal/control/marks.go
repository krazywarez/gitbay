package control

import (
	"fmt"
	"slices"
	"strings"

	"gitbay.org/gitbay/internal/store"
)

// checksMark sums a commit's statuses for a list row at a terminal:
// "2 failed" red, "1 pending" dim, "3/3" green, blank with none.
func checksMark(sts []store.CommitStatus) cell {
	failed, pending, passed := 0, 0, 0
	for _, s := range sts {
		switch s.State {
		case "failure", "error":
			failed++
		case "pending":
			pending++
		default:
			passed++
		}
	}
	switch {
	case len(sts) == 0:
		return cText("")
	case failed > 0:
		return cMark(fmt.Sprintf("%d failed", failed), sgrRed)
	case pending > 0:
		return cMark(fmt.Sprintf("%d pending", pending), sgrDim)
	}
	return cMark(fmt.Sprintf("%d/%d", passed, len(sts)), sgrGreen)
}

// reviewMark is where review of an open merge request stands, by the
// rule the merge gates use: each counting reviewer's latest verdict on
// the current head. Changes requested outranks an approval; a request
// for the viewer's own review is what waits on them.
func reviewMark(mr store.MR, reviews []store.MRReview, counts map[string]bool, viewer string) cell {
	latest := map[string]string{}
	for _, r := range reviews {
		if r.Stale || r.Reviewer == mr.Author || !counts[r.Reviewer] {
			continue
		}
		latest[r.Reviewer] = r.Verdict
	}
	approved := 0
	for _, v := range latest {
		switch v {
		case "request_changes":
			return cMark("changes requested", sgrRed)
		case "approve":
			approved++
		}
	}
	switch {
	case approved > 0:
		return cMark("approved", sgrGreen)
	case slices.Contains(mr.ReviewRequests, viewer):
		return cMark("review requested", sgrYellow)
	case len(mr.ReviewRequests) > 0:
		return cMark("requested", sgrDim)
	}
	return cText("")
}

// mrMarks is the CHECKS and REVIEW cells for every open merge request
// on a list page, from one query for statuses and one for reviews.
func mrMarks(c *Ctx, repo store.Repo, mrs []store.MR) (checks, review map[int64]cell, err error) {
	checks, review = map[int64]cell{}, map[int64]cell{}
	var ids []int64
	var shas []string
	for _, m := range mrs {
		if m.State == "open" {
			ids = append(ids, m.ID)
			shas = append(shas, m.HeadSHA)
		}
	}
	statuses, err := c.Store.CommitStatusesFor(repo.ID, shas)
	if err != nil {
		return nil, nil, err
	}
	reviews, err := c.Store.MRReviewsFor(ids)
	if err != nil {
		return nil, nil, err
	}
	requests, err := c.Store.MRReviewRequestsFor(ids)
	if err != nil {
		return nil, nil, err
	}
	var all []store.MRReview
	for _, rs := range reviews {
		all = append(all, rs...)
	}
	counts := ReviewersWhoCount(c.Store, repo, all)
	for _, m := range mrs {
		if m.State != "open" {
			continue
		}
		checks[m.ID] = checksMark(statuses[m.HeadSHA])
		m.ReviewRequests = requests[m.ID]
		review[m.ID] = reviewMark(m, reviews[m.ID], counts, c.User.Username)
	}
	return checks, review, nil
}

// labelsMark is a list row's labels at a terminal: the first two, then
// how many more.
func labelsMark(labels []string) string {
	if len(labels) <= 2 {
		return strings.Join(labels, ", ")
	}
	return fmt.Sprintf("%s, +%d", strings.Join(labels[:2], ", "), len(labels)-2)
}

// assigneesMark is a row's assignees, yellow when the viewer is one.
func assigneesMark(assignees []string, viewer string) cell {
	s := labelsMark(assignees)
	if slices.Contains(assignees, viewer) {
		return cMark(s, sgrYellow)
	}
	return cText(s)
}
