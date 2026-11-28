package db

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/nwverse/persona/server/internal/conversationlog"
	"github.com/nwverse/persona/server/internal/proto"
)

var (
	ErrConversationEpoch      = errors.New("conversation activation changed")
	ErrConversationSwitchBusy = errors.New("conversation switch is unfinished")
)

type ConversationSwitch struct {
	OperationID   string          `json:"operation_id"`
	ExpectedEpoch int64           `json:"expected_epoch"`
	SourceSegment string          `json:"source_segment"`
	TargetSegment string          `json:"target_segment,omitempty"`
	Request       json.RawMessage `json:"request"`
	Phase         string          `json:"phase"`
	Error         string          `json:"error,omitempty"`
	CreatedAt     time.Time       `json:"-"`
	UpdatedAt     time.Time       `json:"-"`
}

type LogicalConversation struct {
	MachineID         string                    `json:"machine_id"`
	SessionID         string                    `json:"session_id"`
	Epoch             int64                     `json:"epoch"`
	History           string                    `json:"history"`
	ProjectionVersion int64                     `json:"projection_version"`
	Revision          int64                     `json:"revision"`
	Deleted           bool                      `json:"deleted"`
	Segments          []conversationlog.Segment `json:"segments"`
	Switch            *ConversationSwitch       `json:"switch,omitempty"`
}

func (c LogicalConversation) Active() (conversationlog.Segment, bool) {
	for _, s := range c.Segments {
		if s.State == "active" {
			return s, true
		}
	}
	return conversationlog.Segment{}, false
}

type conversationQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

