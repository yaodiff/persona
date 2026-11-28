package assistant

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// A node that connected again (an ssh node whose link dropped and that started over) lists a Codex turn still running
// as stopped. That stop is no turn end while the transcript has the turn open: no finished push, and the stop that
// does end it later is. A stop whose transcript shows the turn closed is the end it says.
func TestReconnectStopIsNoTurnEndUntilTheTranscriptCloses(t *testing.T) {
	for _, closed := range []bool{false, true} {
		name := "open"
		if closed {
			name = "closed"
		}
		t.Run(name, func(t *testing.T) {
			ft := newFleetTestNoStore(t)
			fastDone(ft)
			items := []proto.Item{{ID: "u1", Kind: proto.KindText, Role: "user", Text: "go on"}, {ID: "a1", Kind: proto.KindText, Role: "assistant", Text: "still at it"}}
			if closed {
				items = append(items, proto.Item{ID: "end1", Kind: "turn_duration", TurnDuration: &proto.TurnDuration{TurnID: "turn-1"}})
			}
			ft.f.setSessions("mac") // the node's transcript reads go through the hub
			ft.f.answerNode(DeviceSessionItems, func(string, json.RawMessage) (any, error) {
				return map[string]any{"items": items}, nil
			})
			ft.d.mu.Lock()
			node := ft.d.machines["mac"]
			node.connected = time.Now().Add(-time.Hour)
			ft.d.mu.Unlock()

			work := proto.Session{SessionID: "s1", Agent: "codex", SourceKind: "codex_app_server", Status: proto.StatusWorking, UpdatedAt: time.Now(), Rev: 10}
			ft.sessions("mac", work)
			ft.d.mu.Lock()
			node.connected = time.Now() // the link dropped, the node is back
			ft.d.mu.Unlock()
			stopped := work
			stopped.Status, stopped.LastText = proto.StatusDone, "still at it"
			ft.sessions("mac", stopped)
			got := ft.pushesOf(assistantwire.NotifyDone, 1, 300*time.Millisecond)
			if closed {
				if len(got) != 1 {
					t.Fatalf("a stop whose transcript closed the turn: pushes = %+v", got)
				}
				return
			}
			if len(got) != 0 {
				t.Fatalf("a turn still running was taken as ended after the reconnect: %+v", got)
			}
			// The node finds the turn running, then it ends on this connection: that is the turn's end.
			ft.sessions("mac", work)
			done := work
			done.Status, done.LastText, done.Rev = proto.StatusDone, "Verified result", 20
			ft.sessions("mac", done)
			if got := ft.pushesOf(assistantwire.NotifyDone, 1, time.Second); len(got) != 1 || got[0].Body != done.LastText {
				t.Fatalf("the real end: pushes = %+v", got)
			}
		})
	}
}

