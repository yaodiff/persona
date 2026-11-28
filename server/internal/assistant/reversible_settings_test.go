package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	reversibleBriefingValue        = `{"stalled_minutes":20,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":16}`
	reversibleBriefingDefaultValue = `{"stalled_minutes":20,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":15}`
)

type reversibleSettingCase struct {
	key   string
	value string
	raw   func(Settings) any
}

var reversibleSettingCases = []reversibleSettingCase{
	{key: "assistant.main_background_folded", value: `false`, raw: func(v Settings) any { return v.MainBackgroundFolded }},
	{key: "assistant.recent_task_chats", value: `true`, raw: func(v Settings) any { return v.RecentTaskChats }},
	{key: "assistant.briefing_waits", value: reversibleBriefingValue, raw: func(v Settings) any { return v.BriefingWaits }},
}

func newReversibleSettingService(t *testing.T, on bool) (*Service, *fleetMem) {
	t.Helper()
	f := newFakeHub().withMachine()
	f.hello.Assistant.AutoApproveReversibleSettings = on
	st := newFleetMem()
	return newServiceOn(t, f, st), st
}

func callReversibleSetting(ctx context.Context, s *Service, key, value string) proto.ToolResult {
	return callReversibleSettingAction(ctx, s, "set", key, value, "conv1")
}

func callReversibleSettingAction(ctx context.Context, s *Service, action, key, value, session string) proto.ToolResult {
	args := fmt.Sprintf(`{"action":%q,"key":%q,"value":%s}`, action, key, value)
	return s.Tool(ctx, ToolCall{Name: "persona_settings", Arguments: json.RawMessage(args), MachineID: "server", SessionID: session})
}

func reversibleResultText(r proto.ToolResult) string {
	var out []string
	for _, part := range r.Content {
		out = append(out, part.Text)
	}
	return strings.Join(out, "\n")
}

func requireUnavailableBriefingAction(t *testing.T, s *Service, action, value string) {
	t.Helper()
	const key = "assistant.briefing_waits"
	f := s.hub.(*fakeHub)
	f.answerAppBlocking(AppSettings, AppTool, func(json.RawMessage) (any, error) {
		return proto.TextResult("unexpected settings call to the hub", true), nil
	})
	before := len(f.appParams(AppSettings, AppTool))
	r := callReversibleSettingAction(t.Context(), s, action, key, value, "conv1")
	if got := len(f.appParams(AppSettings, AppTool)); got != before {
		t.Fatalf("manager sent its unavailable setting to the hub: %d additional calls", got-before)
	}
	if !r.IsError || !strings.Contains(reversibleResultText(r), key) {
		t.Fatalf("unavailable briefing setting %s = %+v", action, r)
	}
}

func requireAutomaticReversibleSet(t *testing.T, s *Service, key, value string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	r := callReversibleSetting(ctx, s, key, value)
	if r.IsError {
		t.Fatalf("automatic set %s: %s", key, reversibleResultText(r))
	}
	if pending := s.pendingQuestions(); len(pending) != 0 {
		t.Fatalf("automatic set %s left a pending card: %+v", key, pending)
	}
}

func requireReversibleApproval(t *testing.T, s *Service, key, value string, allow bool) proto.Interaction {
	t.Helper()
	return requireReversibleApprovalAction(t, s, "set", key, value, "conv1", allow)
}

