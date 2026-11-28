package assistant

import (
	"context"
	"encoding/json"
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// The kind must survive the session leaving the machine's list. Exercise the storeless
// service as well as an independent read of the persisted link when PostgreSQL is available.
func TestQuietAgentKindSnapshot(t *testing.T) {
	for _, test := range []struct {
		name, path, reported, launcher, want string
	}{
		{"answered session wins", "answered", "codex", "claude", "codex"},
		{"answered launcher fallback", "answered", "", "codex", "codex"},
		{"answered unknown", "answered", "", "", ""},
		{"adopted session wins", "adopted", "codex", "claude", "codex"},
		{"adopted launcher fallback", "adopted", "", "codex", "codex"},
		{"adopted unknown", "adopted", "", "", ""},
		{"repair session", "repair", "codex", "claude", "codex"},
		{"repair unknown", "repair", "", "claude", ""},
	} {
		for _, storage := range []string{"memory", "postgres"} {
			t.Run(test.name+"/"+storage, func(t *testing.T) {
				var trace []DeviceCall
				ctx := context.WithValue(t.Context(), deviceCallsForTest{}, &trace)
				var database *db.DB
				var st Store
				if storage == "postgres" {
					database = taskRecoveryDatabase(t)
					st = database
				}
				var s *Service
				var f *fakeHub
				machine, sid, cwd := "box", "s1", "/code/app"
				if test.path == "repair" {
					s, f = launcherHealthHubOn(t, func(t *testing.T, f *fakeHub) *Service {
						if st == nil {
							return newStorelessService(t, f)
						}
						return newServiceOn(t, f, st)
					})
					f.hello.Agents.LauncherHealth.Repair.Skill = "agent-cli-setup"
					f.hello.Agents.LauncherHealth.Repair.Launcher = "work"
					machine, cwd = "server", "/home/me"
				} else {
					s, f = newQuietServiceOn(t, st)
					if st != nil {
						setModel(t, s, "agent.new", ModelBinding{Provider: "launcher.work"})
					}
				}
				f.launcherList = []proto.Launcher{{Name: "work", Agent: test.launcher}}
				f.startReply = func(_ map[string]any, session *proto.Session) *proto.StartInstead {
					session.Agent = test.reported
					return nil
				}
				if test.path == "adopted" {
					sid = "s9"
					// The node creates the session and its reply is lost. Inject the
					// timeout at the node, after dispatch, independently of database speed.
					f.failStart = context.DeadlineExceeded
					f.onStart = func(f *fakeHub, a map[string]any) {
						f.sessions["box"] = []proto.Session{{SessionID: sid, MachineID: machine, Cwd: cwd, Agent: test.reported,
							Launcher: a["launcher"].(string), StartedByPersona: true, Status: proto.StatusWorking,
							State: proto.StateWorking, UpdatedAt: time.Now().UTC()}}
					}
				}
				if test.path == "repair" {
					s.healthRound(ctx, true)
				} else {
					const args = `{"machine":"box","cwd":"/code/app","launcher":"work","prompt":"Verify release checks"}`
					if _, err := s.runTool(ctx, "server", "conv1", "agent_start", json.RawMessage(args)); err != nil {
						t.Fatal(err)
					}
				}
				starts := f.started()
				if len(starts) != 1 {
					t.Fatalf("start calls = %d, want one", len(starts))
				}
				start := starts[0]
				prompt, _ := start["prompt"].(string)
				wantPrompt := "Verify release checks"
				if test.path == "repair" {
					wantPrompt = "agent-cli-setup"
				}
				if start["cwd"] != cwd || start["launcher"] != "work" || !strings.Contains(prompt, wantPrompt) {
					t.Fatalf("unexpected start: %v", start)
				}
				if _, ok := start["agent"]; ok {
					t.Fatal("agent was added to session.start")
				}
				f.mu.Lock()
				calls := map[string]int{}
				for _, c := range trace {
					calls[c.Device+"/"+c.Method]++
				}
				f.mu.Unlock()
				wantCalls := quietKindNodeCalls(test.path, test.launcher)
				if !maps.Equal(calls, wantCalls) {
					t.Errorf("node calls = %v, want %v", calls, wantCalls)
				}
				// The explicitly resolved Codex launcher uses its own permissions. A reported
				// Codex kind must never replace a different launcher's permission choice.
				var wantMode any = "bypassPermissions"
				var wantEffort any = ""
				if test.launcher == "codex" {
					wantMode = nil
				}
				if test.path == "repair" {
					wantEffort = nil
				}
				if start["permission_mode"] != wantMode || start["model"] != nil || start["effort"] != wantEffort {
					t.Errorf("start mode/model/effort = %v/%v/%v, want %v/nil/%v", start["permission_mode"], start["model"], start["effort"], wantMode, wantEffort)
				}
				f.setSessions(machine)
				todo := onlyTodo(s)
				if len(todo.Agents) != 1 {
					t.Fatalf("task has %d linked agents", len(todo.Agents))
				}
				link := todo.Agents[0]
				if database != nil {
					rows, err := database.ListAllTodoAgents(ctx)
					if err != nil {
						t.Fatal(err)
					}
					if len(rows) != 1 || rows[0].TodoID != todo.ID || rows[0].MachineID != machine || rows[0].SessionID != sid || rows[0].Agent != test.want {
						t.Errorf("persisted links = %+v, want %s/%s/%q", rows, machine, sid, test.want)
					}
				}
				if !todo.Quiet || link.MachineID != machine || link.SessionID != sid || link.Agent != test.want {
					t.Errorf("quiet=%v, linked %s/%s/%q, want %s/%s/%q", todo.Quiet, link.MachineID, link.SessionID, link.Agent, machine, sid, test.want)
				}
			})
		}
	}
}

// These are the calls the existing start paths make without looking up an Agent kind.
// Count the request's calls so an extra session, launcher or other node read fails the test.
func quietKindNodeCalls(path, launcher string) map[string]int {
	if path == "repair" {
		calls := map[string]int{"*/devices.list": 5, "server/session.start": 1}
		for _, machine := range []string{"server", "l4", "box"} {
			calls[machine+"/launchers.list"] = 1
			calls[machine+"/launchers.check"] = 1 // only the discovered "work" launcher
		}
		return calls
	}
	calls := map[string]int{"*/devices.list": 2, "server/session.get": 1, "box/launchers.list": 1,
		"box/session.start": 1, "box/sessions.list": 1}
	if path == "adopted" {
		// #5732 first reconciles the lost start by its task creation id, then the existing exact-id / heuristic adoption
		// reads the list once more. The actual Agent kind assertions above remain unchanged.
		calls["box/sessions.list"] += 2
	}
	if launcher == "codex" {
		calls["box/node.info"] = 1
	}
	return calls
}
