package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Behaviour checks for the turn triage (#2586): testdata/judge/triage.json holds written-out stopped turns with where
// the hub should say they stand. TestTriageScenariosAreWellFormed runs everywhere and keeps the cases consistent with
// the hub's own rules; TestTriageJudgeLive asks a real model and runs only when PERSONA_LIVE_AI is set:
//
//	PERSONA_LIVE_AI='{"base_url":"…","api_key_file":"…","model":"…"}' go test -run TriageJudgeLive -v ./internal/assistant
//
// docs/assistant/prompts.md ("Behaviour checks") says when to run it.

const triageUnsure = "unsure" // a work state with no phase

// triageCaseStates are the states a case may want: done, unsure, or blocked on what it waits for.
var triageCaseStates = []string{proto.WorkDone, triageUnsure, proto.WorkBlocked + "/" + proto.WaitUser,
	proto.WorkBlocked + "/" + proto.WaitAgent, proto.WorkBlocked + "/" + proto.WaitExternal}

type triageScenario struct {
	Name   string `json:"name"`
	Agent  string `json:"agent"`
	Ask    string `json:"ask"`
	Reply  string `json:"reply"`
	Others []struct {
		ID    string `json:"id"`
		Label string `json:"label"`
		Line  string `json:"line"`
	} `json:"others"`
	Answered     []prompts.AgentAnswerItem `json:"answered"`
	ApprovedWait string                    `json:"approved_wait"`
	ApprovedThen string                    `json:"approved_then"`
	Want         []string                  `json:"want"`
	Question     string                    `json:"question"`
	NoQuestion   bool                      `json:"no_question"`
	Link         string                    `json:"link"`
	WaitsFor     string                    `json:"waits_for"`
	Risky        bool                      `json:"risky"`
	Same         bool                      `json:"same"`
	Known        string                    `json:"known"` // why the hub gets this case wrong today: a miss the live check reports, not fails on
}

// judgeInput is sc as triageTurnModel reads it.
func (sc triageScenario) judgeInput() triageJudgeInput {
	in := prompts.AgentTriageInput{Now: "2026-11-10 10:00 Tue", Agent: sc.Agent, Machine: "mac", Ask: sc.Ask,
		Reply: clipMiddleRunes(sc.Reply, 1500), Answered: sc.Answered, ApprovedWait: sc.ApprovedWait, ApprovedThen: sc.ApprovedThen}
	others := map[string]proto.WorkAgent{}
	for _, o := range sc.Others {
		in.Others = append(in.Others, prompts.AgentTriageOther{ID: o.ID, Label: o.Label, Line: o.Line})
		others[o.ID] = proto.WorkAgent{MachineID: "mac", SessionID: o.ID, Label: o.Label}
	}
	return triageJudgeInput{System: prompts.AgentTriage{Owner: "Alex"}, Input: in, Reply: sc.Reply, Others: others, Lang: i18n.ZhHans}
}

// triageState is w as a case names it.
func triageState(w *proto.WorkState) string {
	switch {
	case w == nil:
		return ""
	case w.Phase == "":
		return triageUnsure
	case w.Phase == proto.WorkBlocked:
		return w.Phase + "/" + w.On
	}
	return w.Phase
}

// mismatch says how w (and same) misses sc's expectation; "" when it meets it.
func (sc triageScenario) mismatch(w *proto.WorkState, same bool) string {
	var miss []string
	if got := triageState(w); !slices.Contains(sc.Want, got) {
		miss = append(miss, fmt.Sprintf("%s, want %s", got, strings.Join(sc.Want, " or ")))
	}
	if w == nil {
		return strings.Join(miss, "; ")
	}
	if sc.Question != "" && len(w.Questions) == 0 {
		miss = append(miss, "the card asks nothing")
	}
	if sc.NoQuestion && len(w.Questions) > 0 {
		miss = append(miss, fmt.Sprintf("the card asks %q", w.Questions[0].Question))
	}
	if sc.Link != "" && (w.Link == nil || w.Link.URL != sc.Link) {
		miss = append(miss, "no link "+sc.Link)
	}
	if sc.WaitsFor != "" && (w.Agent == nil || w.Agent.SessionID != sc.WaitsFor) {
		miss = append(miss, "not waiting for "+sc.WaitsFor)
	}
	if sc.ApprovedThen != "" && same != sc.Same {
		miss = append(miss, fmt.Sprintf("same as the approved step: %v, want %v", same, sc.Same))
	}
	return strings.Join(miss, "; ")
}

func loadTriageScenarios(t *testing.T) []triageScenario {
	t.Helper()
	f, err := os.Open("testdata/judge/triage.json")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var file struct {
		About     string           `json:"about"`
		Scenarios []triageScenario `json:"scenarios"`
	}
	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields() // a misspelt field would otherwise mean nothing, silently
	if err := dec.Decode(&file); err != nil {
		t.Fatal(err)
	}
	return file.Scenarios
}

// triageAnswerJSON is a model's answer that says what sc expects first: its phase and what it waits for, the
// request, link and agent it names, and a red-line next step as the model marks it (wait.risky).
func triageAnswerJSON(sc triageScenario) string {
	phase, on, _ := strings.Cut(sc.Want[0], "/")
	out := map[string]any{"phase": phase, "line": "as expected"}
	if on != "" {
		out["on"] = on
	}
	wait := map[string]any{"what": "as expected", "then": "as expected", "check_minutes": 10, "agent": sc.WaitsFor,
		"risky": sc.Risky, "approved": sc.Same}
	if sc.Risky {
		// The model sees a wait; the hub makes it the user's because of what comes after it.
		out["on"] = proto.WaitExternal
		if sc.WaitsFor != "" {
			out["on"] = proto.WaitAgent
		}
	}
	if out["on"] == proto.WaitAgent || out["on"] == proto.WaitExternal {
		out["wait"] = wait
	}
	if sc.Question != "" {
		out["questions"] = []map[string]any{{"question": sc.Question, "header": "as expected",
			"options": []map[string]string{{"label": "yes"}, {"label": "no"}}}}
	}
	if sc.Link != "" {
		out["link"] = map[string]any{"url": sc.Link, "minutes": 10}
	}
	b, _ := json.Marshal(out)
	return string(b)
}

// TestTriageScenariosAreWellFormed keeps the cases honest without a model: names are unique, states are ones the
// hub gives, no reply is decided from its closeout before the model, a request a case expects is one the hub keeps
// (copied from the reply), and a model answering as the case expects leaves the hub with that
// state after its own rules (a red-line next step keeps the wait the user's, an agent it names must be one it knows).
func TestTriageScenariosAreWellFormed(t *testing.T) {
	scenarios := loadTriageScenarios(t)
	if len(scenarios) < 15 {
		t.Fatalf("%d scenarios, want at least 15", len(scenarios))
	}
	seen := map[string]bool{}
	for _, sc := range scenarios {
		t.Run(sc.Name, func(t *testing.T) {
			if sc.Name == "" || seen[sc.Name] || strings.TrimSpace(sc.Reply) == "" || len(sc.Want) == 0 {
				t.Fatalf("needs a unique name, a reply and what it wants: %+v", sc)
			}
			seen[sc.Name] = true
			for _, w := range sc.Want {
				if !slices.Contains(triageCaseStates, w) {
					t.Fatalf("want %q, not one of %v", w, triageCaseStates)
				}
			}
			if _, _, ok := closeout.Parse(sc.Reply); ok {
				t.Fatal("the reply has a closeout: the hub decides it without the model")
			}
			onlyUser := !slices.ContainsFunc(sc.Want, func(w string) bool { return w != proto.WorkBlocked+"/"+proto.WaitUser })
			if sc.Question != "" {
				if !onlyUser || sc.NoQuestion {
					t.Fatal("a question is for a case that wants blocked/user only, and asks something")
				}
				if strings.TrimSpace(sc.Question) == "" || !strings.Contains(sc.Reply, sc.Question) {
					t.Fatalf("question %q must be copied from the reply", sc.Question)
				}
			}
			if sc.Link != "" && (!onlyUser || !strings.Contains(sc.Reply, sc.Link) || !httpsLink(sc.Link)) {
				t.Fatalf("link %q: an https link in the reply, for a case that wants blocked/user only", sc.Link)
			}
			ids := map[string]bool{}
			for _, o := range sc.Others {
				if o.ID == "" || ids[o.ID] {
					t.Fatalf("other agents need unique ids: %+v", sc.Others)
				}
				ids[o.ID] = true
			}
			if sc.WaitsFor != "" && (!ids[sc.WaitsFor] || !slices.Contains(sc.Want, proto.WorkBlocked+"/"+proto.WaitAgent)) {
				t.Fatalf("waits_for %q: one of the other agents, in a case that wants blocked/agent", sc.WaitsFor)
			}
			if (sc.ApprovedThen == "") != (sc.ApprovedWait == "") || sc.Same && sc.ApprovedThen == "" {
				t.Fatal("an approval names both what it waits for and the step after it; same needs one")
			}
			if sc.Known != "" && strings.TrimSpace(sc.Known) == "" {
				t.Fatal("known says why the hub misses it")
			}
			if sc.Risky && sc.Want[0] != proto.WorkBlocked+"/"+proto.WaitUser {
				t.Fatal("a red-line next step leaves the hub with blocked/user first")
			}

			answer := triageAnswerJSON(sc)
			w, same, err := judgeTriageWith(context.Background(), &cannedJSON{answer: answer}, sc.judgeInput())
			if err != nil {
				t.Fatal(err)
			}
			if got := triageState(w); got != sc.Want[0] {
				t.Fatalf("a model answering %s leaves the hub with %s, not the case's first %s", answer, got, sc.Want[0])
			}
			if miss := sc.mismatch(w, same); miss != "" {
				t.Fatalf("a model answering %s: %s", answer, miss)
			}
		})
	}
}

// judgeTriageWith asks the model with the turn as read, grounds its questions in the whole reply, and says the
// wait is the approved step only when there is one.
func TestTriageJudgeModelInput(t *testing.T) {
	ask := "I'll need your pick between the two palettes."
	long := strings.Repeat("Before. ", 200) + ask + strings.Repeat(" After.", 250)
	j := triageJudgeInput{System: prompts.AgentTriage{Owner: "Alex"}, Reply: long, Lang: i18n.En,
		Input: prompts.AgentTriageInput{Now: "2026-11-10 10:00 Tue", Agent: "Copy", Machine: "mac", Reply: clipMiddleRunes(long, 1500)}}
	if strings.Contains(j.Input.Reply, ask) {
		t.Fatal("the request must sit outside the model's clipped reply")
	}
	m := &cannedJSON{answer: `{"phase":"blocked","on":"user","line":"Pick a draft","questions":[` +
		`{"question":"` + ask + `","options":[{"label":"A"},{"label":"B"}]},` +
		`{"question":"Shall I ship it?","options":[{"label":"Yes"},{"label":"No"}]},` +
		`{"question":""},{"question":" \t\n"}],` +
		`"wait":{"what":"x","then":"push","approved":true}}`}
	w, same, err := judgeTriageWith(context.Background(), m, j)
	if err != nil {
		t.Fatal(err)
	}
	if m.calls != 1 || m.system != prompts.RenderAgentTriage(j.System) || m.user != prompts.RenderAgentTriageInput(j.Input) {
		t.Fatalf("the model was asked %d times with system %q user %q", m.calls, m.system, m.user)
	}
	if triageState(w) != "blocked/user" || len(w.Questions) != 1 || w.Questions[0].Question != ask {
		t.Fatalf("got %+v", w)
	}
	if same {
		t.Fatal("same with no approved step")
	}
	j.Input.ApprovedWait, j.Input.ApprovedThen = "CI", "push"
	if _, same, _ := judgeTriageWith(context.Background(), m, j); !same {
		t.Fatal("not same with the approved step")
	}

	// An answer it cannot read is unsure; the model failing is an error.
	if w, _, err := judgeTriageWith(context.Background(), &cannedJSON{answer: `{"phase":"finished"}`}, j); err != nil || triageState(w) != triageUnsure {
		t.Fatalf("a made-up phase: %+v %v", w, err)
	}
	if w, _, err := judgeTriageWith(context.Background(), &cannedJSON{err: errors.New("no model")}, j); err == nil || w != nil {
		t.Fatalf("a failing model: %+v %v", w, err)
	}
}

// TestTriageJudgeLive asks a real model (PERSONA_LIVE_AI) about every case and prints what it decided; a case it gets
// wrong fails, unless the case records it as a known miss. Run it before and after changing triage.md /
// triage_input.md, judgeTriageWith or triageWork, or the model behind agent.triage, and put both tables in the PR.
func TestTriageJudgeLive(t *testing.T) {
	raw := os.Getenv("PERSONA_LIVE_AI")
	if raw == "" {
		t.Skip("PERSONA_LIVE_AI not set")
	}
	var cfg ai.Config
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	runTriageLive(t, ai.New(cfg), cfg.Model)
}

// runTriageLive judges every case with m (named model in the table) and fails on each case it gets wrong that is
// not a known miss.
func runTriageLive(t *testing.T, m jsonModel, model string) {
	t.Helper()
	var rows []string
	failed := 0
	for _, sc := range loadTriageScenarios(t) {
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		start := time.Now()
		w, same, err := judgeTriageWith(ctx, m, sc.judgeInput())
		cancel()
		got, line := triageState(w), ""
		if w != nil {
			if len(w.Questions) > 0 {
				got += fmt.Sprintf(" (%d asked)", len(w.Questions))
			}
			if same {
				got += " same"
			}
			line = w.Line
		}
		miss := sc.mismatch(w, same)
		if err != nil {
			got, miss = "error", "error: "+err.Error()
		}
		mark := liveMark(t, sc.Name, miss, sc.Known, line)
		if mark == "MISS" {
			failed++
		}
		rows = append(rows, fmt.Sprintf("%s %-28s %-26s want %-34s %5.1fs %s", mark, sc.Name, got, strings.Join(sc.Want, "|"),
			time.Since(start).Seconds(), line))
	}
	t.Logf("triage on %s: %d of %d as expected or known\n%s", model, len(rows)-failed, len(rows), strings.Join(rows, "\n"))
}