// A physical alias is resolved only inside the manager. It grants no caller authority.
func readLogicalConversation(ctx context.Context, q conversationQuerier, machine, session string) (*LogicalConversation, error) {
	c := &LogicalConversation{}
	err := q.QueryRow(ctx, `SELECT c.machine_id,c.session_id,c.epoch,c.history,c.projection_version,c.revision,c.deleted_at IS NOT NULL
	 FROM logical_conversations c WHERE (c.machine_id=$1 AND c.session_id=$2) OR EXISTS
	 (SELECT 1 FROM conversation_segments s WHERE s.logical_machine=c.machine_id AND s.logical_session=c.session_id AND s.machine_id=$1 AND s.session_id=$2)`, machine, session).
		Scan(&c.MachineID, &c.SessionID, &c.Epoch, &c.History, &c.ProjectionVersion, &c.Revision, &c.Deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT id,ordinal,machine_id,session_id,engine,launcher,model,effort,permission_mode,source_history,source_revision,seq_offset,frozen_end,state,close_pending
	 FROM conversation_segments WHERE logical_machine=$1 AND logical_session=$2 ORDER BY ordinal`, c.MachineID, c.SessionID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var s conversationlog.Segment
		if err = rows.Scan(&s.ID, &s.Ordinal, &s.MachineID, &s.SessionID, &s.Engine, &s.Launcher, &s.Model, &s.Effort, &s.PermissionMode, &s.SourceHistory, &s.SourceRevision, &s.Offset, &s.FrozenEnd, &s.State, &s.ClosePending); err != nil {
			rows.Close()
			return nil, err
		}
		c.Segments = append(c.Segments, s)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var op ConversationSwitch
	var request []byte
	err = q.QueryRow(ctx, `SELECT operation_id,expected_epoch,source_segment,coalesce(target_segment,''),request,phase,error,created_at
	 FROM conversation_switches WHERE logical_machine=$1 AND logical_session=$2 AND phase IN ('preparing','prepared','active','in_doubt')`, c.MachineID, c.SessionID).
		Scan(&op.OperationID, &op.ExpectedEpoch, &op.SourceSegment, &op.TargetSegment, &request, &op.Phase, &op.Error, &op.CreatedAt)
	if err == nil {
		op.Request = json.RawMessage(request)
		c.Switch = &op
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	return c, nil
}

func (d *DB) LogicalConversation(ctx context.Context, machine, session string) (*LogicalConversation, error) {
	if d.sets&ManagerSet == 0 {
		return nil, nil
	}
	var out *LogicalConversation
	err := pgx.BeginTxFunc(ctx, d.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly}, func(tx pgx.Tx) error {
		var err error
		out, err = readLogicalConversation(ctx, tx, machine, session)
		return err
	})
	return out, err
}

func (d *DB) LogicalConversations(ctx context.Context) ([]LogicalConversation, error) {
	if d.sets&ManagerSet == 0 {
		return nil, nil
	}
	rows, err := d.pool.Query(ctx, `SELECT machine_id,session_id FROM logical_conversations WHERE deleted_at IS NULL ORDER BY machine_id,session_id`)
	if err != nil {
		return nil, err
	}
	var keys [][2]string
	for rows.Next() {
		var k [2]string
		if err = rows.Scan(&k[0], &k[1]); err != nil {
			rows.Close()
			return nil, err
		}
		keys = append(keys, k)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	var out []LogicalConversation
	for _, k := range keys {
		c, e := d.LogicalConversation(ctx, k[0], k[1])
		if e != nil {
			return nil, e
		}
		if c != nil {
			out = append(out, *c)
		}
	}
	return out, nil
}

func (d *DB) AdoptConversation(ctx context.Context, machine, session, segment, engine, history string) error {
	return d.AdoptConversationAliases(ctx, machine, session, segment, engine, history, nil)
}

// Authenticated physical aliases must be adopted as one identity. Sorted advisory
// locks serialize concurrent first adoption even before either logical row exists.
// Aliases are manager-supplied device configuration, never client input.
func (d *DB) AdoptConversationAliases(ctx context.Context, machine, session, segment, engine, history string, aliases []string) error {
	if machine == "" || session == "" || segment == "" || engine == "" || history == "" {
		return errors.New("incomplete conversation identity")
	}
	aliases = append(slices.Clone(aliases), machine)
	slices.Sort(aliases)
	aliases = slices.Compact(aliases)
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		for _, alias := range aliases {
			if alias == "" {
				continue
			}
			key := "persona.logical.adopt:" + conversationDeletionKey(alias, session)
			if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, key); err != nil {
				return err
			}
		}
		for _, alias := range aliases {
			if alias == "" {
				continue
			}
			if err := conversationAvailableTx(ctx, tx, alias, session); err != nil {
				return err
			}
			c, err := readLogicalConversation(ctx, tx, alias, session)
			if err != nil {
				return err
			}
			if c != nil {
				if c.Deleted {
					return ErrConversationDeleted
				}
				if c.MachineID != machine || c.SessionID != session {
					return ErrConversationEpoch
				}
			}
		}
		_, err := tx.Exec(ctx, `INSERT INTO logical_conversations(machine_id,session_id,history,revision) VALUES($1,$2,$3,coalesce((SELECT revision FROM conversation_histories WHERE machine_id=$1 AND session_id=$2),0)) ON CONFLICT DO NOTHING`, machine, session, history)
		if err != nil {
			return err
		}
		var deleted bool
		if err = tx.QueryRow(ctx, `SELECT deleted_at IS NOT NULL FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 FOR UPDATE NOWAIT`, machine, session).Scan(&deleted); err != nil {
			return err
		}
		if deleted {
			return ErrConversationDeleted
		}
		_, err = tx.Exec(ctx, `INSERT INTO conversation_segments(id,logical_machine,logical_session,ordinal,machine_id,session_id,engine,source_history,seq_offset,state)
   VALUES($1,$2,$3,1,$2,$3,$4,$5,0,'active') ON CONFLICT(logical_machine,logical_session,ordinal) DO NOTHING`, segment, machine, session, engine, history)
		return err
	})
}

// The shared database lock fences activation/deletion across overlapping managers,
// including the actual bounded send or tool action, not just a cached lookup.
func (d *DB) WithConversationEpoch(ctx context.Context, machine, session string, epoch int64, fn func(*LogicalConversation) error) error {
	return d.withConversationEpoch(ctx, machine, session, epoch, false, fn)
}

// WithDrainedConversationEpoch runs a background delivery after earlier owner input has settled.
func (d *DB) WithDrainedConversationEpoch(ctx context.Context, machine, session string, epoch int64, fn func(*LogicalConversation) error) error {
	return d.withConversationEpoch(ctx, machine, session, epoch, true, fn)
}

func (d *DB) withConversationEpoch(ctx context.Context, machine, session string, epoch int64, requireDrained bool, fn func(*LogicalConversation) error) error {
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		var current int64
		var deleted bool
		err := tx.QueryRow(ctx, `SELECT epoch,deleted_at IS NOT NULL FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 FOR SHARE`, machine, session).Scan(&current, &deleted)
		if err != nil {
			return err
		}
		if deleted {
			return ErrConversationDeleted
		}
		if current != epoch {
			return ErrConversationEpoch
		}
		c, err := readLogicalConversation(ctx, tx, machine, session)
		if err != nil {
			return err
		}
		if requireDrained {
			pending, err := conversationInputPending(ctx, tx, machine, session)
			if err != nil {
				return err
			}
			if c.Switch != nil || pending {
				return ErrConversationSwitchBusy
			}
		}
		return fn(c)
	})
}

func (d *DB) BeginConversationSwitch(ctx context.Context, machine, session string, op ConversationSwitch) (result error) {
	defer func() {
		var pe *pgconn.PgError
		if errors.As(result, &pe) && pe.Code == "55P03" {
			result = ErrConversationSwitchBusy
		}
	}()
	if op.OperationID == "" || len(op.OperationID) > 128 || !json.Valid(op.Request) || len(op.Request) > 65536 {
		return errors.New("invalid switch operation")
	}
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		var epoch int64
		var deleted bool
		if err := tx.QueryRow(ctx, `SELECT epoch,deleted_at IS NOT NULL FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 FOR UPDATE NOWAIT`, machine, session).Scan(&epoch, &deleted); err != nil {
			return err
		}
		if deleted {
			return ErrConversationDeleted
		}
		var old []byte
		var expected int64
		err := tx.QueryRow(ctx, `SELECT request,expected_epoch FROM conversation_switches WHERE logical_machine=$1 AND logical_session=$2 AND operation_id=$3`, machine, session, op.OperationID).Scan(&old, &expected)
		if err == nil {
			if expected != op.ExpectedEpoch || !bytes.Equal(old, op.Request) {
				return ErrConversationEpoch
			}
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if epoch != op.ExpectedEpoch {
			return ErrConversationEpoch
		}
		var unfinished bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_switches WHERE logical_machine=$1 AND logical_session=$2 AND phase IN ('preparing','prepared','active','in_doubt'))`, machine, session).Scan(&unfinished); err != nil {
			return err
		}
		if unfinished {
			return ErrConversationSwitchBusy
		}
		var pending bool
		if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_interaction_routes r JOIN interactions i ON i.id=r.interaction_id WHERE r.logical_machine=$1 AND r.logical_session=$2 AND i.status='pending')`, machine, session).Scan(&pending); err != nil {
			return err
		}
		if pending {
			return ErrConversationSwitchBusy
		}
		if err = requireNoUnresolvedConversationInput(ctx, tx, machine, session); err != nil {
			return err
		}
		var active string
		if err = tx.QueryRow(ctx, `SELECT id FROM conversation_segments WHERE logical_machine=$1 AND logical_session=$2 AND state='active'`, machine, session).Scan(&active); err != nil {
			return err
		}
		if active != op.SourceSegment {
			return ErrConversationEpoch
		}
		_, err = tx.Exec(ctx, `INSERT INTO conversation_switches(logical_machine,logical_session,operation_id,expected_epoch,source_segment,request,phase) VALUES($1,$2,$3,$4,$5,$6,'preparing')`, machine, session, op.OperationID, op.ExpectedEpoch, active, []byte(op.Request))
		return err
	})
}

func (d *DB) PrepareConversationSegment(ctx context.Context, machine, session, operation string, target conversationlog.Segment, frozen int) error {
	if _, err := target.Seq(0); err != nil {
		return err
	}
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		var epoch int64
		if err := tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 AND deleted_at IS NULL FOR UPDATE NOWAIT`, machine, session).Scan(&epoch); err != nil {
			return err
		}
		var source, phase string
		var expected int64
		if err := tx.QueryRow(ctx, `SELECT source_segment,phase,expected_epoch FROM conversation_switches WHERE logical_machine=$1 AND logical_session=$2 AND operation_id=$3`, machine, session, operation).Scan(&source, &phase, &expected); err != nil {
			return err
		}
		if expected != epoch || (phase != "preparing" && phase != "in_doubt") {
			return ErrConversationEpoch
		}
		var offset, ordinal int
		if err := tx.QueryRow(ctx, `SELECT seq_offset,ordinal FROM conversation_segments WHERE id=$1 AND state='active'`, source).Scan(&offset, &ordinal); err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, `SELECT max(ordinal) FROM conversation_segments WHERE logical_machine=$1 AND logical_session=$2`, machine, session).Scan(&ordinal); err != nil {
			return err
		}
		if frozen < 0 || offset >= conversationlog.MaxLogicalSeq-frozen {
			return conversationlog.ErrSequenceOverflow
		}
		_, err := tx.Exec(ctx, `UPDATE conversation_segments SET frozen_end=$2 WHERE id=$1`, source, frozen)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `INSERT INTO conversation_segments(id,logical_machine,logical_session,ordinal,machine_id,session_id,engine,source_history,seq_offset,state,launcher,model,effort,permission_mode)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,'prepared',$10,$11,$12,$13)`, target.ID, machine, session, ordinal+1, target.MachineID, target.SessionID, target.Engine, target.SourceHistory, offset+frozen, target.Launcher, target.Model, target.Effort, target.PermissionMode)
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE conversation_switches SET target_segment=$4,phase='prepared',updated_at=now() WHERE logical_machine=$1 AND logical_session=$2 AND operation_id=$3`, machine, session, operation, target.ID)
		return err
	})
}

func (d *DB) ActivateConversationSegment(ctx context.Context, machine, session, operation string, expected int64) error {
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		var epoch int64
		if err := tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 AND deleted_at IS NULL FOR UPDATE NOWAIT`, machine, session).Scan(&epoch); err != nil {
			return err
		}
		if epoch != expected {
			return ErrConversationEpoch
		}
		var source, target, phase string
		var opEpoch int64
		if err := tx.QueryRow(ctx, `SELECT source_segment,target_segment,phase,expected_epoch FROM conversation_switches WHERE logical_machine=$1 AND logical_session=$2 AND operation_id=$3`, machine, session, operation).Scan(&source, &target, &phase, &opEpoch); err != nil {
			return err
		}
		if phase != "prepared" || opEpoch != expected {
			return ErrConversationEpoch
		}
		if err := requireNoUnresolvedConversationInput(ctx, tx, machine, session); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE conversation_segments SET state='retired' WHERE id=$1 AND state='active'`, source); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `UPDATE conversation_segments SET state='active' WHERE id=$1 AND state='prepared'`, target)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConversationEpoch
		}
		tag, err = tx.Exec(ctx, `UPDATE logical_conversations SET epoch=epoch+1,revision=revision+1 WHERE machine_id=$1 AND session_id=$2 AND epoch=$3 AND deleted_at IS NULL`, machine, session, expected)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrConversationEpoch
		}
		_, err = tx.Exec(ctx, `UPDATE conversation_switches SET phase='active',updated_at=now() WHERE logical_machine=$1 AND logical_session=$2 AND operation_id=$3`, machine, session, operation)
		return err
	})
}

// AcceptIdleConversationSwitch accepts an activated batch operation only while
// no real input needs the first-turn delivery/rollback protocol. The input
// check and outcome share the logical row lock, so an input arriving at the
// boundary is either preserved for that protocol or queued after acceptance.
func (d *DB) AcceptIdleConversationSwitch(ctx context.Context, machine, session, operation string, expectedEpoch int64) (accepted bool, err error) {
	err = conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		var epoch int64
		if err := tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 AND deleted_at IS NULL FOR UPDATE NOWAIT`, machine, session).Scan(&epoch); err != nil {
			return err
		}
		var phase string
		var recordedExpected int64
		if err := tx.QueryRow(ctx, `SELECT phase,expected_epoch FROM conversation_switches WHERE logical_machine=$1 AND logical_session=$2 AND operation_id=$3`, machine, session, operation).Scan(&phase, &recordedExpected); err != nil {
			return err
		}
		if phase == "accepted" {
			accepted = true
			return nil
		}
		if phase != "active" || recordedExpected != expectedEpoch || epoch != expectedEpoch+1 {
			return ErrConversationEpoch
		}
		pending, err := conversationInputPending(ctx, tx, machine, session)
		if err != nil || pending {
			return err
		}
		// No input carried the handoff: it stays for the conversation's next one (ConversationPendingHandoff, #14488).
		if err := resolveConversationSwitch(ctx, tx, machine, session, operation, "accepted", "", switchSettlement{keepHandoff: true}); err != nil {
			return err
		}
		accepted = true
		return nil
	})
	return accepted, err
}

