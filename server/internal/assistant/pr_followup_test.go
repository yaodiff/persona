package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistant/repositorystore"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	fupPR   = "https://github.com/o/r/pull/77"
	fupTodo = "todo_followup_case"
	fupSup  = "conv_supervisor"
)

// followupEnv builds a service on a real Postgres store with a forge that returns a scripted PR status.
func followupEnv(t *testing.T, status TodoPRStatus) (*Service, *statusForge) {
	t.Helper()
	st := repositorystore.New(todoTestDB(t))
	s, _ := newQuietServiceOn(t, st)
	forge := &statusForge{status: status}
	s.deploy.forge = forge
	return s, forge
}

func init() { followupRecheck = 0 } // let every test sweep poll immediately rather than wait the real cadence

func seedFollowup(t *testing.T, s *Service, closedAt time.Time) {
	t.Helper()
	st := s.store().(prFollowupStore)
	// The follow-up table references todos; insert a minimal closed row the foreign key accepts.
	rs, ok := s.store().(*repositorystore.Store)
	if !ok {
		t.Fatalf("test store is not *repositorystore.Store: %T", s.store())
	}
	if err := rs.CreateTodo(context.Background(), db.TodoRow{
		ID: fupTodo, Title: "closed work", CreatedAt: closedAt, UpdatedAt: closedAt, DoneAt: &closedAt, DoneBy: db.DoneByReview,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPRFollowups(context.Background(), fupTodo, fupSup, "conv_origin",
		[]string{fupPR}, closedAt); err != nil {
		t.Fatal(err)
	}
}

func followupRows(t *testing.T, s *Service) []repository.PRFollowup {
	t.Helper()
	rows, err := s.store().(prFollowupStore).ListPRFollowups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return rows
}

func keptFollowupNotices(s *Service) []string {
	b := &s.relays
	b.mu.Lock()
	defer b.mu.Unlock()
	var tools []string
	for _, msg := range b.st.Later[fupSup] {
		if msg.Tool == prFollowupTool {
			tools = append(tools, msg.Text)
		}
	}
	return tools
}

func TestGoldenPRFollowupLanguages(t *testing.T) {
	for _, locale := range []string{"zh-Hans", "en", "ja"} {
		for _, event := range []string{"merged", "closed", "conflict", "changes_requested"} {
			text := prompts.RenderPRFollowupNotice(prompts.PRFollowupNotice{
				ID: fupTodo, Title: "Task 原文", PRURL: fupPR, Event: event,
				Language: answerLanguage(locale),
			})
			if !strings.Contains(text, "Task 原文") || !strings.Contains(text, fupPR) {
				t.Fatalf("%s/%s lost the source title or PR URL: %s", locale, event, text)
			}
			golden(t, "pr_followup."+event+"."+locale, text)
		}
	}
}

func TestPRFollowupTerminalNoticeRequiresPostClosureTransition(t *testing.T) {
	for _, tc := range []struct {
		name, terminalField string
		eventOffset         time.Duration
		merged, sawOpen     bool
		want                string
	}{
		{name: "merged before closure", merged: true, terminalField: "merged_at", eventOffset: -time.Minute},
		{name: "first observation is undated and merged", merged: true},
		{name: "first observation is undated and closed"},
		{name: "merged after closure before first poll", merged: true, terminalField: "merged_at", eventOffset: time.Minute, want: "已合并"},
		{name: "closed after closure before first poll", terminalField: "closed_at", eventOffset: time.Minute, want: "未合并"},
		{name: "observed open then merged without a timestamp", sawOpen: true, merged: true, want: "已合并"},
		{name: "observed open then merged in the closure second", sawOpen: true, merged: true, terminalField: "merged_at", want: "已合并"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := t.Context()
			s, f := newQuietServiceOn(t, repositorystore.New(todoTestDB(t)))
			f.mu.Lock()
			f.known[fupSup] = true
			f.mu.Unlock()
			f.setSessions("server", proto.Session{SessionID: fupSup, MachineID: "server", Assistant: true, Status: proto.StatusIdle})
			closedAt := time.Now().UTC().Truncate(time.Second)
			seedFollowup(t, s, closedAt)
			if tc.sawOpen {
				s.deploy.forge = &statusForge{status: TodoPRStatus{State: "open", HeadOID: "h1"}}
				s.sweepPRFollowups(ctx, closedAt.Add(100*time.Millisecond))
			}
			pr := map[string]any{"state": "closed", "merged": tc.merged}
			if tc.terminalField != "" {
				pr[tc.terminalField] = closedAt.Add(tc.eventOffset)
			}
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/repos/o/r/pulls/77" {
					t.Errorf("unexpected forge call: %s", r.URL.Path)
				}
				if err := json.NewEncoder(w).Encode(pr); err != nil {
					t.Error(err)
				}
			}))
			t.Cleanup(srv.Close)
			s.deploy.forge = githubForge{base: srv.URL, client: srv.Client()}
			s.sweepPRFollowups(ctx, closedAt.Add(2*time.Minute))
			s.sweepPRFollowups(ctx, closedAt.Add(3*time.Minute))
			if rows := followupRows(t, s); len(rows) != 0 {
				t.Fatalf("terminal PR left %d follow-up subscriptions", len(rows))
			}
			if sent := f.sentTexts(fupSup); len(sent) != 0 || s.queuedFor(fupSup) != 0 {
				t.Fatalf("post-closure observation scheduled a wake: %v", sent)
			}
			got := keptFollowupNotices(s)
			if tc.want == "" && len(got) != 0 {
				t.Fatalf("preexisting terminal state left a follow-up: %v", got)
			}
			if tc.want != "" && (len(got) != 1 || !strings.Contains(got[0], tc.want)) {
				t.Fatalf("expected one quiet %q notice, got %v", tc.want, got)
			}
		})
	}
}

