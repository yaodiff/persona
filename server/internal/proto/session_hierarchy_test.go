package proto

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"
)

// The hierarchy's members are keys of the row itself, each only when set, and come back as they went (#5120).
func TestSessionHierarchyWire(t *testing.T) {
	started := time.Date(2026, 11, 12, 8, 30, 0, 0, time.UTC)
	var s Session
	s.SessionID = "child"
	s.SetStartedAt(started)
	s.SetAskingParentSessionID("parent")
	s.SetFold("root", "", "", nil)
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"started_at":"2026-11-12T08:30:00Z"`, `"asking_parent_session_id":"parent"`, `"folded_under":"root"`} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s has no %s", b, want)
		}
	}
	if strings.Contains(string(b), `"children"`) || strings.Contains(string(b), `"Hierarchy"`) {
		t.Errorf("%s: no children, and no key of the hierarchy's own", b)
	}
	var back Session
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, s) {
		t.Fatalf("round trip: %+v, want %+v", back.Hierarchy, s.Hierarchy)
	}

	var parent Session
	parent.SessionID = "root"
	parent.SetFold("", "", "", &SessionChildren{Total: 3, Working: 2})
	b, _ = json.Marshal(parent)
	if !strings.Contains(string(b), `"children":{"total":3,"working":2}`) || strings.Contains(string(b), "folded_under") {
		t.Fatalf("parent row: %s", b)
	}
	var none Session
	b, _ = json.Marshal(none)
	for _, key := range []string{"started_at", "asking_parent_session_id", "folded_under", "folded_as", "children", "workers"} {
		if strings.Contains(string(b), `"`+key+`"`) {
			t.Errorf("a row with no hierarchy writes %s: %s", key, b)
		}
	}
	if err := json.Unmarshal(b, &none); err != nil || none.Hierarchy != nil {
		t.Fatalf("a row with no hierarchy decodes to one: %+v %v", none.Hierarchy, err)
	}
}

// A tab's worker says why it folds under its lead, and the lead how many of the rows folded under it are its workers;
// both come back as they went, and a row that is neither writes neither key (#5180).
func TestSessionHierarchyTabTeamWire(t *testing.T) {
	var worker Session
	worker.SessionID = "worker"
	worker.SetFold("lead", FoldedAsWorker, "", nil)
	b, err := json.Marshal(worker)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"folded_under":"lead","folded_as":"worker"`) || strings.Contains(string(b), `"children"`) {
		t.Fatalf("worker row: %s", b)
	}
	var lead Session
	lead.SessionID = "lead"
	lead.SetFold("", "", "", &SessionChildren{Total: 3, Working: 1, NeedsYou: 1, Workers: 2})
	lb, _ := json.Marshal(lead)
	if !strings.Contains(string(lb), `"children":{"total":3,"working":1,"needs_you":1,"workers":2}`) || strings.Contains(string(lb), "folded_as") {
		t.Fatalf("lead row: %s", lb)
	}
	for _, c := range []struct {
		in   []byte
		want Session
	}{{b, worker}, {lb, lead}} {
		var back Session
		if err := json.Unmarshal(c.in, &back); err != nil || !reflect.DeepEqual(back, c.want) {
			t.Fatalf("round trip of %s: %+v %v", c.in, back.Hierarchy, err)
		}
	}
	var child Session
	child.SetFold("lead", "", "", &SessionChildren{Total: 1})
	if cb, _ := json.Marshal(child); strings.Contains(string(cb), "folded_as") || strings.Contains(string(cb), "workers") {
		t.Fatalf("a child folded under a lead: %s", cb)
	}
	ix, _ := json.Marshal(Interaction{ID: "i1", FoldedUnder: "lead", FoldedAs: FoldedAsWorker})
	if !strings.Contains(string(ix), `"folded_under":"lead","folded_as":"worker"`) {
		t.Fatalf("interaction: %s", ix)
	}
	if ix, _ := json.Marshal(Interaction{ID: "i1"}); strings.Contains(string(ix), "folded_as") {
		t.Fatalf("an interaction of no worker: %s", ix)
	}
}

