package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestSupervisionTriageSettingsRequiresOwnerApproval(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	s := newTestService(t, newFakeHub().withMachine())
	call := func(args string) proto.ToolResult {
		return s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(args), MachineID: "server", SessionID: "c1"})
	}
	read := func(want string) {
		t.Helper()
		got := call(`{"action":"get","key":"assistant.supervision_triage"}`)
		if got.IsError || len(got.Content) == 0 || got.Content[0].Text != want {
			t.Fatalf("get supervision triage = %+v, want %s", got, want)
		}
	}
	read("false")
	done := make(chan proto.ToolResult, 1)
	go func() {
		done <- call(`{"action":"set","key":"assistant.supervision_triage","value":true}`)
	}()
	card, ok := awaitCard(s)
	if !ok || card.ToolName != "persona_settings" {
		t.Fatalf("owner approval card = %+v", card)
	}
	read("false")
	allow := true
	raw, _ := json.Marshal(proto.Resolution{Allow: &allow})
	if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: card.ID, Answer: raw}); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.IsError {
			t.Fatalf("approved setting change = %+v", got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	read("true")
	if !s.view(ctx, false).SupervisionTriage {
		t.Fatal("approved change did not reach the Assistant settings view")
	}
}

func TestRoutineSupervisionFiltersBeforeDelivery(t *testing.T) {
	for _, state := range []string{reviewUserCheck, reviewParked, reviewNeedsUser, "recent owner input", "expired owner input"} {
		t.Run(state, func(t *testing.T) {
			s, f, todo := newSupervisedService(t)
			stopSupervisionTimers(s)
			now := time.Now()
			switch state {
			case "recent owner input", "expired owner input":
				age := time.Second
				if state == "expired owner input" {
					age = ownerPresentFor + time.Second
				}
				a := todo.Agents[0]
				s.noteOwnerInstruction(t.Context(), SessionRef{MachineID: a.MachineID, SessionID: a.SessionID},
					"I am working through the findings here.", assistantwire.OwnerInputApp, "direct-message", now.Add(-age))
			default:
				todo.Supervision.State = state
				putTodo(s, todo)
			}
			for _, kind := range []string{sigStalled, sigInterrupted, sigFailed} {
				s.signal(superviseSignal{Todo: todo.ID, Kind: kind, Key: kind, At: now.Add(-time.Minute), Detail: "synthetic task signal"})
			}
			s.sendDigest(t.Context())
			got := strings.Join(f.sentTexts("conv1"), "\n")
			if !strings.Contains(got, "signal="+sigFailed) {
				t.Fatalf("launch failure did not reach the supervisor: %q", got)
			}
			for _, kind := range []string{sigStalled, sigInterrupted} {
				if gotSignal, want := strings.Contains(got, "signal="+kind), state == "expired owner input"; gotSignal != want {
					t.Errorf("%s delivered = %v, want %v: %q", kind, gotSignal, want, got)
				}
			}
		})
	}
}

func TestRoutineSupervisionSignalRules(t *testing.T) {
	now := time.Now()
	todo := Todo{ID: "todo_example", Supervision: &Supervision{State: reviewUserCheck}}
	for _, kind := range []string{sigOrphaned, sigEstimateDue, sigOverdue, sigStalled, sigNoProgress, sigIdle, sigInterrupted} {
		if !routineSupervisionSuppressed(todo, superviseSignal{Kind: kind}, now) {
			t.Errorf("%s can wake a task waiting for acceptance", kind)
		}
	}
	for _, g := range []superviseSignal{
		{Kind: sigReview}, {Kind: sigReport}, {Kind: sigAsk}, {Kind: sigNeedsYou}, {Kind: sigDecide},
		{Kind: sigFailed}, {Kind: sigEscalated}, {Kind: sigOwnerCannot}, {Kind: sigPRConflict},
		{Kind: sigNeedsUserExternalInfo}, {Kind: sigIdle, Handoff: true}, {Kind: sigPREvent, Report: prEventKindConflict},
	} {
		if routineSupervisionSuppressed(todo, g, now) || supervisionTriageEligible(g) {
			t.Errorf("%+v must take the direct path", g)
		}
	}
}

func TestSupervisionTriageDeliversWakeAndListsDeferral(t *testing.T) {
	s, f, todo := newSupervisedServiceOn(t, todoTestDB(t))
	stopSupervisionTimers(s)
	ctx := t.Context()
	var mu sync.Mutex
	var requests [][]supervisionTriageItem
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Messages []struct{ Content string }
		}
		var items []supervisionTriageItem
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || len(req.Messages) != 1 {
			t.Errorf("invalid model request: %+v, %v", req, err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if err := json.Unmarshal([]byte(req.Messages[0].Content), &items); err != nil {
			t.Errorf("invalid model input: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		mu.Lock()
		requests = append(requests, items)
		mu.Unlock()
		var verdicts []supervisionTriageVerdict
		for _, item := range items {
			if item.Signal == sigStalled {
				verdicts = append(verdicts, supervisionTriageVerdict{item.Number, supervisionWake, "A live build needs intervention."})
			} else {
				verdicts = append(verdicts, supervisionTriageVerdict{item.Number, supervisionDefer, "Can wait until the next briefing."})
			}
		}
		reply, _ := json.Marshal(map[string]any{"verdicts": verdicts})
		_ = json.NewEncoder(w).Encode(map[string]any{"content": []map[string]string{{"type": "text", "text": string(reply)}}})
	}))
	t.Cleanup(model.Close)
	f.mu.Lock()
	f.hello.Models.AI = ai.Config{BaseURL: model.URL, APIKey: "test-key", Model: "sorting-model"}
	f.hello.Locale = "en"
	f.mu.Unlock()
	get := getAssistantView(t, s)
	if get["supervision_triage"] != false {
		t.Fatalf("fresh setting: %v", get["supervision_triage"])
	}
	if code, view := putAssistant(t, s, `{"supervision_triage":true}`); code != http.StatusOK || view["supervision_triage"] != true {
		t.Fatalf("enable triage: %d, %v", code, view["supervision_triage"])
	}
	now := time.Now()
	wake := superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "build", At: now.Add(-time.Minute), Detail: "Build is stuck"}
	deferSignal := superviseSignal{Todo: todo.ID, Kind: sigUnjudged, Key: "idle", At: now.Add(-time.Minute), Detail: "An ordinary stop"}
	failed := superviseSignal{Todo: todo.ID, Kind: sigFailed, Key: "launch", At: now.Add(-time.Minute), Detail: "Start failed"}
	for _, g := range []superviseSignal{wake, deferSignal, failed} {
		s.signal(g)
	}
	s.sendDigest(ctx)
	got := strings.Join(f.sentTexts("conv1"), "\n")
	if !strings.Contains(got, "signal="+sigStalled) || !strings.Contains(got, "signal="+sigFailed) || strings.Contains(got, "signal="+sigUnjudged) {
		t.Fatalf("supervision delivery: %q", got)
	}
	mu.Lock()
	calls := slices.Clone(requests)
	mu.Unlock()
	if len(calls) != 1 || len(calls[0]) != 2 || slices.ContainsFunc(calls[0], func(i supervisionTriageItem) bool { return i.Signal == sigFailed }) {
		t.Fatalf("expected one cheap-model call without the direct exception: %+v", calls)
	}
	if !s.noteOwnerWait(ctx, todo, time.Now(), "Please confirm the release scope.") {
		t.Fatal("could not record the new owner decision")
	}
	s.sendDigest(ctx)
	if got := strings.Join(f.sentTexts("conv1"), "\n"); !strings.Contains(got, "Please confirm the release scope.") {
		t.Fatalf("a new owner decision did not reach the supervisor: %q", got)
	}
	mu.Lock()
	nCalls := len(requests)
	mu.Unlock()
	if nCalls != 1 {
		t.Fatalf("the owner decision was submitted to the sorting model, calls = %d", nCalls)
	}
	st := s.store().(supervisionDecisionStore)
	verdicts, err := st.SupervisionTriage(ctx, []string{supervisionTriageID(wake), supervisionTriageID(deferSignal)})
	if err != nil || verdicts[supervisionTriageID(wake)] != "wake" || verdicts[supervisionTriageID(deferSignal)] != "defer" {
		t.Fatalf("durable verdicts: %v, %v", verdicts, err)
	}
	needsBriefingModule(t)
	at := time.Now().Add(time.Second)
	brief, _ := s.briefingRunner()
	for _, kind := range []string{"morning", "evening"} {
		facts := brief.FactsAt(ctx, at, kind)
		if rows := brief.Body(facts, kind).DeferredSupervision; len(rows) != 1 || rows[0].TodoID.String() != todo.ID || rows[0].Reason != "Can wait until the next briefing." {
			t.Fatalf("%s briefing rows: %+v", kind, rows)
		}
		if text := brief.ModelText(facts); !strings.Contains(text, "Can wait until the next briefing.") {
			t.Fatalf("%s model facts omitted the saved decision: %s", kind, text)
		}
	}
	if code, view := putAssistant(t, s, `{"supervision_triage":false}`); code != http.StatusOK || view["supervision_triage"] != false {
		t.Fatalf("disable triage: %d, %v", code, view["supervision_triage"])
	}
}

