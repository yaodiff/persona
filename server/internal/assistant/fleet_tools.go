package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// The nodes' MCP tools (mcp.tools / mcp.call): an assistant conversation's or an agent's Persona tools. The manager
// lists and routes them: the phone's (the hub runs them on the connected phone), the connectors', Cua's and ego's
// (the extensions', in this process: ExtensionTools, internal/extmgr), and the assistant's own (here). The skills' gated commands' settings go to
// the assistant's machine only (assistant.lark).

// runtimeCLITool is the DEBUG app's own channel, for the user's scripts only: never a node's.
const runtimeCLITool = "phone_runtime_cli"

// mcpTools are the tools of an assistant conversation or an agent: the phone's, then those of the connectors that
// are ok and give their tools to agents too (mail); for the assistant also its own, then Cua's, ego's and those of
// the connectors only it gets (Feishu, GitHub…).
func (s *Service) mcpTools(ctx context.Context, assistant bool) []proto.Tool {
	ctx = i18n.With(ctx, s.lang(ctx))
	var agents, assistantOnly []proto.Tool
	if s.ext != nil {
		agents, assistantOnly = s.ext.AgentTools(ctx, assistant)
	}
	tools := append(s.phoneTools(ctx), agents...)
	if !assistant {
		tools = append(tools, toolsNamed(resourceToolNames)...)
	}
	if !assistant {
		tools = append(tools, s.knowledgeFolderTools(ctx)...)
		tools = append(tools, s.AgentTools()...)       // report_to_supervisor, todo_decisions, todo_decision_add, todo_bind_thread (agentToolNames)
		tools = append(tools, s.dispatchTools(ctx)...) // dispatch_start, dispatch_read, when agents.dispatch is on (dispatch.go)
	}
	if assistant {
		tools = append(tools, s.Tools(ctx)...)
		tools = append(tools, assistantOnly...)
	}
	tools = append(tools, s.fileTransferTools(ctx)...) // file_transfer, unless agents.file_transfer is off (file_transfer.go)
	return localizedMCPTools(tools, s.toolLanguage(ctx))
}

// phoneToolTries is how many times a tool list asks the hub for the phone's tools, phoneToolRetry apart, before it
// takes the ones the hub last listed (#6422); tests shorten the wait.
var (
	phoneToolTries = 3
	phoneToolRetry = 500 * time.Millisecond
)

// keptPhoneTools is the phone's tools as the hub last listed them (ok: it has answered since the manager started).
type keptPhoneTools struct {
	mu    sync.Mutex
	tools []proto.Tool
	ok    bool
}

// phoneTools are the phone's tools for a tool list, as the hub lists them now. Tool refresh is driven by task links,
// not phone connectivity, and older MCP clients list only once. A list made while the hub can't answer (restarting,
// its link coming back) must not leave the model thinking the phone has no tools (#6422):
// the hub is asked again briefly, then the list it last gave is used. A call is still the phone's to refuse (a source
// turned off since). Only a manager the hub never answered lists none, as before.
func (s *Service) phoneTools(ctx context.Context) []proto.Tool {
	var err error
	for try := 1; ; try++ {
		var phone struct {
			Tools []proto.Tool `json:"tools"`
		}
		if err = s.appCall(ctx, assistantwire.AppPhone, assistantwire.PhoneTools, map[string]any{}, &phone); err == nil {
			k := &s.phoneKept
			k.mu.Lock()
			k.tools, k.ok = slices.Clone(phone.Tools), true
			k.mu.Unlock()
			return phone.Tools
		}
		if try >= phoneToolTries || ctx.Err() != nil {
			break
		}
		t := time.NewTimer(phoneToolRetry)
		select {
		case <-ctx.Done():
			t.Stop()
		case <-t.C:
		}
		if ctx.Err() != nil {
			break
		}
	}
	k := &s.phoneKept
	k.mu.Lock()
	tools, ok := slices.Clone(k.tools), k.ok
	k.mu.Unlock()
	if !ok {
		s.logf("mcp tools: the phone's: %v; none listed yet, so none", err)
		return nil
	}
	s.logf("mcp tools: the phone's: %v; the %d the hub last listed", err, len(tools))
	return tools
}

// sessionMCPTools are machine's conversation session's tools: a guest conversation's only its own (guest.go; the
// node saying it is one, the hub not knowing it or not able to tell: none), any other's mcpTools. The set the process
// asked for (mcp.tools tool_set, on ctx: toolSetOf): "supervise", a background supervisor's round, narrows an
// assistant conversation's list to the round's (roundTools, supervise_round.go); none, or one this manager does not
// know, is the whole list.
func (s *Service) sessionMCPTools(ctx context.Context, machine, session string, assistant, guestHint bool) []proto.Tool {
	tools, err := s.sessionMCPToolsChecked(ctx, machine, session, assistant, guestHint)
	if err != nil {
		s.logf("mcp tools: %v", err)
		return []proto.Tool{}
	}
	return tools
}