// A transient StatusUnknown between working and done|idle (for example while a runtime's native summary DB lags
// behind its transcript export) must not swallow the working -> stopped turn-end edge, while non-working histories
// and repeated stopped polls must never fire spurious or duplicate notifications.
func TestWorkingThroughTransientUnknownTriggersTurnEnd(t *testing.T) {
	t.Run("working unknown done fires once", func(t *testing.T) {
		ft := newFleetTestNoStore(t)
		fastDone(ft)
		base := proto.Session{SessionID: "agy-1", Agent: "antigravity", Launcher: "antigravity", UpdatedAt: time.Now(), Rev: 1}
		work := base
		work.Status = proto.StatusWorking
		unk := base
		unk.Status, unk.Rev = proto.StatusUnknown, 2
		done := base
		done.Status, done.Rev, done.LastText = proto.StatusDone, 2, "Finished via laggy summary"

		ft.sessions("mac", work)
		ft.sessions("mac", unk)
		ft.sessions("mac", done)
		if got := ft.pushesOf(assistantwire.NotifyDone, 1, time.Second); len(got) != 1 || got[0].Body != done.LastText {
			t.Fatalf("working -> unknown -> done: pushes = %+v", got)
		}
		// Repeated done polls must not trigger a duplicate end.
		ft.sessions("mac", done)
		if got := ft.pushesOf(assistantwire.NotifyDone, 2, 200*time.Millisecond); len(got) != 1 {
			t.Fatalf("repeated done after unknown: pushes = %+v", got)
		}
	})

	t.Run("working consecutive unknown done fires once", func(t *testing.T) {
		ft := newFleetTestNoStore(t)
		fastDone(ft)
		base := proto.Session{SessionID: "agy-2", Agent: "antigravity", Launcher: "antigravity", UpdatedAt: time.Now(), Rev: 1}
		work := base
		work.Status = proto.StatusWorking
		unk := base
		unk.Status = proto.StatusUnknown
		done := base
		done.Status, done.Rev, done.LastText = proto.StatusDone, 2, "Finished after two unknown ticks"

		ft.sessions("mac", work)
		ft.sessions("mac", unk)
		ft.sessions("mac", unk)
		ft.sessions("mac", done)
		if got := ft.pushesOf(assistantwire.NotifyDone, 1, time.Second); len(got) != 1 || got[0].Body != done.LastText {
			t.Fatalf("working -> unknown -> unknown -> done: pushes = %+v", got)
		}
		ft.sessions("mac", done)
		if got := ft.pushesOf(assistantwire.NotifyDone, 2, 200*time.Millisecond); len(got) != 1 {
			t.Fatalf("repeated done after unknown: pushes = %+v", got)
		}
	})

	t.Run("non-working through unknown never triggers", func(t *testing.T) {
		ft := newFleetTestNoStore(t)
		fastDone(ft)
		done := proto.Session{SessionID: "agy-3", Agent: "antigravity", Status: proto.StatusDone, UpdatedAt: time.Now(), Rev: 1, LastText: "Old"}
		unk := done
		unk.Status = proto.StatusUnknown
		ft.sessions("mac", done)
		ft.sessions("mac", unk)
		ft.sessions("mac", done)
		if got := ft.pushesOf(assistantwire.NotifyDone, 1, 200*time.Millisecond); len(got) != 0 {
			t.Fatalf("done -> unknown -> done: pushes = %+v", got)
		}
		firstUnk := proto.Session{SessionID: "agy-4", Agent: "antigravity", Status: proto.StatusUnknown, UpdatedAt: time.Now(), Rev: 1}
		firstDone := firstUnk
		firstDone.Status, firstDone.LastText = proto.StatusDone, "Initial"
		ft.sessions("mac", firstUnk)
		ft.sessions("mac", firstDone)
		if got := ft.pushesOf(assistantwire.NotifyDone, 1, 200*time.Millisecond); len(got) != 0 {
			t.Fatalf("unknown -> done without prior working: pushes = %+v", got)
		}
	})

	t.Run("working unknown interrupted clears pending end", func(t *testing.T) {
		ft := newFleetTestNoStore(t)
		fastDone(ft)
		base := proto.Session{SessionID: "agy-5", Agent: "antigravity", UpdatedAt: time.Now(), Rev: 1}
		work, unk, intr, done := base, base, base, base
		work.Status = proto.StatusWorking
		unk.Status = proto.StatusUnknown
		intr.Status = proto.StatusInterrupted
		done.Status, done.LastText = proto.StatusDone, "After interrupt"
		ft.sessions("mac", work)
		ft.sessions("mac", unk)
		ft.sessions("mac", intr)
		ft.sessions("mac", done)
		if got := ft.pushesOf(assistantwire.NotifyDone, 1, 200*time.Millisecond); len(got) != 0 {
			t.Fatalf("working -> unknown -> interrupted -> done: pushes = %+v", got)
		}
	})

	t.Run("working unknown working done fires once", func(t *testing.T) {
		ft := newFleetTestNoStore(t)
		fastDone(ft)
		base := proto.Session{SessionID: "agy-6", Agent: "antigravity", UpdatedAt: time.Now(), Rev: 1}
		work, unk, done := base, base, base
		work.Status = proto.StatusWorking
		unk.Status = proto.StatusUnknown
		done.Status, done.Rev, done.LastText = proto.StatusDone, 2, "Resumed and finished"
		ft.sessions("mac", work)
		ft.sessions("mac", unk)
		ft.sessions("mac", work)
		ft.sessions("mac", done)
		if got := ft.pushesOf(assistantwire.NotifyDone, 1, time.Second); len(got) != 1 || got[0].Body != done.LastText {
			t.Fatalf("working -> unknown -> working -> done: pushes = %+v", got)
		}
	})
}

