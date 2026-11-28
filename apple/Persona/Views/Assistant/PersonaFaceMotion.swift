import QuartzCore
import SwiftUI
import UIKit

/// Mochi's moving states, drawn once and animated in the render server. AssistantAvatar keeps the original SwiftUI
/// renderer for every still case and owns this host's eligibility through VisibleWork.
struct PersonaFaceMotion: UIViewRepresentable {
    var face: PersonaFace
    /// Tests can draw a fixed moving pose; the app always enters with true and time zero.
    var animating = true
    var time: TimeInterval = 0

    func makeUIView(context: Context) -> PersonaFaceMotionView { PersonaFaceMotionView() }

    func updateUIView(_ view: PersonaFaceMotionView, context: Context) {
        view.configure(face: face, scheme: context.environment.colorScheme, displayScale: context.environment.displayScale,
                       animating: animating, time: time)
    }

    static func dismantleUIView(_ view: PersonaFaceMotionView, coordinator: ()) { view.stop() }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: PersonaFaceMotionView, context: Context) -> CGSize? {
        CGSize(width: face.size, height: face.size)
    }
}

/// The content of one static host, always drawn at phase zero. UIKit applies the current fixed or animated pose to its
/// layer, so fixed-time tests exercise the same decomposition as production.
struct PersonaMotionContent: Equatable {
    var size: CGFloat
    var state: PersonaState
    var time: TimeInterval
    var scheme: ColorScheme
    var displayScale: CGFloat
    var mouth: Double?
    var eyesShut: Bool

    var compact: Bool { size < Mochi.compactBelow }
    var ink: MochiInk { MochiInk(dark: scheme == .dark, offline: state == .offline) }
    var face: PersonaFace {
        PersonaFace(size: size, design: .mochi, state: state, time: time, moving: true, blink: nil,
                    mouth: mouth, eyesShut: eyesShut)
    }
}

enum PersonaMotionPiece {
    case arc, body, workingBody(Int), workingEyes
    case dot(Int), grayDot(Int), ring
}

/// A full-size SwiftUI piece. The frame keeps the original layout and offsets while UIKit owns only the host's
/// transform or opacity; no SwiftUI clock runs here.
struct PersonaMotionPieceView: View {
    var content: PersonaMotionContent
    var piece: PersonaMotionPiece
    var eyeView: UIView?

    private var side: CGFloat {
        if case .dot(_) = piece { return content.face.bubbleDotSide }
        if case .grayDot(_) = piece { return content.face.bubbleDotSide }
        return content.size
    }

    var body: some View {
        drawing
            .frame(width: side, height: side)
            .environment(\.colorScheme, content.scheme)
            .environment(\.displayScale, content.displayScale)
    }

    @ViewBuilder private var drawing: some View {
        let face = content.face
        switch piece {
        case .arc:
            if content.state == .working { face.mochiArc(0) }
        case .body:
            originalAssemblyPose(ZStack {
                face.mochiBody(ink: content.ink, compact: content.compact,
                               lines: content.state == .working ? [] : face.mochiLines(content.time))
                if content.state != .working {
                    if content.state != .offline && !content.eyesShut {
                        face.mochiEyes(content.time, ink: content.ink, compact: content.compact)
                    }
                    if content.eyesShut && content.state != .offline {
                        Canvas { ctx, box in Mochi.closedEyes(&ctx, side: box.width, ink: content.ink, compact: content.compact) }
                    }
                    if let mouth = content.mouth, content.state != .offline {
                        Canvas { ctx, box in Mochi.mouth(&ctx, side: box.width, ink: content.ink, compact: content.compact, open: mouth) }
                    }
                }
            })
        case .workingBody(let phase):
            if content.state == .working {
                originalAssemblyPose(ZStack {
                    face.mochiBody(ink: content.ink, compact: content.compact,
                                   lines: face.mochiLines(Double(phase) * 0.25))
                    if !content.eyesShut, let eyeView { PersonaHostedPiece(view: eyeView, size: content.size) }
                    if content.eyesShut {
                        Canvas { ctx, box in
                            Mochi.closedEyes(&ctx, side: box.width, ink: content.ink, compact: content.compact)
                        }
                    }
                    if let mouth = content.mouth {
                        Canvas { ctx, box in
                            Mochi.mouth(&ctx, side: box.width, ink: content.ink, compact: content.compact, open: mouth)
                        }
                    }
                })
            }
        case .workingEyes:
            if content.state == .working && !content.eyesShut {
                face.mochiEyes(content.time, ink: content.ink, compact: content.compact)
            }
        case .dot(_), .grayDot(_):
            if content.state == .thinking {
                let gray = if case .grayDot(_) = piece { true } else { false }
                Circle().fill(gray ? Color(white: 0.7) : Mochi.cheekColor)
            }
        case .ring:
            face.needsYouRing(content.time)
        }
    }

