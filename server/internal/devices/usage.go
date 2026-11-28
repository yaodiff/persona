package devices

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// Token usage by machine (docs/server/machines.md "Token usage", #4973). The nodes meter their transcripts per minute,
// model and launcher (node/usage.go) for the last 26 hours; the manager keeps them in memory for the island
// (metrics.go's usageBook) and in Postgres: minute rows for 27 hours (the node's window and an hour), then a row each
// 10 minutes for the week, then a row an hour for 53 weeks (usage_history.go). GET /api/v1/machines/{id}/usage draws a
// machine's range from them, GET /api/v1/metrics carries each machine's last hour for its card (usageCard).

const (
	usageMinutesFor = 27 * time.Hour
	usageKeep       = 7*24*time.Hour + time.Hour // ten-minute rows: a 7-day chart starts full
	// usageNodeWindow is how far back a node's whole window ("full") goes (node/usage.go's usageWindow): from then on
	// what it sent is the record.
	usageNodeWindow = 26 * time.Hour
	// usageGather: how long usageLoop takes the writes that come after one, to store them with it in one transaction
	// (one commit, so one WAL flush, not one a write): each node reports on a 10 s tick of its own, so with several
	// machines a window takes several. It waits only while another machine has written in the last usageActive (a
	// single busy machine's writes are stored at once); a chart's finest step is a minute. usageGatherBuckets bounds a
	// transaction when the nodes send their whole windows at once (a manager that came back).
	usageGather        = 5 * time.Second
	usageActive        = 25 * time.Second
	usageGatherBuckets = 4096
	usageWriteTimeout  = 30 * time.Second
)

type usageStore interface {
	PutUsage(ctx context.Context, machine string, buckets []proto.UsageBucket, replaceFrom time.Time) error
	RollupUsage(ctx context.Context, before time.Time) (int64, error)
	PruneUsage(ctx context.Context, before time.Time) (int64, error)
	UsageSeries(ctx context.Context, machine string, from, to time.Time, step int) ([]db.UsageRow, error)
}

// usageBatchStore stores several writes in one transaction, in order (db.PutUsages); a store without it gets one
// PutUsage a write.
type usageBatchStore interface {
	PutUsages(ctx context.Context, writes []db.UsageWrite) (rejected []error, err error)
}

type additiveUsageStore interface {
	AddUsage(ctx context.Context, machine string, buckets []proto.UsageBucket) error
}

func (d *Devices) usageStore() usageStore {
	if s, ok := d.opts.Store.(usageStore); ok {
		return s
	}
	return nil
}

func (d *Devices) restoreRuntimeUsage(ctx context.Context, now time.Time) {
	store, ok := d.opts.Store.(interface {
		RecentRuntimeUsage(context.Context, time.Time) (map[string][]proto.UsageBucket, error)
	})
	if !ok {
		return
	}
	restoreCtx, cancel := context.WithTimeout(ctx, usageWriteTimeout)
	defer cancel()
	buckets, err := store.RecentRuntimeUsage(restoreCtx, now.Truncate(time.Minute).Add(-usageNodeWindow))
	if err != nil {
		d.logf("restore recent runtime usage: %v", err)
		return
	}
	d.usage.mu.Lock()
	defer d.usage.mu.Unlock()
	if d.usage.machines == nil {
		d.usage.machines = map[string]*machineUsage{}
	}
	for machine, entries := range buckets {
		m := d.usage.machines[machine]
		if m == nil {
			m = &machineUsage{buckets: map[usageKey]proto.UsageBucket{}}
			d.usage.machines[machine] = m
		}
		for _, b := range entries {
			k := keyOf(b)
			if _, exists := m.buckets[k]; !exists {
				m.buckets[k] = b
			}
		}
	}
}

// usageWrite is one "usage" notification to store; written in order (a full window, then what changed).
type usageWrite struct {
	machine     string
	buckets     []proto.UsageBucket
	replaceFrom time.Time
	add         bool
	dirs        []proto.UsageDir // what the node resolved directories to, kept before the buckets
}

