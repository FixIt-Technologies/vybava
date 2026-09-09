package cli

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/henderson-tech/vybava/internal/operator"
)

func TestIndexedOperatorCLIHistoryAndOutcome(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	run := func(input string, args ...string) string {
		t.Helper()
		var out bytes.Buffer
		cmd, err := (App{Stdout: &out, Stderr: &out, Stdin: strings.NewReader(input)}).Command("vybava")
		if err != nil {
			t.Fatal(err)
		}
		cmd.SetArgs(append([]string{"operator", "--state-dir", dir, "--json"}, args...))
		if err := cmd.Execute(); err != nil {
			t.Fatal(err)
		}
		return out.String()
	}
	var event struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal([]byte(run(`{"source":"whatsapp","key":"fixture","revision":"1","text":"Fixture only"}`, "observe")), &event); err != nil {
		t.Fatal(err)
	}
	run("Investigate fixture", "propose", event.ID)
	run("", "migrate")
	var before struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal([]byte(run("", "revision")), &before); err != nil {
		t.Fatal(err)
	}
	run("Fixture action ran", "record-outcome", event.ID, "--proposal", "1", "--status", "executed")
	run("Fixture target checked", "record-outcome", event.ID, "--proposal", "1", "--status", "verified")
	var page operator.HistoryPage
	if err := json.Unmarshal([]byte(run("", "history", "--limit", "1")), &page); err != nil {
		t.Fatal(err)
	}
	if len(page.Events) != 1 || page.Events[0].ID != event.ID || len(page.Events[0].Outcomes) != 2 {
		t.Fatalf("history lost outcome: %+v", page)
	}
	var after struct {
		Revision string `json:"revision"`
	}
	if err := json.Unmarshal([]byte(run("", "revision")), &after); err != nil {
		t.Fatal(err)
	}
	if before.Revision == after.Revision {
		t.Fatal("outcome did not invalidate UI revision")
	}
	var snapshot operator.CompanionSnapshot
	if err := json.Unmarshal([]byte(run("", "snapshot", "--limit", "1")), &snapshot); err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Events) != 1 || snapshot.Events[0].Proposals[0].Decision != nil {
		t.Fatal("recording an outcome invented human approval")
	}
}