    /// PersonaFace applies these modifiers to the whole body assembly even when their phase-zero values are identity.
    /// Keeping them also preserves SwiftUI's fractional-size rasterization; UIKit supplies the moving pose outside them.
    private func originalAssemblyPose<V: View>(_ view: V) -> some View {
        let face = content.face
        return view
            .rotationEffect(.degrees(face.mochiRock(content.time)), anchor: UnitPoint(x: Mochi.pivot.x, y: Mochi.pivot.y))
            .offset(y: face.mochiBob(content.time))
    }
}

/// A UIKit piece in the original SwiftUI composition. The representable does not own any animation or visibility policy.
private struct PersonaHostedPiece: UIViewRepresentable {
    var view: UIView
    var size: CGFloat

    func makeUIView(context: Context) -> UIView { view }
    func updateUIView(_ view: UIView, context: Context) {}
    func sizeThatFits(_ proposal: ProposedViewSize, uiView: UIView, context: Context) -> CGSize? {
        CGSize(width: size, height: size)
    }
}

/// SwiftUI can assign the embedded view's bounds after its parent's layout pass.
private final class PersonaMotionContainer: UIView {
    var onBoundsChange: (() -> Void)?
    private var lastBounds = CGRect.null

    override func layoutSubviews() {
        super.layoutSubviews()
        guard bounds != lastBounds else { return }
        lastBounds = bounds
        onBoundsChange?()
    }
}

/// The original disc, ZStack order, circular clip and ring overlay stay in one static SwiftUI host. Only the pieces
/// whose layers move are UIKit children inside fixed-size containers; SwiftUI never lays out a transformed view.
private struct PersonaMotionComposition: View {
    var content: PersonaMotionContent
    var arcView: UIView
    var bodyView: UIView
    var dots: [PersonaPieceHost]
    var grayDots: [PersonaPieceHost]
    var ringView: PersonaPieceHost

    var body: some View {
        ZStack {
            Circle().fill(LinearGradient(colors: content.ink.disc, startPoint: .top, endPoint: .bottom))
            if content.state == .working { PersonaHostedPiece(view: arcView, size: content.size) }
            PersonaHostedPiece(view: bodyView, size: content.size)
            if content.state == .thinking {
                content.face.bubbleDots { index in
                    ZStack {
                        PersonaHostedPiece(view: grayDots[index], size: content.face.bubbleDotSide)
                        PersonaHostedPiece(view: dots[index], size: content.face.bubbleDotSide)
                    }
                }
                .background(.white, in: .capsule)
                .shadow(color: .black.opacity(0.15), radius: content.size * 0.02, y: content.size * 0.01)
                .offset(x: content.size * 0.25, y: -content.size * 0.29)
            }
        }
        .frame(width: content.size, height: content.size)
        .clipShape(.circle)
        .overlay {
            if content.state == .needsYou { PersonaHostedPiece(view: ringView, size: content.size) }
        }
        .environment(\.colorScheme, content.scheme)
        .environment(\.displayScale, content.displayScale)
    }
}

