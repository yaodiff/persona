package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// conversations_switch coordinates batches; logical_switch.go remains the only
// engine-transition protocol. A batch is durable before its first operation, so
// a restart can replay the same per-conversation operation without duplicating a
// prepared process. Nothing here sends input or interrupts a running turn.
const conversationSwitchesKey = "assistant.conversation_switches"

var (
	errConversationSwitchBatchNotFound = errors.New("conversation switch batch not found")
	errConversationSwitchInvalid       = errors.New("invalid conversation switch")
)

const (
	conversationSwitchReady     = "ready"
	conversationSwitchQueued    = "queued"
	conversationSwitchSwitching = "switching"
	conversationSwitchSwitched  = "switched"
	conversationSwitchRefused   = "refused"
)

type conversationExecution struct {
	Machine        string `json:"machine"`
	Launcher       string `json:"launcher"`
	Model          string `json:"model,omitempty"`
	Effort         string `json:"effort,omitempty"`
	PermissionMode string `json:"permission_mode,omitempty"`
}

type conversationSwitchItem struct {
	Machine        string                `json:"machine"`
	ConversationID string                `json:"conversation_id"`
	ExpectedEpoch  int64                 `json:"expected_epoch"`
	Before         conversationExecution `json:"before"`
	Target         conversationExecution `json:"target"`
	OperationID    string                `json:"operation_id,omitempty"`
	Outcome        string                `json:"outcome"`
	Reason         string                `json:"reason,omitempty"`

	// Cancel marks a restore of an item whose switch has not activated: it cancels the original operation
	// OperationID instead of starting a switch back. It is saved with its batch before the cancel runs, so a restart
	// finishes it (runConversationSwitchCancel).
	Cancel       bool                  `json:"cancel,omitempty"`
	BlockedInput *db.ConversationInput `json:"blocked_input,omitempty"`
}

type conversationSwitchBatch struct {
	ID        string                   `json:"id"`
	Space     string                   `json:"space,omitempty"`
	RestoreOf string                   `json:"restore_of,omitempty"`
	CreatedAt time.Time                `json:"created_at"`
	Items     []conversationSwitchItem `json:"items"`
	// FirstTurn: an item's switch is done only when the target took its first input, which carries the switch's
	// summary of what went before; an activated item with none pending waits for one instead of being accepted idle.
	// The usage-limit fallback's batches (limit_fallback.go) are made so; a batch someone asks for is not.
	FirstTurn bool `json:"first_turn,omitempty"`
	// Fallback marks a batch the usage-limit fallback opened (limit_fallback.go).
	Fallback *conversationSwitchFallback `json:"fallback,omitempty"`
	// MachineFallback marks a batch automatic cross-machine failover opened (machine_fallback.go).
	MachineFallback *conversationSwitchMachineFallback `json:"machine_fallback,omitempty"`
}

type conversationSwitchState struct {
	Batches []conversationSwitchBatch `json:"batches,omitempty"`
}

type conversationSwitchReport struct {
	BatchID   string                   `json:"batch_id,omitempty"`
	Preview   bool                     `json:"preview,omitempty"`
	RestoreOf string                   `json:"restore_of,omitempty"`
	Items     []conversationSwitchItem `json:"items"`
}

type conversationSwitchBook struct {
	mu        sync.Mutex
	scheduled bool
	idle      bool
	// due: when a usage-limit fallback next needs a look though nothing is under way (its restore); zero: none.
	due time.Time
}

// settle records what state leaves to do, with b.mu held: idle when no item is under way and no fallback has its
// continuation to hand over, and when a fallback's restore is due.
func (b *conversationSwitchBook) settle(state conversationSwitchState) {
	b.idle = conversationSwitchStateIdle(state) && !limitFallbackPending(state)
	b.due = limitFallbackDue(state)
}

type conversationsSwitchArgs struct {
	ConversationIDs []string `json:"conversation_ids,omitempty"`
	Machine         string   `json:"machine_id,omitempty"`
	Launcher        string   `json:"launcher,omitempty"`
	Model           string   `json:"model,omitempty"`
	Effort          string   `json:"effort,omitempty"`
	PermissionMode  string   `json:"permission_mode,omitempty"`
	Preview         bool     `json:"preview,omitempty"`
	RestoreBatchID  string   `json:"restore_batch_id,omitempty"`
	BatchID         string   `json:"batch_id,omitempty"`

	// space is set only by the MCP caller. Nil is the full-role HTTP scope.
	space *string
	// cancelUnfinished lets restore_batch_id cancel an item whose switch has not activated (#15317). Set for the
	// person's preview and execution; the usage-limit fallback's automatic restore plans as before and never cancels.
	cancelUnfinished bool
}

type conversationSwitchResolver func(ctx context.Context, machine, id string) (proto.Session, error)
type conversationSwitchConfirm func(string) (bool, string, error)

