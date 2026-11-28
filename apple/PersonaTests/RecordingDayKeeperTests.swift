import Foundation
import os
import Testing
@testable import Persona

private let anchor = Date(timeIntervalSince1970: 1_800_000_000) // 2027-01-15T08:00:00Z, 16:00 in Shanghai
private let day: TimeInterval = 86_400

private func date(_ iso: String) throws -> Date { try Date.ISO8601FormatStyle().parse(iso) }

/// Chunk `seq` of a day, 10 s at `offset` from `anchor`, with its audio on the phone unless `hasAudio` is false.
private func chunk(_ seq: Int, at offset: Double, state: RecordingDayChunk.State, hasAudio: Bool = true) -> RecordingDayChunk {
    RecordingDayChunk(seq: seq, startedAt: anchor.addingTimeInterval(offset), duration: 10, receivedAt: anchor, state: state,
                      sentences: state == .transcribed ? [RecordingSentence(start: 1, end: 2, text: "x")] : nil, hasAudio: hasAudio)
}

private func record(day: String = "2027-01-15", zone: String = "Asia/Shanghai", chunks: [RecordingDayChunk],
                    finals: [Date?] = []) -> RecordingDayRecord {
    RecordingDayRecord(id: UUID(), day: day, timeZone: zone, startedAt: anchor,
                       chunks: Dictionary(uniqueKeysWithValues: chunks.map { ($0.seq, $0) }),
                       parts: finals.enumerated().map { RecordingDayPart(hubID: UUID(), part: $0.offset + 1, startedAt: anchor,
                                                                          recapFinalAt: $0.element) })
}

/// All-day recording's retention on the phone (docs/assistant/day-recap.md「Retention and privacy」): the day's audio at
/// the earliest of 3 days after its final recap, 7 days after its day and `audio_days` after it, whatever reached
/// the hub; the whole day once it began more than `audio_days` ago, unless audio is forever.
@MainActor struct RecordingDayKeeperTests {
    @Test func audioGoesAtTheEarliestOfTheThreeDeadlines() throws {
        // The day ends at midnight in Shanghai: 2027-01-15T16:00:00Z.
        let end = try date("2027-01-15T16:00:00Z")
        let recap = end.addingTimeInterval(3600)
        let chunks = [chunk(0, at: 0, state: .transcribed), chunk(1, at: 10, state: .skipped), chunk(2, at: 20, state: .failed),
                      chunk(3, at: 30, state: .waiting), chunk(4, at: 40, state: .transcribed, hasAudio: false)]
        let held = record(chunks: chunks, finals: [recap])
        #expect(RecordingDayRetention.audioDue(held, now: recap.addingTimeInterval(3 * day - 1), audioDays: nil).isEmpty)
        // Every chunk still holding audio goes, transcribed or not: an untranscribed one's time stays a gap.
        #expect(RecordingDayRetention.audioDue(held, now: recap.addingTimeInterval(3 * day), audioDays: nil) == [0, 1, 2, 3])
        #expect(RecordingDayRetention.nextDeadline(held, now: recap, audioDays: nil) == recap.addingTimeInterval(3 * day))

        // No hub ever wrote a final recap (an older hub, no flag): 7 days after the day, whether or not anything went up.
        let alone = record(chunks: [chunk(0, at: 0, state: .transcribed)])
        #expect(RecordingDayRetention.audioDue(alone, now: end.addingTimeInterval(7 * day - 1), audioDays: nil).isEmpty)
        #expect(RecordingDayRetention.audioDue(alone, now: end.addingTimeInterval(7 * day), audioDays: nil) == [0])
        // A hub keeping transcripts 2 days: the audio goes with them.
        #expect(RecordingDayRetention.audioDue(alone, now: end.addingTimeInterval(2 * day), audioDays: 2) == [0])
        #expect(RecordingDayRetention.nextDeadline(alone, now: end, audioDays: 2) == end.addingTimeInterval(2 * day))
        // Forever still keeps the audio's 7-day maximum, not zero.
        #expect(RecordingDayRetention.audioDue(alone, now: end.addingTimeInterval(7 * day), audioDays: 0) == [0])
        #expect(RecordingDayRetention.nextDeadline(alone, now: end, audioDays: 0) == end.addingTimeInterval(7 * day))

        // Several parts: the clock starts once every part has its first final recap, from the latest.
        let later = recap.addingTimeInterval(day)
        let parts = record(chunks: [chunk(0, at: 0, state: .transcribed)], finals: [recap, later])
        #expect(RecordingDayRetention.audioDue(parts, now: recap.addingTimeInterval(3 * day), audioDays: nil).isEmpty)
        #expect(RecordingDayRetention.audioDue(parts, now: later.addingTimeInterval(3 * day), audioDays: nil) == [0])
        let unfinished = record(chunks: [chunk(0, at: 0, state: .transcribed)], finals: [recap, nil])
        #expect(RecordingDayRetention.nextDeadline(unfinished, now: recap, audioDays: nil) == end.addingTimeInterval(7 * day))

        // Nothing to wake for once no audio is left.
        let gone = record(chunks: [chunk(0, at: 0, state: .transcribed, hasAudio: false)], finals: [recap])
        #expect(RecordingDayRetention.nextDeadline(gone, now: recap, audioDays: nil) == nil)
    }

