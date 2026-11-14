import SwiftUI

// MARK: - Per-Mac preferences (kept on this device, as VNCMode / VNCDisplayChoice are)

/// Which picture channel a Mac's live screen uses (#3954), kept per Mac on this device: the hardware **video** channel
/// (smoother, far less bandwidth) or **VNC/RFB** (always available). `of` is nil until the user picks one, so the page
/// can default to video when the computer offers the channel and Screen Recording is granted, else VNC.
enum ScreenPicture: String, CaseIterable {
    case video, vnc
    private static func key(_ ref: MachineRef) -> String { "screen.picture." + ScreenCredentials.key(ref) }
    static func of(_ ref: MachineRef) -> ScreenPicture? {
        UserDefaults.standard.string(forKey: key(ref)).flatMap(ScreenPicture.init(rawValue:))
    }
    static func set(_ p: ScreenPicture, for ref: MachineRef) { UserDefaults.standard.set(p.rawValue, forKey: key(ref)) }

    var label: String {
        switch self {
        case .video: String(localized: "Video (smooth, light)")
        case .vnc: String(localized: "VNC (compatible)")
        }
    }
    var icon: String { self == .video ? Theme.Icon.video : Theme.Icon.screen }
}

/// The quality bias for the video channel (#3954; the interface #6396 fills), kept per Mac: **Auto** is Moonlight-style
/// adaptive tiering; **Sharper** pins the tier toward resolution, **Smoother** toward frame rate.
enum ScreenQuality: String, CaseIterable {
    case auto, sharper, smoother
    private static func key(_ ref: MachineRef) -> String { "screen.quality." + ScreenCredentials.key(ref) }
    static func of(_ ref: MachineRef) -> ScreenQuality {
        UserDefaults.standard.string(forKey: key(ref)).flatMap(ScreenQuality.init(rawValue:)) ?? .auto
    }
    static func set(_ q: ScreenQuality, for ref: MachineRef) { UserDefaults.standard.set(q.rawValue, forKey: key(ref)) }

    var label: String {
        switch self {
        case .auto: String(localized: "Auto")
        case .sharper: String(localized: "Sharper")
        case .smoother: String(localized: "Smoother")
        }
    }
}

/// Whether ⋯ › Quality can do anything (#6396): both this app and the Mac's helper must have quality tiers. The helper
/// says so in `hello.features`; this app says so here. One release carries the app and Persona Link together, so a
/// helper with tiers can meet an app without its side of them, and a choice that changes nothing must not show.
/// `appSupported`: the iPhone's tier policy is in (#6396).
nonisolated enum ScreenTiers {
    static let appSupported = true

    static func offered(helperHasTiers: Bool, appSupported: Bool = appSupported) -> Bool { appSupported && helperHasTiers }
}

/// Whether the connection-stats overlay is shown for a Mac (#3954), kept per Mac. Off by default.
enum ScreenStatsPref {
    private static func key(_ ref: MachineRef) -> String { "screen.stats." + ScreenCredentials.key(ref) }
    static func of(_ ref: MachineRef) -> Bool { UserDefaults.standard.bool(forKey: key(ref)) }
    static func set(_ on: Bool, for ref: MachineRef) { UserDefaults.standard.set(on, forKey: key(ref)) }
}

// MARK: - Stats snapshot

/// One reading for the connection-stats overlay (#3954), refreshed about once a second from the same instrumentation
/// that measured the PoC gates — so what the overlay shows equals what was measured, and it adds no work on the media
/// path. VNC mode fills what RFB measures (round-trip, frame rate, bitrate); video mode adds the per-segment latency
/// and the codec / resolution. A segment that isn't measured is nil and left out. `path` (#6398) and `tier` (#6396)
/// appear when known.
struct ScreenStats: Equatable {
    enum Mode { case video, vnc
        var label: String { self == .video ? String(localized: "Video") : String(localized: "VNC") }
    }
    /// Which network path the stream is on (#6398).
    enum Path: Equatable {
        case directLAN, directTailscale, relay
        var label: String {
            switch self {
            case .directLAN: String(localized: "Direct · LAN")
            case .directTailscale: String(localized: "Direct · Tailscale")
            case .relay: String(localized: "Via hub")
            }
        }
    }

    var mode: Mode
    var path: Path? = nil
    /// The route's own numbers (#6398): its smoothed round trip and the share of packets lost, when measured.
    var routeRttMs: Double? = nil
    var routeLoss: Double? = nil
    /// Why the route last changed, in plain words (`ScreenRouteReason`); nil at the start.
    var routeReason: String? = nil
    var tier: String? = nil            // #6396, e.g. "Auto · 1.5× · 60 fps" (`ScreenTierText`)
    var tierReason: String? = nil      // #6396, why it last changed and how long ago, in plain words
    var bitrateMbps: Double? = nil
    var fps: Double? = nil
    var codec: String? = nil           // video only, e.g. "HEVC"
    var resolution: String? = nil      // video only, e.g. "1920×1080"
    // Per-segment latency (ms), video mode. Capture ≈ 0 on the Mac (see gate ①), so it may be nil / folded into total.
    var captureMs: Double? = nil
    var encodeMs: Double? = nil
    var networkMs: Double? = nil
    var decodeMs: Double? = nil
    var totalMs: Double? = nil
    // VNC mode: what RFB can measure.
    var roundTripMs: Double? = nil
    /// The phone's round trip to the hub on its own hub connection (#6558, `HubRoundTrips`): not the hub's way on to the
    /// Mac, so the overlay names it on a line of its own and never shows it as the picture's latency. The web overlay
    /// shows the same leg.
    var hubRoundTripMs: Double? = nil

