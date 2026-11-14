import SwiftUI
#if os(iOS) || os(visionOS)
import UIKit

/// The top of the Agents page on a phone (#2478): the search field, the room over the list for the shell's top bar and
/// the list's first row (the quick filters), laid out here from two things only: whether the page is searching
/// (`ChatsView.searching`) and how far the list is scrolled past its top (`scrolled`).
///
/// It used to be the system's `navigationBarDrawer` search with our corrections around it: the drawer moved the field,
/// the navigation bar's height and the list's inset; `SearchCloseWatcher` wrote the list's offset to start it folded
/// and to undo UIKit's second adjustment; a blank prompt line made room for the shell's bar and went away while
/// searching; the shell's bar faded on its own clock. Closing a search, four of them moved at once on their own
/// timings (the list overshot ~140 pt and sprang back 138 pt in a frame, the field ended half folded, 2026-11-05;
/// #314, #413, #740, #1051, #2189 were this layer too). Now the navigation bar never changes height, nothing else
/// writes the list's insets, and everything that moves is a function of the two values above:
///
/// - At rest the field sits in the list's first row, above the filters: it scrolls with the list, and is cut off at the
///   list's top as it goes under the bar (`AgentsHeaderRow`), as the drawer folded it. The page starts folded (the list
///   scrolled by the field's band) and comes back folded when its tab is picked again (#740). Let go half way, the
///   list settles folded or shown. In the shell the list has room over it for the taller top bar (`room`).
/// - Searching, the field floats in the navigation bar's row (`AgentsSearchOverlay`). One animated value moves it, the
///   list (slid as a picture, `ListShift`) and the shell's bar's opacity (`AgentsTopBarFade`), so they move as one and
///   the bar is gone before the field gets to it. The list is laid out for searching (`compact`) only where that looks
///   the same: at the end of the way up and the start of the way down; a list laid out again mid-way moved twice.
/// - It starts once the keyboard is up (`AgentsSearchField.focused`): the first one of a launch stalls the main thread.
/// - The results and the whole list swap without an animation: drawn together they ghosted.
@MainActor @Observable final class AgentsHeader {
    /// How far the list is scrolled past its top (pt; negative while pulled down).
    var scrolled: CGFloat = 0
    /// Where the navigation bar ends on screen (global y): the list's top, and the shell's room under it.
    var listTop: CGFloat = 0
    /// Where the shell's top bar is drawn (global y; nil before it is laid out). It grows with the text, the field's way
    /// does not: how soon it fades as the field comes up follows it (`AgentsTopBarFade.gone`).
    var bar: ClosedRange<CGFloat>?
    /// Rows (not just the field folded away) are under the bars: the list's edge effect shows (`AgentsEdge`). Set only
    /// when it changes, so what reads it is not drawn again on every frame of a scroll.
    var rowsUnder = false
    /// The field floats in the bar's row: searching, and until it has come back down after.
    var floating = false
    /// Where the field is on its way, animated: up in the bar's row (searching) or down in the list's row. What moves
    /// follows it (`AgentsSearchOverlay`, `ListShift`, `AgentsTopBarFade`); the page's `searching` changes without an
    /// animation, so the list's rows (links at rest, buttons while searching) are not cross-faded.
    var up = false
    /// On the way up and not laid out for searching yet: what `ListShift.arrived` does it for. `setCompact` clears it
    /// either way, so a slide still at the top as a search closes is not taken for an arrival.
    @ObservationIgnored var rising = false
    /// The page laid out for searching: no field band in the list's row, no shell room over the list. Flipped at once
    /// where the two layouts look the same, the list shifted by the difference (`ListShift`): at the end of the field's
    /// way up, at the start of its way down. Between, the list only slides, as a picture, with the field.
    var compact = false

    /// How much higher the list's content is laid out searching than at rest: the field's band and the shell's room.
    static func shift(shell: Bool) -> CGFloat { band + room(shell: shell) }