func TestPRFollowupConflictStaysQuietAndIsRecordedOncePerHead(t *testing.T) {
	s, forge := followupEnv(t, TodoPRStatus{State: "open", HeadOID: "h1"})
	now := time.Now()
	seedFollowup(t, s, now.Add(-time.Minute))
	s.sweepPRFollowups(t.Context(), now.Add(-time.Second))
	forge.status.Conflicts = true
	s.sweepPRFollowups(context.Background(), now)
	rows := followupRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("conflict should keep the subscription, got %d rows", len(rows))
	}
	if rows[0].NotifiedConflictHead != "h1" {
		t.Fatalf("conflict head not recorded: %+v", rows[0])
	}
	if got := keptFollowupNotices(s); len(got) != 1 || !strings.Contains(got[0], "冲突") {
		t.Fatalf("expected one conflict notice, got %v", got)
	}
	if s.queuedFor(fupSup) != 0 {
		t.Fatal("closed-task conflict queued a supervisor wake")
	}
	// The same conflicting head is kept once.
	s.sweepPRFollowups(context.Background(), now.Add(time.Minute))
	if got := keptFollowupNotices(s); len(got) != 1 {
		t.Fatalf("same-head conflict re-notified: %d notices", len(got))
	}
	// A new head with a conflict adds another quiet record.
	s.deploy.forge = &statusForge{status: TodoPRStatus{State: "open", HeadOID: "h2", Conflicts: true, CheckedAt: time.Now()}}
	s.sweepPRFollowups(context.Background(), now.Add(2*time.Minute))
	if got := keptFollowupNotices(s); len(got) != 2 {
		t.Fatalf("new-head conflict did not re-notify: %d notices", len(got))
	}
}

func TestPRFollowupCurrentHeadChangesRequestedNotifies(t *testing.T) {
	now := time.Now()
	s, forge := followupEnv(t, TodoPRStatus{State: "open", HeadOID: "h1"})
	seedFollowup(t, s, now.Add(-time.Minute))
	s.sweepPRFollowups(t.Context(), now.Add(-time.Second))
	forge.status.Review, forge.status.ChangesRequestedAt = "changes_requested", &now
	s.sweepPRFollowups(t.Context(), now)
	if got := keptFollowupNotices(s); len(got) != 1 || !strings.Contains(got[0], "changes-requested") {
		t.Fatalf("expected one changes-requested notice, got %v", got)
	}
	rows := followupRows(t, s)
	if len(rows) != 1 || rows[0].NotifiedReviewHead != "h1" {
		t.Fatalf("review head not recorded: %+v", rows)
	}
}

func TestPRFollowupForgeUnavailableDoesNotPretendToWatch(t *testing.T) {
	s, _ := followupEnv(t, TodoPRStatus{})
	s.deploy.forge = &statusForge{statusErr: errGitHubUnconfigured}
	now := time.Now().UTC().Truncate(time.Second)
	seedFollowup(t, s, now.Add(-time.Minute))
	s.sweepPRFollowups(context.Background(), now)
	rows := followupRows(t, s)
	if len(rows) != 1 || rows[0].LastObservedAt != nil {
		t.Fatalf("unavailable forge must leave the subscription unobserved, got %+v", rows)
	}
	if rows[0].WatchStatus != repository.PRFollowupWatchHostNotConnected ||
		rows[0].WatchReason != repository.PRFollowupReasonHostNotConnected ||
		rows[0].WatchUpdatedAt == nil || !rows[0].WatchUpdatedAt.Equal(now) {
		t.Fatalf("unavailable forge must persist host_not_connected watch state, got %+v", rows[0])
	}
	// Repeated sweeps while disconnected must not fabricate observations or spam notices.
	s.sweepPRFollowups(context.Background(), now.Add(time.Minute))
	rows = followupRows(t, s)
	if len(rows) != 1 || rows[0].LastObservedAt != nil || rows[0].WatchStatus != repository.PRFollowupWatchHostNotConnected {
		t.Fatalf("repeated disconnected sweep corrupted subscription: %+v", rows)
	}
	if got := keptFollowupNotices(s); len(got) != 0 {
		t.Fatalf("unavailable forge sent a notice: %v", got)
	}
}

