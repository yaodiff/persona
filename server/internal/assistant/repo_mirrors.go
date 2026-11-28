package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/rand/v2"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/repositorywire"
	"github.com/nwverse/persona/server/internal/taskworkspace"
)

type repoMirrorStore interface {
	RepoMirror(ctx context.Context, machineID, repoID string) (*repository.RepoMirrorRecord, error)
	ListRepoMirrors(ctx context.Context, machineID, repoID string) ([]repository.RepoMirrorRecord, error)
	CreateOnDemandRepoMirror(ctx context.Context, machineID, repoID string, now time.Time) (*repository.RepoMirrorRecord, error)
	ListDueAutoSyncRepoMirrors(ctx context.Context, now time.Time) ([]repository.RepoMirrorRecord, error)
	UpsertRepoMirrorConfig(ctx context.Context, cfg repository.RepoMirrorRecord) (repository.RepoMirrorRecord, error)
	SaveRepoMirrorSyncResult(ctx context.Context, machineID, repoID string, res repository.RepoMirrorSyncUpdate) error
	DeleteRepoMirror(ctx context.Context, machineID, repoID string) error
}

const (
	repoMirrorLoopIdleWait   = time.Minute
	repoMirrorCallTimeout    = repositorywire.MirrorEnsureTimeout
	repoMirrorMaxBackoff     = 6 * time.Hour
	repoMirrorErrorMaxRunes  = 300
	repoMirrorJitterFraction = 0.15
)

// repoMirrorSyncState holds injectable dependencies and wakeup signaling for low-frequency periodic mirror sync.
// Its zero value is ready to use with zero background activity when no mirrors have auto_sync enabled.
type repoMirrorSyncState struct {
	mu       sync.Mutex
	wake     chan struct{}
	now      func() time.Time
	jitter   func(base time.Duration) time.Duration
	idleWait time.Duration
}

func (m *repoMirrorSyncState) wakeup() <-chan struct{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.wake == nil {
		m.wake = make(chan struct{}, 1)
	}
	return m.wake
}

func (m *repoMirrorSyncState) signal() {
	m.mu.Lock()
	ch := m.wake
	m.mu.Unlock()
	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func (m *repoMirrorSyncState) clock() time.Time {
	if m.now != nil {
		return m.now().UTC()
	}
	return time.Now().UTC()
}

func (m *repoMirrorSyncState) jitterDelta(base time.Duration) time.Duration {
	if m.jitter != nil {
		return m.jitter(base)
	}
	spread := int64(float64(base) * repoMirrorJitterFraction)
	if spread <= 0 {
		return 0
	}
	// Uniformly distributed in [-spread, +spread].
	return time.Duration(rand.Int64N(2*spread+1) - spread)
}

func (m *repoMirrorSyncState) waitDuration() time.Duration {
	if m.idleWait > 0 {
		return m.idleWait
	}
	return repoMirrorLoopIdleWait
}

type repoMirrorProblem string

func (e repoMirrorProblem) Error() string { return string(e) }

func (s *Service) repoMirrorStore() (repoMirrorStore, error) {
	st, ok := s.store().(repoMirrorStore)
	if !ok {
		return nil, repoMirrorProblem("store")
	}
	return st, nil
}

func repoMirrorError(w http.ResponseWriter, err error) {
	code, status := "store", http.StatusServiceUnavailable
	var prob repoMirrorProblem
	var repoProb repositoryProblem
	switch {
	case errors.As(err, &prob):
		code = string(prob)
		status = map[string]int{
			"invalid":          http.StatusBadRequest,
			"not_found":        http.StatusNotFound,
			"readonly":         http.StatusForbidden,
			"store":            http.StatusServiceUnavailable,
			"node_unavailable": http.StatusServiceUnavailable,
			"unsupported":      http.StatusConflict,
			"sync_failed":      http.StatusBadGateway,
			"remove_failed":    http.StatusBadGateway,
		}[code]
	case errors.As(err, &repoProb):
		repositoryError(w, err)
		return
	}
	if status == 0 {
		code, status = "store", http.StatusServiceUnavailable
	}
	apierr.WriteCode(w, status, "repo_mirror_"+code, i18n.Err("repo-mirrors."+code))
}

func repoMirrorBody(w http.ResponseWriter, r *http.Request, out any) bool {
	if !fullRole(r) {
		repoMirrorError(w, repoMirrorProblem("readonly"))
		return false
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		repoMirrorError(w, repoMirrorProblem("invalid"))
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		repoMirrorError(w, repoMirrorProblem("invalid"))
		return false
	}
	return true
}

func sanitizeMirrorError(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, strings.TrimSpace(err.Error()))
	runes := []rune(msg)
	if len(runes) > repoMirrorErrorMaxRunes {
		return string(runes[:repoMirrorErrorMaxRunes])
	}
	return msg
}

