package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Use existing entrypoints and raw wire maps so removing the implementation still compiles these regressions.
func linkedFollowupTask(t *testing.T, database *db.DB, store Store) (*Service, *fakeHub, Todo, time.Time) {
	t.Helper()
	s, f, task := newSupervisedServiceOn(t, store)
	stopSupervisionTimers(s)
	holdDigest(s)
	now := time.Now().UTC().Truncate(time.Second)
	th := TodoThread{Channel: "feishu", ChatID: "oc_example", ThreadID: "omt_example", Title: "Example questions"}
	if err := database.SetTodoThread(context.Background(), task.ID, th, now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := s.bindThread(context.Background(), task.ID, th, nil); err != nil {
		t.Fatal(err)
	}
	nodeListed(s, f, turnRow(proto.StatusIdle, 1, ""))
	return s, f, todoNow(s, task.ID), now
}

func linkedFollowupObservation(raw json.RawMessage, now time.Time, id, sender string) map[string]any {
	var watch map[string]any
	_ = json.Unmarshal(raw, &watch)
	identity, _ := watch["identity"].(map[string]any)
	if identity == nil || identity["app_id"] == "" {
		watch["identity"] = map[string]any{"brand": "feishu", "app_id": "app_example", "account_open_id": "ou_account"}
	}
	return map[string]any{"watch": watch, "checked_at": now.Format(time.RFC3339Nano), "messages": []any{
		map[string]any{"id": id, "sender_id": sender, "sender_type": "user", "id_type": "open_id",
			"chat_id": "oc_example", "thread_id": "omt_example", "at": now.Add(-2 * time.Minute).Format(time.RFC3339Nano),
			"before": now.Add(-2*time.Minute + time.Second).Format(time.RFC3339Nano)},
	}}
}

func flushLinkedFollowup(s *Service) {
	s.supervise.mu.Lock()
	s.supervise.last = time.Time{}
	for id, g := range s.supervise.pending {
		g.At = time.Now().Add(-time.Minute)
		s.supervise.pending[id] = g
	}
	s.supervise.mu.Unlock()
	s.sendDigest(context.Background())
}

func TestLinkedTopicFollowupReachesAgentAndCurrentSupervisor(t *testing.T) {
	for _, parked := range []bool{false, true} {
		for _, sender := range []string{"ou_requester", "ou_another_person"} {
			name := sender
			if parked {
				name += "/parked"
			}
			t.Run(name, func(t *testing.T) {
				database := taskRecoveryDatabase(t)
				s, f, task, now := linkedFollowupTask(t, database, database)
				setSettings(t, s, func(cfg *Settings) { cfg.Leaders = []string{"current"} })
				if parked {
					parkFor(t, s, task.ID, "Waiting for the upstream result", parkExternalEvent, "2d")
				}
				ledger, _ := json.Marshal(todoNow(s, task.ID).Supervision)
				sends := len(sentToAgent(f))
				f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
					return linkedFollowupObservation(raw, now, "om_followup", sender), nil
				})
				s.sweepDeploys(now)
				if len(sentToAgent(f)) != sends+1 || !slices.Contains(pendingKinds(s, task.ID), "topic_followup") {
					t.Fatalf("follow-up did not reach both routes: sends=%d/%d pending=%v",
						len(sentToAgent(f)), sends, pendingKinds(s, task.ID))
				}
				note := sentToAgent(f)[sends]
				for _, want := range []string{"Lark topic", "om_followup", sender, "not a user instruction", "existing scope and permissions", "not an owner decision"} {
					if !strings.Contains(note, want) {
						t.Errorf("Agent note lacks %q: %s", want, note)
					}
				}
				after, _ := json.Marshal(todoNow(s, task.ID).Supervision)
				decisions, err := database.ListTodoDecisions(context.Background(), task.ID, true)
				if err != nil || len(decisions) != 0 || string(ledger) != string(after) || todoNow(s, task.ID).DoneAt != nil {
					t.Fatalf("source activity changed decisions/lifecycle: decisions=%v err=%v ledger=%s/%s", decisions, err, ledger, after)
				}
				f.mu.Lock()
				f.known["current"] = true
				f.mu.Unlock()
				f.setSessions("server",
					proto.Session{SessionID: "conv1", Assistant: true, Status: proto.StatusIdle},
					proto.Session{SessionID: "current", Assistant: true, Status: proto.StatusIdle})
				if _, _, err := s.updateSupervision(context.Background(), task.ID, func(v *Supervision) bool {
					v.Supervisor, v.SupervisorSource = "current", "explicit"
					return true
				}); err != nil {
					t.Fatal(err)
				}
				flushLinkedFollowup(s)
				if len(f.noticesTo("conv1")) != 0 || len(f.noticesTo("current")) != 1 ||
					!strings.Contains(f.noticesTo("current")[0], "supervisor's copy") {
					t.Fatalf("not sent once to the current supervisor: old=%v current=%v", f.noticesTo("conv1"), f.noticesTo("current"))
				}
				s.sweepDeploys(now.Add(time.Minute))
				s.sweepDeploys(now.Add(2 * time.Minute))
				if len(sentToAgent(f)) != sends+1 || len(f.noticesTo("current")) != 1 {
					t.Fatal("the same source message was delivered again")
				}
			})
		}
	}
}

