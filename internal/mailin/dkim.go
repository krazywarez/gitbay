package mailin

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/mail"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-msgauth/dkim"
)

const (
	// maxSignatures is how many DKIM-Signature fields are checked; any
	// after them are ignored.
	maxSignatures = 5
	// dnsTimeout bounds one selector key lookup.
	dnsTimeout = 5 * time.Second
	// futureSkew is how far ahead of this clock a signature's t= may be.
	futureSkew   = 15 * time.Minute
	keyCacheTTL  = time.Hour
	keyCacheSize = 256
)

// LookupTXT returns the TXT records at name, one string per record.
type LookupTXT func(ctx context.Context, name string) ([]string, error)

type cachedKey struct {
	txts    []string
	expires time.Time
}

// keyCache holds selector key records that resolved, for keyCacheTTL,
// at most keyCacheSize of them. Failures are not cached.
type keyCache struct {
	mu sync.Mutex
	m  map[string]cachedKey
}

func (c *keyCache) get(name string, now time.Time) ([]string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.m[name]
	if !ok || now.After(e.expires) {
		return nil, false
	}
	return e.txts, true
}

func (c *keyCache) put(name string, txts []string, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]cachedKey{}
	}
	if len(c.m) >= keyCacheSize {
		for k, e := range c.m {
			if now.After(e.expires) {
				delete(c.m, k)
			}
		}
		for k := range c.m {
			if len(c.m) < keyCacheSize {
				break
			}
			delete(c.m, k)
		}
	}
	c.m[name] = cachedKey{txts: txts, expires: now.Add(keyCacheTTL)}
}

// lookupKey is the verifier's TXT lookup: cached, bounded by dnsTimeout.
// The dkim package tells a temporary failure from a permanent one by
// the error implementing net.Error with Temporary true, so every error
// returned is a *net.DNSError, and one that is not a plain "no such
// record" is marked temporary.
func (p *Processor) lookupKey(name string) ([]string, error) {
	now := p.now()
	if txts, ok := p.keys.get(name, now); ok {
		return txts, nil
	}
	lookup := p.LookupTXT
	if lookup == nil {
		lookup = net.DefaultResolver.LookupTXT
	}
	ctx, cancel := context.WithTimeout(context.Background(), dnsTimeout)
	defer cancel()
	txts, err := lookup(ctx, name)
	if err != nil {
		var de *net.DNSError
		if errors.As(err, &de) && de.IsNotFound {
			return nil, &net.DNSError{Err: "no such record", Name: name, IsNotFound: true}
		}
		return nil, &net.DNSError{Err: "lookup failed", Name: name, IsTemporary: true}
	}
	p.keys.put(name, txts, now)
	return txts, nil
}

// dkimVerified checks the DKIM signatures on raw, the message as it
// was fetched. It returns "" when one of the first maxSignatures
// verifies, covers From in h=, has a d= in relaxed alignment with the
// From domain, has not expired and is not dated in the future. retry is
// true when no signature passed and one could not be checked because
// its key lookup failed for a reason that may pass.
func (p *Processor) dkimVerified(raw []byte, h mail.Header, from string) (reason string, retry bool) {
	_, fromDomain, ok := strings.Cut(strings.ToLower(from), "@")
	if !ok || fromDomain == "" {
		return "no From domain", false
	}
	// A second From field could be one the signature does not cover
	// while it is the one read as the sender.
	if len(h["From"]) != 1 {
		return "more than one From field", false
	}
	verifs, err := dkim.VerifyWithOptions(bytes.NewReader(raw), &dkim.VerifyOptions{
		LookupTXT: p.lookupKey, MaxVerifications: maxSignatures})
	if err != nil && !errors.Is(err, dkim.ErrTooManySignatures) {
		return "DKIM: unreadable message", false
	}
	if len(verifs) == 0 {
		return "no DKIM-Signature", false
	}
	now := p.now()
	var fails []string
	for _, v := range verifs {
		d := strings.ToLower(v.Domain)
		why := ""
		switch {
		case dkim.IsTempFail(v.Err):
			retry = true
			why = "key lookup failed"
		case v.Err != nil:
			why = strings.TrimPrefix(v.Err.Error(), "dkim: ")
		case !signsFrom(v.HeaderKeys):
			why = "From field not signed"
		case !v.Expiration.IsZero() && now.After(v.Expiration):
			why = "signature has expired"
		case !v.Time.IsZero() && v.Time.After(now.Add(futureSkew)):
			why = "signature dated in the future"
		case !aligned(d, fromDomain):
			why = "d= not aligned with the From domain"
		default:
			return "", false
		}
		if len(d) > 100 {
			d = d[:100]
		}
		fails = append(fails, "d="+d+": "+why)
	}
	return "DKIM: no passing signature aligned with the From domain (" + strings.Join(fails, "; ") + ")", retry
}

func signsFrom(keys []string) bool {
	for _, k := range keys {
		if strings.EqualFold(k, "from") {
			return true
		}
	}
	return false
}
