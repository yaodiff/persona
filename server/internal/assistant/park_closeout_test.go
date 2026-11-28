package assistant

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

func automaticCloseout(waiting, artifact string, evidence map[string]any) string {
	body := "<closeout status=\"blocked\" merged=\"no\" deployed=\"no\">\nwaiting: " + waiting + "\n"
	if artifact != "" {
		body += "artifact: " + artifact + "\n"
	}
	if evidence != nil {
		raw, _ := json.Marshal(evidence)
		body += "park: " + string(raw) + "\n"
	}
	return body + "</closeout>"
}

// parkEvidenceWithoutNextActor is a park line written without evidence.next_actor.
func parkEvidenceWithoutNextActor(kind string) map[string]any {
	a := parkEvidenceArgs(kind)
	delete(a["evidence"].(map[string]any), "next_actor")
	return a
}

func automaticFixture(t *testing.T, reply string) (*Service, *fakeHub, Todo, proto.Session, time.Time) {
	t.Helper()
	s, f, task, c, _ := mechanicalFixture(t)
	holdDigest(s)
	now := time.Now().UTC().Truncate(time.Second)
	c.UpdatedAt, c.LastText = now.Add(-time.Hour), reply
	f.setSessions("box", c)
	task.Delivery = nil
	task.Origin, task.PRURL = "conv1", ""
	task.Supervision.UpdatedAt = now.Add(-2 * time.Hour)
	s.todos.mu.Lock()
	copy := copyTodoLocked(&task)
	s.todos.todos[task.ID] = &copy
	s.todos.mu.Unlock()
	return s, f, task, c, now
}

