package gitutil

import "testing"

func TestSortVersionsNewestFirst(t *testing.T) {
	refs := []Ref{{Name: "v1.2.0"}, {Name: "v1.10.0"}, {Name: "nightly"}, {Name: "v1.2.1"}, {Name: "v0.9"}, {Name: "beta"}, {Name: "2.0.0"}}
	SortVersions(refs)
	var got []string
	for _, r := range refs {
		got = append(got, r.Name)
	}
	want := []string{"2.0.0", "v1.10.0", "v1.2.1", "v1.2.0", "v0.9", "beta", "nightly"}
	for i := range want {
		if i >= len(got) || got[i] != want[i] {
			t.Fatalf("order %v, want %v", got, want)
		}
	}
}
