// Package mailin turns replies to notification mail into comments
// (#295). A poller reads a mailbox over IMAP; each unseen message
// addressed to reply+<token>@<domain> is checked (token, account, sender
// address, the account's access to the thread now) and posted by
// dispatching issue comment or mr comment as that account. A refusal
// sends nothing back and is written to the audit log with its reason,
// never the message's content. Every message is marked seen once it is
// handled, posted or refused; only a failure that may pass (the
// database busy) leaves it for the next poll.
package mailin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/textproto"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/config"
	"gitbay.org/gitbay/internal/control"
	"gitbay.org/gitbay/internal/imapc"
	"gitbay.org/gitbay/internal/mailreply"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// Mailbox is what the processor needs of an IMAP session; imapc.Client
// implements it, and tests use a fake.
type Mailbox interface {
	Unseen() ([]uint32, error)
	Fetch(uid uint32) ([]byte, error)
	MarkSeen(uid uint32) error
}

// maxTries is how many polls a message that keeps failing is tried in
// before it is marked seen and given up on.
const maxTries = 5

// Processor handles fetched messages.
type Processor struct {
	St  *store.Store
	Cfg config.Config
	Now func() time.Time
	// LookupTXT resolves DKIM selector keys; nil is the system
	// resolver.
	LookupTXT LookupTXT

	keys  keyCache
	tries map[uint32]int
	// A window of refusal rows, bounded because anyone can send mail
	// to the mailbox.
	windowStart time.Time
	windowRows  int
}

// refusalsPerMinute bounds the audit rows refusals write.
const refusalsPerMinute = 60

// Result is what became of one message.
type Result struct {
	Posted bool
	Retry  bool   // a failure that may pass; the message is left unseen
	Reason string // why it was refused or failed; empty when posted
}

func refused(format string, args ...any) Result {
	return Result{Reason: fmt.Sprintf(format, args...)}
}

// Drain handles every unseen message in mb. It stops at the first
// mailbox error.
func (p *Processor) Drain(mb Mailbox) error {
	if p.tries == nil {
		p.tries = map[uint32]int{}
	}
	uids, err := mb.Unseen()
	if err != nil {
		return err
	}
	for _, uid := range uids {
		// A message that failed in earlier polls before it could be
		// handled (a fetch the server cut off) is given up on unread.
		if p.tries[uid] >= maxTries {
			p.audit(0, "", "gave up after "+strconv.Itoa(maxTries)+" tries")
			delete(p.tries, uid)
			if err := mb.MarkSeen(uid); err != nil {
				return err
			}
			continue
		}
		raw, err := mb.Fetch(uid)
		var res Result
		switch {
		case errors.Is(err, imapc.ErrTooLarge):
			res = refused("message larger than %d bytes", imapc.MaxMessage)
			p.audit(0, "", res.Reason)
		case errors.Is(err, imapc.ErrLimit):
			// The server sent more than the limits allow for this
			// message: it counts a try, and the closed connection ends
			// the poll.
			p.tries[uid]++
			return err
		case errors.As(err, new(*imapc.RefusedError)):
			// The server refused this message; the session goes on.
			res = Result{Retry: true, Reason: "fetch: " + err.Error()}
		case err != nil:
			// A connection failure or a timeout says nothing about this
			// message or the ones after it: the poll ends, no try is
			// counted.
			return err
		default:
			res = p.Handle(raw)
		}
		if res.Retry {
			p.tries[uid]++
			if p.tries[uid] < maxTries {
				slog.Warn("mail reply: will retry", "uid", uid, "err", res.Reason)
				continue
			}
			p.audit(0, "", "gave up after "+strconv.Itoa(maxTries)+" tries: "+res.Reason)
		}
		delete(p.tries, uid)
		if err := mb.MarkSeen(uid); err != nil {
			return err
		}
	}
	return nil
}

