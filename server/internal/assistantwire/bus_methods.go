package assistantwire

import (
	"encoding/json"

	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/proto"
)

// ToolsResult is the tool catalog returned by assistant.tools.
type ToolsResult struct {
	Tools []proto.Tool `json:"tools"`
}

var (
	SnapshotMethod = buswire.Method[Snapshot, SnapshotResult]{Name: MethodSnapshot}
	ToolsMethod    = buswire.Method[SessionRef, ToolsResult]{Name: MethodTools}
	ToolMethod     = buswire.Method[ToolCall, proto.ToolResult]{Name: MethodTool}
	ContextMethod  = buswire.Method[ContextParams, ContextResult]{Name: MethodContext}
	ActionMethod   = buswire.Method[Action, ActionResult]{Name: MethodAction}

	DeviceMethod       = buswire.Method[DeviceCall, json.RawMessage]{Name: MethodDevice}
	DeviceRetireMethod = buswire.Method[DeviceCall, json.RawMessage]{Name: MethodDeviceRetire}
	AppMethod          = buswire.Method[AppCall, json.RawMessage]{Name: MethodApp}
	HTTPMethod         = buswire.Method[HTTPRequest, HTTPResponse]{Name: MethodHTTP}
	HTTPStreamedMethod = buswire.Method[HTTPRequest, HTTPResponse]{Name: MethodHTTPStreamed}
)
