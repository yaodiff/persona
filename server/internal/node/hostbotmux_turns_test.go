package node

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

type fakeBotmuxTurnsServer struct {
	mu           sync.Mutex
	token        string
	turnsStatus  int
	turnsErrCode string
	schema       string
	caps         []string
	turns        []transcript.BotmuxTurn
	echoActor    bool
	turnsGets    int
	turnsSince   []string
	triggers     []map[string]any
	onTurnsGet   func() // runs while a turns GET is answered (a concurrent dashboard refresh)
	// triggerStatus / triggerErr: a refusal /api/trigger answers with instead (botmux's 403 owner_unresolved, …).
	triggerStatus int
	triggerErr    string
	srv           *httptest.Server
}

func newFakeBotmuxTurnsServer(t *testing.T) *fakeBotmuxTurnsServer {
	t.Helper()
	fs := &fakeBotmuxTurnsServer{token: "tok1", turnsStatus: http.StatusOK, schema: transcript.BotmuxTurnsSchema, echoActor: true}
	fs.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fs.mu.Lock()
		defer fs.mu.Unlock()
		if c, err := r.Cookie("botmux_dashboard_token"); err != nil || c.Value != fs.token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/sessions/") && strings.HasSuffix(r.URL.Path, "/turns") {
			fs.turnsGets++
			if fs.onTurnsGet != nil {
				fs.onTurnsGet()
			}
			fs.turnsSince = append(fs.turnsSince, r.URL.Query().Get("since"))
			w.Header().Set("Content-Type", "application/json")
			if fs.turnsStatus != http.StatusOK {
				w.WriteHeader(fs.turnsStatus)
				if fs.turnsErrCode != "" {
					fmt.Fprintf(w, `{"ok":false,"error":%q}`, fs.turnsErrCode)
				} else {
					fmt.Fprint(w, `{"ok":false,"error":"not_found"}`)
				}
				return
			}
			since := int64(0)
			fmt.Sscanf(r.URL.Query().Get("since"), "%d", &since)
			var page []transcript.BotmuxTurn
			lastSeq := since
			for _, turn := range fs.turns {
				if turn.Seq > since {
					page = append(page, turn)
					if turn.Seq > lastSeq {
						lastSeq = turn.Seq
					}
				}
			}
			_ = json.NewEncoder(w).Encode(transcript.BotmuxTurnsResponse{
				OK:           true,
				Schema:       fs.schema,
				Capabilities: fs.caps,
				Next:         fmt.Sprintf("%d", lastSeq),
				Turns:        page,
			})
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/api/trigger" {
			b, _ := io.ReadAll(r.Body)
			var body map[string]any
			_ = json.Unmarshal(b, &body)
			fs.triggers = append(fs.triggers, body)
			if fs.triggerStatus != 0 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(fs.triggerStatus)
				// botmux's shape (dashboard-ipc-server.ts): the code in errorCode, its words in error.
				fmt.Fprintf(w, `{"ok":false,"errorCode":%q,"error":"refused by the fake"}`, fs.triggerErr)
				return
			}
			resp := map[string]any{"ok": true, "action": "delivered"}
			if fs.echoActor && body["actor"] == "owner" {
				resp["actor"] = "owner"
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(resp)
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(fs.srv.Close)
	return fs
}

func (fs *fakeBotmuxTurnsServer) makeHost(bin string) *botmuxHost {
	port := fs.srv.URL[strings.LastIndex(fs.srv.URL, ":")+1:]
	return &botmuxHost{
		bin:    bin,
		owner:  bmxOwner, // a BotMux Owner Union ID is set; TestBotmuxOwnerUnset* clear it
		client: fs.srv.Client(),
		run: func(_ context.Context, args ...string) ([]byte, error) {
			fs.mu.Lock()
			tok := fs.token
			fs.mu.Unlock()
			switch strings.Join(args, " ") {
			case "observe --include-raw":
				return json.Marshal(map[string]any{
					"schemaVersion": 1,
					"daemons": []any{map[string]any{
						"larkAppId": bmxApp,
						"probe":     map[string]any{"status": "ok"},
						"sessions":  []any{bmxRow(sidA, "claude-code", "alive", "idle", false, "topic")},
					}},
				})
			case "dashboard current", "dashboard current " + botmuxShowLink:
				return []byte(bmxOldDashboard(port, tok)), nil
			default:
				return nil, fmt.Errorf("unexpected args: %v", args)
			}
		},
	}
}

// bmxOwner is the BotMux Owner Union ID the hosts of these tests have (Settings › Agents).
const bmxOwner proto.BotmuxOwnerUnionID = "on_testowner0000000000000000000000"