// These tests use existing entry points and serialized metadata, so baseline production still compiles.
func TestAutomaticCloseoutParkingAdmission(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tc := range []struct {
		name, reply string
		want        bool
	}{
		{"modern work report", `### Persona work report

- Status: blocked
- Merged: no
- Deployed: no
- Artifact: build https://example.org/deployments/rollout-42
- Waiting: deployment rollout-42

---`, true},
		{"named deployment", automaticCloseout("deployment rollout-42", "build https://example.org/deployments/rollout-42", nil), true},
		{"named natural event", automaticCloseout("external event https://example.org/events/result-42", "", nil), true},
		{"Chinese deployment", automaticCloseout("等待部署 rollout-42", "build https://example.org/deployments/rollout-42", nil), true},
		{"explicit date", automaticCloseout("the opening window on "+now.Add(48*time.Hour).Format(time.RFC3339), "", nil), true},
		{"sent person request", automaticCloseout("reviewer-a replies in the sent thread", "", parkEvidenceArgs("person_reply")), true},
		{"ready source-bound review", automaticCloseout("reviewer-a completes the requested review", "", parkEvidenceArgs("merge_review")), true},
		{"reuse recorded review source", automaticCloseout("等上游复审", "pr https://github.com/example/project/pull/42", nil), true},
		{"recorded review cannot hide deployment", automaticCloseout("deployment rollout-42", "pr https://github.com/example/project/pull/42", nil), false},
		{"missing source", automaticCloseout("wait for deployment", "", nil), false},
		{"unrelated build source", automaticCloseout("deployment rollout-43", "build https://example.org/deployments/rollout-42", nil), false},
		{"unobserved PR source", automaticCloseout("wait for upstream review", "pr https://github.com/example/project/pull/42", nil), false},
		{"unrecorded natural event", automaticCloseout("wait for a natural system message", "", nil), false},
		{"unrecorded person", automaticCloseout("wait for reviewer-a to reply", "", nil), false},
		{"owner decision", automaticCloseout("user approval for deployment", "", parkEvidenceArgs("external_event")), false},
		{"internal work", automaticCloseout("finish local tests", "url https://example.org/tests/pending", nil), false},
		{"past date", automaticCloseout("the window on "+now.Add(-time.Hour).Format(time.RFC3339), "", nil), false},
		{"done turn with external delivery wait", strings.ReplaceAll(automaticCloseout("deployment rollout-42", "build https://example.org/deployments/rollout-42", nil), `status="blocked"`, `status="done"`), true},
		{"no structured closeout", "Delivery: PR; blocked on deployment; someone else can unblock it", false},
		{"bad explicit evidence cannot fall back", automaticCloseout("deployment rollout-42", "build https://example.org/deployments/rollout-42", map[string]any{"waiting_on": "external_event"}), false},
		// Only evidence derived from the closeout gets next_actor; what an Agent or an older receipt states keeps its own.
		{"explicit evidence without next actor", automaticCloseout("reviewer-a replies in the sent thread", "", parkEvidenceWithoutNextActor("person_reply")), false},
		{"recorded review without next actor", automaticCloseout("等上游复审", "pr https://github.com/example/project/pull/42", nil), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, task, _, at := automaticFixture(t, tc.reply)
			if tc.name == "ready source-bound review" || tc.name == "reuse recorded review source" || tc.name == "recorded review cannot hide deployment" ||
				tc.name == "recorded review without next actor" {
				s.todos.mu.Lock()
				cur := s.todos.todos[task.ID]
				cur.PRURL = "https://github.com/example/project/pull/42"
				cur.PRStatus = &TodoPRStatus{URL: cur.PRURL, State: "open", HeadOID: "head-1", CheckedAt: at}
				if tc.name != "ready source-bound review" {
					raw, _ := json.Marshal(parkEvidenceArgs("merge_review")["evidence"])
					var evidence ParkEvidence
					_ = json.Unmarshal(raw, &evidence)
					if tc.name == "recorded review without next actor" {
						evidence.NextActor = "" // recorded before admission required it
					}
					cur.Supervision.Reviews = []TaskReview{{At: at.Add(-time.Hour), Verdict: "parked",
						WaitingOn: "someone_else", Until: at.Add(24 * time.Hour), Evidence: &evidence}}
				}
				s.todos.mu.Unlock()
			}
			g := superviseSignal{Todo: task.ID, Kind: "idle"}
			kept := s.screenMechanicalSignals(context.Background(), []superviseSignal{g}, at, at.Add(-time.Hour))
			got := todoNow(s, task.ID)
			if parked := got.Supervision.State == "parked"; parked != tc.want || (len(kept) == 0) != tc.want {
				t.Fatalf("parked=%v, remaining idle=%d, want automatic=%v; ledger=%+v", parked, len(kept), tc.want, got.Supervision)
			}
			if got.DoneAt != nil {
				t.Fatal("parking accepted or completed unfinished work")
			}
			if tc.want {
				p := got.Supervision.parked()
				if p == nil || p.Evidence == nil || !p.Until.After(at) {
					t.Fatalf("missing admitted evidence/deadline: %+v", p)
				}
				for _, signal := range watchSignals(got, nil, at) {
					if signal.Kind == "idle" || signal.Kind == "all_stopped" {
						t.Fatalf("parked task still raised %s", signal.Kind)
					}
				}
				due := watchSignals(got, nil, p.Until.Add(time.Second))
				if len(due) != 1 || due[0].Kind != "parked_due" {
					t.Fatalf("existing deadline did not wake automatic park: %+v", due)
				}
			}
		})
	}
}

func automaticMetadata(t *testing.T, v *Supervision) map[string]any {
	t.Helper()
	if v == nil || len(v.Reviews) == 0 {
		t.Fatal("automatic parking did not persist a review")
	}
	raw, err := json.Marshal(v.Reviews[len(v.Reviews)-1])
	if err != nil {
		t.Fatal(err)
	}
	var r map[string]any
	if err := json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	a, ok := r["automatic"].(map[string]any)
	if !ok {
		t.Fatalf("no durable automatic metadata: %s", raw)
	}
	return a
}

