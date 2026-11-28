package assistant

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/agentprompt"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/supervisionrole"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/wireids"
)

// Starting the assistant's conversations (docs/assistant/assistant.md "Process", "New main chat"): its directory on the machine is
// prepared first (the CLAUDE.md written from what the hub tells it, the connectors' and plugins' skills), the model
// configuration applied, then the node's assistant.send. The main conversation is one of them; putting it away for a
// new one keeps what it left for the next one's first message.

// errNoAssistantMachine: the hub has no machine for the assistant. Coded, so the app is told why in plain words with
// what to do (apierr.Call: 409 no_machine, docs/assistant/onboarding.md 5.2); its message stays the words logs and runs record.
// The main conversation's start and the memory (memory) both refuse with it.
var errNoAssistantMachine error = &rpc.Error{Code: proto.CodeNoMachine, Message: "assistant machine is not configured",
	Data: json.RawMessage(`{"delivered":"none"}`)}

// missingAssistantMachine distinguishes a computer awaiting confirmation from having no computer to use.
func (s *Service) missingAssistantMachine(ctx context.Context) error {
	if d, ok := s.pendingAssistantMachine(ctx); ok {
		return apierr.OnMachine(&rpc.Error{Code: proto.CodeMachinePending, Message: errMachinePending.Error(),
			Data: json.RawMessage(`{"delivered":"none"}`)}, d.Name)
	}
	return errNoAssistantMachine
}

// mainTitle is the main conversation's title on the lists the apps get: it has no title of its own
// (conversation_rename refuses it, the apps show the persona there), so neither its first message (the node's title for
// a transcript without one) nor a model's name nor a rename shows up as one (#1313).
const mainTitle = "Persona"

var errRenameMain = i18n.Err("assistant-start.rename_main")

var errWorkAssistant = i18n.Err("assistant-start.work_machine")

// claudeMD is the assistant directory's CLAUDE.md (prompts/assistant/claude_md.md) for its machine here. Its machine
// list identifies the local computer and the computers available for Agent work. The ssh_host aliases are only
// meaningful from the hub machine.
func (s *Service) claudeMD(ctx context.Context, here string, larkCLI bool) string {
	return prompts.RenderAssistantClaudeMD(s.assistantPrompt(ctx, here, larkCLI, s.settings(ctx)))
}

func (s *Service) assistantPrompt(ctx context.Context, here string, larkCLI bool, settings Settings) prompts.AssistantClaudeMD {
	hello := s.hello(ctx)
	d := prompts.AssistantClaudeMD{Owner: strings.TrimSpace(hello.Owner), Notes: s.notes(ctx), ExtraDirs: s.kbExtraDirs(ctx),
		KnowledgeDir: s.kb.dir, LarkCLI: larkCLI, Language: answerLanguage(hello.Locale),
		BackgroundSupervisor: s.backgroundOn(settings), UrgentCall: s.urgentPrompt(ctx)}
	for _, tool := range s.phoneTools(ctx) {
		if strings.HasPrefix(tool.Name, "phone_") && !strings.HasPrefix(tool.Name, "phone_write_queue_") &&
			!strings.HasPrefix(tool.Name, "phone_calendar_") {
			d.PhoneTools = true
		}
		if tool.Name == "phone_calendar_events" {
			d.PhoneCalendar = true
		}
		if tool.Name == "phone_calendar_update_event" || tool.Name == "phone_calendar_delete_event" {
			d.PhoneCalendarWrite = true
		}
	}
	devices := s.devices(ctx)
	if i := slices.IndexFunc(devices, func(d Device) bool { return d.ID == here }); i >= 0 {
		d.OnHub = devices[i].Kind == "local"
		if cwd := s.assistantCwd(devices[i]); cwd != "" {
			d.StateDir = path.Dir(cwd)
		}
	}
	for _, m := range devices {
		local := m.ID == here
		d.Machines = append(d.Machines, prompts.AssistantMachine{Name: m.Name, ID: m.ID, Local: local, SSHHost: m.SSHHost,
			AgentsOnly: !local && !(d.OnHub && m.Kind == "ssh")})
	}
	return d
}

// prepare tells machine's node what it needs before a message to the assistant (node assistant.prepare) and answers
// the assistant's directory there: its CLAUDE.md with the plugins' parts, and the connectors signed in (rate limited
// counts: still signed in), whose built-in skills it puts in the directory
// (assistantskills.Requires) or takes out, and whose command lines it puts on the assistant's PATH (personad tool --cli <id>).
// A plugin skill the node says it does not have goes with its files next time.
func (s *Service) prepare(ctx context.Context, machine string) (string, error) {
	return s.prepareSpace(ctx, machine, "")
}

// prepareSpace is prepare for one of the assistant's spaces: "health" is the health space's directory with its own
// CLAUDE.md (docs/assistant/health.md); the connectors and plugin skills go there as to the assistant's own.
func (s *Service) prepareSpace(ctx context.Context, machine, space string) (string, error) {
	cwd, _, err := s.prepareSpaceIf(ctx, machine, space, false)
	return cwd, err
}

// prepareSpaceIf is prepareSpace; onlyChanged skips the node when it would get what this hub last gave it there, the
// same CLAUDE.md and plugin skills (refreshInstructions), and it answers whether it was written. An answer that brings
// a skill's files always goes: plugins.assistant hands them out once, so skipping would leave the old files on the
// machine until the CLAUDE.md changed.
func (s *Service) prepareSpaceIf(ctx context.Context, machine, space string, onlyChanged bool) (string, bool, error) {
	// Read settings and publish both instruction files in the same per-directory order.
	dir, tmpl := s.assistantRel(), trackTemplateAssistant
	var track trackEntry
	switch space {
	case "":
	case SpaceHealth:
		dir, tmpl = s.healthRel(), trackTemplateHealth
	default:
		// A track with a directory of its own (spaceTrack, #13472); another must never fall back to the assistant's.
		e, ok := s.spaceTrack(ctx, space)
		if !ok {
			return "", false, fmt.Errorf("space %q has no directory on this version", space)
		}
		dir, tmpl, track = trackRel(e), trackTemplateSpace, e
	}
	key := machine + "\x00" + dir
	s.prepareLocks.acquire(key)
	defer s.prepareLocks.release(key)
	if s.isWorkMachine(machine) {
		return "", false, errWorkAssistant
	}
	if tmpl == trackTemplateSpace && !s.trackSpaceNode(machine) {
		// An older node would write the instructions without the track's SPACE.md and folders.
		return "", false, i18n.Err("assistant-health.node_no_track_spaces", i18n.Args{"machine": s.machineName(machine)})
	}
	var conns struct {
		Connectors []string          `json:"connectors"`
		CLIs       map[string]string `json:"clis"`
		LarkCLI    bool              `json:"lark_cli"`
	}
	if err := s.appCall(ctx, AppConnectors, ConnectorsSignedIn, map[string]any{}, &conns); err != nil {
		return "", false, err
	}
	var plugins struct {
		Instructions string          `json:"instructions"`
		Skills       json.RawMessage `json:"skills"`
	}
	// The digest of the hub's own part lets the extensions' data instructions ride along when it changes (the
	// conversation restarts for it anyway) instead of restarting it on their own (extmgr extension_assistant_data.go).
	pluginParams := map[string]any{"machine_id": machine}
	var own, onDemand, viewsGuide string
	if tmpl == trackTemplateAssistant {
		settings := s.settings(ctx)
		d := s.assistantPrompt(ctx, machine, conns.LarkCLI, settings)
		viewsGuide = prompts.RenderAssistantClaudeMD(d)
		d.FullViewGuide = true
		own = prompts.RenderAssistantClaudeMD(d)
		if settings.workGuideOn() {
			d.WorkGuide = true
			onDemand = prompts.RenderAssistantClaudeMD(d)
			d.FullViewGuide = false
			viewsGuide = prompts.RenderAssistantClaudeMD(d)
		}
		sum := sha256.Sum256([]byte(own))
		pluginParams["base_digest"] = hex.EncodeToString(sum[:16])
	}
	if err := s.appCall(ctx, AppPlugins, PluginsAssistant, pluginParams, &plugins); err != nil {
		return "", false, err
	}
	if len(plugins.Skills) == 0 {
		plugins.Skills = json.RawMessage("null")
	}
	md := own + plugins.Instructions
	switch tmpl {
	case trackTemplateHealth:
		md = s.healthClaudeMD(ctx, machine)
	case trackTemplateSpace:
		// Like the health space's: its own baseline, without the instructions plugins give the main chat; their
		// skills still go there.
		md = s.spaceClaudeMD(ctx, machine, track)
	}
	digests, files := skillDigests(plugins.Skills)
	deny := s.trackDenyDirs(ctx)
	denyKey, _ := json.Marshal(deny)
	kind := s.trackKindOf(ctx, space)
	floor, floorKnown := s.trackFloor(ctx, space)
	var prepCats []string
	if floorKnown {
		prepCats, _ = normalCategories(floor)
		if prepCats == nil {
			prepCats = []string{}
		}
	}
	given := preparedState{md: md, skills: digests, workGuide: onDemand != "", viewsGuide: viewsGuide != "", deny: string(denyKey),
		trackKind: kind, categories: strings.Join(prepCats, ","), categoriesKnown: floorKnown}
	// A track's instructions end with its SPACE.md, which the node reads (and the user edits there): the node is asked
	// every time, and what it read is compared after (space_md_digest).
	if onlyChanged && !files && tmpl != trackTemplateSpace {
		s.preparedMu.Lock()
		same := s.prepared[key] == given
		s.preparedMu.Unlock()
		if same {
			return "", false, nil
		}
	}
	params := map[string]any{"dir": dir, "claude_md": md,
		"connectors": conns.Connectors, "clis": conns.CLIs, "plugin_skills": plugins.Skills, "deny_dirs": deny}
	// Older nodes read only claude_md, so they retain every complete instruction. New nodes select each compact
	// guide only after its managed skill is ready; views_guide_md is the final form when all requested guides are ready.
	if onDemand != "" {
		params["work_guide_md"] = onDemand + plugins.Instructions
	}
	if viewsGuide != "" {
		params["views_guide_md"] = viewsGuide + plugins.Instructions
	}
	if tmpl == trackTemplateSpace {
		params["space"] = map[string]any{"slug": track.Slug, "initial_space_md": s.initialSpaceMD(ctx, track)}
	}
	if kind != "" {
		params["track_kind"] = kind // the node's PERSONA_TRACK_KIND for the directory's processes (an older node ignores it)
	}
	if floorKnown {
		params["categories"] = prepCats // the node's PERSONA_CATEGORIES for the directory's processes (an older node ignores it)
	}
	var prep struct {
		Cwd           string   `json:"cwd"`
		Need          []string `json:"plugin_skills_need"`
		SpaceMDDigest string   `json:"space_md_digest"`
	}
	if err := s.call(ctx, machine, DeviceAssistantPrepare, params, &prep); err != nil {
		return "", false, err
	}
	given.spaceMD = prep.SpaceMDDigest
	s.preparedMu.Lock()
	if s.prepared == nil {
		s.prepared = map[string]preparedState{}
	}
	if onlyChanged && tmpl == trackTemplateSpace && len(prep.Need) == 0 && s.prepared[key] == given {
		s.preparedMu.Unlock()
		return prep.Cwd, false, nil // the node had these instructions already
	}
	if len(prep.Need) > 0 {
		// The node lacks some skills: their files come with the next plugins.assistant (plugins.skills_need below), and
		// the next refresh prepares this directory again, so its copies are made from them.
		delete(s.prepared, key)
	} else {
		s.prepared[key] = given
	}
	s.preparedMu.Unlock()
	if len(prep.Need) > 0 {
		if err := s.appCall(ctx, AppPlugins, PluginsSkillsNeed, map[string]any{"machine_id": machine, "names": prep.Need}, nil); err != nil {
			s.logf("assistant: plugin skills %v: %v", prep.Need, err)
		}
	}
	return prep.Cwd, true, nil
}

