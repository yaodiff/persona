import AVFoundation
import AVKit
import OSLog
import SwiftUI
import Synchronization

#if os(iOS)
/// A Mac's VNC in the system's picture in picture (#1343): it floats over the other apps and the home screen, can be
/// dragged and resized, and only shows (touches are the full page's). Restoring it opens the full page; closing it
/// ends the connection unless the page has it.
///
/// Its source is a layer of the Mac's screen of a normal size (#1839: a 2×2 one in a window that went away never
/// started), in the app's main window behind its content: in a window, not hidden, not seen. It is fed samples on the
/// host clock and has a timebase of its own. The system's transition starts and ends at that layer's frame, so it lies
/// over the page's picture while the page is up, and the page hands its picture over as the small window starts
/// (VNCPiPPlacement, #6333). Until the Mac's picture comes it shows a card (VNCPiPCard: Connecting…, or why
/// not and how to try again), so a machine's VNC starts the small window in the tap and the picture follows in it; the
/// page makes it ready as soon as it shows a Mac, so that Picture in Picture (a tap) and going home start it at once. When the system
/// will not start it, the page opens saying why (VNCPiPLog, Picture in Picture Diagnostics).
@MainActor final class VNCPiP: NSObject, AVPictureInPictureControllerDelegate, AVPictureInPictureSampleBufferPlaybackDelegate {
    static let shared = VNCPiP()
    nonisolated static let log = Logger(subsystem: "persona", category: "vnc")
    private(set) var ref: MachineRef?
    private var from: SessionKey?
    private var host: VNCScreenUIView?
    private var controller: AVPictureInPictureController?
    private var watching: NSKeyValueObservation?
    /// The small window is asked for (not just made ready under the page).
    private var wanted = false
    /// The system said it is starting (willStart): no more tries.
    private var starting = false
    private var restoring = false
    private var waiting: Task<Void, Never>?
    /// How many times the system was asked since the small window was, and when last.
    private var tries = 0
    private var lastTry = ContinuousClock.now
    /// What the source shows instead of the Mac's picture (nil: the picture), and what keeps it up to date.
    private var card: VNCPiPCard.State?
    private var cardFrames = 0
    private var drawing: Task<Void, Never>?
    /// It could not connect: the small window shows ▶, which connects again. The system asks off the main actor.
    private nonisolated let paused = Mutex(false)
    /// Where the page draws the Mac's whole picture, in screen points, while it is up (#6333): the source lies there.
    private var pageRect: CGRect?
    /// Restore waits for the page's picture before the system grows the small window back into it.
    private let restore = VNCPiPRestore()

    static var supported: Bool { AVPictureInPictureController.isPictureInPictureSupported() || showsSource }
    var active: Bool { controller?.isPictureInPictureActive == true }
    /// How long a machine's VNC asks the system for the small window before the page instead, and how many times.
    static let patience: Duration = .seconds(8)
    static let maxTries = 6

    #if DEBUG
    /// DEBUG -showPiPSource YES: the source over the app's content, where it can be seen, also without picture in
    /// picture (the simulators): what the small window would show, from the tap on (VNCTests).
    static let showsSource = UserDefaults.standard.bool(forKey: "showPiPSource")
    #else
    static let showsSource = false
    #endif

