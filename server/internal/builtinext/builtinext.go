// Package builtinext is where a built-in module hands the core what the core needs of it to run the module's
// built-in extension (docs/architecture/core-and-modules.md "Provider seams"): the program a subcommand of personad
// and persona-manager runs (`personad volc`, `personad voice`), and what the extension host (internal/extmgr) shows
// of the extension.
//
// The core declares here what it needs, never a module's types; the composition root (internal/modules/all) provides
// it in the init of the module's file, under the module's build constraint. With the module left out of the build
// nothing is provided: the subcommand is unknown and the extension host registers no such extension, exactly as
// before the extension existed. Switched off in the `modules` setting (moduleswitch), the extension host gets nothing
// either (VolcSpeech, VoiceExtension), so it registers and starts no such extension; the program stays a subcommand
// of the binary, which only a build without the module removes, and nothing in the manager runs it.
//
// Everything is provided while the program initializes, before main runs, and only read afterwards.
package builtinext

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/moduleswitch"
)

// Program is a module's built-in extension program, run as a subcommand of the binaries that link the modules.
type Program func(ctx context.Context, version string) error

type program struct {
	run     Program
	summary string
	module  string
}

var programs = map[string]program{}

// AddProgram provides the module's program, the one the subcommand name runs, and its line in personad's usage (init
// only). It stays a subcommand of the binary whatever the switch; the extension host asks ProgramOff before it starts
// one.
func AddProgram(module, name, summary string, run Program) {
	if _, dup := programs[name]; dup {
		panic("builtinext: program " + name + " provided twice")
	}
	programs[name] = program{run, summary, module}
	moduleswitch.Provided("builtinext.AddProgram", module, func(s moduleswitch.Set) bool { return !ProgramOff(s, name) })
}

// ProgramOff reports whether name is a module's program and s has that module switched off: the extension host
// starts no such program (a hub.json entry or a built-in extension that runs it), as in a build without the module,
// where the subcommand is unknown. A name no module provides (a core subcommand) is never off.
func ProgramOff(s moduleswitch.Set, name string) bool {
	p, ok := programs[name]
	return ok && !s.On(p.module)
}

// LookupProgram is the program the subcommand name runs, if a module in this build provides one.
func LookupProgram(name string) (Program, bool) {
	p, ok := programs[name]
	return p.run, ok
}

// ProgramUsage is the usage lines of the programs, by name, as command's subcommands ("" without any).
func ProgramUsage(command string) string {
	names := make([]string, 0, len(programs))
	for name := range programs {
		names = append(names, name)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, name := range names {
		fmt.Fprintf(&b, "  %-26s%s\n", command+" "+name, programs[name].summary)
	}
	return b.String()
}

// Volc is what the extension host shows of the built-in Volcengine speech extension (internal/modules/volc,
// docs/extensions/volcengine-speech.md): its ids and the choices of its settings form.
type Volc struct {
	// ExtensionID is the extension's id; TranscriptionContribution and SpeechContribution its contributions' ids.
	ExtensionID, TranscriptionContribution, SpeechContribution string
	// StatusPage is its page's id, SecretKey the name of its key's secret.
	StatusPage, SecretKey string
	// Voices and ASRModels are the form's choices, the default first.
	Voices, ASRModels []string
}

var volc = moduleswitch.NewPort[Volc]("builtinext.ProvideVolc")

// ProvideVolc provides the Volcengine speech extension under its module's id (init only).
func ProvideVolc(module string, v Volc) { volc.Provide(module, v) }

// VolcSpeech is the Volcengine speech extension, if its module is in this build and s has it on (the `modules`
// setting the manager started with): switched off, the extension host registers no such extension.
func VolcSpeech(s moduleswitch.Set) (Volc, bool) { return volc.Get(s) }

// Voice is what the extension host needs of the built-in voice extension (internal/modules/voice, `personad voice`,
// docs/assistant/voice.md): the id of its transcription contribution, the meeting transcription a hub without a
// chosen one uses, and how its settings merge, which the host reads to say what the extension lacks.
type Voice struct {
	// TranscriptionContribution is the id of its transcription contribution.
	TranscriptionContribution string
	// GatewayProvider is the upstream a quota gateway extension's settings give the extension (config, its name and
	// API key), serialized as the extension reads PERSONA_VOICE_GATEWAY_CONFIG; false when they give none.
	GatewayProvider func(config json.RawMessage, name, key string) (json.RawMessage, bool)
	// State is what the extension lacks before a conversation can start ("" nothing) and the settings it does not
	// take, merged as the extension merges them; an error when they do not merge (the extension says why itself).
	State func(VoiceSources) (setup string, warnings []string, err error)
	// FormURL is the address in the settings form config ("" none).
	FormURL func(config json.RawMessage) string
	// FormTTSURL is the streaming TTS endpoint address in the settings form config ("" none).
	FormTTSURL func(config json.RawMessage) string
	// Handler serves the extension's HTTP side in this process on a configuration as hub.json gives it, reaching
	// the hub at hubURL with hubToken: the manager's end-to-end tests of meeting transcription run it so.
	Handler func(config json.RawMessage, hubURL, hubToken string, logger *log.Logger) (http.Handler, error)
}

// VoiceSources are where the voice extension's settings come from (the extension's own Sources).
type VoiceSources struct {
	// Form is the settings form, Key and TTSKey its secrets ("set" when only whether each is set is known).
	Form   json.RawMessage
	Key    string
	TTSKey string
	// HubJSON is the config of the hub.json entry the extension took over; Gateway the upstream a quota gateway gives.
	HubJSON, Gateway json.RawMessage
}

var voice = moduleswitch.NewPort[Voice]("builtinext.ProvideVoice")

// ProvideVoice provides the voice extension under its module's id (init only).
func ProvideVoice(module string, v Voice) { voice.Provide(module, v) }

// VoiceExtension is the voice extension, if its module is in this build and s has it on (the `modules` setting the
// manager started with): switched off, the extension host registers no such extension and no meeting transcribes
// with it.
func VoiceExtension(s moduleswitch.Set) (Voice, bool) { return voice.Get(s) }