func (a *conversationsSwitchArgs) validate(status bool) error {
	a.Machine = strings.TrimSpace(a.Machine)
	a.Launcher = strings.TrimSpace(a.Launcher)
	a.Model = strings.TrimSpace(a.Model)
	a.Effort = strings.TrimSpace(a.Effort)
	a.PermissionMode = strings.TrimSpace(a.PermissionMode)
	a.RestoreBatchID = strings.TrimSpace(a.RestoreBatchID)
	a.BatchID = strings.TrimSpace(a.BatchID)
	for i := range a.ConversationIDs {
		a.ConversationIDs[i] = strings.TrimSpace(a.ConversationIDs[i])
	}
	if status {
		if a.BatchID == "" || a.RestoreBatchID != "" || len(a.ConversationIDs) != 0 || a.Machine != "" || a.Launcher != "" || a.Model != "" || a.Effort != "" || a.PermissionMode != "" || a.Preview {
			return errors.New("batch_id alone selects a batch status")
		}
		return nil
	}
	if a.BatchID != "" {
		return errors.New("batch_id is only for status; use restore_batch_id to restore")
	}
	if a.RestoreBatchID != "" {
		if len(a.ConversationIDs) != 0 || a.Machine != "" || a.Launcher != "" || a.Model != "" || a.Effort != "" || a.PermissionMode != "" {
			return errors.New("restore_batch_id cannot be combined with conversation IDs or a target")
		}
		return nil
	}
	if len(a.ConversationIDs) == 0 || len(a.ConversationIDs) > 50 {
		return errors.New("name 1 to 50 conversation_ids, or use restore_batch_id")
	}
	if a.Launcher == "" {
		return errors.New("launcher is required for a forward switch")
	}
	seen := map[string]bool{}
	for _, id := range a.ConversationIDs {
		if id == "" || len(id) > 256 {
			return errors.New("conversation IDs must be nonempty and at most 256 bytes")
		}
		if seen[id] {
			return fmt.Errorf("conversation %s is named twice", id)
		}
		seen[id] = true
	}
	return nil
}

func (s *Service) loadConversationSwitches(ctx context.Context) (conversationSwitchState, bool, error) {
	var state conversationSwitchState
	if s.store() == nil {
		return state, false, errors.New("no database")
	}
	found, err := s.store().Setting(ctx, conversationSwitchesKey, &state)
	return state, found, err
}

func (s *Service) saveConversationSwitches(ctx context.Context, state conversationSwitchState) error {
	return s.putSetting(ctx, conversationSwitchesKey, state)
}

func conversationSwitchStateIdle(state conversationSwitchState) bool {
	for _, batch := range state.Batches {
		for _, item := range batch.Items {
			if item.Outcome == conversationSwitchQueued || item.Outcome == conversationSwitchSwitching || item.Outcome == conversationSwitchReady {
				return false
			}
		}
	}
	return true
}

func conversationExecutionNow(machine string, row proto.Session, c *db.LogicalConversation) conversationExecution {
	if c != nil && c.Epoch > 1 {
		if active, ok := c.Active(); ok {
			return conversationExecution{Machine: active.MachineID, Launcher: active.Launcher, Model: active.Model, Effort: active.Effort, PermissionMode: active.PermissionMode}
		}
	}
	model := row.Model
	if row.RequestedModel != nil {
		model = *row.RequestedModel
	}
	return conversationExecution{Machine: cmp.Or(row.MachineID, machine), Launcher: cmp.Or(row.Launcher, sessionEngineAgent(row)), Model: model, Effort: row.Effort, PermissionMode: row.PermissionMode}
}

func sameConversationExecution(a, b conversationExecution) bool {
	permission := func(v string) string { return cmp.Or(v, "default") }
	return a.Machine == b.Machine && a.Launcher == b.Launcher && a.Model == b.Model && a.Effort == b.Effort && permission(a.PermissionMode) == permission(b.PermissionMode)
}

func conversationSwitchPending(state conversationSwitchState, machine, id string) bool {
	for _, batch := range state.Batches {
		for _, item := range batch.Items {
			if item.Machine == machine && item.ConversationID == id && (item.Outcome == conversationSwitchQueued || item.Outcome == conversationSwitchSwitching || item.Outcome == conversationSwitchReady) {
				return true
			}
		}
	}
	return false
}

