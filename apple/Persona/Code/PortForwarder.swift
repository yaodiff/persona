import Foundation
import Network
import os
import PersonaWire

/// Plain-language cause of a forwarded port's WebSocket handshake or listener failure, recorded per forward so the
/// link sheet can show why a preview failed instead of a blank WebView.
nonisolated enum PortForwardError: Equatable, Sendable {
    case unauthorized
    case forbidden
    case portGone(Int)
    case tooManyConnections
    case serverUnreachable(Int)
    case computerOffline
    case httpError(Int)
    case hubUnreachable
    case listenerFailed

    static func from(statusCode: Int, port: Int) -> PortForwardError {
        switch statusCode {
        case 401: .unauthorized
        case 403: .forbidden
        case 404: .portGone(port)
        case 429: .tooManyConnections
        case 502, 504: .serverUnreachable(statusCode)
        case 503: .computerOffline
        default: .httpError(statusCode)
        }
    }

    var symbol: String {
        switch self {
        case .computerOffline, .hubUnreachable: Theme.Icon.offline
        default: Theme.Icon.error
        }
    }

    @MainActor var title: String {
        switch self {
        case .portGone: String(localized: "Server Stopped")
        case .computerOffline: String(localized: "Computer Offline")
        case .unauthorized: String(localized: "Sign-in Required")
        case .forbidden: String(localized: "Access Denied")
        case .tooManyConnections: String(localized: "Too Many Connections")
        case .serverUnreachable: String(localized: "Server Not Responding")
        case .hubUnreachable: String(localized: "Couldn't Reach Hub")
        case .httpError, .listenerFailed: String(localized: "Couldn't Open Preview")
        }
    }

    @MainActor var message: String {
        switch self {
        case .portGone(let port):
            String(localized: "The service on port :\(String(port)) has stopped.")
        case .computerOffline:
            String(localized: "This computer is offline right now.")
        case .unauthorized:
            String(localized: "Your connection to the hub needs to sign in again.")
        case .forbidden:
            String(localized: "This device doesn't have permission to open this port.")
        case .tooManyConnections:
            String(localized: "Too many connections are open to this computer's ports. Try again in a moment.")
        case .serverUnreachable:
            String(localized: "The dev server didn't respond in time. It may still be starting up.")
        case .hubUnreachable:
            String(localized: "Couldn't reach the hub. Check your network and try again.")
        case .httpError(let code):
            String(localized: "The hub returned an error (HTTP \(String(code))).")
        case .listenerFailed:
            String(localized: "Couldn't restart the local preview connection. Try again.")
        }
    }
}

/// A machine's port on the phone's loopback (docs/operations/ports.md「Forwarding」), the way Moshi forwards a dev server: the app
/// listens on 127.0.0.1 and ::1 at the same port number when it is free (another one when not), and each connection
/// the in-app browser makes there becomes one WebSocket to the hub (…/ports/{port}/connect, the device token), which
/// carries its bytes to the port on the machine. The page loads from http://localhost:<port>/ exactly as on the
/// machine: its absolute paths, its own WebSocket (HMR), redirects and cookies need nothing rewritten.
///
/// Listeners last while the app runs; one found broken (the app was suspended) is started again when returning to the
/// foreground, refreshing, or next asked for. What a connection dials is not fixed when the forward starts (#1886):
/// each one asks for the group's hub, route and token at that moment, so a page keeps loading after the app moves
/// between a tunnel and a direct address, to another hub of the group, or pairs again. A group that goes
/// (disconnected, unpaired) takes its forwards with it.
@MainActor
final class PortForwarder {
    static let shared = PortForwarder()

    struct Key: Hashable, Sendable {
        /// Machine ids are only unique within a group: two groups' machines with one id get a forward each.
        var group: String
        var machine: String
        var port: Int
    }

    /// The group's hub as it is now (the one its socket is on), nil when the group is gone.
    typealias LiveConfig = @MainActor () -> ServerConfig?

    private static let loopbackHosts: Set<String> = ["localhost", "127.0.0.1", "::1", "[::1]"]

    private var forwards: [Key: PortForward] = [:]
    private var restarting: [Key: Task<Int, Error>] = [:]
    private let queue = DispatchQueue(label: "persona.ports")

