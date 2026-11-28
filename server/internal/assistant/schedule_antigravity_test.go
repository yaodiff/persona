package assistant

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestTaskAntigravityCompanionUnavailable(t *testing.T) {
	f := newFakeHub().withMachine()
	f.launcherList = []proto.Launcher{{Name: "agy", Agent: "agy", Source: "zshrc"}}
	s := newTestService(t, f)
	known, err := s.checkTaskLauncher(context.Background(), "server", "agy", &TaskExecution{MachineID: "server", Launcher: "agy", Agent: "agy"})
	if !known || err == nil {
		t.Fatalf("disconnected companion admitted: %v %v", known, err)
	}
	text := i18n.Localize(err, i18n.Lang("en"))
	if !strings.Contains(text, "Restore Antigravity") || strings.Contains(text, "change the launcher") {
		t.Fatalf("wrong recovery: %s", text)
	}
	if f.sent() != 0 {
		t.Fatal("unavailable companion dispatched")
	}
	f.launcherList = []proto.Launcher{{Name: "agy", Agent: "agy", Source: "agy", Assistant: true}}
	s.invalidateLauncherListCache("server")
	known, err = s.checkTaskLauncher(context.Background(), "server", "agy", &TaskExecution{MachineID: "server", Launcher: "agy", Agent: "agy"})
	if !known || err != nil {
		t.Fatalf("restored companion refused: %v %v", known, err)
	}
}

func TestTaskOtherIncompatibleLauncherKeepsRefusal(t *testing.T) {
	f := newFakeHub().withMachine()
	f.launcherList = []proto.Launcher{{Name: "other", Agent: "dsh", Source: "zshrc"}}
	s := newTestService(t, f)
	known, err := s.checkTaskLauncher(context.Background(), "server", "other")
	if !known || err == nil {
		t.Fatalf("unsupported launcher admitted: %v %v", known, err)
	}
	if got, want := err.Error(), s.mainChatRefusal(context.Background(), "server", "other", "").Error(); got != want {
		t.Fatalf("refusal changed: %s != %s", got, want)
	}
}

func TestTaskAntigravityReconnectResumesConversation(t *testing.T) {
	f := newFakeHub().withMachine()
	connected := []proto.Launcher{{Name: "agy", Agent: "agy", Source: "agy", Assistant: true}}
	f.launcherList = connected
	s := newTestService(t, f)
	setTasks(s, Task{ID: "job", Prompt: "Check files", Repeat: repeatInterval, EveryMinutes: 30, Enabled: true})
	setModel(t, s, "task.job", ModelBinding{Provider: "launcher.agy"})
	first, err := runNow(t, s, "job")
	if err != nil {
		t.Fatal(err)
	}
	before, _ := taskByID(s, "job")
	binding, _ := s.resolveModel(context.Background(), "task.job")
	sends := f.sent()
	f.launcherList = []proto.Launcher{{Name: "agy", Agent: "agy", Source: "zshrc"}}
	s.invalidateLauncherListCache("server")
	if _, err := runNow(t, s, "job"); err == nil {
		t.Fatal("disconnected run admitted")
	}
	after, _ := taskByID(s, "job")
	if after.RunSessionID != before.RunSessionID || after.RunLauncher != before.RunLauncher || !reflect.DeepEqual(after.RunExecution, before.RunExecution) || f.sent() != sends {
		t.Fatal("refusal changed execution or dispatched")
	}
	if got, _ := s.resolveModel(context.Background(), "task.job"); got != binding {
		t.Fatal("refusal changed binding")
	}
	f.launcherList = connected
	s.invalidateLauncherListCache("server")
	resumed, err := runNow(t, s, "job")
	if err != nil || resumed.SessionID != first.SessionID {
		t.Fatalf("did not resume: %+v %v", resumed, err)
	}
}

func TestTaskUnboundAntigravityKeepsRefusal(t *testing.T) {
	f := newFakeHub().withMachine()
	f.launcherList = []proto.Launcher{{Name: "agy", Agent: "agy", Source: "zshrc"}}
	s := newTestService(t, f)
	known, err := s.checkTaskLauncher(context.Background(), "server", "agy")
	if !known || err == nil {
		t.Fatalf("unsupported launcher admitted: %v %v", known, err)
	}
	if got, want := err.Error(), s.mainChatRefusal(context.Background(), "server", "agy", "").Error(); got != want {
		t.Fatalf("unbound refusal changed: %s != %s", got, want)
	}
}
