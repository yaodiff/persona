package assistant

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func attentionTestItem(seq int, at time.Time, role, kind, text string) proto.Item {
	return proto.Item{Seq: seq, TS: at, Role: role, Kind: kind, Text: text}
}

func attentionTestTool(seq int, at time.Time, id, name, input string) proto.Item {
	return proto.Item{Seq: seq, TS: at, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: id, ToolName: name, ToolInput: json.RawMessage(input)}
}

func attentionTestResult(seq int, at time.Time, id, text string, failed bool) proto.Item {
	return proto.Item{Seq: seq, TS: at, Role: "user", Kind: proto.KindToolResult, ToolUseID: id, Text: text, IsError: failed}
}

func attentionTestItems(visibleAt, silentAt time.Time) []proto.Item {
	return []proto.Item{
		attentionTestItem(1, visibleAt.Add(-time.Minute), "user", proto.KindText, "Show the result"),
		attentionTestItem(2, visibleAt.Add(-time.Second), "assistant", proto.KindText, "The earlier result"),
		attentionTestItem(3, visibleAt, "", "turn_duration", ""),
		{Seq: 4, TS: silentAt.Add(-time.Minute), Role: "user", Kind: proto.KindText,
			Text: prompts.MessageSourceText("Check the task", &proto.MessageSource{Kind: proto.SourceSupervision})},
		attentionTestTool(5, silentAt.Add(-2*time.Second), "finish", "mcp__persona__supervision_finish", `{"visibility":"silent","summary":"Handled"}`),
		attentionTestResult(6, silentAt.Add(-time.Second), "finish", `{"visibility":"silent","summary":"Handled"}`, false),
		attentionTestItem(7, silentAt, "", "turn_duration", ""),
	}
}

func attentionTestReads(f *fakeHub) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.itemReads
}

func attentionTestRenumber(items []proto.Item) []proto.Item {
	out := append([]proto.Item(nil), items...)
	for i := range out {
		out[i].Seq = i + 1
	}
	return out
}

func TestMainConversationSilentUnreadScope(t *testing.T) {
	s, f := newQuietService(t)
	visibleAt := time.Date(2026, 11, 22, 8, 0, 0, 0, time.UTC)
	silentAt := visibleAt.Add(20 * time.Minute)
	f.setItems(attentionTestItems(visibleAt, silentAt)...)
	row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 9}
	cfg := s.settings(context.Background())

	s.decorateMainConversationAttention(&row, cfg)
	if row.State != proto.StateUnread {
		t.Fatalf("an earlier visible round was hidden with the latest silent one: %s", row.State)
	}
	if got := attentionTestReads(f); got != 1 {
		t.Fatalf("%d transcript reads, want 1", got)
	}

	row.SeenAt = visibleAt.Add(time.Second)
	s.decorateMainConversationAttention(&row, cfg)
	if row.State != proto.StateIdle {
		t.Fatalf("the silent round's own unread mark remains after its earlier work was seen: %s", row.State)
	}
	if got := attentionTestReads(f); got != 1 {
		t.Fatalf("a list refresh reread the same successful classification: %d reads", got)
	}

	promoted := attentionTestItems(visibleAt, silentAt)
	promoted = append(promoted[:6],
		attentionTestTool(7, silentAt.Add(-time.Second), "notify", "notify_user", `{}`),
		attentionTestResult(8, silentAt, "notify", "sent", false),
		attentionTestItem(9, silentAt.Add(time.Second), "", "turn_duration", ""))
	f.setItems(promoted...)
	row.Rev++
	row.State = proto.StateUnread
	s.decorateMainConversationAttention(&row, cfg)
	if row.State != proto.StateUnread {
		t.Fatalf("a later notification was hidden by the silent receipt: %s", row.State)
	}

	laterInput := append(attentionTestItems(visibleAt, silentAt),
		attentionTestItem(8, silentAt.Add(time.Minute), "user", proto.KindText, "Tell me the result"))
	f.setItems(laterInput...)
	row.Rev++
	row.State = proto.StateUnread
	s.decorateMainConversationAttention(&row, cfg)
	if row.State != proto.StateUnread {
		t.Fatalf("later genuine input did not reset the silent verdict: %s", row.State)
	}

	before := attentionTestReads(f)
	for _, state := range []string{proto.StateWorking, proto.StateNeedsYou, proto.StateIdle} {
		busy := row
		busy.State = state
		if state == proto.StateIdle {
			busy.SeenAt = silentAt.Add(2 * time.Minute) // this later direct answer is seen too
		}
		s.decorateMainConversationAttention(&busy, cfg)
		if busy.State != state {
			t.Errorf("%s became %s; working, needs-you and an already-seen row must keep their state", state, busy.State)
		}
	}
	unknownRevision := row
	unknownRevision.Rev = 0
	unknownRevision.State = proto.StateUnread
	s.decorateMainConversationAttention(&unknownRevision, cfg)
	if unknownRevision.State != proto.StateUnread {
		t.Fatal("a revision-less row used a cached silent verdict")
	}
	if got := attentionTestReads(f); got != before {
		t.Fatalf("working / needs-you / seen / Rev-zero rows triggered transcript reads: before=%d after=%d", before, got)
	}
}

