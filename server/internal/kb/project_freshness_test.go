package kb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/kbwire"
)

var projectEvidenceTime = time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)

func projectViews(t *testing.T, e *Engine, id string) (Entry, *Detail) {
	t.Helper()
	detail, err := e.Get(KindProject, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range e.List(KindProject, "") {
		if row.ID == id {
			if !reflect.DeepEqual(row.Freshness, detail.Freshness) || !reflect.DeepEqual(row.Sources, detail.Sources) || row.ProjectMode != detail.ProjectMode {
				t.Fatalf("list and detail disagree: %+v / %+v", row, detail)
			}
			return row, detail
		}
	}
	t.Fatalf("%s is missing from the list", id)
	return Entry{}, nil
}

func projectUpdateOK(t *testing.T, e *Engine, id string, u Update) {
	t.Helper()
	if _, err := e.Apply(KindProject, id, u); err != nil {
		t.Fatal(err)
	}
}

func projectSourceUpdateOK(t *testing.T, e *Engine, id string, u UserUpdate) {
	t.Helper()
	if _, err := e.ApplyUser(KindProject, id, u); err != nil {
		t.Fatal(err)
	}
}

func fieldReview(t *testing.T, f kbwire.ProjectFieldFreshness, reason string, count int) {
	t.Helper()
	if f.ReviewReason != reason || f.NeedsReview != (reason != "") || f.UncoveredEvidenceCount == nil || *f.UncoveredEvidenceCount != count {
		t.Fatalf("want reason %q and %d uncovered evidence units, got %+v", reason, count, f)
	}
}

func TestProjectCoverageAfterLockedAndPartialRefresh(t *testing.T) {
	ctx := context.Background()
	model := &scriptModel{replies: []string{
		`{"summary":"Two items pending","one_line":"Pending"}`,
		`{"summary":"All items delivered","one_line":"Delivered"}`,
		`{"one_line":"Delivered again"}`,
	}}
	e := testEngine(t, model)
	projectUpdateOK(t, e, "example", Update{Log: "Two items pending"})
	if n := e.RefreshProjects(ctx, 1); n != 1 {
		t.Fatalf("initial refresh = %d", n)
	}
	firstRow, first := projectViews(t, e, "example")
	firstSummary, firstLine := first.Freshness.Fields["summary"], first.Freshness.Fields["one_line"]
	fieldReview(t, firstSummary, "", 0)
	fieldReview(t, firstLine, "", 0)
	if !reflect.DeepEqual(firstSummary.Cutoff, firstLine.Cutoff) || firstSummary.Writer != "persona" || firstSummary.Source != "kb.project" {
		t.Fatalf("initial coverage: %+v", first.Freshness)
	}
	// Submitting the same bytes adds the hold without changing the value or its provenance.
	projectUpdateOK(t, e, "example", Update{Summary: first.Summary})
	projectUpdateOK(t, e, "example", Update{Log: "All items delivered"})
	if n := e.RefreshProjects(ctx, 1); n != 1 {
		t.Fatalf("locked refresh = %d", n)
	}
	row, after := projectViews(t, e, "example")
	summary, one := after.Freshness.Fields["summary"], after.Freshness.Fields["one_line"]
	fieldReview(t, summary, "new_evidence", 1)
	fieldReview(t, one, "", 0)
	p, _ := e.Store.Project("example")
	if after.Summary != first.Summary || row.OneLine != "Delivered" || p.Dirty || !taggingFieldLocked(&p.Tagging, "summary") ||
		!reflect.DeepEqual(summary.Cutoff, firstSummary.Cutoff) || summary.LockReason != "field_locked" || summary.ReviewBy != "user" ||
		one.Cutoff.TimelineThrough <= summary.Cutoff.TimelineThrough || row.Updated.Before(firstRow.Updated) {
		t.Fatalf("locked summary changed or gap lost: %+v / %+v / %+v", p, summary, one)
	}

	// Use a second unlocked project to prove an omitted output cannot advance its cutoff.
	e = testEngine(t, &scriptModel{replies: []string{model.replies[0], model.replies[2]}})
	projectUpdateOK(t, e, "partial", Update{Log: "First item"})
	e.RefreshProjects(ctx, 1)
	_, first = projectViews(t, e, "partial")
	projectUpdateOK(t, e, "partial", Update{Log: "Second item"})
	e.RefreshProjects(ctx, 1)
	_, after = projectViews(t, e, "partial")
	fieldReview(t, after.Freshness.Fields["summary"], "new_evidence", 1)
	fieldReview(t, after.Freshness.Fields["one_line"], "", 0)
	if after.Summary != first.Summary || !reflect.DeepEqual(after.Freshness.Fields["summary"].Cutoff, first.Freshness.Fields["summary"].Cutoff) {
		t.Fatalf("partial output changed summary coverage: %+v", after.Freshness)
	}
}

func TestProjectCoverageSurvivesFailureAndReopen(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, &scriptModel{replies: []string{`{"summary":"First","one_line":"First line"}`}})
	projectUpdateOK(t, e, "example", Update{Log: "First evidence"})
	e.RefreshProjects(ctx, 1)
	_, first := projectViews(t, e, "example")
	for i := range projectLogWindow + 3 {
		// Identical times and even repeated text still occupy different positions.
		if err := e.Store.AppendLog("example", projectEvidenceTime, "example", fmt.Sprintf("New detail %d", i%2)); err != nil {
			t.Fatal(err)
		}
	}
	projectUpdateOK(t, e, "example", Update{Log: "Queue refresh"})
	failed := &failingModel{err: errors.New("synthetic model failure")}
	e.Model = failed
	if n := e.RefreshProjects(ctx, 1); n != 0 || failed.calls != 1 {
		t.Fatalf("refresh = %d, model calls = %d", n, failed.calls)
	}
	p, _ := e.Store.Project("example")
	p.Dirty = false
	if err := e.Store.PutProject(p); err != nil {
		t.Fatal(err)
	}
	store, err := NewStore(e.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	e = &Engine{Store: store}
	_, after := projectViews(t, e, "example")
	for _, field := range []string{"summary", "one_line"} {
		view := after.Freshness.Fields[field]
		fieldReview(t, view, "new_evidence", projectLogWindow+4)
		if !reflect.DeepEqual(view.Cutoff, first.Freshness.Fields[field].Cutoff) || after.Freshness.TimelineEnd <= view.Cutoff.TimelineThrough {
			t.Fatalf("%s cutoff advanced after failure or metadata edit: %+v", field, view)
		}
	}
	// A successful retry covers the supplied tail; its start remains visible
	// so the caller can see the earlier entries were outside this model input.
	e.Model = &scriptModel{replies: []string{`{"summary":"Second","one_line":"Second line"}`}}
	if err := e.refreshProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	_, after = projectViews(t, e, "example")
	summary, one := after.Freshness.Fields["summary"], after.Freshness.Fields["one_line"]
	if !reflect.DeepEqual(summary.Cutoff, one.Cutoff) || !summary.Cutoff.TimelineTruncated || summary.Cutoff.TimelineFrom <= first.Freshness.TimelineEnd ||
		summary.Cutoff.TimelineThrough != after.Freshness.TimelineEnd {
		t.Fatalf("incorrect retry window: %+v", after.Freshness)
	}
}

func TestProjectManualSummaryAndUnknownHistoricalWriter(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, &scriptModel{replies: []string{`{"summary":"Ignored automatic summary","one_line":"Reviewed one line"}`}})
	p := &Project{Slug: "historical", Name: "Historical", OneLine: "Original one line",
		Tagging: Tagging{LockedFields: []string{"summary", "one_line"}}}
	if err := e.Store.PutProject(p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(e.Store.Path(KindProject, p.Slug, "summary.md"), []byte("Original summary\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	projectUpdateOK(t, e, p.Slug, Update{Log: "Older conclusion needs review"})
	_, before := projectViews(t, e, p.Slug)
	for _, field := range []string{"summary", "one_line"} {
		view := before.Freshness.Fields[field]
		fieldReview(t, view, "coverage_unknown", 1)
		if view.Writer != "" || view.Cutoff != nil || view.ReviewBy != "user" {
			t.Fatalf("invented historical provenance: %+v", view)
		}
	}
	projectUpdateOK(t, e, p.Slug, Update{Summary: "Updated by the owner", Lock: new(false)})
	e.RefreshProjects(ctx, 1)
	row, after := projectViews(t, e, p.Slug)
	one := after.Freshness.Fields["one_line"]
	fieldReview(t, one, "summary_edited", 1)
	summary := after.Freshness.Fields["summary"]
	if row.OneLine != p.OneLine || after.Summary != "Updated by the owner\n" || one.ReviewBy != "user" || one.Writer != "" ||
		summary.Writer != "user" || summary.Source != "kb.update" || summary.Cutoff != nil || summary.LockReason != "field_locked" {
		t.Fatalf("manual edit or legacy lock missing: %+v / %+v", row, after)
	}
	projectUpdateOK(t, e, "unlocked", Update{Log: "First evidence"})
	e.RefreshProjects(ctx, 1)
	old, _ := projectViews(t, e, "unlocked")
	projectUpdateOK(t, e, "unlocked", Update{Summary: "Owner version"})
	_, after = projectViews(t, e, "unlocked")
	one = after.Freshness.Fields["one_line"]
	fieldReview(t, one, "summary_edited", 0)
	if one.ReviewBy != "persona" || !reflect.DeepEqual(one.Cutoff, old.Freshness.Fields["one_line"].Cutoff) {
		t.Fatalf("manual edit changed derived coverage: %+v", one)
	}
	e.RefreshProjects(ctx, 1)
	_, after = projectViews(t, e, "unlocked")
	fieldReview(t, after.Freshness.Fields["one_line"], "", 0)
}

func TestProjectReferenceOnlyEnforcesWritesAfterReopen(t *testing.T) {
	e := testEngine(t, nil)
	projectUpdateOK(t, e, "example", Update{Summary: "Old summary", Log: "Old detail", Goal: "Old goal"})
	p, _ := e.Store.Project("example")
	frozen := e.projectInput(p)
	ref := referenceOnly
	projectSourceUpdateOK(t, e, "example", UserUpdate{ProjectMode: &ref, SourceURLs: []string{"https://example.org/guide"}, Update: Update{Aliases: []string{"Guide"}}})
	store, err := NewStore(e.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	e.Store = store
	model := &scriptModel{replies: []string{`{"summary":"Do not store","one_line":"Do not store"}`}}
	e.Model = model
	var logs []string
	e.Logf = func(f string, args ...any) { logs = append(logs, fmt.Sprintf(f, args...)) }
	if err := e.commitProject(p, frozen, projectDigest{Summary: "Do not store", OneLine: "Do not store"}); !errors.Is(err, ErrReferenceOnly) {
		t.Fatalf("in-flight refresh was not refused: %v", err)
	}
	if n := e.RefreshProjects(context.Background(), 1); n != 0 || model.calls != 0 {
		t.Fatalf("reference-only project called the model: %d / %d", n, model.calls)
	}
	c := &Chat{ID: "example-thread", Name: "Example thread"}
	if err := store.PutChat(c); err != nil {
		t.Fatal(err)
	}
	if id := e.evolveProject("Guide", "long", []string{"Generated alias"}, "Generated role", c, true, "Do not store", projectEvidenceTime); id != "example" {
		t.Fatalf("could not route using the alias: %q", id)
	}
	if len(logs) != 1 || !strings.Contains(logs[0], "reference-only") || strings.Contains(logs[0], "Do not store") {
		t.Fatalf("unobservable or payload-bearing refusal: %v", logs)
	}
	for _, err := range []error{
		store.AppendLog("example", projectEvidenceTime, "example", "Do not store"),
		store.PutSummary(KindProject, "example", "Do not store"),
		store.WriteLog("example", []string{"- Do not store"}),
		e.AppendProjectMemory("example", ProjectMemoryRecord{ID: "example-memory", Conclusion: "Do not store"}),
	} {
		if !errors.Is(err, ErrReferenceOnly) {
			t.Fatalf("detail write was not refused: %v", err)
		}
	}
	if _, err := e.Apply(KindProject, "new-id", Update{Name: "example", Summary: "Do not store"}); !errors.Is(err, ErrReferenceOnly) {
		t.Fatalf("name collision bypassed the policy: %v", err)
	}
	if _, err := e.Apply(KindProject, "example", Update{Aliases: []string{"No partial write"}, Log: "Do not store", Summary: "Do not store"}); !errors.Is(err, ErrReferenceOnly) || !strings.Contains(err.Error(), "log, summary") {
		t.Fatalf("mixed update was not refused with both fields: %v", err)
	}
	if _, err := e.ApplyExtension(KindProject, "example", Update{Summary: "Do not store"}, ExtensionWrite{Extension: "example"}); !errors.Is(err, ErrReferenceOnly) {
		t.Fatalf("extension bypassed the boundary: %v", err)
	}
	row, after := projectViews(t, e, "example")
	current, _ := store.Project("example")
	if row.ProjectMode != ref || row.OneLine != "" || after.Summary != "Old summary\n" || len(after.Log) != 1 ||
		!strings.Contains(after.Log[0], "Old detail") || current.Goal != "Old goal" || !taggingFieldLocked(&current.Tagging, "summary") ||
		!slices.Equal(current.Aliases, []string{"Guide"}) || !slices.Equal(current.Chats, []string{c.ID}) {
		t.Fatalf("reference-only history or routing changed: %+v / %+v", current, after)
	}
	projectSourceUpdateOK(t, e, "example", UserUpdate{ProjectMode: new("full")})
	projectUpdateOK(t, e, "example", Update{Log: "New permitted detail"})
	_, after = projectViews(t, e, "example")
	if after.ProjectMode != "" || len(after.Log) != 2 || !strings.Contains(after.Log[1], "New permitted detail") {
		t.Fatalf("reference-only policy could not be reversed: %+v", after)
	}
}

func TestProjectCanonicalDeliveryAcrossViews(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, &scriptModel{replies: []string{`{"summary":"Open","one_line":"Open"}`}})
	sc := SourceChat{ID: "sample-garden-4", Name: "Garden delivery", Kind: "pr", State: "open",
		SourceURL: "https://github.com/Sample/Garden/pull/4"}
	c := &Chat{ID: ChatID("github", sc.ID), Source: "github", SourceID: sc.ID, Kind: "pr", Projects: []string{"delivery"}}
	if err := e.Store.PutChat(c); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Ingest(ctx, named("github"), sc, nil); err != nil {
		t.Fatal(err)
	}
	projectUpdateOK(t, e, "delivery", Update{Chats: []string{c.ID}, Log: "Waiting for source delivery"})
	projectSourceUpdateOK(t, e, "verification", UserUpdate{SourceURLs: []string{"https://github.com/sample/garden/issues/4#issuecomment-1",
		"https://github.com/sample/garden/pull/4?tab=files"}, Update: Update{Log: "Run an environment check", Lock: new(false)}})
	e.RefreshProjects(ctx, 2)
	_, settled := projectViews(t, e, "delivery")
	sc.State, sc.StateAt = "merged", projectEvidenceTime
	if _, err := e.Ingest(ctx, named("github"), sc, nil); err != nil {
		t.Fatal(err)
	}
	a, after := projectViews(t, e, "delivery")
	b, verification := projectViews(t, e, "verification")
	if len(a.Sources) != 1 || len(b.Sources) != 1 || len(verification.Log) != 1 || len(after.Log) != 1 ||
		a.Sources[0].ID != "github:sample/garden#4" || a.Sources[0].ID != b.Sources[0].ID || a.Sources[0].URL != b.Sources[0].URL {
		t.Fatalf("canonical identity or separate views lost: %+v / %+v", a, b)
	}
	for _, source := range []kbwire.ProjectSource{a.Sources[0], b.Sources[0]} {
		if source.DeliveryState != "merged" || source.DeliveryAt == nil || !source.DeliveryAt.Equal(sc.StateAt) || source.EnvironmentVerification != "unverified" {
			t.Fatalf("delivery was conflated with verification: %+v", source)
		}
	}
	fieldReview(t, after.Freshness.Fields["summary"], "new_evidence", 1)
	if !reflect.DeepEqual(settled.Freshness.Fields["summary"].Cutoff, after.Freshness.Fields["summary"].Cutoff) {
		t.Fatal("source delivery moved the summary cutoff")
	}
	if err := e.MergeProjects("delivery", "verification", "same source"); err == nil {
		t.Fatal("shared canonical source allowed an automatic merge")
	}
	projectViews(t, e, "delivery")
	projectViews(t, e, "verification")
}

func TestProjectSourceURLPreservesExistingCoverage(t *testing.T) {
	ctx := context.Background()
	model := &scriptModel{replies: []string{`{"summary":"Delivery pending","one_line":"Pending"}`}}
	e := testEngine(t, model)
	sc := SourceChat{ID: "sample-garden-4", Kind: "pr", State: "open", StateAt: projectEvidenceTime}
	c := &Chat{ID: ChatID("github", sc.ID), Source: "github", SourceID: sc.ID, Kind: sc.Kind,
		State: sc.State, StateAt: sc.StateAt, Projects: []string{"garden"}}
	if err := e.Store.PutChat(c); err != nil {
		t.Fatal(err)
	}
	projectUpdateOK(t, e, "garden", Update{Chats: []string{c.ID}, Log: "Review requested"})
	if n := e.RefreshProjects(ctx, 1); n != 1 {
		t.Fatalf("initial refresh = %d", n)
	}
	_, initial := projectViews(t, e, "garden")
	projectUpdateOK(t, e, "garden", Update{Summary: initial.Summary})
	_, before := projectViews(t, e, "garden")
	beforeSummary := before.Freshness.Fields["summary"]
	if beforeSummary.LockReason != "field_locked" || beforeSummary.Cutoff == nil || len(beforeSummary.Cutoff.Sources) != 1 ||
		beforeSummary.Cutoff.Sources[0].SourceID != "chats/"+c.ID {
		t.Fatalf("expected a locked summary with pre-URL coverage: %+v", beforeSummary)
	}

	sc.SourceURL = "https://github.com/sample/garden/pull/4"
	if _, err := e.Ingest(ctx, named("github"), sc, nil); err != nil {
		t.Fatal(err)
	}
	_, after := projectViews(t, e, "garden")
	if len(after.Freshness.Evidence) != 1 || after.Freshness.Evidence[0].SourceID != "github:sample/garden#4" {
		t.Fatalf("canonical source ID missing from current evidence: %+v", after.Freshness.Evidence)
	}
	for _, field := range []string{"summary", "one_line"} {
		view := after.Freshness.Fields[field]
		fieldReview(t, view, "", 0)
		if !reflect.DeepEqual(view, before.Freshness.Fields[field]) {
			t.Fatalf("%s coverage changed after recording the URL: %+v", field, view)
		}
	}
	if after.Freshness.NeedsReview {
		t.Fatal("recording the URL flagged the project for review")
	}
	if n := e.RefreshProjects(ctx, 1); n != 0 || model.calls != 1 {
		t.Fatalf("recording the URL scheduled a refresh: %d projects, %d total model calls", n, model.calls)
	}

	sc.State, sc.StateAt = "merged", projectEvidenceTime.Add(time.Hour)
	if _, err := e.Ingest(ctx, named("github"), sc, nil); err != nil {
		t.Fatal(err)
	}
	_, merged := projectViews(t, e, "garden")
	fieldReview(t, merged.Freshness.Fields["summary"], "new_evidence", 1)
	fieldReview(t, merged.Freshness.Fields["one_line"], "new_evidence", 1)
}

func TestProjectRefreshCommitsWhenSourceURLIsRecorded(t *testing.T) {
	ctx := context.Background()
	e := testEngine(t, nil)
	sc := SourceChat{ID: "sample-garden-4", Kind: "pr", State: "open", StateAt: projectEvidenceTime,
		SourceURL: "https://github.com/sample/garden/pull/4"}
	c := &Chat{ID: ChatID("github", sc.ID), Source: "github", SourceID: sc.ID, Kind: sc.Kind,
		State: sc.State, StateAt: sc.StateAt, Projects: []string{"garden"}}
	if err := e.Store.PutChat(c); err != nil {
		t.Fatal(err)
	}
	projectUpdateOK(t, e, "garden", Update{Chats: []string{c.ID}, Log: "Review requested"})
	e.Model = knowledgeReplyModel{reply: `{"summary":"Delivery pending","one_line":"Pending"}`, call: func(_, _ string) {
		if _, err := e.Ingest(ctx, named("github"), sc, nil); err != nil {
			t.Fatal(err)
		}
	}}
	e.RefreshProjects(ctx, 1)
	row, detail := projectViews(t, e, "garden")
	if row.OneLine != "Pending" || !strings.Contains(detail.Summary, "Delivery pending") {
		t.Fatalf("refresh was discarded when the URL was recorded: %+v / %q", row, detail.Summary)
	}
	fieldReview(t, detail.Freshness.Fields["summary"], "", 0)
	fieldReview(t, detail.Freshness.Fields["one_line"], "", 0)
}

// A rewrite reads the linked chats summarized most recently, not the first linked (#15308); a message that arrives in a
// linked chat while the model works neither reorders the input nor voids the rewrite.
func TestProjectRewriteReadsTheMostRecentlySummarizedChats(t *testing.T) {
	e := projectMemoryEngine(t, t.TempDir())
	var ids []string
	for i := range 10 {
		id := fmt.Sprintf("lark-c%d", i)
		ids = append(ids, id)
		// Linked oldest first: c0 was summarized on day 0, c9 on day 9; c1 has the newest message but an old summary.
		c := &Chat{ID: id, Source: "lark", SourceID: id, Kind: "group", Name: id, Projects: []string{"p"},
			CompressedAt: projectEvidenceTime.AddDate(0, 0, i), CoveredUntil: projectEvidenceTime.AddDate(0, 0, i), LastAt: projectEvidenceTime.AddDate(0, 0, i)}
		if i == 1 {
			c.LastAt = projectEvidenceTime.AddDate(0, 0, 30)
		}
		if err := e.Store.PutChat(c); err != nil {
			t.Fatal(err)
		}
		if err := e.Store.PutSummary(KindChat, id, "summary of "+id); err != nil {
			t.Fatal(err)
		}
	}
	// Never summarized: no summary and no CompressedAt, so it goes last.
	if err := e.Store.PutChat(&Chat{ID: "lark-new", Source: "lark", SourceID: "new", Kind: "group", Name: "new", Projects: []string{"p"},
		LastAt: projectEvidenceTime.AddDate(0, 0, 40)}); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.PutProject(&Project{Slug: "p", Name: "P", Status: "active", Chats: append([]string{"lark-new"}, ids...)}); err != nil {
		t.Fatal(err)
	}
	p, _ := e.Store.Project("p")
	in := e.projectInput(p)
	var got []string
	for _, c := range in.Chats {
		got = append(got, c.ID)
	}
	want := []string{"lark-c9", "lark-c8", "lark-c7", "lark-c6", "lark-c5", "lark-c4", "lark-c3", "lark-c2"}
	if !slices.Equal(got, want) {
		t.Fatalf("chats read = %v, want %v", got, want)
	}

	// A new message in a read chat and in an unread one: same input, so a rewrite started before still commits.
	before := summaryInputPrompt(e, p)
	for _, id := range []string{"lark-c9", "lark-c0"} {
		c, _ := e.Store.Chat(id)
		c.LastAt = projectEvidenceTime.AddDate(0, 0, 50)
		c.CoveredUntil = projectEvidenceTime.AddDate(0, 0, -1) // a late backfilled message moves the coverage back
		c.Pending++
		if err := e.Store.PutChat(c); err != nil {
			t.Fatal(err)
		}
	}
	if after := summaryInputPrompt(e, p); after != before {
		t.Fatalf("a new message changed the rewrite's input:\n--- before ---\n%s\n--- after ---\n%s", before, after)
	}

	// The oldest-linked chat summarized anew moves into what is read.
	c0, _ := e.Store.Chat("lark-c0")
	c0.CompressedAt = projectEvidenceTime.AddDate(0, 0, 50)
	if err := e.Store.PutChat(c0); err != nil {
		t.Fatal(err)
	}
	if in := e.projectInput(p); in.Chats[0].ID != "lark-c0" || len(in.Chats) != projectPromptChatsMost {
		t.Fatalf("after c0's new summary: first %s, %d chats", in.Chats[0].ID, len(in.Chats))
	}
}
