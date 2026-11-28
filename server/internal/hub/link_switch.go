package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Switching an ssh computer to Persona Link (#10662, docs/operations/link.md "Switching from SSH"). The manager that
// connects the computer runs the switch over its own ssh access; the hub does the two steps only it may do
// (devicewire/link_switch.go):
//
//   - begin: the entry is still what the manager saw (kind ssh, the same ssh_host), the user confirmed the computer,
//     the hub takes Persona Link, the computer has no link credential or open pairing code, and no switch or move of
//     it is under way. The hub sets the entry's kind to link, every other key as it was (hub.json's, or the manager's
//     own list for one it keeps), takes it at once as a rename is taken (no restart waits for it), and makes the
//     computer's pairing code. Only the manager gets the code, and only to hand it to the computer; it is in no log
//     line, audit row or error (its hash is: it is no secret).
//   - end: succeeded keeps it all; rolled_back revokes that code and the credential it became (all of the computer's
//     Persona Link while it is kind link, when the hub never got as far as recording the code) and puts the kind back
//     to ssh. An end is final: repeated, it answers the same and changes nothing; a switch that succeeded is never
//     rolled back.
//
// Each step is written to the computer's audit (action link_switch) before what goes by it, so a hub that restarted
// mid-switch still knows: begin (asked: nothing changed yet), begun (its code made, with the code's hash), and one of
// succeeded, rolled_back, begin_failed (nothing changed after all). Only the computer's latest switch counts: an older
// operation id proves nothing. A switch holds its computer for linkSwitchFor from its begin: it is not moved to
// another manager or removed, nor switched again, and its new credential does not rename it after its system
// (nameAfterSystem). The computer keeps its id, so all that is kept by id (its name, pins, mutes, history, trust)
// stays.

// linkSwitchFor is how long a switch holds its computer after its begin (the manager gives up on one after 20 min).
// An end is taken after it as well. A variable for the tests.
var linkSwitchFor = 30 * time.Minute

// A switch's steps in the audit; the last three end it.
const (
	switchBegin       = "begin"
	switchBegun       = "begun"
	switchSucceeded   = "succeeded"
	switchRolledBack  = "rolled_back"
	switchBeginFailed = "begin_failed"
)

func switchEnded(step string) bool {
	return step == switchSucceeded || step == switchRolledBack || step == switchBeginFailed
}

// linkSwitches is this hub's memory of its computers' switches, by machine id: the one under way of each (from its
// begin until it ended), the last that ended, and the computers a move or a removal holds now. What a restart loses
// the audit has.
type linkSwitches struct {
	mu       sync.Mutex
	run      map[string]*linkSwitchRun
	ended    map[string]linkSwitchEnded
	reserved map[string]int   // moves and removals under way
	now      func() time.Time // nil uses the real clock
}

func (s *linkSwitches) clock() time.Time {
	if s.now != nil {
		return s.now()
	}
	return time.Now()
}

// linkSwitchRun is one switch under way: the manager's id for it, who asked (device | admin), the manager that began
// it, its code's hash once it has one, and until when it holds its computer.
type linkSwitchRun struct {
	operation, by, manager, pairHash string
	until                            time.Time
}

// linkSwitchEnded is how a computer's last switch ended (step), with the manager that began it and its code's hash.
type linkSwitchEnded struct{ operation, step, manager, pairHash string }

// errLinkSwitchHeld: a move or a removal of the computer, or another switch of it, is under way.
var errLinkSwitchHeld = errors.New("the computer is being moved, removed or switched")

// claim makes r computer mid's switch under way, unless a move or a removal holds it or another switch does.
func (s *linkSwitches) claim(mid string, r *linkSwitchRun, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reserved[mid] > 0 {
		return errLinkSwitchHeld
	}
	if old := s.run[mid]; old != nil && now.Before(old.until) {
		return errLinkSwitchHeld
	}
	if s.run == nil {
		s.run = map[string]*linkSwitchRun{}
	}
	s.run[mid] = r
	return nil
}

// begun records the code of switch op of computer mid.
func (s *linkSwitches) begun(mid, op, pairHash string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.run[mid]; r != nil && r.operation == op {
		r.pairHash = pairHash
	}
}

// finish records that switch e.operation of computer mid ended (e.step).
func (s *linkSwitches) finish(mid string, e linkSwitchEnded) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.run[mid]; r != nil && r.operation == e.operation {
		delete(s.run, mid)
	}
	if s.ended == nil {
		s.ended = map[string]linkSwitchEnded{}
	}
	s.ended[mid] = e
}