func sha256Of(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

func TestBotmuxSessionListRefreshesExpiredCapabilities(t *testing.T) {
	ctx := context.Background()
	fs := newFakeBotmuxTurnsServer(t)
	fs.turnsStatus = http.StatusNotFound
	h := fs.makeHost("")
	n, _, _ := herdrNode(t)
	n.hosts = host.NewRegistry(h)
	if err := n.refreshHosts(ctx); err != nil {
		t.Fatal(err)
	}
	notice := func() bool {
		for _, row := range n.sessionList(ctx, false) {
			if row.SessionID == sidA {
				return row.BotmuxExternalTrigger()
			}
		}
		t.Fatal("BotMux session missing from the list")
		return false
	}
	notice()
	waitFor(t, "initial capability probe", notice)

	h.mu.Lock()
	h.caps.at = time.Now().Add(-botmuxCapsTTL)
	h.mu.Unlock()
	fs.mu.Lock()
	fs.turnsStatus = http.StatusOK
	fs.caps = []string{transcript.BotmuxCapOwnerActor}
	fs.mu.Unlock()
	if !notice() {
		t.Fatal("expired capabilities hid the last known notice")
	}
	waitFor(t, "capability re-probe", func() bool { return !notice() })
}

func TestBotmuxWatchRepushesLateAndChangedFileReply(t *testing.T) {
	ctx := context.Background()
	fs := newFakeBotmuxTurnsServer(t)
	h := fs.makeHost("")
	n, _, home := herdrNode(t)
	hub := withHub(t, n)
	n.hosts = host.NewRegistry(h)
	if err := n.refreshHosts(ctx); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "projects", "proj", sidA+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"user","uuid":"u1","message":{"role":"user","content":"<user_message>Question</user_message>"}}`,
		`{"type":"assistant","uuid":"a1","message":{"role":"assistant","id":"m1","content":[{"type":"tool_use","id":"tool1","name":"Bash","input":{"command":"botmux send --card-file /tmp/card.json"}}]}}`,
		`{"type":"user","uuid":"r1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool1","content":"{\"success\":true,\"messageId\":\"om_reply\"}"}]}}`,
		`{"type":"assistant","uuid":"a2","message":{"role":"assistant","id":"m2","content":[{"type":"tool_use","id":"tool2","name":"Bash","input":{"command":"echo done"}}]}}`,
		`{"type":"user","uuid":"r2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tool2","content":"ok"}]}}`,
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := n.handle(ctx, "session.items", bmxMustJSON(map[string]any{"session_id": sidA, "fold_work": true, "limit": 50})); err != nil {
		t.Fatal(err)
	}
	if err := n.watch(sidA, 5); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.unwatch(sidA) })

	for index, text := range []string{"Complete card reply", "Updated card reply"} {
		fs.mu.Lock()
		fs.turns = []transcript.BotmuxTurn{{
			TurnID: "turn-1",
			Seq:    int64(index + 1),
			Anchor: &transcript.BotmuxTurnAnchor{CLIEntryID: "u1"},
			Replies: []transcript.BotmuxTurnReply{{
				MessageID: "om_reply", ResponseKind: "final", Text: text,
			}},
		}}
		fs.mu.Unlock()
		h.mu.Lock()
		h.turnsCache[sidA].fetchedAt = time.Time{}
		h.mu.Unlock()
		waitFor(t, text, func() bool {
			for _, item := range hub.got() {
				if item.Kind == proto.KindToolUse && item.ToolUseID == "tool1" &&
					item.Botmux != nil && item.Botmux.Text == text {
					return true
				}
			}
			return false
		})
	}
	items := hub.got()
	if len(items) != 8 || items[3].ToolUseID != "tool2" || items[7].ToolUseID != "tool2" {
		t.Fatalf("replacements should include each complete folded run, got %+v", items)
	}
	for _, batch := range hub.spans() {
		if batch.n > 0 && (batch.fromSeq != 0 || batch.toSeq != 0 || batch.history == "") {
			t.Fatalf("replacement should preserve the existing cursor and history: %+v", batch)
		}
	}
	fs.mu.Lock()
	fs.turnsStatus = http.StatusNotFound
	fs.mu.Unlock()
	h.mu.Lock()
	h.turnsCache[sidA].fetchedAt = time.Time{}
	h.mu.Unlock()
	waitFor(t, "downgrade clears the structured reply", func() bool {
		items := hub.got()
		return len(items) == 12 && items[8].ToolUseID == "tool1" && items[8].Botmux == nil && items[11].ToolUseID == "tool2"
	})
}

func TestBotmuxCachedTurnsRejectStaleDashboard(t *testing.T) {
	for _, scenario := range []string{"base", "token", "stamp", "schema"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := context.Background()
			fs := newFakeBotmuxTurnsServer(t)
			fs.turns = []transcript.BotmuxTurn{{TurnID: "turn-1", Seq: 1}}
			bin := filepath.Join(t.TempDir(), "botmux")
			if err := os.WriteFile(bin, []byte("old"), 0o600); err != nil {
				t.Fatal(err)
			}
			h := fs.makeHost(bin)
			h.sessionTurns(ctx, sidA)
			if len(h.cachedSessionTurns(sidA)) != 1 {
				t.Fatal("expected structured cache")
			}
			switch scenario {
			case "base":
				h.base = "http://127.0.0.1:1"
			case "token":
				h.token = "rotated"
			case "stamp":
				if err := os.WriteFile(bin, []byte("upgraded"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "schema":
				fs.mu.Lock()
				fs.schema = "unsupported"
				fs.mu.Unlock()
				h.turnsCache[sidA].fetchedAt = time.Time{}
				h.sessionTurns(ctx, sidA)
			}
			if got := h.cachedSessionTurns(sidA); got != nil {
				t.Fatalf("stale structured cache: %+v", got)
			}
			h.turnsBackoffUntil = time.Now().Add(botmuxTurnsErrorBackoff)
			if got, _ := h.sessionTurns(ctx, sidA); got != nil {
				t.Fatalf("stale structured cache during backoff: %+v", got)
			}
		})
	}
}

func TestBotmuxWatchRepushesWrapperReplyAfterResult(t *testing.T) {
	ctx := context.Background()
	fs := newFakeBotmuxTurnsServer(t)
	fs.turnsStatus = http.StatusNotFound
	n, _, home := herdrNode(t)
	hub := withHub(t, n)
	n.hosts = host.NewRegistry(fs.makeHost(""))
	if err := n.refreshHosts(ctx); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".claude", "projects", "proj", sidA+".jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	initial := `{"type":"user","uuid":"u1","message":{"role":"user","content":"<user_message>Question</user_message>"}}`
	if err := os.WriteFile(path, []byte(initial+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := n.watch(sidA, 1); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { n.unwatch(sidA) })
	file, err := n.file(sidA)
	if err != nil {
		t.Fatal(err)
	}
	appendLines := func(lines ...string) {
		t.Helper()
		dst, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := dst.WriteString(strings.Join(lines, "\n") + "\n")
		closeErr := dst.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("append transcript: write %v, close %v", writeErr, closeErr)
		}
		if err := n.refresh(sidA, file); err != nil {
			t.Fatal(err)
		}
	}
	appendLines(
		`{"type":"assistant","uuid":"a1","message":{"role":"assistant","id":"m1","content":[{"type":"tool_use","id":"other","name":"Bash","input":{"command":"echo ready"}}]}}`,
		`{"type":"user","uuid":"r1","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"other","content":"ok"}]}}`,
		`{"type":"assistant","uuid":"a2","message":{"role":"assistant","id":"m2","content":[{"type":"tool_use","id":"send","name":"Bash","input":{"command":"botmux send --response-kind final 'delivered'"}}]}}`,
	)
	waitFor(t, "undecorated send", func() bool {
		items := hub.got()
		return len(items) == 3 && items[2].ToolUseID == "send" && items[2].Botmux == nil
	})
	appendLines(`{"type":"user","uuid":"r2","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"send","content":"ok"}]}}`)
	waitFor(t, "replacement with the completed send", func() bool { return len(hub.got()) == 7 })
	items := hub.got()[3:]
	folds := transcript.FoldWork(items)
	if items[0].ToolUseID != "other" || len(folds) != 1 || folds[0].Fold == nil ||
		len(folds[0].Fold.Botmux) != 1 || folds[0].Fold.Botmux[0].Text != "delivered" {
		t.Fatalf("replacement lost the run or its reply: %+v", items)
	}
	batches := hub.spans()
	if last := batches[len(batches)-1]; last.fromSeq != 4 || last.toSeq != 5 {
		t.Fatalf("replacement changed the live cursor: %+v", last)
	}
}

func TestBotmuxExpiredCapsSurviveInconclusiveProbe(t *testing.T) {
	for _, failure := range []string{"unknown session", "transport error", "different dashboard"} {
		t.Run(failure, func(t *testing.T) {
			ctx := context.Background()
			fs := newFakeBotmuxTurnsServer(t)
			fs.caps = []string{transcript.BotmuxCapOwnerActor}
			h := fs.makeHost("")
			if caps := h.probeCaps(ctx, sidA); !caps.ownerActor {
				t.Fatal("initial probe did not learn ownerActor")
			}
			h.mu.Lock()
			h.caps.at = time.Now().Add(-2 * botmuxCapsTTL)
			old := h.caps
			h.mu.Unlock()
			fs.mu.Lock()
			fs.turnsStatus, fs.turnsErrCode = http.StatusNotFound, "unknown_session"
			fs.mu.Unlock()
			if failure != "unknown session" {
				fs.srv.Close()
			}
			if failure == "different dashboard" {
				h.run = newFakeBotmuxTurnsServer(t).makeHost("").run
			}
			caps := h.probeCaps(ctx, sidA)
			if failure == "different dashboard" {
				if caps.ownerActor {
					t.Fatal("carried ownerActor over to a different dashboard")
				}
			} else if caps != old {
				t.Fatalf("probe changed cached capabilities: %+v", caps)
			}
		})
	}
}