func (s *Service) planConversationSwitchItem(ctx context.Context, state conversationSwitchState, st logicalSwitchStore, machine, id string, target conversationExecution, require *conversationExecution, resolve conversationSwitchResolver) conversationSwitchItem {
	item := conversationSwitchItem{Machine: machine, ConversationID: id, Target: target}
	refuse := func(reason string) conversationSwitchItem {
		item.Outcome, item.Reason = conversationSwitchRefused, reason
		return item
	}
	row, err := resolve(ctx, machine, id)
	if err != nil {
		return refuse(err.Error())
	}
	// MCP lookup accepts the same unambiguous prefixes as the other conversation tools.
	// Durable operations always use the resolved public identity, never the prefix.
	machine, id = cmp.Or(row.MachineID, machine), cmp.Or(row.SessionID, id)
	item.Machine, item.ConversationID = machine, id
	if conversationSwitchPending(state, machine, id) {
		return refuse("another batch switch is already queued or running")
	}
	if !row.Assistant || row.PaneID != "" || row.IsDeleted() {
		return refuse("not an available Assistant conversation")
	}
	c, err := st.LogicalConversation(ctx, machine, id)
	if err != nil {
		return refuse(err.Error())
	}
	item.ExpectedEpoch = 1
	if c != nil {
		if c.Deleted || c.MachineID != machine || c.SessionID != id {
			return refuse("not a public Assistant conversation")
		}
		item.ExpectedEpoch = c.Epoch
	}
	item.Before = conversationExecutionNow(machine, row, c)
	item.Target.Machine = cmp.Or(item.Target.Machine, item.Before.Machine)
	if require == nil {
		// advanceLogicalConversation inherits the source permission mode when omitted;
		// record that same effective mode so restore_batch_id's fence matches the prepared segment.
		item.Target.PermissionMode = cmp.Or(item.Target.PermissionMode, item.Before.PermissionMode)
	}
	if c != nil && c.Switch != nil {
		return refuse("another logical switch is still running")
	}
	if require != nil && !sameConversationExecution(item.Before, *require) {
		return refuse("the conversation changed independently; its newer settings were left untouched")
	}
	if s.conversationSwitchBusy(machine, id, row) {
		item.Outcome = conversationSwitchQueued
	} else {
		item.Outcome = conversationSwitchReady
	}
	return item
}

func checkConversationSwitchUnique(items []conversationSwitchItem) error {
	seen := map[string]bool{}
	for _, item := range items {
		key := item.Machine + "\x00" + item.ConversationID
		if seen[key] {
			return fmt.Errorf("%w: conversation %s resolves to the same conversation more than once", errConversationSwitchInvalid, item.ConversationID)
		}
		seen[key] = true
	}
	return nil
}

// planConversationSwitches is read-only. In particular, preview neither
// reconciles old batches nor validates a target by starting a node operation.
func (s *Service) planConversationSwitches(ctx context.Context, state conversationSwitchState, a conversationsSwitchArgs, resolve conversationSwitchResolver) ([]conversationSwitchItem, error) {
	st, ok := s.store().(logicalSwitchStore)
	if !ok {
		return nil, logicalRefusal(ctx, "unavailable")
	}
	if a.RestoreBatchID == "" {
		target := conversationExecution{Launcher: a.Launcher, Model: a.Model, Effort: a.Effort, PermissionMode: a.PermissionMode}
		items := make([]conversationSwitchItem, 0, len(a.ConversationIDs))
		for _, id := range a.ConversationIDs {
			items = append(items, s.planConversationSwitchItem(ctx, state, st, a.Machine, id, target, nil, resolve))
		}
		return items, checkConversationSwitchUnique(items)
	}
	var original *conversationSwitchBatch
	for i := range state.Batches {
		if state.Batches[i].ID == a.RestoreBatchID && (a.space == nil || state.Batches[i].Space == *a.space) {
			original = &state.Batches[i]
			break
		}
	}
	if original == nil {
		return nil, errConversationSwitchBatchNotFound
	}
	items := make([]conversationSwitchItem, 0, len(original.Items))
	for _, old := range original.Items {
		if old.Outcome == conversationSwitchSwitching && a.cancelUnfinished {
			items = append(items, s.planConversationSwitchCancel(ctx, st, old, resolve))
			continue
		}
		if old.Outcome != conversationSwitchSwitched {
			items = append(items, conversationSwitchItem{Machine: old.Machine, ConversationID: old.ConversationID, Before: old.Target, Target: old.Before, Outcome: conversationSwitchRefused, Reason: "the original batch item has not completed a switch"})
			continue
		}
		require := old.Target
		if require.PermissionMode == "" {
			require.PermissionMode = old.Before.PermissionMode
		}
		items = append(items, s.planConversationSwitchItem(ctx, state, st, old.Machine, old.ConversationID, old.Before, &require, resolve))
	}
	return items, checkConversationSwitchUnique(items)
}

