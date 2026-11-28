package node

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A program on a computer reaches the hub only for the allowlisted methods, as the program the node runs now (its
// id and identity revision, never the program's word), and not once it was replaced or its lease ran out.
func TestNodeProgramHubCalls(t *testing.T) {
	type sent struct {
		method string
		call   proto.PluginNodeHubCall
	}
	var got []sent
	ph := &pluginHost{running: map[string]*pluginRun{}, now: time.Now, logf: t.Logf}
	ph.hubCaller = func(_ context.Context, method string, params, out any) error {
		got = append(got, sent{method, params.(proto.PluginNodeHubCall)})
		return json.Unmarshal([]byte(`{"id":"r1"}`), out)
	}
	r := &pluginRun{spec: proto.PluginNodeStart{ID: "example-helper", Revision: 42}}
	ph.running["example-helper"] = r
	handle := ph.programCalls(r)

	out, err := handle(context.Background(), "kb.put_recording", json.RawMessage(`{"id":"x"}`))
	if err != nil || string(out.(json.RawMessage)) != `{"id":"r1"}` {
		t.Fatalf("an allowlisted call: %v %v", out, err)
	}
	if len(got) != 1 || got[0].method != proto.MethodPluginNodeHubCall || got[0].call.ID != "example-helper" ||
		got[0].call.Revision != 42 || got[0].call.Method != "kb.put_recording" || string(got[0].call.Params) != `{"id":"x"}` {
		t.Fatalf("relayed %+v", got)
	}

	for _, method := range []string{"nodes.call", "storage.get", "assistant.messages", "kb.extension_update", "model.complete", "cli.call"} {
		_, err := handle(context.Background(), method, json.RawMessage(`{}`))
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != extension.CodeUnknownMethod {
			t.Fatalf("%s: %v, want unknown_method", method, err)
		}
	}
	if len(got) != 1 {
		t.Fatalf("a method off the allowlist reached the hub: %+v", got)
	}

	r.expired = true
	if _, err := handle(context.Background(), "kb.get", json.RawMessage(`{}`)); err == nil {
		t.Fatal("a program whose lease ran out reached the hub")
	}
	ph.running["example-helper"] = &pluginRun{spec: r.spec}
	r.expired = false
	if _, err := handle(context.Background(), "kb.get", json.RawMessage(`{}`)); err == nil {
		t.Fatal("a replaced program reached the hub")
	}
	if len(got) != 1 {
		t.Fatalf("calls relayed: %+v", got)
	}
}
