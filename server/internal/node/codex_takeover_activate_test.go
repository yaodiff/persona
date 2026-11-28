package node

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

type fakeTakeoverLauncherHost struct {
	kind        string
	readText    string
	readBySrc   map[string]string
	readErr     error
	readSources []string
	onRead      func(host.ReadOpts)
	pids        []int
	pidsErr     error
	keysSent    [][]string
	keysErr     error
	paneEnv     string
	workspace   string
}

func (h *fakeTakeoverLauncherHost) Kind() string                   { return h.kind }
func (h *fakeTakeoverLauncherHost) PaneEnv() string                { return h.paneEnv }
func (h *fakeTakeoverLauncherHost) Version(context.Context) string { return "1.0" }
func (h *fakeTakeoverLauncherHost) Snapshot(context.Context) (*host.Layout, error) {
	return &host.Layout{}, nil
}
func (h *fakeTakeoverLauncherHost) Agents(context.Context) ([]host.Agent, error) { return nil, nil }
func (h *fakeTakeoverLauncherHost) Agent(context.Context, host.PaneRef) (*host.Agent, error) {
	return nil, host.ErrNoAgent
}
func (h *fakeTakeoverLauncherHost) Read(_ context.Context, _ host.PaneRef, opts host.ReadOpts) (string, error) {
	h.readSources = append(h.readSources, opts.Source)
	if h.onRead != nil {
		h.onRead(opts)
	}
	if h.readBySrc != nil {
		if text, ok := h.readBySrc[opts.Source]; ok {
			return text, h.readErr
		}
	}
	return h.readText, h.readErr
}
func (h *fakeTakeoverLauncherHost) SendText(context.Context, host.PaneRef, string) error { return nil }
func (h *fakeTakeoverLauncherHost) SendKeys(_ context.Context, _ host.PaneRef, keys ...string) error {
	h.keysSent = append(h.keysSent, slices.Clone(keys))
	return h.keysErr
}
func (h *fakeTakeoverLauncherHost) Prompt(context.Context, host.PaneRef, string) error { return nil }
func (h *fakeTakeoverLauncherHost) PaneProcesses(context.Context, host.PaneRef) ([]int, error) {
	return slices.Clone(h.pids), h.pidsErr
}
func (h *fakeTakeoverLauncherHost) Spaces(context.Context) ([]host.Space, error) {
	return nil, nil
}
func (h *fakeTakeoverLauncherHost) CreateSpace(context.Context, string, string) (*host.Created, error) {
	return &host.Created{SpaceID: "s1", SlotID: "t1", Pane: host.PaneRef{Kind: h.kind, ID: "p1"}}, nil
}
func (h *fakeTakeoverLauncherHost) CreateSlot(context.Context, string, string, string) (*host.Created, error) {
	return &host.Created{SpaceID: "s1", SlotID: "t1", Pane: host.PaneRef{Kind: h.kind, ID: "p1"}}, nil
}
func (h *fakeTakeoverLauncherHost) Run(context.Context, host.PaneRef, string) error { return nil }
func (h *fakeTakeoverLauncherHost) NameAgent(context.Context, host.PaneRef, string) error {
	return nil
}

var (
	_ host.Host     = (*fakeTakeoverLauncherHost)(nil)
	_ host.Launcher = (*fakeTakeoverLauncherHost)(nil)
)

func writeCodexTestRollout(t *testing.T, home, thread, cwd, originator string, source any, at time.Time, extra ...string) string {
	t.Helper()
	dir := filepath.Join(home, "sessions", at.UTC().Format("2006"), at.UTC().Format("01"), at.UTC().Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"id": thread, "cwd": cwd}
	if originator != "" {
		payload["originator"] = originator
	}
	if source != nil {
		payload["source"] = source
	}
	meta, _ := json.Marshal(map[string]any{"timestamp": at.UTC().Format(time.RFC3339Nano), "type": "session_meta", "payload": payload})
	lines := append([]string{string(meta)}, extra...)
	path := filepath.Join(dir, "rollout-2026-11-23T10-00-00-"+thread+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, at, at); err != nil {
		t.Fatal(err)
	}
	return path
}

func codexEventAt(kind string, at time.Time) string {
	b, _ := json.Marshal(map[string]any{
		"timestamp": at.UTC().Format(time.RFC3339Nano),
		"type":      "event_msg",
		"payload":   map[string]any{"type": kind},
	})
	return string(b)
}

func requireCodexSession(t *testing.T, list []proto.Session, id string) proto.Session {
	t.Helper()
	s, ok := byID(list)[id]
	if !ok {
		t.Fatalf("missing session %s", id)
	}
	if s.Capabilities == nil {
		t.Fatalf("session %s has nil Capabilities: %+v", id, s)
	}
	return s
}

func TestCodexHeldOpenTurnDegradesWhenStalled(t *testing.T) {
	cases := []struct {
		name       string
		age        time.Duration
		endpoint   string
		holder     string
		touchNow   bool
		wantStatus string
		wantReason string
	}{
		{
			name:       "cli recent open turn stays working",
			age:        2 * time.Minute,
			endpoint:   "",
			holder:     codexHolderCLI,
			wantStatus: proto.StatusWorking,
			wantReason: codexHolderCLI + " 正在运行，但未提供共享入口；只能同步本地历史",
		},
		{
			name:       "cli stalled 19h46m degrades to unknown with stalled reason",
			age:        19*time.Hour + 46*time.Minute,
			endpoint:   "",
			holder:     codexHolderCLI,
			wantStatus: proto.StatusUnknown,
			wantReason: i18n.T(i18n.Default, "codex-history.stalled_turn", i18n.Args{"holder": codexHolderCLI}),
		},
		{
			name:       "stdio app-server stalled past threshold degrades to unknown",
			age:        codexHeldWorkingStale + 2*time.Minute,
			endpoint:   "stdio://",
			holder:     codexHolderAppServer,
			wantStatus: proto.StatusUnknown,
			wantReason: i18n.T(i18n.Default, "codex-history.stalled_turn", i18n.Args{"holder": codexHolderAppServer}),
		},
		{
			name:       "old task_started with recent mtime stays working",
			age:        3 * time.Hour,
			endpoint:   "",
			holder:     codexHolderCLI,
			touchNow:   true,
			wantStatus: proto.StatusWorking,
			wantReason: codexHolderCLI + " 正在运行，但未提供共享入口；只能同步本地历史",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n := remoteTestNode(t)
			home := t.TempDir()
			cwd := t.TempDir()
			at := time.Now().Add(-tc.age)
			path := writeCodexTestRollout(t, home, remoteRaw, cwd, "codex-tui", "cli", at, codexEventAt("task_started", at))
			if tc.touchNow {
				now := time.Now()
				if err := os.Chtimes(path, now, now); err != nil {
					t.Fatal(err)
				}
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			n.codices.scan = func() []codexProcess {
				return []codexProcess{{pid: os.Getpid(), home: home, endpoint: tc.endpoint}}
			}
			n.discoverCodex()
			id := n.registerCodex(home, remoteRaw, path)
			s := requireCodexSession(t, n.codexSessions(map[string]bool{}), id)
			if s.Status != tc.wantStatus || s.ReadOnlyReason != tc.wantReason || s.Capabilities.Send {
				t.Fatalf("status=%q reason=%q send=%v, want status=%q reason=%q",
					s.Status, s.ReadOnlyReason, s.Capabilities.Send, tc.wantStatus, tc.wantReason)
			}
		})
	}
}