// drop forgets switch op of computer mid, which never got as far as the audit.
func (s *linkSwitches) drop(mid, op string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.run[mid]; r != nil && r.operation == op {
		delete(s.run, mid)
	}
}

// live: a switch of computer mid is under way here and its time has not run out.
func (s *linkSwitches) live(mid string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r := s.run[mid]
	return r != nil && now.Before(r.until)
}

// hold keeps the computers from a begin until release; errLinkSwitchInProgress when one of them switches here now.
func (s *linkSwitches) hold(mids []string, now time.Time) (func(), error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, mid := range mids {
		if r := s.run[mid]; r != nil && now.Before(r.until) {
			return nil, errLinkSwitchInProgress
		}
	}
	if s.reserved == nil {
		s.reserved = map[string]int{}
	}
	for _, mid := range mids {
		s.reserved[mid]++
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			s.mu.Lock()
			defer s.mu.Unlock()
			for _, mid := range mids {
				if s.reserved[mid]--; s.reserved[mid] <= 0 {
					delete(s.reserved, mid)
				}
			}
		})
	}, nil
}

// linkSwitchAudit reads the switches' rows of the computers' audit (db.DB; machine_trust.go writes them). A store
// without it keeps no audit: this hub's memory is all there is then.
type linkSwitchAudit interface {
	LinkSwitchLatest(ctx context.Context, machine string) (*db.LinkSwitchRow, error)
	LinkSwitchHistory(ctx context.Context, machine, operation string) ([]db.LinkSwitchRow, error)
}

func (h *Hub) switchAudit() linkSwitchAudit {
	a, _ := h.store.(linkSwitchAudit)
	return a
}

// linkSwitchOp is what the hub knows of one switch: whether it began here (its begin row), whether it is the
// computer's latest, the manager that began it, its code's hash once it made one, and how it ended ("": it did not).
type linkSwitchOp struct {
	known, latest, begun     bool
	manager, pairHash, ended string
}

// linkSwitchOpOf is switch op of computer mid: from this hub's memory, else from the audit (this hub restarted since
// it began, or let go of its end).
func (h *Hub) linkSwitchOpOf(ctx context.Context, mid, op string) (linkSwitchOp, error) {
	s := &h.linkSwitch
	s.mu.Lock()
	run, ended := s.run[mid], s.ended[mid]
	var st linkSwitchOp
	switch {
	case run != nil && run.operation == op:
		st = linkSwitchOp{known: true, latest: true, begun: run.pairHash != "", manager: run.manager, pairHash: run.pairHash}
	case run == nil && ended.operation == op:
		st = linkSwitchOp{known: true, latest: true, begun: ended.pairHash != "", manager: ended.manager, pairHash: ended.pairHash,
			ended: ended.step}
	}
	newer := run != nil && run.operation != op // a switch begun after it
	s.mu.Unlock()
	audit := h.switchAudit()
	if st.known || audit == nil {
		return st, nil
	}
	history, err := audit.LinkSwitchHistory(ctx, mid, op)
	if err != nil {
		return st, err
	}
	for _, r := range history {
		if st.manager == "" {
			st.manager = strings.TrimPrefix(r.Actor, "manager:")
		}
		switch {
		case r.Step == switchBegin:
			st.known = true
		case r.Step == switchBegun:
			st.known, st.begun, st.pairHash = true, true, r.PairHash
		case switchEnded(r.Step) && st.ended == "":
			st.ended = r.Step
		}
	}
	if st.known && !newer {
		latest, err := audit.LinkSwitchLatest(ctx, mid)
		if err != nil {
			return st, err
		}
		st.latest = latest != nil && latest.Operation == op
	}
	return st, nil
}

