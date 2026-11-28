package assistant

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Urgent calls (docs/assistant/urgent-call.md, #8734): when a matter can't wait, the assistant rings the owner's iPhone
// like a phone call. Off by default. The model asks with the urgent_call tool; this manager decides whether it may ring
// (the gates in urgent_call_tool.go), has the hub ring the phones (call.ring), and follows the call through: the
// phone's reports, the retry after a missed ring, the P0 push and reminders when nobody answers (urgent_call_loop.go).

const urgentCallSettingsKey = "assistant.urgent_call"

// The built-in "what counts as urgent" rules: the owner may edit or delete them, and restore them.
const (
	urgentMoneyLoss     = "money_loss"
	urgentHarm          = "harm"
	urgentAgentDisaster = "agent_disaster"
)

var builtinUrgentRules = []string{urgentMoneyLoss, urgentHarm, urgentAgentDisaster}

// Limits of the settings.
const (
	urgentCapDefault   = 3
	urgentCapMax       = 10
	urgentRetryDefault = 3 // minutes after a missed ring
	urgentRetryMax     = 30
	urgentRulesMax     = 20
	urgentRuleTextMax  = 200 // runes
)

// urgentRule is one "what counts as urgent" rule. A built-in one without Text uses its definition in the prompt
// (prompts/assistant/urgent_rule_<id>.md); editing it stores the owner's words.
type urgentRule struct {
	ID      string `json:"id"`
	Text    string `json:"text,omitempty"`
	Builtin bool   `json:"builtin,omitempty"`
}

// urgentQuiet is the urgent calls' own quiet hours ("HH:MM", the owner's time zone): no call rings inside them.
type urgentQuiet struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// urgentCallSettings is assistant.urgent_call.
type urgentCallSettings struct {
	Enabled      bool         `json:"enabled"`
	Rules        []urgentRule `json:"rules"`
	DailyCap     int          `json:"daily_cap"`
	Quiet        *urgentQuiet `json:"quiet"`
	RetryMinutes int          `json:"retry_minutes"`
}

func defaultUrgentCallSettings() urgentCallSettings {
	set := urgentCallSettings{DailyCap: urgentCapDefault, RetryMinutes: urgentRetryDefault}
	for _, id := range builtinUrgentRules {
		set.Rules = append(set.Rules, urgentRule{ID: id, Builtin: true})
	}
	return set
}

var errUrgentSettings = errors.New("urgent call settings")

// check validates set as the owner wrote it and puts it in its stored form: limits within range, rule ids unique,
// texts trimmed, a built-in rule's text dropped when it is empty.
func (set *urgentCallSettings) check() error {
	if set.DailyCap == 0 {
		set.DailyCap = urgentCapDefault
	}
	if set.RetryMinutes == 0 {
		set.RetryMinutes = urgentRetryDefault
	}
	if set.DailyCap < 1 || set.DailyCap > urgentCapMax || set.RetryMinutes < 1 || set.RetryMinutes > urgentRetryMax {
		return errUrgentSettings
	}
	if set.Quiet != nil {
		_, okStart := clockMinutes(set.Quiet.Start)
		_, okEnd := clockMinutes(set.Quiet.End)
		if !okStart || !okEnd || set.Quiet.Start == set.Quiet.End {
			return errUrgentSettings
		}
	}
	if len(set.Rules) > urgentRulesMax {
		return errUrgentSettings
	}
	seen := map[string]bool{}
	rules := make([]urgentRule, 0, len(set.Rules))
	for _, r := range set.Rules {
		r.ID = strings.TrimSpace(r.ID)
		r.Text = strings.Join(strings.Fields(r.Text), " ")
		r.Builtin = slices.Contains(builtinUrgentRules, r.ID)
		if r.ID == "" || seen[r.ID] || len(r.ID) > 40 || utf8.RuneCountInString(r.Text) > urgentRuleTextMax || (!r.Builtin && r.Text == "") {
			return errUrgentSettings
		}
		seen[r.ID] = true
		rules = append(rules, r)
	}
	set.Rules = rules
	return nil
}

// clockMinutes reads "HH:MM" as minutes after midnight.
func clockMinutes(s string) (int, bool) {
	t, err := time.Parse("15:04", s)
	if err != nil {
		return 0, false
	}
	return t.Hour()*60 + t.Minute(), true
}

// inQuiet: at falls inside the quiet hours (which may run past midnight).
func (q *urgentQuiet) inQuiet(at time.Time) bool {
	if q == nil {
		return false
	}
	start, ok1 := clockMinutes(q.Start)
	end, ok2 := clockMinutes(q.End)
	if !ok1 || !ok2 {
		return false
	}
	now := at.Hour()*60 + at.Minute()
	if start < end {
		return now >= start && now < end
	}
	return now >= start || now < end
}

// urgentSettings is the stored setting, or the default (off) before the owner changed it.
func (s *Service) urgentSettings(ctx context.Context) (urgentCallSettings, error) {
	set := defaultUrgentCallSettings()
	st := s.store()
	if st == nil {
		return set, nil
	}
	var stored urgentCallSettings
	found, err := st.Setting(ctx, urgentCallSettingsKey, &stored)
	if err != nil || !found {
		return set, err
	}
	if stored.check() != nil {
		s.logf("assistant: %s is not valid: urgent calls stay off", urgentCallSettingsKey)
		return set, nil
	}
	return stored, nil
}

// urgentOn: the owner turned urgent calls on (a store that cannot be read counts as off).
func (s *Service) urgentOn(ctx context.Context) bool {
	set, err := s.urgentSettings(ctx)
	return err == nil && set.Enabled
}

// urgentRuleText is what rule says: the owner's words, or a built-in rule's definition in language (the owner's,
// urgentLanguage; "" is Chinese).
func urgentRuleText(r urgentRule, language string) string {
	if r.Text != "" || !r.Builtin {
		return r.Text
	}
	return prompts.UrgentRuleBuiltinIn(r.ID, language)
}

// urgentLanguage is the owner's language for the built-in rules' words, as the assistant answers in it.
func (s *Service) urgentLanguage(ctx context.Context) string {
	return answerLanguage(s.hello(ctx).Locale)
}

// urgentPrompt is the urgent-call block of the assistant's instructions; nil while urgent calls are off, so an
// assistant without them sees nothing of them.
func (s *Service) urgentPrompt(ctx context.Context) *prompts.UrgentCall {
	set, err := s.urgentSettings(ctx)
	if err != nil || !set.Enabled {
		return nil
	}
	out := &prompts.UrgentCall{}
	language := s.urgentLanguage(ctx)
	for _, r := range set.Rules {
		out.Rules = append(out.Rules, prompts.UrgentCallRule{ID: r.ID, Text: urgentRuleText(r, language)})
	}
	return out
}

// urgentStore is what urgent calls keep (db.DB); without it they never ring.
type urgentStore interface {
	InsertUrgentCall(ctx context.Context, c db.UrgentCall) error
	UrgentCall(ctx context.Context, id string) (db.UrgentCall, bool, error)
	UrgentCallsSince(ctx context.Context, since time.Time) ([]db.UrgentCall, error)
	UrgentCallsDue(ctx context.Context, now time.Time) ([]db.UrgentCall, time.Time, error)
	UpdateUrgentCall(ctx context.Context, c db.UrgentCall) (bool, error)
}

func (s *Service) urgentStore() urgentStore {
	st, _ := s.store().(urgentStore)
	return st
}

// callRinger is the hub's call.ring (BusHub); a hub without it rings nothing.
type callRinger interface {
	CallRing(ctx context.Context, c assistantwire.CallRing) (assistantwire.CallRingResult, error)
}

// CallRing is call.ring: the hub rings the owner's phones once.
func (b *BusHub) CallRing(ctx context.Context, c assistantwire.CallRing) (assistantwire.CallRingResult, error) {
	res, err := buswire.Call(ctx, b.client, assistantwire.CallRingMethod, assistantwire.CallRingRequest{Call: c})
	return res, busError(err)
}

// startOfDay is when the owner's day began, in their time zone.
func (s *Service) startOfDay(ctx context.Context, now time.Time) time.Time {
	t := now.In(s.location(ctx))
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
}

// ---- the apps' routes ----

func (s *Service) urgentCallRoutes() {
	s.handle("GET /api/v1/assistant/urgent-call", s.getUrgentSettings)
	s.handle("PUT /api/v1/assistant/urgent-call", s.putUrgentSettings)
	s.handle("POST /api/v1/assistant/urgent-call/test", s.postUrgentTest)
	s.handle("GET /api/v1/assistant/calls", s.listUrgentCalls)
	s.handle("GET /api/v1/assistant/calls/{id}", s.getUrgentCall)
	s.handle("POST /api/v1/assistant/calls/{id}/state", s.postUrgentCallState)
}

func urgentErr(w http.ResponseWriter, status int, code string) {
	apierr.WriteCode(w, status, "urgent_call_"+code, i18n.Err("assistant-urgent_call."+code))
}

// urgentSettingsView is the settings as the app's Settings › Urgent calls shows them: how many matters rang today.
func (s *Service) writeUrgentSettings(w http.ResponseWriter, r *http.Request, set urgentCallSettings) {
	today := 0
	if st := s.urgentStore(); st != nil {
		if calls, err := st.UrgentCallsSince(r.Context(), s.startOfDay(r.Context(), time.Now())); err == nil {
			today = countedMatters(calls)
		}
	}
	apierr.WriteJSON(w, map[string]any{"settings": set, "builtin": builtinUrgentRules, "today": today, "can_write": fullRole(r)})
}

func (s *Service) getUrgentSettings(w http.ResponseWriter, r *http.Request) {
	set, err := s.urgentSettings(r.Context())
	if err != nil {
		urgentErr(w, 503, "store")
		return
	}
	s.writeUrgentSettings(w, r, set)
}

func (s *Service) putUrgentSettings(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		urgentErr(w, 403, "read_only")
		return
	}
	st := s.store()
	if st == nil {
		urgentErr(w, 503, "store")
		return
	}
	var set urgentCallSettings
	if err := decodeBody(w, r, &set, 32<<10); err != nil {
		writeBodyErr(w, err, err)
		return
	}
	if set.check() != nil {
		urgentErr(w, 400, "invalid")
		return
	}
	before, _ := s.urgentSettings(r.Context())
	if err := st.PutSetting(r.Context(), urgentCallSettingsKey, set); err != nil {
		urgentErr(w, 503, "store")
		return
	}
	if before.Enabled != set.Enabled {
		s.urgentToolsChanged() // the urgent_call tool comes and goes with the switch
	}
	// The rules are in the assistant's instructions: instructionsLoop writes them within a minute, and the node starts
	// the conversation again with them (and the tool list) on its next idle message.
	s.writeUrgentSettings(w, r, set)
}

