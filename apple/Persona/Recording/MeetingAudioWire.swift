import AVFoundation
import Foundation

// Hub transcription's wire and rules on the phone (#3862 phase 5; the contract is docs/assistant/meeting-minutes.md
// 「Phase 5: hub transcription」, the phone's side docs/apple/recording.md「Hub transcription (phase 5)」). Everything here
// is pure, so the tests hold it to the contract without a socket: the frames, the timeline and its limits, the audio
// ring and what falls out of it, the mode in effect, what the page shows, the network rule and the gap uploads' plan.
// Not only the iPhone's: the upload (`RecordingUpload`, compiled for every app that shares `Persona/`) carries the
// timeline; the uplink itself is the iPhone's.

/// Sample positions and seconds on the recording's clock: the writer's frame position, 16 kHz, paused time left out.
nonisolated enum MeetingAudioClock {
    static let rate = 16_000

    /// The sample at `seconds` (rounded to the nearest one, as the hub rounds times).
    static func sample(_ seconds: Double) -> Int64 {
        guard seconds.isFinite, seconds > 0 else { return 0 }
        return Int64((seconds * Double(rate)).rounded())
    }

    static func seconds(_ sample: Int64) -> Double { Double(sample) / Double(rate) }

    /// `seconds` rounded to a sample.
    static func rounded(_ seconds: Double) -> Double { Self.seconds(sample(seconds)) }

    /// `PUT …/audio?at=`'s value: a plain decimal number (the hub takes digits and one dot only: no exponent, no sign),
    /// exact to the sample.
    static func query(_ seconds: Double) -> String {
        let s = sample(seconds)
        let whole = s / Int64(rate), part = s % Int64(rate)
        guard part != 0 else { return String(whole) }
        // 1/16000 s is 0.0000625 s: seven decimals hold every sample exactly.
        var frac = String(part * 625)
        frac = String(repeating: "0", count: max(0, 7 - frac.count)) + frac
        while frac.hasSuffix("0") { frac.removeLast() }
        return "\(whole).\(frac)"
    }
}

/// A stretch of the recording's clock, in seconds (`[from, to]` on the wire).
nonisolated struct MeetingGap: Codable, Hashable, Sendable {
    var from: Double
    var to: Double

    var seconds: Double { max(0, to - from) }

    init(from: Double, to: Double) {
        self.from = from
        self.to = to
    }

    init(from decoder: Decoder) throws {
        var c = try decoder.unkeyedContainer()
        from = try c.decode(Double.self)
        to = try c.decode(Double.self)
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.unkeyedContainer()
        try c.encode(from)
        try c.encode(to)
    }

    /// Overlapping or touching stretches as one, in order (gaps less than `margin` apart merge too, as the hub's do).
    static func union(_ gaps: [MeetingGap], margin: Double = 0) -> [MeetingGap] {
        var out: [MeetingGap] = []
        for g in gaps.filter({ $0.to > $0.from }).sorted(by: { ($0.from, $0.to) < ($1.from, $1.to) }) {
            if let last = out.last, g.from <= last.to + margin {
                out[out.count - 1].to = max(last.to, g.to)
            } else {
                out.append(g)
            }
        }
        return out
    }

    /// Reads `[[from, to], …]` leniently: a pair of another shape, or one that ends before it starts, is left out.
    static func list(_ value: JSONValue?) -> [MeetingGap] {
        guard case .array(let items)? = value else { return [] }
        return items.compactMap { item in
            guard case .array(let pair) = item, pair.count == 2, let a = pair[0].meetingNumber, let b = pair[1].meetingNumber,
                  a.isFinite, b.isFinite, a >= 0, b >= a else { return nil }
            return MeetingGap(from: a, to: b)
        }
    }
}

/// The caption being said now (`interim {start, text}`), replaced by the next one.
nonisolated struct MeetingInterim: Codable, Hashable, Sendable {
    var start: Double
    var text: String
}

/// One line of the meeting's live log as the hub has it (`line {n, start, end, text}` on the socket; `lines[]` of
/// `GET …/live?from=`): in hub mode the hub's own sentences (`source: hub`), or the phone's text where the hub has none
/// (`device`, the fallback). `n` is its position in the live log, never renumbered.
nonisolated struct MeetingHubLine: Codable, Hashable, Sendable, Identifiable {
    var n: Int
    var start: Double
    var end: Double
    var text: String
    /// `hub` or `device`; nil from a socket's `line` frame, which only ever carries the hub's own.
    var source: String?

    var id: Int { n }
}