/// Retains one static SwiftUI host. Identical inputs do not assign rootView, so expression or appearance updates in a
/// sibling cannot redraw this piece or restart its layer animation.
final class PersonaPieceHost: UIView {
    private let piece: PersonaMotionPiece
    private let host: UIHostingController<PersonaMotionPieceView>
    private let eyeView: UIView?
    private var shown: PersonaMotionContent?

    init(_ piece: PersonaMotionPiece, eyeView: UIView? = nil) {
        self.piece = piece
        self.eyeView = eyeView
        let initial = PersonaMotionContent(size: 0, state: .idle, time: 0, scheme: .light, displayScale: 1,
                                           mouth: nil, eyesShut: false)
        host = UIHostingController(rootView: PersonaMotionPieceView(content: initial, piece: piece, eyeView: eyeView))
        super.init(frame: .zero)
        isOpaque = false
        host.safeAreaRegions = []
        host.sizingOptions = []
        host.view.backgroundColor = .clear
        host.view.isOpaque = false
        host.view.isUserInteractionEnabled = false
        host.view.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        isUserInteractionEnabled = false
        accessibilityElementsHidden = true
        addSubview(host.view)
    }

    @available(*, unavailable) required init?(coder: NSCoder) { fatalError() }

    override func layoutSubviews() {
        super.layoutSubviews()
        if let shown { updateScale(shown.displayScale) }
        host.view.frame = bounds
        host.view.layoutIfNeeded()
    }

    func updateScale(_ scale: CGFloat) {
        contentScaleFactor = scale
        host.view.contentScaleFactor = scale
        layer.contentsScale = scale
        host.view.layer.contentsScale = scale
    }

    private func needsRedraw(_ content: PersonaMotionContent) -> Bool {
        guard let old = shown else { return true }
        if old.size != content.size || old.state != content.state || old.scheme != content.scheme ||
            old.displayScale != content.displayScale { return true }
        switch piece {
        case .body: return content.state != .working && (old.eyesShut != content.eyesShut || old.mouth != content.mouth)
        case .workingBody: return old.eyesShut != content.eyesShut || old.mouth != content.mouth
        case .workingEyes: return old.eyesShut != content.eyesShut
        default: return false
        }
    }

    func updateContent(_ content: PersonaMotionContent) {
        host.overrideUserInterfaceStyle = content.scheme == .dark ? .dark : .light
        updateScale(content.displayScale)
        guard needsRedraw(content) else { return }
        shown = content
        host.rootView = PersonaMotionPieceView(content: content, piece: piece, eyeView: eyeView)
    }
}

/// One representable owns the whole piece tree. The clipped content keeps disc → arc → body → bubble order; the ring
/// remains above that clip, exactly as in PersonaFace.
final class PersonaFaceMotionView: UIView {
    private var host: UIHostingController<PersonaMotionComposition>!
    private let arcContainer = PersonaMotionContainer()
    let arc = PersonaPieceHost(.arc)
    private let bodyContainer = PersonaMotionContainer()
    let body = UIView()
    private let bodyCanvas = PersonaPieceHost(.body)
    let workingBodies: [PersonaPieceHost]
    let workingEyes: [PersonaPieceHost]
    private let eyeContainers: [PersonaMotionContainer]
    var eyes: PersonaPieceHost { workingEyes[0] }
    let dots = (0..<3).map { PersonaPieceHost(.dot($0)) }
    let grayDots = (0..<3).map { PersonaPieceHost(.grayDot($0)) }
    let ring = PersonaPieceHost(.ring)

    private var content: PersonaMotionContent?
    private var composition: PersonaMotionContent?
    private var poseTime: TimeInterval = 0
    private var requested = false
    private var dismantled = false
    private var installed: Signature?

    private struct Signature: Equatable {
        var state: PersonaState
        var size: CGFloat
    }

