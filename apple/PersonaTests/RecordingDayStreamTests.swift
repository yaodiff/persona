import AVFoundation
import Foundation
import os
import Testing
@testable import Persona

// All-day recording's watch → phone stream end to end (docs/assistant/day-recap.md「Watch to phone」), with only
// WatchConnectivity replaced by an in-memory link. The watch's real writer encodes synthetic speech and silence into AAC
// chunks in a temporary watch library; the pure planner (`RecordingDayOutbox`) decides what goes and which way, as
// `WatchDayTransfer` would; chunks travel in the real live envelope (`RecordingDayHeader.frame`) or as files with the
// header as metadata, events and acknowledgements as their JSON. The phone's real intake stores and acknowledges them in
// a temporary day store, which applies the control events; its transcription lane (an engine answering after a fixed
// delay) and its upload (to a hub keeping day recordings in memory) take the text the rest of the way.
//
// Deterministic: one wall clock, moved only by the test, drives every deadline (a reply timeout, a backoff), and the
// lanes run when the test says. The only real waits are the measured work; latency is read on the monotonic clock.

private let anchor = Date(timeIntervalSince1970: 1_800_000_000) // 2027-01-15T08:00:00Z, 16:00 in Shanghai
private let zone = "Asia/Shanghai"
/// The midnight that ends 2027-01-15 in Shanghai.
private let shanghaiMidnight = anchor.addingTimeInterval(8 * 3600)
private let liveSeconds = RecordingDayContract.liveChunkSeconds
private let backlogSeconds = RecordingDayContract.backlogChunkSeconds
private typealias Key = RecordingDayOutbox.Key
private typealias Action = RecordingDayOutbox.Action
private typealias Reason = RecordingDayContract.Reason

// MARK: - Clocks

/// The wall clock both devices read: the watch's planner (`plan(now:)`), the phone's store (`receivedAt`), the lane and
/// the upload. Only the test moves it, and never back.
private final class WorldClock: Sendable {
    private let value: OSAllocatedUnfairLock<Date>

    init(_ start: Date) { value = OSAllocatedUnfairLock(initialState: start) }

    var now: Date { value.withLock { $0 } }

    func set(_ date: Date) { value.withLock { $0 = Swift.max($0, date) } }
}

/// Per chunk, the first moment on the monotonic clock it reached each stage: finished by the writer (`ready`), its
/// acknowledgement applied on the watch, the engine done with the run it was in (`transcribed`, each run its own
/// moment; the lane saves the run's text right after), its text stored by the hub. Written from the main actor, the
/// engine and the upload's requests.
private final class Stamps: Sendable {
    enum Stage: Hashable, Sendable { case ready, acked, transcribed, appended }

    private let moments = OSAllocatedUnfairLock(initialState: [Stage: [Int: ContinuousClock.Instant]]())

    func mark(_ stage: Stage, _ seq: Int) {
        let now = ContinuousClock.now
        moments.withLock { moments in
            if moments[stage]?[seq] == nil { moments[stage, default: [:]][seq] = now }
        }
    }

    func moment(_ stage: Stage, _ seq: Int) -> ContinuousClock.Instant? { moments.withLock { $0[stage]?[seq] } }
}

/// One `LATENCY` line: per-chunk milliseconds as p50, p95 (nearest rank) and the maximum.
private struct Latency {
    let label: String
    let millis: [Double]

    init(_ label: String, _ millis: [Double]) {
        self.label = label
        self.millis = millis.sorted()
    }

    /// Infinite without readings, so a bound on it fails.
    func percentile(_ p: Double) -> Double {
        guard !millis.isEmpty else { return .infinity }
        let rank = Int((p * Double(millis.count)).rounded(.up))
        return millis[Swift.min(Swift.max(rank, 1), millis.count) - 1]
    }

    var p50: Double { percentile(0.5) }
    var p95: Double { percentile(0.95) }
    var maximum: Double { millis.last ?? .infinity }

    var line: String {
        "LATENCY \(label) p50=\(Self.text(p50)) p95=\(Self.text(p95)) max=\(Self.text(maximum)) n=\(millis.count)"
    }

    static func millis(_ duration: Duration) -> Double {
        let parts = duration.components
        return Double(parts.seconds) * 1_000 + Double(parts.attoseconds) / 1e15
    }

    private static func text(_ millis: Double) -> String { String(format: "%.1fms", millis) }
}

// MARK: - The microphone

/// What the watch's microphone tap hands the writer (16 kHz mono float): speech-like sound (a 140 Hz voice with two
/// overtones whose loudness rises and falls four times a second, like syllables, over a little room noise: about
/// −25 dBFS) or a quiet room (noise near −75 dBFS, far below `RecordingDayContract.silenceDBFS`, so the phone skips it).
/// Seeded, so every run encodes the same samples.
private enum Microphone {
    static func buffer(seconds: Double, speech: Bool) throws -> AVAudioPCMBuffer {
        let rate = RecordingAudioSpec.sampleRate
        let count = Int((seconds * rate).rounded())
        let format = try #require(AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: rate, channels: 1, interleaved: false))
        let buffer = try #require(AVAudioPCMBuffer(pcmFormat: format, frameCapacity: AVAudioFrameCount(count)))
        let samples = try #require(buffer.floatChannelData?[0])
        var noise = SplitMix64(state: speech ? 1 : 2)
        let room = speech ? 0.003 : 0.000_3
        let tau = 2 * Double.pi
        for i in 0..<count {
            let t = Double(i) / rate
            var sample = (noise.unit() * 2 - 1) * room
            if speech {
                let syllables = 0.5 * (1 - cos(tau * 4 * t))
                let voice = sin(tau * 140 * t) + 0.5 * sin(tau * 280 * t) + 0.25 * sin(tau * 420 * t)
                sample += 0.12 * syllables * voice
            }
            samples[i] = Float(sample)
        }
        buffer.frameLength = AVAudioFrameCount(count)
        return buffer
    }
}

/// A small seeded generator (SplitMix64): the same noise every run.
private struct SplitMix64 {
    var state: UInt64

    /// Uniform in 0..<1.
    mutating func unit() -> Double {
        state &+= 0x9E37_79B9_7F4A_7C15
        var z = state
        z = (z ^ (z >> 30)) &* 0xBF58_476D_1CE4_E5B9
        z = (z ^ (z >> 27)) &* 0x94D0_49BB_1331_11EB
        return Double((z ^ (z >> 31)) >> 11) / Double(UInt64(1) << 53)
    }
}

// MARK: - The watch

/// The watch: a day recording in a temporary library, the real writer making its chunks as the capture drives it, and
/// what `WatchDayTransfer` reads and does around the planner (the items, applying the phone's acknowledgements).
@MainActor private final class Watch {
    /// A chunk or an event the phone has not acknowledged, with what its send needs (`WatchDayTransfer.Work`).
    struct Work {
        enum Payload {
            case chunk(RecordingDayHeader, URL)
            case event(RecordingDayEventEnvelope)
        }

        let item: RecordingDayOutbox.Item
        let payload: Payload
    }

    let library: RecordingLibrary
    let id: UUID
    var outbox = RecordingDayOutbox()
    /// Chunk messages sent whose reply has not come (`WatchDayTransfer.inFlight`).
    var inFlight: Set<Key> = []
    /// The capture records into the day and holds it (the ledger's `recordingInto` and `holding`); a stop lets go.
    private(set) var capturing = true
    /// Where the next sample falls on the wall clock.
    private(set) var cursor: Date
    /// Each chunk's file as the writer finished it, to compare with the phone's copy.
    private(set) var audio: [Int: Data] = [:]
    /// Each chunk's live envelope size (`RecordingDayHeader.frame`), from its file and header.
    private(set) var envelopeBytes: [Int: Int] = [:]
    /// How many acknowledgements named each seq.
    private(set) var ackCount: [Int: Int] = [:]
    private let writer: RecordingSegmentWriter

    init(root: URL, startingAt start: Date) throws {
        let library = RecordingLibrary(root: root)
        let id = UUID()
        let tz = try #require(TimeZone(identifier: zone))
        // As the capture opens a day: the entry with its zone, day and counters before the microphone, then `started`.
        try library.create(source: .watch, kind: .day, id: id, createdAt: start,
                           day: RecordingDayCalendar.day(of: start, in: tz), timeZone: zone)
        library.appendDayEvent(id) { .started($0, at: start) }
        // Each file's seq comes from the library's counter, on the writer's queue, before the file opens.
        let writer = RecordingSegmentWriter(directory: library.folder(id), allocateSeq: { folder in
            UUID(uuidString: folder.lastPathComponent).map { library.allocateSeq($0) }
        })
        writer.segmentLength = liveSeconds
        writer.compactsFiles = true
        writer.cutsAtGaps = true
        self.library = library
        self.id = id
        self.writer = writer
        cursor = start
    }

