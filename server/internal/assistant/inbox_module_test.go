package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/agentref"
	"github.com/nwverse/persona/server/internal/agentref/agentrefadapt"
	"github.com/nwverse/persona/server/internal/assistant/kernel/seam"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/domainkit/defect"
	"github.com/nwverse/persona/server/internal/domainkit/domaintest"
	"github.com/nwverse/persona/server/internal/rpc"
)

// inboxBusMethods are the bus methods the Inbox answered from its own switch before it registered through the
// kernel (inbox_module.go), in that switch's order.
var inboxBusMethods = []string{
	assistantwire.MethodInboxCollected,
	assistantwire.MethodInboxItem,
	assistantwire.MethodInboxForget,
	assistantwire.MethodInboxRepoMutes,
	assistantwire.MethodInboxMutes,
	assistantwire.MethodInboxPost,
	assistantwire.MethodInboxTriagePreview,
	assistantwire.MethodInboxUnmute,
}

// kernelPorts are a test registry's ports: the routes installed, in order, and nothing else.
type kernelPorts struct{ routes []string }

func (p *kernelPorts) Install(r seam.Route) { r.Accept(p) }
func (p *kernelPorts) VisitRoute(pattern string, _ http.HandlerFunc) {
	p.routes = append(p.routes, pattern)
}
func (p *kernelPorts) Full(*http.Request) bool                 { return true }
func (p *kernelPorts) Write(http.ResponseWriter, error, error) {}
func (p *kernelPorts) Report(*http.Request, defect.Defect)     {}

// sessionRef is the session machine and session name, as hooks take it.
func sessionRef(t *testing.T, machine, session string) agentref.Ref {
	t.Helper()
	return domaintest.Ok(t, agentrefadapt.FromWire(SessionRef{MachineID: machine, SessionID: session}))
}

// The Inbox registers through the kernel exactly what its central lines added before: every Inbox route, the eight
// bus methods of its old switch, and its settled-turn, handshake and snapshot hooks.
func TestInboxRegistersThroughTheKernel(t *testing.T) {
	h, it, _ := newInboxAgentStoppedHub(t)
	ctx := context.Background()
	p := &kernelPorts{}
	reg := seam.NewRegistry(p, p, p, p)
	reg.Register(newInboxModule(h.Service))
	reg.Seal()

	if got, want := slices.Sorted(slices.Values(p.routes)), slices.Sorted(slices.Values(assistantwire.InboxRoutes)); !slices.Equal(got, want) {
		t.Errorf("the Inbox's routes through the kernel:\n%s\nwant assistantwire.InboxRoutes:\n%s",
			strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if h.routing != nil {
		t.Error("routesThrough left the Service routing through a Kernel")
	}
	// Params that decode into no method's P: each registered method answers invalid_params and does nothing.
	for _, m := range inboxBusMethods {
		if _, ok, err := reg.Answer(ctx, m, json.RawMessage(`[]`)); !ok || err == nil {
			t.Errorf("%s is not answered through the kernel (%v)", m, err)
		}
	}
	for _, m := range []string{assistantwire.InboxConfigMethod, assistantwire.MethodRename, "inbox.nothing"} {
		if _, ok, _ := reg.Answer(ctx, m, json.RawMessage(`[]`)); ok {
			t.Errorf("the Inbox answers %s, which it never did", m)
		}
	}

	var snap assistantwire.Snapshot
	reg.Snapshot(ctx, &snap)
	if want := h.inboxReport(ctx); snap.Inbox == nil || fmt.Sprint(*snap.Inbox) != fmt.Sprint(*want) || snap.Partial {
		t.Errorf("the snapshot part is %+v (partial %v), want %+v", snap.Inbox, snap.Partial, want)
	}
	told := len(needsYouTold(t, h.f))
	reg.Connected(ctx)
	waitNeedsYou(t, h.f, told+1)
	reg.TurnSettled(ctx, sessionRef(t, "box", "s-sub"))
	if n := inboxAgentStoppedCount(it); n != 1 {
		t.Errorf("a settled turn of the item's Agent recorded %d stops, want 1", n)
	}
}

// A settled turn reaches the Inbox from turnEnded: the ids become the session value once, and only an open item's
// Agent link to that session takes it. Ids that name no session run no hook and are logged as they came.
func TestASettledTurnReachesTheInboxOnce(t *testing.T) {
	h, it, _ := newInboxAgentStoppedHub(t)
	ctx := context.Background()
	h.turnSettledHooks(ctx, SessionRef{MachineID: "box", SessionID: "s-other"})
	h.turnSettledHooks(ctx, SessionRef{MachineID: "elsewhere", SessionID: "s-sub"})
	h.turnSettledHooks(ctx, SessionRef{MachineID: "box", SessionID: "s-sub"})
	waitFor(t, "the Agent's stop on its item", func() bool {
		h.inbox.mu.Lock()
		defer h.inbox.mu.Unlock()
		return inboxAgentStoppedCount(it) == 1
	})
}

// settledHeard is a module whose settled-turn hook notes each session it is told of.
type settledHeard struct {
	mu    sync.Mutex
	heard []agentref.Ref
}

func (m *settledHeard) Name() seam.ModuleName { return seam.NewModuleName("heard") }

func (m *settledHeard) Register(k seam.Kernel) {
	k.TurnSettled(func(_ context.Context, agent agentref.Ref) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.heard = append(m.heard, agent)
	})
}

func (m *settledHeard) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.heard)
}

