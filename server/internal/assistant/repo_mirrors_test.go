package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
	"github.com/nwverse/persona/server/internal/workspacenaming"
)

type repoMirrorTestHub struct {
	*fakeHub
	onCall func(ctx context.Context, machine, method string, params json.RawMessage) (any, error)
}

func (h *repoMirrorTestHub) DeviceCall(ctx context.Context, call DeviceCall) (json.RawMessage, error) {
	if h.onCall != nil && call.Device != DeviceAll && call.Method != DeviceDevicesList {
		res, err := h.onCall(ctx, call.Device, call.Method, call.Params)
		if err != nil {
			return nil, err
		}
		return json.Marshal(res)
	}
	return h.fakeHub.DeviceCall(ctx, call)
}

// memRepoMirrorStore adds in-memory repoMirrorStore persistence on top of containerWorkspaceMemStore.
type memRepoMirrorStore struct {
	*containerWorkspaceMemStore
	mmu           sync.Mutex
	mirrors       map[string]repository.RepoMirrorRecord
	enrollmentErr error
}

func newMemRepoMirrorStore() *memRepoMirrorStore {
	return &memRepoMirrorStore{
		containerWorkspaceMemStore: newContainerWorkspaceMemStore(),
		mirrors:                    map[string]repository.RepoMirrorRecord{},
	}
}

func mirrorKey(machineID, repoID string) string { return machineID + "\x00" + repoID }

func (m *memRepoMirrorStore) RepoMirror(_ context.Context, machineID, repoID string) (*repository.RepoMirrorRecord, error) {
	m.mmu.Lock()
	defer m.mmu.Unlock()
	rec, ok := m.mirrors[mirrorKey(machineID, repoID)]
	if !ok {
		return nil, nil
	}
	cp := rec
	return &cp, nil
}

func (m *memRepoMirrorStore) ListRepoMirrors(_ context.Context, machineID, repoID string) ([]repository.RepoMirrorRecord, error) {
	m.mmu.Lock()
	defer m.mmu.Unlock()
	out := []repository.RepoMirrorRecord{}
	for _, rec := range m.mirrors {
		if machineID != "" && rec.MachineID != machineID {
			continue
		}
		if repoID != "" && rec.RepoID != repoID {
			continue
		}
		out = append(out, rec)
	}
	slices.SortFunc(out, func(a, b repository.RepoMirrorRecord) int {
		if a.MachineID != b.MachineID {
			return strings.Compare(a.MachineID, b.MachineID)
		}
		return strings.Compare(a.RepoID, b.RepoID)
	})
	return out, nil
}

func (m *memRepoMirrorStore) CreateOnDemandRepoMirror(_ context.Context, machineID, repoID string, now time.Time) (*repository.RepoMirrorRecord, error) {
	m.mmu.Lock()
	defer m.mmu.Unlock()
	if m.enrollmentErr != nil {
		return nil, m.enrollmentErr
	}
	key := mirrorKey(machineID, repoID)
	rec, ok := m.mirrors[key]
	if !ok {
		rec = repository.RepoMirrorRecord{
			MachineID:           machineID,
			RepoID:              repoID,
			Enabled:             true,
			SyncIntervalMinutes: repository.DefaultMirrorSyncIntervalMinutes,
			UpdatedAt:           now,
		}
		m.mirrors[key] = rec
	}
	return &rec, nil
}

func (m *memRepoMirrorStore) ListDueAutoSyncRepoMirrors(_ context.Context, now time.Time) ([]repository.RepoMirrorRecord, error) {
	m.mmu.Lock()
	defer m.mmu.Unlock()
	out := []repository.RepoMirrorRecord{}
	for _, rec := range m.mirrors {
		if !rec.Enabled || !rec.AutoSync {
			continue
		}
		if rec.NextSyncAt == nil || !rec.NextSyncAt.After(now) {
			out = append(out, rec)
		}
	}
	return out, nil
}

func (m *memRepoMirrorStore) UpsertRepoMirrorConfig(_ context.Context, cfg repository.RepoMirrorRecord) (repository.RepoMirrorRecord, error) {
	m.mmu.Lock()
	defer m.mmu.Unlock()
	key := mirrorKey(cfg.MachineID, cfg.RepoID)
	existing, ok := m.mirrors[key]
	if ok {
		existing.Enabled = cfg.Enabled
		existing.AutoSync = cfg.AutoSync
		existing.SyncIntervalMinutes = cfg.SyncIntervalMinutes
		existing.NextSyncAt = cfg.NextSyncAt
		if !cfg.Enabled || !cfg.AutoSync {
			existing.ConsecutiveFailures = 0
		}
		existing.UpdatedAt = cfg.UpdatedAt
		m.mirrors[key] = existing
		return existing, nil
	}
	m.mirrors[key] = cfg
	return cfg, nil
}

