package mailin

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/imapc"
	"gitbay.org/gitbay/internal/mailreply"
	"gitbay.org/gitbay/internal/seal"
	"gitbay.org/gitbay/internal/store"
)

const replyBase = "reply@gitbay.example"

type fixture struct {
	p       *Processor
	st      *store.Store
	repo    store.Repo
	issueID int64
	bob     int64
	secrets [][]byte
}

// setup is alice's public repository alice/app with issue #1, and bob,
// who has a verified address and reply by mail on.
func setup(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.MigrateUp(); err != nil {
		t.Fatal(err)
	}
	key, err := seal.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	keyFile := t.TempDir() + "/secret.key"
	if err := seal.WriteKeys(keyFile, []seal.Key{key}); err != nil {
		t.Fatal(err)
	}
	ring, err := seal.Load(keyFile)
	if err != nil {
		t.Fatal(err)
	}
	st.SetKeyring(ring)
	alice, err := st.CreateUser("alice", false)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := st.CreateUser("bob", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddEmail(bob, "Bob@Example.test", "admin", true); err != nil {
		t.Fatal(err)
	}
	if err := st.AddEmail(bob, "old@example.test", "", false); err != nil {
		t.Fatal(err)
	}
	st.SetReplyEnabled(bob, true)
	repoID, err := st.CreateRepo("user", alice, "app", "public")
	if err != nil {
		t.Fatal(err)
	}
	repo, err := st.RepoByID(repoID)
	if err != nil {
		t.Fatal(err)
	}
	issueID, err := st.CreateIssue(repo.ID, alice, "title", "", "md")
	if err != nil {
		t.Fatal(err)
	}
	var cfg config.Config
	cfg.Server.SiteURL = "https://gitbay.example"
	cfg.Mail.Inbound = config.MailInbound{Enabled: true, ReplyAddress: replyBase}
	secrets, err := ring.Derive(mailreply.Purpose)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{p: &Processor{St: st, Cfg: cfg}, st: st, repo: repo,
		issueID: issueID, bob: bob, secrets: secrets}
}

func (f *fixture) token(t *testing.T, user int64) string {
	t.Helper()
	tok, err := mailreply.Mint(f.secrets, mailreply.Target{UserID: user, RepoID: f.repo.ID, Kind: "issue", Number: 1},
		time.Now().Add(mailreply.Lifetime))
	if err != nil {
		t.Fatal(err)
	}
	return tok
}

var msgSeq int

func (f *fixture) message(t *testing.T, from, body string) string {
	t.Helper()
	msgSeq++
	return fmt.Sprintf("From: Bob <%s>\r\nTo: gitbay <%s>\r\nSubject: Re: [alice/app] #1: title\r\nMessage-ID: <m%d@example.test>\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n\r\n%s\r\n", from, mailreply.Address(replyBase, f.token(t, f.bob)), msgSeq, body)
}

func (f *fixture) comments(t *testing.T) []store.IssueComment {
	t.Helper()
	cs, err := f.st.ListIssueComments(f.issueID)
	if err != nil {
		t.Fatal(err)
	}
	return cs
}

// refusalReasons reads back the audit rows the processor wrote.
func (f *fixture) refusalReasons(t *testing.T) string {
	t.Helper()
	entries, err := f.st.AuditEntries(store.AuditFilter{ActionPrefix: "refused mail reply", Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	for _, e := range entries {
		b.WriteString(e.Data + "\n")
	}
	return b.String()
}

func TestReplyPostsComment(t *testing.T) {
	f := setup(t)
	res := f.p.Handle([]byte(f.message(t, "bob@example.test", "Looks good.\r\n\r\nOn Mon, Sep 28, 2026 at 9:00 AM gitbay <x@y> wrote:\r\n> opened issue #1\r\n")))
	if !res.Posted {
		t.Fatalf("not posted: %+v", res)
	}
	cs := f.comments(t)
	if len(cs) != 1 || cs[0].Body != "Looks good." || cs[0].Author != "bob" {
		t.Fatalf("comments = %+v", cs)
	}
	entries, _ := f.st.AuditEntries(store.AuditFilter{ActionPrefix: "cmd issue comment", Limit: 10})
	if len(entries) != 1 || !strings.Contains(entries[0].Data, `"source":"mail"`) {
		t.Fatalf("audit = %+v", entries)
	}
}

func TestReplyRefusals(t *testing.T) {
	for _, tc := range []struct {
		name   string
		prep   func(t *testing.T, f *fixture) string // returns the message
		reason string
	}{
		{"wrong From", func(t *testing.T, f *fixture) string {
			return f.message(t, "mallory@example.test", "hi")
		}, "From is not a verified address"},
		{"unverified From", func(t *testing.T, f *fixture) string {
			return f.message(t, "old@example.test", "hi")
		}, "From is not a verified address"},
		{"revoked access", func(t *testing.T, f *fixture) string {
			f.st.SetRepoVisibility(f.repo.ID, "private")
			return f.message(t, "bob@example.test", "hi")
		}, "comment refused: repository alice/app not found"},
		{"disabled account", func(t *testing.T, f *fixture) string {
			f.st.SetUserDisabled(f.bob, true)
			return f.message(t, "bob@example.test", "hi")
		}, "account disabled"},
		{"archived repository", func(t *testing.T, f *fixture) string {
			f.st.UpdateRepoSettings(f.repo.ID, func(s *store.RepoSettings) { s.Archived = true })
			return f.message(t, "bob@example.test", "hi")
		}, "archived"},
		{"expired token", func(t *testing.T, f *fixture) string {
			f.p.Now = func() time.Time { return time.Now().Add(mailreply.Lifetime + time.Hour) }
			return f.message(t, "bob@example.test", "hi")
		}, "reply token expired"},
		{"reply turned off", func(t *testing.T, f *fixture) string {
			f.st.SetReplyEnabled(f.bob, false)
			return f.message(t, "bob@example.test", "hi")
		}, "reply by mail is off"},
		{"forged token", func(t *testing.T, f *fixture) string {
			m := f.message(t, "bob@example.test", "hi")
			tok := f.token(t, f.bob)
			c := "a"
			if tok[0] == 'a' {
				c = "b"
			}
			return strings.Replace(m, tok, c+tok[1:], 1)
		}, "does not verify"},
		{"no reply address", func(t *testing.T, f *fixture) string {
			return strings.Replace(f.message(t, "bob@example.test", "hi"), "reply+", "other+", 1)
		}, "not addressed to a reply address"},
		{"empty after stripping", func(t *testing.T, f *fixture) string {
			return f.message(t, "bob@example.test", "> quoted only\r\n-- \r\nBob")
		}, "empty reply"},
		{"automatic reply", func(t *testing.T, f *fixture) string {
			return "Auto-Submitted: auto-replied\r\n" + f.message(t, "bob@example.test", "I am away")
		}, "automatic reply"},
		{"html only", func(t *testing.T, f *fixture) string {
			return strings.Replace(f.message(t, "bob@example.test", "<p>hi</p>"), "text/plain", "text/html", 1)
		}, "no text/plain part"},
		{"too long", func(t *testing.T, f *fixture) string {
			return f.message(t, "bob@example.test", strings.Repeat("a", 70<<10))
		}, "reply too long"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t)
			res := f.p.Handle([]byte(tc.prep(t, f)))
			if res.Posted || res.Retry || !strings.Contains(res.Reason, tc.reason) {
				t.Fatalf("result %+v, want refusal %q", res, tc.reason)
			}
			if n := len(f.comments(t)); n != 0 {
				t.Fatalf("%d comments posted", n)
			}
			audit := f.refusalReasons(t)
			if !strings.Contains(audit, tc.reason) {
				t.Fatalf("audit does not name the reason:\n%s", audit)
			}
			if strings.Contains(audit, "I am away") || strings.Contains(audit, "<p>hi") {
				t.Fatalf("audit carries message content:\n%s", audit)
			}
		})
	}
}

func TestDuplicateMessageID(t *testing.T) {
	f := setup(t)
	m := []byte(f.message(t, "bob@example.test", "once"))
	if res := f.p.Handle(m); !res.Posted {
		t.Fatalf("first: %+v", res)
	}
	if res := f.p.Handle(m); res.Posted || !strings.Contains(res.Reason, "already posted") {
		t.Fatalf("second: %+v", res)
	}
	if n := len(f.comments(t)); n != 1 {
		t.Fatalf("%d comments", n)
	}
}

// A refused reply leaves no claim, so the same message is judged afresh.
func TestRefusalLeavesNoClaim(t *testing.T) {
	f := setup(t)
	f.st.UpdateRepoSettings(f.repo.ID, func(s *store.RepoSettings) { s.Archived = true })
	m := []byte(f.message(t, "bob@example.test", "hi"))
	if res := f.p.Handle(m); res.Posted {
		t.Fatal("posted to an archived repository")
	}
	f.st.UpdateRepoSettings(f.repo.ID, func(s *store.RepoSettings) { s.Archived = false })
	if res := f.p.Handle(m); !res.Posted {
		t.Fatalf("after unarchive: %+v", res)
	}
}

type fakeMailbox struct {
	msgs map[uint32][]byte
	seen map[uint32]bool
}

func (m *fakeMailbox) Unseen() ([]uint32, error) {
	var out []uint32
	for uid := range m.msgs {
		if !m.seen[uid] {
			out = append(out, uid)
		}
	}
	return out, nil
}

func (m *fakeMailbox) Fetch(uid uint32) ([]byte, error) {
	if m.msgs[uid] == nil {
		return nil, imapc.ErrTooLarge
	}
	return m.msgs[uid], nil
}

func (m *fakeMailbox) MarkSeen(uid uint32) error {
	m.seen[uid] = true
	return nil
}

// Posted and refused messages alike are marked seen.
func TestDrainMarksSeen(t *testing.T) {
	f := setup(t)
	mb := &fakeMailbox{seen: map[uint32]bool{}, msgs: map[uint32][]byte{
		1: []byte(f.message(t, "bob@example.test", "posted")),
		2: []byte(f.message(t, "mallory@example.test", "refused")),
		3: nil, // too large
	}}
	if err := f.p.Drain(mb); err != nil {
		t.Fatal(err)
	}
	for uid := range mb.msgs {
		if !mb.seen[uid] {
			t.Errorf("message %d not marked seen", uid)
		}
	}
	if n := len(f.comments(t)); n != 1 {
		t.Fatalf("%d comments", n)
	}
}

// A message that fails for a reason that may pass stays unseen, until it
// has failed maxTries times.
func TestDrainRetriesTransientFailure(t *testing.T) {
	f := setup(t)
	mb := &fakeMailbox{seen: map[uint32]bool{}, msgs: map[uint32][]byte{
		1: []byte(f.message(t, "bob@example.test", "hi")),
	}}
	f.st.SetKeyring(nil) // Handle reads no key: a retry
	for i := 1; i < maxTries; i++ {
		if err := f.p.Drain(mb); err != nil {
			t.Fatal(err)
		}
		if mb.seen[1] {
			t.Fatalf("marked seen after %d tries", i)
		}
	}
	f.p.Drain(mb)
	if !mb.seen[1] {
		t.Fatal("not given up on")
	}
}
