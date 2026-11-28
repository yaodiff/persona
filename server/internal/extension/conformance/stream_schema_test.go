package conformance_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
)

func TestStreamContractsDoNotActivateRuntime(t *testing.T) {
	// 8 is the columns layout (#9158), 9 a tile's gauge (#9337), 10 a graph (#9727), 11 a scene (#12544), 12 a map
	// (#14730) and 13 a code's navigate, drawn in snapshots: the stream stays an explicit opt-in (context.stream and
	// extension_ui_stream_v1), never implied by a UI version of 7 or more.
	if extension.UIVersion != 13 {
		t.Fatalf("contract-only stage changed runtime UI version: %d", extension.UIVersion)
	}
	// inbox_group_state is intentionally active: the manager accepts grouped read/replied state and fallback
	// assignments to handled events. tool_images is too (#8484), and tool_views carries out-of-band views from
	// connector tool answers (#14730). Other runtime capabilities remain contract-only.
	if !reflect.DeepEqual(extension.HubCapabilities(), []string{extension.HubInboxGroup, extension.HubInboxGroupState, extension.HubToolImages, extension.HubToolViews}) {
		t.Fatalf("hub capabilities changed without an intentional runtime rollout: %v", extension.HubCapabilities())
	}
	for _, dir := range []string{"", "invalid"} {
		paths, err := filepath.Glob(filepath.Join(schemaDir, "examples", dir, "UIStream*.json"))
		if err != nil || len(paths) == 0 {
			t.Fatalf("missing stream fixtures: %v", err)
		}
		for _, path := range paths {
			t.Run(filepath.Base(path), func(t *testing.T) {
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				definition := strings.Split(filepath.Base(path), ".")[0]
				err = wire.ValidateJSON(definition, raw)
				if (err == nil) != (dir == "") {
					t.Fatalf("embedded schema disagrees with %s: %v", path, err)
				}
				if dir == "" && definition == "UIStreamSnapshot" {
					var snapshot wire.UIStreamSnapshot
					if err := json.Unmarshal(raw, &snapshot); err != nil {
						t.Fatal(err)
					}
					// The required-empty fixture exercises stream serialization, not the retained legacy View
					// encoder that CheckView uses internally. Runtime activation must resolve that boundary.
					if filepath.Base(path) == "UIStreamSnapshot.required-empty.json" {
						if err := wire.Validate("UIStreamSnapshot", snapshot); err != nil {
							t.Fatal(err)
						}
						return
					}
					// Full native View semantic validation remains necessary in addition to stream frame shape checks.
					if err := extension.CheckView(&snapshot.View); err != nil {
						t.Fatal(err)
					}
				}
			})
		}
	}
}
