import SwiftUI
import PersonaWire

/// The Sidebar layout (Settings › Appearance › Navigation, #6332; docs/apple/ios.md「Sidebar layout」): one sidebar with
/// the main conversation, the pages, Persona's chats and the Agents list, as the web app's, and the page picked in it
/// beside it. What the column's pieces share: the Persona rows' state (search, dialogs), what the page beside it shows,
/// and where the Agents' Needs You section is in the list.
@MainActor @Observable final class UnifiedSidebar {
    /// Which part of the one sidebar a column shows (#4206): the whole of it (the Sidebar layout), or in the Separate
    /// layout at regular width each tab its own part, in the same column (header, width, color, slide, foot).
    enum Part {
        /// The main conversation, the pages, Persona's chats and the Agents list (Merged).
        case whole
        /// The Persona tab's: the main conversation, the pages and Persona's chats.
        case persona
        /// The Agents tab's: the Agents list, under the same head.
        case agents
    }

    let part: Part

    init(part: Part = .whole) {
        self.part = part
    }

    /// The Persona rows' search and dialogs (`AssistantSidebarUI`), shared by the head, the rows in the Agents list and
    /// the presenters around the column. Lazy: made when the column is first drawn (P3).
    @ObservationIgnored lazy var ui = AssistantSidebarUI()
    /// The Separate layout's Agents tab (#8596): the Persona tab's part, whose Pages heading gives the line the head's face
    /// stands on (`AssistantSidebarUI.rowsLeading`). The Agents tab has no Pages heading, and its own list's rows start at
    /// another inset on the iPad than on the open Duo, so it takes the Persona tab's line once that is measured.
    @ObservationIgnored weak var linedUpWith: UnifiedSidebar?
    /// The page beside the sidebar: its root, picked in the sidebar, and the drill-down path each root was left with
    /// (#8359, `SidebarPageRoots`). A pick replaces the root; only what is opened inside the page is pushed.
    /// Starts as a constant, no call: `RootView` holds a `UnifiedSidebar` in `@State` (performance rule P3).
    private(set) var roots = SidebarPageRoots.start
    /// The root in front.
    var root: SidebarPageRoot { roots.current }
    // The column's one selected row (#11032) is the root in front, read by each kind of row below: at most one of them
    // holds at a time, a pick in the column changes it, and a page opened from inside the page (pushed on the root, a
    // drill-down) never does.
    /// The page beside the sidebar is the Persona conversation (with or without pages opened from it): its row is the
    /// one shaded (the main conversation's, or the Persona chat's in it).
    var atRoot: Bool { roots.current == .conversation }
    /// The place shown beside the sidebar (Inbox, To-dos, Machines…), its row shaded while it is the page's root, also
    /// with pages opened from it; nil for a conversation or an Agent.
    var shownPlace: ShellPage? { roots.current.place }
    /// The Agent whose chat is the page's root, its row in the Agents list shaded, also with pages opened from it; nil
    /// for the conversation, a place, or another page the Agents list opened (a space, a machine).
    var shownAgent: SessionKey? { roots.current.value?.base as? SessionKey }

    /// `root` picked (its row, a route) while the page shows `path` over the root in front: the root replaced at once,
    /// or the root in front back at its root (`SidebarPageRoots.select`). Only the pop of a re-pick animates.
    @discardableResult
    func select(_ root: SidebarPageRoot, path: Binding<NavigationPath>, contentChanged: Bool = false,
                fresh: Bool = false) -> SidebarPageRoots.Step {
        if root != askedFromOutside { askedFromOutside = nil }
        return apply(path: path) { roots.select(root, path: path.wrappedValue, contentChanged: contentChanged, fresh: fresh) }
    }

    /// An Agent's chat asked for from outside (a notification, `-openSession`) and put in front as the root (#8603):
    /// what the app came up for. Cleared once anything else is picked.
    @ObservationIgnored var askedFromOutside: SidebarPageRoot?

    /// Another assistant or hub: every root was the one before's (`SidebarPageRoots.forget`). `keepingAsked`: a chat
    /// asked for from outside stays in front (the assistant read at launch, after the chat was opened, counts as a
    /// switch; a notification's chat of another assistant stays, as `AppModel.assistantManagerChanged` keeps it).
    func forget(path: Binding<NavigationPath>, keepingAsked: Bool = false) {
        if keepingAsked, let asked = askedFromOutside, roots.current == asked { return }
        askedFromOutside = nil
        // Nothing to forget: written again all the same, the roots drew everything that reads them again, on every
        // choice of the Agents tab, in the frame the tab bar's lens starts in (#10270).
        if roots.forgotten, path.wrappedValue.isEmpty { return }
        apply(path: path) { roots.forget() }
    }

