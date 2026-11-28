import Foundation
import os
import Testing
@testable import Persona

private let anchor = Date(timeIntervalSince1970: 1_800_000_000) // 2027-01-15T08:00:00Z, 16:00 in Shanghai

/// The phone's clock, readable from the store's queue.
private final class Clock: Sendable {
    private let value = OSAllocatedUnfairLock(initialState: anchor.addingTimeInterval(60))
    var now: Date { value.withLock { $0 } }
    func advance(_ seconds: TimeInterval) { value.withLock { $0 = $0.addingTimeInterval(seconds) } }
}

private func refusal(_ status: Int, _ code: String? = nil, _ extra: [String: String] = [:]) -> APIError {
    var fields = extra
    if let code { fields["code"] = code }
    return APIError(status: status, message: "refused", fields: fields)
}

/// A hub that keeps day recordings as the contract says (docs/assistant/day-recap.md「Phone to hub」): a `PUT` replaces
/// every segment and sets the base (its `200` with `base` and `next`; `stale` below the stored revision; `day_full` past
/// `capacity`), an append stores by position on that base (`base_changed`, `segments_gap`, `day_full` past `capacity`,
/// `too_large` past `maxPerAppend`). Any request can be made to fail once (`fail`). Every request is kept, its body as
/// JSON.
private final class DayHub: @unchecked Sendable {
    struct Request {
        var method: String
        var path: String
        var body: [String: Any]
    }

    private struct Held {
        var base: Int64
        var revision: Int64
        var segments: [[String: Any]]
    }

    private let lock = NSLock()
    private var watcher: (@Sendable (Request) -> Void)?
    private var held: [String: Held] = [:]
    private var failures: [String: [APIError]] = [:]
    private var log: [Request] = []
    private var capacity = Int.max
    private var maxPerAppend = Int.max
    private var status: [String: String] = [:]

    var requests: [Request] { lock.withLock { log } }
    func calls(_ method: String) -> [Request] { requests.filter { $0.method == method } }
    func texts(_ id: UUID) -> [String] { lock.withLock { held[key(id)]?.segments.map { $0["text"] as? String ?? "" } ?? [] } }
    func base(_ id: UUID) -> Int64? { lock.withLock { held[key(id)]?.base } }

    /// The next `method` request fails with `error`.
    func fail(_ method: String, _ error: APIError) { lock.withLock { failures[method, default: []].append(error) } }
    func set(capacity: Int? = nil, maxPerAppend: Int? = nil) {
        lock.withLock {
            if let capacity { self.capacity = capacity }
            if let maxPerAppend { self.maxPerAppend = maxPerAppend }
        }
    }
    /// Every `method` request answers `200` with this status (`expired`).
    func answer(_ method: String, status: String) { lock.withLock { self.status[method] = status } }
    /// The hub lost its last segments (a restore), or the whole recording (a new hub).
    func truncate(_ id: UUID, to count: Int) {
        lock.withLock {
            guard var rec = held[key(id)] else { return }
            rec.segments = Array(rec.segments.prefix(count))
            held[key(id)] = rec
        }
    }
    func forget(_ id: UUID) { lock.withLock { held[key(id)] = nil } }
    /// The hub holds segments this phone never sent (another device, a bug), or a revision far above the phone's.
    func inject(_ id: UUID, _ texts: [String]) {
        lock.withLock { held[key(id)]?.segments += texts.map { ["start": 0.0, "end": 0.0, "text": $0] } }
    }
    func raise(_ id: UUID, revision: Int64) { lock.withLock { held[key(id)]?.revision = revision } }
    /// Called with every request as it arrives, before it is answered.
    func watch(_ body: @escaping @Sendable (Request) -> Void) { lock.withLock { watcher = body } }

    func answer(_ request: LocalRequest) throws -> Data {
        let body = (request.body.flatMap { try? JSONSerialization.jsonObject(with: $0) } as? [String: Any]) ?? [:]
        if let watcher = lock.withLock({ watcher }) { watcher(Request(method: request.method, path: request.path, body: body)) }
        return try lock.withLock {
            log.append(Request(method: request.method, path: request.path, body: body))
            if var queued = failures[request.method], !queued.isEmpty {
                let error = queued.removeFirst()
                failures[request.method] = queued
                throw error
            }
            let id = request.path.split(separator: "/").map(String.init)[3]
            return request.method == "PUT" ? try put(id, body) : try append(id, body)
        }
    }

    private func put(_ id: String, _ body: [String: Any]) throws -> Data {
        let revision = (body["revision"] as? NSNumber)?.int64Value ?? 0
        if let status = status["PUT"] { return try json(["id": id, "revision": revision, "status": status]) }
        if let have = held[id], have.revision >= revision { return try json(["id": id, "revision": have.revision, "status": "stale"]) }
        let segments = body["segments"] as? [[String: Any]] ?? []
        guard segments.count <= capacity else { throw refusal(413, "day_full", ["next": "\(held[id]?.segments.count ?? 0)"]) }
        held[id] = Held(base: revision, revision: revision, segments: segments)
        return try json(["id": id, "revision": revision, "base": revision, "next": segments.count, "status": "stored"])
    }

    private func append(_ id: String, _ body: [String: Any]) throws -> Data {
        guard var rec = held[id] else { throw refusal(404, "recording_not_found") }
        if let status = status["POST"] { return try json(["id": id, "revision": rec.revision, "next": rec.segments.count, "status": status]) }
        let base = (body["base"] as? NSNumber)?.int64Value ?? 0
        let revision = (body["revision"] as? NSNumber)?.int64Value ?? 0
        let from = body["from"] as? Int ?? 0
        let segments = body["segments"] as? [[String: Any]] ?? []
        guard revision > base else { throw refusal(400) }
        guard segments.count <= maxPerAppend else { throw refusal(413, "too_large") }
        guard base == rec.base else { throw refusal(409, "base_changed", ["base": "\(rec.base)", "next": "\(rec.segments.count)"]) }
        guard from <= rec.segments.count else { throw refusal(409, "segments_gap", ["next": "\(rec.segments.count)"]) }
        let new = Array(segments.dropFirst(rec.segments.count - from))
        guard rec.segments.count + new.count <= capacity else { throw refusal(413, "day_full", ["next": "\(rec.segments.count)"]) }
        rec.segments += new
        rec.revision = max(rec.revision, revision)
        held[id] = rec
        return try json(["id": id, "revision": rec.revision, "next": rec.segments.count, "status": "stored"])
    }

    private func json(_ object: [String: Any]) throws -> Data { try JSONSerialization.data(withJSONObject: object) }
    private func key(_ id: UUID) -> String { id.uuidString.lowercased() }
}

/// Requests that wait while it is held, and fail as cancelled once their task is: a request caught in flight.
private final class Gate: Sendable {
    private let state = OSAllocatedUnfairLock(initialState: (held: false, arrived: 0))

    var arrived: Int { state.withLock { $0.arrived } }
    func hold() { state.withLock { $0.held = true } }
    func release() { state.withLock { $0.held = false } }

