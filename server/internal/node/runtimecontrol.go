package node

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/shellcmd"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// What a runtime that is a machine's engine needs beyond listing and driving its sessions (Paseo, docs/integrations/paseo.md):
// several launchers (one per provider it runs), the form's choices passed on when a session starts, the phone's
// permission mode and effort (caps.modes, runtimes.go), and — as the engine — every new session and every session to
// continue on this machine.

// runtimeLauncher is one of a runtime's entries for the new-session form (runtime.hello's launchers).
type runtimeLauncher struct {
	Name string `json:"name"`
	// Model is shown next to it (the provider's default model).
	Model string `json:"model,omitempty"`
	// Agent is what it starts: "claude" for Claude Code (the form's Claude choices, the apps' Claude controls), else
	// the runtime's own (or another the apps know).
	Agent string `json:"agent,omitempty"`
	// Models are its models for the form (launchers.models), the default marked.
	Models []proto.AgentModel `json:"models,omitempty"`
	// PromptOptional: a Codex launcher the runtime starts with no first message too (Paseo's, #8140): the form need not
	// ask for one (proto.Launcher.PromptOptional).
	PromptOptional bool `json:"prompt_optional,omitempty"`
	// AutoAccept: its agents' approval is a switch a start in bypassPermissions turns on (Paseo's Auto Accept), passed on
	// in launchers.list (proto.Launcher.AutoAccept, #8141).
	AutoAccept bool `json:"auto_accept,omitempty"`
	// DisplayName and PermissionModes: what the apps call it (Paseo's label for the provider) and, when its agent is
	// not Claude Code, the permission modes a start takes (proto.Launcher's, #8261).
	DisplayName     string                  `json:"display_name,omitempty"`
	PermissionModes proto.PermissionModeSet `json:"permission_modes,omitempty"`
	// AssistantFor: on the runtime's assistant launcher, the Agent launcher it runs the assistant for
	// (proto.Launcher.AssistantFor).
	AssistantFor string `json:"assistant_for,omitempty"`
}

// Bounds on a runtime's launchers. A display name is as long as a provider's may be (the manager's maxDisplayName).
const (
	maxRuntimeLaunchers    = 64
	maxLauncherModels      = 200
	maxLauncherText        = 200
	maxLauncherDisplayName = 64
)

// cleanLaunchers keeps the launchers a runtime may offer: names a launcher may have (shellcmd.NameRe, the shell's
// rule), each once, agents that are ids, text clipped, the lists bounded. Its permission modes are Persona's once read
// (proto.PermissionModeSet).
func cleanLaunchers(list []runtimeLauncher) []runtimeLauncher {
	clipTo := func(s string, n int) string {
		if r := []rune(strings.TrimSpace(s)); len(r) > n {
			return string(r[:n])
		}
		return strings.TrimSpace(s)
	}
	clip := func(s string) string { return clipTo(s, maxLauncherText) }
	out := make([]runtimeLauncher, 0, len(list))
	seen := map[string]bool{}
	for _, l := range list {
		if len(out) == maxRuntimeLaunchers {
			break
		}
		if !shellcmd.NameRe.MatchString(l.Name) || seen[l.Name] || l.Agent != "" && !runtimeIDRe.MatchString(l.Agent) {
			continue
		}
		seen[l.Name] = true
		l.Model = clip(l.Model)
		l.DisplayName = clipTo(l.DisplayName, maxLauncherDisplayName)
		if !shellcmd.NameRe.MatchString(l.AssistantFor) { // a launcher's name, or none
			l.AssistantFor = ""
		}
		models := make([]proto.AgentModel, 0, min(len(l.Models), maxLauncherModels))
		for _, m := range l.Models {
			if len(models) == maxLauncherModels {
				break
			}
			if m.ID = clip(m.ID); m.ID == "" {
				continue
			}
			m.Name, m.Description, m.Resolved, m.DefaultEffort = clip(m.Name), clip(m.Description), clip(m.Resolved), clip(m.DefaultEffort)
			efforts := m.Efforts[:0:0]
			for _, e := range m.Efforts {
				if validModes("", e) == nil {
					efforts = append(efforts, e)
				}
			}
			m.Efforts = efforts
			models = append(models, m)
		}
		l.Models = models
		out = append(out, l)
	}
	return out
}

