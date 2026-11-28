import Foundation

// The phone's end of a screen session (#6398, docs/extensions/computer-use.md "Live screen routes (#6398)"): its
// channels (RFB, then a video stream) over its transports (the hub's relays, direct connections), the pings that
// measure each route, and the route policy. The app gives it transports and takes channel ends that look like a byte
// stream, so the RFB client and the video stream don't know a session is there.
//
// The channels move to the route the policy picks: `use` on the new transport with what the phone has, and the node
// answers `resumed` with what it has, so each side sends on from there. A direct route that fails puts the channels
// back on their relays at once; a relay that fails while a direct route carries the session is opened again by the app
// (`reopen`), so the hub keeps the last word over the session. A relay that left on an interface the phone no longer
// uses, or went quiet, is opened again beside it and replaced once the new one attaches; a channel whose relay ended
// with nothing else to carry it waits a few seconds for that (#8709).

/// A transport of a screen session as the session needs it: the hub's relays and direct connections alike.
public protocol ScreenLinkTransport: AnyObject, Sendable {
    func send(_ data: Data) async throws
    /// The next bytes that came; throws once closed.
    func receive() async throws -> Data
    func close()
}

/// A UDP flow's socket (#7873): whole datagrams to and from the node's address on the route's interface.
public protocol ScreenUDPSocket: AnyObject, Sendable {
    func send(_ packet: Data)
    /// The next datagram; throws once closed.
    func receive() async throws -> Data
    func close()
}

/// A direct connection whose TLS connection exports keys (RFC 8446): a UDP flow's keys come from it (#7873).
public protocol ScreenLinkKeyExporter: AnyObject {
    func exportKeyingMaterial(label: String, context: Data, length: Int) -> Data?
}

/// What the app gives to turn the video over UDP on: the AEAD for a key (CryptoKit's AES-256-GCM).
public struct ScreenUDPSetup: Sendable {
    public var cipher: @Sendable (Data) -> any ScreenUDPCipher

    public init(cipher: @escaping @Sendable (Data) -> any ScreenUDPCipher) { self.cipher = cipher }
}

/// The UDP flow carrying the video, about once a second (#7873): for the mobile data budget and Connection Stats.
public struct ScreenUDPStatus: Equatable, Sendable {
    public var route: String
    /// The node's packets lost before recovery, lately.
    public var loss: Double?
    /// The parity asked for, ten-thousandths per data shard.
    public var parity: Int
    /// Parity and resent bytes per byte of video, lately: what the budget must leave room for.
    public var overhead: Double
    public var rttMs: Double?
    /// The share of its cap the video may use while the flow loses packets to congestion (`ScreenUDPCongestion`).
    public var scale: Double = 1
}

public enum ScreenLinkError: Error, Equatable {
    /// The channel or the session ended.
    case closed
    /// The relay's first message wasn't what a session starts with.
    case notASession
}

