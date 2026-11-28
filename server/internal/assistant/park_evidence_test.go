package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
)

// Use the public tool's JSON contract so these tests still compile against the pre-change production code.
func parkEvidenceArgs(kind string) map[string]any {
	e := map[string]any{"kind": kind, "next_actor": "external", "target": "reviewer-a", "wake": "The named reviewer replies in the linked thread"}
	a := map[string]any{"verdict": "parked", "reason": "The requested review is outside the Agent's control",
		"waiting_on": "someone_else", "until": "3d", "evidence": e}
	switch kind {
	case "person_reply", "merge_review":
		e["contact"] = map[string]any{"channel": "project discussion", "url": "https://example.org/messages/request-1",
			"sender": "requester-a", "sent_at": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano),
			"request": "Please review the ready change and reply in this thread", "status": "sent"}
		if kind == "merge_review" {
			e["ready"], e["url"] = true, "https://github.com/example/project/pull/42"
		}
	case "external_event":
		a["waiting_on"] = "external_event"
		e["target"], e["url"], e["wake"] = "upstream check run 42", "https://example.org/checks/42", "The bound check run completes"
	case "date":
		a["waiting_on"], a["until"] = "date", time.Now().UTC().Add(72*time.Hour).Format(time.RFC3339Nano)
		e["target"], e["wake"] = "the announced application window", "The recorded opening date arrives"
	}
	return a
}

func parkEvidenceContact(a map[string]any) map[string]any {
	return a["evidence"].(map[string]any)["contact"].(map[string]any)
}

