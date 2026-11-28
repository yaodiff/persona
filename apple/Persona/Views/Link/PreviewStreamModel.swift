import Foundation
import ImageIO
import Observation
import os
import PersonaWire
import SwiftUI
#if canImport(UIKit)
import UIKit
#endif

// Remote headless-browser preview stream for iOS (docs/web/preview.md "Messages" & "iPhone"):
// Connects to `GET /api/v1/machines/{mid}/preview/connect?port=&path=&w=&h=&dpr=&mobile=1`, decodes binary `f` JPEG/PNG
// frames off the main actor (docs/apple/performance.md P1/P8), acknowledges every frame with `ack {seq}`, announces
// `features {select: true, png: true}` on every `ready`, forwards mobile pointer/scroll/keyboard/navigation/pick/shot
// events, bridges native `<select>` lists and JS dialogs, and owns the direct-to-stream fallback policy.

/// Image encoding of a binary `f` frame, identified from its magic bytes.
nonisolated enum PreviewFrameFormat: Equatable, Sendable {
    case png
    case jpeg
}

/// Binary message from the node: a rendered page frame (`f`) or a PNG screenshot (`s`).
nonisolated enum PreviewStreamBinary: Equatable, Sendable {
    case frame(seq: UInt32, width: Int, height: Int, data: Data)
    case shot(id: UInt32, data: Data)
}

/// Viewport-relative CSS pixel box of an open `<select>` element.
nonisolated struct PreviewSelectRect: Decodable, Equatable, Sendable {
    var x: Double
    var y: Double
    var w: Double
    var h: Double
}

/// One visible `<option>` inside an open `<select>` list.
nonisolated struct PreviewSelectOption: Decodable, Equatable, Sendable, Identifiable {
    var index: Int
    var label: String
    var disabled: Bool = false
    var group: String? = nil
    var id: Int { index }

    enum CodingKeys: String, CodingKey { case index, label, disabled, group }

    init(index: Int, label: String, disabled: Bool = false, group: String? = nil) {
        self.index = index
        self.label = label
        self.disabled = disabled
        self.group = group
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        index = try c.decode(Int.self, forKey: .index)
        label = (try? c.decode(String.self, forKey: .label)) ?? ""
        disabled = (try? c.decode(Bool.self, forKey: .disabled)) ?? false
        group = try? c.decode(String.self, forKey: .group)
    }
}

/// Sanitized active `<select>` popup state from `select_open`.
nonisolated struct PreviewSelectState: Equatable, Sendable, Identifiable {
    var id: Int
    var rect: PreviewSelectRect
    var selected: Int
    var options: [PreviewSelectOption]
}

/// Contiguous section of `<select>` options sharing an optional `<optgroup>` heading.
nonisolated struct PreviewSelectGroup: Equatable, Sendable, Identifiable {
    var id: String
    var title: String?
    var options: [PreviewSelectOption]
}

/// Active JavaScript dialog from the remote browser tab (`alert` / `confirm` / `prompt` / `beforeunload`).
nonisolated struct PreviewStreamDialog: Equatable, Sendable, Identifiable {
    enum Kind: String, Decodable, Equatable, Sendable {
        case alert, confirm, prompt, beforeunload
    }

    var id = UUID()
    var kind: Kind
    var message: String
    var defaultText: String = ""
}

/// Reasons the computer cannot stream headless-browser previews at all.
nonisolated enum PreviewStreamUnavailableReason: Equatable, Sendable {
    case noBrowser
    case nodeOutdated
}

/// Lifecycle state of the preview stream WebSocket.
nonisolated enum PreviewStreamConnection: Equatable, Sendable {
    case connecting
    case open
    case reconnecting(attempt: Int)
    case closed
    case error(code: String, message: String)
}

/// Non-fatal inline banner shown above the streamed page.
nonisolated enum PreviewStreamBanner: Equatable, Sendable {
    case failed(url: String, error: String)
    case crashed
    case notice(code: String)
}

/// Viewport dimensions sent in the connect query and `size` messages.
nonisolated struct PreviewViewport: Equatable, Sendable {
    var width: Int
    var height: Int
    var dpr: Double
    var mobile: Bool
}

/// Typed cause of a direct loopback `WKWebView` / `PortForwarder` preview failure used to decide whether computer
/// streaming can resolve it and whether automatic fallback should trigger.
nonisolated enum PreviewDirectFailureKind: Equatable, Sendable {
    case portGone(Int)
    case computerOffline
    case unauthorized
    case forbidden
    case hubUnreachable
    case noInternet
    case declinedSelfSigned
    case tlsUntrusted
    case timedOut
    case tooManyConnections
    case serverUnreachable(Int)
    case listenerFailed
    case processTerminated
    case connectionLost
    case httpError(Int)
    case other

    static func from(portError: PortForwardError) -> PreviewDirectFailureKind {
        switch portError {
        case .portGone(let p): .portGone(p)
        case .computerOffline: .computerOffline
        case .unauthorized: .unauthorized
        case .forbidden: .forbidden
        case .tooManyConnections: .tooManyConnections
        case .serverUnreachable(let code): .serverUnreachable(code)
        case .hubUnreachable: .hubUnreachable
        case .httpError(let code): .httpError(code)
        case .listenerFailed: .listenerFailed
        }
    }
}

/// Active rendering mode inside a preview link card.
nonisolated enum PreviewMode: Equatable, Sendable {
    case direct
    case stream
}

/// Result of evaluating a direct-mode failure against the fallback policy.
nonisolated struct PreviewFallbackDecision: Equatable, Sendable {
    /// Whether the direct-mode error overlay should offer "View through the computer" beside Retry.
    var canOfferStream: Bool
    /// Whether the card should automatically switch to "View through the computer" with the fallback notice.
    var shouldAutoFallback: Bool
}

