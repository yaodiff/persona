package assistantwire

import (
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/recordingwire"
)

const (
	// MethodKBPutRecording creates or updates the calling extension's own recording with its minutes and, optionally,
	// its transcript (kb.write; docs/extensions/extensions.md "Writing recordings and minutes"). Only for extensions.
	MethodKBPutRecording = "kb.put_recording"
	// MethodKBExtensionPutRecording is how the manager hands the assistant an extension's kb.put_recording, in a
	// KBExtensionUpdate envelope with the extension's id, which the extension cannot set. Not on the allowlist.
	MethodKBExtensionPutRecording = "kb.extension_put_recording"
	// CodeKBNotWriter: the recording is the phone's or another extension's.
	CodeKBNotWriter = "permission_denied"
	// CodeKBDeleted: the user deleted the recording; it is not made again.
	CodeKBDeleted = "deleted"
)

// KBExtensionPutRecordingMethod carries a recordingwire.RecordingWrite in the manager's envelope.
var KBExtensionPutRecordingMethod = buswire.Method[KBExtensionUpdate, recordingwire.RecordingWriteResult]{Name: MethodKBExtensionPutRecording}
