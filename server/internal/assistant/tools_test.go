package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// shortAgentStartTimeout makes the start wait and adoption pause expire in milliseconds, not the production bounds.
func shortAgentStartTimeout(t *testing.T) {
	t.Helper()
	oldTimeout, oldRetry := agentStartTimeout, agentStartAdoptRetry
	agentStartTimeout, agentStartAdoptRetry = 30*time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() { agentStartTimeout, agentStartAdoptRetry = oldTimeout, oldRetry })
}

// The node built the session and launched the agent but its answer never came back: not a failure, and the model
// must not start a second copy. The start itself is attempted exactly once.
func TestAgentStartSlowAnswerIsUnknownNotFailure(t *testing.T) {
	s, f := newTaskService(t)
	shortAgentStartTimeout(t)
	f.slowStart = true
	res, err := s.runTool(context.Background(), "server", "conv9", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI"}`))
	if err != nil {
		t.Fatalf("unknown start result must not be an error: %v", err)
	}
	if !strings.Contains(res, "may already be starting") || !strings.Contains(res, "Do NOT start it again") ||
		!strings.Contains(res, "Agents tab") {
		t.Fatalf("neutral text missing guidance:\n%s", res)
	}
	if len(f.starts) != 1 {
		t.Fatalf("the start was attempted %d times, want 1 (no automatic retry)", len(f.starts))
	}
}

// A late node whose session list shows the new session has the start confirmed and linked like an answered one.
func TestAgentStartAdoptsTheSessionThatStartedLate(t *testing.T) {
	s, f := newQuietService(t)
	shortAgentStartTimeout(t)
	f.slowStart = true
	f.onStart = func(f *fakeHub, a map[string]any) { // the node adds the session (with its launcher) while handling the late start
		f.sessions["box"] = []proto.Session{{SessionID: "s9", MachineID: "box", Cwd: "/code/app", Agent: "claude",
			Launcher: a["launcher"].(string), StartedByPersona: true, Status: proto.StatusWorking,
			State: proto.StateWorking, UpdatedAt: time.Now().UTC()}}
	}
	res, err := s.runTool(context.Background(), "server", "conv1", "agent_start",
		json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI"}`))
	if err != nil {
		t.Fatalf("adopting the started session must not error: %v", err)
	}
	if !strings.Contains(res, "s9") || !strings.Contains(res, "confirmed from the machine's own list") {
		t.Fatalf("result does not confirm the adopted session:\n%s", res)
	}
	if !strings.Contains(res, "a task of this conversation") {
		t.Fatalf("an adopted quiet task must be described like a normal quiet start:\n%s", res)
	}
	todo := onlyTodo(s)
	if len(todo.Agents) != 1 || todo.Agents[0].SessionID != "s9" {
		t.Fatalf("adopted session not linked as a quiet task: %+v", todo)
	}
}

// Definite failures stay errors: a request the node refused before running anything, and an unreachable machine
// (nothing was started) — never a neutral "may be starting" result.
func TestAgentStartDefiniteFailuresStayErrors(t *testing.T) {
	cases := []struct {
		name string
		set  func(*fakeHub)
		want func(error) bool
	}{
		{"refused", func(f *fakeHub) { f.failStart = &rpc.Error{Code: "open_elsewhere", Message: "open elsewhere"} },
			func(err error) bool { var re *rpc.Error; return errors.As(err, &re) && re.Code == "open_elsewhere" }},
		{"offline", func(f *fakeHub) { f.offline = map[string]bool{"box": true} },
			func(err error) bool { return errors.Is(err, apierr.ErrOffline) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, f := newTaskService(t)
			tc.set(f)
			res, err := s.runTool(context.Background(), "server", "conv9", "agent_start",
				json.RawMessage(`{"machine":"box","cwd":"/code/app","prompt":"看看 CI"}`))
			if err == nil || !tc.want(err) {
				t.Fatalf("%s: got res=%q err=%v", tc.name, res, err)
			}
		})
	}
}

// Which failures leave the start's outcome unknown, and which are definite.
func TestStartResultUnknown(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"deadline", context.DeadlineExceeded, true},
		{"canceled", context.Canceled, true},
		{"closed", rpc.ErrClosed, true},
		{"dropped", rpc.ErrDropped, true},
		{"relay words", &rpc.Error{Message: rpc.ErrClosed.Error()}, true},
		{"wrapped deadline", fmt.Errorf("session.start: %w", context.DeadlineExceeded), true},
		{"coded refusal", &rpc.Error{Code: "open_elsewhere", Message: "open elsewhere"}, false},
		{"offline", apierr.ErrOffline, false},
		{"no device", ErrNoDevice, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := startResultUnknown(tc.err); got != tc.want {
				t.Fatalf("startResultUnknown(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// The adoption filter: new, hub-started, non-assistant, same launcher and directory, fresh sign of life.
func TestAdoptStartedSessionFilters(t *testing.T) {
	s, f := newTaskService(t)
	now := time.Now().UTC()
	mk := func(id string, fn func(*proto.Session)) proto.Session {
		sess := proto.Session{SessionID: id, MachineID: "box", Agent: "claude", Launcher: "claude-code",
			Cwd: "/code/app", StartedByPersona: true, UpdatedAt: now}
		fn(&sess)
		return sess
	}
	known := map[string]bool{"old": true}
	dev := Device{ID: "box"}
	// Adopted: a new cwd session, and a new one whose node-verified WorkspaceCwd is the directory.
	for _, sess := range []proto.Session{
		mk("new", func(*proto.Session) {}),
		mk("new", func(s *proto.Session) { s.Cwd = ""; s.WorkspaceCwd = "/code/app" }),
	} {
		f.setSessions("box", mk("old", func(*proto.Session) {}), sess)
		if got, ok := s.adoptStartedSession(context.Background(), dev, "claude-code", "/code/app", "", known); !ok || got.SessionID != "new" {
			t.Fatalf("got %+v ok=%v, want new adopted", got, ok)
		}
	}

	// Rejected: pre-existing, not hub-started, the assistant, other launcher/cwd, stale or never-seen sign of life.
	for _, tc := range []struct {
		name string
		fn   func(*proto.Session)
	}{
		{"not hub-started", func(s *proto.Session) { s.StartedByPersona = false }},
		{"the assistant itself", func(s *proto.Session) { s.Assistant = true }},
		{"other launcher", func(s *proto.Session) { s.Launcher = "codex" }},
		{"other directory", func(s *proto.Session) { s.Cwd = "/elsewhere" }},
		{"stale sign of life", func(s *proto.Session) { s.UpdatedAt = now.Add(-agentStartAdoptWindow - time.Minute) }},
		{"never seen", func(s *proto.Session) { s.UpdatedAt = time.Time{} }},
	} {
		f.setSessions("box", mk("old", func(*proto.Session) {}), mk("new", tc.fn))
		if _, ok := s.adoptStartedSession(context.Background(), dev, "claude-code", "/code/app", "", known); ok {
			t.Fatalf("%s session was adopted", tc.name)
		}
	}

	// A pre-start list that failed means no guessing afterwards.
	f.setSessions("box", mk("new", func(*proto.Session) {}))
	if _, ok := s.adoptStartedSession(context.Background(), dev, "claude-code", "/code/app", "", nil); ok {
		t.Fatal("adoption happened without a pre-start snapshot")
	}
}

// ask_user's answers come back keyed by question text: two questions worded alike (after trimming) are refused before
// any card goes on the phone, instead of one answer silently overwriting the other (#4517). Distinct wording still asks.
func TestAskUserRefusesDuplicateQuestionText(t *testing.T) {
	s := newTestService(t, newFakeHub())
	q := func(text string) map[string]any {
		return map[string]any{"question": text, "options": []map[string]any{{"label": "好"}, {"label": "不好"}}}
	}
	call := func(texts ...string) (proto.ToolResult, bool) {
		var qs []map[string]any
		for _, x := range texts {
			qs = append(qs, q(x))
		}
		raw, _ := json.Marshal(map[string]any{"questions": qs})
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		carded := make(chan bool, 1)
		go func() {
			for ctx.Err() == nil {
				if qs := s.pendingQuestions(); len(qs) == 1 {
					yes := map[string]string{}
					for _, x := range qs[0].Questions {
						yes[x.Question] = "好"
					}
					raw, _ := json.Marshal(proto.Resolution{Answers: yes})
					_, _ = s.Action(context.Background(), Action{Op: "answer", InteractionID: qs[0].ID, Answer: raw})
					carded <- true
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
			carded <- false
		}()
		r := s.Tool(ctx, ToolCall{Name: "ask_user", Arguments: raw, MachineID: "mac", SessionID: "conv-assistant"})
		cancel()
		return r, <-carded
	}
	text := func(r proto.ToolResult) string {
		var b strings.Builder
		for _, c := range r.Content {
			b.WriteString(c.Text)
		}
		return b.String()
	}

	r, carded := call("发吗？", "  发吗？ ")
	if !r.IsError || text(r) != i18n.Err("hub-hub.duplicate_question").Error() {
		t.Fatalf("duplicate questions: error=%v %q", r.IsError, text(r))
	}
	if carded {
		t.Fatal("a card went on the phone for duplicate questions")
	}

	r, carded = call("发吗？", "现在发吗？")
	if r.IsError || !carded || !strings.Contains(text(r), "发吗？ → 好") || !strings.Contains(text(r), "现在发吗？ → 好") {
		t.Fatalf("distinct questions: carded=%v error=%v %q", carded, r.IsError, text(r))
	}
}

// agent_send's readonly refusal is original MCP model text, not a localized user-facing notification or card.
func TestAgentSendReadonlyPreservesModelResult(t *testing.T) {
	s, f := newTaskService(t)
	const reason = "Original source diagnostic / 原始原因 / 元の理由"
	for _, caps := range []*proto.SessionCapabilities{nil, {Read: true}} {
		f.setSessions("box", proto.Session{SessionID: "readonly-agent", MachineID: "box", Cwd: "/code/app",
			SourceKind: "external-history", Capabilities: caps, ReadOnlyReason: reason})
		for _, locale := range []string{"en", "zh-Hans", "ja", "en"} {
			f.mu.Lock()
			f.hello.Locale = locale
			f.mu.Unlock()
			res := s.Tool(i18n.With(context.Background(), i18n.Lang(locale)), ToolCall{
				MachineID: "server", SessionID: "conv9", Name: "agent_send",
				Arguments: json.RawMessage(`{"machine":"box","session_id":"readonly-agent","text":"Continue"}`),
			})
			if !res.IsError || len(res.Content) != 1 || res.Content[0].Text != "这个 Agent 仅支持查看："+reason {
				t.Fatalf("locale=%q capabilities=%+v: %+v", locale, caps, res)
			}
		}
	}
	if sent := sentToAgent(f); len(sent) != 0 {
		t.Fatalf("readonly agent received messages: %v", sent)
	}
	if len(s.pendingQuestions()) != 0 || len(f.pushed()) != 0 {
		t.Fatal("model-only refusal created a user-facing card or push")
	}
}
