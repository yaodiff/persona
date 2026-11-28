package assistant

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

// All-day recordings through the manager's HTTP, their recap's push, to-dos, kb_get and the evening routine's facts
// (kb_day.go, briefing_day.go; #4741). All names and texts are placeholders.

// The upload of a day recording says its base and the hub's count; appends go by position, a retry stores nothing
// twice, and the refusals come with the contract's codes and bodies.
func TestDaySegmentsHTTP(t *testing.T) {
	d := newDayFixture(t)
	wantCode(t, d.do("POST", segmentsPath(dayRec), "", dayLines(100, 101), http.StatusNotFound), codeRecordingNotFound)

	put := d.putDay(dayRec, 100, d.owner, nil)
	if put["status"] != "stored" || put["revision"] != float64(100) || put["base"] != float64(100) || put["next"] != float64(0) ||
		put["attendance"] != "off" {
		t.Fatalf("PUT: %v", put)
	}
	out := d.do("POST", segmentsPath(dayRec), "", dayLines(100, 101), http.StatusOK)
	if len(out) != 4 || out["id"] != dayRec || out["revision"] != float64(101) || out["next"] != float64(3) || out["status"] != "stored" {
		t.Fatalf("append: %v", out)
	}
	if out := d.do("POST", segmentsPath(dayRec), "", dayLines(100, 102), http.StatusOK); out["next"] != float64(3) || out["revision"] != float64(102) {
		t.Fatalf("retry: %v", out)
	}
	if segs := d.engine().Store.RecordingSegments(dayRec); len(segs) != 3 || segs[2].N != 3 {
		t.Fatalf("stored %+v", segs)
	}

	// The other fields apply only from a request at least as new as the stored revision: an older one (a slow
	// retry) stores its new positions and changes nothing else.
	title := func(rev int64, from int, title, text string) map[string]any {
		return map[string]any{"base": 100, "revision": rev, "from": from, "title": title,
			"segments": []map[string]any{{"start": 3850, "end": 3855, "text": text}}}
	}
	if out := d.do("POST", segmentsPath(dayRec), "", title(300, 3, "Tuesday", "A fourth sentence."), http.StatusOK); out["next"] != float64(4) {
		t.Fatalf("newer: %v", out)
	}
	if out := d.do("POST", segmentsPath(dayRec), "", title(200, 4, "Older", "A fifth sentence."), http.StatusOK); out["next"] != float64(5) ||
		out["revision"] != float64(300) {
		t.Fatalf("older: %v", out)
	}
	if r, _ := d.engine().Store.Recording(dayRec); r.Title != "Tuesday" || r.Revision != 300 || r.Next != 5 {
		t.Fatalf("after an older request: title %q, revision %d, next %d", r.Title, r.Revision, r.Next)
	}

	gap := d.do("POST", segmentsPath(dayRec), "", map[string]any{"base": 100, "revision": 301, "from": 9, "segments": []any{}}, http.StatusConflict)
	if wantCode(t, gap, codeSegmentsGap); gap["next"] != float64(5) || gap["error"] == "" || len(gap) != 3 {
		t.Fatalf("gap: %v", gap)
	}
	changed := d.do("POST", segmentsPath(dayRec), "", map[string]any{"base": 99, "revision": 301, "from": 5, "segments": []any{}}, http.StatusConflict)
	if wantCode(t, changed, codeBaseChanged); changed["base"] != float64(100) || changed["next"] != float64(5) || changed["error"] == "" {
		t.Fatalf("base changed: %v", changed)
	}
	many := make([]map[string]any, recordingwire.MaxAppendSegments+1)
	for i := range many {
		many[i] = map[string]any{"start": 4000 + i, "end": 4000 + i, "text": "x"}
	}
	for name, body := range map[string]any{
		"no base":          `{"revision":5,"from":5,"segments":[]}`,
		"revision at base": `{"base":100,"revision":100,"from":5,"segments":[]}`,
		"no from":          `{"base":100,"revision":301,"segments":[]}`,
		"negative from":    `{"base":100,"revision":301,"from":-1,"segments":[]}`,
		"fractional from":  `{"base":100,"revision":301,"from":1.5,"segments":[]}`,
		"a text time":      `{"base":100,"revision":301,"from":5,"segments":[{"start":"1","end":2,"text":"x"}]}`,
		"not json":         `{"base":`,
		"too many":         map[string]any{"base": 100, "revision": 301, "from": 5, "segments": many},
	} {
		if out := d.do("POST", segmentsPath(dayRec), "", body, http.StatusBadRequest); out["code"] != "bad_request" {
			t.Errorf("%s: %v", name, out)
		}
	}
	if out := d.do("POST", segmentsPath(dayRec), "", `{"base":100,"revision":301,"from":5.0,"segments":[]}`, http.StatusOK); out["next"] != float64(5) {
		t.Fatalf("from 5.0: %v", out)
	}
	wantCode(t, d.do("POST", segmentsPath(dayRec), "", strings.Repeat(" ", maxSegmentsBody+1), http.StatusRequestEntityTooLarge), "too_large")
	wantCode(t, d.do("POST", segmentsPath(dayRec), "answer", dayLines(100, 400), http.StatusForbidden), "forbidden")
	wantCode(t, d.do("POST", segmentsPath(dayRec), "extension", dayLines(100, 400), http.StatusForbidden), "forbidden")

	// Another kind: 409 not_day for an append, kind_changed for a PUT either way.
	d.upload(meetingRec, 1, nil)
	wantCode(t, d.do("POST", segmentsPath(meetingRec), "", dayLines(1, 2), http.StatusConflict), codeNotDay)
	_, day := today8(d.owner)
	wantCode(t, d.do("PUT", "/api/v1/recordings/"+meetingRec, "", map[string]any{"revision": 5, "kind": "day", "day": day,
		"started_at": time.Now().UTC().Format(time.RFC3339), "device": "watch", "segments": []any{}}, http.StatusConflict), codeKindChanged)
	start, _ := today8(d.owner)
	wantCode(t, d.do("PUT", "/api/v1/recordings/"+dayRec, "", map[string]any{"revision": 500, "kind": "meeting",
		"started_at": start.Format(time.RFC3339), "device": "watch", "segments": []any{}}, http.StatusConflict), codeKindChanged)

	// A PUT is a new base: an append on the old one is refused with the new base, the new one's goes on from its count.
	put = d.putDay(dayRec, 600, d.owner, map[string]any{"segments": []map[string]any{{"start": 60, "end": 65, "text": "Morning standup about the export."},
		{"start": 70, "end": 75, "text": ""}}})
	if put["revision"] != float64(600) || put["base"] != float64(600) || put["next"] != float64(2) {
		t.Fatalf("repair PUT: %v", put)
	}
	changed = d.do("POST", segmentsPath(dayRec), "", map[string]any{"base": 100, "revision": 601, "from": 5, "segments": []any{}}, http.StatusConflict)
	if changed["base"] != float64(600) || changed["next"] != float64(2) {
		t.Fatalf("old base: %v", changed)
	}
	if out := d.do("POST", segmentsPath(dayRec), "", map[string]any{"base": 600, "revision": 601, "from": 2,
		"segments": []map[string]any{{"start": 80, "end": 85, "text": "Back on the new base."}}}, http.StatusOK); out["next"] != float64(3) {
		t.Fatalf("new base: %v", out)
	}
	// A stale PUT of a day recording answers the stored revision, so the phone sends again above it.
	if stale := d.putDay(dayRec, 550, d.owner, nil); stale["status"] != "stale" || stale["revision"] != float64(601) || stale["base"] != float64(600) ||
		stale["next"] != float64(3) {
		t.Fatalf("stale PUT: %v", stale)
	}
	recs := d.do("GET", "/api/v1/recordings", "", nil, http.StatusOK)["recordings"].([]any)
	for _, x := range recs {
		if r := x.(map[string]any); r["id"] == dayRec && (r["kind"] != "day" || r["part"] != float64(1) || r["next"] != float64(3) || r["base"] != float64(600)) {
			t.Fatalf("reconciliation: %v", r)
		}
	}

	// Expired: the day started before recording_text_days now (nothing written), as for a PUT.
	old := time.Now().Add(-50 * time.Hour)
	d.putDay(dayPart2, 10, d.owner, map[string]any{"started_at": old.Format(time.RFC3339), "day": old.In(d.owner).Format(time.DateOnly)})
	setSettings(t, d.s, func(v *Settings) { one := 1; v.RecordingTextDays = &one })
	if out := d.do("POST", segmentsPath(dayPart2), "", dayLines(10, 11), http.StatusOK); out["status"] != "expired" {
		t.Fatalf("expired: %v", out)
	}

	// Deleted: 410, before a body's 400.
	d.do("DELETE", "/api/v1/recordings/"+dayRec, "", nil, http.StatusOK)
	wantCode(t, d.do("POST", segmentsPath(dayRec), "", `{"base":`, http.StatusGone), codeRecordingDeleted)
}