func TestBotmuxVersionProbeAndOwnerActorSend(t *testing.T) {
	ctx := context.Background()
	bin := filepath.Join(t.TempDir(), "botmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	fs := newFakeBotmuxTurnsServer(t)
	h := fs.makeHost(bin)

	// 1) Old BotMux (404): cached per instance, sends without actor, advertises external-trigger notice.
	fs.turnsStatus = http.StatusNotFound
	if err := h.Prompt(ctx, host.PaneRef{Kind: host.Botmux, ID: sidA}, "hello old"); err != nil {
		t.Fatalf("old botmux prompt: %v", err)
	}
	if err := h.Prompt(ctx, host.PaneRef{Kind: host.Botmux, ID: sidA}, "hello old 2"); err != nil {
		t.Fatalf("old botmux prompt 2: %v", err)
	}
	if fs.turnsGets != 1 {
		t.Fatalf("expected 1 cached probe GET for old botmux, got %d", fs.turnsGets)
	}
	if h.externalTriggerNotice() != proto.BotmuxTriggerUpgrade {
		t.Fatal("expected the upgrade notice on 404 old botmux")
	}
	if len(fs.triggers) != 2 || fs.triggers[0]["actor"] != nil || fs.triggers[1]["actor"] != nil {
		t.Fatalf("old botmux triggers must not carry actor: %+v", fs.triggers)
	}

	// 2) BotMux upgraded (binary changes): cache invalidates, re-probes 200 with ownerActor.
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n# upgraded\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	fs.mu.Lock()
	fs.turnsStatus = http.StatusOK
	fs.caps = []string{transcript.BotmuxCapOwnerActor}
	fs.mu.Unlock()

	if err := h.Prompt(ctx, host.PaneRef{Kind: host.Botmux, ID: sidA}, "hello owner"); err != nil {
		t.Fatalf("new botmux prompt: %v", err)
	}
	if fs.turnsGets != 2 {
		t.Fatalf("expected re-probe after upgrade, turnsGets = %d", fs.turnsGets)
	}
	if got := h.externalTriggerNotice(); got != "" {
		t.Fatalf("expected no notice when ownerActor is supported and an owner is set, got %q", got)
	}
	last := fs.triggers[len(fs.triggers)-1]
	if last["actor"] != "owner" {
		t.Fatalf("expected actor=owner, got %+v", last)
	}
	env, _ := last["envelope"].(map[string]any)
	if _, hasPayload := env["payload"]; hasPayload {
		t.Fatalf("owner actor envelope must not carry payload: %+v", env)
	}
	if _, hasRaw := env["rawText"]; hasRaw {
		t.Fatalf("owner actor envelope must not carry rawText: %+v", env)
	}

	// 3) BotMux restarted with rotated token and capabilities: [] (no ownerActor) -> re-probes, omits actor.
	fs.mu.Lock()
	fs.token = "tok2"
	fs.caps = []string{}
	fs.mu.Unlock()
	if err := h.Prompt(ctx, host.PaneRef{Kind: host.Botmux, ID: sidA}, "hello no-actor"); err != nil {
		t.Fatalf("no-actor prompt: %v", err)
	}
	if h.externalTriggerNotice() != proto.BotmuxTriggerUpgrade {
		t.Fatal("expected the upgrade notice when capabilities omits ownerActor")
	}
	if got := fs.triggers[len(fs.triggers)-1]["actor"]; got != nil {
		t.Fatalf("expected no actor when capability absent, got %v", got)
	}
}

func TestBotmuxOwnerActorUnechoedDoesNotResend(t *testing.T) {
	ctx := context.Background()
	fs := newFakeBotmuxTurnsServer(t)
	fs.caps = []string{transcript.BotmuxCapOwnerActor}
	fs.echoActor = false // simulates an instance that ignores actor but still runs the turn
	h := fs.makeHost("")

	if err := h.Prompt(ctx, host.PaneRef{Kind: host.Botmux, ID: sidA}, "once only"); err != nil {
		t.Fatalf("prompt: %v", err)
	}
	if len(fs.triggers) != 1 {
		t.Fatalf("must never resend on unechoed actor; got %d triggers", len(fs.triggers))
	}
	if h.externalTriggerNotice() != proto.BotmuxTriggerUpgrade {
		t.Fatal("unechoed actor must mark ownerActor unavailable for future sends")
	}
}

// A refresh between the capability probe and the POST must not carry actor=owner to an instance that never
// advertised ownerActor, nor record the old instance's unechoed answer against the new one.
func TestBotmuxPromptUsesProbedDashboardIdentity(t *testing.T) {
	for _, echo := range []bool{true, false} {
		t.Run(fmt.Sprintf("echo=%v", echo), func(t *testing.T) {
			ctx := context.Background()
			probed := newFakeBotmuxTurnsServer(t)
			probed.caps = []string{transcript.BotmuxCapOwnerActor}
			probed.echoActor = echo
			moved := newFakeBotmuxTurnsServer(t)
			moved.token = "tok-moved"
			h := probed.makeHost("")
			probed.onTurnsGet = func() {
				h.mu.Lock()
				h.base, h.token = moved.srv.URL, moved.token
				h.mu.Unlock()
			}

			if err := h.Prompt(ctx, host.PaneRef{Kind: host.Botmux, ID: sidA}, "hello"); err != nil {
				t.Fatalf("prompt: %v", err)
			}
			probed.mu.Lock()
			triggers := slices.Clone(probed.triggers)
			probed.mu.Unlock()
			moved.mu.Lock()
			movedTriggers := len(moved.triggers)
			moved.mu.Unlock()
			if movedTriggers != 0 {
				t.Fatalf("the refreshed dashboard got %d triggers it never advertised ownerActor for", movedTriggers)
			}
			if len(triggers) != 1 || triggers[0]["actor"] != "owner" {
				t.Fatalf("probed dashboard triggers = %+v, want one with actor=owner", triggers)
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if !h.ownerUnechoed.at.IsZero() {
				t.Fatalf("unechoed result recorded after the dashboard moved: %+v", h.ownerUnechoed)
			}
		})
	}
}

func TestBotmuxTurnsCacheIdleEvictionResetsCursor(t *testing.T) {
	ctx := context.Background()
	fs := newFakeBotmuxTurnsServer(t)
	fs.turns = []transcript.BotmuxTurn{{TurnID: "turn-1", Seq: 1}, {TurnID: "turn-2", Seq: 2}}
	h := fs.makeHost("")
	if got, _ := h.sessionTurns(ctx, sidA); len(got) != 2 {
		t.Fatalf("turns = %+v", got)
	}
	h.mu.Lock()
	h.turnsCache[sidA].usedAt = time.Now().Add(-botmuxTurnsCacheIdle - time.Second)
	h.turnsCache[sidA].fetchedAt = time.Time{}
	h.mu.Unlock()
	if got, _ := h.sessionTurns(ctx, sidA); len(got) != 2 {
		t.Fatalf("turns after idle eviction = %+v", got)
	}
	fs.mu.Lock()
	since := slices.Clone(fs.turnsSince)
	fs.mu.Unlock()
	if want := []string{"0", "0"}; !reflect.DeepEqual(since, want) {
		t.Fatalf("since = %v, want %v (an evicted session starts from the first turn)", since, want)
	}

	h.mu.Lock()
	h.turnsCache[sidA].usedAt = time.Now().Add(-botmuxTurnsCacheIdle - time.Second)
	h.mu.Unlock()
	if got := h.cachedSessionTurns(sidA); got != nil {
		t.Fatalf("idle cached turns returned: %+v", got)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.turnsCache[sidA]; ok {
		t.Fatal("idle entry kept after a cached read")
	}
}

func TestBotmuxTurnsCacheSessionLimit(t *testing.T) {
	ctx := context.Background()
	fs := newFakeBotmuxTurnsServer(t)
	fs.turns = []transcript.BotmuxTurn{{TurnID: "turn-1", Seq: 1}}
	h := fs.makeHost("")
	h.sessionTurns(ctx, sidA) // the dashboard and caps for the entries below
	now := time.Now()
	h.mu.Lock()
	delete(h.turnsCache, sidA)
	for i := range botmuxTurnsCacheSessions {
		h.turnsCache[fmt.Sprintf("other-%02d", i)] = &botmuxSessionTurnsCache{
			base: h.base, token: h.token, byID: map[string]int{},
			usedAt: now.Add(-time.Duration(botmuxTurnsCacheSessions-i) * time.Second), // other-00 oldest
			turns:  []transcript.BotmuxTurn{{TurnID: "t", Seq: 1}},
		}
	}
	h.mu.Unlock()
	if got, _ := h.sessionTurns(ctx, sidA); len(got) != 1 {
		t.Fatalf("turns = %+v", got)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.turnsCache) != botmuxTurnsCacheSessions {
		t.Fatalf("cached sessions = %d, want %d", len(h.turnsCache), botmuxTurnsCacheSessions)
	}
	if _, ok := h.turnsCache["other-00"]; ok {
		t.Fatal("least recently used session kept")
	}
	if _, ok := h.turnsCache[sidA]; !ok {
		t.Fatal("the session just read was evicted")
	}
}

func TestBotmuxTurnsCacheKeepsNewestTurns(t *testing.T) {
	ctx := context.Background()
	fs := newFakeBotmuxTurnsServer(t)
	for seq := int64(1); seq <= botmuxTurnsCacheTurns+5; seq++ {
		fs.turns = append(fs.turns, transcript.BotmuxTurn{TurnID: fmt.Sprintf("turn-%d", seq), Seq: seq})
	}
	h := fs.makeHost("")
	got, _ := h.sessionTurns(ctx, sidA)
	if len(got) != botmuxTurnsCacheTurns || got[0].TurnID != "turn-6" || got[len(got)-1].TurnID != fmt.Sprintf("turn-%d", botmuxTurnsCacheTurns+5) {
		t.Fatalf("kept %d turns from %s", len(got), got[0].TurnID)
	}
	h.mu.Lock()
	sc := h.turnsCache[sidA]
	if len(sc.byID) != botmuxTurnsCacheTurns || sc.byID["turn-6"] != 0 || sc.cursor != fmt.Sprint(botmuxTurnsCacheTurns+5) {
		t.Fatalf("byID %d, turn-6 at %d, cursor %q", len(sc.byID), sc.byID["turn-6"], sc.cursor)
	}
	sc.fetchedAt = time.Time{}
	h.mu.Unlock()

	// An update to a kept turn replaces it in place; the pruned ones stay pruned (their items keep the wrapper).
	fs.mu.Lock()
	fs.turns[len(fs.turns)-1].Replies = []transcript.BotmuxTurnReply{{Text: "edited"}}
	fs.mu.Unlock()
	got, _ = h.sessionTurns(ctx, sidA)
	if len(got) != botmuxTurnsCacheTurns || len(got[len(got)-1].Replies) != 1 {
		t.Fatalf("after update: %d turns, last %+v", len(got), got[len(got)-1])
	}
}

func TestBotmuxTurnsCacheReleasesInvalidEntries(t *testing.T) {
	ctx := context.Background()
	fs := newFakeBotmuxTurnsServer(t)
	fs.turns = []transcript.BotmuxTurn{{TurnID: "turn-1", Seq: 1}}
	h := fs.makeHost("")
	h.sessionTurns(ctx, sidA)
	h.mu.Lock()
	h.setCapsLocked(botmuxCaps{known: true, base: h.base, token: h.token, stamp: h.caps.stamp, at: time.Now()})
	sc := h.turnsCache[sidA]
	if !sc.invalid || sc.turns != nil || sc.byID != nil || len(sc.released) != 1 {
		t.Fatalf("invalidated entry still holds its turns: %+v", sc)
	}
	sc.invalidAt = time.Now().Add(-botmuxTurnsInvalidGrace - time.Second)
	h.mu.Unlock()

	h.cachedSessionTurns("other") // any cache access prunes
	h.mu.Lock()
	defer h.mu.Unlock()
	if _, ok := h.turnsCache[sidA]; ok {
		t.Fatal("invalid entry retained past its grace period")
	}
}

func TestBotmuxUnknownSession404DoesNotPoisonCache(t *testing.T) {
	ctx := context.Background()
	fs := newFakeBotmuxTurnsServer(t)
	fs.turnsStatus = http.StatusNotFound
	fs.turnsErrCode = "unknown_session"
	h := fs.makeHost("")

	caps := h.probeCaps(ctx, sidA)
	if caps.known {
		t.Fatalf("404 unknown_session must not be cached as old botmux: %+v", caps)
	}
	if got := h.externalTriggerNotice(); got != "" {
		t.Fatalf("unknown_session must not enable externalTriggerNotice, got %q", got)
	}
}

func TestBotmuxStructuredReadIncrementalAndFold(t *testing.T) {
	ctx := context.Background()
	fs := newFakeBotmuxTurnsServer(t)
	fs.caps = []string{transcript.BotmuxCapOwnerActor}

	rawOld := "<botmux_reminder>reply</botmux_reminder>\n\n<user_message>\nold turn\n</user_message>\n\n<sender type=\"user\" open_id=\"ou_old\" name=\"Sam\" />"
	rawNew := "<botmux_reminder>reply</botmux_reminder>\n\n<user_message>\nnew turn\n</user_message>\n\n<sender type=\"user\" open_id=\"ou_owner\" name=\"Alex\" />"

	fs.turns = []transcript.BotmuxTurn{
		{
			TurnID: "t_new",
			Seq:    5,
			At:     1763520000000,
			Inbound: []transcript.BotmuxTurnInbound{{
				MessageID: "om_1",
				Text:      "new turn",
				Sender:    &transcript.BotmuxTurnSender{Type: "user", OpenID: "ou_owner", Name: "Alex"},
				Via:       "owner_actor",
				Placement: &transcript.BotmuxTurnPlacement{Kind: "thread", RootMessageID: "om_root"},
			}},
			Replies: []transcript.BotmuxTurnReply{{
				MessageID:    "om_r1",
				ResponseKind: "final",
				At:           1763520010000,
				Text:         "Card reply full text",
			}},
			Anchor: &transcript.BotmuxTurnAnchor{
				CLIID:       "claude-code",
				InputSha256: sha256Of(rawNew),
			},
		},
	}

	h := fs.makeHost("")
	n, _, home := herdrNode(t)
	n.hosts = host.NewRegistry(h)
	if err := n.refreshHosts(ctx); err != nil {
		t.Fatal(err)
	}

	// Write a real Claude transcript with an old turn (no structured record) + a new turn (with --card-file reply).
	p := filepath.Join(home, ".claude", "projects", "proj", sidA+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		fmt.Sprintf(`{"type":"user","uuid":"u1","timestamp":"2026-11-19T01:00:00Z","message":{"role":"user","content":%q}}`, rawOld),
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-19T01:00:01Z","message":{"role":"assistant","id":"m1","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"botmux send 'old reply'"}}]}}`,
		`{"type":"user","uuid":"r1","timestamp":"2026-11-19T01:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"ok"}]}}`,
		fmt.Sprintf(`{"type":"user","uuid":"u2","timestamp":"2026-11-19T01:01:00Z","message":{"role":"user","content":%q}}`, rawNew),
		`{"type":"assistant","uuid":"a2","timestamp":"2026-11-19T01:01:01Z","message":{"role":"assistant","id":"m2","content":[{"type":"tool_use","id":"tu2","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"user","uuid":"r2","timestamp":"2026-11-19T01:01:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu2","content":"ok"}]}}`,
		`{"type":"assistant","uuid":"a3","timestamp":"2026-11-19T01:01:03Z","message":{"role":"assistant","id":"m3","content":[{"type":"tool_use","id":"tu3","name":"Bash","input":{"command":"botmux send --card-file /tmp/card.json"}}]}}`,
		`{"type":"user","uuid":"r3","timestamp":"2026-11-19T01:01:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu3","content":"{\"success\":true,\"messageId\":\"om_r1\"}"}]}}`,
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	rawResp, err := n.handle(ctx, "session.items", bmxMustJSON(map[string]any{"session_id": sidA, "fold_work": true, "limit": 50}))
	if err != nil {
		t.Fatalf("session.items: %v", err)
	}
	var page struct {
		Items []proto.Item `json:"items"`
	}
	b, _ := json.Marshal(rawResp)
	if err := json.Unmarshal(b, &page); err != nil {
		t.Fatal(err)
	}
	var oldUser, newUser *proto.Botmux
	var foldedReplies []proto.Botmux
	for _, it := range page.Items {
		if it.ID == "u1#0" {
			oldUser = it.Botmux
		}
		if it.ID == "u2#0" {
			newUser = it.Botmux
		}
		if it.Kind == proto.KindFold && it.Fold != nil {
			foldedReplies = append(foldedReplies, it.Fold.Botmux...)
		}
	}
	if oldUser == nil || oldUser.Text != "old turn" || oldUser.Sender != "Sam" {
		t.Fatalf("old turn should fall back to wrapper parsing, got %+v", oldUser)
	}
	if newUser == nil || newUser.Text != "new turn" || newUser.Sender != "Alex" || !newUser.Thread {
		t.Fatalf("new turn should use structured turn record, got %+v", newUser)
	}
	if len(foldedReplies) != 1 || foldedReplies[0].Text != "Card reply full text" || !foldedReplies[0].Final {
		t.Fatalf("folded --card-file reply should carry structured text, got %+v", foldedReplies)
	}

	// Late reply arrives with higher seq: after the recency window elapses, incremental fetch updates the cached turn without re-fetching from 0.
	fs.mu.Lock()
	fs.turns[0].Seq = 9
	fs.turns[0].Replies[0].Text = "Updated card reply"
	fs.mu.Unlock()
	h.mu.Lock()
	if sc := h.turnsCache[sidA]; sc != nil {
		sc.fetchedAt = time.Now().Add(-botmuxTurnsFetchMinInterval - time.Second)
	}
	h.mu.Unlock()
	rawResp2, err := n.handle(ctx, "session.items", bmxMustJSON(map[string]any{"session_id": sidA, "fold_work": true, "limit": 50}))
	if err != nil {
		t.Fatalf("session.items 2: %v", err)
	}
	var page2 struct {
		Items []proto.Item `json:"items"`
	}
	b2, _ := json.Marshal(rawResp2)
	_ = json.Unmarshal(b2, &page2)
	var gotUpdated string
	for _, it := range page2.Items {
		if it.Kind == proto.KindFold && it.Fold != nil && len(it.Fold.Botmux) > 0 {
			gotUpdated = it.Fold.Botmux[0].Text
		}
	}
	if gotUpdated != "Updated card reply" {
		t.Fatalf("expected updated late reply, got %q", gotUpdated)
	}

	t.Run("offline history uses fallback and cached turns without refresh", func(t *testing.T) {
		n.setAgents(nil)
		n.mu.Lock()
		_, listed := n.agents[sidA]
		n.mu.Unlock()
		if listed {
			t.Fatal("BotMux session is still listed")
		}

		h.mu.Lock()
		h.turnsCache[sidA].fetchedAt = time.Now().Add(-botmuxTurnsFetchMinInterval - time.Second)
		h.mu.Unlock()
		fs.mu.Lock()
		getsBefore := fs.turnsGets
		fs.mu.Unlock()

		resp, err := n.handle(ctx, "session.items", bmxMustJSON(map[string]any{"session_id": sidA, "limit": 50}))
		if err != nil {
			t.Fatal(err)
		}
		var page struct {
			Items []proto.Item `json:"items"`
		}
		if err := json.Unmarshal(bmxMustJSON(resp), &page); err != nil {
			t.Fatal(err)
		}
		decoded := make(map[string]string)
		for _, item := range page.Items {
			if item.Botmux != nil {
				decoded[item.ID] = item.Botmux.Text
			}
		}
		for id, want := range map[string]string{"u1#0": "old turn", "a1#0": "old reply", "a3#0": "Updated card reply"} {
			if got := decoded[id]; got != want {
				t.Errorf("offline item %s = %q, want %q", id, got, want)
			}
		}
		fs.mu.Lock()
		defer fs.mu.Unlock()
		if fs.turnsGets != getsBefore {
			t.Errorf("offline read requested turns: %d additional GETs", fs.turnsGets-getsBefore)
		}
	})
}

