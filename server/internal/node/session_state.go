package node

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/proto"
)

// stateFile remembers which sessions Persona started and with which launcher. It lives in the hub's Postgres:
// loaded with `state.load` when the node starts and written through with `state.put` events. A session in Sessions
// is Persona's (takesReport, close and decorate's StartedByPersona go by that): what the node only observes of the
// user's own sessions never goes in (ultracode.go keeps that in memory, #5510).
type stateFile struct {
	// persistMu orders each snapshot and Sessions mutation with its persist callback. Acquire it before mu and release
	// it only after persist returns; neither n.mu nor mu is held across persist. Node recovery/ultracode writers take
	// their recoveryPersistMu / ultracodeState.persistMu before this lock (releasing n.mu after their own snapshots),
	// and switchShare.drain releases switchShare.mu before adoptSwitch takes it.
	persistMu sync.Mutex
	mu        sync.Mutex
	Sessions  map[string]sessionState `json:"sessions"`
	persist   func(id string, v sessionState)
	// loading: the hub's copy has yet to come (Serve opens it before loadState runs, which closes it). Until then a
	// session missing from Sessions may still be Persona's. Nil once loaded.
	loading chan struct{}
}

type sessionState struct {
	ModelSource modelhttp.Source `json:"model_source,omitzero"`

	SegmentRetired          bool            `json:"segment_retired,omitempty"`
	SegmentOperation        string          `json:"segment_operation,omitempty"`
	SegmentInputs           []string        `json:"segment_inputs,omitempty"`
	TaskInstructions        string          `json:"task_instructions,omitempty"`
	TaskInstructionFile     string          `json:"task_instruction_file,omitempty"`
	TaskInstructionChannel  string          `json:"task_instruction_channel,omitempty"`
	CreationID              string          `json:"creation_id,omitempty"`
	Incarnation             string          `json:"incarnation,omitempty"`
	Pane                    string          `json:"pane,omitempty"`
	Launcher                string          `json:"launcher"`
	Model                   string          `json:"model,omitempty"`
	ModelFingerprint        string          `json:"model_fingerprint,omitempty"`
	ModelEffort             string          `json:"model_effort,omitempty"`
	CodexStartTier          string          `json:"codex_start_tier,omitempty"`
	ModelFromRecovery       bool            `json:"model_from_recovery,omitempty"` // Model came from automatic recovery, not an explicit pick (#15622)
	TaskExecution           bool            `json:"task_execution,omitempty"`      // the last Assistant input used strict scheduled execution
	RuntimeAssistant        bool            `json:"runtime_assistant,omitempty"`   // the node started or addressed it as an assistant conversation
	Origin                  string          `json:"origin,omitempty"`              // manager-recorded conversation source, for model-call authorization
	OriginKnown             bool            `json:"origin_known,omitempty"`        // manager explicitly decided origin; false on pre-upgrade records
	ConfigDir               string          `json:"config_dir,omitempty"`
	Cwd                     string          `json:"cwd"`
	TaskID                  string          `json:"task_id,omitempty"`
	WorkspaceID             string          `json:"workspace_id,omitempty"`
	WorkspaceHost           string          `json:"workspace_host,omitempty"`
	WorkspaceLabel          string          `json:"workspace_label,omitempty"`
	IsMain                  bool            `json:"is_main,omitempty"`
	SupervisionInstructions string          `json:"supervision_instructions,omitempty"`
	StartedAt               time.Time       `json:"started_at"`
	StopCause               proto.StopCause `json:"stop_cause,omitempty"`
	StopCauseAt             int64           `json:"stop_cause_at,omitempty"`

	// OrcaTerminal records the original terminal receipt for a Persona-opened Agent tab.
	OrcaTerminal *orcaTerminalOwner `json:"orca_terminal,omitempty"`
	// RanAt: when a node last saw the Agent run (unix seconds, written at most every ranAtEvery). A session whose agent
	// ran until shortly before this node started and is gone now was stopped by the restart (interrupted.go, #11441).
	RanAt int64 `json:"ran_at,omitempty"`
	// AwaitingFirstMessage: an empty start reserved this conversation without sending
	// a model input. Runtime instructions and the idle state wait for actual delivery.
	AwaitingFirstMessage bool `json:"awaiting_first_message,omitempty"`
	// MainCompactWindow: the window the main conversation or a leader conversation last ran with
	// (modelExecution.MainCompactWindow), kept like IsMain (the hub's copy keeps neither, so until the node restarts) for
	// a start that carries no execution: a manual compaction with no process running (assistant_compact.go).
	MainCompactWindow int `json:"main_compact_window,omitempty"`
	// AppMode / AppEffort: the permission mode and effort set on an assistant conversation (modes.go). AppMode
	// also retains a terminal Codex start constraint and an agy start's permission mode for later resumes and restarts.
	// They win over what its transcript recorded and outlive the node: an old "default" there no longer locks it in.
	// AppModeExplicit distinguishes a deliberate per-conversation mode from a legacy/default value; on Claude Code only
	// an explicit one outranks the manager's current default, while other agents always retain their legacy AppMode
	// precedence.
	// PlanThen: the mode this session, started in plan mode, goes on in once its plan is approved (planthen.go).
	PlanThen        string `json:"plan_then,omitempty"`
	AppMode         string `json:"app_mode,omitempty"`
	AppModeExplicit bool   `json:"app_mode_explicit,omitempty"`
	AppEffort       string `json:"app_effort,omitempty"`
	// DesktopPendingModel / DesktopPendingEffort: requested initial Codex App settings still awaiting the owning
	// window's readback. They survive partial creation/restarts and must be confirmed before its first message.
	DesktopPendingModel  string `json:"desktop_pending_model,omitempty"`
	DesktopPendingEffort string `json:"desktop_pending_effort,omitempty"`
	// Tasks: the assistant conversation's background tasks and monitors running right now (node/assistant.go).
	Tasks []string `json:"tasks,omitempty"`
	// Compaction: the latest reported manual or idle compaction (assistant_compact.go).
	Compaction *proto.Compaction `json:"compaction,omitempty"`
	// RecoveryPending survives restarts until an input carrying this checkpoint is delivered.
	RecoveryPending     string `json:"recovery_pending,omitempty"`
	RecoveryHookPending bool   `json:"recovery_hook_pending,omitempty"`
	CompactInstructions string `json:"compact_instructions,omitempty"`
	// IdleCompaction is the main chat's user-idle clock and unattended compaction latch.
	IdleCompaction *idleCompactionState `json:"idle_compaction,omitempty"`
	// Guest: a guest conversation's tools (assistant_guest.go): every process it runs in has no built-in tools and
	// only these of Persona's. Nil is an ordinary conversation.
	Guest []string `json:"guest,omitempty"`
	// GuestKind is a guest conversation's kind: "" for an extension's, guestMeeting for a meeting's room
	// (assistant_guest.go), whose every process loads none of the owner's Claude Code setup either.
	GuestKind string `json:"guest_kind,omitempty"`
	// Ultracode: what was last seen of ultracode in the process Persona runs this session in (ultracode.go). Written
	// by setUltracode only, which never makes a record for a session that has none.
	Ultracode *ultracodeRecord `json:"ultracode_process,omitempty"`
	// PendingSwitch: a switch to another launcher waiting for the next message (launcher_switch.go), kept with the
	// record so a node restart neither loses it nor carries it out without the message. Written by setPendingSwitch,
	// which never makes a record; cleared by the start that carries it out (which writes a new record).
	PendingSwitch *proto.PendingSwitch `json:"pending_switch,omitempty"`
	// ModelRecovery: the node's own bounded recovery has this protected conversation answering on another model, or
	// its chain was exhausted (#7824). Node-owned state, not a user preference: it never changes the configured model
	// and is cleared once the configured model runs again. Persisted so a node restart keeps the marker.
	ModelRecovery *proto.ModelRecovery `json:"model_recovery,omitempty"`
	// FollowsSetting marks a leader/supervisor conversation the manager sends with the main chat's model setting
	// (assistant/start.go). Together with IsMain it identifies the conversations the owner's recovery rule protects;
	// worker Agent conversations have neither flag.
	FollowsSetting bool `json:"follows_setting,omitempty"`
	// EngineHistory: the conversation's earlier stretches this session continues after a switch between Claude Code
	// and Codex (engine_switch.go, Session.engine_history), oldest first. Only the session running the conversation now
	// has it: the switch that moves the conversation on gives it to the next session and clears it here. A start that
	// resumes the session keeps it.
	EngineHistory []proto.EngineLeg `json:"engine_history,omitempty"`
	// PendingHandoff: the handoff of a switch to this session whose first message a dialog held back (engine_switch.go
	// startEngine): put in front of the next message sent to the session, then cleared (send.go).
	PendingHandoff *pendingHandoff `json:"pending_handoff,omitempty"`
	// EngineRuntime: the engine runtime (Paseo, Cindy) whose new start wrote this record (recordEngineStart). The
	// runtime keeps the Agent and none of its resumes writes the record again, so Cwd is where the Agent started; a
	// terminal start or resume writes a whole new record without it, and a launcher switch carries the switch's. An
	// extension's resume of a conversation its computer does not list passes on such a record alone, with no transcript
	// (resumeOutsideHealth, #10941).
	EngineRuntime string `json:"engine_runtime,omitempty"`
	// RemoteAgent latches true when the session has been associated with a remote assistant runtime
	// (caps.assistant && caps.remote_agent). Once latched, writes always require fresh confirmation across
	// disconnects, ownership changes, and restarts.
	RemoteAgent bool `json:"remote_agent,omitempty"`
	// RoundForkOf names the base background supervisor session when a supervision round's process forked a separate
	// Claude session id (assistant_round.go). Forked round branches inherit assistant identity and stay hidden from
	// the ordinary Agents list across node restarts.
	RoundForkOf string `json:"round_fork_of,omitempty"`
	// StartOpID is the X-Persona-Op-Id that started this session, kept so reconnect reconciliation after a mid-flight
	// disconnect or node restart can prove the start already ran (#10674).
	StartOpID string `json:"start_op_id,omitempty"`
	// StartTitle is the clipped first prompt of a Persona-started work agent pane (agytitle.go), shown before the
	// agent reports its conversation or generates its own title instead of the shell's agent-start-*.sh command.
	StartTitle string `json:"start_title,omitempty"`
}