    /// The field's band: as tall as the system's drawer had it.
    static let band = Theme.Size.searchBand
    /// Above the filters, in the row: the drawer's field sat above the list, and the list's top margin between it and
    /// the filters (the list's own top margin is none on a phone).
    static let filtersMargin = Theme.Space.xs

    /// Room over the list between the navigation bar and the field: the shell's top bar is taller than the navigation
    /// bar under it (the prompt line's height it replaces, `ShellNavigationClearance`). A bar of the list's own
    /// (`safeAreaBar`, `AgentsTop`), so the list blurs under it as under the navigation bar.
    static func room(shell: Bool) -> CGFloat { shell ? Theme.Shell.agentsRoom : 0 }
    /// The room's bar: the page's color, so faint it draws nothing, but not clear (a clear bar gets no blur).
    static var roomFill: some View { Theme.pageBackground.opacity(0.01) }

    /// How much of the field shows at rest: all at the list's top, less as the list scrolls (0 folded).
    static func shown(scrolled: CGFloat) -> CGFloat { min(max(band - scrolled, 0), band) }

    @ObservationIgnored fileprivate weak var probe: UIView?
    @ObservationIgnored private weak var foundList: UIScrollView?
    @ObservationIgnored private var folded = false

    /// Starts the page with the field folded away (2026-11-01 user request: it shows on a pull, like Mail): the list
    /// scrolled by the field's band, once it is laid out, if it still sits at its top. A list too short to scroll gets
    /// just enough room at its end to. Again when the page comes back to the front (`refold`); never on the way back
    /// from a chat, which keeps the list as it was.
    func foldOnce() {
        guard !folded else { return }
        var tries = 0
        func attempt() {
            tries += 1
            guard !folded else { return }
            if let list = listScrollView, list.contentSize.height > 0, list.bounds.height > 0 {
                folded = true
                measureListTop()
                guard !floating else { return }
                let room = list.bounds.height - list.adjustedContentInset.top - list.adjustedContentInset.bottom
                let short = room + Self.band - list.contentSize.height
                if short > 0 { list.contentInset.bottom += short }
                let top = -list.adjustedContentInset.top
                // x kept as it is: the list's side margins live in its content offset.
                if abs(list.contentOffset.y - top) < 1 { list.setContentOffset(CGPoint(x: list.contentOffset.x, y: top + Self.band), animated: false) }
                return
            }
            if tries < 20 { DispatchQueue.main.asyncAfter(deadline: .now() + .milliseconds(50), execute: attempt) }
        }
        DispatchQueue.main.async(execute: attempt)
    }

    /// The page came back to the front: folded again if the list sits at its top with the field out (#740).
    func refold() {
        folded = false
        foldOnce()
    }

    /// Let go with the field part way out: the list goes on to show it all, or fold it, whichever is nearer.
    func settle() {
        guard !floating, let list = listScrollView, !list.isTracking, !list.isDecelerating else { return }
        let s = list.contentOffset.y + list.adjustedContentInset.top
        guard s > 0.5, s < Self.band - 0.5 else { return }
        let top = -list.adjustedContentInset.top
        list.setContentOffset(CGPoint(x: list.contentOffset.x, y: top + (s < Self.band / 2 ? 0 : Self.band)), animated: true)
    }

    /// Where the navigation bar ends on screen, from UIKit: SwiftUI's frame of a `List` under a navigation bar is not
    /// where UIKit draws it, and the list's inset has the shell's room in it or not.
    func measureListTop() {
        guard let list = listScrollView, let bar = Self.navigationBar(of: list), let window = bar.window else { return }
        let y = bar.convert(bar.bounds, to: window).maxY
        if abs(y - listTop) > 0.5 { listTop = y }
    }

    private static func navigationBar(of v: UIView) -> UINavigationBar? {
        var r: UIResponder? = v
        while let next = r?.next {
            if let vc = next as? UIViewController, let nav = vc.navigationController { return nav.navigationBar }
            r = next
        }
        return nil
    }

