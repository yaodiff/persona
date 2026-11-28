package assistant

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// attentionFleet is a fleet whose main conversation, conv1, runs on the hub machine's node "server".
func attentionFleet(t *testing.T) *fleetTest {
	t.Helper()
	ft := newFleetTestNoStore(t)
	ft.d.add("server", "server")
	ft.f.sessions["server"] = nil
	ft.f.hello.Assistant.MachineID = "server"
	// In the first settings: without the hub's profile the inventory reads the route once (mainConversationRoute).
	ft.f.settings = &Settings{Timezone: "Asia/Shanghai", Main: "conv1", MachineID: "server"}
	withSettings(t, ft.f, ft.s)
	ft.f.known["conv1"] = true
	return ft
}

// mainRow is the main conversation as the node lists it, finished, at revision rev.
func mainRow(rev int64) proto.Session {
	return proto.Session{SessionID: "conv1", Cwd: "/home/u/.persona/assistant", Assistant: true, Status: proto.StatusDone, Rev: rev}
}

// A node's sessions.changed is handled on the read loop of that node's connection (rpc.Peer.Serve, devices'
// OnEvent, NodeEvent, setSessions), and only that loop reads the node's answers. The main conversation's attention
// scan read the conversation from the same node inside setSessions, so after every finished main-chat turn it waited
// for an answer nobody could read: 15 s until its deadline, with every other reply and event of the node held behind
// it (pings, the apps' reads, item pushes, the turn's end) (#16214). Here the node's answer to that read is held, as
// that loop would hold it: the list must still be taken, and once the node answers the verdict lands and the row is
// published again with it.
func TestMainConversationAttentionLeavesTheNodeReadLoop(t *testing.T) {
	ft := attentionFleet(t)
	at := time.Date(2026, 11, 26, 6, 16, 4, 0, time.UTC)
	ft.f.setItems(attentionTestRenumber(attentionTestItems(at, at.Add(time.Minute))[3:])...) // one completed silent round
	release := make(chan struct{})
	var releaseOnce sync.Once
	free := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(free)
	held := make(chan string, 64)
	ft.f.hookReads(receiptHooks{beforeCall: func(p DeviceCall) {
		select {
		case held <- p.Method:
		default:
		}
		<-release
	}})

	taken := make(chan struct{})
	go func() {
		ft.sessions("server", mainRow(11))
		close(taken)
	}()
	select {
	case <-taken:
	case <-time.After(10 * time.Second):
		var waiting []string
		for len(held) > 0 {
			waiting = append(waiting, <-held)
		}
		t.Fatalf("the node's sessions.changed waited for the node itself (held calls: %v): its handler runs on that "+
			"node's read loop, which alone reads the answer (#16214)", waiting)
	}

	select {
	case method := <-held:
		if method != DeviceSessionItems {
			t.Fatalf("the first call to the node was %s, want the scan's %s", method, DeviceSessionItems)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the attention scan never read the main conversation")
	}
	if row := listedRow(ft.lastList("server"), "conv1"); row.State != proto.StateUnread {
		t.Fatalf("before the scan answered, the main conversation was published as %q, want unread", row.State)
	}

	free()
	waitFor(t, "the silent round to be classified and the row published again as idle", func() bool {
		entry, ok := ft.s.conversationAttentionCached("server", "conv1", 11)
		return ok && entry.Silent && listedRow(ft.lastList("server"), "conv1").State == proto.StateIdle
	})
}

// Every revision of the main conversation gets its verdict, one read each, off the read loop (#16214 review). Each
// list comes once the run before it has ended, so a run that ended must let the next revision start one, or the first
// turn's verdict would be the only one ever made. A revision whose verdict is cached is not read again. An open round
// is no verdict: the next list with the same revision reads it again, once the round has closed.
func TestMainConversationAttentionClassifiesEachRevision(t *testing.T) {
	ft := attentionFleet(t)
	t0 := time.Date(2026, 11, 26, 6, 0, 0, 0, time.UTC)
	silent := func(at time.Time, closed bool) []proto.Item { // a supervision round finished silently
		round := attentionTestItems(at, at.Add(time.Minute))[3:]
		if !closed {
			round = round[:3] // the receipt, but no end record from the runtime yet
		}
		return round
	}
	visible := func(at time.Time) []proto.Item { // an exchange the owner sees
		return []proto.Item{
			attentionTestItem(0, at, "user", proto.KindText, "Show me the result"),
			attentionTestItem(0, at.Add(time.Second), "assistant", proto.KindText, "Here it is"),
			attentionTestItem(0, at.Add(2*time.Second), "", "turn_duration", ""),
		}
	}
	history := func(rounds ...[]proto.Item) []proto.Item {
		var all []proto.Item
		for _, round := range rounds {
			all = append(all, round...)
		}
		return attentionTestRenumber(all)
	}
	list := func(rev int64, items []proto.Item) {
		ft.f.setItems(items...)
		ft.sessions("server", mainRow(rev))
	}
	verdict := func(rev int64) (conversationAttentionEntry, bool) {
		return ft.s.conversationAttentionCached("server", "conv1", rev)
	}
	ended := func(what string) { // whatever run the last list started has returned
		waitFor(t, what, func() bool {
			for _, stack := range goroutineStacks() {
				if strings.Contains(stack, "(*Service).runConversationAttention(") {
					return false
				}
			}
			return true
		})
	}

	list(1, history(silent(t0, true)))
	waitFor(t, "revision 1's verdict", func() bool { e, ok := verdict(1); return ok && e.Silent && !e.HasVisible })
	waitFor(t, "revision 1's row published again as idle", func() bool {
		return listedRow(ft.lastList("server"), "conv1").State == proto.StateIdle
	})
	ended("revision 1's run to end")

	// A new latest round, one the owner sees: its own verdict.
	list(2, history(silent(t0, true), visible(t0.Add(10*time.Minute))))
	waitFor(t, "revision 2's verdict, asked for after revision 1's run ended", func() bool {
		e, ok := verdict(2)
		return ok && !e.Silent && e.HasVisible
	})
	ended("revision 2's run to end")
	if n := attentionTestReads(ft.f); n != 2 {
		t.Fatalf("%d session.items reads for two revisions, want one each", n)
	}

	// The same revision again: its verdict is cached.
	list(2, history(silent(t0, true), visible(t0.Add(10*time.Minute))))
	ended("a run for revision 2 again to end")
	if n := attentionTestReads(ft.f); n != 2 {
		t.Fatalf("revision 2 was read again although its verdict is cached: %d reads", n)
	}

	// The latest round has no end record yet: nothing to cache.
	list(3, history(silent(t0, true), visible(t0.Add(10*time.Minute)), silent(t0.Add(20*time.Minute), false)))
	waitFor(t, "revision 3's first read", func() bool { return attentionTestReads(ft.f) == 3 })
	ended("revision 3's first run to end")
	if e, ok := verdict(3); ok {
		t.Fatalf("an open round was cached as revision 3's verdict: %+v", e)
	}
	// The next list, same revision, once the round has closed: read again and classified.
	list(3, history(silent(t0, true), visible(t0.Add(10*time.Minute)), silent(t0.Add(20*time.Minute), true)))
	waitFor(t, "revision 3 read again and classified once its round closed", func() bool {
		e, ok := verdict(3)
		return ok && e.Silent && e.HasVisible
	})
	if n := attentionTestReads(ft.f); n != 4 {
		t.Fatalf("%d session.items reads, want 4: one for each revision and one more for the open round", n)
	}
}

// The bookkeeping that keeps the scan off the read loop: at most one run per conversation, for the newest revision
// asked for; a run that has ended lets the next revision start one; a revision whose verdict is cached starts nothing.
// The run looks the verdict up before it reads, so no read or published row tells the last case apart: it is checked
// here (#16214 review).
func TestAttentionScansStartEachRevisionOnce(t *testing.T) {
	var a attentionScans
	key := conversationAttentionCacheKey("server", "conv1")
	rev := func(r int64) attentionScan { return attentionScan{machine: "server", session: "conv1", rev: r} }
	if !a.want(key, rev(1)) {
		t.Fatal("the first revision started no run")
	}
	if a.want(key, rev(2)) {
		t.Fatal("a second run started while one ran")
	}
	if got, ok := a.take(key); !ok || got.rev != 2 {
		t.Fatalf("take = %+v, %v; want the newest revision asked for, 2", got, ok)
	}
	a.settle(key, 2)
	if got, ok := a.take(key); ok {
		t.Fatalf("take = %+v after the newest revision was taken; want none, and the run's end", got)
	}
	if a.want(key, rev(2)) {
		t.Fatal("a revision whose verdict is cached started a run")
	}
	if !a.want(key, rev(3)) {
		t.Fatal("a new revision started no run after the last run ended")
	}
}

// listedRow is session's row in list (zero when it is not there).
func listedRow(list []proto.Session, session string) proto.Session {
	for _, row := range list {
		if row.SessionID == session {
			return row
		}
	}
	return proto.Session{}
}

// While compaction is running, the node may keep a silent compaction row disguised as StatusDone / StatusIdle while
// /compact and CHECKPOINT steps advance Rev. Both refreshMainConversationAttention and decorateMainConversationAttention
// must skip scanning until CompactionRunning clears, then resume normal classification once compaction settles.
func TestMainConversationAttentionSkipsWhileCompactionRunning(t *testing.T) {
	ft := attentionFleet(t)
	at := time.Date(2026, 11, 26, 6, 16, 4, 0, time.UTC)
	ft.f.setItems(attentionTestRenumber(attentionTestItems(at, at.Add(time.Minute))[3:])...) // one completed silent round

	compacting := func(rev int64, status string) proto.Session {
		row := mainRow(rev)
		row.Status = status
		row.Compaction = &proto.Compaction{State: proto.CompactionRunning}
		return row
	}

	ft.sessions("server", compacting(11, proto.StatusDone))
	ft.sessions("server", compacting(12, proto.StatusIdle))
	if got := attentionTestReads(ft.f); got != 0 {
		t.Fatalf("refreshMainConversationAttention made %d session.items RPCs while CompactionRunning, want 0", got)
	}
	if _, cached := ft.s.conversationAttentionCached("server", "conv1", 11); cached {
		t.Fatal("compacting revision 11 was unexpectedly classified")
	}
	if _, cached := ft.s.conversationAttentionCached("server", "conv1", 12); cached {
		t.Fatal("compacting revision 12 was unexpectedly classified")
	}

	row := compacting(12, proto.StatusDone)
	row.State = proto.StateUnread
	ft.s.decorateMainConversationAttention(&row, ft.s.settings(t.Context()))
	if got := attentionTestReads(ft.f); got != 0 {
		t.Fatalf("decorateMainConversationAttention made %d session.items RPCs while CompactionRunning, want 0", got)
	}
	if row.State != proto.StateUnread {
		t.Fatalf("decorateMainConversationAttention altered state while CompactionRunning: %q", row.State)
	}

	settled := mainRow(12)
	settled.Compaction = &proto.Compaction{State: proto.CompactionDone}
	ft.sessions("server", settled)
	waitFor(t, "settled revision 12 classified once compaction finishes", func() bool {
		e, ok := ft.s.conversationAttentionCached("server", "conv1", 12)
		return ok && e.Silent && listedRow(ft.lastList("server"), "conv1").State == proto.StateIdle
	})
	if got := attentionTestReads(ft.f); got != 1 {
		t.Fatalf("after compaction settled, got %d session.items RPCs, want 1", got)
	}
}
