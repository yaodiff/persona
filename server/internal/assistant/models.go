package assistant

import (
	"cmp"
	"context"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/secretkey"
)

// The model configuration (docs/assistant/model-config.md) is the assistant's: the four categories' records (settings
// model_config.<id>), the providers added in Persona with their keys sealed (models_launchers.go), the hidden ~/.zshrc
// launchers. Only the assistant reads and writes them; the hub hands over what the machines and the plugins have, and
// asks only what its plugins need (models_hub.go). Every model call is made from here, with the keys only the
// assistant opens.

// The Claude profiles GET /model-config offers for the main chat's new conversations (#4633): Persona's own profile
// (the default, stored as none) or this computer's existing Claude Code setup, resolved as the node resolves
// config_dir "legacy" (docs/assistant/assistant.md). Selecting one copies nothing between profiles.
const (
	assistantConfigDirPrivate = "private"
	assistantConfigDirLegacy  = "legacy"
)

var assistantConfigDirs = []string{assistantConfigDirPrivate, assistantConfigDirLegacy}

type modelRecord struct {
	Revision int64        `json:"revision"`
	Binding  ModelBinding `json:"binding"`
	// Candidates: an extension's purpose only, what the user decided about its candidate models (model_candidates.go).
	Candidates *candidateSetting `json:"candidates,omitempty"`
}

// ModelProvider is one provider as Settings › Model configuration offers it: never its key.
type ModelProvider struct {
	ID              string                  `json:"id"`
	Name            string                  `json:"name"`
	Kind            string                  `json:"kind"`
	Source          string                  `json:"source,omitempty"`
	Agent           string                  `json:"agent,omitempty"`
	API             string                  `json:"api,omitempty"`
	ChatEffort      bool                    `json:"chat_effort,omitempty"`
	Effort          string                  `json:"effort,omitempty"`
	Model           string                  `json:"model,omitempty"`
	Machines        []string                `json:"machines,omitempty"`
	Hidden          bool                    `json:"hidden,omitempty"`
	HasKey          bool                    `json:"has_key,omitempty"`
	PermissionModes proto.PermissionModeSet `json:"permission_modes,omitempty"`
	Models          []proto.ProviderModel   `json:"models,omitempty"`
	ModelAliases    map[string]string       `json:"model_aliases,omitempty"`
	Status          string                  `json:"status,omitempty"`
	// Internal provenance for model.run compatibility checks (never serialized).
	builtinCompanion bool
	modelRunMachines []string
}

type ModelChoice = assistantwire.ModelChoice

type ModelUse struct {
	Name string `json:"name"`
}

// ModelPurpose is one purpose (a category, or a use under one) with what it runs on.
type ModelPurpose struct {
	ID         string     `json:"id"`
	CategoryID string     `json:"category_id,omitempty"`
	Uses       []ModelUse `json:"uses,omitempty"`
	Name       string     `json:"name"`
	// Description is what the use is for, in a sentence in the request's language (#5086): wording only, it names no
	// model and changes no binding. Built-in uses have one; a scheduled task's own, an extension's and a plugin's do not.
	Description   string       `json:"description,omitempty"`
	Group         string       `json:"group"`
	Kind          string       `json:"kind"`
	Launch        string       `json:"launch,omitempty"` // derived from the purpose, never stored or edited
	Parent        string       `json:"parent,omitempty"`
	MachineID     string       `json:"machine_id,omitempty"`
	Override      ModelBinding `json:"override"`
	Effective     ModelBinding `json:"effective"`
	Revision      int64        `json:"revision"`
	InheritedFrom string       `json:"inherited_from,omitempty"`
	Status        string       `json:"status,omitempty"`
	Limit         *ai.Pause    `json:"limit,omitempty"` // the connection is held: quota used up or busy (ai/limit.go)
	Fields        []string     `json:"fields"`
	ProviderIDs   []string     `json:"provider_ids"`
	ConfigDirs    []string     `json:"config_dirs,omitempty"` // assistant only: the profiles its new conversations can start in
	// Candidates (an extension's purpose only, #10398): its candidate models as the extension sends them, and as the
	// user keeps, switches off or replaces them (model_candidates.go).
	Candidates *ModelCandidates `json:"candidates,omitempty"`
	// Usage is what the purpose's calls of the last 7 days added up to (model_usage.go); the list only, none without calls.
	Usage *db.ModelUsage `json:"usage,omitempty"`
}

