import Foundation
import os
import Testing
@testable import Persona

private let anchor = Date(timeIntervalSince1970: 1_800_000_000) // 2027-01-15T08:00:00Z, 16:00 in Shanghai
/// The midnight that ends 2027-01-15 in Shanghai.
private let shanghaiMidnight = anchor.addingTimeInterval(8 * 3600)
private let day: TimeInterval = 86_400
private typealias Reason = RecordingDayGap.Reason
private typealias Sentence = RecordingSentence

/// Tombstones a test controls, read from the store's queue. `delete(_:when:)` deletes a day the first time the store asks
/// after a condition holds: a delete on the main actor landing between the store's check and the end of its write.
private final class Tombstones: Sendable {
    private let deleted = OSAllocatedUnfairLock(initialState: Set<UUID>())
    private let pending = OSAllocatedUnfairLock<[UUID: @Sendable () -> Bool]>(initialState: [:])

    func delete(_ id: UUID) { deleted.withLock { _ = $0.insert(id) } }

    func delete(_ id: UUID, when condition: @escaping @Sendable () -> Bool) { pending.withLock { $0[id] = condition } }

    func contains(_ id: UUID) -> Bool {
        if deleted.withLock({ $0.contains(id) }) { return true }
        guard let condition = pending.withLock({ $0[id] }), condition() else { return false }
        delete(id)
        return true
    }
}

/// What a callback was given, from any queue.
private final class Collected<Element: Sendable>: Sendable {
    private let items = OSAllocatedUnfairLock(initialState: [Element]())
    func append(_ item: Element) { items.withLock { $0.append(item) } }
    var all: [Element] { items.withLock { $0 } }
}

/// A temporary recordings folder with one day in it (recorded in Shanghai), and stores over it whose coalesced write never
/// comes during a test unless asked for (only `flush` writes `day.json`): what is on disk is exactly what the test made
/// happen. A second store over the same folder is the app after a relaunch.
private struct Fixture {
    let root = FileManager.default.temporaryDirectory.appendingPathComponent("day-store-\(UUID().uuidString)", isDirectory: true)
    let id = UUID()
    let tombstones = Tombstones()

    var folder: URL { root.appendingPathComponent(id.uuidString, isDirectory: true) }

    func store(now: Date = anchor.addingTimeInterval(30), writeDelay: TimeInterval = 3600,
               arrivalWriteDelay: TimeInterval = 3600) -> RecordingDayStore {
        let tombstones = tombstones
        return RecordingDayStore(root: root, isDeleted: { tombstones.contains($0) }, now: { now }, writeDelay: writeDelay,
                                 arrivalWriteDelay: arrivalWriteDelay)
    }

    /// A store whose clock the test moves.
    func store(clock: OSAllocatedUnfairLock<Date>) -> RecordingDayStore {
        let tombstones = tombstones
        return RecordingDayStore(root: root, isDeleted: { tombstones.contains($0) }, now: { clock.withLock { $0 } },
                                 writeDelay: 3600, arrivalWriteDelay: 3600)
    }

    func at(_ offset: Double) -> Date { anchor.addingTimeInterval(offset) }

    /// Chunk `seq`, `offset` seconds after `start`, as the watch's header says it.
    func header(_ seq: Int, at offset: Double = 0, duration: Double = 10, id: UUID? = nil, day: String = "2027-01-15",
                tz: String = "Asia/Shanghai", start: Date = anchor) -> RecordingDayHeader {
        RecordingDayHeader(recording: id ?? self.id, recordingStartedAt: start, tz: tz, day: day, seq: seq,
                           startedAt: start.addingTimeInterval(offset), duration: duration, level: -30)
    }

    /// A control event of the day, as the watch sends it.
    func envelope(_ event: RecordingDayEvent, id: UUID? = nil, day: String = "2027-01-15", tz: String = "Asia/Shanghai",
                  start: Date = anchor) -> RecordingDayEventEnvelope {
        RecordingDayEventEnvelope(recording: id ?? self.id, recordingStartedAt: start, tz: tz, day: day, event: event)
    }

    /// The transcriber's save of a chunk's text, with the positions it was given.
    func transcribe(_ store: RecordingDayStore, _ seq: Int, _ sentences: [Sentence], placed: [RecordingDayPosition]? = nil) async {
        await store.update(id) { record in
            record.chunks[seq]?.state = .transcribed
            record.chunks[seq]?.sentences = sentences
            record.chunks[seq]?.positions = placed
        }
    }

    func audio(_ seq: Int) -> Data { Data(repeating: UInt8(seq % 256), count: 64) }
    func file(_ name: String) -> URL { folder.appendingPathComponent(name) }
    func exists(_ name: String) -> Bool { FileManager.default.fileExists(atPath: file(name).path) }

    func dayJSON() throws -> RecordingDayRecord {
        try JSONDecoder().decode(RecordingDayRecord.self, from: Data(contentsOf: file("day.json")))
    }

    func sidecar(_ seq: Int) throws -> RecordingDaySidecar {
        try JSONDecoder().decode(RecordingDaySidecar.self, from: Data(contentsOf: file(String(format: "seg-%03d.json", seq))))
    }

    func modified(_ name: String) throws -> Date? {
        try FileManager.default.attributesOfItem(atPath: file(name).path)[.modificationDate] as? Date
    }

    /// Puts audio the phone fails to delete in place of chunk `seq`'s, as after a file system error: a folder holding a
    /// file it may not remove. Returns that folder, for `unlock`. (Assumes the tests don't run as root.)
    func undeletableAudio(_ seq: Int) throws -> URL {
        let audio = file(String(format: "seg-%03d.m4a", seq))
        let locked = audio.appendingPathComponent("locked", isDirectory: true)
        try? FileManager.default.removeItem(at: audio)
        try FileManager.default.createDirectory(at: locked, withIntermediateDirectories: true)
        try self.audio(seq).write(to: locked.appendingPathComponent("audio"))
        try FileManager.default.setAttributes([.posixPermissions: 0o555], ofItemAtPath: locked.path)
        return locked
    }

    func unlock(_ folder: URL) {
        try? FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: folder.path)
    }

    func cleanUp() { try? FileManager.default.removeItem(at: root) }
}

private func placed(_ positions: Int...) -> [RecordingDayPosition] { positions.map { RecordingDayPosition(part: 1, pos: $0) } }

/// The phone's day store (docs/assistant/day-recap.md「Watch to phone」): on disk before the acknowledgement, one copy per
/// seq, the control events applied in order and once, erase and delete before deduplication, a deadline for late audio,
/// the phone's own erases and deletes waiting for the watch, rebuilt from its folder after a crash. Nonisolated: the store
/// runs on WatchConnectivity's queue and its own, never on the main actor.
struct RecordingDayStoreTests {
    @Test func aChunkIsOnDiskBeforeStoreReturns() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        var changes = store.changes.makeAsyncIterator()

        #expect(try store.store(header: f.header(0), audio: f.audio(0)) == .stored)
        #expect(try Data(contentsOf: f.file("seg-000.m4a")) == f.audio(0))
        #expect(f.exists("seg-000.json"), "its sidecar is what the acknowledgement stands on")
        #expect(!f.exists("day.json"), "day.json waits for the coalesced write")
        let changed = await changes.next()
        #expect(changed == f.id)

