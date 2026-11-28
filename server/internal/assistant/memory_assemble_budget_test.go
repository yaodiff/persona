package assistant

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extmgr"
	"github.com/nwverse/persona/server/internal/proto"
)

// The ledger's and the memory extension's budgets are kept apart (#15312, memory_assemble.go "Budgets").

// budgetEntries are n entries of size bytes each, oldest first, with ids prefix-0 … prefix-(n-1).
func budgetEntries(prefix string, n, size int) []proto.AssistantEntry {
	var out []proto.AssistantEntry
	for i := range n {
		out = append(out, proto.AssistantEntry{ID: prefix + strconv.Itoa(i), Text: "qa alpha beta " + strings.Repeat("q", size-len("qa alpha beta "))})
	}
	return out
}

// budgetIDs are ids prefix-from … prefix-(to-1).
func budgetIDs(prefix string, from, to int) []string {
	var out []string
	for i := from; i < to; i++ {
		out = append(out, prefix+strconv.Itoa(i))
	}
	return out
}

// knownGeneral sees every entry as a known general entry of the main space: in a main-space conversation, its own.
func knownGeneral(proto.AssistantEntry) infoUse { return infoUse{Known: true} }

var standingMain = assembleFor{Role: assembleStanding, SpaceKnown: true}

// The extension's standing entries take their own 4096 bytes by default, whatever the ledger's take, and the ledger's
// keep their 1536 bytes, whatever the extension's take: neither crowds the other out.
func TestMemoryStandingBudgetsApart(t *testing.T) {
	ext := budgetEntries(memoryExtensionIDPrefix+"qa-", 8, 500) // 4000 bytes: all fit the extension's 4096
	ledger := budgetEntries("entry-qa-", 3, 600)                // 1800 bytes: the newest two fit the ledger's 1536
	// The assembler's order (memorySources): the extension's, then the ledger's, so the ledger's are spent first.
	a := assembleEntries(append(slices.Clone(ext), ledger...), standingMain, knownGeneral)
	want := append(budgetIDs(memoryExtensionIDPrefix+"qa-", 0, 8), budgetIDs("entry-qa-", 1, 3)...)
	if got := entryIDList(a.Entries); !slices.Equal(got, want) {
		t.Errorf("standing = %v, want %v", got, want)
	}
	// The other way round: the extension's spent first leave the ledger's budget whole.
	a = assembleEntries(append(slices.Clone(ledger), ext...), standingMain, knownGeneral)
	want = append(budgetIDs("entry-qa-", 1, 3), budgetIDs(memoryExtensionIDPrefix+"qa-", 0, 8)...)
	if got := entryIDList(a.Entries); !slices.Equal(got, want) {
		t.Errorf("standing, the ledger's oldest = %v, want %v", got, want)
	}
	// An extension entry from elsewhere (personal, general) spends the same extension budget, not the ledger's
	// personal one.
	elsewhere := func(e proto.AssistantEntry) infoUse {
		if strings.HasPrefix(e.ID, memoryExtensionIDPrefix) {
			return infoUse{Known: true, SourceTrack: "qa-plain"}
		}
		return infoUse{Known: true}
	}
	extElsewhere := budgetEntries(memoryExtensionIDPrefix+"qa-", 8, 500)
	for i := range extElsewhere {
		extElsewhere[i].Scope.Personal = true
	}
	a = assembleEntries(append(extElsewhere, ledger...), standingMain, elsewhere)
	want = append(budgetIDs(memoryExtensionIDPrefix+"qa-", 0, 8), budgetIDs("entry-qa-", 1, 3)...)
	if got := entryIDList(a.Entries); !slices.Equal(got, want) {
		t.Errorf("standing, the extension's from elsewhere = %v, want %v", got, want)
	}
}

// Past the extension's budget the existing rule holds: the most important (newest) first, an entry that does not fit
// is left out and a smaller, older one that still fits goes.
func TestMemoryExtensionStandingOverBudget(t *testing.T) {
	small := proto.AssistantEntry{ID: memoryExtensionIDPrefix + "qa-small", Text: strings.Repeat("s", 90)}
	entries := append([]proto.AssistantEntry{small}, budgetEntries(memoryExtensionIDPrefix+"qa-", 10, 500)...)
	a := assembleEntries(entries, standingMain, knownGeneral)
	// Newest first: qa-9 … qa-2 take 4000 bytes, qa-1 and qa-0 do not fit the 96 left, qa-small does.
	want := append([]string{small.ID}, budgetIDs(memoryExtensionIDPrefix+"qa-", 2, 10)...)
	if got := entryIDList(a.Entries); !slices.Equal(got, want) {
		t.Errorf("over the default budget = %v, want %v", got, want)
	}
}