/// Pure rules deciding which direct-mode failures can be resolved by the computer's headless browser stream and when
/// automatic fallback activates.
nonisolated enum PreviewFallbackPolicy {
    /// Number of consecutive recoverable direct failures that trigger automatic fallback.
    static let repeatedFailureThreshold = 2

    /// Whether switching to the computer's headless browser could plausibly show the page for this direct failure.
    /// Stopped ports, offline computers, auth/permission denials, and phone-to-hub network outages cannot be fixed by
    /// streaming from the same computer/hub.
    static func canStreamResolve(_ failure: PreviewDirectFailureKind) -> Bool {
        switch failure {
        case .portGone, .computerOffline, .unauthorized, .forbidden, .hubUnreachable, .noInternet:
            false
        case .declinedSelfSigned, .tlsUntrusted, .timedOut, .tooManyConnections,
             .serverUnreachable, .listenerFailed, .processTerminated, .connectionLost, .httpError, .other:
            true
        }
    }

    /// Whether a single occurrence immediately warrants switching to the computer stream (rather than showing Retry +
    /// "View through the computer" first and auto-falling back on a repeated failure).
    static func evaluatesImmediateFallback(_ failure: PreviewDirectFailureKind) -> Bool {
        switch failure {
        case .declinedSelfSigned, .tlsUntrusted, .timedOut, .listenerFailed:
            true
        default:
            false
        }
    }

    static func decide(
        failure: PreviewDirectFailureKind,
        consecutiveFailures: Int,
        hubHasPreview: Bool,
        streamUnavailable: PreviewStreamUnavailableReason?,
        userPinnedDirect: Bool
    ) -> PreviewFallbackDecision {
        guard hubHasPreview, streamUnavailable == nil, canStreamResolve(failure) else {
            return PreviewFallbackDecision(canOfferStream: false, shouldAutoFallback: false)
        }
        let immediate = evaluatesImmediateFallback(failure)
        let repeated = consecutiveFailures >= repeatedFailureThreshold
        return PreviewFallbackDecision(
            canOfferStream: true,
            shouldAutoFallback: !userPinnedDirect && (immediate || repeated)
        )
    }
}

/// Per-card preview mode state: starts in direct loopback mode, switches to stream manually or automatically on
/// stream-resolvable failures, remembers the active mode within the open card without bouncing back and forth, and
/// respects an explicit user switch back to direct mode.
@MainActor @Observable
final class PreviewCardModeState {
    private(set) var mode: PreviewMode = .direct
    private(set) var showedAutoFallbackNotice = false
    private(set) var streamUnavailable: PreviewStreamUnavailableReason? = nil
    private(set) var userPinnedDirect = false
    private(set) var consecutiveDirectFailures = 0

    func canOfferStream(hubHasPreview: Bool) -> Bool {
        hubHasPreview && streamUnavailable == nil
    }

    @discardableResult
    func switchToStreamManually(hubHasPreview: Bool) -> Bool {
        guard canOfferStream(hubHasPreview: hubHasPreview) else { return false }
        mode = .stream
        showedAutoFallbackNotice = false
        userPinnedDirect = false
        return true
    }

    func switchToDirectManually() {
        mode = .direct
        showedAutoFallbackNotice = false
        userPinnedDirect = true
        consecutiveDirectFailures = 0
    }

    func dismissFallbackNotice() {
        showedAutoFallbackNotice = false
    }

    func noteDirectSuccess() {
        consecutiveDirectFailures = 0
    }

    func handleDirectFailure(_ kind: PreviewDirectFailureKind, hubHasPreview: Bool) -> PreviewFallbackDecision {
        consecutiveDirectFailures += 1
        let decision = PreviewFallbackPolicy.decide(
            failure: kind,
            consecutiveFailures: consecutiveDirectFailures,
            hubHasPreview: hubHasPreview,
            streamUnavailable: streamUnavailable,
            userPinnedDirect: userPinnedDirect
        )
        if decision.shouldAutoFallback {
            mode = .stream
            showedAutoFallbackNotice = true
        }
        return decision
    }

    func noteStreamUnavailable(_ reason: PreviewStreamUnavailableReason) {
        streamUnavailable = reason
    }
}

// MARK: - Socket abstraction

/// Events delivered from the preview WebSocket to `PreviewStreamModel` on the main actor, in order.
nonisolated enum PreviewStreamSocketEvent: Equatable, Sendable {
    case opened
    case text(String)
    case data(Data)
    case closed(status: Int?, reason: String?)
}

/// WebSocket transport injected via `PreviewStreamModel.Deps` so tests run against an in-memory fake socket.
nonisolated protocol PreviewStreamSocket: AnyObject, Sendable {
    func open(_ handler: @escaping @MainActor @Sendable (PreviewStreamSocketEvent) -> Void)
    func send(text: String)
    func close()
}

/// Observes handshake open and HTTP refusal status while keeping `HubRedirects` (Cloudflare Access + DevMode metrics).
private nonisolated final class PreviewStreamWSDelegate: HubRedirects, URLSessionWebSocketDelegate, @unchecked Sendable {
    private let onOpened: @Sendable () -> Void
    private let onCompleted: @Sendable (Int?, Error?) -> Void
    private let lock = NSLock()
    private var didOpen = false

    init(onOpened: @escaping @Sendable () -> Void, onCompleted: @escaping @Sendable (Int?, Error?) -> Void) {
        self.onOpened = onOpened
        self.onCompleted = onCompleted
        super.init()
    }

    func urlSession(_ session: URLSession, webSocketTask: URLSessionWebSocketTask, didOpenWithProtocol protocol: String?) {
        let shouldFire = lock.withLock { () -> Bool in
            guard !didOpen else { return false }
            didOpen = true
            return true
        }
        if shouldFire { onOpened() }
    }

    func urlSession(_ session: URLSession, task: URLSessionTask, didCompleteWithError error: (any Error)?) {
        let status = (task.response as? HTTPURLResponse)?.statusCode
        onCompleted(status, error)
    }
}

