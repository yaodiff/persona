import Foundation

/// The cap the phone puts on the video channel (#3954, docs/extensions/computer-use.md "Adaptation"): the helper's
/// bandwidth adaptation works under it. 12 Mbit/s on Wi-Fi, 4 on cellular, 1.5 in Low Data Mode; 15 frames a second in
/// Low Power Mode or a serious thermal state, else 60.
///
/// With quality tiers (#6396, `mobileData` given), mobile data follows the user's choice (`ScreenMobileData`): Clearer
/// keeps 4 Mbit/s, Data Saver (the default until they choose) 1.5 Mbit/s and at most one pixel per point; Low Data Mode
/// is always Data Saver. Without tiers the caps are as before.
nonisolated struct ScreenVideoBudget: Equatable, Sendable {
    var maxKbps: Int
    var fps: Int
    /// The most pixels per point the tiers may use; nil, no limit.
    var maxScale: Double? = nil

    static func of(expensive: Bool, constrained: Bool, lowPower: Bool, thermal: ProcessInfo.ThermalState,
                   mobileData: ScreenMobileData? = nil) -> ScreenVideoBudget {
        let hot = thermal == .serious || thermal == .critical
        let fps = lowPower || hot ? 15 : 60
        guard let mobileData else {
            return ScreenVideoBudget(maxKbps: constrained ? 1_500 : expensive ? 4_000 : 12_000, fps: fps)
        }
        if constrained || (expensive && mobileData == .saver) { return ScreenVideoBudget(maxKbps: 1_500, fps: fps, maxScale: 1) }
        return ScreenVideoBudget(maxKbps: expensive ? 4_000 : 12_000, fps: fps)
    }
}

/// How long the page waits before trying video again after it stopped (#3954): 30 s, then twice as long each time up
/// to 5 min; back to 30 s once video has stayed up a while (`ScreenVideoLink`'s `stable`), so a connection that keeps
/// dropping is tried less and less often instead of flipping the picture (and its line) back and forth.
nonisolated struct ScreenVideoBackoff: Equatable, Sendable {
    let first: Duration
    let longest: Duration
    private(set) var next: Duration

    init(first: Duration = .seconds(30), longest: Duration = .seconds(300)) {
        self.first = first
        self.longest = longest
        next = first
    }

    /// The wait before this retry; the one after is twice as long.
    mutating func take() -> Duration {
        defer { next = min(next * 2, longest) }
        return next
    }

    mutating func reset() { next = first }
}

/// Why the page shows VNC while video was chosen (#3954), in words anyone reads: no protocol or codec names.
nonisolated enum ScreenVideoFallback: Equatable, Sendable {
    /// The stream closed, went quiet, or a picture didn't decode; video is tried again later.
    case lost
    /// The Mac is locked or at its login window; video comes back when it is unlocked.
    case locked
    /// The Mac hasn't allowed Persona Screen to record its screen.
    case notAllowed

    /// The helper's `paused` reason as a fallback.
    static func paused(_ reason: String) -> ScreenVideoFallback {
        switch reason {
        case "locked", "login_window": .locked
        case "permission": .notAllowed
        default: .lost
        }
    }

    var message: String {
        switch self {
        case .lost: String(localized: "Video stopped. Showing VNC for now.")
        case .locked: String(localized: "The Mac is locked. Showing VNC until it’s unlocked.")
        case .notAllowed: String(localized: "This Mac hasn’t allowed video yet. Showing VNC.")
        }
    }

    /// The one thing the user can do about `notAllowed` themselves, in one sentence (the page's "How to turn it on").
    /// Before the node has asked macOS (`permission` missing, not `asked`), macOS's alert is about to show on the Mac;
    /// once asked, Persona Screen is in System Settings' list (an alert macOS shows only once).
    static func howToAllow(permission: String?, asked: Bool) -> String {
        if permission == "missing", !asked {
            return String(localized: "That Mac will show a window: choose Open System Settings, then turn on Persona Screen.")
        }
        return String(localized: "On that Mac, turn on Persona Screen in System Settings › Privacy & Security › Screen & System Audio Recording.")
    }
}

#if os(iOS) && canImport(PersonaScreenKit)
import AVFoundation
import CoreVideo
import Network
import Observation
import PersonaScreenKit
import UIKit