        let record = try #require(await store.record(f.id))
        let chunk = try #require(record.chunks[0])
        #expect(chunk.hasAudio && chunk.state == .waiting && chunk.level == -30 && chunk.needsTranscription)
        #expect(chunk.receivedAt == anchor.addingTimeInterval(30), "the latency clock reads the store's time")
        #expect(record.day == "2027-01-15" && record.timeZone == "Asia/Shanghai" && record.startedAt == anchor,
                "the day, its zone and its clock as the watch named them")
        #expect(try f.sidecar(0) == RecordingDaySidecar(header: f.header(0), receivedAt: anchor.addingTimeInterval(30)))

        // By file: moved into the day before the call returns (the system deletes its copy right after).
        let incoming = f.root.appendingPathComponent("incoming.m4a")
        try f.audio(1).write(to: incoming)
        #expect(try store.store(header: f.header(1, at: 10), movingFile: incoming) == .stored)
        #expect(!FileManager.default.fileExists(atPath: incoming.path))
        #expect(try Data(contentsOf: f.file("seg-001.m4a")) == f.audio(1))

        // Not stored, so never acknowledged: a chunk without the audio it should carry, a header the watch can't write.
        #expect(throws: RecordingDayStore.Failure.missingAudio) { try store.store(header: f.header(2, at: 20), audio: Data()) }
        #expect(!f.exists("seg-002.json"))
        var broken = f.header(3, at: 30)
        broken.tz = "Mars/Olympus_Mons"
        #expect(throws: RecordingDayStore.Failure.invalidHeader) { try store.store(header: broken, audio: f.audio(3)) }
    }

    @Test func aChunkSentAgainIsStoredOnce() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        #expect(try store.store(header: f.header(0), audio: f.audio(0)) == .stored)
        // The acknowledgement went missing and the watch sends the chunk again, by message or by file.
        #expect(try store.store(header: f.header(0), audio: Data([9, 9])) == .duplicate)
        let incoming = f.root.appendingPathComponent("again.m4a")
        try Data([8]).write(to: incoming)
        #expect(try store.store(header: f.header(0), movingFile: incoming) == .duplicate)
        #expect(try Data(contentsOf: f.file("seg-000.m4a")) == f.audio(0), "the first copy stays")
        #expect(await store.record(f.id)?.chunks.count == 1)
    }

    @Test func eventsAreAppliedStrictlyInOrderAndOnce() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        // Event 2 comes first (its message overtook the user info carrying 0 and 1): kept on disk, not applied yet.
        #expect(try store.store(event: f.envelope(.started(2, at: f.at(60)))) == .held)
        #expect(f.exists("events.log"), "on disk before it is acknowledged")
        var record = try #require(await store.record(f.id), "a day can begin with an event")
        #expect(record.appliedEvents == 0 && record.events.keys.sorted() == [2] && record.pauses.isEmpty)

        #expect(try store.store(event: f.envelope(.started(0, at: f.at(0)))) == .applied)
        #expect(try store.store(event: f.envelope(.paused(1, at: f.at(20), reason: Reason.interrupted))) == .applied)
        record = try #require(await store.record(f.id))
        #expect(record.appliedEvents == 3 && record.events.isEmpty, "1 let 2 through")
        #expect(record.pauses == [RecordingDayPause(from: f.at(20), to: f.at(60), reason: Reason.interrupted)])

        // Sent again (the acknowledgement went missing): acknowledged again, applied once.
        #expect(try store.store(event: f.envelope(.paused(1, at: f.at(20), reason: Reason.interrupted))) == .duplicate)
        #expect(try store.store(event: f.envelope(.started(2, at: f.at(60)))) == .duplicate)

        // A meeting mark, a type this build doesn't know (applied as nothing, never holding back the rest), a pause, and
        // the end of the day while paused.
        #expect(try store.store(event: f.envelope(.mark(3, at: f.at(30), label: RecordingDayEvent.Label.meeting))) == .applied)
        #expect(try store.store(event: f.envelope(RecordingDayEvent(n: 4, type: RecordingDayEvent.Kind(rawValue: "focus"),
                                                                    at: f.at(40)))) == .applied)
        #expect(try store.store(event: f.envelope(.paused(5, at: f.at(100), reason: Reason.paused))) == .applied)
        #expect(try store.store(event: f.envelope(.closed(6, at: f.at(3600), seqCount: 2))) == .applied)
        record = try #require(await store.record(f.id))
        #expect(record.appliedEvents == 7)
        #expect(record.marks == [RecordingDayMark(at: f.at(30), label: RecordingDayEvent.Label.meeting)])
        #expect(record.pauses.last == RecordingDayPause(from: f.at(100), to: f.at(3600), reason: Reason.paused),
                "a pause still open ends with the recording")
        #expect(record.closedAt == f.at(3600) && record.seqCount == 2)
        #expect(record.missingSeqs == [0, 1], "no chunk yet: the watch still has them")

        // An event the watch could not have written is not stored, so never acknowledged.
        var broken = f.envelope(.started(7, at: f.at(10)))
        broken.tz = "Mars/Olympus_Mons"
        #expect(throws: RecordingDayStore.Failure.invalidEvent) { try store.store(event: broken) }
    }

    @Test func anEraseFromTheWatchTakesTheAudioAndTheTextAndWhatComesLater() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        for seq in 0..<3 { _ = try store.store(header: f.header(seq, at: Double(seq) * 10), audio: f.audio(seq)) }
        await f.transcribe(store, 0, [Sentence(start: 1, end: 4, text: "the export path"),
                                      Sentence(start: 8, end: 12, text: "drops entries")], placed: placed(0, 1))
        await f.transcribe(store, 1, [Sentence(start: 1, end: 3, text: "over four megabytes")])
        await f.transcribe(store, 2, [Sentence(start: 2, end: 5, text: "then lunch")], placed: placed(2))
        let hubID = f.id
        await store.update(f.id) { record in
            record.parts = [RecordingDayPart(hubID: hubID, part: 1, startedAt: anchor, created: true, next: 3, count: 3)]
        }

        // The user erased 10 s to 15 s on the watch, and the watch deleted seq 5 before the phone had it.
        let eraseID = UUID()
        #expect(try store.store(event: f.envelope(.erase(0, id: eraseID, from: f.at(10), to: f.at(15), seqs: [5]))) == .applied)
        let record = try #require(await store.record(f.id))
        #expect(record.erasures == [RecordingDayErasure(id: eraseID, from: f.at(10), to: f.at(15))])
        let erased = try #require(record.chunks[1])
        #expect(erased.erased && !erased.hasAudio && !erased.needsTranscription)
        #expect(erased.sentences == [], "text without a hub position just goes")
        #expect(!f.exists("seg-001.m4a") && f.exists("seg-000.m4a") && f.exists("seg-002.m4a"))
        // Chunk 0 stays, but its sentence running into the range loses its text and keeps its place on the hub.
        #expect(record.chunks[0]?.erased == false)
        #expect(record.chunks[0]?.sentences == [Sentence(start: 1, end: 4, text: "the export path"), Sentence(start: 8, end: 12, text: "")])
        #expect(record.chunks[0]?.positions == placed(0, 1) && record.positionsHeld(in: 1) == 3)
        #expect(record.parts[0].needsRepair && record.hasUnsentText, "the hub may hold the erased words: a PUT takes them out")
        #expect(record.chunks[2]?.sentences?.first?.text == "then lunch")
        #expect(record.erasedSeqs == [5] && record.missingSeqs == [3, 4], "5 is settled, not waited for")

        // Later: a chunk inside the range (sent before the watch erased it), and seq 5 itself: acknowledged and dropped.
        #expect(try store.store(header: f.header(4, at: 12, duration: 2), audio: f.audio(4)) == .settled)
        #expect(try store.store(header: f.header(5, at: 60), audio: f.audio(5)) == .settled)
        #expect(!f.exists("seg-004.m4a") && !f.exists("seg-005.m4a"))
        #expect(try f.sidecar(4).settled == .erased)
        let later = try #require(await store.record(f.id))
        #expect(later.chunks[5]?.erased == true && later.chunks[5]?.hasAudio == false && later.erasedSeqs.isEmpty)
        // One that starts where the range ends holds none of it.
        #expect(try store.store(header: f.header(3, at: 15), audio: f.audio(3)) == .stored)
    }

    @Test func anEditNeverBringsErasedTextBack() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        _ = try store.store(header: f.header(0), audio: f.audio(0))
        _ = try store.store(header: f.header(1, at: 10), audio: f.audio(1))
        _ = try store.store(event: f.envelope(.erase(0, id: UUID(), from: f.at(10), to: f.at(20), seqs: [])))

        // A transcription that started before the erase saves its text after it, from the record it read then.
        await store.update(f.id) { record in
            record.chunks[1]?.state = .transcribed
            record.chunks[1]?.sentences = [Sentence(start: 1, end: 3, text: "over four megabytes")]
            record.chunks[1]?.erased = false
            record.chunks[1]?.hasAudio = true
            record.chunks[0]?.state = .transcribed
            record.chunks[0]?.sentences = [Sentence(start: 2, end: 4, text: "kept"), Sentence(start: 9, end: 11, text: "spills over")]
            record.erasures = []
        }
        let record = try #require(await store.record(f.id))
        let chunk = try #require(record.chunks[1])
        #expect(chunk.erased && !chunk.hasAudio && chunk.sentences == [])
        #expect(record.chunks[0]?.sentences == [Sentence(start: 2, end: 4, text: "kept")])
        #expect(record.erasures.count == 1)

        // An edit that drops an erased chunk gets it back: its seq stays accounted for.
        await store.update(f.id) { $0.chunks[1] = nil }
        #expect(await store.record(f.id)?.chunks[1]?.erased == true)
    }

    @Test func anEraseIsAcknowledgedOnlyOnceItsAudioIsGone() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        _ = try store.store(header: f.header(0), audio: f.audio(0))
        await f.transcribe(store, 0, [Sentence(start: 1, end: 4, text: "the export path")])
        let locked = try f.undeletableAudio(0)
        defer { f.unlock(locked) }

        // Not acknowledged: the watch sends the event again. The text goes at once all the same, and the erase is on disk.
        let erase = f.envelope(.erase(0, id: UUID(), from: f.at(0), to: f.at(10), seqs: []))
        #expect(throws: (any Error).self) { try store.store(event: erase) }
        #expect(f.exists("seg-000.m4a"))
        let chunk = try #require(await store.record(f.id)?.chunks[0])
        #expect(chunk.erased && chunk.sentences == [] && chunk.hasAudio, "noted as still on disk")
        #expect(throws: (any Error).self, "sent again while the audio still can't go: still not acknowledged") {
            try store.store(event: erase)
        }
        #expect(await store.flush(f.id))
        #expect(try f.dayJSON().chunks[0]?.hasAudio == true, "a write doesn't pretend it is gone either")

        // Once it can go, the event sent again deletes it and is acknowledged.
        f.unlock(locked)
        #expect(try store.store(event: erase) == .duplicate)
        #expect(!f.exists("seg-000.m4a"))
        #expect(await store.record(f.id)?.chunks[0]?.hasAudio == false)
    }

    @Test func audioRetentionCouldNotDeleteStaysToBeDeleted() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        _ = try store.store(header: f.header(0), audio: f.audio(0))
        _ = try store.store(header: f.header(1, at: 10), audio: f.audio(1))
        let locked = try f.undeletableAudio(0)
        defer { f.unlock(locked) }

        // Past the deadline: chunk 1's audio goes, chunk 0's can't, and is still noted as there.
        #expect(!(await store.removeAudio(f.id, seqs: [0, 1])))
        let record = try #require(await store.record(f.id))
        #expect(record.chunks[0]?.hasAudio == true && f.exists("seg-000.m4a"), "the next retention pass tries again")
        #expect(record.chunks[1]?.hasAudio == false && !f.exists("seg-001.m4a"))
        #expect(try f.dayJSON().chunks[0]?.hasAudio == true, "written so, for a pass after a relaunch")

        // The next pass, once it can go.
        f.unlock(locked)
        #expect(await store.removeAudio(f.id, seqs: [0]))
        #expect(await store.record(f.id)?.chunks[0]?.hasAudio == false && !f.exists("seg-000.m4a"))
        #expect(try f.dayJSON().chunks[0]?.hasAudio == false)
        // Audio already gone is gone.
        #expect(await store.removeAudio(f.id, seqs: [0, 1]))
    }

    @Test func lossesNeverDropHeldAudio() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        _ = try store.store(header: f.header(2, at: 20), audio: f.audio(2))
        // The watch reports 0…2 lost, but the phone holds 2: its copy is that chunk (seqs are never reused).
        #expect(try store.store(event: f.envelope(.lost(0, seqs: 0...2))) == .applied)
        var record = try #require(await store.record(f.id))
        #expect(record.lost == [0, 1] && record.chunks[2]?.hasAudio == true && f.exists("seg-002.m4a"))
        #expect(record.losses == [RecordingDayLoss(fromSeq: 0, toSeq: 2, from: nil, to: nil)])
        // One turns up after all (a file transfer still on its way): more of the day, kept.
        #expect(try store.store(header: f.header(1, at: 10), audio: f.audio(1)) == .stored)
        record = try #require(await store.record(f.id))
        #expect(record.lost == [0] && record.missingSeqs.isEmpty)
        // Without the watch's times the lost time runs from the recording's start to the chunk after.
        #expect(RecordingDaySummary(record: record, hubOffered: true, now: f.at(60)).gaps
            == [RecordingDayGap(from: 0, to: 10, reason: Reason.lost)])
    }

    @Test func aChunkAfterTheDaysAudioDeadlineIsSettledAsLost() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        // The earliest of 7 days after the day, `audio_days` after it, and 3 days after its final recap.
        var record = RecordingDayRecord(header: f.header(0))
        #expect(record.dayEnd == shanghaiMidnight)
        #expect(record.audioDeadline(audioDays: nil) == shanghaiMidnight.addingTimeInterval(7 * day))
        #expect(record.audioDeadline(audioDays: 0) == shanghaiMidnight.addingTimeInterval(7 * day), "forever keeps the 7-day maximum")
        #expect(record.audioDeadline(audioDays: 2) == shanghaiMidnight.addingTimeInterval(2 * day))
        record.parts = [RecordingDayPart(hubID: f.id, part: 1, startedAt: anchor, recapFinalAt: shanghaiMidnight.addingTimeInterval(3600))]
        #expect(record.audioDeadline(audioDays: 14) == shanghaiMidnight.addingTimeInterval(3600 + 3 * day))
        // A part without its final recap yet holds the 3-day clock back: the audio serves every part.
        record.parts.append(RecordingDayPart(hubID: UUID(), part: 2, startedAt: anchor))
        #expect(record.audioDeadline(audioDays: 14) == shanghaiMidnight.addingTimeInterval(7 * day))

        // Within the hub's 2 days the chunk is kept; a minute past them it is acknowledged, discarded and lost.
        let early = f.store(now: shanghaiMidnight.addingTimeInterval(2 * day - 60))
        early.useAudioDays(2)
        #expect(try early.store(header: f.header(0), audio: f.audio(0)) == .stored)
        let late = f.store(now: shanghaiMidnight.addingTimeInterval(2 * day + 60))
        late.useAudioDays(2)
        #expect(try late.store(header: f.header(1, at: 10), audio: f.audio(1)) == .settled)
        #expect(!f.exists("seg-001.m4a") && f.exists("seg-001.json"))
        #expect(try f.sidecar(1).settled == .lost)
        let lost = try #require(await late.record(f.id)?.chunks[1])
        #expect(lost.lost && !lost.hasAudio && !lost.needsTranscription)
        #expect(try late.store(header: f.header(1, at: 10), audio: f.audio(1)) == .duplicate)
        let gaps = RecordingDaySummary(record: try #require(await late.record(f.id)), hubOffered: true, now: f.at(60)).gaps
        #expect(gaps == [RecordingDayGap(from: 10, to: 20, reason: Reason.lost)])
    }

    @Test func aDeletedDayIsRefusedButAcknowledged() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        f.tombstones.delete(f.id)
        #expect(try store.store(header: f.header(0), audio: f.audio(0)) == .refused)
        #expect(try store.store(event: f.envelope(.started(0, at: f.at(0)))) == .refused)
        #expect(!(await store.requestErase(f.id, from: f.at(0), to: f.at(10))))
        #expect(!FileManager.default.fileExists(atPath: f.folder.path), "nothing of it is kept")
        #expect(await store.record(f.id) == nil)
    }

    @Test func aDeleteLandingDuringTheWriteWins() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        // The user deletes the day after the store checked its tombstone, while it writes the chunk.
        let sidecar = f.file("seg-000.json")
        f.tombstones.delete(f.id, when: { FileManager.default.fileExists(atPath: sidecar.path) })
        #expect(try store.store(header: f.header(0), audio: f.audio(0)) == .refused)
        #expect(!FileManager.default.fileExists(atPath: f.folder.path), "what it wrote is gone again")
        #expect(await store.record(f.id) == nil)
        #expect(await store.ids().isEmpty)
    }

    @Test func aPendingWriteNeverBringsADeletedDayBack() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        _ = try store.store(header: f.header(0), audio: f.audio(0))
        // RecordingLibrary.remove: the tombstone first, then the folder; day.json is still waiting to be written.
        f.tombstones.delete(f.id)
        try FileManager.default.removeItem(at: f.folder)
        await store.flush(f.id)
        await store.flushAll()
        #expect(!FileManager.default.fileExists(atPath: f.folder.path))
        // A chunk or an event the watch sends late is acknowledged and dropped.
        #expect(try store.store(header: f.header(1, at: 10), audio: f.audio(1)) == .refused)
        #expect(try store.store(event: f.envelope(.paused(0, at: f.at(20), reason: Reason.paused))) == .refused)
        #expect(!FileManager.default.fileExists(atPath: f.folder.path))
        let relaunched = f.store()
        #expect(await relaunched.recoverOnLaunch().isEmpty)
        #expect(await relaunched.ids().isEmpty)
    }

    @Test func aDayIsRebuiltFromItsFolderAfterACrash() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let first = f.store()
        let eraseID = UUID(), quiet = UUID()
        _ = try first.store(event: f.envelope(.started(0, at: f.at(0))))
        _ = try first.store(header: f.header(0), audio: f.audio(0))
        _ = try first.store(header: f.header(1, at: 10), audio: f.audio(1))
        _ = try first.store(event: f.envelope(.paused(1, at: f.at(20), reason: Reason.interrupted)))
        _ = try first.store(event: f.envelope(.started(2, at: f.at(60))))
        _ = try first.store(header: f.header(2, at: 60), audio: f.audio(2))
        _ = try first.store(event: f.envelope(.lost(3, seqs: 3...3, from: f.at(70), to: f.at(80))))
        _ = try first.store(event: f.envelope(.erase(4, id: eraseID, from: f.at(12), to: f.at(15), seqs: [])))
        _ = try first.store(header: f.header(4, at: 80), audio: f.audio(4))
        // `closed` overtook event 5, which never came.
        _ = try first.store(event: f.envelope(.closed(6, at: f.at(3600), seqCount: 5)))
        // Audio whose sidecar was never written (the app ended between the two): never acknowledged.
        try Data([1]).write(to: f.file("seg-009.m4a"))
        // A day the watch has only sent an event for.
        _ = try first.store(event: f.envelope(.started(0, at: f.at(0)), id: quiet))
        #expect(!f.exists("day.json"), "the app ended before the first coalesced write")

        // Relaunched a day later.
        let second = f.store(now: anchor.addingTimeInterval(day))
        #expect(await second.recoverOnLaunch() == [f.id, quiet].sorted { $0.uuidString < $1.uuidString })
        #expect(f.exists("day.json"), "written before recovery returns")
        #expect(!f.exists("seg-009.m4a"), "the watch sends it again")
        let record = try #require(await second.record(f.id))
        #expect(record.chunks.keys.sorted() == [0, 1, 2, 4])
        #expect(record.chunks[0]?.hasAudio == true)
        #expect(record.chunks[0]?.receivedAt == anchor.addingTimeInterval(30), "the first arrival's time, not the relaunch's")
        #expect(record.chunks[1]?.erased == true && record.chunks[1]?.hasAudio == false && !f.exists("seg-001.m4a"))
        #expect(record.pauses == [RecordingDayPause(from: f.at(20), to: f.at(60), reason: Reason.interrupted)])
        #expect(record.lost == [3] && record.erasures == [RecordingDayErasure(id: eraseID, from: f.at(12), to: f.at(15))])
        #expect(record.appliedEvents == 5 && record.events.keys.sorted() == [6] && record.closedAt == nil, "closed waits for 5")
        #expect(record.startedAt == anchor && record.day == "2027-01-15" && record.timeZone == "Asia/Shanghai")
        #expect(await second.record(quiet)?.appliedEvents == 1)
        #expect(await second.ids().contains(quiet))
        #expect(await second.recoverOnLaunch().isEmpty, "a current day.json is left as it is")
    }

    @Test func aDayLeftWithoutDayJSONIsListedBeforeRecovery() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        _ = try f.store().store(header: f.header(0), audio: f.audio(0))
        #expect(!f.exists("day.json"), "the app ended before the first coalesced write")

        // Relaunched for a background task that lists the days before the launch's recovery ran.
        let relaunched = f.store()
        #expect(await relaunched.ids() == [f.id])
        #expect(f.exists("day.json"))
        #expect(await relaunched.recoverOnLaunch() == [f.id], "still reported, so the library gets its row")
        #expect(await relaunched.recoverOnLaunch().isEmpty)
    }

    @Test func aRemovedDayIsAnnouncedAndNeverWrittenAgain() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        _ = try store.store(header: f.header(0), audio: f.audio(0))
        var changes = store.changes.makeAsyncIterator()
        // RecordingLibrary.remove: the tombstone, then the folder; then the store is told.
        f.tombstones.delete(f.id)
        try FileManager.default.removeItem(at: f.folder)
        store.didRemove(f.id)
        let changed = await changes.next()
        #expect(changed == f.id, "followers drop it at once")
        #expect(await store.record(f.id) == nil)
        #expect(await store.flush(f.id) == false)
        #expect(!FileManager.default.fileExists(atPath: f.folder.path))
    }

    @Test func whatCameAfterDayJSONIsMergedIn() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let first = f.store()
        _ = try first.store(header: f.header(0), audio: f.audio(0))
        _ = try first.store(header: f.header(1, at: 10), audio: f.audio(1))
        await f.transcribe(first, 1, [Sentence(start: 0, end: 2, text: "hello")])
        #expect(await first.flush(f.id))
        // After that write and before the next: one more chunk, and an erase of chunk 0 whose audio deletion the crash
        // interrupted.
        _ = try first.store(header: f.header(2, at: 20), audio: f.audio(2))
        _ = try first.store(event: f.envelope(.erase(0, id: UUID(), from: f.at(0), to: f.at(5), seqs: [])))
        try f.audio(0).write(to: f.file("seg-000.m4a"))

        let second = f.store()
        #expect(await second.recoverOnLaunch() == [f.id])
        let record = try #require(await second.record(f.id))
        #expect(record.chunks[0]?.erased == true && record.chunks[0]?.hasAudio == false && !f.exists("seg-000.m4a"))
        #expect(record.chunks[1]?.sentences?.first?.text == "hello", "what only day.json held is kept")
        #expect(record.chunks[2]?.hasAudio == true && record.appliedEvents == 1)
    }

    @Test func recoveryFollowsTheJournalNotTheClock() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let first = f.store()
        _ = try first.store(header: f.header(0), audio: f.audio(0))
        await f.transcribe(first, 0, [Sentence(start: 1, end: 4, text: "the export path")])
        #expect(await first.flush(f.id))
        #expect(!f.exists("journal.log"), "day.json holds everything written so far")

        // The clock is set back a minute. Then an erase of chunk 0, a new chunk and a loss are stored and acknowledged,
        // and the app ends before the next write of day.json: their files are dated before it.
        _ = try first.store(event: f.envelope(.erase(0, id: UUID(), from: f.at(0), to: f.at(10), seqs: [])))
        _ = try first.store(header: f.header(1, at: 10), audio: f.audio(1))
        _ = try first.store(event: f.envelope(.lost(1, seqs: 5...5)))
        let backdated = try #require(try f.modified("day.json")).addingTimeInterval(-60)
        for name in ["seg-001.json", "events.log"] {
            try FileManager.default.setAttributes([.modificationDate: backdated], ofItemAtPath: f.file(name).path)
        }

        let second = f.store()
        #expect(await second.recoverOnLaunch() == [f.id])
        let record = try #require(await second.record(f.id))
        let erased = try #require(record.chunks[0])
        #expect(erased.erased && erased.sentences == [] && !erased.hasAudio, "an acknowledged erase is never undone")
        #expect(record.chunks[1]?.hasAudio == true)
        #expect(record.lost == [5])
        #expect(try f.dayJSON() == record, "written before recovery returns")
        #expect(!f.exists("journal.log"))
        #expect(await f.store().recoverOnLaunch().isEmpty, "nothing left behind")
    }

    @Test func aLineACrashCutShortNeverTakesTheNextOneWithIt() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let first = f.store()
        _ = try first.store(event: f.envelope(.started(0, at: f.at(0))))
        #expect(await first.flush(f.id))
        // The app ended while it stored event 1, so it was never acknowledged: its journal line and its line in
        // events.log were cut short.
        try Data("eve".utf8).write(to: f.file("journal.log"))
        let log = try FileHandle(forWritingTo: f.file("events.log"))
        try log.seekToEnd()
        try log.write(contentsOf: Data(#"{"recording":"\#(f.id.uuidString)","ev"#.utf8))
        try log.close()

        // Relaunched, the watch sends event 1 again and it is acknowledged. The app ends again before day.json is
        // written, with the clock set back, so only the journal says events.log has more than day.json.
        let second = f.store()
        #expect(try second.store(event: f.envelope(.paused(1, at: f.at(20), reason: Reason.interrupted))) == .applied)
        let backdated = try #require(try f.modified("day.json")).addingTimeInterval(-60)
        try FileManager.default.setAttributes([.modificationDate: backdated], ofItemAtPath: f.file("events.log").path)

        let third = f.store()
        #expect(await third.recoverOnLaunch() == [f.id])
        let record = try #require(await third.record(f.id))
        #expect(record.appliedEvents == 2, "an acknowledged event is never lost")
        #expect(record.pauses == [RecordingDayPause(from: f.at(20), to: nil, reason: Reason.interrupted)])
    }

    @Test func anEraseOnThePhoneIsKeptAndAskedOfTheWatchUntilItConfirms() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        var requested = store.watchRequestChanges.makeAsyncIterator()
        for seq in 0..<3 { _ = try store.store(header: f.header(seq, at: Double(seq) * 10), audio: f.audio(seq)) }
        await f.transcribe(store, 1, [Sentence(start: 1, end: 3, text: "the salary figures")])

        let eraseID = UUID()
        #expect(await store.requestErase(f.id, from: f.at(10), to: f.at(15), requestID: eraseID))
        let record = try #require(await store.record(f.id))
        #expect(record.erasures == [RecordingDayErasure(id: eraseID, from: f.at(10), to: f.at(15))])
        #expect(record.chunks[1]?.erased == true && record.chunks[1]?.sentences == [] && !f.exists("seg-001.m4a"))
        #expect(f.exists("erases.json"))
        #expect(try f.dayJSON() == record, "on disk when it returns")
        #expect(await requested.next() != nil, "the receiver sends it at once")
        let pending = await store.watchRequests()
        #expect(pending.erases == [RecordingDayEraseRequest(recording: f.id, id: eraseID, from: f.at(10), to: f.at(15))])
        // Asked again (a retry): one erase, one request.
        #expect(await store.requestErase(f.id, from: f.at(10), to: f.at(15), requestID: eraseID))
        #expect(await store.watchRequests() == pending)

        // After a relaunch the request still waits, and the erase holds even when day.json is gone.
        try FileManager.default.removeItem(at: f.file("day.json"))
        let relaunched = f.store()
        #expect(await relaunched.watchRequests() == pending)
        _ = await relaunched.recoverOnLaunch()
        let rebuilt = try #require(await relaunched.record(f.id))
        #expect(rebuilt.erasures.count == 1 && rebuilt.chunks[1]?.erased == true)

        // The watch erased seq 3 before the phone had it: settled, and the request is done.
        await relaunched.watchErased(RecordingDayErased(recording: f.id, erased: eraseID, seqs: [3]))
        #expect(await relaunched.record(f.id)?.erasedSeqs == [3])
        #expect(await relaunched.watchRequests() == RecordingDayWatchRequests.Pending())
        #expect(try relaunched.store(header: f.header(3, at: 30), audio: f.audio(3)) == .settled, "still on its way: dropped")

        // Nothing to erase: no request.
        #expect(!(await relaunched.requestErase(UUID(), from: f.at(0), to: f.at(10))))
        #expect(!(await relaunched.requestErase(f.id, from: f.at(20), to: f.at(10))))
        #expect(await relaunched.watchRequests().erases.isEmpty)
    }

    @Test func aDeleteIsAskedOfTheWatchUntilItConfirms() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        _ = try store.store(header: f.header(0), audio: f.audio(0))
        #expect(await store.requestErase(f.id, from: f.at(0), to: f.at(5)))
        // Deleted here: the library's tombstone and removal, then the watch's copy (twice: two paths reached it).
        f.tombstones.delete(f.id)
        try FileManager.default.removeItem(at: f.folder)
        store.didRemove(f.id)
        await store.requestWatchDelete(f.id)
        await store.requestWatchDelete(f.id)
        let pending = await store.watchRequests()
        #expect(pending.deletes == [RecordingDayDelete(recording: f.id)])
        #expect(pending.erases.isEmpty, "the delete takes the erase's place")
        #expect(await f.store().watchRequests() == pending, "kept outside the day's folder, so it outlives it")

        await store.watchDeleted(RecordingDayDeleted(recording: f.id))
        #expect(await store.watchRequests() == RecordingDayWatchRequests.Pending())

        // A watch that never answers: given up once it can hold nothing of the day.
        await store.requestWatchDelete(UUID())
        let lifetime = RecordingDayWatchRequests.lifetime
        #expect(await f.store(now: anchor.addingTimeInterval(30 + lifetime - 1)).watchRequests().deletes.count == 1)
        #expect(await f.store(now: anchor.addingTimeInterval(30 + lifetime)).watchRequests().deletes.isEmpty)
    }

    @Test func aRequestThatCouldNotBeWrittenIsWrittenLater() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        _ = try store.store(header: f.header(0), audio: f.audio(0))
        // day-requests.json can't be written: a folder stands in its place, as after a file system error.
        let file = f.root.appendingPathComponent("day-requests.json", isDirectory: true)
        try FileManager.default.createDirectory(at: file, withIntermediateDirectories: true)

        let eraseID = UUID(), deleted = UUID()
        #expect(!(await store.requestErase(f.id, from: f.at(0), to: f.at(5), requestID: eraseID)), "not durable, and said so")
        #expect(await store.record(f.id)?.erasures.count == 1 && !f.exists("seg-000.m4a"), "the erase itself is done here")
        #expect(!(await store.requestWatchDelete(deleted)))
        let pending = await store.watchRequests()
        #expect(pending.erases.map(\.erase.id) == [eraseID] && pending.deletes == [RecordingDayDelete(recording: deleted)],
                "still sent while the app runs")
        #expect(!(await store.requestErase(f.id, from: f.at(0), to: f.at(5), requestID: eraseID)),
                "asked again: held here already, and still not taken for being on disk")
        #expect(await f.store().watchRequests() == RecordingDayWatchRequests.Pending())

        // Once the file can be written, the next look at the requests writes them.
        try FileManager.default.removeItem(at: file)
        #expect(await store.watchRequests() == pending)
        #expect(await f.store().watchRequests() == pending, "on disk for the next launch")
        #expect(await store.requestErase(f.id, from: f.at(0), to: f.at(5), requestID: eraseID))
        #expect(await store.requestWatchDelete(deleted))
    }

    /// The delete's request to the watch is written after the library's tombstone, on the store's queue: an app that
    /// ends in between, or a write that fails, would leave the watch recording into a day the phone drops. The tombstone
    /// is what lasts, so every chunk or event refused for it asks the watch again (at most once a minute).
    @Test func aChunkOfADeletedDayAsksTheWatchToDeleteIt() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let clock = OSAllocatedUnfairLock(initialState: anchor)
        let store = f.store(clock: clock)
        var requested = store.watchRequestChanges.makeAsyncIterator()
        _ = try store.store(header: f.header(0), audio: f.audio(0))
        // Deleted here, and the app ended before the request to the watch was written.
        f.tombstones.delete(f.id)
        try FileManager.default.removeItem(at: f.folder)
        #expect(await store.watchRequests().deletes.isEmpty)

        #expect(try store.store(header: f.header(1, at: 10), audio: f.audio(1)) == .refused)
        #expect(await store.watchRequests().deletes == [RecordingDayDelete(recording: f.id)])
        #expect(await requested.next() != nil, "the receiver sends it at once")
        #expect(await f.store().watchRequests().deletes == [RecordingDayDelete(recording: f.id)],
                "on disk before the chunk is acknowledged")
        #expect(!FileManager.default.fileExists(atPath: f.folder.path), "and nothing of the day is kept")

        // Confirmed; a chunk still on its way a moment later doesn't ask again at once.
        await store.watchDeleted(RecordingDayDeleted(recording: f.id))
        clock.withLock { $0 = anchor.addingTimeInterval(RecordingDayStore.refusalInterval - 1) }
        #expect(try store.store(event: f.envelope(.started(0, at: f.at(0)))) == .refused)
        #expect(await store.watchRequests().deletes.isEmpty)
        // The watch is still at it a minute on: asked again.
        clock.withLock { $0 = anchor.addingTimeInterval(RecordingDayStore.refusalInterval) }
        #expect(try store.store(event: f.envelope(.paused(1, at: f.at(20), reason: Reason.paused))) == .refused)
        #expect(await store.watchRequests().deletes == [RecordingDayDelete(recording: f.id)])
    }

    /// "Erase the last 10 minutes" just after midnight, while the new day the watch began at midnight has nothing on the
    /// phone yet (its chunks batched in Low Power, or the phone out of reach): that day gets the erase when its first
    /// chunk or event arrives, here and on the watch, also after a relaunch.
    @Test func anEraseOverMidnightReachesADayThatTurnsUpLater() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store(now: shanghaiMidnight.addingTimeInterval(240))
        _ = try store.store(header: f.header(0, at: 8 * 3600 - 600, duration: 600), audio: f.audio(0))
        let from = shanghaiMidnight.addingTimeInterval(-360), to = shanghaiMidnight.addingTimeInterval(240)
        let eraseID = UUID()
        #expect(await store.requestErase(from: from, to: to, requestID: eraseID) == [f.id], "the only day held now")

        // Today's first chunks arrive: the one inside the erase is dropped, the one after it kept.
        let today = UUID()
        func todays(_ seq: Int, at offset: Double) -> RecordingDayHeader {
            f.header(seq, at: offset, duration: 60, id: today, day: "2027-01-16", start: shanghaiMidnight)
        }
        #expect(try store.store(header: todays(3, at: 180), audio: f.audio(3)) == .settled)
        #expect(try store.store(header: todays(4, at: 240), audio: f.audio(4)) == .stored)
        #expect(try store.store(header: todays(0, at: 0), audio: f.audio(0)) == .settled, "erase comes before anything is kept")
        let record = try #require(await store.record(today))
        #expect(record.erasures == [RecordingDayErasure(id: eraseID, from: from, to: to)], "the same erase, its id included")
        #expect(record.chunks[0]?.erased == true && record.chunks[3]?.erased == true && record.chunks[4]?.erased == false)
        #expect(!FileManager.default.fileExists(atPath: store.audioURL(today, seq: 0).path))
        let asked = await store.watchRequests().erases
        #expect(Set(asked.map(\.recording)) == [f.id, today] && asked.allSatisfy { $0.erase.id == eraseID },
                "the watch erases its copy of the new day too")

        // After a relaunch, a day whose first word is an event; one that began after the range is left alone.
        let relaunched = f.store(now: shanghaiMidnight.addingTimeInterval(300))
        let another = UUID(), later = UUID()
        #expect(try relaunched.store(event: f.envelope(.started(0, at: shanghaiMidnight.addingTimeInterval(60)), id: another,
                                                       day: "2027-01-16", start: shanghaiMidnight.addingTimeInterval(60))) == .applied)
        #expect(await relaunched.record(another)?.erasures.map(\.id) == [eraseID])
        #expect(try relaunched.store(event: f.envelope(.started(0, at: to), id: later, day: "2027-01-16", start: to)) == .applied)
        #expect(await relaunched.record(later)?.erasures.isEmpty == true)
        #expect(await relaunched.watchRequests().erases.map(\.recording).contains(another))
    }

    /// A crash after the range was kept but before every day held had it: the launch erases them.
    @Test func aRangeEraseACrashCutShortIsFinishedAtLaunch() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let before = f.store()
        _ = try before.store(header: f.header(0), audio: f.audio(0))
        await before.flushAll()
        let eraseID = UUID()
        let range = RecordingDayWatchRequests.RangeErase(id: eraseID, from: f.at(0), to: f.at(5),
                                                    until: anchor.addingTimeInterval(RecordingDayWatchRequests.lifetime))
        let requests = RecordingDayWatchRequests(ranges: [range])
        try JSONEncoder().encode(requests).write(to: f.root.appendingPathComponent("day-requests.json"))
        let relaunched = f.store()
        _ = await relaunched.recoverOnLaunch()
        #expect(await relaunched.record(f.id)?.erasures.map(\.id) == [eraseID])
        #expect(!f.exists("seg-000.m4a"))
        #expect(await relaunched.watchRequests().erases.map(\.erase.id) == [eraseID])
        // Done: the next launch has nothing more to do for it.
        let saved = try JSONDecoder().decode(RecordingDayWatchRequests.self,
                                             from: Data(contentsOf: f.root.appendingPathComponent("day-requests.json")))
        #expect(saved.ranges?.first?.done == true)
    }

    /// The request to the watch is taken as soon as erases.json holds the erase: a day.json that can't be written (or
    /// an app that ends before the request is) never leaves the watch's copy unerased.
    @Test func anEraseIsAskedOfTheWatchWhateverHappensToTheDaysWrite() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        _ = try store.store(header: f.header(0), audio: f.audio(0))
        // day.json can't be written: a folder with a file in it stands in its place.
        let blocker = f.file("day.json")
        try FileManager.default.createDirectory(at: blocker, withIntermediateDirectories: true)
        try Data([1]).write(to: blocker.appendingPathComponent("x"))
        let eraseID = UUID()
        #expect(!(await store.requestErase(f.id, from: f.at(0), to: f.at(5), requestID: eraseID)), "the day isn't on disk")
        let request = RecordingDayEraseRequest(recording: f.id, id: eraseID, from: f.at(0), to: f.at(5))
        #expect(await store.watchRequests().erases == [request], "still asked of the watch")
        #expect(await f.store().watchRequests().erases == [request], "and on disk for the next launch")

        // An app that ended between erases.json and the request: the launch asks it again from erases.json.
        try FileManager.default.removeItem(at: blocker)
        try FileManager.default.removeItem(at: f.root.appendingPathComponent("day-requests.json"))
        let relaunched = f.store()
        _ = await relaunched.recoverOnLaunch()
        #expect(await relaunched.record(f.id)?.erasures.map(\.id) == [eraseID])
        #expect(await relaunched.watchRequests().erases == [request])
    }

    @Test func anEraseWhoseRequestWasntWrittenIsAskedAgainAtTheNextLaunch() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        _ = try store.store(header: f.header(0), audio: f.audio(0))
        // day-requests.json can't be written: a folder with a file in it stands in its place.
        let blocker = f.root.appendingPathComponent("day-requests.json")
        try FileManager.default.createDirectory(at: blocker, withIntermediateDirectories: true)
        try Data([1]).write(to: blocker.appendingPathComponent("x"))
        let eraseID = UUID()
        #expect(!(await store.requestErase(f.id, from: f.at(0), to: f.at(5), requestID: eraseID)), "the request isn't on disk")
        let request = RecordingDayEraseRequest(recording: f.id, id: eraseID, from: f.at(0), to: f.at(5))
        #expect(await store.watchRequests().erases == [request], "still asked of the watch while the app runs")
        #expect(try f.dayJSON().erasures.map(\.id) == [eraseID], "the erase itself is on disk")

        // The app ended before the request was written: the launch asks the watch again, once.
        try FileManager.default.removeItem(at: blocker)
        let relaunched = f.store()
        _ = await relaunched.recoverOnLaunch()
        #expect(await relaunched.watchRequests().erases == [request])
        #expect(await f.store().watchRequests().erases == [request], "and on disk now")
        await relaunched.watchErased(RecordingDayErased(recording: f.id, erased: eraseID, seqs: [] as [Int]))
        let later = f.store()
        _ = await later.recoverOnLaunch()
        #expect(await later.watchRequests().erases.isEmpty, "a confirmed erase isn't asked again")
    }

    @Test func dayJSONIsWrittenCoalescedAndOnFlush() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        for seq in 0..<3 { _ = try store.store(header: f.header(seq, at: Double(seq) * 10), audio: f.audio(seq)) }
        #expect(!f.exists("day.json"), "three chunks, no write yet")
        #expect(await store.flush(f.id), "on disk when flush returns")
        let held = await store.record(f.id)
        #expect(try f.dayJSON() == held)
        #expect(try f.dayJSON().chunks.count == 3)

        // An edit that changes nothing writes nothing.
        let written = try f.modified("day.json")
        await store.update(f.id) { _ in }
        await store.flush(f.id)
        #expect(try f.modified("day.json") == written)

        // A real one goes with the next write.
        await store.update(f.id) { $0.chunks[0]?.state = .skipped }
        #expect(try f.dayJSON().chunks[0]?.state == .waiting)
        await store.flush(f.id)
        #expect(try f.dayJSON().chunks[0]?.state == .skipped)

        // A memory warning lets go of what is on disk as it is, never of an edit that isn't written yet.
        let onDisk = try f.dayJSON()
        store.trimMemory()
        #expect(await store.record(f.id) == onDisk)
        await store.update(f.id) { $0.chunks[1]?.state = .skipped }
        store.trimMemory()
        #expect(await store.record(f.id)?.chunks[1]?.state == .skipped)
        #expect(try f.dayJSON().chunks[1]?.state == .waiting)
    }

    @Test func theCoalescedWriteComesByItself() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store(writeDelay: 0.3, arrivalWriteDelay: 0.3)
        for seq in 0..<3 { _ = try store.store(header: f.header(seq, at: Double(seq) * 10), audio: f.audio(seq)) }
        // No flush: the burst reaches day.json by the store's own write a moment later.
        let deadline = Date.now.addingTimeInterval(10)
        while (try? f.dayJSON().chunks.count) != 3, Date.now < deadline { try await Task.sleep(for: .milliseconds(20)) }
        #expect(try f.dayJSON().chunks.count == 3)
    }

    /// A chunk every ten seconds all day: what its sidecar already holds doesn't rewrite the whole day.json each time
    /// (a recovery merges sidecars through the journal anyway); it rides along with the day's next write of any kind.
    @Test func anArrivalWaitsForTheDaysNextWrite() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store(writeDelay: 0.05)
        for seq in 0..<3 { _ = try store.store(header: f.header(seq, at: Double(seq) * 10), audio: f.audio(seq)) }
        try await Task.sleep(for: .milliseconds(400))
        #expect(!f.exists("day.json"), "arrivals alone don't start the coalesced write")
        #expect(f.exists("journal.log") && f.exists("seg-002.json"), "the sidecars and the journal hold them")
        // Also after a relaunch before any write: rebuilt from the sidecars.
        #expect(await f.store().record(f.id)?.chunks.count == 3)

        // An edit (the transcriber's text) is written soon, and the arrivals with it.
        await f.transcribe(store, 0, [Sentence(start: 1, end: 2, text: "hello")])
        let deadline = Date.now.addingTimeInterval(10)
        while (try? f.dayJSON().chunks.count) != 3, Date.now < deadline { try await Task.sleep(for: .milliseconds(20)) }
        #expect(try f.dayJSON().chunks.count == 3)
        #expect(try f.dayJSON().chunks[0]?.state == .transcribed)

        // An erase from the watch takes text and audio: written soon too, not left for later.
        _ = try store.store(header: f.header(3, at: 30), audio: f.audio(3))
        _ = try store.store(event: f.envelope(.erase(0, id: UUID(), from: f.at(30), to: f.at(35), seqs: [])))
        let erased = Date.now.addingTimeInterval(10)
        while (try? f.dayJSON().chunks[3]?.erased) != true, Date.now < erased { try await Task.sleep(for: .milliseconds(20)) }
        #expect(try f.dayJSON().chunks[3]?.erased == true)
    }

    @Test func aSummaryCountsTheDayAndNamesItsGaps() throws {
        let id = UUID()
        func at(_ offset: Double) -> Date { anchor.addingTimeInterval(offset) }
        func chunk(_ seq: Int, _ offset: Double, _ state: RecordingDayChunk.State = .transcribed) -> RecordingDayChunk {
            RecordingDayChunk(seq: seq, startedAt: at(offset), duration: 10, receivedAt: anchor, state: state, hasAudio: true)
        }
        var record = RecordingDayRecord(id: id, day: "2027-01-15", timeZone: "Asia/Shanghai", startedAt: anchor)
        // Seq 2 hasn't arrived, 6 came after the deadline, 8 the watch lost; 7 gets erased.
        var late = chunk(6, 130)
        late.settle(.lost)
        for c in [chunk(0, 0), chunk(1, 10), chunk(3, 40, .waiting), chunk(4, 50, .skipped), chunk(5, 120, .failed), late,
                  chunk(7, 140), chunk(9, 160)] {
            record.put(c, recordingStartedAt: anchor)
        }
        let events: [RecordingDayEvent] = [
            .started(0, at: at(0)),
            .paused(1, at: at(60), reason: Reason.interrupted),
            .started(2, at: at(120)),
            .erase(3, id: UUID(), from: at(140), to: at(150), seqs: []),
            .lost(4, seqs: 8...8, from: at(150), to: at(160)),
            .paused(5, at: at(170), reason: Reason.paused),
            .closed(6, at: at(3600), seqCount: 10),
        ]
        for event in events {
            let held = record.hold(event)
            #expect(held)
        }
        let gone = record.applyHeldEvents()
        #expect(gone == [7], "the erase reached chunk 7's audio")

        var summary = RecordingDaySummary(record: record, hubOffered: false, now: at(4000))
        #expect(summary.firstAt == anchor && summary.lastAt == at(170) && summary.closedAt == at(3600))
        #expect(summary.recordedSeconds == 60 && summary.chunks == 6, "erased and lost chunks left out")
        #expect(summary.waiting == 1 && summary.skipped == 1 && summary.failed == 1)
        #expect(summary.transcribedTo == at(20), "up to the chunk still waiting")
        #expect(summary.gaps == [RecordingDayGap(from: 20, to: 40, reason: Reason.pending),
                                 RecordingDayGap(from: 60, to: 120, reason: Reason.interrupted),
                                 RecordingDayGap(from: 120, to: 130, reason: Reason.untranscribed),
                                 RecordingDayGap(from: 130, to: 140, reason: Reason.lost),
                                 RecordingDayGap(from: 140, to: 150, reason: Reason.erased),
                                 RecordingDayGap(from: 150, to: 160, reason: Reason.lost),
                                 RecordingDayGap(from: 170, to: 3600, reason: Reason.paused)])
        #expect(summary.sync == .notOffered && !summary.complete)

        // Seq 2 reported lost after all, chunk 3 transcribed: everything is in, nothing on the hub yet.
        _ = record.hold(.lost(7, seqs: 2...2))
        _ = record.applyHeldEvents()
        record.chunks[3]?.state = .transcribed
        summary = RecordingDaySummary(record: record, hubOffered: true, now: at(4000))
        #expect(summary.gaps.first == RecordingDayGap(from: 20, to: 40, reason: Reason.lost))
        #expect(summary.transcribedTo == at(170))
        #expect(summary.sync == .pending && !summary.complete)

        // The upload placed every sentence (positions 0…3 of part 1) and the hub took them all.
        for (pos, seq) in [0, 1, 3, 9].enumerated() {
            record.chunks[seq]?.sentences = [Sentence(start: 1, end: 3, text: "words \(seq)")]
            record.chunks[seq]?.positions = placed(pos)
        }
        record.parts = [RecordingDayPart(hubID: id, part: 1, startedAt: anchor, created: true, next: 4, count: 4)]
        summary = RecordingDaySummary(record: record, hubOffered: true, now: at(4000))
        #expect(summary.sync == .synced && !summary.complete, "chunk 5 was never transcribed: its words never reached the hub")
        // A later try transcribed it (nothing intelligible was said).
        record.chunks[5]?.state = .transcribed
        record.chunks[5]?.sentences = []
        summary = RecordingDaySummary(record: record, hubOffered: true, now: at(4000))
        #expect(summary.sync == .synced && summary.complete && summary.hubIDs == [id])

        // Chunk 1 is erased after its text reached the hub: its place stays with no text, and the part needs a PUT.
        record.erase(id: UUID(), from: at(10), to: at(20), seqs: [])
        #expect(record.chunks[1]?.sentences == [Sentence(start: 1, end: 3, text: "")] && record.parts[0].needsRepair)
        summary = RecordingDaySummary(record: record, hubOffered: true, now: at(4000))
        #expect(summary.sync == .pending && !summary.complete)
        // The repair PUT was decided on and stored.
        record.parts[0].needsRepair = false
        #expect(RecordingDaySummary(record: record, hubOffered: true, now: at(4000)).sync == .synced)

        record.parts[0].problem = "invalid segments"
        #expect(RecordingDaySummary(record: record, hubOffered: true, now: at(4000)).sync == .problem)
        #expect(RecordingDaySummary(record: record, hubOffered: true, now: at(4000)).problem == "invalid segments")
        record.problem = "too_large"
        #expect(RecordingDaySummary(record: record, hubOffered: true, now: at(4000)).problem == "too_large", "the day's own first")
        record.problem = nil
        record.parts[0].problem = nil
        #expect(RecordingDaySummary(record: record, hubOffered: true, now: at(4000)).problem == nil)
        record.unsupportedUntilHello = true
        #expect(RecordingDaySummary(record: record, hubOffered: true, now: at(4000)).sync == .unsupported)
    }

    @Test func aPauseStillOpenRunsToNow() throws {
        var record = RecordingDayRecord(id: UUID(), day: "2027-01-15", timeZone: "Asia/Shanghai", startedAt: anchor)
        _ = record.hold(.paused(0, at: anchor.addingTimeInterval(600), reason: Reason.interrupted))
        _ = record.applyHeldEvents()
        let gaps = RecordingDayGap.gaps(in: record, since: anchor, now: anchor.addingTimeInterval(900))
        #expect(gaps == [RecordingDayGap(from: 600, to: 900, reason: Reason.interrupted)])
    }

    @Test func eachKindOfMissingTimeKeepsItsOwnGap() throws {
        func at(_ offset: Double) -> Date { anchor.addingTimeInterval(offset) }
        func chunk(_ seq: Int, _ offset: Double) -> RecordingDayChunk {
            RecordingDayChunk(seq: seq, startedAt: at(offset), duration: 10, receivedAt: anchor, state: .transcribed, hasAudio: true)
        }
        var record = RecordingDayRecord(id: UUID(), day: "2027-01-15", timeZone: "Asia/Shanghai", startedAt: anchor)
        for c in [chunk(0, 0), chunk(3, 30), chunk(6, 60), chunk(8, 80)] { record.put(c, recordingStartedAt: anchor) }
        let events: [RecordingDayEvent] = [
            // 1 is gone with its times and 2 is still on the watch; 4 is on the watch and 5 is gone, its reported times
            // running into chunk 6; 7 is gone without times.
            .lost(0, seqs: 1...1, from: at(10), to: at(20)),
            .lost(1, seqs: 5...5, from: at(50), to: at(65)),
            .lost(2, seqs: 7...7),
            // Two erases half a second apart: the time between them was never erased.
            .erase(3, id: UUID(), from: at(100), to: at(110), seqs: []),
            .erase(4, id: UUID(), from: at(110.5), to: at(120), seqs: []),
        ]
        for event in events { _ = record.hold(event) }
        _ = record.applyHeldEvents()

        #expect(RecordingDayGap.gaps(in: record, since: anchor, now: at(200)) == [
            RecordingDayGap(from: 10, to: 20, reason: Reason.lost),
            RecordingDayGap(from: 20, to: 30, reason: Reason.pending),
            RecordingDayGap(from: 40, to: 50, reason: Reason.pending),
            RecordingDayGap(from: 50, to: 60, reason: Reason.lost),
            RecordingDayGap(from: 70, to: 80, reason: Reason.lost),
            RecordingDayGap(from: 100, to: 110, reason: Reason.erased),
            RecordingDayGap(from: 110.5, to: 120, reason: Reason.erased),
        ])
    }
}