/// `transcription` on `GET …/live` and in `detail.meta` (docs/assistant/meeting-minutes.md「Gaps and the final pass」):
/// the mode decided for a recording and, in hub mode, how far the hub got.
nonisolated struct MeetingTranscriptionView: Hashable, Sendable {
    /// `device` or `hub`; another value reads as `device` (the hub transcribes nothing it doesn't name).
    var mode: MeetingTranscriptionMode
    /// `live`, `finishing`, `final` or `failed` (an open set: kept as it came; `known` reads it).
    var state: String?
    var gaps: [MeetingGap]
    /// The socket's standing problem (`upstream_unavailable`, `upstream_failed`, `no_interim`; an open set).
    var problem: String?
    /// The live log is being filled from the phone's text.
    var fallback: Bool

    nonisolated enum State: String, Sendable { case live, finishing, final, failed }

    /// The state; one the app doesn't know reads as `live` (the hub is still at it).
    var known: State? {
        guard mode == .hub else { return nil }
        return state.map { State(rawValue: $0) ?? .live } ?? .live
    }

    /// The hub is done with it: its text is the transcript (`final`), or it made nothing usable (`failed`).
    var settled: Bool { known == .final || known == .failed }

    init(mode: MeetingTranscriptionMode, state: String? = nil, gaps: [MeetingGap] = [], problem: String? = nil, fallback: Bool = false) {
        self.mode = mode; self.state = state; self.gaps = gaps; self.problem = problem; self.fallback = fallback
    }

    /// Leniently from the JSON value: nil when it isn't an object with a `mode` string.
    init?(_ value: JSONValue?) {
        guard case .object(let o)? = value, let mode = o["mode"]?.meetingText else { return nil }
        self.mode = MeetingTranscriptionMode(rawValue: mode) ?? .device
        state = o["state"]?.meetingText
        gaps = MeetingGap.list(o["gaps"])
        problem = o["problem"]?.meetingText
        fallback = o["fallback"]?.bool ?? false
    }

    /// `detail.meta.transcription` of a recording's detail (`GET /api/v1/kb/recordings/{id}`).
    init?(meta: JSONValue?) {
        guard case .object(let o)? = meta else { return nil }
        self.init(o["transcription"])
    }
}

/// The wire's values as they came: a number only as a number, a text only as a text (`JSONValue.string` would read a
/// number as its digits).
nonisolated extension JSONValue {
    var meetingNumber: Double? { if case .number(let n) = self { n } else { nil } }
    var meetingText: String? { if case .string(let s) = self { s } else { nil } }
}

// MARK: Frames

/// What the phone sends on the audio socket (text frames; the audio goes as binary frames of whole PCM16 samples).
nonisolated enum MeetingAudioOut {
    /// The first frame, within 10 seconds of the upgrade: the oldest second still held, and the whole timeline so far.
    static func start(at: Double, timeline: MeetingTimeline) -> String {
        encode(Start(at: MeetingAudioClock.rounded(at), timeline: timeline))
    }

    /// The next binary frame starts at `at`.
    static func seek(at: Double) -> String { encode(At(type: "seek", at: MeetingAudioClock.rounded(at))) }

    /// The role from `at` on.
    static func role(at: Double, attendance: MeetingAttendance) -> String {
        encode(Role(at: MeetingAudioClock.rounded(at), attendance: attendance.rawValue))
    }

    /// Persona starts (`on`, with the answer it says) or stops speaking at `at`.
    static func speaking(on: Bool, at: Double, text: String? = nil) -> String {
        let said = on ? text.map { MeetingTimeline.cut($0) }.flatMap { $0.isEmpty ? nil : $0 } : nil
        return encode(Speaking(on: on, at: MeetingAudioClock.rounded(at), text: said))
    }

    /// The recording stopped at `at`.
    static func end(at: Double) -> String { encode(At(type: "end", at: MeetingAudioClock.rounded(at))) }

    private struct Start: Encodable { let type = "start"; var at: Double; var timeline: MeetingTimeline }
    private struct At: Encodable { var type: String; var at: Double }
    private struct Role: Encodable { let type = "role"; var at: Double; var attendance: String }
    private struct Speaking: Encodable { let type = "speaking"; var on: Bool; var at: Double; var text: String? }

    private static func encode(_ value: some Encodable) -> String {
        let data = (try? JSONEncoder().encode(value)) ?? Data()
        return String(decoding: data, as: UTF8.self)
    }
}