/// The video channel of one Mac's live screen (#3954): when it runs, what it shows, and when the page falls back to
/// VNC. RFB signs in first and shows until the first video picture decodes; then the picture is video and the session
/// stops asking RFB for updates (input, the clipboard and the fallback stay on RFB). Any of: the stream closes, no frame
/// or heartbeat for 3 s, a picture that doesn't decode, or the Mac locking (`paused`) puts RFB back at once; a stream
/// that ended is tried again after `ScreenVideoBackoff`, and a paused one comes back with the next picture after
/// `resumed`. Owned by the RFBSession, so the page and the small window share it.
@MainActor @Observable final class ScreenVideoLink {
    /// What the page wants shown as video: one display (`display`, the helper's CGDirectDisplayID; nil the main one) at
    /// `rect` on the framebuffer (pixels; nil all of it).
    struct Want: Equatable {
        var display: Int?
        var rect: CGRect?
    }

    /// The signed-in RFB connection video binds to.
    struct Ready: Equatable {
        var group: String
        var machine: String
        var pair: String
    }

    /// Video is the picture: a decoded frame is shown and RFB asks for no updates.
    private(set) var showing = false
    /// Why VNC shows while video was chosen; nil when video shows, is on its way, or isn't wanted.
    private(set) var fallback: ScreenVideoFallback?
    /// Where the video goes on the framebuffer, in its pixels; nil: all of it.
    private(set) var rect: CGRect?
    /// The stream as the stats overlay shows it, about once a second while video shows (not on every frame: only the
    /// overlay reads it).
    private(set) var reading = ScreenVideoReading()
    private(set) var codec: String?
    private(set) var size: CGSize?
    /// The helper said it has quality tiers (#6396, `hello.features`): only then does ⋯ › Quality do anything, so only
    /// then is it offered. Kept once heard, for this session.
    private(set) var tiers = false
    /// The tier now and why it last changed (#6396), for the stats overlay; nil without tiers.
    private(set) var tier: TierPolicy.Change?
    private(set) var tierAt: Date?
    /// Ask once whether mobile data may use more for a clearer picture (`ScreenMobileDataCard`); and whether that's a
    /// personal hotspot rather than cellular (the card's title says which).
    private(set) var askMobileData = false
    private(set) var onHotspot = false
    /// When video is tried again after it stopped (tests).
    @ObservationIgnored private(set) var retryAt: Date?

    /// Showing (true) or not: the session holds or releases RFB's updates.
    @ObservationIgnored var onShowing: @MainActor (Bool) -> Void = { _ in }
    /// Opens the stream's transport; tests give a scripted one.
    @ObservationIgnored var transport: @MainActor (Ready) -> RFBTransport? = ScreenVideoLink.hubTransport
    /// Each change of `fallback`, and each wait before a retry (tests).
    @ObservationIgnored var onFallback: @MainActor (ScreenVideoFallback?) -> Void = { _ in }
    @ObservationIgnored var onRetry: @MainActor (Duration) -> Void = { _ in }
    /// The live screen moved to another network route (#6398): the quality tiers (#6396) look again here.
    @ObservationIgnored var onPathChange: @MainActor (PathChange) -> Void = { _ in }
    /// The route the live screen takes and why it last changed (#6398); nil without a screen session.
    private(set) var route: PathChange?
    /// The route in use as last measured, about once a second (#6398; the tiers', #6396, too); only the overlay draws it.
    private(set) var link: LinkStats?
    /// The node said why it has no direct routes (`firewall`).
    private(set) var directUnavailable: String?

    @ObservationIgnored private var want: Want?
    @ObservationIgnored private var ready: Ready?
    @ObservationIgnored private var held: Set<String> = []
    @ObservationIgnored private var stream: ScreenVideoStream?
    @ObservationIgnored private var opened: (Want, Ready)?
    @ObservationIgnored private var backoff: ScreenVideoBackoff
    /// How long video must stay up before a drop starts the waits from the first again.
    @ObservationIgnored private let stable: Duration
    @ObservationIgnored private var shownSince: ContinuousClock.Instant?
    @ObservationIgnored private var retry: Task<Void, Never>?
    @ObservationIgnored private let layers = NSHashTable<AVSampleBufferDisplayLayer>.weakObjects()
    @ObservationIgnored private let mailbox = ScreenVideoMailbox()
    @ObservationIgnored private var latest: CVPixelBuffer?
    @ObservationIgnored private var tally = ScreenVideoTally()
    @ObservationIgnored private let path = NWPathMonitor()
    @ObservationIgnored private var budget: ScreenVideoBudget
    /// The phone's network as NWPath last said: mobile data (expensive), Low Data Mode (constrained), over cellular.
    @ObservationIgnored private var net: (expensive: Bool, constrained: Bool, cellular: Bool) = (false, false, false)
    /// ⋯ › Quality for this computer (#6396).
    @ObservationIgnored private var bias: TierLadder.Bias = .auto
    /// The tier each computer last settled on, by computer and kind of route, for this app run: the next stream starts
    /// there (`TierPolicy`'s `remembered`).
    @ObservationIgnored private static var settled: [String: Int] = [:]

