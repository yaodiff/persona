package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestClosedTaskArchiveUsesPostStopProvenance(t *testing.T) {
	for _, tc := range []struct {
		name string
		info map[string]any
		err  error
		want proto.StopCause
	}{
		{"completed", map[string]any{"stop_cause": proto.StopTaskCompleted}, nil, proto.StopTaskCompleted},
		{"idle, not the requested cause", map[string]any{"stop_cause": proto.StopIdle}, nil, proto.StopIdle},
		{"later user stop", map[string]any{"stop_cause": proto.StopUser}, nil, proto.StopUser},
		{"missing cause", map[string]any{}, nil, proto.StopUnknown},
		{"failed read", nil, errors.New("node unavailable"), proto.StopUnknown},
		{"invalid cause", map[string]any{"stop_cause": 1}, nil, proto.StopUnknown},
		{"future cause", map[string]any{"stop_cause": "future-cause"}, nil, "future-cause"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ft, c := closeTestService(t, func(c *proto.Session) {
				c.SessionID, c.Agent = "closed"+randomHex(6), "codex"
				// A stale positive snapshot must not authorize a later unknown or user stop.
				c.Capabilities = &proto.SessionCapabilities{Read: true, Send: true, Terminal: true, StopCause: proto.StopRestart}
			})
			s, f := ft.s, ft.f
			ctx := context.Background()
			f.setSessions(c.MachineID, c)
			ft.d.machines[c.MachineID].call = func(method string, params json.RawMessage) (any, error) {
				raw, err := f.DeviceCall(ctx, DeviceCall{Device: c.MachineID, Method: method, Params: params})
				if err != nil {
					return nil, err
				}
				var result any
				err = json.Unmarshal(raw, &result)
				return result, err
			}
			todo, err := s.createTodo(ctx, "Disposable task", "Check the result", "conv1", "")
			if err != nil {
				t.Fatal(err)
			}
			if err := s.linkTodoAgent(ctx, todo.ID, c.MachineID, c.SessionID, c.Title, "/code", "codex"); err != nil {
				t.Fatal(err)
			}
			if ok, err := s.reviewDoneTodo(ctx, todo.ID, "Closed", true); err != nil || !ok {
				t.Fatalf("close: %v %v", ok, err)
			}
			s.todos.mu.Lock()
			old := time.Now().Add(-time.Hour)
			s.todos.todos[todo.ID].DoneAt = &old
			s.todos.mu.Unlock()
			stopped, reads := false, 0
			f.answerNode(nodeproto.StopIfIdle, func(_ string, raw json.RawMessage) (any, error) {
				var args map[string]any
				_ = json.Unmarshal(raw, &args)
				if !maps.Equal(args, map[string]any{"session_id": c.SessionID, "retire": true, "stop_cause": string(proto.StopTaskCompleted)}) {
					t.Errorf("wrong stop request: %v", args)
				}
				stopped = true
				return map[string]any{"stopped": true}, nil
			})
			f.answerNode("session.info", func(_ string, raw json.RawMessage) (any, error) {
				reads++
				var args map[string]any
				_ = json.Unmarshal(raw, &args)
				if !stopped || !maps.Equal(args, map[string]any{"session_id": c.SessionID}) {
					t.Errorf("provenance was read before the stop or for another session: %v", args)
				}
				return tc.info, tc.err
			})
			f.answerNode(deviceSessionArchive, func(string, json.RawMessage) (any, error) {
				if !stopped || reads != 1 {
					t.Errorf("native archive preceded the post-stop provenance read: stopped=%v reads=%d", stopped, reads)
				}
				// Native Codex archival removes the node's live inventory row.
				f.sessions[c.MachineID] = nil
				ft.d.setSessions(c.MachineID, nil)
				return map[string]any{"native": nativeArchiveDone}, nil
			})
			s.closeDoneTaskAgents(ctx, taskCloseRefused{}, doneSeen{{c.MachineID, c.SessionID}: {done: old, running: old}})
			if native := s.nativeArchiveAwaited(ctx, c.MachineID, c.SessionID); native == nil || native.State != nativeArchiveDone {
				t.Fatalf("native archive did not complete: %+v", native)
			}

			restarted, next := newQuietServiceOn(t, ft.st)
			next.setSessions(c.MachineID)
			rows, _, err := restarted.archivedSessions(ctx, c.MachineID, nil, 0)
			if err != nil || len(rows) != 1 {
				t.Fatalf("archive-only inventory after manager restart: %+v %v", rows, err)
			}
			got := rows[0]
			want := &proto.SessionCapabilities{Read: true, Terminal: true, StopCause: tc.want}
			if !got.Archived || got.ReadOnlyReason == "" || !reflect.DeepEqual(got.Capabilities, want) || got.SessionID != c.SessionID {
				t.Fatalf("lost post-stop evidence or retained live controls: %+v capabilities=%+v, want %+v", got, got.Capabilities, want)
			}
			next.setSessions(c.MachineID, got)
			resumingNode(next)
			next.answerNode("session.info", func(string, json.RawMessage) (any, error) { return tc.info, tc.err })
			result, err := runJSON(t, restarted, "conv1", "agent_send", map[string]any{
				"machine": c.MachineID, "session_id": c.SessionID, "text": "Continue the same investigation",
				"resume": true, "resume_why": "The next step needs its context",
				"task": map[string]any{"title": "Continue the investigation", "done_when": []string{"The follow-up passes"}},
			})
			if tc.want.AllowsSupervisorResume() {
				if err != nil || len(startCalls(next)) != 1 {
					t.Fatalf("verified archived conversation could not resume: %s %v", result, err)
				}
			} else if err == nil || len(startCalls(next)) != 0 {
				t.Fatalf("unverified stop allowed a guarded resume: %s %v", result, err)
			}
			if len(sentToAgent(next)) != 0 {
				t.Fatal("archived resume used an ordinary message")
			}
		})
	}
}

