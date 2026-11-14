package i18n

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
)

// InboxText recognizes only hub-owned inbox labels and errors, including old stored or RPC-flattened text.
// User/model content and arbitrary connector diagnostics are not reverse-translated.
func InboxText(l Lang, text string) string {
	if l == "" || text == "" {
		return text
	}
	forms := inboxTemplates()
	for _, n := range forms.candidates(text) {
		form := &forms.forms[n]
		match := form.match(text)
		if match == nil {
			continue
		}
		args := Args{}
		for i, name := range form.names {
			value := match[i+1]
			if name == "count" {
				n, err := strconv.ParseInt(value, 10, 64)
				if err != nil {
					return text
				}
				args[name] = n
			} else {
				args[name] = inboxTemplateArg(l, form.key, name, value)
			}
		}
		return T(l, form.key, args)
	}
	return text
}

// inboxTemplateArg translates only values that are hub-owned labels inside an already recognized template. Every other
// placeholder stays opaque: a person's name, a session title or a model's reason may happen to equal a label.
func inboxTemplateArg(l Lang, key, name, value string) string {
	const detail = "assistant-inbox_chain.triaged_detail"
	switch {
	case name == "tier" && (key == "assistant-inbox_chain.triaged" || key == "assistant-inbox_chain.triaged_urgent" || key == detail):
		return knownInboxLabels(l, value, "assistant-inbox_chain.tier_action", "assistant-inbox_chain.tier_fyi", "assistant-inbox_chain.tier_noise")
	case name == "reason" && key == detail:
		value = knownInboxLabels(l, value, "assistant-inbox.by_rules")
		return knownInboxPolicyReason(l, value)
	default:
		return value
	}
}

var inboxPolicyTemplates = sync.OnceValue(func() *templateIndex {
	return indexTemplates(makeCatalogTemplates(func(key string) bool { return key == "assistant-inbox.by_policy" }))
})

// knownInboxPolicyReason translates the fixed shell of "Your rule: {name}" when the whole reason matches it in any
// supported language. The rule's name is user data and stays verbatim, even when it is itself a known label.
func knownInboxPolicyReason(l Lang, value string) string {
	forms := inboxPolicyTemplates()
	for _, n := range forms.candidates(value) {
		form := &forms.forms[n]
		match := form.match(value)
		if match == nil {
			continue
		}
		args := Args{}
		for i, name := range form.names {
			args[name] = match[i+1]
		}
		return T(l, form.key, args)
	}
	return value
}

// knownInboxLabels finds value by its exact catalog text in any supported language. Unknown or user-written words stay
// as they are; matching by value is necessary because a stored chain does not record the language it was written in.
func knownInboxLabels(l Lang, value string, keys ...string) string {
	for _, key := range keys {
		for _, from := range Langs {
			if value == T(from, key) {
				return T(l, key)
			}
		}
	}
	return value
}

var inboxTemplates = sync.OnceValue(func() *templateIndex {
	return indexTemplates(makeCatalogTemplates(func(key string) bool {
		for _, prefix := range []string{"inbox-errors.", "assistant-inbox_agent.", "assistant-inbox_triage.", "assistant-inbox_admit.", "assistant-inbox_chain."} {
			if strings.HasPrefix(key, prefix) {
				return true
			}
		}
		return key == "assistant-inbox.by_rules" || key == "assistant-inbox.muted"
	}))
})

// InboxJSON translates copies at the public response boundary, including saved reply receipts. Only known
// item/status/error slots are visited: metadata, context, summaries, suggestions, names and user text stay intact.
// Return the original bytes when no recognized text changes, avoiding any impact on unrelated responses.
//
// What it does to a value depends on that value alone, so a list's items can be localized one at a time and the rest
// of the answer on its own, unless the answer is itself an item (InboxIsItem: then its list is not visited). When
// anything changed, the whole answer is marshaled again: an item that did not change then reads as InboxReencoded
// gives it (the hub's inbox list, hub/inbox_forms.go).
func InboxJSON(l Lang, raw []byte) []byte {
	if l == "" {
		return raw
	}
	value, ok := decodeInbox(raw)
	if !ok {
		return raw
	}
	changed := false
	translate := func(m map[string]any, key string) {
		if text, ok := m[key].(string); ok {
			if localized := InboxText(l, text); localized != text {
				m[key], changed = localized, true
			}
		}
	}
	var visit func(any)
	visit = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				visit(item)
			}
		case map[string]any:
			translate(x, "error")
			if inboxItem(x) {
				for _, field := range []string{"agent", "handling", "doc", "moment"} {
					if part, ok := x[field].(map[string]any); ok {
						translate(part, "error")
					}
				}
				if x["triaged_by"] == "rules" {
					translate(x, "reason")
				}
				if chain, ok := x["chain"].([]any); ok {
					for _, entry := range chain {
						if e, ok := entry.(map[string]any); ok {
							translate(e, "text")
							if e["kind"] == "failed" || e["kind"] == "triaged" {
								translate(e, "detail")
							}
						}
					}
				}
				return
			}
			for _, field := range []string{"items", "item", "changed"} {
				if nested, ok := x[field]; ok {
					visit(nested)
				}
			}
			if triage, ok := x["triage"].(map[string]any); ok {
				translate(triage, "reason")
				if sources, ok := triage["sources"].([]any); ok {
					for _, source := range sources {
						if s, ok := source.(map[string]any); ok {
							translate(s, "reason")
							translate(s, "read_reason")
						}
					}
				}
			}
		}
	}
	visit(value)
	if !changed {
		return raw
	}
	out, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return out
}

// InboxReencoded is raw as InboxJSON's marshaling writes it: decoded with its numbers as they are, then marshaled.
// Raw when it does not decode.
func InboxReencoded(raw []byte) []byte {
	value, ok := decodeInbox(raw)
	if !ok {
		return raw
	}
	out, err := json.Marshal(value)
	if err != nil {
		return raw
	}
	return out
}

// InboxIsItem: raw is an object InboxJSON reads as one item, whose own slots it localizes and nothing under it.
func InboxIsItem(raw []byte) bool {
	value, ok := decodeInbox(raw)
	if !ok {
		return false
	}
	m, ok := value.(map[string]any)
	return ok && inboxItem(m)
}

// inboxItem: m has an id, a source and a kind, so InboxJSON reads it as an item.
func inboxItem(m map[string]any) bool {
	id, ok := m["id"].(string)
	return ok && id != "" && m["source"] != nil && m["kind"] != nil
}

func decodeInbox(raw []byte) (any, bool) {
	if !json.Valid(raw) {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if dec.Decode(&value) != nil {
		return nil, false
	}
	return value, true
}
