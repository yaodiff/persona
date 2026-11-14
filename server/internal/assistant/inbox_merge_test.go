package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func mergeItems(h *inboxHub) {
	now := time.Now()
	a := mailItem("a", "ana@example.com", "Ana")
	a.Title, a.Tier, a.Count, a.UpdatedAt, a.Last = "Budget numbers", TierFYI, 2, now.Add(-2*time.Hour), "1"
	b := mailItem("b", "ana@example.com", "Ana")
	b.Title, b.Tier, b.Count, b.UpdatedAt, b.Last = "Budget deadline", TierAction, 1, now.Add(-time.Hour), "1"
	c := mailItem("c", "li@example.com", "Li")
	c.Title, c.Tier, c.Count, c.UpdatedAt, c.Last = "Release notes", TierNoise, 1, now.Add(-3*time.Hour), "1"
	for _, it := range []*InboxItem{a, b, c} {
		h.inbox.items[it.ID] = it
	}
}

func listedIDs(t *testing.T, h *inboxHub) string {
	t.Helper()
	_, out := inboxCall(t, h.routes(), "GET", "/api/v1/inbox", "")
	var ids []string
	for _, v := range out["items"].([]any) {
		ids = append(ids, v.(map[string]any)["id"].(string))
	}
	return strings.Join(sortedStrings(ids), ",")
}

func sortedStrings(in []string) []string {
	for i := range in {
		for j := i + 1; j < len(in); j++ {
			if in[j] < in[i] {
				in[i], in[j] = in[j], in[i]
			}
		}
	}
	return in
}

// Merge: the newest row survives with the highest tier and the sum of the counts; the others leave the list; a link naming
// one of them opens the survivor; a message into a member reopens the survivor; Undo gives the rows back.
func TestInboxMergeAndUndo(t *testing.T) {
	h := muteHub(t)
	mux := h.routes()
	mergeItems(h)

	if code, _ := inboxCall(t, mux, "POST", "/api/v1/inbox/merge", `{"items":[{"id":"a"}]}`); code != 400 {
		t.Fatalf("one row: %d", code)
	}
	code, out := inboxCall(t, mux, "POST", "/api/v1/inbox/merge", `{"items":[{"id":"a"},{"id":"b"},{"id":"c"},{"id":"gone"}]}`)
	if code != 200 {
		t.Fatalf("merge: %d %v", code, out)
	}
	item := out["item"].(map[string]any)
	if item["id"] != "b" || item["tier"] != TierAction || item["count"].(float64) != 4 || len(item["merged_from"].([]any)) != 2 {
		t.Fatalf("merged row: %v", item)
	}
	if fmt.Sprint(out["skipped"]) != "[map[id:gone reason:missing]]" {
		t.Fatalf("skipped: %v", out["skipped"])
	}
	if got := listedIDs(t, h); got != "b" {
		t.Fatalf("list after merge: %q", got)
	}
	if code, o := inboxCall(t, mux, "GET", "/api/v1/inbox/a", ""); code != 200 || o["id"] != "b" {
		t.Fatalf("a link to a member: %d %v", code, o["id"])
	}
	if h.inbox.items["a"].open() {
		t.Fatal("a member counts as open")
	}
	if last := item["chain"].([]any); !strings.Contains(fmt.Sprint(last[len(last)-1]), "3") {
		t.Fatalf("chain ends with %v", last[len(last)-1])
	}

	// A message into a member (its own source collects it) reopens and updates the survivor.
	h.inbox.mu.Lock()
	m := h.inbox.items["c"]
	m.UpdatedAt, m.Preview, m.Tier = time.Now(), "one more", TierAction
	sv := h.inbox.items["b"]
	sv.done(time.Now().Add(-time.Minute), "user")
	changed := h.inbox.followMerged([]*InboxItem{m}, time.Now())
	h.inbox.mu.Unlock()
	if len(changed) != 2 || !sv.open() || sv.Preview != "one more" || sv.Count != 5 {
		t.Fatalf("follow: %d %v %q %d", len(changed), sv.open(), sv.Preview, sv.Count)
	}

	code, out = inboxCall(t, mux, "POST", "/api/v1/inbox/merge/undo", `{"id":"b"}`)
	if code != 200 || listedIDs(t, h) != "a,b,c" {
		t.Fatalf("undo: %d %v list %q", code, out, listedIDs(t, h))
	}
	if got := h.inbox.items["b"]; got.Tier != TierAction || got.MergedFrom != nil {
		t.Fatalf("survivor after undo: %+v", got)
	}
	if code, _ := inboxCall(t, mux, "POST", "/api/v1/inbox/merge/undo", `{"id":"b"}`); code != 404 {
		t.Fatalf("undo twice: %d", code)
	}
}