/// What the Recordings page reads.
@MainActor struct RecordingDaySummariesTests {
    @Test func todayIsEachDaysOwnDateAndADeletedDayLeaves() async throws {
        // 23:30 on 2026-11-01 in New York, the night its clocks fell back (a 25-hour day): already the 2nd in UTC.
        let now = try Date.ISO8601FormatStyle().parse("2026-11-02T04:30:00Z")
        let morning = try Date.ISO8601FormatStyle().parse("2026-11-01T13:00:00Z") // 08:00 in New York
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        let newYork = f.id, utc = UUID()
        _ = try store.store(header: f.header(0, day: "2026-11-01", tz: "America/New_York", start: morning), audio: f.audio(0))
        _ = try store.store(header: f.header(0, id: utc, day: "2026-11-01", tz: "UTC", start: morning), audio: f.audio(0))

        let summaries = RecordingDaySummaries(store: store, hubOffered: { true }, now: { now })
        await summaries.refresh([newYork, utc])
        #expect(summaries.byID.count == 2)
        #expect(summaries.today?.id == newYork)
        #expect(summaries.byID[newYork]?.sync == .pending, "not on the hub yet")
        // The watch's recording is today's only on its own date.
        summaries.receive(watch: RecordingDayWatchState(recording: utc, day: "2026-11-01", state: .recording))
        #expect(summaries.watch?.recording == utc)
        #expect(summaries.today?.id == newYork)

        // A deleted day leaves with its next pass.
        f.tombstones.delete(newYork)
        await summaries.refresh([newYork])
        #expect(summaries.byID[newYork] == nil && summaries.today == nil)
    }

