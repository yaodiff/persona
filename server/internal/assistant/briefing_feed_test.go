package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// The `brief` item of a routine's conversation (modules/briefing/feed.go): the apps' copy of the card, written whether or not the
// Lark bot is on.

func (e *briefRunEnv) briefItems() []FeedItem {
	e.t.Helper()
	items, _, err := e.s.feed(context.Background(), e.sess.SessionID, time.Time{})
	if err != nil {
		e.t.Fatal(err)
	}
	var out []FeedItem
	for _, it := range items {
		if it.Kind == feedBrief {
			out = append(out, it)
		}
	}
	return out
}

// With the Lark bot's switches off the routine still writes its facts into its conversation: the same rows as the card has,
// no Lark card sent. (The task to verify is planted with a card; a card-less ask is in acceptance_cardless_test.go.)
func TestBriefItemIsWrittenWithoutLark(t *testing.T) {
	e := newBriefRunEnvWith(t, true, func(e *briefRunEnv) { e.features(FeishuFeatures{}) })
	e.mirror()
	if n := len(e.cards()); n != 0 {
		t.Fatalf("%d Lark posts with the bot's switches off", n)
	}
	items := e.briefItems()
	if len(items) != 1 || items[0].Ref != "morning" {
		t.Fatalf("brief items: %+v", items)
	}
	var b briefBody
	if err := json.Unmarshal(items[0].Body, &b); err != nil {
		t.Fatal(err)
	}
	if len(b.Waiting) != 2 || b.Waiting[0].ID != "ix_old" || !strings.Contains(b.Waiting[0].Title, "旧的问题") || b.Waiting[0].WaitedS < 5400 {
		t.Errorf("waiting: %+v", b.Waiting)
	}
	if len(b.Accept) != 1 || b.Accept[0].ID != "todo_v1" || b.Accept[0].Title != "修登录崩溃" || b.Accept[0].Detail != "登录不再崩溃" || b.Accept[0].AskedAt.IsZero() {
		t.Errorf("accept: %+v", b.Accept)
	}
	if b.Title != "早报" || b.Hours != 12 || b.Done == nil || b.Failed == nil || b.WaitingUnknown {
		t.Errorf("body: %+v", b)
	}
	raw := string(items[0].Body)
	for _, key := range []string{`"done":[]`, `"failed":[]`} { // lists, never null: the apps read them as lists
		if !strings.Contains(raw, key) {
			t.Errorf("the body lacks %s: %s", key, raw)
		}
	}
}

// The waiting cards and the questions that can wait name the chat they came from, for the apps to open (#2320). A card from
// no chat names none, and the tasks to verify open their to-do instead: the fields are left out there, as a hub from before
// leaves them out everywhere.
func TestBriefRowsNameTheChatTheyCameFrom(t *testing.T) {
	e := newBriefRunEnvWith(t, true, func(e *briefRunEnv) {
		e.later("1", "todo_a", "新接口用 snake_case 还是 camelCase？", time.Hour)
		e.s.fleet.mu.Lock()
		e.s.fleet.interactions["ix_bare"] = &pending{ix: proto.Interaction{ID: "ix_bare", Kind: proto.IxQuestion,
			Questions: []proto.Question{{Question: "不在哪个会话里的问题"}}, CreatedAt: time.Now().Add(-time.Minute)}}
		e.s.fleet.mu.Unlock()
	})
	items := e.briefItems()
	var b briefBody
	if len(items) != 1 || json.Unmarshal(items[0].Body, &b) != nil {
		t.Fatalf("items: %+v", items)
	}
	chats := map[string][2]string{}
	for _, r := range b.Waiting {
		chats[r.ID] = [2]string{r.MachineID, r.SessionID}
	}
	if len(chats) != 3 || chats["ix_old"] != [2]string{"server", "ag_ix_old"} || chats["ix_new"] != [2]string{"server", "ag_ix_new"} ||
		chats["ix_bare"] != [2]string{} {
		t.Errorf("waiting: %+v", b.Waiting)
	}
	if len(b.Later) != 1 || b.Later[0].MachineID != "server" || b.Later[0].SessionID != "ag_1" {
		t.Errorf("later: %+v", b.Later)
	}
	var raw struct{ Waiting, Accept []map[string]any }
	if err := json.Unmarshal(items[0].Body, &raw); err != nil {
		t.Fatal(err)
	}
	for _, r := range append(raw.Waiting, raw.Accept...) {
		if r["id"] != "ix_bare" && r["id"] != "todo_v1" {
			continue
		}
		for _, key := range []string{"machine_id", "session_id"} {
			if _, ok := r[key]; ok {
				t.Errorf("%v has %s: %v", r["id"], key, r)
			}
		}
	}
}