// A row changed since the list, or filed, is left out; with fewer than two left nothing merges.
func TestInboxMergeSkipsChanged(t *testing.T) {
	h := muteHub(t)
	mux := h.routes()
	mergeItems(h)
	h.inbox.items["c"].done(time.Now(), "user")
	code, out := inboxCall(t, mux, "POST", "/api/v1/inbox/merge", `{"items":[{"id":"a","version":"stale"},{"id":"b"},{"id":"c"}]}`)
	if code != 409 || h.inbox.items["b"].MergedFrom != nil {
		t.Fatalf("%d %v", code, out)
	}
}

// Add to to-dos: one to-do each with the shared due day, the rows filed as turned into them, Undo deletes and reopens.
func TestInboxTodoAndUndo(t *testing.T) {
	h := muteHub(t)
	mux := h.routes()
	mergeItems(h)
	code, out := inboxCall(t, mux, "POST", "/api/v1/inbox/todo", `{"items":[{"id":"a"},{"id":"b","title":"Send Ana the numbers"},{"id":"nope"}],"due_date":"2026-11-15"}`)
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	made := out["todos"].([]any)
	if len(made) != 2 || len(out["changed"].([]any)) != 2 {
		t.Fatalf("made %v", out)
	}
	for _, m := range made {
		td := m.(map[string]any)["todo"].(map[string]any)
		if td["due_date"] != "2026-11-15" || td["inbox_item"] == "" {
			t.Fatalf("todo %v", td)
		}
		if m.(map[string]any)["item"] == "b" && td["title"] != "Send Ana the numbers" {
			t.Fatalf("title %v", td["title"])
		}
	}
	a := h.inbox.items["a"]
	if a.open() || a.DoneBy != doneByTodo || a.Chain[len(a.Chain)-1].Link == nil || a.Chain[len(a.Chain)-1].Link.Type != LinkTodo {
		t.Fatalf("row a: %+v", a)
	}
	if _, ok := h.todoNow(made[0].(map[string]any)["todo"].(map[string]any)["id"].(string)); !ok {
		t.Fatal("no to-do")
	}
	code, out = inboxCall(t, mux, "POST", "/api/v1/inbox/todo/undo", fmt.Sprintf(`{"items":%s}`, rawJSON(out["undo"].(map[string]any)["items"])))
	if code != 200 || !h.inbox.items["a"].open() || !h.inbox.items["b"].open() {
		t.Fatalf("undo %d %v", code, out)
	}
	if _, ok := h.todoNow(made[0].(map[string]any)["todo"].(map[string]any)["id"].(string)); ok {
		t.Fatal("to-do stays after Undo")
	}
}

// A to-do suggestion: it needs a readable due, only the processing conversation may make one, and confirming adds the
// to-do (the user's edited due wins) and files the row.
func TestSuggestTodoValidation(t *testing.T) {
	for _, c := range []struct {
		in suggestIn
		ok bool
	}{
		{suggestIn{Kind: "todo", Text: "Send the numbers", Due: "2026-11-15"}, true},
		{suggestIn{Kind: "todo", Text: "Send the numbers", Due: "2026-11-15T18:00:00+08:00"}, true},
		{suggestIn{Kind: "todo", Text: "Send the numbers"}, true},
		{suggestIn{Kind: "todo", Text: "Send the numbers", Due: "Friday"}, false},
		{suggestIn{Kind: "reply", Text: "ok", Due: "2026-11-15"}, false},
		{suggestIn{Kind: "todo", Text: "x", Item: "a"}, false},
	} {
		if err := c.in.check(); (err == nil) != c.ok {
			t.Errorf("%+v: %v", c.in, err)
		}
	}
}