// preparedState is what prepareSpaceIf gave a node for a directory: the CLAUDE.md, the plugin skills (skillDigests),
// the denied track directories (trackDenyDirs, as JSON), and the track's kind and default categories.
type preparedState struct {
	md, skills, deny string
	workGuide        bool
	viewsGuide       bool
	trackKind        string
	categories       string
	categoriesKnown  bool
	// spaceMD is the digest of the SPACE.md a track's instructions ended with, as the node read it.
	spaceMD string
}

// skillDigests are the plugin skills of a plugins.assistant answer as prepareSpaceIf compares them, each one's plugin,
// name and digest; files says whether any comes with its files.
func skillDigests(raw json.RawMessage) (digests string, files bool) {
	var skills []struct {
		Name   string                     `json:"name"`
		Plugin string                     `json:"plugin"`
		Digest string                     `json:"digest"`
		Files  map[string]json.RawMessage `json:"files"`
	}
	_ = json.Unmarshal(raw, &skills) // null from a manager without plugins: none
	var b strings.Builder
	for _, sk := range skills {
		fmt.Fprintf(&b, "%s/%s %s\n", sk.Plugin, sk.Name, sk.Digest)
		files = files || len(sk.Files) > 0
	}
	return b.String(), files
}

// instructionsEvery is how often refreshInstructions looks at the CLAUDE.md the assistant's machine should have.
const instructionsEvery = time.Minute

