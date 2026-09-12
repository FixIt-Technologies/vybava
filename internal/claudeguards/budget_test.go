package claudeguards

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBudgetTiers(t *testing.T) {
	root := t.TempDir()
	transcript := filepath.Join(root, "session.jsonl")
	file := filepath.Join(root, "source.ts")
	if err := os.WriteFile(file, []byte(strings.Repeat("x\n", 300)), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		model                string
		tokens               int
		limit                int
		image, block, remind bool
	}{
		{"claude-fable-5-1", 499999, 101, false, false, false},
		{"claude-fable-5-1", 500000, 101, false, false, true},
		{"claude-opus-5", 700000, 101, false, true, true},
		{"claude-sonnet-5", 700000, 100, false, false, true},
		{"claude-haiku-4-5", 140000, 0, false, true, true},
		{"claude-fable-5-1", 900000, 0, true, false, true},
		{"unknown", 900000, 0, false, false, false},
	} {
		t.Run(fmt.Sprintf("%s-%d-%d-%v", tc.model, tc.tokens, tc.limit, tc.image), func(t *testing.T) {
			row := fmt.Sprintf(`{"type":"assistant","message":{"model":%q,"usage":{"input_tokens":10,"cache_creation_input_tokens":20,"cache_read_input_tokens":%d}}}`, tc.model, tc.tokens-30)
			if err := os.WriteFile(transcript, []byte(row+"\n{partial"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(transcript + ".budget-50"); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			in := &HookInput{CWD: root, TranscriptPath: transcript}
			in.ToolInput.FilePath, in.ToolInput.Limit = file, tc.limit
			if tc.image {
				in.ToolInput.FilePath = filepath.Join(root, "shot.png")
			}
			if got := guardBudget(in); (got != nil) != tc.block {
				t.Fatalf("denial = %v", got)
			}
			message := BudgetContext(in)
			if strings.HasPrefix(message, "Context at") != tc.remind {
				t.Fatalf("reminder = %q", message)
			}
			if tc.remind && BudgetContext(in) != "" {
				t.Fatal("duplicate reminder")
			}
			in.ToolInput.FilePath = ""
			in.ToolInput.Command = "sed -n '1,101p' " + file
			if b, err := ReadBudget(transcript); err == nil && b.Percent() >= 70 {
				if guardBudget(in) == nil {
					t.Fatal("unbounded shell read allowed")
				}
				in.ToolInput.Command += " | tail -10"
				if guardBudget(in) != nil {
					t.Fatal("capped pipeline denied")
				}
			}
		})
	}
	if _, err := ReadBudget(filepath.Join(root, "missing")); err == nil {
		t.Fatal("missing transcript must report unavailable")
	}
}

// What a Read delivers is bounded by the offset as well as the limit, so the
// 70% tier must count what is left of the file, not the whole of it.
func TestBudgetReadCountsRemainingAfterOffset(t *testing.T) {
	root := t.TempDir()
	transcript := filepath.Join(root, "s.jsonl")
	row := `{"type":"assistant","message":{"model":"claude-opus-5","usage":{"input_tokens":800000}}}`
	if err := os.WriteFile(transcript, []byte(row+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, "source.ts")
	if err := os.WriteFile(file, []byte(strings.Repeat("x\n", 300)), 0600); err != nil {
		t.Fatal(err)
	}
	at := func(offset, limit int) *Denial {
		in := &HookInput{CWD: root, TranscriptPath: transcript, SessionID: "s"}
		in.ToolInput.FilePath, in.ToolInput.Offset, in.ToolInput.Limit = file, offset, limit
		return guardBudget(in)
	}
	if d := at(250, 0); d != nil {
		t.Fatalf("50 lines remain after the offset — must be allowed, got %v", d)
	}
	if d := at(250, 200); d != nil {
		t.Fatalf("the file runs out before the limit does — must be allowed, got %v", d)
	}
	if d := at(0, 0); d == nil {
		t.Fatal("the whole 300-line file must still be denied at 80%")
	}
	// Offsets are one-based: offset 1 is the whole file, not one line short.
	if d := at(1, 0); d == nil {
		t.Fatal("offset 1 returns every line and must be denied")
	}
	if d := at(201, 0); d != nil {
		t.Fatalf("lines 201-300 is exactly 100 — must be allowed, got %v", d)
	}
	if d := at(200, 0); d == nil {
		t.Fatal("lines 200-300 is 101 lines and must be denied")
	}
	if d := at(0, 150); d == nil {
		t.Fatal("a 150-line limit must still be denied at 80%")
	}
}

// Fail-open diagnostics belong on stderr. Returned here they become
// additionalContext on every passing tool call — the context-budget rule
// spending context to say it is not working.
func TestBudgetContextSilentWhenUnavailable(t *testing.T) {
	root := t.TempDir()
	unknownModel := filepath.Join(root, "unknown.jsonl")
	row := `{"type":"assistant","message":{"model":"claude-sonnet-4-5-20250929","usage":{"input_tokens":900000}}}`
	if err := os.WriteFile(unknownModel, []byte(row+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(root, "empty.jsonl")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"", filepath.Join(root, "missing.jsonl"), unknownModel, empty} {
		for range 3 {
			if got := BudgetContext(&HookInput{TranscriptPath: path, SessionID: "s"}); got != "" {
				t.Fatalf("%q: fail-open must stay out of the model's context, got %q", path, got)
			}
		}
	}
}
