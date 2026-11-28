import BeautifulUI
import PersonaKit
import SwiftUI
import UIKit
import PersonaWire

/// How far the Persona tab's sidebar is out (0 closed … 1 open). Its own object so following the finger only redraws
/// the page's motion modifier (offset, scale, opacities) and moves two UIKit views, never the conversation or the sidebar.
@Observable final class AssistantDrawer {
    var progress: CGFloat = 0 {
        didSet {
            if (progress > 0) != isOut { isOut = progress > 0 }
            applyToUIKit()
        }
    }
    /// Out at all: changes only when the sidebar starts to come out or is fully back, so views that only care
    /// whether it is out (the sidebar clearing its search) do not redraw on every frame of a drag.
    private(set) var isOut = false
    /// The sidebar's width, measured by `AssistantDrawerLayout`.
    @ObservationIgnored var width: CGFloat = 300 {
        didSet { if inPlace, width != oldValue { applyToUIKit() } }
    }
    /// Under iPhone Duo's side bars (`shellRailEdge`) the page stays where it is and the sidebar comes in over it from
    /// the leading edge (#3755): the shell's pages hold navigation controllers, and moved sideways under those bars they
    /// lay their pages out again on every frame. Set by `AssistantDrawerLayout`.
    @ObservationIgnored var inPlace = false {
        didSet { if inPlace != oldValue { applyToUIKit() } }
    }
    @ObservationIgnored var dragStart: CGFloat = 0
    /// The tab bar fades out as the sidebar comes out (it would sit over the sidebar's bottom row and repeat its Settings).
    /// Faded, not hidden: hiding it changes the page's safe area, and the whole conversation was laid out again on the
    /// first frame of every drag and the last of every close (20–40 ms each, sampled). Set by `DrawerTabBarProbe`.
    @ObservationIgnored weak var tabBar: UITabBar? {
        didSet { applyToUIKit() }
    }
    /// The sidebar's own hosting view (`DrawerSidebarHost`): lifted vertically here, hidden while closed.
    @ObservationIgnored weak var sidebarView: UIView? {
        didSet { applyToUIKit() }
    }
    /// The screen's own bottom inset (the home indicator) without the tab bar: the sidebar reaches down to it.
    var screenBottom: CGFloat = 0
    /// A settle's animation is running: the sidebar is hidden when it ends, not when it starts.
    @ObservationIgnored private var settling = false

    var isOpen: Bool { isOut }

    /// The UIKit side of the progress: the tab bar fades and the stationary sidebar lifts slightly. Inside `settle`
    /// they animate with UIKit, on the same spring as the page.
    func applyToUIKit() {
        if let tabBar {
            // The bar's own container, when it holds only the bar: the tab bar controller sets the bar's alpha back to
            // 1 whenever SwiftUI refreshes the tab bar's visibility (a launch with the sidebar out showed it again).
            let fading = tabBar.superview.flatMap { $0.subviews == [tabBar] ? $0 : nil } ?? tabBar
            let alpha = 1 - progress
            if fading.alpha != alpha { fading.alpha = alpha }
            tabBar.isUserInteractionEnabled = progress == 0
            tabBar.accessibilityElementsHidden = progress > 0
        }
        if let sidebarView {
            if inPlace {
                // Over the page, which stays (iPhone Duo's side bars): in from the leading edge, under the finger.
                sidebarView.transform = CGAffineTransform(translationX: -(1 - progress) * width, y: 0)
            } else {
                // The page is the upper sheet: sliding it away uncovers a sidebar that is already underneath.
                // Keep every horizontal position and the text size fixed; only a subtle vertical lift adds depth.
                sidebarView.transform = CGAffineTransform(translationX: 0, y: (1 - progress) * Theme.Motion.drawerLift)
            }
            let hidden = progress == 0 && !settling
            if inPlace {
                // Opaque all the way in: inside `settle` the alpha would fade in with the slide and show the page through.
                UIView.performWithoutAnimation { sidebarView.alpha = hidden ? 0 : 1 }
            } else {
                sidebarView.alpha = hidden ? 0 : 1
            }
            sidebarView.isHidden = hidden
            sidebarView.accessibilityElementsHidden = hidden
        }
    }

    /// `velocity`: the finger's, in points per second, when a drag lets go (the spring carries on at that speed).
    func open(velocity: CGFloat = 0) {
        UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
        settle(at: 1, velocity: velocity)
    }

    func close(velocity: CGFloat = 0) { settle(at: 0, velocity: velocity) }

    /// A drag took hold: the finger takes over where the page is (even mid-animation).
    func dragBegan() {
        dragStart = progress
        if progress == 0 {
            UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
        }
    }

    /// The finger is `x` points to the right of where the drag took hold. Straight to the value: no animation.
    func drag(_ x: CGFloat) {
        var t = Transaction()
        t.disablesAnimations = true
        withTransaction(t) { progress = min(1, max(0, dragStart + x / max(width, 1))) }
    }

    /// The finger let go at `velocity` points per second; returns whether it settles open.
    @discardableResult func letGo(velocity: CGFloat) -> Bool {
        let opens = Self.settlesOpen(progress: progress, velocity: velocity, width: max(width, 1))
        if opens {
            if dragStart == 0 { Haptics.tap() }
            open(velocity: velocity)
        } else {
            close(velocity: velocity)
        }
        return opens
    }

    private func settle(at target: CGFloat, velocity: CGFloat) {
        let way = (target - progress) * max(width, 1)
        let share = abs(way) > 1 ? max(0, Double(velocity / way)) : 0
        let motion = Theme.Motion.drawer(velocity: share)
        #if DEBUG && os(iOS)
        DrawerProbe.note("settle", ["from": Double(progress), "to": Double(target), "share": share])
        #endif
        #if DEBUG
        ShellTrace.shared.note("drawer \(target == 0 ? "close" : "open")")
        #endif
        // The same spring for the tab bar and the sidebar (UIKit) as for the page (SwiftUI).
        settling = true
        UIView.animate(motion) {
            withAnimation(motion) { progress = target }
        } completion: { [weak self] in
            guard let self else { return }
            settling = false
            applyToUIKit()
        }
    }

    /// Where a drag lets go, like the system's back swipe: a flick goes its way, otherwise past half way (counting a
    /// little of where it is heading) opens. `velocity` in points per second.
    nonisolated static func settlesOpen(progress: CGFloat, velocity: CGFloat, width: CGFloat) -> Bool {
        SwipeRelease.goes(progress: progress, velocity: velocity, width: width)
    }

    /// Whether a touch that has moved `dx`, `dy` from where it went down is the drawer's: within 45° of horizontal
    /// (a thumb's slant; vertical scrolls are steeper), and while closed only to the right.
    nonisolated static func takes(dx: CGFloat, dy: CGFloat, isOpen: Bool) -> Bool {
        abs(dx) > abs(dy) && (isOpen || dx > 0)
    }
}

/// The sidebar under the page: the foreground page slides to the right, uncovering the stationary sidebar with a slight
/// vertical lift. The page stays as a rounded card at the edge (a tap on it, or a drag back, closes). Dragging to the right
/// anywhere on the page opens it (`DrawerPan`). Under iPhone Duo's side bars the page stays where it is, dimmed, and the
/// sidebar slides in over it instead (`AssistantDrawer.inPlace`, #3755).
///
/// The sidebar stays built while closed (hidden): building it on the first frame of a drag was most of that frame.
/// While the finger moves only `DrawerPageMotion` reads the progress (the sidebar and the tab bar are moved by UIKit,
/// `AssistantDrawer.applyToUIKit`), and it changes nothing but transforms and opacities: the corners, shadow and edge
/// line are fixed shapes faded in, not redrawn per frame. docs/operations/testing.md「Sidebar dragging」has the numbers.
struct AssistantDrawerLayout<Sidebar: View, Page: View>: View {
    let drawer: AssistantDrawer
    /// The page is at its root (nothing pushed): only then does a drag open the sidebar.
    var enabled: Bool
    /// Behind the sidebar: the system's grouped gray, or the shell's near-white (docs/apple/ios.md「New shell」).
    var backdrop: Color = Color(.secondarySystemBackground)
    /// The shell's Assistant | Agents strip: a drag sideways may page between them instead (`DrawerPan`).
    var pager: ShellPager? = nil
    /// The Sidebar layout on a phone (#6332): what the sidebar opens is the page stack's first page, with no back, and a
    /// drag on it opens the drawer as on the root (`DrawerPan.firstPages`).
    var firstPages = false
    @ViewBuilder var sidebar: Sidebar
    var page: Page
    /// iPhone Duo's bars down the side (the shell's `shellRailEdge`): the page stays and the sidebar comes over it.
    @Environment(\.shellRailEdge) private var railEdge

    var body: some View {
        GeometryReader { geo in
            let width = min(geo.size.width * 0.82, 380)
            let inPlace = railEdge != nil
            ZStack(alignment: .leading) {
                DrawerSidebarHost(drawer: drawer, content: sidebar.padding(.bottom, drawer.screenBottom)
                    .background { if inPlace { DrawerSidebarGround(backdrop: backdrop) } })
                    .frame(width: width)
                    // Down under the faded tab bar, to the screen's own edge.
                    .ignoresSafeArea(.container, edges: .bottom)
                    .zIndex(inPlace ? 1 : 0)
                page
                    .modifier(DrawerPageMotion(drawer: drawer, width: width, inPlace: inPlace))
            }
            .frame(width: geo.size.width, height: geo.size.height, alignment: .leading)
            .background { backdrop.ignoresSafeArea() }
            #if os(iOS)
            .gesture(DrawerPan(drawer: drawer, enabled: enabled, pager: pager, firstPages: firstPages))
            #endif
            .background { DrawerTabBarProbe(drawer: drawer) }
            .onAppear {
                drawer.width = width
                drawer.inPlace = inPlace
            }
            .onChange(of: width) { drawer.width = width }
            .onChange(of: inPlace) { drawer.inPlace = inPlace }
        }
    }
}

/// The sidebar's own ground while it lies over the page (iPhone Duo's side bars, `AssistantDrawer.inPlace`): the
/// backdrop it otherwise lies on, with the page card's shadow and edge line along its trailing side.
private struct DrawerSidebarGround: View {
    let backdrop: Color

    var body: some View {
        Rectangle()
            .fill(backdrop)
            .shadow(color: DrawerEdge.shadow, radius: DrawerEdge.shadowRadius)
            .overlay(alignment: .trailing) {
                Rectangle().fill(DrawerEdge.line).frame(width: Theme.Size.line)
            }
            .ignoresSafeArea()
    }
}

/// Where the page card (or, over the page, the sidebar) meets the other: a soft shadow, and in dark mode, where the
/// shadow is invisible, a hairline.
private enum DrawerEdge {
    static let shadow = Color.black.opacity(0.18)
    static let shadowRadius: CGFloat = 24
    static let line = Color.primary.opacity(0.12)
}

/// Finds the tab bar and the screen's bottom inset for `AssistantDrawer`.
private struct DrawerTabBarProbe: UIViewRepresentable {
    let drawer: AssistantDrawer

    func makeUIView(context: Context) -> Probe {
        let v = Probe()
        v.drawer = drawer
        v.isUserInteractionEnabled = false
        return v
    }

    func updateUIView(_ v: Probe, context: Context) {}

    final class Probe: UIView {
        weak var drawer: AssistantDrawer?

        override func didMoveToWindow() {
            super.didMoveToWindow()
            guard let drawer else { return }
            guard let window else {
                // Another tab (a notification can switch while the sidebar is out): the bar is that tab's again.
                // Without animation: the bar shows on the other tab, and the sidebar is in when the user comes back.
                drawer.progress = 0
                drawer.tabBar = nil
                return
            }
            var r: UIResponder? = self
            while let next = r?.next, !(next is UIViewController) { r = next }
            drawer.tabBar = (r?.next as? UIViewController)?.tabBarController?.tabBar
            drawer.screenBottom = window.safeAreaInsets.bottom
        }

        override func safeAreaInsetsDidChange() {
            super.safeAreaInsetsDidChange()
            if let window, let drawer, drawer.screenBottom != window.safeAreaInsets.bottom { drawer.screenBottom = window.safeAreaInsets.bottom }
        }
    }
}

/// The sidebar in a hosting controller of its own, with the page's whole environment. It stays built while closed
/// (building it on a drag's first frame cost that frame 15–20 ms on the simulator), and closed it has to be gone from
/// VoiceOver too: SwiftUI's `accessibilityHidden` left its search field, scroll view and rows in the tree. A UIKit view
/// can be hidden for real (`AssistantDrawer.sidebarView`), and moved and faded without SwiftUI updating anything.
///
/// The look (light or dark) is the one thing not taken from the page: it is the window's. The copied environment is only
/// as new as the last update SwiftUI sent here (and SwiftUI hands the same value down as the hosted controllers' traits),
/// while iOS draws the app-switcher snapshots in the other look as the app goes to the background: the sidebar was then
/// left with the other look's `Color.primary`, white text on the light backdrop, until the next update that did not
/// always come (#4496). The window is where Settings › Appearance › Light & Dark and the system set the look (`ColorSchemeChoice`).
private struct DrawerSidebarHost<Content: View>: UIViewControllerRepresentable {
    let drawer: AssistantDrawer
    let content: Content

    /// SwiftUI sets the frame, transform, alpha and hidden flag of the view it is given (every update put the
    /// sidebar back in place, shown): the hosting view inside it is the drawer's own.
    final class Container: UIViewController {
        let host: UIHostingController<AnyView>
        /// The page's content and environment, as last handed in (`set`): the look goes on top of it.
        private var content: AnyView
        private var environment: EnvironmentValues

