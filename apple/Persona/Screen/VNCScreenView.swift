import AVFoundation
import SwiftUI

/// How touches on the Mac's screen work (#1343), kept per Mac on this device: Mouse Mode (the default) points where
/// the finger is, Trackpad Mode moves the pointer like a trackpad.
enum VNCMode: String, CaseIterable {
    case mouse, trackpad

    private static func key(_ ref: MachineRef) -> String { "vnc.mode." + ScreenCredentials.key(ref) }
    static func of(_ ref: MachineRef) -> VNCMode { VNCMode(rawValue: UserDefaults.standard.string(forKey: key(ref)) ?? "") ?? .mouse }
    static func set(_ mode: VNCMode, for ref: MachineRef) { UserDefaults.standard.set(mode.rawValue, forKey: key(ref)) }
}

#if os(iOS) || os(visionOS)
/// The Mac's screen in a layer, aspect-fit and zoomable, its pointer drawn over it, and, when interactive, touches turned
/// into the Mac's pointer as the mode says:
/// - Mouse Mode: a tap clicks there; one finger drags; two scroll (or, zoomed in, move the picture); two tapping or one
///   held down is the right button; pinching zooms. A two-finger touch is a scroll or a pinch, not both (VNCTwoFingers).
/// - Trackpad Mode: one finger moves the pointer by as much; a tap clicks where it is; a tap then a drag drags; two only
///   scroll, never zoom (#6196: pinching zoomed by mistake while scrolling). Zoomed in from ⋯, the picture follows the
///   pointer.
/// Two fingers scroll as VNCWheel says: slowly the Mac's content follows them (a notch a Mac point), quicker drags go
/// further, the notches go out a display frame at a time, and a flick coasts.
/// With `region` set (one of the Mac's displays, #80) only that part of the framebuffer is shown, zoomed and touched;
/// what is sent still carries whole-framebuffer pixels.
final class VNCScreenUIView: UIView, UIGestureRecognizerDelegate {
    let display = AVSampleBufferDisplayLayer()
    /// The video channel's picture (#3954), over RFB's on its display while it shows.
    let video = AVSampleBufferDisplayLayer()
    /// This view shows the video channel (the page's; the small window's source shows RFB's picture only).
    var showsVideo = false
    /// Holds the picture and cuts it to the part shown.
    private let clip = CALayer()
    private let pointerLayer = CALayer()
    weak var session: RFBSession?
    var mode: VNCMode = .mouse { didSet { if mode != oldValue { enable() } } }
    var interactive = false { didSet { if interactive != oldValue { enable() } } }
    /// Zoomed in or back to fitted, for ⋯'s Zoom In / Zoom to Fit (after the change, never during the page's update).
    var onZoom: (Bool) -> Void = { _ in }
    /// Where the Mac's whole picture is drawn, in screen points, as it moves (the page's, for the small window's
    /// transition, #6333); nil: not reported.
    var onPicture: ((CGRect) -> Void)?
    private var reported: CGRect?
    /// The part of the framebuffer shown, in pixels; nil: all of it. A new one starts unzoomed, with the pointer on it
    /// (moved to its middle when it was elsewhere): Trackpad Mode clicks and drags where the pointer is.
    var region: CGRect? {
        didSet {
            guard region != oldValue else { return }
            shown = nil
            stopCoast()
            if let s = session, s.phase == .live, !visible.contains(s.pointer) { s.move(to: CGPoint(x: visible.midX, y: visible.midY)) }
        }
    }
    private var gestures: [UIGestureRecognizer] = []
    private var pinch: UIPinchGestureRecognizer?
    /// Where the Mac's screen is drawn, in this view; nil: fitted (not zoomed).
    private var shown: CGRect? {
        didSet {
            guard (shown == nil) != (oldValue == nil) else { return }
            let now = shown != nil
            DispatchQueue.main.async { [weak self] in self?.onZoom(now) }
        }
    }
    /// The size last laid out: a zoomed-in picture keeps its zoom and middle as the phone turns.
    private var laidOut = CGSize.zero
    /// What the two fingers down are doing in Mouse Mode: scrolling or pinching.
    private var two = VNCTwoFingers()
    /// Two fingers turned into the wheel, and where it goes (a coast keeps going there after they let go); its notches
    /// go out a display frame at a time while it has any to send or coasts.
    private var wheel = VNCWheel()
    private var wheelAt = CGPoint.zero
    private var wheelFrames: CADisplayLink?
    /// Framebuffer pixels a point of the Mac's screen, read as two fingers go down (a notch scrolls a Mac point), and
    /// Settings' Scroll Speed then.
    private var macScale: CGFloat = 1
    private var scrollSpeed: CGFloat = 1
    private var lastTap = Date.distantPast
    private var dragging = false
    private var lastPan = CGPoint.zero

