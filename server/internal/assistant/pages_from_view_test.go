package assistant

import (
	"encoding/json"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

// showDashboardSource returns exactly the reference the authoring tool receives.
func showDashboardSource(t *testing.T, s *Service, session string, view json.RawMessage) map[string]any {
	t.Helper()
	out, err := runPageTool(s, session, "view_show", map[string]any{"view": view})
	if err != nil {
		t.Fatal(err)
	}
	var ref map[string]any
	if err := json.Unmarshal([]byte(out), &ref); err != nil || ref["id"] == nil || ref["version"] != float64(1) {
		t.Fatalf("view_show: %s, %v", out, err)
	}
	return ref
}

func TestPageFromViewSnapshotAndKeep(t *testing.T) {
	s, _, database := pagesToolService(t)
	const session = "conv-1"
	sourceView := json.RawMessage(strings.Replace(runPageView, `,"confirm":"Skip today?"`, "", 1))
	ref := showDashboardSource(t, s, session, sourceView)
	source := ref["id"].(string)
	// The card already has a real answer. Only its layout and actions become part of the page.
	if code, out := postCardAction(t, s, session, source, "skip"); code != http.StatusOK {
		t.Fatalf("answer original card: %d %v", code, out)
	}
	waitCardAction(t, database, session, source)
	out := mustCallPage(t, s, session, map[string]any{"action": "create", "title": "Run log", "from_view": ref})
	if out["status"] != "draft" {
		t.Fatalf("copied page bypassed Keep: %+v", out)
	}
	id := out["id"].(string)
	draft, err := database.AssistantPage(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if draft.OfferView == "" || draft.OfferView == source || draft.CreatedSession != session || draft.Status != db.AssistantPageDraft {
		t.Fatalf("new dashboard preview: %+v", draft)
	}
	offer, err := database.AssistantView(t.Context(), session, draft.OfferView)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := wire.ParseAssistantView(offer.View)
	if err != nil {
		t.Fatal(err)
	}
	var actions []string
	for _, action := range wire.Actions(preview) {
		actions = append(actions, action.ID)
	}
	if !slices.Equal(actions, []string{pageOfferKeep, pageOfferKeepPin, pageOfferNotNow}) || pageItem(preview, "streak").Value != "12 days" {
		t.Fatalf("read-only preview and owner's choices: %s", offer.View)
	}
	if _, err := runPageTool(s, session, "view_update", map[string]any{"id": source, "version": ref["version"], "view": json.RawMessage(`{"fallback":"New result","sections":[]}`)}); err != nil {
		t.Fatalf("edit the source independently: %v", err)
	}
	if code, out := postCardAction(t, s, session, draft.OfferView, pageOfferKeep); code != http.StatusOK {
		t.Fatalf("keep: %d %v", code, out)
	}
	got := mustCallPage(t, s, session, map[string]any{"action": "get", "id": id})
	var want map[string]any
	if err := json.Unmarshal(sourceView, &want); err != nil {
		t.Fatal(err)
	}
	if got["status"] != db.AssistantPageActive || !reflect.DeepEqual(got["view"], want) {
		t.Fatalf("kept snapshot changed: %+v", got)
	}
	if taps, ok := got["recent_actions"].([]any); !ok || len(taps) != 0 {
		t.Fatalf("source replies copied into dashboard: %v", got["recent_actions"])
	}
}

func TestPageFromViewRefusalsWriteNoDraft(t *testing.T) {
	s, hub, database := pagesToolService(t)
	const session = "conv-1"
	hub.fakeHub.setSessions("second", proto.Session{SessionID: session, Assistant: true})
	ref := showDashboardSource(t, s, session, runView())
	old := map[string]any{"id": ref["id"], "version": ref["version"]}
	if _, err := runPageTool(s, session, "view_update", map[string]any{"id": ref["id"], "version": ref["version"], "view": runView()}); err != nil {
		t.Fatal(err)
	}
	ref["version"] = 2
	// Both cards are valid in the live conversation. Dashboards have a stricter component and action vocabulary.
	image := showDashboardSource(t, s, session, json.RawMessage(`{"fallback":"Map","sections":[{"items":[{"type":"image","src":{"url":"https://example.com/map.png"},"alt":"Map"}]}]}`))
	link := showDashboardSource(t, s, session, json.RawMessage(`{"fallback":"Current task","sections":[{"items":[{"type":"button","key":"visit","title":"Task","action":{"id":"visit","open":"persona://task/task_1"}}]}]}`))
	for _, test := range []struct {
		name, machine, conversation string
		ref                         map[string]any
		explicit                    bool
		why                         string
	}{
		{"missing card", "server", session, map[string]any{"id": "view_0000000000000000", "version": 1}, false, "not_found"},
		{"old version", "server", session, old, false, "version_conflict"},
		{"other conversation", "server", "conv-2", ref, false, "not_found"},
		{"same session on different machine", "second", session, ref, false, "not_found"},
		{"unspecified version", "server", session, map[string]any{"id": ref["id"]}, false, "version"},
		{"two sources", "server", session, ref, true, "view"},
		{"image", "server", session, image, false, "image"},
		{"unavailable action", "server", session, link, false, "open"},
	} {
		t.Run(test.name, func(t *testing.T) {
			input := map[string]any{"action": "create", "title": "Test notes", "from_view": test.ref}
			if test.explicit {
				input["view"] = runView()
			}
			if result, err := callPage(s, test.machine, test.conversation, input); err == nil || !strings.Contains(err.Error(), test.why) {
				t.Fatalf("expected %q, got %v, %v", test.why, result, err)
			}
		})
	}
	pages, err := database.AssistantPages(t.Context(), db.AssistantPageQuery{})
	if err != nil || len(pages) != 0 {
		t.Fatalf("failed imports persisted drafts: %v, %v", pages, err)
	}
	// The current reference still works after all refusals and snapshots the revision requested.
	got := mustCallPage(t, s, session, map[string]any{"action": "create", "title": "Allowed notes", "from_view": ref})
	if got["status"] != "draft" {
		t.Fatalf("current reference: %v", got)
	}
}

func TestPageFromViewRefusesWithdrawnOffer(t *testing.T) {
	s, _, database := pagesToolService(t)
	const session = "conv-1"
	id, offer := createPage(t, s, database, session, "Run log")
	mustCallPage(t, s, session, map[string]any{"action": "remove", "id": id, "reason": "Draft withdrawn."})
	card, err := database.AssistantView(t.Context(), session, offer)
	if err != nil {
		t.Fatal(err)
	}
	view, err := wire.ParseAssistantView(card.View)
	if err != nil {
		t.Fatal(err)
	}
	if len(wire.Actions(view)) != 0 {
		t.Fatal("withdrawn offer still has owner actions")
	}
	_, err = callPage(s, "server", session, map[string]any{
		"action": "create", "title": "Copy of withdrawn preview",
		"from_view": map[string]any{"id": card.ID, "version": card.Version},
	})
	if err == nil || !strings.Contains(err.Error(), "dashboard offer") {
		t.Fatalf("withdrawn host-owned preview accepted as an ordinary live card: %v", err)
	}
}
