package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/FixIt-Technologies/vybava/internal/operator"
	"github.com/spf13/cobra"
)

func (rt *runtime) operatorApplet() *cobra.Command {
	c := rt.operatorCommand("operator")
	c.SilenceUsage, c.SilenceErrors = true, true
	c.SetOut(rt.stdout)
	c.SetErr(rt.stderr)
	c.PersistentFlags().BoolVar(&rt.json, "json", false, "emit stable JSON output")
	return c
}

func (rt *runtime) operatorCommand(use string) *cobra.Command {
	return rt.operatorCommandWithClock(use, time.Now)
}

func (rt *runtime) operatorCommandWithClock(use string, now func() time.Time) *cobra.Command {
	var dir string
	c := &cobra.Command{Use: use, Short: "Observe Claude/WhatsApp context, queue Codex attention, and score prepared replies; never send", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error { return cmd.Help() }}
	c.PersistentFlags().StringVar(&dir, "state-dir", "~/.local/share/vybava/operator", "private trial state directory")
	store := func() (operator.Store, error) {
		path, err := expandHome(dir)
		if err != nil {
			return operator.Store{}, err
		}
		path, err = filepath.Abs(path)
		return operator.Store{Dir: path}, err
	}
	output := func(value interface{}) error {
		// JSON is also the explicit detailed form for show/observe. Status/list
		// and watch retain concise default output for everyday use.
		return writeJSON(rt.stdout, value)
	}
	readInput := func() (string, error) {
		b, err := io.ReadAll(io.LimitReader(rt.stdin, 128001))
		if err != nil {
			return "", err
		}
		if len(b) > 128000 {
			return "", errors.New("input exceeds 128000 bytes")
		}
		return strings.TrimSpace(string(b)), nil
	}
	status := &cobra.Command{Use: "status", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		var summary operator.Summary
		if err := s.View(func(state *operator.State) error { summary = state.Summary(); return nil }); err != nil {
			return err
		}
		if rt.json {
			return output(summary)
		}
		_, err = fmt.Fprintf(rt.stdout, "%d events · %d pending · %d queued/unacknowledged · %d acknowledged · %d delivery problems\n%d proposals · %d scored · average %.2f/5 · sending: %s\n", summary.Events, summary.Pending, summary.Queued, summary.Acknowledged, summary.DeliveryProblems, summary.Proposals, summary.Scored, summary.Average, summary.Sending)
		return err
	}}
	list := &cobra.Command{Use: "list", Short: "List observations without printing conversation text", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		type row struct {
			ID           string          `json:"id"`
			Source       operator.Source `json:"source"`
			Delivery     string          `json:"delivery"`
			Superseded   bool            `json:"superseded"`
			Acknowledged bool            `json:"acknowledged"`
			Proposals    int             `json:"proposals"`
		}
		rows := []row{}
		if err := s.View(func(state *operator.State) error {
			for _, e := range state.Events {
				rows = append(rows, row{e.ID, e.Source, e.Delivery, e.Superseded, e.AcknowledgedAt != nil, len(e.Proposals)})
			}
			return nil
		}); err != nil {
			return err
		}
		if rt.json {
			return output(rows)
		}
		for _, r := range rows {
			if _, err := fmt.Fprintf(rt.stdout, "%s  %-8s %-10s acknowledged=%t superseded=%t proposals=%d\n", r.ID, r.Source, r.Delivery, r.Acknowledged, r.Superseded, r.Proposals); err != nil {
				return err
			}
		}
		return nil
	}}
	show := &cobra.Command{Use: "show EVENT", Short: "Read captured context and its proposal history", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		var event operator.Event
		if err := s.View(func(state *operator.State) error {
			e, err := state.Find(args[0])
			if err == nil {
				event = *e
			}
			return err
		}); err != nil {
			return err
		}
		return output(event)
	}}
	observe := &cobra.Command{Use: "observe", Short: "Read an actual app observation JSON from stdin (source, key, revision, text)", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		input, err := readInput()
		if err != nil {
			return err
		}
		var o operator.Observation
		dec := json.NewDecoder(strings.NewReader(input))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&o); err != nil {
			return err
		}
		if dec.Decode(new(json.RawMessage)) != io.EOF {
			return errors.New("expected one observation JSON object")
		}
		var id string
		var fresh bool
		if err := s.With(func(state *operator.State) error { var err error; id, fresh, err = state.Observe(o); return err }); err != nil {
			return err
		}
		return output(struct {
			ID  string `json:"id"`
			New bool   `json:"new"`
		}{id, fresh})
	}}
	ack := &cobra.Command{Use: "ack EVENT", Short: "Record that the operator actually received and read this event", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		if err := s.With(func(state *operator.State) error { return state.Acknowledge(args[0]) }); err != nil {
			return err
		}
		return output(struct {
			Acknowledged string `json:"acknowledged"`
		}{args[0]})
	}}
	propose := &cobra.Command{Use: "propose EVENT", Short: "Record proposed response text from stdin; does not touch the source app", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		text, err := readInput()
		if err != nil {
			return err
		}
		if err := s.With(func(state *operator.State) error { return state.Propose(args[0], text) }); err != nil {
			return err
		}
		return output(struct {
			Proposed string `json:"proposed"`
		}{args[0]})
	}}
	var score, proposal int
	var correction string
	rate := &cobra.Command{Use: "score EVENT", Short: "Record Lukáš's actual score for a specific proposal; never invent ratings", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		if err := s.With(func(state *operator.State) error { return state.Rate(args[0], proposal, score, correction) }); err != nil {
			return err
		}
		return output(struct {
			Scored string `json:"scored"`
		}{args[0]})
	}}
	rate.Flags().IntVar(&score, "score", 0, "human correctness score, 1 (wrong) to 5 (ready unchanged)")
	rate.Flags().IntVar(&proposal, "proposal", 0, "explicit proposal number from show (starts at 1)")
	rate.Flags().StringVar(&correction, "correction", "", "optional human correction or explanation")
	snapshot := &cobra.Command{Use: "snapshot", Short: "Read prepared work and last successful source scan for a local companion", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		var result operator.CompanionSnapshot
		if err := s.View(func(state *operator.State) error { result = state.Snapshot(); return nil }); err != nil {
			return err
		}
		return output(result)
	}}
	var feedbackProposal int
	feedback := &cobra.Command{Use: "feedback EVENT", Short: "Record actual human feedback from stdin without assigning a score", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		text, err := readInput()
		if err != nil {
			return err
		}
		if err := s.With(func(state *operator.State) error { return state.AddFeedback(args[0], feedbackProposal, text) }); err != nil {
			return err
		}
		return output(struct {
			Event string `json:"feedback_recorded"`
		}{args[0]})
	}}
	feedback.Flags().IntVar(&feedbackProposal, "proposal", 0, "explicit proposal number from show (starts at 1)")
	var historyBefore string
	var historyLimit int
	history := &cobra.Command{Use: "history", Short: "Read all recorded observations in stable pages, including superseded events", Args: cobra.NoArgs, RunE: func(*cobra.Command, []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		var result operator.HistoryPage
		if err := s.View(func(state *operator.State) error {
			var err error
			result, err = state.History(historyBefore, historyLimit)
			return err
		}); err != nil {
			return err
		}
		return output(result)
	}}
	history.Flags().StringVar(&historyBefore, "before", "", "event ID cursor from the previous page")
	history.Flags().IntVar(&historyLimit, "limit", 50, "page size, 1–200")
	var decisionProposal int
	var decisionAction string
	decision := &cobra.Command{Use: "decide EVENT", Short: "Record a human review decision; never sends or executes the proposal", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		note, err := readInput()
		if err != nil {
			return err
		}
		if err := s.With(func(state *operator.State) error {
			return state.Decide(args[0], decisionProposal, decisionAction, note)
		}); err != nil {
			return err
		}
		return output(struct {
			Event string `json:"decision_recorded"`
		}{args[0]})
	}}
	decision.Flags().IntVar(&decisionProposal, "proposal", 0, "exact proposal number")
	decision.Flags().StringVar(&decisionAction, "action", "", "approve, reject or revise; records a review only")
	var acknowledgedProposal int
	reviewAck := &cobra.Command{Use: "review-ack EVENT", Short: "Record actual operator receipt of a specific human review", Args: cobra.ExactArgs(1), RunE: func(_ *cobra.Command, args []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		if err := s.With(func(state *operator.State) error { return state.AcknowledgeReview(args[0], acknowledgedProposal) }); err != nil {
			return err
		}
		return output(struct {
			Event string `json:"review_acknowledged"`
		}{args[0]})
	}}
	reviewAck.Flags().IntVar(&acknowledgedProposal, "proposal", 0, "exact reviewed proposal number")
	queue := func(ctx context.Context, thread, message string) (string, error) {
		b, err := exec.CommandContext(ctx, "codex", "queue", "--thread", thread, "--message", message).CombinedOutput()
		if err != nil {
			return string(b), fmt.Errorf("codex queue failed; acceptance may be uncertain: %w", err)
		}
		if !strings.HasPrefix(strings.TrimSpace(string(b)), "Queued message ") {
			return string(b), errors.New("codex did not return a recognized queue receipt; reconcile before retrying")
		}
		return strings.TrimSpace(string(b)), nil
	}
	var thread string
	deliver := &cobra.Command{Use: "deliver EVENT", Short: "Queue one pending observation into an explicit Codex thread", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		s, err := store()
		if err != nil {
			return err
		}
		if err := s.Deliver(cmd.Context(), args[0], thread, queue); err != nil {
			return err
		}
		return output(struct {
			Queued string `json:"queued"`
		}{args[0]})
	}}
	deliver.Flags().StringVar(&thread, "thread", "", "target Codex thread ID or exact name")
	var root, codexRoot, watchThread string
	var excludedCodex []string
	var interval time.Duration
	var once bool
	var attentionSince string
	watch := &cobra.Command{Use: "watch", Short: "Observe new Claude and optional Codex activity; optionally queue events to Codex", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		var since time.Time
		if attentionSince != "" {
			var err error
			since, err = time.Parse(time.RFC3339, attentionSince)
			if err != nil || since.IsZero() || strings.TrimSpace(watchThread) == "" {
				return errors.New("--attention-since requires an RFC3339 timestamp and --thread")
			}
		}
		if interval < time.Second {
			return errors.New("interval must be at least 1s")
		}
		if codexRoot != "" && len(excludedCodex) == 0 {
			return errors.New("--codex-root requires --exclude-codex-session with the operator's actual session ID")
		}
		s, err := store()
		if err != nil {
			return err
		}
		root, err = expandHome(root)
		if err != nil {
			return err
		}
		if codexRoot != "" {
			codexRoot, err = expandHome(codexRoot)
			if err != nil {
				return err
			}
		}
		ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		var nextDispatch time.Time
		for {
			if watchThread != "" {
				var reviewID string
				var reviewNumber int
				if err := s.View(func(state *operator.State) error { reviewID, reviewNumber = state.NextReview(); return nil }); err != nil {
					return err
				}
				if reviewID != "" {
					if err := s.DeliverReview(ctx, reviewID, reviewNumber, watchThread, queue); err != nil {
						return err
					}
				}
			}
			var next string
			var pending int
			added, err := s.Scan(root, codexRoot, excludedCodex)
			if err != nil {
				return err
			}
			if err := s.View(func(state *operator.State) error {
				next, pending = state.NextDelivery(), state.Summary().Pending
				if !since.IsZero() {
					var err error
					next, err = state.ReadyAttention(since, now())
					if err != nil {
						return err
					}
					if now().Before(nextDispatch) {
						next = ""
					}
				}
				return nil
			}); err != nil {
				return err
			}
			if watchThread != "" && next != "" {
				var err error
				if since.IsZero() {
					err = s.Deliver(ctx, next, watchThread, queue)
				} else {
					err = s.DeliverAttention(ctx, next, watchThread, since, queue)
				}
				if err != nil && !errors.Is(err, operator.ErrAttentionChanged) {
					return err
				}
				if err == nil {
					nextDispatch = now().Add(2 * time.Minute)
				}
			}
			if once || len(added) > 0 {
				if rt.json {
					if err := output(struct {
						Observed []string `json:"observed"`
						Pending  int      `json:"pending_at_scan"`
					}{added, pending}); err != nil {
						return err
					}
				} else if _, err := fmt.Fprintf(rt.stdout, "observed=%d pending-at-scan=%d\n", len(added), pending); err != nil {
					return err
				}
			}
			if once {
				return nil
			}
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
			}
		}
	}}
	watch.Flags().StringVar(&root, "claude-root", "~/.claude/projects", "Claude projects directory (top-level session logs only)")
	watch.Flags().StringVar(&codexRoot, "codex-root", "", "optional Codex sessions directory; existing history is baselined")
	watch.Flags().StringSliceVar(&excludedCodex, "exclude-codex-session", nil, "Codex session IDs to exclude; must include the operator itself")
	watch.Flags().StringVar(&watchThread, "thread", "", "optional Codex target; omit to observe without dispatch")
	watch.Flags().StringVar(&attentionSince, "attention-since", "", "only recent, settled Claude questions/blockers/handoffs after this RFC3339 timestamp; requires --thread")
	watch.Flags().DurationVar(&interval, "interval", 5*time.Second, "scan interval")
	watch.Flags().BoolVar(&once, "once", false, "perform one scan then exit")
	c.AddCommand(status, list, show, observe, ack, propose, rate, deliver, watch, snapshot, feedback, history, decision, reviewAck, rt.operatorMessagesCommand(store))
	return c
}
