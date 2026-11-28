package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/i18n"
	"net/http"
	"slices"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devices"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/proto"
)

// The fleet's tests (fleet_*_test.go): a manager with machines whose nodes a test plays (fleetDevs), on a fake hub
// that keeps what it was told (fakeHub.told, pushes) and an in-memory interactions table (fleetMem).

// fleetDevs is the manager's device layer for the fleet's tests: machines with the sessions their nodes list, and a
// node whose calls a test answers.
type fleetDevs struct {
	mu           sync.Mutex
	machines     map[string]*fleetNode
	order        []string
	usage        assistantwire.UsageSummary
	capacity     proto.CapacitySnapshot // what CapacitySnapshot answers (capacity_test.go)
	accounts     []proto.UsageAccount   // what UsageAccounts answers (candidate_headroom_test.go)
	gatewayRetry func(source, provider string, now time.Time) time.Time
	gateways     proto.GatewayAccounts
	// attention is every Persona Link summary handed over (SetAttention), in order.
	attention []link.Attention
	// linkDesktop: the manager has a Persona Link desktop; confirmed: the cards its app confirmed it handed to
	// Notification Center (confirm; LinkRang).
	linkDesktop bool
	confirmed   map[string]bool
	// watched: the sessions an app has open (Watched), by machine and session.
	watched map[[2]string]bool
	// held: the owner's messages held for machines' Agents while their computer is away (HeldMessages), by machine and
	// session; heldAsked and heldLang: how many times HeldMessages was called for a machine and in what language last
	// (counted, not listed: the allocation tests decorate through it); heldActs: every HeldRetry / HeldDelete
	// ("retry <session>/<message>"); heldErr: what those answer for a message they find.
	held      map[string]map[string][]proto.UndeliveredMessage
	heldAsked map[string]int
	heldLang  map[string]i18n.Lang
	heldActs  []string
	heldErr   error
	// transfers: every AgentTransfer asked for, in order; transfer answers them (nil: done, with the paths asked).
	transfers []devices.AgentTransfer
	transfer  func(devices.AgentTransfer) (devices.AgentTransferResult, error)
}

type fleetNode struct {
	name, home, trust string
	homeReal          string // home with its links followed, when the node says it is another path
	executable        string // the file its node said it runs from when its connection began ("": it did not say)
	sessions          []proto.Session
	lists             int // how many times its whole list was read (Sessions)
	connected         time.Time
	// away: its node is not connected (Summary and List say not reachable); its sessions stay as last listed.
	away bool
	// shownUntil: shown as before until then while its node is away (Shown, devices.Shown.Until); unlisted: its node
	// has given no list on this connection yet (Shown's Listed false).
	shownUntil time.Time
	unlisted   bool
	// call answers the node's methods (nil: {}); calls are the ones it got.
	call  func(method string, params json.RawMessage) (any, error)
	calls []fleetCall
}

type fleetCall struct {
	method string
	params json.RawMessage
}

func newFleetDevs() *fleetDevs { return &fleetDevs{machines: map[string]*fleetNode{}} }

// add puts a machine on the devices.
func (d *fleetDevs) add(id, name string) *fleetNode {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := &fleetNode{name: name, home: "/home/u"}
	d.machines[id] = n
	d.order = append(d.order, id)
	return n
}

// setSessions is the node's list now (as devices keeps it before the manager is told).
func (d *fleetDevs) setSessions(id string, list []proto.Session) {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i := range list {
		list[i].MachineID = id
	}
	d.machines[id].sessions = list
}

func (d *fleetDevs) callsTo(id, method string) []json.RawMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []json.RawMessage
	for _, c := range d.machines[id].calls {
		if c.method == method {
			out = append(out, c.params)
		}
	}
	return out
}

func (d *fleetDevs) Hello(json.RawMessage) {}
func (d *fleetDevs) BusUp()                {}
func (d *fleetDevs) Request(context.Context, string, json.RawMessage) (any, bool, error) {
	return nil, false, nil
}
func (d *fleetDevs) Event(string, json.RawMessage) bool     { return false }
func (d *fleetDevs) Handler(next http.Handler) http.Handler { return next }
func (d *fleetDevs) Has(id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.machines[id] != nil
}
func (d *fleetDevs) SetKeep(map[string][]string) {}

