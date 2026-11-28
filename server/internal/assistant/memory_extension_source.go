package assistant

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/extmgr"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The memory extension's entries as a source of the assembler (#13586, docs/extensions/extensions.md "Memory
// sources"): the extensions' manager reads every memory_sources contribution that is on (extmgr/memory_sources.go),
// memoryExtensionBatchOf maps each one's pages, and memoryExtensionCache keeps the result for the assemblies of the
// next minute (read again in the background), so a burst of messages, triaged items and started Agents asks each
// extension once. The four paths take
// it here: a main-space conversation's standing context (standingMemorySection, its standing entries only), a
// dispatched Agent's first message (agentStartMemory, and handOffMemory for a planned task), the Inbox's helper
// (inboxMemoryContext) and a recall (recallExtensionMemory, decisions_query's first page).

// memorySourceReader is the extensions' manager as the memory source reads it.
type memorySourceReader interface {
	MemorySourceReads(ctx context.Context, b extmgr.MemoryReadBounds) []extmgr.MemorySourceRead
	MemorySourcesGeneration() uint64
}

var _ memorySourceReader = (*extmgr.Manager)(nil)

const (
	// memoryExtensionFresh is how long what the memory sources answered is used; a change to any extension (its
	// generation) reads them again before.
	memoryExtensionFresh = time.Minute
	// memoryExtensionRetry is how long a read in which a source failed is used: the next assembly after it asks
	// again, without each message of a burst waiting out a source that does not answer.
	memoryExtensionRetry = 10 * time.Second
	// memoryRecallMax is the most bytes of the memory extension's entries one recall returns, the most important
	// first; memoryInboxMax the most the Inbox's helper is given, as much as an extension's Inbox context was.
	memoryRecallMax = 3072
	memoryInboxMax  = 1 << 10
)

// memoryExtensionCache is the memoryExtensionSource of the extensions' manager: the merged batch of every memory
// source, read at most once per memoryExtensionFresh (memoryExtensionRetry after a failure) and generation, one read
// at a time, in the background. An assembly never waits for a read while a batch of the same generation is there: it
// takes that one, and the read is for the next. Without one (the first read, or an extension changed) a recall, a
// hand-off or the Inbox waits at most memoryExtensionWait, so its context is never held up by a source that does not
// answer; the standing context of an owner's message never waits (CachedMemoryExtensionEntries).
type memoryExtensionCache struct {
	r memorySourceReader

	mu      sync.Mutex
	at      time.Time
	gen     uint64
	failed  bool
	batch   memoryExtensionBatch
	reading chan struct{}
}

// memoryExtensionWait is the longest an assembly waits for a read when it has no batch of the current generation.
const memoryExtensionWait = 3 * time.Second

// errMemorySourcesSlow: the memory sources did not answer within memoryExtensionWait; the read goes on for the next
// assembly.
var errMemorySourcesSlow = errors.New("the memory sources did not answer in time")

// setMemorySources makes the extensions' manager e the memory extension's source when it reads memory sources.
func (s *Service) setMemorySources(e any) {
	if r, ok := e.(memorySourceReader); ok {
		s.memoryExtension = &memoryExtensionCache{r: r}
	}
}

// MemoryExtensionEntries is the merged batch: the one kept when it is fresh, the one kept while a newer is read when
// it is of the current generation, else the one read now (within memoryExtensionWait). A read that ends on an older
// generation (an extension changed while it ran) is followed by one of the current generation within the same wait.
// personal is checked by the caller (memoryExtensionEntries).
func (c *memoryExtensionCache) MemoryExtensionEntries(ctx context.Context, _ bool) (memoryExtensionBatch, error) {
	timer := time.NewTimer(memoryExtensionWait)
	defer timer.Stop()
	for {
		b, have, reading := c.current(ctx)
		if have {
			return b, nil
		}
		select {
		case <-reading: // the next round takes its batch, or starts a read of the current generation
		case <-timer.C:
			return memoryExtensionBatch{}, errMemorySourcesSlow
		case <-ctx.Done():
			return memoryExtensionBatch{}, ctx.Err()
		}
	}
}

