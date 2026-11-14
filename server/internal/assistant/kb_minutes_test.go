package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/kb"
)

// minutesModel answers every knowledge-base call with reply.
type minutesModel struct{ reply string }

func (m minutesModel) JSONMax(_ context.Context, _, _ string, _ int, out any) error {
	return json.Unmarshal([]byte(m.reply), out)
}

// A meeting recording through the phone's routes: its minutes are written, pushed once, read back by the detail (the
// summary card's overview too, #6563), the reconciliation and kb_get / kb_list; confirmed action items become to-dos
// once, however often the app asks, the overview kept as it was.
func TestMinutesHTTP(t *testing.T) {
	f := newFakeHub()
	// The push also goes to the Lark bot and the channels from goroutines of their own: they must not log into a
	// finished test.
	s := kbServiceOn(t, f, newKBStore(), t.TempDir(), func(string, ...any) {})
	e := kbEng(t, s)
	model := minutesModel{reply: `{"title":"发布评审","headline":"导出先保留上限","conclusions":[{"text":"先**保留**上限","line":2}],` +
		`"topics":[{"title":"导出","icon":"progress","points":[{"text":"导出**丢条目**","line":1}],"decisions":[{"text":"先保留上限","line":2}]}],` +
		`"actions":[{"who":"Bo","what":"补上回归测试","by":"周五","due_date":"2026-11-13","line":3},{"what":"发邮件","line":3}]}`}
	e.Model, e.ModelFor = model, func(string) kb.Model { return model }
	server := httptest.NewServer(s)
	defer server.Close()
	request := func(method, path, role string, body any, want int) string {
		t.Helper()
		var data []byte
		if body != nil {
			data, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, server.URL+path, bytes.NewReader(data))
		if role != "" {
			req.Header.Set(RoleHeader, role)
		}
		resp, err := server.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != want {
			t.Fatalf("%s %s: %d %s, want %d", method, path, resp.StatusCode, raw, want)
		}
		return string(raw)
	}
	const id = "22222222-2222-4222-8222-222222222222"
	start := time.Date(2026, 11, 10, 14, 0, 0, 0, time.UTC)
	upload := map[string]any{"revision": 1, "started_at": start, "device": "phone", "complete": false, "kind": "meeting",
		"calendar": map[string]any{"title": "发布评审", "attendees": []string{"Ada", "Bo"}, "id": "ignored"},
		"segments": []map[string]any{{"start": 0, "end": 9, "text": "导出会丢掉大条目"}, {"start": 10, "end": 19, "text": "先保留上限"}, {"start": 20, "end": 29, "text": "Bo 补上回归测试"}}}
	path := "/api/v1/recordings/" + id
	request("PUT", path, "", upload, 200)
	request("POST", path+"/minutes", "", nil, 409) // the transcript is not complete
	upload["revision"], upload["complete"] = 2, true
	request("PUT", path, "", upload, 200)
	if !strings.Contains(request("GET", "/api/v1/recordings", "", nil, 200), `"minutes_state":"pending"`) {
		t.Fatal("reconciliation lacks the pending minutes")
	}
	if n := e.WriteMinutes(context.Background(), 2); n != 1 {
		t.Fatalf("wrote %d", n)
	}
	pushes := f.pushed()
	// It names the recording, so an app that knows it opens the meeting's thread (#3862 phase 6).
	if len(pushes) != 1 || pushes[0].ID != "ntf_minutes_"+id+"_1" || pushes[0].Kind != NotifyPush || !strings.Contains(pushes[0].Body, "发布评审") ||
		pushes[0].RecordingID != id {
		t.Fatalf("pushes %+v", pushes)
	}
	var detail struct {
		Detail struct {
			Meta    map[string]any `json:"meta"`
			Minutes kb.Minutes     `json:"minutes"`
		} `json:"detail"`
	}
	raw := request("GET", "/api/v1/kb/recordings/"+id, "", nil, 200)
	_ = json.Unmarshal([]byte(raw), &detail)
	if detail.Detail.Meta["minutes_state"] != "ready" || detail.Detail.Meta["kind"] != "meeting" || detail.Detail.Minutes.Version != 1 ||
		len(detail.Detail.Minutes.Actions) != 2 || detail.Detail.Minutes.Actions[0].At != 20 {
		t.Fatalf("detail %+v", detail.Detail)
	}
	// The summary card rides along: the points and decisions are its bullets, plain, their bold places in strong.
	card := detail.Detail.Minutes.Overview
	if card == nil || card.Headline != "导出先保留上限" || len(card.Conclusions) != 1 || len(card.Groups) != 1 || len(card.Groups[0].Topics) != 1 ||
		len(card.Groups[0].Topics[0].Bullets) != 2 || card.Groups[0].Topics[0].Bullets[0].Text != "导出丢条目" ||
		strings.Join(card.Groups[0].Topics[0].Bullets[0].Strong, ",") != "丢条目" || !card.Groups[0].Topics[0].Bullets[1].Decision ||
		detail.Detail.Minutes.Points[0].Text != "导出丢条目" || !strings.Contains(raw, `"strong":["丢条目"]`) {
		t.Fatalf("overview %+v in %s", card, raw)
	}

	// Confirming: one to-do per action, the same one however often; the minutes say which.
	todosPath := path + "/minutes/todos"
	var out struct {
		Todos   []Todo     `json:"todos"`
		Minutes kb.Minutes `json:"minutes"`
	}
	_ = json.Unmarshal([]byte(request("POST", todosPath, "", map[string]any{"version": 1, "actions": []string{"a1"}}, 200)), &out)
	if len(out.Todos) != 1 || out.Todos[0].Title != "Bo: 补上回归测试" || out.Todos[0].DueDate != "2026-11-13" ||
		!strings.Contains(out.Todos[0].Notes, "发布评审") || out.Minutes.Actions[0].TodoID != out.Todos[0].ID || out.Minutes.Actions[1].TodoID != "" ||
		out.Minutes.Overview == nil || out.Minutes.Markdown != detail.Detail.Minutes.Markdown {
		t.Fatalf("confirmed %+v", out)
	}
	first := out.Todos[0].ID
	_ = json.Unmarshal([]byte(request("POST", todosPath, "", map[string]any{"version": 1, "actions": []string{"a1", "a2", "a1"}}, 200)), &out)
	if len(out.Todos) != 2 || out.Todos[0].ID != first || out.Minutes.Actions[1].TodoID != out.Todos[1].ID {
		t.Fatalf("again %+v", out)
	}
	mine := 0
	s.todos.mu.RLock()
	for tid := range s.todos.todos {
		if strings.HasPrefix(tid, "todo_mtg_") {
			mine++
		}
	}
	s.todos.mu.RUnlock()
	if mine != 2 {
		t.Fatalf("%d to-dos from the minutes, want 2", mine)
	}
	if body := request("POST", todosPath, "", map[string]any{"version": 7, "actions": []string{"a1"}}, 409); !strings.Contains(body, codeMinutesStale) {
		t.Fatalf("stale: %s", body)
	}
	request("POST", todosPath, "", map[string]any{"version": 1, "actions": []string{"a9"}}, 404)
	request("POST", todosPath, "", map[string]any{"version": 1, "actions": []string{}}, 400)
	request("POST", todosPath, "answer", map[string]any{"version": 1, "actions": []string{"a1"}}, 403)
	request("POST", "/api/v1/recordings/33333333-3333-4333-8333-333333333333/minutes/todos", "", map[string]any{"version": 1, "actions": []string{"a1"}}, 404)

	// The assistant reads them: kb_get prints the minutes before the transcript, kb_list marks the recording.
	got, err := s.toolKB(context.Background(), "kb_get", json.RawMessage(`{"kind":"recordings","id":"`+id+`"}`))
	if err != nil || !strings.Contains(got, "minutes.md:\n# 发布评审") || !strings.Contains(got, "## 核心结论") || !strings.Contains(got, "已决定：先保留上限") ||
		!strings.Contains(got, "transcript.md:") ||
		strings.Index(got, "minutes.md:") > strings.Index(got, "transcript.md:") {
		t.Fatalf("kb_get: %v\n%s", err, got)
	}
	if list, _ := s.toolKB(context.Background(), "kb_list", json.RawMessage(`{"kind":"recordings"}`)); !strings.Contains(list, " minutes") {
		t.Fatalf("kb_list: %s", list)
	}

	// Regenerate: pending again (the old minutes stay), a new version and a second push once written.
	if body := request("POST", path+"/minutes", "", nil, 202); !strings.Contains(body, `"state":"pending"`) {
		t.Fatalf("regenerate: %s", body)
	}
	e.WriteMinutes(context.Background(), 2)
	if pushes := f.pushed(); len(pushes) != 2 || pushes[1].ID != "ntf_minutes_"+id+"_2" {
		t.Fatalf("pushes %+v", pushes)
	}
	request("POST", "/api/v1/recordings/33333333-3333-4333-8333-333333333333/minutes", "", nil, 404)
	// Deleting the recording takes the minutes with it.
	request("DELETE", path, "", nil, 200)
	request("GET", "/api/v1/kb/recordings/"+id, "", nil, 404)
}

