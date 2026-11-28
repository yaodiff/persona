package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/screen"
)

// commandScreen is the terminal dialog Claude Code asked on its own screen in the case this follows (11-23): a command
// approval with a warning and a countdown, which no PermissionRequest hook sees and bypassPermissions does not skip.
func commandScreen(id, session string) proto.Interaction {
	ix := askQuestion(id, session)
	ix.Kind, ix.Questions = proto.IxScreen, nil
	ix.Dialog = &screen.Dialog{Question: "Do you want to proceed?", Command: "rmdir ~/work/pext && echo removed",
		Warning:        "Dangerous rmdir operation on working directory or its ancestor",
		Deadline:       "Claude Code will automatically block this request in 1:29, to avoid holding up an unattended session",
		DeadlineAction: screen.DeadlineDeny, Rev: "r1",
		Options: []screen.Option{{Key: "1", Label: "Yes", Keys: []string{"1"}}, {Key: "2", Label: "No", Keys: []string{"2"}}}}
	return ix
}

// A supervised Agent's terminal command approval goes to its supervisor only: no push, the card shown as delegating,
// the supervisor told the command and the warning. The supervisor picks the option itself, kept as the assistant's
// answer; an unrelated conversation cannot. An unsupervised Agent's non-six-class dialog goes to the main conversation
// without pushing to the owner, while a six-class dialog (folder trust) still rings the owner.
func TestSupervisedCommandScreenStaysWithItsSupervisor(t *testing.T) {
	e := newRouteEnv(t, nil, false)
	// List the unsupervised screen sessions as blocked on mac so any concurrent
	// syncScreenInteractions pass sees them as live screen dialogs rather than stale cards.
	macSessions := append(append([]proto.Session(nil), e.agents...),
		proto.Session{SessionID: "unrelated-session", MachineID: "mac", Title: "Plain terminal", Cwd: "/code/other", Status: proto.StatusBlocked, UpdatedAt: time.Now().UTC()},
		proto.Session{SessionID: "unrelated-session-2", MachineID: "mac", Title: "Trust folder", Cwd: "/code/other2", Status: proto.StatusBlocked, UpdatedAt: time.Now().UTC()},
	)
	e.sessions("mac", macSessions...)
	e.f.setSessions("mac", macSessions...)
	plain := commandScreen("ordinary", "unrelated-session")
	plainAnswer := e.raise(plain)
	waitFor(t, "the unsupervised dialog routed to the main conversation", func() bool {
		return hasRelayFrom(e.s, "conv1", cardAgentQuestion, "interaction_id="+plain.ID)
	})
	if !e.lastShown(plain.ID).Delegating {
		t.Fatal("an unsupervised non-owner dialog was not marked delegating")
	}

	trust := commandScreen("trust-folder", "unrelated-session-2")
	trust.Dialog.Command, trust.Dialog.Warning = "", ""
	trust.Dialog.Question = "Quick safety check: Is this a project you trust?"
	trust.Dialog.Options = []screen.Option{
		{Key: "1", Label: "Yes, I trust this folder", Keys: []string{"enter"}},
		{Key: "2", Label: "No, exit", Keys: []string{"down", "enter"}},
	}
	trustAnswer := e.raise(trust)
	waitFor(t, "the six-class trust dialog pushed to the owner", func() bool { return e.pushesOf(trust.ID) == 1 })
	if e.lastShown(trust.ID).Delegating {
		t.Fatal("a six-class folder trust dialog went to a supervisor")
	}

	ix := commandScreen("scr_mac_worker-1_r1", "worker-1")
	answered := e.raise(ix)
	notice := e.noticeWith("conv1", "interaction_id="+ix.ID)
	for _, want := range []string{"rmdir ~/work/pext", "Dangerous rmdir operation", "Yes / No"} {
		if !strings.Contains(notice, want) {
			t.Errorf("the supervisor was not told %q:\n%s", want, notice)
		}
	}
	if !e.lastShown(ix.ID).Delegating {
		t.Fatal("the supervised dialog is not shown as its supervisor's")
	}
	args := map[string]any{"machine": "mac", "session_id": "worker-1", "interaction_id": ix.ID, "answers": map[string]any{"Do you want to proceed?": "Yes"}}
	if _, err := runJSON(t, e.s, "unrelated", "interaction_answer", args); err == nil || !strings.Contains(err.Error(), "only the user") {
		t.Fatalf("a conversation the dialog was not handed to answered it: %v", err)
	}
	if _, err := runJSON(t, e.s, "conv1", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-1",
		"interaction_id": ix.ID, "answers": map[string]any{"Do you want to proceed?": "Maybe"}}); err == nil || !strings.Contains(err.Error(), "1. Yes / 2. No") {
		t.Fatalf("an option the dialog does not have: %v", err)
	}
	args["reason"] = "the folder is the task's own scratch directory"
	res, err := runJSON(t, e.s, "conv1", "interaction_answer", args)
	if err != nil || !strings.Contains(res, "not the user's") {
		t.Fatalf("the supervisor's pick: %s %v", res, err)
	}
	got := answerOf(t, answered)
	if got.Option != "1" || got.AnsweredBy != proto.AnsweredByAssistant || got.Reason != "the folder is the task's own scratch directory" {
		t.Fatalf("the agent got %+v", got)
	}
	e.settle()
	if n := e.pushesOf(ix.ID); n != 0 {
		t.Fatalf("the supervised dialog rang the owner %d times", n)
	}
	if n := e.pushesOf(plain.ID); n != 0 {
		t.Fatalf("the unsupervised non-owner dialog rang %d times", n)
	}
	if n := e.pushesOf(trust.ID); n != 1 {
		t.Fatalf("the six-class trust dialog rang %d times, want 1", n)
	}
	if err := e.s.resolve(plain.ID, proto.Resolution{Option: "2"}); err != nil {
		t.Fatal(err)
	}
	answerOf(t, plainAnswer)
	if err := e.s.resolve(trust.ID, proto.Resolution{Option: "2"}); err != nil {
		t.Fatal(err)
	}
	answerOf(t, trustAnswer)
}

