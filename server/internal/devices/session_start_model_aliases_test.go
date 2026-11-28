package devices

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/nodeproto"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestSessionStartModelAliasesCapabilityAnnounced(t *testing.T) {
	if !slices.Contains(nodeproto.Announced, nodeproto.SessionStartModelAliases) {
		t.Fatalf("a new node does not announce %q", nodeproto.SessionStartModelAliases)
	}
}

// Aliases for a new Claude Code terminal session go only to a node that announces support. An older node would
// otherwise start the session on its launcher's models without reporting that the aliases were dropped.
func TestMachineCallGatesSessionStartModelAliases(t *testing.T) {
	d := newTestDevices(t)
	m := newMachine(d, MachineConfig{ID: "box", Name: "Box", Kind: "ssh", SSHHost: "box.invalid"})
	without := current("old")
	without.Capabilities = slices.DeleteFunc(slices.Clone(without.Capabilities), func(c string) bool {
		return c == nodeproto.SessionStartModelAliases
	})

	for _, info := range []proto.NodeInfo{current("new"), without, {Version: "pre-handshake", ModelConfiguration: true}} {
		hasCapability := slices.Contains(info.Capabilities, nodeproto.SessionStartModelAliases)
		for _, method := range []string{"session.start", devicewire.MethodStartWorkSession} {
			for _, aliases := range []map[string]any{
				nil,
				{},
				{"sonnet_model": "vendor/standard"},
			} {
				m.mu.Lock()
				m.info, m.online = info, true
				m.mu.Unlock()
				ctx, cancel := context.WithCancel(context.Background())
				cancel() // an admitted call cannot reach a process in this fixture
				params := map[string]any{"cwd": "/tmp"}
				if aliases != nil {
					params["model_aliases"] = aliases
				}
				var re *rpc.Error
				err := m.call(ctx, method, params, nil)
				refused := errors.As(err, &re) && re.Code == proto.CodeNodeOutdated
				want := len(aliases) != 0 && !hasCapability
				if refused != want {
					t.Errorf("%s %s aliases=%v: node_outdated=%v, want %v (err %v)", info.Version, method, aliases, refused, want, err)
				}
			}
		}
	}

	// This is a start option. Established sends keep their existing compatibility path.
	m.mu.Lock()
	m.info = without
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var re *rpc.Error
	if err := m.call(ctx, "session.send", map[string]any{"model_aliases": map[string]any{"sonnet_model": "vendor/standard"}}, nil); errors.As(err, &re) && re.Code == proto.CodeNodeOutdated {
		t.Fatal("an unrelated session.send gained the model-alias start gate")
	}
}