// A row decoded over one that had a hierarchy keeps what the bytes do not say, as every other field does, and never
// writes into the hierarchy the old row shares with its copies.
func TestSessionHierarchyDecodeKeepsSharedOne(t *testing.T) {
	var old Session
	old.SetFold("root", "", "", &SessionChildren{Total: 1})
	old.SetStartedAt(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	copyOf := old
	shared := old.Hierarchy
	if err := json.Unmarshal([]byte(`{"folded_under":"other","children":{"total":5}}`), &copyOf); err != nil {
		t.Fatal(err)
	}
	if shared.FoldedUnder != "root" || shared.Children.Total != 1 {
		t.Fatalf("decoding wrote into the shared hierarchy: %+v %+v", shared, shared.Children)
	}
	if copyOf.FoldedUnder() != "other" || copyOf.Children().Total != 5 || !copyOf.StartedAt().Equal(old.StartedAt()) {
		t.Fatalf("decoded %+v", copyOf.Hierarchy)
	}
	if err := json.Unmarshal([]byte(`{"folded_under":"","children":null,"started_at":"0001-01-01T00:00:00Z"}`), &copyOf); err != nil {
		t.Fatal(err)
	}
	if copyOf.Hierarchy != nil {
		t.Fatalf("an emptied hierarchy stays: %+v", copyOf.Hierarchy)
	}
}

// The setters never write into the hierarchy a row shares with its copies, and make none when nothing changes.
func TestSessionHierarchySetters(t *testing.T) {
	var a Session
	a.SetFold("", "", "", nil)
	a.SetAskingParentSessionID("")
	a.SetStartedAt(time.Time{})
	if a.Hierarchy != nil {
		t.Fatal("empty values made a hierarchy")
	}
	a.SetFold("root", "", "", nil)
	b := a
	b.SetFold("other", "", "", &SessionChildren{Total: 2})
	if a.FoldedUnder() != "root" || a.Children() != (SessionChildren{}) {
		t.Fatalf("a copy's setter changed the row: %+v", a.Hierarchy)
	}
	h := b.Hierarchy
	if b.SetFold("other", "", "", &SessionChildren{Total: 2}); b.Hierarchy != h {
		t.Fatal("an unchanged fold made a new hierarchy")
	}
	if n := testing.AllocsPerRun(100, func() { b.SetFold("other", "", "", &SessionChildren{Total: 2}) }); n != 0 {
		t.Fatalf("an unchanged fold allocates %v times", n)
	}
	if b.SetFold("other", FoldedAsWorker, "", &SessionChildren{Total: 2}); b.Hierarchy == h || h.FoldedAs != "" || b.FoldedAs() != FoldedAsWorker {
		t.Fatalf("folded_as alone changed: %+v (was %+v)", b.Hierarchy, h)
	}
	w := b.Hierarchy
	if b.SetFold("other", FoldedAsWorker, "", &SessionChildren{Total: 2}); b.Hierarchy != w {
		t.Fatal("an unchanged worker fold made a new hierarchy")
	}
	b.SetFold("other", "", "", &SessionChildren{Total: 2, Workers: 1})
	if b.FoldedAs() != "" || b.Children().Workers != 1 || w.Children.Workers != 0 {
		t.Fatalf("a rollup's workers: %+v (was %+v)", b.Hierarchy, w)
	}
	b.SetFold("", "", "", nil)
	if b.Hierarchy != nil {
		t.Fatalf("a cleared fold keeps %+v", b.Hierarchy)
	}
}

// A side session says it folds as one, a row folded through it names it, and the parent's rollup counts its sides by
// their own states; each comes back as it went, and a row that is none writes none of the keys (#7944).
func TestSessionHierarchySideWire(t *testing.T) {
	var side, kid, parent Session
	side.SessionID, kid.SessionID, parent.SessionID = "side", "kid", "parent"
	side.SetParentAgentID("parent")
	side.SetFold("parent", FoldedAsSide, "", nil)
	kid.SetFold("parent", "", "side", nil)
	parent.SetFold("", "", "", &SessionChildren{Total: 3, Working: 1, Sides: 2, SidesWorking: 1, SidesUnread: 1})
	for _, c := range []struct {
		s    Session
		want string
	}{
		{side, `"parent_agent_id":"parent","folded_under":"parent","folded_as":"side"`},
		{kid, `"folded_under":"parent","folded_side":"side"`},
		{parent, `"children":{"total":3,"working":1,"sides":2,"sides_working":1,"sides_unread":1}`},
	} {
		b, err := json.Marshal(c.s)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), c.want) {
			t.Fatalf("%s has no %s", b, c.want)
		}
		ref, _ := json.Marshal(wireOf((*sessionJSON)(&c.s)))
		if string(ref) != string(b) {
			t.Fatalf("written by hand:\n%s\nreflect encoder:\n%s", b, ref)
		}
		var back Session
		if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, c.s) {
			t.Fatalf("round trip of %s: %+v %v", b, back.Hierarchy, err)
		}
	}
	if b, _ := json.Marshal(side); strings.Contains(string(b), "folded_side") || strings.Contains(string(b), "children") {
		t.Fatalf("a side names itself: %s", b)
	}
	var plain Session
	plain.SetFold("lead", "", "", &SessionChildren{Total: 1, Workers: 1})
	if b, _ := json.Marshal(plain); strings.Contains(string(b), "side") {
		t.Fatalf("a row of no side: %s", b)
	}
	h := kid.Hierarchy
	if kid.SetFold("parent", "", "side", nil); kid.Hierarchy != h {
		t.Fatal("an unchanged fold made a new hierarchy")
	}
	if kid.SetFold("parent", "", "", nil); kid.FoldedSide() != "" || h.FoldedSide != "side" {
		t.Fatal("folded_side was not cleared, or was cleared in place")
	}
	ix, _ := json.Marshal(Interaction{ID: "i1", FoldedUnder: "parent", FoldedSide: "side"})
	if !strings.Contains(string(ix), `"folded_under":"parent","folded_side":"side"`) {
		t.Fatalf("interaction: %s", ix)
	}
	if ix, _ := json.Marshal(Interaction{ID: "i1"}); strings.Contains(string(ix), "folded_side") {
		t.Fatalf("an interaction of no side: %s", ix)
	}
}