func requireReversibleApprovalAction(t *testing.T, s *Service, action, key, value, session string, allow bool) proto.Interaction {
	t.Helper()
	type answered struct {
		q   proto.Interaction
		ok  bool
		err error
	}
	answer := make(chan answered, 1)
	go func() {
		q, ok := awaitCard(s)
		if !ok {
			answer <- answered{}
			return
		}
		raw, _ := json.Marshal(proto.Resolution{Allow: &allow})
		_, err := s.Action(context.Background(), Action{Op: "answer", InteractionID: q.ID, Answer: raw})
		answer <- answered{q: q, ok: true, err: err}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	r := callReversibleSettingAction(ctx, s, action, key, value, session)
	select {
	case a := <-answer:
		if !a.ok {
			t.Fatalf("%s did not produce an approval card; tool result: %s", key, reversibleResultText(r))
		}
		if a.err != nil {
			t.Fatalf("answer %s: %v", key, a.err)
		}
		if allow && r.IsError {
			t.Fatalf("approved %s: %s", key, reversibleResultText(r))
		}
		if a.q.Kind != proto.IxPermission || a.q.ToolName != "persona_settings" || a.q.SessionID != session {
			t.Fatalf("approval card: %+v", a.q)
		}
		return a.q
	case <-ctx.Done():
		t.Fatalf("%s approval did not finish: %v; tool result: %s", key, ctx.Err(), reversibleResultText(r))
		return proto.Interaction{}
	}
}

func requireReversibleRaw(t *testing.T, st *fleetMem, tc reversibleSettingCase, want string) {
	t.Helper()
	var saved Settings
	ok, err := st.Setting(context.Background(), settingsKey, &saved)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		saved = Settings{}
	}
	got, err := json.Marshal(tc.raw(saved))
	if err != nil {
		t.Fatal(err)
	}
	if !reversibleJSONEqual(got, json.RawMessage(want)) {
		t.Fatalf("%s raw value = %s, want %s", tc.key, got, want)
	}
}

func reversibleJSONEqual(a, b json.RawMessage) bool {
	var av, bv any
	return json.Unmarshal(a, &av) == nil && json.Unmarshal(b, &bv) == nil && reflect.DeepEqual(av, bv)
}

func putReversibleRaw(t *testing.T, st *fleetMem, tc reversibleSettingCase, raw string) {
	t.Helper()
	var saved Settings
	if raw != `null` {
		switch tc.key {
		case "assistant.main_background_folded":
			var value bool
			if err := json.Unmarshal([]byte(raw), &value); err != nil {
				t.Fatal(err)
			}
			saved.MainBackgroundFolded = &value
		case "assistant.recent_task_chats":
			var value bool
			if err := json.Unmarshal([]byte(raw), &value); err != nil {
				t.Fatal(err)
			}
			saved.RecentTaskChats = &value
		case "assistant.briefing_waits":
			saved.BriefingWaits = moduleValue(json.RawMessage(raw))
		default:
			t.Fatalf("unknown reversible setting %q", tc.key)
		}
	}
	if err := st.PutSetting(context.Background(), settingsKey, saved); err != nil {
		t.Fatal(err)
	}
}

type reversibleRestoreCall struct {
	Tool        string `json:"tool"`
	WhileSwitch string `json:"while_switch_enabled"`
	Arguments   struct {
		Action string          `json:"action"`
		Key    string          `json:"key"`
		Value  json.RawMessage `json:"value"`
	} `json:"arguments"`
}

func requireReversibleRestore(t *testing.T, rec db.InteractionRecord) reversibleRestoreCall {
	t.Helper()
	var audit struct {
		Restore json.RawMessage `json:"restore"`
	}
	if err := json.Unmarshal(rec.Interaction.ToolInput, &audit); err != nil {
		t.Fatal(err)
	}
	var restore reversibleRestoreCall
	if err := json.Unmarshal(audit.Restore, &restore); err != nil {
		t.Fatal(err)
	}
	if restore.Tool != "persona_settings" || restore.WhileSwitch != autoApproveReversibleSettingsKey || restore.Arguments.Action != "set" {
		t.Fatalf("restore call = %+v", restore)
	}
	return restore
}