public actor ScreenLinkSession {
    /// What the app hears about: the node's routes, and about once a second each route's numbers and the policy's
    /// decision.
    public enum Event: Sendable {
        case paths(ScreenLinkPaths)
        case routes([String: LinkStats], current: String, decision: PathPolicy.Decision)
        /// A direct route's connection ended (it failed, or the app dropped it): dial it again later.
        case lost(String)
        /// The channels moved to another route.
        case moved(PathChange)
        /// A channel's relay (RFB, or the video channel) dropped, is on an interface the phone left, or went quiet:
        /// open it again with attach and hand it over with `reattach`.
        case reopen(video: Bool)
        /// A direct route's node opened a UDP flow (#7873): open a socket to the route's address on `port`, on the
        /// route's interface, and hand it over with `addFlow`.
        case openFlow(route: String, port: Int)
        /// The flow carrying the video, about once a second; nil when none does.
        case flow(ScreenUDPStatus?)
        case ended
    }

    /// The route id of the hub's relay in `PathPolicy` and in `Event.routes`.
    public static let relayRoute = "relay"

    public nonisolated let events: AsyncStream<Event>
    private let emit: AsyncStream<Event>.Continuation
    private let now: @Sendable () -> Double

    private struct Link {
        let transport: ScreenLinkTransport
        let writer: AsyncStream<Data>.Continuation
        var relayFor: UInt8? // a relay: the channel the hub opened it for (0: a video relay not named yet)
        var route: String? // a direct one: its route
        var kind: LinkStats.Kind
        var via: LinkStats.Via
        var attached: Bool
        var meter = LinkMeter()
        var lastPing = -Double.infinity
        /// When the oldest ping since anything was last heard on it went out; nil while it answers.
        var unanswered: Double?
        /// Pings sent since anything was last heard on it.
        var unansweredPings = 0
        /// A relay to replace (#8709): it left on an interface the phone no longer uses, or it went silent.
        var stale: Stale?
        /// When it was added (a direct connection or reattaching relay whose attach goes unanswered by `attachWithin`
        /// is lost, #9218).
        var added = -Double.infinity
    }

    /// Why a relay is being replaced: a silent one that speaks again is kept; one on the old interface is not.
    private enum Stale { case network, silent }

    /// A direct route with a ping out this long and nothing heard is lost (the design's "1.5 s silent").
    public static let silentAfter = 1.5
    /// A direct connection or reattaching relay whose attach isn't answered this long after it was added is lost
    /// (#9218). Unattached links are never pinged, so without this a direct redial or reopened relay whose answer went
    /// missing stayed open indefinitely.
    public static let attachWithin = 3.0
    /// A relay is taken as stuck when its oldest unanswered ping is this old, or four of its round trips if longer, and
    /// at least two pings went unanswered (#8709). It is longer than a direct route's limit because a relay's round
    /// trip goes through the hub and a cellular one jitters. A stuck relay is replaced, not dropped: the channel
    /// stays on it until the new one attaches.
    public static let relaySilentAfter = 3.0
    /// While a channel needs a relay opened again, the app is asked again this often.
    public static let reopenEvery = 3.0
    /// A channel left with nothing to carry it waits this long for a relay opened again before it ends. The node
    /// waits 10 s (`Grace`), so a relay that attaches in time finds the session still there.
    public static let relayGrace = 8.0

    private struct Chan {
        let id: UInt8
        var receiver = LinkReceiver()
        var sender: LinkSender
        var active: Int
        var assembler: VideoMessageAssembler?
        var inbox: [Data] = []
        var readers: [CheckedContinuation<Data, Error>] = []
        var roomWaiters: [CheckedContinuation<Void, Error>] = []
        var closeAt: UInt64?
        var ended = false
        /// The relay the hub opened for it, while it is up.
        var home: Int?
        /// `use` went out on `active` and its `resumed` hasn't come: nothing more is sent until it says where the node is.
        var waitingResume = false
        /// The route whose UDP flow carries what the node sends on this channel (#7873); what comes for it on other
        /// transports meanwhile is ignored. What the phone sends still goes on `active`.
        var flow: String?
        /// When the app was last asked to open its relay again (#8709).
        var reopenAskedAt: Double?
        /// Since when nothing carries it: it waits up to `relayGrace` for a relay opened again.
        var orphanedSince: Double?
    }

    private var links: [Int: Link] = [:]
    private var nextLink = 0
    private var chans: [UInt8: Chan] = [:]
    private var paths: ScreenLinkPaths?
    private var relayVia: LinkStats.Via = .wifi
    /// Whether the app has said which interface the relays leave on (until then `relayVia` is a guess).
    private var viaKnown = false
    private var policy = PathPolicy(relay: ScreenLinkSession.relayRoute)
    private var lost: Set<String> = []
    private var networkChanged = false
    private var pendingVideo: [Int: CheckedContinuation<UInt8, Error>] = [:]
    private var muted: Set<LinkStats.Kind> = []
    private var ended = false
    private var ticker: Task<Void, Never>?

    // The video over UDP (#7873).
    private var udp: ScreenUDPSetup?
    private struct Flow {
        let route: String
        let parent: Int
        let kind: LinkStats.Kind
        let id: Data
        var socket: ScreenUDPSocket?
        var sealer: ScreenUDPSealer
        let opener: any ScreenUDPCipher
        let openIV: Data
        var window = ScreenUDPReplayWindow()
        let opened: Double
        var proved = false
        var challenge: UInt64 = 0
        var lastHello = -Double.infinity
        var meter = LinkMeter()
        var loss = ScreenUDPLoss()
        var lastPing = -Double.infinity
        var unanswered: Double?
        var carrying: UInt8?
        var receiver: ScreenUDPReceiver?
        var waitingResume = false
        var useAt = 0.0
        var lastUse = -Double.infinity
        var giveUps: [Double] = []
        var recentBlocks: [Int] = []
        var parity = 0
        var counters: [(at: Double, data: UInt64, parity: UInt64, resent: UInt64)] = []
        var pace = ScreenUDPPace()
        var congestion = ScreenUDPCongestion()
    }
    private var flows: [String: Flow] = [:]
    /// When each route may ask for a flow again after one failed, and how long the next wait is.
    private var flowRetry: [String: (at: Double, wait: Double)] = [:]
    private var flowAsked: [Int: Double] = [:]
    private var flowTicker: Task<Void, Never>?
    private var lastFlowStatus = -Double.infinity
    /// For tests on real networks: everything that arrives on UDP flows is ignored (`muteFlows`).
    private var flowsMuted = false

    /// A session over the RFB relay the hub selected the subprotocol on. `first` is what the relay already said (the
    /// app read it to see the subprotocol); `now` is a monotonic clock in seconds.
    public init(rfbRelay: ScreenLinkTransport, first: Data = Data(), now: @escaping @Sendable () -> Double = ScreenLinkSession.monotonic) {
        (events, emit) = AsyncStream.makeStream(of: Event.self)
        self.now = now
        chans[ScreenLinkWire.rfb] = Chan(id: ScreenLinkWire.rfb, sender: LinkSender(window: 256 << 10), active: 0, home: 0)
        Task { await self.start(rfbRelay, first: first) }
    }

    public static let monotonic: @Sendable () -> Double = {
        Double(DispatchTime.now().uptimeNanoseconds) / 1e9
    }

    private func start(_ t: ScreenLinkTransport, first: Data) {
        _ = register(t, relayFor: ScreenLinkWire.rfb, kind: .relay, via: relayVia, first: first)
        ticker = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(nanoseconds: 250_000_000)
                guard let self, await self.tick() else { return }
            }
        }
    }

    /// A transport joins: its reader, and a writer that sends its messages in order. Ids start at 0 (the RFB relay).
    private func register(_ t: ScreenLinkTransport, relayFor: UInt8?, route: String? = nil, kind: LinkStats.Kind,
                          via: LinkStats.Via, first: Data = Data(), attached: Bool? = nil) -> Int {
        let id = nextLink
        nextLink += 1
        let (stream, writer) = AsyncStream.makeStream(of: Data.self)
        links[id] = Link(transport: t, writer: writer, relayFor: relayFor, route: route, kind: kind, via: via,
                         attached: attached ?? (relayFor != nil), added: now())
        Task { [weak self] in
            for await frame in stream {
                do { try await t.send(frame) } catch { await self?.lose(id); return }
            }
        }
        read(id, t, first: first)
        // Bytes the app sent before this transport was there (the RFB relay registers on the session's first turn)
        // go out now.
        for (ch, c) in chans where c.active == id { pump(ch) }
        return id
    }

    // MARK: Channel ends

    /// The RFB channel as a byte stream.
    public nonisolated func rfb() -> ScreenLinkChannelEnd { ScreenLinkChannelEnd(session: self, channel: ScreenLinkWire.rfb) }

    /// A new video stream's relay (the hub selected the subprotocol on …/screen/video/connect): its first message names
    /// the video channel; the channel as a byte stream of whole video messages.
    public func attachVideo(_ relay: ScreenLinkTransport, first: Data = Data()) async throws -> ScreenLinkChannelEnd {
        guard !ended else { throw ScreenLinkError.closed }
        let id = register(relay, relayFor: 0, kind: .relay, via: relayVia, first: first)
        let ch = try await withCheckedThrowingContinuation { pendingVideo[id] = $0 }
        return ScreenLinkChannelEnd(session: self, channel: ch)
    }

    func receive(_ ch: UInt8) async throws -> Data {
        guard var c = chans[ch] else { throw ScreenLinkError.closed }
        if !c.inbox.isEmpty {
            let d = c.inbox.removeFirst()
            chans[ch] = c
            return d
        }
        if c.ended || closeReached(c) {
            if !c.ended {
                chans[ch]?.ended = true
                if ch == ScreenLinkWire.rfb { end() }
            }
            throw ScreenLinkError.closed
        }
        return try await withCheckedThrowingContinuation { k in
            chans[ch]?.readers.append(k)
        }
    }

    func send(_ ch: UInt8, _ data: Data) async throws {
        while let c = chans[ch], !c.ended, !c.sender.hasRoom {
            try await withCheckedThrowingContinuation { (k: CheckedContinuation<Void, Error>) in chans[ch]?.roomWaiters.append(k) }
        }
        guard var c = chans[ch], !c.ended else { throw ScreenLinkError.closed }
        c.sender.append(data, at: now())
        chans[ch] = c
        pump(ch)
    }

    func close(_ ch: UInt8) {
        guard var c = chans[ch], !c.ended else { return }
        out(c.active, .close(channel: ch, offset: c.sender.end))
        c.ended = true
        chans[ch] = c
        wake(ch, error: ScreenLinkError.closed)
        if ch == ScreenLinkWire.rfb { end() }
    }

    // MARK: Routes

    /// The phone's interface the relay leaves on, as the app's path monitor sees it.
    public func setRelayVia(_ via: LinkStats.Via) {
        if via != relayVia {
            networkChanged = true
            // The phone's networks changed: flows leave now and may be opened again at once.
            for route in Array(flows.keys) { leaveFlow(route, .networkChanged, retry: false) }
            flowRetry = [:]
        }
        relayVia = via
        let first = !viaKnown
        viaKnown = true
        for (id, l) in links where l.relayFor != nil {
            if first {
                links[id]?.via = via // registered before the app said: the guess becomes what it said
            } else if l.via != via, l.stale == nil {
                // Left on an interface the phone no longer uses (#8709): Wi-Fi went away under it, and a socket on a
                // vanished interface often says nothing. Each channel it serves gets a relay on this one.
                links[id]?.stale = .network
            }
        }
        askReopenForStaleRelays()
    }

    /// A direct connection to one of the node's candidates, for route `route` over the phone's `via` interface: it
    /// attaches with the session's token, then is pinged and measured.
    public func addDirect(_ t: ScreenLinkTransport, route: String, kind: LinkStats.Kind, via: LinkStats.Via) {
        guard !ended, let token = paths?.token else { t.close(); return }
        let id = register(t, relayFor: nil, route: route, kind: kind, via: via)
        out(id, .attach(token: token))
    }

    /// For tests on real networks (the DEBUG command channel): direct routes of `kind` go silent (everything that
    /// arrives on them is ignored, as when a network drops packets without closing anything), or speak again.
    public func muteDirect(kind: LinkStats.Kind, _ on: Bool) {
        if on { muted.insert(kind) } else { muted.remove(kind) }
    }

    /// For tests: direct routes of `kind` close now, as when the connection breaks.
    public func dropDirect(kind: LinkStats.Kind) {
        for (id, l) in links where l.route != nil && l.kind == kind { lose(id) }
    }

    /// The routes the app no longer has (their interface went away), so the policy treats them as lost now.
    public func dropDirect(route: String, networkChanged changed: Bool) {
        networkChanged = networkChanged || changed
        for (id, l) in links where l.route == route { lose(id) }
    }

    public var currentRoute: String { policy.current }

    /// A relay opened again after `reopen` (with attach on the hub's route): it sends the session's token first and,
    /// once attached, is the channel's relay again.
    public func reattach(_ relay: ScreenLinkTransport, video: Bool) {
        let ch = video ? currentVideo : ScreenLinkWire.rfb
        guard !ended, let token = paths?.token, let ch, chans[ch].map({ !$0.ended }) == true else {
            relay.close()
            return
        }
        let id = register(relay, relayFor: ch, kind: .relay, via: relayVia, attached: false)
        out(id, .attach(token: token))
    }

    /// The user turned direct routes off: the channels go back to their relays and the direct connections close.
    public func directOff() {
        for route in Array(flows.keys) { closeFlow(route) }
        if case let .move(to, reason) = policy.directOff(now: now()) { move(to: to, reason: reason, routes: [:]) }
        for (id, l) in links where l.route != nil { lose(id) }
    }

    /// The live video channel, if any.
    private var currentVideo: UInt8? { chans.keys.filter { $0 >= ScreenLinkWire.firstVideo && chans[$0]?.ended == false }.max() }

    // MARK: Transports

    private func read(_ id: Int, _ t: ScreenLinkTransport, first: Data) {
        Task { [weak self] in
            var parser = ScreenVideoParser()
            var chunk = first
            do {
                while true {
                    if chunk.isEmpty { chunk = try await t.receive() }
                    let raws = try parser.push(chunk)
                    chunk = Data()
                    for raw in raws {
                        guard let self else { return }
                        await self.handle(try ScreenLinkMessage.decode(type: raw.type, body: raw.body), from: id)
                    }
                }
            } catch {
                await self?.lose(id)
            }
        }
    }

    private func out(_ id: Int, _ m: ScreenLinkMessage) {
        links[id]?.writer.yield(m.framed())
    }

    private func pump(_ ch: UInt8) {
        guard var c = chans[ch], links[c.active] != nil, !c.waitingResume else { return }
        while let (off, bytes) = c.sender.next(max: 16 << 10) {
            out(c.active, .data(channel: ch, offset: off, bytes: bytes))
        }
        chans[ch] = c
    }

    private func handle(_ m: ScreenLinkMessage, from id: Int) {
        guard !ended else { return }
        if let l = links[id], l.route != nil, muted.contains(l.kind) { return } // a test's black-holed route
        links[id]?.unanswered = nil
        links[id]?.unansweredPings = 0
        if links[id]?.stale == .silent { links[id]?.stale = nil } // it speaks again: no need to replace it
        switch m {
        case let .data(ch, off, bytes):
            guard var c = chans[ch], c.flow == nil else { return } // the UDP flow carries it: these are stale copies
            do {
                let fresh = try c.receiver.accept(bytes, at: off)
                if !fresh.isEmpty {
                    if c.assembler != nil {
                        let whole = try c.assembler!.push(fresh)
                        if !whole.isEmpty { c.inbox.append(whole) }
                    } else {
                        c.inbox.append(fresh)
                    }
                }
                chans[ch] = c
                out(id, .ack(channel: ch, offset: c.receiver.offset))
                deliver(ch)
                finishIfClosed(ch)
            } catch {
                lose(id, wait: false) // a gap: this transport skipped bytes
            }
        case let .ack(ch, off):
            guard var c = chans[ch] else { return }
            c.sender.acknowledged(through: off)
            let waiters = c.sender.hasRoom ? c.roomWaiters : []
            if c.sender.hasRoom { c.roomWaiters = [] }
            chans[ch] = c
            waiters.forEach { $0.resume() }
        case let .paths(p):
            paths = p
            emit.yield(.paths(p))
        case .attached:
            links[id]?.attached = true
            for _ in 0..<5 { ping(id) } // the first probe: five round trips at once
            askFlow(id)
            if let ch = links[id]?.relayFor, ch != 0 { relayReattached(id, for: ch) }
        case let .pong(sent, retrans, packets):
            guard var l = links[id] else { return }
            let rtt = (now() * 1e6 - Double(sent)) / 1000
            l.meter.pong(rttMs: max(0, rtt), retransmitted: retrans, sent: packets, counts: l.route != nil, at: now())
            links[id] = l
        case let .resumed(entries):
            // A new video relay's first message names its channel.
            if let k = pendingVideo.removeValue(forKey: id), let e = entries.first {
                chans[e.channel] = Chan(id: e.channel, sender: LinkSender(window: 64 << 10), active: id,
                                        assembler: VideoMessageAssembler(), home: id)
                links[id]?.relayFor = e.channel
                k.resume(returning: e.channel)
                // A direct route carries the session: the new video stream goes there too.
                if let direct = linkID(for: policy.current), direct != id { use([e.channel], on: direct) }
                return
            }
            for e in entries {
                guard var c = chans[e.channel], c.active == id else { continue }
                c.sender.resume(from: e.offset)
                c.waitingResume = false
                chans[e.channel] = c
                pump(e.channel)
            }
        case let .close(ch, off):
            guard chans[ch] != nil else { return }
            chans[ch]?.closeAt = off
            out(id, .closeAck(channel: ch, offset: off))
            finishIfClosed(ch)
        case let .reset(ch, off):
            resetChannel(ch, to: off)
            if let c = chans[ch] { out(id, .ack(channel: ch, offset: c.receiver.offset)) }
        case let .udpReady(r):
            udpReady(r, from: id)
        case .use, .attach, .ping, .unknown, .udpOpen, .closeAck:
            break // the node's to receive, or a type this side doesn't know
        }
    }

    private func deliver(_ ch: UInt8) {
        guard var c = chans[ch] else { return }
        while !c.inbox.isEmpty, !c.readers.isEmpty {
            c.readers.removeFirst().resume(returning: c.inbox.removeFirst())
        }
        chans[ch] = c
    }

    private func closeReached(_ c: Chan) -> Bool { c.closeAt.map { c.receiver.offset >= $0 } ?? false }

    /// The node closed the channel and everything before the close was read: the channel ends.
    private func finishIfClosed(_ ch: UInt8) {
        guard let c = chans[ch], closeReached(c), c.inbox.isEmpty, !c.ended else { return }
        chans[ch]?.ended = true
        wake(ch, error: ScreenLinkError.closed)
        if ch == ScreenLinkWire.rfb { end() }
    }

    private func wake(_ ch: UInt8, error: Error) {
        guard var c = chans[ch] else { return }
        let readers = c.readers, waiters = c.roomWaiters
        c.readers = []
        c.roomWaiters = []
        chans[ch] = c
        readers.forEach { $0.resume(throwing: error) }
        waiters.forEach { $0.resume(throwing: error) }
    }

    /// A transport ended. The channels it carried go on over their relay, or over another route when it was their
    /// relay that ended (which the app then opens again). A channel with neither waits up to `relayGrace` for a relay
    /// opened again (#8709) and ends then, or at once when `wait` is false (a gap) or the session can't attach again.
    private func lose(_ id: Int, wait: Bool = true) {
        guard let l = links.removeValue(forKey: id) else { return }
        l.writer.finish()
        l.transport.close()
        flowAsked[id] = nil
        for (route, f) in flows where f.parent == id { closeFlow(route) } // a flow goes with its route
        if let k = pendingVideo.removeValue(forKey: id) { k.resume(throwing: ScreenLinkError.closed) }
        if let route = l.route {
            lost.insert(route)
            if !ended { emit.yield(.lost(route)) }
        }
        let t = now()
        var reopen: [UInt8] = []
        for (ch, c) in chans where !c.ended {
            if c.home == id {
                chans[ch]?.home = nil
                if c.active != id { reopen.append(ch) } // a direct route carries it: the relay comes back
            }
            guard c.active == id else { continue }
            if let alt = fallback(for: ch, excluding: id) {
                if chans[ch]?.home == nil { reopen.append(ch) }
                use([ch], on: alt)
            } else if wait, !ended, paths?.token != nil {
                // Nothing carries it now. The network may have just changed under its relay: it waits for one opened
                // again, and what the app sends meanwhile is kept for it.
                chans[ch]?.orphanedSince = t
                reopen.append(ch)
            } else {
                endChannel(ch)
            }
        }
        if !ended { for ch in Set(reopen) { askReopen(ch, at: t) } }
    }

    private func endChannel(_ ch: UInt8) {
        chans[ch]?.ended = true
        wake(ch, error: ScreenLinkError.closed)
        if ch == ScreenLinkWire.rfb { end() }
    }

    /// Where a channel goes when its transport ends: its relay unless that is being replaced, else any attached direct
    /// route.
    private func fallback(for ch: UInt8, excluding id: Int) -> Int? {
        if let h = chans[ch]?.home, h != id, let r = links[h], r.attached, r.stale == nil { return h }
        return links.filter { $0.key != id && $0.value.route != nil && $0.value.attached }.keys.min()
    }

    // MARK: Relays opened again (#8709)

    /// Asks the app to open channel `ch`'s relay again, at most once per `reopenEvery`.
    private func askReopen(_ ch: UInt8, at t: Double) {
        guard !ended, paths?.token != nil, var c = chans[ch], !c.ended else { return }
        if let at = c.reopenAskedAt, t - at < Self.reopenEvery { return }
        c.reopenAskedAt = t
        chans[ch] = c
        emit.yield(.reopen(video: ch != ScreenLinkWire.rfb))
    }

    /// Every channel that needs a relay: nothing carries it, its relay is being replaced, or one asked for earlier
    /// hasn't attached (the app is asked again every `reopenEvery`).
    private func askReopenForStaleRelays() {
        let t = now()
        for (ch, c) in chans where !c.ended {
            let homeStale = c.home.flatMap { links[$0]?.stale } != nil
            let waiting = c.home == nil && c.reopenAskedAt != nil
            if c.orphanedSince != nil || homeStale || waiting { askReopen(ch, at: t) }
        }
    }

    /// A relay opened again for channel `ch` attached: it is the channel's relay from now on, the channel moves onto
    /// it once if a relay or nothing carried it, and the relay it replaces closes. If that relay spoke again while
    /// this one opened, it stays and this one closes instead, so the channel doesn't move at all.
    private func relayReattached(_ id: Int, for ch: UInt8) {
        guard var c = chans[ch], !c.ended else { return }
        let old = c.home.flatMap { $0 == id ? nil : $0 }
        c.reopenAskedAt = nil
        if let old, let o = links[old], o.attached, o.stale == nil {
            chans[ch] = c
            retire(id)
            return
        }
        c.home = id
        c.orphanedSince = nil
        chans[ch] = c
        if c.active != id, links[c.active]?.route == nil { use([ch], on: id) } // a direct route keeps it
        if let old { retire(old) }
    }

    /// A relay no channel uses any more: closed without touching the channels.
    private func retire(_ id: Int) {
        guard let l = links.removeValue(forKey: id) else { return }
        l.writer.finish()
        l.transport.close()
        flowAsked[id] = nil
        for (ch, c) in chans where c.home == id { chans[ch]?.home = nil }
    }

    /// The transport a route names: a direct route's connection; nil for the relay (each channel has its own).
    private func linkID(for route: String) -> Int? {
        guard route != Self.relayRoute else { return nil }
        return links.first { $0.value.route == route && $0.value.attached }?.key
    }

    /// Moves channels onto transport `id`: `use` with what the phone has of each; nothing more goes out on them until
    /// the node's `resumed` says what it has.
    private func use(_ chs: [UInt8], on id: Int) {
        var entries: [ScreenLinkOffset] = []
        for ch in chs {
            guard var c = chans[ch], !c.ended else { continue }
            c.active = id
            c.waitingResume = true
            c.orphanedSince = nil
            chans[ch] = c
            entries.append(ScreenLinkOffset(ch, c.receiver.offset))
        }
        if !entries.isEmpty { out(id, .use(entries)) }
    }

    /// The policy picked another route: every live channel moves there (to its own relay for the relay).
    private func move(to route: String, reason: PathChange.Reason, routes: [String: LinkStats]) {
        let direct = linkID(for: route)
        if route != Self.relayRoute, direct == nil { return }
        for (r, f) in flows where r != route && f.carrying != nil { stopCarrying(r) } // the channels move with the route
        var byLink: [Int: [UInt8]] = [:]
        for (ch, c) in chans where !c.ended {
            guard let target = direct ?? c.home, target != c.active else { continue }
            byLink[target, default: []].append(ch)
        }
        for (id, chs) in byLink { use(chs.sorted(), on: id) }
        let from = lastRoute.flatMap { routes[$0] }
        let to = routes[route] ?? LinkStats(kind: route == Self.relayRoute ? .relay : (links[direct ?? -1]?.kind ?? .lan), via: relayVia)
        lastRoute = route
        emit.yield(.moved(PathChange(from: from, to: to, reason: reason)))
    }

    private func end() {
        guard !ended else { return }
        ended = true
        ticker?.cancel()
        flowTicker?.cancel()
        for route in Array(flows.keys) { closeFlow(route) }
        for id in Array(links.keys) { lose(id) }
        for ch in Array(chans.keys) {
            chans[ch]?.ended = true
            wake(ch, error: ScreenLinkError.closed)
        }
        pendingVideo.values.forEach { $0.resume(throwing: ScreenLinkError.closed) }
        pendingVideo = [:]
        emit.yield(.ended)
        emit.finish()
    }

    // MARK: Measuring

    private func ping(_ id: Int) {
        links[id]?.lastPing = now()
        if links[id]?.unanswered == nil { links[id]?.unanswered = now() }
        links[id]?.unansweredPings += 1
        out(id, .ping(UInt64(now() * 1e6)))
    }

    private var lastLook = -Double.infinity
    /// The route the channels were last moved to (nil before the first move: the relay).
    private var lastRoute: String?

    /// Every 250 ms: pings due, a silent direct route dropped, and once a second the policy.
    private func tick() -> Bool {
        guard !ended else { return false }
        let t = now()
        for (id, l) in links {
            if l.route != nil, let since = l.unanswered, t - since >= Self.silentAfter { lose(id) }
            else if !l.attached, t - l.added >= Self.attachWithin { lose(id) } // its attach went unanswered (#9218)
        }
        // A relay gone quiet (#8709): Wi-Fi without a way out, or a network that left without a word. It stays
        // until a new one attaches; a cellular hiccup that ends in time costs nothing.
        for (id, l) in links where l.route == nil && l.attached && l.relayFor != 0 && l.stale == nil {
            guard let since = l.unanswered, l.unansweredPings >= 2 else { continue }
            let rtt = (l.meter.stats(kind: .relay, via: l.via, at: t).srttMs ?? 0) / 1000
            if t - since >= max(Self.relaySilentAfter, 4 * rtt) { links[id]?.stale = .silent }
        }
        for (ch, c) in chans where !c.ended {
            if links[c.active] == nil, let since = c.orphanedSince, t - since >= Self.relayGrace { endChannel(ch) }
        }
        guard !ended else { return false }
        askReopenForStaleRelays()
        // The route in use every second, the others every two, so they stay warm for a move.
        let inUse = chans[ScreenLinkWire.rfb]?.active
        for (id, l) in links where l.attached && l.relayFor != 0 {
            let every = id == inUse ? 1.0 : 2.0
            if t - l.lastPing >= every { ping(id) }
        }
        for (id, l) in links where l.route != nil && l.attached { askFlow(id) }
        if t - lastFlowStatus >= 1 {
            lastFlowStatus = t
            emit.yield(.flow(flowStatus(at: t)))
        }
        guard t - lastLook >= 1 else { return true }
        lastLook = t
        let liveRelay = chans[ScreenLinkWire.rfb]?.home.flatMap { links[$0] }.flatMap { $0.attached ? $0 : nil }
        if lastRoute == nil { // the stream's first route: the relay
            lastRoute = Self.relayRoute
            let relay = liveRelay?.meter.stats(kind: .relay, via: relayVia, at: t)
            emit.yield(.moved(PathChange(from: nil, to: relay ?? LinkStats(kind: .relay, via: relayVia), reason: .start)))
        }
        var routes: [String: LinkStats] = [:]
        if let relay = liveRelay {
            routes[Self.relayRoute] = relay.meter.stats(kind: .relay, via: relayVia, at: t)
        }
        for l in links.values {
            if let r = l.route, l.attached {
                routes[r] = l.meter.stats(kind: l.kind, via: l.via, at: t)
            }
        }
        let decision = policy.decide(routes, lost: lost, networkChanged: networkChanged, now: t)
        lost = []
        networkChanged = false
        emit.yield(.routes(routes, current: policy.current, decision: decision))
        if case let .move(to, reason) = decision { move(to: to, reason: reason, routes: routes) }
        return true
    }
}

