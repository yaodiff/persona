package assistant

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

const (
	inboxFollowDefault = 24 * time.Hour
	inboxFollowMin     = 30 * time.Minute
	inboxFollowMax     = 14 * 24 * time.Hour
)

// suggestFollow is the processing conversation's description of what the item waits on. CheckAt wins over CheckIn;
// with neither, the item is looked at again after 24 hours.
type suggestFollow struct {
	Who     string `json:"who"`
	Next    string `json:"next"`
	CheckAt string `json:"check_at"`
	CheckIn string `json:"check_in"`
}

// follow makes the stored follow-up, with its next look always between 30 minutes and 14 days from now.
func (f *suggestFollow) follow(now time.Time) (*InboxFollow, error) {
	at := now.Add(inboxFollowDefault)
	if raw := strings.TrimSpace(f.CheckAt); raw != "" {
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			return nil, errSuggestion{fmt.Sprintf("follow.check_at %q: use an RFC 3339 time with a time zone", raw)}
		}
		at = parsed
	} else if raw := strings.TrimSpace(f.CheckIn); raw != "" {
		delay, ok := inboxFollowDelay(raw)
		if !ok {
			return nil, errSuggestion{fmt.Sprintf("follow.check_in %q: use a duration such as 30m, 4h or 2d", raw)}
		}
		at = now.Add(delay)
	}
	if earliest := now.Add(inboxFollowMin); at.Before(earliest) {
		at = earliest
	} else if latest := now.Add(inboxFollowMax); at.After(latest) {
		at = latest
	}
	at = at.UTC()
	return &InboxFollow{Who: clip(oneLine(strings.TrimSpace(f.Who)), 80), Next: clip(oneLine(strings.TrimSpace(f.Next)), 80), CheckAt: &at}, nil
}

// inboxFollowDelay reads a duration that may count days ("2d", "2d12h"), which time.ParseDuration does not. Durations
// beyond the follow ceiling saturate: follow clamps them next, however many days were given.
func inboxFollowDelay(raw string) (time.Duration, bool) {
	days, rest, hasDays := strings.Cut(raw, "d")
	if !hasDays {
		d, err := time.ParseDuration(raw)
		return d, err == nil
	}
	negative := strings.HasPrefix(days, "-")
	if negative {
		days = strings.TrimPrefix(days, "-")
	}
	n, err := strconv.Atoi(days)
	if err != nil || n < 0 {
		return 0, false
	}
	var more time.Duration
	if rest != "" {
		more, err = time.ParseDuration(rest)
		if err != nil || more < 0 {
			return 0, false
		}
	}
	d := inboxFollowMax + time.Hour
	if n <= int(inboxFollowMax/(24*time.Hour)) {
		d = time.Duration(n) * 24 * time.Hour
		if more <= inboxFollowMax-d {
			d += more
		} else {
			d = inboxFollowMax + time.Hour
		}
	}
	if negative {
		d = -d
	}
	return d, true
}

// defaultInboxFollow is the follow-up after a reply the user sent in Persona.
func defaultInboxFollow(now time.Time) *InboxFollow {
	at := now.Add(inboxFollowDefault).UTC()
	return &InboxFollow{CheckAt: &at}
}

// ensureInboxFollowCheckAt keeps a waiting matter scheduled. A due check clears CheckAt before waking the processing
// conversation; a turn that only updates the recap, times out or ends idle therefore gets the default next time.
func ensureInboxFollowCheckAt(it *InboxItem, now time.Time) {
	if it.Follow == nil {
		it.Follow = defaultInboxFollow(now)
		return
	}
	if it.Follow.CheckAt == nil {
		at := now.Add(inboxFollowDefault).UTC()
		it.Follow.CheckAt = &at
	}
}

// resetInboxFollowChecks starts the no-news count over when the user takes the matter back into their own hands.
func resetInboxFollowChecks(it *InboxItem) {
	if it.Follow != nil {
		it.Follow.Checks, it.Follow.Due = 0, false
	}
}

// waitForOtherSide records what the item waits on and a server-written chain entry. The processing conversation's own
// rewrite keeps Checks (new messages and user actions reset them before calling this) and clears Due. clearTurn drops
// an interrupted turn's wake and restoration state when this reply or follow is its outcome; a confirmed reply keeps
// any newer work that arrived while it was being sent. Under b.mu.
func (b *inboxBook) waitForOtherSide(it *InboxItem, follow *InboxFollow, via string, now time.Time, loc *time.Location, clearTurn bool) {
	if it.Follow != nil {
		follow.Checks = it.Follow.Checks
	}
	follow.Due = false
	it.Follow = follow
	if clearTurn && it.Agent != nil {
		it.Agent.Wake, it.Agent.Back = false, ""
	}
	b.setHandling(it, HandlingWaiting, "", now)
	key := "waiting_reply"
	args := i18n.Args{"at": follow.CheckAt.In(loc).Format("01-02 15:04")}
	if follow.Who != "" {
		key = "waiting_who"
		args["who"] = follow.Who
	}
	b.chainAdd(it, InboxChainEntry{Kind: ChainWaiting, At: now.UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_chain."+key, args),
		Detail: follow.Next, Via: via})
}