func (d *fleetDevs) CallNode(_ context.Context, id, method string, params json.RawMessage) (json.RawMessage, error) {
	d.mu.Lock()
	n := d.machines[id]
	if n == nil {
		d.mu.Unlock()
		return nil, devices.ErrNoMachine
	}
	n.calls = append(n.calls, fleetCall{method, params})
	call := n.call
	d.mu.Unlock()
	if call == nil {
		return json.RawMessage(`{}`), nil
	}
	out, err := call(method, params)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}

func (d *fleetDevs) List() []devices.Summary {
	d.mu.Lock()
	defer d.mu.Unlock()
	var out []devices.Summary
	for _, id := range d.order {
		n := d.machines[id]
		out = append(out, devices.Summary{ID: id, Name: n.name, Home: n.home, HomeReal: n.homeReal, Executable: n.executable, Trust: n.trust,
			Online: !n.away, Reachable: !n.away})
	}
	return out
}

func (d *fleetDevs) MachineResources(string) (*proto.Machine, *proto.MetricSample, *bool) {
	return nil, nil, nil
}

func (d *fleetDevs) Canonical(id string) string { return id }

// Summary is List's entry for id, found without the others as the devices do.
func (d *fleetDevs) Summary(id string) (devices.Summary, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := d.machines[id]
	if n == nil || !slices.Contains(d.order, id) {
		return devices.Summary{}, false
	}
	return devices.Summary{ID: id, Name: n.name, Home: n.home, HomeReal: n.homeReal, Executable: n.executable, Trust: n.trust,
		Online: !n.away, Reachable: !n.away}, true
}

// Shown is the node's shownUntil and unlisted, as the devices say them.
func (d *fleetDevs) Shown(id string) (devices.Shown, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := d.machines[id]
	if n == nil {
		return devices.Shown{}, false
	}
	return devices.Shown{Listed: !n.unlisted, Until: n.shownUntil}, true
}

func (d *fleetDevs) Sessions(id string) ([]proto.Session, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := d.machines[id]
	if n == nil {
		return nil, false
	}
	n.lists++
	return append([]proto.Session(nil), n.sessions...), true
}

// AppendSessions copies the list into dst's array, as the devices do.
func (d *fleetDevs) AppendSessions(dst []proto.Session, id string) ([]proto.Session, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := d.machines[id]
	if n == nil {
		return dst, false
	}
	return append(dst, n.sessions...), true
}

// SessionsWhere copies out only what keep accepts, as the devices do: a benchmark of a view then counts no copy of the
// whole list.
func (d *fleetDevs) SessionsWhere(id string, keep func(*proto.Session) bool) ([]proto.Session, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := d.machines[id]
	if n == nil {
		return nil, false
	}
	return keptWhere(n.sessions, keep), true
}

// keptWhere is the sessions of list keep accepts, copied (Devices.SessionsWhere).
func keptWhere(list []proto.Session, keep func(*proto.Session) bool) []proto.Session {
	var out []proto.Session
	for i := range list {
		if keep(&list[i]) {
			out = append(out, list[i])
		}
	}
	return out
}

// Session finds one session in place, as devices.Session does: copying the whole list here would make a benchmark of a
// lookup measure this fake instead.
func (d *fleetDevs) Session(id, session string) (proto.Session, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n := d.machines[id]; n != nil {
		for i := range n.sessions {
			if n.sessions[i].SessionID == session {
				return n.sessions[i], true
			}
		}
	}
	return proto.Session{}, false
}

func (d *fleetDevs) ConnectedAt(id string) time.Time {
	d.mu.Lock()
	defer d.mu.Unlock()
	if n := d.machines[id]; n != nil {
		return n.connected
	}
	return time.Time{}
}

func (d *fleetDevs) Usage() assistantwire.UsageSummary {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.usage
}

func (d *fleetDevs) CapacitySnapshot(context.Context) proto.CapacitySnapshot {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.capacity
}

func (d *fleetDevs) UsageAccounts(context.Context) []proto.UsageAccount {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]proto.UsageAccount(nil), d.accounts...)
}

func (d *fleetDevs) UsageGatewayAccounts() proto.GatewayAccounts {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.gateways
}

func (d *fleetDevs) GatewayRetryAt(source, provider string, now time.Time) time.Time {
	if d.gatewayRetry != nil {
		return d.gatewayRetry(source, provider, now)
	}
	return time.Time{}
}

func (d *fleetDevs) SetAttention(a link.Attention) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.attention = append(d.attention, a)
}

// confirm is the Persona Link desktop's app confirming it handed these cards' notifications to Notification Center.
func (d *fleetDevs) confirm(cards ...string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.confirmed == nil {
		d.confirmed = map[string]bool{}
	}
	for _, c := range cards {
		d.confirmed[c] = true
	}
}