    init(backoff: ScreenVideoBackoff = .init(), stable: Duration = .seconds(60)) {
        self.backoff = backoff
        self.stable = stable
        budget = Self.currentBudget(path: nil)
        path.pathUpdateHandler = { @Sendable [weak self] p in
            let expensive = p.isExpensive, constrained = p.isConstrained, cellular = p.usesInterfaceType(.cellular)
            Task { @MainActor in self?.network(expensive: expensive, constrained: constrained, cellular: cellular) }
        }
        path.start(queue: .global(qos: .utility))
    }

    deinit { path.cancel() }

    // MARK: Inputs

    /// What the page wants shown as video; nil: VNC (chosen, or several displays shown at once).
    /// The live screen's route changed (#6398): the helper starts its delay baseline again (`update.path`), since the
    /// one-way delay changed with the route.
    func measured(_ stats: LinkStats?, unavailable: String?) {
        if stats != link { link = stats }
        if unavailable != directUnavailable { directUnavailable = unavailable }
    }

    func routeChanged(_ change: PathChange) {
        route = change
        link = change.to
        if change.reason != .start { stream?.send(UpdateMessage(path: .init(change.to))) }
        stream?.tierPath(change) // the tiers look again (#6396)
        onPathChange(change)
    }

    /// ⋯ › Quality for this computer (#6396): on the running stream at once.
    func setQuality(_ q: ScreenQuality) {
        let b = TierLadder.Bias(rawValue: q.rawValue) ?? .auto
        guard b != bias else { return }
        bias = b
        stream?.tierBias(b)
    }

    /// The answer to the mobile-data question (#6396): kept for every computer, on the running stream at once.
    func chooseMobileData(_ m: ScreenMobileData) {
        ScreenMobileData.choose(m)
        askMobileData = false
        rebudget()
    }

    /// The question closed unanswered: Data Saver stays, and it is asked once more next time.
    func dismissMobileData() { askMobileData = false }

    func want(_ w: Want?) {
        guard w != want else { return }
        want = w
        if w == nil { say(nil); stopRetry(); backoff.reset() }
        reconsider()
    }

    /// The signed-in RFB connection (its pair), or nil when there is none: a new connection tries video at once. The
    /// fallback said stays (the page shows it only while VNC is live), so a reconnection doesn't blink it off and on.
    func rfb(_ r: Ready?) {
        guard r != ready else { return }
        ready = r
        stopRetry()
        reconsider()
    }

    /// Video stops while any reason holds it (the small window shows the screen, the app is in the background) and
    /// comes back when none does, with no fallback said.
    func hold(_ reason: String, _ on: Bool) {
        let was = held.isEmpty
        if on { held.insert(reason) } else { held.remove(reason) }
        if was != held.isEmpty { reconsider() }
    }

    /// Shown in this layer from now on; the newest picture at once.
    func show(in layer: AVSampleBufferDisplayLayer) {
        layers.add(layer)
        if showing, let latest { Self.enqueue(latest, in: [layer]) }
    }

    func hide(from layer: AVSampleBufferDisplayLayer) { layers.remove(layer) }

    // MARK: Running

    private func reconsider() {
        let should: (Want, Ready)? = {
            guard let want, let ready, held.isEmpty, retry == nil else { return nil }
            return (want, ready)
        }()
        if let opened, let should, opened == should { return }
        if stream != nil { stop() }
        if let should { open(should) }
    }

