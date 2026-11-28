package proto

import (
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/agentviewwire"
)

// IsCodexSubagent identifies delegated tasks from source metadata, even when
// their parent is not in the current session list. Titles never imply ancestry.
func (s Session) IsCodexSubagent() bool {
	if s.Agent != "codex" && !strings.HasPrefix(s.SourceKind, "codex_") {
		return false
	}
	return s.IsSubagent || s.ParentSessionID != "" || s.ParentThreadID != "" ||
		(s.RootSessionID != "" && s.RootSessionID != s.SessionID)
}

// IsSubtask says whether a session is listed and counted under the session that started it rather than on its own:
// a Codex subagent, or a runtime's own session that names its parent, whichever agent it runs (SourceRuntime: an
// agent another Paseo agent created, #4494). Unlike a Codex subagent, which its parent drives, such a runtime session
// can need the user. While it does it is no subtask: every place that counts sessions has it, and none folds it
// away. (The node lists it without its parent then; a card can still reach the hub before that row does.)
func (s Session) IsSubtask() bool {
	if s.SourceKind == SourceRuntime {
		return (s.IsSubagent || s.ParentSessionID != "") && s.State != StateNeedsYou
	}
	return s.IsCodexSubagent()
}

// SessionHierarchy is a Session's place among the sessions that started one another (docs/protocol/protocol.md
// "Session hierarchy"), when it began, its completed task ownership, recent command outcomes, and rare lifecycle
// metadata. Each member is a top-level wire key. A Session holds it by pointer (Session.Hierarchy, nil when every
// member is empty): these sparse fields inline would take every row past its size class (TestSessionFitsItsSizeClass).
// The Deleted flag describes no ancestry; it shares this storage to preserve the allocation budget for ordinary rows.
type SessionHierarchy struct {
	// HiddenByDirectory is manager-owned visibility, not collection or archive state.
	HiddenByDirectory bool `json:"hidden_by_directory,omitempty"`
	// CompletedTodo is display-only task ownership. Keep this sparse reference out of every Session's hot row.
	CompletedTodo *TodoRef `json:"completed_todo,omitempty"`
	// ParentAgentID is the session on the same machine a side session was started from (docs/protocol/protocol.md
	// "Session hierarchy"). Unlike parent_session_id it makes no child: the session keeps its own state, sends and
	// unread, and is no subagent. The manager folds it under that session's topmost listed row as a side session
	// (FoldedAsSide, #7944). Set by the node: a Claude desktop app's side session names the session it was started from,
	// while that one is listed too (claudedesktop.go, #5935).
	ParentAgentID string `json:"parent_agent_id,omitempty"`
	// StartedAt is when the session began: its transcript's first line, a runtime's created time. Set by the node where
	// it knows it (Codex rollouts, runtime rows); zero when it does not.
	StartedAt time.Time `json:"started_at,omitzero"`
	// CreationID correlates an assistant-dispatched start with its pre-created to-do, including when the start's answer is
	// lost. Empty for manually opened and older sessions (#5732). Set by the node from its persisted creation record.
	CreationID string `json:"creation_id,omitempty"`
	// AskingParentSessionID names the parent of a runtime child that waits for the user (status blocked). Such a row
	// has is_subagent, parent_session_id and root_session_id cleared, so that apps which fold every child under its
	// parent still list it while it needs the user (#4494); this keeps its parent known to those that fold it as a card
	// in the parent's chat. Set by the node.
	AskingParentSessionID string `json:"asking_parent_session_id,omitempty"`
	// FoldedUnder is the listed session this child folds under: its topmost listed ancestor (on the same machine when
	// FoldedUnderMachineID is empty, or on FoldedUnderMachineID when cross-machine). Set by the manager (fleet_fold.go);
	// empty for a session that is not a child, an orphan and a member of a cycle.
	FoldedUnder string `json:"folded_under,omitempty"`
	// FoldedUnderMachineID is the machine of FoldedUnder when the parent lives on another computer; empty when on the
	// same machine as the session itself. Set by the manager beside FoldedUnder.
	FoldedUnderMachineID string `json:"folded_under_machine_id,omitempty"`
	// FoldedAs says why a row that is no child folds under FoldedUnder: FoldedAsWorker for a worker of a tab's lead
	// (#5180), FoldedAsSide for a side session (#7944). Set by the manager beside FoldedUnder; empty for a child, whose
	// own fields say why.
	FoldedAs string `json:"folded_as,omitempty"`
	// FoldedSide is the nearest side session a folded row belongs to when it folds through one and is not itself that
	// side (a side's own Codex children): its card and child card belong to that side's chat, not FoldedUnder's (#7944).
	// Set by the manager beside FoldedUnder.
	FoldedSide string `json:"folded_side,omitempty"`
	// ParentMachineID is the machine of ParentSessionID when the parent lives on another computer; empty when on the
	// same machine as the session itself. Set by the manager for cross-machine subagents (e.g. A2A / dispatch).
	ParentMachineID string `json:"parent_machine_id,omitempty"`
	// A2ATaskID is the durable A2A task that launched this subagent session, when started via A2A or A2A-backed dispatch.
	// Empty for ordinary A2A peer messages to existing sessions and non-A2A subagents. Set by the manager.
	A2ATaskID string `json:"a2a_task_id,omitempty"`
	// SubagentOrigin classifies how a subagent session was created: SubagentOriginNative, SubagentOriginA2A,
	// SubagentOriginDispatch, SubagentOriginRuntime or SubagentOriginSpawned. Set by the manager.
	SubagentOrigin string `json:"subagent_origin,omitempty"`
	// Children counts the sessions folded under this one, all depths, by their own states. Set by the manager on the
	// topmost listed ancestor only.
	Children *SessionChildren `json:"children,omitempty"`
	// EngineHistory is the conversation's earlier stretches, oldest first, when it moved between Claude Code and Codex
	// (#5054, docs/protocol/protocol.md "Switching a running Agent to another engine"): the session that ran each and
	// when it was switched away from. Only the session running the conversation now carries it; the sessions it names
	// are left out of the machine's list (one conversation, one row), and their transcripts stay readable and resumable.
	// A session may be named more than once (switched back to and away again), each time for another stretch. Set by
	// the node.
	EngineHistory []EngineLeg `json:"engine_history,omitempty"`
	// Aliases are the other session ids its machine knows this row's conversation by, none of which it lists beside it
	// (one conversation, one row): the Claude session holding the conversation of a runtime's session listed under an
	// id of its own, and the sessions that one was forked from, newest first (docs/integrations/paseo.md "Session
	// IDs", #8131); the transcripts a Claude desktop app session went on from, newest first, on the row of the one it
	// runs now (#14394). The manager shows no archive record under one of them: the conversation was listed under it
	// once, and its archived row repeated the row listed now. Set by the node.
	Aliases []string `json:"aliases,omitempty"`
	// BackgroundEnded retains recent shell / Monitor outcomes separately from Session.Background, so older clients keep
	// counting only running work. Oldest first, at most eight; the node lists entries from the last day. Sparse storage
	// keeps ordinary Session rows in their allocation size class.
	BackgroundEnded []BackgroundTask `json:"background_ended,omitempty"`
	// ContextBreakdown splits current context window occupancy by category with an exact/estimated honesty marker.
	// Stored here so ordinary Session rows stay inside their 1,536-byte allocation size class; encoded as a top-level
	// "context_breakdown" wire key.
	ContextBreakdown *ContextBreakdown `json:"context_breakdown,omitempty"`
	Deleted          bool              `json:"deleted,omitempty"` // Permanent manager tombstone; use Session.IsDeleted/SetDeleted.
	// OutputTokens is cumulative generated output, not the latest context size or transcript revision.
	// Stored here so ordinary Session rows stay inside their 1,536-byte allocation size class.
	OutputTokens int32 `json:"output_tokens,omitempty"`
	// View is the session as every client draws it: one of five phases, why, whether its row counts in the Agents
	// list's Needs you, and its rank (docs/protocol/agent-states.md "Five phases", #15397), encoded as the top-level
	// "view" key. The manager computes it on the lists it publishes (assistant/agentview); state and status stay as
	// they are for older clients. Here rather than on Session, which has no room left (TestSessionFitsItsSizeClass).
	// Use Session.View / SetView.
	View *agentviewwire.AgentView `json:"view,omitempty"`
	// BotmuxExternalTrigger: on a botmux session whose botmux instance was probed and does not advertise ownerActor
	// (an older botmux without GET /api/sessions/:id/turns or without actor:"owner"), sends go through the untrusted
	// external-event trigger; clients show a one-line notice in the composer. Stored here so ordinary Session rows
	// stay inside their 1,536-byte allocation size class; encoded as top-level "botmux_external_trigger".
	BotmuxExternalTrigger bool `json:"botmux_external_trigger,omitempty"`
	// BotmuxExternalTriggerReason says why, when BotmuxExternalTrigger is set: botmux needs an upgrade, or no BotMux
	// Owner Union ID is set (proto.BotmuxTriggerOwnerUnset). Empty from an older node; encoded as top-level
	// "botmux_external_trigger_reason".
	BotmuxExternalTriggerReason BotmuxExternalTriggerReason `json:"botmux_external_trigger_reason,omitempty"`
}