        init(_ content: AnyView, _ environment: EnvironmentValues) {
            self.content = content
            self.environment = environment
            host = UIHostingController(rootView: AnyView(content.environment(\.self, environment)))
            super.init(nibName: nil, bundle: nil)
        }

        required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }

        /// Touches only for the sidebar itself: closed (hidden), the container is clear room over the page's top bar and
        /// conversation, and took the Chat History button's tap on a 6.3" phone.
        final class PassThrough: UIView {
            var movedToWindow: () -> Void = {}

            override func hitTest(_ point: CGPoint, with event: UIEvent?) -> UIView? {
                let hit = super.hitTest(point, with: event)
                return hit === self ? nil : hit
            }

            override func didMoveToWindow() {
                super.didMoveToWindow()
                movedToWindow()
            }
        }

        /// The window whose look changes are followed, and the registration for them.
        private weak var watched: UIWindow?
        private var watching: UITraitChangeRegistration?

        /// In a window (or another one): follow its look.
        private func watchWindow() {
            let window = view.window
            if window !== watched {
                if let watching, let watched { watched.unregisterForTraitChanges(watching) }
                watched = window
                watching = window?.registerForTraitChanges([UITraitUserInterfaceStyle.self]) { [weak self] (_: UIWindow, _) in
                    self?.render()
                }
            }
            render()
        }

        override func loadView() {
            let v = PassThrough()
            v.movedToWindow = { [weak self] in self?.watchWindow() }
            view = v
        }

        override func viewDidLoad() {
            super.viewDidLoad()
            view.backgroundColor = .clear
            // The frame given here is the sidebar's whole room; only the keyboard still insets it.
            host.safeAreaRegions = .keyboard
            host.sizingOptions = []
            host.view.backgroundColor = .clear
            addChild(host)
            view.addSubview(host.view)
            host.didMove(toParent: self)
        }

        override func viewDidLayoutSubviews() {
            super.viewDidLayoutSubviews()
            // Bounds and center, not frame: the drawer keeps a transform on it.
            host.view.bounds = view.bounds
            host.view.center = CGPoint(x: view.bounds.midX, y: view.bounds.midY)
        }

        func set(_ content: AnyView, _ environment: EnvironmentValues) {
            self.content = content
            self.environment = environment
            if isViewLoaded { render() }
        }

        /// The page's environment with the window's look in it; the hosting controller's traits on the same look, for
        /// what UIKit and the glass draw in the sidebar. Not in a window yet: the page's.
        private func render() {
            var look = environment
            let style = view.window?.traitCollection.userInterfaceStyle ?? .unspecified
            if style == .dark { look.colorScheme = .dark } else if style == .light { look.colorScheme = .light }
            if host.overrideUserInterfaceStyle != style { host.overrideUserInterfaceStyle = style }
            host.rootView = AnyView(content.environment(\.self, look))
        }
    }

    func makeUIViewController(context: Context) -> Container {
        let c = Container(root, context.environment)
        c.loadViewIfNeeded()
        drawer.sidebarView = c.host.view
        return c
    }

    func updateUIViewController(_ c: Container, context: Context) {
        c.set(root, context.environment)
        if drawer.sidebarView !== c.host.view { drawer.sidebarView = c.host.view }
    }

    private var root: AnyView { AnyView(content.accessibilityIdentifier("assistant-sidebar")) }
}

/// The page pushed aside: a card with rounded corners, a shadow and (dark mode) an edge line, dimmed; a tap on
/// it closes the sidebar.
private struct DrawerPageMotion: ViewModifier {
    let drawer: AssistantDrawer
    let width: CGFloat
    /// iPhone Duo's side bars: the page stays where it is, only dimmed, and the sidebar comes over it
    /// (`AssistantDrawer.inPlace`).
    var inPlace = false

    func body(content: Content) -> some View {
        let p = drawer.progress
        let out = p > 0
        // The corners only change when the page leaves or comes back to full screen (where the screen's own, rounder
        // corners hide them), so the mask and the shadow are not redrawn on every frame. A page that stays is no card.
        let card = RoundedRectangle(cornerRadius: out && !inPlace ? Theme.Radius.page : 0, style: .continuous)
        let edge = inPlace ? 0 : p
        content
            // While the sidebar is out the page is only a card to tap back to (the overlay below says so).
            .accessibilityHidden(out)
            .mask { card.ignoresSafeArea() }
            .background {
                ZStack {
                    card.fill(Color.black).shadow(color: DrawerEdge.shadow, radius: DrawerEdge.shadowRadius).opacity(edge)
                    card.fill(Theme.pageBackground)
                }
                .ignoresSafeArea()
            }
            .overlay {
                if out {
                    // In dark mode the shadow is invisible: a hairline keeps the card apart from the sidebar.
                    card.strokeBorder(DrawerEdge.line, lineWidth: Theme.Size.line)
                        .opacity(edge)
                        .ignoresSafeArea()
                        .allowsHitTesting(false)
                    // The page at the edge: dimmed as the sidebar comes out (#334). The dimming is the card's shape: the
                    // overlay is not under the mask, and a plain rectangle grayed the sidebar's color outside the card's
                    // rounded corners (#1247).
                    card.fill(Theme.Fill.scrim)
                        .opacity(p)
                        .ignoresSafeArea()
                        .allowsHitTesting(false)
                    // A tap on it closes the sidebar: the whole card, or the part beside the sidebar where the page stays.
                    Color.clear
                        .contentShape(.rect)
                        .onTapGesture { drawer.close() }
                        .accessibilityElement()
                        .accessibilityLabel("Close Sidebar")
                        .accessibilityAddTraits(.isButton)
                        .accessibilityAction { drawer.close() }
                        .accessibilityIdentifier("assistant-drawer-close")
                        .padding(.leading, inPlace ? width : 0)
                        .ignoresSafeArea()
                }
            }
            // Slid aside whole, like ChatGPT (2026-11-01 user request): it used to shrink 6% too, which left gaps above
            // and below the card and dropped its top bar below the sidebar's, and moved every point of it each frame.
            .offset(x: inPlace ? 0 : p * width)
    }
}

#if os(iOS)
/// The drag that opens and closes the sidebar. A UIKit pan so it can decide when the finger has moved a little: it takes
/// the touch when that move is within 45° of horizontal (steeper drags stay the list's), only to the right while closed,
/// and never on something that scrolls sideways itself (a wide code block or table that is not at its left edge) —
/// those scroll first. The direction comes from where the touch went down (`Recognizer.start`): the pan's own
/// translation is still zero when it asks, and its velocity then is a guess from the last two samples (a 30° drag
/// read as almost straight down, so a slanted swipe did nothing).
struct DrawerPan: UIGestureRecognizerRepresentable {
    let drawer: AssistantDrawer
    var enabled: Bool
    /// In the shell: the Assistant | Agents strip. On Assistant a drag to the left pages to Agents (to the right is still the
    /// drawer); on Agents a drag to the right pages back to Assistant (the drawer opens from ☰ there).
    var pager: ShellPager? = nil
    /// The Sidebar layout on a phone (#6332): a stack's first page is the sidebar's pick, not a page to go back from, so
    /// only a second page under the finger keeps the drag for its back swipe.
    var firstPages = false

    /// A drag to the left that starts on a row that swipes left (a drawer conversation's Archive, `SwipeRowMark`) is the
    /// row's, unless it starts this close to the screen's right edge, where the strip still pages (#2822). Agents' rows
    /// do not swipe (#1244).
    static let edge: CGFloat = Theme.Space.xxl

    /// What a drag moves, decided when it starts.
    enum Mode { case drawer, pager }

    final class Coordinator: NSObject, UIGestureRecognizerDelegate {
        var drawer: AssistantDrawer
        var enabled = true
        var pager: ShellPager?
        var firstPages = false
        var mode: Mode = .drawer
        init(drawer: AssistantDrawer) { self.drawer = drawer }

        func gestureRecognizerShouldBegin(_ g: UIGestureRecognizer) -> Bool {
            guard enabled, let pan = g as? Recognizer, let view = pan.view, let start = pan.start else { return false }
            let now = pan.location(in: view)
            let dx = now.x - start.x, dy = now.y - start.y
            #if DEBUG
            pan.asked = true
            func refused(_ why: String) -> Bool {
                DrawerProbe.note("refused", ["why": why, "dx": Int(dx), "dy": Int(dy)])
                return false
            }
            #else
            func refused(_ why: String) -> Bool { false }
            #endif
            if drawer.isOpen {
                mode = .drawer
                // A conversation in the sidebar swipes left to Archive: from its row the drag is the row's.
                if dx < 0, abs(dx) > abs(dy), Self.onSwipeRow(at: start, in: view) { return refused("a row's swipe") }
                return AssistantDrawer.takes(dx: dx, dy: dy, isOpen: true) ? true : refused("direction")
            }
            // Within 45° of horizontal: steeper is the page's own scrolling.
            guard abs(dx) > abs(dy) else { return refused("direction") }
            // The To Decide pager's card pages sideways itself (#12831, `SwipePageMark`): a drag on it is the pager's.
            if SwipePageMark.covers(start, in: view) { return refused("a pager's swipe") }
            if Self.pushed(at: start, in: view, beyond: firstPages ? 2 : 1) {
                #if DEBUG
                ShellTrace.shared.note("pan refused: pushed")
                #endif
                return refused("pushed")
            }
            if let pager, Self.pages(pager: pager, dx: dx) {
                if Self.sidewaysScroller(at: start, in: view, towardsRight: dx < 0) { return refused("sideways scroller") }
                if Self.typing(at: start, in: view) { return refused("typing") }
                // Agents' rows do not swipe (#1244: on the way to a chat the drag opened a row's buttons): along them a
                // drag pages, to Assistant or on to Messages (#392). A row that does swipe left (a conversation's Archive
                // in the drawer, `SwipeRowMark`) keeps that drag, except from the band at the right edge (#2822).
                if dx < 0, start.x < view.bounds.width - DrawerPan.edge, Self.onSwipeRow(at: start, in: view) {
                    return refused("a row's swipe")
                }
                mode = .pager
                return true
            }
            // The drawer: only to the right while closed, and not where the strip has a page to the left.
            guard dx > 0, pager.map({ $0.page == 0 }) ?? true else { return refused("direction") }
            mode = .drawer
            return Self.sidewaysScroller(at: start, in: view, towardsRight: false) ? refused("sideways scroller") : true
        }

        /// Whether a drag `dx` points toward an existing neighbor on the page strip.
        static func pages(pager: ShellPager, dx: CGFloat) -> Bool {
            (dx < 0 && pager.page < pager.pageCount - 1) || (dx > 0 && pager.page > 0)
        }

        /// Scroll views under the page wait for the drawer to decide, which it does within the same few points they
        /// would start in (so scrolling starts no later). Without this the conversation's pan won the race on a
        /// thumb's 40° swipe (its vertical part passed the list's threshold first) and the drawer was never asked:
        /// the swipe did nothing. A code block at its left edge likewise took a drag to the right to bounce.
        /// In the shell a list's own swipe (a drawer conversation's Archive) waits too: refused at once on a row that
        /// swipes, the row's swipe goes ahead.
        func gestureRecognizer(_ g: UIGestureRecognizer, shouldBeRequiredToFailBy other: UIGestureRecognizer) -> Bool {
            guard let s = other.view as? UIScrollView else { return false }
            if other === s.panGestureRecognizer { return true }
            // The open drawer's own list (a conversation's Archive) waits too.
            return (pager != nil || drawer.isOpen) && other is UIPanGestureRecognizer && (s is UICollectionView || s is UITableView)
        }

        /// Something under the finger scrolls sideways and has room to go the way the finger pulls it: back towards its
        /// left edge for a drag to the right, on towards its right edge (`towardsRight`) for a drag to the left.
        /// Against the width it shows content in: on an iPad a scroll view in the chat runs on under the sidebar,
        /// with that much inset on its left (#5737: a wide table 751 wide in bounds 788, inset 372, scrolls).
        static func sidewaysScroller(at point: CGPoint, in view: UIView, towardsRight: Bool) -> Bool {
            var hit = view.hitTest(point, with: nil)
            while let v = hit, v !== view {
                if let s = v as? UIScrollView, s.isScrollEnabled,
                   s.contentSize.width > s.bounds.width - s.adjustedContentInset.left - s.adjustedContentInset.right + 1 {
                    let room = towardsRight
                        ? s.contentOffset.x < s.contentSize.width - s.bounds.width + s.adjustedContentInset.right - 1
                        : s.contentOffset.x > -s.adjustedContentInset.left + 1
                    if room { return true }
                }
                hit = v.superview
            }
            return false
        }

        /// A navigation stack under the finger has a page pushed: a drag from there is that stack's back swipe, whatever
        /// the page last said about being at its root (`enabled`). That word comes through SwiftUI state and was stale
        /// now and then: on a chat opened on Agents the strip took the edge swipe and slid to Assistant, the chat still pushed
        /// (`ShellPagerTests.testPushedPagesSwipeBack`, 3 in 24 runs). UIKit's stack is what the swipe acts on.
        /// `beyond`: how many pages a stack has before one counts as pushed (2 in the Sidebar layout, its first page).
        static func pushed(at point: CGPoint, in view: UIView, beyond: Int = 1) -> Bool {
            var r: UIResponder? = view.hitTest(point, with: nil)
            while let x = r, x !== view {
                if let vc = x as? UIViewController, let nav = (vc as? UINavigationController) ?? vc.navigationController,
                   nav.viewControllers.count > beyond {
                    return true
                }
                r = x.next
            }
            return false
        }

