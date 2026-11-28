package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestRecurringTaskKeepsItsConversationDuringLauncherDiscoveryFailure(t *testing.T) {
	f := readyHub()
	s := newTestService(t, f)
	setTasks(s, Task{ID: "job", Title: "Check", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, At: time.Now().UTC(), Enabled: true})
	setModel(t, s, "task.job", ModelBinding{Provider: "launcher.codex", Model: "task-model"})

	catalog := func(baseURL string) {
		s.invalidateLauncherListCache("server")
		f.answerNode(DeviceLaunchersList, func(string, json.RawMessage) (any, error) {
			return map[string]any{"launchers": []proto.Launcher{{Name: "codex", Agent: "codex", Source: "zshrc", BaseURL: baseURL}}}, nil
		})
	}
	run := func(number int) ScheduleRun {
		t.Helper()
		task, _ := taskByID(s, "job")
		s.runDue(t.Context(), task.At)
		s.sched.inflight.Wait()
		got := waitRuns(t, s, number)[number-1]
		if got.Manual || got.Status != runOK || got.Execution == nil || got.SessionID == "" {
			t.Fatalf("scheduled run %d did not dispatch: %+v", number, got)
		}
		return got
	}
	assertResumed := func(got, first ScheduleRun) {
		t.Helper()
		send := f.lastSend(t)
		if got.SessionID != first.SessionID || got.Conversation != "resumed" || got.ConversationReason != "" ||
			send["session_id"] != first.SessionID || send["resume_only"] != true || send["task_execution"] != true ||
			send["model"] != got.Execution.Model {
			t.Fatalf("scheduled run replaced its own conversation %s: %+v (fingerprint %s), send %v",
				first.SessionID, got, got.Execution.Fingerprint, send)
		}
	}
	assertReplaced := func(got, previous ScheduleRun) {
		t.Helper()
		if got.SessionID == previous.SessionID || got.Conversation != "new" || got.ConversationReason != "execution_changed" {
			t.Fatalf("changed execution reused %s: %+v", previous.SessionID, got)
		}
	}

	catalog("https://first.example")
	first := run(1)
	if first.Execution.Fingerprint == "" || first.Execution.BindingFingerprint == "" {
		t.Fatalf("missing observed execution: %+v", first.Execution)
	}
	s.invalidateLauncherListCache("server")
	f.answerNode(DeviceLaunchersList, func(string, json.RawMessage) (any, error) {
		return nil, errors.New("synthetic catalog unavailable")
	})
	outage := run(2)
	assertResumed(outage, first)
	stored, _ := taskByID(s, "job")
	if outage.Execution.Fingerprint != "" || outage.Execution.BindingFingerprint != first.Execution.BindingFingerprint ||
		stored.RunExecution.Fingerprint != first.Execution.Fingerprint {
		t.Fatalf("outage changed saved evidence: current=%+v saved=%+v", outage.Execution, stored.RunExecution)
	}
	legacy := *stored.RunExecution
	legacy.BindingFingerprint = "" // the previous release stored only the combined digest
	stored.RunExecution = &legacy
	setTasks(s, stored)
	s.invalidateLauncherListCache("server")
	f.answerNode(DeviceLaunchersList, func(string, json.RawMessage) (any, error) {
		return nil, context.DeadlineExceeded
	})
	assertResumed(run(3), first)
	s.invalidateLauncherListCache("server")
	f.answerNode(DeviceLaunchersList, func(string, json.RawMessage) (any, error) {
		return map[string]any{"launchers": []proto.Launcher{{Name: "codex", Agent: "codex", Source: "builtin"}}}, nil
	})
	assertResumed(run(4), first)
	catalog("https://first.example")
	assertResumed(run(5), first)
	catalog("https://second.example")
	changed := run(6)
	assertReplaced(changed, first)
	if changed.Execution.Fingerprint == first.Execution.Fingerprint || changed.Execution.BindingFingerprint != first.Execution.BindingFingerprint {
		t.Fatalf("launcher change fingerprints: before=%+v after=%+v", first.Execution, changed.Execution)
	}
	f.answerNode(DeviceLaunchersList, func(string, json.RawMessage) (any, error) {
		return nil, errors.New("synthetic catalog unavailable")
	})
	setModel(t, s, "task.job", ModelBinding{Provider: "launcher.codex", Model: "replacement-model"})
	newModel := run(7)
	assertReplaced(newModel, changed)
	if newModel.Execution.BindingFingerprint == changed.Execution.BindingFingerprint {
		t.Fatalf("model change did not change its binding fingerprint: %+v", newModel.Execution)
	}
	stored, _ = taskByID(s, "job")
	legacy = *stored.RunExecution
	legacy.Fingerprint, legacy.BindingFingerprint = legacy.BindingFingerprint, ""
	stored.RunExecution = &legacy // an older run whose launcher identity was unavailable
	setTasks(s, stored)
	catalog("https://second.example")
	assertResumed(run(8), newModel)
	catalog("https://third.example")
	assertReplaced(run(9), newModel)
}

