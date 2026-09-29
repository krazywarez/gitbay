package httpd

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/store"
)

type p296b struct {
	s      *Server
	st     *store.Store
	h      http.Handler
	repo   store.Repo
	orgID  int64
	alice  *http.Cookie
	bob    *http.Cookie
	aliceU store.User
}

func newP296b(t *testing.T) *p296b {
	t.Helper()
	e := newSettingsEnv(t)
	e.s.cfg.Limits.MaxAssetBytes = 1 << 10
	orgID, err := e.st.CreateOrg("acme", e.alice.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.st.SetOrgMember(orgID, e.bob.ID, "member"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.st.CreateRelease(e.repo.ID, "v1", "One", "", e.alice.ID, "md"); err != nil {
		t.Fatal(err)
	}
	return &p296b{s: e.s, st: e.st, h: e.s.Handler(), repo: e.repo, orgID: orgID,
		alice: sessionCookieFor(t, e.s, e.st, e.alice.ID),
		bob:   sessionCookieFor(t, e.s, e.st, e.bob.ID), aliceU: e.alice}
}

func (p *p296b) do(method, path string, ck *http.Cookie, body io.Reader, ctype string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, body)
	if ctype != "" {
		req.Header.Set("Content-Type", ctype)
	}
	req.AddCookie(ck)
	rr := httptest.NewRecorder()
	p.h.ServeHTTP(rr, req)
	return rr
}

func (p *p296b) post(path string, ck *http.Cookie, f url.Values) *httptest.ResponseRecorder {
	return p.do("POST", path, ck, strings.NewReader(f.Encode()), "application/x-www-form-urlencoded")
}

// follow returns the page a redirect points at, carrying the flash.
func (p *p296b) follow(rr *httptest.ResponseRecorder, ck *http.Cookie) string {
	req := httptest.NewRequest("GET", rr.Header().Get("Location"), nil)
	req.AddCookie(ck)
	for _, c := range rr.Result().Cookies() {
		req.AddCookie(c)
	}
	out := httptest.NewRecorder()
	p.h.ServeHTTP(out, req)
	return out.Body.String()
}

func (p *p296b) get(path string, ck *http.Cookie) string {
	return p.do("GET", path, ck, nil, "").Body.String()
}

func upload(t *testing.T, fields map[string]string, fname string, data []byte) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	if fname != "" {
		fw, _ := mw.CreateFormFile("file", fname)
		fw.Write(data)
	}
	mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestReleaseAssetUploadAndRemove(t *testing.T) {
	p := newP296b(t)
	const path = "/alice/app/releases/assets"
	body, ct := upload(t, map[string]string{"tag": "v1"}, "tool.bin", []byte("payload"))
	rr := p.do("POST", path, p.alice, body, ct)
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("upload: %d %s", rr.Code, rr.Body.String())
	}
	rel, _ := p.st.ReleaseByTag(p.repo.ID, "v1")
	if len(rel.Assets) != 1 || rel.Assets[0].Name != "tool.bin" || rel.Assets[0].Size != 7 {
		t.Fatalf("assets %+v", rel.Assets)
	}
	page := p.get("/alice/app/releases", p.alice)
	for _, want := range []string{"Add asset", `enctype="multipart/form-data"`, "tool.bin", `value="remove"`} {
		if !strings.Contains(page, want) {
			t.Errorf("writer page lacks %q", want)
		}
	}

	// Refusals: over the limit, empty, bad name, no file.
	for name, tc := range map[string]struct {
		fname string
		data  []byte
		field map[string]string
		want  string
	}{
		"oversized":  {"big.bin", bytes.Repeat([]byte("x"), 2<<10), map[string]string{"tag": "v1"}, "max_asset_bytes"},
		"way over":   {"huge.bin", bytes.Repeat([]byte("x"), 3<<20), map[string]string{"tag": "v1"}, "max_asset_bytes"},
		"empty":      {"e.bin", nil, map[string]string{"tag": "v1"}, "empty"},
		"bad name":   {"x.bin", []byte("x"), map[string]string{"tag": "v1", "name": ".hidden"}, "invalid asset name"},
		"no file":    {"", nil, map[string]string{"tag": "v1"}, "choose a file"},
		"no release": {"a.bin", []byte("x"), map[string]string{"tag": "v9"}, ""},
	} {
		body, ct := upload(t, tc.field, tc.fname, tc.data)
		rr := p.do("POST", path, p.alice, body, ct)
		if tc.want == "" {
			if rr.Code != http.StatusNotFound {
				t.Errorf("%s: %d", name, rr.Code)
			}
			continue
		}
		if rr.Code != http.StatusSeeOther || !strings.Contains(p.follow(rr, p.alice), tc.want) {
			t.Errorf("%s: %d, no %q on the page", name, rr.Code, tc.want)
		}
	}
	if rel, _ = p.st.ReleaseByTag(p.repo.ID, "v1"); len(rel.Assets) != 1 {
		t.Fatalf("a refused upload stored something: %+v", rel.Assets)
	}

	// Remove needs the name typed.
	rr = p.post(path, p.alice, url.Values{"action": {"remove"}, "tag": {"v1"}, "name": {"tool.bin"}})
	if !strings.Contains(p.follow(rr, p.alice), "type tool.bin to confirm") {
		t.Fatal("no confirmation demanded")
	}
	if rel, _ = p.st.ReleaseByTag(p.repo.ID, "v1"); len(rel.Assets) != 1 {
		t.Fatal("removed without confirmation")
	}
	p.post(path, p.alice, url.Values{"action": {"remove"}, "tag": {"v1"}, "name": {"tool.bin"}, "confirm": {"tool.bin"}})
	if rel, _ = p.st.ReleaseByTag(p.repo.ID, "v1"); len(rel.Assets) != 0 {
		t.Fatalf("not removed: %+v", rel.Assets)
	}
}

