package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/cliport"
	"github.com/nwverse/persona/server/internal/cliwire"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/rpc"
)

// What a lark-cli's failure may print: its output can carry a token, a path and the message itself (#9125). None of
// it may reach the manager's answer or the log.
var inboxSendSecrets = []string{"t-EXAMPLE-SECRET-TOKEN", "/home/example/.lark-cli", "the reply text itself", "stderr"}

// fakeLarkCLI is a lark-cli that prints what fails on stderr (body) and exits with status; each run is counted in the
// returned file.
func fakeLarkCLI(t *testing.T, body string) (bin, runs string) {
	t.Helper()
	dir := t.TempDir()
	runs = filepath.Join(dir, "runs")
	bin = filepath.Join(dir, "lark-cli")
	script := "#!/bin/sh\necho run >> '" + runs + "'\n" + body + "\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin, runs
}

func fakeRuns(t *testing.T, runs string) int {
	t.Helper()
	raw, err := os.ReadFile(runs)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Count(string(raw), "run\n")
}

// sendTestManager is a manager with the lark-cli connector on bin and a send record, whose log is kept.
func sendTestManager(t *testing.T, bin string, timeout time.Duration) (h *Manager, logs func() string) {
	t.Helper()
	needsLarkCLI(t) // its sends go through the lark-cli connector
	h = newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	if err := h.addConnector(cliport.NewConnector(cliwire.Config{Bin: bin, Timeout: timeout})); err != nil {
		t.Fatal(err)
	}
	h.store = a2NewSentStore()
	var mu sync.Mutex
	var lines []string
	prev := h.opts.Logf
	h.opts.Logf = func(format string, args ...any) {
		mu.Lock()
		lines = append(lines, fmt.Sprintf(format, args...))
		mu.Unlock()
		prev(format, args...)
	}
	return h, func() string {
		mu.Lock()
		defer mu.Unlock()
		return strings.Join(lines, "\n")
	}
}

func sendReply(h *Manager, source, thread, sendID string) (json.RawMessage, error) {
	raw, _ := json.Marshal(map[string]any{"source": source, "send_id": sendID,
		"reply": connector.InboxReply{Thread: thread, Text: "the reply text itself"}})
	out, _, err := h.AppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppInbox, Method: assistantwire.InboxReply, Params: raw})
	return out, err
}

func noSecrets(t *testing.T, what, text string) {
	t.Helper()
	for _, s := range inboxSendSecrets {
		if strings.Contains(text, s) {
			t.Fatalf("%s carries %q: %s", what, s, text)
		}
	}
}

func TestInboxSenderOverrideCrossesAppBoundary(t *testing.T) {
	for _, identity := range []string{"bot", "user"} {
		t.Run(identity, func(t *testing.T) {
			argsPath := filepath.Join(t.TempDir(), "args")
			bin, runs := fakeLarkCLI(t, "printf '%s\\n' \"$*\" > '"+argsPath+"'\nprintf '{\"code\":0,\"data\":{}}\\n'")
			h, _ := sendTestManager(t, bin, 5*time.Second)
			raw, _ := json.Marshal(map[string]any{"source": cliwire.ConnectorID, "sender_identity": identity, "send_id": "selected-sender",
				"reply": connector.InboxReply{Thread: "larkcli:chat:oc_example_chat", Text: "Example reply."}})
			_, _, err := h.AppCall(context.Background(), assistantwire.AppCall{AppID: assistantwire.AppInbox, Method: assistantwire.InboxReply, Params: raw})
			if err != nil {
				t.Fatal(err)
			}
			args, err := os.ReadFile(argsPath)
			if err != nil || !strings.Contains(string(args), "--as "+identity+" --format json") || fakeRuns(t, runs) != 1 {
				t.Fatalf("selected sender did not reach the CLI exactly once: args=%q err=%v", args, err)
			}
		})
	}
}