func TestBotmuxSessionTurnsRefetchesCurrentCursor(t *testing.T) {
	ctx := context.Background()
	fs := newFakeBotmuxTurnsServer(t)
	fs.turns = []transcript.BotmuxTurn{{TurnID: "earlier", Seq: 4}, {TurnID: "current", Seq: 5}}
	h := fs.makeHost("")
	initial, _ := h.sessionTurns(ctx, sidA)
	if len(initial) != 2 || len(initial[1].Replies) != 0 {
		t.Fatalf("expected two turns with no reply at the cursor, got %+v", initial)
	}

	fs.mu.Lock()
	fs.turns[1].Replies = []transcript.BotmuxTurnReply{{MessageID: "om_reply", Text: "Late reply"}}
	fs.turns = append(fs.turns, transcript.BotmuxTurn{TurnID: "late", Seq: 5})
	fs.mu.Unlock()
	h.mu.Lock()
	h.turnsCache[sidA].fetchedAt = time.Time{}
	h.mu.Unlock()

	got, _ := h.sessionTurns(ctx, sidA)
	if len(got) != 3 || got[0].TurnID != "earlier" || got[1].TurnID != "current" ||
		len(got[1].Replies) != 1 || got[1].Replies[0].Text != "Late reply" || got[2].TurnID != "late" {
		t.Fatalf("expected cached history, updated turn and new turn without duplicates, got %+v", got)
	}
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if want := []string{"0", "4"}; !reflect.DeepEqual(fs.turnsSince, want) {
		t.Fatalf("since values = %v, want %v", fs.turnsSince, want)
	}
}