    func pass() async throws {
        state.withLock { $0.arrived += 1 }
        while state.withLock({ $0.held }) {
            try Task.checkCancellation()
            try await Task.sleep(for: .milliseconds(2))
        }
        try Task.checkCancellation()
    }
}

/// The app's state as the upload sees it: in front or not, whether the system gives background time, and the grants it
/// gave, each of which can be made to run out.
@MainActor private final class FakeAppState: RecordingDayAppState {
    final class Grant: RecordingDayGrant {
        let onExpiry: @MainActor () -> Void
        private(set) var ended = false
        init(onExpiry: @escaping @MainActor () -> Void) { self.onExpiry = onExpiry }
        func end() { ended = true }
        func expire() {
            guard !ended else { return }
            onExpiry()
            ended = true
        }
    }

    var inForeground = true
    var givesGrants = true
    private(set) var grants: [Grant] = []

    func beginGrant(named name: String, onExpiry: @escaping @MainActor () -> Void) -> RecordingDayGrant? {
        guard givesGrants else { return nil }
        let grant = Grant(onExpiry: onExpiry)
        grants.append(grant)
        return grant
    }
}

/// Waits (briefly) until `condition` holds: a request reaching the hub happens on the upload's own task.
@MainActor private func until(_ condition: () -> Bool) async {
    var tries = 0
    while !condition(), tries < 2000 {
        tries += 1
        try? await Task.sleep(for: .milliseconds(1))
    }
    #expect(condition())
}

/// Lets the store's change stream reach a started upload, then waits for any pass that started.
@MainActor private func settle(_ sync: RecordingDaySync) async {
    for _ in 0..<50 { await Task.yield() }
    await sync.whenIdle()
}

/// A temporary recordings folder with one day in it (`id`, Shanghai, recorded from `anchor`), its library row in English,
/// a store whose coalesced write never comes during a test (only `flush` writes), and the fake hub.
@MainActor private final class Fixture {
    let root = FileManager.default.temporaryDirectory.appendingPathComponent("day-sync-\(UUID().uuidString)", isDirectory: true)
    let id = UUID()
    let clock = Clock()
    let hub = DayHub()
    let gate = Gate()
    let app = FakeAppState()
    let library: RecordingLibrary
    let store: RecordingDayStore
    var features: Set<String> = [RecordingDayContract.feature]
    var removed: [UUID] = []
    var deletedEverywhere: [UUID] = []
    var noted: [UUID: [UUID]] = [:]
    var finals = 0
    private var events = 0

    init() {
        library = RecordingLibrary(root: root)
        let lib = library, clock = clock
        store = RecordingDayStore(root: root, isDeleted: { lib.isDeletedSnapshot($0) }, now: { clock.now }, writeDelay: 3600)
        library.adoptDay(id: id, day: "2027-01-15", timeZone: "Asia/Shanghai", createdAt: anchor)
        library.change(id) { $0.locale = "en-US" }
    }

    func sync() -> RecordingDaySync {
        let hub = hub, gate = gate, clock = clock
        let api = APIClient(config: ServerConfig(url: URL(string: "https://hub.invalid")!, token: "t"), transport: { request in
            try await gate.pass()
            return try hub.answer(request)
        })
        return RecordingDaySync(store: store, library: library, api: { api }, features: { [unowned self] in self.features },
                                now: { clock.now }, audioDays: { 14 }, appState: app,
                                noteParts: { [unowned self] day, ids in self.noted[day] = ids },
                                removed: { [unowned self] in self.removed.append($0) },
                                removeEverywhere: { [unowned self] id in
                                    self.deletedEverywhere.append(id)
                                    try? self.library.remove(id)
                                },
                                recapFinal: { [unowned self] in self.finals += 1 }, debounce: .zero)
    }

    func at(_ offset: Double) -> Date { anchor.addingTimeInterval(offset) }

    /// Chunk `seq`, `offset` seconds after `anchor`, stored as the watch sent it and transcribed as the transcriber saves
    /// a run: `texts` are its sentences, the first starting 1 s in, a second apart, placed in the same change.
    func chunk(_ seq: Int, at offset: Double, texts: [String]) async throws {
        let header = RecordingDayHeader(recording: id, recordingStartedAt: anchor, tz: "Asia/Shanghai", day: "2027-01-15", seq: seq,
                                        startedAt: at(offset), duration: 10, level: -30)
        _ = try store.store(header: header, audio: Data([1, 2, 3]))
        let sentences = texts.enumerated().map { RecordingSentence(start: Double($0.offset) + 1, end: Double($0.offset) + 1.5, text: $0.element) }
        await store.update(id) { record in
            record.chunks[seq]?.state = .transcribed
            record.chunks[seq]?.sentences = sentences
            RecordingDayUpload.place(&record)
        }
    }

    /// The watch's next control event (numbered in the order the test sends them).
    func event(_ make: (Int) -> RecordingDayEvent) throws {
        let envelope = RecordingDayEventEnvelope(recording: id, recordingStartedAt: anchor, tz: "Asia/Shanghai", day: "2027-01-15",
                                                 event: make(events))
        events += 1
        #expect(try store.store(event: envelope) == .applied)
    }

    /// The microphone closed at `offset` by itself, and the day closed at `closed` with `seqCount` chunks.
    func end(at offset: Double, closed: Double, seqCount: Int) throws {
        try event { .paused($0, at: at(offset), reason: RecordingDayContract.Reason.stopped) }
        try event { .closed($0, at: at(closed), seqCount: seqCount) }
    }

    func record() async throws -> RecordingDayRecord { try #require(await store.record(id)) }
    func part(_ number: Int = 1) async throws -> RecordingDayPart { try #require(try await record().parts.first { $0.part == number }) }
    /// The day as `day.json` holds it now: what a crash would leave.
    nonisolated static func onDisk(_ root: URL, _ id: UUID) -> RecordingDayRecord? {
        let url = root.appendingPathComponent(id.uuidString).appendingPathComponent("day.json")
        return (try? Data(contentsOf: url)).flatMap { try? JSONDecoder().decode(RecordingDayRecord.self, from: $0) }
    }
    func cleanUp() { try? FileManager.default.removeItem(at: root) }
}

private extension DayHub.Request {
    var revision: Int64 { (body["revision"] as? NSNumber)?.int64Value ?? 0 }
    var segments: [[String: Any]] { body["segments"] as? [[String: Any]] ?? [] }
    var texts: [String] { segments.map { $0["text"] as? String ?? "" } }
    var coverage: [String: Any] { body["coverage"] as? [String: Any] ?? [:] }
    var gaps: [[String: Any]] { coverage["gaps"] as? [[String: Any]] ?? [] }
    func time(_ key: String) -> Date? { (body[key] as? String).flatMap { RecordingDayWire.date($0) } }
}

/// All-day recording's upload (docs/assistant/day-recap.md「Phone to hub」): only behind the flag, created with a `PUT`,
/// grown by appends on the day's wall clock, every contract error handled by its code, parts on `day_full`, repairs
/// that keep every other position, crash-safe, and revisions only going up.
@MainActor struct RecordingDaySyncTests {
    @Test func nothingGoesWithoutTheHubFlag() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["Morning."])
        f.features = ["sessions_list"]
        let sync = f.sync()
        #expect(await sync.syncNow(f.id) == .blocked)
        #expect(f.hub.requests.isEmpty)
        #expect(try await f.part().created == false, "the positions are the phone's; nothing went")

        f.features = [RecordingDayContract.feature]
        #expect(await sync.syncNow(f.id) == .done)
        #expect(f.hub.requests.map(\.method) == ["PUT", "POST"])
    }

