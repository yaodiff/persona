package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func continuesArgsWithLauncher(from, launcher string) json.RawMessage {
	task := map[string]any{
		"title":         "Continue task",
		"prompt":        "Previous Agent stopped; carry on the remaining work.",
		"machine":       "box",
		"cwd":           "/code/app",
		"readonly":      true,
		"permission":    "bypassPermissions",
		"why":           "Repository lives on box",
		"continues":     from,
		"continues_why": "Replacing previous Agent",
	}
	if launcher != "" {
		task["launcher"] = launcher
	}
	raw, _ := json.Marshal(map[string]any{"tasks": []map[string]any{task}})
	return raw
}

// runContinues submits a continuation plan and, if a cross-provider handoff waits for Start, confirms it on the card.
func runContinues(t *testing.T, e *threadEnv, todoID, launcher string) (string, error) {
	t.Helper()
	ctx := context.Background()
	res, err := e.s.toolTaskPlan(ctx, "m1", continuesArgsWithLauncher(todoID, launcher))
	if err != nil {
		return res, err
	}
	if strings.Contains(res, "needs the user's Start") {
		planID := planIDRe.FindString(res)
		if planID == "" {
			t.Fatalf("waiting plan missing id: %q", res)
		}
		plan, startErr := e.s.startPlan(ctx, planID, nil, false)
		if startErr != nil {
			return res, startErr
		}
		if plan != nil && len(plan.Tasks) > 0 && plan.Tasks[0].Error != "" {
			return res, errors.New(plan.Tasks[0].Error)
		}
		return res, nil
	}
	return res, nil
}

func waitForReplacementAgent(t *testing.T, e *threadEnv, todoID, oldSessionID string) TodoAgent {
	t.Helper()
	var old TodoAgent
	waitFor(t, "replacement agent on "+todoID, func() bool {
		fresh := false
		for _, a := range todoNow(e.s, todoID).Agents {
			if a.SessionID == oldSessionID {
				old = a
			} else if a.ReleasedAt == nil && a.DismissedAt == nil {
				fresh = true
			}
		}
		return fresh
	})
	return old
}

