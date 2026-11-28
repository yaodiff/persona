package node

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/herdrcore"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestAgyPresenceConversation(t *testing.T) {
	lock := func(id string) string { return "/home/u/.gemini/antigravity-cli/presence/" + id + ".lock" }
	for _, tc := range []struct {
		name  string
		files []string
		want  string
	}{
		{"one", []string{"/dev/null", lock(agyTestID), "socket:[1]"}, agyTestID},
		{"same twice", []string{lock(agyTestID), lock(agyTestID)}, agyTestID},
		{"two conversations", []string{lock(agyTestID), lock(agyOtherTestID)}, ""},
		{"not a presence dir", []string{"/tmp/other/" + agyTestID + ".lock"}, ""},
		{"not a conversation", []string{lock("settings")}, ""},
		{"none", nil, ""},
	} {
		if got := agyPresenceConversation(tc.files); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestProcLocksHasDevIno(t *testing.T) {
	for _, tc := range []struct {
		name  string
		table string
		major uint32
		minor uint32
		ino   uint64
		want  bool
	}{
		{
			name:  "matching FLOCK ADVISORY WRITE",
			table: "1: FLOCK  ADVISORY  WRITE 12345 08:01:67890 0 EOF\n",
			major: 8, minor: 1, ino: 67890, want: true,
		},
		{
			name:  "matching hex major and minor lowercase",
			table: "8: FLOCK  ADVISORY  WRITE 9 00:2c:3445397 0 EOF\n",
			major: 0, minor: 0x2c, ino: 3445397, want: true,
		},
		{
			name:  "matching uppercase hex and large major/minor and 64-bit inode",
			table: "42: FLOCK  ADVISORY  WRITE 0 103:10A0B1:9007199254740993 0 EOF\n",
			major: 0x103, minor: 0x10a0b1, ino: 9007199254740993, want: true,
		},
		{
			name:  "matching MANDATORY FLOCK WRITE with tabs and CRLF",
			table: "7:\tFLOCK\tMANDATORY\tWRITE\t88\tfe:0f:123456\t0\tEOF\r\n",
			major: 0xfe, minor: 0x0f, ino: 123456, want: true,
		},
		{
			name: "match on later line after unrelated entries",
			table: "1: POSIX  ADVISORY  WRITE 100 08:01:67890 0 EOF\n" +
				"2: FLOCK  ADVISORY  READ  101 08:01:67890 0 EOF\n" +
				"3: -> FLOCK  ADVISORY  WRITE 102 08:01:67890 0 EOF\n" +
				"4: FLOCK  ADVISORY  WRITE 103 08:01:67890 0 EOF\n",
			major: 8, minor: 1, ino: 67890, want: true,
		},
		{
			name:  "waiter line with arrow does not hold lock",
			table: "8: -> FLOCK  ADVISORY  WRITE 12 00:2c:3445397 0 EOF\n",
			major: 0, minor: 0x2c, ino: 3445397, want: false,
		},
		{
			name:  "shared FLOCK READ does not match",
			table: "1: FLOCK  ADVISORY  READ  12345 08:01:67890 0 EOF\n",
			major: 8, minor: 1, ino: 67890, want: false,
		},
		{
			name:  "POSIX lock does not match",
			table: "1: POSIX  ADVISORY  WRITE 12345 08:01:67890 0 EOF\n",
			major: 8, minor: 1, ino: 67890, want: false,
		},
		{
			name:  "OFDLCK does not match",
			table: "1: OFDLCK ADVISORY  WRITE -1 08:01:67890 0 EOF\n",
			major: 8, minor: 1, ino: 67890, want: false,
		},
		{
			name:  "LEASE does not match",
			table: "1: LEASE  ACTIVE    WRITE 12345 08:01:67890 0 EOF\n",
			major: 8, minor: 1, ino: 67890, want: false,
		},
		{
			name:  "wrong major",
			table: "1: FLOCK  ADVISORY  WRITE 12345 09:01:67890 0 EOF\n",
			major: 8, minor: 1, ino: 67890, want: false,
		},
		{
			name:  "wrong minor",
			table: "1: FLOCK  ADVISORY  WRITE 12345 08:02:67890 0 EOF\n",
			major: 8, minor: 1, ino: 67890, want: false,
		},
		{
			name:  "wrong inode",
			table: "1: FLOCK  ADVISORY  WRITE 12345 08:01:67891 0 EOF\n",
			major: 8, minor: 1, ino: 67890, want: false,
		},
		{
			name:  "empty table",
			table: "",
			major: 8, minor: 1, ino: 67890, want: false,
		},
		{
			name: "malformed and truncated lines ignored",
			table: "\n" +
				"garbage line\n" +
				": FLOCK ADVISORY WRITE 1 08:01:67890 0 EOF\n" +
				"abc: FLOCK ADVISORY WRITE 1 08:01:67890 0 EOF\n" +
				"1: FLOCK ADVISORY WRITE notpid 08:01:67890 0 EOF\n" +
				"1: FLOCK ADVISORY WRITE 1 08:01:67890\n" +
				"1: FLOCK ADVISORY WRITE 1 08:01:67890 0\n" +
				"1: FLOCK ADVISORY WRITE 1 08:01 0 EOF\n" +
				"1: FLOCK ADVISORY WRITE 1 zz:01:67890 0 EOF\n" +
				"1: FLOCK ADVISORY WRITE 1 08:01:notino 0 EOF\n",
			major: 8, minor: 1, ino: 67890, want: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := procLocksHasDevIno([]byte(tc.table), tc.major, tc.minor, tc.ino); got != tc.want {
				t.Fatalf("procLocksHasDevIno(%q, %d, %d, %d) = %v, want %v", tc.table, tc.major, tc.minor, tc.ino, got, tc.want)
			}
		})
	}
}

