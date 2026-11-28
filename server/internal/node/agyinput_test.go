package node

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/screen"
)

// The output of a sign-in that waits for the code it asks for, as agy's pseudo-terminal writes it: CRLF line ends,
// the prompt last with no newline after it.
const agyTestSignIn = "\r\nTo sign in, visit this URL in any browser:\r\n\r\n  https://auth.example.com/authorize?client=demo\r\n\r\n" +
	"After approving, the page shows a code.\r\nEnter the authorization code: "

// agyInputFixture is an agy pane whose conversation runs task-7, a background task whose output is out.
type agyInputFixture struct {
	n     *Node
	f     *fakeHerdr
	brain string // <agy data>/brain/<conversation>/.system_generated
	log   string
	logs  *syncBuffer // the node's log
}

func newAgyInputFixture(t *testing.T, out string) *agyInputFixture {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("agy's environment is not read on Windows")
	}
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))
	step, _ := json.Marshal(map[string]any{"step_index": 1, "source": "MODEL", "type": "GENERIC", "status": "RUNNING",
		"content": "Tool is running as a background task with task id: " + agyTestID + "/task-7\nTask Description: example-cli login\n" +
			"Task logs are available at: file:///tmp/task-7.log\nLog output:\n"})
	agyTestFile(t, n.agyDataRoot(), agyTestID, `{"step_index":0,"source":"USER_EXPLICIT","type":"USER_INPUT","content":"sign in"}`+"\n"+
		string(step)+"\n")
	fx := &agyInputFixture{n: n, f: f, brain: filepath.Join(n.agyDataRoot(), "brain", agyTestID, ".system_generated"), logs: &syncBuffer{}}
	n.logger = log.New(fx.logs, "", 0)
	for _, d := range []string{"tasks", "messages"} {
		if err := os.MkdirAll(filepath.Join(fx.brain, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	fx.log = filepath.Join(fx.brain, "tasks", "task-7.log")
	agyTestWrite(t, fx.log, out)
	// The pane's agy is this test's own process: started before any of its task logs.
	f.reply("pane.process_info", map[string]any{"process_info": map[string]any{"foreground_processes": []any{
		map[string]any{"pid": os.Getpid(), "name": "agy", "argv0": "agy"}}}})
	return fx
}

// waiting is what the pane's conversation has stopped at, read at now (quiet: as the card list reads it).
func (fx *agyInputFixture) waiting(t *testing.T, now time.Time, quiet bool) []agyWaitingTask {
	t.Helper()
	panes := fx.n.agyInputPanes()
	if len(panes) != 1 || panes[0].id != agyTestRow || panes[0].conversation != agyTestID {
		t.Fatalf("agy panes: %+v", panes)
	}
	return fx.n.agyWaiting(context.Background(), panes[0], now, quiet)
}

// card is the one card the pane lists once its prompt has been quiet.
func (fx *agyInputFixture) card(t *testing.T) proto.Interaction {
	t.Helper()
	now := time.Now()
	fx.waiting(t, now, true) // first sight: not quiet yet
	cards := fx.n.commandInputCards(context.Background())
	if len(cards) != 0 {
		t.Fatalf("a prompt not yet quiet was asked: %+v", cards)
	}
	fx.n.cx.input.mu.Lock()
	for k, l := range fx.n.cx.input.logs {
		l.since = l.since.Add(-agyInputQuiet)
		fx.n.cx.input.logs[k] = l
	}
	fx.n.cx.input.mu.Unlock()
	cards = fx.n.commandInputCards(context.Background())
	if len(cards) != 1 {
		t.Fatalf("cards: %+v", cards)
	}
	return cards[0]
}

func (fx *agyInputFixture) message(t *testing.T, name string, m map[string]any) {
	t.Helper()
	b, _ := json.Marshal(m)
	agyTestWrite(t, filepath.Join(fx.brain, "messages", name), string(b))
}

// A task stopped at a prompt for agyInputQuiet is one card: the reserved tool, the prompt as the one secret, required
// field's label, the command and the lines before the prompt (blank ones left out), the agy pane's row as its session.
func TestAgyTaskAtAPromptIsACard(t *testing.T) {
	fx := newAgyInputFixture(t, agyTestSignIn)
	now := time.Now()
	if got := fx.waiting(t, now, true); len(got) != 0 {
		t.Fatalf("asked at first sight: %+v", got)
	}
	if got := fx.waiting(t, now.Add(agyInputQuiet/2), true); len(got) != 0 {
		t.Fatalf("asked before the prompt was quiet: %+v", got)
	}
	got := fx.waiting(t, now.Add(agyInputQuiet), true)
	if len(got) != 1 || got[0].task != "task-7" || got[0].prompt != "Enter the authorization code:" || !got[0].secret ||
		!strings.Contains(got[0].output, "https://auth.example.com/authorize?client=demo") || strings.Contains(got[0].output, "\r") {
		t.Fatalf("waiting: %+v", got)
	}
	// the command's blank lines are left out: the card has little room
	if want := "To sign in, visit this URL in any browser:\n  https://auth.example.com/authorize?client=demo\n" +
		"After approving, the page shows a code."; got[0].output != want {
		t.Fatalf("output %q, want %q", got[0].output, want)
	}
	ix := fx.card(t)
	if !proto.IsCommandInputCard(ix) || ix.SessionID != agyTestRow || ix.Agent != agyAgent || ix.ID != commandInputID(got[0]) {
		t.Fatalf("card: %+v", ix)
	}
	if f := ix.Fields[0]; f.Label != "Enter the authorization code:" || !f.Secret || f.Optional {
		t.Fatalf("field: %+v", f)
	}
	var in proto.CommandInput
	if err := json.Unmarshal(ix.ToolInput, &in); err != nil || in.Command != "example-cli login" || in.Task != "task-7" ||
		in.Prompt != "Enter the authorization code:" || !in.Masked || !strings.HasPrefix(in.Output, "To sign in") {
		t.Fatalf("tool_input %s: %+v (%v)", ix.ToolInput, in, err)
	}
	if again := fx.n.commandInputCards(context.Background()); len(again) != 1 || !again[0].CreatedAt.Equal(ix.CreatedAt) {
		t.Fatalf("listed again as another card: %+v", again)
	}
}

// Output that is not at a prompt asks nothing: a last line with a newline after it, progress, an empty log.
func TestAgyTaskNotAtAPromptIsNoCard(t *testing.T) {
	for name, out := range map[string]string{
		"newline after the prompt": agyTestSignIn + "test-code\r\n",
		"progress":                 "Downloading model\r\n 45% |#####     | 1.2 GB/2.7 GB",
		"too long for a prompt":    strings.Repeat("x", proto.CommandInputMaxPrompt+1) + ":",
	} {
		t.Run(name, func(t *testing.T) {
			fx := newAgyInputFixture(t, out)
			now := time.Now()
			fx.waiting(t, now, true)
			if got := fx.waiting(t, now.Add(agyInputQuiet), true); len(got) != 0 {
				t.Fatalf("asked: %+v", got)
			}
		})
	}
}

// A task that ended (its end message), one an agy restart stopped (a notice after its last output) and one an earlier
// agy left behind (its log older than the pane's agy) wait for nothing.
func TestAgyTaskThatIsNotRunningIsNoCard(t *testing.T) {
	t.Run("ended", func(t *testing.T) {
		fx := newAgyInputFixture(t, agyTestSignIn)
		fx.message(t, "a1.json", map[string]any{"id": "a1", "recipient": agyTestID, "sender": agyTestID + "/task-7",
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano), "content": "Task id \"" + agyTestID + "/task-7\" finished with result:\nCommand exited with code 1."})
		if got := fx.waiting(t, time.Now(), false); len(got) != 0 {
			t.Fatalf("an ended task asked: %+v", got)
		}
	})
	t.Run("restarted", func(t *testing.T) {
		fx := newAgyInputFixture(t, agyTestSignIn)
		fx.message(t, "r1.json", map[string]any{"id": "r1", "recipient": agyTestID, "sender": "system",
			"timestamp": time.Now().Add(time.Minute).UTC().Format(time.RFC3339Nano),
			"content":   "[Notice] All your subagents and background tasks have been stopped due to server restart."})
		if got := fx.waiting(t, time.Now(), false); len(got) != 0 {
			t.Fatalf("a task a restart stopped asked: %+v", got)
		}
	})
	t.Run("left by an earlier agy", func(t *testing.T) {
		fx := newAgyInputFixture(t, agyTestSignIn)
		started, ok := agyProcessStart(os.Getpid())
		if !ok {
			t.Skip("this system does not tell when a process started")
		}
		old := started.Add(-time.Minute)
		if err := os.Chtimes(fx.log, old, old); err != nil {
			t.Fatal(err)
		}
		if got := fx.waiting(t, time.Now(), false); len(got) != 0 {
			t.Fatalf("a task from before the pane's agy asked: %+v", got)
		}
	})
}

// agyLSRecorder is an agy language server: it records each SendAgentMessage and answers status.
type agyLSRecorder struct {
	mu       sync.Mutex
	requests []agyLSRequest
	status   int
}

type agyLSRequest struct {
	path, token, legacyToken, protocol string
	body                               map[string]string
}

func (r *agyLSRecorder) serve(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		var body map[string]string
		_ = json.NewDecoder(req.Body).Decode(&body)
		r.mu.Lock()
		r.requests = append(r.requests, agyLSRequest{path: req.URL.Path, token: req.Header.Get("X-Codeium-Csrf-Token"),
			legacyToken: req.Header.Get("X-Antigravity-Csrf-Token"), protocol: req.Header.Get("Connect-Protocol-Version"), body: body})
		status := r.status
		r.mu.Unlock()
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = io.WriteString(w, "{}")
		} else { // an error that repeats what it was sent: the node must not pass that on
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "not_found", "message": "no such recipient " + body["content"]})
		}
	}))
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func (r *agyLSRecorder) sent() []agyLSRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]agyLSRequest(nil), r.requests...)
}