/// What the hub sends on the audio socket (docs/assistant/meeting-minutes.md「Audio from the phone」).
nonisolated enum MeetingAudioIn: Equatable, Sendable {
    /// Send from `from` (the end of the latest audio the hub holds); `gaps` are earlier stretches it lacks; `next` is the
    /// live log's length.
    case ready(from: Double, gaps: [MeetingGap], next: Int)
    /// Stored up to `at` (not transcribed: stored).
    case ack(at: Double)
    case interim(MeetingInterim)
    case line(MeetingHubLine)
    /// Transcription is degraded (`upstream_unavailable`, `upstream_failed`, `no_interim`; an open set); nil: it is back.
    case problem(String?)
    /// The socket closes after it. An open set: act on the code, never on the close.
    case error(code: String)
    /// A frame of a type the phone doesn't know, or one it can't read: nothing to do.
    case other

    static func decode(_ text: String) -> MeetingAudioIn {
        guard let value = try? JSON.decoder.decode(JSONValue.self, from: Data(text.utf8)), case .object(let o) = value,
              let type = o["type"]?.meetingText else { return .other }
        switch type {
        case "ready":
            guard let from = o["from"]?.meetingNumber, from.isFinite, from >= 0 else { return .other }
            return .ready(from: from, gaps: MeetingGap.list(o["gaps"]), next: position(o["next"]) ?? 0)
        case "ack":
            guard let at = o["at"]?.meetingNumber, at.isFinite, at >= 0 else { return .other }
            return .ack(at: at)
        case "interim":
            guard let start = o["start"]?.meetingNumber, let text = o["text"]?.meetingText else { return .other }
            return .interim(MeetingInterim(start: start, text: text))
        case "line":
            guard let n = position(o["n"]), let start = o["start"]?.meetingNumber, let end = o["end"]?.meetingNumber,
                  let text = o["text"]?.meetingText else { return .other }
            return .line(MeetingHubLine(n: n, start: start, end: end, text: text, source: o["source"]?.meetingText ?? "hub"))
        case "problem":
            return .problem(o["code"]?.meetingText)
        case "error":
            return .error(code: o["code"]?.meetingText ?? "")
        default:
            return .other
        }
    }

    /// A live log position: a whole number of 0 or more (anything else is no position).
    static func position(_ value: JSONValue?) -> Int? {
        guard let n = value?.meetingNumber, n.isFinite, n >= 0, n < 1e12, n == n.rounded() else { return nil }
        return Int(n)
    }
}

// MARK: The timeline

/// The meeting's role changes and the stretches Persona spoke in, as the hub takes them (`{"roles", "speaking"}`, in the
/// socket's `start` and on every hub-mode upload; docs/assistant/meeting-minutes.md「The timeline」). Only ended stretches:
/// one still being said goes as a `speaking` frame. Within the hub's limits, always: past them the hub would ignore the
/// whole timeline, so the phone sends the first 100 role changes and the first 1000 stretches (what the hub keeps of a
/// union too), each entry well formed, a text cut at 4000 bytes on a character boundary.
nonisolated struct MeetingTimeline: Encodable, Equatable, Sendable {
    struct Role: Encodable, Equatable, Sendable { var at: Double; var attendance: String }
    struct Span: Encodable, Equatable, Sendable { var start: Double; var end: Double; var text: String? }

    var roles: [Role]
    var speaking: [Span]

    static let maxRoles = 100
    static let maxSpans = 1000
    static let maxTextBytes = 4000

    init(roles: [Role] = [], speaking: [Span] = []) {
        self.roles = roles
        self.speaking = speaking
    }

    /// The timeline of an attendance: its role changes and its ended spoken stretches.
    init(_ attendance: RecordingAttendance) {
        self.init(roles: attendance.roles, spoken: attendance.spoken)
    }

    init(roles changes: [RecordingRoleChange], spoken: [RecordingSpan]) {
        roles = Array(changes.filter { $0.at.isFinite && $0.at >= 0 }
            .map { Role(at: MeetingAudioClock.rounded($0.at), attendance: $0.attendance.rawValue) }
            .prefix(Self.maxRoles))
        speaking = Array(spoken.filter { $0.start.isFinite && $0.end.isFinite && $0.start >= 0 && $0.end >= $0.start }
            .sorted { ($0.start, $0.end) < ($1.start, $1.end) }
            .map { span in
                let text = span.text.map(Self.cut).flatMap { $0.isEmpty ? nil : $0 }
                return Span(start: MeetingAudioClock.rounded(span.start), end: MeetingAudioClock.rounded(span.end), text: text)
            }
            .prefix(Self.maxSpans))
    }

    /// `text` cut at `maxTextBytes` of UTF-8, on a character boundary.
    static func cut(_ text: String) -> String {
        guard text.utf8.count > maxTextBytes else { return text }
        var out = ""
        var bytes = 0
        for ch in text {
            let n = String(ch).utf8.count
            if bytes + n > maxTextBytes { break }
            out.append(ch)
            bytes += n
        }
        return out
    }
}

