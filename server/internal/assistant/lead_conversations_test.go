package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/supervisionrole"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A direction's lead conversation (#11392): kept listed however quiet, not deleted while it leads, still in the tree
// when its conversation is missing; notes saves that cannot overwrite each other and that reach the lead only when it
// is idle; an approval of assistant.leaders that compares the whole list. All ids, names and notes are placeholders.

// A current lead is kept listed on its direction record's computer (else the assistant's), on a fresh install with no
// setting touched, and leaves the set when its direction ends.
func TestProfileKeepsCurrentLeads(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	setSettings(t, s, func(v *Settings) { v.Main, v.Leaders = "main-1", []string{"main-1", "lead-a", "lead-b"} })
	b := &s.directions
	b.mu.Lock()
	if !s.directionsLocked(ctx) {
		t.Fatal("directions unreadable")
	}
	b.m["lead-a"] = &direction{MachineID: "box", By: directionByUser, At: time.Now()} // lead-b has no record yet
	b.mu.Unlock()
	s.publish(ctx)
	want := []SessionRef{{MachineID: "server", SessionID: "main-1"}, {MachineID: "box", SessionID: "lead-a"},
		{MachineID: "server", SessionID: "lead-b"}}
	if p := f.lastSnapshot().Profile; p == nil || !slices.Equal(p.Keep, want) {
		t.Fatalf("keep: %+v", p)
	}
	if ended, err := s.endDirection(ctx, "lead-a"); err != nil || !ended {
		t.Fatalf("end: %v %v", ended, err)
	}
	s.publish(ctx)
	want = []SessionRef{{MachineID: "server", SessionID: "main-1"}, {MachineID: "server", SessionID: "lead-b"}}
	if p := f.lastSnapshot().Profile; p == nil || !slices.Equal(p.Keep, want) {
		t.Fatalf("keep after the direction ended: %+v", p)
	}
}

// Deleting a current lead is refused with the protected-conversation code and a message, in the owner's language,
// that names the way out; once its direction ends the same delete goes through.
func TestDeleteRefusesACurrentLead(t *testing.T) {
	ctx := context.Background()
	f := healthHub()
	s := newTestService(t, f)
	lead := "work-0000-0000"
	setSettings(t, s, func(v *Settings) { v.MachineID, v.Main, v.Leaders = "server", "main-0000-0000", []string{lead} })
	path := "/api/v1/assistant/conversations/" + lead
	wayOut := map[i18n.Lang]string{i18n.En: "Unpin it", i18n.ZhHans: "取消常驻", i18n.Ja: "常駐をやめて"}
	for _, l := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		f.mu.Lock()
		f.hello.Locale = string(l)
		f.mu.Unlock()
		w := serve(s, "DELETE", path, `{"machine_id":"server"}`)
		var body struct{ Code, Error string }
		_ = json.Unmarshal(w.Body.Bytes(), &body)
		if w.Code != http.StatusBadRequest || body.Code != "conversation_protected" ||
			body.Error != i18n.T(l, "assistant-conversation_delete.lead_protected") || !strings.Contains(body.Error, wayOut[l]) {
			t.Fatalf("%s: %d %s", l, w.Code, w.Body)
		}
	}
	if !slices.ContainsFunc(s.view(ctx, false).Conversations, func(c proto.Session) bool { return c.SessionID == lead }) {
		t.Fatal("the refused delete took the lead off the list")
	}
	if ended, err := s.endDirection(ctx, lead); err != nil || !ended {
		t.Fatalf("end: %v %v", ended, err)
	}
	if w := serve(s, "DELETE", path, `{"machine_id":"server"}`); w.Code != http.StatusOK {
		t.Fatalf("delete after the direction ended: %d %s", w.Code, w.Body)
	}
}

// The direction tree lists a direction whose conversation its computer does not list, and one whose conversation was
// deleted (while it was not a lead, then made one again, or before deleting a lead was refused), each with its name
// and tasks.
func TestDirectionTreeListsDirectionsWhoseConversationIsMissing(t *testing.T) {
	e := treeEnv(t) // leads conv_lead_a and conv_lead_b
	ctx := context.Background()
	e.leaders("conv_lead_a")
	e.f.setSessions("server", proto.Session{MachineID: "server", SessionID: "conv_lead_b", Assistant: true, Status: proto.StatusIdle})
	if err := e.s.deleteConversation(ctx, "server", "conv_lead_b"); err != nil {
		t.Fatal(err)
	}
	e.f.setSessions("server", proto.Session{MachineID: "server", SessionID: treeMain, Assistant: true, Status: proto.StatusIdle})
	e.leaders("conv_lead_a", "conv_lead_b")
	e.syncLeaders()
	a := e.task("A's task", supervisedBy("conv_lead_a"))
	b := e.task("B's task", supervisedBy("conv_lead_b"))

	rec := httptest.NewRecorder()
	e.s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant/directions/tree", nil))
	var tree directionTree
	if err := json.Unmarshal(rec.Body.Bytes(), &tree); rec.Code != http.StatusOK || err != nil {
		t.Fatalf("tree: %d %s", rec.Code, rec.Body)
	}
	got := tree.Directions
	if len(got) != 3 || !got[2].Secretary {
		t.Fatalf("directions: %+v", got)
	}
	if got[0].ID != "conv_lead_a" || got[0].Name != "Android parity" || !slices.Equal(taskIDs(got[0]), []string{a}) {
		t.Fatalf("not listed: %+v", got[0])
	}
	if got[1].ID != "conv_lead_b" || got[1].Name != "Push relay" || !slices.Equal(taskIDs(got[1]), []string{b}) {
		t.Fatalf("deleted: %+v", got[1])
	}
	if _, listed := e.s.session(ctx, "server", "conv_lead_a"); listed {
		t.Fatal("conv_lead_a is listed")
	}
	if err := e.s.conversationIDAvailable(ctx, "conv_lead_b"); convCode(err) != "conversation_deleted" {
		t.Fatalf("conv_lead_b is not deleted: %v", err)
	}
}

// notesAnswer is a notes PUT's answer: the notes kept and their time, or the 409's code and what is stored.
type notesAnswer struct {
	Code    string     `json:"code"`
	Error   string     `json:"error"`
	Notes   string     `json:"notes"`
	NotesAt *time.Time `json:"notes_at"`
}

func putNotesAnswer(t *testing.T, s *Service, body string, status int) notesAnswer {
	t.Helper()
	rec := putNotes(t, s, "conv_lead", body, false)
	var out notesAnswer
	if err := json.Unmarshal(rec.Body.Bytes(), &out); rec.Code != status || err != nil {
		t.Fatalf("PUT %s: %d %s, want %d", body, rec.Code, rec.Body, status)
	}
	return out
}

// listedNotesAt is conv_lead's notes_at exactly as GET /api/v1/assistant/leaders writes it.
func listedNotesAt(t *testing.T, s *Service) string {
	t.Helper()
	return strings.Trim(string(listedNotesAtRaw(t, s, "conv_lead")), `"`)
}

