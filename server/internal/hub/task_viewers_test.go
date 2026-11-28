package hub

import (
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func TestViewedSessionsKeepOtherClientsAndDropDisconnectedClients(t *testing.T) {
	one := &client{role: roleFull, subs: map[[2]string]bool{{"box", "agent"}: true}}
	two := &client{role: roleFull, subs: map[[2]string]bool{{"box", "agent"}: true, {"box", "other"}: true}}
	h := &Hub{clients: map[*client]struct{}{one: {}, two: {}}}
	want := []assistantwire.SessionRef{{MachineID: "box", SessionID: "agent"}, {MachineID: "box", SessionID: "other"}}
	if got := h.viewedSessions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("deduplicated viewers: %+v", got)
	}
	delete(one.subs, [2]string{"box", "agent"})
	if got := h.viewedSessions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("one client's leaving dropped the other viewer: %+v", got)
	}
	h.mu.Lock()
	delete(h.clients, two)
	h.mu.Unlock()
	if got := h.viewedSessions(); len(got) != 0 {
		t.Fatalf("disconnected client's subscriptions still protect tasks: %+v", got)
	}
}

func TestAppSubscriptionChangesReportTaskViewers(t *testing.T) {
	mh, _, token := subscribeBox(t)
	one, two := dialApp(t, mh, token), dialApp(t, mh, token)
	viewers := func() []assistantwire.SessionRef {
		mh.h.reported.mu.Lock()
		defer mh.h.reported.mu.Unlock()
		return append([]assistantwire.SessionRef(nil), mh.h.reported.clients.Viewing...)
	}
	one.send(t, map[string]any{"type": "subscribe", "machine_id": "box", "session_id": "s1"})
	want := []assistantwire.SessionRef{{MachineID: "box", SessionID: "s1"}}
	eventually(t, 5*time.Second, "view reported after subscribe", func() bool { return reflect.DeepEqual(viewers(), want) })
	two.send(t, map[string]any{"type": "subscribe", "machine_id": "box", "session_id": "s1"})
	two.pongWithin(t, "subscribed", "after subscribing the second viewer")
	one.send(t, map[string]any{"type": "unsubscribe", "machine_id": "box", "session_id": "s1"})
	one.pongWithin(t, "left", "after unsubscribing the first viewer")
	if got := viewers(); !reflect.DeepEqual(got, want) {
		t.Fatalf("one app's leaving dropped the other viewer: %+v", got)
	}
	_ = two.conn.CloseNow()
	eventually(t, 5*time.Second, "view forgotten after disconnect", func() bool { return len(viewers()) == 0 })
}

func TestTerminalOnlyViewerProtectsItsSession(t *testing.T) {
	c := &client{role: roleFull, termSubs: map[[3]string]bool{{"box", "agent", "box"}: true}}
	h := &Hub{clients: map[*client]struct{}{c: {}}}
	want := []assistantwire.SessionRef{{MachineID: "box", SessionID: "agent"}}
	if got := h.viewedSessions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("terminal-only viewer was ignored: %+v", got)
	}
}

func TestViewedSessionsExcludeRestrictedApps(t *testing.T) {
	for _, role := range []string{roleScoped, roleAnswer, roleExtension} {
		t.Run(role, func(t *testing.T) {
			c := &client{
				role:     role,
				subs:     map[[2]string]bool{{"box", "chat"}: true},
				termSubs: map[[3]string]bool{{"box", "terminal", "box"}: true},
			}
			h := &Hub{clients: map[*client]struct{}{c: {}}}
			if got := h.viewedSessions(); len(got) != 0 {
				t.Fatalf("restricted app counted as owner presence: %+v", got)
			}
		})
	}
}

func TestTaskViewersAreScopedToTheirManager(t *testing.T) {
	h := &Hub{}
	h.devices.kept = map[string]string{"box": "manager-a", "other": "manager-b"}
	c := assistantwire.Clients{Elicitation: true, Viewing: []assistantwire.SessionRef{
		{MachineID: "box", SessionID: "agent"}, {MachineID: "other", SessionID: "private-agent"},
	}}
	got := h.clientsForManager(c, "manager-a")
	want := []assistantwire.SessionRef{{MachineID: "box", SessionID: "agent"}}
	if !got.Elicitation || !reflect.DeepEqual(got.Viewing, want) || len(c.Viewing) != 2 {
		t.Fatalf("viewer report crossed a manager boundary or mutated the original: %+v", got)
	}
}