// A routine without facts writes no item.
func TestBriefItemIsNotWrittenWithoutFacts(t *testing.T) {
	e := newBriefRunEnv(t, false)
	if items := e.briefItems(); len(items) != 0 {
		t.Fatalf("brief items: %+v", items)
	}
}

// The Accept button of the item: the round named by asked_at verifies the task, an earlier round or a missing asked_at does
// not, and a task already verified answers "already handled".
func TestBriefAcceptRoute(t *testing.T) {
	e := newBriefRunEnv(t, true)
	var b briefBody
	if err := json.Unmarshal(e.briefItems()[0].Body, &b); err != nil {
		t.Fatal(err)
	}
	post := func(body string) int {
		rec := httptest.NewRecorder()
		e.s.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/todos/todo_v1/accept", strings.NewReader(body)))
		return rec.Code
	}
	if code := post(`{}`); code != http.StatusBadRequest {
		t.Fatalf("no asked_at: %d", code)
	}
	if code := post(`{"asked_at":"2020-01-01T00:00:00Z"}`); code != http.StatusConflict || !askOf(e.s, "todo_v1").AcceptedAt.IsZero() {
		t.Fatalf("an earlier round: %d", code)
	}
	// The task is asked again (another round, #4853): the item's button is an earlier round's now.
	e.s.accept.mu.Lock()
	e.s.accept.asks["todo_v1"].AskedAt = b.Accept[0].AskedAt.Add(time.Minute)
	e.s.accept.mu.Unlock()
	old, _ := json.Marshal(b.Accept[0].AskedAt)
	if code := post(`{"asked_at":` + string(old) + `}`); code != http.StatusConflict || !askOf(e.s, "todo_v1").AcceptedAt.IsZero() {
		t.Fatalf("the button of the round before: %d", code)
	}
	asked, _ := json.Marshal(b.Accept[0].AskedAt.Add(time.Minute))
	if code := post(`{"asked_at":` + string(asked) + `}`); code != http.StatusOK || askOf(e.s, "todo_v1").AcceptedAt.IsZero() {
		t.Fatalf("the round that waits: %d", code)
	}
	if code := post(`{"asked_at":` + string(asked) + `}`); code != http.StatusConflict {
		t.Fatalf("pressed again: %d", code)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/v1/todos/todo_v1/accept", strings.NewReader(`{"asked_at":`+string(asked)+`}`))
	req.Header.Set(RoleHeader, "read")
	e.s.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("a read-only device: %d", rec.Code)
	}
}

// The retry of a run that failed adds no second item; the next day's run adds its own.
func TestBriefRetryAddsNoSecondItem(t *testing.T) {
	e := newBriefRunEnv(t, true)
	task, _ := taskByID(e.s, "morning")
	if _, err := e.s.runTask(task, time.Now(), time.Now(), false, true); err != nil {
		t.Fatal(err)
	}
	if n := len(e.briefItems()); n != 1 {
		t.Fatalf("%d items after the retry, want 1", n)
	}
	if _, err := e.s.runTask(task, time.Now(), time.Now(), false, false); err != nil {
		t.Fatal(err)
	}
	if n := len(e.briefItems()); n != 2 {
		t.Fatalf("%d items after another run, want 2", n)
	}
}

// A hub that just restarted has not read the asks yet: the Accept button of an old item still finds the round it names.
func TestBriefAcceptAfterARestart(t *testing.T) {
	e := newBriefEnv(t)
	asked := time.Now().Add(-30 * time.Hour).UTC()
	e.s.todos.todos["todo_v1"] = &Todo{ID: "todo_v1", Title: "修登录崩溃", Origin: "conv1"}
	if err := e.st.PutSetting(context.Background(), acceptanceKey, map[string]*acceptAsk{"todo_v1": {MessageID: "om_v1", Messages: []string{"om_v1"}, AskedAt: asked}}); err != nil {
		t.Fatal(err)
	}
	e.s.accept.loaded, e.s.accept.asks = false, nil
	if ok, _ := e.s.acceptReviewRound(context.Background(), "todo_v1", asked.Format(timeKeyLayout)); !ok.taken {
		t.Fatal("the button of the round that waits was refused before the asks were read")
	}
}
