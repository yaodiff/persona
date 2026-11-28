package node

import (
	"context"
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/nodeclient"
)

type updateTerminalHost struct {
	inventoryHost
	agents     []host.Agent
	pids       []int
	processErr error
	read       func()
}

func (h *updateTerminalHost) SnapshotForUpdate(context.Context) (*host.Layout, error) {
	if h.read != nil {
		h.read()
	}
	return &host.Layout{Agents: h.agents}, h.snapshotErr
}

func (h *updateTerminalHost) PaneProcesses(context.Context, host.PaneRef) ([]int, error) {
	return h.pids, h.processErr
}

func terminalUpdateFixture(t *testing.T) (*Node, nodeclient.UpdateInventoryTarget, *updateTerminalHost, *remoteFixture, updateProcess) {
	t.Helper()
	n, target := updateInventoryFixture(t)
	f, c := connectRemoteFixture(t, n, t.TempDir(), "terminal-source", remoteRaw)
	n.codices.mu.Lock()
	n.codices.config.Servers = append(n.codices.config.Servers, c.spec)
	n.codices.mu.Unlock()
	h := &updateTerminalHost{inventoryHost: inventoryHost{bareHost: bareHost{kind: host.Herdr}},
		agents: []host.Agent{{Agent: "codex", PaneID: "w1:p1", AgentStatus: "idle", AgentSession: &host.AgentSession{Kind: "id", Value: remoteRaw}}}, pids: []int{123}}
	n.hosts = host.NewRegistry(h)
	p := updateProcess{PID: 123, Signature: strings.Repeat("b", 64), RemoteEndpoint: c.spec.Endpoint, RemoteThread: remoteRaw}
	return n, target, h, f, p
}

func TestUpdateTerminalAuthorityUsesRuntimeNotDisplay(t *testing.T) {
	n, target, _, f, p := terminalUpdateFixture(t)
	f.mu.Lock()
	f.threads, f.ephemeral = []string{remoteRaw, remoteOther}, remoteOther
	f.backgroundReply = map[string]any{"data": []any{map[string]any{"processId": "fixture-process", "command": "synthetic"}}}
	f.mu.Unlock()
	out, err := n.collectUpdateInventory(context.Background(), target, func(context.Context) ([]updateProcess, error) { return []updateProcess{p}, nil })
	if err != nil || !out.Complete {
		t.Fatalf("bound terminal: %+v %v", out, err)
	}
	background, working := false, false
	for _, row := range out.Rows {
		background = background || row.Background > 0
		working = working || row.Status == "working"
	}
	if !background || !working {
		t.Fatal("display idle hid runtime background or ephemeral child activity")
	}
	for _, method := range []string{"thread/resume", "turn/start", "thread/settings/update"} {
		if f.count(method) != 0 {
			t.Fatalf("inventory mutated runtime through %s", method)
		}
	}
}

func TestUpdateTerminalAuthorityRejectsIncompleteBinding(t *testing.T) {
	for _, name := range []string{"claude", "unknown-session", "path-session", "thread", "endpoint", "embedded-client", "pid", "signature", "process-error", "duplicate-pane", "extra-agent-process", "old-api", "disconnected"} {
		t.Run(name, func(t *testing.T) {
			n, target, h, f, p := terminalUpdateFixture(t)
			extra := []updateProcess{}
			switch name {
			case "claude":
				h.agents[0].Agent = "claude"
			case "unknown-session":
				h.agents[0].AgentSession = nil
			case "path-session":
				h.agents[0].AgentSession.Kind = "path"
			case "thread":
				p.RemoteThread = remoteOther
			case "endpoint":
				p.RemoteEndpoint = "ws://127.0.0.1:1"
			case "embedded-client":
				p.RemoteEndpoint = ""
			case "pid":
				h.pids = []int{456}
			case "signature":
				p.Signature = ""
			case "process-error":
				h.processErr = errors.New("unavailable")
			case "duplicate-pane":
				h.agents = append(h.agents, h.agents[0])
			case "extra-agent-process":
				extra = append(extra, updateProcess{PID: 456, Signature: "unknown"})
			case "old-api":
				f.mu.Lock()
				f.reject = "thread/backgroundTerminals/list"
				f.mu.Unlock()
			case "disconnected":
				f.mu.Lock()
				f.hangup = "thread/read"
				f.mu.Unlock()
			}
			out, err := n.collectUpdateInventory(context.Background(), target, func(context.Context) ([]updateProcess, error) { return append([]updateProcess{p}, extra...), nil })
			if err == nil || out.Complete {
				t.Fatalf("incomplete terminal became complete: %+v %v", out, err)
			}
		})
	}
}