// Regression for #16231: continuing a to-do whose active Agent is a live native Antigravity companion session
// (agy-<uuid>, source=runtime, closeable, no terminal) must stop and release it via session.close instead of failing
// with "invalid session id" from terminal-only session.stop.
func TestContinuationStopsAndReleasesNativeAgyRuntimeSession(t *testing.T) {
	const agyID = "agy-11111111-2222-4333-8444-555555555555"

	for _, status := range []string{proto.StatusWorking, proto.StatusIdle, proto.StatusDone} {
		t.Run(status, func(t *testing.T) {
			e := newThreadEnv(t, nil)
			e.f.launcherList = companionLaunchers()
			closeable := true
			e.sessions("box", proto.Session{
				SessionID:        agyID,
				Agent:            "agy",
				Launcher:         "agy",
				SourceKind:       proto.SourceRuntime,
				SourceInstanceID: "agy",
				Status:           status,
				Capabilities: &proto.SessionCapabilities{
					Read:      true,
					Send:      true,
					Interrupt: true,
					Close:     &closeable,
					Terminal:  false,
				},
			})
			putTodo(e.s, Todo{
				ID:     "todo_agy",
				Title:  "Native AGY continuation",
				Origin: "m1",
				Agents: []TodoAgent{{MachineID: "box", SessionID: agyID, Agent: "agy"}},
			})
			brief, err := e.s.ensureTaskBrief(context.Background(), todoNow(e.s, "todo_agy"))
			if err != nil {
				t.Fatal(err)
			}

			var stopCalls, closeCalls atomic.Int32
			var failClose atomic.Bool
			failClose.Store(true)
			e.f.answerNode(deviceSessionStop, func(_ string, raw json.RawMessage) (any, error) {
				stopCalls.Add(1)
				return nil, &rpc.Error{Message: fmt.Sprintf("invalid session id %q", agyID)}
			})
			e.f.answerNode("session.close", func(machine string, raw json.RawMessage) (any, error) {
				if machine != "box" || !strings.Contains(string(raw), agyID) {
					t.Errorf("unexpected session.close target: %s %s", machine, raw)
				}
				closeCalls.Add(1)
				if failClose.Load() {
					return nil, &rpc.Error{Code: "runtime_away", Message: "Antigravity runtime is disconnected"}
				}
				return map[string]any{"ok": true}, nil
			})

			// 1. A failed runtime close stays fail-closed: no release, no replacement start.
			res, err := e.s.toolTaskPlan(context.Background(), "m1", continuesArgsWithLauncher("todo_agy", "agy"))
			if err != nil || !strings.Contains(res, "Antigravity runtime is disconnected") {
				t.Fatalf("failed runtime close: result=%q err=%v", res, err)
			}
			if len(e.f.started()) != 0 || !todoNow(e.s, "todo_agy").Agents[0].active() {
				t.Fatalf("failed close released agent or started replacement: starts=%d agents=%+v", len(e.f.started()), todoNow(e.s, "todo_agy").Agents)
			}

			// 2. Once runtime close succeeds, the old native AGY link is released and replacement starts on the same to-do.
			failClose.Store(false)
			res, err = e.s.toolTaskPlan(context.Background(), "m1", continuesArgsWithLauncher("todo_agy", "agy"))
			if err != nil || strings.Contains(res, "dispatch failed") {
				t.Fatalf("continuation failed after runtime close succeeded: result=%q err=%v", res, err)
			}
			old := waitForReplacementAgent(t, e, "todo_agy", agyID)
			if old.ReleasedAt == nil || old.DismissedAt != nil {
				t.Fatalf("old native AGY agent was not released into history: %+v", old)
			}
			if todoCount(e.s) != 1 || len(e.f.started()) != 1 {
				t.Fatalf("continuation split or duplicated work: todos=%d starts=%d", todoCount(e.s), len(e.f.started()))
			}
			if kept, ok := e.s.briefOfTodo("todo_agy"); !ok || kept.ID != brief.ID {
				t.Fatalf("continuation replaced brief: %+v", kept)
			}
			if stopCalls.Load() != 0 {
				t.Fatalf("listed closeable runtime called terminal session.stop %d time(s), want 0", stopCalls.Load())
			}
			if closeCalls.Load() != 2 {
				t.Fatalf("session.close calls = %d, want 2 (1 refused + 1 succeeded)", closeCalls.Load())
			}
		})
	}
}

