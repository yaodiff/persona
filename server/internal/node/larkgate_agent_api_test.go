package node

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// standardAgentAPI is an unmodified upstream: Bearer authentication and JSON
// responses only. It deliberately knows nothing about Persona decisions.
type standardAgentAPI struct {
	*httptest.Server
	mu            sync.Mutex
	writes        int
	reads         int
	accounts      []string
	personaHeader []string
}

func newStandardAgentAPI(t *testing.T) *standardAgentAPI {
	t.Helper()
	a := &standardAgentAPI{}
	a.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/" && slices.Contains(portProbeAgents, r.Header.Get("User-Agent")) {
			return // the machine's own node may probe test ports, as in fakeAPI
		}
		a.mu.Lock()
		for name := range r.Header {
			if strings.HasPrefix(strings.ToLower(name), "x-persona-") {
				a.personaHeader = append(a.personaHeader, name)
			}
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			a.reads++
			a.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"ok":true}`))
			return
		}
		if r.Header.Get("Authorization") != "Bearer test-token" {
			a.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"error":"read-only credential"}`))
			return
		}
		a.writes++
		a.accounts = append(a.accounts, r.Header.Get("X-Example-Account"))
		a.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	t.Cleanup(a.Close)
	return a
}

func (a *standardAgentAPI) snapshot() (writes, reads int, accounts, headers []string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.writes, a.reads, append([]string(nil), a.accounts...), append([]string(nil), a.personaHeader...)
}

