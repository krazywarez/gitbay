package control

import (
	"slices"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

// The same keyword set has to work wherever the intent is written: a
// commit message, or a merge request title or body.
func TestClosingRefs(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []closeRef
	}{
		{"closes", "Closes #50", []closeRef{{"", 50}}},
		{"lowercase and fix", "fixes #7", []closeRef{{"", 7}}},
		{"resolved", "resolved: #12", []closeRef{{"", 12}}},
		{"several", "Closes #1\n\nAlso fixes #2 and resolves #3", []closeRef{{"", 1}, {"", 2}, {"", 3}}},
		{"repeats collapse", "closes #4, closes #4", []closeRef{{"", 4}}},
		{"bare references do not close", "see #9 for context", nil},
		{"cross-repo carries the path", "closes krz/other#3", []closeRef{{"krz/other", 3}}},
		{"same number in two repos", "closes #3, closes krz/other#3", []closeRef{{"", 3}, {"krz/other", 3}}},
		{"keyword must be its own word", "unclosed #5", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := closingRefs(tc.text)
			slices.SortFunc(got, func(a, b closeRef) int {
				if a.Path != b.Path {
					return strings.Compare(a.Path, b.Path)
				}
				return int(a.N - b.N)
			})
			if !slices.Equal(got, tc.want) {
				t.Errorf("closingRefs(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}

// A merged merge request's description closes an issue in another
// repository only when the merger holds write there. This drives the
// same target resolution the commit path uses, without needing git.
func TestMRDescriptionClosesAcrossRepos(t *testing.T) {
	f := newOrgFixture(t)
	libIssue, _ := f.st.CreateIssue(f.priv.ID, f.alice, "in priv", "", "md")
	appIssue, _ := f.st.CreateIssue(f.app.ID, f.alice, "in app", "", "md")
	_ = libIssue
	_ = appIssue
	mr := func(n int64, title string) store.MR {
		return store.MR{Number: n, Title: title, Body: ""}
	}
	// carol cannot write acme/priv: the issue stays open and no comment
	// lands.
	ProcessMRDescription(f.st, f.app, mr(1, "Closes acme/priv#1"), f.carol)
	if iss, _ := f.st.IssueByNumber(f.priv.ID, 1); iss.State != "open" {
		t.Fatal("outsider closed a private repo's issue")
	}
	// alice can: it closes with a comment naming the source repository.
	ProcessMRDescription(f.st, f.app, mr(2, "Closes acme/priv#1"), f.alice)
	iss, _ := f.st.IssueByNumber(f.priv.ID, 1)
	if iss.State != "closed" {
		t.Fatal("writer did not close across repos")
	}
	comments, _ := f.st.ListIssueComments(iss.ID)
	if len(comments) != 1 || !strings.Contains(comments[0].Body, "(/alice/app/mrs/2)") {
		t.Fatalf("close comment = %+v", comments)
	}
	// An unknown path is text; a bare #N still acts in the source repo.
	ProcessMRDescription(f.st, f.app, mr(3, "Closes nobody/nothing#1 and closes #1"), f.alice)
	if iss, _ := f.st.IssueByNumber(f.app.ID, 1); iss.State != "closed" {
		t.Fatal("bare #N stopped working")
	}
}
