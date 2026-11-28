package hub

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extwire"
)

// meeting_attendance is listed while the default manager (the one the recordings' routes go to) is connected and
// named the capability in its hello: not for another manager's, not from an extension's report, not once it is gone.
func TestMeetingAttendanceFollowsTheDefaultManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	a := connectStateManager(t, h, srv, assistantwire.AssistantID)
	b := connectStateManager(t, h, srv, "m-0b0b0b")
	hello := func(m *stateManager, attends bool) {
		t.Helper()
		caps := []string{assistantwire.CapabilityConfigOwn}
		if attends {
			caps = append(caps, assistantwire.CapabilityMeetingAttendance)
		}
		if err := m.call(t, busHello, map[string]any{"id": m.id, "capabilities": caps}, nil); err != nil {
			t.Fatal(err)
		}
	}
	listed := func() bool { return slices.Contains(h.features(), featureMeetingAttendance) }
	if listed() {
		t.Fatal("listed before any manager named it")
	}
	hello(b, true)
	if listed() {
		t.Fatal("listed for a manager that is not the default")
	}
	hello(a, true)
	if !listed() {
		t.Fatal("the default manager names it, and it is not listed")
	}

	// An extension's report cannot add it.
	hello(a, false)
	a.notify(t, extwire.EventReport, extwire.Report{Features: []string{featureMeetingAttendance, "fa"}})
	eventually(t, 5*time.Second, "the report", func() bool { return slices.Contains(h.features(), "fa") })
	if listed() {
		t.Fatal("an extension's report added it")
	}

	// Another manager made the default decides; gone, it is not listed.
	if err := h.managerStore().PutSetting(context.Background(), managerDefaultKey, b.id); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = h.managerStore().PutSetting(context.Background(), managerDefaultKey, assistantwire.AssistantID)
	})
	h.ownersChanged()
	if !listed() {
		t.Fatal("the new default manager names it, and it is not listed")
	}
	if err := b.ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "the default manager offline", func() bool { return h.busConnOf(b.id) == nil })
	if listed() {
		t.Fatal("listed while the default manager is offline")
	}
}