func TestCodexHeldSharedAppServerSurfacesConnectionReason(t *testing.T) {
	t.Run("unreachable ws endpoint", func(t *testing.T) {
		n := remoteTestNode(t)
		home := t.TempDir()
		cwd := t.TempDir()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close()
		endpoint := "ws://" + addr

		at := time.Now().Add(-time.Minute)
		path := writeCodexTestRollout(t, home, remoteRaw, cwd, "codex-tui", "cli", at, codexEventAt("task_started", at), codexEventAt("task_complete", at))
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		n.codices.scan = func() []codexProcess {
			return []codexProcess{{pid: os.Getpid(), home: home, endpoint: endpoint}}
		}
		n.discoverCodex()
		n.ensureCodexRemote(t.Context(), proto.CodexServer{ID: "srv-unreachable", Home: canonicalCodexHome(home, n.home), Endpoint: endpoint})
		waitFor(t, "unreachable diagnostic recorded", func() bool {
			n.codices.mu.Lock()
			defer n.codices.mu.Unlock()
			return n.codices.remoteDiags[endpoint] == codexDiagUnreachable
		})
		id := n.registerCodex(home, remoteRaw, path)
		s := requireCodexSession(t, n.codexSessions(map[string]bool{}), id)
		want := i18n.T(i18n.Default, "codex-history.shared_unreachable")
		if s.ReadOnlyReason != want {
			t.Fatalf("ReadOnlyReason = %q, want %q", s.ReadOnlyReason, want)
		}
	})

	t.Run("home mismatch on initialize", func(t *testing.T) {
		n := remoteTestNode(t)
		home := t.TempDir()
		otherHome := t.TempDir()
		cwd := t.TempDir()
		fix := &remoteFixture{home: otherHome}
		srv := httptest.NewServer(http.HandlerFunc(fix.serve))
		defer srv.Close()
		endpoint := "ws" + strings.TrimPrefix(srv.URL, "http")

		at := time.Now().Add(-time.Minute)
		path := writeCodexTestRollout(t, home, remoteRaw, cwd, "codex-tui", "cli", at, codexEventAt("task_started", at), codexEventAt("task_complete", at))
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		n.codices.scan = func() []codexProcess {
			return []codexProcess{{pid: os.Getpid(), home: home, endpoint: endpoint}}
		}
		n.discoverCodex()
		n.ensureCodexRemote(t.Context(), proto.CodexServer{ID: "srv-mismatch", Home: canonicalCodexHome(home, n.home), Endpoint: endpoint})
		waitFor(t, "home mismatch diagnostic recorded", func() bool {
			n.codices.mu.Lock()
			defer n.codices.mu.Unlock()
			return n.codices.remoteDiags[endpoint] == codexDiagHomeMismatch
		})
		id := n.registerCodex(home, remoteRaw, path)
		s := requireCodexSession(t, n.codexSessions(map[string]bool{}), id)
		want := i18n.T(i18n.Default, "codex-history.shared_home_mismatch")
		if s.ReadOnlyReason != want {
			t.Fatalf("ReadOnlyReason = %q, want %q", s.ReadOnlyReason, want)
		}
	})

	t.Run("invalid missing unix socket", func(t *testing.T) {
		n := remoteTestNode(t)
		home := t.TempDir()
		cwd := t.TempDir()

		at := time.Now().Add(-time.Minute)
		path := writeCodexTestRollout(t, home, remoteRaw, cwd, "codex-tui", "cli", at, codexEventAt("task_started", at), codexEventAt("task_complete", at))
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		n.codices.scan = func() []codexProcess {
			return []codexProcess{{pid: os.Getpid(), home: home, endpoint: "unix://"}}
		}
		n.discoverCodex()
		id := n.registerCodex(home, remoteRaw, path)
		s := requireCodexSession(t, n.codexSessions(map[string]bool{}), id)
		want := i18n.T(i18n.Default, "codex-history.shared_endpoint_invalid")
		wantEP := codexResolvedEndpoint(canonicalCodexHome(home, n.home), "unix://")
		n.codices.mu.Lock()
		gotEP := n.codices.liveEndpoints[id]
		n.codices.mu.Unlock()
		if s.ReadOnlyReason != want || gotEP != wantEP {
			t.Fatalf("ReadOnlyReason = %q (want %q), endpoint=%q (want %q)", s.ReadOnlyReason, want, gotEP, wantEP)
		}
	})

	t.Run("connected shared app-server without thread loaded", func(t *testing.T) {
		n := remoteTestNode(t)
		home := t.TempDir()
		cwd := t.TempDir()
		_, remote := connectRemoteFixture(t, n, home, "srv-ready")
		at := time.Now().Add(-time.Minute)
		path := writeCodexTestRollout(t, home, remoteRaw, cwd, "codex-tui", "cli", at, codexEventAt("task_started", at), codexEventAt("task_complete", at))
		f, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		n.codices.scan = func() []codexProcess {
			return []codexProcess{{pid: os.Getpid(), home: home, endpoint: remote.spec.Endpoint}}
		}
		n.discoverCodex()
		id := n.registerCodex(home, remoteRaw, path)
		s := requireCodexSession(t, n.codexSessions(map[string]bool{}), id)
		want := i18n.T(i18n.Default, "codex-history.shared_not_loaded")
		if s.ReadOnlyReason != want {
			t.Fatalf("ReadOnlyReason = %q, want %q", s.ReadOnlyReason, want)
		}
	})
}