// allow picks a supervised dialog's Yes or No; a dialog that is the user's (a deletion Claude Code cannot rule out, a
// folder's trust) is refused for ask_escalate, which rings the owner once.
func TestSupervisorScreenAllowAndTheUsersDialogs(t *testing.T) {
	e := newRouteEnv(t, nil, false)
	no := commandScreen("scr_no", "worker-1")
	answered := e.raise(no)
	e.noticeWith("conv1", "interaction_id="+no.ID)
	if _, err := runJSON(t, e.s, "conv1", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-1",
		"interaction_id": no.ID, "allow": false, "reason": "not inside the task's directory"}); err != nil {
		t.Fatal(err)
	}
	if got := answerOf(t, answered); got.Option != "2" || got.AnsweredBy != proto.AnsweredByAssistant {
		t.Fatalf("allow false picked %+v", got)
	}

	rm := commandScreen("scr_rm", "worker-2")
	rm.Dialog.Warning, rm.Dialog.Risk = "Dangerous rm operation on possibly-empty variable", screen.RiskDelete
	rmAnswered := e.raise(rm)
	e.noticeWith("conv1", "interaction_id="+rm.ID)
	if _, err := runJSON(t, e.s, "conv1", "interaction_answer", map[string]any{"machine": "mac", "session_id": "worker-2",
		"interaction_id": rm.ID, "allow": true}); err == nil || !strings.Contains(err.Error(), "ask_escalate") {
		t.Fatalf("the supervisor allowed a deletion: %v", err)
	}
	if e.pushesOf(rm.ID) != 0 {
		t.Fatal("the user's dialog rang before its supervisor escalated it")
	}
	if _, err := runJSON(t, e.s, "conv1", "ask_escalate", map[string]any{"todo_id": e.todo.ID, "reason": "It may delete files."}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the escalated dialog pushed once", func() bool { return e.pushesOf(rm.ID) == 1 })
	if err := e.s.resolve(rm.ID, proto.Resolution{Option: "2"}); err != nil {
		t.Fatal(err)
	}
	answerOf(t, rmAnswered)
	e.settle()
}

