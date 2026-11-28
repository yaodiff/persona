package recordingwire

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestCalendarKindValid(t *testing.T) {
	if !CalendarEvents.Valid() || !CalendarMeetings.Valid() {
		t.Fatal("expected CalendarEvents and CalendarMeetings to be valid")
	}
	for _, invalid := range []CalendarKind{"", "chats", "recordings"} {
		if invalid.Valid() {
			t.Fatalf("expected %q to be invalid", invalid)
		}
	}
}

func TestRecordingWriteEndedAtOmitzeroAndMeetingKind(t *testing.T) {
	started := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)
	withoutEnd := RecordingWrite{
		ID:        ID("55555555-5555-4555-8555-555555555555"),
		StartedAt: started,
		Meeting:   &RecordingWriteMeeting{Kind: CalendarEvents, ID: ItemID("ev-1")},
	}
	raw, err := json.Marshal(withoutEnd)
	if err != nil {
		t.Fatalf("marshal without end: %v", err)
	}
	if strings.Contains(string(raw), `"ended_at"`) {
		t.Fatalf("expected zero ended_at to be omitted, got %s", raw)
	}
	if !strings.Contains(string(raw), `"kind":"events"`) {
		t.Fatalf("expected meeting kind events, got %s", raw)
	}

	ended := started.Add(30 * time.Minute)
	withEnd := withoutEnd
	withEnd.EndedAt = ended
	raw2, err := json.Marshal(withEnd)
	if err != nil {
		t.Fatalf("marshal with end: %v", err)
	}
	if !strings.Contains(string(raw2), `"ended_at":"2026-11-20T10:30:00Z"`) {
		t.Fatalf("expected ended_at in JSON, got %s", raw2)
	}
}
