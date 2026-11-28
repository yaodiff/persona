package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The background supervisor's rounds start clean (#7315, supervise_round.go). i18n-ignore: the supervision message's
// own words (prompts/assistant/supervise.md); an entry names its signal, the rules for it being the static supervision
// instructions (supervision_instructions.go).
const (
	roundHeader     = "这一轮通常从干净的上下文开始"
	roundHeaderLine = roundHeader + "：之前几轮的对话不一定在你眼前，跨轮要记的都在任务台账里（简报、范围调整、已定的事、上次结论），需要时用 todo_get 看。"
	roundIdle       = "signal=" + sigIdle
	roundReview     = "signal=" + sigReview
)

// newRoundService is newBackgroundService with a database in memory: putting the old background supervisor away
// archives it, which needs one.
func newRoundService(t *testing.T) (*Service, *fakeHub, Todo, Store) {
	t.Helper()
	st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
	s, f, todo := newSupervisedServiceOn(t, st)
	s.noBackground = false
	f.mu.Lock()
	f.made = 100
	f.mu.Unlock()
	return s, f, todo, st
}

// assistantSendsTo are the assistant.send params that went to conversation id, in order.
func assistantSendsTo(f *fakeHub, id string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, a := range f.sends {
		if a["session_id"] == id {
			out = append(out, a)
		}
	}
	return out
}

// lastSendTo is the last assistant.send to conversation id (nil: none).
func lastSendTo(f *fakeHub, id string) map[string]any {
	all := assistantSendsTo(f, id)
	if len(all) == 0 {
		return nil
	}
	return all[len(all)-1]
}

// wakeRound has explicitly addressed legacy signals gs go out now, as the digest's timer would. New task signals still
// default to main (#5732); these fixtures name the already configured background conversation to exercise old notices.
func wakeRound(s *Service, gs ...superviseSignal) {
	b := &s.supervise
	legacy := s.settings(context.Background()).BackgroundSupervisor
	b.mu.Lock()
	b.init()
	for _, g := range gs {
		if g.At.IsZero() {
			g.At = time.Now().Add(-time.Minute)
		}
		if g.To == "" {
			g.To = legacy
		}
		b.pending[g.id()] = g
		// These fixtures bypass signal, so establish the durable outbox entry that sendDigest requires before delivery.
		s.updateSupervisePending(func(pending map[string]superviseSignal) { pending[g.id()] = g })
	}
	b.last = time.Time{}
	b.mu.Unlock()
	s.sendDigest(context.Background())
}

// listRound has the assistant's machine list the main conversation idle and the background supervisor id in status
// (Starting when starting).
func listRound(f *fakeHub, id, status string, starting bool) []proto.Session {
	list := []proto.Session{{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle},
		{SessionID: id, MachineID: "server", Assistant: true, Status: status, Starting: starting}}
	f.setSessions("server", list...)
	return list
}

// withBackgroundBG1 has bg1 be the background supervisor on the assistant's machine, started an hour ago and idle.
func withBackgroundBG1(t *testing.T, s *Service, f *fakeHub) string {
	t.Helper()
	at := time.Now().Add(-time.Hour).UTC()
	setSettings(t, s, func(v *Settings) {
		v.BackgroundSupervisor, v.BackgroundSupervisorMachineID, v.BackgroundSupervisorAt = "bg1", "server", &at
	})
	f.mu.Lock()
	f.known["bg1"] = true
	f.mu.Unlock()
	listRound(f, "bg1", proto.StatusIdle, false)
	return "bg1"
}

// roundAnswers has the fake node answer each round's assistant.send with answer.
func roundAnswers(f *fakeHub, answer map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.assistantAnswer = func(a map[string]any) map[string]any {
		if a[superviseRoundParam] == true {
			return answer
		}
		return nil
	}
}

// outgrownOf is why background supervisor id is to be replaced ("" when it is not).
func outgrownOf(s *Service, id string) string {
	r := &s.background.rounds
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.outgrown[id]
}