func TestAutomaticCloseoutThirdAttemptEscalatesAcrossReload(t *testing.T) {
	reply := automaticCloseout("deployment rollout-42", "build https://example.org/deployments/rollout-42", nil)
	s, f, task, c, now := automaticFixture(t, reply)
	for attempt := 1; attempt <= 3; attempt++ {
		if attempt > 1 {
			// A real new turn ends parking. Serialize/reconstruct the ledger just as manager recovery does.
			cur := todoNow(s, task.ID)
			v := supervisionFromRow(cur.Supervision.row(task.ID))
			v.State = ""
			s.todos.mu.Lock()
			s.todos.todos[task.ID].Supervision = v
			s.todos.mu.Unlock()
			c.UpdatedAt = c.UpdatedAt.Add(time.Minute)
			f.setSessions("box", c)
		}
		g := superviseSignal{Todo: task.ID, Kind: "idle"}
		kept := s.screenMechanicalSignals(context.Background(), []superviseSignal{g}, now, now.Add(-time.Hour))
		cur := todoNow(s, task.ID)
		if a := automaticMetadata(t, cur.Supervision); a["count"] != float64(attempt) {
			t.Fatalf("attempt %d, metadata %+v", attempt, a)
		}
		if attempt < 3 {
			if len(kept) != 0 || cur.Supervision.State != "parked" {
				t.Fatalf("attempt %d was not parked: %+v", attempt, kept)
			}
			continue
		}
		if len(kept) != 1 || cur.Supervision.State == "parked" || !strings.Contains(kept[0].Detail, "limit reached") {
			t.Fatalf("third unchanged attempt did not return to supervisor: %+v", kept)
		}
		// A retry of the same stopped turn cannot increment the count.
		s.screenMechanicalSignals(context.Background(), []superviseSignal{g}, now, now.Add(-time.Hour))
		if a := automaticMetadata(t, todoNow(s, task.ID).Supervision); a["count"] != float64(3) {
			t.Fatalf("same-turn replay consumed another attempt: %+v", a)
		}
	}
}

func TestAutomaticCloseoutRetainsExistingSafetyGuards(t *testing.T) {
	reply := automaticCloseout("deployment rollout-42", "build https://example.org/deployments/rollout-42", nil)
	for _, guard := range []string{"working", "queued", "interaction", "error", "owner wait", "scheduled wake", "stale work owner wait", "all stopped", "rework"} {
		t.Run(guard, func(t *testing.T) {
			s, f, task, c, now := automaticFixture(t, reply)
			g := superviseSignal{Todo: task.ID, Kind: "idle"}
			switch guard {
			case "working":
				c.State = proto.StateWorking
			case "queued":
				c.Queued = []string{"continue"}
			case "interaction":
				c.PendingInteractions = 1
			case "error":
				c.TurnError = "upstream unavailable"
			case "owner wait", "stale work owner wait":
				at := c.UpdatedAt
				if guard == "stale work owner wait" {
					at = at.Add(-time.Hour)
				}
				s.putWork(context.Background(), db.SessionWork{MachineID: "box", SessionID: c.SessionID, TurnAt: at,
					Work: proto.WorkState{Phase: proto.WorkBlocked, On: proto.WaitUser}})
			case "scheduled wake":
				c.Loop = &proto.LoopInfo{Kind: "wakeup", NextAt: now.Add(time.Hour)}
			case "all stopped":
				g.Kind = "all_stopped"
			case "rework":
				s.todos.mu.Lock()
				s.todos.todos[task.ID].Supervision.State = "rework"
				s.todos.todos[task.ID].Supervision.Round = 1
				s.todos.mu.Unlock()
			}
			f.setSessions("box", c)
			handled, retry := s.mechanicalSignal(context.Background(), g, now)
			want := guard == "stale work owner wait" || guard == "all stopped" || guard == "rework"
			if handled != want {
				t.Fatalf("guard %s: handled=%v retry=%s, want %v", guard, handled, retry, want)
			}
			if guard == "scheduled wake" && retry.IsZero() {
				t.Fatal("automatic parking lost the pre-existing scheduled wake")
			}
		})
	}
}

func TestAutomaticCloseoutNewSourceResetsStreak(t *testing.T) {
	reply := automaticCloseout("deployment rollout-42", "build https://example.org/deployments/rollout-42", nil)
	s, f, task, c, now := automaticFixture(t, reply)
	g := superviseSignal{Todo: task.ID, Kind: "idle"}
	if handled, _ := s.mechanicalSignal(context.Background(), g, now); !handled {
		t.Fatal("first source did not park")
	}
	s.todos.mu.Lock()
	s.todos.todos[task.ID].Supervision.State = ""
	s.todos.mu.Unlock()
	c.UpdatedAt = c.UpdatedAt.Add(time.Minute)
	c.LastText = strings.ReplaceAll(reply, "rollout-42", "rollout-43")
	f.setSessions("box", c)
	if handled, _ := s.mechanicalSignal(context.Background(), g, now); !handled {
		t.Fatal("a new external object did not park")
	}
	if a := automaticMetadata(t, todoNow(s, task.ID).Supervision); a["count"] != float64(1) {
		t.Fatalf("new source inherited the previous dependency's streak: %+v", a)
	}
}

