package voice

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestDeriveTTSURLs(t *testing.T) {
	tests := []struct {
		name       string
		raw        string
		wantSpeech string
		wantVoices string
		wantOK     bool
	}{
		{
			name:       "https base",
			raw:        "https://tts.example.com",
			wantSpeech: "https://tts.example.com/v1/audio/speech",
			wantVoices: "https://tts.example.com/v1/audio/voices",
			wantOK:     true,
		},
		{
			name:       "https base trailing slash",
			raw:        "https://tts.example.com/",
			wantSpeech: "https://tts.example.com/v1/audio/speech",
			wantVoices: "https://tts.example.com/v1/audio/voices",
			wantOK:     true,
		},
		{
			name:       "https base with port",
			raw:        "https://tts.example.com:8880",
			wantSpeech: "https://tts.example.com:8880/v1/audio/speech",
			wantVoices: "https://tts.example.com:8880/v1/audio/voices",
			wantOK:     true,
		},
		{
			name:       "http base with prefix",
			raw:        "http://127.0.0.1:8880/tts",
			wantSpeech: "http://127.0.0.1:8880/tts/v1/audio/speech",
			wantVoices: "http://127.0.0.1:8880/tts/v1/audio/voices",
			wantOK:     true,
		},
		{
			name:       "http base with prefix trailing slash",
			raw:        "http://127.0.0.1:8880/tts/",
			wantSpeech: "http://127.0.0.1:8880/tts/v1/audio/speech",
			wantVoices: "http://127.0.0.1:8880/tts/v1/audio/voices",
			wantOK:     true,
		},
		{
			name:       "ipv6 base",
			raw:        "http://[::1]:8880",
			wantSpeech: "http://[::1]:8880/v1/audio/speech",
			wantVoices: "http://[::1]:8880/v1/audio/voices",
			wantOK:     true,
		},
		{
			name:       "uppercase scheme normalized",
			raw:        "HTTPS://tts.example.com/prefix",
			wantSpeech: "https://tts.example.com/prefix/v1/audio/speech",
			wantVoices: "https://tts.example.com/prefix/v1/audio/voices",
			wantOK:     true,
		},
		{
			name:       "full speech url",
			raw:        "https://tts.example.com/v1/audio/speech",
			wantSpeech: "https://tts.example.com/v1/audio/speech",
			wantVoices: "https://tts.example.com/v1/audio/voices",
			wantOK:     true,
		},
		{
			name:       "full speech url trailing slash",
			raw:        "https://tts.example.com/v1/audio/speech/",
			wantSpeech: "https://tts.example.com/v1/audio/speech",
			wantVoices: "https://tts.example.com/v1/audio/voices",
			wantOK:     true,
		},
		{
			name:       "full speech url with prefix and port",
			raw:        "http://tts.example.com:9000/api/tts/v1/audio/speech",
			wantSpeech: "http://tts.example.com:9000/api/tts/v1/audio/speech",
			wantVoices: "http://tts.example.com:9000/api/tts/v1/audio/voices",
			wantOK:     true,
		},
		{
			name:       "trimmed whitespace around valid url",
			raw:        "  https://tts.example.com  ",
			wantSpeech: "https://tts.example.com/v1/audio/speech",
			wantVoices: "https://tts.example.com/v1/audio/voices",
			wantOK:     true,
		},
		{
			name:       "suffix without leading slash treated as base",
			raw:        "https://tts.example.com/foov1/audio/speech",
			wantSpeech: "https://tts.example.com/foov1/audio/speech/v1/audio/speech",
			wantVoices: "https://tts.example.com/foov1/audio/speech/v1/audio/voices",
			wantOK:     true,
		},
		{name: "empty", raw: "", wantOK: false},
		{name: "spaces only", raw: "   ", wantOK: false},
		{name: "wss rejected", raw: "wss://tts.example.com/v1/audio/speech", wantOK: false},
		{name: "ws rejected", raw: "ws://tts.example.com", wantOK: false},
		{name: "ftp rejected", raw: "ftp://tts.example.com", wantOK: false},
		{name: "no scheme", raw: "tts.example.com", wantOK: false},
		{name: "no host", raw: "https://", wantOK: false},
		{name: "userinfo rejected", raw: "https://user:pass@tts.example.com", wantOK: false},
		{name: "query rejected", raw: "https://tts.example.com/v1/audio/speech?key=1", wantOK: false},
		{name: "fragment rejected", raw: "https://tts.example.com#frag", wantOK: false},
		{name: "embedded space rejected", raw: "https://tts.example.com/my path", wantOK: false},
		{name: "port zero rejected", raw: "https://tts.example.com:0", wantOK: false},
		{name: "port too high rejected", raw: "https://tts.example.com:70000", wantOK: false},
		{name: "opaque rejected", raw: "http:tts.example.com", wantOK: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			speech, voices, ok := DeriveTTSURLs(tc.raw)
			if ok != tc.wantOK || speech != tc.wantSpeech || voices != tc.wantVoices {
				t.Fatalf("DeriveTTSURLs(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.raw, speech, voices, ok, tc.wantSpeech, tc.wantVoices, tc.wantOK)
			}
		})
	}
}

