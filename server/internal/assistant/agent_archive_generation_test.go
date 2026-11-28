package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

type archiveGenerationWrite struct {
	record  agentArchive
	present bool
}

// archiveGenerationStore records the durable order of one session's archive writes.
type archiveGenerationStore struct {
	*db.DB
	key string

	mu     sync.Mutex
	writes []archiveGenerationWrite
}

func (s *archiveGenerationStore) PutSetting(ctx context.Context, key string, value any) error {
	if err := s.DB.PutSetting(ctx, key, value); err != nil {
		return err
	}
	if key == agentArchivesKey {
		records, ok := value.(map[string]agentArchive)
		if ok {
			rec, present := records[s.key]
			s.mu.Lock()
			s.writes = append(s.writes, archiveGenerationWrite{record: rec, present: present})
			s.mu.Unlock()
		}
	}
	return nil
}

func (s *archiveGenerationStore) history() []archiveGenerationWrite {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]archiveGenerationWrite(nil), s.writes...)
}

// archiveGenerationCloseFailureStore fails only the close metadata write after the archive itself is durable.
type archiveGenerationCloseFailureStore struct {
	*fleetMem
	archiveWrites int
}

func (s *archiveGenerationCloseFailureStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == agentArchivesKey {
		s.archiveWrites++
		if s.archiveWrites == 2 {
			return errors.New("close metadata write failed")
		}
	}
	return s.fleetMem.PutSetting(ctx, key, value)
}

func archiveGenerationDrainNative(s *Service, sid string) {
	s.nativeArchiveAwaited(context.Background(), "mac", sid)
}

