package assistant

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"unicode"

	"github.com/nwverse/persona/server/internal/proto"
)

// The memory assembler (#13532, docs/assistant/health.md "Memory by scene"): which of the memory ledger's entries go
// where. It picks entries by their scope, then lets each through by usage (usage.go) for the scene it goes to:
//
//   - a conversation's standing channel (assembleStanding): its own track's entries (scene 1) and personal entries
//     from elsewhere that are general (scene 2); never a sensitive entry from elsewhere;
//   - a recall on request (assembleRecall: decisions_query, later space_search): its own track's entries, general
//     ones from elsewhere, and sensitive ones from elsewhere only when the conversation carries every one of their
//     categories (scene 2);
//   - a dispatched task's first message (assembleHandOff): general entries the task's words bear on, and sensitive
//     ones only when the owner confirmed a plan whose words were tagged with every one of their categories (scene 3);
//     the to-do's brief lists what was carried (memory_handoff.go);
//   - the Inbox's helper and drafts for other people (assembleInbox): general entries the message bears on, never a
//     sensitive one (scene 5).
//
// It reads no track's name or kind. The ledger's entries reach it through memoryEntryUses, and the memory extension's,
// its second source, through memoryExtensionUses (memory_assemble_entry.go).

// assembleRole is who an assembly is for.
type assembleRole int

const (
	assembleStanding assembleRole = iota + 1
	assembleRecall
	assembleHandOff
	assembleInbox
)

// Budgets, in bytes of entry text (#15312): the ledger's and the memory extension's are kept apart, so neither spends
// the other's. Of the ledger, a standing channel takes at most memoryStandingSpaceMax of its own track's entries and
// memoryStandingPersonalMax of personal ones from elsewhere, a hand-off at most memoryHandOffMax. Of the memory
// extension, a standing channel takes at most the budget it states (memoryExtensionBatch.standingBudget: by default the
// owner's choice, assistant.memory_extension_standing_bytes, 4096 unless changed; never more than the larger of that
// choice and memoryExtensionStandingMost) whichever scene its entries are in, a hand-off at most
// memoryExtensionHandOffMax.
const (
	memoryStandingSpaceMax    = 1536
	memoryStandingPersonalMax = 1536
	memoryHandOffMax          = 1536
	// memoryExtensionStandingDefault is the memory extension's standing budget when it states none and the owner kept
	// the default choice (memoryExtensionStandingOptions); about 30 short rules. Every main-conversation message's
	// snapshot may carry it (sent again only when it changes).
	memoryExtensionStandingDefault = 4096
	// memoryExtensionStandingMost caps the standing budget an extension may state while the owner's choice is below it,
	// as before the choice existed (#15312); a larger choice raises the cap to itself.
	memoryExtensionStandingMost = 8192
	// memoryExtensionStandingCeiling is the largest standing budget there is: the largest of
	// memoryExtensionStandingOptions.
	memoryExtensionStandingCeiling = 16384
	memoryExtensionHandOffMax      = 1536
	// memoryAssemblePages bounds how many pages of the ledger an assembly reads, newest first: past them only older
	// entries are left, which no budget would take before the newer ones.
	memoryAssemblePages = 10
	// memoryRelevantTerms is how many terms an entry shares with a task's or a message's text to bear on it.
	memoryRelevantTerms = 2
)

// assembleFor is where entries go.
type assembleFor struct {
	Role assembleRole
	// Space is the receiving conversation's track ("" the main one) and SpaceKnown whether it could be told: an entry
	// is in its own track (scene 1) only when it is known and the same. A hand-off and the Inbox have none.
	Space      string
	SpaceKnown bool
	// Topic are the categories that make sensitive entries relevant (normalCategories): the conversation's for a
	// recall, the confirmed plan's for a hand-off (PlanTask.MemoryTopic). TopicKnown: they could all be told.
	Topic      []string
	TopicKnown bool
	// Query is the text general entries must bear on for a hand-off or the Inbox (memoryRelevantTerms).
	Query string
	// ExtensionStanding is the memory extension's standing budget (memoryExtensionBatch.standingBudget), at most
	// memoryExtensionStandingCeiling; 0: memoryExtensionStandingDefault.
	ExtensionStanding int
}

// assembled is what an assembly carries, in the ledger's order, and the ids of the sensitive ones among them.
type assembled struct {
	Entries   []proto.AssistantEntry
	Sensitive []string
}

// IDs are the ids of the entries carried.
func (a assembled) IDs() []string {
	out := make([]string, 0, len(a.Entries))
	for _, e := range a.Entries {
		out = append(out, e.ID)
	}
	return out
}