// MARK: The video over UDP (#7873)

extension ScreenLinkSession {
    /// A flow that hasn't proved the phone's address within this is taken as blocked (UDP doesn't get through).
    public static let flowProve = 1.0
    /// A flow with a ping out this long and nothing heard is lost.
    public static let flowSilent = 1.0
    /// More frames than this given up within `flowGiveUpWindow` and the flow is left for the route's TLS connection.
    public static let flowGiveUps = 2
    public static let flowGiveUpWindow = 10.0
    /// After a flow failed or was left, the route asks again after this, doubling to `flowRetryMax`.
    public static let flowRetryFirst = 30.0
    public static let flowRetryMax = 300.0
    /// How often the flows tick (#9492): every 5 ms while one carries the video, whose loss recovery is timed against
    /// the round trip; otherwise every 50 ms, enough for the hellos (0.2 s), pings (1 s) and timeouts and for the video
    /// to move onto a proved flow. A 5 ms clock with nothing to carry (a page on its sign-in form, a route still being
    /// proved) woke the phone about 200 times a second.
    public static func flowTickNanoseconds(carrying: Bool) -> UInt64 { carrying ? 5_000_000 : 50_000_000 }
    /// Pacing asked for before the packets say (kbit/s, `ScreenUDPPace`): a LAN route takes bursts; a Tailscale route
    /// may sit behind a slow uplink, and on cellular it likely does.
    public static let paceLAN: UInt32 = 150_000
    public static let paceTailscale: UInt32 = 40_000
    public static let paceCellular: UInt32 = 8_000