// instructionsLoop keeps the CLAUDE.md of the assistant and of the health space on the assistant's machine the ones
// this hub renders: right after it starts (a new version with new prompts) and whenever what they say changes (notes,
// machines, plugins). They used to be written only when a conversation started, so the main conversation, which goes
// on for days, kept the prompts of the hub that started it (#3235: #3068's ring cards never reached it). The node
// resumes a conversation in a fresh process on its next idle message once its directory's CLAUDE.md changed
// (node assistantProc.instructionsStale): the next message reads the new one.
func (s *Service) instructionsLoop(ctx context.Context) {
	t := time.NewTicker(instructionsEvery)
	defer t.Stop()
	for {
		s.refreshInstructions(ctx)
		s.spaceRecordingsSweep(ctx) // recording_days for the recordings saved into tracks (space_recordings.go)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// refreshInstructions writes the assistant's and the health space's CLAUDE.md on the assistant's machine when they
// differ from the ones this hub last wrote there (none yet after a start: both are written), and their plugin skills
// when those changed (prepareSpaceIf).
func (s *Service) refreshInstructions(ctx context.Context) {
	if s.hello(ctx).Assistant.Standby {
		return // a mesh member: the center's assistant is the one in use (docs/mesh.md)
	}
	machine := s.settings(ctx).MachineID
	if machine == "" || s.isWorkMachine(machine) {
		return
	}
	d, ok := s.device(ctx, machine)
	if !ok || !d.Online || d.Home == "" {
		return
	}
	spaces := []string{"", SpaceHealth}
	if s.trackSpaceNode(machine) {
		spaces = append(spaces, s.spaceTracks(ctx)...) // a node that keeps no track directory gets none (prepareSpaceIf)
	}
	for _, space := range spaces {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		_, wrote, err := s.prepareSpaceIf(cctx, machine, space, true)
		cancel()
		switch {
		case err != nil:
			s.logf("assistant: refresh instructions (space %q): %v", space, err)
		case wrote:
			s.logf("assistant: wrote the CLAUDE.md of space %q on %s", space, machine)
		}
	}
}

// startOn starts a new conversation on machine: the directory prepared, the model configuration of p.Purpose ("" is
// the main chat's) applied, then the node's assistant.send (headless: no herdr tab, streamed tokens, any launcher per message).
func (s *Service) startOn(ctx context.Context, machine string, p StartParams, main bool) (proto.Session, error) {
	if proto.IsExternalLauncher(p.Launcher) {
		scope, ok := ctx.Value(externalPickKey{}).(externalPickScope)
		if !main || p.GuestTools != nil || p.Space != "" || p.Purpose != "" || !ok ||
			p.Launcher != scope.pick.Launcher || p.Model != scope.pick.Model || p.NewSessionID != scope.execution.SessionID ||
			!s.externalMainStartScope(ctx, machine, p.NewSessionID, scope.pick) {
			return proto.Session{}, refusePick(http.StatusBadRequest, i18n.Err("external-adapter.scope_required"))
		}
	}
	if p.RequireLauncher {
		if err := s.modelPickRefusal(ctx, machine, p.Launcher, s.lang(ctx), true); err != nil {
			return proto.Session{}, err
		}
	}
	if !s.trackKnown(ctx, p.Space) {
		return proto.Session{}, badRequest(fmt.Errorf("space %q: not a registered space", p.Space))
	}
	explicitPermission := strings.TrimSpace(p.PermissionMode)
	if p.Origin == OriginInbox {
		// A caller cannot elevate externally originated work. Only the AI twin's own saved override, applied after
		// model resolution below, can replace its safe default. Default keeps the explicit Persona MCP allowlist
		// effective: Plan rejects MCP before consulting it. The manager/node origin policy, not the launcher's mode,
		// prevents externally visible work.
		p.PermissionMode = ""
	} else {
		p.PermissionMode = explicitPermission
	}
	var cwd string
	var err error
	if p.Space != "" {
		// The space owns its directory, instructions and the node's outbound gate classification.
		// Even a task's already-resolved cwd cannot replace the space's preparation.
		cwd, err = s.prepareSpace(ctx, machine, p.Space)
	} else if p.Cwd != "" {
		cwd = p.Cwd
	} else if p.GuestTools != nil {
		cwd, err = s.guestPrepare(ctx, machine) // its own directory: none of the assistant's instructions, memory or skills
	} else {
		cwd, err = s.prepareSpace(ctx, machine, p.Space)
	}
	if err != nil {
		return proto.Session{}, err
	}
	params := map[string]any{"cwd": cwd, "launcher": p.Launcher, "text": p.Text, "images": p.Images, "permission_mode": p.PermissionMode,
		"effort": p.Effort, "is_main": main}
	params["model_source"] = s.startModelSource(ctx, p, main)
	if p.TaskExecution {
		params["task_execution"] = true
		if p.LegacyTaskExecution {
			params[devicewire.LegacyTaskExecution] = true
		}
	}
	if p.Public {
		params["public"] = true
	}
	if p.Origin != "" {
		params["origin"] = p.Origin // the node enforces the origin from the process's first tool call, before start answers
	}
	if p.Model != "" {
		params["model"] = p.Model // picked with the first message, as with a message (sendModel): a typed id too
	}
	if p.ServiceTier != "" {
		params["service_tier"] = p.ServiceTier // acknowledged on a new native thread before its first turn
	}
	if p.ConfigDir != "" {
		params["config_dir"] = p.ConfigDir
	}
	if p.GuestTools != nil {
		// The node starts it with no built-in tools and only these of Persona's (node/assistant_guest.go).
		params["guest"] = map[string]any{"tools": p.GuestTools}
	}
	if p.NewSessionID != "" {
		params["new_session_id"] = p.NewSessionID // node/assistant_guest.go: under this id, once
	}
	// Its first message's (node/node.go assistant.send).
	if p.ClientMsgID != "" {
		params["client_msg_id"] = p.ClientMsgID
	}
	purpose := p.Purpose
	if purpose == "" {
		purpose = "assistant"
		if main {
			purpose = purposeMain
		}
	}
	if purpose == purposeAgent && p.Origin == OriginInbox {
		ctx = context.WithValue(ctx, inboxAssistantFitKey{}, true)
	}
	if p.Launcher != "" && !p.RequireLauncher {
		fit, refusal := s.mainChatFit(ctx, machine, p.Launcher)
		if fit == mainChatRefused {
			return proto.Session{}, refusal
		}
		// A missing launcher stays in params: finishMainChatModel owns its bounded rediscovery and explicitly configured
		// substitutes. Dropping it here would silently turn the start into an ordinary default-launcher start.
	}
	if p.TaskModel != nil && p.TaskExecution {
		params["launch"] = "headless"
		if err := s.finishModel(ctx, purpose, params, *p.TaskModel); err != nil {
			return proto.Session{}, err
		}
	} else if err := s.applyModel(ctx, purpose, params); err != nil {
		return proto.Session{}, err
	}
	if params["launch"] != "headless" {
		return proto.Session{}, badRequest(fmt.Errorf("purpose %q requires headless launch for assistant.send", purpose))
	}
	if !p.TaskExecution && !p.RequireLauncher {
		if _, err := s.finishMainChatModel(ctx, machine, purpose, params, false); err != nil {
			return proto.Session{}, err
		}
	}
	// A guest's profile is its own: the main chat's setting is for the user's conversations only. So is the Inbox AI
	// twin's: it acts on what strangers sent, and the user's own setup (allow rules, hooks, MCP servers) stays out of it
	// in Persona's profile (#10859).
	fromSetting := p.GuestTools == nil && purpose != purposeAgent && s.withNewConversationProfile(ctx, machine, params)
	used, _ := params["launcher"].(string)
	// Settle permission_mode after model resolution so Model Configuration's permission_mode applies before falling
	// back to the assistant default / bypassPermissions, while preserving Inbox and guest safety boundaries.
	switch {
	case p.Origin == OriginInbox:
		mode, modeErr := s.inboxPermissionMode(ctx, machine, used)
		if modeErr != nil {
			return proto.Session{}, modeErr
		}
		params["permission_mode"] = mode
	case p.GuestTools != nil:
		params["permission_mode"] = ""
	default:
		mode, _ := params["permission_mode"].(string)
		if mode == "" {
			mode = cmp.Or(s.settings(ctx).DefaultPermissionMode, proto.ModeBypass)
			params["permission_mode"] = mode
		}
		if explicitPermission != "" {
			params[assistantwire.PermissionModeExplicitParam] = true
		} else if !s.startPermissionModeSupported(ctx, machine, used, mode) {
			delete(params, "permission_mode")
		} else {
			params[assistantwire.DefaultPermissionModeParam] = mode
		}
	}
	// A profile is Claude Code's: the node refuses a config_dir for any other agent before starting it (#6467). Judged
	// here, by the launcher the start finally runs on, not the one asked for: the model configuration and a fallback
	// can both change it. One hidden from the pickers still runs what it runs (withUnhidden, as finishMainChatModel
	// reads it); one whose agent is not known here keeps the profile, which the node refuses for another agent before
	// starting anything, and the start goes again without it: startSignedIn's, and the main chat's below (#6884).
	if _, asked := params["config_dir"]; asked && used != "" && used != "claude" {
		if agent := s.launcherAgent(withUnhidden(ctx, used), machine, used); agent != "" && agent != "claude" {
			delete(params, "config_dir")
		}
	}
	if model, _ := params["model"].(string); p.Model != "" && model == p.Model {
		// A launcher that sets its own --model, or whose agent takes none, would not run it (model_pick.go).
		if err := s.pickedModelRefusal(ctx, machine, used, model); err != nil {
			return proto.Session{}, refusePick(http.StatusBadRequest, err)
		}
	}
	if p.TaskExecution {
		err = s.validateTaskSelections(ctx, machine, "", params, p.PromptSelections)
	} else {
		err = s.validateSkillSelections(ctx, machine, "", params)
	}
	if err != nil {
		return proto.Session{}, err
	}
	var res struct {
		SessionID      string `json:"session_id"`
		PermissionMode string `json:"permission_mode"`
	}
	if p.GuestTools == nil {
		s.putCacheKeepalive(ctx, machine, "", main, params)
	}
	if main && p.Space == "" && p.GuestTools == nil {
		ctx = context.WithValue(ctx, supervisionStartup{}, supervisionrole.RoleMain())
	}
	err = s.call(ctx, machine, DeviceAssistantSend, params, &res)
	if fromSetting && (nodeOutdated(err) || profileRefused(err)) {
		// A node too old to be told a profile starts it where it always has; a launcher running another agent than
		// Claude Code, which alone has profiles, starts without one.
		delete(params, "config_dir")
		err = s.call(ctx, machine, DeviceAssistantSend, params, &res)
	}
	s.startRefused(ctx, machine, used, err) // Claude Code or the launcher missing: the readiness's claude step
	if err != nil {
		if d, ok := s.device(ctx, machine); ok {
			err = apierr.OnMachine(err, d.Name) // claude_missing on a sample user's MacBook (docs/assistant/onboarding.md 4.6)
		}
		return proto.Session{}, err
	}
	requestedMode, _ := params["permission_mode"].(string)
	finalMode := cmp.Or(res.PermissionMode, requestedMode)
	sess := proto.Session{SessionID: res.SessionID, Cwd: cwd, Status: proto.StatusWorking, Launcher: used, PermissionMode: finalMode,
		Effort: p.Effort, StartedByPersona: true, UpdatedAt: time.Now().UTC()}
	sess.MachineID, sess.Assistant, sess.State, sess.Space = machine, true, proto.StateWorking, p.Space
	if strings.TrimSpace(p.Text) == "" && len(p.Images) == 0 {
		sess.Status, sess.State = proto.StatusIdle, proto.StateIdle
	}
	if p.Human && (p.Origin == "" || p.Origin == OriginUser) && strings.TrimSpace(p.Text) != "" {
		s.humanSent.noteForReply(sess.SessionID, firstReplySendID, p.Text)
	}
	sess.Effort, _ = params["effort"].(string)
	sess.Model, _ = params["model"].(string) // what it was started with ("": the launcher's default)
	if startup := ctx.Value(supervisionStartup{}); startup != nil && sess.SessionID != "" {
		if role, ok := startup.(supervisionrole.Role); ok {
			// The node has accepted both the compact policy and this id. Record that exact baseline before any follow-up
			// can find the new process working; a pre-id record could not identify the process it belonged to.
			if err := s.recordAcceptedSupervisionRole(context.WithoutCancel(ctx), machine, sess.SessionID, role); err != nil {
				s.logf("record accepted assistant supervision role: %v", err)
			}
		}
	}
	return sess, nil
}

// inboxPermissionMode reads only the AI twin's own record. Plan, unknown values and modes the resolved launcher cannot
// run return to default: Plan rejects the Persona MCP calls that investigation and inbox_suggest require before their
// allowlist runs, and an unknown value must be normalized before the node's generic mode validation.
func (s *Service) inboxPermissionMode(ctx context.Context, machine, used string) (string, error) {
	record, err := s.modelRecord(ctx, purposeAgent)
	if err != nil {
		return "", i18n.Err("assistant-models.err_read_config", i18n.Args{"error": err.Error()})
	}
	mode := cmp.Or(record.Binding.PermissionMode, proto.ModeDefault)
	if mode == proto.ModePlan || !slices.Contains(proto.PermissionModes, mode) ||
		!s.startPurposePermissionModeSupported(ctx, machine, used, purposeAgent, mode) {
		mode = proto.ModeDefault
	}
	return mode, nil
}

// putInboxPermissionMode replaces any caller-supplied value with the manager's own AI-twin decision. The separate wire
// field lets a new node distinguish this choice from an older manager's caller-controlled permission_mode.
func (s *Service) putInboxPermissionMode(ctx context.Context, machine, session string, params map[string]any, origin string, originKnown bool) error {
	delete(params, assistantwire.InboxPermissionModeParam)
	if !originKnown || origin != OriginInbox {
		return nil
	}
	used, _ := params["launcher"].(string)
	if used == "" {
		if c, ok := s.session(ctx, machine, session); ok {
			used = c.Launcher
		}
	}
	mode, err := s.inboxPermissionMode(ctx, machine, used)
	if err != nil {
		return err
	}
	params[assistantwire.InboxPermissionModeParam] = mode
	return nil
}

// startPermissionModeSupported checks an inherited or saved mode against the launcher the start finally runs on.
// Unknown launchers keep the existing behavior: the node validates the start rather than the manager guessing a type.
func (s *Service) startPermissionModeSupported(ctx context.Context, machine, used, mode string) bool {
	return s.startPurposePermissionModeSupported(ctx, machine, used, "", mode)
}

func (s *Service) startPurposePermissionModeSupported(ctx context.Context, machine, used, purpose, mode string) bool {
	if used == "" || used == "claude" {
		return true
	}
	if l, ok := s.targetMachineLauncher(ctx, machine, used); ok {
		return permissionModeSupportedForPurpose(purpose, cmp.Or(l.AgentType, l.Agent), l.PermissionModes, mode)
	}
	if agent := s.launcherAgent(withUnhidden(ctx, used), machine, used); agent != "" {
		return permissionModeSupportedForPurpose(purpose, agent, 0, mode)
	}
	return true
}

// start starts a conversation: on p.MachineID ("" the assistant's machine), or the main conversation.
func (s *Service) start(ctx context.Context, p StartParams) (proto.Session, error) {
	if p.Main && p.Space != "" {
		return proto.Session{}, badRequest(errors.New("a space has no main conversation"))
	}
	if p.GuestTools != nil && (p.Main || p.Space != "") {
		return proto.Session{}, badRequest(errors.New("a guest conversation is neither the main one nor in a space"))
	}
	if p.Main {
		return s.startMain(ctx, p)
	}
	return s.startConversationOn(ctx, p, false)
}

func (s *Service) startConversationOn(ctx context.Context, p StartParams, main bool) (sess proto.Session, err error) {
	machine := p.MachineID
	if machine == "" {
		machine = s.settings(ctx).MachineID
		if machine == "" {
			return proto.Session{}, s.missingAssistantMachine(ctx)
		}
	}
	if s.isWorkMachine(machine) {
		return proto.Session{}, errWorkAssistant
	}
	// MCP discovery and the first assistant.context can arrive before assistant.send returns its session ID.
	// Keep the manager's authorization and in-flight start space until origin and space are recorded.
	finishStart := s.beginAssistantStartSpace(machine, p.Space, p.NewSessionID)
	defer finishStart()
	guestID := ""
	if p.GuestTools != nil {
		if p.NewSessionID != "" {
			guestID = p.NewSessionID
		} else {
			guestID = newGuestSessionID()
			p.NewSessionID = guestID
		}
		// The conversation's MCP server can discover its tools before assistant.send returns. Register the guest's
		// restriction first, as roomCreate does, so that first discovery is the declared list rather than an empty one.
		if p.GuestHandoff != "" {
			err = s.guestSetHandoff(ctx, machine, guestID, p.GuestHandoff, p.GuestTools)
		} else if p.GuestGroup != "" {
			err = s.guestSetGroup(ctx, machine, guestID, p.GuestGroup, p.GuestTools)
		} else {
			err = s.guestSet(ctx, machine, guestID, p.GuestTools)
		}
		if err != nil {
			return proto.Session{}, err
		}
		defer func() {
			if sess.SessionID != guestID {
				if err := s.guestDrop(context.WithoutCancel(ctx), guestID); err != nil {
					s.logf("assistant: forget an unstarted guest conversation %s: %v", short(guestID), err)
				}
			}
		}()
	}
	if p.NewSessionID != "" {
		s.setConversationOriginDevice(p.NewSessionID, string(p.OriginDeviceID))
		defer func() {
			if sess.SessionID != p.NewSessionID {
				s.clearConversationOriginDevice(p.NewSessionID)
			}
		}()
	}
	sess, err = s.startOn(ctx, machine, p, main)
	if errors.Is(err, ErrNoDevice) && p.MachineID == "" {
		return proto.Session{}, s.missingAssistantMachine(ctx)
	}
	if err != nil {
		return sess, err
	}
	if guestID != "" && sess.SessionID != guestID {
		return sess, errors.New("the node did not start the guest under its registered session id")
	}
	sess.Origin, sess.OriginRef, sess.OpenedBy = firstNonEmpty(p.Origin, OriginUser), p.OriginRef, p.OpenedBy
	s.originSet(ctx, sess.MachineID, sess.SessionID, sess.Origin, sess.OriginRef, sess.OpenedBy)
	// Its space, for when no computer lists it (conversation_space.go).
	s.noteConversationSpaces(ctx, []db.ConversationSpace{{MachineID: sess.MachineID, SessionID: sess.SessionID, Space: p.Space}})
	s.setConversationOriginDevice(sess.SessionID, string(p.OriginDeviceID))
	return sess, nil
}

// startConversation starts a new conversation of the assistant's own (the background supervisor's, a scheduled task's,
// conversation_new's). Nobody is in front of it to sign in, so it starts signed in as a meeting's do (startSignedIn):
// in the profile the assistant's conversations had, not a new private one with no login in it (#6015).
func (s *Service) startConversation(ctx context.Context, p startParams) (proto.Session, error) {
	if p.modelSource.Role != "" {
		ctx = context.WithValue(ctx, modelSourceKey{}, p.modelSource.Safe())
	}
	return s.startSignedIn(ctx, StartParams{Text: prompts.MessageSourceText(p.Text, p.Source), PermissionMode: p.PermissionMode, Purpose: p.Purpose, Space: p.Space,
		Origin: p.Origin, OriginRef: p.OriginRef, MachineID: p.MachineID, Cwd: p.Cwd, TaskExecution: p.TaskExecution,
		LegacyTaskExecution: p.LegacyTaskExecution, PromptSelections: p.PromptSelections, TaskModel: p.TaskModel, NewSessionID: p.NewSessionID})
}

// Start is assistant.start: a conversation one of the hub's features starts (the inbox's AI twin), or the channels'
// (the lark module's message.go, channels_plugin.go). It is named after its first turn like the app's, unless it is untitled.
func (s *Service) Start(ctx context.Context, p StartParams) (proto.Session, error) {
	return s.startUserWith(ctx, p, s.start)
}

func (s *Service) startUserWith(ctx context.Context, p StartParams, start func(context.Context, StartParams) (proto.Session, error)) (proto.Session, error) {
	if p.Origin == "" || p.Origin == OriginUser {
		p.Text = prompts.ProtectUserMessage(p.Text)
	}
	// The first message gets no client_msg_id of the manager's own (#5200): the node would tag its line with it, and the
	// iPhone app, which matches its pending bubble by the id a line carries, would show the message twice. An Always
	// rule still needs a vouched client_msg_id. Missing-reply recovery can also use the vouched start in startOn.
	sess, err := start(ctx, p)
	if err != nil {
		return proto.Session{}, err
	}
	if !p.Untitled {
		s.titleNew(sess.MachineID, sess.SessionID, time.Now())
	}
	return sess, nil
}

// ---- the main conversation ----

// mainHandoffKey is the setting with what the old main conversation left, until the next one takes it.
const mainHandoffKey = "assistant.main_handoff"

// maxFormerMains is how many earlier main conversations the assistant's machine keeps listing however quiet.
const maxFormerMains = 10

// mainHandoff is what a replaced main conversation left for the next one (node assistant.handoff).
type mainHandoff struct {
	From      string    `json:"from"`
	MachineID string    `json:"machine_id"`
	Until     time.Time `json:"until"`
	// For: the new main conversation it waits in, started without a message (Android starts it empty, then sends).
	// A cleared external choice reserves this ID before its start, so retries cannot create another conversation.
	For          string `json:"for,omitempty"`
	FromLauncher string `json:"from_launcher,omitempty"`
	FromModel    string `json:"from_model,omitempty"`
	Summary      string `json:"summary,omitempty"`
	Partial      bool   `json:"partial,omitempty"`
	Recent       []struct {
		Role string `json:"role"`
		Text string `json:"text"`
	} `json:"recent,omitempty"`
	// Pick: the model pick of a main conversation cleared in place (clearMain), for the next one to start on and keep
	// (model_pick.go). A new main conversation (newMain) starts on the default, so its handoff has none.
	Pick *modelPick `json:"pick,omitempty"`
}

// mainBook holds the main conversation still while it changes, and the handoff with no database.
type mainBook struct {
	mu        sync.Mutex
	handoff   mainHandoff
	execution mainExecutionBook
}

func (s *Service) loadHandoff(ctx context.Context) mainHandoff {
	h, _ := s.readHandoff(ctx)
	return h
}

func (s *Service) readHandoff(ctx context.Context) (mainHandoff, error) {
	if st := s.store(); st != nil {
		var h mainHandoff
		if _, err := st.Setting(ctx, mainHandoffKey, &h); err != nil {
			return mainHandoff{}, err
		}
		// A previously prepared handoff can contain verbatim turns. Deleting its
		// source must not replay them into the next main conversation.
		if h.From != "" && s.conversationAvailable(ctx, h.MachineID, h.From) != nil {
			return mainHandoff{Pick: h.Pick}, nil
		}
		return h, nil
	}
	return s.main.handoff, nil
}

func (s *Service) saveHandoff(ctx context.Context, h mainHandoff) error {
	if st := s.store(); st != nil {
		return st.PutSetting(ctx, mainHandoffKey, h)
	}
	s.main.handoff = h
	return nil
}

// startMain starts a new main conversation with the user's first message: what the old one left goes before it
// (prompts/assistant/main_handoff), once. Started without a message, it waits for the first one sent to it.
func (s *Service) startMain(ctx context.Context, p StartParams) (proto.Session, error) {
	s.main.mu.Lock()
	defer s.main.mu.Unlock()
	handoff, err := s.readHandoff(ctx)
	if err != nil {
		return proto.Session{}, err // an unreadable carried choice is not permission to fall back
	}
	empty := strings.TrimSpace(p.Text) == "" && len(p.Images) == 0
	if handoff.From != "" && !empty {
		cfg := s.effective(ctx, s.settings(ctx))
		launcher := cmp.Or(p.Launcher, cfg.Launcher)
		p.Text = s.renderHandoff(ctx, handoff, p.Text, s.launcherAgent(ctx, cfg.MachineID, launcher))
	}
	p.MachineID, p.Purpose, p.Main = "", "", false
	p.Origin, p.OriginRef, p.OpenedBy = OriginUser, "", "" // the main conversation is the user's, whatever channel opened it
	// A cleared main conversation's pick, unless this start names its own launcher or model.
	carried := handoff.Pick
	if carried != nil && (p.Launcher != "" || p.Model != "") {
		carried = nil
	}
	if carried != nil && p.ServiceTier != "" && p.Launcher == "" && p.Model == "" {
		// An unnamed tier can belong only to the client's displayed default, not to this carried setup it did not show.
		p.ServiceTier = ""
	}
	externalCarried := carried != nil && proto.IsExternalLauncher(carried.Launcher)
	if carried != nil {
		if externalCarried {
			ctx, p, handoff, err = s.prepareExternalMain(ctx, p, handoff)
			if err != nil {
				return proto.Session{}, refusePick(http.StatusBadRequest, err)
			}
		} else if err := s.modelPickRefusal(ctx, s.settings(ctx).MachineID, carried.Launcher, s.requestCtxLang(ctx), true); err != nil {
			// Ordinary carried picks keep the existing one-refusal behavior. An external choice is retained for retry.
			s.dropCarriedPick(ctx, handoff)
			if !storedPickRefused(err) {
				err = refusePick(http.StatusBadRequest, err) // it can't run assistant chats
			}
			return proto.Session{}, err
		}
		// The start's own effort (iOS sends its last one with every start) is the first message's; the pick keeps its.
		p.Launcher, p.Model = carried.Launcher, carried.Model
		if p.Effort == "" {
			p.Effort = carried.Effort
		}
	}
	sess, err := s.startConversationOn(ctx, p, true)
	if err != nil {
		var refused *pickError
		if carried != nil && !externalCarried && errors.As(err, &refused) {
			s.dropCarriedPick(ctx, handoff) // its model can't reach that launcher's agent (pickedModelRefusal)
		}
		return proto.Session{}, err
	}
	if externalCarried {
		kept, persisted := s.modelPickOf(ctx, sess.MachineID, sess.SessionID)
		if sess.SessionID != p.NewSessionID || !persisted || kept != *carried ||
			!s.externalMainStartScope(ctx, sess.MachineID, sess.SessionID, *carried) {
			return proto.Session{}, refusePick(http.StatusBadRequest, i18n.Err("external-adapter.scope_required"))
		}
		sess.ModelPick = carried.wire() // persisted before the node was called
	} else if carried != nil {
		s.keepStartPick(ctx, &sess, carried.Launcher, carried.Model, carried.Effort, true)
	}
	if _, err := s.updateSettings(ctx, func(v *Settings) error {
		*v = s.effective(ctx, *v)
		if externalCarried && v.Main != "" && v.Main != sess.SessionID {
			return i18n.Err("external-adapter.scope_required")
		}
		v.Main, v.MainMachineID = sess.SessionID, sess.MachineID
		return nil
	}); err != nil {
		if externalCarried {
			return proto.Session{}, err // retain the stable reservation until the route can be saved
		}
		s.logf("assistant: save main: %v", err)
	}
	if handoff.From != "" || handoff.Pick != nil {
		next := mainHandoff{} // used
		if empty && handoff.From != "" {
			next, next.For, next.Pick = handoff, sess.SessionID, nil
		}
		if err := s.saveHandoff(ctx, next); err != nil {
			s.logf("assistant: what the old main conversation left: %v", err)
		}
	}
	s.settingsChanged(ctx)
	if handoff.From != "" {
		s.handOverMainTasks(context.WithoutCancel(ctx), handoff.From, sess.SessionID)  // its open tasks follow (main_handover.go)
		s.handOverMainAlways(context.WithoutCancel(ctx), handoff.From, sess.SessionID) // its Always-allow grants follow once (#16158)
	} else {
		s.handOverFormerMainTasks(context.WithoutCancel(ctx)) // e.g. after /clear, which puts the old main in Former with no handoff.From
		if cfg := s.settings(ctx); len(cfg.Former) > 0 && cfg.Former[0].SessionID != "" && cfg.Former[0].SessionID != sess.SessionID {
			s.handOverMainAlways(context.WithoutCancel(ctx), cfg.Former[0].SessionID, sess.SessionID)
		}
	}
	return sess, nil
}

// dropCarriedPick saves h, what a main conversation left, without the pick a /clear carried (clearMain): it can't run,
// or New main chat starts on the default. The caller holds s.main.mu.
func (s *Service) dropCarriedPick(ctx context.Context, h mainHandoff) {
	h.Pick = nil
	if err := s.saveHandoff(ctx, h); err != nil {
		s.logf("assistant: drop the cleared main conversation's model pick: %v", err)
	}
}

// newMain puts the main conversation away: what it left is kept for the next one (carry), it is no longer the main
// one, renamed and archived. The next message to the main conversation starts a new one (startMain). It answers the
// old conversation's id ("" when there was none) and whether anything of it is carried over.
func (s *Service) newMain(ctx context.Context, carry bool, expect string) (string, bool, error) {
	s.main.mu.Lock()
	defer s.main.mu.Unlock()
	cfg := s.settings(ctx)
	old := cfg.Main
	if expect != "" && expect != old {
		return "", false, &rpc.Error{Code: "main_changed", Message: i18n.T(s.lang(ctx), "assistant-start.main_changed")}
	}
	if old == "" {
		if h := s.loadHandoff(ctx); h.Pick != nil { // after /clear: the next one starts fresh, on the default
			s.dropCarriedPick(ctx, h)
		}
		return "", false, nil
	}
	machine, c, found := s.findSession(ctx, old, cfg.mainRouteMachine())
	if found && (c.Status == proto.StatusWorking || (c.Compaction != nil && c.Compaction.State == proto.CompactionRunning)) {
		return "", false, &rpc.Error{Code: "busy", Message: i18n.T(s.lang(ctx), "assistant-start.main_busy")}
	}
	if !found {
		machine = cfg.mainRouteMachine()
	}
	now := time.Now().UTC()
	handoff := mainHandoff{From: old, MachineID: machine, Until: now}
	carried := false
	if carry && (found || s.historyKnown(ctx, machine, old)) {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err := s.call(cctx, machine, DeviceAssistantHandoff, map[string]any{"session_id": old}, &handoff)
		cancel()
		handoff.From, handoff.MachineID, handoff.Until = old, machine, now
		if err != nil {
			s.logf("assistant: what the main conversation left: %v", err)
		}
		carried = handoff.Summary != "" || len(handoff.Recent) > 0
	}
	if err := s.saveHandoff(ctx, handoff); err != nil {
		return "", false, err
	}
	if err := s.putAwayMain(ctx, machine, old, now); err != nil {
		return "", false, err
	}
	s.logf("assistant: main conversation %s put away (carried over: %v)", short(old), carried)
	return old, carried, nil
}

// clearMain clears the main conversation's context in place (the apps' "Clear context" and /clear, as Cindy's /clear
// does): it is put away as newMain puts it, with no summary carried over, then its process is closed whether it works or
// not (a turn stops, and nothing it ran in the background starts another turn there) and a question it waits on is
// taken back. What an earlier newMain left for the next conversation is dropped too, so the next message to the main
// conversation starts a new one with a fresh context (startMain): no --resume, no handoff. Its model pick goes with it
// (mainHandoff.Pick): the context is cleared, not the model the user picked. Nothing is deleted, and the work it handed
// out (tasks, their Agents) goes on. It answers the old conversation's id ("" when there was none).
func (s *Service) clearMain(ctx context.Context, expect string) (string, error) {
	// past the checks it goes through as one change, even when the app stops waiting for the answer
	ctx = context.WithoutCancel(ctx)
	old, machine, c, found, err := s.clearMainLocked(ctx, expect)
	if err != nil || old == "" {
		return old, err
	}
	// after the lock: the node may be slow, and every message to the main conversation waits on that lock
	if found {
		s.closeMain(ctx, machine, c)
	}
	s.logf("assistant: main conversation %s cleared", short(old))
	return old, nil
}

// clearMainLocked is clearMain's change under s.main.mu: the check, the handoff dropped and the conversation put away.
func (s *Service) clearMainLocked(ctx context.Context, expect string) (old, machine string, c proto.Session, found bool, err error) {
	s.main.mu.Lock()
	defer s.main.mu.Unlock()
	cfg := s.settings(ctx)
	old = cfg.Main
	if expect != "" && expect != old {
		return "", "", c, false, &rpc.Error{Code: "main_changed", Message: i18n.T(s.lang(ctx), "assistant-start.main_changed")}
	}
	cleared := mainHandoff{}
	if old == "" {
		h, readErr := s.readHandoff(ctx)
		if readErr != nil {
			return "", "", c, false, readErr
		}
		cleared.Pick = h.Pick // cleared again before a new one started: the pick still waits for it
		if h.Pick != nil && proto.IsExternalLauncher(h.Pick.Launcher) {
			cleared.For = h.For // a failed external start keeps its manager-reserved ID
		}
	} else if p, ok := s.modelPickOf(ctx, cfg.mainRouteMachine(), old); ok {
		cleared.Pick = &p // the context is cleared, not the model the user picked for the main conversation
	}
	if err := s.saveHandoff(ctx, cleared); err != nil {
		return "", "", c, false, err
	}
	if old == "" {
		return "", "", c, false, nil
	}
	machine, c, found = s.findSession(ctx, old, cfg.mainRouteMachine())
	if !found {
		machine = cfg.mainRouteMachine()
	}
	if err := s.putAwayMain(ctx, machine, old, time.Now().UTC()); err != nil {
		return "", "", c, false, err
	}
	return old, machine, c, found, nil
}

// closeMain ends the main conversation's process once clearMain has put it away: its turn stops as with the Stop
// button, and a question it waits on is taken back. Only Persona's own headless process is closed: one the user resumed
// in a terminal (a pane) is theirs, and the node's session.close would close that terminal. Unlike the Stop button it
// does not stop a Feishu request the conversation reports on (UserStopped): that would interrupt the Agents working on
// the tasks it handed out, which a clear leaves running. A failure is only logged: an offline machine runs no turn.
func (s *Service) closeMain(ctx context.Context, machine string, c proto.Session) {
	s.withdrawAskUser(c.SessionID)
	if c.PaneID != "" {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := s.call(cctx, machine, "session.close", map[string]any{"session_id": c.SessionID}, nil); err != nil {
		s.logf("assistant: close the main conversation %s: %v", short(c.SessionID), err)
	}
}

// putAwayMain makes old no longer the main conversation: first among the former ones (its machine keeps listing it),
// renamed "旧主对话 · 至 M月D日" by the user's hand (autonaming leaves it) and archived; sending to it again brings it back
// (unarchiveOnSend). The caller holds s.main.mu.
func (s *Service) putAwayMain(ctx context.Context, machine, old string, now time.Time) error {
	if _, err := s.updateSettings(ctx, func(v *Settings) error {
		*v = s.effective(ctx, *v)
		v.Main, v.MainMachineID = "", ""
		v.Former = slices.DeleteFunc(v.Former, func(f FormerMain) bool { return f.SessionID == old })
		v.Former = append([]FormerMain{{MachineID: machine, SessionID: old, Until: now}}, v.Former...)
		if len(v.Former) > maxFormerMains {
			v.Former = v.Former[:maxFormerMains]
		}
		return nil
	}); err != nil {
		return err
	}
	local := now.In(s.location(ctx))
	s.titleSet(machine, old, fmt.Sprintf("旧主对话 · 至 %d月%d日", local.Month(), local.Day()), titleByUser)
	if _, err := s.setArchived(ctx, machine, old, true, titleByUser); err != nil {
		s.logf("assistant: archive the old main conversation %s: %v", short(old), err)
	}
	s.settingsChanged(ctx)
	return nil
}

// findSession is the session id on the machine preferred, else on any machine (false when none lists it).
func (s *Service) findSession(ctx context.Context, id, preferred string) (string, proto.Session, bool) {
	machines := []string{preferred}
	for _, d := range s.devices(ctx) {
		if d.ID != preferred {
			machines = append(machines, d.ID)
		}
	}
	for _, m := range machines {
		if c, ok := s.session(ctx, m, id); ok {
			return m, c, true
		}
	}
	return "", proto.Session{}, false
}

// handoffFor is the text of a message to conversation sid: with what the old main conversation left before it when
// sid is the new main one that was started empty and this is the first message to it.
func (s *Service) handoffFor(ctx context.Context, sid, text string) string {
	return s.handoffForAgent(ctx, sid, "", text)
}

func (s *Service) handoffForAgent(ctx context.Context, sid, agent, text string) string {
	if sid == "" {
		return text
	}
	s.main.mu.Lock()
	defer s.main.mu.Unlock()
	handoff := s.loadHandoff(ctx)
	if handoff.From == "" || handoff.For != sid {
		return text
	}
	if err := s.saveHandoff(ctx, mainHandoff{}); err != nil {
		s.logf("assistant: clear what the old main conversation left: %v", err)
		return text
	}
	return s.renderHandoff(ctx, handoff, text, agent)
}

// renderHandoff puts what the old main conversation left before text (prompts/assistant/main_handoff).
func (s *Service) renderHandoff(ctx context.Context, handoff mainHandoff, text, agent string) string {
	hp := prompts.MainHandoff{FromID: handoff.From, Summary: handoff.Summary, Text: text}
	local := handoff.Until.In(s.location(ctx))
	hp.Until = fmt.Sprintf("%d月%d日 %02d:%02d", local.Month(), local.Day(), local.Hour(), local.Minute())
	for _, t := range handoff.Recent {
		hp.Recent = append(hp.Recent, prompts.HandoffTurn{Role: t.Role, Text: t.Text})
	}
	if agent == agentprompt.Antigravity {
		if ordered, ok := agentprompt.WorkflowCommand(text); ok {
			hp.Text = ""
			context := strings.TrimSpace(prompts.RenderMainHandoff(hp))
			return agentprompt.WithContext(agent, ordered, context)
		}
	}
	return prompts.RenderMainHandoff(hp)
}

// ---- the routes ----

// requestOrigin is where a conversation POST /api/v1/assistant/conversations opens comes from: an extension's
// (a Feishu /agent when it runs on lark.agent's model), else the user's in an app.
func requestOrigin(r *http.Request, purpose string) (origin, ref string) {
	if r.Header.Get(RoleHeader) != roleExtension {
		return OriginUser, ""
	}
	ext := r.Header.Get(ExtensionHeader)
	if purpose == purposeLarkAgent {
		return OriginLarkAgent, ext
	}
	return OriginExtension, ext
}

// requestOpener is the extension whose token asks POST /api/v1/assistant/conversations to open one (the hub's
// headers: a client's own are dropped on the way), "" for anyone else.
func requestOpener(r *http.Request) string {
	if r.Header.Get(RoleHeader) != roleExtension {
		return ""
	}
	return r.Header.Get(ExtensionHeader)
}

func (s *Service) startRoutes() {
	s.handle("POST /api/v1/assistant/conversations", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Prompt         string        `json:"prompt"`
			Images         []proto.Image `json:"images"`
			Launcher       string        `json:"launcher"`
			Model          string        `json:"model"` // the first message's model (#5033): a model id, typed or listed
			PermissionMode string        `json:"permission_mode"`
			Effort         string        `json:"effort"`
			ServiceTier    string        `json:"service_tier"` // native Codex speed for the first turn, independent of effort
			Main           bool          `json:"main"`         // becomes the main conversation
			// Purpose is the model purpose it runs under ("assistant" when empty; a plugin's /agent asks for its own);
			// MachineID one of the hub's machines instead of the assistant's (docs/protocol/protocol.md "REST (App → hub)").
			Purpose   string `json:"purpose"`
			MachineID string `json:"machine_id"`
			Space     string `json:"space"` // a registered space, e.g. "health" (docs/assistant/health.md)
			// ReportTasks: the tasks it opens are told back to it, as the main conversation's quiet ones are (#3192,
			// reporters.go); a plugin's /agent hands their results on.
			ReportTasks bool `json:"report_tasks"`
			// Guest: a guest conversation (guest.go): only these connector tools, nothing of the assistant's.
			Guest *GuestSpec `json:"guest"`
			// Via: how the user's app sent its first message when not typed in the app itself: "intent" (Siri,
			// Shortcuts, an App Intent, which may run on a locked phone), "share" (the share sheet) or "card" (a card's
			// button that wrote it), or "handoff" (a generated context transfer). Such a message never counts as the
			// user's own words (secret_handoff.go
			// handoffUserTurn, the decision on #3962).
			Via string `json:"via"`
		}
		if err := decodeLargeBody(w, r, &in, proto.MessageBodyLimit); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		// A conversation not started has no agent yet: the limits every session gets unless its agent has its own.
		if apierr.ImagesOverLimit(w, proto.HubImageLimits.For(""), len(in.Images)) {
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 110*time.Second)
		defer cancel()
		if in.Purpose != "" {
			if _, err := s.resolveModel(ctx, in.Purpose); err != nil {
				apierr.Write(w, http.StatusBadRequest, fmt.Errorf("purpose %q: %w", in.Purpose, err))
				return
			}
		}
		if !s.trackKnown(ctx, in.Space) {
			apierr.Write(w, http.StatusBadRequest, fmt.Errorf("space %q: not a registered space", in.Space))
			return
		}
		if in.Space != "" && s.trackPolicy(ctx, in.Space).HideFromExtensions && r.Header.Get(RoleHeader) == roleExtension { // a start is a control: trust and grants widen reads only
			apierr.WriteCode(w, http.StatusForbidden, "permission_denied", i18n.Err("apierr-apierr.health_start_forbidden"))
			return
		}
		if in.Model = strings.TrimSpace(in.Model); checkModelID(in.Model) != nil {
			apierr.Write(w, http.StatusBadRequest, checkModelID(in.Model))
			return
		}
		if in.Space != "" && in.Main {
			apierr.Write(w, http.StatusBadRequest, errors.New("a space has no main conversation"))
			return
		}
		if in.MachineID != "" {
			if _, ok := s.device(ctx, in.MachineID); !ok {
				apierr.Write(w, http.StatusBadRequest, fmt.Errorf("no machine %q", in.MachineID))
				return
			}
		}
		if in.Guest != nil && (in.Main || in.Space != "") {
			apierr.Write(w, http.StatusBadRequest, errors.New("a guest conversation is neither the main one nor in a space"))
			return
		}
		if err := s.checkGuest(ctx, in.Guest); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		var guestTools []string
		if in.Guest != nil {
			guestTools, in.ReportTasks = in.Guest.Tools, false // it hands nothing out
		}
		origin, ref := requestOrigin(r, in.Purpose)
		// As the owner wrote it, before workspace mentions are filled in (project_review.go).
		ownerText := in.Prompt
		own := false // only the user's words in it, as filled in (workspace_mention.go fillWorkspaces)
		if in.Guest == nil && in.Via != "handoff" {
			in.Prompt, own = s.fillWorkspaces(ctx, in.Prompt) // as SendParams does for the next messages
		}
		originDevice := ""
		if in.Guest == nil && r.Header.Get(RoleHeader) != roleExtension {
			originDevice = sanitizeOriginDeviceID(r.Header.Get(assistantwire.DeviceIDHeader))
		}
		// The main conversation is the assistant's, on its machine.
		ctx, note := withLauncherNote(ctx)
		sess, err := s.Start(ctx, StartParams{Text: in.Prompt, Images: in.Images, Launcher: in.Launcher, Model: in.Model, PermissionMode: in.PermissionMode,
			Effort: in.Effort, ServiceTier: in.ServiceTier, Purpose: in.Purpose, MachineID: in.MachineID, Main: in.Main, Space: in.Space, Origin: origin, OriginRef: ref,
			OpenedBy: requestOpener(r), GuestTools: guestTools,
			Human:          own && in.Via == "" && (assistantwire.HumanStartOf(r.Context()) || assistantwire.ReplyInputOf(r.Context())),
			OriginDeviceID: wireids.DeviceID(originDevice)})
		var refused *pickError
		switch {
		case errors.As(err, &refused):
			apierr.Write(w, refused.status, refused.err)
			return
		case err != nil:
			apierr.Call(w, err)
			return
		}
		if in.Guest == nil && in.Space == "" && own && in.Via == "" &&
			assistantwire.HumanStartOf(r.Context()) && (strings.TrimSpace(ownerText) != "" || len(in.Images) > 0) {
			ref := SessionRef{MachineID: sess.MachineID, SessionID: sess.SessionID}
			bound, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			err := s.projectReviewOwnerInput(bound, assistantwire.ProjectReviewOwnerInput{
				SessionRef: ref, MessageID: assistantwire.ProjectReviewMessageID("first-assistant:" + sess.SessionID), At: sess.UpdatedAt, Unscoped: true,
			})
			cancel()
			if err != nil {
				s.logf("project review: record first assistant input: %v", err)
			}
		}
		if in.Purpose == "" && in.Guest == nil && in.Main {
			// Only the main conversation keeps its start as a per-conversation pick (#6911).
			s.keepStartPick(ctx, &sess, in.Launcher, in.Model, in.Effort, in.Main)
		}
		if in.ReportTasks && !in.Main {
			s.markReporter(ctx, sess.MachineID, sess.SessionID)
		}
		if *note != "" {
			apierr.WriteJSON(w, map[string]any{"session": sess, "launcher_note": *note})
			return
		}
		apierr.WriteJSON(w, map[string]any{"session": sess})
	})
	// What this hub's assistant can do that an extension asks before relying on it: an older hub answers 404 and
	// would ignore what it does not know (a guest conversation started as an ordinary one).
	s.handle("GET /api/v1/assistant/capabilities", func(w http.ResponseWriter, r *http.Request) {
		apierr.WriteJSON(w, map[string]any{"guest_conversations": true})
	})
	s.handle("POST /api/v1/assistant/conversations/{id}/compact", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			MachineID string `json:"machine_id"` // "" is the assistant's machine
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if in.MachineID == "" {
			in.MachineID = s.settings(r.Context()).MachineID
		}
		if _, ok := s.device(r.Context(), in.MachineID); !ok {
			apierr.Write(w, http.StatusBadRequest, fmt.Errorf("no machine %q", in.MachineID))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Minute) // a process may have to start first
		defer cancel()
		if err := s.call(ctx, in.MachineID, DeviceAssistantCompact, map[string]any{"session_id": r.PathValue("id")}, nil); err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true})
	})
	s.handle("POST /api/v1/assistant/main/new", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Carry  string `json:"carry"`  // summary (default) | none
			Expect string `json:"expect"` // the main conversation the app shows: refused when it is another by now
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if in.Carry != "" && in.Carry != "summary" && in.Carry != "none" {
			apierr.Write(w, http.StatusBadRequest, fmt.Errorf("carry %q: summary or none", in.Carry))
			return
		}
		old, carried, err := s.newMain(r.Context(), in.Carry != "none", in.Expect)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true, "archived": old, "carried": carried})
	})
	s.handle("POST /api/v1/assistant/main/clear", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Expect string `json:"expect"` // the main conversation the app shows: refused when it is another by now
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&in); err != nil && !errors.Is(err, io.EOF) {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		old, err := s.clearMain(r.Context(), in.Expect)
		if err != nil {
			apierr.Call(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"ok": true, "archived": old})
	})
}