// View is the session's five-phase view, nil from a hub that does not send one (FeatureAgentView) and on a row the
// manager did not compute it for. Shared with the row's other copies: not to be modified in place.
func (s *Session) View() *agentviewwire.AgentView {
	if s.Hierarchy == nil {
		return nil
	}
	return s.Hierarchy.View
}

// SetView replaces the session's view without changing other copies of the row; nil takes it away. The view is
// shared from then on: the caller does not change it after.
func (s *Session) SetView(v *agentviewwire.AgentView) {
	if v == nil && s.View() == nil {
		return
	}
	h := s.hierarchy()
	h.View = v
	s.setHierarchy(h)
}

// ContextBreakdown returns the session's structured context window breakdown, or nil when unavailable.
// Shared with the row's other copies: not to be modified in place.
func (s *Session) ContextBreakdown() *ContextBreakdown {
	if s.Hierarchy == nil {
		return nil
	}
	return s.Hierarchy.ContextBreakdown
}

// SetContextBreakdown replaces the session's context breakdown without changing other copies of the row.
func (s *Session) SetContextBreakdown(b *ContextBreakdown) {
	if s.ContextBreakdown().Equal(b) {
		return
	}
	h := s.hierarchy()
	h.ContextBreakdown = b.Clone()
	s.setHierarchy(h)
}

