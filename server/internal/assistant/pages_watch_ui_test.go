package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

type watchFixture struct {
	Now    string             `json:"now"`
	TZ     string             `json:"tz"`
	Draws  []watchDrawFixture `json:"draws"`
	Pushes []watchPushFixture `json:"pushes"`
}

type watchDrawFixture struct {
	Name      string                `json:"name"`
	Watch     json.RawMessage       `json:"watch"`
	Readings  []watchReadingFixture `json:"readings"`
	Tripped   bool                  `json:"tripped"`
	Section   map[string]any        `json:"section"`
	GlanceRow map[string]any        `json:"glance_row"`
}

type watchReadingFixture struct {
	Value  *float64   `json:"value,omitempty"`
	Status string     `json:"status,omitempty"`
	Text   string     `json:"text,omitempty"`
	Rows   [][]string `json:"rows,omitempty"`
	At     string     `json:"at"`
}

type watchPushFixture struct {
	Name    string                 `json:"name"`
	AlertOn bool                   `json:"alert_on"`
	Steps   []watchPushStepFixture `json:"steps"`
}

type watchPushStepFixture struct {
	Value  *float64        `json:"value,omitempty"`
	Switch string          `json:"switch,omitempty"`
	Rule   json.RawMessage `json:"rule,omitempty"`
	Push   bool            `json:"push"`
	At     string          `json:"at"`
}

func loadWatchFixture(t *testing.T) watchFixture {
	t.Helper()
	p := filepath.Join("..", "..", "..", "spec", "fixtures", "dashboards", "watch.json")
	raw, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read %s: %v", p, err)
	}
	var f watchFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatalf("parse %s: %v", p, err)
	}
	return f
}

func toJSONMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Every one of the 15 draws in spec/fixtures/dashboards/watch.json matches the manager's first section (`persona.watch`)
// and Dashboards index row glance (`glance_row`) exactly in English with UTC at `now`.
func TestWatchFixtureDrawsExact(t *testing.T) {
	fx := loadWatchFixture(t)
	if len(fx.Draws) != 15 {
		t.Fatalf("fixture has %d draws, want 15", len(fx.Draws))
	}
	now, err := time.Parse(time.RFC3339, fx.Now)
	if err != nil {
		t.Fatal(err)
	}
	loc, err := time.LoadLocation(fx.TZ)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range fx.Draws {
		t.Run(tc.Name, func(t *testing.T) {
			watch, err := wire.ParsePageWatch(tc.Watch)
			if err != nil {
				t.Fatalf("ParsePageWatch: %v", err)
			}
			readings := make([]watchReading, 0, len(tc.Readings))
			dbRowsDesc := make([]db.AssistantPageReadingRow, len(tc.Readings))
			for i, rf := range tc.Readings {
				at, err := time.Parse(time.RFC3339, rf.At)
				if err != nil {
					t.Fatalf("reading[%d].at: %v", i, err)
				}
				r := wire.PageWatchReading{
					Value:  rf.Value,
					Status: wire.PageWatchStatusValue(rf.Status),
					Text:   rf.Text,
					Rows:   rf.Rows,
				}
				rawReading, err := json.Marshal(r)
				if err != nil {
					t.Fatalf("marshal reading[%d]: %v", i, err)
				}
				parsed, err := wire.ParsePageReading(watch, rawReading)
				if err != nil {
					t.Fatalf("ParsePageReading[%d]: %v", i, err)
				}
				readings = append(readings, watchReading{reading: *parsed, at: at})
				dbRowsDesc[len(tc.Readings)-1-i] = db.AssistantPageReadingRow{Reading: rawReading, At: at}
			}

			var gotTripped bool
			if len(readings) > 0 {
				gotTripped = wire.Trips(watch.Alert, &readings[len(readings)-1].reading)
			}
			if gotTripped != tc.Tripped {
				t.Fatalf("tripped = %v, want %v", gotTripped, tc.Tripped)
			}

			sec := drawWatchSection(watch, readings, i18n.En, loc, now, 0)
			gotSection := toJSONMap(t, sec)
			if !reflect.DeepEqual(gotSection, tc.Section) {
				gotB, _ := json.MarshalIndent(gotSection, "", "  ")
				wantB, _ := json.MarshalIndent(tc.Section, "", "  ")
				t.Fatalf("section mismatch:\ngot:\n%s\nwant:\n%s", gotB, wantB)
			}

			glance := drawWatchGlance(watch, readings, i18n.En)
			gotGlance := toJSONMap(t, struct {
				Value     string      `json:"value,omitempty"`
				Spark     *wire.Spark `json:"spark,omitempty"`
				Icon      string      `json:"icon,omitempty"`
				IconColor string      `json:"icon_color,omitempty"`
			}{
				Value:     glance.value,
				Spark:     glance.spark,
				Icon:      glance.icon,
				IconColor: glance.iconColor,
			})
			if !reflect.DeepEqual(gotGlance, tc.GlanceRow) {
				gotB, _ := json.MarshalIndent(gotGlance, "", "  ")
				wantB, _ := json.MarshalIndent(tc.GlanceRow, "", "  ")
				t.Fatalf("glance_row mismatch:\ngot:\n%s\nwant:\n%s", gotB, wantB)
			}

			// Verify keptPageRowWith applies the glance without a badge and preserves page icon when empty.
			page := db.AssistantPageRow{ID: "pg_0123456789abcdef", Title: "Test watch", Icon: "heart", IconColor: "blue", Watch: tc.Watch, RoutineID: "rt_1"}
			row := keptPageRowWith(page, dbRowsDesc, i18n.En, loc, now)
			if row.Badge != nil {
				t.Fatalf("watch index row must never have a badge: %+v", row.Badge)
			}
			if row.Value != glance.value {
				t.Fatalf("row.Value = %q, want %q", row.Value, glance.value)
			}
			if len(readings) == 0 {
				if row.Icon != "heart" || row.IconColor != "blue" {
					t.Fatalf("empty watch must keep page icon: got icon=%q color=%q", row.Icon, row.IconColor)
				}
			} else if glance.icon != "" {
				if row.Icon != glance.icon || row.IconColor != glance.iconColor {
					t.Fatalf("row icon=%q color=%q, want %q %q", row.Icon, row.IconColor, glance.icon, glance.iconColor)
				}
			}
		})
	}
}

