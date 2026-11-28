package node

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// scriptHub is a node on a fake hub and a socket in front of it (no real node.sock, no phone): the hub's answers to
// interaction.create and notify are the test's.
type scriptHub struct {
	n    *Node
	sock string

	mu             sync.Mutex
	created        []proto.Interaction
	notified       []map[string]any
	cancelled      []string
	codexLists     [][]proto.Interaction
	decisions      []map[string]any
	decisionNames  []string
	decisionAnswer assistantwire.OutsideDecisionReply
	larkSent       []proto.LarkSent // what the node's gate proxy told of (lark.sent)
	lark           proto.AssistantLark

	direction      func(context.Context, string, json.RawMessage) (any, error)
	directionCalls []scriptDirectionCall
}

type scriptDirectionCall struct {
	method string
	params json.RawMessage
}

func newScriptHub(t *testing.T, create func(ctx context.Context, ix proto.Interaction) (any, error), notify func() (any, error)) *scriptHub {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	n, _ := testNode(t)
	n.logger = log.New(io.Discard, "", 0) // the gate proxy's reports may end after the test does, and say so
	h := &scriptHub{n: n}
	n2hR, n2hW := io.Pipe()
	h2nR, h2nW := io.Pipe()
	n.peer = rpc.New("n", n2hW)
	hub := rpc.New("h", h2nW)
	hub.OnRequest = func(ctx context.Context, method string, params json.RawMessage) (any, error) {
		switch method {
		case "interaction.create":
			var p struct {
				Interaction proto.Interaction `json:"interaction"`
			}
			_ = json.Unmarshal(params, &p)
			h.mu.Lock()
			h.created = append(h.created, p.Interaction)
			h.mu.Unlock()
			return create(ctx, p.Interaction)
		case "notify":
			var p map[string]any
			_ = json.Unmarshal(params, &p)
			h.mu.Lock()
			h.notified = append(h.notified, p)
			h.mu.Unlock()
			return notify()
		case "direction.create", "direction.progress":
			h.mu.Lock()
			h.directionCalls = append(h.directionCalls, scriptDirectionCall{method: method, params: params})
			h.mu.Unlock()
			if h.direction != nil {
				return h.direction(ctx, method, params)
			}
			return nil, nil
		case assistantwire.MethodDecisionRaise, assistantwire.MethodDecisionGet, assistantwire.MethodDecisionWithdraw:
			var p map[string]any
			_ = json.Unmarshal(params, &p)
			h.mu.Lock()
			h.decisions = append(h.decisions, p)
			h.decisionNames = append(h.decisionNames, method)
			answer := h.decisionAnswer
			h.mu.Unlock()
			return answer, nil
		case "assistant.lark":
			h.mu.Lock()
			cfg := h.lark
			h.mu.Unlock()
			return cfg, nil
		case proto.MethodLarkSent:
			var s proto.LarkSent
			_ = json.Unmarshal(params, &s)
			h.mu.Lock()
			h.larkSent = append(h.larkSent, s)
			h.mu.Unlock()
			return struct{}{}, nil
		}
		return nil, nil
	}
	hub.OnEvent = func(method string, params json.RawMessage) {
		if method == "codex.interactions" {
			var p struct {
				Interactions []proto.Interaction `json:"interactions"`
			}
			_ = json.Unmarshal(params, &p)
			h.mu.Lock()
			h.codexLists = append(h.codexLists, p.Interactions)
			h.mu.Unlock()
		}
		var p struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(params, &p)
		if method == "interaction.cancel" {
			h.mu.Lock()
			h.cancelled = append(h.cancelled, p.ID)
			h.mu.Unlock()
		}
	}
	go func() { _ = hub.Serve(ctx, n2hR) }()
	// The peer read here, not in the goroutine: a test sets n.peer = nil for "no hub" while this one serves.
	peer := n.peer
	go func() { _ = peer.Serve(ctx, h2nR) }()
	t.Cleanup(func() { cancel(); _ = n2hW.Close(); _ = h2nW.Close() })

	// Not t.TempDir(): with a subtest's long name the socket path passes sun_path, 104 bytes on macOS (#2278).
	dir, err := os.MkdirTemp("/tmp", "sh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	h.sock = filepath.Join(dir, "s")
	ln, err := net.Listen("unix", h.sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go n.serveSock(ctx, c)
		}
	}()
	return h
}