// CachedMemoryExtensionEntries is the batch kept for the current generation, never waiting: none while there is none
// (the first read, or an extension changed), and the read it starts fills it for the next message. The standing
// context of each owner's message takes it, so a message's feedback never waits on a memory source (principle 7).
func (c *memoryExtensionCache) CachedMemoryExtensionEntries(ctx context.Context) memoryExtensionBatch {
	b, _, _ := c.current(ctx)
	return b
}

// current is the batch of the current generation when one is kept (have), starting a read in the background when it
// is not fresh; without one, the read to wait for (started here when none runs).
func (c *memoryExtensionCache) current(ctx context.Context) (b memoryExtensionBatch, have bool, reading chan struct{}) {
	c.mu.Lock()
	defer c.mu.Unlock()
	gen := c.r.MemorySourcesGeneration()
	fresh := memoryExtensionFresh
	if c.failed {
		fresh = memoryExtensionRetry
	}
	have = !c.at.IsZero() && c.gen == gen
	if have && time.Since(c.at) < fresh {
		return c.batch, true, nil
	}
	if c.reading == nil {
		c.reading = make(chan struct{})
		// The read is shared and outlives its caller (each call has its own bound).
		go c.read(context.WithoutCancel(ctx), gen, c.reading)
	}
	if have {
		return c.batch, true, c.reading
	}
	return memoryExtensionBatch{}, false, c.reading
}

// read reads the memory sources for generation gen, keeps what they answered and closes done.
func (c *memoryExtensionCache) read(ctx context.Context, gen uint64, done chan struct{}) {
	b, failed := memoryExtensionMerge(c.r.MemorySourceReads(ctx, memoryReadBounds()))
	c.mu.Lock()
	c.batch, c.failed, c.at, c.gen, c.reading = b, failed, time.Now(), gen, nil
	c.mu.Unlock()
	close(done)
}

// memoryReadBounds are the bounds of memory_extension_wire.go, for the extensions' manager.
func memoryReadBounds() extmgr.MemoryReadBounds {
	return extmgr.MemoryReadBounds{PageLimit: memoryExtensionPageLimit, MaxPages: memoryExtensionMaxPages,
		ReadTimeout: memoryExtensionReadTimeout, TotalMost: memoryExtensionTotalMost,
		CallTimeout: memoryExtensionCallTimeout, AnswerMost: memoryExtensionAnswerMost}
}

// memoryExtensionMerge is the sources' reads as one batch, and whether one of them failed (that source then gives no
// entries). Each read is mapped by memoryExtensionBatchOf; one that is not tagged has its entries put in no track
// (entryTrackUnknown, which no registry knows), so they are not known and the strictest rule holds for them as for an
// untagged batch, while the others keep theirs. An id another source gave first is dropped.
func memoryExtensionMerge(reads []extmgr.MemorySourceRead) (memoryExtensionBatch, bool) {
	out := memoryExtensionBatch{Tagged: true}
	failed := false
	seen := map[string]bool{}
	for _, r := range reads {
		if r.Err != nil {
			failed = true
			continue
		}
		b := memoryExtensionBatchOf(r.Pages)
		out.StandingBytes = max(out.StandingBytes, b.StandingBytes)
		added := false
		for _, e := range b.Entries {
			if seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			if !b.Tagged {
				e.OriginSpace = entryTrackUnknown
			}
			out.Entries = append(out.Entries, e)
			added = true
		}
		if added && r.Name != "" {
			out.Names = append(out.Names, r.Name)
		}
	}
	return out, failed
}