// Reconnecting before or after a transient StatusUnknown gap must still verify the transcript via reconnectedTurnEnd
// instead of blindly firing turnEndSeen, and must never re-fire on subsequent stopped polls.
func TestWorkingThroughUnknownAndReconnectChecksTranscript(t *testing.T) {
	for _, reconnectBeforeUnknown := range []bool{false, true} {
		for _, closed := range []bool{false, true} {
			name := "reconnect-after-unknown"
			if reconnectBeforeUnknown {
				name = "reconnect-before-unknown"
			}
			if closed {
				name += "/closed"
			} else {
				name += "/open"
			}
			t.Run(name, func(t *testing.T) {
				ft := newFleetTestNoStore(t)
				fastDone(ft)
				items := []proto.Item{
					{ID: "1@abc#0", Kind: proto.KindText, Role: "user", Text: "run"},
					{ID: "2@abc#0", Kind: proto.KindText, Role: "assistant", Text: "reply"},
				}
				if closed {
					items = append(items, proto.Item{
						ID: "2@abc#turn_duration", Kind: "turn_duration", Role: "system",
						TurnDuration: &proto.TurnDuration{DurationMS: 0, Source: "agy_turn_end", ParentUUID: "2@abc", UserItemID: "1@abc#0"},
					})
				}
				ft.f.setSessions("mac")
				ft.f.answerNode(DeviceSessionItems, func(string, json.RawMessage) (any, error) {
					return map[string]any{"items": items}, nil
				})
				ft.d.mu.Lock()
				node := ft.d.machines["mac"]
				node.connected = time.Now().Add(-time.Hour)
				ft.d.mu.Unlock()

				reconnect := func() {
					ft.d.mu.Lock()
					node.connected = time.Now()
					ft.d.mu.Unlock()
				}

				work := proto.Session{SessionID: "agy-r1", Agent: "antigravity", Status: proto.StatusWorking, UpdatedAt: time.Now(), Rev: 10}
				unk := work
				unk.Status = proto.StatusUnknown
				done := work
				done.Status, done.LastText = proto.StatusDone, "reply"

				ft.sessions("mac", work)
				if reconnectBeforeUnknown {
					reconnect()
					ft.sessions("mac", unk)
				} else {
					ft.sessions("mac", unk)
					reconnect()
				}
				ft.sessions("mac", done)

				got := ft.pushesOf(assistantwire.NotifyDone, 1, 300*time.Millisecond)
				if closed {
					if len(got) != 1 {
						t.Fatalf("closed transcript across unknown+reconnect: pushes = %+v", got)
					}
					// A repeated done poll must not re-fire.
					ft.sessions("mac", done)
					if again := ft.pushesOf(assistantwire.NotifyDone, 2, 200*time.Millisecond); len(again) != 1 {
						t.Fatalf("duplicate push after closed reconnect: %+v", again)
					}
					return
				}
				if len(got) != 0 {
					t.Fatalf("open transcript across unknown+reconnect must not fire: %+v", got)
				}
				// Another done poll must not falsely treat the unsure-unknown row as previously working.
				ft.sessions("mac", done)
				if again := ft.pushesOf(assistantwire.NotifyDone, 1, 200*time.Millisecond); len(again) != 0 {
					t.Fatalf("second done poll after rejected reconnect stop fired: %+v", again)
				}
				// Once the node observes working again and then done on this connection, that is the real end.
				ft.sessions("mac", work)
				realDone := done
				realDone.LastText, realDone.Rev = "Real end", 20
				ft.sessions("mac", realDone)
				if final := ft.pushesOf(assistantwire.NotifyDone, 1, time.Second); len(final) != 1 || final[0].Body != realDone.LastText {
					t.Fatalf("real end after rejected reconnect: pushes = %+v", final)
				}
			})
		}
	}
}
