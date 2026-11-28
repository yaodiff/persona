package node

import (
	"errors"
	"log"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestShareCreationResultDistinguishesAbsentAndPartialReceiptRequests(t *testing.T) {
	for _, tc := range []struct{ creation, incarnation, result string }{
		{"", "", "receipt_not_requested"},
		{"creation", "", "identity_missing"},
		{"", "incarnation", "identity_missing"},
	} {
		t.Run(tc.result+tc.creation, func(t *testing.T) {
			var line strings.Builder
			n := &Node{state: newState(), logger: log.New(&line, "", 0)}
			const id = "synthetic-start"
			out, err := n.creationResult(&proto.Session{SessionID: id}, nil, "", tc.creation, tc.incarnation)
			if err != nil || len(out.(map[string]any)) != 1 {
				t.Fatalf("unexpected reply: %+v %v", out, err)
			}
			if _, ok := n.state.get(id); ok || !strings.Contains(line.String(), `"result":"`+tc.result+`"`) {
				t.Fatalf("unexpected receipt state or log: %v %s", ok, line.String())
			}
		})
	}
}

func TestShareCreationResultPreservesReceiptAcrossStateUpdates(t *testing.T) {
	n := &Node{state: newState()}
	count := 0
	n.state.persist = func(id string, s sessionState) {
		if s.CreationID != "" {
			count++
		}
	}
	for _, id := range []string{"synthetic-terminal", "synthetic-async", "synthetic-runtime-launcher", "synthetic-engine-runtime"} {
		s := &proto.Session{SessionID: id, Launcher: "synthetic-launcher", Cwd: "/workspace", Starting: id == "synthetic-async"}
		if _, err := n.creationResult(s, nil, "", "creation:"+id, "incarnation:"+id); err != nil {
			t.Fatal(err)
		}
		n.state.put(id, sessionState{Launcher: "synthetic-launcher", Cwd: "/workspace"})
		state, ok := n.state.get(id)
		if !ok || state.CreationID != "creation:"+id || state.Incarnation != "incarnation:"+id {
			t.Fatal("state update lost immutable receipt")
		}
	}
	if count != 8 {
		t.Fatal("creation state was not emitted durably")
	}
	resumed := &proto.Session{SessionID: "synthetic-resume"}
	_, _ = n.creationResult(resumed, nil, "old-session", "new-creation", "new-incarnation")
	if _, ok := n.state.get(resumed.SessionID); ok {
		t.Fatal("resume established ownership receipt")
	}
	_, _ = n.creationResult(&proto.Session{SessionID: "synthetic-failed"}, errors.New("synthetic failure"), "", "new-creation", "new-incarnation")
	if _, ok := n.state.get("synthetic-failed"); ok {
		t.Fatal("failure established receipt")
	}
}
