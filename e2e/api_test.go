package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// apiCall posts one command to the JSON API.
func (i *instance) apiCall(t *testing.T, token string, argv []string, stdin string) (int, map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"argv": argv, "stdin": stdin})
	req, err := http.NewRequest("POST",
		fmt.Sprintf("http://127.0.0.1:%d/api/v1/cmd", i.httpPort), bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("API response not JSON (%d): %s", resp.StatusCode, raw)
	}
	return resp.StatusCode, out
}

func TestJSONAPI(t *testing.T) {
	inst := startInstanceWith(t, "[api]\nenabled = true\n")
	aliceKey := inst.newKey(t, "alice")
	inst.admin(t, "admin", "user", "create", "alice",
		"--key", aliceKey+".pub", "--email", "alice@example.test", "--verified")

	// Tokens are minted over SSH, shown once.
	out, errOut, code := inst.ssh(t, aliceKey, "", "token", "create", "--name", "ci", "--json")
	if code != 0 {
		t.Fatalf("token create: %s", errOut)
	}
	var env struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || !strings.HasPrefix(env.Data.Token, "gb_") {
		t.Fatalf("token create output: %v %s", err, out)
	}
	token := env.Data.Token

	// Auth failures are uniform 401s.
	if status, _ := inst.apiCall(t, "", []string{"whoami"}, ""); status != 401 {
		t.Fatalf("no token: %d", status)
	}
	if status, _ := inst.apiCall(t, "gb_wrong", []string{"whoami"}, ""); status != 401 {
		t.Fatalf("bad token: %d", status)
	}

	// whoami through the API: same envelope, exit_code injected.
	status, body := inst.apiCall(t, token, []string{"whoami"}, "")
	if status != 200 || body["exit_code"].(float64) != 0 {
		t.Fatalf("whoami: %d %v", status, body)
	}
	if data := body["data"].(map[string]any); data["username"] != "alice" {
		t.Fatalf("whoami data: %v", body)
	}

	// Mutations work: create a repo and an issue, then read it back.
	if status, body = inst.apiCall(t, token, []string{"repo", "create", "alice/proj", "--private"}, ""); status != 200 {
		t.Fatalf("repo create: %d %v", status, body)
	}
	if status, _ = inst.apiCall(t, token, []string{"issue", "create", "alice/proj", "--title", "from the api", "--file", "-"}, "body via stdin\n"); status != 200 {
		t.Fatal("issue create failed")
	}
	status, body = inst.apiCall(t, token, []string{"issue", "show", "alice/proj", "1"}, "")
	data := body["data"].(map[string]any)
	if status != 200 || data["title"] != "from the api" || data["body"] != "body via stdin\n" {
		t.Fatalf("issue show: %d %v", status, body)
	}

	// Exit codes map to HTTP statuses.
	if status, _ = inst.apiCall(t, token, []string{"issue", "show", "alice/proj", "99"}, ""); status != 404 {
		t.Fatalf("missing issue: %d", status)
	}
	if status, _ = inst.apiCall(t, token, []string{"nonsense"}, ""); status != 400 {
		t.Fatalf("unknown command: %d", status)
	}

	// Raw-output commands (no envelope) are wrapped. Reading a file is the
	// cheapest raw output, so give the repo one commit to read from.
	work := t.TempDir()
	aliceEnv := inst.gitEnv(aliceKey)
	mustGit(t, work, aliceEnv, "clone", inst.sshURL("alice/proj"), "proj")
	dir := filepath.Join(work, "proj")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("plain text, not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustGit(t, dir, aliceEnv, "checkout", "-q", "-b", "main")
	mustGit(t, dir, aliceEnv, "add", "README")
	mustGit(t, dir, aliceEnv, "commit", "-q", "-m", "init")
	mustGit(t, dir, aliceEnv, "push", "-q", "origin", "main")

	// A tarball is not an envelope, so it comes back under "output".
	status, body = inst.apiCall(t, token, []string{"repo", "download", "alice/proj"}, "")
	raw, isRaw := body["output"].(string)
	if status != 200 || !isRaw || raw == "" {
		t.Fatalf("repo download via API: %d %v", status, body)
	}

	// help answers with the registry as data, so a consumer can read one
	// command's arguments without scraping the whole listing.
	status, body = inst.apiCall(t, token, []string{"help", "issue", "create"}, "")
	if status != 200 {
		t.Fatalf("help via API: %d %v", status, body)
	}
	rows, ok := body["data"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("help issue create: %v", body)
	}
	row := rows[0].(map[string]any)
	if row["path"] != "issue create" || !strings.Contains(row["usage"].(string), "--title") {
		t.Fatalf("help issue create row: %v", row)
	}

	// Git transport is refused by name.
	if status, _ = inst.apiCall(t, token, []string{"git-upload-pack", "alice/proj"}, ""); status != 400 {
		t.Fatalf("git over API: %d", status)
	}

	// Token management never works over the API: no credential minting.
	status, body = inst.apiCall(t, token, []string{"token", "create", "--name", "sneaky"}, "")
	if status != 403 || !strings.Contains(body["error"].(string), "only available over SSH") {
		t.Fatalf("token create via API: %d %v", status, body)
	}

	// Read-scoped tokens read but never write.
	out, _, code = inst.ssh(t, aliceKey, "", "token", "create", "--name", "reader", "--scope", "read", "--json")
	if code != 0 {
		t.Fatal("read token create failed")
	}
	json.Unmarshal([]byte(out), &env)
	readToken := env.Data.Token
	if status, _ = inst.apiCall(t, readToken, []string{"issue", "list", "alice/proj"}, ""); status != 200 {
		t.Fatalf("read token list: %d", status)
	}
	status, body = inst.apiCall(t, readToken, []string{"issue", "close", "alice/proj", "1"}, "")
	if status != 403 || !strings.Contains(body["error"].(string), "read-only") {
		t.Fatalf("read token write: %d %v", status, body)
	}

	// Expiry: a 1-second token dies.
	out, _, _ = inst.ssh(t, aliceKey, "", "token", "create", "--name", "brief", "--ttl", "1s", "--json")
	json.Unmarshal([]byte(out), &env)
	brief := env.Data.Token
	if status, _ = inst.apiCall(t, brief, []string{"whoami"}, ""); status != 200 {
		t.Fatal("fresh short-ttl token rejected")
	}
	time.Sleep(1100 * time.Millisecond)
	if status, _ = inst.apiCall(t, brief, []string{"whoami"}, ""); status != 401 {
		t.Fatal("expired token accepted")
	}

	// Revocation kills a token immediately.
	if _, _, code = inst.ssh(t, aliceKey, "", "token", "revoke", "ci"); code != 0 {
		t.Fatal("revoke failed")
	}
	if status, _ = inst.apiCall(t, token, []string{"whoami"}, ""); status != 401 {
		t.Fatal("revoked token accepted")
	}

	// With [api] disabled (the default), the endpoint does not exist.
	inst2 := startInstance(t)
	req, _ := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/v1/cmd", inst2.httpPort),
		strings.NewReader(`{"argv":["whoami"]}`))
	req.Header.Set("Authorization", "Bearer gb_x")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatalf("API on disabled instance: %d, want 404", resp.StatusCode)
	}
}

