package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extmgr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Dashboards phase 2 watch pages (#13020, docs/assistant/dashboards.md "Phase 2: watch pages (#13020)"):
// the manager draws every watch's first section (`persona.watch`) and Dashboards index glance row from the stored
// watch definition and readings (never the model), adds the routine/pause/alert/overview controls to the hub-owned
// footer, runs manual refreshes asynchronously with immediate receipt feedback ("Checking now…"), rate limits manual
// refreshes to 6 per hour per page, and logs separated tap-to-start and start-to-reading timings daily.

const (
	watchEmptyDash = "—"

	pageRefreshLimit       = 6
	pageRefreshWindow      = time.Hour
	pageRefreshWatchWindow = 2 * time.Hour

	refreshStateChecking  = "checking"
	refreshStateNoReading = "no_reading"
	refreshStateFailed    = "failed"

	refreshOutcomeUpdated   = "updated"
	refreshOutcomeNoReading = "no_reading"
	refreshOutcomeFailed    = "failed"
)

// watchReading is one parsed reading paired with its timestamp, ordered oldest first when held in a slice.
type watchReading struct {
	reading wire.PageWatchReading
	at      time.Time
}

// watchGlance is the Dashboards index row's read-only watch glance. Empty Icon leaves the page's own icon intact.
type watchGlance struct {
	value     string
	spark     *wire.Spark
	icon      string
	iconColor string
}

// pageDrawExtra carries the phase 2 context drawPageWith uses beyond phase 1's view and tap state.
type pageDrawExtra struct {
	readings []db.AssistantPageReadingRow // newest first, as db.AssistantPageReadings returns them
	routine  *Task
	refresh  string // "", refreshStateChecking, refreshStateNoReading, refreshStateFailed
	platform string
}

func watchWord(l i18n.Lang, key string) string { return i18n.T(l, "assistant-page-watch."+key) }

func isOverviewPlatform(platform string) bool {
	return platform == "web" || platform == "desktop"
}

func emptyPageView(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || bytes.Equal(trimmed, []byte("{}")) || bytes.Equal(trimmed, []byte("null"))
}

// parseWatchReadings parses DB reading rows (newest first) into chronological order (oldest first), capped to the
// latest AssistantPageReadingsKept readings.
func parseWatchReadings(watch *wire.PageWatch, rows []db.AssistantPageReadingRow) []watchReading {
	if watch == nil || len(rows) == 0 {
		return nil
	}
	n := min(len(rows), db.AssistantPageReadingsKept)
	out := make([]watchReading, 0, n)
	for i := n - 1; i >= 0; i-- {
		r, err := wire.ParsePageReading(watch, rows[i].Reading)
		if err != nil {
			continue
		}
		out = append(out, watchReading{reading: *r, at: rows[i].At})
	}
	return out
}

// formatWatchNumber formats a finite watch number without scientific notation and with ASCII thousands grouping.
func formatWatchNumber(v float64) string {
	if v == 0 {
		v = 0 // normalize -0
	}
	s := strconv.FormatFloat(v, 'f', -1, 64)
	neg := ""
	if strings.HasPrefix(s, "-") {
		neg, s = "-", s[1:]
	}
	intPart, fracPart, hasFrac := strings.Cut(s, ".")
	if len(intPart) > 3 {
		var b strings.Builder
		rem := len(intPart) % 3
		if rem > 0 {
			b.WriteString(intPart[:rem])
		}
		for i := rem; i < len(intPart); i += 3 {
			if b.Len() > 0 {
				b.WriteByte(',')
			}
			b.WriteString(intPart[i : i+3])
		}
		intPart = b.String()
	}
	if hasFrac {
		return neg + intPart + "." + fracPart
	}
	return neg + intPart
}

func formatWatchValue(v float64, unit string) string {
	s := formatWatchNumber(v)
	if unit != "" {
		return s + " " + unit
	}
	return s
}

func watchStatusVisual(status wire.PageWatchStatusValue) (icon, color string) {
	switch status {
	case wire.PageWatchStatusOK:
		return "check", "green"
	case wire.PageWatchStatusWarning:
		return "warning", "amber"
	case wire.PageWatchStatusProblem:
		return "warning", "red"
	}
	return "info", "gray"
}

func watchStatusText(status wire.PageWatchStatusValue, l i18n.Lang) string {
	switch status {
	case wire.PageWatchStatusOK:
		return watchWord(l, "status_ok")
	case wire.PageWatchStatusWarning:
		return watchWord(l, "status_warning")
	case wire.PageWatchStatusProblem:
		return watchWord(l, "status_problem")
	}
	return watchEmptyDash
}

// watchReadingText renders one reading in plain words in language l.
func watchReadingText(watch *wire.PageWatch, r *wire.PageWatchReading, l i18n.Lang) string {
	if watch == nil || r == nil {
		return watchEmptyDash
	}
	switch watch.Display {
	case wire.PageWatchNumber, wire.PageWatchTrend:
		if r.Value == nil {
			return watchEmptyDash
		}
		return formatWatchValue(*r.Value, watch.Unit)
	case wire.PageWatchStatus:
		if r.Text != "" {
			return r.Text
		}
		return watchStatusText(r.Status, l)
	case wire.PageWatchTable:
		if len(r.Rows) == 0 {
			return watchWord(l, "table_none")
		}
		return i18n.T(l, "assistant-page-watch.table_rows", i18n.Args{"count": len(r.Rows)})
	}
	return watchEmptyDash
}

// watchAlertRuleText renders watch.Alert in plain words in language l.
func watchAlertRuleText(watch *wire.PageWatch, l i18n.Lang) string {
	if watch == nil || watch.Alert == nil {
		return ""
	}
	a := watch.Alert
	switch a.When {
	case wire.PageWatchAlertAbove:
		return i18n.T(l, "assistant-page-watch.rule_above", i18n.Args{"value": formatWatchValue(a.Number, watch.Unit)})
	case wire.PageWatchAlertBelow:
		return i18n.T(l, "assistant-page-watch.rule_below", i18n.Args{"value": formatWatchValue(a.Number, watch.Unit)})
	case wire.PageWatchAlertStatus:
		if a.Status == wire.PageWatchStatusWarning {
			return watchWord(l, "rule_status_warning")
		}
		return watchWord(l, "status_problem")
	case wire.PageWatchAlertRows:
		return i18n.T(l, "assistant-page-watch.rule_rows", i18n.Args{"count": a.Rows})
	}
	return ""
}

// watchCheckedTime renders when a watch was checked, with zero-padded 24-hour clock (08:02) in loc.
func watchCheckedTime(l i18n.Lang, loc *time.Location, now, at time.Time) string {
	at, now = at.In(loc), now.In(loc)
	clock := at.Format("15:04")
	day := func(t time.Time) string { return t.Format(time.DateOnly) }
	switch {
	case day(at) == day(now):
		return i18n.T(l, "assistant-page-watch.checked_today", i18n.Args{"time": clock})
	case day(at) == day(now.AddDate(0, 0, -1)):
		return i18n.T(l, "assistant-page-watch.checked_yesterday", i18n.Args{"time": clock})
	}
	return i18n.T(l, "assistant-page-watch.checked_on", i18n.Args{"date": pageDate(l, at)})
}

// watchNextTime renders when the routine runs next, with zero-padded 24-hour clock (20:00) in loc.
func watchNextTime(l i18n.Lang, loc *time.Location, now, next time.Time) string {
	next, now = next.In(loc), now.In(loc)
	clock := next.Format("15:04")
	day := func(t time.Time) string { return t.Format(time.DateOnly) }
	switch {
	case day(next) == day(now):
		return i18n.T(l, "assistant-page-watch.next_check_today", i18n.Args{"time": clock})
	case day(next) == day(now.AddDate(0, 0, 1)):
		return i18n.T(l, "assistant-page-watch.next_check_tomorrow", i18n.Args{"time": clock})
	}
	return i18n.T(l, "assistant-page-watch.next_check_on", i18n.Args{"date": pageDate(l, next), "time": clock})
}

func watchTrendSpark(readings []watchReading, tripped bool) *wire.Spark {
	start := 0
	if len(readings) > db.AssistantPageReadingsKept {
		start = len(readings) - db.AssistantPageReadingsKept
	}
	vals := make([]float64, 0, len(readings)-start)
	for _, r := range readings[start:] {
		if r.reading.Value != nil {
			vals = append(vals, *r.reading.Value)
		}
	}
	if len(vals) < 2 {
		return nil
	}
	sp := &wire.Spark{Values: vals, Fill: true}
	if tripped {
		sp.Tone = wire.ToneCaution
	}
	return sp
}

// drawWatchSection builds the manager-owned first section (`persona.watch`) of a watch page or Overview card.
// readings must be oldest first. When maxTableRows > 0, table rows are capped to at most maxTableRows.
func drawWatchSection(watch *wire.PageWatch, readings []watchReading, l i18n.Lang, loc *time.Location, now time.Time, maxTableRows int) wire.Section {
	if len(readings) == 0 {
		metric := wire.Component{
			Type:   wire.ComponentMetric,
			Key:    "persona.reading",
			Label:  watch.Question,
			Value:  watchEmptyDash,
			Detail: watchWord(l, "not_checked_yet"),
		}
		if watch.Display == wire.PageWatchStatus {
			metric.Icon, metric.IconColor = "info", "gray"
		}
		return wire.Section{Key: "persona.watch", Items: []wire.Component{metric}}
	}
	latest := readings[len(readings)-1]
	tripped := wire.Trips(watch.Alert, &latest.reading)
	detail := watchCheckedTime(l, loc, now, latest.at)
	if tripped {
		if rule := watchAlertRuleText(watch, l); rule != "" {
			detail = rule + " · " + detail
		}
	}
	metric := wire.Component{
		Type:   wire.ComponentMetric,
		Key:    "persona.reading",
		Label:  watch.Question,
		Value:  watchReadingText(watch, &latest.reading, l),
		Detail: detail,
	}
	switch watch.Display {
	case wire.PageWatchStatus:
		metric.Icon, metric.IconColor = watchStatusVisual(latest.reading.Status)
	case wire.PageWatchTrend:
		metric.Spark = watchTrendSpark(readings, tripped)
		if tripped {
			metric.Tone = wire.ToneCaution
			metric.Icon = "warning"
		}
	case wire.PageWatchNumber, wire.PageWatchTable:
		if tripped {
			metric.Tone = wire.ToneCaution
			metric.Icon = "warning"
		}
	}
	items := []wire.Component{metric}
	if watch.Display == wire.PageWatchTable && len(latest.reading.Rows) > 0 {
		cols := make([]wire.TableColumn, len(watch.Columns))
		for i, c := range watch.Columns {
			cols[i] = wire.TableColumn{Title: c}
		}
		src := latest.reading.Rows
		if maxTableRows > 0 && len(src) > maxTableRows {
			src = src[:maxTableRows]
		}
		rows := make([][]string, len(src))
		for i, row := range src {
			rows[i] = slices.Clone(row)
		}
		items = append(items, wire.Component{
			Type:    wire.ComponentTable,
			Key:     "persona.rows",
			Columns: cols,
			Rows:    rows,
		})
	}
	return wire.Section{Key: "persona.watch", Items: items}
}

// drawWatchGlance builds the Dashboards index row's glance from watch and chronological (oldest first) readings.
// Empty returns value "—" with empty Icon/IconColor so the row keeps the page's own icon; never sets a badge.
func drawWatchGlance(watch *wire.PageWatch, readings []watchReading, l i18n.Lang) watchGlance {
	if len(readings) == 0 {
		return watchGlance{value: watchEmptyDash}
	}
	latest := readings[len(readings)-1]
	tripped := wire.Trips(watch.Alert, &latest.reading)
	g := watchGlance{value: watchReadingText(watch, &latest.reading, l)}
	switch watch.Display {
	case wire.PageWatchStatus:
		g.icon, g.iconColor = watchStatusVisual(latest.reading.Status)
	case wire.PageWatchTrend:
		g.spark = watchTrendSpark(readings, tripped)
		if tripped {
			g.icon, g.iconColor = "warning", "amber"
		}
	case wire.PageWatchNumber, wire.PageWatchTable:
		if tripped {
			g.icon, g.iconColor = "warning", "amber"
		}
	}
	return g
}

// keptPageSubtitle is the index row's subtitle for kept page p.
func keptPageSubtitle(p db.AssistantPageRow, readings []db.AssistantPageReadingRow, l i18n.Lang, loc *time.Location, now time.Time) string {
	if p.Paused {
		return watchWord(l, "updates_paused")
	}
	if p.RoutineID != "" {
		if len(readings) > 0 {
			return watchCheckedTime(l, loc, now, readings[0].At)
		}
		return watchWord(l, "not_checked_yet")
	}
	return pageTime(l, loc, now, p.ContentAt, "updated")
}

// pageFooterTimeLine is the first caption line in the hub-owned footer (`persona.updated`).
func pageFooterTimeLine(p *db.AssistantPageRow, extra pageDrawExtra, l i18n.Lang, loc *time.Location, now time.Time) string {
	if p.RoutineID == "" {
		return pageTime(l, loc, now, p.ContentAt, "updated")
	}
	var checked string
	switch {
	case len(extra.readings) > 0:
		checked = watchCheckedTime(l, loc, now, extra.readings[0].At)
	case len(p.Watch) == 0 && extra.routine != nil && !extra.routine.LastRun.IsZero():
		checked = watchCheckedTime(l, loc, now, extra.routine.LastRun)
	default:
		checked = watchWord(l, "not_checked_yet")
	}
	if !p.Paused && extra.routine != nil && !extra.routine.NextRun.IsZero() {
		return checked + " · " + watchNextTime(l, loc, now, extra.routine.NextRun)
	}
	return checked
}

// pageRefreshStatusItems returns the footer's refresh status caption (and Retry button on failure).
func pageRefreshStatusItems(p *db.AssistantPageRow, status string, l i18n.Lang) []wire.Component {
	if p.RoutineID == "" || p.Paused || status == "" {
		return nil
	}
	switch status {
	case refreshStateChecking:
		return []wire.Component{{Type: wire.ComponentText, Key: "persona.status", Text: watchWord(l, "checking_now"), Style: "caption"}}
	case refreshStateNoReading:
		return []wire.Component{{Type: wire.ComponentText, Key: "persona.status", Text: watchWord(l, "check_no_update"), Style: "caption"}}
	case refreshStateFailed:
		return []wire.Component{
			{Type: wire.ComponentText, Key: "persona.status", Text: watchWord(l, "check_failed"), Style: "caption"},
			{Type: wire.ComponentButton, Key: "persona.retry", Title: pageWord(l, "retry"), Action: &wire.Action{ID: "persona.refresh"}},
		}
	}
	return nil
}

// pageWatchFooterControls returns the phase 2 footer controls placed before Pin / Rename / Remove.
func pageWatchFooterControls(p *db.AssistantPageRow, watch *wire.PageWatch, platform string, l i18n.Lang) []wire.Component {
	var items []wire.Component
	if p.RoutineID != "" && !p.Paused {
		items = append(items, wire.Component{
			Type:   wire.ComponentButton,
			Key:    "persona.refresh",
			Title:  watchWord(l, "refresh_now"),
			Action: &wire.Action{ID: "persona.refresh"},
		})
	}
	pause := wire.Component{
		Type:   wire.ComponentButton,
		Key:    "persona.pause",
		Title:  watchWord(l, "pause_updates"),
		Action: &wire.Action{ID: "persona.pause"},
	}
	if p.Paused {
		pause.Title = watchWord(l, "resume_updates")
		pause.Action = &wire.Action{ID: "persona.resume"}
	}
	items = append(items, pause)
	if watch != nil && watch.Alert != nil {
		items = append(items, wire.Component{
			Type:     wire.ComponentRow,
			Key:      "persona.alert",
			Title:    watchWord(l, "alert_me"),
			Subtitle: watchAlertRuleText(watch, l),
			Control: &wire.RowControl{
				Kind:   wire.RowControlKindSwitch,
				Name:   "on",
				On:     p.Alert.On,
				Action: wire.Action{ID: "persona.alert"},
			},
		})
	}
	if watch != nil && isOverviewPlatform(platform) {
		items = append(items, wire.Component{
			Type:  wire.ComponentRow,
			Key:   "persona.overview",
			Title: watchWord(l, "show_on_overview"),
			Control: &wire.RowControl{
				Kind:   wire.RowControlKindSwitch,
				Name:   "overview",
				On:     p.OnOverview,
				Action: wire.Action{ID: "persona.overview"},
			},
		})
	}
	return items
}

// ---- manual refresh runtime, rate limit and daily timing ----

// pageRefreshState tracks per-page manual refresh status, the 6/hour per-page sliding rate limit, and separated
// tap-to-start / start-to-reading timing samples logged once a local day. Held on pageEvents (pages_events.go).
type pageRefreshState struct {
	mu     sync.Mutex
	active map[string]*pageRefreshEntry
	window map[string][]time.Time // manual refresh taps per page within pageRefreshWindow
	seq    int64
	days   map[string][]pageRefreshSample // local day (2006-01-02) -> samples not yet logged
	logged string                         // last day logged
}

type pageRefreshEntry struct {
	id       int64
	state    string // refreshStateChecking, refreshStateNoReading, refreshStateFailed
	tapAt    time.Time
	startAt  time.Time
	watching bool
	cancel   context.CancelFunc
}

type pageRefreshSample struct {
	outcome        string // refreshOutcomeUpdated, refreshOutcomeNoReading, refreshOutcomeFailed
	hasStart       bool
	tapToStart     time.Duration
	hasReading     bool
	startToReading time.Duration
}

func (s *Service) pageRefreshStatusFor(p *db.AssistantPageRow, readings []db.AssistantPageReadingRow, routine *Task, now time.Time) string {
	if p == nil || p.RoutineID == "" || p.Paused {
		return ""
	}
	rs := &s.pages.events.refresh
	rs.mu.Lock()
	entry := rs.active[p.ID]
	var state string
	if entry != nil {
		state = entry.state
	}
	rs.mu.Unlock()
	if state != "" {
		return state
	}
	var latestAt time.Time
	if len(readings) > 0 {
		latestAt = readings[0].At
	}
	if p.RefreshAskedAt != nil {
		asked := *p.RefreshAskedAt
		if !latestAt.IsZero() && !latestAt.Before(asked) {
			return ""
		}
		if routine != nil && routine.LastStatus == runFailed && !routine.lastShown().Before(asked) {
			return refreshStateFailed
		}
		if now.Sub(asked) < pageRefreshWatchWindow {
			return refreshStateChecking
		}
	}
	if routine != nil && routine.LastStatus == runFailed && !routine.lastShown().IsZero() {
		if latestAt.IsZero() || routine.lastShown().After(latestAt) {
			return refreshStateFailed
		}
	}
	return ""
}

func (s *Service) beginPageRefresh(pageID string, now time.Time, cancel context.CancelFunc) (runID int64, busy, limited bool) {
	rs := &s.pages.events.refresh
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if rs.active == nil {
		rs.active = map[string]*pageRefreshEntry{}
	}
	if rs.window == nil {
		rs.window = map[string][]time.Time{}
	}
	if cur := rs.active[pageID]; cur != nil && cur.state == refreshStateChecking {
		return 0, true, false
	}
	cutoff := now.Add(-pageRefreshWindow)
	ts := rs.window[pageID]
	keep := ts[:0]
	for _, t := range ts {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	rs.window[pageID] = keep
	if len(keep) >= pageRefreshLimit {
		return 0, false, true
	}
	rs.window[pageID] = append(keep, now)
	rs.seq++
	id := rs.seq
	rs.active[pageID] = &pageRefreshEntry{id: id, state: refreshStateChecking, tapAt: now, cancel: cancel}
	return id, false, false
}

func (s *Service) abortPageRefresh(pageID string, runID int64) {
	rs := &s.pages.events.refresh
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if cur := rs.active[pageID]; cur != nil && cur.id == runID {
		if cur.cancel != nil {
			cur.cancel()
		}
		delete(rs.active, pageID)
	}
	if ts := rs.window[pageID]; len(ts) > 0 {
		rs.window[pageID] = ts[:len(ts)-1]
	}
}

func (s *Service) notePageRefreshStarted(pageID string, runID int64, startAt time.Time) {
	rs := &s.pages.events.refresh
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if cur := rs.active[pageID]; cur != nil && cur.id == runID {
		cur.startAt = startAt
	}
}

func (s *Service) notePageRefreshWatching(pageID string, runID int64) {
	rs := &s.pages.events.refresh
	rs.mu.Lock()
	defer rs.mu.Unlock()
	if cur := rs.active[pageID]; cur != nil && cur.id == runID {
		cur.watching = true
	}
}

func (s *Service) finishPageRefresh(ctx context.Context, pageID string, runID int64, state, outcome string, at time.Time) bool {
	rs := &s.pages.events.refresh
	loc := s.location(ctx)
	rs.mu.Lock()
	cur := rs.active[pageID]
	if cur == nil || cur.id != runID || cur.state != refreshStateChecking {
		rs.mu.Unlock()
		return false
	}
	sample := pageRefreshSample{outcome: outcome}
	if !cur.startAt.IsZero() {
		sample.hasStart = true
		sample.tapToStart = max(cur.startAt.Sub(cur.tapAt), 0)
	}
	if cur.cancel != nil {
		cur.cancel()
		cur.cancel = nil
	}
	cur.state = state
	day := cur.tapAt.In(loc).Format(time.DateOnly)
	if rs.days == nil {
		rs.days = map[string][]pageRefreshSample{}
	}
	rs.days[day] = append(rs.days[day], sample)
	rs.mu.Unlock()
	s.logPageRefreshes(ctx, at)
	return true
}

// clearPageRefreshOnReading clears any pending manual refresh state for pageID when a reading lands, recording the
// separated tap->start and start->reading timings.
func (s *Service) clearPageRefreshOnReading(ctx context.Context, pageID string, readingAt time.Time) {
	rs := &s.pages.events.refresh
	loc := s.location(ctx)
	rs.mu.Lock()
	cur := rs.active[pageID]
	delete(rs.active, pageID)
	if cur != nil {
		if cur.cancel != nil {
			cur.cancel()
		}
		if cur.state == refreshStateChecking {
			sample := pageRefreshSample{outcome: refreshOutcomeUpdated}
			if !cur.startAt.IsZero() {
				sample.hasStart = true
				sample.tapToStart = max(cur.startAt.Sub(cur.tapAt), 0)
				sample.hasReading = true
				sample.startToReading = max(readingAt.Sub(cur.startAt), 0)
			} else {
				sample.hasReading = true
				sample.startToReading = max(readingAt.Sub(cur.tapAt), 0)
			}
			day := cur.tapAt.In(loc).Format(time.DateOnly)
			if rs.days == nil {
				rs.days = map[string][]pageRefreshSample{}
			}
			rs.days[day] = append(rs.days[day], sample)
		}
	}
	rs.mu.Unlock()
	s.logPageRefreshes(ctx, readingAt)
}

// cancelPageRefresh drops any in-memory refresh state for pageID (pause, remove, delete).
func (s *Service) cancelPageRefresh(pageID string) {
	rs := &s.pages.events.refresh
	rs.mu.Lock()
	if cur := rs.active[pageID]; cur != nil {
		if cur.cancel != nil {
			cur.cancel()
		}
		delete(rs.active, pageID)
	}
	rs.mu.Unlock()
}

// logPageRefreshes logs completed local days before now's day once each, separating tap-to-start and start-to-reading
// latencies (Principle 7).
func (s *Service) logPageRefreshes(ctx context.Context, now time.Time) {
	loc := s.location(ctx)
	today := now.In(loc).Format(time.DateOnly)
	rs := &s.pages.events.refresh
	rs.mu.Lock()
	if len(rs.days) == 0 {
		rs.mu.Unlock()
		return
	}
	var ready []string
	for day := range rs.days {
		if day < today && day > rs.logged {
			ready = append(ready, day)
		}
	}
	slices.Sort(ready)
	lines := make([]string, 0, len(ready))
	for _, day := range ready {
		lines = append(lines, pageRefreshDayLine(day, rs.days[day]))
		delete(rs.days, day)
		rs.logged = day
	}
	for day := range rs.days {
		if day < today {
			delete(rs.days, day)
		}
	}
	rs.mu.Unlock()
	if s.logf != nil {
		for _, line := range lines {
			s.logf("%s", line)
		}
	}
}

func pageRefreshDayLine(day string, samples []pageRefreshSample) string {
	var updated, noReading, failed int
	var starts, readings []time.Duration
	for _, sp := range samples {
		switch sp.outcome {
		case refreshOutcomeUpdated:
			updated++
		case refreshOutcomeNoReading:
			noReading++
		case refreshOutcomeFailed:
			failed++
		}
		if sp.hasStart {
			starts = append(starts, sp.tapToStart)
		}
		if sp.hasReading {
			readings = append(readings, sp.startToReading)
		}
	}
	sp50, sp95, smax := durationPercentiles(starts)
	rp50, rp95, rmax := durationPercentiles(readings)
	return fmt.Sprintf("assistant pages: refreshes on %s: %d (updated %d, no_update %d, failed %d); tap to start (%d started) p50 %v p95 %v max %v; "+
		"start to reading (%d updated) p50 %v p95 %v max %v",
		day, len(samples), updated, noReading, failed, len(starts), sp50, sp95, smax, len(readings), rp50, rp95, rmax)
}

func durationPercentiles(ds []time.Duration) (p50, p95, mx time.Duration) {
	if len(ds) == 0 {
		return 0, 0, 0
	}
	sorted := slices.Clone(ds)
	slices.Sort(sorted)
	at := func(pct float64) time.Duration {
		idx := int(float64(len(sorted)-1) * pct)
		return sorted[idx].Round(time.Millisecond)
	}
	return at(0.50), at(0.95), sorted[len(sorted)-1].Round(time.Millisecond)
}

// ---- owner actions for watch / routine / pause / alert / overview ----

func parseSwitchBool(raw string) (bool, bool) {
	switch raw {
	case "true":
		return true, true
	case "false":
		return false, true
	}
	return false, false
}

// pageWatchAction handles persona.refresh, persona.pause, persona.resume, persona.alert, persona.overview on kept page p.
func (r *pageRequest) pageWatchAction(ctx context.Context, p *db.AssistantPageRow, action string) (extension.ActionResult, error) {
	switch action {
	case "persona.refresh":
		return r.pageRefresh(ctx, p)
	case "persona.pause", "persona.resume":
		pause := action == "persona.pause"
		updated, changed, err := r.st.PauseAssistantPage(ctx, p.ID, pause, r.now)
		if err != nil {
			return extension.ActionResult{}, err
		}
		if pause {
			r.s.cancelPageRefresh(p.ID)
			_, _, _ = r.st.ClearAssistantPageRefresh(ctx, p.ID, r.now)
		}
		if changed {
			r.s.pageChanged(p.ID, pageContent)
			r.s.pageChanged(pagesIndex, pageContent)
		}
		p, err = r.st.AssistantPage(ctx, updated.ID)
		if err != nil {
			p = updated
		}
		key := "resumed_toast"
		if pause {
			key = "paused_toast"
		}
		return r.pageResult(ctx, p, r.state.P, watchWord(r.l, key))
	case "persona.alert":
		if len(p.Watch) == 0 {
			return extension.ActionResult{}, pagesRefusal(http.StatusBadRequest, "bad_request", "bad_action")
		}
		watch, err := wire.ParsePageWatch(p.Watch)
		if err != nil || watch.Alert == nil {
			return extension.ActionResult{}, pagesRefusal(http.StatusBadRequest, "bad_request", "bad_action")
		}
		on, ok := parseSwitchBool(r.in.Values["on"])
		if !ok {
			return extension.ActionResult{}, &extmgr.PagesError{Status: http.StatusBadRequest, Code: "bad_request", Err: i18n.Err("assistant-page-watch.bad_switch")}
		}
		updated, changed, err := r.st.SetAssistantPageAlert(ctx, p.ID, on, r.now)
		if err != nil {
			return extension.ActionResult{}, err
		}
		if changed {
			r.s.pageChanged(p.ID, pageContent)
		}
		p, err = r.st.AssistantPage(ctx, updated.ID)
		if err != nil {
			p = updated
		}
		key := "alert_off_toast"
		if on {
			key = "alert_on_toast"
		}
		return r.pageResult(ctx, p, r.state.P, watchWord(r.l, key))
	case "persona.overview":
		if len(p.Watch) == 0 {
			return extension.ActionResult{}, pagesRefusal(http.StatusBadRequest, "bad_request", "bad_action")
		}
		raw, has := r.in.Values["overview"]
		if !has {
			raw = r.in.Values["on"]
		}
		on, ok := parseSwitchBool(raw)
		if !ok {
			return extension.ActionResult{}, &extmgr.PagesError{Status: http.StatusBadRequest, Code: "bad_request", Err: i18n.Err("assistant-page-watch.bad_switch")}
		}
		updated, changed, err := r.st.SetAssistantPageOverview(ctx, p.ID, on, r.now)
		if err != nil {
			return extension.ActionResult{}, err
		}
		if changed {
			r.s.pagesListChanged(ctx)
			r.s.pageChanged(p.ID, pageContent)
			r.s.pageChanged(overviewCardID(p.ID), pageContent)
		}
		p, err = r.st.AssistantPage(ctx, updated.ID)
		if err != nil {
			p = updated
		}
		key := "overview_off_toast"
		if on {
			key = "overview_on_toast"
		}
		return r.pageResult(ctx, p, r.state.P, watchWord(r.l, key))
	}
	return extension.ActionResult{}, pagesRefusal(http.StatusBadRequest, "bad_request", "bad_action")
}

// pageRefresh handles persona.refresh: validates state and rate limit synchronously, persists MarkAssistantPageRefresh,
// answers immediately with "Checking now…" (Principle 7), and runs the bound routine asynchronously.
func (r *pageRequest) pageRefresh(ctx context.Context, p *db.AssistantPageRow) (extension.ActionResult, error) {
	if p.RoutineID == "" {
		return extension.ActionResult{}, pagesRefusal(http.StatusBadRequest, "bad_request", "bad_action")
	}
	if p.Paused {
		return r.pageResult(ctx, p, r.state.P, watchWord(r.l, "updates_paused"))
	}
	readings, _ := r.st.AssistantPageReadings(ctx, p.ID, 1)
	var routine *Task
	if t, ok := r.s.taskView(ctx, p.RoutineID); ok {
		routine = &t
	}
	if r.s.pageRefreshStatusFor(p, readings, routine, r.now) == refreshStateChecking {
		return r.pageResult(ctx, p, r.state.P, watchWord(r.l, "already_checking"))
	}
	watchCtx, cancel := context.WithTimeout(context.Background(), pageRefreshWatchWindow)
	runID, busy, limited := r.s.beginPageRefresh(p.ID, r.now, cancel)
	if busy {
		cancel()
		return r.pageResult(ctx, p, r.state.P, watchWord(r.l, "already_checking"))
	}
	if limited {
		cancel()
		return r.pageResult(ctx, p, r.state.P, watchWord(r.l, "refresh_rate"))
	}
	updated, err := r.st.MarkAssistantPageRefresh(ctx, p.ID, r.device, r.now)
	if err != nil {
		r.s.abortPageRefresh(p.ID, runID)
		return extension.ActionResult{}, err
	}
	pCopy := *p
	pCopy.RefreshAskedAt = updated.RefreshAskedAt
	pCopy.RefreshDevice = updated.RefreshDevice
	r.s.pageChanged(p.ID, pageStatus)
	go r.s.runPageRefresh(watchCtx, p.ID, p.RoutineID, runID, r.now)
	return r.pageResult(ctx, &pCopy, r.state.P, watchWord(r.l, "checking_now"))
}

// runPageRefresh starts the routine bound to pageID and watches until a reading arrives, the turn ends without one,
// or the run fails.
func (s *Service) runPageRefresh(watchCtx context.Context, pageID, routineID string, runID int64, tapAt time.Time) {
	defer s.clearWatchCancel(pageID, runID)
	runCtx, cancelRun := context.WithTimeout(watchCtx, 2*time.Minute)
	sess, err := s.runTaskNow(runCtx, routineID, time.Now())
	cancelRun()
	startAt := time.Now()
	if err != nil {
		if errors.Is(err, errTaskBusy) {
			if t, ok := s.taskView(watchCtx, routineID); ok && t.RunSessionID != "" {
				machine := t.RunMachineID
				if machine == "" {
					machine = t.MachineID
				}
				sess = proto.Session{MachineID: machine, SessionID: t.RunSessionID}
				err = nil
			}
		}
	}
	if err != nil {
		if watchCtx.Err() != nil {
			return
		}
		s.clearPageRefreshDB(pageID)
		if s.finishPageRefresh(context.Background(), pageID, runID, refreshStateFailed, refreshOutcomeFailed, time.Now()) {
			s.pageChanged(pageID, pageStatus)
		}
		return
	}
	s.notePageRefreshStarted(pageID, runID, startAt)
	ended, _ := s.waitTurnEndOnReady(watchCtx, sess.MachineID, sess.SessionID, func() {
		s.notePageRefreshWatching(pageID, runID)
	})
	if watchCtx.Err() != nil {
		return // cleared by pageReadingWritten, pause, or remove
	}
	// Check whether a reading already landed right before waitTurnEnd returned.
	if st, ok := s.pageStore(); ok {
		checkCtx, cancelCheck := context.WithTimeout(context.Background(), 5*time.Second)
		rows, rerr := st.AssistantPageReadings(checkCtx, pageID, 1)
		cancelCheck()
		if rerr == nil && len(rows) > 0 && !rows[0].At.Before(tapAt) {
			s.clearPageRefreshOnReading(context.Background(), pageID, rows[0].At)
			s.clearPageRefreshDB(pageID)
			s.pageChanged(pageID, pageStatus)
			return
		}
	}
	failed := !ended
	if !failed {
		checkCtx, cancelCheck := context.WithTimeout(context.Background(), 10*time.Second)
		if s.lastTurnAPIError(checkCtx, sess.MachineID, sess.SessionID) != "" {
			failed = true
		} else if c, ok := s.session(checkCtx, sess.MachineID, sess.SessionID); ok && c.TurnError != "" {
			failed = true
		} else if t, ok := s.taskView(checkCtx, routineID); ok && t.LastStatus == runFailed && !t.lastShown().Before(tapAt.Add(-time.Second)) {
			failed = true
		}
		cancelCheck()
	}
	s.clearPageRefreshDB(pageID)
	state, outcome := refreshStateNoReading, refreshOutcomeNoReading
	if failed {
		state, outcome = refreshStateFailed, refreshOutcomeFailed
	}
	if s.finishPageRefresh(context.Background(), pageID, runID, state, outcome, time.Now()) {
		s.pageChanged(pageID, pageStatus)
	}
}

func (s *Service) clearPageRefreshDB(pageID string) {
	st, ok := s.pageStore()
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _, _ = st.ClearAssistantPageRefresh(ctx, pageID, time.Now())
}

func (s *Service) clearWatchCancel(pageID string, runID int64) {
	rs := &s.pages.events.refresh
	rs.mu.Lock()
	if cur := rs.active[pageID]; cur != nil && cur.id == runID && cur.cancel != nil {
		cur.cancel()
		cur.cancel = nil
	}
	rs.mu.Unlock()
}
