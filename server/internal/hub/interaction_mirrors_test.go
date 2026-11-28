package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func TestDisplayMirrorUpdatesAndResolvesTogether(t *testing.T) {
	h := newTestHub(t)
	c := &client{out: make(chan []byte, 8), urlFeatures: map[string]bool{featureApprovalMirrors: true}}
	addTestClient(h, c)
	origin := proto.Interaction{ID: "ix_origin", SessionID: "side", SessionTitle: "Originating conversation",
		Kind: proto.IxPermission, MirrorMain: true, Summary: "before", CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(time.Minute).UTC()}
	mirror := origin
	mirror.ID, mirror.MirrorOf, mirror.MirrorMain, mirror.SessionID = proto.ApprovalMirrorID(origin.ID, origin.CreatedAt), origin.ID, false, "main"
	forged := mirror
	forged.ID = "ix_runtime_chosen"
	if validDisplayMirror(origin, forged) {
		t.Fatal("a runtime-chosen display ID passed hub validation")
	}
	mirror.Summary = "caller-supplied text"
	reportInteraction(t, h, origin)
	reportInteraction(t, h, mirror)
	for range 2 {
		_ = wireReceive(t, c)
	}
	for _, ix := range h.pendingInteractions() {
		if ix.ID == mirror.ID && ix.Summary != origin.Summary {
			t.Fatalf("display content did not come from the authoritative card: %+v", ix)
		}
	}

	origin.Summary = "after"
	origin.ExpiresAt = origin.ExpiresAt.Add(time.Hour)
	reportInteraction(t, h, origin)
	var updated bool
	for range 2 {
		var event struct {
			Type        string            `json:"type"`
			Interaction proto.Interaction `json:"interaction"`
		}
		if json.Unmarshal(wireReceive(t, c), &event) != nil || event.Type != "interaction" {
			t.Fatal("expected an interaction update")
		}
		if event.Interaction.ID == mirror.ID {
			updated = true
			if event.Interaction.MirrorOf != origin.ID || event.Interaction.SessionID != "main" ||
				event.Interaction.SessionTitle != origin.SessionTitle || event.Interaction.Summary != "after" ||
				!event.Interaction.ExpiresAt.Equal(origin.ExpiresAt) {
				t.Fatalf("copy did not follow the original: %+v", event.Interaction)
			}
		}
	}
	if !updated {
		t.Fatal("the original update did not refresh its display copy")
	}

	reportResolved(t, h, origin.ID, "")
	resolved := map[string]bool{}
	for range 2 {
		var event struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if json.Unmarshal(wireReceive(t, c), &event) != nil || event.Type != "interaction_resolved" {
			t.Fatal("expected a resolution event")
		}
		resolved[event.ID] = true
	}
	if !resolved[origin.ID] || !resolved[mirror.ID] || len(resolved) != 2 {
		t.Fatalf("resolution withdrew %v, want original and copy", resolved)
	}
	if got := h.pendingInteractions(); len(got) != 0 {
		t.Fatalf("resolved copies remain pending: %+v", got)
	}

	// An orphaned copy cannot be reintroduced after its origin has resolved.
	reportInteraction(t, h, mirror)
	select {
	case event := <-c.out:
		t.Fatalf("orphaned copy was broadcast: %s", event)
	default:
	}
	if got := h.pendingInteractions(); len(got) != 0 {
		t.Fatalf("orphaned copy was accepted: %+v", got)
	}
}

func TestResolutionEventCarriesOccurrence(t *testing.T) {
	h := newTestHub(t)
	c := &client{out: make(chan []byte, 2), urlFeatures: map[string]bool{featureApprovalMirrors: true}}
	addTestClient(h, c)
	ix := proto.Interaction{ID: "ix_resolution_occurrence", SessionID: "side", Kind: proto.IxPermission,
		CreatedAt: time.Date(2026, 11, 21, 1, 2, 3, 456789123, time.UTC)}
	reportInteraction(t, h, ix)
	_ = wireReceive(t, c)
	reportResolvedAt(t, h, ix.ID, ix.CreatedAt)
	var event struct {
		Type      string `json:"type"`
		ID        string `json:"id"`
		CreatedAt string `json:"created_at"`
	}
	if err := json.Unmarshal(wireReceive(t, c), &event); err != nil {
		t.Fatal(err)
	}
	at, err := time.Parse(time.RFC3339Nano, event.CreatedAt)
	if event.Type != "interaction_resolved" || event.ID != ix.ID || err != nil || !at.Equal(ix.CreatedAt) {
		t.Fatalf("resolution lost its occurrence: %+v, %v", event, err)
	}
}

func TestDisplayMirrorFromEarlierOccurrenceCannotResolveLaterCard(t *testing.T) {
	h := newTestHub(t)
	first := proto.Interaction{ID: "ix_reused_mirror", SessionID: "side", Kind: proto.IxPermission,
		MirrorMain: true, CreatedAt: time.Unix(1000, 0).UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	firstMirror := first
	firstMirror.ID, firstMirror.MirrorOf, firstMirror.MirrorMain, firstMirror.SessionID = proto.ApprovalMirrorID(first.ID, first.CreatedAt), first.ID, false, "main"
	reportInteraction(t, h, first)
	reportInteraction(t, h, firstMirror)
	reportResolved(t, h, first.ID, "")

	second := first
	second.CreatedAt = second.CreatedAt.Add(time.Second)
	secondMirror := second
	secondMirror.ID, secondMirror.MirrorOf, secondMirror.MirrorMain, secondMirror.SessionID = proto.ApprovalMirrorID(second.ID, second.CreatedAt), second.ID, false, "main"
	reportInteraction(t, h, second)
	reportInteraction(t, h, secondMirror)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+firstMirror.ID+"/resolve", strings.NewReader(`{"allow":true}`))
	r.SetPathValue("id", firstMirror.ID)
	h.normalizeDisplayMirror(r)
	if got := r.PathValue("id"); got != firstMirror.ID {
		t.Fatalf("the first occurrence's stale mirror normalized to %q, leaving the later occurrence answerable", got)
	}
	if got := h.pendingInteractions(); len(got) != 2 {
		t.Fatalf("the stale click changed the later occurrence: %+v", got)
	}
}