// launch is the purpose's execution form, independent of its provider.
// API and plugin purposes never launch a process, even when their parent does.
func (p ModelPurpose) launch() string {
	if p.Kind != "launcher" {
		return ""
	}
	for _, id := range []string{p.ID, p.Parent} {
		switch id {
		case "assistant", "schedule", "inbox.agent", "lark.agent":
			return "headless"
		case "agent.new":
			return "engine"
		}
		if strings.HasPrefix(id, "task.") {
			return "headless"
		}
	}
	return ""
}

var modelCategoryIDs = []string{"assistant", "agent.new", "kb", "inbox.triage", "agent.delegate", "translate"}

// The catalog also includes the uses set apart from their category: the opt-in overrides of Agents (agentsOverride),
// in the order of Agents' uses, then the background supervisor's (#5806), then the In-meeting conversation's and
// Meeting minutes' (#7980), last so the rows the apps drew before each was listed keep their order; category-default
// assignment and model profiles must not write them.
var modelConfigIDs = append(slices.Clone(modelCategoryIDs), purposeAgent, purposeLarkAgent, purposeSupervise, purposeMeetingRoom, purposeMeetingMinutes, purposeMain)

// agentsOverride: a use of Agents the user may set apart, the AI twin (#4232) or Lark /agent. With nothing saved it
// follows Agents; every other use follows its category only.
func agentsOverride(id string) bool { return id == purposeAgent || id == purposeLarkAgent }

const purposeMain = "assistant.main"

// modelPurposes are the uses; Name, Group and Uses hold catalog names (assistant-models), read in a language by
// localized.
var modelPurposes = []ModelPurpose{
	{ID: "assistant", Group: "group_default", Kind: "launcher", ConfigDirs: assistantConfigDirs, Uses: []ModelUse{{Name: "use_chat"}, {Name: "use_schedule"}, {Name: "use_meeting_room"}}},
	{ID: "agent.new", Group: "group_default", Kind: "launcher", Uses: []ModelUse{{Name: "use_new_agent"}, {Name: "use_inbox_agent"}, {Name: "use_lark_agent"}}},
	{ID: "kb", Group: "group_default", Kind: "api", Uses: []ModelUse{{Name: "use_draft"}, {Name: "use_knowledge"}, {Name: "use_projects"}, {Name: "use_titles"},
		{Name: "use_meeting_minutes"}, {Name: "use_meeting_answer"}}},
	{ID: "inbox.triage", Group: "group_default", Kind: "api", Uses: []ModelUse{{Name: "use_triage"}, {Name: "use_importance"}, {Name: "use_reply_or_agent"}}},
	// "Answering agents' questions" (delegate.go) is a category of its own, so it can run on a stronger model than the triage's (#3029).
	{ID: "agent.delegate", Group: "group_default", Kind: "api", Uses: []ModelUse{{Name: "use_delegate"}}},
	{ID: "translate", Group: "group_default", Kind: "api", Uses: []ModelUse{{Name: "use_translate"}}},
	{ID: "inbox.agent", Group: "group_agents", Kind: "launcher", Parent: "agent.new"},
	{ID: "lark.agent", Group: "group_agents", Kind: "launcher", Parent: "agent.new"},
	{ID: "inbox.judge", Group: "group_triage", Kind: "api", Parent: "inbox.triage"},
	{ID: "agent.triage", Group: "group_triage", Kind: "api", Parent: "inbox.triage"},
	// Tagging what a conversation says with information categories (info_tag.go, #13246): short calls with no tools,
	// always on Sorting's model.
	{ID: purposeInfoTag, Group: "group_triage", Kind: "api", Parent: "inbox.triage"},
	// Sorting a quick capture into a track and tagging it, in one call (capture_sort.go, #13502): Sorting's model
	// until it is given its own.
	{ID: purposeCaptureSort, Group: "group_triage", Kind: "api", Parent: "inbox.triage"},
	{ID: "inbox.draft", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "kb.compress", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "kb.project", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "kb.dedupe", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "kb.items", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "kb.moments", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "kb.topics", Group: "group_processing", Kind: "api", Parent: "kb"}, // knowledge points by topic (kb/topics.go)
	{ID: "assistant.title", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "assistant.route", Group: "group_processing", Kind: "api", Parent: "kb"}, // Suggest a supervisor for a new agent (agent_route.go).
	{ID: "agent.title", Group: "group_processing", Kind: "api", Parent: "kb"},
	{ID: "todo.title", Group: "group_processing", Kind: "api", Parent: "kb"}, // a task's todo (assistant/todo_titles.go)
	// A meeting or note named while it records (kb/recording_title.go, #8873): a few short calls a recording.
	{ID: purposeMeetingTitle, Group: "group_processing", Kind: "api", Parent: "kb"},
	// Drafting a source subscription's recipe, called by the manager (models.purpose_complete, #2040).
	{ID: assistantwire.PurposeSubscriptionRecipe, Group: "group_processing", Kind: "api", Parent: "kb"},
	// Answers to the questions asked in an attended meeting (meeting_ask.go): one call with no tools, from meeting material only.
	// A use of Information processing even with the In-meeting conversation set apart: it answers when the room cannot.
	{ID: purposeMeetingAnswer, Group: "group_processing", Kind: "api", Parent: "kb"},
	// A meeting's minutes (modules/recording/writers/minutes.go, #7980): Information processing's until they are given their own provider,
	// model or effort.
	{ID: purposeMeetingMinutes, Group: "group_processing", Kind: "api", Parent: "kb"},
	// The next-message suggestion over an empty composer (suggest_next.go, #4644): one short call with no tools after a turn.
	{ID: purposeSuggestNext, Group: "group_processing", Kind: "api", Parent: "kb"},
	// Scheduled tasks (the morning briefing…) run in the assistant's directory with its instructions, memory and tools: they are the
	// assistant's conversations, on the main chat's model, not new Agents (#1544; #697 had them under Agents, plain Claude Code
	// by default, which is not logged in on every hub machine).
	{ID: "schedule", Group: "group_assistant", Kind: "launcher", Parent: "assistant"},
	// The background supervisor (supervise_background.go, #4927): the main chat's model until it is given its own, which
	// may be a cheaper or a stronger one. In the catalog the apps list (modelConfigIDs, #5806): each app that asked for
	// it by its own id draws it once, and one from before draws the row with the name sent here.
	{ID: purposeSupervise, Group: "group_assistant", Kind: "launcher", Parent: "assistant"},
	// Main execution is independent of the defaults for new side conversations.
	{ID: purposeMain, Group: "group_assistant", Kind: "launcher", Parent: "assistant"},
	// The room conversation of a recorded meeting (meeting_room.go, #7980): the main chat's launcher, model and effort
	// until it is given its own, Claude Code only and never a runtime (models_room.go).
	{ID: purposeMeetingRoom, Group: "group_assistant", Kind: "launcher", Parent: "assistant"},
}

