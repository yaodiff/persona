package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extmgr"
	"github.com/nwverse/persona/server/internal/proto"
)

// fakeMemoryReader is the extensions' manager as the memory source reads it: out for every read, counted.
// onRead, when set, is told the number of each read while it runs (outside f's lock).
type fakeMemoryReader struct {
	mu     sync.Mutex
	gen    uint64
	reads  int
	out    []extmgr.MemorySourceRead
	onRead func(n int)
}

func (f *fakeMemoryReader) MemorySourceReads(_ context.Context, b extmgr.MemoryReadBounds) []extmgr.MemorySourceRead {
	f.mu.Lock()
	f.reads++
	n, out, onRead := f.reads, f.out, f.onRead
	f.mu.Unlock()
	if onRead != nil {
		onRead(n)
	}
	if b.PageLimit != memoryExtensionPageLimit || b.ReadTimeout != memoryExtensionReadTimeout || b.TotalMost != memoryExtensionTotalMost ||
		b.CallTimeout != memoryExtensionCallTimeout || b.AnswerMost != memoryExtensionAnswerMost {
		return []extmgr.MemorySourceRead{{Err: errors.New("unexpected bounds")}}
	}
	return out
}

func (f *fakeMemoryReader) MemorySourcesGeneration() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.gen
}

