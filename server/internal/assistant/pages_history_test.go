package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/db"
)

// Follow the same navigation a current client uses. The state must come from the displayed
// control, so the test also covers the head version carried from the list into the preview.
func openHistoryControl(t *testing.T, s *Service, id string, view *wire.View, key string) *wire.View {
	t.Helper()
	control := pageItem(view, key)
	if control.Action == nil || control.Action.Push == nil || control.Action.Push.UI != id || control.Action.Push.State == "" {
		t.Fatalf("missing navigation for %s: %+v", key, control)
	}
	return mustPage(t, s, id, wire.UIRequest{State: control.Action.Push.State}).View
}

func TestDashboardHistoryReviewAndRestore(t *testing.T) {
	s, _, database := pagesTestService(t)
	ctx := context.Background()
	page := keptPage(t, database, "Garden notes", "main", time.Now().Add(-time.Hour), true)
	second, _, err := database.RenameAssistantPage(ctx, page.ID, "Garden in summer", "tablet", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	live := mustPage(t, s, page.ID, wire.UIRequest{}).View
	history := openHistoryControl(t, s, page.ID, live, "persona.history")
	section, ok := pageSection(history, "persona.history")
	if !ok || len(section.Items) != 2 || section.Items[0].Value != "Current" ||
		section.Items[0].Key != "persona.version:2" || !strings.Contains(section.Items[0].Subtitle, "You") {
		t.Fatalf("history should show the owner's rename first: %+v", section)
	}
	current := openHistoryControl(t, s, page.ID, history, "persona.version:2")
	if pageItem(current, "persona.restore_version").Action != nil {
		t.Fatal("current version offers Restore")
	}

	preview := openHistoryControl(t, s, page.ID, history, "persona.version:1")
	if item := pageItem(preview, "persona.summary"); item.Type != wire.ComponentText || item.Text != "Morning run" {
		t.Fatalf("historical summary: %+v", item)
	}
	acts := wire.Actions(preview)
	if len(preview.Pages) != 0 || len(acts) != 1 || acts[0].ID != "persona.restore_version" || acts[0].Confirm == "" {
		t.Fatalf("preview should expose only a confirmed restore: %+v", acts)
	}
	// The live page has both of these actions; neither is usable from its historical preview.
	for _, action := range []string{"done", "guide", "back"} {
		_, err := pagesUI(t, s, page.ID, wire.UIRequest{Action: action, State: preview.State})
		if !pagesRefused(err, http.StatusBadRequest) {
			t.Fatalf("%s from preview: %v", action, err)
		}
	}

	currentView := strings.Replace(runPageView, "12 days", "18 days", 1)
	third, err := database.UpdateAssistantPage(ctx, db.AssistantPageUpdate{ID: page.ID, Expected: second.Version,
		View: json.RawMessage(currentView), By: db.AssistantPageWriter{Kind: db.AssistantPageByAgent, Machine: "server", Session: "conv-1"}, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	stale := mustPage(t, s, page.ID, wire.UIRequest{Action: acts[0].ID, State: preview.State})
	if stale.Toast == "" || pageItem(stale.View, "persona.version:3").Value != "Current" {
		t.Fatalf("changed head should require choosing again: %+v", stale)
	}
	saved, err := database.AssistantPage(ctx, page.ID)
	if err != nil || saved.Version != third.Version || saved.Paused || !strings.Contains(string(saved.View), "18 days") {
		t.Fatalf("stale review lost the new edit: %+v %v", saved, err)
	}

	reviewed := openHistoryControl(t, s, page.ID, stale.View, "persona.version:1")
	latest := openHistoryControl(t, s, page.ID, stale.View, "persona.version:3")
	for _, pair := range []struct {
		view        *wire.View
		want, stale string
	}{
		{reviewed, "12 days", "18 days"}, {latest, "18 days", "12 days"},
	} {
		rendered, err := json.Marshal(pair.view)
		if err != nil {
			t.Fatal(err)
		}
		body := string(rendered)
		if !strings.Contains(body, pair.want) || strings.Contains(body, pair.stale) ||
			!strings.Contains(body, "Last week") || strings.Contains(body, "https://example.com/guide") {
			t.Fatalf("preview should show the selected revision's metric and nested content: %s", body)
		}
	}
	result := mustPage(t, s, page.ID, wire.UIRequest{Action: "persona.restore_version", State: reviewed.State})
	if result.Toast == "" || !result.Pop {
		t.Fatalf("restore should return to the history list: %+v", result)
	}
	version, err := database.AssistantPageVersion(ctx, page.ID, 4)
	if err != nil || version.By.Kind != db.AssistantPageByRestore || version.By.Device != "phone" {
		t.Fatalf("restore attributed to the requesting owner's device: %+v %v", version, err)
	}
	saved, err = database.AssistantPage(ctx, page.ID)
	if err != nil || saved.Title != page.Title || !saved.Paused || !saved.Pinned || !strings.Contains(string(saved.View), "12 days") {
		t.Fatalf("restored dashboard: %+v %v", saved, err)
	}
	next := db.AssistantPageUpdate{ID: page.ID, Expected: saved.Version, View: json.RawMessage(currentView),
		By: db.AssistantPageWriter{Kind: db.AssistantPageByAgent, Machine: "server", Session: "conv-1"}, At: time.Now()}
	if _, err := database.UpdateAssistantPage(ctx, next); !errors.Is(err, db.ErrAssistantPagePaused) {
		t.Fatalf("writes wait for owner to resume: %v", err)
	}
	live = mustPage(t, s, page.ID, wire.UIRequest{}).View
	resume := pageItem(live, "persona.pause").Action
	if resume == nil || resume.ID != "persona.resume" {
		t.Fatal("restored dashboard does not offer Resume")
	}
	mustPage(t, s, page.ID, wire.UIRequest{Action: resume.ID})
	if _, err := database.UpdateAssistantPage(ctx, next); err != nil {
		t.Fatalf("resumed dashboard update: %v", err)
	}
}

func TestDashboardHistoryRemovedAndLegacyNavigation(t *testing.T) {
	s, _, database := pagesTestService(t)
	ctx := context.Background()
	p := keptPage(t, database, "Weekend walk", "main", time.Now().Add(-time.Hour), false)
	if _, _, err := database.RenameAssistantPage(ctx, p.ID, "Sunday walk", "phone", time.Now()); err != nil {
		t.Fatal(err)
	}

	// UI v1 navigates in place and sends action IDs instead of following a push.
	legacy := func(action, state string) *wire.View {
		return mustPage(t, s, p.ID, wire.UIRequest{Action: action, State: state, Context: &wire.UIContext{UIVersion: 1}}).View
	}
	history := legacy("persona.history", "")
	version := legacy("persona.version:1", history.State)
	// Version 1 does not expose component keys. Find the fallbacks by their wire actions.
	backAction := func(view *wire.View, id string) string {
		t.Helper()
		for _, action := range wire.Actions(view) {
			if action.ID == id {
				return action.ID
			}
		}
		t.Fatalf("missing legacy action %s on %s", id, view.Title)
		return ""
	}
	back := legacy(backAction(version, "persona.history"), version.State)
	if live := legacy(backAction(back, "persona.dashboard"), back.State); live.Title != "Sunday walk" {
		t.Fatalf("legacy back: %s", live.Title)
	}

	mustPage(t, s, p.ID, wire.UIRequest{Action: "persona.remove"})
	unavailable := mustPage(t, s, p.ID, wire.UIRequest{Action: "persona.restore_version", State: version.State})
	if unavailable.View == nil || pageItem(unavailable.View, "persona.restore_version").Action != nil {
		t.Fatal("removed dashboard still offers version restore")
	}
	saved, err := database.AssistantPage(ctx, p.ID)
	if err != nil || saved.Status != db.AssistantPageRemoved || saved.Version != 2 {
		t.Fatalf("removed dashboard: %+v %v", saved, err)
	}
	mustPage(t, s, pagesIndex, wire.UIRequest{Action: "persona.delete:" + p.ID})
	_, err = pagesUI(t, s, p.ID, wire.UIRequest{State: version.State})
	if !pagesRefused(err, http.StatusNotFound) {
		t.Fatalf("deleted dashboard preview: %v", err)
	}
}