func parkEvidenceLedger(t *testing.T, s *Service, database *db.DB, id string) string {
	t.Helper()
	rows, err := database.ListTodoSupervision(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	feed, err := s.feedItems(context.Background(), "conv1", time.Time{})
	if err != nil {
		t.Fatal(err)
	}
	var reviews []FeedItem
	for _, it := range feed {
		if it.Kind == feedReview {
			reviews = append(reviews, it)
		}
	}
	raw, err := json.Marshal([]any{todoNow(s, id).Supervision, rows, reviews, pendingKinds(s, id)})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestParkEvidenceRefusesIncompleteOrUnsentBeforeMutation(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, f, todo := newSupervisedServiceOn(t, database)
	before := parkEvidenceLedger(t, s, database, todo.ID)
	pushes, notices, sends := len(f.pushed()), len(f.notices()), len(sentToAgent(f))
	for _, tc := range []struct {
		name string
		kind string
		edit func(map[string]any)
		want string
	}{
		{"missing evidence", "person_reply", func(a map[string]any) { delete(a, "evidence") }, "evidence"},
		{"null evidence", "person_reply", func(a map[string]any) { a["evidence"] = nil }, "evidence"},
		{"missing kind", "person_reply", func(a map[string]any) { delete(a["evidence"].(map[string]any), "kind") }, "evidence.kind"},
		{"internal work", "internal", nil, "evidence.kind"},
		{"owner choice kind", "owner_priority", nil, "evidence.kind"},
		{"blank target", "person_reply", func(a map[string]any) { a["evidence"].(map[string]any)["target"] = "  " }, "evidence.target"},
		{"missing wake", "person_reply", func(a map[string]any) { delete(a["evidence"].(map[string]any), "wake") }, "evidence.wake"},
		{"next actor missing", "external_event", func(a map[string]any) { delete(a["evidence"].(map[string]any), "next_actor") }, "evidence.next_actor"},
		{"next actor unknown", "external_event", func(a map[string]any) { a["evidence"].(map[string]any)["next_actor"] = "unknown" }, "evidence.next_actor"},
		{"next actor owner on an external event", "external_event", func(a map[string]any) {
			a["reason"] = "The owner must request approval before the job can run"
			e := a["evidence"].(map[string]any)
			e["next_actor"], e["target"], e["wake"] = "owner", "approval request", "The approval is recorded"
		}, "only the owner can take the next step: use needs_user"},
		{"next actor owner on a date wait", "date", func(a map[string]any) {
			a["evidence"].(map[string]any)["next_actor"] = "owner"
		}, "only the owner can take the next step: use needs_user"},
		{"no contact", "person_reply", func(a map[string]any) { delete(a["evidence"].(map[string]any), "contact") }, "evidence.contact"},
		{"no channel", "person_reply", func(a map[string]any) { delete(parkEvidenceContact(a), "channel") }, "contact.channel"},
		{"no sender", "person_reply", func(a map[string]any) { delete(parkEvidenceContact(a), "sender") }, "contact.sender"},
		{"no question", "person_reply", func(a map[string]any) { delete(parkEvidenceContact(a), "request") }, "contact.request"},
		{"no sent time", "person_reply", func(a map[string]any) { delete(parkEvidenceContact(a), "sent_at") }, "contact.sent_at"},
		{"future sent time", "person_reply", func(a map[string]any) {
			parkEvidenceContact(a)["sent_at"] = time.Now().Add(time.Hour).Format(time.RFC3339)
		}, "contact.sent_at"},
		{"no receipt link", "person_reply", func(a map[string]any) { delete(parkEvidenceContact(a), "url") }, "contact.url"},
		{"channel homepage", "person_reply", func(a map[string]any) { parkEvidenceContact(a)["url"] = "https://example.org/" }, "contact.url"},
		{"unsafe link", "person_reply", func(a map[string]any) { parkEvidenceContact(a)["url"] = "javascript:alert(1)" }, "contact.url"},
		{"credentials in link", "person_reply", func(a map[string]any) {
			parkEvidenceContact(a)["url"] = "https://name:secret@example.org/messages/1"
		}, "contact.url"},
		{"draft", "person_reply", func(a map[string]any) { parkEvidenceContact(a)["status"] = "draft" }, "contact.status"},
		{"failed send", "person_reply", func(a map[string]any) { parkEvidenceContact(a)["status"] = "failed" }, "contact.status"},
		{"unknown send", "person_reply", func(a map[string]any) { parkEvidenceContact(a)["status"] = "unknown" }, "contact.status"},
		{"missing outcome", "person_reply", func(a map[string]any) { delete(parkEvidenceContact(a), "status") }, "contact.status"},
		{"coarse kind disagrees", "person_reply", func(a map[string]any) { a["waiting_on"] = "external_event" }, "waiting_on must be"},
		{"owner priority", "person_reply", func(a map[string]any) { a["waiting_on"] = "owner_priority" }, "use needs_user"},
		{"review not ready", "merge_review", func(a map[string]any) { a["evidence"].(map[string]any)["ready"] = false }, "evidence.ready"},
		{"review without source", "merge_review", func(a map[string]any) { delete(a["evidence"].(map[string]any), "url") }, "evidence.url"},
		{"review link is not a PR", "merge_review", func(a map[string]any) {
			a["evidence"].(map[string]any)["url"] = "https://github.com/example/project/issues/42"
		}, "evidence.url"},
		{"event without object", "external_event", func(a map[string]any) { delete(a["evidence"].(map[string]any), "url") }, "evidence.url"},
		{"implicit date", "date", func(a map[string]any) { delete(a, "until") }, "explicit RFC 3339"},
		{"relative date", "date", func(a map[string]any) { a["until"] = "3d" }, "explicit RFC 3339"},
		{"evidence on another verdict", "person_reply", func(a map[string]any) {
			a["verdict"] = "cancelled"
			delete(a, "waiting_on")
			delete(a, "until")
		}, "parked only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := parkEvidenceArgs(tc.kind)
			if tc.edit != nil {
				tc.edit(a)
			}
			_, err := reviewErr(s, todo.ID, a)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unsupported parking was not refused with %q: %v", tc.want, err)
			}
			if after := parkEvidenceLedger(t, s, database, todo.ID); before != after {
				t.Fatalf("refused parking changed the persisted ledger, review feed or pending signals\nbefore: %s\nafter: %s", before, after)
			}
			if len(f.pushed()) != pushes || len(f.notices()) != notices || len(sentToAgent(f)) != sends {
				t.Fatal("refused parking notified someone or sent the Agent an instruction")
			}
		})
	}
}