func TestLinkedTopicFollowupRejectsInvalidSourceReceipts(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"wrong topic", func(o map[string]any) { o["watch"].(map[string]any)["thread_id"] = "omt_other" }},
		{"wrong chat", func(o map[string]any) { o["messages"].([]any)[0].(map[string]any)["chat_id"] = "oc_other" }},
		{"own message", func(o map[string]any) { o["messages"].([]any)[0].(map[string]any)["sender_id"] = "ou_account" }},
		{"bot", func(o map[string]any) { o["messages"].([]any)[0].(map[string]any)["sender_type"] = "app" }},
		{"app masquerading as human", func(o map[string]any) { o["messages"].([]any)[0].(map[string]any)["app_id"] = "app_example" }},
		{"missing identity", func(o map[string]any) { o["watch"].(map[string]any)["identity"] = nil }},
		{"changed cutoff", func(o map[string]any) { o["watch"].(map[string]any)["since"] = time.Time{}.Format(time.RFC3339) }},
		{"future read", func(o map[string]any) { o["checked_at"] = time.Now().Add(time.Hour).Format(time.RFC3339) }},
		{"ambiguous post-bind minute", func(o map[string]any) {
			m := o["messages"].([]any)[0].(map[string]any)
			since, _ := time.Parse(time.RFC3339Nano, o["watch"].(map[string]any)["since"].(string))
			m["at"], m["before"] = since.Add(-time.Second).Format(time.RFC3339Nano), since.Add(time.Minute-time.Second).Format(time.RFC3339Nano)
		}},
		{"old message", func(o map[string]any) {
			m := o["messages"].([]any)[0].(map[string]any)
			m["at"], m["before"] = time.Now().Add(-24*time.Hour).Format(time.RFC3339), time.Now().Add(-24*time.Hour+time.Second).Format(time.RFC3339)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			database := taskRecoveryDatabase(t)
			s, f, task, now := linkedFollowupTask(t, database, database)
			before := len(sentToAgent(f))
			f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
				o := linkedFollowupObservation(raw, now, "om_followup", "ou_requester")
				tc.mutate(o)
				return o, nil
			})
			s.sweepDeploys(now)
			if len(f.appParams(AppConnectors, "connectors.lark_topic")) != 1 {
				t.Fatal("the linked topic was not actually observed")
			}
			if len(sentToAgent(f)) != before || len(pendingKinds(s, task.ID)) != 0 {
				t.Fatalf("invalid observation woke the task: sends=%d/%d pending=%v", len(sentToAgent(f)), before, pendingKinds(s, task.ID))
			}
		})
	}
}

