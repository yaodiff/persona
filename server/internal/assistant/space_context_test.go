package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A track conversation's context (#13499): the time, the track and the owner's language, in each language.
func TestGoldenSpaceContext(t *testing.T) {
	now := time.Date(2026, 11, 22, 21, 30, 0, 0, time.UTC)
	for _, tc := range []struct{ name, language string }{{"zh-Hans", ""}, {"en", "English"}, {"ja", "日本語"}} {
		got := prompts.RenderSpaceContext(prompts.SpaceContext{Now: now, Zone: "UTC", Owner: "Example user", Title: "qa-plain", Language: tc.language})
		golden(t, "space_context."+tc.name, got)
	}
	golden(t, "space_context.health", prompts.RenderSpaceContext(prompts.SpaceContext{Now: now, Zone: "UTC", Health: true}))
}

// A conversation in the health space or another track gets its own short context instead of the main chat's
// snapshot: nothing of Agents, computers, unread Lark chats, tasks or the knowledge base, at most 600 characters. The
// main conversation and a side conversation of the main space keep the snapshot.
func TestContextOfATrackConversationIsItsOwn(t *testing.T) {
	f := newFakeHub().withMachine()
	f.settings = &Settings{MachineID: "server", Main: "m1"}
	f.setSessions("server", proto.Session{SessionID: "m1", MachineID: "server", Assistant: true},
		proto.Session{SessionID: "side", MachineID: "server", Assistant: true},
		proto.Session{SessionID: "health1", MachineID: "server", Assistant: true, Space: SpaceHealth},
		proto.Session{SessionID: "plain1", MachineID: "server", Assistant: true, Space: "qa-plain"})
	s := newTestService(t, f)
	ctx := context.Background()
	text := func(id string) string {
		return s.Context(ctx, ContextParams{SessionID: id, MachineID: "server"}).Text
	}
	for id, head := range map[string]string{"health1": "**这是健康空间的对话**", "plain1": "**这是「qa-plain」空间的对话**"} {
		got := text(id)
		if !strings.HasPrefix(got, head) || !strings.Contains(got, "\n- 现在：") || !strings.Contains(got, "- 回答语言：简体中文") {
			t.Errorf("%s: not its track's context:\n%s", id, got)
		}
		if n := utf8.RuneCountInString(got); n > spaceContextMax {
			t.Errorf("%s: %d characters", id, n)
		}
		for _, leak := range []string{"Agents", "在线的电脑", "飞书", "待办", "知识库", "模型档位", "iPhone", "主对话"} {
			if strings.Contains(got, leak) {
				t.Errorf("%s: its context tells %q:\n%s", id, leak, got)
			}
		}
	}
	if got := text("m1"); !strings.HasPrefix(got, "**这是主对话**") || !strings.Contains(got, "- Agents：") {
		t.Errorf("the main conversation lost its snapshot:\n%s", got)
	}
	if got := text("side"); !strings.Contains(got, "- Agents：") || strings.Contains(got, "空间的对话") {
		t.Errorf("a side conversation of the main space lost its snapshot:\n%s", got)
	}
}

func assertSpaceContextOutput(t *testing.T, label, space, got string) {
	t.Helper()
	if space == "" {
		if !strings.Contains(got, "- Agents：") || strings.Contains(got, "空间的对话") {
			t.Fatalf("%s: expected main snapshot, got:\n%s", label, got)
		}
		return
	}
	wantHead := "**这是「" + space + "」空间的对话**"
	switch space {
	case SpaceHealth:
		wantHead = "**这是健康空间的对话**"
	case "qa-plain":
		wantHead = "**这是「QA plain」空间的对话**"
	}
	if !strings.HasPrefix(got, wantHead) || !strings.Contains(got, "\n- 现在：") {
		t.Fatalf("%s: expected space %q context, got:\n%s", label, space, got)
	}
	if n := utf8.RuneCountInString(got); n > spaceContextMax {
		t.Fatalf("%s: %d characters exceeds %d", label, n, spaceContextMax)
	}
	for _, leak := range []string{"Agents", "在线的电脑", "飞书", "待办", "知识库", "模型档位", "iPhone", "主对话"} {
		if strings.Contains(got, leak) {
			t.Fatalf("%s: space %q context leaked %q:\n%s", label, space, leak, got)
		}
	}
}

func nodeContextAnswer(t *testing.T, s *Service, machine string, body map[string]any) ContextResult {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	out, handled, err := s.nodeAnswer(context.Background(), machine, "assistant.context", raw)
	if err != nil || !handled {
		t.Fatalf("assistant.context(%v): handled=%v err=%v", body, handled, err)
	}
	res, ok := out.(ContextResult)
	if !ok {
		t.Fatalf("assistant.context(%v): unexpected result %T", body, out)
	}
	return res
}

