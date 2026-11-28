package hub

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/approval"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// preparedSettings retains executable hub operations in the hub. The public
// interaction and the assistant's journal contain only the random handle ID.
type preparedSettings struct {
	mu   sync.Mutex
	byID map[string]*preparedSetting
}

type preparedSetting struct {
	manager string
	apply   func(context.Context, proto.Resolution) (string, error)
	timer   *time.Timer
}

// preparedSettingPauseBudget matches the coordinator's cumulative clarification
// budget. The handle also covers the decision time left before and after that
// pause, plus the same answer grace as an ordinary hub-owned card.
// preparedSettingWaitingWindow matches the coordinator's additional unanswered
// waiting window (#10132), which can be followed by a clarification pause.
const (
	preparedSettingPauseBudget   = 24 * time.Hour
	preparedSettingWaitingWindow = 24 * time.Hour
)

func (h *Hub) retainPreparedSetting(manager string, ix proto.Interaction, apply func(context.Context, proto.Resolution) (string, error)) error {
	if manager == "" || ix.ID == "" || apply == nil {
		return errors.New("a prepared setting needs its manager, interaction and apply path")
	}
	p := &h.prepared
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.byID == nil {
		p.byID = map[string]*preparedSetting{}
	}
	if _, exists := p.byID[ix.ID]; exists {
		return errors.New("the prepared setting handle already exists")
	}
	entry := &preparedSetting{manager: manager, apply: apply}
	p.byID[ix.ID] = entry
	if !ix.ExpiresAt.IsZero() {
		h.armPreparedSettingLocked(manager, ix.ID, entry, ix.ExpiresAt.Add(preparedSettingPauseBudget+cardAnswerGrace))
	}
	return nil
}

// armPreparedSettingLocked bounds a retained closure without persisting it. A
// missing handle after this deadline or a hub restart is definitely not applied.
func (h *Hub) armPreparedSettingLocked(manager, id string, entry *preparedSetting, deadline time.Time) {
	entry.timer = time.AfterFunc(time.Until(deadline), func() {
		p := &h.prepared
		p.mu.Lock()
		if current := p.byID[id]; current == entry {
			delete(p.byID, id)
		}
		p.mu.Unlock()
	})
}

func (h *Hub) dropPreparedSetting(manager, id string) {
	p := &h.prepared
	p.mu.Lock()
	if old, ok := p.byID[id]; ok && old.manager == manager {
		delete(p.byID, id)
		if old.timer != nil {
			old.timer.Stop()
		}
	}
	p.mu.Unlock()
}

// applyPreparedSetting takes the handle before executing, so a duplicate bus
// call can never apply twice even if the assistant's own claim is retried.
func (h *Hub) applyPreparedSetting(ctx context.Context, manager, id string, r proto.Resolution) (string, error) {
	p := &h.prepared
	p.mu.Lock()
	old, ok := p.byID[id]
	if ok && old.manager != manager {
		p.mu.Unlock()
		return "", errors.New("the prepared setting is no longer pending")
	}
	if ok {
		delete(p.byID, id)
		if old.timer != nil {
			old.timer.Stop()
		}
	}
	p.mu.Unlock()
	if !ok {
		return "", approval.ErrStale
	}
	return old.apply(ctx, r)
}

