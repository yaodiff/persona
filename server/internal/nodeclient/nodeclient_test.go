package nodeclient

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const modulePath = "github.com/nwverse/persona/server/"

// The commands a computer runs for its Agents link only this client and the contracts: never the node, the manager or
// the hub (the self-contained packaging plan, step 4: the device's program owns these commands).
func TestClientCommandsDoNotLinkTheNodeOrTheManager(t *testing.T) {
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Skip("no go tool:", err)
	}
	forbidden := []string{"internal/node", "internal/devices", "internal/assistant", "internal/hub", "internal/manager",
		"internal/extmgr", "internal/db"}
	for _, target := range []string{"linux/amd64", "linux/arm64", "darwin/arm64", "darwin/amd64", "windows/amd64"} { // a platform's own files can import more
		for _, pkg := range []string{".", "../mcpserver", "../scriptcli", "../toolcli"} {
			cmd := exec.Command(goBin, "list", "-deps", "-f", "{{.ImportPath}}", pkg)
			goos, goarch, _ := strings.Cut(target, "/")
			cmd.Env = append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch)
			out, err := cmd.Output()
			if err != nil {
				t.Fatalf("go list -deps %s (%s): %v", pkg, target, err)
			}
			checkDeps(t, pkg+" ("+target+")", string(out), forbidden)
		}
	}
}

func checkDeps(t *testing.T, pkg, out string, forbidden []string) {
	t.Helper()
	for _, dep := range strings.Fields(out) {
		rel, ok := strings.CutPrefix(dep, modulePath)
		if !ok {
			continue
		}
		for _, f := range forbidden {
			if rel == f || strings.HasPrefix(rel, f+"/") {
				t.Errorf("%s depends on %s: the node.sock commands reach the node only through internal/nodeclient "+
					"(move what they need into it, or into a contract package)", pkg, dep)
			}
		}
	}
}

func TestDialResumableResendsTheSameAttempt(t *testing.T) {
	defer func(d, w time.Duration) { ResumeRetry, ResumeWait = d, w }(ResumeRetry, ResumeWait)
	ResumeRetry, ResumeWait = time.Millisecond, time.Second
	var seen []Request
	dial := func(_ context.Context, req any) ([]byte, error) {
		r := req.(Request)
		seen = append(seen, r)
		switch len(seen) {
		case 1:
			return []byte(`{"retry":true}` + "\n"), nil // the node is going away
		case 2:
			return nil, errors.New("node closed connection") // between nodes
		}
		return []byte(`{"output":{"ok":true}}` + "\n"), nil
	}
	line, err := DialResumable(context.Background(), dial, Request{Kind: "hook", Attempt: "a1"})
	if err != nil || !bytes.Contains(line, []byte(`"ok":true`)) {
		t.Fatalf("answer %q %v", line, err)
	}
	if len(seen) != 3 || seen[0].Resume || !seen[1].Resume || !seen[2].Resume || seen[2].Attempt != "a1" {
		t.Fatalf("requests %+v: want the first plain, then resumes of the same attempt", seen)
	}
	if _, err := DialResumable(context.Background(), func(context.Context, any) ([]byte, error) { return nil, ErrNoNode }, Request{}); !errors.Is(err, ErrNoNode) {
		t.Fatalf("no node: %v", err)
	}
}

func TestRunHookWithoutANodePrintsNothing(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no node.sock there
	for _, args := range [][]string{nil, {GlobalHookFlag}} {
		var out bytes.Buffer
		RunHook(context.Background(), args, strings.NewReader(`{"session_id":"s1","hook_event_name":"Stop"}`), &out)
		if out.Len() != 0 {
			t.Fatalf("%v: printed %q", args, out.String())
		}
	}
	var out bytes.Buffer
	RunHook(context.Background(), nil, strings.NewReader("not json"), &out)
	if out.Len() != 0 {
		t.Fatalf("bad input: printed %q", out.String())
	}
}

// A MessageDisplay hook (the Agents' plugin has it, node/generated.go) hands its input to the node as hook.display and
// prints nothing, whatever the node says: printed, it would replace the lines on the terminal. A node that takes long
// holds Claude's terminal no longer than displayWait.
func TestRunHookPassesDisplayOn(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pd") // a short path: a socket's is bounded
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("HOME", dir)
	t.Setenv("PERSONA_HOME", dir)
	ln, err := net.Listen("unix", SocketPath())
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan Request, 2)
	slow := make(chan struct{})
	go func() {
		for n := 0; ; n++ {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadBytes('\n')
			var req Request
			_ = json.Unmarshal(line, &req)
			got <- req
			if n == 0 { // the first: answered at once, as a node does
				_, _ = c.Write([]byte(`{"ok":true,"output":{"hookSpecificOutput":{"hookEventName":"MessageDisplay","displayContent":"x"}}}` + "\n"))
			} else {
				<-slow
			}
			c.Close()
		}
	}()
	defer close(slow)
	input := `{"session_id":"eb3c877f-aa6d-498b-9024-e15027f3897e","prompt_id":"p1","hook_event_name":"MessageDisplay",` +
		`"turn_id":"t1","message_id":"m1","index":0,"final":false,"delta":"Line 1.\n"}`
	var out bytes.Buffer
	RunHook(context.Background(), nil, strings.NewReader(input), &out)
	req := <-got
	if req.Kind != "hook.display" || string(req.Input) != input || out.Len() != 0 {
		t.Fatalf("sent %+v, printed %q", req, out.String())
	}
	start := time.Now()
	RunHook(context.Background(), nil, strings.NewReader(input), &out)
	if took := time.Since(start); took < displayWait || took > displayWait+500*time.Millisecond || out.Len() != 0 {
		t.Fatalf("a node that does not answer held the hook %v, printed %q", took, out.String())
	}
}

