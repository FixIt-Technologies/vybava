package shrt

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
)

func TestTokenStoreIssueIdentifyRevoke(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	s, err := OpenTokenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	value, err := s.Issue("martin")
	if err != nil {
		t.Fatal(err)
	}
	if len(value) != 64 {
		t.Fatalf("token value length %d", len(value))
	}
	if _, err := s.Issue("martin"); err == nil {
		t.Fatal("duplicate issue must fail")
	}
	if name, ok := s.Identify(value); !ok || name != "martin" {
		t.Fatalf("identify: %q %v", name, ok)
	}

	// Persistence: the value still identifies after reopen (hash stored).
	reopened, err := OpenTokenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := reopened.Identify(value); !ok || name != "martin" {
		t.Fatalf("identify after reopen: %q %v", name, ok)
	}
	// List never leaks hashes.
	for _, tok := range reopened.List() {
		if tok.Hash != "" {
			t.Fatal("List must not include hashes")
		}
	}

	if err := reopened.Revoke("martin"); err != nil {
		t.Fatal(err)
	}
	if _, ok := reopened.Identify(value); ok {
		t.Fatal("revoked token must not identify")
	}
}

func TestServerMemberTokens(t *testing.T) {
	server, ts := newRuleTestServer(t)
	tokens, err := OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	server.Tokens = tokens

	// Admin issues a member token.
	resp, body := ruleAPICall(t, ts, "POST", "/api/tokens", `{"name":"martin"}`, "sekrit")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("issue: %d %s", resp.StatusCode, body)
	}
	var issued tokenIssueResponse
	if err := json.Unmarshal(body, &issued); err != nil {
		t.Fatal(err)
	}
	if issued.Token == "" {
		t.Fatal("issue must return the value once")
	}

	// Member can mint…
	resp, body = ruleAPICall(t, ts, "POST", "/api/mint", `{"url":"https://example.com/member/minted/link/long/enough"}`, issued.Token)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("member mint: %d %s", resp.StatusCode, body)
	}
	// …and manage rules…
	if resp, _ := ruleAPICall(t, ts, "POST", "/api/rules", `{"name":"docs","prefix":"https://docs.example.com/"}`, issued.Token); resp.StatusCode != http.StatusOK {
		t.Fatalf("member rule create: %d", resp.StatusCode)
	}
	// …but NOT manage tokens.
	if resp, _ := ruleAPICall(t, ts, "POST", "/api/tokens", `{"name":"eve2"}`, issued.Token); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member issue must be forbidden, got %d", resp.StatusCode)
	}
	if resp, _ := ruleAPICall(t, ts, "GET", "/api/tokens", "", issued.Token); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("member list must be forbidden, got %d", resp.StatusCode)
	}

	// List (admin) shows the name, no secrets.
	resp, body = ruleAPICall(t, ts, "GET", "/api/tokens", "", "sekrit")
	if resp.StatusCode != http.StatusOK || !strings.Contains(string(body), `"martin"`) || strings.Contains(string(body), issued.Token) {
		t.Fatalf("admin list: %d %s", resp.StatusCode, body)
	}

	// Revoke ends access immediately.
	if resp, _ := ruleAPICall(t, ts, "DELETE", "/api/tokens/martin", "", "sekrit"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("revoke: %d", resp.StatusCode)
	}
	if resp, _ := ruleAPICall(t, ts, "POST", "/api/mint", `{"url":"https://example.com/after/revoke/long/enough/x"}`, issued.Token); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked member mint must 401, got %d", resp.StatusCode)
	}
}
