package assistant

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// companionLaunchers is a computer whose Antigravity companion is connected: its launcher list names the companion's
// own agy launcher (source agy) beside Claude Code's.
func companionLaunchers() []proto.Launcher {
	return []proto.Launcher{{Name: "claude", Agent: "claude"}, {Name: "agy", Agent: "agy", Source: "agy", BuiltinCompanion: true}}
}

// paneLaunchers is the same computer with its companion away: agy is the login shell's CLI, which runs in a pane.
func paneLaunchers() []proto.Launcher {
	return []proto.Launcher{{Name: "claude", Agent: "claude"}, {Name: "agy", Agent: "agy", Source: "zshrc"}}
}

// startHosts is the host each session.start f saw named, "" for none.
func startHosts(f *fakeHub) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var hosts []string
	for _, start := range f.starts {
		host, _ := start["host"].(string)
		hosts = append(hosts, host)
	}
	return hosts
}

var companionAwayErr = &rpc.Error{Code: proto.CodeAgyCompanionOffline, Message: "the companion is not connected"}

// An agy task that names no host starts on the computer's Antigravity companion while that is connected, in the
// default host (a herdr pane) while it is not, and in the default host again when the companion the hub picked went
// away before the start reached it. A host the task names is kept; a companion it named that is away fails the start
// with the node's reason. Other launchers never get a host (#13447).
func TestTaskPlanStartHost(t *testing.T) {
	for _, tc := range []struct {
		name, launcher, host, role string
		tierHost                   *string
		launchers                  []proto.Launcher
		away                       bool     // the node answers a start naming agy with agy_companion_offline
		want                       []string // each session.start's host
		failed                     string   // the task's error code
	}{
		{name: "companion connected", launcher: "agy", launchers: companionLaunchers(), want: []string{"agy"}},
		{name: "no companion", launcher: "agy", launchers: paneLaunchers(), want: []string{""}},
		{name: "companion went away", launcher: "agy", launchers: companionLaunchers(), away: true, want: []string{"agy", ""}},
		{name: "named herdr", launcher: "agy", host: proto.EngineHerdr, launchers: companionLaunchers(), want: []string{"herdr"}},
		{name: "named companion", launcher: "agy", host: proto.HostAgy, launchers: companionLaunchers(), want: []string{"agy"}},
		{name: "named companion away", launcher: "agy", host: proto.HostAgy, launchers: companionLaunchers(), away: true,
			want: []string{"agy"}, failed: proto.CodeAgyCompanionOffline},
		{name: "claude", launcher: "claude", launchers: companionLaunchers(), want: []string{""}},
		{name: "preset herdr leader", launcher: "agy", role: roleLeader, tierHost: new(proto.EngineHerdr), launchers: companionLaunchers(), want: []string{"herdr"}},
		{name: "preset herdr worker", launcher: "agy", role: "worker", tierHost: new(proto.EngineHerdr), launchers: companionLaunchers(), want: []string{"herdr"}},
		{name: "named companion overrides preset", launcher: "agy", host: proto.HostAgy, role: roleLeader, tierHost: new(proto.EngineHerdr),
			launchers: companionLaunchers(), want: []string{"agy"}},
		{name: "other launcher bypasses preset", launcher: "claude", tierHost: new(proto.EngineHerdr), launchers: companionLaunchers(), want: []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, f := readonlyTaskService(t)
				f.launcherList = tc.launchers
				if tc.away {
					f.refuseHost = map[string]error{proto.HostAgy: companionAwayErr}
				}
				ctx := context.Background()
				task := readonlyTestTask("")
				task.Readonly, task.Launcher, task.Host = false, tc.launcher, tc.host
				task.Role = tc.role
				if tc.tierHost != nil {
					st := planUpdateStore{&planMemStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}}
					s.setStore(func() Store { return st })
					putTierProfiles(t, s, "economy", map[string]ModelTier{"agy-work": {Launcher: "agy", Host: tc.tierHost}}, nil)
					task.Tier = "agy-work"
				}
				if _, err := s.toolTaskPlan(ctx, "host-chat", readonlyTaskArgs(t, task)); err != nil {
					t.Fatal(err)
				}
				p := readonlyFeedPlan(t, s, "host-chat")
				if p.Tasks[0].Host != tc.host {
					t.Fatalf("planned host = %q, want %q", p.Tasks[0].Host, tc.host)
				}
				if _, err := s.startPlan(ctx, p.ID, nil, false); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if got := startHosts(f); !slices.Equal(got, tc.want) {
					t.Fatalf("session.start hosts = %q, want %q", got, tc.want)
				}
				got := readonlyFeedPlan(t, s, "host-chat").Tasks[0]
				if got.ErrorCode != tc.failed || (tc.failed == "") != (got.Error == "") {
					t.Fatalf("task = %+v, want error code %q", got, tc.failed)
				}
				if got.Host != tc.host {
					t.Fatalf("the start changed the task's own host to %q", got.Host)
				}
			})
		})
	}
}