func TestOriginalAnswerKeepsDisplayedOccurrenceAfterReplacement(t *testing.T) {
	h := newTestHub(t)
	first := proto.Interaction{ID: "ix_original_reused", SessionID: "side", Kind: proto.IxPermission,
		CreatedAt: time.Unix(1000, 123456789).UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	reportInteraction(t, h, first)
	second := first
	second.CreatedAt = second.CreatedAt.Add(time.Second)
	second.Summary = "replacement"
	reportInteraction(t, h, second)

	r := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+first.ID+"/resolve", strings.NewReader(`{"allow":true}`))
	r.SetPathValue("id", first.ID)
	r.Header.Set(assistantwire.InteractionCreatedAtHeader, first.CreatedAt.Format(time.RFC3339Nano))
	h.normalizeDisplayMirror(r)
	if got := r.Header.Get(assistantwire.InteractionCreatedAtHeader); got != first.CreatedAt.Format(time.RFC3339Nano) {
		t.Fatalf("the displayed occurrence was rebound to the replacement: got %q, want %q", got, first.CreatedAt.Format(time.RFC3339Nano))
	}
}

func TestDisplayMirrorNormalizationRefusesMismatchedClientOccurrence(t *testing.T) {
	h := newTestHub(t)
	origin := proto.Interaction{ID: "ix_mirror_client_occurrence", SessionID: "side", Kind: proto.IxPermission,
		MirrorMain: true, CreatedAt: time.Unix(1000, 123456789).UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	mirror := origin
	mirror.ID, mirror.MirrorOf, mirror.MirrorMain, mirror.SessionID = proto.ApprovalMirrorID(origin.ID, origin.CreatedAt), origin.ID, false, "main"
	reportInteraction(t, h, origin)
	reportInteraction(t, h, mirror)
	for _, supplied := range []string{"client-forged", origin.CreatedAt.Add(time.Second).Format(time.RFC3339Nano)} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+mirror.ID+"/resolve", strings.NewReader(`{"allow":true}`))
		r.SetPathValue("id", mirror.ID)
		r.Header.Set(assistantwire.InteractionCreatedAtHeader, supplied)
		h.normalizeDisplayMirror(r)
		if got := r.PathValue("id"); got != mirror.ID {
			t.Fatalf("client occurrence %q normalized the mirror to %q", supplied, got)
		}
		if got := r.Header.Get(assistantwire.InteractionCreatedAtHeader); got != supplied {
			t.Fatalf("client occurrence %q was rewritten as %q", supplied, got)
		}
	}
}

func TestDisplayMirrorNormalizationPinsTheOccurrenceForTheManagerClaim(t *testing.T) {
	h := newTestHub(t)
	first := proto.Interaction{ID: "ix_normalized_mirror", SessionID: "side", Kind: proto.IxPermission,
		MirrorMain: true, CreatedAt: time.Unix(1000, 0).UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	firstMirror := first
	firstMirror.ID, firstMirror.MirrorOf, firstMirror.MirrorMain, firstMirror.SessionID = proto.ApprovalMirrorID(first.ID, first.CreatedAt), first.ID, false, "main"
	reportInteraction(t, h, first)
	reportInteraction(t, h, firstMirror)
	r := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+firstMirror.ID+"/resolve", strings.NewReader(`{"allow":true}`))
	r.SetPathValue("id", firstMirror.ID)
	r.Header.Set(assistantwire.InteractionCreatedAtHeader, first.CreatedAt.Format(time.RFC3339Nano))
	h.normalizeDisplayMirror(r)
	if got := r.PathValue("id"); got != first.ID {
		t.Fatalf("mirror normalized to %q, want %q", got, first.ID)
	}
	raw := r.Header.Get(assistantwire.InteractionCreatedAtHeader)
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil || !at.Equal(first.CreatedAt) {
		t.Fatalf("normalized occurrence = %q, %v; want %s", raw, err, first.CreatedAt)
	}
	// Replace the origin only after normalization. The already-captured request
	// still names the first occurrence for the manager's final claim check.
	reportResolvedAt(t, h, first.ID, first.CreatedAt)
	second := first
	second.CreatedAt = second.CreatedAt.Add(time.Second)
	secondMirror := second
	secondMirror.ID, secondMirror.MirrorOf, secondMirror.MirrorMain, secondMirror.SessionID = proto.ApprovalMirrorID(second.ID, second.CreatedAt), second.ID, false, "main"
	reportInteraction(t, h, second)
	reportInteraction(t, h, secondMirror)
	if got := r.Header.Get(assistantwire.InteractionCreatedAtHeader); got != raw {
		t.Fatalf("replacement changed the in-flight occurrence from %q to %q", raw, got)
	}
}