// Non-Claude runtimes cover both closeable runtimes (Paseo, Opencode companion, T3Code) and interrupt-only runtimes
// without session.close (Antigravity Desktop, DSH, Managed Agents).
func TestContinuationStopsAndReleasesOtherNonClaudeRuntimes(t *testing.T) {
	t.Run("closeable_runtimes", func(t *testing.T) {
		for _, tc := range []struct {
			name, agent, instance, sid string
		}{
			{"paseo", "paseo", "paseo", "paseo-agent-42"},
			{"opencode", "opencode", "opencode", "opencode-ses-9"},
			{"t3code", "t3code", "t3code", "t3code-thr-7"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e := newThreadEnv(t, nil)
				closeable := true
				e.sessions("box", proto.Session{
					SessionID:        tc.sid,
					Agent:            tc.agent,
					SourceKind:       proto.SourceRuntime,
					SourceInstanceID: tc.instance,
					Status:           proto.StatusWorking,
					Capabilities:     &proto.SessionCapabilities{Read: true, Send: true, Interrupt: true, Close: &closeable},
				})
				putTodo(e.s, Todo{ID: "todo_rt", Title: "Runtime task", Origin: "m1",
					Agents: []TodoAgent{{MachineID: "box", SessionID: tc.sid}}})

				var closeCalls, stopCalls atomic.Int32
				e.f.answerNode(deviceSessionStop, func(string, json.RawMessage) (any, error) {
					stopCalls.Add(1)
					return nil, &rpc.Error{Code: "no_terminal", Message: "this session belongs to another tool"}
				})
				e.f.answerNode("session.close", func(_ string, raw json.RawMessage) (any, error) {
					if !strings.Contains(string(raw), tc.sid) {
						t.Errorf("wrong close target: %s", raw)
					}
					closeCalls.Add(1)
					return map[string]any{"ok": true}, nil
				})

				res, err := runContinues(t, e, "todo_rt", "")
				if err != nil || strings.Contains(res, "dispatch failed") {
					t.Fatalf("%s continuation failed: %q %v", tc.name, res, err)
				}
				if old := waitForReplacementAgent(t, e, "todo_rt", tc.sid); old.ReleasedAt == nil {
					t.Fatalf("%s old agent not released: %+v", tc.name, old)
				}
				if closeCalls.Load() != 1 || stopCalls.Load() != 0 {
					t.Fatalf("%s close=%d stop=%d, want 1 and 0", tc.name, closeCalls.Load(), stopCalls.Load())
				}
			})
		}
	})

	t.Run("interrupt_only_runtimes", func(t *testing.T) {
		for _, tc := range []struct {
			name, instance, sid, status string
			wantInterrupt               int32
		}{
			{"antigravity_desktop_working", "antigravity-desktop", "antigravity-desktop-11111111-2222-4333-8444-555555555555", proto.StatusWorking, 1},
			{"antigravity_desktop_idle", "antigravity-desktop", "antigravity-desktop-11111111-2222-4333-8444-555555555555", proto.StatusIdle, 0},
			{"dsh_working", "dsh", "session-22222222-3333-4444-8555-666666666666", proto.StatusWorking, 1},
			{"dsh_done", "dsh", "session-22222222-3333-4444-8555-666666666666", proto.StatusDone, 0},
			{"managed_agents_working", "managed-agents", "cloud.ses_123", proto.StatusWorking, 1},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e := newThreadEnv(t, nil)
				notCloseable := false
				e.sessions("box", proto.Session{
					SessionID:        tc.sid,
					Agent:            tc.instance,
					SourceKind:       proto.SourceRuntime,
					SourceInstanceID: tc.instance,
					Status:           tc.status,
					Capabilities:     &proto.SessionCapabilities{Read: true, Send: true, Interrupt: true, Close: &notCloseable},
				})
				putTodo(e.s, Todo{ID: "todo_int", Title: "Interrupt-only task", Origin: "m1",
					Agents: []TodoAgent{{MachineID: "box", SessionID: tc.sid}}})

				var stopCalls, closeCalls, interruptCalls atomic.Int32
				var failInterrupt atomic.Bool
				failInterrupt.Store(tc.wantInterrupt > 0)
				e.f.answerNode(deviceSessionStop, func(string, json.RawMessage) (any, error) {
					stopCalls.Add(1)
					return nil, &rpc.Error{Message: fmt.Sprintf("invalid session id %q", tc.sid)}
				})
				e.f.answerNode("session.close", func(string, json.RawMessage) (any, error) {
					closeCalls.Add(1)
					return nil, &rpc.Error{Code: "unsupported", Message: "unsupported"}
				})
				e.f.answerNode("session.interrupt", func(_ string, raw json.RawMessage) (any, error) {
					if !strings.Contains(string(raw), tc.sid) {
						t.Errorf("wrong interrupt target: %s", raw)
					}
					interruptCalls.Add(1)
					if failInterrupt.Load() {
						return nil, errors.New("interrupt timed out")
					}
					return map[string]any{"ok": true, "settled": true}, nil
				})

				if tc.wantInterrupt > 0 {
					res, err := runContinues(t, e, "todo_int", "")
					if (err == nil || !strings.Contains(err.Error(), "interrupt timed out")) && !strings.Contains(res, "interrupt timed out") {
						t.Fatalf("failed interrupt must stay fail-closed: res=%q err=%v", res, err)
					}
					if len(e.f.started()) != 0 || !todoNow(e.s, "todo_int").Agents[0].active() {
						t.Fatal("failed interrupt released agent or started replacement")
					}
					failInterrupt.Store(false)
				}

				res, err := runContinues(t, e, "todo_int", "")
				if err != nil || strings.Contains(res, "dispatch failed") {
					t.Fatalf("%s continuation failed: %q %v", tc.name, res, err)
				}
				if old := waitForReplacementAgent(t, e, "todo_int", tc.sid); old.ReleasedAt == nil {
					t.Fatalf("%s old agent not released: %+v", tc.name, old)
				}
				if stopCalls.Load() != 0 || closeCalls.Load() != 0 {
					t.Fatalf("%s stop=%d close=%d, want 0 and 0", tc.name, stopCalls.Load(), closeCalls.Load())
				}
				wantCalls := tc.wantInterrupt
				if tc.wantInterrupt > 0 {
					wantCalls = 2 // 1 failed + 1 succeeded
				}
				if interruptCalls.Load() != wantCalls {
					t.Fatalf("%s interrupt calls = %d, want %d", tc.name, interruptCalls.Load(), wantCalls)
				}
			})
		}
	})
}