    @Test func theCreatePutCarriesTheDayAndAppendsGoOnTheWallClock() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["Morning."])
        try f.event { .mark($0, at: f.at(4), label: RecordingDayEvent.Label.meeting) }
        try await f.chunk(1, at: 10, texts: ["Second.", "Third."])
        let sync = f.sync()
        #expect(await sync.syncNow(f.id) == .done)

        let put = try #require(f.hub.calls("PUT").first)
        #expect(put.path == "/api/v1/recordings/\(f.id.uuidString.lowercased())")
        #expect(put.body["kind"] as? String == "day")
        #expect(put.body["day"] as? String == "2027-01-15")
        #expect(put.body["tz"] as? String == "Asia/Shanghai")
        #expect(put.body["part"] as? Int == 1)
        let started = try #require(put.body["started_at"] as? String)
        #expect(started == "2027-01-15T16:00:00.000+08:00", "recording_started_at, in the recording's own offset")
        #expect(put.body["ended_at"] == nil, "still recording")
        #expect(put.body["duration"] as? Double == 20)
        #expect(put.body["language"] as? String == "en-US")
        #expect(put.body["device"] as? String == "watch")
        #expect(put.body["title"] as? String == "")
        #expect(put.texts.isEmpty)
        #expect(put.body["complete"] as? Bool == false)
        let bookmarks = put.body["bookmarks"] as? [[String: Any]] ?? []
        #expect(bookmarks.count == 1 && bookmarks[0]["at"] as? Double == 4 && bookmarks[0]["label"] as? String == "meeting",
                "a mark event, on the day's clock")
        #expect(put.coverage["to"] as? Double == 0, "nothing transcribed is on the hub yet")
        #expect(put.revision > 0)

        let append = try #require(f.hub.calls("POST").first)
        #expect(append.path.hasSuffix("/segments"))
        #expect((append.body["base"] as? NSNumber)?.int64Value == put.revision)
        #expect(append.revision > put.revision)
        #expect(append.body["from"] as? Int == 0)
        #expect(append.texts == ["Morning.", "Second.", "Third."], "seq order, then sentence order")
        // Seconds since started_at, pauses included: the chunk's start plus the sentence's offset in it.
        #expect(append.segments.map { $0["start"] as? Double } == [1, 11, 12])
        #expect(append.coverage["to"] as? Double == 20)
        #expect(append.body["title"] == nil, "absent: unchanged, the hub names the day")

        var part = try await f.part()
        #expect(part.created && part.base == put.revision && part.next == 3 && part.count == 3)
        #expect(f.hub.texts(f.id) == ["Morning.", "Second.", "Third."])

        // New text while recording waits out `appendInterval` after the last append.
        try await f.chunk(2, at: 20, texts: ["Fourth."])
        f.clock.advance(5)
        guard case .later(let at) = await sync.syncNow(f.id) else { Issue.record("expected the throttle"); return }
        #expect(at == f.clock.now.addingTimeInterval(RecordingDayContract.appendInterval - 5))
        #expect(f.hub.calls("POST").count == 1)
        f.clock.advance(RecordingDayContract.appendInterval)
        #expect(await sync.syncNow(f.id) == .done)
        let next = try #require(f.hub.calls("POST").last)
        #expect(next.body["from"] as? Int == 3 && next.texts == ["Fourth."])
        part = try await f.part()
        #expect(part.next == 4 && part.count == 4)
    }

    @Test func theCloseGoesAtOnceWithCompleteAndEndedAt() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["Morning."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        try await f.chunk(1, at: 10, texts: ["Evening."])
        try f.end(at: 20, closed: 3600, seqCount: 2)
        #expect(await sync.syncNow(f.id) == .done, "the day closed: no throttle")
        let last = try #require(f.hub.calls("POST").last)
        #expect(last.texts == ["Evening."])
        #expect(last.body["complete"] as? Bool == true)
        #expect(last.time("ended_at") == f.at(3600))
        #expect((last.body["ended_at"] as? String)?.hasSuffix("+08:00") == true)
        #expect(last.coverage["to"] as? Double == 3600)
        #expect(last.gaps.map { $0["reason"] as? String } == ["stopped"])
        #expect(last.gaps.first?["from"] as? Double == 20 && last.gaps.first?["to"] as? Double == 3600)
        #expect(try await f.part().complete)
        // Complete: nothing more goes, whatever comes.
        let sent = f.hub.requests.count
        f.clock.advance(60)
        #expect(await sync.syncNow(f.id) == .done)
        #expect(f.hub.requests.count == sent)
    }

    @Test func anOpenPauseSendsItsGapAtMostEveryFewMinutes() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["Morning."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        // A call took the microphone 10 s in, and the user hasn't continued yet.
        try f.event { .paused($0, at: f.at(10), reason: RecordingDayContract.Reason.interrupted) }
        f.clock.advance(30)
        #expect(await sync.syncNow(f.id) == .done)
        #expect(f.hub.requests.count == 2, "nothing new to say yet")
        f.clock.advance(RecordingDayCoverage.openGapStep)
        #expect(await sync.syncNow(f.id) == .done)
        let first = try #require(f.hub.calls("POST").last)
        #expect(first.texts.isEmpty && first.gaps.map { $0["reason"] as? String } == ["interrupted"])
        #expect(first.coverage["to"] as? Double == 10, "the text ends where the microphone closed")
        // Within the same step the coverage says the same: no request for it.
        f.clock.advance(60)
        #expect(await sync.syncNow(f.id) == .done)
        #expect(f.hub.requests.count == 3)
        f.clock.advance(RecordingDayCoverage.openGapStep)
        #expect(await sync.syncNow(f.id) == .done)
        #expect(f.hub.requests.count == 4)
        let later = try #require(f.hub.calls("POST").last?.gaps.first?["to"] as? Double)
        #expect(later > (first.gaps.first?["to"] as? Double ?? 0))
    }

    @Test func baseChangedOnABaseThePhoneDidNotPutRepairs() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One.", "Two."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        let first = try await f.part()
        try await f.chunk(1, at: 10, texts: ["Three."])
        f.clock.advance(60)
        f.hub.fail("POST", refusal(409, "base_changed", ["base": "999", "next": "2"]))
        #expect(await sync.syncNow(f.id) == .done)
        let repair = try #require(f.hub.calls("PUT").last)
        #expect(f.hub.calls("PUT").count == 2)
        #expect(repair.texts == ["One.", "Two.", "Three."], "every positioned sentence")
        #expect(repair.revision > first.maxSentRevision)
        let part = try await f.part()
        #expect(part.base == repair.revision && part.next == 3 && part.count == 3 && part.pendingPut == nil && !part.needsRepair)
        #expect(f.hub.texts(f.id) == ["One.", "Two.", "Three."])
    }

    @Test func segmentsGapSendsFromTheHubsCount() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One.", "Two."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        f.hub.truncate(f.id, to: 1)
        try await f.chunk(1, at: 10, texts: ["Three."])
        f.clock.advance(60)
        #expect(await sync.syncNow(f.id) == .done)
        let posts = f.hub.calls("POST")
        #expect(posts.map { $0.body["from"] as? Int } == [0, 2, 1], "refused at 2 (the hub holds 1), sent again from 1")
        #expect(f.hub.texts(f.id) == ["One.", "Two.", "Three."])
        #expect(f.hub.calls("PUT").count == 1, "no repair needed")
    }

    @Test func aCountTheHubHoldsThatThePhoneNeverSentRepairs() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        f.hub.inject(f.id, ["Not mine.", "Nor this."])
        try await f.chunk(1, at: 10, texts: ["Two."])
        f.clock.advance(60)
        #expect(await sync.syncNow(f.id) == .done)
        // The append's 200 says next 3, not from + 1 = 2: the logs disagree, and only a replacement mends that.
        #expect(f.hub.requests.map(\.method) == ["PUT", "POST", "POST", "PUT"])
        #expect(f.hub.texts(f.id) == ["One.", "Two."])
        let part = try await f.part()
        #expect(part.next == 2 && part.count == 2 && !part.needsRepair)
    }

    @Test func recordingNotFoundCreatesItAgainWithItsText() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        f.hub.forget(f.id)
        try await f.chunk(1, at: 10, texts: ["Two."])
        f.clock.advance(60)
        #expect(await sync.syncNow(f.id) == .done)
        #expect(f.hub.requests.map(\.method) == ["PUT", "POST", "POST", "PUT"])
        #expect(f.hub.calls("PUT").last?.texts == ["One.", "Two."], "every sentence that has a position, the one refused too")
        #expect(f.hub.texts(f.id) == ["One.", "Two."])
        #expect(try await f.part().next == 2)
    }

    @Test func anOlderManagerIsLeftAloneUntilItsNextHello() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        // The append route is missing: a 404 without the contract's code.
        f.hub.fail("POST", refusal(404))
        try await f.chunk(1, at: 10, texts: ["Two."])
        f.clock.advance(60)
        #expect(await sync.syncNow(f.id) == .blocked)
        #expect(try await f.record().unsupportedUntilHello)
        // New chunks don't bring a storm of refused requests.
        try await f.chunk(2, at: 20, texts: ["Three."])
        f.clock.advance(60)
        #expect(await sync.syncNow(f.id) == .blocked)
        #expect(f.hub.requests.count == 3)
        sync.helloReceived(offered: true)
        #expect(await sync.syncNow(f.id) == .done)
        #expect(f.hub.texts(f.id) == ["One.", "Two.", "Three."])
        #expect(try await f.record().unsupportedUntilHello == false)
    }

    @Test func aRefusedPartStopsUntilAHelloThenGoesAsAReplacement() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        f.hub.fail("POST", refusal(400, "bad_from"))
        try await f.chunk(1, at: 10, texts: ["Two."])
        f.clock.advance(60)
        #expect(await sync.syncNow(f.id) == .done)
        let part = try await f.part()
        #expect(part.problem == "bad_from")
        #expect(RecordingDaySyncState(try await f.record(), hubOffered: true) == .problem, "shown on the day's page")
        // Never sent again unchanged: neither by new text nor by the next visit.
        try await f.chunk(2, at: 20, texts: ["Three."])
        f.clock.advance(60)
        #expect(await sync.syncNow(f.id) == .done)
        #expect(f.hub.requests.count == 3)
        // A hello lets it try once more, as a full replacement.
        sync.helloReceived(offered: true)
        #expect(await sync.syncNow(f.id) == .done)
        #expect(f.hub.requests.map(\.method) == ["PUT", "POST", "POST", "PUT"])
        #expect(f.hub.texts(f.id) == ["One.", "Two.", "Three."])
        #expect(try await f.part().problem == nil)
    }

    @Test func tooLargeHalvesTheBatchAndADaySizedSentenceStops() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: (1...10).map { "S\($0)." })
        f.hub.set(maxPerAppend: 4)
        let sync = f.sync()
        #expect(await sync.syncNow(f.id) == .done)
        let sizes = f.hub.calls("POST").map(\.segments.count)
        #expect(sizes.prefix(7) == [10, 10, 10, 10, 10, 10, 7], "halved from 500 until a batch fits")
        #expect(sizes.dropFirst(7).allSatisfy { $0 <= 4 })
        #expect(f.hub.texts(f.id) == (1...10).map { "S\($0)." }, "in order, none twice, the smaller batches without waiting")
        #expect(try await f.record().parts.count == 1, "too_large never starts a part")

        let g = Fixture()
        defer { g.cleanUp() }
        try await g.chunk(0, at: 0, texts: ["Long."])
        g.hub.set(maxPerAppend: 0)
        #expect(await g.sync().syncNow(g.id) == .blocked)
        #expect(try await g.record().problem == "too_large")
    }

    @Test func dayFullOnAnAppendContinuesInTheNextPartOnTheSameClock() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One.", "Two."])
        f.hub.set(capacity: 2)
        let sync = f.sync()
        await sync.syncNow(f.id)
        try await f.chunk(1, at: 10, texts: ["Three."])
        f.clock.advance(60)
        let closedAt = f.clock.now
        #expect(await sync.syncNow(f.id) == .done)

        let record = try await f.record()
        #expect(record.parts.count == 2)
        let first = try #require(record.parts.first { $0.part == 1 })
        let second = try #require(record.parts.first { $0.part == 2 })
        #expect(second.hubID != f.id && second.startedAt == anchor && second.created && second.next == 1 && second.count == 1)
        #expect(first.endedAt == closedAt && first.count == 2 && first.next == 2 && !first.complete, "complete with the day")
        #expect(record.chunks[1]?.positions == [RecordingDayPosition(part: 2, pos: 0)], "the sentence the hub didn't store moved")
        #expect(f.noted[f.id] == [second.hubID], "kept for deleting every part")
        // The new part was on disk before its PUT, so a crash can't make a second one.
        #expect(Fixture.onDisk(f.root, f.id)?.parts.map(\.hubID) == record.parts.map(\.hubID))

        let calls = f.hub.requests.map { "\($0.method) \($0.path.contains(second.hubID.uuidString.lowercased()) ? 2 : 1)" }
        #expect(calls == ["PUT 1", "POST 1", "POST 1", "POST 1", "PUT 2", "POST 2"])
        let close = f.hub.calls("POST")[2]
        #expect(close.texts.isEmpty && close.time("ended_at") == closedAt && close.body["complete"] as? Bool == false,
                "part 1 is told it ended; it is complete only with the day")
        let create = try #require(f.hub.calls("PUT").last)
        #expect(create.body["part"] as? Int == 2 && create.body["day"] as? String == "2027-01-15"
                && create.body["tz"] as? String == "Asia/Shanghai")
        #expect(create.time("started_at") == anchor, "part 1's started_at")
        #expect(f.hub.texts(second.hubID) == ["Three."])
        #expect(f.hub.calls("POST").last?.segments.first?["start"] as? Double == 11, "the day's clock: never negative")

        // The day closes: both parts are told they are complete.
        try f.end(at: 20, closed: 3600, seqCount: 2)
        #expect(await sync.syncNow(f.id) == .done)
        let finals = f.hub.calls("POST").suffix(2)
        #expect(finals.allSatisfy { $0.body["complete"] as? Bool == true && $0.texts.isEmpty })
        #expect(Set(finals.map(\.path)) == Set([f.id, second.hubID].map { "/api/v1/recordings/\($0.uuidString.lowercased())/segments" }))
        #expect(try await f.part(1).complete)
        #expect(try await f.part(2).complete)
    }

    @Test func dayFullOnARepairPutRepartitionsThePartsAfterIt() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One.", "Two."])
        try await f.chunk(1, at: 10, texts: ["Three.", "Four."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        // The hub now counts the day as fuller than the phone does; an erase on the phone asks for a repair.
        f.hub.set(capacity: 3)
        #expect(await f.store.requestErase(f.id, from: f.at(1), to: f.at(1.5)))
        f.clock.advance(60)
        #expect(await sync.syncNow(f.id) == .done)
        let record = try await f.record()
        #expect(record.parts.map(\.part) == [1, 2])
        let second = try #require(record.parts.first { $0.part == 2 })
        // In position order: part 1 keeps three (the erased one empty in its place), the fourth goes to part 2.
        #expect(f.hub.texts(f.id) == ["", "Two.", "Three."])
        #expect(f.hub.texts(second.hubID) == ["Four."])
        #expect(record.chunks[1]?.positions == [RecordingDayPosition(part: 1, pos: 2), RecordingDayPosition(part: 2, pos: 0)])
        let first = try await f.part(1)
        #expect(first.count == 3 && first.next == 3 && first.endedAt != nil && !first.needsRepair)
        #expect(second.count == 1 && second.next == 1 && second.endedAt == nil && second.created)
        #expect(f.noted[f.id] == [second.hubID])
    }

    @Test func notDayIsAProblemAndNothingIsWrittenOverIt() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        f.hub.fail("POST", refusal(409, "not_day"))
        let sync = f.sync()
        #expect(await sync.syncNow(f.id) == .blocked)
        #expect(try await f.record().problem == "not_day")
        let sent = f.hub.requests.count
        try await f.chunk(1, at: 10, texts: ["Two."])
        f.clock.advance(60)
        #expect(await sync.syncNow(f.id) == .blocked)
        #expect(f.hub.requests.count == sent)
    }

    @Test func forbiddenPausesUntilTheAppComesBack() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        f.hub.fail("PUT", refusal(403, "kb_read_only"))
        let sync = f.sync()
        #expect(await sync.syncNow(f.id) == .blocked)
        #expect(sync.paused)
        #expect(await sync.syncNow(f.id) == .blocked)
        #expect(f.hub.requests.count == 1)
        sync.foreground()
        #expect(await sync.syncNow(f.id) == .done)
        #expect(f.hub.texts(f.id) == ["One."])
    }

    @Test func aPartDeletedOnTheHubDeletesTheWholeDay() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        try await f.chunk(1, at: 10, texts: ["Two."])
        f.clock.advance(60)
        f.hub.fail("POST", refusal(410, "recording_deleted"))
        #expect(await sync.syncNow(f.id) == .done)
        #expect(f.deletedEverywhere == [f.id], "every part on the hub, the day here and on the watch")
        #expect(f.library.entry(f.id) == nil && f.library.isDeleted(f.id))
        #expect(await f.store.record(f.id) == nil)
    }

    @Test func removingADayHereAsksTheWatchToDeleteItsCopy() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        let sync = f.sync()
        try f.library.remove(f.id)
        sync.removedHere(f.id)
        #expect(f.removed == [f.id], "its transcription stops")
        var pending = await f.store.watchRequests()
        for _ in 0..<200 where pending.deletes.isEmpty {
            try? await Task.sleep(for: .milliseconds(2))
            pending = await f.store.watchRequests()
        }
        #expect(pending.deletes == [RecordingDayDelete(recording: f.id)])
    }

    @Test func expiredStopsTheDayForGood() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        f.hub.answer("PUT", status: "expired")
        let sync = f.sync()
        #expect(await sync.syncNow(f.id) == .done)
        #expect(try await f.part().expired)
        try await f.chunk(1, at: 10, texts: ["Two."])
        f.clock.advance(60)
        await sync.syncNow(f.id)
        #expect(f.hub.requests.count == 1)
    }

    @Test func unavailableBacksOffAndSendsTheSamePositionsAgain() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One.", "Two."])
        f.hub.fail("POST", refusal(503, "kb_unavailable"))
        let sync = f.sync()
        #expect(await sync.syncNow(f.id) == .failed)
        let part = try await f.part()
        #expect(part.count == 2 && part.next == 0, "positions kept, nothing confirmed")
        #expect(await sync.syncNow(f.id) == .done)
        let posts = f.hub.calls("POST")
        #expect(posts.count == 2)
        #expect(posts[1].body["from"] as? Int == 0 && posts[1].texts == ["One.", "Two."], "the same sentences at the same positions")
        #expect(f.hub.texts(f.id) == ["One.", "Two."])
    }

    @Test func anEraseRepairsTheHubWithAPutThatKeepsEveryOtherPosition() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["Keep one."])
        try await f.chunk(1, at: 10, texts: ["Erase me."])
        try await f.chunk(2, at: 20, texts: ["Keep two."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        let before = try await f.part()
        // The watch erased 10–20 s after the phone had the chunk.
        try f.event { .erase($0, id: UUID(), from: f.at(10), to: f.at(20), seqs: []) }
        #expect(try await f.part().needsRepair)
        #expect(await sync.syncNow(f.id) == .done)
        let repair = try #require(f.hub.calls("PUT").last)
        #expect(f.hub.calls("PUT").count == 2)
        #expect(repair.revision > before.maxSentRevision)
        #expect(repair.texts == ["Keep one.", "", "Keep two."], "an empty segment in place of the erased one")
        #expect(repair.gaps.contains { $0["reason"] as? String == "erased" && $0["from"] as? Double == 10 && $0["to"] as? Double == 20 })
        let after = try await f.part()
        #expect(after.base == repair.revision && after.next == 3 && after.count == 3 && after.pendingPut == nil && !after.needsRepair)
        #expect(f.hub.texts(f.id) == ["Keep one.", "", "Keep two."])

        // The same on the phone: its own erase, then the next text appended after the kept positions.
        #expect(await f.store.requestErase(f.id, from: f.at(20), to: f.at(30)))
        try await f.chunk(3, at: 30, texts: ["After."])
        f.clock.advance(60)
        #expect(await sync.syncNow(f.id) == .done)
        #expect(f.hub.texts(f.id) == ["Keep one.", "", "", "After."])
    }

    @Test func aRepairDecidedBeforeACrashGoesAgainWithItsRevision() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        await f.sync().syncNow(f.id)
        let decided: Int64 = 9_000_000_000_000
        await f.store.update(f.id) { record in
            record.parts[0].pendingPut = decided
            record.parts[0].maxSentRevision = decided
        }
        #expect(await f.store.flush(f.id))
        let relaunched = f.sync()
        #expect(await relaunched.syncNow(f.id) == .done)
        let again = try #require(f.hub.calls("PUT").last)
        #expect(f.hub.requests.count == 3)
        #expect(again.revision == decided && again.texts == ["One."])
        let part = try await f.part()
        #expect(part.pendingPut == nil && part.base == decided)
    }

    @Test func aStalePutGoesAgainAboveTheHubsRevision() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        // Another device (or a clock far ahead) left the hub at a revision above anything this phone sent.
        let far: Int64 = 9_000_000_000_000
        f.hub.raise(f.id, revision: far)
        try f.event { .erase($0, id: UUID(), from: f.at(1), to: f.at(1.5), seqs: []) }
        f.clock.advance(60)
        #expect(await sync.syncNow(f.id) == .done)
        let puts = f.hub.calls("PUT")
        #expect(puts.count == 3, "the create, the stale repair, and the same repair above the hub's revision")
        #expect(puts[1].revision < far && puts[2].revision == far + 1)
        #expect(try await f.part().base == far + 1)
        #expect(f.hub.texts(f.id) == [""])
    }

    @Test func revisionsOnlyGoUpEvenWhenTheClockGoesBack() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        f.clock.advance(-3600)
        try await f.chunk(1, at: 10, texts: ["Two."])
        try f.end(at: 20, closed: 20, seqCount: 2)
        await sync.syncNow(f.id)
        let revisions = f.hub.requests.map(\.revision)
        #expect(revisions.count >= 3)
        #expect(zip(revisions, revisions.dropFirst()).allSatisfy { $0 < $1 })
        let base = f.hub.calls("PUT")[0].revision
        #expect(f.hub.calls("POST").allSatisfy { $0.revision > base && ($0.body["base"] as? NSNumber)?.int64Value == base })
        #expect(try await f.part().maxSentRevision == revisions.last)
    }

    @Test func reconciliationResendsWhatTheHubLostAndReplacesAnotherBase() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One.", "Two."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        let base = try #require(try await f.part().base)
        // Same base, fewer positions than sent: from there again.
        f.hub.truncate(f.id, to: 1)
        f.clock.advance(120)
        sync.reconciled([f.id: [RecordingDayHubRow(id: f.id, revision: base, part: 1, next: 1, base: base)]])
        #expect(await sync.syncNow(f.id) == .done)
        #expect(f.hub.calls("POST").last?.body["from"] as? Int == 1)
        #expect(f.hub.texts(f.id) == ["One.", "Two."])
        // Another base: replaced.
        f.clock.advance(120)
        sync.reconciled([f.id: [RecordingDayHubRow(id: f.id, revision: base + 5, part: 1, next: 2, base: base + 5)]])
        #expect(await sync.syncNow(f.id) == .done)
        let repair = try #require(f.hub.requests.last)
        #expect(repair.method == "PUT" && repair.texts == ["One.", "Two."] && repair.revision > base + 5)
        // A list made before the phone's last request says nothing about it.
        let current = try await f.part()
        sync.reconciled([f.id: [RecordingDayHubRow(id: f.id, revision: base, part: 1, next: 0, base: base)]])
        #expect(await sync.syncNow(f.id) == .done)
        #expect(try await f.part() == current)
    }

    @Test func aReconciledBaseEqualToThePendingRepairIsAdopted() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        // A repair was stored on the hub, but its answer was lost before the phone wrote it down.
        let decided: Int64 = 9_000_000_000_000
        await f.store.update(f.id) { $0.parts[0].pendingPut = decided }
        f.clock.advance(120)
        let sent = f.hub.requests.count
        sync.reconciled([f.id: [RecordingDayHubRow(id: f.id, revision: decided, part: 1, next: 1, base: decided)]])
        #expect(await sync.syncNow(f.id) == .done)
        #expect(f.hub.requests.count == sent, "nothing sent again")
        let part = try await f.part()
        #expect(part.base == decided && part.pendingPut == nil && part.next == 1)
    }

    @Test func reconciliationBringsTheRecapsFinalTimeWithoutReadingTheDetail() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        try f.end(at: 10, closed: 10, seqCount: 1)
        let sync = f.sync()
        await sync.syncNow(f.id)
        #expect(try await f.part().complete)
        let base = try await f.part().base
        let finalAt = try #require(RecordingDayWire.date("2027-01-15T13:00:41Z"))
        f.clock.advance(120)
        let row = RecordingDayHubRow(id: f.id, part: 1, next: 1, base: base, recapState: "ready", recapFinal: true, recapFinalAt: finalAt)
        sync.reconciled([f.id: [row]])
        await sync.syncNow(f.id)
        let part = try await f.part()
        #expect(part.recapState == "ready" && part.recapFinal && part.recapFinalAt == finalAt)
        #expect(f.finals == 1, "the keeper's 3-day clock starts")
        #expect(f.hub.calls("GET").isEmpty, "the list says it all")
        // The same rows again change nothing; a row from the day's page without those fields doesn't forget them.
        sync.reconciled([f.id: [row]])
        await sync.whenIdle()
        sync.reconciled([f.id: [RecordingDayHubRow(id: f.id, part: 1, next: 1, base: base, recapState: "ready")]])
        await sync.whenIdle()
        #expect(try await f.part().recapFinalAt == finalAt)
        #expect(f.finals == 1)
    }

    @Test func aRowWithoutABaseIsAnOlderManagersAndGetsNothing() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        let sent = f.hub.requests.count
        sync.reconciled([f.id: [RecordingDayHubRow(id: f.id, part: 1, next: 1)]])
        try await f.chunk(1, at: 10, texts: ["Two."])
        f.clock.advance(60)
        #expect(await sync.syncNow(f.id) == .blocked)
        #expect(f.hub.requests.count == sent)
        #expect(try await f.record().unsupportedUntilHello)
        sync.helloReceived(offered: true)
        #expect(await sync.syncNow(f.id) == .done)
        #expect(f.hub.texts(f.id) == ["One.", "Two."])
    }

    @Test func aDayPastTheHubsWindowIsNotUploaded() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        f.clock.advance(15 * 86_400)
        #expect(await f.sync().syncNow(f.id) == .done)
        #expect(f.hub.requests.isEmpty)
    }

    @Test func aFailingRequestWaitsOutItsBackoffWhateverChangesMeanwhile() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        let sync = f.sync()
        await sync.syncNow(f.id)
        // The hub fails the next two appends. The upload follows the store's changes as in the app, the ones its own
        // bookkeeping makes with every request among them.
        f.hub.fail("POST", refusal(503, "kb_unavailable"))
        f.hub.fail("POST", refusal(503, "kb_unavailable"))
        sync.start()
        f.clock.advance(60)
        try await f.chunk(1, at: 10, texts: ["Two."])
        await settle(sync)
        #expect(f.hub.calls("POST").count == 2, "one failed append, not one for every change it made")
        // New text during the backoff waits for it too.
        try await f.chunk(2, at: 20, texts: ["Three."])
        await settle(sync)
        #expect(f.hub.calls("POST").count == 2)
        // Once the first wait is over it goes again, fails again, and the next wait is twice as long.
        f.clock.advance(RecordingDaySync.backoff(after: 1))
        sync.changed(f.id)
        await settle(sync)
        #expect(f.hub.calls("POST").count == 3)
        f.clock.advance(RecordingDaySync.backoff(after: 1))
        sync.changed(f.id)
        await settle(sync)
        #expect(f.hub.calls("POST").count == 3, "10 s after the second failure, not 5")
        f.clock.advance(RecordingDaySync.backoff(after: 2))
        sync.changed(f.id)
        await settle(sync)
        #expect(f.hub.calls("POST").count == 4)
        #expect(f.hub.texts(f.id).prefix(2) == ["One.", "Two."], "through at last, from the same position")
    }

    @Test func everyRequestsRevisionIsOnDiskBeforeItGoes() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        // What a crash at the moment each request reaches the hub would leave: a revision at least as high.
        let seen = OSAllocatedUnfairLock(initialState: [(sent: Int64, onDisk: Int64)]())
        let root = f.root, id = f.id
        f.hub.watch { request in
            let sent = request.revision
            let onDisk = Fixture.onDisk(root, id)?.parts.first?.maxSentRevision ?? 0
            seen.withLock { $0.append((sent, onDisk)) }
        }
        try await f.chunk(0, at: 0, texts: ["One."])
        let sync = f.sync()
        #expect(await sync.syncNow(f.id) == .done)
        try await f.chunk(1, at: 10, texts: ["Two."])
        f.clock.advance(60)
        f.hub.fail("POST", refusal(503, "kb_unavailable"))
        #expect(await sync.syncNow(f.id) == .failed)
        #expect(await sync.syncNow(f.id) == .done, "the retry")
        try f.end(at: 20, closed: 20, seqCount: 2)
        #expect(await sync.syncNow(f.id) == .done, "an append with no segments: the end")
        let requests = seen.withLock { $0 }
        #expect(f.hub.requests.map(\.method) == ["PUT", "POST", "POST", "POST", "POST"])
        #expect(f.hub.calls("POST").last?.texts.isEmpty == true)
        #expect(requests.count == 5 && requests.allSatisfy { $0.onDisk >= $0.sent })
    }

    @Test func aNewLanguageRepairsTheHubEvenWhenNoTextChanged() async throws {
        for silent in [false, true] {
            let f = Fixture()
            defer { f.cleanUp() }
            try await f.chunk(0, at: 0, texts: silent ? [] : ["Un."])
            try f.end(at: 10, closed: 10, seqCount: 1)
            let sync = f.sync()
            await sync.syncNow(f.id)
            let before = try await f.part()
            #expect(before.complete)
            // Retention took the audio (or there was no speech): nothing is read again, yet the hub's language changes.
            await f.store.removeAudio(f.id, seqs: [0])
            f.library.change(f.id) { $0.locale = "fr-FR" }
            f.clock.advance(60)
            await sync.languageChanged(f.id)
            let decided = try #require(Fixture.onDisk(f.root, f.id)?.parts.first?.pendingPut, "decided, and on disk before it goes")
            #expect(decided > before.maxSentRevision)
            await sync.whenIdle()
            let repair = try #require(f.hub.calls("PUT").last)
            #expect(f.hub.calls("PUT").count == 2)
            #expect(repair.body["language"] as? String == "fr-FR" && repair.revision == decided)
            #expect(repair.texts == (silent ? [] : ["Un."]))
            let after = try await f.part()
            #expect(after.pendingPut == nil && after.base == decided && after.complete)
        }
    }

    @Test func theCatchUpRunningOutLeavesNothingRunning() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        f.app.inForeground = false
        f.app.givesGrants = false
        try await f.chunk(0, at: 0, texts: ["One."])
        let sync = f.sync()
        f.gate.hold()
        let catchUp = Task { await sync.drain() }
        await until { f.gate.arrived == 1 }
        // The task's expiration handler cancels it: the request in flight with it.
        catchUp.cancel()
        #expect(await catchUp.value, "work is left for the next catch-up")
        for _ in 0..<20 { await Task.yield() }
        await sync.whenIdle()
        // A change in the background with no time given starts nothing either.
        sync.changed(f.id)
        await sync.whenIdle()
        #expect(f.gate.arrived == 1 && f.hub.requests.isEmpty, "nothing goes after the task's time ran out")
        #expect(f.app.grants.isEmpty, "and no background time was taken in its place")
        f.gate.release()
        f.app.inForeground = true
        sync.foreground()
        await sync.whenIdle()
        #expect(f.hub.texts(f.id) == ["One."])
    }

    @Test func leavingTheFrontWithoutAGrantStopsThePass() async throws {
        let f = Fixture()
        defer { f.cleanUp() }
        try await f.chunk(0, at: 0, texts: ["One."])
        let sync = f.sync()
        f.gate.hold()
        sync.changed(f.id)
        await until { f.gate.arrived == 1 }
        f.app.inForeground = false
        f.app.givesGrants = false
        sync.background()
        await sync.whenIdle()
        sync.changed(f.id)
        await sync.whenIdle()
        #expect(f.gate.arrived == 1 && f.hub.requests.isEmpty)
        // A chunk arriving in the background brings a grant of its own; the day goes inside it, which then ends.
        f.app.givesGrants = true
        f.gate.release()
        sync.changed(f.id)
        await sync.whenIdle()
        #expect(f.hub.texts(f.id) == ["One."])
        #expect(f.app.grants.count == 1 && f.app.grants[0].ended)
    }
}

