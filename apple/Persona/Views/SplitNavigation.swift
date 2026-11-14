import OSLog
import SwiftUI

/// A list and the pages it opens (Agents, Inbox, Settings), laid out for the width there is (docs/apple/ios.md「iPhone Duo」).
///
/// Compact width — every iPhone, and iPhone Duo closed — is one stack with the list at its root, exactly as before.
/// Regular width — iPhone Duo open — keeps the list as a sidebar column and opens its pages in the detail column,
/// the way Mail shows a list and a message side by side; the split view also keeps its columns clear of the fold
/// when the device is partly folded. Both share `path`, so folding or unfolding keeps the page that was open.
///
/// In the split, the list's own links (`NavigationLink(value:)`) replace what the detail column shows; code in the list
/// should do the same (`path = NavigationPath([value])`, which in the single stack is the same as appending).
struct SplitNavigation<Sidebar: View, Empty: View, Destinations: ViewModifier>: View {
    @Environment(\.horizontalSizeClass) private var widthClass
    /// Whether the list column shows (its button in the toolbar closes it).
    @State private var columns = SplitColumns.shown
    @Binding var path: NavigationPath
    /// iOS: the list column's width and closing dragged on the line beside it and remembered under this key (#2624);
    /// nil: the system's column.
    var listKey: String? = nil
    /// The pages the list opens (`navigationDestination`s), registered in both layouts.
    var destinations: Destinations
    /// The Assistant | Agents switch over the page column, on a phone's tabs (iPhone Duo open: `pageTabSwitch`, #2623).
    var pageSwitch = false
    #if os(iOS)
    /// The Sidebar layout on a phone's width (#6332): the list in this drawer, the pages one stack whose root is `empty`
    /// (`drawerLayout`); nil, the list at the stack's root as before. Set around this split only (`splitDrawer`): it is
    /// cleared for what is inside, so a split in a page or a sheet keeps its own layout.
    @Environment(\.splitDrawer) private var drawer
    #endif
    @ViewBuilder var sidebar: Sidebar
    /// The detail column before anything is open.
    @ViewBuilder var empty: Empty

    var body: some View {
        layout.environment(\.splitPush, SplitPush { [path = $path] value in path.wrappedValue.append(value) })
        #if os(iOS)
            .environment(\.splitDrawer, nil)
        #endif
    }

    @ViewBuilder private var layout: some View {
        if widthClass == .regular {
            #if os(macOS)
            // The list in the content column (flush; the sidebar column floats as a glass card on macOS 26 and stays
            // empty). A value link in the content column replaces the detail column's stack, as one in the sidebar did.
            NavigationSplitView(columnVisibility: $columns) {
                SplitColumns.noSidebar
            } content: {
                // The look the list had in the sidebar column (no row lines, footers as before) on the window's white,
                // as the Persona and Agents lists beside the sections are; lists that pick a style keep it. Inside the
                // destinations, so the style stays in this column whichever environment the pushed pages are built in.
                sidebar
                    .listStyle(.sidebar)
                    .scrollContentBackground(.hidden)
                    .modifier(destinations)
                    .modifier(SidebarWidth())
            } detail: {
                NavigationStack(path: $path) {
                    empty.modifier(destinations)
                }
                // The page keeps a width it can be read at; narrowing the window narrows the list first (#1200).
                .navigationSplitViewColumnWidth(min: Theme.Size.macDetailMin, ideal: Theme.Size.macWindowWidth)
                .onGeometryChange(for: Bool.self) { $0.size.width < Theme.Size.macSplitSquished } action: { squished in
                    SplitColumns.log.notice("detail squished: \(squished) (columns \(String(describing: columns), privacy: .public), depth \(path.count))")
                }
            }
            .navigationSplitViewStyle(.balanced)
            .environment(\.splitDetail, SplitDetail(isFirstPage: path.count <= 1))
            // The detail column once came out a few points wide, the chat squeezed into it (2026-11-02, not reproduced):
            // what the columns and the stack did, for the next time (`log show --predicate 'category == "split"'`).
            .onChange(of: columns) { old, new in
                SplitColumns.log.notice("columns \(String(describing: old), privacy: .public) → \(String(describing: new), privacy: .public)")
            }
            .onChange(of: path.count) { old, new in SplitColumns.log.notice("depth \(old) → \(new)") }
            #else
            NavigationSplitView(columnVisibility: $columns) {
                // No stack of its own: a value link in the sidebar column replaces the detail column's stack. A sidebar
                // stack that refused its pushes (a path binding that always read empty) first slid the list away, then
                // back: the sidebar folded and unfolded on every tap.
                sidebar.modifier(destinations)
                    .modifier(SidebarWidth())
            } detail: {
                NavigationStack(path: $path) {
                    empty.modifier(destinations)
                }
                #if os(iOS)
                // The stack's root page only: the pages opened on it carry their own (`SplitDetailPage`, #3680).
                .safeAreaInset(edge: .top, spacing: 0) { if pageSwitch { PageTabSwitchRow() } }
                #endif
            }
            .navigationSplitViewStyle(.balanced)
            .modifier(SidebarTopFill(shown: columns != .detailOnly))
            .modifier(SplitDividerHandle(columns: $columns, key: listKey))
            // On the whole split, not the detail stack: its pages are built from the destinations the sidebar
            // registered, in the sidebar's environment.
            .environment(\.splitDetail, SplitDetail(isFirstPage: path.count <= 1, listHidden: columns == .detailOnly, pageSwitch: pageSwitch))
            .environment(\.splitShowList, SplitShowList(show: { [columns = $columns] in
                withAnimation(Theme.Motion.layout) { columns.wrappedValue = SplitColumns.shown }
            }, hide: { [columns = $columns] in
                withAnimation(Theme.Motion.layout) { columns.wrappedValue = .detailOnly }
            }))
            #endif
        } else {
            #if os(iOS)
            if let drawer {
                drawerLayout(drawer)
            } else {
                NavigationStack(path: $path) {
                    sidebar.modifier(destinations)
                }
            }
            #else
            NavigationStack(path: $path) {
                sidebar.modifier(destinations)
            }
            #endif
        }
    }

