package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	nativeLoopNoopRounds = 6
	nativeLoopNoopAge    = 3 * time.Hour
)

// The latest completed turn stores the streak, so replaying a turn or
// restarting the manager does not count it again.
type nativeLoopNoopStreak struct {
	Job      proto.NativeLoopID `json:"job"`
	Since    time.Time          `json:"since"`
	End      time.Time          `json:"end"`
	Count    int                `json:"count"`
	Evidence string             `json:"evidence,omitempty"`
}

func nativeLoopStart(items []proto.Item) proto.Item {
	sawEnd := false
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		switch {
		case it.Kind == "turn_duration":
			if sawEnd {
				return proto.Item{}
			}
			sawEnd = true
		case it.NativeLoopID != "":
			return it
		case it.Wake, it.Kind == proto.KindText && it.Role == "user":
			return proto.Item{} // a different wake or input superseded the scheduled check
		}
	}
	return proto.Item{}
}

// The first line may give the reason that a check had no new result.
var nativeNoopHeadline = lazyre.New(`(?i)^(?:[*` + "`" + ` ]*(?:no-?op|no changes?|无变化|无实质变化|変化なし)[*` + "`" + ` ]*(?:$|[—:：,，。.!！-])|.*\*\*(?:no-?op)[,，—。（:：])`) // i18n-ignore: matches the original reply

func nativeLoopNoopEvidence(t Todo, fire proto.Item, reply string) string {
	if fire.NativeLoopID == "" || fire.TS.IsZero() || t.Supervision == nil {
		return ""
	}
	head, _, _ := strings.Cut(closeout.Strip(reply), "\n")
	if !nativeNoopHeadline.MatchString(strings.TrimSpace(head)) {
		return ""
	}
	report, artifacts, parsed := closeout.Parse(reply)
	if parsed && (report.Status != proto.CloseoutDone || report.Next != "" || report.Merged == "yes" ||
		report.Deployed == "yes" || report.Delivery != "" && report.Delivery != "none") {
		return ""
	}
	v := t.Supervision
	if v.Progress != nil && !v.Progress.At.Before(fire.TS) ||
		slices.ContainsFunc(v.Criteria, func(c db.TodoCriterion) bool {
			return c.At != nil && !c.At.Before(fire.TS)
		}) {
		return ""
	}
	// The same pending conditions and artifacts can accompany a no-change
	// result. Bookkeeping such as appending a check to the same log can vary.
	payload, _ := json.Marshal([]any{report.Missing, report.Waiting, report.Merged, report.Deployed, artifacts})
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:])
}

func nextNativeLoopNoop(t Todo, prev taskTurn, row proto.Session, fire proto.Item, end time.Time, evidence string) *nativeLoopNoopStreak {
	next := &nativeLoopNoopStreak{Job: fire.NativeLoopID, Since: fire.TS, End: end, Count: 1, Evidence: evidence}
	old := prev.LoopNoop
	if old != nil && prev.Machine == row.MachineID && prev.Session == row.SessionID &&
		old.Job == fire.NativeLoopID && old.Evidence == evidence && fire.TS.After(old.End) &&
		(t.ReopenedAt == nil || !old.Since.Before(*t.ReopenedAt)) {
		next.Since, next.Count = old.Since, old.Count+1
	}
	return next
}

func (s *Service) nativeLoopNoopSignal(ctx context.Context, t Todo) (superviseSignal, bool) {
	if t.DoneAt != nil || t.Supervision == nil {
		return superviseSignal{}, false
	}
	switch t.Supervision.state(t) {
	case reviewClosing, reviewNeedsUser, reviewUserCheck:
		return superviseSignal{}, false
	}
	e, ok := s.turnOf(ctx, t.ID)
	streak := e.LoopNoop
	if !ok || streak == nil || streak.Count < nativeLoopNoopRounds ||
		streak.End.Sub(streak.Since) < nativeLoopNoopAge ||
		t.ReopenedAt != nil && streak.Since.Before(*t.ReopenedAt) ||
		!slices.ContainsFunc(t.Agents, func(a TodoAgent) bool {
			return a.active() && a.MachineID == e.Machine && a.SessionID == e.Session
		}) {
		return superviseSignal{}, false
	}
	return superviseSignal{
		Todo: t.ID, Kind: sigLoopNoop, Machine: e.Machine, Session: e.Session,
		Key:    "loop-noop@" + streak.Since.UTC().Format(time.RFC3339Nano),
		Detail: i18n.T(s.lang(ctx), "assistant-tasks.native_loop_noop"),
	}, true
}

func (s *Service) signalNativeLoopNoop(ctx context.Context, t Todo) {
	if g, ok := s.nativeLoopNoopSignal(ctx, t); ok {
		s.signal(g)
	}
}

// A later supervisor decision supersedes the round that was already running.
func (s *Service) finishNativeLoopPark(ctx context.Context, t Todo, began time.Time) {
	held := t.Supervision.parking()
	if held == nil || held.At.After(began) {
		return
	}
	if _, _, err := s.updateSupervisionGuarded(ctx, t.ID, func(cur Todo) bool {
		park := cur.Supervision.parking()
		return cur.DoneAt == nil && park != nil && park.At.Equal(held.At)
	}, func(v *Supervision) bool {
		v.Waits, v.State = nil, ""
		if v.Round > 0 {
			v.State = reviewRework
		}
		return true
	}); err != nil {
		s.logf("task %s: finish a native loop's park: %v", t.ID, err)
	}
}
