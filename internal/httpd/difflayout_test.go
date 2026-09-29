package httpd

import (
	"net/http/httptest"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
	"gitbay.org/gitbay/internal/web"
)

const layoutPatch = `diff --git a/a.txt b/a.txt
--- a/a.txt
+++ b/a.txt
@@ -1,4 +1,4 @@
 keep
-old one
-old two
+new one
 tail
`

func renderCommitDiff(t *testing.T, layout diffLayout, mutate func([]diffFile)) string {
	t.Helper()
	files := parseDiff(layoutPatch)
	if mutate != nil {
		mutate(files)
	}
	if layout.Split {
		splitFiles(files)
	}
	var sb strings.Builder
	if err := web.Render(&sb, "commit.html", struct {
		repoPage
		SHA, ShortSHA, AuthorName, AuthorEmail, AuthorUser, CommitterEmail, Date, Message string
		Parents                                                                           []string
		Sig                                                                               sigView
		Checks                                                                            []store.CommitStatus
		DiffFiles                                                                         []diffFile
		DiffTruncated                                                                     bool
		Layout                                                                            diffLayout
	}{repoPage: testRepoPage(), SHA: strings.Repeat("a", 40), ShortSHA: "aaaaaaaaaa", DiffFiles: files, Layout: layout}); err != nil {
		t.Fatalf("render: %v", err)
	}
	return sb.String()
}

func TestSplitRowsPairDeletionsWithAdditions(t *testing.T) {
	files := parseDiff(layoutPatch)
	splitFiles(files)
	var pairs []splitRow
	for _, r := range files[0].Rows {
		if r.Kind == "pair" {
			pairs = append(pairs, r)
		}
	}
	if len(pairs) != 4 {
		t.Fatalf("want 4 pair rows, got %d", len(pairs))
	}
	if pairs[1].Old == nil || pairs[1].New == nil || pairs[1].Old.OldLine != 2 || pairs[1].New.NewLine != 2 {
		t.Errorf("first change row does not pair old 2 with new 2: %+v", pairs[1])
	}
	if pairs[2].Old == nil || pairs[2].Old.OldLine != 3 || pairs[2].New != nil {
		t.Errorf("surplus deletion is not left-only: %+v", pairs[2])
	}
}

func TestDiffRendersUnified(t *testing.T) {
	out := renderCommitDiff(t, diffLayout{UnifiedURL: "/x?layout=unified", SplitURL: "/x?layout=split"}, nil)
	if strings.Contains(out, `difftable split`) {
		t.Error("unified layout rendered the split table")
	}
	if !strings.Contains(out, `<a href="/x?layout=split">split</a>`) {
		t.Errorf("no link to the split layout:\n%s", out)
	}
}

func TestDiffRendersSplit(t *testing.T) {
	out := renderCommitDiff(t, diffLayout{Split: true, UnifiedURL: "/x?layout=unified", SplitURL: "/x?layout=split"}, nil)
	if !strings.Contains(out, `class="difftable split"`) {
		t.Fatalf("no split table:\n%s", out)
	}
	for _, want := range []string{`id="f0-o2"`, `id="f0-o3"`, `id="f0-n2"`, `id="f0-n1"`, `<a href="/x?layout=unified">unified</a>`} {
		if !strings.Contains(out, want) {
			t.Errorf("split output lacks %s", want)
		}
	}
	if strings.Count(out, `id="f0-n1"`) != 1 {
		t.Error("a context line's id appears more than once")
	}
	if !strings.Contains(out, `class="ln none"`) {
		t.Error("the surplus deletion has no empty right side")
	}
}

// Each column's line number links its own side, and the layout override
// travels with the link.
func TestSplitCommentLinksKeepSides(t *testing.T) {
	files := parseDiff(layoutPatch)
	splitFiles(files)
	var sb strings.Builder
	rp := testRepoPage()
	rp.Viewer = "alice"
	err := web.Render(&sb, "mr.html", mrPageData{repoPage: rp, MR: testMR("open"), View: "diff",
		DiffFiles: files, Layout: diffLayout{Split: true, Carry: "split"}})
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	out := sb.String()
	for _, want := range []string{
		`cpath=a.txt&amp;cline=2&amp;cside=old&amp;layout=split#compose`,
		`cpath=a.txt&amp;cline=2&amp;cside=new&amp;layout=split#compose`,
	} {
		if !strings.Contains(out, want) {
			i := strings.Index(out, "cpath")
			t.Errorf("lacks %s; sample %q", want, out[max(i-50, 0):min(i+150, len(out))])
		}
	}
}

func TestDiffLayoutForQueryOverridesAccount(t *testing.T) {
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	uid, _ := st.CreateUser("alice", false)
	cfg := config.Default()
	cfg.Web.Mode = "accounts"
	s := New(cfg, st, nil)
	ck := sessionCookieFor(t, s, st, uid)

	get := func(target string) diffLayout {
		req := httptest.NewRequest("GET", target, nil)
		req.AddCookie(ck)
		return s.diffLayoutFor(req)
	}
	if get("/o/r/commit/abc").Split {
		t.Error("a new account defaults to split")
	}
	if err := st.SetDiffLayout(uid, "split"); err != nil {
		t.Fatal(err)
	}
	if l := get("/o/r/commit/abc"); !l.Split || l.Carry != "" {
		t.Errorf("account setting ignored: %+v", l)
	}
	if l := get("/o/r/commit/abc?layout=unified"); l.Split || l.Carry != "unified" {
		t.Errorf("?layout=unified did not override: %+v", l)
	}
	l := get("/o/r/compare?base=main&head=x")
	if !strings.Contains(l.UnifiedURL, "base=main") || !strings.Contains(l.UnifiedURL, "layout=unified") {
		t.Errorf("switch link dropped the query: %s", l.UnifiedURL)
	}
	anon := httptest.NewRequest("GET", "/o/r/commit/abc?layout=split", nil)
	if !s.diffLayoutFor(anon).Split {
		t.Error("?layout=split ignored for a signed-out reader")
	}
}