// planConversationSwitchCancel plans restoring old, whose switch is under way, by cancelling that switch. It is
// read-only. Only a switch that has not activated its target can be cancelled, and only while the conversation is
// still at old's epoch on its saved before-image; anything else is refused as before, and an activated switch is
// restored once it settles.
func (s *Service) planConversationSwitchCancel(ctx context.Context, st logicalSwitchStore, old conversationSwitchItem, resolve conversationSwitchResolver) conversationSwitchItem {
	item := conversationSwitchItem{Machine: old.Machine, ConversationID: old.ConversationID, ExpectedEpoch: old.ExpectedEpoch, Before: old.Before, Target: old.Before, OperationID: old.OperationID}
	refuse := func(reason string) conversationSwitchItem {
		// The same shape as any other unfinished item restore_batch_id refuses.
		return conversationSwitchItem{Machine: old.Machine, ConversationID: old.ConversationID, Before: old.Target, Target: old.Before, Outcome: conversationSwitchRefused, Reason: reason}
	}
	op, err := st.ConversationSwitch(ctx, old.Machine, old.ConversationID, old.OperationID)
	if err != nil {
		return refuse(err.Error())
	}
	c, err := st.LogicalConversation(ctx, old.Machine, old.ConversationID)
	if err != nil {
		return refuse(err.Error())
	}
	if op == nil || op.Phase != "preparing" && op.Phase != "prepared" && op.Phase != "in_doubt" || op.ExpectedEpoch != old.ExpectedEpoch ||
		c == nil || c.Epoch != old.ExpectedEpoch || c.Switch == nil || c.Switch.OperationID != old.OperationID {
		return refuse("the original batch item has not completed a switch")
	}
	if active, ok := c.Active(); !ok || active.ID != op.SourceSegment {
		return refuse("the original batch item has not completed a switch")
	}
	row, err := resolve(ctx, old.Machine, old.ConversationID)
	if err != nil {
		return refuse(err.Error())
	}
	if !sameConversationExecution(conversationExecutionNow(old.Machine, row, c), old.Before) {
		return refuse("the conversation changed independently; its newer settings were left untouched")
	}
	item.Outcome, item.Cancel = conversationSwitchReady, true
	return item
}

// runConversationSwitchCancel finishes the saved cancel of a restore batch's item (Cancel): it cancels the original
// operation, or finds that an earlier pass of this batch did before a crash lost the outcome. The cancel shares the
// conversation's row lock with activation and is fenced by its epoch, so a switch that activated meanwhile is left
// to finish and this item is refused. A lock held for a moment is retried on the next pass.
func (s *Service) runConversationSwitchCancel(ctx context.Context, st logicalSwitchStore, batchID string, item *conversationSwitchItem) bool {
	if item.Outcome != conversationSwitchQueued && item.Outcome != conversationSwitchReady {
		return false
	}
	reason := "cancelled by restore batch " + batchID + "; the conversation kept its original settings"
	cancelled, err := st.CancelConversationSwitch(ctx, item.Machine, item.ConversationID, item.OperationID, item.ExpectedEpoch, reason)
	if errors.Is(err, db.ErrConversationSwitchBusy) {
		return false
	}
	if err == nil && !cancelled {
		op, opErr := st.ConversationSwitch(ctx, item.Machine, item.ConversationID, item.OperationID)
		if opErr != nil {
			s.logf("conversations_switch %s: cancel check: %v", item.OperationID, opErr)
			return false
		}
		cancelled = op != nil && op.Phase == "rejected" && op.Error == reason
	}
	switch {
	case err != nil:
		item.Outcome, item.Reason, item.OperationID = conversationSwitchRefused, err.Error(), ""
	case !cancelled:
		item.Outcome, item.Reason, item.OperationID = conversationSwitchRefused, "the switch went ahead before it could be cancelled; restore the batch again once it finishes", ""
	default:
		item.Outcome, item.Reason = conversationSwitchSwitched, "the unfinished switch was cancelled; the conversation kept its original settings"
		s.afterLogicalSwitchCancel(ctx, st, item.Machine, item.ConversationID)
	}
	return true
}

func actionableConversationSwitchItems(items []conversationSwitchItem) int {
	n := 0
	for _, item := range items {
		if item.Outcome != conversationSwitchRefused {
			n++
		}
	}
	return n
}

func conversationSwitchSummary(items []conversationSwitchItem, a conversationsSwitchArgs) string {
	lines := make([]string, 0, len(items))
	for _, item := range items {
		if item.Outcome != conversationSwitchRefused {
			lines = append(lines, fmt.Sprintf("%s · %s → %s/%s", item.Machine, item.ConversationID, item.Target.Launcher, cmp.Or(item.Target.Model, "launcher default")))
		}
	}
	if a.RestoreBatchID != "" {
		return "Restore batch " + a.RestoreBatchID + "\n\n" + strings.Join(lines, "\n")
	}
	return "Switch Assistant conversations\n\n" + strings.Join(lines, "\n")
}