func TestFoldedState(t *testing.T) {
	for _, c := range []struct {
		state    string
		awaits   bool
		children *SessionChildren
		want     string
	}{
		{StateIdle, false, nil, StateIdle},
		{StateNeedsYou, false, &SessionChildren{Total: 1, Working: 1}, StateNeedsYou},
		{StateWorking, false, &SessionChildren{Total: 1, NeedsYou: 1}, StateNeedsYou},
		{StateIdle, true, &SessionChildren{Total: 2, NeedsYou: 1, Working: 1}, StateNeedsYou},
		{StateIdle, false, &SessionChildren{Total: 1, Working: 1}, StateWorking},
		{StateOffline, false, &SessionChildren{Total: 1, Working: 1}, StateWorking},
		{StateUnread, false, &SessionChildren{Total: 1, Working: 1}, StateUnread},
		{StateIdle, true, &SessionChildren{Total: 1, Working: 1}, StateIdle},
		{StateWorking, false, &SessionChildren{Total: 3}, StateWorking},
		{StateUnread, false, &SessionChildren{Total: 3}, StateUnread},
		// A side session's unseen result shows on its parent (#7944); a Codex child's or a worker's does not.
		{StateIdle, false, &SessionChildren{Total: 1, Sides: 1, SidesUnread: 1}, StateUnread},
		{StateOffline, false, &SessionChildren{Total: 1, Sides: 1, SidesUnread: 1}, StateUnread},
		{StateIdle, true, &SessionChildren{Total: 1, Sides: 1, SidesUnread: 1}, StateUnread},
		{StateWorking, false, &SessionChildren{Total: 1, Sides: 1, SidesUnread: 1}, StateWorking},
		{StateIdle, false, &SessionChildren{Total: 2, Working: 1, Sides: 2, SidesWorking: 1, SidesUnread: 1}, StateWorking},
		{StateIdle, false, &SessionChildren{Total: 2, NeedsYou: 1, Sides: 2, SidesNeedsYou: 1, SidesUnread: 1}, StateNeedsYou},
		{StateIdle, false, &SessionChildren{Total: 2, Workers: 1}, StateIdle},
	} {
		s := Session{State: c.state, AwaitsReply: c.awaits}
		s.SetFold("", "", "", c.children)
		if got := s.FoldedState(); got != c.want {
			t.Errorf("%s (awaits %v) with %+v: %s, want %s", c.state, c.awaits, c.children, got, c.want)
		}
	}
}

