package control

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"regexp"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/policy"
	"gitbay.org/gitbay/internal/protocol"
	"gitbay.org/gitbay/internal/store"
)

func init() {
	register(Command{Path: []string{"repo", "domain", "add"},
		Summary: "claim a custom pages domain (verify with a DNS TXT record): repo domain add <owner/name> <domain>", Run: runDomainAdd})
	register(Command{Path: []string{"repo", "domain", "verify"},
		Summary: "check the DNS challenge and activate a claim: repo domain verify <owner/name> <domain>", Run: runDomainVerify})
	register(Command{Path: []string{"repo", "domain", "remove"},
		Summary: "remove a custom pages domain: repo domain remove <owner/name> <domain>", Run: runDomainRemove})
	register(Command{Path: []string{"repo", "domain", "list"},
		Summary: "list custom pages domains: repo domain list <owner/name>", ReadOnly: true, Run: runDomainList})
}

// challengeLabel prefixes the domain for the ownership TXT record.
const challengeLabel = "_gitbay-challenge."

// hostnamePat is a conservative DNS hostname: dot-separated labels,
// lowercase, at least two labels.
var hostnamePat = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}$`)

// pendingTTL is how long an unverified claim holds a domain. The env
// override exists for tests.
func pendingTTL() int {
	if v := os.Getenv("GITBAY_DOMAIN_PENDING_TTL"); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return int(d.Seconds())
		}
	}
	return 7 * 24 * 3600
}

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

func challengeRecord(domain, token string) (name, value string) {
	return challengeLabel + domain, "gitbay-domain-verify=" + token
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
	buf := make([]byte, 16)
	rand.Read(buf)
	token := hex.EncodeToString(buf)
	if err := c.Store.AddPageDomain(domain, repo.ID, c.User.ID, token, pendingTTL()); err != nil {
		if errors.Is(err, store.ErrExists) {
			// Not naming the holder: domain claims must not enumerate repos.
			return c.fail(protocol.ExitUsage, "%s is already claimed on this instance", domain)
		}
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	name, value := challengeRecord(domain, token)
	days := pendingTTL() / 86400
	return c.emit(map[string]any{
		"domain": domain, "state": "pending",
		"challenge_name": name, "challenge_value": value,
	}, func(w io.Writer) {
		fmt.Fprintf(w, "%s claimed, pending ownership proof. Create this DNS record:\n\n  %s\tTXT\t%q\n\nthen run: repo domain verify %s %s\nUnverified claims expire after %d days.\n",
			domain, name, value, repo.Path(), domain, days)
	})
}

// lookupTXT resolves the challenge record. GITBAY_DNS_SERVER (host:port)
// overrides the system resolver so tests can answer the challenge.
func lookupTXT(name string) ([]string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r := net.DefaultResolver
	if srv := os.Getenv("GITBAY_DNS_SERVER"); srv != "" {
		r = &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "udp", srv)
		}}
	}
	return r.LookupTXT(ctx, name)
}

func runDomainVerify(c *Ctx, args []string) int {
	if len(args) != 2 {
		return c.fail(protocol.ExitUsage, "usage: repo domain verify <owner/name> <domain>")
	}
	repo, code := resolveRepo(c, args[0], policy.CanAdmin)
	if code >= 0 {
		return code
	}
	domain := strings.ToLower(args[1])
	claim, err := c.Store.PageDomainClaim(domain, repo.ID)
	if err != nil {
		return c.fail(protocol.ExitNotFound, "%s has no claim on %s", repo.Path(), domain)
	}
	if claim.Verified() {
		return c.emit(map[string]string{"domain": domain, "state": "verified"}, func(w io.Writer) {
			fmt.Fprintf(w, "%s is already verified\n", domain)
		})
	}
	if c.Store.PageDomainExpired(claim, pendingTTL()) {
		c.Store.RemovePageDomain(domain, repo.ID)
		return c.fail(protocol.ExitUsage, "the claim on %s expired; run repo domain add again", domain)
	}
	name, want := challengeRecord(domain, claim.Token)
	records, err := lookupTXT(name)
	if err != nil {
		return c.fail(protocol.ExitFailure, "looking up %s: %v", name, err)
	}
	found := false
	for _, r := range records {
		if strings.TrimSpace(r) == want {
			found = true
			break
		}
	}
	if !found {
		return c.fail(protocol.ExitDenied, "%s does not carry the expected record %q", name, want)
	}
	if err := c.Store.VerifyPageDomain(domain, repo.ID); err != nil {
		return c.fail(protocol.ExitFailure, "%v", err)
	}
	c.Store.Audit(c.User.ID, "pages.domain_verified", map[string]any{"repo": repo.ID, "domain": domain})
	return c.emit(map[string]string{"domain": domain, "state": "verified"}, func(w io.Writer) {
		fmt.Fprintf(w, "%s verified — it now serves %s's pages branch; point its A/AAAA records at this server\n", domain, repo.Path())
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
	type out struct {
		Domain   string `json:"domain"`
		State    string `json:"state"`
		Verified string `json:"verified_at,omitempty"`
	}
	var list []out
	for _, d := range ds {
		state := "pending"
		if d.Verified() {
			state = "verified"
		} else if c.Store.PageDomainExpired(d, pendingTTL()) {
			state = "expired"
		}
		list = append(list, out{d.Domain, state, d.VerifiedAt})
	}
	return c.emit(list, func(w io.Writer) {
		for _, d := range list {
			fmt.Fprintf(w, "%s\t%s\n", d.Domain, d.State)
		}
	})
}