    /// Laid out for searching or at rest (`compact`): the field's band above the list's top or not, the list at its top.
    func setCompact(_ on: Bool) {
        rising = false
        var still = Transaction()
        still.disablesAnimations = true
        withTransaction(still) { compact = on }
        guard let list = listScrollView else { return }
        let inset = on ? -Self.band : 0
        if list.contentInset.top != inset { list.contentInset.top = inset }
        list.contentOffset = CGPoint(x: list.contentOffset.x, y: -list.adjustedContentInset.top)
    }

    /// Back at the list's top with the field out, at once (a search closed: the whole list is back, from its top).
    func showField() {
        guard let list = listScrollView else { return }
        list.setContentOffset(CGPoint(x: list.contentOffset.x, y: -list.adjustedContentInset.top), animated: false)
    }

    /// The list's scroll view: the largest collection view in the page the probe is in (List is one). Kept once found
    /// while it is in a window (`ReaderAnchor` asks on every push).
    var listScrollView: UIScrollView? {
        if let l = foundList, l.window != nil { return l }
        foundList = findListScrollView
        return foundList
    }

    private var findListScrollView: UIScrollView? {
        var r: UIResponder? = probe
        while let next = r?.next {
            if let vc = next as? UIViewController { return Self.largestScrollView(in: vc.view) }
            r = next
        }
        return nil
    }

    private static func largestScrollView(in root: UIView?) -> UIScrollView? {
        guard let root else { return nil }
        var best: UIScrollView?
        var stack = [root]
        while let v = stack.popLast() {
            if let c = v as? UICollectionView, !c.isHidden, c.bounds.height > (best?.bounds.height ?? 0) { best = c }
            stack.append(contentsOf: v.subviews)
        }
        return best
    }
}

/// Where `AgentsHeader` finds the list's scroll view from.
struct AgentsHeaderProbe: UIViewRepresentable {
    let header: AgentsHeader

    func makeUIView(context: Context) -> UIView {
        let v = UIView()
        v.isUserInteractionEnabled = false
        header.probe = v
        return v
    }

    func updateUIView(_ uiView: UIView, context: Context) { header.probe = uiView }
}

/// The list's first row on a phone: the field (cut off at the list's top as it scrolls under the bar) and the quick
/// filters. Only this row follows the scroll; the list does not.
struct AgentsHeaderRow<Filters: View>: View {
    let header: AgentsHeader
    @Binding var query: String
    var begin: () -> Void
    @ViewBuilder var filters: Filters
    /// A row of its own the filters were at least this tall, centered in it (BeautifulUI's are shorter: 2 pt above and
    /// below); in this row they keep that.
    @Environment(\.defaultMinListRowHeight) private var rowHeight

    var body: some View {
        VStack(spacing: 0) {
            // Always in the row: a row that changes height is laid out again over two passes, the rows under it
            // moving twice. Laid out for searching, the list's top inset hides the band (`compact`).
            AgentsFoldingField(header: header, query: $query, begin: begin)
            filters
                .frame(minHeight: rowHeight)
                .padding(.top, AgentsHeader.filtersMargin)
        }
    }
}

/// The field in the list's row: what is left of it below the list's top, fading as it goes.
private struct AgentsFoldingField: View {
    let header: AgentsHeader
    @Binding var query: String
    var begin: () -> Void

    var body: some View {
        let shown = AgentsHeader.shown(scrolled: header.scrolled)
        let band = AgentsHeader.band
        AgentsSearchField(text: $query, active: false, begin: begin)
            .modifier(AgentsFieldPlace())
            .frame(height: band, alignment: .top)
            .mask(alignment: .bottom) { Rectangle().frame(height: shown) }
            .opacity(header.floating ? 0 : Double(shown / band))
            #if os(iOS)
            .allowsHitTesting(!header.floating && shown >= band / 2)
            #endif
            .accessibilityHidden(header.floating || shown < band / 2)
            .searchTraceMark("field")
    }
}