    private func open(_ target: (Want, Ready)) {
        guard let t = transport(target.1) else {
            ended(.closed)
            return
        }
        opened = target
        rect = target.0.rect
        tally = ScreenVideoTally()
        let start = StartMessage(display: target.0.display, maxSize: Self.maxSize, fps: budget.fps, maxKbps: budget.maxKbps,
                                 codecs: ["hevc", "h264"])
        let mailbox = mailbox
        // The tiers (#6396), when this app has them: they start once the helper's hello says it has them too.
        let tiers = ScreenTiers.appSupported
            ? ScreenVideoTiers(.init(bias: bias, budget: tieredBudget, maxSize: Self.maxSize, link: route?.to,
                                     remembered: Self.settled[settledKey(target.1)]))
            : nil
        tier = nil
        tierAt = nil
        let s = ScreenVideoStream(transport: t, start: start, tiers: tiers) { [weak self] event in
            switch event {
            case let .picture(pb, ms):
                // Newest picture wins: one hop to the main actor at a time, carrying whatever is newest then. A picture
                // still waiting for the hop is replaced, never shown: the tiers count those (the screen can't keep up).
                let hop = mailbox.put(pb, ms)
                tiers?.shown(replaced: !hop, at: ScreenVideoStream.nowMicros)
                if hop { Task { @MainActor in self?.takePicture() } }
            case let .hello(h):
                Task { @MainActor in self?.hello(h) }
            case let .paused(why):
                Task { @MainActor in self?.paused(why) }
            case .resumed:
                break // the next picture shows video again
            case let .timing(t):
                Task { @MainActor in self?.helperTiming(t) }
            case let .tier(c):
                Task { @MainActor in self?.tierChanged(c) }
            case let .encoding(e):
                Task { @MainActor in self?.encodingChanged(e) }
            case let .ended(why):
                Task { @MainActor in self?.ended(why) }
            }
        }
        stream = s
        s.run()
    }

    /// Closes the stream from this side, with no fallback said.
    private func stop() {
        stream?.close()
        stream = nil
        opened = nil
        _ = mailbox.take()
        show(false)
    }

    private func hello(_ h: HelloMessage) {
        codec = h.codec == "h264" ? "H.264" : "HEVC"
        size = CGSize(width: h.width, height: h.height)
        if h.features?.contains("tiers") == true {
            tiers = true
            // On mobile data, the picture starts on Data Saver; until the user chooses, ask (twice at most).
            if ScreenTiers.appSupported, net.expensive, !net.constrained, ScreenMobileData.shouldAsk, !askMobileData {
                ScreenMobileData.markAsked()
                onHotspot = !net.cellular
                askMobileData = true
            }
        }
        if let r = h.rect, r.count == 4 { rect = CGRect(x: r[0], y: r[1], width: r[2], height: r[3]) }
    }

    private func takePicture() {
        guard stream != nil, let (pb, ms) = mailbox.take() else { return }
        latest = pb
        tally.picture(decodeMs: ms, at: .now)
        publish()
        if !showing {
            say(nil) // video is back: the line goes
            show(true)
        }
        Self.enqueue(pb, in: layers.allObjects)
    }

    /// A new tier (#6396): for the overlay, and remembered for this computer's next stream over the same kind of route.
    private func tierChanged(_ c: TierPolicy.Change) {
        guard stream != nil else { return }
        tier = c
        tierAt = .now
        if let ready { Self.settled[settledKey(ready)] = c.index }
    }

    /// The encoder's size after a change in place (#6396): the overlay shows what runs, not what `hello` said.
    private func encodingChanged(_ e: EncodingMessage) {
        guard stream != nil else { return }
        let s = CGSize(width: e.width, height: e.height)
        if s != size { size = s }
    }

    private func settledKey(_ r: Ready) -> String { "\(r.group)/\(r.machine)/\(route?.to.kind.rawValue ?? "relay")" }

    private func helperTiming(_ t: TimingMessage) {
        guard stream != nil else { return }
        tally.helper(t)
        publish()
    }

    /// A new reading for the overlay once a second has passed since the last.
    private func publish() {
        guard let s = stream, let r = tally.reading(at: .now, received: s.receivedBytes), r != reading else { return }
        reading = r
    }

    private func paused(_ why: String) {
        guard stream != nil else { return }
        show(false)
        say(.paused(why))
    }

    private func ended(_ why: ScreenVideoStream.Ending) {
        stream = nil
        opened = nil
        show(false)
        guard want != nil, ready != nil else { return }
        say(.lost)
        let wait = backoff.take()
        retryAt = Date.now.addingTimeInterval(Double(wait.components.seconds) + Double(wait.components.attoseconds) / 1e18)
        onRetry(wait)
        retry = Task { [weak self] in
            try? await Task.sleep(for: wait)
            guard !Task.isCancelled, let self else { return }
            self.retry = nil
            self.retryAt = nil
            self.reconsider()
        }
    }

