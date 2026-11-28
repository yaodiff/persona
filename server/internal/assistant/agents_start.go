package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// What a new or resumed Agent runs on and what its tab is called (docs/assistant/model-config.md "Choosing a model for a new Agent"): the
// manager's. The hub asks before it starts one the app asked for (agents.start_params), before a message resumes one
// nothing says the launcher of (agents.resume_params), and for a tab's name (agents.tab_name: an Agent started in the
// background, the Feishu card's); the assistant's own starts (agents_start, a task's agents) come here directly.

// The Agents business's names on the bus (assistantwire/agents.go).
type (
	WorkReport        = assistantwire.WorkReport
	SessionWork       = assistantwire.SessionWork
	AgentStartParams  = assistantwire.AgentStartParams
	AgentResumeParams = assistantwire.AgentResumeParams
	SessionSeen       = assistantwire.SessionSeen
)

const (
	MethodAgentStartParams  = assistantwire.MethodAgentStartParams
	MethodAgentResumeParams = assistantwire.MethodAgentResumeParams
	MethodAgentTabName      = assistantwire.MethodAgentTabName
	EventSessionSeen        = assistantwire.EventSessionSeen
	CodeGone                = assistantwire.CodeGone
)

// AgentStartParams is agents.start_params: the start's params with what the Agent runs on (Model Settings › Agents, over it
// the launcher, model and effort the app picked for this start) and, when asked, its tab's name. A resumed session
// keeps what it runs on (the node knows). A model configuration that cannot run is the app's to hear (bad_request).
func (s *Service) AgentStartParams(ctx context.Context, p AgentStartParams) (map[string]any, error) {
	params := p.Params
	if params == nil {
		params = map[string]any{}
	}
	delete(params, "external")
	if launcher, _ := params["launcher"].(string); proto.IsExternalLauncher(launcher) {
		return nil, badRequest(i18n.Err("external-adapter.purpose_unsupported"), s.appLang(ctx, p.Lang))
	}
	if resume, _ := params["resume_session_id"].(string); resume != "" {
		if err := checkStartSpeed(params, startAgent{}, resume); err != nil {
			return nil, badRequest(err, s.appLang(ctx, p.Lang))
		}
		clearStandardSpeed(params)
	} else {
		if model, _ := params["model"].(string); model != "" {
			params["explicit_model"] = model
		}
		tierCtx, _ := withTierStart(ctx, p.MachineID)
		own, err := s.resolveStart(tierCtx, params, false)
		if err != nil {
			return nil, badRequest(err, s.appLang(ctx, p.Lang)) // a typed model that is no id among them
		}
		if params["launch"] != "engine" {
			return nil, badRequest(fmt.Errorf("purpose %q requires engine launch for session.start", "agent.new"), s.appLang(ctx, p.Lang))
		}
		// Held to what the computer has (#2640): a launcher picked that it lacks is refused, a default it lacks gives
		// way to one it has. A person's New Agent start: a default of Claude's desktop app opens in the app where the
		// computer has it (models_desktop.go).
		if m, ok := s.device(ctx, p.MachineID); ok {
			note, agent, err := s.fitNewLauncher(withPersonStart(ctx), m, own, params)
			if err != nil {
				if code, _ := relayErrCode(err); code == proto.CodeLauncherOutOfScope || code == proto.CodeLauncherOwnService {
					return nil, err
				}
				return nil, badRequest(err, s.appLang(ctx, p.Lang))
			}
			// With the computer's switch on, a start that keeps the default Claude Code or Codex opens as its Orca chat
			// (orca_chat_default.go, #17650).
			var routed string
			agent, routed = s.routeOrcaChat(ctx, m, own, params, agent)
			note = strings.TrimSpace(note + " " + routed)
			if err := checkStartSpeed(params, agent, ""); err != nil {
				return nil, badRequest(err, s.appLang(ctx, p.Lang))
			}
			if params["model_aliases"] != nil {
				raw, err := json.Marshal(params["model_aliases"])
				var aliases proto.ModelAliases
				if err != nil || json.Unmarshal(raw, &aliases) != nil {
					return nil, badRequest(fmt.Errorf("model_aliases must be an object of model IDs"), s.appLang(ctx, p.Lang))
				}
				if err := aliases.Validate(); err != nil {
					return nil, badRequest(err, s.appLang(ctx, p.Lang))
				}
				if err := checkAliasAgent(aliases, agent); err != nil {
					return nil, badRequest(err, s.appLang(ctx, p.Lang))
				}
			}
			if own.tier != "" {
				if err := checkHostLauncher(params, agent); err != nil {
					return nil, badRequest(err, s.appLang(ctx, p.Lang))
				}
			}
			if mode, _ := params["permission_mode"].(string); mode != "" && own.permission == "" && !agent.supportsPermissionMode(mode) {
				delete(params, "permission_mode")
			}
			leaveLauncherNote(ctx, note) // the app says so (agents_bus.go launcher_note, #4661)
			// AGY returns a session only after startup, including for a launcher resolved from Model settings.
			if declared, _ := params["agent_type"].(string); declared == "" && agent.agent == "agy" {
				params["agent_type"] = agent.agent
			}
		}
		if name, _ := params["launcher"].(string); name != "" {
			if err := s.launcherScopeErr(ctx, p.MachineID, name); err != nil {
				return nil, err // a provider out of this computer's scope (models_launchers_scope.go)
			}
		}
	}
	delete(params, "launcher_default_effort")
	clearStandardSpeed(params)
	s.setPlanThen(ctx, p.MachineID, params)
	if name, _ := params["tab_name"].(string); strings.TrimSpace(name) == "" && p.NameFrom != "" {
		// Start locally; agents.tab_name_started asks the model after the hub has the session identity.
		params["tab_name"] = localTabName(p.NameFrom)
	}
	return params, nil
}