// Handle checks one message and posts it when every check passes.
// Refusals are audited here.
func (p *Processor) Handle(raw []byte) Result {
	if len(bytes.TrimSpace(raw)) == 0 {
		return p.refuse(0, "", "empty message")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return p.refuse(0, "", "unreadable message")
	}
	msgID := strings.TrimSpace(msg.Header.Get("Message-Id"))
	if len(msgID) > 200 {
		msgID = msgID[:200]
	}
	if automatic(msg.Header) {
		return p.refuse(0, msgID, "automatic reply")
	}
	in := p.Cfg.Mail.Inbound
	token := findToken(msg.Header, in.ReplyAddress)
	if token == "" {
		return p.refuse(0, msgID, "not addressed to a reply address")
	}
	keys := p.St.Keyring()
	if keys == nil {
		return Result{Retry: true, Reason: "no secret key loaded"}
	}
	secrets, err := keys.Derive(mailreply.Purpose)
	if err != nil {
		return Result{Retry: true, Reason: "secret key: " + err.Error()}
	}
	target, err := mailreply.Verify(secrets, token, p.now())
	switch {
	case errors.Is(err, mailreply.ErrExpired):
		return p.refuse(target.UserID, msgID, "reply token expired")
	case err != nil:
		return p.refuse(0, msgID, err.Error())
	}

	u, err := p.St.UserByID(target.UserID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return p.refuse(0, msgID, "account no longer exists")
	case err != nil:
		return Result{Retry: true, Reason: err.Error()}
	case u.Disabled:
		return p.refuse(u.ID, msgID, "account disabled")
	case u.Pending:
		return p.refuse(u.ID, msgID, "account not active")
	}
	// Ids are reused after a hard delete: an account created after the
	// token was minted is not the one it named.
	if code := p.createdAfter("users", u.ID, target, msgID, "account"); code != nil {
		return *code
	}
	// The token alone is not enough: the reply must come from one of
	// the account's verified addresses.
	from, err := msg.Header.AddressList("From")
	if err != nil || len(from) != 1 {
		return p.refuse(u.ID, msgID, "no single From address")
	}
	ok, err := p.St.VerifiedEmailOf(u.ID, from[0].Address)
	if err != nil {
		return Result{Retry: true, Reason: err.Error()}
	}
	if !ok {
		return p.refuse(u.ID, msgID, "From is not a verified address of the account")
	}
	if res := p.authenticate(raw, msg.Header, from[0].Address); res != nil {
		if res.Retry {
			return *res
		}
		return p.refuse(u.ID, msgID, res.Reason)
	}
	if on, err := p.St.ReplyEnabled(u.ID); err != nil {
		return Result{Retry: true, Reason: err.Error()}
	} else if !on {
		return p.refuse(u.ID, msgID, "reply by mail is off for the account")
	}

	text, err := textBody(textproto.MIMEHeader(msg.Header), msg.Body, control.MaxCommentBytes)
	switch {
	case errors.Is(err, errNoText):
		return p.refuse(u.ID, msgID, "no text/plain part")
	case errors.Is(err, errTooLong):
		return p.refuse(u.ID, msgID, "reply too long")
	case err != nil:
		return p.refuse(u.ID, msgID, "unreadable body")
	}
	text = stripQuoted(text)
	if text == "" {
		return p.refuse(u.ID, msgID, "empty reply")
	}
	if len(text) > control.MaxCommentBytes {
		return p.refuse(u.ID, msgID, "reply too long")
	}

	repo, err := p.St.RepoByID(target.RepoID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return p.refuse(u.ID, msgID, "repository no longer exists")
	case err != nil:
		return Result{Retry: true, Reason: err.Error()}
	}
	if code := p.createdAfter("repos", repo.ID, target, msgID, "repository"); code != nil {
		return *code
	}

	// The claim names the thread and the account as well as the
	// message, so one account's Message-ID cannot suppress another's.
	id := msgID
	if id == "" {
		sum := sha256.Sum256(raw)
		id = "sha256:" + hex.EncodeToString(sum[:])
	}
	key := fmt.Sprintf("%d/%s/%d/%d/%s", u.ID, target.Kind, target.RepoID, target.Number, id)
	claimed, err := p.St.ClaimMailReply(key)
	if err != nil {
		return Result{Retry: true, Reason: err.Error()}
	}
	if !claimed {
		return p.refuse(u.ID, msgID, "already posted")
	}

	var stdout, stderr bytes.Buffer
	c := &control.Ctx{User: u, Scope: "full", Store: p.St, Cfg: p.Cfg,
		Stdin: strings.NewReader(text), Stdout: &stdout, Stderr: &stderr,
		Source: control.SourceMail}
	code := control.Dispatch(c, []string{target.Kind, "comment", repo.Path(),
		strconv.FormatInt(target.Number, 10), "--file", "-"})
	if code == protocol.ExitOK {
		return Result{Posted: true}
	}
	p.St.ReleaseMailReply(key)
	reason := strings.TrimSpace(stderr.String())
	if code == protocol.ExitFailure {
		return Result{Retry: true, Reason: reason}
	}
	// Dispatch has audited a denied or not-found refusal already; this
	// row says it came by mail and why.
	return p.refuse(u.ID, msgID, "comment refused: "+reason)
}