// Past meetings through the kb tools: kb_list dates the calendar items, kb_get on one names the recordings made at it,
// kb_search kind=recordings finds the recording by the calendar's title, and kb_get on the recording says after the
// minutes where each action item stands now: a suggestion only, or its to-do open (and when due), done, cancelled or
// deleted.
func TestMinutesPastMeetingTools(t *testing.T) {
	f := newFakeHub()
	s := kbServiceOn(t, f, newKBStore(), t.TempDir(), func(string, ...any) {}) // the push's goroutines log late
	setSettings(t, s, func(v *Settings) { v.Timezone = "Asia/Shanghai" })
	loc, _ := time.LoadLocation("Asia/Shanghai")
	e := kbEng(t, s)
	ctx := context.Background()
	start := time.Now().In(loc).Truncate(time.Hour).AddDate(0, 0, -3)
	due := start.AddDate(0, 0, 2).Format("2006-01-02")
	model := minutesModel{reply: `{"title":"Design review","decisions":[{"text":"Keep the cap for this release.","line":2}],"actions":[` +
		`{"who":"Bo","what":"Add the regression test","by":"Friday","due_date":"` + due + `","line":3},{"who":"Ada","what":"Fix the export","line":3},` +
		`{"what":"Mail the notes","line":3},{"what":"Book the room","line":3},{"what":"Draft the plan","line":3}],` +
		`"open":[{"text":"Per entry or per recording?","line":3}]}`}
	e.Model, e.ModelFor = model, func(string) kb.Model { return model }
	if err := e.Store.PutItem(&kb.Item{Kind: kb.KindMeeting, ID: "m1", Title: "Design review", At: start, URL: "https://meet.example.com/m1"}); err != nil {
		t.Fatal(err)
	}
	const id = "44444444-4444-4444-8444-444444444444"
	up := kb.RecordingUpload{Revision: 1, StartedAt: start, Duration: 30, Device: "phone", Complete: true, Kind: kb.RecordingMeeting,
		Segments: []kb.RecordingSegment{{Start: 0, End: 9, Text: "Ada: the export drops large entries"},
			{Start: 10, End: 19, Text: "we keep the cap"}, {Start: 20, End: 29, Text: "Bo adds the regression test by Friday"}}}
	if st, err := e.PutRecording(id, up, start.Add(time.Minute)); err != nil || st.Status != kb.RecordingStored {
		t.Fatalf("put: %v %v", st, err)
	}
	if n := e.WriteMinutes(ctx, 2); n != 1 {
		t.Fatalf("wrote %d", n)
	}

	clock := start.Format("2006-01-02 15:04")
	if out, err := runTool(s, "c1", "kb_list", map[string]any{"kind": "meetings"}); err != nil ||
		out != "- m1 · Design review [- 相关0]  "+clock+" https://meet.example.com/m1\n" {
		t.Fatalf("kb_list meetings: %q %v", out, err)
	}
	if out, err := runTool(s, "c1", "kb_get", map[string]any{"kind": "meetings", "id": "m1"}); err != nil ||
		!strings.HasSuffix(out, "\nRecorded at it (kb_get kind=recordings for the minutes and the transcript):\n- "+id+" · Design review "+clock+" 0min phone minutes\n") {
		t.Fatalf("kb_get meeting:\n%s (%v)", out, err)
	}
	out, err := runTool(s, "c1", "kb_search", map[string]any{"query": "design REVIEW", "kind": "recordings"})
	if err != nil || !strings.HasPrefix(out, "recordings/"+id+"/") {
		t.Fatalf("kb_search kind=recordings: %q %v", out, err)
	}
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if !strings.HasPrefix(l, "recordings/") {
			t.Fatalf("kb_search kind=recordings found %q", l)
		}
	}
	if _, err := runTool(s, "c1", "kb_search", map[string]any{"query": "design review", "kind": "minutes"}); err == nil ||
		!strings.Contains(err.Error(), "one of projects, chats") {
		t.Fatalf("kb_search, an unknown kind: %v", err)
	}

	// Confirmed: a1 stays open (renamed), a2 is done, a3 deleted, a4 cancelled; a5 is never confirmed.
	todos, _, err := s.minutesTodos(ctx, e, id, 1, []string{"a1", "a2", "a3", "a4"})
	if err != nil || len(todos) != 4 {
		t.Fatalf("confirm: %+v %v", todos, err)
	}
	renamed, done := "Bo: Add the regression test for exports", true
	if _, err := s.editTodo(ctx, todos[0].ID, db.TodoEdit{Title: &renamed}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.editTodo(ctx, todos[1].ID, db.TodoEdit{Done: &done}); err != nil {
		t.Fatal(err)
	}
	if err := s.deleteTodo(ctx, todos[2].ID); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	s.todos.mu.Lock()
	s.todos.todos[todos[3].ID].CancelledAt, s.todos.todos[todos[3].ID].DoneAt = &now, &now
	s.todos.mu.Unlock()
	dueDay, _ := time.Parse("2006-01-02", due)
	out, err = runTool(s, "c1", "kb_get", map[string]any{"kind": "recordings", "id": id})
	want := "\nAction items as they stand (one becomes a to-do only when the user confirms it):\n" +
		"- a1 Bo: Add the regression test (by Friday, " + due + ") → to-do " + todos[0].ID + ` "` + renamed + `" open · due ` + dueDay.Format("2006-01-02 Mon") + " (the whole day) (" +
		"- a2 Ada: Fix the export → to-do " + todos[1].ID + " done " + todoNow(s, todos[1].ID).DoneAt.In(loc).Format("2006-01-02") + "\n" +
		"- a3 Mail the notes → its to-do " + todos[2].ID + " was deleted\n" +
		"- a4 Book the room → to-do " + todos[3].ID + " cancelled\n" +
		"- a5 Draft the plan → suggested only, not a to-do\n"
	if err != nil || !strings.Contains(out, "minutes.md:\n# Design review") {
		t.Fatalf("kb_get recording:\n%s (%v)", out, err)
	}
	actions, rest, _ := strings.Cut(out, "\nAction items")
	if !strings.Contains(actions, "minutes.md:") || !strings.Contains(rest, "transcript.md:") || strings.Contains(actions, "transcript.md:") {
		t.Fatalf("not minutes, action items, transcript in that order:\n%s", out)
	}
	open, closed, _ := strings.Cut(want, " (the whole day) (")
	if !strings.Contains(out, open+" (the whole day) (") || !strings.Contains(out, ")\n"+closed) {
		t.Fatalf("kb_get recording, the action items:\n%s\nwant\n%s", out, want)
	}
}
