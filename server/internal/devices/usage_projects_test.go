package devices

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/proto"
)

const appKey = "git:github.com/acme/app"

// Only the resolutions a node makes are kept: never a key or name that could carry a remote's credentials or a URL,
// a relative directory, or a resolution of a token.
func TestUsableDirs(t *testing.T) {
	good := []proto.UsageDir{
		{Dir: "~/code/app", Project: appKey, Name: "Acme/App", Host: "github.com", Place: "~/code/app"},
		{Dir: "/srv/work/infra", Project: "git:gitlab.example.com/Nimbus/Infra", Name: "Nimbus/Infra", Host: "gitlab.example.com"},
		{Dir: "~/scratch", Project: "local:" + strings.Repeat("ab", 20), Name: "scratch", Place: "~/scratch"},
		{Dir: "~/new", Project: "local:dir:new", Name: "new"},
		{Dir: "~/Documents/Trip", Project: "folder:trip", Name: "Trip", Place: "~/Documents/Trip"},
		{Dir: "~", Project: "home", Place: "~"},
		{Dir: "/tmp/x", Project: "temp"}, // a temporary folder in no repository made in it (#16827)
	}
	bad := []proto.UsageDir{
		{Dir: "~/a", Project: "git:me:secret@example.com/acme/app", Name: "acme/app", Host: "example.com"},
		{Dir: "~/a", Project: "git:me@example.com/acme/app", Name: "acme/app", Host: "example.com"},
		{Dir: "~/a", Project: "git:github.com:2222/acme/app", Name: "acme/app", Host: "github.com"},
		{Dir: "~/a", Project: "git:https://github.com/acme/app", Name: "acme/app", Host: "github.com"},
		{Dir: "~/a", Project: "git:github.com/acme/app?token=x", Name: "acme/app", Host: "github.com"},
		{Dir: "~/a", Project: appKey, Name: "acme/other", Host: "github.com"},    // the name is not the key's path
		{Dir: "~/a", Project: appKey, Name: "acme/app", Host: "gitlab.com"},      // nor the host its host
		{Dir: "code/app", Project: appKey, Name: "acme/app", Host: "github.com"}, // relative
		{Dir: "~/a\n", Project: "folder:a", Name: "a"},
		{Dir: "~/a", Project: "folder:a/b", Name: "b"},
		{Dir: "~/a", Project: "folder:", Name: ""},
		{Dir: "~/a", Project: "local:xyz", Name: "a"},
		{Dir: "~", Project: "home", Name: "me"},           // home is never named after the account
		{Dir: "/tmp/x", Project: "temp", Name: "x"},       // a temporary folder is never named after its folder
		{Dir: "/tmp/x", Project: "temp", Place: "/tmp/x"}, // nor has a place
		{Dir: proto.UsageDirTemp, Project: "temp"},        // the tokens are never resolved
		{Dir: proto.UsageDirAssistant, Project: "assistant"},
		{Dir: "~/a", Project: "folder:a", Name: "a", Place: "relative"},
	}
	if got := usableDirs(append(append([]proto.UsageDir{}, good...), bad...)); len(got) != len(good) {
		t.Fatalf("kept %d of %d good ones: %+v", len(got), len(good), got)
	}
	for _, b := range bad {
		if usableResolution(b) {
			t.Errorf("kept %+v", b)
		}
	}
}

