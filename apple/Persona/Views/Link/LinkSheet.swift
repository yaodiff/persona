import BeautifulUI
import CryptoKit
import Security
import SwiftUI
import UIKit
import WebKit
import PersonaWire

// Links open inside the app, Telegram-style (CLAUDE.md): a sheet that rises to 60%, drags up to full screen and down
// to close, with the chat behind it still usable. Feishu docs open in the native reader and company-feed links as the
// post while the hub has them (FeishuLinks.route, LarkFeatures); any other page in a
// WKWebView taken from a pool that preloads the links of the latest answers. Text selected on a page can go to the
// agent as an edit request (Edit with AI) or ride along with the next message (Add as Context), both as <persona-context>.

/// A piece of a page the user picked, carried in a message as
/// `<persona-context kind="context|edit" title="…" url="…">text</persona-context>`, the text through `ContextText.inBlock`
/// so it cannot close the tag (#3831).
nonisolated struct ContextRef: Identifiable, Hashable, Sendable, Codable {
    var id = UUID()
    var kind: String // "context" | "edit"
    var title: String
    var url: String
    var text: String
    /// A quoted message's id in its chat (`kind` "quote"), so its block in the sent bubble leads back to it.
    var msg = ""
    /// Which part of the message is quoted (`part="selection"`: words picked in Select Text, #4549); nil for all of it.
    /// Readers that do not know it read the block as a quote of the message.
    var part: String? = nil
    /// The block's other attributes as written: a forward's metadata (`ForwardMessage`, docs/protocol/protocol.md
    /// "Forwarded messages"), a workspace's machine and folder. Kept so a block read back writes the same attributes.
    var extra: [String: String]? = nil
    /// The composer's line under a forward over the box: where it comes from and where it goes. Shown, never sent.
    var route: String? = nil

    var host: String { URL(string: url)?.host ?? url }

    /// The extra attributes in the order they are written: a forward's metadata and a workspace's location first, then
    /// any others by name.
    static let extraOrder = ["role", "author", "chat", "project", "machine", "cwd", "branch", "time"]

    var xml: String {
        func esc(_ s: String) -> String {
            s.replacingOccurrences(of: "&", with: "&amp;").replacingOccurrences(of: "\"", with: "&quot;")
                .replacingOccurrences(of: "<", with: "&lt;").replacingOccurrences(of: ">", with: "&gt;")
        }
        let msgAttr = msg.isEmpty ? "" : " msg=\"\(esc(msg))\""
        let partAttr = part.map { " part=\"\(esc($0))\"" } ?? ""
        let extras = extra ?? [:]
        let names = Self.extraOrder.filter { extras[$0] != nil } + extras.keys.filter { !Self.extraOrder.contains($0) }.sorted()
        let extraAttrs = names.compactMap { name in extras[name].map { " \(name)=\"\(esc($0))\"" } }.joined()
        return "<persona-context kind=\"\(kind)\" title=\"\(esc(title))\" url=\"\(esc(url))\"\(msgAttr)\(partAttr)\(extraAttrs)>\n\(ContextText.inBlock(text))\n</persona-context>"
    }

    /// One `<persona-context>` block, its attributes in any order and any of them missing (a Lark /agent request's context has no url).
    private static let tagRe = try! NSRegularExpression(pattern: "<" + HubNaming.contextPattern + #"\b([^>]*)>\s*([\s\S]*?)\s*</"# + HubNaming.contextPattern + #">\s*"#)
    private static let attrRe = try! NSRegularExpression(pattern: #"(\w+)="([^"]*)""#)

    /// The attributes that have fields of their own; the rest go to `extra`.
    private static let ownAttributes: Set<String> = ["kind", "title", "url", "msg", "part"]

    /// The hub's mid-turn interject note title (server/internal/prompts/assistant/interject.md, pinned by
    /// assistant_interject_test.go): model instructions in a context block, never a quote the owner picked (#6595).
    static let interjectTitle = "插话" // i18n-ignore: canonical hub prompt title

    /// The contexts in a message and the message without them.
    private static let memo = TextMemo<(refs: [ContextRef], rest: String)>()

    static func parse(_ text: String) -> (refs: [ContextRef], rest: String) { memo(text, scan) }

    private static func unescape(_ s: String) -> String {
        s.replacingOccurrences(of: "&quot;", with: "\"").replacingOccurrences(of: "&lt;", with: "<")
            .replacingOccurrences(of: "&gt;", with: ">").replacingOccurrences(of: "&amp;", with: "&")
    }

    private static func scan(_ text: String) -> (refs: [ContextRef], rest: String) {
        let ns = text as NSString
        var refs: [ContextRef] = []
        for m in tagRe.matches(in: text, range: NSRange(location: 0, length: ns.length)) {
            let attrs = ns.substring(with: m.range(at: 1)) as NSString
            var a: [String: String] = [:]
            for am in attrRe.matches(in: attrs as String, range: NSRange(location: 0, length: attrs.length)) {
                a[attrs.substring(with: am.range(at: 1))] = unescape(attrs.substring(with: am.range(at: 2)))
            }
            // Provenance and protected-user sentinels are transport metadata, never selectable context cards.
            guard a["kind"] != "message-source" else { continue }
            // The hub's interject note is instructions for the model, not an owner-bubble quote (#6595).
            guard (a["kind"] ?? "context") != "context" || a["title"] != interjectTitle else { continue }
            let extra = a.filter { !Self.ownAttributes.contains($0.key) }
            refs.append(ContextRef(kind: a["kind"] ?? "context", title: a["title"] ?? "", url: a["url"] ?? "",
                                   text: ns.substring(with: m.range(at: 2)), msg: a["msg"] ?? "", part: a["part"],
                                   extra: extra.isEmpty ? nil : extra))
        }
        let rest = tagRe.stringByReplacingMatches(in: text, range: NSRange(location: 0, length: ns.length), withTemplate: "")
        return (refs, rest.trimmingCharacters(in: .whitespacesAndNewlines))
    }

    /// What the row of a message's contexts says, folded: the one context's title, or how many.
    static func summary(_ refs: [ContextRef]) -> String {
        if refs.count == 1, let r = refs.first { return r.kind == "edit" ? String(localized: "To Edit · \(r.label)") : String(localized: "Context · \(r.label)") }
        return String(localized: "Context · \(refs.count) items")
    }

    var label: String { title.isEmpty ? (url.isEmpty ? String(localized: "Quote") : host) : title }

    var icon: String {
        switch kind {
        case "instructions": Theme.Icon.info
        case "doc": Theme.Icon.document
        case "edit": Theme.Icon.ai
        case "workspace": Theme.Icon.folder
        case ForwardMessage.kind: Theme.Icon.forwardMessage
        default: Theme.Icon.quote
        }
    }
}

struct LinkTarget: Identifiable, Hashable {
    var url: URL
    /// A machine's port forwarded to the phone (docs/operations/ports.md): what to call it ("machine name :5173"); the url is the
    /// phone's loopback, which only this app serves.
    var forwarded: String? = nil
    /// The manager its native view reads through (the chat's or inbox item's it was tapped in, docs/assistant/managers.md
    /// "Clients"); nil: the default's, nothing named.
    var manager: String? = nil
    var id: String { manager.map { $0 + "\u{0}" + url.absoluteString } ?? url.absoluteString }
}

extension EnvironmentValues {
    /// Opens a link in the in-app sheet (set by ChatView).
    @Entry var openLink: ((URL) -> Void)? = nil
    /// Inside the link sheet: hands picked text to the agent (text, "edit" | "context"). Native document views
    /// (the Feishu reader) show Edit with AI / Add as Context on long press when it is set.
    @Entry var docPick: ((String, String) -> Void)? = nil
}

/// The sheet's content: header (title, host, actions) over the page.
struct LinkSheet: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @Environment(\.scenePhase) private var scenePhase
    let target: LinkTarget
    var key: SessionKey? = nil
    /// Opens another of the session's dev servers in the card (ChatView): a forwarded page's subtitle lists them (#150).
    var choosePreview: ((PreviewOption) -> Void)? = nil
    @State private var activeURL: URL
    @State private var title = ""
    @State private var choosing = false
    @State private var chosen: PreviewOption?
    @State private var childLink: LinkTarget?
    @State private var editing: ContextRef?
    @State private var instruction = ""
    /// Opens full screen (2026-10-28: half a screen was too little to read); pull down to 0.6 to see the chat again.
    @State private var detent: PresentationDetent = .large

    init(target: LinkTarget, key: SessionKey? = nil, choosePreview: ((PreviewOption) -> Void)? = nil) {
        self.target = target
        self.key = key
        self.choosePreview = choosePreview
        _activeURL = State(initialValue: target.url)
    }

    /// Feishu links with a native view (documents, company-feed posts), while the hub has them.
    private var feishuRoute: FeishuRoute? { FeishuLinks.route(target.url, lark: model.larkFeatures, manager: target.manager) }
    private var isFeishuDoc: Bool { if case .doc = feishuRoute { true } else { false } }

    var body: some View {
        VStack(spacing: 0) {
            header
            Divider()
            if let feishuRoute {
                NavigationStack {
                    FeishuRouteView(route: feishuRoute)
                        .toolbar(.hidden, for: .navigationBar)
                        .feishuRoutes()
                        .environment(\.docPick) { text, kind in pick(text, kind: kind) }
                }
            } else {
                #if os(iOS)
                if let key, target.forwarded != nil {
                    // A machine's dev server from a chat: the preview's bar under it (#150).
                    PreviewPage(
                        url: activeURL,
                        key: key,
                        title: $title,
                        forwardedLabel: target.forwarded,
                        onRetry: { reloadPage() },
                        onOpenExternal: { openExternalLink($0) },
                        onReprobePort: { await model.reprobeForwardedPort($0) }
                    ) { selected, kind in pick(selected, kind: kind) }
                } else {
                    page
                }
                #else
                page
                #endif
            }
        }
        .presentationDetents([.fraction(0.6), .large], selection: $detent)
        .presentationDragIndicator(.visible)
        .presentationBackground(Theme.pageBackground) // pages with a clear background let the chat show through
        .presentationBackgroundInteraction(.enabled(upThrough: .fraction(0.6))) // the chat stays usable behind
        .presentationContentInteraction(.resizes) // a swipe up grows the sheet before it scrolls the page
        .onChange(of: target.url) { _, next in activeURL = next }
        .onChange(of: scenePhase) { _, newPhase in
            if newPhase == .active, target.forwarded != nil {
                Task { await ensureForwardedReady(reloadIfMoved: true) }
            }
        }
        #if os(iOS)
        .sheet(isPresented: $choosing, onDismiss: {
            if let o = chosen { chosen = nil; choosePreview?(o) }
        }) {
            if let key, let cwd = model.session(key)?.cwd {
                PreviewChooser(key: key, cwd: cwd) { o in chosen = o; choosing = false }
            }
        }
        #endif
        .sheet(item: $childLink) { child in
            LinkSheet(target: child, key: key)
        }
        .alert("Have an Agent Edit This", isPresented: Binding(get: { editing != nil }, set: { if !$0 { editing = nil } })) {
            TextField(String(localized: "How should it change?"), text: $instruction)
            Button("Cancel", role: .cancel) { instruction = "" }
            Button("Send") { sendEdit() }
        } message: {
            Text(editing.map { String($0.text.prefix(120)) } ?? "")
        }
    }

    private var page: some View {
        WebPageView(
            url: activeURL,
            forwardedLabel: target.forwarded,
            title: $title,
            onRetry: { reloadPage() },
            onOpenExternal: { openExternalLink($0) },
            onReprobePort: target.forwarded != nil ? { await model.reprobeForwardedPort($0) } : nil
        ) { selected, kind in pick(selected, kind: kind) }
            .ignoresSafeArea(.container, edges: .bottom) // full screen: the page runs under the home indicator
    }

    /// A forwarded page whose chat's hub has previews: its subtitle lists the session's other servers.
    private var canChoose: Bool {
        #if os(iOS)
        guard let key, target.forwarded != nil, choosePreview != nil else { return false }
        return model.features(key.group).contains(PreviewLogic.feature)
        #else
        false
        #endif
    }

    private var header: some View {
        HStack(spacing: Theme.Space.m) {
            // The way out first, as on every presented page (#9653); the page's own actions after the title.
            Button { dismiss() } label: { ThemedIcon(Theme.Icon.close).sheetBarLabel() }
                .accessibilityLabel("Close")
                .accessibilityIdentifier("link-close")
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(title.isEmpty ? (isFeishuDoc ? String(localized: "Lark Doc") : feishuRoute != nil ? String(localized: "Moments") : (target.forwarded != nil ? nil : target.url.host) ?? String(localized: "Web Pages")) : title)
                    .font(Theme.Font.secondaryStrong).lineLimit(1)
                if canChoose {
                    Button { Haptics.tap(); choosing = true } label: {
                        HStack(spacing: Theme.Space.xxs) {
                            Text(target.forwarded ?? "").lineLimit(1)
                            ThemedIcon(Theme.Icon.expand).font(Theme.Font.labelStrong)
                        }
                        .font(Theme.Font.label)
                        .foregroundStyle(Theme.accent)
                        // A caption line hit like a row, the header keeping its height (#6577), and over the title above
                        // it too: the header has only `Space.s` under it, and the page below takes its own taps.
                        .padding(.top, Theme.Space.l)
                        .tapTargetOutset()
                        .padding(.top, -Theme.Space.l)
                    }
                    .buttonStyle(.plain)
                    .accessibilityHint(String(localized: "Lists this session's dev servers"))
                    .accessibilityIdentifier("link-servers")
                } else {
                    Text(target.forwarded ?? target.url.host ?? target.url.absoluteString).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            Spacer(minLength: 6)
            if isFeishuDoc, key != nil {
                // The native reader has no custom selection menu: the whole doc can still go along.
                Button { pick("", kind: "context") } label: { ThemedIcon(Theme.Icon.addContext) }
                    .accessibilityLabel("Add this document as context")
            }
            if feishuRoute == nil {
                Button { Haptics.tap(); reloadPage() } label: { ThemedIcon(Theme.Icon.reload) }
                    .accessibilityLabel("Refresh")
                    .accessibilityIdentifier("link-reload")
            }
            Menu {
                Button { UIPasteboard.general.url = activeURL; Haptics.success() } label: { Label("Copy Link", systemImage: Theme.Icon.link) }
                if target.forwarded == nil { browserItems } // the phone's loopback means nothing to another app
            } label: { ThemedIcon(Theme.Icon.moreMenu) }
        }
        .font(Theme.Font.title)
        .padding(.horizontal, Theme.Space.l)
        .padding(.top, Theme.Space.l)
        .padding(.bottom, Theme.Space.s)
        // A bar: it grows with the text up to the largest standard size, as the viewers' bars do. Beyond, its buttons
        // left the title and the server chooser a letter each, and the room it took came off the page and the
        // preview's comment panel above the keyboard (#5114); the page under it does not grow with the text anyway.
        .dynamicTypeSize(...DynamicTypeSize.xxxLarge)
    }

    @ViewBuilder private var browserItems: some View {
        Button { UIApplication.shared.open(target.url) } label: { Label("Open in Safari", systemImage: Theme.Icon.safari) }
        ShareLink(item: target.url) { Label("Share", systemImage: Theme.Icon.share) }
    }

    private func pick(_ text: String, kind: String) {
        guard let key else {
            model.toast = Toast(String(localized: "Open the link from a chat to hand it to an agent."), style: .info)
            return
        }
        // A forwarded port's host is the phone's loopback: name it as the header does ("machine :5173", #3378).
        let ref = ContextRef(kind: kind, title: title.isEmpty ? (target.forwarded ?? target.url.host ?? "") : title, url: target.url.absoluteString,
                             text: text.isEmpty ? "（整篇）" : text) // i18n-ignore: sentinel in the context XML, compared in QuoteViews
        if kind == "edit" {
            instruction = ""
            editing = ref
        } else {
            model.composerContexts[key, default: []].append(ref)
            Haptics.success()
            model.toast = Toast(String(localized: "Added as context. It'll be sent with your next message."), style: .success)
        }
    }

    private func sendEdit() {
        guard let ref = editing, let key else { return }
        let ask = instruction.trimmingCharacters(in: .whitespacesAndNewlines)
        model.sendNow(key, text: ref.xml + "\n\n" + (ask.isEmpty ? "按上面选中的内容修改。" : "按要求修改上面选中的内容：" + ask)) // i18n-ignore
        instruction = ""
        editing = nil
    }

    /// Rebuilds an invalidated loopback forward listener if needed before reloading the page.
    private func reloadPage() {
        guard target.forwarded != nil || PortForwarder.shared.key(for: activeURL) != nil else {
            WebViewPool.shared.reload(activeURL)
            return
        }
        let currentURL = activeURL
        Task {
            PortForwarder.shared.clearError(for: currentURL)
            do {
                let readyURL = try await PortForwarder.shared.ensureReady(for: currentURL)
                if readyURL != currentURL { activeURL = readyURL }
                WebViewPool.shared.reload(currentURL, at: readyURL)
            } catch {
                WebViewPool.shared.existing(for: currentURL)?.pageState.loadError = .from(portError: .listenerFailed)
            }
        }
    }

    private func ensureForwardedReady(reloadIfMoved: Bool) async {
        let currentURL = activeURL
        guard let readyURL = try? await PortForwarder.shared.ensureReady(for: currentURL) else { return }
        if readyURL != currentURL {
            activeURL = readyURL
            if reloadIfMoved { WebViewPool.shared.reload(currentURL, at: readyURL) }
        }
    }

    /// Handles external links opened via `target="_blank"` or `window.open` using the existing in-app link rules.
    private func openExternalLink(_ url: URL) {
        if let opened = model.openOwnLink(url) {
            if case .sheet(let page) = opened { model.linkSheet = page }
            return
        }
        guard let scheme = url.scheme?.lowercased(), scheme == "http" || scheme == "https" else { return }
        childLink = LinkTarget(url: url, manager: target.manager)
    }
}

// MARK: - Web views

/// Session-scoped in-memory trust for self-signed certificates on loopback ports forwarded by `PortForwarder`.
/// Never persisted to disk; only matches the exact `(host, port)` and leaf certificate fingerprint the user confirmed.
@MainActor
final class LoopbackCertificateTrustStore {
    static let shared = LoopbackCertificateTrustStore()
    private var trusted: [String: Set<Data>] = [:]

    func isTrusted(host: String, port: Int, trust: SecTrust) -> Bool {
        guard let digest = Self.leafDigest(of: trust) else { return false }
        return trusted[Self.key(host: host, port: port)]?.contains(digest) == true
    }

    func trust(host: String, port: Int, trust: SecTrust) {
        guard let digest = Self.leafDigest(of: trust) else { return }
        trusted[Self.key(host: host, port: port), default: []].insert(digest)
    }

    func reset() { trusted = [:] }

    private static func key(host: String, port: Int) -> String { "\(host.lowercased()):\(port)" }

    private static func leafDigest(of trust: SecTrust) -> Data? {
        guard let chain = SecTrustCopyCertificateChain(trust) as? [SecCertificate],
              let leaf = chain.first else { return nil }
        let der = SecCertificateCopyData(leaf) as Data
        return Data(SHA256.hash(data: der))
    }
}

/// Evaluates system TLS trust off the main actor (docs/apple/performance.md P1) with network fetches disabled.
private nonisolated func evaluateLoopbackTrustOffMain(_ trust: SecTrust) async -> Bool {
    nonisolated(unsafe) let ref = trust
    return await Task.detached(priority: .userInitiated) {
        SecTrustSetNetworkFetchAllowed(ref, false)
        return SecTrustEvaluateWithError(ref, nil)
    }.value
}

/// User-facing error rendered over a failed or terminated WebView using `EmptyStateView`.
struct WebPageError: Equatable {
    var symbol: String
    var title: String
    var message: String

    @MainActor static func from(portError: PortForwardError) -> WebPageError {
        WebPageError(symbol: portError.symbol, title: portError.title, message: portError.message)
    }

    @MainActor static var declinedSelfSigned: WebPageError {
        WebPageError(
            symbol: Theme.Icon.error,
            title: String(localized: "Untrusted Local Certificate"),
            message: String(localized: "This dev server uses a self-signed certificate that wasn't trusted. Tap Retry if you want to trust it for this session.")
        )
    }

    @MainActor static var processTerminated: WebPageError {
        WebPageError(
            symbol: Theme.Icon.error,
            title: String(localized: "Page Reload Needed"),
            message: String(localized: "The web page stopped unexpectedly. Tap Retry to load it again.")
        )
    }

    @MainActor static func from(navigationError error: Error, forwardedKey: PortForwarder.Key?, declinedSelfSigned: Bool) -> WebPageError {
        if declinedSelfSigned { return .declinedSelfSigned }
        if let forwardedKey, let recorded = PortForwarder.shared.forward(forwardedKey)?.lastError {
            return .from(portError: recorded)
        }
        let ns = error as NSError
        if ns.domain == NSURLErrorDomain {
            switch ns.code {
            case NSURLErrorNotConnectedToInternet:
                return WebPageError(
                    symbol: Theme.Icon.offline,
                    title: String(localized: "No Internet Connection"),
                    message: String(localized: "Check your network connection and try again.")
                )
            case NSURLErrorTimedOut:
                return WebPageError(
                    symbol: Theme.Icon.error,
                    title: String(localized: "Request Timed Out"),
                    message: String(localized: "The page took too long to respond. Try again.")
                )
            case NSURLErrorServerCertificateUntrusted, NSURLErrorServerCertificateHasUnknownRoot,
                 NSURLErrorServerCertificateHasBadDate, NSURLErrorServerCertificateNotYetValid,
                 NSURLErrorSecureConnectionFailed:
                return WebPageError(
                    symbol: Theme.Icon.error,
                    title: String(localized: "Certificate Not Trusted"),
                    message: String(localized: "This site's security certificate isn't trusted.")
                )
            case NSURLErrorCannotConnectToHost, NSURLErrorNetworkConnectionLost:
                if forwardedKey != nil {
                    return WebPageError(
                        symbol: Theme.Icon.error,
                        title: String(localized: "Couldn't Open Preview"),
                        message: String(localized: "Couldn't reach the server on your computer. Make sure it's still running and try again.")
                    )
                }
                return WebPageError(
                    symbol: Theme.Icon.error,
                    title: String(localized: "Couldn't Load Page"),
                    message: String(localized: "Couldn't connect to this page. Check your connection and try again.")
                )
            default:
                break
            }
        }
        return WebPageError(
            symbol: Theme.Icon.error,
            title: String(localized: "Couldn't Load Page"),
            message: error.localizedDescription
        )
    }
}

/// Coalesces concurrent TLS challenges for one forwarded `host:port` and guarantees every WebKit completion handler runs
/// once (defaulting to `.cancelAuthenticationChallenge` if dismissed unanswered).
@MainActor
final class LoopbackTrustPrompt: Identifiable {
    let id = UUID()
    let host: String
    let port: Int
    let displayLabel: String
    let trust: SecTrust
    private var completions: [@MainActor (URLSession.AuthChallengeDisposition, URLCredential?) -> Void]
    private var resolved = false

    init(host: String, port: Int, displayLabel: String, trust: SecTrust,
         completion: @escaping @MainActor (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        self.host = host
        self.port = port
        self.displayLabel = displayLabel
        self.trust = trust
        self.completions = [completion]
    }

    func append(_ completion: @escaping @MainActor (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        if resolved {
            completion(.cancelAuthenticationChallenge, nil)
        } else {
            completions.append(completion)
        }
    }

    func resolve(trust allow: Bool) {
        guard !resolved else { return }
        resolved = true
        let pending = completions
        completions = []
        if allow {
            LoopbackCertificateTrustStore.shared.trust(host: host, port: port, trust: trust)
            let cred = URLCredential(trust: trust)
            for done in pending { done(.useCredential, cred) }
        } else {
            for done in pending { done(.cancelAuthenticationChallenge, nil) }
        }
    }

    deinit {
        if !resolved {
            let pending = completions
            Task { @MainActor in
                for done in pending { done(.cancelAuthenticationChallenge, nil) }
            }
        }
    }
}

/// Safe single-shot completion wrappers for `WKUIDelegate` JavaScript dialogs so WebKit never crashes on sheet teardown.
@MainActor
final class WebDialog: Identifiable {
    enum Kind {
        case alert(message: String, done: () -> Void)
        case confirm(message: String, done: (Bool) -> Void)
        case prompt(message: String, defaultText: String, done: (String?) -> Void)
    }

    let id = UUID()
    let kind: Kind
    private var resolved = false

    init(_ kind: Kind) { self.kind = kind }

    func finishAlert() {
        guard !resolved, case .alert(_, let done) = kind else { return }
        resolved = true
        done()
    }

    func finishConfirm(_ accepted: Bool) {
        guard !resolved, case .confirm(_, let done) = kind else { return }
        resolved = true
        done(accepted)
    }

    func finishPrompt(_ value: String?) {
        guard !resolved, case .prompt(_, _, let done) = kind else { return }
        resolved = true
        done(value)
    }

    func cancelWithDefault() {
        guard !resolved else { return }
        resolved = true
        switch kind {
        case .alert(_, let done): done()
        case .confirm(_, let done): done(false)
        case .prompt(_, _, let done): done(nil)
        }
    }

    deinit {
        if !resolved {
            let k = kind
            Task { @MainActor in
                switch k {
                case .alert(_, let done): done()
                case .confirm(_, let done): done(false)
                case .prompt(_, _, let done): done(nil)
                }
            }
        }
    }
}

/// Observable per-WebView error, TLS prompt, and JS dialog state bridged from `WKNavigationDelegate` and `WKUIDelegate`.
@MainActor @Observable
final class WebPageState {
    var loadError: WebPageError?
    var pendingTrustPrompt: LoopbackTrustPrompt?
    var pendingDialog: WebDialog?
    @ObservationIgnored var dialogQueue: [WebDialog] = []
    @ObservationIgnored var initialURL: URL?
    @ObservationIgnored var forwardedLabel: String?
    @ObservationIgnored var isPresented = false
    @ObservationIgnored var declinedSelfSigned = false
    @ObservationIgnored var didReprobeCachedPort = false
    @ObservationIgnored var onRetry: (() -> Void)?
    @ObservationIgnored var onOpenExternalURL: ((URL) -> Void)?
    @ObservationIgnored var onReprobePort: ((PortForwarder.Key) async -> PortPresence.ReprobeOutcome)?

    func resetForReload() {
        loadError = nil
        declinedSelfSigned = false
        didReprobeCachedPort = false
    }

    func enqueueDialog(_ dialog: WebDialog) {
        if pendingDialog == nil {
            pendingDialog = dialog
        } else {
            dialogQueue.append(dialog)
        }
    }

    func advanceDialog() {
        pendingDialog = dialogQueue.isEmpty ? nil : dialogQueue.removeFirst()
    }

    func dismissPendingPrompts() {
        pendingTrustPrompt?.resolve(trust: false)
        pendingTrustPrompt = nil
        pendingDialog?.cancelWithDefault()
        pendingDialog = nil
        for queued in dialogQueue { queued.cancelWithDefault() }
        dialogQueue = []
    }
}

/// Navigation and UI delegate installed on every `SelectableWebView` to surface errors, confirm loopback self-signed
/// certificates, route `_blank` links, and present native JS dialogs.
@MainActor
final class WebViewBridge: NSObject, WKNavigationDelegate, WKUIDelegate {
    weak var webView: SelectableWebView?
    let state: WebPageState

    init(webView: SelectableWebView, state: WebPageState) {
        self.webView = webView
        self.state = state
    }

    // MARK: WKNavigationDelegate

    func webView(_ webView: WKWebView, didStartProvisionalNavigation navigation: WKNavigation!) {
        state.loadError = nil
        state.declinedSelfSigned = false
    }

    func webView(_ webView: WKWebView, didCommit navigation: WKNavigation!) {
        state.loadError = nil
        state.didReprobeCachedPort = false
    }

    func webView(_ webView: WKWebView, didFinish navigation: WKNavigation!) {
        state.loadError = nil
        state.didReprobeCachedPort = false
    }

    func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
        handleNavigationFailure(webView, error: error)
    }

    func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
        handleNavigationFailure(webView, error: error)
    }

    func webViewWebContentProcessDidTerminate(_ webView: WKWebView) {
        state.loadError = .processTerminated
    }

    func webView(_ webView: WKWebView, didReceive challenge: URLAuthenticationChallenge,
                 completionHandler: @escaping @MainActor (URLSession.AuthChallengeDisposition, URLCredential?) -> Void) {
        guard challenge.protectionSpace.authenticationMethod == NSURLAuthenticationMethodServerTrust,
              let serverTrust = challenge.protectionSpace.serverTrust else {
            completionHandler(.performDefaultHandling, nil)
            return
        }
        let host = challenge.protectionSpace.host
        let port = challenge.protectionSpace.port
        // Security boundary: ONLY loopback ports owned by PortForwarder may prompt for self-signed cert trust.
        // Every other host always follows system default TLS validation.
        guard PortForwarder.shared.isForwardedLoopback(host: host, port: port) else {
            completionHandler(.performDefaultHandling, nil)
            return
        }
        if LoopbackCertificateTrustStore.shared.isTrusted(host: host, port: port, trust: serverTrust) {
            completionHandler(.useCredential, URLCredential(trust: serverTrust))
            return
        }
        nonisolated(unsafe) let trustRef = serverTrust
        Task { @MainActor [weak self] in
            if await evaluateLoopbackTrustOffMain(trustRef) {
                completionHandler(.performDefaultHandling, nil)
                return
            }
            guard let self, self.state.isPresented else {
                completionHandler(.cancelAuthenticationChallenge, nil)
                return
            }
            if LoopbackCertificateTrustStore.shared.isTrusted(host: host, port: port, trust: trustRef) {
                completionHandler(.useCredential, URLCredential(trust: trustRef))
                return
            }
            if let existing = self.state.pendingTrustPrompt,
               existing.host.caseInsensitiveCompare(host) == .orderedSame,
               existing.port == port {
                existing.append(completionHandler)
                return
            }
            let label = self.state.forwardedLabel ?? "\(host):\(port)"
            self.state.pendingTrustPrompt = LoopbackTrustPrompt(
                host: host,
                port: port,
                displayLabel: label,
                trust: trustRef,
                completion: completionHandler
            )
        }
    }

    private func handleNavigationFailure(_ webView: WKWebView, error: Error) {
        let ns = error as NSError
        if ns.domain == "WebKitErrorDomain" && ns.code == 102 { return } // Frame load interrupted by policy/redirect
        if ns.domain == NSURLErrorDomain && ns.code == NSURLErrorCancelled {
            if state.declinedSelfSigned {
                state.loadError = .declinedSelfSigned
            }
            return
        }
        let failingURL = (ns.userInfo[NSURLErrorFailingURLErrorKey] as? URL) ?? webView.url ?? state.initialURL
        let forwardedKey = failingURL.flatMap { PortForwarder.shared.key(for: $0) }
        if let forwardedKey, !state.declinedSelfSigned, !state.didReprobeCachedPort, let reprobe = state.onReprobePort {
            state.didReprobeCachedPort = true
            let capturedError = error
            Task { @MainActor [weak self] in
                guard let self else { return }
                let outcome = await reprobe(forwardedKey)
                if outcome == .live {
                    self.state.onRetry?()
                } else {
                    self.state.loadError = WebPageError.from(
                        navigationError: capturedError,
                        forwardedKey: forwardedKey,
                        declinedSelfSigned: self.state.declinedSelfSigned
                    )
                }
            }
            return
        }
        state.loadError = WebPageError.from(
            navigationError: error,
            forwardedKey: forwardedKey,
            declinedSelfSigned: state.declinedSelfSigned
        )
    }

    // MARK: WKUIDelegate

    func webView(_ webView: WKWebView, createWebViewWith configuration: WKWebViewConfiguration,
                 for navigationAction: WKNavigationAction, windowFeatures: WKWindowFeatures) -> WKWebView? {
        guard let url = navigationAction.request.url else { return nil }
        if PortForwarder.shared.isLoopbackURL(url) {
            webView.load(navigationAction.request)
        } else {
            state.onOpenExternalURL?(url)
        }
        return nil
    }

    func webView(_ webView: WKWebView, runJavaScriptAlertPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping @MainActor () -> Void) {
        guard state.isPresented else { completionHandler(); return }
        state.enqueueDialog(WebDialog(.alert(message: message, done: completionHandler)))
    }

    func webView(_ webView: WKWebView, runJavaScriptConfirmPanelWithMessage message: String,
                 initiatedByFrame frame: WKFrameInfo, completionHandler: @escaping @MainActor (Bool) -> Void) {
        guard state.isPresented else { completionHandler(false); return }
        state.enqueueDialog(WebDialog(.confirm(message: message, done: completionHandler)))
    }

    func webView(_ webView: WKWebView, runJavaScriptTextInputPanelWithPrompt prompt: String,
                 defaultText: String?, initiatedByFrame frame: WKFrameInfo,
                 completionHandler: @escaping @MainActor (String?) -> Void) {
        guard state.isPresented else { completionHandler(nil); return }
        state.enqueueDialog(WebDialog(.prompt(message: prompt, defaultText: defaultText ?? "", done: completionHandler)))
    }
}

/// WKWebViews kept warm: the links of the latest answers load in the background, so opening one shows a finished
/// page. A link the user opens loads in the default (on-disk) website data store, so its assets and logins carry over.
/// A preloaded one loads in an in-memory store instead, seeded with the default store's cookies so signed-in sites
/// still preload signed in: preloads of pages nobody opened had filled WebKit's disk cache (760 MB on one phone, #8352).
/// On iPhone, iPad and Vision Pro a view serves one opening of its link: once a sheet has shown it, the pool lets it go
/// (`release`, from `PooledWebView`), so a later tap on the link starts on the link's own page with no back/forward
/// history, not where the last opening was left (#4074).
@MainActor
final class WebViewPool {
    static let shared = WebViewPool()
    private var views: [String: SelectableWebView] = [:]
    private var order: [String] = []
    private let limit = 6
    private let config: WKWebViewConfiguration = {
        let c = WKWebViewConfiguration()
        c.websiteDataStore = .default()
        c.allowsInlineMediaPlayback = true
        return c
    }()
    /// Preloads' store: in memory only, gone with the process.
    private let preloadConfig: WKWebViewConfiguration = {
        let c = WKWebViewConfiguration()
        c.websiteDataStore = .nonPersistent()
        c.allowsInlineMediaPlayback = true
        return c
    }()

    /// A view for this url, already loading (or loaded) if it was preloaded. A machine's dev server (the phone's
    /// loopback) loads again when its view is taken again: its page changes as the agent works, and the old view
    /// still showed the page as it was last closed (2026-11-05, #1934).
    func view(for url: URL) -> SelectableWebView {
        let key = url.absoluteString
        if let v = views[key] {
            touch(key)
            if Self.isLoopback(url), !v.isLoading {
                v.pageState.resetForReload()
                v.load(Self.request(url))
            }
            return v
        }
        return make(url, configuration: config)
    }

    private func make(_ url: URL, configuration: WKWebViewConfiguration) -> SelectableWebView {
        let key = url.absoluteString
        let v = SelectableWebView(frame: .zero, configuration: configuration)
        v.pageState.initialURL = url
        v.scrollView.showsVerticalScrollIndicator = false
        v.scrollView.showsHorizontalScrollIndicator = false
        v.allowsBackForwardNavigationGestures = true
        #if os(iOS)
        CallMediaGuard.shared.register(v) // a page's media stays still while a system call is live
        #endif
        v.load(Self.request(url))
        views[key] = v
        touch(key)
        return v
    }

    /// The view for this url if the pool has one (nothing is loaded).
    func existing(for url: URL) -> SelectableWebView? { views[url.absoluteString] }

    /// The sheet that showed `v` for `url` has closed. The pool drops `v`, with the page the user browsed to and its
    /// back/forward history, and starts a fresh view on the link, so tapping it again opens the link's own page at once
    /// (#4074). A machine's dev server is asked only when its page is opened, as before. A view the pool no longer
    /// holds (pushed out by newer links while it was shown) changes nothing.
    func release(_ v: WKWebView, for url: URL) {
        if let selectable = v as? SelectableWebView {
            selectable.pageState.isPresented = false
            selectable.pageState.dismissPendingPrompts()
        }
        let key = url.absoluteString
        guard views[key] === v else { return }
        // A sign-in done on a preloaded page is kept: its cookies go to the default store.
        let store = v.configuration.websiteDataStore
        if store === preloadConfig.websiteDataStore { Task { await Self.copyCookies(from: store, to: .default()) } }
        views[key] = nil
        order.removeAll { $0 == key }
        if !Self.isLoopback(url) { _ = view(for: url) }
    }

    /// The sheet's Refresh: the page again from the server, not from the cache. A view that never got a page (its first
    /// load failed: `url` is nil or an error overlay is shown) loads it afresh; if a rebuilt listener moved to a new
    /// local port (`newURL`), re-keys the pooled view and loads `newURL`.
    func reload(_ url: URL, at newURL: URL? = nil) {
        let oldKey = url.absoluteString
        guard let v = views[oldKey] else { return }
        let hadError = v.pageState.loadError != nil
        v.pageState.resetForReload()
        if let newURL, newURL != url {
            let newKey = newURL.absoluteString
            views[oldKey] = nil
            order.removeAll { $0 == oldKey }
            views[newKey] = v
            touch(newKey)
            v.pageState.initialURL = newURL
            v.load(Self.request(newURL))
            return
        }
        if v.url == nil || hadError { v.load(Self.request(url)) } else { v.reloadFromOrigin() }
    }

    /// A page seen before paints from disk at once; a machine's dev server (the phone's loopback) is always asked.
    private static func request(_ url: URL) -> URLRequest {
        var req = URLRequest(url: url)
        req.cachePolicy = isLoopback(url) ? .reloadIgnoringLocalCacheData : .returnCacheDataElseLoad
        return req
    }

    private static func isLoopback(_ url: URL) -> Bool {
        ["localhost", "127.0.0.1", "::1", "[::1]"].contains((url.host(percentEncoded: false) ?? "").lowercased())
    }

    /// Starts loading links that may be opened soon (not the ones a native Feishu view opens).
    func preload(_ urls: [URL]) {
        let lark = AppModel.shared.larkFeatures
        let wanted = urls.prefix(3).filter { views[$0.absoluteString] == nil && FeishuLinks.route($0, lark: lark) == nil }
        guard !wanted.isEmpty else { return }
        Task {
            await Self.copyCookies(from: .default(), to: preloadConfig.websiteDataStore)
            for u in wanted where views[u.absoluteString] == nil { _ = make(u, configuration: preloadConfig) }
        }
    }

    /// Every cookie of one store set in another (an existing cookie of the same name, domain and path is replaced).
    private static func copyCookies(from: WKWebsiteDataStore, to: WKWebsiteDataStore) async {
        for c in await from.httpCookieStore.allCookies() { await to.httpCookieStore.setCookie(c) }
    }

    private func touch(_ key: String) {
        order.removeAll { $0 == key }
        order.append(key)
        while order.count > limit {
            let old = order.removeFirst()
            views[old]?.pageState.dismissPendingPrompts()
            views[old]?.removeFromSuperview()
            views[old] = nil
        }
    }
}

/// A WKWebView whose selection menu has Edit with AI and Add as Context, with navigation/error/TLS/UI delegates attached.
final class SelectableWebView: WKWebView {
    var onPick: ((String, String) -> Void)?
    let pageState = WebPageState()
    private var bridge: WebViewBridge?

    override init(frame: CGRect, configuration: WKWebViewConfiguration) {
        super.init(frame: frame, configuration: configuration)
        let b = WebViewBridge(webView: self, state: pageState)
        bridge = b
        navigationDelegate = b
        uiDelegate = b
    }

    @available(*, unavailable)
    required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

    override func buildMenu(with builder: any UIMenuBuilder) {
        super.buildMenu(with: builder)
        let actions = UIMenu(options: .displayInline, children: [
            UIAction(title: String(localized: "Edit with AI"), image: UIImage(systemName: Theme.Icon.ai)) { [weak self] _ in self?.pickSelection("edit") },
            UIAction(title: String(localized: "Add as Context"), image: UIImage(systemName: Theme.Icon.addContext)) { [weak self] _ in self?.pickSelection("context") },
        ])
        builder.insertChild(actions, atStartOfMenu: .root)
    }

    private func pickSelection(_ kind: String) {
        evaluateJavaScript("window.getSelection().toString()") { [weak self] result, _ in
            let text = (result as? String ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
            guard !text.isEmpty else { return }
            self?.onPick?(text, kind)
        }
    }
}

/// Pooled WebView wrapped with its design-system error overlay, loopback self-signed certificate confirmation, and
/// native JavaScript alert/confirm/prompt dialogs.
struct WebPageView: View {
    var url: URL
    var forwardedLabel: String? = nil
    @Binding var title: String
    var nav: WebNav? = nil
    var onRetry: () -> Void
    var onOpenExternal: (URL) -> Void
    var onReprobePort: ((PortForwarder.Key) async -> PortPresence.ReprobeOutcome)? = nil
    var onPick: (String, String) -> Void
    @State private var pageState: WebPageState?
    @State private var promptInput = ""

    private var activeAlertMessage: String? {
        if case .alert(let msg, _) = pageState?.pendingDialog?.kind { return msg }
        return nil
    }

    private var activeConfirmMessage: String? {
        if case .confirm(let msg, _) = pageState?.pendingDialog?.kind { return msg }
        return nil
    }

    private var activePromptMessage: String? {
        if case .prompt(let msg, _, _) = pageState?.pendingDialog?.kind { return msg }
        return nil
    }

    var body: some View {
        ZStack {
            PooledWebView(
                url: url,
                title: $title,
                nav: nav,
                forwardedLabel: forwardedLabel,
                onRetry: onRetry,
                onOpenExternal: onOpenExternal,
                onReprobePort: onReprobePort,
                onPick: onPick,
                onState: { pageState = $0 }
            )
            if let error = pageState?.loadError {
                ScrollView {
                    EmptyStateView(
                        symbol: error.symbol,
                        title: error.title,
                        message: error.message,
                        action: (title: String(localized: "Retry"), run: onRetry),
                        tint: Theme.caution
                    )
                    .containerRelativeFrame(.vertical)
                }
                .background(Theme.pageBackground)
                .accessibilityIdentifier("webview-error-overlay")
            }
        }
        .alert(
            String(localized: "Untrusted Local Certificate"),
            isPresented: Binding(
                get: { pageState?.pendingTrustPrompt != nil },
                set: { shown in
                    if !shown, let p = pageState?.pendingTrustPrompt {
                        pageState?.declinedSelfSigned = true
                        pageState?.loadError = .declinedSelfSigned
                        p.resolve(trust: false)
                        pageState?.pendingTrustPrompt = nil
                    }
                }
            )
        ) {
            Button(String(localized: "Don't Trust"), role: .cancel) {
                if let p = pageState?.pendingTrustPrompt {
                    pageState?.declinedSelfSigned = true
                    pageState?.loadError = .declinedSelfSigned
                    p.resolve(trust: false)
                    pageState?.pendingTrustPrompt = nil
                }
            }
            Button(String(localized: "Trust for This Session")) {
                if let p = pageState?.pendingTrustPrompt {
                    p.resolve(trust: true)
                    pageState?.pendingTrustPrompt = nil
                }
            }
        } message: {
            Text("This certificate is self-signed by the development server on your computer (\(pageState?.pendingTrustPrompt?.displayLabel ?? "")). Trust it only if you started this server. Trust applies only to this port during this session.")
        }
        .background {
            Color.clear
                .alert(
                    String(localized: "Page Message"),
                    isPresented: Binding(
                        get: { activeAlertMessage != nil },
                        set: { if !$0 { pageState?.pendingDialog?.finishAlert(); pageState?.advanceDialog() } }
                    )
                ) {
                    Button(String(localized: "OK [In App Tunnel View f867f3]"), role: .cancel) {
                        pageState?.pendingDialog?.finishAlert()
                        pageState?.advanceDialog()
                    }
                } message: {
                    Text(activeAlertMessage ?? "")
                }
                .background {
                    Color.clear
                        .alert(
                            String(localized: "Confirm"),
                            isPresented: Binding(
                                get: { activeConfirmMessage != nil },
                                set: { if !$0 { pageState?.pendingDialog?.finishConfirm(false); pageState?.advanceDialog() } }
                            )
                        ) {
                            Button(String(localized: "Cancel"), role: .cancel) {
                                pageState?.pendingDialog?.finishConfirm(false)
                                pageState?.advanceDialog()
                            }
                            Button(String(localized: "OK [Interactive Assistant View Card fac2a6]")) {
                                pageState?.pendingDialog?.finishConfirm(true)
                                pageState?.advanceDialog()
                            }
                        } message: {
                            Text(activeConfirmMessage ?? "")
                        }
                        .background {
                            Color.clear
                                .alert(
                                    String(localized: "Page Prompt"),
                                    isPresented: Binding(
                                        get: { activePromptMessage != nil },
                                        set: { if !$0 { pageState?.pendingDialog?.finishPrompt(nil); pageState?.advanceDialog() } }
                                    )
                                ) {
                                    TextField("", text: $promptInput)
                                    Button(String(localized: "Cancel"), role: .cancel) {
                                        pageState?.pendingDialog?.finishPrompt(nil)
                                        pageState?.advanceDialog()
                                    }
                                    Button(String(localized: "OK [Interactive Assistant View Card fac2a6]")) {
                                        let value = promptInput
                                        pageState?.pendingDialog?.finishPrompt(value)
                                        pageState?.advanceDialog()
                                    }
                                } message: {
                                    Text(activePromptMessage ?? "")
                                }
                        }
                }
        }
        .onChange(of: pageState?.pendingDialog?.id) { _, _ in
            if case .prompt(_, let defaultText, _) = pageState?.pendingDialog?.kind {
                promptInput = defaultText
            }
        }
    }
}

struct PooledWebView: UIViewRepresentable {
    var url: URL
    @Binding var title: String
    /// Told where the page is (the preview's bar, #150).
    var nav: WebNav? = nil
    var forwardedLabel: String? = nil
    var onRetry: (() -> Void)? = nil
    var onOpenExternal: ((URL) -> Void)? = nil
    var onReprobePort: ((PortForwarder.Key) async -> PortPresence.ReprobeOutcome)? = nil
    var onPick: (String, String) -> Void
    var onState: ((WebPageState) -> Void)? = nil

    func makeUIView(context: Context) -> SelectableWebView {
        let v = WebViewPool.shared.view(for: url)
        v.removeFromSuperview()
        v.onPick = onPick
        configureState(v)
        context.coordinator.url = url
        context.coordinator.observe(v) { title = $0 }
        nav?.attach(v)
        let state = v.pageState
        Task { @MainActor in onState?(state) }
        return v
    }

    func updateUIView(_ v: SelectableWebView, context: Context) {
        v.onPick = onPick
        context.coordinator.url = url
        configureState(v)
    }

    private func configureState(_ v: SelectableWebView) {
        v.pageState.isPresented = true
        v.pageState.initialURL = url
        v.pageState.forwardedLabel = forwardedLabel
        v.pageState.onRetry = onRetry
        v.pageState.onOpenExternalURL = onOpenExternal
        v.pageState.onReprobePort = onReprobePort
    }

    /// The sheet has closed: this opening's page and history go with it, and the next tap on the link starts fresh (#4074).
    static func dismantleUIView(_ v: SelectableWebView, coordinator: Coordinator) {
        v.pageState.isPresented = false
        v.pageState.dismissPendingPrompts()
        if let url = coordinator.url { WebViewPool.shared.release(v, for: url) }
    }

    func makeCoordinator() -> Coordinator { Coordinator() }

    @MainActor
    final class Coordinator {
        /// The link this view was taken for: the pool's key, which the page the user browsed to no longer is.
        var url: URL?
        private var token: NSKeyValueObservation?
        private var onTitle: ((String) -> Void)?
        func observe(_ v: WKWebView, title: @escaping (String) -> Void) {
            onTitle = title
            if let t = v.title, !t.isEmpty { title(t) }
            token = v.observe(\.title, options: [.new]) { [weak self] v, _ in
                let t = v.title ?? ""
                Task { @MainActor in if !t.isEmpty { self?.onTitle?(t) } }
            }
        }
    }
}

// MARK: - In messages

/// The contexts a message carries (picked page excerpts, everything a Feishu /agent request hands its agent): one
/// folded row by the bubble, the raw tags never show. Open, a list of titles; a title opens to its text. One context
/// opens straight to its text.
struct ContextGroup: View {
    var refs: [ContextRef]
    /// The message's id: the row stays put in the chat when it opens or closes (`ChatPin`).
    var key: String
    @Environment(\.chatPin) private var pin
    @State private var open = ContextGroup.shownOpen
    @State private var unfold = Unfolding()
    private var pinKey: String { "contexts-\(key)" }

    /// DEBUG `-showContexts YES`: groups and their first context start open (screenshots).
    static var shownOpen: Bool {
        #if DEBUG
        UserDefaults.standard.bool(forKey: "showContexts")
        #else
        false
        #endif
    }

    var body: some View {
        VStack(alignment: .trailing, spacing: Theme.Space.xs) {
            Button {
                Haptics.tap()
                toggleInPlace(pin, pinKey, animation: Theme.Motion.state, unfold: $unfold, closing: open) { open.toggle() }
            } label: {
                HStack(spacing: Theme.Space.sm) {
                    ThemedIcon(refs.count == 1 ? refs[0].icon : Theme.Icon.contexts).font(Theme.Font.label)
                    Text(ContextRef.summary(refs)).font(Theme.Font.labelMedium).lineLimit(1).truncationMode(.middle)
                    ThemedIcon(open ? Theme.Icon.collapse : Theme.Icon.expand).font(Theme.Font.label)
                }
                .foregroundStyle(.secondary)
                .padding(.horizontal, Theme.Space.m)
                .padding(.vertical, Theme.Space.sm)
                .background(Theme.Fill.well, in: .capsule)
                .contentShape(.capsule)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(ContextRef.summary(refs))
            .accessibilityValue(open ? String(localized: "Expanded") : String(localized: "Collapsed [Readiness Card ee6046]"))

            if open {
                VStack(alignment: .leading, spacing: 0) {
                    if refs.count == 1 {
                        ContextBody(ref: refs[0])
                    } else {
                        ForEach(Array(refs.enumerated()), id: \.offset) { n, r in
                            if n > 0 { Divider() }
                            ContextRow(ref: r, key: "\(pinKey)-\(n)", open: n == 0 && ContextGroup.shownOpen)
                        }
                    }
                }
                .scaledFrame(maxWidth: Theme.Size.box380, alignment: .leading)
                .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.card, style: .continuous))
                .unfolding(unfold)
            }
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
        .chatPinned(pinKey)
    }
}

/// One context in an open group: its title; a tap shows its text.
private struct ContextRow: View {
    var ref: ContextRef
    var key: String
    @Environment(\.chatPin) private var pin
    @State var open = false
    @State private var unfold = Unfolding()

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                Haptics.tap()
                toggleInPlace(pin, key, animation: Theme.Motion.state, unfold: $unfold, closing: open) { open.toggle() }
            } label: {
                HStack(spacing: Theme.Space.s) {
                    ThemedIcon(ref.icon).font(Theme.Font.label).foregroundStyle(.secondary)
                        .scaledFrame(width: Theme.Size.markM)
                    Text(ref.label).font(Theme.Font.secondary).foregroundStyle(Color.primary).lineLimit(1).truncationMode(.tail)
                    Spacer(minLength: Theme.Space.s)
                    if ref.text.isEmpty { Text("Empty").font(Theme.Font.label).foregroundStyle(.tertiary).lineLimit(1) }
                    ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.label).foregroundStyle(.tertiary)
                        .rotationEffect(.degrees(open ? 90 : 0))
                }
                .padding(.horizontal, Theme.Space.m)
                .padding(.vertical, Theme.Space.s)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityValue(open ? String(localized: "Expanded") : String(localized: "Collapsed [Readiness Card ee6046]"))
            if open { ContextBody(ref: ref).unfolding(unfold) }
        }
        .chatPinned(key)
    }
}