func TestMergeTTSConfig(t *testing.T) {
	baseHub := json.RawMessage(`{"providers":[{"id":"rt","url":"wss://rt.example.com/v1/live","models":["m1"]}]}`)

	// Without TTS configured, behavior is unchanged.
	c, err := Merge(Sources{HubJSON: baseHub})
	if err != nil {
		t.Fatal(err)
	}
	if c.HasTTS() || c.TTS().Configured() || len(c.Warnings) != 0 {
		t.Fatalf("without TTS: has=%v ep=%+v warnings=%v", c.HasTTS(), c.TTS(), c.Warnings)
	}

	// Form TTS settings + secret tts_key; empty tts_model resolves to "default".
	c, err = Merge(Sources{
		HubJSON: baseHub,
		Form: json.RawMessage(`{
			"tts_url": "https://tts.example.com/custom",
			"tts_default_voice": "af_heart",
			"tts_sample_rate": 24000
		}`),
		TTSKey: "sk-tts-form",
	})
	if err != nil {
		t.Fatal(err)
	}
	ep := c.TTS()
	if !c.HasTTS() || !ep.Configured() || len(c.Warnings) != 0 ||
		ep.SpeechURL() != "https://tts.example.com/custom/v1/audio/speech" ||
		ep.VoicesURL() != "https://tts.example.com/custom/v1/audio/voices" ||
		ep.Model() != DefaultTTSModel ||
		ep.DefaultVoice() != "af_heart" ||
		ep.SampleRate() != 24000 ||
		ep.Key() != "sk-tts-form" {
		t.Fatalf("form TTS: ep=%+v warnings=%v", ep, c.Warnings)
	}

	// hub.json TTS settings work, inline tts_key is ignored, and form's TTSKey is not attached when form has no tts_url.
	c, err = Merge(Sources{
		HubJSON: json.RawMessage(`{
			"providers":[{"id":"rt","url":"wss://rt.example.com/v1/live"}],
			"tts_url":"http://127.0.0.1:8880/v1/audio/speech",
			"tts_model":"org/model-v1",
			"tts_default_voice":"voice_1",
			"tts_sample_rate":16000,
			"tts_key":"inline-ignored"
		}`),
		TTSKey: "stray-secret-ignored",
	})
	if err != nil {
		t.Fatal(err)
	}
	ep = c.TTS()
	if !c.HasTTS() || len(c.Warnings) != 0 ||
		ep.SpeechURL() != "http://127.0.0.1:8880/v1/audio/speech" ||
		ep.VoicesURL() != "http://127.0.0.1:8880/v1/audio/voices" ||
		ep.Model() != "org/model-v1" ||
		ep.DefaultVoice() != "voice_1" ||
		ep.SampleRate() != 16000 ||
		ep.Key() != "" {
		t.Fatalf("hub.json TTS: ep=%+v warnings=%v", ep, c.Warnings)
	}

	// Form overrides hub.json per field; unset form fields fall back to hub.json.
	c, err = Merge(Sources{
		HubJSON: json.RawMessage(`{
			"providers":[{"id":"rt","url":"wss://rt.example.com/v1/live"}],
			"tts_url":"https://hub-tts.example.com",
			"tts_model":"hub-model",
			"tts_default_voice":"hub_voice",
			"tts_sample_rate":22050
		}`),
		Form: json.RawMessage(`{
			"tts_url":"https://form-tts.example.com",
			"tts_model":"form-model"
		}`),
		TTSKey: "sk-form-tts",
	})
	if err != nil {
		t.Fatal(err)
	}
	ep = c.TTS()
	if !c.HasTTS() || len(c.Warnings) != 0 ||
		ep.SpeechURL() != "https://form-tts.example.com/v1/audio/speech" ||
		ep.Model() != "form-model" ||
		ep.DefaultVoice() != "hub_voice" ||
		ep.SampleRate() != 22050 ||
		ep.Key() != "sk-form-tts" {
		t.Fatalf("merged TTS: ep=%+v warnings=%v", ep, c.Warnings)
	}

	// Old format (providers in Form) supports TTS settings and secret tts_key, ignoring inline tts_key.
	c, err = Merge(Sources{
		Form: json.RawMessage(`{
			"providers":[{"id":"rt","url":"wss://rt.example.com/v1/live"}],
			"tts_url":"https://old-tts.example.com",
			"tts_model":"old-model",
			"tts_default_voice":"v1",
			"tts_sample_rate":48000,
			"tts_key":"inline-ignored"
		}`),
		TTSKey: "sk-old-tts",
	})
	if err != nil {
		t.Fatal(err)
	}
	ep = c.TTS()
	if !c.HasTTS() || ep.SpeechURL() != "https://old-tts.example.com/v1/audio/speech" ||
		ep.Model() != "old-model" || ep.DefaultVoice() != "v1" || ep.SampleRate() != 48000 || ep.Key() != "sk-old-tts" {
		t.Fatalf("old format TTS: ep=%+v warnings=%v", ep, c.Warnings)
	}

	// Boundary sample rates 8000 and 48000 are accepted without warnings.
	for _, rate := range []int{8000, 48000} {
		c, err = Merge(Sources{
			HubJSON: baseHub,
			Form:    json.RawMessage(`{"tts_url":"https://tts.example.com","tts_sample_rate":` + jsonNumber(rate) + `}`),
		})
		if err != nil || !c.HasTTS() || c.TTS().SampleRate() != rate || len(c.Warnings) != 0 {
			t.Fatalf("rate %d: ep=%+v warnings=%v err=%v", rate, c.TTS(), c.Warnings, err)
		}
	}
}