// ResolveConversationSwitch is a forward-only outcome. The source is marked for
// closure only after acceptance; a definite rejection restores it at a new epoch.
func (d *DB) ResolveConversationSwitch(ctx context.Context, machine, session, operation, outcome, reason string) error {
	if outcome != "accepted" && outcome != "rejected" && outcome != "in_doubt" {
		return errors.New("invalid switch outcome")
	}
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		return resolveConversationSwitchTx(ctx, tx, machine, session, operation, outcome, reason)
	})
}

func resolveConversationSwitchTx(ctx context.Context, tx pgx.Tx, machine, session, operation, outcome, reason string) error {
	return resolveConversationSwitch(ctx, tx, machine, session, operation, outcome, reason, switchSettlement{})
}

// ResolveConversationPreparation applies a failed or unconfirmed preparation only while the operation still
// owns preparation at its expected epoch. Another manager may have mapped or activated the target during the RPC.
func (d *DB) ResolveConversationPreparation(ctx context.Context, machine, session, operation, outcome, reason string) error {
	if outcome != "rejected" && outcome != "in_doubt" {
		return errors.New("invalid preparation outcome")
	}
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		return resolveConversationSwitch(ctx, tx, machine, session, operation, outcome, reason, switchSettlement{preparationOnly: true})
	})
}

type switchSettlement struct {
	keepHandoff     bool
	preparationOnly bool
	manual          bool
}

