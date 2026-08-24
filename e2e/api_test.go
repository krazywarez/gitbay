package e2e

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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

	// Raw-output commands (no envelope) are wrapped.
	status, body = inst.apiCall(t, token, []string{"help"}, "")
	if status != 200 || !strings.Contains(body["output"].(string), "repo create") {
		t.Fatalf("help via API: %d %v", status, body)
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