// linkSwitchAudited: the audit has a switch of computer mid under way: its latest row is no end, and that switch
// began less than linkSwitchFor ago. What holds a computer across this hub's restart.
func (h *Hub) linkSwitchAudited(ctx context.Context, mid string) (bool, error) {
	audit := h.switchAudit()
	if audit == nil {
		return false, nil
	}
	latest, err := audit.LinkSwitchLatest(ctx, mid)
	if err != nil || latest == nil || switchEnded(latest.Step) {
		return false, err
	}
	began := latest.At
	history, err := audit.LinkSwitchHistory(ctx, mid, latest.Operation)
	if err != nil {
		return false, err
	}
	if len(history) > 0 {
		began = history[0].At
	}
	return h.linkSwitch.clock().Sub(began) < linkSwitchFor, nil
}

// linkSwitchUnderWay: a switch holds computer mid now (this hub's, or one from before its restart).
func (h *Hub) linkSwitchUnderWay(ctx context.Context, mid string) (bool, error) {
	if h.linkSwitch.live(mid, h.linkSwitch.clock()) {
		return true, nil
	}
	return h.linkSwitchAudited(ctx, mid)
}

// holdLinkSwitch keeps the computers from beginning a switch while a move or a removal of them runs: release when it
// is done. errLinkSwitchInProgress when one of them switches now.
func (h *Hub) holdLinkSwitch(ctx context.Context, mids ...string) (func(), error) {
	for _, mid := range mids {
		busy, err := h.linkSwitchAudited(ctx, mid)
		if err != nil {
			return nil, err
		}
		if busy {
			return nil, errLinkSwitchInProgress
		}
	}
	// A begin since is in memory: hold sees it, or it sees the hold.
	return h.linkSwitch.hold(mids, h.linkSwitch.clock())
}

// holdManagerLinkSwitch is holdLinkSwitch for every computer manager id carries (its removal hands them to the
// carrier).
func (h *Hub) holdManagerLinkSwitch(ctx context.Context, id string) (func(), error) {
	var mids []string
	for _, mc := range h.knownEntries() {
		if h.entryOwner(mc.ID) == id {
			mids = append(mids, mc.ID)
		}
	}
	return h.holdLinkSwitch(ctx, mids...)
}

func linkSwitchRefusal(code, msg string) *rpc.Error { return &rpc.Error{Code: code, Message: msg} }

// linkSwitchNoMachine: the computer is none of this manager's (gone, or another manager's now).
func linkSwitchNoMachine() *rpc.Error {
	return linkSwitchRefusal(devicewire.LinkSwitchCodeNoMachine, "no such computer of this manager")
}

// linkSwitchEntryGone: computer mid has no entry a rollback could put back to ssh any more (removed from hub.json, or
// from the list of the manager that kept it), though the running hub may still have it until its restart.
func (h *Hub) linkSwitchEntryGone(mid string) bool {
	if h.knownEntry(mid) == nil {
		return true
	}
	if h.cfgEntry(mid) == nil {
		return false // its manager keeps it
	}
	entry, _, err := hubJSONEntry(h.cfg.dir, mid)
	return err == nil && entry == nil
}

// linkSwitchGone: the switch is undone (its code and credential revoked, its end recorded), and the computer is no
// longer this manager's: the rollback is taken all the same.
func linkSwitchGone() *rpc.Error {
	return linkSwitchRefusal(devicewire.LinkSwitchCodeRolledBackGone, "the switch is undone; the computer is no longer this manager's")
}

func linkSwitchInvalid(msg string) *rpc.Error {
	return &rpc.Error{Code: bus.CodeInvalidParams, Message: msg}
}