// onUsage takes a node's "usage" notification: the island's sums (in memory), and Postgres for the charts.
func (d *Devices) onUsage(machine string, params json.RawMessage, now time.Time) {
	var p struct {
		Buckets []proto.UsageBucket `json:"buckets"`
		Full    bool                `json:"full"`
		Add     bool                `json:"add"`
		Dirs    []proto.UsageDir    `json:"dirs"`
	}
	if json.Unmarshal(params, &p) != nil {
		return
	}
	dirs := usableDirs(p.Dirs)
	if len(dirs) < len(p.Dirs) { // a node's bug: never its text, which may hold a path
		d.logf("[%s] usage: %d of %d directories refused", machine, len(p.Dirs)-len(dirs), len(p.Dirs))
	}
	store := d.usageStore()
	_, additive := store.(additiveUsageStore)
	_, batch := store.(usageBatchStore)
	if p.Add && !additive && !batch {
		d.logf("[%s] usage: runtime usage requires an additive store", machine)
		return
	}
	if !d.usage.apply(machine, params, now) || store == nil || (len(p.Buckets) == 0 && !p.Full && len(dirs) == 0) {
		return
	}
	w := usageWrite{machine: machine, buckets: p.Buckets, add: p.Add, dirs: dirs}
	if p.Full {
		w.replaceFrom = now.Add(-usageNodeWindow).Truncate(time.Minute).Add(time.Minute)
	}
	select {
	case d.usageWrites <- w:
	default: // Postgres is far behind: the next full window (a reconnect) writes it again
		d.logf("[%s] usage: %d buckets not saved, the queue is full", machine, len(p.Buckets))
	}
}

// usageLoop writes the notifications in the order they came (those within usageGather of each other together, when
// the store can), and each minute rolls up the minute rows past 27 hours and the ten-minute rows past a week, and drops
// what is older than the history keeps (a week without it).
func (d *Devices) usageLoop(ctx context.Context) {
	st := d.usageStore()
	tidy := time.NewTicker(metricsTidyEach)
	defer tidy.Stop()
	active := map[string]time.Time{} // when each machine's last write came
	for {
		select {
		case <-ctx.Done():
			return
		case w := <-d.usageWrites:
			if st == nil {
				continue
			}
			writes := []usageWrite{w}
			if _, ok := st.(usageBatchStore); ok {
				writes = d.gatherUsage(ctx, w, active)
			}
			if ctx.Err() != nil { // stopping: as with what is queued, the nodes' next whole window (a reconnect) writes them
				return
			}
			d.saveUsage(ctx, st, writes)
		case <-tidy.C:
			if st == nil {
				continue
			}
			now := time.Now()
			if _, err := st.RollupUsage(ctx, now.Add(-usageMinutesFor)); err != nil && ctx.Err() == nil {
				d.logf("usage rollup: %v", err)
			}
			keep := usageKeep
			if hs, ok := st.(usageHistoryStore); ok {
				if _, err := hs.RollupUsageHours(ctx, now.Add(-usageKeep)); err != nil && ctx.Err() == nil {
					d.logf("usage rollup by the hour: %v", err)
				}
				keep = usageHistoryKeep
			}
			if _, err := st.PruneUsage(ctx, now.Add(-keep)); err != nil && ctx.Err() == nil {
				d.logf("usage prune: %v", err)
			}
		}
	}
}

// gatherUsage is first, what is queued after it, and, while another machine wrote lately (active), the writes that
// come within d.usageWait of it: in order, usageGatherBuckets at most. nil when ctx ended while it waited.
func (d *Devices) gatherUsage(ctx context.Context, first usageWrite, active map[string]time.Time) []usageWrite {
	now := time.Now()
	others := false
	for m, at := range active {
		if now.Sub(at) > usageActive {
			delete(active, m)
		} else if m != first.machine {
			others = true
		}
	}
	active[first.machine] = now
	writes, buckets := []usageWrite{first}, len(first.buckets)
	var wait <-chan time.Time // nil: take only what is queued
	if others {
		t := time.NewTimer(d.usageWait)
		defer t.Stop()
		wait = t.C
	}
	for buckets < usageGatherBuckets {
		var w usageWrite
		select {
		case w = <-d.usageWrites: // what is queued first
		default:
			if wait == nil {
				return writes
			}
			select {
			case w = <-d.usageWrites:
			case <-wait:
				return writes
			case <-ctx.Done():
				return nil
			}
		}
		writes, buckets = append(writes, w), buckets+len(w.buckets)
		active[w.machine] = time.Now()
	}
	return writes
}