func (m *memRepoMirrorStore) SaveRepoMirrorSyncResult(_ context.Context, machineID, repoID string, res repository.RepoMirrorSyncUpdate) error {
	m.mmu.Lock()
	defer m.mmu.Unlock()
	key := mirrorKey(machineID, repoID)
	rec, ok := m.mirrors[key]
	if !ok {
		return nil
	}
	if res.LastSyncedAt != nil {
		t := res.LastSyncedAt.UTC()
		rec.LastSyncedAt = &t
	}
	if res.LastHeadCommit != "" {
		rec.LastHeadCommit = res.LastHeadCommit
	}
	if res.LastDefaultRef != "" {
		rec.LastDefaultRef = res.LastDefaultRef
	}
	rec.LastError = res.LastError
	if res.HasDiskBytes {
		rec.DiskBytes = res.DiskBytes
	}
	rec.ConsecutiveFailures = res.ConsecutiveFailures
	rec.NextSyncAt = res.NextSyncAt
	rec.UpdatedAt = res.UpdatedAt
	m.mirrors[key] = rec
	return nil
}

func (m *memRepoMirrorStore) DeleteRepoMirror(_ context.Context, machineID, repoID string) error {
	m.mmu.Lock()
	defer m.mmu.Unlock()
	delete(m.mirrors, mirrorKey(machineID, repoID))
	return nil
}

func seedMirrorRepo(t *testing.T, st *memRepoMirrorStore, repoID, repoURL string) {
	t.Helper()
	id, err := repository.Parse(repoURL, "")
	if err != nil {
		t.Fatal(err)
	}
	doc := repositoryDocument{
		Revision: 1,
		Repositories: []RepositoryBinding{{
			Identity: id,
			ID:       repoID,
			Name:     repoID,
			CloneURL: repoURL,
		}},
	}
	if err := st.PutSetting(t.Context(), repositoriesKey, doc); err != nil {
		t.Fatal(err)
	}
}

func TestRepoMirrorHTTPRoutesValidationAndLifecycle(t *testing.T) {
	st := newMemRepoMirrorStore()
	seedMirrorRepo(t, st, "repo_web", "https://github.com/example/web")

	baseHub := newFakeHub()
	baseHub.devices = []Device{{ID: "dev1", Name: "Dev 1", Online: true}}
	var ensureCalls, removeCalls int
	now := time.Date(2026, 11, 22, 10, 0, 0, 0, time.UTC)
	hub := &repoMirrorTestHub{
		fakeHub: baseHub,
		onCall: func(_ context.Context, machine, method string, _ json.RawMessage) (any, error) {
			switch method {
			case repository.MethodRepoMirrorEnsure:
				ensureCalls++
				t0 := now
				return repository.MirrorInfo{
					RepoID:       "repo_web",
					Path:         "/home/example/.persona/repos/repo_web.git",
					Exists:       true,
					CloneURL:     "https://github.com/example/web",
					DefaultRef:   "refs/heads/main",
					HeadCommit:   strings.Repeat("a", 40),
					LastSyncedAt: &t0,
					DiskBytes:    4096,
					Fetched:      true,
				}, nil
			case repository.MethodRepoMirrorRemove:
				removeCalls++
				return repository.MirrorRemoveResponse{RepoID: "repo_web", Removed: true, FreedBytes: 4096}, nil
			default:
				return nil, errors.New("unexpected method " + method)
			}
		},
	}

	s := New(Options{Hub: hub, Store: func() Store { return st }})
	s.repoMirrors.now = func() time.Time { return now }
	s.repoMirrors.jitter = func(time.Duration) time.Duration { return 0 }

	// 1. Default unconfigured GET returns enabled=false, auto_sync=false.
	w := placeDo(s, "", http.MethodGet, "/api/v1/assistant/repositories/repo_web/mirrors/dev1", "")
	if w.Code != 200 {
		t.Fatalf("GET default: %d %s", w.Code, w.Body)
	}
	var got struct{ Mirror repository.RepoMirrorRecord }
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.Mirror.Enabled || got.Mirror.AutoSync {
		t.Fatalf("expected default disabled mirror: %+v (%v)", got, err)
	}

	// 2. Reject auto_sync=true when enabled=false, out-of-range interval, and read-only role.
	if w := placeDo(s, "", http.MethodPut, "/api/v1/assistant/repositories/repo_web/mirrors/dev1", `{"enabled":false,"auto_sync":true}`); w.Code != 400 {
		t.Fatalf("expected 400 for auto_sync without enabled, got %d %s", w.Code, w.Body)
	}
	if w := placeDo(s, "", http.MethodPut, "/api/v1/assistant/repositories/repo_web/mirrors/dev1", `{"enabled":true,"auto_sync":true,"sync_interval_minutes":5}`); w.Code != 400 {
		t.Fatalf("expected 400 for interval < 15, got %d %s", w.Code, w.Body)
	}
	if w := placeDo(s, "read", http.MethodPut, "/api/v1/assistant/repositories/repo_web/mirrors/dev1", `{"enabled":true,"auto_sync":false}`); w.Code != 403 {
		t.Fatalf("expected 403 for read-only role, got %d %s", w.Code, w.Body)
	}

	// 3. Enable mirror (auto_sync=false), reject malformed sync payloads, and run explicit sync.
	w = placeDo(s, "", http.MethodPut, "/api/v1/assistant/repositories/repo_web/mirrors/dev1", `{"enabled":true,"auto_sync":false,"sync_interval_minutes":30}`)
	if w.Code != 200 {
		t.Fatalf("PUT enable: %d %s", w.Code, w.Body)
	}
	for _, badBody := range []string{
		`{"force":true} garbage`,
		`{"force":true}{}`,
		`{"force":true,"extra":1}`,
		`not-json`,
	} {
		if w := placeDo(s, "", http.MethodPost, "/api/v1/assistant/repositories/repo_web/mirrors/dev1/sync", badBody); w.Code != 400 || ensureCalls != 0 {
			t.Fatalf("POST sync bad body %q: code=%d calls=%d body=%s", badBody, w.Code, ensureCalls, w.Body)
		}
	}
	// Empty body is valid and triggers a normal TTL sync.
	if w := placeDo(s, "", http.MethodPost, "/api/v1/assistant/repositories/repo_web/mirrors/dev1/sync", ""); w.Code != 200 || ensureCalls != 1 {
		t.Fatalf("POST sync empty body: %d calls=%d %s", w.Code, ensureCalls, w.Body)
	}
	w = placeDo(s, "", http.MethodPost, "/api/v1/assistant/repositories/repo_web/mirrors/dev1/sync", `{"force":true}`)
	if w.Code != 200 || ensureCalls != 2 {
		t.Fatalf("POST sync: %d calls=%d %s", w.Code, ensureCalls, w.Body)
	}
	var synced struct {
		Mirror repository.RepoMirrorRecord
		Live   repository.MirrorInfo
	}
	if err := json.Unmarshal(w.Body.Bytes(), &synced); err != nil || synced.Mirror.LastHeadCommit != strings.Repeat("a", 40) || synced.Mirror.DiskBytes != 4096 {
		t.Fatalf("unexpected sync payload: %+v (%v)", synced, err)
	}

	// 4. List mirrors returns the configured row.
	w = placeDo(s, "", http.MethodGet, "/api/v1/assistant/repositories/mirrors?machine_id=dev1", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), "repo_web") {
		t.Fatalf("GET list: %d %s", w.Code, w.Body)
	}

	// 5. Delete removes on Node and clears Manager persistence.
	w = placeDo(s, "", http.MethodDelete, "/api/v1/assistant/repositories/repo_web/mirrors/dev1", "")
	if w.Code != 200 || removeCalls != 1 {
		t.Fatalf("DELETE: %d removeCalls=%d %s", w.Code, removeCalls, w.Body)
	}
	rec, _ := st.RepoMirror(t.Context(), "dev1", "repo_web")
	if rec != nil {
		t.Fatalf("expected persisted mirror deleted, got %+v", rec)
	}
}