// Each way a reply through a connector fails comes back to the manager as one coded reason, with a fixed sentence and
// nothing of what the CLI printed; the log says the kind, not the words. A refused send went nowhere, so its record is
// forgotten and the same reply asked again is tried again (#9125).
func TestInboxReplyFailuresAreCoded(t *testing.T) {
	needsLarkCLI(t)
	const chat = "larkcli:chat:oc_example_chat"
	stderr := func(line string) string { return "echo '" + line + "' >&2\nexit 1" }
	cases := []struct {
		name, thread, body, code string
		missingBin               bool
	}{
		{name: "bot is not in the group", thread: chat, code: assistantwire.CodeInboxBotNotInChat,
			body: stderr(`{"error":{"type":"api","code":230002,"message":"Bot not in the chat"}}`)},
		{name: "not installed", thread: chat, missingBin: true, code: assistantwire.CodeInboxSourceUnavailable},
		{name: "signed out", thread: chat, code: assistantwire.CodeInboxSourceSignedOut,
			body: stderr(`{"error":{"type":"authentication","subtype":"not_logged_in","message":"t-EXAMPLE-SECRET-TOKEN: stderr"}}`)},
		{name: "expired", thread: chat, code: assistantwire.CodeInboxSourceSignedOut,
			body: stderr(`{"error":{"type":"authentication","subtype":"token_expired","message":"t-EXAMPLE-SECRET-TOKEN expired"}}`)},
		{name: "permission", thread: chat, code: assistantwire.CodeInboxSendDenied,
			body: stderr(`{"error":{"type":"permission","subtype":"permission_denied","message":"bot not in chat; token t-EXAMPLE-SECRET-TOKEN"}}`)},
		{name: "rate limited", thread: chat, code: assistantwire.CodeInboxSendRateLimited,
			body: stderr(`too many requests for t-EXAMPLE-SECRET-TOKEN`)},
		{name: "other", thread: chat, code: assistantwire.CodeInboxSendFailed,
			body: stderr(`stderr: panic reading /home/example/.lark-cli/config.json with t-EXAMPLE-SECRET-TOKEN: the reply text itself`)},
		{name: "read-only document", thread: "doc:example", code: assistantwire.CodeInboxCannotReply, body: "exit 0"},
		{name: "thread with no chat", thread: "larkcli:example", code: assistantwire.CodeInboxCannotReply, body: "exit 0"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bin, runs := fakeLarkCLI(t, c.body)
			if c.missingBin {
				bin = filepath.Join(t.TempDir(), "missing", "lark-cli")
			}
			h, logs := sendTestManager(t, bin, 5*time.Second)
			for attempt := 1; attempt <= 2; attempt++ {
				out, err := sendReply(h, cliwire.ConnectorID, c.thread, "send-1")
				var re *rpc.Error
				if !errors.As(err, &re) || re.Code != c.code || re.Message == "" || len(out) != 0 {
					t.Fatalf("attempt %d: %s %#v, want code %s", attempt, out, err, c.code)
				}
				noSecrets(t, "the answer", re.Message+string(re.Data))
			}
			// Refused, nothing went out: the record is forgotten and the retry ran the CLI again.
			if want := map[bool]int{true: 2, false: 0}[c.body != "exit 0" && !c.missingBin]; fakeRuns(t, runs) != want {
				t.Fatalf("the CLI ran %d times, want %d", fakeRuns(t, runs), want)
			}
			if st := h.store.(*a2SentStore).rows["inbox:send-1"]; st != "" {
				t.Fatalf("a refused send left its record %q", st)
			}
			l := logs()
			if !strings.Contains(l, "not sent: "+c.code) {
				t.Fatalf("the log does not say why: %s", l)
			}
			noSecrets(t, "the log", l)
		})
	}

	t.Run("no connector", func(t *testing.T) {
		h, logs := sendTestManager(t, "/nonexistent/lark-cli", time.Second)
		var re *rpc.Error
		if _, err := sendReply(h, "lark", chat, "send-1"); !errors.As(err, &re) || re.Code != assistantwire.CodeInboxSourceUnavailable {
			t.Fatalf("a reply through a connector that is not here: %#v", err)
		}
		if !strings.Contains(logs(), "not sent: "+assistantwire.CodeInboxSourceUnavailable) {
			t.Fatalf("log: %s", logs())
		}
	})

	t.Run("send record unavailable", func(t *testing.T) {
		bin, runs := fakeLarkCLI(t, "exit 0")
		h, _ := sendTestManager(t, bin, time.Second)
		h.store = failingSendStore{newMemStore()}
		var re *rpc.Error
		if _, err := sendReply(h, cliwire.ConnectorID, chat, "send-1"); !errors.As(err, &re) || re.Code != assistantwire.CodeInboxSendUnrecorded {
			t.Fatalf("a send whose record could not be written: %#v", err)
		}
		noSecrets(t, "the answer", re.Message)
		if n := fakeRuns(t, runs); n != 0 {
			t.Fatalf("sent without its record: %d runs", n)
		}
	})

	t.Run("an extension that names a reason itself", func(t *testing.T) {
		h, _ := sendTestManager(t, "/nonexistent/lark-cli", time.Second)
		if err := h.addConnector(&refusingConnector{err: &rpc.Error{Code: assistantwire.CodeInboxSendDenied, Message: "stderr: t-EXAMPLE-SECRET-TOKEN"}}); err != nil {
			t.Fatal(err)
		}
		var re *rpc.Error
		if _, err := sendReply(h, "example", "chat:C1", "send-1"); !errors.As(err, &re) || re.Code != assistantwire.CodeInboxSendDenied {
			t.Fatalf("an extension's own reason: %#v", err)
		}
		noSecrets(t, "the answer", re.Message)
	})

	t.Run("an extension's rate limit", func(t *testing.T) {
		h, _ := sendTestManager(t, "/nonexistent/lark-cli", time.Second)
		limited := &rpc.Error{Code: "rate_limited", Message: "stderr: t-EXAMPLE-SECRET-TOKEN", Data: json.RawMessage(`{"retry_after":29.2}`)}
		if err := h.addConnector(&refusingConnector{err: limited}); err != nil {
			t.Fatal(err)
		}
		var re *rpc.Error
		if _, err := sendReply(h, "example", "chat:C1", "send-1"); !errors.As(err, &re) || re.Code != assistantwire.CodeInboxSendRateLimited ||
			string(re.Data) != `{"retry_after":30}` {
			t.Fatalf("an extension's rate limit: %#v", err)
		}
		noSecrets(t, "the answer", re.Message)
	})
}

