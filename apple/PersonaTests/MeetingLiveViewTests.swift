import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The live view on the phone (#3862 phase 2; contract docs/assistant/meeting-minutes.md「Phase 2: live view」, the phone's
/// side docs/apple/recording.md「Live view (phase 2)」): the flag snapshot, 15-second segments for the meetings Persona
/// attends with it while their lines go, the room's topics read through `MeetingConversation` (on their event, and
/// without the socket after a batch), the chips' identities, and the Live Activity's topic and latest line with its
/// third-line rule and its 5-second throttle. The hub is a `FakeHub`; nothing reaches a network.
@MainActor struct MeetingLiveViewTests {
    // MARK: Fixtures

    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func meeting(_ attendance: RecordingAttendance?, lines: [String] = []) -> RecordingEntry {
        var e = RecordingEntry(id: UUID(), createdAt: now, source: .phone, title: "Weekly", locale: "zh-CN", active: true)
        e.meeting = RecordingMeeting(title: "Weekly")
        e.attendance = attendance
        for (n, text) in lines.enumerated() {
            e.attendance?.live.append(RecordingSentence(start: Double(n * 10), end: Double(n * 10 + 8), text: text))
        }
        return e
    }

    private func live(_ role: MeetingAttendance = .participant, liveView: Bool = true) -> RecordingAttendance {
        RecordingAttendance.room(role, private: false, photos: false, liveView: liveView)
    }

    private func path(_ id: UUID, _ route: String) -> String { "/api/v1/recordings/\(id.uuidString.lowercased())/\(route)" }

    private func until(_ what: String, _ done: () -> Bool, sourceLocation: SourceLocation = #_sourceLocation) async {
        for _ in 0..<500 {
            if done() { return }
            try? await Task.sleep(for: .milliseconds(10))
        }
        #expect(done(), "timed out waiting: \(what)", sourceLocation: sourceLocation)
    }

    private func content(_ e: RecordingEntry, answer: String? = nil, answeredAt: Date? = nil, topic: String? = nil,
                         at: Date? = nil, previous: RecordingActivity.Content? = nil) -> RecordingActivity.Content {
        RecordingActivity.content(entry: e, recording: true, interrupted: false, elapsed: 100, previous: previous, now: at ?? now,
                                  answer: answer, answeredAt: answeredAt, topic: topic)
    }

    // MARK: The flag

    /// `meeting_live_view` is kept from every hello, only beside the conversation, and a start snapshots it on the
    /// recording for life: a meeting keeps what it started with.
    @Test func theFlagIsKeptOnlyWithTheConversationAndSnapshotted() throws {
        let store = UserDefaults(suiteName: "meeting-live-view-\(UUID())")!
        #expect(MeetingAttendanceDefaults.liveViewFeature == "meeting_live_view")
        MeetingAttendanceDefaults.remember(hello: ["meeting_attendance", "meeting_conversation", "meeting_live_view"], role: nil, store)
        #expect(store.bool(forKey: MeetingAttendanceDefaults.liveViewKey))
        MeetingAttendanceDefaults.remember(hello: ["meeting_attendance", "meeting_live_view"], role: nil, store)
        #expect(!store.bool(forKey: MeetingAttendanceDefaults.liveViewKey), "only with meeting_conversation")
        MeetingAttendanceDefaults.remember(hello: ["meeting_attendance", "meeting_conversation"], role: nil, store)
        #expect(!store.bool(forKey: MeetingAttendanceDefaults.liveViewKey))

        let start = RecordingStart.make(.meeting, offered: true, conversation: true, photos: false, liveView: true, role: .listener,
                                        privateMark: false, attendee: nil)
        guard case .meeting(let attendance?) = start else { Issue.record("no room meeting: \(start)"); return }
        #expect(attendance.room && attendance.liveView)
        let old = RecordingStart.make(.meeting, offered: true, conversation: false, photos: false, liveView: true, role: .listener,
                                      privateMark: false, attendee: nil)
        if case .meeting(let a) = old { #expect(a?.liveView != true, "a #3746 meeting has no live view") }

        // Kept with the recording, and read back; a file from before has none.
        let data = try JSON.encoder.encode(live())
        #expect(try JSON.decoder.decode(RecordingAttendance.self, from: data).liveView)
        let before = try JSON.decoder.decode(RecordingAttendance.self, from: Data(#"{"role":"listener","room":true}"#.utf8))
        #expect(!before.liveView)
    }

    // MARK: Segments

    /// 15-second segments in front and behind only while Persona attends a room meeting started with the live view;
    /// every other recording keeps 30 s in front and 60 s behind.
    @Test func onlyAnAttendedLiveViewMeetingWritesShortSegments() {
        #expect(RecordingCapture.segmentLength(short: true, watched: true) == 15)
        #expect(RecordingCapture.segmentLength(short: true, watched: false) == 15, "the background too")
        #expect(RecordingCapture.segmentLength(short: false, watched: true) == 30)
        #expect(RecordingCapture.segmentLength(short: false, watched: false) == 60)
        #expect(RecordingAudioSpec.meetingLiveSegmentSeconds >= 10, "the writer's minimum")

        #expect(live(.participant).shortSegments(roomMissing: false))
        #expect(live(.listener).shortSegments(roomMissing: false))
        #expect(!live(.off).shortSegments(roomMissing: false), "lines go only while Persona attends")
        #expect(!live(.participant, liveView: false).shortSegments(roomMissing: false), "a meeting started without the live view")
        var turnedOff = live(.listener)
        turnedOff.set(.off, at: 120)
        #expect(!turnedOff.shortSegments(roomMissing: false))
        #expect(!RecordingAttendance(role: .participant, session: "server/s1").shortSegments(roomMissing: false), "a #3746 meeting")
    }

    /// Short segments follow the attended state the asks and the activity use (`MeetingAsk.canAsk`), and only while the
    /// lines still go: not while an older manager holds the meeting without its room (`roomMissing`), not after the hub
    /// took its last line (`live_full`) or the phone stopped attending (`unsupported`). `RecordingSync` says when
    /// `roomMissing` moves (`onSegments`), so the recording goes back to the usual length and to 15 s again with the room.
    @Test func noShortSegmentsWhileTheLinesCantGo() async throws {
        #expect(!live().shortSegments(roomMissing: true), "an older manager took the meeting without its room")
        var full = live()
        full.stopped = .liveFull
        #expect(!full.shortSegments(roomMissing: false), "the hub takes no more lines: questions still work")
        #expect(MeetingAsk.canAsk(meeting(full), roomMissing: false), "still attended")
        var unsupported = live()
        unsupported.stopped = .unsupported
        #expect(!unsupported.shortSegments(roomMissing: false))
        var back = full
        back.set(.listener, at: 300)
        #expect(back.shortSegments(roomMissing: false), "attending again clears `stopped`")

        let root = FileManager.default.temporaryDirectory.appending(path: "meeting-live-view-\(UUID())")
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        var e = meeting(live())
        e.segments = [RecordingSegment(index: 0, start: 0, duration: 15, sentences: [RecordingSentence(start: 1, end: 4, text: "line")])]
        try lib.save(e)
        let told = Told()
        let hub = FakeHub()
        hub.on("GET", "/api/v1/recordings", json: #"{"days":14,"recordings":[],"deleted":[]}"#)
        hub.on("PUT", "/api/v1/recordings/\(e.id.uuidString.lowercased())") { req in
            let body = (try? JSONSerialization.jsonObject(with: req.body ?? Data())) as? [String: Any] ?? [:]
            let revision = (body["revision"] as? NSNumber)?.int64Value ?? 0
            let room = told.withRoom ? #","room":{"state":"ready"},"private":false"# : ""
            return Data(#"{"id":"x","revision":\#(revision),"status":"stored","attendance":"participant"\#(room)}"#.utf8)
        }
        let client = hub.client
        let sync = RecordingSync(library: lib, api: { client })
        sync.onSegments = { told.ids.append($0) }

        await sync.syncNow()
        #expect(sync.roomMissing.contains(e.id))
        #expect(told.ids == [e.id], "the recording hears that its lines can't go")
        let waiting = try #require(lib.entry(e.id)?.attendance)
        #expect(!waiting.shortSegments(roomMissing: sync.roomMissing.contains(e.id)))

        told.withRoom = true
        lib.change(e.id) { $0.title = "Weekly, later" }
        await sync.syncNow()
        #expect(!sync.roomMissing.contains(e.id))
        #expect(told.ids == [e.id, e.id], "and that they can again")
        let settled = try #require(lib.entry(e.id)?.attendance)
        #expect(settled.shortSegments(roomMissing: sync.roomMissing.contains(e.id)))
    }

    private final class Told {
        var ids: [UUID] = []
        var withRoom = false
    }

    // MARK: Topics

    /// `topics` reads leniently: an unknown state is open, a topic without a title leaves the list (not the others), a
    /// bad `at` is dropped; `topics_at` comes with them.
    @Test func topicsDecodeLeniently() throws {
        let text = #"""
        {"room":{"state":"ready"},"topics":[
          {"title":"Export cap","state":"done","at":12},
          {"title":"  Pricing ","state":"current","at":"soon","note":"vendor"},
          {"state":"open"},
          {"title":"Hiring","state":"later"},
          42
        ],"topics_at":"2026-11-12T10:00:00Z","revs":{"topics":3,"lines":40}}
        """#
        let read = try JSON.decoder.decode(MeetingConversation.LiveRead.self, from: Data(text.utf8))
        let topics = try #require(read.topics)
        #expect(topics.map(\.title) == ["Export cap", "Pricing", "Hiring"])
        #expect(topics.map(\.known) == [.done, .current, .open])
        #expect(topics[0].at == 12 && topics[1].at == nil && topics[1].note == "vendor")
        #expect(read.topicsAt == "2026-11-12T10:00:00Z")
        #expect(MeetingTopic.current(topics)?.title == "Pricing")
        #expect(read.room?.known == .ready, "the rest of the answer still reads")
        let none = try JSON.decoder.decode(MeetingConversation.LiveRead.self, from: Data(#"{"topics":"x"}"#.utf8))
        #expect(none.topics == nil)
    }

    /// A read that asks for topics (or reads everything) takes the hub's list, an absent one meaning none; a read of
    /// other parts keeps them; a meeting started without the live view never holds any.
    @Test func topicsMergeByWhatWasAsked() {
        let a = MeetingTopic(title: "A", state: "current"), b = MeetingTopic(title: "B", state: "open")
        var m = MeetingConversation.Meeting()
        MeetingConversation.applyTopics(&m, topics: [a, b], at: "t1", replace: true, shown: true)
        #expect(m.topics == [a, b] && m.topicsAt == "t1")
        MeetingConversation.applyTopics(&m, topics: nil, at: nil, replace: false, shown: true)
        #expect(m.topics == [a, b], "a read of other parts keeps them")
        MeetingConversation.applyTopics(&m, topics: nil, at: nil, replace: true, shown: true)
        #expect(m.topics.isEmpty && m.topicsAt == nil, "asked for and absent: none")
        let many = (0..<30).map { MeetingTopic(title: "T\($0)", state: "open") }
        MeetingConversation.applyTopics(&m, topics: many, at: "t2", replace: true, shown: true)
        #expect(m.topics.count == MeetingConversation.maxTopics)
        MeetingConversation.applyTopics(&m, topics: [a], at: "t3", replace: true, shown: false)
        #expect(m.topics.isEmpty, "no live view: no topics")
    }

    /// A meeting started with the live view follows `topics` in `meeting_live` and reads them with `?parts=topics`; one
    /// without it reads neither, and `lines` is never read (the page shows the phone's own lines).
    @Test func aLiveViewMeetingReadsItsTopicsOnTheirEvent() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "meeting-live-view-\(UUID())")
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        let made = meeting(live()), plain = meeting(live(liveView: false))
        try lib.save(made)
        try lib.save(plain)
        let hub = FakeHub()
        let full = #"{"room":{"state":"ready","machine_id":"server","session_id":"room-1"},"asks":[],"topics":[],"topics_at":"2026-11-12T10:00:00Z","revs":{"room":2,"topics":1,"lines":3}}"#
        for id in [made.id, plain.id] {
            hub.on("GET", path(id, "live")) { req in
                if req.query["parts"] == "topics" {
                    return Data(#"{"topics":[{"title":"Export cap","state":"done"},{"title":"Pricing","state":"current","at":300}],"topics_at":"2026-11-12T10:05:00Z","revs":{"topics":2}}"#.utf8)
                }
                return Data(full.utf8)
            }
        }
        let client = hub.client
        let c = MeetingConversation(deps: MeetingConversation.Deps(api: { client }, group: { "" }, now: { .now }, wait: { _ in },
                                                                  readPhotos: { _ in true }),
                                    library: lib, sync: RecordingSync(library: lib, api: { nil }))
        #expect(c.partsRead(for: made.id).contains("topics"))
        #expect(!c.partsRead(for: plain.id).contains("topics"))
        #expect(!c.partsRead(for: made.id).contains("lines"))

        let owner = UUID()
        c.watch(made.id, by: owner)
        await until("the first read") { c.meeting(made.id).loaded }
        #expect(c.meeting(made.id).topics.isEmpty && c.meeting(made.id).revs["topics"] == 1)

        c.event(recordingID: made.id.uuidString.lowercased(), changed: ["lines"], revs: ["lines": 9])
        try await Task.sleep(for: .milliseconds(100))
        #expect(hub.sent("GET", path(made.id, "live")).count == 1, "new lines read nothing")

        c.event(recordingID: made.id.uuidString.lowercased(), changed: ["topics"], revs: ["topics": 2])
        await until("the topics read") { c.meeting(made.id).topics.count == 2 }
        #expect(hub.sent("GET", path(made.id, "live")).last?.query["parts"] == "topics")
        #expect(MeetingTopic.current(c.meeting(made.id).topics)?.title == "Pricing")
        #expect(c.meeting(made.id).topicsAt == "2026-11-12T10:05:00Z")
        #expect(c.meeting(made.id).room?.known == .ready, "the room stays as read")

        c.watch(plain.id, by: owner)
        await until("the plain meeting's read") { c.meeting(plain.id).loaded }
        c.event(recordingID: plain.id.uuidString.lowercased(), changed: ["topics"], revs: ["topics": 2])
        try await Task.sleep(for: .milliseconds(100))
        #expect(hub.sent("GET", path(plain.id, "live")).count == 1, "no live view: topics aren't followed")
        #expect(c.meeting(plain.id).topics.isEmpty)
    }

    // MARK: Topics without the socket

    /// In the background the socket is closed, so no `meeting_live` comes: after an accepted `POST …/live` the topics of a
    /// meeting with the live view that Persona attends while it records are read, at most every 20 s; never while the
    /// socket is open, for a meeting without the live view, one Persona doesn't attend or one no longer recording.
    @Test func theTopicsAreReadBehindOnlyForARecordingLiveViewMeeting() {
        let e = meeting(live())
        #expect(MeetingConversation.readsTopicsBehind(e, roomMissing: false, streaming: false))
        #expect(!MeetingConversation.readsTopicsBehind(e, roomMissing: false, streaming: true), "the events come")
        #expect(!MeetingConversation.readsTopicsBehind(nil, roomMissing: false, streaming: false))
        #expect(!MeetingConversation.readsTopicsBehind(meeting(live(liveView: false)), roomMissing: false, streaming: false))
        #expect(!MeetingConversation.readsTopicsBehind(meeting(live(.off)), roomMissing: false, streaming: false))
        #expect(!MeetingConversation.readsTopicsBehind(e, roomMissing: true, streaming: false))
        var stopped = e
        stopped.active = false
        #expect(!MeetingConversation.readsTopicsBehind(stopped, roomMissing: false, streaming: false), "not recording")

        #expect(MeetingConversation.topicsEvery >= 15 && MeetingConversation.topicsEvery <= 30)
        #expect(MeetingConversation.topicsReadWait(follows: false, lastRead: nil, now: now) == nil)
        #expect(MeetingConversation.topicsReadWait(follows: true, lastRead: nil, now: now) == 0, "the first at once")
        #expect(MeetingConversation.topicsReadWait(follows: true, lastRead: now, now: now.addingTimeInterval(5)) == 15)
        #expect(MeetingConversation.topicsReadWait(follows: true, lastRead: now, now: now.addingTimeInterval(25)) == 0)
    }

    /// An accepted `POST …/live` reads `?parts=topics` and applies them as their event would; the next one within 20 s
    /// waits for the rest of them and reads then. Nothing while the socket is open, nor for a meeting without the view.
    @Test func anAcceptedBatchReadsTheTopicsWithoutTheSocket() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "meeting-live-view-\(UUID())")
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        let made = meeting(live()), plain = meeting(live(liveView: false))
        try lib.save(made)
        try lib.save(plain)
        let hub = FakeHub()
        for id in [made.id, plain.id] {
            hub.on("GET", path(id, "live")) { _ in
                Data(#"{"topics":[{"title":"Export cap","state":"done"},{"title":"Pricing","state":"current","at":300}],"topics_at":"2026-11-12T10:05:00Z","revs":{"topics":2}}"#.utf8)
            }
        }
        let client = hub.client, clock = now, waits = Waits(), socket = Socket()
        let c = MeetingConversation(deps: MeetingConversation.Deps(api: { client }, group: { "" }, now: { clock },
                                                                  wait: { waits.list.append($0) }, readPhotos: { _ in true },
                                                                  streaming: { socket.open }),
                                    library: lib, sync: RecordingSync(library: lib, api: { nil }))
        c.liveAccepted(made.id)
        await until("the topics read") { c.meeting(made.id).topics.count == 2 }
        #expect(hub.sent("GET", path(made.id, "live")).map { $0.query["parts"] } == ["topics"])
        #expect(MeetingTopic.current(c.meeting(made.id).topics)?.title == "Pricing")
        #expect(c.meeting(made.id).revs["topics"] == 2)
        #expect(waits.list.isEmpty, "the first at once")

        c.liveAccepted(made.id)
        await until("the second read") { hub.sent("GET", path(made.id, "live")).count == 2 }
        #expect(waits.list == [.seconds(MeetingConversation.topicsEvery)], "at most every 20 s")

        c.liveAccepted(plain.id)
        socket.open = true
        c.liveAccepted(made.id)
        try await Task.sleep(for: .milliseconds(100))
        #expect(hub.sent("GET", path(plain.id, "live")).isEmpty, "no live view: no read")
        #expect(hub.sent("GET", path(made.id, "live")).count == 2, "the socket is open: the events come")
    }

    private final class Waits { var list: [Duration] = [] }
    private final class Socket { var open = false }

    /// The chips say each state in words (never the color alone), and VoiceOver says it for an open topic too.
    @Test func theTopicChipsSayTheirState() {
        let done = MeetingTopic(title: "Export cap", state: "done"), now = MeetingTopic(title: "Pricing", state: "current")
        let open = MeetingTopic(title: "Hiring", state: "open")
        #expect(MeetingTopicsRow.words(done) != done.title && MeetingTopicsRow.words(done).contains(done.title))
        #expect(MeetingTopicsRow.words(now) != now.title && MeetingTopicsRow.words(now).contains(now.title))
        #expect(MeetingTopicsRow.words(done) != MeetingTopicsRow.words(MeetingTopic(title: "Export cap", state: "current")))
        #expect(MeetingTopicsRow.words(open) == open.title, "its outline tells it apart")
        #expect(MeetingTopicsRow.spoken(open) != open.title && MeetingTopicsRow.spoken(open).contains(open.title))
    }

    /// The chips' identities don't hang on their place (the room inserts and reorders the whole list) nor on the state
    /// or `at` (a topic goes from open to now to done), and stay unique when the room lists a title twice.
    @Test func theTopicsKeepTheirIdentityWhenTheListChanges() {
        let a = MeetingTopic(title: "Export cap", state: "done", at: 12), b = MeetingTopic(title: "Pricing", state: "current", at: 300)
        let c = MeetingTopic(title: "Hiring", state: "open")
        let before = MeetingTopic.listed([a, b, c])
        #expect(before.map(\.id) == ["Export cap", "Pricing", "Hiring"])
        var begun = c
        begun.state = "current"
        begun.at = 900
        var finished = b
        finished.state = "done"
        let after = MeetingTopic.listed([MeetingTopic(title: "Budget", state: "done"), a, finished, begun])
        #expect(after.map(\.id) == ["Budget", "Export cap", "Pricing", "Hiring"], "an insert moves no other topic's identity")
        #expect(after.first { $0.id == "Hiring" }?.topic.known == .current)

        let twice = MeetingTopic.listed([MeetingTopic(title: "Q&A", state: "done"), MeetingTopic(title: "Q&A #2", state: "open"),
                                         MeetingTopic(title: "Q&A", state: "current"), MeetingTopic(title: "Q&A", state: "open")])
        #expect(Set(twice.map(\.id)).count == twice.count, "unique: \(twice.map(\.id))")
        #expect(twice.first?.id == "Q&A")
        #expect(MeetingTopic.listed([]).isEmpty)
    }

    // MARK: The Live Activity

    /// With the live view and Persona attending: the current topic and the latest line of the live log, each cut to the
    /// contract's length. Without the live view, or with Persona off, neither.
    @Test func theActivityCarriesTheTopicAndTheLatestLine() {
        let longTopic = String(repeating: "议", count: 90), longLine = String(repeating: "话", count: 300)
        let c = content(meeting(live(), lines: ["first", longLine]), topic: longTopic)
        #expect(c.topic?.count == RecordingActivity.topicLength && RecordingActivity.topicLength == 60)
        #expect(c.line?.count == RecordingActivity.lineLength && RecordingActivity.lineLength == 120)
        #expect(c.persona == "participant" && c.answer == nil)

        let plain = content(meeting(live(liveView: false), lines: ["first"]), topic: "Pricing")
        #expect(plain.topic == nil && plain.line == nil, "a meeting started without the live view is as before")
        var off = meeting(live(), lines: ["first"])
        off.attendance?.set(.off, at: 50)
        let silent = content(off, topic: "Pricing")
        #expect(silent.topic == nil && silent.line == nil && silent.persona == nil)
        let blank = content(meeting(live(), lines: ["   "]), topic: " ")
        #expect(blank.topic == nil && blank.line == nil)
    }

    /// The third line is the latest answer for 60 s after it came, then the latest line; with no line yet the answer stays
    /// (as it always does without the live view). Only one of the two is ever carried.
    @Test func theAnswerShowsForAMinuteThenTheLine() {
        let e = meeting(live(), lines: ["We keep the cap."])
        let fresh = content(e, answer: "On Nov 3 you kept them through Q3.", answeredAt: now.addingTimeInterval(-59))
        #expect(fresh.answer == "On Nov 3 you kept them through Q3." && fresh.line == nil)
        let old = content(e, answer: "On Nov 3 you kept them through Q3.", answeredAt: now.addingTimeInterval(-60))
        #expect(old.answer == nil && old.line == "We keep the cap.")
        let noLine = content(meeting(live()), answer: "Through Q3.", answeredAt: now.addingTimeInterval(-600))
        #expect(noLine.answer == "Through Q3." && noLine.line == nil)
        let without = content(meeting(live(liveView: false), lines: ["x"]), answer: "Through Q3.", answeredAt: now.addingTimeInterval(-600))
        #expect(without.answer == "Through Q3.", "no live view: the answer stays")
        #expect(content(e).line == "We keep the cap.", "no answer: the line")
    }

    /// An answer, a phase or anything but the topic and the line goes at once; a new line or topic waits until 5 s after
    /// the last update.
    @Test func linesAndTopicsUpdateAtMostEveryFiveSeconds() {
        let e = meeting(live(), lines: ["one"])
        let shown = content(e, topic: "Pricing")
        var line = shown
        line.line = "two"
        #expect(RecordingActivity.hold(line, after: shown, sentAt: now, now: now.addingTimeInterval(2)) == 3)
        #expect(RecordingActivity.hold(line, after: shown, sentAt: now, now: now.addingTimeInterval(5)) == 0)
        var topic = shown
        topic.topic = "Hiring"
        #expect(RecordingActivity.hold(topic, after: shown, sentAt: now, now: now.addingTimeInterval(1)) == 4)
        #expect(RecordingActivity.hold(topic, after: shown, sentAt: nil, now: now) == 0, "nothing sent yet")

        var answer = line
        answer.answer = "Through Q3."
        answer.line = nil
        #expect(RecordingActivity.hold(answer, after: shown, sentAt: now, now: now.addingTimeInterval(1)) == 0, "an answer at once")
        var paused = line
        paused.phase = .paused
        #expect(RecordingActivity.hold(paused, after: shown, sentAt: now, now: now.addingTimeInterval(1)) == 0, "a phase at once")
        var bookmarked = line
        bookmarked.marks += 1
        #expect(RecordingActivity.hold(bookmarked, after: shown, sentAt: now, now: now) == 0)
    }

    /// Everything at its longest, in Chinese (three bytes a character): still far under ActivityKit's 4 KB. A state from
    /// an app before the live view reads without the new fields.
    @Test func theStateStaysSmall() throws {
        var e = meeting(live(), lines: [String(repeating: "长", count: 300)])
        e.title = String(repeating: "会", count: 300)
        e.marks = Array(repeating: 1, count: 500)
        let withLine = content(e, answer: String(repeating: "答", count: 400), answeredAt: now.addingTimeInterval(-61),
                               topic: String(repeating: "议", count: 90))
        let withAnswer = content(e, answer: String(repeating: "答", count: 400), answeredAt: now,
                                 topic: String(repeating: "议", count: 90))
        #expect(withLine.title.count == RecordingActivity.titleLength)
        for state in [withLine, withAnswer] {
            let bytes = try JSONEncoder().encode(state).count
            #expect(bytes < 1536, "\(bytes) bytes")
        }
        let older = #"{"phase":"recording","runningSince":1800000000,"elapsed":0,"title":"Weekly","meeting":true,"marks":0,"persona":"listener"}"#
        let decoded = try JSONDecoder().decode(RecordingActivity.Content.self, from: Data(older.utf8))
        #expect(decoded.topic == nil && decoded.line == nil && decoded.persona == "listener")
    }
}
