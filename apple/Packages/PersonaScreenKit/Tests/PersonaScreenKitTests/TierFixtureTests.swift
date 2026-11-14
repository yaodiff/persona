import Foundation
import XCTest
@testable import PersonaScreenKit

/// The quality tiers' shared cases (spec/fixtures/screen-tiers, #6396 / #6558): the browser's port
/// (web/src/screen/tiers/) must give exactly what these Swift types give. One scenario per file, its input and the
/// expected output, no internal state. `PERSONA_WRITE_TIER_FIXTURES=1 swift test --filter TierFixtureTests` writes them;
/// otherwise every committed file must be exactly what the scenarios write now, so a change in behaviour on either side
/// fails until the files and the other side follow.
final class TierFixtureTests: XCTestCase {
    // MARK: Ladder

    private struct LadderCase {
        let name: String
        let description: String
        let points: [Int]
        let pixels: [Int]
        var maxSize = 2556
        var bias: TierLadder.Bias = .auto
        var capKbps = 12_000
        var maxFps = 60
        var codec = "hevc"
    }

    private static let retina = (points: [1512, 982], pixels: [3024, 1964])

    private static let ladders: [LadderCase] = [
        LadderCase(name: "retina_auto_wifi", description: "A 14-inch Retina MacBook, Auto, the phone on Wi-Fi.",
                   points: retina.points, pixels: retina.pixels),
        LadderCase(name: "retina_auto_cellular", description: "The same on cellular: 4 Mbit/s cap.",
                   points: retina.points, pixels: retina.pixels, capKbps: 4_000),
        LadderCase(name: "retina_auto_low_data", description: "Low Data Mode: 1.5 Mbit/s cap; tiers whose floor is over it go.",
                   points: retina.points, pixels: retina.pixels, capKbps: 1_500),
        LadderCase(name: "retina_sharper", description: "Sharper: the pixels kept, frame rate given up first.",
                   points: retina.points, pixels: retina.pixels, bias: .sharper),
        LadderCase(name: "retina_smoother", description: "Smoother: the frame rate kept, pixels given up first.",
                   points: retina.points, pixels: retina.pixels, bias: .smoother),
        LadderCase(name: "retina_warm", description: "Low Power Mode or a warm phone: 15 fps at most; tiers merge.",
                   points: retina.points, pixels: retina.pixels, maxFps: 15),
        LadderCase(name: "retina_no_cap", description: "No cap from the client (0): the helper's own 50 Mbit/s.",
                   points: retina.points, pixels: retina.pixels, capKbps: 0),
        LadderCase(name: "retina_browser_desktop", description: "A desktop browser: no long-edge cap (0), Wi-Fi.",
                   points: retina.points, pixels: retina.pixels, maxSize: 0),
        LadderCase(name: "retina_h264", description: "A browser without HEVC: the ladder for H.264.",
                   points: retina.points, pixels: retina.pixels, codec: "h264"),
        LadderCase(name: "display_1x_1080p", description: "A 1× display: 2×, 1.5× and 1× are the same pixels and merge.",
                   points: [1920, 1080], pixels: [1920, 1080]),
        LadderCase(name: "studio_display_5k", description: "A 5K display under a 1920 long-edge cap.",
                   points: [2560, 1440], pixels: [5120, 2880], maxSize: 1920),
        LadderCase(name: "portrait", description: "A display in portrait.", points: [982, 1512], pixels: [1964, 3024]),
        LadderCase(name: "tiny_cap", description: "A cap at the helper's floor: only the last tier is left.",
                   points: [800, 600], pixels: [800, 600], capKbps: 200),
    ]

    private static func ladderFile(_ c: LadderCase) -> [String: Any] {
        let tiers = TierLadder.make(points: (c.points[0], c.points[1]), pixels: (c.pixels[0], c.pixels[1]), maxSize: c.maxSize,
                                    bias: c.bias, capKbps: c.capKbps, maxFps: c.maxFps, codec: c.codec)
        return ["name": c.name, "description": c.description,
                "input": ["points": c.points, "pixels": c.pixels, "maxSize": c.maxSize, "bias": c.bias.rawValue,
                          "capKbps": c.capKbps, "maxFps": c.maxFps, "codec": c.codec],
                "expected": tiers.map { ["scale": $0.scale, "fps": $0.fps, "width": $0.width, "height": $0.height,
                                         "minKbps": $0.minKbps, "maxKbps": $0.maxKbps] }]
    }

