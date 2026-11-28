import Foundation

/// Optional x264 encoding (docs/extensions/computer-use.md "x264 on a direct LAN (#8859)"): when the user installed
/// ffmpeg with libx264 themselves and turned x264 on for this Mac, the helper may encode with it instead of
/// VideoToolbox, on a direct LAN route only. ffmpeg runs as a separate program (`X264Process`): Persona ships, links and loads nothing
/// of x264's or ffmpeg's. On an M1 Ultra a 1080p frame comes back in about 1.5–2 ms (VideoToolbox: 7–8 ms plus about
/// 1 ms to hand it back) for 0.3–0.5 of a core and about 1.3–1.5× the bits.
///
/// These are the decisions, apart from the process, so they test anywhere: which encoder a stream uses, ffmpeg's
/// arguments, and how an encoded frame maps back to the picture it came from.
public enum X264Plan {
    /// Which encoder a stream uses (`hello.encoder`).
    public enum Encoder: String, Sendable {
        case hardware, x264
    }

    /// Why x264, turned on for this Mac, isn't the encoder now (`hello.fallback`).
    public enum Off: String, Sendable {
        /// This helper can't start ffmpeg as its own program (`X264Process.canSpawn`), or the phone takes no H.264.
        case unavailable
        /// The stream isn't on a direct LAN route (or the phone didn't say which route it is on).
        case route
        /// Frames came back too slowly for this stream (`X264Watch`): the CPU is busy.
        case slow
        /// ffmpeg ended, or put out something that isn't the next frame.
        case failed
        /// The phone's reports say the link is queueing: VideoToolbox can lower its bitrate as it runs, ffmpeg can't.
        case congested
    }

    /// The constant quality x264 encodes at, under `cap`'s ceiling: on a LAN the bits are there, so the picture is
    /// kept rather than a bitrate.
    public static let crf = 20

    /// The encoder for a stream. `enabled`: the node passed `--x264` (on for this Mac, ffmpeg found). `spawnable`: the
    /// helper can start it as its own responsible process. `path`: the route the phone says the stream is on.
    /// `failed`: the reason x264 already stopped during this stream, which holds for the rest of it.
    public static func encoder(enabled: Bool, spawnable: Bool, codecs: [String], path: UpdateMessage.Path?,
                               failed: Off?) -> (encoder: Encoder, off: Off?) {
        guard enabled else { return (.hardware, nil) }
        if let failed { return (.hardware, failed) }
        guard spawnable, codecs.contains(where: { $0.lowercased() == "h264" }) else { return (.hardware, .unavailable) }
        guard path?.kind == "lan" else { return (.hardware, .route) }
        return (.x264, nil)
    }

    /// Encoding threads: the performance cores, at most 8 (sliced threads: one slice each, all of one frame at once).
    public static func threads(performanceCores: Int) -> Int { min(max(performanceCores, 1), 8) }

    /// ffmpeg's arguments for `width` × `height` NV12 frames on stdin at `fps`, FLV out on stdout, with `capKbps` as
    /// the bitrate's ceiling. Each choice is measured (#8859):
    /// - `ultrafast` + `zerolatency`, sliced threads: no B-frames and no frame held back, so each frame comes out as
    ///   soon as it is in; the phone decodes synchronously and must get a picture for every sample.
    /// - `keyint=infinite`: a keyframe only when asked for, which a fresh process gives (`X264Session`).
    /// - FLV: each tag carries its length, so a frame's end is known when it arrives (raw H.264 says so only with the
    ///   next frame), and its payload is already the wire's length-prefixed NAL units.
    /// - `-flush_packets 1`: each frame leaves ffmpeg at once (ffmpeg 8 and 9 do so on a pipe anyway).
    /// - `-fps_mode passthrough`: every frame in comes out, in order, so they map back one to one.
    /// - The colour description (BT.709, video range, which ScreenCaptureKit's 420v frames are) goes through
    ///   `-x264-params`: ffmpeg 9.0.2's `-colorspace` output option crashes with these settings.
    public static func arguments(width: Int, height: Int, fps: Int, threads: Int, capKbps: Int) -> [String] {
        let cap = max(capKbps, CapturePlan.minKbps)
        return [
            "-hide_banner", "-loglevel", "error", "-nostdin",
            "-f", "rawvideo", "-pix_fmt", "nv12", "-video_size", "\(width)x\(height)", "-framerate", "\(fps)",
            "-i", "pipe:0",
            "-fps_mode", "passthrough",
            "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency", "-threads", "\(threads)",
            "-x264-params",
            "sliced-threads=1:keyint=infinite:colorprim=bt709:transfer=bt709:colormatrix=bt709:range=tv",
            "-crf", "\(crf)", "-maxrate", "\(cap)k", "-bufsize", "\(max(cap / 20, 100))k",
            "-flush_packets", "1", "-f", "flv", "pipe:1",
        ]
    }

