package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A program on a computer calls the hub's allowlisted kb.* methods with exactly its extension's granted scopes, only
// from a computer the extension is switched on for and only as the program the hub started there.
func TestNodeProgramHubCallScopes(t *testing.T) {
	ctx := context.Background()
	h, _ := extensionTestHub(t)
	fake := withFakeKB(h)
	var recordingWrites []assistantwire.KBExtensionUpdate
	fake.a.on(assistantwire.MethodKBExtensionPutRecording, func(raw json.RawMessage) (any, error) {
		var env assistantwire.KBExtensionUpdate
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, err
		}
		recordingWrites = append(recordingWrites, env)
		return map[string]any{"id": "55555555-5555-4555-8555-555555555555", "revision": 1, "created": true}, nil
	})
	box, _ := extensionNodeMachine(t, h, "box", "linux", "amd64", false)
	_, _ = extensionNodeMachine(t, h, "other", "linux", "amd64", false)

	dir := helperPackage(t)
	raw, _ := os.ReadFile(filepath.Join(dir, extension.ManifestFile))
	manifest := strings.Replace(string(raw), `"engines":`, `"permissions": {"hub": ["kb.read", "kb.write"]}, "engines":`, 1)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	reg := h.extensions.reg
	pv, err := reg.Preview(ctx, "dir:"+dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reg.Install(ctx, pv.ID, pv.Digest, "test"); err != nil {
		t.Fatal(err)
	}
	_ = reg.SetSecret(ctx, "example-helper", "api_key", "k-1", "test")
	if err := reg.SetConfig(ctx, "example-helper", json.RawMessage(`{"computers": ["box"]}`), "test"); err != nil {
		t.Fatal(err)
	}
	h.syncExtensionNodes(ctx, box)
	reg.Get("example-helper").Record.Granted = extension.Permissions{Hub: []string{extension.ScopeKBWrite}} // write only
	rev := extensionNodeRevision(reg.GetExact("example-helper"))

	call := func(machine, method string, revision int64, params string) (json.RawMessage, error) {
		raw, _ := json.Marshal(proto.PluginNodeHubCall{ID: proto.PluginNodeExtensionID("example-helper"), Revision: revision, Method: method, Params: proto.PluginNodeHubCallParams(params)})
		out, err := h.NodeProgramHubCall(ctx, machine, raw)
		if err != nil {
			return nil, err
		}
		b, err := json.Marshal(out)
		return b, err
	}
	code := func(err error) string {
		var re *rpc.Error
		if errors.As(err, &re) {
			return re.Code
		}
		return ""
	}

	// Granted kb.write: its writes go through, as the extension's.
	if out, err := call("box", assistantwire.MethodKBUpdate, rev, `{"kind":"projects","id":"from-node","name":"From node"}`); err != nil || !strings.Contains(string(out), "from-node") {
		t.Fatalf("kb.update from the computer: %s %v", out, err)
	}
	if got := fake.a.got(assistantwire.MethodKBExtensionUpdate); len(got) != 1 || !strings.Contains(string(got[0]), `"extension":"example-helper"`) {
		t.Fatalf("the write as the assistant got it: %s", got)
	}
	recording := `{"id":"55555555-5555-4555-8555-555555555555","started_at":"2026-11-20T10:00:00Z","minutes":{"markdown":"# Sync"}}`
	if out, err := call("box", assistantwire.MethodKBPutRecording, rev, recording); err != nil || !strings.Contains(string(out), `"created":true`) {
		t.Fatalf("kb.put_recording from the computer: %s %v", out, err)
	}
	if len(recordingWrites) != 1 || recordingWrites[0].Extension != "example-helper" || !strings.Contains(string(recordingWrites[0].Params), "# Sync") {
		t.Fatalf("the recording write as the assistant got it: %+v", recordingWrites)
	}

	// Not granted kb.read: refused, as for its program on the hub, before the assistant is asked.
	for _, method := range []string{assistantwire.MethodKBGet, assistantwire.MethodKBList, assistantwire.MethodKBSearch, assistantwire.MethodKBDay, "kb.owner", assistantwire.MethodKBRecent} {
		if _, err := call("box", method, rev, `{}`); code(err) != extension.CodePermissionDenied {
			t.Fatalf("%s without kb.read: %v", method, err)
		}
	}
	if got := fake.a.got(assistantwire.MethodKBGet); len(got) != 0 {
		t.Fatalf("a refused read reached the assistant: %s", got)
	}

	// Off the allowlist: unknown, whatever the extension was granted.
	for _, method := range []string{"nodes.call", "storage.get", "assistant.messages", "model.complete", "cli.call",
		assistantwire.MethodKBExtensionUpdate, assistantwire.MethodKBExtensionPutRecording, "kb.forget", "machines.status"} {
		if _, err := call("box", method, rev, `{}`); code(err) != extension.CodeUnknownMethod {
			t.Fatalf("%s: %v, want unknown_method", method, err)
		}
	}

	// From a computer it is not switched on for, an unknown one, or as a program the hub did not start: refused.
	for _, tc := range []struct {
		machine  string
		revision int64
	}{{"other", rev}, {"ghost", rev}, {"box", rev + 1}} {
		if _, err := call(tc.machine, assistantwire.MethodKBUpdate, tc.revision, `{"kind":"projects","id":"x","name":"X"}`); code(err) != extension.CodePermissionDenied {
			t.Fatalf("%s at revision %d: %v", tc.machine, tc.revision, err)
		}
	}
	raw, _ = json.Marshal(proto.PluginNodeHubCall{ID: "someone-else", Revision: rev, Method: assistantwire.MethodKBUpdate, Params: proto.PluginNodeHubCallParams(`{}`)})
	if _, err := h.NodeProgramHubCall(ctx, "box", raw); code(err) != extension.CodePermissionDenied {
		t.Fatalf("another extension's id: %v", err)
	}

	// Switched off: refused.
	if err := reg.SetEnabled(ctx, "example-helper", false, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := call("box", assistantwire.MethodKBUpdate, rev, `{"kind":"projects","id":"y","name":"Y"}`); code(err) != extension.CodePermissionDenied {
		t.Fatalf("a disabled extension: %v", err)
	}
	if got := fake.a.got(assistantwire.MethodKBExtensionUpdate); len(got) != 1 {
		t.Fatalf("refused writes reached the assistant: %s", got)
	}
}
