import Foundation

// All-day recording between the watch and the phone (docs/assistant/day-recap.md「Watch to phone」): the contract's
// constants and keys, the chunk header and its envelope, the control events, acknowledgements, the phone's erase and
// delete requests with the watch's confirmations, the phone's capabilities, the watch's state for the phone, the
// local-day calendar, the level the phone skips silence by, and the cached hub flag. Pure Foundation and
// `nonisolated`, so the watch, the phone's WatchConnectivity queue, its store queue and the tests all use the same
// code. Names marked [ext] are this app's own, beyond the contract; a phone or watch that does not know them ignores
// them.

nonisolated enum RecordingDayContract {
    /// The envelope version this build writes, and the `recordingDay` value of a phone whose user agreed.
    static let version = 1
    /// The hub's feature flag (`hello.features`): only a hub listing it gets day recordings uploaded.
    static let feature = "recording_day"

    // Application context, phone → watch (docs/assistant/day-recap.md「Watch and phone capabilities」).
    /// Int: `version` once the user agreed to the consent sheet, 0 while the phone takes day recordings but the user has
    /// not agreed yet. A phone from before never sets it.
    static let phoneKey = "recordingDay"
    /// Bool: whether the phone's hub lists `feature`.
    static let phoneHubKey = "recordingDayHub"
    /// Int, 1…`maxKeepDays`: how many days after its `day` the watch keeps a chunk the phone has not acknowledged.
    static let phoneKeepDaysKey = "recordingDayKeepDays"
    /// [ext] Application context, watch → phone: Data, a JSON `RecordingDayWatchState`.
    static let watchStateKey = "recordingDayWatch"

    // Messages, user info and `transferFile` metadata. Every value is JSON `Data`.
    /// `transferFile` metadata, watch → phone: the chunk's `RecordingDayHeader`.
    static let chunkKey = "personaDayChunk"
    /// Watch → phone: a `RecordingDayEventEnvelope`.
    static let eventKey = "personaDayEvent"
    /// Phone → watch: a `RecordingDayAck`. (A live chunk's acknowledgement is the bare JSON of the
    /// `sendMessageData` reply.)
    static let ackKey = "personaDayAck"
    /// Phone → watch: a `RecordingDayEraseRequest`.
    static let eraseKey = "personaDayErase"
    /// Watch → phone: a `RecordingDayErased`, the answer to an erase request.
    static let erasedKey = "personaDayErased"
    /// Phone → watch: a `RecordingDayDelete`.
    static let deleteKey = "personaDayDelete"
    /// Watch → phone: a `RecordingDayDeleted`, the answer to a delete.
    static let deletedKey = "personaDayDeleted"
    /// [ext] Phone → watch: a `RecordingDayCommand`, the switch stamped with when it was sent so the watch acts only on
    /// the newest (`WatchDayCommandOrder`).
    static let commandKey = "personaDayCommand"
    /// The keys a message or a user info may carry, in the order they are looked for.
    static let messageKeys = [eventKey, ackKey, eraseKey, erasedKey, deleteKey, deletedKey, commandKey, chunkKey]

    /// Chunk lengths: while the phone is reachable, and while chunks wait for it.
    static let liveChunkSeconds = 10.0, backlogChunkSeconds = 60.0
    /// A chunk's whole live envelope (`RecordingDayHeader.frame`) stays under this; a bigger one goes by `transferFile`.
    static let maxMessageBytes = 48_000
    /// Resending: no acknowledgement this long after a live send, or after a file transfer the system says completed;
    /// then a backoff from `backoffFirst` doubling to `backoffMax`.
    static let ackTimeout = 30.0, fileAckTimeout = 60.0, backoffFirst = 2.0, backoffMax = 300.0
    /// Messages awaiting their reply at once.
    static let maxInFlightMessages = 3
    /// The phone transcribes past a missing seq once it has been missing this long while later chunks keep coming.
    static let gapWaitSeconds = 120.0
    /// A chunk quieter than this (`RecordingDayLevel`) is not transcribed.
    static let silenceDBFS = -50.0
    /// The phone transcribes contiguous chunks together, in runs up to this long, so words aren't cut at chunk edges.
    static let runSeconds = 60.0
    /// Tries to transcribe a run before its chunks count as untranscribed.
    static let maxAttempts = 3
    /// The phone deletes a day's audio this many days after its first final recap, and at the latest this many after
    /// the day.
    static let audioDaysAfterRecap = 3.0, audioMaxDays = 7.0
    /// The phone appends to the hub at most this often while the day records, and this many sentences per request.
    static let appendInterval = 20.0, maxAppendSegments = 500
    /// The longest the watch keeps an unacknowledged chunk after its `day` (`phoneKeepDaysKey`), and its default.
    static let maxKeepDays = 7
    /// The watch remembers the erases the phone asked for this long, to answer a repeated request the same way.
    static let eraseMemoryDays = 7.0

    /// `recordingDayKeepDays` for a hub keeping audio `audioDays` (nil while the phone doesn't know): `min(7, audio
    /// days)`, and the 7-day maximum when audio is forever (`0`) or the value isn't positive.
    static func keepDays(audioDays: Int?) -> Int {
        guard let audioDays, audioDays > 0 else { return maxKeepDays }
        return min(maxKeepDays, audioDays)
    }

    /// The phone's commands to the watch [ext].
    nonisolated enum Command {
        static let start = "start"
        static let stop = "stop"
    }

    /// Why the microphone closed (a `paused` event's `reason`, and the hub's coverage gap reasons). An unknown reason is
    /// kept as it is.
    nonisolated enum Reason {
        /// The user paused, or turned All-Day Recording off.
        static let paused = "paused"
        /// A call, Siri or another app took the microphone.
        static let interrupted = "interrupted"
        /// Recording stopped by itself: battery, storage, a failed write, the audio service reset, a crash.
        static let stopped = "stopped"
    }
}

