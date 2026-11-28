package node

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/aiwire"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// TestAgyWarmFakeACPHelper is re-executed as a subprocess by the warm-pool tests to emulate both
// `agy acp` (JSON-RPC 2.0 over stdio) and cold `agy --input-format stream-json --output-format stream-json`
// deterministically, cross-platform, and with zero network calls.
func TestAgyWarmFakeACPHelper(t *testing.T) {
	if os.Getenv("PERSONA_FAKE_ACP_HELPER") != "1" {
		return
	}
	os.Exit(runFakeAgyHelper(os.Args[1:]))
}

type fakeAgyTraceEvent struct {
	Mode              string   `json:"mode"`
	PID               int      `json:"pid"`
	Method            string   `json:"method"`
	Args              []string `json:"args,omitempty"`
	Home              string   `json:"home,omitempty"`
	Cwd               string   `json:"cwd,omitempty"`
	DisableAutoUpdate string   `json:"disable_auto_update,omitempty"`
	ConfigDirEnv      string   `json:"config_dir_env,omitempty"`
	AgentMD           string   `json:"agent_md,omitempty"`
	SettingsJSON      string   `json:"settings_json,omitempty"`
	HasAgentsMD       bool     `json:"has_agents_md,omitempty"`
	SystemAppend      string   `json:"system_append,omitempty"`
	HasMeta           bool     `json:"has_meta,omitempty"`
	MCPServersCount   int      `json:"mcp_servers_count,omitempty"`
	SessionID         string   `json:"session_id,omitempty"`
	Model             string   `json:"model,omitempty"`
	Effort            string   `json:"effort,omitempty"`
	Prompt            string   `json:"prompt,omitempty"`
	AuthMethod        string   `json:"auth_method,omitempty"`
	ClientReply       string   `json:"client_reply,omitempty"`
}

func appendFakeTrace(path string, ev fakeAgyTraceEvent) {
	if path == "" {
		return
	}
	b, err := json.Marshal(ev)
	if err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, _ = f.Write(append(b, '\n'))
	_ = f.Close()
}