func isUnknownMirrorMethodError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "unknown method repo.mirror.") || strings.Contains(msg, "unknown method")
}

// nextRepoMirrorSyncTime computes the next periodic sync time with exponential backoff on failures and jitter.
func nextRepoMirrorSyncTime(now time.Time, intervalMinutes, consecutiveFailures int, jitterFn func(time.Duration) time.Duration) time.Time {
	if intervalMinutes < repository.MinMirrorSyncIntervalMinutes {
		intervalMinutes = repository.DefaultMirrorSyncIntervalMinutes
	}
	if intervalMinutes > repository.MaxMirrorSyncIntervalMinutes {
		intervalMinutes = repository.MaxMirrorSyncIntervalMinutes
	}
	base := time.Duration(intervalMinutes) * time.Minute
	delay := base
	if consecutiveFailures > 0 {
		shift := min(consecutiveFailures-1, 4)
		delay = base << shift
		capDelay := max(repoMirrorMaxBackoff, base)
		if delay > capDelay {
			delay = capDelay
		}
	}
	if jitterFn != nil {
		delta := jitterFn(delay)
		if delay+delta >= time.Minute {
			delay += delta
		}
	}
	return now.UTC().Add(delay)
}

func cloneURLForBinding(binding RepositoryBinding) string {
	if binding.CloneURL != "" {
		return binding.CloneURL
	}
	return binding.Identity.CanonicalURL
}

func (s *Service) defaultMirrorRecord(machineID, repoID string) repository.RepoMirrorRecord {
	return repository.RepoMirrorRecord{
		MachineID:           machineID,
		RepoID:              repoID,
		Enabled:             false,
		AutoSync:            false,
		SyncIntervalMinutes: repository.DefaultMirrorSyncIntervalMinutes,
	}
}