// An idle acceptance keeps its handoff for the conversation's next input. Accepting a switch discards
// the handoff an earlier idle acceptance kept for the newly retired source.
func resolveConversationSwitch(ctx context.Context, tx pgx.Tx, machine, session, operation, outcome, reason string, settlement switchSettlement) error {
	var epoch int64
	if err := tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 AND deleted_at IS NULL FOR UPDATE NOWAIT`, machine, session).Scan(&epoch); err != nil {
		return err
	}
	var source, target, phase string
	var expected int64
	if err := tx.QueryRow(ctx, `SELECT source_segment,coalesce(target_segment,''),phase,expected_epoch FROM conversation_switches WHERE logical_machine=$1 AND logical_session=$2 AND operation_id=$3`, machine, session, operation).Scan(&source, &target, &phase, &expected); err != nil {
		return err
	}
	if settlement.preparationOnly && (epoch != expected || phase != "preparing" && phase != "in_doubt") {
		return ErrConversationEpoch
	}
	if phase == outcome {
		return nil
	}
	if phase == "accepted" || phase == "rejected" {
		return ErrConversationEpoch
	}
	activated := phase == "active" || epoch == expected+1
	if activated && outcome == "in_doubt" {
		// A late preparation failure cannot undo activation. The reconciler
		// will observe the activated operation and finish or drain its input.
		return ErrConversationEpoch
	}
	if outcome == "accepted" {
		if !activated || epoch != expected+1 {
			return ErrConversationEpoch
		}
		if _, err := tx.Exec(ctx, `UPDATE conversation_segments SET close_pending=true WHERE id=$1 AND state='retired'`, source); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE conversation_switches SET handoff=NULL WHERE logical_machine=$1 AND logical_session=$2 AND operation_id<>$3 AND phase='accepted' AND handoff IS NOT NULL`, machine, session, operation); err != nil {
			return err
		}
	}
	if outcome == "rejected" {
		if target != "" {
			if _, err := tx.Exec(ctx, `UPDATE conversation_segments SET state='abandoned',frozen_end=0,close_pending=true WHERE id=$1`, target); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(ctx, `UPDATE conversation_segments SET state='active',frozen_end=NULL,close_pending=false WHERE id=$1`, source); err != nil {
			return err
		}
		if activated {
			// A definite first-turn refusal delivered no input. Restore its
			// original queue position for the recovered source, never retry
			// an ambiguous delivery on either engine.
			if _, err := tx.Exec(ctx, `UPDATE conversation_inputs SET state='queued' WHERE logical_machine=$1 AND logical_session=$2 AND epoch=$3 AND state='rejected' AND NOT $4::boolean`, machine, session, expected+1, settlement.manual); err != nil {
				return err
			}
			if epoch != expected+1 {
				return ErrConversationEpoch
			}
			// The abandoned target may already have published an empty span or
			// runtime metadata. A restored source can reuse those physical lines;
			// invalidate that projection before clients reconcile its backlog.
			if _, err := tx.Exec(ctx, `UPDATE logical_conversations SET epoch=epoch+1,revision=revision+1,projection_version=projection_version+1,history='logical:'||md5(history||$4||(epoch+1)::text) WHERE machine_id=$1 AND session_id=$2 AND epoch=$3`, machine, session, epoch, operation); err != nil {
				return err
			}
		}
	}
	_, err := tx.Exec(ctx, `UPDATE conversation_switches SET phase=$4,error=$5,handoff=CASE WHEN $4 IN ('accepted','rejected') AND NOT $6::boolean THEN NULL ELSE handoff END,updated_at=now() WHERE logical_machine=$1 AND logical_session=$2 AND operation_id=$3`, machine, session, operation, outcome, reason, settlement.keepHandoff)
	return err
}

// CancelConversationSwitch settles an operation that has not activated its target as rejected, which keeps the
// source segment active at the same epoch and abandons a prepared target for closure. It cancels nothing and reports
// false once the operation activated, settled, or the conversation moved past expectedEpoch: the logical row lock
// orders it with ActivateConversationSegment, so a concurrent activation either finds the switch rejected or
// wins and is left alone.
func (d *DB) CancelConversationSwitch(ctx context.Context, machine, session, operation string, expectedEpoch int64, reason string) (cancelled bool, err error) {
	err = conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		var epoch int64
		if err := tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 AND deleted_at IS NULL FOR UPDATE NOWAIT`, machine, session).Scan(&epoch); err != nil {
			return err
		}
		var source, phase string
		var opEpoch int64
		if err := tx.QueryRow(ctx, `SELECT source_segment,phase,expected_epoch FROM conversation_switches WHERE logical_machine=$1 AND logical_session=$2 AND operation_id=$3`, machine, session, operation).Scan(&source, &phase, &opEpoch); err != nil {
			return err
		}
		if phase != "preparing" && phase != "prepared" && phase != "in_doubt" || opEpoch != expectedEpoch || epoch != expectedEpoch {
			return nil
		}
		var active bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_segments WHERE id=$1 AND state='active')`, source).Scan(&active); err != nil {
			return err
		}
		if !active {
			return nil
		}
		if err := resolveConversationSwitchTx(ctx, tx, machine, session, operation, "rejected", reason); err != nil {
			return err
		}
		cancelled = true
		return nil
	})
	return cancelled, err
}

// NoteConversationSwitchError keeps the latest failure of an operation that has not activated, so the outcome of
// one that never finishes names its real cause. A settled or activated operation keeps its own error.
func (d *DB) NoteConversationSwitchError(ctx context.Context, machine, session, operation, reason string) error {
	_, err := d.pool.Exec(ctx, `UPDATE conversation_switches s SET error=$4 FROM logical_conversations c
	 WHERE s.logical_machine=$1 AND s.logical_session=$2 AND s.operation_id=$3 AND s.phase IN ('preparing','prepared','in_doubt')
	 AND c.machine_id=s.logical_machine AND c.session_id=s.logical_session AND c.epoch=s.expected_epoch`, machine, session, operation, reason)
	return err
}

func (d *DB) ConversationSwitch(ctx context.Context, machine, session, operation string) (*ConversationSwitch, error) {
	var op ConversationSwitch
	var raw []byte
	err := d.pool.QueryRow(ctx, `SELECT operation_id,expected_epoch,source_segment,coalesce(target_segment,''),request,phase,error,created_at,updated_at FROM conversation_switches WHERE logical_machine=$1 AND logical_session=$2 AND operation_id=$3`, machine, session, operation).
		Scan(&op.OperationID, &op.ExpectedEpoch, &op.SourceSegment, &op.TargetSegment, &raw, &op.Phase, &op.Error, &op.CreatedAt, &op.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	op.Request = json.RawMessage(raw)
	return &op, err
}

func (d *DB) ObserveConversationSource(ctx context.Context, machine, session, history string) error {
	if history == "" {
		return errors.New("missing source generation")
	}
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		c, err := readLogicalConversation(ctx, tx, machine, session)
		if err != nil || c == nil {
			return err
		}
		for _, seg := range c.Segments {
			if seg.MachineID == machine && seg.SessionID == session && seg.SourceHistory == history {
				return nil
			}
		}
		var epoch int64
		if err = tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 FOR UPDATE NOWAIT`, c.MachineID, c.SessionID).Scan(&epoch); err != nil {
			return err
		}
		var id, old, state string
		if err = tx.QueryRow(ctx, `SELECT id,source_history,state FROM conversation_segments WHERE machine_id=$1 AND session_id=$2 FOR UPDATE NOWAIT`, machine, session).Scan(&id, &old, &state); err != nil {
			return err
		}
		if old == history {
			return nil
		}
		if old != "" && state != "abandoned" && state != "prepared" {
			if _, err = tx.Exec(ctx, `UPDATE logical_conversations SET projection_version=projection_version+1,revision=revision+1,history='logical:'||md5(history||$3||$4||(projection_version+1)::text) WHERE machine_id=$1 AND session_id=$2`, c.MachineID, c.SessionID, id, history); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE conversation_segments SET source_history=$3 WHERE machine_id=$1 AND session_id=$2`, machine, session, history)
		return err
	})
}