func newState() *stateFile { return &stateFile{Sessions: map[string]sessionState{}} }

// legacyState reads the pre-Postgres ~/.persona/sessions.json, if any.
// A corrupt legacy file is quarantined to path.corrupt and logged.
func legacyState(path string, logf func(string, ...any)) map[string]sessionState {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return nil
	}
	var f struct {
		Sessions map[string]sessionState `json:"sessions"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		corruptPath := path + ".corrupt"
		_ = os.Rename(path, corruptPath)
		if logf != nil {
			logf("legacy sessions file %s is corrupt (quarantined to %s): %v", path, corruptPath, err)
		}
		return nil
	}
	return f.Sessions
}

// merge adds sessions loaded from the hub without overwriting ones touched since the node started.
func (s *stateFile) merge(m map[string]sessionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, v := range m {
		if observationOnly(v) {
			continue // not Persona's, and what it says of the user's session is not kept either (observationOnly)
		}
		if cur, ok := s.Sessions[id]; !ok {
			if len(v.SegmentInputs) > 0 {
				v.SegmentInputs = mergeSegmentInputs(nil, v.SegmentInputs)
			}
			s.Sessions[id] = v
		} else if len(v.SegmentInputs) > 0 {
			cur.SegmentInputs = mergeSegmentInputs(v.SegmentInputs, cur.SegmentInputs)
			s.Sessions[id] = cur
		}
	}
}

// observationOnly: a record holding nothing but an ultracode observation, as a node of #3782 wrote for a session the
// user started. Persona's own records always have a directory and a launcher (checkStart fills both in), and the hub
// gives back a zero start time only for a record stored without one next to an ultracode observation
// (db.PutNodeSession's started_at_unknown). Such a record is dropped when loaded, observation and all: nothing would
// ever write it again (the user's sessions get no record), so a resume notice dismissed now, or a value of a process
// long gone, would come back from it with every node restart. What the node sees of the session from then on stays in
// memory, as for any session the user started.
func observationOnly(v sessionState) bool {
	return v.Ultracode != nil && v.StartedAt.IsZero() && v.Launcher == "" && v.Cwd == ""
}

// beginLoad and endLoad bracket loading the hub's copy (loading).
func (s *stateFile) beginLoad() {
	s.mu.Lock()
	if s.loading == nil {
		s.loading = make(chan struct{})
	}
	s.mu.Unlock()
}

func (s *stateFile) endLoad() {
	s.mu.Lock()
	if s.loading != nil {
		close(s.loading)
		s.loading = nil
	}
	s.mu.Unlock()
}

func (s *stateFile) isLoading() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loading != nil
}

// loaded waits until the hub's copy is loaded (or failed to load: loadState gives up after 20 seconds).
func (s *stateFile) loaded(ctx context.Context) error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	ch := s.loading
	s.mu.Unlock()
	if ch == nil {
		return nil
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ultracode is the stored ultracode observation of a session Persona started.
func (s *stateFile) ultracode(id string) (ultracodeRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.Sessions[id]
	if !ok || v.Ultracode == nil {
		return ultracodeRecord{}, false
	}
	return *v.Ultracode, true
}

// setUltracode changes the ultracode observation of a session Persona started and nothing else of its record, under
// the lock, so a copy read before another writer's change is never written back over it. A session without a record
// is not Persona's, or not loaded yet: nothing is written for it (false).
func (s *stateFile) setUltracode(id string, rec ultracodeRecord) bool {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	v, ok := s.Sessions[id]
	if !ok {
		s.mu.Unlock()
		return false
	}
	v.Ultracode = &rec
	s.Sessions[id] = v
	persist := s.persist
	s.mu.Unlock()
	if persist != nil {
		persist(id, v)
	}
	return true
}

// setPendingSwitch changes the pending launcher switch of a session Persona started (nil: none) and nothing else of its
// record, under the lock, as setUltracode does; false when the session has no record (not Persona's, or not loaded
// yet): nothing is written for it.
func (s *stateFile) setPendingSwitch(id string, ps *proto.PendingSwitch) bool {
	return s.update(id, func(v *sessionState) bool {
		if v.PendingSwitch == nil && ps == nil {
			return false
		}
		if ps != nil {
			c := *ps
			ps = &c
		}
		v.PendingSwitch = ps
		return true
	})
}

// restoreLaunch puts back what a start that failed to carry out a launcher switch wrote over (start writes a whole new
// record before the agent is up): the launcher, model, directory (and whose start it is), start time and plan mode of
// prev, the record as it was, with the rest as it is now (an ultracode observation made since stays), and no pending
// switch: a failed switch is dropped (launcher_switch.go). launcher is the one to put back when there was no record
// before (the start made one).
func (s *stateFile) restoreLaunch(id string, prev sessionState, had bool, launcher string) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	v, ok := s.Sessions[id]
	if !ok {
		s.mu.Unlock()
		return
	}
	if had {
		v.Launcher, v.Model, v.ModelFingerprint, v.ModelEffort = prev.Launcher, prev.Model, prev.ModelFingerprint, prev.ModelEffort
		v.Cwd, v.StartedAt, v.PlanThen, v.EngineRuntime = prev.Cwd, prev.StartedAt, prev.PlanThen, prev.EngineRuntime
	} else {
		v.Launcher, v.Model, v.ModelFingerprint = launcher, "", ""
	}
	v.PendingSwitch = nil
	s.Sessions[id] = v
	persist := s.persist
	s.mu.Unlock()
	if persist != nil {
		persist(id, v)
	}
}

// adoptSwitch takes v, what a launcher switch carried out on another Node wrote of session id (switch_share.go), and
// writes it through this Node's hub: the fields the switch owns (launcher, model, directory and the engine runtime whose
// start it is, start time, plan mode, pane, pending switch, the conversation's earlier stretches and a handoff waiting
// for the next message) over this Node's record, the rest of it as this Node has it (an ultracode observation the new
// claude's hooks made on this connection); v whole when there is no record.
func (s *stateFile) adoptSwitch(id string, v sessionState) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	if cur, ok := s.Sessions[id]; ok {
		cur.ModelSource = v.ModelSource
		cur.Launcher, cur.Model, cur.ModelFingerprint, cur.ModelEffort = v.Launcher, v.Model, v.ModelFingerprint, v.ModelEffort
		cur.Cwd, cur.StartedAt, cur.PlanThen, cur.Pane, cur.PendingSwitch = v.Cwd, v.StartedAt, v.PlanThen, v.Pane, v.PendingSwitch
		cur.EngineHistory, cur.PendingHandoff = v.EngineHistory, v.PendingHandoff // a switch to another engine (engine_switch.go)
		cur.EngineRuntime = v.EngineRuntime
		cur.RemoteAgent = cur.RemoteAgent || v.RemoteAgent
		if len(v.SegmentInputs) > 0 {
			cur.SegmentInputs = mergeSegmentInputs(cur.SegmentInputs, v.SegmentInputs)
		}
		v = cur
	}
	s.Sessions[id] = v
	persist := s.persist
	s.mu.Unlock()
	if persist != nil {
		persist(id, v)
	}
}

// update changes session id's record with change under the lock and writes it through when change says it changed;
// false when the session has no record: nothing is written for it.
func (s *stateFile) update(id string, change func(v *sessionState) bool) bool {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	v, ok := s.Sessions[id]
	if !ok {
		s.mu.Unlock()
		return false
	}
	if !change(&v) {
		s.mu.Unlock()
		return true
	}
	s.Sessions[id] = v
	persist := s.persist
	s.mu.Unlock()
	if persist != nil {
		persist(id, v)
	}
	return true
}

// putBack puts session id's record back as it was (prev) before a start that did not come up wrote over it: a switch
// to the other engine resuming the conversation's earlier session (engine_switch.go). Without a record before (had
// false), the one the start made keeps no history and no switch: it names nothing the list would leave out.
func (s *stateFile) putBack(id string, prev sessionState, had bool) {
	if had {
		s.put(id, prev)
		return
	}
	s.update(id, func(v *sessionState) bool {
		if v.EngineHistory == nil && v.PendingSwitch == nil {
			return false
		}
		v.EngineHistory, v.PendingSwitch = nil, nil
		return true
	})
}

// setEngineHistory sets the conversation's earlier stretches on session id's record (nil: none), when it has one.
func (s *stateFile) setEngineHistory(id string, legs []proto.EngineLeg) {
	s.update(id, func(v *sessionState) bool {
		if proto.SameEngineHistory(v.EngineHistory, legs) {
			return false
		}
		v.EngineHistory = slices.Clone(legs)
		return true
	})
}

// engineHistories are the records that carry earlier stretches, by session id (nil when none does): one look for a
// whole session list (sessions.go).
func (s *stateFile) engineHistories() map[string][]proto.EngineLeg {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out map[string][]proto.EngineLeg
	for id, v := range s.Sessions {
		if len(v.EngineHistory) == 0 {
			continue
		}
		if out == nil {
			out = map[string][]proto.EngineLeg{}
		}
		out[id] = v.EngineHistory
	}
	return out
}

func (s *stateFile) get(id string) (sessionState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.Sessions[id]
	return v, ok
}

// byStartOp finds the session started with opID, if any (#10674).
func (s *stateFile) byStartOp(opID string) (string, sessionState, bool) {
	if s == nil || opID == "" {
		return "", sessionState{}, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	var foundID string
	var found sessionState
	for id, v := range s.Sessions {
		if v.StartOpID != opID {
			continue
		}
		if foundID == "" || id < foundID {
			foundID, found = id, v
		}
	}
	return foundID, found, foundID != ""
}

// panes returns the remembered pane of every session (herdr pane ids are not reused while the server lives).
func (s *stateFile) panes() map[string]string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]string{}
	for id, v := range s.Sessions {
		if v.Pane != "" {
			out[v.Pane] = id
		}
	}
	return out
}

func (s *stateFile) setPane(id, pane string) {
	s.update(id, func(v *sessionState) bool {
		if v.Pane == pane {
			return false
		}
		v.Pane = pane
		v.OrcaTerminal = nil
		return true
	})
}

// forgetPane takes pane off every record that names it (Node.forgetPane).
func (s *stateFile) forgetPane(pane string) {
	s.mu.Lock()
	var ids []string
	for id, v := range s.Sessions {
		if v.Pane == pane {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	for _, id := range ids {
		s.update(id, func(v *sessionState) bool {
			if v.Pane != pane {
				return false // bound to another pane since
			}
			v.Pane = ""
			v.OrcaTerminal = nil
			return true
		})
	}
}

// put reports only whether its persistence callback was invoked, not whether the
// one-way notification reached durable storage.
func (s *stateFile) put(id string, v sessionState) bool {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.mu.Lock()
	if old, ok := s.Sessions[id]; ok {
		v.SegmentRetired = old.SegmentRetired
		if old.SegmentOperation != "" {
			v.SegmentOperation = old.SegmentOperation
		}
		if len(old.SegmentInputs) > 0 {
			v.SegmentInputs = old.SegmentInputs
		} else if len(v.SegmentInputs) > 0 {
			v.SegmentInputs = mergeSegmentInputs(nil, v.SegmentInputs)
		}
		// Owned by state.update: older process snapshots cannot replace the
		// idle clock or the latest compaction result.
		v.IdleCompaction, v.Compaction = old.IdleCompaction, old.Compaction
		if v.OrcaTerminal == nil && v.Pane == old.Pane {
			v.OrcaTerminal = old.OrcaTerminal
		}
		if old.CreationID != "" {
			v.CreationID, v.Incarnation = old.CreationID, old.Incarnation
		}
		// A send can save its metadata after an interrupt recorded a newer Stop.
		if v.StopCauseAt < old.StopCauseAt {
			v.StopCause, v.StopCauseAt = old.StopCause, old.StopCauseAt
		}
		if v.RanAt == 0 {
			v.RanAt = old.RanAt // owned by recordRunning
		}
		if v.StartOpID == "" && old.StartOpID != "" {
			v.StartOpID = old.StartOpID
		}
		if old.RemoteAgent {
			v.RemoteAgent = true // latch-only security posture: never cleared by later writes
		}
	} else if len(v.SegmentInputs) > 0 {
		v.SegmentInputs = mergeSegmentInputs(nil, v.SegmentInputs)
	}
	s.Sessions[id] = v
	persist := s.persist
	s.mu.Unlock()
	if persist != nil {
		persist(id, v)
		return true
	}
	return false
}