// zh-Hans and ja translations exist for every watch drawing, rule, footer and alert string, with no missing keys or
// unexpanded placeholders.
func TestWatchLocalizedWords(t *testing.T) {
	now := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	checked := time.Date(2026, 11, 25, 8, 2, 0, 0, time.UTC)
	next := time.Date(2026, 11, 25, 20, 0, 0, 0, time.UTC)
	tomorrow := time.Date(2026, 11, 26, 8, 0, 0, 0, time.UTC)
	later := time.Date(2026, 11, 28, 8, 0, 0, 0, time.UTC)

	watch, err := wire.ParsePageWatch(json.RawMessage(`{"question":"Elm Street rent","display":"number","unit":"USD","alert":{"when":"below","value":2000}}`))
	if err != nil {
		t.Fatal(err)
	}
	v := 1950.0
	readings := []watchReading{{reading: wire.PageWatchReading{Value: &v}, at: checked}}
	routine := &Task{ID: "rt_1", Enabled: true, NextRun: next}
	page := &db.AssistantPageRow{ID: "pg_1111111111111111", Title: "Rent", Watch: json.RawMessage(`{"question":"Elm Street rent","display":"number","unit":"USD","alert":{"when":"below","value":2000}}`), RoutineID: "rt_1"}

	checkText := func(lang i18n.Lang, label, s string) {
		t.Helper()
		if strings.TrimSpace(s) == "" || strings.Contains(s, "assistant-page-watch.") || strings.Contains(s, "{") {
			t.Errorf("%s %s invalid: %q", lang, label, s)
		}
	}
	enDetails := map[string]string{}
	for _, lang := range []i18n.Lang{i18n.En, i18n.ZhHans, i18n.Ja} {
		sec := drawWatchSection(watch, readings, lang, time.UTC, now, 0)
		checkText(lang, "detail", sec.Items[0].Detail)
		checkText(lang, "empty", drawWatchSection(watch, nil, lang, time.UTC, now, 0).Items[0].Detail)
		checkText(lang, "yesterday", watchCheckedTime(lang, time.UTC, now, now.AddDate(0, 0, -1)))
		checkText(lang, "older", watchCheckedTime(lang, time.UTC, now, now.AddDate(0, 0, -3)))
		checkText(lang, "next_today", watchNextTime(lang, time.UTC, now, next))
		checkText(lang, "next_tomorrow", watchNextTime(lang, time.UTC, now, tomorrow))
		checkText(lang, "next_on", watchNextTime(lang, time.UTC, now, later))
		checkText(lang, "footer_line", pageFooterTimeLine(page, pageDrawExtra{
			readings: []db.AssistantPageReadingRow{{At: checked}},
			routine:  routine,
		}, lang, time.UTC, now))
		checkText(lang, "alert_body", pageAlertBody(watch.Question, "1,950 USD", "Drop", lang))
		checkText(lang, "status_ok", watchStatusText(wire.PageWatchStatusOK, lang))
		checkText(lang, "status_warning", watchStatusText(wire.PageWatchStatusWarning, lang))
		checkText(lang, "status_problem", watchStatusText(wire.PageWatchStatusProblem, lang))
		if lang == i18n.En {
			enDetails["detail"] = sec.Items[0].Detail
			enDetails["next_today"] = watchNextTime(lang, time.UTC, now, next)
		} else {
			if sec.Items[0].Detail == enDetails["detail"] {
				t.Errorf("%s detail was not translated: %q", lang, sec.Items[0].Detail)
			}
			if watchNextTime(lang, time.UTC, now, next) == enDetails["next_today"] {
				t.Errorf("%s next_today was not translated", lang)
			}
		}
	}
}

func keptWatchPage(t *testing.T, database *db.DB, title, icon string, watchJSON string, viewJSON string, onOverview, alertOn bool, at time.Time) *db.AssistantPageRow {
	t.Helper()
	ctx := context.Background()
	id := db.NewAssistantPageID()
	var view json.RawMessage
	if viewJSON != "" {
		view = json.RawMessage(viewJSON)
	}
	if _, err := database.CreateAssistantPage(ctx, db.NewAssistantPage{
		ID:             id,
		Title:          title,
		Icon:           icon,
		OwnerKind:      db.AssistantPageOwnerMain,
		CreatedMachine: "server",
		CreatedSession: "main-1",
		OfferView:      "view-" + id,
		View:           view,
		Watch:          json.RawMessage(watchJSON),
		At:             at,
	}); err != nil {
		t.Fatal(err)
	}
	row, _, err := database.KeepAssistantPage(ctx, db.AssistantPageKeep{
		OfferView:  "view-" + id,
		Device:     "phone",
		AlertOn:    alertOn,
		OnOverview: onOverview,
		At:         at,
	})
	if err != nil {
		t.Fatal(err)
	}
	return row
}