func TestMainConversationAttentionPrecomputedOnSessionList(t *testing.T) {
	s, f := newQuietServiceOn(t, &memStore{})
	at := time.Date(2026, 11, 22, 8, 30, 0, 0, time.UTC)
	f.setItems(attentionTestRenumber(attentionTestItems(at, at.Add(time.Minute))[3:])...)
	row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 11}
	s.setSessions("server", []proto.Session{row})
	// The scan runs off the node's read loop (#16214): wait for its verdict.
	waitFor(t, "the scan to cache the silent classification", func() bool {
		_, ok := s.conversationAttentionCached("server", "conv1", 11)
		return ok
	})
	entry, ok := s.conversationAttentionCached("server", "conv1", 11)
	if !ok || !entry.Silent {
		t.Fatalf("setSessions did not precompute the silent classification: %+v, %v", entry, ok)
	}
	s.applyMainConversationAttention(&row, "conv1", "server")
	if row.State != proto.StateIdle {
		t.Fatalf("the cached silent classification was not applied: %s", row.State)
	}
	if reads := attentionTestReads(f); reads != 1 {
		t.Fatalf("setSessions read the transcript %d times, want once", reads)
	}
	s.setSessions("server", []proto.Session{row})
	if reads := attentionTestReads(f); reads != 1 {
		t.Fatalf("the same revision was scanned again: %d reads", reads)
	}
}

func TestMainConversationAttentionCacheRestartAndFailures(t *testing.T) {
	st := &memStore{}
	at := time.Date(2026, 11, 22, 9, 0, 0, 0, time.UTC)
	items := attentionTestRenumber(attentionTestItems(at, at.Add(time.Minute))[3:])
	s1, f1 := newQuietServiceOn(t, st)
	f1.setItems(items...)
	row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 7}
	entry, ok := s1.mainConversationAttention(context.Background(), "server", "conv1", 7)
	if !ok || !entry.Silent || entry.HasVisible {
		t.Fatalf("first classification = %+v, %v", entry, ok)
	}
	if _, ok := s1.mainConversationAttention(context.Background(), "server", "conv1", 7); !ok || attentionTestReads(f1) != 1 {
		t.Fatal("the in-memory revision cache did not avoid a second read")
	}

	s2, f2 := newQuietServiceOn(t, st)
	f2.setItems(items...)
	entry, ok = s2.mainConversationAttention(context.Background(), "server", "conv1", 7)
	if !ok || !entry.Silent || attentionTestReads(f2) != 0 {
		t.Fatalf("restart cache = %+v, %v, %d reads", entry, ok, attentionTestReads(f2))
	}
	s2.decorateMainConversationAttention(&row, s2.settings(context.Background()))
	if row.State != proto.StateIdle || !row.SeenAt.IsZero() {
		t.Fatalf("silent-only history: state=%s seen_at=%v; nothing may be marked seen", row.State, row.SeenAt)
	}

	s3, f3 := newQuietService(t)
	f3.setItems(items...)
	f3.itemsFail = 1
	failed := row
	failed.State = proto.StateUnread
	s3.decorateMainConversationAttention(&failed, s3.settings(context.Background()))
	if failed.State != proto.StateUnread {
		t.Fatalf("a failed transcript read did not fail open: %s", failed.State)
	}
	if reads := attentionTestReads(f3); reads != 1 {
		t.Fatalf("a failed transcript read used %d reads, want one", reads)
	}
	s3.decorateMainConversationAttention(&failed, s3.settings(context.Background()))
	if reads := attentionTestReads(f3); reads != 1 {
		t.Fatalf("the failed revision was not negatively cached: %d reads", reads)
	}
	alreadySeen := failed
	alreadySeen.State = proto.StateIdle
	f3.itemsFail = 1
	s3.decorateMainConversationAttention(&alreadySeen, s3.settings(context.Background()))
	if alreadySeen.State != proto.StateIdle {
		t.Fatal("a failed transcript read resurrected an already-seen conversation")
	}
}

func TestMainConversationAttentionRequiresCompletedLatestRound(t *testing.T) {
	s, f := newQuietService(t)
	at := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	items := attentionTestRenumber(attentionTestItems(at, at.Add(time.Minute))[3:6]) // a successful receipt, but no runtime end record yet
	f.setItems(items...)
	row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 3}
	s.decorateMainConversationAttention(&row, s.settings(context.Background()))
	if row.State != proto.StateUnread {
		t.Fatal("a still-open round was classified as completed silent")
	}
	f.setItems(attentionTestRenumber(attentionTestItems(at, at.Add(time.Minute))[3:])...)
	s.decorateMainConversationAttention(&row, s.settings(context.Background()))
	if row.State != proto.StateIdle {
		t.Fatalf("the same open classification was cached after the runtime closed the round: %s", row.State)
	}
}

