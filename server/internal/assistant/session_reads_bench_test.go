package assistant

// What this manager's own readers of a machine's sessions cost (#3197): a turn end reads one session, the per-minute
// passes read what runs. Everything here uses only what main had before the readers changed, so the same file
// measures the old code and the new (benchstat), and the apps' list is checked against the bytes the old code wrote.

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"maps"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

var updateReads = flag.Bool("update-session-reads", false, "rewrite testdata/session_reads_apps_list.json")

// readsFleet is a manager whose machine "mac" lists n sessions (the last one session-<n-1>), half of them in a pane,
// none working, none touched for two days, and has archived records more sessions the node no longer lists, as the
// automatic archive leaves them (3 idle days; never removed).
func readsFleet(tb testing.TB, n, records int) *Service {
	tb.Helper()
	d := newFleetDevs()
	d.add("mac", "Mac")
	st := newFleetMem()
	f := newFakeHub()
	f.hello.Assistant.MachineID = "mac"
	f.hello.Assistant.Machines = []string{"mac"}
	f.devices = []Device{{ID: "mac", Home: "/home/u", Reachable: true}}
	s := New(Options{Hub: f, Store: func() Store { return st }, Devices: d, Logf: func(string, ...any) {}})
	old := time.Now().Add(-48 * time.Hour).UTC()
	list := make([]proto.Session, n)
	for i := range list {
		list[i] = proto.Session{SessionID: fmt.Sprintf("session-%05d", i), Cwd: "/code/example", Agent: "claude",
			Status: proto.StatusDone, Title: "Example task", LastText: strings.Repeat("Example result. ", 12), UpdatedAt: old}
		if i%2 == 0 {
			list[i].PaneID = fmt.Sprintf("p%d", i)
		}
	}
	d.setSessions("mac", list)
	archiveRecords(tb, s, records, old.Add(-30*24*time.Hour))
	return s
}

// archiveRecords adds n archived records of sessions "mac" no longer lists (gone-<i>), changed at.
func archiveRecords(tb testing.TB, s *Service, n int, at time.Time) {
	tb.Helper()
	if n == 0 {
		return
	}
	ctx := context.Background()
	b := &s.agentArchives
	if err := b.lock(ctx); err != nil {
		tb.Fatal(err)
	}
	defer b.unlock()
	if err := s.loadAgentArchivesLocked(ctx); err != nil {
		tb.Fatal(err)
	}
	next := maps.Clone(b.records)
	if next == nil {
		next = map[string]agentArchive{}
	}
	for i := range n {
		row := proto.Session{MachineID: "mac", SessionID: fmt.Sprintf("gone-%05d", i), Agent: "claude", Cwd: "/code/example",
			Title: "Fix the login page crash", LastText: strings.Repeat("Example result. ", 12), UpdatedAt: at}
		next[runtimeListID("mac", row.SessionID)] = agentArchive{Session: archiveSnapshot(row), Archived: true, ChangedAt: at}
	}
	if err := s.saveAgentArchivesLocked(ctx, next); err != nil {
		tb.Fatal(err)
	}
}

// readsSizes are the machines measured: a node's list of 50 and 500 sessions, without and with 1000 archive records.
var readsSizes = []struct{ sessions, records int }{{50, 0}, {50, 1000}, {500, 0}, {500, 1000}}

// BenchmarkSessionReadTurnEnd: a turn end (workTurnSettled) reads the session that stopped, through a new workView,
// as workGuard and agentStopped do for each wake.
func BenchmarkSessionReadTurnEnd(b *testing.B) {
	for _, z := range readsSizes {
		b.Run(fmt.Sprintf("sessions=%d/records=%d", z.sessions, z.records), func(b *testing.B) {
			s := readsFleet(b, z.sessions, z.records)
			ctx := context.Background()
			sid := fmt.Sprintf("session-%05d", z.sessions-1)
			b.ReportAllocs()
			for b.Loop() {
				if c, ok := s.newWorkView(ctx).session("mac", sid); !ok || c.SessionID != sid {
					b.Fatalf("session %s: %v", sid, ok)
				}
			}
		})
	}
}

// BenchmarkSessionReadIdleMinute: the passes an idle manager runs every minute (closeDoneTaskAgents, closeIdleAgents,
// backfillWork) with one task done long ago whose agent's session the node no longer lists, and nothing to close.
func BenchmarkSessionReadIdleMinute(b *testing.B) {
	for _, z := range readsSizes {
		b.Run(fmt.Sprintf("sessions=%d/records=%d", z.sessions, z.records), func(b *testing.B) {
			s := readsFleet(b, z.sessions, z.records)
			ctx := context.Background()
			doneTaskOnGoneAgent(b, s)
			refused, done, seen := taskCloseRefused{}, doneSeen{}, idleSeen{}
			b.ReportAllocs()
			for b.Loop() {
				s.closeDoneTaskAgents(ctx, refused, done)
				s.closeIdleAgents(ctx, refused, done, seen)
				s.backfillWork(ctx, s.newWorkView(ctx), time.Now())
			}
			if len(refused) != 0 {
				b.Fatalf("closed or refused: %v", refused)
			}
		})
	}
}