func (d *DB) ConversationSegmentClosed(ctx context.Context, id string) error {
	_, err := d.pool.Exec(ctx, `UPDATE conversation_segments SET close_pending=false WHERE id=$1 AND state IN ('retired','abandoned')`, id)
	return err
}

func (d *DB) ObserveConversationRevision(ctx context.Context, machine, session string, revision int64) error {
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		c, err := readLogicalConversation(ctx, tx, machine, session)
		if err != nil || c == nil {
			return err
		}
		for _, seg := range c.Segments {
			if seg.MachineID == machine && seg.SessionID == session && seg.SourceRevision == revision {
				return nil
			}
		}
		var epoch int64
		if err = tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 FOR UPDATE NOWAIT`, c.MachineID, c.SessionID).Scan(&epoch); err != nil {
			return err
		}
		var old int64
		var state string
		if err = tx.QueryRow(ctx, `SELECT source_revision,state FROM conversation_segments WHERE machine_id=$1 AND session_id=$2`, machine, session).Scan(&old, &state); err != nil {
			return err
		}
		if old == revision {
			return nil
		}
		if state == "active" {
			if _, err = tx.Exec(ctx, `UPDATE logical_conversations SET revision=greatest(revision+1,$3) WHERE machine_id=$1 AND session_id=$2`, c.MachineID, c.SessionID, revision); err != nil {
				return err
			}
		}
		_, err = tx.Exec(ctx, `UPDATE conversation_segments SET source_revision=$3 WHERE machine_id=$1 AND session_id=$2`, machine, session, revision)
		return err
	})
}

type ConversationInput struct {
	ClientMsgID      string          `json:"client_msg_id"`
	Position         int64           `json:"position"`
	Epoch            int64           `json:"epoch"`
	Payload          json.RawMessage `json:"-"`
	State            string          `json:"state"`
	DispatchSegment  string          `json:"dispatch_segment"`
	DispatchMachine  string          `json:"dispatch_machine"`
	DispatchSession  string          `json:"dispatch_session"`
	DispatchEpoch    int64           `json:"dispatch_epoch"`
	Version          int64           `json:"version"`
	BlockedReason    string          `json:"blocked_reason,omitempty"`
	LastReconciledAt *time.Time      `json:"last_reconciled_at"`
	// ClaimedAt starts the attempt's delivery deadline (conversation_input_deadline.go). It is
	// not part of the CAS tuple: the attempt version already names exactly one claim.
	ClaimedAt time.Time `json:"claimed_at,omitzero"`
}

// Enqueue is idempotent by the original client message ID, not its text.
func (d *DB) EnqueueConversationInput(ctx context.Context, machine, session, id string, epoch int64, payload json.RawMessage) error {
	if id == "" || len(id) > 512 || len(payload) > 1048576 || !json.Valid(payload) {
		return errors.New("invalid conversation input")
	}
	digest := sha256.Sum256(payload)
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		var current int64
		if err := tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 AND deleted_at IS NULL FOR SHARE`, machine, session).Scan(&current); err != nil {
			return err
		}
		if current != epoch {
			return ErrConversationEpoch
		}
		_, err := tx.Exec(ctx, `INSERT INTO conversation_inputs(logical_machine,logical_session,client_msg_id,epoch,payload,payload_hash) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, machine, session, id, epoch, []byte(payload), digest[:])
		if err != nil {
			return err
		}
		var old []byte
		if err = tx.QueryRow(ctx, `SELECT payload_hash FROM conversation_inputs WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3`, machine, session, id).Scan(&old); err != nil {
			return err
		}
		if !bytes.Equal(old, digest[:]) {
			return errors.New("message identity reused for different input")
		}
		return nil
	})
}

// A terminal receipt survives payload cleanup and later engine switches. Matching
// a previous input never replays it on a different physical engine.
func (d *DB) ConversationInputReceipt(ctx context.Context, machine, session, id string, payload json.RawMessage) (string, error) {
	var hash []byte
	var state string
	err := d.pool.QueryRow(ctx, `SELECT payload_hash,state FROM conversation_inputs WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3`, machine, session, id).Scan(&hash, &state)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	if !bytes.Equal(hash, digest[:]) {
		return "", errors.New("message identity reused for different input")
	}
	return state, nil
}

// Claim exactly one queue head. A sending/in-doubt head fences later input until
// reconciled: process restarts cannot blindly repeat an ambiguous model turn.
func (d *DB) ClaimConversationInput(ctx context.Context, machine, session string, epoch int64) (*ConversationInput, error) {
	pending, err := conversationInputPending(ctx, d.pool, machine, session)
	if err != nil || !pending {
		return nil, err
	}
	var out *ConversationInput
	err = conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		var current int64
		if err := tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 AND deleted_at IS NULL FOR UPDATE NOWAIT`, machine, session).Scan(&current); err != nil {
			return err
		}
		if current != epoch {
			return ErrConversationEpoch
		}
		var phase string
		err := tx.QueryRow(ctx, `SELECT phase FROM conversation_switches WHERE logical_machine=$1 AND logical_session=$2 AND phase IN ('preparing','prepared','active','in_doubt')`, machine, session).Scan(&phase)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if err == nil && phase != "active" {
			return nil
		}
		var row ConversationInput
		var raw []byte
		err = tx.QueryRow(ctx, `SELECT client_msg_id,position,epoch,payload,state FROM conversation_inputs WHERE logical_machine=$1 AND logical_session=$2 AND state IN ('queued','sending','in_doubt') ORDER BY position LIMIT 1 FOR UPDATE NOWAIT`, machine, session).
			Scan(&row.ClientMsgID, &row.Position, &row.Epoch, &raw, &row.State)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if row.State != "queued" {
			return nil
		}
		// Claim owns the root lock: target identity is immutable before the first RPC,
		// including a crash between claim and dispatch. Never derive it from active later.
		if err = tx.QueryRow(ctx, `SELECT id,machine_id,session_id FROM conversation_segments WHERE logical_machine=$1 AND logical_session=$2 AND state='active'`, machine, session).Scan(&row.DispatchSegment, &row.DispatchMachine, &row.DispatchSession); err != nil {
			return err
		}
		if err = tx.QueryRow(ctx, `UPDATE conversation_inputs SET state='sending',epoch=$4,dispatch_segment=$5,dispatch_machine=$6,dispatch_session=$7,dispatch_epoch=$4,delivery_version=delivery_version+1,blocked_reason='',last_reconciled_at=NULL,claimed_at=now(),expired_at=NULL WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3 RETURNING delivery_version,claimed_at`, machine, session, row.ClientMsgID, epoch, row.DispatchSegment, row.DispatchMachine, row.DispatchSession).Scan(&row.Version, &row.ClaimedAt); err != nil {
			return err
		}
		row.DispatchEpoch = epoch
		row.Payload = json.RawMessage(raw)
		row.State = "sending"
		row.Epoch = epoch
		out = &row
		return nil
	})
	return out, err
}

