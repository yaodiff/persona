package proto

import (
	"bytes"
	"encoding/json"
	"time"
)

// InternalMaintenanceApproval identifies the explicit local maintenance operation,
// not an extension-name prefix or a standing authorization. Force remains part of
// the original input; the supervisor must approve each requested rerun.
func InternalMaintenanceApproval(ix Interaction) bool {
	if ix.Kind != IxPermission || ix.ToolName != "enkeep_rerun" || ix.Secret || ix.Private || len(ix.Fields) != 0 || ix.Elicitation != nil || ix.OutwardWrite || ix.OwnerProposal || ix.ApprovalBatchID != "" {
		return false
	}
	var input struct {
		Force bool `json:"force"`
	}
	return bytes.HasPrefix(bytes.TrimSpace(ix.ToolInput), []byte("{")) && json.Unmarshal(ix.ToolInput, &input) == nil
}

// MaintenanceAllowed accepts a fresh explicit supervisor answer only for the
// recognized maintenance operation. Cancellation, expiry and late answers never
// authorize execution. It does not change UserAllowed's owner-only contract.
func MaintenanceAllowed(ix Interaction, r Resolution, now time.Time) bool {
	return InternalMaintenanceApproval(ix) && now.Before(ix.ExpiresAt) && r.AnsweredBy == AnsweredByAssistant &&
		r.Allow != nil && *r.Allow && !r.Cancel && !r.Dismiss && !r.Late && !r.Remembered
}
