package extension

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
)

// The to-do events (#5279) are told to the extensions that subscribed AND hold the todos.read grant. The user agrees to
// a manifest's permissions as a whole, so a record granted less than the manifest declares is not expected; if one
// comes about all the same (a record edited by hand, a bug), the grant counts, not the declaration. One granted
// todos.write only, or another scope, hears nothing.
func TestTodoEventSubscribersNeedTheGrantedScope(t *testing.T) {
	r := NewRegistry(t.TempDir(), nil, nil, 1)
	for _, tc := range []struct {
		id        string
		grant     Permissions
		subscribe string
	}{
		{"read", Permissions{Hub: []string{ScopeTodosRead}}, `"todo.changed","todo.decision"`},
		{"read-and-write", Permissions{Hub: []string{ScopeTodosRead, ScopeTodosWrite}}, `"todo.decision"`},
		{"write", Permissions{Hub: []string{ScopeTodosWrite}}, `"todo.changed","todo.decision"`},
		// defensive: the manifest declares todos.read, the record holds none
		{"declared-not-granted", Permissions{}, `"todo.changed","todo.decision"`},
		// another scope
		{"kb-only", Permissions{Hub: []string{ScopeKBRead}}, `"todo.changed"`},
		{"legacy", Permissions{HubAPI: true}, `"todo.changed","todo.decision"`},
		{"not-subscribed", Permissions{Hub: []string{ScopeTodosRead}}, `"agent.finished"`},
	} {
		m := &Manifest{ID: tc.id, Version: "1.0.0", Permissions: Permissions{Hub: []string{ScopeTodosRead, ScopeTodosWrite}},
			Contributes: map[string]json.RawMessage{"events": json.RawMessage(`[{"id":"example","subscribe":[` + tc.subscribe + `]}]`)}}
		if err := r.AddBuiltin(m, t.TempDir(), true); err != nil {
			t.Fatal(err)
		}
		r.mu.Lock()
		r.extensions[tc.id].Record.Granted = tc.grant
		r.mu.Unlock()
	}
	for topic, want := range map[string][]string{
		"todo.changed":  {"legacy", "read"},
		"todo.decision": {"legacy", "read", "read-and-write"},
	} {
		if got := r.EventSubscribers(topic, EventScopes[topic]); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: told %v, want %v", topic, got, want)
		}
	}
	if EventScopes["todo.changed"] != ScopeTodosRead || EventScopes["todo.decision"] != ScopeTodosRead {
		t.Fatalf("the scope of the to-do events: %v", EventScopes)
	}
	for topic := range EventScopes {
		if _, ok := EventTopics[topic]; !ok {
			t.Errorf("%s needs a scope but is no event", topic)
		}
	}
}

// Subscribing to a to-do event is asked about at install, and again when an upgrade adds one.
func TestTodoEventTopicsAreDeclaredAndRequireConsent(t *testing.T) {
	old := with(t, func(m map[string]any) {
		m["activation_events"] = []any{"on_startup"}
		m["contributes"] = map[string]any{"events": []any{map[string]any{"id": "todos", "subscribe": []any{"agent.finished"}}}}
	})
	next := with(t, func(m map[string]any) {
		m["activation_events"] = []any{"on_startup"}
		m["permissions"] = map[string]any{"hub": []any{"todos.read"}}
		m["contributes"] = map[string]any{"events": []any{map[string]any{"id": "todos", "subscribe": []any{"todo.changed", "todo.decision"}}}}
	})
	before, err := Parse(old)
	if err != nil {
		t.Fatal(err)
	}
	after, err := Parse(next)
	if err != nil {
		t.Fatal(err)
	}
	if len(after.Topics()) != 2 {
		t.Fatalf("topics: %v", after.Topics())
	}
	b, _ := json.Marshal(Added(before, after))
	if !strings.Contains(string(b), permissionText(i18n.Default, "event_todo_changed")) || !strings.Contains(string(b), permissionText(i18n.Default, "event_todo_decision")) {
		t.Fatalf("new subscriptions need consent: %s", b)
	}
	if _, err := Parse(with(t, func(m map[string]any) {
		m["activation_events"] = []any{"on_startup"}
		m["contributes"] = map[string]any{"events": []any{map[string]any{"id": "todos", "subscribe": []any{"todo.deleted"}}}}
	})); err == nil {
		t.Fatal("an event that does not exist was accepted")
	}
}

// Every event topic has its scope settled here, in so many words: a topic that needs none says "". An event added to
// EventTopics without a line here, or without the scope it needs in EventScopes, fails, rather than going to every
// subscriber unchecked.
func TestEveryEventTopicHasItsScopeSettled(t *testing.T) {
	want := map[string]string{
		"assistant.entries.changed": ScopeAssistantEntriesRead,
		"kb.changed":                ScopeKBRead,
		"kb.forgotten":              ScopeKBRead,
		"todo.changed":              ScopeTodosRead,
		"todo.decision":             ScopeTodosRead,
		"leader.changed":            ScopeLeadersRead,
		"leader.report":             ScopeLeadersRead,
		"placement.pending":         ScopeTasksSchedule,
		"lark.messages":             ScopeLarkMessagesRead,
		"inbox.changed":             "",
		"agent.needs_you":           "",
		"agent.finished":            "",
	}
	for topic := range EventTopics {
		scope, ok := want[topic]
		if !ok {
			t.Errorf("event %q: say in this test which scope it needs (\"\" for none), and put it in EventScopes", topic)
			continue
		}
		if got := EventScopes[topic]; got != scope {
			t.Errorf("event %q: EventScopes says %q, want %q", topic, got, scope)
		}
	}
	for topic := range want {
		if _, ok := EventTopics[topic]; !ok {
			t.Errorf("event %q: listed here, not an event", topic)
		}
	}
	for topic := range EventScopes {
		if _, ok := EventTopics[topic]; !ok {
			t.Errorf("EventScopes has %q, which is not an event", topic)
		}
	}
}
