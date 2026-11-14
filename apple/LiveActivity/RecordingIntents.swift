import AppIntents
import Foundation

// Recording on the phone without going through the app (docs/apple/recording.md「Meeting recordings」): Siri, Shortcuts, the
// Action button and the Control start a meeting recording (Record, #5243); the recording Live Activity's buttons stop it
// and bookmark it.
// Compiled into the app and the widget extension (the Control and the activity draw them); every body runs in the app.

/// Record: starts a meeting recording, named after the calendar event happening now. An `AudioRecordingIntent`: it may
/// open the microphone without bringing the app to the front, and the recording shows a Live Activity for as long as it
/// lasts (the system stops a recording without one). Before the microphone has ever been allowed it opens the app to ask.
/// The type keeps its name (it was Record a Meeting until #5243): shortcuts people made with it go on working.
struct StartMeetingRecordingIntent: AudioRecordingIntent {
    nonisolated static let title: LocalizedStringResource = "Record [Recordings Section ef2821]"
    // The contract's Wording (#3862 phase 4): a room meeting taking part may send what is said while Talk is held.
    // App Intent metadata says "this device", never "iPhone": App Store Connect rejects it (90626, #6471).
    nonisolated static let description = IntentDescription("Starts recording a meeting in Persona, named after the calendar event happening now. Audio stays on this device, except while you hold Talk: what is said then goes to the voice service. If you set meetings to be transcribed on the hub, the meeting's audio, Talk included, goes through your hub to its speech service, and the hub deletes it once the minutes are written. The transcript goes to Knowledge.")
    nonisolated static var supportedModes: IntentModes { [.background, .foreground(.dynamic)] }

    nonisolated init() {}

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        if await MeetingRecorder.needsForeground { try await continueInForeground(alwaysConfirm: false) }
        // Siri, Shortcuts and the Control say it didn't start (no space, the microphone held by a call), not "done".
        if case .failed(let why) = await MeetingRecorder.shared.startMeeting(inForeground: MeetingRecorder.inForeground) {
            throw RecordingIntentFailure(message: why)
        }
        #endif
        return .result()
    }
}

/// Why a recording didn't start, as Siri and Shortcuts say it.
nonisolated struct RecordingIntentFailure: LocalizedError {
    var message: String
    var errorDescription: String? { message }
}

/// The recording activity's Stop: from the Lock Screen too, without unlocking (as a voice memo's).
struct StopRecordingIntent: LiveActivityIntent {
    nonisolated static let title: LocalizedStringResource = "Stop Recording"
    nonisolated static let isDiscoverable = false
    nonisolated static var authenticationPolicy: IntentAuthenticationPolicy { .alwaysAllowed }

    @Parameter(title: "Recording") var recordingID: String?

    nonisolated init() {}
    nonisolated init(recordingID: String?) { self.recordingID = recordingID }

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        await MeetingRecorder.shared.stop(recordingID: recordingID)
        #endif
        return .result()
    }
}

/// The recording activity's Ask Persona, in a meeting Persona attends (#2981): asks about what was just said. The answer
/// shows in the activity, and taking part it is said out loud through the headset or the loudspeaker. From the Lock
/// Screen too, without unlocking: the hub answers a meeting only from meeting material.
struct AskPersonaInMeetingIntent: LiveActivityIntent {
    nonisolated static let title: LocalizedStringResource = "Ask Persona in the Meeting"
    nonisolated static let isDiscoverable = false
    nonisolated static var authenticationPolicy: IntentAuthenticationPolicy { .alwaysAllowed }

    @Parameter(title: "Recording") var recordingID: String?

    nonisolated init() {}
    nonisolated init(recordingID: String?) { self.recordingID = recordingID }

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        await MeetingRecorder.shared.ask(recordingID: recordingID)
        #endif
        return .result()
    }
}

/// The recording activity's Talk and Done, in a room meeting Persona can be asked in (#3862 phase 4): its buttons can't be
/// held, so a tap on Talk opens a talk window, which ends on Done, 2 seconds of silence after speech, 20 seconds without
/// speech or 60 seconds. With the meeting call live what is said then goes to the voice service; without it the phone
/// transcribes it and asks the room. From the Lock Screen too, without unlocking, as Ask is. `open` carries what the
/// button showed (Talk: true, Done: false), so a Done tapped just after the window closed by itself opens nothing; nil
/// (an activity drawn before the parameter existed) toggles, as it did.
struct TalkInMeetingIntent: LiveActivityIntent {
    nonisolated static let title: LocalizedStringResource = "Talk to Persona in the Meeting"
    nonisolated static let isDiscoverable = false
    nonisolated static var authenticationPolicy: IntentAuthenticationPolicy { .alwaysAllowed }

    @Parameter(title: "Recording") var recordingID: String?
    @Parameter(title: "Open a Talk Window") var open: Bool?

    nonisolated init() {}
    nonisolated init(recordingID: String?, open: Bool? = nil) {
        self.recordingID = recordingID
        self.open = open
    }

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        await MeetingRecorder.shared.talk(recordingID: recordingID, open: open)
        #endif
        return .result()
    }
}

/// The recording activity's Turn Off (#2981): Persona stops attending the meeting; the recording goes on.
struct TurnOffMeetingAttendanceIntent: LiveActivityIntent {
    nonisolated static let title: LocalizedStringResource = "Turn Off Persona in the Meeting"
    nonisolated static let isDiscoverable = false
    nonisolated static var authenticationPolicy: IntentAuthenticationPolicy { .alwaysAllowed }

    @Parameter(title: "Recording") var recordingID: String?

    nonisolated init() {}
    nonisolated init(recordingID: String?) { self.recordingID = recordingID }

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        await MeetingRecorder.shared.setRole(.off, recordingID: recordingID)
        #endif
        return .result()
    }
}

/// The recording activity's Bookmark: marks this moment (the transcript and the minutes show where it was).
struct AddRecordingBookmarkIntent: LiveActivityIntent {
    nonisolated static let title: LocalizedStringResource = "Add a Bookmark"
    nonisolated static let isDiscoverable = false
    nonisolated static var authenticationPolicy: IntentAuthenticationPolicy { .alwaysAllowed }

    @Parameter(title: "Recording") var recordingID: String?

    nonisolated init() {}
    nonisolated init(recordingID: String?) { self.recordingID = recordingID }

    nonisolated func perform() async throws -> some IntentResult {
        #if !WIDGET_EXTENSION
        await MeetingRecorder.shared.addMark(recordingID: recordingID)
        #endif
        return .result()
    }
}
