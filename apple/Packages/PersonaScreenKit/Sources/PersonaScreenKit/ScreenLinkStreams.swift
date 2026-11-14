import Foundation

// Opening a live screen's streams as a screen session when the hub and the node offer one, and as today's raw stream
// otherwise (#6398). The phone offers the subprotocol on the hub's two screen routes; whether the hub selected it is
// known once the WebSocket is open, which is when its first message arrives. Pure: the app gives it sockets.

/// A hub relay as the session needs it: a transport that says which subprotocol the hub selected, once it is open.
public protocol ScreenLinkSocket: ScreenLinkTransport {
    var subprotocol: String? { get }
}

/// One stream as the app's clients read it: RFB's bytes, or a video stream's messages, from a session's channel or
/// straight from the relay.
public protocol ScreenLinkByteStream: AnyObject, Sendable {
    func send(_ data: Data) async throws
    func receive() async throws -> Data
    func close()
}

/// The sessions open now, by their RFB stream's pair, so the video stream of the same live screen joins its session.
public final class ScreenLinkRegistry: @unchecked Sendable {
    public static let shared = ScreenLinkRegistry()
    private let lock = NSLock()
    private var byPair: [String: WeakSession] = [:]

    private struct WeakSession { weak var session: ScreenLinkSession? }

    public init() {}

    public func set(_ s: ScreenLinkSession, for pair: String) {
        lock.withLock {
            byPair = byPair.filter { $0.value.session != nil }
            byPair[pair] = WeakSession(session: s)
        }
    }

    public func session(for pair: String) -> ScreenLinkSession? { lock.withLock { byPair[pair]?.session } }

    public func remove(_ pair: String) { _ = lock.withLock { byPair.removeValue(forKey: pair) } }
}

/// What a stream turned out to be once its relay opened.
enum OpenedStream: Sendable {
    case raw(ScreenLinkSocket, first: Data)
    case channel(ScreenLinkChannelEnd)
}

/// The RFB stream of a live screen: a session's RFB channel when the hub selected the subprotocol (`onSession` hears
/// of the session), else the relay's own bytes.
public final class ScreenLinkRFBStream: ScreenLinkByteStream, @unchecked Sendable {
    private let socket: ScreenLinkSocket
    private let opened: Task<OpenedStream, Error>
    private let lock = NSLock()
    private var firstTaken = false

    public init(socket: ScreenLinkSocket, now: @escaping @Sendable () -> Double = ScreenLinkSession.monotonic,
                onSession: @escaping @Sendable (ScreenLinkSession) -> Void) {
        self.socket = socket
        opened = Task {
            let first = try await socket.receive()
            guard socket.subprotocol == ScreenLinkWire.subprotocol else { return .raw(socket, first: first) }
            let s = ScreenLinkSession(rfbRelay: socket, first: first, now: now)
            onSession(s)
            return .channel(s.rfb())
        }
    }

    public func receive() async throws -> Data {
        switch try await opened.value {
        case let .raw(sock, first):
            let takeFirst: Bool = lock.withLock {
                defer { firstTaken = true }
                return !firstTaken && !first.isEmpty
            }
            return takeFirst ? first : try await sock.receive()
        case let .channel(end):
            return try await end.receive()
        }
    }

    public func send(_ data: Data) async throws {
        switch try await opened.value {
        case let .raw(sock, _): try await sock.send(data)
        case let .channel(end): try await end.send(data)
        }
    }

    public func close() {
        opened.cancel()
        socket.close()
        Task { [opened] in if case let .channel(end)? = try? await opened.value { end.close() } }
    }
}

/// A video stream of a live screen: a new channel of the pair's session when the hub selected the subprotocol, else
/// the relay's own bytes.
public final class ScreenLinkVideoStream: ScreenLinkByteStream, @unchecked Sendable {
    private let socket: ScreenLinkSocket
    private let opened: Task<OpenedStream, Error>
    private let lock = NSLock()
    private var firstTaken = false