func TestBotmuxFetchTurnsPageResponseSize(t *testing.T) {
	for _, test := range []struct {
		name  string
		extra int
	}{
		{name: "exact limit"},
		{name: "one byte over", extra: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := `{"ok":true}`
			body += strings.Repeat(" ", botmuxTurnsMaxBytes+test.extra-len(body))
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = io.WriteString(w, body)
			}))
			defer srv.Close()
			h := &botmuxHost{client: srv.Client()}
			status, res, _, err := h.fetchTurnsPage(context.Background(), srv.URL, "test", sidA, "0", 1)
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			if test.extra > 0 {
				if !errors.Is(err, errBotmuxTurnsTooLarge) || res.OK {
					t.Fatalf("oversize response was accepted or parsed: %+v, %v", res, err)
				}
			} else if err != nil || !res.OK {
				t.Fatalf("response at limit was rejected: %+v, %v", res, err)
			}
		})
	}
}

func bmxMustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func TestBotmuxListKeyIncludesExternalTrigger(t *testing.T) {
	a := []proto.Session{{SessionID: sidA, PaneID: "botmux:" + sidA, UpdatedAt: time.Unix(100, 0)}}
	b := []proto.Session{{SessionID: sidA, PaneID: "botmux:" + sidA, UpdatedAt: time.Unix(100, 0)}}
	b[0].SetBotmuxExternalTrigger(proto.BotmuxTriggerUpgrade)
	if listKey(a) == listKey(b) {
		t.Fatal("listKey must change when BotmuxExternalTrigger toggles")
	}
	c := []proto.Session{b[0]}
	c[0].SetBotmuxExternalTrigger(proto.BotmuxTriggerOwnerUnset)
	if listKey(b) == listKey(c) {
		t.Fatal("listKey must change when the external-trigger reason changes")
	}
}