/// The bar across the page, its field on the page's edges whatever the row's own insets are (an inset grouped list's
/// row starts `Theme.Space.page` in, a plain one at the screen's edge).
struct AgentsFieldPlace: ViewModifier {
    nonisolated private struct Widths: Equatable, Sendable { var row: CGFloat, page: CGFloat }
    @Environment(\.shell) private var shell
    @State private var minX: CGFloat = 0
    @State private var screen: CGFloat = 0

    func body(content: Content) -> some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("AgentsFieldPlace")
        #endif
        let outside = Theme.Space.page
        content
            .frame(width: max(0, screen - 2 * outside))
            .offset(x: outside - minX)
            .frame(maxWidth: .infinity, alignment: .leading)
            // The row's own width and the page's, neither of which a slide of the page changes. From the row's frame
            // in the window, this state was written on every frame the shell's strip slid (about 60 a switch), and the
            // row, the filters under it included, was laid out again each time (#5438).
            .onGeometryChange(for: Widths.self) { [shell] g in
                // The shell's page is as wide as its strip measured it (unobserved, read as the row is laid out).
                if let page = shell?.pager.width { return Widths(row: g.size.width, page: page) }
                let r = g.frame(in: .global) // perf-ok: only outside the shell, where no strip slides the row
                return Widths(row: r.width, page: r.maxX + r.minX)
            } action: { w in
                minX = (w.page - w.row) / 2 // the row is centered on the page
                screen = w.page
            }
    }
}

/// The field while searching: in the navigation bar's row, over the page. It comes up from where the row's field
/// was and goes back down to it; `progress` (0 there … 1 up) is animated, and the shell's bar fades with it.
struct AgentsSearchOverlay: View {
    let header: AgentsHeader
    var shell: Bool
    @Binding var query: String
    var focused: () -> Void
    var cancel: () -> Void

    var body: some View {
        // From the top of the safe area: the navigation bar's row.
        GeometryReader { proxy in
            let from = header.listTop + AgentsHeader.room(shell: shell) - proxy.frame(in: .global).minY // the row's field
            if header.floating {
                HStack(alignment: .top, spacing: Theme.Size.searchCloseGap) {
                    AgentsSearchField(text: $query, active: true, focused: focused)
                    // Its bar puts the item at its trailing margin: the page's edge.
                    SystemCloseButton(action: cancel)
                        .frame(width: Theme.Size.tapTarget + Theme.Space.page, height: AgentsHeader.band) // the bar's own row
                }
                .padding(.leading, Theme.Space.page)
                .frame(height: AgentsHeader.band, alignment: .top)
                .searchTraceMark("float")
                .modifier(Rise(progress: header.up ? 1 : 0, from: from, to: 0))
            }
        }
    }

    /// The field between the row (0) and the bar's row (1).
    private struct Rise: ViewModifier, Animatable {
        var progress: Double
        var from: CGFloat
        var to: CGFloat
        var animatableData: Double {
            get { progress }
            set { progress = newValue }
        }

        func body(content: Content) -> some View {
            content.offset(y: from + (to - from) * progress)
        }
    }
}

/// The list while the field goes up or down: shifted, as a picture (no layout), by what is between where it is laid
/// out and where it is drawn. `progress` is the field's (`AgentsSearchOverlay`), so they move as one.
///
/// `arrived`: the list has slid all the way up still laid out at rest, where laying it out for searching (`setCompact`)
/// moves nothing. Not the rise's animation completion: when the first keyboard of a launch stalled the main thread, the
/// change reached the list only after the stall, the transaction had nothing animating yet and completed at once, and
/// the list was laid out for searching before it had slid: a 54 pt jump in one frame (#5999).
struct ListShift: ViewModifier, Animatable {
    var progress: Double
    var compact: Bool
    var shift: CGFloat
    var arrived: () -> Void = {}
    var animatableData: Double {
        get { progress }
        set { progress = newValue }
    }

