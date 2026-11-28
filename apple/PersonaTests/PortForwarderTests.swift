import CryptoKit
import Foundation
import Network
import Testing
import WebKit
@testable import Persona
import PersonaWire

/// A forwarded port follows the group's hub (#1886): each browser connection dials the hub, route and token the group has
/// at that moment, not the ones the forward started with, and a group that goes takes its forwards with it. The forwards
/// listen on the loopback for real; the hubs are `.invalid` addresses, so nothing leaves the machine.
@MainActor
@Suite(.serialized)
struct PortForwarderTests {
    /// What the group's link would answer with: the test changes it as the app would on a switch.
    final class Live {
        var config: ServerConfig?
        init(_ config: ServerConfig?) { self.config = config }
    }

    let hubA = URL(string: "https://forward-a.invalid/persona")!
    let hubB = URL(string: "http://forward-b.invalid:18660")!
    let tunnel = URL(string: "http://127.0.0.1:52186")!

    /// Opens one browser connection to the forward and returns the request it dialed the hub with.
    private func dial(_ forward: PortForward) async throws -> URLRequest {
        let port = try #require(NWEndpoint.Port(rawValue: UInt16(forward.localPort)))
        let conn = NWConnection(host: "127.0.0.1", port: port, using: .tcp)
        defer {
            conn.cancel()
            forward.dialedForTests = nil
        }
        return try await withCheckedThrowingContinuation { (cont: CheckedContinuation<URLRequest, Error>) in
            let once = RunOnce()
            forward.dialedForTests = { req in once.run { cont.resume(returning: req) } }
            conn.start(queue: .global())
            Task {
                try? await Task.sleep(for: .seconds(10))
                once.run { cont.resume(throwing: URLError(.timedOut)) }
            }
        }
    }

    @Test func eachConnectionDialsTheGroupsCurrentHubRouteAndToken() async throws {
        defer { HubTransport.route(hubA, via: nil) }
        let forwarder = PortForwarder()
        defer { forwarder.closeAll() }
        let live = Live(ServerConfig(url: hubA, token: "fixture-token-one"))
        let key = PortForwarder.Key(group: "g1", machine: "example-node", port: 41886)
        let port = try await forwarder.localPort(key) { live.config }
        let forward = try #require(forwarder.forward(key))

        // Direct, as the forward was opened.
        var req = try await dial(forward)
        #expect(req.url?.absoluteString == "wss://forward-a.invalid/persona/api/v1/machines/example-node/ports/41886/connect")
        #expect(req.value(forHTTPHeaderField: "Authorization") == "Bearer fixture-token-one")

        // The direct address is lost and the app goes through the tunnel: the next connection takes the tunnel.
        HubTransport.route(hubA, via: tunnel)
        req = try await dial(forward)
        #expect(req.url?.absoluteString == "ws://127.0.0.1:52186/persona/api/v1/machines/example-node/ports/41886/connect")

        // The tunnel closes once the hub answers directly again: straight to the hub again, not to the closed tunnel.
        HubTransport.unroute(hubA, from: tunnel)
        req = try await dial(forward)
        #expect(req.url?.host() == "forward-a.invalid")

        // The group's socket moves to another hub, then the device pairs again with a new token.
        live.config = ServerConfig(url: hubB, token: "fixture-token-two")
        req = try await dial(forward)
        #expect(req.url?.absoluteString == "ws://forward-b.invalid:18660/api/v1/machines/example-node/ports/41886/connect")
        #expect(req.value(forHTTPHeaderField: "Authorization") == "Bearer fixture-token-two")

        // Tapping the link again keeps the page's port; the forward is the same one and still follows.
        #expect(try await forwarder.localPort(key) { live.config } == port)
        #expect(forwarder.forward(key) === forward)

        // The group is gone: nothing to dial.
        live.config = nil
        #expect(forward.currentRequest() == nil)
    }

    @Test func askingAgainReplacesWhereTheForwardLooksForItsHub() async throws {
        let forwarder = PortForwarder()
        defer { forwarder.closeAll() }
        let key = PortForwarder.Key(group: "g1", machine: "example-node", port: 0)
        _ = try await forwarder.localPort(key) { ServerConfig(url: hubA, token: "fixture-token-one") }
        _ = try await forwarder.localPort(key) { ServerConfig(url: hubB, token: "fixture-token-two") }
        let req = try #require(forwarder.forward(key)?.currentRequest())
        #expect(req.url?.host() == "forward-b.invalid")
        #expect(req.value(forHTTPHeaderField: "Authorization") == "Bearer fixture-token-two")
    }