// assembleEntries is entries (oldest first) as f takes them: each let through by entryCarried, then the newest that
// fit the role's budget, the ledger's and the memory extension's entries each spending their own (an entry that does
// not fit is left out, and a smaller older one may still fit). Pure: info is how usage sees each entry.
func assembleEntries(entries []proto.AssistantEntry, f assembleFor, info func(proto.AssistantEntry) infoUse) assembled {
	var out assembled
	spaceLeft, personalLeft, handOffLeft := memoryStandingSpaceMax, memoryStandingPersonalMax, memoryHandOffMax
	extStandingLeft, extHandOffLeft := cmp.Or(min(f.ExtensionStanding, memoryExtensionStandingCeiling), memoryExtensionStandingDefault), memoryExtensionHandOffMax
	for i := len(entries) - 1; i >= 0; i-- {
		e := entries[i]
		u := info(e)
		if !entryCarried(e, u, f) {
			continue
		}
		size := len(e.Text)
		ext := strings.HasPrefix(e.ID, memoryExtensionIDPrefix)
		var left *int
		switch {
		case f.Role == assembleStanding && ext:
			left = &extStandingLeft
		case f.Role == assembleStanding && entryScene(u, f) == sceneSourceTrack:
			left = &spaceLeft
		case f.Role == assembleStanding:
			left = &personalLeft
		case f.Role == assembleHandOff && ext:
			left = &extHandOffLeft
		case f.Role == assembleHandOff:
			left = &handOffLeft
		}
		if left != nil {
			if size > *left {
				continue
			}
			*left -= size
		}
		out.Entries = append(out.Entries, e)
		if u.sensitive() {
			out.Sensitive = append(out.Sensitive, e.ID)
		}
	}
	slices.Reverse(out.Entries)
	slices.Reverse(out.Sensitive)
	return out
}

// entryScene is the scene an entry of info is used in for f: its own track's when it goes to a conversation of that
// track, else elsewhere; a hand-off is always one, and the Inbox's helper always works for other people.
func entryScene(info infoUse, f assembleFor) scene {
	switch f.Role {
	case assembleHandOff:
		return sceneHandOff
	case assembleInbox:
		return sceneOthers
	}
	if f.SpaceKnown && info.SourceTrack == f.Space {
		return sceneSourceTrack
	}
	return sceneElsewhere
}

// entryCarried: entry e, of info, goes where f says, by usage's rule for its scene.
func entryCarried(e proto.AssistantEntry, info infoUse, f assembleFor) bool {
	switch usage(info, entryScene(info, f), extensionGrant{}) {
	case usageAllow:
		// Its own track's, or a general entry the Inbox's helper may use: there only when the message bears on it.
		return f.Role != assembleInbox || entryRelevant(e, f.Query)
	case usageRelevant:
		switch f.Role {
		case assembleStanding:
			return e.Scope.Personal // elsewhere's general entries are recalled, not standing, unless personal
		case assembleRecall:
			return true
		case assembleHandOff:
			return entryRelevant(e, f.Query)
		}
	case usageMinimal: // sensitive, elsewhere: recalled when on topic, never standing
		return f.Role == assembleRecall && onTopic(info, f)
	case usageConfirm: // sensitive, handed out (after the owner confirmed a plan on its topic) or sent to others (never)
		return f.Role == assembleHandOff && onTopic(info, f)
	}
	return false
}

// onTopic: f's topic carries every category of info, all known. An entry whose categories are not known is never on
// topic.
func onTopic(info infoUse, f assembleFor) bool {
	if !info.Known || !f.TopicKnown || len(info.Categories) == 0 {
		return false
	}
	for _, c := range info.Categories {
		if !slices.Contains(f.Topic, c) {
			return false
		}
	}
	return true
}

// entryRelevant: entry e's text and tags share memoryRelevantTerms terms with query. A placeholder for the ledger's
// entity prefetch, which replaces it.
func entryRelevant(e proto.AssistantEntry, query string) bool {
	q := memoryTerms(query)
	if len(q) == 0 {
		return false
	}
	n := 0
	for t := range memoryTerms(e.Text + " " + strings.Join(e.Tag, " ")) {
		if q[t] {
			n++
			if n >= memoryRelevantTerms {
				return true
			}
		}
	}
	return false
}

// memoryStopwords are English words of three letters or more too common to tell what a text is about: two texts
// sharing them share nothing (memoryTerms leaves them out).
var memoryStopwords = map[string]bool{
	"the": true, "and": true, "for": true, "are": true, "but": true, "not": true, "you": true, "all": true, "any": true,
	"can": true, "has": true, "had": true, "was": true, "our": true, "out": true, "its": true, "may": true, "who": true,
	"how": true, "did": true, "yes": true, "with": true, "this": true, "that": true, "from": true, "have": true,
	"will": true, "your": true, "they": true, "them": true, "then": true, "than": true, "into": true, "what": true,
	"when": true, "where": true, "which": true, "there": true, "their": true, "about": true, "been": true, "were": true,
	"also": true, "only": true, "just": true, "some": true, "such": true, "each": true, "more": true, "most": true,
	"very": true, "should": true, "would": true, "could": true, "please": true,
}

