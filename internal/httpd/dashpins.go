package httpd

import (
	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/store"
)

// pinnedRow is one pinned repository on the dashboard with the counts
// that say whether it wants attention: open issues, open merge requests
// and the newest build's status ("" when it has none).
type pinnedRow struct {
	Owner  string
	Name   string
	Issues int
	MRs    int
	Build  string
}

// pinnedRows reads the viewer's pinned repositories the way railFor does,
// then adds the counts. Three reads per pinned repository, on the
// dashboard only.
func (s *Server) pinnedRows(viewer store.User) []pinnedRow {
	pinned, _ := s.st.PinnedRepos(viewer.ID)
	var rows []pinnedRow
	for _, rp := range pinned {
		grant, _ := s.st.AccessRole(rp.ID, viewer.ID)
		if !policy.CanRead(viewer, rp, grant) {
			continue
		}
		row := pinnedRow{Owner: rp.OwnerName, Name: rp.Name}
		row.Issues, row.MRs = s.st.OpenCounts(rp.ID)
		if builds, err := s.st.ListBuilds(rp.ID, store.BuildFilter{}, 1); err == nil && len(builds) > 0 {
			row.Build = builds[0].Status
		}
		rows = append(rows, row)
	}
	return rows
}