// MARK: - Encoding

/// How the day wire writes its values: ids in lowercase (as the hub writes them), and times as RFC 3339 with
/// milliseconds and the recording's own offset (`2026-11-11T09:41:07.250+08:00`). Reading is lenient: either case, an
/// offset or `Z`, with or without fractions, or seconds since 1970.
nonisolated enum RecordingDayWire {
    static func id(_ uuid: UUID) -> String { uuid.uuidString.lowercased() }

    static func timestamp(_ date: Date, in zone: TimeZone) -> String {
        date.formatted(Date.ISO8601FormatStyle(timeZoneSeparator: .colon, includingFractionalSeconds: true, timeZone: zone))
    }

    static func date(_ text: String) -> Date? {
        for fractional in [true, false] {
            if let date = try? Date.ISO8601FormatStyle(timeZoneSeparator: .colon, includingFractionalSeconds: fractional).parse(text) {
                return date
            }
        }
        return nil
    }

    /// A recording's `tz`: an identifier `TimeZone` knows, nil otherwise.
    static func zone(_ identifier: String) -> TimeZone? {
        guard !identifier.isEmpty, identifier.count <= 100 else { return nil }
        return TimeZone(identifier: identifier)
    }

    static func uuid<K: CodingKey>(_ c: KeyedDecodingContainer<K>, _ key: K) throws -> UUID {
        guard let id = UUID(uuidString: try c.decode(String.self, forKey: key)) else {
            throw DecodingError.dataCorruptedError(forKey: key, in: c, debugDescription: "not a UUID")
        }
        return id
    }

    static func date<K: CodingKey>(_ c: KeyedDecodingContainer<K>, _ key: K) throws -> Date {
        if let seconds = try? c.decode(Double.self, forKey: key) { return Date(timeIntervalSince1970: seconds) }
        guard let date = date(try c.decode(String.self, forKey: key)) else {
            throw DecodingError.dataCorruptedError(forKey: key, in: c, debugDescription: "not an RFC 3339 time")
        }
        return date
    }

    /// An optional time: absent, or present but unreadable, is nil.
    static func optionalDate<K: CodingKey>(_ c: KeyedDecodingContainer<K>, _ key: K) -> Date? {
        c.contains(key) ? (try? date(c, key)) : nil
    }

    static func finite(_ date: Date?) -> Bool { date?.timeIntervalSince1970.isFinite ?? true }
}

/// A JSON value of the day wire, carried as `Data` under its key: written and read the same way everywhere, and refused
/// on reading when it is not something the other side could have sent.
nonisolated protocol RecordingDayMessage: Codable, Sendable {
    var valid: Bool { get }
}

nonisolated extension RecordingDayMessage {
    /// The JSON; empty only for a value no JSON can hold.
    var data: Data { (try? JSONEncoder().encode(self)) ?? Data() }

    init?(data: Data?) {
        guard let data, let value = try? JSONDecoder().decode(Self.self, from: data), value.valid else { return nil }
        self = value
    }
}

// MARK: - Chunks

/// What travels with each chunk's audio (docs/assistant/day-recap.md「Chunks」): before it in the live envelope, or as a
/// `transferFile`'s metadata.
nonisolated struct RecordingDayHeader: Sendable, Equatable {
    var v = RecordingDayContract.version
    /// The day recording's id on the watch; the phone uploads its first part to the hub under the same id.
    var recording: UUID
    /// When the day recording began on the watch, the same in every chunk: the hub's `started_at`, the day's clock.
    var recordingStartedAt: Date
    /// The recording's IANA time zone, fixed for it: its `day`, its midnight and the hub's hours are in this zone.
    var tz: String
    /// The date in `tz` the recording belongs to, `YYYY-MM-DD`.
    var day: String
    /// 0, 1, 2… per day recording, never reused.
    var seq: Int
    /// The wall clock of the chunk's first sample.
    var startedAt: Date
    /// Seconds of audio in the chunk.
    var duration: Double
    /// Mean RMS level in dBFS (`RecordingDayLevel`); nil when unknown (written as `null`).
    var level: Double?
}