func TestUpdateTerminalAuthorityRechecksActivityAndBinding(t *testing.T) {
	for _, name := range []string{"activity", "foreground", "session", "census", "host-membership"} {
		t.Run(name, func(t *testing.T) {
			n, target, h, f, p := terminalUpdateFixture(t)
			calls := 0
			h.read = func() {
				calls++
				if name == "host-membership" && calls == 1 {
					n.hosts.Set()
				}
				if name == "activity" && calls == 1 {
					f.mu.Lock()
					f.backgroundReply = map[string]any{"data": []any{map[string]any{"processId": "fixture-process", "command": "synthetic"}}}
					f.mu.Unlock()
				}
				if calls == 2 {
					if name == "foreground" {
						h.pids = []int{456}
					}
					if name == "session" {
						h.agents[0].AgentSession.Value = remoteOther
					}
				}
			}
			reads := 0
			out, err := n.collectUpdateInventory(context.Background(), target, func(context.Context) ([]updateProcess, error) {
				reads++
				if name == "census" && reads == 2 {
					p.Signature = "changed"
				}
				return []updateProcess{p}, nil
			})
			if err == nil || out.Complete {
				t.Fatalf("changing terminal authority became complete: %+v %v", out, err)
			}
		})
	}
}

func TestUpdateRemoteResumeRequiresExplicitUnambiguousArguments(t *testing.T) {
	endpoint := "unix:///tmp/synthetic-control.sock"
	for _, args := range [][]string{
		{"/usr/bin/codex", "resume", "--remote", endpoint, remoteRaw},
		// The form Persona's own Codex start writes (codex_start.go), with or without --no-alt-screen.
		{"codex", "--remote", endpoint, "resume", remoteRaw},
		{"/usr/bin/codex", "--remote", endpoint, "--no-alt-screen", "resume", remoteRaw},
	} {
		got, thread := updateRemoteResume(args)
		if got != endpoint || thread != remoteRaw {
			t.Fatalf("explicit remote resume %v: %q %q", args, got, thread)
		}
	}
	for _, args := range [][]string{
		{"codex", "resume", remoteRaw},
		{"codex", "resume", "--remote", endpoint, "--last"},
		{"codex", "resume", "--remote", endpoint, remoteRaw, "a prompt"},
		{"codex", "resume", "--remote", endpoint, "not-an-id"},
		{"codex", "resume", "--remote", "wss://example.com", remoteRaw},
		{"claude", "resume", "--remote", endpoint, remoteRaw},
		{"codex", "--remote", endpoint, "resume", remoteRaw, "a prompt"},
		{"codex", "--remote", endpoint, "resume", "--last"},
		{"codex", "--remote", endpoint, "-c", "model=x", "resume", remoteRaw},
		{"codex", "--remote", endpoint, "--no-alt-screen", "--no-alt-screen", "resume", remoteRaw},
		{"codex", "--remote", "wss://example.com", "resume", remoteRaw},
		{"codex", "--remote", endpoint, "resume"},
		{"codex", "--remote", endpoint, remoteRaw},
		{"claude", "--remote", endpoint, "resume", remoteRaw},
	} {
		if ep, id := updateRemoteResume(args); ep != "" || id != "" {
			t.Fatalf("ambiguous command bound a source: %+v", args)
		}
	}
}

func TestUpdateDarwinArgvDoesNotReadEnvironmentOrLoseBoundaries(t *testing.T) {
	args := []string{"/usr/bin/codex", "resume", "--remote", "unix:///tmp/a path.sock", remoteRaw}
	data := make([]byte, 4)
	binary.NativeEndian.PutUint32(data, uint32(len(args)))
	data = append(data, []byte("/usr/bin/codex\x00\x00")...)
	data = append(data, []byte(strings.Join(args, "\x00")+"\x00SECRET=synthetic\x00")...)
	got, err := updateDarwinArgv(data)
	if err != nil || strings.Join(got, "|") != strings.Join(args, "|") {
		t.Fatalf("kernel argument boundaries lost: %+v %v", got, err)
	}
	for _, bad := range [][]byte{nil, data[:3], data[:8], data[:len(data)-20]} {
		if _, err := updateDarwinArgv(bad); err == nil {
			t.Fatal("truncated kernel arguments accepted")
		}
	}
}