    @Test func theDaysCountFromTheDatesOwnMidnightOnDSTDays() throws {
        // America/New_York: 2026-03-08 has 23 hours, 2026-11-01 has 25.
        let spring = record(day: "2026-03-08", zone: "America/New_York", chunks: [chunk(0, at: 0, state: .waiting)])
        let springEnd = try date("2026-03-09T04:00:00Z")
        #expect(RecordingDayRetention.nextDeadline(spring, now: springEnd, audioDays: nil) == springEnd.addingTimeInterval(7 * day))
        let fall = record(day: "2026-11-01", zone: "America/New_York", chunks: [chunk(0, at: 0, state: .waiting)])
        let fallEnd = try date("2026-11-02T05:00:00Z")
        #expect(RecordingDayRetention.nextDeadline(fall, now: fallEnd, audioDays: nil) == fallEnd.addingTimeInterval(7 * day))
        #expect(RecordingDayRetention.audioDue(fall, now: fallEnd.addingTimeInterval(7 * day - 1), audioDays: nil).isEmpty)
        #expect(RecordingDayRetention.audioDue(fall, now: fallEnd.addingTimeInterval(7 * day), audioDays: nil) == [0])
    }

    @Test func theLocalDayFollowsAudioDaysFromTheRecordingsStart() {
        #expect(!RecordingDayRetention.isExpired(start: anchor, now: anchor.addingTimeInterval(14 * day), days: 14))
        #expect(RecordingDayRetention.isExpired(start: anchor, now: anchor.addingTimeInterval(14 * day + 1), days: 14))
        #expect(!RecordingDayRetention.isExpired(start: anchor, now: anchor.addingTimeInterval(500 * day), days: 0))
    }