// A host no Agent starts in is refused when the task is planned, and the companion runs nothing but agy: nothing is
// started either way.
func TestTaskPlanRefusesHostsItCannotStartIn(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		s, f := readonlyTaskService(t)
		f.launcherList = companionLaunchers()
		ctx := context.Background()
		task := readonlyTestTask("")
		task.Readonly, task.Launcher, task.Host = false, "agy", "tmux"
		if _, err := s.toolTaskPlan(ctx, "host-chat", readonlyTaskArgs(t, task)); err == nil ||
			!strings.Contains(err.Error(), `host "tmux"`) || !strings.Contains(err.Error(), "agy") {
			t.Fatalf("unknown host: %v", err)
		}
		task.Launcher, task.Host = "claude", proto.HostAgy
		if _, err := s.toolTaskPlan(ctx, "host-chat", readonlyTaskArgs(t, task)); err != nil {
			t.Fatal(err)
		}
		p := readonlyFeedPlan(t, s, "host-chat")
		if _, err := s.startPlan(ctx, p.ID, nil, false); err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		if got := readonlyFeedPlan(t, s, "host-chat").Tasks[0]; !strings.Contains(got.Error, "runs only the Antigravity CLI") {
			t.Fatalf("claude on the companion = %+v", got)
		}
		if hosts := startHosts(f); len(hosts) != 0 {
			t.Fatalf("a start went to the node: %q", hosts)
		}
	})
}

// agent_start takes the same host: an agy Agent that names none starts on the companion while it is connected, and
// in the default host when it is not or went away; a named host is kept, and one no Agent starts in is refused.
func TestAgentStartHost(t *testing.T) {
	for _, tc := range []struct {
		name, args string
		usePreset  bool
		launchers  []proto.Launcher
		away       bool
		want       []string
	}{
		{name: "companion connected", args: `"launcher":"agy"`, launchers: companionLaunchers(), want: []string{"agy"}},
		{name: "no companion", args: `"launcher":"agy"`, launchers: paneLaunchers(), want: []string{""}},
		{name: "companion went away", args: `"launcher":"agy"`, launchers: companionLaunchers(), away: true, want: []string{"agy", ""}},
		{name: "named herdr", args: `"launcher":"agy","host":"herdr"`, launchers: companionLaunchers(), want: []string{"herdr"}},
		{name: "preset herdr", args: `"tier":"agy-work"`, usePreset: true, launchers: companionLaunchers(), want: []string{"herdr"}},
		{name: "named companion overrides preset", args: `"tier":"agy-work","host":"agy"`, usePreset: true, launchers: companionLaunchers(), want: []string{"agy"}},
		{name: "claude", args: `"launcher":"claude"`, launchers: companionLaunchers(), want: []string{""}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var st Store
			if tc.usePreset {
				st = &memStore{}
			}
			s, f := newTaskServiceOn(t, st)
			f.launcherList = tc.launchers
			if tc.usePreset {
				putTierProfiles(t, s, "economy", map[string]ModelTier{"agy-work": {Launcher: "agy", Host: new(proto.EngineHerdr)}}, nil)
			}
			if tc.away {
				f.refuseHost = map[string]error{proto.HostAgy: companionAwayErr}
			}
			args := json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"inspect the login flow",` + tc.args + `}`)
			if _, err := s.runTool(context.Background(), "server", "conv9", "agent_start", args); err != nil {
				t.Fatal(err)
			}
			if got := startHosts(f); !slices.Equal(got, tc.want) {
				t.Fatalf("session.start hosts = %q, want %q", got, tc.want)
			}
		})
	}
	s, f := newTaskService(t)
	f.launcherList = companionLaunchers()
	_, err := s.runTool(context.Background(), "server", "conv9", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"inspect","launcher":"agy","host":"tmux"}`))
	if err == nil || !strings.Contains(err.Error(), "no agent was started") || len(startHosts(f)) != 0 {
		t.Fatalf("unknown host: %v, starts %q", err, startHosts(f))
	}
}