func TestDecisionScriptSocketGetAndWithdraw(t *testing.T) {
	h := newScriptHub(t, answerWith(proto.Resolution{}), notifyOK)
	const id = "odc_example"
	h.mu.Lock()
	h.decisionAnswer = assistantwire.OutsideDecisionReply{Decision: &assistantwire.OutsideDecision{ID: id}}
	h.mu.Unlock()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	for _, call := range []struct {
		method string
		run    func(context.Context, nodeclient.Dialer, string, string) (assistantwire.OutsideDecisionReply, error)
	}{
		{"decision.get", nodeclient.DecisionGetNode},
		{"decision.withdraw", nodeclient.DecisionWithdrawNode},
	} {
		reply, err := call.run(ctx, h.dial, id, "w1:p2")
		if err != nil || reply.Decision == nil || reply.Decision.ID != id {
			t.Fatalf("%s: %+v, %v", call.method, reply, err)
		}
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.decisionNames) != 2 || h.decisionNames[0] != "decision.get" || h.decisionNames[1] != "decision.withdraw" {
		t.Fatalf("manager methods: %v", h.decisionNames)
	}
	for i, params := range h.decisions {
		if len(params) != 1 || params["id"] != id {
			t.Errorf("%s params: %v", h.decisionNames[i], params)
		}
	}
}

func TestDecisionScriptRejectsBeforeForwarding(t *testing.T) {
	h := newScriptHub(t, answerWith(proto.Resolution{}), notifyOK)
	h.n.setBlockedAgentDirs([]string{"/work/blocked"})
	for _, tc := range []struct {
		name, method, input, code, message string
	}{
		{"malformed raise", "decision.raise", `{"decision":1}`, "invalid_decision", "bad request"},
		{"empty id", "decision.get", `{"id":""}`, "invalid_decision", "a decision id is required"},
		{"invalid id", "decision.withdraw", `{"id":1}`, "invalid_decision", "a decision id is required"},
		{"blocked folder", "decision.raise", `{"decision":{},"cwd":"/work/blocked"}`, "forbidden", "this folder is blocked in Persona"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := assistantwire.OutsideDecisionReply{Code: tc.code, Error: tc.message}
			if got := h.n.handleDecision(t.Context(), sockRequest{Kind: tc.method, Input: json.RawMessage(tc.input)}); got != want {
				t.Errorf("reply = %+v, want %+v", got, want)
			}
		})
	}
	h.n.peer.SuspendCalls()
	want := assistantwire.OutsideDecisionReply{Code: "unavailable", Error: rpc.ErrSuspended.Error()}
	if got := h.n.handleDecision(t.Context(), sockRequest{Kind: "decision.get", Input: json.RawMessage(`{"id":"odc_example"}`)}); got != want {
		t.Errorf("suspended peer reply = %+v, want %+v", got, want)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.decisions) != 0 {
		t.Fatalf("rejected requests reached the manager: %v", h.decisions)
	}
}

// dial is nodeclient.Dialer over the test's socket.
func (h *scriptHub) dial(ctx context.Context, req any) ([]byte, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "unix", h.sock)
	if err != nil {
		return nil, err
	}
	defer c.Close()
	defer context.AfterFunc(ctx, func() { c.Close() })() // as nodeclient.Dial: a cancelled ctx hangs up
	b, _ := json.Marshal(req)
	if _, err := c.Write(append(b, '\n')); err != nil {
		return nil, err
	}
	return bufio.NewReader(c).ReadBytes('\n')
}

func (h *scriptHub) lastCreated(t *testing.T) proto.Interaction {
	t.Helper()
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.created) == 0 {
		t.Fatal("the hub was asked nothing")
	}
	return h.created[len(h.created)-1]
}

func answerWith(res proto.Resolution) func(context.Context, proto.Interaction) (any, error) {
	return func(context.Context, proto.Interaction) (any, error) { return res, nil }
}