// readsNow is how many reads f answered, once c's background read (if any) is done.
func (f *fakeMemoryReader) readsNow(t *testing.T, c *memoryExtensionCache) int {
	t.Helper()
	c.mu.Lock()
	wait := c.reading
	c.mu.Unlock()
	if wait != nil {
		select {
		case <-wait:
		case <-time.After(5 * time.Second):
			t.Fatal("the background read did not finish in 5s")
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.reads
}

// qaMemoryPage is one tagged page of a fake memory extension's entries.
func qaMemoryPage(entries ...wire.MemoryEntry) []wire.MemoryEntriesResult {
	return []wire.MemoryEntriesResult{{Tagged: true, Entries: entries}}
}

// The sources' reads merged: a failed one gives nothing and makes the batch failed; an untagged one's entries are put
// in no track (not known, the strictest rule) while the tagged one's keep theirs; an id given before is dropped; the
// names are those of the extensions that gave entries.
func TestMemoryExtensionMerge(t *testing.T) {
	b, failed := memoryExtensionMerge([]extmgr.MemorySourceRead{
		{Extension: "qa-memory", Name: "QA Memory", Pages: qaMemoryPage(wire.MemoryEntry{ID: "qa-a", Text: "qa: a", Status: "active", OriginSpace: "qa-plain"})},
		{Extension: "qa-old", Name: "QA Old", Pages: []wire.MemoryEntriesResult{{Entries: []wire.MemoryEntry{
			{ID: "qa-a", Text: "qa: again", Status: "active"}, {ID: "qa-b", Text: "qa: b", Status: "active"}}}}},
		{Extension: "qa-broken", Name: "QA Broken", Err: errors.New("no answer in 5s")},
	})
	if !failed || !b.Tagged || len(b.Entries) != 2 || strings.Join(b.Names, ",") != "QA Memory,QA Old" {
		t.Fatalf("%+v failed %v", b, failed)
	}
	if b.Entries[0].ID != "qa-a" || b.Entries[0].OriginSpace != "qa-plain" || b.Entries[1].ID != "qa-b" || b.Entries[1].OriginSpace != entryTrackUnknown {
		t.Fatalf("%+v", b.Entries)
	}
	s, _ := memoryExtensionService(t, true)
	if u := s.memoryExtensionUses(context.Background(), true)(memoryExtensionBatch{Entries: b.Entries[1:]}.assistantEntries()[0]); u.Known || !u.sensitive() {
		t.Errorf("an untagged source's entry = %+v, want not known", u)
	}
	if b, failed := memoryExtensionMerge(nil); failed || len(b.Entries) != 0 {
		t.Errorf("no sources: %+v %v", b, failed)
	}
}

// The cache reads once per minute and generation, again sooner after a read in which a source failed, and its batch
// is what the sources answered.
func TestMemoryExtensionCache(t *testing.T) {
	ctx := context.Background()
	r := &fakeMemoryReader{out: []extmgr.MemorySourceRead{{Extension: "qa-memory", Name: "QA Memory",
		Pages: qaMemoryPage(wire.MemoryEntry{ID: "qa-a", Text: "qa: a", Status: "active", Standing: true})}}}
	c := &memoryExtensionCache{r: r}
	for range 3 {
		if b, err := c.MemoryExtensionEntries(ctx, true); err != nil || len(b.Entries) != 1 || !b.Entries[0].Standing {
			t.Fatalf("%+v %v", b, err)
		}
	}
	if n := r.readsNow(t, c); n != 1 {
		t.Fatalf("read %d times within a minute, want 1", n)
	}
	r.mu.Lock()
	r.gen++
	r.mu.Unlock()
	_, _ = c.MemoryExtensionEntries(ctx, true)
	if n := r.readsNow(t, c); n != 2 {
		t.Fatalf("an extension changed: read %d times, want 2", n)
	}
	// A minute later the kept batch is used at once and read again in the background.
	c.mu.Lock()
	c.at = time.Now().Add(-memoryExtensionFresh - time.Second)
	c.mu.Unlock()
	if b, err := c.MemoryExtensionEntries(ctx, true); err != nil || len(b.Entries) != 1 {
		t.Fatalf("a stale batch is used while read again: %+v %v", b, err)
	}
	if n := r.readsNow(t, c); n != 3 {
		t.Fatalf("a minute later: read %d times, want 3", n)
	}
	r.mu.Lock()
	r.out = []extmgr.MemorySourceRead{{Extension: "qa-memory", Err: errors.New("not_ready: loading")}}
	r.gen++
	r.mu.Unlock()
	if b, _ := c.MemoryExtensionEntries(ctx, true); len(b.Entries) != 0 {
		t.Fatalf("a failed source gives nothing: %+v", b)
	}
	if n := r.readsNow(t, c); n != 4 {
		t.Fatalf("a failed read: read %d times, want 4", n)
	}
	c.mu.Lock()
	c.at = time.Now().Add(-memoryExtensionRetry - time.Second)
	c.mu.Unlock()
	_, _ = c.MemoryExtensionEntries(ctx, true)
	if n := r.readsNow(t, c); n != 5 {
		t.Fatalf("after a failure: read %d times, want 5", n)
	}
	// The extensions' manager is a source as soon as the service is given it; a fake that reads none is not.
	s := newTestService(t, newFakeHub())
	s.setMemorySources(struct{}{})
	if s.memoryExtension != nil {
		t.Fatal("a manager without memory sources became one")
	}
	s.setMemorySources(r)
	if _, ok := s.memoryExtension.(*memoryExtensionCache); !ok {
		t.Fatalf("source %T", s.memoryExtension)
	}
}

// The standing context never waits: without a batch of the current generation it takes none and starts the read,
// whose batch the next message takes. A read that ends on an older generation (an extension changed while it ran) is
// followed by one of the current generation within the same wait, not answered with nothing.
func TestMemoryExtensionCacheNoWaitAndGeneration(t *testing.T) {
	ctx := context.Background()
	r := &fakeMemoryReader{out: []extmgr.MemorySourceRead{{Extension: "qa-memory", Name: "QA Memory",
		Pages: qaMemoryPage(wire.MemoryEntry{ID: "qa-a", Text: "qa: a", Status: "active", Standing: true})}}}
	c := &memoryExtensionCache{r: r}
	if b := c.CachedMemoryExtensionEntries(ctx); len(b.Entries) != 0 {
		t.Fatalf("no batch yet: %+v", b)
	}
	if n := r.readsNow(t, c); n != 1 {
		t.Fatalf("the standing context started %d reads, want 1", n)
	}
	if b := c.CachedMemoryExtensionEntries(ctx); len(b.Entries) != 1 {
		t.Fatalf("the next message: %+v", b)
	}
	// An extension changes while the next read runs: that read is of the older generation, so a second is made.
	r.mu.Lock()
	r.gen++
	r.onRead = func(n int) {
		if n == 2 {
			r.mu.Lock()
			r.gen++
			r.mu.Unlock()
		}
	}
	r.mu.Unlock()
	if b, err := c.MemoryExtensionEntries(ctx, true); err != nil || len(b.Entries) != 1 {
		t.Fatalf("after a change during the read: %+v %v", b, err)
	}
	if n := r.readsNow(t, c); n != 3 {
		t.Fatalf("read %d times, want 3 (the one of the older generation, then the current)", n)
	}
}

// The public query handler applies explicit tags to mapped extension fields. Full-term matches come first, and
// partial multi-term matches backfill remaining budget by hit count and rank. Category matches still pass through
// the conversation's visibility checks.
func TestDecisionsQueryMemoryTags(t *testing.T) {
	s, _, st, _ := memoryAssembleService(t)
	s.memoryEntries = &memEntrySource{}
	s.setStore(func() Store { return &entriesMemStore{infoCategoryMemStore: st} })
	page := wire.MemoryEntriesResult{Tagged: true, Entries: []wire.MemoryEntry{
		{ID: "qa-workflow", Text: "qa: release checks include a rollback rehearsal", Facet: "workflow", Subjects: []string{"project-alpha"}, Status: "active"},
		{ID: "qa-unrelated", Text: "qa: prepare a weekly note", Facet: "judgement", Status: "active"},
		{ID: "qa-finance", Text: "qa: private budget", Facet: "workflow", Categories: []string{categoryFinance}, OriginSpace: "qa-finance", Status: "active"},
	}}
	s.memoryExtension = &memExtensionSource{batch: memoryExtensionBatchOf([]wire.MemoryEntriesResult{page})}
	for _, tc := range []struct{ tag, want string }{
		{"workflow", "qa-workflow"}, {"WORKFLOW", "qa-workflow"}, {"judgement", "qa-unrelated"},
		{"release rollback", "qa-workflow"}, {"  ROLLBACK \t RELEASE  ", "qa-workflow"},
		{"project-al", "qa-workflow"}, {"project-al workflow rollback", "qa-workflow"},
		{"release absent", "qa-workflow"}, {"nonexistent", ""}, {"finance", ""},
	} {
		t.Run(tc.tag, func(t *testing.T) {
			args, _ := json.Marshal(map[string]string{"tag": tc.tag})
			raw, err := s.toolEntries(t.Context(), "qa-machine", "qa-main", "decisions_query", args)
			if err != nil {
				t.Fatal(err)
			}
			var out struct {
				Memory []recalledMemory `json:"memory"`
			}
			if err := json.Unmarshal([]byte(raw), &out); err != nil {
				t.Fatal(err)
			}
			if tc.want == "" {
				if len(out.Memory) != 0 {
					t.Fatalf("unrelated or hidden entries returned: %s", raw)
				}
			} else if len(out.Memory) != 1 || out.Memory[0].ID != memoryExtensionIDPrefix+tc.want {
				t.Fatalf("tag %q: %s", tc.tag, raw)
			}
		})
	}
	// An explicit category filter does not grant the category. Once granted, the mapped category is searchable.
	if err := s.tagConversation(t.Context(), "qa-main", []string{categoryFinance}, infoTaggedByModel, ""); err != nil {
		t.Fatal(err)
	}
	raw, err := s.toolEntries(t.Context(), "qa-machine", "qa-main", "decisions_query", json.RawMessage(`{"tag":"FINANCE workflow"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Memory []recalledMemory `json:"memory"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Memory) != 2 || out.Memory[0].ID != memoryExtensionIDPrefix+"qa-finance" || out.Memory[1].ID != memoryExtensionIDPrefix+"qa-workflow" {
		t.Fatalf("category recall: %s", raw)
	}
	for _, args := range []string{`{"tag":"workflow","by":"user"}`, `{"tag":"workflow","trust":"user_verbatim"}`, `{"tag":"workflow","status":"superseded"}`} {
		raw, err := s.toolEntries(t.Context(), "qa-machine", "qa-main", "decisions_query", json.RawMessage(args))
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Memory  []recalledMemory `json:"memory"`
			Omitted string           `json:"memory_omitted"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			t.Fatal(err)
		}
		if len(out.Memory) != 0 || out.Omitted != "ledger_filters" {
			t.Fatalf("filter semantics: %s", raw)
		}
	}
}

// decisions_query carries the memory extension's entries in its memory field: the main space's and general ones, on
// the first page only, and none when a filter the extension's entries have no field for is asked.
func TestDecisionsQueryMemoryField(t *testing.T) {
	ctx := context.Background()
	s, _, st, _ := memoryAssembleService(t)
	s.memoryEntries = &memEntrySource{}
	s.memoryExtension = &memExtensionSource{batch: memoryExtensionBatch{Entries: extQAEntries(), Tagged: true}}
	main := entryQAMain
	main.Type = "decision"
	ledger := &entriesMemStore{infoCategoryMemStore: st, entries: []proto.AssistantEntry{main}}
	s.setStore(func() Store { return ledger })
	memory := func(args string) string {
		t.Helper()
		raw, err := s.toolEntries(ctx, "qa-machine", "qa-main", "decisions_query", json.RawMessage(args))
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Entries       []proto.AssistantEntry `json:"entries"`
			Memory        []recalledMemory       `json:"memory"`
			MemoryOmitted string                 `json:"memory_omitted"`
		}
		if err := json.Unmarshal([]byte(raw), &out); err != nil || out.Entries == nil {
			t.Fatalf("decisions_query = %s (%v)", raw, err)
		}
		wantOmitted := args == `{"by":"user"}` || args == `{"trust":"user_verbatim"}` || args == `{"status":"superseded"}`
		if (out.MemoryOmitted == "ledger_filters") != wantOmitted {
			t.Errorf("%s: memory_omitted = %q", args, out.MemoryOmitted)
		}
		var ids []string
		for _, m := range out.Memory {
			if m.Text == "" {
				t.Errorf("an entry without its text: %+v", m)
			}
			ids = append(ids, m.ID)
		}
		return strings.Join(ids, ",")
	}
	want := extID(extQAPlain) + "," + extID(extQAMain)
	for _, args := range []string{`{}`, `{"status":"active"}`} {
		if got := memory(args); got != want {
			t.Errorf("%s: memory = %s, want %s", args, got, want)
		}
	}
	for _, args := range []string{`{"by":"user"}`, `{"trust":"user_verbatim"}`, `{"status":"superseded"}`} {
		if got := memory(args); got != "" {
			t.Errorf("%s: memory = %s, want none", args, got)
		}
	}
}

// A main-space conversation's context carries the standing section on the owner's computer, never the finance entry
// from qa-finance, and nothing on another computer.
func TestContextStandingMemory(t *testing.T) {
	ctx := context.Background()
	s, _ := memoryExtensionService(t, true)
	got := s.Context(ctx, ContextParams{MachineID: "qa-machine", SessionID: "qa-main"}).Text
	if !strings.Contains(got, extQAMain.Text) || strings.Contains(got, extQAFinance.Text) {
		t.Errorf("context = %q", got)
	}
	if got := s.Context(ctx, ContextParams{MachineID: "qa-other", SessionID: "qa-main"}).Text; strings.Contains(got, extQAMain.Text) {
		t.Errorf("another computer = %q", got)
	}
}

// Only the extension's standing entries go to a standing channel; the others come in only by relevance or a recall.
func TestMemoryExtensionStandingOnlyStanding(t *testing.T) {
	ctx := context.Background()
	s, src := memoryExtensionService(t, true)
	named := extQAPlain
	named.Standing = false
	src.batch.Entries = []memoryExtensionEntry{named, extQAMain}
	a, err := s.standingMemory(ctx, "qa-main")
	if err != nil || !slices.Equal(entryIDList(a.Entries), []string{extID(extQAMain)}) {
		t.Errorf("standing = %s (%v), want only the standing entry", entryIDList(a.Entries), err)
	}
	if got := s.standingMemorySection(ctx, "qa-machine", "qa-main"); strings.Contains(got, named.Text) || !strings.Contains(got, extQAMain.Text) {
		t.Errorf("standing section = %q", got)
	}
	var ids []string
	for _, m := range s.recallExtensionMemory(ctx, "qa-main", "") {
		ids = append(ids, m.ID)
	}
	if got, want := strings.Join(ids, ","), extID(named)+","+extID(extQAMain); got != want {
		t.Errorf("recall = %s, want %s", got, want)
	}
}

// The main conversation's standing context: the main space's and general standing entries, never the finance one from
// qa-finance; nothing on a computer that is not the owner's, and nothing without a source or when it failed.
func TestStandingMemorySection(t *testing.T) {
	ctx := context.Background()
	s, src := memoryExtensionService(t, true)
	got := s.standingMemorySection(ctx, "qa-machine", "qa-main")
	if !strings.Contains(got, extQAPlain.Text) || !strings.Contains(got, extQAMain.Text) || strings.Contains(got, extQAFinance.Text) ||
		strings.Contains(got, extQAFinanceUntagged.Text) || strings.Contains(got, extQAUnknown.Text) {
		t.Errorf("main standing = %q", got)
	}
	if got := s.withStandingMemory(ctx, "qa-machine", "qa-main", "- 现在：qa"); !strings.HasPrefix(got, "- 现在：qa\n- ") || !strings.HasSuffix(got, "\n") {
		t.Errorf("after the snapshot = %q", got)
	}
	if got := s.withStandingMemory(ctx, "qa-other", "qa-main", "- 现在：qa"); got != "- 现在：qa" {
		t.Errorf("another computer = %q", got)
	}
	src.batch = memoryExtensionBatch{}
	if got := s.withStandingMemory(ctx, "qa-machine", "qa-main", "- 现在：qa"); got != "- 现在：qa" {
		t.Errorf("no entries = %q", got)
	}
	s.memoryExtension = &memoryExtensionCache{r: &fakeMemoryReader{out: []extmgr.MemorySourceRead{{Extension: "qa-memory", Err: errors.New("no answer in 5s")}}}}
	if got := s.standingMemorySection(ctx, "qa-machine", "qa-main"); got != "" {
		t.Errorf("a failed source = %q", got)
	}
	s.memoryExtension = nil
	if got := s.standingMemorySection(ctx, "qa-machine", "qa-main"); got != "" {
		t.Errorf("no source = %q", got)
	}
}

// The standing memory section in each language (#13586).
func TestGoldenStandingMemory(t *testing.T) {
	ctx := context.Background()
	for _, locale := range []string{"zh-Hans", "en", "ja"} {
		s, f, _, _ := memoryAssembleService(t)
		f.mu.Lock()
		f.hello.Locale = locale
		f.mu.Unlock()
		s.memoryEntries = &memEntrySource{}
		s.memoryExtension = &memExtensionSource{batch: memoryExtensionBatch{Entries: extQAEntries(), Tagged: true}}
		golden(t, "memory_standing."+locale, s.standingMemorySection(ctx, "qa-machine", "qa-main"))
	}
}

// A recall returns the extension's finance entry only once the conversation carries finance, the one from an unknown
// space never; a tag keeps the entries that name it.
func TestRecallExtensionMemory(t *testing.T) {
	ctx := context.Background()
	s, _ := memoryExtensionService(t, true)
	ids := func(tag string) string {
		var out []string
		for _, m := range s.recallExtensionMemory(ctx, "qa-main", tag) {
			out = append(out, m.ID)
		}
		return strings.Join(out, ",")
	}
	if got, want := ids(""), extID(extQAPlain)+","+extID(extQAMain); got != want {
		t.Errorf("without finance = %s, want %s", got, want)
	}
	if got := ids("envelope"); got != "" {
		t.Errorf("a finance tag without finance = %s", got)
	}
	if err := s.tagConversation(ctx, "qa-main", []string{categoryFinance}, infoTaggedByModel, ""); err != nil {
		t.Fatal(err)
	}
	if got := ids(""); !strings.Contains(got, extID(extQAFinance)) || strings.Contains(got, extID(extQAUnknown)) {
		t.Errorf("with finance = %s", got)
	}
	if got, want := ids("Envelope"), extID(extQAFinance); got != want {
		t.Errorf("tag = %s, want %s", got, want)
	}
}

// An Agent started outside a plan carries the general entries its work bears on, never a sensitive one, and only on
// the owner's computer.
func TestAgentStartMemory(t *testing.T) {
	ctx := context.Background()
	s, _ := memoryExtensionService(t, true)
	query := "qa: prepare the quarterly budget envelope totals and the weekly report bullet points"
	got := s.agentStartMemory(ctx, "qa-machine", query)
	if !strings.Contains(got, extQAMain.Text) || strings.Contains(got, extQAFinance.Text) || strings.Contains(got, extQAFinanceUntagged.Text) {
		t.Errorf("agent start = %q", got)
	}
	if got := s.agentStartMemory(ctx, "qa-other", query); got != "" {
		t.Errorf("another computer = %q", got)
	}
	if got := s.withProjectContextForTodo(ctx, "qa-machine", "", "", Todo{}, query); !strings.HasSuffix(got, "\n\n"+query) || !strings.Contains(got, extQAMain.Text) {
		t.Errorf("first message = %q", got)
	}
}

// The Inbox's helper is given the general entries the item bears on under the extension's name, never a sensitive one.
func TestInboxMemoryContext(t *testing.T) {
	ctx := context.Background()
	s, src := memoryExtensionService(t, true)
	src.batch.Names = []string{"QA Memory"}
	blocks := s.inboxMemoryContext(ctx, "qa: can you send the quarterly budget envelope totals, and the weekly report bullet points")
	if len(blocks) != 1 || blocks[0].Name != "QA Memory" || blocks[0].Text != "- "+extQAMain.Text {
		t.Errorf("Inbox = %+v", blocks)
	}
	src.batch = memoryExtensionBatch{}
	if blocks := s.inboxMemoryContext(ctx, "qa: the weekly report bullet points"); len(blocks) != 0 {
		t.Errorf("no entries = %+v", blocks)
	}
}

// The most important entries (the last) that fit the bound, in their order.
func TestMostImportantWithin(t *testing.T) {
	entries := memoryExtensionBatch{Entries: extQAEntries()}.assistantEntries()
	got := mostImportantWithin(entries, len(extQAMain.Text)+len(extQAUnknown.Text))
	if !slices.Equal(entryIDList(got), []string{extID(extQAMain), extID(extQAUnknown)}) {
		t.Errorf("%s", entryIDList(got))
	}
	if got := mostImportantWithin(entries, 1); len(got) != 0 {
		t.Errorf("%s", entryIDList(got))
	}
}

// A targeted recall reaches the last page of a large source after it passes through mapping, the shared cache and
// the public tool handler. The output budget is applied after the tag match, not to a prefix of the source.
func TestDecisionsQueryMemoryBeyondFirstThousand(t *testing.T) {
	s, _, st, _ := memoryAssembleService(t)
	s.memoryEntries = &memEntrySource{}
	s.setStore(func() Store { return &entriesMemStore{infoCategoryMemStore: st} })
	var pages []wire.MemoryEntriesResult
	for start := 0; start < 24001; start += 200 {
		page := wire.MemoryEntriesResult{Tagged: true}
		for i := start; i < min(start+200, 24001); i++ {
			e := wire.MemoryEntry{ID: fmt.Sprintf("qa-%d", i), Text: "qa: unrelated filler", Status: "active"}
			if i == 24000 {
				e.Text, e.Subjects = "qa: late project entry", []string{"qa-late-project"}
			}
			page.Entries = append(page.Entries, e)
		}
		pages = append(pages, page)
	}
	s.memoryExtension = &memoryExtensionCache{r: &fakeMemoryReader{out: []extmgr.MemorySourceRead{{Pages: pages}}}}
	raw, err := s.toolEntries(t.Context(), "qa-machine", "qa-main", "decisions_query", json.RawMessage(`{"tag":"qa-late-project"}`))
	var out struct {
		Memory []recalledMemory `json:"memory"`
	}
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Memory) != 1 || out.Memory[0].ID != memoryExtensionIDPrefix+"qa-24000" {
		t.Fatalf("late entry not recalled: %s", raw)
	}
}

// decisions_query ranks full-term matches first, backfills remaining budget by multi-term hit count, subject weight
// and entry rank, skips entries that would exceed memoryRecallMax, and never leaks sensitive entries.
func TestDecisionsQueryMemoryRankedFallbackAndBudget(t *testing.T) {
	s, _, st, _ := memoryAssembleService(t)
	s.memoryEntries = &memEntrySource{}
	s.setStore(func() Store { return &entriesMemStore{infoCategoryMemStore: st} })
	page := wire.MemoryEntriesResult{Tagged: true, Entries: []wire.MemoryEntry{
		{ID: "qa-full", Text: "qa: deploy atlas rollback checklist", Subjects: []string{"atlas"}, Rank: 400, Status: "active"},
		{ID: "qa-two-high", Text: "qa: deploy atlas notes", Subjects: []string{"atlas"}, Rank: 900, Status: "active"},
		{ID: "qa-two-low", Text: "qa: deploy atlas legacy", Rank: 200, Status: "active"},
		{ID: "qa-one-high", Text: "qa: rollback procedure", Rank: 850, Status: "active"},
		{ID: "qa-huge", Text: "qa: atlas " + strings.Repeat("x", memoryRecallMax), Subjects: []string{"atlas"}, Rank: 999, Status: "active"},
		{ID: "qa-sensitive", Text: "qa: deploy atlas rollback secret", Subjects: []string{"atlas"}, Categories: []string{categoryFinance}, OriginSpace: "qa-finance", Rank: 999, Status: "active"},
	}}
	s.memoryExtension = &memExtensionSource{batch: memoryExtensionBatchOf([]wire.MemoryEntriesResult{page})}
	raw, err := s.toolEntries(t.Context(), "qa-machine", "qa-main", "decisions_query", json.RawMessage(`{"tag":"atlas deploy rollback"}`))
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Memory []recalledMemory `json:"memory"`
	}
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	got := make([]string, len(out.Memory))
	for i, m := range out.Memory {
		got[i] = strings.TrimPrefix(m.ID, memoryExtensionIDPrefix)
	}
	want := []string{"qa-full", "qa-two-high", "qa-two-low", "qa-one-high"}
	if !slices.Equal(got, want) {
		t.Fatalf("order = %v, want %v (raw=%s)", got, want, raw)
	}
}