func saveTestRoutine(t *testing.T, s *Service, task Task) {
	t.Helper()
	ctx := context.Background()
	sc, err := s.lockedSchedule(ctx)
	if err != nil {
		t.Fatal(err)
	}
	err = s.saveLocked(sc, append(append([]Task(nil), sc.tasks...), task))
	sc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

// Watch page drawing, footer controls (checked/next line, Refresh now, Pause/Resume on watch and non-watch pages,
// Alert me switch only when an alert rule exists, Show on Overview only for web/desktop) and index glance rows.
func TestWatchPageDrawingAndFooter(t *testing.T) {
	s, hub, database := pagesTestService(t)
	ctx := context.Background()
	now := time.Now().UTC()
	checkedAt := time.Date(now.Year(), now.Month(), now.Day(), 8, 2, 0, 0, time.UTC)
	if checkedAt.After(now) {
		checkedAt = now.Add(-time.Hour)
	}

	rentWatch := `{"question":"Rent for a 1-bed on Elm Street","display":"number","unit":"USD","alert":{"when":"below","value":2000}}`
	ownView := `{"fallback":"Notes","sections":[{"key":"notes","items":[{"type":"text","key":"notes.1","text":"Near Elm station."}]}]}`
	rent := keptWatchPage(t, database, "Elm Street rent", "money", rentWatch, ownView, false, false, now.Add(-2*time.Hour))

	noAlertWatch := `{"question":"Morning weight","display":"trend","unit":"kg"}`
	weight := keptWatchPage(t, database, "Morning weight", "trend_up", noAlertWatch, "", true, false, now.Add(-2*time.Hour))

	plain := keptPage(t, database, "Morning run", "main", now.Add(-2*time.Hour), false)

	// Bind a routine to rent (daily 20:00 UTC) and add a tripped reading.
	nextRun := time.Date(now.Year(), now.Month(), now.Day(), 20, 0, 0, 0, time.UTC)
	if !nextRun.After(now) {
		nextRun = nextRun.AddDate(0, 0, 1)
	}
	saveTestRoutine(t, s, Task{
		ID: "rt_rent", Title: "Check rent", Prompt: "Check Elm Street rent", Repeat: repeatDaily,
		Time: nextRun.Format("15:04"), Enabled: true, Push: "asked", CreatedBy: "assistant", CreatedAt: now.Add(-time.Hour),
	})
	if _, _, err := database.BindAssistantPageRoutine(ctx, rent.ID, "rt_rent", now); err != nil {
		t.Fatal(err)
	}
	if _, _, err := database.AddAssistantPageReading(ctx, db.NewAssistantPageReading{
		PageID: rent.ID, Reading: json.RawMessage(`{"value":1985.5}`), ByKind: db.AssistantPageByRoutine, At: checkedAt,
	}); err != nil {
		t.Fatal(err)
	}

	// Render watch page on iOS: persona.watch first, then own section, then footer (NO persona.overview on iOS).
	iosRes := mustPage(t, s, rent.ID, wire.UIRequest{Context: &wire.UIContext{UIVersion: 10, Platform: "ios"}})
	v := iosRes.View
	if len(v.Sections) != 3 || v.Sections[0].Key != "persona.watch" || v.Sections[1].Key != "notes" || v.Sections[2].Key != "persona.footer" {
		t.Fatalf("section order: %+v", v.Sections)
	}
	reading := pageItem(v, "persona.reading")
	if reading.Value != "1,985.5 USD" || reading.Tone != wire.ToneCaution || reading.Icon != "warning" {
		t.Fatalf("watch metric: %+v", reading)
	}
	updated := pageItem(v, "persona.updated").Text
	if !strings.Contains(updated, "Checked ") || !strings.Contains(updated, " · next check ") {
		t.Fatalf("routine footer time line = %q", updated)
	}
	if pageItem(v, "persona.refresh").Action == nil || pageItem(v, "persona.refresh").Action.ID != "persona.refresh" {
		t.Fatalf("missing Refresh now button: %+v", v.Sections[2])
	}
	if pageItem(v, "persona.pause").Action == nil || pageItem(v, "persona.pause").Action.ID != "persona.pause" {
		t.Fatalf("missing Pause updates button: %+v", v.Sections[2])
	}
	alertRow := pageItem(v, "persona.alert")
	if alertRow.Title != "Alert me" || alertRow.Subtitle != "Below 2,000 USD" || alertRow.Control == nil || alertRow.Control.Name != "on" || alertRow.Control.On || alertRow.Control.Action.ID != "persona.alert" {
		t.Fatalf("alert row: %+v", alertRow)
	}
	if pageItem(v, "persona.overview").Type != "" {
		t.Fatalf("Show on Overview must not be drawn on ios: %+v", pageItem(v, "persona.overview"))
	}
	// Footer order: refresh, pause, alert come BEFORE pin, rename, remove.
	footer := v.Sections[2].Items
	idxOf := func(key string) int {
		for i, it := range footer {
			if it.Key == key {
				return i
			}
		}
		return -1
	}
	if !(idxOf("persona.refresh") < idxOf("persona.pause") && idxOf("persona.pause") < idxOf("persona.alert") && idxOf("persona.alert") < idxOf("persona.pin")) {
		t.Fatalf("footer item order wrong: %+v", footer)
	}

	// Render on web & desktop: Show on Overview row IS shown.
	for _, plat := range []string{"web", "desktop"} {
		res := mustPage(t, s, rent.ID, wire.UIRequest{Context: &wire.UIContext{UIVersion: 10, Platform: plat}})
		ov := pageItem(res.View, "persona.overview")
		if ov.Title != "Show on Overview" || ov.Control == nil || ov.Control.Kind != "switch" || ov.Control.Name != "overview" || ov.Control.On || ov.Control.Action.ID != "persona.overview" {
			t.Fatalf("%s overview row: %+v", plat, ov)
		}
	}

	// Watch page with empty {} view and no alert rule draws cleanly and omits persona.alert.
	weightRes := mustPage(t, s, weight.ID, wire.UIRequest{Context: &wire.UIContext{UIVersion: 10, Platform: "web"}})
	if len(weightRes.View.Sections) != 2 || weightRes.View.Sections[0].Key != "persona.watch" || weightRes.View.Sections[1].Key != "persona.footer" {
		t.Fatalf("empty-view watch page sections: %+v", weightRes.View.Sections)
	}
	if pageItem(weightRes.View, "persona.alert").Type != "" {
		t.Fatalf("watch without alert rule must omit Alert me row: %+v", pageItem(weightRes.View, "persona.alert"))
	}
	if !pageItem(weightRes.View, "persona.overview").Control.On {
		t.Fatalf("weight on_overview switch should be on")
	}

	// Toggle Alert me switch (persona.alert) and Show on Overview switch (persona.overview).
	res := mustPage(t, s, rent.ID, wire.UIRequest{Action: "persona.alert", Values: map[string]string{"on": "true"}, Context: &wire.UIContext{UIVersion: 10, Platform: "web"}})
	if res.Toast != "Alerts on" || !pageItem(res.View, "persona.alert").Control.On {
		t.Fatalf("alert on: %+v", res)
	}
	res = mustPage(t, s, rent.ID, wire.UIRequest{Action: "persona.alert", Values: map[string]string{"on": "false"}, Context: &wire.UIContext{UIVersion: 10, Platform: "web"}})
	if res.Toast != "Alerts off" || pageItem(res.View, "persona.alert").Control.On {
		t.Fatalf("alert off: %+v", res)
	}
	if _, err := pagesUI(t, s, rent.ID, wire.UIRequest{Action: "persona.alert", Values: map[string]string{"on": "maybe"}}); !pagesRefused(err, http.StatusBadRequest) {
		t.Fatalf("bad alert value should 400: %v", err)
	}

	beforeLists := len(hub.sent("assistant_pages"))
	res = mustPage(t, s, rent.ID, wire.UIRequest{Action: "persona.overview", Values: map[string]string{"overview": "true"}, Context: &wire.UIContext{UIVersion: 10, Platform: "web"}})
	if res.Toast != "Added to Overview" || !pageItem(res.View, "persona.overview").Control.On {
		t.Fatalf("overview on: %+v", res)
	}
	if len(hub.sent("assistant_pages")) != beforeLists+1 {
		t.Fatalf("toggling overview must emit assistant_pages")
	}
	res = mustPage(t, s, rent.ID, wire.UIRequest{Action: "persona.overview", Values: map[string]string{"overview": "false"}, Context: &wire.UIContext{UIVersion: 10, Platform: "web"}})
	if res.Toast != "Removed from Overview" || pageItem(res.View, "persona.overview").Control.On {
		t.Fatalf("overview off: %+v", res)
	}

	// Pause & Resume on watch page: hides Refresh now, shows "Updates paused", removes next check, updates index subtitle.
	res = mustPage(t, s, rent.ID, wire.UIRequest{Action: "persona.pause"})
	if res.Toast != "Updates paused" || pageItem(res.View, "persona.paused").Text != "Updates paused" ||
		pageItem(res.View, "persona.refresh").Type != "" || pageItem(res.View, "persona.pause").Action.ID != "persona.resume" ||
		strings.Contains(pageItem(res.View, "persona.updated").Text, "next check") {
		t.Fatalf("paused watch footer: %+v", res.View.Sections[2])
	}
	idx := mustPage(t, s, pagesIndex, wire.UIRequest{}).View
	rentRow := pageItem(idx, "page:"+rent.ID)
	if rentRow.Subtitle != "Updates paused" || rentRow.Value != "1,985.5 USD" || rentRow.Icon != "warning" || rentRow.IconColor != "amber" || rentRow.Badge != nil {
		t.Fatalf("paused tripped index row: %+v", rentRow)
	}
	weightRow := pageItem(idx, "page:"+weight.ID)
	if weightRow.Value != "—" || weightRow.Icon != "trend_up" || weightRow.Badge != nil {
		t.Fatalf("empty watch index row must keep its own icon: %+v", weightRow)
	}
	res = mustPage(t, s, rent.ID, wire.UIRequest{Action: "persona.resume"})
	if res.Toast != "Updates resumed" || pageItem(res.View, "persona.paused").Type != "" || pageItem(res.View, "persona.refresh").Action.ID != "persona.refresh" {
		t.Fatalf("resumed watch footer: %+v", res.View.Sections[2])
	}

	// Pause & Resume also exist on a non-watch kept page without a routine.
	plainView := mustPage(t, s, plain.ID, wire.UIRequest{}).View
	if pageItem(plainView, "persona.pause").Action == nil || pageItem(plainView, "persona.pause").Action.ID != "persona.pause" {
		t.Fatalf("non-watch page must have Pause updates: %+v", plainView)
	}
	if pageItem(plainView, "persona.refresh").Type != "" {
		t.Fatalf("non-routine page must not have Refresh now")
	}
	pausedPlain := mustPage(t, s, plain.ID, wire.UIRequest{Action: "persona.pause"})
	if pausedPlain.Toast != "Updates paused" || pageItem(pausedPlain.View, "persona.paused").Text != "Updates paused" || pageItem(pausedPlain.View, "persona.pause").Action.ID != "persona.resume" {
		t.Fatalf("paused plain page: %+v", pausedPlain)
	}
}

// Manual refresh lifecycle: immediate "Checking now…", "Already checking" while running, cleared when a reading lands,
// "The check didn't update the page" when a run ends without a reading, "The check failed" + Retry on failure,
// 6/hour per-page rate limit, and daily separated timing log.
func TestWatchManualRefreshLifecycleRateLimitAndTiming(t *testing.T) {
	var logMu sync.Mutex
	var logs []string
	s, hub, database := pagesTestService(t, func(format string, args ...any) {
		logMu.Lock()
		logs = append(logs, fmt.Sprintf(format, args...))
		logMu.Unlock()
	})
	s.turnPoll = 10 * time.Millisecond
	ctx := context.Background()
	now := time.Now().UTC()

	watchJSON := `{"question":"Rent for a 1-bed on Elm Street","display":"number","unit":"USD","alert":{"when":"below","value":2000}}`
	p := keptWatchPage(t, database, "Elm Street rent", "money", watchJSON, "", false, false, now.Add(-time.Hour))
	saveTestRoutine(t, s, Task{
		ID: "rt_refresh", Title: "Check rent", Prompt: "Check Elm Street rent", Repeat: repeatDaily,
		Time: "20:00", Enabled: true, Push: "asked", CreatedBy: "assistant", CreatedAt: now.Add(-time.Hour),
		RunMachineID: "server", RunSessionID: "rt-sess-1", RunLauncher: "claude",
	})
	hub.fakeHub.setSessions("server",
		proto.Session{SessionID: "main-1", Assistant: true},
		proto.Session{SessionID: "conv-1", Assistant: true},
		proto.Session{SessionID: "rt-sess-1", Assistant: true, Status: proto.StatusIdle, State: proto.StateIdle},
		proto.Session{SessionID: "s1", Assistant: true, Status: proto.StatusIdle, State: proto.StateIdle},
	)
	hub.fakeHub.mu.Lock()
	hub.fakeHub.items = []proto.Item{{ID: "item-0", Kind: "assistant", Text: "initial"}}
	hub.fakeHub.mu.Unlock()
	if _, _, err := database.BindAssistantPageRoutine(ctx, p.ID, "rt_refresh", now); err != nil {
		t.Fatal(err)
	}

	// 1. Refresh now -> immediate "Checking now…" in response + DB RefreshAskedAt set; second tap -> "Already checking".
	res := mustPage(t, s, p.ID, wire.UIRequest{Action: "persona.refresh"})
	if res.Toast != "Checking now…" || pageItem(res.View, "persona.status").Text != "Checking now…" {
		t.Fatalf("refresh receipt: %+v", res)
	}
	if row, err := database.AssistantPage(ctx, p.ID); err != nil || row.RefreshAskedAt == nil {
		t.Fatalf("MarkAssistantPageRefresh not stored: %+v %v", row, err)
	}
	busy := mustPage(t, s, p.ID, wire.UIRequest{Action: "persona.refresh"})
	if busy.Toast != "Already checking" {
		t.Fatalf("second tap while checking: %+v", busy)
	}
	waitFor(t, "refresh run started", func() bool {
		s.pages.events.refresh.mu.Lock()
		defer s.pages.events.refresh.mu.Unlock()
		e := s.pages.events.refresh.active[p.ID]
		return e != nil && !e.startAt.IsZero()
	})
	// Reading lands -> clears "Checking now…" and DB RefreshAskedAt.
	updated, _, err := database.AddAssistantPageReading(ctx, db.NewAssistantPageReading{
		PageID: p.ID, Reading: json.RawMessage(`{"value":2150}`), ByKind: db.AssistantPageByRoutine, At: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	s.pageReadingWritten(ctx, updated)
	afterReading := mustPage(t, s, p.ID, wire.UIRequest{}).View
	if pageItem(afterReading, "persona.status").Type != "" {
		t.Fatalf("reading must clear Checking now…: %+v", pageItem(afterReading, "persona.status"))
	}
	if row, err := database.AssistantPage(ctx, p.ID); err != nil || row.RefreshAskedAt != nil {
		t.Fatalf("reading must clear DB refresh state: %+v %v", row, err)
	}

	// 2. Run ends cleanly without writing a reading -> "The check didn't update the page".
	// Flip s1 to working when the prompt is dispatched, wait until runPageRefresh's watcher captures its baseline,
	// then flip s1 back to idle with a reply.
	hub.fakeHub.mu.Lock()
	hub.fakeHub.onAssistantSend = func(f *fakeHub, _ map[string]any) {
		for i := range f.sessions["server"] {
			if f.sessions["server"][i].SessionID == "s1" {
				f.sessions["server"][i].Status, f.sessions["server"][i].State = proto.StatusWorking, proto.StateWorking
			}
		}
	}
	hub.fakeHub.mu.Unlock()
	res = mustPage(t, s, p.ID, wire.UIRequest{Action: "persona.refresh"})
	if res.Toast != "Checking now…" {
		t.Fatalf("second refresh: %+v", res)
	}
	waitFor(t, "second refresh watcher ready", func() bool {
		s.pages.events.refresh.mu.Lock()
		defer s.pages.events.refresh.mu.Unlock()
		e := s.pages.events.refresh.active[p.ID]
		return e != nil && !e.startAt.IsZero() && e.watching
	})
	hub.fakeHub.mu.Lock()
	hub.fakeHub.onAssistantSend = nil
	if hub.fakeHub.sentItems == nil {
		hub.fakeHub.sentItems = map[string][]proto.Item{}
	}
	hub.fakeHub.sentItems["s1"] = append(hub.fakeHub.sentItems["s1"], proto.Item{ID: "item-1", Role: "assistant", Kind: proto.KindText, Text: "No change"})
	for i := range hub.fakeHub.sessions["server"] {
		if hub.fakeHub.sessions["server"][i].SessionID == "s1" {
			hub.fakeHub.sessions["server"][i].Status, hub.fakeHub.sessions["server"][i].State = proto.StatusIdle, proto.StateIdle
		}
	}
	hub.fakeHub.mu.Unlock()
	waitFor(t, "no_reading status", func() bool {
		return pageItem(mustPage(t, s, p.ID, wire.UIRequest{}).View, "persona.status").Text == "The check didn't update the page"
	})

	// 3. Failed check -> "The check failed" with Retry button calling persona.refresh.
	hub.fakeHub.mu.Lock()
	hub.fakeHub.failAssistant = errors.New("offline")
	hub.fakeHub.mu.Unlock()
	res = mustPage(t, s, p.ID, wire.UIRequest{Action: "persona.refresh"})
	if res.Toast != "Checking now…" {
		t.Fatalf("third refresh: %+v", res)
	}
	waitFor(t, "failed status with retry", func() bool {
		view := mustPage(t, s, p.ID, wire.UIRequest{}).View
		return pageItem(view, "persona.status").Text == "The check failed" &&
			pageItem(view, "persona.retry").Title == "Retry" &&
			pageItem(view, "persona.retry").Action != nil &&
			pageItem(view, "persona.retry").Action.ID == "persona.refresh"
	})

	// 4. Rate limit: 3 manual refreshes used so far; 3 more succeed, 7th within an hour is rate-limited.
	for i := 4; i <= pageRefreshLimit; i++ {
		res = mustPage(t, s, p.ID, wire.UIRequest{Action: "persona.refresh"})
		if res.Toast != "Checking now…" {
			t.Fatalf("refresh #%d: %+v", i, res)
		}
		waitFor(t, fmt.Sprintf("refresh #%d settled", i), func() bool {
			s.pages.events.refresh.mu.Lock()
			defer s.pages.events.refresh.mu.Unlock()
			e := s.pages.events.refresh.active[p.ID]
			return e == nil || e.state != refreshStateChecking
		})
	}
	limited := mustPage(t, s, p.ID, wire.UIRequest{Action: "persona.refresh"})
	if limited.Toast != "You can refresh this page up to 6 times an hour. Try again later." {
		t.Fatalf("7th refresh should be rate-limited: %+v", limited)
	}

	// 5. Daily timing log separates tap->start and start->reading.
	s.logPageRefreshes(ctx, time.Now().UTC().AddDate(0, 0, 1))
	logMu.Lock()
	defer logMu.Unlock()
	var line string
	for _, l := range logs {
		if strings.HasPrefix(l, "assistant pages: refreshes on ") {
			line = l
			break
		}
	}
	if line == "" || !strings.Contains(line, "updated 1, no_update 1, failed 4") ||
		!strings.Contains(line, "tap to start") || !strings.Contains(line, "start to reading (1 updated)") {
		t.Fatalf("daily refresh timing log line = %q (all logs: %v)", line, logs)
	}
}