func TestLinkedTopicFollowupRechecksInflightChanges(t *testing.T) {
	for _, action := range []string{"complete", "cancel", "unbind", "rebind", "rebind same topic"} {
		t.Run(action, func(t *testing.T) {
			database := taskRecoveryDatabase(t)
			s, f, task, now := linkedFollowupTask(t, database, database)
			before := len(sentToAgent(f))
			f.answerAppBlocking(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
				switch action {
				case "complete":
					w := httptest.NewRecorder()
					s.ServeHTTP(w, httptest.NewRequest(http.MethodPatch, "/api/v1/todos/"+task.ID, strings.NewReader(`{"done":true}`)))
					if w.Code != http.StatusOK {
						t.Errorf("close: %d %s", w.Code, w.Body.String())
					}
				case "cancel":
					if _, err := reviewErr(s, task.ID, map[string]any{"verdict": "cancelled", "note": "No longer needed"}); err != nil {
						t.Error(err)
					}
				default:
					if _, err := s.unbindThread(context.Background(), task.ID); err != nil {
						t.Error(err)
					}
					if action != "unbind" {
						th := *task.Thread
						if action == "rebind" {
							th.ThreadID = "omt_replacement"
						}
						if _, err := s.bindThread(context.Background(), task.ID, th, nil); err != nil {
							t.Error(err)
						}
					}
				}
				return linkedFollowupObservation(raw, now, "om_followup", "ou_requester"), nil
			})
			s.sweepDeploys(now)
			if len(sentToAgent(f)) != before || slices.Contains(pendingKinds(s, task.ID), "topic_followup") {
				t.Fatalf("stale topic context was delivered: sends=%d/%d pending=%v", len(sentToAgent(f)), before, pendingKinds(s, task.ID))
			}
		})
	}
}

type linkedFollowupWriteDown struct {
	*db.DB
	down     atomic.Bool
	readDown atomic.Bool
}

func (d *linkedFollowupWriteDown) TodoThreadState(ctx context.Context, id string) (json.RawMessage, error) {
	if d.readDown.Load() {
		return nil, errors.New("source receipt read unavailable")
	}
	return any(d.DB).(interface {
		TodoThreadState(context.Context, string) (json.RawMessage, error)
	}).TodoThreadState(ctx, id)
}

func (d *linkedFollowupWriteDown) SaveTodoThreadFollowups(ctx context.Context, id string, before, next json.RawMessage) (json.RawMessage, bool, error) {
	if d.down.Load() {
		return nil, false, errors.New("source receipt write unavailable")
	}
	return any(d.DB).(interface {
		SaveTodoThreadFollowups(context.Context, string, json.RawMessage, json.RawMessage) (json.RawMessage, bool, error)
	}).SaveTodoThreadFollowups(ctx, id, before, next)
}

func TestLinkedTopicFollowupPersistsAndRecovers(t *testing.T) {
	database := taskRecoveryDatabase(t)
	store := &linkedFollowupWriteDown{DB: database}
	s, f, task, now := linkedFollowupTask(t, database, store)
	before := len(sentToAgent(f))
	answer := func(raw json.RawMessage) (any, error) {
		return linkedFollowupObservation(raw, now, "om_followup", "ou_requester"), nil
	}
	f.answerApp(AppConnectors, "connectors.lark_topic", answer)
	store.down.Store(true)
	s.sweepDeploys(now)
	if len(sentToAgent(f)) != before || len(pendingKinds(s, task.ID)) != 0 {
		t.Fatal("a failed source-receipt write published a wake")
	}
	store.down.Store(false)
	s.sweepDeploys(now.Add(time.Minute))
	if len(sentToAgent(f)) != before+1 || !slices.Contains(pendingKinds(s, task.ID), "topic_followup") {
		t.Fatal("failed source receipt was not retried")
	}
	again, hub := newTaskServiceOn(t, store)
	stopSupervisionTimers(again)
	holdDigest(again)
	hub.mu.Lock()
	hub.hello.Assistant.MachineID = "server"
	hub.known["conv1"] = true
	hub.mu.Unlock()
	hub.setSessions("server", proto.Session{SessionID: "conv1", MachineID: "server", Assistant: true, Status: proto.StatusIdle})
	setSettings(t, again, func(cfg *Settings) { cfg.Main = "conv1" })
	if err := again.ensureTodosLoaded(context.Background()); err != nil {
		t.Fatal(err)
	}
	hub.answerApp(AppConnectors, "connectors.lark_topic", func(json.RawMessage) (any, error) {
		return nil, errors.New("source temporarily unavailable after receipt commit")
	})
	again.sweepDeploys(now.Add(2 * time.Minute))
	if len(sentToAgent(hub)) != 0 || len(hub.notices()) == 0 && !slices.Contains(pendingKinds(again, task.ID), "topic_followup") {
		t.Fatalf("restart lost the supervisor copy or resent to Agent: sends=%v pending=%v", sentToAgent(hub), pendingKinds(again, task.ID))
	}
	flushLinkedFollowup(again)
	if len(hub.notices()) != 1 || !strings.Contains(hub.notices()[0], "om_followup") {
		t.Fatalf("persisted copy not delivered: %v", hub.notices())
	}
	again.sweepDeploys(now.Add(3 * time.Minute))
	final, last := restartedOn(t, store)
	stopSupervisionTimers(final)
	holdDigest(final)
	last.answerApp(AppConnectors, "connectors.lark_topic", answer)
	final.sweepDeploys(now.Add(4 * time.Minute))
	if len(sentToAgent(last)) != 0 || len(pendingKinds(final, task.ID)) != 0 {
		t.Fatal("a completed receipt was replayed after reconstruction")
	}
}