// ---- what the hub asks about a message or a rename ----

// badRequest is err as the refusal of a request that is wrong as it is (the hub answers it 400).
// badRequest is err as a bus error, said in l when it is an i18n.Message (no l: the default language).
func badRequest(err error, l ...i18n.Lang) error {
	msg := err.Error()
	if len(l) > 0 {
		msg = i18n.Localize(err, l[0])
	}
	return &rpc.Error{Code: CodeBadRequest, Message: msg}
}

// appLang is the language an app's request came in (a bus call's lang), else the user's.
func (s *Service) appLang(ctx context.Context, lang string) i18n.Lang {
	return i18n.Lang(lang).Or(s.lang(ctx))
}

// SendParams is assistant.send_params: a message for conversation p.SessionID on p.MachineID, with what it runs on
// filled in (sendModel) and, the first one to a new main conversation started empty, what the old one left before
// its text. A conversation the machine does not list (yet) that is not the main one keeps what it runs on.
func (s *Service) SendParams(ctx context.Context, p SendParams) (map[string]any, error) {
	return s.messageParams(ctx, p, true)
}

// messageParams is SendParams for a message that is the owner's own (owner), or one the manager writes in their
// place (a channel's "answer again"), which preempts nothing.
func (s *Service) messageParams(ctx context.Context, p SendParams, owner bool) (map[string]any, error) {
	received := time.Now()
	if err := s.conversationAvailable(ctx, p.MachineID, p.SessionID); err != nil {
		return nil, err
	}
	if p.Params == nil {
		p.Params = map[string]any{}
	}
	delete(p.Params, assistantwire.DefaultPermissionModeParam)
	delete(p.Params, assistantwire.PermissionModeExplicitParam)
	delete(p.Params, "source") // app input cannot claim an automated author
	delete(p.Params, "message_source")
	approvalTarget, _ := p.Params["approval_reply_to"].(string)
	delete(p.Params, "approval_reply_to") // approval metadata is never model input, including on a failed send
	approvalCreatedAt, _ := p.Params["approval_reply_created_at"].(string)
	delete(p.Params, "approval_reply_created_at")
	if id, _ := p.Params["client_msg_id"].(string); id != "" {
		s.availOwnerSent(p.MachineID, p.SessionID, id, time.Now()) // a replay opens no reply wait (availability.go)
	}
	text, said := p.Params["text"].(string)
	if said {
		p.Params["text"] = prompts.ProtectUserMessage(text)
	} else {
		_, said = p.Params["images"] // the node asking what its own message runs on carries neither (NodeAnswer)
	}
	hasContent := hasOwnerMessageContent(text, p.Params["images"])
	sent, _ := p.Params["text"].(string)
	id, _ := p.Params["client_msg_id"].(string)
	// How the user's app sent it when not typed in the app itself ("intent", "share"): it never counts as their own
	// words (secret_handoff.go handoffUserTurn), and is not passed on.
	via, marked := p.Params["via"]
	if text, ok := via.(string); via == nil || ok && text == "" {
		marked = false // a mark of any other kind still marks it
	}
	delete(p.Params, "via")
	params, own, err := s.sendParamsOwn(ctx, p)
	if err == nil {
		err = s.validateSkillSelections(ctx, p.MachineID, p.SessionID, params)
	}
	switch {
	case (!p.Human && !p.ReplyInput) || !own || marked: // anyone else's id, a person's carrying another's words (a mail handed on, the
		// old main conversation's) or sent by Siri or the share sheet, never counts, nor does a person's id once
		// another sent it (secret_handoff.go)
		s.humanSent.voidSend(p.SessionID, id)
	case err == nil && p.Human: // a paired device can also vouch for handoffUserTurn
		s.humanSent.note(p.SessionID, id, sent)
		if said {
			s.rememberReplyInput(ctx, p.MachineID, p.SessionID, id, received)
		}
	case err == nil:
		s.humanSent.noteForReply(p.SessionID, id, sent)
		if said {
			s.rememberReplyInput(ctx, p.MachineID, p.SessionID, id, received)
		}
	}
	if err == nil && approvalTarget != "" && owner && p.Human && !p.ReplyInput && own && !marked {
		handled, approvalErr := s.resolveAppOwnerReply(ctx, p.MachineID, p.SessionID, id, approvalTarget, text, approvalCreatedAt)
		if approvalErr != nil {
			return nil, badRequest(approvalErr, s.appLang(ctx, p.Lang))
		}
		if handled {
			return map[string]any{ownerApprovalHandledParam: approvalTarget}, nil
		}
	}
	if err == nil && said {
		s.setConversationOriginDevice(p.SessionID, string(p.OriginDeviceID))
	}
	if err == nil && said && owner {
		if err := s.releasePushes(ctx, p.SessionID); err != nil {
			return nil, err
		}
		s.endHeldChannelRuns(p.MachineID, p.SessionID)
		s.ownerInterjects(p.SessionID, params)
		if clarifyingCardOf(ctx) == "" && hasContent {
			s.detachPendingApprovalsOnOwnerMessage(p.MachineID, p.SessionID)
		}
	}
	return params, err
}