// BackgroundEnded returns recent shell / Monitor outcomes, oldest first; nil when none are retained.
// Shared with the row's other copies: not to be changed.
func (s *Session) BackgroundEnded() []BackgroundTask {
	if s.Hierarchy == nil {
		return nil
	}
	return s.Hierarchy.BackgroundEnded
}

// SetBackgroundEnded replaces the retained command outcomes without changing other copies of the row.
func (s *Session) SetBackgroundEnded(tasks []BackgroundTask) {
	if len(tasks) == 0 && len(s.BackgroundEnded()) == 0 {
		return
	}
	h := s.hierarchy()
	h.BackgroundEnded = slices.Clone(tasks)
	s.setHierarchy(h)
}

// EngineLeg is one earlier stretch of a conversation (SessionHierarchy.EngineHistory): the session that ran it, its
// engine ("claude" or "codex"), and when the conversation was switched away from it. The stretch began when the one
// before it ended (the first: when its session began).
type EngineLeg struct {
	SessionID string    `json:"session_id"`
	Agent     string    `json:"agent"`
	Until     time.Time `json:"until"`
}

// SameEngineHistory: a and b name the same stretches.
func SameEngineHistory(a, b []EngineLeg) bool {
	return slices.EqualFunc(a, b, func(x, y EngineLeg) bool {
		return x.SessionID == y.SessionID && x.Agent == y.Agent && x.Until.Equal(y.Until)
	})
}

