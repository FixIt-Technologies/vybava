package shrt

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
)

func newHTTPTest(t *testing.T, server *Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(server.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func jsonBody(body string) io.Reader {
	return strings.NewReader(body)
}

func readAll(t *testing.T, resp *http.Response) []byte {
	t.Helper()
	payload, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return payload
}

func enrollServer(t *testing.T, cidrs string) (*Server, *TokenStore) {
	t.Helper()
	tokens, err := OpenTokenStore(filepath.Join(t.TempDir(), "tokens.json"))
	if err != nil {
		t.Fatal(err)
	}
	prefixes, err := ParseEnrollCIDRs(cidrs)
	if err != nil {
		t.Fatal(err)
	}
	trusted, err := ParseEnrollCIDRs("127.0.0.0/8, ::1/128")
	if err != nil {
		t.Fatal(err)
	}
	return &Server{Base: "https://luko.to", MintToken: "sekrit", Tokens: tokens, EnrollCIDRs: prefixes, TrustedProxies: trusted}, tokens
}

func enrollCall(t *testing.T, server *Server, body string, headers map[string]string) (*int, []byte) {
	t.Helper()
	ts := newHTTPTest(t, server)
	req, _ := http.NewRequest("POST", ts.URL+"/api/enroll", jsonBody(body))
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	payload := readAll(t, resp)
	return &resp.StatusCode, payload
}

func TestParseEnrollCIDRs(t *testing.T) {
	if got, err := ParseEnrollCIDRs(""); err != nil || got != nil {
		t.Fatalf("empty must disable: %v %v", got, err)
	}
	if _, err := ParseEnrollCIDRs("not-a-cidr"); err == nil {
		t.Fatal("garbage must error")
	}
	got, err := ParseEnrollCIDRs("10.8.0.0/16, 10.7.0.0/16")
	if err != nil || len(got) != 2 {
		t.Fatalf("parse: %v %v", got, err)
	}
}

func TestEnrollMeshGating(t *testing.T) {
	server, _ := enrollServer(t, "10.8.0.0/16")

	// In-mesh via trusted X-Real-IP → issued.
	status, payload := enrollCall(t, server, `{"name":"martin"}`, map[string]string{"X-Real-IP": "10.8.4.7"})
	if *status != http.StatusOK {
		t.Fatalf("mesh enroll: %d %s", *status, payload)
	}
	var issued tokenIssueResponse
	if err := json.Unmarshal(payload, &issued); err != nil || issued.Token == "" {
		t.Fatalf("issue payload: %v %s", err, payload)
	}
	if name, ok := server.Tokens.Identify(issued.Token); !ok || name != "martin" {
		t.Fatalf("enrolled token identifies as %q %v", name, ok)
	}

	// Duplicate name → 409.
	if status, _ := enrollCall(t, server, `{"name":"martin"}`, map[string]string{"X-Real-IP": "10.8.4.8"}); *status != http.StatusConflict {
		t.Fatalf("duplicate enroll: %d", *status)
	}

	// Outside the mesh → 403.
	if status, _ := enrollCall(t, server, `{"name":"eva"}`, map[string]string{"X-Real-IP": "84.42.1.1"}); *status != http.StatusForbidden {
		t.Fatalf("external enroll must 403: %d", *status)
	}

	// X-Forwarded-For is NEVER trusted — a spoofed mesh value changes nothing.
	if status, _ := enrollCall(t, server, `{"name":"eva"}`, map[string]string{"X-Forwarded-For": "10.8.4.9"}); *status != http.StatusForbidden {
		t.Fatalf("spoofed XFF must 403: %d", *status)
	}

	// "admin" stays banned even over the mesh.
	if status, _ := enrollCall(t, server, `{"name":"admin"}`, map[string]string{"X-Real-IP": "10.8.4.7"}); *status != http.StatusBadRequest {
		t.Fatalf("admin enroll must 400: %d", *status)
	}
}

func TestXRealIPIgnoredFromUntrustedPeer(t *testing.T) {
	// Without the peer in TrustedProxies, a forged X-Real-IP must not grant
	// mesh membership — the peer address (loopback, outside 10.8/16) rules.
	server, _ := enrollServer(t, "10.8.0.0/16")
	server.TrustedProxies = nil
	if status, _ := enrollCall(t, server, `{"name":"forger"}`, map[string]string{"X-Real-IP": "10.8.4.7"}); *status != http.StatusForbidden {
		t.Fatalf("forged X-Real-IP from untrusted peer must 403, got %d", *status)
	}
}

func TestEnrollFailsClosed(t *testing.T) {
	// No CIDRs configured → 403 even from a mesh address.
	server, _ := enrollServer(t, "")
	if status, _ := enrollCall(t, server, `{"name":"martin"}`, map[string]string{"X-Real-IP": "10.8.4.7"}); *status != http.StatusForbidden {
		t.Fatalf("disabled enroll must 403: %d", *status)
	}
	// CIDRs but no token store → 403.
	prefixes, _ := ParseEnrollCIDRs("10.8.0.0/16")
	bare := &Server{Base: "https://luko.to", MintToken: "sekrit", EnrollCIDRs: prefixes}
	if status, _ := enrollCall(t, bare, `{"name":"martin"}`, map[string]string{"X-Real-IP": "10.8.4.7"}); *status != http.StatusForbidden {
		t.Fatalf("storeless enroll must 403: %d", *status)
	}
}

func TestEnrolledTokenCanMint(t *testing.T) {
	server, _ := enrollServer(t, "10.8.0.0/16")
	store, err := OpenStore(filepath.Join(t.TempDir(), "links.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	server.Store = store
	status, payload := enrollCall(t, server, `{"name":"martin"}`, map[string]string{"X-Real-IP": "10.8.4.7"})
	if *status != http.StatusOK {
		t.Fatalf("enroll: %d", *status)
	}
	var issued tokenIssueResponse
	if err := json.Unmarshal(payload, &issued); err != nil {
		t.Fatal(err)
	}
	ts := newHTTPTest(t, server)
	req, _ := http.NewRequest("POST", ts.URL+"/api/mint", jsonBody(`{"url":"https://example.com/enrolled/mint/long/enough/x"}`))
	req.Header.Set("Authorization", "Bearer "+issued.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("enrolled mint: %d", resp.StatusCode)
	}
}

func TestEnrollCIDRPrefixContains(t *testing.T) {
	prefix := netip.MustParsePrefix("10.8.0.0/16")
	if !prefix.Contains(netip.MustParseAddr("10.8.255.1")) || prefix.Contains(netip.MustParseAddr("10.9.0.1")) {
		t.Fatal("prefix sanity")
	}
}
