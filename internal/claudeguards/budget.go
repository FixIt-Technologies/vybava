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

// BudgetContext returns model-visible context. A marker prevents repeated 50%
// reminders; failure to create it stays observable, without blocking tools.
func BudgetContext(in *HookInput) string {
	if in.TranscriptPath == "" {
		return "claude-guards: context budget unavailable (missing transcript_path); budget rules fail open."
	}
	b, err := ReadBudget(in.TranscriptPath)
	if err != nil {
		return "claude-guards: context budget unavailable; budget rules fail open: " + err.Error()
	}
	if b.Percent() < 50 {
		return ""
	}
	f, err := os.OpenFile(in.TranscriptPath+".budget-50", os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return ""
	}
	if err != nil {
		return "claude-guards: cannot record context reminder: " + err.Error()
	}
	if err := f.Close(); err != nil {
		return "claude-guards: cannot close context reminder: " + err.Error()
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
		if in.ToolInput.Limit > 100 {
			over = true
		} else if in.ToolInput.Limit <= 0 {
			n, ok := lineCount(resolvePath(in.ToolInput.FilePath, in.CWD))
			over = ok && n > 100
		}
	}
	for _, seg := range dumpSegments(in.ToolInput.Command) {
		if seg.consumed {
			continue
		}
		verdict, _, _, _ := dumpBudgetWithLimit(seg.text, in.CWD, 100)
		if verdict == dumpOverBudget {
			over = true
		}
	}
	if over {
		return deny("context:budget-read", fmt.Sprintf("Context at %d%% — compact (/compact) or hand off (/handoff) before more reads above 100 lines.", b.Percent()), contextReadEscape)
	}
	return nil
}