// When an older runtime session dropped off sessions.list (so its row is unlisted), session.stop on a current node
// resolves it and answers no_terminal; stopContinuedTaskAgents falls back to session.close only when the live list
// succeeded without listing any terminal pane for it.
func TestContinuationUnlistedRuntimeFallsBackToCloseOnNoTerminal(t *testing.T) {
	const unlistedAgyID = "agy-44444444-5555-4666-8777-888888888888"

	t.Run("fallback_succeeds", func(t *testing.T) {
		e := newThreadEnv(t, nil)
		e.sessions("box") // live list succeeded, session not listed (e.g. aged out of maxSessions=80)
		var infoCalls atomic.Int32
		e.f.answerNode("session.info", func(_ string, raw json.RawMessage) (any, error) {
			infoCalls.Add(1)
			return map[string]any{"session_id": unlistedAgyID, "agent": "agy"}, nil
		})
		putTodo(e.s, Todo{ID: "todo_unlisted", Title: "Unlisted AGY", Origin: "m1",
			Agents: []TodoAgent{{MachineID: "box", SessionID: unlistedAgyID}}})

		var stopCalls, closeCalls atomic.Int32
		e.f.answerNode(deviceSessionStop, func(string, json.RawMessage) (any, error) {
			stopCalls.Add(1)
			return nil, &rpc.Error{Code: "no_terminal", Message: "this session belongs to another tool"}
		})
		e.f.answerNode("session.close", func(_ string, raw json.RawMessage) (any, error) {
			if !strings.Contains(string(raw), unlistedAgyID) {
				t.Errorf("wrong close target: %s", raw)
			}
			closeCalls.Add(1)
			return map[string]any{"ok": true}, nil
		})

		res, err := runContinues(t, e, "todo_unlisted", "")
		if err != nil || strings.Contains(res, "dispatch failed") {
			t.Fatalf("unlisted runtime fallback failed: %q %v", res, err)
		}
		if old := waitForReplacementAgent(t, e, "todo_unlisted", unlistedAgyID); old.ReleasedAt == nil {
			t.Fatalf("old unlisted agent not released: %+v", old)
		}
		if infoCalls.Load() != 1 || stopCalls.Load() != 1 || closeCalls.Load() != 1 {
			t.Fatalf("info=%d stop=%d close=%d, want 1, 1, 1", infoCalls.Load(), stopCalls.Load(), closeCalls.Load())
		}
	})

	t.Run("sessions_list_error_blocks_close_fallback", func(t *testing.T) {
		e := newThreadEnv(t, nil)
		e.f.answerNode(DeviceSessionsList, func(string, json.RawMessage) (any, error) {
			return nil, errors.New("sessions list failed")
		})
		putTodo(e.s, Todo{ID: "todo_errlist", Title: "List error", Origin: "m1",
			Agents: []TodoAgent{{MachineID: "box", SessionID: unlistedAgyID}}})

		var closeCalls atomic.Int32
		e.f.answerNode(deviceSessionStop, func(string, json.RawMessage) (any, error) {
			return nil, &rpc.Error{Code: "no_terminal", Message: "this session belongs to another tool"}
		})
		e.f.answerNode("session.close", func(string, json.RawMessage) (any, error) {
			closeCalls.Add(1)
			return map[string]any{"ok": true}, nil
		})

		res, err := runContinues(t, e, "todo_errlist", "")
		if err == nil && !strings.Contains(res, "dispatch failed") {
			t.Fatalf("sessions list error must not fall back to session.close: %q %v", res, err)
		}
		if closeCalls.Load() != 0 || !todoNow(e.s, "todo_errlist").Agents[0].active() {
			t.Fatalf("close=%d active=%v, want 0 and true", closeCalls.Load(), todoNow(e.s, "todo_errlist").Agents[0].active())
		}
	})
}

