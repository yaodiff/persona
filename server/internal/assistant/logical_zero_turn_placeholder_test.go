package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type zeroTurnPlaceholderRig struct {
	s                     *Service
	d                     *db.DB
	physical              *fleetDevs
	mu                    sync.Mutex
	prepared              int
	sends                 []map[string]any
	liveSessions          []proto.Session
	listErr               error
	laggingList           bool
	preparedZeroTurnAlive bool
}

func setupZeroTurnPlaceholderRig(t *testing.T, earlierFrozen int, missingMode string, keepActiveSwitch bool) *zeroTurnPlaceholderRig {
	t.Helper()
	s, f, d, _ := historyFixture(t, func(string, ...any) {})
	ctx := t.Context()

	if err := d.AdoptConversation(ctx, "server", "side-chat", "seg-one", "claude", "h1"); err != nil {
		t.Fatal(err)
	}
	if earlierFrozen > 0 {
		if err := d.PutConversationPage(ctx, db.ConversationPage{
			MachineID:  "server",
			SessionID:  "side-chat",
			History:    "h1",
			Revision:   100,
			ThroughSeq: earlierFrozen,
			Tail:       true,
			Items: []proto.Item{
				{ID: "u1#0", Seq: 1, Role: "user", Kind: proto.KindText, Text: "earlier question"},
				{ID: "a1#0", Seq: earlierFrozen, Role: "assistant", Kind: proto.KindText, Text: "earlier answer"},
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	req, _ := json.Marshal(logicalSwitchRequest{
		OperationID:    "initial-switch",
		ExpectedEpoch:  1,
		MachineID:      "server",
		Launcher:       "codex",
		Model:          "target-model",
		Effort:         "medium",
		PermissionMode: "default",
	})
	if err := d.BeginConversationSwitch(ctx, "server", "side-chat", db.ConversationSwitch{
		OperationID:   "initial-switch",
		ExpectedEpoch: 1,
		SourceSegment: "seg-one",
		Request:       req,
	}); err != nil {
		t.Fatal(err)
	}
	if err := d.PutConversationHandoff(ctx, "server", "side-chat", "initial-switch", json.RawMessage(`{"summary":"earlier handoff summary","recent":[{"role":"user","text":"earlier question"}]}`)); err != nil {
		t.Fatal(err)
	}
	// Mirrors logical_switch.go: PrepareConversationSegment stores an empty SourceHistory
	// before the prepared target segment has produced its first transcript turn.
	var targetSeg conversationlog.Segment
	targetSeg.ID = "seg-two"
	targetSeg.MachineID = "server"
	targetSeg.SessionID = "prepared-zero-turn"
	targetSeg.Engine = "codex"
	targetSeg.Launcher = "codex"
	targetSeg.Model = "target-model"
	targetSeg.Effort = "medium"
	targetSeg.PermissionMode = "default"
	if err := d.PrepareConversationSegment(ctx, "server", "side-chat", "initial-switch", targetSeg, earlierFrozen); err != nil {
		t.Fatal(err)
	}
	if err := d.ActivateConversationSegment(ctx, "server", "side-chat", "initial-switch", 1); err != nil {
		t.Fatal(err)
	}
	if !keepActiveSwitch {
		accepted, err := d.AcceptIdleConversationSwitch(ctx, "server", "side-chat", "initial-switch", 1)
		if err != nil || !accepted {
			t.Fatalf("accept idle switch: accepted=%v err=%v", accepted, err)
		}
	}

	physical := newFleetDevs()
	physical.add("server", "Computer")
	sessions := []proto.Session{
		{SessionID: "main-chat", Cwd: "/home/u/.persona/assistant", Assistant: true, Rev: 100},
	}
	if keepActiveSwitch {
		sessions = append(sessions, proto.Session{SessionID: "side-chat", Cwd: "/home/u/.persona/assistant", Assistant: true, Agent: "claude", Rev: 100})
	}
	physical.setSessions("server", sessions)

	rig := &zeroTurnPlaceholderRig{s: s, d: d, physical: physical, liveSessions: slices.Clone(sessions)}
	nodeCall := func(method string, params json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		switch method {
		case DeviceSessionsList:
			rig.mu.Lock()
			listErr := rig.listErr
			live := slices.Clone(rig.liveSessions)
			rig.mu.Unlock()
			if listErr != nil {
				return nil, listErr
			}
			return map[string]any{"sessions": live}, nil
		case "assistant.segment.check":
			launcher, _ := p["launcher"].(string)
			engine := "codex"
			if launcher == "claude" || launcher == "sub" {
				engine = "claude"
			}
			return segmentTarget{Registered: true, Engine: engine, Prepare: true, SupervisionPolicy: true}, nil
		case "assistant.segment.prepare":
			rig.mu.Lock()
			rig.prepared++
			id := fmt.Sprintf("recovered-seg-%d", rig.prepared)
			lagging := rig.laggingList
			rig.mu.Unlock()
			launcher, _ := p["launcher"].(string)
			agent := "codex"
			if launcher == "claude" || launcher == "sub" {
				agent = "claude"
			}
			model, _ := p["model"].(string)
			effort, _ := p["effort"].(string)
			perm, _ := p["permission_mode"].(string)
			row := proto.Session{
				SessionID:      id,
				MachineID:      "server",
				Cwd:            "/home/u/.persona/assistant",
				Assistant:      true,
				Agent:          agent,
				Launcher:       launcher,
				Model:          model,
				RequestedModel: &model,
				Effort:         effort,
				PermissionMode: perm,
				Status:         proto.StatusIdle,
				State:          proto.StateIdle,
			}
			rig.mu.Lock()
			rig.liveSessions = append(rig.liveSessions, row)
			rig.mu.Unlock()
			if !lagging {
				physical.mu.Lock()
				physical.machines["server"].sessions = append(physical.machines["server"].sessions, row)
				physical.mu.Unlock()
			}
			return map[string]any{"session_id": id, "prepared": true}, nil
		case "assistant.segment.retire", "assistant.segment.restore", "session.close":
			return map[string]bool{"ok": true}, nil
		case DeviceAssistantSend, DeviceSessionSend, assistantwire.DeviceAssistantAnswerAgain:
			sid, _ := p["session_id"].(string)
			rig.mu.Lock()
			alive := rig.preparedZeroTurnAlive
			rig.mu.Unlock()
			if sid == "prepared-zero-turn" && !alive {
				return nil, &rpc.Error{Code: codeSessionNotFound, Message: "session prepared-zero-turn not found"}
			}
			rig.mu.Lock()
			rig.sends = append(rig.sends, p)
			rig.mu.Unlock()
			return map[string]any{"ok": true, "session_id": sid}, nil
		case DeviceSessionItems:
			sid, _ := p["session_id"].(string)
			if p["user_inputs"] == true {
				if sid == "side-chat" && earlierFrozen > 0 {
					return proto.UserInputIndex{
						Total:   1,
						History: "h1",
						Inputs:  []proto.UserInputPosition{{ID: "u1#0", Seq: 1, Preview: "earlier question", Answer: "earlier answer"}},
					}, nil
				}
				if missingMode == "empty" {
					return proto.UserInputIndex{Inputs: []proto.UserInputPosition{}}, nil
				}
				return nil, &rpc.Error{Code: codeSessionNotFound, Message: fmt.Sprintf("transcript for session %s not found", sid), Data: json.RawMessage(`{"reason":"no_transcript"}`)}
			}
			if sid == "side-chat" && earlierFrozen > 0 {
				fromSeq, toSeq := 0, earlierFrozen
				hasMore := false
				return historyPage{
					History: "h1",
					FromSeq: &fromSeq,
					ToSeq:   &toSeq,
					HasMore: &hasMore,
					Items: []proto.Item{
						{ID: "u1#0", Seq: 1, Role: "user", Kind: proto.KindText, Text: "earlier question"},
						{ID: "a1#0", Seq: earlierFrozen, Role: "assistant", Kind: proto.KindText, Text: "earlier answer"},
					},
				}, nil
			}
			if sid == "prepared-zero-turn" || strings.HasPrefix(sid, "recovered-seg-") {
				if missingMode == "empty" {
					return map[string]any{"items": []proto.Item{}, "has_more": false}, nil
				}
				return nil, &rpc.Error{Code: codeSessionNotFound, Message: fmt.Sprintf("transcript for session %s not found", sid), Data: json.RawMessage(`{"reason":"no_transcript"}`)}
			}
			return map[string]any{"items": []proto.Item{}, "has_more": false}, nil
		case proto.MethodSessionItemsWindow:
			var req proto.ItemsWindowRequest
			_ = json.Unmarshal(params, &req)
			if req.SessionID == "side-chat" && earlierFrozen > 0 {
				return proto.ItemsWindow{
					History: "h1",
					FromSeq: req.FromSeq - 1,
					ToSeq:   min(req.ThroughSeq, earlierFrozen),
					HasMore: req.FromSeq > 1,
					Items: []proto.Item{
						{ID: "u1#0", Seq: 1, Role: "user", Kind: proto.KindText, Text: "earlier question"},
					},
				}, nil
			}
			if missingMode == "empty" {
				return proto.ItemsWindow{Items: []proto.Item{}}, nil
			}
			return nil, &rpc.Error{Code: codeSessionNotFound, Message: fmt.Sprintf("transcript for session %s not found", req.SessionID), Data: json.RawMessage(`{"reason":"no_transcript"}`)}
		default:
			return map[string]bool{"ok": true}, nil
		}
	}
	physical.machines["server"].call = nodeCall
	f.answerNode(DeviceSessionItems, func(_ string, params json.RawMessage) (any, error) {
		return nodeCall(DeviceSessionItems, params)
	})
	f.answerNode(proto.MethodSessionItemsWindow, func(_ string, params json.RawMessage) (any, error) {
		return nodeCall(proto.MethodSessionItemsWindow, params)
	})

	s.devs = &logicalDevices{Devices: physical, s: s}
	s.logicalChanged()
	return rig
}

func (r *zeroTurnPlaceholderRig) sentTo(sid string) []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, p := range r.sends {
		if p["session_id"] == sid {
			out = append(out, p)
		}
	}
	return out
}

func TestZeroTurnPlaceholderSwitchRecoversWithoutDeadlock(t *testing.T) {
	for _, mode := range []string{"not_found", "empty"} {
		t.Run(mode, func(t *testing.T) {
			rig := setupZeroTurnPlaceholderRig(t, 6, mode, false)
			ctx := t.Context()

			placeholder, known := rig.s.nodeSession("server", "side-chat")
			if !known || !placeholder.LogicalPlaceholder || !placeholder.Assistant {
				t.Fatalf("expected LogicalPlaceholder before switch: %+v known=%v", placeholder, known)
			}

			code, raw := conversationSwitchTestCall(t, rig.s, http.MethodPost, "/api/v1/assistant/conversation-switches", "", `{"conversation_ids":["side-chat"],"launcher":"claude","model":"recovered-model","effort":"high"}`)
			started := conversationSwitchTestDecode(t, code, raw)
			item := conversationSwitchTestItemFor(t, started, "side-chat")
			if item.Outcome != conversationSwitchSwitching {
				t.Fatalf("switch did not start from 0-turn placeholder: %+v", started)
			}

			conversationSwitchTestDrive(t, rig.s, started.BatchID, map[string]string{"side-chat": conversationSwitchSwitched})

			c, err := rig.d.LogicalConversation(ctx, "server", "side-chat")
			if err != nil || c == nil {
				t.Fatalf("logical conversation after switch: %+v %v", c, err)
			}
			active, ok := c.Active()
			if !ok || c.Epoch != 3 || !strings.HasPrefix(active.SessionID, "recovered-seg-") || active.Launcher != "claude" {
				t.Fatalf("switch did not activate replacement segment: %+v", c)
			}
			if len(c.Segments) != 3 || c.Segments[1].State != "retired" || c.Segments[1].FrozenEnd == nil || *c.Segments[1].FrozenEnd != 0 {
				t.Fatalf("zero-turn source segment was not cleanly retired at frozen_end=0: %+v", c.Segments)
			}
			handoff, err := rig.d.ConversationPendingHandoff(ctx, "server", "side-chat", active.ID)
			if err != nil || !strings.Contains(string(handoff), "earlier handoff summary") {
				t.Fatalf("switch lost pending handoff from earlier segment: %s err=%v", handoff, err)
			}

			// History remains readable across seg1 (retired, 6) -> seg2 (retired, 0) -> seg3 (active, 0-turn).
			read, err := rig.s.toolConversationRead(ctx, "main-chat", []byte(`{"id":"side-chat","turns":10}`))
			if err != nil {
				t.Fatalf("read history after recovering switch: %v", err)
			}
			if !strings.Contains(read, "earlier question") || !strings.Contains(read, "earlier answer") {
				t.Fatalf("earlier intact segment missing after switch: %s", read)
			}
		})
	}
}

func TestZeroTurnPlaceholderHistoryReadsIntactEarlierOrEmpty(t *testing.T) {
	t.Run("returns_intact_earlier_segments", func(t *testing.T) {
		for _, mode := range []string{"not_found", "empty"} {
			t.Run(mode, func(t *testing.T) {
				rig := setupZeroTurnPlaceholderRig(t, 6, mode, false)
				ctx := t.Context()
				c, err := rig.d.LogicalConversation(ctx, "server", "side-chat")
				if err != nil || c == nil {
					t.Fatalf("load logical conversation: %+v %v", c, err)
				}

				page, err := rig.s.logicalHistoryPage(ctx, rig.d, c, historyParams{Turns: 10, LazyImages: true})
				if err != nil {
					t.Fatalf("backward history page failed on 0-turn placeholder: %v", err)
				}
				if len(page.Items) != 2 || page.Items[0].Text != "earlier question" || page.Items[1].Text != "earlier answer" || page.Partial {
					t.Fatalf("unexpected backward page: %+v", page)
				}

				after := 0
				fwd, err := rig.s.logicalHistoryPage(ctx, rig.d, c, historyParams{After: &after, LazyImages: true})
				if err != nil {
					t.Fatalf("forward history page failed on 0-turn placeholder: %v", err)
				}
				if len(fwd.Items) != 2 || intValue(fwd.ToSeq) != 6 || fwd.Partial {
					t.Fatalf("unexpected forward page: %+v", fwd)
				}

				read, err := rig.s.toolConversationRead(ctx, "main-chat", []byte(`{"id":"side-chat","turns":10}`))
				if err != nil || !strings.Contains(read, "earlier question") || !strings.Contains(read, "earlier answer") {
					t.Fatalf("toolConversationRead failed on 0-turn placeholder: %q err=%v", read, err)
				}

				idxRaw, handled, err := rig.s.logicalCall(ctx, "server", DeviceSessionItems, json.RawMessage(`{"session_id":"side-chat","user_inputs":true}`))
				if !handled || err != nil || !strings.Contains(string(idxRaw), "earlier question") {
					t.Fatalf("user_inputs failed on 0-turn placeholder: %s handled=%v err=%v", idxRaw, handled, err)
				}

				winReq, _ := json.Marshal(proto.ItemsWindowRequest{SessionID: "side-chat", FromSeq: 1, ThroughSeq: 6, History: c.History})
				winRaw, handled, err := rig.s.logicalCall(ctx, "server", proto.MethodSessionItemsWindow, winReq)
				if !handled || err != nil || !strings.Contains(string(winRaw), "earlier question") {
					t.Fatalf("items window failed on 0-turn placeholder: %s handled=%v err=%v", winRaw, handled, err)
				}
			})
		}
	})

	t.Run("returns_empty_history_when_no_earlier_turns", func(t *testing.T) {
		rig := setupZeroTurnPlaceholderRig(t, 0, "not_found", false)
		ctx := t.Context()
		c, err := rig.d.LogicalConversation(ctx, "server", "side-chat")
		if err != nil || c == nil {
			t.Fatalf("load logical conversation: %+v %v", c, err)
		}
		page, err := rig.s.logicalHistoryPage(ctx, rig.d, c, historyParams{Turns: 10, LazyImages: true})
		if err != nil {
			t.Fatalf("empty 0-turn placeholder history errored: %v", err)
		}
		if len(page.Items) != 0 || intValue(page.FromSeq) != 0 || intValue(page.ToSeq) != 0 || page.Partial {
			t.Fatalf("expected clean empty page: %+v", page)
		}
	})
}

func TestZeroTurnPlaceholderSendSelfHealsAndDeliversFirstTurn(t *testing.T) {
	t.Run("conversation_send_recovers", func(t *testing.T) {
		rig := setupZeroTurnPlaceholderRig(t, 6, "not_found", false)
		ctx := t.Context()

		reply, err := rig.s.toolConversationSend(ctx, "main-chat", []byte(`{"id":"side-chat","text":"first real message"}`))
		if err != nil {
			t.Fatalf("conversation_send refused 0-turn placeholder: %v", err)
		}
		if !strings.Contains(reply, "side-chat") {
			t.Fatalf("unexpected conversation_send reply: %q", reply)
		}

		waitFor(t, "zero-turn placeholder self-heal delivery", func() bool {
			rig.s.logicalSweep(ctx)
			return len(rig.sentTo("recovered-seg-1")) == 1
		})
		// The sweep delivers the first turn and then settles the switch in the same goroutine; the send returns
		// before its accept write, so wait for that goroutine to finish before reading the conversation.
		conversationSwitchTestSettle(t, rig.s)

		if stale := rig.sentTo("prepared-zero-turn"); len(stale) != 0 {
			t.Fatalf("message dispatched to vanished 0-turn segment: %+v", stale)
		}
		delivered := rig.sentTo("recovered-seg-1")[0]
		if text := fmt.Sprint(delivered["text"]); !strings.Contains(text, "first real message") || !strings.Contains(text, "earlier handoff summary") {
			t.Fatalf("recovered first turn lost text or handoff: %+v", delivered)
		}

		c, err := rig.d.LogicalConversation(ctx, "server", "side-chat")
		if err != nil || c == nil || c.Epoch != 3 || c.Switch != nil {
			t.Fatalf("conversation did not settle onto recovered segment: %+v %v", c, err)
		}
		if active, ok := c.Active(); !ok || active.SessionID != "recovered-seg-1" || active.Launcher != "codex" || active.Model != "target-model" {
			t.Fatalf("recovered active segment changed config: %+v", active)
		}
		row, known := rig.s.nodeSession("server", "side-chat")
		if !known || row.LogicalPlaceholder {
			t.Fatalf("conversation remained a placeholder after self-heal: %+v known=%v", row, known)
		}
	})

	t.Run("direct_session_send_recovers", func(t *testing.T) {
		rig := setupZeroTurnPlaceholderRig(t, 6, "not_found", false)
		ctx := t.Context()

		raw, handled, err := rig.s.logicalCall(ctx, "server", DeviceSessionSend, json.RawMessage(`{"session_id":"side-chat","client_msg_id":"owner-first-1","text":"hello from app"}`))
		if !handled || err != nil || !strings.Contains(string(raw), `"queued":true`) {
			t.Fatalf("direct session.send did not queue for self-heal: %s handled=%v err=%v", raw, handled, err)
		}

		waitFor(t, "direct session.send self-heal delivery", func() bool {
			rig.s.logicalSweep(ctx)
			return len(rig.sentTo("recovered-seg-1")) == 1
		})

		delivered := rig.sentTo("recovered-seg-1")[0]
		if delivered["client_msg_id"] != "owner-first-1" || !strings.Contains(fmt.Sprint(delivered["text"]), "hello from app") || !strings.Contains(fmt.Sprint(delivered["text"]), "earlier handoff summary") {
			t.Fatalf("direct first turn lost identity or handoff: %+v", delivered)
		}
	})

	t.Run("active_first_turn_switch_rolls_back_instead_of_in_doubt", func(t *testing.T) {
		rig := setupZeroTurnPlaceholderRig(t, 6, "not_found", true)
		ctx := t.Context()

		raw, handled, err := rig.s.logicalCall(ctx, "server", DeviceSessionSend, json.RawMessage(`{"session_id":"side-chat","client_msg_id":"ft-msg-1","text":"recover to source"}`))
		if !handled || err != nil || !strings.Contains(string(raw), `"queued":true`) {
			t.Fatalf("queue under active switch: %s handled=%v err=%v", raw, handled, err)
		}

		waitFor(t, "rollback and delivery to intact source segment", func() bool {
			rig.s.logicalSweep(ctx)
			return len(rig.sentTo("side-chat")) == 1
		})

		c, err := rig.d.LogicalConversation(ctx, "server", "side-chat")
		if err != nil || c == nil || c.Epoch != 3 || c.Switch != nil {
			t.Fatalf("active switch did not roll back cleanly: %+v %v", c, err)
		}
		if active, ok := c.Active(); !ok || active.SessionID != "side-chat" {
			t.Fatalf("expected rollback to intact seg-one: %+v", active)
		}
	})
}

func TestMissingTranscriptWithRealHistoryRemainsProtected(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T) *zeroTurnPlaceholderRig
	}{
		{
			name: "observed_source_history_missing_transcript",
			setup: func(t *testing.T) *zeroTurnPlaceholderRig {
				rig := setupZeroTurnPlaceholderRig(t, 6, "not_found", false)
				if err := rig.d.ObserveConversationSource(t.Context(), "server", "prepared-zero-turn", "h2-real"); err != nil {
					t.Fatal(err)
				}
				rig.s.logicalChanged()
				return rig
			},
		},
		{
			name: "observed_source_revision_missing_transcript",
			setup: func(t *testing.T) *zeroTurnPlaceholderRig {
				rig := setupZeroTurnPlaceholderRig(t, 6, "not_found", false)
				if err := rig.d.ObserveConversationRevision(t.Context(), "server", "prepared-zero-turn", 42); err != nil {
					t.Fatal(err)
				}
				rig.s.logicalChanged()
				return rig
			},
		},
		{
			name: "dispatched_turn_without_sync_missing_transcript",
			setup: func(t *testing.T) *zeroTurnPlaceholderRig {
				rig := setupZeroTurnPlaceholderRig(t, 6, "not_found", false)
				ctx := t.Context()
				payload, _ := json.Marshal(logicalInput{Method: DeviceSessionSend, Params: map[string]any{"text": "real turn 1", "client_msg_id": "turn-1"}})
				if err := rig.d.EnqueueConversationInput(ctx, "server", "side-chat", "turn-1", 2, payload); err != nil {
					t.Fatal(err)
				}
				if _, err := rig.d.ClaimConversationInput(ctx, "server", "side-chat", 2); err != nil {
					t.Fatal(err)
				}
				if err := rig.d.ResolveConversationDelivery(ctx, "server", "side-chat", "turn-1", 2, "", "accepted"); err != nil {
					t.Fatal(err)
				}
				rig.s.logicalChanged()
				return rig
			},
		},
		{
			name: "ordinal_one_root_missing_transcript",
			setup: func(t *testing.T) *zeroTurnPlaceholderRig {
				s, f, d, _ := historyFixture(t, func(string, ...any) {})
				ctx := t.Context()
				if err := d.AdoptConversation(ctx, "server", "side-chat", "seg-one", "claude", "h1"); err != nil {
					t.Fatal(err)
				}
				f.answerNode(DeviceSessionItems, func(string, json.RawMessage) (any, error) {
					return nil, &rpc.Error{Code: codeSessionNotFound, Message: "transcript for session side-chat not found", Data: json.RawMessage(`{"reason":"no_transcript"}`)}
				})
				physical := newFleetDevs()
				physical.add("server", "Computer")
				physical.setSessions("server", []proto.Session{
					{SessionID: "main-chat", Cwd: "/home/u/.persona/assistant", Assistant: true, Rev: 100},
				})
				physical.machines["server"].call = func(method string, params json.RawMessage) (any, error) {
					switch method {
					case DeviceSessionsList:
						return map[string]any{"sessions": []proto.Session{{SessionID: "main-chat", Cwd: "/home/u/.persona/assistant", Assistant: true, Rev: 100}}}, nil
					case "assistant.segment.check":
						return segmentTarget{Registered: true, Engine: "codex", Prepare: true, SupervisionPolicy: true}, nil
					case "assistant.segment.prepare":
						return map[string]any{"session_id": "should-not-activate", "prepared": true}, nil
					case DeviceSessionItems:
						return nil, &rpc.Error{Code: codeSessionNotFound, Message: "transcript for session side-chat not found", Data: json.RawMessage(`{"reason":"no_transcript"}`)}
					default:
						return map[string]bool{"ok": true}, nil
					}
				}
				s.devs = &logicalDevices{Devices: physical, s: s}
				s.logicalChanged()
				return &zeroTurnPlaceholderRig{s: s, d: d, physical: physical}
			},
		},
		{
			name: "external_send_without_msg_id_missing_transcript",
			setup: func(t *testing.T) *zeroTurnPlaceholderRig {
				rig := setupZeroTurnPlaceholderRig(t, 6, "not_found", false)
				ctx := t.Context()
				liveRow := proto.Session{
					SessionID:      "prepared-zero-turn",
					MachineID:      "server",
					Cwd:            "/home/u/.persona/assistant",
					Assistant:      true,
					Agent:          "codex",
					Launcher:       "codex",
					Model:          "target-model",
					Effort:         "medium",
					PermissionMode: "default",
					Status:         proto.StatusIdle,
					State:          proto.StateIdle,
				}
				rig.mu.Lock()
				rig.preparedZeroTurnAlive = true
				rig.liveSessions = append(rig.liveSessions, liveRow)
				rig.mu.Unlock()
				rig.physical.setSessions("server", slices.Clone(rig.liveSessions))
				rig.s.logicalChanged()

				// Legitimate external NodeCall without client_msg_id or once_id bypasses
				// conversation_inputs and dispatches directly to the physical segment.
				extCtx := context.WithValue(ctx, logicalTransportKey{}, devicewire.NodeCall{MachineID: "server", Method: DeviceSessionSend})
				if _, handled, err := rig.s.logicalCall(extCtx, "server", DeviceSessionSend, json.RawMessage(`{"session_id":"side-chat","text":"external send without id"}`)); !handled || err != nil {
					t.Fatalf("external send without msg id failed: handled=%v err=%v", handled, err)
				}
				if got := rig.sentTo("prepared-zero-turn"); len(got) != 1 {
					t.Fatalf("expected direct physical send to prepared-zero-turn, got %d", len(got))
				}

				// Now the segment disappears before history sync records SourceHistory/SourceRevision.
				rig.mu.Lock()
				rig.preparedZeroTurnAlive = false
				rig.liveSessions = []proto.Session{{SessionID: "main-chat", Cwd: "/home/u/.persona/assistant", Assistant: true, Rev: 100}}
				rig.mu.Unlock()
				rig.physical.setSessions("server", slices.Clone(rig.liveSessions))
				rig.s.logicalChanged()
				return rig
			},
		},
		{
			name: "answer_again_without_queue_missing_transcript",
			setup: func(t *testing.T) *zeroTurnPlaceholderRig {
				rig := setupZeroTurnPlaceholderRig(t, 6, "not_found", false)
				ctx := t.Context()
				liveRow := proto.Session{
					SessionID:      "prepared-zero-turn",
					MachineID:      "server",
					Cwd:            "/home/u/.persona/assistant",
					Assistant:      true,
					Agent:          "codex",
					Launcher:       "codex",
					Model:          "target-model",
					Effort:         "medium",
					PermissionMode: "default",
					Status:         proto.StatusIdle,
					State:          proto.StateIdle,
				}
				rig.mu.Lock()
				rig.preparedZeroTurnAlive = true
				rig.liveSessions = append(rig.liveSessions, liveRow)
				rig.mu.Unlock()
				rig.physical.setSessions("server", slices.Clone(rig.liveSessions))
				rig.s.logicalChanged()

				if _, handled, err := rig.s.logicalCall(ctx, "server", assistantwire.DeviceAssistantAnswerAgain, json.RawMessage(`{"session_id":"side-chat","once_id":"aa-1"}`)); !handled || err != nil {
					t.Fatalf("answer_again failed: handled=%v err=%v", handled, err)
				}
				if got := rig.sentTo("prepared-zero-turn"); len(got) != 1 {
					t.Fatalf("expected direct physical answer_again to prepared-zero-turn, got %d", len(got))
				}

				rig.mu.Lock()
				rig.preparedZeroTurnAlive = false
				rig.liveSessions = []proto.Session{{SessionID: "main-chat", Cwd: "/home/u/.persona/assistant", Assistant: true, Rev: 100}}
				rig.mu.Unlock()
				rig.physical.setSessions("server", slices.Clone(rig.liveSessions))
				rig.s.logicalChanged()
				return rig
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rig := tc.setup(t)
			ctx := t.Context()

			c, err := rig.d.LogicalConversation(ctx, "server", "side-chat")
			if err != nil || c == nil {
				t.Fatalf("load logical conversation: %+v %v", c, err)
			}
			origEpoch := c.Epoch
			origSegments := len(c.Segments)

			// 1. History read must NOT swallow missing real history.
			if _, err := rig.s.logicalHistoryPage(ctx, rig.d, c, historyParams{Turns: 10, LazyImages: true}); err == nil {
				t.Fatal("logicalHistoryPage silently hid missing real-history segment")
			}

			// 2. conversation_send must still refuse as unavailable and never fabricate a segment.
			unavailable := i18n.T(i18n.From(ctx), "assistant-conversation_segments.unavailable")
			if _, err := rig.s.toolConversationSend(ctx, "main-chat", []byte(`{"id":"side-chat","text":"do not overwrite lost history"}`)); err == nil || !strings.Contains(err.Error(), unavailable) {
				t.Fatalf("toolConversationSend did not protect real missing segment: %v", err)
			}

			// 3. Direct logicalCall send must NOT auto-start a zero-turn recovery switch.
			_, _, _ = rig.s.logicalCall(ctx, "server", DeviceSessionSend, json.RawMessage(`{"session_id":"side-chat","client_msg_id":"must-not-recover","text":"hello"}`))
			afterSend, err := rig.d.LogicalConversation(ctx, "server", "side-chat")
			if err != nil || afterSend.Switch != nil || afterSend.Epoch != origEpoch || len(afterSend.Segments) != origSegments {
				t.Fatalf("direct send started implicit recovery on real segment: %+v %v", afterSend, err)
			}

			// 4. Switch must NOT freeze the corrupted segment at 0.
			req, _ := json.Marshal(logicalSwitchRequest{OperationID: "unsafe-switch", ExpectedEpoch: origEpoch, MachineID: "server", Launcher: "claude"})
			active, _ := afterSend.Active()
			if err := rig.d.BeginConversationSwitch(ctx, "server", "side-chat", db.ConversationSwitch{
				OperationID: "unsafe-switch", ExpectedEpoch: origEpoch, SourceSegment: active.ID, Request: req,
			}); err != nil {
				t.Fatal(err)
			}
			if err := rig.d.PutConversationHandoff(ctx, "server", "side-chat", "unsafe-switch", json.RawMessage(`{"summary":"existing"}`)); err != nil {
				t.Fatal(err)
			}
			switching, _ := rig.d.LogicalConversation(ctx, "server", "side-chat")
			if err := rig.s.advanceLogicalConversation(ctx, rig.d, switching); err == nil {
				t.Fatal("advanceLogicalConversation froze corrupted real segment at 0")
			}
			finalConv, err := rig.d.LogicalConversation(ctx, "server", "side-chat")
			if err != nil || finalConv.Epoch != origEpoch || len(finalConv.Segments) != origSegments {
				t.Fatalf("corrupted segment was mutated by switch: %+v %v", finalConv, err)
			}
			if errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}

func TestZeroTurnSlowListLagDoesNotRollbackOrTriggerRecovery(t *testing.T) {
	liveTargetRow := func(id string) proto.Session {
		return proto.Session{
			SessionID:      id,
			MachineID:      "server",
			Cwd:            "/home/u/.persona/assistant",
			Assistant:      true,
			Agent:          "codex",
			Launcher:       "codex",
			Model:          "target-model",
			Effort:         "medium",
			PermissionMode: "default",
			Status:         proto.StatusIdle,
			State:          proto.StateIdle,
		}
	}

	t.Run("active_switch_prepare_succeeds_with_lagging_manager_list", func(t *testing.T) {
		rig := setupZeroTurnPlaceholderRig(t, 6, "not_found", false)
		ctx := t.Context()
		// Simulate node list push taking >3s: prepare succeeds on node (updating liveSessions),
		// while manager display cache (physical.machines["server"].sessions) omits the new target.
		rig.mu.Lock()
		rig.laggingList = true
		rig.mu.Unlock()

		req, _ := json.Marshal(logicalSwitchRequest{
			OperationID:    "slow-list-switch",
			ExpectedEpoch:  2,
			MachineID:      "server",
			Launcher:       "claude",
			Model:          "slow-list-model",
			Effort:         "high",
			PermissionMode: "default",
		})
		if err := rig.d.BeginConversationSwitch(ctx, "server", "side-chat", db.ConversationSwitch{
			OperationID:   "slow-list-switch",
			ExpectedEpoch: 2,
			SourceSegment: "seg-two",
			Request:       req,
		}); err != nil {
			t.Fatal(err)
		}
		if _, handled, err := rig.s.logicalCall(ctx, "server", DeviceSessionSend, json.RawMessage(`{"session_id":"side-chat","client_msg_id":"slow-first-1","text":"first turn under slow list"}`)); !handled || err != nil {
			t.Fatalf("queue first turn under slow-list switch: handled=%v err=%v", handled, err)
		}

		waitFor(t, "delivery to prepared target despite lagging manager list", func() bool {
			rig.s.logicalSweep(ctx)
			return len(rig.sentTo("recovered-seg-1")) == 1
		})
		// The sweep delivers the first turn and then accepts the switch in the same goroutine; the send returns
		// before its accept write, so wait for that goroutine to finish before reading the conversation.
		conversationSwitchTestSettle(t, rig.s)

		if stale := rig.sentTo("side-chat"); len(stale) != 0 {
			t.Fatalf("healthy prepared target was rolled back to old segment: %+v", stale)
		}
		if _, cached := rig.physical.Session("server", "recovered-seg-1"); cached {
			t.Fatal("test setup expected manager display cache to lag behind prepare")
		}
		c, err := rig.d.LogicalConversation(ctx, "server", "side-chat")
		if err != nil || c == nil || c.Epoch != 3 || c.Switch != nil {
			t.Fatalf("switch did not accept onto prepared target: %+v %v", c, err)
		}
		if active, ok := c.Active(); !ok || active.SessionID != "recovered-seg-1" || active.ClosePending {
			t.Fatalf("expected active healthy recovered-seg-1, got: %+v", active)
		}
	})

	t.Run("active_switch_probe_timeout_stays_uncertain_without_rollback", func(t *testing.T) {
		rig := setupZeroTurnPlaceholderRig(t, 6, "not_found", true)
		ctx := t.Context()
		rig.mu.Lock()
		rig.preparedZeroTurnAlive = true
		rig.listErr = context.DeadlineExceeded
		rig.mu.Unlock()

		if _, handled, err := rig.s.logicalCall(ctx, "server", DeviceSessionSend, json.RawMessage(`{"session_id":"side-chat","client_msg_id":"timeout-first-1","text":"do not rollback on probe timeout"}`)); !handled || err != nil {
			t.Fatalf("queue first turn: handled=%v err=%v", handled, err)
		}
		waitFor(t, "delivery to active target when probe times out", func() bool {
			rig.s.logicalSweep(ctx)
			return len(rig.sentTo("prepared-zero-turn")) == 1
		})
		if rolledBack := rig.sentTo("side-chat"); len(rolledBack) != 0 {
			t.Fatalf("timed-out probe caused false rollback to old segment: %+v", rolledBack)
		}
	})

	t.Run("idle_prepared_slow_list_send_does_not_fabricate_replacement", func(t *testing.T) {
		rig := setupZeroTurnPlaceholderRig(t, 6, "not_found", false)
		ctx := t.Context()
		// Manager display cache omits prepared-zero-turn (so it looks like a placeholder),
		// but the live node actually still has prepared-zero-turn healthy.
		rig.mu.Lock()
		rig.preparedZeroTurnAlive = true
		rig.liveSessions = append(rig.liveSessions, liveTargetRow("prepared-zero-turn"))
		rig.mu.Unlock()

		if _, err := rig.s.toolConversationSend(ctx, "main-chat", []byte(`{"id":"side-chat","text":"hello healthy slow-listed segment"}`)); err != nil {
			t.Fatalf("conversation_send failed: %v", err)
		}
		waitFor(t, "delivery to healthy prepared-zero-turn without replacement", func() bool {
			rig.s.logicalSweep(ctx)
			return len(rig.sentTo("prepared-zero-turn")) == 1
		})
		rig.mu.Lock()
		preparedCount := rig.prepared
		rig.mu.Unlock()
		if preparedCount != 0 {
			t.Fatalf("slow manager list triggered spurious replacement prepare count=%d", preparedCount)
		}
	})

	t.Run("grace_window_stays_uncertain_without_recovery_or_rollback", func(t *testing.T) {
		rig := setupZeroTurnPlaceholderRig(t, 6, "not_found", true)
		ctx := t.Context()
		rig.physical.mu.Lock()
		rig.physical.machines["server"].shownUntil = time.Now().Add(time.Minute)
		rig.physical.mu.Unlock()

		c, err := rig.d.LogicalConversation(ctx, "server", "side-chat")
		if err != nil || c == nil {
			t.Fatalf("load logical conversation: %+v %v", c, err)
		}
		active, ok := c.Active()
		if !ok {
			t.Fatal("missing active segment")
		}
		if rig.s.physicalSegmentMissing(ctx, active) {
			t.Fatal("grace window must remain uncertain and never report segment missing")
		}
	})
}

// agy materializes an empty history generation during items reads. Its revision
// is not execution evidence, but the persisted first-message state is required.
func TestZeroTurnPreparedEmptyGenerationNeedsExplicitStateAndNoDelivery(t *testing.T) {
	for _, tc := range []struct {
		name       string
		awaiting   bool
		tail       int
		receipt    bool
		dispatch   bool
		unresolved bool
		want       bool
	}{
		{name: "empty_agy_generation", awaiting: true, want: true},
		{name: "revision_one_is_not_proof"},
		{name: "real_history", awaiting: true, tail: 1},
		{name: "node_receipt", awaiting: true, receipt: true},
		{name: "direct_dispatch", awaiting: true, dispatch: true},
		{name: "unresolved_input", awaiting: true, unresolved: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := setupZeroTurnPlaceholderRig(t, 6, "not_found", false)
			ctx := t.Context()
			state := map[string]any{"awaiting_first_message": tc.awaiting}
			if tc.receipt {
				state["segment_inputs"] = []string{"received"}
			}
			if err := rig.d.PutSetting(ctx, "node_model.server.prepared-zero-turn", state); err != nil {
				t.Fatal(err)
			}
			page := db.ConversationPage{MachineID: "server", SessionID: "prepared-zero-turn", History: "empty-agy-generation", Revision: 1, ThroughSeq: tc.tail, Tail: true}
			if tc.tail > 0 {
				page.Items = []proto.Item{{ID: "actual-turn", Seq: 1, Role: "user", Kind: proto.KindText, Text: "real"}}
			}
			if err := rig.d.PutConversationPage(ctx, page); err != nil {
				t.Fatal(err)
			}
			if err := rig.d.ObserveConversationSource(ctx, "server", "prepared-zero-turn", page.History); err != nil {
				t.Fatal(err)
			}
			if err := rig.d.ObserveConversationRevision(ctx, "server", "prepared-zero-turn", 1); err != nil {
				t.Fatal(err)
			}
			if tc.dispatch {
				if err := rig.d.MarkConversationSegmentDispatched(ctx, "server", "side-chat", "seg-two"); err != nil {
					t.Fatal(err)
				}
			}
			if tc.unresolved {
				if err := rig.d.EnqueueConversationInput(ctx, "server", "side-chat", "pending", 2, json.RawMessage(`{"text":"uncertain"}`)); err != nil {
					t.Fatal(err)
				}
				if _, err := rig.d.ClaimConversationInput(ctx, "server", "side-chat", 2); err != nil {
					t.Fatal(err)
				}
			}
			c, _ := rig.d.LogicalConversation(ctx, "server", "side-chat")
			active, _ := c.Active()
			if got := rig.s.isZeroTurnPreparedSegment(ctx, c, active); got != tc.want {
				t.Fatalf("zero-turn=%v want %v: %+v", got, tc.want, active)
			}
			if tc.want {
				zero := 0
				if !rig.s.isZeroTurnEmptyTail(ctx, c, active, historyPage{History: page.History, ToSeq: &zero}, nil, false) {
					t.Fatal("empty generation page refused despite prepared proof")
				}
			}
		})
	}
}

func TestZeroTurnPreparedEmptyHistoryRecoveryCarriesHandoffOnce(t *testing.T) {
	rig := setupZeroTurnPlaceholderRig(t, 6, "not_found", false)
	ctx := t.Context()
	setSettings(t, rig.s, func(cfg *Settings) { cfg.Leaders = append(cfg.Leaders, "side-chat") })
	if err := rig.d.PutSetting(ctx, "node_model.server.prepared-zero-turn", map[string]bool{"awaiting_first_message": true}); err != nil {
		t.Fatal(err)
	}
	if err := rig.d.PutConversationPage(ctx, db.ConversationPage{MachineID: "server", SessionID: "prepared-zero-turn", History: "empty-generation", Revision: 1, Tail: true}); err != nil {
		t.Fatal(err)
	}
	if err := rig.d.ObserveConversationSource(ctx, "server", "prepared-zero-turn", "empty-generation"); err != nil {
		t.Fatal(err)
	}
	if err := rig.d.ObserveConversationRevision(ctx, "server", "prepared-zero-turn", 1); err != nil {
		t.Fatal(err)
	}
	if _, handled, err := rig.s.logicalCall(ctx, "server", DeviceSessionSend, json.RawMessage(`{"session_id":"side-chat","client_msg_id":"first-after-restart","text":"first input after companion restart"}`)); !handled || err != nil {
		t.Fatalf("send refused: %v %v", handled, err)
	}
	waitFor(t, "empty generation recovery first delivery", func() bool {
		rig.s.logicalSweep(ctx)
		return len(rig.sentTo("recovered-seg-1")) == 1
	})
	conversationSwitchTestSettle(t, rig.s)
	if _, handled, err := rig.s.logicalCall(ctx, "server", DeviceSessionSend, json.RawMessage(`{"session_id":"side-chat","client_msg_id":"first-after-restart","text":"first input after companion restart"}`)); !handled || err != nil {
		t.Fatalf("idempotent retry: %v %v", handled, err)
	}
	conversationSwitchTestSettle(t, rig.s)
	sends := rig.sentTo("recovered-seg-1")
	if len(sends) != 1 || !strings.Contains(fmt.Sprint(sends[0]["text"]), "earlier handoff summary") {
		t.Fatalf("handoff missing or repeated execution: %+v", sends)
	}
	if !slices.Contains(rig.s.settings(ctx).Leaders, "side-chat") {
		t.Fatal("supervisor mapping lost")
	}
}