// standingOnly is b with its standing entries only: only those are meant for a standing channel; the others come in
// by relevance in a recall, a hand-off or the Inbox.
func (b memoryExtensionBatch) standingOnly() memoryExtensionBatch {
	out := memoryExtensionBatch{Tagged: b.Tagged, Names: b.Names, StandingBytes: b.StandingBytes}
	for _, e := range b.Entries {
		if e.Standing {
			out.Entries = append(out.Entries, e)
		}
	}
	return out
}

// about keeps entries matching every whitespace-separated query term (case-insensitive substring matches). A term
// may occur in text, a subject, the facet or a category; different terms may match different fields. Visibility is
// still enforced by assembleExtension after this filter, so naming a sensitive category never grants access to it.
func (b memoryExtensionBatch) about(tag string) memoryExtensionBatch {
	terms := strings.Fields(strings.ToLower(tag))
	if len(terms) == 0 {
		return b
	}
	out := memoryExtensionBatch{Tagged: b.Tagged, Names: b.Names, StandingBytes: b.StandingBytes}
	for _, e := range b.Entries {
		fields := []string{strings.ToLower(e.Text), strings.ToLower(e.Facet)}
		for _, values := range [][]string{e.Tags, e.Categories} {
			for _, v := range values {
				fields = append(fields, strings.ToLower(v))
			}
		}
		matches := true
		for _, term := range terms {
			if !slices.ContainsFunc(fields, func(field string) bool { return strings.Contains(field, term) }) {
				matches = false
				break
			}
		}
		if matches {
			out.Entries = append(out.Entries, e)
		}
	}
	return out
}

// assembleExtension is the memory extension's entries in b as f takes them, the ledger's left out: the standing
// context, an Agent started outside a plan, the Inbox and the recall of the extension's entries read them alone,
// as they read its text before; the ledger reaches them in its own step (docs/assistant/health.md "Memory by scene").
func (s *Service) assembleExtension(ctx context.Context, f assembleFor, b memoryExtensionBatch) assembled {
	if f.Role == assembleStanding {
		b = b.standingOnly()
	}
	all, info := s.memorySources(ctx, nil, b)
	if len(all) == 0 {
		return assembled{}
	}
	f.ExtensionStanding = b.standingBudget()
	return assembleEntries(all, f, info)
}

// extensionMemoryBatch is the memory extension's entries for one of the owner's own assemblies; none without a source.
// The callers check the receiver may see personal entries before any of them goes anywhere.
func (s *Service) extensionMemoryBatch(ctx context.Context) memoryExtensionBatch {
	if s.memoryExtension == nil {
		return memoryExtensionBatch{}
	}
	return s.memoryExtensionEntries(ctx, true)
}

// memoryExtensionCached is a source that can hand over what it has without waiting (memoryExtensionCache).
type memoryExtensionCached interface {
	CachedMemoryExtensionEntries(ctx context.Context) memoryExtensionBatch
}

// cachedExtensionMemoryBatch is extensionMemoryBatch without waiting for a read, for the standing context of an
// owner's message: what the source keeps for the current generation, or none until its read is done. A source that
// keeps nothing (a test's) answers at once anyway.
func (s *Service) cachedExtensionMemoryBatch(ctx context.Context) memoryExtensionBatch {
	if c, ok := s.memoryExtension.(memoryExtensionCached); ok {
		return c.CachedMemoryExtensionEntries(ctx)
	}
	return s.extensionMemoryBatch(ctx)
}

// mostImportantWithin are the last of entries (the assembler's order: the most important last) whose texts fit in
// most bytes together, in their order.
func mostImportantWithin(entries []proto.AssistantEntry, most int) []proto.AssistantEntry {
	start := len(entries)
	for i := len(entries) - 1; i >= 0; i-- {
		if most -= len(entries[i].Text); most < 0 {
			break
		}
		start = i
	}
	return entries[start:]
}

