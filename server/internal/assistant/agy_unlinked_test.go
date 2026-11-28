package assistant

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const testUnlinkedReason = "assistant-tasks.check_agy_unlinked"

func unlinkedPaneFixture(t *testing.T, locale string) (*Service, *fakeHub, Todo, proto.Session) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.hello.Locale = locale
	f.settings = &Settings{Main: "conv1"}
	f.known["conv1"] = true
	f.setSessions("server", proto.Session{SessionID: "conv1", Assistant: true})
	s := newServiceOn(t, f, nil)
	linked := time.Now().UTC().Add(-time.Hour)
	pane := proto.Session{SessionID: "agy-w1-p1", Agent: "agy", PaneID: "w1:p1", Cwd: "/code/app", Status: proto.StatusIdle, UpdatedAt: linked}
	todo := Todo{ID: "task", Title: "Check the app", Origin: "conv1", CreatedAt: linked,
		Supervision: &Supervision{Supervisor: "conv1"},
		Agents:      []TodoAgent{{MachineID: "box", SessionID: pane.SessionID, LinkedAt: linked, Cwd: pane.Cwd, Agent: pane.Agent}}}
	putTodo(s, todo)
	f.setSessions("box", pane)
	f.setItems(proto.Item{ID: "agy#0", Seq: 1, Role: "system", Kind: proto.KindNotice, Text: "Transcript unavailable"})
	return s, f, todo, pane
}

// Render through the same last-mile recipient check used by supervisor delivery.
func unlinkedSupervisorMessage(t *testing.T, s *Service, todoID string) string {
	t.Helper()
	s.supervise.mu.Lock()
	g := s.supervise.pending[todoID+"|"+sigUnjudged]
	s.supervise.mu.Unlock()
	message := s.agyRelayText(context.Background(), "server", "conv1", relayMsg{
		Tool: superviseTool, Text: g.Detail, Signals: encodeDigestSignals([]superviseSignal{g}),
	})
	var block struct {
		XMLName xml.Name `xml:"persona-context"`
		Detail  string   `xml:",chardata"`
	}
	if err := xml.Unmarshal([]byte(message), &block); err != nil {
		t.Fatalf("supervisor received no diagnostic context: %v, %q", err, message)
	}
	return strings.TrimSpace(block.Detail)
}

func TestAgyUnlinkedKeepsOrdinaryNoReply(t *testing.T) {
	for _, tc := range []struct {
		name, agent string
		item        proto.Item
	}{
		{"empty history", "agy", proto.Item{}},
		{"bound pane awaiting its first reply", "agy", proto.Item{Role: "user", Kind: proto.KindText, Text: "Check the app"}},
		{"another notice", "agy", proto.Item{ID: "agy#1", Role: "system", Kind: proto.KindNotice}},
		{"Claude", "claude", proto.Item{}},
		{"Codex", "codex", proto.Item{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo, pane := unlinkedPaneFixture(t, "en")
			pane.Agent = tc.agent
			todo.Agents[0].Agent = tc.agent
			putTodo(s, todo)
			f.setSessions("box", pane)
			if tc.item.Kind == "" {
				f.setItems()
			} else {
				f.setItems(tc.item)
			}
			c, err := s.judgeTaskDone(context.Background(), todo)
			if err != nil || c.Verdict != verdictUnsure || c.reasonKey != checkReasonNoReply {
				t.Fatalf("ordinary no-reply check = %+v, error %v", c, err)
			}
		})
	}
}