    /// The phone's port for a machine's port, listening from now on. `config` is asked again for each connection.
    func localPort(_ key: Key, config: @escaping LiveConfig) async throws -> Int {
        if let f = forwards[key], f.isReady {
            f.config = config
            return f.localPort
        }
        forwards[key]?.stop()
        forwards[key] = nil
        let taken = Set(forwards.values.map(\.localPort))
        let f = PortForward(key: key, config: config, queue: queue)
        try await f.start(preferring: taken.contains(key.port) ? 0 : key.port, avoiding: taken)
        forwards[key]?.stop() // another ask for the same port finished first: the last one is kept
        forwards[key] = f
        return f.localPort
    }

    /// Rebuilds the local listeners for `key` if iOS invalidated them (for example after backgrounding), reusing the
    /// same local port whenever both 127.0.0.1 and ::1 are free, or falling back to a fresh dual-stack port.
    func ensureReady(_ key: Key) async throws -> Int {
        if let f = forwards[key], f.isReady { return f.localPort }
        if let task = restarting[key] { return try await task.value }
        let task = Task<Int, Error> { @MainActor [weak self] in
            defer { self?.restarting[key] = nil }
            guard let self, let f = self.forwards[key] else { throw URLError(.cannotConnectToHost) }
            if f.isReady { return f.localPort }
            let preferred = f.localPort > 0 ? f.localPort : key.port
            let taken = Set(self.forwards.filter { $0.key != key }.values.map(\.localPort))
            f.clearError()
            try await f.start(preferring: taken.contains(preferred) ? 0 : preferred, avoiding: taken)
            return f.localPort
        }
        restarting[key] = task
        return try await task.value
    }

    /// Rebuilds the forward behind `localURL` if needed, returning `localURL` (or an updated URL if the local port moved).
    func ensureReady(for localURL: URL) async throws -> URL {
        guard let key = key(for: localURL) else { return localURL }
        let readyPort = try await ensureReady(key)
        guard readyPort != localURL.port,
              var comps = URLComponents(url: localURL, resolvingAgainstBaseURL: false) else { return localURL }
        comps.port = readyPort
        return comps.url ?? localURL
    }

    /// Rebuilds any forwarded port whose listeners are no longer ready (called when returning to the foreground).
    func ensureReadyAll() {
        for (key, f) in forwards where !f.isReady {
            Task { _ = try? await self.ensureReady(key) }
        }
    }

    /// The address to open a port link at on the phone.
    func url(for link: PortLink, machine: MachineRef, config: @escaping LiveConfig) async throws -> URL {
        let port = try await localPort(Key(group: machine.group, machine: machine.id, port: link.port), config: config)
        guard let url = URL(string: "\(link.secure ? "https" : "http")://localhost:\(port)\(link.path)") else {
            throw URLError(.badURL)
        }
        return url
    }

    func isLoopbackHost(_ host: String) -> Bool {
        Self.loopbackHosts.contains(host.lowercased())
    }

    func isLoopbackURL(_ url: URL) -> Bool {
        guard let host = url.host(percentEncoded: false) else { return false }
        return isLoopbackHost(host)
    }

    /// Whether `host:port` is an active loopback endpoint owned by this forwarder (used to scope self-signed TLS prompts).
    func isForwardedLoopback(host: String, port: Int) -> Bool {
        guard port > 0, isLoopbackHost(host) else { return false }
        return forwards.values.contains { $0.localPort == port }
    }

    /// Which machine port a page on the phone's loopback is (the sheet names it).
    func key(for localURL: URL) -> Key? {
        guard let p = localURL.port, isLoopbackURL(localURL) else { return nil }
        return forwards.first { $0.value.localPort == p }?.key
    }

    /// The forward listening for key, if any.
    func forward(_ key: Key) -> PortForward? { forwards[key] }

    /// The last handshake error recorded for the forwarded port behind `localURL`.
    func lastError(for localURL: URL) -> PortForwardError? {
        guard let key = key(for: localURL) else { return nil }
        return forwards[key]?.lastError
    }

    func clearError(for localURL: URL) {
        guard let key = key(for: localURL) else { return }
        forwards[key]?.clearError()
    }

    func recordError(_ error: PortForwardError, for key: Key) {
        forwards[key]?.recordError(error)
    }

