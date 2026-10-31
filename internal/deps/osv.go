package deps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"

	"gitbay.org/gitbay/internal/store"
)

// osvAPI is OSV's public API, a fixed host like the registries. What it is
// sent is the resolved set: ecosystem, package name and version.
const osvAPI = "https://api.osv.dev"

// osvEcosystems are OSV's names for the ecosystems read here.
var osvEcosystems = map[string]string{
	EcoGo:    "Go",
	EcoNPM:   "npm",
	EcoCargo: "crates.io",
	EcoPyPI:  "PyPI",
	EcoSwift: "SwiftURL",
}

// osvBatch is the most queries one querybatch request may carry.
const osvBatch = 1000

// osvPages bounds how far a query with more matches than one response holds
// is followed. A package with that many advisories is reported in part.
const osvPages = 5

// maxAdvisories bounds the advisory records fetched for one repository.
const maxAdvisories = 300

type osvQuery struct {
	Package struct {
		Ecosystem string `json:"ecosystem"`
		Name      string `json:"name"`
	} `json:"package"`
	Version   string `json:"version"`
	PageToken string `json:"page_token,omitempty"`
}

// osvVuln is the part of an OSV record the issue reports.
type osvVuln struct {
	ID        string   `json:"id"`
	Summary   string   `json:"summary"`
	Details   string   `json:"details"`
	Aliases   []string `json:"aliases"`
	Withdrawn string   `json:"withdrawn"`
	Affected  []struct {
		Package struct {
			Ecosystem string `json:"ecosystem"`
			Name      string `json:"name"`
		} `json:"package"`
		Ranges []struct {
			Events []struct {
				Fixed string `json:"fixed"`
			} `json:"events"`
		} `json:"ranges"`
	} `json:"affected"`
	DatabaseSpecific struct {
		Severity string `json:"severity"`
	} `json:"database_specific"`
}

// Advisories returns the published advisories matching the resolved set.
// One record often appears under several IDs (a GHSA and a PYSEC for the
// same flaw); each is reported once per dependency, under whichever ID
// sorts first. Withdrawn advisories are left out.
func (c *Client) Advisories(ctx context.Context, found []Dep) ([]store.DepAdvisory, error) {
	ids, err := c.matches(ctx, found)
	if err != nil {
		return nil, err
	}
	records, err := c.records(ctx, ids)
	if err != nil {
		return nil, err
	}
	var out []store.DepAdvisory
	for i, d := range found {
		seen := map[string]bool{}
		for _, id := range ids[i] {
			v, ok := records[id]
			if !ok || seen[id] || v.Withdrawn != "" {
				continue
			}
			seen[id] = true
			for _, alias := range v.Aliases {
				seen[alias] = true
			}
			out = append(out, store.DepAdvisory{
				Ecosystem: d.Ecosystem, Name: d.Name, Version: d.Current, ID: v.ID,
				Summary: summarize(v), Severity: strings.ToLower(v.DatabaseSpecific.Severity),
				Fixed: fixedAfter(v, osvEcosystems[d.Ecosystem], d.Name, d.Current),
			})
		}
	}
	return out, nil
}

// matches asks OSV which advisory IDs affect each dependency, returning
// them sorted, indexed like found.
func (c *Client) matches(ctx context.Context, found []Dep) ([][]string, error) {
	ids := make([][]string, len(found))
	var pending []int
	queries := make([]osvQuery, len(found))
	for i, d := range found {
		queries[i].Package.Ecosystem = osvEcosystems[d.Ecosystem]
		queries[i].Package.Name = d.Name
		queries[i].Version = d.Current
		pending = append(pending, i)
	}
	for page := 0; len(pending) > 0 && page < osvPages; page++ {
		var next []int
		for start := 0; start < len(pending); start += osvBatch {
			chunk := pending[start:min(start+osvBatch, len(pending))]
			batch := make([]osvQuery, len(chunk))
			for j, i := range chunk {
				batch[j] = queries[i]
			}
			results, err := c.queryBatch(ctx, batch)
			if err != nil {
				return nil, err
			}
			for j, i := range chunk {
				if j >= len(results) {
					break
				}
				for _, v := range results[j].Vulns {
					ids[i] = append(ids[i], v.ID)
				}
				if results[j].NextPageToken != "" {
					queries[i].PageToken = results[j].NextPageToken
					next = append(next, i)
				}
			}
		}
		pending = next
	}
	for i := range ids {
		sort.Strings(ids[i])
	}
	return ids, nil
}