    @Test func todayHoldsThroughTheShortSpringDay() async throws {
        // 2026-03-08 in New York has 23 hours (02:00 skips to 03:00). At 23:59 local it is 03:59 UTC on the 9th.
        let lateEvening = try Date.ISO8601FormatStyle().parse("2026-03-09T03:59:00Z")
        let midnight = try Date.ISO8601FormatStyle().parse("2026-03-09T04:00:00Z")
        let morning = try Date.ISO8601FormatStyle().parse("2026-03-08T13:00:00Z") // 09:00 EDT
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        _ = try store.store(header: f.header(0, day: "2026-03-08", tz: "America/New_York", start: morning), audio: f.audio(0))

        let evening = RecordingDaySummaries(store: store, hubOffered: { false }, now: { lateEvening })
        await evening.refresh([f.id])
        #expect(evening.today?.id == f.id)
        #expect(evening.today?.sync == .notOffered && evening.today?.firstAt == morning)
        #expect(evening.today?.span?.end == midnight, "the day's span ends at its own midnight")
        let next = RecordingDaySummaries(store: store, hubOffered: { false }, now: { midnight })
        await next.refresh([f.id])
        #expect(next.byID[f.id] != nil && next.today == nil, "a past day stays listed, not as today")
    }

    @Test func anEraseOverMidnightReachesBothDays() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let before = f.store()
        let tomorrow = UUID(), earlier = UUID()
        _ = try before.store(header: f.header(0, at: 8 * 3600 - 600), audio: f.audio(0))
        _ = try before.store(event: f.envelope(.closed(0, at: shanghaiMidnight, seqCount: 1)))
        _ = try before.store(header: f.header(0, id: earlier, day: "2027-01-14", start: anchor.addingTimeInterval(-day)), audio: f.audio(0))
        await before.flushAll()
        // After a relaunch the days before are only on disk; the new day's first chunk came a moment ago, and no day has
        // been summarized yet.
        let store = f.store()
        _ = try store.store(header: f.header(0, id: tomorrow, day: "2027-01-16", start: shanghaiMidnight), audio: f.audio(0))
        let summaries = RecordingDaySummaries(store: store, hubOffered: { true }, now: { shanghaiMidnight })
        #expect(summaries.byID.isEmpty)