    func body(content: Content) -> some View {
        let _ = arrive()
        content.offset(y: (compact ? shift : 0) - shift * progress)
    }

    /// With each value it is drawn at (every frame of the slide): up (a spring's overshoot counts), not laid out for
    /// searching yet.
    private func arrive() {
        if progress >= 1, !compact { DispatchQueue.main.async(execute: arrived) }
    }
}

/// The system's search field, on its own (no search bar, no search controller, no navigation item): the same field,
/// keyboard and VoiceOver as the drawer's. `active`: it takes the keyboard as it appears, drawn as the drawer's is
/// while searching (glass; its ✕ is `SystemCloseButton`). Not active, the drawer's at rest: a flat capsule of the
/// tertiary fill, and a tap on it begins a search (`begin`) instead of editing here: the floating one takes over.
/// The field is a search bar's own (`searchTextField`: its magnifier and placeholder drawn as the drawer's, heavier
/// and darker than a plain `UISearchTextField`'s), taken out of the bar and put in a view of ours: in the bar it is laid
/// out again on every pass (8 pt in from the sides, centered in the bar's 64 pt), wherever the bar is put.
struct AgentsSearchField: UIViewRepresentable {
    @Binding var text: String
    var active: Bool
    var begin: () -> Void = {}
    /// Active: it has the keyboard (the first one of a launch takes a moment to load; what moves waits for it).
    var focused: () -> Void = {}

    static var prompt: String { AgentsSearchPrompt.text }

    func makeCoordinator() -> Coordinator { Coordinator(self) }

