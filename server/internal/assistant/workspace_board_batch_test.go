package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/workspaceboard"
)

// Count the persistence boundary, including a CAS conflict after the initial read.
type batchBoardStore struct {
	*memoryBoardStore
	saves atomic.Int32
	fail  error
}

func (s *batchBoardStore) SaveWorkspaceBoard(ctx context.Context, revision int64, next workspaceboard.State) (workspaceboard.State, error) {
	s.saves.Add(1)
	if s.fail != nil {
		return workspaceboard.State{}, s.fail
	}
	return s.memoryBoardStore.SaveWorkspaceBoard(ctx, revision, next)
}

func newBatchBoardStore() *batchBoardStore {
	s := &batchBoardStore{memoryBoardStore: newMemoryBoardStore()}
	for _, id := range []string{"a", "b", "c"} {
		s.state.Cards = append(s.state.Cards, workspaceboard.Card{ID: id, MachineID: "computer", WorkspaceID: id, Title: "Workspace " + id, Cwd: "/work/" + id, Source: "directory", ColumnID: "todo"})
	}
	return s
}

func TestWorkspaceBoardBatchSavesAndBroadcastsOnce(t *testing.T) {
	store := newBatchBoardStore()
	devices := newBoardDevs()
	var nodeCalls atomic.Int32
	devices.add("computer", func(context.Context, string, json.RawMessage) (any, error) {
		nodeCalls.Add(1)
		return nil, errors.New("batch must not call a node")
	})
	s, hub := newBoardService(t, store, devices)
	before := boardCall(s, "GET", "/api/v1/workspace-board", "", roleAnswer)
	if before.Code != 200 || !strings.Contains(before.Body.String(), `"batch_cards":true`) || !strings.Contains(before.Body.String(), `"can_write":false`) {
		t.Fatalf("capability/read-only response: %d %s", before.Code, before.Body)
	}
	body := `{"revision":0,"action":"batch_cards","changes":[{"id":"a","column_id":"done","pinned":true},{"id":"b","column_id":"done","before_id":"a","archived":true},{"id":"c","pinned":true}]}`
	response := boardCall(s, "POST", "/api/v1/workspace-board", body, roleFull)
	after := boardState(t, response)
	if !strings.Contains(response.Body.String(), `"batch_cards":true`) || after.Revision != 1 || store.saves.Load() != 1 {
		t.Fatalf("batch not one save: revision=%d saves=%d body=%s", after.Revision, store.saves.Load(), response.Body)
	}
	if after.Cards[1].ID != "b" || !after.Cards[1].Archived || after.Cards[1].ColumnID != "done" || after.Cards[2].ID != "a" || !after.Cards[2].Pinned || !after.Cards[0].Pinned {
		t.Fatalf("batch data: %+v", after)
	}
	hub.mu.Lock()
	events := append([]string(nil), hub.events...)
	hub.mu.Unlock()
	if !reflect.DeepEqual(events, []string{"workspace_board"}) || nodeCalls.Load() != 0 {
		t.Fatalf("side effects: events=%v node calls=%d", events, nodeCalls.Load())
	}
	// A replay cannot partially repeat the action or emit a second event.
	if got := boardCall(s, "POST", "/api/v1/workspace-board", body, roleFull); got.Code != 409 || store.saves.Load() != 1 {
		t.Fatalf("stale replay: %d %s saves=%d", got.Code, got.Body, store.saves.Load())
	}
	restarted, _ := newBoardService(t, store, nil)
	persisted := boardState(t, boardCall(restarted, "GET", "/api/v1/workspace-board", "", roleFull))
	if !reflect.DeepEqual(persisted, after) {
		t.Fatal("manager restart lost batch")
	}
}

