package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func archiveTestService(t *testing.T) (*fleetTest, []proto.Session) {
	t.Helper()
	ft := newFleetTest(t)
	rows := []proto.Session{}
	for _, agent := range []string{"claude", "codex"} {
		rows = append(rows, proto.Session{MachineID: "mac", SessionID: agent + "-session", Agent: agent,
			Status: proto.StatusOffline, State: proto.StateOffline, Title: agent + " history", UpdatedAt: time.Now().Add(-time.Hour)})
	}
	ft.d.setSessions("mac", rows)
	return ft, rows
}

func archiveRequest(s *Service, mid, sid, role string, archived bool) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/", nil)
	r.SetPathValue("sid", sid)
	r.Header.Set(RoleHeader, role)
	s.setAgentSessionArchive(w, r, mid, archived)
	return w
}

func archiveRows(t *testing.T, s *Service) []proto.Session {
	t.Helper()
	w := httptest.NewRecorder()
	s.listArchivedSessions(w, httptest.NewRequest(http.MethodGet, "/api/v1/sessions/archived", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("archives: %d %s", w.Code, w.Body)
	}
	var body struct {
		Sessions []proto.Session `json:"sessions"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Sessions
}

func TestArchiveSnapshotKeepsStopCauseWithoutLiveControls(t *testing.T) {
	for _, cause := range []proto.StopCause{
		proto.StopTaskCompleted, proto.StopIdle, proto.StopRestart, proto.StopUser, proto.StopUnknown, "future-cause",
	} {
		t.Run(string(cause), func(t *testing.T) {
			c := stoppedBoxAgent("stop0001")
			c.SourceKind = "codex_history"
			c.ReadOnlyReason = "The linked to-do was closed."
			c.Capabilities = &proto.SessionCapabilities{
				Read: true, Send: true, Terminal: true, Attach: true, Interrupt: true, Approve: true,
				StopCause: cause, Modes: proto.CanModes(true), Model: proto.CanModes(true),
			}
			snapshot := archiveSnapshot(c)
			want := &proto.SessionCapabilities{Read: true, Terminal: true, StopCause: cause}
			if !reflect.DeepEqual(snapshot.Capabilities, want) || !stoppedAgent(snapshot) {
				t.Fatalf("lost stopped provenance or retained live controls: %+v", snapshot.Capabilities)
			}
			if snapshot.ReadOnlyReason != c.ReadOnlyReason {
				t.Fatalf("lost closed-task reason: %q, want %q", snapshot.ReadOnlyReason, c.ReadOnlyReason)
			}
		})
	}
	c := stoppedBoxAgent("stop0001")
	c.Capabilities = nil
	if got := archiveSnapshot(c).Capabilities; !reflect.DeepEqual(got, &proto.SessionCapabilities{Read: true}) {
		t.Fatalf("snapshot inferred capabilities: %+v", got)
	}
}

func TestAgentSessionArchivesPersistAndRestore(t *testing.T) {
	ft, _ := archiveTestService(t)
	for _, agent := range []string{"claude", "codex"} {
		for range 2 {
			if w := archiveRequest(ft.s, "mac", agent+"-session", roleFull, true); w.Code != 200 {
				t.Fatalf("%s: %d %s", agent, w.Code, w.Body)
			}
		}
	}
	if got := archiveRows(t, ft.s); len(got) != 2 || !got[0].Archived || !got[1].Archived {
		t.Fatalf("archives: %+v", got)
	}
	for _, row := range ft.s.decorated("mac") {
		if !row.Archived {
			t.Fatalf("inventory missing archive projection: %+v", row)
		}
	}
	restarted, _ := archiveTestService(t)
	restarted.s.setStore(func() Store { return ft.st })
	restarted.d.setSessions("mac", nil)
	rows := archiveRows(t, restarted.s)
	if len(rows) != 2 {
		t.Fatalf("lost snapshots after source inventory expired: %+v", rows)
	}
	for _, row := range rows {
		if row.Status != proto.StatusOffline || row.Capabilities == nil || !row.Capabilities.Read || row.Capabilities.Send {
			t.Fatalf("unsafe fallback: %+v", row)
		}
		for range 2 {
			if w := archiveRequest(restarted.s, "mac", row.SessionID, roleFull, false); w.Code != 200 {
				t.Fatalf("restore: %d %s", w.Code, w.Body)
			}
		}
	}
	if got := archiveRows(t, restarted.s); len(got) != 0 {
		t.Fatalf("restore stayed archived: %+v", got)
	}
	again, _ := archiveTestService(t)
	again.s.setStore(func() Store { return ft.st })
	again.d.setSessions("mac", nil)
	restored := again.s.decorated("mac")
	if len(restored) != 2 {
		t.Fatalf("restores lost on restart: %+v", restored)
	}
	for _, row := range restored {
		if row.Archived {
			t.Fatalf("restored row archived again: %+v", row)
		}
	}
}

func TestAgentSessionArchiveActivityPermanentlyRestores(t *testing.T) {
	for _, activity := range []string{"timestamp", "busy"} {
		t.Run(activity, func(t *testing.T) {
			ft, rows := archiveTestService(t)
			if w := archiveRequest(ft.s, "mac", "codex-session", roleFull, true); w.Code != 200 {
				t.Fatal(w.Body)
			}
			if activity == "timestamp" {
				rows[1].UpdatedAt = time.Now()
			} else {
				rows[1].Status = proto.StatusWorking
			}
			ft.d.setSessions("mac", rows)
			ft.s.decorated("mac")
			if len(archiveRows(t, ft.s)) != 0 {
				t.Fatal("new source activity still archived")
			}
			rows[1].Status = proto.StatusOffline
			ft.d.setSessions("mac", rows)
			for _, row := range ft.s.decorated("mac") {
				if row.Archived {
					t.Fatalf("idle disappeared again: %+v", row)
				}
			}
			restarted, _ := archiveTestService(t)
			restarted.s.setStore(func() Store { return ft.st })
			restarted.d.setSessions("mac", nil)
			if got := archiveRows(t, restarted.s); len(got) != 0 {
				t.Fatalf("restore not persisted: %+v", got)
			}
		})
	}
}

func TestAgentSessionArchiveValidation(t *testing.T) {
	ft, _ := archiveTestService(t)
	for _, role := range []string{"answer", "extension", "link"} {
		for _, archived := range []bool{true, false} {
			if w := archiveRequest(ft.s, "mac", "codex-session", role, archived); w.Code != 403 {
				t.Fatalf("role %s: %d", role, w.Code)
			}
		}
	}
	if w := archiveRequest(ft.s, "mac", "missing", roleFull, true); w.Code != 404 {
		t.Fatalf("unknown: %d", w.Code)
	}
	if w := archiveRequest(ft.s, "unknown", "codex-session", roleFull, true); w.Code != 404 {
		t.Fatalf("unknown machine: %d", w.Code)
	}
	machine, ok := ft.s.fleetMachine("mac")
	if !ok {
		t.Fatal("missing fixture machine")
	}
	// Validate flags before decoration, which intentionally derives interaction and assistant state from the manager.
	for _, variant := range []proto.Session{
		{Starting: true}, {Status: proto.StatusWorking}, {Status: proto.StatusBlocked},
		{PendingInteractions: 1}, {BackgroundWorking: true}, {BackgroundWaiting: true},
		{Subagents: []proto.Subagent{{Running: true}}},
		{Background: []proto.BackgroundTask{{ID: "pending"}}}, {Workflows: []proto.Workflow{{Running: true}}},
		{State: proto.StateNeedsYou},
	} {
		if !agentArchiveBusy(variant) {
			t.Fatalf("busy session accepted: %+v", variant)
		}
	}
	// A finished turn awaiting the user's reply is idle: archiving it is the user's answer ("Delete chat" took it too).
	if agentArchiveBusy(proto.Session{AwaitsReply: true, Status: proto.StatusIdle}) {
		t.Fatal("an idle agent awaiting a reply counted as busy")
	}
	for _, variant := range []struct {
		row  proto.Session
		want int
	}{
		{proto.Session{SessionID: "assistant", Agent: "claude", Cwd: machine.assistantCwd()}, 400},
		// every runtime archives the same way (a manager-owned record): pi, opencode, DSH, a Codex App thread…
		{proto.Session{SessionID: "pi", Agent: "pi", SourceKind: "runtime", Status: proto.StatusIdle}, 200},
		{proto.Session{SessionID: "opencode", Agent: "opencode", Status: proto.StatusDone}, 200},
		{proto.Session{SessionID: "legacy", Status: proto.StatusOffline}, 200},
		{proto.Session{SessionID: "working", Agent: "claude", Status: proto.StatusWorking}, 409},
	} {
		ft.d.setSessions("mac", []proto.Session{variant.row})
		if w := archiveRequest(ft.s, "mac", variant.row.SessionID, roleFull, true); w.Code != variant.want {
			t.Fatalf("%s: %d %s", variant.row.SessionID, w.Code, w.Body)
		}
	}
}

func TestAgentSessionArchiveMachineIdentity(t *testing.T) {
	ft, _ := archiveTestService(t)
	if w := archiveRequest(ft.s, "mac", "claude-session", roleFull, true); w.Code != 200 {
		t.Fatal(w.Body)
	}
	ft.d.add("other", "Other")
	ft.d.setSessions("other", []proto.Session{{SessionID: "claude-session", Agent: "claude"}})
	if got := ft.s.decorated("other"); len(got) != 1 || got[0].Archived {
		t.Fatalf("cross-machine collision: %+v", got)
	}
}

func TestAgentSessionArchiveServiceHTTP(t *testing.T) {
	ft, _ := archiveTestService(t)
	request := func(method, path string) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, nil)
		r.Header.Set(RoleHeader, roleFull)
		ft.s.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		return w
	}
	list := func() []proto.Session {
		t.Helper()
		w := request(http.MethodGet, "/api/v1/sessions/archived?machine_id=mac")
		var body struct {
			Sessions []proto.Session `json:"sessions"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body.Sessions
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("initial archives: %+v", got)
	}
	for _, agent := range []string{"claude", "codex"} {
		request(http.MethodPost, "/api/v1/sessions/mac/"+agent+"-session/archive")
	}
	if got := list(); len(got) != 2 || !got[0].Archived || !got[1].Archived {
		t.Fatalf("HTTP archive list: %+v", got)
	}
	for _, agent := range []string{"claude", "codex"} {
		request(http.MethodPost, "/api/v1/sessions/mac/"+agent+"-session/unarchive")
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("HTTP restore stayed archived: %+v", got)
	}
	for _, row := range ft.s.decorated("mac") {
		if row.Archived {
			t.Fatalf("HTTP restore missing from inventory: %+v", row)
		}
	}
}

// decoratedSession decorates one listed row on its own, and finds an archived row its node no longer lists; either
// way it is the row the whole decorated list has (#2472).
func TestDecoratedSessionIsTheListsRow(t *testing.T) {
	ft, rows := archiveTestService(t)
	if w := archiveRequest(ft.s, "mac", "codex-session", roleFull, true); w.Code != 200 {
		t.Fatalf("archive: %d %s", w.Code, w.Body)
	}
	done := proto.Session{MachineID: "mac", SessionID: "done-session", Agent: "claude", Status: proto.StatusDone, PaneID: "p1",
		Title: "Fix the login", LastText: "Fixed. Shall I push it to beta?", UpdatedAt: time.Now()}
	ft.s.markSeen(context.Background(), "mac", "claude-session")
	for _, listed := range [][]proto.Session{{rows[0], rows[1], done}, {rows[0], done}} { // then codex's row is gone
		ft.d.setSessions("mac", slices.Clone(listed))
		for _, want := range ft.s.decorated("mac") {
			got, ok := ft.s.decoratedSession("mac", want.SessionID)
			if !ok || !reflect.DeepEqual(got, want) {
				t.Fatalf("%s (listed %d): decoratedSession\n%+v\nthe list's row\n%+v", want.SessionID, len(listed), got, want)
			}
		}
	}
	if got, ok := ft.s.decoratedSession("mac", "codex-session"); !ok || !got.Archived {
		t.Fatalf("the archived row the node no longer lists: %+v (found %v)", got, ok)
	}
}

// A list read while an agent still waited on the user does not undo the archive written after it (#4372): a reader
// (a list publish, the archived list, another archive) that read its list before the agent was closed and archived,
// and got the archive's lock only after, must not take that old busy row for new activity. Activity read after the
// archive still restores it.
func TestAgentSessionArchiveIgnoresAListReadBeforeIt(t *testing.T) {
	ft, row := closeTestService(t, func(s *proto.Session) {
		s.Status, s.State = proto.StatusBlocked, proto.StateNeedsYou
	})
	s, ctx := ft.s, context.Background()
	mark := s.archiveMark()
	stale := s.decoratedUnarchived("mac") // the reader reads the list: the agent still waits on the user
	closed := row
	closed.Status, closed.State, closed.PaneID = proto.StatusOffline, proto.StateOffline, ""
	ft.d.setSessions("mac", []proto.Session{closed}) // its node lists it closed
	if err := s.noteAgentArchive(ctx, closed, true); err != nil {
		t.Fatal(err)
	}
	// Only now does the reader get the archive's lock.
	list, changed, err := s.archivedSessions(ctx, "mac", stale, mark)
	if err != nil {
		t.Fatal(err)
	}
	if changed || len(list) != 1 || !list[0].Archived {
		t.Fatalf("the old list showed it restored: changed=%v %+v", changed, list)
	}
	if !archivedIDs(t, s)[row.SessionID] {
		t.Fatal("a list read before the archive restored it")
	}
	ft.d.setSessions("mac", []proto.Session{row}) // asking the user again, read after the archive
	if archivedIDs(t, s)[row.SessionID] {
		t.Fatal("activity after the archive did not restore it")
	}
}

// A conversation archived under an id it went by before (a Paseo agent's transcript, or an earlier fork of it, which
// it was listed under before #6709) is not repeated in Archived or the inventory while a row names that id among its
// aliases (#8131): the row listed under its own id now is the conversation. The record stays: once no row names the
// id, it is listed again. A record under another id, and one on another machine, are not touched.
func TestArchiveUnderAnAliasIsTheListedRow(t *testing.T) {
	const transcript, fork = "5d510000-0000-4000-8000-0000000000e1", "5d510000-0000-4000-8000-0000000000e0"
	ft, rows := archiveTestService(t)
	s, ctx := ft.s, context.Background()
	old := time.Now().Add(-48 * time.Hour)
	for _, sid := range []string{transcript, fork} { // archived while the node listed the conversation under these ids
		if err := s.noteAgentArchive(ctx, proto.Session{MachineID: "mac", SessionID: sid, Agent: "claude", Title: "Fix the build",
			Status: proto.StatusOffline, State: proto.StateOffline, UpdatedAt: old}, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.noteAgentArchive(ctx, rows[1], true); err != nil { // an Agent archived under its own id
		t.Fatal(err)
	}
	if got := archivedIDs(t, s); !got[transcript] || !got[fork] || !got[rows[1].SessionID] {
		t.Fatalf("archived %v", got)
	}

	live := proto.Session{MachineID: "mac", SessionID: "paseo-e1", Agent: "claude", Title: "Fix the build", Status: proto.StatusIdle,
		State: proto.StateIdle, UpdatedAt: time.Now(), SourceKind: proto.SourceRuntime}
	live.SetAliases([]string{transcript, fork})
	ft.d.setSessions("mac", []proto.Session{rows[0], rows[1], live})
	if got := archivedIDs(t, s); got[transcript] || got[fork] || !got[rows[1].SessionID] || len(got) != 1 {
		t.Fatalf("Archived while a row names the conversation's other ids: %v", got)
	}
	for _, row := range s.decorated("mac") {
		if row.SessionID == transcript || row.SessionID == fork {
			t.Fatalf("the inventory repeats the conversation: %+v", row)
		}
	}

	ft.d.setSessions("mac", []proto.Session{rows[0], rows[1]}) // no row names them any more
	if got := archivedIDs(t, s); !got[transcript] || !got[fork] {
		t.Fatalf("the records went with the row that named them: %v", got)
	}
}

func TestRuntimeListIDMatchesJSONMarshal(t *testing.T) {
	cases := [][2]string{
		{"", ""},
		{"mac", "claude-11111111-2222-4333-8444-555555555555"},
		{"box-01", "session:with/safe_punct.~@#%^*()+=[]{}|;',?"},
		{`quote"machine`, `back\slash`},
		{"<html&tag>", "a<b&c>d"},
		{"ctrl\x00nul", "line\nbreak\tand\rcarriage"},
		{"unit\x1fsep", "del\x7fchar"},
		{"\u4f1a\u8bdd", "\u6d4b\u8bd5-\U0001f680"},
		{"line\u2028sep", "para\u2029sep"},
		{"bad\xffutf8", "more\xfe\x80bytes"},
		{strings.Repeat("m", 96), strings.Repeat("s", 96)},
		{strings.Repeat("a", 80) + "<", strings.Repeat("b", 80) + `"`},
	}
	for b := range 256 {
		s := string([]byte{'x', byte(b), 'y'})
		cases = append(cases, [2]string{s, "sess"}, [2]string{"mach", s})
	}
	for _, tc := range cases {
		raw, err := json.Marshal([2]string{tc[0], tc[1]})
		if err != nil {
			t.Fatalf("marshal %q %q: %v", tc[0], tc[1], err)
		}
		want := string(raw)
		got := runtimeListID(tc[0], tc[1])
		if got != want {
			t.Fatalf("runtimeListID(%q, %q) = %q, want %q", tc[0], tc[1], got, want)
		}
		m := map[string]int{want: 42}
		if v, ok := lookupRuntimeListID(m, tc[0], tc[1]); !ok || v != 42 {
			t.Fatalf("lookupRuntimeListID(%q, %q) = (%d, %v), want (42, true)", tc[0], tc[1], v, ok)
		}
	}
}