    @Test func machinesWithOneIdInTwoGroupsGetAForwardEach() async throws {
        let forwarder = PortForwarder()
        defer { forwarder.closeAll() }
        let a = PortForwarder.Key(group: "g1", machine: "example-node", port: 0)
        let b = PortForwarder.Key(group: "g2", machine: "example-node", port: 0)
        _ = try await forwarder.localPort(a) { ServerConfig(url: hubA, token: "fixture-token-one") }
        _ = try await forwarder.localPort(b) { ServerConfig(url: hubB, token: "fixture-token-two") }
        let fa = try #require(forwarder.forward(a))
        let fb = try #require(forwarder.forward(b))
        #expect(fa !== fb && fa.localPort != fb.localPort)
        #expect(fa.currentRequest()?.url?.host() == "forward-a.invalid")
        #expect(fb.currentRequest()?.url?.host() == "forward-b.invalid")

        // Disconnecting one group stops its forwards only.
        forwarder.close(groups: ["g1"])
        #expect(forwarder.forward(a) == nil && !fa.isReady)
        #expect(forwarder.forward(b) === fb && fb.isReady)

        // Signing out of everything stops the rest.
        forwarder.closeAll()
        #expect(forwarder.forward(b) == nil && !fb.isReady)
    }

    @Test func sessionConnectionLimitIsAtLeastThirtyTwoAndDoesNotQueueAtFour() async throws {
        let hub = try await LocalMockHubWS(mode: .echo, handshakeDelayMs: 600)
        defer { hub.stop() }
        let forwarder = PortForwarder()
        defer { forwarder.closeAll() }
        let key = PortForwarder.Key(group: "g1", machine: "example-node", port: 41887)
        let localPort = try await forwarder.localPort(key) { ServerConfig(url: hub.baseURL, token: "fixture-token") }
        let forward = try #require(forwarder.forward(key))

        let session = try #require(Mirror(reflecting: forward).children.first { $0.label == "session" }?.value as? URLSession)
        let limit = session.configuration.httpMaximumConnectionsPerHost
        #expect(limit >= 32, "httpMaximumConnectionsPerHost is \(limit); expected >= 32 (64) to avoid 4-connection SPA deadlock")
        #expect(forward.sessionConfiguration.httpMaximumConnectionsPerHost == PortForward.maxConnectionsPerHost)
        #expect(PortForward.maxConnectionsPerHost == 64)

        // Open 8 simultaneous connections while handshakes take 600ms each: with a 4-connection cap,
        // URLSession holds connections 5..8 in queue (peakInFlight <= 4) instead of dialing them together.
        let count = 8
        let connections = (0..<count).map { _ in
            NWConnection(host: "127.0.0.1", port: NWEndpoint.Port(rawValue: UInt16(localPort))!, using: .tcp)
        }
        defer { connections.forEach { $0.cancel() } }

        let echoed = await withTaskGroup(of: Bool.self) { group in
            for (i, conn) in connections.enumerated() {
                group.addTask {
                    await Self.pingEcho(conn, payload: Data("ping-\(i)".utf8), timeout: .seconds(5))
                }
            }
            var ok = 0
            for await hit in group where hit { ok += 1 }
            return ok
        }
        #expect(echoed == count, "only \(echoed)/\(count) concurrent connections succeeded")
        #expect(hub.peakHandshakeCount >= 6, "URLSession serialized WebSocket handshakes at \(hub.peakHandshakeCount) concurrent (expected >= 6)")
    }

    @Test func ipv6PortConflictPicksDualStackFreePort() async throws {
        // Hold ::1 on an ephemeral port while leaving 127.0.0.1 on that same port free.
        let v6Params = NWParameters.tcp
        v6Params.requiredLocalEndpoint = .hostPort(host: "::1", port: .any)
        let blocker = try NWListener(using: v6Params)
        let q = DispatchQueue(label: "test.v6.blocker")
        blocker.newConnectionHandler = { @Sendable conn in conn.cancel() }
        let blockedPort: Int = try await withCheckedThrowingContinuation { cont in
            let once = RunOnce()
            blocker.stateUpdateHandler = { @Sendable state in
                switch state {
                case .ready: once.run { cont.resume(returning: Int(blocker.port?.rawValue ?? 0)) }
                case .failed(let e), .waiting(let e): once.run { cont.resume(throwing: e) }; blocker.cancel()
                case .cancelled: once.run { cont.resume(throwing: CancellationError()) }
                default: break
                }
            }
            blocker.start(queue: q)
        }
        defer { blocker.cancel() }

        let forwarder = PortForwarder()
        defer { forwarder.closeAll() }
        let key = PortForwarder.Key(group: "g1", machine: "example-node", port: blockedPort)
        let chosen = try await forwarder.localPort(key) { ServerConfig(url: hubA, token: "fixture-token") }
        #expect(chosen != blockedPort, "PortForward silently ignored ::1 conflict on port \(blockedPort) instead of choosing a port free on both 127.0.0.1 and ::1")
    }