/// The upload's pure decisions: positions given once (long sentences split first), the throttle, the clock.
struct RecordingDayUploadTests {
    private func record(_ sentences: [[RecordingSentence]]) -> RecordingDayRecord {
        var record = RecordingDayRecord(id: UUID(), day: "2027-01-15", timeZone: "Asia/Shanghai", startedAt: anchor)
        for (seq, list) in sentences.enumerated() {
            record.chunks[seq] = RecordingDayChunk(seq: seq, startedAt: anchor.addingTimeInterval(Double(seq) * 10), duration: 10,
                                                   receivedAt: anchor, state: .transcribed, sentences: list, hasAudio: true)
        }
        return record
    }

    @Test func placingGivesEachSentenceTheNextPositionOnceAndMakesPartOne() {
        var day = record([[RecordingSentence(start: 1, end: 2, text: "a"), RecordingSentence(start: 3, end: 4, text: "b")],
                          [RecordingSentence(start: 1, end: 2, text: "c")]])
        RecordingDayUpload.place(&day)
        #expect(day.parts.map(\.part) == [1] && day.parts[0].hubID == day.id && day.parts[0].count == 3 && !day.parts[0].created)
        #expect(day.chunks[0]?.positions == [RecordingDayPosition(part: 1, pos: 0), RecordingDayPosition(part: 1, pos: 1)])
        #expect(day.chunks[1]?.positions == [RecordingDayPosition(part: 1, pos: 2)])
        let placed = day
        RecordingDayUpload.place(&day)
        #expect(day == placed, "a position is given once")
        // New text goes to the part that takes it now: the highest the hub hasn't deleted.
        day.parts.append(RecordingDayPart(hubID: UUID(), part: 2, startedAt: anchor))
        day.chunks[2] = RecordingDayChunk(seq: 2, startedAt: anchor.addingTimeInterval(20), duration: 10, receivedAt: anchor,
                                          state: .transcribed, sentences: [RecordingSentence(start: 1, end: 2, text: "d")], hasAudio: true)
        RecordingDayUpload.place(&day)
        #expect(day.chunks[2]?.positions == [RecordingDayPosition(part: 2, pos: 0)] && day.parts[1].count == 1)
    }