    /// Ready for ref's small window (the page, once it shows the Mac; a machine's VNC, in the tap): the source with the
    /// card in it, the controller and the playback audio session; `automatic`: it starts by itself when the app goes to
    /// the background.
    @discardableResult func prepare(_ ref: MachineRef, from: SessionKey?, automatic: Bool) -> Bool {
        if self.ref == ref, host != nil {
            controller?.canStartPictureInPictureAutomaticallyFromInline = automatic
            if let from { self.from = from }
            return true
        }
        end()
        guard Self.supported else { return note(String(localized: "This device doesn't support Picture in Picture")) }
        let audio = AVAudioSession.sharedInstance()
        do {
            try audio.setCategory(.playback, mode: .moviePlayback, options: [.mixWithOthers])
            try audio.setActive(true)
        } catch {
            note(String(localized: "Couldn't start the audio session: \(error.localizedDescription)"))
        }
        guard let window = Self.mainWindow() else { return note(String(localized: "Can't find the app's main window")) }
        let session = VNC.hold(ref)
        let v = VNCScreenUIView(frame: .zero)
        v.session = session
        v.isUserInteractionEnabled = false
        Self.giveTimebase(v.display)
        session.show(in: v.display, timed: true)
        if Self.showsSource {
            v.accessibilityIdentifier = "vnc-pip-source"
            v.accessibilityLabel = String(localized: "Picture in Picture: live screen") // until the card says otherwise (draw)
            window.addSubview(v)
        } else {
            v.accessibilityElementsHidden = true
            window.insertSubview(v, at: 0) // behind the app's content
        }
        self.ref = ref
        self.from = from
        host = v
        place()
        let size = v.frame.size
        // Where there is no picture in picture (-showPiPSource on a simulator) its initializer gives back nil, which
        // Swift takes for an object: the source only.
        if AVPictureInPictureController.isPictureInPictureSupported() {
            let c = AVPictureInPictureController(contentSource: .init(sampleBufferDisplayLayer: v.display, playbackDelegate: self))
            c.delegate = self
            c.requiresLinearPlayback = true
            c.canStartPictureInPictureAutomaticallyFromInline = automatic
            controller = c
            watching = c.observe(\.isPictureInPicturePossible, options: [.new]) { c, _ in
                let possible = c.isPictureInPicturePossible
                Task { @MainActor in VNCPiP.shared.possibleChanged(possible) }
            }
        }
        VNCPiPLog.add("准备好了：源 \(Int(size.width))×\(Int(size.height))pt，\(automatic ? "回桌面时自己开" : "等开")，现在能开：\(VNCPiPLog.yes(controller?.isPictureInPicturePossible == true))") // i18n-ignore
        // The card at once while there is no picture: the source is not empty in the tap that asked.
        draw()
        drawing = Task { [weak self] in
            while !Task.isCancelled {
                try? await Task.sleep(for: .milliseconds(100))
                guard let self, !Task.isCancelled, self.ref == ref else { return }
                self.draw()
            }
        }
        return true
    }

    /// ref's screen in the small window, at once (in the tap that asked): Connecting… until the picture comes; failing
    /// that, the page, saying why.
    func start(_ ref: MachineRef, from: SessionKey? = nil) {
        if self.ref == ref, active { return }
        VNCPiPLog.add("要小窗") // i18n-ignore
        guard prepare(ref, from: from, automatic: true) else { return fallBack(ref, from, why: VNCPiPLog.last ?? String(localized: "This device doesn't support Picture in Picture")) }
        wanted = true
        starting = false
        tries = 0
        // With nothing in the source the system took the call and did nothing (TestFlight 161's Picture in Picture Diagnostics); the card
        // is in it now (prepare).
        attempt(inTap: true)
        if tries == 0 { VNCPiPLog.add("现在还不能开：能开了就开") } // i18n-ignore
        waiting?.cancel()
        waiting = Task { [weak self] in
            let deadline = ContinuousClock.now + Self.patience
            while ContinuousClock.now < deadline {
                try? await Task.sleep(for: .milliseconds(100))
                guard let self, !Task.isCancelled, self.wanted, !self.active, self.ref == ref else { return }
                self.attempt(inTap: false)
            }
            guard let self, !Task.isCancelled, self.wanted, !self.active, self.ref == ref else { return }
            if Self.showsSource { return VNCPiPLog.add("没有画中画（DEBUG）：只把源摆在前面") } // i18n-ignore
            self.fallBack(ref, self.from, why: self.tries == 0 ? String(localized: "The system says it can't start now") : String(localized: "Tried \(self.tries) times; the system didn’t respond"))
        }
    }

