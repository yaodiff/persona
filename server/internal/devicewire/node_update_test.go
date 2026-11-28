package devicewire_test

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestNodeUpdateMethodsKeepLegacyWire(t *testing.T) {
	t.Run("begin", func(t *testing.T) {
		checkNodeUpdateWire(t, devicewire.NodeUpdateBeginMethod, "node.update.begin",
			proto.NodeUpdateBeginParams{Size: 7, SHA256: "abcd", Version: "new-build"},
			`{"size":7,"sha256":"abcd","version":"new-build"}`,
			`{"id":"update-one","offset":3}`, proto.NodeUpdateBeginResult{ID: "update-one", Offset: 3})
	})
	t.Run("abort", func(t *testing.T) {
		checkNodeUpdateWire(t, devicewire.NodeUpdateAbortMethod, "node.update.abort",
			proto.NodeUpdateAbortParams{ID: "update-one"}, `{"id":"update-one"}`, `{"ok":true}`, buswire.None{})
	})
	t.Run("chunk", func(t *testing.T) {
		checkNodeUpdateWire(t, devicewire.NodeUpdateChunkMethod, "node.update.chunk",
			proto.NodeUpdateChunkParams{ID: "update-one", Offset: 3, Data: []byte{0, 1, 0xfe, 0xff}},
			`{"id":"update-one","offset":3,"data":"AAH+/w=="}`,
			`{"offset":7}`, proto.NodeUpdateChunkResult{Offset: 7})
	})
	t.Run("confirm", func(t *testing.T) {
		checkNodeUpdateWire(t, devicewire.NodeUpdateConfirmMethod, "node.update.confirm",
			proto.NodeUpdateConfirmParams{Version: "new-build"}, `{"version":"new-build"}`, `{"ok":true}`, buswire.None{})
	})
}

func TestNodeUpdateCommitGraceKeepsLegacyWire(t *testing.T) {
	zero, positive := 0, 60
	for _, tc := range []struct {
		name   string
		grace  *int
		params string
	}{
		{"zero", &zero, `{"id":"update-one","handoff_grace_seconds":0}`},
		{"positive", &positive, `{"id":"update-one","handoff_grace_seconds":60}`},
		{"omitted", nil, `{"id":"update-one"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			checkNodeUpdateWire(t, devicewire.NodeUpdateCommitMethod, "node.update.commit",
				proto.NodeUpdateCommitParams{ID: "update-one", HandoffGraceSeconds: tc.grace}, tc.params,
				`{"accepted":true,"id":"update-one"}`, proto.NodeUpdateCommitResult{Accepted: true, ID: "update-one"})
		})
	}
}

func TestNodeUpdateCommitDecodesSparseReplies(t *testing.T) {
	params := proto.NodeUpdateCommitParams{ID: "update-one"}
	t.Run("accepted without id", func(t *testing.T) {
		checkNodeUpdateWire(t, devicewire.NodeUpdateCommitMethod, "node.update.commit", params,
			`{"id":"update-one"}`, `{"accepted":true}`, proto.NodeUpdateCommitResult{Accepted: true})
	})
	t.Run("generic acknowledgment", func(t *testing.T) {
		checkNodeUpdateWire(t, devicewire.NodeUpdateCommitMethod, "node.update.commit", params,
			`{"id":"update-one"}`, `{"ok":true}`, proto.NodeUpdateCommitResult{})
	})
}

type nodeUpdateWireCaller func(method string, params, out any) error

func (call nodeUpdateWireCaller) Call(_ context.Context, method string, params, out any) error {
	return call(method, params, out)
}

func checkNodeUpdateWire[P, R any](t *testing.T, method buswire.Method[P, R], wantMethod string,
	params P, wantParams, reply string, wantReply R,
) {
	t.Helper()
	caller := nodeUpdateWireCaller(func(name string, request, out any) error {
		if name != wantMethod {
			t.Fatalf("method = %q, want %q", name, wantMethod)
		}
		raw, err := json.Marshal(request)
		if err != nil || string(raw) != wantParams {
			t.Fatalf("wire params = %s, %v, want %s", raw, err, wantParams)
		}
		var decoded P
		if err := json.Unmarshal(raw, &decoded); err != nil || !reflect.DeepEqual(decoded, params) {
			t.Fatalf("decoded params = %#v, %v, want %#v", decoded, err, params)
		}
		if _, acknowledgment := any(wantReply).(buswire.None); acknowledgment {
			if out != nil {
				t.Fatalf("acknowledgment %s got decode destination %T, want nil", reply, out)
			}
			return nil
		}
		return json.Unmarshal([]byte(reply), out)
	})
	got, err := buswire.Call(t.Context(), caller, method, params)
	if err != nil || !reflect.DeepEqual(got, wantReply) {
		t.Fatalf("decoded reply = %#v, %v, want %#v", got, err, wantReply)
	}
}