// projectHub is a manager with no Postgres whose two machines reported usage by directory: one repository in three
// directories (a worktree too) on two machines, a folder, the home folder, an assistant conversation, a directory
// no node resolved, and yesterday's usage from before directories were kept.
func projectHub(t *testing.T) (*Devices, time.Time) {
	t.Helper()
	loc := time.FixedZone("test", 8*3600)
	d := New(Options{Hub: &fakeHost{}, Store: newMemStore(), Location: func() *time.Location { return loc }})
	now := time.Date(2026, 11, 17, 9, 30, 0, 0, loc)
	minute := now.Unix() / 60 * 60
	report := func(machine string, buckets []proto.UsageBucket, dirs []proto.UsageDir) {
		b, _ := json.Marshal(map[string]any{"full": true, "launchers": true, "buckets": buckets, "dirs": dirs})
		d.onUsage(machine, b, now)
	}
	report("a", []proto.UsageBucket{
		{Minute: minute - 60, Model: "opus", Launcher: "claude", Agent: "claude", Dir: "~/code/app", Input: 10, CacheWrite: 20, CacheRead: 300, Output: 40, Requests: 2},
		// The same minute, model, launcher and agent in another directory: a bucket of its own.
		{Minute: minute - 60, Model: "opus", Launcher: "claude", Agent: "claude", Dir: "~/code/app-wt/feature", CacheRead: 500, Output: 5, Requests: 1},
		{Minute: minute - 2*3600, Model: "sonnet", Launcher: "claude", Agent: "claude", Dir: "~/code/app-wt/feature", Output: 100, Requests: 1},
		{Minute: minute - 3*3600, Model: "opus", Agent: "claude", Dir: "~/Documents/Trip", Output: 50, Requests: 1},
		{Minute: minute - 4*3600, Model: "opus", Agent: "claude", Dir: proto.UsageDirAssistant, Output: 30, Requests: 1},
		{Minute: minute - 5*3600, Model: "opus", Agent: "claude", Dir: "~", Output: 7, Requests: 1},
		{Minute: minute - 6*3600, Model: "opus", Agent: "claude", Dir: "/tmp/gone", Output: 3, Requests: 1},
		{Minute: minute - 10*3600, Model: "opus", Agent: "claude", Output: 70, Requests: 1}, // 23:30 yesterday, not split
	}, []proto.UsageDir{
		{Dir: "~/code/app", Project: appKey, Name: "acme/app", Host: "github.com", Place: "~/code/app"},
		{Dir: "~/code/app-wt/feature", Project: appKey, Name: "acme/app", Host: "github.com", Place: "~/code/app-wt/feature", Worktree: true},
		{Dir: "~/Documents/Trip", Project: "folder:trip", Name: "Trip", Place: "~/Documents/Trip"},
		{Dir: "~", Project: "home", Place: "~"},
	})
	report("b", []proto.UsageBucket{
		{Minute: minute - 30*60, Model: "gpt", Launcher: "codex", Agent: "codex", Dir: "~/src/App", Input: 100, CacheRead: 900, Output: 1000, Requests: 3},
	}, []proto.UsageDir{{Dir: "~/src/App", Project: appKey, Name: "Acme/App", Host: "github.com", Place: "~/src/App"}})
	return d, now
}

func findProject(r devicewire.UsageProjectRange, key string) (devicewire.UsageProject, bool) {
	for _, p := range r.Projects {
		if p.Key == key {
			return p, true
		}
	}
	return devicewire.UsageProject{}, false
}

// One repository is one project across directories, worktrees, machines and the remote's case; each range's projects
// and its not-split part add up to the calendar's total for the same days.
func TestUsageProjectsAcrossMachines(t *testing.T) {
	d, now := projectHub(t)
	out, err := d.usageProjects(context.Background(), now)
	if err != nil || out.Stored || out.Zone != "test" || len(out.Ranges) != 3 {
		t.Fatalf("projects = %+v, %v", out, err)
	}
	cal, err := d.usageDays(context.Background(), 7, now)
	if err != nil {
		t.Fatal(err)
	}
	for i, days := range []int{1, 3, 7} {
		r := out.Ranges[i]
		var want, sum int64
		for _, day := range cal.Days[7-days:] {
			want += day.Total()
		}
		sum = tokensTotal(r.NotSplit)
		for _, p := range r.Projects {
			sum += tokensTotal(p.Tokens)
		}
		if r.Days != days || tokensTotal(r.Total) != want || sum != want {
			t.Fatalf("%d days: total %d, projects and not split %d, calendar %d", days, tokensTotal(r.Total), sum, want)
		}
	}
	today := out.Ranges[0]
	if tokensTotal(today.NotSplit) != 0 || tokensTotal(out.Ranges[1].NotSplit) != 70 {
		t.Fatalf("not split: today %+v, 3 days %+v", today.NotSplit, out.Ranges[1].NotSplit)
	}
	app, ok := findProject(today, appKey)
	if !ok || app.Kind != "repo" || app.Group != "development" || app.Machines != 2 || app.Host != "github.com" ||
		tokensTotal(app.Tokens) != 370+505+100+2000 || app.Tokens.Requests != 7 || today.Projects[0].Key != appKey {
		t.Fatalf("app = %+v (first %s)", app, today.Projects[0].Key)
	}
	if app.Name != "Acme/App" { // the name with the most tokens: machine b's
		t.Fatalf("name = %q", app.Name)
	}
	if want := float64(2975) / float64(tokensTotal(today.Total)); app.Share != want {
		t.Fatalf("share = %v, want %v", app.Share, want)
	}
	for key, want := range map[string][3]string{
		"folder:trip": {"folder", "other", "Trip"},
		"home":        {"home", "other", ""},
		"assistant":   {"assistant", "other", ""},
		"unknown":     {"unknown", "unknown", ""},
	} {
		p, ok := findProject(today, key)
		if !ok || string(p.Kind) != want[0] || string(p.Group) != want[1] || p.Name != want[2] || p.Machines != 1 {
			t.Errorf("%s = %+v, %v", key, p, ok)
		}
	}
}

