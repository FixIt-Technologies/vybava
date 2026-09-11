package claudeguards

import (
	"encoding/json"
	"io"
	"regexp"
	"strings"
)

// HookInput is the PreToolUse payload Claude Code pipes to hook commands.
// Unknown fields are ignored by encoding/json, so schema growth is safe.
type HookInput struct {
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
	CWD            string `json:"cwd"`
	ToolName       string `json:"tool_name"`
	ToolInput      struct {
		Command  string `json:"command"`
		FilePath string `json:"file_path"`
		Offset   int    `json:"offset"`
		Limit    int    `json:"limit"`
	} `json:"tool_input"`
}

// ReadInput parses the hook payload; any error means fail-open.
func ReadInput(r io.Reader) (*HookInput, error) {
	raw, err := io.ReadAll(io.LimitReader(r, 4<<20))
	if err != nil {
		return nil, err
	}
	var in HookInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, err
	}
	return &in, nil
}

// Shell separators after which a new command can start. Splitting on these
// means `make build && git stash` is inspected too. `$(` and backtick open
// command substitutions.
var segmentSplit = regexp.MustCompile("\\|\\||&&|[;&|\n]|\\$\\(|`")

// A heredoc intro with a QUOTED delimiter: `<< 'EOF'` / << "EOF" (optionally
// <<-). A quoted delimiter makes the body literal text — no expansion, no
// command substitution — so it cannot execute anything and is stripped before
// segmenting. Unquoted heredocs (<< EOF) DO expand `$(...)` in the body, so
// their bodies stay in and are scanned like any other text.
var heredocQuotedRE = regexp.MustCompile(`<<-?[ \t]*(?:'([A-Za-z_][A-Za-z0-9_]*)'|"([A-Za-z_][A-Za-z0-9_]*)")`)

// stripQuotedHeredocs removes the bodies (and closing delimiter lines) of
// quoted-delimiter heredocs so prose/scripts fed via stdin don't false-trip
// command rules. Content-preserving for everything else.
func stripQuotedHeredocs(cmd string) string {
	var b strings.Builder
	pos := 0
	for pos < len(cmd) {
		m := heredocQuotedRE.FindStringSubmatchIndex(cmd[pos:])
		if m == nil {
			b.WriteString(cmd[pos:])
			break
		}
		delim := ""
		if m[2] >= 0 {
			delim = cmd[pos+m[2] : pos+m[3]]
		} else {
			delim = cmd[pos+m[4] : pos+m[5]]
		}
		introEnd := pos + m[1]
		nl := strings.IndexByte(cmd[introEnd:], '\n')
		if nl < 0 {
			// Intro with no body in this string; keep everything.
			b.WriteString(cmd[pos:])
			break
		}
		bodyStart := introEnd + nl + 1
		// Keep everything through the intro line's newline.
		b.WriteString(cmd[pos:bodyStart])
		// Skip the body up to and including the closing delimiter line. If the
		// delimiter never appears, shell semantics make the whole rest the
		// body — skip it all.
		rest := cmd[bodyStart:]
		end := len(cmd)
		switch {
		case strings.HasPrefix(rest, delim+"\n"):
			end = bodyStart + len(delim) + 1
		case rest == delim:
			end = len(cmd)
		default:
			if i := strings.Index(rest, "\n"+delim+"\n"); i >= 0 {
				end = bodyStart + i + 1 + len(delim) + 1
			} else if strings.HasSuffix(rest, "\n"+delim) {
				end = len(cmd)
			}
		}
		pos = end
	}
	return b.String()
}

// segments splits a command string into independently inspectable pieces,
// left-trimmed, empties dropped. Quoted-heredoc bodies are excluded first.
func segments(cmd string) []string {
	parts := segmentSplit.Split(stripQuotedHeredocs(cmd), -1)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		s := strings.Trim(p, " \t\r")
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

// textOnly reports whether a segment merely *mentions* commands without being
// able to execute them: comments, echoed/printed text, commit messages. Chained
// real commands live in their own segment, so skipping these costs no coverage.
func textOnly(s string) bool {
	if strings.HasPrefix(s, "#") {
		return true
	}
	for _, p := range []string{"echo ", "printf ", "cat ", "git commit "} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return s == "git commit"
}