nonisolated extension RecordingDayHeader {
    /// The header of a registered chunk of a day recording on the watch; nil for an entry or a chunk that isn't one.
    init?(entry: RecordingEntry, chunk: RecordingSegment) {
        guard entry.isDay, let day = entry.day, let tz = entry.timeZone, let startedAt = chunk.startedAt else { return nil }
        self.init(recording: entry.id, recordingStartedAt: entry.createdAt, tz: tz, day: day, seq: chunk.index,
                  startedAt: startedAt, duration: chunk.duration, level: chunk.level)
        guard valid else { return nil }
    }

    /// A header the watch could have written: anything else is refused and never acknowledged.
    var valid: Bool {
        let numbers = v >= 1 && (0..<1_000_000).contains(seq) && duration.isFinite && (0...600).contains(duration)
            && (level?.isFinite ?? true)
        let times = RecordingDayWire.finite(startedAt) && RecordingDayWire.finite(recordingStartedAt)
        return numbers && times && RecordingDayCalendar.isDay(day) && RecordingDayWire.zone(tz) != nil
    }

    /// The header as JSON (what `chunkKey` carries); empty only for a header no JSON can hold (a non-finite duration).
    var json: Data { (try? JSONEncoder().encode(self)) ?? Data() }

    /// A `transferFile`'s metadata.
    var userInfo: [String: Any] { [RecordingDayContract.chunkKey: json] }

    /// From a `transferFile`'s metadata; nil when it holds no valid header.
    init?(userInfo: [String: Any]?) {
        guard let data = userInfo?[RecordingDayContract.chunkKey] as? Data,
              let header = try? JSONDecoder().decode(Self.self, from: data), header.valid else { return nil }
        self = header
    }

    /// The live envelope, version 1: the header's length as a 4-byte unsigned big-endian integer, the header's UTF-8
    /// JSON, then the audio file's bytes unchanged.
    func frame(audio: Data) -> Data {
        let header = json
        var out = Data(capacity: 4 + header.count + audio.count)
        withUnsafeBytes(of: UInt32(header.count).bigEndian) { out.append(contentsOf: $0) }
        out.append(header)
        out.append(audio)
        return out
    }

    /// An envelope back into its header and audio; nil for one that is cut short or holds no valid header.
    static func unframe(_ data: Data) -> (header: RecordingDayHeader, audio: Data)? {
        guard data.count >= 4 else { return nil }
        let length = data.prefix(4).reduce(0) { $0 << 8 | Int($1) }
        let start = data.startIndex + 4
        guard length > 0, length <= maxHeaderBytes, data.count - 4 >= length,
              let header = try? JSONDecoder().decode(Self.self, from: data.subdata(in: start..<start + length)),
              header.valid else { return nil }
        return (header, data.subdata(in: start + length..<data.endIndex))
    }

    /// A header is a few hundred bytes; a length past this is an envelope from something else.
    private static let maxHeaderBytes = 64 * 1024
}

nonisolated extension RecordingDayHeader: Codable {
    private nonisolated enum CodingKeys: String, CodingKey {
        case v, recording, tz, day, seq, duration, level
        case recordingStartedAt = "recording_started_at"
        case startedAt = "started_at"
    }

    /// Tolerant: unknown keys are ignored (an older draft's `erased`, `end`, `marks` too), a missing `v` is version 1,
    /// and a missing, `null` or unreadable `level` is unknown; every other key must be there.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(v: (try? c.decodeIfPresent(Int.self, forKey: .v)) ?? RecordingDayContract.version,
                  recording: try RecordingDayWire.uuid(c, .recording),
                  recordingStartedAt: try RecordingDayWire.date(c, .recordingStartedAt),
                  tz: try c.decode(String.self, forKey: .tz),
                  day: try c.decode(String.self, forKey: .day),
                  seq: try c.decode(Int.self, forKey: .seq),
                  startedAt: try RecordingDayWire.date(c, .startedAt),
                  duration: try c.decode(Double.self, forKey: .duration),
                  level: try? c.decodeIfPresent(Double.self, forKey: .level))
    }

    func encode(to encoder: Encoder) throws {
        let zone = RecordingDayWire.zone(tz) ?? .gmt
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(v, forKey: .v)
        try c.encode(RecordingDayWire.id(recording), forKey: .recording)
        try c.encode(RecordingDayWire.timestamp(recordingStartedAt, in: zone), forKey: .recordingStartedAt)
        try c.encode(tz, forKey: .tz)
        try c.encode(day, forKey: .day)
        try c.encode(seq, forKey: .seq)
        try c.encode(RecordingDayWire.timestamp(startedAt, in: zone), forKey: .startedAt)
        try c.encode(duration, forKey: .duration)
        // Always one of the contract's nine keys. The writer measures every chunk (`RecordingDayLevel` never returns
        // a non-finite level), so `null` is only for a chunk whose level is unknown, which the phone then transcribes
        // rather than skip as silent.
        if let level, level.isFinite { try c.encode(level, forKey: .level) } else { try c.encodeNil(forKey: .level) }
    }
}

// MARK: - Control events