func (s *Service) repoMirrorRoutes() {
	s.handle("GET /api/v1/assistant/repositories/mirrors", func(w http.ResponseWriter, r *http.Request) {
		st, err := s.repoMirrorStore()
		if err != nil {
			repoMirrorError(w, err)
			return
		}
		machineID := strings.TrimSpace(r.URL.Query().Get("machine_id"))
		repoID := strings.TrimSpace(r.URL.Query().Get("repo_id"))
		if machineID != "" && !repository.ValidateMirrorMachineID(machineID) {
			repoMirrorError(w, repoMirrorProblem("invalid"))
			return
		}
		if repoID != "" && !repository.ValidateMirrorRepoID(repoID) {
			repoMirrorError(w, repoMirrorProblem("invalid"))
			return
		}
		records, err := st.ListRepoMirrors(r.Context(), machineID, repoID)
		if err != nil {
			repoMirrorError(w, repoMirrorProblem("store"))
			return
		}
		apierr.WriteJSON(w, map[string]any{"manager": s.managerIdentity(), "mirrors": records})
	})

	s.handle("GET /api/v1/assistant/repositories/{id}/mirrors/{machine_id}", func(w http.ResponseWriter, r *http.Request) {
		repoID := r.PathValue("id")
		machineID := r.PathValue("machine_id")
		if !repository.ValidateMirrorRepoID(repoID) || !repository.ValidateMirrorMachineID(machineID) {
			repoMirrorError(w, repoMirrorProblem("invalid"))
			return
		}
		st, err := s.repoMirrorStore()
		if err != nil {
			repoMirrorError(w, err)
			return
		}
		rec, err := st.RepoMirror(r.Context(), machineID, repoID)
		if err != nil {
			repoMirrorError(w, repoMirrorProblem("store"))
			return
		}
		out := s.defaultMirrorRecord(machineID, repoID)
		if rec != nil {
			out = *rec
		}
		resp := map[string]any{"manager": s.managerIdentity(), "mirror": out}
		if r.URL.Query().Get("live") == "1" {
			if _, ok := s.device(r.Context(), machineID); ok {
				var live repository.MirrorStatusResponse
				callCtx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
				err := s.call(callCtx, machineID, repository.MethodRepoMirrorStatus, repository.MirrorStatusRequest{RepoID: repoID}, &live)
				cancel()
				if err == nil && live.Mirror != nil {
					resp["live"] = live.Mirror
				}
			}
		}
		apierr.WriteJSON(w, resp)
	})

	s.handle("PUT /api/v1/assistant/repositories/{id}/mirrors/{machine_id}", func(w http.ResponseWriter, r *http.Request) {
		repoID := r.PathValue("id")
		machineID := r.PathValue("machine_id")
		if !repository.ValidateMirrorRepoID(repoID) || !repository.ValidateMirrorMachineID(machineID) {
			repoMirrorError(w, repoMirrorProblem("invalid"))
			return
		}
		var in repository.RepoMirrorConfigInput
		if !repoMirrorBody(w, r, &in) {
			return
		}
		if in.AutoSync && !in.Enabled {
			repoMirrorError(w, repoMirrorProblem("invalid"))
			return
		}
		interval, err := repository.NormalizeMirrorSyncIntervalMinutes(in.SyncIntervalMinutes)
		if err != nil {
			repoMirrorError(w, repoMirrorProblem("invalid"))
			return
		}
		if _, err := s.repositoryByID(r.Context(), repoID); err != nil {
			repoMirrorError(w, err)
			return
		}
		st, err := s.repoMirrorStore()
		if err != nil {
			repoMirrorError(w, err)
			return
		}
		now := s.repoMirrors.clock()
		existing, err := st.RepoMirror(r.Context(), machineID, repoID)
		if err != nil {
			repoMirrorError(w, repoMirrorProblem("store"))
			return
		}
		var nextSyncAt *time.Time
		if in.Enabled && in.AutoSync {
			if existing != nil && existing.LastSyncedAt != nil {
				t := nextRepoMirrorSyncTime(now, interval, 0, s.repoMirrors.jitterDelta)
				nextSyncAt = &t
			} else {
				// Never synced yet: eligible on the next pass immediately.
				t := now
				nextSyncAt = &t
			}
		}
		saved, err := st.UpsertRepoMirrorConfig(r.Context(), repository.RepoMirrorRecord{
			MachineID:           machineID,
			RepoID:              repoID,
			Enabled:             in.Enabled,
			AutoSync:            in.AutoSync,
			SyncIntervalMinutes: interval,
			NextSyncAt:          nextSyncAt,
			UpdatedAt:           now,
		})
		if err != nil {
			repoMirrorError(w, repoMirrorProblem("store"))
			return
		}
		if saved.Enabled && saved.AutoSync {
			s.repoMirrors.signal()
		}
		apierr.WriteJSON(w, map[string]any{"manager": s.managerIdentity(), "mirror": saved})
	})

	s.handle(assistantwire.RouteRepoMirrorSync, func(w http.ResponseWriter, r *http.Request) {
		repoID := r.PathValue("id")
		machineID := r.PathValue("machine_id")
		if !repository.ValidateMirrorRepoID(repoID) || !repository.ValidateMirrorMachineID(machineID) {
			repoMirrorError(w, repoMirrorProblem("invalid"))
			return
		}
		var in struct {
			Force      bool `json:"force,omitempty"`
			TTLSeconds int  `json:"ttl_seconds,omitempty"`
		}
		if !fullRole(r) {
			repoMirrorError(w, repoMirrorProblem("readonly"))
			return
		}
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
			d := json.NewDecoder(r.Body)
			d.DisallowUnknownFields()
			if err := d.Decode(&in); err != nil {
				if !errors.Is(err, io.EOF) {
					repoMirrorError(w, repoMirrorProblem("invalid"))
					return
				}
			} else if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
				repoMirrorError(w, repoMirrorProblem("invalid"))
				return
			}
		}
		if _, err := repository.NormalizeMirrorTTLSeconds(in.TTLSeconds); err != nil {
			repoMirrorError(w, repoMirrorProblem("invalid"))
			return
		}
		rec, live, err := s.EnsureRepoMirror(r.Context(), machineID, repoID, in.TTLSeconds, in.Force)
		if err != nil {
			repoMirrorError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"manager": s.managerIdentity(), "mirror": rec, "live": live})
	})

	s.handle("DELETE /api/v1/assistant/repositories/{id}/mirrors/{machine_id}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			repoMirrorError(w, repoMirrorProblem("readonly"))
			return
		}
		repoID := r.PathValue("id")
		machineID := r.PathValue("machine_id")
		if !repository.ValidateMirrorRepoID(repoID) || !repository.ValidateMirrorMachineID(machineID) {
			repoMirrorError(w, repoMirrorProblem("invalid"))
			return
		}
		force := r.URL.Query().Get("force") == "1"
		res, err := s.RemoveRepoMirror(r.Context(), machineID, repoID, force)
		if err != nil {
			repoMirrorError(w, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"manager": s.managerIdentity(), "removed": res.Removed, "freed_bytes": res.FreedBytes})
	})
}