    // MARK: Signals

    /// One step of a signals scenario: an event, or `take` with what it returned.
    private enum Step {
        case frame(capture: UInt64, arrival: UInt64, firstByte: UInt64?, bytes: Int, refinement: Bool, fps: Int)
        case decoded(ms: Double, at: UInt64)
        case shown(replaced: Bool, at: UInt64)
        case pathChanged
        case take(now: UInt64)
    }

    private struct SignalsCase {
        let name: String
        let description: String
        let steps: [Step]
    }

    /// `count` frames from `start` µs every `every` µs, each `delay(i)` µs after its capture (on a clock 5 s off, as two
    /// machines' are), `bytes(i)` long, and a take after the last.
    private static func frames(_ count: Int, start: UInt64 = 1_000_000, every: UInt64 = 16_667, fps: Int = 60,
                               delay: (Int) -> UInt64 = { _ in 20_000 }, bytes: (Int) -> Int = { _ in 20_000 },
                               firstByte: (Int) -> UInt64? = { _ in nil }, refinement: (Int) -> Bool = { _ in false },
                               decode: Double? = nil, replaced: (Int) -> Bool? = { _ in nil }) -> [Step] {
        var steps: [Step] = []
        for i in 0 ..< count {
            let capture = start + UInt64(i) * every
            let arrival = capture + 5_000_000 + delay(i)
            steps.append(.frame(capture: capture, arrival: arrival, firstByte: firstByte(i).map { arrival - $0 },
                                bytes: bytes(i), refinement: refinement(i), fps: fps))
            if let decode { steps.append(.decoded(ms: decode, at: arrival + UInt64(decode * 1000))) }
            if let r = replaced(i) { steps.append(.shown(replaced: r, at: arrival + 8_000)) }
        }
        return steps
    }

    private static func lastArrival(_ steps: [Step]) -> UInt64 {
        steps.reduce(0) { at, s in if case let .frame(_, a, _, _, _, _) = s { max(at, a) } else { at } }
    }

    private static let signals: [SignalsCase] = {
        var cases: [SignalsCase] = []
        func add(_ name: String, _ description: String, _ steps: [Step], takes: [UInt64] = [0]) {
            let end = lastArrival(steps)
            cases.append(SignalsCase(name: name, description: description, steps: steps + takes.map { .take(now: end + $0) }))
        }
        add("steady", "60 fps at a constant delay: no queue, nothing late; decoding and showing keep up.",
            frames(120, decode: 3, replaced: { _ in false }), takes: [0, 600_000])
        add("queue_rising", "The delay grows 2 ms a frame for a second: the queue rises and frames come late.",
            frames(60, delay: { 20_000 + UInt64($0) * 2_000 }))
        add("queue_falling", "The delay rises for 40 frames, then drains: over the last second the queue falls.",
            frames(80, delay: { $0 < 40 ? 20_000 + UInt64($0) * 4_000 : 180_000 - UInt64($0 - 40) * 4_000 }))
        add("busy_link", "Every frame queued over 50 ms behind the one before: the delivered rate is the link's capacity.",
            frames(40, every: 25_000, fps: 30, delay: { 80_000 + UInt64($0) * 9_000 }, bytes: { _ in 60_000 }))
        add("quiet_demand", "A few frames, no queue: the delivered rate is what came over the last second.",
            frames(8, every: 100_000, fps: 30, bytes: { _ in 5_000 }))
        add("bursts", "Large frames over several reads say how fast the link moved them; a second take has none left.",
            frames(30, every: 33_333, fps: 30, bytes: { $0 % 10 == 0 ? 64_000 : 8_000 },
                   firstByte: { $0 % 10 == 0 ? 20_000 : nil }), takes: [0, 1])
        add("refinement", "Idle refinement's large frames count as bursts but not in the queue or the delivered rate.",
            frames(12, every: 100_000, fps: 30, delay: { $0 % 3 == 2 ? 300_000 : 20_000 },
                   bytes: { $0 % 3 == 2 ? 48_000 : 4_000 }, firstByte: { $0 % 3 == 2 ? 40_000 : nil },
                   refinement: { $0 % 3 == 2 }))
        add("decode_behind", "Slow decodes and pictures replaced before they showed.",
            frames(60, decode: 24, replaced: { $0 % 2 == 0 }))
        var moved = frames(30)
        moved.append(.pathChanged)
        moved += frames(30, start: 1_600_000, delay: { _ in 60_000 })
        add("path_changed", "A new route: the delay floor starts over, so a slower route's delay alone isn't queueing.", moved)
        cases.append(SignalsCase(name: "still_screen", description: "Nothing arrived: no frames, nothing measured.",
                                 steps: [.take(now: 5_000_000)]))
        var window = frames(10, delay: { _ in 10_000 })
        window += frames(10, start: 12_500_000, delay: { _ in 40_000 })
        add("baseline_window", "A delay floor older than 10 s is dropped: the new floor is the newer frames'.", window)
        return cases
    }()