func (s *Service) reconcileConversationSwitchItem(ctx context.Context, st logicalSwitchStore, firstTurn bool, item *conversationSwitchItem) (bool, error) {
	op, err := st.ConversationSwitch(ctx, item.Machine, item.ConversationID, item.OperationID)
	if err != nil {
		return false, err
	}
	if op == nil {
		if item.Outcome == conversationSwitchSwitching {
			item.Outcome = conversationSwitchQueued
			return true, nil
		}
		return false, nil
	}
	switch op.Phase {
	case "accepted":
		if item.Outcome != conversationSwitchSwitched {
			item.Outcome, item.Reason = conversationSwitchSwitched, ""
			return true, nil
		}
	case "rejected":
		if item.Outcome != conversationSwitchRefused {
			item.Outcome = conversationSwitchRefused
			item.Reason = cmp.Or(op.Error, "the logical switch was rejected")
			return true, nil
		}
	case "active":
		c, err := st.LogicalConversation(ctx, item.Machine, item.ConversationID)
		if err != nil {
			return false, err
		}
		blocked := s.logicalBlockedInput(ctx, st, c)
		if blocked != nil {
			item.BlockedInput = blocked
			item.Reason = "blocked_input: " + blocked.ClientMsgID + ": " + blocked.BlockedReason
			return true, nil
		}
		if item.BlockedInput != nil {
			item.BlockedInput, item.Reason = nil, ""
			return true, nil
		}
		if firstTurn {
			// Its first input carries the summary (drainLogicalInput) and settles it as accepted or rejected.
			return false, nil
		}
		// The no-input check and acceptance share one database lock. A real
		// input arriving concurrently therefore either keeps the original
		// delivery/rollback protocol or is queued after acceptance, never lost
		// between two separate checks.
		accepted, err := st.AcceptIdleConversationSwitch(ctx, item.Machine, item.ConversationID, item.OperationID, item.ExpectedEpoch)
		if err != nil {
			return false, err
		}
		if accepted {
			item.Outcome, item.Reason = conversationSwitchSwitched, ""
			s.logicalChanged()
			return true, nil
		}
	}
	return false, nil
}

func conversationSwitchBusyError(err error) bool {
	var re *rpc.Error
	return errors.As(err, &re) && re.Code == assistantwire.CodeConversationBusy
}

func (s *Service) runConversationSwitchItem(ctx context.Context, st logicalSwitchStore, state *conversationSwitchState, batch *conversationSwitchBatch, item *conversationSwitchItem) bool {
	if item.Cancel {
		return s.runConversationSwitchCancel(ctx, st, batch.ID, item)
	}
	if item.Outcome == conversationSwitchSwitching {
		changed, err := s.reconcileConversationSwitchItem(ctx, st, batch.FirstTurn, item)
		if err != nil {
			s.logf("conversations_switch %s: reconcile: %v", item.OperationID, err)
			return false
		}
		if changed {
			s.historyWake()
		}
		return changed
	}
	if item.Outcome != conversationSwitchQueued && item.Outcome != conversationSwitchReady {
		return false
	}
	// A crash can happen after BeginConversationSwitch and before the settings
	// write. Seeing the stored operation turns this back into reconciliation,
	// never a second logical operation.
	op, err := st.ConversationSwitch(ctx, item.Machine, item.ConversationID, item.OperationID)
	if err != nil {
		s.logf("conversations_switch %s: operation read: %v", item.OperationID, err)
		return false
	}
	if op != nil {
		item.Outcome = conversationSwitchSwitching
		_, reconcileErr := s.reconcileConversationSwitchItem(ctx, st, batch.FirstTurn, item)
		if reconcileErr != nil {
			s.logf("conversations_switch %s: replay reconcile: %v", item.OperationID, reconcileErr)
		}
		return true
	}
	// Reconcile an operation already begun even if the reset passed before its batch was saved.
	if fb := batch.Fallback; fb != nil && !time.Now().Before(fb.ResetsAt) {
		item.Outcome, item.Reason = conversationSwitchRefused, "the usage limit reset before the switch could start"
		return true
	}
	automaticRestore := batch.RestoreOf != "" && batch.FirstTurn
	machineFallback := batch.MachineFallback != nil
	row, known := s.session(ctx, item.Machine, item.ConversationID)
	if !known || !row.Assistant || row.PaneID != "" || row.IsDeleted() {
		// Disconnected nodes can lose their cached row before their next snapshot. An explicit deletion is final.
		if automaticRestore && !row.IsDeleted() && s.limitRestoreNodeOffline(ctx, item.Machine) {
			return false
		}
		if machineFallback && !row.IsDeleted() && s.machinePhysicallyOffline(ctx, item.Machine) && s.isFailoverEligibleOffline(ctx, item.Machine, item.ConversationID) {
			row = proto.Session{
				MachineID:      item.Machine,
				SessionID:      item.ConversationID,
				Assistant:      true,
				Launcher:       item.Before.Launcher,
				Model:          item.Before.Model,
				Effort:         item.Before.Effort,
				PermissionMode: item.Before.PermissionMode,
			}
			known = true
		} else {
			item.Outcome, item.Reason = conversationSwitchRefused, "the conversation is no longer available"
			return true
		}
	}
	c, err := st.LogicalConversation(ctx, item.Machine, item.ConversationID)
	if err != nil {
		s.logf("conversations_switch %s: epoch read: %v", item.OperationID, err)
		return false
	}
	epoch := int64(1)
	if c != nil {
		epoch = c.Epoch
	}
	current := conversationExecutionNow(item.Machine, row, c)
	if epoch != item.ExpectedEpoch || !sameConversationExecution(current, item.Before) || (automaticRestore && !limitFallbackOwnsEpoch(state, batch.RestoreOf, epoch)) {
		item.Outcome, item.Reason = conversationSwitchRefused, "the conversation changed independently; its newer settings were left untouched"
		return true
	}
	permission := item.Target.PermissionMode
	if batch.RestoreOf != "" && permission == "" {
		permission = "default"
	}
	_, err = s.beginLogicalSwitch(ctx, item.Machine, item.ConversationID, logicalSwitchRequest{
		OperationID: item.OperationID, ExpectedEpoch: item.ExpectedEpoch, MachineID: item.Target.Machine,
		Launcher: item.Target.Launcher, Model: item.Target.Model, Effort: item.Target.Effort, PermissionMode: permission,
		AllowOfflineSource: machineFallback,
	})
	// The row can still be cached when the original launcher's live check finds its link down.
	// Keep this automatic return's durable operation and recheck its epoch and settings on the next pass.
	if automaticRestore && err != nil {
		linkErr := busError(err)
		if errors.Is(linkErr, apierr.ErrOffline) || errors.Is(linkErr, rpc.ErrClosed) {
			return false
		}
	}
	if conversationSwitchBusyError(err) {
		item.Outcome = conversationSwitchQueued
		return false
	}
	if err != nil {
		item.Outcome, item.Reason = conversationSwitchRefused, err.Error()
		return true
	}
	item.Outcome, item.Reason = conversationSwitchSwitching, ""
	s.logicalChanged()
	s.historyWake()
	return true
}

