package kb

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

// The storage port tells the recording module's rounds which model errors put the work off (the module cannot name
// the model client's errors): a paused connection, no model set up, a run that ended; any other error fails the call.
func TestRecordingCoreDefersAPausedModel(t *testing.T) {
	core := RecordingCore(goldenEngine(t, nil))
	ctx := context.Background()
	paused := &ai.PausedError{Pause: ai.Pause{Reason: ai.ReasonBusy, Until: time.Now().Add(time.Minute)}}
	if !core.Deferred(ctx, paused) {
		t.Error("a paused connection fails the call")
	}
	if core.Deferred(ctx, errors.New("an answer that is not JSON")) {
		t.Error("any error puts the work off")
	}
	ended, cancel := context.WithCancel(ctx)
	cancel()
	if !core.Deferred(ended, errors.New("an answer that is not JSON")) {
		t.Error("a run that ended fails the call")
	}
}

// The port's closed sets name what the knowledge base's own code names: a file, a change and a meeting part the
// writers pass are the store's.
func TestRecordingPortSetsAreTheStores(t *testing.T) {
	for f, name := range map[recordingwire.File]string{recordingwire.FileHubSegments: hubSegmentsFile,
		recordingwire.FileSpeakerTurns: speakerTurnsFile,
		recordingwire.DirAttachments:   photosDir, recordingwire.FileTranscript: "transcript.md"} {
		if string(f) != name || !f.Known() {
			t.Errorf("file %q: the store's is %q (known %v)", f, name, f.Known())
		}
	}
	for _, p := range []string{PartRoom, PartAsks, PartPrivate, PartFollowup, PartPhotos, PartLines, PartTopics, PartHighlights,
		PartTranscription, PartInterim, PartSpeakers} {
		if !recordingwire.MeetingPart(p).Known() {
			t.Errorf("meeting part %q is not one the port knows", p)
		}
	}
	if recordingwire.MeetingPart("x").Known() || recordingwire.File("photos.json").Known() || recordingwire.Change("x").Known() ||
		!recordingwire.ChangeMessages.Known() || !recordingwire.PurposeTitle.Known() {
		t.Error("a closed set of the port is open, or misses a value")
	}
}

// An engine with no store yet can be hooked (the manager's tests do; SetRecordingProvider allows it): the port answers
// that it had no writers before.
func TestRecordingCoreOfAnEngineWithoutAStore(t *testing.T) {
	if _, none := RecordingCore(&Engine{}).Provider().(recordingwire.NoProvider); !none {
		t.Fatal("an engine without a store had writers")
	}
}
