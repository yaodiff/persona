package hub

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Every production alert reaches applyPushGrading through sendFromPolicy or sendCard. Exercise those real boundaries, then
// verify both the JSON saved in hub_settings and the record a restarted pusher reads back.
func TestPushGradingHeldPushSourcesPersist(t *testing.T) {
	tests := []struct {
		name                           string
		reason, todo, machine, session string
		record                         func(*testing.T, *Hub)
	}{
		{
			name: "quiet ask carries its todo and agent session", reason: interceptReasonP1,
			todo: "todo_quiet", machine: "mac", session: "agent-ask",
			record: func(t *testing.T, h *Hub) {
				n := gradingNotification(assistantwire.LevelP1, "ask:todo_quiet", "quiet ask")
				n.DeviceID, n.SessionID = "mac", "agent-ask"
				if err := h.deliverPush(n); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "notify_user keeps its free-form key and session", reason: interceptReasonP1,
			machine: "mac", session: "routine-conv",
			record: func(t *testing.T, h *Hub) {
				n := gradingNotifyUserPush("routine-conv", assistantwire.LevelP1)
				n.Asked, n.RelatedKey = true, "model-chosen subject"
				if err := h.deliverPush(n); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "model-written ask key does not invent a todo", reason: interceptReasonP1,
			machine: "mac", session: "routine-conv",
			record: func(t *testing.T, h *Hub) {
				n := gradingNotifyUserPush("routine-conv", assistantwire.LevelP1)
				n.Asked, n.RelatedKey = true, "ask:not-a-proven-todo"
				if err := h.deliverPush(n); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "recognized todo key remains the primary source", reason: interceptReasonP1,
			todo: "todo_existing", machine: "mac", session: "routine-conv",
			record: func(t *testing.T, h *Hub) {
				n := gradingNotifyUserPush("routine-conv", assistantwire.LevelP1)
				n.Asked, n.RelatedKey = true, "todo:todo_existing@2026-11-14T10:00:00Z"
				if err := h.deliverPush(n); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "finished turn carries its agent session", reason: interceptReasonP2,
			machine: "mac", session: "agent-done",
			record: func(t *testing.T, h *Hub) {
				n := gradingNotification("", "", "finished")
				n.Kind, n.DeviceID, n.SessionID = assistantwire.NotifyDone, "mac", "agent-done"
				if err := h.deliverPush(n); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "held interaction carries its agent session", reason: interceptReasonP1,
			machine: "m1", session: "s1",
			record: func(t *testing.T, h *Hub) {
				ix := proto.Interaction{ID: "ix_source", MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission, Summary: "git push"}
				h.notifyInteractionAt("manager-a", ix, "box", false, assistantwire.LevelP1, "")
			},
		},
		{
			name: "HTTP notify carries its explicit session", reason: interceptReasonP1,
			machine: "mac", session: "agent-api",
			record: func(t *testing.T, h *Hub) {
				_, err := h.deliverNotify(notifyRequest{Title: "API notice", Body: "held", ID: "api-source",
					PushLevel: assistantwire.LevelP1, MachineID: "mac", SessionID: "agent-api"}, "box", "", "manager-a")
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "localized push carries its payload session", reason: interceptReasonP1,
			machine: "mac", session: "agent-localized",
			record: func(t *testing.T, h *Hub) {
				build := func(i18n.Lang) map[string]any {
					return map[string]any{
						"aps":        map[string]any{"alert": map[string]any{"title": "localized", "body": "held"}},
						"machine_id": "mac", "session_id": "agent-localized",
					}
				}
				h.push.sendFrom("manager-a", build, "", nil, quietNone, nil)
			},
		},
		{
			name: "partial session pair stays unset", reason: interceptReasonP1,
			record: func(t *testing.T, h *Hub) {
				build := func(i18n.Lang) map[string]any {
					return map[string]any{
						"aps":        map[string]any{"alert": map[string]any{"title": "partial", "body": "held"}},
						"machine_id": "mac",
					}
				}
				h.push.sendFrom("manager-a", build, "", nil, quietNone, nil)
			},
		},
		{
			name: "generic notify without a source stays read-only", reason: interceptReasonP1,
			record: func(t *testing.T, h *Hub) {
				_, err := h.deliverNotify(notifyRequest{Title: "source-less", ID: "source-less",
					PushLevel: assistantwire.LevelP1}, "", "", "manager-a")
				if err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "dedup record retains the held push source", reason: interceptReasonDedup,
			machine: "mac", session: "agent-dedup",
			record: func(t *testing.T, h *Hub) {
				const key = "notify:manager-a//dedup-source"
				if allow, _ := h.push.applyPushGrading(pushPolicy{level: assistantwire.LevelP0, relatedKey: key, manager: "manager-a"}); !allow {
					t.Fatal("the P0 setup push was held")
				}
				_, err := h.deliverNotify(notifyRequest{Title: "dedup", ID: "dedup-source",
					PushLevel: assistantwire.LevelP2, MachineID: "mac", SessionID: "agent-dedup"}, "box", "", "manager-a")
				if err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := gradingHub(t)
			if err := h.push.setPushGrading(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			tc.record(t, h)
			assert := func(stage string, view []heldPushRecord) {
				t.Helper()
				if len(view) != 1 {
					t.Fatalf("%s: got %d held rows, want 1: %+v", stage, len(view), view)
				}
				got := view[0]
				if got.Reason != tc.reason || got.TodoID != tc.todo || got.MachineID != tc.machine || got.SessionID != tc.session {
					t.Fatalf("%s: source = reason %q todo %q machine %q session %q; want %q %q %q %q (row %+v)",
						stage, got.Reason, got.TodoID, got.MachineID, got.SessionID, tc.reason, tc.todo, tc.machine, tc.session, got)
				}
			}
			assert("in memory", h.push.heldPushesView("", 0))
			if !h.push.waitInterceptsSaved(2 * time.Second) {
				t.Fatal("the held push was not saved")
			}

			store := h.push.store.(*gradingStore)
			store.mu.Lock()
			raw := append([]byte(nil), store.vals[heldPushesSettingKey]...)
			store.mu.Unlock()
			var rows []map[string]any
			if err := json.Unmarshal(raw, &rows); err != nil || len(rows) != 1 {
				t.Fatalf("stored rows = %s (err %v), want one", raw, err)
			}
			for key, want := range map[string]string{"todo_id": tc.todo, "machine_id": tc.machine, "session_id": tc.session} {
				got, present := rows[0][key]
				if want == "" && present {
					t.Fatalf("stored %s = %#v, want the optional key omitted: %s", key, got, raw)
				}
				if want != "" && (!present || got != want) {
					t.Fatalf("stored %s = %#v (present %v), want %q: %s", key, got, present, want, raw)
				}
			}

			restarted := restartPusher(t, h, store)
			assert("after restart", restarted.heldPushesView("", 0))
		})
	}
}

// A merged row has only one destination. Never let it absorb a different push's source, including an older row with
// no source at all; identical sources keep the existing same-minute and same-key coalescing behavior.
func TestPushGradingP1MergesOnlyCompatibleSources(t *testing.T) {
	notification := func(title, key, machine, session string) assistantwire.Notification {
		n := gradingNotification(assistantwire.LevelP1, key, title)
		n.DeviceID, n.SessionID = machine, session
		return n
	}
	tests := []struct {
		name                               string
		first                              assistantwire.Notification
		second                             assistantwire.Notification
		wantRows                           int
		wantAlso                           int
		wantTodo, wantMachine, wantSession string
		moveMinute                         bool
	}{
		{
			name: "identical sources merge", wantRows: 1, wantAlso: 1, wantMachine: "mac", wantSession: "agent-a",
			first: notification("first", "subject-a", "mac", "agent-a"), second: notification("second", "subject-b", "mac", "agent-a"),
		},
		{
			name: "different sessions stay separate", wantRows: 2,
			first: notification("first", "subject-a", "mac", "agent-a"), second: notification("second", "subject-b", "mac", "agent-b"),
		},
		{
			name: "missing and present sources stay separate", wantRows: 2,
			first: notification("first", "subject-a", "", ""), second: notification("second", "subject-b", "mac", "agent-b"),
		},
		{
			name: "same key with different sources stays separate", wantRows: 2, moveMinute: true,
			first: notification("first", "shared", "mac", "agent-a"), second: notification("second", "shared", "mac", "agent-b"),
		},
		{
			name: "different todos stay separate", wantRows: 2,
			first: notification("first", "ask:todo_a", "mac", "agent-a"), second: notification("second", "ask:todo_b", "mac", "agent-a"),
		},
		{
			name: "different related URLs stay separate", wantRows: 2,
			first: notification("first", "https://example.test/a", "", ""), second: notification("second", "https://example.test/b", "", ""),
		},
		{
			name: "different web-special URLs stay separate", wantRows: 2,
			first: notification("first", "https:foo", "", ""), second: notification("second", "https:bar", "", ""),
		},
		{
			name: "legacy clickable and source-less rows stay separate", wantRows: 2,
			first: notification("first", "https://example.test/a", "", ""), second: notification("second", "subject-b", "", ""),
		},
		{
			name: "same related URL merges", wantRows: 1, wantAlso: 1,
			first: notification("first", "https://example.test/a", "", ""), second: notification("second", "https://example.test/a", "", ""),
		},
		{
			name: "same legacy todo merges across rounds", wantRows: 1, wantAlso: 1, wantTodo: "todo_same",
			first: notification("first", "todo:todo_same@round-1", "", ""), second: notification("second", "todo:todo_same@round-2", "", ""),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := gradingHub(t)
			if err := h.push.setPushGrading(context.Background(), true); err != nil {
				t.Fatal(err)
			}
			if err := h.deliverPush(tc.first); err != nil {
				t.Fatal(err)
			}
			if tc.moveMinute {
				moveGradingClock(h, h.push.now().Add(time.Minute))
			}
			if err := h.deliverPush(tc.second); err != nil {
				t.Fatal(err)
			}
			view := h.push.heldPushesView("", 0)
			if len(view) != tc.wantRows {
				t.Fatalf("got %d rows, want %d: %+v", len(view), tc.wantRows, view)
			}
			if tc.wantRows == 1 {
				got := view[0]
				if len(got.Also) != tc.wantAlso || got.TodoID != tc.wantTodo || got.MachineID != tc.wantMachine || got.SessionID != tc.wantSession {
					t.Fatalf("merged row = %+v, want the intact shared source and %d also titles", got, tc.wantAlso)
				}
			}
		})
	}
}

// The source is captured before applyPushGrading makes its own mode check. If the mode changes during payload construction,
// the newly held card still has its session; no goroutine or timing guess is needed to exercise that interleaving.
func TestPushGradingCardSourcePresentWhenModeChangesDuringSend(t *testing.T) {
	h, _ := gradingHub(t)
	h.push.gradingMu.Lock()
	enabled := false
	h.push.gradingCached = &enabled
	h.push.gradingCachedAt = h.push.now()
	h.push.gradingMu.Unlock()

	ix := proto.Interaction{ID: "ix_toggle", MachineID: "m1", SessionID: "s1", Kind: proto.IxPermission, Summary: "git push"}
	build := func(l i18n.Lang) map[string]any {
		h.push.gradingMu.Lock()
		enabled = true
		h.push.gradingMu.Unlock()
		return interactionPayloadIn(l, ix, "box")
	}
	attempts := gradingAttempts(h)
	h.push.sendCard("manager-a", pushPolicy{level: assistantwire.LevelP1, relatedKey: "ix:ix_toggle", title: ix.Summary},
		build, ix.ID, nil, quietNone, nil)
	if *attempts != 0 {
		t.Fatalf("the card alerted %d times after ln mode turned on, want 0", *attempts)
	}
	view := h.push.heldPushesView("", 0)
	if len(view) != 1 || view[0].MachineID != "m1" || view[0].SessionID != "s1" {
		t.Fatalf("held card after the mode change = %+v, want its complete session source", view)
	}
}
