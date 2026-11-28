package assistant

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
)

// The archive's rows carry titles cut to proto.TitleMax runes (#16343): a snapshot taken now keeps the cut title, and a
// record saved before titles were cut, with a whole opening prompt as its title, is listed cut once its node no
// longer lists the session (GET /api/v1/sessions/archived, the whole list). The titles are synthetic.
func TestArchiveRowsCarryBoundedTitles(t *testing.T) {
	long := strings.Repeat("整理一下这周的会议记录，列出待办。", 2000)
	cut := func(where, title string) {
		t.Helper()
		if n := utf8.RuneCountInString(title); n != proto.TitleMax || !strings.HasPrefix(long, title) {
			t.Errorf("%s: a title of %d runes, want the prompt's first %d", where, n, proto.TitleMax)
		}
	}
	cut("archiveSnapshot", archiveSnapshot(proto.Session{MachineID: "mac", SessionID: "s", Title: long}).Title)
	if got := archiveSnapshot(proto.Session{MachineID: "mac", SessionID: "s", Title: "Fix the login page"}).Title; got != "Fix the login page" {
		t.Errorf("a title that fits changed: %q", got)
	}

	saved, _ := archiveTestService(t)
	records := map[string]agentArchive{runtimeListID("mac", "old-session"): {
		Session: proto.Session{MachineID: "mac", SessionID: "old-session", Agent: "codex", Title: long,
			Status: proto.StatusOffline, State: proto.StateOffline, UpdatedAt: time.Now().Add(-48 * time.Hour),
			Capabilities: &proto.SessionCapabilities{Read: true}},
		Archived: true, ChangedAt: time.Now().Add(-24 * time.Hour),
	}}
	if err := saved.st.PutSetting(context.Background(), agentArchivesKey, records); err != nil {
		t.Fatal(err)
	}
	restarted, _ := archiveTestService(t)
	restarted.s.setStore(func() Store { return saved.st })
	restarted.d.setSessions("mac", nil)
	rows := archiveRows(t, restarted.s)
	if len(rows) != 1 || rows[0].SessionID != "old-session" {
		t.Fatalf("archived rows: %d, want the saved record's", len(rows))
	}
	cut("GET /api/v1/sessions/archived", rows[0].Title)
	for _, row := range restarted.s.decorated("mac") {
		if row.SessionID == "old-session" {
			cut("the whole list", row.Title)
		}
	}
}
