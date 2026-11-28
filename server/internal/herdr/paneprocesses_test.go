package herdr

import (
	"context"
	"slices"
	"testing"
)

// Claude Code comes first, whatever herdr lists first (#14681). herdr 0.9.5 names a process by its argv (no argv0) and
// Claude Code's by its version, and a launcher that runs claude under a wrapper makes the wrapper the foreground group's
// leader, here one that lives in a folder named after claude. A herdr that still sends argv0 is read as before.
func TestPaneProcessesPutsClaudeCodeFirst(t *testing.T) {
	front := map[string]string{
		// herdr 0.9.5: a wrapper first, then Claude Code and its MCP server
		"w1:p1": `{"pid":100,"name":"wrapper","argv":["/opt/claude-tools/bin/wrapper","run","--","--session-id","s1"]},` +
			`{"pid":101,"name":"2.1.330","argv":["/home/me/.local/bin/claude","--session-id","s1"]},` +
			`{"pid":102,"name":"personad","argv":["/home/me/.persona/bin/personad","mcp"]}`,
		// a herdr that sends argv0
		"w1:p2": `{"pid":200,"name":"zsh","argv0":"-zsh"},{"pid":201,"name":"2.1.312","argv0":"claude"}`,
		// a wrapper script named claude in front of Claude Code
		"w1:p3": `{"pid":300,"name":"claude","argv":["/bin/sh","/home/me/bin/claude"]},` +
			`{"pid":301,"name":"2.1.330","argv":["/home/me/.local/bin/claude"]}`,
		// no Claude Code: herdr's order
		"w1:p4": `{"pid":400,"name":"codex","argv":["codex"]},{"pid":401,"name":"node","argv":["node","server.js"]}`,
	}
	sock := fakeServer(t, func(method string, params map[string]any) string {
		pane, _ := params["pane_id"].(string)
		procs, ok := front[pane]
		if method != "pane.process_info" || !ok {
			return `"error":{"code":"pane_not_found","message":"pane not found"}`
		}
		return `"result":{"type":"pane_process_info","process_info":{"pane_id":"` + pane + `","foreground_processes":[` + procs + `]}}`
	})
	c := &Client{Bin: "/nonexistent/herdr", Socket: sock}
	for pane, want := range map[string][]int{"w1:p1": {101, 100, 102}, "w1:p2": {201, 200}, "w1:p3": {301, 300}, "w1:p4": {400, 401}} {
		got, err := c.PaneProcesses(context.Background(), pane)
		if err != nil || !slices.Equal(got, want) {
			t.Errorf("%s: PaneProcesses = %v, %v; want %v", pane, got, err, want)
		}
	}
}