// MARK: Audio

/// PCM16 little-endian, mono, 16 kHz, as the socket and `PUT …/audio` take it.
nonisolated enum MeetingPCM {
    /// The samples of a converted buffer (16 kHz mono float), clipped to ±1.
    static func samples(_ buffer: AVAudioPCMBuffer) -> [Int16] {
        let n = Int(buffer.frameLength)
        guard n > 0 else { return [] }
        if let floats = buffer.floatChannelData?[0] {
            return (0..<n).map { sample(floats[$0]) }
        }
        if let ints = buffer.int16ChannelData?[0] {
            return Array(UnsafeBufferPointer(start: ints, count: n))
        }
        return []
    }

    static func sample(_ x: Float) -> Int16 {
        guard x.isFinite else { return 0 }
        return Int16((max(-1, min(1, x)) * 32767).rounded())
    }

    /// `samples` as the wire's bytes.
    static func data(_ samples: ArraySlice<Int16>) -> Data {
        var out = Data(capacity: samples.count * 2)
        for s in samples {
            let le = s.littleEndian
            withUnsafeBytes(of: le) { out.append(contentsOf: $0) }
        }
        return out
    }

    /// About 100 ms a frame (the contract's), far under the 64 KiB a binary frame may hold.
    static let frameSamples = 1600
    /// The most a binary frame may hold, in samples (64 KiB of whole samples).
    static let maxFrameSamples = (64 << 10) / 2
}