func TestEarlierOccurrenceResolutionKeepsLaterOriginalAndMirror(t *testing.T) {
	h := newTestHub(t)
	first := proto.Interaction{ID: "ix_resolution_reused", SessionID: "side", Kind: proto.IxPermission,
		MirrorMain: true, CreatedAt: time.Unix(1000, 0).UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	firstMirror := first
	firstMirror.ID, firstMirror.MirrorOf, firstMirror.MirrorMain, firstMirror.SessionID = proto.ApprovalMirrorID(first.ID, first.CreatedAt), first.ID, false, "main"
	reportInteraction(t, h, first)
	reportInteraction(t, h, firstMirror)
	second := first
	second.CreatedAt = second.CreatedAt.Add(time.Second)
	secondMirror := second
	secondMirror.ID, secondMirror.MirrorOf, secondMirror.MirrorMain, secondMirror.SessionID = proto.ApprovalMirrorID(second.ID, second.CreatedAt), second.ID, false, "main"
	reportInteraction(t, h, second)
	reportInteraction(t, h, secondMirror)
	reportResolvedAt(t, h, first.ID, first.CreatedAt)
	pending := h.pendingInteractions()
	if len(pending) != 2 {
		t.Fatalf("an earlier occurrence withdrew the later cards: %+v", pending)
	}
	ids := map[string]bool{}
	for _, ix := range pending {
		ids[ix.ID] = true
	}
	if !ids[second.ID] || !ids[secondMirror.ID] {
		t.Fatalf("later original and mirror are not both pending: %+v", pending)
	}
	reportResolvedAt(t, h, second.ID, second.CreatedAt)
	if got := h.pendingInteractions(); len(got) != 0 {
		t.Fatalf("the matching occurrence did not resolve normally: %+v", got)
	}
}

func TestEarlierOccurrenceUpdateCannotReplaceLaterCards(t *testing.T) {
	h := newTestHub(t)
	first := proto.Interaction{ID: "ix_update_reused", SessionID: "side", Kind: proto.IxPermission,
		MirrorMain: true, Summary: "first", CreatedAt: time.Unix(1000, 0).UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	firstMirror := first
	firstMirror.ID, firstMirror.MirrorOf, firstMirror.MirrorMain, firstMirror.SessionID = proto.ApprovalMirrorID(first.ID, first.CreatedAt), first.ID, false, "main"
	second := first
	second.CreatedAt = first.CreatedAt.Add(time.Second)
	second.Summary = "replacement"
	secondMirror := second
	secondMirror.ID, secondMirror.MirrorOf, secondMirror.MirrorMain, secondMirror.SessionID = proto.ApprovalMirrorID(second.ID, second.CreatedAt), second.ID, false, "main"
	reportInteraction(t, h, second)
	reportInteraction(t, h, secondMirror)
	reportInteraction(t, h, first)
	reportInteraction(t, h, firstMirror)
	pending := h.pendingInteractions()
	if len(pending) != 2 {
		t.Fatalf("earlier occurrence updates replaced the later cards: %+v", pending)
	}
	ids := map[string]proto.Interaction{}
	for _, ix := range pending {
		ids[ix.ID] = ix
	}
	if got := ids[second.ID]; got.Summary != "replacement" || !got.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("delayed original update restored the earlier occurrence: %+v", got)
	}
	if got := ids[secondMirror.ID]; got.Summary != "replacement" || !got.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("delayed mirror update invalidated the replacement copy: %+v", got)
	}
}

func TestEarlierOccurrenceSnapshotCannotReplaceLaterCards(t *testing.T) {
	h := newTestHub(t)
	first := proto.Interaction{ID: "ix_snapshot_reused", SessionID: "side", Kind: proto.IxPermission,
		MirrorMain: true, Summary: "first", CreatedAt: time.Unix(1000, 1).UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	second := first
	second.CreatedAt = first.CreatedAt.Add(time.Nanosecond)
	second.Summary = "replacement"
	secondMirror := second
	secondMirror.ID, secondMirror.MirrorOf, secondMirror.MirrorMain, secondMirror.SessionID = proto.ApprovalMirrorID(second.ID, second.CreatedAt), second.ID, false, "main"
	reportInteraction(t, h, second)
	reportInteraction(t, h, secondMirror)
	h.takeFleetInteractions([]proto.Interaction{first})
	pending := h.pendingInteractions()
	ids := map[string]proto.Interaction{}
	for _, ix := range pending {
		ids[ix.ID] = ix
	}
	if got := ids[second.ID]; got.Summary != "replacement" || !got.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("delayed snapshot restored the earlier original: pending=%+v", pending)
	}
	if got := ids[secondMirror.ID]; got.Summary != "replacement" || !got.CreatedAt.Equal(second.CreatedAt) {
		t.Fatalf("delayed snapshot withdrew the later mirror: pending=%+v", pending)
	}
}

func TestDisplayMirrorSnapshotRejectsPrivateAndOrphanedCopies(t *testing.T) {
	h := newTestHub(t)
	origin := proto.Interaction{ID: "ix_private", SessionID: "side", Kind: proto.IxPermission, MirrorMain: true, Private: true}
	mirror := origin
	mirror.ID, mirror.MirrorOf, mirror.MirrorMain, mirror.SessionID = proto.ApprovalMirrorID(origin.ID, origin.CreatedAt), origin.ID, false, "main"
	h.takeFleetInteractionsFrom(assistantwire.AssistantID, []proto.Interaction{mirror, origin})
	got := h.pendingInteractions()
	if len(got) != 1 || got[0].ID != origin.ID {
		t.Fatalf("private snapshot accepted a display copy: %+v", got)
	}
	h.takeFleetInteractionsFrom(assistantwire.AssistantID, []proto.Interaction{mirror})
	if got := h.pendingInteractions(); len(got) != 0 {
		t.Fatalf("orphaned snapshot accepted a display copy: %+v", got)
	}
}

func TestDisplayMirrorSnapshotKeepsLiveCopyWithoutManagerCopy(t *testing.T) {
	h := newTestHub(t)
	origin := proto.Interaction{ID: "ix_existing", SessionID: "side", Kind: proto.IxPermission, MirrorMain: true, Summary: "before", ExpiresAt: time.Now().Add(time.Minute).UTC()}
	mirror := origin
	mirror.ID, mirror.MirrorOf, mirror.MirrorMain, mirror.SessionID = proto.ApprovalMirrorID(origin.ID, origin.CreatedAt), origin.ID, false, "main"
	mirror.Manager = assistantwire.AssistantID
	origin.Manager = assistantwire.AssistantID
	h.reported.interactions = map[string]proto.Interaction{origin.ID: origin, mirror.ID: mirror}

	// A reconnect after the preference was turned off for new cards still carries
	// the original alone. The hub must not withdraw the copy it already showed,
	// and its retained display state must follow the original's latest deadline.
	updated := origin
	updated.Summary = "after"
	updated.ExpiresAt = origin.ExpiresAt.Add(time.Hour)
	h.takeFleetInteractionsFrom(assistantwire.AssistantID, []proto.Interaction{updated})
	got := h.pendingInteractions()
	if len(got) != 2 {
		t.Fatalf("reconnect withdrew the live display copy: %+v", got)
	}
	ids := map[string]bool{}
	for _, ix := range got {
		ids[ix.ID] = true
		if ix.ID == mirror.ID && (ix.Summary != "after" || !ix.ExpiresAt.Equal(updated.ExpiresAt)) {
			t.Fatalf("retained copy was not refreshed from the original: %+v", ix)
		}
	}
	if !ids[origin.ID] || !ids[mirror.ID] {
		t.Fatalf("reconnect cards = %v, want original and existing copy", ids)
	}
}