// doneTaskOnGoneAgent is a task, done, whose agent's session "mac" no longer lists: its link stays, as a done task's do.
func doneTaskOnGoneAgent(tb testing.TB, s *Service) {
	tb.Helper()
	ctx := context.Background()
	todo, err := s.createTaskTodo(ctx, "fix the login crash", "", "conv1", "", "")
	if err != nil {
		tb.Fatal(err)
	}
	if err := s.linkTodoAgent(ctx, todo.ID, "mac", "gone-session", "fix the login crash", "/code/example", "claude"); err != nil {
		tb.Fatal(err)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, httptest.NewRequest("PATCH", "/api/v1/todos/"+todo.ID, strings.NewReader(`{"done":true}`)))
	if w.Code != 200 {
		tb.Fatalf("PATCH done = %d %s", w.Code, w.Body.String())
	}
	if got := s.doneTaskAgents(); len(got) != 1 {
		tb.Fatalf("done task agents = %+v", got)
	}
}

// readsGoldenFleet is a machine whose list has a bit of everything the archive and the fold put on it: rows the node
// lists (one archived, one a child of another, a tab's lead and worker) and rows only the archive keeps (archived, and
// restored, one a child of a listed row).
func readsGoldenFleet(t *testing.T) *Service {
	t.Helper()
	s := readsFleet(t, 0, 0)
	at := time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)
	zero := 0
	list := []proto.Session{
		{SessionID: "lead", Agent: "claude", Cwd: "/code/app", Status: proto.StatusWorking, Title: "Lead", UpdatedAt: at,
			PaneID: "p1", PaneIndex: &zero, SpaceKind: "herdr", SpaceID: "w1", SlotID: "t1", SpaceName: "app"},
		{SessionID: "worker", Agent: "codex", Cwd: "/code/app", Status: proto.StatusDone, Title: "Worker", UpdatedAt: at,
			PaneID: "p2", SpaceKind: "herdr", SpaceID: "w1", SlotID: "t1", SpaceName: "app", LastText: "Done."},
		{SessionID: "child", Agent: "claude", Cwd: "/code/app", Status: proto.StatusIdle, Title: "Child", UpdatedAt: at,
			ParentSessionID: "lead", IsSubagent: true},
		{SessionID: "archived-listed", Agent: "claude", Cwd: "/code/old", Status: proto.StatusDone, Title: "Old work",
			UpdatedAt: at, LastText: "Finished."},
		{SessionID: "plain", Agent: "claude", Cwd: "/code/other", Status: proto.StatusDone, Title: "Other", UpdatedAt: at},
	}
	s.devs.(*fleetDevs).setSessions("mac", list)
	ctx := context.Background()
	b := &s.agentArchives
	if err := b.lock(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.loadAgentArchivesLocked(ctx); err != nil {
		t.Fatal(err)
	}
	rec := func(sess proto.Session, archived bool) agentArchive {
		sess.MachineID = "mac"
		return agentArchive{Session: archiveSnapshot(sess), Archived: archived, ChangedAt: at.Add(-time.Hour)}
	}
	next := map[string]agentArchive{
		runtimeListID("mac", "archived-listed"): rec(list[3], true),
		runtimeListID("mac", "gone-archived"): rec(proto.Session{SessionID: "gone-archived", Agent: "claude",
			Cwd: "/code/old", Title: "Gone", UpdatedAt: at.Add(-72 * time.Hour)}, true),
		runtimeListID("mac", "gone-restored"): rec(proto.Session{SessionID: "gone-restored", Agent: "codex",
			Cwd: "/code/app", Title: "Restored", ParentSessionID: "plain", UpdatedAt: at.Add(-72 * time.Hour)}, false),
	}
	other := proto.Session{MachineID: "other", SessionID: "elsewhere", Agent: "claude", Title: "Another machine's", UpdatedAt: at}
	next[runtimeListID("other", "elsewhere")] = agentArchive{Session: archiveSnapshot(other), Archived: true, ChangedAt: at}
	if err := s.saveAgentArchivesLocked(ctx, next); err != nil {
		t.Fatal(err)
	}
	b.unlock()
	return s
}

// stateSince is the one value of a list that is the time it was first decorated.
var stateSince = regexp.MustCompile(`("state_since": ?)"[^"]*"`)

// The list the apps get (sessions.list, the same rows fleet.sessions tells) is byte for byte what it was before the
// manager's own readers stopped going through it (#3197): testdata/session_reads_apps_list.json was written by the
// code before that change (-update-session-reads).
func TestAppsSessionListUnchanged(t *testing.T) {
	s := readsGoldenFleet(t)
	ctx := context.Background()
	var got bytes.Buffer
	for range 2 { // the second read folds with what the first stored
		res, ok, err := s.localFleetDeviceCall(ctx, DeviceCall{Device: "mac", Method: DeviceSessionsList, Params: json.RawMessage(`{}`)})
		if !ok || err != nil {
			t.Fatalf("sessions.list: %v %v", ok, err)
		}
		var v any
		if err := json.Unmarshal(res, &v); err != nil {
			t.Fatal(err)
		}
		pretty, _ := json.MarshalIndent(v, "", "  ")
		got.Write(stateSince.ReplaceAll(pretty, []byte(`${1}"<first decorated>"`)))
		got.WriteByte('\n')
		// The raw bytes too: indenting must not hide a change in how they were written.
		got.Write(stateSince.ReplaceAll(res, []byte(`${1}"<first decorated>"`)))
		got.WriteByte('\n')
	}
	path := filepath.Join("testdata", "session_reads_apps_list.json")
	if *updateReads {
		if err := os.WriteFile(path, got.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got.Bytes(), want) {
		t.Errorf("the apps' session list changed:\n%s\nwant:\n%s", got.Bytes(), want)
	}
	if !bytes.Contains(want, []byte(`"gone-restored"`)) || !bytes.Contains(want, []byte(`"folded_under"`)) {
		t.Fatalf("the golden list lacks the archive's rows or a fold:\n%s", want)
	}
}