func (s *Service) runConversationSwitchesLocked(ctx context.Context, state *conversationSwitchState) bool {
	if len(state.Batches) == 0 {
		return false
	}
	st, ok := s.store().(logicalSwitchStore)
	if !ok {
		return false
	}
	changed := false
	for bi := range state.Batches {
		batch := &state.Batches[bi]
		for ii := range batch.Items {
			if s.runConversationSwitchItem(ctx, st, state, batch, &batch.Items[ii]) {
				changed = true
			}
		}
	}
	now := time.Now()
	if s.limitFallbacksLocked(ctx, state, now) {
		changed = true
	}
	if s.machineFallbacksLocked(ctx, state, now) {
		changed = true
	}
	return changed
}

func makeConversationSwitchReport(batch conversationSwitchBatch, preview bool) conversationSwitchReport {
	return conversationSwitchReport{BatchID: batch.ID, Preview: preview, RestoreOf: batch.RestoreOf, Items: batch.Items}
}

func conversationSwitchPreviewReport(items []conversationSwitchItem, restore string) conversationSwitchReport {
	return conversationSwitchReport{Preview: true, RestoreOf: restore, Items: items}
}

func (s *Service) previewConversationSwitches(ctx context.Context, a conversationsSwitchArgs, resolve conversationSwitchResolver) (conversationSwitchReport, error) {
	a.cancelUnfinished = true
	s.conversationSwitches.mu.Lock()
	defer s.conversationSwitches.mu.Unlock()
	state, _, err := s.loadConversationSwitches(ctx)
	if err != nil {
		return conversationSwitchReport{}, err
	}
	items, err := s.planConversationSwitches(ctx, state, a, resolve)
	if err != nil {
		return conversationSwitchReport{}, err
	}
	return conversationSwitchPreviewReport(items, a.RestoreBatchID), nil
}

