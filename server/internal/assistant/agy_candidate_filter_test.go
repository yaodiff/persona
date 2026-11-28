package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

func agyPaneRef(t Todo) unlinkedPane {
	a := t.Agents[0]
	return unlinkedPane{MachineID: a.MachineID, SessionID: a.SessionID, LinkedAt: a.LinkedAt}
}

func TestAgyUnlinkedCurrentAssignments(t *testing.T) {
	s, f, task, pane := unlinkedPaneFixture(t, "en")
	now := time.Now().UTC()
	makeRow := func(id string) proto.Session {
		c := proto.Session{Agent: "agy", SessionID: "agy-" + id, SourceKind: proto.SourceRuntime, Cwd: pane.Cwd, UpdatedAt: now}
		c.SetAliases([]string{"former-" + id})
		return c
	}
	owned, retained, released := makeRow("responsible"), makeRow("retained"), makeRow("released")
	f.setSessions("box", pane, owned, retained, released)
	putTodo(s, Todo{ID: "closed", DoneAt: &now, Agents: []TodoAgent{
		{MachineID: "box", SessionID: "former-retained"}, {MachineID: "box", SessionID: "former-released", ReleasedAt: &now},
	}})
	putTodo(s, Todo{ID: "assigned", Responsible: &TodoResponsible{Kind: "session", SessionID: "former-responsible"}})
	detail := s.agyUnlinkedDetail(context.Background(), task, []unlinkedPane{agyPaneRef(task)}, "server", "conv1")
	if !strings.Contains(detail, released.SessionID) || strings.Contains(detail, owned.SessionID) || strings.Contains(detail, retained.SessionID) {
		t.Fatalf("detail after a release and two retained assignments: %q", detail)
	}
}

func TestAgyUnlinkedCanonicalDirectory(t *testing.T) {
	for _, tc := range []struct{ name, paneCwd, paneReal, rowCwd, rowReal string }{
		{"symlink", "/alias/app", "/work/app", "/another/alias", "/work/app"},
		{"Windows", `C:\Code\App`, "", "c:/code/app/", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, task, pane := unlinkedPaneFixture(t, "en")
			pane.Cwd, pane.WorkspaceCwd = tc.paneCwd, tc.paneReal
			c := proto.Session{Agent: "agy", SessionID: "agy-real", SourceKind: proto.SourceRuntime, Cwd: tc.rowCwd, WorkspaceCwd: tc.rowReal, UpdatedAt: time.Now()}
			f.setSessions("box", pane, c)
			detail := s.agyUnlinkedDetail(context.Background(), task, []unlinkedPane{agyPaneRef(task)}, "server", "conv1")
			if !strings.Contains(detail, c.SessionID) {
				t.Fatalf("missing candidate at same physical directory: %q", detail)
			}
		})
	}
}

func TestAgyUnlinkedActualRecipient(t *testing.T) {
	s, f, task, pane := unlinkedPaneFixture(t, "en")
	candidate := proto.Session{Agent: "agy", SessionID: "agy-other", SourceKind: proto.SourceRuntime, Cwd: pane.Cwd, UpdatedAt: time.Now()}
	f.setSessions("box", pane, candidate)
	f.setSessions("server", proto.Session{SessionID: "conv1", Assistant: true}, proto.Session{SessionID: "another", Assistant: true, Space: "health"})
	ref := agyPaneRef(task)
	detail := func(to string) string {
		return s.agyUnlinkedDetail(context.Background(), task, []unlinkedPane{ref}, "server", to)
	}
	f.setSessions("another-machine", proto.Session{SessionID: "conv1", Assistant: true, Space: "health"})
	if got := s.agyUnlinkedDetail(context.Background(), task, []unlinkedPane{ref}, "another-machine", "conv1"); got != i18n.T(i18n.En, checkReasonAgyUnlinked) {
		t.Fatalf("same recipient ID on a different machine received %q", got)
	}
	ref.LinkedAt = ref.LinkedAt.Truncate(time.Microsecond) // the link in Postgres has microsecond precision
	if got := detail("conv1"); !strings.Contains(got, candidate.SessionID) {
		t.Fatalf("ordinary supervisor: %q", got)
	}
	for _, to := range []string{"another", "", "missing", "box/worker"} {
		if got := detail(to); got != i18n.T(i18n.En, checkReasonAgyUnlinked) {
			t.Fatalf("recipient %q received %q", to, got)
		}
	}
	pane.SetAliases([]string{candidate.SessionID})
	f.setSessions("box", pane)
	if got := detail("conv1"); strings.Contains(got, pane.SessionID) || strings.Contains(got, candidate.SessionID) {
		t.Fatalf("already bound: %q", got)
	}
	pane.SetAliases(nil)
	f.setSessions("box", pane, candidate)
	task.Agents[0].LinkedAt = task.Agents[0].LinkedAt.Add(time.Second)
	putTodo(s, task)
	if got := detail("conv1"); strings.Contains(got, candidate.SessionID) {
		t.Fatalf("relinked pane: %q", got)
	}
}

func agyUnavailableGolden(t *testing.T, s *Service, f *fakeHub, todo Todo, pane proto.Session) string {
	t.Helper()
	f.mu.Lock()
	f.sessionsFail["box"] = 1
	f.mu.Unlock()
	got := s.agyUnlinkedDetail(context.Background(), todo, []unlinkedPane{agyPaneRef(todo)}, "server", "conv1")
	if strings.Contains(got, pane.SessionID) || strings.Contains(got, "agy-conversation") {
		t.Fatalf("unverified inventory exposed an ID: %q", got)
	}
	return got
}

func TestAgyUnlinkedRestrictedGuestOrUnlistedMachine(t *testing.T) {
	for _, kind := range []string{"handoff", "room", "unlisted machine"} {
		t.Run(kind, func(t *testing.T) {
			s, f, task, pane := unlinkedPaneFixture(t, "en")
			ctx := context.Background()
			c := proto.Session{SessionID: "agy-candidate", Agent: "agy", SourceKind: proto.SourceRuntime, Cwd: pane.Cwd, UpdatedAt: time.Now()}
			f.setSessions("box", pane, c)
			// Even an old whitelist naming agent_read cannot grant that tool to either kind of dedicated guest.
			var err error
			switch kind {
			case "handoff":
				err = s.guestSetHandoff(ctx, "server", "conv1", "handoff", []string{"agent_read"})
			case "room":
				err = s.guestPut(ctx, "conv1", &guestConv{MachineID: "server", Kind: guestKindMeeting, Recording: "recording", Tools: []string{"agent_read"}})
			default:
				f.mu.Lock()
				f.devices = []Device{{ID: "server", Name: "server", Reachable: true}}
				f.mu.Unlock()
			}
			if err != nil {
				t.Fatal(err)
			}
			detail := s.agyUnlinkedDetail(ctx, task, []unlinkedPane{agyPaneRef(task)}, "server", "conv1")
			if strings.Contains(detail, pane.SessionID) || strings.Contains(detail, c.SessionID) {
				t.Fatalf("%s exposed session identities: %q", kind, detail)
			}
		})
	}
}
