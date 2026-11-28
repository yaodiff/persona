package larkhost

import (
	"context"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/moduleswitch"
)

// Without a module provided there is no layer; with one, New makes it for each host unless the switch has it off;
// providing twice panics.
func TestProvide(t *testing.T) {
	saved := port
	t.Cleanup(func() { port = saved })
	port = moduleswitch.NewPort[func(Host) Layer]("larkhosttest")
	var on moduleswitch.Set
	if InBuild(on) {
		t.Fatal("in the build without a module")
	}
	if _, ok := New(on, nil); ok {
		t.Fatal("a layer without a module")
	}
	made := 0
	Provide("rec", func(Host) Layer { made++; return None{} })
	if _, ok := New(on, nil); !ok || made != 1 || !InBuild(on) {
		t.Fatalf("made %d", made)
	}
	if _, ok := New(moduleswitch.Off("rec"), nil); ok || made != 1 || InBuild(moduleswitch.Off("rec")) {
		t.Fatalf("made %d", made)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("provided twice without a panic")
		}
	}()
	Provide("rec", func(Host) Layer { return nil })
}

// None, the port without a channel, is a bot switched off: its status reads (no error) as neither on nor ready, it
// supports nothing, refuses every send with ErrNoChannel and drops notes and thread lines.
func TestNone(t *testing.T) {
	ctx := context.Background()
	var o Outbound = None{}
	if st, err := o.Status(ctx); err != nil || st.Ready || st.Enabled {
		t.Fatalf("status %+v %v", st, err)
	}
	if f, ready := o.Features(ctx); ready || f != (assistantwire.FeishuFeatures{}) {
		t.Fatalf("features %+v %v", f, ready)
	}
	if mid, err := o.Post(ctx, Target{}, "", nil, "", ""); mid != "" || !errors.Is(err, ErrNoChannel) {
		t.Fatalf("post %q %v", mid, err)
	}
	if err := o.Reply(ctx, Target{}, "om_1", "x", "", false); !errors.Is(err, ErrNoChannel) {
		t.Fatalf("reply %v", err)
	}
	if err := o.UpdateCard(ctx, "om_1", nil); !errors.Is(err, ErrNoChannel) {
		t.Fatalf("update %v", err)
	}
	o.Note(ctx, Target{}, "om_1", "x")
	o.ThreadPost("todo_1", ThreadEvent{Kind: ThreadTodoDone})
}

func TestNoneDivertNotices(t *testing.T) {
	var board Board = None{}
	if board.DivertNotices(t.Context(), assistantwire.NoticePush(), nil, nil) {
		t.Fatal("diverted a notice without a channel")
	}
	defer func() {
		r := recover()
		if err, ok := r.(error); !ok || err.Error() != `assistantwire.NoticeKind: unknown notice kind ""` {
			t.Errorf("DivertNotices panic = %v, want an unknown notice kind defect", r)
		}
	}()
	board.DivertNotices(t.Context(), assistantwire.NoticeKind{}, nil, nil)
}

func TestNonePush(t *testing.T) {
	var push Push = None{}
	to := assistantwire.SessionRef{MachineID: "machine", SessionID: "session"}
	unexpected := func() { t.Fatal("a push callback ran without the module") }
	if err := push.PushKeyed(t.Context(), to, "title", "body", "key", assistantwire.LarkPushCopy{
		Started: unexpected, Delivered: unexpected,
	}); err != nil {
		t.Fatalf("a keyed push would retry: %v", err)
	}
	push.TodoReminder(to, assistantwire.LarkReminder{Copy: func(context.Context) (string, string, bool) {
		unexpected()
		return "", "", false
	}})
	push.AgentDone(to, func(context.Context) (assistantwire.LarkAgentDone, bool) {
		unexpected()
		return assistantwire.LarkAgentDone{}, false
	})
	m := assistantwire.LarkMirror{SessionRef: to, StillDue: func(context.Context, bool) bool {
		unexpected()
		return false
	}}
	push.Mirror(t.Context(), m, "en", assistantwire.LarkMirrorTurn{})
	push.MirrorPost(t.Context(), m, "en", nil, nil, "key", unexpected)
}
