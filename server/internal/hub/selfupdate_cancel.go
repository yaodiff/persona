package hub

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// A development attempt owns the interruptible work from reservation through
// staging and the idle wait. The snapshot and handover retain the parent context.
type developmentAttempt struct {
	ctx        context.Context
	cancel     context.CancelFunc
	cancelling bool
}

func (u *updater) reserveDevelopmentLocked(parent context.Context) {
	ctx, cancel := context.WithCancel(parent)
	u.attempt = &developmentAttempt{ctx: ctx, cancel: cancel}
}

func (u *updater) finishDevelopmentLocked() {
	if u.attempt != nil {
		u.attempt.cancel()
		u.attempt = nil
	}
}

func (u *updater) requestCancel() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.attempt == nil {
		return i18n.Err("hub-selfupdate.err_cannot_cancel")
	}
	u.attempt.cancelling = true
	u.attempt.cancel()
	return nil
}

// Claim the adoption boundary under the same lock as cancel. From here onward
// the installer owns recovery even while the adoption journal is being created.
func (u *updater) claimDevelopmentAdoption() error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.attempt != nil && u.attempt.cancelling {
		return context.Canceled
	}
	u.finishDevelopmentLocked()
	u.setLocked("restarting", "", "")
	return nil
}

func (u *updater) recordCancelledLocked(ctx context.Context, record updateRecord) {
	record.Result, record.Reason, record.Ended = "cancelled", "", time.Now()
	if err := u.recordLocked(ctx, record); err != nil {
		u.setLocked("failed", "", su(u.h.userLang(), "err_cancel_history"))
		return
	}
	u.setLocked("idle", "", "")
}
