package claudeguards

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

// fakeOnyxMCP stands in for the daemon's two routes and points the hook at
// itself through the same env the installer documents. lookup is what GET
// /browser answers (200 = this session has a browser, 404 = it does not);
// respond writes the daemon's answer to the POST, nil means a plain success.
// Only the POST is captured — the lookup is a precondition, not the act.
func fakeOnyxMCP(t *testing.T, lookup int, respond func(w http.ResponseWriter)) *onyxCapture {
	t.Helper()
	got := &onyxCapture{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/browser" {
			w.WriteHeader(lookup)
			return
		}
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
// body. Getting a detail wrong here is a silent no-op, so all of it is pinned —
// as the JSON that actually goes over the wire, never through the structs that
// wrote it, which would agree with a renamed tag and leave the daemon rejecting
// every real call. The payload's session also beats the environment's.
func TestBrowserTeardownPostsBrowserStopForTheEndingSession(t *testing.T) {
	seen := fakeOnyxMCP(t, http.StatusOK, nil)

	var errOut bytes.Buffer
	BrowserTeardown(&HookInput{SessionID: "sess-payload"}, &errOut)

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

	const wantBody = `{
		"jsonrpc": "2.0",
		"id": 1,
		"method": "tools/call",
		"params": {
			"name": "browser_stop",
			"arguments": {"session": "sess-payload"},
			"_meta": {"io.modelcontextprotocol/protocolVersion": "2026-07-28"}
		}
	}`
	var got, want any
	if err := json.Unmarshal(seen.body, &got); err != nil {
		t.Fatalf("body is not JSON: %v (%s)", err, seen.body)
	}
	if err := json.Unmarshal([]byte(wantBody), &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("body = %s\nwant   %s", seen.body, wantBody)
	}

	if !strings.Contains(errOut.String(), "stopped the onyx browser for session sess-payload") {
		t.Errorf("stderr = %q, want the stop it really performed named with its session", errOut.String())
	}
}

// SessionEnd payloads carry session_id, but the hook must still work when it is
// hand-run or the payload is malformed — then the environment names the session.
func TestBrowserTeardownFallsBackToTheEnvironmentSession(t *testing.T) {
	seen := fakeOnyxMCP(t, http.StatusOK, nil)

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
	seen := fakeOnyxMCP(t, http.StatusOK, nil)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("ONYX_MCP_SESSION", "")

	BrowserTeardown(&HookInput{}, io.Discard)
	BrowserTeardown(nil, io.Discard)

	if seen.calls != 0 {
		t.Fatalf("calls = %d, want none without a session id", seen.calls)
	}
}

// The lookup is the ask that keeps the hook honest: browser_stop answers
// {"stopped":true} for a session that never had a browser, so without asking
// first every session end on a Mac with onyx would log a stop that never
// happened — and spend a POST to earn it.
func TestBrowserTeardownAsksBeforeStoppingAndStaysSilentWhenThereIsNoBrowser(t *testing.T) {
	seen := fakeOnyxMCP(t, http.StatusNotFound, nil)

	var errOut bytes.Buffer
	BrowserTeardown(&HookInput{SessionID: "sess-1"}, &errOut)

	if seen.calls != 0 {
		t.Errorf("calls = %d, want no browser_stop when the lookup says there is no browser", seen.calls)
	}
	if errOut.Len() != 0 {
		t.Errorf("stderr = %q, want silence about a browser that was never there", errOut.String())
	}
}

// Fail open on everything, and say at most one line about it. This runs at
// every session end on the machine: a Mac without onyx must hear nothing, and a
// daemon that refused is worth exactly one line — the daemon reports a refused
// call and a failed tool in two different shapes, and both are refusals.
func TestBrowserTeardownFailsOpenAndPrintsAtMostOneLine(t *testing.T) {
	cases := map[string]struct {
		setup   func(t *testing.T)
		respond func(w http.ResponseWriter)
		lines   int
		names   string
	}{
		"no token file": {
			setup: func(t *testing.T) { t.Setenv("ONYX_MCP_HTTP_TOKEN_FILE", filepath.Join(t.TempDir(), "missing")) },
		},
		"connection refused": {
			setup: func(t *testing.T) { t.Setenv("ONYX_MCP_HTTP_PORT", "1") }, // nothing listens there
		},
		"server error": {
			respond: func(w http.ResponseWriter) { w.WriteHeader(http.StatusInternalServerError) },
			lines:   1, names: "HTTP 500",
		},
		"jsonrpc error": {
			respond: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32020,"message":"header mismatch"}}`))
			},
			lines: 1, names: "header mismatch",
		},
		// The live shape of a tool-level failure: HTTP 200, no error member,
		// the reason inside result. Read as success it would be invisible.
		"tool error inside a 200": {
			respond: func(w http.ResponseWriter) {
				_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"error: invalidArguments(session)"}],"isError":true}}`))
			},
			lines: 1, names: "invalidArguments(session)",
		},
		// Not a refusal: an answer we cannot parse is one we cannot name, and
		// nothing here retries — so it is read as the stop having happened.
		"unparseable body": {
			respond: func(w http.ResponseWriter) { _, _ = w.Write([]byte("not json")) },
			lines:   1, names: "stopped the onyx browser",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			fakeOnyxMCP(t, http.StatusOK, c.respond)
			if c.setup != nil {
				c.setup(t)
			}
			var errOut bytes.Buffer
			BrowserTeardown(&HookInput{SessionID: "sess-1"}, &errOut)

			var lines []string
			if s := strings.TrimSuffix(errOut.String(), "\n"); s != "" {
				lines = strings.Split(s, "\n")
			}
			if len(lines) != c.lines {
				t.Fatalf("stderr = %q, want %d line(s)", errOut.String(), c.lines)
			}
			if c.names != "" && !strings.Contains(lines[0], c.names) {
				t.Errorf("stderr = %q, want it to name %q", lines[0], c.names)
			}
		})
	}
}
