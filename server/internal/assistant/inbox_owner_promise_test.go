package assistant

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/db"
)

type ownerPromiseTestStore struct {
	*inboxMemStore
	echo map[string]bool
}

func (s *ownerPromiseTestStore) HasSentAsUserMessage(_ context.Context, message string, _ time.Time) (bool, error) {
	return s.echo[message], nil
}

func TestOwnerPromisePrivateReminderAndActions(t *testing.T) {
	ctx := context.Background()
	h := newInboxHub(t)
	st := &ownerPromiseTestStore{inboxMemStore: h.st, echo: map[string]bool{"om_example_echo": true}}
	serviceOnStore := func() *Service {
		return New(Options{Hub: h.f, Store: func() Store { return st }, Logf: testLogf(t)})
	}
	s := serviceOnStore()
	h.in.svc = s
	now := time.Now()
	p := &connectorwire.OwnerPromise{ChatID: "oc_example_group", TopicID: "omt_example_topic", RequestID: "om_example_request"}
	candidate := connector.InboxThing{
		Thread: "owner-promise:example", Kind: connectorwire.OwnerPromiseKind, Last: "om_example_owner",
		At: now.Add(-16 * time.Minute), Preview: "I'll check the import.", Context: "Could you check this import?",
		OpenURL:      "https://applink.feishu.cn/client/thread/open?open_thread_id=omt_example_topic",
		OwnerPromise: p, Tags: []connectorwire.Tag{{Key: connectorwire.TagChat, Value: string(p.ChatID)}},
	}
	makeRound := func(extension bool, things ...connector.InboxThing) assistantwire.InboxRound {
		return assistantwire.InboxRound{Sources: []assistantwire.InboxSource{{ID: "larkcli", Name: "Example Lark", Extension: extension, Things: things}}}
	}
	collect := func(r assistantwire.InboxRound) {
		t.Helper()
		if err := s.InboxCollected(ctx, r); err != nil {
			t.Fatal(err)
		}
	}
	collect(makeRound(true, candidate))
	young := candidate
	young.At = now.Add(-10 * time.Minute)
	echo := candidate
	echo.Last, echo.Thread = "om_example_echo", "owner-promise:echo"
	collect(makeRound(false, young, echo))
	id := connectorInboxItemID("larkcli", candidate.Thread, candidate.Last)
	s.inbox.mu.Lock()
	if len(s.inbox.items) != 0 {
		t.Fatalf("an extension, premature, or echoed observation was admitted: %d items", len(s.inbox.items))
	}
	s.inbox.mu.Unlock()

	// A to-do in the same topic with different source work must not absorb the promise.
	unrelated, err := s.createPlainTodo(ctx, "Unrelated work", "", db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	s.todos.mu.Lock()
	s.todos.todos[unrelated.ID].Thread = &TodoThread{Channel: "feishu", ChatID: string(p.ChatID), ThreadID: string(p.TopicID)}
	s.todos.mu.Unlock()
	collect(makeRound(false, candidate))
	collect(makeRound(false, candidate))
	s.inbox.mu.Lock()
	item := s.inbox.items[id]
	if len(s.inbox.items) != 1 || item == nil || item.status() != StatusAction || item.Connector.CanReply ||
		item.Suggestion == nil || item.Suggestion.Kind != SuggestTodo || s.inbox.policyTwin(item) != TwinNever {
		t.Fatalf("wanted one owner-controlled card: %+v", item)
	}
	s.inbox.mu.Unlock()

	// The task write can succeed just before a restart. The second action
	// recovers that exact task and can finish filing the reminder.
	task, created, err := s.createTodoForInbox(ctx, id, "Check the import", candidate.OpenURL, db.TodoEdit{}, p, candidate.Last)
	if err != nil || !created {
		t.Fatalf("first action: created=%v err=%v", created, err)
	}
	taskAgain, created, err := s.createTodoForInbox(ctx, id, "Check again", "", db.TodoEdit{}, p, candidate.Last)
	if err != nil || created || taskAgain.ID != task.ID {
		t.Fatalf("repeat action created another task: %+v created=%v err=%v", taskAgain, created, err)
	}
	collect(makeRound(false, candidate))
	s.inbox.mu.Lock()
	if item.open() {
		t.Fatal("a durable task did not retire the reminder")
	}
	s.inbox.mu.Unlock()

	// Restart loads the closed Inbox receipt. Replaying this observation does
	// not restore an action or create another row.
	s = serviceOnStore()
	h.in.svc = s
	collect(makeRound(false, candidate))
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	if got := s.inbox.items[id]; got == nil || got.open() || len(s.inbox.items) != 1 {
		t.Fatalf("replay did not preserve the closed card: %+v", got)
	}
}

func TestOwnerPromiseUsesOnlyVerifiedTaskCoverage(t *testing.T) {
	h := newInboxHub(t)
	ctx := context.Background()
	p := &connectorwire.OwnerPromise{ChatID: "oc_example_group", TopicID: "omt_example_topic", RequestID: "om_example_request"}
	request, err := h.createTodoFrom(ctx, "Handle the request", "", connectorInboxItemID("larkcli",
		"larkcli:chat:"+string(p.ChatID)+":thread:"+string(p.TopicID), string(p.RequestID)), db.TodoEdit{})
	if err != nil {
		t.Fatal(err)
	}
	if !h.ownerPromiseCovered(p, "om_example_owner", "item") {
		t.Fatal("a linked open request did not cover the promise")
	}
	if _, _, err := h.createTodoForInbox(ctx, "item", "Duplicate", "", db.TodoEdit{}, p, "om_example_owner"); err == nil {
		t.Fatalf("the click did not recheck coverage: %v", err)
	}
	if err := h.deleteTodo(ctx, request.ID); err != nil && !errors.Is(err, db.ErrTodoNotFound) {
		t.Fatal(err)
	}
}

func TestOwnerPromiseOneClickOnPostgres(t *testing.T) {
	ctx := context.Background()
	database := todoTestDB(t)
	f := newFakeHub().withMachine()
	f.inbox = newFakeInbox()
	start := func() *Service {
		s := New(Options{Hub: f, Store: func() Store { return database }, Logf: testLogf(t)})
		f.inbox.svc = s
		return s
	}
	s := start()
	p := &connectorwire.OwnerPromise{ChatID: "oc_example_group", TopicID: "omt_example_topic"}
	topicURL := "https://applink.feishu.cn/client/thread/open?open_thread_id=omt_example_topic"
	thing := connector.InboxThing{Thread: "owner-promise:example", Kind: connectorwire.OwnerPromiseKind,
		Last: "om_example_promise", OwnerPromise: p, Preview: "I'll check the import.", OpenURL: topicURL,
		At: time.Now().Add(-17 * time.Minute), Tags: []connectorwire.Tag{{Key: connectorwire.TagChat, Value: string(p.ChatID)}}}
	ignored := thing
	ignored.Last, ignored.Thread = "om_example_ignore", "owner-promise:ignore"
	round := assistantwire.InboxRound{Sources: []assistantwire.InboxSource{{ID: "larkcli", Things: []connector.InboxThing{thing, ignored}}}}
	if err := s.InboxCollected(ctx, round); err != nil {
		t.Fatal(err)
	}
	id := connectorInboxItemID("larkcli", thing.Thread, thing.Last)
	path := "/api/v1/inbox/" + id + "/suggestion"
	const body = `{"version":1,"action":"confirm","text":"Check the example import"}`
	if status, reply := inboxCall(t, s, http.MethodPost, path, body); status != http.StatusOK {
		t.Fatalf("one-click confirmation: status=%d reply=%v", status, reply)
	}
	if status, reply := inboxCall(t, s, http.MethodPost, path, body); status == http.StatusOK {
		t.Fatalf("the used card could be confirmed again: %v", reply)
	}
	ignoredID := connectorInboxItemID("larkcli", ignored.Thread, ignored.Last)
	if status, reply := inboxCall(t, s, http.MethodPost, "/api/v1/inbox/"+ignoredID+"/suggestion", `{"version":1,"action":"reject"}`); status != http.StatusOK {
		t.Fatalf("one-click dismissal: status=%d reply=%v", status, reply)
	}
	s = start()
	if err := s.InboxCollected(ctx, round); err != nil {
		t.Fatal(err)
	}
	rows, err := database.ListTodos(ctx)
	if err != nil || len(rows) != 1 || rows[0].InboxItem != id || rows[0].Title != "Check the example import" ||
		!strings.Contains(rows[0].Notes, topicURL) {
		t.Fatalf("wanted exactly one source-linked to-do after reload: rows=%+v err=%v", rows, err)
	}
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	if it := s.inbox.items[id]; it == nil || it.open() || it.DoneBy != doneByTodo {
		t.Fatalf("replayed a handled reminder: %+v", it)
	}
	if it := s.inbox.items[ignoredID]; it == nil || it.open() || it.DoneBy != "dismissed" {
		t.Fatalf("replayed a dismissed reminder: %+v", it)
	}
}

func TestOwnerPromiseBorderlineClassificationUsesBoundedInput(t *testing.T) {
	ctx := context.Background()
	h := newInboxHub(t)
	st := &ownerPromiseTestStore{inboxMemStore: h.st}
	s := New(Options{Hub: h.f, Store: func() Store { return st }, Logf: testLogf(t)})
	h.in.svc = s
	model, calls := captureModel(t, `{"status":"none"}`)
	h.in.model = model
	p := &connectorwire.OwnerPromise{ChatID: "oc_example_group", TopicID: "omt_example_topic", RequestID: "om_example_request", Model: true}
	thing := connector.InboxThing{Thread: "owner-promise:example", Kind: connectorwire.OwnerPromiseKind,
		Last: "om_example_promise", OwnerPromise: p, Preview: "I've asked my AI to check.", Context: "Could you check this?",
		At: time.Now().Add(-17 * time.Minute), Tags: []connectorwire.Tag{{Key: connectorwire.TagChat, Value: string(p.ChatID)}}}
	round := assistantwire.InboxRound{Sources: []assistantwire.InboxSource{{ID: "larkcli", Things: []connector.InboxThing{thing}}}}
	for range 2 {
		if err := s.InboxCollected(ctx, round); err != nil {
			t.Fatal(err)
		}
	}
	if got := calls(); len(got) != 1 || !strings.Contains(got[0].User, `"owner_reply"`) ||
		strings.Contains(got[0].User, string(p.ChatID)) || strings.Contains(got[0].User, string(p.RequestID)) {
		t.Fatalf("wanted one two-snippet call without source metadata: %+v", got)
	}
	s.inbox.mu.Lock()
	defer s.inbox.mu.Unlock()
	if len(s.inbox.items) != 0 {
		t.Fatal("a rejected candidate made an inbox card")
	}
}