    /// Asks the system for the small window if it can start now (possible, the app in front): in the tap, then again a
    /// moment later while it has not, a few times. In the background it starts by itself (from inline).
    private func attempt(inTap: Bool) {
        guard wanted, !active, !starting, let c = controller, c.isPictureInPicturePossible,
              UIApplication.shared.applicationState == .active, tries < Self.maxTries,
              tries == 0 || ContinuousClock.now - lastTry >= (tries == 1 ? .milliseconds(400) : .seconds(1)) else { return }
        tries += 1
        lastTry = .now
        VNCPiPLog.add(inTap ? "在点按里开" : tries == 1 ? "能开了，开" : "系统没反应，再开一次（第 \(tries) 次）") // i18n-ignore
        c.startPictureInPicture()
    }

    private func possibleChanged(_ possible: Bool) {
        guard let c = controller, c.isPictureInPicturePossible == possible else { return }
        VNCPiPLog.add("现在能开：\(VNCPiPLog.yes(possible))") // i18n-ignore
        if possible { attempt(inTap: false) }
    }

    /// The source's picture while the Mac's is not there: the card, turning while it connects and the small window is
    /// asked for or up; the Mac's last frame over it once there is one. When the kept sign-in is refused, the page
    /// asks for a new one (with the app in front; else the card says so).
    private func draw() {
        guard let ref, let host, let session = VNC.current(ref) else { return }
        let now = VNCPiPCard.State(session.phase, pictured: session.pictured), was = card
        card = now
        if now != was {
            VNCPiPLog.add("小窗显示：\(now?.line ?? "实时画面")") // i18n-ignore
            let failed = if case .failed = now { true } else { false }
            paused.withLock { $0 = failed }
            controller?.invalidatePlaybackState()
            if Self.showsSource { host.accessibilityLabel = now.map { String(localized: "Picture in Picture: \($0.line)") } ?? String(localized: "Picture in Picture: live screen") }
        }
        if now == .signIn, wanted || active, UIApplication.shared.applicationState == .active, AppModel.shared.screenOpen?.machine != ref {
            VNCPiPLog.add("整页重新登录") // i18n-ignore
            return AppModel.shared.openScreen(ref, from: from) // the small window hands over to it
        }
        guard let now else {
            if was != nil { session.show(in: host.display, timed: true) } // the Mac's latest frame over the card's
            return
        }
        guard now != was || (now == .connecting && (wanted || active)) else { return }
        cardFrames += 1
        let name = AppModel.shared.machine(ref)?.name ?? ref.id
        let size = Self.size(session.size, width: Theme.Size.pictureInPictureCard)
        if let pb = VNCPiPCard.frame(now, name: name, size: size, turn: Double(cardFrames % 12) / 12) {
            RFBFrameSink.show(pb, in: host.display)
        }
    }

    /// The page drew the Mac's whole picture here (screen points), or went (nil): the source follows it (#6333). Waiting
    /// to restore, the page is back: once this layout is done, the system may grow the small window into it.
    func pagePicture(_ rect: CGRect?) {
        guard rect != pageRect else { return }
        pageRect = rect
        // The page closing as the small window starts (or is up): the source stays where the picture went from, not
        // moved under the system's transition; the page coming back on Restore moves it again.
        if rect == nil, starting || active { return }
        place()
        if rect != nil, restore.pending {
            VNCPiPLog.add("整页画好了，小窗长回去") // i18n-ignore
            DispatchQueue.main.async { [weak self] in self?.restore.finish() }
        }
    }

