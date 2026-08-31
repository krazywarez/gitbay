package deps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

// Registry endpoints. Each is a fixed public host: unlike webhooks and
// mirrors, no part of the URL comes from user input except the package
// name, and safeName restricts that to characters that need no escaping in
// a URL path.
var endpoints = map[string]string{
	EcoGo:    "https://proxy.golang.org",
	EcoNPM:   "https://registry.npmjs.org",
	EcoCargo: "https://crates.io/api/v1/crates",
	EcoPyPI:  "https://pypi.org/pypi",
}

// maxBody bounds a registry response. The npm packument is the large one,
// which is why the abbreviated metadata is requested.
const maxBody = 4 << 20

// safeName is the shape a package name may have before it goes into a URL.
var safeName = regexp.MustCompile(`^@?[A-Za-z0-9][A-Za-z0-9._/@+-]*$`)

// Client queries package registries. The zero value is not usable; call
// NewClient.
type Client struct {
	HTTP    *http.Client
	Hosts   map[string]string // overridden in tests
	Version string            // reported in User-Agent
}

func NewClient(version string) *Client {
	return &Client{
		HTTP:    &http.Client{Timeout: 20 * time.Second},
		Hosts:   endpoints,
		Version: version,
	}
}

// Latest returns the current release of one package. A prerelease is
// reported as no answer: nothing should be nudged onto an rc.
func (c *Client) Latest(ctx context.Context, eco, name string) (string, error) {
	base, ok := c.Hosts[eco]
	if !ok {
		return "", fmt.Errorf("unknown ecosystem %q", eco)
	}
	if !safeName.MatchString(name) || strings.Contains(name, "..") {
		return "", fmt.Errorf("unusable package name %q", name)
	}
	var path, accept string
	switch eco {
	case EcoGo:
		path = "/" + escapeModule(name) + "/@latest"
	case EcoNPM:
		path = "/" + name
		accept = "application/vnd.npm.install-v1+json" // dist-tags, not the full packument
	case EcoCargo:
		path = "/" + name
	case EcoPyPI:
		path = "/" + name + "/json"
	}
	body, err := c.get(ctx, base+path, accept)
	if err != nil {
		return "", err
	}
	var doc struct {
		Version  string            `json:"Version"`   // go
		DistTags map[string]string `json:"dist-tags"` // npm
		Crate    struct {
			MaxStableVersion string `json:"max_stable_version"`
		} `json:"crate"` // cargo
		Info struct {
			Version string `json:"version"`
		} `json:"info"` // pypi
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return "", fmt.Errorf("%s %s: %w", eco, name, err)
	}
	var latest string
	switch eco {
	case EcoGo:
		latest = doc.Version
	case EcoNPM:
		latest = doc.DistTags["latest"]
	case EcoCargo:
		latest = doc.Crate.MaxStableVersion
	case EcoPyPI:
		latest = doc.Info.Version
	}
	if latest == "" || IsPrerelease(latest) {
		return "", nil
	}
	return latest, nil
}

func (c *Client) get(ctx context.Context, u, accept string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "gitbay/"+c.Version+" (+https://gitbay.org)")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}

// escapeModule applies the module proxy's case encoding: an uppercase
// letter becomes "!" followed by its lowercase form, so paths stay distinct
// on case-insensitive filesystems.
func escapeModule(path string) string {
	var b strings.Builder
	for _, r := range path {
		if r >= 'A' && r <= 'Z' {
			b.WriteByte('!')
			r += 'a' - 'A'
		}
		b.WriteRune(r)
	}
	return b.String()
}
