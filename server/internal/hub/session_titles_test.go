package hub

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/sessionhistory"
)

// A node from before titles were cut answers sessions.history with its agents' titles whole, an opening prompt among
// them: the hub's history page carries each cut to proto.TitleMax runes on a rune boundary (#16343), as the whole-list
// and paged answers already cut theirs; a title that fits goes out as it came. The titles are synthetic.
func TestSessionsHistoryCarriesBoundedTitles(t *testing.T) {
	h := newTestHub(t)
	now := time.Now().UTC().Add(-time.Minute)
	long := strings.Repeat("请把这个页面的布局改成两栏。", 2500)
	row := func(s proto.Session) sessionhistory.Row {
		var r sessionhistory.Row
		r.Session, r.Source = s, sessionhistory.SourceClaude
		return r
	}
	historyNode(t, h, "ma", nodeHistory([]sessionhistory.Row{
		row(proto.Session{SessionID: "long", Title: long, Status: proto.StatusOffline, UpdatedAt: now}),
		row(proto.Session{SessionID: "short", Title: "Fix the login page", Status: proto.StatusOffline, UpdatedAt: now.Add(-time.Hour)}),
	}))
	srv := httptest.NewServer(h.routes())
	t.Cleanup(srv.Close)
	code, body := historyGet(t, srv, h.cfg.Token, "/api/v1/sessions?include_inactive=1&since="+now.Add(-24*time.Hour).Format(time.RFC3339Nano))
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}
	var resp struct {
		Sessions []sessionhistory.Row `json:"sessions"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Sessions) != 2 {
		t.Fatalf("%d rows, want 2", len(resp.Sessions))
	}
	for _, r := range resp.Sessions {
		switch s := r.Session; s.SessionID {
		case "long":
			if n := utf8.RuneCountInString(s.Title); n != proto.TitleMax || !strings.HasPrefix(long, s.Title) {
				t.Errorf("a whole prompt as a title went out with %d runes, want its first %d", n, proto.TitleMax)
			}
		case "short":
			if s.Title != "Fix the login page" {
				t.Errorf("a title that fits changed: %q", s.Title)
			}
		}
	}
	if len(body) > 16<<10 {
		t.Errorf("a two-row page is %d bytes", len(body))
	}
}
