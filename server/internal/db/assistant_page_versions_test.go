package db

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"
)

func TestAssistantPageRestoreVersionPreservesLiveState(t *testing.T) {
	d, ctx := pagesDB(t)
	p := draft(1, "c1", "Weekly rent", pagesBase)
	p.View = nil // watch-only pages have an empty stored View.
	p.Watch = watchJSON("Rent", "number", "USD", map[string]any{"when": "below", "value": 1900})
	first := mustCreate(t, d, p)
	kept, _, err := d.KeepAssistantPage(ctx, AssistantPageKeep{OfferView: first.OfferView, Device: "phone", AlertOn: true, OnOverview: true, At: pagesBase})
	if err != nil {
		t.Fatal(err)
	}
	by := AssistantPageWriter{Kind: AssistantPageByAgent, Machine: "m1", Session: "c1", Note: "new graph"}
	current, err := d.UpdateAssistantPage(ctx, AssistantPageUpdate{ID: kept.ID, Expected: 1, Watch: watchJSON("Median rent", "number", "EUR", map[string]any{"when": "below", "value": 1900}), View: pageView("second"), By: by, At: pagesBase.Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	current, _, err = d.RenameAssistantPage(ctx, kept.ID, "Updated rent", "phone", pagesBase.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = d.BindAssistantPageRoutine(ctx, current.ID, "rt_rent", pagesBase)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = d.PinAssistantPage(ctx, current.ID, true, pagesBase)
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = d.AddAssistantPageReading(ctx, NewAssistantPageReading{PageID: current.ID, Reading: numReading(1275), ByKind: AssistantPageByRoutine, At: pagesBase.Add(3 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	beforeRestore, err := d.MarkAssistantPageRefresh(ctx, current.ID, "phone", pagesBase.Add(4*time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	history, err := d.AssistantPageVersionSummaries(ctx, kept.ID)
	if err != nil || len(history) != 3 || history[0].Version != 3 || history[0].By.Device != "phone" || history[1].By != by {
		t.Fatalf("history: %+v, %v", history, err)
	}
	for _, version := range history {
		if version.View != nil {
			t.Fatalf("summary contains view %s", version.View)
		}
	}
	selected, err := d.AssistantPageVersion(ctx, kept.ID, 1)
	if err != nil || selected.Title != first.Title || string(selected.View) != "{}" {
		t.Fatalf("snapshot: %+v, %v", selected, err)
	}

	at := pagesBase.Add(5 * time.Minute)
	row, err := d.RestoreAssistantPageVersion(ctx, current.ID, current.Version, 1, "web", at)
	if err != nil {
		t.Fatal(err)
	}
	if row.Version != 4 || row.Title != selected.Title || !bytes.Equal(row.View, selected.View) || !row.Paused || row.PausedBy != AssistantPagePausedByRestore || !row.ContentAt.Equal(at) || row.RefreshAskedAt != nil || row.RefreshDevice != "" {
		t.Fatalf("restore: %+v", row)
	}
	if row.RoutineID != beforeRestore.RoutineID || !row.Pinned || !row.OnOverview || !row.Alert.On || !reflect.DeepEqual(row.Alert, beforeRestore.Alert) || !bytes.Equal(row.Watch, beforeRestore.Watch) {
		t.Fatalf("lost live settings: %+v", row)
	}
	written, err := d.AssistantPageVersion(ctx, row.ID, row.Version)
	if err != nil || written.By != (AssistantPageWriter{Kind: AssistantPageByRestore, Device: "web"}) || !written.At.Equal(at) || written.Title != selected.Title || !bytes.Equal(written.View, selected.View) {
		t.Fatalf("new snapshot: %+v, %v", written, err)
	}
	for _, kind := range []string{AssistantPageByAgent, AssistantPageByRoutine} {
		if _, err := d.UpdateAssistantPage(ctx, AssistantPageUpdate{ID: row.ID, Expected: row.Version, View: pageView("overwrite"), By: AssistantPageWriter{Kind: kind}, At: at}); !errors.Is(err, ErrAssistantPagePaused) {
			t.Fatalf("paused %s write: %v", kind, err)
		}
		if _, _, err := d.AddAssistantPageReading(ctx, NewAssistantPageReading{PageID: row.ID, Reading: numReading(99), ByKind: kind, At: at}); !errors.Is(err, ErrAssistantPagePaused) {
			t.Fatalf("paused %s reading: %v", kind, err)
		}
	}
	if _, err := d.MarkAssistantPageRefresh(ctx, row.ID, "other", at); !errors.Is(err, ErrAssistantPagePaused) {
		t.Fatalf("stale refresh: %v", err)
	}
	readings, err := d.AssistantPageReadings(ctx, row.ID, 10)
	if err != nil || len(readings) != 1 || !bytes.Contains(readings[0].Reading, []byte("1275")) {
		t.Fatalf("readings: %+v, %v", readings, err)
	}
	if _, changed, err := d.PauseAssistantPage(ctx, row.ID, false, at); err != nil || !changed {
		t.Fatalf("resume: %v %v", changed, err)
	}
	if _, err := d.UpdateAssistantPage(ctx, AssistantPageUpdate{ID: row.ID, Expected: row.Version, View: pageView("resumed"), By: by, At: at}); err != nil {
		t.Fatalf("resumed write: %v", err)
	}
}

func TestAssistantPageRestoreVersionConflicts(t *testing.T) {
	d, ctx := pagesDB(t)
	first := mustCreate(t, d, draft(1, "c1", "Rent", pagesBase))
	if _, err := d.RestoreAssistantPageVersion(ctx, first.ID, 1, 1, "web", pagesBase); !errors.Is(err, ErrAssistantPageNotKept) {
		t.Fatalf("draft: %v", err)
	}
	p := mustKeep(t, d, first, pagesBase)
	p, _, err := d.RenameAssistantPage(ctx, p.ID, "New title", "phone", pagesBase)
	if err != nil {
		t.Fatal(err)
	}
	other := mustKeep(t, d, mustCreate(t, d, draft(2, "c2", "ＲＥＮＴ", pagesBase)), pagesBase)
	if _, err := d.RestoreAssistantPageVersion(ctx, p.ID, 2, 1, "web", pagesBase); !errors.Is(err, ErrAssistantPageTitleTaken) {
		t.Fatalf("title conflict: %v", err)
	}
	current, err := d.AssistantPage(ctx, p.ID)
	if err != nil || current.Paused || current.Version != 2 || current.Title != "New title" {
		t.Fatalf("failed restore mutated the page: %+v, %v", current, err)
	}
	if _, _, err := d.RemoveAssistantPage(ctx, other.ID, AssistantPageRemovedByOwner, pagesBase); err != nil {
		t.Fatal(err)
	}
	if _, err := d.RestoreAssistantPageVersion(ctx, other.ID, 2, 1, "web", pagesBase); !errors.Is(err, ErrAssistantPageRemoved) {
		t.Fatalf("removed: %v", err)
	}
	for _, pair := range [][2]int64{{1, 1}, {2, 0}, {2, 2}, {2, 3}} {
		if _, err := d.RestoreAssistantPageVersion(ctx, p.ID, pair[0], pair[1], "web", pagesBase); !errors.Is(err, ErrAssistantPageVersion) {
			t.Fatalf("expected, pick %v: %v", pair, err)
		}
	}
	if _, err := d.AssistantPageVersion(ctx, p.ID, 100); !errors.Is(err, ErrAssistantPageVersionNotFound) {
		t.Fatalf("missing version: %v", err)
	}

	const writers = 6
	errs := make(chan error, writers)
	var workers sync.WaitGroup
	for range writers {
		workers.Go(func() { _, err := d.RestoreAssistantPageVersion(ctx, p.ID, 2, 1, "web", pagesBase); errs <- err })
	}
	workers.Wait()
	close(errs)
	wins := 0
	for err := range errs {
		if err == nil {
			wins++
		} else if !errors.Is(err, ErrAssistantPageVersion) {
			t.Fatal(err)
		}
	}
	versions, err := d.AssistantPageVersions(ctx, p.ID)
	if err != nil || wins != 1 || len(versions) != 3 || versions[0].Version != 3 || versions[0].By.Kind != AssistantPageByRestore {
		t.Fatalf("wins %d, history %+v, %v", wins, versions, err)
	}
}

func TestAssistantPageRestoreVersionPruning(t *testing.T) {
	d, ctx := pagesDB(t)
	p := mustKeep(t, d, mustCreate(t, d, draft(1, "c1", "Rent", pagesBase)), pagesBase)
	for i := 2; i <= AssistantPageVersionsKept; i++ {
		if _, _, err := d.RenameAssistantPage(ctx, p.ID, fmt.Sprintf("Title %d", i), "phone", pagesBase); err != nil {
			t.Fatal(err)
		}
	}
	current, err := d.RestoreAssistantPageVersion(ctx, p.ID, AssistantPageVersionsKept, 1, "phone", pagesBase)
	if err != nil || current.Title != "Rent" {
		t.Fatalf("restore at retention boundary: %+v, %v", current, err)
	}
	if _, err := d.RestoreAssistantPageVersion(ctx, p.ID, current.Version, 1, "phone", pagesBase); !errors.Is(err, ErrAssistantPageVersionNotFound) {
		t.Fatalf("pruned target: %v", err)
	}
	if _, err := d.AssistantPageVersion(ctx, p.ID, 1); !errors.Is(err, ErrAssistantPageVersionNotFound) {
		t.Fatalf("pruned snapshot: %v", err)
	}
	versions, err := d.AssistantPageVersionSummaries(ctx, p.ID)
	if err != nil || len(versions) != AssistantPageVersionsKept || versions[0].Version != current.Version || versions[len(versions)-1].Version != 2 {
		t.Fatalf("retained snapshots: %+v, %v", versions, err)
	}
}