    #if os(iOS)
    /// The Sidebar layout on a phone (#6332): the list in the drawer that slides in from the left, the pages one stack
    /// whose root is `empty` (the Persona conversation). What the list opens is that stack's first page, as beside an
    /// iPad's column, with the drawer's button where its back would be (`SplitDetail.drawer`), and a drag to the right on
    /// it opens the drawer as on the conversation (`AssistantDrawerLayout.firstPages`). Behind the drawer, the Agents
    /// list's own page color: its head and foot and the list are one surface, in dark mode too.
    private func drawerLayout(_ drawer: AssistantDrawer) -> some View {
        AssistantDrawerLayout(drawer: drawer, enabled: path.count <= 1, backdrop: Theme.groupedPage, firstPages: true, sidebar: { sidebar }, page:
            NavigationStack(path: $path) { empty.modifier(destinations) })
            .environment(\.splitDetail, SplitDetail(isFirstPage: path.count <= 1, listHidden: true, drawer: true))
            .environment(\.splitShowList, SplitShowList(show: { drawer.open() }))
    }
    #endif
}

/// What a split shows when its list is out: the list beside the page. On the Mac the list is the content column of a
/// three-column split whose sidebar column stays empty (`noSidebar`): macOS 26 draws the sidebar column as a glass card
/// floating off the window's edges, the window's backdrop showing round it (2026-11-02 user: like floating on a backdrop, with a gap).
/// The shell's sections column is the window's sidebar there.
enum SplitColumns {
    #if os(macOS)
    static let shown = NavigationSplitViewVisibility.doubleColumn

    /// The empty sidebar column, and no system button to bring it out.
    static var noSidebar: some View { Color.clear.toolbar(removing: .sidebarToggle) }

    static let log = Logger(subsystem: "persona", category: "split")
    #else
    static let shown = NavigationSplitViewVisibility.all
    #endif
}

/// The list column's width. iPad (and a Mac window): wider than the system's sidebar, which is narrow for the agents'
/// cards (they wrapped every title); dragged on the line beside it (`SplitDividerHandle`, #2624), that width. One
/// expression whatever the width: a modifier coming or going would build the list again (its filters, its scroll).
/// iPhone Duo: none of our own (the system sizes the columns, and partly folded it moves the divide onto the fold) until
/// one is kept from a drag; that takes the column once, at the drag's end, never during it.
struct SidebarWidth: ViewModifier {
    @Environment(\.splitListWidth) private var dragged