func TestCreatePRFollowupsOnAcceptedTodo(t *testing.T) {
	s, _ := followupEnv(t, TodoPRStatus{State: "open"})
	rs := s.store().(*repositorystore.Store)
	doneAt := time.Now().UTC().Add(-time.Minute).Truncate(time.Microsecond)
	if err := rs.CreateTodo(context.Background(), db.TodoRow{
		ID: fupTodo, Title: "closed work", CreatedAt: doneAt, UpdatedAt: doneAt, DoneAt: &doneAt, DoneBy: db.DoneByReview,
	}); err != nil {
		t.Fatal(err)
	}
	const mergedPR = "https://github.com/o/r/pull/76"
	todo := Todo{
		ID: fupTodo, Title: "closed work", Origin: "conv_origin", DoneAt: &doneAt,
		PRURL: fupPR, PRs: []string{fupPR, mergedPR}, OtherPRs: []TodoPRStatus{{URL: mergedPR, State: "merged"}},
		Supervision: &Supervision{Supervisor: fupSup},
	}
	s.createPRFollowups(context.Background(), todo)
	rows := followupRows(t, s)
	if len(rows) != 1 {
		t.Fatalf("expected one follow-up for the open delivery PR, got %d", len(rows))
	}
	if rows[0].Supervisor != fupSup || rows[0].PRURL != fupPR || !rows[0].CreatedAt.Equal(doneAt) {
		t.Fatalf("follow-up recorded wrong fields: %+v", rows[0])
	}
	// Re-accepting is idempotent.
	s.createPRFollowups(context.Background(), todo)
	if rows := followupRows(t, s); len(rows) != 1 {
		t.Fatalf("re-acceptance duplicated the follow-up: %d", len(rows))
	}
}

// End to end: accepting a to-do whose delivery PR is still open creates the follow-up subscription through the real
// review/close path (the hook in reviewDoneTodo), without reopening the to-do. Without that hook this fails.
func TestAcceptedTodoCreatesFollowupThroughReviewPath(t *testing.T) {
	st := repositorystore.New(todoTestDB(t))
	s, _, todo := newSupervisedServiceOn(t, st)
	setDeliveryGate(t, s, deliveryGateOff) // a to-do may close while its PR is still open; the follow-up hook is gate-independent
	s.deploy.forge = &statusForge{status: TodoPRStatus{State: "open", HeadOID: "h1"}}
	s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
	s.deploy.listPaths = []string{}
	s.setTaskStage(context.Background(), todo.ID, "review", fupPR)
	if _, err := acceptReview(s, todo.ID, map[string]any{"link": fupPR}); err != nil {
		t.Fatalf("acceptance: %v", err)
	}
	if todoNow(s, todo.ID).DoneAt == nil {
		t.Fatal("the to-do did not close")
	}
	rows, err := st.ListPRFollowups(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].PRURL != fupPR {
		t.Fatalf("accepted to-do created %+v, want one follow-up for %s", rows, fupPR)
	}
}

func TestPRFollowupHostNotConnectedOnCreation(t *testing.T) {
	t.Run("github disconnected under default merged gate records host_not_connected immediately for all github PRs", func(t *testing.T) {
		const secondGHPR = "https://github.com/o/r/pull/78"
		st := repositorystore.New(todoTestDB(t))
		s, _, todo := newSupervisedServiceOn(t, st)
		s.deploy.forge = &statusForge{
			fakeForge: fakeForge{err: errGitHubUnconfigured},
			statusErr: errGitHubUnconfigured,
		}
		s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
		s.deploy.listPaths = []string{}
		s.setTaskStage(context.Background(), todo.ID, "review", secondGHPR)
		s.setTaskStage(context.Background(), todo.ID, "review", fupPR)

		res, err := acceptReview(s, todo.ID, map[string]any{"link": fupPR})
		if err != nil {
			t.Fatalf("acceptance with disconnected GitHub should degrade gracefully: %v", err)
		}
		if !strings.Contains(res, "github.com") {
			t.Fatalf("expected delivery_host_not_connected note in reply, got %q", res)
		}
		rows := followupRows(t, s)
		if len(rows) != 2 {
			t.Fatalf("expected 2 follow-up rows, got %+v", rows)
		}
		for _, r := range rows {
			if r.WatchStatus != repository.PRFollowupWatchHostNotConnected ||
				r.WatchReason != repository.PRFollowupReasonHostNotConnected ||
				r.LastObservedAt != nil || r.WatchUpdatedAt == nil {
				t.Fatalf("created follow-up %s did not persist host_not_connected: %+v", r.PRURL, r)
			}
		}
	})

	t.Run("unsupported host records unsupported_host immediately at creation", func(t *testing.T) {
		const unsupportedMR = "https://git.example.test/team/app/merge_requests/3"
		st := repositorystore.New(todoTestDB(t))
		s, _, todo := newSupervisedServiceOn(t, st)
		s.deploy.forge = &fakeForge{}
		s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
		s.deploy.listPaths = []string{}
		s.setTaskStage(context.Background(), todo.ID, "review", unsupportedMR)

		res, err := acceptReview(s, todo.ID, map[string]any{"link": unsupportedMR})
		if err != nil {
			t.Fatalf("acceptance on unsupported host failed: %v", err)
		}
		if !strings.Contains(res, "git.example.test") {
			t.Fatalf("expected host note in reply, got %q", res)
		}
		rows := followupRows(t, s)
		if len(rows) != 1 {
			t.Fatalf("expected 1 follow-up row, got %+v", rows)
		}
		if rows[0].WatchStatus != repository.PRFollowupWatchHostNotConnected ||
			rows[0].WatchReason != repository.PRFollowupReasonUnsupportedHost ||
			rows[0].LastObservedAt != nil || rows[0].WatchUpdatedAt == nil {
			t.Fatalf("unsupported host follow-up = %+v", rows[0])
		}
	})

	t.Run("unconfigured production githubForge records host_not_connected immediately under delivery_gate=off", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		t.Setenv("GH_TOKEN", "")
		t.Setenv("GITHUB_TOKEN", "")

		st := repositorystore.New(todoTestDB(t))
		s, _, todo := newSupervisedServiceOn(t, st)
		setDeliveryGate(t, s, deliveryGateOff)
		s.deploy.forge = githubForge{base: "https://api.github.com", client: &http.Client{Transport: &noAnonymousGitHub{}}}
		s.deploy.hubBuild = func(context.Context) (string, string, error) { return "dev", "", nil }
		s.deploy.listPaths = []string{}
		s.setTaskStage(context.Background(), todo.ID, "review", fupPR)

		if _, err := acceptReview(s, todo.ID, map[string]any{"link": fupPR}); err != nil {
			t.Fatalf("acceptance under delivery_gate=off failed: %v", err)
		}
		rows := followupRows(t, s)
		if len(rows) != 1 ||
			rows[0].WatchStatus != repository.PRFollowupWatchHostNotConnected ||
			rows[0].WatchReason != repository.PRFollowupReasonHostNotConnected ||
			rows[0].LastObservedAt != nil || rows[0].WatchUpdatedAt == nil {
			t.Fatalf("unconfigured githubForge under delivery_gate=off = %+v", rows)
		}
	})

	t.Run("disabled provider and non-status forge record host_not_connected on createPRFollowups", func(t *testing.T) {
		st := repositorystore.New(todoTestDB(t))
		s, _ := newQuietServiceOn(t, st)
		s.deploy.forge = &fakeForge{} // does not implement prStatusForge
		p := &mergeRequestProvider{acmeProvider: newAcmeProvider(), state: "open"}
		p.set(true, repository.ProviderOff)
		s.repoExt = p

		doneAt := time.Now().UTC().Truncate(time.Second)
		if err := st.CreateTodo(context.Background(), db.TodoRow{
			ID: fupTodo, Title: "closed work", CreatedAt: doneAt, UpdatedAt: doneAt, DoneAt: &doneAt, DoneBy: db.DoneByReview,
		}); err != nil {
			t.Fatal(err)
		}
		s.createPRFollowups(context.Background(), Todo{
			ID: fupTodo, Title: "closed work", Origin: "conv_origin", DoneAt: &doneAt,
			PRURL: fupPR, PRs: []string{fupPR, acmeMR},
			Supervision: &Supervision{Supervisor: fupSup},
		})
		rows := followupRows(t, s)
		if len(rows) != 2 {
			t.Fatalf("expected 2 follow-up rows, got %+v", rows)
		}
		for _, r := range rows {
			if r.WatchStatus != repository.PRFollowupWatchHostNotConnected ||
				r.WatchReason != repository.PRFollowupReasonHostNotConnected ||
				r.LastObservedAt != nil || r.WatchUpdatedAt == nil || !r.WatchUpdatedAt.Equal(doneAt) {
				t.Fatalf("row %s = %+v", r.PRURL, r)
			}
		}
	})
}

