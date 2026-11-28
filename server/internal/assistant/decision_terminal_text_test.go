package assistant

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Old decisions can still have a terminal action among the choices. New reviews reject such options.
func setLegacyResumeOption(s *Service, id string, index int, label string) {
	s.accept.mu.Lock()
	defer s.accept.mu.Unlock()
	a := s.accept.asks[id]
	a.Options[index].Label = label
	if index == 0 {
		a.Recommend = label
	}
}

func TestTerminalResumeLabelsInDecisionOptions(t *testing.T) {
	for _, tc := range []struct {
		label  string
		manual bool
	}{
		{legacyTerminalOption, true},
		{"Resume on a terminal page", true},
		{"Open terminal and resume release-42", false},
		{"Open terminal and resume Agent", true},
		{"Manually resume on its terminal page", true},
		{"Use terminal Resume", true},
		{"Resume the session in the terminal", true},
		{"Open the terminal and resume the session", true},
		{"在终端上恢复 Agent", true},
		{"在终端点 Resume", true},
		{"ターミナルでエージェントを再開", true},
		{"ターミナルで Resume を押す", true},
		{"Do not resume on the terminal; continue in this conversation.", false},
		{"Resume the rollout without opening a terminal.", false},
		{"Open terminal and resume the build", false},
		{"Resume debugging in the terminal", false},
		{"Open terminal and resume compilation", false},
		{"不在终端恢复，启动新 Agent", false},
		{"放弃终端恢复，改开替代方案", false},
		{"恢复终端连接测试", false},
		{"在终端恢复连接测试", false},
		{"ターミナルで再開せず新規エージェントを起動", false},
		{"ターミナル設定を戻してビルドを再開", false},
	} {
		if got := terminalResumeLabel(tc.label, nil); got != tc.manual {
			t.Errorf("terminalResumeLabel(%q) = %v, want %v", tc.label, got, tc.manual)
		}
	}
}

func TestTerminalResumeLegacyBindingInEveryLanguage(t *testing.T) {
	now := time.Now()
	task := Todo{Agents: []TodoAgent{{MachineID: "box", SessionID: "workerid-original"}}}
	for _, label := range []string{"Resume workerid on its terminal page", "Resume Agent workerid on its terminal page", "在终端上恢复 Agent workerid", "ターミナルでエージェント workerid を再開"} {
		if ref := decisionTerminal(task, label, now); ref == nil || ref.SessionID != task.Agents[0].SessionID {
			t.Errorf("no link to the session named by %q: %+v", label, ref)
		}
	}
	task.Agents[0].SessionID = "deadbeef-original"
	for _, label := range []string{"Resume deadbeef on its terminal page", "在终端恢复 deadbeef", "ターミナルで deadbeef を再開"} {
		if ref := decisionTerminal(task, label, now); ref == nil || ref.SessionID != task.Agents[0].SessionID {
			t.Errorf("no link to the session named by %q: %+v", label, ref)
		}
	}
}

func TestTerminalResumeProductInstructionsAcrossAnswersAndDeadline(t *testing.T) {
	labels := []string{"Resume on a terminal page", "Resume workerid on its terminal page"}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		// The node explains the refusal, then gives the instruction. The card gives the instruction first.
		sentences := func(key string) []string {
			return strings.FieldsFunc(i18n.T(lang, key), func(r rune) bool { return r == '.' || r == '。' })
		}
		refusal := sentences("rpc-errors.resume_not_allowed")
		labels = append(labels, strings.TrimSpace(refusal[len(refusal)-1]), sentences("assistant-reports.manual_terminal_resume")[0])
	}
	for _, label := range labels {
		t.Run(label, func(t *testing.T) {
			st := todoTestDB(t)
			s, f, todo := newDecisionAnswerServiceOn(t, st)
			waitsForReview(t, s, f, todo.ID, "Choose the next step.")
			if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
				t.Fatal(err)
			}
			// Keep the live recipient in the fixture. Old choices can name another bound session by its first eight letters.
			s.todos.mu.Lock()
			task := s.todos.todos[todo.ID]
			task.Agents = append(task.Agents, TodoAgent{MachineID: "box", SessionID: "workerid-original"})
			s.todos.mu.Unlock()
			setLegacyResumeOption(s, todo.ID, 0, label)
			a := askOf(s, todo.ID)
			round := a.AskedAt.Format(timeKeyLayout)
			sent := len(sentToAgent(f))
			if row := s.todoAsks(t.Context())[todo.ID]; row.Default != defaultNever {
				t.Fatalf("advertised executable default %q", row.Default)
			}
			if _, err := s.acceptTry(t.Context(), todo.ID, ""); !manualResumeRefused(err) {
				t.Fatalf("accepting the recommendation: %v", err)
			}
			if _, err := s.chooseOption(t.Context(), todo.ID, round, label); !manualResumeRefused(err) {
				t.Fatalf("choosing the option: %v", err)
			}
			if _, err := s.answerDecisionText(t.Context(), todo.ID, round, "“"+label+"”"); !manualResumeRefused(err) {
				t.Fatalf("quoting the option: %v", err)
			}
			pastDeadline(t, s, todo.ID)
			for range 3 {
				s.sweepTaskDeadlines(time.Now())
			}
			cur := askOf(s, todo.ID)
			rows, err := st.ListTodoDecisions(t.Context(), todo.ID, true)
			if !cur.AcceptedAt.IsZero() || cur.DueAt.IsZero() || cur.ByDefault || len(sentToAgent(f)) != sent || err != nil || len(rows) != 0 {
				t.Fatalf("legacy terminal option settled or sent: ask=%+v decisions=%v err=%v sends=%d", cur, rows, err, len(sentToAgent(f))-sent)
			}
		})
	}
}