    /// The source over the page's picture, or (no page) where the small window comes up.
    private func place() {
        guard let host, let window = host.window, let ref else { return }
        let screen: UICoordinateSpace = window.windowScene?.screen.coordinateSpace ?? window
        let page = pageRect.map { window.convert($0, from: screen) }
        let size = Self.size(VNC.current(ref)?.size ?? .zero, width: Theme.Size.pictureInPictureSource)
        let frame = VNCPiPPlacement.source(page: page, in: window.bounds, safeTop: window.safeAreaInsets.top, size: size, margin: Theme.Space.page)
        guard host.frame != frame else { return }
        UIView.performWithoutAnimation { host.frame = frame }
    }

    /// The small window's ▶ after it could not connect: again (the card turns again).
    private func reconnect() {
        guard let ref, let session = VNC.current(ref), case .closed = session.phase else { return }
        VNCPiPLog.add("在小窗里重新连接") // i18n-ignore
        session.start()
    }

    /// The whole page took it: the small window goes (the source stays ready for the page).
    func handOver() {
        wanted = false
        waiting?.cancel()
        if active, !restoring { controller?.stopPictureInPicture() }
    }

    /// The page went: nothing is left to show the small window for unless it is up or asked for.
    func pageGone(_ ref: MachineRef) {
        if self.ref == ref, !wanted, !active { end() }
    }

    func stop() {
        wanted = false
        if active { controller?.stopPictureInPicture() }
        end()
    }

    private func fallBack(_ ref: MachineRef, _ from: SessionKey?, why: String?) {
        let pageUp = AppModel.shared.screenOpen?.machine == ref
        end()
        if let why {
            VNCPiPLog.add("改用整页：\(why)") // i18n-ignore
            AppModel.shared.toast = Toast(String(localized: "Picture in Picture didn't open: \(why). Switched to full screen."), style: .error)
        }
        if !pageUp { AppModel.shared.openScreen(ref, from: from) }
    }

    private func end() {
        waiting?.cancel()
        waiting = nil
        drawing?.cancel()
        drawing = nil
        card = nil
        paused.withLock { $0 = false }
        restore.finish()
        watching = nil
        wanted = false
        if let ref, let host { VNC.current(ref)?.hide(from: host.display) }
        host?.removeFromSuperview()
        host = nil
        controller = nil
        if let ref { VNC.release(ref) }
        ref = nil
        AppModel.shared.vncPiP = nil
    }

    @discardableResult private func note(_ why: String) -> Bool {
        VNCPiPLog.add(why)
        return false
    }