    func body(content: Content) -> some View {
        if UIDevice.current.userInterfaceIdiom == .pad || UIDevice.current.userInterfaceIdiom == .mac {
            content.navigationSplitViewColumnWidth(min: dragged ?? Theme.Size.sidebarMin, ideal: dragged ?? Theme.Size.sidebar,
                                                   max: dragged ?? Theme.Size.sidebarMax)
                .background(GeometryReader { g in Color.clear.preference(key: SidebarEdge.self, value: g.frame(in: .global).maxX) })
        } else if UIDevice.current.userInterfaceIdiom == .phone {
            Group {
                if let dragged { content.navigationSplitViewColumnWidth(dragged) } else { content }
            }
            // Where the column ends, for the handle on the line beside it.
            .background(GeometryReader { g in Color.clear.preference(key: SidebarEdge.self, value: g.frame(in: .global).maxX) })
        } else {
            content
        }
    }
}

/// Where the list column ends, for `SidebarTopFill` and the handle on the line beside it (`SplitDividerHandle`).
struct SidebarEdge: PreferenceKey {
    static let defaultValue: CGFloat = 0
    static func reduce(value: inout CGFloat, nextValue: () -> CGFloat) { value = max(value, nextValue()) }
}

/// iPad: the list column starts below the status bar (its background cannot reach above it), so the strip over it was
/// the window's white and the list looked cut off at the top (2026-10-31). The split fills that strip with the
/// column's color, as wide as the column is.
struct SidebarTopFill: ViewModifier {
    /// The column is open (closed, its last frame stays reported: the fill must go by this).
    var shown: Bool
    @State private var edge: CGFloat = 0

    func body(content: Content) -> some View {
        if UIDevice.current.userInterfaceIdiom == .pad {
            content
                .onPreferenceChange(SidebarEdge.self) { edge = $0 }
                .background(alignment: .topLeading) {
                    (AppStyle.current.usesGlass ? Theme.systemSidebar : Theme.pageBackground)
                        .frame(width: shown ? max(edge, 0) : 0)
                        .animation(Theme.Motion.layout, value: shown)
                        .ignoresSafeArea()
                }
        } else {
            content
        }
    }
}

/// Set on the detail column of a `SplitNavigation` in regular width; nil in a single stack.
struct SplitDetail: Equatable {
    /// The page is the first one the list opened: there is nothing to go back to (the list is beside it).
    var isFirstPage: Bool
    /// The list column is closed (iOS: its button in the list's toolbar closed it). A first page with its own header
    /// (an Agents chat) has no system button to bring it back: its back does (`splitShowList`).
    var listHidden = false
    /// The page is a window of its own (the Mac's chat window), not a split's detail column: the window's title bar has
    /// its title.
    var ownWindow = false
    /// The Assistant | Agents switch rides over each page of the column (a phone's tabs, `pageTabSwitch`, #3680).
    var pageSwitch = false
    /// The Sidebar layout on a phone (#6332): the list is the drawer's, and a first page has the drawer's button where a
    /// back would be.
    var drawer = false
}

extension EnvironmentValues {
    /// The width the list column was dragged to on the line beside it (#2624, `SplitDividerHandle`); nil: the system's.
    @Entry var splitListWidth: CGFloat?
    @Entry var splitDetail: SplitDetail?
    /// Pushes a page by value onto the stack the view is in (an extension's `push`, docs/extensions/extensions.md「Page navigation」): its
    /// destination is registered with the list's.
    @Entry var splitPush: SplitPush?
    /// Opens the list column of the split the page is in again (iOS); nil in a single stack.
    @Entry var splitShowList: SplitShowList?
    #if os(iOS)
    /// The drawer the next `SplitNavigation` puts its list in at a phone's width (the Sidebar layout, #6332).
    @Entry var splitDrawer: AssistantDrawer?
    #endif
}

/// Brings a `SplitNavigation`'s list column back, or (the Sidebar layout's Persona page, its sidebar button, #6332)
/// folds it away.
struct SplitShowList {
    var show: () -> Void
    var hide: (() -> Void)? = nil
}

/// Appends a value to a `SplitNavigation`'s path.
struct SplitPush {
    var push: (any Hashable) -> Void
}

extension View {
    /// A page the list opens: in the split's detail column its first page has no back button, and on a phone's tabs each
    /// page there carries the Assistant | Agents switch (`SplitDetail.pageSwitch`, #3680). `ownsBack`: the page shows or
    /// hides the system's back itself (an Agents chat); on iOS this leaves it alone, as an outer
    /// `navigationBarBackButtonHidden(false)` wins over the page's own `true` (iPhone Duo's side bars, 2026-11-04: the
    /// system's back beside the chat's own, two backs). On the Mac the title row draws no ‹ for it: the chat's header
    /// has its own, shown only with a page under the chat (#1337).
    func splitDetailPage(ownsBack: Bool = false) -> some View { modifier(SplitDetailPage(ownsBack: ownsBack)) }
}