func requireReversibleOutcome(t *testing.T, rec db.InteractionRecord, want string) {
	t.Helper()
	var audit struct {
		Outcome string `json:"outcome"`
	}
	if err := json.Unmarshal(rec.Interaction.ToolInput, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.Outcome != want {
		t.Fatalf("history outcome = %q, want %q; record = %+v", audit.Outcome, want, rec)
	}
}

func reversibleRecords(st *fleetMem) []db.InteractionRecord {
	st.fmu.Lock()
	defer st.fmu.Unlock()
	return append([]db.InteractionRecord(nil), st.recs...)
}

func TestReversibleSettingCandidatesRequireApprovalByDefault(t *testing.T) {
	for _, tc := range reversibleSettingCases {
		t.Run(tc.key, func(t *testing.T) {
			if tc.key == "assistant.briefing_waits" {
				needsBriefingModule(t)
			}
			s, st := newReversibleSettingService(t, false)
			requireReversibleApproval(t, s, tc.key, tc.value, false)
			requireReversibleRaw(t, st, tc, `null`)
			q := requireReversibleApproval(t, s, tc.key, tc.value, true)
			requireReversibleRaw(t, st, tc, tc.value)
			if !strings.Contains(q.Summary, "16") && tc.key == "assistant.briefing_waits" {
				t.Fatalf("briefing proposal does not show the new value: %q", q.Summary)
			}
		})
	}
}

func TestReversibleBooleanNullMatchesLegacyWhenSwitchOff(t *testing.T) {
	for _, tc := range []struct {
		name    string
		setting reversibleSettingCase
		initial Settings
		raw     string
		card    bool
	}{
		{name: "main default becomes false", setting: reversibleSettingCases[0], raw: `null`, card: true},
		{name: "main explicit false is unchanged", setting: reversibleSettingCases[0], initial: Settings{MainBackgroundFolded: ptr(false)}, raw: `false`},
		{name: "recent default is unchanged", setting: reversibleSettingCases[1], raw: `null`},
		{name: "recent explicit true becomes false", setting: reversibleSettingCases[1], initial: Settings{RecentTaskChats: ptr(true)}, raw: `true`, card: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, st := newReversibleSettingService(t, false)
			if err := st.PutSetting(context.Background(), settingsKey, tc.initial); err != nil {
				t.Fatal(err)
			}
			if tc.card {
				requireReversibleApproval(t, s, tc.setting.key, `null`, false)
			} else {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				r := callReversibleSetting(ctx, s, tc.setting.key, `null`)
				if r.IsError || !strings.Contains(reversibleResultText(r), "unchanged") {
					t.Fatalf("switch-off legacy null result = %+v %q", r, reversibleResultText(r))
				}
				if pending := s.pendingQuestions(); len(pending) != 0 {
					t.Fatalf("switch-off legacy null left a card: %+v", pending)
				}
			}
			requireReversibleRaw(t, st, tc.setting, tc.raw)
		})
	}
}

func TestBriefingWaitsExplicitDefaultNullNoopWhenSwitchOff(t *testing.T) {
	needsBriefingModule(t)
	s, st := newReversibleSettingService(t, false)
	initial := Settings{BriefingWaits: moduleValue(json.RawMessage(reversibleBriefingDefaultValue))}
	if err := st.PutSetting(context.Background(), settingsKey, initial); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	r := callReversibleSetting(ctx, s, "assistant.briefing_waits", `null`)
	if r.IsError || !strings.Contains(reversibleResultText(r), "unchanged") {
		t.Fatalf("switch-off explicit-default null result = %+v %q", r, reversibleResultText(r))
	}
	requireReversibleRaw(t, st, reversibleSettingCases[2], `{"stalled_minutes":20,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":15}`)
	if pending := s.pendingQuestions(); len(pending) != 0 {
		t.Fatalf("switch-off effective-value no-op left a card: %+v", pending)
	}
}

