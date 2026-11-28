import Foundation
import XCTest
@testable import PersonaScreenCore

/// Optional x264 encoding (#8859): which encoder a stream gets, ffmpeg's arguments, the frame mapping, the slowness
/// watch, the FLV reader, and the wire fields it adds.
final class X264PlanTests: XCTestCase {
    private let lan = UpdateMessage.Path(kind: "lan", via: "wifi")

    func testEncoderChoice() {
        let codecs = ["hevc", "h264"]
        func choose(enabled: Bool = true, spawnable: Bool = true, codecs: [String] = codecs,
                    path: UpdateMessage.Path? = UpdateMessage.Path(kind: "lan", via: "wifi"),
                    failed: X264Plan.Off? = nil) -> (X264Plan.Encoder, X264Plan.Off?) {
            let c = X264Plan.encoder(enabled: enabled, spawnable: spawnable, codecs: codecs, path: path, failed: failed)
            return (c.encoder, c.off)
        }
        XCTAssertTrue(choose() == (.x264, nil))
        // Not turned on for this Mac (or no ffmpeg): VideoToolbox, and nothing to say about x264.
        XCTAssertTrue(choose(enabled: false) == (.hardware, nil))
        XCTAssertTrue(choose(enabled: false, failed: .slow) == (.hardware, nil))
        // A reason it stopped holds for the rest of the stream, ahead of the route.
        XCTAssertTrue(choose(failed: .slow) == (.hardware, .slow))
        XCTAssertTrue(choose(path: nil, failed: .congested) == (.hardware, .congested))
        XCTAssertTrue(choose(spawnable: false) == (.hardware, .unavailable))
        XCTAssertTrue(choose(codecs: ["hevc"]) == (.hardware, .unavailable))
        XCTAssertTrue(choose(codecs: ["H264"]) == (.x264, nil))
        // Only a direct LAN route; a phone that names none (before #8859) gets VideoToolbox.
        XCTAssertTrue(choose(path: nil) == (.hardware, .route))
        for kind in ["tailscale", "ipv6", "relay", "something-new"] {
            XCTAssertTrue(choose(path: UpdateMessage.Path(kind: kind, via: "wifi")) == (.hardware, .route), kind)
        }
        XCTAssertTrue(choose(path: UpdateMessage.Path(kind: "lan", via: "wired")) == (.x264, nil))
    }

    func testThreads() {
        XCTAssertEqual(X264Plan.threads(performanceCores: 16), 8)
        XCTAssertEqual(X264Plan.threads(performanceCores: 4), 4)
        XCTAssertEqual(X264Plan.threads(performanceCores: 0), 1)
    }

    func testArguments() {
        let args = X264Plan.arguments(width: 1920, height: 1080, fps: 60, threads: 8, capKbps: 40_000)
        func value(after flag: String) -> String? { args.firstIndex(of: flag).map { args[$0 + 1] } }
        XCTAssertEqual(value(after: "-video_size"), "1920x1080")
        XCTAssertEqual(value(after: "-pix_fmt"), "nv12")
        XCTAssertEqual(value(after: "-framerate"), "60")
        XCTAssertEqual(value(after: "-i"), "pipe:0")
        XCTAssertEqual(value(after: "-fps_mode"), "passthrough")
        XCTAssertEqual(value(after: "-c:v"), "libx264")
        XCTAssertEqual(value(after: "-tune"), "zerolatency")
        XCTAssertEqual(value(after: "-threads"), "8")
        XCTAssertEqual(value(after: "-crf"), "\(X264Plan.crf)")
        XCTAssertEqual(value(after: "-maxrate"), "40000k")
        XCTAssertEqual(value(after: "-bufsize"), "2000k")
        XCTAssertEqual(value(after: "-flush_packets"), "1")
        XCTAssertEqual(value(after: "-f"), "rawvideo") // the first -f is the input's
        XCTAssertEqual(args.suffix(3), ["-f", "flv", "pipe:1"])
        XCTAssertEqual(value(after: "-x264-params"),
                       "sliced-threads=1:keyint=infinite:colorprim=bt709:transfer=bt709:colormatrix=bt709:range=tv")
        // ffmpeg 9.0.2 crashes on the -colorspace output option with these settings: the colour goes through x264.
        XCTAssertFalse(args.contains("-colorspace"))
        // A cap below the helper's floor is the floor; the buffer never goes below 100 kbit.
        let low = X264Plan.arguments(width: 640, height: 360, fps: 30, threads: 2, capKbps: 0)
        XCTAssertEqual(low[low.firstIndex(of: "-maxrate")! + 1], "\(CapturePlan.minKbps)k")
        XCTAssertEqual(low[low.firstIndex(of: "-bufsize")! + 1], "100k")
    }