// saveUsage stores writes in order: in one transaction when the store can, else one PutUsage each.
func (d *Devices) saveUsage(ctx context.Context, st usageStore, writes []usageWrite) {
	bs, ok := st.(usageBatchStore)
	if !ok {
		for _, w := range writes {
			wctx, cancel := context.WithTimeout(ctx, usageWriteTimeout)
			var err error
			if w.add {
				if add, ok := st.(additiveUsageStore); ok {
					err = add.AddUsage(wctx, w.machine, w.buckets)
				}
			} else {
				err = st.PutUsage(wctx, w.machine, w.buckets, w.replaceFrom)
			}
			if err != nil && ctx.Err() == nil {
				d.logf("[%s] save usage: %v", w.machine, err)
			}
			cancel()
		}
		return
	}
	batch := make([]db.UsageWrite, len(writes))
	for i, w := range writes {
		batch[i] = db.UsageWrite{Machine: w.machine, Buckets: w.buckets, ReplaceFrom: w.replaceFrom, Add: w.add, Dirs: w.dirs}
	}
	wctx, cancel := context.WithTimeout(ctx, usageWriteTimeout)
	defer cancel()
	rejected, err := bs.PutUsages(wctx, batch)
	if ctx.Err() != nil {
		return
	}
	for i, w := range writes {
		if e := err; e != nil || (rejected != nil && rejected[i] != nil) {
			if e == nil {
				e = rejected[i]
			}
			d.logf("[%s] save usage: %v", w.machine, e)
		}
	}
}

// usageRanges: what a chart can show, and its step (60, 144, 168 points).
var usageRanges = map[string]struct {
	span time.Duration
	step int
}{
	"1h":  {time.Hour, 60},
	"24h": {24 * time.Hour, 600},
	"7d":  {7 * 24 * time.Hour, 3600},
}

// UsageTokens are the four kinds of tokens and the replies, summed over some time (the fields of proto.UsageBucket).
type UsageTokens struct {
	Input      int64 `json:"in"` // not from the cache
	CacheWrite int64 `json:"cache_write"`
	CacheRead  int64 `json:"cache_read"`
	Output     int64 `json:"out"`
	Requests   int   `json:"req"`
}

func (t *UsageTokens) add(b proto.UsageBucket) {
	t.Input += b.Input
	t.CacheWrite += b.CacheWrite
	t.CacheRead += b.CacheRead
	t.Output += b.Output
	t.Requests += b.Requests
}

// Total is every token, cache reads included.
func (t UsageTokens) Total() int64 { return t.Input + t.CacheWrite + t.CacheRead + t.Output }

// UsagePoint is one step of a usage chart: its sums (per step, not per minute: a client divides by step/60), or a
// break where the machine was offline (no monitoring sample in it and nothing counted).
type UsagePoint struct {
	T int64 `json:"t"`
	UsageTokens
	Gap bool `json:"gap,omitempty"`
}

// UsageShare is one model's or launcher's sums over the range.
type UsageShare struct {
	Name string `json:"name"` // a launcher's "": unknown (typed in a terminal, an older node)
	UsageTokens
}