// RequeueConversationInput is only for a live manager that proves it never
// attempted the input RPC. A sending row left by a crash remains ambiguous and
// must use a positive runtime receipt instead. The epoch fences stale cleanup.
func (d *DB) RequeueConversationInput(ctx context.Context, machine, session, id string, epoch int64, operation string) error {
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		return requeueConversationInputTx(ctx, tx, machine, session, id, epoch, operation)
	})
}

func (d *DB) RequeueClaimedConversationInput(ctx context.Context, machine, session string, epoch int64, input ConversationInput, operation string) error {
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		if err := lockConversationDelivery(ctx, tx, machine, session, epoch, input); err != nil {
			return err
		}
		return requeueConversationInputTx(ctx, tx, machine, session, input.ClientMsgID, epoch, operation)
	})
}

func requeueConversationInputTx(ctx context.Context, tx pgx.Tx, machine, session, id string, epoch int64, operation string) error {
	var current int64
	if err := tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 AND deleted_at IS NULL FOR UPDATE NOWAIT`, machine, session).Scan(&current); err != nil {
		return err
	}
	if current != epoch {
		return ErrConversationEpoch
	}
	tag, err := tx.Exec(ctx, `UPDATE conversation_inputs SET state='queued',dispatch_segment='',dispatch_machine='',dispatch_session='',dispatch_epoch=0,delivery_version=delivery_version+1 WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3 AND epoch=$4 AND state='sending'`, machine, session, id, epoch)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConversationEpoch
	}
	if operation != "" {
		return resolveConversationSwitchTx(ctx, tx, machine, session, operation, "rejected", "input refused before dispatch")
	}
	return nil
}

// ResolveConversationDelivery commits the physical input receipt and its first-
// turn switch outcome together. No second manager can release the next queued
// input on a rejected target between these two state transitions.
func (d *DB) ResolveConversationDelivery(ctx context.Context, machine, session, id string, epoch int64, operation, outcome string) error {
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		return resolveConversationDeliveryTx(ctx, tx, machine, session, id, epoch, operation, outcome)
	})
}

func (d *DB) ResolveClaimedConversationDelivery(ctx context.Context, machine, session string, epoch int64, input ConversationInput, operation, outcome string) error {
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		if err := lockConversationDelivery(ctx, tx, machine, session, epoch, input); err != nil {
			return err
		}
		return resolveConversationDeliveryTx(ctx, tx, machine, session, input.ClientMsgID, epoch, operation, outcome)
	})
}

func resolveConversationDeliveryTx(ctx context.Context, tx pgx.Tx, machine, session, id string, epoch int64, operation, outcome string) error {
	if outcome != "accepted" && outcome != "rejected" && outcome != "in_doubt" {
		return errors.New("invalid input outcome")
	}
	var current int64
	if err := tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 AND deleted_at IS NULL FOR UPDATE NOWAIT`, machine, session).Scan(&current); err != nil {
		return err
	}
	if current != epoch {
		return ErrConversationEpoch
	}
	tag, err := tx.Exec(ctx, `UPDATE conversation_inputs SET state=$4,delivery_version=delivery_version+1 WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3 AND epoch=$5 AND dispatch_epoch=$5 AND state IN ('sending','in_doubt')`, machine, session, id, outcome, epoch)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		var state string
		var inputEpoch int64
		if err := tx.QueryRow(ctx, `SELECT state,epoch FROM conversation_inputs WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3`, machine, session, id).Scan(&state, &inputEpoch); err != nil {
			return err
		}
		if state != outcome || inputEpoch != epoch {
			return ErrConversationEpoch
		}
	}
	if operation != "" && outcome != "in_doubt" {
		var owns bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_switches w JOIN conversation_inputs i ON i.logical_machine=w.logical_machine AND i.logical_session=w.logical_session AND i.dispatch_segment=w.target_segment AND i.dispatch_epoch=w.expected_epoch+1 WHERE w.logical_machine=$1 AND w.logical_session=$2 AND w.operation_id=$3 AND i.client_msg_id=$4 AND i.dispatch_epoch=$5)`, machine, session, operation, id, epoch).Scan(&owns); err != nil {
			return err
		}
		if !owns {
			return ErrConversationEpoch
		}
		if err := resolveConversationSwitchTx(ctx, tx, machine, session, operation, outcome, ""); err != nil {
			return err
		}
	}
	if outcome == "accepted" {
		// The input carried the handoff an idle acceptance kept (drainLogicalInput attaches it): it is used up.
		if _, err := tx.Exec(ctx, `UPDATE conversation_switches SET handoff=NULL WHERE logical_machine=$1 AND logical_session=$2 AND phase='accepted' AND handoff IS NOT NULL AND target_segment=(SELECT dispatch_segment FROM conversation_inputs WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3)`, machine, session, id); err != nil {
			return err
		}
	}
	// A rejected first input can have been requeued by rollback above. Keep
	// that payload; only terminal states may discard content/provenance.
	_, err = tx.Exec(ctx, `UPDATE conversation_inputs SET payload='{}'::bytea WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3 AND state IN ('accepted','rejected')`, machine, session, id)
	return err
}

func (d *DB) ResolveConversationInput(ctx context.Context, machine, session, id, state string) error {
	if state != "accepted" && state != "rejected" && state != "in_doubt" {
		return errors.New("invalid input outcome")
	}
	_, err := d.pool.Exec(ctx, `UPDATE conversation_inputs SET state=$4,payload=CASE WHEN $4='accepted' THEN '{}'::bytea ELSE payload END WHERE logical_machine=$1 AND logical_session=$2 AND client_msg_id=$3 AND state IN ('sending','in_doubt')`, machine, session, id, state)
	return err
}

type ConversationInteractionRoute struct {
	MachineID, SessionID, PhysicalMachine, PhysicalSession string
	Epoch                                                  int64
}

func (d *DB) PutConversationInteraction(ctx context.Context, id, machine, session, physicalMachine, physicalSession string, epoch int64) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO conversation_interaction_routes(interaction_id,logical_machine,logical_session,machine_id,session_id,epoch)
	 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING`, id, machine, session, physicalMachine, physicalSession, epoch)
	if err != nil {
		return err
	}
	old, err := d.ConversationInteraction(ctx, id)
	if err != nil {
		return err
	}
	if old == nil || old.MachineID != machine || old.SessionID != session || old.PhysicalMachine != physicalMachine || old.PhysicalSession != physicalSession || old.Epoch != epoch {
		return ErrConversationEpoch
	}
	return nil
}