func TestAutomaticCloseoutSummaryDailyAndDurable(t *testing.T) {
	input := parkEvidenceArgs("external_event")
	s, f, task, _, now := automaticFixture(t, automaticCloseout("upstream check run 42 completes", "", input))
	ctx := context.Background()
	if handled, _ := s.mechanicalSignal(ctx, superviseSignal{Todo: task.ID, Kind: "idle"}, now); !handled {
		t.Fatal("external wait was not automatically parked")
	}
	if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
		raw, _ := json.Marshal(v.Reviews[0])
		var r map[string]any
		_ = json.Unmarshal(raw, &r)
		r["automatic"].(map[string]any)["first_at"] = now.Add(-48 * time.Hour)
		raw, _ = json.Marshal(r)
		_ = json.Unmarshal(raw, &v.Reviews[0])
		return true
	}); err != nil {
		t.Fatal(err)
	}
	// Generate through the real watch, not by calling the new summary helper. The summary goes out from 09:00 in the
	// owner's zone, which here is the machine's own: 10:00 UTC was 02:00 on a machine west of UTC, and the test failed
	// there. Take the next 10:00 in that zone after the fixture's now.
	loc := s.location(ctx)
	local := now.In(loc)
	at := time.Date(local.Year(), local.Month(), local.Day(), 10, 0, 0, 0, loc)
	if !at.After(now) {
		at = at.AddDate(0, 0, 1)
	}
	s.superviseWatch(ctx, at)
	s.supervise.mu.Lock()
	g, ok := s.supervise.pending[task.ID+"|parked_summary"]
	s.supervise.mu.Unlock()
	if !ok || g.level() != "info" || g.urgent() {
		t.Fatalf("missing quiet daily summary: %+v", g)
	}
	entry, ok := s.digestEntry(ctx, g, "")
	if !ok || entry.Parked == nil {
		t.Fatalf("summary does not carry the current wait: %+v", entry)
	}
	text := prompts.RenderSupervise(prompts.Supervise{Entries: []prompts.SuperviseEntry{entry}, Level: "info"})
	if !strings.Contains(text, "Daily automatic-parking summary") || !strings.Contains(text, "keep unchanged waits parked") {
		t.Fatalf("daily summary asks for ordinary unfinished-work triage: %s", text)
	}
	// An unchanged re-park while the supervisor is busy must refresh, not invalidate, its queued daily summary.
	if _, _, err := s.updateSupervision(ctx, task.ID, func(v *Supervision) bool {
		v.Reviews[len(v.Reviews)-1].At = v.Reviews[len(v.Reviews)-1].At.Add(time.Minute)
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.digestEntry(ctx, g, ""); !ok {
		t.Fatal("unchanged replacement park invalidated its already queued daily summary")
	}
	// Existing durable dedup groups it with other tasks and survives manager reconstruction.
	st := &memStore{}
	for i := 0; i < 3; i++ {
		next, _ := newQuietServiceOn(t, st)
		entries, _, rollback, err := next.reserveSupervision(ctx, []prompts.SuperviseEntry{entry}, []superviseSignal{g}, at.Add(time.Duration(i)*time.Hour))
		want := 0
		if i == 0 || i == 2 {
			want = 1
		}
		if err != nil || len(entries) != want {
			t.Fatalf("daily dedup pass %d: %d entries, want %d, err %v", i, len(entries), want, err)
		}
		if i == 0 {
			// A failed send releases the receipt; the next attempt must retry.
			rollback()
			entries, _, _, err = next.reserveSupervision(ctx, []prompts.SuperviseEntry{entry}, []superviseSignal{g}, at)
			if err != nil || len(entries) != 1 {
				t.Fatalf("failed send lost its daily summary: %v, %d", err, len(entries))
			}
		}
		if i == 1 {
			g.At = g.At.AddDate(0, 0, 1)
			_, park, _ := strings.Cut(g.Key, "|")
			g.Key = g.At.Format(time.DateOnly) + "|" + park
		}
	}
	// A fresh background round must retain the summary's informational level and wait instructions.
	f.mu.Lock()
	f.hello.Assistant.MachineID = "server"
	f.mu.Unlock()
	setSettings(t, s, func(v *Settings) { v.Main = "conv1" })
	bg := withBackgroundBG1(t, s, f)
	if !s.deliverDigest(ctx, s.settings(ctx), bg, []superviseSignal{g}, "", at, time.Time{}) {
		t.Fatal("daily summary did not reach the background supervisor")
	}
	send := lastSendTo(f, bg)
	text, _ = send["text"].(string)
	if send[superviseRoundParam] != true || !prompts.IsSuperviseRound(text) ||
		!strings.Contains(text, `<persona-supervision level="info" />`) ||
		!strings.Contains(text, "Daily automatic-parking summary") || !strings.Contains(text, "keep unchanged waits parked") {
		t.Fatalf("fresh background round lost the quiet summary or its round marker: %v\n%s", send, text)
	}
}

func TestAutomaticCloseoutParkingWriteFailureAndRecovery(t *testing.T) {
	ctx := context.Background()
	database := &ledgerDown{DB: taskRecoveryDatabase(t)}
	s, f, task := newSupervisedServiceOn(t, database)
	holdDigest(s)
	now := time.Now().UTC().Truncate(time.Second)
	a := task.Agents[0]
	c := stopped(a.SessionID, automaticCloseout("deployment rollout-42", "build https://example.org/deployments/rollout-42", nil))
	c.MachineID, c.UpdatedAt = a.MachineID, now.Add(-time.Hour)
	f.setSessions(a.MachineID, c)
	g := superviseSignal{Todo: task.ID, Kind: "idle"}
	before, _ := json.Marshal(todoNow(s, task.ID).Supervision)
	database.down.Store(true)
	if handled, _ := s.mechanicalSignal(ctx, g, now); handled {
		t.Fatal("failed ledger write silently consumed idle supervision")
	}
	after, _ := json.Marshal(todoNow(s, task.ID).Supervision)
	if string(before) != string(after) {
		t.Fatal("failed write leaked parking or a spent attempt into memory")
	}
	database.down.Store(false)
	if handled, _ := s.mechanicalSignal(ctx, g, now); !handled {
		t.Fatal("recovered ledger did not retry automatic admission")
	}
	again, _ := restartedOn(t, database)
	holdDigest(again)
	if err := again.ensureTodosLoaded(ctx); err != nil {
		t.Fatal(err)
	}
	recovered := todoNow(again, task.ID)
	if recovered.Supervision.State != "parked" || recovered.Supervision.parked().Evidence.URL != "https://example.org/deployments/rollout-42" ||
		automaticMetadata(t, recovered.Supervision)["count"] != float64(1) {
		t.Fatalf("PostgreSQL/reconstruction lost automatic wait evidence: %+v", recovered.Supervision)
	}
}

func TestAutomaticCloseoutParkingExistingSourceWakes(t *testing.T) {
	ctx := context.Background()
	t.Run("terminal PR", func(t *testing.T) {
		s, hub, task := newSupervisedService(t)
		holdDigest(s)
		now := time.Now().UTC().Truncate(time.Second)
		source := "https://github.com/example/project/pull/42"
		s.setTaskStage(ctx, task.ID, "review", source)
		task = todoNow(s, task.ID)
		if err := s.applyPRStatus(ctx, task, TodoPRStatus{URL: source, State: "open", HeadOID: "head-1", CheckedAt: now}); err != nil {
			t.Fatal(err)
		}
		task = todoNow(s, task.ID)
		a := task.Agents[0]
		c := stopped(a.SessionID, automaticCloseout("reviewer-a completes the requested review", "", parkEvidenceArgs("merge_review")))
		c.MachineID, c.UpdatedAt = a.MachineID, now.Add(-time.Hour)
		hub.setSessions(a.MachineID, c)
		if handled, _ := s.mechanicalSignal(ctx, superviseSignal{Todo: task.ID, Kind: "idle"}, now); !handled {
			t.Fatal("evidenced current PR wait did not automatically park")
		}
		task = todoNow(s, task.ID)
		if err := s.applyPRStatus(ctx, task, TodoPRStatus{URL: source, State: "merged", CheckedAt: now.Add(time.Minute)}); err != nil {
			t.Fatal(err)
		}
		current := todoNow(s, task.ID)
		if current.Supervision.State != "pending_review" || !pendingReviewSignal(s, task.ID) || current.DoneAt != nil {
			t.Fatalf("existing terminal PR observer lost the automatic wait, or accepted it: %+v", current.Supervision)
		}
	})
	t.Run("named-person reply", func(t *testing.T) {
		now := time.Now().UTC().Truncate(time.Second)
		at := now.Add(-5 * time.Minute)
		reply := automaticCloseout("ou_peer replies in the sent topic", "", personReplyParkArgs(now))
		s, hub, task, c, _ := automaticFixture(t, reply)
		c.UpdatedAt = at.Add(-time.Minute)
		hub.setSessions("box", c)
		if handled, _ := s.mechanicalSignal(ctx, superviseSignal{Todo: task.ID, Kind: "idle"}, at); !handled {
			t.Fatal("evidenced person wait did not automatically park")
		}
		hub.answerApp(AppConnectors, "connectors.lark_reply", func(json.RawMessage) (any, error) {
			return personReplyObservation(now), nil
		})
		s.sweepDeploys(now)
		current := todoNow(s, task.ID)
		if current.Supervision.State != "pending_review" || !pendingReviewSignal(s, task.ID) || current.DoneAt != nil {
			t.Fatalf("existing named-person observer lost the automatic wait, or accepted it: %+v", current.Supervision)
		}
	})
}

func TestAutomaticCloseoutPassivePRAdmission(t *testing.T) {
	const source = "https://github.com/example/project/pull/42"
	// i18n-ignore: anonymized Agent closeouts matching the reported external-wait wording.
	reply := func(status, fields, gate string) string {
		delivery := ""
		if gate != "" {
			delivery = "交付：停在 PR；卡点：" + gate + "；谁来解：其他\n"
		}
		return delivery + "<closeout status=\"" + status + "\" merged=\"no\" deployed=\"no\">\n" +
			"artifact: pr " + source + "\n" + fields + "\n</closeout>"
	}
	for _, tc := range []struct {
		name, text string
		noFact     bool
		fact       func(*TodoPRStatus)
		want       bool
	}{
		{"blocked upstream review", reply("blocked", "waiting: 等上游评审及验证维护者处理闸门；本轮不擅自公开回复、合并或部署。", ""), false, nil, true},
		{"blocked review after local deployment", "交付：已上线并实测；卡点：上游评审待复核；谁来解：其他\n" +
			strings.ReplaceAll(reply("blocked", "waiting: 等上游评审及验证维护者处理闸门。", ""), `deployed="no"`, `deployed="yes"`), false, nil, true},
		{"done merge and deployment gate", reply("done", "", "待上游合并及部署 Agent 统一集成，本轮按要求未执行"), false, nil, true},
		{"done maintainer re-review gate", reply("done", "", "待维护者复评，合并、部署及真实外发仍需另行授权"), false, nil, true},
		{"not done merge and live verification", reply("not_done", "missing: 上游合并及统一部署实测。", "待上游合并及统一部署实测"), false, nil, true},
		{"legacy delivery line", "PR " + source + "\n交付：停在 PR；卡点：待上游合并及统一部署实测；谁来解：其他", false, nil, true},
		{"markdown PR source", reply("done", "", "待上游合并") + "\n[PR](" + source + "#discussion_r42)", false, nil, true},
		{"case-normalized PR source", strings.ReplaceAll(reply("done", "", "待上游合并"), source, "https://github.com/EXAMPLE/PROJECT/pull/42"), false, nil, true},
		{"no external blocker", reply("done", "", ""), false, nil, false},
		{"malformed closeout", reply("invalid", "", "待上游合并"), false, nil, false},
		{"owner gate", reply("done", "", "等用户批准合并"), false, nil, false},
		{"agent gate", "PR " + source + "\n交付：停在 PR；卡点：待我修复测试后再合并；谁来解：Agent", false, nil, false},
		{"own unfinished review", reply("blocked", "waiting: finish my review before merging", ""), false, nil, false},
		{"unbound PR", strings.ReplaceAll(reply("done", "", "待上游合并"), source, "https://github.com/example/other/pull/42"), false, nil, false},
		{"ambiguous PRs", reply("done", "", "待上游合并") + "\nhttps://github.com/example/project/pull/43", false, nil, false},
		{"unrecorded reply", reply("blocked", "waiting: wait for reviewer-a to reply", ""), false, nil, false},
		{"deployment alone is not a PR wait", reply("blocked", "waiting: deployment rollout-42", ""), false, nil, false},
		{"unknown PR facts", reply("done", "", "待上游合并"), true, nil, false},
		{"stale PR facts", reply("done", "", "待上游合并"), false, func(f *TodoPRStatus) { f.CheckedAt = f.CheckedAt.Add(-triagePRStatusFreshFor - time.Second) }, false},
		{"draft PR", reply("done", "", "待上游合并"), false, func(f *TodoPRStatus) { f.Draft = true }, false},
		{"conflicting PR", reply("done", "", "待上游合并"), false, func(f *TodoPRStatus) { f.Conflicts = true }, false},
		{"changes requested", reply("done", "", "待上游合并"), false, func(f *TodoPRStatus) { f.Review = "changes_requested" }, false},
		{"failing checks", reply("done", "", "待上游合并"), false, func(f *TodoPRStatus) { f.Checks = "failure" }, false},
		{"unknown head", reply("done", "", "待上游合并"), false, func(f *TodoPRStatus) { f.HeadOID = "" }, false},
		{"closed PR", reply("done", "", "待上游合并"), false, func(f *TodoPRStatus) { f.State = "closed" }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, task, _, now := automaticFixture(t, tc.text)
			fact := &TodoPRStatus{URL: source, State: "open", HeadOID: "head-1", Checks: "none", CheckedAt: now}
			if tc.fact != nil {
				tc.fact(fact)
			}
			if tc.noFact {
				fact = nil
			}
			s.todos.mu.Lock()
			s.todos.todos[task.ID].PRURL, s.todos.todos[task.ID].PRStatus = source, fact
			s.todos.mu.Unlock()
			g := superviseSignal{Todo: task.ID, Kind: sigIdle}
			kept := s.screenMechanicalSignals(context.Background(), []superviseSignal{g}, now, now.Add(-time.Hour))
			current := todoNow(s, task.ID)
			if parked := current.Supervision.State == reviewParked; parked != tc.want || (len(kept) == 0) != tc.want {
				t.Fatalf("parked=%v, remaining idle=%d, want automatic=%v; ledger=%+v", parked, len(kept), tc.want, current.Supervision)
			}
			if tc.want {
				r := current.Supervision.parking()
				if r == nil || r.By != "closeout" || r.Evidence == nil || r.Evidence.Kind != "external_event" ||
					r.Evidence.URL != source || r.Evidence.Contact != nil || r.Evidence.RecordedBy != "" {
					t.Fatalf("passive PR evidence invented a sent request or lost its exact source: %+v", r)
				}
				for _, next := range watchSignals(current, nil, now) {
					if next.Kind == sigIdle || next.Kind == sigAllStopped {
						t.Fatalf("parked task still raises %s", next.Kind)
					}
				}
			}
		})
	}
}

