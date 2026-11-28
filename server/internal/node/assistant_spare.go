package node

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/shellcmd"
)

// A spare process started when the app opens the "Chat" tab, so the first message skips claude's startup.
// It is only adopted while fresh (what it loaded at start — CLAUDE.md, memory, tools — may have changed since).
const assistantSpareMaxAge = 10 * time.Minute

func (n *Node) adoptSpareFor(cwd, launcher, mode string, options ...modelExecution) *assistantProc {
	if mode == proto.ModeBypass { // a Codex spare is fine: its policy goes with each turn
		n.mu.Lock()
		sp := n.spare
		n.mu.Unlock()
		if sp == nil || sp.agent != "codex" {
			return nil
		}
	}
	return n.adoptSpare(cwd, launcher, options...)
}

// adoptSpare hands the warm spare process to a new conversation when it fits (same directory and launcher, fresh).
func (n *Node) adoptSpare(cwd, launcher string, options ...modelExecution) *assistantProc {
	n.mu.Lock()
	defer n.mu.Unlock()
	p := n.spare
	if p == nil || !p.alive() || time.Since(p.born) > assistantSpareMaxAge ||
		((p.agent == "claude" || p.agent == "codex") && p.personaMCPOnly != n.hubSupportsPersonaContracts()) ||
		(launcher != "" && p.launcher != launcher) || (cwd != "" && p.cwd != cwd) || p.fingerprint != n.executionFingerprint(p.launcher, executionOption(options)) ||
		n.toolSearchStale(p) || n.categoriesStaleSpareLocked(p) { // its endpoint refused tool search (#10365) or its directory's categories changed (#13246)
		return nil
	}
	// A Claude spare was generated with the ordinary built-in list. It cannot become an Inbox process without
	// reintroducing SendMessage outside the MCP origin policy; let the Inbox start cold with its restricted list.
	if (p.agent == "claude" && executionOption(options).Origin == originInbox) || p.agent == traexAgent {
		return nil
	}
	// A Claude start names the profile it resolved (assistantProfileSelection), Persona's own included; a spare warmed in
	// another one, as before the main chat's profile choice changed, is not it. A Codex one has no profile.
	if p.agent == "claude" && !sameAssistantProfile(p.model.ConfigDir, executionOption(options).ConfigDir) {
		return nil
	}
	n.spare = nil
	n.procs[p.id] = p
	return p
}

// assistantWarm makes sure a fresh spare process is waiting for the next new conversation.
func (n *Node) assistantWarm(cwd, launcher string, options ...modelExecution) error {
	if proto.IsExternalLauncher(launcher) {
		return externalRefusal(context.Background(), "adapter_purpose_unsupported")
	}
	if n.assistantRuntime(launcher) != nil { // a runtime is running already: nothing to start ahead
		if executionOption(options).ConfigDir != "" {
			return errors.New(assistantwire.ConfigDirClaudeOnly)
		}
		return nil
	}
	execution := executionOption(options)
	if err := execution.checkModel(); err != nil {
		return err
	}
	if launcher == "" || !shellcmd.NameRe.MatchString(launcher) {
		return fmt.Errorf("invalid launcher %q", launcher)
	}
	if err := n.validAssistantModes(context.Background(), launcher, "", execution.Effort); err != nil {
		return err
	}
	if err := n.ensureManaged(context.Background(), launcher); err != nil {
		return err
	}
	// The main chat's spare starts with its window as the main conversation's start carries it (assistantSend, #7314).
	execution.MainCompactWindow = n.mainWindowOn(launcher, execution.MainCompactWindow)
	if agent, err := n.assistantAgent(context.Background(), launcher); err != nil {
		return err
	} else if agent == "claude" && execution.Interactive {
		// No spare for interactive Claude Code (assistant_tui.go): it would start before its conversation's mode is
		// known, and one in print mode would be adopted by a conversation that asked for interactive.
		n.mu.Lock()
		old := n.spare
		n.spare = nil
		n.mu.Unlock()
		if old != nil {
			old.stop()
		}
		return nil
	} else if agent == "claude" {
		execution.ConfigDir, err = n.assistantProfileSelection(context.Background(), "", launcher, execution.ConfigDir, false)
		if err != nil {
			return err
		}
	} else if execution.ConfigDir != "" {
		return errors.New(assistantwire.ConfigDirClaudeOnly)
	}
	n.mu.Lock()
	old := n.spare
	if old != nil && old.alive() && old.launcher == launcher && old.cwd == cwd &&
		((old.agent != "claude" && old.agent != "codex") || old.personaMCPOnly == n.hubSupportsPersonaContracts()) &&
		(old.agent != "claude" || sameAssistantProfile(old.model.ConfigDir, execution.ConfigDir)) &&
		old.fingerprint == n.executionFingerprint(launcher, execution) && time.Since(old.born) < assistantSpareMaxAge/2 &&
		!n.toolSearchStale(old) && !n.categoriesStaleSpareLocked(old) { // one adoptSpare would refuse: replaced (#10365, #13246)
		n.mu.Unlock()
		return nil
	}
	n.spare = nil
	n.mu.Unlock()
	if old != nil {
		old.stop()
	}
	p, err := n.spawnAssistant("", cwd, launcher, false, true, execution)
	if err != nil {
		return err
	}
	n.mu.Lock()
	if n.spare != nil { // a concurrent warm won
		n.mu.Unlock()
		p.stop()
		return nil
	}
	n.spare = p
	n.mu.Unlock()
	return nil
}
