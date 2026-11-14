package conformance_test

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
)

// What the hub advertises at initialize and the capabilities the schema (and so the generated SDKs) list stay the
// same set: an extension reading wire.HubCapabilities to decide what this hub does must see what HubCapabilities
// really answers, and a capability the hub answers without a schema row is one an extension's author cannot name.
func TestHubCapabilitySchemaIsTheHubs(t *testing.T) {
	s := loadSchemas(t)
	raw, ok := s.def(t, "HubCapability")["enum"].([]any)
	if !ok || len(raw) == 0 {
		t.Fatal("the schema's HubCapability has no enum")
	}
	var listed []string
	for _, v := range raw {
		name, ok := v.(string)
		if !ok {
			t.Fatalf("HubCapability enum value %v is not a string", v)
		}
		listed = append(listed, name)
	}
	if have := extension.HubCapabilities(); !slices.Equal(listed, have) {
		t.Fatalf("schema/extension HubCapability %v, the hub answers %v", listed, have)
	}
	if !slices.Equal(wire.HubCapabilities, extension.HubCapabilities()) {
		t.Fatalf("generated wire.HubCapabilities %v, the hub answers %v (regenerate with scripts/gen/extension_schema.py)",
			wire.HubCapabilities, extension.HubCapabilities())
	}
	if wire.HubCapabilityInboxGroupState != extension.HubInboxGroupState {
		t.Fatalf("generated constant %q, the hub's %q", wire.HubCapabilityInboxGroupState, extension.HubInboxGroupState)
	}
	// The reported read / replied state and the fallback assignment to a handled event both ride on this one name;
	// check the sample initialize an extension's author copies names it.
	sample, err := os.ReadFile(schemaDir + "/examples/InitializeParams.example-notes.json")
	if err != nil {
		t.Fatal(err)
	}
	var params struct {
		Hub struct {
			Capabilities []string `json:"capabilities"`
		} `json:"hub"`
	}
	if err := json.Unmarshal(sample, &params); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(params.Hub.Capabilities, extension.HubInboxGroupState) {
		t.Fatalf("the sample initialize lists %v", params.Hub.Capabilities)
	}
}
