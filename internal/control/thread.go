package control

import (
	"fmt"
	"io"
	"strconv"
	"strings"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// Issues and merge requests share their shape: a numbered thread in a
// repository with an author, comments and participants. What used to be
// two copies of the same code, one per noun, lives here once (#110).

// refArgs resolves "<owner/name> <n>" to the repository and the number.
func refArgs(c *Ctx, args []string, perm func(store.User, store.Repo, string) bool, noun string) (store.Repo, int64, int) {
	if len(args) < 2 {
		return store.Repo{}, 0, c.fail(protocol.ExitUsage, "expected <owner/name> <number>")
	}
	repo, code := resolveRepo(c, args[0], perm)
	if code >= 0 {
		return repo, 0, code
	}
	n, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		return repo, 0, c.fail(protocol.ExitUsage, "bad %s number %q", noun, args[1])
	}
	return repo, n, -1
}

// authorOrWrite admits the thread's author and anyone with write access,
// which is who may change what a thread says or whether it is open.
// -1 means proceed; what names the action in the refusal.
func authorOrWrite(c *Ctx, repo store.Repo, author, what string) int {
	if author == c.User.Username {
		return -1
	}
	grant, err := c.Store.AccessRole(repo.ID, c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	if !policy.CanWrite(c.User, repo, grant) {
		return c.fail(protocol.ExitDenied, "only the author or users with write access can %s", what)
	}
	return -1
}

// commentOut is one comment as show emits it, for both nouns.
type commentOut struct {
	Author     string `json:"author"`
	Body       string `json:"body"`
	BodyFormat string `json:"body_format,omitempty"`
	CreatedAt  string `json:"created_at"`
}

// thread is what a comment command needs to know about its noun.
type thread struct {
	symbol  string // "#" or "!"
	segment string // "issues" or "mrs"
	event   string // issue.commented or mr.commented
}

var (
	issueThread = thread{"#", "issues", "issue.commented"}
	mrThread    = thread{"!", "mrs", "mr.commented"}
)

// runComment is issue comment and mr comment: the noun's resolver hands
// back the thread's id, number and title, and the rest is the same.
func runComment(c *Ctx, args []string, t thread, noun string,
	resolve func(rest []string) (repo store.Repo, id, number int64, title string, code int),
	add func(id, userID int64, body, format string) error,
	participants func(id int64) ([]int64, error),
) int {
	f, err := parseFlags(args, flagSpec{Values: []string{"--message", "--file", "--format"}, MaxPos: -1,
		Usage: noun + " comment <owner/name> <n> [--message <m> | --file -] [--format md|org]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	fmtName, err := markupFormat(f.Value("--format"))
	if err != nil {
		return c.failErr(err)
	}
	if fmtName == "" {
		fmtName = "md"
	}
	repo, id, number, title, code := resolve(f.Pos)
	if code >= 0 {
		return code
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	body, err := bodyFrom(c, f.Value("--message"), f.Value("--file"))
	if err != nil {
		return c.failErr(err)
	}
	if strings.TrimSpace(body) == "" {
		return c.fail(protocol.ExitUsage, "empty comment; use --message or --file -")
	}
	if err := add(id, c.User.ID, body, fmtName); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.RecordEvent(repo.ID, c.User.ID, t.event, fmt.Sprintf(`{"number":%d}`, number))
	if parts, err := participants(id); err == nil {
		subject := fmt.Sprintf("[%s] %s%d: %s", repo.Path(), t.symbol, number, title)
		notifyUsers(c, parts, subject,
			notifyBody(c, fmt.Sprintf("commented on %s%d", t.symbol, number), body, fmt.Sprintf("%s/%s/%d", repo.Path(), t.segment, number)))
	}
	return c.emit(map[string]any{"number": number}, func(w io.Writer) {
		fmt.Fprintf(w, "commented on %s%s%d\n", repo.Path(), t.symbol, number)
	})
}