// ownModelRecord: a use under a category that keeps a record of its own, so it can be set apart from the category:
// the opt-in overrides of Agents, a scheduled task's own use, the background supervisor's, the In-meeting
// conversation's and Meeting minutes' (#7980). With nothing saved it follows its category.
func ownModelRecord(id string) bool {
	return agentsOverride(id) || isTaskPurpose(id) || id == purposeMain || id == purposeSupervise || id == purposeMeetingRoom || id == purposeMeetingMinutes
}

// mc is a assistant-models text in l.
func mc(l i18n.Lang, name string, args ...i18n.Args) string {
	return i18n.T(l, "assistant-models."+name, args...)
}

// purposeName is a built-in use's name in l.
func purposeName(l i18n.Lang, id string) string {
	return mc(l, "name_"+strings.ReplaceAll(id, ".", "_"))
}

// purposeDescription is what a built-in use is for, in a sentence in l (assistant-models desc_<id>).
func purposeDescription(l i18n.Lang, id string) string {
	return mc(l, "desc_"+strings.ReplaceAll(id, ".", "_"))
}

// localized is a built-in use's definition with its texts in l.
func (p ModelPurpose) localized(l i18n.Lang) ModelPurpose {
	p.Name, p.Group, p.Description = purposeName(l, p.ID), mc(l, p.Group), purposeDescription(l, p.ID)
	if len(p.Uses) > 0 {
		uses := make([]ModelUse, len(p.Uses))
		for i, u := range p.Uses {
			uses[i] = ModelUse{Name: mc(l, u.Name)}
		}
		p.Uses = uses
	}
	return p
}