func writeAgyPresenceFile(t *testing.T, dir, id string) string {
	t.Helper()
	p := filepath.Join(dir, "presence", id+".lock")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func writeAgyTranscriptAt(t *testing.T, dir, id, rel string, mtime time.Time) string {
	t.Helper()
	p := filepath.Join(dir, "brain", id, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"step_index":0,"type":"USER_INPUT","content":"hi"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, mtime, mtime); err != nil {
		t.Fatal(err)
	}
	return p
}

func withAgyPresenceHooks(t *testing.T, probe func(string) agyLockState, clock func() time.Time) {
	t.Helper()
	oldProbe, oldNow := agyPresenceLockProbe, agyPresenceNow
	if probe != nil {
		agyPresenceLockProbe = probe
	}
	if clock != nil {
		agyPresenceNow = clock
	}
	t.Cleanup(func() {
		agyPresenceLockProbe = oldProbe
		agyPresenceNow = oldNow
	})
}

func TestAgyPresenceHeldFallback(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)

	state := agyLockMissing
	withAgyPresenceHooks(t, func(string) agyLockState { return state }, func() time.Time { return now })

	// No presence file: never held even when the transcript was just written.
	writeAgyTranscriptAt(t, dir, agyTestID, agyTranscriptPath, now.Add(-5*time.Second))
	if agyPresenceHeld(dir, agyTestID) {
		t.Fatal("missing presence file read as held")
	}

	// Lock table explicitly says held: held even with no transcript.
	state = agyLockHeld
	if !agyPresenceHeld(dir, agyOtherTestID) {
		t.Fatal("lock-table held state read as free")
	}

	// Lock table explicitly says free: NOT held even when transcript was just written.
	state = agyLockFree
	if agyPresenceHeld(dir, agyTestID) {
		t.Fatal("lock-table free state fell back to transcript mtime")
	}

	// Unknown lock state (non-Linux or unreadable /proc/locks): falls back to recent transcript writes.
	state = agyLockUnknown
	if agyPresenceHeld(dir, agyOtherTestID) {
		t.Fatal("presence file without transcript read as held")
	}
	if !agyPresenceHeld(dir, agyTestID) {
		t.Fatal("presence file with recent transcript.jsonl write read as free")
	}

	// Exact active-window boundary (60s): quiet again.
	writeAgyTranscriptAt(t, dir, agyTestID, agyTranscriptPath, now.Add(-agyPresenceActiveWindow))
	if agyPresenceHeld(dir, agyTestID) {
		t.Fatal("stale transcript at active-window boundary read as held")
	}

	// Recent transcript_full.jsonl takes precedence over an older transcript.jsonl.
	writeAgyTranscriptAt(t, dir, agyTestID, agyFullTranscriptPath, now.Add(-10*time.Second))
	if !agyPresenceHeld(dir, agyTestID) {
		t.Fatal("presence file with recent transcript_full.jsonl write read as free")
	}

	// Both stale: not held.
	writeAgyTranscriptAt(t, dir, agyTestID, agyFullTranscriptPath, now.Add(-2*time.Minute))
	if agyPresenceHeld(dir, agyTestID) {
		t.Fatal("stale transcripts read as held")
	}
}