func (s *Service) executeConversationSwitches(ctx context.Context, a conversationsSwitchArgs, space string, resolve conversationSwitchResolver, confirm conversationSwitchConfirm) (conversationSwitchReport, string, error) {
	a.cancelUnfinished = true
	var allowed map[string]bool
	if confirm != nil {
		s.conversationSwitches.mu.Lock()
		state, _, err := s.loadConversationSwitches(ctx)
		if err == nil {
			if s.runConversationSwitchesLocked(ctx, &state) {
				err = s.saveConversationSwitches(ctx, state)
			}
		}
		var items []conversationSwitchItem
		if err == nil {
			items, err = s.planConversationSwitches(ctx, state, a, resolve)
		}
		s.conversationSwitches.mu.Unlock()
		if err != nil {
			return conversationSwitchReport{}, "", err
		}
		if actionableConversationSwitchItems(items) == 0 {
			return conversationSwitchReport{RestoreOf: a.RestoreBatchID, Items: items}, "", nil
		}
		ok, outcome, err := confirm(conversationSwitchSummary(items, a))
		if err != nil || !ok {
			if err == nil && outcome == "" {
				outcome = "declined"
			}
			return conversationSwitchReport{}, outcome, err
		}
		// allowed: what was confirmed for each actionable conversation, a cancel (true) or a switch (false).
		allowed = map[string]bool{}
		for _, item := range items {
			if item.Outcome != conversationSwitchRefused {
				allowed[item.Machine+"\x00"+item.ConversationID] = item.Cancel
			}
		}
	}

	s.conversationSwitches.mu.Lock()
	defer s.conversationSwitches.mu.Unlock()
	state, _, err := s.loadConversationSwitches(ctx)
	if err != nil {
		return conversationSwitchReport{}, "", err
	}
	if s.runConversationSwitchesLocked(ctx, &state) {
		if err := s.saveConversationSwitches(ctx, state); err != nil {
			return conversationSwitchReport{}, "", err
		}
	}
	items, err := s.planConversationSwitches(ctx, state, a, resolve)
	if err != nil {
		return conversationSwitchReport{}, "", err
	}
	if allowed != nil {
		for i := range items {
			if items[i].Outcome == conversationSwitchRefused {
				continue
			}
			cancel, ok := allowed[items[i].Machine+"\x00"+items[i].ConversationID]
			if !ok {
				items[i].Outcome, items[i].OperationID, items[i].Cancel = conversationSwitchRefused, "", false
				items[i].Reason = "the item became eligible while confirmation was open; preview and confirm it again"
			} else if cancel != items[i].Cancel {
				// A switch that activated (or settled) while confirmation was open is not cancelled, nor switched back unasked.
				items[i].Outcome, items[i].OperationID, items[i].Cancel = conversationSwitchRefused, "", false
				items[i].Reason = "the switch moved on while confirmation was open; preview and confirm it again"
			}
		}
	}
	if actionableConversationSwitchItems(items) == 0 {
		return conversationSwitchReport{RestoreOf: a.RestoreBatchID, Items: items}, "", nil
	}
	batch := conversationSwitchBatch{ID: randomHex(16), Space: space, RestoreOf: a.RestoreBatchID, CreatedAt: time.Now().UTC(), Items: items}
	for i := range batch.Items {
		item := &batch.Items[i]
		switch {
		case item.Cancel:
			// A switch that has not activated is cancelled, not switched back. The item keeps the original operation's
			// ID, and the cancel runs only once this batch is saved (runConversationSwitchCancel).
			item.Outcome = conversationSwitchQueued
		case item.Outcome != conversationSwitchRefused:
			item.OperationID = randomHex(16)
			item.Outcome = conversationSwitchQueued
		}
	}
	state.Batches = append(state.Batches, batch)
	// The before-images and operation IDs are durable before the first node or
	// logical-segment mutation. This is the crash-recovery boundary.
	if err := s.saveConversationSwitches(ctx, state); err != nil {
		return conversationSwitchReport{}, "", err
	}
	s.conversationSwitches.idle = false
	if s.runConversationSwitchesLocked(ctx, &state) {
		if err := s.saveConversationSwitches(ctx, state); err != nil {
			return conversationSwitchReport{}, "", err
		}
	}
	s.conversationSwitches.settle(state)
	return makeConversationSwitchReport(state.Batches[len(state.Batches)-1], false), "", nil
}

func (s *Service) conversationSwitchStatus(ctx context.Context, id string, space *string) (conversationSwitchReport, error) {
	s.conversationSwitches.mu.Lock()
	defer s.conversationSwitches.mu.Unlock()
	state, found, err := s.loadConversationSwitches(ctx)
	if err != nil {
		return conversationSwitchReport{}, err
	}
	if s.runConversationSwitchesLocked(ctx, &state) {
		if err := s.saveConversationSwitches(ctx, state); err != nil {
			return conversationSwitchReport{}, err
		}
	}
	s.conversationSwitches.settle(state)
	if found {
		for _, batch := range state.Batches {
			if batch.ID == id && (space == nil || batch.Space == *space) {
				return makeConversationSwitchReport(batch, false), nil
			}
		}
	}
	return conversationSwitchReport{}, errConversationSwitchBatchNotFound
}

