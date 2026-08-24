package control

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"profile", "show"},
		Summary: "show a user's or org's profile: profile show [name]", ReadOnly: true, Run: runProfileShow})
	register(Command{Path: []string{"profile", "set"},
		Summary: "set your profile: profile set [--description <d>] [--website <url>] ('' clears)", Run: runProfileSet})
	register(Command{Path: []string{"org", "profile"},
		Summary: "show or set an org's profile: org profile <org> [--description <d>] [--website <url>]", Run: runOrgProfile})
}

// parseProfileFlags pulls --description/--website out of args; a flag given
// with an empty value clears the field, an absent flag leaves it untouched.
func parseProfileFlags(args []string) (rest []string, desc, site *string, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--description", "--website":
			if i+1 >= len(args) {
				return nil, nil, nil, fmt.Errorf("%s requires a value", args[i])
			}
			v := args[i+1]
			if args[i] == "--description" {
				desc = &v
			} else {
				site = &v
			}
			i++
		default:
			rest = append(rest, args[i])
		}
	}
	return rest, desc, site, nil
}

func validateWebsite(url string) error {
	if url == "" || strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		return nil
	}
	return errors.New("website must start with https:// or http://")
}

func applyProfile(p store.Profile, desc, site *string) (store.Profile, error) {
	if desc != nil {
		d, _, _ := strings.Cut(strings.TrimSpace(*desc), "\n")
		if len(d) > 256 {
			d = d[:256]
		}
		p.Description = d
	}
	if site != nil {
		s := strings.TrimSpace(*site)
		if err := validateWebsite(s); err != nil {
			return p, err
		}
		p.Website = s
	}
	return p, nil
}

type profileOut struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Description string `json:"description,omitempty"`
	Website     string `json:"website,omitempty"`
}

func emitProfile(c *Ctx, d profileOut) int {
	return c.emit(d, func(w io.Writer) {
		fmt.Fprintf(w, "%s (%s)\n", d.Name, d.Kind)
		if d.Description != "" {
			fmt.Fprintf(w, "%s\n", d.Description)
		}
		if d.Website != "" {
			fmt.Fprintf(w, "%s\n", d.Website)
		}
	})
}

func runProfileShow(c *Ctx, args []string) int {
	name := c.User.Username
	if len(args) == 1 {
		name = args[0]
	} else if len(args) > 1 {
		return c.fail(protocol.ExitUsage, "usage: profile show [name]")
	}
	kind, id := "", int64(0)
	if u, err := c.Store.UserByUsername(name); err == nil {
		kind, id = "user", u.ID
	} else if o, err := c.Store.OrgByName(name); err == nil {
		kind, id = "org", o.ID
	} else {
		return c.fail(protocol.ExitNotFound, "no user or organization %q", name)
	}
	p, err := c.Store.OwnerProfile(kind, id)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitProfile(c, profileOut{name, kind, p.Description, p.Website})
}

func runProfileSet(c *Ctx, args []string) int {
	rest, desc, site, err := parseProfileFlags(args)
	if err != nil || len(rest) != 0 {
		return c.fail(protocol.ExitUsage, "usage: profile set [--description <d>] [--website <url>]")
	}
	if desc == nil && site == nil {
		return c.fail(protocol.ExitUsage, "nothing to set: pass --description and/or --website")
	}
	p, err := c.Store.OwnerProfile("user", c.User.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	p, err = applyProfile(p, desc, site)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if err := c.Store.SetOwnerProfile("user", c.User.ID, p); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitProfile(c, profileOut{c.User.Username, "user", p.Description, p.Website})
}

func runOrgProfile(c *Ctx, args []string) int {
	rest, desc, site, err := parseProfileFlags(args)
	if err != nil || len(rest) != 1 {
		return c.fail(protocol.ExitUsage, "usage: org profile <org> [--description <d>] [--website <url>]")
	}
	name := rest[0]
	if desc == nil && site == nil {
		return runProfileShow(c, []string{name})
	}
	org, code := orgAdmin(c, name)
	if code >= 0 {
		return code
	}
	p, err := c.Store.OwnerProfile("org", org.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	p, err = applyProfile(p, desc, site)
	if err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	if err := c.Store.SetOwnerProfile("org", org.ID, p); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return emitProfile(c, profileOut{org.Name, "org", p.Description, p.Website})
}