    func makeUIView(context: Context) -> Holder {
        let bar = UISearchBar()
        bar.placeholder = Self.prompt
        let field = bar.searchTextField
        field.removeFromSuperview()
        let holder = Holder(field: field, bar: bar)
        field.returnKeyType = .search
        field.accessibilityTraits.insert(.searchField) // as the drawer's: VoiceOver and UI tests know it as a search field
        field.enablesReturnKeyAutomatically = true
        field.delegate = context.coordinator
        #if os(iOS)
        if !active {
            // UIKit may probe an input's focus while opening a Menu. Only explicit taps or accessibility activation
            // begin search; an incidental first-responder request must not create the floating, active input.
            let tap = UITapGestureRecognizer(target: context.coordinator, action: #selector(Coordinator.beginSearchFromTap(_:)))
            tap.cancelsTouchesInView = false
            tap.delegate = context.coordinator
            holder.addGestureRecognizer(tap)
            holder.activateSearch = { [weak coordinator = context.coordinator] in coordinator?.parent.begin() }
            holder.isAccessibilityElement = true
            holder.accessibilityTraits = .searchField
            holder.accessibilityLabel = Self.prompt
            holder.accessibilityValue = text.isEmpty ? Self.prompt : text
            field.isAccessibilityElement = false
        }
        #endif
        field.addAction(UIAction { [weak field, weak coordinator = context.coordinator] _ in
            coordinator?.parent.text = field?.text ?? ""
        }, for: .editingChanged)
        field.text = text
        if !active {
            field.borderStyle = .none
            field.backgroundColor = .tertiarySystemFill
            field.layer.cornerRadius = Theme.Size.searchField / 2
            field.layer.cornerCurve = .continuous
        } else {
            DispatchQueue.main.async { field.becomeFirstResponder() }
        }
        return holder
    }

    /// The field across it; the bar it came from kept with it (the field's look is the bar's).
    final class Holder: UIView {
        let field: UISearchTextField
        private let bar: UISearchBar
        var activateSearch: (() -> Void)?
        init(field: UISearchTextField, bar: UISearchBar) {
            self.field = field
            self.bar = bar
            super.init(frame: .zero)
            field.frame = bounds
            field.autoresizingMask = [.flexibleWidth, .flexibleHeight]
            addSubview(field)
        }
        required init?(coder: NSCoder) { nil }

        override func accessibilityActivate() -> Bool {
            guard let activateSearch else { return super.accessibilityActivate() }
            activateSearch()
            return true
        }

        /// An inset grouped list rounds its section's cells (a 26 pt corner configuration, clipped): the field's corners
        /// in the first row were cut deeper than the drawer's. The row has no background, so its cell is left unclipped,
        /// and clipped again once the field leaves it (the cell may be reused).
        private weak var cell: UICollectionViewCell?
        /// The list sets it again when it configures the cell (a search closed: the field back in the row).
        private var clipping: NSKeyValueObservation?

        override func layoutSubviews() {
            super.layoutSubviews()
            unclipCell()
        }

        override func didMoveToWindow() {
            super.didMoveToWindow()
            if window == nil {
                clipping = nil
                cell?.clipsToBounds = true
                cell = nil
            } else {
                unclipCell()
            }
        }

        private func unclipCell() {
            if cell == nil {
                var v = superview
                while let x = v, !(x is UICollectionViewCell) { v = x.superview }
                cell = v as? UICollectionViewCell
            }
            guard let cell else { return }
            if cell.clipsToBounds { cell.clipsToBounds = false }
            if clipping == nil {
                clipping = cell.layer.observe(\.masksToBounds, options: [.new]) { layer, _ in
                    if layer.masksToBounds { layer.masksToBounds = false } // on the main thread, where the list set it
                }
            }
        }
    }

    func updateUIView(_ holder: Holder, context: Context) {
        let field = holder.field
        context.coordinator.parent = self
        if field.text != text { field.text = text }
        if !active {
            #if os(iOS)
            holder.accessibilityValue = text.isEmpty ? Self.prompt : text
            #endif
            if field.isFirstResponder { field.resignFirstResponder() }
        }
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: Holder, context: Context) -> CGSize? {
        CGSize(width: proposal.width ?? uiView.field.intrinsicContentSize.width, height: Theme.Size.searchField)
    }

    final class Coordinator: NSObject, UITextFieldDelegate, UIGestureRecognizerDelegate {
        var parent: AgentsSearchField
        init(_ parent: AgentsSearchField) { self.parent = parent }

        @objc func beginSearchFromTap(_ recognizer: UITapGestureRecognizer) {
            guard !parent.active, recognizer.state == .ended else { return }
            parent.begin()
        }

        func gestureRecognizer(_ gestureRecognizer: UIGestureRecognizer,
                               shouldRecognizeSimultaneouslyWith otherGestureRecognizer: UIGestureRecognizer) -> Bool {
            true
        }

        func textFieldShouldBeginEditing(_ field: UITextField) -> Bool {
            #if os(iOS)
            return parent.active
            #else
            if parent.active { return true }
            parent.begin()
            return false
            #endif
        }

        private var keyboard: NSObjectProtocol?

        /// The keyboard is about to come up: loaded (the first one of a launch takes a moment, and the main thread
        /// with it), so what moves now moves without a stall.
        func textFieldDidBeginEditing(_ field: UITextField) {
            guard parent.active, keyboard == nil else { return }
            keyboard = NotificationCenter.default.addObserver(forName: UIResponder.keyboardWillShowNotification, object: nil,
                                                              queue: .main) { [weak self] _ in
                MainActor.assumeIsolated { self?.parent.focused() }
            }
            // Already up (a hardware keyboard: none comes).
            DispatchQueue.main.asyncAfter(deadline: .now() + 0.6) { [weak self] in self?.parent.focused() }
        }

        isolated deinit { if let keyboard { NotificationCenter.default.removeObserver(keyboard) } }

        func textFieldShouldReturn(_ field: UITextField) -> Bool {
            field.resignFirstResponder()
            return false
        }
    }
}

/// The ✕ that ends a search, as the drawer's search shows it in the navigation bar: the system's close item, on a bar
/// of its own (a glass button of our own drew the cross thinner). It sits at the bar's trailing margin.
struct SystemCloseButton: UIViewRepresentable {
    var action: () -> Void