// urgentCallView is a call as the phone and the conversation's record row show it.
func urgentCallView(c db.UrgentCall) map[string]any {
	v := map[string]any{"id": c.ID, "title": c.Title, "body": c.Body, "why": c.Why, "happening": c.Happening,
		"machine_id": c.MachineID, "conversation_id": c.Conversation, "interaction_id": c.InteractionID, "state": c.State,
		"attempt": c.Attempt, "created_at": c.CreatedAt, "seconds": c.Seconds, "outcome": c.Outcome, "transcript": c.Transcript}
	if c.RuleID != "" {
		v["rule"] = map[string]any{"id": c.RuleID, "text": c.RuleText}
	}
	for k, t := range map[string]*time.Time{"rang_at": c.RangAt, "answered_at": c.AnsweredAt, "ended_at": c.EndedAt} {
		if t != nil {
			v[k] = *t
		}
	}
	return v
}

// urgentListDays and urgentListMax bound GET /api/v1/assistant/calls: the calls a conversation shows as record rows.
const (
	urgentListDays = 30
	urgentListMax  = 50
)

// listUrgentCalls is a conversation's urgent calls of the last 30 days, oldest first (?conversation=<id>), for the
// record rows in the conversation; test calls are left out.
func (s *Service) listUrgentCalls(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		urgentErr(w, 403, "read_only")
		return
	}
	conv := r.URL.Query().Get("conversation")
	if conv == "" {
		urgentErr(w, 400, "invalid")
		return
	}
	st := s.urgentStore()
	if st == nil {
		urgentErr(w, 503, "store")
		return
	}
	calls, err := st.UrgentCallsSince(r.Context(), time.Now().Add(-urgentListDays*24*time.Hour))
	if err != nil {
		urgentErr(w, 503, "store")
		return
	}
	out := []map[string]any{}
	for _, c := range calls {
		if c.Conversation == conv && c.Why != urgentWhyTest {
			out = append(out, urgentCallView(c))
		}
	}
	if len(out) > urgentListMax {
		out = out[len(out)-urgentListMax:]
	}
	apierr.WriteJSON(w, map[string]any{"calls": out})
}