// What an entry's compare-and-swap finds instead of what it expected (swapMachineKind).
var (
	errSwapKind = errors.New("the computer's entry is not of the kind expected")
	errSwapHost = errors.New("the computer's ssh_host is not the one expected")
	// errSwapDone: the entry already is what the swap would make it; nothing is written.
	errSwapDone = errors.New("the computer's entry already has that kind")
)

func init() {
	handleTypedBus(devicewire.LinkSwitchBeginMethod, devicewire.MethodLinkSwitchBegin+": ", (*Hub).beginLinkSwitch)
	handleTypedBus(devicewire.LinkSwitchEndMethod, devicewire.MethodLinkSwitchEnd+": ", func(h *Hub, ctx context.Context, c *busConn, p devicewire.LinkSwitchEnd) (buswire.None, error) {
		return buswire.None{}, h.endLinkSwitch(ctx, c, p)
	})
}

// beginLinkSwitch is devices.link_switch.begin from manager c.
func (h *Hub) beginLinkSwitch(ctx context.Context, c *busConn, p devicewire.LinkSwitchBegin) (devicewire.LinkSwitchBegun, error) {
	mid := p.MachineID
	run := &linkSwitchRun{operation: clip(p.Operation, 64), by: clip(p.By, 16), manager: c.managerID()}
	if run.operation == "" {
		return devicewire.LinkSwitchBegun{}, linkSwitchInvalid(devicewire.MethodLinkSwitchBegin + ": operation is required")
	}
	h.machineAdd.Lock() // as a rename: the entry is read and written whole, one change at a time; and one end at a time
	defer h.machineAdd.Unlock()
	if err := ctx.Err(); err != nil {
		return devicewire.LinkSwitchBegun{}, err // the manager stopped waiting while this waited for its turn: nothing begins
	}
	if err := h.linkSwitchReady(ctx, c, p, run.operation); err != nil {
		return devicewire.LinkSwitchBegun{}, err
	}
	now := h.linkSwitch.clock()
	run.until = now.Add(linkSwitchFor)
	if err := h.linkSwitch.claim(mid, run, now); err != nil {
		return devicewire.LinkSwitchBegun{}, linkSwitchRefusal(devicewire.LinkSwitchCodeChanged, "the computer is being moved, removed or switched")
	}
	// Asked, before anything changes: a hub that stops from here on finds the switch in the audit.
	if err := h.linkSwitchRecord(ctx, c, mid, run.operation, run.by, switchBegin, nil); err != nil {
		h.linkSwitch.drop(mid, run.operation)
		return devicewire.LinkSwitchBegun{}, fmt.Errorf("the switch's audit: %w", err)
	}
	err := h.swapMachineKind(ctx, mid, kindLink, func(mc MachineConfig) error {
		switch {
		case kindOf(mc) != "ssh":
			return errSwapKind
		case mc.SSHHost != p.SSHHost:
			return errSwapHost
		}
		return nil
	})
	if err != nil {
		h.beginFailed(ctx, c, mid, run)
		return devicewire.LinkSwitchBegun{}, linkSwitchSwapErr(err)
	}
	code, err := h.newPairCode(ctx, mid)
	if err != nil {
		h.undoBegin(ctx, c, mid, run, "")
		return devicewire.LinkSwitchBegun{}, fmt.Errorf("the computer's pairing code: %w", err)
	}
	hash := hashToken(code.Token)
	if err := h.linkSwitchRecord(ctx, c, mid, run.operation, run.by, switchBegun, map[string]any{"pair_hash": hash}); err != nil {
		h.undoBegin(ctx, c, mid, run, hash)
		return devicewire.LinkSwitchBegun{}, fmt.Errorf("the switch's audit: %w", err)
	}
	h.linkSwitch.begun(mid, run.operation, hash)
	h.logf("[%s] link switch %s begun (asked by %s, through %s)", mid, run.operation, run.by, run.manager)
	return devicewire.LinkSwitchBegun{Link: code.PairURL, PairHash: hash}, nil
}

