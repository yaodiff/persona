package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	neturl "net/url"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
	"github.com/nwverse/persona/server/internal/sessionlist"
)

// catalogNode answers git.status with identity_only as a computer's node does: what repository each directory holds.
type catalogNode struct {
	mu sync.Mutex
	// origins is each directory's remote; a directory not in it is no repository.
	origins map[string]string
	// gone are the directories the node cannot read: it refuses them.
	gone map[string]bool
	// old: a node from before identity_only, which answers with a whole status (no version).
	old bool
	// hold, when set, keeps every answer until it is closed.
	hold  chan struct{}
	asked []string
	done  chan struct{}
}

func (n *catalogNode) Call(_ context.Context, method string, params, out any) error {
	p, _ := params.(map[string]any)
	cwd, _ := p["cwd"].(string)
	n.mu.Lock()
	n.asked = append(n.asked, method+" "+cwd)
	hold, old, origin, gone := n.hold, n.old, n.origins[cwd], n.gone[cwd]
	_, repo := n.origins[cwd]
	n.mu.Unlock()
	if hold != nil {
		<-hold
	}
	if method != "git.status" || p["identity_only"] != true {
		return errors.New("unexpected call")
	}
	if gone {
		return &rpc.Error{Message: "lstat " + cwd + ": no such file or directory"}
	}
	identity := out.(*proto.RepositoryIdentity)
	if old {
		*identity = proto.RepositoryIdentity{IsRepo: repo}
		return nil
	}
	*identity = proto.RepositoryIdentity{Version: 1, Cwd: cwd, IsRepo: repo, Origin: origin}
	return nil
}
func (n *catalogNode) Notify(string, any) error { return nil }
func (n *catalogNode) Done() <-chan struct{}    { return n.done }
func (n *catalogNode) Close(error)              {}

func (n *catalogNode) calls() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return slices.Sorted(slices.Values(n.asked))
}

// catalogMachine adds a computer with sessions (id, directory, space) and, with origins, a node that names them.
func catalogMachine(h *Hub, mid string, node *catalogNode, sessions ...proto.Session) *Machine {
	m := newMachine(h, MachineConfig{ID: mid})
	for i := range sessions {
		sessions[i].MachineID = mid
		sessions[i].UpdatedAt = time.Unix(int64(1000+i), 0)
		if sessions[i].State == "" {
			sessions[i].State = "idle"
		}
	}
	m.sessions = sessions
	if node != nil {
		node.done = make(chan struct{})
		m.peer = node
	}
	h.machines, h.byID[mid] = append(h.machines, m), m
	return m
}

func readCatalog(t *testing.T, h *Hub, query string) sessionCatalog {
	t.Helper()
	w := httptest.NewRecorder()
	h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?catalog=1"+query, nil))
	if w.Code != 200 {
		t.Fatalf("catalog%s: %d %s", query, w.Code, w.Body)
	}
	var out sessionCatalog
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// awaitAsks waits until no ask of a directory's computer is out, by the channels the asks close: no polling.
func awaitAsks(t *testing.T, h *Hub) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		var out []chan struct{}
		h.origins.mu.Lock()
		for _, e := range h.origins.entries {
			if e.asking != nil {
				out = append(out, e.asking)
			}
		}
		h.origins.mu.Unlock()
		if len(out) == 0 {
			return
		}
		for _, done := range out {
			select {
			case <-done:
			case <-deadline:
				t.Fatal("an ask never ended")
			}
		}
	}
}

func project(c sessionCatalog, id string) *catalogProject {
	for i := range c.Projects {
		if c.Projects[i].ID == id {
			return &c.Projects[i]
		}
	}
	return nil
}