// EnsureRepoMirror triggers repo.mirror.ensure on machineID for an enabled mirror and persists sync status.
func (s *Service) EnsureRepoMirror(ctx context.Context, machineID, repoID string, ttlSeconds int, force bool) (repository.RepoMirrorRecord, repository.MirrorInfo, error) {
	st, err := s.repoMirrorStore()
	if err != nil {
		return repository.RepoMirrorRecord{}, repository.MirrorInfo{}, err
	}
	rec, err := st.RepoMirror(ctx, machineID, repoID)
	if err != nil {
		return repository.RepoMirrorRecord{}, repository.MirrorInfo{}, repoMirrorProblem("store")
	}
	if rec == nil || !rec.Enabled {
		return repository.RepoMirrorRecord{}, repository.MirrorInfo{}, repoMirrorProblem("not_found")
	}
	binding, err := s.repositoryByID(ctx, repoID)
	if err != nil {
		return repository.RepoMirrorRecord{}, repository.MirrorInfo{}, err
	}
	if _, ok := s.device(ctx, machineID); !ok {
		return repository.RepoMirrorRecord{}, repository.MirrorInfo{}, repoMirrorProblem("node_unavailable")
	}
	info, err := s.callAndRecordRepoMirrorEnsure(ctx, st, *rec, binding.Identity, cloneURLForBinding(binding), ttlSeconds, force, false)
	if err != nil {
		return repository.RepoMirrorRecord{}, repository.MirrorInfo{}, err
	}
	updated, readErr := st.RepoMirror(ctx, machineID, repoID)
	if readErr != nil || updated == nil {
		return *rec, info, nil
	}
	return *updated, info, nil
}

// RemoveRepoMirror deletes the managed bare mirror on machineID and removes its Manager configuration row.
func (s *Service) RemoveRepoMirror(ctx context.Context, machineID, repoID string, forceLocalOnly bool) (repository.MirrorRemoveResponse, error) {
	st, err := s.repoMirrorStore()
	if err != nil {
		return repository.MirrorRemoveResponse{}, err
	}
	var res repository.MirrorRemoveResponse
	if _, ok := s.device(ctx, machineID); ok {
		callCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = s.call(callCtx, machineID, repository.MethodRepoMirrorRemove, repository.MirrorRemoveRequest{RepoID: repoID}, &res)
		cancel()
		if err != nil && !isUnknownMirrorMethodError(err) {
			if !forceLocalOnly {
				return repository.MirrorRemoveResponse{}, repoMirrorProblem("remove_failed")
			}
		}
	} else if !forceLocalOnly {
		return repository.MirrorRemoveResponse{}, repoMirrorProblem("node_unavailable")
	}
	if err := st.DeleteRepoMirror(ctx, machineID, repoID); err != nil {
		return repository.MirrorRemoveResponse{}, repoMirrorProblem("store")
	}
	return res, nil
}