// memoryTerms are text's terms: its words of three letters or digits or more, lower-cased, but for memoryStopwords,
// and every two characters in a row of Chinese or Japanese.
func memoryTerms(text string) map[string]bool {
	out := map[string]bool{}
	var word, cjk []rune
	flush := func() {
		if w := strings.ToLower(string(word)); len(word) >= 3 && !memoryStopwords[w] {
			out[w] = true
		}
		for i := 0; i+1 < len(cjk); i++ {
			out[string(cjk[i:i+2])] = true
		}
		word, cjk = word[:0], cjk[:0]
	}
	for _, r := range text {
		switch {
		case unicode.In(r, unicode.Han, unicode.Hiragana, unicode.Katakana):
			if len(word) > 0 {
				flush()
			}
			cjk = append(cjk, r)
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if len(cjk) > 0 {
				flush()
			}
			word = append(word, r)
		default:
			flush()
		}
	}
	flush()
	return out
}

// memoryEntriesFor reads the active entries p selects (personal: the receiver may see personal ones), oldest first:
// the newest memoryAssemblePages pages of them; none without a ledger.
func (s *Service) memoryEntriesFor(ctx context.Context, p proto.AssistantEntriesParams, personal bool) ([]proto.AssistantEntry, error) {
	src := s.memoryEntrySrc()
	if src == nil {
		return nil, nil
	}
	p.Status, p.Limit, p.After = "active", 200, ""
	var out []proto.AssistantEntry
	var err error
	for range memoryAssemblePages {
		var res proto.AssistantEntriesResult
		if res, err = src.NewestAssistantEntries(ctx, p, personal); err != nil {
			break
		}
		out = append(out, res.Entries...)
		if !res.HasMore || res.After == "" {
			break
		}
		p.After = res.After
	}
	slices.Reverse(out)
	return out, err
}

// assembleMemory is the entries p selects, and the memory extension's (memoryExtensionEntries; for a standing channel
// only its standing ones), as f takes them.
func (s *Service) assembleMemory(ctx context.Context, f assembleFor, p proto.AssistantEntriesParams, personal bool) (assembled, error) {
	entries, err := s.memoryEntriesFor(ctx, p, personal)
	ext := s.memoryExtensionEntries(ctx, personal)
	if f.Role == assembleStanding {
		ext = ext.standingOnly()
	}
	f.ExtensionStanding = ext.standingBudget(s.memoryExtensionStandingBytes(ctx))
	all, info := s.memorySources(ctx, entries, ext)
	if len(all) == 0 {
		return assembled{}, err
	}
	return assembleEntries(all, f, info), err
}

// assembleForConversation is where entries go for conversation sid in role r: its track, and its categories as the
// topic (conversationUse).
func (s *Service) assembleForConversation(ctx context.Context, sid string, r assembleRole) assembleFor {
	space, known := s.conversationSpace(ctx, sid)
	u := s.conversationUse(ctx, sid)
	return assembleFor{Role: r, Space: space, SpaceKnown: known, Topic: u.Categories, TopicKnown: u.Known}
}

// standingMemory is what conversation sid's standing channel carries of the ledger: its own track's entries and
// personal general ones, the assistant's (executor assistant or any).
func (s *Service) standingMemory(ctx context.Context, sid string) (assembled, error) {
	f := s.assembleForConversation(ctx, sid, assembleStanding)
	return s.assembleMemory(ctx, f, proto.AssistantEntriesParams{Scope: &proto.AssistantEntryScopeFilter{Executor: "assistant"}}, true)
}

// recallMemory is entries, read on request in conversation sid (decisions_query), as a recall takes them.
func (s *Service) recallMemory(ctx context.Context, sid string, entries []proto.AssistantEntry) assembled {
	if len(entries) == 0 {
		return assembled{Entries: entries}
	}
	return assembleEntries(entries, s.assembleForConversation(ctx, sid, assembleRecall), s.memoryEntryUses(ctx, entries))
}

// inboxMemory is what the Inbox's helper may use of the ledger for message text: general entries the message bears
// on, never a sensitive one.
func (s *Service) inboxMemory(ctx context.Context, text string) (assembled, error) {
	f := assembleFor{Role: assembleInbox, Query: text}
	return s.assembleMemory(ctx, f, proto.AssistantEntriesParams{Scope: &proto.AssistantEntryScopeFilter{Executor: "assistant"}}, true)
}