func TestDisplayMirrorSnapshotRefreshesContentAndDeduplicatesIDs(t *testing.T) {
	h := newTestHub(t)
	c := &client{out: make(chan []byte, 8), urlFeatures: map[string]bool{featureApprovalMirrors: true}}
	addTestClient(h, c)
	deadline := time.Now().Add(10 * time.Minute).UTC()
	forgedDeadline := deadline.Add(time.Hour)
	origin := proto.Interaction{ID: "ix_snapshot", SessionID: "side", Kind: proto.IxPermission, MirrorMain: true,
		Summary: "authoritative", ExpiresAt: time.Now().Add(time.Minute).UTC(), Deadline: &deadline,
		Questions: []proto.Question{{Question: "authoritative question", Options: []proto.QuestionOption{{Label: "Allow"}}}},
		ToolInput: json.RawMessage(`{"command":"authoritative"}`)}
	mirror := origin
	mirror.ID, mirror.MirrorOf, mirror.MirrorMain, mirror.SessionID = proto.ApprovalMirrorID(origin.ID, origin.CreatedAt), origin.ID, false, "main"
	forged := mirror
	forged.Summary = "forged"
	forged.ExpiresAt = origin.ExpiresAt.Add(time.Hour)
	forged.Deadline = &forgedDeadline
	forged.Questions = []proto.Question{{Question: "forged question", Options: []proto.QuestionOption{{Label: "Deny"}}}}
	forged.ToolInput = json.RawMessage(`{"command":"forged"}`)
	// The last duplicate has always won in the pending map. The ordered event
	// snapshot must contain that same single entry, rebuilt from the original.
	h.takeFleetInteractionsFrom(assistantwire.AssistantID, []proto.Interaction{origin, mirror, forged})
	if got := len(c.out); got != 2 {
		t.Fatalf("duplicate snapshot produced %d events, want original and one copy", got)
	}
	for range 2 {
		_ = wireReceive(t, c)
	}
	got := h.pendingInteractions()
	if len(got) != 2 {
		t.Fatalf("snapshot cards = %+v, want original and one copy", got)
	}
	for _, ix := range got {
		if ix.ID != mirror.ID {
			continue
		}
		if ix.Summary != origin.Summary || !ix.ExpiresAt.Equal(origin.ExpiresAt) || ix.Deadline == nil || !ix.Deadline.Equal(deadline) ||
			len(ix.Questions) != 1 || ix.Questions[0].Question != "authoritative question" || string(ix.ToolInput) != string(origin.ToolInput) {
			t.Fatalf("snapshot copy kept caller-controlled content: %+v", ix)
		}
	}
}

func TestApprovalMirrorsRequireClientCapabilityEverywhere(t *testing.T) {
	h := newTestHub(t)
	legacy := &client{out: make(chan []byte, 16)}
	capable := &client{out: make(chan []byte, 16)}
	addTestClient(h, legacy, capable)
	h.onClientHello(capable, []byte(`{"platform":"android","version":"test","features":["approval_mirrors"]}`))
	<-capable.out // client_hello_ack
	origin := proto.Interaction{ID: "ix_capability", SessionID: "side", Kind: proto.IxPermission, MirrorMain: true}
	mirror := origin
	mirror.ID, mirror.MirrorOf, mirror.MirrorMain, mirror.SessionID = proto.ApprovalMirrorID(origin.ID, origin.CreatedAt), origin.ID, false, "main"
	reportInteraction(t, h, origin)
	reportInteraction(t, h, mirror)
	for range 2 {
		_ = wireReceive(t, capable)
	}
	var event struct {
		Type        string            `json:"type"`
		Interaction proto.Interaction `json:"interaction"`
	}
	if json.Unmarshal(wireReceive(t, legacy), &event) != nil || event.Interaction.ID != origin.ID {
		t.Fatalf("legacy live event = %+v, want only the original", event)
	}
	select {
	case raw := <-legacy.out:
		t.Fatalf("legacy client received a display copy: %s", raw)
	default:
	}

	// The pre-hello snapshot is authoritative-only. A later hello fills in the
	// copies without repeating the originals; a URL-capable client gets both.
	joining := &client{out: make(chan []byte, 16)}
	h.sendPendingInteractions(joining, false)
	if json.Unmarshal(wireReceive(t, joining), &event) != nil || event.Interaction.ID != origin.ID || len(joining.out) != 0 {
		t.Fatalf("legacy initial snapshot = %+v", event)
	}
	h.onClientHello(joining, []byte(`{"features":["approval_mirrors"]}`))
	<-joining.out // client_hello_ack
	if json.Unmarshal(wireReceive(t, joining), &event) != nil || event.Interaction.ID != mirror.ID || len(joining.out) != 0 {
		t.Fatalf("post-hello copy completion = %+v", event)
	}
	joiningCapable := &client{out: make(chan []byte, 16), urlFeatures: map[string]bool{featureApprovalMirrors: true}}
	h.sendPendingInteractions(joiningCapable, false)
	for range 2 {
		_ = wireReceive(t, joiningCapable)
	}
	if len(joiningCapable.out) != 0 {
		t.Fatal("capable initial snapshot omitted or duplicated a card")
	}

	for _, tc := range []struct {
		header string
		want   int
	}{{"", 1}, {"android/test", 2}, {"ios/test", 1}} {
		r := httptest.NewRequest(http.MethodGet, "https://example.com/api/v1/interactions", nil)
		r.RemoteAddr = "127.0.0.1:1234"
		r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		r.Header.Set("X-Persona-Client", tc.header)
		w := httptest.NewRecorder()
		h.routes().ServeHTTP(w, r)
		var body struct {
			Interactions []proto.Interaction `json:"interactions"`
		}
		if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Interactions) != tc.want {
			t.Fatalf("REST %q: %d %s, want %d cards", tc.header, w.Code, w.Body.String(), tc.want)
		}
	}

	// Event replay uses the same opt-in as live delivery. In particular, an old
	// client cannot catch up on a copy it never knew how to deduplicate.
	h.mu.Lock()
	stream := h.events.stream
	replayLegacy := &client{out: make(chan []byte, 16)}
	replayCapable := &client{out: make(chan []byte, 16), urlFeatures: map[string]bool{featureApprovalMirrors: true}}
	h.helloLocked(replayLegacy, wsHello{Type: "hello"}, stream, 0, true)
	h.helloLocked(replayCapable, wsHello{Type: "hello"}, stream, 0, true)
	h.mu.Unlock()
	<-replayLegacy.out  // hello
	<-replayCapable.out // hello
	if json.Unmarshal(wireReceive(t, replayLegacy), &event) != nil || event.Interaction.ID != origin.ID || len(replayLegacy.out) != 0 {
		t.Fatalf("legacy replay included a display copy: %+v", event)
	}
	seen := map[string]bool{}
	for range 2 {
		if json.Unmarshal(wireReceive(t, replayCapable), &event) != nil {
			t.Fatal("bad capable replay event")
		}
		seen[event.Interaction.ID] = true
	}
	if !seen[origin.ID] || !seen[mirror.ID] || len(replayCapable.out) != 0 {
		t.Fatalf("capable replay cards = %v", seen)
	}
}