func TestPRFollowupDisconnectAfterActiveWatchingAndTransientErrors(t *testing.T) {
	ctx := context.Background()
	s, forge := followupEnv(t, TodoPRStatus{State: "open", HeadOID: "h1"})
	t0 := time.Date(2026, 11, 24, 6, 0, 0, 0, time.UTC)
	seedFollowup(t, s, t0.Add(-time.Minute))

	// Active poll succeeds at t0.
	s.sweepPRFollowups(ctx, t0)
	rows := followupRows(t, s)
	if len(rows) != 1 || rows[0].WatchStatus != repository.PRFollowupWatchActive ||
		rows[0].LastObservedAt == nil || !rows[0].LastObservedAt.Equal(t0) {
		t.Fatalf("after active poll: %+v", rows)
	}

	// Transient forge/network error at t1 must NOT mark the subscription host_not_connected.
	forge.statusErr = errors.New("GitHub network error: i/o timeout")
	s.sweepPRFollowups(ctx, t0.Add(time.Minute))
	rows = followupRows(t, s)
	if len(rows) != 1 || rows[0].WatchStatus != repository.PRFollowupWatchActive ||
		rows[0].WatchReason != "" || rows[0].LastObservedAt == nil || !rows[0].LastObservedAt.Equal(t0) {
		t.Fatalf("transient error must leave active watch state intact: %+v", rows)
	}

	// Later disconnect at t2 persists host_not_connected without overwriting LastObservedAt or prior facts.
	t2 := t0.Add(2 * time.Minute)
	forge.statusErr = errGitHubUnconfigured
	s.sweepPRFollowups(ctx, t2)
	rows = followupRows(t, s)
	if len(rows) != 1 ||
		rows[0].WatchStatus != repository.PRFollowupWatchHostNotConnected ||
		rows[0].WatchReason != repository.PRFollowupReasonHostNotConnected ||
		rows[0].WatchUpdatedAt == nil || !rows[0].WatchUpdatedAt.Equal(t2) ||
		rows[0].LastObservedAt == nil || !rows[0].LastObservedAt.Equal(t0) ||
		rows[0].State != "open" || rows[0].HeadOID != "h1" {
		t.Fatalf("disconnect after active watching = %+v", rows)
	}

	// Second sweep while disconnected keeps LastObservedAt at t0 and emits no notices.
	s.sweepPRFollowups(ctx, t0.Add(3*time.Minute))
	rows = followupRows(t, s)
	if len(rows) != 1 || rows[0].LastObservedAt == nil || !rows[0].LastObservedAt.Equal(t0) {
		t.Fatalf("second disconnected sweep mutated LastObservedAt: %+v", rows)
	}
	if got := keptFollowupNotices(s); len(got) != 0 {
		t.Fatalf("disconnect spammed notices: %v", got)
	}
}