func TestMainConversationAttentionScanCapFailsOpen(t *testing.T) {
	s, f := newQuietService(t)
	at := time.Date(2026, 11, 22, 11, 0, 0, 0, time.UTC)
	page := 0
	f.answerNode(DeviceSessionItems, func(_ string, _ json.RawMessage) (any, error) {
		page++
		seq := (5 - page) * 10
		items := []proto.Item{
			{Seq: seq, TS: at, Role: "user", Kind: proto.KindText, Source: &proto.MessageSource{Kind: proto.SourceSupervision}},
			attentionTestTool(seq+1, at.Add(time.Second), "finish", "supervision_finish", `{}`),
			attentionTestResult(seq+2, at.Add(2*time.Second), "finish", `{"visibility":"silent"}`, false),
			attentionTestItem(seq+3, at.Add(3*time.Second), "", "turn_duration", ""),
		}
		// Even a visible round beyond the bounded scan must not be assumed absent.
		return map[string]any{"items": items, "has_more": true}, nil
	})
	row := proto.Session{MachineID: "server", SessionID: "conv1", Assistant: true, Status: proto.StatusDone, State: proto.StateUnread, Rev: 4}
	s.decorateMainConversationAttention(&row, s.settings(context.Background()))
	if row.State != proto.StateUnread || page != conversationAttentionScan || attentionTestReads(f) != conversationAttentionScan {
		t.Fatalf("state=%s pages=%d reads=%d, want unread after the bounded %d-page scan", row.State, page, attentionTestReads(f), conversationAttentionScan)
	}
}

func TestMainConversationWaitingCountAndExactItems(t *testing.T) {
	ft := newFleetTaskTest(t)
	s := ft.s
	cards := []proto.Interaction{
		{ID: "question", MachineID: "server", SessionID: "conv1", Kind: proto.IxQuestion, ToolName: "ask_user"},
		{ID: "accepted", MachineID: "server", SessionID: "conv1", Kind: proto.IxQuestion, ToolName: "task_review", ToolInput: json.RawMessage(`{"verdict":"accepted"}`)},
		{ID: "needs-user", MachineID: "server", SessionID: "conv1", Kind: proto.IxQuestion, ToolName: "task_review", ToolInput: json.RawMessage(`{"verdict":"needs_user"}`)},
		{ID: "owner-authorization", MachineID: "server", SessionID: "conv1", Kind: proto.IxPermission, OwnerProposal: true},
		{ID: "unstarted", MachineID: "server", SessionID: "conv1", Kind: proto.IxPlan, ToolName: "task_plan"},
		{ID: "failed-start", MachineID: "server", SessionID: "conv1", Kind: proto.IxPermission, ToolName: "task_retry"},
		{ID: "agent-permission", MachineID: "server", SessionID: "conv1", Kind: proto.IxPermission, ToolName: "Bash"},
		{ID: "terminal", MachineID: "server", SessionID: "conv1", Kind: proto.IxScreen},
		{ID: "superseded", MachineID: "server", SessionID: "conv1", Kind: proto.IxQuestion, ToolName: "ask_user", Delegating: true},
		{ID: "agent-permission-elsewhere", MachineID: "box", SessionID: "s1", Kind: proto.IxPermission, ToolName: "Bash"},
	}
	s.fleet.mu.Lock()
	s.fleet.interactions = map[string]*pending{}
	for _, ix := range cards {
		s.fleet.interactions[ix.ID] = &pending{ix: ix}
	}
	s.fleet.mu.Unlock()

	main := s.decorateInventory("server", []proto.Session{{SessionID: "conv1", Assistant: true, Status: proto.StatusIdle}}, false)
	if len(main) != 1 || main[0].PendingInteractions != 4 || main[0].State != proto.StateNeedsYou {
		t.Fatalf("main row = %+v, want exactly the four direct user cards", main)
	}
	var exact []string
	for _, ix := range cards {
		if ix.MachineID == "server" && ix.SessionID == "conv1" && directUserInteraction(ix) {
			exact = append(exact, ix.ID)
		}
	}
	want := []string{"question", "accepted", "needs-user", "owner-authorization"}
	if len(exact) != len(want) {
		t.Fatalf("count and list disagree: %v", exact)
	}
	for i := range want {
		if exact[i] != want[i] {
			t.Fatalf("exact items = %v, want %v", exact, want)
		}
	}
	if got := len(s.pendingInteractions()); got != len(cards) {
		t.Fatalf("the full interaction API lost cards: %d, want %d", got, len(cards))
	}
	agent := s.decorateInventory("box", []proto.Session{{SessionID: "s1", Status: proto.StatusBlocked}}, false)
	if len(agent) != 1 || agent[0].PendingInteractions != 1 || agent[0].State != proto.StateNeedsYou {
		t.Fatalf("the Agents-list badge rule changed: %+v", agent)
	}
}