// The refusals' order: 503 before anything, 413 too_large before the recording is looked at, 404 before 403
// kb_read_only, which comes before 400.
func TestDaySegmentsRefusalOrder(t *testing.T) {
	d := newDayFixture(t)
	d.putDay(dayRec, 100, d.owner, nil)
	d.f.mu.Lock()
	d.f.hello.KB = &KBSettings{ReadOnly: true}
	d.f.mu.Unlock()
	wantCode(t, d.do("POST", segmentsPath(dayRec), "", strings.Repeat(" ", maxSegmentsBody+1), http.StatusRequestEntityTooLarge), "too_large")
	wantCode(t, d.do("POST", segmentsPath(dayPart2), "", `{"base":`, http.StatusNotFound), codeRecordingNotFound)
	wantCode(t, d.do("POST", segmentsPath(dayRec), "", `{"base":`, http.StatusForbidden), "kb_read_only")
	d.f.mu.Lock()
	d.f.hello.KB = &KBSettings{Off: true}
	d.f.mu.Unlock()
	wantCode(t, d.do("POST", segmentsPath(dayRec), "", strings.Repeat(" ", maxSegmentsBody+1), http.StatusServiceUnavailable), "kb_disabled")
}

// Past its limits an append stores nothing and says 413 day_full with the count; a PUT over them is 413 day_full too.
func TestDaySegmentsDayFull(t *testing.T) {
	d := newDayFixture(t)
	empty := make([]map[string]any, 20000)
	for i := range empty {
		empty[i] = map[string]any{"start": i, "end": i, "text": ""}
	}
	if put := d.putDay(dayRec, 100, d.owner, map[string]any{"segments": empty}); put["next"] != float64(20000) {
		t.Fatalf("PUT: %v", put)
	}
	full := d.do("POST", segmentsPath(dayRec), "", map[string]any{"base": 100, "revision": 101, "from": 20000,
		"segments": []map[string]any{{"start": 1, "end": 2, "text": "One more."}}}, http.StatusRequestEntityTooLarge)
	if wantCode(t, full, codeDayFull); full["next"] != float64(20000) || full["error"] == "" {
		t.Fatalf("append: %v", full)
	}
	if r, _ := d.engine().Store.Recording(dayRec); r.Next != 20000 || r.Revision != 100 {
		t.Fatalf("stored something: next %d, revision %d", r.Next, r.Revision)
	}
	over := d.do("PUT", "/api/v1/recordings/"+dayRec, "", map[string]any{"revision": 200, "kind": "day", "day": time.Now().In(d.owner).Format(time.DateOnly),
		"started_at": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "device": "watch", "segments": append(empty, empty[0])},
		http.StatusRequestEntityTooLarge)
	if wantCode(t, over, codeDayFull); over["next"] != float64(20000) {
		t.Fatalf("PUT over: %v", over)
	}
}

