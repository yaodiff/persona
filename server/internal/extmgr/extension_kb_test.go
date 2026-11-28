package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extension/host"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The package fixture reuses this test binary as its actual extension program, before TestMain's database setup.
func init() {
	if os.Getenv("PERSONA_TEST_EXTENSION_KB") != "1" {
		return
	}
	var hub *pserve.Hub
	err := pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{
		ID: os.Getenv("PERSONA_EXTENSION_ID"), Version: "1.0.0",
		Init: func(_ context.Context, p extension.InitializeParams, h *pserve.Hub) (map[string]any, error) {
			if p.Granted.HubAPI {
				return nil, errors.New("the fixture must not have hub_api")
			}
			hub = h
			return nil, nil
		},
		Namespaces: map[string]pserve.Handler{"kbtest": func(ctx context.Context, method string, raw json.RawMessage) (any, error) {
			if method == "checkpoint" || method == "markers" {
				name := kbImportCheckpoint
				if method == "markers" {
					name = kbSentMarkers
				}
				var result json.RawMessage
				err := hub.Call(ctx, name, raw, &result)
				return result, err
			}
			if method == "day" {
				var result assistantwire.KBDayResult
				err := hub.Call(ctx, assistantwire.MethodKBDay, raw, &result)
				return result, err
			}
			if method != "roundtrip" {
				return nil, pserve.UnknownMethod(method)
			}
			var p struct {
				ID string `json:"id"`
			}
			if err := json.Unmarshal(raw, &p); err != nil {
				return nil, err
			}
			var saved assistantwire.KBUpdateResult
			if err := hub.Call(ctx, assistantwire.MethodKBUpdate, assistantwire.KBUpdateParams{
				Kind: kb.KindProject, ID: p.ID, Update: kb.Update{Name: p.ID, Summary: "process correction"},
			}, &saved); err != nil {
				return nil, err
			}
			var got assistantwire.KBGetResult
			err := hub.Call(ctx, assistantwire.MethodKBGet, assistantwire.KBGetParams{Kind: kb.KindProject, ID: saved.ID}, &got)
			return got, err
		}},
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(0)
}

// fakeKB is the assistant's knowledge base as the extensions' kb.* calls reach it in this process: a project per
// kb.update, read back by kb.get and kb.list (the engine itself is the assistant's, tested in internal/assistant).
type fakeKB struct {
	mu       sync.Mutex
	projects map[string]string // id → summary
	names    map[string]string // id → name
	a        *fakeAssistant
}

func withFakeKB(h *Manager) *fakeKB {
	k := &fakeKB{projects: map[string]string{}, names: map[string]string{}, a: withAssistant(h)}
	k.a.on(assistantwire.MethodKBExtensionUpdate, func(raw json.RawMessage) (any, error) {
		var env assistantwire.KBExtensionUpdate
		if err := json.Unmarshal(raw, &env); err != nil {
			return nil, err
		}
		var p assistantwire.KBUpdateParams
		if err := json.Unmarshal(env.Params, &p); err != nil {
			return nil, err
		}
		if env.Extension == "" {
			return nil, errors.New("no extension on the write")
		}
		k.mu.Lock()
		k.projects[p.ID] = p.Summary
		k.names[p.ID] = p.Name
		k.mu.Unlock()
		return assistantwire.KBUpdateResult{ID: p.ID}, nil
	})
	k.a.on(assistantwire.MethodKBGet, func(raw json.RawMessage) (any, error) {
		var p assistantwire.KBGetParams
		_ = json.Unmarshal(raw, &p)
		k.mu.Lock()
		defer k.mu.Unlock()
		summary, ok := k.projects[p.ID]
		if !ok {
			return assistantwire.KBGetResult{}, nil
		}
		meta, _ := json.Marshal(map[string]string{"name": k.names[p.ID]})
		return assistantwire.KBGetResult{Detail: &kb.Detail{Kind: p.Kind, ID: p.ID, Meta: meta, Summary: summary}}, nil
	})
	k.a.on(assistantwire.MethodKBList, func(json.RawMessage) (any, error) {
		k.mu.Lock()
		defer k.mu.Unlock()
		var out []map[string]string
		for id := range k.projects {
			out = append(out, map[string]string{"id": id})
		}
		return map[string]any{"entries": out}, nil
	})
	k.a.on(assistantwire.MethodKBSearch, func(raw json.RawMessage) (any, error) {
		return map[string]any{"query": raw}, nil
	})
	k.a.on(assistantwire.MethodKBDay, func(json.RawMessage) (any, error) {
		return map[string]any{"messages": []map[string]any{{"n": 3, "text": "20:00 Example: source line\ncontinued"}}}, nil
	})
	return k
}

func (k *fakeKB) summary(id string) (string, bool) {
	k.mu.Lock()
	defer k.mu.Unlock()
	s, ok := k.projects[id]
	return s, ok
}

func startKBProcess(t *testing.T, h *Manager, id, scopes string) {
	t.Helper()
	mux := h.routes()
	dir := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	quoted := "'" + strings.ReplaceAll(self, "'", "'\\''") + "'"
	if err := os.WriteFile(filepath.Join(dir, "program"), []byte("#!/bin/sh\nPERSONA_TEST_EXTENSION_KB=1 exec "+quoted+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := fmt.Sprintf(`{"manifest_version":1,"id":%q,"name":"KB process","version":"1.0.0",
		"engines":{"extension_protocol":1},"runtime":{"command":{%q:"program"}},"activation_events":["on_startup"],
		"contributes":{"events":[{"id":%q,"subscribe":["agent.finished"]}]},"permissions":{"hub":%s}}`, id, runtime.GOOS+"-"+runtime.GOARCH, id, scopes)
	if err := os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	code, preview := call(t, mux, "POST", "/api/v1/extensions/-/preview", h.cfg.Token, fmt.Sprintf(`{"source":%q}`, "dir:"+dir))
	if code != 200 {
		t.Fatalf("preview %s: %d %v", id, code, preview)
	}
	code, installed := call(t, mux, "POST", "/api/v1/extensions/-/install", h.cfg.Token,
		fmt.Sprintf(`{"preview_id":%q,"permissions_digest":%q}`, preview["preview_id"], preview["permissions_digest"]))
	if code != 200 {
		t.Fatalf("install %s: %d %v", id, code, installed)
	}
	waitLong(t, "the KB extension process", func() bool {
		p := h.extensions.get(id)
		return p != nil && p.proc != nil && p.proc.Ready()
	})
}

func TestExtensionKBRPCFromProcess(t *testing.T) {
	h, _ := extensionTestHub(t)
	e := withFakeKB(h)
	for _, tc := range []struct {
		id, project, scopes string
		write               bool
	}{
		{"kb-process-write", "process-project", `["kb.read","kb.write"]`, true},
		{"kb-process-read", "refused-project", `["kb.read"]`, false},
	} {
		startKBProcess(t, h, tc.id, tc.scopes)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		var got assistantwire.KBGetResult
		err := h.extensions.get(tc.id).proc.Call(ctx, "kbtest.roundtrip", map[string]string{"id": tc.project}, &got)
		cancel()
		if tc.write {
			if err != nil || got.Detail == nil || got.Detail.ID != tc.project || strings.TrimSpace(got.Detail.Summary) != "process correction" {
				t.Fatalf("process roundtrip: %+v %v", got.Detail, err)
			}
			if summary, _ := e.summary(tc.project); strings.TrimSpace(summary) != "process correction" {
				t.Fatal("the process's correction did not reach the knowledge base")
			}
		} else {
			var re *rpc.Error
			if !errors.As(err, &re) || re.Code != extension.CodePermissionDenied || !strings.Contains(string(re.Data), extension.ScopeKBWrite) {
				t.Fatalf("read-only process write: %v", err)
			}
			if _, ok := e.summary(tc.project); ok {
				t.Fatal("the read-only process wrote a project")
			}
		}
	}
}

// Use the actual extension process handler without launching a process. Calls from that handler reach the assistant
// in this process (Options.Assistant); only the package and its granted permission record are synthetic.
func kbExtensionSpec(t *testing.T, h *Manager, id string, granted extension.Permissions) host.Spec {
	t.Helper()
	if h.extensions == nil {
		h.extensions = newExtensions(t.TempDir(), nil, t.Logf)
		h.extensions.h = h
	}
	raw := fmt.Sprintf(`{"manifest_version":1,"id":%q,"name":"Example","version":"1.0.0",
		"engines":{"extension_protocol":1},"runtime":{"command":{%q:"bin/unused"},"http":true},"activation_events":["on_startup"],
		"contributes":{"api":[{"id":"example","prefix":"example"}]},"permissions":{"hub":["kb.read","kb.write"]}}`, id, runtime.GOOS+"-"+runtime.GOARCH)
	m, err := extension.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := h.extensions.reg.AddBuiltin(m, t.TempDir(), true); err != nil {
		t.Fatal(err)
	}
	p := h.extensions.reg.Get(id)
	p.Record.Granted = granted // the manifest deliberately asks for more than some tests granted
	spec, err := h.extensions.specOf(context.Background(), p, &hubExtension{id: id})
	if err != nil {
		t.Fatal(err)
	}
	return spec
}

func TestExtensionKBRPCScopesAndAssistantBridge(t *testing.T) {
	h := newTestManager(t)
	e := withFakeKB(h)
	read := kbExtensionSpec(t, h, "example-read", extension.Permissions{Hub: []string{extension.ScopeKBRead}})
	write := kbExtensionSpec(t, h, "example-write", extension.Permissions{Hub: []string{extension.ScopeKBWrite}})
	none := kbExtensionSpec(t, h, "example-none", extension.Permissions{})
	full := kbExtensionSpec(t, h, "example-full", extension.Permissions{HubAPI: true})
	callRPC := func(spec host.Spec, method, params string) (string, error) {
		out, err := spec.Handle(context.Background(), method, json.RawMessage(params))
		if err != nil {
			return "", err
		}
		raw, err := json.Marshal(out)
		return string(raw), err
	}
	denied := func(spec host.Spec, method, scope string) {
		t.Helper()
		_, err := callRPC(spec, method, `{}`)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != extension.CodePermissionDenied || !strings.Contains(string(re.Data), scope) {
			t.Fatalf("%s: %v", method, err)
		}
	}
	for _, method := range []string{assistantwire.MethodKBList, assistantwire.MethodKBGet, assistantwire.MethodKBDay, assistantwire.MethodKBSearch, "kb.owner"} {
		denied(write, method, extension.ScopeKBRead)
		denied(none, method, extension.ScopeKBRead)
	}
	denied(read, assistantwire.MethodKBUpdate, extension.ScopeKBWrite)
	denied(none, assistantwire.MethodKBUpdate, extension.ScopeKBWrite)
	denied(read, assistantwire.MethodKBPutRecording, extension.ScopeKBWrite)
	denied(none, assistantwire.MethodKBPutRecording, extension.ScopeKBWrite)
	if out, err := callRPC(write, assistantwire.MethodKBUpdate, `{"kind":"projects","id":"example","name":"Example","summary":"confirmed example","tags":["verified"]}`); err != nil || out != `{"id":"example"}` {
		t.Fatalf("scoped write without hub_api: %s %v", out, err)
	}
	if summary, ok := e.summary("example"); !ok || summary != "confirmed example" {
		t.Fatalf("write did not reach the assistant: %q", summary)
	}
	// The write reaches the assistant as the extension's, under the id the manager knows it by (#6100).
	if got := e.a.got(assistantwire.MethodKBExtensionUpdate); len(got) != 1 || !strings.Contains(string(got[0]), `"name":"Example"`) ||
		!strings.Contains(string(got[0]), `"tags":["verified"]`) || !strings.Contains(string(got[0]), `"extension":"example-write"`) {
		t.Fatalf("the write as the assistant got it: %s", got)
	}
	if got := e.a.got(assistantwire.MethodKBUpdate); len(got) != 0 {
		t.Fatalf("an unattributed write reached the assistant: %s", got)
	}
	for _, tc := range []struct{ method, params, contains string }{
		{assistantwire.MethodKBList, `{"kind":"projects"}`, `"id":"example"`},
		{assistantwire.MethodKBGet, `{"kind":"projects","id":"example"}`, "confirmed example"},
		{assistantwire.MethodKBSearch, `{"query":"confirmed example","messages":false}`, "confirmed example"},
		{assistantwire.MethodKBDay, `{"id":"example-chat","day":"2026-01-02"}`, `"n":3,"text":"20:00 Example: source line\ncontinued"`},
	} {
		if out, err := callRPC(read, tc.method, tc.params); err != nil || !strings.Contains(out, tc.contains) {
			t.Fatalf("scoped read %s: %s %v", tc.method, out, err)
		}
	}
	if out, err := callRPC(full, assistantwire.MethodKBUpdate, `{"kind":"projects","id":"legacy","name":"Legacy"}`); err != nil || out != `{"id":"legacy"}` {
		t.Fatalf("legacy full grant: %s %v", out, err)
	}
	if out, err := callRPC(full, assistantwire.MethodKBGet, `{"kind":"projects","id":"legacy"}`); err != nil || !strings.Contains(out, "Legacy") {
		t.Fatalf("legacy full read: %s %v", out, err)
	}
	for _, method := range []string{"kb.forget", "kb.context", "kb.posts_put", "kb.nope", "kb.owner.update", assistantwire.MethodKBExtensionUpdate, assistantwire.MethodKBExtensionDay, assistantwire.MethodKBExtensionPutRecording} {
		_, err := callRPC(full, method, `{}`)
		var re *rpc.Error
		if !errors.As(err, &re) || re.Code != extension.CodeUnknownMethod {
			t.Fatalf("internal method %s was exposed: %v", method, err)
		}
	}
}

func TestExtensionKBRPCDeniesBeforeAssistantCall(t *testing.T) {
	h := newTestManager(t) // no assistant connected
	read := kbExtensionSpec(t, h, "example-read", extension.Permissions{Hub: []string{extension.ScopeKBRead}})
	_, err := read.Handle(context.Background(), assistantwire.MethodKBUpdate, json.RawMessage(`{}`))
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != extension.CodePermissionDenied {
		t.Fatalf("permission must fail before assistant lookup: %v", err)
	}
	_, err = read.Handle(context.Background(), assistantwire.MethodKBList, json.RawMessage(`{"kind":"projects"}`))
	if !errors.As(err, &re) || re.Code != assistantwire.CodeAssistantOffline {
		t.Fatalf("permitted call preserves offline error: %v", err)
	}
	if err := h.extensions.reg.SetEnabled(context.Background(), "example-read", false, "test"); err != nil {
		t.Fatal(err)
	}
	_, err = read.Handle(context.Background(), assistantwire.MethodKBGet, json.RawMessage(`{}`))
	if !errors.As(err, &re) || re.Code != extension.CodePermissionDenied {
		t.Fatalf("handler must observe the current enabled flag: %v", err)
	}
}

func TestExtensionKBRPCWhileSwitchingEnabled(t *testing.T) {
	h := newTestManager(t)
	withFakeKB(h)
	read := kbExtensionSpec(t, h, "example-read", extension.Permissions{Hub: []string{extension.ScopeKBRead}})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := range 1000 {
			if err := h.extensions.reg.SetEnabled(context.Background(), "example-read", i%2 == 0, "test"); err != nil {
				t.Error(err)
				return
			}
			runtime.Gosched()
		}
	}()
	for range 1000 {
		_, err := read.Handle(context.Background(), assistantwire.MethodKBList, json.RawMessage(`{"kind":"projects"}`))
		var re *rpc.Error
		if err != nil && (!errors.As(err, &re) || re.Code != extension.CodePermissionDenied) {
			t.Errorf("RPC while switching enabled: %v", err)
			break
		}
	}
	wg.Wait()
	if err := h.extensions.reg.SetEnabled(context.Background(), "example-read", false, "test"); err != nil {
		t.Fatal(err)
	}
	_, err := read.Handle(context.Background(), assistantwire.MethodKBList, json.RawMessage(`{"kind":"projects"}`))
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != extension.CodePermissionDenied {
		t.Fatalf("disabled extension: %v", err)
	}
}

func TestExtensionKBStopRevokesTokenMintedDuringStartup(t *testing.T) {
	h := newTestManager(t)
	kbExtensionSpec(t, h, "example-read", extension.Permissions{Hub: []string{extension.ScopeKBRead}})
	ps := h.extensions
	snapshot := ps.reg.Snapshot("example-read")
	if err := ps.reg.SetEnabled(context.Background(), "example-read", false, "test"); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	runContext := make(chan context.Context, 1)
	minted := make(chan string, 1)
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	var token startToken
	oldToken := h.extTokens.issue("example-read", "Example", []string{extension.ScopeKBRead})
	p := host.Start(context.Background(), host.Spec{ID: "example-read", Startup: true,
		Command: "/bin/sh", Args: []string{"-c", "exit 0"}, Logf: t.Logf,
		Check: func(ctx context.Context) error { runContext <- ctx; return nil },
		StartEnv: func() []string {
			close(entered)
			<-release
			minted <- token.renew(ps, snapshot, false)
			return nil
		}})
	t.Cleanup(func() { unblock(); p.Stop() })
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the synthetic old process did not enter startup")
	}
	ps.byID["example-read"] = &hubExtension{id: "example-read", proc: p}
	done := make(chan struct{})
	go func() { ps.apply(context.Background(), "example-read"); close(done) }()
	waitLong(t, "the initial token revocation", func() bool { _, ok := h.extTokens.lookup(oldToken); return !ok })
	running := <-runContext
	select {
	case <-running.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("the old process was not canceled before renewal")
	}
	unblock()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("applying the disabled extension did not stop its old startup")
	}
	if tok := <-minted; tok == "" {
		t.Fatal("the test did not exercise renewal during shutdown")
	} else if _, ok := h.extTokens.lookup(tok); ok {
		t.Fatal("an old startup re-registered a token after the extension was disabled")
	}
}