/// The last 60 seconds of the recording's audio, in memory, whatever the hub acknowledged (docs/assistant/meeting-minutes.md
/// 「Backpressure」): what a reconnect sends again from `ready.from`. Positions are samples on the recording's clock. Audio
/// that falls out before the hub acknowledged it is a gap (`lost`), for the uploads after the meeting; the hub's own
/// `gaps` say what it actually lacks. A value type, so the tests can drive it; `MeetingAudioUplink` keeps one under a lock.
nonisolated struct MeetingAudioRing: Sendable {
    /// What goes next on the socket: a `seek` (the audio jumps), or audio starting at `at`.
    nonisolated enum Out: Equatable, Sendable {
        case seek(Int64)
        case audio(Data, at: Int64)
    }

    static let seconds = 60

    let capacity: Int
    private var storage: [Int16]
    /// The oldest sample held, and one past the newest.
    private(set) var start: Int64 = 0
    private(set) var end: Int64 = 0
    private var started = false
    /// The hub stored everything before this (its last `ack`).
    private(set) var acked: Int64 = 0
    /// The next sample to send; nil while the socket isn't streaming (before `ready`, while down).
    private(set) var cursor: Int64?
    /// A `seek` to send before the next audio.
    private var seekTo: Int64?
    /// Stretches (samples, `[from, to)`) that fell out of the ring unacknowledged, or never reached it: gaps.
    private(set) var lost: [Range<Int64>] = []

    init(seconds: Int = MeetingAudioRing.seconds) {
        capacity = seconds * MeetingAudioClock.rate
        storage = [Int16](repeating: 0, count: capacity)
    }

    var isEmpty: Bool { end == start }
    var streaming: Bool { cursor != nil }

    /// Adds `samples` captured at `position`. The writer's clock has no holes, so they normally follow on; a jump ahead
    /// starts the ring again there (what it held, and the jump, are gaps), and audio the ring already has is dropped.
    mutating func append(_ samples: [Int16], at position: Int64) {
        guard !samples.isEmpty else { return }
        if !started {
            started = true
            start = position
            end = position
        }
        var first = 0
        if position < end {
            first = Int(min(Int64(samples.count), end - position))
            if first == samples.count { return }
        } else if position > end {
            lose(from: max(start, acked), to: end)
            lose(from: end, to: position)
            start = position
            end = position
            if cursor != nil {
                cursor = position
                seekTo = position
            }
        }
        for i in first..<samples.count {
            storage[Int((end % Int64(capacity) + Int64(capacity)) % Int64(capacity))] = samples[i]
            end += 1
        }
        if end - start > Int64(capacity) {
            let newStart = end - Int64(capacity)
            // While the hub acknowledges as the audio goes, its `ack` lies inside the 60 seconds held: none of this is lost.
            lose(from: max(start, acked), to: newStart)
            start = newStart
            if let c = cursor, c < start {
                cursor = start
                seekTo = start
            }
        }
    }

    /// The hub said `ready {from}`: audio goes from there, with a `seek` to the oldest sample held when the hub's end is
    /// older than that (the stretch between is a gap); from a point the ring hasn't reached yet, it waits for it.
    mutating func ready(from: Int64) {
        acked = max(acked, min(from, end))
        if started, from < start {
            cursor = start
            seekTo = start
        } else {
            cursor = from
            seekTo = nil
        }
    }

    /// Nothing from `position` on is the meeting's (`MeetingAudioUplink.cut`): what the ring holds from there goes, and
    /// is never sent.
    mutating func cut(at position: Int64) {
        guard started, position < end else { return }
        end = max(start, position)
        if let c = cursor, c > end { cursor = end }
    }

    /// The socket went: nothing streams until the next `ready`.
    mutating func stop() {
        cursor = nil
        seekTo = nil
    }

    /// `ack {at}`: the hub stored everything before `at`.
    mutating func ack(_ at: Int64) { acked = max(acked, at) }

    /// What goes next, at most `maxSamples` of audio; nil when nothing does (not streaming, or all sent).
    mutating func next(maxSamples: Int = MeetingPCM.frameSamples) -> Out? {
        guard let c = cursor else { return nil }
        if let s = seekTo {
            seekTo = nil
            return .seek(s)
        }
        if started, c < start {
            cursor = start
            return .seek(start)
        }
        guard c < end else { return nil }
        let n = Int(min(Int64(max(1, min(maxSamples, MeetingPCM.maxFrameSamples))), end - c))
        var slice = [Int16]()
        slice.reserveCapacity(n)
        for i in 0..<n {
            slice.append(storage[Int(((c + Int64(i)) % Int64(capacity) + Int64(capacity)) % Int64(capacity))])
        }
        cursor = c + Int64(n)
        return .audio(MeetingPCM.data(slice[...]), at: c)
    }

    /// The lost stretches in seconds, merged.
    var lostGaps: [MeetingGap] {
        MeetingGap.union(lost.map { MeetingGap(from: MeetingAudioClock.seconds($0.lowerBound), to: MeetingAudioClock.seconds($0.upperBound)) })
    }

    /// `[from, to)` fell out unacknowledged (or never reached the ring); nothing when `from >= to`, as when the hub
    /// acknowledged up to `to` or past it. Compared before they make a range: `..<` traps on reversed bounds, and here
    /// that is the recorder's tap thread (#7740: an iPhone crashed on it once a hub-mode meeting filled the ring).
    private mutating func lose(from: Int64, to: Int64) {
        guard from < to else { return }
        let range = from..<to
        if let last = lost.last, last.upperBound >= range.lowerBound {
            lost[lost.count - 1] = min(last.lowerBound, range.lowerBound)..<max(last.upperBound, range.upperBound)
        } else {
            lost.append(range)
        }
    }
}

// MARK: Rules