// The minutes' routes serve a day recording's recap: asking writes it from what the hub has (no push before the
// recap time), the detail and kb_get carry it, its suggested to-dos are confirmed once with notes naming the day and
// the clock time in the recording's zone; the day's push comes once, at the recap time, whatever the parts and
// versions after it.
func TestDayRecapAskTodosAndPush(t *testing.T) {
	d := newDayFixture(t)
	tz := etcZone(t, d.offset+1) // the recording's own zone, an hour east of the owner's
	path := "/api/v1/recordings/" + dayRec
	d.putDay(dayRec, 100, tz, nil)
	d.do("POST", path+"/minutes", "", nil, http.StatusConflict) // no text yet
	d.do("POST", segmentsPath(dayRec), "", dayLines(100, 101), http.StatusOK)
	if got := d.do("POST", path+"/minutes", "", nil, http.StatusAccepted); got["state"] != "pending" {
		t.Fatalf("ask: %v", got)
	}
	rc := d.recap(dayRec)
	if rc.Version != 1 || len(rc.Timeline) != 2 || rc.Timeline[1].Kind != kb.RecapMeeting || len(rc.Actions) != 1 || rc.Actions[0].ID != "a1" {
		t.Fatalf("recap %+v", rc)
	}
	if p := d.f.pushed(); len(p) != 0 {
		t.Fatalf("an asked recap pushed before the recap time: %+v", p)
	}
	r, _ := d.engine().Store.Recording(dayRec)
	if r.MinutesState != "" || r.Meeting != nil || r.MinutesAsked {
		t.Fatalf("became a meeting: %+v", r)
	}

	var detail struct {
		Detail struct {
			Meta    map[string]any `json:"meta"`
			Summary string         `json:"summary"`
			Recap   kb.DayRecap    `json:"recap"`
		} `json:"detail"`
	}
	raw, _ := json.Marshal(d.do("GET", "/api/v1/kb/recordings/"+dayRec, "", nil, http.StatusOK))
	_ = json.Unmarshal(raw, &detail)
	if detail.Detail.Meta["kind"] != "day" || detail.Detail.Meta["recap_state"] != "ready" || detail.Detail.Recap.Version != 1 ||
		!strings.Contains(detail.Detail.Summary, "export review") {
		t.Fatalf("detail %+v", detail.Detail)
	}

	// Confirming: a todo_day_ to-do once, however often; its notes name the day and when it was said there.
	todos := path + "/minutes/todos"
	var out struct {
		Todos []Todo      `json:"todos"`
		Recap kb.DayRecap `json:"recap"`
	}
	conf := func(version int, actions ...string) {
		t.Helper()
		raw, _ := json.Marshal(d.do("POST", todos, "", map[string]any{"version": version, "actions": actions}, http.StatusOK))
		out.Todos, out.Recap = nil, kb.DayRecap{}
		_ = json.Unmarshal(raw, &out)
	}
	conf(1, "a1")
	_, day := today8(tz)
	clock := r.StartedAt.Add(3800 * time.Second).In(tz).Format("15:04")
	if len(out.Todos) != 1 || !strings.HasPrefix(out.Todos[0].ID, "todo_day_") || out.Todos[0].Title != "Bo: Fix the flaky upload test" ||
		!strings.Contains(out.Todos[0].Notes, day) || !strings.Contains(out.Todos[0].Notes, clock) || !strings.Contains(out.Todos[0].Notes, "tomorrow") ||
		out.Recap.Actions[0].TodoID != out.Todos[0].ID || out.Todos[0].ID != dayRecapTodoID(dayRec, 1, "a1") {
		t.Fatalf("confirmed %+v (want %s at %s)", out, day, clock)
	}
	first := out.Todos[0].ID
	if conf(1, "a1", "a1"); len(out.Todos) != 1 || out.Todos[0].ID != first {
		t.Fatalf("again %+v", out)
	}
	wantCode(t, d.do("POST", todos, "", map[string]any{"version": 7, "actions": []string{"a1"}}, http.StatusConflict), codeMinutesStale)
	d.do("POST", todos, "", map[string]any{"version": 1, "actions": []string{"a9"}}, http.StatusNotFound)
	d.do("POST", todos, "answer", map[string]any{"version": 1, "actions": []string{"a1"}}, http.StatusForbidden)

	// kb_get: the recap before the transcript, with where its suggested to-dos stand; kb_list marks it. The day was
	// recorded in English: so is its recap's Markdown.
	got, err := d.s.toolKB(context.Background(), "kb_get", json.RawMessage(`{"kind":"recordings","id":"`+dayRec+`"}`))
	if err != nil || !strings.Contains(got, "recap.md:\n# Day recap · "+day) || !strings.Contains(got, "## Suggested to-dos") || !strings.Contains(got, "Suggested to-dos as they stand") ||
		!strings.Contains(got, "a1 Bo: Fix the flaky upload test (by tomorrow) → to-do "+first+" open") ||
		strings.Index(got, "recap.md:") > strings.Index(got, "transcript.md:") {
		t.Fatalf("kb_get: %v\n%s", err, got)
	}
	if list, _ := d.s.toolKB(context.Background(), "kb_list", json.RawMessage(`{"kind":"recordings"}`)); !strings.Contains(list, " all-day recap-ready") {
		t.Fatalf("kb_list: %s", list)
	}

	// The recap time comes (the evening routine's moved before now, though off): the ready recap pushes, once.
	setEvening(t, d.s, "06:30", false)
	d.engine().RecordingProvider().WriteDayRecaps(context.Background(), 1)
	pushes := d.f.pushed()
	l := d.s.lang(context.Background())
	if len(pushes) != 1 || pushes[0].ID != "ntf_dayrecap_"+r.Day || pushes[0].Kind != NotifyPush || pushes[0].SessionID != "c-main" ||
		pushes[0].Title != i18n.T(l, "assistant-day-recap.ready_title") ||
		pushes[0].Body != i18n.T(l, "assistant-day-recap.ready_body", i18n.Args{"day": r.Day, "meetings": 0, "actions": 1}) { // its meeting matches none the hub knows (#5051)
		t.Fatalf("pushes %+v", pushes)
	}
	// A later version, and the day's second part, push nothing more.
	d.do("POST", segmentsPath(dayRec), "", map[string]any{"base": 100, "revision": 102, "from": 3,
		"segments": []map[string]any{{"start": 4000, "end": 4005, "text": "One more thing."}}}, http.StatusOK)
	d.do("POST", path+"/minutes", "", nil, http.StatusAccepted)
	if rc := d.recap(dayRec); rc.Version != 2 || rc.Actions[0].TodoID != first {
		t.Fatalf("second version %+v", rc)
	}
	d.putDay(dayPart2, 200, tz, map[string]any{"part": 2})
	d.do("POST", segmentsPath(dayPart2), "", dayLines(200, 201), http.StatusOK)
	d.recap(dayPart2)
	d.engine().RecordingProvider().WriteDayRecaps(context.Background(), 1)
	if p := d.f.pushed(); len(p) != 1 {
		t.Fatalf("pushed again: %+v", p)
	}
}

