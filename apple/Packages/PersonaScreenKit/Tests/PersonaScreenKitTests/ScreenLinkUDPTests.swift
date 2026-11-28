import Foundation
import XCTest
@testable import PersonaScreenKit

/// In-memory datagram sockets: the phone's end is a `ScreenUDPSocket`; the node's end drops what `drop` picks.
final class DatagramEnd: ScreenUDPSocket, @unchecked Sendable {
    private let lock = NSLock()
    private var inbox: [Data] = []
    private var waiters: [CheckedContinuation<Data, Error>] = []
    private var closed = false
    weak var peer: DatagramEnd?
    /// Drops a datagram this end sends when it says so (by its order).
    var drop: (@Sendable (Int) -> Bool)?
    private var sent = 0

    static func pair() -> (DatagramEnd, DatagramEnd) {
        let a = DatagramEnd(), b = DatagramEnd()
        a.peer = b
        b.peer = a
        return (a, b)
    }

    func send(_ packet: Data) {
        let n = lock.withLock { () -> Int in sent += 1; return sent - 1 }
        if let drop, drop(n) { return }
        peer?.put(packet)
    }

    func receive() async throws -> Data {
        try await withCheckedThrowingContinuation { k in
            lock.lock()
            if !inbox.isEmpty { let d = inbox.removeFirst(); lock.unlock(); k.resume(returning: d); return }
            if closed { lock.unlock(); k.resume(throwing: ScreenLinkError.closed); return }
            waiters.append(k)
            lock.unlock()
        }
    }

    func close() {
        lock.lock()
        closed = true
        let ws = waiters
        waiters = []
        lock.unlock()
        ws.forEach { $0.resume(throwing: ScreenLinkError.closed) }
    }

    private func put(_ d: Data) {
        lock.lock()
        if closed { lock.unlock(); return }
        if let k = waiters.first { waiters.removeFirst(); lock.unlock(); k.resume(returning: d); return }
        inbox.append(d)
        lock.unlock()
    }
}

/// A direct connection's pipe end that exports keys as its TLS connection would: both ends compute the same bytes.
final class ExportingPipe: ScreenLinkTransport, ScreenLinkKeyExporter, @unchecked Sendable {
    let end: PipeEnd
    init(_ end: PipeEnd) { self.end = end }
    func send(_ data: Data) async throws { try await end.send(data) }
    func receive() async throws -> Data { try await end.receive() }
    func close() { end.close() }
    func exportKeyingMaterial(label: String, context: Data, length: Int) -> Data? { Self.material(context, length) }
    static func material(_ context: Data, _ length: Int) -> Data {
        Data((0 ..< length).map { UInt8(truncatingIfNeeded: $0 * 13 + Int(context.first ?? 0)) })
    }
}

/// A direct route's node in a test: its TLS connection (attach, pongs, `use`, `udp_open`) and its UDP flow (the proof,
/// `use`, pongs, video as fragments with parity, NACKs, give-ups).
final class UDPRouteNode: @unchecked Sendable {
    let tls: PipeEnd
    let udp: DatagramEnd
    let flowID = Data([9, 8, 7, 6, 5, 4, 3, 2])
    private let lock = NSLock()
    private var tlsSeen: [ScreenLinkMessage] = []
    private var udpSeen: [ScreenUDPMessage] = []
    private var seenNumbers: [UInt64] = []
    private var replays = 0
    private var sealer: ScreenUDPSealer
    private let opener: TestAESGCM
    private let openIV: Data
    private var window = ScreenUDPReplayWindow()
    /// What the node sent of the video, by offset, so NACKs can be answered.
    private var sentMessages: [UInt64: (Data, Bool)] = [:]
    /// The flow goes silent: its packets vanish (the TLS connection still answers).
    var udpSilent: Bool { get { lock.withLock { mute } } set { lock.withLock { mute = newValue } } }
    private var mute = false
    /// Where the stream goes on after a give-up.
    var resetTo: UInt64 { get { lock.withLock { resetOffset } } set { lock.withLock { resetOffset = newValue } } }
    private var resetOffset: UInt64 = 0
    /// Per-skip-offset reset targets so a late skip retry for an earlier frame never receives a later round's reset.
    private var resetBySkipOffset: [UInt64: UInt64] = [:]
    func setReset(for skipOffset: UInt64, to target: UInt64) { lock.withLock { resetBySkipOffset[skipOffset] = target } }
    /// How many incoming `skip` packets to ignore before replying with `reset` (simulates lost skip/reset datagrams).
    var dropSkips: Int { get { lock.withLock { ignoredSkips } } set { lock.withLock { ignoredSkips = newValue } } }
    private var ignoredSkips = 0
    /// How many incoming `use` packets to ignore before replying with `resumed` (simulates lost use/resumed datagrams).
    var dropUses: Int { get { lock.withLock { ignoredUses } } set { lock.withLock { ignoredUses = newValue } } }
    private var ignoredUses = 0