func notifyOK() (any, error) { return map[string]any{"sent": true}, nil }

func boolp(b bool) *bool { return &b }

// rawScriptAsk bypasses the typed client so a new input field can be tested before nodeclient knows it.
func rawScriptAsk(t *testing.T, h *scriptHub, input string) nodeclient.AskReply {
	t.Helper()
	line, err := h.dial(context.Background(), nodeclient.Request{Kind: "ask", Input: json.RawMessage(input)})
	if err != nil {
		t.Fatal(err)
	}
	var rep nodeclient.AskReply
	if err := json.Unmarshal(line, &rep); err != nil {
		t.Fatalf("bad reply %q: %v", line, err)
	}
	return rep
}

// scriptInteractionFormat reads the interaction's public wire shape without requiring the new Go field to exist yet.
func scriptInteractionFormat(t *testing.T, ix proto.Interaction) string {
	t.Helper()
	line, err := json.Marshal(ix)
	if err != nil {
		t.Fatal(err)
	}
	var wire struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(line, &wire); err != nil {
		t.Fatal(err)
	}
	return wire.Format
}

// A question with options is the question card AskUserQuestion already is, from "Script" (in Chinese), under the session of the
// caller's pane; the option chosen comes back.
func TestScriptAskOptionsIsAQuestionCard(t *testing.T) {
	h := newScriptHub(t, answerWith(proto.Resolution{Answers: map[string]string{"Deploy where?": "staging"}}), notifyOK)
	h.n.panes = map[string]string{"w1:p1": "sess-1"}
	rep, err := nodeclient.AskNode(context.Background(), h.dial, nodeclient.AskRequest{Question: "Deploy where?", Options: []string{"staging", " prod ", "staging", ""}, Cwd: "/work/app"}, "w1:p1")
	if err != nil || rep.Status != nodeclient.AskAnswered || rep.Answer != "staging" {
		t.Fatalf("reply %+v, %v", rep, err)
	}
	ix := h.lastCreated(t)
	if ix.Kind != proto.IxQuestion || len(ix.Questions) != 1 || ix.Questions[0].Question != "Deploy where?" || ix.Questions[0].MultiSelect {
		t.Fatalf("interaction %+v", ix)
	}
	var labels []string
	for _, o := range ix.Questions[0].Options {
		labels = append(labels, o.Label)
	}
	if len(labels) != 2 || labels[0] != "staging" || labels[1] != "prod" {
		t.Fatalf("options %v (trimmed, without repeats)", labels)
	}
	if ix.SessionID != "sess-1" || ix.Agent != "script" || ix.AgentLabel != "脚本" || ix.Cwd != "/work/app" || !ix.Resumable {
		t.Fatalf("attribution: %+v", ix)
	}
	if ix.SessionTitle != "app" {
		t.Fatalf("title %q: the directory when no transcript names the session", ix.SessionTitle)
	}
	if time.Until(ix.ExpiresAt) < 29*time.Minute {
		t.Fatalf("expires %v: the hook's 30 minutes by default", ix.ExpiresAt)
	}
}

// A card typed into ("Other") answers with what was typed.
func TestScriptAskTypedAnswer(t *testing.T) {
	h := newScriptHub(t, answerWith(proto.Resolution{Answers: map[string]string{"other key": " 手写的 "}}), notifyOK)
	rep, err := nodeclient.AskNode(context.Background(), h.dial, nodeclient.AskRequest{Question: "q", Options: []string{"a", "b"}}, "")
	if err != nil || rep.Status != nodeclient.AskAnswered || rep.Answer != "手写的" {
		t.Fatalf("reply %+v, %v", rep, err)
	}
	if ix := h.lastCreated(t); ix.SessionID != "" {
		t.Fatalf("no pane, no session: %q", ix.SessionID)
	}
}