func TestRepoMirrorPeriodicSyncZeroTrafficWhenDisabledAndBackoffOnFailure(t *testing.T) {
	st := newMemRepoMirrorStore()
	seedMirrorRepo(t, st, "repo_web", "https://github.com/example/web")

	baseHub := newFakeHub()
	baseHub.devices = []Device{{ID: "dev1", Name: "Dev 1", Online: true}}
	var ensureCalls int
	var failNext bool
	now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

	hub := &repoMirrorTestHub{
		fakeHub: baseHub,
		onCall: func(_ context.Context, machine, method string, _ json.RawMessage) (any, error) {
			if method != repository.MethodRepoMirrorEnsure {
				return nil, errors.New("unexpected method")
			}
			ensureCalls++
			if failNext {
				return nil, errors.New("network timeout")
			}
			t0 := now
			return repository.MirrorInfo{
				RepoID:       "repo_web",
				Exists:       true,
				HeadCommit:   strings.Repeat("b", 40),
				DefaultRef:   "refs/heads/main",
				LastSyncedAt: &t0,
				DiskBytes:    1024,
				Fetched:      true,
			}, nil
		},
	}

	s := New(Options{Hub: hub, Store: func() Store { return st }})
	s.repoMirrors.now = func() time.Time { return now }
	s.repoMirrors.jitter = func(base time.Duration) time.Duration { return base / 10 } // deterministic +10% jitter

	// 1. Enabled mirror with auto_sync=false generates zero background RPCs.
	if _, err := st.UpsertRepoMirrorConfig(t.Context(), repository.RepoMirrorRecord{
		MachineID:           "dev1",
		RepoID:              "repo_web",
		Enabled:             true,
		AutoSync:            false,
		SyncIntervalMinutes: 30,
		UpdatedAt:           now,
	}); err != nil {
		t.Fatal(err)
	}
	if next := s.runRepoMirrorSyncPass(t.Context()); !next.IsZero() || ensureCalls != 0 {
		t.Fatalf("expected zero traffic when auto_sync=false: next=%v calls=%d", next, ensureCalls)
	}

	// 2. Enable auto_sync -> due immediately; fail twice to verify exponential backoff.
	dueNow := now
	if _, err := st.UpsertRepoMirrorConfig(t.Context(), repository.RepoMirrorRecord{
		MachineID:           "dev1",
		RepoID:              "repo_web",
		Enabled:             true,
		AutoSync:            true,
		SyncIntervalMinutes: 30,
		NextSyncAt:          &dueNow,
		UpdatedAt:           now,
	}); err != nil {
		t.Fatal(err)
	}

	failNext = true
	next1 := s.runRepoMirrorSyncPass(t.Context())
	// 1st failure: base 30m * 2^0 + 10% jitter = 33m.
	if ensureCalls != 1 || next1.Sub(now) != 33*time.Minute {
		t.Fatalf("1st failure backoff: calls=%d delay=%v", ensureCalls, next1.Sub(now))
	}
	rec, _ := st.RepoMirror(t.Context(), "dev1", "repo_web")
	if rec.ConsecutiveFailures != 1 || rec.LastError == "" {
		t.Fatalf("expected failure recorded: %+v", rec)
	}

	// Advance clock to next1 -> 2nd failure: base 30m * 2^1 + 10% jitter = 66m.
	now = next1
	next2 := s.runRepoMirrorSyncPass(t.Context())
	if ensureCalls != 2 || next2.Sub(now) != 66*time.Minute {
		t.Fatalf("2nd failure backoff: calls=%d delay=%v", ensureCalls, next2.Sub(now))
	}

	// Advance clock to next2 -> recovery resets consecutive_failures to 0 and schedules base+jitter (33m).
	now = next2
	failNext = false
	next3 := s.runRepoMirrorSyncPass(t.Context())
	if ensureCalls != 3 || next3.Sub(now) != 33*time.Minute {
		t.Fatalf("recovery schedule: calls=%d delay=%v", ensureCalls, next3.Sub(now))
	}
	rec, _ = st.RepoMirror(t.Context(), "dev1", "repo_web")
	if rec.ConsecutiveFailures != 0 || rec.LastError != "" || rec.LastHeadCommit != strings.Repeat("b", 40) {
		t.Fatalf("expected clean recovery state: %+v", rec)
	}
}