// threeComputers: one repository checked out on a Mac and a Windows PC (and a fork of it on the Mac), notes that are
// no repository on both, and a computer that is offline, with a session in no directory.
func threeComputers(t *testing.T) (h *Hub, mac, pc *catalogNode) {
	h = newTestHub(t)
	mac = &catalogNode{origins: map[string]string{"/Users/me/code/app": "git.example.com/team/app", "/Users/me/forks/app": "git.example.com/me/app"}}
	pc = &catalogNode{origins: map[string]string{`C:\code\App`: "git.example.com/Team/App"}}
	catalogMachine(h, "mac", mac,
		proto.Session{SessionID: "a1", Cwd: "/Users/me/code/app/web", RepoRoot: "/Users/me/code/app", SpaceID: "w1", SpaceName: "Release"},
		proto.Session{SessionID: "a2", Cwd: "/Users/me/code/app", RepoRoot: "/Users/me/code/app", SpaceID: "w1", SpaceName: "Release", State: "needs_you"},
		proto.Session{SessionID: "a3", Cwd: "/Users/me/notes", SpaceID: "w2", SpaceName: "Writing"},
		proto.Session{SessionID: "a4", Cwd: "/Users/me/forks/app", RepoRoot: "/Users/me/forks/app"},
	)
	catalogMachine(h, "pc", pc,
		proto.Session{SessionID: "b1", Cwd: `C:\code\App`, RepoRoot: `C:\code\App`, SpaceID: "w1", SpaceName: "Desk"},
		proto.Session{SessionID: "b2", Cwd: `D:\work\Notes`, WorkspaceID: "ws-9"},
		proto.Session{SessionID: "b3", Cwd: `D:\work\Notes\`, WorkspaceID: "ws-9"},
	)
	catalogMachine(h, "nas", nil,
		proto.Session{SessionID: "c1", Cwd: "/volume1/code/app", RepoRoot: "/volume1/code/app"},
		proto.Session{SessionID: "c2"}) // in no directory
	return h, mac, pc
}

// A repository is one project wherever it is checked out, with each directory listed; a directory that is no
// repository goes by its folder's name, a Windows path by its own separator.
func TestSessionCatalogGroupsARepositoryAcrossComputers(t *testing.T) {
	h, mac, pc := threeComputers(t)
	got := readCatalog(t, h, "")
	if got.Total != 9 || got.Pending || got.Revision == "" {
		t.Fatalf("catalog: %+v", got)
	}
	// Projects by count, then name, then id; a project's places by count, then computer, then path. A project is
	// named, and its origin spelled, as its first place has them.
	want := []catalogProject{
		{ID: "git:git.example.com/team/app", Name: "app", Origin: "git.example.com/team/app", Total: 3, NeedsYou: 1, Places: []catalogPlace{
			{MachineID: "mac", Path: "/Users/me/code/app", Total: 2, NeedsYou: 1}, {MachineID: "pc", Path: `C:\code\App`, Total: 1}}},
		{ID: "dir:notes", Name: "notes", Total: 3, Places: []catalogPlace{
			{MachineID: "mac", Path: "/Users/me/notes", Total: 1}, {MachineID: "pc", Path: `D:\work\Notes`, Total: 1}, {MachineID: "pc", Path: `D:\work\Notes\`, Total: 1}}},
		{ID: "dir:app", Name: "app", Total: 1, Places: []catalogPlace{{MachineID: "nas", Path: "/volume1/code/app", Total: 1}}},
		{ID: "git:git.example.com/me/app", Name: "app", Origin: "git.example.com/me/app", Total: 1, Places: []catalogPlace{
			{MachineID: "mac", Path: "/Users/me/forks/app", Total: 1}}},
		// the rows in no directory come last, whatever their number
		{Total: 1, Places: []catalogPlace{{MachineID: "nas", Total: 1}}},
	}
	if !reflect.DeepEqual(got.Projects, want) {
		t.Fatalf("projects:\n got %+v\nwant %+v", got.Projects, want)
	}
	// A space's folder is the one most of its rows are in, the first listed among equals (TestSessionCatalogSpaceFolder).
	wantSpaces := []catalogSpace{
		{MachineID: "mac", ID: "w1", Name: "Release", Path: "/Users/me/code/app/web", Total: 2, NeedsYou: 1},
		{MachineID: "pc", ID: "ws-9", Name: "ws-9", Path: `D:\work\Notes`, Total: 2},
		{MachineID: "pc", ID: "w1", Name: "Desk", Path: `C:\code\App`, Total: 1}, {MachineID: "mac", ID: "w2", Name: "Writing", Path: "/Users/me/notes", Total: 1},
		// each computer's rows in no space, last
		{MachineID: "nas", Total: 2}, {MachineID: "mac", Total: 1},
	}
	if !reflect.DeepEqual(got.Spaces, wantSpaces) {
		t.Fatalf("spaces:\n got %+v\nwant %+v", got.Spaces, wantSpaces)
	}
	// Each directory was asked of its own computer, once; the offline computer's goes by its folder.
	if calls := mac.calls(); !reflect.DeepEqual(calls, []string{"git.status /Users/me/code/app", "git.status /Users/me/forks/app", "git.status /Users/me/notes"}) {
		t.Fatalf("mac was asked %v", calls)
	}
	if calls := pc.calls(); !reflect.DeepEqual(calls, []string{`git.status C:\code\App`, `git.status D:\work\Notes`, `git.status D:\work\Notes\`}) {
		t.Fatalf("pc was asked %v", calls)
	}
	// A second read asks nothing again.
	if again := readCatalog(t, h, ""); !reflect.DeepEqual(again.Projects, want) || len(mac.calls()) != 3 || len(pc.calls()) != 3 {
		t.Fatalf("second read: %+v, mac %v, pc %v", again.Projects, mac.calls(), pc.calls())
	}
	// Every row is in exactly one project and one space entry.
	projects, spaces := 0, 0
	for _, p := range got.Projects {
		places := 0
		for _, place := range p.Places {
			places += place.Total
		}
		if places != p.Total {
			t.Errorf("%q: places hold %d of %d", p.ID, places, p.Total)
		}
		projects += p.Total
	}
	for _, sp := range got.Spaces {
		spaces += sp.Total
	}
	if projects != got.Total || spaces != got.Total {
		t.Fatalf("projects hold %d, spaces %d of %d", projects, spaces, got.Total)
	}
}

// A space says the folder its rows are in: workspace_cwd, else cwd, the one most of its listed rows are in, the first
// of them among equals; an archived row's only when no listed row says one; none for a space no row places, nor for the
// rows in no space.
func TestSessionCatalogSpaceFolder(t *testing.T) {
	h := newTestHub(t)
	m := catalogMachine(h, "box", nil,
		proto.Session{SessionID: "s1", Cwd: "/srv/app/web", WorkspaceCwd: "/srv/app", SpaceID: "w1", SpaceName: "app"},
		proto.Session{SessionID: "s2", Cwd: "/srv/other", SpaceID: "w1", SpaceName: "app"},
		proto.Session{SessionID: "s3", Cwd: "/srv/app", SpaceID: "w1", SpaceName: "app"},
		proto.Session{SessionID: "s4", Cwd: "/srv/b", SpaceID: "w2", SpaceName: "tie"},
		proto.Session{SessionID: "s5", Cwd: "/srv/a", SpaceID: "w2", SpaceName: "tie"},
		proto.Session{SessionID: "s6", SpaceID: "w3", SpaceName: "old"},
		proto.Session{SessionID: "s7", SpaceID: "w4", SpaceName: "nowhere"},
		proto.Session{SessionID: "s8", Cwd: "/srv/loose"},
	)
	m.archived = []proto.Session{{MachineID: "box", SessionID: "a1", Cwd: "/srv/old", SpaceID: "w3", SpaceName: "old"}}
	got := map[string]string{}
	for _, sp := range readCatalog(t, h, "").Spaces {
		got[sp.ID] = sp.Path
	}
	want := map[string]string{"w1": "/srv/app", "w2": "/srv/b", "w3": "/srv/old", "w4": "", "": ""}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("space folders %v, want %v", got, want)
	}
}

// The catalog counts the same filtered rows as counts=1, and a project's places select exactly its sessions.
func TestSessionCatalogFollowsTheFiltersAndPlaces(t *testing.T) {
	h, _, _ := threeComputers(t)
	needs := readCatalog(t, h, "&state=needs_you")
	if needs.Total != 1 || len(needs.Projects) != 1 || needs.Projects[0].ID != "git:git.example.com/team/app" || needs.Projects[0].Total != 1 ||
		needs.Projects[0].NeedsYou != 1 || len(needs.Spaces) != 1 || needs.Spaces[0].Name != "Release" || needs.Spaces[0].NeedsYou != 1 {
		t.Fatalf("needs you: %+v", needs)
	}
	if one := readCatalog(t, h, "&machine_id=pc"); one.Total != 3 || len(one.Projects) != 2 || project(one, "git:git.example.com/team/app").Total != 1 {
		t.Fatalf("one computer: %+v", one)
	}
	if found := readCatalog(t, h, "&q=forks"); found.Total != 1 || found.Projects[0].ID != "git:git.example.com/me/app" {
		t.Fatalf("search: %+v", found)
	}
	if none := readCatalog(t, h, "&q=nothing-matches"); none.Total != 0 || none.Projects == nil || none.Spaces == nil || len(none.Projects)+len(none.Spaces) != 0 {
		t.Fatalf("no match: %+v", none)
	}

	all := readCatalog(t, h, "")
	app := project(all, "git:git.example.com/team/app")
	query := "summary=1"
	for _, p := range app.Places {
		pair, _ := json.Marshal([]string{p.MachineID, p.Path})
		query += "&place=" + neturl.QueryEscape(string(pair))
	}
	page := runtimePage(t, h, query)
	var ids func(string) []string
	var listed []string
	for _, s := range page.Sessions {
		listed = append(listed, s.MachineID+"/"+s.SessionID)
	}
	slices.Sort(listed)
	if page.Total != app.Total || !reflect.DeepEqual(listed, []string{"mac/a1", "mac/a2", "pc/b1"}) {
		t.Fatalf("the project's sessions: total %d, %v", page.Total, listed)
	}
	// Its counts are the project's too, by state and by computer.
	w := httptest.NewRecorder()
	h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?counts=1&"+strings.TrimPrefix(query, "summary=1&"), nil))
	var counts sessionlist.Counts
	if err := json.Unmarshal(w.Body.Bytes(), &counts); err != nil || counts.Total != 3 || counts.State["needs_you"] != 1 || counts.Machine["pc"] != 1 {
		t.Fatalf("the project's counts: %s %v", w.Body, err)
	}

	// A space is selected by its computer and id; an empty id or path selects the rows without one.
	ids = func(query string) []string {
		t.Helper()
		var out []string
		for _, s := range runtimePage(t, h, "summary=1&"+query).Sessions {
			out = append(out, s.MachineID+"/"+s.SessionID)
		}
		slices.Sort(out)
		return out
	}
	pair := func(key, machine, value string) string {
		b, _ := json.Marshal([]string{machine, value})
		return key + "=" + neturl.QueryEscape(string(b))
	}
	for query, want := range map[string][]string{
		pair("space", "mac", "w1") + "&" + pair("space", "pc", "w1"):          {"mac/a1", "mac/a2", "pc/b1"},
		pair("space", "pc", "ws-9"):                                           {"pc/b2", "pc/b3"},
		pair("space", "mac", "") + "&" + pair("space", "nas", ""):             {"mac/a4", "nas/c1", "nas/c2"},
		pair("place", "nas", ""):                                              {"nas/c2"},
		pair("space", "mac", "w1") + "&state=needs_you":                       {"mac/a2"},
		pair("space", "mac", "w1") + "&" + pair("place", "pc", `C:\code\App`): nil,
	} {
		if got := ids(query); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: %v, want %v", query, got, want)
		}
	}

	for _, bad := range []string{"catalog=yes", "catalog=1&catalog=1", "catalog=1&place=junk", "catalog=1&limit=0", "place=%5B%22%22%2C%22%2Fx%22%5D",
		"space=junk", "space=%5B%22%22%2C%22w1%22%5D", "space=%5B%22mac%22%5D"} {
		w := httptest.NewRecorder()
		h.listSessions(w, httptest.NewRequest("GET", "/api/v1/sessions?"+bad, nil))
		if w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_query") {
			t.Errorf("%s: %d %s", bad, w.Code, w.Body)
		}
	}
	if !slices.Contains(h.features(), featureSessionsCatalog) || slices.Contains(proto.Features, featureSessionsCatalog) {
		t.Error("sessions_catalog must be advertised by the hub")
	}
}

// A manager-limited view has only its own computers' projects, spaces and total.
func TestSessionCatalogOfOwnMachinesOnly(t *testing.T) {
	h, mac, pc := threeComputers(t)
	got, err := h.sessionCatalogOf(context.Background(), h.sessionListIndex(), sessionlist.Options{}, []string{"pc"}, true, true)
	if err != nil || got.Total != 3 || len(got.Projects) != 2 || len(got.Spaces) != 2 {
		t.Fatalf("own: %+v %v", got, err)
	}
	for _, sp := range got.Spaces {
		if sp.MachineID != "pc" {
			t.Fatalf("another manager's space: %+v", sp)
		}
	}
	for _, p := range got.Projects {
		for _, place := range p.Places {
			if place.MachineID != "pc" {
				t.Fatalf("another manager's directory: %+v", p)
			}
		}
	}
	if len(mac.calls()) != 0 || len(pc.calls()) != 3 {
		t.Fatalf("asked mac %v, pc %v", mac.calls(), pc.calls())
	}
}

// An extension's token gets every project by its folder's name, and no computer is asked for it: what repository a
// directory holds is the Git routes' answer, which take full credentials (testdata/route_scopes.txt).
func TestSessionCatalogForAnExtensionAsksNoComputer(t *testing.T) {
	h, mac, pc := threeComputers(t)
	read := func(p principal) sessionCatalog {
		t.Helper()
		r := httptest.NewRequest("GET", "/api/v1/sessions?catalog=1", nil)
		r = r.WithContext(context.WithValue(r.Context(), principalKey{}, p))
		w := httptest.NewRecorder()
		h.listSessions(w, r)
		var out sessionCatalog
		if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || w.Code != 200 {
			t.Fatalf("catalog: %d %s %v", w.Code, w.Body, err)
		}
		return out
	}
	ext := principal{ID: "extension:tool", Role: roleExtension, Extension: "tool"}
	got := read(ext)
	if got.Pending || len(mac.calls())+len(pc.calls()) != 0 {
		t.Fatalf("asked for an extension: pending %v, mac %v, pc %v", got.Pending, mac.calls(), pc.calls())
	}
	for _, p := range got.Projects {
		if p.Origin != "" || strings.HasPrefix(p.ID, "git:") {
			t.Fatalf("an extension was told a repository: %+v", p)
		}
	}
	// by folder, the checkouts, the fork and the offline copy are all "app"
	if app := project(got, "dir:app"); app == nil || app.Total != 5 || len(app.Places) != 4 {
		t.Fatalf("app by folder: %+v", got.Projects)
	}
	// What a full client learned since is not handed to the extension either.
	if full := read(principal{Role: roleFull}); project(full, "git:git.example.com/team/app") == nil {
		t.Fatalf("a full client: %+v", full.Projects)
	}
	if again := read(ext); project(again, "dir:app") == nil || project(again, "git:git.example.com/team/app") != nil {
		t.Fatalf("after a full client asked: %+v", again.Projects)
	}
}

// A computer that takes its time: the catalog answers with folders for now and says so, then with the repository.
func TestSessionCatalogIsPendingWhileAComputerAnswers(t *testing.T) {
	h := newTestHub(t)
	h.origins.wait = 20 * time.Millisecond
	hold := make(chan struct{})
	node := &catalogNode{origins: map[string]string{"/code/app": "git.example.com/team/app"}, hold: hold}
	catalogMachine(h, "mac", node, proto.Session{SessionID: "a1", Cwd: "/code/app", RepoRoot: "/code/app"})
	first := readCatalog(t, h, "")
	if !first.Pending || len(first.Projects) != 1 || first.Projects[0].ID != "dir:app" {
		t.Fatalf("while asking: %+v", first)
	}
	// Still out: a second read waits for the same ask, it does not send another.
	if second := readCatalog(t, h, ""); !second.Pending || len(node.calls()) != 1 {
		t.Fatalf("second read: %+v, calls %v", second, node.calls())
	}
	close(hold)
	awaitAsks(t, h)
	if got := readCatalog(t, h, ""); got.Pending || got.Projects[0].ID != "git:git.example.com/team/app" || len(node.calls()) != 1 {
		t.Fatalf("once it answered: %+v, asked %v", got, node.calls())
	}
}

// What a computer said is used for originFresh and then asked again without holding the catalog up; an ask that got
// no answer keeps what was known and is retried after originRetry; a refusal is an answer.
func TestPlaceOriginsAgeAndRetry(t *testing.T) {
	h := newTestHub(t)
	now := time.Unix(10_000, 0)
	var clock sync.Mutex
	h.origins.now = func() time.Time { clock.Lock(); defer clock.Unlock(); return now }
	advance := func(d time.Duration) { clock.Lock(); now = now.Add(d); clock.Unlock() }
	node := &catalogNode{origins: map[string]string{"/code/app": "git.example.com/team/app"}, gone: map[string]bool{"/code/gone": true}}
	m := catalogMachine(h, "mac", node,
		proto.Session{SessionID: "a1", Cwd: "/code/app", RepoRoot: "/code/app"},
		proto.Session{SessionID: "a2", Cwd: "/code/gone"})
	// settled reads the catalog (which asks what is due), waits for the asks and reads what they left; the clock
	// stands still meanwhile, so the second read asks nothing more. want: how many times the node was asked by then.
	settled := func(want int) sessionCatalog {
		t.Helper()
		readCatalog(t, h, "")
		awaitAsks(t, h)
		got := readCatalog(t, h, "")
		if len(node.calls()) != want || got.Pending {
			t.Fatalf("asked %v, want %d calls; pending %v", node.calls(), want, got.Pending)
		}
		return got
	}
	first := settled(2)
	if project(first, "git:git.example.com/team/app") == nil || project(first, "dir:gone") == nil || len(node.calls()) != 2 {
		t.Fatalf("first: %+v %v", first, node.calls())
	}
	advance(originFresh - time.Second)
	if readCatalog(t, h, ""); len(node.calls()) != 2 {
		t.Fatalf("asked again while fresh: %v", node.calls())
	}
	// Stale: asked again, and the remote it has moved to is the project from then on.
	node.mu.Lock()
	node.origins["/code/app"] = "git.example.com/team/renamed"
	node.mu.Unlock()
	advance(2 * time.Second)
	if stale := readCatalog(t, h, ""); stale.Pending {
		t.Fatalf("a refresh held the catalog up: %+v", stale)
	}
	if later := settled(4); project(later, "git:git.example.com/team/renamed") == nil {
		t.Fatalf("after the refresh: %+v", later)
	}
	// The computer goes offline: what it said stays, and it is asked again only after originRetry.
	m.mu.Lock()
	m.peer = nil
	m.mu.Unlock()
	advance(originFresh + time.Second)
	if offline := settled(4); project(offline, "git:git.example.com/team/renamed") == nil {
		t.Fatalf("offline: %+v", offline)
	}
	m.mu.Lock()
	m.peer = node
	m.mu.Unlock()
	if readCatalog(t, h, ""); len(node.calls()) != 4 {
		t.Fatalf("retried before originRetry: %v", node.calls())
	}
	advance(originRetry + time.Second)
	if back := settled(6); project(back, "git:git.example.com/team/renamed") == nil {
		t.Fatalf("back online: %+v", back)
	}
}

// A node from before identity_only answers with a whole status: its computer is asked a few times at most, and its
// directories go by their folders.
func TestPlaceOriginsLeaveAnOlderNodeAlone(t *testing.T) {
	h := newTestHub(t)
	node := &catalogNode{old: true, origins: map[string]string{}}
	var sessions []proto.Session
	for _, dir := range []string{"/code/a", "/code/b", "/code/c", "/code/d", "/code/e", "/code/f", "/code/g", "/code/h"} {
		sessions = append(sessions, proto.Session{SessionID: "s" + dir, Cwd: dir, RepoRoot: dir})
	}
	catalogMachine(h, "old", node, sessions...)
	got := readCatalog(t, h, "")
	if got.Pending || len(got.Projects) != 8 || got.Projects[0].Origin != "" || !strings.HasPrefix(got.Projects[0].ID, "dir:") {
		t.Fatalf("catalog: %+v", got)
	}
	if n := len(node.calls()); n == 0 || n > originAsks {
		t.Fatalf("asked an older node %d times, want 1 to %d", n, originAsks)
	}
	before := len(node.calls())
	if readCatalog(t, h, ""); len(node.calls()) != before {
		t.Fatalf("asked it again: %v", node.calls())
	}
}

func TestProjectOfAndFolderName(t *testing.T) {
	for path, want := range map[string]string{
		"/home/me/code/app": "app", "/home/me/code/app/": "app", "/": "/", "app": "app",
		`C:\Users\me\code\App`: "App", `C:\Users\me\code\App\`: "App", `c:/code/app`: "app", `C:\code/mixed\app`: "app",
		`C:\`: "C:", `\\server\share\app`: "app", "/home/me/odd\\name": "odd\\name", "/home/me/C:\\x": "C:\\x",
	} {
		if got := folderName(path); got != want {
			t.Errorf("folderName(%q) = %q, want %q", path, got, want)
		}
	}
	for _, c := range []struct{ path, origin, id, name string }{
		{"/code/checkout", "git.example.com/Team/App", "git:git.example.com/team/app", "App"},
		{"/code/checkout", "account-0123456789abcdef0123456789abcdef@git.example.com/team/app", "git:account-0123456789abcdef0123456789abcdef@git.example.com/team/app", "app"},
		{`C:\code\Notes`, "", "dir:notes", "Notes"},
		{"/home/me/笔记", "", "dir:笔记", "笔记"},
	} {
		if id, name := projectOf(c.path, c.origin); id != c.id || name != c.name {
			t.Errorf("projectOf(%q, %q) = %q, %q", c.path, c.origin, id, name)
		}
	}
}