// authenticate checks that the mail host or the sender's domain vouches
// for From: an Authentication-Results pass from trusted_authserv_id, or
// a DKIM signature that verifies here with require_dkim. When both are
// set either is enough. It returns nil when From is authenticated or
// neither is set, and the unaudited refusal or retry otherwise.
func (p *Processor) authenticate(raw []byte, h mail.Header, from string) *Result {
	in := p.Cfg.Mail.Inbound
	var reasons []string
	if id := in.TrustedAuthservID; id != "" {
		reason := authenticated(h, id, from)
		if reason == "" {
			return nil
		}
		reasons = append(reasons, reason)
	}
	if in.RequireDKIM {
		reason, retry := p.dkimVerified(raw, h, from)
		if reason == "" {
			return nil
		}
		if retry {
			return &Result{Retry: true, Reason: reason}
		}
		reasons = append(reasons, reason)
	}
	if len(reasons) == 0 {
		return nil
	}
	return &Result{Reason: strings.Join(reasons, "; ")}
}

// createdAfter refuses when the row was created after the token was
// minted: a later account or repository that took a freed id. Created
// times are compared to the second, the token's precision.
func (p *Processor) createdAfter(table string, id int64, target mailreply.Target, msgID, what string) *Result {
	created, err := p.St.CreatedAt(table, id)
	if err != nil {
		return &Result{Retry: true, Reason: err.Error()}
	}
	if created.Truncate(time.Second).After(target.Issued()) {
		r := p.refuse(0, msgID, what+" created after the reply token was issued")
		return &r
	}
	return nil
}

func (p *Processor) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

func (p *Processor) refuse(actor int64, msgID, reason string) Result {
	p.audit(actor, msgID, reason)
	return Result{Reason: reason}
}

// audit records a refusal: the reason and the Message-ID, never content
// from the message. Past refusalsPerMinute rows in a minute, the rest of
// that minute's refusals are counted in one row, written with the first
// refusal after it.
func (p *Processor) audit(actor int64, msgID, reason string) {
	now := p.now()
	if now.Sub(p.windowStart) >= time.Minute {
		if over := p.windowRows - refusalsPerMinute; over > 0 {
			p.St.Audit(0, "refused mail reply", map[string]any{"reason": "throttled", "dropped": over, "source": control.SourceMail})
		}
		p.windowStart, p.windowRows = now, 0
	}
	p.windowRows++
	if p.windowRows > refusalsPerMinute {
		return
	}
	data := map[string]any{"reason": reason, "source": control.SourceMail}
	if msgID != "" {
		data["message_id"] = msgID
	}
	p.St.Audit(actor, "refused mail reply", data)
}

// automatic reports an auto-responder's message (RFC 3834, and the
// headers older responders use), which must not post a comment.
func automatic(h mail.Header) bool {
	if v := strings.ToLower(strings.TrimSpace(h.Get("Auto-Submitted"))); v != "" && v != "no" {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(h.Get("Precedence"))) {
	case "bulk", "junk", "list", "auto_reply":
		return true
	}
	return h.Get("X-Autoreply") != "" || h.Get("X-Autorespond") != ""
}

// findToken returns the reply token from the first recipient header
// that carries one.
func findToken(h mail.Header, base string) string {
	for _, name := range []string{"Delivered-To", "X-Original-To", "Envelope-To", "To", "Cc"} {
		for _, v := range h[textproto.CanonicalMIMEHeaderKey(name)] {
			addrs, err := mail.ParseAddressList(v)
			if err != nil {
				continue
			}
			for _, a := range addrs {
				if tok, ok := mailreply.TokenFrom(base, a.Address); ok {
					return tok
				}
			}
		}
	}
	return ""
}

// Poller reads the configured mailbox every poll interval.
type Poller struct {
	P  *Processor
	In config.MailInbound
}

// Run polls until ctx is done.
func (pl *Poller) Run(ctx context.Context) {
	t := time.NewTicker(pl.In.Poll())
	defer t.Stop()
	for {
		if err := pl.Once(); err != nil {
			// The error names the server and the IMAP failure; the
			// password never reaches it (imapc.Client.Login).
			slog.Warn("mail reply: poll failed", "server", pl.In.Addr(), "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Once connects, handles what is waiting, and disconnects.
func (pl *Poller) Once() error {
	c, _, err := imapc.Open(pl.In, false, time.Minute)
	if err != nil {
		return err
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(10 * time.Minute))
	if err := pl.P.Drain(c); err != nil {
		return err
	}
	return pl.P.St.PruneMailReplies(pl.P.now().Add(-mailreply.Lifetime - 24*time.Hour))
}