    /// A part an offline phone filled past the hub's 20000 segments, whose repair `PUT` the hub refused: packed again
    /// to the hub's limit at once, not to 90% of what was refused (22500, refused again, and again).
    @Test func aRefusedPartOverTheHubsLimitIsPackedToTheLimitAtOnce() {
        let total = RecordingDayUpload.maxPartSegments + 5_000
        var day = record([(0..<total).map { RecordingSentence(start: Double($0) * 0.0004, end: Double($0) * 0.0004, text: "s") }])
        RecordingDayUpload.place(&day)
        #expect(day.parts.map(\.count) == [total])
        day.parts[0].created = true
        #expect(RecordingDayUpload.repartition(&day, from: 1, refused: total, now: anchor, newID: { UUID() }))
        let counts = Dictionary(grouping: day.chunks[0]?.positions ?? [], by: \.part).mapValues(\.count)
        #expect(counts == [1: RecordingDayUpload.maxPartSegments, 2: 5_000], "the first PUT fits")
        #expect(day.parts.sorted { $0.part < $1.part }.map(\.count) == [RecordingDayUpload.maxPartSegments, 5_000])
        // Below the limit it is still fewer than were refused (the hub may count bytes the phone doesn't).
        var small = record([(0..<100).map { RecordingSentence(start: Double($0) * 0.05, end: Double($0) * 0.05, text: "s") }])
        RecordingDayUpload.place(&small)
        #expect(RecordingDayUpload.repartition(&small, from: 1, refused: 100, now: anchor, newID: { UUID() }))
        #expect(small.chunks[0]?.positions?.filter { $0.part == 1 }.count == 90)
    }

