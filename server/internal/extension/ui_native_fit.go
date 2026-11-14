package extension

import (
	"bytes"
	"encoding/json"
	"reflect"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// FitNative uses the existing fitting rules on detached DTOs while preserving source
// presence for unchanged native properties. The input is strict native vocabulary;
// assistant fields and executable/unknown properties can never be restored.
func FitNative(raw []byte, slot UISlot, version int, lang i18n.Lang) (json.RawMessage, error) {
	// Validated as UIStreamView (which holds it to View too), decoded and
	// checked as CheckViewJSON checks it: that View is the one fitted.
	view, err := wire.DecodeCheckedStreamViewJSON(raw)
	if err != nil {
		return nil, err
	}
	before, err := json.Marshal(view)
	if err != nil {
		return nil, err
	}
	if _, err = fit(&view, slot, version, true, lang); err != nil {
		return nil, err
	}
	after, err := json.Marshal(view)
	if err != nil {
		return nil, err
	}
	var source any
	_ = json.Unmarshal(raw, &source)
	if !bytes.Equal(before, after) {
		var old, next any
		_ = json.Unmarshal(before, &old)
		_ = json.Unmarshal(after, &next)
		source = restorePresence(source, old, next)
	}
	// Equal bytes decode to equal values, which restorePresence answers with
	// source itself: a view that fitting left alone skips both decodes.
	fitted, err := json.Marshal(source)
	if err != nil {
		return nil, err
	}
	if err = wire.CheckFittedJSON(fitted); err != nil {
		return nil, err
	}
	return fitted, nil
}

func restorePresence(source, old, next any) any {
	if reflect.DeepEqual(old, next) {
		return source
	}
	switch n := next.(type) {
	case map[string]any:
		s, sok := source.(map[string]any)
		b, bok := old.(map[string]any)
		if !sok || !bok || s["type"] != n["type"] || s["key"] != n["key"] {
			return next
		}
		for key, value := range n {
			if original, ok := s[key]; ok {
				n[key] = restorePresence(original, b[key], value)
			}
		}
		for key, value := range s {
			if _, exists := n[key]; !exists {
				if _, encoded := b[key]; !encoded {
					n[key] = value
				}
			}
		}
		return n
	case []any:
		s, sok := source.([]any)
		b, bok := old.([]any)
		if !sok || !bok {
			return next
		}
		// Native keyed lists may have lost/replaced elements during fitting. Never
		// restore an old sibling's data at a shifted array index.
		previousKeys := make(map[any]int, len(b))
		for i, candidate := range b {
			if node, ok := candidate.(map[string]any); ok && node["key"] != nil {
				previousKeys[node["key"]] = i
			}
		}
		for i, value := range n {
			index := i
			if node, ok := value.(map[string]any); ok && node["key"] != nil {
				var found bool
				index, found = previousKeys[node["key"]]
				if !found {
					index = -1
				}
			}
			if index >= 0 && index < len(s) && index < len(b) {
				n[i] = restorePresence(s[index], b[index], value)
			}
		}
		return n
	}
	return next
}