// During Start of a new space conversation, the node asks assistant.context before assistant.send returns or
// sessions.changed publishes the row (#17557). Both current nodes (sending cwd, including empty-id Codex starts)
// and older nodes (sending no cwd) must get the space's short context rather than the main chat's snapshot.
func TestSpaceFirstMessageContextDuringStart(t *testing.T) {
	for _, space := range []string{SpaceHealth, "qa-plain", ""} {
		t.Run("space="+space, func(t *testing.T) {
			ctx := context.Background()
			s, hub, _ := spaceMDService(t)
			const sid = "00000000-0000-4000-8000-000000000757"
			var cwd string
			queried := false
			s.hub = &firstToolsTimingHub{Hub: hub, beforeCall: func(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
				if p.Method == DeviceAssistantPrepare {
					var in struct {
						Dir string `json:"dir"`
					}
					if err := json.Unmarshal(p.Params, &in); err != nil {
						return nil, err
					}
					return json.Marshal(map[string]any{"cwd": hub.devices[0].Home + "/" + in.Dir})
				}
				if p.Method != DeviceAssistantSend {
					return hub.DeviceCall(ctx, p)
				}
				var in struct {
					Cwd string `json:"cwd"`
				}
				if err := json.Unmarshal(p.Params, &in); err != nil {
					return nil, err
				}
				cwd, queried = in.Cwd, true
				if _, ok := s.session(ctx, "server", sid); ok {
					t.Fatal("session already published before first context request")
				}
				// Current node: Claude (new session id + cwd) and Codex (empty session id + cwd).
				claudeWithCwd := nodeContextAnswer(t, s, "server", map[string]any{"session_id": sid, "cwd": cwd})
				assertSpaceContextOutput(t, "claude+cwd", space, claudeWithCwd.Text)

				codexWithCwd := nodeContextAnswer(t, s, "server", map[string]any{"session_id": "", "cwd": cwd})
				assertSpaceContextOutput(t, "codex+cwd", space, codexWithCwd.Text)

				// Older node without cwd: both Claude (new session id) and Codex (empty session id).
				claudeOldNode := nodeContextAnswer(t, s, "server", map[string]any{"session_id": sid})
				assertSpaceContextOutput(t, "claude-old-node", space, claudeOldNode.Text)

				codexOldNode := nodeContextAnswer(t, s, "server", map[string]any{"session_id": ""})
				assertSpaceContextOutput(t, "codex-old-node", space, codexOldNode.Text)

				return json.Marshal(map[string]any{"session_id": sid})
			}}
			sess, err := s.Start(ctx, StartParams{Text: "Synthetic first turn", Space: space})
			if err != nil {
				t.Fatal(err)
			}
			if !queried || sess.SessionID != sid || cwd == "" {
				t.Fatalf("Start did not exercise assistant.send: sess=%+v cwd=%q", sess, cwd)
			}
			// After Start returns, the noted space persists even with a contradictory cwd.
			contradictory := "/home/u/.persona/assistant"
			if space == "" {
				contradictory = "/home/u/.persona/tracks/qa-plain"
			}
			after := nodeContextAnswer(t, s, "server", map[string]any{"session_id": sid, "cwd": contradictory})
			assertSpaceContextOutput(t, "post-start-authoritative", space, after.Text)

			// In-flight tracking is cleared once Start finishes: an unrelated unknown session falls back to main.
			unrelated := nodeContextAnswer(t, s, "server", map[string]any{"session_id": "unknown-after-start"})
			assertSpaceContextOutput(t, "post-start-unrelated", "", unrelated.Text)
		})
	}
}

