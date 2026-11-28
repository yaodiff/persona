import Foundation
import Testing
@testable import Persona

private let anchor = Date(timeIntervalSince1970: 1_800_000_000) // 2027-01-15T08:00:00Z, 16:00 in Shanghai
private let newYork = TimeZone(identifier: "America/New_York")!
private let shanghai = "Asia/Shanghai"

private func utc(_ text: String) throws -> Date { try Date.ISO8601FormatStyle().parse(text) }

private func object(_ data: Data) throws -> [String: Any] {
    try #require(try JSONSerialization.jsonObject(with: data) as? [String: Any])
}

/// A header with every key set, at whole milliseconds so it survives the wire's RFC 3339 times.
private func fullHeader(_ id: UUID = UUID(), seq: Int = 41) -> RecordingDayHeader {
    RecordingDayHeader(recording: id, recordingStartedAt: anchor.addingTimeInterval(-3600), tz: shanghai, day: "2027-01-15",
                       seq: seq, startedAt: anchor.addingTimeInterval(0.25), duration: 10, level: -38.5)
}

/// An event's envelope for a recording in Shanghai.
private func envelope(_ event: RecordingDayEvent, _ id: UUID = UUID()) -> RecordingDayEventEnvelope {
    RecordingDayEventEnvelope(recording: id, recordingStartedAt: anchor.addingTimeInterval(-3600), tz: shanghai,
                              day: "2027-01-15", event: event)
}

/// The wire: keys, header, envelope, events, acknowledgements, erase and delete, capabilities, the watch's state, the
/// calendar, the level, the cached flags. Nonisolated on purpose: the phone runs all of it on WatchConnectivity's and
/// its store's queues.
struct RecordingDayWireTests {
    // MARK: Keys and limits