        /// The finger is on the text being typed (moving the cursor, selecting): not a page turn.
        static func typing(at point: CGPoint, in view: UIView) -> Bool {
            var hit = view.hitTest(point, with: nil)
            while let v = hit, v !== view {
                if v.isFirstResponder, v is UITextInput { return true }
                hit = v.superview
            }
            return false
        }

        /// The finger is on a row that swipes to the left (`SwipeRowMark`): the sidebar's conversations (Archive).
        static func onSwipeRow(at point: CGPoint, in view: UIView) -> Bool {
            var hit = view.hitTest(point, with: nil)
            while let v = hit, v !== view {
                if v is UICollectionViewCell || v is UITableViewCell { return SwipeRowMark.marks(v) }
                hit = v.superview
            }
            return false
        }
    }

    /// The pan, remembering where the touch went down.
    final class Recognizer: UIPanGestureRecognizer {
        private(set) var start: CGPoint?
        #if DEBUG
        var asked = false
        private var moved: CGPoint = .zero
        #endif

        override func touchesBegan(_ touches: Set<UITouch>, with event: UIEvent) {
            if start == nil { start = touches.first?.location(in: view) }
            #if DEBUG
            asked = false
            moved = .zero
            #endif
            super.touchesBegan(touches, with: event)
        }

        #if DEBUG
        override func touchesMoved(_ touches: Set<UITouch>, with event: UIEvent) {
            super.touchesMoved(touches, with: event)
            if let s = start, let p = touches.first?.location(in: view) { moved = CGPoint(x: p.x - s.x, y: p.y - s.y) }
        }
        #endif

        override func reset() {
            #if DEBUG
            // Another gesture took the touch before this one was asked (`DrawerProbe`).
            if !asked, hypot(moved.x, moved.y) > 10 { DrawerProbe.note("lost", ["dx": Int(moved.x), "dy": Int(moved.y)]) }
            #endif
            start = nil
            super.reset()
        }
    }

    func makeCoordinator(converter: CoordinateSpaceConverter) -> Coordinator { Coordinator(drawer: drawer) }

    func makeUIGestureRecognizer(context: Context) -> Recognizer {
        let g = Recognizer()
        g.delegate = context.coordinator
        g.maximumNumberOfTouches = 1
        return g
    }

    func updateUIGestureRecognizer(_ recognizer: Recognizer, context: Context) {
        context.coordinator.enabled = enabled
        context.coordinator.pager = pager
        context.coordinator.firstPages = firstPages
    }

    func handleUIGestureRecognizerAction(_ recognizer: Recognizer, context: Context) {
        if context.coordinator.mode == .pager, let pager = context.coordinator.pager {
            page(recognizer, pager)
            return
        }
        switch recognizer.state {
        case .began:
            #if DEBUG
            DrawerProbe.note("began", ["from": Double(drawer.progress)])
            DrawerProbe.shared.dragBegan()
            #endif
            drawer.dragBegan()
            drawer.drag(recognizer.translation(in: recognizer.view).x)
        case .changed:
            drawer.drag(recognizer.translation(in: recognizer.view).x)
        case .ended:
            let v = recognizer.velocity(in: recognizer.view).x
            let at = drawer.progress
            let opens = drawer.letGo(velocity: v)
            #if DEBUG
            DrawerProbe.note("ended", ["at": Double(at), "velocity": Int(v), "open": opens])
            DrawerProbe.shared.dragEnded(opens ? "open" : "closed")
            #endif
        case .cancelled, .failed:
            drawer.close()
            #if DEBUG
            DrawerProbe.shared.dragEnded("cancelled")
            #endif
        default: break
        }
    }

    /// A drag along the strip: the pages follow the finger, and settle on one of them when it lets go.
    private func page(_ recognizer: Recognizer, _ pager: ShellPager) {
        switch recognizer.state {
        case .began:
            UIApplication.shared.sendAction(#selector(UIResponder.resignFirstResponder), to: nil, from: nil, for: nil)
            #if DEBUG
            DrawerProbe.note("page began", ["from": Double(pager.position)])
            #endif
            pager.dragBegan()
            pager.drag(recognizer.translation(in: recognizer.view).x)
        case .changed:
            pager.drag(recognizer.translation(in: recognizer.view).x)
        case .ended:
            let v = recognizer.velocity(in: recognizer.view).x
            #if DEBUG
            DrawerProbe.note("page ended", ["at": Double(pager.position), "velocity": Int(v)])
            #endif
            pager.letGo(velocity: v)
        case .cancelled, .failed:
            pager.cancel()
        default: break
        }
    }
}
#else
/// visionOS: a pinch-and-drag scrolls, and the sidebar opens from its ☰ button; nothing takes a sideways drag.
struct DrawerPan {}
#endif


/// Marks a view whose sideways drags the drawer and the strip leave alone, either way (the To Decide pager's card, #12831):
/// a drag that starts inside a mounted mark is the view's own.
struct SwipePageMark: UIViewRepresentable {
    final class Marker: UIView {}
    /// The marks on screen, held weakly: the drawer asks only when a drag begins.
    @MainActor private static let marks = NSHashTable<Marker>.weakObjects()

    func makeUIView(context: Context) -> Marker {
        let v = Marker()
        v.isUserInteractionEnabled = false
        Self.marks.add(v)
        return v
    }

    func updateUIView(_ uiView: Marker, context: Context) {}

    /// Whether `point` (in `view`) is inside a mark shown in the same window.
    @MainActor static func covers(_ point: CGPoint, in view: UIView) -> Bool {
        marks.allObjects.contains { m in
            m.window != nil && m.window === view.window && !m.isHidden && m.convert(m.bounds, to: view).contains(point)
        }
    }
}

/// Marks a list row whose swipe to the left the drawer and the strip leave alone (a sidebar conversation's Archive):
/// they look for it in the cell under the finger.
struct SwipeRowMark: UIViewRepresentable {
    final class Marker: UIView {}

    func makeUIView(context: Context) -> Marker {
        let v = Marker()
        v.isUserInteractionEnabled = false
        return v
    }

    func updateUIView(_ uiView: Marker, context: Context) {}

    /// Whether a cell has the mark somewhere inside.
    static func marks(_ cell: UIView) -> Bool {
        var stack = [cell]
        while let v = stack.popLast() {
            if v is Marker { return true }
            stack.append(contentsOf: v.subviews)
        }
        return false
    }
}

/// Calls `closed` when the drawer is back in: only this empty view reads whether it is out, so the sidebar itself is
/// not redrawn when it comes out.
private struct SidebarClosed: View {
    let drawer: AssistantDrawer
    var closed: () -> Void

    var body: some View {
        Color.clear.onChange(of: drawer.isOut) { _, out in if !out { closed() } }
    }
}

/// What the sidebar lists (2026-10-31, laid out like Doubao's): Persona's name with a search button; the things to start
/// First the main conversation, as Persona with the app's logo, and a line (#2004, in every layout); the things to
/// start (New Chat, Scheduled Tasks, Knowledge; the shell's drawer: Inbox … Machines); a line; Pin and Recent, one line each with a round
/// mark; Persona's face, where it runs and Settings at the foot.
struct AssistantSidebar: View {
    /// The open conversation's row: every sidebar row's one selected shade (`SidebarSelectedShade`, #11032).
    private static var selectedFill: Color { SidebarSelectedShade.fill }

    @Environment(AppModel.self) private var model
    var current: SessionKey?
    var open: (SessionKey) -> Void
    var newConversation: () -> Void
    var openProfile: () -> Void
    var openRoutines: () -> Void
    var openKnowledge: () -> Void
    /// nil when the tab bar is beside the sidebar (a column on a wide display): no second way to Settings.
    var openSettings: (() -> Void)?
    /// The drawer it is in (nil as a column). The drawer's sidebar stays built while closed: closing it clears the search.
    var drawer: AssistantDrawer?
    /// The shell's drawer (docs/apple/ios.md「New shell」) also lists Inbox and Machines, and ends in New Chat + Settings like ChatGPT's;
    /// so does the iPad's column (docs/apple/ios.md「iPad」), whose tab bar has no Inbox or Settings.
    var openInbox: (() -> Void)? = nil
    var openTodos: (() -> Void)? = nil
    var onMachines: (() -> Void)? = nil
    /// Opens an extension's main-navigation page (nav_page, #2961): listed after the places to go, with its badge.
    /// nil where they are listed elsewhere (the Mac's sections column).
    // Not `openExtension`: Swift keeps property names in the binary, and that is a DEBUG switch release.sh rejects.
    var onOpenExtension: ((ExtensionNavRoute) -> Void)? = nil
    /// Health: the health space's page (docs/assistant/health.md); nil leaves the row out.
    var openHealth: (() -> Void)? = nil
    /// The `health` module provides Health's page (#14278); while it is off the row is left out. Observed: a hello that
    /// turns it on or off draws the sidebar again.
    private var hasHealth: Bool { Modules.registry.shellPage(id: SidebarRoute.healthPage) != nil }
    /// A domain space (#13500, docs/apple/ios.md "Domain spaces"): its latest conversation, else its home; and New
    /// space. nil leaves the Spaces section out.
    var openSpace: ((String) -> Void)? = nil
    var openNewSpace: (() -> Void)? = nil
    /// Recordings (docs/apple/recording.md「Meeting recordings」): opens the Recordings page, whose Record starts one, or
    /// the recording running. nil leaves the row out (the Mac records nothing).
    var openRecordingsPage: (() -> Void)? = nil
    /// The Sidebar layout (#6332): Messages (the Lark chats, the Separate layout's third page) among the places while
    /// it is turned on; nil leaves it out.
    var openMessages: (() -> Void)? = nil
    var openStarred: (() -> Void)? = nil
    private var isShell: Bool { openInbox != nil }
    /// Which part this draws (#6332): the whole sidebar, or one piece of the Sidebar layout's column, which lays the
    /// rows inside the Agents list and puts the head, the main conversation and the foot around it
    /// (`UnifiedSidebarColumn`).
    var part: AssistantSidebarPart = .whole
    /// What the parts share (the search, the open dialogs); nil: this sidebar's own.
    var shared: AssistantSidebarUI? = nil
    /// The Sidebar layout: the place shown beside the sidebar, its row shaded as the open conversation's is.
    var shownPlace: ShellPage? = nil
    /// The one sidebar's column beside a page (#4206): its head's button folds it away, at the head's trailing end, as
    /// the earlier one sidebar's header had it; the page's own button then only brings it back. nil in the drawer.
    var foldColumn: (() -> Void)? = nil
    /// The Sidebar layout's folds (the rows part): the places and Persona's chats, remembered on this device.
    @StoredValue("sidebar.fold.places") private var placesFolded = false
    @StoredValue("sidebar.fold.chats") private var chatsFolded = false
    @StoredValue("sidebar.fold.spaces") private var spacesFolded = false
    /// The To-dos places under the To-dos row (#10465), folded by the owner only: open at first, never by itself.
    @StoredValue("sidebar.fold.todos") private var todosFolded = false
    @State private var own = AssistantSidebarUI()
    private var ui: AssistantSidebarUI { shared ?? own }
    /// Whether Recent is folded, kept on this device (#6228): default open, the choice survives relaunches.
    @StoredValue("assistant.sidebar.recent.folded") private var recentFolded = false
    private let taskChats = AssistantTaskChatsStore.shared
    @FocusState private var searching: Bool

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("AssistantSidebar")
        #endif
        switch part {
        case .whole: whole
        #if os(iOS)
        case .header: unifiedHeader
        case .main: unifiedMain
        case .rows: unifiedRows
        case .footer: shellFooter
        #else
        case .header, .main, .rows, .footer: whole // the Sidebar layout is the iPhone's and the iPad's (#6332)
        #endif
        }
    }

    /// Closed in its drawer, the sidebar keeps the list it last showed: `assistantConversations` reads every session on
    /// every machine, so each hub push re-ran this body (three times a push) while nobody could see it. It reads the
    /// live list again as soon as it starts to come out (`isOut`).
    private var shownConversations: [Session] {
        ui.feed.conversations(drawer?.isOut ?? true ? model.assistantConversations : nil)
    }

    /// The same for Archived's count: it reads every session too (all of them redrew this while it was closed).
    private var shownArchived: Int {
        ui.feed.archived(drawer?.isOut ?? true ? model.archivedConversations.count : nil)
    }