    /// The groups' forwards stop listening (the groups were disconnected or unpaired).
    func close(groups: Set<String>) {
        for (key, f) in forwards where groups.contains(key.group) {
            restarting[key]?.cancel()
            restarting[key] = nil
            f.stop()
            forwards[key] = nil
        }
    }

    /// Every forward stops listening (Sign Out of everything).
    func closeAll() {
        restarting.values.forEach { $0.cancel() }
        restarting = [:]
        forwards.values.forEach { $0.stop() }
        forwards = [:]
    }

    static func connectRequest(_ key: Key, config: ServerConfig) -> URLRequest {
        var comps = URLComponents(url: config.url, resolvingAgainstBaseURL: false)!
        var base = comps.percentEncodedPath
        if base.hasSuffix("/") { base.removeLast() }
        comps.percentEncodedPath = base + "/api/v1/machines/\(key.machine)/ports/\(key.port)/connect"
        comps.scheme = config.url.scheme == "https" ? "wss" : "ws"
        var req = URLRequest(url: HubTransport.resolve(comps.url!, hub: config.url), timeoutInterval: PortForward.handshakeTimeout)
        config.authorize(&req)
        req.setValue(LanguageStore.shared.launched, forHTTPHeaderField: "Accept-Language")
        return req
    }
}

