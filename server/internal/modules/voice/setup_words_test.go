package voice

import (
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Every code the extension may report has words in each language: the extension host shows them
// (internal/extmgr/voice_extension.go, hub-voice_extension.setup_* and warn_*).
func TestVoiceSetupWords(t *testing.T) {
	for _, l := range i18n.Langs {
		for _, key := range []string{"setup_" + SetupURLAndKey, "setup_" + SetupKey, "setup_" + SetupURL,
			"warn_" + WarnFormIDTaken, "warn_" + WarnFormNoKey, "warn_" + WarnDefaultUnknown,
			"warn_" + WarnTTSURL, "warn_" + WarnTTSModel, "warn_" + WarnTTSDefaultVoice, "warn_" + WarnTTSSampleRate,
			"tts_key_needs_url"} {
			if k := "hub-voice_extension." + key; i18n.T(l, k) == k {
				t.Errorf("%s: no %s", l, k)
			}
		}
	}
}
