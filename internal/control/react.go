package control

import (
	"fmt"
	"io"
	"strconv"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

// Reactions on issues, merge requests and their comments (#291). A
// reaction is not an event: it files no notification, no activity row
// and no webhook.

// ReactionOut is one reaction on one item as show emits it.
type ReactionOut struct {
	Reaction string `json:"reaction"`
	Count    int    `json:"count"`
	Me       bool   `json:"me"`
}

// Reacted is what issue react and mr react emit.
type Reacted struct {
	Reaction string `json:"reaction"`
	Comment  int64  `json:"comment,omitempty"`
	Removed  bool   `json:"removed,omitempty"`
}

func reactionsOut(cs []store.ReactionCount) []ReactionOut {
	var out []ReactionOut
	for _, rc := range cs {
		out = append(out, ReactionOut{rc.Reaction, rc.Count, rc.Me})
	}
	return out
}

func init() {
	register(Command{Path: []string{"issue", "react"},
		Summary: "react to an issue or one of its comments",
		Usage:   "issue react <owner/name> <n> [--comment <id>] <reaction> [--remove]",
		Flags: []Flag{
			{"--comment", "<id>", "react to this comment instead of the issue", ""},
			{"--remove", "", "take the reaction back", ""},
		},
		Examples: []string{
			"issue react krz/gitbay 42 +1",
			"issue react krz/gitbay 42 --comment 7 hooray",
			"issue react krz/gitbay 42 --remove +1",
		},
		Run: func(c *Ctx, args []string) int { return runReact(c, args, "issue") }})
	register(Command{Path: []string{"mr", "react"},
		Summary: "react to a merge request or one of its comments",
		Usage:   "mr react <owner/name> <n> [--comment <id>] <reaction> [--remove]",
		Flags: []Flag{
			{"--comment", "<id>", "react to this comment instead of the merge request", ""},
			{"--remove", "", "take the reaction back", ""},
		},
		Examples: []string{
			"mr react krz/gitbay 431 rocket",
			"mr react krz/gitbay 431 --comment 7 eyes",
		},
		Run: func(c *Ctx, args []string) int { return runReact(c, args, "mr") }})
}

// runReact is issue react and mr react. Whoever may comment may react,
// so it asks what comment asks: read access, and a repository that is
// not archived.
func runReact(c *Ctx, args []string, noun string) int {
	f, err := c.parseArgs(args, flagSpec{Values: []string{"--comment"}, Bools: []string{"--remove"}, MaxPos: 3,
		Usage: noun + " react <owner/name> <n> [--comment <id>] <reaction> [--remove]"})
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if len(f.Pos) != 3 {
		return c.usage()
	}
	var (
		repo     store.Repo
		threadID int64
		number   int64
		code     int
		sym      string
	)
	if noun == "issue" {
		var iss store.Issue
		repo, iss, code = issueRef(c, f.Pos[:2], policy.CanRead)
		threadID, number, sym = iss.ID, iss.Number, "#"
	} else {
		var mr store.MR
		repo, mr, code = mrRef(c, f.Pos[:2], policy.CanRead)
		threadID, number, sym = mr.ID, mr.Number, "!"
	}
	if code >= 0 {
		return code
	}
	reaction, ok := store.ParseReaction(f.Pos[2])
	if !ok {
		return c.fail(protocol.ExitUsage, "unknown reaction %q; one of %s", f.Pos[2], reactionNames())
	}
	var commentID int64
	if f.Has("--comment") {
		commentID, err = strconv.ParseInt(f.Value("--comment"), 10, 64)
		if err != nil || commentID <= 0 {
			return c.fail(protocol.ExitUsage, "bad comment id %q", f.Value("--comment"))
		}
	}
	if code := refuseArchived(c, repo); code >= 0 {
		return code
	}
	if err := c.Store.ReactionTarget(noun, threadID, commentID); err != nil {
		if err == store.ErrNotFound {
			return c.fail(protocol.ExitNotFound, "comment %d not found on %s%d", commentID, sym, number)
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	remove := f.Has("--remove")
	if err := c.Store.SetReaction(noun, threadID, commentID, c.User.ID, reaction, !remove); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(Reacted{Reaction: reaction, Comment: commentID, Removed: remove}, func(w io.Writer) {
		what := fmt.Sprintf("%s%s%d", repo.Path(), sym, number)
		if commentID != 0 {
			what += fmt.Sprintf(" comment %d", commentID)
		}
		verb := "reacted"
		if remove {
			verb = "removed reaction"
		}
		fmt.Fprintf(w, "%s %s on %s\n", verb, store.ReactionEmoji(reaction), what)
	})
}

func reactionNames() string {
	s := ""
	for i, r := range store.Reactions {
		if i > 0 {
			s += " "
		}
		s += r.Name
	}
	return s
}
