package nodeclient

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func mcpWaitTestServer(t *testing.T, payload string) (string, <-chan error) {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "pd")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "node.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	serverDone := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		defer conn.Close()
		if _, err := bufio.NewReader(conn).ReadBytes('\n'); err != nil {
			serverDone <- err
			return
		}
		_, err = conn.Write([]byte(payload))
		serverDone <- err
	}()
	return path, serverDone
}

func requireMCPWaitTestServer(t *testing.T, ctx context.Context, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("the fake node server did not finish")
	}
}

// Only a context that explicitly installed a receiver consumes interim lines. A nil receiver deliberately discards
// them; without the context option the connection retains its original one-response behavior.
func TestDialAtReadsMCPWaitUpdates(t *testing.T) {
	for _, nilReceiver := range []bool{false, true} {
		t.Run(map[bool]string{false: "receiver", true: "nil receiver"}[nilReceiver], func(t *testing.T) {
			path, serverDone := mcpWaitTestServer(t, "{\"wait_kind\":\"approval\"}\n{\"wait_kind\":\"a future value\"}\n{\"content\":[{\"type\":\"text\",\"text\":\"done\"}]}\n")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			received := make(chan proto.MCPWaitKind, 2)
			if nilReceiver {
				ctx = WithMCPWait(ctx, nil)
			} else {
				ctx = WithMCPWait(ctx, func(kind proto.MCPWaitKind) { received <- kind })
			}
			line, err := DialAt(ctx, path, Request{Kind: "mcp.call", WaitUpdates: true})
			if err != nil {
				t.Fatal(err)
			}
			var result proto.ToolResult
			if err := json.Unmarshal(line, &result); err != nil || len(result.Content) != 1 || result.Content[0].Text != "done" {
				t.Fatalf("final response = %s (%v)", line, err)
			}
			requireMCPWaitTestServer(t, ctx, serverDone)
			if nilReceiver {
				return
			}
			for _, want := range []proto.MCPWaitKind{proto.MCPWaitApproval, proto.MCPWaitUnknown} {
				select {
				case got := <-received:
					if got != want {
						t.Fatalf("kind = %q, want %q", got, want)
					}
				case <-ctx.Done():
					t.Fatal("wait update was not received")
				}
			}
		})
	}
}

func TestDialAtWithoutMCPWaitReceiverReturnsResponseUnchanged(t *testing.T) {
	response := `{"wait_kind":"approval"}` + "\n"
	path, serverDone := mcpWaitTestServer(t, response+`{"content":[{"type":"text","text":"a later line must remain unread"}]}`+"\n")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	line, err := DialAt(ctx, path, Request{Kind: "mcp.call", WaitUpdates: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(line) != response {
		t.Fatalf("response changed without an installed receiver: got %q, want %q", line, response)
	}
	requireMCPWaitTestServer(t, ctx, serverDone)
}

func TestDialAtMCPWaitDoesNotTreatFinalResponseAsInterim(t *testing.T) {
	for _, tc := range []struct {
		name     string
		response string
	}{
		{name: "additional final fields", response: `{"wait_kind":"approval","content":[{"type":"text","text":"done"}]}`},
		{name: "large final response", response: `{"wait_kind":"approval","content":[{"type":"text","text":"` + strings.Repeat("x", 1<<20) + `"}]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path, serverDone := mcpWaitTestServer(t, tc.response+"\n")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			received := make(chan proto.MCPWaitKind, 1)
			ctx = WithMCPWait(ctx, func(kind proto.MCPWaitKind) { received <- kind })
			line, err := DialAt(ctx, path, Request{Kind: "mcp.call", WaitUpdates: true})
			if err != nil {
				t.Fatal(err)
			}
			if string(line) != tc.response+"\n" {
				t.Fatalf("the final response was consumed or changed (%d bytes)", len(line))
			}
			select {
			case kind := <-received:
				t.Fatalf("the final response produced wait update %q", kind)
			default:
			}
			requireMCPWaitTestServer(t, ctx, serverDone)
		})
	}
}