    /// A bitrate as the overlay writes it, in Mbps: one decimal, two below 1 Mbps so a small stream that was measured
    /// doesn't read as 0.0 (the web overlay's rule, `mbpsText`).
    nonisolated static func mbpsText(_ mbps: Double) -> String {
        String(format: mbps < 1 ? "%.2f Mbps" : "%.1f Mbps", mbps)
    }

    /// The latency segments present, in pipeline order, for the overlay's "Capture · Encode · Network · Decode" line.
    var segments: [(label: String, ms: Double)] {
        var out: [(String, Double)] = []
        if let v = captureMs { out.append((String(localized: "Capture"), v)) }
        if let v = encodeMs { out.append((String(localized: "Encode"), v)) }
        if let v = networkMs { out.append((String(localized: "Network"), v)) }
        if let v = decodeMs { out.append((String(localized: "Decode"), v)) }
        return out
    }
}

#if os(iOS) || os(visionOS)

// MARK: - Round trips to the hub

/// The recent round trips to the hub (#6558), timed on the app's own hub connection while the stats overlay is shown.
/// The overlay draws the median of the last `kept` from the last `fresh` seconds, as the web does, so one slow answer
/// doesn't make it jump and an old one (the app was in the background) isn't shown.
nonisolated struct HubRoundTrips: Equatable, Sendable {
    static let kept = 5
    static let fresh: TimeInterval = 20
    private struct Sample: Equatable { var ms: Double; var at: Date }
    private var samples: [Sample] = []

    mutating func add(ms: Double, at: Date) {
        guard ms.isFinite, ms >= 0 else { return }
        samples.append(Sample(ms: ms, at: at))
        if samples.count > Self.kept { samples.removeFirst(samples.count - Self.kept) }
    }

    /// The median of the fresh ones at `now`; nil when there are none.
    func median(at now: Date) -> Double? {
        let ms = samples.filter { now.timeIntervalSince($0.at) <= Self.fresh }.map(\.ms).sorted()
        guard !ms.isEmpty else { return nil }
        let m = ms.count / 2
        return ms.count % 2 == 1 ? ms[m] : (ms[m - 1] + ms[m]) / 2
    }
}

/// Why the live screen's route last changed (#6398), in words anyone reads.
enum ScreenRouteReason {
    enum Kind { case faster, lost, degraded, networkChanged, turnedOff, firewall }

    static func text(_ k: Kind) -> String {
        switch k {
        case .faster: String(localized: "Faster route")
        case .lost: String(localized: "Previous route dropped")
        case .degraded: String(localized: "Previous route got worse")
        case .networkChanged: String(localized: "Network changed")
        case .turnedOff: String(localized: "Turned off in Settings")
        case .firewall: String(localized: "The computer's firewall blocks direct connections")
        }
    }
}

// MARK: - Overlay