/// A control event of a day recording (docs/assistant/day-recap.md「Control events」): everything that is not audio,
/// numbered `n` 0, 1, 2… per recording, so the phone applies them strictly in order and works out the coverage without
/// guessing from the gaps between chunks. The watch keeps each until the phone acknowledges it.
nonisolated struct RecordingDayEvent: Sendable, Hashable {
    /// The event's `type`. One this build doesn't know is kept as it came, so the phone acknowledges it and applies it
    /// as nothing.
    nonisolated struct Kind: RawRepresentable, Codable, Sendable, Hashable {
        var rawValue: String
        init(rawValue: String) { self.rawValue = rawValue }

        /// The microphone opened: a first start, continuing after a pause or an interruption, a new day at midnight.
        static let started = Kind(rawValue: "started")
        /// The microphone closed (`reason`: `RecordingDayContract.Reason`).
        static let paused = Kind(rawValue: "paused")
        /// Chunks `fromSeq…toSeq` the watch could not keep, between `from` and `to` when known.
        static let lost = Kind(rawValue: "lost")
        /// The wall-clock range `from…to` was erased (`id`); `seqs` are the chunks it deleted before the phone had them.
        static let erase = Kind(rawValue: "erase")
        /// The day recording ended: no chunk at or after `seqCount` will come.
        static let closed = Kind(rawValue: "closed")
        /// [ext] A bookmark (`label` `Label.bookmark`) or a meeting mark (`Label.meeting`) at `at`.
        static let mark = Kind(rawValue: "mark")
    }

    /// The labels of a `mark` event [ext]: the hub reads `meeting` as a hint for its recap.
    nonisolated enum Label {
        static let bookmark = ""
        static let meeting = "meeting"
    }

    var n: Int
    var type: Kind
    var at: Date? = nil
    var reason: String? = nil
    var fromSeq: Int? = nil
    var toSeq: Int? = nil
    var from: Date? = nil
    var to: Date? = nil
    var id: UUID? = nil
    /// `erase`: inclusive `[[from, to]]` ranges (`RecordingDayRanges`).
    var seqs: [[Int]]? = nil
    var seqCount: Int? = nil
    var label: String? = nil
}

nonisolated extension RecordingDayEvent {
    static func started(_ n: Int, at: Date) -> Self { Self(n: n, type: .started, at: at) }

    static func paused(_ n: Int, at: Date, reason: String) -> Self { Self(n: n, type: .paused, at: at, reason: reason) }

    static func lost(_ n: Int, seqs: ClosedRange<Int>, from: Date? = nil, to: Date? = nil) -> Self {
        Self(n: n, type: .lost, fromSeq: seqs.lowerBound, toSeq: seqs.upperBound, from: from, to: to)
    }

    static func erase(_ n: Int, id: UUID, from: Date, to: Date, seqs: some Sequence<Int>) -> Self {
        Self(n: n, type: .erase, from: from, to: to, id: id, seqs: RecordingDayRanges.ranges(seqs))
    }

    static func closed(_ n: Int, at: Date, seqCount: Int) -> Self { Self(n: n, type: .closed, at: at, seqCount: seqCount) }

    static func mark(_ n: Int, at: Date, label: String) -> Self { Self(n: n, type: .mark, at: at, label: label) }

    /// The seqs an `erase` deleted.
    var erasedSeqs: [Int] { RecordingDayRanges.expand(seqs ?? []) }

    /// The seqs a `lost` names; nil when they are missing or malformed.
    var lostSeqs: ClosedRange<Int>? {
        guard let fromSeq, let toSeq, fromSeq >= 0, fromSeq <= toSeq else { return nil }
        return fromSeq...toSeq
    }

    /// An event the watch could have written. Whether a known type has all its fields is the applier's business: a
    /// refused event would hold back every later one.
    var valid: Bool {
        let numbers = (0..<10_000_000).contains(n) && (seqCount.map { (0..<1_000_000).contains($0) } ?? true)
            && (fromSeq ?? 0) >= 0 && (toSeq ?? 0) >= 0 && (seqs?.count ?? 0) <= 10_000
        let times = RecordingDayWire.finite(at) && RecordingDayWire.finite(from) && RecordingDayWire.finite(to)
        let texts = !type.rawValue.isEmpty && type.rawValue.count <= 40 && (reason?.count ?? 0) <= 100 && (label?.count ?? 0) <= 100
        return numbers && times && texts
    }
}

nonisolated extension RecordingDayEvent: Codable {
    private nonisolated enum CodingKeys: String, CodingKey {
        case n, type, at, reason, from, to, id, seqs, label
        case fromSeq = "from_seq"
        case toSeq = "to_seq"
        case seqCount = "seq_count"
    }

    /// Tolerant: `n` and `type` must be there; any other field that is missing or unreadable is absent.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(n: try c.decode(Int.self, forKey: .n),
                  type: Kind(rawValue: try c.decode(String.self, forKey: .type)),
                  at: RecordingDayWire.optionalDate(c, .at),
                  reason: try? c.decodeIfPresent(String.self, forKey: .reason),
                  fromSeq: try? c.decodeIfPresent(Int.self, forKey: .fromSeq),
                  toSeq: try? c.decodeIfPresent(Int.self, forKey: .toSeq),
                  from: RecordingDayWire.optionalDate(c, .from),
                  to: RecordingDayWire.optionalDate(c, .to),
                  id: c.contains(.id) ? (try? RecordingDayWire.uuid(c, .id)) : nil,
                  seqs: try? c.decodeIfPresent([[Int]].self, forKey: .seqs),
                  seqCount: try? c.decodeIfPresent(Int.self, forKey: .seqCount),
                  label: try? c.decodeIfPresent(String.self, forKey: .label))
    }

    /// Times in UTC: on its own (the watch's library keeps it so) an event has no recording zone.
    func encode(to encoder: Encoder) throws { try encode(to: encoder, zone: .gmt) }

    /// Times in the offset of `zone`, the recording's: how an envelope writes its event.
    func encode(to encoder: Encoder, zone: TimeZone) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(n, forKey: .n)
        try c.encode(type.rawValue, forKey: .type)
        try c.encodeIfPresent(at.map { RecordingDayWire.timestamp($0, in: zone) }, forKey: .at)
        try c.encodeIfPresent(reason, forKey: .reason)
        try c.encodeIfPresent(fromSeq, forKey: .fromSeq)
        try c.encodeIfPresent(toSeq, forKey: .toSeq)
        try c.encodeIfPresent(from.map { RecordingDayWire.timestamp($0, in: zone) }, forKey: .from)
        try c.encodeIfPresent(to.map { RecordingDayWire.timestamp($0, in: zone) }, forKey: .to)
        try c.encodeIfPresent(id.map(RecordingDayWire.id), forKey: .id)
        try c.encodeIfPresent(seqs, forKey: .seqs)
        try c.encodeIfPresent(seqCount, forKey: .seqCount)
        try c.encodeIfPresent(label, forKey: .label)
    }
}