func (s *Service) callAndRecordRepoMirrorEnsure(ctx context.Context, st repoMirrorStore, rec repository.RepoMirrorRecord, identity repository.Identity, cloneURL string, ttlSeconds int, force, periodic bool) (repository.MirrorInfo, error) {
	if ttlSeconds == 0 {
		ttlSeconds = repository.DefaultMirrorFetchTTLSeconds
	}
	var info repository.MirrorInfo
	callCtx, cancel := context.WithTimeout(ctx, repoMirrorCallTimeout)
	err := s.call(callCtx, rec.MachineID, repository.MethodRepoMirrorEnsure, repository.MirrorEnsureRequest{
		RepoID:     rec.RepoID,
		Identity:   identity,
		CloneURL:   cloneURL,
		TTLSeconds: ttlSeconds,
		Force:      force,
	}, &info)
	cancel()
	now := s.repoMirrors.clock()
	if err != nil {
		if isUnknownMirrorMethodError(err) {
			return repository.MirrorInfo{}, repoMirrorProblem("unsupported")
		}
		failures := rec.ConsecutiveFailures
		nextSync := rec.NextSyncAt
		if periodic {
			failures++
			if rec.Enabled && rec.AutoSync {
				t := nextRepoMirrorSyncTime(now, rec.SyncIntervalMinutes, failures, s.repoMirrors.jitterDelta)
				nextSync = &t
			}
		}
		_ = st.SaveRepoMirrorSyncResult(ctx, rec.MachineID, rec.RepoID, repository.RepoMirrorSyncUpdate{
			LastError:           sanitizeMirrorError(err),
			ConsecutiveFailures: failures,
			NextSyncAt:          nextSync,
			UpdatedAt:           now,
		})
		return repository.MirrorInfo{}, repoMirrorProblem("sync_failed")
	}
	// A non-periodic TTL-fresh hit did not fetch; avoid unnecessary writes and never push next_sync_at forward.
	if !periodic && !info.Fetched && info.SkippedReason == "ttl_fresh" && rec.LastSyncedAt != nil {
		return info, nil
	}
	syncedAt := now
	if info.LastSyncedAt != nil {
		syncedAt = info.LastSyncedAt.UTC()
	}
	nextSync := rec.NextSyncAt
	if periodic && rec.Enabled && rec.AutoSync {
		t := nextRepoMirrorSyncTime(now, rec.SyncIntervalMinutes, 0, s.repoMirrors.jitterDelta)
		nextSync = &t
	}
	_ = st.SaveRepoMirrorSyncResult(ctx, rec.MachineID, rec.RepoID, repository.RepoMirrorSyncUpdate{
		LastSyncedAt:        &syncedAt,
		LastHeadCommit:      info.HeadCommit,
		LastDefaultRef:      info.DefaultRef,
		LastError:           "",
		DiskBytes:           info.DiskBytes,
		HasDiskBytes:        true,
		ConsecutiveFailures: 0,
		NextSyncAt:          nextSync,
		UpdatedAt:           now,
	})
	return info, nil
}

// prepareEnabledMirrorsForWorkspace enrolls registered remote repositories on demand, honors saved
// settings, and returns the IDs of mirrors ready within TTL. Workspaces can prepare directly when
// a mirror is unavailable or the Node does not support the mirror RPC.
func (s *Service) prepareEnabledMirrorsForWorkspace(ctx context.Context, r *taskworkspace.Record) []string {
	if ctx.Err() != nil || r == nil || len(r.Plan.Source.Repositories.Repositories) == 0 {
		return nil
	}
	st, err := s.repoMirrorStore()
	if err != nil {
		return nil
	}
	machineID := r.Plan.Source.MachineID
	var ready []string
	for _, prep := range r.Plan.Source.Repositories.Repositories {
		if ctx.Err() != nil {
			break
		}
		if prep.Local != nil || !repository.ValidateMirrorRepoID(prep.RepoID) {
			continue
		}
		rec, err := st.RepoMirror(ctx, machineID, prep.RepoID)
		// Link-only repositories have no catalog revision in the saved source snapshot.
		if err == nil && rec == nil && prep.SourceSnapshot.CatalogRevision > 0 && ctx.Err() == nil {
			rec, err = st.CreateOnDemandRepoMirror(ctx, machineID, prep.RepoID, s.repoMirrors.clock())
		}
		if err != nil || rec == nil || !rec.Enabled || ctx.Err() != nil {
			continue
		}
		info, ensureErr := s.callAndRecordRepoMirrorEnsure(
			ctx, st, *rec,
			prep.SourceSnapshot.Identity,
			prep.SourceSnapshot.CloneURL,
			repository.DefaultMirrorFetchTTLSeconds,
			false,
			false,
		)
		if ensureErr != nil {
			if errors.Is(ensureErr, repoMirrorProblem("unsupported")) {
				return nil
			}
			continue
		}
		if info.Exists {
			ready = append(ready, rec.RepoID)
		}
	}
	return ready
}