    init(tls: PipeEnd, udp: DatagramEnd) {
        self.tls = tls
        self.udp = udp
        let keys = ScreenUDPKeyMaterial(ExportingPipe.material(flowID, ScreenUDPWire.keyMaterialLength))!
        sealer = ScreenUDPSealer(cipher: TestAESGCM(keys.toPhoneKey), iv: keys.toPhoneIV, flow: flowID)
        opener = TestAESGCM(keys.toNodeKey)
        openIV = keys.toNodeIV
        Task { await tlsLoop() }
        Task { await udpLoop() }
    }

    private func tlsLoop() async {
        var parser = ScreenVideoParser()
        while let chunk = try? await tls.receive() {
            guard let raws = try? parser.push(chunk) else { return }
            for raw in raws {
                guard let m = try? ScreenLinkMessage.decode(type: raw.type, body: raw.body) else { return }
                lock.withLock { tlsSeen.append(m) }
                switch m {
                case let .ping(v): try? await say(.pong(v, retransmitted: 0, sent: 100))
                case .attach: try? await say(.attached)
                case let .use(entries): try? await say(.resumed(entries.map { ScreenLinkOffset($0.channel, 0) }))
                case .udpOpen:
                    let id = flowID.base64EncodedString().replacingOccurrences(of: "=", with: "")
                    try? await say(.udpReady(ScreenLinkUDPReady(port: 5000, flow: id)))
                default: break
                }
            }
        }
    }

    func say(_ m: ScreenLinkMessage) async throws { try await tls.send(m.framed()) }

    private func send(_ m: ScreenUDPMessage) {
        let p = lock.withLock { sealer.seal(m.encoded()) }
        if let p { udp.send(p) }
    }

    private func udpLoop() async {
        while let p = try? await udp.receive() {
            guard let head = ScreenUDPPacket.read(p), head.flow == flowID,
                  let body = ScreenUDPPacket.open(p, cipher: opener, iv: openIV, number: head.number) else { continue }
            let accepted = lock.withLock { () -> Bool in
                seenNumbers.append(head.number)
                let ok = window.accept(head.number)
                if !ok { replays += 1 }
                return ok
            }
            guard accepted, let m = try? ScreenUDPMessage.decode(body, fromNode: false) else { continue }
            lock.withLock { udpSeen.append(m) }
            if udpSilent { continue }
            switch m {
            case let .helloPhone(echo):
                send(echo == 77 ? .helloNode(challenge: 0, validated: true) : .helloNode(challenge: 77, validated: false))
            case let .use(entries):
                let reply = lock.withLock { () -> Bool in
                    if ignoredUses > 0 {
                        ignoredUses -= 1
                        return false
                    }
                    return true
                }
                if reply { send(.resumed(entries.map { ScreenLinkOffset($0.channel, 0) })) }
            case let .ping(micros, _, _):
                send(.pong(micros: micros, dataBytes: 0, parityBytes: 0, resentBytes: 0))
            case let .nack(ch, entries):
                for e in entries {
                    guard let (msg, droppable) = lock.withLock({ sentMessages[e.offset] }) else { continue }
                    for (h, frag) in Self.fragments(msg, ch: ch, at: e.offset, ratio: 0, droppable: droppable)
                    where h.block == e.block && h.index == e.index {
                        send(.data(h, fragment: frag))
                    }
                }
            case let .skip(ch, off):
                let replyOffset: UInt64? = lock.withLock {
                    if ignoredSkips > 0 {
                        ignoredSkips -= 1
                        return nil
                    }
                    return resetBySkipOffset[off] ?? resetOffset
                }
                if let replyOffset { send(.reset(channel: ch, offset: replyOffset)) }
            default:
                break
            }
        }
    }

    /// Sends a whole video message as the node does, with `ratio` parity; `lose` drops some of its packets on the way,
    /// and a message not kept can't be sent again (as one the node dropped).
    func video(_ msg: Data, ch: UInt8, at off: UInt64, ratio: Int = 0, droppable: Bool = true, lose: Set<Int> = [], keep: Bool = true) {
        if keep { lock.withLock { sentMessages[off] = (msg, droppable) } }
        for (i, f) in Self.fragments(msg, ch: ch, at: off, ratio: ratio, droppable: droppable).enumerated()
        where !lose.contains(i) {
            send(.data(f.0, fragment: f.1))
        }
    }