func TestMirrorResolutionRequiresClientCapabilityLiveAndReplay(t *testing.T) {
	h := newTestHub(t)
	legacy := &client{out: make(chan []byte, 16)}
	capable := &client{out: make(chan []byte, 16), urlFeatures: map[string]bool{featureApprovalMirrors: true}}
	addTestClient(h, legacy, capable)
	origin := proto.Interaction{ID: "ix_resolution_capability", SessionID: "side", Kind: proto.IxPermission, MirrorMain: true,
		CreatedAt: time.Unix(1000, 1).UTC(), ExpiresAt: time.Now().Add(time.Hour).UTC()}
	mirror := origin
	mirror.ID, mirror.MirrorOf, mirror.MirrorMain, mirror.SessionID = proto.ApprovalMirrorID(origin.ID, origin.CreatedAt), origin.ID, false, "main"
	reportInteraction(t, h, origin)
	reportInteraction(t, h, mirror)
	_ = wireReceive(t, legacy)
	for range 2 {
		_ = wireReceive(t, capable)
	}
	reportResolvedAt(t, h, origin.ID, origin.CreatedAt)
	var event struct {
		Type        string            `json:"type"`
		ID          string            `json:"id"`
		Interaction proto.Interaction `json:"interaction"`
	}
	if json.Unmarshal(wireReceive(t, legacy), &event) != nil || event.Type != "interaction_resolved" || event.ID != origin.ID {
		t.Fatalf("legacy live resolution = %+v, want only the original", event)
	}
	select {
	case raw := <-legacy.out:
		t.Fatalf("legacy client received a mirror resolution: %s", raw)
	default:
	}
	resolved := map[string]bool{}
	for range 2 {
		if json.Unmarshal(wireReceive(t, capable), &event) != nil {
			t.Fatal("bad capable resolution")
		}
		resolved[event.ID] = true
	}
	if !resolved[origin.ID] || !resolved[mirror.ID] {
		t.Fatalf("capable resolutions = %v", resolved)
	}

	h.mu.Lock()
	stream := h.events.stream
	replayLegacy := &client{out: make(chan []byte, 16)}
	replayCapable := &client{out: make(chan []byte, 16), urlFeatures: map[string]bool{featureApprovalMirrors: true}}
	h.helloLocked(replayLegacy, wsHello{Type: "hello"}, stream, 0, true)
	h.helloLocked(replayCapable, wsHello{Type: "hello"}, stream, 0, true)
	h.mu.Unlock()
	<-replayLegacy.out
	<-replayCapable.out
	for i, want := range []struct{ typ, id string }{{"interaction", origin.ID}, {"interaction_resolved", origin.ID}} {
		if json.Unmarshal(wireReceive(t, replayLegacy), &event) != nil || event.Type != want.typ || (event.Type == "interaction" && event.Interaction.ID != want.id) || (event.Type == "interaction_resolved" && event.ID != want.id) {
			t.Fatalf("legacy replay event %d = %+v, want %s %s", i, event, want.typ, want.id)
		}
	}
	if len(replayLegacy.out) != 0 {
		t.Fatalf("legacy replay retained a mirror resolution: %d extra events", len(replayLegacy.out))
	}
	for range 4 {
		_ = wireReceive(t, replayCapable)
	}
	if len(replayCapable.out) != 0 {
		t.Fatalf("capable replay has %d extra events", len(replayCapable.out))
	}
}

func TestApprovalMirrorRESTBindsWebCapabilityToClientInstance(t *testing.T) {
	h := newTestHub(t)
	older := &client{out: make(chan []byte, 8)}
	upgraded := &client{out: make(chan []byte, 8)}
	addTestClient(h, older, upgraded)
	h.onClientHello(older, []byte(`{"platform":"web","version":"1"}`))
	h.onClientHello(upgraded, []byte(`{"platform":"web","version":"1","instance":"tab-upgraded","features":["approval_mirrors"]}`))
	<-older.out    // client_hello_ack
	<-upgraded.out // client_hello_ack
	origin, mirror := mirrorPairForTest("ix_web_tab")
	reportInteraction(t, h, origin)
	reportInteraction(t, h, mirror)
	<-older.out // the original only
	for range 2 {
		<-upgraded.out // the original and its copy
	}

	for _, tc := range []struct {
		name     string
		instance string
		want     int
	}{
		{name: "older tab has no instance", want: 1},
		{name: "older tab instance is not the capable connection", instance: "tab-older", want: 1},
		{name: "requesting upgraded tab", instance: "tab-upgraded", want: 2},
		{name: "unknown instance", instance: "tab-unknown", want: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "https://example.com/api/v1/interactions", nil)
			r.RemoteAddr = "127.0.0.1:1234"
			r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
			r.Header.Set("X-Persona-Client", "web/1")
			if tc.instance != "" {
				r.Header.Set("X-Persona-Client-Instance", tc.instance)
			}
			w := httptest.NewRecorder()
			h.routes().ServeHTTP(w, r)
			var body struct {
				Interactions []proto.Interaction `json:"interactions"`
			}
			if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &body) != nil || len(body.Interactions) != tc.want {
				t.Fatalf("REST cards = %d %s, want %d", w.Code, w.Body.String(), tc.want)
			}
		})
	}
}