// The takeover's terminal goes to the start's host, Orca too since #17044 (it resumes a Codex thread as herdr does),
// else the first other terminal host of the set; a runtime engine is never one.
func TestCodexTakeoverHostSelectsAvailableLauncher(t *testing.T) {
	for _, tc := range []struct {
		name           string
		hosts          []string
		def            string
		wantKind       string
		wantCanResume  bool
		wantCapability bool
	}{
		{"orca default takes it over in orca", []string{proto.EngineOrca, proto.EngineHerdrCore}, proto.EngineOrca, host.Orca, true, true},
		{"herdr-core default takes it over in herdr-core", []string{proto.EngineOrca, proto.EngineHerdrCore}, proto.EngineHerdrCore, host.HerdrCore, true, true},
		{"orca default with tern takes it over in orca", []string{proto.EngineOrca, proto.EngineTern}, proto.EngineOrca, host.Orca, true, true},
		{"paseo default falls back to herdr-core", []string{proto.EnginePaseo, proto.EngineHerdrCore}, proto.EnginePaseo, host.HerdrCore, true, true},
		{"paseo default falls back to orca", []string{proto.EnginePaseo, proto.EngineOrca}, proto.EnginePaseo, host.Orca, true, true},
		{"orca alone takes it over in orca", []string{proto.EngineOrca}, proto.EngineOrca, host.Orca, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := remoteTestNode(t)
			var regs []host.Host
			for _, k := range tc.hosts {
				if proto.RuntimeEngine(k) {
					continue
				}
				regs = append(regs, &fakeTakeoverLauncherHost{kind: k})
			}
			n.hosts = host.NewRegistry(regs...)
			choice := proto.EngineChoice{Hosts: tc.hosts, Default: tc.def}
			n.eng.mu.Lock()
			n.eng.set = &choice
			n.eng.name = proto.LegacyEngine(choice)
			n.eng.mu.Unlock()

			gotCtx, err := n.takeoverHost(context.Background())
			if tc.wantKind != "" {
				if err != nil || n.startKind(gotCtx) != tc.wantKind {
					t.Fatalf("takeoverHost = (%q, %v), want %q", n.startKind(gotCtx), err, tc.wantKind)
				}
			} else if err == nil {
				t.Fatalf("expected error from takeoverHost, got %q", n.startKind(gotCtx))
			}

			cwd := t.TempDir()
			path := writeCodexTestRollout(t, n.codexHome(), startThread, cwd, "codex-tui", "cli", time.Now().Add(-time.Minute))
			id := n.registerCodex(n.codexHome(), startThread, path)
			s := requireCodexSession(t, n.codexSessions(map[string]bool{}), id)
			if (s.Capabilities.Takeover != nil) != tc.wantCapability {
				t.Fatalf("takeover cap = %+v, want present=%v", s.Capabilities.Takeover, tc.wantCapability)
			}
			if s.Capabilities.Takeover != nil && s.Capabilities.Takeover.CanResume != tc.wantCanResume {
				t.Fatalf("CanResume = %v, want %v (%+v)", s.Capabilities.Takeover.CanResume, tc.wantCanResume, s.Capabilities.Takeover)
			}
		})
	}
}

func TestCodexTakeoverColdActivatesOnSharedRemoteWithoutTerminalEngine(t *testing.T) {
	n := remoteTestNode(t) // no terminal hosts at all
	home := n.codexHome()
	cwd := t.TempDir()
	path := writeCodexTestRollout(t, home, remoteRaw, cwd, "codex-tui", "cli", time.Now().Add(-time.Minute))
	id := n.registerCodex(home, remoteRaw, path)

	f, remote := connectRemoteFixture(t, n, home, "shared-cold")
	f.mu.Lock()
	f.response = func(method string, p map[string]any) (any, bool) {
		if method == "thread/resume" {
			tid, _ := p["threadId"].(string)
			f.mu.Lock()
			if !slices.Contains(f.threads, tid) {
				f.threads = append(f.threads, tid)
			}
			f.mu.Unlock()
			return map[string]any{
				"thread": map[string]any{
					"id":     tid,
					"name":   "Cold Resumed Thread",
					"cwd":    cwd,
					"path":   path,
					"status": map[string]any{"type": "idle"},
				},
			}, true
		}
		return nil, false
	}
	f.mu.Unlock()
	n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
	n.takeovers.lockHeld = func(codexSource) bool { return false }

	s := requireCodexSession(t, n.codexSessions(map[string]bool{}), id)
	if s.Capabilities.Takeover == nil || !s.Capabilities.Takeover.CanResume || s.Capabilities.Takeover.Running {
		t.Fatalf("expected cold-activatable takeover cap, got %+v", s.Capabilities.Takeover)
	}
	plan, err := n.prepareCodexTakeover(context.Background(), id)
	if err != nil || !plan.CanResume || plan.Running {
		t.Fatalf("prepare = (%+v, %v)", plan, err)
	}
	activated, err := n.commitCodexTakeover(context.Background(), id, plan.Token, false)
	if err != nil {
		t.Fatalf("commit cold activation failed: %v", err)
	}
	if activated.SessionID != id || activated.SourceKind != "codex_app_server" || activated.SourceInstanceID != remote.spec.ID || !activated.Capabilities.Send || !activated.Attached || activated.ReadOnlyReason != "" {
		t.Fatalf("activated session = %+v, caps = %+v", activated, activated.Capabilities)
	}
	if f.count("thread/resume") != 1 {
		t.Fatalf("thread/resume calls = %d, want 1", f.count("thread/resume"))
	}
}