    @Test func aSentenceOver64KBIsSplitBeforeItGetsAPosition() throws {
        let word = String(repeating: "x", count: 1000)
        let long = Array(repeating: word, count: 100).joined(separator: " ")   // 100 099 bytes
        var day = record([[RecordingSentence(start: 0, end: 10, text: long)]])
        RecordingDayUpload.place(&day)
        let sentences = try #require(day.chunks[0]?.sentences)
        #expect(sentences.count == 2)
        #expect(sentences.allSatisfy { $0.text.utf8.count <= RecordingDayUpload.maxSentenceBytes })
        #expect(sentences.map(\.text).joined() == long, "nothing lost, cut at a space")
        #expect(sentences[0].text.hasSuffix(" ") && sentences[0].start == 0 && sentences[1].end == 10)
        #expect(sentences[0].end == sentences[1].start)
        #expect(day.chunks[0]?.positions?.count == 2 && day.parts[0].count == 2)
        // Without a space, at a character boundary: multi-byte characters stay whole.
        let wide = String(repeating: "語", count: 30_000)   // 90 000 bytes
        let pieces = RecordingDayUpload.split(RecordingSentence(start: 0, end: 1, text: wide))
        #expect(pieces.count == 2 && pieces.map(\.text).joined() == wide)
        #expect(pieces.allSatisfy { $0.text.utf8.count <= RecordingDayUpload.maxSentenceBytes })
    }