// requestCtxLang is the language of what ctx is answering: the request's (http.go puts it in the context), else the
// user's.
func (s *Service) requestCtxLang(ctx context.Context) i18n.Lang {
	return i18n.From(ctx).Or(s.lang(ctx))
}

// isTaskPurpose: a scheduled task's own use (task.<task id>).
func isTaskPurpose(id string) bool { return strings.HasPrefix(id, "task.") }

func modelCategoryID(id string) string {
	if isTaskPurpose(id) {
		return "assistant"
	}
	for _, p := range modelPurposes {
		if p.ID == id && p.Parent != "" {
			return p.Parent
		}
	}
	return id
}

var errModelPurposeNotFound = i18n.Err("assistant-models.err_purpose_not_found")

var errModelConflict = i18n.Err("assistant-models.err_conflict")

const (
	modelKeyPrefix         = "model_config."
	modelOverrideKeyPrefix = modelKeyPrefix + "overrides.v1."
)

func modelKey(id string) string { return modelKeyPrefix + id }

// The AI twin, Lark /agent and a task's own use were retired with category-only settings. A fresh namespace keeps
// dormant records (model_config.inbox.agent, model_config.lark.agent, model_config.task.<id>) inert, including their
// revisions. Only a new user save creates such a record.
func modelRecordKey(id string) string {
	if agentsOverride(id) || isTaskPurpose(id) {
		return modelOverrideKeyPrefix + id
	}
	return modelKey(id)
}

func modelRecordID(p ModelPurpose) string {
	// The AI twin, Lark /agent, an extension's purpose, a task's own use, the background supervisor's and the two
	// meeting purposes (#7980, under model_config.<id>: none was ever written before) have records of their own.
	if p.CategoryID != "" && !ownModelRecord(p.ID) && !isExtensionPurpose(p.ID) {
		return p.CategoryID
	}
	return p.ID
}

// modelsState is the model configuration's: one change at a time, the key that seals the providers' keys, the
// launchers' model lists, the machines' ~/.zshrc launchers as last seen.
type modelsState struct {
	mu sync.Mutex // one change of the configuration at a time (and the test stores without compare-and-set)

	secret *secretkey.Box // secret.key; a key made for this run only in tests
	quotas quotaCache     // the launchers' quota, kept a minute (models_quota.go)

	cacheMu  sync.Mutex
	cacheAt  map[string]time.Time
	have     map[string]map[string]bool   // machine \x00 launcher → its models (ids and what aliases resolve to)
	declared map[string][]string          // machine \x00 launcher → every id the launcher declared (ids, resolved, aliases)
	aliases  map[string]map[string]string // machine \x00 launcher → alias id → its resolved id
	defaults map[string]string            // machine \x00 launcher → its default model's resolved or own id

	customMu    sync.Mutex // one change of the remembered typed ids at a time (models_custom.go)
	requestedMu sync.Mutex // one change of the extensions' requested candidate lists at a time (model_candidates.go)

	zshrcMu           sync.Mutex
	zshrcWrite        sync.Mutex // serialize source-scoped snapshot persistence without holding the reader lock
	externalRefresh   map[string]*externalDirectoryRefresh
	zshrc             map[string][]proto.Launcher // machine → its rc-file launchers (nil: not asked since the start)
	zshrcRefreshing   bool                        // one background refresh fills the last-seen snapshot at a time
	zshrcRefreshAgain bool                        // a node changed its directory during that refresh
	// zshrcSync: how each machine's list came in the last time they were asked (设置 › 模型供应商's 各台电脑).
	zshrcSync map[string]LauncherSync

	ownMu sync.Mutex
	own   map[string]ownSeen // machine + "/" + agent → its own service as the node said (models_launchers_scope.go)

	launcherLists launcherListCache // per-(machine, host) short-TTL launchers.list cache with singleflight (launcher_list_cache.go)
}

func (s *Service) modelRecord(ctx context.Context, id string) (modelRecord, error) {
	var r modelRecord
	if st := s.store(); st != nil {
		_, err := memoSetting(ctx, st, modelRecordKey(id), &r)
		return r, err
	}
	return r, nil
}

// saveModelRecord writes purpose id's record if it is still at revision (the database compares and sets; the test
// stores without that rely on the caller holding models.mu).
func (s *Service) saveModelRecord(ctx context.Context, id string, revision int64, b ModelBinding) error {
	return s.writeModelRecord(ctx, id, revision, b, nil, false)
}