    @ViewBuilder private var whole: some View {
        let out = drawer?.isOut ?? true
        let all = shownConversations
        let archived = shownArchived
        VStack(spacing: 0) {
            topBar
                .padding(.horizontal, Theme.Space.xl)
                .padding(.top, besideBar ? AssistantTopBar.topInset : Theme.Space.s)
                .padding(.bottom, Theme.Space.s)
            // A list (not a stack in a scroll view) for its rows' own swipe: a conversation swipes left to Archive.
            List {
                Group {
                    if ui.query.isEmpty {
                        // The main conversation first, once, in every layout (#2004): never again under Pin or Recent.
                        mainItem(all)
                        VStack { Divider() }.padding(.horizontal, Theme.Space.m).padding(.vertical, Theme.Space.s) // a row lays a bare Divider out upright
                        actions(all)
                        spacesSection
                        VStack { Divider() }.padding(.horizontal, Theme.Space.m).padding(.vertical, Theme.Space.s) // a row lays a bare Divider out upright
                        conversations(all, archived: archived)
                    } else {
                        results(searchRows(all))
                    }
                }
                .listRowInsets(EdgeInsets(top: 0, leading: Theme.Space.s, bottom: 0, trailing: Theme.Space.s))
                .listRowSeparator(.hidden)
                .listRowBackground(Color.clear)
            }
            .listStyle(.plain)
            .scrollContentBackground(.hidden)
            .environment(\.defaultMinListRowHeight, 0)
            .contentMargins(.bottom, Theme.Space.l, for: .scrollContent)
            .scrollDismissesKeyboard(.interactively)
            footer
        }
        #if os(iOS)
        // A column (the iPad's split, the open iPhone Duo) keeps out of the keyboard only while its own search field is
        // typed in, so the chats found stay above it. Another field's keyboard, above all a sheet's over the page, shrank
        // the column behind the sheet: its list lost all but a row or two and got them back as the keyboard went (#7629).
        .ignoresSafeArea(.keyboard, edges: drawer == nil && !searching ? Edge.Set.bottom : [])
        #endif
        // Which conversations the tasks started: read again as the sidebar comes out, when a conversation came or went, and
        // for another hub's or manager's page.
        .task(id: AssistantTaskChatsStore.readKey(out: out, group: model.hub?.group, manager: model.assistantManagerID,
                                                   conversations: all.count)) { if out { await taskChats.refresh(model.api) } }
        // Keep the drawer's growing row types out of the sheet/search modifiers (#6735, #12249).
        .modifier(AssistantSidebarPresenters(ui: ui, open: open))
        .stackSegment()
        .background {
            if let drawer {
                SidebarClosed(drawer: drawer) {
                    ui.query = ""
                    ui.searchOpen = false
                    searching = false
                }
            }
        }
        .modifier(AskChatSearch(ui: ui))
    }

    #if os(iOS)
    // MARK: The Sidebar layout's parts (#6332)

    /// The head: the assistant (its switcher). Search is the foot's, over the whole window (#4206, the owner). Its mark
    /// on the places' symbol line (`rowsLeading`).
    private var unifiedHeader: some View {
        topBar
            .padding(.leading, ui.rowsLeading.map { $0 + Self.headShift } ?? Theme.Space.xl)
            .padding(.trailing, Theme.Space.xl)
            .padding(.top, besideBar ? AssistantTopBar.topInset : Theme.Space.s)
            .padding(.bottom, Theme.Space.s)
    }

    /// The main conversation, held over the list as the web's is (#5581): it never scrolls away. A line under it.
    private var unifiedMain: some View {
        let out = drawer?.isOut ?? true
        let all = shownConversations
        return VStack(spacing: 0) {
            mainItem(all)
            Divider().padding(.horizontal, Theme.Space.m).padding(.top, Theme.Space.s)
        }
        // Where the list's rows start (`rowsLeading`): its mark on the places' symbol line, its shade as wide as theirs.
        .padding(.horizontal, ui.rowsLeading ?? Theme.Space.s)
        // Which conversations the tasks started, as the whole sidebar reads them (the rows' source marks).
        .task(id: AssistantTaskChatsStore.readKey(out: out, group: model.hub?.group, manager: model.assistantManagerID,
                                                   conversations: all.count)) { if out { await taskChats.refresh(model.api) } }
    }

    /// The rows the Agents list holds above its own: the places to go and Persona's chats, each under a heading that
    /// folds it. Rows of the host's list (insets, no lines, its color through). Search is the foot's (#4206).
    private var unifiedRows: some View {
        let all = shownConversations
        let archived = shownArchived
        return Group {
            foldHeading(String(localized: "Pages [Sidebar Layout]"), folded: $placesFolded, id: "sidebar-fold-places") {
                EmptyView()
            } tally: { placesTally }
            .onGeometryChange(for: CGFloat.self) { $0.frame(in: .named(UnifiedSidebar.columnSpace)).minX } action: { x in
                // Only where the rows are: as the column slides away its space moves and this read -376 for a moment,
                // which laid the head and the main conversation out again twice per fold, once 376 pt off (#4206).
                ui.placeRows(at: x)
            }
            if !placesFolded { actions(all) }
            if showsSpaces {
                foldHeading(String(localized: "Spaces [Sidebar Layout]"), folded: $spacesFolded, id: "sidebar-fold-spaces") {
                    EmptyView()
                } tally: { EmptyView() }
                if !spacesFolded { spaceRows }
            }
            // No New Chat on the heading: the column has one, its foot's (#4206, the owner).
            foldHeading(String(localized: "Persona Chats [Sidebar Layout]"), folded: $chatsFolded, id: "sidebar-fold-chats") {
                EmptyView()
            } tally: { EmptyView() }
            if !chatsFolded { conversations(all, archived: archived) }
        }
        .listRowInsets(EdgeInsets(top: 0, leading: Theme.Space.s, bottom: 0, trailing: Theme.Space.s))
        .listRowSeparator(.hidden)
        .listRowBackground(Color.clear)
    }

    /// What a folded Pages heading still says: Inbox's and To-dos' counts (the web's tally).
    @ViewBuilder private var placesTally: some View {
        if placesFolded {
            let inbox = showsInbox ? InboxStore.shared.unreadCount : 0
            let todos = showsTodos ? TodoStore.shared.sidebarCount : 0
            HStack(spacing: Theme.Space.xs) {
                if inbox > 0 { CountBadge(count: inbox, look: .needs, label: String(localized: "Inbox")) }
                if todos > 0 {
                    if TodosV1.shared.isOn {
                        // The V1 list's N, readable by its tests (#10465); today's tally stays as it was.
                        CountBadge(count: todos, look: .needs, label: String(localized: "To-dos"))
                            .accessibilityValue(String(todos))
                            .accessibilityIdentifier("sidebar-fold-places.todos")
                    } else {
                        CountBadge(count: todos, look: .needs, label: String(localized: "To-dos"))
                    }
                }
            }
        }
    }

    /// A section of the Sidebar layout: its name and a chevron that fold it (remembered), what it still counts while
    /// folded, and its own buttons at the end.
    private func foldHeading<Trailing: View, Tally: View>(_ text: String, folded: Binding<Bool>, id: String,
                                                         @ViewBuilder trailing: () -> Trailing,
                                                         @ViewBuilder tally: () -> Tally) -> some View {
        HStack(spacing: Theme.Space.xs) {
            Button {
                withAnimation(Theme.Motion.state) { folded.wrappedValue.toggle() }
            } label: {
                HStack(spacing: Theme.Space.xs) {
                    Text(text).font(Theme.Font.captionStrong).foregroundStyle(.secondary).lineLimit(1)
                    ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.label).foregroundStyle(.tertiary)
                        .rotationEffect(.degrees(folded.wrappedValue ? 0 : 90))
                    Spacer(minLength: 0)
                }
                .modifier(SidebarHeadingBox())
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityLabel(text)
            .accessibilityValue(folded.wrappedValue ? String(localized: "Collapsed") : String(localized: "Expanded"))
            .accessibilityAddTraits(.isHeader)
            .accessibilityIdentifier(id)
            tally()
            trailing()
        }
        .padding(.leading, Theme.Space.m)
        .padding(.top, SidebarHeadingBox.above)
    }
    #endif
}

#if os(iOS)
/// The box of a Sidebar layout section heading's name (#6332, #10551): Pages, Persona Chats and Agents all take it, so the
/// three are as tall and as far apart whatever a heading carries beside its name (Agents' ⋯, Personal Agents and +, in
/// a box of their own that never sets the row's height). A sidebar row's height and padding, and `above` over it.
struct SidebarHeadingBox: ViewModifier {
    /// The room over each heading, inside its row.
    static let above = Theme.Space.s

    func body(content: Content) -> some View {
        content
            .padding(.vertical, Theme.Sidebar.rowPadding)
            .scaledFrame(minHeight: Theme.Sidebar.row)
    }
}
#endif

/// Which part of the sidebar an `AssistantSidebar` draws (#6332).
enum AssistantSidebarPart {
    /// The whole sidebar: the drawer, the iPad's and iPhone Duo's column, the Mac's list.
    case whole
    /// The Sidebar layout's pieces (`UnifiedSidebarColumn`): its head, the main conversation held over the list, the
    /// rows inside the Agents list, and the foot (New Chat, Settings).
    case header, main, rows, footer
}

/// What an `AssistantSidebar`'s parts share: the search's words, and what its rows opened (End…, Rename…, Pin, All Chats,
/// Archived), presented by `AssistantSidebarPresenters` around them. The whole sidebar keeps its own; the Sidebar
/// layout's column one for its pieces, whose rows sit inside another list (a sheet on a list's row went with the row
/// as it scrolled away).
@MainActor @Observable final class AssistantSidebarUI {
    var query = ""
    var searchOpen = false
    #if os(iOS)
    /// The search over the whole window: the Sidebar layout's one search from its foot (#4206).
    var fullSearch = false
    /// What it starts looking through: the Agents where an Agent's chat is in front (#4206).
    var searchScope = AssistantFullSearch.Scope.chats
    /// The Agent picked in it: its chat opens once the search has gone.
    var pickedAgent: SessionKey?
    /// Where the Sidebar layout's list rows start in its column (the list's own margin and the rows' inset), measured on
    /// the Pages heading: its head and the main conversation start there too, so the marks of the head, the main row and
    /// the places stand on one line (#4206). nil until the list is laid out. Written only through `placeRows(at:)`.
    private(set) var rowsLeading: CGFloat?
    /// Lazy: made by the first row-layout measurement, not when the view creates its `@State` model (P3).
    @ObservationIgnored private lazy var rowsLeadingEchoes = LayoutEchoGuard()

    /// A new measure of where the rows start. It is read back by the head and the main row, so a measure that only swings
    /// back to the one before (the list moved by the write it caused) is not written again: the iPhone Duo grown from split
    /// screen to full screen while an Agent's reply streamed swung the column between two widths 9 pt apart for ever,
    /// inside one layout pass, until the watchdog ended the app (#12318).
    func placeRows(at x: CGFloat) {
        guard x >= 0, rowsLeadingEchoes.accepts(x, now: ProcessInfo.processInfo.systemUptime) else { return }
        rowsLeading = x
    }
    #endif
    var ending: Session?
    var renaming: Session?
    /// The chat whose Pin / Unpin confirmation is open (#6214): the menu asks before the hub call.
    var leaderConfirm: Session?
    var showArchived = false
    /// The conversation picked in Archived, Auto conversations or Search: opened when its sheet has gone.
    var picked: SessionKey?
    var autoList: AutoConversationsRoute?
    /// Lazy: made when the sidebar first lists, not as a view's state is set up (docs/apple/performance.md, P3).
    @ObservationIgnored lazy var feed = SidebarFeed()
    /// What was said in the chats, found by the manager (#6284), merged into the title / last-message matches. Lazy (P3):
    /// made by the first search; it is observable itself, so the results redraw when its answer comes.
    @ObservationIgnored lazy var chatSearch = AssistantChatSearch()
}

/// A value measured from layout and written back to state that the same layout reads (docs/apple/performance.md, P7): a
/// measure equal to the one written before the current, within `window` of the current write, is the layout answering
/// its own write (A → B → A), and is refused, so the layout settles on what it has. A real change, a new value or the old
/// one again a while later (a turn there and back), is written (#12318).
nonisolated struct LayoutEchoGuard: Sendable {
    /// How long after a write a return to the value before it counts as the layout's echo.
    static let window: TimeInterval = 0.5
    /// Measures this close are the same.
    static let tolerance: CGFloat = 0.5

    private var current: (value: CGFloat, at: TimeInterval)?
    private var previous: CGFloat?

    /// Whether `value`, measured at `now` (seconds, monotonic), is to be written.
    mutating func accepts(_ value: CGFloat, now: TimeInterval) -> Bool {
        if let current {
            if abs(current.value - value) < Self.tolerance { return false }
            if let previous, abs(previous - value) < Self.tolerance, now - current.at < Self.window { return false }
        }
        previous = current?.value
        current = (value, now)
        return true
    }
}

/// Asks the manager for what was said in the chats as the search's words change (#6284): on the part that holds the
/// search field (the whole sidebar, or the Sidebar layout's head), so one query asks once.
private struct AskChatSearch: ViewModifier {
    @Environment(AppModel.self) private var model
    let ui: AssistantSidebarUI

    func body(content: Content) -> some View {
        content.onChange(of: ui.query) { _, q in
            ui.chatSearch.search(q, api: model.api, on: model.features(model.hub?.group ?? "").contains(AssistantChatSearch.feature))
        }
    }
}

/// The sheets and dialogs an `AssistantSidebar`'s rows open, around the sidebar (or the Sidebar layout's column).
struct AssistantSidebarPresenters: ViewModifier {
    @Environment(AppModel.self) private var model
    let ui: AssistantSidebarUI
    let open: (SessionKey) -> Void

