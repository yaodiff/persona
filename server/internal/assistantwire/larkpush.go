package assistantwire

import (
	"context"
	"time"
)

// LarkPushCopy tells Core when a push starts and when its first card lands (or a companion takes it).
// Both callbacks are optional and run before the push call returns.
type LarkPushCopy struct {
	Started   func()
	Delivered func()
}

// LarkReminder is one claimed to-do reminder. Copy rechecks Core's privacy decision before each
// attempt and supplies the permitted text. A scheduled reminder uses Current to check whether the
// occurrence is still due before retrying.
type LarkReminder struct {
	Todo      string
	Schedule  LarkReminderSchedule
	Copy      func(context.Context) (title, body string, private bool)
	Current   func() bool
	Delivered func()
}

type larkReminderTiming uint8

const (
	larkReminderOnce larkReminderTiming = iota
	larkReminderScheduled
)

// LarkReminderSchedule distinguishes a single send from a scheduled occurrence. Its zero value
// is a single send, even though a scheduled occurrence can itself have a zero timestamp.
type LarkReminderSchedule struct {
	timing larkReminderTiming
	at     time.Time
}

// LarkReminderAt identifies a scheduled occurrence by its timestamp.
func LarkReminderAt(at time.Time) LarkReminderSchedule {
	return LarkReminderSchedule{timing: larkReminderScheduled, at: at}
}

// Occurrence returns the timestamp of a scheduled occurrence and whether there is one.
func (s LarkReminderSchedule) Occurrence() (time.Time, bool) {
	return s.at, s.timing == larkReminderScheduled
}

// LarkReminderPairing binds a reminder post to the owner and pairing generation it first attempted.
type LarkReminderPairing struct {
	Owner    string
	PairedAt time.Time
}

type larkReminderPairingKey struct{}

func WithLarkReminderPairing(ctx context.Context, pairing LarkReminderPairing) context.Context {
	return context.WithValue(ctx, larkReminderPairingKey{}, pairing)
}

func LarkReminderPairingOf(ctx context.Context) (LarkReminderPairing, bool) {
	p, ok := ctx.Value(larkReminderPairingKey{}).(LarkReminderPairing)
	return p, ok
}

// LarkMirror is where a turn or a prepared briefing card is sent. StillDue checks Core's live push
// ledger and the original turn before a retry, after the module has checked the bot's switches.
type LarkMirror struct {
	SessionRef
	Main     bool
	Label    string
	StillDue func(ctx context.Context, pushes bool) bool
	// ThreadChatID and ThreadID route a scheduled task's mirrored result into a specific Feishu group
	// topic (in_thread) instead of the owner's private DM, and bind that topic back to this conversation.
	ThreadChatID FeishuChatID
	ThreadID     FeishuThreadID
}

// LarkMirrorTurn is the finished turn that Core cleared to mirror. Question is the short question
// that can appear on the card, while Asked and Turn are used for the delivery key.
type LarkMirrorTurn struct {
	Asked, Question, Answer, Turn string
}

// LarkAgentDone is the visible content and reply title of a finished Agent notice cleared by Core.
type LarkAgentDone struct {
	Head, Text, Place, Secondary, ThreadTitle string
	AwaitsReply                               bool
}