func TestLinkedTopicFollowupUnavailableAgentStillNotifiesAndRetries(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, task, now := linkedFollowupTask(t, database, database)
	before := len(sentToAgent(f))
	f.mu.Lock()
	f.failSend = errors.New("example transport unavailable")
	f.mu.Unlock()
	f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
		return linkedFollowupObservation(raw, now, "om_followup", "ou_requester"), nil
	})
	s.sweepDeploys(now)
	if len(sentToAgent(f)) != before || !slices.Contains(pendingKinds(s, task.ID), "topic_followup") {
		t.Fatal("Agent failure hid the follow-up from the supervisor")
	}
	// Read the validated digest projection without failing the supervisor's own transport.
	s.supervise.mu.Lock()
	g := s.supervise.pending[task.ID+"|topic_followup"]
	s.supervise.mu.Unlock()
	e, ok := s.digestEntry(context.Background(), g, "")
	if !ok || !strings.Contains(e.Detail, "not delivered; retry pending") {
		t.Fatalf("delivery failure is not inspectable: %+v holds=%v", e, ok)
	}
	f.mu.Lock()
	f.failSend = nil
	f.mu.Unlock()
	s.sweepDeploys(now.Add(time.Minute))
	if len(sentToAgent(f)) != before+1 {
		t.Fatal("pending Agent delivery was not retried")
	}
	flushLinkedFollowup(s)
	if len(f.notices()) != 1 || !strings.Contains(f.notices()[0], "supervisor's copy") {
		t.Fatalf("no success copy after recovery: %v", f.notices())
	}
}

func TestLinkedTopicFollowupKeepsPendingReceiptWhenNewActivityArrives(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, task, now := linkedFollowupTask(t, database, database)
	sends := len(sentToAgent(f))
	f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
		return linkedFollowupObservation(raw, now.Add(-time.Minute), "om_first", "ou_requester"), nil
	})
	s.sweepDeploys(now)

	// Renaming the same topic keeps the checkpoint and its undelivered supervisor copy.
	th := *task.Thread
	th.Title = "Renamed example"
	if _, err := s.bindThread(t.Context(), task.ID, th, nil); err != nil {
		t.Fatal(err)
	}
	f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
		return linkedFollowupObservation(raw, now, "om_second", "ou_another_person"), nil
	})
	s.sweepDeploys(now.Add(time.Minute))
	if len(sentToAgent(f)) != sends+1 {
		t.Fatal("the second observation replaced the first pending receipt")
	}
	flushLinkedFollowup(s)
	holdDigest(s)
	if got := f.notices(); len(got) != 1 || !strings.Contains(got[0], "om_first") {
		t.Fatalf("first supervisor copy lost after new activity and a rename: %v", got)
	}

	s.sweepDeploys(now.Add(2 * time.Minute))
	flushLinkedFollowup(s)
	if got := f.notices(); len(sentToAgent(f)) != sends+2 || len(got) != 2 || !strings.Contains(got[1], "om_second") {
		t.Fatalf("second follow-up not delivered after clearing the first: %v", got)
	}
}