func runFakeAgyHelper(args []string) int {
	// Strip Go test flags before "--".
	if idx := slices.Index(args, "--"); idx >= 0 {
		args = args[idx+1:]
	}
	tracePath := os.Getenv("FAKE_ACP_TRACE")
	pid := os.Getpid()
	cwd, _ := os.Getwd()
	home := os.Getenv("HOME")

	// Cold CLI path when not invoked with `acp` subcommand.
	if len(args) == 0 || args[0] != "acp" {
		inBytes, _ := io.ReadAll(os.Stdin)
		var userMsg struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		_ = json.Unmarshal(bytesTrimSpace(inBytes), &userMsg)
		model := ""
		effort := ""
		for i := 0; i < len(args); i++ {
			if args[i] == "--model" && i+1 < len(args) {
				model = args[i+1]
			}
			if args[i] == "--effort" && i+1 < len(args) {
				effort = args[i+1]
			}
		}
		appendFakeTrace(tracePath, fakeAgyTraceEvent{
			Mode:   "cold",
			PID:    pid,
			Method: "cold_run",
			Args:   args,
			Home:   home,
			Cwd:    cwd,
			Model:  model,
			Effort: effort,
			Prompt: userMsg.Message.Content,
		})
		if model != "" && effort != "" && strings.HasSuffix(model, "-low") && effort != "low" {
			fmt.Fprintf(os.Stdout, `{"status":"ERROR","error":"--model %s conflicts with --effort=%s"}`+"\n", model, effort)
			return 1
		}
		if model == "" {
			model = "gemini-3.8-flash-low"
		}
		respObj := map[string]any{
			"event": "result",
			"result": map[string]any{
				"conversation_id": "cold-1",
				"status":          "SUCCESS",
				"response":        "cold:" + userMsg.Message.Content,
				"num_turns":       1,
				"usage": map[string]any{
					"input_tokens":      11,
					"output_tokens":     7,
					"cache_read_tokens": 3,
				},
			},
		}
		fmt.Fprintf(os.Stdout, `{"event":"init","conversation_id":"cold-1","init":{"model":%q}}`+"\n", model)
		b, _ := json.Marshal(respObj)
		fmt.Fprintln(os.Stdout, string(b))
		return 0
	}

	// ACP server mode.
	agentMDBytes, _ := os.ReadFile(filepath.Join(cwd, ".agents", "agents", agyModelRunAgentName, "agent.md"))
	settingsBytes, _ := os.ReadFile(filepath.Join(home, ".gemini", "antigravity-cli", "settings.json"))
	_, agentsMDErr := os.Stat(filepath.Join(cwd, "AGENTS.md"))
	appendFakeTrace(tracePath, fakeAgyTraceEvent{
		Mode:              "acp",
		PID:               pid,
		Method:            "startup",
		Args:              args,
		Home:              home,
		Cwd:               cwd,
		DisableAutoUpdate: os.Getenv("AGY_CLI_DISABLE_AUTO_UPDATE"),
		ConfigDirEnv:      os.Getenv("ANTIGRAVITY_CLI_CONFIG_DIR"),
		AgentMD:           string(agentMDBytes),
		SettingsJSON:      string(settingsBytes),
		HasAgentsMD:       agentsMDErr == nil,
	})

	mode := os.Getenv("FAKE_ACP_MODE")
	onceFile := os.Getenv("FAKE_ACP_ONCE_FILE")
	dynCmds := strings.Split(os.Getenv("FAKE_ACP_DYNAMIC_CMDS"), ",")
	gateAddr := os.Getenv("FAKE_ACP_GATE_ADDR")

	claimOnce := func() bool {
		if onceFile == "" {
			return false
		}
		f, err := os.OpenFile(onceFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return false
		}
		_ = f.Close()
		return true
	}

	if mode == "init_unknown_subcommand" {
		fmt.Fprintln(os.Stderr, `unknown command "acp" for "agy"`)
		return 2
	}
	if mode == "init_transient_crash_once" && claimOnce() {
		fmt.Fprintln(os.Stderr, "temporary startup I/O error")
		return 1
	}

	var writeMu sync.Mutex
	send := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		writeMu.Lock()
		_, _ = os.Stdout.Write(append(b, '\n'))
		writeMu.Unlock()
	}

	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)

	sessionSeq := 0
	sessionSystems := map[string]string{}
	sessionModels := map[string]string{}
	sessionEfforts := map[string]string{}
	pendingServerReply := map[string]chan string{}
	var replyMu sync.Mutex

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			continue
		}

		// Reply from client to a server->client request (permission/elicitation).
		if req.Method == "" && len(req.ID) > 0 {
			switch string(req.ID) {
			case `"perm-1"`:
				appendFakeTrace(tracePath, fakeAgyTraceEvent{Mode: "acp", PID: pid, Method: "permission_reply", ClientReply: line})
			case `"elicit-1"`:
				appendFakeTrace(tracePath, fakeAgyTraceEvent{Mode: "acp", PID: pid, Method: "elicitation_reply", ClientReply: line})
			}
			replyMu.Lock()
			ch := pendingServerReply[string(req.ID)]
			delete(pendingServerReply, string(req.ID))
			replyMu.Unlock()
			if ch != nil {
				ch <- line
			}
			continue
		}

		switch req.Method {
		case "initialize":
			appendFakeTrace(tracePath, fakeAgyTraceEvent{Mode: "acp", PID: pid, Method: "initialize"})
			if mode == "init_method_not_found" {
				send(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"error":   map[string]any{"code": -32601, "message": "Method not found: initialize"},
				})
				continue
			}
			if mode == "init_auth_once" && claimOnce() {
				send(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"error":   map[string]any{"code": -32000, "message": "Authentication required"},
				})
				continue
			}
			appendCap := mode != "no_cap"
			send(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"protocolVersion": 1,
					"agentInfo":       map[string]any{"name": "fake-agy", "version": "1.5.18-fake"},
					"authMethods":     []map[string]any{{"id": "gemini-api-key"}},
					"agentCapabilities": map[string]any{
						"_meta": map[string]any{
							"antigravity": map[string]any{
								"systemPrompt": map[string]any{
									"append": appendCap,
								},
							},
						},
					},
				},
			})

		case "authenticate":
			var p struct {
				MethodID string `json:"methodId"`
			}
			_ = json.Unmarshal(req.Params, &p)
			appendFakeTrace(tracePath, fakeAgyTraceEvent{Mode: "acp", PID: pid, Method: "authenticate", AuthMethod: p.MethodID})
			send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})

		case "session/new":
			var p struct {
				Cwd        string            `json:"cwd"`
				MCPServers []json.RawMessage `json:"mcpServers"`
				Meta       *struct {
					Antigravity *struct {
						SystemPrompt *struct {
							Append string `json:"append"`
						} `json:"systemPrompt"`
					} `json:"antigravity"`
				} `json:"_meta"`
			}
			_ = json.Unmarshal(req.Params, &p)
			sessionSeq++
			sid := fmt.Sprintf("sid-%d-%d", pid, sessionSeq)
			sysText := ""
			if p.Meta != nil && p.Meta.Antigravity != nil && p.Meta.Antigravity.SystemPrompt != nil {
				sysText = p.Meta.Antigravity.SystemPrompt.Append
			}
			sessionSystems[sid] = sysText
			sessionModels[sid] = "gemini-3.8-flash-low"

			// Simulate per-session files on disk inside isoHome to verify Guardrail ③ cleanup.
			isoData := filepath.Join(home, ".gemini", "antigravity-cli")
			_ = os.MkdirAll(filepath.Join(isoData, "brain", sid), 0o700)
			_ = os.WriteFile(filepath.Join(isoData, "brain", sid, "scratch.txt"), []byte("ephemeral"), 0o600)
			_ = os.MkdirAll(filepath.Join(isoData, "conversations"), 0o700)
			_ = os.WriteFile(filepath.Join(isoData, "conversations", sid+".db"), []byte("sqlite"), 0o600)
			_ = os.WriteFile(filepath.Join(isoData, "conversations", sid+".db-wal"), []byte("wal"), 0o600)
			_ = os.MkdirAll(filepath.Join(isoData, "annotations"), 0o700)
			_ = os.WriteFile(filepath.Join(isoData, "annotations", sid+".pbtxt"), []byte("ann"), 0o600)

			appendFakeTrace(tracePath, fakeAgyTraceEvent{
				Mode:            "acp",
				PID:             pid,
				Method:          "session/new",
				SessionID:       sid,
				SystemAppend:    sysText,
				HasMeta:         p.Meta != nil,
				MCPServersCount: len(p.MCPServers),
			})

			// Emit available_commands_update before returning session/new result.
			var cmds []map[string]any
			for _, c := range dynCmds {
				if c = strings.TrimSpace(c); c != "" {
					cmds = append(cmds, map[string]any{"name": c})
				}
			}
			if len(cmds) > 0 {
				send(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"sessionId": sid,
						"update": map[string]any{
							"sessionUpdate":     "available_commands_update",
							"availableCommands": cmds,
						},
					},
				})
			}

			send(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"sessionId": sid,
					"models":    map[string]any{"currentModelId": sessionModels[sid]},
					"configOptions": []map[string]any{
						{"id": "model", "category": "model", "currentValue": sessionModels[sid]},
						{"id": "thought_level", "category": "thought_level", "currentValue": "low"},
					},
				},
			})
			if mode == "block_stdin_after_session_new" {
				if gateAddr != "" {
					if conn, err := net.Dial("tcp", gateAddr); err == nil {
						_ = conn.Close()
					}
				}
				// Stop reading stdin until the node ends this process, so a large subsequent write fills the OS pipe
				// buffer. Wait for its termination signal, not in select {}: with every goroutine parked and no timer
				// or network waiter, Go on Linux ends such a process as a deadlock, a dead worker instead of a stalled
				// one (#17725). A goroutine waiting for a signal never counts as one.
				stop := make(chan os.Signal, 1)
				signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
				<-stop
				return 0
			}

		case "session/set_model":
			var p struct {
				SessionID string `json:"sessionId"`
				ModelID   string `json:"modelId"`
			}
			_ = json.Unmarshal(req.Params, &p)
			sessionModels[p.SessionID] = p.ModelID
			appendFakeTrace(tracePath, fakeAgyTraceEvent{Mode: "acp", PID: pid, Method: "session/set_model", SessionID: p.SessionID, Model: p.ModelID})
			send(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"configOptions": []map[string]any{
						{"id": "model", "category": "model", "currentValue": p.ModelID},
					},
				},
			})

		case "session/set_config_option":
			var p struct {
				SessionID string `json:"sessionId"`
				ConfigID  string `json:"configId"`
				Value     string `json:"value"`
			}
			_ = json.Unmarshal(req.Params, &p)
			sessionEfforts[p.SessionID] = p.Value
			appendFakeTrace(tracePath, fakeAgyTraceEvent{Mode: "acp", PID: pid, Method: "session/set_config_option", SessionID: p.SessionID, Effort: p.Value})
			send(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"configOptions": []map[string]any{
						{"id": "model", "category": "model", "currentValue": sessionModels[p.SessionID]},
						{"id": "thought_level", "category": "thought_level", "currentValue": p.Value},
					},
				},
			})

		case "session/prompt":
			var p struct {
				SessionID string `json:"sessionId"`
				Prompt    []struct {
					Text string `json:"text"`
				} `json:"prompt"`
			}
			_ = json.Unmarshal(req.Params, &p)
			promptText := ""
			if len(p.Prompt) > 0 {
				promptText = p.Prompt[0].Text
			}
			appendFakeTrace(tracePath, fakeAgyTraceEvent{
				Mode:      "acp",
				PID:       pid,
				Method:    "session/prompt",
				SessionID: p.SessionID,
				Prompt:    promptText,
				Model:     sessionModels[p.SessionID],
				Effort:    sessionEfforts[p.SessionID],
			})

			switch {
			case strings.HasPrefix(promptText, "STOP_REASON:"):
				reason := strings.TrimPrefix(promptText, "STOP_REASON:")
				send(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"sessionId": p.SessionID,
						"update": map[string]any{
							"sessionUpdate": "agent_message_chunk",
							"content":       map[string]any{"type": "text", "text": "partial:" + reason},
						},
					},
				})
				res := map[string]any{
					"usage": map[string]any{
						"inputTokens":      42,
						"outputTokens":     18,
						"cachedReadTokens": 10,
					},
				}
				if reason != "OMITTED" {
					res["stopReason"] = reason
				}
				send(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"result":  res,
				})
				continue

			case promptText == "TRIGGER_AUTH_ERR":
				send(map[string]any{
					"jsonrpc": "2.0",
					"id":      req.ID,
					"error":   map[string]any{"code": -32000, "message": "Authentication required"},
				})
				continue

			case promptText == "TRIGGER_CRASH":
				return 1

			case promptText == "TRIGGER_QUOTA_LINE":
				// agy streams an account error as one line and ends the turn normally (#17860).
				send(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"sessionId": p.SessionID,
						"update": map[string]any{
							"sessionUpdate": "agent_message_chunk",
							"content":       map[string]any{"type": "text", "text": "⚠ RESOURCE_EXHAUSTED (code 429): Individual quota reached. Resets in 118h."},
						},
					},
				})
				send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"stopReason": "end_turn"}})
				continue

			case promptText == "TRIGGER_PERMISSION":
				srvID := `"perm-1"`
				ch := make(chan string, 1)
				replyMu.Lock()
				pendingServerReply[srvID] = ch
				replyMu.Unlock()
				send(map[string]any{
					"jsonrpc": "2.0",
					"id":      "perm-1",
					"method":  "session/request_permission",
					"params":  map[string]any{"sessionId": p.SessionID},
				})
				// Wait in goroutine so scanner keeps reading the client's cancellation reply.
				go func(rid json.RawMessage) {
					<-ch
					send(map[string]any{"jsonrpc": "2.0", "id": rid, "result": map[string]any{"stopReason": "end_turn"}})
				}(req.ID)
				continue

			case promptText == "TRIGGER_ELICITATION":
				srvID := `"elicit-1"`
				ch := make(chan string, 1)
				replyMu.Lock()
				pendingServerReply[srvID] = ch
				replyMu.Unlock()
				send(map[string]any{
					"jsonrpc": "2.0",
					"id":      "elicit-1",
					"method":  "elicitation/create",
					"params":  map[string]any{"sessionId": p.SessionID},
				})
				go func(rid json.RawMessage) {
					<-ch
					send(map[string]any{"jsonrpc": "2.0", "id": rid, "result": map[string]any{"stopReason": "end_turn"}})
				}(req.ID)
				continue

			case promptText == "TRIGGER_TOOL_CALL":
				send(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"sessionId": p.SessionID,
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"name":          "run_command",
							"_meta":         map[string]any{"toolName": "run_command"},
						},
					},
				})
				send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{"stopReason": "end_turn"}})
				continue

			case promptText == "TRIGGER_MANAGE_TASK":
				send(map[string]any{
					"jsonrpc": "2.0",
					"method":  "session/update",
					"params": map[string]any{
						"sessionId": p.SessionID,
						"update": map[string]any{
							"sessionUpdate": "tool_call",
							"_meta":         map[string]any{"toolName": "manage_task"},
						},
					},
				})

			case strings.HasPrefix(promptText, "BLOCK_GATE") && gateAddr != "":
				// Handle asynchronously so session/cancel notification can still be read on stdin.
				go func(rid json.RawMessage, sid, pt string) {
					conn, err := net.Dial("tcp", gateAddr)
					if err == nil {
						var b [1]byte
						_, _ = conn.Read(b[:])
						_ = conn.Close()
					}
					send(map[string]any{
						"jsonrpc": "2.0",
						"method":  "session/update",
						"params": map[string]any{
							"sessionId": sid,
							"update": map[string]any{
								"sessionUpdate": "agent_message_chunk",
								"content":       map[string]any{"type": "text", "text": "warm-gated:" + pt},
							},
						},
					})
					send(map[string]any{
						"jsonrpc": "2.0",
						"id":      rid,
						"result": map[string]any{
							"stopReason": "end_turn",
							"usage": map[string]any{
								"inputTokens":       20,
								"outputTokens":      10,
								"cachedReadTokens":  5,
								"cachedWriteTokens": 2,
							},
						},
					})
				}(req.ID, p.SessionID, promptText)
				continue
			}

			// Emit ignored thought chunk followed by split message chunks.
			send(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": p.SessionID,
					"update": map[string]any{
						"sessionUpdate": "agent_thought_chunk",
						"content":       map[string]any{"type": "text", "text": "SECRET_THOUGHT_MUST_NOT_LEAK"},
					},
				},
			})
			replyText := fmt.Sprintf("warm(sys=%s):%s", sessionSystems[p.SessionID], promptText)
			mid := len(replyText) / 2
			send(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": p.SessionID,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": replyText[:mid]},
					},
				},
			})
			send(map[string]any{
				"jsonrpc": "2.0",
				"method":  "session/update",
				"params": map[string]any{
					"sessionId": p.SessionID,
					"update": map[string]any{
						"sessionUpdate": "agent_message_chunk",
						"content":       map[string]any{"type": "text", "text": replyText[mid:] + "\n"},
					},
				},
			})
			send(map[string]any{
				"jsonrpc": "2.0",
				"id":      req.ID,
				"result": map[string]any{
					"stopReason": "end_turn",
					"usage": map[string]any{
						"inputTokens":       120,
						"outputTokens":      35,
						"cachedReadTokens":  400,
						"cachedWriteTokens": 15,
					},
				},
			})

		case "session/cancel":
			var p struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(req.Params, &p)
			appendFakeTrace(tracePath, fakeAgyTraceEvent{Mode: "acp", PID: pid, Method: "session/cancel", SessionID: p.SessionID})

		case "session/close":
			var p struct {
				SessionID string `json:"sessionId"`
			}
			_ = json.Unmarshal(req.Params, &p)
			appendFakeTrace(tracePath, fakeAgyTraceEvent{Mode: "acp", PID: pid, Method: "session/close", SessionID: p.SessionID})
			send(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}})
		}
	}
	return 0
}

func bytesTrimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

type fakeAgyWarmFixture struct {
	home      string
	realAgy   string
	tracePath string
	node      *Node
	exePath   string
}

func setupFakeAgyWarm(t *testing.T, extraEnv map[string]string) *fakeAgyWarmFixture {
	t.Helper()
	sh := loginShellProgram()
	if sh == "" {
		t.Skip("no supported login shell")
	}
	if _, err := exec.LookPath(sh); err != nil {
		t.Skipf("login shell %q unavailable: %v", sh, err)
	}
	testBin, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	tracePath := filepath.Join(t.TempDir(), "trace.ndjson")
	var envLines strings.Builder
	envLines.WriteString("export PERSONA_FAKE_ACP_HELPER=1\n")
	envLines.WriteString("export FAKE_ACP_TRACE=" + shellQuote(tracePath) + "\n")
	for k, v := range extraEnv {
		envLines.WriteString("export " + k + "=" + shellQuote(v) + "\n")
	}
	script := "#!/bin/sh\n" + envLines.String() + `exec ` + shellQuote(testBin) + ` -test.run=^TestAgyWarmFakeACPHelper$ -- "$@"` + "\n"
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	exe := filepath.Join(binDir, "agy")
	if err := os.WriteFile(exe, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	rcLine := "export PATH=" + scriptPath(binDir) + ":$PATH\n"
	for _, rc := range []string{".zshrc", ".bashrc", ".bash_profile"} {
		if err := os.WriteFile(filepath.Join(home, rc), []byte(rcLine), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("HOME", home)
	t.Setenv("SHELL", sh)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	withShellLaunchers(t)
	launcherCache.Lock()
	launcherCache.list = []proto.Launcher{{Name: "agy", Source: "zshrc", Agent: "agy"}}
	launcherCache.Unlock()
	probeCache.Lock()
	oldAt, oldGot := probeCache.at, probeCache.got
	probeCache.at = map[string]time.Time{"agy": time.Now()}
	probeCache.got = map[string]proto.LauncherDetail{"agy": {Agent: "agy"}}
	probeCache.Unlock()
	t.Cleanup(func() {
		probeCache.Lock()
		probeCache.at, probeCache.got = oldAt, oldGot
		probeCache.Unlock()
	})
	realAgy := filepath.Join(home, ".gemini", "antigravity-cli")
	if err := os.MkdirAll(realAgy, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realAgy, "antigravity-oauth-token"), []byte("tok-v1"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realAgy, "settings.json"), []byte(`{"modelProvider":"oauth"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	n := &Node{home: home, dir: filepath.Join(home, ".persona"), logger: log.New(io.Discard, "", 0)}
	t.Cleanup(n.closeAgyModelRunPool)
	return &fakeAgyWarmFixture{
		home:      home,
		realAgy:   realAgy,
		tracePath: tracePath,
		node:      n,
		exePath:   filepath.Join(home, "bin", "agy"),
	}
}

func readFakeTrace(t *testing.T, path string) []fakeAgyTraceEvent {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatal(err)
	}
	var out []fakeAgyTraceEvent
	for line := range strings.SplitSeq(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var ev fakeAgyTraceEvent
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("bad trace line %q: %v", line, err)
		}
		out = append(out, ev)
	}
	return out
}

func waitAgyWarmUntil(t *testing.T, timeout time.Duration, cond func() bool, desc string) {
	t.Helper()
	if cond() {
		return
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			if cond() {
				return
			}
		case <-deadline.C:
			t.Fatalf("timed out waiting for %s", desc)
		}
	}
}

func TestPromptMatchesSlashCommandBoundaries(t *testing.T) {
	cmds := map[string]struct{}{
		"plan":     {},
		"grill-me": {},
		"learn":    {},
		"custom":   {},
	}
	shouldMatch := []string{
		"/plan",
		"/PLAN",
		"please check /plan now",
		"try (/grill-me) before coding",
		"run /learn.",
		"line1\n/custom\nline2",
		"帮我 /plan 一下",
	}
	for _, s := range shouldMatch {
		if !promptMatchesSlashCommand(s, cmds) {
			t.Errorf("expected slash command match for %q", s)
		}
	}
	shouldNotMatch := []string{
		"/plan/file.txt",
		"/etc/plan/file.txt",
		"./plan",
		"foo/plan",
		"https://example.com/plan",
		"/plan.md",
		"/planning",
		"/plan-v2",
		"/plan_1",
		"plain prompt without slash",
	}
	for _, s := range shouldNotMatch {
		if promptMatchesSlashCommand(s, cmds) {
			t.Errorf("unexpected slash command match for path/text %q", s)
		}
	}
}

// Scenario 1: Warm reuse hit (same PID, exact Text ignoring thought chunks, Model, Usage,
// isolated HOME/env/settings/empty agent.md, per-session brain/conversations/annotations cleanup,
// plus gemini-api-key authenticate and builtinAgyBacked direct exec).
func TestModelRunAgyWarmReuseAndSessionCleanup(t *testing.T) {
	fx := setupFakeAgyWarm(t, nil)
	if err := os.WriteFile(filepath.Join(fx.realAgy, "settings.json"), []byte(`{"modelProvider":"gemini"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", fx.realAgy)

	wantUsage := aiwire.Usage{
		InputTokens:              120,
		OutputTokens:             35,
		CacheReadInputTokens:     400,
		CacheCreationInputTokens: 15,
	}

	var firstWorkerDir string
	for i := range 3 {
		got, err := runModelWithNode(t, fx.node, proto.ModelRunParams{
			Launcher: "agy",
			Model:    "gemini-4-hydrogen-low",
			Effort:   "low",
			Prompt:   fmt.Sprintf("turn-%d", i),
		})
		a, ok := got.(proto.ModelRunAnswer)
		if err != nil || !ok {
			t.Fatalf("turn %d failed: %#v %v", i, got, err)
		}
		if a.Text != fmt.Sprintf("warm(sys=):turn-%d", i) {
			t.Fatalf("turn %d text = %q", i, a.Text)
		}
		if strings.Contains(a.Text, "SECRET_THOUGHT") {
			t.Fatalf("thought chunk leaked into answer: %q", a.Text)
		}
		if a.Model != "gemini-4-hydrogen-low" || a.Usage != wantUsage {
			t.Fatalf("turn %d model/usage mismatch: model=%q usage=%+v", i, a.Model, a.Usage)
		}
		if i == 0 {
			pool := fx.node.agyPool()
			pool.mu.Lock()
			if pool.worker != nil {
				firstWorkerDir = pool.worker.workerDir
			}
			pool.mu.Unlock()
		}
	}

	events := readFakeTrace(t, fx.tracePath)
	var startups, auths, news, closes []fakeAgyTraceEvent
	for _, ev := range events {
		switch ev.Method {
		case "startup":
			startups = append(startups, ev)
		case "authenticate":
			auths = append(auths, ev)
		case "session/new":
			news = append(news, ev)
		case "session/close":
			closes = append(closes, ev)
		}
	}
	if len(startups) != 1 {
		t.Fatalf("expected 1 warm startup, got %d", len(startups))
	}
	st := startups[0]
	if st.DisableAutoUpdate != "true" || st.ConfigDirEnv != "" {
		t.Fatalf("unexpected env: disableAutoUpdate=%q configDir=%q", st.DisableAutoUpdate, st.ConfigDirEnv)
	}
	if st.HasAgentsMD || !strings.HasSuffix(st.AgentMD, "---\n") {
		t.Fatalf("expected empty-body agent.md and no AGENTS.md: hasAgentsMD=%v agentMD=%q", st.HasAgentsMD, st.AgentMD)
	}
	if !strings.Contains(st.SettingsJSON, `"toolPermission":"strict"`) || !strings.Contains(st.SettingsJSON, `"modelProvider":"gemini"`) {
		t.Fatalf("unexpected isolated settings.json: %s", st.SettingsJSON)
	}
	if len(auths) != 1 || auths[0].AuthMethod != "gemini-api-key" {
		t.Fatalf("expected 1 gemini-api-key authenticate, got %+v", auths)
	}
	if len(news) != 3 || len(closes) != 3 {
		t.Fatalf("expected 3 session/new and 3 session/close, got %d / %d", len(news), len(closes))
	}
	for _, ev := range news {
		if ev.PID != st.PID || ev.MCPServersCount != 0 {
			t.Fatalf("expected same PID %d and mcpServers=[], got %+v", st.PID, ev)
		}
		// Guardrail ③: per-session brain/conversations/annotations must be deleted right after session/close.
		isoData := filepath.Join(st.Home, ".gemini", "antigravity-cli")
		for _, p := range []string{
			filepath.Join(isoData, "brain", ev.SessionID),
			filepath.Join(isoData, "conversations", ev.SessionID+".db"),
			filepath.Join(isoData, "conversations", ev.SessionID+".db-wal"),
			filepath.Join(isoData, "annotations", ev.SessionID+".pbtxt"),
		} {
			if _, err := os.Stat(p); !os.IsNotExist(err) {
				t.Errorf("session artifact leaked on disk: %s", p)
			}
		}
	}

	// Verify builtinAgyBacked direct exec path too.
	t.Run("builtin agy companion direct exec", func(t *testing.T) {
		fx2 := setupFakeAgyWarm(t, nil)
		n, _ := runtimeNode(t)
		n.home = fx2.home
		t.Cleanup(n.closeAgyModelRunPool)
		const inst = "builtin-warm-inst"
		n.runtimeHost.agy.mu.Lock()
		n.runtimeHost.agy.instance = inst
		n.runtimeHost.agy.bin = fx2.exePath
		n.runtimeHost.agy.mu.Unlock()
		dialRuntime(t, n, map[string]any{
			"runtime":  "agy",
			"agent":    "agy",
			"instance": inst,
			"launcher": "agy",
			"caps":     map[string]any{"start": true},
		})
		for range 2 {
			got, err := runModelWithNode(t, n, proto.ModelRunParams{Launcher: "agy", Prompt: "hello"})
			if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "warm(sys=):hello" {
				t.Fatalf("builtin warm failed: %#v %v", got, err)
			}
		}
		evs := readFakeTrace(t, fx2.tracePath)
		startupCount := 0
		for _, ev := range evs {
			if ev.Method == "startup" {
				startupCount++
			}
		}
		if startupCount != 1 {
			t.Fatalf("expected 1 builtin warm worker startup across 2 turns, got %d", startupCount)
		}
	})

	fx.node.closeAgyModelRunPool()
	if firstWorkerDir != "" {
		if _, err := os.Stat(firstWorkerDir); !os.IsNotExist(err) {
			t.Fatalf("workerDir left behind after close: %s", firstWorkerDir)
		}
	}
}

// Scenario 2: Dynamic System isolation (ALPHA -> BETA -> empty) and fail-closed fallback + caching
// when initialize omits systemPrompt.append capability.
func TestModelRunAgyWarmSystemIsolationAndCapabilityFallback(t *testing.T) {
	t.Run("dynamic system isolation on single worker", func(t *testing.T) {
		fx := setupFakeAgyWarm(t, nil)
		systems := []string{"ALPHA", "BETA", ""}
		for _, sys := range systems {
			got, err := runModelWithNode(t, fx.node, proto.ModelRunParams{
				Launcher: "agy",
				System:   sys,
				Prompt:   "check",
			})
			a, ok := got.(proto.ModelRunAnswer)
			if err != nil || !ok || a.Text != fmt.Sprintf("warm(sys=%s):check", sys) {
				t.Fatalf("system=%q: got %#v %v", sys, got, err)
			}
		}
		events := readFakeTrace(t, fx.tracePath)
		var news []fakeAgyTraceEvent
		for _, ev := range events {
			if ev.Method == "session/new" {
				news = append(news, ev)
			}
		}
		if len(news) != 3 {
			t.Fatalf("expected 3 session/new events, got %d", len(news))
		}
		if news[0].PID != news[1].PID || news[1].PID != news[2].PID {
			t.Fatalf("expected single reused PID across dynamic systems: %+v", news)
		}
		if news[0].SystemAppend != "ALPHA" || !news[0].HasMeta ||
			news[1].SystemAppend != "BETA" || !news[1].HasMeta ||
			news[2].SystemAppend != "" || news[2].HasMeta {
			t.Fatalf("unexpected _meta systemPrompt.append progression: %+v", news)
		}
	})

	t.Run("missing systemPrompt.append fails closed and caches unsupported", func(t *testing.T) {
		fx := setupFakeAgyWarm(t, map[string]string{"FAKE_ACP_MODE": "no_cap"})
		for i := range 2 {
			got, err := runModelWithNode(t, fx.node, proto.ModelRunParams{
				Launcher: "agy",
				System:   "ALPHA",
				Prompt:   fmt.Sprintf("p-%d", i),
			})
			a, ok := got.(proto.ModelRunAnswer)
			if err != nil || !ok || a.Text != fmt.Sprintf("cold:p-%d", i) {
				t.Fatalf("call %d expected cold fallback, got %#v %v", i, got, err)
			}
		}
		events := readFakeTrace(t, fx.tracePath)
		inits, colds := 0, 0
		for _, ev := range events {
			if ev.Method == "initialize" {
				inits++
			}
			if ev.Method == "cold_run" {
				colds++
			}
		}
		if inits != 1 || colds != 2 {
			t.Fatalf("expected 1 cached capability probe and 2 cold runs, got inits=%d colds=%d", inits, colds)
		}
	})
}

// Scenario 3: Static + dynamic availableCommands two-stage fallback (Stage 2 before session/prompt!)
// and path safety (/plan/file.txt stays warm).
func TestModelRunAgyWarmSlashCommandsTwoStageFallback(t *testing.T) {
	fx := setupFakeAgyWarm(t, map[string]string{"FAKE_ACP_DYNAMIC_CMDS": "plan,secretcmd"})

	// 1. First request carries dynamic unknown slash command `/secretcmd`:
	// Stage 1 doesn't know `secretcmd` yet -> enters session/new -> available_commands_update reveals `secretcmd`
	// -> Stage 2 aborts BEFORE session/prompt, calls session/close, and falls back to cold CLI.
	got, err := runModelWithNode(t, fx.node, proto.ModelRunParams{
		Launcher: "agy",
		Prompt:   "please run /secretcmd now",
	})
	if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "cold:please run /secretcmd now" {
		t.Fatalf("stage2 dynamic slash fallback failed: %#v %v", got, err)
	}

	// 2. Second request with `/secretcmd` hits Stage 1 directly (no new session/new).
	got, err = runModelWithNode(t, fx.node, proto.ModelRunParams{
		Launcher: "agy",
		Prompt:   "/secretcmd",
	})
	if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "cold:/secretcmd" {
		t.Fatalf("stage1 cached dynamic slash fallback failed: %#v %v", got, err)
	}

	// 3. Static built-in `/plan` falls back directly via Stage 1.
	got, err = runModelWithNode(t, fx.node, proto.ModelRunParams{
		Launcher: "agy",
		Prompt:   "please check /plan now",
	})
	if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "cold:please check /plan now" {
		t.Fatalf("static slash fallback failed: %#v %v", got, err)
	}

	// 4. Filesystem path `/plan/file.txt` must NOT false-trigger fallback; stays on warm worker.
	got, err = runModelWithNode(t, fx.node, proto.ModelRunParams{
		Launcher: "agy",
		Prompt:   "inspect /plan/file.txt and /secretcmd/readme.md",
	})
	if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "warm(sys=):inspect /plan/file.txt and /secretcmd/readme.md" {
		t.Fatalf("path prompt should stay on warm worker, got %#v %v", got, err)
	}

	events := readFakeTrace(t, fx.tracePath)
	acpPrompts := 0
	acpNews := 0
	acpCloses := 0
	for _, ev := range events {
		if ev.Mode != "acp" {
			continue
		}
		switch ev.Method {
		case "session/new":
			acpNews++
		case "session/prompt":
			acpPrompts++
			if strings.Contains(ev.Prompt, "/secretcmd ") || ev.Prompt == "/secretcmd" {
				t.Fatalf("session/prompt must never fire for matched slash command: %+v", ev)
			}
		case "session/close":
			acpCloses++
		}
	}
	// session/new #1 (Stage 2 aborted before prompt, closed), session/new #2 (path prompt, prompted & closed).
	if acpNews != 2 || acpPrompts != 1 || acpCloses != 2 {
		t.Fatalf("unexpected ACP counts: news=%d prompts=%d closes=%d", acpNews, acpPrompts, acpCloses)
	}
}

// Scenario 4: Security interception (session/request_permission -> cancelled, elicitation/create -> cancel,
// unexpected non-manage_task tool_call) fails closed and destroys/rebuilds the worker.
func TestModelRunAgyWarmSecurityInterceptionDestroysWorker(t *testing.T) {
	for _, tc := range []struct {
		name           string
		triggerPrompt  string
		wantReplyEvent string
		wantReplySub   string
	}{
		{"request_permission", "TRIGGER_PERMISSION", "permission_reply", `"cancelled"`},
		{"elicitation", "TRIGGER_ELICITATION", "elicitation_reply", `"cancel"`},
		{"unexpected_tool_call", "TRIGGER_TOOL_CALL", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := setupFakeAgyWarm(t, nil)
			// Warm up first to observe PID 1.
			if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "ok-1"}); err != nil {
				t.Fatalf("warmup failed: %v", err)
			}
			pool := fx.node.agyPool()
			pool.mu.Lock()
			pid1 := pool.worker.pid
			dir1 := pool.worker.workerDir
			pool.mu.Unlock()

			// Trigger security violation -> must fail closed and destroy worker.
			if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: tc.triggerPrompt}); err == nil {
				t.Fatalf("%s: expected fail-closed error, got nil", tc.name)
			}
			if _, err := os.Stat(dir1); !os.IsNotExist(err) {
				t.Fatalf("%s: workerDir leaked after security destroy: %s", tc.name, dir1)
			}

			// Next clean request must spawn a brand-new worker PID.
			got, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "ok-2"})
			if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "warm(sys=):ok-2" {
				t.Fatalf("%s: rebuild failed: %#v %v", tc.name, got, err)
			}
			pool.mu.Lock()
			pid2 := pool.worker.pid
			pool.mu.Unlock()
			if pid2 == pid1 {
				t.Fatalf("%s: expected new worker PID after destroy, stayed %d", tc.name, pid1)
			}

			if tc.wantReplyEvent != "" {
				events := readFakeTrace(t, fx.tracePath)
				found := false
				for _, ev := range events {
					if ev.Method == tc.wantReplyEvent && strings.Contains(ev.ClientReply, tc.wantReplySub) {
						found = true
						break
					}
				}
				if !found {
					t.Fatalf("%s: did not observe expected %s containing %s in %+v", tc.name, tc.wantReplyEvent, tc.wantReplySub, events)
				}
			}
		})
	}

	// Verify benign manage_task tool_call is tolerated on warm worker.
	t.Run("manage_task tolerated", func(t *testing.T) {
		fx := setupFakeAgyWarm(t, nil)
		got, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "TRIGGER_MANAGE_TASK"})
		if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "warm(sys=):TRIGGER_MANAGE_TASK" {
			t.Fatalf("manage_task should be tolerated, got %#v %v", got, err)
		}
	})
}