/// The rules hub transcription runs by on the phone (pure, for the tests).
nonisolated enum MeetingTranscriptionRule {
    /// What an upload's `200` said about the mode: `transcription` (nil when the field is absent: an older manager) and
    /// `transcription_problem`.
    struct Answer: Equatable, Sendable {
        var mode: String?
        var problem: String?
    }

    /// The mode in effect.
    struct Decided: Equatable, Sendable {
        var mode: MeetingTranscriptionMode
        var problem: String?
        /// An older manager answered (a `200` without `transcription`): `device` for the rest of the recording, whatever
        /// a later `200` says (the phone stops the uplink after a rollback mid-meeting).
        var olderManager = false
    }

    /// The mode in effect after an upload's `200` (docs/assistant/meeting-minutes.md「Choosing the mode」): nil while the
    /// meeting named no mode (`asked` nil: the hub didn't list `meeting_transcription`). `hub` only when the phone asked
    /// for it and the hub says so: a meeting's audio never leaves the phone otherwise. A `200` without `transcription` is
    /// an older manager: `device`, for good.
    static func decide(_ current: Decided?, asked: MeetingTranscriptionMode?, answer: Answer) -> Decided? {
        guard asked != nil else { return nil }
        if current?.olderManager == true { return current }
        guard let mode = answer.mode else { return Decided(mode: .device, problem: nil, olderManager: true) }
        let hub = mode == MeetingTranscriptionMode.hub.rawValue && asked == .hub
        return Decided(mode: hub ? .hub : .device, problem: answer.problem)
    }

    /// The socket's codes that mean something to the phone; any other (an open set) reads as "hub transcription stopped
    /// for this meeting", and the phone keeps its own.
    nonisolated enum Reaction: Equatable, Sendable {
        /// The hub has no upload of the recording yet: the upload goes first, then the socket again.
        case uploadFirst
        /// Try again in a while (a hiccup, or a frame the hub couldn't read: a fresh socket starts clean).
        case retry
        /// Another socket took the meeting over (`replaced`): hub transcription goes on through it, so this phone opens
        /// no other socket while it runs (two would replace each other in turn), and its hub-mode time stays: what the hub
        /// lacks still goes up as gaps after the meeting.
        case yield
        /// Hub transcription stopped for this meeting: no more socket, its hub-mode time ends here for good
        /// (`RecordingAttendance.stopHub`), and the phone's own transcription goes on.
        case stop
    }

    static func reaction(toError code: String) -> Reaction {
        switch code {
        case "recording_not_found": .uploadFirst
        case "bad_frame", "hub_failed": .retry
        case "replaced": .yield
        // `audio_full`, `recording_deleted`, `kb_read_only`, `not_hub`, `transcript_expired`, `transcription_final`, and
        // every code the phone doesn't know.
        default: .stop
        }
    }

    /// A socket that closed without an `error` frame: the handshake's status when the hub refused it (403 answer-only and
    /// extension tokens, 404 a hub without the route, 401 a device it doesn't know), else a dropped link.
    static func reaction(toClose status: Int?) -> Reaction {
        switch status {
        case 401?, 403?, 404?, 405?, 410?: .stop
        default: .retry
        }
    }

    /// How long before the next socket after `failures` in a row: 1, 2, 4, 8, 16, then 30 seconds.
    static func backoff(_ failures: Int) -> TimeInterval {
        min(30, pow(2, Double(max(0, min(failures, 6) - 1))))
    }

    /// The `bad_frame`s in a row after which the phone stops trying: a frame it keeps getting wrong is its own bug.
    static let maxBadFrames = 3

    /// A socket counts as healthy, and the failures in a row (the backoff, the `bad_frame`s) start again from none, only
    /// once the hub acknowledged audio on it (its first `ack`) or it stayed up this long: a hub that fails right after
    /// `ready` is not tried again every second, each time resending the 60 seconds held.
    static let healthyAfter: Duration = .seconds(30)

    /// Whether audio may go now (docs/assistant/meeting-minutes.md「On the phone」, "Decided" 4): cellular data is allowed,
    /// unless the owner turned on **Hub transcription on Wi-Fi only**; never without a network. A network not known yet
    /// counts as not Wi-Fi. With the switch on, the socket and the gap uploads also ask the system to keep off cellular
    /// and expensive networks (`URLRequest.keepOnWiFi`), so a path that changes after this check never carries them.
    static func mayStream(wifiOnly: Bool, network: MeetingNetworkKind) -> Bool {
        switch network {
        case .none: false
        case .unmetered: true
        case .metered, .unknown: !wifiOnly
        }
    }

    /// `mayStream` for a hub reached through a tunnel (its SSH tunnel, local proxy or in-app tunnel, `HubTransport.via`):
    /// `tunnel` is when its requests began to go through it (nil: straight to the hub), `meteredUntil` the last moment the
    /// phone's path was on cellular or an expensive network (nil: not since launch). The system's guard
    /// (`URLRequest.keepOnWiFi`) covers only the hop to the tunnel's loopback address; the tunnel's own connection stays
    /// on the path it was made on, so a tunnel opened on cellular keeps carrying the audio there after the phone joins
    /// Wi-Fi. With **Hub transcription on Wi-Fi only**, audio goes through a tunnel only while the phone is on Wi-Fi and
    /// has been off cellular since the tunnel began, checked right before each socket and each gap upload.
    static func mayStream(wifiOnly: Bool, network: MeetingNetworkKind, tunnel: Date?, meteredUntil: Date?) -> Bool {
        guard mayStream(wifiOnly: wifiOnly, network: network) else { return false }
        guard wifiOnly, let tunnel else { return true }
        return network == .unmetered && (meteredUntil.map { $0 < tunnel } ?? true)
    }

    /// What the page and the Live Activity show (docs/assistant/meeting-minutes.md「On the phone」).
    struct Shown: Equatable, Sendable {
        /// The hub's caption being said now.
        var hubInterim: Bool
        /// The hub's lines (its `line` frames and the live log read with `?from=`).
        var hubLines: Bool
        /// The phone's own lines (its transcript).
        var phoneLines: Bool
        /// With a notice that transcription has fallen back to the phone.
        var fellBack: Bool
    }

    /// How long a socket that was up may be away (dropped and opening again, or opening again on Continue) before the
    /// page says transcription fell back: a brief reconnect is no fallback.
    static let reconnectGrace: Duration = .seconds(5)

    /// `hub`: the mode in effect is hub; `socket`: the audio socket's state; `everUp`: it was up once in this meeting (the
    /// first connect is no fallback); `away`: a socket that was up has been reconnecting longer than `reconnectGrace`;
    /// `problem`: the standing one; `stopped`: the uplink stopped for good.
    ///
    /// Transcription has fallen back (the notice) only when the uplink stopped, a problem other than `no_interim` stands,
    /// the phone waits for Wi-Fi, or the socket is down (one that was up, once `away`). Paused, at its end after Stop or
    /// ended is no fallback: nothing is being transcribed then.
    static func shown(hub: Bool, socket: MeetingAudioSocket, everUp: Bool, away: Bool, problem: String?, stopped: Bool,
                      role: MeetingAttendance) -> Shown {
        guard hub else { return Shown(hubInterim: false, hubLines: false, phoneLines: true, fellBack: false) }
        let up = socket == .ready
        let degraded = problem != nil && problem != "no_interim"
        let healthy = up && !stopped && !degraded
        let resting = [.paused, .ending, .ended].contains(socket)
        let gone: Bool = switch socket {
        case .waitingForWiFi: true
        case .down: !everUp || away
        case .connecting: everUp && away
        default: false
        }
        let fellBack = stopped || (degraded && !resting) || gone
        // Role off: the hub's captions still come (they reach only this phone), but no line: the phone's own lines.
        let lines = healthy && role != .off
        return Shown(hubInterim: healthy, hubLines: lines, phoneLines: !lines, fellBack: fellBack)
    }
}