/// A context's text (scrolls inside a fixed height when long) and its page, when it has one.
private struct ContextBody: View {
    var ref: ContextRef
    @Environment(\.openLink) private var openLink

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if ref.text.isEmpty {
                Text("No content").font(Theme.Font.caption).foregroundStyle(.tertiary)
            } else if ref.text.count > 1200 {
                ScrollView { text }.scaledFrame(height: Theme.Size.box300)
            } else {
                text
            }
            if let u = URL(string: ref.url), !ref.url.isEmpty {
                Button { openLink?(u) } label: {
                    Label("Open \(ref.host) [Git Hub Thread View eb8679]", systemImage: Theme.Icon.open).font(Theme.Font.labelMedium).lineLimit(1)
                }
                .buttonStyle(.plain)
                .foregroundStyle(Theme.accent)
            }
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.bottom, Theme.Space.m)
        .padding(.top, Theme.Space.xs)
    }

    private var text: some View {
        Text(ref.text).font(Theme.Font.caption).foregroundStyle(.secondary).textSelection(.enabled)
            .frame(maxWidth: .infinity, alignment: .leading)
    }
}

// MARK: - Picking text in native views

/// What a long press on a paragraph offers inside the link sheet (SwiftUI's own selection menu cannot take items).
struct DocPickMenu: View {
    var text: String
    var pick: (String, String) -> Void
    @Binding var selecting: Bool