func TestWorkspaceBoardBatchFailuresDoNotCommitOrBroadcast(t *testing.T) {
	valid := `{"revision":0,"action":"batch_cards","changes":[{"id":"a","archived":true},{"id":"b","pinned":true}]}`
	for _, tc := range []struct {
		name     string
		body     string
		role     string
		fail     error
		status   int
		attempts int32
	}{
		{"invalid middle", `{"revision":0,"action":"batch_cards","changes":[{"id":"a","archived":true},{"id":"missing","pinned":true},{"id":"c","pinned":true}]}`, roleFull, nil, 400, 0},
		{"unknown nested field", `{"revision":0,"action":"batch_cards","changes":[{"id":"a","action":"add_card","pinned":true}]}`, roleFull, nil, 400, 0},
		{"binding edit", `{"revision":0,"action":"batch_cards","changes":[{"id":"a","cwd":"/elsewhere","pinned":true}]}`, roleFull, nil, 400, 0},
		{"top-level field", `{"revision":0,"action":"batch_cards","id":"","changes":[{"id":"a","pinned":true}]}`, roleFull, nil, 400, 0},
		{"changes on single edit", `{"revision":0,"action":"update_card","id":"a","pinned":true,"changes":null}`, roleFull, nil, 400, 0},
		{"missing revision", `{"action":"batch_cards","changes":[{"id":"a","pinned":true}]}`, roleFull, nil, 400, 0},
		{"read only", valid, roleAnswer, nil, 403, 0},
		{"read conflict", strings.Replace(valid, `"revision":0`, `"revision":1`, 1), roleFull, nil, 409, 0},
		{"save conflict", valid, roleFull, db.ErrWorkspaceBoardConflict, 409, 1},
		{"store unavailable", valid, roleFull, errors.New("store unavailable"), 503, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := newBatchBoardStore()
			store.fail = tc.fail
			original, _ := store.WorkspaceBoard(context.Background())
			s, hub := newBoardService(t, store, nil)
			response := boardCall(s, "POST", "/api/v1/workspace-board", tc.body, tc.role)
			if response.Code != tc.status || store.saves.Load() != tc.attempts {
				t.Fatalf("status=%d saves=%d: %s", response.Code, store.saves.Load(), response.Body)
			}
			after, _ := store.WorkspaceBoard(context.Background())
			hub.mu.Lock()
			events := len(hub.events)
			hub.mu.Unlock()
			if !reflect.DeepEqual(original, after) || events != 0 {
				t.Fatalf("failed batch committed or broadcast: %+v events=%d", after, events)
			}
		})
	}
}

func TestWorkspaceBoardBatchRequestBounds(t *testing.T) {
	store := newBatchBoardStore()
	store.state.Cards = nil
	changes := make([]map[string]any, workspaceboard.MaxBatchCards)
	// Long IDs make a legal full-board request exceed the old 64 KiB limit.
	for i := range workspaceboard.MaxBatchCards {
		id := fmt.Sprintf("%04d-%s", i, strings.Repeat("x", 80))
		store.state.Cards = append(store.state.Cards, workspaceboard.Card{ID: id, MachineID: "computer", WorkspaceID: id, Title: "Workspace", Cwd: fmt.Sprintf("/work/%d", i), Source: "directory", ColumnID: "todo"})
		changes[i] = map[string]any{"id": id, "column_id": "done", "archived": true}
	}
	body, _ := json.Marshal(map[string]any{"revision": 0, "action": "batch_cards", "changes": changes})
	if len(body) <= 64<<10 {
		t.Fatal("fixture no longer exercises the expanded batch limit")
	}
	s, _ := newBoardService(t, store, nil)
	result := boardState(t, boardCall(s, "POST", "/api/v1/workspace-board", string(body), roleFull))
	if result.Revision != 1 || len(result.Cards) != workspaceboard.MaxBatchCards || store.saves.Load() != 1 {
		t.Fatalf("maximum batch failed: revision=%d cards=%d saves=%d", result.Revision, len(result.Cards), store.saves.Load())
	}
	for _, body := range []string{
		`{"revision":1,"action":"batch_cards","changes":[]}` + strings.Repeat(" ", 4<<20),
		`{"revision":1,"action":"update_card","id":"a","pinned":true}` + strings.Repeat(" ", 64<<10),
	} {
		if response := boardCall(s, "POST", "/api/v1/workspace-board", body, roleFull); response.Code != 413 {
			t.Fatalf("oversized body status=%d: %s", response.Code, response.Body)
		}
	}
	if store.saves.Load() != 1 {
		t.Fatal("oversized body reached persistence")
	}
}