// TestAPIRateLimit covers the limiter over the wire: a caller who exceeds
// their budget gets 429 with a Retry-After a client can honour, writes are
// metered separately from reads, and one caller cannot spend another's
// budget.
func TestAPIRateLimit(t *testing.T) {
	// 6/minute sustained, so the read burst is 6 and the write burst 0.6 —
	// the first write is allowed and the second is not.
	inst := startInstanceWith(t, "[api]\nenabled = true\n[limits]\napi_rate = 6\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	aliceTok := mintToken(t, inst, aliceKey, "alice-app")
	bobTok := mintToken(t, inst, bobKey, "bob-app")

	// Reads: the burst is spendable, then the door closes.
	var limited bool
	for i := 0; i < 12; i++ {
		status, body := inst.apiCall(t, aliceTok, []string{"whoami"}, "")
		if status == http.StatusTooManyRequests {
			limited = true
			if msg, _ := body["error"].(string); !strings.Contains(msg, "retry") {
				t.Errorf("429 body does not say when to retry: %v", body)
			}
			break
		}
	}
	if !limited {
		t.Fatal("a caller never hit the rate limit")
	}

	// The 429 carries Retry-After, so a client backs off correctly instead
	// of hammering.
	req, _ := http.NewRequest("POST",
		fmt.Sprintf("http://127.0.0.1:%d/api/v1/cmd", inst.httpPort),
		strings.NewReader(`{"argv":["whoami"]}`))
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected a second 429, got %d", resp.StatusCode)
	}
	if ra := resp.Header.Get("Retry-After"); ra == "" || ra == "0" {
		t.Errorf("Retry-After = %q", ra)
	}

	// One caller's flood does not spend another's budget.
	if status, _ := inst.apiCall(t, bobTok, []string{"whoami"}, ""); status != http.StatusOK {
		t.Errorf("bob was limited by alice's traffic: %d", status)
	}

	// Writes are metered separately: bob's read budget is nearly full, but
	// his write budget is not.
	inst.apiCall(t, bobTok, []string{"repo", "create", "bob/one"}, "")
	status, _ := inst.apiCall(t, bobTok, []string{"repo", "create", "bob/two"}, "")
	if status != http.StatusTooManyRequests {
		t.Errorf("second write status %d, want 429 from the write budget", status)
	}
}