func TestSessionIsChild(t *testing.T) {
	for _, c := range []struct {
		s    Session
		want bool
	}{
		{Session{SessionID: "a"}, false},
		{Session{SessionID: "a", RootSessionID: "a"}, false},
		{Session{SessionID: "a", RootSessionID: "r"}, true},
		{Session{SessionID: "a", IsSubagent: true}, true},
		{Session{SessionID: "a", ParentSessionID: "p"}, true},
		{Session{SessionID: "a", Hierarchy: &SessionHierarchy{AskingParentSessionID: "p"}}, true},
	} {
		if got := c.s.IsChild(); got != c.want {
			t.Errorf("%+v: %v", c.s, got)
		}
	}
}

func TestTokens32(t *testing.T) {
	if Tokens32(1_000_000) != 1_000_000 || Tokens32(1<<40) != 1<<31-1 || Tokens32(-1<<40) != -1<<31 {
		t.Fatal("Tokens32 does not saturate")
	}
}

// The conversation's earlier stretches (#5054) are a key of the row, oldest first, written by hand as the reflect
// encoder writes them, and come back as they went; setting the same ones makes no new hierarchy, and copies are not
// shared with the caller's slice.
func TestSessionEngineHistory(t *testing.T) {
	t1 := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Hour)
	legs := []EngineLeg{{SessionID: "claude-1", Agent: "claude", Until: t1}, {SessionID: "codex-1", Agent: "codex", Until: t2}}
	var s Session
	s.SessionID = "claude-1"
	s.SetEngineHistory(legs)
	legs[0].SessionID = "changed"
	if got := s.EngineHistory()[0].SessionID; got != "claude-1" {
		t.Fatalf("the row's history follows the caller's slice: %q", got)
	}
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	want := `"engine_history":[{"session_id":"claude-1","agent":"claude","until":"2026-11-12T09:00:00Z"},{"session_id":"codex-1","agent":"codex","until":"2026-11-12T10:00:00Z"}]`
	if !strings.Contains(string(b), want) {
		t.Fatalf("%s has no %s", b, want)
	}
	ref, err := json.Marshal(wireOf((*sessionJSON)(&s)))
	if err != nil {
		t.Fatal(err)
	}
	if string(ref) != string(b) {
		t.Fatalf("written by hand:\n%s\nreflect encoder:\n%s", b, ref)
	}
	var back Session
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.EngineHistory(), s.EngineHistory()) {
		t.Fatalf("round trip: %+v, want %+v", back.EngineHistory(), s.EngineHistory())
	}
	h := s.Hierarchy
	s.SetEngineHistory([]EngineLeg{{SessionID: "claude-1", Agent: "claude", Until: t1}, {SessionID: "codex-1", Agent: "codex", Until: t2}})
	if s.Hierarchy != h {
		t.Fatal("the same history made a new hierarchy")
	}
	s.SetEngineHistory(nil)
	if s.Hierarchy != nil || s.EngineHistory() != nil {
		t.Fatalf("no history left a hierarchy: %+v", s.Hierarchy)
	}
}