        // "Erase the last 10 minutes" five minutes after midnight.
        let from = shanghaiMidnight.addingTimeInterval(-300), to = shanghaiMidnight.addingTimeInterval(300)
        #expect(await summaries.requestErase(from: from, to: to) == [f.id, tomorrow])
        let first = await store.record(f.id)?.erasures.first?.id
        let second = await store.record(tomorrow)?.erasures.first?.id
        #expect(first != nil && first == second, "one erase, in both day recordings")
        #expect(await store.record(earlier)?.erasures.isEmpty == true)
        let asked: [String] = await store.watchRequests().erases.map(\.recording.uuidString).sorted()
        #expect(asked == [f.id, tomorrow].map(\.uuidString).sorted())
    }

    @Test func followingTheStoreReadsOnlyWhatChangesUntilAllIsAsked() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        let earlier = UUID()
        _ = try store.store(header: f.header(0, id: earlier), audio: f.audio(0))
        let offered = OSAllocatedUnfairLock(initialState: false)
        let summaries = RecordingDaySummaries(store: store, hubOffered: { offered.withLock { $0 } }, now: { anchor },
                                              coalesce: .milliseconds(10))

        // Launched in the background for a chunk: that day is summarized, and no other day is read for it (P9).
        summaries.start()
        _ = try store.store(header: f.header(0), audio: f.audio(0))
        #expect(await eventually { summaries.byID[f.id] != nil })
        #expect(Set(summaries.byID.keys) == [f.id])

        // In front: every day.
        summaries.readAll()
        #expect(await eventually { summaries.byID.count == 2 })
        #expect(summaries.byID[earlier]?.sync == .notOffered)

        // The hub's flag changed: the days shown are summarized again.
        offered.withLock { $0 = true }
        summaries.refreshAll()
        #expect(await eventually { summaries.byID.values.allSatisfy { $0.sync == .pending } })
    }
}