// listedNotesAtRaw is direction id's notes_at field as GET /api/v1/assistant/leaders writes it (nil: left out).
func listedNotesAtRaw(t *testing.T, s *Service, id string) json.RawMessage {
	t.Helper()
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/v1/assistant/leaders", nil))
	var list struct {
		Leaders []struct {
			ID      string          `json:"id"`
			NotesAt json.RawMessage `json:"notes_at"`
		} `json:"leaders"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	for _, l := range list.Leaders {
		if l.ID == id {
			return l.NotesAt
		}
	}
	t.Fatalf("%s not listed: %s", id, rec.Body)
	return nil
}

// A notes save that says which notes_at it started from is refused with what is stored when that is no longer it,
// and changes nothing; the current value is accepted as listed and as a client writes it again at millisecond
// precision; without notes_at a save is not checked.
func TestDirectionNotesSaveChecksNotesAt(t *testing.T) {
	s, _ := notesEnv(t)
	ctx := context.Background()
	// Two editors open the direction with no notes (null); the first save wins, the second is refused.
	first := putNotesAnswer(t, s, `{"notes":"Readings: the store build number.","notes_at":null}`, http.StatusOK)
	if first.NotesAt == nil || first.NotesAt.Nanosecond()%int(time.Millisecond) != 0 {
		t.Fatalf("first save's time: %+v", first)
	}
	stale := putNotesAnswer(t, s, `{"notes":"Never deploy on Fridays.","notes_at":null}`, http.StatusConflict)
	if stale.Code != codeNotesChanged || stale.Error != i18n.T(i18n.Default, "assistant-direction_notes.changed") ||
		stale.Notes != "Readings: the store build number." || stale.NotesAt == nil || !stale.NotesAt.Equal(*first.NotesAt) {
		t.Fatalf("stale null: %+v", stale)
	}
	if d := s.directionOf("conv_lead"); d.Notes != "Readings: the store build number." || !d.NotesAt.Equal(*first.NotesAt) {
		t.Fatalf("a refused save changed the notes: %+v", d)
	}
	// The value as listed, and the same instant written again at millisecond precision in another zone.
	second := putNotesAnswer(t, s, `{"notes":"Readings: v2.","notes_at":"`+listedNotesAt(t, s)+`"}`, http.StatusOK)
	again := second.NotesAt.In(time.FixedZone("", 8*3600)).Format("2006-01-02T15:04:05.000Z07:00")
	third := putNotesAnswer(t, s, `{"notes":"Readings: v3.","notes_at":"`+again+`"}`, http.StatusOK)
	if !third.NotesAt.After(*second.NotesAt) || !second.NotesAt.After(*first.NotesAt) {
		t.Fatalf("times: %v %v %v", first.NotesAt, second.NotesAt, third.NotesAt)
	}
	// An editor that started from the first value is refused with the third.
	old := putNotesAnswer(t, s, `{"notes":"Readings: old.","notes_at":"`+first.NotesAt.Format(time.RFC3339Nano)+`"}`, http.StatusConflict)
	if old.Notes != "Readings: v3." || !old.NotesAt.Equal(*third.NotesAt) {
		t.Fatalf("stale time: %+v", old)
	}
	// Without notes_at: saved as before. A notes_at that is not a time: a bad request.
	if got := putNotesAnswer(t, s, `{"notes":"Readings: v4."}`, http.StatusOK); got.Notes != "Readings: v4." {
		t.Fatalf("unchecked: %+v", got)
	}
	putNotesAnswer(t, s, `{"notes":"x","notes_at":"yesterday"}`, http.StatusBadRequest)
	// A record from before (a finer time) still matches the value a client wrote again at millisecond precision.
	fine := time.Date(2026, 11, 20, 1, 2, 3, 456789123, time.UTC)
	s.directions.mu.Lock()
	s.directions.m["conv_lead"].NotesAt = &fine
	s.directions.mu.Unlock()
	putNotesAnswer(t, s, `{"notes":"Readings: v5.","notes_at":"2026-11-20T01:02:03.456Z"}`, http.StatusOK)
	// Cleared: an editor that still holds a time is refused with no notes (null).
	cleared := putNotesAnswer(t, s, `{"notes":""}`, http.StatusOK)
	if cleared.NotesAt != nil {
		t.Fatalf("cleared: %+v", cleared)
	}
	if got := putNotesAnswer(t, s, `{"notes":"x","notes_at":"`+again+`"}`, http.StatusConflict); got.Notes != "" || got.NotesAt != nil {
		t.Fatalf("stale after a clear: %+v", got)
	}
	// Two saves in the same millisecond never share a time.
	at := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	if next := nextNotesAt(&at, at.Add(300*time.Microsecond)); !next.Equal(at.Add(time.Millisecond)) {
		t.Fatalf("next = %v", next)
	}
	if _, _, err := s.setDirectionNotes(ctx, "nope", "x", nil); err != errNoDirection {
		t.Fatalf("unknown: %v", err)
	}
}

// Notes saved while the lead works leave its policy as its process runs it, so the owner's next message brings the
// node no new role to refuse; the first send that finds the lead idle carries them, also after a manager restart.
func TestDirectionNotesWaitForAnIdleLead(t *testing.T) {
	s, f := notesEnv(t)
	ctx := context.Background()
	lead := func(c proto.Session) {
		c.SessionID, c.MachineID, c.Assistant = "conv_lead", "server", true
		f.setSessions("server", c)
	}
	policyOf := func(s *Service) string {
		t.Helper()
		return dispatchedPolicy(t, s, "conv_lead")
	}
	lead(proto.Session{Status: proto.StatusIdle})
	if _, _, err := s.setDirectionNotes(ctx, "conv_lead", "Readings: v1.", nil); err != nil {
		t.Fatal(err)
	}
	v1 := policyOf(s)
	if !strings.HasPrefix(v1, mustSupervisionRole(t, supervisionrole.RoleLeader())+"\n\n") || !strings.Contains(v1, "Readings: v1.") {
		t.Fatalf("idle: %q", v1)
	}
	if _, _, err := s.setDirectionNotes(ctx, "conv_lead", "Readings: v2.", nil); err != nil {
		t.Fatal(err)
	}
	busy := []proto.Session{{Status: proto.StatusWorking}, {Status: proto.StatusBlocked}, {Status: proto.StatusIdle, Starting: true},
		{Status: proto.StatusIdle, PendingInteractions: 1}, {Status: proto.StatusIdle, Compaction: &proto.Compaction{State: proto.CompactionRunning}},
		{Status: proto.StatusIdle, Background: []proto.BackgroundTask{{ID: "bg1"}}}}
	for _, c := range busy {
		lead(c)
		if got := policyOf(s); got != v1 {
			t.Fatalf("busy %+v: the policy changed to %q", c, got)
		}
	}
	// A manager restart reads the block in effect from the direction's record.
	lead(proto.Session{Status: proto.StatusWorking})
	s2 := newServiceOn(t, f, s.store())
	if got := policyOf(s2); got != v1 {
		t.Fatalf("after a restart: %q", got)
	}
	lead(proto.Session{Status: proto.StatusIdle})
	v2 := policyOf(s2)
	if !strings.Contains(v2, "Readings: v2.") || strings.Contains(v2, "Readings: v1.") {
		t.Fatalf("first idle send: %q", v2)
	}
	lead(proto.Session{Status: proto.StatusWorking})
	if got := policyOf(s2); got != v2 {
		t.Fatalf("busy after the idle send: %q", got)
	}
	// Another leader's policy, and a leader without notes, are untouched.
	if other := mustSupervisionInstructions(t, s2, ctx, "server", "conv_other"); other != mustSupervisionRole(t, supervisionrole.RoleLeader()) {
		t.Fatal("another leader's policy changed")
	}
}

// leadID is the i-th of six 36-character conversation ids, as the launchers make them: the approval card shows only
// the first three of a list and the start of the fourth.
func leadID(i int) string {
	return fmt.Sprintf("%08d-0000-4000-8000-%012d", i+1, i+1)
}

// An assistant.leaders card raised against one list is refused as stale when the owner changed the list before
// approving, also where the card's clipped text reads the same; the list stays as the owner left it.
func TestLeadersApprovalComparesTheWholeList(t *testing.T) {
	ctx := context.Background()
	f := newFakeHub().withMachine()
	s := newTestService(t, f)
	ids := make([]string, 6)
	var listed []proto.Session
	for i := range ids {
		ids[i] = leadID(i)
		listed = append(listed, proto.Session{SessionID: ids[i], MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	}
	f.setSessions("server", listed...)
	setSettings(t, s, func(v *Settings) { v.Main, v.Leaders = "main-1", slices.Clone(ids[:4]) })

	// propose has the assistant set value and, once the card is up, runs owner (the owner's own tap) and approves.
	propose := func(value []string, owner func()) string {
		t.Helper()
		raw, _ := json.Marshal(map[string]any{"action": "set", "key": "assistant.leaders", "value": value})
		out := make(chan string, 1)
		go func() {
			r := s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: raw, MachineID: "server", SessionID: "main-1"})
			out <- r.Content[0].Text
		}()
		q, ok := awaitCard(s)
		if !ok {
			t.Fatal("no card")
		}
		if owner != nil {
			owner()
			// The card's text alone does not show the change: it is what the same proposal reads now.
			v, _ := json.Marshal(value)
			if fresh, err := s.setLeaders(ctx, v); err != nil || fresh.apply == nil || fresh.card(s.lang(ctx)) != q.Summary {
				t.Fatalf("the card text differs, so this does not test the whole-list comparison: %v", err)
			}
		}
		allow := true
		answer, _ := json.Marshal(proto.Resolution{Allow: &allow})
		if _, err := s.Action(ctx, Action{Op: "answer", InteractionID: q.ID, Answer: answer}); err != nil {
			t.Fatal(err)
		}
		select {
		case text := <-out:
			return text
		case <-time.After(5 * time.Second):
			t.Fatal("the tool did not return")
			return ""
		}
	}
	leaders := func() []string { return s.settings(ctx).Leaders }

	// Four leaders; the owner pins a fifth while the card waits.
	out := propose(append(slices.Clone(ids[:4]), ids[5]), func() {
		if _, err := s.makeDirection(ctx, ids[4], "", "", ""); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(out, "changed after this card was prepared") || !slices.Equal(leaders(), ids[:5]) {
		t.Fatalf("pinned meanwhile: %s %v", out, leaders())
	}
	// Five leaders; the owner unpins the fifth while the card waits.
	out = propose(slices.Clone(ids), func() {
		if ended, err := s.endDirection(ctx, ids[4]); err != nil || !ended {
			t.Fatalf("end: %v %v", ended, err)
		}
	})
	if !strings.Contains(out, "changed after this card was prepared") || !slices.Equal(leaders(), ids[:4]) {
		t.Fatalf("unpinned meanwhile: %s %v", out, leaders())
	}
	// Nothing changed meanwhile: the approval applies.
	want := append(slices.Clone(ids[:4]), ids[5])
	if out = propose(want, nil); !strings.HasPrefix(out, "assistant.leaders is") || !slices.Equal(leaders(), want) {
		t.Fatalf("approved: %s %v", out, leaders())
	}
}

// listLeads lists rows as Persona conversations on the assistant's computer (and nothing else there).
func listLeads(f *fakeHub, rows ...proto.Session) {
	for i := range rows {
		rows[i].MachineID, rows[i].Assistant = "server", true
	}
	f.setSessions("server", rows...)
}

// ownerParams are what the hub asks for before it sends the owner's message params (nil: "go on") to conversation id
// (assistant.send_params, as hub/send.go does): the role policy they carry, or their refusal. How the send went is the
// test's to tell (hubSent).
func ownerParams(t *testing.T, s *Service, id string, params map[string]any) (string, error) {
	t.Helper()
	b := &BusHub{svc: s, helloed: make(chan struct{})}
	close(b.helloed) // the hub's hello is answered: the request does not wait for it
	if params == nil {
		params = map[string]any{"text": "go on"}
	}
	raw, err := json.Marshal(assistantwire.SendParams{MachineID: "server", SessionID: id, Params: params, Human: true})
	if err != nil {
		t.Fatal(err)
	}
	out, err := b.request(context.Background(), assistantwire.MethodSendParams, raw)
	if err != nil {
		return "", err
	}
	sent, _ := out.(map[string]any)["params"].(map[string]any)
	policy, _ := sent["supervision_instructions"].(string)
	return policy, nil
}

// ownerPolicy is the role policy the owner's next message to conversation id carries; the hub has yet to tell how it
// went (hubSent).
func ownerPolicy(t *testing.T, s *Service, id string) string {
	t.Helper()
	policy, err := ownerParams(t, s, id, nil)
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

// hubSent is the hub's notice of how the owner's message to conversation id went (session.assistant_sent), naming no
// message: one sent without a client_msg_id, or an older hub's notice.
func hubSent(s *Service, id, outcome string) {
	hubSentID(s, id, "", outcome)
}

// hubSentID is the hub's notice of how the owner's message msg (its client_msg_id) to conversation id went.
func hubSentID(s *Service, id, msg, outcome string) {
	s.ConversationSent(context.Background(), ConversationSent{MachineID: "server", SessionID: id, Outcome: outcome, ClientMsgID: msg})
}

// ownerMessage is the role policy the owner's message msg (its client_msg_id) to conversation id is sent with; the hub
// has yet to tell how it went (hubSentID).
func ownerMessage(t *testing.T, s *Service, id, msg string) string {
	t.Helper()
	policy, err := ownerParams(t, s, id, map[string]any{"text": "go on", "client_msg_id": msg})
	if err != nil {
		t.Fatal(err)
	}
	return policy
}

// within waits for c, failing the test after a bound instead of hanging when a regression never sends.
func within[T any](t *testing.T, c <-chan T, what string) T {
	t.Helper()
	select {
	case v := <-c:
		return v
	case <-time.After(30 * time.Second):
		t.Fatalf("%s never came", what)
		var zero T
		return zero
	}
}

// dispatchedPolicy is the role policy the owner's next message to conversation id is sent with, which the node takes.
func dispatchedPolicy(t *testing.T, s *Service, id string) string {
	t.Helper()
	policy := ownerPolicy(t, s, id)
	hubSent(s, id, assistantwire.ConversationDelivered)
	return policy
}

// managerSend is the manager's own message to conversation id (call), with the role policy it carried and the node's
// answer.
func managerSend(s *Service, id string) (string, error) {
	p := map[string]any{"session_id": id, "text": "a supervision message"}
	err := s.call(context.Background(), "server", DeviceSessionSend, p, nil)
	policy, _ := p["supervision_instructions"].(string)
	return policy, err
}

// nodePolicy is the role policy the node's own next message to conversation id runs on (assistant.model), which is
// the block the lead is taken to run: asking moves nothing.
func nodePolicy(t *testing.T, s *Service, id string) string {
	t.Helper()
	out, ok, err := s.NodeAnswer(context.Background(), "server", "assistant.model", json.RawMessage(`{"session_id":"`+id+`"}`))
	if !ok || err != nil {
		t.Fatalf("assistant.model: %v %v", ok, err)
	}
	m, _ := out.(map[string]any)
	policy, _ := m["supervision_instructions"].(string)
	if policy == "" {
		t.Fatalf("assistant.model gave no role policy: %v", out)
	}
	return policy
}

// nodeRefuses is the node's answer to a send it refuses before it reaches the conversation, as it refuses a new role
// for a process at work.
func nodeRefuses(string, json.RawMessage) (any, error) {
	return nil, &rpc.Error{Code: "busy", Message: "busy", Data: json.RawMessage(`{"delivered":"none"}`)}
}

func nodeTakes(string, json.RawMessage) (any, error) { return map[string]any{"ok": true}, nil }

// A direction ended and made again does not bring back the notes block or role it had before it ended: once unpinned,
// an idle send restarts the conversation as a side supervisor. Re-pinning it while that side process works holds the
// leader role until an idle send; a refused idle send rolls the role back instead of making later messages busy too.
func TestDirectionNotesInEffectFollowTheConversationAcrossItsEnd(t *testing.T) {
	s, f := notesEnv(t)
	ctx := context.Background()
	leader := mustSupervisionRole(t, supervisionrole.RoleLeader())
	side := mustSupervisionRole(t, supervisionrole.RoleSide())
	todo, err := s.createTodo(ctx, "Ship the build", "", "conv_lead", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv_lead", []string{"Shipped"}, 30)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	if _, _, err := s.setDirectionNotes(ctx, "conv_lead", "Readings: the store build number.", nil); err != nil {
		t.Fatal(err)
	}
	if got := dispatchedPolicy(t, s, "conv_lead"); !strings.Contains(got, "Readings: the store build number.") {
		t.Fatalf("lead: %q", got)
	}
	if ended, err := s.endDirection(ctx, "conv_lead"); err != nil || !ended {
		t.Fatalf("end: %v %v", ended, err)
	}
	if got := dispatchedPolicy(t, s, "conv_lead"); got != side {
		t.Fatalf("unpinned leader with remaining work did not become a side supervisor: %q", got)
	}
	if _, _, err := s.setDirectionNotes(ctx, "conv_lead", "", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.makeDirection(ctx, "conv_lead", "", "", ""); err != nil {
		t.Fatal(err)
	}
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if got := dispatchedPolicy(t, s, "conv_lead"); got != side {
		t.Fatalf("pinned again while it works changed the running process's policy: %q", got)
	}
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	if got := ownerPolicy(t, s, "conv_lead"); got != leader {
		t.Fatalf("the idle send did not take the leader role: %q", got)
	}
	hubSent(s, "conv_lead", assistantwire.ConversationFailed)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if got := dispatchedPolicy(t, s, "conv_lead"); got != side {
		t.Fatalf("a refused role change did not roll back: %q", got)
	}
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	if got := dispatchedPolicy(t, s, "conv_lead"); got != leader {
		t.Fatalf("the next idle send did not retry the leader role: %q", got)
	}
}

func TestDirectionEndWaitsForAnIdleRoleChange(t *testing.T) {
	s, f := notesEnv(t)
	ctx := context.Background()
	leader := mustSupervisionRole(t, supervisionrole.RoleLeader())
	todo, err := s.createTodo(ctx, "Ship the build", "", "conv_lead", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv_lead", []string{"Shipped"}, 30)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	if _, _, err := s.setDirectionNotes(ctx, "conv_lead", "Readings: the store build number.", nil); err != nil {
		t.Fatal(err)
	}
	withNotes := dispatchedPolicy(t, s, "conv_lead")
	if !strings.HasPrefix(withNotes, leader) {
		t.Fatalf("lead: %q", withNotes)
	}
	if ended, err := s.endDirection(ctx, "conv_lead"); err != nil || !ended {
		t.Fatalf("end: %v %v", ended, err)
	}
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if got := dispatchedPolicy(t, s, "conv_lead"); got != withNotes {
		t.Fatalf("ending a working direction changed its policy: %q", got)
	}
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	if got := dispatchedPolicy(t, s, "conv_lead"); got != mustSupervisionRole(t, supervisionrole.RoleSide()) {
		t.Fatalf("the idle send did not become a side supervisor: %q", got)
	}
}

func TestDirectionFirstPinWhileSideWorksKeepsItsRole(t *testing.T) {
	s, f := notesEnv(t)
	ctx := context.Background()
	side := mustSupervisionRole(t, supervisionrole.RoleSide())
	todo, err := s.createTodo(ctx, "Keep the side work", "", "conv_side", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv_side", []string{"Kept"}, 30)
	listLeads(f, proto.Session{SessionID: "conv_side", Status: proto.StatusIdle})
	if got := dispatchedPolicy(t, s, "conv_side"); got != side {
		t.Fatalf("side before its first pin: %q", got)
	}
	// The side send has no direction record; its accepted supplement baseline moves onto the new direction at the pin.
	listLeads(f, proto.Session{SessionID: "conv_side", Status: proto.StatusWorking})
	if _, err := s.makeDirection(ctx, "conv_side", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if got := dispatchedPolicy(t, s, "conv_side"); got != side {
		t.Fatalf("the first pin changed a working legacy side's policy: %q", got)
	}
	listLeads(f, proto.Session{SessionID: "conv_side", Status: proto.StatusIdle})
	if got := dispatchedPolicy(t, s, "conv_side"); got != mustSupervisionRole(t, supervisionrole.RoleLeader()) {
		t.Fatalf("the idle send did not take the leader role: %q", got)
	}
}

type directionRolePublicationStore struct {
	Store
	check func(Settings)
}

func (s *directionRolePublicationStore) PutSetting(ctx context.Context, key string, v any) error {
	if key == settingsKey {
		s.check(v.(Settings))
	}
	return s.Store.PutSetting(ctx, key, v)
}

func TestDirectionRoleBaselinePrecedesLeaderPublication(t *testing.T) {
	s, f := notesEnv(t)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Keep the side work", "", "conv_side", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv_side", []string{"Kept"}, 30)
	listLeads(f, proto.Session{SessionID: "conv_side", Status: proto.StatusIdle})
	if got := dispatchedPolicy(t, s, "conv_side"); got != mustSupervisionRole(t, supervisionrole.RoleSide()) {
		t.Fatalf("side before its first pin: %q", got)
	}
	listLeads(f, proto.Session{SessionID: "conv_side", Status: proto.StatusWorking})
	base := s.store()
	checked := false
	wrapped := &directionRolePublicationStore{Store: base, check: func(cfg Settings) {
		if !slices.Contains(cfg.Leaders, "conv_side") {
			return
		}
		checked = true
		s.directions.mu.Lock()
		d := s.directions.m["conv_side"]
		role := ""
		if d != nil {
			role = d.RoleInEffect
		}
		s.directions.mu.Unlock()
		if role != "side" {
			t.Errorf("new leader membership reached storage before its old-role baseline: %q", role)
		}
	}}
	storeFn := func() Store { return wrapped }
	s.storeFn.Store(&storeFn)
	if _, err := s.makeDirection(ctx, "conv_side", "", "", ""); err != nil {
		t.Fatal(err)
	}
	if !checked {
		t.Fatal("the leader settings write was not observed")
	}
}

func TestDirectionFirstEndWhileLegacyLeaderWorksKeepsItsRole(t *testing.T) {
	s, f := notesEnv(t)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Keep the legacy lead", "", "conv_lead", "")
	if err != nil {
		t.Fatal(err)
	}
	s.superviseTodo(ctx, todo.ID, "conv_lead", []string{"Kept"}, 30)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if ended, err := s.endDirection(ctx, "conv_lead"); err != nil || !ended {
		t.Fatalf("end: %v %v", ended, err)
	}
	// The pre-upgrade record and supplement have no role baseline. Assuming a compact leader policy here would invent
	// a fingerprint the working legacy process never received, so it keeps the complete policy until an idle send.
	if got := dispatchedPolicy(t, s, "conv_lead"); got != prompts.RenderSupervisionInstructions() {
		t.Fatalf("the first unpin changed a working legacy leader's full policy: %q", got)
	}
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	if got := dispatchedPolicy(t, s, "conv_lead"); got != mustSupervisionRole(t, supervisionrole.RoleSide()) {
		t.Fatalf("the idle send did not select the former leader's side role: %q", got)
	}
}

// A lead with no block in effect recorded yet (from before #11392, or never sent to since) keeps what it runs on when
// the owner's first save or first clear comes while it works; the first idle send then carries the notes as they are.
func TestDirectionNotesFirstSaveWhileTheLeadWorks(t *testing.T) {
	s, f := notesEnv(t)
	ctx := context.Background()
	plain := mustSupervisionRole(t, supervisionrole.RoleLeader())
	// conv_other has notes from before any block in effect was recorded: every send so far carried them.
	at := time.Now().UTC()
	s.directions.mu.Lock()
	s.directions.m["conv_other"].Notes, s.directions.m["conv_other"].NotesAt = "Infra rule: back up first.", &at
	// This notes test starts with current role-filtered processes; the separate upgrade test covers an absent baseline.
	s.directions.m["conv_lead"].RoleInEffect = "leader"
	s.directions.m["conv_other"].RoleInEffect = "leader"
	s.directions.mu.Unlock()
	withOld := plain + "\n\n" + prompts.RenderDirectionNotes(s.directionPrompt(ctx, "conv_other"))
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking},
		proto.Session{SessionID: "conv_other", Status: proto.StatusWorking})
	if _, _, err := s.setDirectionNotes(ctx, "conv_lead", "Readings: v1.", nil); err != nil { // its first notes
		t.Fatal(err)
	}
	if _, _, err := s.setDirectionNotes(ctx, "conv_other", "", nil); err != nil { // its first clear
		t.Fatal(err)
	}
	if got := dispatchedPolicy(t, s, "conv_lead"); got != plain {
		t.Fatalf("first save while it works: %q", strings.TrimPrefix(got, plain))
	}
	if got := dispatchedPolicy(t, s, "conv_other"); got != withOld {
		t.Fatalf("first clear while it works: %q", strings.TrimPrefix(got, plain))
	}
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle},
		proto.Session{SessionID: "conv_other", Status: proto.StatusIdle})
	if got := dispatchedPolicy(t, s, "conv_lead"); !strings.Contains(got, "Readings: v1.") {
		t.Fatalf("idle: %q", got)
	}
	if got := dispatchedPolicy(t, s, "conv_other"); got != plain {
		t.Fatalf("idle after the clear: %q", got)
	}
}

// A notes_at is never issued twice, also across a clear: an editor that opened the notes before the clear is refused
// even when the next save comes in the same millisecond as the one it read, and an older record keeps its time too.
func TestNotesAtIsNeverIssuedTwiceAcrossAClear(t *testing.T) {
	s, _ := notesEnv(t)
	clock := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	s.directions.mu.Lock()
	s.directions.now = func() time.Time { return clock }
	s.directions.mu.Unlock()
	first := putNotesAnswer(t, s, `{"notes":"Readings: A."}`, http.StatusOK)
	putNotesAnswer(t, s, `{"notes":""}`, http.StatusOK)
	second := putNotesAnswer(t, s, `{"notes":"Readings: B."}`, http.StatusOK)
	if !second.NotesAt.After(*first.NotesAt) {
		t.Fatalf("issued again: %v then %v", first.NotesAt, second.NotesAt)
	}
	stale := putNotesAnswer(t, s, `{"notes":"Readings: C.","notes_at":"`+first.NotesAt.Format(time.RFC3339Nano)+`"}`, http.StatusConflict)
	if stale.Notes != "Readings: B." {
		t.Fatalf("stale editor: %+v", stale)
	}
	// An older record has only NotesAt: cleared and saved again at the same instant, it still moves on.
	old := clock.Add(time.Hour)
	s.directions.mu.Lock()
	d := s.directions.m["conv_lead"]
	d.Notes, d.NotesAt, d.NotesLastAt = "Readings: old.", &old, nil
	s.directions.now = func() time.Time { return old }
	s.directions.mu.Unlock()
	putNotesAnswer(t, s, `{"notes":""}`, http.StatusOK)
	if again := putNotesAnswer(t, s, `{"notes":"Readings: D."}`, http.StatusOK); !again.NotesAt.After(old) {
		t.Fatalf("older record issued again: %v", again.NotesAt)
	}
}

// A notes block a send carried counts as given only once the node took that send. When the node refuses it (the lead
// had just started a turn its session row did not show yet), the block before it is put back, so the next messages of
// that turn keep the policy the lead runs on. The owner's message is settled by the hub's notice of how it sent it,
// the manager's own send by the node's answer; a send the node took keeps the new block.
func TestDirectionNotesCountAsGivenOnlyOnceTheNodeTookTheSend(t *testing.T) {
	s, f := notesEnv(t)
	ctx := context.Background()
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	if _, _, err := s.setDirectionNotes(ctx, "conv_lead", "Readings: v1.", nil); err != nil {
		t.Fatal(err)
	}
	v1 := dispatchedPolicy(t, s, "conv_lead")
	// The owner saves v2 and sends; the row still reads idle, but the node already sees a turn and refuses it.
	if _, _, err := s.setDirectionNotes(ctx, "conv_lead", "Readings: v2.", nil); err != nil {
		t.Fatal(err)
	}
	if got := ownerPolicy(t, s, "conv_lead"); !strings.Contains(got, "Readings: v2.") {
		t.Fatalf("idle send: %q", got)
	}
	hubSent(s, "conv_lead", assistantwire.ConversationFailed)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if got := nodePolicy(t, s, "conv_lead"); got != v1 {
		t.Fatalf("the rest of the turn after a refused message: %q", got)
	}
	// The manager's own send, refused the same way.
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	f.answerNode(DeviceSessionSend, nodeRefuses)
	if _, err := managerSend(s, "conv_lead"); err == nil {
		t.Fatal("the refused send went")
	}
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if got := nodePolicy(t, s, "conv_lead"); got != v1 {
		t.Fatalf("the rest of the turn after a refused manager send: %q", got)
	}
	// Taken: the new block stays.
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	f.answerNode(DeviceSessionSend, nodeTakes)
	if _, err := managerSend(s, "conv_lead"); err != nil {
		t.Fatal(err)
	}
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if got := nodePolicy(t, s, "conv_lead"); !strings.Contains(got, "Readings: v2.") {
		t.Fatalf("after a taken send: %q", got)
	}
}

// A notes block is settled by the outcome of the send that carried it, not by whichever outcome for its conversation
// comes first (#11392). The manager's own send, carrying the block the lead runs on, is in flight while the owner
// saves new notes and sends a message that moves the block; the two outcomes then come in either order, and the lead
// is taken to run the block of the send the node took.
func TestDirectionNotesSettledByTheSendThatCarriedThem(t *testing.T) {
	for _, tc := range []struct {
		name         string
		managerTaken bool   // the earlier send: the manager's
		owner        string // the later: the owner's message, as the hub reports it
		want         string // the notes the lead runs after both
	}{
		{"the earlier send taken then the later refused", true, assistantwire.ConversationFailed, "Readings: v1."},
		{"the earlier send refused then the later taken", false, assistantwire.ConversationDelivered, "Readings: v2."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := notesEnv(t)
			ctx := context.Background()
			listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
			if _, _, err := s.setDirectionNotes(ctx, "conv_lead", "Readings: v1.", nil); err != nil {
				t.Fatal(err)
			}
			v1 := dispatchedPolicy(t, s, "conv_lead")
			// The manager's send is in flight: its policy step is done and the node has it.
			arrived, release := make(chan struct{}, 1), make(chan struct{})
			f.mu.Lock()
			f.beforeSend = func() { arrived <- struct{}{}; <-release }
			f.mu.Unlock()
			if tc.managerTaken {
				f.answerNode(DeviceSessionSend, nodeTakes)
			} else {
				f.answerNode(DeviceSessionSend, nodeRefuses)
			}
			type sent struct {
				policy string
				err    error
			}
			done := make(chan sent, 1)
			go func() {
				policy, err := managerSend(s, "conv_lead")
				done <- sent{policy, err}
			}()
			within(t, arrived, "the manager's send")
			if _, _, err := s.setDirectionNotes(ctx, "conv_lead", "Readings: v2.", nil); err != nil {
				t.Fatal(err)
			}
			if got := ownerPolicy(t, s, "conv_lead"); !strings.Contains(got, "Readings: v2.") {
				t.Fatalf("the owner's message: %q", got)
			}
			close(release)
			if m := within(t, done, "the node's answer"); m.policy != v1 || (m.err == nil) != tc.managerTaken {
				t.Fatalf("the manager's send: %v, carrying %q", m.err, m.policy)
			}
			hubSent(s, "conv_lead", tc.owner)
			listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
			if got := nodePolicy(t, s, "conv_lead"); !strings.Contains(got, tc.want) {
				t.Fatalf("taken to run %q, want %s", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())), tc.want)
			}
		})
	}
}

// Only a send whose outcome is reported moves the notes block in effect, and each such send reports it (#11392):
// assistant.answer_again through call, and a message whose parameters were made but that was refused before it left
// as not taken; the node's own request for what its message runs on (assistant.model) does not move it. Each time,
// the block the lead is then taken to run is the one its process got.
func TestDirectionNotesMovedOnlyBySendsThatReportTheirOutcome(t *testing.T) {
	ctx := context.Background()
	start := func(t *testing.T, row proto.Session) (*Service, *fakeHub, string) {
		t.Helper()
		s, f := notesEnv(t)
		row.SessionID, row.Agent = "conv_lead", "claude"
		listLeads(f, row)
		if _, _, err := s.setDirectionNotes(ctx, "conv_lead", "Readings: v1.", nil); err != nil {
			t.Fatal(err)
		}
		return s, f, dispatchedPolicy(t, s, "conv_lead")
	}
	saveV2 := func(t *testing.T, s *Service) {
		t.Helper()
		if _, _, err := s.setDirectionNotes(ctx, "conv_lead", "Readings: v2.", nil); err != nil {
			t.Fatal(err)
		}
	}
	working := func(f *fakeHub) {
		listLeads(f, proto.Session{SessionID: "conv_lead", Agent: "claude", Status: proto.StatusWorking})
	}

	t.Run("answer again", func(t *testing.T) {
		s, f, _ := start(t, proto.Session{Status: proto.StatusDone})
		const ask = "Summarize today's usage."
		if _, err := ownerParams(t, s, "conv_lead", map[string]any{"client_msg_id": "app-user-input", "text": ask}); err != nil {
			t.Fatal(err)
		}
		hubSent(s, "conv_lead", assistantwire.ConversationDelivered)
		// The turn ends without a reply, and the owner saves v2 meanwhile: the manager asks for the reply again.
		saveV2(t, s)
		f.setItems(
			proto.Item{ID: "u1#0", Seq: 1, Role: "user", Kind: proto.KindText, Text: ask, ClientMsgID: "app-user-input"},
			proto.Item{ID: "t2#0", Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolUseID: "tool", ToolName: "Bash"},
			proto.Item{ID: "r3#0", Seq: 3, Role: "user", Kind: proto.KindToolResult, ToolUseID: "tool", Text: "24 requests"},
		)
		var again []string
		f.answerNode(assistantwire.DeviceAssistantAnswerAgain, func(_ string, raw json.RawMessage) (any, error) {
			var p struct {
				Policy string `json:"supervision_instructions"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			again = append(again, p.Policy)
			return map[string]any{}, nil
		})
		s.assistantTurnSettled(ctx, "server", "conv_lead")
		if len(again) != 1 {
			t.Fatalf("answer_again sends: %d", len(again))
		}
		// The owner's next message fails before it reaches the conversation (its computer is briefly offline).
		ownerPolicy(t, s, "conv_lead")
		hubSent(s, "conv_lead", assistantwire.ConversationFailed)
		working(f)
		if got := nodePolicy(t, s, "conv_lead"); got != again[0] {
			t.Fatalf("taken to run %q, but answer_again gave it %q", got, again[0])
		}
	})

	t.Run("the owner's message refused before it left", func(t *testing.T) {
		s, f, v1 := start(t, proto.Session{Status: proto.StatusIdle})
		saveV2(t, s)
		if _, err := ownerParams(t, s, "conv_lead", map[string]any{"text": "go on", "model": "not a model id!"}); err == nil {
			t.Fatal("a bad model was taken")
		}
		working(f)
		if got := nodePolicy(t, s, "conv_lead"); got != v1 {
			t.Fatalf("taken to run %q after a refused message", got)
		}
	})

	t.Run("the manager's send refused before it left", func(t *testing.T) {
		s, f, v1 := start(t, proto.Session{Status: proto.StatusIdle})
		saveV2(t, s)
		f.mu.Lock()
		f.sessions["mac"], f.catalogNodes = nil, true // another of the owner's computers, the only one the provider serves
		f.mu.Unlock()
		addProvider(t, s, `{"name":"box","base_url":"https://gw.example.com","model":"m1","api_key":"sk-test-box-000000","share":"selected","scope_machines":["mac"]}`)
		p := map[string]any{"session_id": "conv_lead", "launcher": "box", "text": "a supervision message"}
		if err := s.call(ctx, "server", DeviceSessionSend, p, nil); codeOf(err) != proto.CodeLauncherOutOfScope {
			t.Fatalf("a send on a launcher out of scope: %v", err)
		}
		working(f)
		if got := nodePolicy(t, s, "conv_lead"); got != v1 {
			t.Fatalf("taken to run %q after a refused send", got)
		}
	})

	t.Run("the node's own message", func(t *testing.T) {
		s, f, v1 := start(t, proto.Session{Status: proto.StatusIdle})
		saveV2(t, s)
		own := nodePolicy(t, s, "conv_lead") // the node asks what its own message runs on, and sends it
		if own != v1 {
			t.Fatalf("the node's own message moved the block: %q", own)
		}
		// The owner's next message moves it, and fails before it reaches the conversation.
		if got := ownerPolicy(t, s, "conv_lead"); !strings.Contains(got, "Readings: v2.") {
			t.Fatalf("the owner's message: %q", got)
		}
		hubSent(s, "conv_lead", assistantwire.ConversationFailed)
		working(f)
		if got := nodePolicy(t, s, "conv_lead"); got != own {
			t.Fatalf("taken to run %q, but the node's own message gave it %q", got, own)
		}
	})
}