// A round's message is the background supervisor's golden message (supervise.background, TestGoldenBackgroundSupervisor)
// with three lines more: the header after the role line, the marker after the level tag and the task's last review
// after its criteria. It is pinned against that golden rather than as a golden of its own: the supervision events'
// size ratchet (prompt_size_ratchet_test.go) counts every supervise.* golden, and a round adds no event text but these
// lines.
func TestGoldenBackgroundSupervisorFresh(t *testing.T) {
	whole, err := os.ReadFile(filepath.Join("testdata", "prompts", "supervise.background.golden"))
	if err != nil {
		t.Fatal(err)
	}
	got := prompts.RenderSupervise(prompts.Supervise{Leader: true, Background: true, Asked: true, AskMinutes: 5, Fresh: true,
		Entries: sortEntries([]prompts.SuperviseEntry{
			{ID: "t1", Title: "修登录崩溃", Kind: sigReview, Machine: "Mac", Session: "a1b2c3d4", DoneWhen: []string{"PR 合并到 main"}, Last: "做完了，PR 已合并",
				LastReview: &prompts.SuperviseLastReview{Verdict: reviewRework, At: "11-14 10:20", Note: "补深色截图"}},
			{ID: "t2", Title: "补测试", Kind: sigAsk, Asks: []prompts.SuperviseAsk{{ID: "ix1", Kind: proto.IxQuestion, Machine: "Mac", Session: "b2c3d4e5", Text: "用哪个分支？"}}},
		})})
	var want []string
	for _, line := range strings.Split(string(whole), "\n") {
		want = append(want, line)
		switch {
		case strings.HasPrefix(line, "（这是 Persona 的督办消息"):
			want = append(want, roundHeaderLine)
		case strings.HasPrefix(line, "<persona-supervision level="):
			want = append(want, prompts.SuperviseRoundMarker)
		case line == "   验收标准用 todo_get 取原文" && strings.Contains(want[len(want)-2], "任务「修登录崩溃」"):
			want = append(want, "   上次结论（11-14 10:20）：rework，补深色截图")
		}
	}
	if w := strings.Join(want, "\n"); got != w {
		t.Fatalf("the round's message:\n%s\n--- want ---\n%s", got, w)
	}
}

// By default each supervision message to the background supervisor is a round: rendered Fresh (the header, the
// marker) and sent with supervise_round. Its start is not, and neither is anything to the main
// conversation or a leader, nor another kind of message to the background supervisor. The task's last review comes
// along in the next round.
func TestBackgroundRoundStartsFresh(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	ctx := context.Background()
	if backgroundKeepsContext(s.settings(ctx)) {
		t.Fatal("the background supervisor keeps its context by default")
	}
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了，PR https://github.com/o/r/pull/7 已合并", 0)
	bg := withBackgroundBG1(t, s, f)
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "legacy-review"})
	waitFor(t, "the background supervisor's round", func() bool { return len(f.noticesTo(bg)) == 1 })
	send := assistantSendsTo(f, bg)[0]
	text, _ := send["text"].(string)
	if send[superviseRoundParam] != true || !prompts.IsSuperviseRound(text) || !strings.Contains(text, roundHeader) {
		t.Fatalf("the round: %v\n%s", send[superviseRoundParam], text)
	}
	if !strings.Contains(text, roundReview) || strings.Contains(text, roundIdle) || strings.Contains(text, "怎么处理") || strings.Contains(text, "上次结论（") {
		t.Fatalf("a review's round names another signal, carries rules, or a review it never had:\n%s", text)
	}
	for _, a := range conversationStarts(f) {
		if _, set := a[superviseRoundParam]; set {
			t.Fatalf("its start is a round: %v", a)
		}
	}

	f.mu.Lock()
	f.known["lead1"] = true
	f.mu.Unlock()
	setSettings(t, s, func(v *Settings) { v.Leaders = []string{"lead1"} })
	for _, to := range []string{"conv1", "lead1"} {
		if err := s.deliverRelay(ctx, "server", to, relayMsg{From: superviseTool, Tool: superviseTool, Text: text, At: time.Now().UTC()}); err != nil {
			t.Fatal(err)
		}
		if a := lastSendTo(f, to); a == nil || a[superviseRoundParam] != nil {
			t.Fatalf("a marked message to %s: %v", to, a)
		}
	}
	if err := s.deliverRelay(ctx, "server", bg, relayMsg{From: "conv1", Tool: "conversation_send", Text: text, At: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	if a := lastSendTo(f, bg); a[superviseRoundParam] != nil {
		t.Fatalf("another conversation's message is a round: %v", a)
	}

	// Sent back for rework: the next round says so, though it does not see the round that did it.
	if _, err := runJSON(t, s, bg, "task_review", map[string]any{"todo_id": todo.ID, "verdict": "rework", "note": "补深色截图"}); err != nil {
		t.Fatal(err)
	}
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "after-rework", Detail: "改到一半"})
	waitFor(t, "the next round", func() bool { return len(f.noticesTo(bg)) == 3 })
	next := f.noticesTo(bg)[2]
	at := time.Now().In(s.location(ctx)).Format("01-02")
	if !strings.Contains(next, "上次结论（"+at) || !strings.Contains(next, "）：rework，补深色截图") || !strings.Contains(next, roundIdle) || strings.Contains(next, roundReview) {
		t.Fatalf("the next round:\n%s", next)
	}
}

