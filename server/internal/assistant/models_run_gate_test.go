package assistant

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// End-to-end through modelRun + modelRunErr: consecutive CodeModelRunTimeout RPC errors trip the shared ai.NewRun
// gate, expose the pause via ai.Held, and put off subsequent calls with ai.IsPaused without making another node RPC.
func TestModelRunTimeoutTripsSharedRunGate(t *testing.T) {
	ai.SetBusyPauseForTest(t, time.Hour)
	s, _, _ := modelService(t)
	devs := &runDevs{viaDevs: viaDevs{err: &rpc.Error{Code: proto.CodeModelRunTimeout, Message: "timed out"}}}
	s.devs = devs

	model := fmt.Sprintf("m-gate-timeout-e2e-%d", time.Now().UnixNano())
	if r := modelRequest(s, "PATCH", "kb", fmt.Sprintf(`{"revision":0,"provider":"launcher.claude","model":%q}`, model)); r.Code != 200 {
		t.Fatalf("pick claude: %d %s", r.Code, r.Body.String())
	}
	cfg := ai.Config{ViaMachine: "server", RunLauncher: "claude", Model: model}
	ctx := context.Background()

	// First timeout preserves DeadlineExceeded and leaves the gate open.
	if _, err := s.purposeModel("kb.compress").Complete(ctx, "", "first", 16); !errors.Is(err, context.DeadlineExceeded) || ai.IsPaused(err) {
		t.Fatalf("first timeout = %v; want DeadlineExceeded without pause", err)
	}
	if p := ai.Held(cfg); p != nil {
		t.Fatalf("Held after single timeout = %+v; want nil", p)
	}

	// Second consecutive timeout trips the shared run gate.
	if _, err := s.purposeModel("kb.topics").Complete(ctx, "", "second", 16); !ai.IsPaused(err) {
		t.Fatalf("second timeout = %v; want PausedError", err)
	}
	if p := ai.Held(cfg); p == nil || p.Reason != ai.ReasonBusy {
		t.Fatalf("Held after 2nd timeout = %+v; want ReasonBusy", p)
	}

	// Third call is put off by the gate without making another node RPC.
	if _, err := s.purposeModel("kb.compress").Complete(ctx, "", "third", 16); !ai.IsPaused(err) {
		t.Fatalf("third call while held = %v; want PausedError", err)
	}
	if got := len(devs.saw); got != 2 {
		t.Fatalf("node RPC count = %d; want 2 (third call put off by gate)", got)
	}
}