// A turn whose machine id is missing or blank calls no hook, since that is the hub's bad input rather than a
// programmer's error: it is logged with the ids as they came, and the next turn with good ids is heard.
func TestATurnWithoutASessionCallsNoHook(t *testing.T) {
	var mu sync.Mutex
	var lines []string
	s := New(Options{Hub: newFakeHub(), Logf: func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}})
	heard := &settledHeard{}
	reg := s.newRegistry() // a kernel of its own, with the noting module only
	reg.Register(heard)
	reg.Seal()
	ctx := context.Background()
	s.turnSettledHooks(ctx, SessionRef{MachineID: " ", SessionID: "s1"})
	s.turnSettledHooks(ctx, SessionRef{SessionID: "s1"})
	s.turnSettledHooks(ctx, SessionRef{MachineID: "mac", SessionID: "s1"})
	waitFor(t, "the good turn's hook", func() bool { return heard.count() > 0 })
	heard.mu.Lock()
	n, same := len(heard.heard), heard.heard[0].IsSame(sessionRef(t, "mac", "s1"))
	heard.mu.Unlock()
	if n != 1 || !same {
		t.Errorf("the hook heard %d turns (the first the good one: %v), want the good one only", n, same)
	}
	mu.Lock()
	defer mu.Unlock()
	var logged []string
	for _, l := range lines {
		if strings.Contains(l, "no settled-turn hook runs") {
			logged = append(logged, l)
		}
	}
	want := []string{
		`assistant: turn ended with machine " " and session "s1", which name no session: no settled-turn hook runs`,
		`assistant: turn ended with machine "" and session "s1", which name no session: no settled-turn hook runs`,
	}
	if !slices.Equal(logged, want) {
		t.Errorf("logged %q, want %q", logged, want)
	}
}

