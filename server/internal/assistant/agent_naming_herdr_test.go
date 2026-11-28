package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgentNameUsesHerdrTabAndProtectsManualRename(t *testing.T) {
	s, f, calls := namingFixture(t, func(w http.ResponseWriter, _ *http.Request) { namingReply(w, "Fix export") })
	ctx := context.Background()
	interval, limit := 1, 3
	setSettings(t, s, func(v *Settings) {
		v.AgentAutoNamingIntervalHours, v.AgentAutoNamingMax = &interval, &limit
	})
	c := f.sessions["server"][0]
	c.SpaceKind, c.SlotID, c.SlotName = "herdr", "w1:t1", "agent-1"
	f.sessions["server"][0] = c
	if err := s.store().PutSetting(ctx, agentsUseHerdrTabNameKey, true); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	if !s.tryAgentName(ctx, "server", c, now) || calls.Load() != 1 || len(f.sends) != 1 {
		t.Fatalf("automatic tab rename: calls = %d, sends = %v", calls.Load(), f.sends)
	}
	params := f.sends[0]["rename"].(map[string]any)
	if params["tab_name"] != "Fix export" || params["title"] != nil || params["expected_tab_name"] != "agent-1" || params["tab_id"] != c.SlotID {
		t.Fatalf("wrong rename request: %v", params)
	}
	if name := s.agentDisplayName("server", c); name != "" {
		t.Fatalf("automatic tab rename also wrote a separate title %q", name)
	}
	// First the node reports the automatic name, then the owner gives the tab another name in herdr.
	c.SlotName = "Fix export"
	s.tryAgentName(ctx, "server", c, now)
	c.SlotName = "My export work"
	c.UpdatedAt = now.Add(2 * time.Hour)
	c.SetOutputTokens(int(c.OutputTokensValue()) + 1)
	f.sessions["server"][0] = c
	if s.tryAgentName(ctx, "server", c, c.UpdatedAt) || calls.Load() != 1 {
		t.Fatal("renamed an owner-named tab")
	}
	if rec := s.agentNames.records[agentNameKey("server", c.SessionID)]; !rec.ManualTab {
		t.Fatalf("manual tab rename was not recorded: %+v", rec)
	}
}

// The tab rename is a device call whose reply the node's reader loop reads; that loop also names every session row it
// is sent (agentDisplay, which takes the naming lock), so the lock must not be held while the call waits (#11752).
func TestAgentNameTabRenameDoesNotHoldTheNamingLock(t *testing.T) {
	s, f, _ := namingFixture(t, func(w http.ResponseWriter, _ *http.Request) { namingReply(w, "Fix export") })
	ctx := context.Background()
	c := f.sessions["server"][0]
	c.SpaceKind, c.SlotID, c.SlotName = "herdr", "w1:t1", "agent-1"
	f.sessions["server"][0] = c
	if err := s.store().PutSetting(ctx, agentsUseHerdrTabNameKey, true); err != nil {
		t.Fatal(err)
	}
	held := false
	f.onRename = func() {
		if !s.agentNames.mu.TryLock() {
			held = true
			return
		}
		s.agentNames.mu.Unlock()
	}
	if !s.tryAgentName(ctx, "server", c, time.Now()) || len(f.sends) != 1 {
		t.Fatalf("no tab rename: sends = %v", f.sends)
	}
	if held {
		t.Fatal("the naming lock was held across the tab rename call")
	}
	if rec := s.agentNames.records[agentNameKey("server", c.SessionID)]; rec.TabName != "Fix export" || rec.LastFailure != "" {
		t.Fatalf("the rename was not recorded: %+v", rec)
	}
}

func TestAgentNameDoesNotOverwriteTabRenamedDuringNaming(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	s, f, _ := namingFixture(t, func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
		namingReply(w, "Fix export")
	})
	ctx := context.Background()
	if err := s.store().PutSetting(ctx, agentsUseHerdrTabNameKey, true); err != nil {
		t.Fatal(err)
	}
	c := f.sessions["server"][0]
	c.SpaceKind, c.SlotID, c.SlotName = "herdr", "w1:t1", "agent-1"
	f.sessions["server"][0] = c
	done := make(chan bool, 1)
	go func() { done <- s.tryAgentName(ctx, "server", c, time.Now()) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("automatic naming did not call the model")
	}
	f.mu.Lock()
	f.sessions["server"][0].SlotName = "My export work"
	f.mu.Unlock()
	close(release)
	if !<-done || len(f.sends) != 0 {
		t.Fatalf("owner's tab rename lost: %v", f.sends)
	}
}

func TestFeishuAgentsCardHerdrSessionNames(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	f := newFakeHub().withMachine()
	f.answerApp(AppFeishu, FeishuPost, func(json.RawMessage) (any, error) { return map[string]any{"message_id": "om_agents"}, nil })
	s := newTestService(t, f)
	ctx := context.Background()
	row := proto.Session{SessionID: "s1", Title: "CLI session name", SpaceKind: "herdr", SlotID: "w1:t1", SlotName: "Export work",
		State: proto.StateWorking, Status: proto.StatusWorking, UpdatedAt: time.Now()}
	for _, tc := range []struct {
		name, title, tab, primary, secondary string
		on                                   bool
	}{
		{"off", row.Title, row.SlotName, row.Title, "", false},
		{"on", row.Title, row.SlotName, row.SlotName, row.Title, true},
		{"next inventory", row.Title, "Owner's new name", "Owner's new name", row.Title, true},
		{"prompt with path", "user@host: /workspace", row.SlotName, row.SlotName, "", false},
		{"prompt without space", "user@host:~/workspace", row.SlotName, row.SlotName, "", false},
		{"empty title", "", row.SlotName, row.SlotName, "", false},
		{"no tab", row.Title, "", row.Title, "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := s.store().PutSetting(ctx, agentsUseHerdrTabNameKey, tc.on); err != nil {
				t.Fatal(err)
			}
			c := row
			c.Title, c.SlotName = tc.title, tc.tab
			f.sessions["server"] = []proto.Session{c}
			body, fallback := botCommandCard(t, s, f, &FeishuFeatures{Agents: true}, "om_agents_"+tc.name, "/agents")
			if !strings.Contains(body, "**"+tc.primary+"**") || !strings.Contains(fallback, tc.primary) {
				t.Fatalf("primary %q missing: %s\n%s", tc.primary, body, fallback)
			}
			if tc.secondary != "" && !strings.Contains(body, `\n`+tc.secondary) {
				t.Fatalf("secondary %q missing: %s", tc.secondary, body)
			}
			if tc.secondary == "" && tc.title != "" && tc.title != tc.primary && strings.Contains(body, tc.title) {
				t.Fatalf("fallback still shows prompt: %s", body)
			}
		})
	}
}