func TestReversibleSettingsAutoApproveRestoreAndAudit(t *testing.T) {
	for _, tc := range reversibleSettingCases {
		t.Run(tc.key, func(t *testing.T) {
			if tc.key == "assistant.briefing_waits" {
				needsBriefingModule(t)
			}
			s, st := newReversibleSettingService(t, true)
			requireAutomaticReversibleSet(t, s, tc.key, tc.value)
			requireReversibleRaw(t, st, tc, tc.value)
			recs := reversibleRecords(st)
			if len(recs) != 1 {
				t.Fatalf("history records = %d, want one", len(recs))
			}
			rec := recs[0]
			if rec.Status != "resolved" || rec.ResolvedAt == nil || rec.Resolution == nil || rec.Resolution.Allow == nil || !*rec.Resolution.Allow ||
				rec.Resolution.AnsweredBy != proto.AnsweredByAssistant {
				t.Fatalf("resolved assistant decision: %+v", rec)
			}
			if got := st.endedAs(rec.Interaction.ID); got != db.EndedByAssistant {
				t.Fatalf("reversible-setting history ended_by = %q, want %q", got, db.EndedByAssistant)
			}
			var audit struct {
				Actor       string          `json:"actor"`
				Operation   string          `json:"operation"`
				Setting     string          `json:"setting"`
				Before      json.RawMessage `json:"before"`
				After       json.RawMessage `json:"after"`
				Proposal    string          `json:"proposal"`
				Outcome     string          `json:"outcome"`
				CardSkipped bool            `json:"card_skipped"`
				Switch      string          `json:"switch"`
				Restore     json.RawMessage `json:"restore"`
			}
			if err := json.Unmarshal(rec.Interaction.ToolInput, &audit); err != nil {
				t.Fatal(err)
			}
			if audit.Actor != proto.AnsweredByAssistant || audit.Operation != "persona_settings.set" || audit.Setting != tc.key ||
				audit.Switch != autoApproveReversibleSettingsKey || audit.Proposal == "" || audit.Outcome != "saved" || !audit.CardSkipped ||
				!reversibleJSONEqual(audit.Before, json.RawMessage(`null`)) || !reversibleJSONEqual(audit.After, json.RawMessage(tc.value)) {
				t.Fatalf("audit = %+v", audit)
			}
			var restore struct {
				Tool        string `json:"tool"`
				WhileSwitch string `json:"while_switch_enabled"`
				Arguments   struct {
					Action string          `json:"action"`
					Key    string          `json:"key"`
					Value  json.RawMessage `json:"value"`
				} `json:"arguments"`
			}
			if err := json.Unmarshal(audit.Restore, &restore); err != nil {
				t.Fatal(err)
			}
			if restore.Tool != "persona_settings" || restore.WhileSwitch != autoApproveReversibleSettingsKey || restore.Arguments.Action != "set" || restore.Arguments.Key != tc.key ||
				!reversibleJSONEqual(restore.Arguments.Value, json.RawMessage(`null`)) {
				t.Fatalf("restore = %+v", restore)
			}
			if rec.Interaction.Summary != audit.Proposal {
				t.Fatalf("history summary = %q, localized proposal = %q", rec.Interaction.Summary, audit.Proposal)
			}

			r := httptest.NewRequest(http.MethodGet, "/api/v1/interactions/history?kind=permission&machine_id=server&session_id=conv1", nil)
			w := httptest.NewRecorder()
			s.serveInteractionHistory(w, r)
			var answer historyAnswer
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &answer) != nil || len(answer.Interactions) != 1 ||
				answer.Interactions[0].Interaction.ID != rec.Interaction.ID || answer.Interactions[0].Status != "resolved" {
				t.Fatalf("history readback: status %d body %s", w.Code, w.Body.String())
			}
			var readbackAudit struct {
				Outcome string `json:"outcome"`
			}
			if err := json.Unmarshal(answer.Interactions[0].Interaction.ToolInput, &readbackAudit); err != nil || readbackAudit.Outcome != "saved" {
				t.Fatalf("history outcome readback = %+v, %v", readbackAudit, err)
			}

			requireAutomaticReversibleSet(t, s, tc.key, `null`)
			requireReversibleRaw(t, st, tc, `null`)
			if got := len(reversibleRecords(st)); got != 2 {
				t.Fatalf("history records after restore = %d, want two", got)
			}
		})
	}
}

func TestReversibleSettingsRestoreExplicitBeforeImages(t *testing.T) {
	customRaw := `{"stalled_minutes":21,"silent_minutes":121,"orphan_minutes":11,"card_minutes":4,"max_rows":14}`
	for _, tc := range []struct {
		reversibleSettingCase
		initial Settings
		undo    string
	}{
		{reversibleSettingCase: reversibleSettingCases[0], initial: Settings{MainBackgroundFolded: ptr(true)}, undo: `true`},
		{reversibleSettingCase: reversibleSettingCases[1], initial: Settings{RecentTaskChats: ptr(false)}, undo: `false`},
		{reversibleSettingCase: reversibleSettingCases[2], initial: Settings{BriefingWaits: moduleValue(json.RawMessage(customRaw))}, undo: customRaw},
	} {
		t.Run(tc.key, func(t *testing.T) {
			if tc.key == "assistant.briefing_waits" {
				needsBriefingModule(t)
			}
			s, st := newReversibleSettingService(t, true)
			if err := st.PutSetting(context.Background(), settingsKey, tc.initial); err != nil {
				t.Fatal(err)
			}
			requireAutomaticReversibleSet(t, s, tc.key, tc.value)
			requireReversibleRaw(t, st, tc.reversibleSettingCase, tc.value)
			requireAutomaticReversibleSet(t, s, tc.key, tc.undo)
			requireReversibleRaw(t, st, tc.reversibleSettingCase, tc.undo)
		})
	}
}