// busBytes is v as the bus writes an answer (internal/rpc's marshal): what the extension manager received when the
// Inbox's switch returned v.
func busBytes(t *testing.T, v any) string {
	t.Helper()
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// The Inbox's bus methods answer through BusHub.request with the bytes its switch answered: each typed answer is
// pinned by a golden and compared with the map the switch built from the same call. Params with a field the method
// does not know still decode.
func TestInboxBusAnswersKeepTheirBytes(t *testing.T) {
	h := newInboxHub(t)
	ctx := context.Background()
	helloed := make(chan struct{})
	close(helloed)
	b := &BusHub{svc: h.Service, helloed: helloed}
	repos, err := h.InboxRepoMutes(ctx, assistantwire.InboxRepoMutes{Source: "github"})
	if err != nil {
		t.Fatal(err)
	}
	mutes, err := h.InboxSourceMutes(ctx, "github")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		method, params, golden string
		old                    any // what the Inbox's switch returned for the same call
	}{
		{assistantwire.MethodInboxCollected, `{"round":"poll","sources":[],"from_a_newer_peer":1}`, `{}`, map[string]any{}},
		{assistantwire.MethodInboxItem, `{"id":"nothing","from_a_newer_peer":1}`, `{"ok":false}`,
			assistantwire.InboxItemResult{}},
		{assistantwire.MethodInboxForget, `{"source":"nothing","from_a_newer_peer":1}`, `{"deleted":0}`,
			map[string]any{"deleted": 0}},
		{assistantwire.MethodInboxRepoMutes, `{"source":"github","from_a_newer_peer":1}`, busBytes(t, map[string]any{"repos": repos}),
			map[string]any{"repos": repos}},
		{assistantwire.MethodInboxMutes, `{"source":"github","from_a_newer_peer":1}`, busBytes(t, map[string]any{"mutes": mutes}),
			map[string]any{"mutes": mutes}},
		{assistantwire.MethodInboxPost, `{"from_a_newer_peer":1}`, busBytes(t, map[string]any{"on": h.inboxPostOn(ctx)}),
			map[string]any{"on": h.inboxPostOn(ctx)}},
		{assistantwire.MethodInboxPost, `{"on":false}`, `{"on":false}`, map[string]any{"on": false}},
		{assistantwire.MethodInboxPost, `{"on":null}`, `{"on":false}`, map[string]any{"on": false}},
		{assistantwire.MethodInboxTriagePreview, `{"source":"github","things":[],"from_a_newer_peer":1}`, `{"verdicts":[]}`,
			map[string]any{"verdicts": []assistantwire.InboxTriageVerdict{}}},
	} {
		out, err := b.request(ctx, c.method, json.RawMessage(c.params))
		if err != nil {
			t.Errorf("%s: %v", c.method, err)
			continue
		}
		got := busBytes(t, out)
		if got != c.golden || got != busBytes(t, c.old) {
			t.Errorf("%s answers %s, want %s (the switch's %s)", c.method, got, c.golden, busBytes(t, c.old))
		}
	}
	// Each reply shape on its own, with values: the typed answer's bytes are the map's.
	verdicts := []assistantwire.InboxTriageVerdict{{State: "error", Why: "<a & b>"}}
	for _, c := range []struct{ typed, old any }{
		{assistantwire.InboxAck{}, map[string]any{}},
		{assistantwire.InboxForgetResult{Deleted: 3}, map[string]any{"deleted": 3}},
		{assistantwire.InboxRepoMutesResult{Repos: []string{"example-org/site"}}, map[string]any{"repos": []string{"example-org/site"}}},
		{assistantwire.InboxRepoMutesResult{}, map[string]any{"repos": []string(nil)}},
		{assistantwire.InboxMutesResult{Mutes: []assistantwire.InboxMute{{ID: "m1", Source: "github", Value: "x"}}},
			map[string]any{"mutes": []assistantwire.InboxMute{{ID: "m1", Source: "github", Value: "x"}}}},
		{assistantwire.InboxMutesResult{}, map[string]any{"mutes": []assistantwire.InboxMute(nil)}},
		{assistantwire.InboxPostResult{On: true}, map[string]any{"on": true}},
		{assistantwire.InboxTriagePreviewResult{Verdicts: verdicts}, map[string]any{"verdicts": verdicts}},
	} {
		if got, want := busBytes(t, c.typed), busBytes(t, c.old); got != want {
			t.Errorf("%T encodes %s, the switch's map %s", c.typed, got, want)
		}
	}
	// Params that do not decode are answered invalid_params, naming the method, as BusHub.request's decode does.
	_, err = b.request(ctx, assistantwire.MethodInboxForget, json.RawMessage(`{"source":3}`))
	var bad *rpc.Error
	if !errors.As(err, &bad) || bad.Code != bus.CodeInvalidParams || !strings.HasPrefix(bad.Message, assistantwire.MethodInboxForget+": json: ") {
		t.Errorf("bad params: %v, want invalid_params naming %s", err, assistantwire.MethodInboxForget)
	}
}

// BusHub.request answers no module's bus method itself, and the full kernel answers each: with a kernel that has no
// module, every name a domain lists as a module's (assistantwire.ModuleMethods) is an unknown method, so a case for one
// anywhere in the request's own dispatch, which would answer it before the kernel, fails here. archguard's
// TestModuleBusMethodsAreNoCentralCase reads the same from the source without running this package, the extension
// manager's and the devices' cases with it.
func TestTheCentralSwitchLeavesTheKernelsBusMethods(t *testing.T) {
	ctx := context.Background()
	listed := assistantwire.ModuleMethods()
	for _, m := range inboxBusMethods {
		if !slices.Contains(listed, m) {
			t.Errorf("%s is not listed as a module's bus method (assistantwire/inbox_bus.go)", m)
		}
	}
	full := New(Options{Hub: newFakeHub(), Logf: testLogf(t)})
	for _, m := range listed {
		// Params that decode into no method's P: a registered method answers invalid_params and does nothing.
		if _, ok, _ := full.kernel.Answer(ctx, m, json.RawMessage(`[]`)); !ok {
			t.Errorf("no module answers %s, which a domain lists as a module's bus method", m)
		}
	}
	empty := New(Options{Hub: newFakeHub(), Logf: testLogf(t)})
	empty.newRegistry().Seal() // a kernel of its own, with no module
	helloed := make(chan struct{})
	close(helloed)
	b := &BusHub{svc: empty, helloed: helloed}
	for _, m := range listed {
		_, err := b.request(ctx, m, json.RawMessage(`[]`))
		var unknown *rpc.Error
		if !errors.As(err, &unknown) || unknown.Code != bus.CodeUnknownMethod {
			t.Errorf("%s without the kernel: %v, want unknown_method: the central switch answers it before the kernel", m, err)
		}
	}
}
