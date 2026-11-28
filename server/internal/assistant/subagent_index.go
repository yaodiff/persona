package assistant

import (
	"cmp"
	"context"
	"strings"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// subagentLink records durable parent provenance for a manager-launched subagent session
// (A2A launch, cross-machine dispatch, or task_spawn) so decoration and cross-machine
// folding survive manager restarts without age or row-count truncation.
type subagentLink struct {
	ParentMachine string
	ParentSession string
	A2ATaskID     string
	Origin        string
}

func (s *Service) ensureLegacySubagentsLoaded(ctx context.Context) {
	f := &s.fleet
	f.subagentsMu.RLock()
	loaded := f.subagentsLegacyLoaded
	f.subagentsMu.RUnlock()
	if loaded {
		return
	}
	if s.store() == nil {
		f.subagentsMu.Lock()
		f.subagentsLegacyLoaded = true
		f.subagentsMu.Unlock()
		return
	}
	type legacySubagent struct {
		machine, session string
		link             subagentLink
	}
	var legacy []legacySubagent
	for _, dt := range s.dispatched(ctx) {
		if dt.FromSession == "" || dt.Session == "" {
			continue
		}
		legacy = append(legacy, legacySubagent{dt.Machine, dt.Session, subagentLink{
			ParentMachine: dt.FromMachine,
			ParentSession: dt.FromSession,
			Origin:        proto.SubagentOriginDispatch,
		}})
	}
	addSpawned := func(machine, session, source, via string) {
		if source != sourceSpawned || via == "" || machine == "" || session == "" {
			return
		}
		pm, ps, ok := strings.Cut(via, "/")
		if !ok || pm == "" || ps == "" {
			return
		}
		legacy = append(legacy, legacySubagent{machine, session, subagentLink{
			ParentMachine: pm,
			ParentSession: ps,
			Origin:        proto.SubagentOriginSpawned,
		}})
	}
	// Read legacy task_spawn parent links directly instead of calling ensureTodosLoaded:
	// decorateInventory runs on the session-list hot path (and under sentMu), so it must neither
	// run todo startup side effects (supervision backfills, session-list republishing) nor pull
	// ensureTodosLoaded into decorateInventory's static call-graph SCC (which causes escape
	// analysis to spill listedSession's single-row []proto.Session{row} onto the heap).
	todosOK := false
	s.todos.mu.RLock()
	todosLoaded := s.todos.loaded
	if todosLoaded {
		for _, td := range s.todos.todos {
			for _, a := range td.Agents {
				addSpawned(a.MachineID, a.SessionID, a.Source, a.Via)
			}
		}
		todosOK = true
	}
	s.todos.mu.RUnlock()
	if !todosLoaded {
		if st := s.todoDB(); st != nil {
			if alist, err := st.ListAllTodoAgents(ctx); err != nil {
				s.logf("subagent index: load legacy todo agents: %v", err)
			} else {
				for _, a := range alist {
					addSpawned(a.MachineID, a.SessionID, a.Source, a.Via)
				}
				todosOK = true
			}
		} else {
			s.todos.mu.RLock()
			for _, td := range s.todos.todos {
				for _, a := range td.Agents {
					addSpawned(a.MachineID, a.SessionID, a.Source, a.Via)
				}
			}
			s.todos.mu.RUnlock()
			todosOK = true
		}
	}
	f.subagentsMu.Lock()
	for _, item := range legacy {
		key := [2]string{item.machine, item.session}
		if f.subagents != nil {
			if _, exists := f.subagents[key]; exists {
				continue
			}
		}
		s.putSubagentLinkLocked(item.machine, item.session, item.link)
	}
	if todosOK {
		f.subagentsLegacyLoaded = true
	}
	f.subagentsMu.Unlock()
}

// ensureSubagentsChecked resolves durable A2A launch provenance for any unchecked target
// sessions on machine via an exact target-set query (no age or global row limit).
func (s *Service) ensureSubagentsChecked(ctx context.Context, machine string, sessions []string) {
	if machine == "" || len(sessions) == 0 {
		return
	}
	s.ensureLegacySubagentsLoaded(ctx)
	if s.store() == nil {
		return
	}
	st, err := s.a2aStore()
	if err != nil {
		return
	}
	f := &s.fleet
	var missing []string
	f.subagentsMu.RLock()
	bucket := f.subagentsChecked[machine]
	for _, sid := range sessions {
		if sid == "" {
			continue
		}
		if _, ok := bucket[sid]; !ok {
			missing = append(missing, sid)
		}
	}
	f.subagentsMu.RUnlock()
	if len(missing) == 0 {
		return
	}
	rows, err := st.A2ALaunchesForTargets(ctx, machine, missing)
	if err != nil {
		s.logf("subagent index: load A2A launches for %s: %v", machine, err)
		return
	}
	s.applyLoadedA2ALaunches(machine, missing, rows)
}

func (s *Service) applyLoadedA2ALaunches(machine string, checked []string, rows []db.A2ARow) {
	f := &s.fleet
	f.subagentsMu.Lock()
	defer f.subagentsMu.Unlock()
	if f.subagentsChecked == nil {
		f.subagentsChecked = map[string]map[string]struct{}{}
	}
	bucket := f.subagentsChecked[machine]
	if bucket == nil {
		bucket = make(map[string]struct{}, len(checked))
		f.subagentsChecked[machine] = bucket
	}
	for _, sid := range checked {
		bucket[sid] = struct{}{}
	}
	seenTarget := map[string]bool{}
	for i := range rows {
		t, err := decodeA2A(&rows[i], nil)
		if err != nil || t == nil || t.Launch == nil || t.SourceSession == "" || t.TargetSession == "" {
			continue
		}
		if seenTarget[t.TargetSession] {
			continue // rows are newest first; keep the newest launch row per target
		}
		seenTarget[t.TargetSession] = true
		origin := proto.SubagentOriginA2A
		if t.Launch.Dispatch {
			origin = proto.SubagentOriginDispatch
		}
		s.putSubagentLinkLocked(t.TargetMachine, t.TargetSession, subagentLink{
			ParentMachine: t.SourceMachine,
			ParentSession: t.SourceSession,
			A2ATaskID:     t.ID,
			Origin:        origin,
		})
	}
}

func (s *Service) putSubagentLinkLocked(machine, session string, link subagentLink) {
	if machine == "" || session == "" || link.ParentSession == "" {
		return
	}
	f := &s.fleet
	if f.subagents == nil {
		f.subagents = map[[2]string]subagentLink{}
	}
	if f.subagentsChecked == nil {
		f.subagentsChecked = map[string]map[string]struct{}{}
	}
	if f.subagentsChecked[machine] == nil {
		f.subagentsChecked[machine] = map[string]struct{}{}
	}
	key := [2]string{machine, session}
	if link.A2ATaskID != "" {
		if f.subagentsByTask == nil {
			f.subagentsByTask = map[string][2]string{}
		}
		if prev, had := f.subagentsByTask[link.A2ATaskID]; had && prev != key {
			delete(f.subagents, prev)
		}
		f.subagentsByTask[link.A2ATaskID] = key
	}
	f.subagents[key] = link
	f.subagentsChecked[machine][session] = struct{}{}
}

// recordA2ASubagent records a newly created or resolved A2A/dispatch launch in the in-memory
// subagent index so decoration immediately tags the target session without waiting for a reload.
func (s *Service) recordA2ASubagent(t *a2aTask) {
	if t == nil || t.Launch == nil || t.SourceSession == "" || t.TargetMachine == "" || t.TargetSession == "" {
		return
	}
	origin := proto.SubagentOriginA2A
	if t.Launch.Dispatch {
		origin = proto.SubagentOriginDispatch
	}
	s.fleet.subagentsMu.Lock()
	s.putSubagentLinkLocked(t.TargetMachine, t.TargetSession, subagentLink{
		ParentMachine: t.SourceMachine,
		ParentSession: t.SourceSession,
		A2ATaskID:     t.ID,
		Origin:        origin,
	})
	s.fleet.subagentsMu.Unlock()
}

// forgetA2ASubagent removes a reserved launch entry if the launch failed before starting.
func (s *Service) forgetA2ASubagent(taskID string) {
	if taskID == "" {
		return
	}
	s.fleet.subagentsMu.Lock()
	if key, ok := s.fleet.subagentsByTask[taskID]; ok {
		delete(s.fleet.subagentsByTask, taskID)
		if cur, has := s.fleet.subagents[key]; has && cur.A2ATaskID == taskID {
			delete(s.fleet.subagents, key)
		}
	}
	s.fleet.subagentsMu.Unlock()
}

// recordSubagentLink records a non-A2A subagent link (e.g. legacy storeless dispatch or task_spawn).
func (s *Service) recordSubagentLink(machine, session, parentMachine, parentSession, origin string) {
	if machine == "" || session == "" || parentSession == "" {
		return
	}
	s.fleet.subagentsMu.Lock()
	s.putSubagentLinkLocked(machine, session, subagentLink{
		ParentMachine: parentMachine,
		ParentSession: parentSession,
		Origin:        origin,
	})
	s.fleet.subagentsMu.Unlock()
}

func (s *Service) lookupSubagentLink(ctx context.Context, machine, session string) (subagentLink, bool) {
	if machine == "" || session == "" {
		return subagentLink{}, false
	}
	s.ensureSubagentsChecked(ctx, machine, []string{session})
	f := &s.fleet
	f.subagentsMu.RLock()
	link, ok := f.subagents[[2]string{machine, session}]
	f.subagentsMu.RUnlock()
	return link, ok
}

// isA2ASubagent reports whether ref is a session launched as a subagent by an A2A (or A2A-backed dispatch) task.
func (s *Service) isA2ASubagent(ctx context.Context, ref a2aRef) bool {
	if ref.Machine == "" || ref.Session == "" {
		return false
	}
	link, ok := s.lookupSubagentLink(ctx, ref.Machine, ref.Session)
	return ok && link.A2ATaskID != ""
}

// decorateSubagents stamps durable manager-tracked subagent parent provenance (A2A, dispatch,
// task_spawn) onto decorated sessions. Native/runtime node subagents keep their existing wire
// representation untouched, and ordinary rows allocate nothing.
func (s *Service) decorateSubagents(ctx context.Context, machine string, list []proto.Session) {
	if len(list) == 0 {
		return
	}
	s.ensureLegacySubagentsLoaded(ctx)
	if s.store() != nil {
		if st, err := s.a2aStore(); err == nil {
			f := &s.fleet
			var missing []string
			f.subagentsMu.RLock()
			bucket := f.subagentsChecked[machine]
			for i := range list {
				sid := list[i].SessionID
				if !list[i].Assistant && sid != "" {
					if _, ok := bucket[sid]; !ok {
						missing = append(missing, sid)
					}
				}
			}
			f.subagentsMu.RUnlock()
			if len(missing) > 0 {
				if rows, err := st.A2ALaunchesForTargets(ctx, machine, missing); err != nil {
					s.logf("subagent index: load A2A launches for %s: %v", machine, err)
				} else {
					s.applyLoadedA2ALaunches(machine, missing, rows)
				}
			}
		}
	}
	f := &s.fleet
	f.subagentsMu.RLock()
	if len(f.subagents) == 0 {
		f.subagentsMu.RUnlock()
		return
	}
	for i := range list {
		row := &list[i]
		if row.Assistant || row.SessionID == "" {
			continue
		}
		link, ok := f.subagents[[2]string{machine, row.SessionID}]
		if !ok {
			continue
		}
		row.IsSubagent = true
		if row.ParentSessionID == "" {
			row.ParentSessionID = link.ParentSession
		}
		parentMachine := ""
		if link.ParentMachine != "" && link.ParentMachine != machine {
			parentMachine = link.ParentMachine
		}
		row.SetSubagentProvenance(
			cmp.Or(row.ParentMachineID(), parentMachine),
			cmp.Or(row.A2ATaskID(), link.A2ATaskID),
			cmp.Or(row.SubagentOrigin(), link.Origin),
		)
	}
	f.subagentsMu.RUnlock()
}

// pruneSubagentChecked drops negative lookup entries for sessions no longer in machine's final
// complete list (including archived rows and archive-only tail records) while retaining known
// positive subagent hits. Entries are partitioned per machine so pruning scans only machine's
// own bucket, never the fleet.
func (s *Service) pruneSubagentChecked(machine string, list []proto.Session, archived archivedPart) {
	f := &s.fleet
	f.subagentsMu.RLock()
	n := len(f.subagentsChecked[machine])
	f.subagentsMu.RUnlock()
	if n == 0 {
		return
	}
	listed := make(map[string]struct{}, len(list)+len(archived.rows))
	for i := range list {
		if sid := list[i].SessionID; sid != "" {
			listed[sid] = struct{}{}
		}
	}
	for i := range archived.rows {
		if sid := archived.rows[i].SessionID; sid != "" {
			listed[sid] = struct{}{}
		}
	}
	// Query archived.records directly via the zero-allocation lookupRuntimeListID path so
	// Hello.ArchivedApart publishes never copy or iterate thousands of archive-only tail keys.
	checkArchiveRecords := len(archived.keys) > 0 && len(archived.records) > 0
	f.subagentsMu.Lock()
	if bucket := f.subagentsChecked[machine]; len(bucket) > 0 {
		for sid := range bucket {
			if _, ok := listed[sid]; ok {
				continue
			}
			if checkArchiveRecords {
				if rec, ok := lookupRuntimeListID(archived.records, machine, sid); ok && rec.Archived && rec.Session.MachineID == machine && rec.Session.SessionID == sid {
					continue
				}
			}
			if _, isHit := f.subagents[[2]string{machine, sid}]; !isHit {
				delete(bucket, sid)
			}
		}
		if len(bucket) == 0 {
			delete(f.subagentsChecked, machine)
		}
	}
	f.subagentsMu.Unlock()
}