    private static func signalsFile(_ c: SignalsCase) -> [String: Any] {
        var r = ReceiverSignals()
        var steps: [[String: Any]] = []
        for step in c.steps {
            switch step {
            case let .frame(capture, arrival, firstByte, bytes, refinement, fps):
                r.frame(captureMicros: capture, arrivalMicros: arrival, firstByteMicros: firstByte, bytes: bytes,
                        refinement: refinement, fps: fps)
                steps.append(["frame": ["capture": capture, "arrival": arrival, "firstByte": firstByte.map { $0 as Any } ?? NSNull(),
                                        "bytes": bytes, "refinement": refinement, "fps": fps]])
            case let .decoded(ms, at):
                r.decoded(ms: ms, at: at)
                steps.append(["decoded": ["ms": ms, "at": at]])
            case let .shown(replaced, at):
                r.shown(replaced: replaced, at: at)
                steps.append(["shown": ["replaced": replaced, "at": at]])
            case .pathChanged:
                r.pathChanged()
                steps.append(["pathChanged": [String: Any]()])
            case let .take(now):
                let s = r.take(now: now)
                func v(_ d: Double?) -> Any { d.map { $0 as Any } ?? NSNull() }
                steps.append(["take": ["now": now],
                              "expected": ["frames": s.frames, "queueP50Ms": v(s.queueP50Ms), "queueP95Ms": v(s.queueP95Ms),
                                           "queueRising": s.queueRising, "queueFalling": s.queueFalling,
                                           "deliveredKbps": v(s.deliveredKbps), "burstKbps": s.burstKbps,
                                           "lateShare": v(s.lateShare), "decodeP95Ms": v(s.decodeP95Ms),
                                           "replacedShare": v(s.replacedShare)]])
            }
        }
        return ["name": c.name, "description": c.description, "steps": steps]
    }

    // MARK: Policy

    private enum Op {
        case helper(target: Int?, sent: Int, skipped: Int?)
        case tick(TierSignals, now: Double)
        case path(LinkStats, now: Double)
        case setBias(TierLadder.Bias, now: Double)
        case setBudget(cap: Int, fps: Int, maxScale: Double?, now: Double)
    }

    private struct PolicyCase {
        let name: String
        let description: String
        var display = TierPolicy.Display(points: [1512, 982], pixels: [3024, 1964], maxSize: 2868)
        var bias: TierLadder.Bias = .auto
        var capKbps = 12_000
        var maxFps = 60
        var maxScale: Double?
        let link: LinkStats?
        var remembered: Int?
        var ops: [Op] = []
    }

    private static func sig(queue: Double, p95: Double? = nil, delivered: Double? = nil, rising: Bool = false,
                            falling: Bool = false, frames: Int = 30, bursts: [Double] = [], decode: Double? = 4) -> TierSignals {
        var s = TierSignals()
        s.frames = frames
        s.queueP50Ms = queue
        s.queueP95Ms = p95 ?? queue
        s.deliveredKbps = delivered
        s.queueRising = rising
        s.queueFalling = falling
        s.burstKbps = bursts
        s.decodeP95Ms = decode
        s.replacedShare = 0
        return s
    }

    /// Ticks every 250 ms from `from` while `now < to`, with the same signals.
    private static func ticks(from: Double, to: Double, _ s: TierSignals) -> [Op] {
        stride(from: from, to: to, by: 0.25).map { .tick(s, now: $0) }
    }

