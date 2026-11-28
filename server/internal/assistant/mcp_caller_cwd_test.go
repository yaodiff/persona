package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

type firstToolsTimingHub struct {
	Hub
	beforeCall func(context.Context, DeviceCall) (json.RawMessage, error)
}

func (h *firstToolsTimingHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	return h.beforeCall(ctx, p)
}

// Exercise the real Start before assistant.send returns or sessions.changed
// arrives. The fake node returns the requested prepare directory and later
// publishes only Cwd, just as assistantSessions does; it never supplies Space.
func TestSpaceFirstToolsBeforeSessionPublication(t *testing.T) {
	for _, space := range []string{SpaceHealth, "", "qa-plain"} {
		t.Run("space="+space, func(t *testing.T) {
			ctx := context.Background()
			s, hub, _ := spaceMDService(t)
			devices := s.devs.(spaceDevs).fleetDevs
			const sid = "00000000-0000-4000-8000-000000000123"
			var cwd, revision string
			queried := false
			s.hub = &firstToolsTimingHub{Hub: hub, beforeCall: func(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
				if p.Method == DeviceAssistantPrepare {
					var in struct {
						Dir string `json:"dir"`
					}
					if err := json.Unmarshal(p.Params, &in); err != nil {
						return nil, err
					}
					return json.Marshal(map[string]any{"cwd": hub.devices[0].Home + "/" + in.Dir})
				}
				if p.Method != DeviceAssistantSend {
					return hub.DeviceCall(ctx, p)
				}
				var in struct {
					Cwd string `json:"cwd"`
				}
				if err := json.Unmarshal(p.Params, &in); err != nil {
					return nil, err
				}
				cwd, queried = in.Cwd, true
				if _, ok := s.session(ctx, "server", sid); ok {
					t.Fatal("session published before first discovery")
				}
				if got := s.agentCallerSpace(ctx, "server", sid); got != "" {
					t.Fatalf("old node acquired caller space %q", got)
				}
				if _, known := s.selfSpace(ctx, sid); known {
					t.Fatal("old node acquired a known selfSpace")
				}
				old := privacyRequest(t, s, "server", "mcp.tools", map[string]any{"session_id": sid, "assistant": true}).(map[string]any)
				if toolListed(old["tools"].([]proto.Tool), spaceStateTool) {
					t.Fatal("old node gained a space without evidence")
				}
				out := privacyRequest(t, s, "server", "mcp.tools", map[string]any{"session_id": sid, "assistant": true, "caller_cwd": cwd}).(map[string]any)
				tools := out["tools"].([]proto.Tool)
				if !toolListed(tools, "ask_user") {
					t.Fatal("startup admission refused assistant tools")
				}
				if toolListed(tools, spaceStateTool) != (space == "qa-plain") {
					t.Fatalf("first tools for %q: %+v", space, tools)
				}
				revision = out["revision"].(string)
				attested := withNodeCallerCwd(ctx, "server", sid, cwd)
				if got := s.agentCallerSpace(attested, "server", sid); got != space {
					t.Fatalf("caller space = %q, want %q", got, space)
				}
				if got, ok := s.selfSpace(attested, sid); !ok || got != space {
					t.Fatalf("selfSpace = %q, %v", got, ok)
				}
				if toolListed(s.ToolsFor(attested, "server", sid), spaceStateTool) != (space == "qa-plain") {
					t.Fatal("targeted ToolsFor disagrees with node list")
				}
				if s.healthOn() {
					call := privacyRequest(t, s, "server", "mcp.call", map[string]any{"session_id": sid, "assistant": true, "caller_cwd": cwd, "name": "health_intake", "args": map[string]any{}}).(proto.ToolResult)
					denied := len(call.Content) > 0 && strings.Contains(call.Content[0].Text, "known Health conversation")
					if denied != (space != SpaceHealth) {
						t.Fatalf("Health call boundary for %q: %+v", space, call)
					}
					if space == SpaceHealth && (!call.IsError || len(call.Content) == 0 || call.Content[0].Text != "action must be log, list, delete or add_item") {
						t.Fatalf("Health call did not reach intake argument validation: %+v", call)
					}
					legacy := privacyRequest(t, s, "server", "mcp.call", map[string]any{"session_id": sid, "assistant": true, "name": "health_intake", "args": map[string]any{}}).(proto.ToolResult)
					if !legacy.IsError || len(legacy.Content) == 0 || !strings.Contains(legacy.Content[0].Text, "known Health conversation") {
						t.Fatalf("old node bypassed the unknown-space boundary: %+v", legacy)
					}
				}
				return json.Marshal(map[string]any{"session_id": sid})
			}}
			c, err := s.Start(ctx, StartParams{Text: "Synthetic request", Space: space})
			if err != nil {
				t.Fatal(err)
			}
			if !queried || c.SessionID != sid || cwd == "" || revision == "" {
				t.Fatalf("Start not exercised: %+v, cwd=%q", c, cwd)
			}
			devices.setSessions("server", []proto.Session{{SessionID: sid, Cwd: cwd, Assistant: true}})
			if got := s.agentCallerSpace(ctx, "server", sid); got != space {
				t.Fatalf("published space = %q", got)
			}
			if toolListed(s.ToolsFor(ctx, "server", sid), spaceStateTool) != (space == "qa-plain") {
				t.Fatal("hub list after publication disagrees")
			}
			// The authoritative row overrides a stale or contradictory directory.
			conflictingCwd := "/home/u/.persona/health"
			if space == SpaceHealth {
				conflictingCwd = "/home/u/.persona/assistant"
			}
			attested := withNodeCallerCwd(ctx, "server", sid, conflictingCwd)
			if got := s.agentCallerSpace(attested, "server", sid); got != space {
				t.Fatalf("known caller space overridden: %q", got)
			}
			if got, ok := s.selfSpace(attested, sid); !ok || got != space {
				t.Fatalf("known selfSpace overridden: %q, %v", got, ok)
			}
			// Reusing the first list's cursor still long-polls and cancellation
			// returns unchanged, even once the session row has arrived.
			watch, cancel := context.WithCancel(ctx)
			cancel()
			raw, _ := json.Marshal(map[string]any{"session_id": sid, "assistant": true, "caller_cwd": cwd, "revision": revision})
			out, handled, err := s.NodeRequest(watch, "server", "mcp.tools", raw)
			if err != nil || !handled {
				t.Fatalf("watch: %v, handled=%v", err, handled)
			}
			result := out.(map[string]any)
			if result["unchanged"] != true || result["revision"] != revision {
				t.Fatalf("watch lost its cursor: %+v", result)
			}
		})
	}
}

