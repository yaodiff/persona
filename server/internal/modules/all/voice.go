//go:build !persona_without_voice && !persona_core_only

package all

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"

	"github.com/nwverse/persona/server/internal/builtinext"
	"github.com/nwverse/persona/server/internal/modules/voice"
)

// The voice module: its program (`personad voice`) and what the extension host needs of its extension.
func init() {
	modules = append(modules, voice.Module{})
	builtinext.AddProgram(voice.Module{}.Info().ID, "voice", `the built-in voice extension: the hub runs it (hub.json extensions, docs/extensions/extensions.md "Built-in voice extension")`,
		func(ctx context.Context, _ string) error { return voice.Run(ctx) })
	builtinext.ProvideVoice(voice.Module{}.Info().ID, builtinext.Voice{
		TranscriptionContribution: voice.TranscriptionContribution,
		GatewayProvider: func(config json.RawMessage, name, key string) (json.RawMessage, bool) {
			prov, ok := voice.GatewayProviderFromConfig(config, name, key)
			if !ok {
				return nil, false
			}
			b, _ := json.Marshal(prov)
			return b, true
		},
		State: func(s builtinext.VoiceSources) (string, []string, error) {
			cfg, err := voice.Merge(voice.Sources{Form: s.Form, Key: s.Key, TTSKey: s.TTSKey, HubJSON: s.HubJSON, Gateway: s.Gateway})
			if err != nil {
				return "", nil, err
			}
			return cfg.Setup, cfg.Warnings, nil
		},
		FormURL: func(config json.RawMessage) string {
			var f voice.Form
			_ = json.Unmarshal(config, &f)
			return strings.TrimSpace(f.URL)
		},
		FormTTSURL: func(config json.RawMessage) string {
			var f voice.Form
			_ = json.Unmarshal(config, &f)
			return strings.TrimSpace(f.TTSURL)
		},
		Handler: func(config json.RawMessage, hubURL, hubToken string, logger *log.Logger) (http.Handler, error) {
			cfg, err := voice.Merge(voice.Sources{HubJSON: config})
			if err != nil {
				return nil, err
			}
			return voice.New(cfg, hubURL, hubToken, logger).Handler(), nil
		},
	})
}