func TestConfirmTodoSuggestion(t *testing.T) {
	h := muteHub(t)
	mergeItems(h)
	it := h.inbox.items["b"]
	it.Agent = &InboxAgent{State: AgentDrafted}
	it.Suggestion = &InboxSuggestion{Version: 1, Kind: SuggestTodo, Text: "Send Ana the numbers", Due: "2026-11-15T18:00:00+08:00", At: time.Now()}
	edited := "2026-11-16"
	out, err := h.actOnSuggestion(t.Context(), "b", suggestionReq{Version: 1, Action: suggestConfirm, Due: &edited}, true)
	if err != nil || out.DoneBy != doneByTodo || out.Suggestion != nil {
		t.Fatalf("%v %+v", err, out)
	}
	last := out.Chain[len(out.Chain)-1]
	td, ok := h.todoNow(last.Link.ID)
	if !ok || td.Title != "Send Ana the numbers" || td.DueDate != "2026-11-16" || td.InboxItem != "b" {
		t.Fatalf("to-do %+v %v", td, ok)
	}
	if _, err := h.actOnSuggestion(t.Context(), "a", suggestionReq{Version: 0, Action: suggestConfirm}, false); err == nil {
		t.Fatal("answer-only device confirmed")
	}
}

// A model's verdict on a merged row never lowers it below the rows it stands for: its own tier before the merge, and its
// members' now (#7230).
func TestMergedTierSurvivesTriage(t *testing.T) {
	ctx := context.Background()
	merge := func(h *inboxHub, ids string) {
		t.Helper()
		if code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/merge", `{"items":[`+ids+`]}`); code != 200 {
			t.Fatalf("merge: %d %v", code, out)
		}
	}
	t.Run("the survivor's own tier", func(t *testing.T) {
		h := muteHub(t)
		h.in.setModel(fakeModel(t, `{"tier":"noise","summary":"all of it","reason":"nothing to do"}`))
		mergeItems(h)
		merge(h, `{"id":"a"},{"id":"b"},{"id":"c"}`) // b is the newest and Needs you
		sv := h.inbox.items["b"]
		if sv.TriagedAt != "" {
			t.Fatalf("a merged row is judged again: %q", sv.TriagedAt)
		}
		h.saveWrites(ctx, []*inboxWrite{h.triage(ctx, sv)}, "its triage")
		if sv.Tier != TierAction || sv.Summary != "all of it" || sv.TriagedAt != sv.Last {
			t.Fatalf("after triage: tier %q summary %q triaged %q", sv.Tier, sv.Summary, sv.TriagedAt)
		}
	})
	t.Run("a member's tier that moved", func(t *testing.T) {
		h := muteHub(t)
		h.in.setModel(fakeModel(t, `{"tier":"noise","summary":"s","reason":"r"}`))
		mergeItems(h)
		merge(h, `{"id":"a"},{"id":"c"}`) // a (FYI) survives c (collapsed)
		sv := h.inbox.items["a"]
		if sv.Tier != TierFYI {
			t.Fatalf("merged tier %q", sv.Tier)
		}
		h.inbox.mu.Lock()
		c := h.inbox.items["c"]
		c.Tier, c.Reason, c.Urgent = TierAction, "asked for a time", true // its later verdict
		h.inbox.mu.Unlock()
		h.saveWrites(ctx, []*inboxWrite{h.triage(ctx, sv)}, "its triage")
		if sv.Tier != TierAction || sv.Reason != "asked for a time" || !sv.Urgent {
			t.Fatalf("after triage: %q %q urgent=%v", sv.Tier, sv.Reason, sv.Urgent)
		}
	})
	t.Run("a verdict above them stands", func(t *testing.T) {
		h := muteHub(t)
		h.in.setModel(fakeModel(t, `{"tier":"action","summary":"s","reason":"r"}`))
		mergeItems(h)
		merge(h, `{"id":"a"},{"id":"c"}`)
		sv := h.inbox.items["a"]
		h.saveWrites(ctx, []*inboxWrite{h.triage(ctx, sv)}, "its triage")
		if sv.Tier != TierAction || sv.Reason != "r" {
			t.Fatalf("after triage: %q %q", sv.Tier, sv.Reason)
		}
	})
}