/// An event as it travels (`RecordingDayContract.eventKey`), with the recording it belongs to, so the phone can file
/// it before any chunk of that recording arrived.
nonisolated struct RecordingDayEventEnvelope: Sendable, Equatable {
    var recording: UUID
    var recordingStartedAt: Date
    var tz: String
    var day: String
    var event: RecordingDayEvent
}

nonisolated extension RecordingDayEventEnvelope: RecordingDayMessage {
    /// An event of a day recording on the watch; nil for an entry that isn't one.
    init?(entry: RecordingEntry, event: RecordingDayEvent) {
        guard entry.isDay, let day = entry.day, let tz = entry.timeZone else { return nil }
        self.init(recording: entry.id, recordingStartedAt: entry.createdAt, tz: tz, day: day, event: event)
        guard valid else { return nil }
    }

    var valid: Bool {
        RecordingDayWire.finite(recordingStartedAt) && RecordingDayCalendar.isDay(day) && RecordingDayWire.zone(tz) != nil
            && event.valid
    }

    private nonisolated enum CodingKeys: String, CodingKey {
        case recording, tz, day, event
        case recordingStartedAt = "recording_started_at"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(recording: try RecordingDayWire.uuid(c, .recording),
                  recordingStartedAt: try RecordingDayWire.date(c, .recordingStartedAt),
                  tz: try c.decode(String.self, forKey: .tz),
                  day: try c.decode(String.self, forKey: .day),
                  event: try c.decode(RecordingDayEvent.self, forKey: .event))
    }

    func encode(to encoder: Encoder) throws {
        let zone = RecordingDayWire.zone(tz) ?? .gmt
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(RecordingDayWire.id(recording), forKey: .recording)
        try c.encode(RecordingDayWire.timestamp(recordingStartedAt, in: zone), forKey: .recordingStartedAt)
        try c.encode(tz, forKey: .tz)
        try c.encode(day, forKey: .day)
        try c.encode(Zoned(event: event, zone: zone), forKey: .event)
    }

    /// The event with its times in the recording's offset.
    private nonisolated struct Zoned: Encodable {
        var event: RecordingDayEvent
        var zone: TimeZone
        func encode(to encoder: Encoder) throws { try event.encode(to: encoder, zone: zone) }
    }
}

// MARK: - Acknowledgements

/// The phone's acknowledgement (docs/assistant/day-recap.md「Acknowledgements and retries」): these chunks (`acks`) and
/// events (`events`) of the recording are on its disk, as inclusive `[from, to]` ranges; either list may be empty.
nonisolated struct RecordingDayAck: Sendable, Equatable {
    var recording: UUID
    var acks: [[Int]] = []
    var events: [[Int]] = []
}

nonisolated extension RecordingDayAck: RecordingDayMessage {
    init(recording: UUID, seqs: some Sequence<Int>, events: some Sequence<Int>) {
        self.init(recording: recording, acks: RecordingDayRanges.ranges(seqs), events: RecordingDayRanges.ranges(events))
    }

    init(recording: UUID, seqs: some Sequence<Int>) {
        self.init(recording: recording, acks: RecordingDayRanges.ranges(seqs))
    }

    init(recording: UUID, events: some Sequence<Int>) {
        self.init(recording: recording, events: RecordingDayRanges.ranges(events))
    }

    /// The chunks acknowledged.
    var seqs: [Int] { RecordingDayRanges.expand(acks) }
    /// The events acknowledged, by `n`.
    var eventNumbers: [Int] { RecordingDayRanges.expand(events) }
    var isEmpty: Bool { seqs.isEmpty && eventNumbers.isEmpty }
    var valid: Bool { true }

    private nonisolated enum CodingKeys: String, CodingKey { case recording, acks, events }

    /// Tolerant: a missing or unreadable list is empty (a phone from before events sends only `acks`).
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(recording: try RecordingDayWire.uuid(c, .recording),
                  acks: (try? c.decodeIfPresent([[Int]].self, forKey: .acks)) ?? [],
                  events: (try? c.decodeIfPresent([[Int]].self, forKey: .events)) ?? [])
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(RecordingDayWire.id(recording), forKey: .recording)
        try c.encode(acks, forKey: .acks)
        try c.encode(events, forKey: .events)
    }
}

// MARK: - Erasing and deleting from the phone

/// The phone erased a wall-clock range of a day recording (docs/assistant/day-recap.md「Erasing」) and asks the watch to
/// erase its copies; sent until the watch answers with `RecordingDayErased`.
nonisolated struct RecordingDayEraseRequest: Sendable, Equatable {
    nonisolated struct Erase: Sendable, Equatable {
        var id: UUID
        var from: Date
        var to: Date
    }

    var recording: UUID
    var erase: Erase
}