    static func fragments(_ msg: Data, ch: UInt8, at off: UInt64, ratio: Int, droppable: Bool) -> [(ScreenUDPFragmentHead, Data)] {
        let bytes = [UInt8](msg)
        var out: [(ScreenUDPFragmentHead, Data)] = []
        for (bi, b) in ScreenUDPLayout.blocks(length: bytes.count).enumerated() {
            let m = ScreenUDPLayout.parity(data: b.data, ratio: ratio)
            let shards = (0 ..< b.data).map { j -> [UInt8] in
                let from = (b.first + j) * ScreenUDPWire.fragment
                return Array(bytes[from ..< min(from + ScreenUDPWire.fragment, bytes.count)])
            }
            let parity = try! ReedSolomon.encode(shards.map { $0 + [UInt8](repeating: 0, count: b.shardLength - $0.count) }, parity: m)
            let head = { (i: Int) in
                ScreenUDPFragmentHead(channel: ch, offset: off, length: UInt32(bytes.count), block: UInt16(bi), index: UInt16(i),
                                      data: UInt8(b.data), parity: UInt8(m), flags: droppable ? ScreenUDPWire.droppable : 0)
            }
            out += shards.enumerated().map { (head($0.offset), Data($0.element)) }
            out += parity.enumerated().map { (head(b.data + $0.offset), Data($0.element)) }
        }
        return out
    }

    /// The node's reset on the flow: the stream goes on at `off`.
    func flowReset(_ ch: UInt8, at off: UInt64) { send(.reset(channel: ch, offset: off)) }

    func tlsMessages() -> [ScreenLinkMessage] { lock.withLock { tlsSeen } }
    func udpMessages() -> [ScreenUDPMessage] { lock.withLock { udpSeen } }
    func packetNumbers() -> [UInt64] { lock.withLock { seenNumbers } }
    func replayDropCount() -> Int { lock.withLock { replays } }
    func usedOnTLS(_ ch: UInt8) -> UInt64? {
        tlsMessages().reversed().lazy.compactMap { m -> UInt64? in
            if case let .use(e) = m { return e.first { $0.channel == ch }?.offset }
            return nil
        }.first
    }
    func usedOnUDP() -> Bool { udpMessages().contains { if case .use = $0 { true } else { false } } }
}

/// A test cipher wrapper that fails (`seal` returns nil) when the plaintext UDP message body matches `failKind`
/// starting at the `failFromMatch`-th occurrence (1-based), simulating `ScreenUDPSealer` hitting `packetLimit`
/// deterministically without needing 2^62 packets.
private final class FailingSealCipher: ScreenUDPCipher, @unchecked Sendable {
    private let base: TestAESGCM
    private let failKind: UInt8
    private let failFromMatch: Int
    private let matches: SealCounter

    final class SealCounter: @unchecked Sendable {
        private let lock = NSLock()
        private var n = 0
        func next() -> Int { lock.withLock { n += 1; return n } }
        var count: Int { lock.withLock { n } }
    }

    init(key: Data, failKind: UInt8, failFromMatch: Int, matches: SealCounter) {
        self.base = TestAESGCM(key)
        self.failKind = failKind
        self.failFromMatch = failFromMatch
        self.matches = matches
    }

    func seal(_ body: Data, nonce: Data, header: Data) -> Data? {
        if body.first == failKind, matches.next() >= failFromMatch { return nil }
        return base.seal(body, nonce: nonce, header: header)
    }

    func open(_ sealed: Data, nonce: Data, header: Data) -> Data? {
        base.open(sealed, nonce: nonce, header: header)
    }
}

final class ScreenLinkUDPTests: XCTestCase {
    private final class Collected: @unchecked Sendable {
        private let lock = NSLock()
        private var events: [ScreenLinkSession.Event] = []
        init(_ s: ScreenLinkSession) { Task { for await e in s.events { self.lock.withLock { self.events.append(e) } } } }
        func all() -> [ScreenLinkSession.Event] { lock.withLock { events } }
        func moves() -> [PathChange] { all().compactMap { if case let .moved(c) = $0 { c } else { nil } } }
        func openFlow() -> String? { all().lazy.compactMap { if case let .openFlow(r, _) = $0 { r } else { nil } }.first }
    }

