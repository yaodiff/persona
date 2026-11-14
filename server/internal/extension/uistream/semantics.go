package uistream

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"

	"github.com/nwverse/persona/sdk/go/wire"
)

// Supplement legacy semantic checks at the stream boundary, without changing
// snapshot-only rendering or its permissive codecs.
func checkBindings(view *wire.View) error {
	name := func(n string) error {
		if strings.HasPrefix(n, wire.ReservedName) {
			return errors.New("reserved native field name")
		}
		return wire.ValidateJSON("UIStreamID", mustJSON(n))
	}
	for _, n := range []string{
		optionalName(view.Filter, func(v *wire.Filter) string { return v.Name }),
		optionalName(view.Tabs, func(v *wire.Tabs) string { return v.Name }),
		optionalName(view.Review, func(v *wire.Review) string { return v.Name }),
	} {
		if n != "" {
			if err := name(n); err != nil {
				return err
			}
		}
	}
	for _, section := range view.Sections {
		for _, item := range section.Items {
			if item.Type == "field" || item.Type == "composer" || item.Type == "segmented" {
				if err := name(item.Name); err != nil {
					return err
				}
			}
			if item.Control != nil {
				if err := name(item.Control.Name); err != nil {
					return err
				}
			}
			for _, tile := range item.Tiles {
				if tile.Control != nil {
					if err := name(tile.Control.Name); err != nil {
						return err
					}
				}
			}
		}
	}
	for _, action := range wire.Actions(view) {
		for key := range action.Values {
			if err := name(key); err != nil {
				return fmt.Errorf("action %s value name: %w", action.ID, err)
			}
		}
	}
	return nil
}
func optionalName[T any](value *T, get func(*T) string) string {
	if value == nil {
		return ""
	}
	return get(value)
}

// Native media and code line links use uncredentialed HTTPS. Legacy avatar and
// line-link checks omit this restriction, so inspect their source properties here.
// Native schema/strict generated shape has already excluded arbitrary guest properties.
func checkSourcePresence(view []byte) error {
	var visit func(any) error
	visit = func(value any) error {
		switch obj := value.(type) {
		case map[string]any:
			for _, field := range []string{"url", "line_url"} {
				if raw, ok := obj[field].(string); ok {
					if field == "line_url" {
						if raw == "" {
							continue // An explicit empty optional link clears the previous link.
						}
						raw = strings.NewReplacer("{start}", "1", "{end}", "1").Replace(raw)
					}
					u, err := url.Parse(raw)
					if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
						return errors.New("native URL requires uncredentialed HTTPS")
					}
				}
			}
			// Presence matters here: explicit on:false belongs only to a switch, even
			// though the legacy bool-based semantic check cannot distinguish absence.
			if control, ok := obj["control"].(map[string]any); ok {
				if _, present := control["on"]; present && control["kind"] != "switch" {
					return errors.New("on property is a switch's")
				}
			}
			for name, child := range obj {
				if name != "values" {
					if err := visit(child); err != nil {
						return err
					}
				}
			}
		case []any:
			for _, child := range obj {
				if err := visit(child); err != nil {
					return err
				}
			}
		}
		return nil
	}
	var source any
	// This is canonical native JSON, not a normalized DTO.
	if err := json.Unmarshal(view, &source); err != nil {
		return err
	}
	return visit(source)
}
