package agents

import (
	"context"
	"testing"
	"time"
)

// A poll that finds a session moved reports it, and the list it found that in stays cached: the reads its hint brings
// are answered from it, not by asking the source once more each, and they see the change.
func TestPollKeepsTheListItReportsFrom(t *testing.T) {
	f := &fakeBridge{sessions: []AgentSession{{SessionKey: "a", RankTimeMs: 1}, {SessionKey: "b", RankTimeMs: 1}}}
	s, changes := newSvc(f, nil)
	ctx := context.Background()
	s.PollOnce(ctx)
	f.mu.Lock()
	f.sessions[1].UnreadCount = 3
	f.mu.Unlock()
	s.PollOnce(ctx)
	if len(*changes) != 1 {
		t.Fatalf("changes %v", *changes)
	}
	for range 4 { // the paged list's sections, each reading on the hint
		list, err := s.Sessions(ctx, "", "")
		if err != nil {
			t.Fatal(err)
		}
		if len(list) != 2 || list[1].UnreadCount != 3 {
			t.Fatalf("list %+v", list)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listN != 2 {
		t.Fatalf("the source was asked %d times, want 2 (the two polls)", f.listN)
	}
}

// putChanged drops the changed sessions' pages and rejects what was asked before it; a list asked while another
// change came in is not kept, since it may predate that change.
func TestPutChangedKeepsOnlyACurrentList(t *testing.T) {
	c := newCache(time.Minute)
	list := []Session{{ID: "s:a", SessionKey: "a"}}
	page := &MessagePage{}

	gen := c.generation("s")
	c.putLatest("s", gen, "s:a", 20, page)
	c.putLatest("s", gen, "s:b", 20, page)
	c.putChanged("s", gen, list, []string{"s:a"})
	if got, ok := c.getSessions("s"); !ok || len(got) != 1 {
		t.Fatalf("current list not kept: %v %v", got, ok)
	}
	if _, ok := c.getLatest("s:a", 20); ok {
		t.Fatal("the changed session's page survived")
	}
	if _, ok := c.getLatest("s:b", 20); !ok {
		t.Fatal("an unchanged session's page was dropped")
	}
	c.putSessions("s", gen, nil) // a read that asked before the change answers late
	if got, ok := c.getSessions("s"); !ok || len(got) != 1 {
		t.Fatalf("a list asked before the change replaced the poll's: %v %v", got, ok)
	}

	gen = c.generation("s")
	c.invalidate("s", []string{"s:a"}) // a realtime update while the poll was asking
	c.putChanged("s", gen, list, []string{"s:a"})
	if _, ok := c.getSessions("s"); ok {
		t.Fatal("a list asked before a newer change was kept")
	}
}