func TestPrepareBackendWorkspaceUsesEnabledMirrorAndFallsBackCleanly(t *testing.T) {
	setup := func(t *testing.T, enabled *bool, ensureErr, enrollmentErr error) (ensureCalls int, prepareMirrorRepos []string, config *repository.RepoMirrorRecord) {
		t.Helper()
		st := newMemRepoMirrorStore()
		st.enrollmentErr = enrollmentErr
		seedMirrorRepo(t, st, "repo_web", "https://github.com/example/web")
		id, _ := repository.Parse("https://github.com/example/web", "")
		now := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)

		if enabled != nil {
			if _, err := st.UpsertRepoMirrorConfig(t.Context(), repository.RepoMirrorRecord{
				MachineID:           "dev1",
				RepoID:              "repo_web",
				Enabled:             *enabled,
				AutoSync:            false,
				SyncIntervalMinutes: 30,
				UpdatedAt:           now,
			}); err != nil {
				t.Fatal(err)
			}
		}

		// Save todo, repository binding, and workspace naming plan.
		st.todos["todo_1"] = db.TodoRow{ID: "todo_1", Title: "Mirror test", CreatedAt: now, UpdatedAt: now}
		st.memRepoStore.sets["todo_1"] = repository.TaskSet{
			SchemaVersion: 1,
			TodoID:        "todo_1",
			Revision:      1,
			Repositories: []repository.TaskRepository{{
				RepoID:       "repo_web",
				RequestedRef: repository.RequestedRef{Kind: "default"},
				SourceSnapshot: repository.SourceSnapshot{
					Identity:        id,
					CloneURL:        "https://github.com/example/web",
					TransportOrigin: "canonical_default",
					CatalogRevision: 1,
				},
			}},
		}
		snap, err := repository.Snapshot("assistant", "todo_1", 1, st.memRepoStore.sets["todo_1"].Repositories, []string{"repo_web"})
		if err != nil {
			t.Fatal(err)
		}
		src := workspacenaming.Source{
			Manager:      "assistant",
			TodoID:       "todo_1",
			Title:        "Mirror test",
			MachineID:    "dev1",
			PlannedAt:    now,
			Timezone:     "UTC",
			Rules:        workspacenaming.Defaults(),
			Layout:       repository.LayoutRoot,
			Repositories: snap,
		}
		preview := workspacenaming.Resolve(src, workspacenaming.NodeContext{Home: "/home/example"})
		if preview.Plan == nil {
			t.Fatal(preview)
		}
		if _, err := st.SaveWorkspaceNamingPlan(t.Context(), "todo_1", "op_mirror_1", 0, *preview.Plan); err != nil {
			t.Fatal(err)
		}

		baseHub := newFakeHub()
		baseHub.devices = []Device{{ID: "dev1", Name: "Dev 1", Online: true}}
		hub := &repoMirrorTestHub{
			fakeHub: baseHub,
			onCall: func(ctx context.Context, _ string, method string, raw json.RawMessage) (any, error) {
				switch method {
				case "task.workspace.capabilities":
					return map[string]any{"capability": taskworkspace.Capability, "start": true}, nil
				case repository.MethodRepoMirrorEnsure:
					ensureCalls++
					deadline, ok := ctx.Deadline()
					remaining := time.Until(deadline)
					if !ok || remaining <= repository.MirrorColdFetchTimeout || remaining > repoMirrorCallTimeout {
						t.Errorf("mirror RPC has %v remaining, want the cold fetch budget plus time for the reply", remaining)
					}
					var req repository.MirrorEnsureRequest
					if err := json.Unmarshal(raw, &req); err != nil {
						return nil, err
					}
					if req.TTLSeconds != repository.DefaultMirrorFetchTTLSeconds || req.Force {
						t.Errorf("expected on-demand ensure with default TTL: %+v", req)
					}
					if ensureErr != nil {
						return nil, ensureErr
					}
					t0 := now
					return repository.MirrorInfo{
						RepoID:       "repo_web",
						Exists:       true,
						HeadCommit:   strings.Repeat("c", 40),
						DefaultRef:   "refs/heads/main",
						LastSyncedAt: &t0,
						DiskBytes:    2048,
						Fetched:      true,
					}, nil
				case "task.workspace.prepare":
					var req taskworkspace.Request
					if err := json.Unmarshal(raw, &req); err != nil {
						return nil, err
					}
					prepareMirrorRepos = slices.Clone(req.MirrorRepos)
					return taskworkspace.Record{
						SchemaVersion:   1,
						WorkspaceID:     req.WorkspaceID,
						OperationID:     req.OperationID,
						NodeOperationID: req.OperationID,
						Plan:            req.Plan,
						InputDigest:     taskworkspace.InputKey(req.Plan),
						State:           "ready",
						Repositories: []taskworkspace.Instance{{
							RepoID:         "repo_web",
							Input:          req.Plan.Source.Repositories.Repositories[0],
							RelativePath:   req.Plan.Repositories[0].RelativePath,
							Path:           req.Plan.Repositories[0].AbsolutePath,
							CommonDir:      "/home/example/.persona/task-workspaces/x/git/repo_web.git",
							TaskBranch:     req.Plan.Repositories[0].TaskBranch,
							ResolvedRef:    "refs/heads/main",
							ResolvedCommit: strings.Repeat("c", 40),
							State:          "ready",
						}},
						Uses: []string{},
					}, nil
				default:
					return nil, errors.New("unexpected method " + method)
				}
			},
		}

		s := New(Options{Hub: hub, Store: func() Store { return st }})
		s.repoMirrors.now = func() time.Time { return now }
		rec, err := s.PrepareBackendWorkspace(t.Context(), "todo_1", "op_mirror_1")
		if err != nil || rec == nil || rec.State != "ready" {
			t.Fatalf("PrepareBackendWorkspace did not return a ready workspace: %+v, %v", rec, err)
		}
		config, err = st.RepoMirror(t.Context(), "dev1", "repo_web")
		if err != nil {
			t.Fatal(err)
		}
		return ensureCalls, prepareMirrorRepos, config
	}

	t.Run("missing mirror config enrolls on demand", func(t *testing.T) {
		calls, mirrorRepos, config := setup(t, nil, nil, nil)
		if calls != 1 || !slices.Equal(mirrorRepos, []string{"repo_web"}) {
			t.Fatalf("expected one mirror ensure and a mirror hint: calls=%d repos=%v", calls, mirrorRepos)
		}
		if config == nil || !config.Enabled || config.AutoSync || config.NextSyncAt != nil ||
			config.SyncIntervalMinutes != repository.DefaultMirrorSyncIntervalMinutes || config.LastSyncedAt == nil {
			t.Fatalf("expected a synced on-demand mirror with periodic sync off: %+v", config)
		}
	})

	t.Run("explicit opt-out skips mirror ensure and use", func(t *testing.T) {
		calls, mirrorRepos, config := setup(t, new(false), nil, nil)
		if calls != 0 || len(mirrorRepos) != 0 || config == nil || config.Enabled {
			t.Fatalf("expected disabled config and no mirror work: calls=%d repos=%v config=%+v", calls, mirrorRepos, config)
		}
	})

	t.Run("mirror enabled refreshes and passes MirrorRepos", func(t *testing.T) {
		calls, mirrorRepos, _ := setup(t, new(true), nil, nil)
		if calls != 1 || !slices.Equal(mirrorRepos, []string{"repo_web"}) {
			t.Fatalf("expected one mirror ensure and a mirror hint: calls=%d repos=%v", calls, mirrorRepos)
		}
	})

	t.Run("mirror ensure failure falls back cleanly", func(t *testing.T) {
		calls, mirrorRepos, _ := setup(t, nil, errors.New("transient error"), nil)
		if calls != 1 || len(mirrorRepos) != 0 {
			t.Fatalf("expected clean fallback on ensure error: calls=%d repos=%v", calls, mirrorRepos)
		}
	})

	t.Run("older node without repo.mirror.ensure falls back cleanly", func(t *testing.T) {
		calls, mirrorRepos, _ := setup(t, nil, errors.New("unknown method repo.mirror.ensure"), nil)
		if calls != 1 || len(mirrorRepos) != 0 {
			t.Fatalf("expected clean fallback on older node: calls=%d repos=%v", calls, mirrorRepos)
		}
	})

	t.Run("mirror store failure falls back cleanly", func(t *testing.T) {
		calls, mirrorRepos, config := setup(t, nil, nil, errors.New("mirror store unavailable"))
		if calls != 0 || len(mirrorRepos) != 0 || config != nil {
			t.Fatalf("expected direct prepare when enrollment fails: calls=%d repos=%v config=%+v", calls, mirrorRepos, config)
		}
	})
}

