package assistant

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Track kinds (#13472, docs/assistant/health.md "Tracks and kinds"). A kind is what its tracks do: a config whose every
// field picks one of the options defined here (a new option is a code change; a new combination is a new kind, config
// only). The built-in kinds are the manager's; the user's are rows (assistant_track_kinds). Nothing branches on a
// kind's name: every decision reads a field of the track's effective config (its kind's, with what the track sets).

// The core owns orchestration and the headless health policy. Domain-space
// kinds are declarative extension contributions.
const (
	trackKindOrchestration = "orchestration"
	trackKindHealth        = "health"
)

// The options of each field.
var (
	trackUserInstructionOptions = []string{"", "space_md"}
	trackContextOptions         = []string{"engineering", "track", "none"}
	trackSupervisionOptions     = []string{"leaders", "routine", "none"}
	trackAssemblyOptions        = []string{"assistant", "track"}
	trackChannelOptions         = []string{"topics", "single"}
	trackPushCapOptions         = []string{assistantwire.LevelP0, assistantwire.LevelP1, assistantwire.LevelP2}
	trackDeepDefaultOptions     = []string{"quick", "deep"}
	trackEnterModeOptions       = []string{"none", "immersive"}
	trackTopicPolicyOptions     = []string{"new", "continue"}
)

// The scenes and rules a kind's usage names (usage.go), by their wire names.
var (
	trackUsageScenes = map[string]scene{"source_track": sceneSourceTrack, "elsewhere": sceneElsewhere, "hand_off": sceneHandOff,
		"lock_screen": sceneLockScreen, "others": sceneOthers, "extension": sceneExtension, "kb_write": sceneKBWrite}
	trackUsageRules = map[string]usageRule{"allow": usageAllow, "relevant": usageRelevant, "minimal": usageMinimal,
		"redact": usageRedact, "confirm": usageConfirm, "deny": usageDeny}
)

// trackKindAll is a usage rule's category for every category, general and unknown information included.
const trackKindAll = "*"

// Kind warnings: what a kind asks that this manager does not do yet.
const trackWarnSupervisionLeaders = "supervision_leaders" // leaders review the main chat only (a later step)

// builtinKindConfigs contains only the core orchestration kind. Health keeps
// its own headless policy kind. Domain-space kinds come from enabled extensions.
func builtinKindConfigs() map[string]assistantwire.TrackKindConfig {
	attention := assistantwire.TrackAttention{PushCap: assistantwire.LevelP0, DeepDefault: "quick", EnterMode: "none"}
	return map[string]assistantwire.TrackKindConfig{
		trackKindOrchestration: {Schema: assistantwire.TrackKindSchema, Template: trackTemplateAssistant, Context: "engineering",
			DefaultCategories: []string{}, TagFocus: []string{}, Rhythms: []assistantwire.TrackRhythm{}, Attention: attention,
			Supervision: "leaders", Usage: []assistantwire.TrackUsageRule{}, Assembly: "assistant", Channels: []string{}},
		trackKindHealth: {Schema: assistantwire.TrackKindSchema, Template: trackTemplateHealth, Context: "track",
			DefaultCategories: []string{categoryHealth}, TagFocus: []string{categoryHealth}, Rhythms: []assistantwire.TrackRhythm{}, Attention: attention,
			Supervision: "none", Usage: []assistantwire.TrackUsageRule{}, Assembly: "track", Channels: []string{}},
	}
}

// builtinKindOrder lists the headless kinds the core owns.
var builtinKindOrder = []string{trackKindOrchestration, trackKindHealth}

// kindEntry is one kind: built in, or the user's (a row).
type kindEntry struct {
	Slug    string
	Title   string
	Config  assistantwire.TrackKindConfig
	Builtin bool
	// Extension marks a declarative contribution: like a built-in, it cannot be
	// replaced through the user's custom-kind API.
	Extension bool
	// Owner is the extension contributing an Extension kind ("" for any other, and from a hub that does not say).
	Owner    string
	Revision int64
	// Disabled says why the kind is not enabled (kindDisabledSchema, kindDisabledInvalid); its tracks are left out.
	Disabled string
}

// Why a kind is not enabled (kindEntry.Disabled).
const (
	kindDisabledSchema  = "schema"
	kindDisabledInvalid = "invalid"
)

// trackIconPattern is a design-system icon token's name: never an SF Symbol's (they have dots) or a color.
var trackIconPattern = lazyre.New(`^[a-z][a-zA-Z0-9_]{0,63}$`)

// validTrackIcon: icon is empty or a token name.
func validTrackIcon(icon string) bool { return icon == "" || trackIconPattern.MatchString(icon) }

// validKindSlug: slug may name a user's kind (a track's slug's rules, and not a built-in kind's).
func validKindSlug(slug string) bool {
	return identity.ValidTrackSlug(slug) && slug != trackKindOrchestration && slug != trackKindHealth
}

// errKindSchema is a kind config newer than this manager knows.
var errKindSchema = fmt.Errorf("the kind's config is newer than this assistant (schema above %d): update the assistant", assistantwire.TrackKindSchema)

// parseKindConfig reads a kind's config from its stored JSON: a newer schema is errKindSchema, a field this manager does
// not know or an option it does not have is an error.
func parseKindConfig(raw []byte) (assistantwire.TrackKindConfig, error) {
	var head struct {
		Schema int `json:"schema"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return assistantwire.TrackKindConfig{}, err
	}
	if head.Schema > assistantwire.TrackKindSchema {
		return assistantwire.TrackKindConfig{}, errKindSchema
	}
	var c assistantwire.TrackKindConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return assistantwire.TrackKindConfig{}, err
	}
	return normalKindConfig(c)
}

// normalKindConfig checks every field of c against its options, and gives it in a normal form (schema set, categories
// sorted, no nil lists). A usage rule looser than the assistant's own rules is an error.
func normalKindConfig(c assistantwire.TrackKindConfig) (assistantwire.TrackKindConfig, error) {
	if c.Schema == 0 {
		c.Schema = assistantwire.TrackKindSchema
	}
	if c.Schema > assistantwire.TrackKindSchema {
		return c, errKindSchema
	}
	if c.Schema < 0 {
		return c, errors.New("schema: not a version")
	}
	if !slices.Contains(trackTemplates, c.Template) {
		return c, fmt.Errorf("template: %q is not one of %v", c.Template, trackTemplates)
	}
	for _, f := range []struct {
		name, v string
		opts    []string
	}{
		{"user_instructions", c.UserInstructions, trackUserInstructionOptions},
		{"context", c.Context, trackContextOptions},
		{"supervision", c.Supervision, trackSupervisionOptions},
		{"assembly", c.Assembly, trackAssemblyOptions},
		{"attention.push_cap", c.Attention.PushCap, trackPushCapOptions},
		{"attention.deep_default", c.Attention.DeepDefault, trackDeepDefaultOptions},
		{"attention.enter_mode", c.Attention.EnterMode, trackEnterModeOptions},
	} {
		if !slices.Contains(f.opts, f.v) {
			return c, fmt.Errorf("%s: %q is not one of %q", f.name, f.v, f.opts)
		}
	}
	var ok bool
	if c.DefaultCategories, ok = kindCategories(c.DefaultCategories); !ok {
		return c, errors.New("default_categories: not an information category")
	}
	if c.TagFocus, ok = kindCategories(c.TagFocus); !ok {
		return c, errors.New("tag_focus: not an information category")
	}
	if c.Channels == nil {
		c.Channels = []string{}
	}
	for _, ch := range c.Channels {
		if !slices.Contains(trackChannelOptions, ch) {
			return c, fmt.Errorf("channels: %q is not one of %q", ch, trackChannelOptions)
		}
	}
	if c.Rhythms == nil {
		c.Rhythms = []assistantwire.TrackRhythm{}
	}
	for i, r := range c.Rhythms {
		if err := validRhythm(r); err != nil {
			return c, fmt.Errorf("rhythms[%d]: %w", i, err)
		}
	}
	if c.Usage == nil {
		c.Usage = []assistantwire.TrackUsageRule{}
	}
	for i, u := range c.Usage {
		if err := tightensUsage(u); err != nil {
			return c, fmt.Errorf("usage[%d]: %w", i, err)
		}
	}
	if !validTrackIcon(c.Sidebar.Icon) {
		return c, errors.New("sidebar.icon: not an icon token's name")
	}
	if len(c.Sidebar.Group) > 40 {
		return c, errors.New("sidebar.group: longer than 40 bytes")
	}
	return c, nil
}

// kindCategories is cats in a normal form (none: an empty list), and whether each is an information category.
func kindCategories(cats []string) ([]string, bool) {
	out, ok := normalCategories(cats)
	if out == nil {
		out = []string{}
	}
	return out, ok
}

// validRhythm checks one scheduled task a kind gives its new tracks.
func validRhythm(r assistantwire.TrackRhythm) error {
	if r.Title == "" || len(r.Title) > 200 {
		return errors.New("title: empty or longer than 200 bytes")
	}
	if r.Prompt == "" || len(r.Prompt) > 4000 {
		return errors.New("prompt: empty or longer than 4000 bytes")
	}
	if _, err := parseCron(r.Cron); err != nil {
		return fmt.Errorf("cron: %w", err)
	}
	if r.TZ != "" {
		if _, err := time.LoadLocation(r.TZ); err != nil {
			return fmt.Errorf("tz: %w", err)
		}
	}
	if !slices.Contains(trackTopicPolicyOptions, r.TopicPolicy) {
		return fmt.Errorf("topic_policy: %q is not one of %q", r.TopicPolicy, trackTopicPolicyOptions)
	}
	return nil
}

// errUsageLooser is a usage rule looser than the assistant's own: a kind or a track can only tighten.
var errUsageLooser = errors.New("looser than the assistant's own rule: a kind or a track can only tighten how information is used")

// tightensUsage checks that u is at least as strict as usage's own rule for every piece of information it covers: of
// its category, or (for "*") of any category, general and unknown included, outside the track it came from.
func tightensUsage(u assistantwire.TrackUsageRule) error {
	sc, ok := trackUsageScenes[u.Scene]
	if !ok {
		return fmt.Errorf("scene: %q is not one", u.Scene)
	}
	rule, ok := trackUsageRules[u.Rule]
	if !ok {
		return fmt.Errorf("rule: %q is not one", u.Rule)
	}
	var floor usageRule
	switch {
	case u.Category == trackKindAll:
		floor = usage(infoUse{SourceTrack: "-"}, sc, extensionGrant{}) // unknown information: the strictest
	case slices.Contains(infoCategories, u.Category):
		floor = usage(infoUse{Categories: []string{u.Category}, Known: true, SourceTrack: "-"}, sc, extensionGrant{})
	default:
		return fmt.Errorf("category: %q is not an information category or %q", u.Category, trackKindAll)
	}
	if rule < floor {
		return errUsageLooser
	}
	return nil
}

// kindWarnings are what config c asks that this manager does not do yet, for a kind's tracks (never the main chat).
func kindWarnings(c assistantwire.TrackKindConfig) []string {
	if c.Supervision == "leaders" {
		return []string{trackWarnSupervisionLeaders}
	}
	return nil
}

// The fields a track may set apart from its kind (config_override). Template and default categories are the track's
// own columns, not part of its override.
var trackOverridable = []string{"tag_focus", "rhythms", "attention", "usage", "capture_target", "sidebar"}

// applyTrackOverride is kind config c with what a track sets apart (override, a JSON object of trackOverridable
// fields; the sidebar's icon only). The track's usage rules come on top of its kind's, so a track can only tighten.
func applyTrackOverride(c assistantwire.TrackKindConfig, override []byte) (assistantwire.TrackKindConfig, error) {
	if len(bytes.TrimSpace(override)) == 0 {
		return c, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(override, &fields); err != nil {
		return c, fmt.Errorf("config_override: %w", err)
	}
	strict := func(raw json.RawMessage, v any) error {
		dec := json.NewDecoder(bytes.NewReader(raw))
		dec.DisallowUnknownFields()
		return dec.Decode(v)
	}
	out := c
	out.Usage = slices.Clone(c.Usage)
	for name, raw := range fields {
		var err error
		switch name {
		case "tag_focus":
			err = strict(raw, &out.TagFocus)
		case "rhythms":
			out.Rhythms = nil
			err = strict(raw, &out.Rhythms)
		case "attention":
			err = strict(raw, &out.Attention) // only the fields given change
		case "usage":
			var more []assistantwire.TrackUsageRule
			if err = strict(raw, &more); err == nil {
				out.Usage = append(out.Usage, more...)
			}
		case "capture_target":
			err = strict(raw, &out.CaptureTarget)
		case "sidebar":
			var sb struct {
				Icon *string `json:"icon"`
			}
			if err = strict(raw, &sb); err == nil && sb.Icon != nil {
				out.Sidebar.Icon = *sb.Icon
			}
		default:
			return c, fmt.Errorf("config_override: a track cannot set %q (only %q)", name, trackOverridable)
		}
		if err != nil {
			return c, fmt.Errorf("config_override.%s: %w", name, err)
		}
	}
	out, err := normalKindConfig(out)
	if err != nil {
		return c, fmt.Errorf("config_override: %w", err)
	}
	return out, nil
}

// effectiveTrackConfig is a track's effective config: its kind's, with the track's own template and default categories
// and its override.
func effectiveTrackConfig(kind assistantwire.TrackKindConfig, template string, cats []string, override []byte) (assistantwire.TrackKindConfig, error) {
	c, err := applyTrackOverride(kind, override)
	if err != nil {
		return kind, err
	}
	c.Template = template
	var ok bool
	if c.DefaultCategories, ok = kindCategories(cats); !ok {
		return kind, errors.New("default_categories: not an information category")
	}
	return normalKindConfig(c)
}