func TestTaskExecutionInheritsOnItsComputer(t *testing.T) {
	fastLauncherRecovery(t)
	for _, name := range []string{"remote-agent", "shared"} {
		t.Run(name, func(t *testing.T) {
			f := newFakeHub().withMachine()
			f.sessions["remote"] = nil
			f.zshrc = map[string][]proto.Launcher{
				"server": {{Name: "shared", Agent: "claude", Source: "zshrc", BaseURL: "https://main.example"}},
				"remote": {{Name: name, Agent: "claude", Source: "zshrc", BaseURL: "https://remote.example", Model: "remote-default"}},
			}
			// A remote default is used only when the operator names it as a same-agent recovery fallback.
			f.hello.Models.LauncherRecovery = map[string]LauncherRecovery{
				"remote": {WaitSeconds: 1, Fallbacks: map[string][]string{"shared": {name}}},
			}
			s := newTestService(t, f)
			setModel(t, s, "assistant", ModelBinding{Provider: "launcher.shared", Model: "main-only", Effort: "high"})
			setTasks(s, Task{ID: "job", Title: "Check", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true, ExecutionMachineID: "remote"})
			b, err := s.resolveModel(context.Background(), "task.job")
			if err != nil || b.Provider != launcherProvider(name) || b.Model != "" || b.Effort != "" {
				t.Fatalf("wrong computer's inheritance: %+v %v", b, err)
			}
			first, err := runNow(t, s, "job")
			if err != nil {
				t.Fatal(err)
			}
			p := f.lastSend(t)
			effort, _ := p["effort"].(string)
			if first.MachineID != "remote" || p["launcher"] != name || p["model"] != nil || effort != "" {
				t.Fatalf("inherited binding differs from the actual send: %+v %v", first, p)
			}
			before := waitRuns(t, s, 1)[0].Execution.Fingerprint
			// An alias can retain its name while changing the provider identity on this computer.
			f.mu.Lock()
			f.zshrc["remote"][0].BaseURL = "https://replacement.example"
			f.mu.Unlock()
			// Refresh the advertised node list through the same API used by the editor.
			if r := request(s, "GET", "/api/v1/machines/remote/launchers?purpose=task&refresh=1", ""); r.Code != 200 {
				t.Fatalf("refresh catalog: %d %s", r.Code, r.Body.String())
			} else {
				var catalog struct {
					Default struct{ Provider, Model, Effort string }
				}
				if err := json.Unmarshal(r.Body.Bytes(), &catalog); err != nil || catalog.Default.Provider != name || catalog.Default.Model != "" || catalog.Default.Effort != "" {
					t.Fatalf("picker inherited from another computer: %s %v", r.Body.String(), err)
				}
			}
			second, err := runNow(t, s, "job")
			if err != nil || second.SessionID == first.SessionID || waitRuns(t, s, 2)[1].Execution.Fingerprint == before {
				t.Fatalf("changed target identity reused a conversation: %+v %v", second, err)
			}
		})
	}
}

