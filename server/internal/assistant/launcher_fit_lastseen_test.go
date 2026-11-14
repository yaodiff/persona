package assistant

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/nwverse/persona/server/internal/proto"
)

// A Claude wrapper (a relay script on PATH) whose computer is too slow to list its launchers at start still starts
// its task in bypassPermissions, from the last list the hub saw there; it used to fall to "default" and stop on the
// first Bash approval. A Codex alias in that list is not made a Claude start (its read-only task keeps plan).
func TestTaskOnSlowListUsesLastSeenAgent(t *testing.T) {
	for _, tc := range []struct {
		name, agent, want string
	}{
		{"relay", "", "bypassPermissions"},
		{"relay", "claude", "bypassPermissions"},
		{"cx", "codex", "plan"},
	} {
		t.Run(tc.name+"/"+tc.agent, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				s, f := readonlyTaskService(t)
				f.launcherList = nil // the live list can't be read
				s.models.zshrcMu.Lock()
				s.models.zshrc = map[string][]proto.Launcher{"box": {{Name: tc.name, Agent: tc.agent, Source: "path"}}}
				s.models.zshrcMu.Unlock()
				task := readonlyTestTask("plan")
				task.Launcher = tc.name
				if _, err := s.toolTaskPlan(context.Background(), "slow-chat", readonlyTaskArgs(t, task)); err != nil {
					t.Fatal(err)
				}
				synctest.Wait()
				if len(f.starts) != 1 || f.starts[0]["permission_mode"] != tc.want {
					t.Fatalf("start = %v, want permission_mode %s", f.starts, tc.want)
				}
			})
		})
	}
}

// Nothing is guessed when the snapshot does not name the launcher either.
func TestLastSeenAgentUnknownLauncher(t *testing.T) {
	s, _ := readonlyTaskService(t)
	s.models.zshrcMu.Lock()
	s.models.zshrc = map[string][]proto.Launcher{"box": {{Name: "relay"}}}
	s.models.zshrcMu.Unlock()
	if got := s.lastSeenAgent(context.Background(), "box", "other"); got != "" {
		t.Fatalf("lastSeenAgent(other) = %q, want empty", got)
	}
	if got := s.lastSeenAgent(context.Background(), "elsewhere", "relay"); got != "" {
		t.Fatalf("lastSeenAgent on another machine = %q, want empty", got)
	}
	if got := s.lastSeenAgent(context.Background(), "box", "relay"); got != "claude" {
		t.Fatalf("lastSeenAgent(relay) = %q, want claude", got)
	}
}