// Discovery with a cursor must distinguish a denied/empty list from a failed
// state read. Otherwise the failed list is cached until an unrelated link changes.
func (s *Service) sessionMCPToolsChecked(ctx context.Context, machine, session string, assistant, guestHint bool) (result []proto.Tool, resultErr error) {
	defer func() { result = localizedMCPTools(result, s.toolLanguage(ctx)) }()
	if assistant && !s.assistantDiscovery(ctx, machine, session) {
		s.logf("mcp tools: assistant discovery denied for machine %s session %s (tool_set=%q); falling back to agent tools", machine, short(session), toolSetOf(ctx))
		s.noteAssistantDenied(ctx, machine, session, "mcp.tools")
		assistant = false
	}
	allowed, guest, err := s.guestOf(ctx, machine, session)
	if err != nil {
		return nil, err
	}
	// A proxy reply's guest lists only its one tool, before any other guest's list (bot_handoff.go).
	if id, handoff, err := s.handoffOf(ctx, machine, session); guest && err == nil && handoff {
		_ = id
		if s.isWorkMachine(machine) {
			return []proto.Tool{}, nil
		}
		return guestTools(s.larkModule().HandoffTools(), allowed), nil
	}
	// A group guest lists the reply tool and the owner-listed connector tools (bot_guest.go).
	if key, group, err := s.groupGuestOf(ctx, machine, session); guest && err == nil && group {
		_ = key
		if s.isWorkMachine(machine) {
			return []proto.Tool{}, nil
		}
		// The connector tools come from the extensions' own list, agent-facing and assistant-only. mcpTools builds its
		// list from the built-in catalog and panics on a connector's name.
		var connectorTools []proto.Tool
		if s.ext != nil {
			agents, assistantOnly := s.ext.AgentTools(ctx, true)
			for _, t := range append(agents, assistantOnly...) {
				if s.ext.ConnectorTool(t.Name) {
					connectorTools = append(connectorTools, t)
				}
			}
		}
		tools := guestTools(append(s.larkModule().HandoffTools(), connectorTools...), allowed)
		return slices.DeleteFunc(tools, func(t proto.Tool) bool { return isKnowledgeFolderTool(t.Name) }), nil
	}
	if _, room, err := s.roomOf(ctx, machine, session); guest && err == nil && room {
		if s.isWorkMachine(machine) {
			return []proto.Tool{}, nil
		}
		return guestTools(s.meetingRoomTools(), allowed), nil
	}
	if !guest {
		if guestHint {
			return []proto.Tool{}, nil
		}
		if s.isWorkMachine(machine) {
			return s.AgentTools(), nil
		}
		if !assistant && session != "" {
			if err := s.ensureTodosLoaded(ctx); err != nil {
				return nil, err
			}
		}
		tools := s.mcpTools(ctx, assistant)
		if assistant {
			tools = append(tools, s.healthTools(ctx, machine, session)...)
			tools = append(tools, s.externalSourceTools(ctx, machine, session)...)
			tools = s.guardTools(ctx, machine, session, tools)
			tools = append(tools, s.spaceTools(ctx, machine, session)...) // space_state_update (track_space_md.go)
		}
		if !assistant {
			tools = append(tools, s.leaderTools(ctx, machine, session)...)
			tools = append(tools, s.spawnTools(ctx, machine, session)...)
		}
		cfg := s.a2aConfig(ctx)
		_, hasA2A := s.a2aStore()
		if hasA2A == nil && (session == "" && s.a2aMachineAllowed(ctx, cfg, s.canonical(machine)) ||
			s.a2aToolAllowed(ctx, cfg, a2aRef{s.canonical(machine), session})) {
			tools = append(tools, toolsNamed(assistantwire.A2AToolNames)...)
		}
		if assistant && toolSetOf(ctx) == toolSetSupervise {
			tools = roundTools(tools)
		}
		return tools, nil
	}
	if s.isWorkMachine(machine) {
		return []proto.Tool{}, nil
	}
	tools := guestTools(s.mcpTools(ctx, assistant), allowed)
	return slices.DeleteFunc(tools, func(t proto.Tool) bool { return isKnowledgeFolderTool(t.Name) }), nil
}

