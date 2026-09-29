package control

import (
	"fmt"
	"io"

	"gitbay.org/gitbay/internal/protocol"
)

func init() {
	register(Command{Path: []string{"web", "diff", "show"},
		Summary:  "the diff layout the web UI uses for you",
		Usage:    "web diff show",
		Examples: []string{"web diff show"},
		ReadOnly: true, Run: runWebDiffShow})
	register(Command{Path: []string{"web", "diff", "set"},
		Summary:  "draw web diffs unified or side by side",
		Usage:    "web diff set unified|split",
		Examples: []string{"web diff set split"}, Run: runWebDiffSet})
}

var diffLayouts = map[string]bool{"unified": true, "split": true}

func runWebDiffShow(c *Ctx, args []string) int {
	if len(args) != 0 {
		return c.usage()
	}
	return emitDiffLayout(c)
}

func runWebDiffSet(c *Ctx, args []string) int {
	if len(args) != 1 || !diffLayouts[args[0]] {
		return c.usage()
	}
	if err := c.Store.SetDiffLayout(c.User.ID, args[0]); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitDiffLayout(c)
}

func emitDiffLayout(c *Ctx) int {
	layout, err := c.Store.DiffLayout(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"layout": layout}, func(w io.Writer) {
		fmt.Fprintf(w, "layout: %s\n", layout)
	})
}