// FoldedAsWorker is SessionHierarchy.FoldedAs for a worker of a tab team: another running Agent in the tab whose first
// pane runs the lead it folds under (docs/protocol/protocol.md "Session hierarchy", #5180).
const FoldedAsWorker = "worker"

// FoldedAsSide is SessionHierarchy.FoldedAs for a side session (a Claude desktop app's side chat, /btw question or
// spawned session, parent_agent_id): its own step up is a side link, and it lives in the chat of the row it folds under
// (docs/protocol/protocol.md "Session hierarchy", #7944).
const FoldedAsSide = "side"

// SubagentOrigin values for SessionHierarchy.SubagentOrigin.
const (
	SubagentOriginNative   = "native"
	SubagentOriginA2A      = "a2a"
	SubagentOriginDispatch = "dispatch"
	SubagentOriginRuntime  = "runtime"
	SubagentOriginSpawned  = "spawned"
)

// SessionChildren is the rollup a parent row carries of the children folded under it (SessionHierarchy.Children).
type SessionChildren struct {
	Total    int `json:"total"`
	Working  int `json:"working,omitempty"`
	NeedsYou int `json:"needs_you,omitempty"`
	// Workers is how many of them are the row's tab workers (folded_as "worker"), counted in Total too.
	Workers int `json:"workers,omitempty"`
	// Sides is how many of them are side sessions (folded_as "side"), counted in Total too; SidesWorking,
	// SidesNeedsYou and SidesUnread count those sides by their own states (#7944).
	Sides         int `json:"sides,omitempty"`
	SidesWorking  int `json:"sides_working,omitempty"`
	SidesNeedsYou int `json:"sides_needs_you,omitempty"`
	SidesUnread   int `json:"sides_unread,omitempty"`
}

func (h *SessionHierarchy) empty() bool {
	return !h.HiddenByDirectory && h.ParentAgentID == "" && h.StartedAt.IsZero() && h.CreationID == "" && h.AskingParentSessionID == "" &&
		h.FoldedUnder == "" && h.FoldedUnderMachineID == "" && h.FoldedAs == "" && h.FoldedSide == "" &&
		h.ParentMachineID == "" && h.A2ATaskID == "" && h.SubagentOrigin == "" &&
		h.Children == nil && len(h.EngineHistory) == 0 && h.CompletedTodo == nil && len(h.Aliases) == 0 &&
		len(h.BackgroundEnded) == 0 && h.ContextBreakdown == nil && h.OutputTokens == 0 && !h.Deleted && h.View == nil && !h.BotmuxExternalTrigger &&
		h.BotmuxExternalTriggerReason == ""
}

// OutputTokensValue is the cumulative generated output recorded for the row.
func (s Session) OutputTokensValue() int32 {
	if s.Hierarchy == nil {
		return 0
	}
	return s.Hierarchy.OutputTokens
}

// SetOutputTokens records cumulative generated output without growing the ordinary Session row.
func (s *Session) SetOutputTokens(n int) {
	if s.OutputTokensValue() == Tokens32(n) {
		return
	}
	h := s.hierarchy()
	h.OutputTokens = Tokens32(n)
	s.setHierarchy(h)
}

// BotmuxExternalTrigger reports whether this botmux session's sends fall back to the untrusted external-event trigger.
func (s *Session) BotmuxExternalTrigger() bool {
	return s.Hierarchy != nil && s.Hierarchy.BotmuxExternalTrigger
}

// BotmuxExternalTriggerReason says why this botmux session's sends fall back to the external-event trigger, "" when
// they do not or the node did not say.
func (s *Session) BotmuxExternalTriggerReason() BotmuxExternalTriggerReason {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.BotmuxExternalTriggerReason
}

