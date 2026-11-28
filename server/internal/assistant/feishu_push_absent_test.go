package assistant

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/proto"
)

// serviceWithLarkOff is a manager whose `modules` setting switched the lark module off; in a build without it (-tags
// persona_without_lark, persona_core_only) the module is absent the same way. logs collects what the manager logged.
func serviceWithLarkOff(t *testing.T) (s *Service, logs func() []string) {
	t.Helper()
	f := newFakeHub()
	st := &memStore{}
	var mu sync.Mutex
	var lines []string
	s = withSettings(t, f, New(Options{Hub: f, Store: func() Store { return st }, ModulesOff: []string{"lark"},
		Logf: func(format string, args ...any) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, fmt.Sprintf(format, args...))
		}}))
	if !s.larkAbsent() {
		t.Fatal("the lark module is not absent")
	}
	return s, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), lines...) }
}

// Every push entry point with the lark module absent or switched off (#13404, open question 2): nil, no failure line,
// no retry, and notify_user's copy report says skipped for the Feishu copy.
func TestFeishuPushesAreSkippedWithoutTheLarkModule(t *testing.T) {
	s, logs := serviceWithLarkOff(t)
	ctx := context.Background()
	s.setLarkTiming("mirror_retry", time.Millisecond)

	r := newCopyReport(1)
	if err := s.feishuPushTold(ctx, "m1", "s1", "title", "body", "", r); err != nil {
		t.Fatalf("pushTold: %v", err)
	}
	copies := r.wait(time.Second)
	if len(copies) != 1 || copies[0].channel != channel.Feishu || copies[0].state != copySkipped {
		t.Fatalf("copies %+v, want one skipped Feishu copy", copies)
	}
	if err := (recordingHost{s: s}).FeishuPushKeyed(ctx, "m1", "s1", "title", "body", "key"); err != nil {
		t.Fatalf("recording push would retry: %v", err)
	}
	s.feishuPush("m1", "s1", "title", "body")
	remind := time.Now().Add(time.Minute)
	done := make(chan struct{})
	go func() {
		defer close(done)
		sendTestLarkReminder(s, "m1", "s1", "title", "body", Todo{ID: "todo_1", RemindAt: &remind})
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the reminder keeps trying")
	}
	s.feishuAgentDone("m1", proto.Session{SessionID: "s2"}, "head", "text", "place", false)
	s.feishuMirror(ctx, "m1", "s1")
	<-time.After(50 * time.Millisecond) // a window past a retry slot: nothing may be logged in it
	for _, l := range logs() {
		if strings.Contains(l, "bots:") {
			t.Fatalf("logged %q", l)
		}
	}
}

// A skipped copy is not told in notify_user's answer: a hub without Feishu answers as with no copy at all.
func TestNotifyAnswerLeavesOutSkippedCopies(t *testing.T) {
	devices := 1
	res := NotifyResult{OK: true, Devices: &devices}
	skipped := []pushCopy{{channel: channel.Feishu, state: copySkipped}}
	if got, want := notifyAnswerText("en", res, skipped), notifyAnswerText("en", res, nil); got != want {
		t.Fatalf("%q, want %q", got, want)
	}
	zero := 0
	res = NotifyResult{OK: true, Devices: &zero, Reason: "no_device"}
	if got, want := notifyAnswerText("en", res, skipped), notifyAnswerText("en", res, nil); got != want {
		t.Fatalf("%q, want %q", got, want)
	}
}

// With the module on, a bot that is not ready is still no skipped copy: it keeps what it did.
func TestFeishuPushWithTheModuleOnIsNotSkipped(t *testing.T) {
	needsLarkLayer(t)
	s := newTestService(t, newFakeHub())
	r := newCopyReport(1)
	_ = s.feishuPushTold(context.Background(), "m1", "s1", "title", "body", "", r)
	for _, c := range r.wait(time.Second) {
		if c.state == copySkipped {
			t.Fatalf("skipped with the module on: %+v", c)
		}
	}
}