    /// Turns the video over UDP on (the AEAD for a key) or off (nil: flows close and no more open).
    public func enableUDP(_ setup: ScreenUDPSetup?) {
        udp = setup
        if setup == nil {
            for route in Array(flows.keys) { leaveFlow(route, .directOff, retry: false) }
        } else {
            for (id, l) in links where l.route != nil && l.attached { askFlow(id) }
        }
    }

    /// For tests on real networks (the DEBUG command channel): UDP flows go silent (everything that arrives on them is
    /// ignored, as when a network drops UDP), so the video falls back to the route's TLS connection; or speak again.
    public func muteFlows(_ on: Bool) { flowsMuted = on }

    /// The socket the app opened for a route's flow (`openFlow`).
    public func addFlow(_ socket: ScreenUDPSocket, route: String) {
        guard var f = flows[route], f.socket == nil, !ended else { socket.close(); return }
        f.socket = socket
        flows[route] = f
        let id = f.id
        Task { [weak self] in
            while let p = try? await socket.receive() {
                guard let self else { return }
                await self.flowPacket(route, id, p)
            }
            await self?.flowSocketEnded(route, id)
        }
        sendHello(route)
    }

    /// Asks a direct route's node for a flow: UDP turned on, the node offers it, none open, not waiting to retry, and
    /// the phone not on cellular. On a slow, congested cellular link a flow brought nothing over TCP (#7873's shaped
    /// cellular class: as many stalls or more, a worse tail), so it stays for the LAN and Tailscale over Wi-Fi; leaving
    /// Wi-Fi leaves the flow (a network change), and coming back asks again at once.
    private func askFlow(_ id: Int) {
        guard udp != nil, paths?.udp == 1, let l = links[id], let route = l.route, l.attached, flows[route] == nil,
              l.via != .cellular, relayVia != .cellular else { return }
        let t = now()
        if let r = flowRetry[route], t < r.at { return }
        if let at = flowAsked[id], t - at < Self.flowProve * 3 { return }
        flowAsked[id] = t
        out(id, .udpOpen)
    }