    public init(socket: ScreenLinkSocket, session: ScreenLinkSession?) {
        self.socket = socket
        opened = Task {
            let first = try await socket.receive()
            guard socket.subprotocol == ScreenLinkWire.subprotocol, let session else { return .raw(socket, first: first) }
            return .channel(try await session.attachVideo(socket, first: first))
        }
    }

    public func receive() async throws -> Data {
        switch try await opened.value {
        case let .raw(sock, first):
            let takeFirst: Bool = lock.withLock {
                defer { firstTaken = true }
                return !firstTaken && !first.isEmpty
            }
            return takeFirst ? first : try await sock.receive()
        case let .channel(end):
            return try await end.receive()
        }
    }

    public func send(_ data: Data) async throws {
        switch try await opened.value {
        case let .raw(sock, _): try await sock.send(data)
        case let .channel(end): try await end.send(data)
        }
    }

    public func close() {
        opened.cancel()
        socket.close()
        Task { [opened] in if case let .channel(end)? = try? await opened.value { end.close() } }
    }
}

/// When to dial each direct candidate (#6398): at once when it is new or the phone's networks changed, then after a
/// failure 30 s, doubling to 5 min, and from 30 s again once it attached. A candidate the phone can't reach (no
/// eligible interface) is never dialed.
public struct ProbePlan: Sendable {
    public static let firstRetry = 30.0
    public static let longestRetry = 300.0

    private var retryAt: [String: Double] = [:]
    private var wait: [String: Double] = [:]
    private var open: Set<String> = []

    public init() {}

    /// The candidates to dial now, of those eligible: not open, and not waiting after a failure.
    public mutating func due(_ eligible: [String], now: Double) -> [String] {
        eligible.filter { !open.contains($0) && (retryAt[$0].map { now >= $0 } ?? true) }
            .map { open.insert($0); return $0 }
    }

    /// A dial or an open connection failed: try again later, waiting longer each time.
    public mutating func failed(_ id: String, now: Double) {
        open.remove(id)
        let w = min(Self.longestRetry, wait[id].map { $0 * 2 } ?? Self.firstRetry)
        wait[id] = w
        retryAt[id] = now + w
    }

    /// A connection attached: the next failure waits from the first again.
    public mutating func attached(_ id: String) {
        wait[id] = nil
        retryAt[id] = nil
    }

    /// When the next candidate waiting after a failure is due, if one is (#7791): the prober wakes then and dials
    /// again. Only times after `now`: one already due is dialed by the probe running now, or, with no interface for
    /// it, at the next network change.
    public func nextRetry(after now: Double) -> Double? {
        retryAt.filter { !open.contains($0.key) && $0.value > now }.values.min()
    }

    /// The phone's networks changed: every candidate may be tried again at once.
    public mutating func networkChanged() { retryAt = [:]; wait = [:] }

    /// A candidate the node no longer offers, or whose connection closed without failing.
    public mutating func forget(_ id: String) { open.remove(id) }
}

/// One pending wake-up (#7791): `arm` replaces it with a call of `fire` after that many seconds (nil: none), `cancel`
/// drops it. The prober arms it for the probe plan's next retry; `sleep` is a test's clock.
public final class RetryWake: @unchecked Sendable {
    private let lock = NSLock()
    private var task: Task<Void, Never>?
    private let sleep: @Sendable (Double) async -> Void

    public init(sleep: @escaping @Sendable (Double) async -> Void = { try? await Task.sleep(for: .seconds($0)) }) {
        self.sleep = sleep
    }

    public func arm(in seconds: Double?, _ fire: @escaping @Sendable () async -> Void) {
        lock.withLock {
            task?.cancel()
            task = seconds.map { s in
                Task { [sleep] in
                    await sleep(max(0, s))
                    guard !Task.isCancelled else { return }
                    await fire()
                }
            }
        }
    }

    public func cancel() {
        lock.withLock {
            task?.cancel()
            task = nil
        }
    }
}
