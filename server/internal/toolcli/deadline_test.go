package toolcli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// fakeDial replaces the node for one test with answer, through the package's own call.
func fakeDial(t *testing.T, answer func(ctx context.Context) ([]byte, error)) {
	t.Helper()
	oldDial, oldCall := dial, call
	t.Cleanup(func() { dial, call = oldDial, oldCall })
	call = realCall
	dial = func(ctx context.Context, _ any) ([]byte, error) { return answer(ctx) }
}

// stuck is a node that takes the call and never answers, as one blocked on a folder that does not answer: the
// connection ends only at the call's deadline, as nodeclient's does.
func stuck(ctx context.Context) ([]byte, error) {
	<-ctx.Done()
	return nil, fmt.Errorf("node closed connection: read: %w", os.ErrDeadlineExceeded)
}

func runMain(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := Main(context.Background(), args, strings.NewReader(""), &out, &errOut)
	return code, out.String(), errOut.String()
}

// A read whose node does not answer fails soon (readTimeout, not the 30 minutes a write may wait for the phone), as
// unavailable, naming the tool and what did not answer, and saying it can be tried again (#14400).
func TestReadThatGetsNoAnswerFailsSoon(t *testing.T) {
	old := readTimeout
	t.Cleanup(func() { readTimeout = old })
	readTimeout = 50 * time.Millisecond
	fakeDial(t, stuck)
	start := time.Now()
	code, out, errOut := runMain("a2a_read", `{"machine_id":"m","session_id":"s","turns":4}`)
	if took := time.Since(start); took > 10*time.Second {
		t.Fatalf("the read took %v", took)
	}
	var failure struct {
		Error string `json:"error"`
		Kind  string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(errOut), &failure); err != nil || code != exitUnavailable || out != "" || failure.Kind != "unavailable" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	for _, want := range []string{"a2a_read got no answer within 50ms", "Persona node on this machine", "can be tried again"} {
		if !strings.Contains(failure.Error, want) {
			t.Errorf("error %q lacks %q", failure.Error, want)
		}
	}
	if strings.Contains(failure.Error, "not reachable") {
		t.Errorf("a node that took the call and did not answer is not one that cannot be reached: %q", failure.Error)
	}
}

// Each call's deadline: a read gets readTimeout plus what it waits by design (dispatch_read's wait_seconds); every other
// tool, and a read of the connector command line or secret storage, the 30 minutes a write may wait for the owner.
func TestDeadlines(t *testing.T) {
	var left time.Duration
	fakeDial(t, func(ctx context.Context) ([]byte, error) {
		dl, ok := ctx.Deadline()
		if !ok {
			return nil, fmt.Errorf("no deadline")
		}
		left = time.Until(dl)
		return json.Marshal(proto.TextResult(`{"ok":true}`, false))
	})
	for _, c := range []struct {
		tool string
		want time.Duration
	}{
		{"a2a_sessions", readTimeout},
		{"a2a_get", readTimeout},
		{"a2a_read", readTimeout},
		{"resource_status", readTimeout},
		{"resource_get", readTimeout},
		{"todo_decisions", readTimeout},
		{"todo_repositories_get", readTimeout},
		{"dispatch_read", readTimeout + assistantwire.DispatchReadWaitMost},
		{"a2a_send", callTimeout},
		{"a2a_report", callTimeout},
		{"task_spawn", callTimeout},
		{"example_say", callTimeout},
		{"resource_acquire", callTimeout},
	} {
		left = 0
		if code, _, errOut := runMain(c.tool, `{}`); code != exitOK {
			t.Fatalf("%s: exit %d, stderr %q", c.tool, code, errOut)
		}
		if left > c.want || left < c.want-time.Minute/2 {
			t.Errorf("%s: deadline in %v, want %v", c.tool, left, c.want)
		}
	}
	// The read deadline is for reads: every name it covers is a tool an Agent calls, and none of them is a write.
	for name := range assistantwire.ReadToolWaits {
		if wait, read := deadline(name); !read || wait < readTimeout {
			t.Errorf("%s: deadline %v, read %v", name, wait, read)
		}
	}
}

// A read that answers before its deadline is a read like any other.
func TestReadThatAnswersInTime(t *testing.T) {
	old := readTimeout
	t.Cleanup(func() { readTimeout = old })
	readTimeout = 5 * time.Second
	fakeDial(t, func(context.Context) ([]byte, error) {
		return json.Marshal(proto.TextResult(`{"tasks":[]}`, false))
	})
	if code, out, errOut := runMain("a2a_get", `{}`); code != exitOK || out != "{\"tasks\":[]}\n" || errOut != "" {
		t.Fatalf("exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// A write that got no answer may have run anyway: its failure says to check, never that it can be retried as it is.
func TestNoAnswerToAWrite(t *testing.T) {
	msg := noAnswer("a2a_send", callTimeout, false)
	if !strings.HasPrefix(msg, "a2a_send got no answer within 30m0s: ") || !strings.Contains(msg, "check before trying again") ||
		strings.Contains(msg, "can be tried again") {
		t.Fatalf("%q", msg)
	}
}
