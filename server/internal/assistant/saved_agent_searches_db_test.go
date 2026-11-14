package assistant

import (
	"context"
	"encoding/json"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
)

// barrierSearchStore makes two managers read the same persisted revision before
// either submits its SQL CAS. A process-local mutex cannot make this test pass.
type barrierSearchStore struct {
	*db.DB
	ready   chan struct{}
	release chan struct{}
}

func (st *barrierSearchStore) Setting(ctx context.Context, key string, out any) (bool, error) {
	found, err := st.DB.Setting(ctx, key, out)
	if key == savedAgentSearchesKey {
		st.ready <- struct{}{}
		select {
		case <-st.release:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	return found, err
}
func TestSavedAgentSearchRealDatabaseCASAndRestart(t *testing.T) {
	database := taskRecoveryDatabase(t)
	ctx := context.Background()
	first, _ := savedSearchService(database)
	initial := savedSearchResponseOf(t, first, "GET", nil)
	if initial.Revision != "0" {
		t.Fatal(initial)
	}
	barrier := &barrierSearchStore{DB: database, ready: make(chan struct{}, 2), release: make(chan struct{})}
	a, ah := savedSearchService(barrier)
	b, bh := savedSearchService(barrier)
	results := make(chan int, 2)
	var wg sync.WaitGroup
	for i, service := range []*Service{a, b} {
		wg.Go(func() {
			raw, _ := json.Marshal(searchMutation("0", "add", map[string]any{"name": "Query " + strconv.Itoa(i), "query": "state:working"}))
			results <- serve(service, "POST", savedSearchPath, string(raw)).Code
		})
	}
	<-barrier.ready
	<-barrier.ready
	close(barrier.release)
	wg.Wait()
	close(results)
	counts := map[int]int{}
	for code := range results {
		counts[code]++
	}
	if counts[200] != 1 || counts[409] != 1 || len(ah.messages())+len(bh.messages()) != 1 {
		t.Fatalf("CAS not atomic: %v events=%d", counts, len(ah.messages())+len(bh.messages()))
	}
	restarted, _ := savedSearchService(database)
	saved := savedSearchResponseOf(t, restarted, "GET", nil)
	if saved.Revision != "1" || len(saved.Searches) != 1 {
		t.Fatalf("restart lost winner: %+v", saved)
	}
	var stored savedAgentSearchDocument
	if found, err := database.Setting(ctx, savedAgentSearchesKey, &stored); err != nil || !found || stored.Revision != 1 || !reflect.DeepEqual(stored.Searches, saved.Searches) {
		t.Fatalf("stored document: %+v %v", stored, err)
	}
	removed := savedSearchResponseOf(t, restarted, "POST", searchMutation("1", "delete", map[string]any{"id": saved.Searches[0].ID}))
	third, _ := savedSearchService(database)
	empty := savedSearchResponseOf(t, third, "GET", nil)
	if empty.Revision != "2" || len(empty.Searches) != 0 {
		t.Fatal(empty)
	}
	restored := savedSearchResponseOf(t, third, "POST", searchMutation(removed.Revision, "restore", map[string]any{"item": saved.Searches[0]}))
	if restored.Revision != "3" || !reflect.DeepEqual(restored.Searches, saved.Searches) {
		t.Fatalf("persistent undo: %+v", restored)
	}
	// Exercise the UPDATE branch of CompareSetting too, after the collection has
	// already been created, deleted and restored. Both managers read revision 3.
	updateBarrier := &barrierSearchStore{DB: database, ready: make(chan struct{}, 2), release: make(chan struct{})}
	left, leftHub := savedSearchService(updateBarrier)
	right, rightHub := savedSearchService(updateBarrier)
	type updateResult struct {
		code int
		name string
	}
	updates := make(chan updateResult, 2)
	for i, service := range []*Service{left, right} {
		wg.Go(func() {
			name := "Update winner " + strconv.Itoa(i)
			raw, _ := json.Marshal(searchMutation("3", "update", map[string]any{"id": saved.Searches[0].ID, "name": name, "query": "project:example"}))
			updates <- updateResult{serve(service, "POST", savedSearchPath, string(raw)).Code, name}
		})
	}
	<-updateBarrier.ready
	<-updateBarrier.ready
	close(updateBarrier.release)
	wg.Wait()
	close(updates)
	updateCounts, winner := map[int]int{}, ""
	for result := range updates {
		updateCounts[result.code]++
		if result.code == 200 {
			winner = result.name
		}
	}
	if updateCounts[200] != 1 || updateCounts[409] != 1 || len(leftHub.messages())+len(rightHub.messages()) != 1 {
		t.Fatalf("subsequent SQL CAS lost an update: %v", updateCounts)
	}
	kept := savedSearchResponseOf(t, third, "GET", nil)
	if kept.Revision != "4" || len(kept.Searches) != 1 || kept.Searches[0].Name != winner || kept.Searches[0].Query != "project:example" || !kept.Searches[0].CreatedAt.Equal(saved.Searches[0].CreatedAt) {
		t.Fatalf("winning update was not retained: %+v winner=%q", kept, winner)
	}

	// Revision strings round-trip beyond JavaScript's safe integer range.
	stored = savedAgentSearchDocument{Revision: 9007199254740993, Searches: restored.Searches}
	if err := database.PutSetting(ctx, savedAgentSearchesKey, stored); err != nil {
		t.Fatal(err)
	}
	final := savedSearchResponseOf(t, third, "POST", searchMutation("9007199254740993", "update", map[string]any{"id": saved.Searches[0].ID, "name": "Changed", "query": "is:error"}))
	if final.Revision != "9007199254740994" {
		t.Fatal(final)
	}
}

// PostgreSQL trigger errors prove that an HTTP failure leaves both INSERT and
// UPDATE paths unchanged. These triggers exist only in this test's own database.
func TestSavedAgentSearchRealDatabaseWriteFailure(t *testing.T) {
	for _, stage := range []string{"INSERT", "UPDATE"} {
		t.Run(stage, func(t *testing.T) {
			ctx := context.Background()
			dsn := newDatabase(t)
			database, err := db.Open(ctx, dsn, db.ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(database.Close)
			service, hub := savedSearchService(database)
			before := savedSearchResponseOf(t, service, "GET", nil)
			if stage == "UPDATE" {
				before = savedSearchResponseOf(t, service, "POST", searchMutation("0", "add", map[string]any{"name": "Existing", "query": "state:idle"}))
			}
			mutation := searchMutation(before.Revision, "add", map[string]any{"name": "New", "query": "state:working"})
			if stage == "UPDATE" {
				mutation = searchMutation(before.Revision, "update", map[string]any{"id": before.Searches[0].ID, "name": "Changed", "query": "is:error"})
			}
			raw, _ := json.Marshal(mutation)
			events := len(hub.messages())
			execIn(t, dsn, `CREATE FUNCTION reject_saved_agent_search() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'saved search write rejected'; END $$;
CREATE TRIGGER reject_saved_agent_search BEFORE `+stage+` ON settings FOR EACH ROW WHEN (NEW.key = 'agents.saved_searches') EXECUTE FUNCTION reject_saved_agent_search()`)
			failed := serve(service, "POST", savedSearchPath, string(raw))
			if failed.Code != 503 || !strings.Contains(failed.Body.String(), "storage_unavailable") {
				t.Fatalf("SQL failure misreported: %d %s", failed.Code, failed.Body)
			}
			if after := savedSearchResponseOf(t, service, "GET", nil); !reflect.DeepEqual(after, before) || len(hub.messages()) != events {
				t.Fatalf("failed SQL changed document/event: %+v events=%v", after, hub.messages())
			}
			execIn(t, dsn, `DROP TRIGGER reject_saved_agent_search ON settings`)
			retried := savedSearchResponseOf(t, service, "POST", mutation)
			expected, _ := strconv.ParseInt(before.Revision, 10, 64)
			if retried.Revision != strconv.FormatInt(expected+1, 10) || len(hub.messages()) != events+1 {
				t.Fatalf("retry did not commit exactly once: %+v", retried)
			}
			// Read through a new connection pool and a new manager instance.
			database.Close()
			reopened, err := db.Open(ctx, dsn, db.ManagerSet)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(reopened.Close)
			restarted, _ := savedSearchService(reopened)
			if after := savedSearchResponseOf(t, restarted, "GET", nil); !reflect.DeepEqual(after, retried) {
				t.Fatalf("reopened database lost the successful retry: %+v", after)
			}
		})
	}
}
