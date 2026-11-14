package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// autoname is the app's POST …/autoname as the hub relays it (role "" is a test's own full request).
func autoname(t *testing.T, s *Service, role, machine, sid string) (int, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+machine+"/"+sid+"/autoname", nil)
	req.Header.Set("Accept-Language", "en")
	if role != "" {
		req.Header.Set(RoleHeader, role)
	}
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("answer is not JSON: %d %s", rec.Code, rec.Body.String())
	}
	return rec.Code, body
}

func agentNameRecordOf(s *Service, machine, sid string) agentNameRecord {
	s.agentNames.mu.Lock()
	defer s.agentNames.mu.Unlock()
	return s.agentNames.records[agentNameKey(machine, sid)]
}

func putAgentNameRecord(t *testing.T, s *Service, rec agentNameRecord) {
	t.Helper()
	s.agentNames.mu.Lock()
	defer s.agentNames.mu.Unlock()
	if !s.agentNamesLocked(context.Background()) {
		t.Fatal("ledger load")
	}
	if err := s.putAgentNameLocked(context.Background(), agentNameKey(rec.Machine, rec.Session), rec); err != nil {
		t.Fatal(err)
	}
}

// The request is the background naming's own call: the Sorting model, the same prompt, input and output bound. It is
// not held back by the background's guards: the switch is off and the one background change is spent.
func TestRequestedAgentNameIgnoresBackgroundLimits(t *testing.T) {
	s, f, calls := namingFixture(t, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
			System    any    `json:"system"`
			Messages  []struct {
				Content any `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		system, _ := json.Marshal(body.System)
		input, _ := json.Marshal(body.Messages)
		var wantSystem, wantFirst string
		quoted, _ := json.Marshal(strings.TrimSpace(prompts.RenderAgentSessionName()))
		wantSystem = strings.Trim(string(quoted), `"`)
		quoted, _ = json.Marshal("修复 PDF 导出时丢失图片的问题")
		wantFirst = strings.Trim(string(quoted), `"`)
		if body.Model != "sorting" || body.MaxTokens != 256 || !strings.Contains(string(system), wantSystem) || !strings.Contains(string(input), wantFirst) {
			t.Errorf("not the background naming's call: model=%q max=%d system=%s input=%s", body.Model, body.MaxTokens, system, input)
		}
		namingReply(w, "修复 PDF 导出图片")
	})
	setSettings(t, s, func(v *Settings) { off := false; v.AgentAutoNaming = &off })
	c := f.sessions["server"][0]
	putAgentNameRecord(t, s, agentNameRecord{Machine: "server", Session: "s1", Title: "整理导出文档", RawTitle: c.Title, Changes: 1, NamedAt: time.Now().Add(-time.Hour), Failures: 2, LastFailure: "invalid_name", NextAttempt: time.Now().Add(time.Hour)})
	if s.tryAgentName(context.Background(), "server", c, time.Now()) || calls.Load() != 0 {
		t.Fatal("the fixture's background naming is not exhausted")
	}
	status, body := autoname(t, s, roleFull, "server", "s1")
	if status != http.StatusOK || body["ok"] != true || body["title"] != "修复 PDF 导出图片" || body["title_source"] != "auto" || body["changed"] != true {
		t.Fatalf("answer: %d %v", status, body)
	}
	if got := s.agentDisplayName("server", c); got != "修复 PDF 导出图片" || calls.Load() != 1 {
		t.Fatalf("shared title %q after %d calls", got, calls.Load())
	}
	rec := agentNameRecordOf(s, "server", "s1")
	if !rec.Requested || rec.Manual || rec.Changes != 1 || rec.Failures != 0 || rec.LastFailure != "" || !rec.NextAttempt.IsZero() || rec.NamedOutput != c.OutputTokens {
		t.Fatalf("ledger: %+v", rec)
	}
	// The same answer again is no change, and the background still does not spend another call on it.
	if status, body = autoname(t, s, roleFull, "server", "s1"); status != http.StatusOK || body["changed"] != false || calls.Load() != 2 {
		t.Fatalf("second request: %d %v after %d calls", status, body, calls.Load())
	}
	if s.tryAgentName(context.Background(), "server", c, time.Now()) || calls.Load() != 2 {
		t.Fatal("the background renamed a session the user just named")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call.Method == DeviceSessionRename || call.Method == DeviceSessionSend {
			t.Fatalf("a requested name touched the Agent's input: %+v", call)
		}
	}
}