// writeModelRecord is saveModelRecord that also sets an extension purpose's candidate setting when setCandidates
// (nil: the extension's own list); otherwise the stored setting is kept.
func (s *Service) writeModelRecord(ctx context.Context, id string, revision int64, b ModelBinding, candidates *candidateSetting, setCandidates bool) error {
	if err := checkExternalPurpose(b); err != nil {
		return err
	}
	st := s.store()
	if st == nil {
		return i18n.Err("assistant-models.err_no_database")
	}
	r := modelRecord{Revision: revision + 1, Binding: b, Candidates: candidates}
	if !setCandidates && isExtensionPurpose(id) { // a binding change keeps what the user decided about candidates
		var old modelRecord
		if _, err := st.Setting(ctx, modelRecordKey(id), &old); err != nil {
			return err
		}
		if old.Revision != revision {
			return errModelConflict
		}
		r.Candidates = old.Candidates
	}
	if cas, ok := st.(interface {
		CompareSetting(context.Context, string, int64, any) (bool, error)
	}); ok {
		updated, err := cas.CompareSetting(ctx, modelRecordKey(id), revision, r)
		if err != nil {
			return err
		}
		if !updated {
			return errModelConflict
		}
		return nil
	}
	var old modelRecord
	if _, err := st.Setting(ctx, modelRecordKey(id), &old); err != nil {
		return err
	}
	if old.Revision != revision {
		return errModelConflict
	}
	return st.PutSetting(ctx, modelRecordKey(id), r)
}

// saveModel is saveModelRecord at r's revision, one change at a time.
func (s *Service) saveModel(ctx context.Context, id string, r modelRecord) error {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	return s.saveModelRecord(ctx, id, r.Revision, r.Binding)
}

func (s *Service) modelPurposeDef(ctx context.Context, id string) (ModelPurpose, error) {
	l := s.requestCtxLang(ctx)
	for _, p := range modelPurposes {
		if p.ID == id {
			p = p.localized(l)
			p.CategoryID = modelCategoryID(id)
			if p.Kind == "launcher" && p.ID != "agent.new" {
				p.MachineID = s.settings(ctx).MachineID
			}
			if p.ID == "inbox.agent" {
				h := s.hello(ctx)
				if m := h.Models.InboxAgentMachine; m != "" && slices.Contains(h.Assistant.Machines, m) {
					p.MachineID = m
				}
			}
			return p, nil
		}
	}
	if isTaskPurpose(id) {
		tasks := s.taskViews(ctx)
		if tasks == nil {
			return ModelPurpose{}, i18n.Err("assistant-models.err_read_tasks")
		}
		for _, t := range tasks {
			if id == "task."+t.ID {
				// A task's own conversation starts on the assistant's machine (schedule.go runTask), so its override
				// is held to that machine's launchers. A conversation it came from keeps its own launcher and does not
				// use this purpose.
				machine := cmp.Or(t.ExecutionMachineID, s.settings(ctx).MachineID, t.MachineID)
				return ModelPurpose{ID: id, CategoryID: "assistant", Name: t.Label, Group: mc(l, "group_assistant"), Kind: "launcher", Parent: "assistant", MachineID: machine}, nil
			}
		}
	}
	if p, ok := s.extensionPurposeDef(ctx, id); ok { // an extension's own (model_rpc.go)
		return p, nil
	}
	if strings.HasPrefix(id, "plugin.") {
		for _, p := range s.voicePlugins(ctx) {
			if id == "plugin."+p.ID {
				return ModelPurpose{ID: id, Name: p.Name, Group: mc(l, "group_extensions"), Kind: "plugin"}, nil
			}
		}
	}
	return ModelPurpose{}, errModelPurposeNotFound
}

// legacyModelConfig is hub.json's connection by its provider id, with its field-wise inheritance (Hello.Models).
func (s *Service) legacyModelConfig(ctx context.Context, id string) ai.Config {
	m := s.hello(ctx).Models
	c := m.AI
	switch id {
	case "legacy.triage":
		if m.TriageAI != nil {
			c = m.TriageAI.Over(c)
		}
	case "legacy.judge":
		if m.JudgeAI != nil {
			c = m.JudgeAI.Over(c)
		}
	case "legacy.kb":
		if m.KBAI != nil {
			c = m.KBAI.Over(c)
		} else {
			c = s.legacyModelConfig(ctx, "legacy.triage")
		}
	}
	return c
}

