package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type logicalConversationStore interface {
	LogicalConversation(context.Context, string, string) (*db.LogicalConversation, error)
	LogicalConversations(context.Context) ([]db.LogicalConversation, error)
	AdoptConversationAliases(context.Context, string, string, string, string, string, []string) error
	WithConversationEpoch(context.Context, string, string, int64, func(*db.LogicalConversation) error) error
	WithDrainedConversationEpoch(context.Context, string, string, int64, func(*db.LogicalConversation) error) error
	BeginConversationSwitch(context.Context, string, string, db.ConversationSwitch) error
	PrepareConversationSegment(context.Context, string, string, string, conversationlog.Segment, int) error
	ActivateConversationSegment(context.Context, string, string, string, int64) error
	ConversationSwitch(context.Context, string, string, string) (*db.ConversationSwitch, error)
	ObserveConversationSource(context.Context, string, string, string) error
	ObserveConversationRevision(context.Context, string, string, int64) error
	ConversationSegmentClosed(context.Context, string) error
	EnqueueConversationInput(context.Context, string, string, string, int64, json.RawMessage) error
	ConversationInputReceipt(context.Context, string, string, string, json.RawMessage) (string, error)
	ClaimConversationInput(context.Context, string, string, int64) (*db.ConversationInput, error)
	ResolveConversationInput(context.Context, string, string, string, string) error
}

type logicalSwitchStore interface {
	ResolveConversationDelivery(context.Context, string, string, string, int64, string, string) error
	RequeueConversationInput(context.Context, string, string, string, int64, string) error
	RecordConversationPreparedCandidate(context.Context, db.ConversationPreparedCandidate) error
	ConversationPreparedCandidates(context.Context) ([]db.ConversationPreparedCandidate, error)
	ConversationPreparedCandidateDone(context.Context, string, string) error
	logicalConversationStore
	PutConversationHandoff(context.Context, string, string, string, json.RawMessage) error
	ConversationPreparation(context.Context, string, string, string) (json.RawMessage, bool, error)
	ConversationPendingHandoff(context.Context, string, string, string) (json.RawMessage, error)
	ClaimConversationPreparation(context.Context, string, string, string) (bool, error)
	ConversationPreparedSession(context.Context, string, string, string, string, int64) (string, error)
	ResolveConversationPreparation(context.Context, string, string, string, string, string) error
	ConversationUnresolvedInput(context.Context, string, string) (*db.ConversationInput, error)
	ConversationInputPending(context.Context, string, string) (bool, error)
	AcceptIdleConversationSwitch(context.Context, string, string, string, int64) (bool, error)
	CancelConversationSwitch(context.Context, string, string, string, int64, string) (bool, error)
	NoteConversationSwitchError(context.Context, string, string, string, string) error
	ConversationClosures(context.Context) ([][2]string, error)
	ConversationClosureDone(context.Context, string, string) error
}

type logicalModesStore interface {
	SetConversationSegmentModes(context.Context, string, string, int64, string, *string, *string, *string) error
}
type logicalSourceKey struct{}
type logicalHistoryOnlyKey struct{}

type logicalDeviceState struct {
	mu         sync.Mutex
	at         time.Time
	rows       []db.LogicalConversation
	index      map[[2]string]*db.LogicalConversation
	running    map[[2]string]bool
	sweepAfter [2]string
	watches    map[[2]string]json.RawMessage
}