// SetBotmuxExternalTrigger updates the sparse external-trigger flag without mutating shared copies of the row. A
// reason other than "" sets the flag and says why; "" clears both.
func (s *Session) SetBotmuxExternalTrigger(reason BotmuxExternalTriggerReason) {
	if s.BotmuxExternalTrigger() == (reason != "") && s.BotmuxExternalTriggerReason() == reason {
		return
	}
	h := s.hierarchy()
	h.BotmuxExternalTrigger, h.BotmuxExternalTriggerReason = reason != "", reason
	s.setHierarchy(h)
}

// HiddenByDirectory reports the manager's directory visibility without growing every session row.
func (s *Session) HiddenByDirectory() bool {
	return s.Hierarchy != nil && s.Hierarchy.HiddenByDirectory
}

// SetHiddenByDirectory preserves sparse metadata shared by session copies.
func (s *Session) SetHiddenByDirectory(hidden bool) {
	if s.HiddenByDirectory() == hidden {
		return
	}
	h := s.hierarchy()
	h.HiddenByDirectory = hidden
	s.setHierarchy(h)
}

// CompletedTodo is the last completed association for navigation, never active work.
func (s *Session) CompletedTodo() *TodoRef {
	if s.Hierarchy == nil {
		return nil
	}
	return s.Hierarchy.CompletedTodo
}

// SetCompletedTodo preserves the copy-on-write sparse metadata shared by session copies.
func (s *Session) SetCompletedTodo(todo *TodoRef) {
	if s.CompletedTodo() == todo {
		return
	}
	h := s.hierarchy()
	h.CompletedTodo = todo
	s.setHierarchy(h)
}

// hierarchy is a copy of s's hierarchy to change, its children copied too: the one s holds is shared.
func (s *Session) hierarchy() SessionHierarchy {
	if s.Hierarchy == nil {
		return SessionHierarchy{}
	}
	h := *s.Hierarchy
	if h.Children != nil {
		c := *h.Children
		h.Children = &c
	}
	h.EngineHistory = slices.Clone(h.EngineHistory)
	h.Aliases = slices.Clone(h.Aliases)
	h.BackgroundEnded = slices.Clone(h.BackgroundEnded)
	h.ContextBreakdown = h.ContextBreakdown.Clone()
	return h
}

// setHierarchy makes h the session's, nil when it is empty.
func (s *Session) setHierarchy(h SessionHierarchy) {
	if h.empty() {
		s.Hierarchy = nil
		return
	}
	s.Hierarchy = &h
}

// IsDeleted reports the permanent manager tombstone. Ordinary rows allocate no lifecycle metadata.
func (s *Session) IsDeleted() bool { return s.Hierarchy != nil && s.Hierarchy.Deleted }

// SetDeleted uses copy-on-write, like the other rare fields: copies of a
// Session share Hierarchy, so changing one must never change another.
func (s *Session) SetDeleted(deleted bool) {
	if s.IsDeleted() == deleted {
		return
	}
	h := s.hierarchy()
	h.Deleted = deleted
	s.setHierarchy(h)
}

// ParentAgentID is the session this one is drawn under while staying its own (SessionHierarchy.ParentAgentID); "" when
// none.
func (s *Session) ParentAgentID() string {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.ParentAgentID
}

// SetParentAgentID sets the session this one is drawn under ("" for none), with no new hierarchy when it already is.
func (s *Session) SetParentAgentID(id string) {
	if s.ParentAgentID() == id {
		return
	}
	h := s.hierarchy()
	h.ParentAgentID = id
	s.setHierarchy(h)
}

// StartedAt is when the session began (SessionHierarchy.StartedAt); zero when not known.
func (s *Session) StartedAt() time.Time {
	if s.Hierarchy == nil {
		return time.Time{}
	}
	return s.Hierarchy.StartedAt
}

// CreationID is the identity the node recorded for an assistant-dispatched start; "" for any other session.
func (s *Session) CreationID() string {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.CreationID
}

// SetCreationID records an assistant-dispatched start's identity ("" when there is none).
func (s *Session) SetCreationID(id string) {
	if s.CreationID() == id {
		return
	}
	h := s.hierarchy()
	h.CreationID = id
	s.setHierarchy(h)
}