// guestRefuses refuses a guest conversation's call of a tool it was not given (and any call of one the hub cannot
// tell about, or that the node says is a guest the hub does not know).
func (s *Service) guestRefuses(ctx context.Context, machine, session, name string, guestHint bool) (proto.ToolResult, bool) {
	allowed, guest, err := s.guestOf(ctx, machine, session)
	switch {
	case err != nil:
		s.logf("mcp call %s: %v", name, err)
		return proto.TextResult("tool unavailable for the moment: "+name, true), true
	case !guest && guestHint:
		return proto.TextResult("unknown tool "+name, true), true
	case guest && (isKnowledgeFolderTool(name) || !slices.Contains(allowed, name)):
		return proto.TextResult("unknown tool "+name, true), true
	}
	return proto.ToolResult{}, false
}

// mcpCall runs one: a connector's, Cua's or ego's at the hub (it says whether it is one of them), else the
// assistant's own (for its conversations), else the phone's.
func (s *Service) mcpCall(ctx context.Context, machine, session, name string, args json.RawMessage, assistant bool) proto.ToolResult {
	return s.mcpCallWithOrigin(ctx, machine, session, name, args, assistant, "")
}

// mcpCallWithOrigin also takes the node's attested conversation origin. It is needed before assistant.send returns:
// the manager's origin book is written from the returned session afterwards.
func (s *Service) mcpCallWithOrigin(ctx context.Context, machine, session, name string, args json.RawMessage, assistant bool, origin string) proto.ToolResult {
	if res, refused := s.meetingRefuses(ctx, machine, session, name, assistant); refused {
		return res // a meeting's batch started this turn: only reading tools (meeting_turns.go)
	}
	if s.isWorkMachine(machine) {
		if isAgentTool(name) {
			return s.agentTool(ctx, machine, session, name, args)
		}
		return proto.TextResult(i18n.T(s.lang(ctx), "assistant-privacy.tool_denied"), true)
	}
	if assistant && !s.assistantCaller(ctx, machine, session) {
		s.noteAssistantDenied(ctx, machine, session, name)
		return proto.TextResult(i18n.T(s.lang(ctx), "assistant-privacy.assistant_denied"), true)
	}
	if res, refused := s.inboxModelRefusal(ctx, machine, session, origin, name); refused {
		return res
	}
	if res, refused := s.inboxVisibilityToolRefusal(ctx, machine, session, origin, name, args); refused {
		return res
	}
	if isExternalSourceTool(name) {
		if !assistant {
			return proto.TextResult("unknown tool "+name, true)
		}
		return s.Tool(ctx, ToolCall{Name: name, Arguments: args, MachineID: machine, SessionID: session})
	}
	if slices.Contains(resourceToolNames, name) {
		return s.resourceTool(ctx, machine, session, name, args)
	}
	if name == "secret_store" {
		return s.toolSecretStore(ctx, machine, session, args)
	}
	if assistantwire.IsA2ATool(name) {
		return s.a2aTool(ctx, a2aRef{s.canonical(machine), session}, name, args)
	}
	if name == fileTransferTool { // Agents' and assistant conversations' alike (file_transfer.go)
		return s.fileTransferToolCall(ctx, machine, session, args, assistant)
	}
	if isKnowledgeFolderTool(name) && !assistant {
		return s.Tool(ctx, ToolCall{Name: name, Arguments: args, MachineID: machine, SessionID: session})
	}
	if s.ext != nil { // the connectors', Cua's, ego's: ctx is the node's call (its caller going away withdraws a card)
		if s.ext.ConnectorTool(name) { // a connector is told where the call comes from (connector_context.go)
			ctx = connector.WithCallContext(ctx, s.connectorCallContext(ctx, session))
			if assistant && machine != "" && session != "" && !s.isWorkMachine(machine) && !s.isRoom(session) {
				if _, guest, err := s.guestOf(ctx, machine, session); err == nil && !guest {
					ctx = connector.WithOwnerCaller(ctx)
				} else {
					ctx = connector.WithoutOwnerCaller(ctx)
				}
			} else {
				ctx = connector.WithoutOwnerCaller(ctx)
			}
		}
		callerMachine := machine
		if name == "browser_fetch_download" {
			// The file belongs on the machine running this turn. Approval still belongs to the logical conversation.
			if caller, ok := ctx.Value(logicalAuthenticatedKey{}).(logicalAuthenticated); ok &&
				caller.Machine == machine && caller.Session == session && caller.Lease != nil && caller.Lease.Load() {
				callerMachine = caller.PhysicalMachine
			}
		}
		if res, ok := s.ext.CallTool(ctx, callerMachine, session, name, args, assistant); ok {
			return res
		}
	}
	if !assistant && isAgentTool(name) {
		return s.agentTool(ctx, machine, session, name, args)
	}
	if !assistant && name == "task_spawn" {
		text, err := s.taskSpawn(ctx, machine, session, args)
		if err != nil {
			return proto.TextResult(err.Error(), true)
		}
		return proto.TextResult(text, false)
	}
	if !assistant && name == "task_plan" { // only a task leader's agent has it (task_leader.go)
		return s.leaderTaskPlan(ctx, machine, session, args)
	}
	if !assistant && name == "task_review" {
		return s.leaderTaskReview(ctx, machine, session, args)
	}
	if !assistant && isDispatchTool(name) {
		return s.dispatchTool(ctx, machine, session, name, args)
	}
	if assistant && (name == spaceStateTool || slices.Contains(healthToolNames, name)) { // listed per conversation, so not in Tools: its handler checks
		return s.Tool(ctx, ToolCall{Name: name, Arguments: args, MachineID: machine, SessionID: session})
	}
	if assistant && s.isOwnTool(ctx, name) {
		return s.Tool(ctx, ToolCall{Name: name, Arguments: args, MachineID: machine, SessionID: session})
	}
	switch {
	case name == runtimeCLITool:
		return proto.TextResult("unknown tool "+name, true)
	case strings.HasPrefix(name, "phone_"):
		if name == "phone_notify" && s.justMeNow(machine, session) {
			// A Just me turn (meeting_justme.go): a phone on the table shows its notifications to the room. As
			// notify_user's: nothing goes out, the text stays in the conversation, which the meeting page shows.
			return proto.TextResult(justMeNotice(s.lang(ctx)), false)
		}
		if name == "phone_notify" {
			// Local phone notifications follow the same privacy rule as notify_user: what they say decides.
			var a map[string]any
			decodeErr := json.Unmarshal(args, &a)
			title, _ := a["title"].(string)
			body, _ := a["body"].(string)
			if space, _ := s.selfSpace(ctx, session); s.exitPolicy(ctx, title+"\n"+body, session, space).Redact {
				if decodeErr != nil {
					return proto.TextResult(decodeErr.Error(), true)
				}
				if a != nil {
					a["title"], a["body"] = s.redactedNotice(ctx, s.lang(ctx), space)
					args, _ = json.Marshal(a)
				}
			}
		}
		var res proto.ToolResult
		params := map[string]any{"name": name, "args": args}
		mcpWaitParams(ctx, params)
		if err := s.appCall(ctx, assistantwire.AppPhone, assistantwire.PhoneCall, params, &res); err != nil {
			return proto.TextResult(err.Error(), true)
		}
		return phoneCreateResultWithSource(name, res)
	}
	return proto.TextResult("unknown tool "+name, true)
}