type osvResult struct {
	Vulns []struct {
		ID string `json:"id"`
	} `json:"vulns"`
	NextPageToken string `json:"next_page_token"`
}

func (c *Client) queryBatch(ctx context.Context, batch []osvQuery) ([]osvResult, error) {
	payload, err := json.Marshal(map[string]any{"queries": batch})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.osv()+"/v1/querybatch", bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	body, err := c.do(req)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Results []osvResult `json:"results"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, fmt.Errorf("osv querybatch: %w", err)
	}
	return doc.Results, nil
}

// records fetches each distinct advisory once. One that fails to fetch is
// dropped from this sweep, as a failed registry lookup is; all of them
// failing is an error.
func (c *Client) records(ctx context.Context, ids [][]string) (map[string]osvVuln, error) {
	var distinct []string
	seen := map[string]bool{}
	for _, list := range ids {
		for _, id := range list {
			if !seen[id] && len(distinct) < maxAdvisories {
				seen[id] = true
				distinct = append(distinct, id)
			}
		}
	}
	var (
		mu      sync.Mutex
		out     = map[string]osvVuln{}
		failed  []error
		wg      sync.WaitGroup
		tickets = make(chan struct{}, lookups)
	)
	for _, id := range distinct {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			tickets <- struct{}{}
			defer func() { <-tickets }()
			v, err := c.record(ctx, id)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failed = append(failed, err)
				return
			}
			out[id] = v
		}(id)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if len(failed) > 0 && len(failed) == len(distinct) {
		return nil, failed[0]
	}
	return out, nil
}

func (c *Client) record(ctx context.Context, id string) (osvVuln, error) {
	var v osvVuln
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.osv()+"/v1/vulns/"+url.PathEscape(id), nil)
	if err != nil {
		return v, err
	}
	body, err := c.do(req)
	if err != nil {
		return v, err
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return v, fmt.Errorf("osv %s: %w", id, err)
	}
	return v, nil
}

func (c *Client) osv() string {
	if c.OSV != "" {
		return c.OSV
	}
	return osvAPI
}

func (c *Client) do(req *http.Request) ([]byte, error) {
	req.Header.Set("User-Agent", "gitbay/"+c.Version+" (+https://gitbay.org)")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", req.URL, resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, maxBody))
}

// summarize is the record's summary, or the first line of its details when
// it has none, cut to fit a table cell.
func summarize(v osvVuln) string {
	s := strings.TrimSpace(v.Summary)
	if s == "" {
		s, _, _ = strings.Cut(strings.TrimSpace(v.Details), "\n")
	}
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > 120 {
		s = string(r[:119]) + "…"
	}
	return s
}

// fixedAfter is the lowest fixed release above current among the record's
// ranges for this package, or "" when no fix is published. A record can
// carry one range per maintained branch, so the lowest one past current is
// the nearest upgrade that clears it.
func fixedAfter(v osvVuln, ecosystem, name, current string) string {
	best := ""
	for _, a := range v.Affected {
		if a.Package.Ecosystem != ecosystem || a.Package.Name != name {
			continue
		}
		for _, r := range a.Ranges {
			for _, e := range r.Events {
				if e.Fixed == "" || !Newer(current, e.Fixed) {
					continue
				}
				if best == "" || Newer(e.Fixed, best) {
					best = e.Fixed
				}
			}
		}
	}
	return best
}