/// One forwarded port: its listeners and the connections through them.
nonisolated final class PortForward: @unchecked Sendable {
    /// Matches hub `maxPortStreams = 64`: iOS's default of 4 starves multi-module dev servers (6 keep-alive + HMR WS).
    static let maxConnectionsPerHost = 64
    /// Handshake timeout for upgrading to WebSocket; upgraded long-lived streams are not capped by this interval.
    static let handshakeTimeout: TimeInterval = 15

    static func makeSessionConfiguration() -> URLSessionConfiguration {
        let c = URLSessionConfiguration.default
        c.urlCache = nil
        c.httpMaximumConnectionsPerHost = maxConnectionsPerHost
        c.timeoutIntervalForRequest = handshakeTimeout
        return c
    }

    private struct State: @unchecked Sendable {
        var listeners: [NWListener] = []
        var localPort = 0
        var lastError: PortForwardError?
    }

    let key: PortForwarder.Key
    /// Where connections go: read on the main actor as each one is accepted (`PortForwarder.localPort` replaces it).
    @MainActor var config: PortForwarder.LiveConfig
    private let queue: DispatchQueue
    private let session: URLSession
    private let state = OSAllocatedUnfairLock(initialState: State())
    #if DEBUG
    /// Unit tests: each request a browser connection dials, before it goes out.
    @MainActor var dialedForTests: ((URLRequest) -> Void)?
    #endif

    @MainActor init(key: PortForwarder.Key, config: @escaping PortForwarder.LiveConfig, queue: DispatchQueue) {
        self.key = key
        self.config = config
        self.queue = queue
        session = URLSession(configuration: Self.makeSessionConfiguration())
    }

    var sessionConfiguration: URLSessionConfiguration { session.configuration }

    var localPort: Int { state.withLock { $0.localPort } }

    var lastError: PortForwardError? { state.withLock { $0.lastError } }

    func recordError(_ error: PortForwardError) {
        state.withLock { $0.lastError = error }
    }

    func clearError() {
        state.withLock { $0.lastError = nil }
    }

    /// The request the next connection dials: the group's hub, route (`HubTransport`) and token as they are now; nil
    /// when the group is gone.
    @MainActor func currentRequest() -> URLRequest? {
        config().map { PortForwarder.connectRequest(key, config: $0) }
    }

    var isReady: Bool {
        let ls = state.withLock { $0.listeners }
        return !ls.isEmpty && ls.allSatisfy { $0.state == .ready }
    }

    /// Listens on both 127.0.0.1 and ::1 at the same port number. If `port` is taken on either family, chooses an
    /// ephemeral port free on both so `localhost` (which resolves ::1 first) never connects to an unrelated process.
    func start(preferring port: Int, avoiding taken: Set<Int> = []) async throws {
        let (boundPort, boundListeners) = try await bindDualStack(preferring: port, avoiding: taken)
        let previous = state.withLock { s -> [NWListener] in
            let old = s.listeners
            s.localPort = boundPort
            s.listeners = boundListeners
            return old
        }
        previous.forEach { $0.cancel() }
    }

    func stop() {
        let old = state.withLock { s -> [NWListener] in
            let ls = s.listeners
            s.listeners = []
            return ls
        }
        old.forEach { $0.cancel() }
        session.invalidateAndCancel()
    }

    #if DEBUG
    /// Simulates iOS cancelling active listeners in the background while keeping the forward entry and local port.
    func cancelListenersForTests() {
        let ls = state.withLock { $0.listeners }
        ls.forEach { $0.cancel() }
    }
    #endif

    private func bindDualStack(preferring port: Int, avoiding taken: Set<Int>) async throws -> (Int, [NWListener]) {
        if port != 0, !taken.contains(port), let pair = try await tryBindPair(port: port) {
            return (port, pair)
        }
        var lastError: Error = POSIXError(.EADDRINUSE)
        for _ in 0..<16 {
            do {
                let v4 = try await listen("127.0.0.1", 0)
                let p = Int(v4.port?.rawValue ?? 0)
                if p == 0 || taken.contains(p) {
                    v4.cancel()
                    continue
                }
                do {
                    let v6 = try await listen("::1", p)
                    return (p, [v4, v6])
                } catch {
                    if Self.isIPv6Unavailable(error) { return (p, [v4]) }
                    v4.cancel()
                    lastError = error
                }
            } catch {
                lastError = error
            }
        }
        throw lastError
    }

    private func tryBindPair(port: Int) async throws -> [NWListener]? {
        let v4: NWListener
        do {
            v4 = try await listen("127.0.0.1", port)
        } catch {
            return nil
        }
        do {
            let v6 = try await listen("::1", port)
            return [v4, v6]
        } catch {
            if Self.isIPv6Unavailable(error) { return [v4] }
            v4.cancel()
            return nil
        }
    }

    private static func isIPv6Unavailable(_ error: Error) -> Bool {
        if case NWError.posix(let code) = error {
            return code == .EAFNOSUPPORT || code == .EADDRNOTAVAIL || code == .EPROTONOSUPPORT || code == .ENOPROTOOPT
        }
        return false
    }

    private func listen(_ host: NWEndpoint.Host, _ port: Int) async throws -> NWListener {
        let params = NWParameters.tcp
        params.requiredLocalEndpoint = .hostPort(host: host, port: NWEndpoint.Port(rawValue: UInt16(port)) ?? .any)
        let l = try NWListener(using: params)
        l.newConnectionHandler = { @Sendable [weak self] conn in self?.accept(conn) }
        return try await withCheckedThrowingContinuation { (cont: CheckedContinuation<NWListener, Error>) in
            let once = RunOnce()
            l.stateUpdateHandler = { @Sendable state in
                switch state {
                case .ready: once.run { cont.resume(returning: l) }
                case .failed(let e), .waiting(let e):
                    once.run { cont.resume(throwing: e) }
                    l.cancel()
                case .cancelled: once.run { cont.resume(throwing: CancellationError()) }
                default: break
                }
            }
            l.start(queue: queue)
        }
    }

    private func accept(_ conn: NWConnection) {
        Task { @MainActor [self] in
            guard let request = currentRequest() else {
                recordError(.hubUnreachable)
                conn.cancel() // the group is gone: nothing to carry the bytes to
                return
            }
            #if DEBUG
            dialedForTests?(request)
            #endif
            let ws = session.webSocketTask(with: request)
            ws.maximumMessageSize = 1 << 20
            Pump(conn: conn, ws: ws, forward: self, queue: queue).start()
        }
    }
}