// A wake while a round runs (its conversation working, or starting while its process restarts) queues: later wakes
// merge into the one queued message, nothing is sent, and once it is idle exactly one round goes in.
func TestBackgroundWakeWhileARoundRuns(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	bg := withBackgroundBG1(t, s, f)
	listRound(f, bg, proto.StatusWorking, false)
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "k1", Minutes: 50})
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "k2", Detail: "停在提问上"})
	listRound(f, bg, proto.StatusIdle, true) // restarting for a round: Starting
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigInterrupted, Key: "k3"})
	if n := s.queuedFor(bg); n != 1 || len(assistantSendsTo(f, bg)) != 0 {
		t.Fatalf("while a round runs: %d queued, %d sent", n, len(assistantSendsTo(f, bg)))
	}
	s.drainRelays("server", listRound(f, bg, proto.StatusIdle, false))
	waitFor(t, "the round", func() bool { return len(assistantSendsTo(f, bg)) == 1 })
	settleService(t, s)
	sends := assistantSendsTo(f, bg)
	text, _ := sends[0]["text"].(string)
	if len(sends) != 1 || sends[0][superviseRoundParam] != true || !prompts.IsSuperviseRound(text) {
		t.Fatalf("sent %d: %v", len(sends), sends[0])
	}
	for _, want := range []string{"50 分钟没有进展", "停在提问上", "被模型服务出错打断了", "signal=" + sigStalled, roundIdle, "signal=" + sigInterrupted} {
		if !strings.Contains(text, want) {
			t.Errorf("the merged round lacks %q:\n%s", want, text)
		}
	}
}

// What the background supervisor does in a round is accepted after the next one was sent: the id is the same (a late
// call of the process before carries it), and the main conversation's announcement still counts.
func TestBackgroundReviewFromThePreviousRound(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	ctx := context.Background()
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了", 0)
	bg := withBackgroundBG1(t, s, f)
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "legacy-review"})
	waitFor(t, "round 1", func() bool { return len(f.noticesTo(bg)) == 1 })
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "k2", Minutes: 40})
	waitFor(t, "round 2", func() bool { return len(f.noticesTo(bg)) == 2 })
	for i, a := range assistantSendsTo(f, bg) {
		if a[superviseRoundParam] != true {
			t.Fatalf("message %d is no round", i+1)
		}
	}
	if res, err := runJSON(t, s, bg, "task_scope", map[string]any{"todo_id": todo.ID, "change": "the dark screenshot is enough", "why": "the light one is unchanged", "expect_minutes": 60}); err != nil {
		t.Fatalf("task_scope: %s %v", res, err)
	}
	if res, err := runJSON(t, s, bg, "task_review", map[string]any{"todo_id": todo.ID, "verdict": "rework", "note": "补测试"}); err != nil || !strings.Contains(res, "Rework round 1") {
		t.Fatalf("task_review: %s %v", res, err)
	}
	receipt := assistantwire.ViewErrorReceipt{ItemID: backgroundAnnounceReceipt, Digest: bg}
	s.Context(ctx, ContextParams{SessionID: "conv1", Ack: []assistantwire.ViewErrorReceipt{receipt}})
	if !s.settings(ctx).BackgroundSupervisorAnnounced {
		t.Fatal("the announcement's receipt no longer counts")
	}
}

