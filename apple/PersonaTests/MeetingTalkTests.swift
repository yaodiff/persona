import Foundation
import Testing
import UserNotifications
@testable import Persona
import PersonaWire

/// Talk about this meeting (#3862 phase 6; contract docs/assistant/meeting-minutes.md「Phase 6: talk about this meeting」,
/// the phone's side docs/apple/recording.md「Talk about this meeting (phase 6)」): which recordings' pages offer it, that a
/// recording that never had a room writes its follow-up at once (no room to wait for), and where a tapped minutes push
/// goes. The hub is a `FakeHub`; nothing reaches a network.
@MainActor struct MeetingTalkTests {
    // MARK: Fixtures

    private func recording(meeting: Bool = false, active: Bool = false, kind: RecordingEntry.Kind? = nil,
                           attendance: RecordingAttendance? = nil, lines: [String] = ["We keep the 4 MB cap."]) -> RecordingEntry {
        var e = RecordingEntry(id: UUID(), createdAt: Date(timeIntervalSince1970: 1_800_000_000), source: .phone, title: "Weekly",
                               locale: "en-US", active: active)
        if meeting { e.meeting = RecordingMeeting(title: "Weekly") }
        if let kind { e.kind = kind }
        e.attendance = attendance
        let sentences = lines.enumerated().map { RecordingSentence(start: Double($0.offset * 5), end: Double($0.offset * 5 + 4), text: $0.element) }
        if !sentences.isEmpty { e.segments = [RecordingSegment(index: 0, start: 0, duration: 60, sentences: sentences)] }
        return e
    }

    private func offered(_ e: RecordingEntry, conversation: Bool = true, minutes: Bool = false,
                         meeting: MeetingConversation.Meeting = .init(), expired: Bool = false) -> Bool {
        MeetingTalk.offered(e, conversation: conversation, minutes: minutes, meeting: meeting, text: MeetingTalk.hasText(e),
                            expired: expired)
    }

    private func path(_ id: UUID, _ route: String) -> String { "/api/v1/recordings/\(id.uuidString.lowercased())/\(route)" }

    private func until(_ what: String, _ done: () -> Bool, sourceLocation: SourceLocation = #_sourceLocation) async {
        for _ in 0..<500 {
            if done() { return }
            try? await Task.sleep(for: .milliseconds(10))
        }
        #expect(done(), "timed out waiting: \(what)", sourceLocation: sourceLocation)
    }

    // MARK: Which recordings offer it

    /// Any recording that ended with words in it: a note, a meeting no room attended, a room meeting; whatever recorded it.
    @Test func anyFinishedRecordingWithTextOffersIt() {
        #expect(offered(recording()), "a note")
        #expect(offered(recording(meeting: true)), "a meeting no room attended (#3746, or recorded before phase 1)")
        #expect(offered(recording(meeting: true, attendance: .room(.listener, private: false, photos: false))), "a room meeting")
        var watch = recording()
        watch.source = .watch
        #expect(offered(watch), "the watch's")
    }

    /// Only on a hub with the meeting conversation (phase 1's `…/followup`), and not while it records, nor for a rolling or
    /// an all-day recording, nor one with nothing to talk about.
    @Test func itNeedsTheConversationAnEndAndMaterial() {
        #expect(!offered(recording(), conversation: false), "a hub without …/followup")
        #expect(!offered(recording(active: true)), "still recording")
        #expect(!offered(recording(kind: .rolling)), "a rolling recording")
        #expect(!offered(recording(kind: .day)), "an all-day recording")
        #expect(!offered(recording(lines: [])), "no transcript yet and no minutes")
        #expect(!offered(recording(lines: ["  ", "\n"])), "only blank lines")
    }

