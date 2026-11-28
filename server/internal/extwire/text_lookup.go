package extwire

// TextLookupEventType names the events of the text_lookup extension point (docs/extensions/extensions.md
// "Looking up a word").
type TextLookupEventType string

// EventTextLookupChanged tells the apps to read GET /api/v1/text-lookup again: the dictionary came or went, or a
// word was saved or removed.
const EventTextLookupChanged TextLookupEventType = "text_lookup_changed"

// TextLookupChanged is the text_lookup_changed event the manager broadcasts; on the wire {"type":"text_lookup_changed"}.
type TextLookupChanged struct {
	Type TextLookupEventType `json:"type"`
}