    @Test func clientHalfCloseDoesNotTruncateRemoteResponse() async throws {
        let expected = Data("HTTP/1.1 200 OK\r\nContent-Length: 16\r\nConnection: close\r\n\r\nhalf-close-alive".utf8)
        let hub = try await LocalMockHubWS(mode: .delayedReply(delayMs: 250, body: expected))
        defer { hub.stop() }
        let forwarder = PortForwarder()
        defer { forwarder.closeAll() }
        let key = PortForwarder.Key(group: "g1", machine: "example-node", port: 41888)
        let localPort = try await forwarder.localPort(key) { ServerConfig(url: hub.baseURL, token: "fixture-token") }

        let conn = NWConnection(host: "127.0.0.1", port: NWEndpoint.Port(rawValue: UInt16(localPort))!, using: .tcp)
        defer { conn.cancel() }
        let received = try await Self.sendHalfCloseAndReadAll(
            conn,
            request: Data("GET /slow HTTP/1.1\r\nHost: localhost\r\nConnection: close\r\n\r\n".utf8),
            timeout: .seconds(5)
        )
        let text = String(decoding: received, as: UTF8.self)
        #expect(text.contains("half-close-alive"), "expected full response after client half-close, got \(received.count) bytes: \(text)")
    }

    @Test func multiModulePageWithHMRRendersInWKWebViewWithoutBlankScreen() async throws {
        let dev = try await LocalMultiModuleDevServer()
        defer { dev.stop() }
        // Mirror URLSession's connection-slot ceiling on the hub side using the forward's real
        // httpMaximumConnectionsPerHost so the test reproduces the exact iOS 4-slot keep-alive + HMR
        // deadlock before the fix and verifies <2 s rendering at 64 connections after the fix.
        let forwarder = PortForwarder()
        defer { forwarder.closeAll() }
        let hub = try await LocalMockHubWS(mode: .bridge(targetPort: dev.port))
        defer { hub.stop() }
        let key = PortForwarder.Key(group: "g1", machine: "example-node", port: dev.port)
        let localPort = try await forwarder.localPort(key) { ServerConfig(url: hub.baseURL, token: "fixture-token") }
        let forward = try #require(forwarder.forward(key))
        let session = try #require(Mirror(reflecting: forward).children.first { $0.label == "session" }?.value as? URLSession)
        hub.setMaxActiveStreams(session.configuration.httpMaximumConnectionsPerHost)

        // Warm up WebContent process once so the 2 s assertion measures page loading, not cold process spawn.
        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        let web = WKWebView(frame: CGRect(x: 0, y: 0, width: 390, height: 844), configuration: config)
        web.loadHTMLString("<!doctype html><html><body>warm</body></html>", baseURL: nil)
        for _ in 0..<60 {
            if let ready = try? await web.evaluateJavaScript("document.body?.textContent || ''") as? String, ready == "warm" { break }
            try await Task.sleep(for: .milliseconds(50))
        }

        var req = URLRequest(url: URL(string: "http://localhost:\(localPort)/")!)
        req.cachePolicy = .reloadIgnoringLocalCacheData
        let started = ContinuousClock.now
        web.load(req)

        var text = ""
        for _ in 0..<60 { // 3 seconds max: fixed code mounts well under 2 s; 4-slot cap deadlocks #root blank
            if let value = try? await web.evaluateJavaScript("document.getElementById('root')?.textContent || ''") as? String,
               value.contains("modules=12"), value.contains("hmr=open") {
                text = value
                break
            }
            try await Task.sleep(for: .milliseconds(50))
        }
        let elapsed = ContinuousClock.now - started
        #expect(text.contains("modules=12") && text.contains("hmr=open"),
                "WKWebView rendered blank screen after \(elapsed) with httpMaximumConnectionsPerHost=\(session.configuration.httpMaximumConnectionsPerHost); #root='\(text)', peakWS=\(hub.peakActive), queuedWS=\(hub.queuedCount)")
        #expect(elapsed < .seconds(2), "multi-module page took too long to mount: \(elapsed)")
    }

    @Test func ensureReadyRebuildsCancelledListenersAndRestoresForwarding() async throws {
        let hub = try await LocalMockHubWS(mode: .echo)
        defer { hub.stop() }
        let forwarder = PortForwarder()
        defer { forwarder.closeAll() }
        let key = PortForwarder.Key(group: "g1", machine: "example-node", port: 41889)
        let firstPort = try await forwarder.localPort(key) { ServerConfig(url: hub.baseURL, token: "fixture-token") }
        let forward = try #require(forwarder.forward(key))
        #expect(forward.isReady)

        // Simulate iOS suspending/cancelling listeners in the background.
        forward.cancelListenersForTests()
        for _ in 0..<40 where forward.isReady { try await Task.sleep(for: .milliseconds(25)) }
        #expect(!forward.isReady, "listener should report not ready after cancellation")

        let rebuiltPort = try await forwarder.ensureReady(key)
        #expect(forward.isReady, "ensureReady should rebuild invalidated listeners")
        #expect(rebuiltPort == firstPort || rebuiltPort > 0)

        let conn = NWConnection(host: "127.0.0.1", port: NWEndpoint.Port(rawValue: UInt16(rebuiltPort))!, using: .tcp)
        defer { conn.cancel() }
        let ok = await Self.pingEcho(conn, payload: Data("rebuilt".utf8), timeout: .seconds(4))
        #expect(ok, "rebuilt listener should forward traffic again")
    }

    @Test func handshakeErrorsAreRecordedAndSurfacedInSelectableWebView() async throws {
        // Verify status code mapping for every documented hub failure code.
        #expect(PortForwardError.from(statusCode: 401, port: 5173) == .unauthorized)
        #expect(PortForwardError.from(statusCode: 403, port: 5173) == .forbidden)
        #expect(PortForwardError.from(statusCode: 404, port: 5173) == .portGone(5173))
        #expect(PortForwardError.from(statusCode: 429, port: 5173) == .tooManyConnections)
        #expect(PortForwardError.from(statusCode: 502, port: 5173) == .serverUnreachable(502))
        #expect(PortForwardError.from(statusCode: 503, port: 5173) == .computerOffline)
        #expect(PortForwardError.from(statusCode: 504, port: 5173) == .serverUnreachable(504))

        // Live 404 handshake failure through PortForwarder.shared + SelectableWebView sets loadError instead of blank screen.
        let hub = try await LocalMockHubWS(mode: .reject(status: 404, reason: "port_gone"))
        defer { hub.stop() }
        let key = PortForwarder.Key(group: "test-404-\(UUID().uuidString)", machine: "example-node", port: 41890)
        defer { PortForwarder.shared.close(groups: [key.group]) }
        let localPort = try await PortForwarder.shared.localPort(key) { ServerConfig(url: hub.baseURL, token: "fixture-token") }
        let url = try #require(URL(string: "http://localhost:\(localPort)/"))

        let config = WKWebViewConfiguration()
        config.websiteDataStore = .nonPersistent()
        let web = SelectableWebView(frame: CGRect(x: 0, y: 0, width: 390, height: 844), configuration: config)
        web.pageState.initialURL = url
        web.pageState.isPresented = true
        var req = URLRequest(url: url)
        req.cachePolicy = .reloadIgnoringLocalCacheData
        web.load(req)

        for _ in 0..<80 where web.pageState.loadError == nil {
            try await Task.sleep(for: .milliseconds(50))
        }
        #expect(PortForwarder.shared.lastError(for: url) == .portGone(41890))
        let pageError = try #require(web.pageState.loadError, "SelectableWebView should populate loadError on 404 handshake failure")
        #expect(pageError == .from(portError: .portGone(41890)))
    }

    /// Sends a single payload on `conn`, keeps `conn` open, and waits for the exact echo back.
    private nonisolated static func pingEcho(_ conn: NWConnection, payload: Data, timeout: Duration) async -> Bool {
        await withCheckedContinuation { cont in
            let once = RunOnce()
            let queue = DispatchQueue(label: "test.port.ping")
            conn.stateUpdateHandler = { @Sendable state in
                switch state {
                case .ready:
                    conn.send(content: payload, completion: .contentProcessed { @Sendable err in
                        if err != nil { once.run { cont.resume(returning: false) }; return }
                        conn.receive(minimumIncompleteLength: payload.count, maximumLength: 1024) { @Sendable data, _, _, err in
                            once.run { cont.resume(returning: err == nil && data == payload) }
                        }
                    })
                case .failed, .cancelled:
                    once.run { cont.resume(returning: false) }
                default: break
                }
            }
            conn.start(queue: queue)
            Task {
                try? await Task.sleep(for: timeout)
                once.run { cont.resume(returning: false) }
            }
        }
    }

    /// Sends `request` with TCP FIN (`isComplete: true`, client half-close) and reads until remote EOF.
    private nonisolated static func sendHalfCloseAndReadAll(_ conn: NWConnection, request: Data, timeout: Duration) async throws -> Data {
        try await withCheckedThrowingContinuation { cont in
            let once = RunOnce()
            let queue = DispatchQueue(label: "test.port.halfclose")
            let box = DataBox()
            conn.stateUpdateHandler = { @Sendable state in
                switch state {
                case .ready:
                    conn.send(content: request, contentContext: .finalMessage, isComplete: true, completion: .contentProcessed { @Sendable err in
                        if let err { once.run { cont.resume(throwing: err) }; return }
                        Self.readAllUntilEOF(conn, box: box) { once.run { cont.resume(returning: box.snapshot()) } }
                    })
                case .failed(let e): once.run { cont.resume(throwing: e) }
                case .cancelled: once.run { cont.resume(returning: box.snapshot()) }
                default: break
                }
            }
            conn.start(queue: queue)
            Task {
                try? await Task.sleep(for: timeout)
                once.run { cont.resume(returning: box.snapshot()) }
            }
        }
    }

    private nonisolated static func readAllUntilEOF(_ conn: NWConnection, box: DataBox, done: @escaping @Sendable () -> Void) {
        conn.receive(minimumIncompleteLength: 1, maximumLength: 65536) { @Sendable data, _, complete, error in
            if let data, !data.isEmpty { box.append(data) }
            if complete || error != nil {
                done()
            } else {
                Self.readAllUntilEOF(conn, box: box, done: done)
            }
        }
    }
}

