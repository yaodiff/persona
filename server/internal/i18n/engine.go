package i18n

import (
	"slices"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/proto"
)

// EngineText words a note a node wrote on one of its engines (proto.Engine.Note) in l. The node writes the canonical
// Chinese of the node-engine catalog (the Paseo engine's notes, node/paseobridge.go; an incompatible Orca's,
// node/engine.go, whose canonical words are those nodes wrote before the catalog had them), and the hub matches it at
// the response boundary, as ComputerText does for a computer's. Any other note (an older node's own words) stays as
// it is, and so does the reason a note carries: what Node.js or the bridge printed is diagnostic data.
func EngineText(l Lang, text string) string {
	if text == "" || l == "" {
		return text
	}
	forms := engineTemplates()
	for _, n := range forms.candidates(text) {
		form := &forms.forms[n]
		match := form.match(text)
		if match == nil {
			continue
		}
		args := Args{}
		for i, name := range form.names {
			args[name] = match[i+1] // versions and reasons are data
		}
		return T(l, form.key, args)
	}
	return text
}

var engineTemplates = sync.OnceValue(func() *templateIndex {
	return indexTemplates(makeCatalogTemplates(func(key string) bool { return strings.HasPrefix(key, "node-engine.") }))
})

// EngineNotes is engines with each note in l: a copy when a note changes, never the slice it was given (a machine's
// snapshot is shared by every reader).
func EngineNotes(l Lang, engines []proto.Engine) []proto.Engine {
	var out []proto.Engine
	for i, e := range engines {
		note := EngineText(l, e.Note)
		if note == e.Note {
			continue
		}
		if out == nil {
			out = slices.Clone(engines)
		}
		out[i].Note = note
	}
	if out == nil {
		return engines
	}
	return out
}