    /// The chunk length: 10 s while the phone is reachable, a minute otherwise (the capture's `linkReachable`).
    var chunkSeconds: Double {
        get { writer.segmentLength }
        set { writer.segmentLength = newValue }
    }

    var ledger: RecordingDayWatchLedger {
        RecordingDayWatchLedger(library: library, recordingInto: capturing ? id : nil, holding: capturing ? id : nil)
    }

    // MARK: Capture

    /// Hands the writer `seconds` of microphone audio from the cursor on; returns the chunks it finished.
    func feed(_ buffer: AVAudioPCMBuffer, seconds: Double) -> [RecordingSegmentWriter.Segment] {
        writer.append(buffer, startedAt: cursor)
        cursor = cursor.addingTimeInterval(seconds)
        return writer.takeEvents().compactMap(\.segment)
    }

    /// Saves finished chunks as the capture does (`RecordingCapture.register`): a record with its level.
    func register(_ segments: [RecordingSegmentWriter.Segment]) {
        for segment in segments {
            let data = try? Data(contentsOf: segment.url)
            var record = RecordingSegment(index: segment.index, start: segment.start, duration: segment.duration,
                                          startedAt: segment.startedAt)
            record.level = segment.level
            audio[segment.index] = data
            if let data, let entry = library.entry(id), let header = RecordingDayHeader(entry: entry, chunk: record) {
                envelopeBytes[segment.index] = RecordingDayOutbox.frameBytes(audio: data.count, header: header)
            }
            library.addSegment(id, record)
        }
    }

    /// The microphone closes (`paused`, and why): the chunk being written is cut and saved first, and the event is at the
    /// end of the audio, as the capture writes it.
    func micClosed(_ reason: String, now: Date) {
        writer.cut()
        register(writer.takeEvents().compactMap(\.segment))
        let at = RecordingCapture.pauseTime(audioEnd: writer.audioEnd, now: now)
        library.appendDayEvent(id) { .paused($0, at: at, reason: reason) }
    }

    /// Time passes with the microphone closed.
    func wait(_ seconds: Double) {
        cursor = cursor.addingTimeInterval(seconds)
    }

    /// The microphone opens again (`started`).
    func micOpened() {
        let at = cursor
        library.appendDayEvent(id) { .started($0, at: at) }
    }

    /// The user turns All-Day Recording off: a pause, and the capture lets go of the day.
    func stop(now: Date) {
        micClosed(Reason.paused, now: now)
        capturing = false
    }

    /// A bookmark now [ext]. Returns its time.
    @discardableResult func mark() -> Date {
        let at = cursor
        library.appendDayEvent(id) { .mark($0, at: at, label: RecordingDayEvent.Label.bookmark) }
        return at
    }

    /// Erases `window` as the watch's Erase does (`RecordingCapture.eraseDayRecent`): the chunk being written is cut and
    /// saved first, then `RecordingCapture.erase` saves the `erase` event naming the chunks it deletes, and deletes them.
    /// Returns those seqs: the ones the phone had not acknowledged.
    func erase(_ window: RecordingErase, eventID: UUID) -> [Int] {
        _ = writer.cutForErase()
        register(writer.takeEvents().compactMap(\.segment))
        #expect(RecordingCapture.erase(window, ofDay: id, in: library, eventID: eventID), "the erase event was saved")
        return library.entry(id)?.dayEvents?.last(where: { $0.id == eventID })?.erasedSeqs ?? []
    }

    // MARK: Transport

    /// Everything the phone has not acknowledged, as `WatchDayTransfer.waitingWork` lists it: the events, then the
    /// chunks, each envelope's size measured from its file.
    func waiting() -> [Key: Work] {
        guard let entry = library.entry(id) else { return [:] }
        var work: [Key: Work] = [:]
        for event in entry.dayEvents ?? [] {
            guard let envelope = RecordingDayEventEnvelope(entry: entry, event: event) else {
                Issue.record("event \(event.n) has no envelope")
                continue
            }
            let item = RecordingDayOutbox.item(envelope)
            work[item.key] = Work(item: item, payload: .event(envelope))
        }
        for record in entry.segments {
            let url = library.audio(id, record.index)
            let size = (try? FileManager.default.attributesOfItem(atPath: url.path))?[.size] as? Int
            guard let header = RecordingDayHeader(entry: entry, chunk: record), let size,
                  let item = RecordingDayOutbox.item(record, of: id, frameBytes: RecordingDayOutbox.frameBytes(audio: size, header: header)) else {
                Issue.record("chunk \(record.index) can't go to the phone")
                continue
            }
            work[item.key] = Work(item: item, payload: .chunk(header, url))
        }
        return work
    }

    /// The watch still holds this chunk or event (`WatchDayTransfer.isHeld`).
    func holds(_ key: Key) -> Bool {
        guard let entry = library.entry(key.recording) else { return false }
        if key.isEvent { return (entry.dayEvents ?? []).contains { $0.n == key.number } }
        return entry.segments.contains { $0.index == key.number }
    }

    /// Anything of the day still waits for the phone.
    var holdsAnything: Bool {
        guard let entry = library.entry(id) else { return false }
        return !entry.segments.isEmpty || !(entry.dayEvents ?? []).isEmpty
    }

    /// The chunks the watch still holds, by seq.
    var heldSeqs: [Int] { library.entry(id)?.segments.map(\.index).sorted() ?? [] }

    /// What the watch still holds, for failure messages.
    var heldDescription: String {
        guard let entry = library.entry(id) else { return "nothing" }
        return "seqs \(heldSeqs), events \((entry.dayEvents ?? []).map(\.n))"
    }

    /// The day's audio files still on the watch.
    var audioFiles: [String] {
        let names = (try? FileManager.default.contentsOfDirectory(atPath: library.folder(id).path)) ?? []
        return names.filter { $0.hasSuffix(".m4a") }
    }

    /// The phone's acknowledgement, as `WatchDayTransfer.apply` takes it: the ledger deletes the chunks and forgets the
    /// events, the outbox forgets them, and a closed day the phone has all of goes. Returns the seqs the watch still held.
    @discardableResult func apply(_ ack: RecordingDayAck, now: Date) -> [Int] {
        for seq in ack.seqs { ackCount[seq, default: 0] += 1 }
        let held = ledger.apply(ack).seqs
        let keys = ack.seqs.map { Key.chunk(ack.recording, seq: $0) } + ack.eventNumbers.map { Key.event(ack.recording, n: $0) }
        outbox.acked(keys)
        inFlight.subtract(keys)
        ledger.removeSettled(now: now)
        return held
    }
}

// MARK: - The link

/// WatchConnectivity between the two, in memory: live messages (while the phone is reachable) and the system's queues of
/// file and user info transfers (delivered while `systemDelivers`, in the order queued), both ways. Faults are injected
/// per chunk or event and fire once: a reply that never comes back, a message or file that reaches the phone twice, a
/// cancelled transfer the system delivers anyway. A chunk's acknowledgements can also be lost for a while
/// (`loseAcks`): until `deliverAcks`, the watch hears none, however they come. Everything the watch sends is logged
/// with its route, and every arrival at the phone counted.
@MainActor private final class Link {
    enum Route: Equatable { case message, file, userInfo, cancel }
    enum Fault: Hashable { case dropReply, deliverTwice, deliverDespiteCancel }

    enum Payload {
        /// A chunk's live envelope (`sendMessageData`).
        case chunk(Data)
        /// An event's message (`sendMessage`, JSON under `eventKey`).
        case event([String: Data])
    }

    enum Reply {
        case chunk(Data)
        case event([String: Data])
    }

    struct Message {
        let key: Key
        let payload: Payload
    }

    struct FileTransfer {
        let key: Key
        /// What the system took of the file when it was queued.
        let bytes: Data
        let metadata: [String: Data]
        var cancelled = false
    }

    struct UserInfo {
        let key: Key
        let info: [String: Data]
    }

    var reachable = true
    var systemDelivers = true
    private(set) var log: [(key: Key, route: Route)] = []
    private(set) var arrivals: [Key: Int] = [:]
    /// The wall-clock time each chunk or event first reached the phone.
    private(set) var firstArrival: [Key: Date] = [:]
    private var faults: [Fault: Set<Key>] = [:]
    private var acksLost: Set<Key> = []
    private var messages: [Message] = []
    private var files: [FileTransfer] = []
    private var userInfos: [UserInfo] = []
    /// The phone's acknowledgements sent while the watch app wasn't reachable: user info, delivered with the system's
    /// queues.
    private var forWatch: [[String: Data]] = []

    func inject(_ fault: Fault, _ keys: Key...) {
        faults[fault, default: []].formUnion(keys)
    }