// AskingParentSessionID is the parent of a blocked runtime child (SessionHierarchy.AskingParentSessionID).
func (s *Session) AskingParentSessionID() string {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.AskingParentSessionID
}

// FoldedUnder is the listed session this one folds under (SessionHierarchy.FoldedUnder); "" when none.
func (s *Session) FoldedUnder() string {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.FoldedUnder
}

// FoldedUnderMachineID is the machine of FoldedUnder when cross-machine (SessionHierarchy.FoldedUnderMachineID); "" when same machine or none.
func (s *Session) FoldedUnderMachineID() string {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.FoldedUnderMachineID
}

// ParentMachineID is the machine of ParentSessionID when cross-machine (SessionHierarchy.ParentMachineID); "" when same machine or none.
func (s *Session) ParentMachineID() string {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.ParentMachineID
}

// SetParentMachineID sets the parent's machine ID ("" for same machine / none).
func (s *Session) SetParentMachineID(id string) {
	if s.ParentMachineID() == id {
		return
	}
	h := s.hierarchy()
	h.ParentMachineID = id
	s.setHierarchy(h)
}

// A2ATaskID is the A2A task that launched this subagent session (SessionHierarchy.A2ATaskID); "" when none.
func (s *Session) A2ATaskID() string {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.A2ATaskID
}

// SetA2ATaskID sets the A2A task ID that launched this subagent ("" for none).
func (s *Session) SetA2ATaskID(id string) {
	if s.A2ATaskID() == id {
		return
	}
	h := s.hierarchy()
	h.A2ATaskID = id
	s.setHierarchy(h)
}

// SubagentOrigin is how this subagent session was created (SessionHierarchy.SubagentOrigin); "" when unset.
func (s *Session) SubagentOrigin() string {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.SubagentOrigin
}

// SetSubagentOrigin sets the subagent origin ("" for unset).
func (s *Session) SetSubagentOrigin(origin string) {
	if s.SubagentOrigin() == origin {
		return
	}
	h := s.hierarchy()
	h.SubagentOrigin = origin
	s.setHierarchy(h)
}

// SetSubagentProvenance updates parent_machine_id, a2a_task_id and subagent_origin in one copy-on-write step.
func (s *Session) SetSubagentProvenance(parentMachine, a2aTaskID, origin string) {
	if s.ParentMachineID() == parentMachine && s.A2ATaskID() == a2aTaskID && s.SubagentOrigin() == origin {
		return
	}
	h := s.hierarchy()
	h.ParentMachineID, h.A2ATaskID, h.SubagentOrigin = parentMachine, a2aTaskID, origin
	s.setHierarchy(h)
}

// FoldedAs says why this row folds under FoldedUnder when it is no child (SessionHierarchy.FoldedAs); "" otherwise.
func (s *Session) FoldedAs() string {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.FoldedAs
}

// FoldedSide is the side session this folded row belongs to when it is not that side (SessionHierarchy.FoldedSide);
// "" when none.
func (s *Session) FoldedSide() string {
	if s.Hierarchy == nil {
		return ""
	}
	return s.Hierarchy.FoldedSide
}

// Children is the rollup of the sessions folded under this one; the zero value when none are.
func (s *Session) Children() SessionChildren {
	if s.Hierarchy == nil || s.Hierarchy.Children == nil {
		return SessionChildren{}
	}
	return *s.Hierarchy.Children
}

// EngineHistory is the conversation's earlier stretches this session continues (SessionHierarchy.EngineHistory),
// oldest first; nil when it never moved between engines. Shared with the row's other copies: not to be changed.
func (s *Session) EngineHistory() []EngineLeg {
	if s.Hierarchy == nil {
		return nil
	}
	return s.Hierarchy.EngineHistory
}

// SetEngineHistory sets the conversation's earlier stretches (nil: none), with no new hierarchy when it already has
// them.
func (s *Session) SetEngineHistory(legs []EngineLeg) {
	if SameEngineHistory(s.EngineHistory(), legs) {
		return
	}
	h := s.hierarchy()
	h.EngineHistory = slices.Clone(legs)
	s.setHierarchy(h)
}

