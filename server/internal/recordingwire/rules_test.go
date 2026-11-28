package recordingwire

import (
	"regexp"
	"testing"
	"time"
)

// RecordingID accepts exactly what the pattern the knowledge base used accepts.
func TestRecordingIDIsAUUID(t *testing.T) {
	re := regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	for _, raw := range []string{"6f1c2a3b-0000-4000-8000-00000000c0de", " 6F1C2A3B-0000-4000-8000-00000000C0DE\n", "",
		"6f1c2a3b-0000-4000-8000-00000000c0d", "6f1c2a3b-0000-4000-8000-00000000c0dee", "6f1c2a3b00000-4000-8000-00000000c0de",
		"6f1c2a3b-0000-4000-8000-00000000c0dg", "6f1c2a3b_0000-4000-8000-00000000c0de", "../../etc/passwd-0000-4000-8000-0000",
		"6f1c2a3b-0000-4000-8000-00000000c0de\n6f1c2a3b-0000-4000-8000-00000000c0de", "６f1c2a3b-0000-4000-8000-00000000c0de"} {
		id, ok := RecordingID(raw)
		if want := re.MatchString(id); ok != want {
			t.Errorf("RecordingID(%q) = %q, %v; the pattern says %v", raw, id, ok, want)
		}
	}
}

func TestIsMeetingAudioPath(t *testing.T) {
	cases := []struct {
		rel  string
		want bool
	}{
		{"recordings/0f8fad5b-d9cb-469f-a165-70867728950e/attachments/audio", true},
		{"recordings/0f8fad5b-d9cb-469f-a165-70867728950e/attachments/audio/", true},
		{"recordings/0f8fad5b-d9cb-469f-a165-70867728950e/attachments/audio/0.pcm", true},
		{"recordings/0f8fad5b-d9cb-469f-a165-70867728950e/attachments/audio/index.json", true},
		{"recordings/sample-meeting/attachments/audio/nested/chunk.pcm", true},
		{"./recordings/sample-meeting/attachments/audio/0.pcm", true},
		{"/recordings/sample-meeting/attachments/audio/0.pcm", true},
		{"recordings/../recordings/sample-meeting/attachments/audio/0.pcm", true},
		{"recordings/0f8fad5b-d9cb-469f-a165-70867728950e/attachments/p1.jpg", false},
		{"recordings/0f8fad5b-d9cb-469f-a165-70867728950e/attachments/audio.jpg", false},
		{"recordings/0f8fad5b-d9cb-469f-a165-70867728950e/attachments", false},
		{"recordings/0f8fad5b-d9cb-469f-a165-70867728950e/hub_segments.json", false},
		{"recordings/attachments/audio/0.pcm", false},
		{"recordings/./attachments/audio/0.pcm", false},
		{"recordings/../chats/c1/attachments/audio/0.pcm", false},
		{"../recordings/sample-meeting/attachments/audio/0.pcm", false},
		{"chats/c1/attachments/audio/note.ogg", false},
		{"", false},
		{".", false},
	}
	for _, tc := range cases {
		if got := IsMeetingAudioPath(tc.rel); got != tc.want {
			t.Errorf("IsMeetingAudioPath(%q) = %v, want %v", tc.rel, got, tc.want)
		}
	}
}

func TestSpeakersWaitDeadlineAndHolds(t *testing.T) {
	base := time.Date(2026, 11, 20, 10, 0, 0, 0, time.UTC)

	// Nil or non-hub or non-waiting never holds.
	if SpeakersHolds(nil, base) {
		t.Fatal("nil recording should not hold")
	}
	deviceRec := &Recording{ID: "r1", Duration: 600, Complete: true}
	if SpeakersHolds(deviceRec, base) {
		t.Fatal("device recording should not hold")
	}

	// Short meeting (60s): dur/5 + 2m = 2m12s < 10m after FinalAt, so 10m after FinalAt wins.
	shortRec := &Recording{
		ID:       "r2",
		Duration: 60,
		Complete: true,
		Conv: &Meeting{
			Transcription: &MeetingTranscription{
				Mode:    TranscriptionHub,
				State:   TranscriptionFinal,
				FinalAt: base,
			},
			Speakers: &MeetingSpeakers{
				State: SpeakerStateWaiting,
				Since: base,
				Tries: 1,
			},
		},
	}
	wantShort := base.Add(SpeakersFinalWait)
	if got := SpeakersWaitDeadline(shortRec); !got.Equal(wantShort) {
		t.Fatalf("short deadline = %v, want %v", got, wantShort)
	}
	if !SpeakersHolds(shortRec, wantShort.Add(-time.Second)) {
		t.Fatal("should hold 1s before short deadline")
	}
	if SpeakersHolds(shortRec, wantShort) {
		t.Fatal("should release at exact short deadline")
	}

	// Long meeting (6000s = 100 min): dur/5 + 2m = 22m after Since > 10m after FinalAt, so Since + 22m wins.
	longRec := &Recording{
		ID:       "r3",
		Duration: 6000,
		Complete: true,
		Conv: &Meeting{
			Transcription: &MeetingTranscription{
				Mode:    TranscriptionHub,
				State:   TranscriptionFinal,
				FinalAt: base,
			},
			Speakers: &MeetingSpeakers{
				State: SpeakerStateWaiting,
				Since: base.Add(time.Minute),
				Tries: 1,
			},
		},
	}
	wantLong := base.Add(time.Minute + 22*time.Minute)
	if got := SpeakersWaitDeadline(longRec); !got.Equal(wantLong) {
		t.Fatalf("long deadline = %v, want %v", got, wantLong)
	}
	if !SpeakersHolds(longRec, base.Add(15*time.Minute)) {
		t.Fatal("long meeting should still hold past 10m when dur/5+2m is later")
	}
	if SpeakersHolds(longRec, wantLong) {
		t.Fatal("long meeting should release at dur/5+2m deadline")
	}

	// Not holding while transcription is still live or finishing (TranscriptionHolds handles that phase).
	shortRec.Conv.Transcription.State = TranscriptionFinishing
	if SpeakersHolds(shortRec, base.Add(time.Minute)) {
		t.Fatal("should not hold before transcription ends")
	}
	// Failed transcription still allows SpeakersHolds when speakers is waiting.
	shortRec.Conv.Transcription.State = TranscriptionFailed
	if !SpeakersHolds(shortRec, base.Add(time.Minute)) {
		t.Fatal("should hold when transcription failed and speakers is waiting")
	}
	// Ready / failed / cleared speakers never hold.
	for _, st := range []SpeakerState{SpeakerStateReady, SpeakerStateFailed, SpeakerStateCleared} {
		shortRec.Conv.Speakers.State = st
		if SpeakersHolds(shortRec, base.Add(time.Minute)) {
			t.Errorf("state %q should not hold", st)
		}
	}
}