/// The consent sheet's agreement and what the watch is told.
@MainActor struct RecordingDayConsentTests {
    @Test func theAgreementIsKeptWithItsVersionAndToldToTheWatch() async throws {
        let suite = "recording-day-consent-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store(now: shanghaiMidnight.addingTimeInterval(day + 60))
        let consent = RecordingDayConsent(defaults: defaults, now: { anchor })
        #expect(!consent.agreed && consent.agreedVersion == nil && consent.agreedAt == nil)

        var sent: [RecordingDayCapabilities] = []
        let capabilities = RecordingDayPhoneCapabilities(consent: consent, store: store, hub: false, audioDays: nil,
                                                         send: { sent.append($0) })
        capabilities.start()
        #expect(sent == [RecordingDayCapabilities(consent: false, hub: false, keepDays: 7)])
        #expect(sent.last?.context[RecordingDayContract.phoneKey] as? Int == 0, "the phone takes them, the user hasn't agreed")

        consent.agree()
        #expect(consent.agreed && consent.agreedVersion == RecordingDayConsent.version && consent.agreedAt == anchor)
        #expect(defaults.integer(forKey: RecordingDayConsent.versionKey) == RecordingDayConsent.version)
        #expect(sent.last?.context[RecordingDayContract.phoneKey] as? Int == RecordingDayContract.version)
        consent.agree()
        #expect(sent.count == 2, "agreeing again changes nothing")
        #expect(RecordingDayConsent(defaults: defaults).agreed, "kept for the next launch")

        // The hub flag and the hub's audio retention, each pushed when it changes: keep days are min(7, audio_days).
        capabilities.hubChanged(offered: true)
        capabilities.hubChanged(offered: true)
        capabilities.audioDaysChanged(3)
        capabilities.audioDaysChanged(30)
        #expect(Array(sent.dropFirst(2)) == [RecordingDayCapabilities(consent: true, hub: true, keepDays: 7),
                                             RecordingDayCapabilities(consent: true, hub: true, keepDays: 3),
                                             RecordingDayCapabilities(consent: true, hub: true, keepDays: 7)])

        // The store's audio deadline counts the same retention: a day after a one-day retention ran out, a chunk is lost.
        capabilities.audioDaysChanged(1)
        #expect(try store.store(header: f.header(0), audio: f.audio(0)) == .settled)

        // An agreement a newer build stored counts; one to an older sheet would ask again.
        defaults.set(RecordingDayConsent.version + 1, forKey: RecordingDayConsent.versionKey)
        #expect(RecordingDayConsent(defaults: defaults).agreed)
        defaults.set(RecordingDayConsent.version - 1, forKey: RecordingDayConsent.versionKey)
        let upgraded = RecordingDayConsent(defaults: defaults)
        #expect(!upgraded.agreed)
        // A version bump asks again, but the phone still tells the watch it takes day recordings: 0, never nothing.
        // A recording that started under the older sheet then goes on under it (`WatchPhoneCapabilities.takesDays`).
        let afterUpgrade = RecordingDayPhoneCapabilities(consent: upgraded, store: store, hub: false, audioDays: nil, send: { _ in })
        #expect(afterUpgrade.current.context[RecordingDayContract.phoneKey] as? Int == 0)
    }
}