// repoMirrorSyncLoop runs optional, opt-in low-frequency periodic mirror syncs.
// When no mirrors have enabled=true && auto_sync=true, it performs zero Node RPCs.
func (s *Service) repoMirrorSyncLoop(ctx context.Context) {
	wake := s.repoMirrors.wakeup()
	for {
		wait := s.repoMirrors.waitDuration()
		if !s.hello(ctx).Assistant.Standby {
			if next := s.runRepoMirrorSyncPass(ctx); !next.IsZero() {
				if delta := next.Sub(s.repoMirrors.clock()); delta < wait {
					wait = max(delta, time.Second)
				}
			}
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-wake:
			t.Stop()
		case <-t.C:
		}
	}
}

// runRepoMirrorSyncPass executes one sweep of due auto-sync mirrors and returns the earliest
// next scheduled sync time (zero if none are due or no auto-sync mirrors exist).
func (s *Service) runRepoMirrorSyncPass(ctx context.Context) time.Time {
	st, err := s.repoMirrorStore()
	if err != nil {
		return time.Time{}
	}
	now := s.repoMirrors.clock()
	due, err := st.ListDueAutoSyncRepoMirrors(ctx, now)
	if err != nil || len(due) == 0 {
		return time.Time{}
	}
	var earliest time.Time
	for _, rec := range due {
		if ctx.Err() != nil {
			break
		}
		if _, online := s.device(ctx, rec.MachineID); !online {
			next := nextRepoMirrorSyncTime(now, rec.SyncIntervalMinutes, rec.ConsecutiveFailures+1, s.repoMirrors.jitterDelta)
			_ = st.SaveRepoMirrorSyncResult(ctx, rec.MachineID, rec.RepoID, repository.RepoMirrorSyncUpdate{
				LastError:           "node_unavailable",
				ConsecutiveFailures: rec.ConsecutiveFailures + 1,
				NextSyncAt:          &next,
				UpdatedAt:           now,
			})
			if earliest.IsZero() || next.Before(earliest) {
				earliest = next
			}
			continue
		}
		binding, err := s.repositoryByID(ctx, rec.RepoID)
		if err != nil {
			next := nextRepoMirrorSyncTime(now, rec.SyncIntervalMinutes, rec.ConsecutiveFailures+1, s.repoMirrors.jitterDelta)
			_ = st.SaveRepoMirrorSyncResult(ctx, rec.MachineID, rec.RepoID, repository.RepoMirrorSyncUpdate{
				LastError:           sanitizeMirrorError(err),
				ConsecutiveFailures: rec.ConsecutiveFailures + 1,
				NextSyncAt:          &next,
				UpdatedAt:           now,
			})
			if earliest.IsZero() || next.Before(earliest) {
				earliest = next
			}
			continue
		}
		_, ensureErr := s.callAndRecordRepoMirrorEnsure(ctx, st, rec, binding.Identity, cloneURLForBinding(binding), repository.DefaultMirrorFetchTTLSeconds, false, true)
		if errors.Is(ensureErr, repoMirrorProblem("unsupported")) {
			next := nextRepoMirrorSyncTime(now, rec.SyncIntervalMinutes, rec.ConsecutiveFailures+1, s.repoMirrors.jitterDelta)
			_ = st.SaveRepoMirrorSyncResult(ctx, rec.MachineID, rec.RepoID, repository.RepoMirrorSyncUpdate{
				LastError:           "unsupported",
				ConsecutiveFailures: rec.ConsecutiveFailures + 1,
				NextSyncAt:          &next,
				UpdatedAt:           now,
			})
		}
		if updated, err := st.RepoMirror(ctx, rec.MachineID, rec.RepoID); err == nil && updated != nil && updated.NextSyncAt != nil {
			if earliest.IsZero() || updated.NextSyncAt.Before(earliest) {
				earliest = *updated.NextSyncAt
			}
		}
	}
	return earliest
}