nonisolated extension RecordingDayEraseRequest: RecordingDayMessage {
    init(recording: UUID, id: UUID, from: Date, to: Date) {
        self.init(recording: recording, erase: Erase(id: id, from: from, to: to))
    }

    var valid: Bool {
        RecordingDayWire.finite(erase.from) && RecordingDayWire.finite(erase.to) && erase.from <= erase.to
    }

    /// The same request as `other`: the same recording and erase id. Its times may differ by the wire's rounding to
    /// milliseconds (a copy read back from a queued transfer), so they don't count.
    func isSame(as other: RecordingDayEraseRequest) -> Bool {
        recording == other.recording && erase.id == other.erase.id
    }

    private nonisolated enum CodingKeys: String, CodingKey { case recording, erase }
    private nonisolated enum EraseKeys: String, CodingKey { case id, from, to }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let erase = try c.nestedContainer(keyedBy: EraseKeys.self, forKey: .erase)
        self.init(recording: try RecordingDayWire.uuid(c, .recording),
                  erase: Erase(id: try RecordingDayWire.uuid(erase, .id), from: try RecordingDayWire.date(erase, .from),
                               to: try RecordingDayWire.date(erase, .to)))
    }

    /// Times in UTC: the request names no zone, and the watch reads any offset.
    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(RecordingDayWire.id(recording), forKey: .recording)
        var nested = c.nestedContainer(keyedBy: EraseKeys.self, forKey: .erase)
        try nested.encode(RecordingDayWire.id(erase.id), forKey: .id)
        try nested.encode(RecordingDayWire.timestamp(erase.from, in: .gmt), forKey: .from)
        try nested.encode(RecordingDayWire.timestamp(erase.to, in: .gmt), forKey: .to)
    }
}

/// The watch's answer to an erase request: done, and these are the chunks it deleted before the phone had them, which
/// count as settled. A repeated request gets the same answer.
nonisolated struct RecordingDayErased: Sendable, Equatable {
    var recording: UUID
    /// The request's `erase.id`.
    var erased: UUID
    var seqs: [[Int]]
}

nonisolated extension RecordingDayErased: RecordingDayMessage {
    init(recording: UUID, erased: UUID, seqs: some Sequence<Int>) {
        self.init(recording: recording, erased: erased, seqs: RecordingDayRanges.ranges(seqs))
    }

    init(recording: UUID, done: RecordingDayEraseDone) {
        self.init(recording: recording, erased: done.id, seqs: done.seqs)
    }

    var erasedSeqs: [Int] { RecordingDayRanges.expand(seqs) }
    var valid: Bool { seqs.count <= 10_000 }

    private nonisolated enum CodingKeys: String, CodingKey { case recording, erased, seqs }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(recording: try RecordingDayWire.uuid(c, .recording), erased: try RecordingDayWire.uuid(c, .erased),
                  seqs: (try? c.decodeIfPresent([[Int]].self, forKey: .seqs)) ?? [])
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(RecordingDayWire.id(recording), forKey: .recording)
        try c.encode(RecordingDayWire.id(erased), forKey: .erased)
        try c.encode(seqs, forKey: .seqs)
    }
}

/// An erase the phone asked for and the watch carried out, kept with the day recording on the watch so a repeated
/// request (its answer was lost) gets the same answer.
nonisolated struct RecordingDayEraseDone: Codable, Sendable, Hashable {
    var id: UUID
    var seqs: [[Int]]
    /// When it was carried out; forgotten `RecordingDayContract.eraseMemoryDays` later.
    var at: Date
}

/// The phone deleted a whole day recording (docs/assistant/day-recap.md「Deleting」); sent until the watch confirms.
nonisolated struct RecordingDayDelete: Sendable, Equatable {
    var recording: UUID
}

nonisolated extension RecordingDayDelete: RecordingDayMessage {
    var valid: Bool { true }

    private nonisolated enum CodingKeys: String, CodingKey { case recording, deleted }

    /// Only `"deleted": true` is a delete.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        guard try c.decode(Bool.self, forKey: .deleted) else {
            throw DecodingError.dataCorruptedError(forKey: .deleted, in: c, debugDescription: "not a delete")
        }
        self.init(recording: try RecordingDayWire.uuid(c, .recording))
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(RecordingDayWire.id(recording), forKey: .recording)
        try c.encode(true, forKey: .deleted)
    }
}

/// The watch deleted its copy of a day recording the phone deleted.
nonisolated struct RecordingDayDeleted: Sendable, Equatable {
    var recording: UUID
}

nonisolated extension RecordingDayDeleted: RecordingDayMessage {
    var valid: Bool { true }

    private nonisolated enum CodingKeys: String, CodingKey {
        case recording
        case deletedAck = "deleted_ack"
    }

    /// Only `"deleted_ack": true` is a confirmation.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        guard try c.decode(Bool.self, forKey: .deletedAck) else {
            throw DecodingError.dataCorruptedError(forKey: .deletedAck, in: c, debugDescription: "not a confirmation")
        }
        self.init(recording: try RecordingDayWire.uuid(c, .recording))
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(RecordingDayWire.id(recording), forKey: .recording)
        try c.encode(true, forKey: .deletedAck)
    }
}