/// Production `URLSessionWebSocketTask` transport using hub routing (`HubTransport`), auth (`ServerConfig`), and
/// periodic pings (matching `HubVoiceSocket`).
nonisolated final class HubPreviewStreamSocket: PreviewStreamSocket, @unchecked Sendable {
    static let pingEvery: Duration = .seconds(20)
    /// 16 MB covers 2x PNG still frames and full-page PNG screenshots up to 12,000 px tall.
    static let maxMessageSize = 16 << 20

    private let session: URLSession
    private let task: URLSessionWebSocketTask
    private let lock = NSLock()
    private var handler: (@MainActor @Sendable (PreviewStreamSocketEvent) -> Void)?
    private var delegate: PreviewStreamWSDelegate?
    private var done = false
    private var opened = false
    private var awaitingPong = false
    private var reader: Task<Void, Never>?
    private var pinger: Task<Void, Never>?

    init(request: URLRequest) {
        let cfg = URLSessionConfiguration.ephemeral
        cfg.urlCache = nil
        cfg.timeoutIntervalForRequest = 40
        cfg.timeoutIntervalForResource = 0
        session = URLSession(configuration: cfg)
        task = session.webSocketTask(with: request)
        task.maximumMessageSize = Self.maxMessageSize
    }

    func open(_ handler: @escaping @MainActor @Sendable (PreviewStreamSocketEvent) -> Void) {
        let del = PreviewStreamWSDelegate(
            onOpened: { [weak self] in self?.handleOpened() },
            onCompleted: { [weak self] status, error in
                guard let self else { return }
                let reason = error?.localizedDescription
                self.finish(status: status, reason: reason)
            }
        )
        lock.withLock {
            self.handler = handler
            self.delegate = del
        }
        task.delegate = del
        task.resume()
        let reader = Task { [weak self] in
            guard let self else { return }
            await self.readLoop()
        }
        let pinger = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: Self.pingEvery)
                guard let self, !Task.isCancelled, !self.isDone else { return }
                guard self.lock.withLock({ () -> Bool in
                    defer { self.awaitingPong = true }
                    return !self.awaitingPong
                }) else {
                    self.finish(status: nil, reason: "no pong")
                    return
                }
                self.task.sendPing { @Sendable [weak self] error in
                    guard let self else { return }
                    if let error {
                        self.finish(status: nil, reason: error.localizedDescription)
                    } else {
                        self.lock.withLock { self.awaitingPong = false }
                    }
                }
            }
        }
        lock.withLock {
            self.reader = reader
            self.pinger = pinger
        }
    }

    func send(text: String) {
        guard !isDone else { return }
        task.send(.string(text)) { @Sendable [weak self] error in
            guard let error else { return }
            self?.finish(status: nil, reason: error.localizedDescription)
        }
    }

    func close() {
        guard let tasks = stop() else { return }
        tasks.forEach { $0?.cancel() }
        task.cancel(with: .normalClosure, reason: nil)
        session.invalidateAndCancel()
    }

    private func handleOpened() {
        let h = lock.withLock { () -> (@MainActor @Sendable (PreviewStreamSocketEvent) -> Void)? in
            guard !done, !opened else { return nil }
            opened = true
            return handler
        }
        guard let h else { return }
        Task { @MainActor [weak self] in
            guard let self, !self.isDone else { return }
            h(.opened)
        }
    }

    private func readLoop() async {
        while true {
            let event: PreviewStreamSocketEvent
            do {
                switch try await task.receive() {
                case .string(let s): event = .text(s)
                case .data(let d): event = .data(d)
                @unknown default: continue
                }
            } catch {
                let status = refusedStatus
                if status == nil {
                    // Let didCompleteWithError deliver the HTTP status if the handshake just failed.
                    try? await Task.sleep(for: .milliseconds(60))
                }
                finish(status: refusedStatus, reason: closeReason ?? error.localizedDescription)
                return
            }
            handleOpened()
            guard let h = lock.withLock({ done ? nil : self.handler }) else { return }
            await MainActor.run { [weak self] in
                guard let self, !self.isDone else { return }
                h(event)
            }
        }
    }

    private var isDone: Bool { lock.withLock { done } }

    private var refusedStatus: Int? {
        guard let code = (task.response as? HTTPURLResponse)?.statusCode, code != 101, code >= 300 else { return nil }
        return code
    }

    private var closeReason: String? {
        task.closeReason.flatMap { String(data: $0, encoding: .utf8) }.flatMap { $0.isEmpty ? nil : $0 }
    }

    private func stop() -> [Task<Void, Never>?]? {
        lock.withLock {
            guard !done else { return nil }
            done = true
            handler = nil
            return [reader, pinger]
        }
    }

    private func finish(status: Int?, reason: String?) {
        let resolvedStatus = status.flatMap { $0 != 101 && $0 >= 300 ? $0 : nil } ?? refusedStatus
        let resolvedReason = closeReason ?? reason
        let h = lock.withLock { self.handler }
        guard let tasks = stop() else { return }
        tasks.forEach { $0?.cancel() }
        task.cancel(with: .goingAway, reason: nil)
        session.invalidateAndCancel()
        guard let h else { return }
        Task { @MainActor in h(.closed(status: resolvedStatus, reason: resolvedReason)) }
    }
}

// MARK: - Pure protocol & coordinate logic

