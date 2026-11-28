package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The owner's choice of the memory extension's standing budget (assistant.memory_extension_standing_bytes): 4 KB
// (the default), 8 KB or 16 KB, in bytes (memory_assemble.go "Budgets", docs/assistant/health.md "Budgets").

// standingBudget with each choice: the choice when the extension states none; a stated budget up to the larger of
// the choice and 8192; an unknown choice is the default.
func TestMemoryExtensionStandingBudgetChoice(t *testing.T) {
	for _, c := range []struct {
		owner, stated, want int
	}{
		// The default choice keeps the budgets from before it existed.
		{4096, 0, 4096}, {4096, -1, 4096}, {4096, 1000, 1000}, {4096, 6144, 6144}, {4096, 8192, 8192}, {4096, 20000, 8192},
		{8192, 0, 8192}, {8192, 1000, 1000}, {8192, 8192, 8192}, {8192, 8193, 8192}, {8192, 20000, 8192},
		{16384, 0, 16384}, {16384, 1000, 1000}, {16384, 8192, 8192}, {16384, 12000, 12000}, {16384, 16384, 16384}, {16384, 1 << 30, 16384},
		// Not a choice there is: the default, never another bound.
		{0, 0, 4096}, {-1, 0, 4096}, {5000, 0, 4096}, {32768, 0, 4096}, {32768, 20000, 8192},
	} {
		if got := (memoryExtensionBatch{StandingBytes: c.stated}).standingBudget(c.owner); got != c.want {
			t.Errorf("standingBudget(owner %d, stated %d) = %d, want %d", c.owner, c.stated, got, c.want)
		}
	}
}

// The assembler takes as many bytes of standing entries as each choice allows, newest (most important) first, never
// more than 16384 whatever it is told.
func TestMemoryExtensionStandingChoiceAssembles(t *testing.T) {
	ext := budgetEntries(memoryExtensionIDPrefix+"qa-", 40, 500) // 20000 bytes
	for _, c := range []struct {
		budget, kept int
	}{
		{4096, 8},   // 4000 of 4096
		{8192, 16},  // 8000 of 8192
		{16384, 32}, // 16000 of 16384
		{1 << 20, 32},
	} {
		f := standingMain
		f.ExtensionStanding = c.budget
		a := assembleEntries(ext, f, knownGeneral)
		if got, want := entryIDList(a.Entries), budgetIDs(memoryExtensionIDPrefix+"qa-", 40-c.kept, 40); !slices.Equal(got, want) {
			t.Errorf("budget %d: %d entries %v, want %d", c.budget, len(got), got, c.kept)
		}
	}
}

// Multi-byte text at the boundary: an entry is carried whole or left out, never cut, so the section stays valid UTF-8
// and within the budget to the byte; one exactly at the budget fits, one byte over does not.
func TestMemoryExtensionStandingChoiceUTF8Boundary(t *testing.T) {
	for _, budget := range memoryExtensionStandingOptions {
		f := standingMain
		f.ExtensionStanding = budget
		// 3-byte runes: an entry of exactly budget bytes is carried alone.
		exact := proto.AssistantEntry{ID: memoryExtensionIDPrefix + "qa-exact", Text: strings.Repeat("记", budget/3) + strings.Repeat("a", budget%3)}
		if len(exact.Text) != budget {
			t.Fatalf("exact entry is %d bytes, want %d", len(exact.Text), budget)
		}
		if got := entryIDList(assembleEntries([]proto.AssistantEntry{exact}, f, knownGeneral).Entries); !slices.Equal(got, []string{exact.ID}) {
			t.Errorf("budget %d: the entry of exactly the budget = %v", budget, got)
		}
		// One more 4-byte rune past it: left out whole, the smaller older one still goes.
		over := proto.AssistantEntry{ID: memoryExtensionIDPrefix + "qa-over", Text: exact.Text[:budget-3] + "😀"}
		small := proto.AssistantEntry{ID: memoryExtensionIDPrefix + "qa-small", Text: "记忆"}
		a := assembleEntries([]proto.AssistantEntry{small, over}, f, knownGeneral)
		if got := entryIDList(a.Entries); !slices.Equal(got, []string{small.ID}) {
			t.Errorf("budget %d: one byte over = %v, want only the small one", budget, got)
		}
		// Mixed CJK entries: whole, valid, within the budget.
		var mixed []proto.AssistantEntry
		for i := range 200 {
			mixed = append(mixed, proto.AssistantEntry{ID: memoryExtensionIDPrefix + "qa-cjk-" + strconv.Itoa(i), Text: strings.Repeat("原则画像", 10+i%7) + "😀"})
		}
		total := 0
		for _, e := range assembleEntries(mixed, f, knownGeneral).Entries {
			if !utf8.ValidString(e.Text) {
				t.Errorf("budget %d: %s is not valid UTF-8", budget, e.ID)
			}
			total += len(e.Text)
		}
		if total > budget || total < budget-200 {
			t.Errorf("budget %d: carried %d bytes", budget, total)
		}
	}
}