// A hand-written name is replaced because the user asked, and the result is an automatic name: the background leaves
// it alone while the custom title stands, a later native rename wins, and so does a later rename in the app.
func TestRequestedAgentNameReplacesAManualName(t *testing.T) {
	s, f, calls := namingFixture(t, func(w http.ResponseWriter, r *http.Request) { namingReply(w, "修复 PDF 导出图片") })
	if _, err := s.Rename(context.Background(), RenameParams{MachineID: "server", SessionID: "s1", Params: map[string]any{"title": "我选的名字"}}); err != nil {
		t.Fatal(err)
	}
	manual := f.sessions["server"][0]
	manual.Title, manual.TitleSource, manual.TitleManual = "我选的名字", proto.TitleSourceManual, true
	f.setSessions("server", manual) // the node applied /rename
	if got := s.agentDisplayName("server", manual); got != "" {
		t.Fatalf("a native custom title was decorated before the request: %q", got)
	}
	if status, body := autoname(t, s, roleFull, "server", "s1"); status != http.StatusOK || body["title"] != "修复 PDF 导出图片" || body["changed"] != true {
		t.Fatalf("answer: %d %v", status, body)
	}
	row := manual
	s.nameAgentRow("server", &row)
	if row.Title != "修复 PDF 导出图片" || row.TitleSource != proto.TitleSourceAuto || row.TitleManual {
		t.Fatalf("row: title=%q source=%q manual=%v", row.Title, row.TitleSource, row.TitleManual)
	}
	if rec := agentNameRecordOf(s, "server", "s1"); rec.Manual || !rec.Requested || rec.RawTitle != "我选的名字" {
		t.Fatalf("ledger: %+v", rec)
	}
	// The background sees a custom title: it neither calls the model nor takes the requested name away.
	if s.tryAgentName(context.Background(), "server", manual, time.Now()) || calls.Load() != 1 || s.agentDisplayName("server", manual) != "修复 PDF 导出图片" {
		t.Fatal("the background pass dropped or replaced the requested name")
	}
	// A native rename after it wins at once, and the next pass records it as the user's.
	renamed := manual
	renamed.Title = "终端里改的名字"
	f.setSessions("server", renamed)
	if got := s.agentDisplayName("server", renamed); got != "" {
		t.Fatalf("a later native rename did not win: %q", got)
	}
	if s.tryAgentName(context.Background(), "server", renamed, time.Now()) || calls.Load() != 1 {
		t.Fatal("the background named a session with a custom title")
	}
	if rec := agentNameRecordOf(s, "server", "s1"); !rec.Manual || rec.Requested || rec.Title != "" {
		t.Fatalf("native rename not recorded as the user's: %+v", rec)
	}
	// Asked again, then renamed in the app: the hand-written name is the user's again.
	if status, _ := autoname(t, s, roleFull, "server", "s1"); status != http.StatusOK || s.agentDisplayName("server", renamed) != "修复 PDF 导出图片" {
		t.Fatalf("second request: %d", status)
	}
	if _, err := s.Rename(context.Background(), RenameParams{MachineID: "server", SessionID: "s1", Params: map[string]any{"title": "又改回手写"}}); err != nil {
		t.Fatal(err)
	}
	plain := renamed
	plain.TitleSource, plain.TitleManual = proto.TitleSourcePrompt, false // before the node applies it
	if rec := agentNameRecordOf(s, "server", "s1"); !rec.Manual || rec.Requested || s.agentDisplayName("server", plain) != "又改回手写" {
		t.Fatalf("app rename after a requested name: %+v", rec)
	}
}

// A name written by hand while the model is asked is the later wish: it stays, in the app or in the Agent itself.
func TestRequestedAgentNameYieldsToARenameDuringTheCall(t *testing.T) {
	for _, where := range []string{"app", "native"} {
		t.Run(where, func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			s, f, _ := namingFixture(t, func(w http.ResponseWriter, r *http.Request) {
				close(started)
				<-release
				namingReply(w, "修复 PDF 导出图片")
			})
			var once sync.Once
			letGo := func() { once.Do(func() { close(release) }) }
			t.Cleanup(letGo)
			type answer struct {
				status int
				body   map[string]any
			}
			done := make(chan answer, 1)
			go func() {
				status, body := autoname(t, s, roleFull, "server", "s1")
				done <- answer{status, body}
			}()
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("the model never started")
			}
			row := f.sessions["server"][0]
			if where == "app" {
				if _, err := s.Rename(context.Background(), RenameParams{MachineID: "server", SessionID: "s1", Params: map[string]any{"title": "我选的名字"}}); err != nil {
					t.Fatal(err)
				}
			} else {
				row.Title, row.TitleSource, row.TitleManual = "终端里改的名字", proto.TitleSourceManual, true
				f.setSessions("server", row)
			}
			letGo()
			select {
			case a := <-done:
				if a.status != http.StatusConflict || a.body["code"] != "renamed_meanwhile" || a.body["retryable"] != false {
					t.Fatalf("answer: %d %v", a.status, a.body)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("the request never answered")
			}
			want := map[string]string{"app": "我选的名字", "native": ""}[where] // "": the Agent's own custom title shows
			if got := s.agentDisplayName("server", row); got != want {
				t.Fatalf("the hand-written name was replaced: %q", got)
			}
			if rec := agentNameRecordOf(s, "server", "s1"); rec.Requested {
				t.Fatalf("ledger: %+v", rec)
			}
		})
	}
}