    var body: some View {
        Button { pick(text, "edit") } label: { Label("Edit This with AI", systemImage: Theme.Icon.ai) }
        Button { pick(text, "context") } label: { Label("Add This as Context", systemImage: Theme.Icon.addContext) }
        Button { selecting = true } label: { Label("Select Text…", systemImage: Theme.Icon.select) }
        Button { UIPasteboard.general.string = text; Haptics.success() } label: { Label("Copy [Readiness Views d37380]", systemImage: Theme.Icon.copy) }
    }
}

/// What a selection's menu offers in `PickTextView`: a title, its icon, and the kind `pick` gets with the text.
struct PickAction: Hashable, Sendable {
    var title: String
    var icon: String
    var kind: String

    /// The chat's link sheet: Edit with AI and Add as Context.
    static let ai = [PickAction(title: String(localized: "Edit with AI"), icon: Theme.Icon.ai, kind: "edit"),
                     PickAction(title: String(localized: "Add as Context"), icon: Theme.Icon.addContext, kind: "context")]
}

/// The paragraph in a text view where any span can be selected; its selection menu has `actions` (Edit with AI and
/// Add as Context by default).
struct PickTextSheet: View {
    @Environment(\.dismiss) private var dismiss
    var text: String
    var actions = PickAction.ai
    var pick: (String, String) -> Void