// A herdr-core terminal running agy is an agy pane whose conversation is the presence lock its agy holds (#13603).
func TestHerdrCoreAgyTerminal(t *testing.T) {
	old := agyPresenceOf
	agyPresenceOf = func(pid int) string {
		if pid == 42 {
			return agyTestID
		}
		return ""
	}
	t.Cleanup(func() { agyPresenceOf = old })

	byStatus := herdrcore.Terminal{Address: "t51", Cwd: "/w", Status: &herdrcore.Status{App: "antigravity", State: "done"},
		Foreground: &herdrcore.Foreground{Pid: 42, Name: "agy", Argv: []string{"agy"}}}
	a := fromTerminal(byStatus)
	if a.Agent != agyAgent || a.AgentStatus != proto.StatusDone || reportedAgyConversation(a) != agyTestID {
		t.Fatalf("status antigravity: %+v session %+v", a, a.AgentSession)
	}
	byProgram := herdrcore.Terminal{Address: "t52", Cwd: "/w", Foreground: &herdrcore.Foreground{Pid: 7, Argv: []string{"/usr/local/bin/agy"}}}
	if a := fromTerminal(byProgram); a.Agent != agyAgent || a.AgentSession != nil {
		t.Fatalf("agy before its first status, no presence lock: %+v", a)
	}
	shell := herdrcore.Terminal{Address: "t53", Cwd: "/w", Status: &herdrcore.Status{App: "antigravity", State: "done"},
		Foreground: &herdrcore.Foreground{Pid: 42, Name: "zsh", Argv: []string{"-zsh"}}}
	if a := fromTerminal(shell); a.Agent != "" {
		t.Fatalf("agy exited to its shell: %+v", a)
	}

	n, _, _ := herdrNode(t)
	id := otherSession(a)
	n.mu.Lock()
	n.agents[id] = a
	if n.hostAgents == nil {
		n.hostAgents = map[string][]host.Agent{}
	}
	n.hostAgents[host.HerdrCore] = []host.Agent{a}
	n.mu.Unlock()
	if got := n.agyConversation(id, a.PaneID); got != agyTestID {
		t.Fatalf("herdr-core pane conversation %q, want %q", got, agyTestID)
	}
}

// A message or interrupt for the companion's agy-<conversation> goes to the pane whose agy has that conversation open,
// never to the companion, which would run a second agy on it (#13603).
func TestAgyCompanionRowCallsGoToItsPane(t *testing.T) {
	n, f, home := herdrNode(t)
	n.runtimes = newRuntimeRegistry()
	companion := n.runtimes.add(nil) // a call that reached it would fail: it has no peer
	companion.ready, companion.hello.Runtime = true, agyAgent
	acpRow := "agy-" + agyTestID
	n.runtimes.setSessions(companion, []proto.Session{{SessionID: acpRow, Agent: agyAgent, Cwd: home, Status: proto.StatusIdle}})
	seedAgents(t, n, f, agyTestAgent(agyTestPane, home, agyTestID))
	stubAgyNativePrompt(t, n, f, agyTestPane, agyTestID)

	agyTestCall(t, n, "session.send", acpRow, map[string]any{"text": "给 ppe_dev 补 LF"})
	agyTestCall(t, n, "session.interrupt", acpRow, nil)
	assertTrace(t, f.trace("agent.prompt", "pane.send_keys", "pane.send_input"), []string{
		call("agent.prompt", map[string]any{"target": agyTestPane, "text": "给 ppe_dev 补 LF"}),
		sendKeys(agyTestPane, "escape"),
	})
	if _, err := request(t, n, "session.fork", map[string]any{"session_id": acpRow}); refusalCode(err) != codeAgyInTerminal {
		t.Fatalf("fork of a conversation open in a pane: %v", err)
	}
	if pane, _, held := n.agyOpenCall("session.items", acpRow); held || pane != "" {
		t.Fatal("a read of the companion's row was taken from the companion")
	}
}

