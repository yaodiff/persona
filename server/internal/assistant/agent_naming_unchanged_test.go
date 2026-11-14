package assistant

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func TestAgentNamePeriodicReturningRawTitleCountsChange(t *testing.T) {
	s, f, calls := namingFixture(t, func(w http.ResponseWriter, r *http.Request) { namingReply(w, "Fix export") })
	hours, maximum := 24, 2
	setSettings(t, s, func(v *Settings) { v.AgentAutoNamingIntervalHours = &hours; v.AgentAutoNamingMax = &maximum })
	f.sessions["server"][0].Title = "Fix export"
	f.sessions["server"][0].Agent = "claude"
	c := f.sessions["server"][0]
	s.agentNames.mu.Lock()
	if !s.agentNamesLocked(context.Background()) {
		t.Fatal("ledger load")
	}
	rec := agentNameRecord{Machine: "server", Session: "s1", Title: "Fix PDF images", RawTitle: c.Title, Changes: 1, NamedAt: time.Now().Add(-25 * time.Hour), NamedOutput: c.OutputTokens - 1}
	if err := s.putAgentNameLocked(context.Background(), agentNameKey("server", "s1"), rec); err != nil {
		t.Fatal(err)
	}
	s.agentNames.mu.Unlock()
	if !s.tryAgentName(context.Background(), "server", c, time.Now()) || s.agentDisplayName("server", c) != "Fix export" {
		t.Fatal("periodic result returning raw title not applied")
	}
	s.agentNames.mu.Lock()
	got := s.agentNames.records[agentNameKey("server", "s1")]
	s.agentNames.mu.Unlock()
	if got.Changes != 2 || calls.Load() != 1 || s.tryAgentName(context.Background(), "server", c, time.Now().Add(25*time.Hour)) {
		t.Fatalf("real title change bypassed maximum: %+v", got)
	}
}

func TestAgentNameConcisePromptUnchangedDoesNotRetry(t *testing.T) {
	for _, periodic := range []bool{false, true} {
		t.Run(map[bool]string{false: "default", true: "periodic"}[periodic], func(t *testing.T) {
			s, f, calls := namingFixture(t, func(w http.ResponseWriter, r *http.Request) { namingReply(w, "Fix export") })
			if periodic {
				hours := 24
				setSettings(t, s, func(v *Settings) { v.AgentAutoNamingIntervalHours = &hours })
			}
			f.sessions["server"][0].Title = "Fix export"
			f.sessions["server"][0].Agent = "claude"
			c := f.sessions["server"][0]
			now := time.Now()
			if !s.tryAgentName(context.Background(), "server", c, now) {
				t.Fatal("initial prompt naming not checked")
			}
			restarted := newServiceOn(t, f, s.store())
			for _, service := range []*Service{s, restarted} {
				if service.tryAgentName(context.Background(), "server", c, now.Add(time.Minute)) {
					t.Fatal("unchanged concise prompt was checked again")
				}
				current := c
				current.Title = "◑ Claude Code"
				if service.agentDisplayName("server", current) != "Fix export" || service.tryAgentName(context.Background(), "server", current, now.Add(time.Minute)) {
					t.Fatal("spinner lost the checked concise name or repeated the call")
				}
			}
			s.agentNames.mu.Lock()
			rec := s.agentNames.records[agentNameKey("server", "s1")]
			s.agentNames.mu.Unlock()
			if rec.Changes != 0 || calls.Load() != 1 {
				t.Fatalf("unchanged title counted a change or repeated: changes=%d calls=%d", rec.Changes, calls.Load())
			}
			c.OutputTokens++
			c.UpdatedAt = now.Add(25 * time.Hour)
			c.Status = proto.StatusWorking
			if agentNameDue(s.settings(context.Background()), rec, c, now.Add(25*time.Hour)) != periodic {
				t.Fatal("unchanged title did not respect periodic opt-in")
			}
		})
	}
}
