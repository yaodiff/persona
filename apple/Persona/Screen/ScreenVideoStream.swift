#if os(iOS) && canImport(PersonaScreenKit)
import CoreMedia
import CoreVideo
import Foundation
import PersonaScreenKit
import VideoToolbox

// The live screen's video channel on the phone (#3954, docs/extensions/computer-use.md "Live screen video (#3954)"): one
// stream from Persona Screen on the Mac, through the hub, decoded here. ScreenVideoLink decides when it runs and what
// shows; this file only speaks the wire and decodes.

/// The helper's samples as pictures: HEVC or H.264 in length-prefixed NAL units (AVCC), decoded one at a time by
/// VideoToolbox (the helper never reorders frames, so each decodes as it comes).
nonisolated final class ScreenVideoDecoder {
    enum Failure: Error, Equatable {
        case format(OSStatus)
        case decode(OSStatus)
        /// A frame before any `format`, or one the decoder gave no picture for.
        case noPicture
    }

    let codec: String
    private(set) var format: CMVideoFormatDescription?
    private var session: VTDecompressionSession?

    init(codec: String) { self.codec = codec }

    deinit { invalidate() }

    /// New parameter sets: the same ones keep the decoder, different ones start a new one.
    func setFormat(_ m: FormatMessage) throws {
        let f = try Self.description(codec: codec, sets: m.parameterSets)
        if let format, CMFormatDescriptionEqual(format, otherFormatDescription: f) { return }
        invalidate()
        format = f
    }

    /// One frame's picture. A session iOS invalidated (the app went to the background) is made again once.
    func decode(_ frame: VideoFrame) throws -> CVPixelBuffer {
        guard let format else { throw Failure.noPicture }
        let sample = try Self.sample(frame.sample, format: format)
        do {
            return try decode(sample, format: format)
        } catch Failure.decode(kVTInvalidSessionErr) {
            invalidate()
            return try decode(sample, format: format)
        }
    }

    private func decode(_ sample: CMSampleBuffer, format: CMVideoFormatDescription) throws -> CVPixelBuffer {
        if session == nil { session = try Self.session(format) }
        guard let session else { throw Failure.noPicture }
        let out = Output()
        // No asynchronous flag: the handler runs before this returns.
        let status = VTDecompressionSessionDecodeFrame(session, sampleBuffer: sample, flags: [], infoFlagsOut: nil) { s, _, image, _, _ in
            out.set(s, image)
        }
        let (decoded, image) = out.get()
        guard status == noErr else { throw Failure.decode(status) }
        guard decoded == noErr else { throw Failure.decode(decoded) }
        guard let image else { throw Failure.noPicture }
        return image
    }

    private func invalidate() {
        if let session { VTDecompressionSessionInvalidate(session) }
        session = nil
    }

    /// What the decode handler gave back (it is called on this thread, before the call returns).
    private final class Output: @unchecked Sendable {
        private let lock = NSLock()
        private var status: OSStatus = noErr
        private var image: CVImageBuffer?
        func set(_ s: OSStatus, _ i: CVImageBuffer?) { lock.withLock { status = s; image = i } }
        func get() -> (OSStatus, CVImageBuffer?) { lock.withLock { (status, image) } }
    }

    private static func session(_ format: CMVideoFormatDescription) throws -> VTDecompressionSession {
        // IOSurface-backed, so the display layer takes the picture without a copy.
        let attrs = [kCVPixelBufferIOSurfacePropertiesKey as String: [String: Any]()] as CFDictionary
        var s: VTDecompressionSession?
        let status = VTDecompressionSessionCreate(allocator: nil, formatDescription: format, decoderSpecification: nil,
                                                  imageBufferAttributes: attrs, outputCallback: nil, decompressionSessionOut: &s)
        guard status == noErr, let s else { throw Failure.decode(status) }
        VTSessionSetProperty(s, key: kVTDecompressionPropertyKey_RealTime, value: kCFBooleanTrue)
        return s
    }

    /// The format for `codec`'s parameter sets (HEVC: VPS, SPS, PPS; H.264: SPS, PPS), with 4-byte NAL lengths.
    static func description(codec: String, sets: [Data]) throws -> CMVideoFormatDescription {
        guard !sets.isEmpty, !sets.contains(where: \.isEmpty) else { throw Failure.format(kCMFormatDescriptionError_InvalidParameter) }
        let copies = sets.map { d -> UnsafeMutablePointer<UInt8> in
            let p = UnsafeMutablePointer<UInt8>.allocate(capacity: d.count)
            d.copyBytes(to: p, count: d.count)
            return p
        }
        defer { copies.forEach { $0.deallocate() } }
        let pointers = copies.map { UnsafePointer($0) }, sizes = sets.map(\.count)
        var out: CMFormatDescription?
        let status = pointers.withUnsafeBufferPointer { p in
            sizes.withUnsafeBufferPointer { n in
                codec == "h264"
                    ? CMVideoFormatDescriptionCreateFromH264ParameterSets(
                        allocator: nil, parameterSetCount: sets.count, parameterSetPointers: p.baseAddress!,
                        parameterSetSizes: n.baseAddress!, nalUnitHeaderLength: 4, formatDescriptionOut: &out)
                    : CMVideoFormatDescriptionCreateFromHEVCParameterSets(
                        allocator: nil, parameterSetCount: sets.count, parameterSetPointers: p.baseAddress!,
                        parameterSetSizes: n.baseAddress!, nalUnitHeaderLength: 4, extensions: nil, formatDescriptionOut: &out)
            }
        }
        guard status == noErr, let out else { throw Failure.format(status) }
        return out
    }

    /// One sample of `bytes` (AVCC) in `format`, untimed: it is decoded and shown at once.
    static func sample(_ bytes: Data, format: CMVideoFormatDescription) throws -> CMSampleBuffer {
        guard !bytes.isEmpty else { throw Failure.noPicture }
        var block: CMBlockBuffer?
        var status = CMBlockBufferCreateWithMemoryBlock(allocator: nil, memoryBlock: nil, blockLength: bytes.count,
                                                        blockAllocator: nil, customBlockSource: nil, offsetToData: 0,
                                                        dataLength: bytes.count, flags: kCMBlockBufferAssureMemoryNowFlag,
                                                        blockBufferOut: &block)
        guard status == noErr, let block else { throw Failure.decode(status) }
        status = bytes.withUnsafeBytes { CMBlockBufferReplaceDataBytes(with: $0.baseAddress!, blockBuffer: block,
                                                                       offsetIntoDestination: 0, dataLength: bytes.count) }
        guard status == noErr else { throw Failure.decode(status) }
        var sample: CMSampleBuffer?
        var size = bytes.count
        status = CMSampleBufferCreateReady(allocator: nil, dataBuffer: block, formatDescription: format, sampleCount: 1,
                                          sampleTimingEntryCount: 0, sampleTimingArray: nil, sampleSizeEntryCount: 1,
                                          sampleSizeArray: &size, sampleBufferOut: &sample)
        guard status == noErr, let sample else { throw Failure.decode(status) }
        return sample
    }
}

