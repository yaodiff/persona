package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func inboxVisibilitySource(cfg *assistantwire.InboxVisibilityConfig, source string) bool {
	return cfg.AppliesTo(source)
}

// inboxVisibilityMatch evaluates only stored metadata. Explicit ids match groups and direct chats; the direct-chat
// switch fails closed when a Lark item has no verifiable type. Non-Lark sources are untouched unless named explicitly.
func inboxVisibilityMatch(it *InboxItem, cfg *assistantwire.InboxVisibilityConfig) bool {
	if it == nil || !inboxVisibilitySource(cfg, it.Source) {
		return false
	}
	chatID, direct, known := "", false, false
	ambiguous := false
	if it.Lark != nil {
		chatID, direct, known = strings.TrimSpace(it.Lark.FeedID), !it.Lark.IsGroup, true
	} else if it.Connector != nil {
		for _, tag := range it.Connector.Tags {
			if tag.Key != connector.TagChat {
				continue
			}
			id := strings.TrimSpace(tag.Value)
			if chatID == "" {
				chatID = id
			} else if id != chatID {
				ambiguous = true
			}
			ambiguous = ambiguous || id == ""
		}
		switch it.Kind {
		case "dm", "bot":
			direct, known = true, true
		case "group", "mention", connectorwire.OwnerPromiseKind:
			known = true
		}
	}
	if (chatID == "" || ambiguous) && (cfg.DirectChats || len(cfg.Chats) > 0) {
		// Empty, whitespace-only or conflicting chat tags cannot prove that the row is outside either boundary.
		return true
	}
	for _, excluded := range cfg.Chats {
		if chatID != "" && strings.TrimSpace(excluded) == strings.TrimSpace(chatID) {
			return true
		}
	}
	return cfg.DirectChats && (!known || direct)
}

func inboxVisibilityExcluded(it *InboxItem, cfg *assistantwire.InboxVisibilityConfig) bool {
	return it != nil && (it.VisibilityExcluded || inboxVisibilityMatch(it, cfg))
}

// inboxVisibilityThingItem gives pre-admission batches the same metadata-only decision as stored items. No body or
// connector call is needed: migrated Lark blocks, generic chat tags/kind and the legacy chat:<feed> thread are enough.
func inboxVisibilityThingItem(source string, t connector.InboxThing) *InboxItem {
	it := &InboxItem{Source: source}
	fillConnectorItem(it, t)
	if t.Extra != nil && t.Extra.HasBlock() {
		setBlocks(it, t.Extra)
	}
	if it.Lark == nil && source == migratedConnectorID {
		if feed, ok := larkChatFeed(t.Thread); ok {
			it.Lark = &InboxLark{FeedID: feed, IsGroup: t.Kind == KindGroup || t.Kind == KindMention}
		}
	}
	return it
}

// inboxVisibilityBatchAllowed runs before pre-admission grouping can call its model, group tool, fallback or append.
// The fresh configuration is returned so candidate events from the same batch can be filtered with the same snapshot.
func (s *Service) inboxVisibilityBatchAllowed(ctx context.Context, source string, t connector.InboxThing) (assistantwire.InboxConfig, bool) {
	cfg, err := s.inboxConfigFresh(ctx)
	if err != nil {
		return assistantwire.InboxConfig{}, false
	}
	if inboxVisibilityExcluded(inboxVisibilityThingItem(source, t), cfg.Visibility) {
		s.quarantineInboxVisibility(ctx, cfg.Visibility)
		return cfg, false
	}
	return cfg, true
}

// inboxVisibilityWithMembersExcludedLocked also treats a survivor as excluded when any hidden merged member is
// excluded. Otherwise an old mixed merge could keep feeding private member text to triage, drafting and title models.
// The caller holds inbox.mu; the visited set also makes malformed historical merge cycles fail without recursing forever.
func inboxVisibilityWithMembersExcludedLocked(b *inboxBook, it *InboxItem, cfg *assistantwire.InboxVisibilityConfig) bool {
	if it == nil {
		return false
	}
	if stored := b.items[it.ID]; stored != nil {
		it = stored
	}
	visiting := map[*InboxItem]bool{}
	var excluded func(*InboxItem) bool
	excluded = func(current *InboxItem) bool {
		if current == nil || visiting[current] {
			return false
		}
		if inboxVisibilityExcluded(current, cfg) {
			return true
		}
		visiting[current] = true
		defer delete(visiting, current)
		for _, ref := range current.MergedFrom {
			member := b.items[ref.ID]
			if member != nil && member.MergedInto == current.ID && excluded(member) {
				return true
			}
		}
		for _, ref := range current.VisibilityContentRefs {
			member := b.items[ref.ID]
			if member == nil {
				// The survivor still holds the contributor's text. A selected source whose metadata row was purged cannot
				// prove that retained text is outside the active boundary.
				if cfg != nil && (cfg.DirectChats || len(cfg.Chats) > 0) && cfg.AppliesTo(ref.Source) {
					return true
				}
				continue
			}
			if excluded(member) {
				return true
			}
		}
		return false
	}
	return excluded(it)
}

