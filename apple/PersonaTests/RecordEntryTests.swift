import Foundation
import Testing
@testable import Persona
import PersonaWire

/// One Record entry on the iPhone (#5243, docs/apple/recording.md「Meeting recordings」) and the watch (#5493): Record
/// always records a meeting, the calendar only names it, and Note is a secondary choice: before the start on the
/// Recordings page (`RecordingStart`), and while it records from the live sheet's page menu (`MeetingRecorder.switched`,
/// the pure half of `setMeeting`). What each way round uploads (`kind`, the attendance or the room), what Persona does
/// then, what the watch marks, and that every entry point says Record.
@MainActor struct RecordEntryTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private let agent = SessionKey(machineID: "mac", sessionID: "s1")

    /// A phone recording being made, with what was said so far: two lines, at 10 s and at 200 s.
    private func recording(meeting: RecordingMeeting? = nil, title: String = "", attendance: RecordingAttendance? = nil) -> RecordingEntry {
        var e = RecordingEntry(id: UUID(), createdAt: now, source: .phone, title: title, active: true)
        e.meeting = meeting
        e.attendance = attendance
        e.segments = [RecordingSegment(index: 0, start: 0, duration: 240, sentences: [
            RecordingSentence(start: 2, end: 10, text: "Before it was a meeting."),
            RecordingSentence(start: 190, end: 200, text: "Still before the switch."),
        ])]
        return e
    }

    private func json(_ e: RecordingEntry) throws -> [String: Any] {
        try JSONSerialization.jsonObject(with: JSON.encoder.encode(RecordingUpload(e))) as? [String: Any] ?? [:]
    }

    private func line(endingAt end: Double) -> RecordingSentence { RecordingSentence(start: end - 5, end: end, text: "x") }

    private func candidate(_ key: SessionKey) -> MeetingAttendees.Candidate {
        MeetingAttendees.Candidate(key: key, title: "Export fix", detail: "", main: false, persona: false)
    }

    // MARK: Before the start: Meeting unless Note is picked

    /// Note is a plain start whatever the options say; Meeting starts with what the page shows. Who attends is read only
    /// when it matters (it lists every session).
    @Test func recordStartsAMeetingUnlessNoteIsPicked() {
        var asked = 0
        func attendee() -> MeetingAttendees.Candidate? { asked += 1; return candidate(agent) }

        #expect(RecordingStart.make(.note, offered: true, conversation: true, photos: true, role: .participant, privateMark: true,
                                    attendee: attendee()) == .note)
        #expect(RecordingStart.make(.meeting, offered: false, conversation: false, photos: false, role: .off, privateMark: false,
                                    attendee: attendee()) == .meeting(nil), "an older hub: the one-tap default, as before")
        #expect(RecordingStart.make(.meeting, offered: true, conversation: true, photos: true, role: .listener, privateMark: true,
                                    attendee: attendee()) == .meeting(.room(.listener, private: true, photos: true)))
        #expect(RecordingStart.make(.meeting, offered: true, conversation: true, photos: false, role: .off, privateMark: false,
                                    attendee: attendee()) == .meeting(.room(.off, private: false, photos: false)),
                "a room meeting even when Persona is off")
        #expect(RecordingStart.make(.meeting, offered: true, conversation: false, photos: false, role: .off, privateMark: false,
                                    attendee: attendee()) == .meeting(RecordingAttendance(role: .off)))
        #expect(asked == 0, "who attends is never read for a note, a room meeting or Persona off")

        let picked = RecordingStart.make(.meeting, offered: true, conversation: false, photos: false, role: .listener,
                                         privateMark: false, attendee: attendee())
        #expect(picked == .meeting(MeetingAttendees.choose(candidate(agent), in: RecordingAttendance(role: .listener))))
        #expect(asked == 1)
        let nobody = RecordingStart.make(.meeting, offered: true, conversation: false, photos: false, role: .participant,
                                         privateMark: false, attendee: nil)
        #expect(nobody == .meeting(RecordingAttendance(role: .off)), "nobody chosen: Persona starts off, never the main chat")
    }

    /// The choice's words and marks are the recording row's.
    @Test func theChoiceNamesMeetingAndNote() {
        #expect(RecordingKindChoice.allCases == [.meeting, .note], "Meeting first: the default")
        #expect(RecordingKindChoice.meeting.title == String(localized: "Meeting"))
        #expect(RecordingKindChoice.note.title == String(localized: "Note [Recording Kind]"))
        #expect(RecordingKindChoice(recording(meeting: RecordingMeeting())) == .meeting)
        #expect(RecordingKindChoice(recording()) == .note)
    }

    /// The watch (#5493) reads no calendar: its Record marks a meeting with an empty one for the phone to name, its Note
    /// marks nothing; each reads back as what was picked, and every message to the phone says which it is.
    @Test func theWatchMarksAMeetingAndNotANote() {
        #expect(RecordingKindChoice.meeting.watchMeeting == RecordingMeeting())
        #expect(RecordingKindChoice.note.watchMeeting == nil)
        for kind in RecordingKindChoice.allCases {
            var made = RecordingEntry(id: UUID(), createdAt: now, source: .watch, active: true)
            made.meeting = kind.watchMeeting
            #expect(RecordingKindChoice(made) == kind)
            let segment = RecordingTransfer(entry: made, segment: RecordingSegment(index: 0, start: 0, duration: 30))
            #expect((segment.meeting == true) == (kind == .meeting), "every segment says whether it is a meeting")
            #expect((RecordingTransfer(entry: made).meeting == true) == (kind == .meeting), "and so does the end message")
        }
    }

    // MARK: While it records: Meeting → Note

    /// A #3746 meeting Persona attends becomes a note: `kind: note`, no role or session on the upload (the hub turns Persona
    /// off and writes no minutes), the role off from the switch, nothing more attended. What it heard and spoke stays.
    @Test func aMeetingTurnedIntoANoteDropsPersona() throws {
        var attending = MeetingAttendees.choose(candidate(agent), in: RecordingAttendance(role: .participant))
        attending.spoken = [RecordingSpan(start: 100, end: 110)]
        let meeting = recording(meeting: RecordingMeeting(eventID: "e1", title: "Weekly"), title: "Weekly", attendance: attending)
        #expect(meeting.attended)
        #expect(try json(meeting)["attendance"] as? String == "participant")

        let note = MeetingRecorder.switched(meeting, meeting: false, at: 300, named: nil, start: RecordingAttendance(role: .off))
        #expect(!note.isMeeting && !note.attended)
        let body = try json(note)
        #expect(body["kind"] as? String == "note")
        #expect(body["attendance"] == nil && body["attending_session"] == nil && body["room"] == nil)
        #expect(body["calendar"] == nil, "a note carries no calendar hint")
        #expect(note.title == "Weekly", "its name stays")
        #expect(note.attendance?.role == .off)
        #expect(note.attendance?.periods.last?.end == 300, "attending ended at the switch")
        #expect(note.attendance?.spoken == attending.spoken, "Persona's own voice stays out of the transcript")
        #expect((body["segments"] as? [Any])?.count == 2, "what was said stays the recording's text")
        let activity = RecordingActivity.content(entry: note, recording: true, interrupted: false, elapsed: 300, previous: nil, now: now)
        #expect(!activity.meeting && activity.persona == nil, "the Live Activity says Recording, without Persona")
    }

    /// A room meeting (#3862) becomes a note: its uploads drop `room` with `kind: note` (the hub turns its role off and the
    /// recording stays its room's), the sheet shows the live view (`roomMeeting` false), and the room is kept, off.
    @Test func aRoomMeetingKeepsItsRoomOffAsANote() throws {
        let meeting = recording(meeting: RecordingMeeting(), attendance: .room(.participant, private: false, photos: true))
        #expect(meeting.roomMeeting)
        let note = MeetingRecorder.switched(meeting, meeting: false, at: 300, named: nil, start: RecordingAttendance(role: .off))
        #expect(!note.roomMeeting && !note.attended)
        let body = try json(note)
        #expect(body["kind"] as? String == "note")
        #expect(body["room"] == nil && body["private"] == nil && body["attendance"] == nil)
        #expect(note.attendance?.room == true && note.attendance?.role == .off)
    }

    // MARK: While it records: Note → Meeting

    /// A note becomes a meeting as a start now would make it: named after the event on now (an unnamed one stays
    /// "Meeting · <date time>"), `kind: meeting` with the event as the hint, and Persona's part from the switch on: what
    /// was said before is the meeting's transcript, but no line before it is attended (no live line goes for it).
    @Test func aNoteBecomesAMeetingAsAStartWould() throws {
        let note = recording()
        #expect(note.displayTitle == String(localized: "Recording · \(now.formatted(date: .abbreviated, time: .shortened))"))

        let unnamed = MeetingRecorder.switched(note, meeting: true, at: 240, named: nil, start: RecordingAttendance(role: .off))
        #expect(unnamed.isMeeting && unnamed.title.isEmpty)
        #expect(unnamed.displayTitle == String(localized: "Meeting · \(now.formatted(date: .abbreviated, time: .shortened))"))
        #expect(unnamed.attendance == nil, "Persona off, no room: no attendance, as a start writes none")
        #expect(try json(unnamed)["kind"] as? String == "meeting")
        #expect(try json(unnamed)["calendar"] == nil)

        let event = RecordingMeeting(eventID: "e1", title: "Design review", attendees: ["Alex"])
        let room = MeetingRecorder.switched(note, meeting: true, at: 240, named: event,
                                            start: .room(.listener, private: false, photos: false))
        #expect(room.title == "Design review" && room.meeting == event)
        #expect(room.roomMeeting && room.attended, "the sheet shows the meeting page; Persona listens")
        let body = try json(room)
        #expect(body["kind"] as? String == "meeting")
        #expect((body["room"] as? [String: Any])?["attendance"] as? String == "listener")
        #expect((body["calendar"] as? [String: Any])?["title"] as? String == "Design review")
        #expect((body["segments"] as? [Any])?.count == 2, "what was said before is the meeting's transcript")
        let attendance = try #require(room.attendance)
        #expect(attendance.periods == [RecordingPeriod(start: 240)])
        #expect(!attendance.attended(line(endingAt: 200)), "said before the switch: never a live line")
        #expect(attendance.attended(line(endingAt: 260)))

        let session = MeetingAttendees.choose(candidate(agent), in: RecordingAttendance(role: .listener))
        let older = MeetingRecorder.switched(note, meeting: true, at: 240, named: nil, start: session)
        let olderBody = try json(older)
        #expect(olderBody["attendance"] as? String == "listener")
        #expect((olderBody["attending_session"] as? [String: Any])?["session_id"] as? String == "s1")
        #expect(older.attendance?.periods == [RecordingPeriod(start: 240)])

        let named = MeetingRecorder.switched(recording(title: "Kept"), meeting: true, at: 10, named: event, start: RecordingAttendance(role: .off))
        #expect(named.title == "Kept", "a name the recording has stays")
    }

    /// Meeting → Note → Meeting: the room stays the one it had (a meeting never moves from its room), attending again from
    /// the second switch; the live log the hub has and Persona's spoken stretches stay.
    @Test func aMeetingAgainKeepsWhatItHad() throws {
        var started = RecordingAttendance.room(.participant, private: true, photos: false)
        started.live = [RecordingSentence(start: 2, end: 10, text: "Before it was a meeting.")]
        started.spoken = [RecordingSpan(start: 50, end: 60)]
        let meeting = recording(meeting: RecordingMeeting(), attendance: started)
        let note = MeetingRecorder.switched(meeting, meeting: false, at: 100, named: nil, start: RecordingAttendance(role: .off))
        let again = MeetingRecorder.switched(note, meeting: true, at: 400, named: nil, start: .room(.listener, private: false, photos: false))
        let a = try #require(again.attendance)
        #expect(a.room && a.role == .listener && a.privateMark == true, "the room and its mark as it started")
        #expect(a.periods == [RecordingPeriod(start: 0, end: 100), RecordingPeriod(start: 400)])
        #expect(a.live == started.live && a.spoken == started.spoken)
        #expect((try json(again)["room"] as? [String: Any])?["attendance"] as? String == "listener")

        // A #3746 meeting turned back on takes the attendee a start would take now.
        let picked = MeetingAttendees.choose(candidate(agent), in: RecordingAttendance(role: .listener))
        let resumed = MeetingAttendees.resumed(RecordingAttendance(role: .off, at: 0), start: picked, at: 50)
        #expect(resumed.key == agent && resumed.sentRole == .listener && resumed.periods == [RecordingPeriod(start: 50)])
        let nobody = MeetingAttendees.resumed(RecordingAttendance(role: .off), start: RecordingAttendance(role: .participant), at: 50)
        #expect(nobody.sentRole == .off, "nobody chosen to attend: off, never the main chat")
    }

    /// Asked for what it already is: nothing changes.
    @Test func switchingToWhatItIsChangesNothing() {
        let meeting = recording(meeting: RecordingMeeting(title: "Weekly"), title: "Weekly", attendance: RecordingAttendance(role: .listener))
        #expect(MeetingRecorder.switched(meeting, meeting: true, at: 10, named: nil, start: RecordingAttendance(role: .off)) == meeting)
        let note = recording()
        #expect(MeetingRecorder.switched(note, meeting: false, at: 10, named: nil, start: RecordingAttendance(role: .off)) == note)
    }

    /// Nothing records: the page's switch does nothing (a stale menu after Stop).
    @Test func setMeetingWithoutARecordingDoesNothing() async {
        #expect(RecordingCapture.shared.state == .idle)
        await MeetingRecorder.shared.setMeeting(false, recordingID: UUID())
        #expect(RecordingCapture.shared.state == .idle)
    }

    // MARK: One name

    /// Siri and Shortcuts, the Control and the home screen's menu all say Record, the word of the drawer and the page.
    @Test func everyEntryPointSaysRecord() {
        let record = String(localized: "Record [Recordings Section ef2821]")
        #expect(String(localized: StartMeetingRecordingIntent.title) == record)
        #expect(QuickAction.meetingRecording.item.localizedTitle == record)
        #expect(QuickAction.meetingRecording.rawValue == "persona.meeting-recording", "the type stays: kept home screen items work")
    }
}