func TestPrepareWorkspaceDoesNotEnrollLocalOrLinkOnlyRepositories(t *testing.T) {
	st := newMemRepoMirrorStore()
	hub := &repoMirrorTestHub{
		fakeHub: newFakeHub(),
		onCall: func(_ context.Context, _ string, method string, _ json.RawMessage) (any, error) {
			t.Fatalf("unexpected mirror RPC %s", method)
			return nil, nil
		},
	}
	s := New(Options{Hub: hub, Store: func() Store { return st }})
	r := &taskworkspace.Record{Plan: workspacenaming.Plan{Source: workspacenaming.Source{
		MachineID: "dev1",
		Repositories: repository.PreparationSnapshot{Repositories: []repository.PreparationRepository{
			{RepoID: "local", Local: &repositorywire.LocalCheckout{}},
			{RepoID: "linked_example"},
			{RepoID: "invalid/id", SourceSnapshot: repository.SourceSnapshot{CatalogRevision: 1}},
		}},
	}}}
	if ready := s.prepareEnabledMirrorsForWorkspace(t.Context(), r); len(ready) != 0 {
		t.Fatalf("unexpected mirror hints: %v", ready)
	}
	if mirrors, err := st.ListRepoMirrors(t.Context(), "dev1", ""); err != nil || len(mirrors) != 0 {
		t.Fatalf("unexpected saved mirrors: %+v, %v", mirrors, err)
	}
}