// inboxVisibilityProviderAllowed requires positive provider support before any selected source's content reaches a
// model. The two Lark providers implement the boundary; every other source must declare it through the extension
// manager. This also protects transient dry-run items, which have no stored quarantine marker.
func (s *Service) inboxVisibilityProviderAllowed(cfg *assistantwire.InboxVisibilityConfig, source string) bool {
	if cfg == nil || !cfg.DirectChats && len(cfg.Chats) == 0 || !cfg.AppliesTo(source) {
		return true
	}
	if source == "lark" || source == "larkcli" {
		return true
	}
	provider, ok := s.ext.(interface{ InboxVisibilityProvider(string) bool })
	return ok && provider.InboxVisibilityProvider(source)
}

// inboxVisibilityModelAllowed authorizes one item immediately before any model prompt is built from its content.
// The display cache is not an authorization input. Existing items are quarantined in the same pass; callers handling
// a not-yet-admitted item persist the excluded verdict themselves. A configuration error never permits a model read.
func (s *Service) inboxVisibilityModelAllowed(ctx context.Context, it *InboxItem) (bool, error) {
	cfg, err := s.inboxConfigFresh(ctx)
	if err != nil {
		return false, err
	}
	b := &s.inbox
	b.mu.Lock()
	source := ""
	if it != nil {
		if stored := b.items[it.ID]; stored != nil {
			source = stored.Source
		} else {
			source = it.Source
		}
	}
	excluded := inboxVisibilityWithMembersExcludedLocked(b, it, cfg.Visibility)
	b.mu.Unlock()
	if !s.inboxVisibilityProviderAllowed(cfg.Visibility, source) || excluded {
		s.quarantineInboxVisibility(ctx, cfg.Visibility)
		return false, nil
	}
	return true, nil
}