func TestTerminalResumeUnboundReleaseNameKeepsDefault(t *testing.T) {
	st := todoTestDB(t)
	s, f, todo := newDecisionAnswerServiceOn(t, st)
	waitsForReview(t, s, f, todo.ID, "Choose the next step.")
	const release = "Open terminal and resume release-42"
	args := rolloutDecision()
	args["recommend"] = release
	args["options"].([]map[string]string)[0]["label"] = release
	if _, err := reviewErr(s, todo.ID, args); err != nil {
		t.Fatalf("refused the supervisor's release option: %v", err)
	}
	before := len(sentToAgent(f))
	pastDeadline(t, s, todo.ID)
	s.sweepTaskDeadlines(time.Now())
	rows, err := st.ListTodoDecisions(t.Context(), todo.ID, true)
	if err != nil || len(rows) != 1 || rows[0].Choice != release || !askOf(s, todo.ID).ByDefault || len(sentToAgent(f)) != before+1 {
		t.Fatalf("release decision did not execute: rows=%+v err=%v sends=%d", rows, err, len(sentToAgent(f))-before)
	}
}

func TestTerminalResumeTextAnswerKeepsDecisionOpen(t *testing.T) {
	st := todoTestDB(t)
	s, f, todo := newDecisionAnswerServiceOn(t, st)
	waitsForReview(t, s, f, todo.ID, "Choose the next step.")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	a := askOf(s, todo.ID)
	round := a.AskedAt.Format(timeKeyLayout)
	calls := f.callCount()
	for _, label := range []string{legacyTerminalOption, "在终端点 Resume", "ターミナルで Resume を押す"} {
		setLegacyResumeOption(s, todo.ID, 1, label)
		reply := "“" + strings.ToLower(label) + "”."
		answer, _ := json.Marshal(map[string]string{"text": reply})
		web := answerTodoAsk(s, todo.ID, askDecisionPrefix+round, string(answer))
		var refused struct{ Code, Delivered string }
		if err := json.Unmarshal(web.Body.Bytes(), &refused); err != nil || refused.Code != "accept_not_taken" || refused.Delivered != "none" {
			t.Fatalf("to-do answer %q: %d %s", reply, web.Code, web.Body.String())
		}
		if _, err := s.routeDecisionAnswer(t.Context(), todo.Origin, todo.ID, round, reply); !manualResumeRefused(err) {
			t.Fatalf("routed answer %q: %v", reply, err)
		}
		body, _ := json.Marshal(batchDecisionRequest{Items: []batchDecisionItem{{TodoID: todo.ID, Reply: reply}}})
		w := batchDecisionsRequest(s, "full", string(body))
		var batch batchDecisionResponse
		if err := json.Unmarshal(w.Body.Bytes(), &batch); err != nil || batch.Failed != 1 || len(batch.Results) != 1 ||
			batch.Results[0].Error != i18n.T(s.requestCtxLang(t.Context()), "assistant-reports.manual_terminal_resume") {
			t.Fatalf("batch reply %q: %d %s", reply, w.Code, w.Body.String())
		}
	}
	if got := askOf(s, todo.ID); !got.AcceptedAt.IsZero() || !got.DueAt.IsZero() || f.callCount() != calls {
		t.Fatalf("text answer changed the decision or reached a device: %+v", got)
	}
	if rows, err := st.ListTodoDecisions(t.Context(), todo.ID, false); err != nil || len(rows) != 0 {
		t.Fatalf("refused text written: %+v, %v", rows, err)
	}

	// Mentioning the same action in the owner's own words remains a valid answer.
	text := "Resume the rollout without opening a terminal."
	if result, err := s.answerDecisionText(t.Context(), todo.ID, round, text); err != nil || !result.taken {
		t.Fatalf("free text answer: %+v, %v", result, err)
	}
}