// mintToken creates an API token over SSH and returns its value.
func mintToken(t *testing.T, inst *instance, key, name string) string {
	t.Helper()
	out, errOut, code := inst.ssh(t, key, "", "token", "create", "--name", name, "--json")
	if code != 0 {
		t.Fatalf("token create: %s", errOut)
	}
	var env struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(out), &env); err != nil || env.Data.Token == "" {
		t.Fatalf("token JSON: %v\n%s", err, out)
	}
	return env.Data.Token
}

// apiGet fetches one read command, optionally conditionally.
func (i *instance) apiGet(t *testing.T, token string, argv []string, ifNoneMatch string) (int, string, string) {
	t.Helper()
	q := url.Values{}
	for _, a := range argv {
		q.Add("argv", a)
	}
	req, err := http.NewRequest("GET",
		fmt.Sprintf("http://127.0.0.1:%d/api/v1/read?%s", i.httpPort, q.Encode()), nil)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if ifNoneMatch != "" {
		req.Header.Set("If-None-Match", ifNoneMatch)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, resp.Header.Get("ETag"), string(raw)
}

// TestAPIReadGET covers the conditional-request surface: reads over GET
// with an ETag, 304 on revalidation, writes refused, and one caller's ETag
// never matching another's.
func TestAPIReadGET(t *testing.T) {
	inst := startInstanceWith(t, "[api]\nenabled = true\n")
	aliceKey := inst.newKey(t, "alice")
	bobKey := inst.newKey(t, "bob")
	inst.admin(t, "admin", "user", "create", "alice", "--key", aliceKey+".pub")
	inst.admin(t, "admin", "user", "create", "bob", "--key", bobKey+".pub")
	aliceTok := mintToken(t, inst, aliceKey, "alice-get")
	bobTok := mintToken(t, inst, bobKey, "bob-get")
	if _, errOut, code := inst.ssh(t, aliceKey, "", "repo", "create", "alice/app"); code != 0 {
		t.Fatalf("repo create: %s", errOut)
	}

	status, etag, body := inst.apiGet(t, aliceTok, []string{"repo", "show", "alice/app"}, "")
	if status != 200 {
		t.Fatalf("GET read: %d %s", status, body)
	}
	if etag == "" {
		t.Fatal("no ETag, so a client cannot revalidate")
	}
	if !strings.Contains(body, `"alice/app"`) {
		t.Errorf("body: %s", body)
	}

	// Revalidation returns 304 with no body — the point of the surface.
	status, _, body = inst.apiGet(t, aliceTok, []string{"repo", "show", "alice/app"}, etag)
	if status != http.StatusNotModified {
		t.Fatalf("revalidation status %d, want 304", status)
	}
	if body != "" {
		t.Errorf("304 carried a body: %q", body)
	}
	// A weak validator from an intermediary still matches.
	if status, _, _ := inst.apiGet(t, aliceTok, []string{"repo", "show", "alice/app"}, "W/"+etag); status != http.StatusNotModified {
		t.Errorf("weak ETag not honoured: %d", status)
	}

	// A stale ETag gets the real body back, not a 304.
	if status, _, body := inst.apiGet(t, aliceTok, []string{"repo", "show", "alice/app"}, `"stale"`); status != 200 || body == "" {
		t.Errorf("stale ETag: %d %q", status, body)
	}

	// The ETag is salted per caller, so one account can never be handed a
	// 304 for another account's cached answer.
	if status, _, _ := inst.apiGet(t, bobTok, []string{"repo", "show", "alice/app"}, etag); status == http.StatusNotModified {
		t.Error("another caller's ETag matched")
	}

	// Responses must not be storable by shared caches.
	req, _ := http.NewRequest("GET",
		fmt.Sprintf("http://127.0.0.1:%d/api/v1/read?argv=whoami", inst.httpPort), nil)
	req.Header.Set("Authorization", "Bearer "+aliceTok)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if cc := resp.Header.Get("Cache-Control"); !strings.Contains(cc, "private") {
		t.Errorf("Cache-Control = %q, want private", cc)
	}

	// A GET can never mutate: writes are refused by the registry's own
	// ReadOnly flag rather than by a hand-kept list.
	status, _, body = inst.apiGet(t, aliceTok, []string{"repo", "create", "alice/sneaky"}, "")
	if status != http.StatusBadRequest || !strings.Contains(body, "POST it") {
		t.Fatalf("write over GET: %d %s", status, body)
	}
	if _, _, code := inst.ssh(t, aliceKey, "", "repo", "show", "alice/sneaky"); code == 0 {
		t.Fatal("a GET created a repository")
	}

	// Unauthenticated reads are refused like everywhere else.
	if status, _, _ := inst.apiGet(t, "", []string{"whoami"}, ""); status != http.StatusUnauthorized {
		t.Errorf("anonymous GET status %d, want 401", status)
	}
}
