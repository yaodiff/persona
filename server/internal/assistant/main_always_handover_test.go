package assistant

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// Replacing the main conversation via newMain, clearMain, or settings carries over its
// "Always allow in this conversation" grants once to the new main conversation (#16158).
// Revocations on the new main stick even after periodic handOverFormerMainTasks runs,
// and leader or Inbox-origin conversations never copy grants onto the main conversation.
func TestMainHandoverCarriesAlwaysGrants(t *testing.T) {
	t.Run("newMain carries grants and revocation sticks", func(t *testing.T) {
		st := &grantTestStore{fleetMem: newFleetMem()}
		s, _ := newQuietServiceOn(t, st)
		ctx := context.Background()
		if err := st.AllowAlways(ctx, "conv1", "Bash", []string{"tool:Bash", "lark:/read"}); err != nil {
			t.Fatal(err)
		}
		if old, _, err := s.newMain(ctx, false, "conv1"); err != nil || old != "conv1" {
			t.Fatalf("newMain: %q %v", old, err)
		}
		sess, err := s.start(ctx, StartParams{Main: true})
		if err != nil || sess.SessionID == "" {
			t.Fatalf("start main: %+v %v", sess, err)
		}
		next := sess.SessionID
		for _, key := range []string{"tool:Bash", "lark:/read"} {
			if ok, err := st.AlwaysAllowed(ctx, next, []string{key}); err != nil || !ok {
				t.Fatalf("new main %s missing carried grant %q: ok=%v err=%v", next, key, ok, err)
			}
		}
		// Revoking on the new main conversation must stick even if handOverFormerMainTasks runs again.
		if err := st.RevokeAlways(ctx, next, grantHash("tool:Bash")); err != nil {
			t.Fatal(err)
		}
		s.handOverFormerMainTasks(ctx)
		if ok, err := st.AlwaysAllowed(ctx, next, []string{"tool:Bash"}); err != nil || ok {
			t.Fatalf("revoked grant resurrected on %s: ok=%v err=%v", next, ok, err)
		}
		if ok, err := st.AlwaysAllowed(ctx, next, []string{"lark:/read"}); err != nil || !ok {
			t.Fatalf("unrelated carried grant lost on %s: ok=%v err=%v", next, ok, err)
		}
		if ok, err := st.AlwaysAllowed(ctx, "conv1", []string{"tool:Bash"}); err != nil || !ok {
			t.Fatalf("archived conv1 lost its own historical grant: ok=%v err=%v", ok, err)
		}
	})

	t.Run("clearMain carries grants from Former[0]", func(t *testing.T) {
		st := &grantTestStore{fleetMem: newFleetMem()}
		s, _ := newQuietServiceOn(t, st)
		ctx := context.Background()
		if err := st.AllowAlways(ctx, "conv1", "Bash", []string{"tool:Bash"}); err != nil {
			t.Fatal(err)
		}
		if old, err := s.clearMain(ctx, "conv1"); err != nil || old != "conv1" {
			t.Fatalf("clearMain: %q %v", old, err)
		}
		sess, err := s.start(ctx, StartParams{Main: true})
		if err != nil || sess.SessionID == "" {
			t.Fatalf("start main after clear: %+v %v", sess, err)
		}
		if ok, err := st.AlwaysAllowed(ctx, sess.SessionID, []string{"tool:Bash"}); err != nil || !ok {
			t.Fatalf("new main %s after /clear missing carried grant: ok=%v err=%v", sess.SessionID, ok, err)
		}
	})

	t.Run("settings Main switch carries grants", func(t *testing.T) {
		st := &grantTestStore{fleetMem: newFleetMem()}
		s, _ := newQuietServiceOn(t, st)
		ctx := context.Background()
		if err := st.AllowAlways(ctx, "conv1", "Bash", []string{"tool:Bash"}); err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest("PUT", "/api/v1/assistant", strings.NewReader(`{"main":"conv2"}`))
		req.Header.Set("X-Persona-Role", "full")
		w := httptest.NewRecorder()
		s.ServeHTTP(w, req)
		if w.Code != 200 {
			t.Fatalf("PUT /api/v1/assistant: %d %s", w.Code, w.Body.String())
		}
		if ok, err := st.AlwaysAllowed(ctx, "conv2", []string{"tool:Bash"}); err != nil || !ok {
			t.Fatalf("conv2 missing carried grant after settings switch: ok=%v err=%v", ok, err)
		}
	})

	t.Run("leader and inbox conversations never copy grants", func(t *testing.T) {
		ft := newFleetTest(t)
		ctx := context.Background()
		setSettings(t, ft.s, func(v *Settings) { v.MachineID, v.Leaders = "mac", []string{"lead-1"} })
		if err := ft.st.AllowAlways(ctx, "lead-1", "Bash", []string{"tool:Bash"}); err != nil {
			t.Fatal(err)
		}
		if n := ft.s.handOverMainAlways(ctx, "lead-1", "main-2"); n != 0 {
			t.Fatalf("leader grants copied: %d", n)
		}
		ft.s.originSet(ctx, "mac", "inbox-1", OriginInbox, "item-1", "")
		if err := ft.st.AllowAlways(ctx, "inbox-1", "Bash", []string{"tool:Bash"}); err != nil {
			t.Fatal(err)
		}
		if n := ft.s.handOverMainAlways(ctx, "inbox-1", "main-2"); n != 0 {
			t.Fatalf("inbox grants copied: %d", n)
		}
		if ok, _ := ft.st.AlwaysAllowed(ctx, "main-2", []string{"tool:Bash"}); ok {
			t.Fatal("main-2 unexpectedly received grants from leader or inbox conversation")
		}
		// Sanity: a card on the new main with an actually copied grant auto-allows.
		if err := ft.st.AllowAlways(ctx, "main-old", "Bash", []string{"tool:Read"}); err != nil {
			t.Fatal(err)
		}
		if n := ft.s.handOverMainAlways(ctx, "main-old", "main-2"); n != 1 {
			t.Fatalf("handOverMainAlways = %d, want 1", n)
		}
		ix := proto.Interaction{ID: "ix_carry", MachineID: "mac", SessionID: "main-2", Kind: proto.IxPermission, ToolName: "Read", AlwaysKeys: []string{"tool:Read"}}
		if res, ok := ft.s.alwaysAllowed(ctx, ix); !ok || res.Allow == nil || !*res.Allow || !res.Remembered {
			t.Fatalf("carried grant did not auto-allow card: ok=%v res=%+v", ok, res)
		}
	})
}