func TestParkEvidenceAllKindsSurviveDatabaseReload(t *testing.T) {
	for _, kind := range []string{"person_reply", "merge_review", "external_event", "date"} {
		t.Run(kind, func(t *testing.T) {
			database := taskRecoveryDatabase(t)
			s, f, todo := newSupervisedServiceOn(t, database)
			a := parkEvidenceArgs(kind)
			e := a["evidence"].(map[string]any)
			e["recorded_by"] = "not-the-recording-conversation"
			pushes, sends := len(f.pushed()), len(sentToAgent(f))
			if _, err := reviewErr(s, todo.ID, a); err != nil {
				t.Fatal(err)
			}
			e["recorded_by"] = "conv1"
			assertEvidence := func(service *Service) {
				t.Helper()
				listed := listedTodos(t, service)[todo.ID]
				v, _ := listed["supervision"].(map[string]any)
				p, _ := v["parked"].(map[string]any)
				got, _ := p["evidence"].(map[string]any)
				if v["state"] != "parked" || p["waiting_on"] != a["waiting_on"] || !reflect.DeepEqual(got, e) {
					t.Fatalf("parked evidence missing/changed: state=%v parked=%v want=%v", v["state"], p, e)
				}
				reviews, _ := v["reviews"].([]any)
				if len(reviews) == 0 || !reflect.DeepEqual(reviews[len(reviews)-1].(map[string]any)["evidence"], e) {
					t.Fatalf("the review ledger lost evidence: %v", reviews)
				}
				if listed["ask"] != nil || listed["done_at"] != nil {
					t.Fatalf("parking asked the owner or completed the task: %v", listed)
				}
			}
			assertEvidence(s)
			again, f2 := restartedOn(t, database)
			if err := again.ensureTodosLoaded(context.Background()); err != nil {
				t.Fatal(err)
			}
			assertEvidence(again)
			rows, err := database.ListTodoSupervision(context.Background())
			if err != nil || len(rows) != 1 {
				t.Fatalf("persisted ledgers=%v err=%v", rows, err)
			}
			var stored []map[string]any
			if err := json.Unmarshal(rows[0].Reviews, &stored); err != nil || len(stored) != 1 || !reflect.DeepEqual(stored[0]["evidence"], e) {
				t.Fatalf("evidence not stored in existing Reviews JSON: %s err=%v", rows[0].Reviews, err)
			}
			if len(f.pushed()) != pushes || len(sentToAgent(f)) != sends || len(f2.pushed()) != 0 || len(sentToAgent(f2)) != 0 {
				t.Fatal("parking/reload sent a request or invented a notification")
			}
		})
	}
}

func TestParkEvidenceAcceptsReadyGitLabReviewReceipt(t *testing.T) {
	s, _, todo := newSupervisedService(t)
	a := parkEvidenceArgs("merge_review")
	a["evidence"].(map[string]any)["url"] = "https://git.example.org/group/project/-/merge_requests/42"
	if _, err := reviewErr(s, todo.ID, a); err != nil {
		t.Fatal(err)
	}
}

func TestParkEvidenceLegacyReadsDoNotInventContacts(t *testing.T) {
	for _, waitingOn := range []string{"owner_priority", "someone_else", "external_event", "date"} {
		t.Run(waitingOn, func(t *testing.T) {
			database := taskRecoveryDatabase(t)
			s, _, todo := newSupervisedServiceOn(t, database)
			at := time.Now().UTC().Add(-time.Hour)
			_, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool {
				v.State = "parked"
				v.Reviews = []TaskReview{{At: at, Verdict: "parked", Reason: "Legacy external wait", WaitingOn: waitingOn,
					Until: at.Add(72 * time.Hour), Parks: 2}}
				return true
			})
			if err != nil {
				t.Fatal(err)
			}
			again, f := restartedOn(t, database)
			listed := listedTodos(t, again)[todo.ID]
			v, _ := listed["supervision"].(map[string]any)
			p, _ := v["parked"].(map[string]any)
			if v["state"] != "parked" || p["waiting_on"] != waitingOn || p["count"] != float64(2) || p["evidence"] != nil {
				t.Fatalf("legacy evidence was invented or its park was changed: %v", v)
			}
			if len(f.pushed()) != 0 || len(f.notices()) != 0 || len(sentToAgent(f)) != 0 {
				t.Fatal("merely loading legacy parking woke someone")
			}
		})
	}
}