// Without options it is an allow / deny permission card, so the phone's buttons (and the notification's) work.
func TestScriptAskWithoutOptionsIsAPermissionCard(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  proto.Resolution
		want string
	}{
		{"allow", proto.Resolution{Allow: boolp(true)}, nodeclient.AskAnswered},
		{"deny", proto.Resolution{Allow: boolp(false), Message: "no"}, nodeclient.AskDenied},
		{"handed back", proto.Resolution{Dismiss: true}, nodeclient.AskDismissed},
		{"no verdict", proto.Resolution{}, nodeclient.AskDismissed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newScriptHub(t, answerWith(tc.res), notifyOK)
			rep, err := nodeclient.AskNode(context.Background(), h.dial, nodeclient.AskRequest{Question: "Run the migration?"}, "")
			if err != nil || rep.Status != tc.want || rep.Answer != "" {
				t.Fatalf("reply %+v, %v", rep, err)
			}
			ix := h.lastCreated(t)
			if ix.Kind != proto.IxPermission || ix.Summary != "Run the migration?" || ix.Agent != "script" || len(ix.Questions) != 0 {
				t.Fatalf("interaction %+v", ix)
			}
		})
	}
}

// Markdown is a per-ask opt-in carried on either kind of interaction for the phone and web to render.
func TestScriptAskMarkdownFormat(t *testing.T) {
	for _, tc := range []struct {
		name     string
		input    string
		res      proto.Resolution
		kind     string
		question string
		answer   string
	}{
		{
			name:     "permission",
			input:    `{"question":"**Deploy?**\n\n- migrates the database","format":"markdown"}`,
			res:      proto.Resolution{Allow: boolp(true)},
			kind:     proto.IxPermission,
			question: "**Deploy?**\n\n- migrates the database",
		},
		{
			name:     "question",
			input:    `{"question":"**Deploy where?**\n\n- keeps the data","options":["staging","prod"],"format":"markdown"}`,
			res:      proto.Resolution{Answers: map[string]string{"**Deploy where?**\n\n- keeps the data": "staging"}},
			kind:     proto.IxQuestion,
			question: "**Deploy where?**\n\n- keeps the data",
			answer:   "staging",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newScriptHub(t, answerWith(tc.res), notifyOK)
			rep := rawScriptAsk(t, h, tc.input)
			if rep.Status != nodeclient.AskAnswered || rep.Answer != tc.answer {
				t.Fatalf("reply %+v", rep)
			}
			ix := h.lastCreated(t)
			if ix.Kind != tc.kind {
				t.Fatalf("kind %q, want %q", ix.Kind, tc.kind)
			}
			question := ix.Summary
			if ix.Kind == proto.IxQuestion {
				if len(ix.Questions) != 1 {
					t.Fatalf("questions %+v", ix.Questions)
				}
				question = ix.Questions[0].Question
			}
			if question != tc.question {
				t.Fatalf("question %q, want the original Markdown", question)
			}
			if got := scriptInteractionFormat(t, ix); got != "markdown" {
				t.Fatalf("format %q, want markdown; interaction %s", got, mustJSON(ix))
			}
		})
	}
}

// Omitted, plain and unrecognized formats retain the existing literal-text cards; only markdown opts in.
func TestScriptAskFormatDefaultsToPlain(t *testing.T) {
	const question = "**Keep this literal?**"
	for _, shape := range []struct {
		name    string
		options string
		res     proto.Resolution
		kind    string
	}{
		{name: "permission", res: proto.Resolution{Allow: boolp(true)}, kind: proto.IxPermission},
		{name: "question", options: `,"options":["yes","no"]`, res: proto.Resolution{Answers: map[string]string{question: "yes"}}, kind: proto.IxQuestion},
	} {
		for _, format := range []struct {
			name  string
			input string
		}{
			{name: "omitted"},
			{name: "plain", input: `,"format":"plain"`},
			{name: "unknown", input: `,"format":"html"`},
		} {
			t.Run(shape.name+"/"+format.name, func(t *testing.T) {
				h := newScriptHub(t, answerWith(shape.res), notifyOK)
				rep := rawScriptAsk(t, h, `{"question":"`+question+`"`+shape.options+format.input+`}`)
				if rep.Status != nodeclient.AskAnswered {
					t.Fatalf("reply %+v", rep)
				}
				ix := h.lastCreated(t)
				if ix.Kind != shape.kind {
					t.Fatalf("kind %q, want %q", ix.Kind, shape.kind)
				}
				gotQuestion := ix.Summary
				if ix.Kind == proto.IxQuestion {
					gotQuestion = ix.Questions[0].Question
				}
				if gotQuestion != question {
					t.Fatalf("question %q, want the unchanged literal text", gotQuestion)
				}
				if got := scriptInteractionFormat(t, ix); got != "" && got != "plain" {
					t.Fatalf("format %q, want the default plain behavior", got)
				}
			})
		}
	}
}