// With no pane the node knows, a held presence lock (or recent external transcript write when no lock table can be read)
// keeps the companion out of the conversation; a lock left behind by an exited agy does not.
func TestAgyCompanionRowRefusedWhileATerminalHoldsIt(t *testing.T) {
	t.Run("lock table held then free", func(t *testing.T) {
		n, _, _ := herdrNode(t)
		dir := t.TempDir()
		t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", dir)
		acpRow := "agy-" + agyTestID
		targetLock := writeAgyPresenceFile(t, dir, agyTestID)
		held := true
		withAgyPresenceHooks(t, func(path string) agyLockState {
			if path != targetLock {
				return agyLockMissing
			}
			if held {
				return agyLockHeld
			}
			return agyLockFree
		}, nil)

		for _, method := range []string{"session.send", proto.MethodSessionSendUnseen, "session.interrupt", "session.rewind"} {
			_, err := request(t, n, method, map[string]any{"session_id": acpRow, "text": "x", "client_msg_id": "c1"})
			if refusalCode(err) != codeAgyInTerminal {
				t.Errorf("%s while a terminal holds the conversation: %v", method, err)
			}
		}
		if _, _, isHeld := n.agyOpenCall("session.items", acpRow); isHeld {
			t.Error("a read was refused")
		}
		if _, _, isHeld := n.agyOpenCall("session.send", "agy-"+agyOtherTestID); isHeld {
			t.Error("another conversation was refused")
		}
		held = false
		if _, _, isHeld := n.agyOpenCall("session.send", acpRow); isHeld {
			t.Error("refused after the terminal's agy exited")
		}
	})

	t.Run("fallback presence file plus recent transcript write", func(t *testing.T) {
		n, _, _ := herdrNode(t)
		dir := t.TempDir()
		t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", dir)
		acpRow := "agy-" + agyTestID
		now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
		writeAgyPresenceFile(t, dir, agyTestID)
		writeAgyTranscriptAt(t, dir, agyTestID, agyFullTranscriptPath, now.Add(-5*time.Second))
		withAgyPresenceHooks(t, func(path string) agyLockState {
			if _, err := os.Stat(path); err != nil {
				return agyLockMissing
			}
			return agyLockUnknown
		}, func() time.Time { return now })

		for _, method := range []string{"session.send", proto.MethodSessionSendUnseen, "session.interrupt", "session.rewind"} {
			_, err := request(t, n, method, map[string]any{"session_id": acpRow, "text": "x", "client_msg_id": "c1"})
			if refusalCode(err) != codeAgyInTerminal {
				t.Errorf("%s while a terminal writes the transcript: %v", method, err)
			}
		}
		if _, _, isHeld := n.agyOpenCall("session.items", acpRow); isHeld {
			t.Error("a read was refused")
		}
		if _, _, isHeld := n.agyOpenCall("session.send", "agy-"+agyOtherTestID); isHeld {
			t.Error("another conversation was refused")
		}
		now = now.Add(2 * time.Minute)
		if _, _, isHeld := n.agyOpenCall("session.send", acpRow); isHeld {
			t.Error("refused after the transcript went quiet")
		}
	})
}