func TestTaskPlanContainerUsesOnDemandRepoMirror(t *testing.T) {
	h := newWorkspaceTestHarness(t, newMemRepoMirrorStore())
	h.f.nodes[repository.MethodRepoMirrorEnsure] = func(string, json.RawMessage) (any, error) {
		return repository.MirrorInfo{RepoID: "repo_web", Exists: true, Fetched: true}, nil
	}

	ctx := t.Context()
	args := `{"tasks":[{"title":"Prepare workspace","prompt":"Update the example repository","role":"worker","machine":"box","permission":"bypassPermissions","done_when":["Tests pass"],"workspace":{"container":{"selected_repo_ids":["repo_web"]}}}]}`
	if _, err := h.s.toolTaskPlan(ctx, "mirror-chat", json.RawMessage(args)); err != nil {
		t.Fatal(err)
	}
	p := readonlyFeedPlan(t, h.s, "mirror-chat")
	if _, err := h.s.startPlan(ctx, p.ID, nil, false); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "workspace agent link", func() bool {
		todo, ok := h.s.todoNow(p.Tasks[0].ReservedTodoID)
		return ok && len(todo.Agents) == 1
	})
	h.mu.Lock()
	defer h.mu.Unlock()
	if !slices.Equal(h.lastPrepare.MirrorRepos, []string{"repo_web"}) {
		t.Fatalf("workspace did not use the enrolled mirror: %v", h.lastPrepare.MirrorRepos)
	}
}

func TestTaskPlanColdMirrorTimeoutFallsBackAndLaunches(t *testing.T) {
	testTaskColdMirrorTimeoutFallsBackAndLaunches(t, 2, func(t *testing.T, h *containerTestHarness) string {
		ctx := t.Context()
		args := `{"tasks":[{"title":"Prepare workspace","prompt":"Update the example repositories","role":"worker","machine":"box","permission":"bypassPermissions","done_when":["Tests pass"],"workspace":{"container":{"selected_repo_ids":["repo_web","repo_api"]}}}]}`
		if _, err := h.s.toolTaskPlan(ctx, "mirror-chat", json.RawMessage(args)); err != nil {
			t.Fatal(err)
		}
		p := readonlyFeedPlan(t, h.s, "mirror-chat")
		if _, err := h.s.startPlan(ctx, p.ID, nil, false); err != nil {
			t.Fatal(err)
		}
		return p.Tasks[0].ReservedTodoID
	})
}

func TestSavedBackendTaskColdMirrorTimeoutFallsBackAndLaunches(t *testing.T) {
	testTaskColdMirrorTimeoutFallsBackAndLaunches(t, 1, func(t *testing.T, h *containerTestHarness) string {
		ctx := t.Context()
		const planID, operationID, conversation = "plan_backend", "op_saved_backend", "mirror-chat"
		repos := []string{"repo_web"}
		todo, err := h.s.createTaskTodoWithRepositories(ctx, "todo_backend", "Prepare workspace", "Update the example repository", conversation, planID, "", "", repos)
		if err != nil {
			t.Fatal(err)
		}
		if err := h.s.ensureBackendWorkspacePlan(ctx, todo.ID, namingRequest{
			OperationID: operationID, MachineID: "box", Layout: repository.LayoutRoot, SelectedRepoIDs: repos,
		}); err != nil {
			t.Fatal(err)
		}
		// The saved operation can be admitted even if the task plan has no workspace preview.
		task := PlanTask{TodoID: todo.ID, Machine: "box", Title: todo.Title, Prompt: todo.Notes, Role: "worker",
			Workspace: &TaskWorkspace{Mode: workspaceBackend, Backend: &BackendWorkspaceSelection{OperationID: operationID}}}
		now := time.Now().UTC()
		plan := &TaskPlan{ID: planID, Conversation: conversation, Tasks: []PlanTask{task}, State: planStarted, CreatedAt: now, UpdatedAt: now}
		if err := h.s.savePlan(ctx, plan); err != nil {
			t.Fatal(err)
		}
		if err := h.s.failTodo(ctx, todo.ID, "Previous start was interrupted."); err != nil {
			t.Fatal(err)
		}
		if _, err := h.s.retryTask(ctx, todo.ID); err != nil {
			t.Fatal(err)
		}
		return todo.ID
	})
}