// withAgyEnv makes every process's environment name addr and token as agy's language server.
func withAgyEnv(t *testing.T, addr, token string) {
	t.Helper()
	old := agyProcEnv
	agyProcEnv = func(int) map[string]string { return map[string]string{agyLSAddressEnv: addr, agyLSTokenEnv: token} }
	t.Cleanup(func() { agyProcEnv = old })
}

// answer is the phone's answer to card ix, as the manager hands it over (codex.answer).
func (fx *agyInputFixture) answer(t *testing.T, ix proto.Interaction, r proto.Resolution) error {
	t.Helper()
	_, err := request(t, fx.n, "codex.answer", map[string]any{"session_id": ix.SessionID, "id": ix.ID, "resolution": r})
	return err
}

func (fx *agyInputFixture) dismissed(id string) bool {
	fx.n.cx.mu.Lock()
	defer fx.n.cx.mu.Unlock()
	_, ok := fx.n.cx.dismissed[id]
	return ok
}

// A line goes into the task the way agy's own send_input sends it: one message to the task from the conversation,
// the line and a newline, with the CSRF token agy gave its commands; then the same prompt is not asked again.
func TestAgyInputAnswerTypesTheLineIntoTheTask(t *testing.T) {
	fx := newAgyInputFixture(t, agyTestSignIn)
	var ls agyLSRecorder
	withAgyEnv(t, ls.serve(t), "csrf-test-token")
	ix := fx.card(t)
	yes := true
	if err := fx.answer(t, ix, proto.Resolution{Allow: &yes, Values: map[string]string{proto.CommandInputField: "test-code-123"}}); err != nil {
		t.Fatal(err)
	}
	sent := ls.sent()
	if len(sent) != 1 {
		t.Fatalf("requests: %+v", sent)
	}
	r := sent[0]
	if r.path != agyLSMethod || r.token != "csrf-test-token" || r.legacyToken != "csrf-test-token" || r.protocol != "1" ||
		r.body["recipient"] != agyTestID+"/task-7" || r.body["senderId"] != agyTestID || r.body["content"] != "test-code-123\n" {
		t.Fatalf("request: %+v", r)
	}
	if !fx.dismissed(ix.ID) {
		t.Fatal("the prompt just answered would be asked again")
	}
	if !strings.Contains(fx.logs.String(), "typed a line into task-7") {
		t.Fatalf("log: %s", fx.logs.String())
	}
	fx.logsKeep(t, "test-code-123", "csrf-test-token")
}