func TestAgyUnlinkedCandidates(t *testing.T) {
	s, f, todo, pane := unlinkedPaneFixture(t, "en")
	pane.LastText = "A previous conversation finished the task."
	linked := todo.Agents[0].LinkedAt
	row := func(id string, after time.Duration) proto.Session {
		return proto.Session{SessionID: id, Agent: "agy", SourceKind: proto.SourceRuntime, SourceInstanceID: "agy",
			Cwd: pane.Cwd, Status: proto.StatusDone, UpdatedAt: linked.Add(after)}
	}

	hidden := row("agy-hidden", time.Minute)
	hidden.SetHiddenByDirectory(true)
	otherSpace := row("agy-other-space", time.Minute)
	otherSpace.Space = "health"
	otherDir := row("agy-other-dir", time.Minute)
	otherDir.Cwd = "/code/other"
	anotherPane := row("agy-terminal", time.Minute)
	anotherPane.PaneID = "w2:p1"
	codex := row("codex-runtime", time.Minute)
	codex.Agent = "codex"
	aliased := row("agy-already-assigned", time.Minute)
	aliased.SetAliases([]string{"agy-old-pane"})
	putTodo(s, Todo{ID: "other", Title: "Another task", Origin: "conv1", CreatedAt: linked, Agents: []TodoAgent{
		{MachineID: "box", SessionID: "agy-old-pane"}, {MachineID: "box", SessionID: "agy-taken"},
	}})
	f.setSessions("other-box", row("agy-other-computer", time.Minute))
	f.setSessions("box", pane, row("agy-oldest", time.Second), row("agy-latest", 4*time.Second),
		row("agy-third", 2*time.Second), row("agy-second", 3*time.Second), row("agy-predates-link", -time.Second),
		row("agy-taken", time.Minute), hidden, otherSpace, otherDir, anotherPane, aliased, codex)
	var logs logBuffer
	s.logf = logs.logf

	ctx := context.Background()
	c, err := s.judgeTaskDone(ctx, todo)
	if err != nil || c.reasonKey != testUnlinkedReason || c.Verdict != verdictUnsure {
		t.Fatalf("unlinked agy pane = %+v, error %v", c, err)
	}
	short := i18n.T(i18n.En, testUnlinkedReason)
	if c.Reason != short || checkOf(i18n.En, c, linked, time.Now()).Reason != short {
		t.Fatalf("manual and stored checks should both say %q, got %+v", short, c)
	}
	s.superviseJudged(ctx, todo, linked, c, nil)
	detail := unlinkedSupervisorMessage(t, s, todo.ID)
	now := time.Now().UTC()
	todo.DoneAt = &now
	s.recordTaskOutcome(todo, now)
	for name, line := range map[string]string{"signal": detail, "log": logs.String()} {
		if !strings.Contains(line, pane.SessionID) {
			t.Fatalf("%s does not name the unreadable pane: %q", name, line)
		}
		prev := -1
		for _, want := range []string{"agy-latest", "agy-second", "agy-third"} {
			pos := strings.Index(line, want)
			if pos <= prev {
				t.Fatalf("%s: missing or misordered candidate %s in %q", name, want, line)
			}
			prev = pos
		}
		for _, skip := range []string{"agy-oldest", "agy-taken", aliased.SessionID, hidden.SessionID, otherSpace.SessionID,
			otherDir.SessionID, anotherPane.SessionID, codex.SessionID, "agy-predates-link", "agy-other-computer"} {
			if strings.Contains(line, skip) {
				t.Fatalf("%s names ineligible session %s: %q", name, skip, line)
			}
		}
	}
	if !strings.Contains(logs.String(), "agy_unlinked") || len(todoNow(s, todo.ID).Agents) != 1 {
		t.Fatalf("unexpected task or log after diagnosis: %+v, %s", todoNow(s, todo.ID), logs.String())
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, call := range f.calls {
		if call.Method != DeviceSessionItems || call.Device == "server" {
			continue
		}
		var args struct {
			SessionID string `json:"session_id"`
		}
		if err := json.Unmarshal(call.Params, &args); err != nil || args.SessionID != pane.SessionID {
			t.Fatalf("unexpected transcript read: %s, %v", call.Params, err)
		}
	}
}

func TestGoldenAgyUnlinked(t *testing.T) {
	for _, lang := range []string{"en", "zh-Hans", "ja"} {
		t.Run(lang, func(t *testing.T) {
			s, f, todo, pane := unlinkedPaneFixture(t, lang)
			ctx := context.Background()
			check := func(work time.Time) string {
				t.Helper()
				c, err := s.judgeTaskDone(ctx, todo)
				if err != nil || c.reasonKey != testUnlinkedReason {
					t.Fatalf("missing unlinked reason: %+v, %v", c, err)
				}
				s.superviseJudged(ctx, todo, work, c, nil)
				detail := strings.TrimSpace(unlinkedSupervisorMessage(t, s, todo.ID))
				if !strings.Contains(detail, pane.SessionID) {
					t.Fatalf("missing pane in %q", detail)
				}
				return c.Reason + "\n" + detail
			}
			noCandidates := check(todo.CreatedAt)
			row := proto.Session{SessionID: "agy-conversation", Agent: "agy", SourceKind: proto.SourceRuntime, SourceInstanceID: "agy",
				Cwd: pane.Cwd, UpdatedAt: todo.CreatedAt.Add(time.Minute)}
			f.setSessions("box", pane, row)
			oneCandidate := check(todo.CreatedAt.Add(time.Second))
			if !strings.Contains(oneCandidate, row.SessionID) {
				t.Fatalf("missing the one eligible candidate in %q", oneCandidate)
			}
			golden(t, "agy_unlinked."+lang, noCandidates+"\n\n"+oneCandidate+"\n\n"+agyUnavailableGolden(t, s, f, todo, pane)+"\n")
		})
	}
}

// A missing pane transcript cannot overrule known evidence about another linked agent.
func TestAgyUnlinkedWithAnotherAgent(t *testing.T) {
	for _, tc := range []struct {
		name, role, text, verdict, reasonKey string
	}{
		{"actual reply", "assistant", "The remaining step is documented.", verdictNotDone, ""},
		{"awaiting a reply", "user", "Please check one more file.", verdictUnsure, checkReasonNoReply},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, todo, pane := unlinkedPaneFixture(t, "en")
			pane.LastText = "Stale text from the previous conversation."
			other := proto.Session{SessionID: "codex-other", Agent: "codex", Cwd: pane.Cwd}
			todo.Agents = append(todo.Agents, TodoAgent{MachineID: "box", SessionID: other.SessionID, LinkedAt: todo.CreatedAt, Agent: other.Agent})
			putTodo(s, todo)
			f.setSessions("box", pane, other)
			f.judge = `{"verdict":"not_done","reason":"One step remains."}`
			f.answerNode(DeviceSessionItems, func(_ string, raw json.RawMessage) (any, error) {
				var params struct {
					SessionID string `json:"session_id"`
				}
				if err := json.Unmarshal(raw, &params); err != nil {
					return nil, err
				}
				item := proto.Item{ID: "agy#0", Role: "system", Kind: proto.KindNotice}
				if params.SessionID == other.SessionID {
					item = proto.Item{ID: "codex-item", Role: tc.role, Kind: proto.KindText, Text: tc.text}
				}
				return map[string]any{"items": []proto.Item{item}}, nil
			})
			got, err := s.judgeTaskDone(context.Background(), todo)
			if err != nil || got.Verdict != tc.verdict || got.reasonKey != tc.reasonKey {
				t.Fatalf("judgment = %+v, error %v", got, err)
			}
			var logs logBuffer
			s.logf = logs.logf
			out, _ := s.outcomeAgents(context.Background(), todo)
			if tc.role == "assistant" && (len(out) != 1 || out[0].Text != tc.text) || tc.role == "user" && len(out) != 0 {
				t.Fatalf("unexpected mixed-agent result: %+v", out)
			}
			s.recordTaskOutcome(todo, time.Now().UTC())
			if line := logs.String(); strings.Contains(line, "agy_unlinked") || tc.role == "user" && !strings.Contains(line, "no agent reply to read") {
				t.Fatalf("mixed-agent outcome log: %s", line)
			}
		})
	}
}