// A queued wake is a round by its text and the switch when it goes in: rendered as one and the switch turned on
// since, it keeps the context; rendered whole and the switch turned off since, it keeps it once, and the next wake is
// a round.
func TestBackgroundSwitchFlippedWithAQueuedWake(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	bg := withBackgroundBG1(t, s, f)
	on, off := true, false

	listRound(f, bg, proto.StatusWorking, false)
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "k1", Minutes: 30})
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisorKeepsContext = &on })
	s.drainRelays("server", listRound(f, bg, proto.StatusIdle, false))
	waitFor(t, "the first message", func() bool { return len(assistantSendsTo(f, bg)) == 1 })
	if a := assistantSendsTo(f, bg)[0]; a[superviseRoundParam] != nil || !prompts.IsSuperviseRound(a["text"].(string)) {
		t.Fatalf("rendered as a round, switched on: %v", a[superviseRoundParam])
	}

	listRound(f, bg, proto.StatusWorking, false)
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "k2"})
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisorKeepsContext = &off })
	s.drainRelays("server", listRound(f, bg, proto.StatusIdle, false))
	waitFor(t, "the second message", func() bool { return len(assistantSendsTo(f, bg)) == 2 })
	if a := assistantSendsTo(f, bg)[1]; a[superviseRoundParam] != nil || prompts.IsSuperviseRound(a["text"].(string)) {
		t.Fatalf("rendered whole, switched off: %v", a[superviseRoundParam])
	}

	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigInterrupted, Key: "k3"})
	waitFor(t, "the third message", func() bool { return len(assistantSendsTo(f, bg)) == 3 })
	if a := assistantSendsTo(f, bg)[2]; a[superviseRoundParam] != true || !prompts.IsSuperviseRound(a["text"].(string)) {
		t.Fatalf("the next wake: %v", a[superviseRoundParam])
	}
}

// The switch turned on after a round: the next supervision message to the background supervisor asks for no round,
// which is what tells the node it needs every tool (its round's process, narrowed, is replaced while idle,
// node/assistant_round_switch_test.go), and it is rendered whole. Turned off again, the next one is a round again.
func TestBackgroundSwitchOnAfterARoundAsksForEveryTool(t *testing.T) {
	s, f, todo := newBackgroundService(t)
	bg := withBackgroundBG1(t, s, f)
	on, off := true, false
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "k1", Minutes: 30})
	waitFor(t, "the round", func() bool { return len(assistantSendsTo(f, bg)) == 1 })
	if a := assistantSendsTo(f, bg)[0]; a[superviseRoundParam] != true {
		t.Fatalf("the first message is no round: %v", a)
	}

	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisorKeepsContext = &on })
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "k2"})
	waitFor(t, "the message after the switch", func() bool { return len(assistantSendsTo(f, bg)) == 2 })
	a := assistantSendsTo(f, bg)[1]
	text, _ := a["text"].(string)
	if _, asked := a[superviseRoundParam]; asked || prompts.IsSuperviseRound(text) || strings.Contains(text, roundHeader) || a[holdPushesParam] != true {
		t.Fatalf("with the switch on: %v\n%s", a, text)
	}

	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisorKeepsContext = &off })
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigInterrupted, Key: "k3"})
	waitFor(t, "the message after the switch is off again", func() bool { return len(assistantSendsTo(f, bg)) == 3 })
	if a := assistantSendsTo(f, bg)[2]; a[superviseRoundParam] != true {
		t.Fatalf("with the switch off again: %v", a)
	}
}

// A round queued before the manager restarted goes in as a round after it, from its persisted text, with the pushes of
// its turn held as any supervision message's.
func TestBackgroundQueuedWakeAfterManagerRestart(t *testing.T) {
	// The queue now rechecks the task ledger after restart, so persist both in a real database.
	database, err := db.Open(context.Background(), newMigratedDatabase(t, db.AllSets), db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var st Store = database
	s, f, todo := newSupervisedServiceOn(t, st)
	s.noBackground = false
	f.mu.Lock()
	f.made = 100
	f.mu.Unlock()
	bg := withBackgroundBG1(t, s, f)
	listRound(f, bg, proto.StatusWorking, false)
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "k1", Minutes: 45})
	if s.queuedFor(bg) != 1 {
		t.Fatal("not queued")
	}
	r := &s.relays
	r.mu.Lock()
	queued := r.st.Queue[bg][0].Text
	r.mu.Unlock()
	settleService(t, s)

	f.mu.Lock()
	f.settings = nil // the new manager reads its settings from the database, as on a hub
	f.mu.Unlock()
	again := newServiceOn(t, f, st)
	again.noBackground = false
	stopHolds(t, again)
	if again.queuedFor(bg) != 1 {
		t.Fatal("the queue did not outlive the restart")
	}
	before := len(assistantSendsTo(f, bg))
	again.drainRelays("server", listRound(f, bg, proto.StatusIdle, false))
	waitFor(t, "the round after the restart", func() bool { return len(assistantSendsTo(f, bg)) == before+1 })
	a := lastSendTo(f, bg)
	if text, _ := a["text"].(string); a[superviseRoundParam] != true || !strings.HasSuffix(text, queued) || a[holdPushesParam] != true {
		t.Fatalf("after the restart: %v", a)
	}
	if !again.supervising(bg) {
		t.Fatal("its turn's pushes are not held")
	}
}