private nonisolated final class DataBox: @unchecked Sendable {
    private let lock = NSLock()
    private var data = Data()
    func append(_ chunk: Data) { lock.lock(); data.append(chunk); lock.unlock() }
    func snapshot() -> Data { lock.lock(); defer { lock.unlock() }; return data }
}

/// Minimal RFC 6455 WebSocket mock hub on 127.0.0.1:0 for PortForwarder unit tests.
private nonisolated final class LocalMockHubWS: @unchecked Sendable {
    enum Mode: Sendable {
        case echo
        case delayedReply(delayMs: Int, body: Data)
        case bridge(targetPort: Int)
        case reject(status: Int, reason: String)
    }

    private let listener: NWListener
    private let queue = DispatchQueue(label: "test.mockhub.ws")
    private let mode: Mode
    private let handshakeDelayMs: Int
    private let lock = NSLock()
    private var active = 0
    private var peak = 0
    private var accepted = 0
    private var inFlightHandshakes = 0
    private var peakHandshakes = 0
    private var maxActiveStreams = 0
    private var queued = 0
    private var waitQueue: [@Sendable () -> Void] = []
    private(set) var port: Int = 0
    var baseURL: URL { URL(string: "http://127.0.0.1:\(port)")! }
    var peakActive: Int { lock.lock(); defer { lock.unlock() }; return peak }
    var acceptedCount: Int { lock.lock(); defer { lock.unlock() }; return accepted }
    var peakHandshakeCount: Int { lock.lock(); defer { lock.unlock() }; return peakHandshakes }
    var queuedCount: Int { lock.lock(); defer { lock.unlock() }; return queued }

    func setMaxActiveStreams(_ limit: Int) {
        lock.lock()
        maxActiveStreams = limit
        lock.unlock()
    }

    init(mode: Mode, handshakeDelayMs: Int = 0) async throws {
        self.mode = mode
        self.handshakeDelayMs = handshakeDelayMs
        let params = NWParameters.tcp
        params.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: .any)
        let l = try NWListener(using: params)
        self.listener = l
        let q = queue
        l.newConnectionHandler = { @Sendable [weak self] conn in self?.handle(conn) }
        self.port = try await withCheckedThrowingContinuation { cont in
            let once = RunOnce()
            l.stateUpdateHandler = { @Sendable state in
                switch state {
                case .ready: once.run { cont.resume(returning: Int(l.port?.rawValue ?? 0)) }
                case .failed(let e), .waiting(let e): once.run { cont.resume(throwing: e) }; l.cancel()
                case .cancelled: once.run { cont.resume(throwing: CancellationError()) }
                default: break
                }
            }
            l.start(queue: q)
        }
    }

    func stop() { listener.cancel() }

    private func acquireStreamSlot(_ run: @escaping @Sendable () -> Void) {
        lock.lock()
        if maxActiveStreams > 0 && active >= maxActiveStreams {
            queued += 1
            waitQueue.append(run)
            lock.unlock()
            return
        }
        accepted += 1
        active += 1
        if active > peak { peak = active }
        lock.unlock()
        run()
    }

    private func decActive() {
        var next: (@Sendable () -> Void)?
        lock.lock()
        active = max(0, active - 1)
        if !waitQueue.isEmpty && (maxActiveStreams == 0 || active < maxActiveStreams) {
            accepted += 1
            active += 1
            if active > peak { peak = active }
            next = waitQueue.removeFirst()
        }
        lock.unlock()
        next?()
    }

    private func handle(_ conn: NWConnection) {
        conn.start(queue: queue)
        readHeaders(conn, buffer: Data()) { [weak self] headerText in
            guard let self, let headerText else { conn.cancel(); return }
            self.lock.lock()
            self.inFlightHandshakes += 1
            if self.inFlightHandshakes > self.peakHandshakes { self.peakHandshakes = self.inFlightHandshakes }
            self.lock.unlock()
            let proceed: @Sendable () -> Void = { [weak self] in
                guard let self else { conn.cancel(); return }
                self.lock.lock()
                self.inFlightHandshakes = max(0, self.inFlightHandshakes - 1)
                self.lock.unlock()
                if case .reject(let status, let reason) = self.mode {
                    let body = Data("{\"error\":\"\(reason)\"}".utf8)
                    let resp = "HTTP/1.1 \(status) \(reason)\r\nContent-Type: application/json\r\nContent-Length: \(body.count)\r\nConnection: close\r\n\r\n"
                    conn.send(content: Data(resp.utf8) + body, contentContext: .finalMessage, isComplete: true, completion: .contentProcessed { _ in conn.cancel() })
                    return
                }
                guard let key = Self.headerValue("Sec-WebSocket-Key", in: headerText) else {
                    conn.cancel()
                    return
                }
                self.acquireStreamSlot { [weak self] in
                    guard let self else { conn.cancel(); return }
                    let accept = Self.wsAccept(key)
                    let upgrade = "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: \(accept)\r\n\r\n"
                    conn.send(content: Data(upgrade.utf8), completion: .contentProcessed { @Sendable [weak self] err in
                        guard let self, err == nil else { self?.decActive(); conn.cancel(); return }
                        switch self.mode {
                        case .echo:
                            self.pumpEcho(conn, buffer: Data())
                        case .delayedReply(let delayMs, let body):
                            self.pumpDelayed(conn, buffer: Data(), delayMs: delayMs, body: body)
                        case .bridge(let targetPort):
                            self.pumpBridge(conn, targetPort: targetPort)
                        case .reject:
                            self.decActive(); conn.cancel()
                        }
                    })
                }
            }
            if self.handshakeDelayMs > 0 {
                self.queue.asyncAfter(deadline: .now() + .milliseconds(self.handshakeDelayMs), execute: proceed)
            } else {
                proceed()
            }
        }
    }

    private func readHeaders(_ conn: NWConnection, buffer: Data, done: @escaping @Sendable (String?) -> Void) {
        conn.receive(minimumIncompleteLength: 1, maximumLength: 8192) { @Sendable [weak self] data, _, complete, error in
            guard let self else { done(nil); return }
            var buf = buffer
            if let data { buf.append(data) }
            if let range = buf.range(of: Data("\r\n\r\n".utf8)) {
                done(String(decoding: buf[..<range.lowerBound], as: UTF8.self))
                return
            }
            if complete || error != nil { done(nil); return }
            self.readHeaders(conn, buffer: buf, done: done)
        }
    }

    private func pumpEcho(_ conn: NWConnection, buffer: Data) {
        conn.receive(minimumIncompleteLength: 1, maximumLength: 65536) { @Sendable [weak self] data, _, complete, error in
            guard let self else { return }
            var buf = buffer
            if let data { buf.append(data) }
            while let frame = Self.parseClientFrame(&buf) {
                if frame.opcode == 8 { self.decActive(); conn.cancel(); return }
                if frame.opcode == 9 { conn.send(content: Self.encodeServerFrame(opcode: 0xA, payload: frame.payload), completion: .idempotent); continue }
                if frame.opcode == 1 || frame.opcode == 2 {
                    conn.send(content: Self.encodeServerFrame(opcode: 2, payload: frame.payload), completion: .idempotent)
                }
            }
            if complete || error != nil { self.decActive(); conn.cancel(); return }
            self.pumpEcho(conn, buffer: buf)
        }
    }

    private func pumpDelayed(_ conn: NWConnection, buffer: Data, delayMs: Int, body: Data) {
        conn.receive(minimumIncompleteLength: 1, maximumLength: 65536) { @Sendable [weak self] data, _, complete, error in
            guard let self else { return }
            var buf = buffer
            if let data { buf.append(data) }
            while let frame = Self.parseClientFrame(&buf) {
                if frame.opcode == 8 { self.decActive(); conn.cancel(); return }
                if frame.opcode == 1 || frame.opcode == 2 {
                    self.queue.asyncAfter(deadline: .now() + .milliseconds(delayMs)) {
                        let out = Self.encodeServerFrame(opcode: 2, payload: body) + Self.encodeServerFrame(opcode: 8, payload: Data([0x03, 0xE8]))
                        conn.send(content: out, contentContext: .finalMessage, isComplete: true, completion: .contentProcessed { _ in
                            self.decActive()
                            conn.cancel()
                        })
                    }
                    return
                }
            }
            if complete || error != nil { self.decActive(); conn.cancel(); return }
            self.pumpDelayed(conn, buffer: buf, delayMs: delayMs, body: body)
        }
    }

    private func pumpBridge(_ wsConn: NWConnection, targetPort: Int) {
        let tcp = NWConnection(host: "127.0.0.1", port: NWEndpoint.Port(rawValue: UInt16(targetPort))!, using: .tcp)
        let closed = RunOnce()
        let finish: @Sendable () -> Void = { [weak self] in
            closed.run {
                self?.decActive()
                wsConn.cancel()
                tcp.cancel()
            }
        }
        tcp.stateUpdateHandler = { @Sendable state in
            switch state {
            case .ready:
                Self.bridgeReadTCP(tcp, wsConn: wsConn, finish: finish)
                Self.bridgeReadWS(wsConn, tcp: tcp, buffer: Data(), finish: finish)
            case .failed, .cancelled:
                finish()
            default: break
            }
        }
        tcp.start(queue: queue)
    }

    private static func bridgeReadTCP(_ tcp: NWConnection, wsConn: NWConnection, finish: @escaping @Sendable () -> Void) {
        tcp.receive(minimumIncompleteLength: 1, maximumLength: 65536) { @Sendable data, _, complete, error in
            if let data, !data.isEmpty {
                wsConn.send(content: Self.encodeServerFrame(opcode: 2, payload: data), completion: .contentProcessed { @Sendable err in
                    if err != nil { finish(); return }
                    if complete || error != nil {
                        wsConn.send(content: Self.encodeServerFrame(opcode: 8, payload: Data([0x03, 0xE8])), completion: .contentProcessed { _ in finish() })
                    } else {
                        Self.bridgeReadTCP(tcp, wsConn: wsConn, finish: finish)
                    }
                })
            } else if complete || error != nil {
                wsConn.send(content: Self.encodeServerFrame(opcode: 8, payload: Data([0x03, 0xE8])), completion: .contentProcessed { _ in finish() })
            } else {
                Self.bridgeReadTCP(tcp, wsConn: wsConn, finish: finish)
            }
        }
    }

    private static func bridgeReadWS(_ wsConn: NWConnection, tcp: NWConnection, buffer: Data, finish: @escaping @Sendable () -> Void) {
        wsConn.receive(minimumIncompleteLength: 1, maximumLength: 65536) { @Sendable data, _, complete, error in
            var buf = buffer
            if let data { buf.append(data) }
            while let frame = Self.parseClientFrame(&buf) {
                if frame.opcode == 8 { finish(); return }
                if frame.opcode == 9 { wsConn.send(content: Self.encodeServerFrame(opcode: 0xA, payload: frame.payload), completion: .idempotent); continue }
                if frame.opcode == 1 || frame.opcode == 2 {
                    tcp.send(content: frame.payload, completion: .idempotent)
                }
            }
            if complete || error != nil { finish(); return }
            Self.bridgeReadWS(wsConn, tcp: tcp, buffer: buf, finish: finish)
        }
    }

    static func headerValue(_ name: String, in headers: String) -> String? {
        let needle = name.lowercased() + ":"
        for line in headers.split(separator: "\r\n") {
            if line.lowercased().hasPrefix(needle) {
                return line.dropFirst(needle.count).trimmingCharacters(in: .whitespaces)
            }
        }
        return nil
    }

    static func wsAccept(_ key: String) -> String {
        let digest = Insecure.SHA1.hash(data: Data((key + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11").utf8))
        return Data(digest).base64EncodedString()
    }

    static func parseClientFrame(_ buf: inout Data) -> (opcode: UInt8, payload: Data)? {
        guard buf.count >= 2 else { return nil }
        let b0 = buf[buf.startIndex]
        let b1 = buf[buf.startIndex + 1]
        let opcode = b0 & 0x0F
        let masked = (b1 & 0x80) != 0
        var idx = buf.startIndex + 2
        var length = Int(b1 & 0x7F)
        if length == 126 {
            guard buf.count >= 4 else { return nil }
            length = (Int(buf[idx]) << 8) | Int(buf[idx + 1])
            idx += 2
        } else if length == 127 {
            guard buf.count >= 10 else { return nil }
            length = 0
            for i in 0..<8 { length = (length << 8) | Int(buf[idx + i]) }
            idx += 8
        }
        let maskLen = masked ? 4 : 0
        let end = idx + maskLen + length
        guard buf.endIndex >= end else { return nil }
        let mask = masked ? Array(buf[idx..<(idx + 4)]) : [0, 0, 0, 0]
        let payloadStart = idx + maskLen
        var payload = Data(capacity: length)
        for i in 0..<length {
            payload.append(buf[payloadStart + i] ^ mask[i % 4])
        }
        buf.removeFirst(end - buf.startIndex)
        return (opcode, payload)
    }

    static func encodeServerFrame(opcode: UInt8, payload: Data) -> Data {
        var out = Data([0x80 | opcode])
        let n = payload.count
        if n < 126 {
            out.append(UInt8(n))
        } else if n < 65536 {
            out.append(126)
            out.append(UInt8((n >> 8) & 0xFF))
            out.append(UInt8(n & 0xFF))
        } else {
            out.append(127)
            for shift in stride(from: 56, through: 0, by: -8) { out.append(UInt8((n >> shift) & 0xFF)) }
        }
        out.append(payload)
        return out
    }
}

/// HTTP/1.1 keep-alive dev server serving 12 external ES modules/CSS + 1 HMR WebSocket for WKWebView blank-screen tests.
private nonisolated final class LocalMultiModuleDevServer: @unchecked Sendable {
    private let listener: NWListener
    private let queue = DispatchQueue(label: "test.devserver")
    private(set) var port: Int = 0

    init() async throws {
        let params = NWParameters.tcp
        params.requiredLocalEndpoint = .hostPort(host: "127.0.0.1", port: .any)
        let l = try NWListener(using: params)
        self.listener = l
        let q = queue
        l.newConnectionHandler = { @Sendable [weak self] conn in self?.handle(conn) }
        self.port = try await withCheckedThrowingContinuation { cont in
            let once = RunOnce()
            l.stateUpdateHandler = { @Sendable state in
                switch state {
                case .ready: once.run { cont.resume(returning: Int(l.port?.rawValue ?? 0)) }
                case .failed(let e), .waiting(let e): once.run { cont.resume(throwing: e) }; l.cancel()
                case .cancelled: once.run { cont.resume(throwing: CancellationError()) }
                default: break
                }
            }
            l.start(queue: q)
        }
    }

    func stop() { listener.cancel() }

    private func handle(_ conn: NWConnection) {
        conn.start(queue: queue)
        readNextRequest(conn, buffer: Data())
    }

    private func readNextRequest(_ conn: NWConnection, buffer: Data) {
        conn.receive(minimumIncompleteLength: 1, maximumLength: 16384) { @Sendable [weak self] data, _, complete, error in
            guard let self else { return }
            var buf = buffer
            if let data { buf.append(data) }
            if let range = buf.range(of: Data("\r\n\r\n".utf8)) {
                let headerText = String(decoding: buf[..<range.lowerBound], as: UTF8.self)
                let rest = Data(buf[range.upperBound...])
                let firstLine = headerText.split(separator: "\r\n", maxSplits: 1).first ?? ""
                let parts = firstLine.split(separator: " ")
                let path = parts.count >= 2 ? String(parts[1]) : "/"
                if path == "/hmr", let wsKey = LocalMockHubWS.headerValue("Sec-WebSocket-Key", in: headerText) {
                    let accept = LocalMockHubWS.wsAccept(wsKey)
                    let upgrade = "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: \(accept)\r\n\r\n"
                    let hello = LocalMockHubWS.encodeServerFrame(opcode: 1, payload: Data("hmr-ok".utf8))
                    conn.send(content: Data(upgrade.utf8) + hello, completion: .contentProcessed { @Sendable err in
                        if err != nil { conn.cancel(); return }
                        self.holdHMR(conn)
                    })
                    return
                }
                let (contentType, body, delayMs) = Self.response(for: path)
                let sendReply: @Sendable () -> Void = {
                    let head = "HTTP/1.1 200 OK\r\nContent-Type: \(contentType)\r\nContent-Length: \(body.count)\r\nConnection: keep-alive\r\nCache-Control: no-store\r\n\r\n"
                    conn.send(content: Data(head.utf8) + body, completion: .contentProcessed { @Sendable err in
                        if err != nil { conn.cancel(); return }
                        self.readNextRequest(conn, buffer: rest)
                    })
                }
                if delayMs > 0 {
                    self.queue.asyncAfter(deadline: .now() + .milliseconds(delayMs), execute: sendReply)
                } else {
                    sendReply()
                }
                return
            }
            if complete || error != nil { conn.cancel(); return }
            self.readNextRequest(conn, buffer: buf)
        }
    }

    private func holdHMR(_ conn: NWConnection) {
        conn.receive(minimumIncompleteLength: 1, maximumLength: 4096) { @Sendable [weak self] _, _, complete, error in
            if complete || error != nil { conn.cancel(); return }
            self?.holdHMR(conn)
        }
    }

    private static func response(for path: String) -> (String, Data, Int) {
        if path == "/" {
            let html = """
            <!doctype html><html><head><meta charset="utf-8"><title>Multi Module App</title>
            <link rel="stylesheet" href="/style1.css">
            <link rel="stylesheet" href="/style2.css">
            </head><body><div id="root"></div>
            <script>
            window.__hmr = new Promise((resolve) => {
              const ws = new WebSocket((location.protocol === 'https:' ? 'wss://' : 'ws://') + location.host + '/hmr');
              window.__hmrWS = ws;
              ws.onmessage = (ev) => { if (ev.data === 'hmr-ok') resolve('open'); };
            });
            </script>
            <script type="module" src="/main.js"></script>
            </body></html>
            """
            return ("text/html; charset=utf-8", Data(html.utf8), 0)
        }
        if path == "/style1.css" { return ("text/css", Data("body { margin: 0; }".utf8), 100) }
        if path == "/style2.css" { return ("text/css", Data("#root { padding: 8px; }".utf8), 100) }
        if path == "/main.js" {
            let imports = (1...10).map { "import { v as v\($0) } from '/mod\($0).js';" }.joined(separator: "\n")
            let sum = (1...10).map { "v\($0)" }.joined(separator: " + ")
            let js = """
            \(imports)
            const totalMods = 10 + document.styleSheets.length;
            const checksum = \(sum);
            window.__hmr.then((status) => {
              if (status === 'open' && checksum === 55) {
                document.getElementById('root').textContent = 'ready modules=' + totalMods + ' hmr=' + status;
              }
            });
            """
            return ("application/javascript; charset=utf-8", Data(js.utf8), 40)
        }
        if path.hasPrefix("/mod"), path.hasSuffix(".js"),
           let n = Int(path.dropFirst(4).dropLast(3)), (1...10).contains(n) {
            return ("application/javascript; charset=utf-8", Data("export const v = \(n);".utf8), 120)
        }
        return ("text/plain", Data("not found".utf8), 0)
    }
}