func TestAgyUnlinkedJudgmentReplaysAfterRestart(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f := newQuietServiceOn(t, database)
	ctx := context.Background()
	todo, err := s.createTodo(ctx, "Check the app", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.superviseTodo(ctx, todo.ID, "conv1", nil, 0); err != nil {
		t.Fatal(err)
	}
	pane := proto.Session{SessionID: "agy-w1-p1", MachineID: "box", Agent: "agy", PaneID: "w1:p1", Cwd: "/code/app", Status: proto.StatusIdle, Rev: 1, UpdatedAt: time.Now().UTC()}
	if err := s.linkTodoAgent(ctx, todo.ID, "box", pane.SessionID, todo.Title, pane.Cwd, pane.Agent); err != nil {
		t.Fatal(err)
	}
	todo = todoNow(s, todo.ID)
	candidate := proto.Session{SessionID: "agy-conversation", Agent: "agy", SourceKind: proto.SourceRuntime,
		SourceInstanceID: "agy", Cwd: pane.Cwd, UpdatedAt: time.Now().UTC()}
	f.setSessions("box", pane, candidate)
	f.setItems(proto.Item{ID: "agy#0", Role: "system", Kind: proto.KindNotice})
	s.takeTurn(ctx, todo, pane, nil, turnClose{}, false, todo.CreatedAt, turnDone)
	check, err := s.judgeTaskDone(ctx, todo)
	if err != nil || check.reasonKey != testUnlinkedReason {
		t.Fatalf("before restart: %+v, error %v", check, err)
	}
	s.keepTaskCheck(ctx, todo, todo.CreatedAt, check)
	s.turnJudged(ctx, todo.ID, check, nil, "")

	// The process stopped after the judgment write, before a signal was registered.
	// The sweep must recover it without a new start, using the current inventory.
	again, hub := restartedOn(t, database)
	current := candidate
	current.SessionID = "agy-current-candidate"
	hub.setSessions("box", pane, current)
	hub.setItems(proto.Item{ID: "agy#0", Role: "system", Kind: proto.KindNotice})
	if err := again.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	saved, ok := again.turnOf(ctx, todo.ID)
	if !ok || saved.Judged == nil || saved.Signal != nil {
		t.Fatalf("expected an undelivered judgment from storage: %+v", saved)
	}
	sweeps(t, again, todo.ID, 1)
	countNotices := func() int {
		n := 0
		for _, notice := range hub.notices() {
			if strings.Contains(notice, "signal=unjudged") {
				n++
			}
		}
		return n
	}
	waitFor(t, "the recovered supervisor notice", func() bool { return countNotices() > 0 })
	notice := strings.Join(hub.notices(), "\n")
	if !strings.Contains(notice, pane.SessionID) || !strings.Contains(notice, current.SessionID) || strings.Contains(notice, candidate.SessionID) {
		t.Fatalf("unexpected recovered pane or candidates: %q", notice)
	}
	sweeps(t, again, todo.ID, 2)
	if turn, _ := again.turnOf(ctx, todo.ID); countNotices() != 1 || turn.Signal == nil || turn.Signal.Told == nil {
		t.Fatalf("signal was not delivered exactly once: %+v, %v", turn.Signal, hub.notices())
	}
}

func TestAgyUnlinkedManualCheckLanguage(t *testing.T) {
	s, f, todo, pane := unlinkedPaneFixture(t, "en")
	row := proto.Session{SessionID: "agy-candidate", Agent: "agy", SourceKind: proto.SourceRuntime,
		Cwd: pane.Cwd, UpdatedAt: time.Now()}
	f.setSessions("box", pane, row)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/todos/"+todo.ID+"/done-check", strings.NewReader(`{"apply":true}`))
	r.Header.Set(RoleHeader, "full")
	r.Header.Set("Accept-Language", "ja")
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	var public struct {
		TodoCheck
		Applied bool `json:"applied"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &public) != nil || !public.Applied ||
		public.Verdict != verdictUnsure || public.Reason != i18n.T(i18n.Ja, testUnlinkedReason) || strings.Contains(w.Body.String(), row.SessionID) {
		t.Fatalf("manual check: %d %s", w.Code, w.Body.String())
	}
	if stored := todoNow(s, todo.ID).Check; stored == nil || stored.Reason != i18n.T(i18n.En, testUnlinkedReason) {
		t.Fatalf("stored check did not retain the owner’s language: %+v", stored)
	}
}