    private func udpReady(_ r: ScreenLinkUDPReady, from id: Int) {
        guard let l = links[id], let route = l.route, let udp, flows[route] == nil else { return }
        guard let port = r.port, let fid = r.flowID, let exporter = l.transport as? ScreenLinkKeyExporter,
              let material = exporter.exportKeyingMaterial(label: ScreenUDPWire.exporterLabel, context: fid, length: ScreenUDPWire.keyMaterialLength),
              let keys = ScreenUDPKeyMaterial(material) else {
            retryFlow(route)
            return
        }
        flows[route] = Flow(route: route, parent: id, kind: l.kind, id: fid,
                            sealer: ScreenUDPSealer(cipher: udp.cipher(keys.toNodeKey), iv: keys.toNodeIV, flow: fid),
                            opener: udp.cipher(keys.toPhoneKey), openIV: keys.toPhoneIV, opened: now())
        emit.yield(.openFlow(route: route, port: port))
        if flowTicker == nil {
            flowTicker = Task { [weak self] in
                var carrying = false
                while !Task.isCancelled {
                    try? await Task.sleep(nanoseconds: Self.flowTickNanoseconds(carrying: carrying))
                    guard let self, let next = await self.flowTick() else { return }
                    carrying = next
                }
            }
        }
    }