// Nobody answering is a timeout (not "unreachable"), and the card is withdrawn from the phone.
func TestScriptAskTimeout(t *testing.T) {
	h := newScriptHub(t, func(ctx context.Context, _ proto.Interaction) (any, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}, notifyOK)
	start := time.Now()
	rep, err := nodeclient.AskNode(context.Background(), h.dial, nodeclient.AskRequest{Question: "q", TimeoutSeconds: 1}, "")
	if err != nil || rep.Status != nodeclient.AskTimeout {
		t.Fatalf("reply %+v, %v", rep, err)
	}
	if d := time.Since(start); d < 900*time.Millisecond || d > 5*time.Second {
		t.Fatalf("took %v for a 1s timeout", d)
	}
}

// The script going away (Ctrl-C) takes the card off the phone.
func TestScriptAskCancelledWithTheCaller(t *testing.T) {
	asked := make(chan struct{})
	h := newScriptHub(t, func(ctx context.Context, _ proto.Interaction) (any, error) {
		close(asked)
		<-ctx.Done()
		return nil, ctx.Err()
	}, notifyOK)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := nodeclient.AskNode(ctx, h.dial, nodeclient.AskRequest{Question: "q"}, "")
		done <- err
	}()
	<-asked
	cancel()
	<-done
	waitFor(t, "the card to be withdrawn", func() bool {
		h.mu.Lock()
		defer h.mu.Unlock()
		return len(h.cancelled) == 1 && h.cancelled[0] == h.created[0].ID
	})
}

func TestScriptAskUnavailableAndInvalid(t *testing.T) {
	h := newScriptHub(t, answerWith(proto.Resolution{}), notifyOK)
	rep, _ := nodeclient.AskNode(context.Background(), h.dial, nodeclient.AskRequest{Question: "   "}, "")
	if rep.Status != nodeclient.AskInvalid || rep.Error == "" {
		t.Fatalf("an empty question: %+v", rep)
	}
	if rep, _ = nodeclient.AskNode(context.Background(), h.dial, nodeclient.AskRequest{Question: "q", TimeoutSeconds: -1}, ""); rep.Status != nodeclient.AskInvalid {
		t.Fatalf("a negative timeout: %+v", rep)
	}
	many := make([]string, nodeclient.AskOptionsMost+1)
	for i := range many {
		many[i] = string(rune('a' + i))
	}
	if rep, _ = nodeclient.AskNode(context.Background(), h.dial, nodeclient.AskRequest{Question: "q", Options: many}, ""); rep.Status != nodeclient.AskInvalid {
		t.Fatalf("too many options: %+v", rep)
	}
	h.mu.Lock()
	asked := len(h.created)
	h.mu.Unlock()
	if asked != 0 {
		t.Fatalf("the hub was asked %d invalid questions", asked)
	}

	h.n.peer = nil // a node with no hub
	if rep, _ = nodeclient.AskNode(context.Background(), h.dial, nodeclient.AskRequest{Question: "q"}, ""); rep.Status != nodeclient.AskUnavailable {
		t.Fatalf("no hub: %+v", rep)
	}
}

// A hub that fails the request (not a timeout, not a hand back) is "unreachable" to the script.
func TestScriptAskHubFailure(t *testing.T) {
	h := newScriptHub(t, func(context.Context, proto.Interaction) (any, error) {
		return nil, &rpc.Error{Message: "boom"}
	}, notifyOK)
	rep, err := nodeclient.AskNode(context.Background(), h.dial, nodeclient.AskRequest{Question: "q"}, "")
	if err != nil || rep.Status != nodeclient.AskUnavailable {
		t.Fatalf("reply %+v, %v", rep, err)
	}
}