    /// Whether `fault` is set for `key`; it fires once.
    func fires(_ fault: Fault, _ key: Key) -> Bool {
        faults[fault]?.remove(key) != nil
    }

    /// From now on every acknowledgement of this chunk is lost on its way to the watch: the replies to its messages,
    /// and its seq in any other acknowledgement.
    func loseAcks(of key: Key) { acksLost.insert(key) }

    /// Its acknowledgements get through again.
    func deliverAcks(of key: Key) { acksLost.remove(key) }

    /// Whether the reply to this chunk's message is lost.
    func losesReply(_ key: Key) -> Bool { acksLost.contains(key) }

    /// What of `ack` reaches the watch: all of it but the seqs whose acknowledgements are lost.
    func passing(_ ack: RecordingDayAck) -> RecordingDayAck {
        let lost = Set(acksLost.filter { !$0.isEvent && $0.recording == ack.recording }.map(\.number))
        guard !lost.isEmpty else { return ack }
        return RecordingDayAck(recording: ack.recording, seqs: ack.seqs.filter { !lost.contains($0) }, events: ack.eventNumbers)
    }

    // MARK: The watch sends

    func sendMessage(_ key: Key, _ payload: Payload) {
        log.append((key: key, route: .message))
        messages.append(Message(key: key, payload: payload))
    }

    func transferFile(_ key: Key, bytes: Data, metadata: [String: Data]) {
        log.append((key: key, route: .file))
        files.append(FileTransfer(key: key, bytes: bytes, metadata: metadata))
    }

    func transferUserInfo(_ key: Key, _ info: [String: Data]) {
        log.append((key: key, route: .userInfo))
        userInfos.append(UserInfo(key: key, info: info))
    }

    /// `WCSessionFileTransfer.cancel()`: the system drops the transfer and later reports it failed.
    func cancel(_ key: Key) {
        log.append((key: key, route: .cancel))
        for i in files.indices where files[i].key == key { files[i].cancelled = true }
    }

    /// What the system still holds (`outstandingFileTransfers`, `outstandingUserInfoTransfers`), one transfer per chunk or
    /// event; those of anything that doesn't wait any more, or a second one of the same chunk, are cancelled, as
    /// `WatchDayTransfer` cancels them.
    func outstanding(waiting: Set<Key>) -> Set<Key> {
        var held = Set<Key>()
        for i in files.indices where !files[i].cancelled {
            if waiting.contains(files[i].key), held.insert(files[i].key).inserted { continue }
            files[i].cancelled = true
        }
        userInfos.removeAll { !waiting.contains($0.key) }
        held.formUnion(userInfos.map(\.key))
        return held
    }

    // MARK: The system delivers

    func takeMessages() -> [Message] {
        defer { messages = [] }
        return messages
    }

    func takeFiles() -> [FileTransfer] {
        defer { files = [] }
        return files
    }

    func takeUserInfos() -> [UserInfo] {
        defer { userInfos = [] }
        return userInfos
    }

    /// One queued user info, ahead of the others.
    func takeUserInfo(_ key: Key) -> UserInfo? {
        guard let index = userInfos.firstIndex(where: { $0.key == key }) else { return nil }
        return userInfos.remove(at: index)
    }

    func queueForWatch(_ info: [String: Data]) {
        forWatch.append(info)
    }

    func takeForWatch() -> [[String: Data]] {
        defer { forWatch = [] }
        return forWatch
    }

    func arrived(_ key: Key, at time: Date) {
        arrivals[key, default: 0] += 1
        if firstArrival[key] == nil { firstArrival[key] = time }
    }

    // MARK: Reading

    /// Every way `key` went (or was cancelled), in order.
    func routes(_ key: Key) -> [Route] { log.filter { $0.key == key }.map { $0.route } }

    /// Chunks in the order they first went `route`.
    func order(_ route: Route) -> [Int] {
        var seen = Set<Int>()
        return log.filter { $0.route == route && !$0.key.isEvent && seen.insert($0.key.number).inserted }.map { $0.key.number }
    }

    /// The chunks whose file transfer the system holds, in the order queued.
    var queuedFiles: [Int] { files.filter { !$0.cancelled }.map(\.key.number) }
    /// The events whose user info the system holds, in the order queued.
    var queuedEvents: [Int] { userInfos.map(\.key.number) }
}

// MARK: - The phone

/// The engine of the phone's lane: one sentence per chunk file, named after its seq, after a fixed delay that stands in
/// for the analyzer (the measured work). Each file is first opened as AAC and its length checked against its header, so
/// audio damaged anywhere on the way is a problem the test reports. As it hands a run back, its chunks are stamped
/// `transcribed`: a backlog's runs go one after the other, and each chunk's reading is its own run's.
private final class FakeEngine: Sendable {
    static let delay = Duration.milliseconds(5)
    /// AAC packets are 1024 samples; the writer's files are within two of the header's length.
    private static let frameSlack: AVAudioFramePosition = 2_048

    private let stamps: Stamps
    private let state = OSAllocatedUnfairLock(initialState: (seqs: [Int](), problems: [String]()))

    init(stamps: Stamps) { self.stamps = stamps }

    /// The seqs transcribed, in order.
    var seqs: [Int] { state.withLock { $0.seqs } }
    var problems: [String] { state.withLock { $0.problems } }

    static func text(_ seq: Int) -> String { "chunk \(seq)" }
    static func seq(of text: String) -> Int? { text.hasPrefix("chunk ") ? Int(text.dropFirst(6)) : nil }

    func run(_ urls: [URL], _ durations: [Double]) async throws -> [(file: Int, sentence: RecordingSentence)] {
        let seqs = urls.map { RecordingSegment.index(fileName: $0.lastPathComponent) ?? -1 }
        var found: [(file: Int, sentence: RecordingSentence)] = []
        for (file, url) in urls.enumerated() {
            let seconds = durations.indices.contains(file) ? durations[file] : 0
            check(url, seconds: seconds)
            found.append((file: file, sentence: RecordingSentence(start: 1, end: Swift.min(2, seconds), text: Self.text(seqs[file]))))
        }
        try await Task.sleep(for: Self.delay)
        state.withLock { $0.seqs += seqs }
        for seq in seqs { stamps.mark(.transcribed, seq) }
        return found
    }

    private func check(_ url: URL, seconds: Double) {
        let expected = AVAudioFramePosition((seconds * RecordingAudioSpec.sampleRate).rounded())
        let frames = (try? AVAudioFile(forReading: url))?.length
        if let frames, abs(frames - expected) <= Self.frameSlack { return }
        let read = frames.map { "\($0)" } ?? "unreadable"
        state.withLock { $0.problems.append("\(url.lastPathComponent): \(read) of \(expected) frames") }
    }
}

/// A hub that keeps day recordings as docs/assistant/day-recap.md「Phone to hub」says: a `PUT` replaces every segment and
/// sets the base (its `200` carries `base` and `next`); an append stores by position on that base, skipping positions it
/// holds. Each text the first time it is stored is stamped `appended`, its chunk read off the text the engine wrote. Every
/// request is kept with its body.
private final class FakeDayHub: @unchecked Sendable {
    private struct Held {
        var base: Int64
        var revision: Int64
        var texts: [String]
    }

    private static let address = URL(string: "https://hub.invalid")!

    private let lock = NSLock()
    private let stamps: Stamps
    private var held: [String: Held] = [:]
    private var log: [(method: String, id: String, body: [String: Any])] = []

    init(stamps: Stamps) { self.stamps = stamps }

    var client: APIClient {
        var client = APIClient(config: ServerConfig(url: Self.address, token: "t"))
        client.transport = { [self] request in try self.answer(request) }
        return client
    }

    /// The texts the hub holds for a recording, by position.
    func texts(_ id: UUID) -> [String] { lock.withLock { held[Self.key(id)]?.texts ?? [] } }

    /// The bodies of a recording's `method` requests, oldest first.
    func bodies(_ id: UUID, _ method: String) -> [[String: Any]] {
        lock.withLock { log.filter { $0.id == Self.key(id) && $0.method == method }.map { $0.body } }
    }

    /// The newest request's body for a recording: a day `PUT` and every append carry the day's fields whole.
    func latest(_ id: UUID) -> [String: Any]? { lock.withLock { log.last(where: { $0.id == Self.key(id) })?.body } }

    private func answer(_ request: LocalRequest) throws -> Data {
        let parts = request.path.split(separator: "/").map(String.init)
        let body = (request.body.flatMap { try? JSONSerialization.jsonObject(with: $0) } as? [String: Any]) ?? [:]
        guard parts.count >= 4, parts[0..<3] == ["api", "v1", "recordings"] else {
            throw APIError(status: 404, message: "FakeDayHub: no route for \(request.method) \(request.path)")
        }
        return try lock.withLock {
            log.append((method: request.method, id: parts[3], body: body))
            switch (request.method, parts.count) {
            case ("PUT", 4): return try put(parts[3], body)
            case ("POST", 5) where parts[4] == "segments": return try append(parts[3], body)
            default: throw APIError(status: 404, message: "FakeDayHub: no route for \(request.method) \(request.path)")
            }
        }
    }