func TestReversibleNullClearsExplicitDefaults(t *testing.T) {
	for _, tc := range []struct {
		reversibleSettingCase
		initial  Settings
		explicit string
	}{
		{reversibleSettingCase: reversibleSettingCases[0], initial: Settings{MainBackgroundFolded: ptr(true)}, explicit: `true`},
		{reversibleSettingCase: reversibleSettingCases[1], initial: Settings{RecentTaskChats: ptr(false)}, explicit: `false`},
		{reversibleSettingCase: reversibleSettingCases[2], initial: Settings{BriefingWaits: moduleValue(json.RawMessage(reversibleBriefingDefaultValue))}, explicit: `{"stalled_minutes":20,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":15}`},
	} {
		t.Run(tc.key, func(t *testing.T) {
			if tc.key == "assistant.briefing_waits" {
				needsBriefingModule(t)
			}
			s, st := newReversibleSettingService(t, true)
			if err := st.PutSetting(context.Background(), settingsKey, tc.initial); err != nil {
				t.Fatal(err)
			}
			requireAutomaticReversibleSet(t, s, tc.key, `null`)
			requireReversibleRaw(t, st, tc.reversibleSettingCase, `null`)
			recs := reversibleRecords(st)
			if len(recs) != 1 {
				t.Fatalf("history records = %d, want one", len(recs))
			}
			var audit struct {
				Before json.RawMessage `json:"before"`
				After  json.RawMessage `json:"after"`
			}
			if err := json.Unmarshal(recs[0].Interaction.ToolInput, &audit); err != nil {
				t.Fatal(err)
			}
			if !reversibleJSONEqual(audit.Before, json.RawMessage(tc.explicit)) || !reversibleJSONEqual(audit.After, json.RawMessage(`null`)) {
				t.Fatalf("explicit-default to unset audit = %+v", audit)
			}
		})
	}
}

func TestReversibleSettingsAuditRestoreRoundTripsRawState(t *testing.T) {
	defaults := []string{
		`true`,
		`false`,
		`{"stalled_minutes":20,"silent_minutes":120,"orphan_minutes":10,"card_minutes":3,"max_rows":15}`,
	}
	for i, setting := range reversibleSettingCases {
		for _, tc := range []struct {
			name   string
			before string
			after  string
		}{
			{name: "explicit default to unset", before: defaults[i], after: `null`},
			{name: "unset to explicit default", before: `null`, after: defaults[i]},
			{name: "unset to nondefault", before: `null`, after: setting.value},
			{name: "nondefault to unset", before: setting.value, after: `null`},
			{name: "explicit default to nondefault", before: defaults[i], after: setting.value},
			{name: "nondefault to explicit default", before: setting.value, after: defaults[i]},
		} {
			t.Run(setting.key+"/"+tc.name, func(t *testing.T) {
				if setting.key == "assistant.briefing_waits" {
					needsBriefingModule(t)
				}
				s, st := newReversibleSettingService(t, true)
				putReversibleRaw(t, st, setting, tc.before)
				requireAutomaticReversibleSet(t, s, setting.key, tc.after)
				requireReversibleRaw(t, st, setting, tc.after)
				recs := reversibleRecords(st)
				if len(recs) != 1 {
					t.Fatalf("history records = %d, want one", len(recs))
				}
				restore := requireReversibleRestore(t, recs[0])
				if restore.Arguments.Key != setting.key || !reversibleJSONEqual(restore.Arguments.Value, json.RawMessage(tc.before)) {
					t.Fatalf("restore = %+v, before = %s", restore, tc.before)
				}

				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				r := callReversibleSettingAction(ctx, s, restore.Arguments.Action, restore.Arguments.Key, string(restore.Arguments.Value), "conv1")
				if r.IsError {
					t.Fatalf("execute audited restore: %s", reversibleResultText(r))
				}
				requireReversibleRaw(t, st, setting, tc.before)
				if got := len(reversibleRecords(st)); got != 2 {
					t.Fatalf("history records after restore = %d, want two", got)
				}
			})
		}
	}
}

