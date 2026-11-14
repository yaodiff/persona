package hub

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/nodeproto"
)

func TestNodeConfigurationAnnouncesHubFeatures(t *testing.T) {
	h := newTestHub(t)
	m := newMachine(h, MachineConfig{ID: "computer"})
	reply := h.machineConnected(context.Background(), m, time.Now())
	var features []string
	if err := json.Unmarshal(reply.Configure["hub_features"], &features); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(features, h.features()) || !slices.Contains(features, nodeproto.PersonaContracts) ||
		!slices.Contains(features, nodeproto.MCPWait) {
		t.Fatalf("node configuration must announce the same capabilities as hello: %v", features)
	}
}