// A project's breakdown: by machine, agent, model, launcher and place (a worktree marked), by day and today's hours,
// its share of everything and its cache hit rate.
func TestUsageProjectDetail(t *testing.T) {
	d, now := projectHub(t)
	out, err := d.usageProjectDetail(context.Background(), appKey, now)
	if err != nil || len(out.Ranges) != 3 || len(out.Days) != 7 {
		t.Fatalf("detail = %+v, %v", out, err)
	}
	if out.Project.Key != appKey || out.Project.Name != "Acme/App" || out.Project.Machines != 2 || tokensTotal(out.Project.Tokens) != 2975 {
		t.Fatalf("project = %+v", out.Project)
	}
	today := out.Ranges[0]
	if tokensTotal(today.Total) != 2975 || today.Days != 1 {
		t.Fatalf("today = %+v", today)
	}
	if want := float64(300+500+900) / float64(10+20+300+500+100+900); today.CacheHit != want {
		t.Fatalf("cache hit = %v, want %v", today.CacheHit, want)
	}
	if len(today.Machines) != 2 || today.Machines[0].Key != "b" || tokensTotal(today.Machines[0].Tokens) != 2000 {
		t.Fatalf("machines = %+v", today.Machines)
	}
	if len(today.Agents) != 2 || today.Agents[0].Key != "codex" || today.Agents[1].Key != "claude" {
		t.Fatalf("agents = %+v", today.Agents)
	}
	if len(today.Models) != 3 || len(today.Launchers) != 2 {
		t.Fatalf("models = %+v, launchers = %+v", today.Models, today.Launchers)
	}
	if len(today.Places) != 3 || today.Places[0].Place != "~/src/App" || string(today.Places[0].Machine) != "b" {
		t.Fatalf("places = %+v", today.Places)
	}
	for _, p := range today.Places {
		if p.Worktree != (p.Place == "~/code/app-wt/feature") {
			t.Errorf("place %+v", p)
		}
	}
	if tokensTotal(out.Days[6].Tokens) != 2975 || out.Days[6].Date != "2026-11-17" || tokensTotal(out.Days[5].Tokens) != 0 {
		t.Fatalf("days = %+v", out.Days)
	}
	if len(out.Hours) != 10 || tokensTotal(out.Hours[9].Tokens) != 2875 || tokensTotal(out.Hours[8].Tokens) != 0 ||
		tokensTotal(out.Hours[7].Tokens) != 100 { // 9:00 to now, and 7:30
		t.Fatalf("hours = %+v", out.Hours)
	}

	// The assistant's usage has no place to show; a key no directory resolves to is not a project.
	if a, err := d.usageProjectDetail(context.Background(), "assistant", now); err != nil || tokensTotal(a.Project.Tokens) != 30 || len(a.Ranges[0].Places) != 0 {
		t.Fatalf("assistant = %+v, %v", a, err)
	}
	if u, err := d.usageProjectDetail(context.Background(), "unknown", now); err != nil || tokensTotal(u.Project.Tokens) != 3 {
		t.Fatalf("unknown = %+v, %v", u.Project, err)
	}
	if _, err := d.usageProjectDetail(context.Background(), "git:github.com/nobody/x", now); err != errNoProject {
		t.Fatalf("err = %v", err)
	}
}

// An older node sends no directories: all of its usage is not split, and the routes still answer.
func TestUsageProjectsOlderNode(t *testing.T) {
	loc := time.UTC
	d := New(Options{Hub: &fakeHost{}, Store: newMemStore(), Location: func() *time.Location { return loc }})
	now := time.Date(2026, 11, 17, 9, 30, 0, 0, loc)
	b, _ := json.Marshal(map[string]any{"full": true, "launchers": true, "buckets": []proto.UsageBucket{
		{Minute: now.Unix()/60*60 - 60, Model: "opus", Agent: "claude", Output: 9, Requests: 1}}})
	d.onUsage("old", b, now)
	out, err := d.usageProjects(context.Background(), now)
	if err != nil || len(out.Ranges[0].Projects) != 0 || tokensTotal(out.Ranges[0].NotSplit) != 9 || tokensTotal(out.Ranges[0].Total) != 9 {
		t.Fatalf("projects = %+v, %v", out, err)
	}
}