func hasOwnerMessageContent(text string, images any) bool {
	if strings.TrimSpace(text) != "" {
		return true
	}
	switch imgs := images.(type) {
	case []proto.Image:
		return len(imgs) > 0
	case []any:
		return len(imgs) > 0
	}
	return false
}

// ownerInterjects: params are the owner's own message to conversation id, ready to send. On a round they did not
// start it is read at once, whatever interject_background says (owner_preempt.go).
func (s *Service) ownerInterjects(id string, params map[string]any) {
	if s.ownerPreempts(id) {
		params["interject_background"] = true
	}
	s.ownerSent(id)
}

// sendParams also serves manager-generated notices, which must not be marked as
// user-authored merely because they share the app's model-selection path.
func (s *Service) sendParams(ctx context.Context, p SendParams) (map[string]any, error) {
	params, _, err := s.sendParamsOwn(ctx, p)
	return params, err
}

// sendParamsOwn is sendParams, and whether the message's text holds only the sender's own words as it goes (own,
// workspace_mention.go fillWorkspaces): no context block an app put in it, workspace mentions aside where they were
// filled in, and not what the old main conversation left (handoffFor). A text that is not filled in (a guest's, a
// conversation the manager does not list) is the sender's only with no context block at all.
func (s *Service) sendParamsOwn(ctx context.Context, p SendParams) (map[string]any, bool, error) {
	params := p.Params
	if params == nil {
		params = map[string]any{}
	}
	params["model_source"] = s.conversationModelSource(ctx, p.MachineID, p.SessionID)
	delete(params, "external")
	delete(params, "cache_keepalive") // only the manager may attest a refresh role, including on early returns
	if launcher, _ := params["launcher"].(string); proto.IsExternalLauncher(launcher) {
		return nil, false, badRequest(i18n.Err("external-adapter.scope_required"), s.appLang(ctx, p.Lang))
	}
	// Keep the raw value until external admission checks it; ordinary conversations ignore it, even on an early return.
	defer delete(params, "runtime")
	delete(params, "origin") // only the manager's origin book may attest an existing conversation
	delete(params, "origin_known")
	origin, originKnown := s.managerOriginDecision(ctx, p.MachineID, p.SessionID, "", false)
	if originKnown {
		params["origin_known"] = true // an empty origin is an explicit ordinary decision, including on assistant.model
	}
	if originKnown && origin == OriginInbox {
		ctx = context.WithValue(ctx, inboxAssistantFitKey{}, true)
	}
	if origin != "" {
		// A node reload does not restore the node's in-memory origin field. Re-attest it on every later send so the
		// first resumed process keeps its Inbox tool policy and its Lark gate cannot fall back to an ordinary conversation.
		params["origin"] = origin
	}
	if err := s.putInboxPermissionMode(ctx, p.MachineID, p.SessionID, params, origin, originKnown); err != nil {
		return nil, false, err
	}
	delete(params, "categories") // only the manager may attest conversation categories, including on early returns
	delete(params, "categories_unknown")
	text, _ := params["text"].(string)
	own := !contextTagRe.MatchString(text)
	params["is_main"] = false
	if err := s.putSupervisionInstructions(ctx, p.MachineID, p.SessionID, params); err != nil {
		return nil, false, err
	}
	if err := s.roomRefusesSend(ctx, p.MachineID, p.SessionID, p.Lang); err != nil {
		return nil, false, err // an ended room: 409 meeting_ended (meeting_room.go)
	}
	if s.isGuest(ctx, p.MachineID, p.SessionID) {
		// A guest conversation keeps what it started on: no launcher, model or mode picked for a message (the node
		// refuses a change too, node/assistant_guest.go).
		for _, k := range []string{"launcher", "model", "effort", "model_fingerprint", "permission_mode"} {
			delete(params, k)
		}
		return params, own, nil
	}
	cfg := s.settings(ctx)
	main := cfg.Main != "" && cfg.Main == p.SessionID && cfg.mainRouteMachine() == p.MachineID
	current, currentKnown := proto.Session{}, false
	if !main {
		current, currentKnown = s.session(ctx, p.MachineID, p.SessionID)
		if !currentKnown {
			return params, own, nil
		}
	}
	s.putConversationCategories(ctx, p.SessionID, params)
	// sendModel has successful early returns for native and external picks. Attach mode provenance at their common
	// exit too; on an error the params are not sent. Inbox keeps its dedicated policy, and guests returned above.
	inputMode, _ := params["permission_mode"].(string)
	inputMode = strings.TrimSpace(inputMode)
	sendModelFailed := false
	defer func() {
		if sendModelFailed || origin == OriginInbox {
			return
		}
		if inputMode != "" {
			if mode, _ := params["permission_mode"].(string); strings.TrimSpace(mode) != "" {
				params[assistantwire.PermissionModeExplicitParam] = true
			}
			return
		}
		used, _ := params["launcher"].(string)
		if used == "" {
			if currentKnown {
				used = current.Launcher
			} else if current, known := s.session(ctx, p.MachineID, p.SessionID); known {
				used = current.Launcher
			}
		}
		if proto.IsExternalLauncher(used) {
			return
		}
		executionMachine := s.logicalExecutionMachine(ctx, p.MachineID, p.SessionID)
		// The manager default is a Claude Code resume policy. Other agents keep their saved AppMode even when their
		// declared mode list contains the default; an unknown launcher keeps the node's existing validation path.
		if used != "" && used != "claude" {
			if agent := s.launcherAgent(withUnhidden(ctx, used), executionMachine, used); agent != "" && agent != "claude" {
				return
			}
		}
		if mode, _ := params["permission_mode"].(string); strings.TrimSpace(mode) != "" {
			// logicalSendModel inherited this generic mode from the active segment; it is not a new explicit choice.
			// Let Claude's current default take its place. Non-Claude returned above with its legacy generic mode intact.
			delete(params, "permission_mode")
		}
		mode := cmp.Or(cfg.DefaultPermissionMode, proto.ModeBypass)
		if s.startPermissionModeSupported(ctx, executionMachine, used, mode) {
			params[assistantwire.DefaultPermissionModeParam] = mode
		}
	}()
	if err := s.sendModel(ctx, p.MachineID, p.SessionID, params, s.appLang(ctx, p.Lang)); err != nil {
		sendModelFailed = true
		var other *launcherOtherKind
		if errors.As(err, &other) {
			return nil, false, other.rpcError(s.appLang(ctx, p.Lang)) // 409 launcher_other_kind, with the kinds for the app
		}
		if storedPickRefused(err) {
			return nil, false, err // the conversation's pick is out of scope or gone: its code goes to the app (model_pick.go)
		}
		return nil, false, badRequest(err, s.appLang(ctx, p.Lang)) // a typed model that is no id is the app's user's to read
	}
	if text, ok := params["text"].(string); ok && !p.VerbatimText {
		text, own = s.fillWorkspaces(ctx, text) // an @-mentioned workspace: where it is and what worked there (workspace_mention.go)
		if main {
			// The agent only orders a workflow command and what the old main conversation left (renderHandoff), so an
			// ordinary message asks no computer for its launchers.
			agent := ""
			if currentKnown {
				agent = current.Agent
			}
			if _, command := agentprompt.WorkflowCommand(text); command {
				if used, _ := params["launcher"].(string); agent == "" && used != "" {
					agent = s.launcherAgent(withUnhidden(ctx, used), s.logicalExecutionMachine(ctx, p.MachineID, p.SessionID), used)
				}
			}
			left := s.handoffForAgent(ctx, p.SessionID, agent, text)
			own = own && left == text // what the old main conversation left is not the sender's words
			text = left
		}
		params["text"] = text
	}
	return params, own, nil
}

