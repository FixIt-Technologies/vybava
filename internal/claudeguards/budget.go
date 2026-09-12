package claudeguards

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// Budget is the latest input usage, not cumulative session spend.
type Budget struct {
	Tokens int    `json:"tokens"`
	Window int    `json:"window"`
	Model  string `json:"model"`
}

func (b Budget) Percent() int {
	if b.Window == 0 {
		return 0
	}
	return b.Tokens * 100 / b.Window
}

// ReadBudget bounds hook IO even for sessions containing base64 screenshots.
// An incomplete first/last record is ignored. Unknown models fail open.
func ReadBudget(path string) (Budget, error) {
	f, err := os.Open(path)
	if err != nil {
		return Budget{}, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return Budget{}, err
	}
	const tailBytes int64 = 4 << 20
	start := max(int64(0), st.Size()-tailBytes)
	if _, err = f.Seek(start, io.SeekStart); err != nil {
		return Budget{}, err
	}
	raw, err := io.ReadAll(io.LimitReader(f, tailBytes))
	if err != nil {
		return Budget{}, err
	}
	lines := bytes.Split(raw, []byte{'\n'})
	if start > 0 {
		lines = lines[1:]
	}
	for i := len(lines) - 1; i >= 0; i-- {
		var row struct {
			Type    string `json:"type"`
			Message struct {
				Model string `json:"model"`
				Usage *struct {
					Input    int `json:"input_tokens"`
					Creation int `json:"cache_creation_input_tokens"`
					Read     int `json:"cache_read_input_tokens"`
				} `json:"usage"`
			} `json:"message"`
		}
		if json.Unmarshal(lines[i], &row) != nil || row.Type != "assistant" || row.Message.Usage == nil {
			continue
		}
		window := modelWindow(row.Message.Model)
		if window == 0 {
			return Budget{}, fmt.Errorf("unknown model %q", row.Message.Model)
		}
		u := row.Message.Usage
		if u.Input < 0 || u.Creation < 0 || u.Read < 0 {
			return Budget{}, errors.New("negative usage")
		}
		return Budget{Tokens: u.Input + u.Creation + u.Read, Window: window, Model: row.Message.Model}, nil
	}
	return Budget{}, errors.New("no recent assistant usage")
}

func modelWindow(model string) int {
	switch {
	case strings.HasPrefix(model, "claude-haiku-"):
		return 200_000
	case strings.HasPrefix(model, "claude-fable-5"), strings.HasPrefix(model, "claude-opus-5"), strings.HasPrefix(model, "claude-sonnet-5"):
		return 1_000_000
	default:
		return 0
	}
}

// budgetDiag reports a fail-open condition to the operator. It deliberately
// does NOT travel back as additionalContext: that string enters the model's
// context on every passing tool call, and a context-budget rule narrating its
// own failure hundreds of times is the exact cost this file exists to prevent.
func budgetDiag(msg string) { fmt.Fprintln(os.Stderr, "claude-guards: "+msg) }

// budgetMarkers are the once-per-session reminder markers, in preference
// order. The transcript's own directory is first; a temp-dir marker keyed by
// session is the fallback, because without any marker the reminder repeats on
// every single tool call.
func budgetMarkers(in *HookInput) []string {
	markers := []string{in.TranscriptPath + ".budget-50"}
	if in.SessionID != "" {
		markers = append(markers, filepath.Join(os.TempDir(), "claude-guards-budget-50-"+filepath.Base(in.SessionID)))
	}
	return markers
}

// BudgetContext returns model-visible context: the one 50% reminder per
// session, and nothing else. Every fail-open path returns "" and reports on
// stderr instead.
func BudgetContext(in *HookInput) string {
	if in.TranscriptPath == "" {
		budgetDiag("context budget unavailable (missing transcript_path); budget rules fail open")
		return ""
	}
	b, err := ReadBudget(in.TranscriptPath)
	if err != nil {
		budgetDiag("context budget unavailable; budget rules fail open: " + err.Error())
		return ""
	}
	if b.Percent() < 50 {
		return ""
	}
	claimed := false
	for _, marker := range budgetMarkers(in) {
		f, err := os.OpenFile(marker, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(err, os.ErrExist) {
			return "" // already reminded this session
		}
		if err != nil {
			budgetDiag("cannot record context reminder at " + marker + ": " + err.Error())
			continue
		}
		if err := f.Close(); err != nil {
			budgetDiag("cannot close context reminder: " + err.Error())
		}
		claimed = true
		break
	}
	if !claimed {
		budgetDiag("context reminder could not be recorded anywhere; it may repeat")
	}
	return fmt.Sprintf("Context at %d%% (%d/%d tokens). Compact (/compact) or hand off (/handoff) before prolonged work. At 70%%, text reads above 100 lines are denied; screenshot reads remain available.", b.Percent(), b.Tokens, b.Window)
}

func guardBudget(in *HookInput) *Denial {
	if os.Getenv("CLAUDE_ALLOW_CONTEXT_DUMP") == "1" || escapeHatch(in.ToolInput.Command, "CLAUDE_ALLOW_CONTEXT_DUMP") {
		return nil
	}
	b, err := ReadBudget(in.TranscriptPath)
	if err != nil || b.Percent() < 70 {
		return nil
	}
	over := false
	if in.ToolInput.FilePath != "" && !noLineBudget[strings.ToLower(filepath.Ext(in.ToolInput.FilePath))] {
		// What a Read actually delivers is bounded by BOTH the limit and what
		// is left after the offset. Reading a 300-line file from offset 250
		// yields 50 lines, whatever the limit says.
		n, measured := lineCount(resolvePath(in.ToolInput.FilePath, in.CWD))
		remaining := max(0, n-max(0, in.ToolInput.Offset))
		switch {
		case in.ToolInput.Limit > 0:
			printed := in.ToolInput.Limit
			if measured && remaining < printed {
				printed = remaining
			}
			over = printed > 100
		case measured:
			over = remaining > 100
		}
	}
	segments := dumpSegments(in.ToolInput.Command)
	if len(segments) > 0 {
		cfg := guardConfig(in.CWD) // once per hook call, not once per segment
		for _, seg := range segments {
			if seg.consumed {
				continue
			}
			verdict, _, _, _ := dumpBudgetWithLimit(seg.text, in.CWD, cfg, 100)
			if verdict == dumpOverBudget {
				over = true
			}
		}
	}
	if over {
		return deny("context:budget-read", fmt.Sprintf("Context at %d%% — compact (/compact) or hand off (/handoff) before more reads above 100 lines.", b.Percent()), contextReadEscape)
	}
	return nil
}
