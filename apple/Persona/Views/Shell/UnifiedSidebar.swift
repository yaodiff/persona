import SwiftUI

/// The Sidebar layout (Settings › Appearance › Navigation, #6332; docs/apple/ios.md「Sidebar layout」): one sidebar with
/// the main conversation, the pages, Persona's chats and the Agents list, as the web app's, and the page picked in it
/// beside it. What the column's pieces share: the Persona rows' state (search, dialogs), what the page beside it shows,
/// and where the Agents' Needs You section is in the list.
@MainActor @Observable final class UnifiedSidebar {
    /// The Persona rows' search and dialogs (`AssistantSidebarUI`), shared by the head, the rows in the Agents list and
    /// the presenters around the column. Lazy: made when the column is first drawn (P3).
    @ObservationIgnored lazy var ui = AssistantSidebarUI()
    /// The page beside the sidebar is the Persona conversation itself, nothing over it: its row is the one shaded.
    var atRoot = true
    /// The place shown beside the sidebar (Inbox, To-dos, Machines…), its row shaded; nil for a conversation or an Agent.
    var shownPlace: ShellPage?
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
enum SidebarRoute: Hashable, Sendable {
    case health, messages
}

extension ShellPage {
    /// A place the sidebar lists, its row shaded while it is beside the sidebar.
    var isSidebarPlace: Bool {
        switch self {
        case .inbox, .todos, .routines, .knowledge, .machines, .health, .recordings, .extensionPage, .messages: true
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
    private var inDrawer: Bool { split?.drawer == true }
    let state: UnifiedSidebar
    /// The Agents list, given the Persona rows to hold above its own.
    @ViewBuilder var list: (AssistantSidebar) -> Agents

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("UnifiedSidebarColumn")
        #endif
        VStack(spacing: 0) {
            persona(.header)
            persona(.main)
            list(persona(.rows))
            SidebarNeedsJump(state: state)
            SidebarUsageStrip { show(.machines) }
            persona(.footer)
        }
        .modifier(AssistantSidebarPresenters(ui: state.ui, open: open))
        // A container: on its own the identifier went onto every element in it, the rows' own lost.
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("unified-sidebar")
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
                         openRecordingsPage: { show(.recordings) },
                         openMessages: LarkHomePreference.shared.shows ? { show(.messages) } : nil,
                         part: part, shared: state.ui, shownPlace: state.shownPlace)
    }

    private func open(_ key: SessionKey) {
        model.assistantCurrent = key
        show(.conversation)
    }

    private func show(_ page: ShellPage) {
        state.shownPlace = page.isSidebarPlace ? page : nil
        router?.show(page)
        if inDrawer { state.drawer.close() }
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
    var openMachines: () -> Void
    private let store = UsageWindowsStore.shared

    var body: some View {
        let accounts = UsageWindows.top(UsageWindows.live(store.accounts))
        VStack(spacing: 0) {
            // Always in the column, so the numbers are asked for before there are any (a task on nothing never runs).
            Color.clear.frame(height: Theme.Size.nonvisualProbe)
                .tabActiveTask(id: model.groupIDs.joined(separator: ",")) { await store.keepFresh(model) }
            if !accounts.isEmpty {
                Button(action: openMachines) {
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
        } else {
            content
        }
    }
}
#endif
