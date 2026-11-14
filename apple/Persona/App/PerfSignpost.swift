#if DEBUG || PERF_SIGNPOST
import os
import QuartzCore
import SwiftUI
#if canImport(UIKit)
import UIKit
#elseif canImport(AppKit)
import AppKit
#endif

nonisolated enum PerfTrace {
    private static let log = OSLog(subsystem: "Persona.Performance", category: .pointsOfInterest)

    static func event(_ name: StaticString, _ detail: String = "") {
        os_signpost(.event, log: log, name: name, "%{public}@", detail as NSString)
    }

    /// The root's first appearance, immediately before SwiftUI hands its first frame to Core Animation. This is the
    /// closest app-side launch boundary; XCTApplicationLaunchMetric supplies the actual displayed-frame timestamp.
    /// Later root appearances (for example a look change) are not launches. The static Bool adds no allocation or IO.
    @MainActor private static var firstFrameMarked = false
    @MainActor static func firstFrame() {
        guard !firstFrameMarked else { return }
        firstFrameMarked = true
        event("First frame (root appeared)")
    }

    /// A view's body was evaluated: a signpost (Instruments' Points of Interest) and a count (`PerfCount`).
    @MainActor static func body(_ name: StaticString) {
        os_signpost(.event, log: log, name: "Body", "%{public}@", "\(name)" as NSString)
        PerfCount.body("\(name)")
    }

    /// A hub push is being applied (`AppModel.handle`).
    @MainActor static func push(_ kind: String) {
        os_signpost(.event, log: log, name: "Push", "%{public}@", kind as NSString)
        PerfCount.pushArrived(kind)
    }
}

/// What hub pushes cost, for `LiveUpdateTests` (launched with `-perfCounts YES`): view bodies evaluated, by view, and the
/// main thread's time from a push until the run loop goes idle again (SwiftUI's update and layout included; pushes
/// applied in the same pass of the run loop count once there). Read through the hidden `perf.counts` text.
@MainActor enum PerfCount {
    static let enabled = UserDefaults.standard.bool(forKey: "perfCounts")
    private(set) static var bodies: [String: Int] = [:]
    private(set) static var events = 0
    private(set) static var passes = 0
    private(set) static var totalMS = 0.0
    private(set) static var maxMS = 0.0
    private static var started: CFTimeInterval?

    static func body(_ name: String) {
        guard enabled else { return }
        bodies[name, default: 0] += 1
        #if os(iOS) || os(visionOS)
        if PerfScroll.shared.scrolling { PerfScroll.shared.bodies[name, default: 0] += 1 }
        #endif
    }

    static func pushArrived(_ kind: String) {
        guard enabled else { return }
        events += 1
        #if os(iOS) || os(visionOS)
        PerfScroll.shared.pushed(kind)
        #endif
        guard started == nil else { return }
        started = CACurrentMediaTime()
        // After Core Animation's commit (order 2,000,000), when the main run loop has nothing left to do.
        let observer = CFRunLoopObserverCreateWithHandler(nil, CFRunLoopActivity.beforeWaiting.rawValue, false, CFIndex.max) { _, _ in
            MainActor.assumeIsolated { finish() }
        }
        CFRunLoopAddObserver(CFRunLoopGetMain(), observer, .commonModes)
    }

    private static func finish() {
        guard let t0 = started else { return }
        let ms = (CACurrentMediaTime() - t0) * 1000
        started = nil
        passes += 1
        totalMS += ms
        maxMS = max(maxMS, ms)
    }

    static var summary: String {
        let b = bodies.sorted { $0.key < $1.key }.map { "\"\($0.key)\":\($0.value)" }.joined(separator: ",")
        #if os(iOS) || os(visionOS)
        let scroll = ",\"scroll\":" + PerfScroll.shared.summary
        #else
        let scroll = ""
        #endif
        let blocks: String
        if let data = try? JSONSerialization.data(withJSONObject: MainThreadWatchdog.shared.report, options: [.sortedKeys]),
           let json = String(data: data, encoding: .utf8) {
            blocks = ",\"main_thread_blocks\":" + json
        } else {
            blocks = ""
        }
        return "{\"bodies\":{\(b)},\"events\":\(events),\"passes\":\(passes),\"ms\":\(String(format: "%.1f", totalMS)),\"max\":\(String(format: "%.1f", maxMS))\(blocks)\(scroll)}"
    }
}