// Scenario 5: 2-way concurrent requests — 1st holds warm worker, 2nd immediately falls back to
// modelRunAgyCold without blocking or spawning a 2nd warm worker, clean under -race.
func TestModelRunAgyWarmConcurrentFallbackNoBlock(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	fx := setupFakeAgyWarm(t, map[string]string{"FAKE_ACP_GATE_ADDR": ln.Addr().String()})

	type res struct {
		got any
		err error
	}
	ch1 := make(chan res, 1)
	go func() {
		g, e := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "BLOCK_GATE_1"})
		ch1 <- res{g, e}
	}()

	// Wait deterministically until Request 1 is inside warm session/prompt.
	conn, err := ln.Accept()
	if err != nil {
		t.Fatal(err)
	}

	// Launch Request 2 while Request 1 is still holding the warm worker.
	got2, err2 := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "concurrent-2"})
	if a2, ok := got2.(proto.ModelRunAnswer); err2 != nil || !ok || a2.Text != "cold:concurrent-2" {
		_ = conn.Close()
		t.Fatalf("concurrent request #2 expected immediate cold fallback, got %#v %v", got2, err2)
	}

	// Release Request 1 and verify it finishes on the warm worker.
	_ = conn.Close()
	r1 := <-ch1
	if a1, ok := r1.got.(proto.ModelRunAnswer); r1.err != nil || !ok || a1.Text != "warm-gated:BLOCK_GATE_1" {
		t.Fatalf("request #1 expected warm answer, got %#v %v", r1.got, r1.err)
	}

	events := readFakeTrace(t, fx.tracePath)
	startups, colds := 0, 0
	for _, ev := range events {
		if ev.Method == "startup" {
			startups++
		}
		if ev.Method == "cold_run" {
			colds++
		}
	}
	if startups != 1 || colds != 1 {
		t.Fatalf("expected 1 warm startup and 1 cold fallback, got startups=%d colds=%d", startups, colds)
	}
}