func TestAgentAPIStandardUpstreamNeedsOnlyThePersonaGrant(t *testing.T) {
	api := newStandardAgentAPI(t)
	n, _, _ := ledgerNode(t, api.URL)
	n.larkCfg.Gates[0].PathPrefix = "/agent/v1"
	p := n.gates()
	base, release, err := p.open("ex", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	target := base + "/agent/v1/conversations/F1/messages"
	body := `{"text":"approved once"}`

	// A read-only credential available to the caller cannot write directly to
	// the standard upstream. This is a synthetic file, never a real credential.
	credential := filepath.Join(t.TempDir(), "read-only-token")
	if err := os.WriteFile(credential, []byte("read-only-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(credential)
	if err != nil {
		t.Fatal(err)
	}
	if code, _ := send(t, http.MethodPost, api.URL+"/agent/v1/conversations/F1/messages", body,
		"Authorization", "Bearer "+strings.TrimSpace(string(raw))); code != http.StatusForbidden {
		t.Fatalf("the standard upstream accepted a direct write with the read-only credential: %d", code)
	}
	if code, _ := send(t, http.MethodPost, target, body); code != http.StatusForbidden {
		t.Fatalf("an unapproved proxy write = %d, want 403", code)
	}
	if writes, _, _, _ := api.snapshot(); writes != 0 {
		t.Fatalf("a refused write reached the standard upstream: %d", writes)
	}

	call := larkCall{Method: http.MethodPost, Path: "/conversations/F1/messages", Body: body}
	p.grant("ex", "session-1", call, proto.LarkLetThrough{Kind: proto.LarkThroughCard})
	if code, response := send(t, http.MethodPost, target, body); code != http.StatusOK {
		t.Fatalf("the one approved write against the standard upstream = %d %s", code, response)
	}
	if code, _ := send(t, http.MethodPost, target, body); code != http.StatusForbidden {
		t.Fatalf("the consumed grant replayed: %d, want 403", code)
	}
	if code, response := send(t, http.MethodGet, base+"/agent/v1/me", ""); code != http.StatusOK {
		t.Fatalf("the ordinary read = %d %s", code, response)
	}
	writes, reads, _, headers := api.snapshot()
	if writes != 1 || reads != 1 {
		t.Fatalf("standard upstream executed %d writes and %d reads, want one each", writes, reads)
	}
	if len(headers) != 0 {
		t.Fatalf("the standard upstream received Persona-only approval headers: %v", headers)
	}
}

func TestAgentAPIWriteGETClassificationMatchesAgentAPI(t *testing.T) {
	g := proxiedGate("http://example.invalid")
	g.PathPrefix = "/agent/v1"
	for _, route := range []struct{ pattern, concrete string }{
		{"/conversations/*/qr-code", "/conversations/feed-1/qr-code"},
		{"/calendar/event/*/share-link", "/calendar/event/event-1/share-link"},
		{"/contacts/my-link", "/contacts/my-link"},
		{"/documents/share/password", "/documents/share/password"},
		{"/links/open", "/links/open"},
		{"/meetings/participants/*/audio", "/meetings/participants/person-1/audio"},
		{"/maintenance/export", "/maintenance/export"},
	} {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			if larkGateRead(g, larkCall{Method: method, Path: route.concrete}) {
				t.Errorf("%s %s was treated as an ordinary read", method, route.concrete)
			}
		}
	}
	if !larkGateRead(g, larkCall{Method: http.MethodGet, Path: "/conversations/feed-1/messages"}) {
		t.Fatal("an ordinary Agent API GET became a write")
	}
	g.Rules = []proto.LarkRule{{Method: "*", Path: "**", Action: "confirm"}}
	cfg := proto.AssistantLark{Gates: []proto.CommandGate{g}}
	if got := larkDecision(cfg, larkCall{Gate: 0, Method: http.MethodGet, Path: "/links/open"}); got != "confirm" {
		t.Fatalf("a generic read catch-all let the special write GET through: %q", got)
	}
	if got := larkDecision(cfg, larkCall{Gate: 0, Method: http.MethodGet, Path: "/conversations/feed-1/messages"}); got != "allow" {
		t.Fatalf("an ordinary GET no longer uses the read default: %q", got)
	}
	g.Rules = []proto.LarkRule{
		{Method: http.MethodGet, Path: "**", Action: "allow"},
		{Method: "*", Path: "**", Action: "confirm"},
	}
	cfg.Gates[0] = g
	if got := larkDecision(cfg, larkCall{Gate: 0, Method: http.MethodGet, Path: "/links/open"}); got != "confirm" {
		t.Fatalf("a generic read allow authorized a special write GET: %q", got)
	}
	if got := larkDecision(cfg, larkCall{Gate: 0, Method: http.MethodGet, Path: "/conversations/feed-1/messages"}); got != "allow" {
		t.Fatalf("the generic read allow no longer covers an ordinary GET: %q", got)
	}
	g.Rules = []proto.LarkRule{{Method: http.MethodGet, Path: "/links/open", Action: "allow"}}
	cfg.Gates[0] = g
	special := larkCall{Gate: 0, Method: http.MethodGet, Path: "/links/open"}
	if got := larkDecision(cfg, special); got != "allow" {
		t.Fatalf("an explicit owner allow for the exact special route was ignored: %q", got)
	}
	if summary := larkSummary(cfg.Gates, []larkCall{special}); !strings.Contains(summary, "写操作") || strings.Contains(summary, "读操作") {
		t.Fatalf("the special GET card was labelled as a read: %q", summary)
	}
	ordinary := g
	ordinary.PathPrefix = "/api/v1"
	if !larkGateRead(ordinary, larkCall{Method: http.MethodGet, Path: "/links/open"}) {
		t.Fatal("a same-path third-party gate inherited Agent API's classification")
	}
}

func TestGateProxyAgentAPIWriteGETUsesOneGrantAndWritesTheLedger(t *testing.T) {
	api := newStandardAgentAPI(t)
	n, h, _ := ledgerNode(t, api.URL)
	n.larkCfg.Gates[0].PathPrefix = "/agent/v1"
	p := n.gates()
	base, release, err := p.open("ex", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	target := base + "/agent/v1/links/open?url=https%3A%2F%2Fexample.com"
	if code, _ := send(t, http.MethodGet, target, ""); code != http.StatusForbidden {
		t.Fatalf("an unapproved special GET = %d, want 403", code)
	}
	call := larkCall{Method: http.MethodGet, Path: "/links/open", Query: "url=https%3A%2F%2Fexample.com"}
	p.grant("ex", "session-1", call, proto.LarkLetThrough{Kind: proto.LarkThroughCard})
	if code, body := send(t, http.MethodGet, target, ""); code != http.StatusOK {
		t.Fatalf("the approved special GET = %d %s", code, body)
	}
	if code, _ := send(t, http.MethodGet, target, ""); code != http.StatusForbidden {
		t.Fatalf("the special GET grant replayed: %d, want 403", code)
	}
	if code, body := send(t, http.MethodGet, base+"/agent/v1/me", ""); code != http.StatusOK {
		t.Fatalf("the ordinary read = %d %s", code, body)
	}
	if reports := h.reportList(t, 1); len(reports) != 1 || reports[0].Method != http.MethodGet || reports[0].Path != "/links/open" {
		t.Fatalf("the successful special GET has no sent-as-user ledger row: %+v", reports)
	}
	if _, reads, _, headers := api.snapshot(); reads != 2 || len(headers) != 0 {
		t.Fatalf("standard upstream reads=%d, Persona-only headers=%v", reads, headers)
	}
}

func TestGateProxyUsesTheConfiguredAgentAccountBaseline(t *testing.T) {
	api := newFakeAPI(t)
	n, _, _ := ledgerNode(t, api.URL)
	n.larkCfg.Gates[0].PathPrefix = "/agent/v1"
	n.larkCfg.Gates[0].Env["EXAMPLE_ACCOUNT"] = "2"
	p := n.gates()
	base, release, err := p.open("ex", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	call, deny, ok := parseGateCall(n.larkCfg.Gates[0],
		[]string{http.MethodPost, "/conversations/F1/messages", `{}`}, nil, nil, t.TempDir())
	if !ok || deny != "" {
		t.Fatalf("the configured-account call was not parsed: ok=%v deny=%q", ok, deny)
	}
	p.grant("ex", "session-1", call, proto.LarkLetThrough{Kind: proto.LarkThroughCard})
	code, body := send(t, http.MethodPost, base+"/agent/v1/conversations/F1/messages", `{}`, "x-example-account", "2")
	if code != http.StatusOK || call.Account != "2" {
		t.Fatalf("approved call account=%q; configured account 2 proxy response=%d %s, want account 2 and 200", call.Account, code, body)
	}
	seen := api.requests()
	if len(seen) != 1 || seen[0].Account != "2" {
		t.Fatalf("the configured-account request did not reach account 2 exactly once: %+v", seen)
	}
}

func TestGateProxyBindsItsGrantToTheAgentAccount(t *testing.T) {
	api := newFakeAPI(t)
	n, _, _ := ledgerNode(t, api.URL)
	n.larkCfg.Gates[0].PathPrefix = "/agent/v1"
	p := n.gates()
	base, release, err := p.open("ex", "session-1")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	target := base + "/agent/v1/conversations/F1/messages"
	call := larkCall{Method: http.MethodPost, Path: "/conversations/F1/messages", Body: `{}`}
	p.grant("ex", "session-1", call, proto.LarkLetThrough{Kind: proto.LarkThroughCard})
	if code, body := send(t, http.MethodPost, target, `{}`, "x-example-account", "2"); code != http.StatusForbidden {
		t.Fatalf("a primary-account grant executed as account 2: %d %s", code, body)
	}
	if got := api.requests(); len(got) != 0 {
		t.Fatalf("the altered account reached upstream: %+v", got)
	}
	if code, body := send(t, http.MethodPost, target, `{}`); code != http.StatusOK {
		t.Fatalf("the approved primary account could not use its grant: %d %s", code, body)
	}

	added := call
	added.Account = "2"
	p.grant("ex", "session-1", added, proto.LarkLetThrough{Kind: proto.LarkThroughCard})
	if code, body := send(t, http.MethodPost, target, `{}`, "x-example-account", "2"); code != http.StatusOK {
		t.Fatalf("the approved added account could not use its grant: %d %s", code, body)
	}
	seen := api.requests()
	if len(seen) != 2 || seen[0].Account != "" || seen[1].Account != "2" {
		t.Fatalf("upstream accounts = %+v, want primary then account 2", seen)
	}
}