// The routes answer as the other usage routes do, and a missing key is refused.
func TestUsageProjectRoutes(t *testing.T) {
	d, _ := projectHub(t)
	mux := http.NewServeMux()
	d.usageProjectRoutes(mux)
	get := func(path string) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
		return w
	}
	w := get("/api/v1/usage/projects")
	var list struct {
		Usage devicewire.UsageProjects `json:"usage"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil || len(list.Usage.Ranges) != 3 {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if w := get("/api/v1/usage/projects/detail"); w.Code != http.StatusBadRequest {
		t.Fatalf("no key: %d", w.Code)
	}
	if w := get("/api/v1/usage/projects/detail?key=folder%3Anothing"); w.Code != http.StatusNotFound {
		t.Fatalf("no such project: %d", w.Code)
	}
	if w := get("/api/v1/usage/projects/detail?key=assistant"); w.Code != 200 || !strings.Contains(w.Body.String(), `"project"`) {
		t.Fatalf("assistant: %d %s", w.Code, w.Body)
	}
}

// usageDirsStore records what the manager stores of a node's notification.
type usageDirsStore struct {
	*usageMemStore
	got chan db.UsageWrite
}

func (s *usageDirsStore) PutUsages(_ context.Context, writes []db.UsageWrite) ([]error, error) {
	for _, w := range writes {
		s.got <- w
	}
	return nil, nil
}

// The resolutions a node sends are stored with its buckets (a refused one left out), and a node asks whether this
// manager keeps directories at all before it splits its buckets by them.
func TestUsageDirsAreStored(t *testing.T) {
	st := &usageDirsStore{usageMemStore: &usageMemStore{memStore: newMemStore(), done: make(chan struct{}, 8)}, got: make(chan db.UsageWrite, 4)}
	d := New(Options{Hub: &fakeHost{}, Store: st})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.usageLoop(ctx)
	now := time.Now()
	b, _ := json.Marshal(map[string]any{"launchers": true,
		"buckets": []proto.UsageBucket{{Minute: now.Unix() / 60 * 60, Model: "opus", Agent: "claude", Dir: "~/code/app", Output: 1, Requests: 1}},
		"dirs": []proto.UsageDir{
			{Dir: "~/code/app", Project: appKey, Name: "acme/app", Host: "github.com", Place: "~/code/app"},
			{Dir: "~/x", Project: "git:me:pw@example.com/acme/app", Name: "acme/app", Host: "github.com"},
		}})
	d.onUsage("a", b, now)
	select {
	case w := <-st.got:
		if w.Machine != "a" || len(w.Dirs) != 1 || w.Dirs[0].Project != appKey || len(w.Buckets) != 1 || w.Buckets[0].Dir != "~/code/app" {
			t.Fatalf("stored %+v", w)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing stored")
	}

	m := &Machine{d: d}
	out, err := m.onRequest(context.Background(), "usage.features", json.RawMessage(`{}`))
	if raw, _ := json.Marshal(out); err != nil || string(raw) != `{"dirs":true,"temp_dirs":true}` {
		t.Fatalf("usage.features = %s, %v", raw, err)
	}
}

// usageDirsPostgres stands in for Postgres's resolutions and sums (usageProjectStore): what an earlier run stored.
type usageDirsPostgres struct {
	*memStore
	dirs  []db.UsageDirRow
	spans []db.UsageDirSpan
}

func (s *usageDirsPostgres) UsageDirs(context.Context) ([]db.UsageDirRow, error) { return s.dirs, nil }
func (s *usageDirsPostgres) UsageDirSpans(_ context.Context, _ []time.Time, _ bool, dirs []string) ([]db.UsageDirSpan, error) {
	var out []db.UsageDirSpan
	for _, sp := range s.spans {
		if dirs == nil || slices.Contains(dirs, sp.Dir) {
			out = append(out, sp)
		}
	}
	return out, nil
}

// A temporary folder is the project of a repository made in it (a worktree in /tmp), and otherwise the one
// "Temporary folders" with an older node's :temp: never split by its folders' names, never a place. A node's temp
// for a directory never replaces the repository it was before, so a worktree removed since keeps its project, in
// memory and over what Postgres kept (#16827).
func TestUsageProjectsTempFolders(t *testing.T) {
	loc := time.UTC
	now := time.Date(2026, 11, 17, 9, 30, 0, 0, loc)
	minute := now.Unix() / 60 * 60
	wt := proto.UsageDir{Dir: "/tmp/app-lane-x", Project: appKey, Name: "acme/app", Host: "github.com", Place: "/tmp/app-lane-x", Worktree: true}
	bucket := func(dir string, out int64) proto.UsageBucket {
		return proto.UsageBucket{Minute: minute - 60, Model: "opus", Launcher: "claude", Agent: "claude", Dir: dir, Output: out, Requests: 1}
	}
	check := func(t *testing.T, d *Devices) {
		t.Helper()
		out, err := d.usageProjects(context.Background(), now)
		if err != nil {
			t.Fatal(err)
		}
		today := out.Ranges[0]
		app, okApp := findProject(today, appKey)
		temp, okTemp := findProject(today, "temp")
		if len(today.Projects) != 2 || !okApp || !okTemp || tokensTotal(app.Tokens) != 110 || tokensTotal(temp.Tokens) != 15 ||
			temp.Kind != devicewire.UsageProjectTemp || temp.Group != devicewire.UsageGroupOther || temp.Name != "" || temp.Machines != 2 {
			t.Fatalf("projects %+v", today.Projects)
		}
		td, err := d.usageProjectDetail(context.Background(), "temp", now)
		if err != nil || tokensTotal(td.Project.Tokens) != 15 || tokensTotal(td.Ranges[0].Total) != 15 || len(td.Ranges[0].Places) != 0 ||
			td.Project.Name != "" || len(td.Ranges[0].Machines) != 2 {
			t.Fatalf("temp detail %+v, %v", td, err)
		}
		ad, err := d.usageProjectDetail(context.Background(), appKey, now)
		if err != nil || tokensTotal(ad.Project.Tokens) != 110 || len(ad.Ranges[0].Places) != 2 {
			t.Fatalf("app detail %+v, %v", ad, err)
		}
		for _, p := range ad.Ranges[0].Places {
			if p.Worktree != (p.Place == "/tmp/app-lane-x") {
				t.Errorf("place %+v", p)
			}
		}
	}
	newer := []proto.UsageBucket{bucket("/tmp/app-lane-x", 100), bucket("/tmp/scratch-1", 7), bucket("/tmp/scratch-2", 3), bucket("~/code/app", 10)}
	gone := proto.UsageDir{Dir: "/tmp/app-lane-x", Project: "temp"}

	t.Run("memory", func(t *testing.T) {
		d := New(Options{Hub: &fakeHost{}, Store: newMemStore(), Location: func() *time.Location { return loc }})
		report := func(machine string, full bool, buckets []proto.UsageBucket, dirs []proto.UsageDir) {
			b, _ := json.Marshal(map[string]any{"full": full, "launchers": true, "buckets": buckets, "dirs": dirs})
			d.onUsage(machine, b, now)
		}
		report("old", true, []proto.UsageBucket{bucket(proto.UsageDirTemp, 5)}, nil) // an older node
		report("new", true, newer, []proto.UsageDir{wt, {Dir: "/tmp/scratch-1", Project: "temp"}, {Dir: "/tmp/scratch-2", Project: "temp"},
			{Dir: "~/code/app", Project: appKey, Name: "acme/app", Host: "github.com", Place: "~/code/app"}})
		report("new", false, nil, []proto.UsageDir{gone}) // the worktree removed: its node says temp now
		check(t, d)
	})
	t.Run("over Postgres", func(t *testing.T) {
		st := &usageDirsPostgres{memStore: newMemStore(), dirs: []db.UsageDirRow{
			{Machine: "new", UsageDir: wt},
			{Machine: "new", UsageDir: proto.UsageDir{Dir: "/tmp/scratch-1", Project: "temp"}},
			{Machine: "new", UsageDir: proto.UsageDir{Dir: "/tmp/scratch-2", Project: "temp"}},
			{Machine: "new", UsageDir: proto.UsageDir{Dir: "~/code/app", Project: appKey, Name: "acme/app", Host: "github.com", Place: "~/code/app"}},
		}}
		for _, b := range append(newer, bucket(proto.UsageDirTemp, 5)) {
			machine := "new"
			if b.Dir == proto.UsageDirTemp {
				machine = "old"
			}
			st.spans = append(st.spans, db.UsageDirSpan{Span: 6, Machine: machine, Dir: b.Dir, Model: "opus", Agent: "claude", Launcher: "claude", Output: b.Output, Requests: 1})
		}
		d := New(Options{Hub: &fakeHost{}, Store: st, Location: func() *time.Location { return loc }})
		b, _ := json.Marshal(map[string]any{"dirs": []proto.UsageDir{gone}}) // this run's memory: temp
		d.onUsage("new", b, now)
		check(t, d)
	})
}