func TestDisplayMirrorOwnershipMoveWithdrawsCopyWithOrigin(t *testing.T) {
	h := newTestHub(t)
	c := &client{out: make(chan []byte, 8), urlFeatures: map[string]bool{featureApprovalMirrors: true}}
	addTestClient(h, c)
	origin, mirror := mirrorPairForTest("ix_moved_origin")
	origin.Manager, mirror.Manager = "m-old", "m-old"
	mirror.MachineID = "main-machine"
	kept, keptMirror := mirrorPairForTest("ix_unmoved_origin")
	kept.Manager, keptMirror.Manager = "m-new", "m-new"
	kept.MachineID, keptMirror.MachineID = "other", "other"
	h.reported.interactions = map[string]proto.Interaction{
		origin.ID: origin, mirror.ID: mirror, kept.ID: kept, keptMirror.ID: keptMirror,
	}
	h.withdrawCardsOn("box", "m-new")
	got := h.pendingInteractions()
	if len(got) != 2 {
		t.Fatalf("machine move left an orphaned display copy: %+v", got)
	}
	ids := map[string]bool{}
	for _, ix := range got {
		ids[ix.ID] = true
	}
	if !ids[kept.ID] || !ids[keptMirror.ID] {
		t.Fatalf("machine move withdrew the wrong manager's cards: %+v", got)
	}
	resolved := map[string]bool{}
	for range 2 {
		var event struct {
			Type string `json:"type"`
			ID   string `json:"id"`
		}
		if json.Unmarshal(wireReceive(t, c), &event) != nil || event.Type != "interaction_resolved" {
			t.Fatalf("machine move event = %+v", event)
		}
		resolved[event.ID] = true
	}
	if !resolved[origin.ID] || !resolved[mirror.ID] {
		t.Fatalf("machine move resolution events = %v, want original and copy", resolved)
	}
}

func mirrorPairForTest(id string) (proto.Interaction, proto.Interaction) {
	origin := proto.Interaction{ID: id, MachineID: "box", SessionID: "side", Kind: proto.IxPermission,
		MirrorMain: true, CanClarify: true, CreatedAt: time.Unix(1000, 0).UTC()}
	mirror := origin
	mirror.ID, mirror.MirrorOf, mirror.MirrorMain, mirror.SessionID = proto.ApprovalMirrorID(origin.ID, origin.CreatedAt), origin.ID, false, "main"
	return origin, mirror
}

func TestDisplayMirrorIDNamespaceRejectsAuthoritativeCollisions(t *testing.T) {
	for _, snapshot := range []bool{false, true} {
		for _, collisionFirst := range []bool{false, true} {
			name := "live/copy-first"
			if snapshot {
				name = "snapshot/copy-first"
			}
			if collisionFirst {
				name = "live/collision-first"
				if snapshot {
					name = "snapshot/collision-first"
				}
			}
			t.Run(name, func(t *testing.T) {
				h := newTestHub(t)
				c := &client{out: make(chan []byte, 8), urlFeatures: map[string]bool{featureApprovalMirrors: true}}
				addTestClient(h, c)
				origin, mirror := mirrorPairForTest("ix_collision")
				collision := origin
				collision.ID, collision.MirrorMain, collision.SessionID = mirror.ID, false, "other"
				if snapshot {
					list := []proto.Interaction{origin, mirror, collision}
					if collisionFirst {
						list = []proto.Interaction{collision, origin, mirror}
					}
					h.takeFleetInteractionsFrom(assistantwire.AssistantID, list)
				} else {
					if collisionFirst {
						reportInteraction(t, h, collision)
					}
					reportInteraction(t, h, origin)
					reportInteraction(t, h, mirror)
					if !collisionFirst {
						reportInteraction(t, h, collision)
					}
				}
				got := h.pendingInteractions()
				ids := map[string]proto.Interaction{}
				for _, ix := range got {
					ids[ix.ID] = ix
				}
				if len(ids) != 2 || ids[origin.ID].ID != origin.ID || ids[mirror.ID].MirrorOf != origin.ID {
					t.Fatalf("authoritative collision changed display state: %+v", got)
				}
				for len(c.out) > 0 {
					var event struct {
						Type        string            `json:"type"`
						Interaction proto.Interaction `json:"interaction"`
					}
					if json.Unmarshal(wireReceive(t, c), &event) != nil || event.Type != "interaction" || event.Interaction.ID == collision.ID && event.Interaction.MirrorOf == "" {
						t.Fatalf("reserved authoritative ID was broadcast: %+v", event)
					}
				}
			})
		}
	}
}

func TestDisplayMirrorLiveUpdateWithdrawsEveryInvalidCopy(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*proto.Interaction)
	}{
		{name: "display intent removed", edit: func(ix *proto.Interaction) { ix.MirrorMain = false }},
		{name: "kind changed", edit: func(ix *proto.Interaction) { ix.Kind = proto.IxQuestion }},
		{name: "private", edit: func(ix *proto.Interaction) { ix.Private = true }},
		{name: "moved into destination", edit: func(ix *proto.Interaction) { ix.SessionID = "main" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestHub(t)
			c := &client{out: make(chan []byte, 8), urlFeatures: map[string]bool{featureApprovalMirrors: true}}
			addTestClient(h, c)
			origin, mirror := mirrorPairForTest("ix_invalid_update")
			reportInteraction(t, h, origin)
			reportInteraction(t, h, mirror)
			for range 2 {
				_ = wireReceive(t, c)
			}
			updated := origin
			tc.edit(&updated)
			reportInteraction(t, h, updated)
			var originalEvent, resolvedEvent struct {
				Type        string            `json:"type"`
				ID          string            `json:"id"`
				Interaction proto.Interaction `json:"interaction"`
			}
			if json.Unmarshal(wireReceive(t, c), &originalEvent) != nil || originalEvent.Type != "interaction" || originalEvent.Interaction.ID != origin.ID {
				t.Fatalf("original update event = %+v", originalEvent)
			}
			if json.Unmarshal(wireReceive(t, c), &resolvedEvent) != nil || resolvedEvent.Type != "interaction_resolved" || resolvedEvent.ID != mirror.ID {
				t.Fatalf("invalid copy withdrawal = %+v", resolvedEvent)
			}
			got := h.pendingInteractions()
			if len(got) != 1 || got[0].ID != origin.ID || len(c.out) != 0 {
				t.Fatalf("invalid copy stayed actionable: %+v", got)
			}
		})
	}
}