// An ask whose node goes away reconnects with the same attempt and finds the same card id; no node at all is nodeclient.ErrNoNode.
func TestAskNodeResumes(t *testing.T) {
	defer func(d time.Duration) { nodeclient.ResumeRetry = d }(nodeclient.ResumeRetry)
	nodeclient.ResumeRetry = 10 * time.Millisecond
	got := fakeNodes(t,
		nil, // the node dies while the phone decides
		map[string]any{"retry": true},
		map[string]any{"status": "answered", "answer": "b"},
	)
	rep, err := nodeclient.AskNode(context.Background(), nodeclient.Dial, nodeclient.AskRequest{Question: "q", Options: []string{"a", "b"}}, "w1:p1")
	if err != nil || rep.Status != nodeclient.AskAnswered || rep.Answer != "b" {
		t.Fatalf("reply %+v, %v", rep, err)
	}
	first, second, third := <-got, <-got, <-got
	if first.Kind != "ask" || first.Attempt == "" || first.Attempt != second.Attempt || second.Attempt != third.Attempt {
		t.Fatalf("attempts %+v %+v %+v", first, second, third)
	}
	if first.Resume || !second.Resume || !third.Resume || first.Pane != "w1:p1" {
		t.Fatalf("only reconnects are resumes: %+v %+v %+v", first, second, third)
	}
	if scriptInteractionID(first.Attempt) != scriptInteractionID(third.Attempt) || scriptInteractionID("") == scriptInteractionID("") {
		t.Fatal("one attempt is one card; no attempt is a random one")
	}
}

func TestAskNodeGivesUpAfterResumeWait(t *testing.T) {
	defer func(d, w time.Duration) { nodeclient.ResumeRetry, nodeclient.ResumeWait = d, w }(nodeclient.ResumeRetry, nodeclient.ResumeWait)
	nodeclient.ResumeRetry, nodeclient.ResumeWait = 10*time.Millisecond, 50*time.Millisecond
	fakeNodes(t, nil) // dies, and no node comes back
	if _, err := nodeclient.AskNode(context.Background(), nodeclient.Dial, nodeclient.AskRequest{Question: "q"}, ""); err == nil {
		t.Fatal("the phone could not be asked: an error")
	}
}

func TestAskNodeWithoutNode(t *testing.T) {
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	if _, err := nodeclient.AskNode(context.Background(), nodeclient.Dial, nodeclient.AskRequest{Question: "q"}, ""); err == nil || !isNoNode(err) {
		t.Fatalf("err %v, want nodeclient.ErrNoNode", err)
	}
	if _, err := nodeclient.NotifyNode(context.Background(), nodeclient.Dial, nodeclient.NotifyRequest{Title: "t"}, ""); err == nil || !isNoNode(err) {
		t.Fatalf("err %v, want nodeclient.ErrNoNode", err)
	}
}

