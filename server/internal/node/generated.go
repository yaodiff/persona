package node

import (
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"

	"github.com/nwverse/persona/server/internal/assistantskills"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

// generatedFile is a file the node writes for the sessions it starts: Group is the unit the doctor reports on, Rel
// the path under the node's directory.
type generatedFile struct {
	Group   string
	Rel     string
	Content []byte
}

// generatedGroups are the groups of generatedFiles, then the skills plugin (agent-plugin, assistantskills).
var generatedGroups = []string{"claude-settings.json", "claude-hooks", "assistant-settings.json", "assistant-hooks", "mcp.json"}

const agentPluginGroup = "agent-plugin"

// cuaMCPMatcher is the PreToolUse matcher of Cua Driver's MCP tools, under whatever name with "cua" in it the
// user gave the server (cuaToolOf decides).
const cuaMCPMatcher = "mcp__.*cua.*__.*"

// hookSettings is the settings with Persona's hooks for a claude whose program is bin, of the node whose directory is
// dir: the agents' (assistant false) or the assistant's, which add the Feishu gate on Bash (larkgate.go). The hook
// names dir itself (rootAssign): Claude Code runs it with the claude's environment, which is its pane's (agentenv.go).
func hookSettings(bin, dir string, assistant bool) map[string]any {
	return hookSettingsForOS(runtime.GOOS, bin, dir, assistant)
}

func hookSettingsForOS(goos, bin, dir string, assistant bool) map[string]any {
	cmd := shellQuote(bin) + " hook"
	if dir != "" {
		cmd = rootAssign(dir, shellQuote) + cmd
	}
	hook := func(timeout int) map[string]any {
		h := map[string]any{"type": "command", "command": cmd, "timeout": timeout}
		if goos == "windows" {
			// Native hook argv avoids Git Bash consuming the quotes around Windows paths.
			// Keep the same node and gates, with its data directory passed explicitly.
			args := []string{"hook"}
			if dir != "" {
				args = append(args, nodeclient.HomeFlag, dir)
			}
			h["command"], h["args"] = bin, args
		}
		return h
	}
	blocking := func(matcher string) map[string]any {
		return map[string]any{"matcher": matcher, "hooks": []any{hook(86400)}}
	}
	notify := map[string]any{"hooks": []any{hook(10)}}
	postQuestion := map[string]any{"matcher": "AskUserQuestion", "hooks": notify["hooks"]}
	pre := []any{blocking("AskUserQuestion")}
	if assistant {
		pre = append(pre, blocking("Bash"))
	}
	if goos == "darwin" {
		// An Agent's own Cua Driver never operates System Settings or a permission prompt (cua_gate.go, #4349): its
		// MCP tools, and Bash commands running cua-driver. Decided at once, so a short timeout. The assistant's
		// Bash hook above takes its commands to the gate too; an Agent's goes to the node only when the command
		// mentions cua-driver (nodeclient.CuaOnlyFlag), so other Bash commands cost a process start and nothing else.
		quick := func(matcher, command string) map[string]any {
			h := hook(60)
			h["command"] = command
			return map[string]any{"matcher": matcher, "hooks": []any{h}}
		}
		pre = append(pre, quick(cuaMCPMatcher, cmd))
		if !assistant {
			pre = append(pre, quick("Bash", cmd+" "+nodeclient.CuaOnlyFlag))
		}
	}
	return map[string]any{"hooks": map[string]any{
		"PreToolUse":        pre,
		"PostToolUse":       []any{postQuestion},
		"PermissionRequest": []any{blocking("*")},
		"Elicitation":       []any{blocking("*")},
		"Notification":      []any{notify},
		"Stop":              []any{notify},
		"UserPromptSubmit":  []any{notify},
		"SessionStart":      []any{notify},
	}}
}

// displayHooks is the MessageDisplay hook of the Agents Persona starts, the live replies plugin's (hooksplugin.go,
// livereply.go, #1309), for the node whose directory is dir: Claude Code hands it each batch of whole lines of a reply
// as it shows them. It shows the lines only once the hook is done, whatever "async" says (2.1.319), so the hook just
// passes them to the node and prints nothing (nodeclient.Display, at most 150 ms after it started); the timeout
// (seconds, the shortest whole one) bounds one that hangs, after which Claude shows the lines as they are. It is in
// exec form ("command" the program, "args" its arguments): Claude Code starts it directly instead of through /bin/sh -c,
// which costs every batch 6–8 ms more on a Mac (docs/integrations/claude-integration.md "What the terminal pays").
// Every Claude Code with the event takes exec form (2.1.150 has exec form and not the event; 2.1.200 has both). With
// no shell to put PERSONA_HOME in front (hookSettings), the hook is told dir as an argument (nodeclient.HomeFlag).
// The headless assistant streams its replies itself and has none.
func displayHooks(bin, dir string) []any {
	args := []string{"hook"}
	if dir != "" {
		args = append(args, nodeclient.HomeFlag, dir)
	}
	return []any{map[string]any{"hooks": []any{map[string]any{"type": "command", "command": bin, "args": args, "async": true, "timeout": 1}}}}
}

// generatedFiles is every file writeGeneratedConfig writes under the node's directory dir (but the skills plugin),
// computed without writing. Each set of hooks is there twice: as a plugin for a claude that loads plugins, in a
// settings file for one that does not (hooksplugin.go). The hooks and the MCP server are told dir, as PERSONA_HOME.
// The Agents' live replies plugin is part of their hooks (claude-hooks), written whatever the setting says: a start
// loads it or not (liveRepliesPlugin).
func generatedFiles(bin, dir string) ([]generatedFile, error) {
	var out []generatedFile
	for _, kind := range []string{"claude", "assistant"} {
		settings := hookSettings(bin, dir, kind == "assistant")
		b, err := marshalJSON(settings)
		if err != nil {
			return nil, err
		}
		out = append(out, generatedFile{Group: kind + "-settings.json", Rel: kind + "-settings.json", Content: b})
		files, err := hooksPluginFiles(settings["hooks"])
		if err != nil {
			return nil, err
		}
		for _, rel := range slices.Sorted(maps.Keys(files)) {
			out = append(out, generatedFile{Group: kind + "-hooks", Rel: filepath.Join(kind+"-hooks", rel), Content: files[rel]})
		}
		if kind != "claude" {
			continue
		}
		live, err := liveRepliesPluginFiles(bin, dir)
		if err != nil {
			return nil, err
		}
		for _, rel := range slices.Sorted(maps.Keys(live)) {
			out = append(out, generatedFile{Group: kind + "-hooks", Rel: filepath.Join(liveRepliesDir, rel), Content: live[rel]})
		}
	}
	server := map[string]any{"command": bin, "args": []string{"mcp"}}
	if dir != "" {
		server["env"] = map[string]string{"PERSONA_HOME": dir}
	}
	// This shared file also serves automatic Agent resumes, before any hub handshake.
	// Keep both namespaces here so historical tool calls remain available. Assistant
	// starts choose their own per-process registration after capability negotiation.
	mcp, err := marshalJSON(map[string]any{"mcpServers": compat.MCPRegistrationServers(map[string]any{"persona": server}, true)})
	if err != nil {
		return nil, err
	}
	return append(out, generatedFile{Group: "mcp.json", Rel: "mcp.json", Content: mcp}), nil
}

// writeGeneratedConfig writes the hooks, MCP config and skills plugin that sessions Persona starts load into dir,
// for a claude whose hook program is bin: what the node does at each start, and what `node doctor --fix` does.
func writeGeneratedConfig(dir, bin string) error {
	files, err := generatedFiles(bin, dir)
	if err != nil {
		return err
	}
	for _, f := range files {
		path := filepath.Join(dir, f.Rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := writeFileAtomic(path, f.Content); err != nil {
			return err
		}
	}
	return assistantskills.WritePlugin(filepath.Join(dir, agentPluginGroup)) // the skills the agents Persona starts load (agentplugin.go)
}
