package control

import (
	"slices"
	"testing"
)

// The same keyword set has to work wherever the intent is written: a
// commit message, or a merge request title or body.
func TestClosingRefs(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		want []int64
	}{
		{"closes", "Closes #50", []int64{50}},
		{"lowercase and fix", "fixes #7", []int64{7}},
		{"resolved", "resolved: #12", []int64{12}},
		{"several", "Closes #1\n\nAlso fixes #2 and resolves #3", []int64{1, 2, 3}},
		{"repeats collapse", "closes #4, closes #4", []int64{4}},
		{"bare references do not close", "see #9 for context", nil},
		{"cross-repo stays display-only", "closes krz/other#3", nil},
		{"keyword must be its own word", "unclosed #5", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := closingRefs(tc.text)
			slices.Sort(got)
			if !slices.Equal(got, tc.want) {
				t.Errorf("closingRefs(%q) = %v, want %v", tc.text, got, tc.want)
			}
		})
	}
}