    @Test func keysAndLimitsAreTheContracts() {
        #expect(RecordingDayContract.chunkKey == "personaDayChunk" && RecordingDayContract.eventKey == "personaDayEvent")
        #expect(RecordingDayContract.ackKey == "personaDayAck")
        #expect(RecordingDayContract.eraseKey == "personaDayErase" && RecordingDayContract.erasedKey == "personaDayErased")
        #expect(RecordingDayContract.deleteKey == "personaDayDelete" && RecordingDayContract.deletedKey == "personaDayDeleted")
        #expect(RecordingDayContract.commandKey == "personaDayCommand")
        #expect(RecordingDayContract.phoneKey == "recordingDay" && RecordingDayContract.phoneHubKey == "recordingDayHub")
        #expect(RecordingDayContract.phoneKeepDaysKey == "recordingDayKeepDays" && RecordingDayContract.watchStateKey == "recordingDayWatch")
        #expect(Set(RecordingDayContract.messageKeys) == [RecordingDayContract.chunkKey, RecordingDayContract.eventKey,
                                                          RecordingDayContract.ackKey, RecordingDayContract.eraseKey,
                                                          RecordingDayContract.erasedKey, RecordingDayContract.deleteKey,
                                                          RecordingDayContract.deletedKey, RecordingDayContract.commandKey])
        #expect(RecordingDayContract.maxMessageBytes == 48_000)
        #expect(RecordingDayContract.ackTimeout == 30 && RecordingDayContract.fileAckTimeout == 60)
        #expect(RecordingDayContract.backoffFirst == 2 && RecordingDayContract.backoffMax == 300)
        #expect(RecordingDayContract.liveChunkSeconds == 10 && RecordingDayContract.backlogChunkSeconds == 60)
        #expect(WatchSync.phoneDayKeepDaysKey == "recording.phoneDayKeepDays")
    }

    @Test func keepDaysFollowTheAudioRetentionWithAForeverMaximum() {
        #expect(RecordingDayContract.keepDays(audioDays: nil) == 7, "7 until the phone knows")
        #expect(RecordingDayContract.keepDays(audioDays: 14) == 7)
        #expect(RecordingDayContract.keepDays(audioDays: 3) == 3)
        #expect(RecordingDayContract.keepDays(audioDays: 1) == 1)
        #expect(RecordingDayContract.keepDays(audioDays: 0) == 7, "forever still keeps the watch's 7-day maximum")
        #expect(RecordingDayContract.keepDays(audioDays: -4) == 7)
    }

    // MARK: Header

    @Test func headerHasExactlyTheContractKeys() throws {
        let header = fullHeader()
        let json = try object(header.json)
        #expect(Set(json.keys) == ["v", "recording", "recording_started_at", "tz", "day", "seq", "started_at", "duration", "level"])
        #expect(json["recording"] as? String == header.recording.uuidString.lowercased())
        #expect(json["v"] as? Int == RecordingDayContract.version && json["tz"] as? String == shanghai)
        // RFC 3339 with milliseconds, in the recording's own offset.
        #expect(json["started_at"] as? String == "2027-01-15T16:00:00.250+08:00")
        #expect(json["recording_started_at"] as? String == "2027-01-15T15:00:00.000+08:00")
        #expect(try JSONDecoder().decode(RecordingDayHeader.self, from: header.json) == header)

        // A level never measured is still one of the nine keys, as null, and reads back as unknown.
        var unmeasured = header
        unmeasured.level = nil
        let written = try object(unmeasured.json)
        #expect(Set(written.keys) == Set(json.keys) && written["level"] is NSNull)
        #expect(try JSONDecoder().decode(RecordingDayHeader.self, from: unmeasured.json) == unmeasured)
    }

    @Test func headerDecodesWhatOthersMayWrite() throws {
        let id = UUID()
        // No `v`, an upper-case id, a time without fractions, one in UTC, seconds since 1970, an unknown key, an older
        // draft's keys, and a level that isn't a number.
        let json = """
        {"recording": "\(id.uuidString.uppercased())", "recording_started_at": 12, "tz": "Asia/Shanghai", "day": "2027-01-15",
         "seq": 0, "started_at": "2027-01-15T16:00:00+08:00", "duration": 60, "future": {"x": 1},
         "erased": true, "end": true, "marks": [{"at": 1, "label": ""}], "after": "paused", "lost": true, "level": "loud"}
        """
        let header = try JSONDecoder().decode(RecordingDayHeader.self, from: Data(json.utf8))
        #expect(header.recording == id && header.seq == 0 && header.duration == 60 && header.v == 1)
        #expect(header.startedAt == anchor && header.recordingStartedAt == Date(timeIntervalSince1970: 12))
        #expect(header.level == nil && header.valid)

        let inUTC = """
        {"v": 1, "recording": "\(id)", "recording_started_at": "2027-01-15T07:00:00.000Z", "tz": "Asia/Shanghai",
         "day": "2027-01-15", "seq": 3, "started_at": "2027-01-15T08:00:00.500Z", "duration": 10}
        """
        #expect(try JSONDecoder().decode(RecordingDayHeader.self, from: Data(inUTC.utf8)).startedAt == anchor.addingTimeInterval(0.5))
    }

    @Test func headersWithoutAContractKeyAreRefused() throws {
        let full = try object(fullHeader().json)
        for key in ["recording", "recording_started_at", "tz", "day", "seq", "started_at", "duration"] {
            var json = full
            json[key] = nil
            let data = try JSONSerialization.data(withJSONObject: json)
            #expect(throws: (any Error).self, "\(key)") { try JSONDecoder().decode(RecordingDayHeader.self, from: data) }
        }
        var badTime = full
        badTime["started_at"] = "yesterday"
        let data = try JSONSerialization.data(withJSONObject: badTime)
        #expect(throws: (any Error).self) { try JSONDecoder().decode(RecordingDayHeader.self, from: data) }
    }

    @Test func invalidHeadersAreRefused() {
        #expect(fullHeader().valid)
        let broken: [(String, (inout RecordingDayHeader) -> Void)] = [
            ("version 0", { $0.v = 0 }),
            ("negative seq", { $0.seq = -1 }),
            ("seq too large", { $0.seq = 1_000_000 }),
            ("duration over 600 s", { $0.duration = 600.5 }),
            ("negative duration", { $0.duration = -1 }),
            ("NaN duration", { $0.duration = .nan }),
            ("infinite level", { $0.level = .infinity }),
            ("unpadded day", { $0.day = "2027-1-15" }),
            ("letters in the day", { $0.day = "2027-01-1a" }),
            ("full-width digits", { $0.day = "\u{FF12}\u{FF10}\u{FF12}\u{FF17}-01-15" }),
            ("no time zone", { $0.tz = "" }),
            ("an unknown time zone", { $0.tz = "Mars/Olympus_Mons" }),
        ]
        for (name, edit) in broken {
            var header = fullHeader()
            edit(&header)
            #expect(!header.valid, "\(name)")
            // Never read back as it was: refused, or (a level JSON can't hold) written as null.
            #expect(RecordingDayHeader(userInfo: header.userInfo) != header, "\(name)")
        }
    }

    @Test func headerOfAChunkOnTheWatch() {
        let id = UUID()
        var entry = RecordingEntry(id: id, createdAt: anchor.addingTimeInterval(-3600), source: .watch, kind: .day)
        entry.day = "2027-01-15"
        entry.timeZone = shanghai
        let chunk = RecordingSegment(index: 41, start: 0, duration: 10, startedAt: anchor.addingTimeInterval(0.25), level: -38.5)
        #expect(RecordingDayHeader(entry: entry, chunk: chunk) == fullHeader(id))

        var noZone = entry
        noZone.timeZone = nil
        var rolling = entry
        rolling.kind = .rolling
        #expect(RecordingDayHeader(entry: noZone, chunk: chunk) == nil && RecordingDayHeader(entry: rolling, chunk: chunk) == nil)
        #expect(RecordingDayHeader(entry: entry, chunk: RecordingSegment(index: 1, start: 0, duration: 10)) == nil,
                "a chunk without its wall clock")
    }

    // MARK: Envelope

    @Test func envelopesRoundTripAndFitTheLiveLimit() throws {
        let header = fullHeader()
        let audio = Data((0..<2000).map { UInt8($0 % 251) })
        let frame = header.frame(audio: audio)
        let length = header.json.count
        let prefix: [UInt8] = [0, 0, UInt8(length >> 8), UInt8(length & 0xFF)]
        #expect(frame.prefix(4) == Data(prefix))
        #expect(frame.count == 4 + header.json.count + audio.count)
        let back = try #require(RecordingDayHeader.unframe(frame))
        #expect(back.header == header && back.audio == audio)

        // Ten seconds at 32 kbps (about 40 KB) and a header fit under the live limit.
        #expect(header.frame(audio: Data(count: 40_000)).count < RecordingDayContract.maxMessageBytes)

        // A slice of a larger buffer (its indices don't start at 0) reads the same.
        let slice = (Data([9, 9, 9]) + frame).dropFirst(3)
        #expect(RecordingDayHeader.unframe(slice)?.audio == audio)
    }

    @Test func brokenEnvelopesAreRefused() {
        let header = fullHeader()
        let frame = header.frame(audio: Data([1, 2, 3]))
        #expect(RecordingDayHeader.unframe(Data()) == nil)
        #expect(RecordingDayHeader.unframe(Data([0, 0, 1])) == nil)
        #expect(RecordingDayHeader.unframe(frame.prefix(4 + header.json.count - 1)) == nil, "header cut short")
        #expect(RecordingDayHeader.unframe(Data([0, 0, 0, 0]) + header.json) == nil, "zero length")
        #expect(RecordingDayHeader.unframe(Data([0xFF, 0xFF, 0xFF, 0xFF]) + header.json) == nil, "absurd length")
        #expect(RecordingDayHeader.unframe(Data([0, 0, 0, 4]) + Data("nope".utf8)) == nil, "not JSON")
        var invalid = header
        invalid.tz = "Nowhere"
        #expect(RecordingDayHeader.unframe(invalid.frame(audio: Data([1]))) == nil, "an invalid header")
    }

    @Test func transferMetadataRoundTrips() {
        let header = fullHeader()
        #expect(header.userInfo.keys.sorted() == ["personaDayChunk"])
        #expect(RecordingDayHeader(userInfo: header.userInfo) == header)
        #expect(RecordingDayHeader(userInfo: nil) == nil)
        #expect(RecordingDayHeader(userInfo: ["personaRecording": header.json]) == nil)
        #expect(RecordingDayHeader(userInfo: [RecordingDayContract.chunkKey: "text"]) == nil)
    }

    // MARK: Control events

    @Test func eachEventWritesOnlyItsFieldsInTheRecordingsOffset() throws {
        let erase = UUID()
        let cases: [(RecordingDayEvent, Set<String>)] = [
            (.started(0, at: anchor), ["n", "type", "at"]),
            (.paused(1, at: anchor, reason: RecordingDayContract.Reason.interrupted), ["n", "type", "at", "reason"]),
            (.lost(2, seqs: 4...6, from: anchor, to: anchor.addingTimeInterval(30)), ["n", "type", "from_seq", "to_seq", "from", "to"]),
            (.lost(3, seqs: 7...7), ["n", "type", "from_seq", "to_seq"]),
            (.erase(4, id: erase, from: anchor.addingTimeInterval(-600), to: anchor, seqs: [9, 8, 12]),
             ["n", "type", "id", "from", "to", "seqs"]),
            (.closed(5, at: anchor, seqCount: 13), ["n", "type", "at", "seq_count"]),
            (.mark(6, at: anchor, label: RecordingDayEvent.Label.meeting), ["n", "type", "at", "label"]),
        ]
        for (event, keys) in cases {
            let sent = envelope(event)
            let outer = try object(sent.data)
            #expect(Set(outer.keys) == ["recording", "recording_started_at", "tz", "day", "event"])
            let inner = try #require(outer["event"] as? [String: Any])
            #expect(Set(inner.keys) == keys, "\(event.type.rawValue)")
            #expect(inner["type"] as? String == event.type.rawValue)
            if inner["at"] != nil { #expect(inner["at"] as? String == "2027-01-15T16:00:00.000+08:00") }
            #expect(RecordingDayEventEnvelope(data: sent.data) == sent, "\(event.type.rawValue)")
        }
        let erased = try object(envelope(cases[4].0).data)["event"] as? [String: Any]
        #expect(erased?["id"] as? String == erase.uuidString.lowercased())
        #expect(erased?["seqs"] as? [[Int]] == [[8, 9], [12, 12]])
    }

    @Test func anEventOnItsOwnIsWrittenInUTC() throws {
        // The watch's library keeps unacknowledged events without their recording's zone.
        let event = RecordingDayEvent.paused(3, at: anchor, reason: RecordingDayContract.Reason.stopped)
        let data = try JSONEncoder().encode(event)
        #expect(try object(data)["at"] as? String == "2027-01-15T08:00:00.000Z")
        #expect(try JSONDecoder().decode(RecordingDayEvent.self, from: data) == event)
    }

    @Test func theContractsExampleEventDecodes() throws {
        let id = UUID()
        let json = """
        {"recording": "\(id)", "recording_started_at": "2026-11-11T08:02:11.000+08:00", "tz": "Asia/Shanghai", "day": "2026-11-11",
         "event": {"n": 17, "type": "paused", "at": "2026-11-11T12:30:00.000+08:00", "reason": "interrupted"}}
        """
        let received = try #require(RecordingDayEventEnvelope(data: Data(json.utf8)))
        let (started, at) = (try utc("2026-11-11T00:02:11Z"), try utc("2026-11-11T04:30:00Z"))
        #expect(received.recording == id && received.day == "2026-11-11" && received.tz == shanghai)
        #expect(received.recordingStartedAt == started)
        #expect(received.event == .paused(17, at: at, reason: RecordingDayContract.Reason.interrupted))
    }

    @Test func unknownEventsAreKeptAndBrokenOnesRefused() throws {
        let id = UUID()
        func received(_ event: String, tz: String = shanghai) -> RecordingDayEventEnvelope? {
            RecordingDayEventEnvelope(data: Data("""
            {"recording": "\(id)", "recording_started_at": "2027-01-15T15:00:00+08:00", "tz": "\(tz)", "day": "2027-01-15", "event": \(event)}
            """.utf8))
        }
        // A newer watch's type, with fields this build doesn't know, decodes and keeps its type.
        let newer = try #require(received(#"{"n": 3, "type": "battery", "at": "2027-01-15T16:00:00+08:00", "percent": 12}"#))
        #expect(newer.event.type.rawValue == "battery" && newer.event.at == anchor && newer.event.valid)
        #expect(try object(newer.data)["event"].flatMap { $0 as? [String: Any] }?["type"] as? String == "battery")
        // Unreadable optional fields are absent; the event still counts.
        let sloppy = try #require(received(#"{"n": 4, "type": "erase", "at": "soon", "seqs": "all", "id": "x"}"#))
        #expect(sloppy.event.at == nil && sloppy.event.seqs == nil && sloppy.event.id == nil)
        #expect(received(#"{"type": "paused"}"#) == nil, "n is required")
        #expect(received(#"{"n": 5}"#) == nil, "type is required")
        #expect(received(#"{"n": -1, "type": "paused"}"#) == nil, "a negative n")
        #expect(received(#"{"n": 1, "type": "started"}"#, tz: "Mars/Base") == nil, "an unknown zone")
    }

    @Test func eventHelpersReadTheirSeqs() {
        let erase = RecordingDayEvent.erase(0, id: UUID(), from: anchor, to: anchor, seqs: [3, 1, 2, 7])
        #expect(erase.seqs == [[1, 3], [7, 7]] && erase.erasedSeqs == [1, 2, 3, 7])
        #expect(RecordingDayEvent.lost(1, seqs: 4...6).lostSeqs == 4...6)
        #expect(RecordingDayEvent(n: 2, type: .lost, fromSeq: 6, toSeq: 4).lostSeqs == nil)
        #expect(!RecordingDayEvent(n: 3, type: .mark, label: String(repeating: "x", count: 101)).valid)
        #expect(!RecordingDayEvent(n: 4, type: .lost, fromSeq: -1, toSeq: 2).valid)
    }

    // MARK: Acknowledgements

    @Test func acknowledgementsHaveTheContractShape() throws {
        let id = UUID()
        let ack = RecordingDayAck(recording: id, seqs: [2, 0, 1, 5], events: [4, 3])
        #expect(ack.acks == [[0, 2], [5, 5]] && ack.events == [[3, 4]])
        #expect(ack.seqs == [0, 1, 2, 5] && ack.eventNumbers == [3, 4] && !ack.isEmpty)
        let json = try object(ack.data)
        #expect(Set(json.keys) == ["recording", "acks", "events"])
        #expect(json["recording"] as? String == id.uuidString.lowercased())
        #expect(RecordingDayAck(data: ack.data) == ack)
        // Either list may be empty, and both are always written.
        let chunksOnly = RecordingDayAck(recording: id, seqs: [7])
        #expect(try object(chunksOnly.data)["events"] as? [[Int]] == [])
        #expect(RecordingDayAck(recording: id, events: [9]).acks.isEmpty)
        #expect(RecordingDayAck(recording: id).isEmpty)
        // A phone from before events sends no `events`.
        #expect(RecordingDayAck(data: Data(#"{"recording": "\#(id)", "acks": [[1, 2]]}"#.utf8))?.seqs == [1, 2])
        #expect(RecordingDayAck(data: Data("{}".utf8)) == nil && RecordingDayAck(data: nil) == nil)
    }

    // MARK: Erasing and deleting

    @Test func eraseRequestsAndAnswersHaveTheContractShape() throws {
        let recording = UUID(), erase = UUID()
        let request = RecordingDayEraseRequest(recording: recording, id: erase, from: anchor.addingTimeInterval(-600.5), to: anchor)
        let json = try object(request.data)
        #expect(Set(json.keys) == ["recording", "erase"])
        let range = try #require(json["erase"] as? [String: Any])
        #expect(Set(range.keys) == ["id", "from", "to"] && range["to"] as? String == "2027-01-15T08:00:00.000Z")
        #expect(RecordingDayEraseRequest(data: request.data) == request)
        let backwards = RecordingDayEraseRequest(recording: recording, id: erase, from: anchor, to: anchor.addingTimeInterval(-1))
        #expect(RecordingDayEraseRequest(data: backwards.data) == nil, "from after to")

        let done = RecordingDayEraseDone(id: erase, seqs: [[3, 5]], at: anchor)
        let answer = RecordingDayErased(recording: recording, done: done)
        #expect(Set(try object(answer.data).keys) == ["recording", "erased", "seqs"])
        #expect(RecordingDayErased(data: answer.data) == answer && answer.erasedSeqs == [3, 4, 5])
        #expect(RecordingDayErased(recording: recording, erased: erase, seqs: [5, 4, 3]) == answer)
    }

    /// The phone's resend of an erase request is not queued again while a copy waits with the system. That copy's
    /// times came through the wire's milliseconds, and the phone's own request keeps the instant it was made, so the
    /// two are the same request by recording and id, never by their times.
    @Test func aQueuedEraseRequestIsTheSameRequestWhateverItsTimesRoundTo() throws {
        let recording = UUID(), id = UUID()
        let request = RecordingDayEraseRequest(recording: recording, id: id, from: anchor.addingTimeInterval(-600.000_4),
                                               to: anchor.addingTimeInterval(0.000_7))
        let queued = try #require(RecordingDayEraseRequest(data: request.data))
        #expect(queued != request, "rounded to milliseconds on the wire")
        #expect(queued.isSame(as: request) && request.isSame(as: queued))
        #expect(!queued.isSame(as: RecordingDayEraseRequest(recording: recording, id: UUID(), from: request.erase.from,
                                                             to: request.erase.to)), "another erase")
        #expect(!queued.isSame(as: RecordingDayEraseRequest(recording: UUID(), id: id, from: request.erase.from,
                                                             to: request.erase.to)), "another recording's")
    }

    @Test func deletesAndTheirConfirmationsHaveTheContractShape() throws {
        let id = UUID()
        let delete = RecordingDayDelete(recording: id)
        let json = try object(delete.data)
        #expect(Set(json.keys) == ["recording", "deleted"] && json["deleted"] as? Bool == true)
        #expect(RecordingDayDelete(data: delete.data) == delete)
        #expect(RecordingDayDelete(data: Data(#"{"recording": "\#(id)", "deleted": false}"#.utf8)) == nil)

        let deleted = RecordingDayDeleted(recording: id)
        let answer = try object(deleted.data)
        #expect(Set(answer.keys) == ["recording", "deleted_ack"] && answer["deleted_ack"] as? Bool == true)
        #expect(RecordingDayDeleted(data: deleted.data) == deleted)
        #expect(RecordingDayDeleted(data: delete.data) == nil, "a delete is not its confirmation")
    }

    // MARK: Ranges

    @Test func rangesMergeAndExpand() {
        #expect(RecordingDayRanges.ranges([5, 1, 2, 3, 8, 7, 8, -1]) == [[1, 3], [5, 5], [7, 8]])
        #expect(RecordingDayRanges.runs([5, 1, 2, 3]) == [1...3, 5...5])
        #expect(RecordingDayRanges.ranges([Int]()) == [])
        #expect(RecordingDayRanges.expand([[1, 3], [5, 5], [7, 8]]) == [1, 2, 3, 5, 7, 8])
        // One number is a range of one; malformed ranges are skipped; overlaps count once.
        #expect(RecordingDayRanges.expand([[3], [5, 4], [-1, 2], [1, 2, 3], [], [9, 10], [10, 11]]) == [3, 9, 10, 11])
        // A range that names far too many seqs stops at the limit.
        #expect(RecordingDayRanges.expand([[0, Int.max]], limit: 5) == [0, 1, 2, 3, 4])
        let seqs = Set((0..<500).filter { $0 % 7 != 3 })
        #expect(Set(RecordingDayRanges.expand(RecordingDayRanges.ranges(seqs))) == seqs)
    }

    // MARK: Capabilities

    @Test func capabilitiesSayConsentHubAndKeepDays() {
        let agreed = RecordingDayCapabilities(consent: true, hub: false, keepDays: 30)
        #expect(agreed.context[RecordingDayContract.phoneKey] as? Int == RecordingDayContract.version)
        #expect(agreed.context[RecordingDayContract.phoneHubKey] as? Bool == false)
        #expect(agreed.context[RecordingDayContract.phoneKeepDaysKey] as? Int == 7, "never past 7 days")
        let waiting = RecordingDayCapabilities(consent: false, hub: true, keepDays: 3)
        #expect(waiting.context[RecordingDayContract.phoneKey] as? Int == 0, "0: the phone takes them, the user hasn't agreed")
        #expect(waiting.context[RecordingDayContract.phoneKeepDaysKey] as? Int == 3)

        var ctx: [String: Any] = ["summary": Data()]
        #expect(!agreed.isIn(ctx))
        agreed.context.forEach { ctx[$0.key] = $0.value }
        #expect(agreed.isIn(ctx) && !waiting.isIn(ctx))
        #expect(!RecordingDayCapabilities(consent: true, hub: true, keepDays: 7).isIn(ctx), "the hub flag changed")
    }

    @Test func phoneCapabilitiesChangeOnlyWithFullContexts() throws {
        let name = "watch-capabilities-\(UUID().uuidString)"
        let defaults = try #require(CountingDefaults(suiteName: name))
        defer { defaults.removePersistentDomain(forName: name) }
        #expect(WatchPhoneCapabilities.keepDays(defaults) == 7, "7 until the phone says")
        let full: [String: Any] = ["recordingRolling": true, RecordingDayContract.phoneKey: 0, RecordingDayContract.phoneHubKey: false,
                                   RecordingDayContract.phoneKeepDaysKey: 3, "summary": Data()]
        #expect(WatchPhoneCapabilities.store(full, full: true, in: defaults) == 4)
        #expect(defaults.object(forKey: WatchPhoneCapabilities.dayKey) as? Int == 0, "0 (agree on the phone) is kept apart from absent")
        #expect(defaults.object(forKey: WatchPhoneCapabilities.dayHubKey) as? Bool == false, "false is kept apart from absent")
        #expect(WatchPhoneCapabilities.keepDays(defaults) == 3)
        // The phone resends its whole context with every summary: nothing is written again.
        let before = defaults.writes
        #expect(WatchPhoneCapabilities.store(full, full: true, in: defaults) == 0)
        #expect(defaults.writes == before)
        // A pairing push by user info, or a config reply, carries no day keys and removes nothing.
        #expect(WatchPhoneCapabilities.store(["configs": Data()], full: false, in: defaults) == 0)
        #expect(defaults.object(forKey: WatchPhoneCapabilities.dayKey) as? Int == 0)
        // The user agreed, and the hub keeps transcripts longer than a week.
        var agreed = full
        agreed[RecordingDayContract.phoneKey] = 1
        agreed[RecordingDayContract.phoneKeepDaysKey] = 14
        #expect(WatchPhoneCapabilities.store(agreed, full: true, in: defaults) == 2)
        #expect(defaults.integer(forKey: WatchPhoneCapabilities.dayKey) == 1 && WatchPhoneCapabilities.keepDays(defaults) == 7)
        // A phone that no longer takes day recordings (an older build): its full context lacks the keys.
        #expect(WatchPhoneCapabilities.store(["recordingRolling": true], full: true, in: defaults) == 3)
        for key in [WatchPhoneCapabilities.dayKey, WatchPhoneCapabilities.dayHubKey, WatchPhoneCapabilities.keepDaysKey] {
            #expect(defaults.object(forKey: key) == nil, "\(key)")
        }
        #expect(WatchPhoneCapabilities.store(["recordingRolling": true], full: true, in: defaults) == 0)
        #expect(defaults.bool(forKey: WatchPhoneCapabilities.rollingKey), "rolling keeps its value")
        #expect(WatchSync.phoneDayKey == WatchPhoneCapabilities.dayKey && WatchSync.phoneDayHubKey == WatchPhoneCapabilities.dayHubKey)
        #expect(WatchSync.phoneDayKeepDaysKey == WatchPhoneCapabilities.keepDaysKey)
    }

    /// `recordingDay` 0 and absent differ: 0 is a phone that takes day recordings whose user has not agreed to the
    /// sheet's current version yet, so what the watch already recorded keeps going to it under the agreement it
    /// started with; absent is a phone from before, which takes nothing.
    @Test func aPhoneThatHasNotAgreedToThisSheetStillTakesARunningRecording() throws {
        let name = "watch-day-takes-\(UUID().uuidString)"
        let defaults = try #require(CountingDefaults(suiteName: name))
        defer { defaults.removePersistentDomain(forName: name) }
        #expect(!WatchPhoneCapabilities.takesDays(defaults), "a phone from before never sets the key")

        let waiting: [String: Any] = [RecordingDayContract.phoneKey: 0, RecordingDayContract.phoneHubKey: false,
                                      RecordingDayContract.phoneKeepDaysKey: 7]
        #expect(WatchPhoneCapabilities.storeDay(waiting, full: true, in: defaults) == 3)
        #expect(WatchPhoneCapabilities.takesDays(defaults), "0: the phone takes them; its user agreed to an older sheet")
        #expect(WatchPhoneCapabilities.keepDays(defaults) == 7, "the age limit still follows the phone")

        var agreed = waiting
        agreed[RecordingDayContract.phoneKey] = RecordingDayContract.version
        #expect(WatchPhoneCapabilities.storeDay(agreed, full: true, in: defaults) == 1)
        #expect(WatchPhoneCapabilities.takesDays(defaults))

        // A full context without the day keys (a phone from before, or another phone): nothing goes to it.
        #expect(WatchPhoneCapabilities.storeDay([:], full: true, in: defaults) == 3)
        #expect(!WatchPhoneCapabilities.takesDays(defaults))
    }

    /// What tells the watch's day transfer to look again (`WatchSync.onDayCapabilities`): any change of the day keys,
    /// the consent arriving above all, and only that. The rolling key and the resends of the same context don't.
    @Test func onlyAChangeOfTheDayKeysCountsAsOne() throws {
        let name = "watch-day-keys-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: name))
        defer { defaults.removePersistentDomain(forName: name) }
        var ctx: [String: Any] = ["recordingRolling": true, RecordingDayContract.phoneKey: 0, RecordingDayContract.phoneHubKey: true,
                                  RecordingDayContract.phoneKeepDaysKey: 7]
        #expect(WatchPhoneCapabilities.storeRolling(ctx, in: defaults) == 1)
        #expect(WatchPhoneCapabilities.storeDay(ctx, full: true, in: defaults) == 3)
        #expect(WatchPhoneCapabilities.storeDay(ctx, full: true, in: defaults) == 0, "the same context again")
        // The user agrees on the phone (after a reinstall cleared the consent): sending can start.
        ctx[RecordingDayContract.phoneKey] = 1
        #expect(WatchPhoneCapabilities.storeDay(ctx, full: true, in: defaults) == 1)
        // The hub's transcript retention drops to two days: the age limit follows.
        ctx[RecordingDayContract.phoneKeepDaysKey] = 2
        #expect(WatchPhoneCapabilities.storeDay(ctx, full: true, in: defaults) == 1 && WatchPhoneCapabilities.keepDays(defaults) == 2)
        ctx["recordingRolling"] = false
        #expect(WatchPhoneCapabilities.storeDay(ctx, full: true, in: defaults) == 0, "rolling is no day key")
        #expect(WatchPhoneCapabilities.store(ctx, full: true, in: defaults) == 1, "store is both")
    }

    #if os(iOS) || os(visionOS)
    /// The phone's capabilities for the watch's application context, sent at activation and again when the watch app
    /// is installed later (`WatchSync.sessionWatchStateDidChange`): rolling recording, and the day keys once the app said
    /// them, so a watch installed after the phone's launch still learns it may offer All-Day Recording.
    @MainActor @Test func theCapabilitiesForAWatchAppInstalledLaterHoldTheDayKeys() {
        let day = RecordingDayCapabilities(consent: false, hub: true, keepDays: 30)
        let ctx = WatchSync.capabilityContext(day: day)
        #expect(ctx["recordingRolling"] as? Bool == true && day.isIn(ctx))
        #expect(ctx[RecordingDayContract.phoneKey] as? Int == 0, "the watch points to the phone for the consent")
        #expect(Set(WatchSync.capabilityContext(day: nil).keys) == ["recordingRolling"], "never a consent nobody gave")
    }
    #endif

    // MARK: The watch's state

    @Test func watchStateRoundTripsAndReadsNewStates() throws {
        let state = RecordingDayWatchState(recording: UUID(), day: "2027-01-15", state: .interrupted, since: anchor, next: 12, pending: 3)
        #expect(RecordingDayWatchState(data: state.data) == state)
        #expect(state.data == state.data, "the same state is the same bytes, so an unchanged push is skipped")
        #expect(state.isOn)
        #expect(!RecordingDayWatchState(state: .off).isOn && !RecordingDayWatchState(state: .stopped).isOn)
        #expect(RecordingDayWatchState(state: .paused).isOn && RecordingDayWatchState(state: .recording).isOn)

        let newer = Data(#"{"state": "charging", "next": 4, "extra": true}"#.utf8)
        let read = try #require(RecordingDayWatchState(data: newer))
        #expect(read.state == .stopped && read.next == 4 && read.recording == nil)
        #expect(RecordingDayWatchState(data: Data(#"{"next": 4}"#.utf8)) == nil, "a state is required")
    }

    // MARK: Calendar

    @Test func dayFollowsTheTimeZone() throws {
        #expect(RecordingDayCalendar.day(of: anchor, in: TimeZone(identifier: shanghai)!) == "2027-01-15")
        // Exactly local midnight belongs to the day it starts.
        #expect(RecordingDayCalendar.day(of: anchor, in: TimeZone(identifier: "America/Los_Angeles")!) == "2027-01-15")
        #expect(RecordingDayCalendar.day(of: anchor, in: TimeZone(identifier: "Pacific/Honolulu")!) == "2027-01-14")
        // 01:30 in the hour New York repeats when DST ends is still that day.
        let repeated = try utc("2026-11-01T06:30:00Z")
        #expect(RecordingDayCalendar.day(of: repeated, in: newYork) == "2026-11-01")
    }

    @Test func midnightsAcrossDaylightSavingChanges() throws {
        // 2026-03-08 in New York has 23 hours, 2026-11-01 has 25.
        let march8 = try #require(RecordingDayCalendar.start(of: "2026-03-08", in: newYork))
        let march9 = try #require(RecordingDayCalendar.start(of: "2026-03-09", in: newYork))
        let (march8UTC, march9UTC, march8Late) = (try utc("2026-03-08T05:00:00Z"), try utc("2026-03-09T04:00:00Z"), try utc("2026-03-08T15:00:00Z"))
        #expect(march8 == march8UTC && march9 == march9UTC)
        #expect(march9.timeIntervalSince(march8) == 23 * 3600)
        #expect(RecordingDayCalendar.nextMidnight(after: march8Late, in: newYork) == march9)
        #expect(RecordingDayCalendar.end(of: "2026-03-08", in: newYork) == march9)

        let november1 = try #require(RecordingDayCalendar.start(of: "2026-11-01", in: newYork))
        let november2 = try #require(RecordingDayCalendar.start(of: "2026-11-02", in: newYork))
        let (repeatedHour, november3) = (try utc("2026-11-01T06:30:00Z"), try utc("2026-11-03T05:00:00Z"))
        #expect(november2.timeIntervalSince(november1) == 25 * 3600)
        #expect(RecordingDayCalendar.nextMidnight(after: repeatedHour, in: newYork) == november2)
        #expect(RecordingDayCalendar.end(of: "2026-11-01", in: newYork) == november2)
        // Midnight itself: the next one, a day later.
        #expect(RecordingDayCalendar.nextMidnight(after: november2, in: newYork) == november3)
        #expect(RecordingDayCalendar.nextMidnight(after: november2.addingTimeInterval(-1), in: newYork) == november2)
        for day in ["2026-03-08", "2026-03-09", "2026-11-01", "2026-11-02"] {
            let start = try #require(RecordingDayCalendar.start(of: day, in: newYork))
            #expect(RecordingDayCalendar.day(of: start, in: newYork) == day)
        }
    }

    @Test func aDayWhoseMidnightIsSkippedStartsAtOne() throws {
        // Havana moves its clocks from 00:00 to 01:00 on 2026-03-08: that day starts at 01:00 local (05:00 UTC).
        let havana = try #require(TimeZone(identifier: "America/Havana"))
        let (start, noonBefore) = (try utc("2026-03-08T05:00:00Z"), try utc("2026-03-07T17:00:00Z"))
        #expect(RecordingDayCalendar.start(of: "2026-03-08", in: havana) == start)
        #expect(RecordingDayCalendar.nextMidnight(after: noonBefore, in: havana) == start)
        #expect(RecordingDayCalendar.day(of: start, in: havana) == "2026-03-08")
    }

    @Test func onlyRealDatesHaveAStart() {
        for text in ["2026-02-30", "2026-13-01", "2026-00-10", "26-11-11", "2026/11/11", "2026-11-11 ", ""] {
            #expect(RecordingDayCalendar.start(of: text, in: newYork) == nil, "\(text)")
            #expect(RecordingDayCalendar.end(of: text, in: newYork) == nil, "\(text)")
        }
        #expect(RecordingDayCalendar.start(of: "2028-02-29", in: newYork) != nil)
        #expect(RecordingDayCalendar.isDay("2026-11-11") && !RecordingDayCalendar.isDay("2026-11-1"))
    }

    // MARK: Level

    @Test func levelIsTheRMSInDBFS() {
        // A full-scale sine has a mean square of 1/2: -3.01 dBFS.
        let sine = RecordingDayLevel.dBFS(sumOfSquares: 0.5 * 16_000, count: 16_000)
        #expect(abs(sine - (-3.0103)) < 0.001)
        #expect(RecordingDayLevel.dBFS(sumOfSquares: 16_000, count: 16_000) == 0)
        // Quiet speech at RMS 0.01 is -40 dBFS, above the silence line.
        #expect(abs(RecordingDayLevel.dBFS(sumOfSquares: 0.0001 * 100, count: 100) - (-40)) < 0.0001)
        #expect(RecordingDayLevel.dBFS(sumOfSquares: 0.0001 * 100, count: 100) > RecordingDayContract.silenceDBFS)
        let floors: [(Double, Int)] = [(0, 100), (1, 0), (.nan, 10), (.infinity, 10), (1e-30, 16_000), (-1, 10)]
        for (sum, count) in floors {
            #expect(RecordingDayLevel.dBFS(sumOfSquares: sum, count: count) == -160, "\(sum) / \(count)")
        }
    }

    // MARK: Chunk records

    @Test func fileNamesGiveTheirIndexBack() {
        for index in [0, 7, 999, 1234, 86_400] {
            #expect(RecordingSegment.index(fileName: RecordingSegment.fileName(index)) == index)
        }
        for name in ["seg-.m4a", "seg-x1.m4a", "seg-007.json", "recording.json", "seg--1.m4a", "seg-٣.m4a"] {
            #expect(RecordingSegment.index(fileName: name) == nil, "\(name)")
        }
    }

    @Test func dayChunksOverlapARangeHalfOpen() {
        func chunk(_ start: Double, _ duration: Double = 10) -> RecordingSegment {
            RecordingSegment(index: 0, start: 0, duration: duration, startedAt: anchor.addingTimeInterval(start))
        }
        let (from, to) = (anchor.addingTimeInterval(10), anchor.addingTimeInterval(38))
        #expect(!chunk(0).overlapsDay(from, to), "it ends where the range starts")
        #expect(chunk(5).overlapsDay(from, to) && chunk(30, 8).overlapsDay(from, to) && chunk(37.9).overlapsDay(from, to))
        #expect(!chunk(38).overlapsDay(from, to), "the chunk the erase's cut opened starts where the range ends")
        #expect(!RecordingSegment(index: 0, start: 0, duration: 10).overlapsDay(from, to), "no wall clock, no overlap")
    }

    // MARK: Cached flags

    @Test func hubFlagIsCachedAndWrittenOnlyOnChange() throws {
        let name = "recording-day-\(UUID().uuidString)"
        let defaults = try #require(CountingDefaults(suiteName: name))
        defer { defaults.removePersistentDomain(forName: name) }
        #expect(!RecordingDayDefaults.offered([], defaults: defaults))
        #expect(RecordingDayDefaults.offered([RecordingDayContract.feature, "x"], defaults: defaults))
        RecordingDayDefaults.remember(offered: false, defaults: defaults)
        #expect(defaults.writes == 1, "the first answer is kept, even false")
        RecordingDayDefaults.remember(offered: false, defaults: defaults)
        #expect(defaults.writes == 1)
        RecordingDayDefaults.remember(offered: true, defaults: defaults)
        RecordingDayDefaults.remember(offered: true, defaults: defaults)
        #expect(defaults.writes == 2)
        #expect(RecordingDayDefaults.offered([], defaults: defaults), "without a hello, the last one")
        #expect(!RecordingDayDefaults.offered(["meeting_attendance"], defaults: defaults), "a hello without the flag wins")
    }

    // MARK: Command order

    @Test func aLateCommandNeverUndoesANewerOne() throws {
        let name = "watch-day-commands-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: name))
        defer { defaults.removePersistentDomain(forName: name) }
        // The phone sends "start", then "stop"; start's message fails only after stop went out.
        let stamps = WatchDayCommandOrder.Stamps()
        let start = stamps.next(now: anchor.addingTimeInterval(-30))
        let stop = stamps.next(now: anchor.addingTimeInterval(-20))
        #expect(!stamps.isNewest(start), "the failed start is not queued as user info")
        #expect(stamps.isNewest(stop))
        // Had it been queued anyway, the watch gets stop, then the late start.
        #expect(WatchDayCommandOrder.accept(sentAt: stop, now: anchor, defaults: defaults))
        #expect(!WatchDayCommandOrder.accept(sentAt: start, now: anchor, defaults: defaults), "the late start is dropped")
        #expect(!WatchDayCommandOrder.accept(sentAt: stop, now: anchor, defaults: defaults), "stop arriving twice counts once")
        let relaunched = try #require(UserDefaults(suiteName: name))
        #expect(!WatchDayCommandOrder.accept(sentAt: start, now: anchor, defaults: relaunched), "the order outlives a relaunch")
        // A newer command is acted on, even when the phone's clock stepped back a little.
        let again = stamps.next(now: anchor.addingTimeInterval(-25))
        #expect(again > stop)
        #expect(WatchDayCommandOrder.accept(sentAt: again, now: anchor, defaults: defaults))
        #expect(stamps.next(now: anchor) < stamps.next(now: anchor), "two commands in the same instant still have an order")
    }

    @Test func aStaleCommandIsDroppedWithoutMovingTheOrder() throws {
        let name = "watch-day-commands-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: name))
        defer { defaults.removePersistentDomain(forName: name) }
        let sent = anchor.timeIntervalSince1970
        let late = anchor.addingTimeInterval(WatchDayCommandOrder.freshness + 1)
        #expect(!WatchDayCommandOrder.accept(sentAt: sent, now: late, defaults: defaults), "queued too long ago")
        #expect(defaults.object(forKey: WatchDayCommandOrder.lastKey) == nil)
        #expect(WatchDayCommandOrder.accept(sentAt: sent, now: anchor.addingTimeInterval(WatchDayCommandOrder.freshness), defaults: defaults))
        #expect(!WatchDayCommandOrder.accept(sentAt: .nan, now: anchor, defaults: defaults))
        #expect(defaults.double(forKey: WatchDayCommandOrder.lastKey) == sent)
    }

    @Test func commandsAreJSONCarryingTheirStamp() throws {
        let stamps = WatchDayCommandOrder.Stamps()
        let command = RecordingDayCommand(command: RecordingDayContract.Command.start, sentAt: stamps.next(now: anchor))
        let json = try object(command.data)
        #expect(Set(json.keys) == ["command", "sent_at"] && json["command"] as? String == "start")
        #expect(RecordingDayCommand(data: command.data) == command)
        // Two commands of the same instant keep their order through the wire.
        let sent = [stamps.next(now: anchor), stamps.next(now: anchor)]
        let read = sent.compactMap { RecordingDayCommand(data: RecordingDayCommand(command: "stop", sentAt: $0).data)?.sentAt }
        #expect(read == sent && read[0] < read[1])
        // A newer phone's command is passed on; a broken one is refused.
        #expect(RecordingDayCommand(data: Data(#"{"command": "pause", "sent_at": 12, "future": 1}"#.utf8))?.command == "pause")
        for broken in [#"{"command": "start"}"#, #"{"command": "", "sent_at": 12}"#, #"{"sent_at": 12}"#, #""start""#, "{"] {
            #expect(RecordingDayCommand(data: Data(broken.utf8)) == nil, "\(broken)")
        }
        #expect(RecordingDayCommand(data: nil) == nil)
    }
}