func testTaskColdMirrorTimeoutFallsBackAndLaunches(t *testing.T, repositories int, start func(*testing.T, *containerTestHarness) string) {
	t.Helper()
	// Keep the harness's local model listener outside the virtual clock.
	h := newWorkspaceTestHarness(t, newMemRepoMirrorStore())
	// Disable the Agent's background tab naming so it cannot open a real network connection in the bubble.
	h.f.mu.Lock()
	h.f.hello.Models.AI = ai.Config{}
	h.f.mu.Unlock()
	synctest.Test(t, func(t *testing.T) {
		prepareTimeout := taskworkspace.RPCTimeout + time.Duration(repositories)*(taskworkspace.GitTimeout+taskworkspace.ColdFetchTimeout)
		timeouts := map[string]time.Duration{
			taskworkspace.MethodPrefix + "prepare": prepareTimeout,
			taskworkspace.MethodPrefix + "acquire": taskworkspace.RPCTimeout,
			devicewire.MethodStartBackendSession:   taskStartTimeout,
		}
		var mirrorWaits []time.Duration
		launched := make(chan struct{}, 1)
		setHub(h.s, &firstToolsTimingHub{Hub: h.f, beforeCall: func(ctx context.Context, call DeviceCall) (json.RawMessage, error) {
			if call.Method == repository.MethodRepoMirrorEnsure {
				started := time.Now()
				<-ctx.Done()
				mirrorWaits = append(mirrorWaits, time.Since(started))
				return nil, ctx.Err()
			}
			if timeout, ok := timeouts[call.Method]; ok {
				if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) != timeout {
					t.Errorf("%s deadline leaves %v, want %v", call.Method, time.Until(deadline), timeout)
				}
				// Use almost all of each RPC budget after the mirrors have timed out.
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(timeout - time.Second):
				}
			}
			out, err := h.f.DeviceCall(ctx, call)
			if call.Method == devicewire.MethodStartBackendSession && err == nil {
				launched <- struct{}{}
			}
			return out, err
		}})

		todoID := start(t, h)
		launchWait := time.Duration(repositories)*repositorywire.MirrorEnsureTimeout + prepareTimeout + taskworkspace.RPCTimeout + taskStartTimeout + time.Minute
		select {
		case <-launched:
		case <-time.After(launchWait):
			t.Errorf("agent did not launch after a direct workspace prepare")
		}
		synctest.Wait()
		if want := slices.Repeat([]time.Duration{repositorywire.MirrorEnsureTimeout}, repositories); !slices.Equal(mirrorWaits, want) {
			t.Errorf("mirror attempts waited %v, want %v", mirrorWaits, want)
		}
		h.mu.Lock()
		defer h.mu.Unlock()
		if h.prepareCalls != 1 || len(h.lastPrepare.MirrorRepos) != 0 {
			t.Errorf("workspace prepare calls = %d, supplied mirrors = %v, want one direct prepare", h.prepareCalls, h.lastPrepare.MirrorRepos)
		}
		if todo, ok := h.s.todoNow(todoID); !ok || len(todo.Agents) != 1 {
			t.Errorf("expected one agent linked to the task, found %d (%s)", len(todo.Agents), todo.StartError)
		}
	})
}

func TestPrepareWorkspaceMirrorsStopsOnCallerCancellation(t *testing.T) {
	st := newMemRepoMirrorStore()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var calls int
	hub := &repoMirrorTestHub{fakeHub: newFakeHub(), onCall: func(context.Context, string, string, json.RawMessage) (any, error) {
		calls++
		cancel()
		return nil, context.Canceled
	}}
	s := New(Options{Hub: hub, Store: func() Store { return st }})
	r := &taskworkspace.Record{}
	r.Plan.Source.MachineID = "dev1"
	for _, repoID := range []string{"repo_web", "repo_api"} {
		url := "https://github.com/example/" + repoID
		seedMirrorRepo(t, st, repoID, url)
		id, _ := repository.Parse(url, "")
		r.Plan.Source.Repositories.Repositories = append(r.Plan.Source.Repositories.Repositories, repository.PreparationRepository{
			RepoID:         repoID,
			SourceSnapshot: repository.SourceSnapshot{Identity: id, CloneURL: url, CatalogRevision: 1},
		})
	}
	if ready := s.prepareEnabledMirrorsForWorkspace(ctx, r); len(ready) != 0 || calls != 1 {
		t.Fatalf("after cancellation: ready = %v, RPCs = %d, want no mirror and one RPC", ready, calls)
	}
	if rec, err := st.RepoMirror(t.Context(), "dev1", "repo_api"); err != nil || rec != nil {
		t.Fatalf("enrolled the second repository after cancellation: %+v (%v)", rec, err)
	}
}