// undoBegin takes back a begin that could not finish: its code (hash, when it made one) and its kind. When either
// fails the switch stays under way, its code's hash with it, for the manager's rollback (it was told begin failed) to
// finish. h.machineAdd is held.
func (h *Hub) undoBegin(ctx context.Context, c *busConn, mid string, run *linkSwitchRun, hash string) {
	ctx = context.WithoutCancel(ctx)
	if hash != "" {
		h.linkSwitch.begun(mid, run.operation, hash)
		id, _, err := h.store.RevokeLinkPair(ctx, hash, mid)
		if err != nil {
			h.logf("[%s] link switch %s: its begin failed, and its code could not be revoked: %v", mid, run.operation, err)
			return
		}
		if id != "" {
			h.revoked(id)
		}
	}
	if err := h.swapMachineKind(ctx, mid, "ssh", linkSwitchBack); err != nil && !errors.Is(err, errSwapDone) {
		h.logf("[%s] link switch %s: its begin failed, and its kind could not be put back to ssh: %v", mid, run.operation, err)
		return
	}
	h.beginFailed(ctx, c, mid, run)
}

// beginFailed ends a begin that changed nothing in the end (begin_failed). h.machineAdd is held.
func (h *Hub) beginFailed(ctx context.Context, c *busConn, mid string, run *linkSwitchRun) {
	if err := h.linkSwitchRecord(context.WithoutCancel(ctx), c, mid, run.operation, run.by, switchBeginFailed, nil); err != nil {
		h.logf("[%s] link switch %s: its begin changed nothing, which the audit could not record: %v", mid, run.operation, err)
	}
	h.linkSwitch.finish(mid, linkSwitchEnded{run.operation, switchBeginFailed, run.manager, run.pairHash})
}

// linkSwitchReady refuses a begin the hub cannot do now (devicewire's LinkSwitchCode*). h.machineAdd is held.
func (h *Hub) linkSwitchReady(ctx context.Context, c *busConn, p devicewire.LinkSwitchBegin, op string) error {
	mid := p.MachineID
	if mid == "" || h.ownsMachine(c, mid) != nil {
		return linkSwitchNoMachine()
	}
	mc := h.knownEntry(mid)
	switch {
	case mc == nil:
		return linkSwitchNoMachine()
	case kindOf(*mc) != "ssh":
		return linkSwitchRefusal(devicewire.LinkSwitchCodeNotSSH, "the computer is not reached over ssh")
	case mc.SSHHost != p.SSHHost:
		return linkSwitchRefusal(devicewire.LinkSwitchCodeChanged, "the computer's entry changed")
	case h.machineUntrusted(mid):
		return linkSwitchRefusal(devicewire.LinkSwitchCodePending, i18n.T(h.userLang(), "hub-machine_trust.pending"))
	case !h.linkPairable():
		return linkSwitchRefusal(devicewire.LinkSwitchCodeUnavailable, i18n.T(h.userLang(), "hub-pairing.tls_required"))
	case h.store == nil:
		return errors.New("no database")
	}
	inUse, err := h.store.MachineLinkInUse(ctx, mid)
	if err != nil {
		return err
	}
	if inUse {
		return linkSwitchRefusal(devicewire.LinkSwitchCodeExistingLink, "the computer has a Persona Link credential or pairing code already")
	}
	if audit := h.switchAudit(); audit != nil {
		if used, err := audit.LinkSwitchHistory(ctx, mid, op); err != nil {
			return err
		} else if len(used) > 0 {
			return linkSwitchInvalid("this operation id was used before")
		}
	}
	if busy, err := h.linkSwitchAudited(ctx, mid); err != nil {
		return err
	} else if busy {
		return linkSwitchRefusal(devicewire.LinkSwitchCodeChanged, "another switch of the computer is under way")
	}
	return nil
}

// linkSwitchBack is end's compare-and-swap: kind link goes back to ssh; ssh is so already.
func linkSwitchBack(mc MachineConfig) error {
	switch kindOf(mc) {
	case kindLink:
		return nil
	case "ssh":
		return errSwapDone
	}
	return errSwapKind
}

