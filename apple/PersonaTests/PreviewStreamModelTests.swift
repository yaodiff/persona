import Foundation
import PersonaWire
import Testing
#if os(iOS)
import UIKit
#endif
@testable import Persona

/// Unit tests for iOS headless-browser preview streaming (`PreviewStreamLogic`, `PreviewStreamModel`) and direct-to-stream
/// fallback decisions (`PreviewFallbackPolicy`, `PreviewCardModeState`).
@MainActor
struct PreviewStreamModelTests {
    #if os(iOS)
    /// In-memory fake WebSocket recording every outbound JSON message and letting tests push inbound events synchronously.
    final class FakePreviewSocket: PreviewStreamSocket, @unchecked Sendable {
        private let lock = NSLock()
        private var handler: (@MainActor @Sendable (PreviewStreamSocketEvent) -> Void)?
        private var sentTexts: [String] = []
        private var closedCount = 0

        var sentMessages: [[String: Any]] {
            lock.withLock {
                sentTexts.compactMap { text in
                    guard let data = text.data(using: .utf8),
                          let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return nil }
                    return obj
                }
            }
        }

        var closeCount: Int { lock.withLock { closedCount } }

        func open(_ handler: @escaping @MainActor @Sendable (PreviewStreamSocketEvent) -> Void) {
            lock.withLock { self.handler = handler }
        }

        func send(text: String) {
            lock.withLock { sentTexts.append(text) }
        }

        func close() {
            lock.withLock {
                closedCount += 1
                handler = nil
            }
        }