/// The counts as a hidden text the UI tests read (`-perfCounts YES`).
struct PerfCountProbe: View {
    var body: some View {
        if PerfCount.enabled {
            TimelineView(.periodic(from: .now, by: 0.25)) { _ in
                Text(PerfCount.summary).font(Theme.Font.label).lineLimit(1).opacity(0.02).accessibilityIdentifier("perf.counts")
                    .allowsHitTesting(false)
            }
            .frame(width: Theme.Size.dotXS, height: Theme.Size.dotXS)
            .clipped()
            #if os(iOS) || os(visionOS)
            .onAppear { PerfScroll.shared.start() }
            #endif
        }
    }
}

#if os(iOS) || os(visionOS)
/// Scrolling under the hub's pushes (`-perfCounts YES`; `ScrollUnderPushTests`, docs/archive/apple/live-updates.md). Every frame
/// a scroll view is dragged or gliding, where what is on it sits on screen: its cells (a `List`), or the layers SwiftUI
/// draws a `ScrollView`'s rows in. Under the finger and in a glide everything on screen moves together and smoothly;
/// the counts are the frames where it did not:
/// - `jumps`: the content as a whole left the finger, or a glide sped up or turned (inserted above, back to the top);
///   `big` of them by 100 pt or more. The small ones happen with no push at all (the lazy rows sizing themselves, the
///   synthesized drags of a UI test): measured on the real hub with its pushes held back, none reached 100 pt;
/// - `moved`: rows moved against each other (reordered, resized, a row put in between); read it on lists: on a page of
///   glass cards (Machines) the effects' own layers move against the content with no push at all;
/// - `replaced`: something on screen was taken away and drawn anew while it was still in view (a flash);
/// - `stopped`: a glide cut short;
/// - `resized`: the content's height changed;
/// - `defaults`: UserDefaults changed (every `@AppStorage` view in the app is drawn again: the list re-laid itself out).
/// Each is put down to the kind of push that came in the 0.6 s before it (`causes`, "kind:push"), or "none".
/// One in the 0.3 s after a page let go of what it held (`released()`: the Agents list), or in the first 0.5 s of the
/// first scroll after it let go at rest, is that landing: the update arriving as designed, which UIKit lays out under
/// the next touch (docs/apple/ios-state.md「Timely」). Counted apart (`landed`), not as the above.
@MainActor final class PerfScroll {
    static let shared = PerfScroll()
    private(set) var scrolling = false
    var bodies: [String: Int] = [:]
    private var pushes: [String: Int] = [:]
    private var causes: [String: Int] = [:]
    private var frames = 0, jumps = 0, big = 0, moved = 0, replaced = 0, stopped = 0, resized = 0, defaults = 0, landed = 0
    private var releasedAt: CFTimeInterval = 0
    private var scrollEndedAt: CFTimeInterval = 0
    private var scrollStartedAt: CFTimeInterval = 0
    private var maxJump: CGFloat = 0, maxMoved: CGFloat = 0
    private var lastPush: (kind: String, at: CFTimeInterval)?
    private var link: CADisplayLink?
    private var candidates: [Weak] = []
    private var lastScan: CFTimeInterval = 0
    private var prev: Frame?
    private static let log = OSLog(subsystem: "Persona.Performance", category: "Scroll")

    private struct Weak { weak var view: UIScrollView? }

    private struct Frame {
        weak var view: UIScrollView?
        var id: ObjectIdentifier
        var t: CFTimeInterval
        var finger: CGFloat
        var tracking: Bool
        var trackedFrames = 0 // under the finger for this many frames (its first moves include the touch slop)
        var decelerating: Bool
        var inBounds: Bool
        var height: CGFloat
        var offset: CGFloat = 0
        var inset: CGFloat = 0
        var viewport: CGRect
        var marks: [ObjectIdentifier: CGRect]
        var names: [ObjectIdentifier: String] // what each mark is, for the log
        var step: CGFloat? // how far it all moved on screen since the frame before, per second
    }