// launcherList is every launcher h offers: hello's launcher, then its launchers.
func (h *runtimeConn) launcherList() []runtimeLauncher {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []runtimeLauncher
	if h.hello.Launcher != "" {
		entry := runtimeLauncher{Name: h.hello.Launcher, Model: h.hello.LauncherModel}
		for _, l := range h.hello.Launchers {
			if l.Name == h.hello.Launcher {
				if l.Model != "" && entry.Model == "" {
					entry.Model = l.Model
				}
				entry.Agent = l.Agent
				entry.Models = l.Models
				entry.AutoAccept = l.AutoAccept
				entry.DisplayName, entry.PermissionModes, entry.AssistantFor = l.DisplayName, l.PermissionModes, l.AssistantFor
				break
			}
		}
		out = append(out, entry)
	}
	for _, l := range h.hello.Launchers {
		if l.Name != h.hello.Launcher {
			out = append(out, l)
		}
	}
	return out
}

// launcherNamed is h's launcher of that name.
func (h *runtimeConn) launcherNamed(name string) (runtimeLauncher, bool) {
	for _, l := range h.launcherList() {
		if l.Name == name {
			return l, true
		}
	}
	return runtimeLauncher{}, false
}

// setLaunchers replaces h's launchers (runtime.launchers: a runtime whose providers changed, or came in after hello).
func (h *runtimeConn) setLaunchers(list []runtimeLauncher) {
	h.mu.Lock()
	h.hello.Launchers = list
	h.mu.Unlock()
}

// runtimeLauncherModels is launchers.models for a runtime's launcher: the models it said (none: an empty list, the
// form offers no choice); false for a launcher no runtime runs (runtimeOf).
func (n *Node) runtimeLauncherModels(name string) (proto.LauncherModels, bool) {
	return launcherModelsOf(n.runtimeOf(name), name)
}

// agyRuntimeModels is the Antigravity companion's advertised catalog for settings.
func (n *Node) agyRuntimeModels() (proto.LauncherModels, bool) {
	return launcherModelsOf(n.runtimes.launcherRuntime(agyAgent), agyAgent)
}

// launcherModelsOf is runtime h's advertisement for its launcher name; false without a runtime.
func launcherModelsOf(h *runtimeConn, name string) (proto.LauncherModels, bool) {
	if h == nil {
		return proto.LauncherModels{}, false
	}
	l, _ := h.launcherNamed(name)
	agent := l.Agent
	if agent == "" {
		agent = h.agent()
	}
	models := l.Models
	if models == nil {
		models = []proto.AgentModel{}
	}
	return proto.LauncherModels{Agent: agent, Models: models}, true
}

// runtimeStartParams are what session.start passes to a runtime.
type runtimeStartParams struct {
	Cwd, Prompt, Launcher         string
	Model, PermissionMode, Effort string
	ClientMsgID                   string
	Images                        []proto.Image
	WorkspaceID                   string // a space of the runtime's to start in (one of its sessions' space_id)
	DesktopProjectID              string // the exact native desktop project selected in the project board
	Resume                        string // the Claude session to take over and continue (caps.resume)
	// SessionID: the id the hub named for a new session (session.start's session_id, #8130): the manager finds a
	// start whose answer it lost by it. A runtime that can give its session that id does (Paseo); one that cannot
	// picks its own, and the manager looks among the sessions Persona started instead (started_by_persona).
	SessionID string
	// AgentType: the kind of agent Launcher starts (claude, codex…), for a runtime that runs only its own launchers
	// (caps.same_kind) and does not have it: it starts one of its own of that kind, or refuses.
	AgentType       string
	RequireLauncher bool // the start carries context to a fresh session on its old launcher
	// DeferMCP asks an Antigravity Companion to return its real session id before attaching MCP or sending the first
	// prompt. The manager activates that starting row with an ordinary send after linking the task role. Older nodes and
	// Companions ignore the field; only an answer that actually says Starting is activated again.
	DeferMCP      bool `json:"defer_mcp"`
	Shell         bool // Launcher is a login shell's launcher: the runtime picks its own of that name (no caps.same_kind)
	OpenElsewhere bool // a running claude has Resume open: only a runtime running it may take it
}

