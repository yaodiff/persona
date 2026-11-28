package extmgr

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestKBProviderProcessPermissionsAndWire(t *testing.T) {
	h, _ := extensionTestHub(t)
	a := withAssistant(h)
	const checkpointReply = `{"written":2,"read":3,"approval":"plan","state":"running"}`
	const markerReply = `{"next_after":4,"markers":[{"sent_by":"assistant","n":4}]}`
	const markerParams = `{"approval":"plan","day":"2026-03-14","after":2}`
	a.on(assistantwire.MethodKBProviderCheckpoint, func(json.RawMessage) (any, error) {
		return json.RawMessage(checkpointReply), nil
	})
	a.on(assistantwire.MethodKBProviderSentMarkers, func(json.RawMessage) (any, error) {
		return json.RawMessage(markerReply), nil
	})
	for _, tc := range []struct {
		id, scopes        string
		progress, markers bool
	}{
		{"kb-legacy-reader", `["kb.read","sent.read"]`, false, false},
		{"kb-checkpoint-reader", `["kb.import_status"]`, true, false},
		{"kb-marker-reader", `["kb.import_status","sent.read"]`, true, true},
	} {
		startKBProcess(t, h, tc.id, tc.scopes)
		p := h.extensions.get(tc.id)
		for _, read := range []struct {
			call, params, internal, reply string
			query                         assistantwire.KBImportQuery
			ok                            bool
		}{
			{"checkpoint", "{}", assistantwire.MethodKBProviderCheckpoint, checkpointReply, assistantwire.KBImportQuery{}, tc.progress},
			{"markers", markerParams, assistantwire.MethodKBProviderSentMarkers, markerReply,
				assistantwire.KBImportQuery{Day: "2026-03-14", Approval: "plan", After: 2}, tc.markers},
		} {
			var out json.RawMessage
			before := len(a.got(read.internal))
			err := p.proc.Call(t.Context(), "kbtest."+read.call, json.RawMessage(read.params), &out)
			if read.ok {
				if err != nil {
					t.Fatal(err)
				}
				if string(out) != read.reply {
					t.Fatalf("%s reply = %s, want %s", read.call, out, read.reply)
				}
				calls := a.got(read.internal)
				if len(calls) != before+1 {
					t.Fatalf("assistant calls %d", len(calls))
				}
				var who assistantwire.KBProviderRequest
				if json.Unmarshal(calls[before], &who) != nil {
					t.Fatal("invalid internal envelope")
				}
				born := h.extensions.reg.SnapshotExact(tc.id).Record.InstalledAt.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano)
				if who.Extension.String() != tc.id || who.StorageID.String() != tc.id || who.InstalledAt != born {
					t.Fatalf("installation attribution %+v", who)
				}
				if who.Params != read.query {
					t.Fatalf("%s query = %+v, want %+v", read.call, who.Params, read.query)
				}
			} else {
				var re *rpc.Error
				if !errors.As(err, &re) || re.Code != extension.CodePermissionDenied || len(a.got(read.internal)) != before {
					t.Fatalf("unexpected permission result: %s %v", tc.id, err)
				}
			}
		}
	}
	p := h.extensions.get("kb-marker-reader")
	for _, q := range []string{
		`{"approval":"plan","day":"2026-03-14","storage_id":"other"}`,
		`{"approval":"plan","day":"2026-03-14","source":"other"}`,
		`{"approval":"plan","day":"2026-03-14","after":-1}`,
	} {
		var out json.RawMessage
		before := len(a.got(assistantwire.MethodKBProviderSentMarkers))
		err := p.proc.Call(t.Context(), "kbtest.markers", json.RawMessage(q), &out)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != "invalid_params" || len(a.got(assistantwire.MethodKBProviderSentMarkers)) != before {
			t.Fatalf("foreign fields reached assistant: %v", err)
		}
	}
	for _, tc := range []struct {
		call, method, params, reply string
	}{
		{"checkpoint", assistantwire.MethodKBProviderCheckpoint, "{}", `{"state":"running","approval":"plan","read":3,"written":2,"source":"example"}`},
		{"markers", assistantwire.MethodKBProviderSentMarkers, markerParams, `{"markers":[{"n":4,"sent_by":"assistant","text":"archived private data"}]}`},
	} {
		t.Run(tc.call+" rejects extra reply fields", func(t *testing.T) {
			a.on(tc.method, func(json.RawMessage) (any, error) { return json.RawMessage(tc.reply), nil })
			var raw json.RawMessage
			err := p.proc.Call(t.Context(), "kbtest."+tc.call, json.RawMessage(tc.params), &raw)
			var re *rpc.Error
			if !errors.As(err, &re) || re.Code != assistantwire.CodeKBUnavailable || re.Message != "invalid import status response" || len(re.Data) > 0 || len(raw) > 0 {
				t.Fatalf("upstream returned an unapproved field: %s %v", raw, err)
			}
		})
	}
}

func TestKBProviderProcessRetiredWhileReading(t *testing.T) {
	h, _ := extensionTestHub(t)
	a := withAssistant(h)
	startKBProcess(t, h, "kb-changing", `["kb.import_status"]`)
	started, release := make(chan struct{}), make(chan struct{})
	a.on(assistantwire.MethodKBProviderCheckpoint, func(json.RawMessage) (any, error) {
		close(started)
		select {
		case <-release:
		case <-t.Context().Done():
		}
		return json.RawMessage(`{"state":"running","approval":"private-reply","read":1,"written":1}`), nil
	})
	result := make(chan error, 1)
	ctx := t.Context()
	p := h.extensions.get("kb-changing")
	go func() {
		var out json.RawMessage
		err := p.proc.Call(ctx, "kbtest.checkpoint", json.RawMessage("{}"), &out)
		if err == nil {
			err = errors.New("disclosed: " + string(out))
		}
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		close(release)
		t.Fatal("provider never reached the assistant")
	}
	if err := h.extensions.reg.SetEnabled(ctx, "kb-changing", false, "test"); err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-result; err == nil || strings.Contains(err.Error(), "private-reply") {
		t.Fatalf("reply after grant expired: %v", err)
	}
}
