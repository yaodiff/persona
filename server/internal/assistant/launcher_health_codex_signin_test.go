package assistant

import (
	"context"
	"fmt"
	"testing"
	"testing/synctest"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A Codex start that met its sign-in screen (node/codex_start.go) is the launcher's sign-in: recorded as auth from its
// code, whatever language the node's words are in, never set aside as a stalled start; another launcher may still work,
// and a task keeps the code for the hub to word.
func TestCodexSignInStartCountsAsAuth(t *testing.T) {
	s, f := launcherHealthHubOn(t, newStorelessService)
	synctest.Test(t, func(t *testing.T) {
		ctx := context.Background()
		release := make(chan struct{})
		f.onCheck = func(*fakeHub) { <-release } // the start's record, before the check it calls for replaces it
		err := fmt.Errorf("session.start: %w", &rpc.Error{Code: proto.CodeCodexNotLoggedIn,
			Message: "电脑上的 Codex 还没登录：它打开后停在登录界面，Agent 没有启动。\n› 1. Sign in with ChatGPT"}) // i18n-ignore: a node's words in its default language
		if startStalled(err) || startRefused(err) || !launcherSpecificFailure(err) {
			t.Fatalf("stalled %v, refused %v, launcher's %v", startStalled(err), startRefused(err), launcherSpecificFailure(err))
		}
		if code, _ := codedStartRefusal(err); code != proto.CodeCodexNotLoggedIn {
			t.Fatalf("task code %q", code)
		}
		s.recordStartFailure(ctx, "l4", "work", err)
		synctest.Wait()
		if r := healthState(s, "l4", "work"); r.Kind != errKindAuth || r.Source != healthSourceStart || r.State == healthOK {
			t.Fatalf("recorded %+v", r)
		}
		close(release)
		synctest.Wait()
	})
}
