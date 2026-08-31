package deps

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/store"
)

func testWorker(t *testing.T) (*Worker, store.Repo) {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "gitbay.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	owner, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddEmail(owner, "alice@example.com", "admin", true); err != nil {
		t.Fatal(err)
	}
	id, err := st.CreateRepo("user", owner, "thing", "public")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoByID(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EnableDepCheck(repo.ID); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Server.SiteURL = "https://gitbay.test"
	cfg.Mail.SMTPHost = "localhost:587"
	return &Worker{St: st, Cfg: cfg}, repo
}

func reports(pairs ...string) []store.DepReport {
	var out []store.DepReport
	for i := 0; i < len(pairs); i += 3 {
		out = append(out, store.DepReport{
			Ecosystem: EcoGo, Name: pairs[i], Current: pairs[i+1], Latest: pairs[i+2]})
	}
	return out
}

func TestReconcileIssueLifecycle(t *testing.T) {
	w, repo := testWorker(t)

	// Nothing behind: no issue, no mail.
	if err := w.reconcile(repo, nil); err != nil {
		t.Fatal(err)
	}
	if check, _ := w.St.DepCheckFor(repo.ID); check.IssueNumber != 0 {
		t.Fatalf("issue %d opened with nothing behind", check.IssueNumber)
	}

	// Something falls behind: one issue, one notification.
	if err := w.reconcile(repo, reports("github.com/a/b", "v1.0.0", "v1.1.0")); err != nil {
		t.Fatal(err)
	}
	check, err := w.St.DepCheckFor(repo.ID)
	if err != nil || check.IssueNumber == 0 {
		t.Fatalf("no issue opened: %v", err)
	}
	issue, err := w.St.IssueByNumber(repo.ID, check.IssueNumber)
	if err != nil {
		t.Fatal(err)
	}
	if issue.Author != store.BotUsername {
		t.Errorf("issue author = %q, want %q", issue.Author, store.BotUsername)
	}
	if issue.Title != IssueTitle {
		t.Errorf("issue title = %q", issue.Title)
	}
	if !strings.Contains(issue.Body, "github.com/a/b") || !strings.Contains(issue.Body, "v1.1.0") {
		t.Errorf("issue body missing the dependency:\n%s", issue.Body)
	}
	mail, err := w.St.DueMail(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(mail) != 1 || mail[0].Recipient != "alice@example.com" {
		t.Fatalf("mail = %+v, want one to the owner", mail)
	}

	// Same set again: the issue is left alone and nobody is mailed twice.
	if err := w.reconcile(repo, reports("github.com/a/b", "v1.0.0", "v1.1.0")); err != nil {
		t.Fatal(err)
	}
	if mail, _ := w.St.DueMail(10); len(mail) != 1 {
		t.Errorf("unchanged set mailed again: %d messages", len(mail))
	}

	// The set changes: same issue, rewritten body, another notification.
	if err := w.reconcile(repo, reports("github.com/a/b", "v1.0.0", "v1.2.0")); err != nil {
		t.Fatal(err)
	}
	after, _ := w.St.DepCheckFor(repo.ID)
	if after.IssueNumber != check.IssueNumber {
		t.Errorf("second issue opened: %d then %d", check.IssueNumber, after.IssueNumber)
	}
	issue, _ = w.St.IssueByNumber(repo.ID, check.IssueNumber)
	if !strings.Contains(issue.Body, "v1.2.0") {
		t.Errorf("issue body not rewritten:\n%s", issue.Body)
	}
	if mail, _ := w.St.DueMail(10); len(mail) != 2 {
		t.Errorf("changed set produced %d messages, want 2", len(mail))
	}

	// Caught up: the issue closes and the reports are forgotten.
	if err := w.reconcile(repo, nil); err != nil {
		t.Fatal(err)
	}
	issue, _ = w.St.IssueByNumber(repo.ID, check.IssueNumber)
	if issue.State != "closed" {
		t.Errorf("issue state = %q, want closed", issue.State)
	}
	if left, _ := w.St.ReportedDeps(repo.ID); len(left) != 0 {
		t.Errorf("reports left behind: %v", left)
	}
	if mail, _ := w.St.DueMail(10); len(mail) != 2 {
		t.Errorf("closing mailed: %d messages", len(mail))
	}
}

func TestReconcileOpensFreshIssueAfterClose(t *testing.T) {
	w, repo := testWorker(t)
	if err := w.reconcile(repo, reports("github.com/a/b", "v1.0.0", "v1.1.0")); err != nil {
		t.Fatal(err)
	}
	first, _ := w.St.DepCheckFor(repo.ID)
	issue, _ := w.St.IssueByNumber(repo.ID, first.IssueNumber)

	// The maintainer closes it. The worker does not reopen it; the next
	// change gets its own issue.
	if err := w.St.SetIssueState(issue.ID, "closed"); err != nil {
		t.Fatal(err)
	}
	if err := w.reconcile(repo, reports("github.com/a/b", "v1.0.0", "v1.3.0")); err != nil {
		t.Fatal(err)
	}
	second, _ := w.St.DepCheckFor(repo.ID)
	if second.IssueNumber == first.IssueNumber {
		t.Fatalf("reused closed issue #%d", first.IssueNumber)
	}
	if reopened, _ := w.St.IssueByNumber(repo.ID, first.IssueNumber); reopened.State != "closed" {
		t.Error("the closed issue was reopened")
	}
}

func TestBehindQueriesRegistries(t *testing.T) {
	srv, _ := fakeRegistry(t, map[string]string{
		"/github.com/a/b/@latest": `{"Version":"v1.1.0"}`,
		"/github.com/c/d/@latest": `{"Version":"v2.0.0"}`,
	})
	w, _ := testWorker(t)
	w.Client = NewClient("test")
	w.Client.Hosts = map[string]string{EcoGo: srv.URL}

	got, err := w.behind(context.Background(), []Dep{
		{Ecosystem: EcoGo, Name: "github.com/a/b", Current: "v1.0.0"}, // behind
		{Ecosystem: EcoGo, Name: "github.com/c/d", Current: "v2.0.0"}, // current
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Name != "github.com/a/b" || got[0].Latest != "v1.1.0" {
		t.Fatalf("behind = %+v", got)
	}
}

func TestBehindToleratesOneFailureButNotAll(t *testing.T) {
	srv, _ := fakeRegistry(t, map[string]string{"/github.com/a/b/@latest": `{"Version":"v1.1.0"}`})
	w, _ := testWorker(t)
	w.Client = NewClient("test")
	w.Client.Hosts = map[string]string{EcoGo: srv.URL}
	found := []Dep{
		{Ecosystem: EcoGo, Name: "github.com/a/b", Current: "v1.0.0"},
		{Ecosystem: EcoGo, Name: "github.com/gone/away", Current: "v1.0.0"}, // 404s
	}
	got, err := w.behind(context.Background(), found)
	if err != nil {
		t.Fatalf("one failed lookup failed the sweep: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("behind = %+v", got)
	}
	if _, err := w.behind(context.Background(), found[1:]); err == nil {
		t.Error("every lookup failing was reported as success")
	}
}

func TestReconcileLeavesClosedIssueClosedOnUnchangedSet(t *testing.T) {
	w, repo := testWorker(t)
	behind := reports("github.com/a/b", "v1.0.0", "v1.1.0")
	if err := w.reconcile(repo, behind); err != nil {
		t.Fatal(err)
	}
	check, _ := w.St.DepCheckFor(repo.ID)
	issue, _ := w.St.IssueByNumber(repo.ID, check.IssueNumber)
	if err := w.St.SetIssueState(issue.ID, "closed"); err != nil {
		t.Fatal(err)
	}
	// Nothing has changed, so closing the issue has to stick.
	if err := w.reconcile(repo, behind); err != nil {
		t.Fatal(err)
	}
	after, _ := w.St.DepCheckFor(repo.ID)
	if after.IssueNumber != check.IssueNumber {
		t.Fatalf("opened issue #%d on an unchanged set", after.IssueNumber)
	}
	if again, _ := w.St.IssueByNumber(repo.ID, check.IssueNumber); again.State != "closed" {
		t.Error("the closed issue came back")
	}
}