    var body: some View {
        NavigationStack {
            PickTextView(text: text, actions: actions) { t, kind in
                dismiss()
                pick(t, kind)
            }
            .navigationTitle("Select Text [Link Sheet 409a5a]")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { SheetClose { dismiss() } }
        }
        .presentationDetents([.medium, .large])
    }
}

struct PickTextView: UIViewRepresentable {
    var text: String
    var actions = PickAction.ai
    var pick: (String, String) -> Void

    func makeUIView(context: Context) -> UITextView {
        let v = UITextView()
        v.isEditable = false
        v.isSelectable = true
        v.font = .preferredFont(forTextStyle: .body)
        v.adjustsFontForContentSizeCategory = true
        v.textContainerInset = UIEdgeInsets(top: 16, left: 12, bottom: 24, right: 12)
        v.backgroundColor = .clear
        v.delegate = context.coordinator
        v.text = text
        return v
    }

    func updateUIView(_ v: UITextView, context: Context) {
        context.coordinator.pick = pick
        context.coordinator.actions = actions
    }

    func makeCoordinator() -> Coordinator { Coordinator(pick: pick, actions: actions) }

    final class Coordinator: NSObject, UITextViewDelegate {
        var pick: (String, String) -> Void
        var actions: [PickAction]
        init(pick: @escaping (String, String) -> Void, actions: [PickAction]) {
            self.pick = pick
            self.actions = actions
        }

        func textView(_ textView: UITextView, editMenuForTextIn range: NSRange, suggestedActions: [UIMenuElement]) -> UIMenu? {
            let selected = (textView.text as NSString).substring(with: range).trimmingCharacters(in: .whitespacesAndNewlines)
            guard !selected.isEmpty else { return UIMenu(children: suggestedActions) }
            let ours = UIMenu(options: .displayInline, children: actions.map { a in
                UIAction(title: a.title, image: UIImage(systemName: a.icon)) { [weak self] _ in self?.pick(selected, a.kind) }
            })
            return UIMenu(children: [ours] + suggestedActions)
        }
    }
}