    @discardableResult
    private func sendFlow(_ route: String, _ m: ScreenUDPMessage) -> Bool {
        guard var f = flows[route], let socket = f.socket else { return false }
        guard let p = f.sealer.seal(m.encoded()) else {
            leaveFlow(route, .lost) // the packet numbers ran out: never reuse a nonce
            return false
        }
        flows[route] = f
        socket.send(p)
        return true
    }

    private func sendHello(_ route: String) {
        guard var f = flows[route] else { return }
        f.lastHello = now()
        flows[route] = f
        sendFlow(route, .helloPhone(echo: f.challenge))
    }

    private func flowSocketEnded(_ route: String, _ id: Data) {
        guard flows[route]?.id == id else { return }
        leaveFlow(route, .lost)
    }

    /// One datagram from a flow's socket: dropped unless it opens under the node's key with a number not seen before.
    private func flowPacket(_ route: String, _ id: Data, _ p: Data) {
        guard !flowsMuted, var f = flows[route], f.id == id, let head = ScreenUDPPacket.read(p), head.flow == f.id,
              let body = ScreenUDPPacket.open(p, cipher: f.opener, iv: f.openIV, number: head.number),
              f.window.accept(head.number), let m = try? ScreenUDPMessage.decode(body, fromNode: true) else { return }
        let t = now()
        f.loss.received(head.number, at: t)
        f.unanswered = nil
        flows[route] = f
        switch m {
        case let .helloNode(challenge, validated):
            if validated {
                if !f.proved {
                    flows[route]?.proved = true
                    pingFlow(route)
                }
            } else if challenge != 0 {
                flows[route]?.challenge = challenge
                sendHello(route)
            }
        case let .data(h, fragment):
            flows[route]?.pace.packet(offset: h.offset, bytes: p.count, at: t)
            flowData(route, h, fragment, at: t)
        case .resumed:
            flows[route]?.waitingResume = false
        case let .reset(ch, off):
            flowReset(route, ch, off, at: t)
        case let .pong(micros, data, parity, resent):
            let rtt = max(0, (t * 1e6 - Double(micros)) / 1000)
            flows[route]?.meter.pong(rttMs: rtt, retransmitted: 0, sent: 0, counts: false, at: t)
            flows[route]?.counters.append((t, data, parity, resent))
            flows[route]?.counters.removeAll { t - $0.at > 5 }
        default:
            break
        }
    }