// The model writes one title for a merged row, in the background, from every row: the row keeps the newest row's title
// until it answers (and when it cannot); Undo puts the old title back; a title the user wrote stays (#7229).
func TestMergedRowTitle(t *testing.T) {
	merge := func(h *inboxHub, ids string) map[string]any {
		t.Helper()
		code, out := inboxCall(t, h.routes(), "POST", "/api/v1/inbox/merge", `{"items":[`+ids+`]}`)
		if code != 200 {
			t.Fatalf("merge: %d %v", code, out)
		}
		return out
	}
	t.Run("written, and undone", func(t *testing.T) {
		h := muteHub(t)
		model, calls := captureModel(t, `{"title":"Budget for the quarter"}`)
		h.in.setModel(model)
		mergeItems(h)
		out := merge(h, `{"id":"a"},{"id":"b"},{"id":"c"}`)
		if got := out["item"].(map[string]any)["title"]; got != "Budget deadline" {
			t.Fatalf("the reply carries the newest title before the model answers: %v", got)
		}
		h.inbox.titling.Wait()
		sv := h.inbox.items["b"]
		if sv.Title != "Budget for the quarter" || sv.TitleBy != TitleByMerged || sv.PreMergeTitle != "Budget deadline" {
			t.Fatalf("after the model: %q by %q (was %q)", sv.Title, sv.TitleBy, sv.PreMergeTitle)
		}
		call := onlyCall(t, calls())
		for _, want := range []string{"Budget numbers", "Budget deadline", "Release notes"} {
			if !strings.Contains(call.User, want) {
				t.Errorf("the model was not shown %q:\n%s", want, call.User)
			}
		}
		if _, l := inboxCall(t, h.routes(), "GET", "/api/v1/inbox/b", ""); l["title"] != "Budget for the quarter" || l["pre_merge_title"] != nil || l["merge_title"] != nil {
			t.Fatalf("listed: %v", l)
		}
		inboxCall(t, h.routes(), "POST", "/api/v1/inbox/merge/undo", `{"id":"b"}`)
		if sv.Title != "Budget deadline" || sv.TitleBy != "" || sv.MergeTitle != "" {
			t.Fatalf("after undo: %q by %q", sv.Title, sv.TitleBy)
		}
	})
	t.Run("the user's title stays, and a failure leaves the newest", func(t *testing.T) {
		h := muteHub(t)
		h.in.setModel(fakeModel(t, `{"title":"Something else"}`))
		mergeItems(h)
		h.inbox.items["b"].TitleBy = TitleByUser
		merge(h, `{"id":"a"},{"id":"b"}`)
		h.inbox.titling.Wait()
		if sv := h.inbox.items["b"]; sv.Title != "Budget deadline" || sv.TitleBy != TitleByUser {
			t.Fatalf("the user's title: %q by %q", sv.Title, sv.TitleBy)
		}
		h2 := muteHub(t)
		h2.in.setModel(fakeModel(t, `not json`))
		mergeItems(h2)
		merge(h2, `{"id":"a"},{"id":"b"}`)
		h2.inbox.titling.Wait()
		if sv := h2.inbox.items["b"]; sv.Title != "Budget deadline" || sv.TitleBy == TitleByMerged {
			t.Fatalf("after a failed answer: %q by %q", sv.Title, sv.TitleBy)
		}
	})
	t.Run("grouping leaves it alone, an undone merge drops a late answer", func(t *testing.T) {
		h := muteHub(t)
		h.in.setModel(fakeModel(t, `{"title":"One matter"}`))
		mergeItems(h)
		merge(h, `{"id":"a"},{"id":"b"}`)
		h.inbox.titling.Wait()
		if !keepsWords(h.inbox.items["b"]) {
			t.Fatal("grouping would rewrite the merged title")
		}
		// a late answer: the row was undone meanwhile
		h.inbox.mu.Lock()
		sv := h.inbox.items["b"]
		h.inbox.unmerge(sv, time.Now())
		h.inbox.mu.Unlock()
		h.rewriteMergedTitle(context.Background(), "b")
		if sv.Title != "Budget deadline" || sv.MergeTitle != "" {
			t.Fatalf("after undo and a late answer: %q %q", sv.Title, sv.MergeTitle)
		}
	})
}