nonisolated enum PreviewStreamLogic {
    static let maxDPR: Double = 2
    static let minWidth = 200
    static let maxWidth = 3840
    static let minHeight = 200
    static let maxHeight = 2400
    static let maxSelectOptions = 1000
    static let maxSelectLabel = 200

    /// Quantizes and clamps viewport dimensions to the preview protocol's range (200–3840 × 200–2400, dpr 1–2).
    static func clampedViewport(size: CGSize, displayScale: CGFloat, mobile: Bool = true) -> PreviewViewport {
        let w = max(minWidth, min(maxWidth, Int(size.width.rounded())))
        let h = max(minHeight, min(maxHeight, Int(size.height.rounded())))
        let rawDPR = displayScale.isFinite && displayScale > 0 ? Double(displayScale) : 1
        let dpr = max(1, min(maxDPR, rawDPR))
        return PreviewViewport(width: w, height: h, dpr: dpr, mobile: mobile)
    }

    /// Parses binary frames from the node: `f` (seq uint32, w uint16, h uint16, image) or `s` (id uint32, PNG).
    static func parseBinary(_ data: Data) -> PreviewStreamBinary? {
        guard data.count >= 5 else { return nil }
        let start = data.startIndex
        let tag = data[start]
        let u32 = (UInt32(data[start + 1]) << 24)
            | (UInt32(data[start + 2]) << 16)
            | (UInt32(data[start + 3]) << 8)
            | UInt32(data[start + 4])
        if tag == UInt8(ascii: "f") {
            guard data.count >= 9 else { return nil }
            let w = (Int(data[start + 5]) << 8) | Int(data[start + 6])
            let h = (Int(data[start + 7]) << 8) | Int(data[start + 8])
            return .frame(seq: u32, width: w, height: h, data: Data(data[(start + 9)...]))
        }
        if tag == UInt8(ascii: "s") {
            return .shot(id: u32, data: Data(data[(start + 5)...]))
        }
        return nil
    }

    /// Identifies JPEG (`FF D8 FF`) or PNG (`89 50 4E 47`) from magic bytes without assuming JPEG.
    static func frameFormat(_ data: Data) -> PreviewFrameFormat? {
        let start = data.startIndex
        if data.count >= 4,
           data[start] == 0x89, data[start + 1] == 0x50, data[start + 2] == 0x4E, data[start + 3] == 0x47 {
            return .png
        }
        if data.count >= 3,
           data[start] == 0xFF, data[start + 1] == 0xD8, data[start + 2] == 0xFF {
            return .jpeg
        }
        return nil
    }

    /// Centered aspect-fit rectangle for `contentSize` inside `containerSize`.
    static func aspectFitRect(contentSize: CGSize, in containerSize: CGSize) -> CGRect {
        guard contentSize.width > 0, contentSize.height > 0,
              containerSize.width > 0, containerSize.height > 0 else {
            return CGRect(origin: .zero, size: containerSize)
        }
        let scale = min(containerSize.width / contentSize.width, containerSize.height / contentSize.height)
        let w = contentSize.width * scale
        let h = contentSize.height * scale
        return CGRect(
            x: (containerSize.width - w) / 2,
            y: (containerSize.height - h) / 2,
            width: w,
            height: h
        )
    }

    /// Converts a view-space point to clamped page CSS pixels, matching web's `toPage`.
    static func toPage(point: CGPoint, in displayedRect: CGRect, pageSize: CGSize) -> CGPoint {
        guard displayedRect.width > 0, displayedRect.height > 0, pageSize.width > 0, pageSize.height > 0 else {
            return .zero
        }
        let px = ((point.x - displayedRect.minX) * pageSize.width) / displayedRect.width
        let py = ((point.y - displayedRect.minY) * pageSize.height) / displayedRect.height
        return CGPoint(
            x: max(0, min(pageSize.width, px.rounded())),
            y: max(0, min(pageSize.height, py.rounded()))
        )
    }

    /// Converts a finger drag translation in view points to CDP `wheel` delta in page CSS pixels (dragging up scrolls down).
    static func wheelDelta(translation: CGSize, in displayedRect: CGRect, pageSize: CGSize) -> CGSize {
        guard displayedRect.width > 0, displayedRect.height > 0, pageSize.width > 0, pageSize.height > 0 else {
            return .zero
        }
        return CGSize(
            width: (-translation.width * pageSize.width) / displayedRect.width,
            height: (-translation.height * pageSize.height) / displayedRect.height
        )
    }

    /// Groups contiguous options sharing the same `<optgroup>` heading for a SwiftUI sectioned list.
    static func selectGroups(from options: [PreviewSelectOption]) -> [PreviewSelectGroup] {
        var groups: [PreviewSelectGroup] = []
        for opt in options {
            let title = opt.group.flatMap { $0.isEmpty ? nil : $0 }
            if var last = groups.last, last.title == title {
                last.options.append(opt)
                groups[groups.count - 1] = last
            } else {
                groups.append(PreviewSelectGroup(id: "g-\(groups.count)-\(title ?? "")", title: title, options: [opt]))
            }
        }
        return groups
    }

    /// Credential-free `http` / `https` URL allowed for external links opened by the streamed page.
    static func safeExternalURL(_ raw: String) -> URL? {
        let trimmed = raw.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return nil }
        for scalar in trimmed.unicodeScalars {
            if scalar.value <= 0x20 || scalar.value == 0x7F { return nil }
        }
        let lowered = trimmed.lowercased()
        guard lowered.hasPrefix("http://") || lowered.hasPrefix("https://") else { return nil }
        let afterScheme = trimmed.dropFirst(lowered.hasPrefix("https://") ? 8 : 7)
        let authority = afterScheme.prefix { $0 != "/" && $0 != "?" && $0 != "#" }
        guard !authority.isEmpty, !authority.contains("@"), !authority.contains("\\") else { return nil }
        guard let comps = URLComponents(string: trimmed),
              let scheme = comps.scheme?.lowercased(), scheme == "http" || scheme == "https",
              let host = comps.host, !host.isEmpty,
              comps.user == nil, comps.password == nil else {
            return nil
        }
        return comps.url
    }

    /// Builds the authorized WebSocket upgrade request for `/api/v1/machines/{mid}/preview/connect`, routed through `HubTransport`.
    static func connectRequest(
        machine: String,
        port: Int,
        path: String,
        viewport: PreviewViewport,
        config: ServerConfig
    ) -> URLRequest {
        var comps = URLComponents(url: config.url, resolvingAgainstBaseURL: false)!
        var base = comps.percentEncodedPath
        if base.hasSuffix("/") { base.removeLast() }
        let encMachine = machine.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? machine
        comps.percentEncodedPath = base + "/api/v1/machines/\(encMachine)/preview/connect"
        comps.scheme = config.url.scheme == "https" ? "wss" : "ws"
        let dprString = viewport.dpr.rounded() == viewport.dpr ? String(Int(viewport.dpr)) : String(viewport.dpr)
        var items = [
            URLQueryItem(name: "port", value: String(port)),
            URLQueryItem(name: "path", value: path.isEmpty ? "/" : path),
            URLQueryItem(name: "w", value: String(viewport.width)),
            URLQueryItem(name: "h", value: String(viewport.height)),
            URLQueryItem(name: "dpr", value: dprString),
        ]
        if viewport.mobile {
            items.append(URLQueryItem(name: "mobile", value: "1"))
        }
        comps.queryItems = items
        var req = URLRequest(url: HubTransport.resolve(comps.url!, hub: config.url), timeoutInterval: 40)
        config.authorize(&req)
        req.setValue(LanguageStore.shared.launched, forHTTPHeaderField: "Accept-Language")
        return req
    }

    /// Extracts a server-relative path (`/…`) from a localhost URL on `port`, or `nil` when pointing elsewhere.
    static func relativePath(from urlString: String, port: Int) -> String? {
        guard let url = URL(string: urlString) else { return nil }
        let addr = PreviewLogic.address(url, localPort: port)
        return addr.hasPrefix("/") ? addr : nil
    }

    #if os(iOS)
    /// Decodes a JPEG or PNG frame off the main actor using ImageIO (docs/apple/performance.md P1/P8), never calling
    /// `UIImage(data:)` on the main thread and bounding oversized images to 2x viewport pixel dimensions.
    nonisolated static func decodeImageOffMain(
        data: Data,
        format: PreviewFrameFormat,
        cssWidth: Int,
        cssHeight: Int
    ) -> PreviewStreamModel.DecodedFrame? {
        let _ = format
        guard let src = CGImageSourceCreateWithData(data as CFData, nil) else { return nil }
        let maxPixel = max(512, max(cssWidth, cssHeight) * 2)
        let props = CGImageSourceCopyPropertiesAtIndex(src, 0, nil) as? [CFString: Any]
        let srcW = props?[kCGImagePropertyPixelWidth] as? Int ?? 0
        let srcH = props?[kCGImagePropertyPixelHeight] as? Int ?? 0
        let cg: CGImage?
        if max(srcW, srcH) > maxPixel {
            let opts: [CFString: Any] = [
                kCGImageSourceCreateThumbnailFromImageAlways: true,
                kCGImageSourceCreateThumbnailWithTransform: true,
                kCGImageSourceThumbnailMaxPixelSize: maxPixel,
                kCGImageSourceShouldCacheImmediately: true,
            ]
            cg = CGImageSourceCreateThumbnailAtIndex(src, 0, opts as CFDictionary)
        } else {
            let opts: [CFString: Any] = [kCGImageSourceShouldCacheImmediately: true]
            cg = CGImageSourceCreateImageAtIndex(src, 0, opts as CFDictionary)
        }
        guard let cg else { return nil }
        let scale: CGFloat = cssWidth > 0 ? max(1, CGFloat(cg.width) / CGFloat(cssWidth)) : 1
        return PreviewStreamModel.DecodedFrame(
            image: UIImage(cgImage: cg, scale: scale, orientation: .up),
            pixelWidth: cg.width,
            pixelHeight: cg.height
        )
    }

    /// Decodes a PNG screenshot off the main actor using ImageIO (P8).
    nonisolated static func decodeShotOffMain(data: Data) -> UIImage? {
        guard let src = CGImageSourceCreateWithData(data as CFData, nil) else { return nil }
        let opts: [CFString: Any] = [kCGImageSourceShouldCacheImmediately: true]
        guard let cg = CGImageSourceCreateImageAtIndex(src, 0, opts as CFDictionary) else { return nil }
        return UIImage(cgImage: cg)
    }
    #endif
}