// logsKeep fails when the node's log holds any of secrets.
func (fx *agyInputFixture) logsKeep(t *testing.T, secrets ...string) {
	t.Helper()
	logs := fx.logs.String()
	for _, s := range secrets {
		if strings.Contains(logs, s) {
			t.Fatalf("the node's log holds %q: %s", s, logs)
		}
	}
}

// Not now and a hand-back type nothing, and the same prompt is not asked again.
func TestAgyInputNotNowTypesNothing(t *testing.T) {
	fx := newAgyInputFixture(t, agyTestSignIn)
	var ls agyLSRecorder
	withAgyEnv(t, ls.serve(t), "csrf-test-token")
	ix := fx.card(t)
	no := false
	if err := fx.answer(t, ix, proto.Resolution{Allow: &no}); err != nil {
		t.Fatal(err)
	}
	if len(ls.sent()) != 0 || !fx.dismissed(ix.ID) {
		t.Fatalf("Not now: sent %+v, dismissed %v", ls.sent(), fx.dismissed(ix.ID))
	}
}

// A line goes only into the prompt the card was made from: output after it (the task moved on) types nothing; nor does
// a line with a line break, or a language server that is not on this computer.
func TestAgyInputAnswerRefusals(t *testing.T) {
	yes := true
	line := func(s string) proto.Resolution {
		return proto.Resolution{Allow: &yes, Values: map[string]string{proto.CommandInputField: s}}
	}
	t.Run("the prompt moved on", func(t *testing.T) {
		fx := newAgyInputFixture(t, agyTestSignIn)
		var ls agyLSRecorder
		withAgyEnv(t, ls.serve(t), "csrf-test-token")
		ix := fx.card(t)
		agyTestAppend(t, fx.log, "test-code\r\nSigned in.\r\n")
		var re *rpc.Error
		if err := fx.answer(t, ix, line("test-code-123")); !errors.As(err, &re) || re.Code != screen.PromptChanged {
			t.Fatalf("err %v", err)
		}
		if len(ls.sent()) != 0 {
			t.Fatalf("typed into a prompt that moved on: %+v", ls.sent())
		}
	})
	t.Run("a line break", func(t *testing.T) {
		fx := newAgyInputFixture(t, agyTestSignIn)
		var ls agyLSRecorder
		withAgyEnv(t, ls.serve(t), "csrf-test-token")
		ix := fx.card(t)
		var re *rpc.Error
		if err := fx.answer(t, ix, line("test-code\nrm -rf x")); !errors.As(err, &re) || re.Code != codeCommandInputLine {
			t.Fatalf("err %v", err)
		}
		if len(ls.sent()) != 0 || fx.dismissed(ix.ID) {
			t.Fatalf("sent %+v, dismissed %v", ls.sent(), fx.dismissed(ix.ID))
		}
	})
	t.Run("not on this computer", func(t *testing.T) {
		fx := newAgyInputFixture(t, agyTestSignIn)
		var ls agyLSRecorder
		ls.serve(t)
		withAgyEnv(t, "ls.example.com:80", "csrf-test-token")
		ix := fx.card(t)
		var re *rpc.Error
		if err := fx.answer(t, ix, line("test-code-123")); !errors.As(err, &re) || re.Code != codeCommandInputUnreachable {
			t.Fatalf("err %v", err)
		}
		if len(ls.sent()) != 0 {
			t.Fatalf("sent %+v", ls.sent())
		}
	})
	t.Run("agy refuses", func(t *testing.T) {
		fx := newAgyInputFixture(t, agyTestSignIn)
		ls := agyLSRecorder{status: http.StatusNotFound}
		withAgyEnv(t, ls.serve(t), "csrf-test-token")
		ix := fx.card(t)
		var re *rpc.Error
		err := fx.answer(t, ix, line("test-code-123"))
		if !errors.As(err, &re) || re.Code != codeCommandInputRefused || !strings.Contains(re.Message, "not_found") {
			t.Fatalf("err %v", err)
		}
		if strings.Contains(re.Message, "test-code-123") || strings.Contains(re.Message, "csrf-test-token") || fx.dismissed(ix.ID) {
			t.Fatalf("the refusal repeats the line or the token, or the card went: %q", re.Message)
		}
		fx.logsKeep(t, "test-code-123", "csrf-test-token")
	})
	t.Run("no answer", func(t *testing.T) {
		fx := newAgyInputFixture(t, agyTestSignIn)
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ln.Close() })
		go func() { // takes the request, then hangs up without an answer
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				buf := make([]byte, 4096)
				_, _ = c.Read(buf)
				c.Close()
			}
		}()
		withAgyEnv(t, ln.Addr().String(), "csrf-test-token")
		ix := fx.card(t)
		var re *rpc.Error
		if err := fx.answer(t, ix, line("test-code-123")); !errors.As(err, &re) || re.Code != codeCommandInputUnconfirmed ||
			!strings.Contains(string(re.Data), `"delivered":"unknown"`) {
			t.Fatalf("err %v (%s)", err, re.Data)
		}
		fx.logsKeep(t, "test-code-123", "csrf-test-token")
	})
}