// AgentResumeParams is agents.resume_params: a message that resumes an Agent started outside Persona (nothing on its
// machine says its launcher) goes on Model Settings › Agents' launcher and model rather than plain claude, which may not be
// signed in there (node/sendsafe.go resumeLauncher). When that cannot run as a launcher the message goes as it was.
func (s *Service) AgentResumeParams(ctx context.Context, p AgentResumeParams) (map[string]any, error) {
	params := p.Params
	if params == nil {
		params = map[string]any{}
	}
	delete(params, "external")
	if launcher, _ := params["launcher"].(string); proto.IsExternalLauncher(launcher) {
		return nil, badRequest(i18n.Err("external-adapter.purpose_unsupported"), s.requestCtxLang(ctx))
	}
	if err := checkStartSpeed(params, startAgent{}, p.SessionID); err != nil {
		return nil, badRequest(err, s.requestCtxLang(ctx))
	}
	clearStandardSpeed(params)
	q := map[string]any{}
	if err := s.applyModel(ctx, "agent.new", q); err != nil {
		s.logf("resume %s: 新建 Agent's launcher not applied: %v", p.SessionID, err)
		return params, nil
	}
	// One the computer lacks gives way to one it has (#2640; a default is never refused).
	if m, ok := s.device(ctx, p.MachineID); ok {
		if _, _, err := s.fitLauncher(ctx, m, startPick{}, q); err != nil {
			s.logf("resume %s: the New Agent launcher not applied: %v", p.SessionID, err)
			return params, nil
		}
	}
	if name, _ := q["launcher"].(string); s.launcherScopeErr(ctx, p.MachineID, name) != nil {
		return params, nil // out of this computer's scope: the message goes as it was
	}
	for _, k := range []string{"launcher", "model", "effort", "model_fingerprint"} {
		if v, ok := q[k]; ok {
			params[k] = v
		}
	}
	return params, nil
}

// tabNameSyncTimeout is the agent.title budget on the paths that still answer a caller: the
// agents.tab_name bus method and the autonameTabs batch. It stays at the original eight seconds;
// only the detached start-naming job uses the longer agentTitleAsyncTimeout. A var so tests can
// compress it.
var tabNameSyncTimeout = 8 * time.Second

// tabName is tabNameWithTimeout with the short synchronous budget.
func (s *Service) tabName(ctx context.Context, prompt string) string {
	return s.tabNameWithTimeout(ctx, prompt, tabNameSyncTimeout)
}

// tabNameWithTimeout asks agent.title for a short herdr tab name using the given model budget. ""
// when there is no message or the model fails: the node names it agent-<n>.
func (s *Service) tabNameWithTimeout(ctx context.Context, prompt string, timeout time.Duration) string {
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ""
	}
	if r := []rune(prompt); len(r) > 1500 {
		prompt = string(r[:1500])
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	// Reasoning models think first: 20 tokens came back empty. Thinking is off for this short tab name, and a
	// cut-off chain-of-thought line is rejected before tabSlug turns it into "we-need-answer-title".
	r, err := s.purposeModel("agent.title").Chat(cctx, ai.Request{
		System:          prompts.RenderTabName(),
		Messages:        []ai.Message{{Role: ai.RoleUser, Content: prompt}},
		MaxTokens:       200,
		DisableThinking: true,
	})
	if err != nil {
		if !ai.IsPaused(err) {
			s.logf("tab name: %v", err)
		}
		return ""
	}
	cleaned := stripTitleThinking(r.Text)
	if titleReplyIsThinkingLeak(cleaned) {
		s.logf("tab name: dropped thinking leakage %q", clip(oneLine(r.Text), 80))
		return ""
	}
	name := tabSlug(cleaned)
	if name == "" {
		s.logf("tab name: nothing usable in %q", clip(oneLine(r.Text), 80))
	}
	return name
}

// nameStartedAgent records the dispatched Agent's initial Persona title. Use the actual tab label when the host
// made the requested name unique. A later CLI or Persona rename can replace it.
func (s *Service) nameStartedAgent(ctx context.Context, machine string, c proto.Session, requested string) string {
	if requested == "" || c.SessionID == "" {
		return requested
	}
	title := cmp.Or(c.HerdrTabName(), requested)
	if err := s.putInitialAgentTitle(ctx, machine, c.SessionID, title); err != nil {
		s.logf("dispatch: save session title for %q/%q: %v", machine, c.SessionID, err)
	} else {
		s.publishSessions(machine)
	}
	return title
}