// Where no lock table can be read, the companion's own turns and settling transcript exports must not be mistaken for
// another process writing the transcript, while writes by an external terminal agy after the settle window are refused.
func TestAgyPresenceFallbackAllowsCompanionOwnWork(t *testing.T) {
	n, _, home := herdrNode(t)
	dir := t.TempDir()
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", dir)
	acpRow := "agy-" + agyTestID

	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	var nowMu sync.Mutex
	getNow := func() time.Time {
		nowMu.Lock()
		defer nowMu.Unlock()
		return now
	}
	setNow := func(ts time.Time) {
		nowMu.Lock()
		now = ts
		nowMu.Unlock()
	}

	writeAgyPresenceFile(t, dir, agyTestID)
	writeAgyTranscriptAt(t, dir, agyTestID, agyTranscriptPath, getNow().Add(-5*time.Minute))
	withAgyPresenceHooks(t, func(path string) agyLockState {
		if _, err := os.Stat(path); err != nil {
			return agyLockMissing
		}
		return agyLockUnknown
	}, getNow)

	// Connect an in-memory fake Antigravity companion over net.Pipe.
	nodeSide, compSide := net.Pipe()
	t.Cleanup(func() { _ = nodeSide.Close(); _ = compSide.Close() })
	var (
		mu              sync.Mutex
		methods         []string
		failNext        error
		onSendInFlight  func()
		interruptResult = map[string]any{"ok": true, "settled": true}
	)
	compPeer := rpc.New("c", compSide)
	compPeer.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
		mu.Lock()
		methods = append(methods, method)
		err := failNext
		failNext = nil
		hook := onSendInFlight
		onSendInFlight = nil
		intRes := interruptResult
		mu.Unlock()
		if method == "session.send" && hook != nil {
			hook()
		}
		if err != nil {
			return nil, err
		}
		if method == "session.interrupt" {
			return intRes, nil
		}
		return map[string]any{"ok": true}, nil
	}
	nodePeer := rpc.New("n", nodeSide)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = compPeer.Serve(ctx, compSide) }()
	go func() { _ = nodePeer.Serve(ctx, nodeSide) }()

	n.runtimes = newRuntimeRegistry()
	companion := n.runtimes.add(nodePeer)
	companion.ready = true
	companion.hello = runtimeHello{
		Runtime: agyAgent,
		Agent:   agyAgent,
		Name:    "Antigravity",
	}
	companion.hello.Caps.Send = true
	companion.hello.Caps.Interrupt = true
	companion.hello.Caps.Rewind = true
	companion.hello.Caps.Fork = true
	n.runtimes.setSessions(companion, []proto.Session{{SessionID: acpRow, Agent: agyAgent, Cwd: home, Status: proto.StatusIdle}})

	// 1. A failed companion call must not mask a subsequent external write.
	mu.Lock()
	failNext = refusal("agent_busy", "busy", nil)
	mu.Unlock()
	if _, err := request(t, n, "session.send", map[string]any{"session_id": acpRow, "text": "fail", "client_msg_id": "m0"}); refusalCode(err) != "agent_busy" {
		t.Fatalf("expected agent_busy from failed call, got %v", err)
	}
	writeAgyTranscriptAt(t, dir, agyTestID, agyTranscriptPath, getNow().Add(time.Second))
	setNow(getNow().Add(2 * time.Second))
	if _, err := request(t, n, "session.send", map[string]any{"session_id": acpRow, "text": "blocked", "client_msg_id": "m0b"}); refusalCode(err) != codeAgyInTerminal {
		t.Fatalf("external write after failed call should be refused, got %v", err)
	}

	// 2. Real companion timing: StatusWorking arrives WHILE session.send is still in flight (rt.sessionsChanged(0) before
	// readyResolve). Even >15s later with no deltas, the turn must stay recognized as the companion's own.
	setNow(getNow().Add(2 * time.Minute))
	mu.Lock()
	onSendInFlight = func() {
		n.runtimes.setSessions(companion, []proto.Session{{SessionID: acpRow, Agent: agyAgent, Cwd: home, Status: proto.StatusWorking}})
	}
	mu.Unlock()
	if _, err := request(t, n, "session.send", map[string]any{"session_id": acpRow, "text": "Turn 1", "client_msg_id": "m1"}); err != nil {
		t.Fatalf("Turn 1 send failed: %v", err)
	}

	// 30s into Turn 1 (no deltas, silent tool call writing transcript): queued follow-up send must NOT be refused and
	// must NOT wipe sawWorking.
	setNow(getNow().Add(30 * time.Second))
	writeAgyTranscriptAt(t, dir, agyTestID, agyTranscriptPath, getNow())
	if _, err := request(t, n, "session.send", map[string]any{"session_id": acpRow, "text": "Queue", "client_msg_id": "m1q"}); err != nil {
		t.Fatalf("follow-up send during companion turn refused: %v", err)
	}

	// 20s after the queued send (still no extra StatusWorking push): companion is still working, so delta/interrupt works.
	setNow(getNow().Add(20 * time.Second))
	n.runtimeEvent(companion, "session.delta", json.RawMessage(`{"session_id":"`+acpRow+`","text":"thinking"}`))
	writeAgyTranscriptAt(t, dir, agyTestID, agyTranscriptPath, getNow())

	// 3. Turn 1 finishes without needing any synthetic StatusWorking push; companion exports reply 3s later (inside settle window).
	setNow(getNow().Add(10 * time.Second))
	n.runtimes.setSessions(companion, []proto.Session{{SessionID: acpRow, Agent: agyAgent, Cwd: home, Status: proto.StatusDone}})
	writeAgyTranscriptAt(t, dir, agyTestID, agyFullTranscriptPath, getNow().Add(3*time.Second))
	setNow(getNow().Add(5 * time.Second))

	// Immediate Turn 2 while companion's own export is fresh must go through.
	if _, err := request(t, n, "session.send", map[string]any{"session_id": acpRow, "text": "Turn 2", "client_msg_id": "m2"}); err != nil {
		t.Fatalf("Turn 2 send after companion export refused: %v", err)
	}

	// 4. An unsettled session.interrupt (no settled: true) leaves the turn active while it unwinds >15s; a retry
	// interrupt must still reach the companion.
	n.runtimes.setSessions(companion, []proto.Session{{SessionID: acpRow, Agent: agyAgent, Cwd: home, Status: proto.StatusWorking}})
	mu.Lock()
	interruptResult = map[string]any{"ok": true}
	mu.Unlock()
	if _, err := request(t, n, "session.interrupt", map[string]any{"session_id": acpRow}); err != nil {
		t.Fatalf("unsettled interrupt refused: %v", err)
	}
	setNow(getNow().Add(agyExportSettleWindow + 5*time.Second))
	writeAgyTranscriptAt(t, dir, agyTestID, agyFullTranscriptPath, getNow())
	mu.Lock()
	interruptResult = map[string]any{"ok": true, "settled": true}
	mu.Unlock()
	if _, err := request(t, n, "session.interrupt", map[string]any{"session_id": acpRow}); err != nil {
		t.Fatalf("retry interrupt while unsettled turn unwinds refused: %v", err)
	}

	// 5. After settled interrupt + 15s export settle window, an external terminal agy write IS refused.
	setNow(getNow().Add(agyExportSettleWindow + 2*time.Second))
	writeAgyTranscriptAt(t, dir, agyTestID, agyFullTranscriptPath, getNow())
	setNow(getNow().Add(time.Second))
	if _, err := request(t, n, "session.send", map[string]any{"session_id": acpRow, "text": "External", "client_msg_id": "m3"}); refusalCode(err) != codeAgyInTerminal {
		t.Fatalf("external CLI write after settle window should be refused, got %v", err)
	}

	// 6. Fast turn (StatusDone without ever seeing StatusWorking): after 15s, an external terminal's StatusWorking push
	// must NOT resurrect the stale companion entry and mask external writes.
	setNow(getNow().Add(2 * time.Minute))
	n.runtimes.setSessions(companion, []proto.Session{{SessionID: acpRow, Agent: agyAgent, Cwd: home, Status: proto.StatusDone}})
	if _, err := request(t, n, "session.send", map[string]any{"session_id": acpRow, "text": "Fast", "client_msg_id": "m4"}); err != nil {
		t.Fatalf("fast turn send failed: %v", err)
	}
	n.runtimes.setSessions(companion, []proto.Session{{SessionID: acpRow, Agent: agyAgent, Cwd: home, Status: proto.StatusDone}})
	setNow(getNow().Add(2 * time.Minute))
	// External terminal starts working; companion notices nativeWorking and pushes StatusWorking.
	n.runtimes.setSessions(companion, []proto.Session{{SessionID: acpRow, Agent: agyAgent, Cwd: home, Status: proto.StatusWorking}})
	writeAgyTranscriptAt(t, dir, agyTestID, agyFullTranscriptPath, getNow())
	if _, err := request(t, n, "session.send", map[string]any{"session_id": acpRow, "text": "External2", "client_msg_id": "m4b"}); refusalCode(err) != codeAgyInTerminal {
		t.Fatalf("external terminal StatusWorking after expired fast turn should be refused, got %v", err)
	}

	// 7. An older finishing settled interrupt must not clear a newer session.send's active turn.
	setNow(getNow().Add(2 * time.Minute))
	n.runtimes.setSessions(companion, []proto.Session{{SessionID: acpRow, Agent: agyAgent, Cwd: home, Status: proto.StatusIdle}})
	oldInterrupt := n.runtimes.agyWorkBegin(companion, "session.interrupt", acpRow)
	newSend := n.runtimes.agyWorkBegin(companion, "session.send", acpRow)
	n.runtimes.setSessions(companion, []proto.Session{{SessionID: acpRow, Agent: agyAgent, Cwd: home, Status: proto.StatusWorking}})
	newSend(nil, false)
	oldInterrupt(nil, true)
	setNow(getNow().Add(agyExportSettleWindow + 5*time.Second))
	writeAgyTranscriptAt(t, dir, agyTestID, agyFullTranscriptPath, getNow())
	if _, _, held := n.agyOpenCall("session.send", acpRow); held {
		t.Fatal("stale settled interrupt cleared a newer active companion turn")
	}

	// 8. Companion disconnect while a turn is active clears active state (never stuck active).
	n.runtimes.remove(companion)
	setNow(getNow().Add(agyExportSettleWindow + 2*time.Second))
	writeAgyTranscriptAt(t, dir, agyTestID, agyFullTranscriptPath, getNow())
	if _, _, held := n.agyOpenCall("session.send", acpRow); !held {
		t.Fatal("external write after companion disconnect was not refused")
	}

	mu.Lock()
	gotMethods := append([]string(nil), methods...)
	mu.Unlock()
	if len(gotMethods) != 7 {
		t.Fatalf("unexpected companion calls (%d): %v", len(gotMethods), gotMethods)
	}
}

