package assistant

import (
	"context"
	"strings"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// What the memory assembler (memory_assemble.go) is told of the ledger's entries (#13532, docs/assistant/health.md
// "Memory by scene"). This file is the one place that reads an entry's space and categories, so wiring the memory
// ledger's next step (per-entry tagging, a space scope dimension) changes only it.

// memoryEntrySource lists ledger entries newest first (each page the next older one): the manager database, or a
// test's.
type memoryEntrySource interface {
	NewestAssistantEntries(context.Context, proto.AssistantEntriesParams, bool) (proto.AssistantEntriesResult, error)
}

// memoryEntrySrc is where the assembler reads entries: Service.memoryEntries when a test set one, else the manager
// database; nil when there is none.
func (s *Service) memoryEntrySrc() memoryEntrySource {
	if s.memoryEntries != nil {
		return s.memoryEntries
	}
	st, _ := s.store().(memoryEntrySource)
	return st
}

// entryTrackUnknown is the track of an entry whose track cannot be told (as track_kinds.go reads unknown information):
// no conversation is of it and the registry knows no such track, so the entry is in no conversation's own track and
// is not known.
const entryTrackUnknown = "-"

// memoryEntryUses is how usage sees each of entries (an infoUse), read for all of them at once:
//
//   - Its track (SourceTrack) is the one it was said in: the space of the conversation it names as its source, the
//     space dimension of its scope until the entry type has one. An entry that states a track (OriginSpace) other
//     than its conversation's is not known and takes the conversation's; one that states none (the main track, or a
//     lookup that failed when it was written) whose conversation cannot be told now has no track it is used in as its
//     own, and is not known. An entry naming no conversation keeps what it states ("" the main track).
//   - Its categories are the ones it carries (Category) and its track's default categories, which everything from
//     that track carries.
//   - An entry that carries none was not tagged itself (nothing tags entries yet): it takes the categories the
//     conversation it was said in was tagged with, whenever they are kept, and is known only when that conversation
//     was tagged and none of its turns waits. Otherwise it is not known, so it is used as usual in its own track and
//     taken for sensitive everywhere else.
//   - With no model set up for info.tag nothing new is tagged: an entry then carries its track's default categories
//     and those its conversation was tagged with before, as a conversation does (info_use.go), known when the track is
//     registered and the conversation's categories could be read.
//
// An entry naming a category the manager does not know, or a track the registry does not, is not known.
func (s *Service) memoryEntryUses(ctx context.Context, entries []proto.AssistantEntry) func(proto.AssistantEntry) infoUse {
	tagging := s.infoTagging(ctx)
	type floor struct {
		cats []string
		ok   bool
	}
	floors := map[string]floor{}
	floorOf := func(space string) floor {
		if f, ok := floors[space]; ok {
			return f
		}
		cats, ok := s.trackFloor(ctx, space)
		floors[space] = floor{cats, ok}
		return floors[space]
	}
	var sessions, untagged []string
	seen, seenUntagged := map[string]bool{}, map[string]bool{}
	for _, e := range entries {
		sid := e.Source.Session
		if sid == "" {
			continue
		}
		if !seen[sid] {
			seen[sid] = true
			sessions = append(sessions, sid)
		}
		if len(e.Category) == 0 && !seenUntagged[sid] {
			seenUntagged[sid] = true
			untagged = append(untagged, sid)
		}
	}
	var spaces map[string]spaceTold
	if len(sessions) > 0 {
		spaces = s.conversationSpaces(ctx, sessions)
	}
	var rows map[string][]db.InfoCategory
	readOK := true
	if st, durable := s.store().(infoCategoryStore); durable && len(untagged) > 0 {
		rctx, cancel := context.WithTimeout(ctx, infoCategoryStoreTimeout)
		var err error
		rows, err = st.InfoCategories(rctx, db.InfoCategoryConversation, untagged)
		cancel()
		if err != nil {
			s.logf("memory: entry categories: %v", err)
			readOK = false
		}
	}
	return func(e proto.AssistantEntry) infoUse {
		sid := e.Source.Session
		track, trackKnown := e.OriginSpace, true
		if sid != "" {
			told := spaces[sid]
			switch {
			case told.known && told.space != track:
				track, trackKnown = told.space, track == ""
			case !told.known && track == "":
				track, trackKnown = entryTrackUnknown, false
			}
		}
		cats, ok := normalCategories(e.Category)
		f := floorOf(track)
		known := ok && f.ok && trackKnown
		if len(cats) == 0 {
			tagged := false
			for _, r := range rows[sid] {
				tagged = true
				if !r.RemovedByUser {
					cats = append(cats, r.Category)
				}
			}
			if tagging {
				known = known && readOK && tagged && !s.infoTagWaiting.has(sid)
			} else {
				known = known && (sid == "" || readOK)
			}
		}
		cats, _ = normalCategories(append(cats, f.cats...))
		return infoUse{Categories: cats, Known: known, SourceTrack: track}
	}
}

// The memory extension's entries are the assembler's second source, after the ledger's (#13532). The extension keeps
// them in its own state and answers them through memory.entries once it declares memory_sources (#13586): each
// carries the space it was first said in (origin_space, the slug, "" for the main space) and its categories
// (categories). The extensions' manager reads them (memory_extension_source.go, the source Service.memoryExtension is
// set to) and stops delivering that extension's text outlets directly; memoryExtensionUses is the rules they are held
// to.

// memoryExtensionIDPrefix starts the id of every entry of the memory extension as the assembler carries it, so a
// to-do's brief tells them from the ledger's.
const memoryExtensionIDPrefix = "memory-extension:"

// memoryExtensionEntry is an entry of the memory extension as a source hands it over.
type memoryExtensionEntry struct {
	ID   string
	Text string
	// OriginSpace is origin_space: the slug of the space the entry was first said in, "" the main space.
	OriginSpace string
	// Categories are the names in categories (health, finance, relationships, emotion, legal_id); none: general.
	Categories []string
	Tags       []string
	// Facet is the extension's grouping, used only by explicit recall queries.
	Facet string
	// Standing is the extension's standing flag: it kept the entry in every conversation (always in context). Only
	// a standing entry goes to a standing channel (memoryExtensionBatch.standingOnly); the others come in by relevance
	// (their subjects or text) in a recall, a hand-off or the Inbox.
	Standing bool
	// Rank is the extension's importance rank (0..1000), used to order recall and entity-triggered matches.
	Rank int
}

// memoryExtensionBatch is what a source hands over at once. Tagged says the extension that wrote the entries sets
// origin_space and categories (0.2.0-beta.41 or later): both are left out when empty, so a missing field means the
// main space or no category only then. From an older extension, or one whose version cannot be told, a missing
// field means nothing is known.
type memoryExtensionBatch struct {
	Entries []memoryExtensionEntry
	Tagged  bool
	// Names are the names of the extensions whose entries these are, for the Inbox's background block.
	Names []string
	// StandingBytes is the standing budget the extension stated (standing_bytes, the largest of its pages and of the
	// sources); 0: none stated.
	StandingBytes int
}

// standingBudget is the bytes of b's standing entries a standing channel carries (#15312): what the extension stated,
// at most memoryExtensionStandingMost, else memoryExtensionStandingDefault. It is the extension's own, never spent by
// the ledger's entries.
func (b memoryExtensionBatch) standingBudget() int {
	if b.StandingBytes <= 0 {
		return memoryExtensionStandingDefault
	}
	return min(b.StandingBytes, memoryExtensionStandingMost)
}

// memoryExtensionSource hands the assembler the memory extension's entries: personal says the receiver may see what
// is about the owner (every one of them is).
type memoryExtensionSource interface {
	MemoryExtensionEntries(ctx context.Context, personal bool) (memoryExtensionBatch, error)
}

// memoryExtensionEntries is the memory extension's entries for an assembly: none while no source is set
// (Service.memoryExtension, a test's until a source of the manager's lands) or when the receiver may not see
// personal entries.
func (s *Service) memoryExtensionEntries(ctx context.Context, personal bool) memoryExtensionBatch {
	if s.memoryExtension == nil || !personal {
		return memoryExtensionBatch{}
	}
	b, err := s.memoryExtension.MemoryExtensionEntries(ctx, personal)
	if err != nil {
		s.logf("memory: extension entries: %v", err)
		return memoryExtensionBatch{}
	}
	return b
}

// assistantEntries are b's entries as the assembler carries them: personal, for any executor, ids prefixed.
func (b memoryExtensionBatch) assistantEntries() []proto.AssistantEntry {
	out := make([]proto.AssistantEntry, 0, len(b.Entries))
	for _, e := range b.Entries {
		out = append(out, proto.AssistantEntry{ID: memoryExtensionIDPrefix + e.ID, Text: e.Text, OriginSpace: e.OriginSpace,
			Category: e.Categories, Tag: e.Tags, Scope: proto.AssistantEntryScope{Executor: "any", Personal: true}, Status: "active"})
	}
	return out
}

// memoryExtensionUses is how usage sees the memory extension's entries (assistantEntries), held to the ledger's
// strict rules:
//
//   - From an extension that tags (tagged), an entry's track is the space it states, "" the main one: it names no
//     conversation the manager could check it against, as a ledger entry naming none keeps what it states. Its
//     categories are its own and its track's default categories, which the manager adds itself (an entry of a
//     finance space the extension did not tag is finance all the same). A category or a track the manager does not
//     know makes it not known.
//   - From an extension that does not tag, where an entry was said cannot be told: it is in no track (entryTrackUnknown)
//     and not known, so it is no conversation's own and is taken for sensitive everywhere. Its categories, when it
//     names some, still count.
func (s *Service) memoryExtensionUses(ctx context.Context, tagged bool) func(proto.AssistantEntry) infoUse {
	return func(e proto.AssistantEntry) infoUse {
		cats, ok := normalCategories(e.Category)
		if !tagged {
			return infoUse{Categories: cats, SourceTrack: entryTrackUnknown}
		}
		floor, floorOK := s.trackFloor(ctx, e.OriginSpace)
		cats, _ = normalCategories(append(cats, floor...))
		return infoUse{Categories: cats, Known: ok && floorOK, SourceTrack: e.OriginSpace}
	}
}

// memorySources are the assembler's sources together, oldest first: the memory extension's entries, then the
// ledger's. Each entry is seen by its own source's rules and spends its own source's budget (assembleEntries).
func (s *Service) memorySources(ctx context.Context, ledger []proto.AssistantEntry, ext memoryExtensionBatch) ([]proto.AssistantEntry, func(proto.AssistantEntry) infoUse) {
	extEntries := ext.assistantEntries()
	if len(extEntries) == 0 {
		return ledger, s.memoryEntryUses(ctx, ledger)
	}
	ledgerUse, extUse := s.memoryEntryUses(ctx, ledger), s.memoryExtensionUses(ctx, ext.Tagged)
	all := append(extEntries, ledger...)
	return all, func(e proto.AssistantEntry) infoUse {
		if strings.HasPrefix(e.ID, memoryExtensionIDPrefix) {
			return extUse(e)
		}
		return ledgerUse(e)
	}
}