/// Waits up to ten seconds for `condition` (a coalesced pass that runs by itself); whether it came to hold.
@MainActor private func eventually(_ condition: () -> Bool) async -> Bool {
    let deadline = Date.now.addingTimeInterval(10)
    while !condition() {
        guard Date.now < deadline else { return false }
        try? await Task.sleep(for: .milliseconds(10))
    }
    return true
}

/// What WatchConnectivity's queue runs for a chunk or an event: acknowledged only once stored, together where it can be.
/// Nonisolated, like that queue.
struct RecordingDayIntakeTests {
    @Test func theIntakeAcknowledgesOnlyWhatIsStoredAndTogether() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let sent = Collected<RecordingDayAck>()
        let arrivals = Collected<UUID>(), refusals = Collected<UUID>()
        let intake = RecordingDayIntake(store: f.store(), sendAck: { sent.append($0) }, arrived: { arrivals.append($0) },
                                        refused: { refusals.append($0) }, ackDelay: 3600)
        func file(_ seq: Int) throws -> URL {
            let url = f.root.appendingPathComponent("incoming-\(seq).m4a")
            try FileManager.default.createDirectory(at: f.root, withIntermediateDirectories: true)
            try f.audio(seq).write(to: url)
            return url
        }

        // The live path: the reply is the acknowledgement, once the chunk is on disk.
        let reply = intake.receive(frame: f.header(0).frame(audio: f.audio(0)))
        #expect(RecordingDayAck(data: reply) == RecordingDayAck(recording: f.id, acks: [[0, 0]]))
        #expect(f.exists("seg-000.json"))
        // Not an envelope, or a chunk without its audio: no acknowledgement, so the watch sends it again.
        #expect(intake.receive(frame: Data([0, 0, 0, 9, 1])).isEmpty)
        #expect(intake.receive(frame: f.header(5, at: 50).frame(audio: Data())).isEmpty)