// phoneCreateResultWithSource carries the hub's executing-device attribution into the JSON that quick-result cards
// parse. Payload source claims are removed before attested values are added. Failures, malformed responses and
// unrelated tools keep their original content; legacy and queued payloads without source claims stay byte-identical.
func phoneCreateResultWithSource(tool string, res proto.ToolResult) proto.ToolResult {
	if tool != "phone_calendar_create_event" && tool != "phone_reminders_create" || res.IsError {
		return res
	}
	sourceName := strings.TrimSpace(res.SourceDeviceName)
	sourcePlatform := strings.TrimSpace(res.SourceDevicePlatform)
	res.SourceDeviceName, res.SourceDevicePlatform = sourceName, sourcePlatform
	res.Content = slices.Clone(res.Content)
	for i, content := range res.Content {
		var payload map[string]json.RawMessage
		if err := json.Unmarshal([]byte(content.Text), &payload); err != nil || payload == nil {
			continue
		}
		var state string
		queued := payload["state"] != nil && json.Unmarshal(payload["state"], &state) == nil && state == "queued"
		_, hasName := payload["source_device_name"]
		_, hasPlatform := payload["source_device_platform"]
		if (queued || sourceName == "" && sourcePlatform == "") && !hasName && !hasPlatform {
			continue
		}
		delete(payload, "source_device_name")
		delete(payload, "source_device_platform")
		if !queued {
			if sourceName != "" {
				raw, _ := json.Marshal(sourceName)
				payload["source_device_name"] = raw
			}
			if sourcePlatform != "" {
				raw, _ := json.Marshal(sourcePlatform)
				payload["source_device_platform"] = raw
			}
		}
		text, err := json.Marshal(payload)
		if err != nil {
			continue
		}
		res.Content[i].Text = string(text)
	}
	return res
}