    /// The FLV timestamp, in ms, of the `index`th frame (from 0) given to one ffmpeg at `fps`: frame i is at i/fps
    /// seconds, rounded to the millisecond as ffmpeg's FLV muxer does. Frames written sparsely still count one by one.
    public static func timestampMs(index: Int, fps: Int) -> UInt32 {
        let f = max(fps, 1)
        return UInt32(truncatingIfNeeded: (index * 1000 * 2 + f) / (2 * f))
    }
}

/// Whether x264 keeps up with a stream (#8859): it falls back to VideoToolbox when any frame takes over 50 ms of its own,
/// or when more than 10% of the frames in the last 2 s took over 8 ms of their own (VideoToolbox's whole time). A
/// frame's own time starts when ffmpeg could start on it (`ownMicros`), so waiting behind the frame before it isn't
/// counted twice. A process's first frame counts for neither: it is a keyframe, and ffmpeg sets up its encoder on it
/// (5–7 ms from a waiting process, 30–40 ms from a cold Homebrew ffmpeg, which loads many libraries); only
/// `overdueMicros` bounds it. Its next `warmupFrames` (about half a second at 60 fps) count only against the 50 ms
/// bound, and the share is judged on at least `minFrames` (about a second): on the first real session 2 of the first 16
/// frames took over 8 ms of their own (x264's threads starting, the capture's fresh buffers read for the first time)
/// and stopped x264 a third of a second in. Times are µs on the helper's monotonic clock; nothing here reads a clock.
public struct X264Watch {
    public static let windowMicros: UInt64 = 2_000_000
    public static let slowMicros: Int64 = 8_000
    public static let slowShare = 0.10
    /// Fewer frames than this in the window say nothing about a share.
    public static let minFrames = 60
    /// A process's frames after its first that count only against `stallMicros`: its warm-up.
    public static let warmupFrames = 30
    public static let stallMicros: Int64 = 50_000
    /// A frame given to ffmpeg and not back after this long: it has stalled.
    public static let overdueMicros: UInt64 = 200_000

    private var window: [(at: UInt64, micros: Int64)] = []
    /// Why `encoded` last said too slow, for the helper's log.
    public private(set) var why = ""

    public init() {}

    /// A frame's own encode time: from when it was given, or when the frame before it came out if that was later (it
    /// waited behind it), to `now`.
    public static func ownMicros(given: UInt64, lastOut: UInt64, now: UInt64) -> Int64 {
        let start = max(given, lastOut)
        guard now > start else { return 0 }
        let elapsed = now - start
        return elapsed > UInt64(Int64.max) ? .max : Int64(elapsed)
    }

    /// The `index`th frame of its process (from 0) came back after `micros` of its own. True: too slow, fall back.
    public mutating func encoded(micros: Int64, at now: UInt64, index: Int) -> Bool {
        window.removeAll { now > $0.at && now - $0.at > Self.windowMicros }
        guard index > 0 else { return false }
        if micros > Self.stallMicros {
            why = String(format: "%.1f ms on its own", Double(micros) / 1000)
            return true
        }
        guard index > Self.warmupFrames else { return false }
        window.append((now, micros))
        guard window.count >= Self.minFrames else { return false }
        let slow = window.filter { $0.micros > Self.slowMicros }.count
        guard Double(slow) / Double(window.count) > Self.slowShare else { return false }
        let sorted = window.map(\.micros).sorted()
        why = String(format: "%d of the last %d frames over 8 ms on their own (median %.1f ms, max %.1f ms)", slow,
                     window.count, Double(sorted[sorted.count / 2]) / 1000, Double(sorted[sorted.count - 1]) / 1000)
        return true
    }

    /// The oldest frame still with ffmpeg was given to it at `oldest`: true when that is overdue.
    public static func overdue(oldest: UInt64?, now: UInt64) -> Bool {
        guard let oldest, now > oldest else { return false }
        return now - oldest > overdueMicros
    }
}

/// Reads ffmpeg's FLV output as it arrives (#8859): the AVC sequence header's parameter sets and each frame's
/// length-prefixed NAL units, which are the wire's `format` and `frame` as they are. Supplemental enhancement
/// information (x264 writes its version and settings into the first frame) and access unit delimiters are dropped:
/// the phone has no use for them. Audio and script tags are skipped, as is the end-of-sequence tag at close.
public struct FLVReader {
    public enum Item: Equatable, Sendable {
        case parameterSets([Data])
        case frame(timestampMs: UInt32, keyframe: Bool, sample: Data)
    }