// linkSwitchSwapErr is what a refused compare-and-swap answers the manager.
func linkSwitchSwapErr(err error) error {
	var re *rpc.Error
	switch {
	case errors.Is(err, errNoMachine), errors.As(err, &re) && re.Code == devicewire.CodeNoDevice:
		return linkSwitchNoMachine()
	case errors.Is(err, errSwapKind):
		return linkSwitchRefusal(devicewire.LinkSwitchCodeNotSSH, "the computer's entry is not of the kind expected")
	case errors.Is(err, errSwapHost):
		return linkSwitchRefusal(devicewire.LinkSwitchCodeChanged, "the computer's entry changed")
	}
	return err
}

// endLinkSwitch is devices.link_switch.end from manager c, for the computer's latest switch op only, one at a time
// with begin. An end is final: once one is recorded, the same answers again and nothing changes (a switch that
// succeeded answers a rollback already_succeeded); an operation the hub never saw begin, or one a later switch came
// after, changes nothing either. A rollback's hash must be that switch's code's.
func (h *Hub) endLinkSwitch(ctx context.Context, c *busConn, p devicewire.LinkSwitchEnd) error {
	mid, op := p.MachineID, clip(p.Operation, 64)
	rollBack := p.Outcome == devicewire.LinkSwitchRolledBack
	switch {
	case p.Outcome != devicewire.LinkSwitchSucceeded && !rollBack:
		return linkSwitchInvalid(devicewire.MethodLinkSwitchEnd + ": unknown outcome")
	case mid == "" || op == "":
		return linkSwitchInvalid(devicewire.MethodLinkSwitchEnd + ": machine_id and operation are required")
	}
	h.machineAdd.Lock() // before what it reads: a begin under way is either done or not begun
	defer h.machineAdd.Unlock()
	st, err := h.linkSwitchOpOf(ctx, mid, op)
	if err != nil {
		return err
	}
	if h.ownsMachine(c, mid) != nil {
		return h.endMovedLinkSwitch(ctx, c, mid, op, p.PairHash, rollBack, st)
	}
	switch st.ended {
	case switchSucceeded:
		if rollBack {
			return linkSwitchRefusal(devicewire.LinkSwitchCodeSucceeded, "this switch succeeded; nothing is undone")
		}
		return nil
	case switchRolledBack, switchBeginFailed:
		switch {
		case !rollBack:
			return linkSwitchInvalid("this switch did not succeed: it was undone")
		case h.linkSwitchEntryGone(mid):
			return linkSwitchGone() // as the first answer said: the computer is gone since
		}
		return nil
	}
	if !st.known || !st.latest {
		if rollBack {
			h.logf("[%s] link switch %s rolled back, which is no switch under way here: nothing to undo", mid, op)
			return nil
		}
		return linkSwitchInvalid("no such switch under way")
	}
	if p.PairHash != "" && p.PairHash != st.pairHash {
		return linkSwitchInvalid("pair_hash is not this switch's code")
	}
	if !rollBack {
		if !st.begun {
			return linkSwitchInvalid("this switch made no pairing code")
		}
		if err := h.linkSwitchRecord(ctx, c, mid, op, "", switchSucceeded, nil); err != nil {
			return fmt.Errorf("the switch's audit: %w", err)
		}
		h.linkSwitch.finish(mid, linkSwitchEnded{op, switchSucceeded, st.manager, st.pairHash})
		return nil
	}
	revoked, err := h.revokeLinkSwitch(ctx, mid, st.pairHash)
	if err != nil {
		return err
	}
	var refused error // the kind is not this switch's to put back: the switch is over all the same
	var re *rpc.Error
	switch err := h.swapMachineKind(ctx, mid, "ssh", linkSwitchBack); {
	case err == nil, errors.Is(err, errSwapDone):
	case errors.Is(err, errNoMachine), errors.As(err, &re) && re.Code == devicewire.CodeNoDevice:
		refused = linkSwitchGone() // removed meanwhile: undone all the same
	case errors.Is(err, errSwapKind):
		refused = linkSwitchSwapErr(err)
	default:
		return err // tried again: what was revoked stays revoked
	}
	if err := h.linkSwitchRecord(ctx, c, mid, op, "", switchRolledBack, map[string]any{"credentials_revoked": revoked}); err != nil {
		return fmt.Errorf("the switch's audit: %w", err)
	}
	h.linkSwitch.finish(mid, linkSwitchEnded{op, switchRolledBack, st.manager, st.pairHash})
	h.logf("[%s] link switch %s rolled back: %d credentials revoked", mid, op, revoked)
	return refused
}