    private func put(_ id: String, _ body: [String: Any]) throws -> Data {
        let revision = Self.int64(body["revision"])
        if let have = held[id], have.revision > revision {
            return try Self.json(["id": id, "revision": have.revision, "base": have.base, "next": have.texts.count, "status": "stale"])
        }
        let texts = Self.texts(body)
        held[id] = Held(base: revision, revision: revision, texts: texts)
        stamp(texts)
        return try Self.json(["id": id, "revision": revision, "base": revision, "next": texts.count, "status": "stored"])
    }

    private func append(_ id: String, _ body: [String: Any]) throws -> Data {
        guard var have = held[id] else { throw Self.refusal(404, "recording_not_found") }
        let base = Self.int64(body["base"]), revision = Self.int64(body["revision"])
        let from = (body["from"] as? NSNumber)?.intValue ?? 0
        guard revision > base else { throw Self.refusal(400) }
        guard base == have.base else {
            throw Self.refusal(409, "base_changed", ["base": "\(have.base)", "next": "\(have.texts.count)"])
        }
        guard from >= 0, from <= have.texts.count else { throw Self.refusal(409, "segments_gap", ["next": "\(have.texts.count)"]) }
        let new = Array(Self.texts(body).dropFirst(have.texts.count - from))
        have.texts += new
        have.revision = Swift.max(have.revision, revision)
        held[id] = have
        stamp(new)
        return try Self.json(["id": id, "revision": have.revision, "next": have.texts.count, "status": "stored"])
    }

    private func stamp(_ texts: [String]) {
        for seq in texts.compactMap(FakeEngine.seq(of:)) { stamps.mark(.appended, seq) }
    }

    private static func texts(_ body: [String: Any]) -> [String] {
        (body["segments"] as? [[String: Any]] ?? []).map { $0["text"] as? String ?? "" }
    }

    private static func int64(_ value: Any?) -> Int64 { (value as? NSNumber)?.int64Value ?? 0 }
    private static func key(_ id: UUID) -> String { id.uuidString.lowercased() }
    private static func json(_ object: [String: Any]) throws -> Data { try JSONSerialization.data(withJSONObject: object) }

    private static func refusal(_ status: Int, _ code: String? = nil, _ extra: [String: String] = [:]) -> APIError {
        var fields = extra
        if let code { fields["code"] = code }
        return APIError(status: status, message: "refused", fields: fields)
    }
}

/// What a callback was given, from any queue.
private final class Collected<Element: Sendable>: Sendable {
    private let items = OSAllocatedUnfairLock(initialState: [Element]())

    func append(_ item: Element) { items.withLock { $0.append(item) } }

    func take() -> [Element] {
        items.withLock { items in
            let taken = items
            items = []
            return taken
        }
    }
}

/// The iPhone: the day store over a temporary recordings folder with the library's index rows, the real intake as
/// WatchConnectivity calls it (on a queue of its own, as the session's delegate queue, never the main actor), the
/// transcription lane with the fake engine, and the upload to the fake hub.
@MainActor private final class Phone {
    let library: RecordingLibrary
    let store: RecordingDayStore
    let engine: FakeEngine
    let hub: FakeDayHub
    private let intake: RecordingDayIntake
    private let lane: RecordingDayTranscriber
    private let upload: RecordingDaySync
    private let defaultsSuite: String
    private let connectivity = DispatchQueue(label: "day-stream.watch-connectivity")
    /// Acknowledgements the phone sent on its own (`WatchSync.sendDayAck`).
    private let sentAcks: Collected<RecordingDayAck>
    /// Days the intake changed (`RecordingDayReceiver.arrived`).
    private let changed: Collected<UUID>

    init(root: URL, clock: WorldClock, stamps: Stamps) throws {
        let library = RecordingLibrary(root: root)
        // `day.json`'s coalesced write never comes during a test: only the upload's `flush` writes it.
        let store = RecordingDayStore(root: root, isDeleted: { library.isDeletedSnapshot($0) }, now: { clock.now }, writeDelay: 3600)
        let sentAcks = Collected<RecordingDayAck>(), changed = Collected<UUID>()
        let engine = FakeEngine(stamps: stamps), hub = FakeDayHub(stamps: stamps)
        let suite = "day-stream-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        // The file acknowledgements' gathering (`ackDelay`) ends when the test says (`ackDelayElapsed`), not on a timer.
        intake = RecordingDayIntake(store: store, sendAck: { ack in
            if !ack.isEmpty { sentAcks.append(ack) }
        }, arrived: { changed.append($0) }, ackDelay: 3600)
        // The lane's clock runs `settleSeconds` ahead, so a run never waits for chunks after it: how long a run waits to
        // join its neighbours is `RecordingDayRuns`' (RecordingDayTranscriberTests); here only the work is timed.
        // The lane's and the upload's seams (`Engine`, `changed`, `whenIdle`, `syncNow(_:force:)`, and every hook the
        // upload calls into the app with) are the ones the app's shared instances use.
        lane = RecordingDayTranscriber(store: store, library: library,
                                       engine: { urls, durations, _, _, _ in try await engine.run(urls, durations) },
                                       clipsIdle: {}, now: { clock.now.addingTimeInterval(RecordingDayRuns.settleSeconds) },
                                       appState: RecordingDayForeground(), defaults: defaults, languageChanged: { _ in })
        let client = hub.client
        // Every hook into the app is a no-op: the upload's defaults reach the app's shared syncs.
        upload = RecordingDaySync(store: store, library: library, api: { client }, features: { [RecordingDayContract.feature] },
                                  now: { clock.now }, audioDays: { 14 }, appState: RecordingDayForeground(),
                                  noteParts: { _, _ in }, removed: { _ in }, removeEverywhere: { _ in }, recapFinal: {},
                                  debounce: .zero)
        self.library = library
        self.store = store
        self.engine = engine
        self.hub = hub
        self.sentAcks = sentAcks
        self.changed = changed
        defaultsSuite = suite
    }

    func cleanUp() {
        UserDefaults.standard.removePersistentDomain(forName: defaultsSuite)
    }

    func record(_ id: UUID) async throws -> RecordingDayRecord {
        try #require(await store.record(id))
    }

    // MARK: WatchConnectivity's callbacks

    /// `didReceiveMessageData`: the intake stores the chunk, and the reply is its acknowledgement (`WatchSync.onDayChunk`).
    func receiveMessageData(_ frame: Data) -> Data {
        let intake = intake
        return onConnectivityQueue { intake.receive(frame: frame) }
    }

    /// `didReceive file`: the header read from the metadata, the file moved in before this returns (`WatchSync.onDayFile`).
    func receiveFile(_ file: URL, metadata: [String: Data]) {
        let intake = intake
        onConnectivityQueue {
            guard let header = RecordingDayHeader(userInfo: metadata) else { return }
            _ = intake.receive(file: file, header: header)
        }
    }

    /// An event by message, acknowledged in the reply, or by user info, acknowledged on its own (`sendDayAck`), as
    /// `WatchSync.receiveDayEvent` routes it.
    func receiveEvent(_ info: [String: Data], replying: Bool) -> [String: Data] {
        let intake = intake, sentAcks = sentAcks
        return onConnectivityQueue {
            guard let envelope = RecordingDayEventEnvelope(data: info[RecordingDayContract.eventKey]),
                  let ack = intake.receive(event: envelope) else { return [:] }
            guard replying else {
                if !ack.isEmpty { sentAcks.append(ack) }
                return [:]
            }
            return [RecordingDayContract.ackKey: ack.data]
        }
    }

    /// The gathering of file acknowledgements is over: they go now.
    func ackDelayElapsed() {
        let intake = intake
        onConnectivityQueue { intake.flushAcks() }
    }

    /// The acknowledgements the phone sent on its own since the last call, oldest first.
    func takeAcks() -> [RecordingDayAck] { sentAcks.take() }

    nonisolated private func onConnectivityQueue<T: Sendable>(_ work: @Sendable () -> T) -> T {
        connectivity.sync(execute: work)
    }

    // MARK: What follows the store

    /// What follows the intake in the app (`RecordingDayReceiver.arrived`, the lane and the upload following
    /// `RecordingDayStore.changes`), run here in order: each changed day gets its library row, its chunks transcribed
    /// (the engine stamps each run), and its text uploaded. The upload goes once the lane is idle, so a backlog's text
    /// goes up together, as the app's does when its runs end within the upload's one-second debounce (a fixed wait,
    /// left out like the other timers).
    func work() async {
        let ids = Set(changed.take()).sorted { $0.uuidString < $1.uuidString }
        guard !ids.isEmpty else { return }
        for id in ids {
            await adoptRow(id)
            lane.changed(id)
        }
        await lane.whenIdle()
        for id in ids { await upload.syncNow(id, force: true) }
    }