private struct SplitDetailPage: ViewModifier {
    @Environment(\.splitDetail) private var split
    var ownsBack = false

    func body(content: Content) -> some View {
        #if os(macOS)
        content.modifier(MacDetailBack(shown: !ownsBack && split?.isFirstPage == false))
        #else
        // `ownsBack` is fixed for a page (no identity change as the path grows).
        Group {
            if ownsBack { content } else { content.navigationBarBackButtonHidden(split?.isFirstPage == true) }
        }
        #if os(iOS)
        .modifier(PageTabSwitchInset(shown: split?.pageSwitch == true))
        // The Sidebar layout on a phone (#6332): a first page opens the drawer where its back would be.
        .modifier(DrawerButtonItem(shown: !ownsBack && split?.drawer == true && split?.isFirstPage == true))
        #endif
        #endif
    }
}

/// The detail column before anything is open: a quiet hint.
struct SplitPlaceholder: View {
    @Environment(\.splitDetail) private var split
    @Environment(\.splitShowList) private var showList
    var symbol: String
    var title: String
    var message: String

    var body: some View {
        ContentUnavailableView {
            Label(title, systemImage: symbol)
        } description: {
            Text(message)
        } actions: {
            // The list closed with nothing open: this page hides its bar, where the system's button to open the list
            // would be, so it filled the screen with no way back to the list.
            if split?.listHidden == true, let showList {
                Button { showList.show() } label: { Text("Show List").lineLimit(1) }.themedButton()
            }
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Theme.pageBackground)
        .toolbarVisibility(.hidden, for: .navigationBar)
    }
}

/// The line between a split's list and its page on iOS (#2624, iPad and iPhone Duo open), as Feishu's: a handle on it
/// drags the list wider or narrower (`least`…`most`), the page taking the rest; dragged well past the least it closes the
/// list, and from the page's leading edge it brings the list back; a double tap gives the column back to the system.
/// The width and the closing are remembered under `key` (nil: no handle, the system's column, as before). The column's
/// own button closes and opens it as well, remembered the same. Not on the Mac or Apple Vision Pro.
struct SplitDividerHandle: ViewModifier {
    @Binding var columns: NavigationSplitViewVisibility
    let key: String?
    /// The width dragged to; 0: never dragged (or given back), the system's.
    @StoredValue private var width: Double
    @StoredValue private var closed: Bool
    /// The split's frame (global), and the column's width as last laid out at rest.
    @State private var origin: CGFloat = 0
    @State private var total: CGFloat = 0
    @State private var measured: CGFloat = 0
    /// While the handle is held: where the drag began, the width it would leave, whether letting go closes the list.
    /// Gesture state: a drag the system cancels leaves nothing behind.
    @GestureState private var live: Live?

    struct Live: Equatable {
        var start: CGFloat
        var width: CGFloat
        var closing: Bool
    }

    init(columns: Binding<NavigationSplitViewVisibility>, key: String?) {
        _columns = columns
        self.key = key
        _width = StoredValue(wrappedValue: 0, (key ?? "split.list") + ".width")
        _closed = StoredValue(wrappedValue: false, (key ?? "split.list") + ".closed")
    }

    private var shown: Bool { columns != .detailOnly }
    private var phone: Bool { UIDevice.current.userInterfaceIdiom == .phone }

    /// The least and the most the list is dragged to: an iPhone Duo's open display is narrower than an iPad; the page
    /// keeps `splitDetailMin` beside it.
    private var least: CGFloat { phone ? Theme.Size.sidebarMinPhone : Theme.Size.sidebarMin }
    private var most: CGFloat { max(least, min(Theme.Size.sidebarMax, total - Theme.Size.splitDetailMin)) }
    private func fitted(_ w: CGFloat) -> CGFloat { min(max(w, least), most) }

    /// The width the list has now: the one kept (fitted to the room there is, which a turn or another window changes),
    /// else the system's as laid out.
    private var current: CGFloat { width > 0 ? fitted(CGFloat(width)) : max(least, measured) }

