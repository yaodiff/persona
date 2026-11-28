package hub

import (
	"cmp"
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/sessionlist"
)

// The catalog of a paged runtime list (catalog=1, featureSessionsCatalog): the projects and the spaces its sessions
// are in, counted over the same filtered rows as counts=1. A project is a repository wherever it is checked out: the
// directories whose computers name the same network origin are one project, each listed as a place. A directory
// without one (no repository, no remote, a computer that does not say) goes by its folder's name. A space is one
// computer's, named as its rows name it, in the folder most of them are in. Reading it changes nothing a page, a count or
// a push is built from.

type sessionCatalog struct {
	Revision string `json:"revision"`
	Total    int    `json:"total"`
	// Pending: some directories' computers were still being asked what repository they hold when this was written.
	// Those directories go by their folder's name here; a read a moment later has them by their repository.
	Pending  bool             `json:"pending,omitempty"`
	Projects []catalogProject `json:"projects"`
	Spaces   []catalogSpace   `json:"spaces"`
}

// catalogProject is one project. ID is "git:<origin>" for a repository with a network remote, its origin lowercased
// (hosts and most forges ignore case), and "dir:<folder name>" lowercased otherwise; Origin is the remote as the
// computer spelled it, without credentials. Places are its directories, the ones a place= filter takes. The rows in
// no directory are the last project, with an empty ID and name, a place with an empty path for each of their computers.
type catalogProject struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Origin string `json:"origin,omitempty"`
	Total  int    `json:"total"`
	// NeedsYou: how many of them need the user, by the state the list filters by (the folded one with roots=1).
	NeedsYou int            `json:"needs_you,omitempty"`
	Places   []catalogPlace `json:"places"`
}

// catalogPlace is one directory of a project: Path is the rows' project attribute there (repo_root, else
// workspace_cwd, else cwd), which with MachineID is what the project filter and place= select by.
type catalogPlace struct {
	MachineID string `json:"machine_id"`
	Path      string `json:"path"`
	Total     int    `json:"total"`
	NeedsYou  int    `json:"needs_you,omitempty"`
}

// catalogSpace is one computer's space: ID is the rows' group attribute there (space_id, else workspace_id), which
// with MachineID is what space= selects by. Path is the folder its rows are in (workspace_cwd, else cwd), omitted when
// none says. A computer's rows in no space come last, with an empty ID, name and path.
type catalogSpace struct {
	MachineID string `json:"machine_id"`
	ID        string `json:"id"`
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	Total     int    `json:"total"`
	NeedsYou  int    `json:"needs_you,omitempty"`
}

const (
	// catalogWait is how long a catalog waits for computers to say what repository a directory holds, before it
	// answers with what it knows (Pending).
	catalogWait = 1500 * time.Millisecond
	// originFresh is how long a computer's answer about a directory is used before it is asked again; originRetry how
	// long after an ask that got no answer (the computer offline, a timeout).
	originFresh = 30 * time.Minute
	originRetry = time.Minute
	// originAskTimeout bounds one ask; originAsks is how many run on one computer at once (each is a few git commands
	// there).
	originAskTimeout = 8 * time.Second
	originAsks       = 3
	// originsKept bounds the remembered directories; past it, the ones no catalog used for originFresh go.
	originsKept = 4096
)

// placeOrigins remembers what repository each directory holds, as its computer last said (git.status with
// identity_only, the node's gitIdentity). It is a cache the hub may lose at any time: a catalog asks again. Nothing
// on a list, count or push path reads it.
type placeOrigins struct {
	mu      sync.Mutex
	entries map[sessionlist.Place]*placeOrigin
	// slots holds, per computer, a token for each ask running there.
	slots map[string]chan struct{}
	// silent: computers whose node answered without an identity (one from before identity_only): not asked again
	// until then.
	silent map[string]time.Time
	// now is the clock and wait what a catalog waits in place of catalogWait, for tests.
	now  func() time.Time
	wait time.Duration
}

type placeOrigin struct {
	// origin is the network origin the computer last named; "" for none (no repository, no remote, a local remote).
	origin string
	// answered: the computer said what the directory holds at least once; until then a catalog waits for the ask.
	answered bool
	// until: not asked again before this.
	until time.Time
	// asking is closed when the ask now out ends; nil when none is.
	asking chan struct{}
	used   time.Time
}