func TestLinkedTopicFollowupQueuedCopyIsInvalidatedByRebinding(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, task, now := linkedFollowupTask(t, database, database)
	f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
		return linkedFollowupObservation(raw, now, "om_followup", "ou_requester"), nil
	})
	s.sweepDeploys(now)
	if !slices.Contains(pendingKinds(s, task.ID), "topic_followup") {
		t.Fatal("fixture produced no queued copy")
	}
	if _, err := s.unbindThread(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.bindThread(context.Background(), task.ID, *task.Thread, nil); err != nil {
		t.Fatal(err)
	}
	flushLinkedFollowup(s)
	if len(f.notices()) != 0 {
		t.Fatalf("old binding's queued notification survived rebinding: %v", f.notices())
	}
}

func TestLinkedTopicFollowupDoesNotTrustChangedSourceIdentity(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, task, now := linkedFollowupTask(t, database, database)
	f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
		o := linkedFollowupObservation(raw, now, "om_old", "ou_requester")
		o["messages"] = []any{}
		return o, nil
	})
	s.sweepDeploys(now)
	for i, field := range []string{"brand", "app_id", "account_open_id"} {
		f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
			o := linkedFollowupObservation(raw, now, "om_followup", "ou_requester")
			o["watch"].(map[string]any)["identity"].(map[string]any)[field] = "other"
			return o, nil
		})
		before := len(sentToAgent(f))
		s.sweepDeploys(now.Add(time.Duration(i+1) * time.Minute))
		if len(sentToAgent(f)) != before || len(pendingKinds(s, task.ID)) != 0 {
			t.Fatal("a changed configured identity silently retargeted the topic")
		}
	}
	threads, err := database.ListTodoThreads(context.Background())
	if err != nil || !reflect.DeepEqual(threads[task.ID], *task.Thread) {
		t.Fatalf("internal checkpoints leaked into or changed the public binding: %v %v", threads, err)
	}
}

func TestLinkedTopicFollowupLegacyBindingStartsWithoutHistoryReplay(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, task, now := linkedFollowupTask(t, database, database)
	var raw json.RawMessage
	if err := database.QueryRow(context.Background(), `UPDATE todos SET thread = thread - 'followups'
		WHERE id = $1 RETURNING thread`, task.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	before := len(sentToAgent(f))
	f.answerApp(AppConnectors, "connectors.lark_topic", func(params json.RawMessage) (any, error) {
		return linkedFollowupObservation(params, now.Add(-5*time.Minute), "om_history", "ou_requester"), nil
	})
	s.sweepDeploys(now)
	if len(f.appParams(AppConnectors, "connectors.lark_topic")) != 1 {
		t.Fatal("legacy binding was not observed")
	}
	if len(sentToAgent(f)) != before || len(pendingKinds(s, task.ID)) != 0 {
		t.Fatal("legacy topic history was replayed as new work")
	}
	f.answerApp(AppConnectors, "connectors.lark_topic", func(params json.RawMessage) (any, error) {
		return linkedFollowupObservation(params, now, "om_new", "ou_requester"), nil
	})
	s.sweepDeploys(now.Add(time.Minute))
	if len(sentToAgent(f)) != before+1 || !slices.Contains(pendingKinds(s, task.ID), "topic_followup") {
		t.Fatal("baseline suppressed a genuinely later follow-up")
	}
}

func TestLinkedTopicFollowupTimestampOnlyCheckpoint(t *testing.T) {
	for _, initial := range []string{"om_history", ""} {
		t.Run("initial="+initial, func(t *testing.T) {
			database := taskRecoveryDatabase(t)
			s, f, task, now := linkedFollowupTask(t, database, database)
			before := len(sentToAgent(f))
			for i, id := range []string{initial, "om_new", "om_new", "om_older"} {
				f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
					at := now.Add(-8 * time.Minute)
					if id == "om_older" {
						at = at.Add(-time.Second)
					}
					o := linkedFollowupObservation(raw, at, id, "ou_requester")
					if id == "" {
						o["messages"] = []any{}
					}
					return o, nil
				})
				s.sweepDeploys(now.Add(time.Duration(i) * time.Minute))
				flushLinkedFollowup(s)
				holdDigest(s)
				want := min(i, 1)
				if got := len(sentToAgent(f)) - before; got != want {
					t.Fatalf("poll %d (%q): got %d Agent deliveries, want %d", i, id, got, want)
				}
				if got := len(f.notices()); got != want {
					t.Fatalf("poll %d (%q): got %d supervisor deliveries, want %d", i, id, got, want)
				}
			}
			if pending := linkedFollowupStoredPending(t, database, task.ID); pending != nil {
				t.Fatalf("the acknowledged boundary message remained pending: %v", pending)
			}
		})
	}
}

