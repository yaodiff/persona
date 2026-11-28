package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Behaviour checks for the done judge (#2586): testdata/judge/task_done.json holds written-out tasks with the
// verdict the hub should end with. TestTaskDoneScenariosAreWellFormed runs everywhere and keeps the cases consistent
// with the hub's own rules; TestTaskDoneJudgeLive asks a real model and runs only when PERSONA_LIVE_AI is set:
//
//	PERSONA_LIVE_AI='{"base_url":"…","api_key_file":"…","model":"…"}' go test -run TaskDoneJudgeLive -v ./internal/assistant
//
// docs/assistant/prompts.md ("Behaviour checks") says when to run it.

type taskDoneScenario struct {
	Name     string   `json:"name"`
	Title    string   `json:"title"`
	Notes    string   `json:"notes"`
	DoneWhen []string `json:"done_when"`
	Delivery bool     `json:"delivery"`
	Replies  []struct {
		Machine string `json:"machine"`
		Ask     string `json:"ask"`
		Text    string `json:"text"`
		From    string `json:"from"`
		Source  string `json:"source"`
	} `json:"replies"`
	SideTurnBlocked bool     `json:"side_turn_blocked"`
	Early           bool     `json:"early"`
	Verdicts        []string `json:"verdicts"`
	DeliveryState   string   `json:"delivery_state"`
}

func (sc taskDoneScenario) input() taskDoneInput {
	in := taskDoneInput{Owner: "Alex", Delivery: sc.Delivery, Title: sc.Title, Notes: sc.Notes, Criteria: sc.DoneWhen}
	for _, r := range sc.Replies {
		in.Replies = append(in.Replies, taskDoneReply{TaskDoneReply: prompts.TaskDoneReply{Machine: r.Machine, Ask: r.Ask, Text: r.Text, From: r.From, Source: r.Source}})
	}
	return in
}

// judge runs the done judge on sc as the hub does: the verdicts given without a model first, then m.
func (sc taskDoneScenario) judge(ctx context.Context, m jsonModel) (taskDoneCheck, error) {
	in := sc.input()
	if c, ok := taskDoneWithoutModel(in.Replies, sc.SideTurnBlocked); ok {
		return c, nil
	}
	return judgeTaskDoneWith(ctx, m, in)
}

// mismatch says how c misses sc's expectation; "" when it meets it.
func (sc taskDoneScenario) mismatch(c taskDoneCheck) string {
	var miss []string
	if !slices.Contains(sc.Verdicts, c.Verdict) {
		miss = append(miss, fmt.Sprintf("verdict %q, want one of %v", c.Verdict, sc.Verdicts))
	}
	if sc.DeliveryState != "" {
		got := ""
		if c.Delivery != nil {
			got = c.Delivery.State
		}
		if got != sc.DeliveryState {
			miss = append(miss, fmt.Sprintf("delivery %q, want %q", got, sc.DeliveryState))
		}
	}
	return strings.Join(miss, "; ")
}

func loadTaskDoneScenarios(t *testing.T) []taskDoneScenario {
	t.Helper()
	f, err := os.Open("testdata/judge/task_done.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var file struct {
		About     string             `json:"about"`
		Scenarios []taskDoneScenario `json:"scenarios"`
	}
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields() // a misspelt field would otherwise mean nothing, silently
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	return file.Scenarios
}

// TestTaskDoneScenariosAreWellFormed keeps the cases honest without a model: names are unique, verdicts and delivery
// states are ones the hub gives, early cases are decided before the model and the others are not, and a model that
// answered as each case expects would leave the hub with that verdict and delivery (the hub's own rules on top of the
// model's answer: undelivered, the agent's delivery line, a PR named without a deploy).
func TestTaskDoneScenariosAreWellFormed(t *testing.T) {
	scenarios := loadTaskDoneScenarios(t)
	if len(scenarios) < 15 {
		t.Fatalf("%d scenarios, want at least 15", len(scenarios))
	}
	verdicts := []string{verdictDone, verdictNotDone, verdictWaiting, verdictNeedsUser, verdictUnsure, verdictUndelivered}
	states := []string{deliveryLive, deliveryPR, deliveryMerged, deliveryOff, deliveryNone}
	seen := map[string]bool{}
	for _, sc := range scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			if sc.Name == "" || seen[sc.Name] || strings.TrimSpace(sc.Title) == "" || len(sc.Verdicts) == 0 {
				t.Fatalf("needs a unique name, a title and at least one verdict: %+v", sc)
			}
			seen[sc.Name] = true
			for _, v := range sc.Verdicts {
				if !slices.Contains(verdicts, v) || v == verdictUndelivered && !sc.Delivery {
					t.Fatalf("verdict %q is not one the hub gives here", v)
				}
			}
			if sc.DeliveryState != "" && (!sc.Delivery || !slices.Contains(states, sc.DeliveryState)) {
				t.Fatalf("delivery_state %q needs delivery on and one of %v", sc.DeliveryState, states)
			}
			in := sc.input()
			c, early := taskDoneWithoutModel(in.Replies, sc.SideTurnBlocked)
			if early != sc.Early {
				t.Fatalf("decided without the model: %v, the case says early %v", early, sc.Early)
			}
			if early {
				if miss := sc.mismatch(c); miss != "" {
					t.Fatal(miss)
				}
				return
			}
			// A model that answers as the case expects: its first verdict (done, for undelivered) and the delivery.
			want := sc.Verdicts[0]
			if want == verdictUndelivered {
				want = verdictDone
			}
			answer, _ := json.Marshal(map[string]string{"verdict": want, "reason": "as expected", "delivery": sc.DeliveryState})
			m := &cannedJSON{answer: string(answer)}
			c, err := sc.judge(context.Background(), m)
			if err != nil {
				t.Fatal(err)
			}
			if c.Verdict != sc.Verdicts[0] {
				t.Fatalf("a model answering %s leaves the hub with %q, not the case's first verdict %q", answer, c.Verdict, sc.Verdicts[0])
			}
			if miss := sc.mismatch(c); miss != "" {
				t.Fatalf("a model answering %s: %s", answer, miss)
			}
		})
	}
}