/// The audio socket of the recording being made (`MeetingTranscription.Socket`).
nonisolated enum MeetingAudioSocket: String, Equatable, Sendable {
    /// No socket: the meeting is transcribed on the phone, isn't being recorded here, or its uplink stopped.
    case off
    /// Hub mode asked for; the first upload's `200` isn't in yet (the audio is kept meanwhile).
    case waitingForMode
    /// **Hub transcription on Wi-Fi only**, and no Wi-Fi: no audio leaves the phone; the time becomes gaps.
    case waitingForWiFi
    /// Opened; the hub's `ready` isn't in yet.
    case connecting
    /// Up: the audio goes.
    case ready
    /// It dropped (or there is no network): another comes.
    case down
    /// The recording is paused: closed until it goes on.
    case paused
    /// `end` sent at Stop: the hub's last lines come, then it closes.
    case ending
    /// Closed after the meeting.
    case ended
}

/// The kind of network the phone is on now, as the uplink's rule needs it.
nonisolated enum MeetingNetworkKind: String, Sendable {
    case none
    /// Wi-Fi or wired, and not marked expensive (not a phone's hotspot).
    case unmetered
    /// Cellular, or a network marked expensive.
    case metered
    /// Not known yet (the path monitor hasn't answered): not Wi-Fi.
    case unknown
}