    /// The width the column is given: the one being dragged to, else the one kept; nil: the system's. On iPhone Duo a
    /// column with no width yet takes one only when the drag ends (`SidebarWidth`).
    private var applied: CGFloat? {
        if let live, !(phone && width == 0) { return live.width }
        return width > 0 ? current : nil
    }

    func body(content: Content) -> some View {
        #if os(iOS)
        if let key, UIDevice.current.userInterfaceIdiom == .pad || phone {
            content
                .environment(\.splitListWidth, applied)
                .onPreferenceChange(SidebarEdge.self) { edge in if live == nil { measured = edge - origin } }
                .onGeometryChange(for: CGRect.self) { $0.frame(in: .global) } action: { origin = $0.minX; total = $0.width }
                .overlay(alignment: .leading) {
                    handle
                        .offset(x: shown ? (live?.width ?? current) - Theme.Split.handleWidth / 2 : 0)
                        .accessibilityIdentifier("split.divider." + key)
                }
                .onAppear { if closed, shown { columns = .detailOnly } }
                .onChange(of: columns) { _, now in closed = now == .detailOnly }
        } else {
            content
        }
        #else
        content
        #endif
    }

    /// The handle: a short bar on the line (at the page's edge while the list is closed), its touch area wider than it.
    private var handle: some View {
        Capsule()
            .fill(live?.closing == true ? Theme.danger : live == nil ? Theme.Split.handle : Theme.Split.handleHeld)
            .frame(width: Theme.Size.barWide, height: Theme.Size.tapTarget)
            .scaledFrame(width: Theme.Split.handleWidth, height: Theme.Split.handleHeight)
            .contentShape(.rect)
            .gesture(drag)
            .onTapGesture(count: 2) { width = 0 } // the system's column again
            .animation(Theme.Motion.standard, value: live?.closing)
            .accessibilityElement()
            .accessibilityLabel(shown ? String(localized: "Resize the list") : String(localized: "Show List"))
            .accessibilityAddTraits(.isButton)
            .accessibilityAction { if !shown { withAnimation(Theme.Motion.layout) { columns = SplitColumns.shown } } }
            .accessibilityAdjustableAction { direction in
                guard shown else { return }
                switch direction {
                case .increment: width = Double(fitted(current + Theme.Split.step))
                case .decrement: width = Double(fitted(current - Theme.Split.step))
                @unknown default: break
                }
            }
            .accessibilityActions {
                if shown {
                    Button(String(localized: "Hide List")) { withAnimation(Theme.Motion.layout) { columns = .detailOnly } }
                }
            }
    }

    private var drag: some Gesture {
        DragGesture(minimumDistance: Theme.Split.slop, coordinateSpace: .global)
            .updating($live) { g, state, _ in
                // Mostly sideways, or it is the list's or the page's scroll and taps, not this.
                guard shown, state != nil || abs(g.translation.width) > abs(g.translation.height) else { return }
                let start = state?.start ?? current
                let wanted = start + g.translation.width
                state = Live(start: start, width: fitted(wanted), closing: wanted < least - Theme.Split.closesPast)
            }
            .onEnded { g in
                guard abs(g.translation.width) > abs(g.translation.height) else { return }
                if shown {
                    let wanted = current + g.translation.width
                    if wanted < least - Theme.Split.closesPast {
                        withAnimation(Theme.Motion.layout) { columns = .detailOnly }
                    } else if abs(g.translation.width) > Theme.Split.slop {
                        width = Double(fitted(wanted))
                    }
                } else if g.translation.width > Theme.Split.opensPast {
                    withAnimation(Theme.Motion.layout) { columns = SplitColumns.shown }
                }
            }
    }
}

#if os(iOS)
/// The drawer's button at the leading edge of a first page's bar in the Sidebar layout on a phone (#6332): the persona's
/// face as on the conversation (`ShellPersonaButton`, the inbox's mark), with how many Agents need the user. Only where
/// it shows; everywhere else the page is left exactly as it was.
struct DrawerButtonItem: ViewModifier {
    var shown: Bool
    @Environment(\.splitShowList) private var showList

    func body(content: Content) -> some View {
        if shown {
            content.toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    ShellPersonaButton(id: "sidebar-drawer-button") { showList?.show() }
                        .overlay(alignment: .topTrailing) { SidebarWaitingBadge() }
                }
                .sharedBackgroundVisibility(.hidden)
            }
        } else {
            content
        }
    }
}
#endif