func TestTaskExecutionRuntimeReusesScheduledAndManualRuns(t *testing.T) {
	f := newFakeHub().withMachine()
	f.launcherList = []proto.Launcher{{Name: "dsh", Agent: "dsh", Source: "dsh", Assistant: true}}
	s := newTestService(t, f)
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.dsh"})
	setTasks(s, Task{ID: "job", Title: "Check", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
	starts, sends := 0, 0
	unknown := false
	f.answerNode(DeviceAssistantSend, func(_ string, raw json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(raw, &p)
		sends++
		if id, _ := p["session_id"].(string); id == "" {
			starts++
		} else if id != "runtime-task" || p["resume_only"] != true || p["task_execution"] != true {
			t.Errorf("runtime task lost its strict resume: %v", p)
		}
		if unknown {
			return nil, rpc.ErrClosed
		}
		return map[string]string{"session_id": "runtime-task"}, nil
	})
	for range 2 {
		if _, err := runNow(t, s, "job"); err != nil {
			t.Fatal(err)
		}
	}
	if r := request(s, "POST", "/api/v1/assistant/tasks/job/run", `{}`); r.Code != 200 {
		t.Fatalf("manual runtime resume: %d %s", r.Code, r.Body.String())
	}
	unknown = true
	if _, err := runNow(t, s, "job"); err == nil || starts != 1 || sends != 4 {
		t.Fatalf("unknown runtime delivery replaced or retried: %v starts=%d sends=%d", err, starts, sends)
	}
}

func TestTaskExecutionMovingComputersKeepsInheritanceSparse(t *testing.T) {
	fastLauncherRecovery(t)
	f := newFakeHub().withMachine()
	f.sessions["first"], f.sessions["second"] = nil, nil
	f.zshrc = map[string][]proto.Launcher{
		"server": {{Name: "main-agent", Source: "zshrc"}},
		"first":  {{Name: "first-agent", Source: "zshrc"}},
		"second": {{Name: "second-agent", Source: "zshrc"}},
	}
	f.hello.Models.LauncherRecovery = map[string]LauncherRecovery{
		"first":  {WaitSeconds: 1, Fallbacks: map[string][]string{"main-agent": {"first-agent"}}},
		"second": {WaitSeconds: 1, Fallbacks: map[string][]string{"main-agent": {"second-agent"}}},
	}
	s := newTestService(t, f)
	setModel(t, s, "assistant", ModelBinding{Provider: "launcher.main-agent", Model: "main-only"})
	setTasks(s, Task{ID: "job", Title: "Check", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
	for _, machine := range []string{"first", "second"} {
		r := request(s, "PUT", "/api/v1/assistant/tasks/job", `{"title":"Check","prompt":"Check files","repeat":"interval","every_minutes":30,"enabled":true,"execution_machine_id":"`+machine+`"}`)
		if r.Code != 200 {
			t.Fatalf("choose %s: %d %s", machine, r.Code, r.Body.String())
		}
		if _, err := runNow(t, s, "job"); err != nil {
			t.Fatal(err)
		}
		if p := f.lastSend(t); p["launcher"] != machine+"-agent" || p["model"] != nil {
			t.Fatalf("old computer's default leaked: %v", p)
		}
		rc, err := s.modelRecord(context.Background(), "task.job")
		if err != nil || rc.Binding != (ModelBinding{}) {
			t.Fatalf("inherited provider was pinned: %+v %v", rc, err)
		}
	}
	setModel(t, s, "task.job", ModelBinding{Provider: "launcher.missing-agent", Model: "explicit-model"})
	before := f.sent()
	if _, err := runNow(t, s, "job"); err == nil || f.sent() != before {
		t.Fatalf("explicit unavailable configuration was substituted: %v", err)
	}
}