// End to end through the main conversation's context (Context → withStandingMemory): with the default choice the
// standing section carries 4 KB of the extension's standing entries, after a change to 8 KB or 16 KB as much from
// the next message, with no restart; an extension that states a budget keeps it under the default choice.
func TestMemoryExtensionStandingChoiceEndToEnd(t *testing.T) {
	ctx := context.Background()
	s, src := memoryExtensionService(t, true)
	var ext []memoryExtensionEntry
	for i := range 40 { // 40 × 500 = 20000 bytes, the oldest first
		ext = append(ext, memoryExtensionEntry{ID: fmt.Sprintf("qa-standing-%02d", i), Text: fmt.Sprintf("qa-%02d: ", i) + strings.Repeat("r", 500-len("qa-00: ")), Standing: true})
	}
	carried := func(t *testing.T) int {
		t.Helper()
		section := s.standingMemorySection(ctx, "qa-machine", "qa-main")
		ctxText := s.Context(ctx, ContextParams{MachineID: "qa-machine", SessionID: "qa-main"}).Text
		n := 0
		for _, e := range ext {
			if strings.Contains(section, e.Text) {
				n++
				if !strings.Contains(ctxText, e.Text) {
					t.Errorf("%s is in the section but not in the context", e.ID)
				}
			}
		}
		// The newest are kept.
		if n > 0 && !strings.Contains(section, ext[len(ext)-1].Text) {
			t.Errorf("the newest entry is left out")
		}
		return n
	}
	src.batch = memoryExtensionBatch{Entries: ext, Tagged: true}
	if n := carried(t); n != 8 {
		t.Errorf("default choice carries %d entries, want 8 (4096 bytes)", n)
	}
	for _, c := range []struct{ choice, want int }{{8192, 16}, {16384, 32}, {4096, 8}} {
		choice := c.choice
		setSettings(t, s, func(v *Settings) { v.MemoryExtensionStandingBytes = &choice })
		if n := carried(t); n != c.want {
			t.Errorf("choice %d carries %d entries, want %d", c.choice, n, c.want)
		}
	}
	// Under the default choice an extension that states 8192 keeps it, as before the choice.
	src.batch = memoryExtensionBatch{Entries: ext, Tagged: true, StandingBytes: 8192}
	if n := carried(t); n != 16 {
		t.Errorf("stated 8192 under the default choice carries %d, want 16", n)
	}
	// Stated 16384: 8192 under the default choice, 16384 once chosen.
	src.batch.StandingBytes = 16384
	if n := carried(t); n != 16 {
		t.Errorf("stated 16384 under the default choice carries %d, want 16", n)
	}
	sixteen := 16384
	setSettings(t, s, func(v *Settings) { v.MemoryExtensionStandingBytes = &sixteen })
	if n := carried(t); n != 32 {
		t.Errorf("stated 16384 with 16 KB chosen carries %d, want 32", n)
	}
	// The standing channel's own assembly (standingMemory) follows the choice too.
	a, err := s.standingMemory(ctx, "qa-main")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, id := range entryIDList(a.Entries) {
		if strings.HasPrefix(id, memoryExtensionIDPrefix) {
			n++
		}
	}
	if n != 32 {
		t.Errorf("standingMemory carries %d extension entries, want 32", n)
	}
}