func archiveGenerationFixture(t *testing.T, row proto.Session) (*fleetTest, *db.DB, *archiveGenerationStore) {
	t.Helper()
	database, err := db.Open(t.Context(), newMigratedDatabase(t, db.ManagerSet), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(database.Close)
	key := runtimeListID("mac", row.SessionID)
	store := &archiveGenerationStore{DB: database, key: key}
	ft := newFleetTestOn(t, store)
	t.Cleanup(func() { archiveGenerationDrainNative(ft.s, row.SessionID) })
	ft.f.mu.Lock()
	ft.f.sessions["mac"] = nil
	ft.f.mu.Unlock()
	row.MachineID = "mac"
	ft.d.setSessions("mac", []proto.Session{row})
	return ft, database, store
}

func archiveGenerationPut(t *testing.T, database *db.DB, rec agentArchive) {
	t.Helper()
	var records map[string]agentArchive
	if _, err := database.Setting(t.Context(), agentArchivesKey, &records); err != nil {
		t.Fatal(err)
	}
	if records == nil {
		records = map[string]agentArchive{}
	}
	records[runtimeListID(rec.Session.MachineID, rec.Session.SessionID)] = rec
	if err := database.PutSetting(t.Context(), agentArchivesKey, records); err != nil {
		t.Fatal(err)
	}
}

func archiveGenerationStored(t *testing.T, database *db.DB, sid string) (agentArchive, bool) {
	t.Helper()
	var records map[string]agentArchive
	if _, err := database.Setting(t.Context(), agentArchivesKey, &records); err != nil {
		t.Fatal(err)
	}
	rec, ok := records[runtimeListID("mac", sid)]
	return rec, ok
}

func archiveGenerationRestart(t *testing.T, database *db.DB, row proto.Session) (*fleetTest, agentArchive, bool) {
	t.Helper()
	restarted := newFleetTestOn(t, database)
	t.Cleanup(func() { archiveGenerationDrainNative(restarted.s, row.SessionID) })
	restarted.d.setSessions("mac", []proto.Session{row})
	if _, _, err := restarted.s.archivedRow(t.Context(), "mac", row.SessionID); err != nil {
		t.Fatal(err)
	}
	rec, ok := restarted.s.agentArchives.records[runtimeListID("mac", row.SessionID)]
	return restarted, rec, ok
}

func archiveGenerationAfter(t *testing.T, operation string, previous, next time.Time) {
	t.Helper()
	if !next.After(previous) {
		t.Errorf("%s did not strictly advance the archive change marker: previous=%s next=%s", operation, previous.Format(time.RFC3339Nano), next.Format(time.RFC3339Nano))
	}
}

func TestAgentArchiveGenerationExplicitConcurrentRestart(t *testing.T) {
	row := proto.Session{MachineID: "mac", SessionID: "explicit-generation", Agent: "codex", Status: proto.StatusOffline,
		State: proto.StateOffline, UpdatedAt: time.Now().Add(-2 * time.Hour).UTC()}
	ft, database, store := archiveGenerationFixture(t, row)

	// A persisted marker ahead of the wall clock models a clock that was set back.
	seed := agentArchive{Session: archiveSnapshot(row), Archived: false, ChangedAt: time.Now().Add(24 * time.Hour).UTC()}
	archiveGenerationPut(t, database, seed)

	const goroutines = 6
	pattern := []bool{true, true, false, false}
	start := make(chan struct{})
	errs := make(chan error, goroutines*len(pattern))
	var wg sync.WaitGroup
	for range goroutines {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for _, archived := range pattern {
				if err := ft.s.noteAgentArchive(t.Context(), row, archived); err != nil {
					errs <- err
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("explicit archive/restore: %v", err)
		}
	}

	history := store.history()
	if len(history) != goroutines*len(pattern) {
		t.Fatalf("persisted writes = %d, want %d", len(history), goroutines*len(pattern))
	}
	previous := seed.ChangedAt
	var sawArchive, sawRestore, reported bool
	for i, write := range history {
		if !write.present {
			t.Fatalf("explicit mutation %d removed the record", i)
		}
		sawArchive = sawArchive || write.record.Archived
		sawRestore = sawRestore || !write.record.Archived
		if !write.record.ChangedAt.After(previous) && !reported {
			archiveGenerationAfter(t, "concurrent repeated explicit mutation", previous, write.record.ChangedAt)
			reported = true
		}
		previous = write.record.ChangedAt
	}
	if !sawArchive || !sawRestore {
		t.Fatalf("mutations did not include both archive and restore: archive=%v restore=%v", sawArchive, sawRestore)
	}
	last := history[len(history)-1].record
	persisted, ok := archiveGenerationStored(t, database, row.SessionID)
	if !ok || !persisted.ChangedAt.Equal(last.ChangedAt) || persisted.Archived != last.Archived {
		t.Fatalf("last concurrent mutation was not durable: persisted=%+v present=%v last=%+v", persisted, ok, last)
	}

	restarted, loaded, ok := archiveGenerationRestart(t, database, row)
	if !ok || !loaded.ChangedAt.Equal(last.ChangedAt) || loaded.Archived != last.Archived {
		t.Fatalf("restart lost the last mutation: loaded=%+v present=%v last=%+v", loaded, ok, last)
	}
	if err := restarted.s.noteAgentArchive(t.Context(), row, !last.Archived); err != nil {
		t.Fatal(err)
	}
	afterRestart, ok := archiveGenerationStored(t, database, row.SessionID)
	if !ok {
		t.Fatal("mutation after restart removed the record")
	}
	archiveGenerationAfter(t, "explicit mutation after PostgreSQL manager restart", last.ChangedAt, afterRestart.ChangedAt)
}

func TestAgentArchiveGenerationAutomaticConcurrentReadersRestart(t *testing.T) {
	row := proto.Session{MachineID: "mac", SessionID: "automatic-generation", Agent: "claude", Status: proto.StatusOffline,
		State: proto.StateOffline, UpdatedAt: time.Now().Add(-2 * time.Hour).UTC()}
	ft, database, store := archiveGenerationFixture(t, row)
	if err := ft.s.noteAgentArchive(t.Context(), row, true); err != nil {
		t.Fatal(err)
	}
	archived, ok := archiveGenerationStored(t, database, row.SessionID)
	if !ok || !archived.Archived {
		t.Fatalf("archive fixture: %+v present=%v", archived, ok)
	}
	archived.ChangedAt = time.Now().Add(24 * time.Hour).UTC()
	archiveGenerationPut(t, database, archived)

	// Restart once so the automatic readers must use the marker ahead of the wall clock.
	ft = newFleetTestOn(t, store)
	active := row
	active.UpdatedAt = time.Now().UTC()
	ft.d.setSessions("mac", []proto.Session{active})
	before := len(store.history())
	mark := ft.s.archiveMark()
	const readers = 12
	type readResult struct {
		rows    []proto.Session
		changed bool
		err     error
	}
	start := make(chan struct{})
	results := make(chan readResult, readers)
	for range readers {
		go func() {
			<-start
			rows, changed, err := ft.s.archivedSessions(t.Context(), "mac", []proto.Session{active}, mark)
			results <- readResult{rows: rows, changed: changed, err: err}
		}()
	}
	close(start)
	changed := 0
	for range readers {
		result := <-results
		if result.err != nil {
			t.Fatalf("competing list reader: %v", result.err)
		}
		if result.changed {
			changed++
		}
		for _, got := range result.rows {
			if got.SessionID == row.SessionID && got.Archived {
				t.Errorf("competing reader lost the automatic restore: %+v", got)
			}
		}
	}
	if changed != 1 {
		t.Errorf("automatic restore commits = %d, want exactly one", changed)
	}
	history := store.history()
	if len(history) != before+1 {
		t.Fatalf("automatic restore writes = %d, want one (all writes %d)", len(history)-before, len(history))
	}
	restoreWrite := history[len(history)-1]
	if !restoreWrite.present || restoreWrite.record.Archived {
		t.Fatalf("automatic restore persisted the wrong state: %+v present=%v", restoreWrite.record, restoreWrite.present)
	}
	archiveGenerationAfter(t, "automatic restoration", archived.ChangedAt, restoreWrite.record.ChangedAt)

	restarted, loaded, ok := archiveGenerationRestart(t, database, active)
	if !ok || loaded.Archived || !loaded.ChangedAt.Equal(restoreWrite.record.ChangedAt) {
		t.Fatalf("restart regressed the automatic restore: loaded=%+v present=%v restore=%+v", loaded, ok, restoreWrite.record)
	}
	rows, changedAfterRestart, err := restarted.s.archivedSessions(t.Context(), "mac", []proto.Session{active}, restarted.s.archiveMark())
	if err != nil {
		t.Fatal(err)
	}
	if changedAfterRestart || len(rows) != 1 || rows[0].Archived || rows[0].ArchiveChangedAt == nil || !rows[0].ArchiveChangedAt.Equal(restoreWrite.record.ChangedAt) {
		t.Fatalf("restart did not preserve the restore ordering: changed=%v rows=%+v", changedAfterRestart, rows)
	}
}

func TestAgentArchiveGenerationForceRollbackFencingRestart(t *testing.T) {
	for _, tc := range []struct {
		name     string
		mutate   bool
		archived bool
	}{
		{name: "rollback"},
		{name: "stale_rollback_after_restore", mutate: true, archived: false},
		{name: "stale_rollback_after_rearchive", mutate: true, archived: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			row := proto.Session{MachineID: "mac", SessionID: "force-rollback-" + tc.name, Agent: "claude", Status: proto.StatusBlocked,
				State: proto.StateNeedsYou, UpdatedAt: time.Now().Add(-2 * time.Hour).UTC()}
			ft, database, _ := archiveGenerationFixture(t, row)
			seed := agentArchive{Session: archiveSnapshot(row), Archived: false, ChangedAt: time.Now().Add(24 * time.Hour).UTC()}
			archiveGenerationPut(t, database, seed)

			closeStarted := make(chan struct{})
			finishClose := make(chan struct{})
			closeErr := errors.New("archive generation close refused")
			ft.d.mu.Lock()
			ft.d.machines["mac"].call = func(method string, _ json.RawMessage) (any, error) {
				if method == "session.close" {
					close(closeStarted)
					<-finishClose
					return nil, closeErr
				}
				if method == deviceSessionArchive || method == deviceSessionUnarchive {
					return map[string]any{"native": nativeArchiveDone}, nil
				}
				return map[string]any{}, nil
			}
			ft.d.mu.Unlock()
			done := make(chan error, 1)
			go func() { done <- ft.s.forceArchiveConfirmation(t.Context(), "mac", row.SessionID) }()
			<-closeStarted

			provisional, ok := archiveGenerationStored(t, database, row.SessionID)
			if !ok || !provisional.Archived || !provisional.ClosedAt.After(time.Now()) {
				t.Fatalf("force close did not persist its provisional fence: %+v present=%v", provisional, ok)
			}
			archiveGenerationAfter(t, "force-close provisional archive", seed.ChangedAt, provisional.ChangedAt)
			newer := provisional
			if tc.mutate {
				if err := ft.s.noteAgentArchive(t.Context(), row, tc.archived); err != nil {
					t.Fatal(err)
				}
				newer, ok = archiveGenerationStored(t, database, row.SessionID)
				if !ok || newer.Archived != tc.archived {
					t.Fatalf("newer mutation during close: %+v present=%v", newer, ok)
				}
				archiveGenerationAfter(t, "mutation during force close", provisional.ChangedAt, newer.ChangedAt)
			}
			close(finishClose)
			if err := <-done; !errors.Is(err, closeErr) {
				t.Fatalf("force close error = %v, want %v", err, closeErr)
			}

			final, ok := archiveGenerationStored(t, database, row.SessionID)
			if !ok {
				t.Fatalf("rollback erased the session's durable archive state (newer mutation=%v)", tc.mutate)
			}
			if final.Archived != tc.archived || !final.ClosedAt.IsZero() {
				t.Errorf("rollback state: archived=%v closed_at=%s, want archived=%v and no close fence", final.Archived, final.ClosedAt, tc.archived)
			}
			if tc.mutate {
				if !final.ChangedAt.Equal(newer.ChangedAt) {
					t.Errorf("stale rollback changed the newer generation: newer=%s final=%s", newer.ChangedAt, final.ChangedAt)
				}
			} else {
				archiveGenerationAfter(t, "force-close rollback", newer.ChangedAt, final.ChangedAt)
			}

			restarted, loaded, ok := archiveGenerationRestart(t, database, row)
			if !ok || loaded.Archived != tc.archived || !loaded.ClosedAt.IsZero() || !loaded.ChangedAt.Equal(final.ChangedAt) {
				t.Fatalf("restart lost rollback state: loaded=%+v present=%v final=%+v", loaded, ok, final)
			}
			if err := restarted.s.noteAgentArchive(t.Context(), row, !tc.archived); err != nil {
				t.Fatal(err)
			}
			afterRestart, ok := archiveGenerationStored(t, database, row.SessionID)
			if !ok {
				t.Fatal("mutation after rollback restart removed the record")
			}
			archiveGenerationAfter(t, "mutation after rollback restart", final.ChangedAt, afterRestart.ChangedAt)
		})
	}
}

func TestAgentArchiveGenerationCloseCompletionFencingRestart(t *testing.T) {
	for _, closePath := range []string{"ordinary", "force"} {
		for _, mutation := range []struct {
			name     string
			archived bool
		}{
			{name: "restore", archived: false},
			{name: "rearchive", archived: true},
		} {
			t.Run(closePath+"/"+mutation.name, func(t *testing.T) {
				row := proto.Session{MachineID: "mac", SessionID: "close-" + closePath + "-" + mutation.name, Agent: "codex", Title: "open",
					PaneID: "w1:p1", StartedByPersona: true, Status: proto.StatusIdle, State: proto.StateIdle, UpdatedAt: time.Now().Add(-time.Hour).UTC()}
				if closePath == "force" {
					row.Status, row.State = proto.StatusBlocked, proto.StateNeedsYou
				}
				ft, database, _ := archiveGenerationFixture(t, row)
				native := playNativeNode(ft, "mac")
				closeStarted := make(chan struct{})
				finishClose := make(chan struct{})
				if closePath == "ordinary" {
					ft.f.answerNode(deviceSessionRetire, func(_ string, _ json.RawMessage) (any, error) {
						close(closeStarted)
						<-finishClose
						return map[string]any{"closed": true}, nil
					})
				} else {
					ft.d.mu.Lock()
					nativeCall := ft.d.machines["mac"].call
					ft.d.machines["mac"].call = func(method string, params json.RawMessage) (any, error) {
						if method == "session.close" {
							close(closeStarted)
							<-finishClose
							return map[string]any{}, nil
						}
						return nativeCall(method, params)
					}
					ft.d.mu.Unlock()
				}

				type closeResult struct {
					result agentArchiveCloseResult
					err    error
				}
				done := make(chan closeResult, 1)
				go func() {
					if closePath == "ordinary" {
						res, err := ft.s.archiveAndCloseAgentSession(t.Context(), "mac", row.SessionID)
						done <- closeResult{result: res, err: err}
						return
					}
					done <- closeResult{err: ft.s.forceArchiveConfirmation(t.Context(), "mac", row.SessionID)}
				}()
				<-closeStarted

				started, ok := archiveGenerationStored(t, database, row.SessionID)
				if !ok || !started.Archived {
					t.Fatalf("close did not start from an archived generation: %+v present=%v", started, ok)
				}
				if err := ft.s.noteAgentArchive(t.Context(), row, mutation.archived); err != nil {
					t.Fatal(err)
				}
				newer, ok := archiveGenerationStored(t, database, row.SessionID)
				if !ok || newer.Archived != mutation.archived {
					t.Fatalf("newer close-race mutation: %+v present=%v", newer, ok)
				}
				archiveGenerationAfter(t, "mutation during "+closePath+" close", started.ChangedAt, newer.ChangedAt)

				intent := ft.s.nativeArchiveAwaited(t.Context(), "mac", row.SessionID)
				if intent == nil || intent.State != nativeArchiveDone || intent.Archived != mutation.archived {
					t.Fatalf("newer native intent before close completion: %+v", intent)
				}
				callsBefore := native.calls()
				// The worker may coalesce the ordinary close's initial archive wish into
				// the newer wish before its first node call.
				if len(callsBefore) == 0 || closePath == "force" && len(callsBefore) != 1 || closePath == "ordinary" && len(callsBefore) > 2 {
					t.Fatalf("native calls before close completion = %v", callsBefore)
				}
				wantMethod := deviceSessionUnarchive
				if mutation.archived {
					wantMethod = deviceSessionArchive
				}
				if got := callsBefore[len(callsBefore)-1]; got != wantMethod+" "+row.SessionID {
					t.Fatalf("newer native request = %q, want %q", got, wantMethod+" "+row.SessionID)
				}

				close(finishClose)
				closed := <-done
				if closed.err != nil || closePath == "ordinary" && !closed.result.Closed {
					t.Fatalf("close completion: result=%+v err=%v", closed.result, closed.err)
				}
				intent = ft.s.nativeArchiveAwaited(t.Context(), "mac", row.SessionID)
				callsAfter := native.calls()
				if len(callsAfter) != len(callsBefore) {
					t.Errorf("stale close completion issued native requests: new calls=%v all calls=%v", callsAfter[len(callsBefore):], callsAfter)
				}
				if intent == nil || intent.State != nativeArchiveDone || intent.Archived != mutation.archived {
					t.Errorf("stale close completion replaced the newer native intent: %+v, want archived=%v done", intent, mutation.archived)
				}
				final, ok := archiveGenerationStored(t, database, row.SessionID)
				if !ok || final.Archived != mutation.archived {
					t.Fatalf("close completion lost the newer state: final=%+v present=%v", final, ok)
				}
				if !final.ChangedAt.Equal(newer.ChangedAt) {
					t.Errorf("close completion changed the newer generation: newer=%s final=%s", newer.ChangedAt, final.ChangedAt)
				}
				if !final.ClosedAt.Equal(newer.ClosedAt) {
					t.Errorf("close completion overwrote ClosedAt: newer=%s final=%s", newer.ClosedAt, final.ClosedAt)
				}

				restarted, loaded, ok := archiveGenerationRestart(t, database, row)
				if !ok || loaded.Archived != mutation.archived || !loaded.ChangedAt.Equal(newer.ChangedAt) || !loaded.ClosedAt.Equal(newer.ClosedAt) {
					t.Errorf("restart lost the fenced close outcome: loaded=%+v present=%v newer=%+v", loaded, ok, newer)
				}
				if err := restarted.s.loadNativeArchives(t.Context()); err != nil {
					t.Fatal(err)
				}
				intent = restarted.s.nativeArchiveView("mac", row.SessionID)
				if intent == nil || intent.State != nativeArchiveDone || intent.Archived != mutation.archived {
					t.Errorf("restart lost the newer native intent: %+v, want archived=%v done", intent, mutation.archived)
				}
			})
		}
	}
}

func TestAgentArchiveGenerationForcePublishUsesCompletionMark(t *testing.T) {
	row := proto.Session{MachineID: "mac", SessionID: "force-publish-after-completion", Agent: "codex", Status: proto.StatusBlocked,
		State: proto.StateNeedsYou, UpdatedAt: time.Now().Add(-time.Hour).UTC()}
	ft, database, _ := archiveGenerationFixture(t, row)
	playNativeNode(ft, "mac")
	closeStarted := make(chan struct{})
	finishClose := make(chan struct{})
	ft.d.mu.Lock()
	ft.d.machines["mac"].call = func(method string, _ json.RawMessage) (any, error) {
		if method == "session.close" {
			close(closeStarted)
			<-finishClose
		}
		return map[string]any{"native": nativeArchiveDone}, nil
	}
	ft.d.mu.Unlock()
	done := make(chan error, 1)
	go func() { done <- ft.s.forceArchiveConfirmation(t.Context(), "mac", row.SessionID) }()
	<-closeStarted
	started, ok := archiveGenerationStored(t, database, row.SessionID)
	if !ok || !started.Archived {
		t.Fatalf("missing provisional archive: %+v present=%v", started, ok)
	}

	// Let close completion save its metadata, then hold its publish while a newer re-archive commits.
	ft.s.fleet.sentMu.Lock()
	locked := true
	defer func() {
		if locked {
			ft.s.fleet.sentMu.Unlock()
		}
	}()
	close(finishClose)
	waitUntil(t, func() bool {
		rec, present := archiveGenerationStored(t, database, row.SessionID)
		return present && rec.ChangedAt.Equal(started.ChangedAt) && !rec.ClosedAt.IsZero()
	})
	if err := ft.s.noteAgentArchive(t.Context(), row, true); err != nil {
		t.Fatal(err)
	}
	newer, ok := archiveGenerationStored(t, database, row.SessionID)
	if !ok || !newer.Archived || !newer.ClosedAt.IsZero() {
		t.Fatalf("newer re-archive: %+v present=%v", newer, ok)
	}
	archiveGenerationAfter(t, "re-archive after close completion", started.ChangedAt, newer.ChangedAt)
	ft.s.fleet.sentMu.Unlock()
	locked = false
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	final, ok := archiveGenerationStored(t, database, row.SessionID)
	if !ok || !final.Archived || !final.ChangedAt.Equal(newer.ChangedAt) || !final.ClosedAt.Equal(newer.ClosedAt) {
		t.Fatalf("stale close publish restored the newer archive: final=%+v present=%v newer=%+v", final, ok, newer)
	}
	_, loaded, ok := archiveGenerationRestart(t, database, row)
	if !ok || !loaded.Archived || !loaded.ChangedAt.Equal(newer.ChangedAt) || !loaded.ClosedAt.Equal(newer.ClosedAt) {
		t.Fatalf("restart lost the re-archive after stale publish: loaded=%+v present=%v newer=%+v", loaded, ok, newer)
	}
}

func TestAgentArchiveGenerationOrdinaryCloseRetriesNativeAfterMetadataFailure(t *testing.T) {
	ft, row := closeTestService(t, func(s *proto.Session) { s.Agent = "codex" })
	store := &archiveGenerationCloseFailureStore{fleetMem: ft.st}
	ft.s.setStore(func() Store { return store })
	t.Cleanup(func() { archiveGenerationDrainNative(ft.s, row.SessionID) })
	native := playNativeNode(ft, "mac")
	terminalClosed := make(chan struct{})
	native.set(func(string) (any, error) {
		select {
		case <-terminalClosed:
			return map[string]any{"native": nativeArchiveDone}, nil
		default:
			return nil, errors.New("terminal still open")
		}
	})
	initialPending := false
	ft.f.answerNode(deviceSessionRetire, func(_ string, _ json.RawMessage) (any, error) {
		intent := ft.s.nativeArchiveAwaited(context.Background(), "mac", row.SessionID)
		initialPending = intent != nil && intent.State == nativeArchivePending
		close(terminalClosed)
		return map[string]any{"closed": true}, nil
	})
	res, err := ft.s.archiveAndCloseAgentSession(t.Context(), "mac", row.SessionID)
	if err != nil || !res.Closed || !initialPending {
		t.Fatalf("close=%+v err=%v initial native pending=%v", res, err, initialPending)
	}
	intent := ft.s.nativeArchiveAwaited(t.Context(), "mac", row.SessionID)
	if calls := native.calls(); len(calls) != 2 {
		t.Fatalf("native calls after close metadata failure = %v, want the initial try and immediate retry", calls)
	}
	if intent == nil || intent.State != nativeArchiveDone || !intent.Archived {
		t.Fatalf("native archive was not retried after close metadata failure: %+v", intent)
	}
}

func TestAgentArchiveGenerationOrdinaryCloseReadErrorDoesNotReplaceRestore(t *testing.T) {
	ft, row := closeTestService(t, func(s *proto.Session) { s.Agent = "codex" })
	native := playNativeNode(ft, "mac")
	t.Cleanup(func() { archiveGenerationDrainNative(ft.s, row.SessionID) })
	var restoreErr error
	ft.f.answerNode(deviceSessionRetire, func(_ string, _ json.RawMessage) (any, error) {
		restoreErr = ft.s.noteAgentArchive(context.Background(), row, false)
		ft.s.nativeArchiveAwaited(context.Background(), "mac", row.SessionID)
		store := &archiveFailStore{fleetMem: ft.st, failRead: true}
		ft.s.setStore(func() Store { return store })
		ft.s.agentArchives.loaded = false // force close completion to fail before it can inspect the newer restore
		return map[string]any{"closed": true}, nil
	})
	res, err := ft.s.archiveAndCloseAgentSession(t.Context(), "mac", row.SessionID)
	if err != nil || !res.Closed || restoreErr != nil {
		t.Fatalf("close=%+v err=%v restore=%v", res, err, restoreErr)
	}
	calls := native.calls()
	if len(calls) == 0 || calls[len(calls)-1] != deviceSessionUnarchive+" "+row.SessionID {
		t.Fatalf("restore was not the last native request before close completion: %v", calls)
	}
	intent := ft.s.nativeArchiveAwaited(t.Context(), "mac", row.SessionID)
	if after := native.calls(); len(after) != len(calls) {
		t.Fatalf("close completion retried without reading the newer generation: before=%v after=%v", calls, after)
	}
	if intent == nil || intent.State != nativeArchiveDone || intent.Archived {
		t.Fatalf("close metadata read error replaced the newer restore: %+v", intent)
	}
}

func TestAgentArchiveGenerationCancelledTaskPublishesAfterMetadataFailure(t *testing.T) {
	ft, row := closeTestService(t, func(s *proto.Session) { s.Status, s.State = proto.StatusOffline, proto.StateOffline })
	store := &archiveGenerationCloseFailureStore{fleetMem: ft.st}
	ft.s.setStore(func() Store { return store })
	t.Cleanup(func() { archiveGenerationDrainNative(ft.s, row.SessionID) })
	ctx := t.Context()
	todo, err := ft.s.createTodo(ctx, "Cancelled task", "", "conv1", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := ft.s.linkTodoAgent(ctx, todo.ID, "mac", row.SessionID, row.Title, "/code", row.Agent); err != nil {
		t.Fatal(err)
	}
	if closed, err := ft.s.reviewDoneTodo(ctx, todo.ID, "Cancelled", false); err != nil || !closed {
		t.Fatalf("cancel: %v, %v", closed, err)
	}
	before := len(ft.toldOf(assistantwire.EventFleetSessions))
	ft.s.archiveRetiredTaskAgent(ctx, doneTaskAgent{
		todo: todo.ID, machine: "mac", session: row.SessionID, doneAt: *todoNow(ft.s, todo.ID).DoneAt,
	}, row)
	after := len(ft.toldOf(assistantwire.EventFleetSessions))
	if after != before+1 {
		t.Fatalf("fleet publishes after committed archive = %d, want %d (metadata error must not suppress publish)", after-before, 1)
	}
	if !archivedIDs(t, ft.s)[row.SessionID] {
		t.Fatal("cancelled task's archive was not committed")
	}
}