// Scenario 6: Timeout, ctx cancellation (sends session/cancel), -32000 auth error (CodeModelRunLoggedOut),
// model+effort conflict fallback, and mid-turn crash all destroy cleanly with 0 directory residue and rebuild.
func TestModelRunAgyWarmAnomaliesDestroyAndRebuild(t *testing.T) {
	t.Run("timeout sends cancel destroys worker and rebuilds", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go func() {
			if c, e := ln.Accept(); e == nil {
				defer c.Close()
				var b [1]byte
				_, _ = c.Read(b[:])
			}
		}()

		fx := setupFakeAgyWarm(t, map[string]string{"FAKE_ACP_GATE_ADDR": ln.Addr().String()})
		// Warm up first so we know workerDir before the timeout.
		if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "warm-init"}); err != nil {
			t.Fatal(err)
		}
		pool := fx.node.agyPool()
		pool.mu.Lock()
		dir1 := pool.worker.workerDir
		pid1 := pool.worker.pid
		pool.mu.Unlock()

		_, err = runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "BLOCK_GATE_TIMEOUT", TimeoutMS: 250})
		if runCode(err) != proto.CodeModelRunTimeout {
			t.Fatalf("expected CodeModelRunTimeout, got %v", err)
		}
		if _, statErr := os.Stat(dir1); !os.IsNotExist(statErr) {
			t.Fatalf("workerDir leaked after timeout: %s", dir1)
		}
		events := readFakeTrace(t, fx.tracePath)
		sawCancel := false
		for _, ev := range events {
			if ev.Method == "session/cancel" {
				sawCancel = true
				break
			}
		}
		if !sawCancel {
			t.Fatalf("expected best-effort session/cancel notification on timeout")
		}

		// Rebuild cleanly.
		got, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "after-timeout"})
		if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "warm(sys=):after-timeout" {
			t.Fatalf("rebuild after timeout failed: %#v %v", got, err)
		}
		pool.mu.Lock()
		pid2 := pool.worker.pid
		pool.mu.Unlock()
		if pid2 == pid1 {
			t.Fatalf("expected new PID after timeout destroy")
		}
	})

	t.Run("ctx cancel destroys worker", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()

		fx := setupFakeAgyWarm(t, map[string]string{"FAKE_ACP_GATE_ADDR": ln.Addr().String()})
		if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "warm-init"}); err != nil {
			t.Fatal(err)
		}
		pool := fx.node.agyPool()
		pool.mu.Lock()
		dir1 := pool.worker.workerDir
		pool.mu.Unlock()

		ctx, cancel := context.WithCancel(context.Background())
		errCh := make(chan error, 1)
		go func() {
			raw, _ := json.Marshal(proto.ModelRunParams{Launcher: "agy", Prompt: "BLOCK_GATE_CANCEL"})
			_, e := fx.node.modelRun(ctx, raw)
			errCh <- e
		}()

		conn, err := ln.Accept()
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		cancel()

		if err := <-errCh; runCode(err) != proto.CodeModelRunTimeout {
			t.Fatalf("expected CodeModelRunTimeout on ctx cancel, got %v", err)
		}
		if _, statErr := os.Stat(dir1); !os.IsNotExist(statErr) {
			t.Fatalf("workerDir leaked after ctx cancel: %s", dir1)
		}
	})

	t.Run("auth error maps to CodeModelRunLoggedOut and destroys worker", func(t *testing.T) {
		fx := setupFakeAgyWarm(t, nil)
		if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "ok"}); err != nil {
			t.Fatal(err)
		}
		pool := fx.node.agyPool()
		pool.mu.Lock()
		dir1 := pool.worker.workerDir
		pool.mu.Unlock()

		_, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "TRIGGER_AUTH_ERR"})
		if runCode(err) != proto.CodeModelRunLoggedOut {
			t.Fatalf("expected CodeModelRunLoggedOut, got %v", err)
		}
		if _, statErr := os.Stat(dir1); !os.IsNotExist(statErr) {
			t.Fatalf("workerDir leaked after auth error: %s", dir1)
		}
	})

	t.Run("process crash destroys and rebuilds cleanly", func(t *testing.T) {
		fx := setupFakeAgyWarm(t, nil)
		if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "ok"}); err != nil {
			t.Fatal(err)
		}
		pool := fx.node.agyPool()
		pool.mu.Lock()
		dir1 := pool.worker.workerDir
		pid1 := pool.worker.pid
		pool.mu.Unlock()

		if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "TRIGGER_CRASH"}); err == nil {
			t.Fatal("expected error on worker crash")
		}
		if _, statErr := os.Stat(dir1); !os.IsNotExist(statErr) {
			t.Fatalf("workerDir leaked after crash: %s", dir1)
		}
		got, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "recovered"})
		if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "warm(sys=):recovered" {
			t.Fatalf("recovery after crash failed: %#v %v", got, err)
		}
		pool.mu.Lock()
		pid2 := pool.worker.pid
		pool.mu.Unlock()
		if pid2 == pid1 {
			t.Fatalf("expected new PID after crash, stayed %d", pid1)
		}
	})

	t.Run("conflicting model and effort falls back to cold CLI semantics", func(t *testing.T) {
		fx := setupFakeAgyWarm(t, nil)
		_, err := runModelWithNode(t, fx.node, proto.ModelRunParams{
			Launcher: "agy",
			Model:    "gemini-4-hydrogen-low",
			Effort:   "medium",
			Prompt:   "ping",
		})
		if err == nil || !strings.Contains(err.Error(), "conflicts with --effort=medium") {
			t.Fatalf("expected cold CLI conflict error, got %v", err)
		}
		events := readFakeTrace(t, fx.tracePath)
		for _, ev := range events {
			if ev.Mode == "acp" {
				t.Fatalf("conflicting model+effort should bypass warm ACP, saw %+v", ev)
			}
		}
	})
}

