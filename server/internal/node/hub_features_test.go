package node

import (
	"testing"

	"github.com/nwverse/persona/server/internal/nodeproto"
)

func TestHubFeaturesConfiguration(t *testing.T) {
	n, _ := testNode(t)
	if n.hubSupportsPersonaContracts() {
		t.Fatal("a node without a hub announcement must keep legacy aliases")
	}
	if n.hubMCPWait.Load() {
		t.Fatal("a node without a hub announcement must keep single-response MCP calls")
	}
	for _, tt := range []struct {
		name          string
		params        map[string]any
		wantContracts bool
		wantMCPWait   bool
	}{
		{"current hub", map[string]any{"machine_id": "computer", "hub_features": []string{nodeproto.PersonaContracts, nodeproto.MCPWait}}, true, true},
		{"partial configuration", map[string]any{"keep_sessions": []string{}}, true, true},
		{"older hub with contracts only", map[string]any{"hub_features": []string{nodeproto.PersonaContracts}}, true, false},
		{"old hub after reconnect", map[string]any{"machine_id": "computer", "hub_features": []string{}}, false, false},
		{"current hub again", map[string]any{"hub_features": []string{nodeproto.PersonaContracts, nodeproto.MCPWait}}, true, true},
		{"old manager handover", map[string]any{"machine_id": "computer"}, false, false},
		{"unknown feature", map[string]any{"hub_features": []string{"future_contracts"}}, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := nodeCall(t, n, "node.configure", tt.params); err != nil {
				t.Fatal(err)
			}
			if got := n.hubSupportsPersonaContracts(); got != tt.wantContracts {
				t.Fatalf("hubSupportsPersonaContracts = %v, want %v", got, tt.wantContracts)
			}
			if got := n.hubMCPWait.Load(); got != tt.wantMCPWait {
				t.Fatalf("hubMCPWait = %v, want %v", got, tt.wantMCPWait)
			}
		})
	}
}