/// A small, semi-transparent, draggable panel showing the live-screen connection numbers (#3954): mode, path, tier,
/// bitrate, frame rate, and the latency broken into segments (video mode) or the round-trip (VNC mode). Off by default,
/// toggled from "…", kept per Mac. It draws a snapshot the page refreshes about once a second and does no media work.
struct ScreenStatsOverlay: View {
    let stats: ScreenStats
    let onClose: () -> Void
    @State private var placed = CGSize.zero
    @GestureState private var dragging = CGSize.zero

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            header
            if let route = routeText {
                kv(String(localized: "Route [Screen Stats]"), route)
                // Why it last changed, on its own line as the tier's: the reasons are words and would widen the panel
                // past the screen at the larger text sizes.
                if let why = stats.routeReason { Text(why).foregroundStyle(.tertiary) }
            }
            if let tier = stats.tier {
                kv(String(localized: "Tier"), tier)
                // Why it last changed, on its own line: the reasons are words and would widen the panel past the screen.
                if let why = stats.tierReason { Text(why).foregroundStyle(.tertiary) }
            }
            rateLine
            latencyLine
            if let hub = stats.hubRoundTripMs {
                Text("Round trip to the hub \(Int(hub.rounded())) ms")
                    .foregroundStyle(.secondary)
                    .accessibilityIdentifier("screen-stats-hub")
            }
        }
        .font(Theme.Font.caption)
        .monospacedDigit()
        .foregroundStyle(.primary)
        .padding(Theme.Space.s)
        .fixedSize()
        .surface(in: .rect(cornerRadius: Theme.Radius.panel, style: .continuous))
        .offset(x: placed.width + dragging.width, y: placed.height + dragging.height)
        .gesture(
            DragGesture()
                .updating($dragging) { value, state, _ in state = value.translation }
                .onEnded { value in placed.width += value.translation.width; placed.height += value.translation.height }
        )
        .padding(Theme.Space.m)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("screen-stats")
    }

    private var header: some View {
        HStack(spacing: Theme.Space.xs) {
            Text(stats.mode.label).font(Theme.Font.captionStrong)
            if let path = stats.path {
                Text("·").foregroundStyle(.tertiary)
                Text(path.label).foregroundStyle(.secondary)
            }
            // The one number the user watches most: the end-to-end total (video) or the round-trip (VNC), up here on the
            // first line rather than trailing the per-segment breakdown. The segments line below breaks it down.
            if let total = primaryLatencyMs {
                Text("·").foregroundStyle(.tertiary)
                Text(ms(total))
            }
            Spacer(minLength: Theme.Space.m)
            Button { onClose() } label: { ThemedIcon(Theme.Icon.close).font(Theme.Font.caption) }
                .buttonStyle(.plain)
                .foregroundStyle(.secondary)
                .accessibilityLabel("Hide Connection Stats")
                .accessibilityIdentifier("screen-stats-close")
        }
    }

    /// The one latency number the user watches most: the end-to-end total (video) or the round-trip (VNC). It leads the
    /// header; the per-segment line breaks it down.
    private var primaryLatencyMs: Double? { stats.totalMs ?? stats.roundTripMs }

    /// bitrate · fps, and codec · resolution in video mode.
    private var rateLine: some View {
        var parts: [String] = []
        if let mbps = stats.bitrateMbps { parts.append(ScreenStats.mbpsText(mbps)) }
        if let fps = stats.fps { parts.append(String(format: "%.0f fps", fps)) }
        if let codec = stats.codec { parts.append(codec) }
        if let res = stats.resolution { parts.append(res) }
        return Text(parts.joined(separator: "  ·  ")).foregroundStyle(.secondary)
    }

    @ViewBuilder private var latencyLine: some View {
        if let text = latencyText { Text(text).foregroundStyle(.secondary) }
    }

    /// The per-segment breakdown, "Capture x · Encode x · Network x · Decode x" (video); omitted segments drop out. The
    /// total that these add up to is in the header. VNC has no segments, so this line is left out and only the header's
    /// round-trip shows.
    private var latencyText: String? {
        let segs = stats.segments
        guard !segs.isEmpty else { return nil }
        return segs.map { "\($0.label) \(ms($0.ms))" }.joined(separator: "  ·  ")
    }

    private func kv(_ label: String, _ value: String, note: String? = nil) -> some View {
        HStack(spacing: Theme.Space.xs) {
            Text(label).foregroundStyle(.secondary)
            Text(value)
            if let note { Text("· \(note)").foregroundStyle(.tertiary) }
        }
    }

    private func ms(_ v: Double) -> String { String(format: "%.0f ms", v) }

    /// The route's round trip and loss (#6398), "6 ms round trip · 0% lost"; nil with nothing measured.
    private var routeText: String? {
        var parts: [String] = []
        if let rtt = stats.routeRttMs { parts.append(String(localized: "\(Int(rtt.rounded())) ms round trip")) }
        if let loss = stats.routeLoss { parts.append(String(localized: "\(Int((loss * 100).rounded()))% lost")) }
        if parts.isEmpty, stats.routeReason == nil { return nil }
        return parts.isEmpty ? (stats.path?.label ?? "") : parts.joined(separator: " · ")
    }
}

#if DEBUG
extension ScreenStats {
    /// Sample numbers for previews and `-showScreenStatsDemo` screenshots (video mode shows the per-segment latency).
    /// Capture ≈ 0 ms on the Mac (gate ①: the frame reaches us before its display time), so it's folded into the total.
    static let demoVideo = ScreenStats(
        mode: .video, path: .directLAN, routeRttMs: 6, routeLoss: 0, routeReason: ScreenRouteReason.text(.faster),
        tier: String(localized: "Auto") + " · 1.5× · 60 fps",
        tierReason: String(localized: "Connection is steady") + " · " + String(localized: "\(12) s ago"),
        bitrateMbps: 3.4, fps: 60, codec: "HEVC", resolution: "2268×1472",
        captureMs: nil, encodeMs: 9, networkMs: 6, decodeMs: 8, totalMs: 23, roundTripMs: nil)
    static let demoVNC = ScreenStats(
        mode: .vnc, path: .relay, tier: nil, tierReason: nil,
        bitrateMbps: 24.0, fps: 6, codec: nil, resolution: nil,
        captureMs: nil, encodeMs: nil, networkMs: nil, decodeMs: nil, totalMs: nil, roundTripMs: 48)
}

#Preview("Stats overlay · video") {
    ZStack { Color.black; ScreenStatsOverlay(stats: .demoVideo) {} }.environment(\.colorScheme, .dark)
}
#Preview("Stats overlay · VNC") {
    ZStack { Color.black; ScreenStatsOverlay(stats: .demoVNC) {} }.environment(\.colorScheme, .dark)
}
#endif

#endif