// inboxVisibilityToolRefusal is the manager-side veto for an explicitly listed target or a read an older provider
// cannot authorize. A provider declaring CapInboxVisibility performs the positive group-type preflight itself; without
// that declaration the all-direct-chats switch fails closed instead of guessing from an unverifiable chat id.
func (s *Service) inboxVisibilityToolRefusal(ctx context.Context, machine, session, origin, name string, args json.RawMessage) (proto.ToolResult, bool) {
	if !s.isInboxModelCall(ctx, machine, session, origin) {
		return proto.ToolResult{}, false
	}
	source, kind := "", ""
	generic := false
	switch name {
	case "lark_messages":
		source, kind = "lark", "messages"
	case "lark_search":
		source, kind = "lark", "search"
	case "lark_chats":
		source, kind = "lark", "chats"
	case "larkcli_messages":
		source, kind = "larkcli", "messages"
	case "larkcli_search":
		source, kind = "larkcli", "search"
	case "larkcli_chats":
		source, kind = "larkcli", "chats"
	default:
		resolve, ok := s.ext.(interface{ InboxVisibilitySource(string) (string, bool) })
		if !ok {
			return proto.ToolResult{}, false
		}
		source, ok = resolve.InboxVisibilitySource(name)
		if !ok || source == "" {
			return proto.ToolResult{}, false
		}
		generic = true
	}
	denied := func() (proto.ToolResult, bool) {
		return proto.TextResult("This chat is excluded from AI and knowledge-base reads.", true), true
	}
	// This is an authorization read, not a display read: a newly enabled boundary cannot wait for the cache to expire.
	fullCfg, err := s.inboxConfigFresh(ctx)
	if err != nil {
		return denied()
	}
	cfg := fullCfg.Visibility
	if cfg == nil || !cfg.DirectChats && len(cfg.Chats) == 0 {
		return proto.ToolResult{}, false
	}
	if !inboxVisibilitySource(cfg, source) {
		return proto.ToolResult{}, false
	}
	preflight, ok := s.ext.(interface{ InboxVisibilityPreflight(string) bool })
	providerChecks := ok && preflight.InboxVisibilityPreflight(name)
	if generic {
		// Arbitrary connector ids are supported by visibility.sources. A provider without the positive preflight
		// capability cannot prove that its response omitted an excluded body, whatever its tool is named. A capable
		// provider owns its source-specific argument schema; global search remains unavailable in either case.
		if strings.HasSuffix(name, "_search") || !providerChecks {
			return denied()
		}
		return proto.ToolResult{}, false
	}
	if kind == "search" {
		// Result-level filtering cannot prove that a global search omitted an excluded body.
		return denied()
	}
	var target struct {
		ChatID string `json:"chat_id"`
		Chat   string `json:"chat"`
		FeedID string `json:"feed_id"`
		Thread string `json:"thread"`
		Filter string `json:"filter"`
	}
	if len(args) == 0 || json.Unmarshal(args, &target) != nil {
		return denied()
	}
	if kind == "chats" {
		switch strings.TrimSpace(target.Filter) {
		case "", "recent", "unread":
			// An older provider's list can include an explicitly excluded preview, and cannot establish that every row
			// is a group. Without a provider-side preflight, no chat-list response is authorized under either control.
			if !providerChecks {
				return denied()
			}
			return proto.ToolResult{}, false
		case "at_me":
			// This mode is implemented by a body-returning message search on older providers. The current provider
			// contract disables it whenever either visibility control is on.
			return denied()
		default:
			if !providerChecks {
				return denied()
			}
			return proto.ToolResult{}, false
		}
	}
	if strings.TrimSpace(target.Thread) != "" {
		// Lark runners prefer thread over chat_id when both are supplied. The allowed chat_id is not evidence about
		// the thread, whose own chat cannot be verified before its bodies are returned.
		return denied()
	}
	chatID := strings.TrimSpace(target.ChatID)
	if chatID == "" {
		chatID = strings.TrimSpace(target.Chat)
	}
	if chatID == "" {
		chatID = strings.TrimSpace(target.FeedID)
	}
	if chatID == "" {
		return denied()
	}
	for _, excluded := range cfg.Chats {
		if chatID == strings.TrimSpace(excluded) {
			return denied()
		}
	}
	if cfg.DirectChats && !providerChecks {
		return denied()
	}
	return proto.ToolResult{}, false
}

// quarantineInboxVisibility makes the source boundary retroactive without deleting content or sent-attempt audit.
// The persistent marker also closes old suggestions and direct confirmation calls, including if a source later
// redelivers the same stored item. A matched item's unfinished handling returns to the owner in the same pass.
func (s *Service) quarantineInboxVisibility(ctx context.Context, cfg *assistantwire.InboxVisibilityConfig) {
	b := &s.inbox
	b.mu.Lock()
	var writes []*inboxWrite
	now := time.Now()
	for _, it := range b.items {
		if !inboxVisibilityWithMembersExcludedLocked(b, it, cfg) {
			continue
		}
		active, pending := inboxAgentReleaseState(it)
		if it.VisibilityExcluded && !active && !pending {
			continue
		}
		w := beginWrite(it)
		it.VisibilityExcluded = true
		if active {
			b.setHandling(it, HandlingDone, "", now)
			b.chainAdd(it, InboxChainEntry{Kind: ChainConcluded, At: now.UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_agent.never")})
		}
		if a := it.Agent; a != nil {
			a.Pending, a.Wake, a.Back, a.RetryAfter = nil, false, "", nil
			a.DeferredAt, a.WakeSlot = nil, false
			// Detach a live handling session as well as releasing its queue state. Its later tool calls can no longer
			// find this item, while the origin-based outbound policy remains fail closed.
			a.MachineID, a.SessionID = "", ""
			if it.Handling != nil {
				it.Handling.MachineID, it.Handling.SessionID = "", ""
			}
		}
		b.settle(w)
		writes = append(writes, w)
	}
	b.mu.Unlock()
	s.saveWrites(ctx, writes, "the chat visibility quarantine")
}