// Only nodeclient.DialInterruptible hangs up on a cancelled ctx (Ctrl-C in personad ask); nodeclient.Dial waits as it always did
// (a hook, the MCP server and the launcher end by the deadline or the node's answer).
func TestDialSocketCancel(t *testing.T) {
	home, err := os.MkdirTemp("", "jh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(home) })
	t.Setenv("PERSONA_HOME", "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	_ = os.MkdirAll(filepath.Join(home, ".persona"), 0o700)
	ln, err := net.Listen("unix", nodeclient.SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	conns := make(chan net.Conn, 2)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// A node that reads the request and never answers. The listener accepts before the client's DialContext
			// has returned, and a cancel in that gap fails the dial itself (net checks ctx once the connect is
			// done); the request line is written only after DialContext returns, so having read it means the
			// client is connected and the cancel lands where the test means it to: while waiting for the answer.
			_, _ = bufio.NewReader(c).ReadBytes('\n')
			conns <- c
		}
	}()

	// The node's ends are held until the test is over: one nothing refers to is closed when the garbage collector
	// finalizes it, and the dial then reads EOF (or a reset) as if the node had hung up.
	var held []net.Conn
	t.Cleanup(func() {
		for _, c := range held {
			c.Close()
		}
	})
	dial := func(f nodeclient.Dialer) (cancel func(), done chan error) {
		ctx, cancel := context.WithCancel(context.Background())
		done = make(chan error, 1)
		go func() { _, err := f(ctx, map[string]any{"kind": "x"}); done <- err }()
		held = append(held, <-conns)
		return cancel, done
	}
	cancel, done := dial(nodeclient.DialInterruptible)
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a hung-up dial is an error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nodeclient.DialInterruptible kept waiting after its ctx was cancelled")
	}

	cancel, done = dial(nodeclient.Dial)
	cancel()
	select {
	case err := <-done:
		t.Fatalf("nodeclient.Dial gave up on a cancelled ctx: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
}

func isNoNode(err error) bool { return err != nil && errors.Is(err, nodeclient.ErrNoNode) }

// A notification goes to the hub as the notify request the HTTP API takes: the session of the caller's pane, the
// level and the sound as asked, long texts cut to the hub's limits.
func TestScriptNotify(t *testing.T) {
	h := newScriptHub(t, answerWith(proto.Resolution{}), notifyOK)
	h.n.panes = map[string]string{"w1:p1": "sess-1"}
	rep, err := nodeclient.NotifyNode(context.Background(), h.dial, nodeclient.NotifyRequest{Title: "Build done", Body: "all green", Silent: true}, "w1:p1")
	if err != nil || !rep.Sent || rep.Error != "" {
		t.Fatalf("reply %+v, %v", rep, err)
	}
	long := make([]rune, 3000)
	for i := range long {
		long[i] = '字'
	}
	if _, err = nodeclient.NotifyNode(context.Background(), h.dial, nodeclient.NotifyRequest{Title: "Tests failed", Body: string(long), Error: true}, ""); err != nil {
		t.Fatal(err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.notified) != 2 {
		t.Fatalf("notified %v", h.notified)
	}
	a, b := h.notified[0], h.notified[1]
	if a["title"] != "Build done" || a["body"] != "all green" || a["level"] != "active" || a["silent"] != true || a["session_id"] != "sess-1" {
		t.Fatalf("first %v", a)
	}
	if b["title"] != "⚠️ Tests failed" || b["level"] != "time-sensitive" || b["silent"] != false {
		t.Fatalf("second %v", b)
	}
	if _, has := b["session_id"]; has {
		t.Fatalf("no pane, no session: %v", b)
	}
	if got := len([]rune(b["body"].(string))); got != notifyBodyMost {
		t.Fatalf("body of %d characters, want it cut to %d", got, notifyBodyMost)
	}
}

func TestScriptNotifyFailures(t *testing.T) {
	h := newScriptHub(t, answerWith(proto.Resolution{}), func() (any, error) {
		return map[string]any{"sent": false, "reason": "muted"}, nil
	})
	rep, err := nodeclient.NotifyNode(context.Background(), h.dial, nodeclient.NotifyRequest{Title: "t"}, "")
	if err != nil || rep.Sent || rep.Reason != "muted" {
		t.Fatalf("muted: %+v, %v", rep, err)
	}
	if rep, _ = nodeclient.NotifyNode(context.Background(), h.dial, nodeclient.NotifyRequest{Title: " "}, ""); rep.Sent || rep.Error == "" {
		t.Fatalf("empty: %+v", rep)
	}
	h2 := newScriptHub(t, answerWith(proto.Resolution{}), func() (any, error) { return nil, &rpc.Error{Message: "unknown method notify"} })
	if rep, _ = nodeclient.NotifyNode(context.Background(), h2.dial, nodeclient.NotifyRequest{Title: "t"}, ""); rep.Sent || rep.Error == "" {
		t.Fatalf("an older hub: %+v", rep)
	}
	h2.n.peer = nil
	if rep, _ = nodeclient.NotifyNode(context.Background(), h2.dial, nodeclient.NotifyRequest{Title: "t"}, ""); rep.Sent || rep.Error == "" {
		t.Fatalf("no hub: %+v", rep)
	}
}
