// Package briefing gathers scheduled routine facts, writes the conversation feed, renders the
// delivery cards and handles the feed's deferred questions. It reaches the core through modapi.
package briefing

import (
	_ "embed"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/modapi"
)

//go:embed module.json
var manifest []byte

// Module is the briefing module.
type Module struct{}

// Info is the embedded manifest (module.json, checked against schema/module.schema.json).
func (Module) Info() modapi.Info {
	var info modapi.Info
	if err := json.Unmarshal(manifest, &info); err != nil {
		panic("briefing: module.json: " + err.Error()) // embedded and tested: never at run time
	}
	return info
}

// Register adds the wait setting and the routine facts' wait rules and runtime runner.
func (Module) Register(h modapi.Host) error {
	if err := h.AddSetting(modapi.Setting{Key: SettingKey, Value: value, Check: check, Describe: describe}); err != nil {
		return err
	}
	r := newRunner(h)
	if err := h.AddHubRoute(handleLaterRoute, r.handleLater); err != nil {
		return err
	}
	return h.AddScheduleFacts(modapi.ScheduleFacts{
		Setting: SettingKey,
		Waits: func(stored json.RawMessage) modapi.WaitRules {
			return waitsOf(stored)
		},
		Runner: r,
	})
}