func TestBotmuxOwnerActorUnechoedSurvivesStructuredRead(t *testing.T) {
	ctx := context.Background()
	fs := newFakeBotmuxTurnsServer(t)
	fs.caps = []string{transcript.BotmuxCapOwnerActor}
	fs.echoActor = false
	h := fs.makeHost("")

	// Prompt 1 probes capabilities (ownerActor=true), sends actor="owner", server does not echo it -> sticky fallback.
	if err := h.Prompt(ctx, host.PaneRef{Kind: host.Botmux, ID: sidA}, "first"); err != nil {
		t.Fatalf("prompt 1: %v", err)
	}
	if len(fs.triggers) != 1 || fs.triggers[0]["actor"] != "owner" {
		t.Fatalf("prompt 1 should have attempted actor=owner: %+v", fs.triggers)
	}
	if h.externalTriggerNotice() != proto.BotmuxTriggerUpgrade {
		t.Fatal("expected the upgrade notice after unechoed actor")
	}

	// Structured read between two Prompts returns capabilities: ["ownerActor"], must NOT clear the sticky fallback.
	if _, _ = h.sessionTurns(ctx, sidA); h.externalTriggerNotice() != proto.BotmuxTriggerUpgrade {
		t.Fatal("sessionTurns must not overwrite sticky unechoed ownerActor=false")
	}

	// Prompt 2 must stay in external-event mode (no actor="owner").
	if err := h.Prompt(ctx, host.PaneRef{Kind: host.Botmux, ID: sidA}, "second"); err != nil {
		t.Fatalf("prompt 2: %v", err)
	}
	if len(fs.triggers) != 2 || fs.triggers[1]["actor"] != nil {
		t.Fatalf("prompt 2 must omit actor after unechoed fallback + structured read: %+v", fs.triggers[1])
	}

	// Dashboard restart (token rotation) clears the sticky negative record and allows ownerActor again.
	fs.mu.Lock()
	fs.token = "tok_restarted"
	fs.echoActor = true
	fs.mu.Unlock()
	if err := h.Prompt(ctx, host.PaneRef{Kind: host.Botmux, ID: sidA}, "third"); err != nil {
		t.Fatalf("prompt 3: %v", err)
	}
	if len(fs.triggers) != 3 || fs.triggers[2]["actor"] != "owner" {
		t.Fatalf("prompt 3 after dashboard token rotation should re-enable actor=owner: %+v", fs.triggers[2])
	}
}

func TestBotmuxSessionTurnsCallerContextDoesNotBackOff(t *testing.T) {
	tests := []struct {
		name       string
		newContext func() (context.Context, context.CancelFunc)
		wantErr    error
	}{
		{
			name: "deadline exceeded",
			newContext: func() (context.Context, context.CancelFunc) {
				return context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			},
			wantErr: context.DeadlineExceeded,
		},
		{
			name: "canceled",
			newContext: func() (context.Context, context.CancelFunc) {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx, cancel
			},
			wantErr: context.Canceled,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fs := newFakeBotmuxTurnsServer(t)
			fs.turns = []transcript.BotmuxTurn{{TurnID: "cached-turn", Seq: 1}}
			h := fs.makeHost("")
			want, _ := h.sessionTurns(context.Background(), sidA)
			if len(want) != 1 {
				t.Fatalf("failed to seed cached turns: %+v", want)
			}
			h.mu.Lock()
			h.turnsCache[sidA].fetchedAt = time.Time{}
			h.mu.Unlock()

			ctx, cancel := test.newContext()
			defer cancel()
			if ctx.Err() != test.wantErr {
				t.Fatalf("context error = %v, want %v", ctx.Err(), test.wantErr)
			}
			if got, _ := h.sessionTurns(ctx, sidA); !reflect.DeepEqual(got, want) {
				t.Fatalf("caller error lost cached turns: %+v", got)
			}
			if !h.turnsBackoffUntil.IsZero() {
				t.Errorf("caller error set host-wide backoff until %v", h.turnsBackoffUntil)
			}

			fs.mu.Lock()
			fs.turns = []transcript.BotmuxTurn{{TurnID: "other-session-turn", Seq: 1}}
			fs.mu.Unlock()
			if got, _ := h.sessionTurns(context.Background(), sidB); len(got) != 1 || got[0].TurnID != "other-session-turn" {
				t.Fatalf("another session could not fetch structured turns: %+v", got)
			}
		})
	}
}