    override init(frame: CGRect) {
        let eyes = (0..<3).map { _ in PersonaPieceHost(.workingEyes) }
        let containers = (0..<3).map { _ in PersonaMotionContainer() }
        for (container, eye) in zip(containers, eyes) {
            container.backgroundColor = .clear
            container.isOpaque = false
            container.addSubview(eye)
        }
        workingEyes = eyes
        eyeContainers = containers
        workingBodies = (0..<3).map { PersonaPieceHost(.workingBody($0), eyeView: containers[$0]) }
        super.init(frame: frame)
        let initial = PersonaMotionContent(size: 0, state: .idle, time: 0, scheme: .light, displayScale: 1,
                                           mouth: nil, eyesShut: false)
        host = UIHostingController(rootView: PersonaMotionComposition(content: initial, arcView: arcContainer, bodyView: bodyContainer,
                                                                       dots: dots, grayDots: grayDots, ringView: ring))
        isOpaque = false
        isUserInteractionEnabled = false
        accessibilityElementsHidden = true
        backgroundColor = .clear
        host.safeAreaRegions = []
        host.sizingOptions = []
        host.view.backgroundColor = .clear
        host.view.isOpaque = false
        host.view.isUserInteractionEnabled = false
        host.view.autoresizingMask = [.flexibleWidth, .flexibleHeight]
        arcContainer.backgroundColor = .clear
        arcContainer.isOpaque = false
        bodyContainer.backgroundColor = .clear
        bodyContainer.isOpaque = false
        arcContainer.addSubview(arc)
        bodyContainer.addSubview(body)
        body.backgroundColor = .clear
        body.isOpaque = false
        addSubview(host.view)
        body.addSubview(bodyCanvas)
        for phase in workingBodies { body.addSubview(phase) }
        for container in [arcContainer, bodyContainer] + eyeContainers {
            container.onBoundsChange = { [weak self] in self?.setNeedsLayout() }
        }
    }

    @available(*, unavailable) required init?(coder: NSCoder) { fatalError() }

    override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? { nil }

    func configure(face: PersonaFace, scheme: ColorScheme, displayScale: CGFloat, animating: Bool, time: TimeInterval) {
        let next = PersonaMotionContent(size: face.size, state: face.state, time: 0,
                                        scheme: scheme, displayScale: displayScale, mouth: face.mouth, eyesShut: face.eyesShut)
        poseTime = animating ? 0 : time
        requested = animating
        dismantled = false
        host.overrideUserInterfaceStyle = scheme == .dark ? .dark : .light
        contentScaleFactor = displayScale
        host.view.contentScaleFactor = displayScale
        layer.contentsScale = displayScale
        host.view.layer.contentsScale = displayScale
        if composition == nil || composition?.size != next.size || composition?.state != next.state ||
            composition?.scheme != next.scheme || composition?.displayScale != next.displayScale {
            composition = next
            host.rootView = PersonaMotionComposition(content: next, arcView: arcContainer, bodyView: bodyContainer,
                                                      dots: dots, grayDots: grayDots, ringView: ring)
        }
        if content != next {
            content = next
            for host in allHosts { host.updateContent(next) }
            updateVisibility()
        }
        setNeedsLayout()
        layoutIfNeeded()
    }

    private var allHosts: [PersonaPieceHost] {
        [arc, bodyCanvas, ring] + workingBodies + workingEyes + dots + grayDots
    }

    private func updateVisibility() {
        guard let content else { return }
        arc.isHidden = content.state != .working
        ring.isHidden = content.state != .needsYou
        for eye in workingEyes { eye.isHidden = content.state != .working || content.eyesShut }
        bodyCanvas.isHidden = content.state == .working
        for phase in workingBodies { phase.isHidden = content.state != .working }
    }

    override func layoutSubviews() {
        super.layoutSubviews()
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        layoutPieces()
        applyStaticPose()
        CATransaction.commit()
        updateAnimations()
    }

