import AppIntents
import Foundation
import Testing
@testable import Persona

/// One-tap meeting recordings (docs/apple/recording.md「Meeting recordings」): which calendar event names one, what its
/// Live Activity shows, its intents' rules, and what a meeting adds to a recording.
@MainActor struct MeetingRecordingTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func event(_ title: String, from start: Double, to end: Double, allDay: Bool = false, declined: Bool = false) -> MeetingCalendar.Candidate {
        MeetingCalendar.Candidate(id: title, title: title, start: now.addingTimeInterval(start * 60), end: now.addingTimeInterval(end * 60),
                                  allDay: allDay, declined: declined)
    }

    // MARK: The calendar's meeting

    @Test func picksTheMeetingOnNow() {
        let events = [event("Earlier", from: -120, to: -60), event("Weekly", from: -10, to: 50), event("Later", from: 30, to: 60)]
        #expect(MeetingCalendar.pick(events, at: now)?.title == "Weekly")
    }

    /// All day, declined or untitled: not a meeting in the room.
    @Test func skipsAllDayDeclinedAndUntitled() {
        let events = [event("Holiday", from: -600, to: 600, allDay: true), event("Declined", from: -5, to: 25, declined: true),
                      event("  ", from: -5, to: 25)]
        #expect(MeetingCalendar.pick(events, at: now) == nil)
    }

    /// Back to back: the one that just began, not the one running over.
    @Test func prefersTheOneThatStartedLast() {
        let events = [event("Overrunning", from: -60, to: 5), event("Next", from: -1, to: 30)]
        #expect(MeetingCalendar.pick(events, at: now)?.title == "Next")
    }

    /// People sit down a few minutes early; not ten.
    @Test func countsAMeetingAboutToStart() {
        #expect(MeetingCalendar.pick([event("Soon", from: 4, to: 34)], at: now)?.title == "Soon")
        #expect(MeetingCalendar.pick([event("Too soon", from: 10, to: 40)], at: now) == nil)
        #expect(MeetingCalendar.pick([event("Just ended", from: -30, to: 0)], at: now) == nil)
    }

    @Test func namesOrganizerFirstEachOnce() {
        let names = MeetingCalendar.names(organizer: " Alex ", attendees: ["Sam", "Alex", nil, "", "Kim"])
        #expect(names == ["Alex", "Sam", "Kim"])
        #expect(MeetingCalendar.names(organizer: nil, attendees: (0..<80).map { "P\($0)" }).count == 50)
    }

    @Test func meetingCopiesTheEvent() {
        var e = event("Weekly", from: -10, to: 50)
        e.location = "Room 3"
        e.attendees = ["Alex"]
        let meeting = RecordingMeeting(e)
        #expect(meeting.eventID == "Weekly")
        #expect(meeting.title == "Weekly")
        #expect(meeting.location == "Room 3")
        #expect(meeting.attendees == ["Alex"])
        #expect(meeting.end == now.addingTimeInterval(50 * 60))
    }

    // MARK: The recording

    @Test func olderRecordingsDecodeWithoutAMeeting() throws {
        let entry = RecordingEntry(id: UUID(), createdAt: now, source: .phone)
        var json = try JSONSerialization.jsonObject(with: JSONEncoder().encode(entry)) as! [String: Any]
        json.removeValue(forKey: "meeting")
        let decoded = try JSONDecoder().decode(RecordingEntry.self, from: JSONSerialization.data(withJSONObject: json))
        #expect(decoded.meeting == nil)
        #expect(!decoded.isMeeting)
    }

    @Test func aMeetingIsKeptAndNamed() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        let untitled = try lib.create(source: .phone, meeting: RecordingMeeting())
        #expect(untitled.isMeeting)
        #expect(untitled.displayTitle == String(localized: "Meeting · \(untitled.createdAt.formatted(date: .abbreviated, time: .shortened))"))
        let named = try lib.create(source: .phone, title: "Weekly", meeting: RecordingMeeting(eventID: "e1", title: "Weekly", attendees: ["Alex"]))
        let reread = RecordingLibrary(root: root).entry(named.id)
        #expect(reread?.title == "Weekly")
        #expect(reread?.meeting?.eventID == "e1")
        #expect(reread?.meeting?.attendees == ["Alex"])
        let note = try lib.create(source: .phone)
        #expect(!note.isMeeting)
    }

    /// The hub writes minutes for `kind: meeting`; the event the phone matched goes with it as a hint (title, attendees,
    /// location, no id: docs/assistant/meeting-minutes.md「Field contract」).
    @Test func uploadMarksAMeeting() throws {
        func json(_ e: RecordingEntry) throws -> [String: Any] {
            try JSONSerialization.jsonObject(with: JSON.encoder.encode(RecordingUpload(e))) as! [String: Any]
        }
        var e = RecordingEntry(id: UUID(), createdAt: now, source: .phone, title: "Weekly")
        let note = try json(e)
        #expect(note["kind"] as? String == "note")
        #expect(note["calendar"] == nil)
        e.meeting = RecordingMeeting()
        let bare = try json(e)
        #expect(bare["kind"] as? String == "meeting")
        #expect(bare["calendar"] == nil)
        e.meeting = RecordingMeeting(eventID: "e1", title: "Weekly", start: now, end: now.addingTimeInterval(3600), location: "Room 3",
                                     attendees: ["Alex", "Sam"])
        let calendar = try #require(try json(e)["calendar"] as? [String: Any])
        #expect(calendar["title"] as? String == "Weekly")
        #expect(calendar["attendees"] as? [String] == ["Alex", "Sam"])
        #expect(calendar["location"] as? String == "Room 3")
        #expect(calendar["id"] == nil)
        #expect(calendar["start"] == nil)
    }

    /// A watch recording is a note until the phone matches it to a calendar event (on the phone, by when it started).
    @Test func watchRecordingKindFollowsTheCalendar() throws {
        var e = RecordingEntry(id: UUID(), createdAt: now, source: .watch)
        #expect(RecordingUpload(e).kind == "note")
        e.meeting = RecordingMeeting(eventID: "e1", title: "Standup")
        e.title = "Standup"
        let body = RecordingUpload(e)
        #expect(body.kind == "meeting")
        #expect(body.device == "watch")
        #expect(body.calendar?.title == "Standup")
    }

    // MARK: Minutes

    /// The contract's example, as the hub returns it in `detail.minutes`.
    private let minutesJSON = """
    {"title": "Design review", "when": "2026-11-10 14:00–15:10", "where": "Room 3", "attendees": ["Ada", "Bo"],
     "points": [{"text": "The export path drops entries over 4 MB.", "at": 312.0, "who": "Ada"}],
     "decisions": [{"text": "Ship the cap as-is for this release.", "at": 1504.5}],
     "actions": [{"who": "Bo", "what": "Add the regression test.", "by": "Friday", "at": 2100.0}],
     "open": [{"text": "Whether the cap should be per entry or per recording.", "at": 2408.0}]}
    """

    @Test func minutesDecodeTheContract() throws {
        let m = try JSONDecoder().decode(RecordingMinutes.self, from: Data(minutesJSON.utf8))
        #expect(m.title == "Design review")
        #expect(m.place == "Room 3")
        #expect(m.attendees == ["Ada", "Bo"])
        #expect(m.points.first?.who == "Ada")
        #expect(m.decisions.first?.at == 1504.5)
        #expect(m.actions.first?.text == "Add the regression test.")
        #expect(m.actions.first?.by == "Friday")
        #expect(RecordingMinutes.byline(m.actions[0]) == "Bo · Friday")
        #expect(m.openQuestions.count == 1)
        #expect(!m.isEmpty)
        // Sections left out are empty, not an error.
        let sparse = try JSONDecoder().decode(RecordingMinutes.self, from: Data(#"{"points":[{"text":"x"}]}"#.utf8))
        #expect(sparse.decisions.isEmpty)
        #expect(sparse.points.first?.at == nil)
    }

    // MARK: When, in the app's own date style (#4875)

    private func date(_ s: String) throws -> Date { try #require(JSON.parseDate(s)) }

    /// The header's style: the end's time alone on the same day, its date too on another; never the hub's fixed format.
    @Test(arguments: ["zh_CN", "ja_JP"]) func spanInTheHeadersDateStyle(_ id: String) throws {
        let locale = Locale(identifier: id), zone = try #require(TimeZone(identifier: "Asia/Shanghai"))
        let start = try date("2027-01-15T17:00:00+08:00"), end = try date("2027-01-15T17:02:00+08:00")
        let same = RecordingFormat.span(start, end, locale: locale, timeZone: zone)
        // The hour and minute in the locale's own way: "17:00" in Chinese, 「17時00分」 in Japanese (as in the header).
        let times = [#"17\D00"#, #"17\D02"#].allSatisfy { same.range(of: $0, options: .regularExpression) != nil }
        #expect(same.contains("1月15日") && times, "\(same)")
        #expect(same.components(separatedBy: "15日").count == 2 && !same.contains("2027"), "\(same)")
        let lateStart = try date("2027-01-15T23:30:00+08:00"), lateEnd = try date("2027-01-16T00:30:00+08:00")
        let late = RecordingFormat.span(lateStart, lateEnd, locale: locale, timeZone: zone)
        #expect(late.contains("1月15日") && late.contains("1月16日"), "\(late)")
    }

    /// `start` / `end` decoded (RFC 3339 with the offset) and shown, also in Copy and Share; `when` from a hub without
    /// both, or with one unreadable; nothing without any.
    @Test func whenFromStartAndEndElseTheHubsWhen() throws {
        func decode(_ json: String) throws -> RecordingMinutes { try JSONDecoder().decode(RecordingMinutes.self, from: Data(json.utf8)) }
        let old = try decode(minutesJSON)
        #expect(old.start == nil && old.whenLine == "2026-11-10 14:00–15:10")
        let m = try decode(#"{"when": "2026-11-10 14:00–15:10", "start": "2026-11-10T14:00:00+08:00", "end": "2026-11-10T15:10:00+08:00", "where": "Room 3"}"#)
        let start = try date("2026-11-10T06:00:00Z"), end = try date("2026-11-10T07:10:00Z")
        #expect(m.start == start && m.end == end)
        let span = RecordingFormat.span(try #require(m.start), try #require(m.end))
        #expect(m.whenLine == span)
        #expect(m.text(title: "x").components(separatedBy: "\n")[1] == "\(span) · Room 3")
        #expect(try decode(#"{"when": "w", "start": "2026-11-10T14:00:00+08:00"}"#).whenLine == "w")
        #expect(try decode(#"{"when": "w", "start": "yesterday", "end": "2026-11-10T15:10:00+08:00"}"#).whenLine == "w")
        #expect(try decode("{}").whenLine == nil)
        // What the watch is sent keeps them.
        #expect(try JSONDecoder().decode(RecordingMinutes.self, from: JSONEncoder().encode(m)) == m)
    }

    /// An item may cite a meeting photo instead of a transcript line (#3862 phase 3, `"photo": 3`): it is read and kept
    /// (what the watch is sent too), Copy and Share name it after the item as the hub's Markdown does, and a citation of
    /// another shape reads as none without losing the minutes.
    @Test func anItemCitesAPhoto() throws {
        let json = #"{"decisions":[{"text":"Cap exports at 4 MB.","at":1834.2,"photo":3},{"text":"Ship Friday.","at":40}],"#
            + #""points":[{"text":"Odd","photo":"three"},{"text":"Zero","photo":0}]}"#
        let m = try JSONDecoder().decode(RecordingMinutes.self, from: Data(json.utf8))
        #expect(m.decisions.map(\.photo) == [3, nil])
        #expect(m.points.map(\.photo) == [nil, nil])
        #expect(try JSONDecoder().decode(RecordingMinutes.self, from: JSONEncoder().encode(m)) == m)
        let rows = m.text(title: "Weekly").components(separatedBy: "\n")
        #expect(rows.contains("- Cap exports at 4 MB. [30:34] " + RecordingMinutes.photoMention(3)), "\(rows)")
        #expect(rows.contains("- Ship Friday. [0:40]"), "no photo, no mention: \(rows)")
        #expect(RecordingMinutes.photoMention(3).contains("3"))
    }

    @Test func minutesTextHasEverySection() throws {
        let m = try JSONDecoder().decode(RecordingMinutes.self, from: Data(minutesJSON.utf8))
        let text = m.text(title: "fallback")
        #expect(text.hasPrefix("# Design review\n2026-11-10 14:00–15:10 · Room 3"))
        #expect(text.contains("- Add the regression test. (Bo · Friday) [35:00]"))
        #expect(text.contains("[40:08]"))
        #expect(RecordingMinutes(points: [.init(text: "a")]).text(title: "Weekly").hasPrefix("# Weekly"))
    }

    // MARK: The summary card (#6563)

    /// The contract's example of minutes with an overview ("The `overview` field"), as the hub returns them.
    private let overviewJSON = """
    {"version": 3, "title": "Sprint review", "where": "Room 3", "attendees": ["Ada", "Bo"],
     "points": [{"text": "Export drops entries over 4 MB", "at": 312.0, "who": "Ada"},
                {"text": "The cache bug reproduced once on a weak network", "at": 1950.0, "photo": 2}],
     "decisions": [{"text": "Export keeps the cap, with a warning when an entry is skipped", "at": 1504.5}],
     "actions": [{"id": "a1", "who": "Bo", "what": "Add the regression test.", "by": "Friday", "at": 2100.0, "todo_id": ""}],
     "open": [{"text": "Whether the cap is per entry or per recording", "at": 2408.0}],
     "overview": {
       "headline": "Export ships with the size cap",
       "conclusions": [{"text": "Export ships with the 4 MB cap and a warning", "strong": ["4 MB"], "at": 1504.5}],
       "groups": [
         {"title": "Project progress", "topics": [
           {"title": "Export", "icon": "progress", "bullets": [
             {"text": "Export drops entries over 4 MB", "strong": ["4 MB"], "at": 312.0, "who": "Ada"},
             {"text": "Export keeps the cap, with a warning when an entry is skipped", "strong": ["keeps the cap"], "at": 1504.5, "decision": true}]}]},
         {"title": "Tooling and performance", "topics": [
           {"title": "Cache bug", "icon": "quality", "bullets": [
             {"text": "The cache bug reproduced once on a weak network", "strong": ["once"], "at": 1950.0, "photo": 2}]}]}]},
     "markdown": "# Sprint review\\n\\nExport ships with the size cap\\n"}
    """

    private func decode(_ json: String) throws -> RecordingMinutes {
        try JSONDecoder().decode(RecordingMinutes.self, from: Data(json.utf8))
    }

    /// The overview is read beside the lists, which keep their meaning; what the watch is sent keeps it all.
    @Test func overviewDecodesBesideTheLists() throws {
        let m = try decode(overviewJSON)
        let o = try #require(m.overview)
        #expect(o.headline == "Export ships with the size cap")
        #expect(o.conclusions.map(\.strong) == [["4 MB"]])
        #expect(o.conclusions.first?.decision == false && o.conclusions.first?.who == nil)
        #expect(o.groups.map(\.title) == ["Project progress", "Tooling and performance"])
        #expect(o.groups[0].topics[0].icon == .progress)
        #expect(o.groups[0].topics[0].bullets.map(\.decision) == [false, true])
        #expect(o.groups[0].topics[0].bullets[0].who == "Ada")
        #expect(o.groups[1].topics[0].bullets[0].photo == 2)
        #expect(m.points.count == 2 && m.decisions.count == 1 && m.actions.first?.id == "a1" && m.openQuestions.count == 1)
        #expect(m.markdown == "# Sprint review\n\nExport ships with the size cap\n")
        #expect(try JSONDecoder().decode(RecordingMinutes.self, from: JSONEncoder().encode(m)) == m)
        // Minutes without one, from any hub before #6563, have none.
        #expect(try decode(minutesJSON).overview == nil)
        #expect(try decode(minutesJSON).markdown == nil)
    }

    /// A broken overview never costs the minutes their lists: no overview, the same points, decisions, actions and open
    /// questions (docs/assistant/meeting-minutes.md "Clients").
    @Test(arguments: [
        #""x""#, "null", "[]", "3", "{}", #"{"groups": "x"}"#, #"{"groups": {}}"#, #"{"groups": []}"#,
        #"{"groups": [{"title": "A"}]}"#, #"{"groups": [{"topics": "x"}]}"#, #"{"groups": [{"topics": [{"bullets": [{"text": "a"}]}]}]}"#,
        #"{"groups": [{"topics": [{"title": "  ", "bullets": [{"text": "a"}]}]}]}"#,
        #"{"groups": [{"topics": [{"title": 3, "bullets": [{"text": "a"}]}]}]}"#,
        #"{"groups": [{"topics": [{"title": "T", "bullets": "a"}]}]}"#,
        #"{"groups": [{"topics": [{"title": "T", "bullets": [{"text": " \n"}, {"text": 4}, "a", null]}]}]}"#,
        #"{"groups": ["x", 3, null, [{"topics": []}]]}"#,
    ])
    func aBrokenOverviewIsNone(_ overview: String) throws {
        let json = #"{"version": 2, "points": [{"text": "p", "at": 1}], "decisions": [{"text": "d", "at": 2}], "#
            + #""actions": [{"id": "a1", "what": "w"}], "open": [{"text": "o"}], "markdown": "M", "overview": "# + overview + "}"
        let m = try decode(json)
        #expect(m.overview == nil)
        #expect(m.points.map(\.text) == ["p"] && m.decisions.map(\.text) == ["d"] && m.actions.first?.id == "a1")
        #expect(m.openQuestions.map(\.text) == ["o"] && m.version == 2)
        // Copy and Share keep the phone's own text, as without an overview.
        #expect(m.shareText(title: "Weekly") == m.text(title: "Weekly"))
    }

    /// The cleaning, member by member: what is not an object goes, a group's title that is not a string is "", a wrong
    /// headline, conclusion, `who`, `strong` or `at` counts as absent, only `true` marks a decision, only a positive
    /// whole number cites a photo, an unknown icon is `general`; empty topics and groups go.
    @Test func overviewIsCleanedMemberByMember() throws {
        let json = #"""
        {"overview": {"headline": 3, "conclusions": [{"text": "c1", "at": -1, "strong": "x"}, "c2", {"text": ""}, {"text": "c3", "at": "12", "photo": 0}],
         "groups": [
           "x",
           {"title": 7, "topics": [
             3,
             {"title": "Empty", "icon": "progress", "bullets": [{"text": "  "}]},
             {"title": "Kept", "icon": "rocket", "bullets": [
               {"text": "b1", "strong": ["b", 2, "", null], "at": 12.5, "who": 4, "decision": "true", "photo": 2.0},
               {"text": "b2", "at": true, "decision": 1, "photo": 2.5},
               {"text": "b3", "who": "Ada", "decision": true, "photo": "3", "at": 0}]}]},
           {"title": "Gone", "topics": [{"title": "No bullets", "bullets": []}]}]}}
        """#
        let o = try #require(try decode(json).overview)
        #expect(o.headline == nil)
        #expect(o.conclusions.map(\.text) == ["c1", "c3"])
        #expect(o.conclusions.map(\.at) == [nil, nil] && o.conclusions.map(\.strong) == [[], []] && o.conclusions[1].photo == nil)
        #expect(o.groups.count == 1 && o.groups[0].title == "")
        let topic = try #require(o.groups.first?.topics.first)
        #expect(o.groups[0].topics.count == 1 && topic.title == "Kept" && topic.icon == .general)
        #expect(topic.bullets.map(\.text) == ["b1", "b2", "b3"])
        #expect(topic.bullets.map(\.strong) == [["b"], [], []])
        #expect(topic.bullets.map(\.at) == [12.5, nil, 0])
        #expect(topic.bullets.map(\.who) == [nil, nil, "Ada"])
        #expect(topic.bullets.map(\.decision) == [false, false, true])
        #expect(topic.bullets.map(\.photo) == [2, nil, nil])
    }

    /// Every icon on the contract's list is read as itself; anything else, a wrong case included, is `general`.
    @Test func topicIconsFromTheClosedList() throws {
        for icon in RecordingMinutes.TopicIcon.allCases {
            let json = #"{"overview": {"groups": [{"title": "", "topics": [{"title": "T", "icon": "\#(icon.rawValue)", "bullets": [{"text": "b"}]}]}]}}"#
            #expect(try decode(json).overview?.groups.first?.topics.first?.icon == icon)
        }
        for icon in [#""Progress""#, #""rocket""#, "3", "null", "\"\""] {
            let json = #"{"overview": {"groups": [{"title": "", "topics": [{"title": "T", "icon": "# + icon + #", "bullets": [{"text": "b"}]}]}]}}"#
            #expect(try decode(json).overview?.groups.first?.topics.first?.icon == .general, "\(icon)")
        }
        #expect(RecordingMinutes.TopicIcon.allCases.count == 13)
    }

    /// Bold (`strong`): each entry's first exact occurrence at or after the end of the one before; one not found bolds
    /// nothing and the search goes on from where it was; code units, no case folding or normalization.
    @Test func boldPlacesFollowTheStrongRule() {
        func parts(_ text: String, _ strong: [String]) -> [String] {
            RecordingMinutes.boldParts(text, strong: strong).map { $0.bold ? "**\($0.text)**" : $0.text }
        }
        #expect(parts("QPS raised to 40K", ["40K"]) == ["QPS raised to ", "**40K**"])
        #expect(parts("from 20K to 40K", ["20K", "40K"]) == ["from ", "**20K**", " to ", "**40K**"])
        // A figure twice: the second entry is found after the first.
        #expect(parts("4 MB here, 4 MB there", ["4 MB", "4 MB"]) == ["**4 MB**", " here, ", "**4 MB**", " there"])
        // Out of order: the second is looked for after the first, so it is not found.
        #expect(parts("a b", ["b", "a"]) == ["a ", "**b**"])
        // Not found: skipped, the next still found from where the search was.
        #expect(parts("module X done", ["Y", "done"]) == ["module X ", "**done**"])
        #expect(parts("Done done", ["done"]) == ["Done ", "**done**"])
        #expect(parts("café", ["cafe\u{301}"]) == ["café"])
        #expect(parts("plain", []) == ["plain"])
        #expect(parts("x", ["", "x"]) == ["**x**"])
        #expect(parts("核心结论：完成 90%", ["完成", "90%"]) == ["核心结论：", "**完成**", " ", "**90%**"])
        #expect(parts("emoji 👍🏽 ok", ["👍🏽"]) == ["emoji ", "**👍🏽**", " ok"])
        // Never read as Markdown: asterisks and links stay words.
        #expect(parts("**not bold** [x](https://example.com)", []) == ["**not bold** [x](https://example.com)"])
        let bold = RecordingMinutes.bolded("QPS to 40K", strong: ["40K"])
        #expect(String(bold.characters) == "QPS to 40K")
        #expect(bold.runs.map { $0.inlinePresentationIntent } == [nil, .stronglyEmphasized])
    }

    /// Copy and Share: with the summary card, the hub's own Markdown; without one (or from a hub that sends none), the
    /// phone's text exactly as before.
    @Test func copySendsTheHubsMarkdownWithTheSummaryCard() throws {
        let m = try decode(overviewJSON)
        #expect(m.shareText(title: "x") == "# Sprint review\n\nExport ships with the size cap\n")
        var bare = m
        bare.markdown = nil
        #expect(bare.shareText(title: "x") == bare.text(title: "x"))
        bare.markdown = " \n"
        #expect(bare.shareText(title: "x") == bare.text(title: "x"))
        let legacy = try decode(minutesJSON)
        #expect(legacy.shareText(title: "fallback") == legacy.text(title: "fallback"))
        // The legacy text has no trace of the overview.
        var withMarkdown = legacy
        withMarkdown.markdown = "# Other"
        #expect(withMarkdown.shareText(title: "fallback") == legacy.text(title: "fallback"))
    }

    /// What Copy and Share send, picked as the page picks it (`RecordingDetailView`: the summary card when the prepared
    /// Summary has an overview): the hub's Markdown with the summary card; the phone's own text for minutes without an
    /// overview, and for one the contract's cleaning empties (`"groups": "x"`), Markdown or not.
    @Test func copyAndShareSendWhatTheSummaryShows() throws {
        func sent(_ m: RecordingMinutes) -> String {
            let summaryCard = RecordingMinutesCards.summary(m, sentences: []).overview != nil
            return RecordingMinutesActions.text(m, title: "fallback", summaryCard: summaryCard)
        }
        let m = try decode(overviewJSON)
        #expect(sent(m) == "# Sprint review\n\nExport ships with the size cap\n")
        #expect(RecordingMinutesActions.text(m, title: "fallback", summaryCard: false) == m.text(title: "fallback"))
        let legacy = try decode(minutesJSON)
        #expect(sent(legacy) == legacy.text(title: "fallback"))
        let malformed = try decode(overviewJSON.replacingOccurrences(of: #""groups": ["#, with: #""groups": "x", "was": ["#))
        #expect(malformed.markdown?.isEmpty == false)
        #expect(sent(malformed) == malformed.text(title: "fallback"))
        #expect(sent(malformed) != malformed.markdown)
    }

    /// A time lands on the line said then (or just before), the first line before any.
    @Test func timeFindsItsTranscriptLine() {
        let lines = [RecordingSentence(start: 2, end: 10, text: "a"), RecordingSentence(start: 21, end: 30, text: "b"),
                     RecordingSentence(start: 44, end: 50, text: "c")]
        #expect(RecordingMinutes.sentenceIndex(at: 44, in: lines) == 2)
        #expect(RecordingMinutes.sentenceIndex(at: 43.5, in: lines) == 2)
        #expect(RecordingMinutes.sentenceIndex(at: 30, in: lines) == 1)
        #expect(RecordingMinutes.sentenceIndex(at: 0, in: lines) == 0)
        #expect(RecordingMinutes.sentenceIndex(at: 9_999, in: lines) == 2)
        #expect(RecordingMinutes.sentenceIndex(at: 5, in: []) == nil)
    }

    /// `minutes_state` as the page shows it: being written, failed, or not a meeting.
    @Test func minutesStateFromTheHub() throws {
        let m = try JSONDecoder().decode(RecordingMinutes.self, from: Data(minutesJSON.utf8))
        #expect(RecordingMinutesModel.state(minutes: m, status: "ready", meeting: true, synced: true) == .ready(m))
        #expect(RecordingMinutesModel.state(minutes: nil, status: "pending", meeting: true, synced: true) == .writing)
        #expect(RecordingMinutesModel.state(minutes: nil, status: "failed", meeting: true, synced: true) == .failed)
        #expect(RecordingMinutesModel.state(minutes: nil, status: nil, meeting: true, synced: false) == .waitingForSync)
        // Synced, and the hub says nothing: it writes no minutes (an older hub), not "writing" for ever.
        #expect(RecordingMinutesModel.state(minutes: nil, status: nil, meeting: true, synced: true) == .unsupported)
        // Ready with nothing to note is still ready.
        #expect(RecordingMinutesModel.state(minutes: RecordingMinutes(), status: "ready", meeting: true, synced: true) == .ready(RecordingMinutes()))
        #expect(RecordingMinutesModel.state(minutes: nil, status: nil, meeting: false, synced: true) == .none)
        // A note the hub wrote minutes for anyway shows them.
        #expect(RecordingMinutesModel.state(minutes: m, status: nil, meeting: false, synced: true) == .ready(m))
    }

    // MARK: Action items as to-dos, and Regenerate (#4062)

    /// A meeting recorded, transcribed and done: what the page loads minutes for.
    private func doneMeeting() -> RecordingEntry {
        var e = RecordingEntry(id: UUID(), createdAt: now, source: .phone, title: "Weekly", active: false, expectedSegments: 1)
        e.meeting = RecordingMeeting(title: "Weekly")
        e.segments = [RecordingSegment(index: 0, start: 0, duration: 60, sentences: [RecordingSentence(start: 1, end: 9, text: "Bo adds the test.")])]
        return e
    }

    private func detail(version: Int, added: String = "", state: String = "ready", trimmed: Bool = false) -> String {
        """
        {"detail": {"meta": {"minutes_state": "\(state)"\(trimmed ? #", "trimmed_at": "2026-11-20T00:00:00Z""# : "")},
         "minutes": {"version": \(version), "points": [{"text": "Export", "at": 1}],
          "actions": [{"id": "a1", "who": "Bo", "what": "Add the test.", "by": "Friday", "due_date": "2026-11-13", "at": 2, "todo_id": "\(added)"},
                      {"id": "a2", "what": "Write the notes.", "at": 3, "todo_id": ""}]}}}
        """
    }

    @Test func actionItemsDecodeTheirIDsAndTodos() throws {
        let answer = try JSONDecoder().decode(RecordingMinutesModel.Answer.self, from: Data(detail(version: 2, added: "t1", trimmed: true).utf8))
        let m = try #require(answer.detail.minutes)
        #expect(m.version == 2)
        #expect(m.actions.map(\.id) == ["a1", "a2"])
        #expect(m.actions[0].added && !m.actions[1].added)
        #expect(m.actions[0].dueDate == "2026-11-13")
        #expect(answer.detail.meta?.trimmedAt == "2026-11-20T00:00:00Z")
        // The date it resolves to goes with the deadline as said.
        #expect(RecordingMinutes.byline(m.actions[0])?.hasPrefix("Bo · Friday · ") == true)
        // What the watch is sent keeps them.
        let again = try JSONDecoder().decode(RecordingMinutes.self, from: JSONEncoder().encode(m))
        #expect(again == m)
    }

    /// Add sends the version shown and those actions, shows them added, and reads the recording back before anything else.
    @Test func addSendsTheVersionShownAndReadsBack() async throws {
        let hub = FakeHub()
        let entry = doneMeeting()
        let path = "/api/v1/recordings/\(entry.id.uuidString.lowercased())/minutes/todos"
        var added = ""
        hub.on("GET", "/api/v1/kb/recordings/\(entry.id.uuidString.lowercased())") { _ in Data(self.detail(version: 2, added: added).utf8) }
        hub.on("POST", path) { req in
            let body = try JSONSerialization.jsonObject(with: req.body ?? Data()) as? [String: Any]
            #expect(body?["version"] as? Int == 2)
            #expect(body?["actions"] as? [String] == ["a1", "a2"])
            added = "t1"
            return Data(#"{"todos": [{"id": "t1"}], "minutes": {"version": 2, "actions": [{"id": "a1", "what": "Add the test.", "todo_id": "t1"}]}}"#.utf8)
        }
        let model = RecordingMinutesModel(api: { hub.client })
        await model.load(entry, synced: true)
        await model.add(["a1", "a2"], to: entry, synced: true)
        #expect(hub.sent("POST", path).count == 1)
        #expect(hub.sent("GET", "/api/v1/kb/recordings/\(entry.id.uuidString.lowercased())").count == 2)
        guard case .ready(let m) = model.state else { Issue.record("not ready: \(model.state)"); return }
        #expect(m.actions[0].added)
        #expect(!model.asking)
    }

    /// Rewritten since they were shown: the new ones are read and shown, nothing is sent again, and the page says so.
    @Test func staleMinutesAreReadAgainNotResent() async throws {
        let hub = FakeHub()
        let entry = doneMeeting()
        let path = "/api/v1/recordings/\(entry.id.uuidString.lowercased())/minutes/todos"
        var version = 2
        hub.on("GET", "/api/v1/kb/recordings/\(entry.id.uuidString.lowercased())") { _ in Data(self.detail(version: version).utf8) }
        hub.on("POST", path) { _ in
            version = 3
            throw APIError(status: 409, message: "stale", fields: ["code": "minutes_stale"])
        }
        let model = RecordingMinutesModel(api: { hub.client })
        await model.load(entry, synced: true)
        await model.add(["a1"], to: entry, synced: true)
        #expect(hub.sent("POST", path).count == 1)
        guard case .ready(let m) = model.state else { Issue.record("not ready"); return }
        #expect(m.version == 3)
        #expect(!m.actions[0].added)
        #expect(AppModel.shared.toast?.message.isEmpty == false)
    }

    /// Regenerate goes with minutes shown only when the transcript is whole on the hub, nothing is being written and it
    /// hasn't expired; asked, the minutes stay shown as Updating.
    @Test func regenerateKeepsTheMinutesShown() async throws {
        let hub = FakeHub()
        let entry = doneMeeting()
        let id = entry.id.uuidString.lowercased()
        var state = "ready", trimmed = false
        hub.on("GET", "/api/v1/kb/recordings/\(id)") { _ in Data(self.detail(version: 2, state: state, trimmed: trimmed).utf8) }
        hub.on("POST", "/api/v1/recordings/\(id)/minutes") { _ in Data(#"{"state": "pending"}"#.utf8) }
        let model = RecordingMinutesModel(api: { hub.client })
        await model.load(entry, synced: true)
        #expect(model.canRegenerate(entry, synced: true))
        #expect(!model.canRegenerate(entry, synced: false))
        await model.regenerate(entry.id)
        guard case .ready = model.state else { Issue.record("the minutes went"); return }
        #expect(model.updating && model.waiting)
        #expect(!model.canRegenerate(entry, synced: true))
        state = "pending"
        await model.load(entry, synced: true)
        #expect(model.updating)
        state = "ready"; trimmed = true
        await model.load(entry, synced: true)
        #expect(!model.updating && model.trimmed)
        #expect(!model.canRegenerate(entry, synced: true))
    }

    // MARK: The Live Activity

    private func entry(marks: [Double] = [], meeting: Bool = true) -> RecordingEntry {
        var e = RecordingEntry(id: UUID(), createdAt: now, source: .phone, title: "Weekly")
        e.marks = marks
        if meeting { e.meeting = RecordingMeeting(title: "Weekly") }
        return e
    }

    /// Recording: the system counts from `now − elapsed`; later saves of the same running recording change nothing.
    @Test func runningTimeIsCountedBySystem() {
        let first = RecordingActivity.content(entry: entry(), recording: true, interrupted: false, elapsed: 100, previous: nil, now: now)
        #expect(first.phase == .recording)
        #expect(first.runningSince == now.timeIntervalSince1970 - 100)
        let later = RecordingActivity.content(entry: entry(), recording: true, interrupted: false, elapsed: 160, previous: first,
                                              now: now.addingTimeInterval(60.3))
        #expect(later == first)
    }

    @Test func pausedAndInterruptedStandStill() {
        let running = RecordingActivity.content(entry: entry(), recording: true, interrupted: false, elapsed: 100, previous: nil, now: now)
        let paused = RecordingActivity.content(entry: entry(), recording: false, interrupted: false, elapsed: 130, previous: running, now: now)
        #expect(paused.phase == .paused)
        #expect(paused.runningSince == nil)
        #expect(paused.elapsed == 130)
        let call = RecordingActivity.content(entry: entry(), recording: false, interrupted: true, elapsed: 130, previous: running, now: now)
        #expect(call.phase == .interrupted)
        // Resumed: counted again from the time recorded, not from before the pause.
        let resumed = RecordingActivity.content(entry: entry(), recording: true, interrupted: false, elapsed: 130, previous: call,
                                                now: now.addingTimeInterval(600))
        #expect(resumed.runningSince == now.addingTimeInterval(600).timeIntervalSince1970 - 130)
    }

    @Test func showsTitleMeetingAndBookmarks() {
        let content = RecordingActivity.content(entry: entry(marks: [10, 70]), recording: true, interrupted: false, elapsed: 80,
                                                previous: nil, now: now)
        #expect(content.title == "Weekly")
        #expect(content.meeting)
        #expect(content.marks == 2)
        let note = RecordingActivity.content(entry: entry(meeting: false), recording: true, interrupted: false, elapsed: 0, previous: nil, now: now)
        #expect(!note.meeting)
    }

    /// Small enough for ActivityKit's 4 KB, with a long title.
    @Test func contentStaysSmall() throws {
        var e = entry(marks: Array(repeating: 1, count: 500))
        e.title = String(repeating: "长", count: 200)
        let content = RecordingActivity.content(entry: e, recording: true, interrupted: false, elapsed: 7200, previous: nil, now: now)
        #expect(try JSONEncoder().encode(content).count < 1024)
    }

    @Test func linkOpensRecordings() {
        #expect(RecordingLink.matches(RecordingLink.page))
        #expect(!RecordingLink.matches(URL(string: "persona://open")!))
        #expect(!RecordingLink.matches(URL(string: "https://example.com/record")!))
    }

    // MARK: Intents

    /// The activity's buttons work locked (as a voice memo's) and stay out of Shortcuts.
    @Test func activityButtonsWorkLockedOutOfShortcuts() {
        #expect(StopRecordingIntent.authenticationPolicy == .alwaysAllowed)
        #expect(AddRecordingBookmarkIntent.authenticationPolicy == .alwaysAllowed)
        #expect(StopRecordingIntent.isDiscoverable == false)
        #expect(AddRecordingBookmarkIntent.isDiscoverable == false)
    }

    /// Siri and the Control start it without opening the app; the first time it may come to the front to ask for the microphone.
    @Test func startRunsInTheBackground() {
        #expect(StartMeetingRecordingIntent.supportedModes.contains(.background))
    }

    /// A button restored without its recording (or for one that ended) does nothing and doesn't throw.
    @Test func buttonsWithoutARecordingDoNothing() async throws {
        #expect(RecordingCapture.shared.state == .idle)
        _ = try await StopRecordingIntent().perform()
        _ = try await AddRecordingBookmarkIntent(recordingID: UUID().uuidString).perform()
        #expect(RecordingCapture.shared.state == .idle)
    }
}
