import Foundation
import Testing
@testable import Persona

/// The hub's recording sync (docs/protocol/protocol.md「Recording sync」) as far as the phone needs it: stores the latest revision,
/// remembers deletes (a later PUT gets 410), can be down (503).
private final class FakeRecordingHub: @unchecked Sendable {
    private let lock = NSLock()
    private var _stored: [String: Int64] = [:]
    private var _deleted: Set<String> = []
    private var _calls: [String] = []
    var down = false
    var days = 14
    var audioDays: Int?
    var textDays: Int?
    var refusal: APIError?
    var writesOnly = false

    var stored: [String: Int64] { lock.withLock { _stored } }
    var calls: [String] { lock.withLock { _calls } }
    func forget(_ id: UUID) { lock.withLock { _ = _stored.removeValue(forKey: id.uuidString.lowercased()) } }
    func deleteThere(_ id: UUID) { lock.withLock { _deleted.insert(id.uuidString.lowercased()); _ = _stored.removeValue(forKey: id.uuidString.lowercased()) } }

    func answer(_ r: LocalRequest) throws -> Data {
        try lock.withLock {
            _calls.append("\(r.method) \(r.path)")
            if down { throw APIError(status: 503, message: "知识库暂时不可用") }
            if let refusal, !writesOnly || r.method != "GET" { throw refusal }
            if r.method == "GET", r.path == "/api/v1/recordings" {
                let list = _stored.map { ["id": $0.key, "revision": $0.value, "complete": true, "started_at": "2026-11-05T00:00:00Z"] as [String: Any] }
                var answer: [String: Any] = ["days": days, "recordings": list, "deleted": Array(_deleted)]
                if let audioDays { answer["audio_days"] = audioDays }
                if let textDays { answer["text_days"] = textDays }
                return try JSONSerialization.data(withJSONObject: answer)
            }
            let id = String(r.path.split(separator: "/").last ?? "")
            if r.method == "DELETE" {
                _deleted.insert(id)
                let had = _stored.removeValue(forKey: id) != nil
                return Data("{\"deleted\":\(had)}".utf8)
            }
            if _deleted.contains(id) { throw APIError(status: 410, message: "deleted", fields: ["code": "recording_deleted"]) }
            let body = try JSONSerialization.jsonObject(with: r.body ?? Data()) as? [String: Any] ?? [:]
            let revision = (body["revision"] as? NSNumber)?.int64Value ?? 0
            if let have = _stored[id], have > revision {
                return Data("{\"id\":\"\(id)\",\"revision\":\(have),\"status\":\"stale\"}".utf8)
            }
            _stored[id] = revision
            return Data("{\"id\":\"\(id)\",\"revision\":\(revision),\"status\":\"stored\"}".utf8)
        }
    }
}

@MainActor struct RecordingSyncTests {
    private func api(_ hub: FakeRecordingHub) -> APIClient {
        APIClient(config: ServerConfig(url: URL(string: "https://hub.invalid")!, token: "t"), transport: { try hub.answer($0) })
    }

    private func setup(now: Date = .now) -> (RecordingLibrary, RecordingSync, FakeRecordingHub, URL) {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        let lib = RecordingLibrary(root: root)
        let hub = FakeRecordingHub()
        let api = api(hub)
        return (lib, RecordingSync(library: lib, api: { api }, now: { now }, audioDaysChanged: { _ in }), hub, root)
    }

    private func transcribed(_ lib: RecordingLibrary, createdAt: Date = .now, text: String = "下周检查进度。") throws -> RecordingEntry {
        var entry = RecordingEntry(id: UUID(), createdAt: createdAt, source: .phone, active: false, expectedSegments: 1)
        entry.segments = [RecordingSegment(index: 0, start: 0, duration: 30, sentences: [RecordingSentence(start: 1, end: 3, text: text)])]
        try lib.save(entry)
        return lib.entry(entry.id)!
    }

