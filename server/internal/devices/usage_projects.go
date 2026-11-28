package devices

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/wireids"
)

// Token usage by project (docs/server/machines.md "Token usage by project", #16530). Each node says where each bucket's
// session started (proto.UsageBucket.Dir) and what it resolved that directory to (proto.UsageDir: a git remote's host
// and path, a repository's root commit, a folder's name); the manager keeps the latest resolution of each machine's
// directories (usage_dirs, and in memory) and sums every machine's usage by project, so one repository in several
// directories, worktrees and computers is one project. Rows with no directory are "not split": stored before, or
// from an older node. The days are the calendar's (usage_history.go), so a range's projects and its not-split part
// add up to the calendar's total for it.

// usageDirStore keeps resolutions that come without live buckets (a backfill's).
type usageDirStore interface {
	PutUsageDirs(ctx context.Context, machine string, dirs []proto.UsageDir) error
}

// usageProjectStore is the Postgres side: the resolutions and the sums by directory.
type usageProjectStore interface {
	UsageDirs(ctx context.Context) ([]db.UsageDirRow, error)
	UsageDirSpans(ctx context.Context, bounds []time.Time, detail bool, dirs []string) ([]db.UsageDirSpan, error)
}

const (
	usageProjectKeyMost = 512  // a project key or name, in bytes
	usageDirMost        = 1024 // a directory, in bytes
)

const usageProjectUnknown = devicewire.UsageProjectUnknown

var (
	// gitProjectKey: "git:<host>/<path>" as a node writes it: a host in lower case with no user and no port, path
	// segments of the characters a forge allows. No '@' or ':' after "git:", so no credentials can be in one.
	gitProjectKey = lazyre.New(`^git:([a-z0-9]([a-z0-9.-]*[a-z0-9])?)((/[A-Za-z0-9._~+-]+)+)$`)
	// localProjectKey: a repository's root commit (SHA-1 or SHA-256).
	localProjectKey = lazyre.New(`^local:[0-9a-f]{40}([0-9a-f]{24})?$`)
)

// plainText: no control characters, valid UTF-8, at most most bytes, and not empty when needed.
func plainText(s string, most int, needed bool) bool {
	if len(s) > most || needed && s == "" || !utf8.ValidString(s) {
		return false
	}
	return !strings.ContainsFunc(s, unicode.IsControl)
}

// usableDir: a directory as a node writes one (proto.UsageBucket.Dir): written from home, absolute, or a token.
func usableDir(dir string) bool {
	if !plainText(dir, usageDirMost, true) {
		return false
	}
	switch dir {
	case proto.UsageDirAssistant, proto.UsageDirTemp, proto.UsageDirUnknown:
		return true
	}
	return dir == "~" || strings.HasPrefix(dir, "~/") || strings.HasPrefix(dir, "/")
}

// usableDirs are the resolutions in a node's "dirs" the manager keeps: only the shapes a node makes (proto.UsageDir),
// so a node's bug can never store a remote's credentials, a URL or a malformed key. The rest are left out.
func usableDirs(in []proto.UsageDir) []proto.UsageDir {
	var out []proto.UsageDir
	for _, x := range in {
		if usableResolution(x) {
			out = append(out, x)
		}
	}
	return out
}

func usableResolution(x proto.UsageDir) bool {
	if !usableDir(x.Dir) || strings.HasPrefix(x.Dir, ":") || !plainText(x.Name, usageProjectKeyMost, false) ||
		!plainText(x.Project, usageProjectKeyMost, true) || x.Place != "" && !usableDir(x.Place) {
		return false
	}
	switch kind := projectKind(x.Project); kind {
	case devicewire.UsageProjectRepo:
		m := gitProjectKey.FindStringSubmatch(x.Project)
		// Its host is the key's, and its name the key's path in the remote's own case.
		return m != nil && x.Host == m[1] && strings.EqualFold("/"+x.Name, m[3])
	case devicewire.UsageProjectLocal:
		rest := strings.TrimPrefix(x.Project, "local:dir:")
		return x.Host == "" && (localProjectKey.MatchString(x.Project) || rest != x.Project && rest != "" && !strings.Contains(rest, "/"))
	case devicewire.UsageProjectFolder:
		rest := strings.TrimPrefix(x.Project, "folder:")
		return x.Host == "" && rest != "" && !strings.Contains(rest, "/")
	case devicewire.UsageProjectHome:
		return x.Host == "" && x.Name == ""
	case devicewire.UsageProjectTemp: // a temporary folder in no repository made inside it (#16827): never its name
		return x.Host == "" && x.Name == "" && x.Place == "" && !x.Worktree
	default:
		return false // assistant is a token (proto.UsageDirAssistant), never a resolution
	}
}