func TestAutomaticReversibleSettingRejectsConcurrentEdit(t *testing.T) {
	s, st := newReversibleSettingService(t, true)
	st.resolvedInsertGate = make(chan struct{})
	started := make(chan struct{})
	st.resolvedInsertStarted = started
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := make(chan proto.ToolResult, 1)
	go func() { result <- callReversibleSetting(ctx, s, "assistant.main_background_folded", `false`) }()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("automatic history write did not start")
	}
	if _, err := s.updateSettings(ctx, func(v *Settings) error { v.MainBackgroundFolded = ptr(true); return nil }); err != nil {
		t.Fatal(err)
	}
	close(st.resolvedInsertGate)
	select {
	case r := <-result:
		if !r.IsError || !strings.Contains(reversibleResultText(r), "stale") {
			t.Fatalf("concurrent result = %+v %q", r, reversibleResultText(r))
		}
	case <-ctx.Done():
		t.Fatal("automatic write did not finish")
	}
	requireReversibleRaw(t, st, reversibleSettingCases[0], `true`)
	recs := reversibleRecords(st)
	if len(recs) != 1 {
		t.Fatalf("history records = %d, want one", len(recs))
	}
	requireReversibleOutcome(t, recs[0], "not_saved")
	if pending := s.pendingQuestions(); len(pending) != 0 {
		t.Fatalf("stale automatic change left a card: %+v", pending)
	}
}

func TestAutomaticReversibleSettingFailsClosedWithoutHistory(t *testing.T) {
	s, st := newReversibleSettingService(t, true)
	st.resolvedInsertErr = errors.New("history unavailable")
	r := callReversibleSetting(context.Background(), s, "assistant.recent_task_chats", `true`)
	if !r.IsError || !strings.Contains(reversibleResultText(r), "history unavailable") {
		t.Fatalf("history failure result = %+v %q", r, reversibleResultText(r))
	}
	requireReversibleRaw(t, st, reversibleSettingCases[1], `null`)
	if got := len(reversibleRecords(st)); got != 0 {
		t.Fatalf("history records = %d, want none", got)
	}
}

func TestAutomaticReversibleSettingHistoryRecordsSaveFailure(t *testing.T) {
	s, st := newReversibleSettingService(t, true)
	st.setFailPut(errors.New("disk full"))
	r := callReversibleSetting(context.Background(), s, "assistant.recent_task_chats", `true`)
	if !r.IsError || !strings.Contains(reversibleResultText(r), "disk full") {
		t.Fatalf("settings failure result = %+v %q", r, reversibleResultText(r))
	}
	requireReversibleRaw(t, st, reversibleSettingCases[1], `null`)
	recs := reversibleRecords(st)
	if len(recs) != 1 {
		t.Fatalf("history after failed save = %+v", recs)
	}
	requireReversibleOutcome(t, recs[0], "not_saved")
}

func TestAutomaticReversibleSettingLeavesOutcomeUnknownWhenMarkingFails(t *testing.T) {
	s, st := newReversibleSettingService(t, true)
	st.resolvedOutcomeErr = errors.New("history outcome unavailable")
	requireAutomaticReversibleSet(t, s, "assistant.recent_task_chats", `true`)
	requireReversibleRaw(t, st, reversibleSettingCases[1], `true`)
	recs := reversibleRecords(st)
	if len(recs) != 1 {
		t.Fatalf("history after outcome-marking failure = %+v", recs)
	}
	requireReversibleOutcome(t, recs[0], "attempted")
}

func TestAutomaticReversibleSettingMarksJustMeHistoryPrivate(t *testing.T) {
	needsRecordingModule(t) // a path of the recording module's
	s, st := newReversibleSettingService(t, true)
	s.justMeSent("server", "conv1")
	s.justMeStarted("server", "conv1", true, false)
	if !s.justMeNow("server", "conv1") {
		t.Fatal("test turn is not marked Just me")
	}
	requireAutomaticReversibleSet(t, s, "assistant.recent_task_chats", `true`)
	recs := reversibleRecords(st)
	if len(recs) != 1 {
		t.Fatalf("history records = %d, want one", len(recs))
	}
	if !recs[0].Interaction.Private {
		t.Error("Just me automatic decision is not Private")
	}
	// A known non-health directory isolates the Private filter from the unknown-conversation fallback.
	recs[0].Interaction.Cwd = t.TempDir()
	if hidden := s.extensionHiddenCards(context.Background(), true, recs); len(hidden) != 1 || !hidden[0] {
		t.Errorf("extension visibility for Just me decision = %v, want hidden", hidden)
	}
}

