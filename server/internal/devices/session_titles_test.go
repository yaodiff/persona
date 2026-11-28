package devices

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// A node from before titles were cut sends whatever its agents stored as a title, a whole opening prompt among them:
// the manager keeps its list with each title cut to proto.TitleMax runes (#16343), from a sessions.changed and from the
// list a connection starts with alike, so every reader of the kept list (the assistant, the archive, the hub) has the
// cut one. A title that fits is kept as it came. The titles are synthetic.
func TestKeptListCarriesBoundedTitles(t *testing.T) {
	long := strings.Repeat("把这段话改得更清楚一些。", 3000)
	check := func(where string, list []proto.Session) {
		t.Helper()
		for _, s := range list {
			switch s.SessionID {
			case "short":
				if s.Title != "Fix the login page" {
					t.Errorf("%s: a title that fits changed: %q", where, s.Title)
				}
			default:
				if n := utf8.RuneCountInString(s.Title); n != proto.TitleMax || !strings.HasPrefix(long, s.Title) {
					t.Errorf("%s: %s kept with a title of %d runes, want the first %d", where, s.SessionID, n, proto.TitleMax)
				}
			}
			if s.MachineID != "computer" {
				t.Errorf("%s: %s on machine %q", where, s.SessionID, s.MachineID)
			}
		}
	}
	peer := new(rpc.Peer)
	m := &Machine{cfg: MachineConfig{ID: "computer"}, peer: peer}
	if !m.setInitialSessions(peer, m.sessionsRevision(), []proto.Session{{SessionID: "snapshot", Title: long}}) {
		t.Fatal("rejected the current connection's initial list")
	}
	check("initial list", m.sessionList())
	m.setSessions([]proto.Session{{SessionID: "notified", Title: long}, {SessionID: "short", Title: "Fix the login page"}})
	check("sessions.changed", m.sessionList())
	if got := len(m.sessionList()); got != 2 {
		t.Fatalf("%d sessions kept, want 2", got)
	}
}