    private func apply(path: Binding<NavigationPath>, _ make: () -> SidebarPageRoots.Step) -> SidebarPageRoots.Step {
        var t = Transaction()
        t.disablesAnimations = true
        // The root and the path change in one update, without animation: the page is replaced, not pushed or popped.
        let step = withTransaction(t) {
            let s = make()
            if !s.animated, path.wrappedValue != s.path { path.wrappedValue = s.path }
            return s
        }
        if step.animated, path.wrappedValue != step.path {
            withAnimation { path.wrappedValue = step.path }
        }
        return step
    }
    /// How much of the Agents' Needs You section is on screen in the list (its heading and its rows, `NeedsAnchor`), and,
    /// when none is, whether it was scrolled past (above) or not reached yet (below): the jump's arrow (`SidebarNeedsJump`).
    private(set) var needsOnScreen = 0
    var needsAbove = false
    var needsShown: Bool { needsOnScreen > 0 }

    func needsAppeared() { needsOnScreen += 1 }
    func needsGone() { needsOnScreen = max(0, needsOnScreen - 1) }
    #if os(iOS)
    /// The phone's drawer the column slides in from (#6332's iPhone layout; the iPad's column has none). Lazy (P3).
    @ObservationIgnored lazy var drawer = AssistantDrawer()
    #endif
    /// Scrolls the list to the Agents' Needs You section, or to the Agents' heading (the Agents list sets them).
    @ObservationIgnored var scrollToNeeds: (() -> Void)?
    @ObservationIgnored var scrollToAgents: (() -> Void)?

    /// The column's coordinate space, where its parts measure where the list's rows start (`AssistantSidebarUI.rowsLeading`).
    nonisolated static let columnSpace = "unified.column"
    /// The list's anchors (`ScrollViewReader` ids).
    nonisolated static let agentsAnchor = "unified.agents"
    nonisolated static let needsAnchor = "unified.needs"

    /// Where the Needs You section's top is in the list (its scroll view's coordinates) as it moves: above the top edge
    /// once scrolled past.
    func needsMoved(_ minY: CGFloat) {
        let above = minY < 0
        if needsAbove != above { needsAbove = above }
    }
}

extension UnifiedSidebar {
    /// The column's one color (#4206, the owner: as ChatGPT's, the shell drawer's neutral near-white / near-black, in every
    /// style and at every width): behind its head, its list and its foot, in the strip over it on the iPad, and behind
    /// the phone's drawer. The system sidebar's grey-blue it had stayed under the head and the foot only in the styles
    /// whose list paints the page's own color, three bands down the column (#4855).
    @MainActor static var columnColor: Color { Theme.Shell.drawer }
}

extension EnvironmentValues {
    /// The Sidebar layout's shared state, under its column and the page beside it; nil in the Separate layout.
    @Entry var unifiedSidebar: UnifiedSidebar? = nil
    /// The Sidebar layout's one path for the page beside the sidebar (the split's): the Persona page, its root, pushes
    /// its pages on it; nil elsewhere.
    @Entry var unifiedPath: Binding<NavigationPath>? = nil
}

/// How many Agents need the user, on the Sidebar layout's sidebar button while the column is folded away (#6332): the
/// count the Separate layout's switch and tabs show (`agentsWaiting`), in the same badge. Its own view, so only it is
/// drawn again as the count changes.
struct SidebarWaitingBadge: View {
    @Environment(AppModel.self) private var model

    var body: some View {
        let waiting = model.agentsWaiting
        if waiting > 0 {
            CountBadge(count: waiting, look: .needs, label: StateName.count(.needsYou, waiting))
                .offset(x: Theme.Space.xs, y: -Theme.Space.xs)
                .allowsHitTesting(false)
                .accessibilityIdentifier("sidebar-button-needs")
        }
    }
}

/// The pages the Sidebar layout opens beside its sidebar by value that are not the shell's places (`ShellRoute`), which
/// the Persona page registers (Health needs its state): pushed onto the one path the page beside the sidebar has, a pick
/// replacing what was there (as the web's routes). In the other layouts Health is presented by flag, as before.
nonisolated enum SidebarRoute: Hashable, Sendable {
    case health, messages
    /// A domain space's home and the new space form (#13500), drawn as Health's page is.
    case space(String), newSpace

    /// The id of the module shell page that draws `.health` (the `health` module's, `ModuleSlot.shellPlaces`).
    static let healthPage = "health"
}

nonisolated extension ShellPage {
    /// A place the sidebar lists, its row shaded while it is beside the sidebar.
    var isSidebarPlace: Bool {
        switch self {
        case .inbox, .todos, .routines, .knowledge, .machines, .health, .recordings, .extensionPage, .messages, .space, .newSpace: true
        #if os(iOS)
        case .starred: true
        #endif
        case .newConversation, .profile, .conversation: false
        }
    }
}