    private func layoutPieces() {
        host.view.frame = bounds
        guard let content else { return }
        host.view.contentScaleFactor = content.displayScale
        host.view.layoutIfNeeded()
        host.view.contentScaleFactor = content.displayScale
        host.view.layer.contentsScale = content.displayScale
        for piece in allHosts { piece.updateScale(content.displayScale) }
        // Start at the requested size before SwiftUI supplies the wrapper's actual geometry.
        let square = CGRect(x: 0, y: 0, width: content.size, height: content.size)
        setFullFrame(arc, in: arcContainer.bounds.isEmpty ? square : arcContainer.bounds)
        let anchor = content.state == .needsYou ? Mochi.pivot : CGPoint(x: 0.5, y: 0.5)
        setFullFrame(body, in: bodyContainer.bounds.isEmpty ? square : bodyContainer.bounds, anchor: anchor)
        for host in [bodyCanvas] + workingBodies { setFullFrame(host, in: body.bounds) }
        for phase in workingBodies { phase.layoutIfNeeded() }
        for (eye, container) in zip(workingEyes, eyeContainers) {
            setFullFrame(eye, in: container.bounds.isEmpty ? square : container.bounds)
        }
    }

    private func setFullFrame(_ view: UIView, in rect: CGRect, anchor: CGPoint = CGPoint(x: 0.5, y: 0.5)) {
        view.layer.bounds = CGRect(origin: .zero, size: rect.size)
        view.layer.anchorPoint = anchor
        view.layer.position = CGPoint(x: rect.minX + rect.width * anchor.x, y: rect.minY + rect.height * anchor.y)
    }

    /// The model layers describe the current fixed pose. While moving, poseTime is zero and the animations are relative
    /// to that base; fixed-time tests set these same transforms and opacities without installing animations.
    private func applyStaticPose() {
        guard let content else { return }
        let face = content.face, t = poseTime
        switch content.state {
        case .needsYou:
            body.layer.transform = CATransform3DMakeRotation(face.mochiRock(t) * .pi / 180, 0, 0, 1)
        case .working:
            body.layer.transform = CATransform3DMakeTranslation(0, face.mochiBob(t), 0)
        default:
            body.layer.transform = CATransform3DIdentity
        }
        let look = face.mochiEyeLook(t), baseLook = face.mochiEyeLook(0)
        for eye in workingEyes {
            eye.layer.transform = CATransform3DMakeTranslation(look.width - baseLook.width, look.height - baseLook.height, 0)
        }
        let arcDegrees = face.mochiArcDegrees(t) - face.mochiArcDegrees(0)
        arc.layer.transform = CATransform3DMakeRotation(arcDegrees * .pi / 180, 0, 0, 1)
        ring.layer.opacity = Float(face.needsYouPulse(t))
        if content.state == .working {
            let lit = face.workingLineLit(t)
            for (index, phase) in workingBodies.enumerated() { phase.layer.opacity = lit == index ? 1 : 0 }
        }
        if content.state == .thinking {
            let lit = face.thinkingLit(t)
            for (index, dot) in dots.enumerated() { dot.layer.opacity = lit == index || lit == 3 ? 1 : 0 }
            for (index, dot) in grayDots.enumerated() { dot.layer.opacity = lit == index || lit == 3 ? 0 : 1 }
        }
    }

    override func didMoveToWindow() {
        super.didMoveToWindow()
        updateAnimations()
    }