    private func flowData(_ route: String, _ h: ScreenUDPFragmentHead, _ fragment: Data, at t: Double) {
        guard var f = flows[route], let ch = f.carrying, h.channel == ch, var rx = f.receiver else { return }
        let d = rx.fragment(h, fragment, at: t)
        f.receiver = rx
        for m in d.messages {
            f.recentBlocks += ScreenUDPLayout.blocks(length: m.bytes.count).map(\.data)
        }
        if f.recentBlocks.count > 240 { f.recentBlocks.removeFirst(f.recentBlocks.count - 240) }
        flows[route] = f
        for m in d.messages where !deliverFlow(ch, m.offset, m.bytes) {
            leaveFlow(route, .lost)
            return
        }
        if d.ack { sendFlow(route, .ack(channel: ch, offset: rx.expected)) }
    }

    /// A whole message from a flow into its channel, as the TLS connection would have brought it.
    private func deliverFlow(_ ch: UInt8, _ off: UInt64, _ bytes: Data) -> Bool {
        guard var c = chans[ch] else { return true }
        do {
            let fresh = try c.receiver.accept(bytes, at: off)
            if !fresh.isEmpty {
                if c.assembler != nil {
                    let whole = try c.assembler!.push(fresh)
                    if !whole.isEmpty { c.inbox.append(whole) }
                } else {
                    c.inbox.append(fresh)
                }
            }
        } catch {
            return false
        }
        chans[ch] = c
        deliver(ch)
        finishIfClosed(ch)
        return true
    }

    /// The node's reset of a channel, over a route or a flow: the channel goes on at `off`, and a partial message from
    /// before it is dropped. A reset at or behind the receiver changes nothing (#10005): it is a late copy, from a route
    /// the channel has left, of a reset already applied, and the message the phone has received in part since is the
    /// one the stream goes on with.
    private func resetChannel(_ ch: UInt8, to off: UInt64) {
        if chans[ch]?.receiver.reset(to: off) == true { chans[ch]?.assembler?.reset() }
    }

    private func flowReset(_ route: String, _ ch: UInt8, _ off: UInt64, at t: Double) {
        guard var f = flows[route], f.carrying == ch, var rx = f.receiver else { return }
        resetChannel(ch, to: off)
        let d = rx.reset(to: off, at: t)
        f.receiver = rx
        flows[route] = f
        for m in d.messages where !deliverFlow(ch, m.offset, m.bytes) {
            leaveFlow(route, .lost)
            return
        }
        sendFlow(route, .ack(channel: ch, offset: rx.expected))
    }

    /// The phone's interface a flow leaves on is cellular, or Tailscale rides cellular: parity is capped lower and
    /// counts against the mobile data budget.
    private func onCellular(_ f: Flow) -> Bool {
        links[f.parent]?.via == .cellular || relayVia == .cellular
    }

    private func pingFlow(_ route: String) {
        guard var f = flows[route] else { return }
        let t = now()
        f.lastPing = t
        if f.unanswered == nil { f.unanswered = t }
        // Loss over the last 2 s is the congestion signal; parity stays low while a link drops that much.
        let recent = f.loss.loss(at: t, window: 2, enough: 20)
        f.congestion.look(loss: recent, at: t)
        var cap = onCellular(f) ? ScreenUDPParity.cellularCap : ScreenUDPParity.wifiCap
        if let recent, recent >= ScreenUDPCongestion.congested { cap = min(cap, ScreenUDPParity.cellularCap) }
        f.parity = ScreenUDPParity.ratio(loss: f.loss.loss(at: t), cap: cap, recentBlocks: f.recentBlocks)
        let start = f.kind == .lan ? Self.paceLAN : onCellular(f) ? Self.paceCellular : Self.paceTailscale
        flows[route] = f
        sendFlow(route, .ping(micros: UInt64(t * 1e6), parity: UInt16(f.parity), paceKbps: f.pace.request(at: t, start: start)))
    }