    /// As ffmpeg's FLV muxer writes them (seen on 8.1.3 and 9.0.2): i/fps seconds to the nearest millisecond.
    func testTimestamps() {
        XCTAssertEqual((0 ..< 7).map { X264Plan.timestampMs(index: $0, fps: 60) }, [0, 17, 33, 50, 67, 83, 100])
        XCTAssertEqual((0 ..< 4).map { X264Plan.timestampMs(index: $0, fps: 30) }, [0, 33, 67, 100])
        XCTAssertEqual(X264Plan.timestampMs(index: 3600, fps: 60), 60_000)
    }

    func testWatch() {
        var w = X264Watch()
        var now: UInt64 = 1_000_000
        // A process's first frame (a keyframe, ffmpeg setting up) counts for nothing; any other over 50 ms ends it, its
        // warm-up included.
        XCTAssertFalse(w.encoded(micros: 60_000, at: now, index: 0))
        XCTAssertTrue(X264Watch().encodedOnce(micros: 50_001, index: 5))
        XCTAssertFalse(X264Watch().encodedOnce(micros: 50_000, index: 5))
        // The warm-up's frames count only against 50 ms: all of them at 9 ms change nothing.
        for i in 1 ... X264Watch.warmupFrames {
            now += 16_000
            XCTAssertFalse(w.encoded(micros: 9_000, at: now, index: i), "\(i)")
        }
        // Then the share, once there are 60: 6 slow in 60 is 10%, fine; one more is over.
        var index = X264Watch.warmupFrames
        for k in 0 ..< X264Watch.minFrames {
            now += 16_000
            index += 1
            XCTAssertFalse(w.encoded(micros: k < 6 ? 9_000 : 2_000, at: now, index: index), "\(k)")
        }
        now += 16_000
        index += 1
        XCTAssertTrue(w.encoded(micros: 9_000, at: now, index: index))
        XCTAssertTrue(w.why.contains("7 of the last 61 frames over 8 ms"), w.why)

        // Old frames leave the 2 s window, and too few frames say nothing about a share.
        var sparse = X264Watch()
        var t: UInt64 = 0
        for i in 40 ..< 45 {
            t += 1_000_000
            XCTAssertFalse(sparse.encoded(micros: 9_000, at: t, index: i))
        }
        XCTAssertFalse(X264Watch.overdue(oldest: nil, now: 5_000_000))
        XCTAssertFalse(X264Watch.overdue(oldest: 1_000_000, now: 1_200_000))
        XCTAssertTrue(X264Watch.overdue(oldest: 1_000_000, now: 1_200_001))
        // Backward clock must not wrap into a false overdue stall or evict the slow-frame window.
        XCTAssertFalse(X264Watch.overdue(oldest: 1_000_000, now: 900_000))
        var backward = X264Watch()
        var bt: UInt64 = 10_000_000
        for k in 0 ..< (X264Watch.minFrames - 1) {
            bt += 16_000
            XCTAssertFalse(backward.encoded(micros: 9_000, at: bt, index: X264Watch.warmupFrames + 1 + k))
        }
        // A slightly earlier timestamp must keep the existing 59 slow samples so sample #60 still triggers fallback.
        XCTAssertTrue(backward.encoded(micros: 9_000, at: 9_000_000, index: X264Watch.warmupFrames + X264Watch.minFrames))
    }

    /// A frame's own time starts when ffmpeg could start on it: when it was given, or when the one before it came out.
    func testOwnTime() {
        XCTAssertEqual(X264Watch.ownMicros(given: 1_000, lastOut: 0, now: 3_000), 2_000)
        XCTAssertEqual(X264Watch.ownMicros(given: 1_000, lastOut: 30_000, now: 32_000), 2_000) // waited behind the one before
        XCTAssertEqual(X264Watch.ownMicros(given: 5_000, lastOut: 4_000, now: 6_500), 1_500)
        XCTAssertEqual(X264Watch.ownMicros(given: 5_000, lastOut: 4_000, now: 3_000), 0) // backward clock clamps to 0
        XCTAssertEqual(X264Watch.ownMicros(given: 0, lastOut: 0, now: UInt64.max), Int64.max) // saturates instead of wrapping negative
    }