// voicePlugins are the voice plugins and their models, as the hub has them (plugins.voice).
func (s *Service) voicePlugins(ctx context.Context) []assistantwire.VoicePlugin {
	var res struct {
		Plugins []assistantwire.VoicePlugin `json:"plugins"`
	}
	if err := s.appCall(ctx, AppPlugins, assistantwire.PluginsVoice, map[string]any{}, &res); err != nil {
		s.logf("assistant: voice plugins: %v", err)
	}
	return res.Plugins
}

// viewAgent is the CLI a launcher runs, its name telling when it says none.
func viewAgent(v LauncherView) string {
	if v.Agent == "" && slices.Contains([]string{"codex", "traex", "agy", "omp"}, v.Name) {
		return v.Name
	}
	return v.Agent
}

func (s *Service) modelProviders(ctx context.Context) []ModelProvider {
	l := s.requestCtxLang(ctx)
	out := []ModelProvider{}
	for _, x := range []struct{ id, name string }{{"legacy.ai", mc(l, "provider_legacy_ai")}, {"legacy.triage", mc(l, "provider_legacy_triage")}, {"legacy.judge", mc(l, "provider_legacy_judge")}, {"legacy.kb", mc(l, "provider_legacy_kb")}} {
		c := s.legacyModelConfig(ctx, x.id)
		if c.BaseURL == "" {
			continue
		}
		apiName := c.API
		if apiName == "" {
			apiName = "messages"
		}
		out = append(out, ModelProvider{ID: x.id, Name: x.name, Kind: "api", API: apiName, ChatEffort: apiName == "chat", Model: c.Model, HasKey: c.APIKeyFile != "" || c.APIKey != ""})
	}
	for _, v := range s.launcherViews(ctx) {
		kind := v.Kind
		if kind == "" {
			kind = "launcher"
		}
		apiName := v.API
		if kind == "api" && apiName == "" {
			apiName = "messages"
		}
		agent := v.Agent
		if kind == "launcher" {
			agent = viewAgent(v)
		}
		var aliases map[string]string
		if kind == "api" && apiName == "chat" {
			stored := storedLauncher{ManagedLauncher: proto.ManagedLauncher{
				OpusModel: v.OpusModel, SonnetModel: v.SonnetModel, HaikuModel: v.HaikuModel,
				FableModel: v.FableModel, SmallModel: v.SmallModel,
			}}
			for alias := range apiTierAliases {
				if resolved, err := resolveAPITierAlias(stored, alias); err == nil {
					if aliases == nil {
						aliases = map[string]string{}
					}
					aliases[alias] = resolved
				}
			}
		}
		out = append(out, ModelProvider{ID: "launcher." + v.Name, Name: cmp.Or(v.DisplayName, v.Name), Kind: kind, Source: v.Source, Agent: agent, API: apiName, ChatEffort: kind == "api" && apiName == "chat", Effort: v.Effort, Model: v.Model, Machines: v.Machines, Hidden: v.Hidden, HasKey: v.HasKey, PermissionModes: v.PermissionModes, Models: v.Models, ModelAliases: aliases, builtinCompanion: v.builtinCompanion, modelRunMachines: v.modelRunMachines})
	}
	if !slices.ContainsFunc(out, func(p ModelProvider) bool { return p.ID == "launcher.claude" }) {
		p := ModelProvider{ID: "launcher.claude", Name: mc(l, "provider_claude_default"), Kind: "launcher", Source: "builtin", Agent: "claude"}
		if b, ok := s.builtinClaude(ctx, s.zshrcLaunchers(ctx, false)); ok {
			p.Machines = b.Machines // where plain claude is: Settings › Model Providers lists it by machine
		}
		out = append(out, p)
	}
	if p, ok := s.claudeDesktopProvider(ctx, s.zshrcLaunchers(ctx, false), out); ok {
		out = append(out, p) // Agents' only (models_desktop.go)
	}
	for _, p := range s.voicePlugins(ctx) {
		models := make([]proto.ProviderModel, len(p.Models))
		for i, m := range p.Models {
			models[i] = proto.ProviderModel{ID: m.ID, Name: m.Name}
		}
		out = append(out, ModelProvider{ID: "plugin." + p.ID, Name: p.Name, Kind: "plugin", Model: p.Default, Models: models, Status: p.Error})
	}
	return out
}
