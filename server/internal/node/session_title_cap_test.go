package node

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/sessionhistory"
)

// longTitle is a synthetic opening prompt stored as a title: thousands of multibyte runes, so a cut by bytes would split
// one.
var longTitle = strings.Repeat("改一下这个函数，并补上测试。", 2000)

// longReply is a synthetic whole reply reported as last_text, ending with a question to the user.
var longReply = strings.Repeat("The change is in and the tests pass. ", 300) + "Which database should the new table go in?"

// checkCutTitle fails unless title is longTitle cut to proto.TitleMax runes on a rune boundary.
func checkCutTitle(t *testing.T, where, title string) {
	t.Helper()
	if n := utf8.RuneCountInString(title); n != proto.TitleMax || !utf8.ValidString(title) || !strings.HasPrefix(longTitle, title) {
		t.Errorf("%s: title of %d runes (%d bytes), valid %v; want longTitle's first %d runes", where, n, len(title),
			utf8.ValidString(title), proto.TitleMax)
	}
}

// A session's title leaves the node no longer than proto.TitleMax runes, whatever its source stored (#16343): a
// runtime's row whose title is a whole opening prompt is listed with it cut on a rune boundary, in the list and in
// sessions.changed; a title that fits is listed as it is. Its long last_text is cut as the node's own rows' are, the
// question at its end read off the whole reply first, so the manager still knows the session waits on the user.
func TestListedRowsCarryBoundedTitlesAndPreviews(t *testing.T) {
	n, hub := runtimeNode(t)
	host := dialRuntime(t, n, dshHello())
	_ = host.peer.Notify("runtime.sessions", map[string]any{"sessions": []map[string]any{
		{"session_id": "long", "status": "done", "title": longTitle, "last_text": longReply},
		{"session_id": "short", "status": "done", "title": "Fix the login page", "last_text": "All set."},
	}})
	waitFor(t, "both runtime rows listed", func() bool {
		rows := listed(t, n)
		return rows["long"].SessionID != "" && rows["short"].SessionID != ""
	})
	rows := listed(t, n)
	long, short := rows["long"], rows["short"]
	checkCutTitle(t, "sessions.list", long.Title)
	if short.Title != "Fix the login page" {
		t.Errorf("a title that fits is listed as it is: %q", short.Title)
	}
	if got := utf8.RuneCountInString(long.LastText); got > maxRuntimeLastText+1 || !strings.HasSuffix(long.LastText, "…") ||
		!strings.HasPrefix(long.LastText, "The change is in") {
		t.Errorf("a whole reply as last_text is cut to a preview: %d runes, %q…", got, long.LastText[:min(40, len(long.LastText))])
	}
	if !long.AsksRead || !strings.Contains(long.Asks, "Which database") {
		t.Errorf("the question a long reply ends with is read off it before the cut: asks %q, asks_read %v", long.Asks, long.AsksRead)
	}
	if short.LastText != "All set." || short.AsksRead {
		t.Errorf("a short last_text is left as it is, for the manager to read: %q, asks_read %v", short.LastText, short.AsksRead)
	}

	n.publish(n.sessionList(t.Context(), false))
	waitFor(t, "sessions.changed with the rows", func() bool {
		hub.mu.Lock()
		defer hub.mu.Unlock()
		for _, list := range hub.lists {
			for _, s := range list {
				if s.SessionID == "long" {
					return true
				}
			}
		}
		return false
	})
	hub.mu.Lock()
	defer hub.mu.Unlock()
	for _, list := range hub.lists {
		for _, s := range list {
			if utf8.RuneCountInString(s.Title) > proto.TitleMax {
				t.Errorf("sessions.changed carried %s with a title of %d runes", s.SessionID, utf8.RuneCountInString(s.Title))
			}
		}
	}
}

// A history page's rows carry the same bounded titles and previews as the live list's (#16343).
func TestSessionsHistoryRowsCarryBoundedTitles(t *testing.T) {
	n, _ := runtimeNode(t)
	now := time.Now().UTC().Add(-time.Minute)
	hello := dshHello()
	hello["caps"].(map[string]any)["history"] = true
	host := dialRuntime(t, n, hello)
	host.mu.Lock()
	host.answer = historyRuntime([]map[string]any{{"session_id": "long", "status": "offline", "title": longTitle,
		"last_text": longReply, "updated_at": now.Format(time.RFC3339Nano)}})
	host.mu.Unlock()
	waitFor(t, "runtime ready", func() bool { return len(n.runtimes.ready()) == 1 })
	ans := historyCall(t, n, map[string]any{"since": now.Add(-time.Hour).Format(time.RFC3339Nano), "source": sessionhistory.SourceRuntime})
	if len(ans.Rows) != 1 {
		t.Fatalf("%d rows, want the one", len(ans.Rows))
	}
	row := ans.Rows[0].Session
	checkCutTitle(t, "sessions.history", row.Title)
	if utf8.RuneCountInString(row.LastText) > maxRuntimeLastText+1 {
		t.Errorf("history last_text of %d runes", utf8.RuneCountInString(row.LastText))
	}
}