// A classic herdr agy pane without herdr:antigravity_cli (or whose agent_session was replaced by custom:persona's
// resume report) resolves its conversation from its foreground process's open presence/<uuid>.lock on Linux, so the
// pane merges with the companion row instead of listing a duplicate placeholder beside agy-<conversation>.
func TestHerdrAgyPanePresenceMergesCompanionRow(t *testing.T) {
	n, f, home := herdrNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	acpRow := "agy-" + agyTestID
	n.runtimes = newRuntimeRegistry()
	companion := n.runtimes.add(nil)
	companion.ready, companion.hello.Runtime = true, agyAgent
	n.runtimes.setSessions(companion, []proto.Session{
		{SessionID: acpRow, Agent: agyAgent, Cwd: home, Status: proto.StatusDone, Title: "Real agy task", LastText: "All fixed."},
	})
	agyTestFile(t, n.agyDataRoot(), agyTestID, `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","content":"Fix the auth bug"}
{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","content":"All fixed."}
`)

	oldPresence := agyPresenceOf
	presencePIDs := map[int]string{4242: agyTestID}
	agyPresenceOf = func(pid int) string { return presencePIDs[pid] }
	t.Cleanup(func() { agyPresenceOf = oldPresence })

	f.reply("pane.process_info", map[string]any{
		"process_info": map[string]any{
			"foreground_processes": []any{map[string]any{"pid": 4242, "name": "agy", "argv0": "agy"}},
		},
	})

	// Case 1: classic herdr pane with no agent_session at all (herdr:antigravity_cli missing).
	paneAgent := agyTestAgent(agyTestPane, home, "")
	f.reply("session.snapshot", snapshot(nil, nil, []any{paneAgent}, nil))
	list := listSessions(t, n)
	var agyRows []proto.Session
	for _, s := range list {
		if s.Agent == agyAgent {
			agyRows = append(agyRows, s)
		}
	}
	if len(agyRows) != 1 || agyRows[0].SessionID != agyTestRow || agyRows[0].Title != "Real agy task" || agyRows[0].LastText != "All fixed." {
		t.Fatalf("without herdr integration: got %d agy rows %+v, want single merged row %s", len(agyRows), agyRows, agyTestRow)
	}
	items, _ := agyTestItems(t, n, agyTestRow, nil)
	if len(items) != 2 || items[1].Text != "All fixed." {
		t.Fatalf("items from presence-bound herdr pane = %+v, want transcript reply", items)
	}

	// Case 2: herdr pane whose agent_session came from custom:persona (resume_argv report) still merges via presence lock.
	paneAgent["agent_session"] = map[string]any{"kind": "id", "value": agyTestRow, "source": resumeSource, "agent": agyAgent}
	f.reply("session.snapshot", snapshot(nil, nil, []any{paneAgent}, nil))
	list = listSessions(t, n)
	agyRows = nil
	for _, s := range list {
		if s.Agent == agyAgent {
			agyRows = append(agyRows, s)
		}
	}
	if len(agyRows) != 1 || agyRows[0].SessionID != agyTestRow {
		t.Fatalf("with custom:persona agent_session: got %d agy rows %+v, want single merged row %s", len(agyRows), agyRows, agyTestRow)
	}

	// Case 3: ambiguous foreground processes holding two different presence locks fail closed (do not guess).
	presencePIDs[4243] = agyOtherTestID
	f.reply("pane.process_info", map[string]any{
		"process_info": map[string]any{
			"foreground_processes": []any{
				map[string]any{"pid": 4242, "name": "agy", "argv0": "agy"},
				map[string]any{"pid": 4243, "name": "agy", "argv0": "agy"},
			},
		},
	})
	f.reply("session.snapshot", snapshot(nil, nil, []any{agyTestAgent(agyTestPane, home, "")}, nil))
	_ = n.refreshHosts(t.Context())
	if got := n.agyConversation(agyTestRow, agyTestPane); got != "" {
		t.Fatalf("ambiguous presence locks bound to %q, want empty", got)
	}
}