// engineRuntimes maps the engines that are a runtime to that runtime's id (engine.go).
var engineRuntimes = map[string]string{
	proto.EnginePaseo:  proto.EnginePaseo,
	proto.EngineCindy:  proto.EngineCindy,
	proto.EngineT3Code: proto.EngineT3Code,
}

// engineRuntime is the runtime that is the host of a start in ctx (Paseo: the one the start named, else the machine's
// default host), when it is connected and starts sessions; nil when that host is a terminal (herdr, Orca…).
func (n *Node) engineRuntime(ctx context.Context) *runtimeConn {
	id, ok := engineRuntimes[n.startKind(ctx)]
	if !ok {
		return nil
	}
	n.runtimes.mu.Lock()
	h := n.runtimes.byRuntime(id)
	n.runtimes.mu.Unlock()
	if h == nil {
		return nil
	}
	h.mu.Lock()
	start := h.hello.Caps.Start
	h.mu.Unlock()
	if !start {
		return nil
	}
	return h
}

// engineRuntimeAway is why a session cannot start or continue on a machine whose engine is a runtime that is not
// connected (Paseo not running, or its bridge not up yet). Nothing was started or sent.
func (n *Node) engineRuntimeAway(ctx context.Context) error {
	name := proto.EngineNames[n.startKind(ctx)]
	return refusal(engineMissing, fmt.Sprintf("这台电脑的引擎是 %s，但 %s 现在没有连上 Persona（没在运行，或刚启动还没连上），不能在这里新开或接着会话，这次没有发送。"+
		"在电脑上打开 %s 后再试。", name, name, name), nil)
}

// resumeHostRefusal is why no resume could go on in the host a start names (host; "" the machine's default),
// whatever the id: a host this computer does not enable (checkStartHost), or an engine runtime that is not connected
// (engineRuntimeAway, Paseo restarting its bridge included). An extension's resume the computer does not know
// (resumeOutsideHealth) is told this before resume_unknown, as the owner's resume of any id is: the engine may have the
// session (Paseo keeps its Agents, and lists an archived one only for a while), and the refusal says nothing of the id
// (#10941). A host a record of the session picks (withResumeHost) is not asked: that would depend on the id.
func (n *Node) resumeHostRefusal(ctx context.Context, host string) error {
	if err := n.checkStartHost(host); err != nil {
		return err
	}
	ctx = withStartHost(ctx, host)
	if _, runtime := engineRuntimes[n.startKind(ctx)]; runtime && n.engineRuntime(ctx) == nil {
		return n.engineRuntimeAway(ctx)
	}
	return nil
}

// startOnEngineRuntime starts a session on the machine's engine runtime: a launcher of the login shell's, or a session
// to continue (resume), go there too — on such a machine there is no terminal to start them in.
func (n *Node) startOnEngineRuntime(ctx context.Context, p runtimeStartParams) (*proto.Session, *proto.StartInstead, error) {
	h := n.engineRuntime(ctx)
	if h == nil {
		return nil, nil, n.engineRuntimeAway(ctx)
	}
	if p.Resume != "" {
		s, err := n.takeOverInRuntime(ctx, h, p)
		return s, nil, err
	}
	// The engine runs every start with a provider of its own: the one it has by the launcher's name (a managed
	// launcher made from it carries the same configuration). For one it does not have, an engine that runs only its own
	// launchers (caps.same_kind, Paseo) is told the kind of agent it starts: it runs one of its own of that kind, said
	// on its answer, or refuses (#8140); never another kind, nor another account unsaid. An older engine picks its
	// default (shell_launcher). A managed launcher's own settings apply only where personad launch runs it
	// (runtimeStart), never inside the engine.
	if _, own := h.launcherNamed(p.Launcher); !own && p.Launcher != "" {
		if p.RequireLauncher {
			return nil, nil, refusal("launcher_missing", i18n.T(i18n.From(ctx), "hub-context_handoff.launcher_missing"), nil)
		}
		if sameKind(h) {
			p.AgentType = n.agentKindOf(p.Launcher, p.AgentType)
		} else {
			p.Shell = true
		}
	}
	return n.runtimeStartOn(ctx, h, p)
}