    private func updateAnimations() {
        guard let content, requested, !dismantled, window != nil, bounds.width > 0, bounds.height > 0 else {
            removeAnimations()
            return
        }
        let signature = Signature(state: content.state, size: content.size)
        if installed != signature { removeAnimations() }
        let wallTime = Date.timeIntervalSinceReferenceDate
        let face = content.face
        func ensure(_ key: String, on layer: CALayer, make: () -> CAAnimation) {
            guard layer.animation(forKey: key) == nil else { return }
            add(make(), to: layer, key: key)
        }
        switch content.state {
        case .thinking:
            for (index, dot) in dots.enumerated() {
                let key = PersonaMotionAnimations.dotKey(index)
                ensure(key, on: dot.layer) { PersonaMotionAnimations.dot(index, face: face, wallTime: wallTime) }
            }
            for (index, dot) in grayDots.enumerated() {
                let key = PersonaMotionAnimations.grayDotKey(index)
                ensure(key, on: dot.layer) { PersonaMotionAnimations.grayDot(index, face: face, wallTime: wallTime) }
            }
        case .working:
            ensure(PersonaMotionAnimations.arcKey, on: arc.layer) {
                PersonaMotionAnimations.arc(face: face, wallTime: wallTime)
            }
            ensure(PersonaMotionAnimations.bobKey, on: body.layer) {
                PersonaMotionAnimations.bob(face: face, wallTime: wallTime)
            }
            if !content.eyesShut {
                for (index, eye) in workingEyes.enumerated() where !content.compact || index == 0 {
                    ensure(PersonaMotionAnimations.eyesKey, on: eye.layer) {
                        PersonaMotionAnimations.eyes(face: face, wallTime: wallTime)
                    }
                }
            }
            if !content.compact {
                for (index, phase) in workingBodies.enumerated() {
                    let key = PersonaMotionAnimations.lineKey(index)
                    ensure(key, on: phase.layer) { PersonaMotionAnimations.line(index, face: face, wallTime: wallTime) }
                }
            }
        case .needsYou:
            ensure(PersonaMotionAnimations.rockKey, on: body.layer) {
                PersonaMotionAnimations.rock(face: face, wallTime: wallTime)
            }
            ensure(PersonaMotionAnimations.ringKey, on: ring.layer) {
                PersonaMotionAnimations.ring(face: face, wallTime: wallTime)
            }
        case .idle, .offline:
            break
        }
        for (index, eye) in workingEyes.enumerated()
        where content.state != .working || content.eyesShut || (content.compact && index > 0) {
            eye.layer.removeAnimation(forKey: PersonaMotionAnimations.eyesKey)
        }
        if content.state != .working || content.compact {
            for (index, phase) in workingBodies.enumerated() {
                phase.layer.removeAnimation(forKey: PersonaMotionAnimations.lineKey(index))
            }
        }
        installed = signature
    }

    private func add(_ animation: CAAnimation, to layer: CALayer, key: String) {
        let now = layer.convertTime(CACurrentMediaTime(), from: nil)
        animation.beginTime += now
        layer.add(animation, forKey: key)
    }

    private func removeAnimations() {
        installed = nil
        let layers = [body.layer, arc.layer, ring.layer] + workingBodies.map(\.layer) + workingEyes.map(\.layer) +
            dots.map(\.layer) + grayDots.map(\.layer)
        for layer in layers { layer.removeAllAnimations() }
    }

    func stop() {
        requested = false
        dismantled = true
        removeAnimations()
    }
}

/// Exact-function animation factories, kept separate from the host for deterministic tests. The single finish helper
/// owns repeat registration, wall phase and the advisory 15 fps cadence for every loop.
enum PersonaMotionAnimations {
    static let arcKey = "persona.arc", bobKey = "persona.bob", eyesKey = "persona.eyes"
    static let rockKey = "persona.rock", ringKey = "persona.ring"
    static func lineKey(_ index: Int) -> String { "persona.line.\(index)" }
    static func dotKey(_ index: Int) -> String { "persona.dot.\(index)" }
    static func grayDotKey(_ index: Int) -> String { "persona.dot.gray.\(index)" }

    static let arcPeriod: TimeInterval = 1.6
    static let eyesPeriod = 2 * Double.pi / 1.8
    static let bobPeriod = Double.pi / 5
    static let rockPeriod = 2 * Double.pi / 7
    static let ringPeriod = 2 * Double.pi / 3
    static let linePeriod: TimeInterval = 0.75
    static let dotPeriod: TimeInterval = 1.6

    static func arc(face: PersonaFace, wallTime: TimeInterval) -> CABasicAnimation {
        let animation = CABasicAnimation(keyPath: "transform.rotation.z")
        animation.fromValue = 0
        animation.toValue = 2 * Double.pi
        animation.duration = arcPeriod
        animation.timingFunction = CAMediaTimingFunction(name: .linear)
        return finish(animation, period: arcPeriod, wallTime: wallTime)
    }