/// Counts writes to user defaults (a set or a removal is one, however Foundation carries it out inside).
private final class CountingDefaults: UserDefaults, @unchecked Sendable {
    private(set) var writes = 0
    private var depth = 0

    private func counting(_ write: () -> Void) {
        if depth == 0 { writes += 1 }
        depth += 1
        write()
        depth -= 1
    }

    override func set(_ value: Any?, forKey defaultName: String) { counting { super.set(value, forKey: defaultName) } }
    override func set(_ value: Bool, forKey defaultName: String) { counting { super.set(value, forKey: defaultName) } }
    override func set(_ value: Int, forKey defaultName: String) { counting { super.set(value, forKey: defaultName) } }
    override func removeObject(forKey defaultName: String) { counting { super.removeObject(forKey: defaultName) } }
}

/// The library's all-day operations, on a temporary folder.
@MainActor struct RecordingDayTests {
    private func library() -> RecordingLibrary {
        RecordingLibrary(root: FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString))
    }

    /// An open day recording on the watch that began at `anchor`.
    private func day(_ lib: RecordingLibrary, id: UUID = UUID(), createdAt: Date = anchor, day: String = "2027-01-15") throws -> UUID {
        try lib.create(source: .watch, kind: .day, id: id, createdAt: createdAt, day: day, timeZone: shanghai).id
    }

    /// Chunk `seq`, `duration` seconds from `start` seconds after `anchor`, with its audio on disk.
    @discardableResult
    private func chunk(_ lib: RecordingLibrary, _ id: UUID, _ seq: Int, at start: Double, duration: Double = 10,
                       register: Bool = true) throws -> RecordingSegment {
        try FileManager.default.createDirectory(at: lib.folder(id), withIntermediateDirectories: true)
        try Data([UInt8(seq % 256)]).write(to: lib.audio(id, seq))
        let segment = RecordingSegment(index: seq, start: start, duration: duration, startedAt: anchor.addingTimeInterval(start), level: -30)
        if register { lib.addSegment(id, segment) }
        return segment
    }

    private func hasAudio(_ lib: RecordingLibrary, _ id: UUID, _ seq: Int) -> Bool {
        FileManager.default.fileExists(atPath: lib.audio(id, seq).path)
    }

    @Test func createStartsTheCountersAndKeepsAudioAlreadyThere() throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        // The writer of the next day's recording can put audio in its folder before the entry exists.
        let next = UUID()
        try chunk(lib, next, 0, at: 0, register: false)
        let id = try day(lib, id: next)
        let entry = try #require(lib.entry(id))
        #expect(entry.kind == .day && entry.isDay && entry.day == "2027-01-15" && entry.timeZone == shanghai)
        #expect(entry.dayTimeZone == TimeZone(identifier: shanghai))
        #expect(entry.createdAt == anchor && entry.active && entry.source == .watch)
        #expect(entry.nextSeq == 0 && entry.nextEvent == 0 && entry.closedAt == nil && entry.dayEvents == nil)
        #expect(hasAudio(lib, id, 0))
        #expect(RecordingLibrary(root: lib.root).entry(id)?.kind == .day, "the kind is saved")
        // Asking again for the same id changes nothing.
        let again = try lib.create(source: .watch, title: "Other", kind: .day, id: id, createdAt: anchor.addingTimeInterval(60))
        #expect(again.title.isEmpty && again.createdAt == anchor)
        let clip = try lib.create(source: .watch)
        let rolling = try lib.create(source: .watch, kind: .rolling)
        #expect(clip.kind == nil && clip.nextSeq == nil, "a clip stays as before kinds")
        #expect(rolling.kind == .rolling && rolling.nextEvent == nil)
    }

    @Test func seqsAreHandedOutOnceFromAnyQueue() async throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        let id = try day(lib)
        // The writer takes seqs on its own queue.
        let seqs = await withTaskGroup(of: Int.self) { group in
            for _ in 0..<50 { group.addTask { lib.allocateSeq(id) } }
            var taken: [Int] = []
            for await seq in group { taken.append(seq) }
            return taken
        }
        #expect(seqs.sorted() == Array(0..<50) && lib.allocatedSeqs(id) == 50)
        // Every save keeps the counter, so a relaunch goes on from it.
        lib.appendDayEvent(id) { .mark($0, at: anchor, label: RecordingDayEvent.Label.bookmark) }
        #expect(lib.entry(id)?.nextSeq == 50 && lib.entry(id)?.nextIndex == 50)
        #expect(RecordingLibrary(root: lib.root).allocateSeq(id) == 50)

        // The next day's recording, named before midnight: its seqs start at 0, and its entry made later keeps them.
        let tomorrow = UUID()
        #expect(lib.allocateSeq(tomorrow) == 0 && lib.allocateSeq(tomorrow) == 1)
        _ = try day(lib, id: tomorrow, createdAt: anchor.addingTimeInterval(8 * 3600), day: "2027-01-16")
        #expect(lib.entry(tomorrow)?.nextSeq == 2 && lib.allocateSeq(tomorrow) == 2)
    }

    @Test func aSeqTakenRightBeforeACrashIsNeverTakenAgain() throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        let id = try day(lib)
        lib.appendDayEvent(id) { .started($0, at: anchor) }
        // The writer took seqs 0 and 1, and the app ended before either file opened or any save counted them.
        #expect(lib.allocateSeq(id) == 0 && lib.allocateSeq(id) == 1)
        #expect(RecordingLibrary(root: lib.root).entry(id)?.nextSeq == 0, "no save counted them")

        let restored = RecordingLibrary(root: lib.root)
        restored.recover(source: .watch)
        let entry = try #require(restored.entry(id))
        let lost = try #require(entry.dayEvents?.first { $0.type == .lost })
        #expect(lost.lostSeqs == 0...1 && lost.from != nil && lost.from == lost.to, "no audio of them existed: lost for no time")
        #expect(entry.nextSeq == 2 && entry.dayEvents?.last?.type == .paused)
        #expect(try FileManager.default.contentsOfDirectory(atPath: lib.folder(id).path) == ["recording.json"],
                "the record holds the counter now")
        // Reported once, and the counter goes on past them.
        let again = RecordingLibrary(root: lib.root)
        again.recover(source: .watch)
        #expect(again.entry(id)?.dayEvents?.map(\.n) == entry.dayEvents?.map(\.n))
        #expect(again.allocateSeq(id) == 2)
    }

    @Test func eventsAreNumberedSavedAndKeptUntilAcknowledged() throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        let id = try day(lib)
        let started = try #require(lib.appendDayEvent(id) { .started($0, at: anchor) })
        #expect(started.n == 0 && lib.entry(id)?.gapReason == nil)
        let mark = lib.appendDayEvent(id) { .mark($0, at: anchor.addingTimeInterval(5), label: RecordingDayEvent.Label.meeting) }
        // `n` is the library's, whatever the maker put there.
        let paused = lib.appendDayEvent(id) { _ in .paused(99, at: anchor.addingTimeInterval(9), reason: RecordingDayContract.Reason.interrupted) }
        #expect(mark?.n == 1 && paused?.n == 2)
        #expect(lib.entry(id)?.gapReason == RecordingDayContract.Reason.interrupted, "a pause keeps its reason")

        let reloaded = RecordingLibrary(root: lib.root)
        #expect(reloaded.entry(id)?.dayEvents == [started, mark, paused].compactMap { $0 } && reloaded.entry(id)?.nextEvent == 3)

        lib.ackDayEvents(id, ns: [0, 2])
        #expect(lib.entry(id)?.dayEvents?.map(\.n) == [1])
        let saved = lib.entry(id)?.updatedAt
        lib.ackDayEvents(id, ns: [0, 2, 40])
        #expect(lib.entry(id)?.updatedAt == saved, "an acknowledgement repeated writes nothing")
        lib.ackDayEvents(id, ns: [1])
        #expect(lib.entry(id)?.dayEvents == nil)
        #expect(lib.appendDayEvent(id) { .started($0, at: anchor.addingTimeInterval(60)) }?.n == 3, "numbers are never used again")
        #expect(lib.entry(id)?.gapReason == nil, "the microphone opened again")

        let rolling = try lib.create(source: .watch, kind: .rolling)
        #expect(lib.appendDayEvent(rolling.id) { .started($0, at: anchor) } == nil && lib.appendDayEvent(UUID()) { .started($0, at: anchor) } == nil)
    }

    @Test func acknowledgedChunksGoAtOnceAndNeverComeBack() throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        let id = try day(lib)
        for seq in 0..<3 { try chunk(lib, id, seq, at: Double(seq) * 10) }
        #expect(lib.ackDayChunks(id, seqs: [0, 1, 7]) == [0, 1])
        var entry = try #require(lib.entry(id))
        #expect(entry.segments.map(\.index) == [2] && entry.prunedBelow == 2 && entry.nextIndex == 3)
        #expect(!hasAudio(lib, id, 0) && !hasAudio(lib, id, 1) && hasAudio(lib, id, 2))
        // The writer's replay of its finished chunks at stop never brings one back.
        try chunk(lib, id, 0, at: 0)
        #expect(lib.entry(id)?.segments.map(\.index) == [2] && !hasAudio(lib, id, 0))
        // The same acknowledgement again (a resent file's) writes nothing.
        let saved = lib.entry(id)?.updatedAt
        #expect(lib.ackDayChunks(id, seqs: [0, 1]).isEmpty && lib.entry(id)?.updatedAt == saved)
        #expect(lib.ackDayChunks(id, seqs: [2]) == [2])
        entry = try #require(lib.entry(id))
        #expect(entry.segments.isEmpty && entry.prunedBelow == 3 && entry.prunedAbove == nil)
        #expect(lib.allocateSeq(id) == 3, "an acknowledged seq is never used again")
    }

    @Test func eraseDeletesTheHeldChunksInTheRange() throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        let id = try day(lib)
        for seq in 0..<3 { try chunk(lib, id, seq, at: Double(seq) * 10) }
        try chunk(lib, id, 3, at: 30, duration: 8) // the chunk the erase's cut closed at 38 s
        lib.ackDayChunks(id, seqs: [0])
        // The phone holds chunk 0 already; the range starts where chunk 0 ends.
        #expect(lib.eraseDayChunks(id, from: anchor.addingTimeInterval(10), to: anchor.addingTimeInterval(38)) == [1, 2, 3])
        try chunk(lib, id, 4, at: 38) // the chunk the cut opened
        let entry = try #require(lib.entry(id))
        #expect(entry.segments.map(\.index) == [4] && hasAudio(lib, id, 4), "the chunk after the cut keeps its audio")
        for seq in 1...3 { #expect(!hasAudio(lib, id, seq), "seq \(seq) has no audio") }
        #expect(entry.prunedBelow == 4 && entry.nextIndex == 5, "erased seqs are tombstoned")
        try chunk(lib, id, 2, at: 20)
        #expect(lib.entry(id)?.segments.map(\.index) == [4], "an erased chunk never comes back")
        #expect(lib.eraseDayChunks(id, from: anchor.addingTimeInterval(10), to: anchor.addingTimeInterval(38)).isEmpty, "nothing new")
        #expect(lib.entry(id)?.dayErases == nil, "the watch's own erase is an event, not a remembered request")

        let rolling = try lib.create(source: .watch, kind: .rolling)
        lib.addSegment(rolling.id, RecordingSegment(index: 0, start: 0, duration: 10, startedAt: anchor))
        #expect(lib.eraseDayChunks(rolling.id, from: anchor, to: anchor.addingTimeInterval(60)).isEmpty)
        #expect(lib.entry(rolling.id)?.segments.count == 1, "rolling recordings erase with `erase`")
    }

    @Test func aRepeatedPhoneEraseGetsTheSameAnswer() throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        let id = try day(lib)
        try chunk(lib, id, 0, at: 0)
        try chunk(lib, id, 1, at: 10)
        let request = UUID()
        let now = anchor.addingTimeInterval(60)
        #expect(lib.eraseDayChunks(id, from: anchor, to: anchor.addingTimeInterval(15), request: request, now: now) == [0, 1])
        let done = RecordingDayEraseDone(id: request, seqs: [[0, 1]], at: now)
        #expect(RecordingLibrary(root: lib.root).entry(id)?.dayErases == [done], "saved with the erase")
        // The answer was lost and the phone asks again: the same seqs, and nothing erased a second time.
        try chunk(lib, id, 2, at: 12)
        #expect(lib.eraseDayChunks(id, from: anchor, to: anchor.addingTimeInterval(15), request: request, now: now) == [0, 1])
        #expect(lib.entry(id)?.segments.map(\.index) == [2])
        // A request when the phone already has every chunk in the range is remembered too, with no seqs.
        let later = UUID()
        lib.ackDayChunks(id, seqs: [2])
        #expect(lib.eraseDayChunks(id, from: anchor, to: anchor.addingTimeInterval(15), request: later, now: now).isEmpty)
        #expect(lib.entry(id)?.dayErases?.last == RecordingDayEraseDone(id: later, seqs: [], at: now))
        // A week later the old requests are forgotten when a new one is remembered.
        let weekLater = now.addingTimeInterval(RecordingDayContract.eraseMemoryDays * 86_400 + 1)
        lib.eraseDayChunks(id, from: anchor, to: anchor.addingTimeInterval(15), request: UUID(), now: weekLater)
        #expect(lib.entry(id)?.dayErases?.map(\.at) == [weekLater])
    }

    @Test func closingADayCountsEverySeqHandedOut() throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        let id = try day(lib)
        for seq in 0..<2 { try chunk(lib, id, lib.allocateSeq(id), at: Double(seq) * 10) }
        // Seq 2 was cut at midnight and is still on its way from the writer.
        #expect(lib.allocateSeq(id) == 2)
        let midnight = try #require(RecordingDayCalendar.end(of: "2027-01-15", in: TimeZone(identifier: shanghai)!))
        let closed = try #require(lib.closeDay(id, at: midnight))
        #expect(closed == .closed(0, at: midnight, seqCount: 3))
        var entry = try #require(lib.entry(id))
        #expect(!entry.active && entry.closedAt == midnight && entry.nextSeq == 3 && entry.dayEvents == [closed])
        #expect(lib.closeDay(id, at: midnight.addingTimeInterval(60)) == nil && lib.entry(id)?.dayEvents?.count == 1, "closed once")
        #expect(lib.openDays(source: .watch).isEmpty)
        // Its last chunk still arrives and goes to the phone.
        try chunk(lib, id, 2, at: 20)
        #expect(lib.entry(id)?.segments.map(\.index) == [0, 1, 2])
        #expect(lib.entry(id)?.isDaySettled == false)
        lib.ackDayChunks(id, seqs: [0, 1, 2])
        #expect(lib.entry(id)?.isDaySettled == false, "until the closed event is acknowledged too")
        lib.ackDayEvents(id, ns: [0])
        entry = try #require(lib.entry(id))
        #expect(entry.isDaySettled, "the phone has all of it: it can go")
    }

    @Test func lostChunksAreReportedOncePerRun() throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        let id = try day(lib)
        for seq in 0..<5 { try chunk(lib, id, seq, at: Double(seq) * 10) }
        let events = lib.loseDayChunks(id, seqs: [1, 2, 4, 9])
        #expect(events == [.lost(0, seqs: 1...2, from: anchor.addingTimeInterval(10), to: anchor.addingTimeInterval(30)),
                           .lost(1, seqs: 4...4, from: anchor.addingTimeInterval(40), to: anchor.addingTimeInterval(50))],
                "seq 9 was never handed out")
        let entry = try #require(lib.entry(id))
        #expect(entry.segments.map(\.index) == [0, 3] && entry.dayEvents == events)
        #expect(!hasAudio(lib, id, 1) && !hasAudio(lib, id, 4) && hasAudio(lib, id, 3))
        #expect(lib.loseDayChunks(id, seqs: [1, 2, 4]).isEmpty, "reported once")
    }

    @Test func recoverReportsWhatTheExitCutShort() throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        // Recording: chunk 0 is registered; chunk 1 was being written; seq 2 was taken and a save counted it, but its
        // file never opened.
        let recording = try day(lib)
        lib.appendDayEvent(recording) { .started($0, at: anchor) }
        try chunk(lib, recording, lib.allocateSeq(recording), at: 0)
        try chunk(lib, recording, lib.allocateSeq(recording), at: 10, register: false)
        _ = lib.allocateSeq(recording)
        lib.appendDayEvent(recording) { .mark($0, at: anchor.addingTimeInterval(12), label: RecordingDayEvent.Label.bookmark) }
        // Paused before the exit, with nothing cut short.
        let paused = try day(lib, createdAt: anchor.addingTimeInterval(1))
        lib.appendDayEvent(paused) { .paused($0, at: anchor.addingTimeInterval(2), reason: RecordingDayContract.Reason.paused) }
        // Closed at midnight while its last chunk was on its way.
        let closed = try day(lib, createdAt: anchor.addingTimeInterval(2), day: "2027-01-14")
        try chunk(lib, closed, lib.allocateSeq(closed), at: 0, register: false)
        lib.closeDay(closed, at: anchor)
        let clip = try lib.create(source: .watch)
        // The last save is the latest moment the app is known to have run (the library stamps it with the real clock).
        let lastSave = try #require(lib.entry(recording)?.updatedAt)

        let restored = RecordingLibrary(root: lib.root)
        #expect(Set(restored.recover(source: .watch)) == [recording, paused], "open days stay open")
        let entry = try #require(restored.entry(recording))
        let added = try #require(entry.dayEvents?.suffix(2))
        let lost = try #require(added.first)
        let lastHeard = max(anchor.addingTimeInterval(10), lastSave)
        #expect(lost.type == .lost && lost.n == 2 && lost.lostSeqs == 1...2)
        #expect(lost.from != nil && lost.from! <= lastHeard, "from chunk 1's file")
        #expect(lost.to == lastHeard, "seq 2's file never opened: no audio of it was lost after the last known")
        #expect(added.last == .paused(3, at: lastHeard, reason: RecordingDayContract.Reason.stopped),
                "the microphone stopped with the app, after the last audio known")
        #expect(entry.active && entry.gapReason == RecordingDayContract.Reason.stopped && entry.nextSeq == 3)
        #expect(entry.segments.map(\.index) == [0] && hasAudio(lib, recording, 0) && !hasAudio(lib, recording, 1))

        #expect(restored.entry(paused)?.dayEvents?.count == 1 && restored.entry(paused)?.gapReason == RecordingDayContract.Reason.paused)
        let closedEvents = try #require(restored.entry(closed)?.dayEvents)
        #expect(closedEvents.map(\.type) == [.closed, .lost] && closedEvents.last?.lostSeqs == 0...0)
        #expect(!hasAudio(lib, closed, 0))
        #expect(restored.entry(clip.id)?.active == false, "other recordings end as before")

        // Nothing is reported twice.
        let again = RecordingLibrary(root: lib.root)
        again.recover(source: .watch)
        #expect(again.entry(recording)?.dayEvents?.map(\.n) == entry.dayEvents?.map(\.n))
        #expect(again.entry(closed)?.dayEvents?.map(\.n) == closedEvents.map(\.n))
        #expect(again.recover(source: .phone).isEmpty)
        #expect(again.allocateSeq(recording) == 3, "the counter stays")
    }

    @Test func aDeletedRecordingStaysDeleted() async throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        let id = try day(lib)
        let kept = try day(lib, createdAt: anchor.addingTimeInterval(1))
        #expect(await Self.offMain(lib, id) == (false, false))
        try lib.remove(id)
        #expect(await Self.offMain(lib, id) == (false, true))
        #expect(await Self.offMain(lib, kept) == (false, false))
        // A write already under way when it was deleted put its folder back.
        try FileManager.default.createDirectory(at: lib.folder(id), withIntermediateDirectories: true)
        try JSONEncoder().encode(RecordingEntry(id: id, createdAt: anchor, source: .watch, kind: .day))
            .write(to: lib.folder(id).appendingPathComponent("recording.json"))
        let restored = RecordingLibrary(root: lib.root)
        #expect(restored.entry(id) == nil && restored.entry(kept) != nil)
        #expect(!FileManager.default.fileExists(atPath: lib.folder(id).path), "its folder is gone")
        #expect(restored.isDeletedSnapshot(id) && restored.isDeleted(id))
        #expect(restored.appendDayEvent(id) { .started($0, at: anchor) } == nil)
        _ = restored.allocateSeq(id)
        #expect(!FileManager.default.fileExists(atPath: lib.folder(id).path), "a seq taken for it writes nothing")
    }

    /// A crash between the writer's midnight and the main thread making the next day's entry leaves a folder with the
    /// new day's seq counter and audio but no recording.json: no entry names it, `recover` never sees it, nobody was
    /// told of its audio. The next launch removes it. A folder without a seq counter's mark (the phone's chunks before
    /// the day's index row, a rolling segment before its entry) stays, and so does every recording.
    @Test func aDayFolderAnExitLeftWithoutItsEntryGoesAtTheNextLaunch() async throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        let kept = try day(lib)
        try chunk(lib, kept, lib.allocateSeq(kept), at: 0)
        // The next day's recording, named before midnight: its first seqs taken and audio written, never registered.
        let orphan = UUID()
        try chunk(lib, orphan, lib.allocateSeq(orphan), at: 3_600, register: false)
        try chunk(lib, orphan, lib.allocateSeq(orphan), at: 3_610, register: false)
        // Chunks the phone stored before the day's index row.
        let unnamed = UUID()
        try chunk(lib, unnamed, 0, at: 0, register: false)
        #expect(FileManager.default.fileExists(atPath: lib.folder(orphan).path))

        let relaunched = RecordingLibrary(root: lib.root)
        await relaunched.orphanCleanup?.value
        #expect(!FileManager.default.fileExists(atPath: lib.folder(orphan).path), "its audio doesn't outlive every limit")
        #expect(FileManager.default.fileExists(atPath: lib.audio(unnamed, 0).path))
        #expect(relaunched.entry(kept)?.segments.map(\.index) == [0] && hasAudio(relaunched, kept, 0))
        #expect(relaunched.entry(orphan) == nil && !relaunched.isDeleted(orphan))
        // Nothing to do at a launch without such folders.
        let removedFolder = RecordingLibrary(root: lib.root)
        await removedFolder.orphanCleanup?.value
        #expect(FileManager.default.fileExists(atPath: lib.audio(unnamed, 0).path))
    }

    /// Asks from a thread that isn't the main one, as the phone's chunk store does.
    nonisolated private static func probe(_ lib: RecordingLibrary, _ id: UUID) -> (main: Bool, deleted: Bool) {
        (Thread.isMainThread, lib.isDeletedSnapshot(id))
    }

    private static func offMain(_ lib: RecordingLibrary, _ id: UUID) async -> (Bool, Bool) {
        let result = await Task.detached { probe(lib, id) }.value
        return (result.main, result.deleted)
    }

    @Test func phoneIndexRowIsWrittenOnlyWhenItChanges() throws {
        let lib = library()
        defer { try? FileManager.default.removeItem(at: lib.root) }
        let id = UUID()
        let first = try #require(lib.adoptDay(id: id, day: "2027-01-15", timeZone: shanghai, createdAt: anchor.addingTimeInterval(60)))
        #expect(first.isDay && first.source == .watch && first.active && first.segments.isEmpty)
        #expect(first.day == "2027-01-15" && first.timeZone == shanghai && first.createdAt == anchor.addingTimeInterval(60))
        #expect(first.nextSeq == nil && first.dayEvents == nil, "the phone's row has none of the watch's bookkeeping")
        let saved = first.updatedAt
        // The next chunk of the same day: nothing to write.
        #expect(lib.adoptDay(id: id, day: "2027-01-15", timeZone: shanghai, createdAt: anchor.addingTimeInterval(60))?.updatedAt == saved)
        // An earlier start never moves later.
        lib.adoptDay(id: id, day: "2027-01-15", timeZone: shanghai, createdAt: anchor)
        lib.adoptDay(id: id, day: "2027-01-15", timeZone: shanghai, createdAt: anchor.addingTimeInterval(120))
        #expect(lib.entry(id)?.createdAt == anchor)
        lib.adoptDay(id: id, day: "2027-01-15", timeZone: shanghai, createdAt: anchor, closed: true)
        lib.adoptDay(id: id, day: "2027-01-15", timeZone: shanghai, createdAt: anchor)
        #expect(lib.entry(id)?.active == false, "an ended day is never reopened")
        #expect(RecordingLibrary(root: lib.root).entry(id)?.createdAt == anchor)

        try lib.remove(id)
        #expect(lib.adoptDay(id: id, day: "2027-01-15", timeZone: shanghai, createdAt: anchor) == nil)
        #expect(lib.entry(id) == nil && !FileManager.default.fileExists(atPath: lib.folder(id).path))
    }

    @Test func entriesFromBeforeDaysAndFromTheOlderDraftDecode() throws {
        var entry = RecordingEntry(id: UUID(), createdAt: anchor, source: .watch, kind: .rolling)
        entry.segments = [RecordingSegment(index: 0, start: 0, duration: 30, startedAt: anchor)]
        var json = try object(JSONEncoder().encode(entry))
        for key in ["day", "timeZone", "gapReason", "nextSeq", "nextEvent", "dayEvents", "dayErases", "closedAt"] {
            #expect(json[key] == nil, "\(key) is not written when unset")
        }
        json["segments"] = [["index": 0, "start": 0, "duration": 30, "delivered": false]]
        let old = try JSONDecoder().decode(RecordingEntry.self, from: JSONSerialization.data(withJSONObject: json))
        #expect(old.day == nil && old.dayEvents == nil && old.segments.first?.level == nil)
        #expect(!old.isDay && old.isRolling)

        // A watch entry written by the earlier draft: its markers and marks are ignored.
        json["kind"] = "day"
        json["dayMarks"] = [["at": 0, "label": ""]]
        json["dayEndAt"] = 0
        json["segments"] = [["index": 0, "start": 0, "duration": 0, "delivered": false, "end": true, "erased": true, "after": "paused"]]
        let draft = try JSONDecoder().decode(RecordingEntry.self, from: JSONSerialization.data(withJSONObject: json))
        #expect(draft.isDay && draft.segments.count == 1 && draft.closedAt == nil)
    }

    @Test func aDayIsTitledByItsDateInItsOwnTimeZone() throws {
        // 2027-01-15 in Kiritimati (UTC+14) begins at 10:00 UTC the day before: still the 15th wherever the phone is.
        let zone = try #require(TimeZone(identifier: "Pacific/Kiritimati"))
        var entry = RecordingEntry(id: UUID(), createdAt: anchor, source: .watch, kind: .day)
        entry.day = "2027-01-15"
        entry.timeZone = zone.identifier
        let noon = try #require(RecordingDayCalendar.start(of: "2027-01-15", in: zone)).addingTimeInterval(12 * 3600)
        #expect(entry.displayTitle.contains(noon.formatted(Date.FormatStyle(date: .abbreviated, time: .omitted, timeZone: zone))))
        entry.title = "Offsite"
        #expect(entry.displayTitle == "Offsite")
    }
}