func TestReversibleSettingAllowlistIsExactAndNotAgentWritable(t *testing.T) {
	s, _ := newReversibleSettingService(t, true)
	for _, o := range s.ownSettings() {
		if o.Key == autoApproveReversibleSettingsKey {
			t.Fatal("the administrator switch is exposed through persona_settings")
		}
		want := o.Key == "assistant.main_background_folded" || o.Key == "assistant.recent_task_chats" || o.Key == "assistant.briefing_waits"
		if got := reversibleSettingAutoApproved(o.Key); got != want {
			t.Errorf("reversibleSettingAutoApproved(%q) = %v, want %v", o.Key, got, want)
		}
	}
	for _, key := range []string{autoApproveReversibleSettingsKey, "direction.d1.prompt", "mail.accounts", "inbox.rules", "notifications.low_distraction", "extension.x"} {
		if reversibleSettingAutoApproved(key) {
			t.Errorf("forwarded or dynamic key %q is auto-approved", key)
		}
	}
}

func TestForwardedSettingKeepsHubApprovalWhenSwitchOn(t *testing.T) {
	f := newFakeHub().withMachine()
	f.hello.Assistant.AutoApproveReversibleSettings = true
	f.answerApp(AppSettings, AppTool, func(json.RawMessage) (any, error) {
		return proto.TextResult("hub approval card requested", false), nil
	})
	st := newFleetMem()
	s := newServiceOn(t, f, st)
	r := callReversibleSetting(context.Background(), s, "notifications.low_distraction", `false`)
	if r.IsError || !strings.Contains(reversibleResultText(r), "hub approval card requested") {
		t.Fatalf("forwarded setting result = %+v %q", r, reversibleResultText(r))
	}
	calls := f.appParams(AppSettings, AppTool)
	if len(calls) != 1 || calls[0]["name"] != compat.LegacyTool("persona_settings") {
		t.Fatalf("forwarded hub calls = %+v", calls)
	}
	args, ok := calls[0]["arguments"].(map[string]any)
	if !ok || args["action"] != "set" || args["key"] != "notifications.low_distraction" || args["value"] != false {
		t.Fatalf("forwarded hub arguments = %+v", calls[0]["arguments"])
	}
	if recs := reversibleRecords(st); len(recs) != 0 {
		t.Fatalf("forwarded hub setting produced assistant auto-approval history: %+v", recs)
	}
}

func TestSensitiveSettingsStillRequireApprovalWhenSwitchOn(t *testing.T) {
	for _, tc := range []struct {
		key   string
		value string
	}{
		{key: "assistant.notes", value: `"changed"`},
		{key: "assistant.main_tasks_quiet", value: `false`},
		{key: "assistant.start_requested_tasks", value: `false`},
		{key: "assistant.idle_auto_compact", value: `false`},
		{key: "assistant.idle_auto_compact_minutes", value: `31`},
		{key: "assistant.idle_auto_compact_tokens", value: `300001`},
		{key: "assistant.idle_release_minutes", value: `21`},
		{key: "assistant.readonly_mode", value: `"prompt"`},
		{key: "assistant.task_workspace_preference", value: `"prefer-new"`},
		{key: "assistant.recording_days", value: `13`},
		{key: "assistant.minutes_days", value: `365`},
		{key: "assistant.kb_topics", value: `false`},
		{key: "assistant.main_compact_window", value: `250000`},
		{key: "assistant.meeting_attendance_default", value: `"participant"`},
		{key: "assistant.meeting_transcription_default", value: `"hub"`},
		{key: "assistant.delivery_gate", value: `"live"`},
		{key: "assistant.done_needs_delivery", value: `true`},
		{key: "assistant.review_sample_percent", value: `99`},
		{key: "assistant.leaders", value: `[]`},
		{key: "assistant.background_supervisor_keeps_context", value: `true`},
		{key: "assistant.interject_background", value: `true`},
		{key: "assistant.claude_interactive", value: `true`},
		{key: "assistant.load_all_tools", value: `true`},
		{key: "assistant.agent_tasks", value: `true`},
		{key: "assistant.task_lessons", value: `true`},
		{key: "assistant.close_ended_terminals", value: `true`},
		{key: "assistant.inbox_signals", value: `true`},
		{key: "assistant.auto_supervision_messages", value: `false`},
		{key: "assistant.main_supervises_own_tasks", value: `false`},
		{key: "assistant.supervision_batch_minutes", value: `45`},
	} {
		t.Run(tc.key, func(t *testing.T) {
			s, _ := newReversibleSettingService(t, true)
			if tc.key == "assistant.leaders" {
				setSettings(t, s, func(v *Settings) { v.Leaders = []string{"previous"} })
			}
			requireReversibleApproval(t, s, tc.key, tc.value, false)
		})
	}
}