// endMovedLinkSwitch is an end from a manager that does not carry the computer (any more: it moved, or was removed,
// meanwhile). Only a rollback of the computer's latest switch that this manager began: under way and naming its code,
// that code and the credential it became are revoked (the kind is not this manager's to put back) and the switch is
// over; then, and for a repeat once it is over (undone here, or by its begin), rolled_back_gone: the rollback is taken.
// Anything else changes nothing: no_machine. h.machineAdd is held.
func (h *Hub) endMovedLinkSwitch(ctx context.Context, c *busConn, mid, op, pairHash string, rollBack bool, st linkSwitchOp) error {
	if !rollBack || !st.known || !st.latest || st.manager != c.managerID() || pairHash != "" && pairHash != st.pairHash {
		return linkSwitchNoMachine()
	}
	switch st.ended {
	case switchRolledBack, switchBeginFailed:
		return linkSwitchGone()
	case "":
	default: // it succeeded: what it made stays
		return linkSwitchNoMachine()
	}
	if pairHash == "" { // never all of a computer that is another manager's now
		return linkSwitchNoMachine()
	}
	revoked, err := h.revokeLinkSwitch(ctx, mid, pairHash)
	if err != nil {
		return err
	}
	if err := h.linkSwitchRecord(ctx, c, mid, op, "", switchRolledBack, map[string]any{"credentials_revoked": revoked, "moved": true}); err != nil {
		return fmt.Errorf("the switch's audit: %w", err)
	}
	h.linkSwitch.finish(mid, linkSwitchEnded{op, switchRolledBack, st.manager, st.pairHash})
	h.logf("[%s] link switch %s rolled back after the computer left its manager: %d credentials revoked", mid, op, revoked)
	return linkSwitchGone()
}

// revokeLinkSwitch revokes what a switch of computer mid made: the pairing code whose hash is pairHash and the
// credential it became; with no hash (the hub stopped before it recorded the code) every Persona Link credential of
// the computer and its codes not used yet, while its entry is kind link (begin refused a computer that had any). It
// closes what they have open (revoked); the computer's share records stay, as it does, on ssh. How many credentials.
func (h *Hub) revokeLinkSwitch(ctx context.Context, mid, pairHash string) (int, error) {
	if h.store == nil {
		return 0, errors.New("no database")
	}
	if pairHash != "" {
		id, _, err := h.store.RevokeLinkPair(ctx, pairHash, mid)
		if err != nil || id == "" {
			return 0, err
		}
		h.revoked(id)
		return 1, nil
	}
	if mc := h.knownEntry(mid); mc == nil || mc.Kind != kindLink {
		return 0, nil // not on Persona Link: what it has of one is not this switch's
	}
	cr, _ := h.store.(credentialRevoker)
	if cr == nil {
		return 0, errors.New("this store cannot revoke a computer's credentials")
	}
	ids, err := cr.RevokeMachineCredentials(ctx, mid)
	if err != nil {
		return 0, err
	}
	for _, id := range ids {
		h.revoked(id)
	}
	return len(ids), nil
}