// Directory visibility is sparse and copy-on-write, like the other row metadata.
func TestSessionHiddenByDirectoryCopyOnWrite(t *testing.T) {
	var a Session
	a.SetHiddenByDirectory(false)
	if a.Hierarchy != nil {
		t.Fatal("default visibility allocated sparse metadata")
	}
	a.SetHiddenByDirectory(true)
	b := a
	b.SetHiddenByDirectory(false)
	if !a.HiddenByDirectory() || b.HiddenByDirectory() || b.Hierarchy != nil {
		t.Fatal("visibility update mutated a shared row")
	}
	kept := a.Hierarchy
	if n := testing.AllocsPerRun(100, func() { a.SetHiddenByDirectory(true) }); n != 0 || a.Hierarchy != kept {
		t.Fatal("unchanged visibility allocated")
	}
	a.SetFold("parent", "", "", nil)
	a.SetHiddenByDirectory(false)
	if a.HiddenByDirectory() || a.FoldedUnder() != "parent" {
		t.Fatal("visibility update dropped other metadata")
	}
}

// Cross-machine hierarchy fields (folded_under_machine_id, parent_machine_id, a2a_task_id, subagent_origin)
// live in sparse SessionHierarchy, round-trip cleanly on the wire, remain zero-alloc when unchanged, and omit
// empty keys for ordinary same-machine rows.
func TestSessionHierarchyCrossMachineWire(t *testing.T) {
	var child Session
	child.MachineID = "box"
	child.SessionID = "sub-1"
	child.IsSubagent = true
	child.ParentSessionID = "lead-1"
	child.SetSubagentProvenance("mac", "task-123", SubagentOriginA2A)
	child.SetFoldWithMachine("mac", "lead-1", "", "", nil)

	b, err := json.Marshal(child)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"folded_under":"lead-1"`,
		`"folded_under_machine_id":"mac"`,
		`"parent_machine_id":"mac"`,
		`"a2a_task_id":"task-123"`,
		`"subagent_origin":"a2a"`,
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("%s missing %s", b, want)
		}
	}
	ref, err := json.Marshal(wireOf((*sessionJSON)(&child)))
	if err != nil {
		t.Fatal(err)
	}
	if string(ref) != string(b) {
		t.Fatalf("hand-written wire differs from reflect:\n got %s\nwant %s", b, ref)
	}
	var back Session
	if err := json.Unmarshal(b, &back); err != nil || !reflect.DeepEqual(back, child) {
		t.Fatalf("round trip: %+v vs %+v (err=%v)", back.Hierarchy, child.Hierarchy, err)
	}

	// Copy-on-write and zero-alloc on unchanged provenance / fold.
	clone := child
	kept := child.Hierarchy
	if n := testing.AllocsPerRun(100, func() {
		child.SetSubagentProvenance("mac", "task-123", SubagentOriginA2A)
		child.SetFoldWithMachine("mac", "lead-1", "", "", nil)
	}); n != 0 || child.Hierarchy != kept {
		t.Fatalf("unchanged cross-machine setters allocated %v times", n)
	}
	clone.SetFoldWithMachine("", "", "", "", nil)
	if clone.FoldedUnder() != "" || clone.FoldedUnderMachineID() != "" || child.FoldedUnderMachineID() != "mac" {
		t.Fatalf("clearing clone mutated original or left dangling machine: clone=%+v child=%+v", clone.Hierarchy, child.Hierarchy)
	}
	clone.SetSubagentProvenance("", "", "")
	if clone.Hierarchy != nil {
		t.Fatalf("emptying all sparse fields left hierarchy: %+v", clone.Hierarchy)
	}

	// Interaction carries folded_under_machine_id only when set.
	ixBytes, err := json.Marshal(Interaction{ID: "ix-1", MachineID: "box", SessionID: "sub-1", FoldedUnder: "lead-1", FoldedUnderMachineID: "mac"})
	if err != nil || !strings.Contains(string(ixBytes), `"folded_under":"lead-1","folded_under_machine_id":"mac"`) {
		t.Fatalf("interaction wire: %s (err=%v)", ixBytes, err)
	}
	if plainIx, _ := json.Marshal(Interaction{ID: "ix-2", FoldedUnder: "lead-1"}); strings.Contains(string(plainIx), "folded_under_machine_id") {
		t.Fatalf("same-machine interaction wrote folded_under_machine_id: %s", plainIx)
	}
}

func TestSessionBotmuxExternalTriggerWire(t *testing.T) {
	var s Session
	s.SessionID = "botmux-1"
	if s.BotmuxExternalTrigger() || s.Hierarchy != nil {
		t.Fatalf("zero session should have false flag and nil hierarchy")
	}
	s.SetBotmuxExternalTrigger(BotmuxTriggerOwnerUnset)
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"botmux_external_trigger":true`) ||
		!strings.Contains(string(b), `"botmux_external_trigger_reason":"owner_unset"`) {
		t.Fatalf("missing top-level botmux_external_trigger and its reason: %s", b)
	}
	var back Session
	if err := json.Unmarshal(b, &back); err != nil || !back.BotmuxExternalTrigger() ||
		back.BotmuxExternalTriggerReason() != BotmuxTriggerOwnerUnset || !reflect.DeepEqual(back, s) {
		t.Fatalf("round trip mismatch: %+v vs %+v (err=%v)", back.Hierarchy, s.Hierarchy, err)
	}
	upgrade := s
	upgrade.SetBotmuxExternalTrigger(BotmuxTriggerUpgrade)
	if !upgrade.BotmuxExternalTrigger() || upgrade.BotmuxExternalTriggerReason() != BotmuxTriggerUpgrade ||
		s.BotmuxExternalTriggerReason() != BotmuxTriggerOwnerUnset {
		t.Fatalf("changing the reason must copy on write: upgrade=%+v orig=%+v", upgrade.Hierarchy, s.Hierarchy)
	}
	clone := s
	clone.SetBotmuxExternalTrigger("")
	if clone.BotmuxExternalTrigger() || clone.Hierarchy != nil || !s.BotmuxExternalTrigger() {
		t.Fatalf("clearing clone failed copy-on-write or empty cleanup: clone=%+v orig=%+v", clone.Hierarchy, s.Hierarchy)
	}
	if bFalse, _ := json.Marshal(clone); strings.Contains(string(bFalse), "botmux_external_trigger") {
		t.Fatalf("false flag must be omitted on wire: %s", bFalse)
	}
}

func TestParseBotmuxOwnerUnionID(t *testing.T) {
	for in, want := range map[string]BotmuxOwnerUnionID{
		"":                               "",
		"  ":                             "",
		" on_abc123 ":                    "on_abc123",
		"on_A-b_9":                       "on_A-b_9",
		"on_" + strings.Repeat("a", 125): BotmuxOwnerUnionID("on_" + strings.Repeat("a", 125)),
	} {
		if got, ok := ParseBotmuxOwnerUnionID(in); !ok || got != want {
			t.Errorf("ParseBotmuxOwnerUnionID(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, in := range []string{"on_", "ou_abc", "on_a b", "on_中文", "ON_abc", "on_abc@example.com", "on_" + strings.Repeat("a", 126)} {
		if got, ok := ParseBotmuxOwnerUnionID(in); ok || got != "" {
			t.Errorf("ParseBotmuxOwnerUnionID(%q) = %q, %v; want refused", in, got, ok)
		}
	}
}