func TestAutomaticCloseoutPassivePRUsesExistingWake(t *testing.T) {
	for _, state := range []string{"merged", "closed", "changes_requested"} {
		t.Run(state, func(t *testing.T) {
			ctx := context.Background()
			const source = "https://github.com/example/project/pull/42"
			s, hub, task, c, now := automaticFixture(t, "PR "+source+
				"\n交付：停在 PR；卡点：待上游合并及统一部署实测；谁来解：其他") // i18n-ignore: Agent delivery data.
			s.setTaskStage(ctx, task.ID, "review", source)
			fact := TodoPRStatus{URL: source, State: "open", HeadOID: "head-1", CheckedAt: now}
			if err := s.applyPRStatus(ctx, todoNow(s, task.ID), fact); err != nil {
				t.Fatal(err)
			}
			hub.setSessions(c.MachineID, c)
			if handled, _ := s.mechanicalSignal(ctx, superviseSignal{Todo: task.ID, Kind: sigIdle}, now); !handled {
				t.Fatal("passive upstream PR wait did not automatically park")
			}
			current := todoNow(s, task.ID)
			r := *current.Supervision.parking()
			r.Automatic = nil
			if parkWaitsOnPR(&r, source) {
				t.Fatal("automatic admission changed the separately owned manual external-event observer")
			}
			fact.CheckedAt = now.Add(time.Minute)
			if state == "changes_requested" {
				at := now.Add(30 * time.Second)
				fact.Review, fact.ChangesRequestedAt = state, &at
			} else {
				fact.State = state
			}
			if err := s.applyPRStatus(ctx, current, fact); err != nil {
				t.Fatal(err)
			}
			current = todoNow(s, task.ID)
			if current.Supervision.State != reviewPending || !pendingReviewSignal(s, task.ID) || current.DoneAt != nil {
				t.Fatalf("existing PR observer lost the automatic source wait or accepted it: %+v", current.Supervision)
			}
		})
	}
}