    /// Frames 60 fps apart, each taking `cost(i)` of its own; what the watch says, frame by frame, as X264Session feeds it.
    private func watch(_ count: Int, cost: (Int) -> UInt64) -> (stopped: Bool, why: String) {
        var w = X264Watch()
        var last: UInt64 = 0
        for i in 0 ..< count {
            let given = 1_000_000 + UInt64(i) * 16_667
            let out = max(given, last) + cost(i)
            if w.encoded(micros: X264Watch.ownMicros(given: given, lastOut: last, now: out), at: out, index: i) {
                return (true, w.why)
            }
            last = out
        }
        return (false, w.why)
    }

    /// #8859: a cold Homebrew ffmpeg's first frame takes 36 ms, the next waits behind it (21 ms in all) and the one after
    /// a little (5 ms), then each takes 1.5 ms: on their own times that is no slow frame at all.
    func testAColdStartIsNotSlow() {
        XCTAssertFalse(watch(180) { $0 == 0 ? 36_000 : ($0 == 2 ? 4_500 : 1_500) }.stopped)
    }

    /// #8859, the first real session after the cold-start fix: 2 of the first 16 frames took over 8 ms of their own
    /// (x264's threads starting, the capture's fresh buffers) and x264 stopped a third of a second in. A slow warm-up then
    /// a steady stream keeps x264.
    func testASlowWarmUpThenSteadyKeepsX264() {
        let result = watch(240) { i in
            switch i {
            case 0: 36_000
            case 3, 9: 11_000
            case 1 ... 20: 4_000
            default: 1_600
            }
        }
        XCTAssertFalse(result.stopped, result.why)
    }

    /// A Mac that really can't keep up still stops x264 once the warm-up is over and a second of frames says so: every
    /// frame 9 ms on its own.
    func testASteadilySlowMacStopsX264() {
        let result = watch(240) { $0 == 0 ? 36_000 : 9_000 }
        XCTAssertTrue(result.stopped)
        XCTAssertTrue(result.why.contains("over 8 ms") && result.why.contains("median 9.0 ms"), result.why)
        // And not before the warm-up and a second of frames: no stop from the first 90.
        XCTAssertFalse(watch(90) { $0 == 0 ? 36_000 : 9_000 }.stopped)
    }

    func testArgumentsFlag() throws {
        let args = try HelperArguments.parse(["--socket", "/tmp/s", "--token", "t", "--x264", "/opt/homebrew/bin/ffmpeg"])
        XCTAssertEqual(args.x264, "/opt/homebrew/bin/ffmpeg")
        XCTAssertNil(try HelperArguments.parse(["--socket", "/tmp/s", "--token", "t"]).x264)
        // Only an absolute path: the helper never looks a program up.
        XCTAssertThrowsError(try HelperArguments.parse(["--socket", "/tmp/s", "--token", "t", "--x264", "ffmpeg"]))
        XCTAssertThrowsError(try HelperArguments.parse(["--socket", "/tmp/s", "--token", "t", "--x264"]))
    }