    func body(content: Content) -> some View {
        content
        .confirmClose(Bindable(ui).ending)
        .renameSessionAlert(Bindable(ui).renaming)
        .accessibleConfirmationDialog(
            ui.leaderConfirm.map { session in
                model.assistantLeaders.contains(session.sessionID)
                    ? String(localized: "Unpin Confirmation Title [Leader Actions 6214]")
                    : String(localized: "Pin Confirmation Title [Leader Actions 6214]")
            } ?? "",
            isPresented: Binding(get: { ui.leaderConfirm != nil }, set: { if !$0 { ui.leaderConfirm = nil } }),
            titleVisibility: .visible, hasCancel: true, presenting: ui.leaderConfirm) { session in
            // The hub stays the only state source: only the second choice calls make/end (#6214).
            let lead = model.assistantLeaders.contains(session.sessionID)
            Button(lead ? String(localized: "Unpin [Assistant Sidebar b7c40e]")
                       : String(localized: "Pin [Assistant Sidebar 6d1f2a]")) {
                Task { await model.setDirectionLead(session.sessionID, !lead) }
            }
            Button("Cancel", role: .cancel) {}
        } message: { session in
            if model.assistantLeaders.contains(session.sessionID) {
                let title = session.title.isEmpty || session.title == "Claude Code"
                    ? String(localized: "New Chat [Quick Actions ea4b29]") : session.title
                Text(UnpinWords.message(String(localized: "Unpin Confirmation Message \(title) [Leader Actions 6214]"), model: model))
            } else {
                Text(String(localized: "Pin Confirmation Message [Leader Actions 6214]"))
            }
        }
        // Tasks over the drawer: a conversation picked in one opens once its sheet is gone, so the sheet's dismissal and
        // the drawer's closing never run together (docs/design/sidebar-navigation.md).
        .sheet(item: Bindable(ui).autoList, onDismiss: openPicked) { route in
            AutoConversationsView(source: route.source) { key in
                ui.picked = key
                ui.autoList = nil
            }
        }
        .sheet(isPresented: Bindable(ui).showArchived, onDismiss: openPicked) {
            ArchivedConversationsView { key in
                ui.picked = key
                ui.showArchived = false
            }
        }
        #if os(iOS)
        .fullScreenCover(isPresented: Bindable(ui).fullSearch, onDismiss: openPicked) {
            AssistantFullSearch(scope: ui.searchScope) { key in
                ui.picked = key
                ui.fullSearch = false
            } onOpenAgent: { key in
                ui.pickedAgent = key
                ui.fullSearch = false
            }
        }
        #endif
        #if DEBUG && os(iOS)
        .task { // `-openFullSearch YES`: Search over the window on any phone; only an open iPhone Duo has it otherwise (#6577)
            guard UserDefaults.standard.bool(forKey: "openFullSearch") else { return }
            try? await Task.sleep(for: .seconds(2))
            ui.fullSearch = true
        }
        #endif
    }

    private func openPicked() {
        #if os(iOS)
        if let agent = ui.pickedAgent {
            ui.pickedAgent = nil
            model.open(agent) // the Agents' chat, beside the sidebar as a notification's would (#4206)
            return
        }
        #endif
        guard let key = ui.picked else { return }
        ui.picked = nil
        #if DEBUG
        ShellTrace.shared.note("task dismissed, open")
        #endif
        open(key)
    }
}

extension AssistantSidebar {
    // MARK: Top: the name, search

    @ViewBuilder private var topBar: some View {
        if ui.searchOpen {
            HStack(spacing: Theme.Space.m) {
                searchField
                Button("Cancel") {
                    withAnimation(Theme.Motion.state) {
                        ui.query = ""
                        ui.searchOpen = false
                        searching = false
                    }
                }
                .font(Theme.Font.body)
                .lineLimit(1)
            }
        } else {
            HStack {
                if model.showsAssistantSwitcher, let current = model.currentAssistant {
                    // One installed assistant uses the switcher too; only a missing roster or current assistant keeps the persona title.
                    AssistantSwitcherHeader(manager: current, nameFont: Theme.Font.title,
                                            faceGap: part == .header ? Self.headFaceGap : Theme.Space.m, openSettings: showSettings)
                } else {
                    // A hub without a roster has no switcher: with several hubs the title keeps a hub menu under it (#12594).
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        Text(model.assistantShownName).font(isShell && !besideBar ? Theme.Font.display : Theme.Font.title).foregroundStyle(Color.primary).lineLimit(1)
                        HubSwitcher(compact: true)
                    }
                }
                Spacer(minLength: Theme.Space.s)
                if part == .header {
                    // The Sidebar layout searches from its foot, over the whole window, Persona's chats and the Agents
                    // (#4206, the owner): no field here. Beside a page, the button that folds the column.
                    if let foldColumn {
                        AssistantTopBar.circle(Theme.Icon.sidebar, label: String(localized: "Hide Sidebar"), id: "sidebar-fold-column") {
                            Haptics.tap()
                            foldColumn()
                        }
                    }
                } else if besideBar {
                    // The same button as the top bar's beside it (2026-11-04: on iPhone Duo open, this one stood lower
                    // and larger than Chat History / New Chat in the next column).
                    AssistantTopBar.circle(Theme.Icon.search, label: String(localized: "Search Chats [Assistant Sidebar 55c9e4]"), id: "assistant-sidebar-search", action: openSearch)
                } else {
                    Button(action: openSearch) {
                        ThemedIcon(Theme.Icon.search)
                            .font(Theme.Font.heading)
                            .foregroundStyle(Color.primary)
                            .scaledFrame(width: Theme.Size.avatarL, height: Theme.Size.avatarL)
                            .surface(interactive: true, in: .circle)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Search Chats [Assistant Sidebar 55c9e4]")
                }
            }
        }
    }

    private func openSearch() {
        withAnimation(Theme.Motion.state) { ui.searchOpen = true }
        searching = true
    }

    /// How far the head's face starts past the rows' start: its centre on the places' symbols, which are centred in their
    /// column after the row's padding (#4206).
    private static var headShift: CGFloat { Theme.Space.m + Theme.Sidebar.glyphColumn / 2 - Theme.Size.avatar / 2 }
    /// Between the head's face and its name: the name starts where the places' titles do.
    private static var headFaceGap: CGFloat {
        Theme.Space.m + Theme.Sidebar.glyphColumn + Theme.Sidebar.gap - headShift - Theme.Size.avatar
    }

    /// Settings on the page `AppModel.settingsPath` names (the switcher's Manage Assistants…): the way this sidebar has
    /// to Settings, else the app's own.
    private func showSettings() {
        if let openSettings { openSettings() } else { model.showSettings() }
    }

    /// The split's list column (a wide display), beside the conversation's top bar: the header is that bar's row, with
    /// the shell's places or without. The drawer keeps its own; on the Mac both sit in the title bar's row already (#830).
    private var besideBar: Bool {
        drawer == nil
    }

