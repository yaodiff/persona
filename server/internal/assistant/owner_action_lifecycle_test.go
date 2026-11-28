package assistant

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// TestPersistentOwnerActionSurvivesFormerDeadlineCallerAndRestart exercises the
// complete SQL restore, original-card inventory, Feishu mapping, caller wait and
// connector gate. The default run backdates a card beyond two minutes. The final
// acceptance run sets PERSONA_OWNER_APPROVAL_WALL_CLOCK=1 to wait two real
// minutes instead; only the clock changes, not the assertions or production path.
func TestPersistentOwnerActionSurvivesFormerDeadlineCallerAndRestart(t *testing.T) {
	needsLarkLayer(t) // the chat is the lark module's
	for _, finalAllow := range []bool{true, false} {
		name := "Deny executes nothing"
		if finalAllow {
			name = "Allow executes at most once"
		}
		t.Run(name, func(t *testing.T) {
			url := newMigratedDatabase(t, db.AllSets)
			database, err := db.Open(t.Context(), url, db.AllSets)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(database.Close)
			f, _ := feishuHub("ou_owner", time.Unix(100, 0))
			on := true
			f.settings = &Settings{MachineID: "server", Main: "m1", OwnerReplyApproval: &on, PersistentOwnerActionCards: &on}
			s := newServiceOn(t, f, database)
			book, ok := newOwnerActionBook(s.store())
			if !ok {
				t.Fatal("real database lacks owner-action CAS")
			}
			ext := &ownerActionConnector{svc: s}
			s.SetExtensions(ext)
			args := json.RawMessage(`{"items":[{"tool":"mail_send","input":{"to":"a@example.test","body":"unchanged"}}]}`)
			ctx, cancelCaller := context.WithCancel(context.Background())
			prepared := make(chan string, 1)
			prepareErr := make(chan error, 1)
			go func() {
				text, err := s.toolOwnerActionPrepare(ctx, "machine-1", "session-1", args)
				prepared <- text
				prepareErr <- err
			}()
			cards := waitOwnerActionCards(t, s, 2)
			var batchIX proto.Interaction
			ids := make([]string, 0, 2)
			for _, ix := range cards {
				ids = append(ids, ix.ID)
				if ix.ToolName == ownerActionBatchTool {
					batchIX = ix
				}
				keepChatCard(s, ix.ID, &botCard{MessageID: "om_" + ix.ID, Ix: ix, At: time.Now()})
			}
			if batchIX.ID == "" {
				t.Fatalf("no batch card in %+v", cards)
			}
			cancelCaller()
			select {
			case err := <-prepareErr:
				if err != nil {
					t.Fatal(err)
				}
				if text := <-prepared; !strings.Contains(text, "still pending") {
					t.Fatalf("caller departure did not preserve the proposal: %q", text)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("caller departure did not release its tool wait")
			}
			assertPersistentOwnerSurfaces(t, s, book, batchIX.ApprovalBatchID, ids)

			created := time.Now().Add(-3 * time.Minute).UTC().Truncate(time.Microsecond)
			formerDeadline := created.Add(time.Minute)
			wallClock := os.Getenv("PERSONA_OWNER_APPROVAL_WALL_CLOCK") == "1"
			if wallClock {
				created = time.Now().UTC().Truncate(time.Microsecond)
				formerDeadline = created.Add(time.Minute)
			}
			conn, err := pgx.Connect(t.Context(), url)
			if err != nil {
				t.Fatal(err)
			}
			for _, id := range ids {
				_, err = conn.Exec(t.Context(), `UPDATE interactions SET created_at = $2, payload = jsonb_set(
					jsonb_set(payload, '{created_at}', to_jsonb($2::timestamptz), true),
					'{expires_at}', to_jsonb($3::timestamptz), true) WHERE id = $1`, id, created, formerDeadline)
				if err != nil {
					_ = conn.Close(context.Background())
					t.Fatal(err)
				}
			}
			if err := conn.Close(context.Background()); err != nil {
				t.Fatal(err)
			}
			// Keep every durable representation of this occurrence consistent:
			// strict terminal writes use both the SQL column and payload, while
			// projection uses the owner book's creation time.
			doc, err := book.read(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			for i := range doc.Batches {
				if doc.Batches[i].ID == batchIX.ApprovalBatchID {
					doc.Batches[i].CreatedAt = created
				}
			}
			doc.Revision++
			saved, err := database.CompareSetting(t.Context(), ownerActionBookKey, doc.Revision-1, doc)
			if err != nil || !saved {
				t.Fatalf("backdate the durable owner occurrence: saved=%v err=%v", saved, err)
			}
			for _, id := range ids {
				updateChatCard(s, id, func(card *botCard) {
					card.Ix.CreatedAt, card.Ix.ExpiresAt = created, formerDeadline
				})
			}

			restarted := newServiceOn(t, f, database)
			ext.svc = restarted
			restarted.SetExtensions(ext)
			var endedMu sync.Mutex
			ended := map[string]int{}
			decisions := map[string][]proto.Resolution{}
			restarted.fleetEnded = func(ix proto.Interaction, resolution *proto.Resolution) {
				endedMu.Lock()
				ended[ix.ID]++
				if resolution != nil {
					decisions[ix.ID] = append(decisions[ix.ID], *resolution)
				}
				endedMu.Unlock()
			}
			restarted.restoreInteractions(context.Background())
			assertPersistentOwnerSurfaces(t, restarted, book, batchIX.ApprovalBatchID, ids)

			// A reconnecting caller must not revive the stale timestamp as a timer.
			waitCtx, cancelWait := context.WithCancel(context.Background())
			defer cancelWait()
			waitDone := make(chan error, 1)
			go func() {
				_, err := restarted.waitInteraction(waitCtx, batchIX)
				waitDone <- err
			}()
			observation := 75 * time.Millisecond // let an incorrectly restored past-deadline timer fire
			if wallClock {
				observation = 2 * time.Minute
			}
			timer := time.NewTimer(observation)
			defer timer.Stop()
			select {
			case err := <-waitDone:
				t.Fatalf("former deadline ended the persistent waiter: %v", err)
			case <-timer.C:
			case <-t.Context().Done():
				t.Fatalf("persistent waiter observation: %v", t.Context().Err())
			}
			assertPersistentOwnerSurfaces(t, restarted, book, batchIX.ApprovalBatchID, ids)
			cancelWait()
			select {
			case err := <-waitDone:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("departing restored caller did not release its wait")
			}
			assertPersistentOwnerSurfaces(t, restarted, book, batchIX.ApprovalBatchID, ids)

			if err := restarted.resolveApproved(context.Background(), batchIX.ID, proto.Resolution{Allow: &finalAllow}, nil); err != nil {
				t.Fatal(err)
			}
			ownerActionPoll(t, 3*time.Second, func() bool {
				endedMu.Lock()
				complete := len(ended) == len(ids)
				endedMu.Unlock()
				return complete && len(restarted.pendingInteractions()) == 0
			})
			endedMu.Lock()
			for _, id := range ids {
				if ended[id] != 2 {
					t.Errorf("surface result for %s published %d events, want one decision and one close", id, ended[id])
				}
				got := decisions[id]
				if len(got) != 1 || got[0].Allow == nil || *got[0].Allow != finalAllow {
					t.Errorf("surface decision for %s = %+v, want exactly one Allow=%v", id, got, finalAllow)
				}
			}
			endedMu.Unlock()
			if pending := restarted.pendingInteractions(); len(pending) != 0 {
				t.Fatalf("decided proposal remains on the original surface: %+v", pending)
			}
			for _, id := range ids {
				if ref, ok := chatCardRef(restarted, id); ok {
					t.Errorf("decided Feishu proposal %s retains its pending mapping: %+v", id, ref)
				}
			}

			// A granted call passes the existing connector gate without a second card;
			// a denied or already consumed call must first get a fresh ordinary card.
			callDone := make(chan struct{})
			go func() {
				defer close(callDone)
				_, _ = ext.CallTool(context.Background(), "machine-1", "session-1", "mail_send", json.RawMessage(`{"to":"a@example.test","body":"unchanged"}`), true)
			}()
			wantExecutions := 0
			if finalAllow {
				wantExecutions = 1
				select {
				case <-callDone:
				case <-time.After(3 * time.Second):
					t.Fatal("granted connector call did not finish")
				}
				if pending := restarted.pendingInteractions(); len(pending) != 0 {
					t.Fatalf("Allow created a duplicate approval card: %+v", pending)
				}
				if got := len(ext.executions()); got != 1 {
					t.Fatalf("Allow produced %d executions, want one", got)
				}
				// The same grant cannot execute a second time.
				callDone = make(chan struct{})
				go func() {
					defer close(callDone)
					_, _ = ext.CallTool(context.Background(), "machine-1", "session-1", "mail_send", json.RawMessage(`{"to":"a@example.test","body":"unchanged"}`), true)
				}()
			}
			fresh := waitOrdinaryOwnerActionCards(t, restarted, 1)[0]
			if got := len(ext.executions()); got != wantExecutions {
				t.Fatalf("executions before the fresh ordinary decision = %d, want %d", got, wantExecutions)
			}
			no := false
			if err := restarted.resolve(fresh.ID, proto.Resolution{Allow: &no}); err != nil {
				t.Fatal(err)
			}
			select {
			case <-callDone:
			case <-time.After(3 * time.Second):
				t.Fatal("fresh ordinary Deny did not release the connector call")
			}
			if got := len(ext.executions()); got != wantExecutions {
				t.Fatalf("final executions = %d, want %d", got, wantExecutions)
			}
		})
	}
}

func TestOwnerActionConcurrentConsumersPostgres(t *testing.T) {
	url := newMigratedDatabase(t, db.AllSets)
	first, err := db.Open(t.Context(), url, db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(first.Close)
	second, err := db.Open(t.Context(), url, db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(second.Close)
	bookA, ok := newOwnerActionBook(repositorystore.New(first))
	if !ok {
		t.Fatal("first manager lacks owner-action CAS")
	}
	bookB, ok := newOwnerActionBook(repositorystore.New(second))
	if !ok {
		t.Fatal("second manager lacks owner-action CAS")
	}
	batch, err := bookA.createPersistent(t.Context(), "machine-1", "session-1", []ownerActionDraft{
		{Tool: "mail_send", Input: json.RawMessage(`{"to":"a@example.test","body":"once"}`)},
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bookA.decide(t.Context(), validOwnerProof(batch.ID, ""), true); err != nil {
		t.Fatal(err)
	}

	const callers = 24
	start := make(chan struct{})
	errs := make(chan error, callers)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for i := range callers {
		book := bookA
		if i%2 == 1 {
			book = bookB
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			granted, err := book.consume(t.Context(), batch.MachineID, batch.SessionID, "mail_send", json.RawMessage(batch.Items[0].Canonical))
			if err != nil {
				errs <- err
				return
			}
			if granted {
				successes.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Errorf("independent PostgreSQL consumer: %v", err)
	}
	if got := successes.Load(); got != 1 {
		t.Fatalf("independent PostgreSQL consumers received %d grants, want one", got)
	}
	granted, err := bookB.consume(t.Context(), batch.MachineID, batch.SessionID, "mail_send", json.RawMessage(batch.Items[0].Canonical))
	if err != nil || granted {
		t.Fatalf("post-restart duplicate consumption: granted=%v err=%v", granted, err)
	}
}

func assertPersistentOwnerSurfaces(t *testing.T, s *Service, book *ownerActionBook, batchID string, ids []string) {
	t.Helper()
	pending := s.pendingInteractions()
	gotIDs := make([]string, 0, len(pending))
	for _, ix := range pending {
		gotIDs = append(gotIDs, ix.ID)
		if !persistentOwnerProposal(ix) {
			t.Errorf("restored card lost persistence metadata: %+v", ix)
		}
	}
	slices.Sort(gotIDs)
	wantIDs := slices.Clone(ids)
	slices.Sort(wantIDs)
	if !slices.Equal(gotIDs, wantIDs) {
		t.Fatalf("original pending cards = %v, want %v", gotIDs, wantIDs)
	}
	for _, id := range ids {
		ref, ok := chatCardRef(s, id)
		if !ok || !ref.OwnerProposal || !ref.NoExpiry || ref.MessageID == "" {
			t.Errorf("Feishu proposal %s missing or not persistent: %+v", id, ref)
		}
	}
	batch, found, err := book.batch(context.Background(), batchID)
	if err != nil || !found {
		t.Fatalf("durable batch read: found=%v err=%v", found, err)
	}
	for _, item := range batch.Items {
		if item.State != ownerActionPending {
			t.Errorf("unanswered item %s is %s", item.ID, item.State)
		}
	}
}