// takeOverInRuntime has the engine runtime take a Claude session over (session.start with resume): it continues it
// under the same id, its transcript the conversation. Refused when the runtime cannot (no caps.resume) or a running
// claude has the session open elsewhere (a second writer of the transcript).
func (n *Node) takeOverInRuntime(ctx context.Context, h *runtimeConn, p runtimeStartParams) (*proto.Session, error) {
	h.mu.Lock()
	resume := h.hello.Caps.Resume
	h.mu.Unlock()
	if !resume {
		return nil, refusal(notRunning, fmt.Sprintf("%s 不能接着这台电脑上没在运行的会话，这次没有发送。", h.label()), nil)
	}
	// A running claude has it open: the runtime's own (an agent of its it does not list) is taken as it is; anyone
	// else's would get a second writer, and the runtime refuses (open_elsewhere): the refusal says where it runs.
	elsewhere := n.openElsewhereRefusal(p.Resume)
	p.OpenElsewhere = elsewhere != nil
	// What the session ran with, for what the takeover leaves out: its directory, and its launcher — a branch's is the
	// provider of the session it was branched from (fork.go), so the runtime continues it on the same account rather
	// than on whichever provider ran last (#8130).
	st, _ := n.state.get(p.Resume)
	p.Cwd = cmp.Or(p.Cwd, st.Cwd)
	p.Launcher = cmp.Or(p.Launcher, st.Launcher)
	if p.Cwd == "" {
		if path := n.transcriptPath(p.Resume); path != "" {
			if fi, err := os.Stat(path); err == nil {
				p.Cwd = n.meta(path, fi).cwd
			}
		}
	}
	if p.PermissionMode == "" || p.Effort == "" {
		mode, effort := n.modesOf(p.Resume)
		p.PermissionMode = cmp.Or(p.PermissionMode, mode)
		p.Effort = cmp.Or(p.Effort, effort)
	}
	s, _, err := n.runtimeStartOn(ctx, h, p)
	if elsewhere != nil && rpcCode(err) == openElsewhere {
		return nil, elsewhere
	}
	if err != nil {
		return nil, err
	}
	if s.SessionID != p.Resume {
		return nil, fmt.Errorf("%s took session %s over as %s", h.label(), p.Resume, s.SessionID)
	}
	n.watchTakenOver(h, p.Resume)
	return s, nil
}

// watchTakenOver: runtime h has just taken session id over while the hub watches it (the node followed its transcript
// itself: the session was not running anywhere). Its conversation is still that transcript, but its live text comes
// from h now, and h follows a session only while it is asked to (Paseo subscribes to the agent's timeline per watched
// session): h is asked to watch it, as the hub's own session.watch would have once the session was h's
// (runtimeTranscriptCall). Without this the open chat showed no live text until it was opened again (#8130). Asked
// before the takeover answers, so the runtime follows the session before the message that follows it starts a turn.
func (n *Node) watchTakenOver(h *runtimeConn, id string) {
	n.mu.Lock()
	w := n.watches[id]
	n.mu.Unlock()
	if w == nil || w.parent == nil || w.ctx.Err() != nil || n.runtimes.watchedOn(h, id) {
		return
	}
	attempt := n.runtimes.watch(h, id, 0, "", w.parent)
	if err := n.runtimeWatch(h, id, attempt.since); err != nil {
		n.runtimes.watchFailed(h, id, attempt.token)
	}
}