// MARK: - PreviewStreamModel

#if os(iOS)
private nonisolated struct RawSelectOpen: Decodable {
    var id: Int
    var rect: PreviewSelectRect?
    var selected: Int?
    var options: [PreviewSelectOption]?
}

private nonisolated struct RawStreamEnvelope: Decodable {
    var t: String
    var browser: String?
    var w: Int?
    var h: Int?
    var url: String?
    var title: String?
    var loading: Bool?
    var back: Bool?
    var forward: Bool?
    var error: String?
    var code: String?
    var kind: PreviewStreamDialog.Kind?
    var message: String?
    var defaultText: String?
    var on: Bool?
    var id: Int?
    var text: String?

    enum CodingKeys: String, CodingKey {
        case t, browser, w, h, url, title, loading, back, forward, error, code, kind, message, on, id, text
        case defaultText = "default"
    }
}

/// Observable controller for one streamed headless-browser preview session on iOS, with hub routing, socket creation,
/// image decoding, timing, and clipboard side effects injected through `Deps`.
@MainActor @Observable
final class PreviewStreamModel {
    struct DecodedFrame: Sendable {
        var image: UIImage
        var pixelWidth: Int
        var pixelHeight: Int
    }

    struct Deps {
        var config: @MainActor () -> ServerConfig?
        var openPort: @MainActor (String, Int) async throws -> Void
        var makeSocket: @MainActor (URLRequest) -> any PreviewStreamSocket
        var decodeFrame: @Sendable (Data, PreviewFrameFormat, Int, Int) async -> DecodedFrame?
        var decodeShot: @Sendable (Data) async -> UIImage?
        var sleep: @MainActor (Duration) async throws -> Void
        var copyToClipboard: @MainActor (String) -> Void

        static func live(group: String) -> Deps {
            Deps(
                config: { AppModel.shared.api(group)?.config },
                openPort: { machine, port in
                    guard let api = AppModel.shared.api(group) else {
                        throw APIError(status: 0, message: String(localized: "Not connected to a hub [Port Presence c03cfc]"))
                    }
                    _ = try await api.openPort(machine: machine, port: port)
                },
                makeSocket: { HubPreviewStreamSocket(request: $0) },
                decodeFrame: { data, format, w, h in
                    await Task.detached(priority: .userInitiated) {
                        PreviewStreamLogic.decodeImageOffMain(data: data, format: format, cssWidth: w, cssHeight: h)
                    }.value
                },
                decodeShot: { data in
                    await Task.detached(priority: .userInitiated) {
                        PreviewStreamLogic.decodeShotOffMain(data: data)
                    }.value
                },
                sleep: { try await Task.sleep(for: $0) },
                copyToClipboard: { UIPasteboard.general.string = $0 }
            )
        }
    }

    let machine: String
    let port: Int
    private(set) var connection: PreviewStreamConnection = .connecting
    private(set) var currentPath: String
    private(set) var pageURL: String = ""
    private(set) var pageTitle: String = ""
    private(set) var isLoading: Bool = true
    private(set) var canGoBack: Bool = false
    private(set) var canGoForward: Bool = false
    private(set) var pageSize: CGSize = .zero
    private(set) var frameImage: UIImage?
    private(set) var frameFormat: PreviewFrameFormat?
    private(set) var isPicking: Bool = false
    private(set) var isShooting: Bool = false
    private(set) var selectOpen: PreviewSelectState?
    /// Pre-grouped `<select>` sections computed once per `select_open` (P4: no grouping work in `body`).
    private(set) var selectGroups: [PreviewSelectGroup] = []
    private(set) var activeDialog: PreviewStreamDialog?
    private(set) var banner: PreviewStreamBanner?
    private(set) var unavailableReason: PreviewStreamUnavailableReason?

