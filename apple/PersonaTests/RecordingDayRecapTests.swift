import Foundation
import Testing
@testable import Persona
import PersonaWire

private let shanghai = TimeZone(identifier: "Asia/Shanghai")!
/// The contract's example day: 2026-11-11, recording since 08:02:11 in Shanghai.
private let dayStart = JSON.parseDate("2026-11-11T08:02:11+08:00")!

/// `detail.recap` as the contract (docs/assistant/day-recap.md) writes its example.
private let contractRecap = #"""
{
  "version": 3,
  "revision": 1762862400000,
  "generated_at": "2026-11-11T13:00:41Z",
  "day": "2026-11-11",
  "covers": {"from": 0, "to": 46800.0},
  "final": false,
  "final_reason": "complete",
  "summary": "Mostly the export release.",
  "timeline": [
    {
      "from": 3600.0, "to": 6900.0,
      "kind": "meeting",
      "title": "Design review",
      "meeting": {"kind": "recordings", "id": "8a0c4f52-6a37-4cc4-9d2a-0d5b1f0e8d11", "name": "Design review"},
      "points": [{"text": "The export path drops entries over 4 MB.", "at": 3912.0, "who": "Ada"}]
    },
    {
      "from": 7200.0, "to": 7800.0,
      "kind": "outside",
      "title": "Lunch with the platform team",
      "points": [{"text": "Bo will look at the flaky upload test.", "at": 7420.5}]
    }
  ],
  "actions": [
    {"id": "a1", "who": "Bo", "what": "Look at the flaky upload test.", "by": "tomorrow", "due_date": "2026-11-12", "at": 7420.5, "todo_id": ""}
  ],
  "gaps": [{"from": 3120.0, "to": 3410.5, "reason": "interrupted"}],
  "partial": false,
  "markdown": "# 2026-11-11\n…"
}
"""#

private func decode<T: Decodable>(_ type: T.Type, _ json: String) throws -> T {
    try JSON.decoder.decode(T.self, from: Data(json.utf8))
}

private func recap(_ json: String) throws -> RecordingDayRecap { try decode(RecordingDayRecap.self, json) }

/// A recap with one timeline entry per `(offset, title)`, each with one point at its start.
private func recap(entries: [(Double, String)], summary: String = "", isFinal: Bool = false,
                   finalReason: String? = nil) -> RecordingDayRecap {
    RecordingDayRecap(isFinal: isFinal, finalReason: finalReason, summary: summary, timeline: entries.map { offset, title in
        RecordingDayRecap.Entry(from: offset, to: offset + 600, kind: "outside", title: title,
                                points: [RecordingDayRecap.Point(text: title + " point", at: offset)])
    })
}

private func summary(id: UUID = UUID(), hubIDs: [UUID] = [], sync: RecordingDaySyncState = .synced,
                     transcribedTo: Date? = dayStart.addingTimeInterval(600), recapState: String? = nil,
                     closedAt: Date? = nil, chunks: Int = 360, skipped: Int = 0, complete: Bool = false) -> RecordingDaySummary {
    RecordingDaySummary(id: id, day: "2026-11-11", timeZone: "Asia/Shanghai", firstAt: dayStart, lastAt: dayStart.addingTimeInterval(3600),
                        closedAt: closedAt, recordedSeconds: 3600, transcribedTo: transcribedTo, chunks: chunks, waiting: 0,
                        skipped: skipped, failed: 0, gaps: [], marks: [], sync: sync, recapState: recapState, recapFinal: false,
                        hubIDs: hubIDs, complete: complete)
}

/// The contract's example day, closed at 19:07 there and complete on the hub: every chunk settled and appended.
private let dayEnd = JSON.parseDate("2026-11-11T19:07:00+08:00")!

/// That day, complete: its chunks all quieter than speech (`silent`), or some of them with words.
private func completeDay(id: UUID = UUID(), silent: Bool) -> RecordingDaySummary {
    summary(id: id, hubIDs: [id], transcribedTo: dayEnd, closedAt: dayEnd, chunks: 66, skipped: silent ? 66 : 3, complete: true)
}

/// The detail the hub answers for a day: its meta (`recap_final` when `final`) and, when given, its recap (raw JSON).
private func detail(state: String?, final isFinal: Bool = false, recap: String? = nil,
                    startedAt: String = "2026-11-11T08:02:11+08:00") -> String {
    let stateField = state.map { #", "recap_state": "\#($0)""# } ?? ""
    let finalField = isFinal ? #", "recap_final": true"# : ""
    let recapField = recap.map { #", "recap": \#($0)"# } ?? ""
    return #"{"detail": {"meta": {"kind": "day", "tz": "Asia/Shanghai", "started_at": "\#(startedAt)"\#(stateField)\#(finalField)}, "segments": [{"start": 1, "end": 2, "text": "…"}]\#(recapField)}}"#
}

private func kbPath(_ id: UUID) -> String { "/api/v1/kb/recordings/\(id.uuidString.lowercased())" }

private let listPath = "/api/v1/recordings"

/// `GET /api/v1/recordings` with a day's parts in order, each with its `recap_state` (nil: none due) and the day fields
/// reconciliation reads; `final` parts list `recap_final` and `recap_final_at`.
private func listing(_ parts: [(UUID, String?)], final finalParts: Set<UUID> = [], next: Int = 120) -> String {
    let rows = parts.enumerated().map { i, part in
        let stateField = part.1.map { #", "recap_state": "\#($0)""# } ?? ""
        let finalField = finalParts.contains(part.0) ? #", "recap_final": true, "recap_final_at": "2026-11-12T06:00:00Z""# : ""
        let dayFields = #""kind": "day", "day": "2026-11-11", "part": \#(i + 1), "next": \#(next), "base": 1762826467250"#
        return #"{"id": "\#(part.0.uuidString.lowercased())", "revision": 1762830067891, \#(dayFields)\#(stateField)\#(finalField)}"#
    }
    return #"{"days": 14, "recordings": [\#(rows.joined(separator: ", "))]}"#
}

/// The hub's recap of a recorded day: decoding (contract shape, tolerant), the page's wall-clock content, the recap's
/// state, and the model that reads and follows it.
@MainActor struct RecordingDayRecapTests {
    // MARK: Decoding

    @Test func decodesTheContractsRecap() throws {
        let decoded = try recap(contractRecap)
        #expect(decoded.version == 3)
        #expect(decoded.revision == 1_762_862_400_000)
        #expect(decoded.writtenAt == JSON.parseDate("2026-11-11T13:00:41Z"))
        #expect(decoded.day == "2026-11-11")
        #expect(decoded.covers == .init(from: 0, to: 46_800))
        #expect(decoded.isFinal == false)
        #expect(decoded.finalReason == RecordingDayRecap.Reason.complete)
        #expect(decoded.partial == false)
        #expect(decoded.summary == "Mostly the export release.")
        #expect(decoded.timeline?.count == 2)
        let meeting = try #require(decoded.timeline?.first)
        #expect(meeting.kind == "meeting")
        #expect(meeting.meeting?.name == "Design review")
        #expect(meeting.points == [.init(text: "The export path drops entries over 4 MB.", at: 3912, who: "Ada")])
        #expect(decoded.actions == [.init(id: "a1", who: "Bo", what: "Look at the flaky upload test.", by: "tomorrow",
                                        dueDate: "2026-11-12", at: 7420.5, todoID: "")])
        #expect(decoded.gaps == [.init(from: 3120, to: 3410.5, reason: "interrupted")])
        #expect(decoded.markdown == "# 2026-11-11\n…")
        // What it encodes reads back the same (the contract's keys).
        let again = try JSON.decoder.decode(RecordingDayRecap.self, from: JSONEncoder().encode(decoded))
        #expect(again == decoded)
    }

    /// A newer hub, or a bad item: what can't be read is left out, never the whole recap.
    @Test func aMalformedItemLeavesTheRestReadable() throws {
        let decoded = try recap(#"""
        {"summary": 7, "final": "yes", "generated_at": "not a time",
         "timeline": [123, {"from": "x", "title": "Lunch", "points": [5, {"text": "Pasta again.", "at": 10}], "mood": "fine"}],
         "actions": "none", "gaps": [{"from": 1, "to": 2}], "highlights": [1, 2]}
        """#)
        #expect(decoded.summary == nil)
        #expect(decoded.isFinal == nil)
        #expect(decoded.writtenAt == nil)
        #expect(decoded.timeline == [.init(from: nil, title: "Lunch", points: [.init(text: "Pasta again.", at: 10)])])
        #expect(decoded.actions == nil)
        #expect(decoded.gaps == [.init(from: 1, to: 2, reason: nil)])
        #expect(try recap("{}") == RecordingDayRecap())
    }

    @Test func theAnswerKeepsItsStateWhenTheRecapCantBeRead() throws {
        let answer = try decode(RecordingDayRecapAnswer.self, detail(state: "pending", recap: #""garbage""#))
        #expect(answer.recap == nil)
        #expect(answer.recapState == "pending")
        #expect(answer.startedAt == dayStart)
        let ready = try decode(RecordingDayRecapAnswer.self, detail(state: "ready", recap: contractRecap))
        #expect(ready.recap?.version == 3)
        #expect(!ready.recapFinal)
        let finalAnswer = try decode(RecordingDayRecapAnswer.self, detail(state: "ready", final: true, recap: contractRecap))
        #expect(finalAnswer.recapFinal)
        #expect(finalAnswer.seen == .listed("ready", final: true))
        let none = try decode(RecordingDayRecapAnswer.self, #"{"detail": {"meta": {"kind": "day"}}}"#)
        #expect(none == RecordingDayRecapAnswer())
    }

    @Test func theListingSaysWhereEachPartIs() throws {
        let a = UUID(), b = UUID(), c = UUID()
        let listing = try decode(RecordingDayRecapListing.self, #"""
        {"days": 14, "recordings": [
          {"id": "\#(a.uuidString.lowercased())", "kind": "day", "recap_state": "ready", "recap_final": true,
           "recap_final_at": "2026-11-12T06:00:00Z"},
          {"id": "not-an-id", "recap_state": "failed"},
          {"id": "\#(b.uuidString.lowercased())", "revision": 5, "recap_final": "yes"}
        ]}
        """#)
        #expect(listing.rows.count == 2)
        #expect(listing.seen([a, b, c]) == [a: .listed("ready", final: true), b: .listed(nil, final: false), c: .absent])
        #expect(listing.rows.first?.recapFinalAt == JSON.parseDate("2026-11-12T06:00:00Z"))
        #expect(listing.rows.last?.recapFinalAt == nil)
        #expect(RecordingDayRecapSeen.listed("failed", final: false).failed)
        #expect(!RecordingDayRecapSeen.listed("ready", final: true).failed && !RecordingDayRecapSeen.absent.failed)
    }

    /// What the page hands the day's upload: the rows reconciliation would, and only when the list names every part.
    @Test func theListingGivesTheDaysUploadReconciliationsRows() throws {
        let a = UUID(), b = UUID()
        let list = try decode(RecordingDayRecapListing.self, listing([(a, "ready"), (b, nil)]))
        #expect(list.hubRows([a, b]) == [
            RecordingDayHubRow(id: a, revision: 1762830067891, part: 1, next: 120, base: 1762826467250, recapState: "ready"),
            RecordingDayHubRow(id: b, revision: 1762830067891, part: 2, next: 120, base: 1762826467250, recapState: nil),
        ])
        let finalList = try decode(RecordingDayRecapListing.self, listing([(a, "ready")], final: [a]))
        #expect(finalList.hubRows([a]) == [
            RecordingDayHubRow(id: a, revision: 1762830067891, part: 1, next: 120, base: 1762826467250, recapState: "ready",
                               recapFinal: true, recapFinalAt: JSON.parseDate("2026-11-12T06:00:00Z")),
        ])
        #expect(RecordingDayRecapListing.dayFinal(finalList.hubRows([a]) ?? []))
        #expect(!RecordingDayRecapListing.dayFinal(list.hubRows([a, b]) ?? []), "final only once every part is")
        #expect(!RecordingDayRecapListing.dayFinal([]))
        #expect(list.hubRows([a, UUID()]) == nil, "a part missing from the list is reconciliation's to judge")
        #expect(list.hubRows([]) == nil)
        // The day's state across its parts, as its record sums it.
        #expect(RecordingDayRecapListing.dayState(["ready", nil, "pending"]) == "pending")
        #expect(RecordingDayRecapListing.dayState(["pending", "failed", "ready"]) == "failed")
        #expect(RecordingDayRecapListing.dayState(["ready", nil]) == "ready")
        #expect(RecordingDayRecapListing.dayState(["queued"]) == "queued", "a newer hub's state as it is")
        #expect(RecordingDayRecapListing.dayState([nil, nil]) == nil)
    }

    // MARK: What the page shows

    @Test func theTimelineIsOnTheWallClockByTimeOfDay() throws {
        let content = RecordingDayRecapContent(parts: [.init(recap: try recap(contractRecap), startedAt: dayStart)], zone: shanghai)
        // 3600 s after 08:02:11 is 09:02:11 (morning); 7200 s is 10:02:11.
        #expect(content.sections.map(\.period) == [.morning])
        let entries = try #require(content.sections.first?.entries)
        #expect(entries.map(\.title) == ["Design review", "Lunch with the platform team"])
        #expect(entries[0].from == dayStart.addingTimeInterval(3600))
        #expect(entries[0].to == dayStart.addingTimeInterval(6900))
        #expect(entries[0].meeting && entries[0].meetingName == "Design review")
        #expect(!entries[1].meeting && entries[1].meetingName == nil)
        #expect(entries[0].points.first?.at == dayStart.addingTimeInterval(3912))
        #expect(entries[0].points.first?.who == "Ada")
        #expect(content.actions.map(\.what) == ["Look at the flaky upload test."])
        #expect(content.actions.first?.at == dayStart.addingTimeInterval(7420.5))
        #expect(content.actions.first?.by == "tomorrow" && content.actions.first?.dueDate == "2026-11-12")
        #expect(content.actions.first?.confirmed == false, "an empty todo_id is not confirmed")
        #expect(content.writtenAt == JSON.parseDate("2026-11-11T13:00:41Z"))
        #expect(content.finality == .soFar, "a reason without final is no final recap")
        #expect(!content.isFinal)
        #expect(content.shareText == "# 2026-11-11\n…")

        let spread = recap(entries: [(3.0 * 3600, "late morning"), (6.0 * 3600, "lunch"), (11.0 * 3600, "dinner")])
        let periods = RecordingDayRecapContent(parts: [.init(recap: spread, startedAt: dayStart)], zone: shanghai).sections
        // 11:02 is the morning, 14:02 the afternoon, 19:02 the evening.
        #expect(periods.map(\.period) == [.morning, .afternoon, .evening])
        #expect(periods.map { $0.entries.map(\.title) } == [["late morning"], ["lunch"], ["dinner"]])
    }

    /// 2026-11-01 in New York has 25 hours: 12.5 h after midnight is 11:30 on the clock, 18.5 h is 17:30.
    @Test func aDSTDayIsGroupedByItsOwnClock() throws {
        let newYork = TimeZone(identifier: "America/New_York")!
        let midnight = try #require(JSON.parseDate("2026-11-01T00:00:00-04:00"))
        let content = RecordingDayRecapContent(parts: [.init(recap: recap(entries: [(12.5 * 3600, "standup"), (18.5 * 3600, "review")]),
                                                             startedAt: midnight)], zone: newYork)
        #expect(content.sections.map(\.period) == [.morning, .afternoon])
    }

    @Test func partsJoinOnTheWallClock() throws {
        var first = recap(entries: [(11.0 * 3600, "first part")], summary: "Morning.", isFinal: true)
        first.actions = [.init(id: "a1", what: "Send the notes.", at: 11.0 * 3600, todoID: "t-9"), .init(id: "a2", what: "  ")]
        first.markdown = "# part 1"
        var second = recap(entries: [(600, "second part")], summary: "Evening.", isFinal: false)
        second.actions = [.init(id: "a1", what: "Book the room.", at: 60)]
        second.partial = true
        let secondStart = dayStart.addingTimeInterval(12 * 3600)
        let content = RecordingDayRecapContent(parts: [.init(recap: second, startedAt: secondStart), .init(recap: first, startedAt: dayStart)],
                                               zone: shanghai)
        #expect(content.sections.flatMap(\.entries).map(\.title) == ["first part", "second part"])
        #expect(content.sections.last?.entries.last?.from == secondStart.addingTimeInterval(600))
        #expect(content.summary == "Evening.\n\nMorning.")
        #expect(!content.isFinal, "final only once every part is")
        #expect(content.partial)
        #expect(content.actions.map(\.what) == ["Send the notes.", "Book the room."], "in time order, empty ones left out")
        #expect(content.actions.map(\.confirmed) == [true, false])
        #expect(Set(content.actions.map(\.id)).count == 2, "the same action id in two parts stays two items")
        #expect(content.markdown == "# part 1")
    }

    /// "Final recap" vs "Recap so far": final once every part's recap is; one made at the cutoff says so, since the rest
    /// of the day may still come.
    @Test func theRecapIsFinalOnlyOnceEveryPartsIs() {
        let complete = recap(entries: [], isFinal: true, finalReason: RecordingDayRecap.Reason.complete)
        let cutoff = recap(entries: [], isFinal: true, finalReason: RecordingDayRecap.Reason.cutoff)
        let soFar = recap(entries: [])
        #expect(RecordingDayRecapContent.finality([complete]) == .final)
        #expect(RecordingDayRecapContent.finality([recap(entries: [], isFinal: true)]) == .final, "a final recap without a reason")
        #expect(RecordingDayRecapContent.finality([complete, cutoff]) == .cutoff)
        #expect(RecordingDayRecapContent.finality([complete, soFar]) == .soFar)
        #expect(RecordingDayRecapContent.finality([]) == .soFar)
        let content = RecordingDayRecapContent(parts: [.init(recap: complete, startedAt: dayStart)], zone: shanghai)
        #expect(content.finality == .final && content.isFinal)
    }

    @Test func anEntryWithoutAStartIsPlacedByItsFirstPoint() {
        let recap = RecordingDayRecap(timeline: [
            .init(kind: "meeting", points: [.init(text: "Ship it.", at: 120)]),
            .init(from: 300, title: "  ", points: []),
            .init(title: "no time at all", points: [.init(text: "Nothing timed.")]),
        ])
        let entries = RecordingDayRecapContent(parts: [.init(recap: recap, startedAt: dayStart)], zone: shanghai).sections.flatMap(\.entries)
        #expect(entries.map(\.title) == ["Ship it."], "titled by its first point; no title and no time are left out")
        #expect(entries.first?.from == dayStart.addingTimeInterval(120))
        #expect(entries.first?.meeting == true)
    }

    // MARK: The recap's state

    @Test func theStateFollowsTheHubAndTheUpload() throws {
        let readyRecap = try recap(contractRecap)
        func state(_ parts: [RecordingDayRecapState.Part?]?, sync: RecordingDaySyncState = .synced, transcribed: Bool = true) -> RecordingDayRecapState {
            RecordingDayRecapState.of(parts: parts, sync: sync, transcribed: transcribed, fallbackStart: dayStart, zone: shanghai)
        }
        #expect(state(nil, sync: .notOffered) == .unsupported)
        #expect(state([.init(recapState: "ready", recap: readyRecap)], sync: .unsupported) == .unsupported)
        #expect(state(nil) == .waitingForSync, "no answer, text transcribed: it waits for the hub")
        #expect(state(nil, transcribed: false) == .waitingForText)
        #expect(state(nil, sync: .expired) == .none)
        #expect(state([nil]) == .waitingForSync, "the hub doesn't have it yet")
        #expect(state([.init(recapState: nil)]) == .waitingForText, "on the hub, no recap due yet")
        #expect(state([.init(recapState: "pending")]) == .writing)
        #expect(state([.init(recapState: "failed")]) == .failed)
        #expect(state([.init(recapState: "failed"), .init(recapState: "pending")]) == .failed)
        let content = RecordingDayRecapContent(parts: [.init(recap: readyRecap, startedAt: dayStart)], zone: shanghai)
        #expect(state([.init(recapState: "ready", recap: readyRecap, startedAt: dayStart)]) == .ready(content, note: nil))
        #expect(state([.init(recapState: "pending", recap: readyRecap)]) == .ready(content, note: .updating), "the previous recap stays readable")
        #expect(state([.init(recapState: "failed", recap: readyRecap)]) == .ready(content, note: .failed))
        // Every part shares the day recording's clock, so a later part whose meta leaves it out borrows the day's start.
        #expect(state([nil, .init(recapState: "ready", recap: readyRecap)]) == .ready(content, note: nil))
        #expect(RecordingDayRecapState.of(parts: [.init(recapState: "ready", recap: readyRecap)], sync: .synced, transcribed: true,
                                          fallbackStart: nil, zone: shanghai) == .waitingForText, "no clock at all: not placed")
    }

    @Test func onlyAStateOnItsWayIsFollowed() throws {
        let content = RecordingDayRecapContent(parts: [], zone: shanghai)
        #expect(RecordingDayRecapState.waitingForText.waiting)
        #expect(RecordingDayRecapState.waitingForSync.waiting)
        #expect(RecordingDayRecapState.writing.waiting)
        #expect(RecordingDayRecapState.ready(content, note: .updating).waiting)
        #expect(!RecordingDayRecapState.ready(content, note: nil).waiting)
        #expect(!RecordingDayRecapState.ready(content, note: .failed).waiting)
        #expect(!RecordingDayRecapState.failed.waiting)
        #expect(!RecordingDayRecapState.unsupported.waiting)
        #expect(!RecordingDayRecapState.none.waiting)
    }

    // MARK: The transcript

    @Test func theTranscriptIsOnTheWallClockByTheHour() {
        let id = UUID()
        var record = RecordingDayRecord(id: id, day: "2026-11-11", timeZone: "Asia/Shanghai", startedAt: dayStart)
        func chunk(_ seq: Int, at offset: Double, _ sentences: [RecordingSentence], erased: Bool = false,
                   lost: Bool = false) -> RecordingDayChunk {
            RecordingDayChunk(seq: seq, startedAt: dayStart.addingTimeInterval(offset), duration: 60, receivedAt: dayStart,
                              state: .transcribed, sentences: sentences, hasAudio: true, erased: erased, lost: lost)
        }
        record.chunks[1] = chunk(1, at: 3600, [.init(start: 5, end: 8, text: "Second hour."), .init(start: 9, end: 9.5, text: "   ")])
        record.chunks[0] = chunk(0, at: 0, [.init(start: 1, end: 4, text: " First line. "), .init(start: 30, end: 33, text: "Still 08:02.")])
        record.chunks[2] = chunk(2, at: 3700, [.init(start: 0, end: 2, text: "Erased words.")], erased: true)
        record.chunks[3] = chunk(3, at: 3760, [.init(start: 0, end: 2, text: "Past the deadline.")], lost: true)
        let lines = RecordingDayTranscript.lines(record)
        #expect(lines.map(\.text) == ["First line.", "Still 08:02.", "Second hour."])
        #expect(lines.map(\.id) == ["s-0-0", "s-0-1", "s-1-0"])
        #expect(lines[2].at == dayStart.addingTimeInterval(3605))
        let hours = RecordingDayTranscript.hours(lines, in: shanghai)
        #expect(hours.map { $0.lines.count } == [2, 1])
        #expect(hours.first?.id == JSON.parseDate("2026-11-11T08:00:00+08:00"))
        // A recap's time lands on the line said by then (a second's grace), else the first.
        #expect(RecordingDayTranscript.line(at: dayStart.addingTimeInterval(29.5), in: lines)?.id == "s-0-1")
        #expect(RecordingDayTranscript.line(at: dayStart.addingTimeInterval(-60), in: lines)?.id == "s-0-0")
        #expect(RecordingDayTranscript.line(at: dayStart, in: []) == nil)
    }

    // MARK: The model

    @Test func theModelReadsEveryPartOfTheDay() async throws {
        let first = UUID(), second = UUID(), third = UUID()
        let hub = FakeHub()
        // The third part isn't on the hub yet (not listed); the second has no recap due.
        hub.on("GET", listPath, json: listing([(first, "ready"), (second, nil)]))
        hub.on("GET", kbPath(first), json: detail(state: "ready", recap: contractRecap))
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in })
        await model.load(summary(id: first, hubIDs: [first, second, third]))
        guard case .ready(let content, let note) = model.state else {
            Issue.record("not ready: \(model.state)")
            return
        }
        #expect(note == nil)
        #expect(content.summary == "Mostly the export release.")
        #expect(hub.sent("GET", listPath).count == 1)
        #expect(hub.sent("GET", kbPath(first)).count == 1)
        #expect(hub.sent("GET", kbPath(second)).isEmpty, "no recap due: its detail (the whole day's text) isn't read")
        #expect(hub.sent("GET", kbPath(third)).isEmpty, "not on the hub")
        #expect(!model.waiting)
    }

    @Test func aDayWithNoRecapDueReadsNoDetail() async {
        let id = UUID()
        let hub = FakeHub()
        hub.on("GET", listPath, json: listing([(id, nil)]))
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in })
        await model.load(summary(id: id))
        #expect(model.state == .waitingForText)
        #expect(model.waiting)
        #expect(hub.sent("GET", kbPath(id)).isEmpty)
        // Not uploaded yet: not listed.
        let other = UUID()
        await model.load(summary(id: other))
        #expect(model.state == .waitingForSync)
        #expect(hub.sent("GET", kbPath(other)).isEmpty)
    }

    @Test func aHubWithoutDayRecordingsIsNotAsked() async {
        let hub = FakeHub()
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in })
        await model.load(summary(sync: .notOffered))
        #expect(model.state == .unsupported)
        #expect(hub.requests.isEmpty)
    }

    @Test func aLookReadsTheRecapAgainOnlyWhenItsStateMoved() async throws {
        let id = UUID()
        let hub = FakeHub()
        hub.on("GET", kbPath(id), json: detail(state: "pending"))
        hub.on("GET", listPath, json: listing([(id, "pending")]))
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in })
        let day = summary(id: id, recapState: "pending")
        await model.load(day)
        #expect(model.state == .writing)
        #expect(model.waiting)
        await model.load(day)
        #expect(hub.sent("GET", listPath).count == 2)
        #expect(hub.sent("GET", kbPath(id)).count == 1, "nothing moved: the detail (the whole day's text) isn't read again")
        hub.on("GET", listPath, json: listing([(id, "ready")]))
        hub.on("GET", kbPath(id), json: detail(state: "ready", recap: contractRecap))
        await model.load(day)
        #expect(hub.sent("GET", kbPath(id)).count == 2)
        guard case .ready = model.state else {
            Issue.record("not ready: \(model.state)")
            return
        }
        #expect(!model.waiting)
    }

    /// The list may say a recap is ready before the knowledge base serves it: the page shows what the detail says, and
    /// reads it again at each look until it agrees.
    /// A recap can become the final one while its state stays `ready` (the day became complete between two looks): the
    /// list's `recap_final` moving is enough to read the detail again, so the page says "Final recap".
    @Test func aRecapThatBecameFinalIsReadAgain() async throws {
        let id = UUID()
        let hub = FakeHub()
        hub.on("GET", listPath, json: listing([(id, "ready")]))
        hub.on("GET", kbPath(id), json: detail(state: "ready", recap: contractRecap))
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in })
        let day = summary(id: id, recapState: "ready")
        await model.load(day)
        guard case .ready(let soFar, _) = model.state else {
            Issue.record("not ready: \(model.state)")
            return
        }
        #expect(soFar.finality == .soFar)
        let finalRecap = contractRecap.replacingOccurrences(of: #""final": false"#, with: #""final": true"#)
        hub.on("GET", listPath, json: listing([(id, "ready")], final: [id]))
        hub.on("GET", kbPath(id), json: detail(state: "ready", final: true, recap: finalRecap))
        await model.load(day)
        #expect(hub.sent("GET", kbPath(id)).count == 2, "the state stayed ready, the recap became final")
        guard case .ready(let finalContent, _) = model.state else {
            Issue.record("not ready: \(model.state)")
            return
        }
        #expect(finalContent.finality == .final)
        await model.load(day)
        #expect(hub.sent("GET", kbPath(id)).count == 2, "nothing moved since: not read again")
    }

    @Test func aDetailBehindTheListIsReadAgain() async throws {
        let id = UUID()
        let hub = FakeHub()
        hub.on("GET", listPath, json: listing([(id, "ready")]))
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in })
        let day = summary(id: id, recapState: "pending")
        await model.load(day)
        #expect(hub.sent("GET", kbPath(id)).count == 1, "not served yet (404)")
        hub.on("GET", kbPath(id), json: detail(state: "pending"))
        await model.load(day)
        #expect(hub.sent("GET", kbPath(id)).count == 2)
        #expect(model.state == .writing, "the detail's own state, said with its recap")
        hub.on("GET", kbPath(id), json: detail(state: "ready", recap: contractRecap))
        await model.load(day)
        #expect(hub.sent("GET", kbPath(id)).count == 3)
        guard case .ready = model.state else {
            Issue.record("not ready: \(model.state)")
            return
        }
        await model.load(day)
        #expect(hub.sent("GET", kbPath(id)).count == 3, "they agree: not read again")
    }

    /// What a look finds goes to the day's upload, so the day's rows and the page's header say what the recap section
    /// shows; only when it is news to the day's summary, and only with every part listed.
    @Test func whatALookFindsReachesTheDaysRows() async throws {
        let first = UUID(), second = UUID()
        let hub = FakeHub()
        hub.on("GET", listPath, json: listing([(first, "ready"), (second, "ready")]))
        hub.on("GET", kbPath(first), json: detail(state: "ready", recap: contractRecap))
        hub.on("GET", kbPath(second), json: detail(state: "ready", startedAt: "2026-11-11T20:00:00+08:00"))
        var reported: [(day: UUID, rows: [RecordingDayHubRow])] = []
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { reported.append((day: $0, rows: $1)) })
        let stale = summary(id: first, hubIDs: [first, second], recapState: "pending")
        await model.load(stale)
        #expect(reported.count == 1)
        #expect(reported.first?.day == first)
        #expect(reported.first?.rows.map(\.id) == [first, second])
        #expect(reported.first?.rows.map(\.recapState) == ["ready", "ready"])
        #expect(reported.first?.rows.first?.base == 1762826467250)
        #expect(reported.first?.rows.first?.next == 120)
        // The header says it before the summary catches up.
        #expect(model.shown(stale).recapState == "ready")
        #expect(RecordingDayStatus.word(model.shown(stale)) == .recapReady)
        // Once the summary says it, there is nothing to tell.
        await model.load(summary(id: first, hubIDs: [first, second], recapState: "ready"))
        #expect(reported.count == 1)
        // A part the list leaves out is reconciliation's to judge, and the header goes back to the summary.
        hub.on("GET", listPath, json: listing([(first, "ready")]))
        await model.load(stale)
        #expect(reported.count == 1)
        #expect(model.shown(stale).recapState == "pending")
    }

    /// A recap that became final is news too: the day's upload stores it (the audio's 3-day clock starts there), and
    /// once the summary says it, there is nothing to tell.
    @Test func aFinalRecapTheSummaryDoesntKnowIsReported() async throws {
        let id = UUID()
        let hub = FakeHub()
        hub.on("GET", listPath, json: listing([(id, "ready")], final: [id]))
        hub.on("GET", kbPath(id), json: detail(state: "ready", final: true, recap: contractRecap))
        var reported: [[RecordingDayHubRow]] = []
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { reported.append($1) })
        var day = summary(id: id, hubIDs: [id], recapState: "ready")
        await model.load(day)
        #expect(reported.count == 1)
        #expect(reported.first?.first?.recapFinal == true)
        #expect(reported.first?.first?.recapFinalAt == JSON.parseDate("2026-11-12T06:00:00Z"))
        day.recapFinal = true
        await model.load(day)
        #expect(reported.count == 1)
    }

    @Test func aDayTheUploadHasNotNamedIsNotReported() async {
        let id = UUID()
        let hub = FakeHub()
        hub.on("GET", listPath, json: listing([(id, "pending")]))
        hub.on("GET", kbPath(id), json: detail(state: "pending"))
        var reported = 0
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in reported += 1 })
        await model.load(summary(id: id, hubIDs: []))
        #expect(model.state == .writing)
        #expect(reported == 0, "no parts on the day yet: nothing to store the state on")
    }

    @Test func aReadyRecapStaysWhenTheHubIsOutOfReach() async throws {
        let id = UUID()
        let hub = FakeHub()
        hub.on("GET", listPath, json: listing([(id, "ready")]))
        hub.on("GET", kbPath(id), json: detail(state: "ready", recap: contractRecap))
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in })
        await model.load(summary(id: id))
        hub.on("GET", listPath) { _ in throw APIError(status: 503, message: "offline") }
        await model.load(summary(id: id))
        guard case .ready = model.state else {
            Issue.record("the recap went: \(model.state)")
            return
        }
    }

    @Test func aFailureStaysWhenTheHubIsOutOfReach() async {
        let id = UUID()
        let hub = FakeHub()
        hub.on("GET", listPath, json: listing([(id, "failed")]))
        hub.on("GET", kbPath(id), json: detail(state: "failed"))
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in })
        await model.load(summary(id: id))
        #expect(model.state == .failed)
        hub.on("GET", listPath) { _ in throw APIError(status: 503, message: "offline") }
        await model.load(summary(id: id))
        #expect(model.state == .failed, "the last read, not \"waiting for the text\"")
    }

    @Test func retryAsksTheHubForTheFailedPart() async throws {
        let first = UUID(), second = UUID()
        let hub = FakeHub()
        hub.on("GET", listPath, json: listing([(first, "ready"), (second, "failed")]))
        hub.on("GET", kbPath(first), json: detail(state: "ready", recap: contractRecap))
        hub.on("GET", kbPath(second), json: detail(state: "failed", startedAt: "2026-11-11T20:00:00+08:00"))
        hub.on("POST", "/api/v1/recordings/\(second.uuidString.lowercased())/minutes", json: #"{"state": "pending"}"#)
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in })
        let day = summary(id: first, hubIDs: [first, second], recapState: "failed")
        await model.load(day)
        guard case .ready(_, .failed) = model.state else {
            Issue.record("expected the first part's recap with the second's failure: \(model.state)")
            return
        }
        await model.regenerate(day)
        #expect(hub.sent("POST", "/api/v1/recordings/\(second.uuidString.lowercased())/minutes").count == 1)
        #expect(hub.sent("POST", "/api/v1/recordings/\(first.uuidString.lowercased())/minutes").isEmpty, "only the part that failed")
        guard case .ready(_, .updating) = model.state else {
            Issue.record("expected the recap shown as updating: \(model.state)")
            return
        }
        #expect(model.round == 1)
        #expect(!model.asking)
        // The next look reads the part asked again, whatever the list says by then; the other stays as read.
        await model.load(day)
        #expect(hub.sent("GET", kbPath(second)).count == 2)
        #expect(hub.sent("GET", kbPath(first)).count == 1)
    }

    // MARK: A recap that never comes

    /// A day that ended without a word (every chunk quieter than speech): the hub lists it with no recap state, which it
    /// keeps for good. The page shows no "comes later" and stops asking.
    @Test func aSilentCompleteDayWaitsForNoRecap() async {
        let id = UUID()
        let hub = FakeHub()
        hub.on("GET", listPath, json: listing([(id, nil)], next: 0))
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in }, now: { dayEnd.addingTimeInterval(60) })
        let day = completeDay(id: id, silent: true)
        #expect(RecordingDayStatus.word(day) == .noSpeech)
        await model.load(day)
        #expect(model.state == .none)
        #expect(!model.waiting, "polled the hub for a recap that never comes")
        // Not complete yet (a chunk may still come from the watch): it may still have words, so it waits.
        var open = day
        open.complete = false
        await model.load(open)
        #expect(model.state == .waitingForText)
        #expect(model.waiting)
        // Offline: the phone's own facts say the same.
        #expect(RecordingDayRecapState.of(parts: nil, sync: .synced, transcribed: true, day: .init(day), fallbackStart: dayStart,
                                          zone: shanghai) == .none)
    }

    /// Transcribed chunks whose speech held no sentence: the phone can't tell, the hub's list does (no position held).
    @Test func aCompleteDayTheHubHoldsNoTextOfWaitsForNoRecap() async {
        let id = UUID()
        let hub = FakeHub()
        hub.on("GET", listPath, json: listing([(id, nil)], next: 0))
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in }, now: { dayEnd.addingTimeInterval(60) })
        await model.load(completeDay(id: id, silent: false))
        #expect(model.state == .none)
        #expect(!model.waiting)
        // With text there, the recap comes: the hub writes the final one when the day becomes complete.
        hub.on("GET", listPath, json: listing([(id, nil)], next: 40))
        await model.load(completeDay(id: id, silent: false))
        #expect(model.state == .waitingForText)
        #expect(model.waiting)
    }

    /// A complete day's last recap the hub writes by itself is due 6 hours and a half after the day ended: still none
    /// listed by then, none comes unless asked, and the page stops asking every 30 seconds.
    @Test func aCompleteDayPastItsLastRecapIsNotPolled() async {
        let id = UUID()
        let hub = FakeHub()
        hub.on("GET", listPath, json: listing([(id, nil)]))
        var now = dayEnd.addingTimeInterval(3600)
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in }, now: { now })
        let day = completeDay(id: id, silent: false)
        await model.load(day)
        #expect(model.state == .waitingForText)
        #expect(model.waiting)
        now = dayEnd.addingTimeInterval(RecordingDayRecapState.Day.lastRecapAfter)
        #expect(model.state == .waitingForText)
        #expect(!model.waiting)
        // An open day keeps following: its recap comes in the evening, and again once it ends.
        var open = day
        open.complete = false
        open.closedAt = nil
        await model.load(open)
        #expect(model.waiting)
        // A recap being written is always followed.
        #expect(RecordingDayRecapState.writing.follows(.init(day), now: now))
    }

    /// The hub refused the day (a 400, or another kind of recording under its id): nothing more is sent, so nothing is
    /// waited for, offline or not.
    @Test func aDayKnowledgeDidntAcceptWaitsForNothing() async {
        let id = UUID()
        let hub = FakeHub()
        hub.on("GET", listPath, json: listing([]))
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in })
        let refused = summary(id: id, hubIDs: [id], sync: .problem)
        await model.load(refused)
        #expect(model.state == .none, "not \"It's written once the day's text reaches the hub\"")
        #expect(!model.waiting)
        #expect(RecordingDayStatus.word(refused) == .notAccepted)
        #expect(RecordingDayRecapState.of(parts: nil, sync: .problem, transcribed: true, fallbackStart: dayStart, zone: shanghai) == .none)
        // A part the hub holds keeps its recap (another part was the one refused).
        hub.on("GET", listPath, json: listing([(id, "ready")]))
        hub.on("GET", kbPath(id), json: detail(state: "ready", recap: contractRecap))
        await model.load(refused)
        guard case .ready = model.state else {
            Issue.record("the held part's recap went: \(model.state)")
            return
        }
    }

    /// The hub took the empty create, then refused the first append: it lists the part with no position and no recap,
    /// and nothing more goes, so nothing is waited for; a stopped day whose part holds text waits only as long as a
    /// complete one would.
    @Test func aStoppedUploadWaitsOnlyForWhatTheHubHolds() async {
        let id = UUID()
        let hub = FakeHub()
        hub.on("GET", listPath, json: listing([(id, nil)], next: 0))
        var now = dayEnd.addingTimeInterval(60)
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in }, now: { now })
        let refused = summary(id: id, hubIDs: [id], sync: .problem, closedAt: dayEnd)
        await model.load(refused)
        #expect(model.state == .none)
        #expect(!model.waiting)
        hub.on("GET", listPath, json: listing([(id, nil)], next: 40))
        await model.load(refused)
        #expect(model.state == .waitingForText, "the hub writes from what it holds")
        #expect(model.waiting)
        now = dayEnd.addingTimeInterval(RecordingDayRecapState.Day.lastRecapAfter)
        #expect(!model.waiting)
    }

    /// The page's recap task restarts only on what changes the recap's state: the upload's pending / synced flips with
    /// each chunk while the watch records, and isn't one of them.
    @Test func theRecapTaskIsKeyedOnWhatMovesTheRecap() {
        let id = UUID()
        let synced = summary(id: id, hubIDs: [id], sync: .synced)
        var pending = synced
        pending.sync = .pending
        pending.recordedSeconds += 10
        pending.chunks += 1
        #expect(RecordingDayRecapKey(synced, round: 0) == RecordingDayRecapKey(pending, round: 0))
        #expect(RecordingDayRecapKey(synced, round: 0) != RecordingDayRecapKey(synced, round: 1), "Retry")
        for sync in [RecordingDaySyncState.notOffered, .unsupported, .problem, .expired] {
            var moved = synced
            moved.sync = sync
            #expect(RecordingDayRecapKey(synced, round: 0) != RecordingDayRecapKey(moved, round: 0), "\(sync)")
        }
        var ready = synced
        ready.recapState = "ready"
        #expect(RecordingDayRecapKey(synced, round: 0) != RecordingDayRecapKey(ready, round: 0))
        #expect(RecordingDayRecapKey(synced, round: 0) != RecordingDayRecapKey(completeDay(id: id, silent: false), round: 0))
        // A silent day recording: a chunk that comes in waiting, then is skipped as quiet, moves nothing while it is open;
        // once the day is complete, whether any of it may hold words does.
        let quiet = summary(id: id, hubIDs: [id], chunks: 10, skipped: 10)
        var arriving = quiet
        arriving.chunks += 1
        #expect(RecordingDayRecapKey(quiet, round: 0) == RecordingDayRecapKey(arriving, round: 0))
        #expect(RecordingDayRecapKey(completeDay(id: id, silent: true), round: 0)
                != RecordingDayRecapKey(completeDay(id: id, silent: false), round: 0))
    }

    /// A look cut short by the task starting again isn't the hub out of reach: what the page shows stays as it was.
    @Test func aCancelledLookChangesNothing() async {
        let id = UUID()
        let hub = FakeHub()
        hub.on("GET", listPath) { _ in throw CancellationError() }
        let model = RecordingDayRecapModel(api: { hub.client }, reconciled: { _, _ in })
        await model.load(summary(id: id))
        #expect(model.state == .none, "not \"waiting for sync\" after a first look that was cancelled")
        hub.on("GET", listPath, json: listing([(id, "pending")]))
        hub.on("GET", kbPath(id), json: detail(state: "pending"))
        await model.load(summary(id: id))
        #expect(model.state == .writing)
        hub.on("GET", listPath) { _ in throw URLError(.cancelled) }
        await model.load(summary(id: id, transcribedTo: nil))
        #expect(model.state == .writing)
    }
}