// ownersPersonal says whether conversation session on machine may be given what is about the owner: the owner's own
// assistant machine, not a guest's conversation or a work computer.
func (s *Service) ownersPersonal(ctx context.Context, machine, session string) bool {
	if machine == "" || machine != s.settings(ctx).MachineID || s.isWorkMachine(machine) {
		return false
	}
	_, guest, err := s.guestOf(ctx, machine, session)
	return err == nil && !guest
}

// standingMemorySection is the snapshot section of the memory extension's standing entries that main-space
// conversation session on machine may use (its standing channel): "" when there are none, or it may not see the
// owner's personal entries.
func (s *Service) standingMemorySection(ctx context.Context, machine, session string) string {
	// The cheap checks first: a guest's conversation or a work computer never takes the batch.
	if !s.ownersPersonal(ctx, machine, session) {
		return ""
	}
	b := s.cachedExtensionMemoryBatch(ctx).standingOnly()
	if len(b.Entries) == 0 {
		return ""
	}
	a := s.assembleExtension(ctx, s.assembleForConversation(ctx, session, assembleStanding), b)
	if len(a.Entries) == 0 {
		return ""
	}
	d := prompts.StandingMemory{Language: answerLanguage(s.hello(ctx).Locale)}
	for _, e := range a.Entries {
		d.Entries = append(d.Entries, prompts.MemoryEntry{Text: oneLine(prompts.ContextBody(e.Text))})
	}
	return prompts.RenderStandingMemory(d)
}

// withStandingMemory is snapshot with the standing memory section after it (standingMemorySection).
func (s *Service) withStandingMemory(ctx context.Context, machine, session, snapshot string) string {
	section := s.standingMemorySection(ctx, machine, session)
	if section == "" {
		return snapshot
	}
	if snapshot != "" && !strings.HasSuffix(snapshot, "\n") {
		snapshot += "\n"
	}
	return snapshot + section + "\n"
}

// agentStartMemory is the context block of the memory extension's entries an Agent started outside a plan carries
// on machine (an agent the assistant starts, a to-do handed to a new Agent): general entries query (what it is asked)
// bears on, only on the owner's own computer; never a sensitive one, as no confirmed plan named their categories.
func (s *Service) agentStartMemory(ctx context.Context, machine, query string) string {
	if machine == "" || machine != s.settings(ctx).MachineID || s.isWorkMachine(machine) {
		return ""
	}
	b := s.extensionMemoryBatch(ctx)
	if len(b.Entries) == 0 {
		return ""
	}
	a := s.assembleExtension(ctx, assembleFor{Role: assembleHandOff, Query: query}, b)
	if len(a.Entries) == 0 {
		return ""
	}
	return s.renderMemoryEntries(ctx, a)
}

// inboxMemoryContext is the Inbox helper's background block of the memory extension's entries for the item query
// (scene 5): general entries the item bears on, never a sensitive one, the most important within memoryInboxMax.
func (s *Service) inboxMemoryContext(ctx context.Context, query string) []prompts.ExtensionContext {
	b := s.extensionMemoryBatch(ctx)
	if len(b.Entries) == 0 {
		return nil
	}
	a := s.assembleExtension(ctx, assembleFor{Role: assembleInbox, Query: query}, b)
	entries := mostImportantWithin(a.Entries, memoryInboxMax)
	if len(entries) == 0 {
		return nil
	}
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		lines = append(lines, "- "+oneLine(prompts.ContextBody(e.Text)))
	}
	return []prompts.ExtensionContext{{Name: oneLine(strings.Join(b.Names, ", ")), Text: strings.Join(lines, "\n")}}
}

// recalledMemory is one of the memory extension's entries as a recall returns it.
type recalledMemory struct {
	ID   string `json:"id"`
	Text string `json:"text"`
}