// linkSwitchRecord writes one row of the computer's audit about switch op, before what goes by it: its failure is
// the caller's. A store that keeps no audit keeps none (this hub's memory is all there is then). Never the code.
func (h *Hub) linkSwitchRecord(ctx context.Context, c *busConn, mid, op, by, step string, more map[string]any) error {
	ts := h.trustStore()
	if ts == nil {
		return nil
	}
	detail := map[string]any{"operation": op, "step": step}
	if by != "" {
		detail["by"] = by
	}
	for k, v := range more {
		detail[k] = v
	}
	b, _ := json.Marshal(detail)
	return ts.AddMachineAudit(context.WithoutCancel(ctx), mid, "link_switch", "manager:"+c.managerID(), b)
}

// linkSwitchRoute is the apps' start of a switch (devicewire.Routes): a signed-in full device's only (deviceRoutesRelayed).
const linkSwitchRoute = "POST /api/v1/machines/{mid}/link-switch"

// errLinkSwitchInProgress: the computer is switching to Persona Link; it is neither moved to another manager nor
// removed until the switch ends (its manager runs it, and undoes it when it fails).
var errLinkSwitchInProgress = refuseAdd(http.StatusConflict, "link_switch_in_progress", i18n.Err("hub-link_switch.in_progress"))

// swapMachineKind sets computer mid's entry kind to kind, every other key as it was, when check (given the entry as
// it is now) says yes: in its manager's own list for one it keeps (machines_kept.go), else in hub.json, then in the
// running hub at once (kindLive). check's error is the answer, nothing written. h.machineAdd is held.
func (h *Hub) swapMachineKind(ctx context.Context, mid, kind string, check func(MachineConfig) error) error {
	decoded := func(raw json.RawMessage) error {
		var mc MachineConfig
		if err := json.Unmarshal(raw, &mc); err != nil {
			return err
		}
		return check(mc)
	}
	if kept, err := h.editKeptMachineIf(ctx, mid, decoded, map[string]any{"kind": kind}); kept {
		if err == nil {
			h.broadcastMachines() // its manager replaces the machine; its next report shows it so
		}
		return err
	}
	err := editHubJSON(h.cfg.dir, func(o *jsonObject) error {
		list, err := machineEntries(o)
		if err != nil {
			return err
		}
		for _, raw := range list {
			if machineEntryID(raw) == mid {
				if err := decoded(raw); err != nil {
					return err
				}
				return setMachineField(o, mid, "kind", kind)
			}
		}
		return errNoMachine
	})
	if err != nil {
		return err
	}
	h.kindLive(mid, kind)
	return nil
}

// kindLive takes a hub.json computer's new kind into the running hub, as renameMachineLive a name: the entry it hands
// the manager (which connects it the new way now), and so what the apps see once the manager reports it. No restart
// waits for it (pendingRestart).
func (h *Hub) kindLive(mid, kind string) {
	h.editEntryLive(mid, func(mc *MachineConfig) { mc.Kind = kind })
	h.devicesConfigChanged()
	h.broadcastMachines() // and the mesh record
}

// adminLinkSwitchRoutes: the admin page's Machines switches a computer to Persona Link. The manager that connects it
// answers, as for a device's POST /api/v1/machines/{mid}/link-switch (devicewire.Routes), told that a signed-in admin
// session asks (assistantwire.AdminHeader). mux is the admin's signed-in routes: the session and the write's CSRF
// token are checked before (admin.go).
func (h *Hub) adminLinkSwitchRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/v1/admin/machines/{mid}/link-switch", func(w http.ResponseWriter, r *http.Request) {
		mid := r.PathValue("mid")
		owner := h.machineOwner(mid)
		_ = h.waitDevicesFor(r.Context(), owner) // the hub just started: the manager is on its way (bounded), and its machines
		if h.localMachine(mid) == nil {
			writeErr(w, http.StatusNotFound, i18n.Err("devices-api.unknown_machine"))
			return
		}
		r = r.Clone(r.Context())
		r.URL.Path = "/api/v1/machines/" + mid + "/link-switch"
		r.URL.RawPath = "/api/v1/machines/" + url.PathEscape(mid) + "/link-switch"
		r.Header.Del("X-Persona-CSRF") // the admin session's own, not the manager's business
		h.forwardAdminHTTP(w, r, owner)
	})
}
