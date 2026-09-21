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
		"/cmc":                 "about",
		"/cmc/-/repositories":  "repos",
		"/cmc/-/bookmarks":     "bookmarks",
		"/cmc/-/snippets":      "snippets",
		"/cmc/-/people":        "people",
		"/krz":                 "about",
		"/cmc/-/snippets/abcd": "about",
	} {
		if got := profileTab(path); got != want {
			t.Errorf("profileTab(%q) = %q, want %q", path, got, want)
		}
	}
}

// The profile is sections rather than one stack (#242): the bar reads
// About, Repositories, Bookmarks, Snippets, and each holds one thing.
func TestOwnerPageTabOrder(t *testing.T) {
	d := ownerFixture()
	d.Self, d.Snippets = true, 2
	out := renderOwner(t, "about", d)
	order := []string{
		`href="/cmc">About`,
		`href="/cmc/-/repositories">Repositories`,
		`href="/cmc/-/bookmarks">Bookmarks`,
		`href="/cmc/-/snippets">Snippets`,
	}
	at := -1
	for _, want := range order {
		i := strings.Index(out, want)
		if i < 0 {
			t.Fatalf("tab bar missing %q:\n%s", want, out)
		}
		if i < at {
			t.Errorf("tab %q is out of order", want)
		}
		at = i
	}
}

// The graph moved inside About, with a log of the newest events under
// it — not the whole history, which is what the atom feed is for.
func TestOwnerPageAboutCarriesTheGraphAndLog(t *testing.T) {
	d := ownerFixture()
	d.Log = []feedLine{{Actor: "cmc", Verb: "opened issue", Ref: "#12", Repo: "krz/gitbay", URL: "/krz/gitbay/issues/12"}}
	out := renderOwner(t, "about", d)
	for _, want := range []string{"Christian Cleberg", "actgraph", "opened issue", "#12", "/cmc/activity.atom"} {
		if !strings.Contains(out, want) {
			t.Errorf("about tab missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "reminiscecleberg.com") {
		t.Errorf("the about tab carries the repository list:\n%s", out)
	}
	// A profile with no About file still has the graph, so the tab is
	// never empty and never a 404.
	d.AboutHTML = ""
	if out := renderOwner(t, "about", d); !strings.Contains(out, "actgraph") {
		t.Errorf("a profile with no About file loses its graph:\n%s", out)
	}
	// Nothing to log reads as nothing, not as an empty block.
	d.Log = nil
	if out := renderOwner(t, "about", d); !strings.Contains(out, "Nothing yet.") {
		t.Errorf("an empty log says nothing:\n%s", out)
	}
}

func TestOwnerPageSectionsAreSeparate(t *testing.T) {
	d := ownerFixture()
	d.Self, d.Snippets = true, 1
	d.Bookmarks = []control.BookmarkOut{{Path: "krz/hutch", Description: "a SourceHut client", Visibility: "public"}}
	d.SnippetRows = []snippetRow{{store.Snippet{PublicID: "ab12", Description: "a shell one-liner", Visibility: "public"}, "run.sh"}}

	repos := renderOwner(t, "repos", d)
	if !strings.Contains(repos, "reminiscecleberg.com") || strings.Contains(repos, "actgraph") {
		t.Errorf("repos tab is not the repository list alone:\n%s", repos)
	}
	marks := renderOwner(t, "bookmarks", d)
	if !strings.Contains(marks, "krz/hutch") || strings.Contains(marks, "reminiscecleberg.com") {
		t.Errorf("bookmarks tab is not the bookmark list alone:\n%s", marks)
	}
	// The snippet list renders here rather than on a page of its own.
	snips := renderOwner(t, "snippets", d)
	for _, want := range []string{"a shell one-liner", "run.sh", `href="/cmc/-/snippets/ab12"`, "new snippet"} {
		if !strings.Contains(snips, want) {
			t.Errorf("snippets tab missing %q:\n%s", want, snips)
		}
	}
	if !strings.Contains(snips, `href="/cmc/-/repositories">Repositories`) {
		t.Errorf("the snippets tab lost the profile's tab bar:\n%s", snips)
	}
}

// Bookmarks are the viewer's own: repo bookmarks takes no owner and
// lists the caller's. The tab is not offered on anyone else's profile.
func TestOwnerPageBookmarksTabIsSelfOnly(t *testing.T) {
	if out := renderOwner(t, "about", ownerFixture()); strings.Contains(out, "/-/bookmarks") {
		t.Errorf("a stranger's profile offers a bookmarks tab:\n%s", out)
	}
	d := ownerFixture()
	d.Self = true
	if out := renderOwner(t, "about", d); !strings.Contains(out, "/-/bookmarks") {
		t.Errorf("own profile has no bookmarks tab:\n%s", out)
	}
}

func TestOwnerPagePeopleTabHoldsTheAdminPanel(t *testing.T) {
	d := ownerFixture()
	d.Kind = "org"
	d.CanAdmin = true
	d.Members = []control.ProfileMember{{Name: "cmc", Role: "admin"}}

	about := renderOwner(t, "about", d)
	if !strings.Contains(about, `href="/cmc/-/people"`) {
		t.Errorf("an admin gets no people tab:\n%s", about)
	}
	if strings.Contains(about, "Create a team") {
		t.Errorf("the admin forms still sit on another tab:\n%s", about)
	}
	// An org has no snippets, so it is not offered the tab.
	if strings.Contains(about, "/-/snippets") {
		t.Errorf("an org offers a snippets tab:\n%s", about)
	}
	people := renderOwner(t, "people", d)
	for _, want := range []string{"Create a team", "member-add", "org-rename"} {
		if !strings.Contains(people, want) {
			t.Errorf("people tab missing %q:\n%s", want, people)
		}
	}
}

type ownerFixtureData struct {
	Kind        string
	AboutHTML   template.HTML
	CanAdmin    bool
	Self        bool
	Snippets    int
	Members     []control.ProfileMember
	Log         []feedLine
	Bookmarks   []control.BookmarkOut
	SnippetRows []snippetRow
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
	err := web.Render(&sb, "owner.html", ownerPage{
		basePage:      basePage{Site: "gitbay"},
		Owner:         "cmc",
		Kind:          d.Kind,
		Tab:           tab,
		Profile:       store.Profile{Description: "Org-Mode · Self-Hosting · Privacy"},
		AboutHTML:     d.AboutHTML,
		Repos:         []profileRepoRow{{control.ProfileRepo{Path: "cmc/reminiscecleberg.com", Description: "Personal placeholder site."}}},
		Members:       d.Members,
		Activity:      []activityWeek{{Month: "Sep", Days: []activityDay{{Date: "2026-09-20", Count: 3, Level: 2}}}},
		ActivityTotal: 6088,
		Log:           d.Log,
		Bookmarks:     d.Bookmarks,
		SnippetRows:   d.SnippetRows,
		CanAdmin:      d.CanAdmin,
		Self:          d.Self,
		Snippets:      d.Snippets,
		Feed:          "/cmc/activity.atom",
	})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}
