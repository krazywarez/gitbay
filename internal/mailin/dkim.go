package mailin

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
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
	futureSkew = 15 * time.Minute
	// keyCacheTTL is how long a key record is reused. The resolver API
	// does not report the record's TTL, so this is short: a revoked key
	// is still honoured for up to this long.
	keyCacheTTL  = 15 * time.Minute
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
// was fetched. A signature passes when it is one of the first
// maxSignatures, verifies, has a d= in relaxed alignment with the From
// domain, has not expired, is not dated in the future, and its h=
// covers From, tokenField (the To or Cc the reply address was read
// from), Content-Type, and Message-ID when the message has one. An
// unsigned Content-Transfer-Encoding is accepted only when it is an
// identity encoding (7bit, 8bit, binary), which does not change what
// the body decodes to; mail clients commonly leave it out of h=. It returns an id for each passing
// signature (a hash of its b=), or the refusal's reason. retry is true
// when none passed and one could not be checked because its key lookup
// failed for a reason that may pass.
func (p *Processor) dkimVerified(raw []byte, rh rawHeader, from, tokenField string) (ids []string, reason string, retry bool) {
	fromDomain := ""
	if i := strings.LastIndex(from, "@"); i >= 0 {
		fromDomain = strings.ToLower(from[i+1:])
	}
	if fromDomain == "" {
		return nil, "no From domain", false
	}
	need := []string{"from", tokenField, "content-type"}
	if rh.count["message-id"] > 0 {
		need = append(need, "message-id")
	}
	cteOK := true
	switch rh.cte {
	case "7bit", "8bit", "binary":
	default:
		cteOK = rh.count["content-transfer-encoding"] == 0
	}
	verifs, err := dkim.VerifyWithOptions(bytes.NewReader(raw), &dkim.VerifyOptions{
		LookupTXT: p.lookupKey, MaxVerifications: maxSignatures})
	if err != nil && !errors.Is(err, dkim.ErrTooManySignatures) {
		return nil, "DKIM: unreadable message", false
	}
	if len(verifs) == 0 {
		return nil, "no DKIM-Signature", false
	}
	now := p.now()
	var fails []string
	for i, v := range verifs {
		d := strings.ToLower(v.Domain)
		why := ""
		switch {
		case dkim.IsTempFail(v.Err):
			retry = true
			why = "key lookup failed"
		case v.Err != nil:
			why = strings.TrimPrefix(v.Err.Error(), "dkim: ")
		case !v.Expiration.IsZero() && now.After(v.Expiration):
			why = "signature has expired"
		case !v.Time.IsZero() && v.Time.After(now.Add(futureSkew)):
			why = "signature dated in the future"
		case !aligned(d, fromDomain):
			why = "d= not aligned with the From domain"
		default:
			if n := unsigned(v.HeaderKeys, need); n != "" {
				why = n + " not in h="
			} else if !cteOK && unsigned(v.HeaderKeys, []string{"content-transfer-encoding"}) != "" {
				why = "content-transfer-encoding not in h= and not 7bit, 8bit or binary"
			} else if i < len(rh.dkimB) && rh.dkimB[i] != "" {
				sum := sha256.Sum256([]byte(rh.dkimB[i]))
				ids = append(ids, "dkim:"+hex.EncodeToString(sum[:]))
				continue
			} else {
				why = "no b= tag"
			}
		}
		if len(d) > 100 {
			d = d[:100]
		}
		fails = append(fails, "d="+d+": "+why)
	}
	if len(ids) > 0 {
		return ids, "", false
	}
	return nil, "DKIM: no passing signature aligned with the From domain (" + strings.Join(fails, "; ") + ")", retry
}

// unsigned returns the first of need that keys (a signature's h=) does
// not list, or "".
func unsigned(keys, need []string) string {
	for _, n := range need {
		found := false
		for _, k := range keys {
			if strings.EqualFold(k, n) {
				found = true
				break
			}
		}
		if !found {
			return n
		}
	}
	return ""
}