func TestGoldenSupervisionTriageLanguages(t *testing.T) {
	for _, l := range []string{"en", "zh-Hans", "ja"} {
		golden(t, "supervision_triage."+l, prompts.RenderSupervisionTriage(answerLanguage(l)))
	}
}

func TestSupervisionTriageUnusableVerdictsDefer(t *testing.T) {
	for _, model := range []*cannedJSON{
		{err: errors.New("model timeout")},
		{answer: `{"verdicts":[{"number":1,"decision":"drop","reason":"discarded"}]}`},
		{answer: `{"verdicts":[{"number":1,"decision":"wake"}]}`},
		{answer: `{"verdicts":[{"number":1,"decision":"wake","reason":"first"},{"number":1,"decision":"defer","reason":"second"}]}`},
	} {
		got := triageSupervisionWith(t.Context(), model, i18n.Lang("en"), []supervisionTriageItem{{Number: 1, Signal: sigStalled}})
		if len(got) != 1 || got[0].Decision != supervisionDefer || got[0].Reason == "" {
			t.Errorf("unusable response %+v produced %+v", model, got)
		}
	}
}

func TestSupervisionTriageBriefingBacklogSurvivesAnInterruptedSchedule(t *testing.T) {
	st := todoTestDB(t)
	ctx := t.Context()
	at := time.Now().UTC()
	backlog := db.SupervisionTriageRow{
		ID: "backlog", TodoID: "todo_example", Recipient: "conv1", Signal: sigStalled,
		Decision: "defer", Reason: "Can wait", At: at.Add(-48 * time.Hour),
	}
	recent := backlog
	recent.ID, recent.At = "recent", at.Add(-time.Minute)
	if _, err := st.SaveSupervisionTriage(ctx, []db.SupervisionTriageRow{backlog, recent}); err != nil {
		t.Fatal(err)
	}
	changed := backlog
	changed.Decision, changed.Reason = "wake", "New verdict for the same generation"
	if saved, err := st.SaveSupervisionTriage(ctx, []db.SupervisionTriageRow{changed}); err != nil || saved[backlog.ID] != "defer" {
		t.Fatalf("retry overwrote the original verdict: %+v, %v", saved, err)
	}
	before, err := st.DeferredSupervision(ctx, at.Add(-24*time.Hour), at)
	if err != nil || len(before) != 2 || before[0].ID != backlog.ID || before[0].Reason != backlog.Reason {
		t.Fatalf("briefing dropped the missed backlog: %+v, %v", before, err)
	}
	if err := st.SupervisionBriefed(ctx, []string{backlog.ID, recent.ID}, at); err != nil {
		t.Fatal(err)
	}
	after, err := st.DeferredSupervision(ctx, at.Add(-24*time.Hour), at)
	if err != nil || len(after) != 1 || after[0].ID != recent.ID {
		t.Fatalf("next briefing did not retain the recent entry: %+v, %v", after, err)
	}
	saved, err := st.SupervisionTriage(ctx, []string{backlog.ID})
	if err != nil || saved[backlog.ID] != "defer" {
		t.Fatalf("briefing cleared the audit verdict: %+v, %v", saved, err)
	}
}
