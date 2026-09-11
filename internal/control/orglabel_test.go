package control

import (
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/protocol"
)

func TestOrgLabelSetListRemove(t *testing.T) {
	f := newOrgFixture(t)
	// Two repos already hold bug; the org set folds them in.
	f.st.SetLabel(f.core, "bug", "")
	f.st.SetLabel(f.priv, "bug", "")
	c, out := f.ctx(f.alice)
	if code := runOrgLabelSet(c, []string{"acme", "bug", "--color", "ff0000"}); code != protocol.ExitOK ||
		!strings.Contains(out.String(), `"folded":2`) {
		t.Fatalf("set: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runOrgLabelList(c, []string{"acme"}); code != protocol.ExitOK ||
		!strings.Contains(out.String(), `"name":"bug"`) || !strings.Contains(out.String(), `"color":"#ff0000"`) {
		t.Fatalf("list: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runOrgLabelRemove(c, []string{"acme", "bug"}); code != protocol.ExitOK {
		t.Fatalf("remove: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runOrgLabelRemove(c, []string{"acme", "bug"}); code != protocol.ExitNotFound {
		t.Fatalf("second remove: exit %d %s", code, out.String())
	}
}

func TestOrgLabelWritesNeedOrgAdmin(t *testing.T) {
	f := newOrgFixture(t)
	c, out := f.ctx(f.bob)
	if code := runOrgLabelSet(c, []string{"acme", "bug"}); code != protocol.ExitDenied {
		t.Fatalf("member set: exit %d %s", code, out.String())
	}
	if code := runOrgLabelRemove(c, []string{"acme", "bug"}); code != protocol.ExitDenied {
		t.Fatalf("member remove: exit %d %s", code, out.String())
	}
	c, out = f.ctx(f.alice)
	if code := runOrgLabelSet(c, []string{"nope", "bug"}); code != protocol.ExitNotFound {
		t.Fatalf("missing org: exit %d %s", code, out.String())
	}
	if code := runOrgLabelSet(c, []string{"acme", "bug", "--color", "zz"}); code != protocol.ExitUsage {
		t.Fatalf("bad colour: exit %d %s", code, out.String())
	}
}

func TestOrgLabelListVisibility(t *testing.T) {
	f := newOrgFixture(t)
	f.st.SetOrgLabel(f.org, "bug", "")
	// Members read; an outsider reads because acme/core is public.
	for _, uid := range []int64{f.bob, f.carol} {
		c, out := f.ctx(uid)
		if code := runOrgLabelList(c, []string{"acme"}); code != protocol.ExitOK {
			t.Fatalf("user %d list: exit %d %s", uid, code, out.String())
		}
	}
	// With every repo private, the outsider is refused, not told the org
	// is missing.
	f.st.SetRepoVisibility(f.core.ID, "private")
	c, out := f.ctx(f.carol)
	if code := runOrgLabelList(c, []string{"acme"}); code != protocol.ExitDenied ||
		!strings.Contains(out.String(), "visible to its members") {
		t.Fatalf("outsider list: exit %d %s", code, out.String())
	}
}

func TestOrgMilestoneLifecycle(t *testing.T) {
	f := newOrgFixture(t)
	f.st.CreateMilestone(f.core, "v1", "", "")
	c, out := f.ctx(f.alice)
	if code := runOrgMilestoneCreate(c, []string{"acme", "v1", "--due", "2027-01-01"}); code != protocol.ExitOK ||
		!strings.Contains(out.String(), `"folded":1`) {
		t.Fatalf("create: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runOrgMilestoneCreate(c, []string{"acme", "v1"}); code != protocol.ExitFailure {
		t.Fatalf("duplicate create: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runOrgMilestoneCreate(c, []string{"acme", "v2", "--due", "soon"}); code != protocol.ExitUsage {
		t.Fatalf("bad due: exit %d %s", code, out.String())
	}
	out.Reset()
	// An issue in each repo attaches by title; progress spans both.
	f.st.CreateIssue(f.core.ID, f.alice, "c1", "", "md")
	f.st.CreateIssue(f.priv.ID, f.alice, "p1", "", "md")
	runIssueMilestone(c, []string{"acme/core", "1", "v1"})
	runIssueMilestone(c, []string{"acme/priv", "1", "v1"})
	out.Reset()
	if code := runOrgMilestoneList(c, []string{"acme"}); code != protocol.ExitOK ||
		!strings.Contains(out.String(), `"open":2`) || !strings.Contains(out.String(), `"due":"2027-01-01"`) {
		t.Fatalf("list: exit %d %s", code, out.String())
	}
	out.Reset()
	// carol reads only the public repo's count.
	cc, cout := f.ctx(f.carol)
	if code := runOrgMilestoneList(cc, []string{"acme"}); code != protocol.ExitOK || !strings.Contains(cout.String(), `"open":1`) {
		t.Fatalf("outsider list: exit %d %s", code, cout.String())
	}
	if code := runOrgMilestoneClose(c, []string{"acme", "v1"}); code != protocol.ExitOK {
		t.Fatalf("close: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runOrgMilestoneList(c, []string{"acme"}); code != protocol.ExitOK || strings.Contains(out.String(), `"title":"v1"`) {
		t.Fatalf("closed still listed as open: %s", out.String())
	}
	out.Reset()
	if code := runOrgMilestoneReopen(c, []string{"acme", "v1"}); code != protocol.ExitOK {
		t.Fatalf("reopen: exit %d %s", code, out.String())
	}
	out.Reset()
	if code := runOrgMilestoneClose(c, []string{"acme", "nope"}); code != protocol.ExitNotFound {
		t.Fatalf("close missing: exit %d %s", code, out.String())
	}
	bc, bout := f.ctx(f.bob)
	if code := runOrgMilestoneClose(bc, []string{"acme", "v1"}); code != protocol.ExitDenied {
		t.Fatalf("member close: exit %d %s", code, bout.String())
	}
}