// A legacy background that cannot start from its first turn keeps its queued work while busy. Once idle, that work
// moves to main; the old conversation stays readable and no replacement is started (#5732, #7315).
func TestBackgroundWithoutAnchorMovesLegacyWorkToMain(t *testing.T) {
	s, f, todo, _ := newRoundService(t)
	ctx := context.Background()
	roundAnswers(f, map[string]any{"context": "kept", "why": "no_anchor", "transcript_bytes": 4096})
	agentTurn(s, f, proto.StatusWorking, "", 0)
	agentTurn(s, f, proto.StatusDone, "做完了", 0)
	old := withBackgroundBG1(t, s, f)
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "legacy-review"})
	waitFor(t, "the answer noted", func() bool { return outgrownOf(s, old) == "no_anchor" })

	// Working: it stays, and the explicitly legacy wake queues for it.
	listRound(f, old, proto.StatusWorking, false)
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "k1", Minutes: 50})
	if got := s.settings(ctx).BackgroundSupervisor; got != old || len(conversationStarts(f)) != 0 || s.queuedFor(old) != 1 {
		t.Fatalf("retired while it works: %q, %d starts, %d queued", got, len(conversationStarts(f)), s.queuedFor(old))
	}
	if s.queueRelay(old, relayMsg{From: "conv1", FromTitle: "Main", Tool: "conversation_send", Text: "顺便看看", At: time.Now().UTC()}) == 0 {
		t.Fatal("could not queue")
	}

	// Idle again: both the known signals and the message with no in-memory signals move to main.
	listRound(f, old, proto.StatusIdle, false)
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigInterrupted, Key: "k2"})
	// The moved conversation message may go in first, leaving this digest queued behind its turn. Let that turn finish,
	// as the node's next idle session list does on a hub, before requiring both parts of the moved work.
	waitFor(t, "the moved conversation message in main", func() bool {
		return strings.Contains(strings.Join(f.noticesTo("conv1"), "\n"), "顺便看看")
	})
	s.drainRelays("server", listRound(f, old, proto.StatusIdle, false))
	waitFor(t, "the moved legacy work in main", func() bool {
		got := strings.Join(f.noticesTo("conv1"), "\n")
		return strings.Contains(got, "50 分钟没有进展") && strings.Contains(got, "被模型服务出错打断了") && strings.Contains(got, "顺便看看")
	})
	if got := s.settings(ctx).BackgroundSupervisor; got != old || len(conversationStarts(f)) != 0 || s.isArchived(old) {
		t.Fatalf("retirement replaced or archived the old conversation: %q, %d starts, archived %v", got, len(conversationStarts(f)), s.isArchived(old))
	}
	if s.queuedFor(old) != 0 || outgrownOf(s, old) != "no_anchor" {
		t.Fatalf("after the move: old queue %d, outgrown %q", s.queuedFor(old), outgrownOf(s, old))
	}
	if got := f.noticesTo(old); len(got) != 1 {
		t.Fatalf("the old conversation got another message after retirement: %q", got)
	}
}

