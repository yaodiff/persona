package assistant

import "time"

// Following a matter up (docs/assistant/inbox.md "Following up"): an item stays one matter for as long as it goes on.
// It says where it happens (InboxItem.Where), who wrote its title (TitleBy), what it waits on and when to look at it
// again (Follow), and, as listed, its state as a key (Now) that every app puts in words from the same table
// (spec/fixtures/inbox/now.json).

// Who wrote an item's title and summary (InboxItem.TitleBy).
const (
	TitleByModel     = "model"     // the grouping model
	TitleByRules     = "rules"     // the connector's fallback grouping
	TitleByAssistant = "assistant" // the processing conversation
	TitleByUser      = "user"      // the user
	TitleByMerged    = "merged"    // the model, for rows the user merged (inbox_merge_title.go): grouping leaves it alone
)

// InboxFollow is what a matter waits on: who or what (Who, in the user's words), the next step (Next), when to look
// at it again (CheckAt), how many times in a row it was looked at again with nothing new (Checks), and whether that
// time came with nobody to look (Due: the AI twin was off, so it went back to the user).
type InboxFollow struct {
	Who     string     `json:"who,omitempty"`
	Next    string     `json:"next,omitempty"`
	CheckAt *time.Time `json:"check_at,omitempty"`
	Checks  int        `json:"checks,omitempty"`
	Due     bool       `json:"due,omitempty"`
}

// State keys (InboxItem.Now). The apps put each in words; "" (an older hub) leaves them on their own state words.
const (
	NowConfirmReply   = "confirm_reply"   // a reply suggestion waits for the user
	NowConfirmAgent   = "confirm_agent"   // an Agent suggestion waits for the user to start it
	NowConfirmTodo    = "confirm_todo"    // a to-do suggestion waits for the user to add it (#6480)
	NowDecide         = "decide"          // a suggestion handed back: the user decides
	NowQueued         = "queued"          // queued for the AI
	NowLooking        = "looking"         // the AI is looking
	NowAgentWorking   = "agent_working"   // an Agent it started is working
	NowWaiting        = "waiting"         // waiting on the other side (Follow says on whom, when to look again, the next step)
	NowFollowUpAsk    = "follow_up_ask"   // looked at again three times with nothing new: back to the user
	NowCheckDue       = "check_due"       // the time to look again came while the AI twin was off: back to the user
	NowFailed         = "failed"          // the AI could not finish (Handling.Error says why)
	NowNeedsYou       = "needs_you"       // needs the user, nothing prepared
	NowFYI            = "fyi"             // worth knowing
	NowFolded         = "folded"          // folded
	NowDoneReplied    = "done_replied"    // done: the user replied
	NowDoneManager    = "done_manager"    // done: the AI looked, nothing to do
	NowDoneUser       = "done_user"       // done: the user marked it done
	NowDoneRead       = "done_read"       // done: read elsewhere
	NowDoneDismissed  = "done_dismissed"  // done: the user dismissed it
	NowDoneMuted      = "done_muted"      // done: muted (done_by read; the newest done entry has By "muted")
	NowDoneSuperseded = "done_superseded" // done: a newer item took its place
)

// inboxFollowUpAskChecks is how many consecutive looks with nothing new send the matter back to the user.
const inboxFollowUpAskChecks = 3

// nowKey is the item's state key, from the item alone (no clock, no settings: the list memo holds,
// inbox_list_memo.go). An unknown done_by has no mapped key: leave Now empty so the apps keep their own done words.
func (it *InboxItem) nowKey() string {
	if !it.open() {
		// Replied and superseded filings do not add a done entry, so an old muted entry is stale for them.
		if it.DoneBy == "read" {
			for i := len(it.Chain) - 1; i >= 0; i-- {
				if it.Chain[i].Kind != ChainDone {
					continue
				}
				if it.Chain[i].By == DoneByMuted {
					return NowDoneMuted
				}
				break // only the newest done entry describes this filing
			}
		}
		switch it.DoneBy {
		case "replied":
			return NowDoneReplied
		case doneByManager:
			return NowDoneManager
		case "user":
			return NowDoneUser
		case "read":
			return NowDoneRead
		case "dismissed":
			return NowDoneDismissed
		case "superseded":
			return NowDoneSuperseded
		default:
			return ""
		}
	}
	if it.Tier == TierNoise {
		return NowFolded
	}
	g := currentSuggestion(it)
	if g != nil && g.Kind != SuggestConclusion {
		if g.Handback {
			return NowDecide
		}
		switch g.Kind {
		case SuggestReply:
			return NowConfirmReply
		case SuggestAgent:
			return NowConfirmAgent
		case SuggestTodo:
			return NowConfirmTodo
		}
	}
	// A conclusion suppresses processing in status() too: it falls through to follow-up and tier rather than looking
	// like actionable work. Known action suggestions returned above, so this guard does not hide their queued agent.
	if g == nil {
		if h := it.Handling; h != nil {
			if a := it.Agent; a != nil && a.State == AgentQueued && a.SessionID == "" && h.State == HandlingLooking {
				return NowQueued
			}
			switch h.State {
			case HandlingLooking:
				return NowLooking
			case HandlingAgent:
				return NowAgentWorking
			case HandlingWaiting:
				return NowWaiting
			case HandlingFailed:
				return NowFailed
			}
		}
	}
	if f := it.Follow; f != nil {
		if f.Checks >= inboxFollowUpAskChecks {
			return NowFollowUpAsk
		}
		if f.Due {
			return NowCheckDue
		}
	}
	if it.Tier == TierFYI {
		return NowFYI
	}
	return NowNeedsYou
}
