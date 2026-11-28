package briefing

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/modapi"
)

// SettingKey is the persona_settings key of Waits (kept in the assistant's settings, field briefing_waits).
const SettingKey = "assistant.briefing_waits"

// Waits tunes the deterministic wait lists, not the supervisor's own signals. The defaults deliberately reuse the
// existing supervision floors: a working agent is stalled after 20 minutes, any other silent session after two hours,
// an offline agent is orphaned after ten minutes, and an unanswered card waits three minutes (the core's test pins
// them to superviseStallMin, superviseSilentMin, orphanOfflineAfter and superviseNeedsAfter). MaxRows is a display
// bound per direction; owner-wait rows are always kept in full.
type Waits struct {
	StalledMinutes int `json:"stalled_minutes"`
	SilentMinutes  int `json:"silent_minutes"`
	OrphanMinutes  int `json:"orphan_minutes"`
	CardMinutes    int `json:"card_minutes"`
	MaxRows        int `json:"max_rows"`
}

// Defaults are the waits when none are set.
func Defaults() Waits {
	return Waits{StalledMinutes: 20, SilentMinutes: 120, OrphanMinutes: 10, CardMinutes: 3, MaxRows: 15}
}

// waitsOf is the waits in force for what is stored: the defaults when nothing (or nothing readable) is.
func waitsOf(stored json.RawMessage) Waits {
	out := Defaults()
	if len(stored) == 0 || string(stored) == "null" {
		return out
	}
	var w Waits
	if json.Unmarshal(stored, &w) != nil {
		return out
	}
	return w
}

// Validate says whether w may be stored.
func (w Waits) Validate() error {
	for _, n := range []int{w.StalledMinutes, w.SilentMinutes, w.OrphanMinutes, w.CardMinutes} {
		if n < 1 || n > 7*24*60 {
			return errors.New("minute thresholds must be whole numbers, 1–10080")
		}
	}
	if w.MaxRows < 1 || w.MaxRows > 100 {
		return errors.New("max_rows must be a whole number, 1–100")
	}
	return nil
}

func value(stored json.RawMessage) json.RawMessage {
	raw, _ := json.Marshal(waitsOf(stored))
	return raw
}

func check(proposed json.RawMessage) (json.RawMessage, error) {
	var next *Waits
	if err := json.Unmarshal(proposed, &next); err != nil {
		return nil, errors.New("value must be the whole BriefingWaits object, or null for the defaults")
	}
	if next == nil {
		return nil, nil
	}
	if err := next.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(next)
}

func describe(v json.RawMessage) string {
	w := waitsOf(v)
	return fmt.Sprintf("stalled=%dm, silent=%dm, orphan=%dm, cards=%dm, max_rows=%d", w.StalledMinutes, w.SilentMinutes, w.OrphanMinutes, w.CardMinutes, w.MaxRows)
}

func (w Waits) CardAfter() time.Duration   { return time.Duration(w.CardMinutes) * time.Minute }
func (w Waits) OrphanAfter() time.Duration { return time.Duration(w.OrphanMinutes) * time.Minute }

func (w Waits) StalledAfter(expect int) time.Duration {
	return max(time.Duration(w.StalledMinutes)*time.Minute, time.Duration(expect)*time.Minute*3/2)
}

func (w Waits) SilentAfter(expect int) time.Duration {
	return max(time.Duration(w.SilentMinutes)*time.Minute, time.Duration(expect)*time.Minute*3/2)
}

// OwnerWait says whether a row of kind waits on the owner: such rows are never folded.
func OwnerWait(kind string) bool {
	return kind == "needs_user" || kind == "question" || kind == "permission" || kind == "private"
}

// FoldDirection keeps every owner wait, then up to MaxRows other waits. Each group is oldest first.
func (w Waits) FoldDirection(d *modapi.BriefDirection) {
	rows := append([]modapi.BriefWait{}, d.Waits...)
	slices.SortStableFunc(rows, func(a, b modapi.BriefWait) int {
		if ao, bo := OwnerWait(a.Kind), OwnerWait(b.Kind); ao != bo {
			if ao {
				return -1
			}
			return 1
		}
		if c := a.Since.Compare(b.Since); c != 0 {
			return c
		}
		return strings.Compare(a.TodoID+a.InteractionID, b.TodoID+b.InteractionID)
	})
	keep := len(rows)
	for i, row := range rows {
		if !OwnerWait(row.Kind) {
			keep = min(len(rows), i+w.MaxRows)
			break
		}
	}
	d.Waits, d.More = rows[:keep], len(rows)-keep
}