    /// `RecordingDayReceiver.adoptRow`: the library's small index row of the day.
    private func adoptRow(_ id: UUID) async {
        guard let record = await store.record(id) else { return }
        library.adoptDay(id: id, day: record.day, timeZone: record.timeZone, createdAt: record.startedAt,
                         closed: record.closedAt != nil)
    }
}

// MARK: - The loop

/// The watch, the link and the phone on one wall clock: recording on the watch, its transport's passes
/// (`WatchDayTransfer.run` without the session), the link's deliveries, and the phone's lanes, each step when the test
/// says.
@MainActor private final class Loopback {
    let root: URL
    let clock: WorldClock
    let stamps: Stamps
    let watch: Watch
    let link: Link
    let phone: Phone
    private let inbox: URL
    private var sounds: [String: AVAudioPCMBuffer] = [:]

    /// Rounds of passes and deliveries before the transport counts as stuck, and wakes before the drain does.
    private static let maxRounds = 20
    private static let maxWakes = 40

    init(startingAt start: Date) throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent("day-stream-\(UUID().uuidString)", isDirectory: true)
        let clock = WorldClock(start), stamps = Stamps()
        self.root = root
        self.clock = clock
        self.stamps = stamps
        inbox = root.appendingPathComponent("inbox", isDirectory: true)
        watch = try Watch(root: root.appendingPathComponent("watch", isDirectory: true), startingAt: start)
        link = Link()
        phone = try Phone(root: root.appendingPathComponent("phone", isDirectory: true), clock: clock, stamps: stamps)
        try FileManager.default.createDirectory(at: inbox, withIntermediateDirectories: true)
    }

    func cleanUp() {
        phone.cleanUp()
        try? FileManager.default.removeItem(at: root)
    }

    // MARK: Recording

    /// `seconds` of speech or of a quiet room recorded on the watch. The wall clock moves to the end of it; each chunk
    /// the writer finished is stamped `ready` at once, then saved. Returns their seqs.
    @discardableResult func record(_ seconds: Double, speech: Bool) throws -> [Int] {
        let segments = watch.feed(try sound(seconds, speech: speech), seconds: seconds)
        for segment in segments { stamps.mark(.ready, segment.index) }
        watch.register(segments)
        clock.set(watch.cursor)
        return segments.map(\.index)
    }

    /// Time passes on the watch with the microphone closed.
    func wait(_ seconds: Double) {
        watch.wait(seconds)
        clock.set(watch.cursor)
    }

    private func sound(_ seconds: Double, speech: Bool) throws -> AVAudioPCMBuffer {
        let key = "\(seconds)-\(speech)"
        if let cached = sounds[key] { return cached }
        let buffer = try Microphone.buffer(seconds: seconds, speech: speech)
        sounds[key] = buffer
        return buffer
    }

    // MARK: Reachability

    /// The phone goes out of reach, and out of range: neither messages nor the system's transfers get through.
    func disconnect() {
        link.reachable = false
        link.systemDelivers = false
        watch.outbox.reachabilityChanged(false)
    }

    /// The phone is in reach again (`WatchDayTransfer.refreshReachability`). `systemDelivers`: whether the system's
    /// queued transfers get through yet. Returns the moment, on the monotonic clock.
    @discardableResult func reconnect(systemDelivers: Bool = true) -> ContinuousClock.Instant {
        link.reachable = true
        link.systemDelivers = systemDelivers
        watch.outbox.reachabilityChanged(true)
        return .now
    }

    // MARK: The watch's transport

    /// One pass of the watch's transport (`WatchDayTransfer.run`): what the phone hasn't acknowledged, the planner's
    /// actions for it at the wall clock's time, each carried out on the link. Returns the actions.
    @discardableResult func pump() -> [Action] {
        let now = clock.now
        watch.ledger.removeSettled(now: now)
        let work = watch.waiting()
        let system = link.outstanding(waiting: Set(work.keys))
        let actions = watch.outbox.plan(work.values.map(\.item), now: now, reachable: link.reachable, lowPower: false,
                                        suspending: false, inFlight: watch.inFlight, withSystem: system)
        for action in actions { perform(action, work[action.key], now: now) }
        return actions
    }

    /// `WatchDayTransfer.perform`: a chunk's message carries its envelope (the header, then the audio file's bytes), its
    /// file the header as metadata; an event goes as its envelope's JSON.
    private func perform(_ action: Action, _ work: Watch.Work?, now: Date) {
        guard let work else {
            Issue.record("the planner named \(action.key), which nothing waits for")
            return
        }
        switch (action, work.payload) {
        case (.cancel(let key), _):
            link.cancel(key)
        case (.message(let key), .chunk(let header, let url)):
            watch.inFlight.remove(key)
            watch.outbox.sent(action, at: now)
            guard let audio = try? Data(contentsOf: url) else {
                Issue.record("chunk \(key.number)'s audio is gone")
                return
            }
            let frame = header.frame(audio: audio)
            guard RecordingDayOutbox.fitsMessage(frame.count) else {
                // As `WatchDayTransfer.framed`: too big after all, so the next pass sends it as a file.
                watch.outbox.reset(key)
                return
            }
            watch.inFlight.insert(key)
            link.sendMessage(key, .chunk(frame))
        case (.file(let key), .chunk(let header, let url)):
            watch.inFlight.remove(key)
            watch.outbox.sent(action, at: now)
            guard let bytes = try? Data(contentsOf: url) else {
                Issue.record("chunk \(key.number)'s audio is gone")
                return
            }
            link.transferFile(key, bytes: bytes, metadata: header.userInfo.compactMapValues { $0 as? Data })
        case (.message(let key), .event(let envelope)):
            watch.outbox.sent(action, at: now)
            link.sendMessage(key, .event([RecordingDayContract.eventKey: envelope.data]))
        case (.userInfo(let key), .event(let envelope)):
            watch.outbox.sent(action, at: now)
            link.transferUserInfo(key, [RecordingDayContract.eventKey: envelope.data])
        case (.file, .event), (.userInfo, .chunk):
            Issue.record("the planner paired \(action) with the wrong kind")
        }
    }

    // MARK: The link delivers

    /// Everything the link can deliver now: the system's queues (while it delivers), then the live messages; then the
    /// phone's gathered file acknowledgements go and every acknowledgement reaches the watch. Returns whether anything
    /// arrived anywhere.
    @discardableResult func deliver() -> Bool {
        var moved = false
        if link.systemDelivers {
            moved = deliverFiles() || moved
            moved = deliverUserInfos() || moved
            moved = deliverAcksQueuedForWatch() || moved
        }
        moved = deliverMessages() || moved
        phone.ackDelayElapsed()
        moved = routePhoneAcks() || moved
        return moved
    }

    /// The system delivers one queued event's user info now, ahead of the others, and its acknowledgement goes back.
    func deliverQueuedEvent(_ n: Int) {
        guard let transfer = link.takeUserInfo(.event(watch.id, n: n)) else {
            Issue.record("event \(n) is not queued with the system")
            return
        }
        receiveUserInfo(transfer)
        routePhoneAcks()
    }

    private func deliverFiles() -> Bool {
        let transfers = link.takeFiles()
        for transfer in transfers {
            if transfer.cancelled, !link.fires(.deliverDespiteCancel, transfer.key) {
                fileFinished(transfer.key, failed: true)
                continue
            }
            let copies = link.fires(.deliverTwice, transfer.key) ? 2 : 1
            for _ in 0..<copies { receiveFile(transfer) }
            fileFinished(transfer.key, failed: false)
        }
        return !transfers.isEmpty
    }

    /// The system hands the phone a temporary copy of the file, deleted once the callback returns.
    private func receiveFile(_ transfer: Link.FileTransfer) {
        let url = inbox.appendingPathComponent(UUID().uuidString + ".m4a")
        guard (try? transfer.bytes.write(to: url)) != nil else {
            Issue.record("the system couldn't copy chunk \(transfer.key.number)")
            return
        }
        link.arrived(transfer.key, at: clock.now)
        phone.receiveFile(url, metadata: transfer.metadata)
        try? FileManager.default.removeItem(at: url)
    }

    /// `WatchDayTransfer.finished`: a chunk acknowledged, erased or lost since has nothing waiting for the report.
    private func fileFinished(_ key: Key, failed: Bool) {
        guard watch.holds(key) else { return }
        watch.outbox.transferFinished(key, failed: failed, at: clock.now)
    }

    private func deliverUserInfos() -> Bool {
        let transfers = link.takeUserInfos()
        for transfer in transfers { receiveUserInfo(transfer) }
        return !transfers.isEmpty
    }

    /// The end of a user info transfer reaches nothing on the watch yet (`WatchDayTransfer.finished(_:failed:)` for
    /// events waits for WatchSync): the planner finds it gone from the system's queue at its next pass.
    private func receiveUserInfo(_ transfer: Link.UserInfo) {
        link.arrived(transfer.key, at: clock.now)
        _ = phone.receiveEvent(transfer.info, replying: false)
    }

    private func deliverAcksQueuedForWatch() -> Bool {
        let infos = link.takeForWatch()
        for info in infos { watchReceived(ackMessage: info) }
        return !infos.isEmpty
    }

    private func deliverMessages() -> Bool {
        let messages = link.takeMessages()
        for message in messages { deliver(message) }
        return !messages.isEmpty
    }

    private func deliver(_ message: Link.Message) {
        guard link.reachable else { return messageFailed(message) }
        let copies = link.fires(.deliverTwice, message.key) ? 2 : 1
        var reply = Link.Reply.chunk(Data())
        for _ in 0..<copies {
            link.arrived(message.key, at: clock.now)
            reply = answer(message.payload)
        }
        // Lost on its way back: the watch hears nothing, and its timeout sends the message again.
        guard !link.fires(.dropReply, message.key), !link.losesReply(message.key) else { return }
        switch reply {
        case .chunk(let data): chunkReplied(data, key: message.key)
        case .event(let info): watchReceived(ackMessage: info)
        }
    }

    private func answer(_ payload: Link.Payload) -> Link.Reply {
        switch payload {
        case .chunk(let frame): .chunk(phone.receiveMessageData(frame))
        case .event(let info): .event(phone.receiveEvent(info, replying: true))
        }
    }

    /// A message that couldn't go (the phone out of reach): a chunk's error reaches `WatchDayTransfer.failed`; an
    /// event's message falls back to user info, as `WatchSync.sendDayEvent` does.
    private func messageFailed(_ message: Link.Message) {
        switch message.payload {
        case .chunk:
            watch.inFlight.remove(message.key)
            watch.outbox.messageFailed(message.key, at: clock.now)
        case .event(let info):
            link.transferUserInfo(message.key, info)
        }
    }

    /// `WatchDayTransfer.replied`: the phone answered, so the live path works; an answer that doesn't acknowledge this
    /// chunk (it couldn't be stored) counts as a failure.
    private func chunkReplied(_ reply: Data, key: Key) {
        watch.outbox.linkAnswered()
        watch.inFlight.remove(key)
        let ack = RecordingDayAck(data: reply).map(link.passing)
        if let ack { watchReceived(ack) }
        let named = ack.map { $0.recording == key.recording && $0.seqs.contains(key.number) } ?? false
        if !named { watch.outbox.messageFailed(key, at: clock.now, answered: true) }
    }

    /// The phone's own acknowledgements: `WatchSync.deliver` sends a message while the watch app is reachable, else
    /// user info.
    @discardableResult private func routePhoneAcks() -> Bool {
        let acks = phone.takeAcks()
        for ack in acks {
            let info = [RecordingDayContract.ackKey: ack.data]
            if link.reachable { watchReceived(ackMessage: info) } else { link.queueForWatch(info) }
        }
        return !acks.isEmpty
    }

    private func watchReceived(ackMessage info: [String: Data]) {
        guard let ack = RecordingDayAck(data: info[RecordingDayContract.ackKey]) else { return }
        watchReceived(ack)
    }

    /// Every acknowledgement reaches the watch here, whichever way it came, less what the link loses of it.
    private func watchReceived(_ ack: RecordingDayAck) {
        for seq in watch.apply(link.passing(ack), now: clock.now) { stamps.mark(.acked, seq) }
    }

    // MARK: Rounds

    /// Passes and deliveries until a round moves nothing, then the phone's lanes for what arrived. Nothing waits on a
    /// timer: what is due later waits for `drain` to move the clock.
    func settle() async {
        for _ in 0..<Self.maxRounds {
            let acted = !pump().isEmpty
            let moved = deliver()
            guard acted || moved else {
                await phone.work()
                return
            }
        }
        Issue.record("the transport never came to rest")
    }

    /// Until the watch holds nothing more: `settle`, and while something waits for its acknowledgement, the clock moved
    /// to the planner's next deadline (a reply or an acknowledgement overdue, a backoff over), when `WatchDayTransfer`'s
    /// wake timer would fire.
    func drain() async {
        for _ in 0..<Self.maxWakes {
            await settle()
            guard watch.holdsAnything else { return }
            guard let wake = watch.outbox.nextWake(after: clock.now) else {
                Issue.record("the watch still holds \(watch.heldDescription) and nothing would wake its transport")
                return
            }
            clock.set(wake)
        }
        Issue.record("the watch still holds \(watch.heldDescription) after \(Self.maxWakes) wakes")
    }

    // MARK: Checking

    /// The live path's premise (docs/assistant/day-recap.md「Chunks」: 10 s of 32 kbps AAC is about 40 KB, the whole
    /// envelope under `maxMessageBytes`): these 10-second chunks' envelopes fit a message. When they don't, the planner
    /// rightly sends them as files, and the live-path checks after this one fail for that reason, not their own.
    func expectLiveSized(_ seqs: [Int]) {
        let over = seqs.filter { !RecordingDayOutbox.fitsMessage(watch.envelopeBytes[$0] ?? .max) }
        let sizes = over.map { "\($0): \(watch.envelopeBytes[$0].map { "\($0)" } ?? "unknown") bytes" }
        #expect(over.isEmpty, "10-second chunks too big for the live envelope (\(RecordingDayContract.maxMessageBytes) bytes): \(sizes)")
    }

    /// Each seq is held by the phone as it first arrived (no later copy replaced it), its audio the bytes the watch's
    /// writer finished.
    func expectStoredOnce(_ seqs: [Int], in record: RecordingDayRecord) {
        for seq in seqs {
            let key = Key.chunk(watch.id, seq: seq)
            #expect(record.chunks[seq]?.receivedAt == link.firstArrival[key], "chunk \(seq) kept its first copy")
            let stored = try? Data(contentsOf: phone.store.audioURL(watch.id, seq: seq))
            #expect(stored != nil && stored == watch.audio[seq], "chunk \(seq)'s audio arrived unchanged")
        }
    }

    /// The latency of `stage` for `seqs`, from when each was finished (`ready`), or, for a drain's own figure, from
    /// `start` (the moment the phone was back in reach), printed as one `LATENCY` line.
    @discardableResult func report(_ label: String, _ stage: Stamps.Stage, _ seqs: [Int],
                                   since start: ContinuousClock.Instant? = nil) -> Latency {
        let millis = seqs.compactMap { seq -> Double? in
            guard let from = start ?? stamps.moment(.ready, seq), let to = stamps.moment(stage, seq) else { return nil }
            return Latency.millis(to - from)
        }
        let latency = Latency(label, millis)
        print(latency.line)
        #expect(millis.count == seqs.count, "every chunk of \(label) has its reading")
        return latency
    }
}