func TestPrepareBackendWorkspaceFreshRefreshPreservesAutoSyncSchedule(t *testing.T) {
	st := newMemRepoMirrorStore()
	seedMirrorRepo(t, st, "repo_web", "https://github.com/example/web")
	id, _ := repository.Parse("https://github.com/example/web", "")

	baseTime := time.Date(2026, 11, 22, 12, 0, 0, 0, time.UTC)
	now := baseTime
	scheduledNext := baseTime.Add(15 * time.Minute)
	if _, err := st.UpsertRepoMirrorConfig(t.Context(), repository.RepoMirrorRecord{
		MachineID:           "dev1",
		RepoID:              "repo_web",
		Enabled:             true,
		AutoSync:            true,
		SyncIntervalMinutes: 15,
		NextSyncAt:          &scheduledNext,
		UpdatedAt:           baseTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SaveRepoMirrorSyncResult(t.Context(), "dev1", "repo_web", repository.RepoMirrorSyncUpdate{
		LastSyncedAt:   &baseTime,
		LastHeadCommit: strings.Repeat("a", 40),
		LastDefaultRef: "refs/heads/main",
		DiskBytes:      2048,
		HasDiskBytes:   true,
		NextSyncAt:     &scheduledNext,
		UpdatedAt:      baseTime,
	}); err != nil {
		t.Fatal(err)
	}

	var nodeFetched bool
	var ensureCalls int
	baseHub := newFakeHub()
	baseHub.devices = []Device{{ID: "dev1", Name: "Dev 1", Online: true}}
	hub := &repoMirrorTestHub{
		fakeHub: baseHub,
		onCall: func(_ context.Context, _ string, method string, _ json.RawMessage) (any, error) {
			if method != repository.MethodRepoMirrorEnsure {
				return nil, errors.New("unexpected method " + method)
			}
			ensureCalls++
			if !nodeFetched {
				t0 := baseTime
				return repository.MirrorInfo{
					RepoID:        "repo_web",
					Exists:        true,
					HeadCommit:    strings.Repeat("a", 40),
					DefaultRef:    "refs/heads/main",
					LastSyncedAt:  &t0,
					DiskBytes:     2048,
					Fetched:       false,
					SkippedReason: "ttl_fresh",
				}, nil
			}
			t0 := now
			return repository.MirrorInfo{
				RepoID:       "repo_web",
				Exists:       true,
				HeadCommit:   strings.Repeat("b", 40),
				DefaultRef:   "refs/heads/main",
				LastSyncedAt: &t0,
				DiskBytes:    2048,
				Fetched:      true,
			}, nil
		},
	}

	s := New(Options{Hub: hub, Store: func() Store { return st }})
	s.repoMirrors.now = func() time.Time { return now }
	s.repoMirrors.jitter = func(time.Duration) time.Duration { return 0 }

	wsRec := &taskworkspace.Record{
		Plan: workspacenaming.Plan{
			Source: workspacenaming.Source{
				MachineID: "dev1",
				Repositories: repository.PreparationSnapshot{
					Repositories: []repository.PreparationRepository{{
						RepoID:       "repo_web",
						RequestedRef: repository.RequestedRef{Kind: "default"},
						SourceSnapshot: repository.SourceSnapshot{
							Identity: id,
							CloneURL: "https://github.com/example/web",
						},
					}},
				},
			},
		},
	}

	// Repeated fresh workspace prepares within TTL must not push next_sync_at forward.
	for _, offset := range []time.Duration{2 * time.Minute, 4 * time.Minute} {
		now = baseTime.Add(offset)
		nodeFetched = false
		ready := s.prepareEnabledMirrorsForWorkspace(t.Context(), wsRec)
		if !slices.Equal(ready, []string{"repo_web"}) {
			t.Fatalf("at +%v: expected repo_web ready, got %v", offset, ready)
		}
		got, err := st.RepoMirror(t.Context(), "dev1", "repo_web")
		if err != nil || got == nil || got.NextSyncAt == nil || !got.NextSyncAt.Equal(scheduledNext) {
			t.Fatalf("at +%v: expected next_sync_at=%v preserved, got %+v (%v)", offset, scheduledNext, got, err)
		}
	}

	// Even a workspace prepare that does fetch (>5m TTL) preserves the periodic schedule.
	now = baseTime.Add(8 * time.Minute)
	nodeFetched = true
	ready := s.prepareEnabledMirrorsForWorkspace(t.Context(), wsRec)
	if !slices.Equal(ready, []string{"repo_web"}) {
		t.Fatalf("at +8m: expected repo_web ready, got %v", ready)
	}
	got, _ := st.RepoMirror(t.Context(), "dev1", "repo_web")
	if got.NextSyncAt == nil || !got.NextSyncAt.Equal(scheduledNext) || got.LastHeadCommit != strings.Repeat("b", 40) {
		t.Fatalf("at +8m: expected head updated and next_sync_at=%v preserved, got %+v", scheduledNext, got)
	}

	// When the scheduled time arrives, the periodic pass selects the mirror and advances next_sync_at.
	now = scheduledNext
	nodeFetched = true
	nextAfterPeriodic := s.runRepoMirrorSyncPass(t.Context())
	wantNext := scheduledNext.Add(15 * time.Minute)
	if ensureCalls != 4 || !nextAfterPeriodic.Equal(wantNext) {
		t.Fatalf("periodic pass: calls=%d next=%v want=%v", ensureCalls, nextAfterPeriodic, wantNext)
	}
	got, _ = st.RepoMirror(t.Context(), "dev1", "repo_web")
	if got.NextSyncAt == nil || !got.NextSyncAt.Equal(wantNext) {
		t.Fatalf("expected periodic pass to advance next_sync_at to %v, got %+v", wantNext, got)
	}
}