// continueInRuntime is a message to a Claude session not running anywhere on a machine whose engine is a runtime: the
// runtime takes the session over, and the message goes to it there (the caller keeps the echo, as for any send).
func (n *Node) continueInRuntime(ctx context.Context, id, text string, images []proto.Image, clientMsgID string) error {
	h := n.engineRuntime(ctx)
	if h == nil {
		return n.engineRuntimeAway(ctx)
	}
	if _, err := n.takeOverInRuntime(ctx, h, runtimeStartParams{Resume: id}); err != nil {
		return err
	}
	cctx, cancel := context.WithTimeout(ctx, runtimeCallTimeout)
	defer cancel()
	err := h.peer.Call(cctx, "session.send", map[string]any{"session_id": id, "text": text, "images": images, "client_msg_id": clientMsgID}, nil)
	if err != nil && !isRPCError(err) {
		err = fmt.Errorf("%s: %w", h.label(), err)
	}
	return err
}

// liveRuntimeModels asks runtime h, the one offering launcher name to the start in question (runtimeOfIn), for its
// current native catalog when it opts in. Older runtimes keep their advertised list.
func (n *Node) liveRuntimeModels(ctx context.Context, h *runtimeConn, name, requestedLauncher string, refresh bool, fallback proto.LauncherModels) (proto.LauncherModels, error) {
	if h == nil {
		return fallback, nil
	}
	h.mu.Lock()
	supported := h.hello.Caps.Models
	h.mu.Unlock()
	if !supported {
		return fallback, nil
	}
	params := map[string]any{"launcher": name, "refresh": refresh}
	if h.agent() == agyAgent {
		ch, err := n.agyChannelFor(ctx, cmp.Or(requestedLauncher, name))
		if err != nil {
			return proto.LauncherModels{}, err
		}
		if ch != nil {
			params["channel"] = ch
		}
	}
	ctx, cancel := context.WithTimeout(ctx, runtimeCallTimeout)
	defer cancel()
	var out proto.LauncherModels
	if err := h.peer.Call(ctx, "launchers.models", params, &out); err != nil {
		return proto.LauncherModels{}, err
	}
	// Apply the same bounds as runtime.launchers before exposing a native response.
	clean := cleanLaunchers([]runtimeLauncher{{Name: name, Agent: fallback.Agent, Models: out.Models}})
	out.Agent, out.Models = fallback.Agent, clean[0].Models
	out.SupportsStartModel = false // only the node's native omp start can advertise this.
	if out.Models == nil {
		out.Models = []proto.AgentModel{}
	}
	return out, nil
}

// runtimeLauncherCheck opts into the authenticated availability probe of runtime h, the one offering launcher name to
// the start in question (runtimeOfIn). A refused probe never falls back to the login shell, whose credentials may be
// different. Older runtimes keep the existing shell check.
func (n *Node) runtimeLauncherCheck(ctx context.Context, h *runtimeConn, name, cwd, prompt, model string) (proto.LauncherCheck, bool) {
	if h == nil {
		return proto.LauncherCheck{}, false
	}
	h.mu.Lock()
	supported := h.hello.Caps.Check
	h.mu.Unlock()
	if !supported {
		return proto.LauncherCheck{}, false
	}
	ctx, cancel := context.WithTimeout(ctx, launcherCheckTimeout)
	defer cancel()
	start := time.Now()
	var result proto.LauncherCheck
	params := map[string]any{"launcher": name, "cwd": cwd, "prompt": prompt}
	if model != "" {
		params["model"] = model
	}
	if h.agent() == agyAgent {
		ch, err := n.agyChannelFor(ctx, name)
		if err != nil {
			result = proto.LauncherCheck{Agent: agyAgent, Error: textfmt.LauncherErrorSummary(err.Error(), 400), Millis: int(time.Since(start).Milliseconds())}
			return result, true
		}
		if ch != nil {
			params["channel"] = ch
		}
	}
	if err := h.peer.Call(ctx, "launchers.check", params, &result); err != nil {
		result = proto.LauncherCheck{Error: err.Error()}
	}
	// The runtime's CLI may print colour and shell integration like any other: the record and the apps read it clean.
	if result.Error != "" {
		result.Error = textfmt.LauncherErrorSummary(result.Error, 400)
	}
	result.Answer = textfmt.StripTerminalEscapes(result.Answer)
	launcher, _ := h.launcherNamed(name)
	result.Agent = launcher.Agent
	if result.Agent == "" {
		result.Agent = h.agent()
	}
	result.Millis = int(time.Since(start).Milliseconds())
	return result, true
}