// A send that may have gone out stays in doubt whatever its error says: lark-cli stopped by its own deadline is
// assistant_in_doubt, never a reason that invites sending again, and the same reply asked again is not sent (#9125).
func TestInboxReplyCutOffStaysInDoubt(t *testing.T) {
	needsLarkCLI(t)
	bin, runs := fakeLarkCLI(t, "exec sleep 5\necho 't-EXAMPLE-SECRET-TOKEN stderr' >&2\nexit 1")
	h, logs := sendTestManager(t, bin, 200*time.Millisecond)
	ran := -1
	for attempt := 1; attempt <= 2; attempt++ {
		var re *rpc.Error
		if _, err := sendReply(h, cliwire.ConnectorID, "larkcli:chat:oc_example_chat", "send-1"); !errors.As(err, &re) || re.Code != assistantwire.CodeAssistantInDoubt {
			t.Fatalf("attempt %d: %#v, want %s", attempt, err, assistantwire.CodeAssistantInDoubt)
		}
		noSecrets(t, "the answer", re.Message)
		// The first may be stopped before the script counts it (a new program's first start can be slow on macOS);
		// the second must not start it at all.
		if n := fakeRuns(t, runs); attempt == 1 {
			ran = n
		} else if n != ran {
			t.Fatalf("a send in doubt was tried again: %d runs, %d before", n, ran)
		}
	}
	if st := h.store.(*a2SentStore).rows["inbox:send-1"]; st != sendUnknown {
		t.Fatalf("its record: %q", st)
	}
	if l := logs(); !strings.Contains(l, "may have gone out: "+assistantwire.CodeAssistantInDoubt) {
		t.Fatalf("log: %s", l)
	} else {
		noSecrets(t, "the log", l)
	}
	// Whatever else wraps them, these are in doubt, not refused.
	for _, err := range []error{context.DeadlineExceeded, rpc.ErrDropped, &cliwire.Error{Kind: cliwire.KindTimeout},
		fmt.Errorf("send: %w", &rpc.Error{Code: assistantwire.CodeAssistantInDoubt, Message: "stderr"})} {
		if inboxSendOutcome(err) != sendUnknown {
			t.Errorf("%v is not in doubt", err)
		}
	}
}

type failingSendStore struct{ *memStore }

func (failingSendStore) ClaimSend(context.Context, string, string, string) (string, error) {
	return "", errors.New("pq: connection to /home/example/.lark-cli refused: t-EXAMPLE-SECRET-TOKEN")
}
func (failingSendStore) SettleSend(context.Context, string, string) error { return nil }

// refusingConnector is an extension's connector whose Reply fails with err.
type refusingConnector struct {
	inboxConnector
	err error
}

func (c *refusingConnector) Reply(context.Context, connector.InboxReply) error { return c.err }