    private static let lan = LinkStats(kind: .lan, via: .wifi)
    private static let relay = LinkStats(kind: .relay, via: .wifi)
    private static let cellular = LinkStats(kind: .relay, via: .cellular)

    private static let policies: [PolicyCase] = [
        PolicyCase(name: "start_lan", description: "The same network: the best tier.", link: lan),
        PolicyCase(name: "start_tailscale_fast", description: "Tailscale under 30 ms: the best tier.",
                   link: LinkStats(kind: .tailscale, via: .wifi, minRttMs: 18)),
        PolicyCase(name: "start_tailscale_slow", description: "Tailscale over 30 ms: the second.",
                   link: LinkStats(kind: .tailscale, via: .cellular, minRttMs: 60)),
        PolicyCase(name: "start_relay_wifi", description: "The relay over Wi-Fi: the second.", link: relay),
        PolicyCase(name: "start_relay_cellular", description: "The relay over cellular, under the cellular cap: the third rung.",
                   capKbps: 4_000, link: cellular),
        PolicyCase(name: "start_unmeasured", description: "No route measured yet: as the relay over Wi-Fi.", link: nil),
        PolicyCase(name: "start_remembered", description: "Where this computer last settled over this route.", link: relay,
                   remembered: 3),
        PolicyCase(name: "start_data_saver", description: "Data Saver on mobile data: 1.5 Mbit/s and one pixel per point.",
                   capKbps: 1_500, maxScale: 1, link: cellular),
        PolicyCase(name: "long_queue_jumps", description: "A queue over 400 ms for a quarter second jumps to what arrived.",
                   link: lan, ops: [.tick(sig(queue: 500, delivered: 3_000), now: 1), .tick(sig(queue: 500, delivered: 3_000), now: 1.25)]),
        PolicyCase(name: "skipped_frames", description: "Frames the helper skipped count as a long queue; no rate: one down.",
                   link: lan, ops: [.helper(target: 7_000, sent: 7_000, skipped: 4), .tick(sig(queue: 100), now: 1),
                                    .tick(sig(queue: 100), now: 1.25)]),
        PolicyCase(name: "exhausted_band", description: "The bitrate at the band's floor and the queue not draining: down, spaced, and at once for a runaway queue.",
                   link: lan, ops: [.helper(target: 10_000, sent: 10_000, skipped: 0), .tick(sig(queue: 200), now: 1),
                                    .helper(target: 6_500, sent: 6_500, skipped: 0), .tick(sig(queue: 200, delivered: 5_500), now: 1.25),
                                    .helper(target: 4_100, sent: 4_100, skipped: 0), .tick(sig(queue: 200), now: 1.75),
                                    .tick(sig(queue: 200, falling: true), now: 2.5), .tick(sig(queue: 200), now: 2.75),
                                    .helper(target: 2_100, sent: 2_100, skipped: 0), .tick(sig(queue: 280, rising: true), now: 3),
                                    .tick(sig(queue: 520, rising: true), now: 3.25)]),
        PolicyCase(name: "backlog_rides_out", description: "A tier whose floor fits what arrives rides out a backlog.",
                   link: relay, ops: [.helper(target: 4_100, sent: 4_100, skipped: 3)]
                       + ticks(from: 0.25, to: 3.25, sig(queue: 700, delivered: 5_500))
                       + [.helper(target: 4_100, sent: 4_100, skipped: 0), .tick(sig(queue: 700, delivered: 4_000), now: 3.25)]),
        PolicyCase(name: "phone_behind", description: "Decoding behind for 2 s: the next tier with a lower frame rate.",
                   link: lan, ops: [.tick(sig(queue: 5, decode: 30), now: 1), .tick(sig(queue: 5, decode: 30), now: 2.75),
                                    .tick(sig(queue: 5, decode: 30), now: 3)]),
        PolicyCase(name: "up_needs_proof", description: "A calm window isn't enough: up only once the link carried more.",
                   link: relay, ops: [.helper(target: 12_000, sent: 300, skipped: 0)] + ticks(from: 0.25, to: 12, sig(queue: 5))
                       + [.helper(target: 12_000, sent: 11_000, skipped: 0)] + ticks(from: 12, to: 22, sig(queue: 5))),
        PolicyCase(name: "first_seconds", description: "In the first 10 s a step up waits 3 s, not 8.",
                   link: relay, ops: [.helper(target: 12_000, sent: 11_500, skipped: 0)] + ticks(from: 0.25, to: 4, sig(queue: 5))),
        PolicyCase(name: "bursts_prove_headroom", description: "Three large frames at 1.5× the next floor prove headroom.",
                   link: relay, ops: [.helper(target: 12_000, sent: 200, skipped: 0), .tick(sig(queue: 5, bursts: [20_000]), now: 0.25),
                                      .tick(sig(queue: 5, bursts: [8_000]), now: 0.5), .tick(sig(queue: 5, bursts: [16_000, 18_000]), now: 0.75)]
                       + ticks(from: 1, to: 4, sig(queue: 5))),
        PolicyCase(name: "undone_step_up", description: "A step up undone within 15 s doubles that tier's window.",
                   link: relay, ops: [.helper(target: 12_000, sent: 11_500, skipped: 0)] + ticks(from: 0.25, to: 3.5, sig(queue: 5))
                       + [.tick(sig(queue: 600, delivered: 7_000), now: 13.25), .tick(sig(queue: 600, delivered: 7_000), now: 13.5)]
                       + ticks(from: 13.75, to: 30, sig(queue: 5))),
        PolicyCase(name: "still_screen", description: "A still screen (no frames) neither proves nor breaks the window.",
                   link: relay, ops: [.helper(target: 12_000, sent: 11_500, skipped: 0), .tick(sig(queue: 5), now: 0.25)]
                       + ticks(from: 0.5, to: 3, sig(queue: 0, frames: 0)) + [.tick(sig(queue: 5), now: 3.25)]),
        PolicyCase(name: "slower_route", description: "A slower route steps down at once; a faster one keeps the tier.",
                   link: lan, ops: [.path(cellular, now: 5), .path(lan, now: 30)]),
        PolicyCase(name: "new_bias", description: "A new bias takes the tier the rate fits, never richer in pixels a second.",
                   link: lan, ops: [.helper(target: 3_000, sent: 3_000, skipped: 0), .setBias(.sharper, now: 1), .setBias(.sharper, now: 2),
                                    .setBias(.smoother, now: 3)]),
        PolicyCase(name: "data_saver_budget", description: "Choosing Data Saver moves the stream at once; Clearer back.",
                   capKbps: 4_000, link: cellular, ops: [.helper(target: 2_500, sent: 2_500, skipped: 0),
                                                         .setBudget(cap: 1_500, fps: 60, maxScale: 1, now: 1),
                                                         .setBudget(cap: 4_000, fps: 60, maxScale: nil, now: 2)]),
        PolicyCase(name: "warm_phone", description: "A frame-rate cap is the phone being warm; a lower bitrate cap the network.",
                   link: lan, ops: [.helper(target: 9_000, sent: 9_000, skipped: 0), .setBudget(cap: 12_000, fps: 15, maxScale: nil, now: 1),
                                    .setBudget(cap: 1_500, fps: 15, maxScale: nil, now: 2)]),
    ]

