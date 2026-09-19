package control

import (
	"fmt"
	"io"

	"gitbay.org/gitbay/internal/protocol"
)

func init() {
	register(Command{Path: []string{"web", "theme", "show"},
		Summary:  "the colour scheme the web UI uses for you",
		Usage:    "web theme show",
		ReadOnly: true, Run: runWebThemeShow})
	register(Command{Path: []string{"web", "theme", "set"},
		Summary: "follow the browser's scheme, or force light or dark",
		Usage:   "web theme set system|light|dark", Run: runWebThemeSet})
}

// themes are the values the layout knows how to stamp. system is the
// default and stamps nothing: the stylesheet's media query decides.
var themes = map[string]bool{"system": true, "light": true, "dark": true}

func runWebThemeShow(c *Ctx, args []string) int {
	if len(args) != 0 {
		return c.usage()
	}
	return emitTheme(c)
}

func runWebThemeSet(c *Ctx, args []string) int {
	if len(args) != 1 || !themes[args[0]] {
		return c.usage()
	}
	if err := c.Store.SetTheme(c.User.ID, args[0]); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitTheme(c)
}

func emitTheme(c *Ctx) int {
	theme, err := c.Store.Theme(c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"theme": theme}, func(w io.Writer) {
		fmt.Fprintf(w, "theme: %s\n", theme)
	})
}
