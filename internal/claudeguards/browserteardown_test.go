package claudeguards

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// onyxCapture is what the fake daemon saw, copied out of the request while the
// handler still owns it.
type onyxCapture struct {
	calls  int
	method string
	path   string
	header http.Header
	body   []byte
}

// fakeOnyxMCP stands in for the daemon's POST /mcp endpoint and points the hook
// at itself through the same env the installer documents. respond writes the
// daemon's answer; nil means a plain success.
func fakeOnyxMCP(t *testing.T, respond func(w http.ResponseWriter)) *onyxCapture {
	t.Helper()
	got := &onyxCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.calls++
		got.method, got.path, got.header = r.Method, r.URL.Path, r.Header.Clone()
		got.body, _ = io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if respond != nil {
			respond(w)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stopped":true}}`))
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	tok := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tok, []byte("test-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ONYX_MCP_HTTP_PORT", u.Port())
	t.Setenv("ONYX_MCP_HTTP_TOKEN_FILE", tok)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-env")
	return got
}

// The wire contract, in full: the daemon serves 2026-07-28 only, with no
// handshake, and rejects any disagreement between the mirrored headers and the
// body. Getting a detail wrong here is a silent no-op, so all of it is pinned.
// The payload's session also beats the environment's.
func TestBrowserTeardownPostsBrowserStopForTheEndingSession(t *testing.T) {
	seen := fakeOnyxMCP(t, nil)

	BrowserTeardown(&HookInput{SessionID: "sess-payload"}, io.Discard)

	if seen.calls != 1 {
		t.Fatalf("calls = %d, want exactly one POST", seen.calls)
	}
	if seen.method != http.MethodPost || seen.path != "/mcp" {
		t.Fatalf("request = %s %s, want POST /mcp", seen.method, seen.path)
	}
	wantHeaders := map[string]string{
		"Content-Type":         "application/json",
		"Authorization":        "Bearer test-token",
		"MCP-Protocol-Version": "2026-07-28",
		"Mcp-Method":           "tools/call",
		"Mcp-Name":             "browser_stop",
	}
	for k, want := range wantHeaders {
		if got := seen.header.Get(k); got != want {
			t.Errorf("header %s = %q, want %q", k, got, want)
		}
	}

	var got mcpToolCall
	if err := json.Unmarshal(seen.body, &got); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, seen.body)
	}
	want := mcpToolCall{
		JSONRPC: "2.0", ID: 1, Method: "tools/call",
		Params: mcpCallParams{
			Name:      "browser_stop",
			Arguments: map[string]string{"session": "sess-payload"},
			Meta:      map[string]string{"io.modelcontextprotocol/protocolVersion": "2026-07-28"},
		},
	}
	if got.JSONRPC != want.JSONRPC || got.ID != want.ID || got.Method != want.Method {
		t.Errorf("envelope = %+v, want %+v", got, want)
	}
	if got.Params.Name != want.Params.Name || got.Params.Arguments["session"] != want.Params.Arguments["session"] {
		t.Errorf("params = %+v, want %+v", got.Params, want.Params)
	}
	if got.Params.Meta[onyxMetaVersionKey] != want.Params.Meta[onyxMetaVersionKey] {
		t.Errorf("params._meta = %+v, want the protocol version to match the header", got.Params.Meta)
	}
}

// SessionEnd payloads carry session_id, but the hook must still work when it is
// hand-run or the payload is malformed — then the environment names the session.
func TestBrowserTeardownFallsBackToTheEnvironmentSession(t *testing.T) {
	seen := fakeOnyxMCP(t, nil)

	BrowserTeardown(&HookInput{}, io.Discard)

	var got mcpToolCall
	if err := json.Unmarshal(seen.body, &got); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if got.Params.Arguments["session"] != "sess-env" {
		t.Fatalf("session = %q, want the environment's sess-env", got.Params.Arguments["session"])
	}
}

// No id, no target: the hook must never guess, sweep or stop a peer's browser.
func TestBrowserTeardownWithoutSessionStopsNothing(t *testing.T) {
	seen := fakeOnyxMCP(t, nil)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("ONYX_MCP_SESSION", "")

	BrowserTeardown(&HookInput{}, io.Discard)
	BrowserTeardown(nil, io.Discard)

	if seen.calls != 0 {
		t.Fatalf("calls = %d, want none without a session id", seen.calls)
	}
}

// Fail open on everything: a SessionEnd hook that errors or hangs degrades
// every session end on the machine.
func TestBrowserTeardownFailsOpenOnEveryDaemonFailure(t *testing.T) {
	cases := map[string]struct {
		respond func(w http.ResponseWriter)
		setup   func(t *testing.T)
	}{
		"no token file": {
			setup: func(t *testing.T) { t.Setenv("ONYX_MCP_HTTP_TOKEN_FILE", filepath.Join(t.TempDir(), "missing")) },
		},
		"connection refused": {
			setup: func(t *testing.T) { t.Setenv("ONYX_MCP_HTTP_PORT", "1") }, // nothing listens there
		},
		"server error": {
			respond: func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) },
		},
		"jsonrpc error": {
			respond: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32020,"message":"header mismatch"}}`))
			},
		},
		"unparseable body": {
			respond: func(w http.ResponseWriter) { _, _ = w.Write([]byte("not json")) },
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fakeOnyxMCP(t, c.respond)
			if c.setup != nil {
				c.setup(t)
			}
			// The contract is simply that this returns: no panic, no error, no
			// wait beyond the client timeout.
			BrowserTeardown(&HookInput{SessionID: "sess-1"}, io.Discard)
		})
	}
}