func TestPRFollowupHostNotConnectedSurvivesRestartAndRecovers(t *testing.T) {
	ctx := context.Background()
	db1 := todoTestDB(t)
	s1, _ := newQuietServiceOn(t, repositorystore.New(db1))
	s1.deploy.forge = &statusForge{statusErr: errGitHubUnconfigured}

	t0 := time.Date(2026, 11, 24, 6, 0, 0, 0, time.UTC)
	seedFollowup(t, s1, t0.Add(-time.Minute))
	s1.sweepPRFollowups(ctx, t0)

	rows1 := followupRows(t, s1)
	if len(rows1) != 1 || rows1[0].WatchStatus != repository.PRFollowupWatchHostNotConnected || rows1[0].LastObservedAt != nil {
		t.Fatalf("before restart: %+v", rows1)
	}

	// Simulate manager restart with a fresh Postgres pool and a brand-new Service instance.
	db2, err := db.Open(ctx, os.Getenv("PERSONA_TEST_DATABASE_URL"), db.ManagerSet)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db2.Close)
	s2, _ := newQuietServiceOn(t, repositorystore.New(db2))

	rows2 := followupRows(t, s2)
	if len(rows2) != 1 ||
		rows2[0].WatchStatus != repository.PRFollowupWatchHostNotConnected ||
		rows2[0].WatchReason != repository.PRFollowupReasonHostNotConnected ||
		rows2[0].WatchUpdatedAt == nil || !rows2[0].WatchUpdatedAt.Equal(t0) ||
		rows2[0].LastObservedAt != nil {
		t.Fatalf("after restart, unwatched state did not persist: %+v", rows2)
	}

	// Reconnect GitHub on the restarted manager: first sweep observes open + conflict on h1,
	// clears host_not_connected back to active, notifies once, and keeps the to-do closed.
	t1 := t0.Add(5 * time.Minute)
	forge2 := &statusForge{status: TodoPRStatus{State: "open", HeadOID: "h1", Conflicts: true}}
	s2.deploy.forge = forge2
	s2.sweepPRFollowups(ctx, t1)

	rows2 = followupRows(t, s2)
	if len(rows2) != 1 ||
		rows2[0].WatchStatus != repository.PRFollowupWatchActive ||
		rows2[0].WatchReason != "" ||
		rows2[0].LastObservedAt == nil || !rows2[0].LastObservedAt.Equal(t1) ||
		rows2[0].NotifiedConflictHead != "h1" {
		t.Fatalf("after recovery sweep: %+v", rows2)
	}
	if got := keptFollowupNotices(s2); len(got) != 1 || !strings.Contains(got[0], "冲突") {
		t.Fatalf("recovery conflict notice = %v", got)
	}
	if td, ok := s2.todoNow(fupTodo); !ok || td.DoneAt == nil {
		t.Fatalf("to-do reopened after recovery: ok=%v todo=%+v", ok, td)
	}

	// Current-head changes_requested review on a new head wakes once after recovery.
	t2 := t0.Add(10 * time.Minute)
	forge2.status = TodoPRStatus{State: "open", HeadOID: "h2", Review: "changes_requested", ChangesRequestedAt: &t2}
	s2.sweepPRFollowups(ctx, t2)
	if got := keptFollowupNotices(s2); len(got) != 2 || !strings.Contains(got[1], "changes-requested") {
		t.Fatalf("post-recovery review notice = %v", got)
	}

	// Terminal merged PR wakes once and clears the subscription.
	t3 := t0.Add(15 * time.Minute)
	forge2.status = TodoPRStatus{State: "merged", HeadOID: "h2", terminalAt: t3}
	s2.sweepPRFollowups(ctx, t3)
	if rows := followupRows(t, s2); len(rows) != 0 {
		t.Fatalf("terminal PR after recovery left %d subscriptions", len(rows))
	}
	if got := keptFollowupNotices(s2); len(got) != 3 || !strings.Contains(got[2], "已合并") {
		t.Fatalf("post-recovery terminal notice = %v", got)
	}
}

