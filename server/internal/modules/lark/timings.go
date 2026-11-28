package lark

import "time"

// The module's times, by name: the defaults, which Host.Timings overrides (a test and tuning knob of the core's,
// assistant.Options.LarkTimings; never a user setting).
var (
	defaultTimings = map[string]time.Duration{
		// answer_card_wait is how long the answer waits out a refused update of the progress card before giving up on
		// the card: the run is over, so no later update writes it again (cardBackoffFirst).
		"answer_card_wait": cardBackoffFirst,
		// kit_check_every is how often an entity's message is read back to see it is still there (kitGone).
		"kit_check_every": 30 * time.Second,
		// progress_after and progress_idle: the Feishu card comes a second or two after the message and its elapsed
		// time moves every few seconds, new steps or not: a long turn must not look stuck.
		"progress_after": time.Second,
		"progress_idle":  3 * time.Second,
		// cot_unified_grace is how long, of the turn's took, unified mode holds the progress card back while no agent
		// message is open yet (#16847): a text-only turn past it gets its card and stays a card turn.
		"cot_unified_grace": 10 * time.Second,
		// cot_answer_wait is how long the answer waits for the worker's terminal result in unified mode before sending
		// cards (#16847).
		"cot_answer_wait": 25 * time.Second,
		// turn_wait is how long a message of the chat waits for the one before it (feishuTurnWait).
		"turn_wait": feishuTurnWait,
		// topic_lookup_wait is how long the topic of a message just sent waits between two reads of it
		// (topicLookupWait).
		"topic_lookup_wait": topicLookupWait,
		// card_answer_wait is how long a press waits for its answer to go through before it says "Processing…"
		// (cardAnswerWait).
		"card_answer_wait": cardAnswerWait,
		// start_card_window and form_window: how long a repeat press of the same /start card, and of a card's "Start
		// new agent" button, is ignored (botStartCardWindow, botFormWindow).
		"start_card_window": botStartCardWindow,
		"form_window":       botFormWindow,
		// card_recheck is how long after a reconnection the cards that may still be waiting are looked at again (the
		// manager restores its interactions meanwhile; closeEndedBotCards).
		"card_recheck": 90 * time.Second,
		// mirror_retry is the pause before another attempt to deliver a mirror or a to-do reminder.
		"mirror_retry": mirrorRetry,
		// read_working_after is how long a held /read works before its owner is told privately (readWorkingAfter).
		"read_working_after": readWorkingAfter,
	}
	defaultTimingLists = map[string][]time.Duration{
		// cot_end_tries are the waits between tries of cotEnd (a rate limit that hit the end likely hits the first
		// try too).
		"cot_end_tries": {0, time.Second, 3 * time.Second},
	}
)

// timing is the time called name: the host's, else the default.
func (s core) timing(name string) time.Duration {
	if one, _ := s.h.Timings(); one != nil {
		if d, ok := one[name]; ok {
			return d
		}
	}
	return defaultTimings[name]
}

// timingList is the list of times called name: the host's, else the default.
func (s core) timingList(name string) []time.Duration {
	if _, lists := s.h.Timings(); lists != nil {
		if l, ok := lists[name]; ok {
			return l
		}
	}
	return defaultTimingLists[name]
}