// MachineUsageSeries is GET /api/v1/machines/{id}/usage.
type MachineUsageSeries struct {
	MachineID string       `json:"machine_id"`
	Online    bool         `json:"online"`
	Range     string       `json:"range"`
	Step      int          `json:"step"`
	Points    []UsagePoint `json:"points"`
	Total     UsageTokens  `json:"total"`
	// Models: each model's share under the launcher filter; Launchers: each launcher's under the model filter, both
	// largest first. So the filters' menus list what there is to pick.
	Models    []UsageShare `json:"models"`
	Launchers []UsageShare `json:"launchers"`
	// LaunchersKnown: the machine's node names the launcher of its usage; false for an older node (its usage is all
	// under "": the apps grey out the launcher filter and say to update it).
	LaunchersKnown bool `json:"launchers_known"`
	// Stored: the manager keeps the week (Postgres); false: only what is in memory (the last 26 hours).
	Stored bool `json:"stored"`
}

// usageFilter: a model and a launcher to keep; nil keeps all.
type usageFilter struct{ model, launcher *string }

func (f usageFilter) keeps(b proto.UsageBucket, model, launcher bool) bool {
	return (!model || f.model == nil || b.Model == *f.model) && (!launcher || f.launcher == nil || b.Launcher == *f.launcher)
}

// usageSeries sums rows (any order, Minute a step start or a minute) into the range's steps from from to now.
// present: the steps with a monitoring sample (nil: no monitoring to tell offline time by, no breaks).
func usageSeries(rows []proto.UsageBucket, from, now time.Time, step int, f usageFilter, present map[int64]bool) ([]UsagePoint, UsageTokens, []UsageShare, []UsageShare) {
	first, last := from.Unix()/int64(step)*int64(step), now.Unix()/int64(step)*int64(step)
	n := int((last-first)/int64(step)) + 1
	points := make([]UsagePoint, n)
	counted := make([]bool, n)
	for i := range points {
		points[i].T = first + int64(i*step)
	}
	var total UsageTokens
	models, launchers := map[string]*UsageShare{}, map[string]*UsageShare{}
	share := func(m map[string]*UsageShare, name string) *UsageShare {
		s := m[name]
		if s == nil {
			s = &UsageShare{Name: name}
			m[name] = s
		}
		return s
	}
	for _, b := range rows {
		i := int((b.Minute/int64(step)*int64(step) - first) / int64(step))
		if i < 0 || i >= n {
			continue
		}
		if f.keeps(b, false, true) {
			share(models, b.Model).add(b)
		}
		if f.keeps(b, true, false) {
			share(launchers, b.Launcher).add(b)
		}
		if f.keeps(b, true, true) {
			points[i].add(b)
			total.add(b)
			counted[i] = true
		}
	}
	if present != nil {
		for i := range points {
			points[i].Gap = !present[points[i].T] && !counted[i] && points[i].Total() == 0
		}
	}
	sorted := func(m map[string]*UsageShare) []UsageShare {
		out := make([]UsageShare, 0, len(m))
		for _, s := range m {
			out = append(out, *s)
		}
		slices.SortFunc(out, func(a, b UsageShare) int {
			return cmp.Or(cmp.Compare(b.Total(), a.Total()), cmp.Compare(a.Name, b.Name))
		})
		return out
	}
	return points, total, sorted(models), sorted(launchers)
}

// machineUsage draws a machine's range: from Postgres, or from memory (the last 26 hours) without it.
func (d *Devices) machineUsage(ctx context.Context, m *Machine, name string, f usageFilter, now time.Time) (MachineUsageSeries, error) {
	rg := usageRanges[name]
	id := m.cfg.ID
	from := now.Add(-rg.span).Truncate(time.Duration(rg.step) * time.Second).Add(time.Duration(rg.step) * time.Second)
	var rows []proto.UsageBucket
	st := d.usageStore()
	if st != nil {
		var err error
		if rows, err = st.UsageSeries(ctx, id, from, now.Add(time.Minute), rg.step); err != nil {
			return MachineUsageSeries{}, err
		}
	} else {
		rows = d.usage.buckets(id)
	}
	// Offline time: the steps without a monitoring sample, once the machine has sent any in the range.
	var present map[int64]bool
	if ms := d.metricsStore(); ms != nil {
		series, err := ms.MetricSeries(ctx, []string{id}, from, now.Add(time.Minute), rg.step)
		if err != nil {
			return MachineUsageSeries{}, err
		}
		if pts := series[id]; len(pts) > 0 {
			present = map[int64]bool{}
			for _, p := range pts {
				present[p.T] = true
			}
		}
	}
	out := MachineUsageSeries{MachineID: id, Online: m.snapshot().Online, Range: name, Step: rg.step, Stored: st != nil}
	out.Points, out.Total, out.Models, out.Launchers = usageSeries(rows, from, now, rg.step, f, present)
	out.LaunchersKnown = d.usage.knowsLaunchers(id)
	for _, l := range out.Launchers {
		out.LaunchersKnown = out.LaunchersKnown || l.Name != ""
	}
	return out, nil
}

