package hub

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestNotifyAnswerLegacyJSON(t *testing.T) {
	for _, tc := range []struct {
		name string
		out  *pushOutcome
		want string
	}{
		{"unreported", nil, `{"ok":true}`},
		{"no device", &pushOutcome{}, `{"devices":0,"ok":true,"reason":"no_device"}`},
		{"filtered", &pushOutcome{reachable: 1}, `{"devices":0,"ok":true,"reason":"filtered"}`},
		{"delivered and kept", &pushOutcome{devices: 2, kept: 1}, `{"devices":2,"kept":1,"ok":true}`},
		{"held", &pushOutcome{held: interceptReasonP1}, `{"devices":0,"held":true,"ok":true,"reason":"ln_p1_held"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := json.Marshal(notifyAnswer(tc.out))
			if err != nil || string(got) != tc.want {
				t.Errorf("notifyAnswer JSON = %s, %v, want %s", got, err, tc.want)
			}
		})
	}
}

func TestNotifyMethodDuplicateHandover(t *testing.T) {
	h := ringHub(t)
	handler := busMethods[assistantwire.NotifyMethod.Name]
	params := json.RawMessage(`{"notification":{"id":"ntf_retry","kind":"assistant","title":"Report ready"}}`)
	for _, want := range []string{`{"devices":0,"ok":true,"reason":"no_device"}`, `{"ok":true}`} {
		out, err := handler(h, t.Context(), nil, params)
		if err != nil {
			t.Fatal(err)
		}
		got, err := json.Marshal(out)
		if err != nil || string(got) != want {
			t.Fatalf("notify.request JSON = %s, %v, want %s", got, err, want)
		}
	}
}

func TestNotifyMethodInvalidParams(t *testing.T) {
	out, err := busMethods[assistantwire.NotifyMethod.Name](nil, t.Context(), nil, json.RawMessage(`[]`))
	var re *rpc.Error
	if out != nil || !errors.As(err, &re) || re.Code != bus.CodeInvalidParams || !strings.HasPrefix(re.Message, "notify.request: ") {
		t.Fatalf("notify.request [] = %v, %v, want invalid_params with prefix %q", out, err, "notify.request: ")
	}
}