    static func eyes(face: PersonaFace, wallTime: TimeInterval) -> CAKeyframeAnimation {
        let base = face.mochiEyeLook(0).width
        return sampled(keyPath: "transform.translation.x", period: eyesPeriod, wallTime: wallTime) {
            Double(face.mochiEyeLook($0).width - base)
        }
    }

    static func bob(face: PersonaFace, wallTime: TimeInterval) -> CAKeyframeAnimation {
        sampled(keyPath: "transform.translation.y", period: bobPeriod, wallTime: wallTime) { Double(face.mochiBob($0)) }
    }

    static func rock(face: PersonaFace, wallTime: TimeInterval) -> CAKeyframeAnimation {
        sampled(keyPath: "transform.rotation.z", period: rockPeriod, wallTime: wallTime) { face.mochiRock($0) * .pi / 180 }
    }

    static func ring(face: PersonaFace, wallTime: TimeInterval) -> CAKeyframeAnimation {
        sampled(keyPath: "opacity", period: ringPeriod, wallTime: wallTime) { face.needsYouPulse($0) }
    }

    static func line(_ index: Int, face: PersonaFace, wallTime: TimeInterval) -> CAKeyframeAnimation {
        discrete(phases: [0.0, 1.0 / 3, 2.0 / 3, 1.0], period: linePeriod, wallTime: wallTime) {
            NSNumber(value: face.workingLineLit($0 * linePeriod) == index ? 1 : 0)
        }
    }

    static func dot(_ index: Int, face: PersonaFace, wallTime: TimeInterval) -> CAKeyframeAnimation {
        discrete(phases: [0.0, 0.25, 0.5, 0.75, 1.0], period: dotPeriod, wallTime: wallTime) {
            let lit = face.thinkingLit($0 * dotPeriod)
            return NSNumber(value: lit == index || lit == 3 ? 1 : 0)
        }
    }

    static func grayDot(_ index: Int, face: PersonaFace, wallTime: TimeInterval) -> CAKeyframeAnimation {
        discrete(phases: [0.0, 0.25, 0.5, 0.75, 1.0], period: dotPeriod, wallTime: wallTime) {
            let lit = face.thinkingLit($0 * dotPeriod)
            return NSNumber(value: lit == index || lit == 3 ? 0 : 1)
        }
    }

    private static func discrete(phases: [Double], period: TimeInterval, wallTime: TimeInterval,
                                 value: (Double) -> NSNumber) -> CAKeyframeAnimation {
        let animation = CAKeyframeAnimation(keyPath: "opacity")
        animation.values = phases.map(value)
        animation.keyTimes = phases.map { NSNumber(value: $0) }
        animation.calculationMode = .discrete
        animation.duration = period
        return finish(animation, period: period, wallTime: wallTime)
    }

    private static func sampled(keyPath: String, period: TimeInterval, wallTime: TimeInterval,
                                value: @escaping (TimeInterval) -> Double) -> CAKeyframeAnimation {
        let steps = max(2, Int(ceil(period / Theme.Motion.personaFrameInterval)))
        var times = Set((0...steps).map { period * Double($0) / Double(steps) })
        for quarter in [0.0, 0.25, 0.5, 0.75, 1.0] { times.insert(period * quarter) }
        let ordered = times.sorted()
        let animation = CAKeyframeAnimation(keyPath: keyPath)
        animation.values = ordered.map { NSNumber(value: value($0)) }
        animation.keyTimes = ordered.map { NSNumber(value: $0 / period) }
        animation.calculationMode = .linear
        animation.duration = period
        return finish(animation, period: period, wallTime: wallTime)
    }

    private static func finish<A: CAAnimation>(_ animation: A, period: TimeInterval, wallTime: TimeInterval) -> A {
        let phase = wallTime.truncatingRemainder(dividingBy: period)
        animation.beginTime = -(phase < 0 ? phase + period : phase)
        animation.repeatCount = .infinity
        let fps = Float(1 / Theme.Motion.personaFrameInterval)
        animation.preferredFrameRateRange = CAFrameRateRange(minimum: fps, maximum: fps, preferred: fps)
        return animation
    }
}