func (o *placeOrigins) clock() time.Time {
	if o.now != nil {
		return o.now()
	}
	return time.Now()
}

// originsOf is each place's repository origin as known now ("" for none or not known). The computers of the places
// never answered for (or answered for a while ago) are asked; it waits up to catalogWait for the answers never had
// before. pending: some of those were still out at the end.
func (h *Hub) originsOf(ctx context.Context, places []sessionlist.Place) (origins map[sessionlist.Place]string, pending bool) {
	o := &h.origins
	now := o.clock()
	wait := cmp.Or(o.wait, catalogWait)
	var first []chan struct{}
	o.mu.Lock()
	if o.entries == nil {
		o.entries, o.slots, o.silent = map[sessionlist.Place]*placeOrigin{}, map[string]chan struct{}{}, map[string]time.Time{}
	}
	for _, p := range places {
		e := o.entries[p]
		if e == nil {
			e = &placeOrigin{}
			o.entries[p] = e
		}
		e.used = now
		if e.asking == nil && !now.Before(e.until) && !now.Before(o.silent[p.Machine]) {
			e.asking = make(chan struct{})
			slot := o.slots[p.Machine]
			if slot == nil {
				slot = make(chan struct{}, originAsks)
				o.slots[p.Machine] = slot
			}
			go h.askOrigin(p, e, slot)
		}
		if e.asking != nil && !e.answered {
			first = append(first, e.asking)
		}
	}
	o.pruneLocked(now)
	o.mu.Unlock()

	if len(first) > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
	waiting:
		for _, done := range first {
			select {
			case <-done:
			case <-timer.C:
				break waiting
			case <-ctx.Done():
				break waiting
			}
		}
	}
	origins = make(map[sessionlist.Place]string, len(places))
	o.mu.Lock()
	defer o.mu.Unlock()
	for _, p := range places {
		if e := o.entries[p]; e != nil {
			origins[p] = e.origin
			pending = pending || (e.asking != nil && !e.answered)
		}
	}
	return origins, pending
}

// pruneLocked drops the directories no catalog used lately once there are too many; one being asked stays.
func (o *placeOrigins) pruneLocked(now time.Time) {
	if len(o.entries) <= originsKept {
		return
	}
	for p, e := range o.entries {
		if e.asking == nil && now.Sub(e.used) > originFresh {
			delete(o.entries, p)
		}
	}
}

// askOrigin asks p's computer what repository the directory holds and records the answer. An ask that got no answer
// (offline, timed out) keeps what the computer said before and is tried again after originRetry.
func (h *Hub) askOrigin(p sessionlist.Place, e *placeOrigin, slot chan struct{}) {
	o := &h.origins
	slot <- struct{}{}
	// The slot is given back once the answer is recorded: the next ask of this computer then sees what this one found.
	defer func() { <-slot }()
	o.mu.Lock()
	quiet := o.clock().Before(o.silent[p.Machine]) // an ask ahead of this one found the node does not answer these
	o.mu.Unlock()
	origin, answer := "", originUnsupported
	if !quiet {
		origin, answer = h.repositoryOrigin(p)
	}
	now := o.clock()
	o.mu.Lock()
	defer o.mu.Unlock()
	switch answer {
	case originSaid:
		e.origin, e.answered, e.until = origin, true, now.Add(originFresh)
	case originUnsupported:
		// Its other directories are not asked either: an older node answers this with a whole status each time.
		o.silent[p.Machine] = now.Add(originFresh)
		e.answered, e.until = true, now.Add(originFresh)
	default:
		e.until = now.Add(originRetry)
	}
	close(e.asking)
	e.asking = nil
}

type originAnswer int

const (
	originUnanswered  originAnswer = iota // the computer was not reached
	originSaid                            // it said what the directory holds
	originUnsupported                     // its node does not know identity_only
)