// GET/PUT /api/v1/assistant: 4096 by default; 4096, 8192 and 16384 saved and read back; anything else refused with
// 400 and nothing saved; a change names its manager.
func TestMemoryExtensionStandingSettingHTTP(t *testing.T) {
	s := newServiceOn(t, newFakeHub().withMachine(), &memStore{})
	get := func() assistantView {
		t.Helper()
		w, v := permissionSettingsRequest(t, s, http.MethodGet, "/api/v1/assistant", s.managerIdentity(), "full", "")
		if w.Code != http.StatusOK {
			t.Fatalf("GET: %d %s", w.Code, w.Body.String())
		}
		return v
	}
	put := func(body string, want int) assistantView {
		t.Helper()
		w, v := permissionSettingsRequest(t, s, http.MethodPut, "/api/v1/assistant", s.managerIdentity(), "full", body)
		if w.Code != want {
			t.Fatalf("PUT %s: %d, want %d: %s", body, w.Code, want, w.Body.String())
		}
		if want == http.StatusBadRequest && !strings.Contains(w.Body.String(), "4096, 8192 or 16384") {
			t.Errorf("PUT %s: the refusal does not name the choices: %s", body, w.Body.String())
		}
		return v
	}
	if got := get().MemoryExtensionStandingBytes; got != 4096 {
		t.Fatalf("default = %d, want 4096", got)
	}
	for _, body := range []string{
		`{"memory_extension_standing_bytes":0}`, `{"memory_extension_standing_bytes":-4096}`,
		`{"memory_extension_standing_bytes":4095}`, `{"memory_extension_standing_bytes":4097}`,
		`{"memory_extension_standing_bytes":6144}`, `{"memory_extension_standing_bytes":12288}`,
		`{"memory_extension_standing_bytes":32768}`, `{"memory_extension_standing_bytes":8}`,
	} {
		put(body, http.StatusBadRequest)
	}
	// Not a number at all: refused by the decoder.
	for _, body := range []string{`{"memory_extension_standing_bytes":"8192"}`, `{"memory_extension_standing_bytes":"8KB"}`, `{"memory_extension_standing_bytes":8192.5}`} {
		w, _ := permissionSettingsRequest(t, s, http.MethodPut, "/api/v1/assistant", s.managerIdentity(), "full", body)
		if w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s: %d, want 400", body, w.Code)
		}
	}
	if got := get().MemoryExtensionStandingBytes; got != 4096 {
		t.Fatalf("after refusals = %d, want 4096", got)
	}
	// Without the manager it is for: refused.
	if w := putAssistantSettingsReq(s, `{"memory_extension_standing_bytes":8192}`, false); w.Code != http.StatusBadRequest {
		t.Errorf("PUT without the manager: %d, want 400", w.Code)
	}
	for _, b := range []int{8192, 16384, 4096} {
		if got := put(fmt.Sprintf(`{"memory_extension_standing_bytes":%d}`, b), http.StatusOK).MemoryExtensionStandingBytes; got != b {
			t.Errorf("PUT %d answered %d", b, got)
		}
		if got := get().MemoryExtensionStandingBytes; got != b {
			t.Errorf("after PUT %d, GET = %d", b, got)
		}
		if got := s.memoryExtensionStandingBytes(context.Background()); got != b {
			t.Errorf("after PUT %d, in effect %d", b, got)
		}
	}
	// Another setting's PUT leaves it alone.
	put(`{"memory_extension_standing_bytes":16384}`, http.StatusOK)
	put(`{"recent_task_chats":true}`, http.StatusOK)
	if got := get().MemoryExtensionStandingBytes; got != 16384 {
		t.Errorf("after another PUT = %d, want 16384", got)
	}
}

// persona_settings: listed with get and set; set takes 4096, 8192 or 16384 as a JSON number and refuses anything
// else; an unchanged value is a no-op; a change shows its card and applies.
func TestMemoryExtensionStandingSettingTool(t *testing.T) {
	ctx := context.Background()
	s := newServiceOn(t, newFakeHub().withMachine(), &memStore{})
	var st ownSetting
	for _, o := range s.ownSettings() {
		if o.Key == "assistant.memory_extension_standing_bytes" {
			st = o
		}
	}
	if st.Key == "" || !slices.Contains(st.Actions, "get") || !slices.Contains(st.Actions, "set") {
		t.Fatalf("persona_settings lists %+v", st.Setting)
	}
	if !strings.Contains(st.Value, "4096, 8192 or 16384") || !strings.Contains(st.About, "bytes") || !strings.Contains(st.About, "4 KB") {
		t.Errorf("description: about %q, value %q", st.About, st.Value)
	}
	if got, err := st.get(ctx); err != nil || got != "4096" {
		t.Fatalf("get = %q, %v; want 4096", got, err)
	}
	for _, raw := range []string{`0`, `4095`, `5000`, `12288`, `32768`, `-1`, `"8192"`, `"8KB"`, `null`, `8192.5`, `true`} {
		if _, err := st.set(ctx, json.RawMessage(raw)); err == nil || !strings.Contains(err.Error(), "4096, 8192 or 16384") {
			t.Errorf("set %s: %v, want refused", raw, err)
		}
	}
	if ch, err := st.set(ctx, json.RawMessage(`4096`)); err != nil || ch == nil || ch.apply != nil {
		t.Fatalf("unchanged set: %+v, %v", ch, err)
	}
	for _, b := range []int{16384, 8192, 4096} {
		ch, err := st.set(ctx, json.RawMessage(strconv.Itoa(b)))
		if err != nil || ch == nil || ch.apply == nil {
			t.Fatalf("set %d: %+v, %v", b, ch, err)
		}
		if card := ch.cardText(i18n.Default); !strings.Contains(card, strconv.Itoa(b/1024)+" KB") {
			t.Errorf("card for %d = %q", b, card)
		}
		if _, err := ch.apply(ctx); err != nil {
			t.Fatalf("apply %d: %v", b, err)
		}
		if got, _ := st.get(ctx); got != strconv.Itoa(b) {
			t.Errorf("after set %d, get = %s", b, got)
		}
	}
}