// agy's language server is found in the environment of a process under the pane's agy, as agy starts its commands.
func TestReadAgyProcEnv(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("agy's environment is not read on Windows")
	}
	cmd := exec.Command("sleep", "30")
	cmd.Env = append(os.Environ(), agyLSAddressEnv+"=127.0.0.1:43210", agyLSTokenEnv+"=csrf-env-token")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	var env map[string]string
	waitFor(t, "the child's environment", func() bool {
		env = readAgyProcEnv(cmd.Process.Pid)
		return env[agyLSTokenEnv] != ""
	})
	if env[agyLSAddressEnv] != "127.0.0.1:43210" || env[agyLSTokenEnv] != "csrf-env-token" {
		t.Fatalf("env %v", env)
	}
}

func TestLoopbackAddress(t *testing.T) {
	for addr, want := range map[string]bool{
		"127.0.0.1:5387": true, "localhost:5387": true, "[::1]:5387": true, "127.0.0.2:80": true,
		"192.0.2.10:5387": false, "ls.example.com:80": false, "127.0.0.1": false, "127.0.0.1:0": false, "localhost:x": false,
	} {
		if got := loopbackAddress(addr); got != want {
			t.Errorf("loopbackAddress(%q) = %v, want %v", addr, got, want)
		}
	}
}