    @ObservationIgnored var onOpenExternal: ((URL) -> Void)?
    @ObservationIgnored var onPicked: ((PickedElement) -> Void)?
    @ObservationIgnored var onShot: ((UIImage) -> Void)?
    @ObservationIgnored var onShotFailed: ((String) -> Void)?
    @ObservationIgnored var onPickFailed: (() -> Void)?
    @ObservationIgnored var onUnavailable: ((PreviewStreamUnavailableReason) -> Void)?

    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var socket: (any PreviewStreamSocket)?
    @ObservationIgnored private var connectTask: Task<Void, Never>?
    @ObservationIgnored private var resizeTask: Task<Void, Never>?
    @ObservationIgnored private var viewport: PreviewViewport?
    @ObservationIgnored private var lastSentViewport: PreviewViewport?
    @ObservationIgnored private var running = false
    @ObservationIgnored private var reconnectAttempt = 0
    @ObservationIgnored private var terminalFailure = false
    @ObservationIgnored private var lastDrawnSeq: UInt32 = 0
    @ObservationIgnored private var shotSeq: UInt32 = 0
    @ObservationIgnored private var pendingShots: Set<UInt32> = []
    @ObservationIgnored private var generation = 0

    init(machine: String, port: Int, path: String = "/", deps: Deps) {
        self.machine = machine
        self.port = port
        self.currentPath = path.isEmpty ? "/" : path
        self.deps = deps
    }

    /// Starts streaming at `viewportSize` and `displayScale`. Safe to call repeatedly as layout settles.
    func start(viewportSize: CGSize, displayScale: CGFloat) {
        let vp = PreviewStreamLogic.clampedViewport(size: viewportSize, displayScale: displayScale, mobile: true)
        viewport = vp
        if running {
            updateViewport(size: viewportSize, displayScale: displayScale)
            return
        }
        running = true
        terminalFailure = false
        reconnectAttempt = 0
        openConnection()
    }

    /// Tears down the socket, cancels background tasks, and drops retained image buffers (P9/P11).
    func stop() {
        running = false
        generation += 1
        connectTask?.cancel()
        connectTask = nil
        resizeTask?.cancel()
        resizeTask = nil
        socket?.close()
        socket = nil
        pendingShots = []
        isShooting = false
        isPicking = false
        clearSelectLocally()
        activeDialog = nil
        frameImage = nil
    }

    /// Reopens the stream from a stopped, closed, or failed state.
    func reconnect() {
        guard let vp = viewport else { return }
        stop()
        connection = .connecting
        banner = nil
        start(viewportSize: CGSize(width: vp.width, height: vp.height), displayScale: CGFloat(vp.dpr))
    }

    /// Sends a debounced `size` update when quantized viewport dimensions or DPR change (P7).
    func updateViewport(size: CGSize, displayScale: CGFloat) {
        let next = PreviewStreamLogic.clampedViewport(size: size, displayScale: displayScale, mobile: true)
        let prev = viewport
        viewport = next
        if prev != nil && prev != next && selectOpen != nil {
            dismissSelect()
        }
        guard running, connection == .open, next != lastSentViewport else { return }
        resizeTask?.cancel()
        resizeTask = Task { @MainActor [weak self] in
            do { try await self?.deps.sleep(.milliseconds(150)) } catch { return }
            guard let self, self.running, self.connection == .open, let current = self.viewport, current != self.lastSentViewport else { return }
            self.lastSentViewport = current
            self.sendJSON([
                "t": "size",
                "w": current.width,
                "h": current.height,
                "dpr": current.dpr,
                "mobile": current.mobile,
            ])
        }
    }

    // MARK: Outbound user actions

    func tap(at point: CGPoint, in displayedRect: CGRect) {
        guard connection == .open else { return }
        if selectOpen != nil {
            dismissSelect()
            return
        }
        let targetSize = effectivePageSize
        let p = PreviewStreamLogic.toPage(point: point, in: displayedRect, pageSize: targetSize)
        let x = Int(p.x), y = Int(p.y)
        sendJSON(["t": "mouse", "type": "move", "x": x, "y": y, "button": "none", "buttons": 0, "clicks": 0, "mods": 0])
        sendJSON(["t": "mouse", "type": "down", "x": x, "y": y, "button": "left", "buttons": 1, "clicks": 1, "mods": 0])
        sendJSON(["t": "mouse", "type": "up", "x": x, "y": y, "button": "left", "buttons": 0, "clicks": 1, "mods": 0])
    }

    func scroll(delta: CGSize, at point: CGPoint, in displayedRect: CGRect) {
        guard connection == .open else { return }
        if selectOpen != nil {
            dismissSelect()
            return
        }
        let targetSize = effectivePageSize
        let p = PreviewStreamLogic.toPage(point: point, in: displayedRect, pageSize: targetSize)
        let d = PreviewStreamLogic.wheelDelta(translation: delta, in: displayedRect, pageSize: targetSize)
        guard d.width != 0 || d.height != 0 else { return }
        sendJSON([
            "t": "wheel",
            "x": Int(p.x),
            "y": Int(p.y),
            "dx": d.width,
            "dy": d.height,
            "mods": 0,
        ])
    }

    func sendText(_ text: String) {
        guard connection == .open, selectOpen == nil, !text.isEmpty else { return }
        sendJSON(["t": "text", "text": text])
    }

    func sendKey(key: String, code: String, keyCode: Int, text: String? = nil, mods: Int = 0) {
        guard connection == .open, selectOpen == nil else { return }
        var down: [String: Any] = [
            "t": "key",
            "type": "down",
            "key": key,
            "code": code,
            "key_code": keyCode,
            "mods": mods,
            "location": 0,
        ]
        if let text, !text.isEmpty { down["text"] = text }
        let up: [String: Any] = [
            "t": "key",
            "type": "up",
            "key": key,
            "code": code,
            "key_code": keyCode,
            "mods": mods,
            "location": 0,
        ]
        sendJSON(down)
        sendJSON(up)
    }

    func goBack() {
        guard connection == .open else { return }
        dismissSelect()
        sendJSON(["t": "back"])
    }