// A stopped thread a connected shared app-server has not loaded is read-only like any stopped Agent (#4754, #17809): a
// message to it is refused and loads nothing there; the explicit takeover above is what activates it.
func TestSendCodexDoesNotColdResumeOnSharedRemote(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	path := writeCodexTestRollout(t, home, remoteRaw, t.TempDir(), "codex-tui", "cli", time.Now())
	id := n.registerCodex(home, remoteRaw, path)
	f, _ := connectRemoteFixture(t, n, home, "shared-cold")
	row := requireCodexSession(t, n.codexSessions(map[string]bool{}), id)
	if row.Capabilities.Send || row.Capabilities.Takeover == nil || !row.Capabilities.Takeover.CanResume {
		t.Fatalf("cold history should be read-only with its takeover offered: %+v (%+v)", row.Capabilities, row.Capabilities.Takeover)
	}
	err := n.send(t.Context(), id, "continue", nil, sendOpts{})
	if refusalCode(err) == "" || f.count("thread/resume") != 0 || f.count("turn/start") != 0 {
		t.Fatalf("send = %v, resumes = %v, turns = %v", err, f.sent("thread/resume"), f.sent("turn/start"))
	}
}

func TestCodexTakeoverColdActivateMapsWriterConflictAndEncryptedFailure(t *testing.T) {
	t.Run("active writer conflict from app-server", func(t *testing.T) {
		n := remoteTestNode(t)
		home := n.codexHome()
		cwd := t.TempDir()
		path := writeCodexTestRollout(t, home, remoteRaw, cwd, "codex-tui", "cli", time.Now().Add(-time.Minute))
		id := n.registerCodex(home, remoteRaw, path)
		f, _ := connectRemoteFixture(t, n, home, "shared-conflict")
		f.mu.Lock()
		f.reject = "thread/resume"
		f.rejectCode = -32600
		f.rejectMessage = "thread already has an active writer"
		f.mu.Unlock()
		n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
		n.takeovers.lockHeld = func(codexSource) bool { return false }

		plan, err := n.prepareCodexTakeover(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := n.commitCodexTakeover(context.Background(), id, plan.Token, false); refusalCode(err) != "codex_takeover_writer_busy" {
			t.Fatalf("writer conflict err = %v, want codex_takeover_writer_busy", err)
		}
	})

	t.Run("encrypted context refusal marks continue-only", func(t *testing.T) {
		n := remoteTestNode(t)
		home := n.codexHome()
		cwd := t.TempDir()
		path := writeCodexTestRollout(t, home, remoteRaw, cwd, "codex-tui", "cli", time.Now().Add(-time.Minute))
		id := n.registerCodex(home, remoteRaw, path)
		f, _ := connectRemoteFixture(t, n, home, "shared-encrypted")
		f.mu.Lock()
		f.reject = "thread/resume"
		f.rejectCode = -32000
		f.rejectMessage = "invalid_encrypted_content: unable to decrypt"
		f.mu.Unlock()
		n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
		n.takeovers.lockHeld = func(codexSource) bool { return false }

		plan, err := n.prepareCodexTakeover(context.Background(), id)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := n.commitCodexTakeover(context.Background(), id, plan.Token, false); refusalCode(err) != "codex_takeover_continue_only" {
			t.Fatalf("encrypted failure err = %v, want codex_takeover_continue_only", err)
		}
		s := requireCodexSession(t, n.codexSessions(map[string]bool{}), id)
		if s.Capabilities.Takeover == nil || s.Capabilities.Takeover.CanResume || s.Capabilities.Takeover.Reason != "encrypted_context" {
			t.Fatalf("post-failure cap = %+v", s.Capabilities.Takeover)
		}
	})
}

func TestCodexTakeoverSupportsNonDefaultCodexHome(t *testing.T) {
	t.Run("terminal takeover passes CODEX_HOME and raw thread id", func(t *testing.T) {
		n, f, cwd := codexHistoryNode(t)
		altHome := t.TempDir()
		rawThread := remoteOther
		path := writeCodexTestRollout(t, altHome, rawThread, cwd, "codex-tui", "cli", time.Now().Add(-time.Minute))
		id := n.registerCodex(altHome, rawThread, path)
		if id == rawThread || !strings.HasPrefix(id, "c0de-") {
			t.Fatalf("expected opaque non-default id, got %q", id)
		}
		listSessions(t, n)
		file := resumeFile(f, cwd)
		f.setScreen("w1:p2", cxReady)
		f.after(func(c herdrCall) {
			if c.Method == "pane.send_input" && strings.Contains(str(c.Params["text"]), "codex-start") {
				src, _ := n.codexSource(id)
				n.codices.mu.Lock()
				n.codices.remotes["takeover-alt"] = &codexRemote{n: n, ready: true,
					spec:    proto.CodexServer{ID: "takeover-alt", Home: src.home},
					threads: map[string]remoteThread{rawThread: {id: rawThread, publicID: id, joined: true}}}
				n.codices.mu.Unlock()
			}
		})
		n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
		n.takeovers.lockHeld = func(codexSource) bool { return false }
		n.takeovers.shared = func(context.Context, codexSource) ([]string, error) {
			return []string{"codex", "--remote", "unix:///shared/alt.sock"}, nil
		}

		s := requireCodexSession(t, listSessions(t, n), id)
		if s.Capabilities.Takeover == nil || !s.Capabilities.Takeover.CanResume {
			t.Fatalf("non-default home takeover cap = %+v", s.Capabilities.Takeover)
		}
		plan, err := n.prepareCodexTakeover(context.Background(), id)
		if err != nil || !plan.CanResume {
			t.Fatalf("prepare = (%+v, %v)", plan, err)
		}
		resumed, err := n.commitCodexTakeover(context.Background(), id, plan.Token, false)
		if err != nil {
			t.Fatal(err)
		}
		if resumed.SessionID != id {
			t.Fatalf("resumed SessionID = %q, want %q", resumed.SessionID, id)
		}
		canonicalAlt := canonicalCodexHome(altHome, n.home)
		wantArgv := []string{"CODEX_HOME=" + canonicalAlt, "codex", "--remote", "unix:///shared/alt.sock", "-C", cwd, "resume", rawThread}
		if !slices.Equal(file().Argv, wantArgv) {
			t.Fatalf("argv = %v, want %v", file().Argv, wantArgv)
		}
	})

	t.Run("shared remote cold activation on non-default home uses raw thread id", func(t *testing.T) {
		n := remoteTestNode(t)
		altHome := t.TempDir()
		cwd := t.TempDir()
		rawThread := remoteOther
		path := writeCodexTestRollout(t, altHome, rawThread, cwd, "codex-tui", "cli", time.Now().Add(-time.Minute))
		id := n.registerCodex(altHome, rawThread, path)
		if !strings.HasPrefix(id, "c0de-") {
			t.Fatalf("expected opaque id, got %q", id)
		}
		f, _ := connectRemoteFixture(t, n, altHome, "shared-alt")
		var resumedRaw string
		f.mu.Lock()
		f.response = func(method string, p map[string]any) (any, bool) {
			if method == "thread/resume" {
				resumedRaw, _ = p["threadId"].(string)
				return map[string]any{"thread": map[string]any{"id": resumedRaw, "cwd": cwd, "path": path, "status": map[string]any{"type": "idle"}}}, true
			}
			return nil, false
		}
		f.mu.Unlock()
		n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
		n.takeovers.lockHeld = func(codexSource) bool { return false }

		plan, err := n.prepareCodexTakeover(context.Background(), id)
		if err != nil || !plan.CanResume {
			t.Fatalf("prepare = (%+v, %v)", plan, err)
		}
		sess, err := n.commitCodexTakeover(context.Background(), id, plan.Token, false)
		if err != nil {
			t.Fatal(err)
		}
		if resumedRaw != rawThread || sess.SessionID != id || sess.SourceThreadID != rawThread || !sess.Capabilities.Send {
			t.Fatalf("resumedRaw=%q sess=%+v caps=%+v", resumedRaw, sess, sess.Capabilities)
		}
	})
}

func TestCodexTakeoverInteractiveAndDesktopViewOnlyOrigins(t *testing.T) {
	t.Run("interactive non-tui originator is eligible", func(t *testing.T) {
		n, _, cwd := codexHistoryNode(t)
		path := writeCodexTestRollout(t, n.codexHome(), remoteOther, cwd, "persona", "cli", time.Now().Add(-time.Minute))
		id := n.registerCodex(n.codexHome(), remoteOther, path)
		n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
		n.takeovers.lockHeld = func(codexSource) bool { return false }
		s := requireCodexSession(t, listSessions(t, n), id)
		if s.Capabilities.Takeover == nil || !s.Capabilities.Takeover.CanResume {
			t.Fatalf("interactive originator cap = %+v", s.Capabilities.Takeover)
		}
		plan, err := n.prepareCodexTakeover(context.Background(), id)
		if err != nil || !plan.CanResume || plan.ContinuationPrompt == "" {
			t.Fatalf("prepare = (%+v, %v)", plan, err)
		}
	})

	checkDesktopViewOnly := func(t *testing.T, originator string, terminalWhenClosed bool) {
		n, _, cwd := codexHistoryNode(t)
		home := n.codexHome()
		if err := os.WriteFile(filepath.Join(home, ".codex-global-state.json"), []byte(`{"projectless-thread-ids":["`+remoteOther+`"]}`), 0o600); err != nil {
			t.Fatal(err)
		}
		at := time.Now().Add(-time.Minute)
		path := writeCodexTestRollout(t, home, remoteOther, cwd, originator, "vscode", at, codexEventAt("task_started", at), codexEventAt("task_complete", at))
		id := n.registerCodex(home, remoteOther, path)
		n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
		n.takeovers.lockHeld = func(codexSource) bool { return false }

		// The App's bundled stdio app-server keeps the App thread in continuation-only mode.
		desktopRunning := true
		n.codices.scan = func() []codexProcess {
			if desktopRunning {
				return []codexProcess{{pid: 999999, home: home, endpoint: "stdio://", desktop: true}}
			}
			return nil
		}
		n.codices.checked = func() ([]codexProcess, bool) { return n.codices.scan(), true }
		n.discoverCodex()

		s := requireCodexSession(t, listSessions(t, n), id)
		if s.Capabilities.Terminal {
			t.Fatal("running App offered ordinary terminal resume")
		}
		if takeover := s.Capabilities.Takeover; takeover == nil || takeover.CanResume || takeover.Reason != "app_running" || !takeover.AppOwned {
			t.Fatalf("running App takeover cap = %+v", takeover)
		}
		plan, err := n.prepareCodexTakeover(context.Background(), id)
		if err != nil || plan.CanResume || plan.Reason != "app_running" || plan.ContinuationPrompt == "" {
			t.Fatalf("running App prepare = (%+v, %v)", plan, err)
		}

		// Desktop closed, no writer lock, no conflict: CanResume becomes true.
		desktopRunning = false
		n.discoverCodex()
		s = requireCodexSession(t, listSessions(t, n), id)
		if s.Capabilities.Terminal != terminalWhenClosed {
			t.Fatalf("closed App terminal cap = %t, want %t", s.Capabilities.Terminal, terminalWhenClosed)
		}
		if takeover := s.Capabilities.Takeover; takeover == nil || !takeover.CanResume || !takeover.AppOwned {
			t.Fatalf("closed App takeover cap = %+v", takeover)
		}
		plan, err = n.prepareCodexTakeover(context.Background(), id)
		if err != nil || !plan.CanResume || !plan.AppOwned {
			t.Fatalf("closed App prepare = (%+v, %v)", plan, err)
		}

		// If desktop app starts between prepare and commit, commit must refuse writer_busy.
		desktopRunning = true
		if _, err := n.commitCodexTakeover(context.Background(), id, plan.Token, true); refusalCode(err) != "codex_takeover_writer_busy" {
			t.Fatalf("desktop started mid-takeover err = %v, want codex_takeover_writer_busy", err)
		}
	}
	t.Run("desktop origin respects the running App", func(t *testing.T) { checkDesktopViewOnly(t, "Codex Desktop", true) })
	t.Run("sidebar-confirmed shared origin respects the running App", func(t *testing.T) { checkDesktopViewOnly(t, "persona", false) })

	t.Run("exec, sdk, subagent, and guardian remain rejected", func(t *testing.T) {
		n, _, cwd := codexHistoryNode(t)
		for _, tc := range []struct {
			name, originator string
			source           any
		}{
			{"codex_exec", "codex_exec", "exec"},
			{"codex_sdk", "codex_sdk_ts", "cli"},
			{"subagent", "codex-tui", map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": startThread}}}},
			{"guardian", "codex-tui", map[string]any{"subagent": map[string]any{"other": "guardian"}}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				tid := newUUID()
				path := writeCodexTestRollout(t, n.codexHome(), tid, cwd, tc.originator, tc.source, time.Now().Add(-time.Minute))
				id := n.registerCodex(n.codexHome(), tid, path)
				if _, err := n.prepareCodexTakeover(context.Background(), id); refusalCode(err) != "codex_takeover_unsupported" {
					t.Fatalf("%s accepted: %v", tc.name, err)
				}
			})
		}
	})
}

func TestCodexProcessArgsExtractsTerminalEnvs(t *testing.T) {
	t.Run("nul separated linux environ", func(t *testing.T) {
		env := strings.Join([]string{
			"CODEX_HOME=/opt/codex",
			"HERDR_PANE_ID=w2:p3",
			"HERDR_SESSION=dev",
			"TMUX_PANE=%17",
			"TMUX=/tmp/tmux-501/work,1234,0",
			"HC_TERMINAL_ID=term-9",
			"TERN_PANE=42",
			"ORCA_TERMINAL_HANDLE=handle-7",
			"ORCA_PANE_KEY=tab1:leaf2",
		}, "\x00") + "\x00"
		p, ok := codexProcessArgs(101, []string{"codex"}, env)
		if !ok {
			t.Fatal("expected codex process")
		}
		if p.home != "/opt/codex" || p.pane != "w2:p3" || p.herdrSession != "dev" ||
			p.tmuxPane != "%17" || p.tmuxSocket != "/tmp/tmux-501/work" ||
			p.herdrCore != "term-9" || p.ternPane != "42" ||
			p.orcaHandle != "handle-7" || p.orcaPane != "tab1:leaf2" || !p.hasTakeoverTerminal() {
			t.Fatalf("parsed = %+v", p)
		}
	})

	t.Run("space separated macos ps environ", func(t *testing.T) {
		env := "codex TMUX_PANE=%3 TMUX=/private/tmp/tmux-501/default,4242,0 HC_TERMINAL_ID=hc-1 TERN_PANE=88 ORCA_TERMINAL_HANDLE=h-1"
		p, ok := codexProcessArgs(202, []string{"codex"}, env)
		if !ok {
			t.Fatal("expected codex process")
		}
		if p.tmuxPane != "%3" || p.tmuxSocket != "" || p.herdrCore != "hc-1" || p.ternPane != "88" || p.orcaHandle != "h-1" || !p.hasTakeoverTerminal() {
			t.Fatalf("parsed = %+v", p)
		}
	})

	t.Run("plain external terminal has no takeover terminal", func(t *testing.T) {
		p, ok := codexProcessArgs(303, []string{"codex"}, "TERM_PROGRAM=ghostty\x00CODEX_HOME=/h/.codex\x00")
		if !ok || p.hasTakeoverTerminal() {
			t.Fatalf("expected plain terminal without takeover env, got %+v (ok=%v)", p, ok)
		}
	})
}

func TestCodexTakeoverColdResumeSurvivesConcurrentRefresh(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	cwd := t.TempDir()
	path := writeCodexTestRollout(t, home, remoteRaw, cwd, "codex-tui", "cli", time.Now().Add(-time.Minute))
	id := n.registerCodex(home, remoteRaw, path)

	readEntered := make(chan struct{})
	releaseRead := make(chan struct{})
	var mu sync.Mutex
	blockNextRead := false
	loaded := []string{remoteOther}
	var writeMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		send := func(v any) {
			b, _ := json.Marshal(v)
			writeMu.Lock()
			defer writeMu.Unlock()
			wctx, cancel := context.WithTimeout(r.Context(), time.Second)
			defer cancel()
			_ = c.Write(wctx, websocket.MessageText, b)
		}
		for {
			_, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var m codexMessage
			if json.Unmarshal(b, &m) != nil || len(m.ID) == 0 {
				continue
			}
			go func(m codexMessage) {
				var p map[string]any
				_ = json.Unmarshal(m.Params, &p)
				tid, _ := p["threadId"].(string)
				switch m.Method {
				case "initialize":
					send(map[string]any{"id": m.ID, "result": map[string]any{"codexHome": home}})
				case "thread/loaded/list":
					mu.Lock()
					ids := slices.Clone(loaded)
					mu.Unlock()
					send(map[string]any{"id": m.ID, "result": map[string]any{"data": ids}})
				case "thread/read":
					mu.Lock()
					shouldBlock := tid == remoteOther && blockNextRead
					if shouldBlock {
						blockNextRead = false
					}
					mu.Unlock()
					if shouldBlock {
						close(readEntered)
						<-releaseRead
					}
					send(map[string]any{"id": m.ID, "result": map[string]any{
						"thread": map[string]any{"id": tid, "name": "Other", "cwd": cwd, "status": map[string]any{"type": "idle"}},
					}})
				case "thread/resume":
					mu.Lock()
					if !slices.Contains(loaded, tid) {
						loaded = append(loaded, tid)
					}
					mu.Unlock()
					send(map[string]any{"id": m.ID, "result": map[string]any{
						"thread": map[string]any{"id": tid, "name": "Cold Resumed Thread", "cwd": cwd, "path": path, "status": map[string]any{"type": "idle"}},
					}})
				default:
					send(map[string]any{"id": m.ID, "result": map[string]any{}})
				}
			}(m)
		}
	}))
	t.Cleanup(srv.Close)

	rctx, rcancel := context.WithCancel(context.Background())
	t.Cleanup(rcancel)
	spec := proto.CodexServer{ID: "shared-race", Home: canonicalCodexHome(home, n.home), Endpoint: "ws" + strings.TrimPrefix(srv.URL, "http")}
	n.ensureCodexRemote(rctx, spec)
	var remote *codexRemote
	waitFor(t, "concurrent remote connected", func() bool {
		n.codices.mu.Lock()
		c := n.codices.remotes[spec.ID]
		n.codices.mu.Unlock()
		if c == nil {
			return false
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.ready {
			return false
		}
		remote = c
		return true
	})

	n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
	n.takeovers.lockHeld = func(codexSource) bool { return false }

	mu.Lock()
	blockNextRead = true
	mu.Unlock()
	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- remote.refresh()
	}()
	<-readEntered

	plan, err := n.prepareCodexTakeover(context.Background(), id)
	if err != nil || !plan.CanResume {
		close(releaseRead)
		t.Fatalf("prepare = (%+v, %v)", plan, err)
	}
	activated, err := n.commitCodexTakeover(context.Background(), id, plan.Token, false)
	if err != nil {
		close(releaseRead)
		t.Fatalf("commit cold activation failed: %v", err)
	}
	close(releaseRead)
	if err := <-refreshDone; err != nil {
		t.Fatalf("concurrent refresh failed: %v", err)
	}

	owner, thread, err := n.codexOwner(id)
	if err != nil || owner != remote || !thread.joined || !activated.Capabilities.Send {
		t.Fatalf("cold-resumed thread lost after concurrent refresh: owner=%v thread=%+v err=%v", owner, thread, err)
	}
}

