package node

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
)

// Split, close a whole tab and the guarded tab rename on Orca (#17043), against the fake runtime, which checks each
// request against Orca's schema. Orca 1.4.277 did each of them live on an isolated runtime.

// orcaPaneHost is what the panes listing says the Orca host can do, or a failed test.
func orcaPaneHost(t *testing.T, n *Node) proto.PaneAbilities {
	t.Helper()
	res, err := n.listPanes(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range res.Hosts {
		if h.Kind == host.Orca && h.Abilities != nil {
			return *h.Abilities
		}
	}
	t.Fatalf("no Orca host with abilities in %+v", res)
	return proto.PaneAbilities{}
}

// A pane splits to the right (Orca's "vertical") or down ("horizontal"), fenced to the terminal's process; the new
// pane is the one Orca made, in the same tab. A pane working in another folder gets `cd <folder>` as the new shell's
// first command, as Orca takes no cwd on this call; the worktree's own folder needs none.
func TestOrcaSplitsAPane(t *testing.T) {
	n, o, _ := orcaSession(t)
	if c := orcaPaneHost(t, n); !c.Split || !c.CloseTab || !c.Rename {
		t.Fatalf("Orca offers %+v, want split, close tab and rename", c)
	}
	res, err := request(t, n, "panes.split", map[string]any{"pane": "orca:tab1:leaf1", "direction": "right"})
	if err != nil {
		t.Fatal(err)
	}
	if res["pane"] != "orca:tab1:leaf_split_1" || res["terminal"] != "pane:orca:tab1:leaf_split_1" {
		t.Fatalf("split answered %v", res)
	}
	if _, err := request(t, n, "panes.split", map[string]any{"pane": "orca:tab1:leaf1", "direction": "down"}); err != nil {
		t.Fatal(err)
	}
	var splits []string
	for _, w := range o.Writes() {
		if w.Split != "" {
			splits = append(splits, w.Handle+" "+w.Split+" "+w.Text)
		}
	}
	if strings.Join(splits, "|") != "term_1 vertical |term_1 horizontal " {
		t.Fatalf("splits %q", splits)
	}
	for _, p := range o.Params("terminal.split") {
		if p["expectedIncarnationId"] != "inc-term_1" {
			t.Errorf("split not fenced to the terminal's process: %v", p)
		}
	}
	if inv := o.Invalid(); len(inv) != 0 {
		t.Errorf("params Orca would refuse: %v", inv)
	}

	h := n.hosts.Get(host.Orca).(*orcaHost)
	o.Reset()
	if _, err := h.Split(context.Background(), host.PaneRef{Kind: host.Orca, ID: "tab1:leaf1"}, "right", orcaCwd+"/web"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.Split(context.Background(), host.PaneRef{Kind: host.Orca, ID: "tab1:leaf1"}, "right", orcaCwd+"/bad\nname"); err != nil {
		t.Fatal(err)
	}
	if w := o.Writes(); len(w) != 2 || w[0].Text != "cd "+shellQuote(orcaCwd+"/web") || w[1].Text != "" {
		t.Fatalf("split commands %+v", w)
	}
	if _, err := h.Split(context.Background(), host.PaneRef{Kind: host.Orca, ID: "tab1:leaf1"}, "left", ""); err == nil {
		t.Fatal("split to the left")
	}
}

// Close tab closes the whole tab through one of its terminals; the node then lists none of it.
func TestOrcaClosesATab(t *testing.T) {
	n, o, _ := orcaSession(t)
	if _, err := request(t, n, "tabs.close", map[string]any{"kind": host.Orca, "tab_id": "tab1"}); err != nil {
		t.Fatal(err)
	}
	if w := o.Writes(); len(w) != 1 || w[0].Handle != "term_1" || !w[0].Tab {
		t.Fatalf("writes %+v", w)
	}
	if tr := o.Trace("terminal.closeTab", "terminal.close"); strings.Join(tr, "|") != `terminal.closeTab {"terminal":"term_1"}` {
		t.Fatalf("calls %v", tr)
	}
	if m := byID(listSessions(t, n)); m[sidA].PaneID != "" {
		t.Fatalf("the closed tab's session still has a pane: %+v", m[sidA])
	}
	if _, err := request(t, n, "tabs.close", map[string]any{"kind": host.Orca, "tab_id": "tab1"}); err == nil {
		t.Fatal("closed a tab that is not there")
	}
}

// An Orca without terminal.split or terminal.closeTab answers method_not_found: the action is refused as unsupported
// with nothing done, and the listing stops offering it until another Orca runtime answers.
func TestOrcaWithoutSplitOrCloseTab(t *testing.T) {
	n, o, _ := orcaSession(t)
	o.Lack("terminal.split")
	o.Lack("terminal.closeTab")
	_, err := request(t, n, "panes.split", map[string]any{"pane": "orca:tab1:leaf1", "direction": "right"})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("split on an older Orca: %v", err)
	}
	_, err = request(t, n, "tabs.close", map[string]any{"kind": host.Orca, "tab_id": "tab1"})
	if err == nil || !strings.Contains(err.Error(), "unsupported") {
		t.Fatalf("close tab on an older Orca: %v", err)
	}
	if w := o.Writes(); len(w) != 0 {
		t.Fatalf("an older Orca was written to: %+v", w)
	}
	if c := orcaPaneHost(t, n); c.Split || c.CloseTab || !c.Rename {
		t.Fatalf("an older Orca offers %+v", c)
	}
	// Refused again without asking that Orca.
	o.Reset()
	h := n.hosts.Get(host.Orca).(*orcaHost)
	if err := h.CloseSlot(context.Background(), "tab1"); !errors.Is(err, host.ErrUnsupported) || !errors.Is(err, host.ErrNotDelivered) {
		t.Fatalf("close tab again: %v", err)
	}
	if n := o.Count("terminal.closeTab"); n != 0 {
		t.Fatalf("asked the older Orca again %d times", n)
	}
	// Orca started again (updated): offered again.
	if err := o.Restart("test-token-2"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.Probe(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c := orcaPaneHost(t, n); !c.Split || !c.CloseTab {
		t.Fatalf("a new Orca runtime offers %+v", c)
	}
}

// A renamed Orca tab is listed under its new name, though Orca's layouts (on a runtime with no window) keep its first
// title: the phone tab model's custom name wins, read when the layouts are. Persona's own rename has the next listing
// read them; a rename made in Orca shows once they are read again. An Orca without the model lists the layouts' titles.
func TestOrcaRenamedTabsKeepTheirNames(t *testing.T) {
	n, o, _ := orcaSession(t)
	slotName := func() string {
		t.Helper()
		return byID(listSessions(t, n))[sidA].SlotName
	}
	if got := slotName(); got != "tab tab1" {
		t.Fatalf("first name %q", got)
	}
	if _, err := request(t, n, "tabs.rename", map[string]any{"kind": host.Orca, "tab_id": "tab1", "label": "fix login"}); err != nil {
		t.Fatal(err)
	}
	if got := slotName(); got != "fix login" {
		t.Fatalf("after Persona's rename %q", got)
	}
	o.RenameTab("tab1", "owner name")
	o.SetRevision("repo::"+orcaCwd, 2) // the layouts are read again
	if got := slotName(); got != "owner name" {
		t.Fatalf("after a rename in Orca %q", got)
	}
	if tr := o.Trace(); len(tr) != 1 || !strings.HasPrefix(tr[0], "terminal.rename ") {
		t.Errorf("the listing's reads showed as actions: %v", tr)
	}

	n, o, _ = orcaSession(t)
	o.Lack("session.tabs.list")
	o.RenameTab("tab1", "owner name")
	o.SetRevision("repo::"+orcaCwd, 2)
	if got := slotName(); got != "tab tab1" {
		t.Fatalf("an Orca without the tab model: %q", got)
	}
	o.SetRevision("repo::"+orcaCwd, 3)
	listSessions(t, n)
	if c := o.Count("session.tabs.list"); c != 1 {
		t.Fatalf("asked an Orca without the tab model %d times", c)
	}
}

// The assistant's tab name (#15674) lands on an Orca tab only while Orca, asked then, still shows the name the
// assistant saw: a rename made meanwhile (the owner's in Orca's window, or Persona's own) wins, and nothing is renamed.
// The name asked for is the phone tab model's (session.tabs.list): on an Orca with no window the layout keeps the tab's
// first title whatever it is renamed to (as the fake does, after Orca 1.4.277).
func TestOrcaGuardedTabRename(t *testing.T) {
	n, o, _ := orcaSession(t)
	rename := func(expected, name string) error {
		_, err := request(t, n, assistantwire.DeviceSessionRenameTabIfCurrent, map[string]any{"session_id": sidA,
			"tab_id": "tab1", "expected_tab_name": expected, "tab_name": name})
		return err
	}
	if err := rename("tab tab1", "async-login"); err != nil {
		t.Fatal(err)
	}
	if tr := o.Trace("terminal.rename"); strings.Join(tr, "|") != `terminal.rename {"terminal":"term_1","title":"async-login"}` {
		t.Fatalf("rename calls %v", tr)
	}
	if tr := o.Trace("session.tabs.list"); len(tr) != 1 || tr[0] != `session.tabs.list {"worktree":"id:repo::`+orcaCwd+`"}` {
		t.Fatalf("tab names read %v", tr)
	}
	o.Reset()
	// The layout still says "tab tab1"; the tab is called async-login now.
	if err := rename("tab tab1", "late-name"); err == nil {
		t.Fatal("renamed over a name given since, which only the layout did not show")
	}
	o.RenameTab("tab1", "owner-chosen")
	if err := rename("async-login", "late-name"); err == nil {
		t.Fatal("renamed over the owner's name")
	}
	if n := o.Count("terminal.rename"); n != 0 {
		t.Fatalf("renamed %d times over a newer name", n)
	}
	if inv := o.Invalid(); len(inv) != 0 {
		t.Errorf("params Orca would refuse: %v", inv)
	}

	// An Orca without the phone's tab model: its layout's titles, read now, decide.
	n, o, _ = orcaSession(t)
	o.Lack("session.tabs.list")
	if err := rename("tab tab1", "async-login"); err != nil {
		t.Fatalf("an Orca without session.tabs.list: %v", err)
	}
	if err := rename("other", "late-name"); err == nil {
		t.Fatal("renamed a tab the layout does not show under the expected name")
	}
	if n := o.Count("terminal.rename"); n != 1 {
		t.Fatalf("%d renames on an Orca without session.tabs.list", n)
	}
}