// MARK: - Tests

/// The watch → phone stream through a fake WatchConnectivity (docs/assistant/day-recap.md「Chunks」,「Control events」,
/// 「Acknowledgements and retries」and「Erasing」), with the per-chunk latency of the in-process work. Serialized, so the
/// latency of one scenario is not measured while another one encodes audio.
@Suite(.serialized)
@MainActor struct RecordingDayStreamTests {
    /// In reach: each chunk goes by message as soon as it is finished, and is stored and acknowledged once, in seq order,
    /// as the bytes the watch wrote; speech reaches the hub once, in order, and silence is skipped.
    @Test func liveChunksAreStoredOnceInOrderAndReachTheHub() async throws {
        let loop = try Loopback(startingAt: anchor)
        defer { loop.cleanUp() }
        let id = loop.watch.id
        // Twenty, so a nearest-rank p95 is not just the slowest reading.
        let seqs = Array(0..<20)
        let quiet: Set<Int> = [3, 7, 8, 15]
        for seq in seqs {
            let finished = try loop.record(liveSeconds, speech: !quiet.contains(seq))
            #expect(finished == [seq])
            await loop.settle()
        }
        let speech = seqs.filter { !quiet.contains($0) }

        // The watch: every chunk went once, by message, reached the phone once, and one acknowledgement took it off.
        loop.expectLiveSized(seqs)
        let notLive = seqs.filter { loop.link.routes(.chunk(id, seq: $0)) != [.message] }
        #expect(notLive.isEmpty, "chunks that didn't go once by message: \(notLive.map { ($0, loop.link.routes(.chunk(id, seq: $0))) })")
        let notOnce = seqs.filter { loop.link.arrivals[.chunk(id, seq: $0)] != 1 || loop.watch.ackCount[$0] != 1 }
        #expect(notOnce.isEmpty, "chunks that arrived or were acknowledged other than once: \(notOnce)")
        #expect(!loop.watch.holdsAnything && loop.watch.audioFiles.isEmpty, "nothing of the day is left on the watch")

        // The phone: every seq once, in seq order, its audio the watch's bytes.
        let record = try await loop.phone.record(id)
        #expect(record.chunks.keys.sorted() == seqs)
        let received = seqs.compactMap { record.chunks[$0]?.receivedAt }
        #expect(received.count == seqs.count && zip(received, received.dropFirst()).allSatisfy { $0 < $1 }, "stored in seq order")
        loop.expectStoredOnce(seqs, in: record)
        #expect(record.day == "2027-01-15" && record.timeZone == zone && record.startedAt == anchor)

        // Speech transcribed once each from audio that decodes as recorded; silence skipped without the engine; the hub
        // given every sentence once, in order, on a day created with the recording's day, zone and clock.
        #expect(speech.allSatisfy { record.chunks[$0]?.state == .transcribed })
        #expect(quiet.allSatisfy { record.chunks[$0]?.state == .skipped })
        #expect(loop.phone.engine.seqs.sorted() == speech)
        #expect(loop.phone.engine.problems.isEmpty, "\(loop.phone.engine.problems)")
        #expect(loop.phone.hub.texts(id) == speech.map(FakeEngine.text))
        let create = try #require(loop.phone.hub.bodies(id, "PUT").first)
        #expect(create["kind"] as? String == "day" && create["day"] as? String == "2027-01-15")
        #expect(create["tz"] as? String == zone, "the recording's own zone")
        #expect((create["started_at"] as? String).flatMap { RecordingDayWire.date($0) } == anchor, "recording_started_at")

        let acked = loop.report("live finished->stored+acked", .acked, seqs)
        let transcribed = loop.report("live finished->transcribed", .transcribed, speech)
        let appended = loop.report("live finished->appended", .appended, speech)
        #expect(acked.p95 < 500, "\(acked.line)")
        #expect(transcribed.p95 < 1_000, "\(transcribed.line)")
        #expect(appended.p95 < 2_000, "\(appended.line)")
    }