// localTabName starts an Agent without a model call. The existing fixed placeholder covers prompts with no ASCII
// words and, unlike a meaningful local slug, remains eligible for the ordinary background naming pass if a late event is lost.
func localTabName(prompt string) string { return cmp.Or(tabSlug(prompt), "persona") }

// tabSlug keeps what a tab name may be: lowercase ASCII letters and digits, single hyphens, at most 24 characters.
func tabSlug(s string) string {
	if i := strings.IndexByte(strings.TrimSpace(s), '\n'); i > 0 {
		s = strings.TrimSpace(s)[:i]
	}
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if dash && b.Len() > 0 {
				b.WriteByte('-')
			}
			dash = false
			b.WriteRune(r)
		case r == '-' || r == '_' || r == ' ' || r == '/':
			dash = true
		}
		if b.Len() >= 24 {
			break
		}
	}
	return strings.Trim(b.String()[:min(b.Len(), 24)], "-")
}

// defaultTabName: a name nobody chose — the old fixed "persona", herdr's numbers, the agent-<n> fallback.
var defaultTabName = lazyre.New(`^(persona(-\d+)?|\d+|agent-\d+)?$`)

// orcaDefaultTabName is the name Orca gives every tab it opens without one (Orca 1.4.277): a name nobody chose too.
const orcaDefaultTabName = "Terminal"

// autonamedHosts are the hosts whose tabs autonameTabs renames (#17043: Orca's too).
var autonamedHosts = map[string]bool{"herdr": true, "orca": true}

// unchosenTabName: the session's tab has a name nobody chose, as its host names tabs.
func unchosenTabName(c proto.Session) bool {
	return defaultTabName.MatchString(c.SlotName) || c.SpaceKind == "orca" && c.SlotName == orcaDefaultTabName
}

// TabRename is one tab of POST /api/v1/tabs/autoname: renamed, or (dry run) what it would be called.
type TabRename struct {
	MachineID string `json:"machine_id"`
	SessionID string `json:"session_id"`
	Space     string `json:"space"`
	Title     string `json:"title"`
	From      string `json:"from"`
	To        string `json:"to,omitempty"`
	Error     string `json:"error,omitempty"`
}

// autonameTabs renames the herdr and Orca tabs of running agents whose names nobody chose (all of them with all) after
// their session's title, the way new tabs are named. One tab per agent; agents without a title keep theirs.
func (s *Service) autonameTabs(ctx context.Context, dryRun, all bool) []TabRename {
	type tab struct {
		machine string
		c       proto.Session
	}
	var todo []tab
	taken := map[string]map[string]bool{} // machine/workspace → names in use
	for _, d := range s.devices(ctx) {
		list, _ := s.sessions(ctx, d.ID)
		seen := map[string]bool{}
		for _, c := range list {
			if c.Assistant || !autonamedHosts[c.SpaceKind] || c.TabID == "" || c.Status == proto.StatusOffline || seen[c.TabID] {
				continue
			}
			seen[c.TabID] = true
			ws := d.ID + "/" + c.SpaceID
			if taken[ws] == nil {
				taken[ws] = map[string]bool{}
			}
			taken[ws][c.SlotName] = true
			if (all || unchosenTabName(c)) && strings.TrimSpace(c.Title) != "" {
				todo = append(todo, tab{d.ID, c})
			}
		}
	}
	// Names first (the model, a few at a time), then pick unique ones per space in a fixed order.
	names := make([]string, len(todo))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, t := range todo {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if names[i] = s.tabName(ctx, t.c.Title); names[i] == "" { // the small model comes back empty now and then
				names[i] = s.tabName(ctx, t.c.Title)
			}
		}()
	}
	wg.Wait()
	out := make([]TabRename, 0, len(todo))
	for i, t := range todo {
		r := TabRename{MachineID: t.machine, SessionID: t.c.SessionID, Space: t.c.SpaceName, Title: t.c.Title, From: t.c.SlotName}
		if names[i] == "" {
			r.Error = "no name from the model"
			out = append(out, r)
			continue
		}
		ws := taken[t.machine+"/"+t.c.SpaceID]
		delete(ws, t.c.SlotName) // its own old name is free again
		r.To = uniqueName(names[i], ws)
		ws[r.To] = true
		if !dryRun && r.To != r.From {
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			if err := s.call(cctx, t.machine, DeviceSessionRename, map[string]any{"session_id": t.c.SessionID, "tab_name": r.To}, nil); err != nil {
				r.Error = err.Error()
			}
			cancel()
		}
		out = append(out, r)
	}
	return out
}

// uniqueName is name, or name-2, name-3… when taken.
func uniqueName(name string, taken map[string]bool) string {
	if !taken[name] {
		return name
	}
	for i := 2; ; i++ {
		if n := fmt.Sprintf("%s-%d", name, i); !taken[n] {
			return n
		}
	}
}