func TestLinkedTopicFollowupSameTimeIDsAndOlderMessages(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, _, now := linkedFollowupTask(t, database, database)
	before := len(sentToAgent(f))
	for i, id := range []string{"om_first", "om_second", "om_second", "om_older"} {
		f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
			at := now
			if id == "om_older" {
				at = at.Add(-time.Minute)
			}
			return linkedFollowupObservation(raw, at, id, "ou_requester"), nil
		})
		s.sweepDeploys(now.Add(time.Duration(i) * time.Minute))
		flushLinkedFollowup(s)
		holdDigest(s)
		want := min(i+1, 2)
		if len(sentToAgent(f)) != before+want {
			t.Fatalf("message %s: sends=%d/%d want +%d", id, len(sentToAgent(f)), before, want)
		}
	}
}

func TestLinkedTopicFollowupOtherParticipantDoesNotSatisfyNamedWait(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, task, now := linkedFollowupTask(t, database, database)
	parkPersonReply(t, s, task, now)
	f.answerApp(AppConnectors, "connectors.lark_reply", func(json.RawMessage) (any, error) {
		o := personReplyObservation(now)
		o["reply"].(map[string]any)["sender_id"] = "ou_requester"
		return o, nil
	})
	f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
		return linkedFollowupObservation(raw, now, "om_followup", "ou_requester"), nil
	})
	before := len(sentToAgent(f))
	s.sweepDeploys(now)
	if len(sentToAgent(f)) != before+1 || !slices.Contains(pendingKinds(s, task.ID), "topic_followup") {
		t.Fatal("a follow-up from someone other than the awaited person did not wake the Agent and supervisor")
	}
	p := todoNow(s, task.ID).Supervision.parking()
	if p == nil || p.Reply != nil {
		t.Fatal("the unrelated participant was recorded as satisfying the named person's wait")
	}
}

func TestLinkedTopicFollowupBoundsReadsAndNotifiesUnsupervisedFallback(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, _, now := linkedFollowupTask(t, database, database)
	for i := 0; i < 5; i++ {
		task, err := s.createTodo(context.Background(), fmt.Sprintf("Example follow-up %d", i), "", "", "")
		if err != nil {
			t.Fatal(err)
		}
		th := TodoThread{Channel: "feishu", ChatID: "oc_example", ThreadID: fmt.Sprintf("omt_example_%d", i)}
		if err := database.SetTodoThread(context.Background(), task.ID, th, now.Add(-10*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if _, err := s.bindThread(context.Background(), task.ID, th, nil); err != nil {
			t.Fatal(err)
		}
	}
	f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
		o := linkedFollowupObservation(raw, now, "om_followup", "ou_requester")
		thread := o["watch"].(map[string]any)["thread_id"]
		o["messages"].([]any)[0].(map[string]any)["thread_id"] = thread
		return o, nil
	})
	s.sweepDeploys(now)
	if n := len(f.appParams(AppConnectors, "connectors.lark_topic")); n != 4 {
		t.Fatalf("source pass reads=%d want 4", n)
	}
	s.sweepDeploys(now.Add(time.Minute))
	calls := f.appParams(AppConnectors, "connectors.lark_topic")
	threads := map[any]bool{}
	for _, c := range calls {
		threads[c["thread_id"]] = true
	}
	if len(calls) != 8 || len(threads) != 6 {
		t.Fatalf("unvisited topics starved: calls=%d unique=%d", len(calls), len(threads))
	}
	flushLinkedFollowup(s)
	if missing := strings.Count(strings.Join(f.notices(), "\n"), "the task has no agent"); missing != 5 {
		t.Fatalf("manually linked tasks did not report their missing Agent to main: %v", f.notices())
	}
}