// The budget an extension states holds, capped at 8192 bytes; none or 0 is the default (the owner's default choice,
// 4096: the budgets from before the choice existed).
func TestMemoryExtensionStandingBudgetStated(t *testing.T) {
	ext := budgetEntries(memoryExtensionIDPrefix+"qa-", 20, 500) // 10000 bytes
	for _, c := range []struct {
		stated, kept int
	}{
		{0, 8},      // 4096: 8 of 500
		{1000, 2},   // 1000: 2
		{6144, 12},  // 6144: 12
		{8192, 16},  // 8192: 16
		{20000, 16}, // capped at 8192
	} {
		b := memoryExtensionBatch{StandingBytes: c.stated}
		f := standingMain
		f.ExtensionStanding = b.standingBudget(memoryExtensionStandingDefault)
		a := assembleEntries(ext, f, knownGeneral)
		if got, want := entryIDList(a.Entries), budgetIDs(memoryExtensionIDPrefix+"qa-", 20-c.kept, 20); !slices.Equal(got, want) {
			t.Errorf("stated %d: %v, want %v", c.stated, got, want)
		}
	}
	for stated, want := range map[int]int{-1: 4096, 0: 4096, 800: 800, 4096: 4096, 8192: 8192, 8193: 8192} {
		if got := (memoryExtensionBatch{StandingBytes: stated}).standingBudget(memoryExtensionStandingDefault); got != want {
			t.Errorf("standingBudget(%d) = %d, want %d", stated, got, want)
		}
	}
}

// A hand-off keeps the ledger's and the extension's budgets apart too: the ledger's entries no longer spend the
// extension's.
func TestMemoryHandOffBudgetsApart(t *testing.T) {
	ext := budgetEntries(memoryExtensionIDPrefix+"qa-", 3, 600)
	ledger := budgetEntries("entry-qa-", 3, 600)
	f := assembleFor{Role: assembleHandOff, Query: "qa alpha beta"}
	a := assembleEntries(append(ext, ledger...), f, knownGeneral)
	want := append(budgetIDs(memoryExtensionIDPrefix+"qa-", 1, 3), budgetIDs("entry-qa-", 1, 3)...)
	if got := entryIDList(a.Entries); !slices.Equal(got, want) {
		t.Errorf("hand-off = %v, want %v", got, want)
	}
}

// The budget an extension states reaches the assembler: the largest of its pages and of the sources, kept by
// standingOnly and about.
func TestMemoryExtensionStandingBytesCarried(t *testing.T) {
	page := func(n int) wire.MemoryEntriesResult {
		return wire.MemoryEntriesResult{Tagged: true, StandingBytes: n,
			Entries: []wire.MemoryEntry{{ID: "qa-" + strconv.Itoa(n), Text: "qa: entry", Status: "active", Standing: true}}}
	}
	if got := memoryExtensionBatchOf([]wire.MemoryEntriesResult{page(2048), page(0), page(6144)}).StandingBytes; got != 6144 {
		t.Errorf("pages' standing_bytes = %d, want 6144", got)
	}
	b, failed := memoryExtensionMerge([]extmgr.MemorySourceRead{
		{Name: "qa-one", Pages: []wire.MemoryEntriesResult{page(1000)}},
		{Name: "qa-two", Pages: []wire.MemoryEntriesResult{page(5000)}},
	})
	if failed || b.StandingBytes != 5000 {
		t.Errorf("merged standing_bytes = %d (failed %v), want 5000", b.StandingBytes, failed)
	}
	if got := b.standingOnly().StandingBytes; got != 5000 {
		t.Errorf("standingOnly kept %d", got)
	}
	if got := b.about("entry").StandingBytes; got != 5000 {
		t.Errorf("about kept %d", got)
	}
}

// End to end: a main conversation's standing channel takes the extension's standing entries within 4096 bytes beside
// the ledger's, where before both shared 1536.
func TestMemoryExtensionStandingDefaultEndToEnd(t *testing.T) {
	ctx := context.Background()
	s, src := memoryExtensionService(t, true)
	var ext []memoryExtensionEntry
	for i := range 9 { // 9 × 450 = 4050 bytes
		ext = append(ext, memoryExtensionEntry{ID: "qa-standing-" + strconv.Itoa(i), Text: "qa: " + strings.Repeat("r", 446), Standing: true})
	}
	src.batch = memoryExtensionBatch{Entries: ext, Tagged: true}
	s.memoryEntries = &memEntrySource{entries: []proto.AssistantEntry{entryQAPlain, entryQAMain}}
	a, err := s.standingMemory(ctx, "qa-main")
	if err != nil {
		t.Fatal(err)
	}
	got := entryIDList(a.Entries)
	for _, e := range ext {
		if !slices.Contains(got, memoryExtensionIDPrefix+e.ID) {
			t.Errorf("standing = %v, missing %s", got, e.ID)
		}
	}
	if !slices.Contains(got, entryQAPlain.ID) || !slices.Contains(got, entryQAMain.ID) {
		t.Errorf("standing = %v, missing the ledger's entries", got)
	}
	// The snapshot's section carries them all as well (assembleExtension).
	b := src.batch.standingOnly()
	if n := len(s.assembleExtension(ctx, s.assembleForConversation(ctx, "qa-main", assembleStanding), b).Entries); n != len(ext) {
		t.Errorf("standing section carries %d of %d", n, len(ext))
	}
}
