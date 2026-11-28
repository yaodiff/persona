package assistant

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

type browserDownloadExtension struct {
	ExtensionTools
	machine, session string
	args             json.RawMessage
}

func (*browserDownloadExtension) ConnectorTool(string) bool { return false }

func (e *browserDownloadExtension) CallTool(_ context.Context, machine, session, name string, args json.RawMessage, assistant bool) (proto.ToolResult, bool) {
	if name != "browser_fetch_download" || !assistant {
		return proto.ToolResult{}, false
	}
	e.machine, e.session, e.args = machine, session, args
	return proto.TextResult("received", false), true
}

func TestBrowserFetchDownloadUsesThePhysicalCaller(t *testing.T) {
	s, _ := newQuietService(t)
	probe := &browserDownloadExtension{}
	s.SetExtensions(probe)
	lease := &atomic.Bool{}
	lease.Store(true)
	ctx := context.WithValue(t.Context(), logicalAuthenticatedKey{}, logicalAuthenticated{
		Machine: "server", Session: "conv1", PhysicalMachine: "box", PhysicalSession: "switched-session", Lease: lease,
	})
	result := s.mcpCall(ctx, "server", "conv1", "browser_fetch_download", json.RawMessage(`{"machine":"browser","filename":"Browser report.bin"}`), true)
	if result.IsError || probe.machine != "box" || probe.session != "conv1" {
		t.Fatalf("the browser tool got machine=%q session=%q, result=%+v", probe.machine, probe.session, result)
	}
	var request map[string]string
	if err := json.Unmarshal(probe.args, &request); err != nil || request["machine"] != "browser" || request["filename"] != "Browser report.bin" {
		t.Fatalf("the browser tool got arguments %s: %v", probe.args, err)
	}
}