func TestMergeTTSBadValuesWarnWithoutStoppingExtension(t *testing.T) {
	baseHub := json.RawMessage(`{"providers":[{"id":"rt","url":"wss://rt.example.com/v1/live"}]}`)

	// Bad tts_url disables TTS with a warning and keeps the extension and its realtime provider running.
	c, err := Merge(Sources{
		HubJSON: json.RawMessage(`{"providers":[{"id":"rt","url":"wss://rt.example.com/v1/live"}],"tts_url":"https://good.example.com"}`),
		Form:    json.RawMessage(`{"tts_url":"wss://not-http.example.com"}`),
		TTSKey:  "sk-never-sent",
	})
	if err != nil {
		t.Fatalf("bad tts_url must not fail Merge: %v", err)
	}
	if c.HasTTS() || c.TTS().Configured() || c.TTSKey != "" || len(c.Providers) != 1 || !slices.Contains(c.Warnings, WarnTTSURL) {
		t.Fatalf("bad tts_url: has=%v key=%q providers=%d warnings=%v", c.HasTTS(), c.TTSKey, len(c.Providers), c.Warnings)
	}

	// Bad tts_model, tts_default_voice and tts_sample_rate warn and fall back while keeping TTS enabled.
	c, err = Merge(Sources{
		HubJSON: baseHub,
		Form: json.RawMessage(`{
			"tts_url":"https://tts.example.com",
			"tts_model":"bad model name",
			"tts_default_voice":"../invalid voice",
			"tts_sample_rate":7999
		}`),
	})
	if err != nil {
		t.Fatalf("bad optional TTS fields must not fail Merge: %v", err)
	}
	ep := c.TTS()
	if !c.HasTTS() || ep.Model() != DefaultTTSModel || ep.DefaultVoice() != "" || ep.SampleRate() != 0 {
		t.Fatalf("fallbacks: ep=%+v", ep)
	}
	for _, want := range []string{WarnTTSModel, WarnTTSDefaultVoice, WarnTTSSampleRate} {
		if !slices.Contains(c.Warnings, want) {
			t.Errorf("missing warning %q in %v", want, c.Warnings)
		}
	}

	// Sample rate above upper bound and overly long model name also warn.
	c, err = Merge(Sources{
		HubJSON: baseHub,
		Form: json.RawMessage(`{
			"tts_url":"https://tts.example.com",
			"tts_model":"` + strings.Repeat("a", 129) + `",
			"tts_sample_rate":48001
		}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasTTS() || c.TTS().Model() != DefaultTTSModel || c.TTS().SampleRate() != 0 ||
		!slices.Contains(c.Warnings, WarnTTSModel) || !slices.Contains(c.Warnings, WarnTTSSampleRate) {
		t.Fatalf("upper-bound checks: ep=%+v warnings=%v", c.TTS(), c.Warnings)
	}
}

func TestLoadConfigReadsTTSKey(t *testing.T) {
	t.Setenv("PERSONA_EXTENSION_CONFIG", `{"url":"wss://live.example.com/v1/live","tts_url":"https://tts.example.com","tts_default_voice":"af_heart"}`)
	t.Setenv("PERSONA_EXTENSION_SECRETS", `{"key":"sk-live","tts_key":"sk-tts"}`)
	t.Setenv("PERSONA_VOICE_HUB_CONFIG", "")
	t.Setenv("PERSONA_VOICE_GATEWAY_CONFIG", "")
	c, err := LoadConfig()
	if err != nil {
		t.Fatal(err)
	}
	ep := c.TTS()
	if !c.HasTTS() || ep.SpeechURL() != "https://tts.example.com/v1/audio/speech" || ep.Key() != "sk-tts" || ep.DefaultVoice() != "af_heart" {
		t.Fatalf("LoadConfig TTS: ep=%+v", ep)
	}
}

func TestDirectConfigStructResolvesTTS(t *testing.T) {
	cfg := Config{
		TTSURL:          "https://tts.example.com/api",
		TTSDefaultVoice: "v1",
		TTSSampleRate:   24000,
		TTSKey:          "sk-direct",
	}
	if !cfg.HasTTS() {
		t.Fatal("expected HasTTS true")
	}
	ep := cfg.TTS()
	if ep.SpeechURL() != "https://tts.example.com/api/v1/audio/speech" ||
		ep.VoicesURL() != "https://tts.example.com/api/v1/audio/voices" ||
		ep.Model() != DefaultTTSModel || ep.DefaultVoice() != "v1" || ep.SampleRate() != 24000 || ep.Key() != "sk-direct" {
		t.Fatalf("direct Config TTS: %+v", ep)
	}
}

func jsonNumber(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