func (s *Service) wakeConversationSwitches() {
	book := &s.conversationSwitches
	book.mu.Lock()
	if book.scheduled || book.idle && (book.due.IsZero() || time.Now().Before(book.due)) {
		book.mu.Unlock()
		return
	}
	book.scheduled = true
	book.mu.Unlock()
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		defer func() {
			book.mu.Lock()
			book.scheduled = false
			book.mu.Unlock()
		}()
		book.mu.Lock()
		defer book.mu.Unlock()
		state, _, err := s.loadConversationSwitches(ctx)
		if err != nil {
			s.logf("conversations_switch: load: %v", err)
			return
		}
		if s.runConversationSwitchesLocked(ctx, &state) {
			if err := s.saveConversationSwitches(ctx, state); err != nil {
				s.logf("conversations_switch: save: %v", err)
				return
			}
		}
		book.settle(state)
	}()
}

func writeConversationSwitchError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errConversationSwitchBatchNotFound):
		apierr.Write(w, http.StatusNotFound, err)
	case errors.Is(err, errConversationSwitchInvalid):
		apierr.Write(w, http.StatusBadRequest, err)
	default:
		apierr.Call(w, err)
	}
}

func (s *Service) conversationSwitchRoutes() {
	s.handle("POST /api/v1/assistant/conversation-switches", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, 403, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		var a conversationsSwitchArgs
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 65536))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&a); err != nil {
			apierr.Write(w, 400, err)
			return
		}
		if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
			apierr.Write(w, 400, errors.New("one JSON object is required"))
			return
		}
		if err := a.validate(false); err != nil {
			apierr.Write(w, 400, err)
			return
		}
		defaultMachine := cmp.Or(a.Machine, s.settings(r.Context()).MachineID)
		a.Machine = defaultMachine
		resolve := func(ctx context.Context, targetMachine, id string) (proto.Session, error) {
			machine := cmp.Or(targetMachine, defaultMachine)
			row, ok := s.session(ctx, machine, id)
			if !ok {
				return proto.Session{}, fmt.Errorf("conversation not found")
			}
			row.MachineID = machine
			return row, nil
		}
		if a.Preview {
			report, err := s.previewConversationSwitches(r.Context(), a, resolve)
			if err != nil {
				writeConversationSwitchError(w, err)
				return
			}
			apierr.WriteJSON(w, report)
			return
		}
		report, _, err := s.executeConversationSwitches(r.Context(), a, "", resolve, nil)
		if err != nil {
			writeConversationSwitchError(w, err)
			return
		}
		apierr.WriteJSON(w, report)
	})
	s.handle("GET /api/v1/assistant/conversation-switches/{batch}", func(w http.ResponseWriter, r *http.Request) {
		if !fullRole(r) {
			apierr.Write(w, 403, i18n.Err("assistant-model_pick.err_role"))
			return
		}
		report, err := s.conversationSwitchStatus(r.Context(), r.PathValue("batch"), nil)
		if err != nil {
			writeConversationSwitchError(w, err)
			return
		}
		apierr.WriteJSON(w, report)
	})
}

func (s *Service) toolConversationsSwitch(ctx context.Context, machine, self string, raw json.RawMessage) (string, error) {
	var a conversationsSwitchArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return "", fmt.Errorf("arguments: %w", err)
	}
	status := a.BatchID != ""
	if err := a.validate(status); err != nil {
		return "", err
	}
	space, err := s.knownSpace(ctx, self)
	if err != nil {
		return "", err
	}
	a.space = &space
	if status {
		report, err := s.conversationSwitchStatus(ctx, a.BatchID, &space)
		if err != nil {
			return "", err
		}
		encoded, _ := json.MarshalIndent(report, "", "  ")
		return string(encoded), nil
	}
	defaultMachine := cmp.Or(a.Machine, s.settings(ctx).MachineID)
	a.Machine = defaultMachine
	resolve := func(ctx context.Context, targetMachine, id string) (proto.Session, error) {
		target := cmp.Or(targetMachine, defaultMachine)
		row, err := s.ownSpaceConversationOnMachine(ctx, target, self, id)
		if err != nil {
			return proto.Session{}, err
		}
		row.MachineID = cmp.Or(row.MachineID, target)
		return row, nil
	}
	if a.Preview {
		report, err := s.previewConversationSwitches(ctx, a, resolve)
		if err != nil {
			return "", err
		}
		encoded, _ := json.MarshalIndent(report, "", "  ")
		return "Preview, nothing done yet\n" + string(encoded), nil
	}
	confirm := func(summary string) (bool, string, error) {
		return s.confirmOutbound(ctx, machine, self, "conversations_switch", "", summary, raw)
	}
	report, outcome, err := s.executeConversationSwitches(ctx, a, space, resolve, confirm)
	if err != nil {
		return "", err
	}
	if outcome != "" {
		return outboundNotDone(outcome, "The user declined on the phone: no conversation was switched and nothing was sent.", "no conversation was switched and nothing was sent"), nil
	}
	encoded, _ := json.MarshalIndent(report, "", "  ")
	return string(encoded), nil
}