// The waits a closeout's own text implies (a dated wait, a passive PR wait, a deployment or other external object) need
// no owner step, so the evidence closeoutPark synthesizes for them says next_actor "external": since #12771, readPark
// refuses evidence without it, and every automatic closeout park was refused. An explicit `park:` line is the
// Agent's own account, so it must say next_actor itself; docs/assistant/todos.md's example line has to pass admission.
func TestAutomaticCloseoutDocumentedParkLineIsAdmitted(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "assistant", "todos.md"))
	if err != nil {
		t.Fatal(err)
	}
	var waiting, park string
	for _, line := range strings.Split(string(doc), "\n") {
		if strings.HasPrefix(line, "park: {") {
			park = line
			break
		}
		if strings.HasPrefix(line, "waiting: ") {
			waiting = line
		}
	}
	if waiting == "" || park == "" {
		t.Fatal("docs/assistant/todos.md has no example closeout `waiting:` + `park:` lines")
	}
	closeoutWith := func(park string) string {
		return "<closeout status=\"blocked\" merged=\"no\" deployed=\"no\">\n" +
			"did: Prepared the change.\n" +
			"artifact: pr https://github.com/example/project/pull/42\n" +
			"verified: Local tests pass.\n" +
			"missing: The requested review.\n" +
			waiting + "\n" + park + "\n</closeout>"
	}
	now := time.Date(2026, 11, 18, 8, 0, 0, 0, time.UTC)
	p, ok := closeoutPark(closeoutWith(park), nil, now, time.UTC)
	if !ok || p.Evidence == nil || p.Evidence.Kind != "merge_review" || p.Evidence.NextActor != "external" {
		t.Fatalf("the documented park line was not admitted: ok=%v park=%+v evidence=%+v", ok, p, p.Evidence)
	}
	// without next_actor the same line is refused, as task_review refuses it
	bare := strings.Replace(park, `"next_actor":"external",`, "", 1)
	if bare == park {
		t.Fatalf("the documented park line has no next_actor: %s", park)
	}
	if _, ok := closeoutPark(closeoutWith(bare), nil, now, time.UTC); ok {
		t.Fatal("a park line without next_actor was admitted")
	}
	// the evidence closeoutPark writes for a wait it reads off the text names the external next actor
	waitingLine := regexp.MustCompile(`(?m)^waiting: .*$`)
	withWaiting := func(reply, w string) string { return waitingLine.ReplaceAllLiteralString(reply, "waiting: "+w) }
	for name, reply := range map[string]string{
		"date": withWaiting(reviewArtifactCloseout, "the release window opens 2026-11-25"),
		"pr":   reviewArtifactCloseout,
		"deployment": strings.Replace(withWaiting(reviewArtifactCloseout, "deployment rollout-42"),
			"artifact: pr "+reviewArtifactURL, "artifact: build https://example.org/deployments/rollout-42", 1),
	} {
		if p, ok := closeoutPark(reply, nil, now, time.UTC); !ok || p.Evidence == nil || p.Evidence.NextActor != "external" {
			t.Errorf("%s: closeout wait not admitted with an external next actor: ok=%v evidence=%+v", name, ok, p.Evidence)
		}
	}
}