// replacesResolution: next, a node's latest resolution of a directory, takes the place of old, the one kept. A
// temporary folder's "temp" never replaces another (#16827): a node says it for a folder that is gone too, and a
// worktree removed since keeps the repository it was. db's putUsageDirSQL keeps the same rule.
func replacesResolution(old, next proto.UsageDir) bool {
	return projectKind(next.Project) != devicewire.UsageProjectTemp || projectKind(old.Project) == devicewire.UsageProjectTemp
}

// projectKind is a project key's kind (devicewire.UsageProject*).
func projectKind(key string) devicewire.UsageProjectKind {
	switch {
	case strings.HasPrefix(key, "git:"):
		return devicewire.UsageProjectRepo
	case strings.HasPrefix(key, "local:"):
		return devicewire.UsageProjectLocal
	case strings.HasPrefix(key, "folder:"):
		return devicewire.UsageProjectFolder
	case key == string(devicewire.UsageProjectHome), key == string(devicewire.UsageProjectTemp), key == string(devicewire.UsageProjectAssistant):
		return devicewire.UsageProjectKind(key)
	}
	return usageProjectUnknown
}

// projectGroup is the group the apps show a kind in.
func projectGroup(kind devicewire.UsageProjectKind) devicewire.UsageProjectGroup {
	switch kind {
	case devicewire.UsageProjectRepo, devicewire.UsageProjectLocal:
		return devicewire.UsageGroupDevelopment
	case usageProjectUnknown:
		return devicewire.UsageGroupUnknown
	}
	return devicewire.UsageGroupOther
}

// usageDirMap is what every machine's directories resolved to: machine → dir → resolution.
type usageDirMap map[string]map[string]proto.UsageDir

// projectOf is the project of a machine's directory: "" not split (no directory), a token's own, its resolution's, or
// unknown when no node resolved it. A temporary folder resolved to temp is the token's project, with no resolution
// (ok false): the one "Temporary folders", never a folder's name or place.
func (m usageDirMap) projectOf(machine, dir string) (key string, res proto.UsageDir, ok bool) {
	switch dir {
	case "":
		return "", proto.UsageDir{}, false
	case proto.UsageDirAssistant:
		return string(devicewire.UsageProjectAssistant), proto.UsageDir{}, false
	case proto.UsageDirTemp:
		return string(devicewire.UsageProjectTemp), proto.UsageDir{}, false
	case proto.UsageDirUnknown:
		return string(usageProjectUnknown), proto.UsageDir{}, false
	}
	if r, ok := m[machine][dir]; ok {
		if projectKind(r.Project) == devicewire.UsageProjectTemp {
			return r.Project, proto.UsageDir{}, false
		}
		return r.Project, r, true
	}
	return string(usageProjectUnknown), proto.UsageDir{}, false
}

// dirsOf are the directories, on any machine, whose project is key (a superset: the sums are filtered again by
// machine); nil for the projects a directory cannot name (unknown: every directory no node resolved). Temp's are an
// older node's token and every temporary folder resolved to it.
func (m usageDirMap) dirsOf(key string) []string {
	seen := map[string]bool{}
	out := []string{}
	switch devicewire.UsageProjectKind(key) {
	case devicewire.UsageProjectAssistant:
		return []string{proto.UsageDirAssistant}
	case devicewire.UsageProjectTemp:
		seen[proto.UsageDirTemp] = true
		out = append(out, proto.UsageDirTemp)
	case usageProjectUnknown:
		return nil
	}
	for _, dirs := range m {
		for dir, r := range dirs {
			if r.Project == key && !seen[dir] {
				seen[dir] = true
				out = append(out, dir)
			}
		}
	}
	sort.Strings(out)
	return out
}