/// One video stream (`…/screen/video/connect?pair=…`): `start` out, then `hello`, `format` and frames in; a receiver
/// report goes back for every frame (the helper's bandwidth adaptation, RateController). It tells what happens through
/// `onEvent`, from its own task; a burst of frames that came together shows only its newest picture.
nonisolated final class ScreenVideoStream: @unchecked Sendable {
    enum Event {
        case hello(HelloMessage)
        /// A decoded picture, and how long decoding it took.
        case picture(CVPixelBuffer, decodeMs: Double)
        /// Capture stopped (`paused`): locked, login_window, permission, display_gone or busy. The stream stays open.
        case paused(String)
        case resumed
        /// The helper's side of the latency (capture, encode, network) and what it sent, about once a second.
        case timing(TimingMessage)
        /// The quality tier changed (#6396): already sent to the helper; for the stats overlay and the link's memory.
        case tier(TierPolicy.Change)
        /// What the helper's encoder runs at now (#6396, `encoding`): the overlay's resolution after an in-place change.
        case encoding(EncodingMessage)
        /// The stream is over; nothing more comes.
        case ended(Ending)
    }

    enum Ending: Equatable {
        /// It closed, or never opened (the hub or the node said no).
        case closed
        /// No frame and no heartbeat for `silence` (the helper sends one each second nothing changed).
        case silent
        /// A frame that did not decode, or a message that did not parse.
        case undecodable
    }

    /// How long without a frame or heartbeat before the stream counts as dead (not while capture is paused).
    static let silence: Duration = .seconds(3)
    /// How long the first message may take: the hub waits up to 10 s for the node to start the helper.
    static let opening: Duration = .seconds(15)

    private let transport: RFBTransport
    private let start: StartMessage
    /// The quality tiers (#6396), when this app has them; they start once the helper says it has them too.
    let tiers: ScreenVideoTiers?
    private let onEvent: @Sendable (Event) -> Void
    private let silence: Duration
    private let lock = NSLock()
    private var task: Task<Void, Never>?
    private var heardAt: ContinuousClock.Instant
    private var heardAny = false
    /// Capture is paused (`paused` came): the helper sends no heartbeat then, so silence means nothing until `resumed`.
    private var quiet = false
    private var over = false
    private var total = 0
    /// A frame came since `hello` (or since capture paused): the helper's capture is running. Until then a tier change
    /// waits (`held`, the latest only): ScreenCaptureKit drops a configuration change made before capture has really
    /// started, so a start tier sent on `hello` never took and capture stayed at 60 fps (#7842).
    private var capturing = false
    private var held: TierPolicy.Change?

    /// Every byte that came so far (the overlay's bitrate is what arrived, not what was sent).
    var receivedBytes: Int { lock.withLock { total } }

    init(transport: RFBTransport, start: StartMessage, tiers: ScreenVideoTiers? = nil, silence: Duration = ScreenVideoStream.silence,
         onEvent: @escaping @Sendable (Event) -> Void) {
        self.transport = transport
        self.start = start
        self.tiers = tiers
        self.silence = silence
        self.onEvent = onEvent
        heardAt = .now
    }

    func run() {
        task = Task.detached { [self] in
            var ending = Ending.closed
            do {
                try await transport.send(Array(ScreenVideoMessage.start(start).framed()))
                try await withThrowingTaskGroup(of: Void.self) { g in
                    g.addTask { [self] in try await read() }
                    g.addTask { [self] in try await watch() }
                    try await g.next()
                    g.cancelAll()
                }
            } catch let e as Ended {
                ending = e.why
            } catch {
                ending = .closed
            }
            transport.close()
            finish(ending)
        }
    }

    /// Ends it from this side; no `ended` event follows.
    func close() {
        lock.withLock { over = true }
        task?.cancel()
        transport.close()
    }

    /// `update` to the helper (a new cap, frame rate, or pause from the phone's side).
    func send(_ update: UpdateMessage) {
        let bytes = Array(ScreenVideoMessage.update(update).framed())
        Task.detached { [transport] in try? await transport.send(bytes) }
    }

    /// A tier the policy chose: to the helper, then to the link (#6396); held until capture runs (`capturing`).
    private func apply(_ change: TierPolicy.Change?) {
        guard let change else { return }
        let now: Bool = lock.withLock {
            if !capturing { held = change }
            return capturing
        }
        if now { put(change) }
    }

    private func put(_ change: TierPolicy.Change) {
        send(ScreenVideoTiers.update(change))
        onEvent(.tier(change))
    }

    /// Capture stopped (a new `hello`, or `paused`): tier changes wait for its next frame. A new `hello` starts a new
    /// policy, whose start replaces what was held; a pause keeps it.
    private func captureStopped(newHello: Bool) {
        lock.withLock {
            capturing = false
            if newHello { held = nil }
        }
    }

    /// A frame came: capture runs, and a held tier change goes out.
    private func captureRuns() {
        let release: TierPolicy.Change? = lock.withLock {
            guard !capturing else { return nil }
            capturing = true
            defer { held = nil }
            return held
        }
        if let release { put(release) }
    }

    /// ⋯ › Quality, the phone's budget, or the route changed (#6396, #6398): the policy looks again.
    func tierBias(_ b: TierLadder.Bias) { apply(tiers?.setBias(b, at: Self.nowMicros)) }
    func tierBudget(_ b: ScreenVideoBudget) { apply(tiers?.setBudget(b, at: Self.nowMicros)) }
    func tierPath(_ change: PathChange) { apply(tiers?.path(change, at: Self.nowMicros)) }

    /// A decoded picture reached the screen, or a newer one replaced it before it did (the link's mailbox).
    func shown(replaced: Bool) { tiers?.shown(replaced: replaced, at: Self.nowMicros) }

    private struct Ended: Error { let why: Ending }

    private func finish(_ why: Ending) {
        let tell = lock.withLock { () -> Bool in
            defer { over = true }
            return !over
        }
        if tell { onEvent(.ended(why)) }
    }

    private func heard(paused: Bool = false) { lock.withLock { heardAt = .now; heardAny = true; quiet = paused } }

    private func watch() async throws {
        while !Task.isCancelled {
            try await Task.sleep(for: .milliseconds(250))
            // Typed, and named apart from the `quiet` property it reads: inferred, the tuple crashed the type checker.
            let (at, any, paused): (ContinuousClock.Instant, Bool, Bool) = lock.withLock { (heardAt, heardAny, quiet) }
            if !paused, ContinuousClock.now - at > (any ? silence : Self.opening) { throw Ended(why: .silent) }
            if !paused { apply(tiers?.tick(at: Self.nowMicros)) } // the tier's 250 ms decision (#6396)
        }
    }

    private func read() async throws {
        var parser = ScreenVideoParser()
        var decoder: ScreenVideoDecoder?
        var bytes = 0
        /// When the message still incomplete after the last read began arriving (a large frame over several reads).
        var partialSince: UInt64?
        while !Task.isCancelled {
            let chunk: [UInt8]
            do { chunk = try await transport.receive() } catch { throw Ended(why: .closed) }
            bytes += chunk.count
            lock.withLock { total += chunk.count }
            let arrived = Self.nowMicros
            let waiting = parser.pendingBytes > 0 ? partialSince : nil
            let messages: [ScreenVideoWire.RawMessage]
            do { messages = try parser.push(Data(chunk)) } catch { throw Ended(why: .undecodable) }
            partialSince = parser.pendingBytes > 0 ? (messages.isEmpty ? (partialSince ?? arrived) : arrived) : nil
            var firstOfRead = true
            var newest: (CVPixelBuffer, Double)?
            for raw in messages {
                let m: ScreenVideoMessage
                do { m = try ScreenVideoMessage.decode(type: raw.type, body: raw.body) } catch { throw Ended(why: .undecodable) }
                switch m {
                case let .hello(h):
                    heard()
                    decoder = ScreenVideoDecoder(codec: h.codec)
                    newest = nil
                    onEvent(.hello(h))
                    captureStopped(newHello: true)
                    apply(tiers?.hello(h, now: Double(Self.nowMicros) / 1_000_000))
                case let .format(f):
                    do { try decoder?.setFormat(f) } catch { throw Ended(why: .undecodable) }
                case let .frame(f):
                    heard()
                    captureRuns()
                    report(f, bytes: bytes)
                    // Only the read's first message can have begun in an earlier read.
                    tiers?.frame(f, arrival: arrived, firstByte: firstOfRead ? waiting : nil, bytes: raw.body.count + 5)
                    bytes = 0
                    guard let decoder else { throw Ended(why: .undecodable) }
                    let began = ContinuousClock.now
                    do {
                        let picture = try decoder.decode(f)
                        let ms = Self.ms(ContinuousClock.now - began)
                        newest = (picture, ms)
                        tiers?.decoded(ms: ms, at: Self.nowMicros)
                    } catch {
                        throw Ended(why: .undecodable)
                    }
                case .heartbeat:
                    heard()
                case let .paused(p):
                    heard(paused: true)
                    captureStopped(newHello: false)
                    newest = nil
                    onEvent(.paused(p.reason))
                case .resumed:
                    heard()
                    onEvent(.resumed)
                case let .timing(t):
                    tiers?.helper(t)
                    onEvent(.timing(t))
                case let .encoding(e):
                    onEvent(.encoding(e))
                default:
                    break // phone → helper types, or ones newer than this app: skipped
                }
                firstOfRead = false
            }
            if let newest { onEvent(.picture(newest.0, decodeMs: newest.1)) }
        }
    }

    /// When the frame came, on the phone's own clock: the helper measures the queue above the lowest delay it has seen,
    /// so the two clocks needn't agree.
    private func report(_ f: VideoFrame, bytes: Int) {
        let r = ReceiverReport(lastFrame: f.number, captureMicros: f.captureMicros, arrivalMicros: Self.nowMicros,
                               bytesSinceLast: UInt32(clamping: bytes), flags: [])
        let out = Array(ScreenVideoMessage.report(r).framed())
        Task.detached { [transport] in try? await transport.send(out) }
    }

    static var nowMicros: UInt64 { clock_gettime_nsec_np(CLOCK_UPTIME_RAW) / 1000 }

    private static func ms(_ d: Duration) -> Double {
        Double(d.components.seconds) * 1000 + Double(d.components.attoseconds) / 1e15
    }
}
#endif