func (d *DB) ConversationInteraction(ctx context.Context, id string) (*ConversationInteractionRoute, error) {
	if d.sets&ManagerSet == 0 {
		return nil, nil
	}
	r := &ConversationInteractionRoute{}
	err := d.pool.QueryRow(ctx, `SELECT logical_machine,logical_session,machine_id,session_id,epoch FROM conversation_interaction_routes WHERE interaction_id=$1`, id).
		Scan(&r.MachineID, &r.SessionID, &r.PhysicalMachine, &r.PhysicalSession, &r.Epoch)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func (d *DB) PutConversationHandoff(ctx context.Context, machine, session, operation string, raw json.RawMessage) error {
	if !json.Valid(raw) || len(raw) > 262144 {
		return errors.New("invalid conversation handoff")
	}
	_, err := d.pool.Exec(ctx, `UPDATE conversation_switches SET handoff=coalesce(handoff,$4),updated_at=now() WHERE logical_machine=$1 AND logical_session=$2 AND operation_id=$3 AND phase='preparing'`, machine, session, operation, []byte(raw))
	return err
}

// ConversationPendingHandoff is the handoff an idle acceptance kept for segment, the conversation's active one: the
// next input the conversation is sent carries it, and its acceptance discards it (#14488). Nil when there is none.
func (d *DB) ConversationPendingHandoff(ctx context.Context, machine, session, segment string) (json.RawMessage, error) {
	var raw []byte
	err := d.pool.QueryRow(ctx, `SELECT handoff FROM conversation_switches WHERE logical_machine=$1 AND logical_session=$2 AND target_segment=$3 AND phase='accepted' AND handoff IS NOT NULL ORDER BY updated_at DESC LIMIT 1`, machine, session, segment).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return json.RawMessage(raw), err
}

func (d *DB) ConversationPreparation(ctx context.Context, machine, session, operation string) (json.RawMessage, bool, error) {
	var raw []byte
	var claimed bool
	err := d.pool.QueryRow(ctx, `SELECT handoff,start_claimed FROM conversation_switches WHERE logical_machine=$1 AND logical_session=$2 AND operation_id=$3`, machine, session, operation).Scan(&raw, &claimed)
	return json.RawMessage(raw), claimed, err
}
func (d *DB) ClaimConversationPreparation(ctx context.Context, machine, session, operation string) (bool, error) {
	tag, err := d.pool.Exec(ctx, `UPDATE conversation_switches SET start_claimed=true,updated_at=now() WHERE logical_machine=$1 AND logical_session=$2 AND operation_id=$3 AND phase='preparing' AND handoff IS NOT NULL AND NOT start_claimed`, machine, session, operation)
	return tag.RowsAffected() == 1, err
}

// ConversationPreparedSession returns a saved positive receipt for a current preparation,
// or an empty session if the node must be consulted. PrepareConversationSegment checks
// the epoch again under the row lock when mapping either receipt.
func (d *DB) ConversationPreparedSession(ctx context.Context, machine, session, operation, targetMachine string, epoch int64) (string, error) {
	var target string
	err := d.pool.QueryRow(ctx, `SELECT coalesce(p.session_id,'')
	 FROM conversation_switches s
	 JOIN logical_conversations c ON c.machine_id=s.logical_machine AND c.session_id=s.logical_session
	 LEFT JOIN conversation_prepared_candidates p ON p.logical_machine=s.logical_machine AND p.logical_session=s.logical_session
	 AND p.operation_id=s.operation_id AND p.machine_id=$4
	 WHERE s.logical_machine=$1 AND s.logical_session=$2 AND s.operation_id=$3
	 AND c.epoch=$5 AND c.deleted_at IS NULL AND s.expected_epoch=c.epoch AND s.phase IN ('preparing','in_doubt')
	 ORDER BY p.session_id LIMIT 1`, machine, session, operation, targetMachine, epoch).Scan(&target)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrConversationEpoch
	}
	return target, err
}

func (d *DB) ConversationClosures(ctx context.Context) ([][2]string, error) {
	rows, err := d.pool.Query(ctx, `SELECT machine_id,session_id FROM conversation_segment_closures ORDER BY machine_id,session_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out [][2]string
	for rows.Next() {
		var k [2]string
		if err := rows.Scan(&k[0], &k[1]); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}
func (d *DB) ConversationClosureDone(ctx context.Context, machine, session string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM conversation_segment_closures WHERE machine_id=$1 AND session_id=$2`, machine, session)
	return err
}

func (d *DB) ConversationInputPending(ctx context.Context, machine, session string) (bool, error) {
	return conversationInputPending(ctx, d.pool, machine, session)
}

