package voice

import (
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Streaming TTS defaults, bounds and warnings (docs/assistant/voice.md "Streaming TTS", #17843).
const (
	// DefaultTTSModel is sent as "model" when tts_model is empty.
	DefaultTTSModel = "default"
	// TTSSampleRateMin and TTSSampleRateMax are the accepted bounds for tts_sample_rate (Hz).
	TTSSampleRateMin = 8000
	TTSSampleRateMax = 48000

	WarnTTSURL          = "tts_url"           // tts_url is not a valid http(s) URL: TTS is disabled
	WarnTTSModel        = "tts_model"         // tts_model is malformed: "default" is used instead
	WarnTTSDefaultVoice = "tts_default_voice" // tts_default_voice is not a valid voice id: ignored
	WarnTTSSampleRate   = "tts_sample_rate"   // tts_sample_rate is outside 8000–48000: ignored
)

const ttsSpeechSuffix = "/v1/audio/speech"
const ttsVoicesSuffix = "/v1/audio/voices"

// TTSEndpoint is the resolved streaming TTS endpoint from Config (docs/assistant/voice.md "Streaming TTS").
// The zero value means TTS is not configured (Configured returns false).
type TTSEndpoint struct {
	speechURL    string
	voicesURL    string
	model        string
	defaultVoice string
	sampleRate   int
	key          string
}

// Configured reports whether a streaming TTS endpoint is configured.
func (e TTSEndpoint) Configured() bool { return e.speechURL != "" }

// SpeechURL is the POST .../v1/audio/speech endpoint URL.
func (e TTSEndpoint) SpeechURL() string { return e.speechURL }

// VoicesURL is the GET .../v1/audio/voices endpoint URL.
func (e TTSEndpoint) VoicesURL() string { return e.voicesURL }

// Model is the model sent in speech requests ("default" when tts_model is empty).
func (e TTSEndpoint) Model() string { return e.model }

// DefaultVoice is the configured fallback voice id (empty when unset).
func (e TTSEndpoint) DefaultVoice() string { return e.defaultVoice }

// SampleRate is the configured fallback PCM sample rate in Hz (8000–48000, or 0 when unset).
func (e TTSEndpoint) SampleRate() int { return e.sampleRate }

// Key is the optional Bearer token sent to the TTS endpoint (empty when unset).
func (e TTSEndpoint) Key() string { return e.key }

// HasTTS reports whether a valid streaming TTS endpoint is configured.
func (c *Config) HasTTS() bool { return c != nil && c.TTS().Configured() }

// TTS returns the resolved streaming TTS endpoint, or the zero TTSEndpoint when TTS is not configured.
func (c *Config) TTS() TTSEndpoint {
	if c == nil {
		return TTSEndpoint{}
	}
	if c.tts.Configured() {
		return c.tts
	}
	if strings.TrimSpace(c.TTSURL) == "" {
		return TTSEndpoint{}
	}
	ep, _ := resolveTTS(c.TTSURL, c.TTSModel, c.TTSDefaultVoice, c.TTSSampleRate, c.TTSKey)
	return ep
}

// DeriveTTSURLs resolves a configured tts_url (an http/https base URL or a full .../v1/audio/speech URL)
// into the speech and sibling voices URLs. Returns ok=false for empty or malformed URLs.
func DeriveTTSURLs(raw string) (speechURL, voicesURL string, ok bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, " \t\r\n") {
		return "", "", false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", "", false
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", "", false
	}
	if port := u.Port(); port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", "", false
		}
	}
	cleanPath := strings.TrimRight(u.Path, "/")
	var speechPath, voicesPath string
	if strings.HasSuffix(cleanPath, ttsSpeechSuffix) {
		speechPath = cleanPath
		voicesPath = strings.TrimSuffix(cleanPath, ttsSpeechSuffix) + ttsVoicesSuffix
	} else {
		speechPath = cleanPath + ttsSpeechSuffix
		voicesPath = cleanPath + ttsVoicesSuffix
	}
	origin := scheme + "://" + u.Host
	return origin + speechPath, origin + voicesPath, true
}

// validTTSModel reports whether s is an acceptable non-empty TTS model identifier (no whitespace or control chars, <= 128 runes).
func validTTSModel(s string) bool {
	if s == "" || utf8.RuneCountInString(s) > 128 {
		return false
	}
	for _, r := range s {
		if unicode.IsSpace(r) || r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}

func resolveTTS(rawURL, rawModel, rawDefaultVoice string, sampleRate int, key string) (TTSEndpoint, []string) {
	rawURL = strings.TrimSpace(rawURL)
	rawModel = strings.TrimSpace(rawModel)
	rawDefaultVoice = strings.TrimSpace(rawDefaultVoice)
	key = strings.TrimSpace(key)

	var warnings []string
	if rawModel != "" && !validTTSModel(rawModel) {
		warnings = append(warnings, WarnTTSModel)
		rawModel = ""
	}
	if rawDefaultVoice != "" && !voicePattern.MatchString(rawDefaultVoice) {
		warnings = append(warnings, WarnTTSDefaultVoice)
		rawDefaultVoice = ""
	}
	if sampleRate != 0 && (sampleRate < TTSSampleRateMin || sampleRate > TTSSampleRateMax) {
		warnings = append(warnings, WarnTTSSampleRate)
		sampleRate = 0
	}
	if rawURL == "" {
		return TTSEndpoint{}, warnings
	}
	speechURL, voicesURL, ok := DeriveTTSURLs(rawURL)
	if !ok {
		warnings = append(warnings, WarnTTSURL)
		return TTSEndpoint{}, warnings
	}
	model := rawModel
	if model == "" {
		model = DefaultTTSModel
	}
	return TTSEndpoint{
		speechURL:    speechURL,
		voicesURL:    voicesURL,
		model:        model,
		defaultVoice: rawDefaultVoice,
		sampleRate:   sampleRate,
		key:          key,
	}, warnings
}

// validateTTS normalizes and validates the TTS fields on c. Bad values record warnings and are cleared; a bad tts_url
// disables TTS with WarnTTSURL and never stops the extension.
func (c *Config) validateTTS() {
	c.TTSURL = strings.TrimSpace(c.TTSURL)
	c.TTSModel = strings.TrimSpace(c.TTSModel)
	c.TTSDefaultVoice = strings.TrimSpace(c.TTSDefaultVoice)
	c.TTSKey = strings.TrimSpace(c.TTSKey)

	ep, warnings := resolveTTS(c.TTSURL, c.TTSModel, c.TTSDefaultVoice, c.TTSSampleRate, c.TTSKey)
	c.Warnings = append(c.Warnings, warnings...)
	if slices.Contains(warnings, WarnTTSModel) {
		c.TTSModel = ""
	}
	if slices.Contains(warnings, WarnTTSDefaultVoice) {
		c.TTSDefaultVoice = ""
	}
	if slices.Contains(warnings, WarnTTSSampleRate) {
		c.TTSSampleRate = 0
	}
	if !ep.Configured() {
		c.TTSURL = ""
		c.TTSKey = ""
		c.tts = TTSEndpoint{}
		return
	}
	c.tts = ep
}