    /// The app's own window (not the page's, ScreenWindow, nor the toasts'): it stays while the small window is up.
    private static func mainWindow() -> UIWindow? {
        let scenes = UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }
        let scene = scenes.first { $0.activationState == .foregroundActive } ?? scenes.first
        return scene?.windows.first { $0.windowLevel == .normal && !$0.isHidden && $0.rootViewController != nil }
    }

    /// This wide in the Mac's proportions (16:10 before it is known).
    private static func size(_ mac: CGSize, width: CGFloat) -> CGSize {
        let ratio = mac.width > 1 && mac.height > 1 ? mac.height / mac.width : 10.0 / 16
        return CGSize(width: width, height: (width * ratio).rounded())
    }

    /// The layer's own clock, the host's, running: the samples' times (RFBFrameSink) are shown as they come.
    private static func giveTimebase(_ layer: AVSampleBufferDisplayLayer) {
        var timebase: CMTimebase?
        guard CMTimebaseCreateWithSourceClock(allocator: kCFAllocatorDefault, sourceClock: CMClockGetHostTimeClock(), timebaseOut: &timebase) == noErr,
              let timebase else { return }
        CMTimebaseSetTime(timebase, time: CMClockGetTime(CMClockGetHostTimeClock()))
        CMTimebaseSetRate(timebase, rate: 1)
        layer.controlTimebase = timebase
    }

    // MARK: AVPictureInPictureControllerDelegate

    nonisolated func pictureInPictureControllerWillStartPictureInPicture(_ c: AVPictureInPictureController) {
        MainActor.assumeIsolated {
            wanted = true // also when it starts by itself, the app going to the background with the page up
            starting = true
            #if canImport(PersonaScreenKit)
            // The small window shows RFB's picture (its source is prepared under the page all along, so this, not that,
            // is when video steps aside, #3954).
            if let ref { VNC.current(ref)?.video.hold("small window", true) }
            #endif
            VNCPiPLog.add("小窗要开了") // i18n-ignore
            // The picture goes into the small window now: the page hands it over and closes with the transition, not
            // after it (#6333), so the picture never shows twice.
            AppModel.shared.vncPiP = ref
        }
    }

    nonisolated func pictureInPictureControllerDidStartPictureInPicture(_ c: AVPictureInPictureController) {
        MainActor.assumeIsolated {
            waiting?.cancel()
            VNCPiPLog.add("小窗开了") // i18n-ignore
            // The small window has it now: the page closes as its × does (ScreenWindowRoot), its connection held here.
            AppModel.shared.vncPiP = ref
        }
    }

    nonisolated func pictureInPictureController(_ c: AVPictureInPictureController,
                                                restoreUserInterfaceForPictureInPictureStopWithCompletionHandler done: @escaping (Bool) -> Void) {
        nonisolated(unsafe) let done = done
        MainActor.assumeIsolated {
            // Restore (or a tap on it): the full page, which holds the session before this lets go. The system grows the
            // small window back into the source once `done` is called: when the page has drawn its picture and the
            // source lies over it, or after a moment at the latest (#6333).
            VNCPiPLog.add("还原成整页") // i18n-ignore
            restore.arm { done($0) }
            restoring = true
            if let ref { AppModel.shared.openScreen(ref, from: from) }
            restoring = false
        }
    }

    nonisolated func pictureInPictureControllerDidStopPictureInPicture(_ c: AVPictureInPictureController) {
        MainActor.assumeIsolated {
            VNCPiPLog.add("小窗关了") // i18n-ignore
            wanted = false
            starting = false
            #if canImport(PersonaScreenKit)
            if let ref { VNC.current(ref)?.video.hold("small window", false) }
            #endif
            AppModel.shared.vncPiP = nil
            // Ready again under the page when it is up (and by itself again when the app goes to the background); else
            // the connection goes.
            if let ref, AppModel.shared.screenOpen?.machine == ref {
                controller?.canStartPictureInPictureAutomaticallyFromInline = true
                return
            }
            end()
        }
    }

    nonisolated func pictureInPictureController(_ c: AVPictureInPictureController, failedToStartPictureInPictureWithError error: Error) {
        Self.log.error("the small window failed to start: \(error.localizedDescription, privacy: .public)")
        MainActor.assumeIsolated {
            starting = false
            guard let ref else { return }
            #if canImport(PersonaScreenKit)
            VNC.current(ref)?.video.hold("small window", false)
            #endif
            fallBack(ref, from, why: String(localized: "The system didn't allow it (\(error.localizedDescription))"))
        }
    }

    // MARK: AVPictureInPictureSampleBufferPlaybackDelegate (live: no time line; paused only when it could not connect)

    nonisolated func pictureInPictureController(_ c: AVPictureInPictureController, setPlaying playing: Bool) {
        guard playing else { return }
        Task { @MainActor in self.reconnect() }
    }
    nonisolated func pictureInPictureControllerTimeRangeForPlayback(_ c: AVPictureInPictureController) -> CMTimeRange {
        CMTimeRange(start: .negativeInfinity, duration: .positiveInfinity)
    }
    nonisolated func pictureInPictureControllerIsPlaybackPaused(_ c: AVPictureInPictureController) -> Bool { paused.withLock { $0 } }
    nonisolated func pictureInPictureController(_ c: AVPictureInPictureController, didTransitionToRenderSize size: CMVideoDimensions) {}
    nonisolated func pictureInPictureController(_ c: AVPictureInPictureController, skipByInterval interval: CMTime,
                                                completion: @escaping () -> Void) { completion() }
}