func TestSpecialSensitiveSettingsStillRequireApprovalWhenSwitchOn(t *testing.T) {
	t.Run("tasks.trust", func(t *testing.T) {
		s, f := trustFixture(t)
		f.hello.Assistant.AutoApproveReversibleSettings = true
		requireReversibleApprovalAction(t, s, "add", "tasks.trust", `{"machine":"box","directory":"/home/me/src/tools"}`, "conv1", false)
	})
	t.Run("model_config.kb", func(t *testing.T) {
		s, f, _ := modelService(t)
		f.hello.Assistant.AutoApproveReversibleSettings = true
		putModelGroupProviders(t, s, "https://provider.invalid")
		setModel(t, s, "kb", ModelBinding{Provider: "launcher.old-provider", Model: "old-model"})
		requireReversibleApproval(t, s, "model_config.kb", `"launcher.new-provider"`, false)
	})
	t.Run("model_profiles.tiers", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.hello.Assistant.AutoApproveReversibleSettings = true
		f.launcherList = []proto.Launcher{{Name: "claude"}, {Name: "fast", Model: "m-fast"}}
		s := newTestService(t, f)
		putTierProfiles(t, s, "economy", map[string]ModelTier{"standard": {Launcher: "claude"}}, nil)
		requireReversibleApproval(t, s, "model_profiles.tiers", `{"tier":"review","launcher":"claude"}`, false)
	})
	t.Run("assistant.admission", func(t *testing.T) {
		f := newFakeHub().withMachine()
		f.hello.Assistant.AutoApproveReversibleSettings = true
		s := newTestService(t, f)
		requireReversibleApproval(t, s, "assistant.admission", `{"direction_cap_by_direction":{"dir-9":2}}`, false)
	})
	t.Run("agents.hidden_dirs", func(t *testing.T) {
		ft := newFleetTest(t)
		ft.f.hello.Assistant.AutoApproveReversibleSettings = true
		useAgentDirs(ft.s)
		requireReversibleApprovalAction(t, ft.s, "add", "agents.hidden_dirs", `{"machine":"mac","pattern":"/work/"}`, "conv1", false)
	})
	t.Run("direction.prompt", func(t *testing.T) {
		s, f := notesEnv(t)
		f.hello.Assistant.AutoApproveReversibleSettings = true
		setSettings(t, s, func(v *Settings) { v.Main = "main_chat" })
		if _, _, err := s.setDirectionNotes(context.Background(), "conv_lead", "# Rules\n\nVerify before merge.", nil); err != nil {
			t.Fatal(err)
		}
		requireReversibleApprovalAction(t, s, "set", "direction.conv_lead.prompt", `{"old":"Verify before merge.","new":"Run server tests before merge."}`, "main_chat", false)
	})
	t.Run("urgent_call.rules", func(t *testing.T) {
		s, _ := newReversibleSettingService(t, true)
		requireReversibleApprovalAction(t, s, "add", "urgent_call.rules", `{"text":"Custom urgent rule"}`, "conv1", false)
	})
	t.Run("assistant.launcher", func(t *testing.T) {
		s, f, _ := modelService(t)
		f.hello.Assistant.AutoApproveReversibleSettings = true
		f.launcherList = []proto.Launcher{{Name: "claude"}, {Name: "fast", Model: "fast-default"}}
		setModel(t, s, "assistant", ModelBinding{Provider: "launcher.claude", Model: "old-model"})
		requireReversibleApproval(t, s, "assistant.launcher", `"fast"`, false)
	})
}
