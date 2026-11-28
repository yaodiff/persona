package larkcli

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// InboxOptions is the complete lark-cli connector settings object. Chat and
// document Inbox settings share one object so options.get/options.set can
// round-trip them without one poller overwriting the other.
type InboxOptions struct {
	InboxAllChats      []string `json:"inbox_all_chats"`
	OwnerPromiseGroups []string `json:"owner_promise_groups,omitempty"`
	OwnerPromiseModel  bool     `json:"owner_promise_model,omitempty"`
	// InboxExcludeChats and InboxExcludeDirectChats remove matching chats from AI collection and read tools. Both
	// controls are independent, and their zero values preserve the legacy behavior.
	InboxExcludeChats       []string `json:"inbox_exclude_chats"`
	InboxExcludeDirectChats bool     `json:"inbox_exclude_direct_chats"`
	Documents               []string `json:"documents"`
	WikiSpaces              []string `json:"wiki_spaces"`
	// OwnedAllComments: every comment on a watched document the user owns (DocumentOptions).
	OwnedAllComments bool `json:"owned_all_comments"`
	// TriageRules: what counts as Needs you / FYI / Collapsed for lark-cli's items, in the user's words.
	TriageRules string `json:"triage_rules"`
}

func (c *Connector) Options() json.RawMessage {
	c.ensureDocuments()
	c.inboxMu.Lock()
	ids := make([]string, 0, len(c.inboxAllChats))
	for id := range c.inboxAllChats {
		ids = append(ids, id)
	}
	excluded := make([]string, 0, len(c.inboxExcludeChats))
	for id := range c.inboxExcludeChats {
		excluded = append(excluded, id)
	}
	var promiseGroups []string
	for id := range c.ownerPromiseGroups {
		promiseGroups = append(promiseGroups, id)
	}
	promiseModel := c.ownerPromiseModel
	excludeDirect := c.inboxExcludeDirectChats
	rules := c.triageRules
	c.inboxMu.Unlock()
	sort.Strings(ids)
	sort.Strings(excluded)
	sort.Strings(promiseGroups)

	c.docs.mu.Lock()
	documents := c.docs.options.normalized()
	c.docs.mu.Unlock()
	if documents.Documents == nil {
		documents.Documents = []string{}
	}
	if documents.WikiSpaces == nil {
		documents.WikiSpaces = []string{}
	}
	b, _ := json.Marshal(InboxOptions{
		InboxAllChats:           ids,
		OwnerPromiseGroups:      promiseGroups,
		OwnerPromiseModel:       promiseModel,
		InboxExcludeChats:       excluded,
		InboxExcludeDirectChats: excludeDirect,
		Documents:               documents.Documents,
		WikiSpaces:              documents.WikiSpaces,
		OwnedAllComments:        documents.OwnedAllComments,
		TriageRules:             rules,
	})
	return b
}

func (c *Connector) SetOptions(raw json.RawMessage) error {
	var wire struct {
		InboxAllChats           json.RawMessage `json:"inbox_all_chats"`
		OwnerPromiseGroups      *[]string       `json:"owner_promise_groups"`
		OwnerPromiseModel       *bool           `json:"owner_promise_model"`
		InboxExcludeChats       json.RawMessage `json:"inbox_exclude_chats"`
		InboxExcludeDirectChats bool            `json:"inbox_exclude_direct_chats"`
		TriageRules             *string         `json:"triage_rules"`
	}
	if err := json.Unmarshal(raw, &wire); err != nil {
		return fmt.Errorf("options: %v", err)
	}

	var idsValue []string
	if len(wire.InboxAllChats) > 0 && string(wire.InboxAllChats) != "null" {
		if err := json.Unmarshal(wire.InboxAllChats, &idsValue); err != nil {
			return fmt.Errorf("options.inbox_all_chats: must be an array of chat ids")
		}
	}
	seen := make(map[string]struct{}, len(idsValue))
	chats := make(map[string]struct{}, len(idsValue))
	for _, id := range idsValue {
		id = strings.TrimSpace(id)
		if id == "" {
			return fmt.Errorf("options.inbox_all_chats contains an empty chat id")
		}
		if len(id) > 256 {
			return fmt.Errorf("options.inbox_all_chats chat id is too long")
		}
		if _, ok := seen[id]; ok {
			return fmt.Errorf("options.inbox_all_chats contains duplicate chat id %q", id)
		}
		seen[id] = struct{}{}
		chats[id] = struct{}{}
	}

	var promiseGroups map[string]struct{}
	if wire.OwnerPromiseGroups != nil {
		if len(*wire.OwnerPromiseGroups) > maxOwnerPromiseGroups {
			return fmt.Errorf("options.owner_promise_groups allows up to %d group ids", maxOwnerPromiseGroups)
		}
		promiseGroups = make(map[string]struct{}, len(*wire.OwnerPromiseGroups))
		for _, id := range *wire.OwnerPromiseGroups {
			id = strings.TrimSpace(id)
			if !strings.HasPrefix(id, "oc_") || !validOpaqueID(id) {
				return fmt.Errorf("options.owner_promise_groups must contain Lark group ids")
			}
			if _, exists := promiseGroups[id]; exists {
				return fmt.Errorf("options.owner_promise_groups contains duplicate group ids")
			}
			promiseGroups[id] = struct{}{}
		}
	}
	excluded, err := parseExcludedChatIDs(wire.InboxExcludeChats)
	if err != nil {
		return err
	}
	documents, err := parseDocumentOptions(raw)
	if err != nil {
		return err
	}
	c.ensureDocuments()
	rules := ""
	if wire.TriageRules != nil {
		rules = strings.TrimSpace(*wire.TriageRules)
	}
	if len([]rune(rules)) > maxTriageRules {
		return fmt.Errorf("options.triage_rules is longer than %d characters", maxTriageRules)
	}
	c.inboxMu.Lock()
	c.inboxAllChats = chats
	if wire.OwnerPromiseGroups != nil {
		c.ownerPromiseGroups = promiseGroups
		for id := range c.inboxState.PromiseSince {
			if _, selected := promiseGroups[id]; !selected {
				delete(c.inboxState.PromiseSince, id)
			}
		}
	}
	if wire.OwnerPromiseModel != nil {
		c.ownerPromiseModel = *wire.OwnerPromiseModel
	}

	c.inboxExcludeChats = excluded
	c.inboxExcludeDirectChats = wire.InboxExcludeDirectChats
	c.triageRules = rules
	c.inboxMu.Unlock()
	c.docs.setOptions(documents)
	return nil
}

// maxTriageRules is the Inbox's limit for a source's rules.
const maxTriageRules = 4000

// rules are the user's triage rules for lark-cli's items.
func (c *Connector) rules() string {
	c.inboxMu.Lock()
	defer c.inboxMu.Unlock()
	return c.triageRules
}

func (c *Connector) ensureDocuments() {
	if c.docs == nil {
		c.docs = newDocumentInbox()
	}
}