    private func stopRetry() {
        retry?.cancel()
        retry = nil
        retryAt = nil
    }

    /// Changes the line only when the reason changes: the same one again (a retry that fails as the last one did) is
    /// no change, so nothing redraws.
    private func say(_ f: ScreenVideoFallback?) {
        guard f != fallback else { return }
        fallback = f
        onFallback(f)
    }

    private func show(_ on: Bool) {
        guard showing != on else { return }
        showing = on
        if on {
            shownSince = .now
        } else {
            // Video that stayed up a while starts the waits over; one that dropped soon after coming back keeps doubling.
            if let since = shownSince, ContinuousClock.now - since >= stable { backoff.reset() }
            shownSince = nil
            reading = ScreenVideoReading()
            latest = nil
            // Emptied, so when video shows again it never starts on an old picture.
            for l in layers.allObjects { l.sampleBufferRenderer.flush(removingDisplayedImage: true, completionHandler: nil) }
        }
        onShowing(on)
    }

    private func network(expensive: Bool, constrained: Bool, cellular: Bool) {
        let changed = net.expensive != expensive || net.constrained != constrained
        net = (expensive, constrained, cellular)
        if !expensive || constrained { askMobileData = false } // Wi-Fi, or Low Data Mode: nothing to ask
        if changed { rebudget() }
    }

    /// The caps after a network or mobile-data change: with tiers, the policy takes the new ladder; without, the cap
    /// and frame rate go to the helper as before.
    private func rebudget() {
        if stream?.tiers?.active == true {
            stream?.tierBudget(tieredBudget)
            return
        }
        let b = Self.currentBudget(path: (net.expensive, net.constrained))
        guard b != budget else { return }
        budget = b
        stream?.send(UpdateMessage(fps: b.fps, maxKbps: b.maxKbps))
    }

    /// The caps with tiers (#6396): mobile data as the user chose (`ScreenMobileData`).
    private var tieredBudget: ScreenVideoBudget {
        Self.currentBudget(path: (net.expensive, net.constrained), mobileData: ScreenMobileData.current)
    }

    private static func currentBudget(path: (expensive: Bool, constrained: Bool)?, mobileData: ScreenMobileData? = nil)
        -> ScreenVideoBudget {
        let p = ProcessInfo.processInfo
        return ScreenVideoBudget.of(expensive: path?.expensive ?? false, constrained: path?.constrained ?? false,
                                    lowPower: p.isLowPowerModeEnabled, thermal: p.thermalState, mobileData: mobileData)
    }

    /// The picture's long edge at most this many pixels: the phone's screen, and never under 1080p's.
    static var maxSize: Int {
        let scene = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first
        let b = scene?.screen.nativeBounds ?? .zero
        return max(1920, Int(max(b.width, b.height)))
    }

    private static func enqueue(_ pb: CVPixelBuffer, in layers: [AVSampleBufferDisplayLayer]) {
        guard !layers.isEmpty, let sample = RFBFrameSink.sample(pb, immediate: true) else { return }
        for l in layers { RFBFrameSink.enqueue(sample, in: l) }
    }

    /// The hub's relay of the video channel (`…/screen/video/connect?pair=…`), the way RFB's goes.
    static func hubTransport(_ r: Ready) -> RFBTransport? {
        guard let api = AppModel.shared.api(r.group) else { return nil }
        let path = "/api/v1/machines/\(r.machine)/screen/video/connect"
        // The live screen's session (#6398): the video joins it as a channel when the RFB stream opened one.
        if ScreenRoutes.enabled, let session = ScreenLinkRegistry.shared.session(for: r.pair) {
            let ws = RFBWebSocket(api: api, path: path, pair: r.pair, protocols: [ScreenLinkWire.subprotocol, "binary"])
            return ScreenLinkRFBTransport(ScreenLinkVideoStream(socket: ScreenLinkRelaySocket(ws), session: session))
        }
        return RFBWebSocket(api: api, path: path, pair: r.pair)
    }
}

