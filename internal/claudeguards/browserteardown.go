package claudeguards

// Browser teardown — SessionEnd hook.
//
// Onyx went HTTP-only on 2026-09-11: one resident onyx-mcp daemon per Mac owns
// every session's Helium, keyed by CLAUDE_CODE_SESSION_ID. The daemon reaps a
// browser when its OWNING PROCESS dies — which used to be the session's own MCP
// process, and is now the daemon itself, which never dies. So nothing collects
// them: on 2026-09-12 this Mac carried 23 stranded Heliums, the oldest 21 hours
// old, ~0.5 GB apiece. The idle watchdog is no backstop either, because agents
// are widely told to pass idle_timeout_seconds: 0, which disables it outright.
//
// The daemon cannot fix this alone: from inside it, a session that will never
// call again looks exactly like a session thinking. Session end is knowledge
// only the ending session has, so the session hands it over — one POST of
// browser_stop for its OWN id.
//
// Two properties are load-bearing:
//
//   - ONLY this session's browser. Never a sweep, never a pkill, never a peer's
//     id. Onyx already shipped a cleanup that killed every peer's browser and
//     wiped their logged-in profiles; a hook that reintroduces it is worse than
//     the leak it fixes.
//   - FAIL OPEN, ALWAYS. This runs on every session end on the machine. No
//     token file, no daemon, a refused connection, a non-200, a JSON-RPC error:
//     all of them mean "nothing to do or cannot do it", none of them is worth
//     degrading a session end over.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// browserTeardownTimeout bounds the whole exchange. Stopping a browser is
	// heavier than the PreToolUse lookup, but a session end must not wait on a
	// wedged daemon.
	browserTeardownTimeout = 3 * time.Second
	// onyxProtocolVersion is the one MCP revision the daemon serves. It has no
	// initialize handshake, so a single POST is the whole interaction — and the
	// version must appear in both the header and params._meta or the daemon
	// answers with a header-mismatch error.
	onyxProtocolVersion = "2026-07-28"
	onyxMetaVersionKey  = "io.modelcontextprotocol/protocolVersion"
	onyxToolsCallMethod = "tools/call"
	onyxBrowserStopTool = "browser_stop"
)

// mcpToolCall is one JSON-RPC tools/call request in the 2026-07-28 shape.
type mcpToolCall struct {
	JSONRPC string        `json:"jsonrpc"`
	ID      int           `json:"id"`
	Method  string        `json:"method"`
	Params  mcpCallParams `json:"params"`
}

type mcpCallParams struct {
	Name      string            `json:"name"`
	Arguments map[string]string `json:"arguments"`
	Meta      map[string]string `json:"_meta"`
}

// teardownSessionID names the one browser this hook may stop: the ending
// session's own id from the payload, else the environment. Never anything else.
func teardownSessionID(in *HookInput) string {
	if in != nil {
		if s := strings.TrimSpace(in.SessionID); s != "" {
			return s
		}
	}
	return onyxSessionID()
}

// stopBrowser POSTs browser_stop for exactly one session. The mirrored headers
// (Mcp-Method, Mcp-Name) are the daemon's anti-confused-deputy check: they must
// equal the body's method and params.name or it refuses to run the call.
func stopBrowser(session string) error {
	token, ok := onyxBearerToken()
	if !ok {
		return errors.New("no onyx-mcp token file")
	}
	body, err := json.Marshal(mcpToolCall{
		JSONRPC: "2.0",
		ID:      1,
		Method:  onyxToolsCallMethod,
		Params: mcpCallParams{
			Name:      onyxBrowserStopTool,
			Arguments: map[string]string{"session": session},
			Meta:      map[string]string{onyxMetaVersionKey: onyxProtocolVersion},
		},
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, onyxHTTPBase()+"/mcp", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("MCP-Protocol-Version", onyxProtocolVersion)
	req.Header.Set("Mcp-Method", onyxToolsCallMethod)
	req.Header.Set("Mcp-Name", onyxBrowserStopTool)

	client := &http.Client{Timeout: browserTeardownTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return &daemonRefusal{fmt.Sprintf("HTTP %d %s", resp.StatusCode, rpcErrorMessage(raw))}
	}
	if msg := rpcErrorMessage(raw); msg != "" {
		return &daemonRefusal{msg}
	}
	return nil
}

// daemonRefusal is an answer FROM the daemon: onyx is installed and reachable,
// and it declined the stop. That is the one failure worth a line at session end
// — everything else means onyx simply is not here.
type daemonRefusal struct{ detail string }

func (e *daemonRefusal) Error() string { return e.detail }

// rpcErrorMessage pulls the JSON-RPC error out of a response body, "" when the
// body carries none (or is not JSON at all — an unparseable body is not an
// error we can name, and nothing here retries).
func rpcErrorMessage(raw []byte) string {
	var envelope struct {
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil || envelope.Error == nil {
		return ""
	}
	return fmt.Sprintf("%s (code %d)", envelope.Error.Message, envelope.Error.Code)
}

// BrowserTeardown is the SessionEnd hook entry point: stop the ending session's
// own onyx browser. No session id means there is nothing to stop. Every failure
// is silent — a Mac without onyx must see no noise at session end — except one
// the daemon itself reported, which is worth a line because it means onyx IS
// here and the browser survived anyway.
func BrowserTeardown(in *HookInput, stderr io.Writer) {
	session := teardownSessionID(in)
	if session == "" {
		return
	}
	if err := stopBrowser(session); err != nil {
		var refusal *daemonRefusal
		if errors.As(err, &refusal) {
			fmt.Fprintf(stderr, "claude-guards: onyx-mcp refused browser_stop for session %s: %v\n", session, refusal)
		}
		return
	}
	fmt.Fprintf(stderr, "claude-guards: stopped the onyx browser for session %s\n", session)
}