func TestExternalImportsScreenIsUserOnly(t *testing.T) {
	ix := proto.Interaction{Kind: proto.IxScreen, Dialog: &screen.Dialog{
		Question: "Allow external CLAUDE.md file imports?",
		Options: []screen.Option{
			{Key: "1", Label: " Yes, allow external imports "},
			{Key: "2", Label: "No, continue without external imports"},
		},
	}}
	if why := userScreen(ix); why != "authorizes the agent's access, which only the user grants" {
		t.Fatalf("userScreen = %q", why)
	}
	if !routable(ix) || terminalQuestion(ix) {
		t.Fatal("the supervisor must escalate the dialog to the user")
	}
}

func TestSupervisedScreenAndUserScreen(t *testing.T) {
	ix := commandScreen("id", "s")
	if !supervisedScreen(ix) || !routable(ix) || userScreen(ix) != "" || terminalQuestion(ix) {
		t.Fatal("a command approval is its supervisor's, not an ordinary terminal question")
	}
	for name, change := range map[string]func(*proto.Interaction){
		"raw":     func(v *proto.Interaction) { v.Dialog = nil },
		"secret":  func(v *proto.Interaction) { v.Secret = true },
		"private": func(v *proto.Interaction) { v.Private = true },
		"form":    func(v *proto.Interaction) { v.Fields = []proto.Field{{}} },
		"no options": func(v *proto.Interaction) {
			v.Dialog.Options = []screen.Option{{Key: "1", Label: "Other", Kind: screen.KindTypeSomething}}
		},
	} {
		v, d := ix, *ix.Dialog
		v.Dialog = &d
		change(&v)
		if routable(v) {
			t.Errorf("%s dialog went to a supervisor", name)
		}
	}
	for name, change := range map[string]func(*screen.Dialog){
		"delete":  func(d *screen.Dialog) { d.Risk = screen.RiskDelete },
		"trust":   func(d *screen.Dialog) { d.Options[0].Label = "Yes, I trust this folder" },
		"outward": func(d *screen.Dialog) { d.Command = "gh pr comment 12 --body done" },
	} {
		v, d := ix, *ix.Dialog
		d.Options = append([]screen.Option(nil), ix.Dialog.Options...)
		v.Dialog = &d
		change(&d)
		if !routable(v) || userScreen(v) == "" {
			t.Errorf("%s dialog: routable %v, user's %q", name, routable(v), userScreen(v))
		}
	}
}

// The Live Activity and Persona Link's summary leave out an agent whose every pending card its supervisor holds, though
// its state says needs_you; one with a card of the user's beside it, an unsupervised dialog, or a question the
// assistant is still deciding on stays counted.
func TestSupervisorHeldAgentsAreNotTheUsers(t *testing.T) {
	needs := func(id string) fleetInput {
		return fleetInput{Machine: "Mac", Session: &proto.Session{MachineID: "mac", SessionID: id, Status: proto.StatusBlocked, State: proto.StateNeedsYou}}
	}
	card := func(id, session, kind string, delegating bool) proto.Interaction {
		return proto.Interaction{ID: id, MachineID: "mac", SessionID: session, Kind: kind, Delegating: delegating}
	}
	agents := []fleetInput{needs("held"), needs("mixed"), needs("plain"), needs("deciding"), needs("terminal")}
	ixs := []proto.Interaction{
		card("a", "held", proto.IxScreen, true), card("b", "held", proto.IxPermission, true),
		card("c", "mixed", proto.IxScreen, true), card("d", "mixed", proto.IxQuestion, false),
		card("e", "plain", proto.IxScreen, false),
		card("f", "deciding", proto.IxQuestion, true),
	}
	st := buildFleet(agents, ixs)
	if st.NeedsYou != 4 {
		t.Fatalf("Live Activity needs you = %d, want 4 (all but the supervisor-held agent)", st.NeedsYou)
	}
	att := linkAttention(i18n.Default, agents, ixs, map[string]bool{"d": true, "e": true})
	if att.NeedsYou != 4 {
		t.Fatalf("Persona Link needs you = %d, want 4", att.NeedsYou)
	}
	for _, it := range att.Items {
		if it.SessionID == "held" {
			t.Fatalf("the supervisor-held agent is listed: %+v", it)
		}
	}
}