/// Observes WebSocket handshake success or failure while preserving `HubRedirects` (Cloudflare Access + DevMode metrics).
private nonisolated final class PortWebSocketDelegate: HubRedirects, URLSessionWebSocketDelegate, @unchecked Sendable {
    private let forward: PortForward
    private let onHandshakeFailure: @Sendable () -> Void
    private let state = OSAllocatedUnfairLock(initialState: (opened: false, reported: false))

    init(forward: PortForward, onHandshakeFailure: @escaping @Sendable () -> Void) {
        self.forward = forward
        self.onHandshakeFailure = onHandshakeFailure
        super.init()
    }

    var didOpen: Bool { state.withLock { $0.opened } }

    func markOpened() {
        let wasOpened = state.withLock { s -> Bool in
            let prev = s.opened
            s.opened = true
            return prev
        }
        if !wasOpened { forward.clearError() }
    }

    func reportFailure(response: URLResponse?) {
        let shouldReport = state.withLock { s -> Bool in
            guard !s.opened && !s.reported else { return false }
            s.reported = true
            return true
        }
        guard shouldReport else { return }
        if let http = response as? HTTPURLResponse, http.statusCode >= 400 {
            forward.recordError(PortForwardError.from(statusCode: http.statusCode, port: forward.key.port))
        } else {
            forward.recordError(.hubUnreachable)
        }
        onHandshakeFailure()
    }

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didOpenWithProtocol protocol: String?) {
        markOpened()
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: (any Error)?) {
        guard error != nil else { return }
        reportFailure(response: task.response)
    }
}

/// One browser connection and its WebSocket to the hub, each read only once the last bytes went out the other side.
nonisolated private final class Pump: @unchecked Sendable {
    let conn: NWConnection
    let ws: URLSessionWebSocketTask
    let forward: PortForward
    let queue: DispatchQueue
    private let done = RunOnce()
    private var delegate: PortWebSocketDelegate?

    init(conn: NWConnection, ws: URLSessionWebSocketTask, forward: PortForward, queue: DispatchQueue) {
        self.conn = conn
        self.ws = ws
        self.forward = forward
        self.queue = queue
    }

    func start() {
        let del = PortWebSocketDelegate(forward: forward) { [self] in
            finishLocal()
        }
        delegate = del
        ws.delegate = del
        conn.stateUpdateHandler = { @Sendable [self] state in
            if case .failed = state { close() }
            if case .cancelled = state { close() }
        }
        conn.start(queue: queue)
        ws.resume()
        readLocal()
        readRemote()
    }

    private func readLocal() {
        conn.receive(minimumIncompleteLength: 1, maximumLength: 64 << 10) { @Sendable [self] data, _, complete, error in
            if let data, !data.isEmpty {
                ws.send(.data(data)) { @Sendable [self] err in
                    if err != nil {
                        close()
                    } else if complete {
                        // Half-close: client finished writing; keep pumping remote bytes until the server ends.
                    } else if error != nil {
                        close()
                    } else {
                        readLocal()
                    }
                }
            } else if error != nil {
                close()
            } else if complete {
                // Half-close with no remaining bytes: stop reading local, wait for remote response to finish.
            } else {
                readLocal()
            }
        }
    }

    private func readRemote() {
        ws.receive { @Sendable [self] result in
            let data: Data
            switch result {
            case .success(.data(let d)):
                delegate?.markOpened()
                data = d
            case .success(.string(let s)):
                delegate?.markOpened()
                data = Data(s.utf8)
            case .success:
                delegate?.markOpened()
                data = Data()
            case .failure:
                if delegate?.didOpen == false {
                    if let http = ws.response as? HTTPURLResponse, http.statusCode >= 400 {
                        delegate?.reportFailure(response: http)
                    } else {
                        // Give urlSession(_:task:didCompleteWithError:) a brief moment to deliver the HTTP status code
                        // before closing the local socket, so the sheet reads the exact handshake reason.
                        queue.asyncAfter(deadline: .now() + .milliseconds(80)) { [self] in
                            delegate?.reportFailure(response: ws.response)
                        }
                    }
                    return
                }
                // The machine's side ended: what was sent before is already written, then the browser sees the end.
                finishLocal()
                return
            }
            conn.send(content: data, completion: .contentProcessed { @Sendable [self] err in
                if err != nil { close() } else { readRemote() }
            })
        }
    }

    private func finishLocal() {
        conn.send(content: nil, contentContext: .finalMessage, isComplete: true, completion: .contentProcessed { @Sendable [self] _ in
            close()
        })
    }

    private func close() {
        done.run {
            ws.cancel(with: .normalClosure, reason: nil)
            conn.cancel()
        }
    }
}

/// Runs its block the first time only, from any thread.
nonisolated final class RunOnce: @unchecked Sendable {
    private let lock = NSLock()
    private var ran = false

    func run(_ f: () -> Void) {
        lock.lock()
        let first = !ran
        ran = true
        lock.unlock()
        if first { f() }
    }
}
