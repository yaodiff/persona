package node

import (
	"context"

	"github.com/nwverse/persona/server/internal/host"
)

// A tab a start opens runs the user's shell, and its startup files run before anything the node types: oh-my-zsh's
// "Would you like to update? [Y/n]" read its key while ~/.zshrc loaded, so the start waited on it and ended with
// shell_waiting (#2570), and every retry opened another tab that stopped on the same question (#10365). What the
// node types (agentenv.go) comes too late for that. So the tab's shell is given startShellEnv as it is made, where
// the engine takes an environment for a new shell (host.EnvLauncher: herdr's tab.create / workspace.create env, the
// Herdr App's terminal.run env, Orca's terminal.create env; Tern takes none yet): the Agent mark (agentMarkEnv,
// #10111) and the startup frameworks' own switches for their questions. The typed start line unsets the switches again
// (startupOnlyEnv) and keeps the mark, so the Agent and the shell the tab keeps run with the user's environment. Only
// start tabs get them: a terminal the user opens, a split, a preview's server never do.

// startShellEnv is the environment ("KEY=VALUE") of a start tab's shell: the Agent mark, oh-my-zsh's update check and
// compfix prompt off (DISABLE_AUTO_UPDATE wins over DISABLE_UPDATE_PROMPT; DISABLE_UPDATE_PROMPT alone would update
// without asking, a git pull in the tab, so they always go together; oh-my-bash reads DISABLE_AUTO_UPDATE too) and
// powerlevel10k's configuration wizard off (it would take the typed line as answers and rewrite ~/.zshrc). Nothing that
// changes what a shell may do.
var startShellEnv = []string{
	agentMarkEnv,
	"DISABLE_AUTO_UPDATE=true",
	"DISABLE_UPDATE_PROMPT=true",
	"ZSH_DISABLE_COMPFIX=true",
	"POWERLEVEL9K_DISABLE_CONFIGURATION_WIZARD=true",
}

// createStartSpace is launch.CreateSpace for a start's new tab: its shell starts with startShellEnv where the engine
// takes an environment for it (host.EnvLauncher).
func createStartSpace(ctx context.Context, launch host.Launcher, cwd, label string) (*host.Created, error) {
	if el, ok := launch.(host.EnvLauncher); ok {
		return el.CreateSpaceEnv(ctx, cwd, label, startShellEnv)
	}
	return launch.CreateSpace(ctx, cwd, label)
}

// createStartSlot is launch.CreateSlot for a start's new tab, as createStartSpace.
func createStartSlot(ctx context.Context, launch host.Launcher, space, cwd, label string) (*host.Created, error) {
	if el, ok := launch.(host.EnvLauncher); ok {
		return el.CreateSlotEnv(ctx, space, cwd, label, startShellEnv)
	}
	return launch.CreateSlot(ctx, space, cwd, label)
}