    func goForward() {
        guard connection == .open else { return }
        dismissSelect()
        sendJSON(["t": "forward"])
    }

    func reload() {
        if case .error = connection { reconnect(); return }
        if connection == .closed { reconnect(); return }
        guard connection == .open else { return }
        dismissSelect()
        sendJSON(["t": "reload"])
    }

    func navigate(to url: String) {
        let trimmed = url.trimmingCharacters(in: .whitespacesAndNewlines)
        guard connection == .open, !trimmed.isEmpty else { return }
        dismissSelect()
        sendJSON(["t": "nav", "url": trimmed])
    }

    func setPicking(_ on: Bool) {
        guard connection == .open else { return }
        dismissSelect()
        isPicking = on
        sendJSON(["t": "pick", "on": on])
    }

    func requestShot(full: Bool) {
        guard connection == .open else { return }
        shotSeq &+= 1
        let id = shotSeq
        pendingShots.insert(id)
        isShooting = true
        sendJSON(["t": "shot", "id": Int(id), "full": full])
    }

    func chooseSelect(index: Int) {
        guard let cur = selectOpen else { return }
        guard let opt = cur.options.first(where: { $0.index == index }), !opt.disabled else { return }
        clearSelectLocally()
        // Explicitly always include `index`, including 0.
        sendJSON(["t": "select_choose", "id": cur.id, "index": index])
    }

    func dismissSelect() {
        guard let cur = selectOpen else { return }
        clearSelectLocally()
        sendJSON(["t": "select_close", "id": cur.id])
    }

    func answerDialog(accept: Bool, text: String? = nil) {
        guard activeDialog != nil else { return }
        activeDialog = nil
        var payload: [String: Any] = ["t": "dialog", "accept": accept]
        if accept, let text { payload["text"] = text }
        sendJSON(payload)
    }

    func dismissBanner() {
        banner = nil
    }

    // MARK: Connection lifecycle

    private var effectivePageSize: CGSize {
        if pageSize.width > 0 && pageSize.height > 0 { return pageSize }
        if let vp = viewport { return CGSize(width: vp.width, height: vp.height) }
        return CGSize(width: 390, height: 844)
    }

    private func openConnection() {
        connectTask?.cancel()
        generation += 1
        let gen = generation
        connectTask = Task { @MainActor [weak self] in
            guard let self, self.running, self.generation == gen else { return }
            guard let cfg = self.deps.config(), let vp = self.viewport else {
                self.setTerminalError(code: "offline", message: String(localized: "Not connected to a hub [Port Presence c03cfc]"))
                return
            }
            // Ensure the port is authorized on the hub and catch stopped port / offline computer early.
            do {
                try await self.deps.openPort(self.machine, self.port)
            } catch let e as APIError {
                guard self.running, self.generation == gen else { return }
                if e.status == 404 {
                    self.setTerminalError(code: "port_gone", message: e.localizedDescription)
                    return
                }
                if e.status == 503 {
                    self.setTerminalError(code: "offline", message: e.localizedDescription)
                    return
                }
                if e.status == 401 {
                    self.setTerminalError(code: "unauthorized", message: e.localizedDescription)
                    return
                }
                if e.status == 403 {
                    self.setTerminalError(code: "forbidden", message: e.localizedDescription)
                    return
                }
                if e.code == "node_outdated" {
                    self.setTerminalError(code: "node_outdated", message: e.localizedDescription)
                    return
                }
            } catch {
                guard self.running, self.generation == gen else { return }
                if error.isCancellation { return }
            }

            guard self.running, self.generation == gen else { return }
            self.lastDrawnSeq = 0
            self.lastSentViewport = vp
            let req = PreviewStreamLogic.connectRequest(
                machine: self.machine,
                port: self.port,
                path: self.currentPath,
                viewport: vp,
                config: cfg
            )
            let sock = self.deps.makeSocket(req)
            self.socket = sock
            sock.open { [weak self] event in
                guard let self, self.generation == gen else { return }
                self.handleSocketEvent(event, socket: sock, generation: gen)
            }
        }
    }

    private func handleSocketEvent(_ event: PreviewStreamSocketEvent, socket sock: any PreviewStreamSocket, generation gen: Int) {
        switch event {
        case .opened:
            if case .error = connection { return }
            connection = .open
        case .text(let text):
            handleTextMessage(text, socket: sock)
        case .data(let data):
            handleBinaryMessage(data, socket: sock, generation: gen)
        case .closed(let status, let reason):
            if socket === sock { socket = nil }
            clearSelectLocally()
            activeDialog = nil
            pendingShots = []
            isShooting = false
            isPicking = false
            guard running, !terminalFailure else { return }
            if let status {
                switch status {
                case 409:
                    setTerminalError(code: "node_outdated", message: reason ?? "")
                    return
                case 404:
                    setTerminalError(code: "port_gone", message: reason ?? "")
                    return
                case 403:
                    setTerminalError(code: "forbidden", message: reason ?? "")
                    return
                case 401:
                    setTerminalError(code: "unauthorized", message: reason ?? "")
                    return
                case 503:
                    setTerminalError(code: "offline", message: reason ?? "")
                    return
                default:
                    break
                }
            }
            reconnectAttempt += 1
            connection = .reconnecting(attempt: reconnectAttempt)
            let delaySeconds = min(15, 1 << min(reconnectAttempt - 1, 4))
            connectTask?.cancel()
            connectTask = Task { @MainActor [weak self] in
                do { try await self?.deps.sleep(.seconds(delaySeconds)) } catch { return }
                guard let self, self.running, self.generation == gen, !self.terminalFailure else { return }
                self.openConnection()
            }
        }
    }