    @Test func theKeeperDeletesDueAudioAndPurgesDaysPastRecordingDays() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("day-keeper-\(UUID().uuidString)", isDirectory: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let library = RecordingLibrary(root: root)
        let lib = library
        let clock = OSAllocatedUnfairLock(initialState: anchor)
        let store = RecordingDayStore(root: root, isDeleted: { lib.isDeletedSnapshot($0) }, now: { clock.withLock { $0 } },
                                      writeDelay: 3600)
        let shanghai = try #require(TimeZone(identifier: "Asia/Shanghai"))
        /// A day recorded `ago` days before `anchor`, with two chunks, neither transcribed yet.
        func recorded(daysAgo ago: Double) throws -> UUID {
            let id = UUID()
            let start = anchor.addingTimeInterval(-ago * day)
            let name = RecordingDayCalendar.day(of: start, in: shanghai)
            for seq in 0..<2 {
                let header = RecordingDayHeader(recording: id, recordingStartedAt: start, tz: "Asia/Shanghai", day: name, seq: seq,
                                                startedAt: start.addingTimeInterval(Double(seq) * 10), duration: 10, level: -30)
                _ = try store.store(header: header, audio: Data([1, 2, 3]))
            }
            library.adoptDay(id: id, day: name, timeZone: "Asia/Shanghai", createdAt: start)
            return id
        }
        let old = try recorded(daysAgo: 15), recent = try recorded(daysAgo: 5), fresh = try recorded(daysAgo: 1)
        // The recent day's final recap was written 4 days ago: 3 days on, its audio goes, transcribed or not.
        await store.update(recent) { record in
            record.chunks[0]?.state = .transcribed
            record.chunks[0]?.sentences = []
            record.parts = [RecordingDayPart(hubID: record.id, part: 1, startedAt: record.startedAt,
                                             recapFinalAt: anchor.addingTimeInterval(-4 * day))]
        }
        var forgotten: [UUID] = []
        var window: Int? = nil
        let keeper = RecordingDayKeeper(store: store, library: library, audioDays: { window }, now: { anchor },
                                        forget: { forgotten.append($0); store.didRemove($0) })
        func audio(_ id: UUID, _ seq: Int) -> Bool { FileManager.default.fileExists(atPath: store.audioURL(id, seq: seq).path) }

        // Until the hub said how long it keeps recordings, no day goes for its age.
        await keeper.run(force: true)
        #expect(library.entry(old) != nil && forgotten.isEmpty)
        #expect(!audio(recent, 0) && !audio(recent, 1))
        #expect(await store.record(recent)?.chunks[0]?.hasAudio == false)
        #expect(await store.record(recent)?.chunks[0]?.state == .transcribed, "the text stays")
        #expect(!audio(old, 0) && !audio(old, 1), "past 7 days after its day")
        #expect(audio(fresh, 0) && audio(fresh, 1))

        window = 0
        await keeper.run(force: true)
        #expect(library.entry(old) != nil && forgotten.isEmpty, "forever never purges a day for its age")

        window = 14
        await keeper.run(force: true)
        #expect(forgotten == [old])
        #expect(library.entry(old) == nil && library.isDeleted(old), "tombstoned: a late chunk never brings it back")
        #expect(!FileManager.default.fileExists(atPath: store.folder(old).path))
        #expect(await store.record(old) == nil)
        #expect(library.entry(recent) != nil && library.entry(fresh) != nil)

        // A hub that keeps transcripts 3 days: the day that began 5 days ago goes whole; yesterday's keeps its audio.
        window = 3
        await keeper.run(force: true)
        #expect(forgotten == [old, recent])
        #expect(library.entry(fresh) != nil && audio(fresh, 0) && audio(fresh, 1))
    }

    /// A final recap time learned minutes after a run (a phone that was offline for days learns it late) can bring a
    /// deadline that passed already: the audio goes now, not at the next run hours later.
    @Test func aFinalRecapLearnedLateDeletesTheAudioAtOnce() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("day-keeper-\(UUID().uuidString)", isDirectory: true)
        defer { try? FileManager.default.removeItem(at: root) }
        let library = RecordingLibrary(root: root)
        let lib = library
        let store = RecordingDayStore(root: root, isDeleted: { lib.isDeletedSnapshot($0) }, now: { anchor }, writeDelay: 3600)
        let id = UUID()
        let start = anchor.addingTimeInterval(-4 * day)
        let header = RecordingDayHeader(recording: id, recordingStartedAt: start, tz: "Asia/Shanghai", day: "2027-01-11", seq: 0,
                                        startedAt: start, duration: 10, level: -30)
        _ = try store.store(header: header, audio: Data([1, 2, 3]))
        let keeper = RecordingDayKeeper(store: store, library: library, audioDays: { nil }, now: { anchor }, forget: { _ in },
                                        debounce: .zero)
        func audio() -> Bool { FileManager.default.fileExists(atPath: store.audioURL(id, seq: 0).path) }
        await keeper.run()
        #expect(audio(), "four days old, no final recap yet: kept")

        // Reconciliation brings its final recap, written four days ago, a moment after that run.
        await store.update(id) { record in
            record.parts = [RecordingDayPart(hubID: id, part: 1, startedAt: start, recapFinalAt: anchor.addingTimeInterval(-3.5 * day))]
        }
        keeper.schedule()
        for _ in 0..<50 { await Task.yield() }
        #expect(audio(), "an ordinary trigger within the hour runs nothing")
        keeper.deadlineChanged()
        for _ in 0..<500 where audio() { try await Task.sleep(for: .milliseconds(2)) }
        #expect(!audio(), "past three days after its final recap: gone at once")
    }
}