// sendModel puts into p (a message for one of the assistant's conversations, sid on machine) which launcher and model
// run it: one picked in the chat's menu (sent with the message) wins; else the main conversation and a scheduled
// task's own conversation (the morning briefing's, #1544), a leader conversation and the background supervisor follow
// Model configuration › Main chat, and the others keep what they run. Those follow
// the setting on every message, not what they ran last: once a run had moved one to another launcher, every later
// message stayed there (#1518). A launcher or model that is gone falls back to the assistant's. A picked one is checked
// and fingerprinted like a configured one. The phone's messages, the node's own and the follow-ups the assistant
// schedules for itself (runTask) all go through here.
//
// A model picked for this message may be one the launcher's list does not have (typed in the chip, #1644): it has to
// be a model id, and it is kept. Only a model carried over from what the conversation ran is held to the list (and the
// ids typed for that launcher before), so a model the gateway dropped does not fail every later turn.
//
// A message that picks neither a launcher nor a model runs on the conversation's own pick when it has one (PUT
// …/conversations/{id}/model, model_pick.go), its effort under the message's own: that is the user's choice for the
// conversation, so it is never replaced; a pick that can't run here refuses the message, saying why in lang.
func (s *Service) sendModel(ctx context.Context, machine, sid string, p map[string]any, lang i18n.Lang) error {
	ctx = withLauncherPurpose(ctx, "assistant")
	delete(p, "external")
	cfg := s.settings(ctx)
	p["is_main"] = cfg.Main != "" && cfg.Main == sid && cfg.mainRouteMachine() == machine
	delete(p, "idle_compaction")
	if p["is_main"] == true {
		p["idle_compaction"] = s.idleCompactionPolicy(ctx)
	}
	s.putCacheKeepalive(ctx, machine, sid, p["is_main"] == true, p)
	putMainCompactWindow(cfg, p, s.isLeader(ctx, sid)) // a direction's lead is held to it too (#15318)
	if handled, err := s.larkAgentFollowsCurrentModel(ctx, machine, sid, p, lang); handled {
		return err
	}
	if handled, err := s.logicalSendModel(ctx, machine, sid, p); handled {
		return err
	}
	if cfg.InterjectBackground != nil && *cfg.InterjectBackground {
		p["interject_background"] = true
	}
	if cfg.ClaudeInteractive != nil && *cfg.ClaudeInteractive {
		p["claude_interactive"] = true // what the conversation's Claude runs in (#10058, node/assistant_tui.go)
	}
	// A leader conversation and the background supervisor follow it too: the hub's own messages (supervision, digests)
	// drive them and pick nothing, so a model a failed turn's recovery moved one to (node/assistant_recovery.go: Opus
	// down to Haiku on a run of 429s) was carried over as what it runs, for good.
	followsSetting := cfg.Main != "" && cfg.Main == sid && cfg.mainRouteMachine() == machine || s.isTaskConversation(ctx, machine, sid) ||
		s.startedByTask(sid) || s.isLeader(ctx, sid) || s.isBackground(ctx, sid)
	// The owner-visible recovery rule (#7824) covers the main conversation and the leader/background supervisors, but
	// not worker Agent task conversations, whose cheaper-tier behavior stays unchanged. The node marks the protected
	// ones so it can publish and persist the model-recovery marker only for them.
	p["follows_setting"] = s.isLeader(ctx, sid) || s.isBackground(ctx, sid)
	inheritedLarkModel, _ := p[larkAgentInheritedModelKey].(bool)
	delete(p, larkAgentInheritedModelKey)
	picked := false // a model picked with this message
	if m, ok := p["model"].(string); ok && strings.TrimSpace(m) != "" {
		m = strings.TrimSpace(m)
		if err := checkModelID(m); err != nil {
			return err
		}
		p["model"], picked = m, !inheritedLarkModel
	}
	l, _ := p["launcher"].(string)
	if proto.IsExternalLauncher(l) {
		return i18n.Err("external-adapter.scope_required") // only the kept pick, never a message's launcher
	}
	asked := l != "" // picked in the chat's menu for this message
	if asked {
		if err := s.otherKindPick(ctx, machine, sid, l); err != nil {
			return err // another agent kind can't resume this conversation: refused, not swapped on the node
		}
	}
	if !asked {
		pick, ok, err := s.readModelPick(ctx, machine, sid)
		if err != nil {
			return err // an unreadable pick book is not permission to fall back to the default
		}
		if ok {
			if proto.IsExternalLauncher(pick.Launcher) {
				if model, _ := p["model"].(string); picked && model != pick.Model {
					return i18n.Err("external-adapter.scope_required") // change the persisted pick, not a message's model
				}
				return s.sendOnPick(ctx, machine, sid, p, pick, lang)
			}
			if !picked {
				return s.sendOnPick(ctx, machine, sid, p, pick, lang)
			}
		}
	}
	if l == "" {
		c, known := s.session(ctx, machine, sid)
		if known && c.SourceKind == proto.SourceRuntime {
			// Preserve existing runtime inheritance unless the main chat has its
			// own binding, or its inherited binding already targets this runtime
			// launcher with a configured model or effort.
			if p["is_main"] != true {
				return nil
			}
			record, err := s.modelRecord(ctx, purposeMain)
			if err != nil {
				return err
			}
			if record.Binding == (ModelBinding{}) {
				resolved, err := s.resolveModel(ctx, purposeMain)
				if err != nil {
					return err
				}
				if c.Launcher == "" || resolved.Provider != launcherProvider(c.Launcher) || (resolved.Model == "" && resolved.Effort == "") {
					return nil
				}
			}
		}
		if known && !followsSetting {
			l = c.Launcher // no launcher picked: the one this conversation runs
			if l != "" {
				p["launcher"] = l
			}
			// And the model it runs, so no pick keeps it; one that is no model id (Claude's "<synthetic>") is not.
			if _, asked := p["model"]; !asked {
				model := c.Model // older nodes only report the transcript name
				if c.RequestedModel != nil {
					model = *c.RequestedModel
				}
				if proto.ValidModelID(model) {
					p["model"] = model
				}
			}
		}
	}
	if proto.IsExternalLauncher(l) {
		return i18n.Err("external-adapter.scope_required") // a side conversation cannot inherit an old external launcher
	}
	// One picked for this message that can't run the assistant is refused, saying why. Otherwise keep the conversation's
	// binding intact: finishMainChatModel owns rediscovery and explicitly configured substitutes for a launcher that is
	// gone or no longer compatible. Replacing it with launcherNow here would bypass that policy before it can run.
	if l != "" {
		model, _ := p["model"].(string)
		fit, refusal := s.mainChatFit(ctx, machine, l)
		switch {
		case fit == mainChatRefused && asked:
			return refusal
		case fit == mainChatFits && model != "" && !picked:
			resolved, ok := s.resolveLauncherModel(ctx, machine, l, model)
			if !ok {
				s.logf("[%s] %s: model %s is not in %s's list any more, using its default", machine, short(sid), model, l)
				delete(p, "model")
				leaveLauncherNote(ctx, i18n.T(lang, "assistant-start.model_missing", i18n.Args{"model": model, "launcher": l}))
			} else if resolved != "" && resolved != model {
				// The gateway echoed a routed name for an older node's row: send the launcher's clean declared id.
				p["model"] = resolved
			}
		}
	}
	pickedLauncher := l != ""
	// The background supervisor runs under its own purpose (which follows Main chat until it is set apart), whoever
	// writes to it: the hub's relays already do (backgroundModel), so a message typed to it must too.
	purpose := s.conversationModelPurpose(ctx, machine, sid)
	if pickedLauncher {
		if err := s.applyPickedModel(ctx, p); err != nil {
			return err
		}
	} else {
		if err := preservePermissionMode(p, func() error { return s.applyModel(ctx, purpose, p) }); err != nil {
			return err
		}
	}
	if purpose == purposeMain {
		if err := s.mainDefaultExecution(ctx, machine, sid, p); err != nil {
			return err
		}
	}
	_, err := s.finishMainChatModel(ctx, machine, purpose, p, pickedLauncher)
	return err
}