// waitPreparedSetting asks the assistant to coordinate the card while the hub
// retains apply. Only Clarifying leaves the handle retained after this call;
// every other return cleans it up (a successful apply has already taken it).
func (h *Hub) waitPreparedSetting(ctx context.Context, manager string, ix proto.Interaction, replacement string,
	apply func(context.Context, proto.Resolution) (string, error), report func(proto.MCPWaitKind)) (approval.Outcome, error) {
	if err := h.retainPreparedSetting(manager, ix, apply); err != nil {
		return approval.Outcome{}, err
	}
	keep := false
	defer func() {
		if !keep {
			h.dropPreparedSetting(manager, ix.ID)
		}
	}()
	if report != nil {
		report(proto.MCPWaitApproval)
		defer report(proto.MCPWaitComputer)
	}
	waitCtx := ctx
	if !ix.ExpiresAt.IsZero() {
		var cancel context.CancelFunc
		waitCtx, cancel = context.WithDeadline(ctx, ix.ExpiresAt.Add(cardAnswerGrace))
		defer cancel()
	}
	if h.askVia != nil { // the existing settings tests play the manager
		r, err := h.askVia(waitCtx, ix)
		if err != nil {
			if waitCtx.Err() != nil {
				return approval.Outcome{Text: prompts.RenderSettingUnanswered()}, nil
			}
			return approval.Outcome{}, err
		}
		switch {
		case r.UserAllowed():
			text, err := h.applyPreparedSetting(waitCtx, manager, ix.ID, r)
			return approval.Outcome{Text: text}, err
		case r.UserDenied():
			return approval.Outcome{Text: "The user declined the change: nothing changed."}, nil
		default:
			return approval.Outcome{Text: prompts.RenderSettingUnanswered()}, nil
		}
	}
	if h.machineOwner(ix.MachineID) != manager {
		return approval.Outcome{}, errors.New("the prepared setting's manager no longer owns the machine")
	}
	var out approval.Outcome
	err := h.asstOf(manager).callAsIs(waitCtx, assistantwire.MethodFleetAskPrepared,
		map[string]any{"interaction": ix, "replacement": replacement}, &out)
	if err != nil {
		if waitCtx.Err() != nil {
			return approval.Outcome{Text: prompts.RenderSettingUnanswered()}, nil
		}
		return approval.Outcome{}, err
	}
	keep = out.Clarifying
	if keep {
		p := &h.prepared
		p.mu.Lock()
		if entry := p.byID[ix.ID]; entry != nil && entry.manager == manager {
			switch {
			case out.Text == approval.WaitingText:
				// The unanswered waiting window can be followed by the full
				// cumulative clarification pause. Do not expire between them.
				if entry.timer != nil {
					entry.timer.Stop()
				}
				h.armPreparedSettingLocked(manager, ix.ID, entry,
					time.Now().Add(preparedSettingWaitingWindow+preparedSettingPauseBudget+cardAnswerGrace))
			case ix.ExpiresAt.IsZero() && entry.timer == nil:
				h.armPreparedSettingLocked(manager, ix.ID, entry, time.Now().Add(preparedSettingPauseBudget+cardAnswerGrace))
			}
		}
		p.mu.Unlock()
	}
	return out, nil
}

func init() {
	handleTypedBus(assistantwire.FleetApprovalApplyMethod, "invalid prepared setting apply: ", (*Hub).busPreparedSettingApply)
	handleTypedBus(assistantwire.FleetApprovalDiscardMethod, "invalid prepared setting discard: ", (*Hub).busPreparedSettingDiscard)
}

func (h *Hub) busPreparedSettingApply(ctx context.Context, c *busConn, p assistantwire.FleetApprovalApply) (assistantwire.FleetApprovalApplyResult, error) {
	if p.InteractionID == "" {
		// The original check formatted a nil decode error for a missing ID.
		return assistantwire.FleetApprovalApplyResult{}, errors.New("invalid prepared setting apply: %!w(<nil>)")
	}
	if p.Resolution.Remembered || !p.Resolution.UserAllowed() {
		return assistantwire.FleetApprovalApplyResult{}, errors.New("a prepared setting apply requires a fresh owner Allow")
	}
	text, err := h.applyPreparedSetting(ctx, c.ident.ID, p.InteractionID, p.Resolution)
	if errors.Is(err, approval.ErrStale) {
		return assistantwire.FleetApprovalApplyResult{Stale: true}, nil
	}
	if err != nil {
		return assistantwire.FleetApprovalApplyResult{}, err
	}
	return assistantwire.FleetApprovalApplyResult{Text: text}, nil
}

func (h *Hub) busPreparedSettingDiscard(_ context.Context, c *busConn, p assistantwire.FleetApprovalDiscard) (assistantwire.FleetApprovalDiscardResult, error) {
	if p.InteractionID == "" {
		return assistantwire.FleetApprovalDiscardResult{}, errors.New("invalid prepared setting discard: %!w(<nil>)")
	}
	h.dropPreparedSetting(c.ident.ID, p.InteractionID)
	return assistantwire.FleetApprovalDiscardResult{OK: true}, nil
}