    func start() {
        guard link == nil, PerfCount.enabled else { return }
        let l = CADisplayLink(target: self, selector: #selector(tick))
        l.add(to: .main, forMode: .common)
        link = l
        NotificationCenter.default.addObserver(forName: UserDefaults.didChangeNotification, object: nil, queue: .main) { _ in
            MainActor.assumeIsolated {
                guard PerfScroll.shared.scrolling else { return }
                PerfScroll.shared.defaults += 1
                PerfScroll.shared.note("defaults", CACurrentMediaTime(), "")
            }
        }
    }

    func pushed(_ kind: String) {
        lastPush = (kind, CACurrentMediaTime())
        if scrolling { pushes[kind, default: 0] += 1 }
    }

    var summary: String {
        func dict(_ d: [String: Int]) -> String { "{" + d.sorted { $0.key < $1.key }.map { "\"\($0.key)\":\($0.value)" }.joined(separator: ",") + "}" }
        return "{\"frames\":\(frames),\"jumps\":\(jumps),\"big\":\(big),\"maxJump\":\(Int(maxJump)),\"moved\":\(moved),\"maxMoved\":\(Int(maxMoved)),"
            + "\"replaced\":\(replaced),\"stopped\":\(stopped),\"resized\":\(resized),\"defaults\":\(defaults),\"landed\":\(landed),\"pushes\":\(dict(pushes)),\"bodies\":\(dict(bodies)),\"causes\":\(dict(causes))}"
    }

    @objc private func tick(_ l: CADisplayLink) {
        let now = CACurrentMediaTime()
        var active = candidates.lazy.compactMap(\.view).first { $0.window != nil && ($0.isTracking || $0.isDecelerating) }
        if active == nil, now - lastScan > 0.5 {
            rescan(now)
            active = candidates.lazy.compactMap(\.view).first { $0.window != nil && ($0.isTracking || $0.isDecelerating) }
        }
        guard let sv = active else {
            if let p = prev, p.decelerating, let v = p.step, abs(v) > 300, p.inBounds, let view = p.view, !view.isTracking {
                note("stopped", now, String(format: "at %.0f pt/s", v))
                stopped += 1
            }
            if scrolling { scrollEndedAt = now }
            prev = nil
            scrolling = false
            return
        }
        if !scrolling { scrollStartedAt = now }
        scrolling = true
        frames += 1
        var f = capture(sv, now)
        f.offset = sv.contentOffset.y
        f.inset = sv.adjustedContentInset.top
        if let p = prev, p.id == f.id {
            if f.tracking { f.trackedFrames = p.trackedFrames + 1 }
            compare(p, &f, now)
        }
        prev = f
    }

    private func rescan(_ now: CFTimeInterval) {
        lastScan = now
        var found: [Weak] = []
        func walk(_ v: UIView) {
            if let s = v as? UIScrollView, s.bounds.height > 200, s.contentSize.height > s.bounds.height { found.append(Weak(view: s)) }
            for c in v.subviews where !c.isHidden { walk(c) }
        }
        for scene in UIApplication.shared.connectedScenes {
            for w in (scene as? UIWindowScene)?.windows ?? [] where !w.isHidden { walk(w) }
        }
        candidates = found
    }

    private func capture(_ sv: UIScrollView, _ now: CFTimeInterval) -> Frame {
        let viewport = sv.convert(sv.bounds, to: nil)
        var marks: [ObjectIdentifier: CGRect] = [:]
        var names: [ObjectIdentifier: String] = [:]
        func keep(_ r: CGRect) -> Bool { r.width >= 40 && r.height >= 12 && r.height <= viewport.height * 1.2 && r.intersects(viewport) }
        if let cv = sv as? UICollectionView {
            for c in cv.visibleCells {
                let r = c.convert(c.bounds, to: nil)
                if keep(r) {
                    marks[ObjectIdentifier(c)] = r
                    names[ObjectIdentifier(c)] = c.accessibilityLabel ?? String(describing: type(of: c))
                }
            }
        } else if let window = sv.window {
            var n = 0
            func walk(_ layer: CALayer, _ depth: Int) {
                guard n < 400 else { return }
                for sub in layer.sublayers ?? [] where !sub.isHidden && sub.opacity > 0 {
                    if sub.delegate is UIImageView, sub.superlayer === sv.layer { continue } // the scroll indicators
                    let r = sub.convert(sub.bounds, to: window.layer)
                    if keep(r) {
                        marks[ObjectIdentifier(sub)] = r
                        names[ObjectIdentifier(sub)] = sub.delegate.map { String(describing: type(of: $0)) } ?? String(describing: type(of: sub))
                        n += 1
                    }
                    if depth < 6 { walk(sub, depth + 1) }
                }
            }
            walk(sv.layer, 0)
        }
        let top = -sv.adjustedContentInset.top, bottom = max(top, sv.contentSize.height + sv.adjustedContentInset.bottom - sv.bounds.height)
        return Frame(view: sv, id: ObjectIdentifier(sv), t: now, finger: sv.panGestureRecognizer.translation(in: nil).y,
                     tracking: sv.isTracking, decelerating: sv.isDecelerating && !sv.isTracking,
                     inBounds: sv.contentOffset.y >= top + 1 && sv.contentOffset.y <= bottom - 1,
                     height: sv.contentSize.height, viewport: viewport, marks: marks, names: names)
    }

    private func compare(_ p: Frame, _ f: inout Frame, _ now: CFTimeInterval) {
        if p.height != f.height {
            resized += 1
            note("resized", now, String(format: "%.0f -> %.0f", p.height, f.height))
        }
        let common = f.marks.keys.filter { p.marks[$0] != nil }
        guard !common.isEmpty else { return }
        let steps = common.map { f.marks[$0]!.minY - p.marks[$0]!.minY }.sorted()
        let step = steps[steps.count / 2]
        let dt = max(f.t - p.t, 1.0 / 240)
        f.step = step / dt
        // Rows that moved against the rest.
        let off = steps.map { abs($0 - step) }.max() ?? 0
        if off > 1.5, !landing(now, "moved") {
            moved += 1
            maxMoved = max(maxMoved, off)
            note("moved", now, String(format: "%.0f pt", off))
        }
        // The whole: under the finger it follows the finger; in a glide it only slows down.
        if p.inBounds && f.inBounds {
            var jump: CGFloat = 0
            if p.tracking && f.tracking && p.trackedFrames >= 3 {
                jump = abs(step - (f.finger - p.finger))
                if jump <= 3 { jump = 0 }
            } else if p.decelerating && f.decelerating, let v0 = p.step, let v1 = f.step {
                let turned = v0 != 0 && v1 != 0 && (v0 > 0) != (v1 > 0)
                if turned || abs(v1) > abs(v0) * 1.15 + 150 { jump = abs(v1 - v0) * dt }
            }
            if jump > 0, !(jump >= 100 && landing(now, "big")) {
                jumps += 1
                if jump >= 100 { big += 1 }
                maxJump = max(maxJump, jump)
                note(jump >= 100 ? "big" : "jump", now, String(format: "%.0f pt (offset %.0f -> %.0f, inset %.0f -> %.0f, height %.0f -> %.0f, %@)",
                                                               jump, p.offset, f.offset, p.inset, f.inset, p.height, f.height, f.tracking ? "finger" : "glide"))
            }
        }
        // Taken away while still in view.
        let shift = abs(step) + 2
        let inner = p.viewport.insetBy(dx: 0, dy: shift)
        // A list's cells; in a ScrollView whole rows only (a line of text drawn anew, like a running timer, is no flash).
        let rows = p.names.values.contains { $0.contains("Cell") }
        let gone = p.marks.filter { f.marks[$0.key] == nil && inner.contains($0.value) && (rows || $0.value.height >= 40) }
        if !gone.isEmpty, !landing(now, "replaced") {
            replaced += 1
            let what = gone.map { "\(p.names[$0.key] ?? "?") at \(Int($0.value.minY))" }.sorted().prefix(3).joined(separator: "; ")
            note("replaced", now, "\(gone.count) of \(p.marks.count): \(what)")
        }
    }

    /// A page let what it held land (at rest).
    func released() { releasedAt = CACurrentMediaTime() }

    /// Something the page did about the scrolling (the Agents list held its data, let it go), on the same timeline.
    static func mark(_ what: String) {
        guard PerfCount.enabled else { return }
        os_log(.default, log: log, "PERFSCROLL mark %{public}@", what)
    }

    /// The landing's, counted apart: within 0.3 s of a page letting go, or early in the first scroll after it let go
    /// at rest.
    private func landing(_ now: CFTimeInterval, _ what: String) -> Bool {
        let atRest = releasedAt > scrollEndedAt && scrollStartedAt > releasedAt && now - scrollStartedAt < 0.5
        guard now - releasedAt < 0.3 || atRest else { return false }
        landed += 1
        causes["landed:" + what, default: 0] += 1
        return true
    }

    private func note(_ what: String, _ now: CFTimeInterval, _ detail: String) {
        let cause = lastPush.flatMap { now - $0.at < 0.6 ? $0.kind : nil } ?? "none"
        causes[what + ":" + cause, default: 0] += 1
        os_log(.default, log: Self.log, "PERFSCROLL %{public}@ %{public}@ cause=%{public}@", what, detail, cause)
    }
}
#endif

@MainActor private final class PerfTabLifetime: ObservableObject {
    nonisolated let name: String
    nonisolated let token = UUID()