// repositoryOrigin asks p's computer for the directory's repository identity. Only a network remote is an origin:
// a remote that is a local path names a directory of that one computer.
func (h *Hub) repositoryOrigin(p sessionlist.Place) (string, originAnswer) {
	m := h.machineByID(p.Machine)
	if m == nil || p.Key == "" {
		return "", originUnanswered
	}
	ctx, cancel := context.WithTimeout(context.Background(), originAskTimeout)
	defer cancel()
	var identity proto.RepositoryIdentity
	if err := m.call(ctx, "git.status", map[string]any{"cwd": p.Key, "identity_only": true}, &identity); err != nil {
		// The node's own refusal (the directory is gone, it cannot be read) is an answer: nothing is there to name.
		var refused *rpc.Error
		if errors.As(err, &refused) {
			return "", originSaid
		}
		return "", originUnanswered
	}
	if identity.Version != 1 {
		return "", originUnsupported
	}
	if strings.HasPrefix(identity.Origin, "file:") {
		return "", originSaid
	}
	return identity.Origin, originSaid
}

// folderName is the last folder of a path as its computer wrote it: a Windows computer separates with backslashes
// (and takes slashes too), any other with slashes only. A path that is only a root is its own name.
func folderName(path string) string {
	separators := "/"
	if proto.WindowsPath(path) {
		separators = `/\`
	}
	trimmed := strings.TrimRight(path, separators)
	if at := strings.LastIndexAny(trimmed, separators); at >= 0 {
		trimmed = trimmed[at+1:]
	}
	if trimmed == "" {
		return path
	}
	return trimmed
}

// projectOf is the project a directory belongs to: its repository when its computer named a network origin, else
// its folder's name.
func projectOf(path, origin string) (id, name string) {
	if origin != "" {
		name = origin
		if at := strings.LastIndexByte(origin, '/'); at >= 0 && at < len(origin)-1 {
			name = origin[at+1:]
		}
		return "git:" + strings.ToLower(origin), name
	}
	name = folderName(path)
	return "dir:" + strings.ToLower(name), name
}

// sessionCatalogOf builds the catalog of the rows opts selects; with own (an extension token's manager-limited
// view), only of the machines in it. repositories: the directories' computers are asked what repository each holds.
// Without it every project goes by its folder's name: that answer is the Git routes', which take full credentials,
// so an extension's token neither gets it nor has a computer asked for it.
func (h *Hub) sessionCatalogOf(ctx context.Context, index *sessionlist.Index, opts sessionlist.Options, own []string, limited, repositories bool) (sessionCatalog, error) {
	opts.Cursor = "" // the current membership, as counts=1 reads it without a cursor
	counts, err := index.PlaceCounts(opts)
	if err != nil {
		return sessionCatalog{}, err
	}
	seen := func(mid string) bool { return !limited || slices.Contains(own, mid) }
	out := sessionCatalog{Revision: counts.Revision, Projects: []catalogProject{}, Spaces: []catalogSpace{}}
	machines := map[string]bool{}
	var places []sessionlist.Place // the directories: the places with a path
	for p, n := range counts.Project {
		if !seen(p.Machine) {
			continue
		}
		out.Total += n.Total
		if p.Key != "" {
			places = append(places, p)
		}
	}
	for p := range counts.Group {
		if seen(p.Machine) && p.Key != "" {
			machines[p.Machine] = true
		}
	}
	var origins map[sessionlist.Place]string
	if repositories {
		origins, out.Pending = h.originsOf(ctx, places)
	}

	// The rows in no directory are a project of their own, under the empty id.
	projects := map[string]*catalogProject{}
	for p, n := range counts.Project {
		if !seen(p.Machine) {
			continue
		}
		id := ""
		if p.Key != "" {
			id, _ = projectOf(p.Key, origins[p])
		}
		project := projects[id]
		if project == nil {
			project = &catalogProject{ID: id}
			projects[id] = project
		}
		project.Total += n.Total
		project.NeedsYou += n.NeedsYou
		project.Places = append(project.Places, catalogPlace{MachineID: p.Machine, Path: p.Key, Total: n.Total, NeedsYou: n.NeedsYou})
	}
	for _, project := range projects {
		slices.SortFunc(project.Places, func(a, b catalogPlace) int {
			return cmp.Or(cmp.Compare(b.Total, a.Total), cmp.Compare(a.MachineID, b.MachineID), cmp.Compare(a.Path, b.Path))
		})
		if project.ID != "" {
			// Its directories can spell the remote or the folder in different cases (Notes and notes are one
			// project): the name and the origin are as its busiest directory has them.
			first := sessionlist.Place{Machine: project.Places[0].MachineID, Key: project.Places[0].Path}
			project.Origin = origins[first]
			_, project.Name = projectOf(first.Key, project.Origin)
		}
		out.Projects = append(out.Projects, *project)
	}
	slices.SortFunc(out.Projects, func(a, b catalogProject) int {
		return cmp.Or(lastIfEmpty(a.ID, b.ID), cmp.Compare(b.Total, a.Total),
			cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
	})

	attrs := h.spaceAttrs(machines, counts.Group)
	for p, n := range counts.Group {
		if !seen(p.Machine) {
			continue
		}
		space := catalogSpace{MachineID: p.Machine, ID: p.Key, Total: n.Total, NeedsYou: n.NeedsYou}
		if p.Key != "" {
			space.Name = cmp.Or(attrs[p].name, p.Key)
			space.Path = attrs[p].path
		}
		out.Spaces = append(out.Spaces, space)
	}
	slices.SortFunc(out.Spaces, func(a, b catalogSpace) int {
		return cmp.Or(lastIfEmpty(a.ID, b.ID), cmp.Compare(b.Total, a.Total),
			cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.MachineID, b.MachineID), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// lastIfEmpty orders an entry with an empty id (the rows without the attribute) after every other.
func lastIfEmpty(a, b string) int {
	switch {
	case a == "" && b != "":
		return 1
	case a != "" && b == "":
		return -1
	}
	return 0
}

// spaceAttr is what a space's kept rows say of it: its name and its folder.
type spaceAttr struct{ name, path string }

// folderTally counts a space's rows by folder, keeping the folders in the order the rows came.
type folderTally struct {
	rows  map[string]int
	order []string
}

func (t *folderTally) add(dir string) {
	if t.rows == nil {
		t.rows = map[string]int{}
	}
	if t.rows[dir] == 0 {
		t.order = append(t.order, dir)
	}
	t.rows[dir]++
}

// most is the folder most of the rows are in, the first of them among equals; "" for none.
func (t *folderTally) most() string {
	best := ""
	for _, dir := range t.order {
		if t.rows[dir] > t.rows[best] {
			best = dir
		}
	}
	return best
}

// spaceAttrs is what the kept rows of machines say of each counted space. Its name: the first named row of a space,
// in its machine's list order, then among the archived rows told apart (Machine.archived), which named it in the list
// too. Its folder (workspace_cwd, else cwd): the one most of its listed rows are in, the first in the list's order
// among equals; the archived rows' only when no listed row says one.
func (h *Hub) spaceAttrs(machines map[string]bool, counted map[sessionlist.Place]sessionlist.PlaceCount) map[sessionlist.Place]spaceAttr {
	attrs := map[sessionlist.Place]spaceAttr{}
	folders := map[sessionlist.Place]*[2]folderTally{} // the listed rows' folders, then the archived rows'
	for _, m := range h.machinesNow() {
		if !machines[m.cfg.ID] {
			continue
		}
		list, archived := m.keptWhole()
		for kept, rows := range [2][]proto.Session{list, archived} {
			for i := range rows { // by index: a range value copies every (large) session it passes
				s := &rows[i]
				p := sessionlist.Place{Machine: m.cfg.ID, Key: cmp.Or(s.SpaceID, s.WorkspaceID)}
				if p.Key == "" {
					continue
				}
				if _, ok := counted[p]; !ok {
					continue
				}
				if a := attrs[p]; a.name == "" && s.SpaceName != "" {
					a.name = s.SpaceName
					attrs[p] = a
				}
				if dir := cmp.Or(s.WorkspaceCwd, s.Cwd); dir != "" {
					tally := folders[p]
					if tally == nil {
						tally = &[2]folderTally{}
						folders[p] = tally
					}
					tally[kept].add(dir)
				}
			}
		}
	}
	for p, tally := range folders {
		a := attrs[p]
		a.path = cmp.Or(tally[0].most(), tally[1].most())
		attrs[p] = a
	}
	return attrs
}

// catalogAsked: the request asks for the catalog (catalog=1 / true), which sessionlist.Parse has validated.
func catalogAsked(r *http.Request) bool {
	v := r.URL.Query().Get("catalog")
	return v == "1" || v == "true"
}