        @MainActor func emit(_ event: PreviewStreamSocketEvent) {
            let h = lock.withLock { handler }
            h?(event)
        }
    }

    private static let samplePNG = Data([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, 0x00, 0x01])
    private static let sampleJPEG = Data([0xFF, 0xD8, 0xFF, 0xE0, 0x00, 0x10])

    private static func makeFramePacket(seq: UInt32, width: UInt16, height: UInt16, payload: Data) -> Data {
        var data = Data([UInt8(ascii: "f")])
        var s = seq.bigEndian
        var w = width.bigEndian
        var h = height.bigEndian
        data.append(Data(bytes: &s, count: 4))
        data.append(Data(bytes: &w, count: 2))
        data.append(Data(bytes: &h, count: 2))
        data.append(payload)
        return data
    }

    private static func makeShotPacket(id: UInt32, payload: Data) -> Data {
        var data = Data([UInt8(ascii: "s")])
        var i = id.bigEndian
        data.append(Data(bytes: &i, count: 4))
        data.append(payload)
        return data
    }

    private static func solidImage(width: Int = 2, height: Int = 2) -> UIImage {
        let format = UIGraphicsImageRendererFormat()
        format.scale = 1
        return UIGraphicsImageRenderer(size: CGSize(width: width, height: height), format: format).image { ctx in
            UIColor.systemBlue.setFill()
            ctx.fill(CGRect(x: 0, y: 0, width: width, height: height))
        }
    }

    private func makeModel(
        port: Int = 5173,
        path: String = "/settings",
        sockets: @escaping @MainActor (URLRequest) -> FakePreviewSocket,
        decodeFrame: (@Sendable (Data, PreviewFrameFormat, Int, Int) async -> PreviewStreamModel.DecodedFrame?)? = nil
    ) -> PreviewStreamModel {
        let cfg = ServerConfig(url: URL(string: "https://hub.example.invalid/persona")!, token: "fixture-token")
        return PreviewStreamModel(
            machine: "macbook",
            port: port,
            path: path,
            deps: .init(
                config: { cfg },
                openPort: { _, _ in },
                makeSocket: { req in sockets(req) },
                decodeFrame: decodeFrame ?? { _, _, w, h in
                    await MainActor.run { PreviewStreamModel.DecodedFrame(image: Self.solidImage(width: 4, height: 4), pixelWidth: w * 2, pixelHeight: h * 2) }
                },
                decodeShot: { _ in await MainActor.run { Self.solidImage(width: 8, height: 8) } },
                sleep: { _ in },
                copyToClipboard: { _ in }
            )
        )
    }

    @Test func parsesBinaryFramesAndDetectsPNGAndJPEGMagicBytes() {
        let pngPacket = Self.makeFramePacket(seq: 42, width: 390, height: 844, payload: Self.samplePNG)
        let parsedPNG = PreviewStreamLogic.parseBinary(pngPacket)
        #expect(parsedPNG == .frame(seq: 42, width: 390, height: 844, data: Self.samplePNG))
        #expect(PreviewStreamLogic.frameFormat(Self.samplePNG) == .png)

        let jpgPacket = Self.makeFramePacket(seq: 43, width: 412, height: 915, payload: Self.sampleJPEG)
        let parsedJPG = PreviewStreamLogic.parseBinary(jpgPacket)
        #expect(parsedJPG == .frame(seq: 43, width: 412, height: 915, data: Self.sampleJPEG))
        #expect(PreviewStreamLogic.frameFormat(Self.sampleJPEG) == .jpeg)

        let shotPacket = Self.makeShotPacket(id: 7, payload: Self.samplePNG)
        #expect(PreviewStreamLogic.parseBinary(shotPacket) == .shot(id: 7, data: Self.samplePNG))

        // Invalid magic bytes and truncated frames return nil cleanly.
        #expect(PreviewStreamLogic.frameFormat(Data([0x00, 0x01, 0x02, 0x03])) == nil)
        #expect(PreviewStreamLogic.frameFormat(Data([0x89, 0x50])) == nil)
        #expect(PreviewStreamLogic.parseBinary(Data([UInt8(ascii: "f"), 0, 0, 0])) == nil)
        #expect(PreviewStreamLogic.parseBinary(Data([UInt8(ascii: "x"), 0, 0, 0, 1])) == nil)
    }

    @Test func buildsConnectRequestWithHubRouteCredentialsAndMobileViewport() throws {
        defer { HubTransport.route(URL(string: "https://hub.example.invalid/persona")!, via: nil) }
        let hub = URL(string: "https://hub.example.invalid/persona")!
        let cfg = ServerConfig(url: hub, token: "fixture-token-preview")
        let vp = PreviewStreamLogic.clampedViewport(size: CGSize(width: 393.4, height: 742.6), displayScale: 3.0, mobile: true)
        #expect(vp == PreviewViewport(width: 393, height: 743, dpr: 2, mobile: true))

        // Tiny / huge dimensions clamp to protocol bounds [200..3840] x [200..2400], dpr [1..2].
        #expect(PreviewStreamLogic.clampedViewport(size: CGSize(width: 80, height: 90), displayScale: 0.5, mobile: true) ==
                PreviewViewport(width: 200, height: 200, dpr: 1, mobile: true))
        #expect(PreviewStreamLogic.clampedViewport(size: CGSize(width: 5000, height: 4000), displayScale: 3.0, mobile: true) ==
                PreviewViewport(width: 3840, height: 2400, dpr: 2, mobile: true))

        let req = PreviewStreamLogic.connectRequest(machine: "macbook", port: 5173, path: "/settings?tab=1", viewport: vp, config: cfg)
        let url = try #require(req.url)
        let comps = try #require(URLComponents(url: url, resolvingAgainstBaseURL: false))
        #expect(comps.scheme == "wss")
        #expect(comps.host == "hub.example.invalid")
        #expect(comps.path == "/persona/api/v1/machines/macbook/preview/connect")
        let q = Dictionary(uniqueKeysWithValues: (comps.queryItems ?? []).map { ($0.name, $0.value ?? "") })
        #expect(q["port"] == "5173")
        #expect(q["path"] == "/settings?tab=1")
        #expect(q["w"] == "393")
        #expect(q["h"] == "743")
        #expect(q["dpr"] == "2")
        #expect(q["mobile"] == "1")
        #expect(req.value(forHTTPHeaderField: "Authorization") == "Bearer fixture-token-preview")

        // Tunnel routing follows HubTransport.resolve like PortForwarder.
        let tunnel = URL(string: "http://127.0.0.1:52199")!
        HubTransport.route(hub, via: tunnel)
        let routed = PreviewStreamLogic.connectRequest(machine: "macbook", port: 5173, path: "/", viewport: vp, config: cfg)
        #expect(routed.url?.scheme == "ws")
        #expect(routed.url?.host == "127.0.0.1")
        #expect(routed.url?.port == 52199)
    }

    @Test func readySendsFeaturesAndEveryBinaryFrameSendsAck() async throws {
        var createdSockets: [FakePreviewSocket] = []
        let model = makeModel(sockets: { _ in
            let s = FakePreviewSocket()
            createdSockets.append(s)
            return s
        })
        model.start(viewportSize: CGSize(width: 390, height: 844), displayScale: 3.0)
        // Wait for async start task to open socket.
        for _ in 0..<40 where createdSockets.isEmpty { try await Task.sleep(for: .milliseconds(10)) }
        let sock = try #require(createdSockets.first)

        sock.emit(.opened)
        sock.emit(.text(#"{"t":"ready","browser":"Google Chrome 131","w":390,"h":844}"#))
        let features = sock.sentMessages.first { ($0["t"] as? String) == "features" }
        #expect(features?["select"] as? Bool == true)
        #expect(features?["png"] as? Bool == true)

        // Frame 1 (JPEG), Frame 2 (PNG), Frame 3 (bad magic bytes), Frame 1 again (stale seq) — ALL must ack!
        sock.emit(.data(Self.makeFramePacket(seq: 1, width: 390, height: 844, payload: Self.sampleJPEG)))
        sock.emit(.data(Self.makeFramePacket(seq: 2, width: 390, height: 844, payload: Self.samplePNG)))
        sock.emit(.data(Self.makeFramePacket(seq: 3, width: 390, height: 844, payload: Data([0x01, 0x02, 0x03, 0x04]))))
        sock.emit(.data(Self.makeFramePacket(seq: 1, width: 390, height: 844, payload: Self.sampleJPEG)))

        for _ in 0..<50 {
            let acks = sock.sentMessages.filter { ($0["t"] as? String) == "ack" }
            if acks.count >= 4 { break }
            try await Task.sleep(for: .milliseconds(10))
        }
        let ackSeqs = sock.sentMessages.filter { ($0["t"] as? String) == "ack" }.compactMap { $0["seq"] as? Int }
        #expect(Set(ackSeqs) == Set([1, 2, 3]))
        #expect(ackSeqs.count == 4)
        #expect(model.frameImage != nil)
        #expect(model.frameFormat == .png) // seq 2 (PNG) stays displayed; stale seq 1 did not overwrite it
        #expect(model.pageSize == CGSize(width: 390, height: 844))
    }

    @Test func mapsViewCoordinatesToPageCSSPixelsAndForwardsTapScrollAndTyping() async throws {
        // Aspect-fit rect: 390x844 inside 402x700 -> scale = 700/844 ≈ 0.8294, width ≈ 323.46, centered horizontally.
        let fitted = PreviewStreamLogic.aspectFitRect(contentSize: CGSize(width: 390, height: 844), in: CGSize(width: 402, height: 700))
        #expect(abs(fitted.height - 700) < 0.5)
        #expect(abs(fitted.width - 390 * (700.0 / 844.0)) < 0.5)
        #expect(abs(fitted.minX - (402 - fitted.width) / 2) < 0.5)

        // Center of displayed rect maps to center of page CSS size (195, 422).
        let center = PreviewStreamLogic.toPage(point: CGPoint(x: fitted.midX, y: fitted.midY), in: fitted, pageSize: CGSize(width: 390, height: 844))
        #expect(center == CGPoint(x: 195, y: 422))

        // Out-of-bounds taps clamp cleanly to [0..390] x [0..844].
        #expect(PreviewStreamLogic.toPage(point: CGPoint(x: -50, y: -20), in: fitted, pageSize: CGSize(width: 390, height: 844)) == CGPoint(x: 0, y: 0))
        #expect(PreviewStreamLogic.toPage(point: CGPoint(x: 999, y: 999), in: fitted, pageSize: CGSize(width: 390, height: 844)) == CGPoint(x: 390, y: 844))

        var sockets: [FakePreviewSocket] = []
        let model = makeModel(sockets: { _ in let s = FakePreviewSocket(); sockets.append(s); return s })
        model.start(viewportSize: CGSize(width: 390, height: 844), displayScale: 2.0)
        for _ in 0..<40 where sockets.isEmpty { try await Task.sleep(for: .milliseconds(10)) }
        let sock = try #require(sockets.first)
        sock.emit(.opened)
        sock.emit(.text(#"{"t":"ready","browser":"Chrome","w":390,"h":844}"#))
        sock.emit(.data(Self.makeFramePacket(seq: 1, width: 390, height: 844, payload: Self.samplePNG)))
        for _ in 0..<40 where model.pageSize == .zero { try await Task.sleep(for: .milliseconds(10)) }

        let rect = CGRect(x: 0, y: 0, width: 390, height: 844)
        model.tap(at: CGPoint(x: 195, y: 173), in: rect)
        let mouseEvents = sock.sentMessages.filter { ($0["t"] as? String) == "mouse" }
        #expect(mouseEvents.map { $0["type"] as? String } == ["move", "down", "up"])
        #expect(mouseEvents[1]["x"] as? Int == 195 && mouseEvents[1]["y"] as? Int == 173)
        #expect(mouseEvents[1]["button"] as? String == "left")

        model.scroll(delta: CGSize(width: 0, height: -120), at: CGPoint(x: 195, y: 400), in: rect)
        let wheel = try #require(sock.sentMessages.last { ($0["t"] as? String) == "wheel" })
        #expect(wheel["x"] as? Int == 195 && wheel["y"] as? Int == 400)
        // Finger dragged up (negative translation) scrolls page down (positive dy).
        #expect((wheel["dy"] as? Double ?? 0) > 100)

        model.sendText("hello")
        let textMsg = try #require(sock.sentMessages.last { ($0["t"] as? String) == "text" })
        #expect(textMsg["text"] as? String == "hello")

        model.sendKey(key: "Enter", code: "Enter", keyCode: 13, text: "\r")
        let keyMsgs = sock.sentMessages.filter { ($0["t"] as? String) == "key" }
        #expect(keyMsgs.map { $0["type"] as? String } == ["down", "up"])
        #expect(keyMsgs.first?["key"] as? String == "Enter")
        #expect(keyMsgs.first?["text"] as? String == "\r")
    }

    @Test func handlesSelectOpenChooseIndexZeroDisabledCancelAndNodeClose() async throws {
        var sockets: [FakePreviewSocket] = []
        let model = makeModel(sockets: { _ in let s = FakePreviewSocket(); sockets.append(s); return s })
        model.start(viewportSize: CGSize(width: 390, height: 844), displayScale: 2.0)
        for _ in 0..<40 where sockets.isEmpty { try await Task.sleep(for: .milliseconds(10)) }
        let sock = try #require(sockets.first)
        sock.emit(.opened)
        sock.emit(.text(#"{"t":"ready","browser":"Chrome","w":390,"h":844}"#))

        let openPayload = """
        {"t":"select_open","id":5,"rect":{"x":22,"y":154,"w":346,"h":38},"selected":1,"options":[
          {"index":0,"label":"Editor","group":"Workspace"},
          {"index":1,"label":"Viewer","group":"Workspace"},
          {"index":2,"label":"Owner (managed)","disabled":true,"group":"Workspace"},
          {"index":3,"label":"Reviewer","group":"Automation"}
        ]}
        """
        sock.emit(.text(openPayload))
        let opened = try #require(model.selectOpen)
        #expect(opened.id == 5)
        #expect(opened.selected == 1)
        #expect(opened.options.count == 4)
        let groups = PreviewStreamLogic.selectGroups(from: opened.options)
        #expect(groups.map(\.title) == ["Workspace", "Automation"])
        #expect(groups[0].options.count == 3 && groups[1].options.count == 1)

        // Disabled option cannot be chosen: no select_choose sent, picker stays open.
        model.chooseSelect(index: 2)
        #expect(model.selectOpen?.id == 5)
        #expect(sock.sentMessages.filter { ($0["t"] as? String) == "select_choose" }.isEmpty)

        // Choosing index 0 MUST send select_choose with explicit index: 0!
        model.chooseSelect(index: 0)
        #expect(model.selectOpen == nil)
        let chosen = try #require(sock.sentMessages.last { ($0["t"] as? String) == "select_choose" })
        #expect(chosen["id"] as? Int == 5)
        #expect(chosen["index"] as? Int == 0)

        // Cancel by user sends select_close {id}.
        sock.emit(.text(#"{"t":"select_open","id":6,"rect":{"x":0,"y":0,"w":100,"h":30},"selected":0,"options":[{"index":0,"label":"One"}]}"#))
        #expect(model.selectOpen?.id == 6)
        model.dismissSelect()
        #expect(model.selectOpen == nil)
        let closeSent = try #require(sock.sentMessages.last { ($0["t"] as? String) == "select_close" })
        #expect(closeSent["id"] as? Int == 6)

        // Node select_close with matching id dismisses locally WITHOUT echoing select_close back; stale id is ignored.
        sock.emit(.text(#"{"t":"select_open","id":7,"rect":{"x":0,"y":0,"w":100,"h":30},"selected":0,"options":[{"index":0,"label":"One"}]}"#))
        let closeCountBefore = sock.sentMessages.filter { ($0["t"] as? String) == "select_close" }.count
        sock.emit(.text(#"{"t":"select_close","id":6}"#)) // stale id
        #expect(model.selectOpen?.id == 7)
        sock.emit(.text(#"{"t":"select_close","id":7}"#)) // matching id
        #expect(model.selectOpen == nil)
        #expect(sock.sentMessages.filter { ($0["t"] as? String) == "select_close" }.count == closeCountBefore)
    }

    @Test func handlesPageExternalDialogPickShotAndUnavailableErrors() async throws {
        var sockets: [FakePreviewSocket] = []
        let model = makeModel(sockets: { _ in let s = FakePreviewSocket(); sockets.append(s); return s })
        var openedExternals: [URL] = []
        var pickedElements: [PickedElement] = []
        var shotsReceived = 0
        var unavailableReasons: [PreviewStreamUnavailableReason] = []
        model.onOpenExternal = { openedExternals.append($0) }
        model.onPicked = { pickedElements.append($0) }
        model.onShot = { _ in shotsReceived += 1 }
        model.onUnavailable = { unavailableReasons.append($0) }

        model.start(viewportSize: CGSize(width: 390, height: 844), displayScale: 2.0)
        for _ in 0..<40 where sockets.isEmpty { try await Task.sleep(for: .milliseconds(10)) }
        let sock = try #require(sockets.first)
        sock.emit(.opened)
        sock.emit(.text(#"{"t":"ready","browser":"Chrome","w":390,"h":844}"#))

        // page updates title, address, navigation flags
        sock.emit(.text(#"{"t":"page","url":"http://localhost:5173/notes","title":"Notes · Acme","loading":false,"back":true,"forward":false}"#))
        #expect(model.pageTitle == "Notes · Acme")
        #expect(model.currentPath == "/notes")
        #expect(model.canGoBack == true && model.canGoForward == false && model.isLoading == false)

        // external opens safe http/https URLs and ignores unsafe/credential URLs
        sock.emit(.text(#"{"t":"external","url":"https://docs.example.com/guide"}"#))
        sock.emit(.text(#"{"t":"external","url":"javascript:alert(1)"}"#))
        sock.emit(.text(#"{"t":"external","url":"https://user:pass@evil.example.com/"}"#))
        #expect(openedExternals.map(\.absoluteString) == ["https://docs.example.com/guide"])

        // dialog & answer & dialog_closed
        sock.emit(.text(#"{"t":"dialog","kind":"prompt","message":"Enter name","default":"Sam"}"#))
        #expect(model.activeDialog?.kind == .prompt)
        #expect(model.activeDialog?.defaultText == "Sam")
        model.answerDialog(accept: true, text: "Alex")
        #expect(model.activeDialog == nil)
        let dlgReply = try #require(sock.sentMessages.last { ($0["t"] as? String) == "dialog" })
        #expect(dlgReply["accept"] as? Bool == true && dlgReply["text"] as? String == "Alex")

        sock.emit(.text(#"{"t":"dialog","kind":"confirm","message":"Continue?"}"#))
        sock.emit(.text(#"{"t":"dialog_closed"}"#))
        #expect(model.activeDialog == nil)

        // pick & picked
        model.setPicking(true)
        #expect(sock.sentMessages.last?["t"] as? String == "pick")
        #expect(sock.sentMessages.last?["on"] as? Bool == true)
        sock.emit(.text(#"{"t":"picked","selector":"button.save","tag":"button","text":"Save","html":"<button>Save</button>","url":"http://localhost:5173/settings","title":"Settings"}"#))
        #expect(model.isPicking == false)
        #expect(pickedElements.first?.selector == "button.save")

        // shot & binary 's'
        model.requestShot(full: true)
        let shotReq = try #require(sock.sentMessages.last { ($0["t"] as? String) == "shot" })
        let shotID = try #require(shotReq["id"] as? Int)
        #expect(shotReq["full"] as? Bool == true)
        sock.emit(.data(Self.makeShotPacket(id: UInt32(shotID), payload: Self.samplePNG)))
        for _ in 0..<40 where shotsReceived == 0 { try await Task.sleep(for: .milliseconds(10)) }
        #expect(shotsReceived == 1)
        #expect(model.isShooting == false)

        // no_browser marks unavailable and stops reconnecting
        sock.emit(.text(#"{"t":"error","code":"no_browser","error":"no Chrome on this computer"}"#))
        #expect(model.unavailableReason == .noBrowser)
        #expect(unavailableReasons == [.noBrowser])
        #expect(model.connection == .error(code: "no_browser", message: "no Chrome on this computer"))
    }
    #endif

    @Test func fallbackPolicyDistinguishesStreamResolvableFromNonResolvableErrors() {
        // Errors stream CANNOT solve: never offer button, never auto-fallback.
        let unresolvable: [PreviewDirectFailureKind] = [
            .portGone(5173),
            .computerOffline,
            .unauthorized,
            .forbidden,
            .hubUnreachable,
            .noInternet,
        ]
        for err in unresolvable {
            let d1 = PreviewFallbackPolicy.decide(failure: err, consecutiveFailures: 1, hubHasPreview: true, streamUnavailable: nil, userPinnedDirect: false)
            let d3 = PreviewFallbackPolicy.decide(failure: err, consecutiveFailures: 3, hubHasPreview: true, streamUnavailable: nil, userPinnedDirect: false)
            #expect(!d1.canOfferStream && !d1.shouldAutoFallback, "unexpected stream fallback for \(err)")
            #expect(!d3.canOfferStream && !d3.shouldAutoFallback, "unexpected repeated stream fallback for \(err)")
        }

        // Immediate auto-fallback errors: declined self-signed cert, untrusted TLS, timeout, listener failure.
        for err in [PreviewDirectFailureKind.declinedSelfSigned, .tlsUntrusted, .timedOut, .listenerFailed] {
            let d = PreviewFallbackPolicy.decide(failure: err, consecutiveFailures: 1, hubHasPreview: true, streamUnavailable: nil, userPinnedDirect: false)
            #expect(d.canOfferStream && d.shouldAutoFallback, "expected immediate auto-fallback for \(err)")
        }

        // Recoverable transient/transport failures: first failure offers button (no auto-fallback); 2nd consecutive auto-falls back.
        for err in [PreviewDirectFailureKind.serverUnreachable(502), .tooManyConnections, .connectionLost, .processTerminated] {
            let first = PreviewFallbackPolicy.decide(failure: err, consecutiveFailures: 1, hubHasPreview: true, streamUnavailable: nil, userPinnedDirect: false)
            #expect(first.canOfferStream && !first.shouldAutoFallback, "expected offer-only on first failure for \(err)")
            let second = PreviewFallbackPolicy.decide(failure: err, consecutiveFailures: 2, hubHasPreview: true, streamUnavailable: nil, userPinnedDirect: false)
            #expect(second.canOfferStream && second.shouldAutoFallback, "expected auto-fallback on 2nd consecutive failure for \(err)")
        }

        // Unavailable stream (hub lacks preview, no_browser, or node_outdated): never offer or auto-fallback.
        #expect(PreviewFallbackPolicy.decide(failure: .declinedSelfSigned, consecutiveFailures: 2, hubHasPreview: false, streamUnavailable: nil, userPinnedDirect: false) ==
                PreviewFallbackDecision(canOfferStream: false, shouldAutoFallback: false))
        #expect(PreviewFallbackPolicy.decide(failure: .timedOut, consecutiveFailures: 2, hubHasPreview: true, streamUnavailable: .noBrowser, userPinnedDirect: false) ==
                PreviewFallbackDecision(canOfferStream: false, shouldAutoFallback: false))
        #expect(PreviewFallbackPolicy.decide(failure: .timedOut, consecutiveFailures: 2, hubHasPreview: true, streamUnavailable: .nodeOutdated, userPinnedDirect: false) ==
                PreviewFallbackDecision(canOfferStream: false, shouldAutoFallback: false))
    }

    @Test func cardModeStateRemembersModeAndNeverBouncesBackAndForth() {
        let state = PreviewCardModeState()
        #expect(state.mode == .direct)
        #expect(state.canOfferStream(hubHasPreview: true))
        #expect(!state.canOfferStream(hubHasPreview: false))

        // First 502 error offers button without auto-switching.
        let first = state.handleDirectFailure(.serverUnreachable(502), hubHasPreview: true)
        #expect(first.canOfferStream && !first.shouldAutoFallback)
        #expect(state.mode == .direct)

        // Second consecutive 502 error auto-switches and shows notice.
        let second = state.handleDirectFailure(.serverUnreachable(502), hubHasPreview: true)
        #expect(second.shouldAutoFallback)
        #expect(state.mode == .stream)
        #expect(state.showedAutoFallbackNotice)

        // User explicitly switches back to direct -> sticks (userPinnedDirect == true), does NOT auto-bounce on next error.
        state.switchToDirectManually()
        #expect(state.mode == .direct)
        #expect(state.userPinnedDirect)
        #expect(!state.showedAutoFallbackNotice)

        let afterPin = state.handleDirectFailure(.declinedSelfSigned, hubHasPreview: true)
        #expect(afterPin.canOfferStream && !afterPin.shouldAutoFallback)
        #expect(state.mode == .direct)

        // User can still switch manually back to stream.
        #expect(state.switchToStreamManually(hubHasPreview: true))
        #expect(state.mode == .stream)
        #expect(!state.showedAutoFallbackNotice)

        // Stream reporting no_browser disables future offers cleanly.
        state.noteStreamUnavailable(.noBrowser)
        #expect(!state.canOfferStream(hubHasPreview: true))
        #expect(state.streamUnavailable == .noBrowser)
    }
}