#if os(iOS)
/// The Sidebar layout's column: the assistant and Search, the main conversation held at the top, then one list (the
/// Agents list, with the pages and Persona's chats as its first rows, `list`), and at the foot the Needs You jump, the
/// subscription usage, New Chat and Settings. The same column in the iPad's split and (#6332's second part) the iPhone's
/// drawer.
struct UnifiedSidebarColumn<Agents: View>: View {
    @Environment(AppModel.self) private var model
    @Environment(\.padRouter) private var router
    /// A phone's width: the column is the drawer's (`UnifiedSidebar.drawer`), which a pick closes. The split says so
    /// (`SplitDetail.drawer`): an iPad's sidebar column can read compact by its own width.
    @Environment(\.splitDetail) private var split
    @Environment(\.splitShowList) private var showList
    private var inDrawer: Bool { split?.drawer == true }
    let state: UnifiedSidebar
    /// The Agents list, given the Persona rows to hold above its own (none for the Agents tab's part).
    @ViewBuilder var list: (AssistantSidebar?) -> Agents

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("UnifiedSidebarColumn")
        #endif
        VStack(spacing: 0) {
            // The head and the main row take the column's width and no more (#12318): they stand on the rows' line
            // (`rowsLeading`), measured in the list below, so a head that could widen the column moved the list, which
            // moved the line, which moved the head, round and round inside one layout pass.
            persona(.header).frame(minWidth: .zero, maxWidth: .infinity)
            switch state.part {
            case .whole:
                persona(.main).frame(minWidth: .zero, maxWidth: .infinity)
                list(persona(.rows))
                SidebarNeedsJump(state: state)
            case .persona:
                persona(.main).frame(minWidth: .zero, maxWidth: .infinity)
                personaList
            case .agents:
                list(nil)
                SidebarNeedsJump(state: state)
            }
            SidebarUsageStrip { show(.machines) }
            persona(.footer)
        }
        .coordinateSpace(.named(UnifiedSidebar.columnSpace))
        // Typing in the page beside it brings the keyboard up under both: the column stays where it is, its foot
        // (New Chat, Search, Settings) not riding up with the keyboard (#4206, the owner). It holds no field of its own.
        .ignoresSafeArea(.keyboard, edges: .bottom)
        .modifier(AssistantSidebarPresenters(ui: state.ui, open: open))
        .background { UnifiedSidebar.columnColor.ignoresSafeArea() }
        // A container: on its own the identifier went onto every element in it, the rows' own lost.
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("unified-sidebar")
    }

    /// The Persona tab's rows (the Separate layout, #4206): the pages and Persona's chats in a list of their own, as they
    /// sit over the Agents in the whole sidebar.
    private var personaList: some View {
        List { persona(.rows) }
            .listStyle(.plain)
            .scrollContentBackground(.hidden)
            .contentMargins(.horizontal, Theme.Space.l, for: .scrollContent)
            .environment(\.defaultMinListRowHeight, Theme.Space.flush)
    }

    /// A piece of the Persona sidebar, every place it lists opening beside the sidebar.
    private func persona(_ part: AssistantSidebarPart) -> AssistantSidebar {
        AssistantSidebar(current: state.atRoot ? model.assistantCurrent : nil,
                         open: open,
                         newConversation: { show(.newConversation) },
                         openProfile: { show(.profile) },
                         openRoutines: { show(.routines) },
                         openKnowledge: { show(.knowledge) },
                         openSettings: { state.drawer.close(); model.shellSettings = true },
                         drawer: inDrawer ? state.drawer : nil,
                         openInbox: { show(.inbox) },
                         openTodos: { show(.todos) },
                         onMachines: { show(.machines) },
                         onOpenExtension: { show(.extensionPage($0)) },
                         openHealth: { show(.health) },
                         openSpace: { show(.space($0)) },
                         openNewSpace: { show(.newSpace) },
                         openRecordingsPage: { show(.recordings) },
                         openMessages: LarkHomePreference.shared.shows ? { show(.messages) } : nil,
                         openStarred: { show(.starred) },
                         part: part, shared: state.ui, shownPlace: state.shownPlace,
                         foldColumn: inDrawer ? nil : showList?.hide)
    }

    private func open(_ key: SessionKey) {
        model.assistantCurrent = key
        show(.conversation)
    }

    private func show(_ page: ShellPage) {
        // The Agents tab's foot opens Persona's pages (New Chat, Settings' places) on the Persona tab (#4206).
        if state.part == .agents, model.selectedTab != .assistant { model.selectedTab = .assistant }
        router?.show(page)
        if inDrawer { state.drawer.close() }
        // Laid over the page (the open Duo upright): a pick slides the column away, even one that leaves the page as it
        // was (#4852); a pick that changes the page does it through the split's path too.
        showList?.hideOverlay?()
    }
}

