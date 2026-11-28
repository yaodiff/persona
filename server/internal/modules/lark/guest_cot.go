package lark

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/i18n"
)

// A guest's process bubble (docs/extensions/bots.md "Group access for teammates"). It is built only from tool calls
// the hub itself observes: a fixed safe title per call, a fixed result line, no arguments, no output, no thinking.
// Only when the owner switched topic COT on, and only for a turn that calls an owner-listed tool. A problem here is
// logged and never returned: it must not fail the tool call or the reply.

// GuestToolStep records one sanitized step and returns the func that finishes it. The func is a no-op when the step
// was not shown (COT off, the turn closed, too many steps, or the agent message could not be made).
func (l *Layer) GuestToolStep(ctx context.Context, key, name string) func(bool) {
	none := func(bool) {}
	st, err := l.Status(ctx)
	if err != nil || !st.Features.TopicCot {
		return none
	}
	s := l.chat()
	r := s.guestGet(key)
	if r == nil || r.Turn == nil || r.Turn.State != guestStateAccepted || r.Turn.CotOff || r.Turn.Steps >= guestCotStepsMax {
		return none
	}
	step := s.guestCotStep(ctx, key, name)
	if step == "" {
		return none
	}
	return func(failed bool) { s.guestCotResult(key, step, failed) }
}

// guestCotCallFor bounds one agent-message call: a slow Lark must not hold a guest's tool call or reply.
const guestCotCallFor = 10 * time.Second

// guestCotTurn is what the agent-message calls need of a turn, copied out under the book's lock.
type guestCotTurn struct {
	id, cot, msg string
	off          bool
}

// guestCotSnap copies key's current turn. ok is false when there is none.
func (s core) guestCotSnap(key string) (guestCotTurn, bool) {
	b := s.t.guests()
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.records[key]
	if r == nil || r.Turn == nil {
		return guestCotTurn{}, false
	}
	return guestCotTurn{id: r.Turn.ID, cot: r.Turn.CotID, msg: r.Turn.CotMsg, off: r.Turn.CotOff}, true
}

// guestCotUpdate changes key's turn when it is still the turn id, and saves the book. False when the turn moved on.
func (s core) guestCotUpdate(key, id string, fn func(*guestTurnRecord)) bool {
	b := s.t.guests()
	b.mu.Lock()
	defer b.mu.Unlock()
	r := b.records[key]
	if r == nil || r.Turn == nil || r.Turn.ID != id {
		return false
	}
	fn(r.Turn)
	_ = s.guestSave(context.Background())
	return true
}

// guestCotCall makes one agent-message call on its own deadline, never under the book's lock.
func (s core) guestCotCall(ctx context.Context, method string, params map[string]any, out any) error {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), guestCotCallFor)
	defer cancel()
	return s.appCall(cctx, method, params, out)
}

// guestCotStep opens the agent message on the first step and appends one tool call. "" when nothing was shown. Calls
// for one conversation are serialized by its own lock; the book's lock is taken only to read and write the record.
func (s core) guestCotStep(ctx context.Context, key, name string) string {
	defer s.t.guests().cotLock(key)()
	t, ok := s.guestCotSnap(key)
	if !ok || t.off {
		return ""
	}
	l := s.botLang(ctx)
	safe := botcard.GuestSafeToolName(name)
	if t.cot == "" {
		var created struct {
			CotID     string `json:"cot_id"`
			MessageID string `json:"message_id"`
		}
		if err := s.guestCotCall(ctx, assistantwire.FeishuCotCreate, map[string]any{"guest": t.id}, &created); err != nil || created.CotID == "" {
			s.logf("lark: guest %s: cot: %v", t.id, err)
			s.guestCotUpdate(key, t.id, func(r *guestTurnRecord) { r.CotOff = true })
			return ""
		}
		t.cot, t.msg = created.CotID, created.MessageID
		if !s.guestCotUpdate(key, t.id, func(r *guestTurnRecord) { r.CotID, r.CotMsg = t.cot, t.msg }) {
			s.guestCotClose(ctx, t) // the turn ended while the message was being made
			return ""
		}
		if err := s.guestCotCall(ctx, assistantwire.FeishuCotAppend, map[string]any{"cot_id": t.cot, "message_id": t.msg,
			"events": botcard.CotStart(guestHash(key), t.id, "p0")}, nil); err != nil {
			s.guestCotOff(ctx, key, t)
			return ""
		}
	}
	n := 0
	if !s.guestCotUpdate(key, t.id, func(r *guestTurnRecord) {
		if r.Steps < guestCotStepsMax {
			r.Steps++
			n = r.Steps
		}
	}) || n == 0 {
		return ""
	}
	step := fmt.Sprintf("s%d", n)
	title := i18n.T(l, "assistant-feishu.guest_cot_step", i18n.Args{"tool": safe})
	if err := s.guestCotCall(ctx, assistantwire.FeishuCotAppend, map[string]any{"cot_id": t.cot, "message_id": t.msg,
		"events": botcard.CotToolCall(step, "p0", safe, title, "default")}, nil); err != nil {
		s.guestCotOff(ctx, key, t)
		return ""
	}
	return step
}

// guestCotResult appends the fixed result line of one step.
func (s core) guestCotResult(key, step string, failed bool) {
	defer s.t.guests().cotLock(key)()
	t, ok := s.guestCotSnap(key)
	if !ok || t.cot == "" || t.off {
		return
	}
	key2 := "assistant-feishu.guest_cot_done"
	if failed {
		key2 = "assistant-feishu.guest_cot_failed"
	}
	ctx := context.Background()
	line := i18n.T(s.botLang(ctx), key2)
	if err := s.guestCotCall(ctx, assistantwire.FeishuCotAppend, map[string]any{"cot_id": t.cot,
		"message_id": t.msg, "events": []botcard.CotEvent{botcard.CotToolResult(step, line, false)}}, nil); err != nil {
		s.guestCotOff(ctx, key, t)
	}
}

// guestCotFinish closes the agent message: finished when the reply was posted, an error otherwise. A turn that called
// no owner-listed tool has no agent message, and nothing is posted for it.
func (s core) guestCotFinish(ctx context.Context, key string, ok bool) {
	defer s.t.guests().cotLock(key)()
	t, found := s.guestCotSnap(key)
	if !found || t.cot == "" {
		return
	}
	if ok {
		_ = s.guestCotCall(ctx, assistantwire.FeishuCotAppend, map[string]any{"cot_id": t.cot, "message_id": t.msg,
			"events": botcard.CotFinish(guestHash(key), t.id, "p0")}, nil)
		return
	}
	s.guestCotClose(ctx, t)
}

// guestCotOff stops the bubble for the turn and closes the one already opened, so it does not keep spinning. The
// conversation's agent-message lock is held.
func (s core) guestCotOff(ctx context.Context, key string, t guestCotTurn) {
	s.guestCotUpdate(key, t.id, func(r *guestTurnRecord) { r.CotOff, r.CotID = true, "" })
	s.guestCotClose(ctx, t)
}

// guestCotClose ends an opened agent message with an error.
func (s core) guestCotClose(ctx context.Context, t guestCotTurn) {
	if t.cot == "" {
		return
	}
	_ = s.guestCotCall(ctx, assistantwire.FeishuCotComplete, map[string]any{"cot_id": t.cot, "message_id": t.msg, "reason": "error"}, nil)
}

// guestHash is a short hash of the conversation key, so the bubble's thread id carries no chat id.
func guestHash(key string) string {
	sum := sha256.Sum256([]byte(key))
	return hex.EncodeToString(sum[:8])
}
