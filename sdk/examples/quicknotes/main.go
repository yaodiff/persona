// Quick notes: the sample of docs/extensions/core-only-features.md, a feature added to a Persona Core install without
// changing Core. It is an extension, installed onto a running hub: one page on the main navigation, one app-facing
// route (/api/v1/quicknotes/notes), one hub feature flag ("quicknotes") and one stored value (notes.json in its data
// directory). Switched off, its page, route and flag are gone and the stored notes stay as they are.
package main

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"time"

	"github.com/nwverse/persona/sdk/go/ext"
)

//go:embed persona-extension.json
var manifest []byte

func main() { ext.Main(program(&app{now: time.Now})) }

func program(a *app) ext.Program {
	var m struct{ ID, Version string }
	_ = json.Unmarshal(manifest, &m)
	p := ext.Program{
		ID: m.ID, Version: m.Version, Manifest: manifest,
		Init: a.init,
		// Contribution ids are the manifest's.
		UI: map[string]ext.Page{"quicknotes": {Render: a.home, Action: a.act}},
	}
	// The route is served on the socket the hub gives (runtime.http). `personad extension test` and the conformance
	// kit start the program without one, to check the protocol alone. The book is opened in Init, before any request.
	if ext.Env().Socket != "" {
		p.HTTP = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { a.routes().ServeHTTP(w, r) })
	}
	return p
}