// MarkConversationSegmentDispatched records durable dispatch evidence on segment
// before any physical send RPC is attempted on a non-root segment.
func (d *DB) MarkConversationSegmentDispatched(ctx context.Context, machine, session, segment string) error {
	if d.sets&ManagerSet == 0 {
		return errors.New("manager database unavailable")
	}
	tag, err := d.pool.Exec(ctx, `UPDATE conversation_segments SET dispatched=true WHERE id=$1 AND logical_machine=$2 AND logical_session=$3 AND state='active'`, segment, machine, session)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrConversationEpoch
	}
	return nil
}

// ConversationSegmentDispatched reports whether segment has any durable dispatch
// evidence (a direct send marker, or any sending/in_doubt/accepted input since
// activation), or whether the segment or its activation switch cannot be verified.
func (d *DB) ConversationSegmentDispatched(ctx context.Context, machine, session, segment string) (bool, error) {
	if d.sets&ManagerSet == 0 {
		return true, nil
	}
	var dispatched bool
	err := d.pool.QueryRow(ctx, `SELECT
		NOT EXISTS(SELECT 1 FROM conversation_segments WHERE id=$3 AND logical_machine=$1 AND logical_session=$2)
		OR EXISTS(SELECT 1 FROM conversation_segments WHERE id=$3 AND logical_machine=$1 AND logical_session=$2 AND dispatched)
		OR EXISTS(SELECT 1 FROM conversation_inputs WHERE logical_machine=$1 AND logical_session=$2 AND state IN ('sending','in_doubt'))
		OR NOT EXISTS(SELECT 1 FROM conversation_switches WHERE logical_machine=$1 AND logical_session=$2 AND target_segment=$3)
		OR EXISTS(
			SELECT 1 FROM conversation_inputs i
			JOIN conversation_switches s
				ON s.logical_machine=i.logical_machine AND s.logical_session=i.logical_session
			WHERE s.logical_machine=$1 AND s.logical_session=$2 AND s.target_segment=$3
				AND i.epoch >= s.expected_epoch + 1
				AND i.state IN ('sending', 'in_doubt', 'accepted')
		)`, machine, session, segment).Scan(&dispatched)
	return dispatched, err
}

func conversationInputPending(ctx context.Context, q conversationQuerier, machine, session string) (bool, error) {
	var pending bool
	err := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM conversation_inputs WHERE logical_machine=$1 AND logical_session=$2 AND state IN ('queued','sending','in_doubt'))`, machine, session).Scan(&pending)
	return pending, err
}

func (d *DB) FinishConversationInteraction(ctx context.Context, id string) error {
	if d.sets&ManagerSet == 0 {
		return nil
	}
	_, err := d.pool.Exec(ctx, `UPDATE conversation_interaction_routes SET pending=false WHERE interaction_id=$1`, id)
	return err
}

func conversationTransaction(ctx context.Context, d *DB, fn func(pgx.Tx) error) error {
	err := pgx.BeginFunc(ctx, d.pool, fn)
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "55P03" {
		return ErrConversationSwitchBusy
	}
	return err
}

func (d *DB) SetConversationPick(ctx context.Context, machine, session string, epoch int64, launcher, model, effort string) error {
	return conversationTransaction(ctx, d, func(tx pgx.Tx) error {
		var current int64
		if err := tx.QueryRow(ctx, `SELECT epoch FROM logical_conversations WHERE machine_id=$1 AND session_id=$2 AND deleted_at IS NULL FOR UPDATE NOWAIT`, machine, session).Scan(&current); err != nil {
			return err
		}
		if epoch != current {
			return ErrConversationEpoch
		}
		_, err := tx.Exec(ctx, `UPDATE conversation_segments SET launcher=$3,model=$4,effort=$5 WHERE logical_machine=$1 AND logical_session=$2 AND state='active'`, machine, session, launcher, model, effort)
		return err
	})
}

type ConversationPreparedCandidate struct{ MachineID, SessionID, LogicalMachine, LogicalSession, OperationID string }

func (d *DB) RecordConversationPreparedCandidate(ctx context.Context, c ConversationPreparedCandidate) error {
	_, err := d.pool.Exec(ctx, `INSERT INTO conversation_prepared_candidates(machine_id,session_id,logical_machine,logical_session,operation_id) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, c.MachineID, c.SessionID, c.LogicalMachine, c.LogicalSession, c.OperationID)
	return err
}
func (d *DB) ConversationPreparedCandidates(ctx context.Context) ([]ConversationPreparedCandidate, error) {
	rows, err := d.pool.Query(ctx, `SELECT machine_id,session_id,logical_machine,logical_session,operation_id FROM conversation_prepared_candidates`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConversationPreparedCandidate
	for rows.Next() {
		var c ConversationPreparedCandidate
		if err = rows.Scan(&c.MachineID, &c.SessionID, &c.LogicalMachine, &c.LogicalSession, &c.OperationID); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}
func (d *DB) ConversationPreparedCandidateDone(ctx context.Context, machine, session string) error {
	_, err := d.pool.Exec(ctx, `DELETE FROM conversation_prepared_candidates WHERE machine_id=$1 AND session_id=$2`, machine, session)
	return err
}

// The caller holds WithConversationEpoch's shared lock through the physical
// acknowledgement and this write. Updating the segment never upgrades that root
// lock or changes execution authority; activation/deletion remain fenced.
func (d *DB) SetConversationSegmentModes(ctx context.Context, machine, session string, epoch int64, id string, model, effort, mode *string) error {
	tag, err := d.pool.Exec(ctx, `UPDATE conversation_segments s SET model=coalesce($5,model),effort=coalesce($6,effort),permission_mode=coalesce($7,permission_mode)
 WHERE s.id=$4 AND s.logical_machine=$1 AND s.logical_session=$2 AND s.state='active' AND EXISTS(SELECT 1 FROM logical_conversations c WHERE c.machine_id=$1 AND c.session_id=$2 AND c.epoch=$3 AND c.deleted_at IS NULL)`, machine, session, epoch, id, model, effort, mode)
	if err == nil && tag.RowsAffected() != 1 {
		return ErrConversationEpoch
	}
	return err
}

func (d *DB) ReserveConversationInteraction(ctx context.Context, ix proto.Interaction, machine, session, physicalMachine, physicalSession string, epoch int64) error {
	// Reserve identity only. Card content is persisted by the standard sanitized
	// publication pipeline under a fresh epoch fence, before it can be answered.
	return d.PutConversationInteraction(ctx, ix.ID, machine, session, physicalMachine, physicalSession, epoch)
}