// Aliases are the other session ids the row's conversation is known by on its machine (SessionHierarchy.Aliases);
// nil when none. Shared with the row's other copies: not to be changed.
func (s *Session) Aliases() []string {
	if s.Hierarchy == nil {
		return nil
	}
	return s.Hierarchy.Aliases
}

// SetAliases sets the other ids the row's conversation is known by (nil: none), with no new hierarchy when it already
// has them.
func (s *Session) SetAliases(ids []string) {
	if slices.Equal(s.Aliases(), ids) {
		return
	}
	h := s.hierarchy()
	h.Aliases = slices.Clone(ids)
	s.setHierarchy(h)
}

// SetStartedAt sets when the session began (zero: not known), with no new hierarchy when it is already so.
func (s *Session) SetStartedAt(t time.Time) {
	if s.StartedAt().Equal(t) && s.StartedAt().Location() == t.Location() {
		return
	}
	h := s.hierarchy()
	h.StartedAt = t
	s.setHierarchy(h)
}

// SetAskingParentSessionID sets the parent a blocked runtime child names ("" for none).
func (s *Session) SetAskingParentSessionID(id string) {
	if s.AskingParentSessionID() == id {
		return
	}
	h := s.hierarchy()
	h.AskingParentSessionID = id
	s.setHierarchy(h)
}

// SetFold sets what folding says of the session on its own machine: the session it folds under ("" for none), why
// when it is no child (as: FoldedAsWorker or FoldedAsSide, "" for none), the side session it belongs to when it is not
// that side (side, "" for none) and the rollup of those folded under it (nil for none). Nothing new is made when the
// session already says so.
func (s *Session) SetFold(under, as, side string, children *SessionChildren) {
	s.SetFoldWithMachine("", under, as, side, children)
}

// SetFoldWithMachine is SetFold with an optional cross-machine parent machine ID (empty for same machine or none).
func (s *Session) SetFoldWithMachine(underMachine, under, as, side string, children *SessionChildren) {
	if under == "" {
		underMachine = ""
	}
	cur := s.Hierarchy
	if cur == nil && underMachine == "" && under == "" && as == "" && side == "" && children == nil {
		return
	}
	if cur != nil && cur.FoldedUnderMachineID == underMachine && cur.FoldedUnder == under && cur.FoldedAs == as && cur.FoldedSide == side &&
		(cur.Children == nil) == (children == nil) && (children == nil || *cur.Children == *children) {
		return
	}
	h := s.hierarchy()
	h.FoldedUnderMachineID, h.FoldedUnder, h.FoldedAs, h.FoldedSide = underMachine, under, as, side
	h.Children = nil
	if children != nil {
		c := *children
		h.Children = &c
	}
	s.setHierarchy(h)
}

// IsChild says whether the session was started by another one, as the hierarchy reads it: a subagent, a session that
// names its parent (also while it waits for the user, AskingParentSessionID) or a root other than itself.
func (s *Session) IsChild() bool {
	return s.IsSubagent || s.ParentSessionID != "" || s.AskingParentSessionID() != "" ||
		(s.RootSessionID != "" && s.RootSessionID != s.SessionID)
}

// FoldedState is the state a row shows with its folded children (docs/protocol/protocol.md "Session hierarchy"): it
// needs the user when it or one of them does; it works when one of them works, unless it has a result not looked at
// yet or awaits the user's reply; it is unread when one of its side sessions is and it does not work itself (sides
// only: a Codex child's or a worker's result is its parent's to report, #7944); otherwise its own.
func (s *Session) FoldedState() string {
	c := s.Children()
	switch {
	case s.State == StateNeedsYou || c.NeedsYou > 0:
		return StateNeedsYou
	case c.Working > 0 && s.State != StateUnread && !(s.State == StateIdle && s.AwaitsReply):
		return StateWorking
	case c.SidesUnread > 0 && s.State != StateWorking:
		return StateUnread
	}
	return s.State
}