func TestLinkedTopicFollowupUsesTheCurrentAgentAfterObservation(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, task, now := linkedFollowupTask(t, database, database)
	before := len(sentToAgent(f))
	f.answerAppBlocking(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
		for _, route := range []struct{ method, path string }{
			{http.MethodDelete, "/api/v1/todos/" + task.ID + "/agents/box/s1"},
			{http.MethodPut, "/api/v1/todos/" + task.ID + "/agents/box/s2"},
		} {
			if route.method == http.MethodPut {
				f.setSessions("box", proto.Session{MachineID: "box", SessionID: "s2", Agent: "codex", Status: proto.StatusIdle})
			}
			w := httptest.NewRecorder()
			s.ServeHTTP(w, httptest.NewRequest(route.method, route.path, nil))
			if w.Code != http.StatusOK {
				t.Errorf("change Agent: %d %s", w.Code, w.Body.String())
			}
		}
		return linkedFollowupObservation(raw, now, "om_followup", "ou_requester"), nil
	})
	s.sweepDeploys(now)
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.agentSent) != before+1 || f.agentSent[len(f.agentSent)-1]["session_id"] != "s2" {
		t.Fatalf("old observation destination used: %v", f.agentSent)
	}
}

func TestLinkedTopicFollowupPromptGolden(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, _, now := linkedFollowupTask(t, database, database)
	before := len(sentToAgent(f))
	f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
		return linkedFollowupObservation(raw, now, "om_followup", "ou_requester"), nil
	})
	s.sweepDeploys(now)
	if len(sentToAgent(f)) != before+1 {
		t.Fatal("no linked-topic source note reached the Agent")
	}
	note := strings.ReplaceAll(sentToAgent(f)[before], now.Add(-2*time.Minute).Format(time.RFC3339Nano), "2026-11-17T04:00:00Z")
	golden(t, "topic_followup", note)
}

func linkedFollowupStoredPending(t *testing.T, database *db.DB, id string) map[string]any {
	t.Helper()
	var raw json.RawMessage
	if err := database.QueryRow(context.Background(), `SELECT thread FROM todos WHERE id = $1`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var record map[string]any
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	followups, _ := record["followups"].(map[string]any)
	pending, _ := followups["pending"].(map[string]any)
	return pending
}

func TestLinkedTopicFollowupUnknownSourceReceiptIsRetriedNotAcknowledged(t *testing.T) {
	database := taskRecoveryDatabase(t)
	store := &linkedFollowupWriteDown{DB: database}
	s, f, task, now := linkedFollowupTask(t, database, store)
	f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
		return linkedFollowupObservation(raw, now, "om_followup", "ou_requester"), nil
	})
	s.sweepDeploys(now)
	store.readDown.Store(true)
	flushLinkedFollowup(s)
	holdDigest(s)
	if len(f.notices()) != 0 || !slices.Contains(pendingKinds(s, task.ID), "topic_followup") ||
		linkedFollowupStoredPending(t, database, task.ID)["supervisor_told"] == true {
		t.Fatal("unknown source receipt was treated as moot/delivered")
	}
	store.readDown.Store(false)
	flushLinkedFollowup(s)
	if len(f.notices()) != 1 || linkedFollowupStoredPending(t, database, task.ID)["supervisor_told"] != true {
		t.Fatal("source read recovery did not deliver and acknowledge the supervisor copy")
	}
}