// Past recording_days a day recording keeps its recap (trimmed, as one with minutes): it can't be written again, the
// phone's uploads of it are expired, and its to-dos can still be confirmed; kb_get says only the recap is left.
func TestDayRecapAfterTheTrim(t *testing.T) {
	d := newDayFixture(t)
	setSettings(t, d.s, func(v *Settings) { days := 14; v.RecordingTextDays = &days }) // the text is kept forever by default
	path := "/api/v1/recordings/" + dayRec
	d.putDay(dayRec, 100, d.owner, nil)
	d.do("POST", segmentsPath(dayRec), "", dayLines(100, 101), http.StatusOK)
	d.do("POST", path+"/minutes", "", nil, http.StatusAccepted)
	d.recap(dayRec)
	d.engine().PruneRecordings(time.Now().AddDate(0, 0, 20))
	if r, _ := d.engine().Store.Recording(dayRec); !r.Trimmed() || r.RecapState != kb.MinutesReady {
		t.Fatalf("not trimmed with its recap: %+v", r)
	}
	wantCode(t, d.do("POST", path+"/minutes", "", nil, http.StatusConflict), codeTranscriptExpired)
	if out := d.do("POST", segmentsPath(dayRec), "", map[string]any{"base": 100, "revision": 102, "from": 3, "segments": []any{}}, http.StatusOK); out["status"] != "expired" {
		t.Fatalf("append: %v", out)
	}
	if put := d.putDay(dayRec, 200, d.owner, nil); put["status"] != "expired" || put["base"] != float64(100) || put["next"] != float64(3) {
		t.Fatalf("PUT: %v", put)
	}
	if out := d.do("POST", path+"/minutes/todos", "", map[string]any{"version": 1, "actions": []string{"a1"}}, http.StatusOK); len(out["todos"].([]any)) != 1 {
		t.Fatalf("to-dos: %v", out)
	}
	got, err := d.s.toolKB(context.Background(), "kb_get", json.RawMessage(`{"kind":"recordings","id":"`+dayRec+`"}`))
	if err != nil || !strings.Contains(got, "recap.md:") || !strings.Contains(got, "only the recap above is kept") || strings.Contains(got, "transcript.md:\n") {
		t.Fatalf("kb_get: %v\n%s", err, got)
	}
}

