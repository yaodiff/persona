package assistant

import (
	"cmp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// What the manager takes of a memory extension's memory.entries answers (schema/extension/memory.schema.json,
// docs/extensions/extensions.md "Memory sources", #13532): the pages, re-checked and mapped to the assembler's input
// (memoryExtensionBatch, memory_assemble_entry.go). Pure: the extensions' manager calls the extension
// (extmgr/memory_sources.go) and memory_extension_source.go maps what it answered with memoryExtensionBatchOf.

// The bounds of reading one complete memory_sources contribution into the shared cache.
const (
	// memoryExtensionPageLimit is the limit each memory.entries call asks for.
	memoryExtensionPageLimit = 200
	// memoryExtensionMaxPages bounds how many pages one complete memory.entries read may follow.
	memoryExtensionMaxPages = 250
	// A source is cached only after pagination completes. Time and byte limits fail the read rather than caching
	// a prefix that would permanently hide later entries from targeted recall.
	memoryExtensionReadTimeout = 2 * time.Minute
	memoryExtensionTotalMost   = 128 << 20
	// memoryExtensionCallTimeout bounds one memory.entries call; a call that runs past it counts as failed.
	memoryExtensionCallTimeout = 5 * time.Second
	// memoryExtensionAnswerMost is the most bytes one answer may hold; a longer one counts as failed. limit only caps
	// the entries on a page: the extension ends a page early (has_more, after) to stay within it.
	memoryExtensionAnswerMost = 1 << 20
)

// The bounds of one MemoryEntry, as memory.schema.json states them.
const (
	memoryEntryTextMost       = 4096
	memoryEntryCategoriesMost = 8
	memoryEntrySubjectsMost   = 16
	memoryEntrySubjectMost    = 80
	memoryEntryRankMost       = 1000
)

var (
	memoryEntryIDPattern    = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	memoryEntrySpacePattern = lazyre.New(`^[a-z0-9][a-z0-9-]{0,63}$`)
)

// memoryExtensionBatchOf is what pages, a contribution's memory.entries answers in the order they were read, hand the
// assembler:
//
//   - Tagged only when every page said tagged (and there is at least one): one page that cannot promise origin_space
//     and categories makes every entry's missing field mean nothing is known.
//   - Only active entries, each holding to the schema's rules (an id, a text of 1 to 4096 characters, a space slug, at
//     most 8 categories, a rank of 0 to 1000); one that breaks a rule is dropped, never repaired. An id seen before
//     is dropped too (the first one counts).
//   - Categories go through as the extension wrote them: one the manager does not know makes the entry not known
//     (memoryExtensionUses), which is stricter than dropping it.
//   - Subjects become the entry's tags, so a task's or a message's words that name them bring it in (entryRelevant);
//     each on one line, empty ones and those over 80 characters dropped, at most 16.
//   - Standing goes through on the entry (memoryExtensionEntry.Standing): only a standing entry is meant for a
//     standing channel, the others come in by relevance; the order alone does not keep them apart.
//   - StandingBytes is the largest standing_bytes a page states (the assembler caps it: standingBudget).
//   - Order: the assembler takes entries oldest first and spends a budget newest first, so the most important go
//     last: the extension's own order (most important first) is kept among entries alike, standing ones and then
//     higher ranks put first, and the whole reversed.
//
// Facet is retained for explicit recall filtering; categories still go through the hub's visibility checks.
func memoryExtensionBatchOf(pages []wire.MemoryEntriesResult) memoryExtensionBatch {
	b := memoryExtensionBatch{Tagged: len(pages) > 0}
	type ranked struct {
		e        memoryExtensionEntry
		standing bool
		rank     int
	}
	var keep []ranked
	seen := map[string]bool{}
	for _, p := range pages {
		b.Tagged = b.Tagged && p.Tagged
		b.StandingBytes = max(b.StandingBytes, p.StandingBytes)
		for _, w := range p.Entries {
			e, ok := memoryExtensionEntryOf(w)
			if !ok || seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			keep = append(keep, ranked{e, w.Standing, w.Rank})
		}
	}
	slices.SortStableFunc(keep, func(x, y ranked) int {
		if x.standing != y.standing {
			if x.standing {
				return -1
			}
			return 1
		}
		return cmp.Compare(y.rank, x.rank)
	})
	slices.Reverse(keep)
	for _, k := range keep {
		b.Entries = append(b.Entries, k.e)
	}
	return b
}

// memoryExtensionEntryOf is w as the assembler takes it, ok when it is active and holds to the schema's rules.
func memoryExtensionEntryOf(w wire.MemoryEntry) (memoryExtensionEntry, bool) {
	text := strings.TrimSpace(w.Text)
	switch {
	case w.Status != "active", !memoryEntryIDPattern.MatchString(w.ID), text == "",
		utf8.RuneCountInString(w.Text) > memoryEntryTextMost,
		w.OriginSpace != "" && !memoryEntrySpacePattern.MatchString(w.OriginSpace),
		len(w.Categories) > memoryEntryCategoriesMost, w.Rank < 0, w.Rank > memoryEntryRankMost:
		return memoryExtensionEntry{}, false
	}
	var tags []string
	for _, s := range w.Subjects {
		s = strings.Join(strings.Fields(s), " ")
		if s == "" || utf8.RuneCountInString(s) > memoryEntrySubjectMost || slices.Contains(tags, s) {
			continue
		}
		if tags = append(tags, s); len(tags) == memoryEntrySubjectsMost {
			break
		}
	}
	return memoryExtensionEntry{ID: w.ID, Text: text, OriginSpace: w.OriginSpace, Categories: slices.Clone(w.Categories),
		Tags: tags, Facet: w.Facet, Standing: w.Standing, Rank: w.Rank}, true
}