// Concurrent starts in different spaces on the same machine: a node sending cwd gets each conversation's exact
// context; an older node without cwd never leaks the main snapshot to an unknown session while a space start is
// in flight, while an already-known main conversation keeps its snapshot.
func TestSpaceConcurrentStartsContextIsolation(t *testing.T) {
	ctx := context.Background()
	s, hub, _ := spaceMDService(t)
	if _, err := s.updateSettings(ctx, func(v *Settings) error {
		v.MachineID = "server"
		v.Main = "main-existing"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	devices := s.devs.(spaceDevs).fleetDevs
	devices.setSessions("server", []proto.Session{{SessionID: "main-existing", MachineID: "server", Assistant: true}})

	entered := make(chan string, 2)
	release := make(chan struct{})
	s.hub = &firstToolsTimingHub{Hub: hub, beforeCall: func(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
		if p.Method == DeviceAssistantPrepare {
			var in struct {
				Dir string `json:"dir"`
			}
			if err := json.Unmarshal(p.Params, &in); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]any{"cwd": hub.devices[0].Home + "/" + in.Dir})
		}
		if p.Method != DeviceAssistantSend {
			return hub.DeviceCall(ctx, p)
		}
		var in struct {
			Cwd          string `json:"cwd"`
			NewSessionID string `json:"new_session_id"`
		}
		if err := json.Unmarshal(p.Params, &in); err != nil {
			return nil, err
		}
		entered <- in.Cwd
		<-release
		sid := in.NewSessionID
		if sid == "" {
			sid = "started-" + in.Cwd
		}
		return json.Marshal(map[string]any{"session_id": sid})
	}}

	errs := make(chan error, 2)
	go func() {
		_, err := s.Start(ctx, StartParams{Text: "space msg", Space: "qa-plain", NewSessionID: "pre-space-id"})
		errs <- err
	}()
	go func() {
		_, err := s.Start(ctx, StartParams{Text: "main msg", Space: ""})
		errs <- err
	}()

	cwd1, cwd2 := <-entered, <-entered
	var spaceCwd, mainCwd string
	for _, c := range []string{cwd1, cwd2} {
		if strings.Contains(c, "qa-plain") {
			spaceCwd = c
		} else {
			mainCwd = c
		}
	}
	if spaceCwd == "" || mainCwd == "" {
		close(release)
		t.Fatalf("expected one space cwd and one main cwd, got %q and %q", cwd1, cwd2)
	}

	// Current node: cwd disambiguates both concurrent starts accurately.
	assertSpaceContextOutput(t, "concurrent-space-cwd", "qa-plain",
		nodeContextAnswer(t, s, "server", map[string]any{"session_id": "new-space", "cwd": spaceCwd}).Text)
	assertSpaceContextOutput(t, "concurrent-main-cwd", "",
		nodeContextAnswer(t, s, "server", map[string]any{"session_id": "new-main", "cwd": mainCwd}).Text)

	// Pre-assigned NewSessionID disambiguates even without cwd.
	assertSpaceContextOutput(t, "concurrent-preassigned-id", "qa-plain",
		nodeContextAnswer(t, s, "server", map[string]any{"session_id": "pre-space-id"}).Text)

	// Older node without cwd and unknown session id: fail closed (never leak main snapshot).
	ambiguous := nodeContextAnswer(t, s, "server", map[string]any{"session_id": "new-unknown"})
	for _, leak := range []string{"Agents", "在线的电脑", "飞书", "待办", "知识库", "主对话"} {
		if strings.Contains(ambiguous.Text, leak) {
			close(release)
			t.Fatalf("ambiguous concurrent start without cwd leaked %q:\n%s", leak, ambiguous.Text)
		}
	}

	// Already-known main conversation is unaffected by concurrent space starts.
	existingMain := nodeContextAnswer(t, s, "server", map[string]any{"session_id": "main-existing"})
	if !strings.HasPrefix(existingMain.Text, "**这是主对话**") || !strings.Contains(existingMain.Text, "- Agents：") {
		close(release)
		t.Fatalf("existing main conversation lost its snapshot:\n%s", existingMain.Text)
	}

	close(release)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

// A failed start releases its in-flight space record so later requests do not stay pinned to that space.
func TestSpaceFailedStartClearsInFlightSpace(t *testing.T) {
	ctx := context.Background()
	s, hub, _ := spaceMDService(t)
	refused := errors.New("runtime refused to start")
	s.hub = &firstToolsTimingHub{Hub: hub, beforeCall: func(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
		if p.Method == DeviceAssistantPrepare {
			var in struct {
				Dir string `json:"dir"`
			}
			if err := json.Unmarshal(p.Params, &in); err != nil {
				return nil, err
			}
			return json.Marshal(map[string]any{"cwd": hub.devices[0].Home + "/" + in.Dir})
		}
		if p.Method == DeviceAssistantSend {
			inFlight := nodeContextAnswer(t, s, "server", map[string]any{"session_id": "failing-start"})
			assertSpaceContextOutput(t, "in-flight-before-fail", "qa-plain", inFlight.Text)
			return nil, refused
		}
		return hub.DeviceCall(ctx, p)
	}}
	if _, err := s.Start(ctx, StartParams{Text: "will fail", Space: "qa-plain"}); !errors.Is(err, refused) {
		t.Fatalf("expected refused error, got %v", err)
	}
	after := nodeContextAnswer(t, s, "server", map[string]any{"session_id": "after-fail"})
	assertSpaceContextOutput(t, "after-failed-start", "", after.Text)
}