    public enum Failure: Error, Equatable {
        case notFLV, notAVC, badConfig, badSample, tooLarge
    }

    /// No tag is larger than this (FLV allows up to 16 MB): a 4K keyframe at CRF 20 is well under it.
    public static let maxTag = 8 << 20

    private var buffer = Data()
    private var headerDone = false

    public init() {}

    /// Takes the next bytes of the stream; returns what they completed.
    public mutating func push(_ bytes: Data) throws -> [Item] {
        buffer.append(bytes)
        var items: [Item] = []
        var offset = buffer.startIndex
        func available() -> Int { buffer.endIndex - offset }
        if !headerDone {
            guard available() >= 13 else { return [] }
            guard buffer[offset] == 0x46, buffer[offset + 1] == 0x4C, buffer[offset + 2] == 0x56 else { throw Failure.notFLV }
            let dataOffset = Int(Self.u32(buffer, offset + 5))
            guard dataOffset >= 9, dataOffset <= 64 else { throw Failure.notFLV }
            guard available() >= dataOffset + 4 else { return [] }
            offset += dataOffset + 4 // the header, then PreviousTagSize0
            headerDone = true
        }
        while available() >= 11 {
            let type = buffer[offset]
            let size = Int(Self.u24(buffer, offset + 1))
            guard size <= Self.maxTag else { throw Failure.tooLarge }
            guard available() >= 11 + size + 4 else { break }
            let timestamp = Self.u24(buffer, offset + 4) | UInt32(buffer[offset + 7]) << 24
            let body = buffer[(offset + 11) ..< (offset + 11 + size)]
            offset += 11 + size + 4
            guard type == 9 else { continue } // audio, script data
            if let item = try Self.video(body, timestamp: timestamp) { items.append(item) }
        }
        buffer.removeSubrange(buffer.startIndex ..< offset)
        return items
    }

    private static func video(_ body: Data, timestamp: UInt32) throws -> Item? {
        guard body.count >= 5 else { throw Failure.badSample }
        let start = body.startIndex
        guard body[start] & 0x0F == 7 else { throw Failure.notAVC }
        let keyframe = body[start] >> 4 == 1
        let payload = body[(start + 5)...]
        switch body[start + 1] {
        case 0: return .parameterSets(try parameterSets(payload))
        case 1: return .frame(timestampMs: timestamp, keyframe: keyframe, sample: try nalUnits(payload))
        default: return nil // 2: end of sequence
        }
    }

    /// The SPS and PPS of an AVCDecoderConfigurationRecord with 4-byte NAL lengths (all the wire carries).
    private static func parameterSets(_ record: Data) throws -> [Data] {
        let r = record.startIndex
        guard record.count >= 7, record[r] == 1, record[r + 4] & 0x03 == 3 else { throw Failure.badConfig }
        var sets: [Data] = []
        var p = r + 5
        for countMask in [UInt8(0x1F), 0xFF] {
            guard p < record.endIndex else { throw Failure.badConfig }
            let count = Int(record[p] & countMask)
            p += 1
            for _ in 0 ..< count {
                guard p + 2 <= record.endIndex else { throw Failure.badConfig }
                let length = Int(record[p]) << 8 | Int(record[p + 1])
                p += 2
                guard length > 0, p + length <= record.endIndex else { throw Failure.badConfig }
                sets.append(Data(record[p ..< (p + length)]))
                p += length
            }
        }
        guard sets.count >= 2 else { throw Failure.badConfig }
        return sets
    }

    /// The frame's length-prefixed NAL units without SEI (6) and access unit delimiters (9), checked to fill the tag.
    private static func nalUnits(_ payload: Data) throws -> Data {
        var out = Data(capacity: payload.count)
        var p = payload.startIndex
        while p < payload.endIndex {
            guard p + 4 <= payload.endIndex else { throw Failure.badSample }
            let length = Int(u32(payload, p))
            guard length > 0, p + 4 + length <= payload.endIndex else { throw Failure.badSample }
            let type = payload[p + 4] & 0x1F
            if type != 6, type != 9 { out.append(payload[p ..< (p + 4 + length)]) }
            p += 4 + length
        }
        guard !out.isEmpty else { throw Failure.badSample }
        return out
    }

    private static func u24(_ d: Data, _ i: Int) -> UInt32 { UInt32(d[i]) << 16 | UInt32(d[i + 1]) << 8 | UInt32(d[i + 2]) }
    private static func u32(_ d: Data, _ i: Int) -> UInt32 { u24(d, i) << 8 | UInt32(d[i + 3]) }
}