func TestDisplayMirrorOperationReceiptUsesAuthoritativeTarget(t *testing.T) {
	for _, tc := range []struct {
		action, body, op string
	}{
		{action: "resolve", body: `{"allow":true}`, op: "00000000-0000-4000-8000-000000000001"},
		{action: "clarify", body: `{"text":"Why?","client_msg_id":"receipt-question"}`, op: "00000000-0000-4000-8000-000000000002"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			h := newTestHub(t)
			h.store = &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
			got := mirrorManagerRequests(t, h)
			origin, mirror := mirrorPairForTest("ix_receipt")
			h.reported.interactions = map[string]proto.Interaction{origin.ID: origin, mirror.ID: mirror}
			routes := h.routes()
			request := func(id string) *httptest.ResponseRecorder {
				r := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+id+"/"+tc.action, strings.NewReader(tc.body))
				r.RemoteAddr = "127.0.0.1:12345"
				r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
				r.Header.Set("X-Persona-Op-Id", tc.op)
				w := httptest.NewRecorder()
				routes.ServeHTTP(w, r)
				return w
			}
			first := request(mirror.ID)
			if first.Code != http.StatusOK {
				t.Fatalf("first action: %d %s", first.Code, first.Body)
			}
			select {
			case req := <-got:
				want := "/api/v1/interactions/" + origin.ID + "/" + tc.action
				if req.Path != want {
					t.Fatalf("forwarded path = %q, want %q", req.Path, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("first action did not reach the manager")
			}
			if tc.action == "resolve" {
				reportResolved(t, h, origin.ID, "")
			}
			second := request(origin.ID)
			if second.Code != first.Code || second.Body.String() != first.Body.String() {
				t.Fatalf("cross-card retry = %d %s, first = %d %s", second.Code, second.Body, first.Code, first.Body)
			}
			select {
			case req := <-got:
				t.Fatalf("recorded operation reached the manager twice: %+v", req)
			default:
			}
		})
	}
}

func TestDisplayMirrorOperationReceiptSurvivesWithdrawal(t *testing.T) {
	sequences := []struct {
		name                     string
		firstMirror, retryMirror bool
	}{
		{name: "mirror then same stale mirror", firstMirror: true, retryMirror: true},
		{name: "mirror then original", firstMirror: true, retryMirror: false},
		{name: "original then stale mirror", firstMirror: false, retryMirror: true},
	}
	opNumber := 100
	for _, tc := range []struct {
		action, body string
	}{
		{action: "resolve", body: `{"allow":true}`},
		{action: "clarify", body: `{"text":"Why?","client_msg_id":"receipt-question"}`},
	} {
		for _, sequence := range sequences {
			t.Run(tc.action+"/"+sequence.name, func(t *testing.T) {
				opNumber++
				h := newTestHub(t)
				h.store = &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
				got := mirrorManagerRequests(t, h)
				origin, mirror := mirrorPairForTest("ix_withdrawal_receipt")
				origin.Manager, mirror.Manager = assistantwire.AssistantID, assistantwire.AssistantID
				h.reported.interactions = map[string]proto.Interaction{origin.ID: origin, mirror.ID: mirror}
				routes := h.routes()
				request := func(useMirror bool) *httptest.ResponseRecorder {
					id := origin.ID
					if useMirror {
						id = mirror.ID
					}
					r := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+url.PathEscape(id)+"/"+tc.action, strings.NewReader(tc.body))
					r.RemoteAddr = "127.0.0.1:12345"
					r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
					r.Header.Set("X-Persona-Op-Id", fmt.Sprintf("00000000-0000-4000-8000-%012d", opNumber))
					w := httptest.NewRecorder()
					routes.ServeHTTP(w, r)
					return w
				}
				first := request(sequence.firstMirror)
				if first.Code != http.StatusOK {
					t.Fatalf("first action: %d %s", first.Code, first.Body)
				}
				select {
				case req := <-got:
					want := "/api/v1/interactions/" + url.PathEscape(origin.ID) + "/" + tc.action
					if req.Path != want {
						t.Fatalf("forwarded path = %q, want %q", req.Path, want)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("first action did not reach the manager")
				}
				reportResolved(t, h, origin.ID, "")
				if got := h.pendingInteractions(); len(got) != 0 {
					t.Fatalf("resolution did not withdraw original and copy: %+v", got)
				}
				second := request(sequence.retryMirror)
				if second.Code != first.Code || second.Body.String() != first.Body.String() {
					t.Fatalf("retry = %d %s, first = %d %s", second.Code, second.Body, first.Code, first.Body)
				}
				select {
				case req := <-got:
					t.Fatalf("recorded operation reached the manager twice: %+v", req)
				default:
				}
			})
		}
	}
}

func TestDisplayMirrorRecordedReceiptRestoresEscapedOpaquePath(t *testing.T) {
	ops := &clientOpsStore{ops: map[string]db.ClientOp{}}
	h := newTestHub(t)
	h.store = &receiptStore{memStore: newMemStore(), ops: ops}
	origin, mirror := mirrorPairForTest("tenant/approval")
	const opID = "00000000-0000-4000-8000-000000000098"
	target, _ := json.Marshal(map[string]string{"path": "/api/v1/interactions/tenant/approval/resolve", "query": ""})
	ops.ops[opID] = db.ClientOp{ID: opID, Target: target}
	r := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+mirror.ID+"/resolve", strings.NewReader(`{"allow":true}`))
	r.Header.Set("X-Persona-Op-Id", opID)
	r.SetPathValue("id", mirror.ID)
	var gotPath, gotRawPath, gotID string
	h.withNormalizedDisplayMirror(func(_ http.ResponseWriter, r *http.Request) {
		gotPath, gotRawPath, gotID = r.URL.EscapedPath(), r.URL.RawPath, r.PathValue("id")
	})(httptest.NewRecorder(), r)
	if gotID != origin.ID || gotPath != "/api/v1/interactions/tenant%2Fapproval/resolve" ||
		gotRawPath != "/api/v1/interactions/tenant%2Fapproval/resolve" || r.URL.Path != "/api/v1/interactions/tenant/approval/resolve" {
		t.Fatalf("recorded stale-copy normalization = id %q, escaped path %q, RawPath %q, Path %q", gotID, gotPath, gotRawPath, r.URL.Path)
	}
}