// A message file is read from its first bytes: a task's end from its sender and its first words, a restart notice
// with its time; anything else says nothing.
func TestReadAgyMessage(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	ended := write("e.json", `{"id":"e", "recipient":"`+agyTestID+`", "sender":"`+agyTestID+`/task-52", "priority":"MESSAGE_PRIORITY_HIGH", "content":"Task id \"`+agyTestID+`/task-52\" finished with result:\nCommand exited with code 0.`+strings.Repeat("x", 8<<10)+`"}`)
	if m := readAgyMessage(ended, agyTestID); m.task != "task-52" || !m.restart.IsZero() {
		t.Fatalf("end: %+v", m)
	}
	restart := write("r.json", `{"id":"r", "recipient":"`+agyTestID+`", "sender":"system", "timestamp":"2026-11-24T06:15:30.537038Z", "content":"[Notice] All your subagents and background tasks have been stopped due to server restart."}`)
	if m := readAgyMessage(restart, agyTestID); m.task != "" || !m.restart.Equal(time.Date(2026, 11, 24, 6, 15, 30, 537038000, time.UTC)) {
		t.Fatalf("restart: %+v", m)
	}
	user := write("u.json", `{"id":"u", "recipient":"`+agyTestID+`", "sender":"user", "content":"Task id is what?"}`)
	other := write("o.json", `{"id":"o", "recipient":"`+agyTestID+`", "sender":"`+agyOtherTestID+`/task-1", "content":"Task id \"x\" finished"}`)
	for _, p := range []string{user, other} {
		if m := readAgyMessage(p, agyTestID); m.task != "" || !m.restart.IsZero() {
			t.Fatalf("%s: %+v", p, m)
		}
	}
}