    /// The open search field; in the BeautifulUI style SidebarNav's.
    @ViewBuilder private var searchField: some View {
        if AppStyle.current.usesBeautifulUI {
            BUISearchField(String(localized: "Search Chats [Assistant Sidebar 55c9e4]"), text: Bindable(ui).query, focused: $searching, clearLabel: String(localized: "Clear Search"))
                .autocorrectionDisabled()
        } else {
            HStack(spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.search).font(Theme.Font.secondaryMedium).foregroundStyle(.secondary)
                TextField(String(localized: "Search Chats [Assistant Sidebar 55c9e4]"), text: Bindable(ui).query)
                    .focused($searching)
                    .submitLabel(.search)
                    .autocorrectionDisabled()
                if !ui.query.isEmpty {
                    Button { ui.query = "" } label: { ThemedIcon(Theme.Icon.clear).foregroundStyle(.tertiary) }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Clear Search")
                }
            }
            .padding(.horizontal, Theme.Space.m)
            .scaledFrame(height: Theme.Size.avatar)
            .background(Theme.Fill.chip, in: .capsule)
        }
    }

    // MARK: Spaces (#13500)

    /// The assistant has tracks and this sidebar opens spaces.
    private var showsSpaces: Bool { openSpace != nil && ExtensionSlots.shared.spaceConversationsEnabled && SpaceStore.shared.supported == true }

    /// The whole sidebar's Spaces: a heading over the spaces' rows and New space, under the places.
    @ViewBuilder private var spacesSection: some View {
        if showsSpaces {
            heading(String(localized: "Spaces [Assistant Sidebar]")) { EmptyView() }
            spaceRows
        }
    }

    /// One row per space (`SpaceStore.sidebar`, worked out when the tracks change), then New space. Health keeps its own
    /// row among the places.
    @ViewBuilder private var spaceRows: some View {
        if let openSpace {
            VStack(alignment: .leading, spacing: 0) {
                ForEach(SpaceStore.shared.sidebar) { track in
                    action(track.name, SpaceIcon.symbol(of: track), id: "sidebar-space-\(track.slug)", place: .space(track.slug)) {
                        openSpace(track.slug)
                    }
                }
                if let openNewSpace {
                    action(String(localized: "New space"), Theme.Icon.add, id: "sidebar-new-space", place: .newSpace, run: openNewSpace)
                }
            }
        }
    }

    // MARK: What to start

    private var showsInbox: Bool {
        #if os(iOS)
        !model.hiddenNavigationPages.contains(.inbox)
        #else
        true
        #endif
    }

    private var showsTodos: Bool {
        #if os(iOS)
        if model.hiddenNavigationPages.contains(.todos) { return false }
        #endif
        return TodoStore.shared.isSupported
    }

    @ViewBuilder private func actions(_ all: [Session]) -> some View {
        if let openInbox, let onMachines {
            // The shell's: the places to go (the main conversation is the row above them, `mainItem`).
            VStack(alignment: .leading, spacing: 0) {
                meetingRow
                if showsInbox {
                    action(String(localized: "Inbox"), Theme.Icon.inbox, id: "sidebar-inbox", count: InboxStore.shared.unreadCount, place: .inbox, run: openInbox)
                }
                starredRow
                if showsTodos {
                    todosRow { (openTodos ?? openInbox)() }
                }
                action(String(localized: "Scheduled Tasks"), Theme.Icon.schedule, place: .routines, run: openRoutines)
                action(String(localized: "Knowledge"), Theme.Icon.knowledge, place: .knowledge, run: openKnowledge)
                if let openHealth, hasHealth { action(String(localized: "Health"), Theme.Icon.health, id: "sidebar-health", place: .health, run: openHealth) }
                action(String(localized: "Machines"), Theme.Icon.machines, id: "sidebar-machines", place: .machines, run: onMachines)
                if let openMessages {
                    action(String(localized: "Messages [Quote Views 4da199]"), Theme.Icon.feishuChats, id: "sidebar-messages", place: .messages, run: openMessages)
                }
                extensionRows
            }
        } else {
            phoneActions
        }
    }

    private var phoneActions: some View {
        VStack(alignment: .leading, spacing: 0) {
            action(String(localized: "New Chat [Quick Actions ea4b29]"), Theme.Icon.conversation, id: "sidebar-new", run: newConversation)
            meetingRow
            starredRow
            action(String(localized: "Scheduled Tasks"), Theme.Icon.schedule, run: openRoutines)
            action(String(localized: "Knowledge"), Theme.Icon.knowledge, run: openKnowledge)
            if let openHealth, hasHealth { action(String(localized: "Health"), Theme.Icon.health, id: "sidebar-health", run: openHealth) }
            extensionRows
        }
    }

    @ViewBuilder private var starredRow: some View {
        #if os(iOS)
        if let openStarred, let count = StarredStore.shared.count {
            action(String(localized: "Starred"), Theme.Icon.starredMessage, id: "sidebar-starred", count: count, place: .starred, run: openStarred)
        }
        #endif
    }

    /// First among the places: Meetings (#8731; Recordings before), a place like the rows under it, so a tap opens the page
    /// and its Start Meeting starts one (#5517: as Record, the tap recorded at once). While something records, the row says
    /// what (`MeetingWords.running`) and opens it.
    @ViewBuilder private var meetingRow: some View {
        #if os(iOS)
        if let openRecordingsPage {
            action(MeetingWords.running ?? MeetingWords.place,
                   Theme.Icon.meeting, id: "sidebar-meeting-recording", place: .recordings, run: openRecordingsPage)
        }
        #endif
    }

    /// The extensions' pages on the main navigation (nav_page, docs/extensions/extensions.md「UI slots」): one row each, its icon,
    /// title and badge, in the hub's order. Nothing with a hub without slots.
    @ViewBuilder private var extensionRows: some View {
        if let onOpenExtension, ExtensionSlots.shared.available {
            ForEach(ExtensionSlots.shared.pages, id: \.scopedID) { p in
                extensionRow(p) { onOpenExtension(ExtensionNavRoute(p)) }
            }
        }
    }

    nonisolated static func isExtensionSelected(_ place: ExtensionUIPlace, shown: ShellPage?) -> Bool {
        guard case .extensionPage(let route) = shown else { return false }
        return route.id == place.scopedID
    }

    @ViewBuilder private func extensionRow(_ p: ExtensionUIPlace, run: @escaping () -> Void) -> some View {
        let symbol = ExtensionSlots.icon(p.icon)
        let selected = Self.isExtensionSelected(p, shown: shownPlace)
        Group {
            if AppStyle.current.usesBeautifulUI {
                BUISidebarRow(p.title, symbol: Theme.Icon.styled(symbol), isActive: selected, action: run) {
                    if let b = p.badge { ExtensionBadgeView(badge: b) }
                }
            } else {
                Button(action: run) {
                    HStack(spacing: Theme.Sidebar.gap) {
                        Image(systemName: symbol).resizable().scaledToFit().fontWeight(isShell ? .medium : .regular)
                            .foregroundStyle(Color.primary)
                            .scaledFrame(width: Theme.Sidebar.glyph, height: Theme.Sidebar.glyph)
                            .scaledFrame(width: Theme.Sidebar.glyphColumn)
                        Text(p.title).font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                        Spacer(minLength: 0)
                        if let b = p.badge { ExtensionBadgeView(badge: b) }
                    }
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.vertical, Theme.Sidebar.rowPadding)
                    .scaledFrame(minHeight: Theme.Sidebar.row)
                    .background {
                        if selected { SidebarSelectedShade() }
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(p.title)
        .accessibilityValue(p.badge?.text ?? "")
        .accessibilityAddTraits(selected ? [.isButton, .isSelected] : .isButton)
        .accessibilityAction { run() }
        .accessibilityIdentifier("sidebar-extension-\(p.owner)-\(p.id)")
    }


    /// The sidebar's first row in every layout: the main conversation, as "Main Chat" with the main conversation's symbol
    /// in the places' symbol column (#9973). The assistant's face and name are the head's right above it; this row had
    /// them too (#2004, #3203, #6401) and the iPad showed the same face three times. Its state
    /// (working, waiting, unread) on the right, as on a conversation's row. It shows while its session is not in the
    /// list too (not loaded yet, or listed by no machine now, #4606): it still opens it, or with no main conversation
    /// yet the new-conversation page, whose first one becomes it. Off until the hub's assistant config came: a
    /// conversation started before would not become the main one. No swipe: it cannot be archived.
    private func mainItem(_ all: [Session]) -> some View {
        let key = model.assistantMain
        let main = all.first { $0.key == key }
        let selected = key != nil && key == current
        return Button { if let key { open(key) } else { newConversation() } } label: {
            HStack(spacing: Theme.Sidebar.gap) {
                // Where the places below have their symbol, drawn as theirs.
                Theme.Icon.image(Theme.Icon.home).resizable().scaledToFit() // resizable: an Image, not ThemedIcon
                    .fontWeight(isShell ? .medium : .regular)
                    .foregroundStyle(Color.primary)
                    .scaledFrame(width: Theme.Sidebar.glyph, height: Theme.Sidebar.glyph)
                    .scaledFrame(width: Theme.Sidebar.glyphColumn)
                Text("Main Chat").font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                Spacer(minLength: 0)
                if let main { RowStateMark(state: main.state) }
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Sidebar.rowPadding)
            .scaledFrame(minHeight: Theme.Sidebar.row)
            .background {
                if selected { SidebarSelectedShade() }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel([String(localized: "Main Chat"), main.flatMap { stateWord($0) }].compactMap { $0 }.joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label")))
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityIdentifier("sidebar-main")
        .disabled(key == nil && model.assistantConfig == nil)
        .contextMenu {
            if let key {
                MainConversationMenu(key: key) // Compact context, New Main Chat… (#1762)
                Button {
                    UIPasteboard.general.string = key.sessionID
                    model.toast = Toast(String(localized: "Session ID Copied"), style: .success)
                } label: { Label("Copy Session ID [Assistant Sidebar f24fb3]", systemImage: Theme.Icon.id) }
                Button {
                    UIPasteboard.general.string = ChatTrace.shared.text(key) // structure only, to paste into a report
                    model.toast = Toast(String(localized: "Diagnostics copied"), style: .success)
                } label: { Label("Copy Diagnostics", systemImage: Theme.Icon.diagnostics) }
                if let main, main.status.isLive {
                    Button(role: .destructive) { ui.ending = main } label: { Label("End…", systemImage: Theme.Icon.end) }
                }
            }
        }
    }

    /// To-dos: on the V1 path (#10465) with its places folded under it (`TodoPlaceRows`), else the one row it always was.
    @ViewBuilder private func todosRow(run: @escaping () -> Void) -> some View {
        #if os(iOS)
        if TodosV1.shared.isOn && !TodoWorkspace.shared.holdsToday {
            TodoPlaceRows(shown: shownPlace == .todos, open: run, folded: $todosFolded) { selected, run, fold in
                todosHead(selected: selected, run: run, fold: fold)
            } row: { item in
                todoPlaceRow(item)
            }
        } else {
            action(String(localized: "To-dos"), Theme.Icon.todo, id: "sidebar-todos", count: TodoStore.shared.sidebarCount, place: .todos, run: run)
        }
        #else
        action(String(localized: "To-dos"), Theme.Icon.todo, id: "sidebar-todos", count: TodoStore.shared.sidebarCount, place: .todos, run: run)
        #endif
    }

    #if os(iOS)
    /// The To-dos row over its places: its chevron beside it, not in it (the row keeps its own tap), and one shade
    /// across both, as a Pinned direction's row (`AssistantSidebarDirection`). In BeautifulUI the chevron lies over the
    /// row's end, where the row keeps room for it, so the row keeps its siblings' height.
    @ViewBuilder private func todosHead(selected: Bool, run: @escaping () -> Void, fold: TodoPlacesFold?) -> some View {
        let title = String(localized: "To-dos")
        let count = TodoStore.shared.sidebarCount
        if let fold {
            if AppStyle.current.usesBeautifulUI {
                BUISidebarRow(title, symbol: Theme.Icon.styled(Theme.Icon.todo), isActive: selected, action: run) {
                    if count > 0 { CountBadge(count: count, look: .needs) }
                    Color.clear.scaledFrame(width: Theme.Size.tapTarget)
                }
                .accessibilityIdentifier("sidebar-todos")
                .overlay(alignment: .trailing) { fold.overRow() }
            } else {
                plainAction(title, Theme.Icon.todo, id: "sidebar-todos", count: count, run: run)
                    .accessibilityAddTraits(selected ? .isSelected : [])
                    .safeAreaInset(edge: .trailing, spacing: Theme.Space.flush) { fold }
                    .background {
                        if selected { SidebarSelectedShade() }
                    }
            }
        } else {
            action(title, Theme.Icon.todo, id: "sidebar-todos", count: count, place: .todos, run: run)
        }
    }

    /// A To-dos place under To-dos: the sidebar's row, set in so its symbol is where To-dos' words start (BeautifulUI's
    /// row by its own measures), its part of N said in words. Its own function, so the rows every sidebar draws keep
    /// their stack frames (`MainPagesStackTests`).
    @ViewBuilder private func todoPlaceRow(_ item: TodoPlaceRowItem) -> some View {
        let count = item.count > 0 ? StateName.count(.needsYou, item.count) : ""
        Group {
            if AppStyle.current.usesBeautifulUI {
                BUISidebarRow(item.title, symbol: Theme.Icon.styled(item.symbol), isActive: item.selected, indent: 1, action: item.run) {
                    if item.count > 0 { CountBadge(count: item.count, look: .needs) }
                }
                .accessibilityLabel(item.title)
                .accessibilityValue(count)
                .accessibilityIdentifier(item.identifier)
            } else {
                HStack(spacing: Theme.Sidebar.gap) {
                    Color.clear.scaledFrame(width: Theme.Sidebar.glyphColumn)
                    plainAction(item.title, item.symbol, id: item.identifier, count: item.count, selected: item.selected, run: item.run)
                        .accessibilityLabel(item.title)
                        .accessibilityValue(count)
                }
            }
        }
        .padding(.top, item.gapBefore ? Theme.Space.m : Theme.Space.flush)
    }
    #endif

    /// A place to go; `place`: what it shows beside the Sidebar layout's sidebar, its row shaded while it does.
    @ViewBuilder private func action(_ title: String, _ symbol: String, id: String? = nil, count: Int = 0, place: ShellPage? = nil,
                                     run: @escaping () -> Void) -> some View {
        let selected = place != nil && place == shownPlace
        if AppStyle.current.usesBeautifulUI {
            // SidebarNav's primary items.
            BUISidebarRow(title, symbol: Theme.Icon.styled(symbol), isActive: selected, action: run) {
                if count > 0 { CountBadge(count: count, look: .needs) }
            }
            .accessibilityIdentifier(id ?? "sidebar-" + title)
        } else {
            plainAction(title, symbol, id: id, count: count, selected: selected, run: run)
        }
    }

    private func plainAction(_ title: String, _ symbol: String, id: String? = nil, count: Int = 0, selected: Bool = false,
                             run: @escaping () -> Void) -> some View {
        Button(action: run) {
            HStack(spacing: Theme.Sidebar.gap) {
                // One box for every symbol: a wide one (Knowledge's books) scales down into it instead of pushing its words.
                Image(systemName: symbol).resizable().scaledToFit().fontWeight(isShell ? .medium : .regular).foregroundStyle(Color.primary)
                    .scaledFrame(width: Theme.Sidebar.glyph, height: Theme.Sidebar.glyph)
                    .scaledFrame(width: Theme.Sidebar.glyphColumn)
                Text(title).font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                Spacer(minLength: 0)
                if count > 0 { CountBadge(count: count, look: .needs) }
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Sidebar.rowPadding)
            .scaledFrame(minHeight: Theme.Sidebar.row)
            .background {
                if selected { SidebarSelectedShade() }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityIdentifier(id ?? "sidebar-" + title)
    }

    // MARK: Conversations

    @ViewBuilder private func conversations(_ all: [Session], archived: Int) -> some View {
        let main = model.assistantMain
        // The leader conversations are the Directions group, in the hub's order, under each direction's name
        // (docs/assistant/unified-entry.md "Leader conversations"); then Recent, the newest few of the other chats,
        // whoever opened them (#4873, #5195); everything else is one tap away in All Chats (#4331).
        let directions = AssistantSidebarGroups.directions(
            all, leaders: model.assistantLeaders, main: main, outside: model.outsideDirections)
        if !directions.isEmpty {
            heading(String(localized: "Directions")) { EmptyView() }
            // Each tappable direction folds open to its to-dos (#4352). A targetless outside direction is deliberately
            // plain content: no button, disclosure, conversation menu or swipe (#8236). The secretary group has no row.
            ForEach(directions) { entry in
                let selected = entry.session.map { $0.key == current }
                    ?? entry.target.map { model.inHub($0) == current } ?? false
                let name = model.directionNames[entry.id] ?? entry.session?.title ?? entry.outside?.name ?? ""
                AssistantSidebarDirection(
                    direction: entry.canDisclose ? model.directionTree?.leader(entry.id) : nil,
                    id: entry.id, name: name, isShell: isShell, drawer: drawer,
                    selected: selected, selectedFill: Self.selectedFill, lead: entry.session?.key) { shades in
                    if let session = entry.session {
                        row(session, named: model.directionNames[entry.id], drawsShade: shades)
                    } else {
                        outsideRow(entry, named: name, selected: selected, drawsShade: shades)
                    }
                }
            }
        }
        let recent = AssistantSidebarGroups.recent(all, leaders: model.assistantLeaders, main: main,
                                                   background: model.assistantConfig?.backgroundSupervisor,
                                                   tasks: taskChats.known(on: model.api), withTasks: model.recentTaskChats)
        if !recent.isEmpty {
            recentHeading(recent)
        }
        allChatsRow
        archivedRow(archived)
    }

    /// All Chats (#4331): the full list with its filters and search (`AutoConversationsView`). It works on every hub
    /// (its rows come from the conversations the app already has); a hub that predates the origins only lacks the
    /// source filters there.
    private var allChatsRow: some View {
        Button { ui.autoList = AutoConversationsRoute(source: nil) } label: {
            HStack(spacing: Theme.Space.m) {
                if !isShell {
                    ThemedIcon(Theme.Icon.list).font(Theme.Font.label).foregroundStyle(.secondary)
                        .scaledFrame(width: Theme.Sidebar.mark, height: Theme.Sidebar.mark)
                }
                Text("All Chats").font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                Spacer(minLength: Theme.Space.xs)
                ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Sidebar.rowPadding)
            .scaledFrame(minHeight: Theme.Sidebar.row)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("sidebar-all-chats")
    }

    /// Archived (N) at the end of the list when there are any: where swiped-away conversations are.
    @ViewBuilder private func archivedRow(_ n: Int) -> some View {
        if n > 0 {
            Button { ui.showArchived = true } label: {
                Label("Archived (\(n))", systemImage: Theme.Icon.archive)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .padding(.horizontal, Theme.Space.m)
                    .padding(.vertical, Theme.Space.m)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("sidebar-archived")
        }
    }

    /// The search's rows: the local matches, and once the manager answers, its results first (archived chats too).
    private func searchRows(_ all: [Session]) -> [AssistantSearchMerge.Row] {
        let local = AssistantSidebarSearch.filter(all, ui.query, names: model.directionNames)
        guard let answer = ui.chatSearch.answer(for: ui.query) else { return local.map { AssistantSearchMerge.Row(session: $0) } }
        return AssistantSearchMerge.rows(local: local, answer: answer, known: all + model.archivedConversations)
    }

    @ViewBuilder private func results(_ hits: [AssistantSearchMerge.Row]) -> some View {
        if hits.isEmpty {
            VStack(spacing: Theme.Space.sm) {
                ThemedIcon(Theme.Icon.search).font(Theme.Font.title).foregroundStyle(.tertiary)
                Text("No Results for “\(ui.query)”").font(Theme.Font.secondary).foregroundStyle(.secondary)
            }
            .frame(maxWidth: .infinity)
            .padding(.top, 40)
        } else {
            heading(String(localized: "\(hits.count) Chats [Assistant Sidebar a72a2a]")) { EmptyView() }
            if ui.chatSearch.answer(for: ui.query)?.partial == true {
                Text("Still going through older chats; some may not show yet.")
                    .font(Theme.Font.label).foregroundStyle(.secondary)
                    .padding(.horizontal, Theme.Space.m)
                    .accessibilityIdentifier("sidebar-search-partial")
            }
            ForEach(hits) { r in row(r.session, named: model.directionNames[r.session.sessionID], said: r.hit) }
        }
    }

    /// A section's name, small and gray, with its ⋯ menu on the right when it has one.
    @ViewBuilder private func heading<Menu: View>(_ text: String, @ViewBuilder menu: () -> Menu) -> some View {
        if AppStyle.current.usesBeautifulUI {
            let items = menu()
            BUISidebarHeading(text) {
                if !HeadingMenu.isEmpty(items) {
                    SwiftUI.Menu { items } label: {
                        ThemedIcon(Theme.Icon.more).font(Theme.Font.captionStrong)
                            .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.wellXS)
                            .contentShape(.rect)
                    }
                    .tint(Color.secondary)
                    .labelOnlyMenu()
                    .accessibilityLabel(String(localized: "More actions for \(text) [Assistant Sidebar f357af]"))
                }
            }
            .scaledFrame(minHeight: HeadingMenu.minHeight(Menu.self))
            .padding(.top, Theme.Space.l)
            .padding(.bottom, Theme.Space.xs)
        } else {
            plainHeading(text, menu: menu)
        }
    }

    private func plainHeading<Menu: View>(_ text: String, @ViewBuilder menu: () -> Menu) -> some View {
        let items = menu()
        return HStack {
            Text(text).font(Theme.Font.caption).foregroundStyle(Color.secondary).accessibilityAddTraits(.isHeader)
            Spacer(minLength: 0)
            if !HeadingMenu.isEmpty(items) {
                SwiftUI.Menu { items } label: {
                    ThemedIcon(Theme.Icon.more).font(Theme.Font.captionStrong).foregroundStyle(.tertiary)
                        .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.wellXS)
                        .contentShape(.rect)
                }
                .tint(Color.secondary) // a gray ⋯, not the accent
                .labelOnlyMenu()
                .accessibilityLabel(String(localized: "More actions for \(text) [Assistant Sidebar f357af]"))
            }
        }
        .scaledFrame(minHeight: HeadingMenu.minHeight(Menu.self))
        .padding(.leading, Theme.Space.m)
        .padding(.top, Theme.Space.l)
        .padding(.bottom, Theme.Space.xs)
    }

    /// The Recent heading and its rows (#6228): the heading folds them, like the web's and Android's, and the choice is
    /// kept on this device. The open conversation's row still shows while folded, so folding never hides where a
    /// message lands.
    @ViewBuilder private func recentHeading(_ recent: [Session]) -> some View {
        let holdsCurrent = current.map { key in recent.contains { $0.key == key } } ?? false
        let open = !recentFolded || holdsCurrent
        Button {
            withAnimation(Theme.Motion.state) { recentFolded.toggle() }
            Haptics.tap()
        } label: {
            HStack {
                Text("Recent").font(Theme.Font.caption).foregroundStyle(Color.secondary).lineLimit(1)
                Spacer(minLength: 0)
                ThemedIcon(Theme.Icon.disclosure)
                    .font(Theme.Font.captionStrong)
                    .foregroundStyle(Color.secondary)
                    .rotationEffect(.degrees(open ? 90 : 0))
                    .animation(Theme.Motion.state, value: open)
            }
            .padding(.leading, Theme.Space.m)
            .padding(.top, Theme.Space.l)
            .padding(.bottom, Theme.Space.xs)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(String(localized: "Recent"))
        .accessibilityValue(open ? String(localized: "Expanded") : String(localized: "Collapsed [Assistant Sidebar e64447]"))
        .accessibilityAction(named: open ? String(localized: "Collapse") : String(localized: "Expand")) {
            withAnimation(Theme.Motion.state) { recentFolded.toggle() }
        }
        .accessibilityIdentifier("sidebar-recent-fold")
        if open {
            ForEach(recent) { row($0) }
        }
    }

    /// A conversation's title on one line. A 「1101 | 修复 | 主题」 title (docs/assistant/assistant.md「Chat titles」) has its day and
    /// kind in the secondary color: the topic is what the eye finds, and what a narrow drawer cuts at the end.
    @ViewBuilder private func titleText(_ title: String, named: Bool) -> some View {
        if named, let t = ConversationTitle(title) {
            Text("\(Text(t.head).foregroundStyle(Color.secondary))\(t.topic)")
                .font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
        } else {
            Text(title).font(Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
        }
    }

    /// A conversation: a round mark (Persona's face for the main one), its title on one line, its state on the right.
    /// `named`: a direction's name, shown instead of its leader conversation's title.
    /// `drawsShade` false: a direction's row with its fold chevron beside it, which draws the shade across both (#4352).
    /// `said`: the message a search found it by (#6284), shown under its title and opened at.
    private func row(_ s: Session, named: String? = nil, drawsShade: Bool = true, said: AssistantSearchAnswer.Hit? = nil) -> some View {
        let selected = s.key == current
        let isMain = s.key == model.assistantMain
        // The main conversation and the hub's built-in background supervisor (#5083) stay: no Archive, Pin or End.
        let builtIn = model.isBackgroundSupervisor(s.sessionID)
        let kept = isMain || builtIn
        let title = isMain ? String(localized: "Main Chat") : named ?? (s.title.isEmpty || s.title == "Claude Code" ? String(localized: "New Chat [Quick Actions ea4b29]") : s.title)
        let preview = s.lastText.map { Fmt.plain($0).replacingOccurrences(of: "\n", with: " ") } ?? ""
        let direction = model.assistantLeaders.contains(s.sessionID) ? model.directionTree?.leader(s.sessionID) : nil
        let counts = direction?.counts.label
        // One activity mark for the whole row (#6230): it spins whether this conversation runs its own turn or anything
        // under it runs (the tree's `running`, an Agent at work, #6626); the row's own needs-you / unread dot still takes
        // precedence. It is the only way the row says so: no "N working" beside it.
        let leadWorking = direction?.running ?? false
        // What opened it when Persona did (#5195): the rule All Chats' filters use, so its mark and its filter agree. One
        // an extension opened is marked with that extension's own icon when it can be (#10358).
        let source = isMain ? nil : AutoConversations.source(of: s, tasks: taskChats.known(on: model.api))
        let opener = ConversationOpener.of(s)
        return Button {
            if let said { model.messageJump = MessageJump(key: s.key, item: said.itemID) }
            open(s.key)
        } label: {
            HStack(spacing: Theme.Space.m) {
                // The shell's list is ChatGPT's: titles only, all in one column (the marks made it busy); the main
                // conversation (only in search results here) keeps its symbol, as its first row has it (#9973).
                if isMain {
                    ThemedIcon(Theme.Icon.home).font(Theme.Font.body).foregroundStyle(.secondary)
                        .scaledFrame(width: isShell ? Theme.Sidebar.glyphColumn : Theme.Sidebar.mark)
                } else if isShell {
                    EmptyView()
                } else {
                    ConversationMark(id: s.sessionID)
                }
                if let said {
                    VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                        titleText(title, named: !isMain)
                        SearchSaidLine(hit: said)
                            .accessibilityIdentifier("sidebar-search-said")
                    }
                } else {
                    titleText(title, named: !isMain)
                }
                ConversationSourceMark(source: source, channels: s.channels, opener: opener) // what opened it, or its latest message's channel
                Spacer(minLength: Theme.Space.xs)
                // What waits on you under a direction ("N need you", #4331); what runs is the mark after it (#6626).
                directionStatus(counts, state: s.state, running: leadWorking)
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Sidebar.rowPadding)
            .scaledFrame(minHeight: Theme.Sidebar.row)
            .background {
                if selected && drawsShade {
                    SidebarSelectedShade()
                }
            }
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .tag(s.key)
        // a row a search found by a message says the manager's whole snippet in the last message's place (#6284)
        .sourceLabel([title, counts, isMain && s.title != title ? s.title : nil], source: source, opener: opener,
                     [stateWord(s, under: leadWorking), said?.snippet ?? (preview.isEmpty ? nil : preview)],
                     separator: String(localized: ", ", comment: "Separator between parts of an accessibility label"))
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityIdentifier(isMain ? "sidebar-main" : "sidebar-conversation")
        .swipeActions(edge: .trailing, allowsFullSwipe: true) {
            if !kept {
                Button { Task { await model.archiveConversation(s.key, true) } } label: {
                    Label("Archive", systemImage: Theme.Icon.archive)
                }
                .tint(Theme.accent)
            }
        }
        .background { if !kept { SwipeRowMark() } } // the drawer leaves a drag to the left on it to the row
        .contextMenu {
            if isMain { MainConversationMenu(key: s.key) } // Compact context, New Main Chat… (#1762)
            if !kept {
                // A direction is the hub's, for every device (#4331): the long press makes a conversation one, or ends
                // the direction there (#4331, `make` / `end`). It asks a second time first (#6214).
                let lead = model.assistantLeaders.contains(s.sessionID)
                Button {
                    ui.leaderConfirm = s
                } label: {
                    lead ? Label("Unpin [Assistant Sidebar b7c40e]", systemImage: Theme.Icon.unpin)
                        : Label("Pin [Assistant Sidebar 6d1f2a]", systemImage: Theme.Icon.pin)
                }
                // File it under a direction, or on its own (#11516): only where the hub files sessions.
                MoveToMenu(key: s.key, title: title, state: s.state)
            }
            if !isMain {
                Button { ui.renaming = s } label: { Label("Rename… [Assistant Sidebar ec4cd0]", systemImage: Theme.Icon.edit) }
                if !builtIn {
                    Button { Task { await model.archiveConversation(s.key, true) } } label: { Label("Archive", systemImage: Theme.Icon.archive) }
                }
            }
            Button {
                UIPasteboard.general.string = s.sessionID
                model.toast = Toast(String(localized: "Session ID Copied"), style: .success)
            } label: { Label("Copy Session ID [Assistant Sidebar f24fb3]", systemImage: Theme.Icon.id) }
            Button {
                UIPasteboard.general.string = ChatTrace.shared.text(s.key) // structure only, to paste into a report
                model.toast = Toast(String(localized: "Diagnostics copied"), style: .success)
            } label: { Label("Copy Diagnostics", systemImage: Theme.Icon.diagnostics) }
            if s.status.isLive, !builtIn {
                Button(role: .destructive) { ui.ending = s } label: { Label("End…", systemImage: Theme.Icon.end) }
            }
        } preview: {
            // The long-press preview: what it last said (the row itself shows only the title).
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                Text(title).font(Theme.Font.heading).lineLimit(2)
                if !preview.isEmpty { Text(preview).font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(6) }
                Text(Fmt.relative(s.updatedAt)).font(Theme.Font.label).foregroundStyle(.tertiary)
            }
            .padding(Theme.Space.l)
            .frame(width: Theme.Size.box280, alignment: .leading)
        }
    }

    /// The direction status shared by conversation-backed and outside rows. `state` is the row's own conversation state;
    /// an outside row passes idle so its bound Agent's state never replaces the tree's running value (#8236).
    @ViewBuilder private func directionStatus(_ counts: String?, state: AgentState, running: Bool) -> some View {
        if let counts {
            Text(counts).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
        }
        RowStateMark(state: state, working: running)
    }

    /// An outside reporting direction. Its label deliberately reuses the conversation row's mark, title, metrics and
    /// status, but none of `row`'s conversation tag, preview, menu or swipe actions. Only a complete Agent target gets
    /// a button; targetless content has no button trait and tapping it does nothing.
    @ViewBuilder private func outsideRow(
        _ entry: AssistantSidebarGroups.DirectionEntry, named: String, selected: Bool, drawsShade: Bool
    ) -> some View {
        if let target = entry.target {
            Button {
                drawer?.close()
                openTodoAgentChat(model.inHub(target), in: model)
            } label: {
                outsideLabel(entry, named: named, selected: selected, drawsShade: drawsShade)
            }
            .buttonStyle(.plain)
            .contentShape(.rect)
            .accessibilityAddTraits(selected ? .isSelected : [])
            .accessibilityIdentifier("sidebar-outside-direction")
        } else {
            outsideLabel(entry, named: named, selected: false, drawsShade: false)
        }
    }

    private func outsideLabel(
        _ entry: AssistantSidebarGroups.DirectionEntry, named: String, selected: Bool, drawsShade: Bool
    ) -> some View {
        let counts = entry.outside?.counts.label
        let running = entry.outside?.running ?? false
        return HStack(spacing: Theme.Space.m) {
            if !isShell { ConversationMark(id: entry.id) }
            titleText(named, named: true)
            Spacer(minLength: Theme.Space.xs)
            directionStatus(counts, state: .idle, running: running)
        }
        .padding(.horizontal, Theme.Space.m)
        .padding(.vertical, Theme.Sidebar.rowPadding)
        .scaledFrame(minHeight: Theme.Sidebar.row)
        .background {
            if selected && drawsShade { SidebarSelectedShade() }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([named, counts, running ? AgentState.working.label : nil]
            .compactMap { $0 }.joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label")))
        .accessibilityIdentifier("sidebar-outside-direction")
    }

    /// The row's state as VoiceOver says it: its mark's (`RowStateMark`), so a lead that spins for what runs under it
    /// (`under`) says Working too.
    private func stateWord(_ s: Session, under: Bool = false) -> String? {
        switch s.state {
        case .working, .needsYou, .unread: s.state.label
        default: under ? AgentState.working.label : s.updatedAt.map { Fmt.relative($0) }
        }
    }

    // MARK: Foot: Persona, Settings

    private var machineID: String { model.assistantConfig?.machineID ?? model.hubMachineID }

    /// Persona's face and name with where it runs (a tap: its profile), Settings on the right (the tab bar is hidden while
    /// the drawer is out).
    @ViewBuilder private var footer: some View {
        if isShell {
            shellFooter
            // The iPad's column (the shell's places beside the conversation): Persona's face and where it runs stay
            // at the foot, under the shell's New Chat + Settings (2026-11-05 user: the bottom left is fine).
            if drawer == nil {
                AssistantProfileFooter(machineID: machineID, openProfile: openProfile, openSettings: nil)
            }
        } else {
            AssistantProfileFooter(machineID: machineID, openProfile: openProfile, openSettings: openSettings)
        }
    }

    /// The foot's New Chat pill, in the Theme Color (#1312).
    private var newChatLabel: some View {
        Label("New Chat [Quick Actions ea4b29]", systemImage: Theme.Icon.compose)
            .font(Theme.Font.bodyStrong)
            .foregroundStyle(.white)
            .lineLimit(1)
            .fixedSize()
            .padding(.horizontal, Theme.Space.xl)
            .scaledFrame(height: Theme.Size.avatarL)
            .background(Theme.accent, in: .capsule)
            .contentShape(.capsule)
    }

    /// A round button of the Sidebar layout's foot, in the capsule they share.
    private func footButton(_ symbol: String, label: String, id: String, action: @escaping () -> Void) -> some View {
        Button(action: action) {
            ThemedIcon(symbol)
                .font(Theme.Font.heading)
                .foregroundStyle(Color.primary)
                .scaledFrame(width: Theme.Size.avatarL, height: Theme.Size.avatarL)
                .contentShape(.circle)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
        .accessibilityIdentifier(id)
    }

    /// The Sidebar layout's search, over the whole window: on the Agents while an Agent's chat is in front (the tab
    /// follows what is in front there), else on Persona's chats (#4206).
    private func openFullSearch() {
        #if os(iOS)
        ui.searchScope = model.selectedTabNow == .chats ? .agents : .chats
        ui.fullSearch = true
        #endif
    }

    /// The shell's foot, ChatGPT's: a New Chat pill in the Theme Color on the left (#1312), Settings in a glass circle on the right.
    private var shellFooter: some View {
        HStack(spacing: Theme.Space.m) {
            Button(action: newConversation) {
                // Its name while it fits, else its icon alone: beside Search and Settings in the open Duo's 320 pt
                // column the name was cut to "…" (#4206; a button's words never wrap or cut).
                ViewThatFits(in: .horizontal) {
                    newChatLabel.labelStyle(.titleAndIcon)
                    newChatLabel.labelStyle(.iconOnly)
                }
            }
            .buttonStyle(.plain)
            .accessibilityLabel("New Chat [Quick Actions ea4b29]")
            .accessibilityIdentifier("sidebar-new")
            .layoutPriority(1) // offered the row's room before the spacer, so the name shows wherever it fits
            Spacer(minLength: Theme.Space.s)
            if part == .footer, let openSettings {
                // The Sidebar layout's foot (#4206, the owner): Search and Settings in one capsule. Search is the
                // column's one, over the whole window, Persona's chats and the Agents.
                HStack(spacing: Theme.Space.flush) {
                    footButton(Theme.Icon.search, label: String(localized: "Search"), id: "sidebar-search", action: openFullSearch)
                    footButton(Theme.Icon.settings, label: String(localized: "Settings"), id: "sidebar-settings", action: openSettings)
                        .overlay(alignment: .topTrailing) { if onOpenExtension == nil { ExtensionSettingsBadge() } }
                }
                .surface(interactive: true, in: .capsule)
            } else if let openSettings {
                Button(action: openSettings) {
                    ThemedIcon(Theme.Icon.settings)
                        .font(Theme.Font.heading)
                        .foregroundStyle(Color.primary)
                        .scaledFrame(width: Theme.Size.avatarL, height: Theme.Size.avatarL)
                        .contentShape(.circle)
                }
                .buttonStyle(.plain)
                .surface(interactive: true, in: .circle)
                // The plugins' entries are in Settings (docs/extensions/extensions.md「UI slots」, badges): their badges add up here, unless the
                // sidebar lists the entries itself.
                .overlay(alignment: .topTrailing) { if onOpenExtension == nil { ExtensionSettingsBadge() } }
                .accessibilityLabel("Settings")
                .accessibilityIdentifier("sidebar-settings")
            }
        }
        .padding(.horizontal, Theme.Space.xl)
        .padding(.vertical, Theme.Space.m)
    }
}

/// Persona's face and name with where it runs, Settings on the right: a view of its own, so the machines' heartbeats (and
/// the page coming to the front) draw this row again, not the conversations' list.
private struct AssistantProfileFooter: View {
    @Environment(AppModel.self) private var model
    @Environment(\.tabIsActive) private var inFront
    let machineID: String
    let openProfile: () -> Void
    let openSettings: (() -> Void)?

    var body: some View {
        // Behind another section (the Mac keeps the page): the machine as it is now, not each push's heartbeat.
        let machine = model.machine(model.hubRef(machineID), inFront: inFront)
        let online = machine?.online ?? false
        let state = online ? String(localized: "Online") : String(localized: "Offline [Machine Entity be1b4f]")
        return VStack(spacing: 0) {
            Divider()
            HStack(spacing: Theme.Space.m) {
                Button(action: openProfile) {
                    HStack(spacing: Theme.Space.m) {
                        PersonaAvatar(size: Theme.Size.avatarL)
                        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                            HStack(spacing: Theme.Space.xs) {
                                Text(model.assistantShownName).font(Theme.Font.bodyStrong).foregroundStyle(Color.primary).lineLimit(1)
                                ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
                            }
                            HStack(spacing: Theme.Space.xs) {
                                Circle().fill(online ? Theme.positive : Color.secondary.opacity(0.5))
                                    .scaledFrame(width: Theme.Size.dotXS, height: Theme.Size.dotXS)
                                Text(online ? String(localized: "Online · \(machine?.name ?? machineID)") : String(localized: "Offline · \(machine?.name ?? machineID)")).lineLimit(1)
                            }
                            .font(Theme.Font.label)
                            .foregroundStyle(.secondary)
                        }
                    }
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityElement(children: .combine)
                .accessibilityLabel("\(model.assistantShownName), \(state), view profile")
                Spacer(minLength: Theme.Space.s)
                if let openSettings {
                    Button(action: openSettings) {
                        ThemedIcon(Theme.Icon.settings).font(Theme.Font.title).foregroundStyle(Color.primary)
                            .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                            .contentShape(.rect)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Settings")
                }
            }
            .padding(.horizontal, Theme.Space.l)
            .padding(.vertical, Theme.Space.m)
        }
    }
}


/// A conversation's round mark in the sidebar: a chat bubble in a soft color picked by its id, so neighbours differ.
struct ConversationMark: View {
    var id: String

    private static let hues = [Theme.Hue.orange, Theme.Hue.purple, Theme.Hue.blue, Theme.Hue.green]

    var body: some View {
        let hue = Self.hues[Int(id.unicodeScalars.reduce(0) { ($0 &* 31 &+ Int($1.value)) & 0xFFFF }) % Self.hues.count]
        Circle()
            .fill(hue.opacity(0.14))
            .overlay(Circle().stroke(hue.opacity(0.25), lineWidth: Theme.Size.hairline))
            .scaledFrame(width: Theme.Sidebar.mark, height: Theme.Sidebar.mark)
            .overlay(ThemedIcon(Theme.Icon.conversation).font(Theme.Font.label).foregroundStyle(hue))
            .accessibilityHidden(true)
    }
}

/// The sidebar's conversations by day, newest first: Today, Yesterday, Previous 7 Days, Earlier (and those without a time).
nonisolated enum AssistantSidebarGroups {
    static let older = String(localized: "Earlier")

    struct Group: Equatable {
        var title: String
        var sessions: [Session]
    }

    /// One row in Directions, keyed by the hub's opaque direction id. A real conversation wins if both representations
    /// exist. `target` belongs only to an outside direction and is navigation metadata, never row or fold identity.
    struct DirectionEntry: Identifiable {
        var id: String
        var session: Session?
        var outside: DirectionTree.Direction?

        init(id: String, session: Session? = nil, outside: DirectionTree.Direction? = nil) {
            self.id = id
            self.session = session
            self.outside = outside
        }

        var target: SessionKey? { session == nil ? outside?.externalTarget : nil }
        /// A targetless outside row has no navigation and no disclosure chevron, even if its tree entry lists tasks.
        var canDisclose: Bool { session != nil || target != nil }
    }

    /// The sidebar's Directions in `leaders`' order (the hub's), never the main one. Listed conversations come first;
    /// otherwise a current outside direction can stand on its own. An id described by neither stays omitted (#8236).
    static func directions(
        _ sessions: [Session], leaders: [String], main: SessionKey?,
        outside: [String: DirectionTree.Direction] = [:]
    ) -> [DirectionEntry] {
        var conversations: [String: Session] = [:]
        for session in sessions where conversations[session.sessionID] == nil {
            conversations[session.sessionID] = session
        }
        return leaders.compactMap { id in
            if id == main?.sessionID { return nil }
            if let session = conversations[id] { return DirectionEntry(id: id, session: session) }
            if let direction = outside[id], direction.isExternal {
                return DirectionEntry(id: id, outside: direction)
            }
            return nil
        }
    }

    /// How many conversations the sidebar's Recent lists (#4873); the rest are in All Chats.
    static let recentLimit = 5

    /// The sidebar's Recent (#4873, #5195): the newest `recentLimit` conversations by last activity, whoever opened them
    /// (the user, Lark /agent, the Inbox…; a row says which with its source's mark), except the main one, the
    /// directions (listed above it), the hub's built-in background supervisor (#4927, listed with the scheduled tasks'
    /// ones in All Chats only) and the scheduled tasks' own chats (#6228: those have their own Scheduled Tasks entry and
    /// stay in All Chats). `tasks` also catches a task chat a hub did not mark with `origin` (#2838). `sessions` are the
    /// open ones (no archived). `leaders`: the directions' session ids; `background`: the background supervisor's.
    /// `withTasks`: the user's Show Scheduled Task Chats (#8324) brings those last two back, by time with the rest.
    static func recent(_ sessions: [Session], leaders: [String], main: SessionKey?, background: String?, tasks: AssistantTaskChats = .none,
                       withTasks: Bool = false) -> [Session] {
        let lead = Set(leaders)
        let rest = sessions.filter {
            $0.key != main && !lead.contains($0.sessionID)
                && (withTasks || ($0.sessionID != background && AutoConversations.source(of: $0, tasks: tasks) != .task))
        }
        return Array(newestFirst(rest).prefix(recentLimit))
    }

    /// Newest first, a fixed order for equal times (the sidebar's Recent).
    static func newestFirst(_ sessions: [Session]) -> [Session] {
        sessions.sorted {
            let a = $0.updatedAt ?? .distantPast, b = $1.updatedAt ?? .distantPast
            return a != b ? a > b : $0.sessionID < $1.sessionID
        }
    }

    static func group(_ sessions: [Session], now: Date = .now, calendar: Calendar = .current) -> [Group] {
        let sorted = sessions.sorted {
            let a = $0.updatedAt ?? .distantPast, b = $1.updatedAt ?? .distantPast
            return a != b ? a > b : $0.sessionID < $1.sessionID // a fixed order for equal times
        }
        let today = calendar.startOfDay(for: now)
        let yesterday = calendar.date(byAdding: .day, value: -1, to: today) ?? today
        let week = calendar.date(byAdding: .day, value: -7, to: today) ?? today
        var out: [Group] = []
        for s in sorted {
            let title: String
            if let d = s.updatedAt, d.isMeaningful {
                title = d >= today ? String(localized: "Today") : d >= yesterday ? String(localized: "Yesterday") : d >= week ? String(localized: "Previous 7 Days") : older
            } else {
                title = older
            }
            if out.last?.title == title { out[out.count - 1].sessions.append(s) } else { out.append(Group(title: title, sessions: [s])) }
        }
        return out
    }
}

/// The sidebar's search: a conversation matches on its title or its last message, ignoring case and width.
nonisolated enum AssistantSidebarSearch {
    /// `names`: the directions' names by session id; a direction is found by its name too.
    static func filter(_ sessions: [Session], _ query: String, names: [String: String] = [:]) -> [Session] {
        let q = query.trimmingCharacters(in: .whitespaces)
        guard !q.isEmpty else { return sessions }
        let opts: String.CompareOptions = [.caseInsensitive, .widthInsensitive, .diacriticInsensitive]
        return sessions.filter {
            $0.title.range(of: q, options: opts) != nil || ($0.lastText ?? "").range(of: q, options: opts) != nil
                || (names[$0.sessionID] ?? "").range(of: q, options: opts) != nil
        }
    }
}

/// The conversations the sidebar showed last (not observed: keeping them changes nothing on screen).
final class SidebarFeed {
    private var last: [Session] = []

    /// `live` when the sidebar can be seen; nil while it is closed, for the list it had.
    func conversations(_ live: [Session]?) -> [Session] {
        if let live { last = live }
        return last
    }

    private var archivedCount = 0

    /// Archived's count the same way.
    func archived(_ live: Int?) -> Int {
        if let live { archivedCount = live }
        return archivedCount
    }
}

/// A sidebar heading's ⋯ menu (#778: Pin with only the main conversation pinned drew a ⋯ that opened nothing).
nonisolated enum HeadingMenu {
    /// Whether the menu has nothing in it now, so the heading draws no ⋯: no items, or items behind one `if` that is
    /// false, which a view builder makes a nil `Optional` rather than an `EmptyView`. Only those two shapes are looked
    /// into: several `if`s, an `if` / `else`, a `ForEach` or a `Group` count as items even when they draw nothing.
    static func isEmpty(_ items: some View) -> Bool {
        if items is EmptyView { return true }
        let mirror = Mirror(reflecting: items)
        return mirror.displayStyle == .optional && mirror.children.isEmpty
    }

    /// The heading's least height: one whose menu can empty keeps the ⋯'s height without it, so what is below does
    /// not jump; one that never has a menu (the search's 「N 个对话」) keeps its own.
    static func minHeight<Items: View>(_: Items.Type) -> CGFloat? {
        Items.self == EmptyView.self ? nil : Theme.Size.wellXS
    }
}