/// "3 need you ↓" over the column's foot while Agents wait for the user (the count the Separate layout's switch and tabs
/// show, `agentsWaiting`): a tap scrolls the list to the Agents' Needs You section. A band of its own above the usage,
/// so it never covers a row; it stays while anything waits (the arrow says which way the section is, none while it is
/// on screen), so the list's foot does not jump as it scrolls.
struct SidebarNeedsJump: View {
    @Environment(AppModel.self) private var model
    let state: UnifiedSidebar

    var body: some View {
        let waiting = model.agentsWaiting
        if waiting > 0 {
            Button {
                Haptics.tap()
                state.scrollToNeeds?()
            } label: {
                HStack(spacing: Theme.Space.xs) {
                    Text(StateName.count(.needsYou, waiting)).font(Theme.Font.labelStrong).lineLimit(1)
                    if !state.needsShown {
                        ThemedIcon(state.needsAbove ? Theme.Icon.up : Theme.Icon.down).font(Theme.Font.label)
                    }
                }
                .foregroundStyle(StatusPalette.needsYou.text)
                .padding(.horizontal, Theme.Space.m)
                .scaledFrame(minHeight: Theme.Size.wellS)
                .surface(interactive: true, in: .capsule)
                .contentShape(.capsule)
            }
            .buttonStyle(.plain)
            .dynamicTypeSize(...Theme.Shell.topBarLargestTypeSize)
            .frame(maxWidth: .infinity)
            .padding(.vertical, Theme.Space.xs)
            .accessibilityLabel(StateName.count(.needsYou, waiting))
            .accessibilityHint(String(localized: "Shows the Agents that need you [Sidebar Layout]"))
            .accessibilityIdentifier("sidebar-needs-jump")
        }
    }
}

/// The subscription usage at the foot (the web sidebar's, #2432): the two fullest accounts (`UsageWindows.top`), each
/// its name, how full and a bar. Nothing until a hub has numbers. A tap opens Machines, whose Subscription Usage card
/// has every window.
struct SidebarUsageStrip: View {
    @Environment(AppModel.self) private var model
    /// Not `openMachines`: a stored property's name stays in the Release binary, and that is a DEBUG launch switch (#8306).
    var onOpenMachines: () -> Void
    private let store = UsageWindowsStore.shared

    var body: some View {
        let accounts = UsageWindows.top(UsageWindows.live(store.accounts))
        VStack(spacing: 0) {
            // Always in the column, so the numbers are asked for before there are any (a task on nothing never runs).
            Color.clear.frame(height: Theme.Size.nonvisualProbe)
                .tabActiveTask(id: model.groupIDs.joined(separator: ",")) { await store.keepFresh(model) }
            if !accounts.isEmpty {
                Button(action: onOpenMachines) {
                    HStack(spacing: Theme.Space.l) {
                        ForEach(accounts) { a in
                            let used = UsageWindows.fullest(a)
                            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                                HStack(spacing: Theme.Space.xs) {
                                    Text(a.name).lineLimit(1)
                                    Spacer(minLength: Theme.Space.xs)
                                    Text(UsageWindows.percentText(used)).monospacedDigit().lineLimit(1)
                                }
                                .font(Theme.Font.label)
                                .foregroundStyle(.secondary)
                                UsageMeter(used: used)
                            }
                            .accessibilityElement(children: .ignore)
                            .accessibilityLabel([a.name, UsageWindows.percentText(used)]
                                .joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label")))
                        }
                    }
                    .padding(.horizontal, Theme.Space.xl)
                    .padding(.top, Theme.Space.s)
                    .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .dynamicTypeSize(...Theme.Shell.topBarLargestTypeSize)
                .accessibilityIdentifier("sidebar-usage")
            }
        }
    }
}
#endif

#if os(iOS)
/// Registers the Sidebar layout's pages (`SidebarRoute`) on the Persona page, the root of the page beside the sidebar,
/// which draws them (`AssistantView.sidebarPage`); first pages of the split's detail, no back to the conversation.
struct SidebarRouteDestinations<Page: View>: ViewModifier {
    var enabled: Bool
    @ViewBuilder var page: (SidebarRoute) -> Page

    func body(content: Content) -> some View {
        if enabled {
            content.navigationDestination(for: SidebarRoute.self) { route in page(route).splitDetailPage() }
                // A task's detail opened from inside the page (#13156), pushed as `detailPresentation` pushed it by view.
                .navigationDestination(for: TaskDetailRoute.self) { r in
                    Group {
                        if let id = r.todo {
                            TaskDetailSheet(todoID: id, initialAgent: r.taskAgent)
                        } else if let key = r.agent {
                            TaskDetailSheet(agent: key)
                        }
                    }
                    .id(r)
                    .environment(\.detailPushed, true)
                }
        } else {
            content
        }
    }
}
#endif