    @Test func newTextWaitsOutTheThrottleAndABacklogGoesOnAtOnce() {
        var day = record([(0..<3).map { RecordingSentence(start: Double($0), end: Double($0) + 0.5, text: "s\($0)") }])
        RecordingDayUpload.place(&day)
        day.parts[0].created = true
        day.parts[0].base = 1
        let now = anchor.addingTimeInterval(100)
        let recent = now.addingTimeInterval(-5)
        func step(_ last: RecordingDayUpload.LastAppend?, force: Bool = false, limit: Int = 500) -> RecordingDayUpload.Step {
            RecordingDayUpload.step(day, partIndex: 0, isLast: true, now: now, lastAppend: last, sentMeta: nil, force: force, limit: limit)
        }
        // All three were placed after an append 5 s ago: they wait for the interval.
        #expect(step(.init(at: recent, count: 0)) == .wait(recent.addingTimeInterval(RecordingDayContract.appendInterval)))
        #expect(step(.init(at: recent, count: 0), limit: 2) == .wait(recent.addingTimeInterval(RecordingDayContract.appendInterval)))
        // They were waiting already at that append (it carried a smaller batch): on at once.
        #expect(step(.init(at: recent, count: 3), limit: 2) == .append(from: 0, to: 2))
        #expect(step(nil) == .append(from: 0, to: 3), "the first append after the create")
        #expect(step(.init(at: recent, count: 0), force: true) == .append(from: 0, to: 3), "the day ended")
        #expect(step(.init(at: now.addingTimeInterval(-RecordingDayContract.appendInterval), count: 0)) == .append(from: 0, to: 3))
        // A hole or a position held twice: only a replacement mends it.
        var holed = day
        holed.chunks[0]?.positions?[1].pos = 5
        #expect(RecordingDayUpload.step(holed, partIndex: 0, isLast: true, now: now, lastAppend: nil, sentMeta: nil, force: true) == .put)
        var repair = day
        repair.parts[0].needsRepair = true
        #expect(RecordingDayUpload.step(repair, partIndex: 0, isLast: true, now: now, lastAppend: nil, sentMeta: nil, force: true) == .put)
    }

    @Test func aSentenceIsOnTheDaysWallClock() {
        let chunk = RecordingDayChunk(seq: 4, startedAt: anchor.addingTimeInterval(3600.5), duration: 10, receivedAt: anchor)
        #expect(RecordingDayUpload.segment(RecordingSentence(start: 1.25, end: 2, text: "x"), of: chunk, since: anchor)
                == RecordingDaySegment(start: 3601.75, end: 3602.5, text: "x"))
        #expect(RecordingDayUpload.segment(RecordingSentence(start: .nan, end: .infinity, text: "y"), of: chunk, since: anchor)
                == RecordingDaySegment(start: 3600.5, end: 3600.5, text: "y"), "a time no JSON carries is the chunk's start")
    }
}
