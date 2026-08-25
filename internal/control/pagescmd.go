package control

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"repo", "domain", "add"},
		Summary: "serve pages on a custom domain: repo domain add <owner/name> <domain>", Run: runDomainAdd})
	register(Command{Path: []string{"repo", "domain", "remove"},
		Summary: "remove a custom pages domain: repo domain remove <owner/name> <domain>", Run: runDomainRemove})
	register(Command{Path: []string{"repo", "domain", "list"},
		Summary: "list custom pages domains: repo domain list <owner/name>", ReadOnly: true, Run: runDomainList})
}

// hostnamePat is a conservative DNS hostname: dot-separated labels,
// lowercase, at least two labels.
var hostnamePat = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

func validatePageDomain(c *Ctx, domain string) error {
	if !hostnamePat.MatchString(domain) {
		return fmt.Errorf("invalid domain %q: lowercase hostname like docs.example.org", domain)
	}
	if domain == c.Cfg.SiteHost() || strings.HasSuffix(c.Cfg.SiteHost(), "."+domain) {
		return errors.New("that is the forge's own host: pages content must stay off its origin")
	}
	if pd := c.Cfg.Pages.Domain; pd != "" && (domain == pd || strings.HasSuffix(domain, "."+pd)) {
		return fmt.Errorf("%s is under the built-in pages domain; it is served automatically", domain)
	}
	return nil
}

func runDomainAdd(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: repo domain add <owner/name> <domain>")
	}
	domain := strings.ToLower(args[1])
	if err := validatePageDomain(c, domain); err != nil {
		return c.fail(protocol.ExitUsage, "%v", err)
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	if repo.Visibility != "public" {
		return c.fail(protocol.ExitUsage, "pages serve public repositories only; %s is private", repo.Path())
	}
	if err := c.Store.AddPageDomain(domain, repo.ID); err != nil {
		if errors.Is(err, store.ErrExists) {
			// Not naming the holder: domain claims must not enumerate repos.
			return c.fail(protocol.ExitUsage, "%s is already claimed on this instance", domain)
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"domain": domain}, func(w io.Writer) {
		fmt.Fprintf(w, "%s now serves %s's pages branch — point its DNS (A/AAAA) at this server\n", domain, repo.Path())
	})
}

func runDomainRemove(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: repo domain remove <owner/name> <domain>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	domain := strings.ToLower(args[1])
	if err := c.Store.RemovePageDomain(domain, repo.ID); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return c.fail(protocol.ExitNotFound, "%s is not a domain of %s", domain, repo.Path())
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(map[string]string{"removed": domain}, func(w io.Writer) {
		fmt.Fprintf(w, "removed %s\n", domain)
	})
}

func runDomainList(c *Ctx, args []string) int {
	if len(args) != 1 {
		return c.fail(protocol.ExitUsage, "usage: repo domain list <owner/name>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanRead)
	if code >= 0 {
		return code
	}
	ds, err := c.Store.ListPageDomains(repo.ID)
	if err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	return c.emit(ds, func(w io.Writer) {
		for _, d := range ds {
			fmt.Fprintln(w, d)
		}
	})
}