// TestTaskDoneJudgeLive asks a real model (PERSONA_LIVE_AI) about every case and prints what it decided; a case it
// gets wrong fails. Run it before and after changing task_done.md / task_done_input.md, or the done judge's model, and
// put both tables in the PR.
func TestTaskDoneJudgeLive(t *testing.T) {
	raw := os.Getenv("PERSONA_LIVE_AI")
	if raw == "" {
		t.Skip("PERSONA_LIVE_AI not set")
	}
	var cfg ai.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	runTaskDoneLive(t, ai.New(cfg), cfg.Model)
}

// runTaskDoneLive judges every case with m (named model in the table) and fails on each case it gets wrong.
func runTaskDoneLive(t *testing.T, m jsonModel, model string) {
	t.Helper()
	var rows []string
	failed := 0
	for _, sc := range loadTaskDoneScenarios(t) {
		ctx, cancel := context.WithTimeout(context.Background(), taskDoneTimeout)
		start := time.Now()
		c, err := sc.judge(ctx, m)
		cancel()
		got := c.Verdict
		if c.Delivery != nil {
			got += "/" + c.Delivery.State
		}
		miss := sc.mismatch(c)
		if err != nil {
			miss = "error: " + err.Error()
		}
		mark := "ok  "
		if miss != "" {
			mark = "MISS"
			failed++
			t.Errorf("%s: %s (reason %q)", sc.Name, miss, c.Reason)
		}
		want := strings.Join(sc.Verdicts, "|")
		if sc.DeliveryState != "" {
			want += "/" + sc.DeliveryState
		}
		rows = append(rows, fmt.Sprintf("%s %-32s %-20s want %-34s %5.1fs %s", mark, sc.Name, got, want,
			time.Since(start).Seconds(), c.Reason))
	}
	t.Logf("done judge on %s: %d of %d as expected\n%s", model, len(rows)-failed, len(rows), strings.Join(rows, "\n"))
}