    init(_ name: String) {
        self.name = name
        PerfTrace.event("Tab lifetime start", "\(name) \(token)")
    }

    deinit { PerfTrace.event("Tab lifetime end", "\(name) \(token)") }
}

@MainActor struct PerfTabMount: ViewModifier {
    @StateObject private var lifetime: PerfTabLifetime
    @Environment(\.colorScheme) private var scheme
    @Environment(\.dynamicTypeSize) private var typeSize
    private let name: String

    init(_ name: String) {
        self.name = name
        _lifetime = StateObject(wrappedValue: PerfTabLifetime(name))
    }

    func body(content: Content) -> some View {
        let _ = PerfTrace.event("Tab mount body", "\(name) \(lifetime.token)")
        content
            .background { PerfWindowProbe(name: name).frame(width: 0, height: 0).allowsHitTesting(false).accessibilityHidden(true) }
            .onAppear { PerfTrace.event("Tab appear", "\(name) \(lifetime.token)") }
            .onDisappear { PerfTrace.event("Tab disappear", "\(name) \(lifetime.token)") }
            .onChange(of: scheme) { _, _ in PerfTrace.event("Tab color scheme", name) }
            .onChange(of: typeSize) { _, _ in PerfTrace.event("Tab type size", name) }
    }
}

#if os(iOS) || os(visionOS)
@MainActor private struct PerfWindowProbe: UIViewRepresentable {
    let name: String
    func makeUIView(context: Context) -> PerfProbeView { PerfProbeView(name) }
    func updateUIView(_ view: PerfProbeView, context: Context) { PerfTrace.event("Tab UIView update", view.detail) }
}

@MainActor private final class PerfProbeView: UIView {
    nonisolated let name: String
    nonisolated let token = UUID()
    nonisolated var detail: String { "\(name) \(token)" }