// MARK: - The phone's switch [ext]

/// [ext] The phone's All-Day Recording switch, phone → watch (`commandKey`). It goes as a message, or as user info when
/// the watch app isn't running, so it can arrive late, twice or after a newer one: it carries when the phone sent it, and
/// the watch acts only on a command newer than the last one it acted on, and recent (`WatchDayCommandOrder`).
nonisolated struct RecordingDayCommand: RecordingDayMessage, Equatable {
    /// `RecordingDayContract.Command.start` or `.stop`. One this build doesn't know is passed on, and ignored there.
    var command: String
    /// Seconds since 1970 (`WatchDayCommandOrder.Stamps`). A number rather than an RFC 3339 time: milliseconds could
    /// not keep two commands of the same instant in order.
    var sentAt: Double

    private nonisolated enum CodingKeys: String, CodingKey {
        case command
        case sentAt = "sent_at"
    }

    var valid: Bool { !command.isEmpty && command.count <= 64 && sentAt.isFinite }
}

// MARK: - Ranges

/// Seqs (and event numbers) as the contract's inclusive ranges (`[[from, to]]`), and back.
nonisolated enum RecordingDayRanges {
    /// Sorted, merged ranges of the non-negative numbers given (duplicates and order don't matter).
    static func ranges(_ seqs: some Sequence<Int>) -> [[Int]] {
        runs(seqs).map { [$0.lowerBound, $0.upperBound] }
    }

    /// The same as closed ranges.
    static func runs(_ seqs: some Sequence<Int>) -> [ClosedRange<Int>] {
        var out: [ClosedRange<Int>] = []
        for seq in Set(seqs.filter { $0 >= 0 }).sorted() {
            if let last = out.last, last.upperBound + 1 == seq { out[out.count - 1] = last.lowerBound...seq } else { out.append(seq...seq) }
        }
        return out
    }

    /// The numbers the ranges name, sorted and unique. A malformed range (not one or two numbers, negative, or from >
    /// to) is skipped, and at most `limit` numbers are read, so a bad message can't make the receiver count forever.
    static func expand(_ ranges: [[Int]], limit: Int = 100_000) -> [Int] {
        var out = Set<Int>()
        var budget = limit
        for range in ranges {
            guard (1...2).contains(range.count), let from = range.first, let to = range.last, from >= 0, from <= to else { continue }
            for seq in from...to {
                guard budget > 0 else { return out.sorted() }
                budget -= 1
                out.insert(seq)
            }
        }
        return out.sorted()
    }
}

// MARK: - Capabilities

/// What the phone tells the watch about all-day recording (docs/assistant/day-recap.md「Watch and phone capabilities」),
/// as its application context carries it.
nonisolated struct RecordingDayCapabilities: Sendable, Equatable {
    /// The user agreed to the consent sheet's current version.
    var consent: Bool
    /// The phone's hub lists `recording_day`.
    var hub: Bool
    /// `RecordingDayContract.keepDays(audioDays:)`.
    var keepDays: Int

    /// The application context keys and values.
    var context: [String: Any] {
        [RecordingDayContract.phoneKey: consent ? RecordingDayContract.version : 0,
         RecordingDayContract.phoneHubKey: hub,
         RecordingDayContract.phoneKeepDaysKey: RecordingDayContract.keepDays(audioDays: keepDays)]
    }

    /// Whether a context already says exactly this.
    func isIn(_ ctx: [String: Any]) -> Bool {
        let wanted = context
        return ctx[RecordingDayContract.phoneKey] as? Int == wanted[RecordingDayContract.phoneKey] as? Int
            && ctx[RecordingDayContract.phoneHubKey] as? Bool == hub
            && ctx[RecordingDayContract.phoneKeepDaysKey] as? Int == wanted[RecordingDayContract.phoneKeepDaysKey] as? Int
    }
}

// MARK: - The watch's state

/// [ext] What the watch's all-day recording is doing, for the phone's Today row (application context, watch → phone):
/// the state between chunks, and how far the watch has got.
nonisolated struct RecordingDayWatchState: Sendable, Equatable {
    nonisolated enum State: String, Codable, Sendable { case off, recording, paused, interrupted, stopped }

    /// The open day recording, nil when none.
    var recording: UUID?
    var day: String?
    var state: State
    /// When `state` began.
    var since: Date?
    /// The seq the next chunk gets: every seq below it was recorded, or reported lost or erased.
    var next: Int?
    /// Chunks the phone has not acknowledged yet.
    var pending: Int?
}

nonisolated extension RecordingDayWatchState: Codable {
    private nonisolated enum CodingKeys: String, CodingKey { case recording, day, state, since, next, pending }

    /// Tolerant of a newer watch: a state this build does not know reads as `stopped` (not recording, needs a look).
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let state = try c.decode(String.self, forKey: .state)
        self.init(recording: try? c.decodeIfPresent(UUID.self, forKey: .recording),
                  day: try? c.decodeIfPresent(String.self, forKey: .day),
                  state: State(rawValue: state) ?? .stopped,
                  since: try? c.decodeIfPresent(Date.self, forKey: .since),
                  next: try? c.decodeIfPresent(Int.self, forKey: .next),
                  pending: try? c.decodeIfPresent(Int.self, forKey: .pending))
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encodeIfPresent(recording, forKey: .recording)
        try c.encodeIfPresent(day, forKey: .day)
        try c.encode(state, forKey: .state)
        try c.encodeIfPresent(since, forKey: .since)
        try c.encodeIfPresent(next, forKey: .next)
        try c.encodeIfPresent(pending, forKey: .pending)
    }
}