func TestBotmuxSessionTurnsBackoffAndRecency(t *testing.T) {
	ctx := context.Background()

	t.Run("recent cursor skips redundant HTTP fetch", func(t *testing.T) {
		fs := newFakeBotmuxTurnsServer(t)
		fs.caps = []string{transcript.BotmuxCapOwnerActor}
		h := fs.makeHost("")
		_, _ = h.sessionTurns(ctx, sidA)
		_, _ = h.sessionTurns(ctx, sidA)
		if fs.turnsGets != 1 {
			t.Fatalf("expected 1 GET within recency window, got %d", fs.turnsGets)
		}
	})

	t.Run("unknown_session backs off subsequent fetches", func(t *testing.T) {
		fs := newFakeBotmuxTurnsServer(t)
		fs.turns = []transcript.BotmuxTurn{{TurnID: "cached-turn", Seq: 1}}
		h := fs.makeHost("")
		want, _ := h.sessionTurns(ctx, sidA)
		h.mu.Lock()
		h.turnsCache[sidA].fetchedAt = time.Time{}
		h.mu.Unlock()
		fs.mu.Lock()
		fs.turnsStatus = http.StatusNotFound
		fs.turnsErrCode = "unknown_session"
		fs.mu.Unlock()
		for attempt := 0; attempt < 2; attempt++ {
			if got, _ := h.sessionTurns(ctx, sidA); !reflect.DeepEqual(got, want) || len(got) != 1 {
				t.Fatalf("attempt %d lost cached turns: %+v", attempt, got)
			}
		}
		if fs.turnsGets != 2 {
			t.Fatalf("expected initial GET and one GET before backoff, got %d", fs.turnsGets)
		}
	})

	t.Run("transport error does not refresh dashboard CLI and backs off", func(t *testing.T) {
		fs := newFakeBotmuxTurnsServer(t)
		h := fs.makeHost("")
		var cliCalls int
		origRun := h.run
		h.run = func(ctx context.Context, args ...string) ([]byte, error) {
			if len(args) > 0 && args[0] == "dashboard" {
				cliCalls++
			}
			return origRun(ctx, args...)
		}
		// Prime dashboard URL once, then close the server to force a transport error.
		if _, _, err := h.dashboard(ctx, false); err != nil {
			t.Fatal(err)
		}
		fs.srv.Close()

		_, _ = h.sessionTurns(ctx, sidA)
		if !time.Now().Before(h.turnsBackoffUntil) {
			t.Fatal("transport error did not set host-wide backoff")
		}
		if cliCalls != 1 {
			t.Fatalf("transport error must not trigger dashboard CLI refresh; cliCalls = %d, want 1", cliCalls)
		}
		_, _ = h.sessionTurns(ctx, sidA)
		if cliCalls != 1 {
			t.Fatalf("backoff must suppress subsequent dashboard calls; cliCalls = %d, want 1", cliCalls)
		}
	})

	t.Run("dashboard failure backs off other sessions", func(t *testing.T) {
		fs := newFakeBotmuxTurnsServer(t)
		fs.turnsStatus = http.StatusServiceUnavailable
		h := fs.makeHost("")
		_, _ = h.sessionTurns(ctx, sidA)
		if !time.Now().Before(h.turnsBackoffUntil) {
			t.Fatal("dashboard failure did not set host-wide backoff")
		}
		_, _ = h.sessionTurns(ctx, sidB)
		if fs.turnsGets != 1 {
			t.Fatalf("expected only one GET during host-wide backoff, got %d", fs.turnsGets)
		}
	})

	t.Run("pushItems cached path does not block on stalled dashboard", func(t *testing.T) {
		block := make(chan struct{})
		defer close(block)
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-block:
			case <-r.Context().Done():
			}
		}))
		defer srv.Close()
		port := srv.URL[strings.LastIndex(srv.URL, ":")+1:]
		h := &botmuxHost{
			client: srv.Client(),
			base:   "http://127.0.0.1:" + port,
			token:  "tok",
			run: func(_ context.Context, args ...string) ([]byte, error) {
				if len(args) > 0 && args[0] == "observe" {
					return json.Marshal(map[string]any{
						"schemaVersion": 1,
						"daemons": []any{map[string]any{
							"larkAppId": bmxApp,
							"probe":     map[string]any{"status": "ok"},
							"sessions":  []any{bmxRow(sidA, "claude-code", "alive", "idle", false, "topic")},
						}},
					})
				}
				return []byte(bmxOldDashboard(port, "tok")), nil
			},
		}
		n, _, home := herdrNode(t)
		n.hosts = host.NewRegistry(h)
		if err := n.refreshHosts(ctx); err != nil {
			t.Fatal(err)
		}

		start := time.Now()
		_ = n.applyBotmuxTurnsCached(sidA, nil, []proto.Item{{ID: "u1", Seq: 1, Role: "user", Kind: proto.KindText, Text: "<user_message>\nhi\n</user_message>"}})
		if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
			t.Fatalf("applyBotmuxTurnsCached blocked for %v on stalled dashboard", elapsed)
		}
		path := filepath.Join(home, ".claude", "projects", "proj", sidA+".jsonl")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		line := `{"type":"user","uuid":"u1","message":{"role":"user","content":"<user_message>hi</user_message>"}}`
		if err := os.WriteFile(path, []byte(line+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		start = time.Now()
		result, err := n.handle(ctx, "session.items", bmxMustJSON(map[string]any{"session_id": sidA, "after_seq": 0}))
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Fatalf("session.items blocked for %v on stalled dashboard", elapsed)
		}
		if err != nil {
			t.Fatal(err)
		}
		items := result.(map[string]any)["items"].([]proto.Item)
		if len(items) != 1 || items[0].Botmux == nil || items[0].Botmux.Text != "hi" {
			t.Fatalf("session.items lost wrapper fallback: %+v", items)
		}
	})
}

func TestBotmuxColdDashboardReadFallsBackWithinBudget(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "botmux")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\nsleep 2 &\nwait\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	n, _, _ := herdrNode(t)
	n.hosts = host.NewRegistry(newBotmuxHost(bin, t.Logf))
	n.agents[sidA] = host.Agent{PaneID: "botmux:" + sidA, AgentStatus: proto.StatusIdle}
	items := []proto.Item{{ID: "u1", Role: "user", Kind: proto.KindText, Text: "<user_message>\nwrapper fallback\n</user_message>"}}

	start := time.Now()
	got := n.applyBotmuxTurns(context.Background(), sidA, nil, items, false)
	if elapsed := time.Since(start); elapsed < botmuxTurnsReadTimeout || elapsed >= botmuxTurnsReadTimeout+500*time.Millisecond {
		t.Fatalf("cold dashboard read took %v, want about %v", elapsed, botmuxTurnsReadTimeout)
	}
	if got[0].Botmux == nil || got[0].Botmux.Text != "wrapper fallback" {
		t.Fatalf("cold dashboard read lost wrapper fallback: %+v", got[0])
	}
}