    @Test func uploadsOnceUntilItChanges() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        let entry = try transcribed(lib)
        await sync.syncNow()
        #expect(hub.stored[entry.id.uuidString.lowercased()] == entry.revision)
        #expect(sync.status(entry) == .synced)
        let puts = hub.calls.filter { $0.hasPrefix("PUT") }.count
        await sync.syncNow()
        #expect(hub.calls.filter { $0.hasPrefix("PUT") }.count == puts, "nothing new: nothing sent")
        // A new revision (another language, a title) goes again; the saved copy's revision only moves forward.
        lib.change(entry.id) { $0.title = "周会" }
        let changed = lib.entry(entry.id)!
        #expect(changed.revision > entry.revision)
        #expect(sync.status(changed) == .pending)
        await sync.syncNow()
        #expect(hub.stored[entry.id.uuidString.lowercased()] == changed.revision)
        // What was sent survives a relaunch.
        let again = RecordingSync(library: lib, api: { nil })
        #expect(again.status(changed) == .synced)
    }

    @Test func offlineQueuesAndRetries() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        hub.down = true
        let entry = try transcribed(lib)
        await sync.syncNow()
        #expect(sync.status(entry) == .pending)
        #expect(sync.problem != nil)
        hub.down = false
        await sync.syncNow()
        #expect(sync.status(entry) == .synced)
        #expect(sync.problem == nil)
    }

    @Test func configurationRefusalsPauseWithoutLosingPendingUploads() async throws {
        for (status, fields) in [
            (503, ["code": "kb_disabled"]),
            (403, ["code": "kb_read_only"]),
            (503, ["code": "internal", "detail": "the knowledge base is off"])
        ] {
            let (lib, sync, hub, root) = setup()
            defer { try? FileManager.default.removeItem(at: root) }
            let entry = try transcribed(lib)
            hub.refusal = APIError(status: status, message: "Request failed", fields: fields)
            await sync.syncNow()
            #expect(sync.automaticRetryPaused)
            #expect(sync.status(entry) == .pending)
            #expect(hub.stored.isEmpty)
            #expect(sync.problem?.contains("hub") == false)
            let before = hub.calls.count
            lib.change(entry.id) { $0.title = "Updated title" }
            sync.kick()
            await sync.syncNow()
            #expect(hub.calls.count == before, "saving while disabled must not resume automatic requests")
            hub.refusal = nil
            await sync.syncNow(retryPaused: true)
            #expect(sync.status(lib.entry(entry.id)!) == .synced)
            #expect(!sync.automaticRetryPaused)
        }
    }

    @Test func pairingLaterDoesNotNeedAConfigurationRetry() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root), hub = FakeRecordingHub()
        var client: APIClient?
        let sync = RecordingSync(library: lib, api: { client }, audioDaysChanged: { _ in })
        let entry = try transcribed(lib)
        await sync.syncNow()
        #expect(!sync.automaticRetryPaused && sync.status(entry) == .pending)
        client = api(hub)
        await sync.syncNow()
        #expect(sync.status(entry) == .synced)
    }

    @Test func transportTimeoutKeepsUploadsPendingWithoutClaimingHubIsOffline() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        let client = APIClient(config: ServerConfig(url: URL(string: "https://hub.invalid")!, token: "t"),
                               transport: { _ in throw URLError(.timedOut) })
        let sync = RecordingSync(library: lib, api: { client }, audioDaysChanged: { _ in })
        let entry = try transcribed(lib)
        await sync.syncNow()
        #expect(sync.status(entry) == .pending)
        #expect(!sync.automaticRetryPaused)
        #expect(sync.problem == String(localized: "The request timed out. Try again later."))
    }

    @Test func readOnlyAndUnsupportedDeletesStayPendingAndVisible() async throws {
        for refusal in [APIError(status: 403, message: "Read-only", fields: ["code": "kb_read_only"]),
                        APIError(status: 404, message: "Not found")] {
            let (lib, sync, hub, root) = setup()
            defer { try? FileManager.default.removeItem(at: root) }
            let entry = try transcribed(lib)
            await sync.syncNow()
            hub.refusal = refusal
            hub.writesOnly = true
            try sync.delete(entry.id)
            await sync.syncNow()
            #expect(sync.automaticRetryPaused)
            #expect(sync.store.deletes.contains(entry.id))
            #expect(hub.stored[entry.id.uuidString.lowercased()] != nil)
            #expect(sync.problem?.contains(String(localized: "Pending deletions have not been removed from Knowledge yet.")) == true)
            let relaunched = RecordingSync(library: lib, api: { nil })
            #expect(relaunched.store.deletes.contains(entry.id))
            hub.refusal = nil
            await sync.syncNow(retryPaused: true)
            #expect(sync.store.deletes.isEmpty)
            #expect(hub.stored.isEmpty)
        }
    }

    @Test func deleteHereDeletesThereEvenAfterBeingOffline() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        let entry = try transcribed(lib)
        await sync.syncNow()
        hub.down = true
        try sync.delete(entry.id)
        #expect(lib.entry(entry.id) == nil)
        await sync.syncNow()
        #expect(sync.store.deletes.contains(entry.id))
        hub.down = false
        await sync.syncNow()
        #expect(sync.store.deletes.isEmpty)
        #expect(hub.stored.isEmpty)
        #expect(hub.calls.contains("DELETE /api/v1/recordings/\(entry.id.uuidString.lowercased())"))
    }

    @Test func deletedInTheKnowledgeBaseGoesHereToo() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        let entry = try transcribed(lib)
        await sync.syncNow()
        hub.deleteThere(entry.id)
        lib.change(entry.id) { $0.title = "改名" } // the next upload gets 410
        await sync.syncNow()
        #expect(lib.entry(entry.id) == nil)
        #expect(lib.isDeleted(entry.id))
    }

    @Test func reconcileResendsWhatTheHubLost() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        let entry = try transcribed(lib)
        await sync.syncNow()
        hub.forget(entry.id) // a new hub
        let api = api(hub)
        let relaunched = RecordingSync(library: lib, api: { api }, audioDaysChanged: { _ in }) // asks the hub what it has first
        #expect(relaunched.status(entry) == .synced)
        await relaunched.syncNow()
        #expect(hub.stored[entry.id.uuidString.lowercased()] == entry.revision)
    }

    @Test func expiredRecordingsAreDroppedWithTheirAudio() async throws {
        let now = Date.now
        let (lib, sync, hub, root) = setup(now: now)
        defer { try? FileManager.default.removeItem(at: root) }
        hub.days = 3
        let old = try transcribed(lib, createdAt: now.addingTimeInterval(-5 * 86_400))
        let fresh = try transcribed(lib, createdAt: now.addingTimeInterval(-86_400))
        try Data("aac".utf8).write(to: lib.audio(old.id, 0))
        await sync.syncNow()
        #expect(sync.store.days == 3)
        #expect(lib.entry(old.id) == nil)
        #expect(!FileManager.default.fileExists(atPath: lib.folder(old.id).path))
        #expect(lib.entry(fresh.id) != nil)
        #expect(hub.stored[old.id.uuidString.lowercased()] == nil)
        #expect(hub.stored[fresh.id.uuidString.lowercased()] != nil)
    }

    /// #16444: `audio_days` is the phone's own-copy window even when it is forever, while `text_days` is only the
    /// Knowledge copy's window and may be longer than 365.
    @Test func splitRetentionKeepsAudioForeverAndTextPast365() async throws {
        let now = Date.now
        let (lib, sync, hub, root) = setup(now: now)
        defer { try? FileManager.default.removeItem(at: root) }
        hub.days = 365
        hub.audioDays = 0
        hub.textDays = 1000
        let old = try transcribed(lib, createdAt: now.addingTimeInterval(-500 * 86_400))
        await sync.syncNow()
        #expect(sync.audioRetentionDays == 0)
        #expect(sync.textRetentionDays == 1000)
        #expect(sync.store.audioDays == 0 && sync.store.days == 365, "old builds still read the capped legacy field")
        #expect(lib.entry(old.id) != nil, "forever audio means no local copy is deleted for age")
        #expect(sync.store.textDays == 1000, "the exact text window survives a relaunch")
        let relaunched = RecordingSync(library: lib, api: { nil }, audioDaysChanged: { _ in })
        #expect(relaunched.audioRetentionDays == 0 && relaunched.textRetentionDays == 1000)
    }

    /// A finite audio window still removes the phone's copy even when Knowledge keeps the text forever.
    @Test func audioRetentionDeletesLocalCopiesWhileTextIsForever() async throws {
        let now = Date.now
        let (lib, sync, hub, root) = setup(now: now)
        defer { try? FileManager.default.removeItem(at: root) }
        hub.days = 365
        hub.audioDays = 3
        hub.textDays = 0
        let expired = try transcribed(lib, createdAt: now.addingTimeInterval(-5 * 86_400))
        let fresh = try transcribed(lib, createdAt: now.addingTimeInterval(-86_400))
        await sync.syncNow()
        #expect(sync.audioRetentionDays == 3 && sync.textRetentionDays == 0)
        #expect(sync.store.audioDays == 3 && sync.store.days == 365)
        #expect(lib.entry(expired.id) == nil, "the phone follows audio_days")
        #expect(lib.entry(fresh.id) != nil)
    }

    /// An older hub has only `days`: it remains both the audio and the text window.
    @Test func legacyDaysIsTheFallbackForBothWindows() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        hub.days = 30
        hub.audioDays = nil
        hub.textDays = nil
        await sync.syncNow()
        #expect(sync.audioRetentionDays == 30 && sync.textRetentionDays == 30)
        #expect(sync.store.audioDays == nil && sync.store.days == 30)
        #expect(sync.store.textDays == 30, "the older hub's days fills the text field too")
    }

    /// `RecordingSync.Store` as the app wrote it before #16444: no `audioDays`, no `textDays`.
    nonisolated private struct StoreBefore16444: Encodable {
        var synced: [UUID: Int64]
        var rejected: [UUID: Int64] = [:]
        var deletes: Set<UUID> = []
        var days: Int
        var daysKnown: Bool
    }

    /// A `sync.json` from before #16444 has no `textDays`: it reads and the old shared window applies. The file is
    /// written the way the app wrote it, with `JSONEncoder` (#16642: a hand-written one had JSON objects where a
    /// `[UUID: Int64]` is an array, and read as no file at all).
    @Test func anOldSyncFileStillReadsWithDaysForBothWindows() throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        let recording = UUID()
        let old = StoreBefore16444(synced: [recording: 3], days: 30, daysKnown: true)
        try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
        try JSONEncoder().encode(old).write(to: root.appendingPathComponent("sync.json"))
        let sync = RecordingSync(library: lib, api: { nil }, audioDaysChanged: { _ in })
        #expect(sync.store.synced == [recording: 3] && sync.store.daysKnown, "the old file was read, not replaced by the defaults")
        #expect(sync.audioRetentionDays == 30 && sync.textRetentionDays == 30)
        #expect(sync.store.audioDays == nil, "an old file has no exact field")
    }

    @Test func silentRecordingIsNotUploaded() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        let entry = try transcribed(lib, text: "  ")
        await sync.syncNow()
        #expect(sync.status(entry) == .waiting)
        #expect(!hub.calls.contains { $0.hasPrefix("PUT") })
    }

    @Test func aNewRetentionTellsTheWatchHowLongItMayKeepAudio() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        let hub = FakeRecordingHub()
        let api = api(hub)
        var told: [Int] = []
        let sync = RecordingSync(library: lib, api: { api }, audioDaysChanged: { told.append($0) })
        hub.days = 3
        await sync.syncNow(retryPaused: true)
        #expect(told == [3], "recordingDayKeepDays follows audio_days (docs/assistant/day-recap.md「Watch and phone capabilities」)")
        await sync.syncNow(retryPaused: true)
        #expect(told == [3], "unchanged: nothing to tell")
        hub.days = 30
        await sync.syncNow(retryPaused: true)
        #expect(told == [3, 30])
        hub.textDays = 1000
        await sync.syncNow(retryPaused: true)
        #expect(told == [3, 30], "the watch follows audio, not text")
        hub.audioDays = 0
        await sync.syncNow(retryPaused: true)
        #expect(told == [3, 30, 0], "forever is passed to the watch as zero; its cap maps it to seven")
    }

    @Test func aDayWhosePartTheHubDeletedGoesWholeEverywhere() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        let hub = FakeRecordingHub()
        let api = api(hub)
        let store = RecordingDayStore(root: root, isDeleted: { lib.isDeletedSnapshot($0) }, writeDelay: 3600)
        var stopped: [UUID] = []
        let daySync = RecordingDaySync(store: store, library: lib, api: { api }, features: { [RecordingDayContract.feature] },
                                       audioDays: { 14 }, appState: RecordingDayForeground(), noteParts: { _, _ in },
                                       removed: { stopped.append($0) }, removeEverywhere: { _ in }, recapFinal: {})
        let sync = RecordingSync(library: lib, api: { api }, daySync: { daySync }, audioDaysChanged: { _ in })
        let day = UUID(), second = UUID()
        lib.adoptDay(id: day, day: "2027-01-15", timeZone: "Asia/Shanghai", createdAt: .now)
        sync.noteDayParts(day, [second])
        // The web deleted the day's second part.
        hub.deleteThere(second)
        await sync.syncNow(retryPaused: true)
        #expect(lib.entry(day) == nil && lib.isDeleted(day), "the day goes here, tombstoned")
        #expect(stopped == [day], "its transcription stops")
        #expect(hub.calls.contains("DELETE /api/v1/recordings/\(day.uuidString.lowercased())"), "every part goes from the hub")
        #expect(hub.calls.contains("DELETE /api/v1/recordings/\(second.uuidString.lowercased())"))
        #expect(sync.store.deletes.isEmpty && sync.store.dayParts?[day] == nil)
        var pending = await store.watchRequests()
        for _ in 0..<200 where pending.deletes.isEmpty {
            try? await Task.sleep(for: .milliseconds(2))
            pending = await store.watchRequests()
        }
        #expect(pending.deletes == [RecordingDayDelete(recording: day)], "and the watch's copy")
    }

    /// The day upload's `410` deletes a day the library has no row for yet (written only after its first chunk was
    /// stored): it is still a day, so its transcription stops and the watch is asked to delete its copy.
    @Test func aDayWithoutItsRowYetIsStillDeletedEverywhere() async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        let lib = RecordingLibrary(root: root)
        let store = RecordingDayStore(root: root, isDeleted: { lib.isDeletedSnapshot($0) }, writeDelay: 3600)
        var stopped: [UUID] = []
        let daySync = RecordingDaySync(store: store, library: lib, api: { nil }, features: { [RecordingDayContract.feature] },
                                       audioDays: { 14 }, appState: RecordingDayForeground(), noteParts: { _, _ in },
                                       removed: { stopped.append($0) }, removeEverywhere: { _ in }, recapFinal: {})
        let sync = RecordingSync(library: lib, api: { nil }, daySync: { daySync }, audioDaysChanged: { _ in })
        let day = UUID()
        try sync.delete(day, day: true)
        #expect(lib.isDeleted(day) && stopped == [day])
        var pending = await store.watchRequests()
        for _ in 0..<200 where pending.deletes.isEmpty {
            try? await Task.sleep(for: .milliseconds(2))
            pending = await store.watchRequests()
        }
        #expect(pending.deletes == [RecordingDayDelete(recording: day)])
    }

    // MARK: The replayed meeting (#10589)

    /// A replayed tape's meeting (`ReplayedMeeting`), as `MeetingReplay.play` saves it.
    private func replayed(_ lib: RecordingLibrary) throws -> RecordingEntry {
        var entry = RecordingEntry(id: ReplayedMeeting.id, createdAt: .now, source: .phone, title: "Weekly sync", active: false, expectedSegments: 1)
        entry.attendance = RecordingAttendance.room(.listener, private: false, photos: false, liveView: true, highlights: true)
        entry.segments = [RecordingSegment(index: 0, start: 0, duration: 30, sentences: [RecordingSentence(start: 1, end: 3, text: "Hello")])]
        lib.restore(entry.id)
        try lib.save(entry)
        return lib.entry(entry.id)!
    }

    /// Never uploaded, however much it looks like a recording to upload.
    @Test func theReplayedMeetingIsNeverUploaded() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        let entry = try replayed(lib)
        #expect(RecordingSync.uploadable(entry))
        #expect(sync.status(entry) == .waiting)
        await sync.syncNow()
        #expect(!hub.calls.contains { $0.contains(ReplayedMeeting.id.uuidString.lowercased()) }, "\(hub.calls)")
    }

    /// Delete is this phone's only: no `DELETE` for its fixed id, which would leave the hub a tombstone that deletes every
    /// later replay while it plays.
    @Test func deletingTheReplayedMeetingSendsNothing() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        let entry = try replayed(lib)
        try sync.delete(entry.id)
        #expect(lib.entry(entry.id) == nil)
        #expect(!sync.store.deletes.contains(entry.id))
        await sync.syncNow()
        #expect(!hub.calls.contains { $0.hasPrefix("DELETE") }, "\(hub.calls)")
    }

    /// A hub that holds a tombstone for its id (an older app deleted a replay there) has no say on the replay playing now;
    /// reconciliation (a relaunch asks the hub what it has first) still deletes here what was deleted there.
    @Test func aTombstoneOnTheHubLeavesTheReplayAlone() async throws {
        let (lib, sync, hub, root) = setup()
        defer { try? FileManager.default.removeItem(at: root) }
        let other = try transcribed(lib)
        await sync.syncNow()
        let entry = try replayed(lib)
        hub.deleteThere(ReplayedMeeting.id)
        hub.deleteThere(other.id)
        let api = api(hub)
        let relaunched = RecordingSync(library: lib, api: { api }, audioDaysChanged: { _ in })
        await relaunched.syncNow()
        #expect(lib.entry(entry.id) != nil)
        #expect(lib.entry(other.id) == nil, "a recording deleted there still goes here")
    }
}