    /// Out of reach the chunks queue with the system as files; back in reach the small ones are taken back and sent
    /// live, the minute-long ones drain as files, oldest first. A cancelled transfer that arrives anyway, a file delivered
    /// twice, a lost reply whose acknowledgement comes another way, and a chunk none of whose acknowledgements get
    /// through until its timeout and backoff sent it again: every chunk stored once, none lost.
    @Test func aBacklogDrainsOldestFirstWithoutLossOrDuplicates() async throws {
        let loop = try Loopback(startingAt: anchor)
        defer { loop.cleanUp() }
        let id = loop.watch.id
        func chunk(_ seq: Int) -> Key { .chunk(id, seq: seq) }

        // In reach: 0 (speech) and 1 (quiet) go live.
        try loop.record(liveSeconds, speech: true)
        await loop.settle()
        try loop.record(liveSeconds, speech: false)
        await loop.settle()
        // 2 and 3 go live as well, and the phone is out of reach before they arrive: their messages fail, and they go to
        // the system as files at once.
        try loop.record(liveSeconds, speech: true)
        try loop.record(liveSeconds, speech: true)
        loop.expectLiveSized(Array(0...3))
        #expect(loop.pump() == [.message(chunk(2)), .message(chunk(3))])
        loop.disconnect()
        await loop.settle()
        // Out of reach the chunks are a minute long, each queued as a file as soon as it is finished; a bookmark goes as
        // user info.
        loop.watch.chunkSeconds = backlogSeconds
        for speech in [true, false, true] {
            try loop.record(backlogSeconds, speech: speech)
            await loop.settle()
        }
        let marked = loop.watch.mark()
        await loop.settle()
        let away = Array(2...6)
        #expect(loop.link.queuedFiles == away, "queued oldest first, and none delivered while away")
        let before = try await loop.phone.record(id)
        #expect(before.chunks.keys.sorted() == [0, 1])

        // Back in reach. The system delivers chunk 2's file although the watch cancelled it, and the reply to chunk 2's
        // live copy is lost; chunk 4's file arrives twice; and every acknowledgement of chunk 3 is lost until the watch
        // has sent it again.
        loop.link.inject(.deliverDespiteCancel, chunk(2))
        loop.link.inject(.dropReply, chunk(2))
        loop.link.inject(.deliverTwice, chunk(4))
        loop.link.loseAcks(of: chunk(3))
        let back = loop.reconnect()
        let sentLive = loop.clock.now
        #expect(loop.pump() == [.cancel(chunk(2)), .cancel(chunk(3)), .message(chunk(2)), .message(chunk(3))],
                "the small chunks come back from the system and go live, oldest first; the minute-long ones stay files")
        await loop.settle()

        // Chunk 2 needs no resend: its file's acknowledgement came with the bookmark's. Chunk 3 is on the phone, but the
        // watch heard nothing: its transport wakes once the reply is `ackTimeout` overdue, counts the send failed, and
        // sends it again when the first backoff is over.
        #expect(loop.watch.heldSeqs == [3], "only chunk 3 waits for its acknowledgement: \(loop.watch.heldDescription)")
        let overdue = sentLive.addingTimeInterval(RecordingDayContract.ackTimeout)
        #expect(loop.watch.outbox.nextWake(after: loop.clock.now) == overdue, "the transport wakes when the reply is overdue")
        loop.clock.set(overdue)
        #expect(loop.pump().isEmpty, "the send failed, and the backoff holds the next one")
        let retry = overdue.addingTimeInterval(RecordingDayContract.backoffFirst)
        #expect(loop.watch.outbox.nextWake(after: loop.clock.now) == retry, "the transport wakes when the backoff is over")
        loop.clock.set(retry)
        #expect(loop.pump() == [.message(chunk(3))], "sent again, live")
        #expect(loop.watch.ackCount[3] == nil, "no acknowledgement of chunk 3 reached the watch before its resend")
        // Its acknowledgements get through again: the phone, holding chunk 3 already, acknowledges the second copy
        // without storing it.
        loop.link.deliverAcks(of: chunk(3))
        await loop.drain()

