#if os(iOS)
import SwiftUI

/// What one tap of Record starts on the Recordings page (`RecordingCaptureControls`).
enum RecordingStart: Equatable {
    /// `MeetingRecorder.startMeeting` with Persona's part; nil when the hub doesn't offer attending (the one-tap default).
    case meeting(RecordingAttendance?)
    /// The plain start: no calendar name, no minutes, no Persona.
    case note

    /// `kind`'s start with what the page shows: the role, the private mark (with the meeting conversation, #3862) and who
    /// attends (`attendee`: the one picked there and still listed, else the one picked last that isn't the main chat; read
    /// only when it matters). With nobody chosen to attend Persona starts off, and the main chat attends only when picked.
    /// `transcription`: the mode the meeting asks for (#3862 phase 5, `MeetingAttendees.startTranscription`: nil while
    /// the hub doesn't list `meeting_transcription`), only for a room meeting.
    static func make(_ kind: RecordingKindChoice, offered: Bool, conversation: Bool, photos: Bool, liveView: Bool = false,
                     highlights: Bool = false, transcription: MeetingTranscriptionMode? = nil, role: MeetingAttendance,
                     privateMark: Bool,
                     attendee: @autoclosure () -> MeetingAttendees.Candidate?) -> RecordingStart {
        guard kind == .meeting else { return .note }
        guard offered else { return .meeting(nil) }
        // A room meeting whatever the role, off included: every upload carries `room` and the private mark.
        if conversation {
            return .meeting(.room(role, private: privateMark, photos: photos, liveView: liveView, highlights: highlights,
                                  transcription: liveView ? transcription : nil))
        }
        guard role.attends, let chosen = attendee() else { return .meeting(RecordingAttendance(role: .off)) }
        return .meeting(MeetingAttendees.choose(chosen, in: RecordingAttendance(role: role)))
    }
}

/// Meeting / Note for the recording being made: a section of the live sheet's page menu (`RecordingLiveView`, `MeetingPage`),
/// so it stays a secondary choice and never a second button. Turning it into a note turns Persona off; the sheet then
/// shows the page for what it has become (a room meeting's page, or the live view).
struct RecordingKindPicker: View {
    var entry: RecordingEntry

    var body: some View {
        Section("Type") {
            Picker(selection: Binding(get: { RecordingKindChoice(entry) }, set: { kind in
                let id = entry.id
                Task { await MeetingRecorder.shared.setMeeting(kind == .meeting, recordingID: id) }
            })) {
                ForEach(RecordingKindChoice.allCases, id: \.self) { Label($0.title, systemImage: $0.symbol).tag($0) }
            } label: {
                Text("Type")
            }
            .accessibilityIdentifier("recording-kind-now")
        }
    }
}
#endif
