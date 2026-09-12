package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// browser-teardown runs at every session end, so a payload it cannot parse must
// not be fatal: the environment still names the session whose browser has to
// stop, and the command exits 0 either way.
func TestClaudeGuardsBrowserTeardownSurvivesMalformedStdin(t *testing.T) {
	calls, sessions := 0, ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		sessions = string(body)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":{"stopped":true}}`))
	}))
	defer srv.Close()
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

	var out, errOut bytes.Buffer
	cmd, err := App{Stdin: strings.NewReader("{ this is not hook JSON"), Stdout: &out, Stderr: &errOut}.Command("claude-guards")
	if err != nil {
		t.Fatal(err)
	}
	cmd.SetArgs([]string{"browser-teardown"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("browser-teardown must never fail the session: %v", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want one browser_stop from the environment session", calls)
	}
	if !strings.Contains(sessions, `"session":"sess-env"`) {
		t.Fatalf("body = %s, want the environment session", sessions)
	}
}