// The live replies hook runs without a shell, so it is told its node's directory as an argument (node/generated.go
// displayHooks): it reaches that node's socket whatever its environment says.
func TestRunHookHomeFlag(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "pd") // a short path: a socket's is bounded
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	other := t.TempDir()
	t.Setenv("HOME", other)
	t.Setenv("PERSONA_HOME", filepath.Join(other, "elsewhere")) // restored after RunHook sets it
	ln, err := net.Listen("unix", filepath.Join(dir, "node.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan Request, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadBytes('\n')
		var req Request
		_ = json.Unmarshal(line, &req)
		got <- req
		_, _ = c.Write([]byte(`{"ok":true}` + "\n"))
	}()
	input := `{"session_id":"eb3c877f-aa6d-498b-9024-e15027f3897e","hook_event_name":"MessageDisplay","index":0,"final":true,"delta":"Hi."}`
	var out bytes.Buffer
	RunHook(context.Background(), []string{HomeFlag, dir}, strings.NewReader(input), &out)
	select {
	case req := <-got:
		if req.Kind != "hook.display" || out.Len() != 0 {
			t.Fatalf("sent %+v, printed %q", req, out.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the hook did not reach the socket of the directory it was given")
	}
}

func TestAskRequestNormalize(t *testing.T) {
	a := AskRequest{Question: "  Deploy?  ", Options: []string{" yes ", "", "yes", "no"}}
	if err := a.Normalize(); err != nil || a.Question != "Deploy?" || len(a.Options) != 2 {
		t.Fatalf("%+v %v", a, err)
	}
	b, _ := json.Marshal(Request{Kind: "ask", Attempt: NewAttempt()})
	var back Request
	if json.Unmarshal(b, &back) != nil || len(back.Attempt) != 36 {
		t.Fatalf("round trip %s", b)
	}
}

// A script's Markdown opt-in survives the typed client on its way to node.sock.
func TestAskNodeSendsMarkdownFormat(t *testing.T) {
	var ask AskRequest
	if err := json.Unmarshal([]byte(`{"question":"**Deploy?**","format":"markdown"}`), &ask); err != nil {
		t.Fatal(err)
	}
	var sent json.RawMessage
	dial := func(_ context.Context, req any) ([]byte, error) {
		r, ok := req.(Request)
		if !ok {
			t.Fatalf("request type %T", req)
		}
		sent = append(sent, r.Input...)
		return []byte(`{"status":"answered"}` + "\n"), nil
	}
	rep, err := AskNode(context.Background(), dial, ask, "")
	if err != nil || rep.Status != AskAnswered {
		t.Fatalf("reply %+v, %v", rep, err)
	}
	var input map[string]any
	if err := json.Unmarshal(sent, &input); err != nil {
		t.Fatalf("input %q: %v", sent, err)
	}
	if got := input["format"]; got != "markdown" {
		t.Fatalf("format %#v, want markdown; input %s", got, sent)
	}
}

// Formatting does not change the existing 1000-character question boundary, including for unknown formats.
func TestAskRequestQuestionLimitRegardlessOfFormat(t *testing.T) {
	for _, format := range []struct {
		name  string
		input string
	}{
		{name: "omitted"},
		{name: "markdown", input: `,"format":"markdown"`},
		{name: "plain", input: `,"format":"plain"`},
		{name: "unknown", input: `,"format":"html"`},
	} {
		for _, limit := range []struct {
			name      string
			length    int
			wantError bool
		}{
			{name: "at the limit", length: AskQuestionMost},
			{name: "over the limit", length: AskQuestionMost + 1, wantError: true},
		} {
			t.Run(format.name+"/"+limit.name, func(t *testing.T) {
				question, err := json.Marshal(strings.Repeat("界", limit.length))
				if err != nil {
					t.Fatal(err)
				}
				var ask AskRequest
				input := `{"question":` + string(question) + format.input + `}`
				if err := json.Unmarshal([]byte(input), &ask); err != nil {
					t.Fatal(err)
				}
				err = ask.Normalize()
				if limit.wantError && err == nil {
					t.Fatalf("a %d-character question was accepted, want the %d-character limit", limit.length, AskQuestionMost)
				}
				if !limit.wantError && err != nil {
					t.Fatalf("a %d-character question was rejected: %v", limit.length, err)
				}
			})
		}
	}
}