        // By file: acknowledgements gather, and go with that recording's next event acknowledgement.
        #expect(intake.receive(file: try file(1), header: f.header(1, at: 10)))
        #expect(intake.receive(file: try file(2), header: f.header(2, at: 20)))
        #expect(sent.all.isEmpty, "the batch waits")
        let started = f.envelope(.started(0, at: f.at(0)))
        #expect(intake.receive(event: started) == RecordingDayAck(recording: f.id, seqs: [1, 2], events: [0]))
        #expect(intake.receive(event: started) == RecordingDayAck(recording: f.id, events: [0]), "sent again: acknowledged again")
        // Or with its next live chunk's reply.
        #expect(intake.receive(file: try file(3), header: f.header(3, at: 30)))
        #expect(RecordingDayAck(data: intake.receive(frame: f.header(4, at: 40).frame(audio: f.audio(4))))?.seqs == [3, 4])
        // Or on their own.
        #expect(intake.receive(file: try file(6), header: f.header(6, at: 60)))
        intake.flushAcks()
        #expect(sent.all == [RecordingDayAck(recording: f.id, seqs: [6])])
        #expect(arrivals.all == Array(repeating: f.id, count: 7), "every change reaches the main actor's side")

        // A chunk or an event of a deleted day is still acknowledged, so the watch lets go of it; nothing follows.
        f.tombstones.delete(f.id)
        #expect(RecordingDayAck(data: intake.receive(frame: f.header(7, at: 70).frame(audio: f.audio(7))))?.seqs == [7])
        #expect(intake.receive(event: f.envelope(.paused(1, at: f.at(80), reason: Reason.paused)))?.eventNumbers == [1])
        #expect(arrivals.all.count == 7)
        #expect(refusals.all == [f.id, f.id], "the receiver sends the watch the delete again")
    }
}

/// The receiver's main-actor side: each day's library row, the background catch-up (C9), and the requests to the watch.
/// Serialized: its tests swap the process-wide catch-up seam (`RecordingDayReceiver.requestCatchUp`).
@MainActor @Suite(.serialized) struct RecordingDayReceiverTests {
    private func makeReceiver(_ f: Fixture, _ store: RecordingDayStore, library: RecordingLibrary, now: @escaping () -> Date,
                          erases: @escaping (RecordingDayEraseRequest) -> Void = { _ in },
                          deletes: @escaping (RecordingDayDelete) -> Void = { _ in },
                          retain: @escaping () -> Void = {}) -> RecordingDayReceiver {
        let consent = RecordingDayConsent(defaults: UserDefaults(suiteName: "recording-day-receiver-\(f.id.uuidString)") ?? .standard)
        let capabilities = RecordingDayPhoneCapabilities(consent: consent, store: store, hub: false, audioDays: nil, send: { _ in })
        return RecordingDayReceiver(store: store, library: library, summaries: RecordingDaySummaries(store: store, hubOffered: { false }),
                                    capabilities: capabilities, sendErase: erases, sendDelete: deletes, retain: retain, now: now)
    }

    /// A phone only ever woken by WatchConnectivity never comes to the front: arriving chunks ask for the audio's
    /// retention pass (the keeper's own gate keeps it hourly).
    @Test func arrivingChunksAskForRetentionAtMostHourly() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        var clock = anchor
        var passes = 0
        let receiver = makeReceiver(f, store, library: RecordingLibrary(root: f.root), now: { clock }, retain: { passes += 1 })
        let original = RecordingDayReceiver.requestCatchUp
        RecordingDayReceiver.requestCatchUp = {}
        defer { RecordingDayReceiver.requestCatchUp = original }
        func settle(_ want: Int) async throws {
            for _ in 0..<200 where passes != want { try await Task.sleep(for: .milliseconds(10)) }
        }
        _ = try store.store(header: f.header(0), audio: f.audio(0))
        receiver.arrived(f.id)
        try await settle(1)
        #expect(passes == 1)
        clock = anchor.addingTimeInterval(RecordingDayReceiver.retentionInterval - 1)
        receiver.arrived(f.id)
        try await Task.sleep(for: .milliseconds(100))
        #expect(passes == 1)
        clock = anchor.addingTimeInterval(RecordingDayReceiver.retentionInterval)
        receiver.arrived(f.id)
        try await settle(2)
        #expect(passes == 2)
        // A new launch within the hour (the in-memory gate starts over) still skips it: the store remembers the pass.
        clock = anchor.addingTimeInterval(RecordingDayReceiver.retentionInterval + 60)
        let relaunched = makeReceiver(f, store, library: RecordingLibrary(root: f.root), now: { clock }, retain: { passes += 1 })
        relaunched.arrived(f.id)
        try await Task.sleep(for: .milliseconds(100))
        #expect(passes == 2)
        await receiver.rowsAdopted()
        await relaunched.rowsAdopted()
    }

    /// Chunks of a day deleted here change nothing, so they never pass through `arrived`: the delete still waiting goes
    /// to the watch again from them.
    @Test func aRefusedChunkSendsTheWaitingDeleteAgain() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        var clock = anchor
        var deletes: [RecordingDayDelete] = []
        let receiver = makeReceiver(f, store, library: RecordingLibrary(root: f.root), now: { clock },
                                    deletes: { deletes.append($0) })
        await store.requestWatchDelete(f.id)
        receiver.refused(f.id)
        await receiver.requestsSent()
        #expect(deletes == [RecordingDayDelete(recording: f.id)])
        // A chunk every ten seconds asks at most once a minute.
        clock = anchor.addingTimeInterval(10)
        receiver.refused(f.id)
        await receiver.requestsSent()
        #expect(deletes.count == 1)
        clock = anchor.addingTimeInterval(RecordingDayReceiver.resendInterval)
        receiver.refused(f.id)
        await receiver.requestsSent()
        #expect(deletes.count == 2)
    }

    @Test func aDayGetsItsRowAndAsksForTheCatchUpAtMostEveryQuarterHour() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        let library = RecordingLibrary(root: f.root)
        var clock = anchor
        let receiver = makeReceiver(f, store, library: library, now: { clock })
        var requests = 0
        let original = RecordingDayReceiver.requestCatchUp
        RecordingDayReceiver.requestCatchUp = { requests += 1 }
        defer { RecordingDayReceiver.requestCatchUp = original }

        _ = try store.store(header: f.header(0), audio: f.audio(0))
        receiver.arrived(f.id)
        await receiver.rowsAdopted()
        let row = try #require(library.entry(f.id))
        #expect(row.isDay && row.source == .watch && row.active && row.segments.isEmpty)
        #expect(row.day == "2027-01-15" && row.timeZone == "Asia/Shanghai" && row.createdAt == anchor)
        #expect(requests == 1)

        // A chunk every ten seconds asks once per quarter hour.
        clock = anchor.addingTimeInterval(14 * 60)
        _ = try store.store(header: f.header(1, at: 10), audio: f.audio(1))
        receiver.arrived(f.id)
        #expect(requests == 1)
        clock = anchor.addingTimeInterval(15 * 60)
        _ = try store.store(event: f.envelope(.closed(0, at: f.at(20), seqCount: 2)))
        receiver.arrived(f.id)
        await receiver.rowsAdopted()
        #expect(requests == 2)
        #expect(library.entry(f.id)?.active == false, "the watch's `closed` closes the row")

        // A day the user deleted never gets its row back from a late chunk.
        try library.remove(f.id)
        receiver.arrived(f.id)
        await receiver.rowsAdopted()
        #expect(library.entry(f.id) == nil)
    }

    @Test func requestsGoAgainUntilTheWatchAnswers() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        let store = f.store()
        var clock = anchor
        var erases: [RecordingDayEraseRequest] = [], deletes: [RecordingDayDelete] = []
        let receiver = makeReceiver(f, store, library: RecordingLibrary(root: f.root), now: { clock },
                                    erases: { erases.append($0) }, deletes: { deletes.append($0) })
        let original = RecordingDayReceiver.requestCatchUp
        RecordingDayReceiver.requestCatchUp = {}
        defer { RecordingDayReceiver.requestCatchUp = original }
        _ = try store.store(header: f.header(0), audio: f.audio(0))
        let eraseID = UUID()
        #expect(await store.requestErase(f.id, from: f.at(0), to: f.at(5), requestID: eraseID))
        let request = RecordingDayEraseRequest(recording: f.id, id: eraseID, from: f.at(0), to: f.at(5))

        receiver.resendRequests()
        await receiver.requestsSent()
        #expect(erases == [request])
        // The watch shows up (a chunk arrives): asked again, at most once a minute.
        receiver.arrived(f.id)
        await receiver.requestsSent()
        clock = anchor.addingTimeInterval(30)
        receiver.arrived(f.id)
        await receiver.requestsSent()
        #expect(erases == [request, request])
        receiver.sessionActivated()
        await receiver.requestsSent()
        #expect(erases.count == 3)

        // Answered: settled, and never sent again.
        await receiver.watchErased(RecordingDayErased(recording: f.id, erased: eraseID, seqs: []))
        receiver.resendRequests()
        await receiver.requestsSent()
        #expect(erases.count == 3)

        let deleted = UUID()
        await store.requestWatchDelete(deleted)
        receiver.resendRequests()
        await receiver.requestsSent()
        #expect(deletes == [RecordingDayDelete(recording: deleted)])
        await receiver.watchDeleted(RecordingDayDeleted(recording: deleted))
        receiver.resendRequests()
        await receiver.requestsSent()
        #expect(deletes.count == 1)
    }
}