// Scenario 7: Key invalidation (oauth token mtime/size + settings.json), short idle TTL auto-eviction,
// max-request rotation, and Node.Serve exit cleanup.
func TestModelRunAgyWarmInvalidationTTLRotationAndServeClose(t *testing.T) {
	t.Run("oauth and settings key invalidation", func(t *testing.T) {
		fx := setupFakeAgyWarm(t, nil)
		if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "t1"}); err != nil {
			t.Fatal(err)
		}
		pool := fx.node.agyPool()
		pool.mu.Lock()
		pid1 := pool.worker.pid
		dir1 := pool.worker.workerDir
		pool.mu.Unlock()

		// Update oauth token (changes size + mtime).
		tokPath := filepath.Join(fx.realAgy, "antigravity-oauth-token")
		if err := os.WriteFile(tokPath, []byte("tok-v2-refreshed"), 0o600); err != nil {
			t.Fatal(err)
		}
		future := time.Now().Add(2 * time.Second)
		_ = os.Chtimes(tokPath, future, future)

		if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "t2"}); err != nil {
			t.Fatal(err)
		}
		pool.mu.Lock()
		pid2 := pool.worker.pid
		dir2 := pool.worker.workerDir
		pool.mu.Unlock()
		if pid2 == pid1 {
			t.Fatalf("expected worker replacement after oauth token change, PID stayed %d", pid1)
		}
		if _, err := os.Stat(dir1); !os.IsNotExist(err) {
			t.Fatalf("old workerDir leaked after oauth invalidation: %s", dir1)
		}

		// Update settings.json (modelProvider change).
		if err := os.WriteFile(filepath.Join(fx.realAgy, "settings.json"), []byte(`{"modelProvider":"gemini"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "t3"}); err != nil {
			t.Fatal(err)
		}
		pool.mu.Lock()
		pid3 := pool.worker.pid
		pool.mu.Unlock()
		if pid3 == pid2 {
			t.Fatalf("expected worker replacement after settings change, PID stayed %d", pid2)
		}
		if _, err := os.Stat(dir2); !os.IsNotExist(err) {
			t.Fatalf("old workerDir leaked after settings invalidation: %s", dir2)
		}
	})

	t.Run("short idle TTL auto eviction", func(t *testing.T) {
		fx := setupFakeAgyWarm(t, nil)
		evicted := make(chan struct{}, 1)
		pool := fx.node.agyPool()
		pool.mu.Lock()
		pool.idleTTL = 80 * time.Millisecond
		pool.onIdleEvict = func() {
			select {
			case evicted <- struct{}{}:
			default:
			}
		}
		pool.mu.Unlock()

		if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "ttl-1"}); err != nil {
			t.Fatal(err)
		}
		pool.mu.Lock()
		pid1 := pool.worker.pid
		dir1 := pool.worker.workerDir
		pool.mu.Unlock()

		select {
		case <-evicted:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for idle TTL eviction")
		}
		if _, err := os.Stat(dir1); !os.IsNotExist(err) {
			t.Fatalf("workerDir leaked after idle TTL eviction: %s", dir1)
		}
		waitAgyWarmUntil(t, 2*time.Second, func() bool { return !osproc.Alive(pid1) }, "evicted worker exit")

		if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "ttl-2"}); err != nil {
			t.Fatal(err)
		}
		pool.mu.Lock()
		pid2 := pool.worker.pid
		pool.mu.Unlock()
		if pid2 == pid1 {
			t.Fatalf("expected new PID after idle eviction, stayed %d", pid1)
		}
	})

	t.Run("max requests graceful rotation", func(t *testing.T) {
		fx := setupFakeAgyWarm(t, nil)
		pool := fx.node.agyPool()
		pool.mu.Lock()
		pool.maxRequests = 2
		pool.mu.Unlock()

		if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "r1"}); err != nil {
			t.Fatal(err)
		}
		pool.mu.Lock()
		pid1 := pool.worker.pid
		dir1 := pool.worker.workerDir
		pool.mu.Unlock()

		if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "r2"}); err != nil {
			t.Fatal(err)
		}
		// After reaching maxRequests=2, worker must already be gracefully destroyed.
		pool.mu.Lock()
		wAfter2 := pool.worker
		pool.mu.Unlock()
		if wAfter2 != nil {
			t.Fatalf("expected worker rotated out after reaching maxRequests=2")
		}
		if _, err := os.Stat(dir1); !os.IsNotExist(err) {
			t.Fatalf("rotated workerDir leaked: %s", dir1)
		}

		if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "r3"}); err != nil {
			t.Fatal(err)
		}
		pool.mu.Lock()
		pid2 := pool.worker.pid
		pool.mu.Unlock()
		if pid2 == pid1 {
			t.Fatalf("expected fresh PID after rotation, stayed %d", pid1)
		}
	})

	t.Run("Node.Serve exit closes warm pool cleanly", func(t *testing.T) {
		fx := setupFakeAgyWarm(t, nil)
		n, err := New(fx.node.dir)
		if err != nil {
			t.Fatal(err)
		}
		n.logger = log.New(io.Discard, "", 0)
		n.home = fx.home

		pr, pw := io.Pipe()
		serveDone := make(chan error, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			serveDone <- n.Serve(ctx, pr, io.Discard)
		}()

		// Warm up a worker on this live Node.
		if _, err := runModelWithNode(t, n, proto.ModelRunParams{Launcher: "agy", Prompt: "serve-1"}); err != nil {
			t.Fatal(err)
		}
		pool := n.agyPool()
		pool.mu.Lock()
		pid := pool.worker.pid
		dir := pool.worker.workerDir
		pool.mu.Unlock()

		// End the hub connection so Node.Serve exits and runs defer n.closeAgyModelRunPool().
		_ = pw.Close()
		cancel()
		select {
		case <-serveDone:
		case <-time.After(10 * time.Second):
			t.Fatal("Node.Serve did not exit")
		}
		if _, err := os.Stat(dir); !os.IsNotExist(err) {
			t.Fatalf("workerDir leaked after Node.Serve exit: %s", dir)
		}
		waitAgyWarmUntil(t, 2*time.Second, func() bool { return !osproc.Alive(pid) }, "worker exit after Serve")
	})
}

// Regression test (PR #16796 P2 #1 & ma-review): normal terminal ACP stopReasons (empty/omitted,
// `max_tokens`, `max_turn_requests`, `refusal`) preserve aggregated partial text, usage, and the
// warm worker (reused on the next turn), while `cancelled` and unknown stopReasons fail closed and
// destroy the worker.
func TestModelRunAgyWarmStopReasons(t *testing.T) {
	wantUsage := aiwire.Usage{InputTokens: 42, OutputTokens: 18, CacheReadInputTokens: 10}
	for _, tc := range []struct {
		name   string
		reason string
	}{
		{"empty", ""},
		{"omitted", "OMITTED"},
		{"max_tokens", "max_tokens"},
		{"max_turn_requests", "max_turn_requests"},
		{"refusal", "refusal"},
	} {
		reason := tc.reason
		t.Run("normal_stop_"+tc.name, func(t *testing.T) {
			fx := setupFakeAgyWarm(t, nil)
			got, err := runModelWithNode(t, fx.node, proto.ModelRunParams{
				Launcher: "agy",
				Prompt:   "STOP_REASON:" + reason,
			})
			a, ok := got.(proto.ModelRunAnswer)
			if err != nil || !ok || a.Text != "partial:"+reason || a.Usage != wantUsage {
				t.Fatalf("stopReason=%q: got %#v %v", reason, got, err)
			}
			pool := fx.node.agyPool()
			pool.mu.Lock()
			if pool.worker == nil {
				pool.mu.Unlock()
				t.Fatalf("stopReason=%q: expected warm worker kept alive", reason)
			}
			pid1 := pool.worker.pid
			pool.mu.Unlock()

			// Subsequent turn must reuse the exact same warm worker PID.
			got2, err2 := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "after-" + tc.name})
			if a2, ok2 := got2.(proto.ModelRunAnswer); err2 != nil || !ok2 || a2.Text != "warm(sys=):after-"+tc.name {
				t.Fatalf("follow-up after %s failed: %#v %v", tc.name, got2, err2)
			}
			pool.mu.Lock()
			pid2 := pool.worker.pid
			pool.mu.Unlock()
			if pid2 != pid1 {
				t.Fatalf("stopReason=%q destroyed warm worker: pid1=%d pid2=%d", reason, pid1, pid2)
			}
		})
	}

	for _, tc := range []struct {
		name   string
		reason string
	}{
		{"cancelled", "cancelled"},
		{"unexpected_error", "unexpected_error"},
	} {
		reason := tc.reason
		t.Run("abnormal_stop_"+tc.name, func(t *testing.T) {
			fx := setupFakeAgyWarm(t, nil)
			if _, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "warmup"}); err != nil {
				t.Fatal(err)
			}
			pool := fx.node.agyPool()
			pool.mu.Lock()
			pid1 := pool.worker.pid
			dir1 := pool.worker.workerDir
			pool.mu.Unlock()

			_, err := runModelWithNode(t, fx.node, proto.ModelRunParams{
				Launcher: "agy",
				Prompt:   "STOP_REASON:" + reason,
			})
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("stopReason %q", reason)) {
				t.Fatalf("stopReason=%q: expected fail-closed error, got %v", reason, err)
			}
			if _, statErr := os.Stat(dir1); !os.IsNotExist(statErr) {
				t.Fatalf("stopReason=%s: workerDir leaked: %s", reason, dir1)
			}

			// Rebuild must use a fresh worker PID.
			got2, err2 := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "recover"})
			if a2, ok2 := got2.(proto.ModelRunAnswer); err2 != nil || !ok2 || a2.Text != "warm(sys=):recover" {
				t.Fatalf("recover after %s failed: %#v %v", reason, got2, err2)
			}
			pool.mu.Lock()
			pid2 := pool.worker.pid
			pool.mu.Unlock()
			if pid2 == pid1 {
				t.Fatalf("stopReason=%s did not replace worker: stayed pid %d", reason, pid1)
			}
		})
	}
}

// Regression test (PR #16796 P2 #2): transient cmd.Start / auth / crash failures during warm startup fall back
// without permanently marking the launcher unsupported, while explicit permanent incompatibility (-32601 Method
// not found or unknown acp subcommand) caches unsupported for that launcherKey.
func TestModelRunAgyWarmTransientVsPermanentInitFailures(t *testing.T) {
	t.Run("transient_cmd_start_failure_recovers_on_next_call", func(t *testing.T) {
		fx := setupFakeAgyWarm(t, nil)
		n, _ := runtimeNode(t)
		n.home = fx.home
		t.Cleanup(n.closeAgyModelRunPool)
		const inst = "builtin-transient-start"
		n.runtimeHost.agy.mu.Lock()
		n.runtimeHost.agy.instance = inst
		n.runtimeHost.agy.bin = fx.exePath
		n.runtimeHost.agy.mu.Unlock()
		dialRuntime(t, n, map[string]any{
			"runtime":  "agy",
			"agent":    "agy",
			"instance": inst,
			"launcher": "agy",
			"caps":     map[string]any{"start": true},
		})

		// Simulate transient exec failure (e.g. non-executable/busy binary during CLI upgrade).
		if err := os.Chmod(fx.exePath, 0o600); err != nil {
			t.Fatal(err)
		}
		_, _ = runModelWithNode(t, n, proto.ModelRunParams{Launcher: "agy", Prompt: "during-upgrade"})
		if n.agyPool().isUnsupported("builtin:" + fx.exePath) {
			t.Fatal("transient cmd.Start error must not permanently mark launcher unsupported")
		}

		// Restore executable permission: next call must start and use warm ACP cleanly.
		if err := os.Chmod(fx.exePath, 0o755); err != nil {
			t.Fatal(err)
		}
		got, err := runModelWithNode(t, n, proto.ModelRunParams{Launcher: "agy", Prompt: "after-upgrade"})
		if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != "warm(sys=):after-upgrade" {
			t.Fatalf("expected warm recovery after transient cmd.Start failure, got %#v %v", got, err)
		}
	})

	for _, tc := range []struct {
		name string
		mode string
	}{
		{"transient_init_auth_error_recovers", "init_auth_once"},
		{"transient_init_crash_recovers", "init_transient_crash_once"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			onceFile := filepath.Join(t.TempDir(), "once.flag")
			fx := setupFakeAgyWarm(t, map[string]string{
				"FAKE_ACP_MODE":      tc.mode,
				"FAKE_ACP_ONCE_FILE": onceFile,
			})
			// 1st call hits transient failure -> falls back to cold without caching unsupported.
			got1, err1 := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "first"})
			if a1, ok1 := got1.(proto.ModelRunAnswer); err1 != nil || !ok1 || a1.Text != "cold:first" {
				t.Fatalf("1st call expected cold fallback, got %#v %v", got1, err1)
			}
			// 2nd call must retry warm ACP and succeed.
			got2, err2 := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: "second"})
			if a2, ok2 := got2.(proto.ModelRunAnswer); err2 != nil || !ok2 || a2.Text != "warm(sys=):second" {
				t.Fatalf("2nd call expected warm recovery, got %#v %v", got2, err2)
			}
		})
	}

	for _, tc := range []struct {
		name       string
		mode       string
		wantStarts int
	}{
		{"permanent_method_not_found_cached", "init_method_not_found", 1},
		{"permanent_unknown_subcommand_cached", "init_unknown_subcommand", 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := setupFakeAgyWarm(t, map[string]string{"FAKE_ACP_MODE": tc.mode})
			for i := range 2 {
				got, err := runModelWithNode(t, fx.node, proto.ModelRunParams{Launcher: "agy", Prompt: fmt.Sprintf("p-%d", i)})
				if a, ok := got.(proto.ModelRunAnswer); err != nil || !ok || a.Text != fmt.Sprintf("cold:p-%d", i) {
					t.Fatalf("call %d expected cold fallback, got %#v %v", i, got, err)
				}
			}
			events := readFakeTrace(t, fx.tracePath)
			startups, colds := 0, 0
			for _, ev := range events {
				if ev.Method == "startup" {
					startups++
				}
				if ev.Method == "cold_run" {
					colds++
				}
			}
			if startups != tc.wantStarts || colds != 2 {
				t.Fatalf("%s: expected %d startup probe and 2 cold runs, got startups=%d colds=%d", tc.name, tc.wantStarts, startups, colds)
			}
		})
	}
}

// Regression test (PR #16796 P2 #3): a blocked large writeFrame on a full stdin pipe respects ctx cancellation
// and never deadlocks with destroy() / pool.close(), leaving zero leaked goroutines or temp directories.
func TestModelRunAgyWarmWriteBlockedNoDeadlock(t *testing.T) {
	t.Run("direct_blocked_pipe_write_interrupted_by_destroy", func(t *testing.T) {
		stdinR, stdinW, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer stdinR.Close()
		stdoutR, stdoutW, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer stdoutW.Close()
		stderrR, stderrW, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer stderrW.Close()

		writeStarted := make(chan struct{})
		var startOnce sync.Once
		w := &agyWarmWorker{
			stdin:         stdinW,
			stdout:        stdoutR,
			stderr:        stderrR,
			writeCh:       make(chan agyWriteReq),
			stopWriteCh:   make(chan struct{}),
			writerDone:    make(chan struct{}),
			pending:       map[int64]chan agyRPCResponse{},
			dynamicCmds:   map[string]struct{}{},
			startupAuthCh: make(chan struct{}),
			commandsReady: make(chan struct{}),
			stdoutDone:    make(chan struct{}),
			stderrDone:    make(chan struct{}),
			waitDone:      make(chan struct{}),
			onBeforeWrite: func() {
				startOnce.Do(func() { close(writeStarted) })
			},
		}
		close(w.waitDone)
		go w.writeLoop()
		go w.readStdoutLoop()
		go w.readStderrLoop()

		// 512 KiB exceeds OS pipe buffer so w.stdin.Write blocks until interrupted by destroy().
		bigPayload := map[string]any{"data": strings.Repeat("A", 512*1024)}
		writeErrCh := make(chan error, 1)
		go func() {
			writeErrCh <- w.writeFrame(context.Background(), bigPayload, nil)
		}()

		select {
		case <-writeStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("writeLoop did not start writing")
		}

		destroyDone := make(chan struct{})
		go func() {
			w.destroy()
			close(destroyDone)
		}()

		select {
		case <-destroyDone:
		case <-time.After(5 * time.Second):
			t.Fatal("destroy() deadlocked against blocked writeFrame")
		}
		select {
		case werr := <-writeErrCh:
			if werr == nil {
				t.Fatal("expected writeFrame error after destroy")
			}
		case <-time.After(5 * time.Second):
			t.Fatal("writeFrame did not return after destroy")
		}
		select {
		case <-w.writerDone:
		default:
			t.Fatal("writerDone not closed after destroy")
		}
	})

	t.Run("request_timeout_unblocks_stalled_prompt_write_and_cleans_up", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()
		go func() {
			if c, e := ln.Accept(); e == nil {
				_ = c.Close()
			}
		}()

		fx := setupFakeAgyWarm(t, map[string]string{
			"FAKE_ACP_MODE":      "block_stdin_after_session_new",
			"FAKE_ACP_GATE_ADDR": ln.Addr().String(),
		})
		bigPrompt := strings.Repeat("Z", 512*1024)
		start := time.Now()
		_, err = runModelWithNode(t, fx.node, proto.ModelRunParams{
			Launcher:  "agy",
			Prompt:    bigPrompt,
			TimeoutMS: 300,
		})
		elapsed := time.Since(start)
		if runCode(err) != proto.CodeModelRunTimeout {
			t.Fatalf("expected CodeModelRunTimeout on stalled stdin write, got %v (elapsed %s)", err, elapsed)
		}
		if elapsed > 5*time.Second {
			t.Fatalf("stalled stdin write took too long to abort: %s", elapsed)
		}
		events := readFakeTrace(t, fx.tracePath)
		for _, ev := range events {
			if ev.Method == "startup" && ev.Cwd != "" {
				workerDir := filepath.Dir(ev.Cwd)
				if _, statErr := os.Stat(workerDir); !os.IsNotExist(statErr) {
					t.Fatalf("workerDir leaked after stalled write timeout: %s", workerDir)
				}
				waitAgyWarmUntil(t, 2*time.Second, func() bool { return !osproc.Alive(ev.PID) }, "stalled worker exit")
			}
		}
	})

	t.Run("concurrent_pool_close_unblocks_stalled_prompt_write", func(t *testing.T) {
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		defer ln.Close()

		fx := setupFakeAgyWarm(t, map[string]string{
			"FAKE_ACP_MODE":      "block_stdin_after_session_new",
			"FAKE_ACP_GATE_ADDR": ln.Addr().String(),
		})
		doneCh := make(chan error, 1)
		go func() {
			_, e := runModelWithNode(t, fx.node, proto.ModelRunParams{
				Launcher:  "agy",
				Prompt:    strings.Repeat("Q", 512*1024),
				TimeoutMS: 30000,
			})
			doneCh <- e
		}()

		// Wait deterministically until fake ACP finished session/new and stopped reading stdin.
		conn, err := ln.Accept()
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.Close()

		closeDone := make(chan struct{})
		go func() {
			fx.node.closeAgyModelRunPool()
			close(closeDone)
		}()

		select {
		case <-closeDone:
		case <-time.After(5 * time.Second):
			t.Fatal("closeAgyModelRunPool deadlocked against stalled prompt write")
		}
		select {
		case <-doneCh:
		case <-time.After(5 * time.Second):
			t.Fatal("in-flight modelRun did not unblock after closeAgyModelRunPool")
		}
	})
}

// TestModelRunAgyRealBench is an opt-in real-device end-to-end verification against an installed `agy` CLI
// (skipped unless PERSONA_REAL_AGY_BENCH=1). It benchmarks cold vs warm latency, checks Usage/Model parity,
// measures warm process RSS, verifies dynamic System isolation and zero cross-session bleed, verifies short
// idle TTL eviction with 0 process/temp-dir residue, and confirms zero pollution of ~/.gemini/antigravity-cli.
func TestModelRunAgyRealBench(t *testing.T) {
	if os.Getenv("PERSONA_REAL_AGY_BENCH") != "1" {
		t.Skip("set PERSONA_REAL_AGY_BENCH=1 to run real agy end-to-end verification")
	}
	bin, err := exec.LookPath("agy")
	if err != nil {
		t.Fatalf("agy not on PATH: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	model := cmp.Or(os.Getenv("PERSONA_REAL_AGY_MODEL"), "gemini-3.8-flash-low")

	realAgyDir := filepath.Join(home, ".gemini", "antigravity-cli")
	// Snapshot top-level session entries (brain/, conversations/, annotations/, presence/) plus settings/token stamps
	// so unrelated already-active host agy sessions updating their own transcript do not cause false positives, while
	// any new session file or config mutation from model.run is immediately caught.
	snapshotRealAgy := func(root string) map[string]string {
		snap := map[string]string{}
		for _, sub := range []string{"brain", "conversations", "annotations", "presence"} {
			entries, err := os.ReadDir(filepath.Join(root, sub))
			if err != nil {
				continue
			}
			for _, e := range entries {
				name := e.Name()
				if strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") {
					continue
				}
				snap[sub+"/"+name] = "present"
			}
		}
		for _, f := range []string{"settings.json", "antigravity-oauth-token"} {
			if info, err := os.Stat(filepath.Join(root, f)); err == nil {
				snap[f] = fmt.Sprintf("%d:%d", info.Size(), info.ModTime().UnixNano())
			}
		}
		return snap
	}
	beforeReal := snapshotRealAgy(realAgyDir)

	n := &Node{
		home:   home,
		dir:    filepath.Join(t.TempDir(), ".persona"),
		logger: log.New(io.Discard, "", 0),
	}
	t.Cleanup(n.closeAgyModelRunPool)
	// Point builtinAgyBacked directly at the installed agy binary to measure pure agy startup vs warm reuse.
	n.runtimeHost = &RuntimeHost{}
	n.runtimeHost.agy.bin = bin

	timedOut := &rpc.Error{Code: proto.CodeModelRunTimeout, Message: "bench timeout"}

	// 1. Cold start baseline (n=2).
	var coldLatencies []time.Duration
	var coldUsages []aiwire.Usage
	for i := range 2 {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		start := time.Now()
		ans, err := n.modelRunAgyCold(ctx, proto.ModelRunParams{
			Launcher: "agy",
			Model:    model,
			System:   "Reply with exactly one word: PONG",
			Prompt:   fmt.Sprintf("cold ping %d", i+1),
		}, true, timedOut)
		elapsed := time.Since(start)
		cancel()
		if err != nil {
			t.Fatalf("cold run %d failed: %v", i+1, err)
		}
		coldLatencies = append(coldLatencies, elapsed)
		coldUsages = append(coldUsages, ans.Usage)
		t.Logf("COLD #%d: %d ms | model=%q | text=%q | usage=%+v", i+1, elapsed.Milliseconds(), ans.Model, ans.Text, ans.Usage)
	}

	// 2. Warm pool: 1st request (cold boot of warm worker) + 3 steady-state warm requests with dynamic System & bleed checks.
	pool := n.agyPool()
	evicted := make(chan struct{}, 1)
	pool.mu.Lock()
	pool.idleTTL = 5 * time.Second
	pool.onIdleEvict = func() {
		select {
		case evicted <- struct{}{}:
		default:
		}
	}
	pool.mu.Unlock()

	warmCases := []struct {
		label  string
		system string
		prompt string
	}{
		{"WARM_FIRST", "You must reply with only the exact token ALPHA-7319 and nothing else.", "Remember secret phrase ORCHID-9942. Now reply."},
		{"WARM_STEADY_1", "You must reply with only the exact token BETA-4820 and nothing else.", "What was the secret phrase from earlier? If none, follow your system instruction."},
		{"WARM_STEADY_2", "", "Reply with only the single word OK."},
		{"WARM_STEADY_3", "Reply with only the word PONG.", "warm ping"},
	}

	var warmPID int
	var warmWorkerDir string
	var warmSteadyLatencies []time.Duration
	for idx, wc := range warmCases {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		start := time.Now()
		ans, err := n.modelRunAgy(ctx, proto.ModelRunParams{
			Launcher: "agy",
			Model:    model,
			System:   wc.system,
			Prompt:   wc.prompt,
		}, true, timedOut)
		elapsed := time.Since(start)
		cancel()
		if err != nil {
			t.Fatalf("%s failed: %v", wc.label, err)
		}
		pool.mu.Lock()
		if pool.worker == nil {
			pool.mu.Unlock()
			t.Fatalf("%s: expected warm worker to remain alive in pool", wc.label)
		}
		pid := pool.worker.pid
		wdir := pool.worker.workerDir
		pool.mu.Unlock()
		if idx == 0 {
			warmPID = pid
			warmWorkerDir = wdir
		} else {
			if pid != warmPID {
				t.Fatalf("%s: expected reused warm PID %d, got %d", wc.label, warmPID, pid)
			}
			warmSteadyLatencies = append(warmSteadyLatencies, elapsed)
		}
		if idx == 1 && strings.Contains(strings.ToUpper(ans.Text), "ORCHID-9942") {
			t.Fatalf("cross-session bleed detected in %s: %q", wc.label, ans.Text)
		}
		rssKB := readProcTreeRSSKB(warmPID)
		t.Logf("%s: %d ms | pid=%d | rss_kb=%d (%.1f MiB) | model=%q | text=%q | usage=%+v",
			wc.label, elapsed.Milliseconds(), pid, rssKB, float64(rssKB)/1024.0, ans.Model, ans.Text, ans.Usage)
	}

	// 3. Wait for short idle TTL (5s) auto-eviction and verify 0 residue.
	evictStart := time.Now()
	select {
	case <-evicted:
		t.Logf("IDLE_EVICTED after %d ms", time.Since(evictStart).Milliseconds())
	case <-time.After(15 * time.Second):
		t.Fatalf("warm worker did not auto-evict within 15s")
	}
	if osproc.Alive(warmPID) {
		t.Fatalf("warm worker PID %d still alive after idle eviction", warmPID)
	}
	if _, err := os.Stat(warmWorkerDir); !os.IsNotExist(err) {
		t.Fatalf("warm workerDir still exists after eviction: %s", warmWorkerDir)
	}

	// 4. Verify zero pollution of real ~/.gemini/antigravity-cli.
	afterReal := snapshotRealAgy(realAgyDir)
	var diffs []string
	for k, v := range afterReal {
		if beforeReal[k] != v {
			diffs = append(diffs, fmt.Sprintf("changed/added %s (%q -> %q)", k, beforeReal[k], v))
		}
	}
	for k := range beforeReal {
		if _, ok := afterReal[k]; !ok {
			diffs = append(diffs, "removed "+k)
		}
	}
	if len(diffs) > 0 {
		t.Fatalf("real ~/.gemini/antigravity-cli polluted: %v", diffs)
	}

	avgDur := func(ds []time.Duration) float64 {
		var sum float64
		for _, d := range ds {
			sum += float64(d.Milliseconds())
		}
		return sum / float64(len(ds))
	}
	t.Logf("SUMMARY: cold_avg=%.1fms (n=%d) | warm_steady_avg=%.1fms (n=%d) | real_agy_dir_diffs=0 | cold_usages=%+v",
		avgDur(coldLatencies), len(coldLatencies), avgDur(warmSteadyLatencies), len(warmSteadyLatencies), coldUsages)
}

func readProcTreeRSSKB(rootPID int) int64 {
	var total int64
	readOne := func(pid int) int64 {
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid))
		if err != nil {
			return 0
		}
		for line := range strings.SplitSeq(string(b), "\n") {
			if strings.HasPrefix(line, "VmRSS:") {
				fields := strings.Fields(line)
				if len(fields) >= 2 {
					if v, err := strconv.ParseInt(fields[1], 10, 64); err == nil {
						return v
					}
				}
			}
		}
		return 0
	}
	total += readOne(rootPID)
	// Include any direct/group child processes in the same PGID on Linux.
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return total
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil || pid == rootPID {
			continue
		}
		b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if err != nil {
			continue
		}
		s := string(b)
		rp := strings.LastIndexByte(s, ')')
		if rp < 0 || rp+2 >= len(s) {
			continue
		}
		fields := strings.Fields(s[rp+2:])
		// fields[0]=state, fields[1]=ppid, fields[2]=pgrp
		if len(fields) >= 3 {
			if pgrp, err := strconv.Atoi(fields[2]); err == nil && pgrp == rootPID {
				total += readOne(pid)
			}
		}
	}
	return total
}