    override init(frame: CGRect) {
        super.init(frame: frame)
        backgroundColor = .black
        clipsToBounds = true
        display.videoGravity = .resize
        clip.masksToBounds = true
        clip.addSublayer(display)
        video.videoGravity = .resizeAspect
        video.isHidden = true
        clip.addSublayer(video)
        layer.addSublayer(clip)
        pointerLayer.contentsGravity = .resizeAspect
        layer.addSublayer(pointerLayer)
        let tap = UITapGestureRecognizer(target: self, action: #selector(tapped(_:)))
        let twoTap = UITapGestureRecognizer(target: self, action: #selector(rightTapped(_:)))
        twoTap.numberOfTouchesRequired = 2
        let pan = UIPanGestureRecognizer(target: self, action: #selector(panned(_:)))
        pan.maximumNumberOfTouches = 1
        let twoPan = UIPanGestureRecognizer(target: self, action: #selector(twoPanned(_:)))
        twoPan.minimumNumberOfTouches = 2
        twoPan.maximumNumberOfTouches = 2
        let hold = UILongPressGestureRecognizer(target: self, action: #selector(held(_:)))
        hold.minimumPressDuration = 0.5
        let pinch = UIPinchGestureRecognizer(target: self, action: #selector(pinched(_:)))
        self.pinch = pinch
        gestures = [tap, twoTap, pan, twoPan, hold, pinch]
        for g in gestures {
            g.delegate = self
            addGestureRecognizer(g)
        }
        enable()
        isAccessibilityElement = true
        accessibilityLabel = String(localized: "Mac's screen")
        accessibilityIdentifier = "vnc-screen"
    }

    required init?(coder: NSCoder) { fatalError() }

    /// Touches while interactive; pinching only in Mouse Mode (#6196).
    private func enable() {
        for g in gestures { g.isEnabled = interactive && (g !== pinch || mode == .mouse) }
    }

    func gestureRecognizer(_ g: UIGestureRecognizer, shouldRecognizeSimultaneouslyWith other: UIGestureRecognizer) -> Bool {
        (g is UIPinchGestureRecognizer && other is UIPanGestureRecognizer) || (other is UIPinchGestureRecognizer && g is UIPanGestureRecognizer)
    }

    // MARK: Where things are

    private var screenSize: CGSize {
        let s = session?.size ?? .zero
        return s.width > 0 ? s : CGSize(width: 16, height: 10)
    }

    /// The part of the framebuffer shown: the region while it lies on the framebuffer, else all of it.
    private var visible: CGRect {
        let whole = CGRect(origin: .zero, size: screenSize)
        guard let region, !region.isEmpty, whole.contains(region) else { return whole }
        return region
    }

    private var fitted: CGRect { VNCZoom.fit(visible.size, in: bounds) }
    private var rect: CGRect { shown ?? fitted }
    private var viewport: VNCViewport { VNCViewport(visible: visible, rect: rect) }

    /// A point of this view on the Mac's screen, kept on the part shown.
    func toScreen(_ p: CGPoint) -> CGPoint { viewport.clamped(viewport.toScreen(p)) }

    func toView(_ q: CGPoint) -> CGPoint { viewport.toView(q) }

    /// Where Trackpad Mode acts: the pointer, kept on the part shown.
    private func pointer(_ s: RFBSession) -> CGPoint { viewport.clamped(s.pointer) }

    override func layoutSubviews() {
        super.layoutSubviews()
        if bounds.size != laidOut {
            if let r = shown, laidOut.width > 0 {
                shown = VNCZoom.turned(r, picture: visible.size, from: CGRect(origin: .zero, size: laidOut), to: bounds)
            }
            laidOut = bounds.size
        }
        place()
    }

    /// The picture and the pointer where they go now.
    func place() {
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        let port = viewport
        clip.frame = port.rect
        display.frame = port.framebufferFrame(screenSize)
        #if os(iOS) && canImport(PersonaScreenKit)
        if showsVideo, let v = session?.video {
            video.isHidden = !v.showing
            // RFB's picture is held (stale) while video shows: hidden, so a video of another shape leaves black, not it.
            display.isHidden = v.showing
            video.frame = port.frame(of: v.rect ?? CGRect(origin: .zero, size: screenSize))
        }
        #endif
        if let onPicture, let window, bounds.width > 0 {
            let whole = convert(display.frame.offsetBy(dx: port.rect.minX, dy: port.rect.minY), to: window.windowScene?.screen.coordinateSpace ?? window)
            if whole != reported {
                reported = whole
                onPicture(whole)
            }
        }
        if let s = session {
            let at = toView(s.pointer)
            // The Mac's pointer at a size that reads, with the rings it needs (VNCPointerArt, #3075); the app's own arrow
            // while it sends none (macOS's never does).
            if let art = pointerArt(s.cursor, natural: 1 / port.pixelsPerPoint) {
                pointerLayer.contents = art.image
                pointerLayer.frame = CGRect(x: at.x - art.hot.x, y: at.y - art.hot.y, width: art.size.width, height: art.size.height)
            } else {
                pointerLayer.contents = nil
            }
            pointerLayer.isHidden = s.phase != .live || !port.visible.contains(s.pointer)
            #if DEBUG
            // UI tests measure (the frame rate, the latency) and look (the pointer's size in points, #3075; the zoom and
            // where the pointer is drawn, #6196).
            accessibilityValue = String(format: "fps=%.0f;latency=%.0f;mode=%@;pointer=%.0fx%.0f;zoom=%.2f;at=%.0f,%.0f;picture=%@", // i18n-ignore
                                        s.fps, s.latency ?? -1, mode.rawValue, pointerLayer.frame.width, pointerLayer.frame.height,
                                        port.rect.width / max(fitted.width, 1), at.x, at.y, video.isHidden ? "vnc" : "video") // i18n-ignore
            #endif
        }
        CATransaction.commit()
    }

    /// The pointer as last drawn: for which cursor, at how many device pixels a pixel of it.
    private var drawnPointer: (cursor: RFBClient.Cursor, k: CGFloat, art: VNCPointerArt.Art)?

    /// The Mac's pointer as drawn, `natural` points a pixel of the picture; nil: the app's own arrow.
    private func pointerArt(_ mac: RFBClient.Cursor?, natural: CGFloat) -> VNCPointerArt.Art? {
        // The trait's, not the window's screen (which visionOS does not have); 0 only before the view is in a window.
        let scale = traitCollection.displayScale > 0 ? traitCollection.displayScale : 2
        // The app's own arrow is drawn at the screen's scale already: as it is, a pixel a device pixel.
        let c: RFBClient.Cursor, perPoint: CGFloat, least: CGFloat
        if let mac, mac.width > 0 { (c, perPoint, least) = (mac, natural, Theme.Size.pointer) } else { (c, perPoint, least) = (Self.arrow(scale), 1 / scale, 0) }
        let k = VNCPointerArt.devicePixelsPerPixel(c, natural: perPoint, minHeight: least, scale: scale)
        if let d = drawnPointer, d.k == k, d.cursor.hotX == c.hotX, d.cursor.hotY == c.hotY, d.cursor.width == c.width, d.cursor.pixels == c.pixels {
            return d.art
        }
        guard let art = VNCPointerArt.render(c, natural: perPoint, minHeight: least, scale: scale) else { return nil }
        pointerLayer.contentsScale = scale
        drawnPointer = (c, k, art)
        return art
    }

    private static var arrows: [CGFloat: RFBClient.Cursor] = [:]

    /// The app's own arrow, where the Mac's pointer is: macOS's Screen Sharing sends none (it draws its own into the
    /// picture, at the Mac's size), so this is the one that shows. Black like the Mac's, `Theme.Size.icon` points square
    /// at this scale, its tip the hot spot, as a cursor of the Mac's: drawn the same way, it gets the white ring the Mac's
    /// has. It used to come out solid black with nothing around it, lost on a dark screen (#3075): meant white, it was
    /// tinted before `applyingSymbolConfiguration`, and a symbol's `cgImage` ignores the tint anyway.
    static func arrow(_ scale: CGFloat) -> RFBClient.Cursor {
        if let a = arrows[scale] { return a }
        let side = Int((Theme.Size.icon * scale).rounded())
        var pixels = [UInt8](repeating: 0, count: side * side * 4)
        let symbol = UIImage(systemName: Theme.Icon.pointer, withConfiguration: UIImage.SymbolConfiguration(pointSize: Theme.Size.icon, weight: .bold))?
            .withTintColor(.black, renderingMode: .alwaysOriginal)
        pixels.withUnsafeMutableBytes { buf in
            guard let symbol, let ctx = CGContext(data: buf.baseAddress, width: side, height: side, bitsPerComponent: 8, bytesPerRow: side * 4,
                                                  space: CGColorSpaceCreateDeviceRGB(),
                                                  bitmapInfo: CGImageAlphaInfo.premultipliedFirst.rawValue | CGBitmapInfo.byteOrder32Little.rawValue)
            else { return }
            // UIKit's drawing, in points from the top-left: its first row the buffer's first, as a cursor's is.
            ctx.translateBy(x: 0, y: CGFloat(side))
            ctx.scaleBy(x: scale, y: -scale)
            UIGraphicsPushContext(ctx)
            symbol.draw(in: AVMakeRect(aspectRatio: symbol.size, insideRect: CGRect(x: 0, y: 0, width: Theme.Size.icon, height: Theme.Size.icon)))
            UIGraphicsPopContext()
        }
        let a = RFBClient.Cursor(width: side, height: side, hotX: side * 3 / 10, hotY: side * 3 / 20, pixels: pixels)
        arrows[scale] = a
        return a
    }

    // MARK: Touches

    @objc private func tapped(_ g: UITapGestureRecognizer) {
        guard let s = session else { return }
        switch mode {
        case .mouse: s.click(at: toScreen(g.location(in: self)))
        case .trackpad: s.click(at: pointer(s))
        }
        lastTap = Date()
    }

    @objc private func rightTapped(_ g: UITapGestureRecognizer) {
        guard let s = session else { return }
        s.click(at: mode == .mouse ? toScreen(g.location(in: self)) : pointer(s), button: 4)
    }

    @objc private func held(_ g: UILongPressGestureRecognizer) {
        guard g.state == .began, mode == .mouse, let s = session else { return }
        s.click(at: toScreen(g.location(in: self)), button: 4)
    }

    @objc private func panned(_ g: UIPanGestureRecognizer) {
        guard let s = session else { return }
        let p = g.location(in: self)
        switch (mode, g.state) {
        case (.mouse, .began):
            s.move(to: toScreen(p), buttons: 0)
            s.move(to: toScreen(p), buttons: 1)
        case (.mouse, .changed):
            s.move(to: toScreen(p), buttons: 1)
        case (.mouse, _):
            s.move(to: toScreen(p), buttons: 0)
        case (.trackpad, .began):
            lastPan = .zero
            dragging = Date().timeIntervalSince(lastTap) < 0.35 // a tap, then a drag: dragging
            if dragging { s.move(to: pointer(s), buttons: 1) }
        case (.trackpad, .changed):
            let t = g.translation(in: self)
            let port = viewport, speed = port.pixelsPerPoint * 1.4
            s.move(to: port.clamped(CGPoint(x: s.pointer.x + (t.x - lastPan.x) * speed, y: s.pointer.y + (t.y - lastPan.y) * speed)),
                   buttons: dragging ? 1 : 0)
            lastPan = t
            follow(s)
        case (.trackpad, _):
            if dragging { s.move(to: s.pointer, buttons: 0) }
            dragging = false
        }
        place()
    }

    @objc private func twoPanned(_ g: UIPanGestureRecognizer) {
        guard let s = session else { return }
        let lifted = g.state == .ended || g.state == .cancelled || g.state == .failed
        defer { if lifted { two.ended() } }
        if g.state == .began {
            wheel.began()
            macScale = VNCDisplays.pixelsPerMacPoint(AppModel.shared.screenDisplays(s.ref), framebuffer: s.size)
            scrollSpeed = VNCScrollSpeed.current.factor
        }
        // Trackpad Mode: only a scroll. Mouse Mode: a scroll or a pinch, whichever the fingers show first; until then the
        // travel stays in the translation, for the scroll.
        let meaning: VNCTwoFingers.Meaning = mode == .trackpad ? .scroll
            : lifted || g.numberOfTouches < 2 ? two.meaning : two.moved(center: g.location(in: self), spread: spread(g))
        guard meaning != .undecided else { return }
        let t = g.translation(in: self)
        if shown != nil, mode == .mouse { // zoomed in: two fingers move the picture
            var r = rect
            r.origin.x += t.x
            r.origin.y += t.y
            shown = clamp(r)
            g.setTranslation(.zero, in: self)
            place()
            return
        }
        guard meaning == .scroll else { // a pinch at the fitted size: no scroll
            g.setTranslation(.zero, in: self)
            return
        }
        // Mac points a view point at this zoom: slowly, the Mac's content moves as far as the fingers over its picture.
        let perPoint = viewport.pixelsPerPoint / macScale * scrollSpeed
        wheelAt = mode == .mouse ? toScreen(g.location(in: self)) : pointer(s)
        let velocity = g.velocity(in: self).y
        wheel.moved(t.y, velocity: velocity, perPoint: perPoint)
        g.setTranslation(.zero, in: self)
        if g.state == .ended { wheel.released(velocity: velocity, perPoint: perPoint) }
        if wheel.busy, wheelFrames == nil {
            let link = CADisplayLink(target: self, selector: #selector(wheelFrame(_:)))
            link.add(to: .main, forMode: .common)
            wheelFrames = link
        }
    }

    /// A display frame: the wheel's notches due now; none left and not coasting, the frames stop (what is left over
    /// stays for the fingers' next move).
    @objc private func wheelFrame(_ link: CADisplayLink) {
        let notches = wheel.frame(link.targetTimestamp - link.timestamp)
        if notches != 0 { session?.scroll(notches: notches, at: wheelAt) }
        if !wheel.busy {
            link.invalidate()
            wheelFrames = nil
        }
    }

    /// A touch, or the view going away, stops a coast where it is, with what it had not sent yet.
    private func stopCoast() {
        wheel.stop()
        wheelFrames?.invalidate()
        wheelFrames = nil
    }

    override func touchesBegan(_ touches: Set<UITouch>, with event: UIEvent?) {
        stopCoast()
        // The second finger down: a new two-finger touch, from where both are now.
        let down = Array(event?.touches(for: self)?.filter { $0.phase != .ended && $0.phase != .cancelled } ?? [])
        if down.count == 2 {
            let a = down[0].location(in: self), b = down[1].location(in: self)
            two.began(center: CGPoint(x: (a.x + b.x) / 2, y: (a.y + b.y) / 2), spread: hypot(a.x - b.x, a.y - b.y))
        }
        super.touchesBegan(touches, with: event)
    }

    /// How far apart a gesture's first two fingers are.
    private func spread(_ g: UIGestureRecognizer) -> CGFloat {
        guard g.numberOfTouches >= 2 else { return 0 }
        let a = g.location(ofTouch: 0, in: self), b = g.location(ofTouch: 1, in: self)
        return hypot(a.x - b.x, a.y - b.y)
    }

    override func willMove(toWindow newWindow: UIWindow?) {
        super.willMove(toWindow: newWindow)
        if newWindow == nil { stopCoast() }
    }

    /// Mouse Mode only (Trackpad Mode turns it off, #6196); it zooms once the touch is a pinch (VNCTwoFingers), from
    /// where the fingers are then, so a scroll that drifts apart a little never zooms.
    @objc private func pinched(_ g: UIPinchGestureRecognizer) {
        let lifted = g.state == .ended || g.state == .cancelled || g.state == .failed
        defer { if lifted { two.ended() } }
        let meaning = lifted || g.numberOfTouches < 2 ? two.meaning : two.moved(center: g.location(in: self), spread: spread(g))
        guard meaning == .zoom, !lifted else {
            g.scale = 1
            return
        }
        let anchor = g.location(in: self)
        let r = rect, base = fitted
        let width = min(max(r.width * g.scale, base.width), base.width * VNCZoom.most)
        g.scale = 1
        guard width > base.width * 1.01 else {
            shown = nil
            place()
            return
        }
        shown = clamp(VNCZoom.zoomed(r, by: width / r.width, at: anchor))
        place()
    }

    /// A zoomed picture kept over the view (centered on an axis it is narrower on).
    private func clamp(_ r: CGRect) -> CGRect { VNCZoom.clamp(r, in: bounds.size) }

    /// ⋯ › Zoom In (#6196): twice the fitted picture, the pointer staying where it is (the middle when it is not on the
    /// part shown). The only zoom in Trackpad Mode, where the picture then follows the pointer.
    func zoomIn() {
        var anchor = CGPoint(x: bounds.midX, y: bounds.midY)
        if let s = session, visible.contains(s.pointer) { anchor = toView(s.pointer) }
        shown = clamp(VNCZoom.zoomed(fitted, by: VNCZoom.step, at: anchor))
        place()
    }

    /// ⋯ › Zoom to Fit: the whole picture again.
    func zoomToFit() {
        shown = nil
        place()
    }

    /// Zoomed in, Trackpad Mode's picture moves to keep the pointer in view.
    private func follow(_ s: RFBSession) {
        guard let r = shown, mode == .trackpad else { return }
        let moved = VNCZoom.following(r, keep: toView(s.pointer), in: bounds.size, margin: Theme.Space.xxl)
        if moved != r { shown = moved }
    }

    /// Center Pointer.
    func centerPointer() {
        guard let s = session else { return }
        s.move(to: CGPoint(x: visible.midX, y: visible.midY))
        follow(s)
        place()
    }
}

/// The Mac's screen in SwiftUI: its layer shown while this is up, touches when interactive.
struct VNCScreen: UIViewRepresentable {
    let session: RFBSession
    var mode: VNCMode = .mouse
    var interactive = false
    /// One of the Mac's displays to show alone (its frame on the framebuffer); nil: all of them.
    var region: CGRect? = nil
    /// The view, for the page's Center Pointer and Zoom In.
    var onView: (VNCScreenUIView) -> Void = { _ in }
    /// Zoomed in or back to fitted.
    var onZoom: (Bool) -> Void = { _ in }
    /// Where the whole picture is drawn, in screen points (VNCScreenUIView.onPicture).
    var onPicture: ((CGRect) -> Void)? = nil

    func makeUIView(context: Context) -> VNCScreenUIView {
        let v = VNCScreenUIView()
        v.session = session
        v.onZoom = onZoom
        v.onPicture = onPicture
        session.show(in: v.display)
        #if os(iOS) && canImport(PersonaScreenKit)
        v.showsVideo = true
        session.video.show(in: v.video)
        #endif
        onView(v)
        return v
    }

    func updateUIView(_ v: VNCScreenUIView, context: Context) {
        v.onZoom = onZoom
        v.onPicture = onPicture
        v.mode = mode
        v.interactive = interactive
        v.region = region
        _ = (session.pointer, session.size, session.cursor?.width, session.phase, session.fps) // redrawn as they change
        #if os(iOS) && canImport(PersonaScreenKit)
        _ = (session.video.showing, session.video.rect)
        #endif
        v.place()
    }

    static func dismantleUIView(_ v: VNCScreenUIView, coordinator: ()) {
        v.session?.hide(from: v.display)
        #if os(iOS) && canImport(PersonaScreenKit)
        v.session?.video.hide(from: v.video)
        #endif
    }
}
#else
/// The Mac's screen on the Mac: its layer, aspect-fit; clicks and keys go to it when interactive (a click there, a
/// drag drags, the wheel scrolls).
final class VNCScreenNSView: NSView {
    let display = AVSampleBufferDisplayLayer()
    weak var session: RFBSession?
    var interactive = false
    var onClick: (() -> Void)?

    override init(frame: NSRect) {
        super.init(frame: frame)
        wantsLayer = true
        layer?.backgroundColor = NSColor.black.cgColor
        display.videoGravity = .resizeAspect
        layer?.addSublayer(display)
        setAccessibilityIdentifier("vnc-screen")
    }

    required init?(coder: NSCoder) { fatalError() }

    override func layout() {
        super.layout()
        display.frame = bounds
    }

    private var screenSize: CGSize { session?.size ?? CGSize(width: 16, height: 10) }

    private func toScreen(_ e: NSEvent) -> CGPoint {
        let p = convert(e.locationInWindow, from: nil)
        let r = AVMakeRect(aspectRatio: screenSize, insideRect: bounds)
        return CGPoint(x: (p.x - r.minX) / r.width * screenSize.width, y: (r.maxY - p.y) / r.height * screenSize.height)
    }

    override func mouseDown(with e: NSEvent) {
        guard interactive else { onClick?(); return }
        session?.move(to: toScreen(e), buttons: 1)
    }
    override func mouseDragged(with e: NSEvent) { if interactive { session?.move(to: toScreen(e), buttons: 1) } }
    override func mouseUp(with e: NSEvent) { if interactive { session?.move(to: toScreen(e), buttons: 0) } }
    override func mouseMoved(with e: NSEvent) { if interactive { session?.move(to: toScreen(e), buttons: 0) } }
    override func rightMouseDown(with e: NSEvent) { if interactive { session?.click(at: toScreen(e), button: 4) } }
    override func scrollWheel(with e: NSEvent) {
        guard interactive, abs(e.scrollingDeltaY) > 2 else { return }
        session?.scroll(notches: e.scrollingDeltaY > 0 ? 1 : -1, at: toScreen(e))
    }
    override var acceptsFirstResponder: Bool { interactive }
    override func keyDown(with e: NSEvent) {
        guard interactive, let s = session else { return }
        if let special = VNCKeys.keysym(macKeyCode: e.keyCode) { s.press(special, with: VNCKeys.modifiers(e.modifierFlags)) }
        else if let text = e.charactersIgnoringModifiers, !text.isEmpty {
            if e.modifierFlags.intersection([.command, .control, .option]).isEmpty { s.type(e.characters ?? text) }
            else { for ch in text.unicodeScalars { s.press(RFBSession.keysym(ch), with: VNCKeys.modifiers(e.modifierFlags)) } }
        }
    }

    override func updateTrackingAreas() {
        super.updateTrackingAreas()
        trackingAreas.forEach(removeTrackingArea)
        addTrackingArea(NSTrackingArea(rect: bounds, options: [.mouseMoved, .activeInKeyWindow, .inVisibleRect], owner: self))
    }
}

struct VNCScreen: NSViewRepresentable {
    let session: RFBSession
    var mode: VNCMode = .mouse
    var interactive = false
    var onClick: (() -> Void)? = nil

    func makeNSView(context: Context) -> VNCScreenNSView {
        let v = VNCScreenNSView()
        v.session = session
        session.show(in: v.display)
        return v
    }

    func updateNSView(_ v: VNCScreenNSView, context: Context) {
        v.interactive = interactive
        v.onClick = onClick
        if interactive { DispatchQueue.main.async { v.window?.makeFirstResponder(v) } }
    }

    static func dismantleNSView(_ v: VNCScreenNSView, coordinator: ()) { v.session?.hide(from: v.display) }
}
#endif

/// Keys by name, for the shortcut row and the Mac's keyboard.
enum VNCKeys {
    static let escape: UInt32 = 0xff1b, tab: UInt32 = 0xff09, enter: UInt32 = 0xff0d, backspace: UInt32 = 0xff08
    static let left: UInt32 = 0xff51, up: UInt32 = 0xff52, right: UInt32 = 0xff53, down: UInt32 = 0xff54

    #if os(macOS)
    static func keysym(macKeyCode code: UInt16) -> UInt32? {
        switch code {
        case 53: escape
        case 48: tab
        case 36, 76: enter
        case 51: backspace
        case 123: left
        case 124: right
        case 125: down
        case 126: up
        default: nil
        }
    }

    static func modifiers(_ f: NSEvent.ModifierFlags) -> [UInt32] {
        var out: [UInt32] = []
        if f.contains(.command) { out.append(RFBSession.command) }
        if f.contains(.option) { out.append(RFBSession.option) }
        if f.contains(.control) { out.append(RFBSession.control) }
        if f.contains(.shift) { out.append(RFBSession.shift) }
        return out
    }
    #endif
}