func TestPRFollowupBriefingSurfacesUnwatchedHonestly(t *testing.T) {
	ctx := context.Background()
	st := repositorystore.New(todoTestDB(t))
	now := time.Date(2026, 11, 24, 8, 0, 0, 0, time.UTC)

	seedTodo := func(id, title string, doneAt time.Time) {
		t.Helper()
		if err := st.CreateTodo(ctx, db.TodoRow{
			ID: id, Title: title, CreatedAt: doneAt, UpdatedAt: doneAt, DoneAt: &doneAt, DoneBy: db.DoneByReview,
		}); err != nil {
			t.Fatal(err)
		}
	}
	seedTodo("todo_gh_unwatched", "GitHub disconnected task", now.Add(-10*time.Minute))
	seedTodo("todo_unsupported", "Unsupported host task", now.Add(-5*time.Minute))
	seedTodo("todo_disconnected_later", "Disconnected after poll", now.Add(-2*time.Hour))
	seedTodo("todo_active_fresh", "Active recent task", now.Add(-30*time.Minute))
	seedTodo("todo_active_stale", "Active silent task", now.Add(-30*time.Hour))
	// Build Service after seeding Postgres so briefing resolves titles from freshly loaded database state.
	s, _ := newQuietServiceOn(t, st)

	const (
		prGHUnwatched = "https://github.com/o/r/pull/201"
		prUnsupported = "https://git.example.test/team/app/merge_requests/202"
		prDisconn     = "https://github.com/o/r/pull/203"
		prFresh       = "https://github.com/o/r/pull/204"
		prStale       = "https://github.com/o/r/pull/205"
	)

	unwatchedAt := now.Add(-10 * time.Minute)
	unsupportedAt := now.Add(-5 * time.Minute)
	if err := st.UpsertPRFollowupRecords(ctx, []repository.PRFollowup{
		{
			TodoID: "todo_gh_unwatched", PRURL: prGHUnwatched, Supervisor: fupSup, CreatedAt: unwatchedAt,
			WatchStatus: repository.PRFollowupWatchHostNotConnected, WatchReason: repository.PRFollowupReasonHostNotConnected,
			WatchUpdatedAt: &unwatchedAt,
		},
		{
			TodoID: "todo_unsupported", PRURL: prUnsupported, Supervisor: fupSup, CreatedAt: unsupportedAt,
			WatchStatus: repository.PRFollowupWatchHostNotConnected, WatchReason: repository.PRFollowupReasonUnsupportedHost,
			WatchUpdatedAt: &unsupportedAt,
		},
		{
			TodoID: "todo_disconnected_later", PRURL: prDisconn, Supervisor: fupSup, CreatedAt: now.Add(-2 * time.Hour),
			WatchStatus: repository.PRFollowupWatchActive,
		},
		{
			TodoID: "todo_active_fresh", PRURL: prFresh, Supervisor: fupSup, CreatedAt: now.Add(-30 * time.Minute),
			WatchStatus: repository.PRFollowupWatchActive,
		},
		{
			TodoID: "todo_active_stale", PRURL: prStale, Supervisor: fupSup, CreatedAt: now.Add(-30 * time.Hour),
			WatchStatus: repository.PRFollowupWatchActive,
		},
	}); err != nil {
		t.Fatal(err)
	}

	rows := followupRows(t, s)
	byTodo := map[string]repository.PRFollowup{}
	for _, r := range rows {
		byTodo[r.TodoID] = r
	}

	// Mark todo_disconnected_later as observed 1h ago, then disconnected 15m ago.
	polledAt := now.Add(-time.Hour)
	disconn := byTodo["todo_disconnected_later"]
	disconn.State, disconn.HeadOID, disconn.LastObservedAt = "open", "h1", &polledAt
	if ok, err := st.SavePRFollowupObservation(ctx, disconn); err != nil || !ok {
		t.Fatalf("save disconn obs: ok=%v err=%v", ok, err)
	}
	if ok, err := st.SavePRFollowupWatchStatus(ctx, disconn.ID, repository.PRFollowupWatchHostNotConnected,
		repository.PRFollowupReasonHostNotConnected, now.Add(-15*time.Minute)); err != nil || !ok {
		t.Fatalf("save disconn watch status: ok=%v err=%v", ok, err)
	}

	// Mark todo_active_fresh as observed 5m ago (must NOT surface in briefing).
	freshAt := now.Add(-5 * time.Minute)
	fresh := byTodo["todo_active_fresh"]
	fresh.State, fresh.HeadOID, fresh.LastObservedAt = "open", "h2", &freshAt
	if ok, err := st.SavePRFollowupObservation(ctx, fresh); err != nil || !ok {
		t.Fatalf("save fresh obs: ok=%v err=%v", ok, err)
	}

	// Mark todo_active_stale as observed 26h ago (stale active, MUST surface as active).
	staleAt := now.Add(-26 * time.Hour)
	stale := byTodo["todo_active_stale"]
	stale.State, stale.HeadOID, stale.LastObservedAt = "open", "h3", &staleAt
	if ok, err := st.SavePRFollowupObservation(ctx, stale); err != nil || !ok {
		t.Fatalf("save stale obs: ok=%v err=%v", ok, err)
	}

	staleRows := s.stalePRFollowups(ctx, now)
	coreByTodo := map[string]repository.PRFollowup{}
	for _, row := range staleRows {
		coreByTodo[row.TodoID] = row
	}
	if _, present := coreByTodo["todo_active_fresh"]; present {
		t.Fatalf("recently polled active subscription should not be stale: %+v", coreByTodo["todo_active_fresh"])
	}
	if len(coreByTodo) != 4 {
		t.Fatalf("core stale PR follow-ups = %d (%+v), want 4", len(coreByTodo), staleRows)
	}
	needsBriefingModule(t)
	body := s.briefBodyOf(&briefFacts{At: now, Lang: string(i18n.En)}, "Morning")
	gotByTodo := map[string]briefBodyPRFollowup{}
	for _, row := range body.PRFollowups {
		gotByTodo[row.TodoID] = row
	}
	if _, present := gotByTodo["todo_active_fresh"]; present {
		t.Fatalf("recently polled active subscription should not appear in briefing: %+v", gotByTodo["todo_active_fresh"])
	}
	if len(gotByTodo) != 4 {
		t.Fatalf("briefing PRFollowups count = %d (%+v), want 4", len(gotByTodo), body.PRFollowups)
	}

	ghRow := gotByTodo["todo_gh_unwatched"]
	if ghRow.Title != "GitHub disconnected task" || ghRow.State != "" ||
		ghRow.WatchStatus != repository.PRFollowupWatchHostNotConnected ||
		ghRow.WatchReason != repository.PRFollowupReasonHostNotConnected ||
		ghRow.LastObservedAt != nil || !strings.Contains(ghRow.Note, "Not watched") || !strings.Contains(ghRow.Note, "github.com") {
		t.Fatalf("unwatched GitHub briefing row = %+v", ghRow)
	}

	unsupRow := gotByTodo["todo_unsupported"]
	if unsupRow.State != "" ||
		unsupRow.WatchStatus != repository.PRFollowupWatchHostNotConnected ||
		unsupRow.WatchReason != repository.PRFollowupReasonUnsupportedHost ||
		unsupRow.LastObservedAt != nil || !strings.Contains(unsupRow.Note, "git.example.test") {
		t.Fatalf("unsupported host briefing row = %+v", unsupRow)
	}

	disconnRow := gotByTodo["todo_disconnected_later"]
	if disconnRow.State != "open" ||
		disconnRow.WatchStatus != repository.PRFollowupWatchHostNotConnected ||
		disconnRow.LastObservedAt == nil || !disconnRow.LastObservedAt.Equal(polledAt) ||
		!strings.Contains(disconnRow.Note, "Not watched") {
		t.Fatalf("later-disconnected briefing row = %+v", disconnRow)
	}

	staleRow := gotByTodo["todo_active_stale"]
	if staleRow.WatchStatus != repository.PRFollowupWatchActive || staleRow.WatchReason != "" || staleRow.Note != "" || staleRow.State != "open" {
		t.Fatalf("stale active briefing row = %+v", staleRow)
	}

	// Verify zh-Hans and ja localizations render distinct unwatched notes too.
	for _, lang := range []i18n.Lang{i18n.ZhHans, i18n.Ja} {
		locBody := s.briefBodyOf(&briefFacts{At: now, Lang: string(lang)}, "Brief")
		found := false
		for _, row := range locBody.PRFollowups {
			if row.TodoID == "todo_gh_unwatched" && row.Note != "" && strings.Contains(row.Note, "github.com") {
				found = true
			}
		}
		if !found {
			t.Fatalf("locale %s missing unwatched briefing note: %+v", lang, locBody.PRFollowups)
		}
	}

	// Once GitHub reconnects and sweeps successfully, the recovered GitHub PRs drop out of the unwatched briefing list.
	s.deploy.forge = &statusForge{status: TodoPRStatus{State: "open", HeadOID: "h9"}}
	s.sweepPRFollowups(ctx, now.Add(time.Minute))
	afterBody := s.briefBodyOf(&briefFacts{At: now.Add(2 * time.Minute), Lang: string(i18n.En)}, "Morning")
	if len(afterBody.PRFollowups) != 1 || afterBody.PRFollowups[0].TodoID != "todo_unsupported" {
		t.Fatalf("after GitHub recovery, only unsupported host should remain unwatched in briefing, got %+v", afterBody.PRFollowups)
	}
}