func TestLinkedTopicFollowupSupervisorAckWriteIsRetryable(t *testing.T) {
	database := taskRecoveryDatabase(t)
	store := &linkedFollowupWriteDown{DB: database}
	s, f, task, now := linkedFollowupTask(t, database, store)
	f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
		return linkedFollowupObservation(raw, now, "om_followup", "ou_requester"), nil
	})
	s.sweepDeploys(now)
	store.down.Store(true)
	flushLinkedFollowup(s)
	holdDigest(s)
	if len(f.notices()) != 1 || linkedFollowupStoredPending(t, database, task.ID)["supervisor_told"] == true ||
		!slices.Contains(pendingKinds(s, task.ID), "topic_followup") {
		t.Fatal("lost acknowledgment was hidden or falsely committed")
	}
	// The supervisor took the digest, but the receipt write is still pending.
	// Age the stored fingerprint past the normal deduplication window.
	ctx := t.Context()
	history := map[string]superviseFingerprint{}
	if _, err := database.Setting(ctx, superviseDedupKey, &history); err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 {
		t.Fatalf("expected one confirmed delivery fingerprint, got %v", history)
	}
	for key, seen := range history {
		seen.At = now.Add(-2 * superviseDedupWindow)
		history[key] = seen
	}
	if err := database.PutSetting(ctx, superviseDedupKey, history); err != nil {
		t.Fatal(err)
	}
	store.down.Store(false)
	flushLinkedFollowup(s)
	if linkedFollowupStoredPending(t, database, task.ID)["supervisor_told"] != true {
		t.Fatal("acknowledgment was not recovered through the existing delivery/dedup path")
	}
	if got := len(f.notices()); got != 1 {
		t.Fatalf("confirmed supervisor delivery repeated after deduplication window: got %d notices, want 1", got)
	}
	s.sweepDeploys(now.Add(time.Minute))
	if pending := linkedFollowupStoredPending(t, database, task.ID); pending != nil {
		t.Fatalf("acknowledged pending receipt not cleared: %v", pending)
	}
}

func TestLinkedTopicFollowupProvisionalSentIsNotAnAck(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, task, now := linkedFollowupTask(t, database, database)
	f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
		return linkedFollowupObservation(raw, now, "om_followup", "ou_requester"), nil
	})
	s.sweepDeploys(now)
	id := task.ID + "|topic_followup"
	s.supervise.mu.Lock()
	g := s.supervise.pending[id]
	s.supervise.sent[id] = g.Key // sendDigest reserves this before the transport takes the message.
	s.supervise.mu.Unlock()
	s.sweepDeploys(now.Add(time.Minute))
	pending := linkedFollowupStoredPending(t, database, task.ID)
	if pending == nil || pending["supervisor_told"] == true || len(f.notices()) != 0 {
		t.Fatal("the provisional reservation discarded undelivered supervisor context")
	}
	s.supervise.mu.Lock()
	delete(s.supervise.sent, id)
	s.supervise.mu.Unlock()
	flushLinkedFollowup(s)
	if len(f.notices()) != 1 || linkedFollowupStoredPending(t, database, task.ID)["supervisor_told"] != true {
		t.Fatal("the actual supervisor delivery did not commit its acknowledgment")
	}
}

func TestLinkedTopicFollowupBusySupervisorKeepsTypedWorkPending(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, task, now := linkedFollowupTask(t, database, database)
	f.answerApp(AppConnectors, "connectors.lark_topic", func(raw json.RawMessage) (any, error) {
		return linkedFollowupObservation(raw, now, "om_followup", "ou_requester"), nil
	})
	s.sweepDeploys(now)
	f.setSessions("server", proto.Session{SessionID: "conv1", Assistant: true, Status: proto.StatusWorking})
	flushLinkedFollowup(s)
	holdDigest(s)
	if len(f.notices()) != 0 || s.queuedFor("conv1") != 0 ||
		!slices.Contains(pendingKinds(s, task.ID), "topic_followup") ||
		linkedFollowupStoredPending(t, database, task.ID)["supervisor_told"] == true {
		t.Fatal("busy supervisor received an unchecked rendered relay or a false acknowledgment")
	}
	if _, err := s.unbindThread(context.Background(), task.ID); err != nil {
		t.Fatal(err)
	}
	f.setSessions("server", proto.Session{SessionID: "conv1", Assistant: true, Status: proto.StatusIdle})
	flushLinkedFollowup(s)
	if len(f.notices()) != 0 {
		t.Fatal("a stale queued source wake survived unbinding")
	}
}