func TestBotmuxFoldExpandAndAfterSeqPreserveStructuredReplies(t *testing.T) {
	ctx := context.Background()
	fs := newFakeBotmuxTurnsServer(t)
	fs.caps = []string{transcript.BotmuxCapOwnerActor}

	rawUser := "<botmux_reminder>reply</botmux_reminder>\n\n<user_message>\nship card\n</user_message>\n\n<sender type=\"user\" open_id=\"ou_owner\" name=\"Alex\" />"
	fs.turns = []transcript.BotmuxTurn{{
		TurnID: "t_card",
		Seq:    10,
		At:     1763520000000,
		Inbound: []transcript.BotmuxTurnInbound{{
			MessageID: "om_1",
			Text:      "ship card",
			Sender:    &transcript.BotmuxTurnSender{Type: "user", Name: "Alex"},
			Via:       "owner_actor",
		}},
		Replies: []transcript.BotmuxTurnReply{{
			MessageID:    "om_r1",
			ResponseKind: "final",
			At:           1763520010000,
			Text:         "Structured card body",
		}},
		Anchor: &transcript.BotmuxTurnAnchor{
			CLIID:       "claude-code",
			InputSha256: sha256Of(rawUser),
		},
	}}

	h := fs.makeHost("")
	n, _, home := herdrNode(t)
	n.hosts = host.NewRegistry(h)
	if err := n.refreshHosts(ctx); err != nil {
		t.Fatal(err)
	}

	p := filepath.Join(home, ".claude", "projects", "proj", sidA+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		fmt.Sprintf(`{"type":"user","uuid":"u1","timestamp":"2026-11-19T01:00:00Z","message":{"role":"user","content":%q}}`, rawUser),
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-19T01:00:01Z","message":{"role":"assistant","id":"m1","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"go test ./..."}}]}}`,
		`{"type":"user","uuid":"r1","timestamp":"2026-11-19T01:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"ok"}]}}`,
		`{"type":"assistant","uuid":"a2","timestamp":"2026-11-19T01:00:03Z","message":{"role":"assistant","id":"m2","content":[{"type":"tool_use","id":"tu2","name":"Bash","input":{"command":"botmux send --card-file /tmp/card.json"}}]}}`,
		`{"type":"user","uuid":"r2","timestamp":"2026-11-19T01:00:04Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu2","content":"{\"success\":true,\"messageId\":\"om_r1\"}"}]}}`,
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines[:4], "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	after := func(seq int) map[string]any {
		t.Helper()
		response, err := n.handle(ctx, "session.items", bmxMustJSON(map[string]any{"session_id": sidA, "after_seq": seq}))
		if err != nil {
			t.Fatal(err)
		}
		return response.(map[string]any)
	}
	first := after(3)
	firstItems := first["items"].([]proto.Item)
	if len(firstItems) != 1 || firstItems[0].ToolUseID != "tu2" || firstItems[0].Botmux != nil || first["to_seq"] != 4 {
		t.Fatalf("expected a pending send at seq 4: %+v", first)
	}
	file, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, writeErr := file.WriteString(lines[4] + "\n")
	if closeErr := file.Close(); writeErr != nil || closeErr != nil {
		t.Fatalf("append receipt: write %v, close %v", writeErr, closeErr)
	}
	second := after(4)
	secondItems := second["items"].([]proto.Item)
	folded := transcript.FoldWork(secondItems)
	if len(secondItems) != 4 || secondItems[0].Seq != 2 || secondItems[3].Seq != 5 ||
		len(folded) != 1 || folded[0].Fold == nil || len(folded[0].Fold.Botmux) != 1 ||
		folded[0].Fold.Botmux[0].Text != "Structured card body" || second["from_seq"] != 4 || second["to_seq"] != 5 {
		t.Fatalf("receipt must resend the completed run and preserve the cursor: %+v", second)
	}

	// 1) Expand the fold (from_seq=2, through_seq=5): user item (seq 1) is NOT in the returned range.
	rangeResp, err := n.handle(ctx, "session.items", bmxMustJSON(map[string]any{
		"session_id":  sidA,
		"from_seq":    2,
		"through_seq": 5,
	}))
	if err != nil {
		t.Fatalf("range session.items: %v", err)
	}
	var rangePage struct {
		Items []proto.Item `json:"items"`
	}
	b, _ := json.Marshal(rangeResp)
	_ = json.Unmarshal(b, &rangePage)
	var foundRange *proto.Botmux
	for _, it := range rangePage.Items {
		if it.ToolUseID == "tu2" && it.Kind == proto.KindToolUse {
			foundRange = it.Botmux
		}
	}
	if len(rangePage.Items) != 4 || rangePage.Items[0].Seq != 2 || rangePage.Items[3].Seq != 5 ||
		foundRange == nil || foundRange.Text != "Structured card body" || !foundRange.Final {
		t.Fatalf("fold expand lost structured --card-file reply: %+v", foundRange)
	}

	// 2) Incremental AfterSeq (after_seq=3): only seq 4..5 returned, no user item.
	afterResp, err := n.handle(ctx, "session.items", bmxMustJSON(map[string]any{
		"session_id": sidA,
		"after_seq":  3,
	}))
	if err != nil {
		t.Fatalf("after_seq session.items: %v", err)
	}
	var afterPage struct {
		Items []proto.Item `json:"items"`
	}
	b2, _ := json.Marshal(afterResp)
	_ = json.Unmarshal(b2, &afterPage)
	var foundAfter *proto.Botmux
	for _, it := range afterPage.Items {
		if it.ToolUseID == "tu2" && it.Kind == proto.KindToolUse {
			foundAfter = it.Botmux
		}
	}
	if foundAfter == nil || foundAfter.Text != "Structured card body" || !foundAfter.Final {
		t.Fatalf("after_seq batch lost structured --card-file reply: %+v", foundAfter)
	}
}

func TestBotmuxNonBotmuxSessionNotDecorated(t *testing.T) {
	ctx := context.Background()
	n, _, home := herdrNode(t)
	plainSID := "11111111-1111-4111-8111-111111111111"
	p := filepath.Join(home, ".claude", "projects", "proj", plainSID+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	lines := []string{
		`{"type":"user","uuid":"u1","timestamp":"2026-11-19T01:00:00Z","message":{"role":"user","content":"<user_message>\nhello from normal chat\n</user_message>"}}`,
		`{"type":"assistant","uuid":"a1","timestamp":"2026-11-19T01:00:01Z","message":{"role":"assistant","id":"m1","content":[{"type":"tool_use","id":"tu1","name":"Bash","input":{"command":"botmux send 'manual test'"}}]}}`,
		`{"type":"user","uuid":"r1","timestamp":"2026-11-19T01:00:02Z","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu1","content":"{\"ok\":true}"}]}}`,
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	rawResp, err := n.handle(ctx, "session.items", bmxMustJSON(map[string]any{"session_id": plainSID, "limit": 50}))
	if err != nil {
		t.Fatalf("session.items: %v", err)
	}
	var page struct {
		Items []proto.Item `json:"items"`
	}
	b, _ := json.Marshal(rawResp)
	_ = json.Unmarshal(b, &page)
	if len(page.Items) != 3 {
		t.Fatalf("items = %d, want 3", len(page.Items))
	}
	for i, it := range page.Items {
		if it.Botmux != nil {
			t.Fatalf("non-BotMux session item[%d] must not be decorated: %+v", i, it.Botmux)
		}
	}
}