// Reads may use a short-lived projection index. Authority and execution never do:
// logicalCall and logicalCaller always consult Postgres and its epoch locks.
func (s *Service) logicalIndex() ([]db.LogicalConversation, map[[2]string]*db.LogicalConversation, error) {
	st, ok := s.store().(logicalConversationStore)
	if !ok {
		return nil, nil, nil
	}
	b := &s.logical
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.index != nil && time.Since(b.at) < time.Second {
		return b.rows, b.index, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rows, err := st.LogicalConversations(ctx)
	if err != nil {
		return nil, nil, err
	}
	index := map[[2]string]*db.LogicalConversation{}
	for i := range rows {
		c := &rows[i]
		index[[2]string{c.MachineID, c.SessionID}] = c
		for _, seg := range c.Segments {
			index[[2]string{seg.MachineID, seg.SessionID}] = c
		}
	}
	b.rows, b.index, b.at = rows, index, time.Now()
	return rows, index, nil
}

func (s *Service) logicalChanged() {
	s.logical.mu.Lock()
	s.logical.at = time.Time{}
	s.logical.mu.Unlock()
}

// logicalDevices is s.devs when the store keeps logical conversations. Embedding the Devices interface promotes only
// its methods, so an optional capability (nodeAnnouncer, mcpWaitNotifier, machineRedeployer, ReportState) is asked of
// physicalDevices(): asked of s.devs it would never be found (#12928).
type logicalDevices struct {
	Devices
	s *Service
}

func (s *Service) physicalDevices() Devices {
	if d, ok := s.devs.(*logicalDevices); ok {
		return d.Devices
	}
	return s.devs
}

func (d *logicalDevices) Has(machine string) bool {
	if d.Devices.Has(machine) {
		return true
	}
	rows, _, err := d.s.logicalIndex()
	if err != nil {
		return false
	}
	for _, c := range rows {
		if c.MachineID == machine {
			return true
		}
	}
	return false
}

func (d *logicalDevices) Canonical(machine string) string {
	rows, _, err := d.s.logicalIndex()
	if err == nil {
		for _, c := range rows {
			if c.MachineID == machine {
				return machine
			}
		}
	}
	return d.Devices.Canonical(machine)
}

func (d *logicalDevices) List() []devices.Summary {
	out := d.Devices.List()
	rows, _, err := d.s.logicalIndex()
	if err != nil {
		return out
	}
	seen := map[string]bool{}
	for i := range out {
		seen[out[i].ID] = true
		if !out[i].Online {
			if _, ok := d.liveTarget(out[i].ID); ok {
				out[i].Online = true
			}
		}
	}
	for _, c := range rows {
		if seen[c.MachineID] {
			continue
		}
		if active, ok := c.Active(); ok {
			if m, ok := d.Devices.Summary(active.MachineID); ok {
				m.ID = c.MachineID
				out = append(out, m)
				seen[c.MachineID] = true
			}
		}
	}
	return out
}

func (d *logicalDevices) Summary(machine string) (devices.Summary, bool) {
	if m, ok := d.Devices.Summary(machine); ok {
		m.ID = d.Canonical(machine)
		if !m.Online {
			if _, live := d.liveTarget(machine); live {
				m.Online = true
			}
		}
		return m, true
	}
	rows, _, err := d.s.logicalIndex()
	if err != nil {
		return devices.Summary{}, false
	}
	for _, c := range rows {
		if c.MachineID == machine {
			if a, ok := c.Active(); ok {
				m, found := d.Devices.Summary(a.MachineID)
				m.ID = machine
				return m, found
			}
		}
	}
	return devices.Summary{}, false
}

func (d *logicalDevices) Shown(machine string) (devices.Shown, bool) {
	if target, ok := d.liveTarget(machine); ok {
		return d.Devices.Shown(target)
	}
	if shown, ok := d.Devices.Shown(machine); ok {
		return shown, true
	}
	rows, _, err := d.s.logicalIndex()
	if err != nil {
		return devices.Shown{}, false
	}
	for _, c := range rows {
		if c.MachineID == machine {
			if a, ok := c.Active(); ok {
				return d.Devices.Shown(a.MachineID)
			}
		}
	}
	return devices.Shown{}, false
}

func (d *logicalDevices) ConnectedAt(machine string) time.Time {
	if target, ok := d.liveTarget(machine); ok {
		return d.Devices.ConnectedAt(target)
	}
	if d.Devices.Has(machine) {
		return d.Devices.ConnectedAt(machine)
	}
	rows, _, err := d.s.logicalIndex()
	if err == nil {
		for _, c := range rows {
			if c.MachineID == machine {
				if a, ok := c.Active(); ok {
					return d.Devices.ConnectedAt(a.MachineID)
				}
			}
		}
	}
	return time.Time{}
}

// logicalConversationSpace recovers the authoritative space of logical conversation c from the
// conversation-space registry across its logical root and segments. Returns known=false when no space
// has been recorded or the store cannot be read so callers fail closed instead of treating an unknown
// space as the default empty space.
func (s *Service) logicalConversationSpace(ctx context.Context, c *db.LogicalConversation) (string, bool) {
	if c == nil {
		return "", false
	}
	ids := make([]string, 0, 1+len(c.Segments))
	seen := make(map[string]struct{}, 1+len(c.Segments))
	add := func(id string) {
		if id == "" {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	add(c.SessionID)
	for _, seg := range c.Segments {
		add(seg.SessionID)
	}
	return s.notedSpaceForIDs(ctx, ids, "")
}

// logicalRowSpace recovers the authoritative space of a LogicalPlaceholder row, consulting both the
// logical conversation's segment ids (when present in the logical index) and any explicit non-empty Space
// already carried on the row. Returns known=false when the space cannot be determined or the store fails.
func (s *Service) logicalRowSpace(ctx context.Context, row proto.Session) (string, bool) {
	ids := make([]string, 0, 2)
	seen := make(map[string]struct{}, 2)
	add := func(id string) {
		if id == "" {
			return
		}
		if _, dup := seen[id]; dup {
			return
		}
		seen[id] = struct{}{}
		ids = append(ids, id)
	}
	add(row.SessionID)
	_, index, err := s.logicalIndex()
	if err != nil {
		return "", false
	}
	if index != nil && row.SessionID != "" {
		if c := index[[2]string{row.MachineID, row.SessionID}]; c != nil {
			add(c.SessionID)
			for _, seg := range c.Segments {
				add(seg.SessionID)
			}
		}
	}
	return s.notedSpaceForIDs(ctx, ids, row.Space)
}

// isLogicalOfflineRow reports whether row represents an offline logical placeholder with no physical Cwd/Space,
// either in-process (LogicalPlaceholder) or after JSON decoding (where LogicalPlaceholder's json:"-" tag is
// stripped but the row still matches a logical conversation with empty Cwd and Space).
func (s *Service) isLogicalOfflineRow(row proto.Session) bool {
	if row.LogicalPlaceholder {
		return true
	}
	if !row.Assistant || row.Cwd != "" || row.Space != "" || row.SessionID == "" {
		return false
	}
	_, index, err := s.logicalIndex()
	if err != nil {
		return true
	}
	if index == nil {
		return false
	}
	return index[[2]string{row.MachineID, row.SessionID}] != nil
}

// conversationRowSpace returns row's authoritative space and whether it is known. Physical rows keep
// spaceOf(row); offline logical placeholders never equate an empty Cwd/Space to the default space unless
// their space is affirmatively known.
func (s *Service) conversationRowSpace(ctx context.Context, row proto.Session) (string, bool) {
	if !s.isLogicalOfflineRow(row) {
		return spaceOf(row), true
	}
	return s.logicalRowSpace(ctx, row)
}

func (s *Service) notedSpaceForIDs(ctx context.Context, ids []string, existingSpace string) (string, bool) {
	if len(ids) == 0 && existingSpace == "" {
		return "", false
	}
	lookupCtx := ctx
	var cancel context.CancelFunc
	if lookupCtx == nil {
		lookupCtx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
	} else if _, hasDeadline := lookupCtx.Deadline(); !hasDeadline {
		lookupCtx, cancel = context.WithTimeout(lookupCtx, 2*time.Second)
		defer cancel()
	}
	var noted map[string]string
	if len(ids) > 0 {
		var err error
		noted, err = s.readNotedSpaces(lookupCtx, ids)
		if err != nil {
			return "", false
		}
	}
	found := false
	best := ""
	bestRank := -1
	consider := func(sp string) {
		rank := 0
		if sp != "" {
			rank = 1 + s.trackPolicy(lookupCtx, sp).strictness()
		}
		if !found || rank > bestRank || (rank == bestRank && sp < best) {
			found, best, bestRank = true, sp, rank
		}
	}
	for _, id := range ids {
		if sp, ok := noted[id]; ok {
			consider(sp)
		}
	}
	if existingSpace != "" {
		consider(existingSpace)
	}
	return best, found
}

func (d *logicalDevices) Session(machine, session string) (proto.Session, bool) {
	_, index, err := d.s.logicalIndex()
	if err != nil {
		return proto.Session{}, false
	}
	c, err := d.s.physicalLogicalIndex(index, machine, session)
	if err != nil {
		return proto.Session{}, false
	}
	if c != nil {
		if machine != c.MachineID || session != c.SessionID {
			return proto.Session{}, false
		}
		if a, ok := c.Active(); ok {
			row, found := d.Devices.Session(a.MachineID, a.SessionID)
			if !found {
				row = proto.Session{Status: proto.StatusOffline, Assistant: true, LogicalPlaceholder: true}
			}
			row.MachineID, row.SessionID = c.MachineID, c.SessionID
			if c.Epoch > 1 {
				row.Rev = c.Revision
			}
			if row.LogicalPlaceholder {
				if sp, known := d.s.logicalConversationSpace(context.Background(), c); known && sp != "" {
					row.Space = sp
				}
			}
			return row, true
		}
		return proto.Session{}, false
	}
	return d.Devices.Session(machine, session)
}

func (d *logicalDevices) Sessions(machine string) ([]proto.Session, bool) {
	rows, index, err := d.s.logicalIndex()
	if err != nil {
		return nil, false
	}
	physical, known := d.Devices.Sessions(machine)
	out := make([]proto.Session, 0, len(physical))
	for _, row := range physical {
		c, err := d.s.physicalLogicalIndex(index, machine, row.SessionID)
		if err == nil && c == nil {
			out = append(out, row)
		}
	}
	for _, c := range rows {
		if c.MachineID == machine {
			if row, ok := d.Session(machine, c.SessionID); ok {
				out = append(out, row)
				known = true
			}
		}
	}
	return out, known
}

func (d *logicalDevices) AppendSessions(dst []proto.Session, machine string) ([]proto.Session, bool) {
	list, ok := d.Sessions(machine)
	return append(dst, list...), ok
}

func (d *logicalDevices) SessionsWhere(machine string, keep func(*proto.Session) bool) ([]proto.Session, bool) {
	list, ok := d.Sessions(machine)
	out := list[:0]
	for i := range list {
		if keep(&list[i]) {
			out = append(out, list[i])
		}
	}
	return out, ok
}

func (d *logicalDevices) Watched(machine, session string) bool {
	d.s.logical.mu.Lock()
	_, ok := d.s.logical.watches[[2]string{machine, session}]
	d.s.logical.mu.Unlock()
	return ok || d.Devices.Watched(machine, session)
}

func (d *logicalDevices) CallNode(ctx context.Context, machine, method string, raw json.RawMessage) (json.RawMessage, error) {
	if ctx.Value(logicalSourceKey{}) == nil {
		if out, handled, err := d.s.logicalCall(ctx, machine, method, raw); handled {
			return out, err
		}
	}
	return d.Devices.CallNode(ctx, machine, method, raw)
}

func (d *logicalDevices) Request(ctx context.Context, method string, raw json.RawMessage) (any, bool, error) {
	if method == devicewire.MethodCall && ctx.Value(logicalSourceKey{}) == nil {
		var p devicewire.NodeCall
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, true, err
		}
		if out, handled, err := d.s.logicalCall(context.WithValue(ctx, logicalTransportKey{}, p), p.MachineID, p.Method, p.Params); handled {
			return out, true, err
		}
	}
	return d.Devices.Request(ctx, method, raw)
}

func (d *logicalDevices) Follow(ctx context.Context, machine, session string, on bool) error {
	st, ok := d.s.store().(logicalConversationStore)
	if !ok {
		return d.Devices.Follow(ctx, machine, session, on)
	}
	c, err := d.s.physicalLogicalConversation(ctx, st, machine, session)
	if err != nil {
		return err
	}
	if c == nil {
		return d.Devices.Follow(ctx, machine, session, on)
	}
	if c.Deleted {
		return db.ErrConversationDeleted
	}
	if c.MachineID != machine || c.SessionID != session {
		return logicalRefusal(ctx, "private_segment")
	}
	a, ok := c.Active()
	if !ok {
		return errors.New("conversation has no active segment")
	}
	return d.Devices.Follow(ctx, a.MachineID, a.SessionID, on)
}

// The manager retains the root bus destination even if its original computer
// disappeared. It advertises no physical session mapping to the transport hub.
func (s *Service) logicalProjectStates(st devicewire.States) devicewire.States {
	rows, _, err := s.logicalIndex()
	if err != nil {
		return st
	}
	index := map[string]int{}
	physicalStates := map[string]devicewire.State{}
	for i, state := range st.Machines {
		index[state.Machine.ID] = i
		physicalStates[state.Machine.ID] = state
	}
	// Work on copies: the devices' snapshots may be concurrently read elsewhere.
	st.Machines = append([]devicewire.State(nil), st.Machines...)
	for _, c := range rows {
		a, ok := c.Active()
		if !ok {
			continue
		}
		target, known := physicalStates[a.MachineID]
		if !known {
			if physical := s.physicalDevices(); physical != nil {
				target, known = physicalStates[physical.Canonical(a.MachineID)]
			}
		}
		if !known {
			continue
		}
		if source, exists := index[c.MachineID]; exists {
			if !st.Machines[source].Connected && target.Connected {
				st.Machines[source].Connected = true
				st.Machines[source].ConnectedAt = target.ConnectedAt
				st.Machines[source].Machine.Online = true
			}
			continue
		}
		state := target
		state.Machine.ID = c.MachineID
		state.TransportFor = target.Machine.ID
		state.Config = nil
		state.Derived = true
		st.Machines = append(st.Machines, state)
		index[c.MachineID] = len(st.Machines) - 1
	}
	return st
}

// A retained but disconnected source must not make the public conversation look
// unreachable while its active segment has a live transport on another machine.
// Physical resource/accounting fields remain the source machine's own fields.
func (d *logicalDevices) liveTarget(machine string) (string, bool) {
	if source, ok := d.Devices.Summary(machine); ok && source.Online {
		return "", false
	}
	rows, _, err := d.s.logicalIndex()
	if err != nil {
		return "", false
	}
	for _, c := range rows {
		if c.MachineID != machine {
			continue
		}
		if a, ok := c.Active(); ok && a.MachineID != machine {
			if target, found := d.Devices.Summary(a.MachineID); found && target.Online {
				return a.MachineID, true
			}
		}
	}
	return "", false
}

type logicalSegmentInputStore interface {
	ConversationSegmentDispatched(context.Context, string, string, string) (bool, error)
	MarkConversationSegmentDispatched(context.Context, string, string, string) error
}

type currentConnectionListState interface {
	CurrentConnectionListed(string) bool
}

func isMissingTranscriptErr(err error) bool {
	if err == nil {
		return false
	}
	var re *rpc.Error
	if errors.As(err, &re) && re.Code == codeSessionNotFound {
		return true
	}
	code, coded := relayErrCode(err)
	return coded && code == codeSessionNotFound
}

// Empty runtime generations are not model turns. Recovery needs explicit
// first-message/preparation evidence, zero durable coverage and no dispatch.
func (s *Service) isZeroTurnPreparedSegment(ctx context.Context, c *db.LogicalConversation, seg conversationlog.Segment) bool {
	if c == nil || seg.Ordinal <= 1 || seg.State != "active" || seg.FrozenEnd != nil {
		return false
	}
	hs, ok := s.store().(conversationHistoryStore)
	if !ok {
		return false
	}
	h, err := hs.ConversationHistory(ctx, seg.MachineID, seg.SessionID)
	if err != nil {
		return false
	}
	var state struct {
		Awaiting *bool    `json:"awaiting_first_message"`
		Inputs   []string `json:"segment_inputs"`
	}
	_, err = s.store().Setting(ctx, "node_model."+seg.MachineID+"."+seg.SessionID, &state)
	if err != nil || len(state.Inputs) != 0 || state.Awaiting != nil && !*state.Awaiting {
		return false
	}
	if h != nil {
		// A read of agy items creates revision 1/history even before a turn.
		// Revision alone is never sufficient evidence for rebuilding a segment.
		if state.Awaiting == nil || !*state.Awaiting || h.TailSeq == nil || *h.TailSeq != 0 || h.Summary != "" || seg.SourceRevision > h.Revision || seg.SourceHistory != "" && seg.SourceHistory != h.History {
			return false
		}
		for _, r := range h.Coverage {
			if r.From != 0 || r.Through != 0 {
				return false
			}
		}
	} else if seg.SourceHistory != "" || seg.SourceRevision != 0 {
		return false
	}
	// A pristine segment with no observed generation is also proven prepared
	// by its durable activation switch (the DB predicate fails closed otherwise).
	inputs, ok := s.store().(logicalSegmentInputStore)
	if !ok {
		return false
	}
	dispatched, err := inputs.ConversationSegmentDispatched(ctx, c.MachineID, c.SessionID, seg.ID)
	return err == nil && !dispatched
}

func (s *Service) physicalSegmentMissing(ctx context.Context, seg conversationlog.Segment) bool {
	physical := s.physicalDevices()
	if physical != nil {
		if _, found := physical.Session(seg.MachineID, seg.SessionID); found {
			return false
		}
		if live, ok := physical.(currentConnectionListState); ok {
			if !live.CurrentConnectionListed(seg.MachineID) {
				return false
			}
		} else {
			sum, known := physical.Summary(seg.MachineID)
			if !known || !sum.Online {
				return false
			}
			if shown, ok := physical.Shown(seg.MachineID); !ok || !shown.Listed || !shown.Until.IsZero() {
				return false
			}
		}
	}
	// Never decide absence from the display cache alone: Shown.Listed can hold a
	// retained pre-reconnect snapshot or lag behind a slow >3s list push after
	// segment prepare. Probe the live node authoritatively with a bounded timeout;
	// offline, grace, unready, malformed, or timed-out probes stay uncertain.
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var raw json.RawMessage
	var err error
	if physical != nil {
		raw, err = physical.CallNode(probeCtx, seg.MachineID, DeviceSessionsList, json.RawMessage(`{}`))
	} else {
		err = s.call(context.WithValue(probeCtx, logicalSourceKey{}, true), seg.MachineID, DeviceSessionsList, map[string]any{}, &raw)
	}
	if err != nil {
		return false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return false
	}
	rawSessions, ok := fields["sessions"]
	if !ok {
		return false
	}
	var sessions []proto.Session
	if len(rawSessions) > 0 && string(rawSessions) != "null" {
		if err := json.Unmarshal(rawSessions, &sessions); err != nil {
			return false
		}
	}
	for _, row := range sessions {
		if row.SessionID == seg.SessionID {
			return false
		}
	}
	return true
}

func (s *Service) isVanishedZeroTurnSegment(ctx context.Context, c *db.LogicalConversation, seg conversationlog.Segment) bool {
	return s.isZeroTurnPreparedSegment(ctx, c, seg) && s.physicalSegmentMissing(ctx, seg)
}

func (s *Service) isZeroTurnPlaceholderConversation(ctx context.Context, machine, session string) bool {
	st, ok := s.store().(logicalConversationStore)
	if !ok {
		return false
	}
	c, err := s.physicalLogicalConversation(ctx, st, machine, session)
	if err != nil || c == nil || c.Deleted || c.MachineID != machine || c.SessionID != session {
		return false
	}
	a, ok := c.Active()
	return ok && s.isZeroTurnPreparedSegment(ctx, c, a)
}