        // No loss, no duplicate: every seq once, as it first arrived, though 2, 3 and 4 reached the phone twice.
        let all = Array(0...6)
        let record = try await loop.phone.record(id)
        #expect(record.chunks.keys.sorted() == all)
        loop.expectStoredOnce(all, in: record)
        let twice = [2, 3, 4].filter { loop.link.arrivals[chunk($0)] == 2 }
        #expect(twice == [2, 3, 4], "arrivals: \(loop.link.arrivals)")
        #expect(loop.watch.ackCount[3] == 1, "acknowledged once it got through")
        #expect(!loop.watch.holdsAnything && loop.watch.audioFiles.isEmpty, "every chunk acknowledged in the end")
        #expect(loop.link.routes(chunk(2)) == [.message, .file, .cancel, .message],
                "acknowledged through its file, never sent again")
        #expect(loop.link.routes(chunk(3)) == [.message, .file, .cancel, .message, .message],
                "sent again after its timeout")
        for seq in 4...6 {
            let routes = loop.link.routes(chunk(seq))
            #expect(!routes.contains(.message) && !routes.contains(.cancel), "chunk \(seq) is too big for a message: \(routes)")
        }
        #expect(loop.link.order(.file) == away)
        #expect(record.marks == [RecordingDayMark(at: marked, label: RecordingDayEvent.Label.bookmark)])

        // The text: each spoken chunk transcribed once and on the hub once; the quiet ones skipped.
        let speech = [0, 2, 3, 4, 6]
        #expect(speech.allSatisfy { record.chunks[$0]?.state == .transcribed })
        #expect([1, 5].allSatisfy { record.chunks[$0]?.state == .skipped })
        #expect(loop.phone.engine.seqs.sorted() == speech)
        #expect(loop.phone.engine.problems.isEmpty, "\(loop.phone.engine.problems)")
        let texts = loop.phone.hub.texts(id)
        #expect(texts.sorted() == speech.map(FakeEngine.text).sorted(), "\(texts)")

        // From when each chunk was finished. The time away, chunk 3's timeout and its backoff are the test's wall clock,
        // which costs no time on the monotonic one: these readings are the in-process work between a chunk's end and
        // each stage (recording the later chunks while away, the drain, and for chunk 3 the passes of its retry, after
        // the phone's lanes ran).
        let spoken = [2, 3, 4, 6]
        let acked = loop.report("backlog finished->stored+acked", .acked, away)
        let transcribed = loop.report("backlog finished->transcribed", .transcribed, spoken)
        let appended = loop.report("backlog finished->appended", .appended, spoken)
        #expect(acked.maximum < 5_000, "\(acked.line)")
        #expect(transcribed.maximum < 6_000, "\(transcribed.line)")
        #expect(appended.maximum < 8_000, "\(appended.line)")

        // The drain's own: from the moment the phone was back in reach.
        let drainAcked = loop.report("backlog reachable->stored+acked", .acked, away, since: back)
        let drainTranscribed = loop.report("backlog reachable->transcribed", .transcribed, spoken, since: back)
        let drainAppended = loop.report("backlog reachable->appended", .appended, spoken, since: back)
        #expect(drainAcked.maximum < 2_000, "\(drainAcked.line)")
        #expect(drainTranscribed.maximum < 3_000, "\(drainTranscribed.line)")
        #expect(drainAppended.maximum < 5_000, "\(drainAppended.line)")
    }

    /// A day around midnight: an interruption, an erase of the chunk being written (it never leaves the watch), a stop,
    /// and the close at midnight. The first events wait with the system while the later ones overtake them by message,
    /// one comes twice, the system's arrive in reverse: the phone holds each until those before it came, applies all of
    /// them in `n` order once, and the day ends complete on the hub and is removed from the watch.
    @Test func controlEventsApplyInOrderWhicheverWayTheyArrive() async throws {
        let midnight = shanghaiMidnight
        let loop = try Loopback(startingAt: midnight - 120)
        defer { loop.cleanUp() }
        let id = loop.watch.id
        let eraseID = UUID()

        // Out of reach: `started` (0), chunk 0, an interruption (1) and the microphone back (2) queue with the system.
        loop.disconnect()
        try loop.record(liveSeconds, speech: true)
        loop.expectLiveSized([0])
        loop.watch.micClosed(Reason.interrupted, now: loop.clock.now)
        loop.wait(50)
        loop.watch.micOpened()
        await loop.settle()
        #expect(loop.link.queuedEvents == [0, 1, 2] && loop.link.queuedFiles == [0])

        // In reach again while the system still holds those: chunk 0 is taken back and sent live. Then the erase of the
        // chunk being written (3), the user's stop (4) and the close at midnight (5) go by message as they happen,
        // overtaking 0…2. Event 4 reaches the phone twice; the reply to 5 is lost.
        loop.reconnect(systemDelivers: false)
        loop.link.inject(.deliverTwice, .event(id, n: 4))
        loop.link.inject(.dropReply, .event(id, n: 5))
        await loop.settle()
        try loop.record(4, speech: true)
        let erased = loop.watch.erase(RecordingErase(from: midnight - 58, to: midnight - 56), eventID: eraseID)
        #expect(erased == [1], "the open chunk is cut, and erased before it ever left the watch")
        loop.watch.stop(now: loop.clock.now)
        loop.clock.set(midnight + 1)
        #expect(loop.watch.ledger.closeStaleDays(now: loop.clock.now) == [id], "closed at its midnight")
        await loop.settle()
        var record = try await loop.phone.record(id)
        #expect(record.chunks.keys.sorted() == [0])
        #expect(record.appliedEvents == 0 && record.events.keys.sorted() == [3, 4, 5], "held until 0, 1 and 2 arrive")
        #expect(record.closedAt == nil && record.pauses.isEmpty && record.erasures.isEmpty)

        // The system delivers 2, then 0, then 1.
        loop.deliverQueuedEvent(2)
        record = try await loop.phone.record(id)
        #expect(record.appliedEvents == 0 && record.events.keys.sorted() == [2, 3, 4, 5])
        loop.deliverQueuedEvent(0)
        record = try await loop.phone.record(id)
        #expect(record.appliedEvents == 1 && record.events.keys.sorted() == [2, 3, 4, 5] && record.closedAt == nil)
        loop.deliverQueuedEvent(1)
        record = try await loop.phone.record(id)
        #expect(record.appliedEvents == 6 && record.events.isEmpty)
        #expect(record.pauses == [RecordingDayPause(from: midnight - 110, to: midnight - 60, reason: Reason.interrupted),
                                  RecordingDayPause(from: midnight - 56, to: midnight, reason: Reason.paused)])
        #expect(record.erasures == [RecordingDayErasure(id: eraseID, from: midnight - 58, to: midnight - 56)])
        #expect(record.erasedSeqs == [1] && record.closedAt == midnight && record.seqCount == 2 && record.missingSeqs.isEmpty)

        // The rest of the way: the event whose reply was lost goes again after its timeout and changes nothing; the
        // watch, holding nothing more of a closed day, lets it go.
        loop.link.systemDelivers = true
        await loop.drain()
        record = try await loop.phone.record(id)
        #expect(record.appliedEvents == 6 && record.pauses.count == 2 && record.erasures.count == 1)
        #expect(loop.link.arrivals[.event(id, n: 4)] == 2 && (loop.link.arrivals[.event(id, n: 5)] ?? 0) >= 2)
        // Kept a while after its midnight, all of it on the phone: an erase of the last minutes on the watch can still
        // reach it (docs/assistant/day-recap.md「Erasing」). Then it goes.
        let removable = midnight + RecordingDayOutbox.keepClosedSeconds
        if loop.clock.now < removable {
            #expect(loop.watch.library.entry(id) != nil, "kept until \(removable)")
        }
        loop.clock.set(removable)
        loop.pump()
        #expect(loop.watch.library.entry(id) == nil, "the closed day the phone has all of is removed from the watch")

        // The hub: the day's one sentence, and the day complete with its gaps.
        #expect(loop.phone.hub.texts(id) == [FakeEngine.text(0)])
        let latest = try #require(loop.phone.hub.latest(id))
        #expect(latest["complete"] as? Bool == true)
        let gaps = (latest["coverage"] as? [String: Any])?["gaps"] as? [[String: Any]] ?? []
        let reasons = Set(gaps.compactMap { $0["reason"] as? String })
        #expect(reasons.isSuperset(of: [Reason.interrupted, Reason.paused, "erased"]) && !reasons.contains("pending"),
                "\(gaps)")
    }

    /// Chunks the watch let go of once acknowledged, transcribed and on the hub, then erased on the watch: the `erase`
    /// event (delivered twice) takes their audio and words off the phone, leaves the others alone, and the hub is
    /// repaired keeping every other sentence's position.
    @Test func anEraseAfterTheAcknowledgementTakesAudioAndTextOffThePhone() async throws {
        let loop = try Loopback(startingAt: anchor)
        defer { loop.cleanUp() }
        let id = loop.watch.id
        let seqs = Array(0..<6)
        for _ in seqs {
            try loop.record(liveSeconds, speech: true)
            await loop.settle()
        }
        #expect(!loop.watch.holdsAnything, "acknowledged, so gone from the watch")
        #expect(loop.phone.hub.texts(id) == seqs.map(FakeEngine.text))

        // 00:30–00:50 of the recording is chunks 3 and 4, which only the phone and the hub still have.
        let eraseID = UUID()
        let window = RecordingErase(from: anchor + 30, to: anchor + 50)
        loop.link.inject(.deliverTwice, .event(id, n: 1))
        #expect(loop.watch.erase(window, eventID: eraseID).isEmpty, "the watch held none of it any more")
        await loop.drain()

        let record = try await loop.phone.record(id)
        #expect(record.appliedEvents == 2, "applied once")
        #expect(record.erasures == [RecordingDayErasure(id: eraseID, from: window.from, to: window.to)])
        for seq in [3, 4] {
            let chunk = try #require(record.chunks[seq])
            #expect(chunk.erased && !chunk.hasAudio, "chunk \(seq)")
            #expect(!FileManager.default.fileExists(atPath: loop.phone.store.audioURL(id, seq: seq).path), "chunk \(seq)'s audio")
            #expect((chunk.sentences ?? []).allSatisfy { $0.text.isEmpty }, "chunk \(seq)'s words")
        }
        for seq in [0, 1, 2, 5] {
            let chunk = try #require(record.chunks[seq])
            #expect(!chunk.erased && chunk.hasAudio && chunk.sentences?.map(\.text) == [FakeEngine.text(seq)], "chunk \(seq)")
        }
        loop.expectStoredOnce([0, 1, 2, 5], in: record)

        // The hub: repaired by a `PUT` that keeps every other sentence's position, an empty one in place of each erased.
        #expect(loop.phone.hub.texts(id) == [0, 1, 2].map(FakeEngine.text) + ["", ""] + [FakeEngine.text(5)])
        #expect(loop.phone.hub.bodies(id, "PUT").count >= 2, "the create, then the repair")
        #expect(!loop.watch.holdsAnything, "the erase event acknowledged")
    }
}