    func testWireFields() throws {
        let start = StartMessage(display: nil, maxSize: 1920, fps: 60, maxKbps: 40_000, codecs: ["hevc", "h264"], path: lan)
        let decoded = try ScreenVideoMessage.decode(type: ScreenVideoWire.Kind.start.rawValue, body: body(.start(start)))
        XCTAssertEqual(decoded, .start(start))
        XCTAssertTrue(String(decoding: body(.start(start)), as: UTF8.self).contains(#""path":{"#))
        let hello = HelloMessage(version: 1, codec: "h264", display: 1, width: 1920, height: 1080, encoder: "hardware",
                                 fallback: "route")
        XCTAssertEqual(try ScreenVideoMessage.decode(type: ScreenVideoWire.Kind.hello.rawValue, body: body(.hello(hello))),
                       .hello(hello))
        // Absent, they are left out: a message without them is byte for byte what it was before #8859.
        let plain = String(decoding: body(.start(StartMessage(display: nil, maxSize: 0, fps: 60, maxKbps: 0, codecs: []))),
                           as: UTF8.self)
        XCTAssertFalse(plain.contains("path"))
        let oldHello = String(decoding: body(.hello(HelloMessage(version: 1, codec: "h264", display: 0, width: 2, height: 2))),
                              as: UTF8.self)
        XCTAssertFalse(oldHello.contains("encoder"))
        XCTAssertFalse(oldHello.contains("fallback"))
    }

    private func body(_ m: ScreenVideoMessage) -> Data { m.framed().dropFirst(5) }
}

private extension X264Watch {
    func encodedOnce(micros: Int64, index: Int) -> Bool {
        var w = self
        return w.encoded(micros: micros, at: 1, index: index)
    }
}

final class FLVReaderTests: XCTestCase {
    static let sps = Data([0x67, 0x42, 0xC0, 0x2A, 0x8C, 0x8D])
    static let pps = Data([0x68, 0xCE, 0x3C, 0x80])

    static func header() -> Data { Data([0x46, 0x4C, 0x56, 0x01, 0x01, 0, 0, 0, 9, 0, 0, 0, 0]) }

    static func tag(_ type: UInt8, ms: UInt32, _ body: Data) -> Data {
        var d = Data([type, UInt8(body.count >> 16 & 0xFF), UInt8(body.count >> 8 & 0xFF), UInt8(body.count & 0xFF),
                      UInt8(ms >> 16 & 0xFF), UInt8(ms >> 8 & 0xFF), UInt8(ms & 0xFF), UInt8(ms >> 24), 0, 0, 0])
        d.append(body)
        let size = UInt32(11 + body.count)
        d.append(contentsOf: [UInt8(size >> 24), UInt8(size >> 16 & 0xFF), UInt8(size >> 8 & 0xFF), UInt8(size & 0xFF)])
        return d
    }

    static func config(lengthSize: UInt8 = 4) -> Data {
        var r = Data([0x17, 0, 0, 0, 0, 1, 0x42, 0xC0, 0x2A, 0xFC | (lengthSize - 1), 0xE1])
        r.append(contentsOf: [0, UInt8(sps.count)]); r.append(sps)
        r.append(1); r.append(contentsOf: [0, UInt8(pps.count)]); r.append(pps)
        return r
    }

    static func nal(_ bytes: [UInt8]) -> Data {
        var d = Data([0, 0, 0, UInt8(bytes.count)])
        d.append(contentsOf: bytes)
        return d
    }

    static func frame(key: Bool, _ nals: [Data]) -> Data {
        var d = Data([key ? 0x17 : 0x27, 1, 0, 0, 0])
        nals.forEach { d.append($0) }
        return d
    }

    /// What ffmpeg writes: the header, metadata, the sequence header, a keyframe with x264's SEI and two slices, a
    /// frame, the end of sequence. Read whole and a byte at a time, it gives the same items.
    func testStream() throws {
        let sei = Self.nal([0x06, 0x05, 0x10, 0xAA])
        let slice1 = Self.nal([0x65, 0x88, 0x84]), slice2 = Self.nal([0x65, 0x00, 0x11])
        let p = Self.nal([0x41, 0x9A, 0x02])
        var stream = Self.header()
        stream.append(Self.tag(18, ms: 0, Data([0x02, 0x00, 0x0A])))
        stream.append(Self.tag(9, ms: 0, Self.config()))
        stream.append(Self.tag(9, ms: 0, Self.frame(key: true, [sei, slice1, slice2])))
        stream.append(Self.tag(9, ms: 17, Self.frame(key: false, [Self.nal([0x09, 0xF0]), p])))
        stream.append(Self.tag(9, ms: 17, Data([0x17, 2, 0, 0, 0])))
        let expected: [FLVReader.Item] = [
            .parameterSets([Self.sps, Self.pps]),
            .frame(timestampMs: 0, keyframe: true, sample: slice1 + slice2),
            .frame(timestampMs: 17, keyframe: false, sample: p),
        ]
        var whole = FLVReader()
        XCTAssertEqual(try whole.push(stream), expected)
        var bytewise = FLVReader()
        var items: [FLVReader.Item] = []
        for b in stream { items += try bytewise.push(Data([b])) }
        XCTAssertEqual(items, expected)
    }

    func testTimestampExtension() throws {
        var r = FLVReader()
        let ms: UInt32 = 0x0123_4567
        let items = try r.push(Self.header() + Self.tag(9, ms: ms, Self.frame(key: false, [Self.nal([0x41, 1])])))
        XCTAssertEqual(items, [.frame(timestampMs: ms, keyframe: false, sample: Self.nal([0x41, 1]))])
    }

    func testFailures() {
        func fails(_ stream: Data, _ failure: FLVReader.Failure, line: UInt = #line) {
            var r = FLVReader()
            XCTAssertThrowsError(try r.push(stream), line: line) { XCTAssertEqual($0 as? FLVReader.Failure, failure, line: line) }
        }
        fails(Data("RIFF0000000000".utf8), .notFLV)
        fails(Self.header() + Self.tag(9, ms: 0, Data([0x12, 1, 0, 0, 0, 1])), .notAVC) // codec 2, not AVC
        fails(Self.header() + Self.tag(9, ms: 0, Self.config(lengthSize: 2)), .badConfig)
        fails(Self.header() + Self.tag(9, ms: 0, Self.frame(key: true, [Data([0, 0, 0, 9, 0x65])])), .badSample)
        fails(Self.header() + Self.tag(9, ms: 0, Self.frame(key: true, [Self.nal([0x06, 1])])), .badSample) // only SEI
        fails(Self.header() + Self.tag(9, ms: 0, Data([0x17, 1])), .badSample)
        var huge = Self.header()
        huge.append(contentsOf: [9, 0x80, 0x00, 0x01]) // 8 MB and a byte
        huge.append(Data(count: 7))
        fails(huge, .tooLarge)
    }
}

#if os(macOS)
import Darwin
import VideoToolbox

/// Lifecycle safety of `X264Process` and `X264Session` without requiring ffmpeg: releasing without an explicit `stop()`
/// must neither resurrect `self` during `deinit` nor leave child processes running.
final class X264ProcessLifecycleTests: XCTestCase {
    func testDeinitWithoutStopTerminatesProcessCleanly() throws {
        guard X264Process.canSpawn else { throw XCTSkip("disclaim unavailable") }
        let ended = DispatchSemaphore(value: 0)
        var process: X264Process? = try X264Process(ffmpeg: "/bin/sleep", arguments: ["60"]) { event in
            if case .ended = event { ended.signal() }
        }
        let spawnedPid = process!.pid
        XCTAssertGreaterThan(spawnedPid, 0)
        process = nil // deallocates here without `stop()`
        XCTAssertEqual(ended.wait(timeout: .now() + 5), .success, "reader thread must observe child exit and reap it")
        let rc = kill(spawnedPid, 0)
        let err = errno
        XCTAssertEqual(rc, -1)
        XCTAssertEqual(err, ESRCH, "child process must be gone after deinit")
    }

    func testSessionDeinitWithoutStopTerminatesBothProcesses() throws {
        guard X264Process.canSpawn else { throw XCTSkip("disclaim unavailable") }
        let queue = DispatchQueue(label: "test.x264.deinit")
        let ended = DispatchSemaphore(value: 0)
        final class Counter: @unchecked Sendable {
            private let lock = NSLock()
            private var value = 0
            func inc() { lock.withLock { value += 1 } }
            func get() -> Int { lock.withLock { value } }
        }
        let sessionEvents = Counter()
        var pids: [pid_t] = []
        var session: X264Session<Int>? = X264Session<Int>(
            ffmpeg: "/bin/sleep", width: 320, height: 180, fps: 30, threads: 1, capKbps: 1_000,
            queue: queue, clock: { DispatchTime.now().uptimeNanoseconds / 1000 },
            makeProcess: { _, _, onEvent in
                let proc = try X264Process(ffmpeg: "/bin/sleep", arguments: ["60"]) { event in
                    if case .ended = event { ended.signal() }
                    onEvent(event)
                }
                pids.append(proc.pid)
                return proc
            },
            onEvent: { _ in sessionEvents.inc() }
        )
        var started = false
        queue.sync { started = session!.start() }
        XCTAssertTrue(started)
        XCTAssertEqual(pids.count, 2)
        session = nil // deallocates here without `stop()`
        XCTAssertEqual(ended.wait(timeout: .now() + 5), .success, "first process must exit and be reaped")
        XCTAssertEqual(ended.wait(timeout: .now() + 5), .success, "second process must exit and be reaped")
        for pid in pids {
            let rc = kill(pid, 0)
            let err = errno
            XCTAssertEqual(rc, -1)
            XCTAssertEqual(err, ESRCH, "child process \(pid) must be gone after session deinit")
        }
        // Drain the serial queue to confirm no stray session callbacks after deinit.
        queue.sync {}
        XCTAssertEqual(sessionEvents.get(), 0)
    }

    func testStopAfterNaturalExitDoesNotSignalReapedPid() throws {
        guard X264Process.canSpawn else { throw XCTSkip("disclaim unavailable") }
        final class SignalLog: @unchecked Sendable {
            private let lock = NSLock()
            private var calls: [(pid_t, Int32)] = []
            func record(_ pid: pid_t, _ sig: Int32) -> Int32 {
                lock.withLock { calls.append((pid, sig)) }
                return Darwin.kill(pid, sig)
            }
            func all() -> [(pid_t, Int32)] { lock.withLock { calls } }
        }
        let signals = SignalLog()
        let ended = DispatchSemaphore(value: 0)
        let process = try X264Process(ffmpeg: "/usr/bin/true", arguments: [], signal: { signals.record($0, $1) }) { event in
            if case .ended = event { ended.signal() }
        }
        XCTAssertEqual(ended.wait(timeout: .now() + 5), .success, "child should exit naturally and be reaped")
        process.stop()
        XCTAssertTrue(signals.all().isEmpty, "must not send SIGKILL to an already-reaped PID: \(signals.all())")
    }

    func testSessionStartPartialFailureStopsFirstProcess() throws {
        guard X264Process.canSpawn else { throw XCTSkip("disclaim unavailable") }
        let queue = DispatchQueue(label: "test.x264.partial-start")
        let firstEnded = DispatchSemaphore(value: 0)
        var attempts = 0
        var firstPid: pid_t = 0
        let session = X264Session<Int>(
            ffmpeg: "/bin/sleep", width: 320, height: 180, fps: 30, threads: 1, capKbps: 1_000,
            queue: queue, clock: { DispatchTime.now().uptimeNanoseconds / 1000 },
            makeProcess: { _, _, onEvent in
                attempts += 1
                if attempts == 1 {
                    let proc = try X264Process(ffmpeg: "/bin/sleep", arguments: ["60"]) { event in
                        if case .ended = event { firstEnded.signal() }
                        onEvent(event)
                    }
                    firstPid = proc.pid
                    return proc
                }
                throw X264Process.SpawnError.system("posix_spawn", ENOMEM)
            },
            onEvent: { _ in }
        )
        var started = true
        queue.sync { started = session.start() }
        XCTAssertFalse(started)
        XCTAssertEqual(attempts, 2)
        XCTAssertGreaterThan(firstPid, 0)
        XCTAssertEqual(firstEnded.wait(timeout: .now() + 5), .success, "first process must be stopped when second spawn fails")
        let rc = kill(firstPid, 0)
        let err = errno
        XCTAssertEqual(rc, -1)
        XCTAssertEqual(err, ESRCH)
    }

    func testMalformedOutputKillsChildAndPostExitStopDoesNotResignal() throws {
        guard X264Process.canSpawn else { throw XCTSkip("disclaim unavailable") }
        final class SignalLog: @unchecked Sendable {
            private let lock = NSLock()
            private var calls: [(pid_t, Int32)] = []
            func record(_ pid: pid_t, _ sig: Int32) -> Int32 {
                lock.withLock { calls.append((pid, sig)) }
                return Darwin.kill(pid, sig)
            }
            func all() -> [(pid_t, Int32)] { lock.withLock { calls } }
        }
        let signals = SignalLog()
        let ended = DispatchSemaphore(value: 0)
        final class ErrorBox: @unchecked Sendable {
            private let lock = NSLock()
            private var value: Error?
            func set(_ e: Error?) { lock.withLock { value = e } }
            func get() -> Error? { lock.withLock { value } }
        }
        let box = ErrorBox()
        let process = try X264Process(ffmpeg: "/bin/echo", arguments: ["RIFF000000000000"], signal: { signals.record($0, $1) }) { event in
            if case let .ended(err) = event {
                box.set(err)
                ended.signal()
            }
        }
        XCTAssertEqual(ended.wait(timeout: .now() + 5), .success)
        XCTAssertEqual(box.get() as? FLVReader.Failure, .notFLV)
        let beforeStop = signals.all().count
        XCTAssertEqual(beforeStop, 1, "reader should kill child once on malformed FLV")
        process.stop()
        XCTAssertEqual(signals.all().count, beforeStop, "stop() after reap must not signal the PID again")
    }
}

/// The real thing, with the ffmpeg named by `PERSONA_TEST_FFMPEG` (e.g. Homebrew's /opt/homebrew/bin/ffmpeg, built with
/// libx264); skipped without it. Frames go in, come out in order as keyframe-first H.264 the phone's decoder takes
/// (VTDecompressionSession, as ScreenVideoStream decodes), and a keyframe on request comes from the spare.
final class X264SessionTests: XCTestCase {
    func testEncodesAndDecodes() throws {
        guard let ffmpeg = ProcessInfo.processInfo.environment["PERSONA_TEST_FFMPEG"], !ffmpeg.isEmpty else {
            throw XCTSkip("set PERSONA_TEST_FFMPEG to an ffmpeg with libx264")
        }
        XCTAssertTrue(X264Process.canSpawn)
        let (width, height, count) = (320, 180, 8)
        let queue = DispatchQueue(label: "test.x264")
        let done = expectation(description: "frames")
        var sets: [Data] = []
        var frames: [(sample: Data, keyframe: Bool, number: Int, micros: Int64)] = []
        var off: X264Plan.Off?
        let session = X264Session<Int>(ffmpeg: ffmpeg, width: width, height: height, fps: 60, threads: 2, capKbps: 8_000,
                                       queue: queue, clock: { DispatchTime.now().uptimeNanoseconds / 1000 }) { event in
            switch event {
            case let .parameterSets(s): sets = s
            case let .frame(sample, keyframe, number, micros):
                frames.append((sample, keyframe, number, micros))
                if frames.count == count { done.fulfill() }
            case let .off(reason, _): off = reason; done.fulfill()
            }
        }
        var started = false
        queue.sync { started = session.start() }
        XCTAssertTrue(started)
        for i in 0 ..< count {
            let frame = Self.nv12(width: width, height: height, shade: UInt8(16 + i * 20))
            queue.sync {
                session.encode(context: i, keyframe: i == 5) { write in
                    frame.withUnsafeBytes { _ = write($0.baseAddress!, $0.count) }
                }
            }
            Thread.sleep(forTimeInterval: 0.02)
        }
        wait(for: [done], timeout: 10)
        queue.sync { session.stop() }
        XCTAssertNil(off)
        XCTAssertEqual(frames.map(\.number), Array(0 ..< count))
        XCTAssertEqual(frames.map(\.keyframe), (0 ..< count).map { $0 == 0 || $0 == 5 })
        XCTAssertEqual(sets.count, 2)
        XCTAssertEqual(sets.first?.first.map { $0 & 0x1F }, 7) // SPS
        for f in frames {
            XCTAssertGreaterThan(f.micros, 0)
            var p = f.sample.startIndex // no SEI left in a sample
            while p < f.sample.endIndex {
                let length = Int(f.sample[p]) << 24 | Int(f.sample[p + 1]) << 16 | Int(f.sample[p + 2]) << 8 | Int(f.sample[p + 3])
                XCTAssertNotEqual(f.sample[p + 4] & 0x1F, 6)
                p += 4 + length
            }
        }
        try Self.decode(frames.map(\.sample), sets: sets, width: width, height: height)
    }

    /// #8859: from a cold ffmpeg, 60 frames of 1080p at 60 fps stay on x264 (the first real session stopped it as too
    /// slow a fraction of a second in, on frames that only waited behind the first), and come out in order.
    func testAColdStartAt1080p60StaysOnX264() throws {
        guard let ffmpeg = ProcessInfo.processInfo.environment["PERSONA_TEST_FFMPEG"], !ffmpeg.isEmpty else {
            throw XCTSkip("set PERSONA_TEST_FFMPEG to an ffmpeg with libx264")
        }
        let (width, height, count) = (1920, 1080, 60)
        let pictures = (0 ..< 4).map { Self.nv12(width: width, height: height, shade: UInt8(30 + $0 * 40)) }
        let queue = DispatchQueue(label: "test.x264.1080p", qos: .userInteractive)
        let done = expectation(description: "frames")
        var numbers: [Int] = []
        var off: (X264Plan.Off, String)?
        let session = X264Session<Int>(ffmpeg: ffmpeg, width: width, height: height, fps: 60, threads: 8, capKbps: 40_000,
                                       queue: queue, clock: { DispatchTime.now().uptimeNanoseconds / 1000 }) { event in
            switch event {
            case .parameterSets: break
            case let .frame(_, _, number, _):
                numbers.append(number)
                if numbers.count == count { done.fulfill() }
            case let .off(reason, detail): off = (reason, detail); done.fulfill()
            }
        }
        var started = false
        queue.sync { started = session.start() }
        XCTAssertTrue(started)
        let start = DispatchTime.now().uptimeNanoseconds
        for i in 0 ..< count {
            let due = start + UInt64(i) * 16_666_667
            while DispatchTime.now().uptimeNanoseconds < due { usleep(200) }
            let picture = pictures[i % pictures.count]
            queue.sync {
                session.encode(context: i, keyframe: i == 0) { write in
                    picture.withUnsafeBytes { _ = write($0.baseAddress!, $0.count) }
                }
                session.tick()
            }
        }
        wait(for: [done], timeout: 15)
        queue.sync { session.stop() }
        XCTAssertNil(off.map { "\($0.0.rawValue): \($0.1)" })
        XCTAssertEqual(numbers, Array(0 ..< count))
    }

    /// A gray NV12 picture with a gradient, so frames differ.
    static func nv12(width: Int, height: Int, shade: UInt8) -> Data {
        var d = Data(count: width * height * 3 / 2)
        d.withUnsafeMutableBytes { b in
            for y in 0 ..< height { for x in 0 ..< width { b[y * width + x] = shade &+ UInt8(truncatingIfNeeded: x / 4) } }
            for i in (width * height) ..< b.count { b[i] = 128 }
        }
        return d
    }

    static func decode(_ samples: [Data], sets: [Data], width: Int, height: Int) throws {
        var format: CMFormatDescription?
        let status: OSStatus = sets[0].withUnsafeBytes { s in
            sets[1].withUnsafeBytes { p in
                let pointers = [s.bindMemory(to: UInt8.self).baseAddress!, p.bindMemory(to: UInt8.self).baseAddress!]
                return CMVideoFormatDescriptionCreateFromH264ParameterSets(
                    allocator: nil, parameterSetCount: 2, parameterSetPointers: pointers, parameterSetSizes: [sets[0].count, sets[1].count],
                    nalUnitHeaderLength: 4, formatDescriptionOut: &format)
            }
        }
        XCTAssertEqual(status, noErr)
        let dims = CMVideoFormatDescriptionGetDimensions(format!)
        XCTAssertEqual([Int(dims.width), Int(dims.height)], [width, height])
        var session: VTDecompressionSession?
        XCTAssertEqual(VTDecompressionSessionCreate(allocator: nil, formatDescription: format!, decoderSpecification: nil,
                                                    imageBufferAttributes: nil, outputCallback: nil,
                                                    decompressionSessionOut: &session), noErr)
        defer { VTDecompressionSessionInvalidate(session!) }
        for (i, sample) in samples.enumerated() {
            var block: CMBlockBuffer?
            XCTAssertEqual(CMBlockBufferCreateWithMemoryBlock(allocator: nil, memoryBlock: nil, blockLength: sample.count,
                                                              blockAllocator: nil, customBlockSource: nil, offsetToData: 0,
                                                              dataLength: sample.count, flags: 0, blockBufferOut: &block), noErr)
            _ = sample.withUnsafeBytes { CMBlockBufferReplaceDataBytes(with: $0.baseAddress!, blockBuffer: block!,
                                                                        offsetIntoDestination: 0, dataLength: sample.count) }
            var buffer: CMSampleBuffer?
            var size = sample.count
            XCTAssertEqual(CMSampleBufferCreateReady(allocator: nil, dataBuffer: block, formatDescription: format,
                                                     sampleCount: 1, sampleTimingEntryCount: 0, sampleTimingArray: nil,
                                                     sampleSizeEntryCount: 1, sampleSizeArray: &size, sampleBufferOut: &buffer), noErr)
            var picture = false
            let decoded = VTDecompressionSessionDecodeFrame(session!, sampleBuffer: buffer!, flags: [], infoFlagsOut: nil) { status, _, image, _, _ in
                picture = status == noErr && image != nil
            }
            XCTAssertEqual(decoded, noErr, "frame \(i)")
            XCTAssertTrue(picture, "frame \(i) gave no picture")
        }
    }
}
#endif