nonisolated extension RecordingDayWatchState {
    /// The mode is on: recording, or paused until the user taps Continue.
    var isOn: Bool { state == .recording || state == .paused || state == .interrupted }

    /// The same state always gives the same bytes (sorted keys), so the watch can skip a push that changes nothing.
    var data: Data? {
        let encoder = JSONEncoder()
        encoder.outputFormatting = .sortedKeys
        return try? encoder.encode(self)
    }

    init?(data: Data?) {
        guard let data, let value = try? JSONDecoder().decode(Self.self, from: data) else { return nil }
        self = value
    }
}

// MARK: - Calendar, level, hub flag

/// Local days for all-day recording: the Gregorian date in a time zone, written `YYYY-MM-DD` whatever the device's
/// calendar or language, and its midnights found so that a day of 23 or 25 hours (a DST change) ends where it does.
nonisolated enum RecordingDayCalendar {
    /// The local date of a moment.
    static func day(of date: Date, in zone: TimeZone) -> String {
        let parts = calendar(zone).dateComponents([.year, .month, .day], from: date)
        return String(format: "%04d-%02d-%02d", parts.year ?? 0, parts.month ?? 0, parts.day ?? 0)
    }

    /// The first moment of the day after the one `date` falls in.
    static func nextMidnight(after date: Date, in zone: TimeZone) -> Date {
        let calendar = calendar(zone)
        // From noon, which every day has, a day later is never inside a DST jump; that day's start is the answer,
        // also where the clock skips midnight itself (the day then starts at 01:00).
        let start = calendar.startOfDay(for: date)
        let noon = calendar.date(bySettingHour: 12, minute: 0, second: 0, of: start) ?? start.addingTimeInterval(12 * 3600)
        let tomorrow = calendar.date(byAdding: .day, value: 1, to: noon) ?? noon.addingTimeInterval(86_400)
        return calendar.startOfDay(for: tomorrow)
    }

    /// The first moment of a `YYYY-MM-DD` date; nil for text that is not a real date.
    static func start(of day: String, in zone: TimeZone) -> Date? {
        guard isDay(day) else { return nil }
        let numbers = day.split(separator: "-").compactMap { Int($0) }
        let calendar = calendar(zone)
        guard numbers.count == 3,
              let noon = calendar.date(from: DateComponents(year: numbers[0], month: numbers[1], day: numbers[2], hour: 12)),
              // A date the calendar had to roll over (02-30 becomes 03-02) is not the date asked for.
              Self.day(of: noon, in: zone) == day else { return nil }
        return calendar.startOfDay(for: noon)
    }

    /// The midnight that ends a `YYYY-MM-DD` date (a day recording's end); nil for text that is not a real date.
    static func end(of day: String, in zone: TimeZone) -> Date? {
        start(of: day, in: zone).map { nextMidnight(after: $0, in: zone) }
    }

    /// Whether text has the form `YYYY-MM-DD` (ASCII digits).
    static func isDay(_ text: String) -> Bool {
        let chars = Array(text.utf8)
        guard chars.count == 10 else { return false }
        return chars.enumerated().allSatisfy { i, c in i == 4 || i == 7 ? c == UInt8(ascii: "-") : (UInt8(ascii: "0")...UInt8(ascii: "9")).contains(c) }
    }

    private static func calendar(_ zone: TimeZone) -> Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.locale = Locale(identifier: "en_US_POSIX")
        calendar.timeZone = zone
        return calendar
    }
}

/// The level a chunk is skipped by: the RMS of all its samples in dBFS (a power average, so a few words in ten quiet
/// seconds still lift it), with digital silence at `floor`.
nonisolated enum RecordingDayLevel {
    static let floor = -160.0

    static func dBFS(sumOfSquares: Double, count: Int) -> Double {
        guard count > 0, sumOfSquares.isFinite, sumOfSquares > 0 else { return floor }
        let level = 10 * log10(sumOfSquares / Double(count))
        return level.isFinite ? max(floor, level) : floor
    }
}

/// The phone's copy of whether its hub takes day recordings (`recording_day`), for times without a hub `hello` yet
/// (a launch in the background to receive a chunk), like `MeetingAttendanceDefaults`. Rarely changes: a user default.
nonisolated enum RecordingDayDefaults {
    static let feature = RecordingDayContract.feature
    static let offeredKey = "recording.day.offered"

    /// From the hub's features when known, else what the last `hello` said.
    static func offered(_ hubFeatures: Set<String>, defaults: UserDefaults = .standard) -> Bool {
        hubFeatures.isEmpty ? defaults.bool(forKey: offeredKey) : hubFeatures.contains(feature)
    }

    /// Keeps what the hub said; writes only when it changed (every write redraws the views reading defaults).
    static func remember(offered: Bool, defaults: UserDefaults = .standard) {
        if defaults.object(forKey: offeredKey) == nil || defaults.bool(forKey: offeredKey) != offered {
            defaults.set(offered, forKey: offeredKey)
        }
    }
}