func TestSpaceCallerCwdIsBoundToNodeCaller(t *testing.T) {
	s, _, _ := spaceMDService(t)
	ctx := context.Background()
	attested := withNodeCallerCwd(ctx, "server", "new", "/home/u/.persona/health/notes")
	if got := s.agentCallerSpace(attested, "server", "new"); got != SpaceHealth {
		t.Fatalf("subdirectory space = %q", got)
	}
	for _, ref := range []SessionRef{{MachineID: "server", SessionID: "other"}, {MachineID: "other", SessionID: "new"}, {MachineID: "server"}, {SessionID: "new"}} {
		if got := s.agentCallerSpace(attested, ref.MachineID, ref.SessionID); got != "" {
			t.Fatalf("evidence escaped caller binding: %+v = %q", ref, got)
		}
	}
	// assistant.tools and assistant.tool are hub/extension entry points, not
	// authenticated node requests. Unknown same-named JSON fields stay inert.
	helloed := make(chan struct{})
	close(helloed)
	b := &BusHub{svc: s, helloed: helloed}
	raw := json.RawMessage(`{"machine_id":"server","session_id":"new","caller_cwd":"/home/u/.persona/tracks/qa-plain"}`)
	out, err := b.request(ctx, assistantwire.MethodTools, raw)
	if err != nil {
		t.Fatal(err)
	}
	if toolListed(out.(assistantwire.ToolsResult).Tools, spaceStateTool) {
		t.Fatal("hub payload attested a node directory")
	}
	if s.healthOn() {
		raw = json.RawMessage(`{"machine_id":"server","session_id":"new","caller_cwd":"/home/u/.persona/health","name":"health_intake","arguments":{}}`)
		out, err = b.request(ctx, assistantwire.MethodTool, raw)
		if err != nil {
			t.Fatal(err)
		}
		result := out.(proto.ToolResult)
		if !result.IsError || len(result.Content) == 0 || !strings.Contains(result.Content[0].Text, "known Health conversation") {
			t.Fatalf("hub payload bypassed Health boundary: %+v", result)
		}
	}
}

// A request which now has process evidence must not reuse the earlier ordinary
// list, even when its session identity and task-link cursor have not changed.
func TestSpaceToolsWatchRechecksCallerDirectory(t *testing.T) {
	s, _, _ := spaceMDService(t)
	finish := s.beginAssistantStart("server")
	defer finish()
	query := map[string]any{"session_id": "new", "assistant": true}
	old := privacyRequest(t, s, "server", "mcp.tools", query).(map[string]any)
	query["revision"] = old["revision"]
	query["caller_cwd"] = "/home/u/.persona/tracks/qa-plain"
	fresh := privacyRequest(t, s, "server", "mcp.tools", query).(map[string]any)
	if fresh["revision"] == old["revision"] || fresh["unchanged"] == true || !toolListed(fresh["tools"].([]proto.Tool), spaceStateTool) {
		t.Fatalf("new directory evidence reused an ordinary list: %+v", fresh)
	}
}

func TestSpaceToolsDiscoveryKeepsListAndCursorOnSameSnapshot(t *testing.T) {
	s, _, _ := spaceMDService(t)
	devices := s.devs.(spaceDevs).fleetDevs
	ctx := withNodeCallerCwd(context.Background(), "server", "new", "/home/u/.persona/health")
	listing := s.mcpDiscoveryContext(ctx, "server", "new", true)
	before := s.guardDiscoveryCursor(listing, "server", "new", true, "cursor")
	devices.setSessions("server", []proto.Session{{SessionID: "new", Cwd: "/home/u/.persona/tracks/qa-plain", Assistant: true}})
	if s.agentCallerSpace(listing, "server", "new") != SpaceHealth || len(s.spaceTools(listing, "server", "new")) != 0 || s.guardDiscoveryCursor(listing, "server", "new", true, "cursor") != before {
		t.Fatal("a row arriving during discovery changed the list or its cursor")
	}
	fresh := s.mcpDiscoveryContext(ctx, "server", "new", true)
	if s.agentCallerSpace(fresh, "server", "new") != "qa-plain" || len(s.spaceTools(fresh, "server", "new")) == 0 || s.guardDiscoveryCursor(fresh, "server", "new", true, "cursor") == before {
		t.Fatal("the next discovery did not use the authoritative row")
	}
}