// An out-of-order sequenced snapshot from a concurrent publish must not revert fleet.rep
// or trigger a spurious sessionsChanged/syncScreenInteractions pass. Unsequenced direct
// snapshots (Seq == 0) remain authoritative.
func TestFleetTakeSnapshotIgnoresStaleSeq(t *testing.T) {
	ft := newFleetTest(t)
	s := ft.s
	ft.sessions("mac", proto.Session{SessionID: "worker-1", MachineID: "mac", Status: proto.StatusBlocked})

	s.fleetTakeSnapshot(Snapshot{
		Seq:   10,
		Todos: &TodoReport{Links: []TodoLink{{MachineID: "mac", SessionID: "worker-1", TodoID: "t1"}}},
	})

	ix := commandScreen("stale-check", "unlisted-session")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan proto.Resolution, 1)
	go func() {
		r, _ := s.waitInteraction(ctx, ix)
		done <- r
	}()
	waitFor(t, "card stale-check", func() bool { _, ok := s.pendingIx(ix.ID); return ok })

	// Stale sequenced snapshot arrives after Seq 10: must be dropped without reverting links
	// or cancelling pending screen cards via a spurious sessionsChanged("mac").
	s.fleetTakeSnapshot(Snapshot{
		Seq:   9,
		Todos: &TodoReport{Links: []TodoLink{}},
	})
	s.fleet.rep.mu.RLock()
	gotSeq := s.fleet.rep.seq
	gotLink := s.fleet.rep.links[[2]string{"mac", "worker-1"}]
	s.fleet.rep.mu.RUnlock()
	if gotSeq != 10 || gotLink.TodoID != "t1" {
		t.Fatalf("stale snapshot overwrote fleet.rep: seq=%d link=%+v", gotSeq, gotLink)
	}
	if _, ok := s.pendingIx(ix.ID); !ok {
		t.Fatal("stale snapshot triggered sessionsChanged and cancelled pending screen card")
	}
	if err := s.resolve(ix.ID, proto.Resolution{Option: "2"}); err != nil {
		t.Fatal(err)
	}
	<-done

	// Newer sequenced snapshot applies cleanly.
	s.fleetTakeSnapshot(Snapshot{
		Seq:   11,
		Todos: &TodoReport{Links: []TodoLink{}},
	})
	s.fleet.rep.mu.RLock()
	gotSeq = s.fleet.rep.seq
	gotLinks := len(s.fleet.rep.links)
	s.fleet.rep.mu.RUnlock()
	if gotSeq != 11 || gotLinks != 0 {
		t.Fatalf("newer snapshot not applied: seq=%d links=%d", gotSeq, gotLinks)
	}

	// Unsequenced direct snapshot (Seq == 0) remains authoritative.
	s.fleetTakeSnapshot(Snapshot{
		Todos: &TodoReport{Links: []TodoLink{{MachineID: "mac", SessionID: "worker-2", TodoID: "t2"}}},
	})
	s.fleet.rep.mu.RLock()
	gotLink2 := s.fleet.rep.links[[2]string{"mac", "worker-2"}]
	s.fleet.rep.mu.RUnlock()
	if gotLink2.TodoID != "t2" {
		t.Fatalf("unsequenced snapshot not applied: link=%+v", gotLink2)
	}
}
