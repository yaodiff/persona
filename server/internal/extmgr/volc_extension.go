package extmgr

import (
	"context"
	"encoding/json"
	"slices"

	"github.com/nwverse/persona/server/internal/moduleswitch"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/builtinext"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The built-in Volcengine speech extension, `personad volc` (docs/extensions/volcengine-speech.md): off until the owner
// turns it on and saves a key in Settings › Extensions. It contributes a transcription (meetings on the hub, and
// extensions' own audio) and a speech (meeting answers read aloud) on the owner's Volcengine account. Its form's
// "Use for Meeting Transcription" is not a setting of its own: it shows and sets the assistant's choice of meeting
// transcription (volcMeetings), so the switch and Recording settings can never disagree.
//
// The extension comes with its module (internal/modules/volc), which provides what is shown of it through
// internal/builtinext; in a build without the module (persona_without_volc) there is no such extension.

// speechPoint is the speech extension point's name (extension.Points).
const speechPoint = "speech"

// volcRef is the extension's transcription contribution as the assistant names it (meeting_transcription_extension),
// "" without the module.
func volcRef(s moduleswitch.Set) string {
	v, ok := builtinext.VolcSpeech(s)
	if !ok {
		return ""
	}
	return v.ExtensionID + "/" + v.TranscriptionContribution
}

// volcHost is the service the program reaches (shown with its permissions).
const volcHost = "openspeech.bytedance.com"

// volcMeetingsField is the form's switch for meeting transcription, which reads and writes the assistant's choice and
// is never saved with the extension's settings.
const volcMeetingsField = "meetings"

// volcModelKeys name the recognition models' labels in the catalogs (their ids have dots).
var volcModelKeys = map[string]string{
	"volc.seedasr.sauc.duration": "asr_seed2_hour", "volc.seedasr.sauc.concurrent": "asr_seed2_concurrent",
	"volc.bigasr.sauc.duration": "asr_big1_hour", "volc.bigasr.sauc.concurrent": "asr_big1_concurrent",
}

// volcConfig is the settings form in language l (English in the manifest, the rest by volcTranslations).
func volcConfig(volc builtinext.Volc, l i18n.Lang) json.RawMessage {
	t := func(key string, args ...i18n.Args) string {
		return i18n.T(l, "hub-volc_extension.manifest_"+key, args...)
	}
	type field = map[string]any
	meetings, _ := json.Marshal(field{"type": "boolean", "title": t("meetings_title"), "description": t("meetings_description"),
		"default": false, "x-confirm": t("meetings_confirm")})
	readAnswers, _ := json.Marshal(field{"type": "boolean", "title": t("read_answers_title"), "description": t("read_answers_description"), "default": true})
	var voices []field
	for _, v := range volc.Voices {
		voices = append(voices, field{"const": v, "title": t("voice_" + v)})
	}
	voice, _ := json.Marshal(field{"type": "string", "title": t("voice_title"), "oneOf": voices, "default": volc.Voices[0]})
	var models []field
	for _, m := range volc.ASRModels {
		models = append(models, field{"const": m, "title": t(volcModelKeys[m])})
	}
	asr, _ := json.Marshal(field{"type": "string", "title": t("asr_model_title"), "description": t("asr_model_description"), "oneOf": models, "default": volc.ASRModels[0]})
	voiceID, _ := json.Marshal(field{"type": "string", "title": t("voice_id_title"), "description": t("voice_id_description"), "pattern": "^[A-Za-z0-9_.-]{0,128}$"})
	appID, _ := json.Marshal(field{"type": "string", "title": t("app_id_title"), "description": t("app_id_description"), "pattern": "^[A-Za-z0-9_-]{0,64}$"})
	// Joined by hand to keep the properties in this order: the form follows it.
	return json.RawMessage(`{"type":"object","properties":{"meetings":` + string(meetings) + `,"read_answers":` + string(readAnswers) +
		`,"voice":` + string(voice) + `,"advanced":{"type":"object","title":` + jsonString(t("advanced_title")) + `,"properties":{"asr_model":` +
		string(asr) + `,"voice_id":` + string(voiceID) + `,"app_id":` + string(appID) + `}}}}`)
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// volcManifest is the extension's manifest in English, with volcTranslations for the other languages.
func volcManifest(h *Manager, volc builtinext.Volc) *extension.Manifest {
	t := func(key string) string { return i18n.T(i18n.En, "hub-volc_extension.manifest_"+key) }
	transcription, _ := json.Marshal([]extension.TranscriptionItem{{ID: volc.TranscriptionContribution, Name: t("contribution")}})
	speech, _ := json.Marshal([]extension.SpeechItem{{ID: volc.SpeechContribution, Name: t("contribution")}})
	ui, _ := json.Marshal([]extension.UIItem{{ID: volc.StatusPage, Title: t("page"), Placement: "extension_page"}})
	return &extension.Manifest{ManifestVersion: extension.ManifestVersion, ID: volc.ExtensionID, Name: t("name"), Version: h.version,
		Description: t("description"), Homepage: "https://www.volcengine.com/product/voice-tech",
		Engines:          extension.Engines{ExtensionProtocol: extension.Protocol},
		Runtime:          &extension.Runtime{Command: extension.Command{Path: "personad"}, Args: []string{"volc"}, HTTP: true},
		ActivationEvents: []string{extension.OnStartup},
		Permissions:      extension.Permissions{Network: []string{volcHost}},
		Contributes:      map[string]json.RawMessage{transcriptionPoint: transcription, speechPoint: speech, "ui": ui},
		Configuration:    volcConfig(volc, i18n.En),
		SettingsCheck:    true,
		Secrets:          []extension.SecretSpec{{Name: volc.SecretKey, Title: t("key_title"), Description: t("key_description"), Hint: "last4"}}}
}

// volcTranslations are the manifest's text in every language but English.
func volcTranslations(volc builtinext.Volc) extension.Translations {
	out := extension.Translations{}
	for _, l := range []i18n.Lang{i18n.ZhHans, i18n.Ja} {
		t := func(key string) string { return i18n.T(l, "hub-volc_extension.manifest_"+key) }
		voices := map[string]string{}
		for _, v := range volc.Voices {
			voices[v] = t("voice_" + v)
		}
		models := map[string]string{}
		for _, m := range volc.ASRModels {
			models[m] = t(volcModelKeys[m])
		}
		out[l] = &extension.Translation{Name: t("name"), Description: t("description"),
			Configuration: map[string]*extension.FieldText{
				"meetings":     {Title: t("meetings_title"), Description: t("meetings_description"), Confirm: t("meetings_confirm")},
				"read_answers": {Title: t("read_answers_title"), Description: t("read_answers_description")},
				"voice":        {Title: t("voice_title"), Choices: voices},
				"advanced": {Title: t("advanced_title"), Properties: map[string]*extension.FieldText{
					"asr_model": {Title: t("asr_model_title"), Description: t("asr_model_description"), Choices: models},
					"voice_id":  {Title: t("voice_id_title"), Description: t("voice_id_description")},
					"app_id":    {Title: t("app_id_title"), Description: t("app_id_description")},
				}},
			},
			Secrets: map[string]*extension.SecretText{volc.SecretKey: {Title: t("key_title"), Description: t("key_description")}},
			UI:      map[string]string{volc.StatusPage: t("page")}}
	}
	return out
}

// addVolcExtension registers the built-in extension, off until the owner turns it on, unless a package was installed
// under its id before (it keeps the id, as for the voice extension).
func (h *Manager) addVolcExtension(ctx context.Context, reg *extension.Registry, store extension.Store) {
	volc, ok := builtinext.VolcSpeech(h.modules())
	if !ok { // the module is not in this build
		return
	}
	if store != nil {
		rows, err := store.Extensions(ctx)
		if err != nil { // fail closed: whether a package has the id is unknown
			h.logf("plugin %s: not registered: %v", volc.ExtensionID, err)
			return
		}
		if slices.ContainsFunc(rows, func(r extension.Record) bool {
			return r.ID == volc.ExtensionID && r.Source == extension.SourceInstalled && !r.BuiltinPredecessor
		}) {
			return
		}
	}
	if err := reg.AddBuiltinTranslated(volcManifest(h, volc), "", false, volcTranslations(volc)); err != nil {
		h.logf("plugin %s: %v", volc.ExtensionID, err)
		return
	}
	// Both implementations use the same extension process, transcription and speech
	// dispatch. apply stops its predecessor before publishing the replacement.
	reg.AllowBuiltinHandoff(volc.ExtensionID)
}

// isVolc says whether p is the built-in Volcengine speech extension.
func isVolc(s moduleswitch.Set, p *extension.Extension) bool {
	volc, ok := builtinext.VolcSpeech(s)
	return ok && p != nil && p.Record.ID == volc.ExtensionID && p.Record.Source == extension.SourceBuiltin
}

// volcID is the extension's id ("" without the module).
func volcID(s moduleswitch.Set) string {
	volc, _ := builtinext.VolcSpeech(s)
	return volc.ExtensionID
}

// volcSecretKey is the name of the extension's key's secret ("" without the module).
func volcSecretKey(s moduleswitch.Set) string {
	volc, _ := builtinext.VolcSpeech(s)
	return volc.SecretKey
}

// volcView adds to the extension's view: needs_setup while no key is saved (it runs, so that saving one can be
// checked, but nothing transcribes or reads aloud).
func (ps *extensions) volcView(ctx context.Context, p *extension.Extension, v *ExtensionView) {
	if !p.Record.Enabled || !isVolc(ps.modules(), p) || ps.reg.SecretsSet(ctx, p)[volcSecretKey(ps.modules())] {
		return
	}
	if v.Status == host.StateError || v.Status == extensionIncompatible {
		return
	}
	v.Status, v.NeedsSetup = extensionNeedsSetup, append(v.NeedsSetup, i18n.T(ps.h.contextLang(ctx), "hub-volc_extension.setup_key"))
}

// volcMeetings is whether meetings transcribe with the extension now: the assistant's choice names it.
func (h *Manager) volcMeetings(ctx context.Context) (on, known bool) {
	res, err := buswire.Call(ctx, assistantCaller(h.callAssistant), assistantwire.MeetingTranscriptionChoiceMethod, assistantwire.MeetingTranscriptionChoose{})
	if err != nil {
		return false, false
	}
	return res.Extension == volcRef(h.modules()), true
}

// volcConfigWrite takes the switch out of the settings about to be saved and, when it differs from the assistant's
// choice, makes the choice: on, meetings transcribe with the extension and new meetings on the hub; off, the choice
// goes back to the built-in one and new meetings to the phone. The settings come back without it, and whether
// anything else in them changed.
func (h *Manager) volcConfigWrite(ctx context.Context, p *extension.Extension, raw json.RawMessage) (json.RawMessage, bool, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return raw, true, nil // the schema check says what is wrong with it
	}
	sw, has := fields[volcMeetingsField]
	delete(fields, volcMeetingsField)
	rest, _ := json.Marshal(fields)
	saved := map[string]json.RawMessage{}
	_ = json.Unmarshal(h.extensions.reg.SavedConfig(p), &saved)
	delete(saved, volcMeetingsField)
	changed := !sameSettings(fields, saved)
	if !has {
		return rest, changed, nil
	}
	var want bool
	if err := json.Unmarshal(sw, &want); err != nil {
		return raw, true, nil // not a boolean: the schema check refuses it
	}
	if on, known := h.volcMeetings(ctx); known && on == want {
		return rest, changed, nil
	}
	res, err := buswire.Call(ctx, assistantCaller(h.callAssistant), assistantwire.MeetingTranscriptionChoiceMethod,
		assistantwire.MeetingTranscriptionChoose{Set: true, Use: want, Extension: volcRef(h.modules())})
	if err != nil {
		return nil, false, err
	}
	h.logf("plugin %s: meetings transcribe with it: %v (meeting transcription %q, new meetings %s)", p.Record.ID, want, res.Extension, res.Default)
	return rest, changed, nil
}

// sameSettings says whether two settings objects hold the same values.
func sameSettings(a, b map[string]json.RawMessage) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if !sameValue(v, b[k]) {
			return false
		}
	}
	return true
}

// volcDetail shows the switch as the assistant's choice is.
func (h *Manager) volcDetail(ctx context.Context, p *extension.Extension, d *ExtensionDetail) {
	if !isVolc(h.modules(), p) || d.Config == nil {
		return
	}
	on, _ := h.volcMeetings(ctx)
	fields := map[string]json.RawMessage{}
	_ = json.Unmarshal(d.Config, &fields)
	fields[volcMeetingsField] = json.RawMessage(map[bool]string{true: "true", false: "false"}[on])
	d.Config, _ = json.Marshal(fields)
}