func TestTerminalResumeFeishuRepliesKeepDecisionOpen(t *testing.T) {
	needsLarkLayer(t)
	s, f, todo, _, notes := conclusionService(t, false)
	waitsForReview(t, s, f, todo.ID, "Choose the next step.")
	if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
		t.Fatal(err)
	}
	// A question mark written into an option is part of its label when the owner repeats it verbatim.
	const label = legacyTerminalOption + "?"
	setLegacyResumeOption(s, todo.ID, 0, label)
	sent := len(sentToAgent(f))
	beforeSupervisor := sendOnceText(f)
	a := askOf(s, todo.ID)
	for _, msg := range []struct{ id, reply string }{{"om_letter", "A"}, {"om_verbatim", label}, {"om_quoted", "“" + label + "”"}} {
		n := len(notes())
		feishuSay(t, s, f, nil, p2pText(msg.id, msg.reply, "", a.MessageID), "")
		if got := askOf(s, todo.ID); !got.AcceptedAt.IsZero() || !got.DueAt.IsZero() || len(sentToAgent(f)) != sent || sendOnceText(f) != beforeSupervisor {
			t.Fatalf("reply %q forwarded or settled the terminal choice: %+v", msg.reply, got)
		}
		if after := notes(); len(after) != n+1 || after[n] != i18n.T(s.botLang(t.Context()), "assistant-reports.manual_terminal_resume") {
			t.Fatalf("reply %q: missing manual-resume explanation: %v", msg.reply, after)
		}
	}
	// Without a question mark in the displayed label, asking about it is a question for the supervisor.
	setLegacyResumeOption(s, todo.ID, 0, legacyTerminalOption)
	feishuSay(t, s, f, nil, p2pText("om_question", label, "", a.MessageID), "")
	if got := sendOnceText(f); !strings.Contains(got, label) || !askOf(s, todo.ID).AcceptedAt.IsZero() {
		t.Fatalf("a genuine question did not reach the supervisor with the decision open: %q", got)
	}
	setLegacyResumeOption(s, todo.ID, 0, label)
	// An already delivered choice records the owner's actual words without a second send.
	if taken, err := s.decisionTold(t.Context(), todo.ID, label, "box", "conv1", label); err != nil || !taken || len(sentToAgent(f)) != sent {
		t.Fatalf("already relayed choice: taken %v, error %v", taken, err)
	}
}

func TestTerminalResumeFeishuReplyQueuedBehindSettlement(t *testing.T) {
	needsLarkLayer(t)
	for _, state := range []string{"superseded", "closed", "recheck", "returned"} {
		t.Run(state, func(t *testing.T) {
			s, f, todo, _, notes := conclusionService(t, false)
			waitsForReview(t, s, f, todo.ID, "Choose the next step.")
			if _, err := reviewErr(s, todo.ID, rolloutDecision()); err != nil {
				t.Fatal(err)
			}
			setLegacyResumeOption(s, todo.ID, 0, legacyTerminalOption)
			a := askOf(s, todo.ID)
			before := len(notes())
			recognized := make(chan bool, 1)
			func() {
				actions := &s.reviewDismissals.actions
				actions.acquire(todo.ID)
				defer actions.release(todo.ID)
				go func() {
					recognized <- s.acceptReply(t.Context(), "om_racing", legacyTerminalOption, false, a.MessageID)
				}()
				waitFor(t, "Feishu reply waiting for the task lock", func() bool { return actions.waiting(todo.ID) == 2 })
				at := time.Now().UTC()
				switch state {
				case "superseded":
					s.supersedeDecision(t.Context(), todo.ID, "Already settled.")
				case "closed":
					s.todos.mu.Lock()
					s.todos.todos[todo.ID].DoneAt = &at
					s.todos.mu.Unlock()
				case "recheck":
					s.accept.mu.Lock()
					s.accept.asks[todo.ID].RecheckAt = at
					s.accept.mu.Unlock()
				case "returned":
					s.todos.mu.Lock()
					reviews := s.todos.todos[todo.ID].Supervision.Reviews
					reviews[len(reviews)-1].RecheckAt = at
					s.todos.mu.Unlock()
				}
			}()
			select {
			case got := <-recognized:
				after := notes()
				if !got || len(after) != before+1 || after[before] != i18n.T(s.botLang(t.Context()), "assistant-feishu_cards.already_handled") {
					t.Fatalf("expected an already-handled reply: recognized=%v notes=%v", got, after)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("reply did not finish after the competing action")
			}
		})
	}
}

func TestTerminalResumeTextOnOutsideDecisionReturnsToItsSource(t *testing.T) {
	e := newTodoEnv(t, nil)
	in := outsideFixture()
	in.Options[1].Label, in.Recommend = legacyTerminalOption, legacyTerminalOption
	d := raiseOutside(t, e, "release-script", in)
	a := askOf(e.s, d.AskTodoID)
	if result, err := e.s.answerDecisionText(t.Context(), d.AskTodoID, a.AskedAt.Format(timeKeyLayout), legacyTerminalOption); !result.taken || err != nil {
		t.Fatalf("outside text answer: %+v, %v", result, err)
	}
	if got := pollOutside(t, e, "release-script", d.ID); got.Answer == nil || got.Answer.Text != legacyTerminalOption {
		t.Fatalf("source did not receive the owner's words: %+v", got.Answer)
	}
}