    func makeCoordinator() -> Coordinator { Coordinator(action) }

    func makeUIView(context: Context) -> UINavigationBar {
        let bar = UINavigationBar()
        bar.directionalLayoutMargins = .zero
        bar.preservesSuperviewLayoutMargins = false
        let item = UINavigationItem()
        let close = UIBarButtonItem(systemItem: .close, primaryAction: UIAction { [weak coordinator = context.coordinator] _ in coordinator?.action() })
        item.rightBarButtonItem = close
        bar.items = [item]
        return bar
    }

    func updateUIView(_ bar: UINavigationBar, context: Context) { context.coordinator.action = action }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: UINavigationBar, context: Context) -> CGSize? {
        CGSize(width: proposal.width ?? Theme.Size.tapTarget, height: proposal.height ?? Theme.Size.searchField)
    }

    final class Coordinator {
        var action: () -> Void
        init(_ action: @escaping () -> Void) { self.action = action }
    }
}

#endif

#if os(iOS) || os(visionOS)
/// The shell's bar over the Agents list: reports where it is drawn to the header and fades as the field rises, reading
/// the header itself. `ChatsView`'s body read `up`, `bar` and `listTop` for this; the bar is put back each time the
/// strip comes to rest on Agents and reports its place again, so the page's body, and with it the list's over every row,
/// ran on each switch from Assistant (#5438, docs/apple/performance.md P5).
struct AgentsTopBarPlace: ViewModifier {
    let header: AgentsHeader

    func body(content: Content) -> some View {
        content
            .onGeometryChange(for: ClosedRange<CGFloat>.self) { g in
                let f = g.frame(in: .global) // perf-ok: only its top and bottom, which a slide sideways leaves alone
                return f.minY...f.maxY
            } action: { [header] y in
                if header.bar != y { header.bar = y }
            }
            // The field on its way up into the bar's row, animated (`AgentsHeader.up`).
            .modifier(AgentsTopBarFade(progress: header.up ? 1 : 0,
                                       gone: AgentsTopBarFade.gone(bar: header.bar, field: header.listTop + AgentsHeader.room(shell: true))))
    }
}
#endif

/// The shell's top bar while the field comes up into its row: gone before the field gets to it, back only once the
/// field has gone below it. `progress` is the same animated value as the field's (`AgentsSearchOverlay`).
struct AgentsTopBarFade: ViewModifier, Animatable {
    var progress: Double
    /// The share of the field's way up by which the bar has gone (`gone(bar:field:)`).
    var gone = Self.standard
    var animatableData: Double {
        get { progress }
        set { progress = newValue }
    }

    /// At the standard text size the field reaches the bar's bottom at about 0.4 of its way (150 → 62 pt, the bar
    /// 62–112): the bar is gone by 0.35.
    static let standard = 0.35

    /// By when the bar has gone, so that the field never comes into its rows while the bar still shows, with a little
    /// air (`Theme.Space.sm`): sooner when larger text makes the bar taller, never later than `standard`. At XXXL the
    /// bar is 62–122 and the field met it at 0.32 with the bar still 8 % there (#4928). `bar`: where it is drawn;
    /// `field`: where the field starts up from (global y). The field ends at the bar's top: both at the top of the safe area.
    static func gone(bar: ClosedRange<CGFloat>?, field: CGFloat) -> Double {
        guard let bar, field - bar.lowerBound > 1 else { return standard }
        let clear = field - bar.upperBound - Theme.Space.sm
        return min(standard, max(0.05, Double(clear / (field - bar.lowerBound))))
    }

    func body(content: Content) -> some View {
        content.opacity(max(0, 1 - progress / gone))
    }
}