// saveLeadNotes saves notes for the lead, as the owner's editor does.
func saveLeadNotes(t *testing.T, s *Service, notes string) {
	t.Helper()
	if _, _, err := s.setDirectionNotes(context.Background(), "conv_lead", notes, nil); err != nil {
		t.Fatal(err)
	}
}

// setClock puts the directions on a clock the test moves.
func setClock(s *Service, at time.Time) {
	s.directions.mu.Lock()
	s.directions.now = func() time.Time { return at }
	s.directions.mu.Unlock()
}

// The hub's notice names the owner's message it is about, and settles that message only (#11392): a message whose
// notice never comes (the hub answered a retry from its record, or the notice was lost) takes no one else's. Here the
// next message comes a minute later, moves the block and is refused: its own notice puts the block back.
func TestDirectionNotesOwnerNoticeSettlesItsOwnMessage(t *testing.T) {
	s, f := notesEnv(t)
	at := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	setClock(s, at)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	saveLeadNotes(t, s, "Readings: v1.")
	v1 := ownerMessage(t, s, "conv_lead", "m-1")
	hubSentID(s, "conv_lead", "m-1", assistantwire.ConversationDelivered)
	ownerMessage(t, s, "conv_lead", "m-2") // its notice never comes
	setClock(s, at.Add(time.Minute))
	saveLeadNotes(t, s, "Readings: v2.")
	if got := ownerMessage(t, s, "conv_lead", "m-3"); !strings.Contains(got, "Readings: v2.") {
		t.Fatalf("the next message: %q", got)
	}
	hubSentID(s, "conv_lead", "m-3", assistantwire.ConversationFailed)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if got := nodePolicy(t, s, "conv_lead"); got != v1 {
		t.Fatalf("taken to run %q after its own refusal", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
	// The rest of the turn keeps the policy the lead runs on.
	if got := ownerMessage(t, s, "conv_lead", "m-4"); got != v1 {
		t.Fatalf("a later message of the turn: %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
	hubSentID(s, "conv_lead", "m-4", assistantwire.ConversationDelivered)
}

// A second request for parameters of a message the manager already gave them for is the hub's retry, which the hub may
// answer from its record without sending it or telling how it went: it moves nothing and waits for no notice.
func TestDirectionNotesReplayedMessageMovesNothing(t *testing.T) {
	s, f := notesEnv(t)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	saveLeadNotes(t, s, "Readings: v1.")
	v1 := ownerMessage(t, s, "conv_lead", "m-1")
	hubSentID(s, "conv_lead", "m-1", assistantwire.ConversationDelivered)
	saveLeadNotes(t, s, "Readings: v2.")
	if got := ownerMessage(t, s, "conv_lead", "m-1"); got != v1 {
		t.Fatalf("the replay moved the block: %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
	// The next message moves it, and is refused: the block before it is the one the lead runs.
	if got := ownerMessage(t, s, "conv_lead", "m-2"); !strings.Contains(got, "Readings: v2.") {
		t.Fatalf("the next message: %q", got)
	}
	hubSentID(s, "conv_lead", "m-2", assistantwire.ConversationFailed)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if got := nodePolicy(t, s, "conv_lead"); got != v1 {
		t.Fatalf("taken to run %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
}

// An older hub names no message in its notice: the oldest message still waiting for one is settled, as before.
func TestDirectionNotesOlderHubNoticeWithoutAnID(t *testing.T) {
	s, f := notesEnv(t)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	saveLeadNotes(t, s, "Readings: v1.")
	v1 := ownerMessage(t, s, "conv_lead", "m-1")
	hubSent(s, "conv_lead", assistantwire.ConversationDelivered)
	saveLeadNotes(t, s, "Readings: v2.")
	if got := ownerMessage(t, s, "conv_lead", "m-2"); !strings.Contains(got, "Readings: v2.") {
		t.Fatalf("the next message: %q", got)
	}
	hubSent(s, "conv_lead", assistantwire.ConversationFailed)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if got := nodePolicy(t, s, "conv_lead"); got != v1 {
		t.Fatalf("taken to run %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
}

// The manager's own send that provably never reached the computer is not taken, and the block it moved is put back
// (#11392): a computer offline or not one of the hub's, a call cancelled or not written before it left, the bus down or
// suspended, a conversation that is gone, and a refusal the devices layer answers before the computer is reached,
// also in the forms the bus gives them back (busError). A send that may have gone in keeps its block, as before.
func TestDirectionNotesSendThatNeverReachedTheComputerIsNotTaken(t *testing.T) {
	fromBus := func(code string) error { return busError(&rpc.Error{Code: code, Message: code}) }
	for _, tc := range []struct {
		name  string
		err   error
		taken bool
	}{
		{"offline", fmt.Errorf("server: %w", apierr.ErrOffline), false},
		{"offline, from the bus", fromBus(CodeMachineOffline), false},
		{"not one of the hub's", fmt.Errorf("%w %q", ErrNoDevice, "server"), false},
		{"not one of the hub's, from the bus", fromBus(CodeNoDevice), false},
		{"cancelled before it left", rpc.NotSent(context.Canceled), false},
		{"its time ran out before it left", rpc.NotSent(context.DeadlineExceeded), false},
		{"not written behind a long write", fmt.Errorf("%w (%w)", context.DeadlineExceeded, rpc.ErrNotWritten), false},
		{"the bus down", bus.ErrOffline, false},
		{"the bus suspended", rpc.ErrSuspended, false},
		{"node_outdated from the devices layer", &rpc.Error{Code: proto.CodeNodeOutdated, Message: "update Persona on this computer"}, false},
		{"the conversation is gone", errors.New("conversation conv_lead is gone"), false},
		// It may have gone in: the block stays.
		{"cancelled after it was written", context.Canceled, true},
		{"its time ran out with the request out", context.DeadlineExceeded, true},
		{"the link dropped while it went", rpc.ErrDropped, true},
		{"the hub's deadline with the node on it", &rpc.Error{Code: devicewire.CodeDeadline, Message: "deadline"}, true},
		{"the node's link closed while it went, from the bus", fromBus(CodeNodeClosed), true},
		{"the node may have typed it", &rpc.Error{Code: "send_failed", Message: "send failed", Data: json.RawMessage(`{"delivered":"unknown"}`)}, true},
		{"a node error without a code", &rpc.Error{Message: "the pane closed"}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := notesEnv(t)
			listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
			saveLeadNotes(t, s, "Readings: v1.")
			v1 := dispatchedPolicy(t, s, "conv_lead")
			saveLeadNotes(t, s, "Readings: v2.")
			f.answerNode(DeviceSessionSend, func(string, json.RawMessage) (any, error) { return nil, tc.err })
			v2, err := managerSend(s, "conv_lead")
			if err == nil || !strings.Contains(v2, "Readings: v2.") {
				t.Fatalf("the send: %v, carrying %q", err, v2)
			}
			want := v1
			if tc.taken {
				want = v2
			}
			listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
			if got := nodePolicy(t, s, "conv_lead"); got != want {
				t.Fatalf("taken to run %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
			}
		})
	}
}

// The hub's retry of the owner's message that it really sends (#11392): the first attempt reached nothing (the hub
// stopped before it claimed it, or it typed nothing and its notice was lost), so the one notice that comes is the
// retry's. While the first attempt still waits for a notice, the retry carries the block the first attempt carried and
// the notice settles that move: taken, the lead runs it; refused, the block before it is put back. A retry late in the
// wait waits a whole wait again, as the hub waits for it as long as for any send.
func TestDirectionNotesRetryTheHubSendsSettlesTheBlockItCarried(t *testing.T) {
	at := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	for _, tc := range []struct {
		name    string
		later   time.Duration // the retry comes this long after the first attempt, and its notice two minutes after it
		outcome string        // the retry's notice
		taken   bool
	}{
		{"the first attempt never claimed, the retry taken", 0, assistantwire.ConversationDelivered, true},
		{"the first attempt's notice lost, the retry taken late", 9 * time.Minute, assistantwire.ConversationDelivered, true},
		{"the retry refused", 0, assistantwire.ConversationFailed, false},
		{"the retry refused late", 9 * time.Minute, assistantwire.ConversationFailed, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := notesEnv(t)
			setClock(s, at)
			listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
			saveLeadNotes(t, s, "Readings: v1.")
			v1 := ownerMessage(t, s, "conv_lead", "m-0")
			hubSentID(s, "conv_lead", "m-0", assistantwire.ConversationDelivered)
			saveLeadNotes(t, s, "Readings: v2.")
			v2 := ownerMessage(t, s, "conv_lead", "m-1") // no notice comes for it
			if !strings.Contains(v2, "Readings: v2.") {
				t.Fatalf("the first attempt: %q", strings.TrimPrefix(v2, mustSupervisionRole(t, supervisionrole.RoleLeader())))
			}
			setClock(s, at.Add(tc.later))
			if got := ownerMessage(t, s, "conv_lead", "m-1"); got != v2 {
				t.Fatalf("the retry carries %q, not the first attempt's block", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
			}
			setClock(s, at.Add(tc.later+2*time.Minute))
			hubSentID(s, "conv_lead", "m-1", tc.outcome)
			want := v1
			if tc.taken {
				want = v2
			}
			listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
			if got := nodePolicy(t, s, "conv_lead"); got != want {
				t.Fatalf("taken to run %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
			}
			// The rest of the turn carries what the lead runs.
			if got := ownerMessage(t, s, "conv_lead", "m-2"); got != want {
				t.Fatalf("a later message of the turn: %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
			}
		})
	}
}

// A retry the hub answers from its record sends nothing and brings no notice (the first attempt went, and its notice
// was lost): it moves nothing, though the owner saved new notes meanwhile. The first attempt's block is kept once its
// notice can no longer come, and the new notes wait for the next message that finds the lead idle.
func TestDirectionNotesRetryAnsweredFromTheRecordMovesNothing(t *testing.T) {
	s, f := notesEnv(t)
	at := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	setClock(s, at)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	saveLeadNotes(t, s, "Readings: v1.")
	v1 := ownerMessage(t, s, "conv_lead", "m-0")
	hubSentID(s, "conv_lead", "m-0", assistantwire.ConversationDelivered)
	saveLeadNotes(t, s, "Readings: v2.")
	v2 := ownerMessage(t, s, "conv_lead", "m-1") // it goes; its notice is lost
	saveLeadNotes(t, s, "Readings: v3.")
	setClock(s, at.Add(time.Minute))
	if got := ownerMessage(t, s, "conv_lead", "m-1"); got != v2 {
		t.Fatalf("the retry carries %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	setClock(s, at.Add(notesOwnerWait))
	if got := nodePolicy(t, s, "conv_lead"); got != v1 {
		t.Fatalf("while a notice can still come: %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
	setClock(s, at.Add(time.Minute+notesOwnerWait))
	if got := nodePolicy(t, s, "conv_lead"); got != v2 {
		t.Fatalf("once none can: %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	if got := ownerMessage(t, s, "conv_lead", "m-2"); !strings.Contains(got, "Readings: v3.") {
		t.Fatalf("the next idle message: %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
}

// A retry whose first attempt's move is no longer in flight (a later message moved the block again and was taken)
// carries the last confirmed block, the one the lead runs, and its notice moves nothing.
func TestDirectionNotesRetryAfterALaterMessageWasTaken(t *testing.T) {
	s, f := notesEnv(t)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	saveLeadNotes(t, s, "Readings: v1.")
	ownerMessage(t, s, "conv_lead", "m-0")
	hubSentID(s, "conv_lead", "m-0", assistantwire.ConversationDelivered)
	saveLeadNotes(t, s, "Readings: v2.")
	ownerMessage(t, s, "conv_lead", "m-1") // no notice comes for it
	saveLeadNotes(t, s, "Readings: v3.")
	v3 := ownerMessage(t, s, "conv_lead", "m-2")
	hubSentID(s, "conv_lead", "m-2", assistantwire.ConversationDelivered)
	if got := ownerMessage(t, s, "conv_lead", "m-1"); got != v3 {
		t.Fatalf("the retry carries %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
	hubSentID(s, "conv_lead", "m-1", assistantwire.ConversationDelivered)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if got := nodePolicy(t, s, "conv_lead"); got != v3 {
		t.Fatalf("taken to run %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
}

// noticeBus has the hub's notices reach s as the bus takes them: read off the connection they came on (queueEvent
// stamps it), then handed to ConversationSent as event hands them, here at once rather than on a goroutine of their
// own. hello is the hub's answer to a new connection's hello (onHello).
type noticeBus struct {
	t *testing.T
	b *BusHub
	s *Service
}

func newNoticeBus(t *testing.T, s *Service) *noticeBus {
	b := NewBusHub(BusOptions{Logf: t.Logf})
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // what onHello starts once a connection is up never starts: none is
	b.mu.Lock()
	b.svc, b.ctx = s, ctx
	b.mu.Unlock()
	return &noticeBus{t, b, s}
}

func (n *noticeBus) hello() {
	n.t.Helper()
	if err := n.b.onHello(json.RawMessage(`{}`)); err != nil {
		n.t.Fatal(err)
	}
}

// read is the notice of how the owner's message msg (its client_msg_id; "" none) to the lead went, as the bus reads it
// off the current connection; handle hands it on.
func (n *noticeBus) read(msg, outcome string) busEvent {
	n.t.Helper()
	raw, err := json.Marshal(ConversationSent{MachineID: "server", SessionID: "conv_lead", Outcome: outcome, ClientMsgID: msg})
	if err != nil {
		n.t.Fatal(err)
	}
	n.b.queueEvent(assistantwire.EventConversationSent, raw)
	ev, ok := n.b.events.take()
	if !ok {
		n.t.Fatal("the notice is not in line")
	}
	n.b.pending.Add(-1)
	return ev
}

func (n *noticeBus) handle(ev busEvent) {
	n.t.Helper()
	var p ConversationSent
	if err := json.Unmarshal(ev.params, &p); err != nil {
		n.t.Fatal(err)
	}
	n.s.ConversationSent(ev.ctx(context.Background()), p)
}

func (n *noticeBus) sent(msg, outcome string) { n.handle(n.read(msg, outcome)) }

// Once the hub has named a message in a notice on a connection, a notice on it that names none is about a message sent
// without an id (here one sent just before the manager restarted, which it does not know): it settles no message that
// has one. A new connection, which may be to an older hub that names none, falls back to the oldest message waiting,
// also when a named notice of the connection before it is handled after the new hello.
func TestDirectionNotesNoticeWithoutAnIDOnceTheHubNamesMessages(t *testing.T) {
	s, f := notesEnv(t)
	hub := newNoticeBus(t, s)
	hub.hello()
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	saveLeadNotes(t, s, "Readings: v1.")
	ownerMessage(t, s, "conv_lead", "m-0")
	hub.sent("m-0", assistantwire.ConversationDelivered)
	saveLeadNotes(t, s, "Readings: v2.")
	v2 := ownerMessage(t, s, "conv_lead", "m-1")
	hub.sent("", assistantwire.ConversationFailed) // the earlier message without an id
	hub.sent("m-1", assistantwire.ConversationDelivered)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if got := nodePolicy(t, s, "conv_lead"); got != v2 {
		t.Fatalf("taken to run %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
	late := hub.read("m-x", assistantwire.ConversationDelivered) // read on this connection, handled after the next hello
	hub.hello()
	hub.handle(late)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	saveLeadNotes(t, s, "Readings: v3.")
	if got := ownerMessage(t, s, "conv_lead", "m-2"); !strings.Contains(got, "Readings: v3.") {
		t.Fatalf("the next message: %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
	hub.sent("", assistantwire.ConversationFailed) // an older hub's notice for it
	// The lead runs v2, and the rest of its turn carries it.
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if got := ownerMessage(t, s, "conv_lead", "m-3"); got != v2 {
		t.Fatalf("after a new connection, a message of the turn carries %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
	hub.sent("", assistantwire.ConversationDelivered)
}

// Messages with and without ids wait side by side, each with its own move, the one with an id the older: each notice
// settles its own message, whatever order the notices come in, never the oldest one waiting.
func TestDirectionNotesMessagesWithAndWithoutIDs(t *testing.T) {
	const failed, delivered = assistantwire.ConversationFailed, assistantwire.ConversationDelivered
	for _, tc := range []struct {
		name          string
		namedFirst    bool   // the notice of the message with an id comes first
		first, second string // the outcomes, in the order their notices come
		after, want   string // the notes the lead is taken to run after the first notice, and after both
	}{
		{"the one without an id refused, then the other taken", false, failed, delivered, "v1", "v2"},
		{"the one without an id taken, then the other refused", false, delivered, failed, "v3", "v3"},
		{"the one with an id refused, then the other taken", true, failed, delivered, "v1", "v3"},
		{"both refused", true, failed, failed, "v1", "v1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f := notesEnv(t)
			listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
			saveLeadNotes(t, s, "Readings: v1.")
			ownerMessage(t, s, "conv_lead", "m-0")
			hubSentID(s, "conv_lead", "m-0", delivered)
			saveLeadNotes(t, s, "Readings: v2.")
			if got := ownerMessage(t, s, "conv_lead", "m-1"); !strings.Contains(got, "Readings: v2.") {
				t.Fatalf("the message with an id: %q", got)
			}
			saveLeadNotes(t, s, "Readings: v3.")
			if got := ownerPolicy(t, s, "conv_lead"); !strings.Contains(got, "Readings: v3.") { // without an id
				t.Fatalf("the message without an id: %q", got)
			}
			notice := func(named bool, outcome string) {
				if named {
					hubSentID(s, "conv_lead", "m-1", outcome)
				} else {
					hubSent(s, "conv_lead", outcome)
				}
			}
			expect := func(what, want string) {
				t.Helper()
				if got := nodePolicy(t, s, "conv_lead"); !strings.Contains(got, "Readings: "+want+".") {
					t.Fatalf("%s: taken to run %q, want %s", what, strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())), want)
				}
			}
			listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
			notice(tc.namedFirst, tc.first)
			expect("after the first notice", tc.after)
			notice(!tc.namedFirst, tc.second)
			expect("after both", tc.want)
		})
	}
}

// A retry of the owner's message whose first attempt reached nothing, while a later message that moved the block again
// is still in flight (it went in; its notice has not come), carries the block in effect, the later message's, not its
// own older one: found idle, the lead would otherwise be restarted on a block the manager no longer holds.
func TestDirectionNotesRetryWhileALaterMoveIsInFlight(t *testing.T) {
	for _, outcome := range []string{assistantwire.ConversationDelivered, assistantwire.ConversationFailed} {
		t.Run(outcome, func(t *testing.T) {
			s, f := notesEnv(t)
			listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
			saveLeadNotes(t, s, "Readings: v1.")
			ownerMessage(t, s, "conv_lead", "m-0")
			hubSentID(s, "conv_lead", "m-0", assistantwire.ConversationDelivered)
			saveLeadNotes(t, s, "Readings: v2.")
			ownerMessage(t, s, "conv_lead", "m-1") // never claimed
			saveLeadNotes(t, s, "Readings: v3.")
			v3 := ownerMessage(t, s, "conv_lead", "m-2") // it goes in; its notice comes later
			if got := ownerMessage(t, s, "conv_lead", "m-1"); got != v3 {
				t.Fatalf("the retry carries %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
			}
			hubSentID(s, "conv_lead", "m-1", outcome)
			hubSentID(s, "conv_lead", "m-2", assistantwire.ConversationDelivered)
			listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
			if got := nodePolicy(t, s, "conv_lead"); got != v3 {
				t.Fatalf("taken to run %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
			}
		})
	}
}

// A retry tied to its first attempt whose parameters are refused settles nothing: the hub sends nothing for it, and the
// first attempt keeps waiting for its own notice on its own clock, its block kept once that can no longer come.
func TestDirectionNotesRetryRefusedSettlesNothing(t *testing.T) {
	s, f := notesEnv(t)
	at := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	setClock(s, at)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	saveLeadNotes(t, s, "Readings: v1.")
	v1 := ownerMessage(t, s, "conv_lead", "m-0")
	hubSentID(s, "conv_lead", "m-0", assistantwire.ConversationDelivered)
	saveLeadNotes(t, s, "Readings: v2.")
	v2 := ownerMessage(t, s, "conv_lead", "m-1") // it goes; its notice is lost
	setClock(s, at.Add(9*time.Minute))
	if _, err := ownerParams(t, s, "conv_lead", map[string]any{"text": "go on", "client_msg_id": "m-1", "model": "not a model id!"}); err == nil {
		t.Fatal("a bad model was taken")
	}
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if got := nodePolicy(t, s, "conv_lead"); got != v1 {
		t.Fatalf("while the first attempt waits: %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
	setClock(s, at.Add(notesOwnerWait))
	if got := nodePolicy(t, s, "conv_lead"); got != v2 {
		t.Fatalf("once its notice can no longer come: %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
}

// A notice of an outcome the hub never learned (unknown: the link dropped after the message went) counts as taken: the
// message may be in, so its block stays, and the rest of the turn carries it.
func TestDirectionNotesNoticeOfAnUnknownOutcomeKeepsTheBlock(t *testing.T) {
	s, f := notesEnv(t)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	saveLeadNotes(t, s, "Readings: v1.")
	ownerMessage(t, s, "conv_lead", "m-0")
	hubSentID(s, "conv_lead", "m-0", assistantwire.ConversationDelivered)
	saveLeadNotes(t, s, "Readings: v2.")
	v2 := ownerMessage(t, s, "conv_lead", "m-1")
	hubSentID(s, "conv_lead", "m-1", assistantwire.ConversationUnknown)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if got := nodePolicy(t, s, "conv_lead"); got != v2 {
		t.Fatalf("taken to run %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
	if got := ownerMessage(t, s, "conv_lead", "m-2"); got != v2 {
		t.Fatalf("a later message of the turn: %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
}

// A send whose outcome nobody reports (the node's own message, assistant.model) takes the last confirmed block, not
// one a send still in flight moved: the owner's message that moved it may yet be refused, and the block before it put
// back while the process runs what the node's own message gave it.
func TestDirectionNotesSendWithoutAHandleTakesTheConfirmedBlock(t *testing.T) {
	for _, outcome := range []string{assistantwire.ConversationFailed, assistantwire.ConversationDelivered} {
		t.Run(outcome, func(t *testing.T) {
			s, f := notesEnv(t)
			listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
			saveLeadNotes(t, s, "Readings: v1.")
			v1 := ownerMessage(t, s, "conv_lead", "m-1")
			hubSentID(s, "conv_lead", "m-1", assistantwire.ConversationDelivered)
			saveLeadNotes(t, s, "Readings: v2.")
			v2 := ownerMessage(t, s, "conv_lead", "m-2") // in flight
			if own := nodePolicy(t, s, "conv_lead"); own != v1 {
				t.Fatalf("the node's own message took a block in flight: %q", strings.TrimPrefix(own, mustSupervisionRole(t, supervisionrole.RoleLeader())))
			}
			hubSentID(s, "conv_lead", "m-2", outcome)
			listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
			want := v1
			if outcome == assistantwire.ConversationDelivered {
				want = v2
			}
			if got := nodePolicy(t, s, "conv_lead"); got != want {
				t.Fatalf("taken to run %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
			}
		})
	}
}

// A send whose outcome is never learned keeps the block it carried (#11392): the owner's message whose notice never
// comes is taken for confirmed once its notice can no longer come (until then a send nobody reports takes the block
// before it), and a message whose notice reaches a restarted manager keeps its block too.
func TestDirectionNotesKeptWhenTheOutcomeIsNeverLearned(t *testing.T) {
	s, f := notesEnv(t)
	at := time.Date(2026, 11, 20, 1, 2, 3, 0, time.UTC)
	setClock(s, at)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	saveLeadNotes(t, s, "Readings: v1.")
	v1 := ownerMessage(t, s, "conv_lead", "m-1")
	hubSentID(s, "conv_lead", "m-1", assistantwire.ConversationDelivered)
	saveLeadNotes(t, s, "Readings: v2.")
	v2 := ownerMessage(t, s, "conv_lead", "m-2") // no notice comes for it
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	setClock(s, at.Add(notesOwnerWait-time.Second))
	if got := nodePolicy(t, s, "conv_lead"); got != v1 {
		t.Fatalf("while its notice can still come: %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
	setClock(s, at.Add(notesOwnerWait))
	if got := nodePolicy(t, s, "conv_lead"); got != v2 {
		t.Fatalf("once it can no longer come: %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
	// A manager restart between a send and its outcome: the block is kept, and the restarted manager settles nothing.
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusIdle})
	saveLeadNotes(t, s, "Readings: v3.")
	v3 := ownerMessage(t, s, "conv_lead", "m-3")
	s2 := newServiceOn(t, f, s.store())
	hubSentID(s2, "conv_lead", "m-3", assistantwire.ConversationFailed)
	listLeads(f, proto.Session{SessionID: "conv_lead", Status: proto.StatusWorking})
	if got := nodePolicy(t, s2, "conv_lead"); got != v3 || !strings.Contains(v3, "Readings: v3.") {
		t.Fatalf("after a restart: %q", strings.TrimPrefix(got, mustSupervisionRole(t, supervisionrole.RoleLeader())))
	}
}

// GET /api/v1/assistant/leaders lists notes_at as null for a direction without notes, the value a save sends back, so
// two editors of an empty direction that both echo it are told apart: the first saves, the second gets 409.
func TestLeadersListNotesAtNullWithoutNotes(t *testing.T) {
	s, _ := notesEnv(t)
	listed := listedNotesAtRaw(t, s, "conv_lead")
	if listed == nil {
		t.Fatal("notes_at without notes is left out, want null")
	} else if string(listed) != "null" {
		t.Fatalf("notes_at without notes: %s, want null", listed)
	}
	putNotesAnswer(t, s, `{"notes":"Readings: A.","notes_at":`+string(listed)+`}`, http.StatusOK)
	if got := putNotesAnswer(t, s, `{"notes":"Readings: B.","notes_at":`+string(listed)+`}`, http.StatusConflict); got.Notes != "Readings: A." {
		t.Fatalf("second editor: %+v", got)
	}
}
