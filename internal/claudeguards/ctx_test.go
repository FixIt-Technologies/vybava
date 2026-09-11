package claudeguards

import (
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestDiagnoseContext(t *testing.T) {
	header := make([]byte, 24)
	copy(header, "\x89PNG\r\n\x1a\n")
	binary.BigEndian.PutUint32(header[16:], 1280)
	binary.BigEndian.PutUint32(header[20:], 900)
	row := `{"type":"assistant","timestamp":"2026-09-11T10:00:00Z","message":{"id":"one","usage":{"input_tokens":10,"cache_read_input_tokens":499990,"output_tokens":100,"output_tokens_details":{"thinking_tokens":20}},"content":[{"type":"tool_use","id":"read1","name":"Read"}]}}`
	result := fmt.Sprintf(`{"type":"user","message":{"content":[{"type":"text","text":"Open vitrinka todos"},{"type":"tool_result","tool_use_id":"read1","content":[{"type":"text","text":"some result"},{"type":"image","source":{"data":%q}}]}]}}`, base64.StdEncoding.EncodeToString(header))
	p := filepath.Join(t.TempDir(), "abc.jsonl")
	if err := os.WriteFile(p, []byte(row+"\n"+row+"\n"+result+"\n{partial"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := DiagnoseContext(p)
	if err != nil {
		t.Fatal(err)
	}
	if r.Responses != 1 || r.OutputTokens != 100 || r.ThinkingTokens != 20 || r.MaxContext != 500000 || r.TodoInjections != 1 || r.MalformedRecords != 1 {
		t.Fatalf("report: %+v", r)
	}
	if len(r.Images) != 1 || r.Images[0].EstimatedTokens != 1536 {
		t.Fatal(r.Images)
	}
	if got, err := ResolveTranscript(filepath.Dir(p), "abc"); err != nil || got != p {
		t.Fatal(got, err)
	}
}