func TestReleaseAssetReaderSeesNoFormAndIsRefused(t *testing.T) {
	p := newP296b(t)
	body, ct := upload(t, map[string]string{"tag": "v1"}, "tool.bin", []byte("payload"))
	rr := p.do("POST", "/alice/app/releases/assets", p.bob, body, ct)
	if !strings.Contains(p.follow(rr, p.bob), `role="alert"`) {
		t.Fatalf("no refusal shown: %d", rr.Code)
	}
	if rel, _ := p.st.ReleaseByTag(p.repo.ID, "v1"); len(rel.Assets) != 0 {
		t.Fatal("a reader uploaded an asset")
	}
	page := p.get("/alice/app/releases", p.bob)
	if strings.Contains(page, "Add asset") || strings.Contains(page, "releases/assets") {
		t.Fatal("reader sees the asset form")
	}
}

func TestRepoMilestoneCreateCloseReopen(t *testing.T) {
	p := newP296b(t)
	const path = "/alice/app/milestones"
	rr := p.post(path, p.alice, url.Values{"title": {"v2"}, "description": {"next"}, "due": {"2026-12-01"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", rr.Code)
	}
	m, err := p.st.MilestoneByTitle(p.repo, "v2")
	if err != nil || m.Description != "next" || m.DueDate != "2026-12-01" {
		t.Fatalf("%+v %v", m, err)
	}
	page := p.get(path, p.alice)
	for _, want := range []string{"New milestone", `value="close"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	p.post(path, p.alice, url.Values{"action": {"close"}, "title": {"v2"}})
	if m, _ = p.st.MilestoneByTitle(p.repo, "v2"); m.State != "closed" {
		t.Fatalf("state %q", m.State)
	}
	p.post(path, p.alice, url.Values{"action": {"reopen"}, "title": {"v2"}})
	if m, _ = p.st.MilestoneByTitle(p.repo, "v2"); m.State != "open" {
		t.Fatalf("state %q", m.State)
	}

	// Refusals show on the page.
	rr = p.post(path, p.alice, url.Values{"title": {"v3"}, "due": {"soon"}})
	if !strings.Contains(p.follow(rr, p.alice), "--due must be YYYY-MM-DD") {
		t.Fatal("bad date not reported")
	}
	rr = p.post(path, p.alice, url.Values{"title": {"v2"}})
	if rr.Code != http.StatusSeeOther || !strings.Contains(p.follow(rr, p.alice), `role="alert"`) {
		t.Fatal("duplicate not reported")
	}
}

func TestRepoMilestoneReaderSeesNoForm(t *testing.T) {
	p := newP296b(t)
	page := p.get("/alice/app/milestones", p.bob)
	if strings.Contains(page, "New milestone") || strings.Contains(page, `action="/alice/app/milestones"`) {
		t.Fatal("reader sees the form")
	}
	rr := p.post("/alice/app/milestones", p.bob, url.Values{"title": {"sneaky"}})
	if !strings.Contains(p.follow(rr, p.bob), `role="alert"`) {
		t.Fatal("no refusal")
	}
	if _, err := p.st.MilestoneByTitle(p.repo, "sneaky"); err == nil {
		t.Fatal("a reader created a milestone")
	}
}

func TestOrgLabelSetAndRemove(t *testing.T) {
	p := newP296b(t)
	const path = "/acme/-/labels"
	rr := p.post(path, p.alice, url.Values{"name": {"bug"}, "color": {"d73a4a"}})
	if rr.Code != http.StatusSeeOther {
		t.Fatalf("set: %d", rr.Code)
	}
	labels, _ := p.st.ListOrgLabels(p.orgID, nil)
	if len(labels) != 1 || labels[0].Name != "bug" || labels[0].Color != "#d73a4a" {
		t.Fatalf("%+v", labels)
	}
	page := p.get(path, p.alice)
	for _, want := range []string{"New org label", `value="remove"`, `aria-label="Colour for bug"`} {
		if !strings.Contains(page, want) {
			t.Errorf("page lacks %q", want)
		}
	}
	rr = p.post(path, p.alice, url.Values{"name": {"bug"}, "color": {"zzz"}})
	if !strings.Contains(p.follow(rr, p.alice), "--color takes rrggbb") {
		t.Fatal("bad colour not reported")
	}
	rr = p.post(path, p.alice, url.Values{"action": {"remove"}, "name": {"bug"}})
	if !strings.Contains(p.follow(rr, p.alice), "type bug to confirm") {
		t.Fatal("no confirmation demanded")
	}
	if labels, _ = p.st.ListOrgLabels(p.orgID, nil); len(labels) != 1 {
		t.Fatal("removed without confirmation")
	}
	p.post(path, p.alice, url.Values{"action": {"remove"}, "name": {"bug"}, "confirm": {"bug"}})
	if labels, _ = p.st.ListOrgLabels(p.orgID, nil); len(labels) != 0 {
		t.Fatalf("not removed: %+v", labels)
	}
}

func TestOrgLabelMemberSeesNoFormAndIsRefused(t *testing.T) {
	p := newP296b(t)
	page := p.get("/acme/-/labels", p.bob)
	if strings.Contains(page, "New org label") || strings.Contains(page, `action="/acme/-/labels"`) {
		t.Fatal("member sees the form")
	}
	rr := p.post("/acme/-/labels", p.bob, url.Values{"name": {"bug"}})
	if !strings.Contains(p.follow(rr, p.bob), "only admins of acme") {
		t.Fatalf("no refusal: %d", rr.Code)
	}
	if labels, _ := p.st.ListOrgLabels(p.orgID, nil); len(labels) != 0 {
		t.Fatal("a member created an org label")
	}
}

func TestOrgMilestoneCreateCloseReopen(t *testing.T) {
	p := newP296b(t)
	const path = "/acme/-/milestones"
	if rr := p.post(path, p.alice, url.Values{"title": {"mobile"}, "due": {"2026-12-01"}}); rr.Code != http.StatusSeeOther {
		t.Fatalf("create: %d", rr.Code)
	}
	state := func(s string) int {
		ms, _ := p.st.ListOrgMilestones(p.orgID, s, nil)
		return len(ms)
	}
	if state("open") != 1 {
		t.Fatal("not created")
	}
	if page := p.get(path, p.alice); !strings.Contains(page, "New org milestone") || !strings.Contains(page, `value="close"`) {
		t.Fatal("admin page lacks the controls")
	}
	p.post(path, p.alice, url.Values{"action": {"close"}, "title": {"mobile"}})
	if state("closed") != 1 || state("open") != 0 {
		t.Fatal("not closed")
	}
	p.post(path, p.alice, url.Values{"action": {"reopen"}, "title": {"mobile"}})
	if state("open") != 1 {
		t.Fatal("not reopened")
	}
	rr := p.post(path, p.alice, url.Values{"title": {"mobile"}})
	if !strings.Contains(p.follow(rr, p.alice), `role="alert"`) {
		t.Fatal("duplicate not reported")
	}
}

func TestOrgMilestoneMemberSeesNoFormAndIsRefused(t *testing.T) {
	p := newP296b(t)
	page := p.get("/acme/-/milestones", p.bob)
	if strings.Contains(page, "New org milestone") || strings.Contains(page, `action="/acme/-/milestones"`) {
		t.Fatal("member sees the form")
	}
	rr := p.post("/acme/-/milestones", p.bob, url.Values{"title": {"sneaky"}})
	if !strings.Contains(p.follow(rr, p.bob), "only admins of acme") {
		t.Fatalf("no refusal: %d", rr.Code)
	}
	if ms, _ := p.st.ListOrgMilestones(p.orgID, "all", nil); len(ms) != 0 {
		t.Fatal("a member created an org milestone")
	}
}