// LinkRang is the devices' (internal/devices LinkRang): true once the desktop's app confirmed card, false at once
// without a desktop, and when the time is up or ctx ends.
func (d *fleetDevs) LinkRang(ctx context.Context, card string, within time.Duration) bool {
	ctx, cancel := context.WithTimeout(ctx, within)
	defer cancel()
	for {
		d.mu.Lock()
		on, rang := d.linkDesktop, d.confirmed[card]
		d.mu.Unlock()
		if !on || rang {
			return rang
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// handedAttention is every summary handed over so far.
func (d *fleetDevs) handedAttention() []link.Attention {
	d.mu.Lock()
	defer d.mu.Unlock()
	return slices.Clone(d.attention)
}

// fleetMem is the in-memory store of the fleet's tests: the assistant's settings, the interactions table, "Always Allow in Session",
// the seen times.
type fleetMem struct {
	memStore
	fmu    sync.Mutex
	status map[string]string
	res    map[string]*proto.Resolution
	recs   []db.InteractionRecord
	// endedBy is who or what ended each interaction, as the history's ended_by keeps it (db.WithEndedBy).
	endedBy map[string]string
	always  map[string]map[string]bool // session → keys
	seen    map[[2]string]time.Time
	// insertGate, when set before the cards come, holds each interaction write until it is closed (a slow database).
	insertGate chan struct{}
	// resolvedInsert* controls the one-statement reversible-setting decision history write in failure and race tests.
	resolvedInsertErr     error
	resolvedInsertGate    chan struct{}
	resolvedInsertStarted chan struct{}
	resolvedOutcomeErr    error
	deadlineErr           error
}

func newFleetMem() *fleetMem {
	return &fleetMem{status: map[string]string{}, res: map[string]*proto.Resolution{}, always: map[string]map[string]bool{},
		seen: map[[2]string]time.Time{}}
}

func (m *fleetMem) InsertInteraction(ctx context.Context, ix proto.Interaction) error {
	if m.insertGate != nil {
		select {
		case <-m.insertGate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	m.fmu.Lock()
	defer m.fmu.Unlock()
	for i := range m.recs {
		if m.recs[i].Interaction.ID != ix.ID {
			continue
		}
		same := sameDurableApprovalOccurrence(ix, m.recs[i].Interaction)
		startupRelist := same && !ix.Resumable && m.recs[i].Status == "cancelled" && m.recs[i].Resolution == nil
		if ix.CreatedAt.After(m.recs[i].Interaction.CreatedAt) || startupRelist {
			m.recs[i] = db.InteractionRecord{Interaction: ix, Status: "pending"}
			m.status[ix.ID], m.res[ix.ID] = "pending", nil
			delete(m.endedBy, ix.ID)
		}
		return nil
	}
	m.status[ix.ID] = "pending"
	m.recs = append(m.recs, db.InteractionRecord{Interaction: ix, Status: "pending"})
	return nil
}

func (m *fleetMem) UpdateInteractionDeadline(_ context.Context, id string, created, expires time.Time) error {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	if m.deadlineErr != nil {
		return m.deadlineErr
	}
	if m.status[id] != "pending" {
		return errors.New("interaction deadline was not recorded")
	}
	for i := range m.recs {
		if m.recs[i].Interaction.ID == id && m.recs[i].Interaction.CreatedAt.Equal(created) {
			m.recs[i].Interaction.ExpiresAt = expires
			return nil
		}
	}
	return errors.New("interaction deadline was not recorded")
}

func (m *fleetMem) InsertResolvedInteraction(ctx context.Context, ix proto.Interaction, res *proto.Resolution) error {
	m.fmu.Lock()
	started, gate, insertErr := m.resolvedInsertStarted, m.resolvedInsertGate, m.resolvedInsertErr
	m.resolvedInsertStarted = nil
	m.fmu.Unlock()
	if started != nil {
		close(started)
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if insertErr != nil {
		return insertErr
	}
	m.fmu.Lock()
	defer m.fmu.Unlock()
	if _, exists := m.status[ix.ID]; exists {
		return errors.New("duplicate interaction id")
	}
	now := time.Now().UTC()
	m.status[ix.ID], m.res[ix.ID] = "resolved", res
	if m.endedBy == nil {
		m.endedBy = map[string]string{}
	}
	m.endedBy[ix.ID] = db.EndedBy(ctx)
	m.recs = append(m.recs, db.InteractionRecord{Interaction: ix, Status: "resolved", Resolution: res, ResolvedAt: &now})
	return nil
}

func (m *fleetMem) MarkResolvedInteractionOutcome(_ context.Context, id string, created time.Time, outcome string, _ time.Time) error {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	if m.resolvedOutcomeErr != nil {
		return m.resolvedOutcomeErr
	}
	if outcome != "saved" && outcome != "not_saved" {
		return errors.New("unknown resolved-interaction outcome")
	}
	if m.status[id] != "resolved" {
		return errors.New("resolved-interaction outcome was not recorded")
	}
	for i := range m.recs {
		if m.recs[i].Interaction.ID != id || !m.recs[i].Interaction.CreatedAt.Equal(created) {
			continue
		}
		var input map[string]json.RawMessage
		if err := json.Unmarshal(m.recs[i].Interaction.ToolInput, &input); err != nil {
			return err
		}
		raw, _ := json.Marshal(outcome)
		input["outcome"] = raw
		updated, err := json.Marshal(input)
		if err != nil {
			return err
		}
		m.recs[i].Interaction.ToolInput = updated
		return nil
	}
	return errors.New("resolved-interaction outcome was not recorded")
}

func (m *fleetMem) MarkInteractionResumable(_ context.Context, id string) (bool, error) {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	if m.status[id] != "pending" {
		return false, nil
	}
	for i := range m.recs {
		if m.recs[i].Interaction.ID == id {
			m.recs[i].Interaction.Resumable = true
		}
	}
	return true, nil
}

func (m *fleetMem) FinishInteraction(ctx context.Context, id, status string, res *proto.Resolution) error {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	if m.status[id] == "pending" {
		m.status[id], m.res[id] = status, res
		if m.endedBy == nil {
			m.endedBy = map[string]string{}
		}
		m.endedBy[id] = db.EndedBy(ctx)
		for i := range m.recs {
			if m.recs[i].Interaction.ID == id {
				m.recs[i].Status = status
			}
		}
	}
	return nil
}

func (m *fleetMem) FinishInteractionOccurrence(ctx context.Context, ix proto.Interaction, status string, res *proto.Resolution) (bool, bool, error) {
	if err := m.InsertInteraction(ctx, ix); err != nil {
		return false, false, err
	}
	m.fmu.Lock()
	defer m.fmu.Unlock()
	for i := range m.recs {
		if m.recs[i].Interaction.ID != ix.ID {
			continue
		}
		if !sameDurableApprovalOccurrence(ix, m.recs[i].Interaction) {
			return false, m.status[ix.ID] != "pending", nil
		}
		if m.status[ix.ID] == "pending" {
			m.status[ix.ID], m.res[ix.ID] = status, res
			if m.endedBy == nil {
				m.endedBy = map[string]string{}
			}
			m.endedBy[ix.ID] = db.EndedBy(ctx)
			m.recs[i].Status, m.recs[i].Resolution = status, res
		}
		if m.status[ix.ID] != status {
			return false, true, errors.New("interaction occurrence already has a different terminal status")
		}
		return true, true, nil
	}
	return false, false, errors.New("interaction occurrence is missing after its insert")
}

// endedAs is who or what ended interaction id, as the history keeps it.
func (m *fleetMem) endedAs(id string) string {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	return m.endedBy[id]
}

func (m *fleetMem) InteractionOutcome(_ context.Context, id string) (string, *proto.Resolution, error) {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	return m.status[id], m.res[id], nil
}

func (m *fleetMem) Interactions(_ context.Context, machine, session string, _ int) ([]db.InteractionRecord, error) {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	out := []db.InteractionRecord{}
	for _, r := range m.recs {
		if (machine == "" || r.Interaction.MachineID == machine) && (session == "" || r.Interaction.SessionID == session) {
			out = append(out, r)
		}
	}
	return out, nil
}

// InteractionsPage is db.InteractionsPage over the kept records, by their interaction's CreatedAt.
func (m *fleetMem) InteractionsPage(_ context.Context, q db.InteractionQuery) (db.InteractionPage, error) {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	if q.Limit <= 0 || q.Limit > 500 {
		q.Limit = 100
	}
	var all []db.InteractionRecord
	for _, r := range m.recs {
		ix := r.Interaction
		if q.Machine != "" && ix.MachineID != q.Machine || q.Session != "" && ix.SessionID != q.Session || q.Kind != "" && ix.Kind != q.Kind ||
			!q.Since.IsZero() && ix.CreatedAt.Before(q.Since) {
			continue
		}
		if !q.Before.IsZero() && !(ix.CreatedAt.Before(q.Before) || q.BeforeID != "" && ix.CreatedAt.Equal(q.Before) && ix.ID < q.BeforeID) {
			continue
		}
		all = append(all, r)
	}
	sort.Slice(all, func(i, j int) bool {
		a, b := all[i].Interaction, all[j].Interaction
		if a.CreatedAt.Equal(b.CreatedAt) {
			return a.ID > b.ID
		}
		return a.CreatedAt.After(b.CreatedAt)
	})
	page := db.InteractionPage{Records: all[:min(len(all), q.Limit)]}
	if len(all) >= q.Limit {
		last := all[q.Limit-1].Interaction
		page.NextBefore, page.NextBeforeID = &last.CreatedAt, last.ID
	}
	return page, nil
}

func (m *fleetMem) AllowAlways(_ context.Context, session, _ string, keys []string) error {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	if m.always[session] == nil {
		m.always[session] = map[string]bool{}
	}
	for _, k := range keys {
		m.always[session][k] = true
	}
	return nil
}

func (m *fleetMem) AlwaysAllowed(_ context.Context, session string, keys []string) (bool, error) {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	for _, k := range keys {
		if !m.always[session][k] {
			return false, nil
		}
	}
	return len(keys) > 0, nil
}

func (m *fleetMem) CopyAlways(_ context.Context, fromSession, toSession string) (int, error) {
	if fromSession == "" || toSession == "" || fromSession == toSession {
		return 0, nil
	}
	m.fmu.Lock()
	defer m.fmu.Unlock()
	src := m.always[fromSession]
	if len(src) == 0 {
		return 0, nil
	}
	if m.always[toSession] == nil {
		m.always[toSession] = map[string]bool{}
	}
	copied := 0
	for k := range src {
		if !m.always[toSession][k] {
			m.always[toSession][k] = true
			copied++
		}
	}
	return copied, nil
}

func (m *fleetMem) MarkSeen(_ context.Context, machine, session string, at time.Time) error {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	m.seen[[2]string{machine, session}] = at
	return nil
}

func (m *fleetMem) SeenTimes(context.Context) (map[[2]string]time.Time, error) {
	m.fmu.Lock()
	defer m.fmu.Unlock()
	out := map[[2]string]time.Time{}
	for k, v := range m.seen {
		out[k] = v
	}
	return out, nil
}

var _ fleetStore = (*fleetMem)(nil)

// fleetTest is a manager with the fleet on: s, its devices d, its hub f, its store st.
type fleetTest struct {
	t      *testing.T
	s      *Service
	d      *fleetDevs
	f      *fakeHub
	st     *fleetMem
	cancel context.CancelFunc
}

// newFleetTest is a manager carrying the machine "mac" (named "Mac") with a store, on a fake hub.
func newFleetTest(t *testing.T) *fleetTest {
	t.Helper()
	f := newFakeHub()
	d := newFleetDevs()
	d.add("mac", "Mac")
	st := newFleetMem()
	s := New(Options{Hub: f, Store: func() Store { return st }, Logf: testLogf(t), Devices: d})
	return &fleetTest{t: t, s: s, d: d, f: f, st: st}
}

// newFleetTestNoStore is newFleetTest without a database.
func newFleetTestNoStore(t *testing.T) *fleetTest {
	t.Helper()
	f := newFakeHub()
	d := newFleetDevs()
	d.add("mac", "Mac")
	s := New(Options{Hub: f, Logf: testLogf(t), Devices: d})
	return &fleetTest{t: t, s: s, d: d, f: f}
}

// event is a node's event on machine as the devices hand it over.
func (ft *fleetTest) event(machine, name string, params any) bool {
	b, _ := json.Marshal(params)
	return ft.s.NodeEvent(machine, name, b)
}

// sessions has machine's node list sessions (sessions.changed).
func (ft *fleetTest) sessions(machine string, list ...proto.Session) {
	ft.d.setSessions(machine, append([]proto.Session(nil), list...))
	ft.event(machine, "sessions.changed", map[string]any{"sessions": list})
}

// waitPending waits for n pending interactions.
func (ft *fleetTest) waitPending(n int) []proto.Interaction {
	ft.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		got := ft.s.pendingInteractions()
		if len(got) == n {
			return got
		}
		if time.Now().After(deadline) {
			ft.t.Fatalf("pending = %d, want %d", len(got), n)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// toldOf are the params of what the hub was told of kind event, in order.
func (ft *fleetTest) toldOf(event string) []json.RawMessage {
	ft.f.mu.Lock()
	defer ft.f.mu.Unlock()
	var out []json.RawMessage
	for _, t := range ft.f.told {
		if t.event == event {
			out = append(out, t.params)
		}
	}
	return out
}

// lastList is the last list the hub was told for machine (fleet.sessions).
func (ft *fleetTest) lastList(machine string) []proto.Session {
	ft.t.Helper()
	list := ft.toldOf(assistantwire.EventFleetSessions)
	for i := len(list) - 1; i >= 0; i-- {
		var p assistantwire.FleetSessions
		if json.Unmarshal(list[i], &p) == nil && p.MachineID == machine {
			return p.Sessions
		}
	}
	return nil
}

// pushesOf are the pushes the hub was asked for of kind, waited for a little (they go off the caller's way).
func (ft *fleetTest) pushesOf(kind string, n int, wait time.Duration) []Notification {
	deadline := time.Now().Add(wait)
	for {
		ft.f.mu.Lock()
		var out []Notification
		for _, p := range ft.f.pushes {
			if p.Kind == kind {
				out = append(out, p)
			}
		}
		ft.f.mu.Unlock()
		if len(out) >= n || time.Now().After(deadline) {
			return out
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// ids are the pending interactions' ids, sorted.
func ids(list []proto.Interaction) []string {
	out := []string{}
	for _, ix := range list {
		out = append(out, ix.ID)
	}
	sort.Strings(out)
	return out
}

var errNode = errors.New("the node refused")

func init() { _ = apierr.ErrOffline }

func (d *fleetDevs) Follow(context.Context, string, string, bool) error { return nil }

func (d *fleetDevs) Watched(id, session string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.watched[[2]string{id, session}]
}

// watch has an app open machine id's session (on) or close it.
func (d *fleetDevs) watch(id, session string, on bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.watched == nil {
		d.watched = map[[2]string]bool{}
	}
	d.watched[[2]string{id, session}] = on
}

// HeldMessages are the messages the test holds for machine's Agents (held), as the devices' index answers them; the
// calls are counted (heldAsked, heldLang).
func (d *fleetDevs) HeldMessages(machine string, lang i18n.Lang) map[string][]proto.UndeliveredMessage {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.heldAsked == nil {
		d.heldAsked, d.heldLang = map[string]int{}, map[string]i18n.Lang{}
	}
	d.heldAsked[machine]++
	d.heldLang[machine] = lang
	var out map[string][]proto.UndeliveredMessage
	for sid, list := range d.held[machine] {
		if out == nil {
			out = map[string][]proto.UndeliveredMessage{}
		}
		out[sid] = slices.Clone(list)
	}
	return out
}

func (d *fleetDevs) HeldRetry(_ context.Context, sid, msg string) (bool, error) {
	return d.heldAct("retry", sid, msg)
}

func (d *fleetDevs) HeldDelete(_ context.Context, sid, msg string) (bool, error) {
	return d.heldAct("delete", sid, msg)
}

// AgentTransfer notes t and answers it with transfer (a done file at the path asked when that is nil).
func (d *fleetDevs) AgentTransfer(_ context.Context, t devices.AgentTransfer) (devices.AgentTransferResult, error) {
	d.mu.Lock()
	d.transfers = append(d.transfers, t)
	answer := d.transfer
	d.mu.Unlock()
	if answer != nil {
		return answer(t)
	}
	return devices.AgentTransferResult{ID: "t1", Kind: proto.FilePackFile, Name: "report.pdf", Source: t.Path,
		ResultPath: "/home/me/Downloads/Persona/report.pdf", Size: 2048, SHA256: "abababababababababababababababababababababababababababababababab"}, nil
}

// heldAct notes Retry or Delete (what) on held message msg of session sid, found on any machine or not; one found is
// answered heldErr, and a Delete that succeeds takes it away.
func (d *fleetDevs) heldAct(what, sid, msg string) (bool, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.heldActs = append(d.heldActs, what+" "+sid+"/"+msg)
	for _, bySession := range d.held {
		if i := slices.IndexFunc(bySession[sid], func(m proto.UndeliveredMessage) bool { return m.ID == msg }); i >= 0 {
			if what == "delete" && d.heldErr == nil {
				bySession[sid] = slices.Delete(slices.Clone(bySession[sid]), i, i+1)
			}
			return true, d.heldErr
		}
	}
	return false, nil
}