    private func handleBinaryMessage(_ data: Data, socket sock: any PreviewStreamSocket, generation gen: Int) {
        guard let parsed = PreviewStreamLogic.parseBinary(data) else { return }
        switch parsed {
        case .frame(let seq, let w, let h, let payload):
            let fmt = PreviewStreamLogic.frameFormat(payload)
            Task { @MainActor [weak self] in
                defer {
                    if let self, self.socket === sock {
                        self.sendJSON(["t": "ack", "seq": Int(seq)], on: sock)
                    }
                }
                guard let self, let fmt, w > 0, h > 0 else { return }
                guard let decoded = await self.deps.decodeFrame(payload, fmt, w, h) else { return }
                guard self.running, self.generation == gen, self.socket === sock else { return }
                guard seq >= self.lastDrawnSeq else { return }
                self.lastDrawnSeq = seq
                self.frameImage = decoded.image
                self.frameFormat = fmt
                let cssSize = CGSize(width: w, height: h)
                if self.pageSize != cssSize { self.pageSize = cssSize }
            }
        case .shot(let id, let payload):
            guard pendingShots.remove(id) != nil else { return }
            isShooting = !pendingShots.isEmpty
            Task { @MainActor [weak self] in
                guard let self else { return }
                if let image = await self.deps.decodeShot(payload) {
                    guard self.running, self.generation == gen else { return }
                    self.onShot?(image)
                } else {
                    self.onShotFailed?(String(localized: "Couldn't take the screenshot. Try again."))
                }
            }
        }
    }

    private func handleTextMessage(_ text: String, socket sock: any PreviewStreamSocket) {
        guard let rawData = text.data(using: .utf8),
              let msg = try? JSONDecoder().decode(RawStreamEnvelope.self, from: rawData) else {
            return
        }
        switch msg.t {
        case "ready":
            reconnectAttempt = 0
            connection = .open
            sendJSON(["t": "features", "select": true, "png": true], on: sock)
            if let vp = viewport, let w = msg.w, let h = msg.h, (w != vp.width || h != vp.height) {
                lastSentViewport = vp
                sendJSON(["t": "size", "w": vp.width, "h": vp.height, "dpr": vp.dpr, "mobile": vp.mobile], on: sock)
            }
        case "page":
            if let url = msg.url, !url.isEmpty {
                pageURL = url
                if let rel = PreviewStreamLogic.relativePath(from: url, port: port) {
                    currentPath = rel
                }
            }
            if let title = msg.title { pageTitle = title }
            if let loading = msg.loading { isLoading = loading }
            if let back = msg.back { canGoBack = back }
            if let forward = msg.forward { canGoForward = forward }
            if case .failed = banner { banner = nil }
            if case .crashed = banner { banner = nil }
        case "external":
            if let urlStr = msg.url, let safe = PreviewStreamLogic.safeExternalURL(urlStr) {
                onOpenExternal?(safe)
            }
        case "failed":
            banner = .failed(url: msg.url ?? "", error: msg.error ?? "")
        case "crashed":
            banner = .crashed
        case "notice":
            if let code = msg.code, !code.isEmpty { banner = .notice(code: code) }
        case "dialog":
            if let kind = msg.kind {
                activeDialog = PreviewStreamDialog(kind: kind, message: msg.message ?? "", defaultText: msg.defaultText ?? "")
            }
        case "dialog_closed":
            activeDialog = nil
        case "pick":
            isPicking = msg.on ?? false
        case "picked":
            isPicking = false
            if let el = try? JSONDecoder().decode(PickedElement.self, from: rawData) {
                onPicked?(el)
            }
        case "pick_failed":
            isPicking = false
            onPickFailed?()
        case "shot_failed":
            if let id = msg.id { pendingShots.remove(UInt32(max(0, id))) }
            isShooting = !pendingShots.isEmpty
            onShotFailed?(msg.error ?? String(localized: "Couldn't take the screenshot. Try again."))
        case "copied":
            if let copied = msg.text, !copied.isEmpty { deps.copyToClipboard(copied) }
        case "select_open":
            if let raw = try? JSONDecoder().decode(RawSelectOpen.self, from: rawData),
               let normalized = Self.normalizeSelectOpen(raw) {
                selectOpen = normalized
                selectGroups = PreviewStreamLogic.selectGroups(from: normalized.options)
            }
        case "select_close":
            if let id = msg.id, selectOpen?.id == id {
                clearSelectLocally()
            }
        case "closed":
            terminalFailure = true
            clearSelectLocally()
            activeDialog = nil
            connection = .closed
        case "error":
            setTerminalError(code: msg.code ?? "failed", message: msg.error ?? "")
        default:
            break
        }
    }

    private func setTerminalError(code: String, message: String) {
        terminalFailure = true
        clearSelectLocally()
        activeDialog = nil
        connection = .error(code: code, message: message)
        if code == "no_browser" {
            unavailableReason = .noBrowser
            onUnavailable?(.noBrowser)
        } else if code == "node_outdated" {
            unavailableReason = .nodeOutdated
            onUnavailable?(.nodeOutdated)
        }
    }

    private func clearSelectLocally() {
        selectOpen = nil
        selectGroups = []
    }

    private func sendJSON(_ object: [String: Any], on target: (any PreviewStreamSocket)? = nil) {
        let sock = target ?? socket
        guard let sock,
              let data = try? JSONSerialization.data(withJSONObject: object),
              let text = String(data: data, encoding: .utf8) else {
            return
        }
        sock.send(text: text)
    }

    private static func normalizeSelectOpen(_ raw: RawSelectOpen) -> PreviewSelectState? {
        guard raw.id > 0, let rect = raw.rect, let rawOptions = raw.options else { return nil }
        func cap(_ s: String) -> String {
            s.count <= PreviewStreamLogic.maxSelectLabel ? s : String(s.prefix(PreviewStreamLogic.maxSelectLabel))
        }
        var sanitized: [PreviewSelectOption] = []
        sanitized.reserveCapacity(min(rawOptions.count, PreviewStreamLogic.maxSelectOptions))
        for opt in rawOptions.prefix(PreviewStreamLogic.maxSelectOptions) where opt.index >= 0 {
            let group = opt.group.flatMap { $0.isEmpty ? nil : cap($0) }
            sanitized.append(PreviewSelectOption(index: opt.index, label: cap(opt.label), disabled: opt.disabled, group: group))
        }
        return PreviewSelectState(
            id: raw.id,
            rect: PreviewSelectRect(
                x: rect.x.isFinite ? rect.x : 0,
                y: rect.y.isFinite ? rect.y : 0,
                w: rect.w.isFinite ? max(0, rect.w) : 0,
                h: rect.h.isFinite ? max(0, rect.h) : 0
            ),
            selected: raw.selected ?? -1,
            options: sanitized
        )
    }
}
#endif