    private func until(_ what: String, timeout: Double = 8, _ cond: () async -> Bool) async {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if await cond() { return }
            try? await Task.sleep(nanoseconds: 10_000_000)
        }
        XCTFail("timed out: \(what)")
    }

    private struct Rig {
        let s: ScreenLinkSession
        let events: Collected
        let route: UDPRouteNode
        let udpPhone: DatagramEnd
        let video: ScreenLinkChannelEnd
        let videoNode: FakeNode
    }

    /// A session whose video rides a direct route's UDP flow.
    private func rig(
        udp: Bool = true,
        now: @escaping @Sendable () -> Double = ScreenLinkSession.monotonic,
        setup: ScreenUDPSetup? = nil,
        configureRoute: ((UDPRouteNode) -> Void)? = nil,
        file: StaticString = #filePath,
        line: UInt = #line
    ) async throws -> Rig {
        let shared = AutoNode.SharedReceive()
        let (phoneR, nodeR) = PipeEnd.pair()
        _ = AutoNode(nodeR, delayMs: 30, shared: shared)
        let paths = ScreenLinkPaths(token: "tok", cert: "pin", port: 4000, candidates: [.init(kind: "lan", addr: "192.0.2.10", prefix: 24)], udp: 1)
        let s = ScreenLinkSession(rfbRelay: phoneR, first: ScreenLinkMessage.paths(paths).framed(), now: now)
        let events = Collected(s)
        if udp { await s.enableUDP(setup ?? ScreenUDPSetup { TestAESGCM($0) }) }
        let (phoneV, nodeV) = PipeEnd.pair()
        let vnode = FakeNode(nodeV)
        try await vnode.say(.resumed([ScreenLinkOffset(2, 0)]))
        let video = try await s.attachVideo(phoneV)
        let (phoneD, nodeD) = PipeEnd.pair()
        let (phoneU, nodeU) = DatagramEnd.pair()
        let route = UDPRouteNode(tls: nodeD, udp: nodeU)
        configureRoute?(route)
        await until("paths read") { events.all().contains { if case .paths = $0 { true } else { false } } }
        await s.addDirect(ExportingPipe(phoneD), route: "lan:a", kind: .lan, via: .wifi)
        if udp {
            await until("the session asked for a flow") { events.openFlow() == "lan:a" }
            await s.addFlow(phoneU, route: "lan:a")
        }
        return Rig(s: s, events: events, route: route, udpPhone: phoneU, video: video, videoNode: vnode)
    }

    /// What a channel end hands on, read in the background so a test waits for it with a deadline rather than forever.
    private final class Received: @unchecked Sendable {
        private let lock = NSLock()
        private var got = Data()
        init(_ end: ScreenLinkChannelEnd) {
            Task { while let d = try? await end.receive() { self.lock.withLock { self.got += d } } }
        }
        var data: Data { lock.withLock { got } }
    }

    private func acked(_ ms: [ScreenLinkMessage], _ off: UInt64) -> Bool {
        ms.contains { if case .ack(2, off) = $0 { true } else { false } }
    }

    private func acked(_ ms: [ScreenUDPMessage], _ off: UInt64) -> Bool {
        ms.contains { if case .ack(2, off) = $0 { true } else { false } }
    }

    private func frame(_ n: Int, size: Int) -> Data {
        ScreenVideoWire.frame(3, body: Data((0 ..< size).map { UInt8(truncatingIfNeeded: $0 + n) }))
    }

    /// The flows tick every 5 ms only while one carries the video (#9492). With nothing to carry (a page on its sign-in
    /// form, a route still being proved) the 5 ms clock woke the phone about 200 times a second.
    func testFlowsTickFastOnlyWhileCarryingTheVideo() {
        XCTAssertEqual(ScreenLinkSession.flowTickNanoseconds(carrying: true), 5_000_000)
        XCTAssertEqual(ScreenLinkSession.flowTickNanoseconds(carrying: false), 50_000_000)
        XCTAssertLessThan(Double(ScreenLinkSession.flowTickNanoseconds(carrying: false)) / 1e9, 0.2, "under the hellos' 0.2 s")
    }

    /// The video moves onto the flow once the route carries the session, comes whole and in order over it, and goes
    /// back to the route's TLS connection when the flow goes silent, from where the phone was.
    func testTheVideoRidesTheFlowAndFallsBackToTheRoute() async throws {
        let r = try await rig()
        await until("the video on the flow") { r.route.usedOnUDP() }
        await until("onto udp") { r.events.moves().contains { $0.to.transport == .udp && $0.reason == .faster } }
        var off: UInt64 = 0
        var sent = Data()
        for i in 0 ..< 6 {
            let m = frame(i, size: [200, 5000, 30000][i % 3])
            r.route.video(m, ch: 2, at: off)
            off += UInt64(m.count)
            sent += m
        }
        var got = Data()
        while got.count < sent.count { got += try await r.video.receive() }
        XCTAssertEqual(got, sent)
        await until("the phone acknowledged it all") {
            r.route.udpMessages().contains { if case let .ack(2, o) = $0 { o == off } else { false } }
        }

        r.route.udpSilent = true // the flow's packets vanish
        await until("back on the route's TLS connection", timeout: 5) { r.route.usedOnTLS(2) == off }
        await until("the move says lost") { r.events.moves().contains { $0.from?.transport == .udp && $0.to.transport == .tcp } }
        let next = frame(9, size: 800)
        try await r.route.say(.data(channel: 2, offset: off, bytes: next))
        let after = try await r.video.receive()
        XCTAssertEqual(after, next)
    }

    /// Muting flows (the DEBUG command channel's `screenFlow mute`, as a network dropping UDP) sends the video back to
    /// the route's TLS connection.
    func testMutedFlowsFallBackToTheRoute() async throws {
        let r = try await rig()
        await until("the video on the flow") { r.route.usedOnUDP() }
        await r.s.muteFlows(true)
        await until("back on the route's TLS connection", timeout: 5) { r.route.usedOnTLS(2) != nil }
        await until("the move says so") { r.events.moves().contains { $0.from?.transport == .udp && $0.to.transport == .tcp } }
    }

    /// Packets lost on the flow are rebuilt from parity or asked for again; the video still comes whole and in order.
    func testLossOnTheFlowIsRebuiltOrAskedFor() async throws {
        let r = try await rig()
        await until("the video on the flow") { r.route.usedOnUDP() }
        try await Task.sleep(nanoseconds: 50_000_000)
        var off: UInt64 = 0
        var sent = Data()
        for i in 0 ..< 8 {
            let m = frame(i, size: 12000) // 11 shards
            r.route.video(m, ch: 2, at: off, ratio: i < 4 ? 2000 : 0, lose: [1, 5]) // with parity, then without
            off += UInt64(m.count)
            sent += m
        }
        var got = Data()
        while got.count < sent.count { got += try await r.video.receive() }
        XCTAssertEqual(got, sent)
        let nacks = r.route.udpMessages().filter { if case .nack = $0 { true } else { false } }.count
        XCTAssertGreaterThan(nacks, 0, "the messages without parity were asked for")
    }

    /// A frame that holds the picture back too long is given up; the node's reset says where the stream goes on.
    func testAGivenUpFrameGoesOnAtTheReset() async throws {
        let r = try await rig()
        await until("the video on the flow") { r.route.usedOnUDP() }
        try await Task.sleep(nanoseconds: 50_000_000)
        let a = frame(1, size: 400), lost = frame(2, size: 5000), b = frame(3, size: 400)
        r.route.video(a, ch: 2, at: 0)
        let first = try await r.video.receive()
        XCTAssertEqual(first, a)
        let lostAt = UInt64(a.count)
        // One shard of it comes (the phone knows it is a frame); the rest never does: the node has dropped it, so the
        // NACKs find nothing, and a later frame shows the gap.
        r.route.video(lost, ch: 2, at: lostAt, lose: Set(0 ..< 4), keep: false)
        r.route.resetTo = lostAt + UInt64(lost.count) + 1000
        r.route.video(b, ch: 2, at: lostAt + UInt64(lost.count))
        await until("given up") { r.route.udpMessages().contains { if case .skip(2, lostAt) = $0 { true } else { false } } }
        let c = frame(4, size: 600)
        r.route.video(c, ch: 2, at: r.route.resetTo)
        let next = try await r.video.receive()
        XCTAssertEqual(next, c, "the stream goes on at the reset")
    }

    /// Retrying `skip` for the same single given-up frame while waiting for `reset` must not count as additional
    /// given-up frames and prematurely tear down the UDP flow as degraded.
    func testSkipRetriesForASingleFrameDoNotLeaveTheFlowAsDegraded() async throws {
        let r = try await rig()
        await until("the video on the flow") { r.route.usedOnUDP() }
        let got = Received(r.video)
        let a = frame(1, size: 400), lost = frame(2, size: 5000), b = frame(3, size: 400)
        r.route.video(a, ch: 2, at: 0)
        await until("first frame delivered", timeout: 3) { got.data == a }
        let lostAt = UInt64(a.count)
        // Drop more skip datagrams than flowGiveUps (2) so the receiver must resend skip for the same frame.
        r.route.dropSkips = ScreenLinkSession.flowGiveUps + 1
        let resetAt = lostAt + UInt64(lost.count) + 1000
        r.route.setReset(for: lostAt, to: resetAt)
        r.route.video(lost, ch: 2, at: lostAt, lose: Set(0 ..< 4), keep: false)
        r.route.video(b, ch: 2, at: lostAt + UInt64(lost.count))
        await until("skip retried past flowGiveUps without leaving UDP", timeout: 3) {
            let skips = r.route.udpMessages().filter { if case .skip(2, lostAt) = $0 { true } else { false } }.count
            return skips >= ScreenLinkSession.flowGiveUps + 2
        }
        let c = frame(4, size: 600)
        r.route.video(c, ch: 2, at: resetAt)
        await until("the stream recovers on UDP once reset arrives", timeout: 3) { got.data == a + c }
        XCTAssertFalse(r.events.moves().contains { $0.reason == .degraded }, "skip retries for one frame must not degrade the flow")
        XCTAssertFalse(r.events.moves().contains { $0.from?.transport == .udp && $0.to.transport == .tcp }, "the video stays on UDP instead of falling back to TLS")
    }

    /// More than `flowGiveUps` distinct given-up frames within `flowGiveUpWindow` still leave the UDP flow as degraded.
    func testDistinctGivenUpFramesOverThresholdLeaveTheFlowAsDegraded() async throws {
        let r = try await rig()
        await until("the video on the flow") { r.route.usedOnUDP() }
        let got = Received(r.video)
        var expected = Data()
        var off: UInt64 = 0
        for i in 0 ..< (ScreenLinkSession.flowGiveUps + 1) {
            let lost = frame(i * 3 + 1, size: 5000), later = frame(i * 3 + 2, size: 400)
            let lostAt = off
            let nextOff = lostAt + UInt64(lost.count) + 1000
            r.route.setReset(for: lostAt, to: nextOff)
            r.route.video(lost, ch: 2, at: lostAt, lose: Set(0 ..< 4), keep: false)
            r.route.video(later, ch: 2, at: lostAt + UInt64(lost.count))
            if i < ScreenLinkSession.flowGiveUps {
                await until("skip \(i + 1) sent", timeout: 3) {
                    r.route.udpMessages().contains { if case .skip(2, lostAt) = $0 { true } else { false } }
                }
                let after = frame(i * 3 + 3, size: 400)
                r.route.video(after, ch: 2, at: nextOff)
                expected += after
                await until("frame after reset \(i + 1)", timeout: 3) { got.data == expected }
                off = nextOff + UInt64(after.count)
            }
        }
        await until("flow left as degraded", timeout: 3) {
            r.events.moves().contains { $0.from?.transport == .udp && $0.to.transport == .tcp && $0.reason == .degraded }
        }
        XCTAssertGreaterThan(off, 0)
        await until("back on TLS after degraded fallback", timeout: 3) { r.route.usedOnTLS(2) == off }
    }

    /// Retrying `use` in `flowTick` must keep the advanced sealer packet number instead of overwriting `flows[route]`
    /// with a stale struct copy (which rolled back `sealer.next`, reused AES-GCM nonces, and triggered replay drops).
    func testUseRetryDoesNotReusePacketNumbersOrTriggerReplayDrops() async throws {
        let r = try await rig(configureRoute: { $0.dropUses = 2 })
        await until("third use accepted by node without replay drops", timeout: 3) {
            r.route.udpMessages().filter { if case .use = $0 { true } else { false } }.count >= 3
        }
        let got = Received(r.video)
        let a = frame(1, size: 400)
        r.route.video(a, ch: 2, at: 0)
        await until("video frame delivered on UDP after use retries", timeout: 3) { got.data == a }
        let nums = r.route.packetNumbers()
        XCTAssertEqual(nums.count, Set(nums).count, "every UDP packet must have a unique packet number (no nonce reuse): \(nums)")
        XCTAssertEqual(r.route.replayDropCount(), 0, "node replay window must not drop retried UDP packets")
        XCTAssertFalse(r.events.moves().contains { $0.from?.transport == .udp && $0.to.transport == .tcp },
                       "the video must stay on UDP once resumed arrives")
    }

    /// If sealing fails during `startCarrying`'s initial `use`, `flowTick` must not force-unwrap the removed flow
    /// and `startCarrying` must not emit a spurious `.tcp -> .udp` `.faster` move after `leaveFlow` already ran.
    func testSealerFailureDuringStartCarryingLeavesFlowWithoutCrashingOrEmittingFaster() async throws {
        let useSeals = FailingSealCipher.SealCounter()
        let setup = ScreenUDPSetup { key in
            FailingSealCipher(key: key, failKind: ScreenUDPWire.Kind.use.rawValue, failFromMatch: 1, matches: useSeals)
        }
        let r = try await rig(setup: setup)
        await until("flow left as lost on sealer failure", timeout: 3) {
            r.events.moves().contains { $0.from?.transport == .udp && $0.to.transport == .tcp && $0.reason == .lost }
        }
        XCTAssertFalse(r.events.moves().contains { $0.to.transport == .udp && $0.reason == .faster },
                       "startCarrying must not emit a .faster move after sendFlow left the flow")
    }

    /// If sealing fails during a `use` retry in `flowTick`, `flowTick` must not resurrect the removed flow with a
    /// stale struct write back to `flows[route]`.
    func testSealerFailureDuringUseRetryDoesNotResurrectRemovedFlow() async throws {
        let useSeals = FailingSealCipher.SealCounter()
        let setup = ScreenUDPSetup { key in
            FailingSealCipher(key: key, failKind: ScreenUDPWire.Kind.use.rawValue, failFromMatch: 2, matches: useSeals)
        }
        let r = try await rig(setup: setup, configureRoute: { $0.dropUses = 5 })
        await until("flow left as lost on use retry sealer failure", timeout: 3) {
            r.events.moves().contains { $0.from?.transport == .udp && $0.to.transport == .tcp && $0.reason == .lost }
        }
        let got = Received(r.video)
        let a = frame(1, size: 400)
        try await r.route.say(.data(channel: 2, offset: 0, bytes: a))
        await until("video continues over TLS after flow leaves", timeout: 3) { got.data == a }
        let lostMoves = r.events.moves().filter { $0.from?.transport == .udp && $0.to.transport == .tcp && $0.reason == .lost }
        XCTAssertEqual(lostMoves.count, 1, "removed flow must not be resurrected to leave a second time")
        XCTAssertEqual(useSeals.count, 2, "no further use retries should be attempted on a removed flow")
    }

    /// If sealing fails during `pingFlow` inside `flowTick`, `flowTick` must not force-unwrap the removed flow.
    func testSealerFailureDuringPeriodicPingLeavesFlowWithoutCrashing() async throws {
        final class Clock: @unchecked Sendable {
            private let lock = NSLock()
            private var t = 100.0
            func now() -> Double { lock.withLock { t } }
            func advance(_ dt: Double) { lock.withLock { t += dt } }
        }
        let clock = Clock()
        let pingSeals = FailingSealCipher.SealCounter()
        let setup = ScreenUDPSetup { key in
            FailingSealCipher(key: key, failKind: ScreenUDPWire.Kind.ping.rawValue, failFromMatch: 2, matches: pingSeals)
        }
        let r = try await rig(udp: false, now: { clock.now() })
        // Ensure the direct route already carries video before opening the UDP flow; gently advance the virtual clock
        // if an early tick recorded `lastLook` before `"lan:a"` attached under CI load.
        await until("video on direct route", timeout: 3) {
            if r.route.usedOnTLS(2) != nil { return true }
            clock.advance(0.02)
            return false
        }
        await r.s.enableUDP(setup)
        await until("the session asked for a flow", timeout: 3) { r.events.openFlow() == "lan:a" }
        await r.s.addFlow(r.udpPhone, route: "lan:a")
        await until("initial UDP ping answered and video carrying", timeout: 3) { r.route.usedOnUDP() }
        // Advance past the 1 s periodic ping interval so flowTick invokes pingFlow(route) and hits sealer failure.
        clock.advance(1.1)
        await until("flow left as lost on periodic ping sealer failure", timeout: 3) {
            r.events.moves().contains { $0.from?.transport == .udp && $0.to.transport == .tcp && $0.reason == .lost }
        }
        XCTAssertEqual(pingSeals.count, 2, "flow must leave on the second (periodic) ping seal failure")
    }

    /// #10005 over TCP: a reset the node owes again goes out on the new route, and its copy on the route the channel
    /// left can come later, in the middle of the keyframe that follows it. That copy is at or behind the receiver and
    /// changes nothing: the keyframe completes. Between whole messages it is as before, and a reset ahead of the receiver
    /// still drops a partial message.
    func testALateResetFromTheOldRouteKeepsAHalfReceivedMessage() async throws {
        let r = try await rig(udp: false)
        await until("the video on the direct route") { r.route.usedOnTLS(2) != nil }
        let got = Received(r.video)
        let jump: UInt64 = 1000
        let key = frame(1, size: 3000), half = 1200
        // The new route: the reset, then the keyframe's first bytes.
        try await r.route.say(.reset(channel: 2, offset: jump))
        try await r.route.say(.data(channel: 2, offset: jump, bytes: key.prefix(half)))
        await until("the first part taken") { self.acked(r.route.tlsMessages(), jump + UInt64(half)) }
        // The old route's copy of the same reset, late.
        try await r.videoNode.say(.reset(channel: 2, offset: jump))
        let late = try await relayAck(r.videoNode)
        XCTAssertEqual(late, jump + UInt64(half), "the late copy moves nothing")
        try await r.route.say(.data(channel: 2, offset: jump + UInt64(half), bytes: key.dropFirst(half)))
        await until("the keyframe whole", timeout: 3) { got.data == key }

        // A late copy between whole messages, as before.
        var off = jump + UInt64(key.count)
        try await r.videoNode.say(.reset(channel: 2, offset: jump))
        let between = try await relayAck(r.videoNode)
        XCTAssertEqual(between, off)
        let next = frame(2, size: 400)
        try await r.route.say(.data(channel: 2, offset: off, bytes: next))
        await until("the next frame", timeout: 3) { got.data == key + next }

        // A reset ahead of the receiver: the node dropped the rest of a message, and the part the phone has goes too.
        off += UInt64(next.count)
        try await r.route.say(.data(channel: 2, offset: off, bytes: frame(3, size: 2000).prefix(500)))
        await until("the partial taken") { self.acked(r.route.tlsMessages(), off + 500) }
        let ahead = off + 5000, after = frame(4, size: 600)
        try await r.route.say(.reset(channel: 2, offset: ahead))
        try await r.route.say(.data(channel: 2, offset: ahead, bytes: after))
        await until("the frame after the reset, whole", timeout: 3) { got.data == key + next + after }
    }

    /// The phone's next acknowledgement of the video on a route the test node doesn't otherwise read.
    private func relayAck(_ n: FakeNode) async throws -> UInt64 {
        while true {
            if case let .ack(2, o) = try await n.next(skippingAcks: false) { return o }
        }
    }

    /// #10005 on a UDP flow: a reset at or behind the receiver leaves a message the phone has in part, and one ahead of
    /// it drops it. A node sends a flow whole messages, so a late reset finds no partial one in the assembler there; the
    /// test node sends the keyframe in two parts to put its first part where a TCP route leaves it.
    func testALateResetOnTheFlowKeepsAHalfReceivedMessage() async throws {
        let r = try await rig()
        await until("the video on the flow") { r.route.usedOnUDP() }
        try await Task.sleep(nanoseconds: 50_000_000)
        let got = Received(r.video)
        let jump: UInt64 = 1000
        let key = frame(1, size: 3000), half = 1200
        r.route.flowReset(2, at: jump)
        r.route.video(key.prefix(half), ch: 2, at: jump)
        await until("the first part taken") { self.acked(r.route.udpMessages(), jump + UInt64(half)) }
        r.route.flowReset(2, at: jump) // the late copy
        r.route.video(key.dropFirst(half), ch: 2, at: jump + UInt64(half))
        await until("the keyframe whole", timeout: 3) { got.data == key }

        // A late copy between whole messages, as before.
        var off = jump + UInt64(key.count)
        r.route.flowReset(2, at: jump)
        let next = frame(2, size: 400)
        r.route.video(next, ch: 2, at: off)
        await until("the next frame", timeout: 3) { got.data == key + next }

        // A reset ahead of the receiver drops the part the phone has.
        off += UInt64(next.count)
        r.route.video(frame(3, size: 2000).prefix(500), ch: 2, at: off)
        await until("the partial taken") { self.acked(r.route.udpMessages(), off + 500) }
        let ahead = off + 5000, after = frame(4, size: 600)
        r.route.flowReset(2, at: ahead)
        r.route.video(after, ch: 2, at: ahead)
        await until("the frame after the reset, whole", timeout: 3) { got.data == key + next + after }
        XCTAssertTrue(r.events.moves().allSatisfy { $0.reason != .lost }, "the flow was never left")
    }

    /// On cellular (the route's interface, or the phone's path under Tailscale) no flow is asked for; back on Wi-Fi one is.
    func testNoFlowOnCellular() async throws {
        let shared = AutoNode.SharedReceive()
        let (phoneR, nodeR) = PipeEnd.pair()
        _ = AutoNode(nodeR, delayMs: 30, shared: shared)
        let paths = ScreenLinkPaths(token: "tok", cert: "pin", port: 4000, candidates: [.init(kind: "tailscale", addr: "100.64.0.9")], udp: 1)
        let s = ScreenLinkSession(rfbRelay: phoneR, first: ScreenLinkMessage.paths(paths).framed())
        let events = Collected(s)
        await s.enableUDP(ScreenUDPSetup { TestAESGCM($0) })
        await s.setRelayVia(.cellular)
        let (phoneD, nodeD) = PipeEnd.pair()
        let (_, nodeU) = DatagramEnd.pair()
        let route = UDPRouteNode(tls: nodeD, udp: nodeU)
        await until("paths read") { events.all().contains { if case .paths = $0 { true } else { false } } }
        await s.addDirect(ExportingPipe(phoneD), route: "tailscale:a", kind: .tailscale, via: .other)
        try await Task.sleep(nanoseconds: 1_000_000_000)
        XCTAssertFalse(route.tlsMessages().contains { if case .udpOpen = $0 { true } else { false } }, "asked for a flow on cellular")
        await s.setRelayVia(.wifi)
        await until("asked for a flow back on Wi-Fi") { route.tlsMessages().contains { if case .udpOpen = $0 { true } else { false } } }
    }

    /// Without the app turning UDP on, no flow is asked for and the route carries the video as before.
    func testWithoutUDPNoFlowIsAskedFor() async throws {
        let r = try await rig(udp: false)
        try await Task.sleep(nanoseconds: 1_500_000_000)
        XCTAssertFalse(r.route.tlsMessages().contains { if case .udpOpen = $0 { true } else { false } })
        XCTAssertNil(r.events.openFlow())
    }
}