func (s *Service) getUrgentCall(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		urgentErr(w, 403, "read_only")
		return
	}
	st := s.urgentStore()
	if st == nil {
		urgentErr(w, 503, "store")
		return
	}
	c, ok, err := st.UrgentCall(r.Context(), r.PathValue("id"))
	switch {
	case err != nil:
		urgentErr(w, 503, "store")
	case !ok:
		urgentErr(w, 404, "not_found")
	default:
		apierr.WriteJSON(w, urgentCallView(c))
	}
}

// What a phone reports about a call (POST /api/v1/assistant/calls/{id}/state).
const (
	callRinging   = "ringing"
	callAnswered  = "answered"
	callDeclined  = "declined"
	callMissed    = "missed"
	callEnded     = "ended"
	callFailed    = "failed"
	callCancelled = "cancelled"
	callSeen      = "seen" // the owner opened the matter from a missed call's notification: no more reminders
)

// Limits of what a phone reports.
const (
	callTranscriptMax = 20000 // runes
	callOutcomeMax    = 500
	callSecondsMax    = 4 * 3600
)

func (s *Service) postUrgentCallState(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		urgentErr(w, 403, "read_only")
		return
	}
	st := s.urgentStore()
	if st == nil {
		urgentErr(w, 503, "store")
		return
	}
	var body struct {
		State      string `json:"state"`
		Seconds    int    `json:"seconds"`
		Outcome    string `json:"outcome"`
		Transcript string `json:"transcript"`
	}
	if err := decodeBody(w, r, &body, 256<<10); err != nil {
		writeBodyErr(w, err, err)
		return
	}
	switch body.State {
	case callAnswered, callDeclined, callMissed, callEnded, callSeen:
	default:
		urgentErr(w, 400, "invalid")
		return
	}
	report := callReport{state: body.State, seconds: min(max(body.Seconds, 0), callSecondsMax),
		outcome: clipRunesTo(strings.TrimSpace(body.Outcome), callOutcomeMax), transcript: clipRunesTo(strings.TrimSpace(body.Transcript), callTranscriptMax)}
	c, err := s.reportCall(r.Context(), st, r.PathValue("id"), report, time.Now())
	switch {
	case errors.Is(err, errCallNotFound):
		urgentErr(w, 404, "not_found")
	case errors.Is(err, errCallState):
		urgentErr(w, 409, "state")
	case err != nil:
		urgentErr(w, 503, "store")
	default:
		apierr.WriteJSON(w, urgentCallView(c))
	}
}

// postUrgentTest rings the phones once with a test call (Settings › Urgent calls › Test call): not a matter, never
// retried or followed by a push, and outside the day's count.
func (s *Service) postUrgentTest(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		urgentErr(w, 403, "read_only")
		return
	}
	st := s.urgentStore()
	if st == nil {
		urgentErr(w, 503, "store")
		return
	}
	set := s.settings(r.Context())
	l := s.lang(r.Context())
	now := time.Now()
	c := db.UrgentCall{ID: "call_" + randomHex(8), MachineID: set.MachineID, Conversation: set.Main, Matter: "test:" + randomHex(4),
		Title: i18n.T(l, "assistant-urgent_call.test_title"), Body: i18n.T(l, "assistant-urgent_call.test_body"), Why: urgentWhyTest,
		State: callRinging, Attempt: 1, CreatedAt: now}
	res, err := s.ringNew(r.Context(), st, &c, now)
	if err != nil {
		urgentErr(w, 503, "store")
		return
	}
	apierr.WriteJSON(w, map[string]any{"call": urgentCallView(c), "devices": res.Devices, "reason": res.Reason})
}