// preservePermissionMode runs fn against an existing conversation's send params without letting a purpose's
// configured permission_mode be injected into or overwrite the session's active permission mode.
func preservePermissionMode(p map[string]any, fn func() error) error {
	prevMode, hadMode := p["permission_mode"]
	if err := fn(); err != nil {
		return err
	}
	if hadMode {
		p["permission_mode"] = prevMode
	} else {
		delete(p, "permission_mode")
	}
	return nil
}

// sendOnPick puts conversation sid's pick into p (sendModel): its launcher, model and effort (an effort sent with the
// message wins), checked and fingerprinted like a message's pick. One that can't run on machine, or runs the other
// agent kind (launcher_kind.go), refuses the message.
func (s *Service) sendOnPick(ctx context.Context, machine, sid string, p map[string]any, pick modelPick, lang i18n.Lang) error {
	if proto.IsExternalLauncher(pick.Launcher) {
		var err error
		ctx, err = s.externalPickContext(ctx, machine, sid, pick)
		if err != nil {
			return err
		}
	} else if err := s.modelPickRefusal(ctx, machine, pick.Launcher, lang, true); err != nil {
		return err
	}
	if err := s.otherKindPick(ctx, machine, sid, pick.Launcher); err != nil {
		return err
	}
	return preservePermissionMode(p, func() error { return s.applyPickFor(ctx, s.conversationModelPurpose(ctx, machine, sid), p, pick) })
}