    private static func linkObject(_ l: LinkStats?) -> Any {
        guard let l else { return NSNull() }
        return ["kind": l.kind.rawValue, "via": l.via.rawValue, "minRttMs": l.minRttMs.map { $0 as Any } ?? NSNull(),
                "srttMs": l.srttMs.map { $0 as Any } ?? NSNull()]
    }

    private static func changeObject(_ c: TierPolicy.Change?) -> Any {
        guard let c else { return NSNull() }
        return ["index": c.index, "scale": c.tier.scale, "fps": c.tier.fps, "width": c.tier.width, "height": c.tier.height,
                "minKbps": c.tier.minKbps, "maxKbps": c.tier.maxKbps, "targetKbps": c.targetKbps, "reason": c.reason.rawValue]
    }

    private static func signalsObject(_ s: TierSignals) -> [String: Any] {
        func v(_ d: Double?) -> Any { d.map { $0 as Any } ?? NSNull() }
        return ["frames": s.frames, "queueP50Ms": v(s.queueP50Ms), "queueP95Ms": v(s.queueP95Ms), "queueRising": s.queueRising,
                "queueFalling": s.queueFalling, "deliveredKbps": v(s.deliveredKbps), "burstKbps": s.burstKbps,
                "lateShare": v(s.lateShare), "decodeP95Ms": v(s.decodeP95Ms), "replacedShare": v(s.replacedShare)]
    }

