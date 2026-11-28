package node

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func confirmedMissingFlag(err error) bool {
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != codeNotFound || len(re.Data) == 0 {
		return false
	}
	var data struct {
		ConfirmedMissing bool `json:"confirmed_missing"`
	}
	return json.Unmarshal(re.Data, &data) == nil && data.ConfirmedMissing
}

func TestSessionInfoReturnsStructuredNotFoundReasons(t *testing.T) {
	n, home := testNode(t)
	const (
		unknownID  = "99999999-2222-3333-4444-555555555555"
		knownID    = "11111111-2222-3333-4444-555555555555"
		startingID = "22222222-2222-3333-4444-555555555555"
		unlistedRT = "agy-33333333-4444-4555-8666-777777777777"
	)

	// Unknown non-runtime session after records loaded: typed not_found with unknown_session.
	_, err := n.handle(context.Background(), "session.info", json.RawMessage(`{"session_id":"`+unknownID+`"}`))
	if rpcCode(err) != codeNotFound || notFoundReason(err) != proto.NotFoundUnknownSession {
		t.Fatalf("unknown session.info = code %q reason %q (%v), want not_found/unknown_session", rpcCode(err), notFoundReason(err), err)
	}

	// Known session without transcript: no_transcript (never unknown_session).
	n.state.put(knownID, sessionState{Launcher: "claude", Cwd: home, StartedAt: time.Now().Add(-time.Hour).UTC()})
	_, err = n.handle(context.Background(), "session.info", json.RawMessage(`{"session_id":"`+knownID+`"}`))
	if rpcCode(err) != codeNotFound || notFoundReason(err) != proto.NotFoundNoTranscript {
		t.Fatalf("known session.info = code %q reason %q (%v), want not_found/no_transcript", rpcCode(err), notFoundReason(err), err)
	}

	// Starting session without state row yet: no_transcript (never unknown_session).
	n.startsBegun.begin(startingID, time.Minute)
	_, err = n.handle(context.Background(), "session.info", json.RawMessage(`{"session_id":"`+startingID+`"}`))
	if rpcCode(err) != codeNotFound || notFoundReason(err) != proto.NotFoundNoTranscript {
		t.Fatalf("starting session.info = code %q reason %q (%v), want not_found/no_transcript", rpcCode(err), notFoundReason(err), err)
	}

	// While records are still loading from the hub, unknown IDs omit reason.
	n.state.beginLoad()
	_, err = n.handle(context.Background(), "session.info", json.RawMessage(`{"session_id":"`+unknownID+`"}`))
	n.state.endLoad()
	if rpcCode(err) != codeNotFound || notFoundReason(err) != "" {
		t.Fatalf("loading session.info = code %q reason %q (%v), want not_found without reason", rpcCode(err), notFoundReason(err), err)
	}

	// Runtime-shaped session when no owning runtime is connected must not claim unknown_session or confirmed_missing.
	_, err = n.handle(context.Background(), "session.info", json.RawMessage(`{"session_id":"`+unlistedRT+`"}`))
	if rpcCode(err) != codeNotFound || notFoundReason(err) != "" || confirmedMissingFlag(err) {
		t.Fatalf("disconnected runtime session.info = code %q reason %q confirmed=%v (%v), want plain not_found", rpcCode(err), notFoundReason(err), confirmedMissingFlag(err), err)
	}
}

func TestRuntimeForMarksConfirmedMissingAndVetoesLocalAgyHold(t *testing.T) {
	n, _ := runtimeNode(t)
	prevProbe := agyPresenceLockProbe
	probeState := agyLockMissing
	agyPresenceLockProbe = func(string) agyLockState { return probeState }
	t.Cleanup(func() { agyPresenceLockProbe = prevProbe })

	const (
		agedOutID = "agy-11111111-2222-4333-8444-555555555555"
		missingID = "agy-99999999-8888-4777-8666-555555555555"
		heldID    = "agy-77777777-6666-4555-8444-333333333333"
		paneUUID  = "88888888-7777-4666-8555-444444444444"
		paneID    = "agy-" + paneUUID
	)

	agy := dialRuntime(t, n, map[string]any{
		"runtime": "agy",
		"name":    "Antigravity",
		"agent":   "agy",
		"version": "test",
		"caps":    map[string]any{"start": true, "send": true, "interrupt": true, "close": true},
	})
	agy.answer = func(method string, params json.RawMessage) (any, error) {
		if method == "session.items" {
			var p struct {
				SessionID string `json:"session_id"`
			}
			_ = json.Unmarshal(params, &p)
			if p.SessionID == agedOutID {
				return map[string]any{"items": []any{}, "to_seq": 1}, nil
			}
			return nil, &rpc.Error{Code: codeNotFound, Message: "Antigravity session not found"}
		}
		return map[string]any{"ok": true}, nil
	}

	// Aged-out session still answered by runtime: session.info succeeds (never confirmed_missing).
	if _, err := nodeCall(t, n, "session.info", map[string]any{"session_id": agedOutID}); err != nil {
		t.Fatalf("aged-out runtime session.info failed: %v", err)
	}

	// Truly nonexistent runtime session with no local pane or lock: confirmed_missing=true, original message preserved.
	_, err := nodeCall(t, n, "session.info", map[string]any{"session_id": missingID})
	if rpcCode(err) != codeNotFound || !confirmedMissingFlag(err) || !strings.Contains(err.Error(), "Antigravity session not found") {
		t.Fatalf("missing runtime session.info = code %q confirmed=%v err=%v, want confirmed_missing=true", rpcCode(err), confirmedMissingFlag(err), err)
	}

	// Cached goneOf must re-check live local presence locks: once a presence lock becomes held or unknown,
	// confirmed_missing is vetoed immediately even within the unclaim cache window.
	probeState = agyLockHeld
	_, err = nodeCall(t, n, "session.info", map[string]any{"session_id": missingID})
	if rpcCode(err) != codeNotFound || confirmedMissingFlag(err) {
		t.Fatalf("cached gone with held presence lock = code %q confirmed=%v (%v), want vetoed", rpcCode(err), confirmedMissingFlag(err), err)
	}

	probeState = agyLockUnknown
	_, err = nodeCall(t, n, "session.info", map[string]any{"session_id": heldID})
	if rpcCode(err) != codeNotFound || confirmedMissingFlag(err) {
		t.Fatalf("unknown presence lock = code %q confirmed=%v (%v), want vetoed", rpcCode(err), confirmedMissingFlag(err), err)
	}
	probeState = agyLockMissing

	// Twin terminal panes attributing the same conversation (alias-suppressed) still veto confirmed_missing.
	n.mu.Lock()
	n.agents["agy-w1-p1"] = host.Agent{
		Agent: agyAgent, PaneID: "w1:p1", AgentStatus: proto.StatusWorking,
		AgentSession: &host.AgentSession{Source: agyPresenceSource, Agent: agyAgent, Kind: "id", Value: paneUUID},
	}
	n.agents["agy-w2-p2"] = host.Agent{
		Agent: agyAgent, PaneID: "w2:p2", AgentStatus: proto.StatusWorking,
		AgentSession: &host.AgentSession{Source: agyPresenceSource, Agent: agyAgent, Kind: "id", Value: paneUUID},
	}
	n.mu.Unlock()

	_, err = nodeCall(t, n, "session.info", map[string]any{"session_id": paneID})
	if rpcCode(err) != codeNotFound || confirmedMissingFlag(err) {
		t.Fatalf("twin-pane attributed agy session = code %q confirmed=%v (%v), want vetoed", rpcCode(err), confirmedMissingFlag(err), err)
	}
}