// Terminal-backed sessions (including pi/omp inside a terminal pane and herdr-pane agy), assistant conversations, and
// read-only runtimes must never have session.close called during continuation.
func TestContinuationRuntimeStopGuardsTerminalsAssistantsAndReadOnly(t *testing.T) {
	closeTrue := true
	closeFalse := false

	for _, tc := range []struct {
		name      string
		row       proto.Session
		stopErr   error
		wantFail  bool
		wantStops int32
	}{
		{
			name: "terminal_backed_runtime_pi_in_pane",
			row: proto.Session{
				SessionID:    "pi-session-1",
				Agent:        "pi",
				SourceKind:   proto.SourceRuntime,
				PaneID:       "w1:p1",
				Status:       proto.StatusWorking,
				Capabilities: &proto.SessionCapabilities{Read: true, Send: true, Interrupt: true, Close: &closeTrue, Terminal: true},
			},
			stopErr:   &rpc.Error{Code: "no_terminal", Message: "this session belongs to another tool"},
			wantFail:  true,
			wantStops: 1,
		},
		{
			name: "herdr_pane_agy_keeps_terminal_via_stop",
			row: proto.Session{
				SessionID:    "agy-w1-p1",
				Agent:        "agy",
				PaneID:       "w1:p1",
				Status:       proto.StatusIdle,
				Capabilities: &proto.SessionCapabilities{Read: true, Send: true, Interrupt: true, Terminal: true},
			},
			stopErr:   nil,
			wantFail:  false,
			wantStops: 1,
		},
		{
			name: "runtime_assistant_never_closed",
			row: proto.Session{
				SessionID:    "agy-33333333-4444-4555-8666-777777777777",
				Agent:        "agy",
				SourceKind:   proto.SourceRuntime,
				Assistant:    true,
				Status:       proto.StatusIdle,
				Capabilities: &proto.SessionCapabilities{Read: true, Send: true, Interrupt: true, Close: &closeTrue},
			},
			stopErr:   &rpc.Error{Code: "not_retirable", Message: "assistant conversations are not stopped"},
			wantFail:  true,
			wantStops: 1,
		},
		{
			name: "readonly_runtime_fails_closed",
			row: proto.Session{
				SessionID:    "dsh-readonly-1",
				Agent:        "dsh",
				SourceKind:   proto.SourceRuntime,
				Status:       proto.StatusWorking,
				Capabilities: &proto.SessionCapabilities{Read: true, Close: &closeFalse, Interrupt: false},
			},
			stopErr:   &rpc.Error{Code: "no_terminal", Message: "this session belongs to another tool"},
			wantFail:  true,
			wantStops: 1,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newThreadEnv(t, nil)
			e.sessions("box", tc.row)
			putTodo(e.s, Todo{ID: "todo_guard", Title: "Guard test", Origin: "m1",
				Agents: []TodoAgent{{MachineID: "box", SessionID: tc.row.SessionID}}})

			var stopCalls, closeCalls atomic.Int32
			e.f.answerNode(deviceSessionStop, func(string, json.RawMessage) (any, error) {
				stopCalls.Add(1)
				if tc.stopErr != nil {
					return nil, tc.stopErr
				}
				return map[string]any{"stopped": true}, nil
			})
			e.f.answerNode("session.close", func(string, json.RawMessage) (any, error) {
				closeCalls.Add(1)
				return nil, errors.New("session.close must not be called")
			})

			res, err := runContinues(t, e, "todo_guard", "")
			if closeCalls.Load() != 0 {
				t.Fatalf("%s called session.close %d time(s), want 0", tc.name, closeCalls.Load())
			}
			if stopCalls.Load() != tc.wantStops {
				t.Fatalf("%s session.stop calls = %d, want %d", tc.name, stopCalls.Load(), tc.wantStops)
			}
			if tc.wantFail {
				failed := err != nil || strings.Contains(res, "dispatch failed")
				if !failed || !todoNow(e.s, "todo_guard").Agents[0].active() || len(e.f.started()) != 0 {
					t.Fatalf("%s unexpectedly succeeded: res=%q err=%v active=%v starts=%d", tc.name, res, err, todoNow(e.s, "todo_guard").Agents[0].active(), len(e.f.started()))
				}
			} else {
				if err != nil || strings.Contains(res, "dispatch failed") {
					t.Fatalf("%s unexpectedly failed: res=%q err=%v", tc.name, res, err)
				}
				if old := waitForReplacementAgent(t, e, "todo_guard", tc.row.SessionID); old.ReleasedAt == nil {
					t.Fatalf("%s did not release stopped terminal agent: %+v", tc.name, old)
				}
			}
		})
	}

	t.Run("aliased_herdr_pane_agy_stops_by_canonical_pane_id", func(t *testing.T) {
		const (
			paneID = "agy-w1-p1"
			convID = "agy-55555555-6666-4777-8888-999999999999"
		)
		e := newThreadEnv(t, nil)
		row := proto.Session{
			SessionID:    paneID,
			Agent:        "agy",
			PaneID:       "w1:p1",
			Status:       proto.StatusIdle,
			Capabilities: &proto.SessionCapabilities{Read: true, Send: true, Interrupt: true, Terminal: true},
		}
		row.SetAliases([]string{convID})
		e.sessions("box", row)
		putTodo(e.s, Todo{ID: "todo_alias_pane", Title: "Aliased AGY pane", Origin: "m1",
			Agents: []TodoAgent{{MachineID: "box", SessionID: convID}}})

		var stopCalls, closeCalls atomic.Int32
		e.f.answerNode(deviceSessionStop, func(_ string, raw json.RawMessage) (any, error) {
			stopCalls.Add(1)
			if !strings.Contains(string(raw), paneID) {
				t.Errorf("session.stop target = %s, want canonical pane id %s", raw, paneID)
			}
			return map[string]any{"stopped": true}, nil
		})
		e.f.answerNode("session.close", func(string, json.RawMessage) (any, error) {
			closeCalls.Add(1)
			return nil, errors.New("session.close must not be called for terminal pane")
		})

		res, err := runContinues(t, e, "todo_alias_pane", "")
		if err != nil || strings.Contains(res, "dispatch failed") {
			t.Fatalf("aliased herdr-pane agy continuation failed: res=%q err=%v", res, err)
		}
		if closeCalls.Load() != 0 || stopCalls.Load() != 1 {
			t.Fatalf("close=%d stop=%d, want 0 and 1", closeCalls.Load(), stopCalls.Load())
		}
		if old := waitForReplacementAgent(t, e, "todo_alias_pane", convID); old.ReleasedAt == nil {
			t.Fatalf("aliased herdr-pane agy link not released: %+v", old)
		}
	})
}