func TestCodexTakeoverColdResumeReceivesImmediateApprovalRequest(t *testing.T) {
	n := remoteTestNode(t)
	hub := withFakeHub(t, n)
	home := n.codexHome()
	cwd := t.TempDir()
	path := writeCodexTestRollout(t, home, remoteRaw, cwd, "codex-tui", "cli", time.Now().Add(-time.Minute))
	id := n.registerCodex(home, remoteRaw, path)

	f, _ := connectRemoteFixture(t, n, home, "shared-ask")
	f.mu.Lock()
	f.resumeRequest = true
	f.mu.Unlock()
	n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
	n.takeovers.lockHeld = func(codexSource) bool { return false }

	plan, err := n.prepareCodexTakeover(context.Background(), id)
	if err != nil || !plan.CanResume {
		t.Fatalf("prepare = (%+v, %v)", plan, err)
	}
	if _, err := n.commitCodexTakeover(context.Background(), id, plan.Token, false); err != nil {
		t.Fatalf("commit cold activation failed: %v", err)
	}
	waitFor(t, "immediate approval request delivered on cold resume", func() bool {
		return f.replyCount() == 1 && len(hub.interactions()) == 1
	})
}

func TestCodexTakeoverRowReusesListPassDesktopProbe(t *testing.T) {
	n, _, cwd := codexHistoryNode(t)
	home := n.codexHome()
	if err := os.WriteFile(filepath.Join(home, ".codex-global-state.json"), []byte(`{"projectless-thread-ids":["`+remoteOther+`"]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Minute)
	path := writeCodexTestRollout(t, home, remoteOther, cwd, "Codex Desktop", "vscode", at, codexEventAt("task_started", at), codexEventAt("task_complete", at))
	id := n.registerCodex(home, remoteOther, path)
	src, _ := n.codexSource(id)
	n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
	n.takeovers.lockHeld = func(codexSource) bool { return false }

	// A bus seen by the list pass still reports app_running even though a fresh probe would find no live socket.
	pass := newCodexListPass()
	pass.desktop[src.home] = true
	got := n.codexTakeoverRowIn(id, src, quickMeta{}, false, pass)
	if got == nil || got.CanResume || got.Reason != "app_running" || !got.AppOwned {
		t.Fatalf("pass-cached desktop probe was bypassed: got %+v", got)
	}

	// prepareCodexTakeover always performs a fresh live probe (sees no bus) and permits idle activation.
	plan, err := n.prepareCodexTakeover(context.Background(), id)
	if err != nil || !plan.CanResume {
		t.Fatalf("prepare should perform live desktop probe, got (%+v, %v)", plan, err)
	}
}

func TestCodexTakeoverColdResumeRollbackNotResurrectedByConcurrentRefresh(t *testing.T) {
	n := remoteTestNode(t)
	home := n.codexHome()
	cwd := t.TempDir()
	path := writeCodexTestRollout(t, home, remoteRaw, cwd, "codex-tui", "cli", time.Now().Add(-time.Minute))
	id := n.registerCodex(home, remoteRaw, path)

	resumeEntered := make(chan struct{})
	releaseResume := make(chan struct{})
	readEntered := make(chan struct{})
	releaseRead := make(chan struct{})
	var mu sync.Mutex
	loaded := []string{}
	blockRead := false
	var writeMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		send := func(v any) {
			b, _ := json.Marshal(v)
			writeMu.Lock()
			defer writeMu.Unlock()
			wctx, cancel := context.WithTimeout(r.Context(), time.Second)
			defer cancel()
			_ = c.Write(wctx, websocket.MessageText, b)
		}
		for {
			_, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var m codexMessage
			if json.Unmarshal(b, &m) != nil || len(m.ID) == 0 {
				continue
			}
			go func(m codexMessage) {
				var p map[string]any
				_ = json.Unmarshal(m.Params, &p)
				tid, _ := p["threadId"].(string)
				switch m.Method {
				case "initialize":
					send(map[string]any{"id": m.ID, "result": map[string]any{"codexHome": home}})
				case "thread/loaded/list":
					mu.Lock()
					ids := slices.Clone(loaded)
					mu.Unlock()
					send(map[string]any{"id": m.ID, "result": map[string]any{"data": ids}})
				case "thread/read":
					mu.Lock()
					shouldBlock := tid == remoteRaw && blockRead
					if shouldBlock {
						blockRead = false
					}
					mu.Unlock()
					if shouldBlock {
						close(readEntered)
						<-releaseRead
					}
					send(map[string]any{"id": m.ID, "result": map[string]any{
						"thread": map[string]any{"id": tid, "name": "Server Loaded", "cwd": cwd, "status": map[string]any{"type": "idle"}},
					}})
				case "thread/resume":
					close(resumeEntered)
					<-releaseResume
					send(map[string]any{"id": m.ID, "error": map[string]any{"code": -32600, "message": "thread already has an active writer"}})
				default:
					send(map[string]any{"id": m.ID, "result": map[string]any{}})
				}
			}(m)
		}
	}))
	t.Cleanup(srv.Close)

	rctx, rcancel := context.WithCancel(context.Background())
	t.Cleanup(rcancel)
	spec := proto.CodexServer{ID: "shared-rollback", Home: canonicalCodexHome(home, n.home), Endpoint: "ws" + strings.TrimPrefix(srv.URL, "http")}
	n.ensureCodexRemote(rctx, spec)
	var remote *codexRemote
	waitFor(t, "rollback remote connected", func() bool {
		n.codices.mu.Lock()
		c := n.codices.remotes[spec.ID]
		n.codices.mu.Unlock()
		if c == nil {
			return false
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if !c.ready {
			return false
		}
		remote = c
		return true
	})

	n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
	n.takeovers.lockHeld = func(codexSource) bool { return false }

	plan, err := n.prepareCodexTakeover(context.Background(), id)
	if err != nil || !plan.CanResume {
		t.Fatalf("prepare = (%+v, %v)", plan, err)
	}
	commitDone := make(chan error, 1)
	go func() {
		_, err := n.commitCodexTakeover(context.Background(), id, plan.Token, false)
		commitDone <- err
	}()
	<-resumeEntered

	// Server now reports remoteRaw loaded while coldResume is still provisional; start refresh and pause it mid-read.
	mu.Lock()
	loaded = []string{remoteRaw}
	blockRead = true
	mu.Unlock()
	refreshDone := make(chan error, 1)
	go func() {
		refreshDone <- remote.refresh()
	}()
	<-readEntered

	// Now fail & rollback coldResume before refresh merges next into c.threads.
	close(releaseResume)
	if err := <-commitDone; refusalCode(err) != "codex_takeover_writer_busy" {
		close(releaseRead)
		t.Fatalf("commit err = %v, want codex_takeover_writer_busy", err)
	}
	close(releaseRead)
	if err := <-refreshDone; err != nil {
		t.Fatalf("refresh err = %v", err)
	}

	// Thread loaded on the server must be visible to codexOwner as non-provisional and unjoined.
	owner, thread, err := n.codexOwner(id)
	if err != nil || owner != remote || thread.provisional || thread.joined {
		t.Fatalf("rolled-back provisional state resurrected: owner=%v thread=%+v err=%v", owner, thread, err)
	}
}

func TestCodexTakeoverColdResumeDeliversApprovalAnsweredBeforeResumeReply(t *testing.T) {
	n := remoteTestNode(t)
	hub := withFakeHub(t, n)
	home := n.codexHome()
	cwd := t.TempDir()
	path := writeCodexTestRollout(t, home, remoteRaw, cwd, "codex-tui", "cli", time.Now().Add(-time.Minute))
	id := n.registerCodex(home, remoteRaw, path)

	releaseResume := make(chan struct{})
	replyGot := make(chan codexMessage, 1)
	var writeMu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		send := func(v any) {
			b, _ := json.Marshal(v)
			writeMu.Lock()
			defer writeMu.Unlock()
			wctx, cancel := context.WithTimeout(r.Context(), time.Second)
			defer cancel()
			_ = c.Write(wctx, websocket.MessageText, b)
		}
		for {
			_, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			var m codexMessage
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			if m.Method == "" {
				select {
				case replyGot <- m:
				default:
				}
				continue
			}
			if len(m.ID) == 0 {
				continue
			}
			go func(m codexMessage) {
				var p map[string]any
				_ = json.Unmarshal(m.Params, &p)
				tid, _ := p["threadId"].(string)
				switch m.Method {
				case "initialize":
					send(map[string]any{"id": m.ID, "result": map[string]any{"codexHome": home}})
				case "thread/loaded/list":
					send(map[string]any{"id": m.ID, "result": map[string]any{"data": []string{}}})
				case "thread/resume":
					// Emit the pending approval request BEFORE replying to thread/resume, so fakeHub answers while
					// the thread is still provisional.
					send(map[string]any{
						"id":     77,
						"method": "item/commandExecution/requestApproval",
						"params": map[string]any{"threadId": tid, "itemId": "cmd-early", "command": "echo early"},
					})
					<-releaseResume
					send(map[string]any{"id": m.ID, "result": map[string]any{
						"thread": map[string]any{"id": tid, "name": "Early Ask Thread", "cwd": cwd, "path": path, "status": map[string]any{"type": "active"}},
					}})
				default:
					send(map[string]any{"id": m.ID, "result": map[string]any{}})
				}
			}(m)
		}
	}))
	t.Cleanup(srv.Close)

	rctx, rcancel := context.WithCancel(context.Background())
	t.Cleanup(rcancel)
	spec := proto.CodexServer{ID: "shared-early-ask", Home: canonicalCodexHome(home, n.home), Endpoint: "ws" + strings.TrimPrefix(srv.URL, "http")}
	n.ensureCodexRemote(rctx, spec)
	waitFor(t, "early-ask remote connected", func() bool {
		n.codices.mu.Lock()
		c := n.codices.remotes[spec.ID]
		n.codices.mu.Unlock()
		if c == nil {
			return false
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.ready
	})

	n.takeovers.writers = func(codexSource) ([]codexWriter, error) { return nil, nil }
	n.takeovers.lockHeld = func(codexSource) bool { return false }

	plan, err := n.prepareCodexTakeover(context.Background(), id)
	if err != nil || !plan.CanResume {
		t.Fatalf("prepare = (%+v, %v)", plan, err)
	}
	commitDone := make(chan error, 1)
	go func() {
		_, err := n.commitCodexTakeover(context.Background(), id, plan.Token, false)
		commitDone <- err
	}()
	// Wait until the hub received & answered the approval while thread/resume is still blocked!
	waitFor(t, "early approval created on hub", func() bool { return len(hub.interactions()) == 1 })
	close(releaseResume)
	if err := <-commitDone; err != nil {
		t.Fatalf("commit err = %v", err)
	}
	select {
	case rep := <-replyGot:
		if string(rep.ID) != "77" {
			t.Fatalf("unexpected approval reply id: %s", rep.ID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("user approval answered before thread/resume returned was dropped")
	}
}