// dirMap is every machine's resolutions: Postgres's, then memory's over them (memory has what came since the last
// write).
func (d *Devices) dirMap(ctx context.Context) (usageDirMap, error) {
	out := usageDirMap{}
	put := func(machine string, r proto.UsageDir) {
		if out[machine] == nil {
			out[machine] = map[string]proto.UsageDir{}
		}
		if old, ok := out[machine][r.Dir]; !ok || replacesResolution(old, r) {
			out[machine][r.Dir] = r
		}
	}
	if st, ok := d.opts.Store.(usageProjectStore); ok {
		rows, err := st.UsageDirs(ctx)
		if err != nil {
			return nil, err
		}
		for _, r := range rows {
			put(r.Machine, r.UsageDir)
		}
	}
	d.usage.mu.Lock()
	for id, m := range d.usage.machines {
		for _, r := range m.dirs {
			put(id, r)
		}
	}
	d.usage.mu.Unlock()
	return out, nil
}

// dirSpans sums the usage into the spans between bounds by machine and directory (and with detail by agent, model and
// launcher): from Postgres, or from memory (the last 26 hours) without it. dirs nil keeps every directory.
func (d *Devices) dirSpans(ctx context.Context, bounds []time.Time, detail bool, dirs []string) ([]db.UsageDirSpan, bool, error) {
	if st, ok := d.opts.Store.(usageProjectStore); ok {
		spans, err := st.UsageDirSpans(ctx, bounds, detail, dirs)
		return spans, true, err
	}
	return d.usage.dirSpans(bounds, detail, dirs), false, nil
}

// dirSpans is UsageDirSpans over the buckets in memory.
func (u *usageBook) dirSpans(bounds []time.Time, detail bool, dirs []string) []db.UsageDirSpan {
	if len(bounds) < 2 {
		return nil
	}
	var keep map[string]bool
	if dirs != nil {
		keep = map[string]bool{}
		for _, dir := range dirs {
			keep[dir] = true
		}
	}
	type key struct {
		span                                 int
		machine, dir, agent, model, launcher string
	}
	sums := map[key]*db.UsageDirSpan{}
	u.mu.Lock()
	for id, m := range u.machines {
		for k, b := range m.buckets {
			t := time.Unix(k.minute, 0)
			if t.Before(bounds[0]) || !t.Before(bounds[len(bounds)-1]) || keep != nil && !keep[k.dir] {
				continue
			}
			kk := key{span: sort.Search(len(bounds), func(i int) bool { return bounds[i].After(t) }) - 1, machine: id, dir: k.dir}
			if detail {
				kk.agent, kk.model, kk.launcher = k.agent, k.model, k.launcher
			}
			s := sums[kk]
			if s == nil {
				s = &db.UsageDirSpan{Span: kk.span, Machine: id, Dir: k.dir, Agent: kk.agent, Model: kk.model, Launcher: kk.launcher}
				sums[kk] = s
			}
			s.Input += b.Input
			s.CacheWrite += b.CacheWrite
			s.CacheRead += b.CacheRead
			s.Output += b.Output
			s.Requests += b.Requests
		}
	}
	u.mu.Unlock()
	out := make([]db.UsageDirSpan, 0, len(sums))
	for _, s := range sums {
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b db.UsageDirSpan) int {
		return cmp.Or(cmp.Compare(a.Span, b.Span), cmp.Compare(a.Machine, b.Machine), cmp.Compare(a.Dir, b.Dir),
			cmp.Compare(a.Agent, b.Agent), cmp.Compare(a.Model, b.Model), cmp.Compare(a.Launcher, b.Launcher))
	})
	return out
}

