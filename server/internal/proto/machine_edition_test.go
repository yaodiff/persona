package proto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/nodeproto"
)

// A full build's node.info is what it was before editions: no edition field. A core-only node says "core", and a
// node.info without the field reads as the full build.
func TestNodeInfoEditionIsLeftOutByAFullBuild(t *testing.T) {
	full, err := json.Marshal(NodeInfo{Edition: nodeproto.Edition(nodeproto.EditionFull.Marker())})
	if err != nil || strings.Contains(string(full), "edition") {
		t.Fatalf("a full node's info: %s %v", full, err)
	}
	core, err := json.Marshal(NodeInfo{Edition: nodeproto.Edition(nodeproto.EditionCore.Marker())})
	if err != nil || !strings.Contains(string(core), `"edition":"core"`) {
		t.Fatalf("a core-only node's info: %s %v", core, err)
	}
	var old NodeInfo
	if err := json.Unmarshal([]byte(`{"hostname":"h","os":"linux","arch":"amd64"}`), &old); err != nil {
		t.Fatal(err)
	}
	if got, ok := nodeproto.ParseEdition(string(old.Edition)); !ok || got != nodeproto.EditionFull {
		t.Fatalf("a node from before editions reads as %q, %v", got, ok)
	}
}