#elseif os(visionOS)
/// visionOS has no picture in picture for a sample-buffer layer: the small window is not offered there
/// (`supported` is false); the full page shows the screen.
@MainActor final class VNCPiP: NSObject {
    static let shared = VNCPiP()
    nonisolated static let log = Logger(subsystem: "persona", category: "vnc")
    private(set) var ref: MachineRef?

    static var supported: Bool { false }
    var active: Bool { false }

    func start(_ ref: MachineRef, from: SessionKey? = nil) {}
    func restore() {}
    func handOver() {}
    func stop() {}
}
#else
/// A Mac's VNC in a small window that floats over the other windows (#1343): it only shows; a click on it opens the
/// full window; closing it ends the connection unless that window has it.
@MainActor final class VNCPiP: NSObject, NSWindowDelegate {
    static let shared = VNCPiP()
    nonisolated static let log = Logger(subsystem: "persona", category: "vnc")
    private(set) var ref: MachineRef?
    private var from: SessionKey?
    private var panel: NSPanel?

    static var supported: Bool { true }
    var active: Bool { panel != nil }

    func start(_ ref: MachineRef, from: SessionKey? = nil) {
        if self.ref == ref, panel != nil { panel?.orderFront(nil); return }
        stop()
        self.from = from
        let session = VNC.hold(ref)
        let p = NSPanel(contentRect: NSRect(x: 0, y: 0, width: 480, height: 300),
                        styleMask: [.titled, .closable, .resizable, .utilityWindow, .nonactivatingPanel], backing: .buffered, defer: false)
        p.title = "VNC · " + (AppModel.shared.machine(ref)?.name ?? ref.id)
        p.level = .floating
        p.isFloatingPanel = true
        p.hidesOnDeactivate = false
        p.isReleasedWhenClosed = false
        p.collectionBehavior = [.canJoinAllSpaces, .fullScreenAuxiliary]
        p.contentView = NSHostingView(rootView: VNCScreen(session: session, onClick: { [weak self] in self?.restore() }))
        p.delegate = self
        if let screen = NSScreen.main?.visibleFrame {
            p.setFrameOrigin(NSPoint(x: screen.maxX - p.frame.width - Theme.Space.xl, y: screen.maxY - p.frame.height - Theme.Space.xl))
        }
        p.orderFront(nil)
        self.ref = ref
        panel = p
        AppModel.shared.vncPiP = ref
    }

    /// The full window, then the small one goes.
    func restore() {
        guard let ref else { return }
        AppModel.shared.openScreen(ref, from: from)
    }

    /// The full window took it.
    func handOver() { stop() }

    func stop() {
        let p = panel
        panel = nil
        p?.close()
        if let ref { VNC.release(ref) }
        ref = nil
        AppModel.shared.vncPiP = nil
    }

    func windowWillClose(_ notification: Notification) {
        guard panel != nil else { return }
        panel = nil
        if let ref { VNC.release(ref) }
        ref = nil
        AppModel.shared.vncPiP = nil
    }
}
#endif

#if os(iOS) || os(visionOS)
/// What the small window did, step by step (#1839): the last few dozen lines, for Picture in Picture Diagnostics on the page and the
/// runtime CLI's state; each also goes to the system log (category vnc). No names or passwords.
@MainActor enum VNCPiPLog {
    private(set) static var lines: [String] = []
    static var last: String? { lines.last.map { String($0.drop { $0 != " " }.dropFirst()) } }

    static func yes(_ on: Bool) -> String { on ? "是" : "否" } // i18n-ignore

    static func add(_ line: String) {
        lines.append(Date.now.formatted(.dateTime.hour(.twoDigits(amPM: .omitted)).minute(.twoDigits).second(.twoDigits)) + " " + line)
        if lines.count > 60 { lines.removeFirst(lines.count - 60) }
        VNCPiP.log.notice("pip: \(line, privacy: .public)")
    }
}
#endif