// recallExtensionMemory is the memory extension's entries a recall in conversation session returns (scene 2): its own
// track's, general ones from elsewhere and sensitive ones from elsewhere only when the conversation carries their
// categories; with tag, full-term matches come first and remaining budget is backfilled by multi-term hit count and
// subject/rank relevance; at most memoryRecallMax bytes.
func (s *Service) recallExtensionMemory(ctx context.Context, session, tag string) []recalledMemory {
	b := s.extensionMemoryBatch(ctx)
	if len(b.Entries) == 0 {
		return nil
	}
	// Enforce visibility and category checks before scoring so hidden or sensitive entries never leak.
	a := s.assembleExtension(ctx, s.assembleForConversation(ctx, session, assembleRecall), b)
	if len(a.Entries) == 0 {
		return nil
	}
	terms := uniqueQueryTerms(tag)
	if len(terms) == 0 {
		var out []recalledMemory
		for _, e := range mostImportantWithin(a.Entries, memoryRecallMax) {
			out = append(out, recalledMemory{ID: e.ID, Text: e.Text})
		}
		return out
	}
	byID := make(map[string]memoryExtensionEntry, len(b.Entries))
	for _, orig := range b.Entries {
		byID[memoryExtensionIDPrefix+orig.ID] = orig
	}
	type scored struct {
		entry        proto.AssistantEntry
		fullMatch    bool
		matchedTerms int
		score        int
		rank         int
		order        int
	}
	var candidates []scored
	for idx, e := range a.Entries {
		orig := byID[e.ID]
		matched, subjectHits, exactSubjectHits := scoreRecallEntry(orig, terms)
		if matched == 0 {
			continue
		}
		candidates = append(candidates, scored{
			entry:        e,
			fullMatch:    matched == len(terms),
			matchedTerms: matched,
			score:        exactSubjectHits*40 + subjectHits*15 + matched*10 + orig.Rank,
			rank:         orig.Rank,
			order:        idx,
		})
	}
	if len(candidates) == 0 {
		return nil
	}
	slices.SortStableFunc(candidates, func(x, y scored) int {
		if x.fullMatch != y.fullMatch {
			if x.fullMatch {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(y.matchedTerms, x.matchedTerms); c != 0 {
			return c
		}
		if c := cmp.Compare(y.score, x.score); c != 0 {
			return c
		}
		if c := cmp.Compare(y.rank, x.rank); c != 0 {
			return c
		}
		return cmp.Compare(y.order, x.order)
	})
	left := memoryRecallMax
	var out []recalledMemory
	for _, c := range candidates {
		if len(c.entry.Text) > left {
			continue
		}
		left -= len(c.entry.Text)
		out = append(out, recalledMemory{ID: c.entry.ID, Text: c.entry.Text})
		if left == 0 {
			break
		}
	}
	return out
}

func uniqueQueryTerms(tag string) []string {
	var out []string
	for _, t := range strings.Fields(strings.ToLower(tag)) {
		if t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

func scoreRecallEntry(e memoryExtensionEntry, terms []string) (matched, subjectHits, exactSubjectHits int) {
	textLower := strings.ToLower(e.Text)
	facetLower := strings.ToLower(e.Facet)
	tagsLower := make([]string, len(e.Tags))
	for i, v := range e.Tags {
		tagsLower[i] = strings.ToLower(v)
	}
	catsLower := make([]string, len(e.Categories))
	for i, v := range e.Categories {
		catsLower[i] = strings.ToLower(v)
	}
	for _, term := range terms {
		inTag := false
		exactTag := false
		for _, v := range tagsLower {
			if strings.Contains(v, term) {
				inTag = true
				if v == term {
					exactTag = true
				}
			}
		}
		inFacet := facetLower != "" && strings.Contains(facetLower, term)
		inText := strings.Contains(textLower, term)
		inCat := false
		for _, v := range catsLower {
			if strings.Contains(v, term) {
				inCat = true
				break
			}
		}
		if inTag || inFacet || inText || inCat {
			matched++
			if inTag || inFacet {
				subjectHits++
			}
			if exactTag || facetLower == term {
				exactSubjectHits++
			}
		}
	}
	return matched, subjectHits, exactSubjectHits
}