// meeting_conversation is listed only beside meeting_attendance, while the default manager names both.
func TestMeetingConversationFollowsTheDefaultManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	a := connectStateManager(t, h, srv, assistantwire.AssistantID)
	hello := func(caps ...string) {
		t.Helper()
		if err := a.call(t, busHello, map[string]any{"id": a.id, "capabilities": append([]string{assistantwire.CapabilityConfigOwn}, caps...)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	listed := func() bool { return slices.Contains(h.features(), featureMeetingConversation) }
	hello(assistantwire.CapabilityMeetingConversation)
	if listed() {
		t.Fatal("listed without meeting_attendance")
	}
	hello(assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation)
	if !listed() || !slices.Contains(h.features(), featureMeetingAttendance) {
		t.Fatalf("features %v", h.features())
	}
	hello(assistantwire.CapabilityMeetingAttendance)
	if listed() {
		t.Fatal("listed for a manager that does not name it")
	}
}

// meeting_photos is listed only beside meeting_conversation, while the default manager names it too.
func TestMeetingPhotosFollowTheDefaultManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	a := connectStateManager(t, h, srv, assistantwire.AssistantID)
	hello := func(caps ...string) {
		t.Helper()
		if err := a.call(t, busHello, map[string]any{"id": a.id, "capabilities": append([]string{assistantwire.CapabilityConfigOwn}, caps...)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	listed := func() bool { return slices.Contains(h.features(), featureMeetingPhotos) }
	hello(assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingPhotos)
	if listed() {
		t.Fatal("listed without meeting_conversation")
	}
	hello(assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation, assistantwire.CapabilityMeetingPhotos)
	if !listed() || !slices.Contains(h.features(), featureMeetingConversation) {
		t.Fatalf("features %v", h.features())
	}
	hello(assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation)
	if listed() {
		t.Fatal("listed for a manager that does not name it")
	}
}

// meeting_live_view is listed only beside meeting_conversation, while the default manager names it too.
func TestMeetingLiveViewFollowsTheDefaultManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	a := connectStateManager(t, h, srv, assistantwire.AssistantID)
	hello := func(caps ...string) {
		t.Helper()
		if err := a.call(t, busHello, map[string]any{"id": a.id, "capabilities": append([]string{assistantwire.CapabilityConfigOwn}, caps...)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	listed := func() bool { return slices.Contains(h.features(), featureMeetingLiveView) }
	hello(assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingLiveView)
	if listed() {
		t.Fatal("listed without meeting_conversation")
	}
	hello(assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation, assistantwire.CapabilityMeetingLiveView)
	if !listed() || !slices.Contains(h.features(), featureMeetingConversation) || slices.Contains(h.features(), featureMeetingPhotos) {
		t.Fatalf("features %v", h.features())
	}
	hello(assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation)
	if listed() {
		t.Fatal("listed for a manager that does not name it")
	}
}

// meeting_transcription is listed only beside meeting_conversation and meeting_live_view, while the default manager
// names it too: not for another manager's hello, not once the default manager is gone.
func TestMeetingTranscriptionFollowsTheDefaultManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	a := connectStateManager(t, h, srv, assistantwire.AssistantID)
	b := connectStateManager(t, h, srv, "m-0c0c0c")
	hello := func(m *stateManager, caps ...string) {
		t.Helper()
		if err := m.call(t, busHello, map[string]any{"id": m.id, "capabilities": append([]string{assistantwire.CapabilityConfigOwn}, caps...)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	all := []string{assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation, assistantwire.CapabilityMeetingLiveView,
		assistantwire.CapabilityMeetingTranscription}
	listed := func() bool { return slices.Contains(h.features(), featureMeetingTranscription) }
	hello(b, all...)
	hello(a, assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingTranscription)
	if listed() {
		t.Fatal("listed without meeting_conversation, or for a manager that is not the default")
	}
	hello(a, assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation, assistantwire.CapabilityMeetingTranscription)
	if listed() {
		t.Fatal("listed without meeting_live_view")
	}
	hello(a, all...)
	if !listed() || !slices.Contains(h.features(), featureMeetingLiveView) {
		t.Fatalf("features %v", h.features())
	}
	hello(a, all[:3]...)
	if listed() {
		t.Fatal("listed for a manager that does not name it")
	}
	hello(a, all...)
	if err := a.ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "the default manager offline", func() bool { return h.busConnOf(a.id) == nil })
	if listed() {
		t.Fatal("listed while the default manager is offline")
	}
}

// meeting_highlights is listed only beside meeting_live_view (and so meeting_conversation), while the default manager
// names it too.
func TestMeetingHighlightsFollowTheDefaultManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	a := connectStateManager(t, h, srv, assistantwire.AssistantID)
	hello := func(caps ...string) {
		t.Helper()
		if err := a.call(t, busHello, map[string]any{"id": a.id, "capabilities": append([]string{assistantwire.CapabilityConfigOwn}, caps...)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	listed := func() bool { return slices.Contains(h.features(), featureMeetingHighlights) }
	hello(assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation, assistantwire.CapabilityMeetingHighlights)
	if listed() {
		t.Fatal("listed without meeting_live_view")
	}
	hello(assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingLiveView, assistantwire.CapabilityMeetingHighlights)
	if listed() {
		t.Fatal("listed without meeting_conversation")
	}
	hello(assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation, assistantwire.CapabilityMeetingLiveView,
		assistantwire.CapabilityMeetingHighlights)
	if !listed() || !slices.Contains(h.features(), featureMeetingLiveView) || slices.Contains(h.features(), featureMeetingPhotos) {
		t.Fatalf("features %v", h.features())
	}
	hello(assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation, assistantwire.CapabilityMeetingLiveView)
	if listed() || !slices.Contains(h.features(), featureMeetingLiveView) {
		t.Fatalf("listed for a manager that does not name it: %v", h.features())
	}
}

// meeting_notes is listed only beside meeting_live_view (and so meeting_conversation), while the default manager names
// it too (#8851): a phone sends the notes role to no manager that would read it as off.
func TestMeetingNotesFollowsTheDefaultManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	a := connectStateManager(t, h, srv, assistantwire.AssistantID)
	hello := func(caps ...string) {
		t.Helper()
		if err := a.call(t, busHello, map[string]any{"id": a.id, "capabilities": append([]string{assistantwire.CapabilityConfigOwn}, caps...)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	listed := func() bool { return slices.Contains(h.features(), featureMeetingNotes) }
	hello(assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation, assistantwire.CapabilityMeetingNotes)
	if listed() {
		t.Fatal("listed without meeting_live_view")
	}
	hello(assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation, assistantwire.CapabilityMeetingLiveView,
		assistantwire.CapabilityMeetingNotes)
	if !listed() || slices.Contains(h.features(), featureMeetingHighlights) {
		t.Fatalf("features %v", h.features())
	}
	hello(assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation, assistantwire.CapabilityMeetingLiveView)
	if listed() {
		t.Fatalf("listed for a manager that does not name it: %v", h.features())
	}
}

// meeting_echo is listed only beside meeting_attendance, while the default manager names it too (#5905).
func TestMeetingEchoFollowsTheDefaultManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	a := connectStateManager(t, h, srv, assistantwire.AssistantID)
	hello := func(caps ...string) {
		t.Helper()
		if err := a.call(t, busHello, map[string]any{"id": a.id, "capabilities": append([]string{assistantwire.CapabilityConfigOwn}, caps...)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	listed := func() bool { return slices.Contains(h.features(), featureMeetingEcho) }
	hello(assistantwire.CapabilityMeetingEcho)
	if listed() {
		t.Fatal("listed without meeting_attendance")
	}
	hello(assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingEcho)
	if !listed() || slices.Contains(h.features(), featureMeetingConversation) {
		t.Fatalf("features %v", h.features())
	}
	hello(assistantwire.CapabilityMeetingAttendance)
	if listed() {
		t.Fatal("listed for a manager that does not name it")
	}
}

// meeting_speakers is listed only beside meeting_transcription (and so meeting_live_view, meeting_conversation and
// meeting_attendance), while the default manager names it too: not for another manager's hello, not once the default
// manager is gone (#17674).
func TestMeetingSpeakersFollowsTheDefaultManager(t *testing.T) {
	h, srv, _ := newBusHub(t)
	a := connectStateManager(t, h, srv, assistantwire.AssistantID)
	b := connectStateManager(t, h, srv, "m-0d0d0d")
	hello := func(m *stateManager, caps ...string) {
		t.Helper()
		if err := m.call(t, busHello, map[string]any{"id": m.id, "capabilities": append([]string{assistantwire.CapabilityConfigOwn}, caps...)}, nil); err != nil {
			t.Fatal(err)
		}
	}
	all := []string{assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation,
		assistantwire.CapabilityMeetingLiveView, assistantwire.CapabilityMeetingTranscription, assistantwire.CapabilityMeetingSpeakers}
	listed := func() bool { return slices.Contains(h.features(), featureMeetingSpeakers) }
	hello(b, all...)
	hello(a, assistantwire.CapabilityMeetingAttendance, assistantwire.CapabilityMeetingConversation,
		assistantwire.CapabilityMeetingLiveView, assistantwire.CapabilityMeetingSpeakers)
	if listed() {
		t.Fatal("listed without meeting_transcription, or for a manager that is not the default")
	}
	hello(a, all...)
	if !listed() || !slices.Contains(h.features(), featureMeetingTranscription) {
		t.Fatalf("features %v", h.features())
	}
	hello(a, all[:4]...)
	if listed() {
		t.Fatal("listed for a manager that does not name it")
	}
	hello(a, all...)
	if err := a.ws.CloseNow(); err != nil {
		t.Fatal(err)
	}
	eventually(t, 5*time.Second, "the default manager offline", func() bool { return h.busConnOf(a.id) == nil })
	if listed() {
		t.Fatal("listed while the default manager is offline")
	}
}

// An extension's report cannot add a meeting feature: the hub lists them only from the default manager's hello.
func TestMeetingFeaturesNotFromAReport(t *testing.T) {
	h := newTestHub(t)
	h.keepExtReport(assistantwire.AssistantID, &extwire.Report{Features: []string{featureMeetingAttendance, featureMeetingConversation,
		featureMeetingPhotos, featureMeetingLiveView, featureMeetingHighlights, featureMeetingNotes, featureMeetingEcho, featureMeetingTranscription, featureMeetingSpeakers, "fa"}})
	features := h.features()
	if !slices.Contains(features, "fa") {
		t.Fatalf("the report's own feature is missing: %v", features)
	}
	for _, f := range []string{featureMeetingAttendance, featureMeetingConversation, featureMeetingPhotos, featureMeetingLiveView, featureMeetingHighlights,
		featureMeetingNotes, featureMeetingEcho, featureMeetingTranscription, featureMeetingSpeakers} {
		if slices.Contains(features, f) {
			t.Errorf("a report added %s", f)
		}
	}
}