// A transcript over backgroundTranscriptLimit moves legacy work to main at the next idle digest; one at the limit does
// not, and background_supervisor_keeps_context keeps the existing long conversation instead.
func TestBackgroundOversizedTranscriptMovesLegacyWorkToMain(t *testing.T) {
	s, f, todo, _ := newRoundService(t)
	ctx := context.Background()
	bg := withBackgroundBG1(t, s, f)
	s.superviseRoundAnswered(bg, superviseRoundReply{Context: "fresh", TranscriptBytes: backgroundTranscriptLimit})
	if why := outgrownOf(s, bg); why != "" {
		t.Fatalf("at the limit: %q", why)
	}
	roundAnswers(f, map[string]any{"context": "fresh", "transcript_bytes": backgroundTranscriptLimit + 1})
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "k1", Minutes: 50})
	waitFor(t, "the answer noted", func() bool { return outgrownOf(s, bg) == "oversized" })

	on := true
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisorKeepsContext = &on })
	if id, gone := s.ensureBackground(ctx); id != bg || gone != "" {
		t.Fatalf("retired with the switch on: %q %q", id, gone)
	}
	off := false
	setSettings(t, s, func(v *Settings) { v.BackgroundSupervisorKeepsContext = &off })
	listRound(f, bg, proto.StatusIdle, false)
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "k2"})
	waitFor(t, "the oversized legacy work in main", func() bool {
		return strings.Contains(strings.Join(f.noticesTo("conv1"), "\n"), "signal="+sigIdle)
	})
	if len(conversationStarts(f)) != 0 || s.isArchived(bg) || outgrownOf(s, bg) != "oversized" {
		t.Fatalf("after the move: %d starts, archived %v, outgrown %q", len(conversationStarts(f)), s.isArchived(bg), outgrownOf(s, bg))
	}
}

// mcp.tools with tool_set supervise lists only the round's tools, in the order of the whole list; without it, or with
// a set this manager does not know, the whole list.
func TestRoundToolSet(t *testing.T) {
	retry := phoneToolRetry
	phoneToolRetry = time.Millisecond // the fake has no phone: its tools are asked for briefly
	t.Cleanup(func() { phoneToolRetry = retry })
	s, _, _ := newBackgroundService(t)
	devices := newFleetDevs()
	devices.add("server", "server")
	s.devs = devices
	names := func(set string) []string {
		t.Helper()
		params := map[string]any{"session_id": "conv1", "assistant": true}
		if set != "" {
			params["tool_set"] = set
		}
		raw, _ := json.Marshal(params)
		out, handled, err := s.NodeRequest(context.Background(), "server", "mcp.tools", raw)
		if err != nil || !handled {
			t.Fatalf("mcp.tools: %v %v", handled, err)
		}
		var got []string
		for _, tool := range out.(map[string]any)["tools"].([]proto.Tool) {
			got = append(got, tool.Name)
		}
		return got
	}
	all := names("")
	var want []string
	for _, n := range all {
		if slices.Contains(roundToolNames, n) {
			want = append(want, n)
		}
	}
	round := names(toolSetSupervise)
	if !slices.Equal(round, want) {
		t.Fatalf("the round's tools %v, want %v", round, want)
	}
	for _, n := range roundToolNames {
		if !slices.Contains(roundConnectorTools, n) && !slices.Contains(round, n) { // they come with their extension or connector, not here
			t.Errorf("the round lacks %s", n)
		}
	}
	// task_decided confirms the owner's answer a decision_reply entry brings; todo_list is where the snapshot sends it for
	// the tasks it leaves out.
	for _, n := range []string{"task_decided", "todo_list", "todo_get", "task_review", "notify_user"} {
		if !slices.Contains(round, n) {
			t.Errorf("the round lacks %s", n)
		}
	}
	for _, n := range []string{"ask_user", "conversation_send", "task_start_screen", "task_start_keys", "task_retry", "persona_settings", "todo_create"} {
		if slices.Contains(round, n) {
			t.Errorf("the round lists %s", n)
		}
	}
	if got := names("another"); !slices.Equal(got, all) {
		t.Fatalf("an unknown set: %v", got)
	}
}