func TestPRFollowupProviderHostUnsupportedAndRecovery(t *testing.T) {
	ctx := context.Background()
	st := repositorystore.New(todoTestDB(t))
	s, _ := newQuietServiceOn(t, st)
	s.deploy.forge = &fakeForge{}

	p := &mergeRequestProvider{acmeProvider: newAcmeProvider(), state: ""} // empty state => errReviewUnsupported
	s.repoExt = p

	t0 := time.Date(2026, 11, 24, 6, 0, 0, 0, time.UTC)
	if err := st.CreateTodo(ctx, db.TodoRow{
		ID: fupTodo, Title: "provider work", CreatedAt: t0, UpdatedAt: t0, DoneAt: &t0, DoneBy: db.DoneByReview,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPRFollowups(ctx, fupTodo, fupSup, "conv_origin", []string{acmeMR}, t0); err != nil {
		t.Fatal(err)
	}

	// Provider cannot read review state (errReviewUnsupported) => persisted as host_not_connected / unsupported_host.
	s.sweepPRFollowups(ctx, t0.Add(time.Minute))
	rows := followupRows(t, s)
	if len(rows) != 1 ||
		rows[0].WatchStatus != repository.PRFollowupWatchHostNotConnected ||
		rows[0].WatchReason != repository.PRFollowupReasonUnsupportedHost ||
		rows[0].LastObservedAt != nil {
		t.Fatalf("provider errReviewUnsupported row = %+v", rows)
	}

	// Provider switched off => persisted as host_not_connected / host_not_connected.
	p.set(true, repository.ProviderOff)
	s.sweepPRFollowups(ctx, t0.Add(2*time.Minute))
	rows = followupRows(t, s)
	if len(rows) != 1 ||
		rows[0].WatchStatus != repository.PRFollowupWatchHostNotConnected ||
		rows[0].WatchReason != repository.PRFollowupReasonHostNotConnected ||
		rows[0].LastObservedAt != nil {
		t.Fatalf("provider off row = %+v", rows)
	}

	// Provider re-enabled with state=open => recovers back to active watching.
	p.set(true, repository.ProviderAvailable)
	p.mu.Lock()
	p.state = "open"
	p.mu.Unlock()
	t3 := t0.Add(3 * time.Minute)
	s.sweepPRFollowups(ctx, t3)
	rows = followupRows(t, s)
	if len(rows) != 1 ||
		rows[0].WatchStatus != repository.PRFollowupWatchActive ||
		rows[0].WatchReason != "" ||
		rows[0].State != "open" ||
		rows[0].LastObservedAt == nil || !rows[0].LastObservedAt.Equal(t3) {
		t.Fatalf("provider recovered row = %+v", rows)
	}

	// Provider reports merged => notifies supervisor once and deletes subscription.
	p.mu.Lock()
	p.state = "merged"
	p.mu.Unlock()
	s.sweepPRFollowups(ctx, t0.Add(4*time.Minute))
	if rows := followupRows(t, s); len(rows) != 0 {
		t.Fatalf("merged provider MR left %d subscriptions", len(rows))
	}
	if got := keptFollowupNotices(s); len(got) != 1 || !strings.Contains(got[0], "已合并") {
		t.Fatalf("provider terminal notice = %v", got)
	}
}

func TestPRFollowupUnwatchedRespectsRecheckCadenceThenRecovers(t *testing.T) {
	orig := followupRecheck
	followupRecheck = 10 * time.Minute
	t.Cleanup(func() { followupRecheck = orig })

	ctx := context.Background()
	s, forge := followupEnv(t, TodoPRStatus{})
	forge.statusErr = errGitHubUnconfigured
	t0 := time.Date(2026, 11, 24, 6, 0, 0, 0, time.UTC)
	seedFollowup(t, s, t0.Add(-time.Minute))

	// First sweep polls immediately (no LastObservedAt and no WatchUpdatedAt yet) and records host_not_connected.
	s.sweepPRFollowups(ctx, t0)
	if forge.statusCalls != 1 {
		t.Fatalf("first sweep calls = %d, want 1", forge.statusCalls)
	}
	// Within the 10-minute cadence, sweep does not hammer the disconnected forge.
	s.sweepPRFollowups(ctx, t0.Add(3*time.Minute))
	if forge.statusCalls != 1 {
		t.Fatalf("sweep inside recheck window should skip, calls = %d", forge.statusCalls)
	}
	// Once GitHub connects and the cadence elapses, sweep polls and recovers to active.
	forge.statusErr = nil
	forge.status = TodoPRStatus{State: "open", HeadOID: "h1"}
	tRecover := t0.Add(10 * time.Minute)
	s.sweepPRFollowups(ctx, tRecover)
	if forge.statusCalls != 2 {
		t.Fatalf("recovery sweep calls = %d, want 2", forge.statusCalls)
	}
	rows := followupRows(t, s)
	if len(rows) != 1 || rows[0].WatchStatus != repository.PRFollowupWatchActive ||
		rows[0].LastObservedAt == nil || !rows[0].LastObservedAt.Equal(tRecover) {
		t.Fatalf("recovered row = %+v", rows)
	}
}

func TestPRFollowupGitLabAuthErrorRecordsHostNotConnectedAndRecovers(t *testing.T) {
	const glMR = "https://gitlab.com/team/app/-/merge_requests/11"
	ctx := context.Background()
	st := repositorystore.New(todoTestDB(t))
	s, _ := newQuietServiceOn(t, st)
	s.deploy.forge = &fakeForge{}

	var glState string
	var glErr error = &repository.CheckError{Code: "authentication"}
	s.gitlabReview = func(context.Context, repository.Identity, repository.TaskLink, string) (repository.ReviewStatus, error) {
		if glErr != nil {
			return repository.ReviewStatus{}, glErr
		}
		return repository.ReviewStatus{State: glState}, nil
	}

	t0 := time.Date(2026, 11, 24, 6, 0, 0, 0, time.UTC)
	if err := st.CreateTodo(ctx, db.TodoRow{
		ID: fupTodo, Title: "gitlab work", CreatedAt: t0, UpdatedAt: t0, DoneAt: &t0, DoneBy: db.DoneByReview,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPRFollowups(ctx, fupTodo, fupSup, "conv_origin", []string{glMR}, t0); err != nil {
		t.Fatal(err)
	}

	s.sweepPRFollowups(ctx, t0.Add(time.Minute))
	rows := followupRows(t, s)
	if len(rows) != 1 ||
		rows[0].WatchStatus != repository.PRFollowupWatchHostNotConnected ||
		rows[0].WatchReason != repository.PRFollowupReasonHostNotConnected ||
		rows[0].LastObservedAt != nil {
		t.Fatalf("gitlab auth error row = %+v", rows)
	}

	// Once GitLab auth succeeds, subscription recovers to active watching.
	glErr = nil
	glState = repository.ReviewOpen
	tRecover := t0.Add(2 * time.Minute)
	s.sweepPRFollowups(ctx, tRecover)
	rows = followupRows(t, s)
	if len(rows) != 1 ||
		rows[0].WatchStatus != repository.PRFollowupWatchActive ||
		rows[0].WatchReason != "" ||
		rows[0].State != repository.ReviewOpen ||
		rows[0].LastObservedAt == nil || !rows[0].LastObservedAt.Equal(tRecover) {
		t.Fatalf("gitlab recovered row = %+v", rows)
	}
}