// mailAttachment answers a node's mail.attachment: the bytes of the attachment mail_save_attachment saves on that
// machine (node/mailattachment.go), for a caller that could call the tool through mcp.call: not a work machine, an
// assistant conversation only when it is one, a guest only when it was given the tool; then the extensions decide
// whether the mail connector offers it to that caller (extmgr MailAttachment).
func (s *Service) mailAttachment(ctx context.Context, machine string, req proto.MailAttachmentRequest) (proto.MailAttachment, error) {
	// Not named "name": the i18n key check resolves constants by name across the package, and a constant called
	// name would turn every "prefix."+name text wrapper into one whole key.
	const saveAttachmentTool = "mail_save_attachment"
	if s.isWorkMachine(machine) {
		return proto.MailAttachment{}, errors.New(i18n.T(s.lang(ctx), "assistant-privacy.tool_denied"))
	}
	if req.Assistant && !s.assistantCaller(ctx, machine, req.SessionID) {
		return proto.MailAttachment{}, errors.New(i18n.T(s.lang(ctx), "assistant-privacy.assistant_denied"))
	}
	res, refused := s.guestRefuses(ctx, machine, req.SessionID, saveAttachmentTool, req.Guest)
	if !refused {
		res, refused = s.meetingRefuses(ctx, machine, req.SessionID, saveAttachmentTool, req.Assistant)
	}
	if refused {
		var why strings.Builder
		for _, c := range res.Content {
			why.WriteString(c.Text)
		}
		return proto.MailAttachment{}, errors.New(why.String())
	}
	if s.ext == nil {
		return proto.MailAttachment{}, errors.New("mail is not available")
	}
	return s.ext.MailAttachment(ctx, req)
}

// isOwnTool: name is one of the assistant's own tools.
func (s *Service) isOwnTool(ctx context.Context, name string) bool {
	for _, t := range s.Tools(ctx) {
		if t.Name == name {
			return true
		}
	}
	return false
}

// assistantLark is what the assistant's machine gets for its skills' commands (proto.AssistantLark): lark-cli's
// settings and rules, and the plugins' command gates (the hub's, app.call hub). The credentials go to the assistant's
// machine only (its process runs them): another personal machine gets otherMachineLark, a work machine nothing.
func (s *Service) assistantLark(ctx context.Context, machine string) (proto.AssistantLark, error) {
	if s.isWorkMachine(machine) || s.ext == nil {
		return proto.AssistantLark{}, nil
	}
	cfg, err := s.ext.AssistantLark(ctx)
	if err != nil || s.sameMachine(machine, s.settings(ctx).MachineID) {
		return cfg, err
	}
	return otherMachineLark(cfg), nil
}

// otherMachineLark is what another personal machine's gate gets for the assistant conversations the manager starts
// there (a Lark /agent placed on it, #5479): the rules and lark-cli's name, so its lark-cli calls go by lark-cli's own
// risk levels instead of all being confirmed. Not the command gates (their credentials) nor the extensions' MCP
// servers, and not the profile, which names one in the manager computer's lark-cli: that machine uses its lark-cli's
// default profile, and an absolute bin is looked up there by its base name.
func otherMachineLark(cfg proto.AssistantLark) proto.AssistantLark {
	out := proto.AssistantLark{Rules: cfg.Rules}
	if cfg.CLI != nil {
		out.CLI = &proto.LarkCLI{Bin: cfg.CLI.Bin}
		if out.CLI.Bin != "" {
			out.CLI.Bin = filepath.Base(out.CLI.Bin)
		}
	}
	return out
}

// assistantSummary is the text a gated command's card shows, as its plugin names what the ids are; false: none.
func (s *Service) assistantSummary(ctx context.Context, tool, summary string) (string, bool) {
	if tool == "" {
		return summary, false
	}
	if s.ext == nil {
		return summary, false
	}
	return s.ext.AssistantSummary(ctx, tool, summary)
}

// Localize only fixed built-in definitions, after discovery/permission filtering.
// The source slices and third-party tool contracts are never rewritten.
func localizedMCPTools(tools []proto.Tool, lang string) []proto.Tool {
	out := slices.Clone(tools)
	for i, t := range out {
		localized := prompts.LocalizeTool(prompts.MCPTool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}, lang)
		out[i].Description, out[i].InputSchema = localized.Description, localized.InputSchema
	}
	return out
}

// A zero Service can enumerate its static tools without a configured hub.
func (s *Service) toolLanguage(ctx context.Context) string {
	if s.hub == nil {
		return ""
	}
	return string(s.lang(ctx))
}