    private static func policyFile(_ c: PolicyCase) -> [String: Any] {
        var p = TierPolicy(display: c.display, bias: c.bias, capKbps: c.capKbps, maxFps: c.maxFps, maxScale: c.maxScale,
                           link: c.link, remembered: c.remembered, now: 0)
        let start = changeObject(p.last) // init's answer: the stream's first tier
        var steps: [[String: Any]] = []
        for op in c.ops {
            switch op {
            case let .helper(target, sent, skipped):
                p.helper(TimingMessage(captureMs: nil, encodeMs: nil, networkMs: nil, kbps: sent, fps: 60, targetKbps: target,
                                       skipped: skipped))
                steps.append(["helper": ["targetKbps": target.map { $0 as Any } ?? NSNull(), "kbps": sent,
                                         "skipped": skipped.map { $0 as Any } ?? NSNull()]])
            case let .tick(s, now):
                steps.append(["tick": ["signals": signalsObject(s), "now": now], "expected": changeObject(p.tick(s, now: now))])
            case let .path(to, now):
                let change = PathChange(from: nil, to: to, reason: .networkChanged)
                steps.append(["path": ["to": linkObject(to), "now": now], "expected": changeObject(p.path(change, now: now))])
            case let .setBias(b, now):
                steps.append(["setBias": ["bias": b.rawValue, "now": now], "expected": changeObject(p.setBias(b, now: now))])
            case let .setBudget(cap, fps, scale, now):
                steps.append(["setBudget": ["capKbps": cap, "maxFps": fps, "maxScale": scale.map { $0 as Any } ?? NSNull(), "now": now],
                              "expected": changeObject(p.setBudget(capKbps: cap, maxFps: fps, maxScale: scale, now: now))])
            }
        }
        return ["name": c.name, "description": c.description,
                "init": ["display": ["points": c.display.points, "pixels": c.display.pixels, "maxSize": c.display.maxSize],
                         "bias": c.bias.rawValue, "capKbps": c.capKbps, "maxFps": c.maxFps,
                         "maxScale": c.maxScale.map { $0 as Any } ?? NSNull(), "link": linkObject(c.link),
                         "remembered": c.remembered.map { $0 as Any } ?? NSNull(), "now": 0],
                "start": start, "steps": steps]
    }

    // MARK: Files

    private static var root: URL {
        URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent() // the package
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent() // the repository
            .appendingPathComponent("spec/fixtures/screen-tiers")
    }

    private static func encoded(_ object: [String: Any]) throws -> Data {
        var data = try JSONSerialization.data(withJSONObject: object, options: [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes])
        data.append(0x0a)
        return data
    }

    private func check(_ dir: String, _ files: [(String, [String: Any])]) throws {
        let folder = Self.root.appendingPathComponent(dir)
        let write = ProcessInfo.processInfo.environment["PERSONA_WRITE_TIER_FIXTURES"] == "1"
        if write { try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true) }
        let committed = Set((try? FileManager.default.contentsOfDirectory(atPath: folder.path)) ?? [])
        for (name, object) in files {
            let url = folder.appendingPathComponent(name + ".json")
            let data = try Self.encoded(object)
            if write { try data.write(to: url); continue }
            let have = try Data(contentsOf: url)
            XCTAssertEqual(String(decoding: have, as: UTF8.self), String(decoding: data, as: UTF8.self),
                           "spec/fixtures/screen-tiers/\(dir)/\(name).json is out of date: run PERSONA_WRITE_TIER_FIXTURES=1 swift test --filter TierFixtureTests, and bring web/src/screen/tiers along")
        }
        if !write {
            let stale = committed.subtracting(files.map { $0.0 + ".json" })
            XCTAssertTrue(stale.isEmpty, "files no scenario writes any more: \(stale.sorted())")
        }
    }

    func testTheLadderCases() throws {
        try check("ladder", Self.ladders.map { ($0.name, Self.ladderFile($0)) })
    }

    func testTheSignalsCases() throws {
        try check("signals", Self.signals.map { ($0.name, Self.signalsFile($0)) })
    }

    func testThePolicyCases() throws {
        try check("policy", Self.policies.map { ($0.name, Self.policyFile($0)) })
    }
}