    /// One tick of the open flows: proving, pings, moving the video onto one, asking for what is missing. Nil: none
    /// left, the ticker ends; else whether any flow carries the video now (the next tick's interval,
    /// `flowTickNanoseconds`).
    private func flowTick() -> Bool? {
        guard !ended, !flows.isEmpty else {
            flowTicker = nil
            return nil
        }
        let t = now()
        for route in Array(flows.keys) {
            guard var f = flows[route] else { continue }
            guard links[f.parent] != nil else { closeFlow(route); continue }
            if f.socket == nil || !f.proved {
                if t - f.opened >= Self.flowProve + (f.socket == nil ? 2 : 0) {
                    leaveFlow(route, .lost) // UDP doesn't get through: the route stays on TLS
                } else if f.socket != nil, t - f.lastHello >= 0.2 {
                    sendHello(route)
                }
                continue
            }
            if t - f.lastPing >= 1 {
                pingFlow(route)
                guard let refreshed = flows[route] else { continue }
                f = refreshed
            }
            if let since = f.unanswered, t - since >= Self.flowSilent {
                leaveFlow(route, .lost)
                continue
            }
            if f.carrying == nil, f.meter.lastPongAt != nil, let v = currentVideo, let c = chans[v], c.active == f.parent,
               !c.waitingResume, c.flow == nil {
                startCarrying(route, v)
                guard let refreshed = flows[route] else { continue }
                f = refreshed
            }
            guard let ch = f.carrying, var rx = f.receiver else { continue }
            if f.waitingResume {
                if t - f.useAt >= Self.flowProve {
                    leaveFlow(route, .lost)
                    continue
                }
                if t - f.lastUse >= 0.1 {
                    f.lastUse = t
                    flows[route] = f
                    guard sendFlow(route, .use([ScreenLinkOffset(ch, rx.expected)])), let refreshed = flows[route] else { continue }
                    f = refreshed
                }
            }
            let givenUpBefore = rx.givenUp
            let req = rx.requests(at: t, rtt: (f.meter.srttMs ?? 20) / 1000, jitter: (f.meter.jitterMs ?? 0) / 1000)
            f.receiver = rx
            let newGiveUp = rx.givenUp > givenUpBefore
            if newGiveUp {
                f.giveUps.append(t)
                f.giveUps.removeAll { t - $0 > Self.flowGiveUpWindow }
            }
            flows[route] = f
            if let off = req.skip {
                if newGiveUp, f.giveUps.count > Self.flowGiveUps {
                    leaveFlow(route, .degraded)
                    continue
                }
                guard sendFlow(route, .skip(channel: ch, offset: off)) else { continue }
            }
            if let w = req.want {
                guard sendFlow(route, .want(channel: ch, from: w.from, to: w.to)) else { continue }
            }
            var nack = req.nack[...]
            while !nack.isEmpty {
                guard sendFlow(route, .nack(channel: ch, Array(nack.prefix(80)))) else { break }
                nack = nack.dropFirst(80)
            }
        }
        return flows.values.contains { $0.carrying != nil }
    }

    /// The route's stats with the flow's round trip, as the video sees it on `transport`.
    private func flowStats(_ f: Flow, _ transport: LinkStats.Transport) -> LinkStats {
        let t = now()
        let l = links[f.parent]
        var s = l?.meter.stats(kind: f.kind, via: l?.via ?? relayVia, at: t) ?? LinkStats(kind: f.kind, via: relayVia)
        if transport == .udp {
            s.srttMs = f.meter.srttMs ?? s.srttMs
            s.jitterMs = f.meter.jitterMs ?? s.jitterMs
            s.packetLoss = f.loss.loss(at: t)
        }
        s.transport = transport
        return s
    }

    /// The video moves onto a proved flow: from the start of the message it was in the middle of (a flow sends whole
    /// messages only), and nothing more is taken for it from other transports.
    private func startCarrying(_ route: String, _ ch: UInt8) {
        guard var c = chans[ch], var f = flows[route] else { return }
        let boundary = c.receiver.offset - UInt64(c.assembler?.pendingBytes ?? 0)
        c.receiver.rewind(to: boundary)
        c.assembler?.reset()
        c.flow = route
        chans[ch] = c
        let t = now()
        f.carrying = ch
        f.receiver = ScreenUDPReceiver(channel: ch, expected: boundary)
        f.waitingResume = true
        f.useAt = t
        f.lastUse = t
        flows[route] = f
        guard sendFlow(route, .use([ScreenLinkOffset(ch, boundary)])) else { return }
        emit.yield(.moved(PathChange(from: flowStats(f, .tcp), to: flowStats(f, .udp), reason: .faster)))
    }

    /// The flow stops carrying the channel (it moves with the route, or back to the route's TLS connection).
    private func stopCarrying(_ route: String) {
        guard var f = flows[route], let ch = f.carrying else { return }
        f.carrying = nil
        f.receiver = nil
        f.waitingResume = false
        flows[route] = f
        if chans[ch]?.flow == route { chans[ch]?.flow = nil }
    }

    /// The flow is left: the video goes on over the route's TLS connection from what the phone has, and the route asks
    /// for a flow again later (`retry`) or after a network change.
    private func leaveFlow(_ route: String, _ reason: PathChange.Reason, retry: Bool = true) {
        guard let f = flows[route] else { return }
        let ch = f.carrying
        stopCarrying(route)
        flows[route] = nil
        f.socket?.close()
        if retry { retryFlow(route) }
        guard let ch, let c = chans[ch], !c.ended else { return }
        if links[c.active] != nil { use([ch], on: c.active) }
        emit.yield(.moved(PathChange(from: flowStats(f, .udp), to: flowStats(f, .tcp), reason: reason)))
    }

    /// The flow closes with its route or the session: no move of its own (the channels follow the route).
    private func closeFlow(_ route: String) {
        stopCarrying(route)
        flows.removeValue(forKey: route)?.socket?.close()
    }

    private func retryFlow(_ route: String) {
        let wait = flowRetry[route].map { min($0.wait * 2, Self.flowRetryMax) } ?? Self.flowRetryFirst
        flowRetry[route] = (now() + wait, wait)
    }

    /// The carrying flow's state for the app: loss, parity, the overhead the budget leaves room for.
    private func flowStatus(at t: Double) -> ScreenUDPStatus? {
        guard let f = flows.values.first(where: { $0.carrying != nil }) else { return nil }
        var overhead = Double(f.recentBlocks.isEmpty ? 0 : f.recentBlocks.map { ScreenUDPLayout.parity(data: $0, ratio: f.parity) }.reduce(0, +))
            / Double(max(1, f.recentBlocks.reduce(0, +)))
        if let a = f.counters.first, let b = f.counters.last, b.data > a.data + 64 << 10 {
            overhead = max(overhead, ScreenUDPParity.overhead(dataBytes: b.data - a.data, parityBytes: b.parity - a.parity,
                                                              resentBytes: b.resent - a.resent))
        }
        return ScreenUDPStatus(route: f.route, loss: f.loss.loss(at: t), parity: f.parity, overhead: overhead, rttMs: f.meter.srttMs,
                               scale: f.congestion.scale)
    }
}

/// One channel of a session as a byte stream (`RFBTransport`'s shape): RFB's bytes, or a video stream's whole messages.
public final class ScreenLinkChannelEnd: ScreenLinkTransport, @unchecked Sendable {
    public let session: ScreenLinkSession
    public let channel: UInt8

    init(session: ScreenLinkSession, channel: UInt8) {
        self.session = session
        self.channel = channel
    }

    public func send(_ data: Data) async throws { try await session.send(channel, data) }
    public func receive() async throws -> Data { try await session.receive(channel) }
    public func close() {
        let s = session, ch = channel
        Task { await s.close(ch) }
    }
}