func TestClosedTaskArchiveOverlayPreservesCurrentProvenance(t *testing.T) {
	for _, pane := range []string{"", "w1:p1"} {
		for _, cause := range []proto.StopCause{
			proto.StopTaskCompleted, proto.StopIdle, proto.StopRestart, proto.StopUser, proto.StopUnknown, "future-cause", "missing-capabilities",
		} {
			t.Run(pane+"/"+cause.String(), func(t *testing.T) {
				now := time.Now()
				c := proto.Session{MachineID: "box", SessionID: "closed-codex", Agent: "codex", PaneID: pane,
					Status: proto.StatusIdle, State: proto.StateIdle, UpdatedAt: now.Add(-time.Hour)}
				c.Capabilities = &proto.SessionCapabilities{Read: true, Terminal: true, StopCause: proto.StopTaskCompleted}
				rec := agentArchive{Session: archiveSnapshot(c), Archived: true, ClosedAt: now, ChangedAt: now}
				rec.Session.ReadOnlyReason = "The linked to-do was closed."
				c.Capabilities = &proto.SessionCapabilities{Read: true, Send: true, Terminal: true, StopCause: cause,
					Modes: proto.CanModes(true), Model: proto.CanModes(true)}
				want := &proto.SessionCapabilities{Read: true, Terminal: true, StopCause: cause}
				if cause == "missing-capabilities" {
					c.Capabilities = nil
					want = &proto.SessionCapabilities{Read: true, Terminal: pane != ""}
				}
				rows := []proto.Session{c}
				markAgentArchives("box", rows,
					map[string]agentArchive{runtimeListID("box", c.SessionID): rec}, nil, 0)
				if len(rows) != 1 {
					t.Fatalf("closed-task overlay returned %d rows", len(rows))
				}
				if !rows[0].Archived || rows[0].ReadOnlyReason != rec.Session.ReadOnlyReason ||
					!reflect.DeepEqual(rows[0].Capabilities, want) {
					t.Fatalf("closed-task overlay lost current provenance or retained live controls: %+v, want %+v", rows[0].Capabilities, want)
				}
			})
		}
	}
}