// MARK: Gap uploads

/// Uploading what the hub lacks after the meeting (`PUT …/audio?at=`, docs/assistant/meeting-minutes.md「Gaps and the final
/// pass」).
nonisolated enum MeetingGapUpload {
    /// At most this much audio a request: 10 MiB is about 5 minutes and 27 seconds of PCM16 at 16 kHz.
    static let maxSeconds: Double = 300

    /// One request: audio from `at` for `seconds`.
    struct Piece: Equatable, Sendable {
        var at: Double
        var seconds: Double
    }

    /// The requests that fill `gaps`, in order, each at most `maxSeconds`, rounded to samples; only what lies inside the
    /// phone's audio (`held`, the recording's length here: a stretch past it has nothing to send) and inside `within`,
    /// the recording's hub-mode time (`MeetingTranscription.uploadable`): a gap the hub has where the recording was a
    /// Note, or after a rollback, is never sent, and keeps the phone's text. Nothing `within`: nothing at all.
    static func plan(_ gaps: [MeetingGap], held: Double, within allowed: [MeetingGap],
                     maxSeconds: Double = MeetingGapUpload.maxSeconds) -> [Piece] {
        var out: [Piece] = []
        for gap in intersection(MeetingGap.union(gaps), MeetingGap.union(allowed)) {
            var at = MeetingAudioClock.rounded(max(0, gap.from))
            let to = MeetingAudioClock.rounded(min(gap.to, held))
            while to - at >= 1.0 / Double(MeetingAudioClock.rate) {
                let seconds = MeetingAudioClock.rounded(min(maxSeconds, to - at))
                out.append(Piece(at: at, seconds: seconds))
                at = MeetingAudioClock.rounded(at + seconds)
            }
        }
        return out
    }

    /// The stretches both `a` and `b` cover (each in order, without overlaps, as `MeetingGap.union` gives them).
    static func intersection(_ a: [MeetingGap], _ b: [MeetingGap]) -> [MeetingGap] {
        var out: [MeetingGap] = []
        var i = 0, j = 0
        while i < a.count, j < b.count {
            let from = max(a[i].from, b[j].from), to = min(a[i].to, b[j].to)
            if to > from { out.append(MeetingGap(from: from, to: to)) }
            if a[i].to < b[j].to { i += 1 } else { j += 1 }
        }
        return out
    }

    /// How long before another gap pass after `passes` in a row left a meeting `later` (the hub or the network away):
    /// 15, 30, 60, 120, 240, then every 300 seconds, so the gaps still reach the hub before its final pass (at most 30
    /// minutes after the `complete: true` upload).
    static func retryDelay(_ passes: Int) -> Double {
        min(300, 15 * pow(2, Double(max(0, min(passes, 6) - 1))))
    }

    /// What to do after a refused `PUT …/audio`, in the contract's order of errors.
    nonisolated enum Reaction: Equatable, Sendable {
        /// Nothing more to upload for this recording (final pass started, not hub mode, trimmed, deleted, full, gone).
        case done
        /// The hub has no upload of it: the upload goes first, then again.
        case uploadFirst
        /// The piece was too large: halve it.
        case smaller
        /// Not now (the hub or the network away, a read-only knowledge base): the next pass tries again.
        case later
    }

    static func reaction(status: Int, code: String?) -> Reaction {
        switch (status, code) {
        case (404, "recording_not_found"): .uploadFirst
        case (413, "too_large"): .smaller
        case (403, "kb_read_only"): .later
        case (409, _), (410, _), (404, _), (413, _), (400, _), (401, _), (403, _): .done
        default: .later
        }
    }
}