    /// Minutes, a follow-up already made or a room the hub reports are material enough without a transcript here (a
    /// transcript past its retention keeps its minutes).
    @Test func minutesOrAConversationTheHubHoldsOfferItWithoutText() throws {
        let bare = recording(meeting: true, lines: [])
        #expect(offered(bare, minutes: true))
        var followup = MeetingConversation.Meeting()
        followup.followup = RecordingUpload.SessionRef(machineID: "server", sessionID: "f1")
        #expect(offered(bare, meeting: followup))
        var room = MeetingConversation.Meeting()
        room.room = try JSON.decoder.decode(MeetingRoom.self, from: Data(#"{"state":"ended"}"#.utf8))
        #expect(offered(bare, meeting: room))
        var none = MeetingConversation.Meeting()
        none.room = try JSON.decoder.decode(MeetingRoom.self, from: Data(#"{"state":"none"}"#.utf8))
        #expect(!offered(bare, meeting: none), "a room that never came up holds nothing")
        #expect(offered(recording(meeting: true, attendance: .room(.off, private: false, photos: false), lines: [])),
                "a room meeting has its thread")
    }

    /// Past retention (`RecordingSync.Status.expired`) the hub has cut the transcript, or deleted the recording, and the
    /// room with it: the lines still on this phone are no material. Minutes, a follow-up, or a room whose conversation the
    /// hub still has (its cut not run yet) are.
    @Test func pastRetentionTheTranscriptHereIsNoMaterial() throws {
        #expect(!offered(recording(), expired: true), "a note's words on this phone only")
        #expect(!offered(recording(meeting: true, attendance: .room(.listener, private: false, photos: false)), expired: true),
                "a room meeting whose room the hub doesn't report")
        #expect(offered(recording(), minutes: true, expired: true), "the minutes outlive the transcript")
        var followup = MeetingConversation.Meeting()
        followup.followup = RecordingUpload.SessionRef(machineID: "server", sessionID: "f1")
        #expect(offered(recording(), meeting: followup, expired: true), "retention never deletes the follow-up")
        var room = MeetingConversation.Meeting()
        room.room = try JSON.decoder.decode(MeetingRoom.self, from: Data(#"{"state":"ended","machine_id":"server","session_id":"r1"}"#.utf8))
        #expect(offered(recording(meeting: true), meeting: room, expired: true), "a room the hub still has")
        room.room = try JSON.decoder.decode(MeetingRoom.self, from: Data(#"{"state":"deleted"}"#.utf8))
        #expect(!offered(recording(meeting: true), meeting: room, expired: true), "the room went at the transcript's cut")
        #expect(offered(recording(meeting: true), meeting: room), "within retention, as before")
    }

    /// `hasText` says what `sentences` holds that isn't blank, without building it: any segment, in any order, and not the
    /// lines Persona said out loud in the meeting.
    @Test func hasTextIsTheTranscriptsWords() throws {
        #expect(MeetingTalk.hasText(recording()))
        #expect(!MeetingTalk.hasText(recording(lines: [])))
        #expect(!MeetingTalk.hasText(recording(lines: [" ", "\n\t"])))
        var later = recording(lines: [])
        later.segments = [RecordingSegment(index: 1, start: 60, duration: 60, sentences: [RecordingSentence(start: 61, end: 64, text: "Ship it.")]),
                          RecordingSegment(index: 0, start: 0, duration: 60, sentences: nil)]
        #expect(MeetingTalk.hasText(later), "a segment not transcribed yet doesn't hide another's words")
        var spoken = recording(meeting: true, attendance: .room(.participant, private: false, photos: false), lines: ["You kept the cap."])
        spoken.attendance?.spoken = [try JSONDecoder().decode(RecordingSpan.self, from: Data(#"{"start":0,"end":4,"text":"You kept the cap."}"#.utf8))]
        #expect(spoken.sentences.isEmpty)
        #expect(!MeetingTalk.hasText(spoken), "Persona's echo isn't the meeting's")
        var other = spoken
        other.segments[0].sentences?.append(RecordingSentence(start: 2, end: 3.8, text: "Who owns the test?"))
        #expect(MeetingTalk.hasText(other), "someone speaking while Persona answers is (#5905)")
        spoken.segments[0].sentences?.append(RecordingSentence(start: 10, end: 14, text: "Who owns the test?"))
        #expect(MeetingTalk.hasText(spoken))
        var old = recording(meeting: true, attendance: .room(.participant, private: false, photos: false), lines: ["Who owns the test?"])
        old.attendance?.spoken = [RecordingSpan(start: 0, end: 4)]
        #expect(!MeetingTalk.hasText(old), "a stretch saved without its answer keeps the time rule")
    }

    // MARK: Writing the follow-up of a recording without a room

    /// A note the hub has whole: nothing to wait for (no room, the end stored), so the first message goes to `…/followup`
    /// at once and makes the follow-up.
    @Test func aNoteWritesItsFollowUpAtOnce() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "meeting-talk-\(UUID())")
        let lib = RecordingLibrary(root: root)
        try lib.save(recording())
        let note = try #require(lib.entries.first)
        var store = RecordingSync.Store()
        store.synced[note.id] = note.revision
        try JSONEncoder().encode(store).write(to: root.appendingPathComponent("sync.json"))
        let sync = RecordingSync(library: lib, api: { nil })
        #expect(sync.status(note) == .synced)

        let hub = FakeHub()
        hub.on("GET", path(note.id, "live"), json: #"{"attendance":"off","next":0,"fed":0,"asks":[]}"#)
        hub.on("POST", path(note.id, "followup")) { req in
            let id = (try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any])?["id"] as? String ?? ""
            return Data(#"{"followup":{"machine_id":"server","session_id":"f1"},"created":true,"client_msg_id":"followup-\#(id)"}"#.utf8)
        }
        let client = hub.client
        let c = MeetingConversation(deps: MeetingConversation.Deps(api: { client }, group: { "" }, now: { .now }, wait: { _ in }),
                                    library: lib, sync: sync)
        #expect(!c.running(note), "no room to wait for before any read")
        await c.refresh(note.id)
        #expect(!c.running(note))
        #expect(c.followupHold(note) == nil, "the composer takes words")

        let sent = try #require(c.sendFollowup(note.id, text: "What did we decide?"))
        await until("made") { c.followups(for: note.id).first?.state == .sent }
        #expect(hub.sent("POST", path(note.id, "followup")).count == 1)
        #expect(c.followupKey(note.id) == SessionKey(machineID: "server", sessionID: "f1"))
        #expect(c.followups(for: note.id).first?.id == sent)
    }

    // MARK: The minutes push

    /// The push's `recording_id` (lowercase from the hub; any case reads), nothing without it or when it isn't a UUID.
    @Test func thePushNamesItsRecording() {
        let id = UUID()
        #expect(MeetingTalk.recordingID(["recording_id": id.uuidString.lowercased(), "machine_id": "m", "session_id": "s"]) == id)
        #expect(MeetingTalk.recordingID(["recording_id": " \(id.uuidString) "]) == id)
        #expect(MeetingTalk.recordingID(["machine_id": "m", "session_id": "s"]) == nil, "an older hub's push")
        #expect(MeetingTalk.recordingID(["recording_id": "rec-1"]) == nil)
        #expect(MeetingTalk.recordingID(["recording_id": 42]) == nil)
    }

    /// A tap opens the meeting's thread only for a recording on this phone; a dismissal, a card's push and a recording made
    /// elsewhere (or a push without the field) open what they open today.
    @Test func aTapOpensTheThreadOfARecordingOnThisPhone() {
        let here = UUID(), elsewhere = UUID()
        let onPhone: (UUID) -> Bool = { $0 == here }
        let open = UNNotificationDefaultActionIdentifier
        #expect(MeetingTalk.threadToOpen(here, interaction: nil, action: open, onPhone: onPhone) == here)
        #expect(MeetingTalk.threadToOpen(elsewhere, interaction: nil, action: open, onPhone: onPhone) == nil, "the main chat, as before")
        #expect(MeetingTalk.threadToOpen(nil, interaction: nil, action: open, onPhone: onPhone) == nil, "no field: as before")
        #expect(MeetingTalk.threadToOpen(here, interaction: nil, action: UNNotificationDismissActionIdentifier, onPhone: onPhone) == nil)
        #expect(MeetingTalk.threadToOpen(here, interaction: "ix-1", action: open, onPhone: onPhone) == nil, "a card's push")
    }

    /// The page asked for is taken once, by the Recordings page that appears; one nobody took soon is dropped.
    @Test func theOpenedPageIsTakenOnceAndNotLate() {
        let opener = MeetingPageOpener()
        let id = UUID(), now = Date(timeIntervalSince1970: 1_800_000_000)
        #expect(opener.take(now: now) == nil)
        opener.open(id, now: now)
        #expect(opener.serial == 1)
        #expect(opener.take(now: now.addingTimeInterval(2)) == id)
        #expect(opener.take(now: now.addingTimeInterval(2)) == nil, "once")
        opener.open(id, now: now)
        #expect(opener.take(now: now.addingTimeInterval(MeetingPageOpener.stale + 1)) == nil, "too late: a launch that never got there")
    }

    /// With a recording running, the live sheet doesn't present itself over the meeting the push opened: the next appear
    /// skips it, once and only soon after; asking for the sheet (the Live Activity, the now-recording card) cancels that.
    @Test func thePushedMeetingIsNotCoveredByTheLiveSheet() {
        let presenter = RecordingLivePresenter()
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        #expect(presenter.presentsOnAppear(now: now))
        presenter.holdOnAppear(now: now)
        #expect(!presenter.presentsOnAppear(now: now.addingTimeInterval(1)), "the page the push opened")
        #expect(presenter.presentsOnAppear(now: now.addingTimeInterval(2)), "once")
        presenter.holdOnAppear(now: now)
        #expect(presenter.presentsOnAppear(now: now.addingTimeInterval(MeetingPageOpener.stale + 1)), "a hold nobody took soon")
        presenter.holdOnAppear(now: now)
        presenter.open()
        #expect(presenter.serial == 1)
        #expect(presenter.presentsOnAppear(now: now.addingTimeInterval(1)), "asked for: it presents")
    }
}