func TestParkEvidenceReadsLegacyTypedWaitWithoutNextActor(t *testing.T) {
	database := taskRecoveryDatabase(t)
	s, _, todo := newSupervisedServiceOn(t, database)
	const saved = `{"kind":"external_event","target":"upstream run","wake":"The run finishes","url":"https://example.org/runs/1","recorded_by":"conv1"}`
	var evidence ParkEvidence
	if err := json.Unmarshal([]byte(saved), &evidence); err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Hour)
	if _, _, err := s.updateSupervision(context.Background(), todo.ID, func(v *Supervision) bool {
		v.State = "parked"
		v.Reviews = []TaskReview{{At: at, Verdict: "parked", Reason: "Wait for the run", WaitingOn: "external_event",
			Until: at.Add(24 * time.Hour), Parks: 1, Evidence: &evidence}}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	again, f := restartedOn(t, database)
	listed := listedTodos(t, again)[todo.ID]
	v := listed["supervision"].(map[string]any)
	p := v["parked"].(map[string]any)
	var want map[string]any
	if err := json.Unmarshal([]byte(saved), &want); err != nil {
		t.Fatal(err)
	}
	if v["state"] != "parked" || !reflect.DeepEqual(p["evidence"], want) || listed["ask"] != nil || len(f.pushed()) != 0 {
		t.Fatalf("loading a typed legacy park changed its state or evidence, or asked the owner: %v", listed)
	}
}

func TestParkEvidenceMechanicalWaitDoesNotInventReceipt(t *testing.T) {
	s, _, todo, c, now := mechanicalFixture(t)
	handled, retry := s.mechanicalSignal(context.Background(),
		superviseSignal{Todo: todo.ID, Kind: sigIdle, Machine: c.MachineID, Session: c.SessionID, Key: "turn-1"}, now)
	v := todoNow(s, todo.ID).Supervision
	if handled || !retry.IsZero() || v.State != "" || len(v.Reviews) != 0 {
		t.Fatalf("a PR alone silently parked the task without a contact receipt: handled=%v retry=%v ledger=%+v", handled, retry, v)
	}
}

func TestParkSelfTodoURLRefusesOwnTaskURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		url  string
		want bool
	}{
		{"task page", "https://hub.example.com/todos/todo_abc", true},
		{"task page with query and fragment", "https://hub.example.com/todos/todo_abc?tab=reviews#top", true},
		{"task api path", "https://hub.example.com/api/v1/todos/todo_abc", true},
		{"different task page", "https://hub.example.com/todos/todo_other", false},
		{"worker session page", "https://hub.example.com/s/box/sess_1", false},
		{"external run", "https://github.com/org/repo/actions/runs/123", false},
	} {
		if got := parkSelfTodoURL("todo_abc", tc.url); got != tc.want {
			t.Fatalf("%s: parkSelfTodoURL(%q) = %v, want %v", tc.name, tc.url, got, tc.want)
		}
	}

	s, f, todo := newSupervisedService(t)
	before := todoNow(s, todo.ID).Supervision
	pushes, notices, sends := len(f.pushed()), len(f.notices()), len(sentToAgent(f))
	a := parkEvidenceArgs("external_event")
	a["evidence"].(map[string]any)["url"] = "https://hub.example.com/todos/" + todo.ID
	_, err := reviewErr(s, todo.ID, a)
	if err == nil || !strings.Contains(err.Error(), "own page") {
		t.Fatalf("self-referencing external_event URL was not refused: %v", err)
	}
	if after := todoNow(s, todo.ID).Supervision; !reflect.DeepEqual(before, after) {
		t.Fatalf("refused self-URL parking changed supervision state\nbefore: %+v\nafter: %+v", before, after)
	}
	if len(f.pushed()) != pushes || len(f.notices()) != notices || len(sentToAgent(f)) != sends {
		t.Fatal("refused self-URL parking notified someone or sent the Agent an instruction")
	}
}
