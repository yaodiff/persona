package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A waiting ask_user lets go of its turn when a message from elsewhere waits for its conversation (#7392,
// ask_release.go). All ids are placeholders.

func releaseAfter(t *testing.T, d time.Duration) {
	t.Helper()
	old := askReleaseAfter
	askReleaseAfter = d
	t.Cleanup(func() { askReleaseAfter = old })
}

// askHeld has c1x1x1 ask with ask_user (waiting, the default) while its turn runs, and returns the card and where the
// call's result comes.
func (e *askTodoEnv) askHeld(ctx context.Context) (proto.Interaction, chan string) {
	e.t.Helper()
	e.sessions("server", proto.Session{SessionID: "m1", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "c1x1x1", Title: "Release chat", Assistant: true, Status: proto.StatusWorking})
	done := make(chan string, 1)
	go func() {
		text, err := e.s.toolAskUser(ctx, "server", "c1x1x1", json.RawMessage(strings.Replace(askLaterArgs, `"wait":false,`, "", 1)))
		if err != nil {
			text = "error: " + err.Error()
		}
		done <- text
	}()
	var ix proto.Interaction
	waitFor(e.t, "the question out", func() bool {
		if qs := e.s.pendingQuestions(); len(qs) == 1 {
			ix = qs[0]
			return true
		}
		return false
	})
	if ix.Resumable {
		e.t.Fatalf("a waiting question's card is resumable: %+v", ix)
	}
	return ix, done
}

// send is conversation_send from the main conversation to c1x1x1.
func (e *askTodoEnv) send(ctx context.Context, text string) string {
	e.t.Helper()
	out, err := e.s.toolConversationSend(ctx, "m1", json.RawMessage(`{"id":"c1x1x1","text":"`+text+`"}`))
	if err != nil {
		e.t.Fatal(err)
	}
	return out
}

// A conversation other than the main one waits on its question; the main conversation sends it a message. The question
// lets go of the turn: the call returns saying the card stays up and the answer comes later, the card is the same one
// (now resumable, nothing asked again), the message goes in once the turn ends, and the owner's answer on the card then
// comes to the conversation as one message, as for a question asked with wait false.
func TestAWaitingQuestionLetsGoOfItsTurnForAMessageFromAnotherConversation(t *testing.T) {
	releaseAfter(t, 0)
	e := newAskTodoEnv(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ix, done := e.askHeld(ctx)
	if out := e.send(ctx, "the numbers are in"); !strings.Contains(out, "queued") {
		t.Fatalf("conversation_send to a running conversation: %q", out)
	}
	var text string
	select {
	case text = <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the question still holds the turn with a message waiting for it")
	}
	for _, want := range []string{ix.ID, "no longer holds this turn", "End this turn", "new message", "not asked again"} {
		if !strings.Contains(text, want) {
			t.Errorf("tool result lacks %q: %q", want, text)
		}
	}
	qs := e.s.pendingQuestions()
	if len(qs) != 1 || qs[0].ID != ix.ID || !qs[0].Resumable || !qs[0].AskedWithoutWaiting() || !qs[0].ExpiresAt.Equal(ix.ExpiresAt) {
		t.Fatalf("the card after letting go: %+v, want the same one, resumable", qs)
	}
	if !e.s.askLaterSpokeOrKept(ix.ID) {
		t.Fatal("no record kept for the question: a restart would lose its answer")
	}
	if got := e.told("c1x1x1"); len(got) != 0 {
		t.Fatalf("told while the turn still runs: %q", got)
	}
	// The turn ends: the waiting message goes in.
	e.sessions("server", proto.Session{SessionID: "m1", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "c1x1x1", Title: "Release chat", Assistant: true, Status: proto.StatusIdle})
	waitFor(t, "the message from the main conversation", func() bool {
		got := e.told("c1x1x1")
		return len(got) == 1 && strings.Contains(got[0], "the numbers are in")
	})
	// The owner answers the card later: one message that quotes the question and the answer.
	e.sessions("server", proto.Session{SessionID: "m1", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "c1x1x1", Title: "Release chat", Assistant: true, Status: proto.StatusIdle})
	e.mustEnd(ix.ID, `{"answers":{"Merge the release PR now?":"Wait"}}`)
	waitFor(t, "the answer as a message", func() bool { return len(e.told("c1x1x1")) == 2 })
	if got := e.told("c1x1x1")[1]; !strings.Contains(got, "Merge the release PR now? → Wait") {
		t.Fatalf("the answer's message:\n%s", got)
	}
	if qs := e.s.pendingQuestions(); len(qs) != 0 {
		t.Fatalf("the question is still out after its answer was told: %+v", qs)
	}
	if e.s.askLaterSpokeOrKept(ix.ID) {
		t.Fatal("the record outlived the answer")
	}
}

// A question keeps its turn for askReleaseAfter, so a confirmation the owner answers within a minute or two is not cut
// short; it lets go once it has waited that long and the message still waits.
func TestAWaitingQuestionKeepsItsTurnForAWhile(t *testing.T) {
	releaseAfter(t, 400*time.Millisecond)
	e := newAskTodoEnv(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ix, done := e.askHeld(ctx)
	e.send(ctx, "the numbers are in")
	select {
	case text := <-done:
		t.Fatalf("let go at once: %q", text)
	case <-time.After(150 * time.Millisecond):
	}
	select {
	case text := <-done:
		if !strings.Contains(text, "no longer holds this turn") {
			t.Fatalf("tool result %q", text)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("never let go of the turn")
	}
	if since := time.Since(ix.CreatedAt); since < askReleaseAfter {
		t.Fatalf("let go after %v, before %v", since, askReleaseAfter)
	}
}

// A question answered before anything waits for its conversation is the call's answer, as ever; and with nothing
// waiting it never lets go.
func TestAWaitingQuestionWithNothingWaitingKeepsItsTurn(t *testing.T) {
	releaseAfter(t, 0)
	e := newAskTodoEnv(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ix, done := e.askHeld(ctx)
	select {
	case text := <-done:
		t.Fatalf("returned with nothing waiting: %q", text)
	case <-time.After(150 * time.Millisecond):
	}
	e.mustEnd(ix.ID, `{"answers":{"Merge the release PR now?":"Wait"}}`)
	if text := <-done; !strings.Contains(text, "The user answered") {
		t.Fatalf("tool result %q", text)
	}
	if got := e.told("c1x1x1"); len(got) != 0 {
		t.Fatalf("a waiting question's answer was sent as a message too: %q", got)
	}
}

// A question whose card the history no longer has as pending (answered or ended meanwhile) keeps its turn: nothing
// would keep its card across a restart. The message waits behind the turn as before.
func TestAQuestionWhoseCardCannotBeKeptKeepsItsTurn(t *testing.T) {
	releaseAfter(t, 0)
	e := newAskTodoEnv(t, newFleetMem()) // the card was never recorded: not pending there
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ix, done := e.askHeld(ctx)
	e.send(ctx, "the numbers are in")
	select {
	case text := <-done:
		t.Fatalf("let go of the turn with a card that can't be kept: %q", text)
	case <-time.After(200 * time.Millisecond):
	}
	if e.s.askLaterSpokeOrKept(ix.ID) {
		t.Fatal("a record was left for a question that kept its turn")
	}
	e.mustEnd(ix.ID, `{"answers":{"Merge the release PR now?":"Wait"}}`)
	if text := <-done; !strings.Contains(text, "The user answered") {
		t.Fatalf("tool result %q", text)
	}
}

// Only ask_user lets go: a confirmation card another tool waits on (a to-do change, persona_settings) keeps its call.
func TestOtherCardsKeepTheirTurn(t *testing.T) {
	releaseAfter(t, 0)
	e := newAskTodoEnv(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	e.sessions("server", proto.Session{SessionID: "m1", Assistant: true, Status: proto.StatusIdle},
		proto.Session{SessionID: "c1x1x1", Title: "Release chat", Assistant: true, Status: proto.StatusWorking})
	now := time.Now().UTC()
	ix := proto.Interaction{ID: "ix_confirm1", MachineID: "server", SessionID: "c1x1x1", Kind: proto.IxPermission,
		ToolName: "todo_delete", Summary: "Delete a to-do", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}
	done := make(chan error, 1)
	go func() {
		_, err := e.s.ask(ctx, ix)
		done <- err
	}()
	waitFor(t, "the card out", func() bool { return len(e.s.pendingQuestions()) == 1 })
	e.send(ctx, "the numbers are in")
	select {
	case err := <-done:
		t.Fatalf("a confirmation card let go of its call: %v", err)
	case <-time.After(200 * time.Millisecond):
	}
	yes := true
	answer, _ := json.Marshal(proto.Resolution{Allow: &yes})
	e.mustEnd(ix.ID, string(answer))
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// On the manager's own interaction queue the card of a question that let go of its turn stays the one card: not pushed
// again, made resumable on the queue and in the history (so its node's connection ending, or a restart, does not end
// it), shown to the hub again as such; an answer given on the queue reaches the conversation as the message, and a
// manager started on the same history takes the card back.
func TestALetGoQuestionsCardOutlivesItsTurnOnTheQueue(t *testing.T) {
	releaseAfter(t, 0)
	a := newAskRig(t)
	ix, done := a.ask(t)
	waitFor(t, "the card's push", func() bool { return len(a.f.pushed()) == 1 })
	a.s.queueRelay("s1", relayMsg{From: "m1", FromTitle: "Persona", Tool: "conversation_send", Text: "the numbers are in",
		At: time.Now().UTC()})
	if text := a.result(t, done); !strings.Contains(text, "no longer holds this turn") {
		t.Fatalf("tool result %q", text)
	}
	if card, up := a.s.pendingIx(ix.ID); !up || !card.Resumable || !card.AskedWithoutWaiting() {
		t.Fatalf("the card on the queue: %+v, %v; want it up and resumable", card, up)
	}
	recs, _ := a.mem.Interactions(context.Background(), "mac", "s1", 10)
	if len(recs) != 1 || recs[0].Interaction.ID != ix.ID || !recs[0].Interaction.Resumable || recs[0].Status != "pending" {
		t.Fatalf("history %+v: want the one card, pending and resumable", recs)
	}
	shownResumable := func() bool {
		a.f.mu.Lock()
		defer a.f.mu.Unlock()
		for _, e := range a.f.told {
			var p struct {
				Interaction proto.Interaction `json:"interaction"`
			}
			if json.Unmarshal(e.params, &p) == nil && p.Interaction.ID == ix.ID && p.Interaction.Resumable {
				return true
			}
		}
		return false
	}
	waitFor(t, "the hub told the card is resumable", shownResumable)
	underWay := pushesUnderWay()
	a.s.NodeDetached("mac")
	if _, up := a.s.pendingIx(ix.ID); !up {
		t.Fatal("the node's connection ending took the card down")
	}
	// NodeDetached returns once its own work is done, but a push it decided on goes out on a goroutine (deliver): every
	// push still on its way has to land before the count says anything. This relies on NodeDetached deciding its pushes
	// before it returns; a push handed to another long-lived goroutine (over a channel, say) would not be counted here.
	waitFor(t, "the pushes under way to land", func() bool { return underWay() == 0 })
	if n := len(a.f.pushed()); n != 1 {
		t.Fatalf("%d pushes, want the card's one", n)
	}

	next := withSettings(t, a.f, New(Options{Hub: a.f, Store: func() Store { return a.mem }, Logf: testLogf(t), Devices: hookDevs{a.d, a.nudgeRig}}))
	p := &pending{ix: recs[0].Interaction, done: make(chan proto.Resolution, 1)} // as restoreInteractions takes it back
	next.fleet.mu.Lock()
	next.fleet.interactions = map[string]*pending{ix.ID: p}
	next.orphanLocked(p)
	next.fleet.mu.Unlock()
	next.publish(context.Background())
	waitFor(t, "the card claimed by the next manager", func() bool {
		next.fleet.mu.Lock()
		defer next.fleet.mu.Unlock()
		return p.orphaned == nil
	})
	if err := next.resolve(ix.ID, proto.Resolution{Answers: map[string]string{"Push it to beta?": "Yes"}}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the answer on its way to the conversation", func() bool {
		b := &next.relays
		b.mu.Lock()
		defer b.mu.Unlock()
		if !next.relayLocked(context.Background()) {
			return false
		}
		for _, m := range b.st.Queue["s1"] {
			if m.Tool == askAnswerTool && strings.Contains(m.Text, "Push it to beta? → Yes") {
				return true
			}
		}
		return false
	})
	waitFor(t, "the question gone once its answer went", func() bool { return len(next.pendingQuestions()) == 0 })
}

// pushesUnderWay, called just before a call, counts the pushes the call set off that have not reached the hub yet:
// the goroutines deliver starts to hand one over, notifyInteraction's while it asks Persona Link first, and the ones
// the calling goroutine started since (a push sent off on one of the call's own). A goroutine is listed from its go
// statement until it returns, and a "created by" line names the function with the go statement (one with a go statement
// is never inlined), so once the call has returned and this is 0, every push it set off is in the hub's pushes. The
// functions are named through method expressions, so renaming one cannot leave this counting nothing.
func pushesUnderWay() func() int {
	var pushers []string
	for _, by := range []any{(*Service).deliver, (*Service).notifyInteraction} {
		pushers = append(pushers, "\ncreated by "+runtime.FuncForPC(reflect.ValueOf(by).Pointer()).Name()+" in goroutine ")
	}
	gs := goroutineStacks()
	before := map[string]bool{}
	for _, g := range gs {
		before[goroutineID(g)] = true
	}
	startedHere := " in goroutine " + goroutineID(gs[0]) + "\n" // the calling goroutine is the first
	return func() int {
		n := 0
		for _, g := range goroutineStacks() {
			pusher := strings.Contains(g, pushers[0]) || strings.Contains(g, pushers[1])
			if pusher || !before[goroutineID(g)] && strings.Contains(g, startedHere) {
				n++
			}
		}
		return n
	}
}

// goroutineStacks is every goroutine's stack (runtime.Stack), the calling goroutine's first.
func goroutineStacks() []string {
	buf := make([]byte, 1<<16)
	for {
		if n := runtime.Stack(buf, true); n < len(buf) {
			return strings.Split(string(buf[:n]), "\n\n")
		}
		buf = make([]byte, 2*len(buf))
	}
}

// goroutineID is the id a goroutine's stack starts with ("goroutine 7 [running]:").
func goroutineID(stack string) string {
	id, _, _ := strings.Cut(strings.TrimPrefix(stack, "goroutine "), " ")
	return id
}

// askLaterSpokeOrKept: a record of question id is kept among the questions asked without waiting (ask_later.go).
func (s *Service) askLaterSpokeOrKept(id string) bool {
	b := &s.askLaters
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, a := range b.list {
		if a.Interaction.ID == id {
			return a.Interaction.Resumable
		}
	}
	return false
}

func TestGoldenAskRelease(t *testing.T) {
	golden(t, "ask_release.tool_result", prompts.RenderQuestionReleased(prompts.QuestionAskedLater{ID: "ix_0a1b2c", Until: "11-08 17:04"}))
}
