package httpd

import (
	"html/template"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
)

func TestProfileTabFromPath(t *testing.T) {
	for path, want := range map[string]string{
		"/cmc":            "repos",
		"/cmc/-/about":    "about",
		"/cmc/-/activity": "activity",
		"/cmc/-/people":   "people",
		"/krz":            "repos",
		"/cmc/-/snippets": "repos",
	} {
		if got := profileTab(path); got != want {
			t.Errorf("profileTab(%q) = %q, want %q", path, got, want)
		}
	}
}

// The About text and the year of squares sat above the repository list
// and pushed it below the fold (#242). Repositories are the bare
// /{owner} now and the rest are tabs beside them.
func TestOwnerPageLeadsWithRepositories(t *testing.T) {
	out := renderOwner(t, "repos", ownerFixture())
	if !strings.Contains(out, "reminiscecleberg.com") {
		t.Errorf("the repository list is not on the default tab:\n%s", out)
	}
	for _, unwanted := range []string{"actgraph", "Christian Cleberg"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("the default tab still carries %q:\n%s", unwanted, out)
		}
	}
	for _, want := range []string{
		`aria-current="page" href="/cmc"`,
		`href="/cmc/-/about"`,
		`href="/cmc/-/activity"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("tab bar missing %q:\n%s", want, out)
		}
	}
	// Nobody administers this profile, so it offers no people tab.
	if strings.Contains(out, "/-/people") {
		t.Errorf("a profile nobody admins offers a people tab:\n%s", out)
	}
}

func TestOwnerPageTabsCarryOneSectionEach(t *testing.T) {
	if out := renderOwner(t, "about", ownerFixture()); !strings.Contains(out, "Christian Cleberg") ||
		strings.Contains(out, "actgraph") || strings.Contains(out, "reminiscecleberg.com") {
		t.Errorf("about tab is not the About file alone:\n%s", out)
	}
	if out := renderOwner(t, "activity", ownerFixture()); !strings.Contains(out, "actgraph") ||
		strings.Contains(out, "reminiscecleberg.com") {
		t.Errorf("activity tab is not the graph alone:\n%s", out)
	}
	// A profile with no About file does not offer the tab.
	d := ownerFixture()
	d.AboutHTML = ""
	if out := renderOwner(t, "repos", d); strings.Contains(out, "/-/about") {
		t.Errorf("a profile with no About file offers the tab:\n%s", out)
	}
}

func TestOwnerPagePeopleTabHoldsTheAdminPanel(t *testing.T) {
	d := ownerFixture()
	d.Kind = "org"
	d.CanAdmin = true
	d.Members = []control.ProfileMember{{Name: "cmc", Role: "admin"}}

	repos := renderOwner(t, "repos", d)
	if !strings.Contains(repos, `href="/cmc/-/people"`) {
		t.Errorf("an admin gets no people tab:\n%s", repos)
	}
	if strings.Contains(repos, "Create a team") {
		t.Errorf("the admin forms still sit under the repository list:\n%s", repos)
	}
	people := renderOwner(t, "people", d)
	for _, want := range []string{"Create a team", "member-add", "org-rename"} {
		if !strings.Contains(people, want) {
			t.Errorf("people tab missing %q:\n%s", want, people)
		}
	}
}

type ownerFixtureData struct {
	Kind      string
	AboutHTML template.HTML
	CanAdmin  bool
	Members   []control.ProfileMember
}

func ownerFixture() ownerFixtureData {
	return ownerFixtureData{
		Kind:      "user",
		AboutHTML: template.HTML("<p><strong>Christian Cleberg</strong></p>"),
	}
}

func renderOwner(t *testing.T, tab string, d ownerFixtureData) string {
	t.Helper()
	var sb strings.Builder
	err := web.Render(&sb, "owner.html", struct {
		basePage
		Owner         string
		Kind          string
		Tab           string
		Profile       store.Profile
		AboutHTML     template.HTML
		Repos         []profileRepoRow
		Members       []control.ProfileMember
		Orgs          []control.ProfileMember
		Activity      []activityWeek
		ActivityTotal int
		Teams         []teamView
		CanAdmin      bool
		Self          bool
		Snippets      int
		Notice        string
		Feed          string
	}{
		basePage{Site: "gitbay"}, "cmc", d.Kind, tab,
		store.Profile{Description: "Org-Mode · Self-Hosting · Privacy"}, d.AboutHTML,
		[]profileRepoRow{{control.ProfileRepo{Path: "cmc/reminiscecleberg.com", Description: "Personal placeholder site."}}},
		d.Members, nil,
		[]activityWeek{{Month: "Sep", Days: []activityDay{{Date: "2026-09-20", Count: 3, Level: 2}}}}, 6088,
		nil, d.CanAdmin, false, 0, "", "/cmc/activity.atom",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}
