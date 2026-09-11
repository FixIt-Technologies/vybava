package claudeguards

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeOnyx stands in for onyx-mcp's GET /browser?session= route and points the
// guard at itself through the same env the installer documents.
func fakeOnyx(t *testing.T, status int) (calls *int) {
	t.Helper()
	n := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(status)
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
	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-1")
	return &n
}

func browserInput() *HookInput {
	in := &HookInput{ToolName: "mcp__playwright__browser_navigate"}
	return in
}

// The regression itself: no onyx browser for this session → the third-party
// MCP would launch standalone → block, naming browser_start with the session.
func TestBrowserBlocksWhenNoOnyxBrowserForSession(t *testing.T) {
	fakeOnyx(t, http.StatusNotFound)
	d := Browser(browserInput())
	if d == nil {
		t.Fatal("expected a denial when the lookup says 404")
	}
	if d.Rule != "browser:onyx-first" {
		t.Fatalf("rule = %q", d.Rule)
	}
	if !strings.Contains(d.Text(), `browser_start(session: "sess-1")`) {
		t.Fatalf("denial must name the fix with the session id:\n%s", d.Text())
	}
}

func TestBrowserAllowsWhenOnyxBrowserIsRunning(t *testing.T) {
	fakeOnyx(t, http.StatusOK)
	if d := Browser(browserInput()); d != nil {
		t.Fatalf("unexpected denial: %s", d.Text())
	}
}

// Fail-open cases: the guard must never brick a session where onyx is absent.
func TestBrowserFailsOpenWithoutSessionTokenOrServer(t *testing.T) {
	calls := fakeOnyx(t, http.StatusNotFound)

	t.Setenv("CLAUDE_CODE_SESSION_ID", "")
	t.Setenv("ONYX_MCP_SESSION", "")
	if d := Browser(browserInput()); d != nil {
		t.Fatalf("no session id must fail open, got: %s", d.Text())
	}
	if *calls != 0 {
		t.Fatalf("no session id must not even call the server, calls=%d", *calls)
	}

	t.Setenv("CLAUDE_CODE_SESSION_ID", "sess-1")
	t.Setenv("ONYX_MCP_HTTP_TOKEN_FILE", filepath.Join(t.TempDir(), "missing"))
	if d := Browser(browserInput()); d != nil {
		t.Fatalf("missing token file must fail open, got: %s", d.Text())
	}

	fakeOnyx(t, http.StatusUnauthorized)
	if d := Browser(browserInput()); d != nil {
		t.Fatalf("401 is not 'no browser' and must fail open, got: %s", d.Text())
	}

	t.Setenv("ONYX_MCP_HTTP_PORT", "1") // nothing listens there
	if d := Browser(browserInput()); d != nil {
		t.Fatalf("unreachable server must fail open, got: %s", d.Text())
	}
}
