package httpd

import "testing"

// Explore's column counts topics across the visible repositories, most
// used first then by name, capped at twenty, each linking to ?q=<topic>
// and the active one clearing the query (desktop layout spec).
func TestTopicFacets(t *testing.T) {
	repos := []describedRepo{
		{Topics: []string{"cli", "git"}},
		{Topics: []string{"git", "swift"}},
		{Topics: []string{"git"}},
	}
	g := topicFacets(repos, "cli")
	if g.Title != "Topics" || len(g.Items) != 3 {
		t.Fatalf("group: %+v", g)
	}
	if g.Items[0].Label != "git" || g.Items[0].Count != 3 || g.Items[0].Href != "/explore?q=git" {
		t.Errorf("git: %+v", g.Items[0])
	}
	if g.Items[1].Label != "cli" || !g.Items[1].Active || g.Items[1].Href != "/explore" {
		t.Errorf("cli: %+v", g.Items[1])
	}
	if g.Items[2].Label != "swift" || g.Items[2].Count != 1 {
		t.Errorf("swift: %+v", g.Items[2])
	}
	var many []describedRepo
	for i := 0; i < 30; i++ {
		many = append(many, describedRepo{Topics: []string{string(rune('a' + i))}})
	}
	if n := len(topicFacets(many, "").Items); n != 20 {
		t.Errorf("cap: %d", n)
	}
}