// assistant.background_supervisor_keeps_context: off by default, set through persona_settings with its card; on, the
// background supervisor is today's long conversation: no round is asked for, its message is whole and it is never
// replaced for its rounds.
func TestBackgroundKeepsContextSetting(t *testing.T) {
	ctx := context.Background()
	t.Run("persona_settings", func(t *testing.T) {
		s := newTestService(t, newFakeHub().withMachine())
		const key = "assistant.background_supervisor_keeps_context"
		tool := func(args string) (string, bool) {
			r := s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(args), MachineID: "server", SessionID: "c1"})
			return r.Content[0].Text, r.IsError
		}
		get := func() string { out, _ := tool(`{"action":"get","key":"` + key + `"}`); return out }
		if got := get(); got != "false" {
			t.Fatalf("default: %s", got)
		}
		if out, isErr := tool(`{"action":"set","key":"` + key + `","value":false}`); isErr || !strings.Contains(out, "is unchanged") || len(s.pendingQuestions()) != 0 {
			t.Fatalf("set to what it is: %s", out)
		}
		card := i18n.T(i18n.Default, "assistant-settings_tool.change_background_supervisor_keeps_context",
			i18n.Args{"before": i18n.T(i18n.Default, "assistant-settings_tool.off"), "after": i18n.T(i18n.Default, "assistant-settings_tool.on")})
		go func() {
			q, ok := awaitCard(s)
			if !ok || q.Summary != card {
				t.Errorf("card %q (%v), want %q", q.Summary, ok, card)
				return
			}
			allow := true
			raw, _ := json.Marshal(proto.Resolution{Allow: &allow})
			if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: raw}); err != nil {
				t.Error(err)
			}
		}()
		if out, _ := tool(`{"action":"set","key":"` + key + `","value":true}`); !strings.HasPrefix(out, key+" is true") || get() != "true" {
			t.Fatalf("approved: %s", out)
		}
		if got := settingTitles[key]; got != "title_background_supervisor_keeps_context" {
			t.Fatalf("its title key: %q", got)
		}
	})
	t.Run("on", func(t *testing.T) {
		s, f, todo := newBackgroundService(t)
		on := true
		setSettings(t, s, func(v *Settings) { v.BackgroundSupervisorKeepsContext = &on })
		agentTurn(s, f, proto.StatusWorking, "", 0)
		agentTurn(s, f, proto.StatusDone, "做完了", 0)
		bg := withBackgroundBG1(t, s, f)
		wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigReview, Key: "legacy-review"})
		waitFor(t, "the message", func() bool { return len(f.noticesTo(bg)) == 1 })
		a := assistantSendsTo(f, bg)[0]
		text, _ := a["text"].(string)
		if a[superviseRoundParam] != nil || prompts.IsSuperviseRound(text) || strings.Contains(text, roundHeader) || !strings.Contains(text, "signal=") {
			t.Fatalf("with the switch on: %v\n%s", a[superviseRoundParam], text)
		}
		s.superviseRoundAnswered(bg, superviseRoundReply{Context: "kept", Why: "no_anchor"})
		if id, gone := s.ensureBackground(ctx); id != bg || gone != "" {
			t.Fatalf("replaced with the switch on: %q %q", id, gone)
		}
	})
}

// Moving outgrown legacy work to main neither drops the owner's model pick nor archives the conversation it belonged to.
func TestBackgroundOutgrownKeepsExistingModelPick(t *testing.T) {
	s, f, todo, _ := newRoundService(t)
	ctx := context.Background()
	bg := withBackgroundBG1(t, s, f)
	machine := s.settings(ctx).BackgroundSupervisorMachineID
	pick := modelPick{MachineID: machine, Launcher: "claude", Model: "example-model", At: time.Now().UTC()}
	if err := s.setModelPick(ctx, bg, &pick); err != nil {
		t.Fatal(err)
	}
	roundAnswers(f, map[string]any{"context": "fresh", "transcript_bytes": backgroundTranscriptLimit + 1})
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigStalled, Key: "k1", Minutes: 50})
	waitFor(t, "the answer noted", func() bool { return outgrownOf(s, bg) == "oversized" })
	listRound(f, bg, proto.StatusIdle, false)
	wakeRound(s, superviseSignal{Todo: todo.ID, Kind: sigIdle, Key: "k2"})
	waitFor(t, "the legacy work in main", func() bool { return len(f.noticesTo("conv1")) > 0 })
	got, ok := s.modelPickOf(ctx, machine, bg)
	if !ok || got.Launcher != pick.Launcher || got.Model != pick.Model {
		t.Fatalf("the existing conversation's pick: %+v %v, want %+v", got, ok, pick)
	}
	if s.settings(ctx).BackgroundSupervisor != bg || s.isArchived(bg) || len(conversationStarts(f)) != 0 {
		t.Fatalf("retirement changed the conversation: archived %v, %d starts", s.isArchived(bg), len(conversationStarts(f)))
	}
}