func TestDisplayMirrorActionsEscapeOpaqueAuthoritativeID(t *testing.T) {
	for _, tc := range []struct{ action, body string }{
		{action: "resolve", body: `{"allow":true}`},
		{action: "clarify", body: `{"text":"Why?","client_msg_id":"opaque-question"}`},
	} {
		t.Run(tc.action, func(t *testing.T) {
			h := newTestHub(t)
			got := mirrorManagerRequests(t, h)
			origin, mirror := mirrorPairForTest("tenant/approval")
			origin.Manager, mirror.Manager = assistantwire.AssistantID, assistantwire.AssistantID
			h.reported.interactions = map[string]proto.Interaction{origin.ID: origin, mirror.ID: mirror}
			r := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+mirror.ID+"/"+tc.action, strings.NewReader(tc.body))
			r.RemoteAddr = "127.0.0.1:12345"
			r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
			w := httptest.NewRecorder()
			h.routes().ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("action: %d %s", w.Code, w.Body)
			}
			select {
			case req := <-got:
				want := "/api/v1/interactions/tenant%2Fapproval/" + tc.action
				if req.Path != want {
					t.Fatalf("forwarded path = %q, want %q", req.Path, want)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("action did not reach the manager")
			}
		})
	}
}

func TestDisplayMirrorRecordedReceiptRejectsArbitraryAlias(t *testing.T) {
	h := newTestHub(t)
	h.store = &receiptStore{memStore: newMemStore(), ops: &clientOpsStore{ops: map[string]db.ClientOp{}}}
	got := mirrorManagerRequests(t, h)
	origin, mirror := mirrorPairForTest("ix_recorded_alias")
	origin.Manager, mirror.Manager = assistantwire.AssistantID, assistantwire.AssistantID
	h.reported.interactions = map[string]proto.Interaction{origin.ID: origin, mirror.ID: mirror}
	routes := h.routes()
	request := func(id string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+url.PathEscape(id)+"/resolve", strings.NewReader(`{"allow":true}`))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		r.Header.Set("X-Persona-Op-Id", "00000000-0000-4000-8000-000000000099")
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		return w
	}
	if first := request(origin.ID); first.Code != http.StatusOK {
		t.Fatalf("first action: %d %s", first.Code, first.Body)
	}
	select {
	case <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("first action did not reach the manager")
	}
	reportResolved(t, h, origin.ID, "")
	if got := h.pendingInteractions(); len(got) != 0 {
		t.Fatalf("resolution did not withdraw original and copy: %+v", got)
	}
	alias := proto.ApprovalMirrorID("an-unrelated-approval")
	if second := request(alias); second.Code != http.StatusConflict || !strings.Contains(second.Body.String(), "op_id_reused") {
		t.Fatalf("unrelated stale alias borrowed the recorded operation: %d %s", second.Code, second.Body)
	}
	select {
	case req := <-got:
		t.Fatalf("unrelated alias reached the manager: %+v", req)
	default:
	}
}

func mirrorManagerRequests(t *testing.T, h *Hub) <-chan assistantwire.HTTPRequest {
	t.Helper()
	hubIn, assistantOut := io.Pipe()
	assistantIn, hubOut := io.Pipe()
	hp, ap := rpc.New("h", hubOut), rpc.New("a", assistantOut)
	got := make(chan assistantwire.HTTPRequest, 8)
	ap.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		if method != assistantwire.MethodHTTP {
			return nil, errors.New("unexpected " + method)
		}
		var req assistantwire.HTTPRequest
		if err := json.Unmarshal(params, &req); err != nil {
			return nil, err
		}
		got <- req
		return assistantwire.HTTPResponse{Status: http.StatusOK, Body: []byte(`{"ok":true}`)}, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	go hp.Serve(ctx, hubIn)
	go ap.Serve(ctx, assistantIn)
	t.Cleanup(func() {
		cancel()
		hp.Close(io.EOF)
		ap.Close(io.EOF)
		_ = hubIn.Close()
		_ = assistantIn.Close()
		_ = hubOut.Close()
		_ = assistantOut.Close()
	})
	h.bus.current = map[string]*busConn{busAssistant: {peer: hp}}
	return got
}

func TestDisplayMirrorActionsRelayTheAuthoritativePath(t *testing.T) {
	h := newTestHub(t)
	const token = "test-mirror-owner-token"
	h.store = &tokenStore{memStore: *newMemStore(), rows: map[string]db.DeviceToken{
		hashToken(token): {ID: "device-1", Name: "Trusted device", Role: roleFull},
	}, rev: map[string]bool{}}
	got := mirrorManagerRequests(t, h)
	origin := proto.Interaction{ID: "ix_authoritative", MachineID: "box", SessionID: "side", Kind: proto.IxPermission,
		Manager: assistantwire.AssistantID, MirrorMain: true, CanClarify: true, CreatedAt: time.Unix(1500, 0).UTC()}
	mirror := origin
	mirror.ID, mirror.MirrorOf, mirror.MirrorMain, mirror.SessionID = proto.ApprovalMirrorID(origin.ID, origin.CreatedAt), origin.ID, false, "main"
	h.reported.interactions = map[string]proto.Interaction{origin.ID: origin, mirror.ID: mirror}
	routes := h.routes()
	for _, tc := range []struct{ action, body string }{
		{"resolve", `{"allow":true}`},
		{"clarify", `{"text":"Why?","client_msg_id":"mirror-question"}`},
	} {
		r := httptest.NewRequest(http.MethodPost, "/api/v1/interactions/"+mirror.ID+"/"+tc.action, strings.NewReader(tc.body))
		r.RemoteAddr = "127.0.0.1:12345"
		r.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		routes.ServeHTTP(w, r)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", tc.action, w.Code, w.Body)
		}
		select {
		case req := <-got:
			want := "/api/v1/interactions/" + origin.ID + "/" + tc.action
			if req.Path != want {
				t.Fatalf("%s forwarded path = %q, want %q", tc.action, req.Path, want)
			}
			values := req.Header[assistantwire.InteractionCreatedAtHeader]
			raw := ""
			if len(values) > 0 {
				raw = values[0]
			}
			at, err := time.Parse(time.RFC3339Nano, raw)
			if err != nil || !at.Equal(origin.CreatedAt) {
				t.Fatalf("%s forwarded occurrence = %q, %v; want %s", tc.action, raw, err, origin.CreatedAt)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s did not reach the manager", tc.action)
		}
	}
}