// An agy in an Orca terminal resolves its conversation from the presence/<uuid>.lock its foreground process holds open
// (Linux), as a classic herdr pane's does, so the pane merges with the companion row before any hook fired or after a
// node restart, and a call that drives the companion's row finds the pane instead of a lock with no pane (#16531).
func TestOrcaAgyPanePresenceMergesCompanionRow(t *testing.T) {
	n, _, o, procs, home := orcaNode(t)
	t.Setenv("ANTIGRAVITY_CLI_CONFIG_DIR", "")
	const pane = "tab1:leaf1"
	paneID := "orca:" + pane
	acpRow := "agy-" + agyTestID
	n.runtimes = newRuntimeRegistry()
	companion := n.runtimes.add(nil)
	companion.ready, companion.hello.Runtime = true, agyAgent
	n.runtimes.setSessions(companion, []proto.Session{
		{SessionID: acpRow, Agent: agyAgent, Cwd: home, Status: proto.StatusDone, Title: "Real agy task", LastText: "All fixed."},
	})
	agyTestFile(t, n.agyDataRoot(), agyTestID, `{"step_index":0,"type":"USER_INPUT","source":"USER_EXPLICIT","content":"Fix the auth bug"}
{"step_index":1,"type":"PLANNER_RESPONSE","status":"DONE","content":"All fixed."}
`)
	oldPresence := agyPresenceOf
	presencePIDs := map[int]string{4242: agyTestID}
	agyPresenceOf = func(pid int) string { return presencePIDs[pid] }
	t.Cleanup(func() { agyPresenceOf = oldPresence })

	o.SetTerminals(orcaTerm("term_1", "tab1", "leaf1", ""))
	setProcesses := func(pids ...int) {
		args, env := "", ""
		for _, pid := range pids {
			args += fmt.Sprintf("%d S+ 01:00 agy\n", pid)
			env += fmt.Sprintf("%d agy ORCA_PANE_KEY=%s\n", pid, pane)
		}
		procs.set(pane, parseOrcaPSEnv(env, parseOrcaPSArgs(args, time.Now()), nil)[pane]...)
	}
	setProcesses(4242)
	paneRow := otherSession(host.Agent{Agent: agyAgent, PaneID: paneID})
	agyRows := func() []proto.Session {
		var rows []proto.Session
		for _, s := range listSessions(t, n) {
			if s.Agent == agyAgent {
				rows = append(rows, s)
			}
		}
		return rows
	}
	if rows := agyRows(); len(rows) != 1 || rows[0].SessionID != paneRow || rows[0].Title != "Real agy task" ||
		rows[0].LastText != "All fixed." {
		var got []string
		for _, s := range rows {
			got = append(got, s.SessionID+" / "+s.Title+" / "+s.LastText)
		}
		t.Fatalf("got agy rows %q, want the Orca pane's %s alone with the companion's title and reply", got, paneRow)
	}
	if id, a, held := n.agyOpenCall("session.send", acpRow); !held || id != paneRow || a.PaneID != paneID {
		t.Fatalf("a send to %s goes to %q (pane %q, held %v), want the Orca pane's row", acpRow, id, a.PaneID, held)
	}

	// Two foreground agy processes holding different presence locks fail closed: no conversation is guessed.
	presencePIDs[4243] = agyOtherTestID
	setProcesses(4242, 4243)
	staleScan(n.hosts.Get(host.Orca).(*orcaHost))
	_ = n.refreshHosts(t.Context())
	if got := n.agyConversation(paneRow, paneID); got != "" {
		t.Fatalf("ambiguous presence locks bound to %q, want empty", got)
	}
}
