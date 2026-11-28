package kb

import (
	"testing"
	"time"
)

// The text and the audio of recordings have separate retentions (#16412): with recording_text_days forever (0, the
// assistant's default) no recording's text leaves the knowledge base for its age, a meeting's or an extension's
// transcript included, while the audio window still says when the phone drops its copies and tombstones go.
func TestRecordingTextKeptForever(t *testing.T) {
	e := goldenEngine(t, nil)
	e.RecordingDaysFunc = func() int { return 0 } // recording_text_days: forever
	e.AudioDaysFunc = func() int { return 14 }    // recording_audio_days
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, goldenZone)
	start := now.AddDate(0, 0, -400)
	meeting := recUpload(1, start, true, "the meeting's own words")
	meeting.Kind = RecordingMeeting
	if st, err := e.PutRecording(recID, meeting, start.Add(time.Hour)); err != nil || st.Status != RecordingStored {
		t.Fatalf("seed: %v %v", st, err)
	}
	note := "55555555-5555-4555-8555-555555555555"
	if st, err := e.PutRecording(note, recUpload(1, start, true, "a quick note"), start.Add(time.Hour)); err != nil || st.Status != RecordingStored {
		t.Fatalf("seed note: %v %v", st, err)
	}
	ext := "66666666-6666-4666-8666-666666666666"
	w := extMinutesWrite(ext, start, "# An imported meeting")
	w.Transcript = []RecordingSegment{{Start: 0, End: 4, Text: "imported transcript"}}
	if _, err := e.PutExtensionRecording(w, "minutes-ext", start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// A late upload of an old recording is not expired: its text would be kept.
	late := "77777777-7777-4777-8777-777777777777"
	if st, _ := e.PutRecording(late, recUpload(1, start, true, "late"), now); st.Status != RecordingStored {
		t.Fatalf("an old upload with the text kept forever: %v", st.Status)
	}
	deleted := "44444444-4444-4444-8444-444444444444"
	if _, err := e.DeleteRecording(deleted, now.AddDate(0, 0, -20)); err != nil {
		t.Fatal(err)
	}

	if n := e.PruneRecordings(now); n != 0 {
		t.Fatalf("pruned %d with the text kept forever", n)
	}
	for _, id := range []string{recID, note, ext, late} {
		r, ok := e.Store.Recording(id)
		if !ok || r.Trimmed() {
			t.Fatalf("%s: kept %v, trimmed %v", id, ok, ok && r.Trimmed())
		}
		if raw, err := e.Store.recordingRead(id, "transcript.md"); err != nil || len(raw) == 0 {
			t.Fatalf("%s: transcript gone: %v", id, err)
		}
	}
	// The tombstone keeps the audio window, not the text's.
	if _, kept := e.Store.tombstones()[deleted]; kept {
		t.Fatal("a tombstone past the audio window was kept")
	}
	if e.AudioDays() != 14 || e.RecordingDays() != 0 || e.MinutesDays() != 0 {
		t.Fatalf("days: audio %d, text %d, minutes %d", e.AudioDays(), e.RecordingDays(), e.MinutesDays())
	}
}

// A finite text window still trims or deletes what is past it, whatever the audio window says (#16412).
func TestRecordingTextWindowIndependentOfAudio(t *testing.T) {
	e := goldenEngine(t, nil)
	e.RecordingDaysFunc = func() int { return 30 } // recording_text_days
	e.AudioDaysFunc = func() int { return 0 }      // recording_audio_days: forever
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, goldenZone)
	old, recent := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
	if _, err := e.PutRecording(old, recUpload(1, now.AddDate(0, 0, -40), true, "forty days ago"), now.AddDate(0, 0, -40)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PutRecording(recent, recUpload(1, now.AddDate(0, 0, -20), true, "twenty days ago"), now); err != nil {
		t.Fatal(err)
	}
	if st, _ := e.PutRecording("33333333-3333-4333-8333-333333333333", recUpload(1, now.AddDate(0, 0, -31), true, "too old"), now); st.Status != RecordingExpired {
		t.Fatalf("an upload past the text window: %v, want expired", st.Status)
	}
	deleted := "44444444-4444-4444-8444-444444444444"
	if _, err := e.DeleteRecording(deleted, now.AddDate(0, 0, -100)); err != nil {
		t.Fatal(err)
	}
	if n := e.PruneRecordings(now); n != 1 {
		t.Fatalf("pruned %d, want the note past 30 days", n)
	}
	if _, ok := e.Store.Recording(old); ok {
		t.Fatal("a note past the text window was kept")
	}
	if _, ok := e.Store.Recording(recent); !ok {
		t.Fatal("a note within the text window went")
	}
	// Audio kept forever: the phone may still hold a copy, so the tombstone stays.
	if _, kept := e.Store.tombstones()[deleted]; !kept {
		t.Fatal("a tombstone went while the audio is kept forever")
	}
	if e.AudioDays() != 0 {
		t.Fatalf("audio days %d, want forever", e.AudioDays())
	}
}

// Minutes never go before the text they are made of: forever while the text is kept forever (#16412).
func TestMinutesDaysForText(t *testing.T) {
	for _, c := range []struct{ minutes, text, want int }{
		{0, 0, 0}, {30, 0, 0}, {0, 30, 0}, {30, 60, 60}, {90, 60, 90},
	} {
		if got := MinutesDaysFor(c.minutes, c.text); got != c.want {
			t.Errorf("MinutesDaysFor(%d, %d) = %d, want %d", c.minutes, c.text, got, c.want)
		}
	}
}
