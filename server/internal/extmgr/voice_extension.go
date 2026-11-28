package extmgr

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/moduleswitch"

	"github.com/nwverse/persona/server/internal/builtinext"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The built-in voice extension, `personad voice` (docs/extensions/extensions.md "Built-in voice extension", docs/assistant/voice.md
// "Configurable GPT-Live upstream"): a built-in extension with a settings form and a secret key, so its realtime
// service can be set from the apps. hub.json's first entry of it is taken over under its own id (its upstreams stay,
// and come first); without one the extension is there, switched off, as "voice".
//
// The extension comes with its module (internal/modules/voice), which provides what the host needs of it through
// internal/builtinext; in a build without the module (persona_without_voice) there is no such extension, and a
// hub.json entry running `personad voice` stays an extension of hub.json's like any other.

// voiceExtensionID is its id when hub.json has no entry of it.
const voiceExtensionID = "voice"

// voiceSecretKey is the secret holding the settings' upstream's key, sent to it as Authorization: Bearer.
const voiceSecretKey = "key"

// voiceTTSSecretKey is the optional secret holding the streaming TTS endpoint's key, sent only to tts_url as Authorization: Bearer.
const voiceTTSSecretKey = "tts_key"

// voiceBaseProperties are the settings form's upstream and archive fields (kept in order before the TTS fields):
// one upstream (models custom/<model>) and how the model talks. ssh, key_file, key_command, an explicit tools and more
// upstreams are hub.json's only: key_command runs a command on the hub's machine. The form's upstream declares the
// function tools on its own (the extension's Provider.Tools unset, #1420).
const voiceBaseProperties = `
    "url": {"type": "string", "title": "服务地址", "description": "GPT-Live 兼容的会话地址，wss://<主机>/v1/live；ws:// 只用于内网，Key 会明文传输。换了主机，设置的 Key 会被清掉", "pattern": "^wss?://[^/?#\\s]+"},
    "name": {"type": "string", "title": "名字", "description": "有几个服务时显示在模型名后面", "default": "GPT-Live"},
    "dialect": {"type": "string", "title": "协议", "oneOf": [{"const": "live", "title": "GPT-Live"}, {"const": "codex", "title": "Codex（gpt-live-1-codex）"}, {"const": "realtime", "title": "OpenAI Realtime（gpt-realtime）"}], "default": "live"},
    "models": {"type": "array", "title": "模型", "description": "不填时读服务自己的模型列表", "items": {"type": "string", "pattern": "^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$"}, "uniqueItems": true},
    "default_model": {"type": "string", "title": "默认模型", "pattern": "^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$"},
    "input_rate": {"type": "integer", "title": "麦克风采样率", "description": "不填时 GPT-Live 用 16000，Codex 用 24000", "enum": [16000, 24000]},
    "owner": {"type": "string", "title": "语音里怎么称呼你"},
    "notes": {"type": "string", "title": "给语音助手的补充说明"},
    "archive_machine": {"type": "string", "format": "machine", "title": "通话存档到哪台电脑", "description": "选一台电脑：每通电话挂断后，两边说的话按先后、带时间写成一份 Markdown 存到那台电脑上；hub 不落盘。不选不存"},
    "archive_dir": {"type": "string", "title": "存档目录", "description": "那台电脑上的目录，~/ 开头或绝对路径；空着是 ~/PersonaCalls", "pattern": "^(~/|/)"}`

// voiceConfigSchema builds the built-in voice extension's settings form schema, appending the localized TTS endpoint
// properties in fixed order (#17843).
func voiceConfigSchema() json.RawMessage {
	t := func(key string) string { return i18n.T(i18n.En, "hub-voice_extension.manifest_"+key) }
	type field = map[string]any
	ttsURL, _ := json.Marshal(field{
		"type":        "string",
		"title":       t("tts_url_title"),
		"description": t("tts_url_description"),
		"pattern":     `^https?://[^/?#@\s]+(/[^?#\s]*)?$`,
	})
	ttsModel, _ := json.Marshal(field{
		"type":        "string",
		"title":       t("tts_model_title"),
		"description": t("tts_model_description"),
		"maxLength":   128,
		"pattern":     `^\S+$`,
	})
	ttsDefaultVoice, _ := json.Marshal(field{
		"type":        "string",
		"title":       t("tts_default_voice_title"),
		"description": t("tts_default_voice_description"),
		"pattern":     `^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`,
	})
	ttsSampleRate, _ := json.Marshal(field{
		"type":        "integer",
		"title":       t("tts_sample_rate_title"),
		"description": t("tts_sample_rate_description"),
		"minimum":     8000,
		"maximum":     48000,
	})
	return json.RawMessage(`{"type":"object","properties":{` + voiceBaseProperties +
		`,"tts_url":` + string(ttsURL) +
		`,"tts_model":` + string(ttsModel) +
		`,"tts_default_voice":` + string(ttsDefaultVoice) +
		`,"tts_sample_rate":` + string(ttsSampleRate) + `}}`)
}

// voiceTranslations translates the localized manifest fields (TTS settings and tts_key) into zh-Hans and ja; English
// lives in voiceManifest itself.
func voiceTranslations() extension.Translations {
	out := extension.Translations{}
	for _, l := range []i18n.Lang{i18n.ZhHans, i18n.Ja} {
		t := func(key string) string { return i18n.T(l, "hub-voice_extension.manifest_"+key) }
		out[l] = &extension.Translation{
			Configuration: map[string]*extension.FieldText{
				"tts_url":           {Title: t("tts_url_title"), Description: t("tts_url_description")},
				"tts_model":         {Title: t("tts_model_title"), Description: t("tts_model_description")},
				"tts_default_voice": {Title: t("tts_default_voice_title"), Description: t("tts_default_voice_description")},
				"tts_sample_rate":   {Title: t("tts_sample_rate_title"), Description: t("tts_sample_rate_description")},
			},
			Secrets: map[string]*extension.SecretText{
				voiceTTSSecretKey: {Title: t("tts_key_title"), Description: t("tts_key_description")},
			},
		}
	}
	return out
}

// voiceManifest is the built-in voice extension under id, named name (hub.json's entry's, or the Chinese for "Realtime
// voice").
func voiceManifest(h *Manager, voice builtinext.Voice, id, name string) *extension.Manifest {
	if name == "" {
		name = "实时语音"
	}
	items, _ := json.Marshal([]extension.VoiceItem{{ID: id, Name: name}})
	// Meeting transcription on the hub (docs/assistant/meeting-minutes.md "The built-in adapter"), with the same
	// upstream and key: the manager dials it, never the apps.
	transcription, _ := json.Marshal([]extension.TranscriptionItem{{ID: voice.TranscriptionContribution, Name: "GPT-Live"}})
	t := func(key string) string { return i18n.T(i18n.En, "hub-voice_extension.manifest_"+key) }
	return &extension.Manifest{ManifestVersion: extension.ManifestVersion, ID: id, Name: name, Version: h.version,
		Description:      "在 App 里和 Persona 实时语音对话，语音来自 GPT-Live 兼容的实时服务。填服务地址和 Key 后打开；manager.json 里写的上游照常可用。",
		Engines:          extension.Engines{ExtensionProtocol: extension.Protocol},
		Runtime:          &extension.Runtime{Command: extension.Command{Path: "personad"}, Args: []string{"voice"}, RPC: "none", HTTP: true},
		ActivationEvents: []string{extension.OnStartup}, Permissions: extension.Permissions{HubAPI: true},
		Contributes:   map[string]json.RawMessage{"voice": items, transcriptionPoint: transcription},
		Configuration: voiceConfigSchema(),
		Secrets: []extension.SecretSpec{
			{Name: voiceSecretKey, Title: "API Key", Description: "作为 Authorization: Bearer 发给服务；只发给上面的服务地址，换了主机要重新填"},
			{Name: voiceTTSSecretKey, Title: t("tts_key_title"), Description: t("tts_key_description")},
		}}
}

// takesOverVoice says whether a hub.json entry runs `personad voice`: its first such entry becomes the built-in
// extension. Any other program (a node script) stays a hub.json extension.
func takesOverVoice(s moduleswitch.Set, c ExtensionConfig) bool {
	if _, ok := builtinext.VoiceExtension(s); !ok { // the module is not in this build
		return false
	}
	return (c.Kind == "" || c.Kind == "voice") && extension.IsBuiltinCommand(c.Command) && len(c.Args) > 0 && c.Args[0] == "voice"
}

// addVoiceExtension registers the built-in voice extension in reg (attachExtensions, before Load): under the id of the
// hub.json entry it takes over, on unless that entry says disabled (then always off); without one as "voice", off
// until the user turns it on, unless hub.json has a voice extension of its own (another program) or a package was
// installed under that id before (it keeps the id). It never yields its id to a package installed later: that one is
// refused as any of the same id (Preview), so no other program is handed the key set here.
func (h *Manager) addVoiceExtension(ctx context.Context, reg *extension.Registry, store extension.Store) {
	voice, ok := builtinext.VoiceExtension(h.modules())
	if !ok { // the module is not in this build
		return
	}
	ps := h.extensions
	c := ps.voice
	if c == nil {
		for _, p := range reg.List() {
			if p.Manifest.Kind() == "voice" || p.Record.ID == voiceExtensionID {
				return
			}
		}
		if store != nil {
			rows, err := store.Extensions(ctx)
			if err != nil { // fail closed: whether a package has the id is unknown
				h.logf("plugin %s: not registered: %v", voiceExtensionID, err)
				return
			}
			if slices.ContainsFunc(rows, func(r extension.Record) bool {
				return r.ID == voiceExtensionID && r.Source == extension.SourceInstalled
			}) {
				return // the package installed under this id before keeps it (Load)
			}
		}
		if err := reg.AddBuiltinTranslated(voiceManifest(h, voice, voiceExtensionID, ""), "", false, voiceTranslations()); err != nil {
			h.logf("plugin %s: %v", voiceExtensionID, err)
			return
		}
		ps.voiceID = voiceExtensionID
		return
	}
	// On without a row (an upgraded hub's entry runs as it did); disabled holds it off without writing that to its row.
	if err := reg.AddBuiltinTranslated(voiceManifest(h, voice, c.ID, c.Name), "", true, voiceTranslations()); err != nil {
		h.logf("plugin %s: %v", c.ID, err)
		ps.mu.Lock()
		delete(ps.byID, c.ID) // its placeholder from newExtensions: nothing will start it
		ps.mu.Unlock()
		return
	}
	if c.Disabled { // manager.json has the last word on its switch
		reg.KeepOff(c.ID, i18n.Err("hub-voice_extension.disabled_in_hub_json"))
	}
	ps.voiceID = c.ID
}

// isVoice says whether p is the built-in voice extension.
func (ps *extensions) isVoice(p *extension.Extension) bool {
	return p != nil && ps.voiceID != "" && p.Record.ID == ps.voiceID && p.Record.Source == extension.SourceBuiltin
}

const gatewaySecretKey = "gateway_key"

// voiceSpec gives the built-in voice extension what the hub.json entry it took over had (docs/assistant/voice.md "How the
// extension gets its configuration"): its config as PERSONA_VOICE_HUB_CONFIG (upstreams, default, owner, notes), its
// env and its directory, plus PERSONA_VOICE_GATEWAY_CONFIG when a quota gateway extension is configured with an API key.
// The form's settings and key come as any extension's (PERSONA_EXTENSION_CONFIG, PERSONA_EXTENSION_SECRETS).
func (ps *extensions) voiceSpec(p *extension.Extension, s *host.Spec) {
	if !ps.isVoice(p) {
		return
	}
	if c := ps.voice; c != nil {
		if c.Dir != "" {
			s.Dir = expandHome(c.Dir)
		}
		for k, v := range c.Env {
			s.Env = append(s.Env, k+"="+v)
		}
		if len(c.Config) > 0 {
			s.Env = append(s.Env, "PERSONA_VOICE_HUB_CONFIG="+string(c.Config))
		}
	}
	raw := ps.voiceGatewaySpec(context.Background())
	ps.mu.Lock()
	ps.voiceGatewaySent = string(raw)
	ps.mu.Unlock()
	if len(raw) > 0 {
		s.Env = append(s.Env, "PERSONA_VOICE_GATEWAY_CONFIG="+string(raw))
	}
}

// voiceGatewaySpec returns the serialized provider (the extension's Provider) derived from an enabled quota gateway extension's settings
// and secret when it has a valid gateway_url and an API key.
func (ps *extensions) voiceGatewaySpec(ctx context.Context) json.RawMessage {
	voice, ok := builtinext.VoiceExtension(ps.modules())
	if ps == nil || ps.reg == nil || !ok {
		return nil
	}
	for _, p := range ps.reg.List() {
		if !p.Record.Enabled || p.Manifest.Kind() != "quota" {
			continue
		}
		secrets, err := ps.programSecrets(ctx, p.Record.ID)
		if err != nil {
			continue
		}
		name := p.Manifest.Name
		if p.Full != nil && p.Full.Name != "" {
			name = p.Full.Name
		}
		if b, ok := voice.GatewayProvider(ps.reg.Config(p), name, secrets[gatewaySecretKey]); ok {
			return b
		}
	}
	return nil
}

// voiceGatewayState returns a placeholder provider derived from an enabled quota gateway extension's settings
// without unsealing the secret (only checking whether gateway_key is set).
func (ps *extensions) voiceGatewayState(ctx context.Context) json.RawMessage {
	voice, ok := builtinext.VoiceExtension(ps.modules())
	if ps == nil || ps.reg == nil || !ok {
		return nil
	}
	for _, p := range ps.reg.List() {
		if !p.Record.Enabled || p.Manifest.Kind() != "quota" || !ps.reg.SecretsSet(ctx, p)[gatewaySecretKey] {
			continue
		}
		name := p.Manifest.Name
		if p.Full != nil && p.Full.Name != "" {
			name = p.Full.Name
		}
		if b, ok := voice.GatewayProvider(ps.reg.Config(p), name, "set"); ok {
			return b
		}
	}
	return nil
}

// voiceHubJSON is the config of the hub.json entry the built-in voice extension took over (nil without one).
func (ps *extensions) voiceHubJSON() json.RawMessage {
	if ps.voice == nil {
		return nil
	}
	return ps.voice.Config
}

// voiceState is what the built-in voice extension lacks before a conversation can start (its Config.Setup) and the
// settings it does not take, merged as the extension merges them. It runs either way; the apps offer voice only once
// it has an upstream. Only whether the key is set is read: its value stays sealed.
func (ps *extensions) voiceState(ctx context.Context, p *extension.Extension) (setup string, warnings []string) {
	secretsSet := ps.reg.SecretsSet(ctx, p)
	key, ttsKey := "", ""
	if secretsSet[voiceSecretKey] {
		key = "set"
	}
	if secretsSet[voiceTTSSecretKey] {
		ttsKey = "set"
	}
	voice, ok := builtinext.VoiceExtension(ps.modules())
	if !ok {
		return "", nil
	}
	setup, warnings, err := voice.State(builtinext.VoiceSources{Form: ps.reg.Config(p), Key: key, TTSKey: ttsKey, HubJSON: ps.voiceHubJSON(), Gateway: ps.voiceGatewayState(ctx)})
	if err != nil {
		return "", nil // the extension does not start and says why itself
	}
	return setup, warnings
}

// voiceView adds to the built-in voice extension's view: needs_setup while it has no upstream (it runs, but no
// conversation starts; the hint the hub's like hubJSONWaitsForTriageModel's), and the settings it does not take.
func (ps *extensions) voiceView(ctx context.Context, p *extension.Extension, v *ExtensionView) {
	if !p.Record.Enabled || !ps.isVoice(p) {
		return
	}
	setup, warnings := ps.voiceState(ctx, p)
	l := ps.h.contextLang(ctx)
	if setup != "" && v.Status != host.StateError && v.Status != extensionIncompatible {
		v.Status, v.NeedsSetup = extensionNeedsSetup, append(v.NeedsSetup, i18n.T(l, "hub-voice_extension.setup_"+setup))
	}
	if len(warnings) > 0 {
		v.Warnings = slices.Clone(v.Warnings)
		for _, w := range warnings {
			v.Warnings = append(v.Warnings, i18n.T(l, "hub-voice_extension.warn_"+w))
		}
	}
}

// voiceKeyFollowsURL keeps each secret bound to the address it was set for (the registry's BoundSecrets,
// docs/assistant/voice.md "Manifest"): a configuration that moves url or tts_url to another origin (scheme, host, port)
// deletes its bound key before saving, whoever saves it, in the same registry operation a key is set in, so neither key
// is ever sent as a Bearer to a host it was not given for.
func (ps *extensions) voiceKeyFollowsURL(p *extension.Extension, old, next json.RawMessage) []string {
	if !ps.isVoice(p) {
		return nil
	}
	var cleared []string
	if !sameOrigin(voiceURL(ps.modules(), old), voiceURL(ps.modules(), next)) {
		cleared = append(cleared, voiceSecretKey)
	}
	if !sameOrigin(voiceTTSURL(ps.modules(), old), voiceTTSURL(ps.modules(), next)) {
		cleared = append(cleared, voiceTTSSecretKey)
	}
	return cleared
}

// keyNeedsURLError is the refusal of a key while the settings have no address (the route answers needs_url).
type keyNeedsURLError struct{ error }

func (e keyNeedsURLError) Unwrap() error { return e.error }

// voiceKeyNeedsURL refuses a key for the built-in voice extension while its bound address is empty (the registry's
// SecretNeeds): each key is set for its own address (voiceKeyFollowsURL).
func (ps *extensions) voiceKeyNeedsURL(p *extension.Extension, name string, config json.RawMessage) error {
	if !ps.isVoice(p) {
		return nil
	}
	switch name {
	case voiceSecretKey:
		if voiceURL(ps.modules(), config) != "" {
			return nil
		}
		return keyNeedsURLError{i18n.Err("hub-voice_extension.key_needs_url")}
	case voiceTTSSecretKey:
		if voiceTTSURL(ps.modules(), config) != "" {
			return nil
		}
		return keyNeedsURLError{i18n.Err("hub-voice_extension.tts_key_needs_url")}
	default:
		return nil
	}
}

// voiceURL is the settings' realtime address ("" none).
func voiceURL(s moduleswitch.Set, config json.RawMessage) string {
	voice, ok := builtinext.VoiceExtension(s)
	if !ok {
		return ""
	}
	return voice.FormURL(config)
}

// voiceTTSURL is the settings' streaming TTS endpoint address ("" none).
func voiceTTSURL(s moduleswitch.Set, config json.RawMessage) string {
	voice, ok := builtinext.VoiceExtension(s)
	if !ok || voice.FormTTSURL == nil {
		return ""
	}
	return voice.FormTTSURL(config)
}

// sameOrigin says whether two addresses have the same scheme, host and port (a default port written or not); two
// empty ones do, an empty one and another do not.
func sameOrigin(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	oa, ok1 := originOf(a)
	ob, ok2 := originOf(b)
	return ok1 && ok2 && oa == ob
}

func originOf(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "", false
	}
	scheme := strings.ToLower(u.Scheme)
	port := u.Port()
	if port == "" {
		switch scheme {
		case "wss", "https":
			port = "443"
		case "ws", "http":
			port = "80"
		}
	}
	return scheme + "://" + net.JoinHostPort(strings.ToLower(u.Hostname()), port), true
}
