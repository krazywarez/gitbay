// Package gitpin runs git against a user-supplied http or https remote
// only at addresses resolved and checked immediately before: mirror
// sync (#279), repo import (#298) and repo import-issues (#301), whose
// API client dials the same way.
package gitpin

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"gitbay.org/gitbay/internal/toolpath"
	"gitbay.org/gitbay/internal/webhook"
)

// Lookup resolves a host to its addresses.
type Lookup func(ctx context.Context, host string) ([]net.IP, error)

// LookupIP is the system resolver.
func LookupIP(ctx context.Context, host string) ([]net.IP, error) {
	return net.DefaultResolver.LookupIP(ctx, "ip", host)
}

// Remote is a URL whose host resolved to IPs, every one of which passed
// the address check.
type Remote struct {
	URL *url.URL
	IPs []net.IP
}

// Resolve parses raw, requires http or https, resolves the host with
// lookup, and refuses it when it resolves to nothing or, unless
// allowLocal, to any private or local address.
func Resolve(ctx context.Context, lookup Lookup, raw string, allowLocal bool) (Remote, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Remote{}, err
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return Remote{}, fmt.Errorf("URL scheme %q is not http or https", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return Remote{}, fmt.Errorf("URL has no host")
	}
	if err := CheckHost(host); err != nil {
		return Remote{}, err
	}
	ips, err := lookup(ctx, host)
	if err != nil {
		return Remote{}, fmt.Errorf("resolving %s: %w", host, err)
	}
	if len(ips) == 0 {
		// An empty resolve list would leave curl to resolve the host itself.
		return Remote{}, fmt.Errorf("%s resolves to no address", host)
	}
	if err := webhook.CheckAddrs(host, ips, allowLocal); err != nil {
		return Remote{}, err
	}
	return Remote{URL: u, IPs: ips}, nil
}

// DialContext connects to r's checked addresses, trying each in turn,
// whatever host addr names; only its port is used. An HTTP client
// built on it must not follow a redirect to another host.
func (r Remote) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: 10 * time.Second}
	for _, ip := range r.IPs {
		var conn net.Conn
		conn, err = d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
	}
	return nil, err
}

// CheckHost refuses a host written as a number in a form other than
// an IP literal: 127.1, 2130706433 and 0x7f.1 are loopback to curl's
// parser but not to Go's, so they are refused rather than left to a
// resolver.
func CheckHost(host string) error {
	if net.ParseIP(host) == nil && numericHost(host) {
		return fmt.Errorf("host %q is a numeric address in a form other than dotted decimal; write it as a.b.c.d", host)
	}
	return nil
}

// numericHost reports whether every label of host is a decimal, octal
// or hex number, the shapes inet_aton reads as an IPv4 address.
func numericHost(host string) bool {
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		digits, base := label, "0123456789"
		if rest, ok := strings.CutPrefix(strings.ToLower(label), "0x"); ok {
			digits, base = rest, "0123456789abcdef"
		}
		if strings.Trim(strings.ToLower(digits), base) != "" || label == "" {
			return false
		}
	}
	return true
}

// Args are git's leading -c options for r: curl's resolve list pins
// the host, and any other name on the same port, to the checked
// addresses, and with redirects off a server
// cannot send git on to a host nobody checked. An address literal
// needs no pin.
func (r Remote) Args() []string {
	args := []string{"-c", "http.followRedirects=false"}
	host := r.URL.Hostname()
	if net.ParseIP(host) != nil {
		return args
	}
	port := r.URL.Port()
	if port == "" {
		port = "443"
		if r.URL.Scheme == "http" {
			port = "80"
		}
	}
	addrs := make([]string, len(r.IPs))
	for i, ip := range r.IPs {
		if ip.To4() == nil {
			addrs[i] = "[" + ip.String() + "]"
		} else {
			addrs[i] = ip.String()
		}
	}
	pinned := port + ":" + strings.Join(addrs, ",")
	// The wildcard entry catches a lookup under any other spelling of
	// the host, so it too lands on the checked addresses.
	return append(args, "-c", "http.curloptResolve="+host+":"+pinned,
		"-c", "http.curloptResolve=*:"+pinned)
}

// Env is git's whole environment for a pinned remote. No system or
// global gitconfig: a proxy, URL rewrite or redirect setting there
// would take git around the pin.
func Env(home string) []string {
	return []string{"GIT_TERMINAL_PROMPT=0", "HOME=" + home,
		"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null"}
}

// VersionOK accepts the output of `git version` for git 2.37 or later,
// the first release with http.curloptResolve. An older git ignores the
// setting and would resolve the host itself.
func VersionOK(out string) error {
	fields := strings.Fields(out)
	if len(fields) >= 3 && fields[0] == "git" && fields[1] == "version" {
		parts := strings.Split(fields[2], ".")
		if len(parts) >= 2 {
			major, err1 := strconv.Atoi(parts[0])
			minor, err2 := strconv.Atoi(parts[1])
			if err1 == nil && err2 == nil {
				if major > 2 || major == 2 && minor >= 37 {
					return nil
				}
				return fmt.Errorf("git %s is older than 2.37 and cannot pin remote addresses", fields[2])
			}
		}
	}
	return fmt.Errorf("cannot read git version from %q", strings.TrimSpace(out))
}

// CheckGit runs the server's git and refuses one that cannot pin.
func CheckGit(ctx context.Context) error {
	out, err := exec.CommandContext(ctx, toolpath.Look("git"), "version").Output()
	if err != nil {
		return fmt.Errorf("running git version: %v", err)
	}
	return VersionOK(string(out))
}