// The push from a recap written at the recap time: the day waited for its first recap, which pushes in its write.
func TestDayRecapPushAtTheRecapTime(t *testing.T) {
	d := newDayFixture(t)
	setEvening(t, d.s, "06:30", true)
	d.putDay(dayRec, 100, d.owner, nil)
	d.do("POST", segmentsPath(dayRec), "", dayLines(100, 101), http.StatusOK)
	rc := d.recap(dayRec)
	pushes := d.f.pushed()
	if len(pushes) != 1 || pushes[0].ID != "ntf_dayrecap_"+rc.Day || pushes[0].Title != i18n.T(d.s.lang(context.Background()), "assistant-day-recap.ready_title") {
		t.Fatalf("pushes %+v", pushes)
	}
	if r, _ := d.engine().Store.Recording(dayRec); !r.RecapPushed || r.RecapPush {
		t.Fatalf("push not marked told: %+v", r)
	}
}

// The evening routine's facts say today's all-day recording to the model only: its coverage, each part's recap or
// state, the timeline's titles and the to-dos not confirmed; never in the card, and not for another routine.
func TestEveningFactsSayTodaysDayRecording(t *testing.T) {
	needsBriefingModule(t)
	d := newDayFixture(t)
	ctx := context.Background()
	if f := d.s.briefFactsAt(ctx, time.Now(), "evening"); f.Day != nil {
		t.Fatalf("a day without a day recording: %+v", f.Day)
	}
	// One of an earlier day, closed then, is not today's.
	old := time.Now().Add(-40 * time.Hour)
	ended := old.Add(2 * time.Hour)
	d.putDay(meetingOther, 50, d.owner, map[string]any{"started_at": old.Format(time.RFC3339), "ended_at": ended.Format(time.RFC3339),
		"day": old.In(d.owner).Format(time.DateOnly), "complete": true, "segments": []map[string]any{{"start": 1, "end": 2, "text": "An old day."}},
		"coverage": map[string]any{"to": 7200}})
	if f := d.s.briefFactsAt(ctx, time.Now(), "evening"); f.Day != nil {
		t.Fatalf("an earlier day's recording: %+v", f.Day)
	}
	d.putDay(dayRec, 100, d.owner, nil)
	d.do("POST", segmentsPath(dayRec), "", dayLines(100, 101), http.StatusOK)
	d.do("POST", "/api/v1/recordings/"+dayRec+"/minutes", "", nil, http.StatusAccepted)
	rc := d.recap(dayRec)
	d.putDay(dayPart2, 200, d.owner, map[string]any{"part": 2, "duration": 1800})

	now := time.Now()
	f := d.s.briefFactsAt(ctx, now, "evening")
	if f.Day == nil || len(f.Day.Parts) != 2 || len(f.Day.Timeline) != 2 || len(f.Day.Actions) != 1 || len(f.Day.Gaps) != 1 {
		t.Fatalf("facts %+v", f.Day)
	}
	text := d.s.briefModelText(f)
	made := rc.GeneratedAt.In(d.owner).Format("15:04")
	// Open, it was recorded until now; its text goes as far as the phone uploaded it.
	recorded := "录音时段 08:00–" + now.In(d.owner).Format("15:04") + "，文字转写到 09:05，"
	for _, want := range []string{"的全天录音", recorded + "其间没录到的：被打断（来电、Siri 等占用了麦克风） 1 段共 15 分钟（08:10–08:25）",
		"第 1 部分：回顾写于 " + made + "，读到 ", "概要：A standup, then the export review.", "第 2 部分：录了 30 分钟，但没有转写出文字",
		"08:01–08:01 会议以外：Standup", "09:02–09:03 会议：Export review", "Bo：Fix the flaky upload test", "所以请在回答里"} {
		if !strings.Contains(text, want) {
			t.Errorf("facts lack %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "The export drops big entries") {
		t.Errorf("facts carry the points:\n%s", text)
	}
	card, fallback, _ := d.s.briefCard(f, "晚报", "ok")
	if strings.Contains(card.JSON(), "Export review") || strings.Contains(fallback, "Export review") || strings.Contains(briefRunnerForTest(t).CountsIn(f, f.Lang), "Export review") {
		t.Fatalf("the card or the push carries the day: %s\n%s", card.JSON(), fallback)
	}
	if f := d.s.briefFactsAt(ctx, time.Now(), "morning"); f.Day != nil || strings.Contains(d.s.briefModelText(f), "全天录音") {
		t.Fatalf("the morning routine: %+v", f.Day)
	}
	// Confirmed, a to-do is not a suggestion any more.
	d.do("POST", "/api/v1/recordings/"+dayRec+"/minutes/todos", "", map[string]any{"version": 1, "actions": []string{"a1"}}, http.StatusOK)
	if f := d.s.briefFactsAt(ctx, time.Now(), "evening"); len(f.Day.Actions) != 0 || strings.Contains(d.s.briefModelText(f), "还没确认的建议待办") {
		t.Fatalf("a confirmed to-do: %+v", f.Day.Actions)
	}
}

// The push counts the day's meetings and suggested to-dos across its parts' recaps; a meeting only when the hub knows
// of it, which neither part's is (#5051, TestDayRecapCountsOnlyKnownMeetings).
func TestDayRecapPushCountsEveryPart(t *testing.T) {
	d := newDayFixture(t)
	for i, id := range []string{dayRec, dayPart2} {
		base := int64(100 * (i + 1))
		d.putDay(id, base, d.owner, map[string]any{"part": i + 1})
		d.do("POST", segmentsPath(id), "", dayLines(base, base+1), http.StatusOK)
		d.do("POST", "/api/v1/recordings/"+id+"/minutes", "", nil, http.StatusAccepted)
		if rc := d.recap(id); len(rc.Actions) != 1 || rc.Timeline[1].Kind != kb.RecapMeeting {
			t.Fatalf("recap of %s: %+v", id, rc)
		}
	}
	if p := d.f.pushed(); len(p) != 0 {
		t.Fatalf("pushed before the recap time: %+v", p)
	}
	setEvening(t, d.s, "06:30", false)
	d.engine().RecordingProvider().WriteDayRecaps(context.Background(), 1)
	_, day := today8(d.owner)
	want := i18n.T(d.s.lang(context.Background()), "assistant-day-recap.ready_body", i18n.Args{"day": day, "meetings": 0, "actions": 2})
	if p := d.f.pushed(); len(p) != 1 || p[0].Body != want {
		t.Fatalf("pushes %+v, want %q", p, want)
	}
}

// The evening facts of a read-only knowledge base take the owner's today in the owner's zone (its store is not put
// in it); what was recorded is the recording's span, and how far its text goes is said apart.
func TestEveningFactsDayReadOnlyInTheOwnersZone(t *testing.T) {
	d := newDayFixture(t)
	// An owner about 12 hours from the host's zone, so their days part.
	_, local := time.Now().Zone()
	hours := int(math.Round(float64(local) / 3600))
	owner := hours + 12
	if hours > 0 {
		owner = hours - 12
	}
	loc := etcZone(t, owner)
	setSettings(t, d.s, func(v *Settings) { v.Timezone = loc.String() })
	y := time.Now().In(loc).AddDate(0, 0, -1)
	start := time.Date(y.Year(), y.Month(), y.Day(), 8, 0, 0, 0, loc)
	end, now := start.Add(time.Hour), start.Add(5*time.Hour)
	d.putDay(dayRec, 100, loc, map[string]any{"started_at": start.Format(time.RFC3339), "ended_at": end.Format(time.RFC3339),
		"day": y.Format(time.DateOnly), "complete": true, "segments": []map[string]any{{"start": 60, "end": 65, "text": "Morning standup."}},
		"coverage": map[string]any{"to": 1800}})
	d.f.mu.Lock()
	d.f.hello.KB = &KBSettings{ReadOnly: true}
	d.f.mu.Unlock()
	if d.s.briefingOn() {
		f := d.s.briefFactsAt(context.Background(), now, "evening")
		if f.Day == nil || f.Day.Date != y.Format(time.DateOnly) {
			t.Fatalf("the owner's day in a read-only knowledge base: %+v", f.Day)
		}
		if text := d.s.briefModelText(f); !strings.Contains(text, "录音时段 08:00–09:00，文字转写到 08:30") {
			t.Fatalf("recorded and transcribed:\n%s", text)
		}
	}
}