// A runtime that cannot say where its title came from is skipped by the background; the user's request names it.
func TestRequestedAgentNameWithoutProvenance(t *testing.T) {
	s, f, calls := namingFixture(t, func(w http.ResponseWriter, r *http.Request) { namingReply(w, "Fix PDF export images") })
	c := f.sessions["server"][0]
	c.Title, c.TitleSource = "Background job retries", proto.TitleSourceUnknown
	f.setSessions("server", c)
	if s.tryAgentName(context.Background(), "server", c, time.Now()) || calls.Load() != 0 {
		t.Fatal("the background named a title of unknown provenance")
	}
	if status, body := autoname(t, s, roleFull, "server", "s1"); status != http.StatusOK || body["title"] != "Fix PDF export images" {
		t.Fatalf("answer: %d %v", status, body)
	}
	row := c
	s.nameAgentRow("server", &row)
	if row.Title != "Fix PDF export images" || row.TitleSource != proto.TitleSourceAuto {
		t.Fatalf("row: %q %q", row.Title, row.TitleSource)
	}
	c.Title = "Background job retries, part two" // the runtime's own title moved on
	if got := s.agentDisplayName("server", c); got != "" {
		t.Fatalf("a changed native title kept the old name: %q", got)
	}
}

func TestRequestedAgentNameRefusals(t *testing.T) {
	for _, tc := range []struct {
		name      string
		role, sid string
		prepare   func(t *testing.T, s *Service, f *fakeHub)
		answer    func(http.ResponseWriter, *http.Request)
		status    int
		code      string
		retryable bool
		reason    string // the static reason logged ("" for a refusal that is not a failure)
		modelRuns int32
	}{
		{name: "read-only device", role: "readonly", sid: "s1", status: http.StatusForbidden, code: "forbidden"},
		{name: "extension", role: roleExtension, sid: "s1", status: http.StatusForbidden, code: "forbidden"},
		{name: "unknown session", role: roleFull, sid: "gone", status: http.StatusNotFound, code: "not_found"},
		{name: "assistant conversation", role: roleFull, sid: "s1", prepare: func(t *testing.T, s *Service, f *fakeHub) {
			c := f.sessions["server"][0]
			c.Assistant = true
			f.setSessions("server", c)
		}, status: http.StatusBadRequest, code: "assistant_session"},
		{name: "main conversation", role: roleFull, sid: "s1", prepare: func(t *testing.T, s *Service, f *fakeHub) {
			setSettings(t, s, func(v *Settings) { v.Main = "s1" })
		}, status: http.StatusBadRequest, code: "assistant_session"},
		{name: "no model", role: roleFull, sid: "s1", prepare: func(t *testing.T, s *Service, f *fakeHub) {
			f.mu.Lock()
			f.hello.Models.AI, f.hello.Models.TriageAI = ai.Config{}, nil
			f.mu.Unlock()
			s.settingsChanged(context.Background())
		}, status: http.StatusConflict, code: "model_not_configured", reason: "model_not_configured"},
		{name: "no dialogue", role: roleFull, sid: "s1", prepare: func(t *testing.T, s *Service, f *fakeHub) { f.setItems() },
			status: http.StatusConflict, code: "no_dialogue", reason: "no_dialogue"},
		{name: "invalid name", role: roleFull, sid: "s1", answer: func(w http.ResponseWriter, r *http.Request) { namingReply(w, "<private-instruction>") },
			status: http.StatusBadGateway, code: "invalid_name", retryable: true, reason: "invalid_name", modelRuns: 1},
		{name: "truncated", role: roleFull, sid: "s1", answer: func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"private-instruction"}],"stop_reason":"max_tokens"}`))
		}, status: http.StatusBadGateway, code: "invalid_name", retryable: true, reason: "model_output_truncated", modelRuns: 1},
		{name: "provider error", role: roleFull, sid: "s1", answer: func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("private-provider-token private-instruction"))
		}, status: http.StatusBadGateway, code: "model_failed", retryable: true, reason: "model_call_failed", modelRuns: 1},
		{name: "items unreadable", role: roleFull, sid: "s1", prepare: func(t *testing.T, s *Service, f *fakeHub) {
			f.mu.Lock()
			f.itemsFail = 2
			f.mu.Unlock()
		}, status: http.StatusBadGateway, code: "dialogue_unavailable", retryable: true, reason: "dialogue_unavailable"},
		{name: "ledger write", role: roleFull, sid: "s1", prepare: func(t *testing.T, s *Service, f *fakeHub) {
			base := s.store()
			s.store = func() Store { return failingAgentNameStore{base} }
		}, status: http.StatusInternalServerError, code: "save_failed", retryable: true, reason: "result_write_failed", modelRuns: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			answer := tc.answer
			if answer == nil {
				answer = func(w http.ResponseWriter, r *http.Request) { namingReply(w, "Fix export") }
			}
			s, f, calls := namingFixture(t, answer)
			var mu sync.Mutex
			var lines []string
			s.logf = func(format string, args ...any) {
				mu.Lock()
				defer mu.Unlock()
				lines = append(lines, fmt.Sprintf(format, args...))
			}
			if tc.prepare != nil {
				tc.prepare(t, s, f)
			}
			status, body := autoname(t, s, tc.role, "server", tc.sid)
			if status != tc.status || body["code"] != tc.code || body["retryable"] != tc.retryable || body["error"] == "" || body["title"] != nil {
				t.Fatalf("answer: %d %v, want %d %s retryable=%v", status, body, tc.status, tc.code, tc.retryable)
			}
			if text, _ := body["error"].(string); strings.Contains(text, "private-") || strings.Contains(text, "assistant-agent_naming.") {
				t.Fatalf("error text leaks or is not localized: %q", text)
			}
			if calls.Load() != tc.modelRuns {
				t.Fatalf("model calls: %d, want %d", calls.Load(), tc.modelRuns)
			}
			mu.Lock()
			log := strings.Join(lines, "\n")
			mu.Unlock()
			if tc.reason != "" && !strings.Contains(log, `requested session="server"/"`+tc.sid+`" reason=`+tc.reason) || strings.Contains(log, "private-") {
				t.Fatalf("missing or unsafe failure log: %s", log)
			}
			// A failed request is not a background failure: no counter, no cooldown, no name.
			if rec := agentNameRecordOf(s, "server", tc.sid); rec.Failures != 0 || !rec.NextAttempt.IsZero() || rec.Title != "" || rec.Requested {
				t.Fatalf("a refused request changed the ledger: %+v", rec)
			}
			if got := s.agentDisplayName("server", f.sessions["server"][0]); got != "" {
				t.Fatalf("a refused request changed the name: %q", got)
			}
			if tc.code == "forbidden" && strings.Contains(strings.Join(lines, "\n"), "assistant-agent_naming.") {
				t.Fatalf("error text is not localized: %v", body)
			}
		})
	}
}

func TestRequestedAgentNameTimesOut(t *testing.T) {
	before := agentNamingTimeout
	agentNamingTimeout = 50 * time.Millisecond
	t.Cleanup(func() { agentNamingTimeout = before })
	release := make(chan struct{})
	s, _, _ := namingFixture(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	})
	t.Cleanup(func() { close(release) }) // after the fixture's: it runs before the model's server waits for its handlers
	var mu sync.Mutex
	var lines []string
	s.logf = func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	status, body := autoname(t, s, roleFull, "server", "s1")
	if status != http.StatusGatewayTimeout || body["code"] != "timeout" || body["retryable"] != true {
		t.Fatalf("answer: %d %v", status, body)
	}
	mu.Lock()
	defer mu.Unlock()
	if log := strings.Join(lines, "\n"); !strings.Contains(log, `requested session="server"/"s1" reason=naming_timeout`) {
		t.Fatalf("timeout not logged: %s", log)
	}
}

// Requests for one session that arrive while its call is out share it: one model call, the same answer for each.
func TestRequestedAgentNameRunsOncePerSession(t *testing.T) {
	started, release := make(chan struct{}, 16), make(chan struct{})
	s, f, calls := namingFixture(t, func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-release
		namingReply(w, "修复 PDF 导出图片")
	})
	var once sync.Once
	letGo := func() { once.Do(func() { close(release) }) }
	t.Cleanup(letGo) // a failed run must not leave the model's server waiting for its handlers
	other := f.sessions["server"][0]
	other.SessionID = "s2"
	f.setSessions("server", f.sessions["server"][0], other)
	const waiting = 6
	type answer struct {
		status int
		body   map[string]any
	}
	answers := make(chan answer, waiting+1)
	ask := func(sid string) {
		status, body := autoname(t, s, roleFull, "server", sid)
		answers <- answer{status, body}
	}
	// Every caller has reached the singleflight gate, so all six share the call already held by the model.
	admitted := make(chan struct{}, waiting)
	s.agentNames.beforeRequest = func() { admitted <- struct{}{} }
	go ask("s1")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("the model never started")
	}
	for range waiting - 1 {
		go ask("s1")
	}
	for range waiting {
		select {
		case <-admitted:
		case <-time.After(5 * time.Second):
			t.Fatal("a request never reached the shared call")
		}
	}
	s.agentNames.beforeRequest = nil
	// Another session has its own model call; this one still has only one caller.
	go ask("s2")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("another session waited for this one's call")
	}
	letGo()
	for range waiting + 1 {
		select {
		case a := <-answers:
			if a.status != http.StatusOK || a.body["title"] != "修复 PDF 导出图片" {
				t.Fatalf("answer: %d %v", a.status, a.body)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("a request never answered")
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("model calls: %d, want one per session", calls.Load())
	}
}

// With the fleet on, the name is on the list the hub is told before the request is answered (the apps' session
// update), a custom title's row says the name is automatic, and the assistant's own conversations are refused.
func TestRequestedAgentNameUpdatesTheSessionList(t *testing.T) {
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { namingReply(w, "修复 PDF 导出图片") }))
	t.Cleanup(model.Close)
	f := newFakeHub()
	f.ownModels = true
	f.hello.Models.TriageAI = &ai.Config{BaseURL: model.URL, APIKey: "fixture", Model: "sorting", ReasoningEffort: "off"}
	// The transcript is read through the hub's device call, as every node method the manager does not answer itself.
	f.sessions["mac"] = nil
	f.items = []proto.Item{{Seq: 1, Role: "user", Kind: proto.KindText, Text: "修复 PDF 导出时丢失图片的问题。"}, {Seq: 2, Role: "assistant", Kind: proto.KindText, Text: "正在检查导出图片的路径。"}}
	d := newFleetDevs()
	d.add("mac", "Mac")
	st := newFleetMem()
	ft := &fleetTest{t: t, s: New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), Devices: d}), d: d, f: f, st: st}
	m, ok := ft.s.fleetMachine("mac")
	if !ok {
		t.Fatal("no machine")
	}
	ft.sessions("mac",
		proto.Session{SessionID: "custom", Title: "我选的名字", TitleSource: proto.TitleSourceManual, TitleManual: true, Cwd: "/work/example-project", Status: proto.StatusIdle},
		proto.Session{SessionID: "own", Title: "Persona", TitleSource: proto.TitleSourcePrompt, Cwd: m.assistantCwd(), Status: proto.StatusIdle})
	titleOf := func(id string) (proto.Session, bool) {
		for _, c := range ft.lastList("mac") {
			if c.SessionID == id {
				return c, true
			}
		}
		return proto.Session{}, false
	}
	if c, ok := titleOf("custom"); !ok || c.Title != "我选的名字" || c.TitleSource != proto.TitleSourceManual {
		t.Fatalf("list before: %+v %v", c, ok)
	}
	if status, body := autoname(t, ft.s, roleFull, "mac", "own"); status != http.StatusBadRequest || body["code"] != "assistant_session" {
		t.Fatalf("assistant conversation: %d %v", status, body)
	}
	if status, body := autoname(t, ft.s, roleFull, "nowhere", "custom"); status != http.StatusNotFound || body["code"] != "not_found" {
		t.Fatalf("unknown machine: %d %v", status, body)
	}
	if status, body := autoname(t, ft.s, roleFull, "mac", "custom"); status != http.StatusOK || body["title"] != "修复 PDF 导出图片" || body["changed"] != true {
		t.Fatalf("answer: %d %v", status, body)
	}
	// No waiting: the list was told before the answer.
	c, ok := titleOf("custom")
	if !ok || c.Title != "修复 PDF 导出图片" || c.TitleSource != proto.TitleSourceAuto || c.TitleManual {
		t.Fatalf("list after: title=%q source=%q manual=%v found=%v", c.Title, c.TitleSource, c.TitleManual, ok)
	}
	if own, _ := titleOf("own"); own.Title == "修复 PDF 导出图片" {
		t.Fatal("the assistant's conversation was named")
	}
	for _, call := range d.callsTo("mac", assistantwire.DeviceSessionRename) {
		t.Fatalf("a requested name was sent to the node: %s", call)
	}
}