func dirSpanTokens(s db.UsageDirSpan) devicewire.UsageTokens {
	return devicewire.UsageTokens{Input: s.Input, CacheWrite: s.CacheWrite, CacheRead: s.CacheRead, Output: s.Output, Requests: s.Requests}
}

func addTokens(t *devicewire.UsageTokens, o devicewire.UsageTokens) {
	t.Input += o.Input
	t.CacheWrite += o.CacheWrite
	t.CacheRead += o.CacheRead
	t.Output += o.Output
	t.Requests += o.Requests
}

func tokensTotal(t devicewire.UsageTokens) int64 {
	return t.Input + t.CacheWrite + t.CacheRead + t.Output
}

// share is part over whole, 0 when whole is.
func share(part, whole int64) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) / float64(whole)
}

// cacheHit is cache reads over all input (docs/server/machines.md "Token usage").
func cacheHit(t devicewire.UsageTokens) float64 {
	return share(t.CacheRead, t.Input+t.CacheWrite+t.CacheRead)
}

// inRange: span (of the last 7 days' day bounds) is in the range of days ending today.
func inRange(span, days int) bool {
	return span >= usageProjectDays-days && span < usageProjectDays
}

// usageProjectDays is how many days the routes cover: the longest range.
const usageProjectDays = 7

// projectSum is one project's sums over a range while they are added up.
type projectSum struct {
	key      string
	tokens   devicewire.UsageTokens
	machines map[string]bool
	// names: the tokens behind each name and host a resolution of the key gives (the remote's own case may differ
	// between machines): the project takes the largest.
	names map[[2]string]int64
}

func (p *projectSum) add(machine string, res proto.UsageDir, ok bool, t devicewire.UsageTokens) {
	addTokens(&p.tokens, t)
	if p.machines == nil {
		p.machines, p.names = map[string]bool{}, map[[2]string]int64{}
	}
	p.machines[machine] = true
	if ok {
		p.names[[2]string{res.Name, res.Host}] += tokensTotal(t)
	}
}

// project is the sums as the apps get them, share of whole.
func (p *projectSum) project(whole int64) devicewire.UsageProject {
	kind := projectKind(p.key)
	out := devicewire.UsageProject{Key: p.key, Kind: kind, Group: projectGroup(kind), Machines: len(p.machines),
		Share: share(tokensTotal(p.tokens), whole), Tokens: p.tokens}
	var best int64 = -1
	for nh, n := range p.names {
		if n > best || n == best && (nh[0] < out.Name || nh[0] == out.Name && nh[1] < out.Host) {
			best, out.Name, out.Host = n, nh[0], nh[1]
		}
	}
	switch kind { // the apps name these: never a folder's name, which would show the account's
	case devicewire.UsageProjectHome, devicewire.UsageProjectTemp, devicewire.UsageProjectAssistant, usageProjectUnknown:
		out.Name, out.Host = "", ""
	}
	return out
}

// usageProjects is GET /api/v1/usage/projects.
func (d *Devices) usageProjects(ctx context.Context, now time.Time) (devicewire.UsageProjects, error) {
	loc := d.location()
	bounds := usageDayBounds(now, loc, usageProjectDays)
	dirs, err := d.dirMap(ctx)
	if err != nil {
		return devicewire.UsageProjects{}, err
	}
	spans, stored, err := d.dirSpans(ctx, bounds, false, nil)
	if err != nil {
		return devicewire.UsageProjects{}, err
	}
	out := devicewire.UsageProjects{Zone: loc.String(), Stored: stored}
	for _, days := range devicewire.UsageProjectRanges {
		r := devicewire.UsageProjectRange{Days: days, Projects: []devicewire.UsageProject{}}
		sums := map[string]*projectSum{}
		for _, s := range spans {
			if !inRange(s.Span, days) {
				continue
			}
			t := dirSpanTokens(s)
			addTokens(&r.Total, t)
			key, res, ok := dirs.projectOf(s.Machine, s.Dir)
			if key == "" {
				addTokens(&r.NotSplit, t)
				continue
			}
			p := sums[key]
			if p == nil {
				p = &projectSum{key: key}
				sums[key] = p
			}
			p.add(s.Machine, res, ok, t)
		}
		for _, p := range sums {
			if tokensTotal(p.tokens) > 0 {
				r.Projects = append(r.Projects, p.project(tokensTotal(r.Total)))
			}
		}
		slices.SortFunc(r.Projects, func(a, b devicewire.UsageProject) int {
			return cmp.Or(cmp.Compare(tokensTotal(b.Tokens), tokensTotal(a.Tokens)), cmp.Compare(a.Key, b.Key))
		})
		out.Ranges = append(out.Ranges, r)
	}
	return out, nil
}