func (s *Service) conversationModelPurpose(ctx context.Context, machine, sid string) string {
	cfg := s.settings(ctx)
	if cfg.Main != "" && cfg.Main == sid && cfg.mainRouteMachine() == machine {
		return purposeMain
	}
	if s.isBackground(ctx, sid) {
		return purposeSupervise
	}
	return "assistant"
}

// startedByTask: sid is a conversation a scheduled task started (origin task), also once it is no task's current
// conversation (a task's "New chat next time", a deleted task): it follows the setting like the current one, which is
// what the apps show for every conversation of that origin (#9248).
func (s *Service) startedByTask(sid string) bool {
	o, ok := s.conversationOrigins()
	return ok && o[sid].origin == OriginTask
}

// isTaskConversation: sid on machine is a scheduled task's own conversation.
func (s *Service) isTaskConversation(ctx context.Context, machine, sid string) bool {
	return slices.ContainsFunc(s.taskViews(ctx), func(t TaskView) bool { return t.RunSessionID == sid && t.RunMachineID == machine })
}

// Rename is assistant.rename: the user renames conversation p.SessionID in the app ({title?, tab_name?}). The main
// conversation has no title to change; a Codex one keeps the assistant's title (its tab is still renamed). The node
// renames; a new title is the user's from now on. An extension's (p.Extension, as the hub's auth found it) is only a
// hint (titleHinted): the hub still names a conversation it started, as it names the others.
// An active direction updates its canonical name instead, preserving the ordinary title.
func (s *Service) Rename(ctx context.Context, p RenameParams) (json.RawMessage, error) {
	params := p.Params
	if params == nil {
		params = map[string]any{}
	}
	params["session_id"] = p.SessionID
	title, _ := params["title"].(string)
	if strings.TrimSpace(title) != "" && s.isMain(ctx, p.SessionID) {
		return nil, badRequest(errRenameMain, s.lang(ctx)) // the apps show the persona there (#1313)
	}
	if c, found := s.session(ctx, p.MachineID, p.SessionID); found && !c.Assistant {
		return s.renameAgent(ctx, p, c)
	}
	directionTitle := strings.TrimSpace(title) != "" && p.Extension == "" &&
		p.MachineID == s.settings(ctx).MachineID && s.isLeader(ctx, p.SessionID)
	if c, _ := s.session(ctx, p.MachineID, p.SessionID); c.Agent == "codex" || directionTitle {
		delete(params, "title") // only the assistant's title for Codex (its conversation_rename does the same)
	}
	out := json.RawMessage(`{"ok":true}`)
	if params["title"] != nil || params["tab_name"] != nil {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := s.call(cctx, p.MachineID, DeviceSessionRename, params, &out); err != nil {
			return nil, err
		}
	}
	switch {
	case directionTitle:
		if err := s.setDirectionName(ctx, p.SessionID, title, directionByUser); err != nil {
			return nil, err
		}
	case strings.TrimSpace(title) == "":
	case p.Extension != "":
		s.titleHinted(p.MachineID, p.SessionID, title)
	default:
		s.titleSet(p.MachineID, p.SessionID, title, titleByUser)
	}
	return out, nil
}

// answerLanguage is what the CLAUDE.md asks the assistant to answer in, and the language the inbox's summaries and
// the conversations' titles are written in: the user's language by its own name, "" for zh-Hans (the prompts then
// say "use Chinese", as they did before i18n).
func answerLanguage(locale string) string {
	l := i18n.Lang(locale)
	if !i18n.Valid(l) || l == i18n.ZhHans {
		return ""
	}
	return l.Name()
}