    init(_ name: String) {
        self.name = name
        super.init(frame: .zero)
        PerfTrace.event("Tab UIView create", detail)
    }

    required init?(coder: NSCoder) { fatalError() }
    override func didMoveToWindow() {
        super.didMoveToWindow()
        PerfTrace.event("Tab window", "\(detail) attached=\(window != nil)")
    }
    override func traitCollectionDidChange(_ previousTraitCollection: UITraitCollection?) {
        super.traitCollectionDidChange(previousTraitCollection)
        PerfTrace.event("Tab UIKit traits", detail)
    }
    deinit { PerfTrace.event("Tab UIView destroy", detail) }
}
#else
/// The Mac's probe: when the tab's view joins or leaves a window.
@MainActor private struct PerfWindowProbe: NSViewRepresentable {
    let name: String
    func makeNSView(context: Context) -> PerfProbeView { PerfProbeView(name) }
    func updateNSView(_ view: PerfProbeView, context: Context) { PerfTrace.event("Tab NSView update", view.detail) }
}

@MainActor private final class PerfProbeView: NSView {
    nonisolated let name: String
    nonisolated let token = UUID()
    nonisolated var detail: String { "\(name) \(token)" }

    init(_ name: String) {
        self.name = name
        super.init(frame: .zero)
        PerfTrace.event("Tab NSView create", detail)
    }

    required init?(coder: NSCoder) { fatalError() }
    override func viewDidMoveToWindow() {
        super.viewDidMoveToWindow()
        PerfTrace.event("Tab window", "\(detail) attached=\(window != nil)")
    }
    deinit { PerfTrace.event("Tab NSView destroy", detail) }
}
#endif
#endif