/// What the stats overlay shows of the video channel (#3954): frames shown and megabits that came in the last second,
/// the phone's decode time, and the helper's capture, encode and network times (from `timing`; absent from a helper
/// before it). The total adds the segments up only when encode, network and decode are all known.
nonisolated struct ScreenVideoReading: Equatable, Sendable {
    var fps: Double?
    var mbps: Double?
    var captureMs: Double?
    var encodeMs: Double?
    var networkMs: Double?
    var decodeMs: Double?

    var totalMs: Double? {
        guard let encodeMs, let networkMs, let decodeMs else { return nil }
        return (captureMs ?? 0) + encodeMs + networkMs + decodeMs
    }
}

/// Counts the pictures shown and keeps the helper's latest `timing`, and turns them into a reading once a second.
nonisolated struct ScreenVideoTally {
    private var frames = 0
    private var decode: [Double] = []
    private var since: ContinuousClock.Instant?
    /// The stream's received bytes at the last reading (a new stream starts at 0).
    private var bytesAt = 0
    private var helperTiming: TimingMessage?

    mutating func picture(decodeMs: Double, at now: ContinuousClock.Instant) {
        if since == nil { since = now }
        frames += 1
        decode.append(decodeMs)
    }

    mutating func helper(_ t: TimingMessage) { helperTiming = t }

    /// The last second's reading once a second has passed since the last one (or since the first picture); nil before.
    mutating func reading(at now: ContinuousClock.Instant, received: Int) -> ScreenVideoReading? {
        guard let since, now - since >= .seconds(1) else { return nil }
        let d = now - since
        let seconds = Double(d.components.seconds) + Double(d.components.attoseconds) / 1e18
        defer {
            frames = 0
            decode = []
            self.since = now
            bytesAt = received
        }
        return ScreenVideoReading(fps: (Double(frames) / seconds).rounded(), mbps: (Double(max(0, received - bytesAt)) * 8 / 1e6 / seconds * 10).rounded() / 10,
                                  captureMs: helperTiming?.captureMs, encodeMs: helperTiming?.encodeMs, networkMs: helperTiming?.networkMs,
                                  decodeMs: Self.median(decode))
    }

    private static func median(_ values: [Double]) -> Double? {
        guard !values.isEmpty else { return nil }
        let s = values.sorted(), mid = s.count / 2
        let m = s.count % 2 == 1 ? s[mid] : (s[mid - 1] + s[mid]) / 2
        return (m * 10).rounded() / 10
    }
}

/// The tier as Connection Stats shows it (#6396): the Quality choice, the scale and frame rate, and why it last changed
/// in plain words with how long ago.
enum ScreenTierText {
    /// The scale is the picture's own (#7842): "1×" for every tier of a display at one pixel per point, "1.9×" for 2×
    /// under the phone's long-edge cap.
    static func value(_ quality: ScreenQuality, _ tier: TierLadder.Tier) -> String {
        let tenths = (tier.pixelsPerPoint * 10).rounded() / 10
        let scale = tenths == tenths.rounded() ? String(Int(tenths)) : String(format: "%.1f", tenths)
        return "\(quality.label) · \(scale)× · \(tier.fps) fps"
    }

    static func reason(_ r: TierReason, at: Date, now: Date) -> String {
        let seconds = max(0, Int(now.timeIntervalSince(at)))
        let ago = seconds < 60 ? String(localized: "\(seconds) s ago") : String(localized: "\(seconds / 60) min ago")
        return "\(words(r)) · \(ago)"
    }

    static func words(_ r: TierReason) -> String {
        switch r {
        case .start: String(localized: "Picked for this connection")
        case .linkSlowed: String(localized: "Connection slowed down")
        case .linkSteady: String(localized: "Connection is steady")
        case .networkChanged: String(localized: "Network changed")
        case .phoneWarm: String(localized: "iPhone is warm or saving power")
        case .phoneBehind: String(localized: "iPhone couldn’t keep up")
        case .choice: String(localized: "You changed Quality")
        }
    }
}

/// The newest decoded picture on its way to the main actor: `put` says whether a hop is needed (none is on its way).
nonisolated final class ScreenVideoMailbox: @unchecked Sendable {
    private let lock = NSLock()
    private var picture: (CVPixelBuffer, Double)?

    func put(_ pb: CVPixelBuffer, _ ms: Double) -> Bool {
        lock.withLock {
            defer { picture = (pb, ms) }
            return picture == nil
        }
    }

    func take() -> (CVPixelBuffer, Double)? {
        lock.withLock {
            defer { picture = nil }
            return picture
        }
    }
}
#endif