// UnknownLauncher is the launcher filter's value for usage with no launcher (a session typed in a terminal, an
// older node): a launcher is a shell function's name, never "-", and the apps' clients drop empty query values.
const UnknownLauncher = "-"

// usageRoutes is GET /api/v1/machines/{id}/usage?range=1h|24h|7d[&model=…][&launcher=…|-].
func (d *Devices) usageRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/machines/{id}/usage", func(w http.ResponseWriter, r *http.Request) {
		m := d.machineByID(r.PathValue("id"))
		if m == nil {
			writeErr(w, http.StatusNotFound, errors.New("no such machine"))
			return
		}
		q := r.URL.Query()
		name := q.Get("range")
		if name == "" {
			name = "24h"
		}
		if _, ok := usageRanges[name]; !ok {
			writeErr(w, http.StatusBadRequest, errors.New("range is 1h, 24h or 7d"))
			return
		}
		var f usageFilter
		if q.Has("model") {
			v := q.Get("model")
			f.model = &v
		}
		if q.Has("launcher") {
			v := q.Get("launcher")
			if v == UnknownLauncher {
				v = ""
			}
			f.launcher = &v
		}
		out, err := d.machineUsage(r.Context(), m, name, f, time.Now())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, map[string]any{"usage": out})
	})
}

// UsageCard is a machine's tokens on its card in 机器 (GET /api/v1/metrics): today's sums, every token per minute
// now (over the last two to three minutes, as the island's TPM), and the last hour by minute (the sparkline).
type UsageCard struct {
	Today     UsageTokens `json:"today"`
	PerMinute int64       `json:"per_minute"`
	// Recent: every token of each of the last 60 minutes, oldest first; the last is the minute now.
	Recent []int64 `json:"recent"`
}

// card is a machine's UsageCard from memory; nil when its node never reported usage.
func (u *usageBook) card(machine string, now time.Time, loc *time.Location) *UsageCard {
	u.mu.Lock()
	defer u.mu.Unlock()
	m := u.machines[machine]
	if m == nil {
		return nil
	}
	y, mo, dd := now.In(loc).Date()
	today := time.Date(y, mo, dd, 0, 0, 0, 0, loc).Unix()
	minute := now.Unix() / 60 * 60
	from := minute - tpmSpan*60
	c := &UsageCard{Recent: make([]int64, 60)}
	var recent int64
	for k, b := range m.buckets {
		tokens := b.Input + b.CacheWrite + b.CacheRead + b.Output
		if i := 59 - int((minute-k.minute)/60); i >= 0 && i < 60 {
			c.Recent[i] += tokens
		}
		if k.minute >= from {
			recent += tokens
		}
		if k.minute >= today {
			c.Today.add(b)
		}
	}
	c.PerMinute = int64(float64(recent) / (float64(now.Unix()-from) / 60))
	return c
}

// buckets are a machine's buckets in memory (the last 26 hours).
func (u *usageBook) buckets(machine string) []proto.UsageBucket {
	u.mu.Lock()
	defer u.mu.Unlock()
	m := u.machines[machine]
	if m == nil {
		return nil
	}
	out := make([]proto.UsageBucket, 0, len(m.buckets))
	for _, b := range m.buckets {
		out = append(out, b)
	}
	return out
}

// knowsLaunchers: the machine's node said its buckets name their launcher.
func (u *usageBook) knowsLaunchers(machine string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	m := u.machines[machine]
	return m != nil && m.launchers
}