// errNoProject: no machine's directory resolves to the key, and it is none of the projects a token names.
var errNoProject = errors.New("no such project")

// partSums adds up the parts of a breakdown by key.
type partSums map[string]*devicewire.UsagePart

func (p partSums) add(key string, t devicewire.UsageTokens) {
	s := p[key]
	if s == nil {
		s = &devicewire.UsagePart{Key: key}
		p[key] = s
	}
	addTokens(&s.Tokens, t)
}

func (p partSums) sorted(name func(string) string) []devicewire.UsagePart {
	out := make([]devicewire.UsagePart, 0, len(p))
	for _, s := range p {
		if tokensTotal(s.Tokens) == 0 {
			continue
		}
		if name != nil {
			s.Name = name(s.Key)
		}
		out = append(out, *s)
	}
	slices.SortFunc(out, func(a, b devicewire.UsagePart) int {
		return cmp.Or(cmp.Compare(tokensTotal(b.Tokens), tokensTotal(a.Tokens)), cmp.Compare(a.Key, b.Key))
	})
	return out
}

// usageProjectDetail is GET /api/v1/usage/projects/detail?key=.
func (d *Devices) usageProjectDetail(ctx context.Context, key string, now time.Time) (devicewire.UsageProjectDetail, error) {
	loc := d.location()
	dirs, err := d.dirMap(ctx)
	if err != nil {
		return devicewire.UsageProjectDetail{}, err
	}
	only := dirs.dirsOf(key)
	if only != nil && len(only) == 0 {
		return devicewire.UsageProjectDetail{}, errNoProject
	}
	days := usageDayBounds(now, loc, usageProjectDays)
	spans, stored, err := d.dirSpans(ctx, days, true, only)
	if err != nil {
		return devicewire.UsageProjectDetail{}, err
	}
	// Every project's usage by day, for the shares: the calendar's sums.
	all, _, err := d.usageSpans(ctx, days, db.UsageSum)
	if err != nil {
		return devicewire.UsageProjectDetail{}, err
	}
	hourBounds := usageHourBounds(now, loc, 1)
	hourSpans, _, err := d.dirSpans(ctx, hourBounds, false, only)
	if err != nil {
		return devicewire.UsageProjectDetail{}, err
	}
	out := devicewire.UsageProjectDetail{Zone: loc.String(), Stored: stored, Days: make([]devicewire.UsageProjectDay, usageProjectDays)}
	for i := range out.Days {
		out.Days[i].Date = days[i].Format(time.DateOnly)
	}
	mine := func(machine, dir string) (proto.UsageDir, bool, bool) {
		k, res, ok := dirs.projectOf(machine, dir)
		return res, ok, k == key
	}
	machineName := func(id string) string {
		if m := d.machineByID(id); m != nil {
			return m.cfg.Name
		}
		return ""
	}
	header := &projectSum{key: key}
	for _, s := range spans {
		res, ok, in := mine(s.Machine, s.Dir)
		if !in || s.Span < 0 || s.Span >= usageProjectDays {
			continue
		}
		addTokens(&out.Days[s.Span].Tokens, dirSpanTokens(s))
		header.add(s.Machine, res, ok, dirSpanTokens(s))
	}
	if len(header.names) == 0 { // nothing in the week: named after its resolutions all the same
		header.names = map[[2]string]int64{}
		for _, m := range dirs {
			for _, r := range m {
				if r.Project == key {
					header.names[[2]string{r.Name, r.Host}] += 0
				}
			}
		}
	}
	for _, n := range devicewire.UsageProjectRanges {
		var whole int64
		for _, s := range all {
			if inRange(s.Span, n) {
				whole += tokensTotal(spanUsageTokens(s))
			}
		}
		b := devicewire.UsageProjectBreakdown{Days: n}
		machines, agents, models, launchers := partSums{}, partSums{}, partSums{}, partSums{}
		type placeKey struct {
			machine, place string
			worktree       bool
		}
		places := map[placeKey]*devicewire.UsagePlace{}
		for _, s := range spans {
			res, ok, in := mine(s.Machine, s.Dir)
			if !in || !inRange(s.Span, n) {
				continue
			}
			t := dirSpanTokens(s)
			addTokens(&b.Total, t)
			machines.add(s.Machine, t)
			agents.add(s.Agent, t)
			models.add(s.Model, t)
			launchers.add(s.Launcher, t)
			if !ok { // a token's usage (assistant, temp, unknown) has no place to show
				continue
			}
			pk := placeKey{s.Machine, cmp.Or(res.Place, res.Dir), res.Worktree}
			p := places[pk]
			if p == nil {
				p = &devicewire.UsagePlace{Machine: wireids.MachineID(s.Machine), Place: pk.place, Worktree: pk.worktree}
				places[pk] = p
			}
			addTokens(&p.Tokens, t)
		}
		b.Share, b.CacheHit = share(tokensTotal(b.Total), whole), cacheHit(b.Total)
		b.Machines, b.Agents, b.Models, b.Launchers = machines.sorted(machineName), agents.sorted(nil), models.sorted(nil), launchers.sorted(nil)
		b.Places = []devicewire.UsagePlace{}
		for _, p := range places {
			if tokensTotal(p.Tokens) > 0 {
				p.MachineName = machineName(string(p.Machine))
				b.Places = append(b.Places, *p)
			}
		}
		slices.SortFunc(b.Places, func(a, c devicewire.UsagePlace) int {
			return cmp.Or(cmp.Compare(tokensTotal(c.Tokens), tokensTotal(a.Tokens)), cmp.Compare(a.Machine, c.Machine), cmp.Compare(a.Place, c.Place))
		})
		out.Ranges = append(out.Ranges, b)
		if n == usageProjectDays {
			out.Project = header.project(whole)
		}
	}
	out.Hours = make([]devicewire.UsageProjectHour, len(hourBounds)-1)
	for i := range out.Hours {
		t := hourBounds[i].In(loc)
		out.Hours[i] = devicewire.UsageProjectHour{T: t.Unix(), Hour: t.Hour()}
	}
	for _, s := range hourSpans {
		if _, _, in := mine(s.Machine, s.Dir); in && s.Span >= 0 && s.Span < len(out.Hours) {
			addTokens(&out.Hours[s.Span].Tokens, dirSpanTokens(s))
		}
	}
	return out, nil
}

// spanUsageTokens is a calendar span's tokens as devicewire has them.
func spanUsageTokens(s db.UsageSpan) devicewire.UsageTokens {
	return devicewire.UsageTokens{Input: s.Input, CacheWrite: s.CacheWrite, CacheRead: s.CacheRead, Output: s.Output, Requests: s.Requests}
}

// usageProjectRoutes are GET /api/v1/usage/projects and GET /api/v1/usage/projects/detail?key=.
func (d *Devices) usageProjectRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/v1/usage/projects", func(w http.ResponseWriter, r *http.Request) {
		out, err := d.usageProjects(r.Context(), time.Now())
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, devicewire.UsageProjectsReply{Usage: out})
	})
	mux.HandleFunc("GET /api/v1/usage/projects/detail", func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		if !plainText(key, usageProjectKeyMost, true) {
			writeErr(w, http.StatusBadRequest, errors.New("key is a project's key"))
			return
		}
		out, err := d.usageProjectDetail(r.Context(), key, time.Now())
		if errors.Is(err, errNoProject) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, devicewire.UsageProjectDetailReply{Usage: out})
	})
}
