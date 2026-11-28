import BeautifulUI
import OSLog
import SwiftUI
import PersonaWire

/// Rendered rows omit pinned Agents; worktree navigation and actions still address the complete direct inventory.
@MainActor enum WorkspaceNavigation {
    static func directSessions(_ workspace: HostWorkspace, inventory: [Session], catalog: [HostWorkspace]) -> [Session] {
        inventory.filter { !$0.assistant && workspace.contains($0, catalog: catalog) }
    }

    static func firstAgent(rendered: [Session], workspace: HostWorkspace?, inventory: [Session], catalog: [HostWorkspace],
                           ordered: ([Session]) -> [Session]) -> SessionKey? {
        let direct = rendered.isEmpty ? workspace.map { directSessions($0, inventory: inventory, catalog: catalog) } ?? [] : rendered
        return ordered(direct).first?.key
    }
}

/// Agents that share a space (a herdr workspace, or whatever groups agents in another host) on one machine.
nonisolated struct SpaceGroup: Identifiable, Sendable {
    var id: String // machineID/spaceID, "group|machineID/spaceID" for another group of hubs
    var machineID: String
    var spaceID: String
    /// By Project can use a workspace ID when the session has no terminal space ID.
    var workspaceGroupID: String? = nil
    /// The group of hubs its machine is in (docs/protocol/multi-hub.md); "" = the default group.
    var group: String = ""
    var machineRef: MachineRef { MachineRef(group: group, id: machineID) }
    var name: String
    var projectFolder: AgentProjectFolder? = nil
    var index: Int
    var sessions: [Session] // in tab order
    /// The hub project key for whole-space todo links; nil outside a herdr workspace.
    var todoSpace: String? = nil
    var aggregate: SessionListCounts? = nil
    var bounded = false

    var headingLabel: String { [name, projectFolder?.qualifier].compactMap { $0 }.joined(separator: " · ") }

    static func withProjectFolders(_ groups: [Self], machines: [MachineRef: Machine]) -> [Self] {
        let sharedNames = Dictionary(grouping: groups, by: \.machineRef).mapValues { sameComputer in
            Dictionary(grouping: sameComputer) { $0.name.lowercased() }.filter { $0.value.count > 1 }
        }
        return groups.map { group in
            guard group.workspaceGroupID != nil || (group.spaceID != "-" && !group.spaceID.isEmpty) else { return group }
            var counts: [String: Int] = [:]
            for row in group.sessions {
                let path = row.workspaceCWD.flatMap { $0.isEmpty ? nil : $0 } ?? row.cwd
                if !path.isEmpty { counts[path, default: 0] += 1 }
            }
            guard let folder = counts.min(by: { left, right in
                left.value == right.value ? left.key < right.key : left.value > right.value
            })?.key else { return group }
            let qualifier = sharedNames[group.machineRef]?[group.name.lowercased()] == nil ? nil
                : AgentProjectFolder.parentLabel(folder, home: machines[group.machineRef]?.home)
            var withFolder = group
            withFolder.projectFolder = AgentProjectFolder(machine: group.machineRef, cwd: folder, qualifier: qualifier)
            return withFolder
        }
    }

    var state: AgentState { sessions.map(\.countedState).min { $0.priority < $1.priority } ?? .idle }
    var updatedAt: Date { sessions.compactMap(\.updatedAt).max() ?? .distantPast }
    /// The same from the rows' times while the Agents list follows them (`AgentsFeed.time`): the list's structure
    /// does not change for time passing, so its agents' `updatedAt` may be older (docs/apple/ios-state.md「Step 3」).
    @MainActor func updatedAt(_ feed: AgentsFeed?) -> Date {
        sessions.compactMap { feed?.time($0.id) ?? $0.updatedAt }.max() ?? .distantPast
    }
    /// Where it sorts among spaces in the same state: its agents' `sortDate`, so a working agent's every line does not
    /// move the space (a busy hub pushes every two seconds: the spaces changed places under the finger, 2026-11-01).
    var sortDate: Date { sessions.map(\.sortDate).max() ?? .distantPast }
    /// Nothing going on: every agent quiet and none waiting on a /loop or background work.
    var isQuiet: Bool { sessions.allSatisfy { $0.isQuiet && !$0.isWaiting } }

    /// Its agents in a state as counted (`Session.countedState`: Waiting in the Background is not Working). A subtask that
    /// is a row only because its parent is not drawn is not counted, as on Android and the hub (`Session.isNestedSubtask`).
    func count(_ s: AgentState) -> Int {
        bounded ? aggregate?.state[s.rawValue] ?? 0 : sessions.filter { $0.countedState == s && !$0.isNestedSubtask }.count
    }
    /// Its agents that only wait in the background (#6045): counted apart, with a clock (`StateCounts`).
    var waitingInBackground: Int { sessions.filter { $0.waitsInBackground && !$0.isNestedSubtask }.count }

    static func build(_ sessions: [Session], byProject: Bool = false) -> [SpaceGroup] {
        var groups: [String: SpaceGroup] = [:]
        for s in sessions where s.state.isLive {
            let spaceID = s.spaceID ?? "-"
            let workspace = byProject && (s.spaceID ?? "").isEmpty ? s.workspaceID.flatMap { $0.isEmpty ? nil : $0 } : nil
            let key = "\(s.machineRef.path)/\(workspace ?? spaceID)"
            if groups[key] == nil {
                let todoSpace: String?
                if let id = s.spaceID, !id.isEmpty {
                    todoSpace = s.spaceName.flatMap { $0.isEmpty ? nil : $0 } ?? id
                } else {
                    todoSpace = nil
                }
                groups[key] = SpaceGroup(id: key, machineID: s.machineID, spaceID: spaceID, workspaceGroupID: workspace, group: s.group,
                                         name: s.spaceName ?? s.folderName, index: s.spaceIndex ?? 0, sessions: [], todoSpace: todoSpace)
            }
            groups[key]!.sessions.append(s)
        }
        return groups.values.map { g -> (group: SpaceGroup, priority: Int, sortDate: Date) in
            var g = g
            // Tab order, as in herdr: an agent keeps its place whatever its state (the state shows on the row).
            // The agents of one tab together, in the order of their panes (#2320).
            g.sessions.sort(by: TabPanes.inTabOrder)
            return (group: g, priority: g.state.priority, sortDate: g.sortDate)
        }
        .sorted { a, b in
            if a.priority != b.priority { return a.priority < b.priority }
            return a.sortDate != b.sortDate ? a.sortDate > b.sortDate : a.group.id < b.group.id // one order on every render
        }
        .map(\.group)
    }
}

extension DevGrouping {
    /// The groupings offered: 「按待办」 needs the hub's todos (an old hub answers 404 and has none), By Pinned the hub's
    /// direction tree (#4352).
    static func offered(supportsTodos: Bool, supportsDirections: Bool = false) -> [DevGrouping] {
        allCases.filter { (supportsTodos || $0 != .todo) && (supportsDirections || $0 != .direction) }
    }

    /// The grouping in use: a stored 「按待办」 falls back to 「按项目」 on a hub without todos; a stored By Pinned to By
    /// To-do (or By Project) on a hub without the direction tree, or before it is read.
    static func resolved(_ stored: DevGrouping, supportsTodos: Bool, supportsDirections: Bool = false) -> DevGrouping {
        if stored == .direction && !supportsDirections { return resolved(.todo, supportsTodos: supportsTodos) }
        return stored == .todo && !supportsTodos ? .space : stored
    }

    /// Whether the hub has the direction tree By Pinned is built from.
    @MainActor static var supportsDirections: Bool { AppModel.shared.directionTree != nil }

    /// `offered` and `resolved` for what the hub has now: its todos, its direction tree.
    @MainActor static var offeredNow: [DevGrouping] {
        offered(supportsTodos: TodoStore.shared.isSupported, supportsDirections: supportsDirections)
    }
    @MainActor static func resolvedNow(_ stored: DevGrouping) -> DevGrouping {
        resolved(stored, supportsTodos: TodoStore.shared.isSupported, supportsDirections: supportsDirections)
    }
}

/// How Develop groups its agents (the menu at the top left; remembered).
enum DevGrouping: String, CaseIterable, Identifiable {
    case repo, space, todo, state, machine, app, recent
    /// By Pinned direction (#4352): the hub's direction tree, direction → to-do → Agent → sub-Agent. Not the default.
    case direction
    /// What an app that never chose a grouping shows (#2108): machines → repositories → working trees, after Orca.
    static let standard = DevGrouping.repo
    var id: String { rawValue }
    var title: String {
        switch self {
        case .repo: String(localized: "By Repository")
        case .space: String(localized: "By Project")
        case .todo: String(localized: "By To-do")
        case .state: String(localized: "By Status [Chats View 5aa882]")
        case .machine: String(localized: "By Machine")
        case .app: String(localized: "By App")
        case .recent: String(localized: "Recently Used")
        case .direction: String(localized: "By Pinned")
        }
    }
    var symbol: String {
        switch self {
        case .repo: Theme.Icon.folder
        case .space: Theme.Icon.space
        case .todo: Theme.Icon.todo
        case .state: Theme.Icon.byState
        case .machine: Theme.Icon.machine
        case .app: Theme.Icon.byApp
        case .recent: Theme.Icon.recent
        case .direction: Theme.Icon.pin
        }
    }
}

/// How the Agents list orders its agents (the same menu; remembered): the state first (default), or the latest update
/// only. Under By Status the sections are the states, so only the rows' order inside them is the time either way.
enum AgentSort: String, CaseIterable, Identifiable {
    case state, updated
    var id: String { rawValue }
    var title: String {
        switch self {
        case .state: String(localized: "By Status [Chats View 8464f8]")
        case .updated: String(localized: "Recently Updated")
        }
    }
    var symbol: String {
        switch self {
        case .state: Theme.Icon.byState
        case .updated: Theme.Icon.time
        }
    }
}

/// The quick filters over the Agents list (the bar under the search field): one tap, stacked on the machine menu.
nonisolated enum AgentFilter: String, CaseIterable, Identifiable, Sendable {
    case all, needsYou, unread, working
    /// Working, but only waiting on background commands (#6045): its own chip, as on the web.
    case backgroundWaiting
    /// Where the agent was opened (#2526): a botmux session (Lark Group) or a herdr pane (Terminal).
    case feishu, terminal
    var id: String { rawValue }
    var title: String {
        switch self {
        case .all: String(localized: "All")
        case .needsYou: StateName.needsYou.text
        case .unread: StateName.unread.text
        case .working: StateName.working.text
        case .backgroundWaiting: String(localized: "Waiting in the background")
        case .feishu: SessionSource.feishu.label
        case .terminal: SessionSource.terminal.label
        }
    }
    var state: AgentState? {
        switch self {
        case .all, .feishu, .terminal, .backgroundWaiting: nil
        case .needsYou: .needsYou
        case .unread: .unread
        case .working: .working
        }
    }
    /// The chip's colors: the state's, or Waiting in the Background's lighter amber.
    @MainActor var tone: StatusTone? { self == .backgroundWaiting ? StatusPalette.backgroundWaiting : state?.tone }
    /// A mark in place of the chip's dot: Waiting in the Background's clock.
    var symbol: String? { self == .backgroundWaiting ? Theme.Icon.backgroundWaiting : nil }
    var source: SessionSource? {
        switch self {
        case .feishu: .feishu
        case .terminal: .terminal
        default: nil
        }
    }
    /// A muted agent that is working or finished unread is not counted or listed under Unread / Working (it sits under
    /// Muted); one that needs you still is, the same as the list's order (`AgentListOrder.rank`).
    func matches(_ s: Session, muted: Bool = false) -> Bool {
        if let source { return s.source == source }
        if self == .backgroundWaiting { return s.waitsInBackground && !muted }
        guard let state else { return true }
        // One that only waits in the background is not Working (#6045).
        return s.countedState == state && (state == .needsYou || !muted)
    }
}

/// Develop: every agent across machines, grouped by repository (default), space, todo, state or machine. The navigation
/// and the toolbar are here; the list is `AgentsList`, the view that follows the hub. A toolbar declared by the view the
/// hub's pushes redraw is handed to UIKit again on each of them, and the ⋯ menu open over the list jumped back to its
/// top (2026-10-31): nothing here reads the sessions.
struct ChatsView: View {
    @Environment(AppModel.self) private var model
    /// The shell (docs/apple/ios.md「New shell」): its ☰ and Assistant | Agents switch take the navigation bar.
    @Environment(\.shell) private var shell
    /// The Sidebar layout (#6332): this list is the sidebar's, the Persona sidebar's rows over it, and the page beside it
    /// starts at the Persona conversation (`UnifiedSidebarColumn`); nil in the Separate layout.
    @Environment(\.unifiedSidebar) private var unified
    @State private var path = NavigationPath()
    /// Which space page sits at which depth of `path` (only this view pushes them), so a chat opened from a space
    /// page goes back to it instead of stacking another copy.
    @State private var spaceAt: [Int: String] = [:]
    /// Opens a space's page from a chat on this page (`\.openSpace`): the same object for the page's life (#10303).
    @State private var spaceOpener = SpaceOpener()
    /// Opens a parent session focused on Session Overview's Subagents section from a row's rollup subtitle (P5).
    @State private var subagentsOpener = SubagentsOverviewOpener()
    @State private var query = ""
    @State private var searching = false
    /// What was searched for when a result was opened: closing the search empties the system field (our text stays,
    /// so nothing writes it back), and on coming back the list showed the results under an empty field (2026-10-31).
    /// The system's search only: beside the split (`AgentsHeader` on a phone keeps its text).
    @State private var searchedBeforePush: String?
    /// Regular width (the split, iPhone Duo open): the list is a sidebar column with the system's search.
    @Environment(\.horizontalSizeClass) private var widthClass
    /// The top on a phone: its own search field and layout (#2478).
    @State private var header = AgentsHeader()
    private var ownsTop: Bool { widthClass != .regular && unified == nil }
    #if os(iOS)
    /// The Separate layout's tab at regular width, the system's tab bar across the top: the split makes the tab bar's
    /// room once (`TabBarRoomOnce`, #8596). iOS only, as `TabBarRoomOnce` and the side bars are (#9310).
    private var tabSplit: Bool {
        widthClass == .regular && !splitSideBars && (unified?.part == .persona || unified?.part == .agents)
    }
    @Environment(\.splitSideBars) private var splitSideBars
    #endif
    /// This page opens the Agents' chats asked for from outside: all but the Persona tab's (the Separate layout's new
    /// look, #4206), whose column has no Agents; the Agents tab's takes them.
    private var takesAgents: Bool { unified?.part != .persona }
    /// Where the list column's shown state is kept (`SplitListMemory`): each tab's own as before, the Sidebar layout's.
    private var listKey: String {
        switch unified?.part {
        case nil, .agents: "agents.list"
        case .persona: "assistant.list"
        case .whole: "sidebar.list"
        }
    }
    /// Every page beside the one sidebar's column (#8359, docs/design/sidebar-navigation.md option 3): the Sidebar layout's,
    /// the Persona tab's and the Agents tab's (#8603). The list picks the page's root (`UnifiedSidebar.roots`), and the path
    /// holds only what is opened inside it. The Agents tab shows "Choose an Agent" while nothing is picked.
    private var rootsMode: Bool { unified != nil }
    /// Laid out for searching (`AgentsHeader.compact`).
    private var headerCompact: Bool { header.compact }
    /// Where the shell's bar is and how far it has faded for the rising field, read apart from this body (`AgentsTopBarPlace`).
    private var topBarPlace: AgentsTopBarPlace { AgentsTopBarPlace(header: header) }
    @State private var showNew = false
    /// New Session's sheet: RootView's on iOS, which outlives the tree a fold or an unfold builds again (#2836: the sheet
    /// closed with what was typed in it), this page's elsewhere.
    @Environment(\.newAgentShown) private var hostedNewSession
    private var newSession: Binding<Bool> { hostedNewSession ?? $showNew }
    @Environment(\.shownTree) private var shownTree
    @Environment(\.builtTree) private var builtTree
    /// This page's tree is not the one the window shows: one built before, on its way out after a fold or an unfold (or a
    /// new look), which lives on a moment (#2836). The chats asked for are the shown tree's page's to open, and what the
    /// app remembers is left alone: this page took the chat the new one opened again, and opened it where no one saw it.
    private var leaving: Bool {
        guard let builtTree, let shown = shownTree?.tree else { return false }
        return shown !== builtTree
    }
    #if os(iOS)
    @State private var personalEntry = PersonalAgentEntryModel()
    private var showsPersonalEntry: Bool { personalEntry.isAvailable(for: model.api) }
    private var personalCount: Int? { personalEntry.count(for: model.api) }
    #else
    private var showsPersonalEntry: Bool { true }
    private var personalCount: Int? { nil }
    #endif
    /// The ⋯ menu's actions, kept current by the list (not observed: see `AgentsListMenu`).
    @State private var menuActions = AgentsListMenu.Actions()
    @StoredValue("dev.machineFilter") private var machineFilter = ""

    /// The list has appeared once: pushes made before that are held back (`model.openChat`, #2061).
    @State private var rootShown = false
    /// Whether the stack is in the middle of a push or a pop: a chat opened from outside waits for it (#1941).
    @State private var stackStill = StackStillWatcher()
    /// The list under the stack has been shown once (#8467): from then on it stays in the tree.
    @State private var listBuilt = false
    /// A launch onto a chat (`-openSession`, a notification, the chat open last time) on a phone leaves the list out
    /// until it could be seen (`AgentsListPending`): the chat goes over it at once, and building the list's view types
    /// took ~150 ms of the launch's main thread before the chat started. A chat on its way or open over it, and the list
    /// not shown yet; dropped, the list comes in.
    private var listWaits: Bool {
        !listBuilt && (model.openChat != nil || model.openPersonalAgent != nil || !path.isEmpty)
    }

    /// An active search keeps the system navigation bar up over what is pushed (the chat showed it over its own
    /// header; closing the search once pushed is too late): opening something from the results closes the search
    /// first and pushes on the next turn of the run loop. The search's text and results stay for coming back.
    private var pushing: Binding<NavigationPath> {
        Binding(get: { path }, set: { new in
            guard new.count > path.count else { return path = new }
            if ownsTop, searching || header.floating {
                // The custom field holds no navigation bar up: close it and push in the same turn.
                header.closeSearchForPush(wasSearching: searching)
                searching = false
                return path = new
            }
            guard searching else { return path = new }
            searchedBeforePush = query
            searching = false
            DispatchQueue.main.async { path = new }
        })
    }

    /// Programmatic list opens close the custom field just like a NavigationLink. System search keeps its own closing.
    private var listPushPath: Binding<NavigationPath> { ownsTop ? pushing : $path }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("ChatsView")
        #endif
        // Closing the search before a push from its results redraws the navigation with it, as when the list was
        // drawn here: drawn by the list alone, the chat came up under the system navigation bar again (SearchOpenTests).
        let _ = searching
        SplitNavigation(path: pushing, listKey: listKey,
                        destinations: AgentsDestinations(path: pushing, unifiedPath: unified == nil ? nil : pushing),
                        columnColor: unified == nil ? nil : UnifiedSidebar.columnColor,
                        rootsSwap: rootsMode, selection: rootsMode ? unified.map { AnyHashable($0.root) } : nil) {
            listColumn
        } empty: {
            if rootsMode, let unified, unified.part == .agents {
                // The Agents tab (#8603): the Agent picked in the list (or a space, a computer…) is the root, replaced
                // at once; nothing picked, "Choose an Agent".
                Group {
                    if let value = unified.root.value {
                        SidebarAgentsRootPage(value: value)
                            .id(value)
                            .sidebarRootPage(ownsBack: value.base is SessionKey)
                    } else {
                        SplitPlaceholder(symbol: Theme.Icon.agents, title: String(localized: "Choose an Agent"),
                                         message: String(localized: "Click an agent in the list on the left to open its chat here."))
                    }
                }
                #if DEBUG && os(iOS)
                // On the page itself: on the tab's outer background the tab's badge count took the probe's value.
                .background { if ScreenProbe.enabled { SidebarPageProbe(root: unified.root, path: path.count, name: "sidebar-page-agents") } }
                #endif
            } else if rootsMode {
                // The Sidebar layout (#6332) and the Persona tab's (#4206): the page beside the sidebar. `AssistantView`
                // (embedded) draws the root the sidebar picked, the conversation or another page (#8359).
                AssistantView()
            } else {
                SplitPlaceholder(symbol: Theme.Icon.agents, title: String(localized: "Choose an Agent"),
                                 message: String(localized: "Click an agent in the list on the left to open its chat here."))
            }
        }
        .environment(\.unifiedPath, unified == nil ? nil : pushing)
        #if os(iOS)
        // On a phone the sidebar is the drawer, the Persona conversation the page under everything it opens (#6332).
        .environment(\.splitDrawer, unified?.drawer)
        #endif
        .modifier(UnifiedFront(unified: unified, atRoot: rootsMode ? unified?.atRoot == true && path.isEmpty : path.isEmpty))
        #if DEBUG && os(iOS)
        .background { if rootsMode, ScreenProbe.enabled, let unified, unified.part != .agents { SidebarPageProbe(root: unified.root, path: path.count) } }
        #endif
        #if os(iOS)
        .tabActiveTask(id: model.api.map { PersonalAgentEntryModel.Connection($0) }) {
            await personalEntry.watch(model.api)
        }
        #endif
        // While searching the field rises into the bar the shell's floats over: the bar makes way (the field has its
        // own Cancel), as a system search takes the navigation bar, and comes back when the search closes (TestFlight 70:
        // the field was drawn under ☰, the switch and New Session). A result opened closes the search before the push
        // (`searchedBeforePush` set): the bar stays away rather than flash back for that moment.
        .overlay(alignment: .top) {
            ZStack {
                if let shell, path.isEmpty, ownsTop, !headerCompact {
                    // Fades with the field coming up into its row (`AgentsTopBarFade`), from the same animation. Gone
                    // once the field is up (`compact`: set when the rise ends, cleared before the way down): faded out,
                    // its controls were still in the accessibility tree over the field, ☰ found there and tappable.
                    AgentsTopBar(shell: shell, menuActions: menuActions) { newSession.wrappedValue = true }
                        .equatable()
                        .searchTraceMark("topbar")
                        .modifier(topBarPlace)
                        .allowsHitTesting(!searching)
                        .accessibilityHidden(searching)
                        .transition(.opacity)
                } else if let shell, path.isEmpty, !searching, searchedBeforePush == nil {
                    AgentsTopBar(shell: shell, menuActions: menuActions) { newSession.wrappedValue = true }
                        .equatable()
                        .transition(.opacity)
                }
            }
            .animation(Theme.Motion.state, value: ownsTop ? false : searching) // the bar only: not the list under it
        }
        .overlay {
            if ownsTop, path.isEmpty {
                AgentsSearchOverlay(header: header, shell: shell != nil, query: $query, begin: beginSearch,
                                    focused: riseSearch, cancel: endSearch)
            }
        }
        .animation(Theme.Motion.state, value: path.isEmpty)
        .environment(\.openSpace, spaceOpener.set { space in
            if spaceAt[path.count - 2] == space { path.removeLast() } else { pushSpace(space) }
        })
        .environment(\.openSubagentsOverview, subagentsOpener.set { key in
            #if os(iOS)
            AgentMenuRouting.shared.open(.subagents, at: key)
            #endif
            if searching { searching = false }
            openFromList(key)
        })
    }

    /// The list column: its own property, so Xcode 26.0 type-checks it apart from the split around it (#5333, #7173).
    /// Erased at its base (#6735): unoptimized, this getter reserves a stack slot for every modifier below, each holding
    /// the whole column (`MainPagesStackTests`).
    ///
    /// All of its modifiers in one chain are more than Xcode 26.0 type-checks as one expression (#9379, #7292: again after
    /// #7302 moved it here). So the chain is cut into four stages, each a function of the one before it that returns
    /// `some View`, and Swift types each apart. What a closure does is a method below the stages, not statements inside
    /// the chain, and the operator expressions that feed it are typed `let`s. The stages add the modifiers in the order the
    /// chain had, so the type built, its identity and what this getter reads are what they were. A stage that grows past a
    /// handful of modifiers is cut again.
    private var listColumn: some View {
        let column = AnyView(agentsColumn)
        let probed = listColumnProbes(column)
        let barred = listColumnBar(probed)
        let following = listColumnFollowing(barred)
        return listColumnOpening(following)
    }

    /// Stage 1 of `listColumn`: the header it hands the list, what it puts behind the list and the way back from a search.
    private func listColumnProbes(_ column: some View) -> some View {
        let clearsShellBar: Bool = shell != nil && !ownsTop && !searching
        return column
            .environment(header)
            // The shell's top bar floats over the navigation bar: beside the split this page's own bar makes room
            // for it (not the navigation container: a chat pushed on it must not move, nor the list on the way
            // back, #340). On a phone the room is the list's first row's (`AgentsHeader`).
            .background { ShellNavigationClearance(active: clearsShellBar) }
            .background { StackStillProbe(watcher: stackStill) }
            #if os(iOS)
            .background { if tabSplit { TabBarRoomOnce() } }
            #endif
            .onChange(of: path.isEmpty) { _, atList in backAtList(atList) }
    }

    /// Stage 2: the bar's items (on the Mac, buttons in the list column's own title row) and New Session's sheet.
    private func listColumnBar(_ column: some View) -> some View {
        withBarItems(column)
            .sheet(isPresented: $showNew) { NewChatView { session in newSessionStarted(session) } }
    }

    @ViewBuilder private func withBarItems(_ column: some View) -> some View {
        if columnHidesBar {
            column
        } else if #available(iOS 26.0, *) {
            column.pageToolbar { barItems }
        } else {
            column.pageToolbar { barItemsBeforeGlass }
        }
    }

    /// Stage 3: following the stack: the shell told where the page is, the path, the sidebar's pick and the Agents tab
    /// chosen again.
    private func listColumnFollowing(_ column: some View) -> some View {
        let workAtRoot: Bool = path.isEmpty && !searching
        let pickedRoot: SidebarPageRoot? = rootsMode ? unified?.root : nil
        return column
            // Searching is not the root either: the drawer would slide the page with the keyboard up and the field open.
            .onChange(of: workAtRoot, initial: true) { _, root in shell?.workAtRoot = root }
            .onChange(of: path) { _, p in stackPathChanged(to: p) }
            .onChange(of: pickedRoot) { _, root in sidebarRootPicked(root) }
            .background { AgentsRevealWatch { request in revealAgentsList(request) } }
    }

    /// Stage 4: taking what is asked for from outside, the list's first appearance and the assistant's switches.
    private func listColumnOpening(_ column: some View) -> some View {
        column
            .onChange(of: model.openChat, initial: true) { _, _ in openChatAsked() }
            .onChange(of: model.openPersonalAgent) { _, _ in openPersonalAgentAsked() }
            .onAppear { listAppeared() }
            .onChange(of: model.assistantSwitches) { assistantSwitched() }
    }

    /// The list column's bar items (`listColumnBar`): an empty middle while the shell's own bar floats over this one,
    /// else the page's own.
    @available(iOS 26.0, *)
    @ToolbarContentBuilder private var barItems: some ToolbarContent {
        if barFloatsOver {
            emptyMiddle.sharedBackgroundVisibility(.hidden)
        } else {
            pageItems
            #if os(iOS)
            // Where the system's Hide Sidebar was, in a glass circle of its own as that one: the column is slid
            // by a finger now (#4206).
            if showsHideList {
                ToolbarSpacer(.fixed, placement: .topBarTrailing)
                hideListItem
            }
            #endif
        }
    }

    /// `barItems` before iOS 26 (#6730): the bar has no glass behind its items to hide or to keep apart.
    @ToolbarContentBuilder private var barItemsBeforeGlass: some ToolbarContent {
        if barFloatsOver {
            emptyMiddle
        } else {
            pageItems
            #if os(iOS)
            if showsHideList { hideListItem }
            #endif
        }
    }

    /// The one sidebar's column hides its navigation bar (`agentsColumn`); its head and its Agents heading have the bar's
    /// buttons. Handed to the hidden bar all the same, UIKit was given them anew, their images looked up again, each time
    /// the column came back into the window: about a fifth of the main thread of every switch between the tabs of the
    /// open iPhone Duo or the iPad (#10303).
    private var columnHidesBar: Bool {
        #if os(iOS)
        unified != nil
        #else
        false
        #endif
    }

    /// The shell's own bar floats over this one (AgentsTopBar). Searching on a phone the field takes the bar's row: no
    /// title under it either.
    private var barFloatsOver: Bool { shell != nil || (ownsTop && header.up) }

    private var emptyMiddle: some ToolbarContent {
        ToolbarItem(placement: .principal) { Color.clear.frame(width: Theme.Size.hairline) }
    }

    @ToolbarContentBuilder private var pageItems: some ToolbarContent {
        // Not on the Mac (#907): the connection state is at the foot of the window's sections column (and
        // the page's while that is hidden), and holding the bar's middle this empty item pushed New Session
        // out of the list column, an empty stretch to its left over the conversation. Without it the
        // button sits at the list column's trailing edge, by the conversation, as Mail's compose does.
        ToolbarItem(placement: .principal) { ConnectionTitle() }
        // On the Mac the shell shows it, next to its sidebar button (MacShell).
        ToolbarItem(placement: .topBarLeading) { AgentsListMenu(actions: menuActions) }
        if showsPersonalEntry {
            ToolbarItem(placement: .topBarTrailing) {
                Button { openFromList(PersonalAgentsRoute()) } label: { Label("Personal Agents", systemImage: Theme.Icon.personalAgents) }
                    .accessibilityIdentifier("agents.personal")
            }
        }
        ToolbarItem(placement: .topBarTrailing) {
            Button { newSession.wrappedValue = true } label: { Label("New Session [Chats View 58e21b]", systemImage: Theme.Icon.compose) }
        }
    }

    #if os(iOS)
    private var showsHideList: Bool { widthClass == .regular && unified == nil }
    private var hideListItem: some ToolbarContent { ToolbarItem(placement: .topBarTrailing) { SplitHideListButton() } }
    #endif

    /// `path` became empty, or not (`listColumnProbes`).
    private func backAtList(_ atList: Bool) {
        // Back at the list: the field shows what the results are for again (set it anew, the field is empty).
        guard atList, let q = searchedBeforePush else { return }
        searchedBeforePush = nil
        query = ""
        DispatchQueue.main.async { query = q }
    }

    /// New Session's sheet started an agent: the sheet closes and that agent's chat opens (`listColumnBar`).
    private func newSessionStarted(_ session: Session) {
        showNew = false
        openFromList(session.key)
    }

    /// The stack's path changed to `p` (`listColumnFollowing`).
    private func stackPathChanged(to p: NavigationPath) {
        #if os(iOS)
        // The Sidebar layout on a phone: what the drawer's list opened is in front, the drawer goes (#6332).
        if let unified, widthClass != .regular, !p.isEmpty { unified.drawer.close() }
        #endif
        spaceAt = spaceAt.filter { $0.key < p.count }
        // Back at the list: next launch starts here. Not the Persona tab's page (#4206): its path is Persona's.
        if p.isEmpty, !agentsRootInFront, !leaving, takesAgents { AppModel.rememberChat(nil); model.chatSwitched = [:] }
    }

    /// The sidebar picked another root, or none (`listColumnFollowing`).
    private func sidebarRootPicked(_ root: SidebarPageRoot?) {
        guard let root else { return }
        #if os(iOS)
        // A phone's drawer: the page under it was replaced, the drawer goes (#6332).
        if let unified, widthClass != .regular, root.value != nil { unified.drawer.close() }
        #endif
        // Back at the conversation or a place: no Agent's page in front, the next launch starts at the conversation.
        if path.isEmpty, !agentsRootInFront, !leaving, takesAgents { AppModel.rememberChat(nil); model.chatSwitched = [:] }
    }

    /// An explicit Agents-tab selection asked for the list (`AppModel.agentsListReveal`, `listColumnFollowing`).
    private func revealAgentsList(_ request: Int) {
        // The Sidebar layout: Agents is a part of the sidebar, not a page: choosing it shows that part, and
        // the page beside it stays (#6332). The Persona tab's page leaves it to the Agents tab's (#4206), which
        // goes back to its list as the Separate layout's did.
        if unified?.part == .persona { return }
        if let unified, unified.part == .whole {
            #if os(iOS)
            if widthClass != .regular { unified.drawer.open() } // on a phone the sidebar is the drawer
            #endif
            unified.scrollToAgents?()
            return
        }
        let previousPath = path
        let previousRoot = unified?.root
        stackStill.whenStill {
            // A newer targeted open wins, even if the stable-stack opener already consumed it (with roots, an
            // Agent picked meanwhile changes the root, not the path).
            guard !leaving, model.agentsNavigationRevision == request, path == previousPath, unified?.root == previousRoot,
                  model.openChat == nil, model.openPersonalAgent == nil else { return }
            spaceAt = [:]
            if rootsMode, let unified { unified.forget(path: $path) } else if !path.isEmpty { path = NavigationPath() }
        }
    }

    /// `model.openChat` changed, or is there as the list comes in (`listColumnOpening`).
    private func openChatAsked() {
        // Not before the list is on screen: a push into a stack whose root has not appeared yet built the
        // chat three times (a TabView launch onto a chat, `-openSession` / a notification: items
        // read three times, `-openUploadDemo` uploading twice, #2061). The list's appearing opens it.
        if rootShown { takeOpenChat() }
    }

    /// `model.openPersonalAgent` changed (`listColumnOpening`).
    private func openPersonalAgentAsked() {
        if rootShown { takeOpenPersonalAgent() } // held back as `openChat` is (above)
    }

    /// The list column appeared (`listColumnOpening`).
    private func listAppeared() {
        if !rootShown {
            rootShown = true
            // Asked for before the list was there (above): a turn later, as the remembered chat below.
            if model.openChat != nil { DispatchQueue.main.async { takeOpenChat() } }
            if model.openPersonalAgent != nil { DispatchQueue.main.async { takeOpenPersonalAgent() } }
        }
        // A new tree (RootView swaps the shell and the tabs when the width class changes while the app is
        // active: iPhone Duo folded or opened; iOS's snapshots on the way to the background did it too
        // until #2836, 2026-11-04) starts at the list: open the chat that was open, as a launch does
        // (AppModel.init). Back at the list it is forgotten (rememberChat(nil) above). A turn later: the
        // split of the tree going away empties the paths as it goes, and a push made in the same update
        // did not show. The old tree's page, living on a moment, leaves it to this one (`leaving`).
        // Still the same chat a turn later: going back runs onAppear before onChange(path) forgets it (#1868).
        // Not over a personal agent's conversation asked for (#4519): that one is what the app came up for.
        if takesAgents, path.isEmpty, model.openChat == nil, model.openPersonalAgent == nil, model.selectedTab == .chats,
           let v = AppModel.rememberedChat {
            let request = model.agentsNavigationRevision
            DispatchQueue.main.async {
                if path.isEmpty, model.openChat == nil, model.openPersonalAgent == nil,
                   model.selectedTab == .chats, model.agentsNavigationRevision == request,
                   AppModel.rememberedChat == v, !leaving {
                    model.openChat = SessionKey(path: v)
                }
            }
        }
        if model.debugShowNewChat { model.debugShowNewChat = false; newSession.wrappedValue = true }
        #if DEBUG
        // `-openMachinePage <machine id>` (with `-tab dev`): that machine's projects.
        if path.isEmpty, let m = UserDefaults.standard.string(forKey: "openMachinePage") { openFromList(MachineRoute(id: m)) }
        // `-openTerminalsPage all|<machine path>` (with `-tab dev`): Terminals (#4754), every computer's or one's.
        if path.isEmpty, let m = UserDefaults.standard.string(forKey: "openTerminalsPage") {
            openFromList(TerminalsRoute(machine: m == "all" ? nil : m))
        }
        // `-openMonitor all|<machine id>` (with `-tab dev`): Machines, or one machine's page.
        if path.isEmpty, let m = UserDefaults.standard.string(forKey: "openMonitor") {
            if m == "all" { openFromList(MachineListRoute()) } else { openFromList(MachineDetailRoute(id: m)) }
        }
        #endif
        if !machineFilter.isEmpty, !model.machines.isEmpty, model.machine(MachineRef(path: machineFilter)) == nil { machineFilter = "" }
    }

    /// Another assistant is current, and the list is its agents now: a computer picked in the menu that is
    /// not one of its own would leave the list empty, and what was pushed from the list (a chat, a space, a
    /// computer) is the assistant before's (`listColumnOpening`).
    private func assistantSwitched() {
        if !machineFilter.isEmpty, !model.scopedMachines(inFront: false).contains(where: { $0.ref.path == machineFilter }) { machineFilter = "" }
        // The Agents tab's root was the assistant before's too (the other pages forget theirs in `AssistantView`),
        // unless it is one of this assistant's: a chat asked for at launch is opened before the demo or the paired
        // assistant is set, and the first switch took it away again.
        if rootsMode, unified?.part == .agents, let unified {
            if !rootIsCurrentAssistants(unified.root) { unified.forget(path: $path, keepingAsked: true) }
        } else if !path.isEmpty { path = NavigationPath() }
    }

    /// The list column: the Agents list, or in the Sidebar layout the whole sidebar around it (#6332).
    @ViewBuilder private var agentsColumn: some View {
        #if os(iOS)
        if let unified {
            UnifiedSidebarColumn(state: unified) { rows in agentsList(rows: rows.map { AnyView($0) }) }
                .toolbarVisibility(.hidden, for: .navigationBar)
        } else if ownsTop, listWaits {
            AgentsListPending { listBuilt = true }
        } else {
            agentsList(rows: nil).onAppear { listBuilt = true }
        }
        #else
        agentsList(rows: nil)
        #endif
    }

    private func agentsList(rows: AnyView?) -> AgentsList {
        AgentsList(path: listPushPath, spaceAt: $spaceAt, query: $query, searching: $searching,
                   searchedBeforePush: $searchedBeforePush, showNew: newSession, menuActions: menuActions, ownsTop: ownsTop,
                   showsPersonalEntry: showsPersonalEntry, personalCount: personalCount, beginSearch: beginSearch, sidebarRows: rows)
    }

    /// The chat asked for from outside the list (a notification, a launch onto it): open, and the request taken.
    /// Not while the stack is still moving (a back swipe under the finger or let go, a pop or push on its way): set then,
    /// the path and the stack fell apart, the stack left with a page of nothing over the list under the shell's bar,
    /// which shows at the root only (#1941, AgentsPushPopRaceTests). Once it is still, at once.
    private func takeOpenChat() {
        guard model.openChat != nil, takesAgents else { return }
        stackStill.whenStill { pushOpenChat() }
    }

    private func pushOpenChat() {
        guard let key = model.openChat, !leaving else { return }
        spaceAt = [:] // the chat is the first page now, no space page under anything (as `openFromList`)
        if rootsMode, let unified {
            unified.select(.page(AnyHashable(key)), path: $path)
            unified.askedFromOutside = .page(AnyHashable(key)) // what the app came up for: kept through an assistant switch
        } else {
            listPushPath.wrappedValue = NavigationPath([key])
        }
        #if DEBUG
        // `-openOverSpace YES` with `-openSession`: the chat on its space's page, a page under it (its ‹, #1337).
        if UserDefaults.standard.bool(forKey: "openOverSpace") {
            Task {
                try? await Task.sleep(for: .seconds(3)) // the session list may not be in yet
                guard let space = model.session(key)?.spaceKey else { return }
                var under = NavigationPath()
                under.append(SpaceRoute(id: space))
                under.append(key)
                path = under
                spaceAt = [0: space]
            }
        }
        #endif
        model.openChat = nil
    }

    /// A tap on the field in the list's row: the floating field takes its place and the keyboard, then comes up into
    /// the bar's row (`riseSearch`): the first keyboard of a launch takes a moment to load, and an animation started
    /// before it jumped by what that moment would have shown. Never stuck below if the keyboard does not come.
    private func beginSearch() {
        guard !searching, !header.floating else { return }
        header.measureListTop()
        #if DEBUG
        SearchTrace.shared.note("begin listTop=\(header.listTop) scrolled=\(header.scrolled)")
        #endif
        header.motion = 0
        header.rising = true
        let start = header.startSearch()
        DispatchQueue.main.asyncAfter(deadline: .now() + 1) { riseSearch(start) } // no keyboard came
    }

    /// The floating field has the keyboard: up into the bar's row.
    private func riseSearch(_ start: UUID?) {
        let listInFront = path.isEmpty && ownsTop && !leaving && model.selectedTabNow == .chats
        guard !searching, header.completeSearchStart(start, listInFront: listInFront) else { return }
        var still = Transaction()
        still.disablesAnimations = true
        withTransaction(still) { searching = true }
        // Laid out for searching once the list has slid up with it (`ListShift.arrived`).
        header.rising = true
        withAnimation(Theme.Motion.state) { header.up = true }
    }

    /// 「取消」: the whole list again, at once (results and list drawn together ghosted), from its top with the field out;
    /// the field goes back down into the row, the shell's bar fading back as it goes.
    private func endSearch() {
        var still = Transaction()
        still.disablesAnimations = true
        withTransaction(still) { query = "" }
        if header.cancelPendingSearch() { return }
        // Laid out at rest again, drawn where it is (shifted up by the difference), then down with the field.
        header.setCompact(false)
        // A turn later, once the whole list is in and laid out again.
        DispatchQueue.main.async {
            header.showField()
            header.measureListTop()
            #if DEBUG
            SearchTrace.shared.note("end listTop=\(header.listTop) scrolled=\(header.scrolled)")
            #endif
            withTransaction(still) { searching = false }
            withAnimation(Theme.Motion.state) { header.up = false } completion: { if !searching { header.floating = false } }
        }
    }

    /// A space page from a page already open (a chat's Space menu): on top of it.
    private func pushSpace(_ id: String) {
        path.append(SpaceRoute(id: id))
        spaceAt[path.count - 1] = id
    }

    /// Something opened from here (a new chat, a debug page): it replaces the detail column, or pushes onto the list.
    private func openFromList(_ value: some Hashable) {
        if rootsMode, let unified {
            // The page's root (#8359): replaced, not pushed. A space at the root is under index -1 (`openSpace`).
            spaceAt = (value as? SpaceRoute).map { [-1: $0.id] } ?? [:]
            unified.select(.page(AnyHashable(value)), path: $path)
            return
        }
        spaceAt = (value as? SpaceRoute).map { [0: $0.id] } ?? [:]
        path = NavigationPath([value])
    }

    /// The Sidebar layout's root is something the Agents list opened (an Agent's chat, a space…), not the conversation
    /// or a place (#8359).
    /// The Agents tab's root may stay with the assistant in front: nothing picked, an Agent's chat on one of its computers,
    /// or one not known yet (asked for at launch, before the session list is in): only a root known to be another
    /// assistant's goes.
    private func rootIsCurrentAssistants(_ root: SidebarPageRoot) -> Bool {
        guard let value = root.value else { return true }
        guard let key = value.base as? SessionKey else { return false }
        guard let session = model.session(key) else { return true }
        let machines = model.scopedMachines(inFront: false)
        return machines.isEmpty || machines.contains { $0.ref == session.machineRef }
    }

    private var agentsRootInFront: Bool {
        guard rootsMode, let root = unified?.root else { return false }
        return root.value != nil && root.place == nil
    }

    /// A personal agent's conversation asked for from outside (a Spotlight result, #4519): once the stack is still, as
    /// `takeOpenChat`.
    private func takeOpenPersonalAgent() {
        guard model.openPersonalAgent != nil, takesAgents else { return }
        stackStill.whenStill { pushOpenPersonalAgent() }
    }

    /// Over the personal agents list, as a tap there opens it: its ‹ goes back to that list. Through `pushing`, so an
    /// open search closes first. The chat the path held is forgotten: the path does not pass through empty on the way,
    /// and a rebuilt list or the next launch would bring that chat back over this conversation.
    private func pushOpenPersonalAgent() {
        guard let session = model.openPersonalAgent, !leaving else { return }
        AppModel.rememberChat(nil)
        spaceAt = [:]
        if rootsMode, let unified {
            // Personal Agents is the root (#8359), the conversation pushed on it: its ‹ goes back to that list.
            unified.select(.page(AnyHashable(PersonalAgentsRoute())), path: $path)
            var t = Transaction()
            t.disablesAnimations = true
            withTransaction(t) { path = NavigationPath([session]) }
            model.openPersonalAgent = nil
            return
        }
        var under = NavigationPath()
        under.append(PersonalAgentsRoute())
        under.append(session)
        pushing.wrappedValue = under
        model.openPersonalAgent = nil
    }
}

/// The Agents list itself: what follows the hub's pushes (`ChatsView` holds the navigation and the toolbar).
private struct AgentsList: View {
    @State private var workspaceCatalog = WorkspaceCatalog()
    @State private var openedWorkspace: String?
    @State private var creatingChild: HostWorkspace?
    @State private var createdWorktree: HostWorkspace?
    @State private var startingWorktree: HostWorkspace?
    @State private var startingProject: AgentProjectFolder?
    @State private var renamingWorktree: HostWorkspace?
    /// iPhone Duo's bars down the side: the shell's bar is the side rail, and the list needs no room for it at its top.
    @Environment(\.shellRailEdge) private var railEdge
    static let emptyMessage = String(localized: "Start an agent on any of your computers, then follow it live and take over from your phone.")
    /// No computer yet (a hub with machines_add): as the machines page says it (docs/assistant/onboarding.md 4.5「空状态改写」).
    static let emptyNoComputerTitle = String(localized: "No computers yet")
    static let emptyNoComputerMessage = String(localized: "Add one and Persona can work on it for you.")
    @Environment(AppModel.self) private var model
    @Environment(\.shell) private var shell
    @Environment(\.agentConsole) private var agentConsole
    /// The list's own column: regular beside the iPad's split; on a phone's split (iPhone Duo open) the sidebar column
    /// reads compact while the page is regular (#1971). `pushedOver` goes by `ownsTop` instead (#3680).
    @Environment(\.horizontalSizeClass) private var widthClass
    @Binding var path: NavigationPath
    @Binding var spaceAt: [Int: String]
    @Binding var query: String
    @Binding var searching: Bool
    @Binding var searchedBeforePush: String?
    @Binding var showNew: Bool
    var menuActions: AgentsListMenu.Actions
    /// A phone: the page's own top (`AgentsHeader`), not the system's search.
    var ownsTop: Bool
    var showsPersonalEntry: Bool
    /// How many personal agent conversations there are, for their group's heading (nil while unknown).
    var personalCount: Int?
    var beginSearch: () -> Void
    /// The Sidebar layout (#6332): the Persona sidebar's rows (the pages, Persona's chats), held above the Agents, which
    /// get a heading of their own with the list's ⋯, Personal Agents and New Session (`UnifiedSidebarColumn`).
    var sidebarRows: AnyView? = nil
    @Environment(\.unifiedSidebar) private var unified
    /// The split it is in (the Sidebar layout on a phone: the drawer's, `SplitDetail.drawer`).
    @Environment(\.splitDetail) private var split

    @State private var searchClose = SearchCloseWatcher()
    @Environment(AgentsHeader.self) private var header
    /// Laid out for searching (`AgentsHeader.compact`).
    private var compactTop: Bool { ownsTop && header.compact }
    @StoredValue("dev.machineFilter") private var machineFilter = ""
    @StoredValue("dev.harnessFilter") private var harnessFilter = ""
    @StoredValue("dev.projectFilter") private var projectFilter = ""
    @StoredValue("dev.modelFilter") private var modelFilter = ""
    @StoredValue("dev.pinnedOnly") private var pinnedOnly = false
    /// What the search reads (#7443; the ⋯ menu's Search In), applied only while something is searched.
    @StoredValue(AgentSearchScope.key) private var searchScope: AgentSearchScope = .all
    @StoredValue("dev.searchAsCode") private var searchAsCode = false
    /// Several groups of hubs: "" all of them, else the list is narrowed to one (`HubFilter`).
    @StoredValue(HubFilter.key) private var storedHubFilter = ""
    /// The group the list shows when narrowed: the current hub's (`HubFilter.shown`, #11657).
    private var hubFilter: String { HubFilter.shown(storedHubFilter, current: model.hub?.group) }
    @StoredValue("dev.grouping") private var storedGrouping: DevGrouping = .standard // by repository first (#2108)
    /// The grouping in use (`DevGrouping.resolved`).
    private var grouping: DevGrouping { agentConsole ? .machine : DevGrouping.resolvedNow(storedGrouping) }
    @StoredValue("dev.sort") private var sort: AgentSort = .state
    /// Opt-in By Repository toggle (#14607): off by default, aligned with Android and Web.
    @StoredValue("dev.showEmptyWorkspaces") private var showEmptyWorkspaces = false
    /// Compact List: one line per agent.
    @StoredValue("dev.compact") private var compact = false
    /// How a row with the agent's latest words is drawn otherwise (#2530, Settings › Appearance › Layout).
    @StoredValue(AgentRowStyle.key) private var rowStyle: AgentRowStyle = .standard
    /// Folded sections (spaces, machines, states), one id per line.
    @StoredValue("dev.folded") private var foldedList = ""
    /// The notice that read rows now stay (`seenRowsNotice`, #12514) was closed, or the choice made in Settings.
    @StoredValue(SeenRows.noticedKey) private var seenRowsNoticed = false
    @State private var quick: AgentFilter = .all
    @State private var showHiddenDirectories = false
    @State private var paginationArmed = false

    private var selectedHub: String? {
        if !machineFilter.isEmpty { return MachineRef(path: machineFilter).group }
        return hubFilter.isEmpty ? nil : String(hubFilter.dropFirst())
    }

    private var listQuery: SessionListQuery {
        var q = SessionListQuery(state: quick.state?.rawValue ?? "", text: query)
        q.includeHidden = showHiddenDirectories
        if !machineFilter.isEmpty { q.machine = MachineRef(path: machineFilter).id }
        let links = model.links.filter { selectedHub == nil || $0.group == selectedHub }
        let pins = links.contains { !$0.pins.pins.sessions.isEmpty || !$0.pins.pins.spaces.isEmpty }
        let mutes = links.contains { !$0.pins.mutes.sessions.isEmpty || !$0.pins.mutes.spaces.isEmpty }
        q.legacy = AgentListLoadingPolicy.runtime(grouping: grouping, filter: quick, query: query, hasPins: pins, hasMutes: mutes)
        return q
    }
    /// Under a quick filter, the agents it showed stay until it is chosen again: one opened from Unread is seen now
    /// but keeps its place, so the next one is still right below it.
    @State private var kept = Set<SessionKey>()
    @State private var expandedQuiet = Set<String>()
    @State private var expandedIdle = Set<String>()
    /// The agents whose Subtasks are open (`CodexTasks`), and, searching, those whose found subtasks were folded.
    @State private var expandedTasks = Set<SessionKey>()
    @State private var foldedFound = Set<SessionKey>()
    @State private var renamingSpace: SpaceGroup?
    @State private var renamingSession: Session?
    @State private var closingSession: Session?
    @State private var deletingSession: Session?
    @State private var linkingSession: Session?
    @State private var linkingSpace: SpaceGroup?
    /// A card on no session, open in a sheet of its own (#4059): it has no chat to go to.
    @State private var answering: Interaction?
    /// The long-press menu's actions on a row (not observed: see `AgentRowMenu`).
    @State private var rowActions = AgentMenuHandlers()
    /// Opens an unread folded side session in one tap (`\.openUnreadSide`): the same object for the list's life (#13524).
    @State private var sideOpener = SideSessionOpener()
    @State private var takeoverCoordinator = CodexTakeoverCoordinator()
    /// The agents, the questions and the machines the list shows (`AgentsFeed`): held still while it is scrolled or
    /// out of sight.
    @State private var feed = AgentsFeed()
    #if DEBUG && os(iOS)
    @State private var sortShadow = AgentSortShadowObserver()
    #endif

    /// The machines and agents the rows name, as the body last saw them (`Lookup`).
    @State private var lookup = Lookup()
    /// Machines whose herdr does not run: a row with Start each (#1916).
    @State private var herdr = HerdrWatch()

    /// A page pushed over the list. Whether it is out of sight for another reason (the shell's other page, another tab)
    /// is `AgentsCoverWatch`'s to read, not this body's (#5438). Goes by the page's layout (`ownsTop`), not this column's
    /// width class: the split's sidebar column reads compact on a phone (#1971), and on iPhone Duo open the list beside
    /// a chat opened in code (the one remembered at launch, a card, New Session, a worktree line) was held as out of
    /// sight: its last lines, order and new agents stopped coming in (#3680).
    private var pushedOver: Bool {
        ownsTop && !path.isEmpty
    }

    private var sessions: [Session] { feed.following ? feed.sessions : model.sessionLists.sessions }

    private var machines: [Machine] { feed.following ? feed.machines : model.scopedMachines() }
    /// What the rows read (a machine's name, an agent's label), filled by each run of the body and not observed. SwiftUI
    /// tracks what each row of a `List` read when it was last drawn: a row that asked the model for its machine went on
    /// following the machines while the list was held, was drawn again by the next push, and the `List` measured its
    /// rows anew mid-glide (the content jumped by the difference from its estimate).
    private final class Lookup {
        var machines: [MachineRef: Machine] = [:]
        var sessions: [SessionKey: Session] = [:]
        var scopes: [String: RuntimeSessionLists.Scope] = [:]
        var projection: AgentListProjection?
        /// The Needs You section's rows (`AgentsNeedsYou`), which its filter counts too: sessions, not cards.
        var needsYou: Int?

        func update(_ projection: AgentListProjection) {
            guard self.projection?.identity != projection.identity else { return }
            self.projection = projection
            machines = projection.machinesByRef
            sessions = projection.sessionsByKey
            scopes = projection.scopes
        }
    }

    private func machine(_ ref: MachineRef) -> Machine? { lookup.machines[ref] }

    private var showsHerdrNotices: Bool { query.isEmpty && model.connection.shown == .online && !herdrStopped.isEmpty }

    /// Beside the split (the Mac): the 「herdr 没在运行」 rows under the filter bar, not as the list's first section, which
    /// a grouped list pads above for a header (an empty band between the bar and the row; `docs/lessons/macos.md`).
    private var herdrNoticesAbove: some View {
        VStack(spacing: 0) {
            ForEach(herdrStopped) { m in
                HerdrNoticeRow(machine: m, watch: herdr)
                    .padding(.horizontal, Theme.Space.page)
                    .padding(.vertical, Theme.Space.s)
                Divider().padding(.leading, Theme.Space.page)
            }
        }
    }

    /// The machines with a 「herdr 没在运行」 row, under the machine menu and the group of hubs shown.
    private var herdrStopped: [Machine] {
        machines.filter { m in
            herdr.stopped.contains(m.ref) && HerdrWatch.watched(m) && (machineFilter.isEmpty || m.ref.path == machineFilter) &&
                (!model.isMultiHub || HubFilter.matches(hubFilter, m.group))
        }
    }
    private func session(_ key: SessionKey) -> Session? { lookup.sessions[key] }

    /// The scope the list applies. The Sidebar layout's field (chats and Agents) names no scope: Full Text there
    /// (`AgentsListMenu`).
    private var appliedScope: AgentSearchScope {
        unified == nil ? searchScope.applied(query: query, searching: searching) : .all
    }

    /// Small value inputs, not a fingerprint of the inventory. Paging counts and preferences remain partitioned.
    private func projectionSettings() -> AgentListProjection.Settings {
        var preferences: [String: AgentListProjection.Preferences] = [:]
        for link in model.links { preferences[link.group] = .init(pins: link.pins.pins, mutes: link.pins.mutes) }
        return .init(grouping: grouping.rawValue, sort: sort.rawValue, filter: quick, runningTerminalOnly: agentConsole, query: query,
                     searchScope: appliedScope,
                     searchAsCode: searchAsCode,
                     machine: machineFilter, harness: harnessFilter, project: projectFilter, model: modelFilter,
                     pinnedOnly: pinnedOnly, showEmptyWorkspaces: showEmptyWorkspaces, hub: hubFilter, multiHub: model.isMultiHub, kept: kept,
                     held: model.heldInList, heldJustFinished: model.heldJustFinished, heldSeen: heldSeen, preferences: preferences,
                     pagedGroups: Set(model.sessionLists.lists.filter { $0.value.paged }.keys),
                     listQuery: listQuery, sideFold: model.sideFoldGroups)
    }

    private func listProjection() -> AgentListProjection {
        let settings = projectionSettings()
        let partitions = AgentListProjectionCache.partitions(model)
        let follows = feed.following && feed.hasPartitions(partitions)
        let asks = ChildSessions.asks(model.aggregatePendingInteractions)
        let key = AgentListProjectionCache.Key(revision: feed.revision, partitions: partitions, settings: settings,
                                               workspaceRevision: workspaceCatalog.revision, asks: asks)
        return feed.projection.resolve(key, immediate: !follows || feed.immediateRevision == key.revision,
            input: { .init(sessions: follows ? feed.sessions : model.sessionLists.sessions,
                           machines: follows ? feed.machines : model.scopedMachines(), settings: settings,
                           workspaces: workspaceCatalog.workspaces, asks: asks) },
            valid: { key in
                key.revision >= feed.immediateRevision && AgentListProjectionCache.partitions(model) == key.partitions &&
                    projectionSettings() == key.settings && workspaceCatalog.revision == key.workspaceRevision &&
                    key.asks == asks
            },
            canPublish: { key in feed.mayPublish(key.revision) },
            willPublish: { feed.reader.willChange() })
    }

    @discardableResult
    private func shadowSubmit(active: [SpaceGroup], quiet: [SpaceGroup]) -> Int {
        #if DEBUG && os(iOS)
        guard sortShadow.enabled else { return 0 }
        let snapshot = shadowPresentation(active + quiet)
        sortShadow.submit(feed: feed, settings: projectionSettings(), presentation: snapshot)
        return 0
        #else
        return 0
        #endif
    }

    #if DEBUG && os(iOS)
    private func shadowPresentation(_ groups: [SpaceGroup]) -> AgentSortPresentation {
        guard sortShadow.enabled else { return .init() }
        var result = AgentSortPresentation(folded: folded, expandedIdle: expandedIdle,
                                           expandedQuiet: expandedQuiet, compact: compact)
        let settings = projectionSettings()
        for group in groups where group.bounded {
            result.bounded.insert(group.id)
            if let counts = model.sessionLists.aggregate(AgentListProjection.aggregateScope(group, settings: settings)) {
                result.aggregate[group.id] = counts
            }
        }
        return result
    }
    #endif

    private func withAggregate(_ group: SpaceGroup) -> SpaceGroup {
        guard group.bounded, let scope = lookup.scopes[group.id] else { return group }
        var group = group
        group.aggregate = model.sessionLists.aggregate(scope)
        return group
    }

    /// The agents the machine menu and the search leave, as the current publication last derived them.
    private var base: [Session] { listProjection().base }

    private var folded: Set<String> { Set(foldedList.split(separator: "\n").map(String.init)) }

    /// Folded as shown: a search opens every folded section, so no match hides behind a fold (folded spaces showed
    /// only their header, and the agent searched for was not there to open; quiet spaces and idle rows already open).
    /// Sections that start folded: By Status's Seen is the long tail. For them the saved list holds "!<id>" once opened.
    private static let foldedByDefault: Set<String> = ["state/idle"]
    /// Sections whose default depends on what they hold (Started Manually and Unassigned, `TodoAgentGroup.folded`): the
    /// saved list holds "<id>" or "!<id>" once the user chose, nothing until then (#1836, #6458).
    private static let foldedByChoice: Set<String> = ["todo:none", "todo:manual"]

    /// Starts folded: `foldedByDefault`, and a working tree's「N 个子项」(#3075).
    private static func foldsByDefault(_ id: String) -> Bool {
        foldedByDefault.contains(id) || id.hasSuffix(RepoTree.childFoldSuffix) || id.hasSuffix(RepoTree.agentsFoldSuffix)
    }

    private func isFolded(_ id: String, otherwise fallback: Bool = false) -> Bool {
        guard query.isEmpty else { return false }
        if Self.foldedByChoice.contains(id) { return folded.contains(id) || (fallback && !folded.contains("!" + id)) }
        return Self.foldsByDefault(id) ? !folded.contains("!" + id) : folded.contains(id)
    }

    private func setFolded(_ ids: Set<String>) { foldedList = ids.sorted().joined(separator: "\n") }

    /// `ids` folded (true) or open, on top of what is saved.
    private func folding(_ ids: some Sequence<String>, _ fold: Bool, in f: Set<String>) -> Set<String> {
        var f = f
        for id in ids {
            if Self.foldedByChoice.contains(id) {
                f.remove(id)
                f.remove("!" + id)
                f.insert(fold ? id : "!" + id)
                continue
            }
            let key = Self.foldsByDefault(id) ? "!" + id : id
            let marks = Self.foldsByDefault(id) ? !fold : fold // a default-folded section marks being open
            if marks { f.insert(key) } else { f.remove(key) }
        }
        return f
    }

    /// `shown`: whether it is folded on screen when that is not `isFolded(id)` alone (Unassigned's own default), so a
    /// tap always flips what is seen.
    private func toggleFold(_ id: String, shown: Bool? = nil) {
        Haptics.tap()
        feed.reader.keep(id)
        let f = folding([id], !(shown ?? isFolded(id)), in: folded)
        if shown ?? isFolded(id), let scope = lookup.scopes[id] { Task { await model.sessionLists.expand(scope) } }
        withAnimation(Theme.Motion.standard) { setFolded(f) }
    }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("AgentsList")
        #endif
        let projection = listProjection()
        let _ = lookup.update(projection)
        let base = projection.base
        let matching = projection.matching
        let filtered = projection.filtered
        let unpinned = projection.unpinned
        // Paging counts arrive per header: overlaid here (O(sections)), not a reason to derive the inventory again.
        let active = projection.active.map(withAggregate)
        let quiet = projection.quiet.map(withAggregate)
        let _ = shadowSubmit(active: active, quiet: quiet)
        let pinnedAgents = projection.pinnedAgents
        // One row per session that waits for the user, its cards under it (`AgentsNeedsYou`): the section, the Needs
        // You filter and the tab's badge count these rows.
        let cardSession = model.cardSessionResolver() // the same row for a card here as in the counts
        let waiting = AgentsNeedsYou.rows(
            cards: AgentsNeedsYou.cards(feed.following ? feed.interactions : model.scopedInteractions, session: { cardSession($0) ?? session($0) },
                                        machine: machineFilter, hub: model.isMultiHub ? hubFilter : nil),
            waiting: AgentsNeedsYou.waiting(base)) // a folded child's card sits under its parent; a subtask the hub did not fold stays its own row
        let _ = (lookup.needsYou = waiting.count)
        let needsYou = quick == .all || quick == .needsYou ? waiting : []
        let interactions = needsYou.flatMap(\.cards)
        let unreadShown = projection.unread
        let byHost = projection.byHost
        let tree = projection.tree

        // What the ⋯ menu's actions act on, kept current without making the open menu a function of the list. Split by
        // machine, Collapse All still folds the spaces and repositories, not the machines: every one of them a line, the overview.
        let sectionsForMenu: [String] = {
            if grouping == .todo {
                return TodosLogic.buildTodoAgentGroups(sessions: unpinned, todos: TodoStore.shared.todos, isMuted: model.isMuted).map(\.key)
            }
            if grouping == .direction, let layout = directionLayout(unpinned.filter { $0.state.isLive }) { return layout.foldIDs }
            return projection.menuSections
        }()
        let _ = menuActions.update(sections: sectionsForMenu,
                                   foldAll: { setFolded(folding($0, true, in: folded)); expandedQuiet = [] },
                                   unfoldAll: { setFolded(Set(Self.foldedByDefault.union(Self.foldedByChoice).map { "!" + $0 })) },
                                   onMachines: { openFromList(MachineListRoute()) },
                                   onTerminals: { openFromList(TerminalsRoute()) },
                                   showingHidden: showHiddenDirectories,
                                   toggleHidden: { showHiddenDirectories.toggle() })
        let _ = rowActions.update(rename: { renamingSession = $0 }, close: { closingSession = $0 }, linkTodo: { linkingSession = $0 },
                                  delete: { deletingSession = $0 }, takeover: { session in
                                      Task {
                                          if let adopted = await takeoverCoordinator.takeOver(session, model: model) {
                                              model.adopt(adopted)
                                              openFromList(adopted.key)
                                          }
                                      }
                                  }, continueInNewSession: { session in
                                      Task {
                                          if let adopted = await takeoverCoordinator.continueInNewSession(session, model: model) {
                                              model.adopt(adopted)
                                              openFromList(adopted.key)
                                          }
                                      }
                                  })

        #if os(iOS)
        let _ = bindAgentMenuActions()
        #endif

        // The list and the chain below are erased in a few places (#6735). Unoptimized, a function reserves a stack slot on
        // entry for every opaque intermediate of a modifier chain, each holding everything before it: ~45 modifiers
        // over the List made `observedAgentList` reserve 422 KB, and a Debug build on an iPhone (a 1 MB main-thread
        // stack, the simulator's is 8 MB) overflowed on every launch into Agents. An erased type is the same on every
        // update, so the list keeps its identity and state. `MainPagesStackTests` measures the page.
        observedAgentList(
            AnyView(agentList(base: base, filtered: filtered, unpinned: unpinned, pinnedAgents: pinnedAgents,
                              needsYou: needsYou, unreadShown: unreadShown, active: active, quiet: quiet,
                              tree: tree, byHost: byHost, hasSearchResults: projection.hasSearchResults,
                              holdsSeen: !projection.heldSeen.isEmpty)),
            base: base, interactions: interactions, quiet: quiet, matching: matching, needsSeen: projection.needsSeen)
            .codexTakeoverDialogs(coordinator: takeoverCoordinator) { adopted in
                model.adopt(adopted)
                openFromList(adopted.key)
            }
    }

    #if os(iOS)
    private func bindAgentMenuActions() {
        rowActions.navigate = { current, destination in
            switch destination {
            case .space:
                if let space = current.spaceKey { openFromList(SpaceRoute(id: space)) }
            default:
                AgentMenuRouting.shared.open(destination, at: current.key)
                openFromList(current.key)
            }
        }
        rowActions.openTodo = { session in
            if let id = session.todo?.id { openFromList(TodoDetailRoute(id: id)) }
        }
    }
    #endif

    // Opaque helper boundaries keep the same List/modifier tree while allowing
    // the minimum compiler to solve each expression independently.
    private func agentList(base: [Session], filtered: [Session], unpinned: [Session], pinnedAgents: [Session],
                           needsYou: [AgentsNeedsYou.Row], unreadShown: [SessionKey], active: [SpaceGroup], quiet: [SpaceGroup],
                           tree: [RepoTree.Host], byHost: Bool, hasSearchResults: Bool, holdsSeen: Bool) -> some View {
        List {
            // The Sidebar layout (#6332): the pages and Persona's chats first, as the web sidebar has them over its Agents.
            if let sidebarRows {
                Section { sidebarRows }
                    .listSectionSeparator(.hidden)
                    // No sections' gap before the Agents heading: it stands as far below Persona's last row as Persona
                    // Chats below the pages', its own heading's room only (`SidebarHeadingBox`, #10551).
                    .listSectionSpacing(.custom(Theme.Space.flush))
            }
            // On a phone the filter bar is the list's first row: it scrolls with the list, so the search field can
            // fold away and come back on a pull (a bar pinned in the safe area stayed put under a folded search field,
            // leaving a blurred gap, TestFlight 35). Beside the split (regular width) it stays above the list; in the
            // Sidebar layout it is under the Agents' own heading, in the list.
            if widthClass != .regular || unified != nil {
                Section {
                    #if os(iOS)
                    if unified != nil { unifiedAgentsHeading }
                    #endif
                    if ownsTop {
                        // The room for the shell's bar and the search field above the filters (`AgentsHeader`).
                        AgentsHeaderRow(header: header, query: $query, begin: beginSearch) {
                            VStack(alignment: .leading, spacing: 0) {
                                filterBar(base)
                                machineFilterChip
                            }
                        }
                    } else {
                        // iPhone Duo open: the split's sidebar column reads compact, but the page is regular and its
                        // search is the system's drawer (`AgentsSystemSearch`): the filters alone, no second field (#1971).
                        VStack(alignment: .leading, spacing: 0) {
                            filterBar(base)
                            machineFilterChip
                        }
                    }
                }
                    .listRowBackground(Color.clear)
                    .listRowInsets(EdgeInsets())
                    .listRowSeparator(.hidden)
                    .id("agents.controls")
            }
            if !model.desktops.isEmpty && query.isEmpty {
                Section { DesktopPresenceRow() } // 「Mac 上的 Persona 已登录」, WeChat's line over its chats
            }
            if model.hubNotice != nil && query.isEmpty {
                Section { HubNoticeRow() } // the hub's version: an update out, under way, or too old (docs/operations/hub-update.md)
            }
            if (widthClass != .regular || unified != nil) && showsHerdrNotices {
                // "MacBook's herdr is not running" with Start (#1916); beside the split they are under the filter bar (`herdrNoticesAbove`).
                ForEach(herdrStopped) { m in
                    Section { HerdrNoticeRow(machine: m, watch: herdr) }
                }
            }
            if !needsYou.isEmpty && query.isEmpty {
                listSection {
                    ForEach(needsYou) { row in needsYouRow(row).modifier(NeedsAnchor(unified: unified)) }
                } header: {
                    SectionHeader(title: StateName.needsYou.text, count: needsYou.count, tone: StatusPalette.needsYou)
                        .searchTraceMark("needs")
                        .modifier(NeedsAnchor(unified: unified))
                }
                .id(UnifiedSidebar.needsAnchor)
            }

            if quick == .unread && !unreadShown.isEmpty {
                Section {
                    Button {
                        model.markSeen(unreadShown)
                        kept = []
                    } label: {
                        if compact {
                            HStack(spacing: CompactList.gap) {
                                ThemedIcon(Theme.Icon.success).font(CompactList.metaStrong).scaledFrame(width: CompactList.mark)
                                Text(StateName.markAllSeen()).font(CompactList.title).lineLimit(1)
                                Spacer()
                                Text(StateName.count(.unread, unreadShown.count)).font(CompactList.meta).foregroundStyle(.secondary).lineLimit(1)
                            }
                        } else if floating {
                            HStack {
                                Label(StateName.markAllSeen(), systemImage: Theme.Icon.success).lineLimit(1)
                                Spacer()
                                Text(StateName.count(.unread, unreadShown.count)).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                            }
                            .card(padding: Theme.Space.m)
                        } else {
                            HStack {
                                Label(StateName.markAllSeen(), systemImage: Theme.Icon.success).lineLimit(1)
                                Spacer()
                                Text(StateName.count(.unread, unreadShown.count)).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                            }
                        }
                    }
                    .listRowInsets(compact ? CompactList.rowInsets : floating ? Self.capsuleInsets : nil)
                    .listRowBackground(floating ? Color.clear : nil)
                    .listRowSeparator(floating ? .hidden : .automatic)
                }
            }

            if !pinnedAgents.isEmpty {
                listSection {
                    // No state word: the dot and the name's color already say it.
                    ForEach(pinnedAgents) { s in slotRow(s, context: true, inStateSection: true) }
                } header: {
                    SectionHeader(title: String(localized: "section.pinned", defaultValue: "Pinned"), count: nil, tone: StatusPalette.idle)
                }
            }

            #if os(iOS)
            // The shell's way to the personal agents (#140): a group of its own above the computers' (#16769), only
            // over the whole list (not a quick filter's or a search's results, which it is not one of).
            if shell != nil, showsPersonalEntry, quick == .all, query.isEmpty {
                Section {
                    AgentsPersonalHeading(count: personalCount) { openFromList(PersonalAgentsRoute()) }
                        .modifier(ButtonRowInsets(edges: Self.hostHeadingInsets))
                        .listRowBackground(Color.clear)
                        .listRowSeparator(.hidden)
                }
                .listSectionSpacing(.custom(0))
            }
            #endif

            if grouping == .recent {
                recentSection(unpinned)
            } else if grouping == .todo {
                todoSections(unpinned.filter { $0.state.isLive })
            } else if grouping == .direction {
                directionSections(unpinned.filter { $0.state.isLive })
            } else if grouping == .state {
                stateSections(unpinned.filter { $0.state.isLive })
            } else if grouping == .machine {
                machineSections(unpinned.filter { $0.state.isLive })
            } else if grouping == .app {
                appSections(unpinned) // offline ones too, as the apps' sidebars list them
            } else if grouping == .repo {
                repoSections(tree, byHost: byHost)
            } else {
                spaceSections(active: active, quiet: quiet, byHost: byHost)
            }

            if !model.sessionLists.errors.isEmpty {
                Section {
                    ForEach(model.sessionLists.errors, id: \.self) { error in
                        Label(error, systemImage: Theme.Icon.error).foregroundStyle(Theme.danger)
                    }
                }
            }
            if model.sessionLists.hasMore {
                Section {
                    Button { Task { await model.sessionLists.next() } } label: {
                        Label("Load More", systemImage: Theme.Icon.disclosure).lineLimit(1)
                    }
                    .themedButton()
                    .frame(maxWidth: .infinity)
                    .accessibilityIdentifier("agents.nextPage")
                    .task(id: paginationArmed) { if paginationArmed { await model.sessionLists.next() } }
                }
                .listRowBackground(Color.clear)
            }

            if !hasSearchResults, !query.isEmpty {
                // Nothing matches the search: not Start Your First Session with its New Session, which read as if the agents were gone.
                Section {
                    EmptyStateView(symbol: Theme.Icon.search, title: String(localized: "No Results for “\(query)”"),
                                   message: String(localized: "You can search space, tab and session titles and content. Try another word."))
                }
                .listRowBackground(Color.clear)
            } else if !hasSearchResults, appliedScope.isSearch(query: query) {
                // An open search under Errors with nothing typed and no Agent with an error: the same, not the first-run screen.
                Section {
                    EmptyStateView(symbol: Theme.Icon.success, title: String(localized: "No Agents with Errors"),
                                   message: String(localized: "Agents that stop with an error show up here."),
                                   action: (String(localized: "Show All"), { searchScope = .all }))
                }
                .listRowBackground(Color.clear)
            } else if !hasSearchResults, machineFilter.isEmpty, machines.isEmpty, model.canAddComputer {
                Section {
                    EmptyStateView(symbol: Theme.Icon.machines, title: Self.emptyNoComputerTitle, message: Self.emptyNoComputerMessage,
                                   action: (String(localized: "Add Computer"), { model.openAddComputer() }))
                }
                .listRowBackground(Color.clear)
                .listRowSeparator(floating ? .hidden : .automatic)
            } else if !hasSearchResults {
                Section {
                    EmptyStateView(symbol: Theme.Icon.agents,
                                   title: machineFilter.isEmpty ? String(localized: "Start Your First Session") : String(localized: "No Sessions on This Machine"),
                                   message: Self.emptyMessage,
                                   action: (String(localized: "New Session [Chats View 58e21b]"), { showNew = true }))
                }
                .listRowBackground(Color.clear)
                .listRowSeparator(floating ? .hidden : .automatic)
            } else if filtered.isEmpty, let st = quick.state {
                Section {
                    EmptyStateView(symbol: Theme.Icon.success, title: String(localized: "No Sessions: \(st.label)"),
                                   message: machineFilter.isEmpty ? String(localized: "Agents on your computers show up here when something happens.")
                                                                  : String(localized: "Agents on this machine show up here when something happens."),
                                   action: (String(localized: "Show All"), { choose(.all, base) }))
                }
                .listRowBackground(Color.clear)
                .listRowSeparator(floating ? .hidden : .automatic)
            }
        }
        #if os(iOS)
        // The changed default said once (#12514, PRINCIPLES 1), when a read row first stays, with the way to change it
        // back: floating at the list's foot, not in the list, so no row moves when it comes or goes. A section at the
        // list's top, put in while the chat was open over it, moved the rows ~570 pt on the way back.
        .floatingFooter(over: listPage, shown: holdsSeen && !seenRowsNoticed && model.seenRows == .stay && query.isEmpty) {
            seenRowsNotice
                .padding(.horizontal, Theme.Space.page)
                .padding(.bottom, Theme.Space.sm)
        }
        #endif
    }

    private func styledAgentList<Content: View>(_ list: Content, base: [Session], interactions: [Interaction]) -> some View {
        list
        .modifier(AgentsListStyle(capsules: floating, room: ownsTop && shell != nil && railEdge == nil && !compactTop, ownsTop: ownsTop))
        // Between the two layouts, slid with the field (`AgentsHeader.compact`); the page's color behind it.
        .modifier(ListShift(progress: ownsTop && header.up ? 1 : 0, compact: ownsTop && header.compact,
                            shift: AgentsHeader.shift(shell: shell != nil)) { [header] in
            // Up: laid out for searching, where it is drawn already.
            if header.rising, searching { header.setCompact(true) }
        })
        // Keep the phone's first row close to the bar: the grouped list's default first-section margin otherwise. The
        // room between the search field and the filters is the row's own (`AgentsHeader.filtersMargin`). The split's
        // sidebar keeps its native content margins.
        .contentMargins(.top, widthClass == .regular ? nil : 0, for: .scrollContent)
        #if os(iOS)
        // Every row a finger taps is at least its height, in every grouping and display (#3753).
        .environment(\.defaultMinListRowHeight, Theme.Size.tapTarget)
        #else
        .environment(\.defaultMinListRowHeight, grouping == .repo ? Theme.Size.wellXS : compact ? 30 : 44)
        #endif
        .environment(\.compactList, compact)
        #if DEBUG || PERF_SIGNPOST
        .overlay { if ScreenProbe.enabled { AgentsListProbe(feed: feed, sessions: base, asks: interactions, page: shell == nil ? nil : "agents") } }
        #endif
        .listSectionSpacing(repoTree ? .custom(Theme.Space.m) : compact ? .compact : .default)
        .modifier(AgentsListPage(capsules: floating, workspaceSidebar: grouping == .repo && !repoTree,
                                 column: unified == nil ? nil : UnifiedSidebar.columnColor))
        // In the split's sidebar column (iPhone Duo open) the bar sits above the list; on a phone it is the list's
        // first row (above).
        .modifier(TopBar(abovePage: widthClass == .regular && unified == nil) {
            filterBar(base)
            if showsHerdrNotices { herdrNoticesAbove } // beside the split: over the list, not its first section
        })
        .navigationTitle("Agents")
        // No title over the list (the tab says it); the name stays for the back button. The principal item (ChatsView)
        // takes the title's place: empty while all is well, Connecting… and the like while the hub is out of reach
        // (toolbar(removing: .title) would hide that too).
        .navigationBarTitleDisplayMode(.inline)
        .modifier(AgentsTop(ownsTop: ownsTop, searchClose: searchClose,
                            retainsQuery: AgentsSearchVisibility.retained(query: query)))
        #if (DEBUG || PERF_SIGNPOST) && (os(iOS) || os(visionOS))
        .background { if ScreenProbe.enabled { AgentsScrollProbe(page: shell == nil ? nil : "agents") } }
        #endif
        // Beside the split and on the Mac: the system's search (a phone has its own, `AgentsHeader`). The filter bar is
        // not pinned in the safe area under a folding field: pinned, it stayed under the folded field (TestFlight 35,
        // ~120 pt of blurred list). On the list itself, before the alerts, as it always was.
        .modifier(AgentsSystemSearch(enabled: !ownsTop && unified == nil, query: $query, searching: $searching))
    }

    private func observedAgentList<Content: View>(_ content: Content, base: [Session], interactions: [Interaction],
                                                 quiet: [SpaceGroup], matching: Set<SessionKey>,
                                                 needsSeen: [SessionKey: AgentListOrder.Held]) -> some View {
        let decorated = AnyView(styledAgentList(content, base: base, interactions: interactions))
        // A pull is a refresh: the rows read during the visit go where their state puts them (#12514).
        .refreshable { releaseSeenRows(); await model.sessionLists.reload(); await model.refresh() }
        // Whether each machine's herdr runs, while the list is in sight (a modifier of its own: in this chain the
        // compiler ran out of time type-checking it).
        // Both stop while the list is out of sight; `AgentsCoverWatch` reads that and carries them.
        .background {
            AgentsCoverWatch(pushedOver: pushedOver, herdr: herdr, herdrKey: HerdrWatch.key(machines),
                             catalog: workspaceCatalog, catalogEnabled: grouping == .repo,
                             covered: { [feed] in feed.hold.set(.outOfSight, $0) },
                             chatShown: { openedWorkspace = nil })
        }
        #if DEBUG
        // `-openQuietSpaces YES` (with `-tab dev`): every quiet space opened, for screenshots (`-dev.folded` folds spaces).
        .task(id: quiet.map(\.id)) { if UserDefaults.standard.bool(forKey: "openQuietSpaces") { expandedQuiet.formUnion(quiet.map(\.id)) } }
        #endif
        .renameSpaceAlert($renamingSpace)
        .renameSessionAlert($renamingSession)
        .sheet(item: $renamingWorktree) { workspace in
            RenameWorktreeSheet(workspace: workspace) {
                await workspaceCatalog.refresh(model.groupIDs) { model.api($0) }
                await model.refresh()
            }
        }
        .confirmClose($closingSession)
        .confirmDeleteSession($deletingSession)
        .nodeActionPrompts()
        let sheets = AnyView(decorated)
        .sheet(item: $creatingChild, onDismiss: {
            guard let child = createdWorktree else { return }
            createdWorktree = nil
            selectEmptyWorktree(child)
            startingWorktree = child
        }) { workspace in
            NewWorktreeSheet(group: workspace.creationGroup, workspace: workspace) { made in
                creatingChild = nil
                createdWorktree = workspace.createdWorkspace(made)
                Task { await workspaceCatalog.refresh(model.groupIDs) { model.api($0) } }
            }
        }
        .sheet(item: $startingWorktree) { workspace in
            NewChatView(preset: workspace.preset) { session in
                startingWorktree = nil
                openFromList(session.key)
            }
        }
        .sheet(item: $startingProject) { project in
            NewChatView(projectFolder: project) { session in
                startingProject = nil
                openFromList(session.key)
            }
        }
        .sheet(item: $linkingSession) { s in
            TodoLinkAgentSheet(session: s)
        }
        .sheet(item: $answering) { SessionlessCardSheet(interaction: $0) }
        .sheet(item: $linkingSpace) { group in
            if let space = group.todoSpace {
                TodoLinkAgentSheet(space: TodoItem.Space(machineID: group.machineID, space: space))
            }
        }
        // What this visit showed as Unread or Just finished: each keeps that place once seen, here or elsewhere (#12514);
        // noted again when rows that are read stay is chosen once more (the list's rows have not changed since).
        let noted: [SessionKey: AgentListOrder.Held] = model.seenRows == .stay ? needsSeen : [:]
        let list = AnyView(sheets)
        .task(id: listQuery) {
            paginationArmed = false
            if !query.isEmpty { try? await Task.sleep(for: .milliseconds(250)) }
            guard !Task.isCancelled else { return }
            await model.sessionLists.select(listQuery, hub: selectedHub, model: model)
        }
        .task(id: hubFilter) { await model.sessionLists.select(listQuery, hub: selectedHub, model: model) }
        .onChange(of: matching) { _, now in kept.formUnion(now) }
        .onChange(of: noted, initial: true) { _, now in model.noteShownUnseen(now) }
        .onChange(of: query.isEmpty, initial: true) { _, empty in feed.searching = !empty }
        .onChange(of: grouping, initial: true) { _, mode in feed.recent = mode == .recent }
        #if DEBUG && (os(iOS) || os(visionOS))
        .onChange(of: searching) { _, now in SearchTrace.shared.note("searching \(now)") }
        .onChange(of: query) { _, now in SearchTrace.shared.note("query '\(now)'") }
        #endif
        .onChange(of: machineFilter) { _, _ in kept = []; releaseSeenRows() }
        .onChange(of: hubFilter) { _, _ in releaseSeenRows() }
        // Narrowed to one hub, another one made current (#11657): a computer of the hub before is not on this list, also
        // when the switch was made while the list was not there.
        .modifier(MachineFilterFollowsHub(hubFilter: hubFilter, machineFilter: $machineFilter))
        .onAppear {
            #if DEBUG && os(iOS)
            sortShadow.enable()
            #endif
            feed.follow(model)
            // The list rests with the field folded, a band down from its top: that is still the top (`atTop`); counted
            // as read down, a change at launch kept a row and threw the list ~350 pt down (SearchOpenTests).
            if ownsTop { Self.keepReader(feed.reader, top: Theme.Size.searchBand) { [weak header] in header?.listScrollView } }
            else { Self.keepReader(feed.reader) { [weak searchClose] in searchClose?.listScrollView } }
        }
        .onScrollPhaseChange { _, phase in
            #if (os(iOS) || os(visionOS)) && (DEBUG || PERF_SIGNPOST)
            PerfScroll.mark("Agents list \(phase)")
            #endif
            if phase != .idle { feed.reader.cancel(); paginationArmed = true }
            if ownsTop, phase == .idle { header.settle() }
            feed.hold.set(.scrolling, phase != .idle)
        }
        // Another section in front (the page stays in the window behind it, `MacPageStack`).
        .background { PageFrontWatcher { front in feed.hold.set(.behind, !front) } }
        // The row being read, brought back when a change took it off screen (`ReaderAnchor.reveal`). Around a local:
        // the chain above in a closure is type-checked with all of it.
        return ScrollViewReader { proxy in
            list.onAppear {
                feed.reader.reveal = { id, at in proxy.scrollTo(id, anchor: UnitPoint(x: 0.5, y: at)) }
                // The Sidebar layout's jumps (#6332): to the Agents' Needs You section, and to the Agents' heading.
                unified?.scrollToNeeds = { withAnimation(Theme.Motion.layout) { proxy.scrollTo(UnifiedSidebar.needsAnchor, anchor: .top) } }
                unified?.scrollToAgents = { withAnimation(Theme.Motion.layout) { proxy.scrollTo(UnifiedSidebar.agentsAnchor, anchor: .top) } }
            }
        }
        .environment(feed) // a quiet space's line reads its agents' times from the rows (`SpaceGroup.updatedAt(_:)`)
        .environment(\.openUnreadSide, sideOpener.set { parent, side in openUnreadSide(parent: parent, side: side) })
    }

    /// The quick filter bar. Equal while the counts are: the bar is not drawn again for pushes that change none of
    /// them. The tap reads the list as it is then.
    /// `base`: as the body worked it out (working it out again here went through every agent twice more per push).
    private func filterBar(_ base: [Session]) -> some View {
        QuickFilterBar(selection: quick, counts: model.sessionLists.paged ? [:] : filterCounts,
                       countsUnknown: model.sessionLists.paged, pageAligned: ownsTop) { choose($0, self.base) }.equatable()
            .searchTraceMark("filter")
    }

    /// The states' counts, with Needs You counting the section's rows (`AgentsNeedsYou`): sessions, as the badge does.
    private var filterCounts: [AgentFilter: Int] {
        var counts = lookup.projection?.filterCounts ?? [:]
        if let n = lookup.needsYou { counts[.needsYou] = n }
        return counts
    }

    /// The machine the list is narrowed to, in the shell: its menu sits behind the selected Agents switch and says
    /// nothing while closed, so a machine chosen once hid every other computer's agents with no trace (#1659). A tap
    /// shows every machine again.
    @ViewBuilder private var machineFilterChip: some View {
        if shell != nil, !machineFilter.isEmpty, let chosen = model.machineChoices.first(where: { $0.id == machineFilter }) {
            Button {
                Haptics.tap()
                withAnimation(Theme.Motion.standard) { machineFilter = "" }
            } label: {
                Chip(text: String(localized: "Only \(chosen.name)"), symbol: Theme.Icon.clear, look: .tint(Theme.accent))
            }
            .buttonStyle(.plain)
            .padding(.horizontal, Theme.Space.page)
            .padding(.bottom, Theme.Space.sm)
            .accessibilityIdentifier("agents.machineFilter")
            .accessibilityLabel("Only \(chosen.name)")
            .accessibilityHint("Shows all machines")
        }
    }

    /// A quick filter chosen (again): it starts from what matches now, without the rows read during the visit (#12514).
    private func choose(_ f: AgentFilter, _ base: [Session]) {
        Haptics.tap()
        quick = f
        kept = Set(base.filter { f != .all && f.matches($0, muted: model.isMuted($0)) }.map(\.key))
        releaseSeenRows()
    }

    /// The list starts again (#12514): the rows read during the visit go where their state puts them, and the ones it
    /// shows unseen now are this visit's (the projection the body last published).
    private func releaseSeenRows() {
        model.releaseAllSeen(keeping: lookup.projection?.needsSeen ?? [:])
    }

    #if os(iOS)
    /// Says once that a read row now stays where it was (#12514), with the way to the setting; closed for good with ✕.
    private var seenRowsNotice: some View {
        NoticeBanner(text: SeenRows.notice, tone: .neutral, dismiss: {
            withAnimation(Theme.Motion.standard) { seenRowsNoticed = true }
        }) {
            Button {
                Haptics.tap()
                model.revealSeenRowsSetting = true
                model.settingsPath = NavigationPath([SettingsPage.appearance, SettingsPage.layout])
                model.showSettings()
            } label: {
                Text(String(localized: "Change in Settings"))
                    .tapTargetHeight()
                    .contentShape(.rect)
            }
            .accessibilityIdentifier("agents.seenNotice.settings")
        }
        .accessibilityIdentifier("agents.seenNotice")
    }

    /// The color behind the list (`AgentsListPage`), which what floats over its foot is washed back to.
    private var listPage: Color {
        if unified != nil { return UnifiedSidebar.columnColor }
        return grouping == .repo && !repoTree ? Theme.WorkspaceSidebar.background : Theme.groupedPage
    }
    #endif

    /// One row of the Needs You section (`AgentsNeedsYou.Row`): a session's only card as it always was; several cards of
    /// one session under its name, each answering itself; a session waiting in its terminal, a row that opens it.
    @ViewBuilder private func needsYouRow(_ row: AgentsNeedsYou.Row) -> some View {
        if row.cards.count == 1, let ix = row.cards.first {
            cardRow(ix)
        } else if let first = row.cards.first {
            if compact {
                agentNavigationLink(first.sessionKey) {
                    CompactInteractionRow(interaction: first, machine: machine(first.machineRef)?.title, more: row.cards.count - 1)
                }
                .listRowInsets(CompactList.rowInsets)
                .accessibilityIdentifier("agents.interaction")
            } else if floating {
                VStack(alignment: .leading, spacing: 0) { stackedCards(row.cards, dividers: true) }
                    .card()
                    .listRowInsets(Self.capsuleInsets)
                    .listRowBackground(Color.clear)
                    .listRowSeparator(.hidden)
            } else {
                VStack(alignment: .leading, spacing: 0) { stackedCards(row.cards, dividers: false) }
                    .alignmentGuide(.listRowSeparatorLeading) { _ in 0 } // rows of different shapes: one line
            }
        } else {
            waitingRow(row)
        }
    }

    /// One pending card as its own row.
    @ViewBuilder private func cardRow(_ ix: Interaction) -> some View {
        let titleOverride = interactionSessionTitleOverride(for: ix)
        if compact && ix.isSessionless {
            Button { answering = ix } label: {
                CompactInteractionRow(interaction: ix, machine: machine(ix.machineRef)?.title)
                    .modifier(ButtonRowLabel(edges: CompactList.rowInsets))
            }
            .buttonStyle(.plain)
            .modifier(ButtonRowInsets(edges: CompactList.rowInsets))
            .accessibilityIdentifier("agents.interaction")
        } else if compact {
            // Compact List: one row like an agent's; the chat answers it.
            agentNavigationLink(ix.sessionKey) {
                CompactInteractionRow(interaction: ix, machine: machine(ix.machineRef)?.title, sessionTitleOverride: titleOverride)
            }
            .listRowInsets(CompactList.rowInsets)
            .accessibilityIdentifier("agents.interaction")
        } else if floating {
            // A card of its own on the page, as the agents' cards. The capsules: several groups of hubs,
            // the card says whose (one group: as it always was, no machine); State Cards: as the
            // grouped list's card says, the machine and the agent.
            InteractionPreviewCard(interaction: ix, machine: capsules && !model.isMultiHub ? nil : machine(ix.machineRef)?.title,
                                   agentLabel: capsules ? "Claude" : session(ix.sessionKey)?.agentLabel ?? "Claude",
                                   inCell: false, sessionTitleOverride: titleOverride) { openCard(ix) }
                .listRowInsets(Self.capsuleInsets)
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
                .accessibilityIdentifier("agents.interaction")
        } else {
            InteractionPreviewCard(interaction: ix, machine: machine(ix.machineRef)?.title,
                                   agentLabel: session(ix.sessionKey)?.agentLabel ?? "Claude", inCell: true,
                                   sessionTitleOverride: titleOverride) { openCard(ix) }
                .alignmentGuide(.listRowSeparatorLeading) { _ in 0 } // rows of different shapes: one line
                .accessibilityIdentifier("agents.interaction")
        }
    }

    /// A session's cards, oldest first, in one cell: its place over the first only. Inside a card of its own a line
    /// parts them; in the list's cell only room does, so the list's lines keep marking where a session ends.
    @ViewBuilder private func stackedCards(_ cards: [Interaction], dividers: Bool) -> some View {
        ForEach(Array(cards.enumerated()), id: \.element.uid) { i, ix in
            if i > 0 && dividers { Divider().padding(.vertical, Theme.Space.s) }
            InteractionPreviewCard(interaction: ix, machine: capsules && !model.isMultiHub ? nil : machine(ix.machineRef)?.title,
                                   agentLabel: capsules ? "Claude" : session(ix.sessionKey)?.agentLabel ?? "Claude",
                                   inCell: true, showsPlace: i == 0,
                                   sessionTitleOverride: interactionSessionTitleOverride(for: ix)) { openCard(ix) }
                .padding(.top, i > 0 && !dividers ? Theme.Space.m : 0)
                .accessibilityIdentifier("agents.interaction")
        }
    }

    private func interactionSessionTitleOverride(for ix: Interaction) -> String? {
        guard !ix.isSessionless, let s = session(ix.sessionKey) else { return nil }
        return s.displayTabName(preferTab: model.prefersHerdrTabName(for: s))
    }

    /// A session that waits in its terminal, no card read: a row that opens it.
    @ViewBuilder private func waitingRow(_ row: AgentsNeedsYou.Row) -> some View {
        let s = session(row.key)
        let place = s.flatMap { machine($0.machineRef)?.title }
        let preferTab = s.map { model.prefersHerdrTabName(for: $0) } ?? model.prefersHerdrTabName(for: row.key.group)
        if compact {
            agentNavigationLink(row.key) {
                CompactWaitingRow(session: s, machine: place, since: row.since, preferHerdrTabName: preferTab)
            }
            .listRowInsets(CompactList.rowInsets)
            .accessibilityIdentifier("agents.waiting")
        } else if floating {
            NeedsYouWaitingCard(session: s, machine: capsules && !model.isMultiHub ? nil : place, inCell: false, preferHerdrTabName: preferTab) { openFromList(row.key) }
                .listRowInsets(Self.capsuleInsets)
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
                .accessibilityIdentifier("agents.waiting")
        } else {
            NeedsYouWaitingCard(session: s, machine: place, preferHerdrTabName: preferTab) { openFromList(row.key) }
                .alignmentGuide(.listRowSeparatorLeading) { _ in 0 }
                .accessibilityIdentifier("agents.waiting")
        }
    }

    /// A card's chat, where it is answered; a card on no session opens by itself (#4059).
    private func openCard(_ ix: Interaction) {
        if ix.isSessionless { answering = ix } else { openFromList(ix.sessionKey) }
    }

    /// Something the list opens: in the split it replaces the detail column; in one stack the list is at the root, so
    /// this is the same as pushing it.
    private func openFromList(_ value: some Hashable) {
        openedWorkspace = nil
        #if os(iOS)
        if let unified {
            // The page beside the one sidebar (#8359, #8603): its root replaced, not pushed. A space at the root is index -1 (`openSpace`).
            spaceAt = (value as? SpaceRoute).map { [-1: $0.id] } ?? [:]
            unified.select(.page(AnyHashable(value)), path: $path)
            return
        }
        #endif
        spaceAt = (value as? SpaceRoute).map { [0: $0.id] } ?? [:]
        path = NavigationPath([value])
    }

    private func selectEmptyWorktree(_ workspace: HostWorkspace) {
        spaceAt = [:]
        #if os(iOS)
        if let unified { unified.select(.conversation, path: $path) } else { path = NavigationPath() }
        #else
        path = NavigationPath()
        #endif
        openedWorkspace = workspace.key
        AppModel.rememberChat(nil)
    }

    private func openWorktree(_ worktree: RepoTree.Worktree) {
        if let first = WorkspaceNavigation.firstAgent(rendered: worktree.sessions, workspace: worktree.workspace,
                                                      inventory: model.sessionsNow, catalog: workspaceCatalog.workspaces,
                                                      ordered: { ordered($0) }) { openFromList(first) }
        else if let workspace = worktree.workspace { selectEmptyWorktree(workspace) }
    }

    private func openWorktree(_ workspace: HostWorkspace) {
        if let first = WorkspaceNavigation.firstAgent(rendered: [], workspace: workspace, inventory: model.sessionsNow,
                                                      catalog: workspaceCatalog.workspaces, ordered: { ordered($0) }) { openFromList(first) }
        else { selectEmptyWorktree(workspace) }
    }

    #if os(iOS)
    /// The Sidebar layout's Agents heading (#6332), as the web sidebar's: the name, then the list's ⋯ (grouping, sort,
    /// display, fold all), Personal Agents and New Session, which the Agents page has in its bar.
    private var unifiedAgentsHeading: some View {
        HStack(spacing: Theme.Space.xs) {
            // The name in the Persona rows' heading box (`SidebarHeadingBox`, #10551): the row is as tall as Pages' and
            // Persona Chats', its buttons sit in it, and it stands as far below the row before it.
            HStack(spacing: Theme.Space.xs) {
                Text("Agents").font(Theme.Font.captionStrong).foregroundStyle(.secondary).lineLimit(1)
                Spacer(minLength: Theme.Space.s)
            }
            .modifier(SidebarHeadingBox())
            .accessibilityElement(children: .combine)
            .accessibilityAddTraits(.isHeader)
            .accessibilityIdentifier("sidebar-agents-heading")
            // In a view graph of its own: the list's updates replaced the open menu and scrolled it to its top (#10738).
            MenuIsland(content: SidebarAgentsListMenu(actions: menuActions))
            if showsPersonalEntry {
                Button { openFromList(PersonalAgentsRoute()) } label: {
                    ThemedIcon(Theme.Icon.personalAgents).font(Theme.Font.label).foregroundStyle(.secondary)
                        .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.wellXS)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("Personal Agents")
                .accessibilityIdentifier("agents.personal")
            }
            Button { showNew = true } label: {
                ThemedIcon(Theme.Icon.add).font(Theme.Font.label).foregroundStyle(.secondary)
                    .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.wellXS)
                    .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("New Session [Chats View 58e21b]")
            .accessibilityIdentifier("sidebar-agents-new")
        }
        // Where the Persona rows' headings start (their row's inset and their own), so the three headings line up.
        .padding(.leading, Theme.Space.s + Theme.Space.m)
        .padding(.trailing, Theme.Space.s)
        .padding(.top, SidebarHeadingBox.above)
        // The Separate layout's Agents tab has no Pages heading for the head to place its face by (`rowsLeading`): it
        // takes the Persona tab's line (`linedUpWith`, #8596). Before the Persona tab is laid out this heading gives an
        // estimate, its row's start plus the Persona rows' extra inset on an iPad. Without either the face sat
        // `Theme.Space.xl` in, 4 pt left of the Persona tab's (#8359).
        .onGeometryChange(for: CGFloat.self) { $0.frame(in: .named(UnifiedSidebar.columnSpace)).minX } action: { x in
            guard sidebarRows == nil, let unified, x >= 0 else { return }
            unified.ui.placeRows(at: unified.linedUpWith?.ui.rowsLeading ?? x + Theme.Space.s)
        }
        .onChange(of: sidebarRows == nil ? unified?.linedUpWith?.ui.rowsLeading : nil, initial: true) { _, line in
            guard let line, let unified else { return }
            unified.ui.placeRows(at: line)
        }
        .id(UnifiedSidebar.agentsAnchor)
    }
    #endif

    /// `ReaderAnchor` on the list's own scroll view.
    /// `top`: how far down from the list's top it still counts as at the top.
    private static func keepReader(_ reader: ReaderAnchor, top slack: CGFloat = 0, _ listView: @escaping () -> UIScrollView?) {
        reader.shown = {
            guard let list = listView(), list.window != nil else { return nil }
            let inset = list.adjustedContentInset
            let frame = list.convert(list.bounds, to: nil)
            let top = frame.minY + inset.top, bottom = frame.maxY - inset.bottom
            guard bottom > top else { return nil }
            return (top...bottom, list.contentOffset.y <= -inset.top + slack + 1)
        }
        reader.scroll = { dy in
            guard let list = listView() else { return }
            let inset = list.adjustedContentInset
            let lowest = -inset.top
            let highest = max(lowest, list.contentSize.height + inset.bottom - list.bounds.height)
            list.contentOffset.y = min(max(list.contentOffset.y + dy, lowest), highest)
        }
        reader.moving = {
            guard let list = listView() else { return false }
            return list.isTracking || list.isDragging || list.isDecelerating
        }
    }

    /// Opens a search result: the search closes first (open, it keeps the system navigation bar up over the chat), and
    /// the chat is pushed once it has folded away. The search's text and results stay for coming back.
    private func openFromSearch(_ key: SessionKey) {
        if ownsTop {
            // The compact path binding closes the custom field before pushing (`AgentsHeader.closeSearchForPush`).
            // Nothing to wait for: our own field holds no navigation bar up. The text and results stay.
            path.append(key)
            return
        }
        searchedBeforePush = query
        searching = false
        searchClose.whenClosed { path.append(key) }
    }

    /// The row follows its own agent (`AgentsFeed.Row`): what only it shows changes it, not the list.
    @ViewBuilder private func slotRowContent(_ s: Session, context: Bool, inStateSection: Bool, cardLast: Bool? = nil, showHost: Bool = false,
                                             pane: TabPanes.Role? = nil, open: Bool = false) -> some View {
        if feed.following {
            FeedRow(row: feed.row(s), reader: feed.reader) { live, time in
                slotRowDrawn(live, time: time, context: context, inStateSection: inStateSection, cardLast: cardLast, showHost: showHost, pane: pane, open: open)
            }
        } else {
            slotRowDrawn(s, time: nil, context: context, inStateSection: inStateSection, cardLast: cardLast, showHost: showHost, pane: pane, open: open)
        }
    }

    /// Opens an unread folded side session in its parent's chat in one tap (#13524).
    private func openUnreadSide(parent: SessionKey, side: SessionKey) {
        model.sideLanding = SideLanding(parent: parent, side: side)
        if searching {
            openFromSearch(parent)
        } else {
            openFromList(parent)
        }
    }

    /// Reads the unread-side summary for a row kept Unread by folded sides (#13524). Rows with no unread side return
    /// nil immediately without touching `model.sides(of:)`, keeping observation narrow (P5).
    private func unreadSideSummary(for s: Session) -> SideFold.UnreadSummary? {
        guard s.state == .unread, !s.assistant, (s.children?.sidesUnread ?? 0) > 0 else { return nil }
        if let summary = model.sides(of: s.key).unreadSummary { return summary }
        return SideFold.unreadSummary(parent: s, ownState: model.session(s.key)?.state, sides: nil)
    }

    @ViewBuilder private func slotRowDrawn(_ s: Session, time: AgentTime?, context: Bool, inStateSection: Bool, cardLast: Bool?, showHost: Bool = false,
                                           pane: TabPanes.Role? = nil, open: Bool = false) -> some View {
        // herdr is where most agents run: only the others name their host
        let host = showHost ? s.hostKind.flatMap { $0 == "herdr" ? nil : $0 } : nil
        let sideUnread = unreadSideSummary(for: s)
        let preferTab = model.prefersHerdrTabName(for: s)
        let pendingTitle = model.pendingAgentTitle(s.key)
        if grouping == .repo && !repoTree {
            // The Mac's and Apple Vision Pro's repository cards keep their rows. On iOS By Repository draws the same
            // rows as every other grouping (#3753).
            WorkspaceAgentRow(session: s, pendingTitle: pendingTitle, time: time, muted: model.isMuted(s), sideUnread: sideUnread, preferHerdrTabName: preferTab)
        } else if compact {
            CompactSlotRow(session: s, pendingTitle: pendingTitle, muted: model.isMuted(s), hideState: inStateSection, space: context ? (s.spaceName ?? s.folderName) : nil, host: host, time: time, pane: pane, sideUnread: sideUnread, preferHerdrTabName: preferTab)
                .equatable()
        } else {
            SlotRow(session: s, pendingTitle: pendingTitle, muted: model.isMuted(s), space: context ? (s.spaceName ?? s.folderName) : nil,
                    machine: rowMachine(s, context: context), host: host,
                    hideState: inStateSection, surface: cardLast.map { .cardRow(last: $0) } ?? .capsule, time: time, pane: pane,
                    rowStyle: listRowStyle, dense: !ownsTop, sideUnread: sideUnread, preferHerdrTabName: preferTab)
                .equatable()
        }
    }

    /// The machine a row with `context` names: not By Machine or for one machine picked. The row styles (#2530) name it
    /// only where the list mixes machines (one machine's agents say nothing new with it); BeautifulUI's capsules and the
    /// Mac's rows always did. The row's key takes it too: a row drawn before the list came to mix machines changes.
    private func rowMachine(_ s: Session, context: Bool) -> String? {
        guard context, grouping != .machine, machineFilter.isEmpty else { return nil }
        if listRowStyle != nil, !AppStyle.current.usesBeautifulUI, lookup.projection?.byHost == false { return nil }
        return machine(s.machineRef)?.title
    }

    // MARK: By space

    /// By Project: the spaces as they always were; with agents on more than one machine, each machine's spaces under its
    /// heading, its quiet ones in its own Idle Spaces (#2108).
    @ViewBuilder private func spaceSections(active: [SpaceGroup], quiet: [SpaceGroup], byHost: Bool) -> some View {
        if byHost {
            let hosts = RepoTree.hostOrder(Set((active + quiet).map(\.machineRef)), machines: machines.map(\.ref))
            ForEach(hosts, id: \.self) { ref in
                let mine = active.filter { $0.machineRef == ref }
                let still = quiet.filter { $0.machineRef == ref }
                hostGroup(ref, sessions: (mine + still).flatMap(\.sessions)) {
                    ForEach(mine) { g in spaceSection(g, namesMachine: false) }
                    quietSection(still, title: String(localized: "Idle Spaces"), namesMachine: false)
                }
            }
        } else {
            ForEach(active) { g in spaceSection(g, namesMachine: machineFilter.isEmpty) }
            quietSection(quiet, title: String(localized: "Idle Spaces"), namesMachine: machineFilter.isEmpty)
        }
    }

    /// A space: tap its header to fold it (the counts stay on it); busy (and pinned / looping) agents first, idle ones
    /// fold into one row so a big workspace stays compact. Under a quick filter every agent it left shows.
    private func spaceSection(_ g: SpaceGroup, namesMachine: Bool) -> some View {
        foldableSection(g.id) { spaceHeader(g, folded: $0, namesMachine: namesMachine) } content: { agentRows(g.sessions, key: g.id) }
    }

    /// A section that folds from its header. Folded: the header alone, as a plain row. As an empty section each kept a
    /// header's and a section's padding (Collapse All showed six a screen).
    @ViewBuilder private func foldableSection<Header: View, Content: View>(_ id: String, @ViewBuilder header: (Bool) -> Header,
                                                                           @ViewBuilder content: () -> Content) -> some View {
        if isFolded(id) {
            Section {
                header(true)
                    // Where a header sits; the floating list: the page's edge, where an open space's title is too.
                    .listRowInsets(floating ? EdgeInsets(top: 0, leading: Theme.Space.page, bottom: 0, trailing: Theme.Space.page)
                                   : Self.foldedHeaderInsets)
                    .listRowBackground(Color.clear)
                    .listRowSeparator(.hidden)
            }
            .listSectionSpacing(.custom(0))
        } else {
            listSection(content: content) { header(false) }
        }
    }

    /// The agents of a space or a working tree: busy (and pinned / looping) ones first, idle ones behind one "N more idle"
    /// row (`key` remembers it open). `depth`: levels in, under a working tree's row (2 under a nested one's).
    /// The agents of a tab with several together, under the tab's row (`tabRows`, #2320).
    @ViewBuilder private func agentRows(_ list: [Session], key: String, depth: Int = 0) -> some View {
        let rows = lookup.projection?.rows[key].map { (busy: $0.busy, idle: $0.idle) } ?? order.tabEntries(list, all: quick == .all)
        cappedRows(rows.busy, key: key, depth: depth) { e in entryRows(e, depth: depth) }
        if !rows.idle.isEmpty {
            let idle = rows.idle.flatMap(\.sessions)
            if expandedIdle.contains(key) || !query.isEmpty {
                ForEach(rows.idle) { e in entryRows(e, depth: depth) }
                if query.isEmpty { collapseRow(String(localized: "Hide \(idle.count) Idle"), key: key, depth: depth) }
            } else {
                expandRow(idle, key: key, depth: depth)
            }
        }
    }

    /// One agent's row, or a tab's: its row, then (unless folded) its agents, the starter on top marked, the others a
    /// level in under it.
    @ViewBuilder private func entryRows(_ e: TabPanes.Entry, depth: Int = 0) -> some View {
        switch e {
        case .agent(let s):
            slotRow(s, depth: depth)
        case .tab(let g):
            tabRow(g, depth: depth)
            if !isFolded(g.foldID) {
                ForEach(g.sessions) { s in slotRow(s, depth: depth, pane: g.role(of: s), branch: g.isBranch(s)) }
            }
        }
    }

    /// A tab's row: its name, what its most urgent agent is doing in that one's state, 「N 个 Agent」; tap to fold it.
    private func tabRow(_ g: TabPanes.Group, depth: Int) -> some View {
        let folded = isFolded(g.foldID)
        // By Repository moves it in by the tree's levels (`TreeIndent`), on the edges of its display's rows.
        let edges = compact ? rowInsets(level: depth) : floating ? Self.tabCapsuleInsets(level: repoTree ? 0 : depth) : nil
        return Button { toggleFold(g.foldID) } label: {
            TabGroupRow(group: g, folded: folded, preferHerdrTabName: model.prefersHerdrTabName(for: g.lead)).padding(.leading, nested(depth))
                .modifier(TreeIndent(level: repoTree ? depth : 0))
                .modifier(ButtonRowLabel(edges: edges))
        }
        .buttonStyle(.plain)
        .modifier(FoldAnchor(id: g.foldID, reader: feed.reader))
        .modifier(ButtonRowInsets(edges: edges))
        .listRowBackground(repoTree ? treeBackground(false) : floating ? AnyView(Color.clear) : nil)
        .listRowSeparator(floating || repoTree ? .hidden : .automatic)
        .accessibilityIdentifier("agents.tab")
    }

    private func spaceScope(_ group: SpaceGroup) -> RuntimeSessionLists.Scope {
        var query = listQuery
        query.machine = group.machineID
        query.group = group.workspaceGroupID ?? (group.spaceID == "-" ? "" : group.spaceID)
        return RuntimeSessionLists.Scope(hub: group.group, query: query)
    }

    private func machineScope(_ machine: Machine) -> RuntimeSessionLists.Scope {
        var query = listQuery
        query.machine = machine.id
        return RuntimeSessionLists.Scope(hub: machine.group, query: query)
    }

    private func repoScope(_ repo: RepoTree.Repo) -> RuntimeSessionLists.Scope {
        var query = listQuery
        query.machine = repo.machine.id
        query.project = repo.path
        return RuntimeSessionLists.Scope(hub: repo.machine.group, query: query)
    }

    /// A visible ⋯ for open / mark read / pin / mute: long press on a list section header does not reach a context menu.
    private func spaceHeader(_ g: SpaceGroup, folded isFolded: Bool, namesMachine: Bool) -> some View {
        HStack(spacing: Theme.Space.xs) {
            Button { toggleFold(g.id) } label: {
                SpaceHeader(group: g, machine: machine(g.machineRef), showMachine: namesMachine,
                            pinned: model.isPinned(g), muted: model.isMuted(g), folded: isFolded)
                    // As tall as the ⋯ beside it: the whole header folds it, not only its words (#3753).
                    .modifier(TouchHeight())
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("agents.space")
            TodoSpaceTag(group: g) { todoID in openFromList(TodoDetailRoute(id: todoID)) }
            if let folder = availableProjectFolder(g) {
                Button {
                    startingProject = folder
                } label: {
                    ThemedIcon(Theme.Icon.add).font(Theme.Font.label).foregroundStyle(.secondary)
                        .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("agents.project.new")
                .accessibilityLabel("New Agent in \(g.headingLabel)")
            }
            if g.workspaceGroupID == nil {
                Menu {
                    SpaceHeaderMenu(group: g, isMuted: model.isMuted, rename: { renamingSpace = g }, open: { openFromList(SpaceRoute(id: g.id)) },
                                    linkTodo: { linkingSpace = g }).equatable()
                } label: {
                    headerMenuLabel
                }
                // Compact List: as quiet as the counts beside it (a Menu's label takes the accent otherwise).
                .tint(compact ? Color.secondary : nil)
                .labelOnlyMenu()
                .accessibilityLabel("More actions for \(g.name) [Chats View 661d12]")
            }
        }
        .task(id: spaceScope(g)) { await model.sessionLists.loadCounts(spaceScope(g)) }
    }

    private func availableProjectFolder(_ group: SpaceGroup) -> AgentProjectFolder? {
        guard machine(group.machineRef)?.online == true,
              model.sessionLists.hasCompleteSessions(group.group) else { return nil }
        return group.projectFolder
    }

    /// The ⋯ on a space's or a repository's header.
    private var headerMenuLabel: some View {
        ThemedIcon(compact ? Theme.Icon.more : Theme.Icon.moreMenu)
            .font(compact ? CompactList.metaStrong : Theme.Font.body)
            .foregroundStyle(.secondary)
            .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
            .contentShape(.rect)
    }

    /// The quiet spaces or repositories, one line each, opened in place: Idle Spaces / Idle Repositories.
    @ViewBuilder private func quietSection(_ groups: [SpaceGroup], title: String, namesMachine: Bool, repos: Bool = false) -> some View {
        if !groups.isEmpty {
            listSection {
                ForEach(groups) { g in
                    if repos {
                        QuietRepositorySelection(group: g.id, membership: lookup.projection?.quietRepositoryBySession ?? [:],
                                                 expandedQuiet: expandedQuiet, forceExpanded: !query.isEmpty,
                                                 enabled: repoTree && !ownsTop && openedWorkspace == nil) { selected in
                            quietRow(g, namesMachine: namesMachine, repo: true, selected: selected)
                        }
                    } else {
                        quietRow(g, namesMachine: namesMachine, repo: false, selected: false)
                            .contextMenu {
                                if let folder = availableProjectFolder(g) {
                                    Button {
                                        startingProject = folder
                                    } label: {
                                        Label("New Agent in \(g.headingLabel)", systemImage: Theme.Icon.add)
                                    }
                                }
                            }
                    }
                }
            } header: {
                SectionHeader(title: title, count: groups.count, tone: StatusPalette.idle)
            }
        }
    }

    @ViewBuilder private func quietRow(_ g: SpaceGroup, namesMachine: Bool, repo: Bool, selected: Bool) -> some View {
        if compact {
            compactQuietSpace(g, namesMachine: namesMachine, repo: repo, selected: selected)
        } else if capsules {
            quietSpaceCard(g, namesMachine: namesMachine, repo: repo, selected: selected)
        } else if floatingCards {
            quietSpaceStateCard(g, namesMachine: namesMachine, repo: repo, selected: selected)
        } else {
            let open = expandedQuiet.contains(g.id) || !query.isEmpty
            let row = DisclosureGroup(isExpanded: Binding(
                get: { open },
                set: { if $0 { expandedQuiet.insert(g.id) } else { expandedQuiet.remove(g.id) } }
            )) {
                let roles = TabPanes.roles(g.sessions)
                ForEach(TabPanes.grouped(ordered(g.sessions))) { s in slotRow(s, pane: roles[s.key]) }
            } label: {
                QuietSpaceRow(group: g, machine: machine(g.machineRef), showMachine: namesMachine, repo: repo)
            }
            .accessibilityAddTraits(selected ? .isSelected : [])
            if open {
                // A DisclosureGroup gives its identifier to every built child; leave the agent rows' own ids intact.
                row
            } else if selected {
                row.accessibilityIdentifier("agents.quietSpace")
                    .listRowBackground(treeBackground(true))
            } else {
                row.accessibilityIdentifier("agents.quietSpace")
            }
        }
    }

    // MARK: By repository

    /// The fold id of a machine where the list is split by machine: the same in By Repository, By Project and By Machine.
    private static func hostFoldID(_ ref: MachineRef) -> String { "machine/" + ref.path }

    /// Nothing going on in it: every agent idle (none pinned, looping or with work in the background). Under a quick
    /// filter nothing is quiet: every repository it leaves is listed.
    private func isQuiet(_ r: RepoTree.Repo) -> Bool {
        !r.structured && (lookup.projection?.repositories[r.id]?.quiet ?? (quick == .all && r.sessions.allSatisfy { !stays($0) }))
    }

    /// By Repository (#2108): the machines when there is more than one, each machine's repositories by name with their working
    /// trees in their card, as Orca's projects; repositories with nothing going on fold into the machine's Idle Repositories.
    @ViewBuilder private func repoSections(_ tree: [RepoTree.Host], byHost: Bool) -> some View {
        ForEach(tree) { host in
            if byHost {
                hostGroup(host.ref, sessions: host.sessions) { hostRepos(host) }
            } else {
                hostRepos(host)
            }
        }
    }

    /// A machine's repositories: the ones with something going on, then the rest folded into its Idle Repositories.
    @ViewBuilder private func hostRepos(_ host: RepoTree.Host) -> some View {
        ForEach(lookup.projection?.busyRepositories[host.id] ?? []) { r in
            #if os(iOS)
            repoCard(r)
            #else
            repoSection(r)
            #endif
        }
        quietSection(lookup.projection?.quietRepositories[host.id] ?? [],
                     title: String(localized: "Idle Repositories"), namesMachine: false, repos: true)
    }

    /// A machine's heading and, unless the machine is folded, the sections under it (its quiet ones last). iOS: the heading is a section of its own, the sections follow it.
    /// The Mac: the heading is the header of one section that holds the machine's sections. A Mac list has no
    /// `listSectionSpacing` and puts about 24pt over every section's header; a heading in a section of its own got that
    /// band above and the next section's below (#2318, about 100pt a heading). Sections inside a section are drawn as
    /// its rows (their headers too), so the machine's repositories or spaces sit right under its heading.
    @ViewBuilder private func hostGroup<Content: View>(_ ref: MachineRef, sessions: [Session],
                                                       @ViewBuilder content: () -> Content) -> some View {
        let open = !isFolded(Self.hostFoldID(ref))
        hostHeading(ref, sessions: sessions)
        if open { content() }
    }

    /// A machine's heading: its mark, name and counts; tap to fold the machine.
    private func hostHeadingRow(_ ref: MachineRef, sessions: [Session]) -> some View {
        let id = Self.hostFoldID(ref)
        let m = machine(ref)
        let group = SpaceGroup(id: id, machineID: ref.id, spaceID: "-", group: ref.group, name: m?.title ?? ref.id, index: 0, sessions: sessions)
        return FoldButton(title: group.name, folded: isFolded(id), detail: StateCounts.spoken(group),
                          bottomPadding: Theme.Space.xs) { toggleFold(id) } label: {
            HostHeading(symbol: m?.symbol ?? Theme.Icon.machine, title: group.name, group: group)
        }
        .accessibilityIdentifier("agents.host")
    }

    /// A machine's heading: a plain row on the page's edge, a level above the headers under it. On iOS its button is a
    /// finger's height (`FoldButton`, #3753), the room it gains above its words: the row gives up that much above, so its
    /// mark stays where it was.
    private func hostHeading(_ ref: MachineRef, sessions: [Session]) -> some View {
        Section {
            hostHeadingRow(ref, sessions: sessions)
                .modifier(ButtonRowInsets(edges: Self.hostHeadingInsets))
                .listRowBackground(Color.clear)
                .listRowSeparator(.hidden)
        }
        .listSectionSpacing(.custom(0))
    }

    #if os(iOS)
    /// By Repository's tree (#2531): a repository is a card of its own (a section of the grouped list; where the rows
    /// float on the page, its lines on the page). Its header folds it, and holds its actions in its long-press menu;
    /// then its worktrees, each line with its agents under it, or the agents a level in under the header when it has its
    /// main checkout alone. The agents are the display's rows, as in every other grouping (#3753).
    @ViewBuilder private func repoCard(_ r: RepoTree.Repo) -> some View {
        let folded = isFolded(r.id)
        Section {
            Button { toggleFold(r.id) } label: {
                RepoCardHeader(name: r.name, folded: folded, workspace: r.flattenedWorkspace)
            }
                .buttonStyle(.plain)
                .contextMenu { repoMenu(r) }
                .accessibilityIdentifier("agents.project")
                .accessibilityLabel(repositoryLabel(r, folded: folded))
                .selectionDisabled()
                .modifier(FoldAnchor(id: r.id, reader: feed.reader))
                .modifier(ButtonRowInsets(edges: rowInsets(level: 0)))
                .listRowBackground(treeBackground(false))
                .listRowSeparator(.hidden)
                .task(id: repoScope(r)) { await model.sessionLists.loadCounts(repoScope(r)) }
            if !folded {
                if r.flat {
                    agentRows(r.worktrees[0].sessions, key: r.worktrees[0].id, depth: 1)
                } else {
                    ForEach(r.worktrees) { w in treeWorktreeRows(w, project: r.name) }
                }
            }
        }
    }

    /// What the ⋯ and the to-do's mark beside a repository's header offered: when its agents are all in one herdr space
    /// (the usual case), that space's pin, mute, rename, open and to-do; otherwise Mark All as Seen.
    @ViewBuilder private func repoMenu(_ r: RepoTree.Repo) -> some View {
        if let workspace = r.flattenedWorkspace { worktreeMenu(workspace) }
        let prepared = lookup.projection?.repositories[r.id]
        if let space = prepared?.space {
            SpaceHeaderMenu(group: space, isMuted: model.isMuted, rename: { renamingSpace = space }, open: { openFromList(SpaceRoute(id: space.id)) },
                            linkTodo: { linkingSpace = space }).equatable()
            TodoSpaceMenuItem(group: space) { todoID in openFromList(TodoDetailRoute(id: todoID)) }
        } else {
            RepoMenuItems(unread: prepared?.unread ?? [], all: prepared?.keys ?? []).equatable()
        }
    }

    /// A worktree in its repository's card (#2531): its line, its agents a level in, then the worktrees nested under it
    /// behind one line (Orca's child worktrees, #3075), each a level in once opened.
    @ViewBuilder private func treeWorktreeRows(_ w: RepoTree.Worktree, project: String) -> some View {
        ForEach(workspaceRows(w, project: project)) { row in
            if row.toggle {
                treeChildrenLine(row.workspace, level: row.depth + 1)
            } else {
                treeWorktreeLine(row.workspace, level: row.depth + 1, parent: row.parent, project: row.project)
            }
        }
    }

    /// What a worktree's line says after its name, small: the repository of a child from another project, its branch
    /// when the name is not it, or Detached HEAD (`caution`), as its card said on the host's catalog.
    private func treeDetail(_ w: RepoTree.Worktree, parent: HostWorkspace?, project: String) -> (text: String, caution: Bool)? {
        if let badge = w.repositoryBadge(parent: parent, inheritedName: project) { return (badge, false) }
        guard let target = w.workspace else { return nil }
        if !target.branch.isEmpty { return target.branch == w.name ? nil : (target.branch, false) }
        if target.workspaceKind == "git" {
            return (String(localized: "Detached HEAD") + (target.head.isEmpty ? "" : " @ " + String(target.head.prefix(7))), true)
        }
        return nil
    }

    /// A worktree's line, then (unless folded) its agents under it. One the host lists (a workspace) opens its first
    /// agent, or its page, on a tap and holds its actions in its long-press menu; its chevron folds it. One with nothing
    /// going on starts folded, as a quiet space does.
    @ViewBuilder private func treeWorktreeLine(_ w: RepoTree.Worktree, level: Int, parent: HostWorkspace?, project: String) -> some View {
        let (quiet, open) = worktreeFold(w)
        let shown = !ownsTop && openedWorkspace != nil && openedWorkspace == w.workspace?.key
        let detail = treeDetail(w, parent: parent, project: project)
        Group {
            if let target = w.workspace {
                HStack(spacing: Theme.Space.xs) {
                    let line = RepoWorktreeLine(name: w.name, main: w.main, count: w.sessions.count, quiet: quiet, open: open, level: level,
                                                detail: detail?.text, caution: detail?.caution == true,
                                                offline: !target.available || !target.online, showChevron: false, pinned: target.pinnedAt != nil)
                    // The host's catalog worktrees keep the long-press menu their cards had; it reads the whole inventory,
                    // so the others go without it, as before.
                    Group {
                        if w.catalog {
                            WorkspaceSidebarControl(open: { openWorktree(w) }) { line } actions: { worktreeMenu(w) }
                        } else {
                            Button { openWorktree(w) } label: { line }.buttonStyle(.plain)
                        }
                    }
                    .accessibilityIdentifier("agents.worktree")
                    if !w.sessions.isEmpty || !w.children.isEmpty {
                        // A finger's square, its chevron at the end where the header's is (#3753).
                        Button { toggleWorktree(w) } label: {
                            FoldChevron(folded: !open)
                                .tapTargetFrame(alignment: .trailing)
                                .contentShape(.rect)
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel(String(localized: "workspace.toggle", defaultValue: "Expand or collapse worktree"))
                        .accessibilityIdentifier("agents.foldWorkspace")
                    }
                }
            } else {
                Button { toggleWorktree(w) } label: {
                    RepoWorktreeLine(name: w.name, main: w.main, count: w.sessions.count, quiet: quiet, open: open, level: level,
                                     detail: detail?.text, caution: detail?.caution == true)
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("agents.worktree")
            }
        }
        .modifier(FoldAnchor(id: w.id, reader: feed.reader))
        .modifier(ButtonRowInsets(edges: rowInsets(level: 0)))
        .listRowBackground(treeBackground(shown))
        .listRowSeparator(.hidden)
        // Its agents under its line at its level, as a tab's under the tab's row (#3753): the display's rows keep the
        // room they have in the other groupings.
        if open {
            if quiet {
                ForEach(lookup.projection?.rows[w.id]?.all ?? TabPanes.entries(ordered(w.sessions))) { e in entryRows(e, depth: level) }
            } else {
                agentRows(w.sessions, key: w.id, depth: level)
            }
        }
    }

    /// The worktrees nested under a worktree, behind one line as a worktree's: their count, their agents' state counts;
    /// folded until opened (remembered).
    private func treeChildrenLine(_ w: RepoTree.Worktree, level: Int) -> some View {
        let id = w.childrenFoldID
        let open = !isFolded(id)
        let below = w.children.flatMap(\.treeSessions)
        return Button { toggleFold(id) } label: {
            RepoChildrenLine(count: w.children.count, open: open,
                             group: SpaceGroup(id: id, machineID: below.first?.machineID ?? "", spaceID: "-", name: w.name, index: 0, sessions: below),
                             level: level)
        }
        .buttonStyle(.plain)
        .modifier(FoldAnchor(id: id, reader: feed.reader))
        .modifier(ButtonRowInsets(edges: rowInsets(level: 0)))
        .listRowBackground(treeBackground(false))
        .listRowSeparator(.hidden)
        .accessibilityIdentifier("agents.childSpaces")
    }
    #endif

    /// A repository: its agents right in its card when it has only its main checkout, else its working trees, each a
    /// row with its agents a level in under it.
    @ViewBuilder private func repoSection(_ r: RepoTree.Repo) -> some View {
        repoHeader(r, folded: isFolded(r.id))
            .selectionDisabled()
            .modifier(FoldAnchor(id: r.id, reader: feed.reader))
            .listRowInsets(EdgeInsets(top: Theme.Space.sm, leading: Theme.WorkspaceSidebar.cardLeading,
                                     bottom: Theme.Space.xs, trailing: Theme.Space.s))
            .listRowBackground(Theme.WorkspaceSidebar.background).listRowSeparator(.hidden)
        if !isFolded(r.id) {
            if r.flat {
                agentRows(r.worktrees[0].sessions, key: r.worktrees[0].id)
            } else {
                ForEach(r.worktrees) { w in
                    if w.catalog {
                        WorkspaceSidebarCard(workspace: w, project: r.name, selectedSession: openedWorkspace == nil ? shownAgent : nil,
                                             selectedWorkspace: openedWorkspace, folded: { isFolded($0) }, toggle: { toggleFold($0) },
                                             open: { openWorktree($0) },
                                             selectAgent: { openFromList($0) },
                                             appeared: { s in if paginationArmed { Task { await model.sessionLists.appeared(s) } } },
                                             agentIdentifier: compact ? "agents.compactRow" : "agents.row",
                                             subtasks: { lookup.projection?.tasks[$0.key]?.map(\.session) ?? [] },
                                             ordered: { ordered($0) }) { s in
                            slotRowContent(s, context: false, inStateSection: false)
                        } actions: { worktree, agent in worktreeMenu(worktree, agent: agent) }
                        .listRowInsets(EdgeInsets(top: 0, leading: Theme.Space.xs, bottom: Theme.Space.xs, trailing: Theme.Space.xs))
                        .listRowBackground(Color.clear).listRowSeparator(.hidden)
                        .selectionDisabled()
                    } else { worktreeRows(w) }
                }
            }
        }
    }

    /// A repository's header: its name and counts; when its agents are all in one herdr space (the usual case), that
    /// space's marks, todo and ⋯ (pin, mute, rename…) are the repository's.
    private func repoHeader(_ r: RepoTree.Repo, folded isFolded: Bool) -> some View {
        let prepared = lookup.projection?.repositories[r.id]
        let space = prepared?.space
        let unread = prepared?.unread ?? []
        let workspace = r.flattenedWorkspace
        return HStack(spacing: Theme.Space.xs) {
            Button { toggleFold(r.id) } label: {
                HStack(spacing: Theme.Space.s) {
                    ThemedIcon(workspace?.main == true ? Theme.Icon.home : Theme.Icon.folder).foregroundStyle(Theme.WorkspaceSidebar.muted)
                    Text(r.name).font(Theme.WorkspaceSidebar.titleUnread).lineLimit(1)
                        .foregroundStyle(workspace?.unavailable == true ? Theme.WorkspaceSidebar.muted : Theme.WorkspaceSidebar.foreground)
                    if workspace?.latestPinnedAt != nil {
                        ThemedIcon(Theme.Icon.pinned).font(Theme.WorkspaceSidebar.meta).foregroundStyle(Theme.accent).accessibilityHidden(true)
                    }
                    Spacer(minLength: 0)
                    FoldChevron(folded: isFolded)
                }.contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("agents.project")
            .accessibilityLabel(repositoryLabel(r, folded: isFolded))
            if let space {
                TodoSpaceTag(group: space) { todoID in openFromList(TodoDetailRoute(id: todoID)) }
            }
            if workspace != nil || space != nil || !unread.isEmpty {
                Menu {
                    if let workspace { worktreeMenu(workspace) }
                    if let space {
                        SpaceHeaderMenu(group: space, isMuted: model.isMuted, rename: { renamingSpace = space }, open: { openFromList(SpaceRoute(id: space.id)) },
                                        linkTodo: { linkingSpace = space }).equatable()
                    } else {
                        RepoMenuItems(unread: unread, all: prepared?.keys ?? []).equatable()
                    }
                } label: {
                    headerMenuLabel
                }
                .tint(Theme.WorkspaceSidebar.muted)
                .labelOnlyMenu()
                .accessibilityLabel("More actions for \(r.name) [Chats View 661d12]")
            }
        }
        .task(id: repoScope(r)) { await model.sessionLists.loadCounts(repoScope(r)) }
    }

    private func repositoryLabel(_ repo: RepoTree.Repo, folded: Bool) -> String {
        var labels = [repo.name]
        if let workspace = repo.flattenedWorkspace {
            if workspace.main { labels.append(String(localized: "Main worktree")) }
            if workspace.latestPinnedAt != nil { labels.append(String(localized: "Pinned [Personal Agent List View fb47db]")) }
            if workspace.unavailable { labels.append(String(localized: "This computer is offline")) }
        }
        labels.append(folded ? String(localized: "Collapsed [Readiness Card ee6046]") : String(localized: "Expanded"))
        return labels.joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label"))
    }

    /// A working tree in its repository's card: its row and agents (`worktreeRow`), then the spaces nested under it
    /// behind one「N 个子项」row.
    private struct WorkspaceRowEntry: Identifiable {
        var workspace: RepoTree.Worktree
        var depth: Int
        var toggle: Bool
        /// The worktree it is nested under, and the project name it inherits (the repository's, or its parent's).
        var parent: HostWorkspace? = nil
        var project = ""
        var id: String { workspace.id + (toggle ? "/children-toggle" : "") }
    }

    /// Flatten only visible branches for SwiftUI's List; the model retains every parent/child edge.
    private func workspaceRows(_ root: RepoTree.Worktree, project: String = "") -> [WorkspaceRowEntry] {
        var out: [WorkspaceRowEntry] = []
        func visit(_ w: RepoTree.Worktree, depth: Int, parent: HostWorkspace?, project: String) {
            out.append(WorkspaceRowEntry(workspace: w, depth: depth, toggle: false, parent: parent, project: project))
            guard worktreeFold(w).open, !w.children.isEmpty else { return }
            out.append(WorkspaceRowEntry(workspace: w, depth: depth + 1, toggle: true))
            if !isFolded(w.childrenFoldID) {
                let inherited = w.workspace?.projectName.flatMap { $0.isEmpty ? nil : $0 } ?? project
                for child in w.children { visit(child, depth: depth + 1, parent: w.workspace ?? parent, project: inherited) }
            }
        }
        visit(root, depth: 0, parent: nil, project: project)
        return out
    }

    @ViewBuilder private func worktreeRows(_ w: RepoTree.Worktree) -> some View {
        ForEach(workspaceRows(w)) { row in
            if row.toggle { childWorktreeRows(row.workspace, depth: row.depth) }
            else { worktreeRow(row.workspace, depth: row.depth) }
        }
    }

    /// Whether a working tree has nothing going on (its children included: it then folds as a quiet space does, not
    /// remembered) and whether it is open.
    private func worktreeFold(_ w: RepoTree.Worktree) -> (quiet: Bool, open: Bool) {
        let quiet = lookup.projection?.quietWorktrees[w.id] ?? (quick == .all && w.treeSessions.allSatisfy { !stays($0) })
        return (quiet, !query.isEmpty || (quiet ? expandedQuiet.contains(w.id) : !isFolded(w.id)))
    }

    /// A working tree's row (⌂ for the main checkout), then its agents a level in; `depth` 1: a nested space's, under
    /// its parent's「N 个子项」. One with nothing going on (its children included) starts folded, as a quiet space does;
    /// a busy one folds from its row, remembered.
    @ViewBuilder private func worktreeRow(_ w: RepoTree.Worktree, depth: Int) -> some View {
        let (quiet, open) = worktreeFold(w)
        Group {
            if w.workspace != nil {
                HStack(spacing: Theme.Space.s) {
                    Button { openWorktree(w) } label: {
                        WorktreeRow(name: w.name, main: w.main, count: w.sessions.count, quiet: false, open: open, showChevron: false)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("agents.worktree")
                    if !w.sessions.isEmpty || !w.children.isEmpty {
                        Button { toggleWorktree(w) } label: {
                            FoldChevron(folded: !open)
                                .scaledFrame(width: Theme.Size.wellXS, height: Theme.Size.wellXS)
                                .contentShape(.rect)
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel(String(localized: "workspace.toggle", defaultValue: "Expand or collapse worktree"))
                        .accessibilityIdentifier("agents.foldWorkspace")
                    }
                }
            } else {
                Button { toggleWorktree(w) } label: {
                    WorktreeRow(name: w.name, main: w.main, count: w.sessions.count, quiet: quiet, open: open)
                        .padding(.leading, nested(depth))
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("agents.worktree")
            }
        }
        .listRowInsets(WorkspaceList.rowInsets(level: depth))
        .listRowBackground(Color.clear)
        .listRowSeparator(.hidden)
        if open {
            if quiet {
                ForEach(lookup.projection?.rows[w.id]?.all ?? TabPanes.entries(ordered(w.sessions))) { e in
                    entryRows(e, depth: depth + 1)
                }
            } else {
                agentRows(w.sessions, key: w.id, depth: depth + 1)
            }
        }
    }

    private func toggleWorktree(_ w: RepoTree.Worktree) {
        let (quiet, open) = worktreeFold(w)
        if quiet {
            Haptics.tap()
            withAnimation(Theme.Motion.standard) {
                if open { expandedQuiet.remove(w.id) } else { expandedQuiet.insert(w.id) }
            }
        } else { toggleFold(w.id) }
    }

    // MARK: Recently active

    #if !os(iOS)
    private func workspaceAgentMenu(_ s: Session) -> AgentRowMenu {
        AgentRowMenu(key: s.key, pinned: model.isPinned(s.key), muted: model.isMuted(s), looping: s.loop != nil,
                     live: s.state.isLive, terminal: s.canTerminal, renamable: s.canRename, closable: s.canClose,
                     attachable: s.canAttach, deletable: s.canDelete,
                     paneActions: NodeMenu.offersPaneActions(s), actions: rowActions)
    }
    #endif


    @ViewBuilder private func worktreeMenu(_ worktree: RepoTree.Worktree, agent: Session? = nil) -> some View {
        if let target = worktree.workspace {
            let direct = ordered(WorkspaceNavigation.directSessions(target, inventory: model.sessionsNow, catalog: workspaceCatalog.workspaces))
            let parent = workspaceCatalog.workspaces.first { $0.key == target.parentKey }
            let open: (HostWorkspace) -> Void = { target in
                if target.key == worktree.workspace?.key { openWorktree(worktree) } else { openWorktree(target) }
            }
            #if os(iOS)
            WorktreeSidebarMenu(workspace: target, parent: parent, sessions: direct, agent: agent, handlers: rowActions,
                                open: open, newSession: { startingWorktree = $0 }, newChild: { creatingChild = $0 },
                                rename: { renamingWorktree = $0 }).equatable()
            #else
            WorktreeSidebarMenu(workspace: target, parent: parent, sessions: direct, agent: agent,
                                menus: direct.map(workspaceAgentMenu),
                                open: open, newSession: { startingWorktree = $0 }, newChild: { creatingChild = $0 },
                                rename: { renamingWorktree = $0 }).equatable()
            #endif
        }
    }

    @ViewBuilder private func recentSection(_ sessions: [Session]) -> some View {
        if !sessions.isEmpty {
            listSection {
                cappedRows(AgentListOrder.recent(sessions), key: "recent") { s in slotRow(s, context: true) }
            } header: {
                SectionHeader(title: String(localized: "Recently Used"), count: model.sessionLists.paged ? nil : sessions.count, tone: StatusPalette.idle)
            }
        }
    }

    /// The spaces nested under a working tree (Orca's child worktrees, #3075): one「N 个子项」row a level in, with the
    /// state counts of everything under it, folded until opened (remembered); open, each nested space as a working tree
    /// a level in.
    @ViewBuilder private func childWorktreeRows(_ w: RepoTree.Worktree, depth: Int) -> some View {
        let id = w.childrenFoldID
        let open = !isFolded(id)
        let below = w.children.flatMap(\.treeSessions)
        Button { toggleFold(id) } label: {
            ChildSpacesRow(count: w.children.count, open: open,
                           group: SpaceGroup(id: id, machineID: below.first?.machineID ?? "", spaceID: "-", name: w.name, index: 0, sessions: below), dense: true)
                .environment(\.compactList, true)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
        .buttonStyle(.plain)
        .modifier(FoldAnchor(id: id, reader: feed.reader))
        .listRowInsets(WorkspaceList.rowInsets(level: depth))
        .listRowBackground(Color.clear)
        .listRowSeparator(.hidden)
        .accessibilityIdentifier("agents.childSpaces")
    }

    // MARK: By state / by machine

    private var order: AgentListOrder {
        AgentListOrder(isPinned: model.isPinned, isMuted: model.isMuted, byRank: quick == .all && sort == .state, held: model.heldInList,
                       heldJustFinished: model.heldJustFinished, heldSeen: heldSeen)
    }
    /// The rows seen during this visit that keep their place (#12514): none when seen rows leave with their chat.
    private var heldSeen: [SessionKey: AgentListOrder.Held] { model.seenRows == .stay ? model.seenThisVisit : [:] }
    /// By Machine's machines: those with live agents, in their own order; under Recently Updated the one with the latest agent first.
    /// The list and ↑ / ↓ (`rowsInOrder`) both go by it.
    private func machineOrder(_ live: [Session]) -> [Machine] {
        let shown = machines.filter { m in live.contains { $0.machineRef == m.ref } }
        guard sort == .updated else { return shown }
        return AgentListOrder.latestFirst(shown, date: { m in
            live.filter { $0.machineRef == m.ref }.map(\.sortDate).max() ?? .distantPast
        }, id: \.ref.path)
    }

    private func latestFirst(_ groups: [SpaceGroup]) -> [SpaceGroup] {
        AgentListOrder.latestFirst(groups, date: \.sortDate, id: \.id)
    }
    private func stays(_ s: Session) -> Bool { order.stays(s) }
    private func rank(_ s: Session) -> Int { order.rank(s) }
    private func ordered(_ list: [Session]) -> [Session] { order.ordered(list) }

    private static let stateTitles: [(key: String, title: String, tint: StatusTone)] = [
        ("needs", StateName.needsYou.text, StatusPalette.needsYou), ("unread", StateName.unread.text, StatusPalette.unread),
        ("awaiting", StateName.awaitsReply.text, StatusPalette.awaitingReply),
        ("working", StateName.working.text, StatusPalette.working),
        ("background", String(localized: "Waiting in the background"), StatusPalette.backgroundWaiting),
        ("just_finished", StateName.justFinished.text, StatusPalette.unread),
        ("waiting", StateName.waiting.text, StatusTone(dot: Theme.loop, text: Theme.loop)),
        ("muted", String(localized: "Muted [Chats View b66705]"), StatusPalette.idle), ("idle", StateName.seen.text, StatusPalette.idle),
    ]

    /// The fold ids of the sections By Status / By Machine shows now (Collapse All).
    private func sectionIDs(_ sessions: [Session]) -> [String] { order.sectionIDs(sessions, byMachine: grouping == .machine) }

    private func stateSectionCount(_ key: String, loaded: Int) -> Int {
        let filters: [String: AgentFilter] = ["needs": .needsYou, "unread": .unread, "working": .working]
        guard model.sessionLists.paged, let filter = filters[key] else { return loaded }
        return model.sessionLists.stateCounts?[filter] ?? 0
    }

    /// A section per rank (`rank`); Seen starts folded, it is the long tail, and its header is its one fold: no
    /// "N more idle" row inside it as well (2026-10-31: it took two taps where the other states take one). Waiting: nothing new
    /// yet, but a /loop or background work will bring some.
    @ViewBuilder private func stateSections(_ live: [Session]) -> some View {
        let order = self.order
        let byRank = Dictionary(grouping: live, by: order.rank)
        let buckets = Self.stateTitles.enumerated().map { i, t in (key: t.key, title: t.title, rows: byRank[i] ?? [], tint: t.tint) }
        // Rows read during this visit and kept in place, drawn as Seen (#12514): not counted, a section counts what is still
        // in its state, and its Clear Seen lets them go, all but the one whose chat is open (it goes when it is left). With
        // seen rows leaving with their chat, the open one counts as it always did.
        let stay = model.seenRows == .stay
        let open = model.heldInList
        ForEach(buckets, id: \.key) { b in
            let rows = order.ordered(b.rows)
            let id = "state/" + b.key
            let seen = stay ? rows.filter { order.isHeldSeen($0) && $0.key != open }.map(\.key) : []
            // As the quick filters count: not a subtask that is a row only because its parent is not drawn.
            let counted = rows.filter { !$0.isNestedSubtask && !(stay && order.isHeldSeen($0)) }.count
            if !rows.isEmpty {
                listSection(foldable: true) {
                    if isFolded(id) {
                    } else if b.key == "idle" {
                        cappedRows(rows, key: id) { s in slotRow(s, context: true, inStateSection: true) }
                    } else {
                        // Muted mixes states: each row says its own. Loops and background work stay off the rows (the chat shows them).
                        cappedRows(rows, key: id) { s in slotRow(s, context: true, inStateSection: b.key != "muted") }
                    }
                } header: {
                    stateSectionHeader(key: b.key, id: id, title: b.title, count: stateSectionCount(b.key, loaded: counted),
                                       tint: b.tint, rows: rows, seen: seen)
                }
                .listSectionSpacing(isFolded(id) ? .custom(0) : .default)
            }
        }
    }

    /// A By Status section's folding header; only Just finished attaches a context menu so the other headings keep plain
    /// button hit-testing without an empty menu wrapper.
    @ViewBuilder private func stateSectionHeader(key: String, id: String, title: String, count: Int,
                                                 tint: StatusTone, rows: [Session], seen: [SessionKey]) -> some View {
        let topPadding = floating ? Theme.Space.l : Theme.Space.s
        let bottomPadding = floating ? Theme.Space.xs : Theme.Space.s
        let header = Group {
            if seen.isEmpty {
                FoldButton(title: title, folded: isFolded(id),
                           detail: String(localized: "\(count) [Chats View cb60bd]"),
                           topPadding: topPadding, bottomPadding: bottomPadding) { toggleFold(id) } label: {
                    SectionHeader(title: title, count: count, tone: tint,
                                  symbol: key == "background" ? Theme.Icon.backgroundWaiting : nil)
                }
                .accessibilityIdentifier("agents.section")
            } else {
                FoldButton(title: title, folded: isFolded(id),
                           detail: String(localized: "\(count) [Chats View cb60bd]"),
                           identifier: "agents.section",
                           topPadding: topPadding, bottomPadding: bottomPadding,
                           action: { toggleFold(id) }) {
                    SectionHeader(title: title, count: count, tone: tint,
                                  symbol: key == "background" ? Theme.Icon.backgroundWaiting : nil)
                } accessory: {
                    clearSeenButton(seen)
                }
            }
        }
        .modifier(FoldAnchor(id: id, reader: feed.reader))
        if key == "just_finished" {
            header.contextMenu {
                if rows.contains(where: { $0.needsSeen() }) {
                    Button { model.markSeen(rows.map(\.key)) } label: {
                        Label(StateName.markAllSeen(), systemImage: Theme.Icon.success)
                    }
                }
            }
        } else {
            header
        }
    }

    /// A section's Clear Seen (N) (#12514): the rows read during this visit and kept there leave it, as a pull to refresh
    /// lets every one of them go; under a quick filter they leave the list, as its Mark All as Seen's do.
    private func clearSeenButton(_ keys: [SessionKey]) -> some View {
        Button {
            Haptics.tap()
            withAnimation(Theme.Motion.standard) {
                model.releaseSeen(keys)
                kept.subtract(keys)
            }
        } label: {
            Text(SeenRows.clearTitle(keys.count))
                .font(Theme.Font.secondary)
                .foregroundStyle(Theme.accent)
                .textCase(nil)
                .lineLimit(1)
                .fixedSize()
                .tapTargetHeight(alignment: .bottom)
                .contentShape(.rect)
        }
        .buttonStyle(.borderless)
        .accessibilityHint(SeenRows.clearHint)
        .accessibilityIdentifier("agents.clearSeen")
    }

    /// One section per todo, its agents ordered; agents opened by hand in the terminal go into "Started Manually", the other
    /// unassigned ones into "Unassigned": each folded by default, open while one of them needs you or nothing is above it
    /// (`TodoAgentGroup.folded`), and listed while empty when nothing narrows the list, to say what it would hold (#6458).
    @ViewBuilder private func todoSections(_ live: [Session]) -> some View {
        let keepEmpty = query.isEmpty && quick == .all && !live.isEmpty
        let todoGroups = TodosLogic.buildTodoAgentGroups(sessions: live, todos: TodoStore.shared.todos, isMuted: model.isMuted, keepEmpty: keepEmpty)
        todoGroupSections(todoGroups, mixedHosts: Set(live.compactMap(\.hostKind)).count > 1)
    }

    /// By To-do's sections for `todoGroups` (also By Pinned's last ones, for the Agents its tree does not have).
    @ViewBuilder private func todoGroupSections(_ todoGroups: [TodoAgentGroup], mixedHosts: Bool) -> some View {
        ForEach(todoGroups) { g in
            let id = g.key
            let folded = g.todoID == nil ? isFolded(id, otherwise: g.folded) : isFolded(id)
            if !g.sessions.isEmpty || g.hint != nil {
                listSection(foldable: true) {
                    if !folded {
                        if g.sessions.isEmpty, let hint = g.hint {
                            groupEmptyRow(hint)
                        } else {
                            cappedRows(g.sessions, key: id) { s in slotRow(s, context: true, showHost: mixedHosts) }
                        }
                    }
                } header: {
                    // A group says what it holds with its count, for VoiceOver too: its header is one element (#6458).
                    FoldButton(title: g.name, folded: folded, detail: [String(localized: "\(g.sessions.count) [Chats View cb60bd]"), g.hint ?? ""].filter { !$0.isEmpty }
                        .joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label")),
                               topPadding: floating ? Theme.Space.l : Theme.Space.s,
                               bottomPadding: floating ? Theme.Space.xs : Theme.Space.s) { toggleFold(id, shown: folded) } label: {
                        HStack(spacing: Theme.Space.sm) {
                            if compact {
                                Text(g.name).font(CompactList.sectionTitle).foregroundStyle(Color.primary).textCase(nil).lineLimit(1)
                            } else {
                                Text(g.name)
                            }
                            if let hint = g.hint { InfoTip(name: g.name, text: hint).textCase(nil) }
                            if let st = g.todoStatus {
                                CountBadge(count: g.sessions.count, look: .tone(st.todoTone))
                            } else {
                                // the most urgent of its agents' states, the rule a to-do's count follows (#6458)
                                CountBadge(count: g.sessions.count, look: .tone(g.mostUrgent?.tone ?? StatusPalette.idle))
                            }
                            if let todoID = g.todoID,
                                g.sessions.allSatisfy({ TodosLogic.isOnTodosHub(sessionGroup: $0.key.group, hubGroup: model.hub?.group) }) {
                                Button {
                                    openFromList(TodoDetailRoute(id: todoID))
                                } label: {
                                    ThemedIcon(Theme.Icon.disclosure)
                                        .font(compact ? CompactList.metaStrong : Theme.Font.body)
                                        .foregroundStyle(.secondary)
                                        .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)
                                        .contentShape(.rect)
                                }
                                .buttonStyle(.plain)
                                .accessibilityLabel("View to-do details")
                            }
                        }
                    }
                    .accessibilityIdentifier("agents.section")
                }
                .listSectionSpacing(folded ? .custom(0) : .default)
            }
        }
    }

    /// An open group with no agents (#6458): what it would hold, in its place.
    private func groupEmptyRow(_ hint: String) -> some View {
        Text(hint)
            .font(compact ? CompactList.meta : Theme.Font.caption)
            .foregroundStyle(.secondary)
            .fixedSize(horizontal: false, vertical: true)
            .listRowInsets(rowInsets(level: 0))
            .listRowBackground(floating ? Color.clear : nil)
            .listRowSeparator(.hidden)
            .accessibilityIdentifier("agents.groupEmpty")
    }

    /// By Pinned (#4352, `DirectionAgentSections`) from the hub's tree; nil without one.
    private func directionLayout(_ live: [Session]) -> DirectionAgentSections? {
        guard let tree = model.directionTree else { return nil }
        return DirectionAgentSections.build(tree, shown: live, known: Set(model.sessionsNow.map(\.key)),
                                            group: model.hub?.group ?? "", filtering: quick != .all || !query.isEmpty,
                                            isMuted: model.isMuted)
    }

    /// By Pinned: a section per direction of the hub's tree, headed by its name and 「在做 N · 等你 N」, folding as a
    /// to-do's section does; in it each to-do's row (its state; a tap opens its card), its Agents a level in under it
    /// and their sub-Agents further in. Then By To-do's Started Manually and Unassigned for the Agents the tree does not
    /// have. Without a tree (it is being read), By To-do.
    @ViewBuilder private func directionSections(_ live: [Session]) -> some View {
        if let layout = directionLayout(live) {
            let mixedHosts = Set(live.compactMap(\.hostKind)).count > 1
            ForEach(layout.entries) { e in
                let folded = isFolded(e.key)
                let title = model.directionTitle(e.direction)
                listSection(foldable: true) {
                    if !folded {
                        ForEach(e.tasks) { t in
                            directionTaskRow(t.task)
                            ForEach(t.lines) { l in
                                if let s = l.session {
                                    slotRow(s, context: true, showHost: mixedHosts, depth: l.depth + 1)
                                } else {
                                    directionAgentLine(l)
                                }
                            }
                        }
                    }
                } header: {
                    FoldButton(title: title, folded: folded, detail: e.direction.counts.label ?? "",
                               topPadding: floating ? Theme.Space.l : Theme.Space.s,
                               bottomPadding: floating ? Theme.Space.xs : Theme.Space.s) { toggleFold(e.key) } label: {
                        HStack(spacing: Theme.Space.sm) {
                            if compact {
                                Text(title).font(CompactList.sectionTitle).foregroundStyle(Color.primary).textCase(nil).lineLimit(1)
                            } else {
                                Text(title).lineLimit(1)
                            }
                            DirectionCountsLine(direction: e.direction).textCase(nil)
                        }
                    }
                }
                .listSectionSpacing(folded ? .custom(0) : .default)
            }
            todoGroupSections(layout.others, mixedHosts: mixedHosts)
        } else {
            todoSections(live)
        }
    }

    /// A to-do under a direction: its title and its state in words; a tap opens its card.
    private func directionTaskRow(_ task: DirectionTree.TaskNode) -> some View {
        Button { openFromList(TodoDetailRoute(id: task.id)) } label: {
            HStack(spacing: compact ? CompactList.gap : Theme.Space.s) {
                ThemedIcon(Theme.Icon.todo)
                    .font(compact ? CompactList.metaStrong : Theme.Font.secondaryMedium)
                    .foregroundStyle(.secondary)
                    .scaledFrame(width: compact ? CompactList.mark : Theme.Size.iconS)
                Text(task.displayTitle).font(compact ? CompactList.title : Theme.Font.body).foregroundStyle(Color.primary).lineLimit(1)
                Spacer(minLength: Theme.Space.xs)
                DirectionTaskStateWord(state: task.state)
                ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.captionStrong).foregroundStyle(.tertiary)
            }
            .scaledFrame(minHeight: Theme.Size.tapTarget)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([task.displayTitle, task.state.text]
            .joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label")))
        .accessibilityHint(String(localized: "View to-do details"))
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("agents.direction.task")
        .listRowInsets(rowInsets(level: 0))
        .listRowBackground(floating ? Color.clear : nil)
        .listRowSeparator(floating ? .hidden : .automatic)
    }

    /// An Agent the tree has and the list does not (a sub-Agent inside a session): its state from the tree, its title; a
    /// tap opens it.
    private func directionAgentLine(_ l: DirectionAgentSections.Line) -> some View {
        let state = l.node.agentState
        let title = l.node.title.isEmpty ? (l.node.agent.isEmpty ? String(localized: "Agent") : l.node.agent) : l.node.title
        return Button { openFromList(l.key) } label: {
            HStack(spacing: compact ? CompactList.gap : Theme.Space.s) {
                StatusDot(state: state)
                Text(title).font(compact ? CompactList.title : Theme.Font.secondary).foregroundStyle(Color.primary).lineLimit(1)
                Spacer(minLength: Theme.Space.xs)
                // Not the dot's color alone (HIG-040): every state says itself in words too.
                Text(state.label).font(Theme.Font.caption).foregroundStyle(state.tone.text).lineLimit(1)
            }
            .padding(.leading, nested(l.depth + 1))
            .scaledFrame(minHeight: Theme.Size.tapTarget)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([title, state.label]
            .joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label")))
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("agents.direction.agent")
        .listRowInsets(rowInsets(level: l.depth + 1))
        .listRowBackground(floating ? Color.clear : nil)
        .listRowSeparator(floating ? .hidden : .automatic)
    }

    /// An app section's spoken counts: the state counts, then the offline agents, which they leave out.
    static func appDetail(_ group: SpaceGroup) -> String {
        let offline = group.sessions.filter { $0.state == .offline }.count
        return ([StateCounts.spoken(group)].filter { !$0.isEmpty } + (offline > 0 ? [String(localized: "\(offline) \(AgentState.offline.label)")] : []))
            .joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label"))
    }

    /// By App (#3756): a section per app (`AppGroups`) with its running and offline agents by project, the latest first; each folds
    /// as a machine's section does, its state counts on its title.
    @ViewBuilder private func appSections(_ sessions: [Session]) -> some View {
        ForEach(lookup.projection?.apps ?? AppGroups.build(sessions)) { app in
            let machines = Set(app.projects.compactMap(\.machine))
            let group = SpaceGroup(id: app.id, machineID: "", spaceID: "-", name: app.title, index: 0, sessions: app.sessions)
            listSection(foldable: true) {
                if !isFolded(app.id) {
                    if app.showsProjects {
                        // As the desktop apps' sidebars: a project, then its sessions a level in (`AppGroups.projects`).
                        ForEach(app.projects) { project in
                            appProjectRow(project, machine: machines.count > 1 ? project.machine.flatMap { machine($0)?.title ?? $0.id } : nil)
                            if !isFolded(project.id) {
                                // Other's agents keep where they run on their rows: they share no folder.
                                cappedRows(project.sessions, key: project.id, depth: 1, cap: AgentRowCap.projectRows) { s in
                                    slotRow(s, context: project.isOther, depth: 1)
                                }
                            }
                        }
                    } else {
                        cappedRows(app.sessions, key: app.id) { s in slotRow(s, context: true) }
                    }
                }
            } header: {
                FoldButton(title: app.title, folded: isFolded(app.id), detail: Self.appDetail(group),
                           topPadding: floating ? Theme.Space.l : Theme.Space.s,
                           bottomPadding: floating ? Theme.Space.xs : Theme.Space.s) { toggleFold(app.id) } label: {
                    AppSectionHeader(app: app, counts: group)
                }
                .accessibilityIdentifier("agents.section")
            }
            .listSectionSpacing(isFolded(app.id) ? .custom(0) : .default)
        }
    }

    /// A project's heading in a By App section (`AppProjectRow`): tap to fold its sessions.
    private func appProjectRow(_ project: AppProject, machine: String?) -> some View {
        Button { toggleFold(project.id) } label: {
            AppProjectRow(project: project, folded: isFolded(project.id), machine: machine)
        }
        .buttonStyle(.plain)
        .modifier(FoldAnchor(id: project.id, reader: feed.reader)) // stays put as it folds, as a tab's row
        .listRowInsets(compact ? rowInsets(level: 0) : floating ? Self.tabCapsuleInsets(level: 0) : nil)
        .listRowBackground(floating ? AnyView(Color.clear) : nil)
        .listRowSeparator(floating ? .hidden : .automatic)
        .accessibilityIdentifier("agents.appProject")
    }

    /// One section per machine, its agents by state; idle ones fold like in a space.
    @ViewBuilder private func machineSections(_ live: [Session]) -> some View {
        ForEach(machineOrder(live), id: \.ref) { m in
            let rows = ordered(live.filter { $0.machineRef == m.ref })
            let id = "machine/" + m.ref.path
            let group = SpaceGroup(id: "m-" + m.ref.path, machineID: m.id, spaceID: "-", group: m.group, name: m.title, index: 0, sessions: rows,
                                   aggregate: model.sessionLists.aggregate(machineScope(m)), bounded: model.sessionLists.isPaged(m.group))
            listSection(foldable: true) {
                if !isFolded(id) {
                    if quick == .all {
                        cappedRows(rows.filter(stays), key: id) { s in slotRow(s, context: true) }
                        foldedRows(rows.filter { !stays($0) }, key: "machine-" + m.ref.path, label: String(localized: "Idle"))
                    } else {
                        cappedRows(rows, key: id) { s in slotRow(s, context: true) }
                    }
                }
            } header: {
                FoldButton(title: m.title, folded: isFolded(id), detail: StateCounts.spoken(group),
                           topPadding: floating ? Theme.Space.l : Theme.Space.s,
                           bottomPadding: floating ? Theme.Space.xs : Theme.Space.s) { toggleFold(id) } label: {
                    MachineSectionHeader(machine: m, group: group)
                }
                .accessibilityIdentifier("agents.section")
                .task(id: machineScope(m)) { await model.sessionLists.loadCounts(machineScope(m)) }
            }
            .listSectionSpacing(isFolded(id) ? .custom(0) : .default)
        }
    }

    /// A long run of rows: the first `rowCap`, then "Show All N" (open until the list's view is rebuilt).
    @ViewBuilder private func cappedRows<Item: Identifiable, Row: View>(_ items: [Item], key: String, depth: Int = 0, cap: Int = AgentRowCap.rows,
                                                                       @ViewBuilder row: @escaping (Item) -> Row) -> some View {
        let key = "all:" + key
        // Under a quick filter or a search every row it leaves shows: what was asked for, and already few.
        if let cap = AgentRowCap.capped(items.count, cap: cap), query.isEmpty, quick == .all, !model.sessionLists.paged {
            if expandedIdle.contains(key) {
                ForEach(items) { row($0) }
                collapseRow(String(localized: "Collapse"), key: key, depth: depth)
            } else {
                ForEach(items.prefix(cap)) { row($0) }
                showAllRow(items.count, key: key, depth: depth)
            }
        } else {
            ForEach(items) { row($0) }
        }
    }

    private func showAllRow(_ count: Int, key: String, depth: Int) -> some View {
        Button {
            Haptics.tap()
            withAnimation(Theme.Motion.standard) { _ = expandedIdle.insert(key) }
        } label: {
            HStack(spacing: compact ? CompactList.gap : Theme.Space.s) {
                ThemedIcon(Theme.Icon.expand)
                    .font(compact ? CompactList.metaStrong : Theme.Font.secondaryMedium)
                    .scaledFrame(width: compact ? CompactList.mark : Theme.Size.iconS)
                Text("Show All \(count)").font(compact ? CompactList.title : Theme.Font.secondaryMedium).lineLimit(1)
                Spacer(minLength: 0)
            }
            .foregroundStyle(Theme.accent)
            .contentShape(.rect)
            .padding(.leading, nested(depth))
            .modifier(TreeIndent(level: repoTree ? depth : 0))
            .modifier(ButtonRowLabel(edges: rowInsets(level: depth)))
        }
        .buttonStyle(.plain)
        .modifier(ButtonRowInsets(edges: rowInsets(level: depth)))
        .listRowBackground(repoTree ? treeBackground(false) : floating ? AnyView(Color.clear) : nil)
        .listRowSeparator(floating || repoTree ? .hidden : .automatic)
        .accessibilityIdentifier("agents.showAll")
    }

    /// Quiet rows behind one "N more…" row (always open while searching).
    @ViewBuilder private func foldedRows(_ rows: [Session], key: String, label: String, inStateSection: Bool = false) -> some View {
        if !rows.isEmpty {
            if expandedIdle.contains(key) || !query.isEmpty {
                ForEach(rows) { s in slotRow(s, context: true, inStateSection: inStateSection) }
                if query.isEmpty { collapseRow(String(localized: "Hide \(rows.count) \(label)"), key: key) }
            } else {
                expandRow(rows, key: key)
            }
        }
    }

    /// "N more idle": the idle agents behind one row.
    private func expandRow(_ rows: [Session], key: String, depth: Int = 0) -> some View {
        Button {
            Haptics.tap()
            withAnimation(Theme.Motion.standard) { _ = expandedIdle.insert(key) }
        } label: {
            if repoTree {
                treeIdleLine(rows, level: depth)
            } else {
                Group { if compact { CompactFoldRow(sessions: rows) } else { IdleFoldRow(sessions: rows) } }
                    .padding(.leading, nested(depth))
                    .modifier(ButtonRowLabel(edges: rowInsets(level: depth)))
            }
        }
        .buttonStyle(.plain)
        .modifier(ButtonRowInsets(edges: rowInsets(level: depth)))
        .listRowBackground(repoTree ? treeBackground(false) : floating ? AnyView(Color.clear) : nil)
        .listRowSeparator(floating || repoTree ? .hidden : .automatic)
        .accessibilityIdentifier("agents.idleFold")
    }

    /// By Repository's tree (#2531): the idle agents' quiet line, or (`title`) the one that folds them again.
    @ViewBuilder private func treeIdleLine(_ rows: [Session], level: Int, title: String? = nil) -> some View {
        #if os(iOS)
        RepoIdleLine(sessions: rows, level: level, title: title)
        #endif
    }

    private func collapseRow(_ title: String, key: String, depth: Int = 0) -> some View {
        Button {
            withAnimation(Theme.Motion.standard) { _ = expandedIdle.remove(key) }
        } label: {
            Group {
                if repoTree {
                    treeIdleLine([], level: depth, title: title)
                } else if compact {
                    CompactCollapseRow(title: title)
                } else {
                    Label(title, systemImage: Theme.Icon.collapse).font(Theme.Font.secondaryMedium).lineLimit(1)
                }
            }
            .padding(.leading, nested(depth))
        }
        // A button in the list's own style is its whole row; the tree's line has its own room (`RepoTreeLine`).
        .modifier(ButtonRowInsets(edges: rowInsets(level: depth), on: repoTree))
        .listRowBackground(repoTree ? treeBackground(false) : floating ? AnyView(Color.clear) : nil)
        .listRowSeparator(floating || repoTree ? .hidden : .automatic)
        .accessibilityIdentifier("agents.collapse")
    }

    /// A row's edges in Compact List and the capsules, `level` levels in: under a working tree, under a tab's starter, or both.
    /// Nil: the grouped list's own, the row's content moved in instead (`nested`). By Repository on iOS: the edges its
    /// display gives a row at the top level; its rows move themselves in by the tree's levels (`TreeIndent`, #3753).
    private func rowInsets(level: Int) -> EdgeInsets? {
        if grouping == .repo && !repoTree { return WorkspaceList.rowInsets(level: level) }
        let level = repoTree ? 0 : level
        if compact { return CompactList.rowInsets(level: level) }
        if floating { return Self.capsuleInsets(level: level) }
        return nil
    }

    /// How far a row's content moves in under a working tree in the grouped list (its row keeps the list's insets, so
    /// its line starts where its text does).
    private func nested(_ level: Int) -> CGFloat { grouping != .repo && !compact && !floating ? Theme.Space.l * CGFloat(level) : 0 }

    /// Compact List: a quiet space as one row like an agent's, its agents under it when opened (a DisclosureGroup's own row
    /// is taller and its arrow heavier than everything else in the list).
    @ViewBuilder private func compactQuietSpace(_ g: SpaceGroup, namesMachine: Bool, repo: Bool, selected: Bool) -> some View {
        let open = expandedQuiet.contains(g.id) || !query.isEmpty
        Button {
            withAnimation(Theme.Motion.standard) {
                if open { expandedQuiet.remove(g.id) } else { expandedQuiet.insert(g.id) }
            }
        } label: {
            CompactQuietSpaceRow(group: g, machine: machine(g.machineRef), showMachine: namesMachine, expanded: open, repo: repo)
                .modifier(ButtonRowLabel(edges: CompactList.rowInsets))
        }
        .buttonStyle(.plain)
        .modifier(ButtonRowInsets(edges: CompactList.rowInsets))
        .listRowBackground(selected ? treeBackground(true) : nil)
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityIdentifier("agents.quietSpace")
        if open {
            let roles = TabPanes.roles(g.sessions)
            ForEach(TabPanes.grouped(ordered(g.sessions))) { s in slotRow(s, pane: roles[s.key]) }
        }
    }

    /// The rows' style (#2530), on iOS: the Mac and Apple Vision Pro keep their rows.
    private var listRowStyle: AgentRowStyle? {
        #if os(iOS)
        rowStyle
        #else
        nil
        #endif
    }

    /// By Repository on iOS (#2531): a card per repository with its worktrees' lines, the agents in the display's rows
    /// (#3753); the Mac and Apple Vision Pro keep their sidebar cards.
    private var repoTree: Bool {
        #if os(iOS)
        grouping == .repo
        #else
        false
        #endif
    }

    /// BeautifulUI's capsules: the agents as cards of their own on the page (no list card, line or system chevron), in
    /// that style's own components; not Compact List.
    private var capsules: Bool { !compact && AppStyle.current.usesBeautifulUI }

    /// The rows float on the page, each its own card: the capsules, or the State Cards row style (#2530), which takes
    /// their list (plain, on the page's color, every row on the page's edges, a section's title its first row) and
    /// draws the rest in its own (`floatingCards`).
    private var floating: Bool { capsules || (!compact && listRowStyle == .cards) }
    /// State Cards outside the BeautifulUI style: the floating list without BeautifulUI's components.
    private var floatingCards: Bool { floating && !capsules }

    /// A folded space's header, a plain row, where an open section's header sits: 20 is the system's inset for an
    /// inset-grouped section header on iOS (matching it, not a design value).
    private static let foldedHeaderInsets: EdgeInsets? = EdgeInsets(top: 0, leading: 20, bottom: 0, trailing: 20)

    #if os(iOS)
    private static let hostHeadingInsets = EdgeInsets(top: Theme.Space.flush, leading: Theme.Space.page, bottom: Theme.Space.xs, trailing: Theme.Space.page)
    #else
    private static let hostHeadingInsets = EdgeInsets(top: Theme.Space.l, leading: Theme.Space.page, bottom: Theme.Space.xs, trailing: Theme.Space.page)
    #endif

    /// How far the Sidebar layout's selected shade reaches past an Agent row's content, into the row's insets (#11032).
    private static let selectedOutset = EdgeInsets(top: -Theme.Space.xs, leading: -Theme.Space.s,
                                                   bottom: -Theme.Space.xs, trailing: -Theme.Space.s)
    /// A capsule's room: the page's edges, a little air above and below.
    private static let capsuleInsets = EdgeInsets(top: Theme.Space.xs, leading: Theme.Space.page, bottom: Theme.Space.xs, trailing: Theme.Space.page)
    /// A capsule `level` levels in, under a working tree's row or a tab's starter: a level is as far in as a quiet space's
    /// opened card sets its agents.
    private static func capsuleInsets(level: Int) -> EdgeInsets {
        EdgeInsets(top: Theme.Space.xs, leading: Theme.Space.page + Theme.Space.l * CGFloat(level), bottom: Theme.Space.xs, trailing: Theme.Space.page)
    }
    /// A tab's or a working tree's row among the capsules: a line of text on the page over its agents' capsules.
    private static func tabCapsuleInsets(level: Int) -> EdgeInsets {
        EdgeInsets(top: Theme.Space.s, leading: Theme.Space.page + Theme.Space.l * CGFloat(level), bottom: Theme.Space.xxs, trailing: Theme.Space.page)
    }
    /// A section. The capsules are a plain list (every row sets its own edges, all on `Theme.Space.page`): its title is
    /// its first row on the page's edge (a plain list's headers stick to the top, and a grouped list pads its first
    /// section about 35 pt). Elsewhere the grouped list's header. When `foldable`, the header's button owns the vertical
    /// room so a tap near either edge hits it (#3753, #13575).
    @ViewBuilder private func listSection<Content: View, Header: View>(foldable: Bool = false,
                                                                      @ViewBuilder content: () -> Content,
                                                                      @ViewBuilder header: () -> Header) -> some View {
        if floating {
            Section {
                header()
                    .font(Theme.Font.sectionHeader)
                    .foregroundStyle(.secondary)
                    .textCase(nil)
                    .listRowInsets(EdgeInsets(top: foldable ? Theme.Space.flush : Theme.Space.l, leading: Theme.Space.page,
                                              bottom: foldable ? Theme.Space.flush : Theme.Space.xs, trailing: Theme.Space.page))
                    .listRowBackground(Color.clear)
                    .listRowSeparator(.hidden)
                content()
            }
        } else {
            Section { content() } header: {
                if foldable {
                    header()
                        .modifier(ButtonRowInsets(edges: Self.foldedHeaderInsets))
                } else {
                    header()
                }
            }
        }
    }

    /// The capsules: a quiet space is one card, its title on top and, opened, its agents under it, set in a level.
    @ViewBuilder private func quietSpaceCard(_ g: SpaceGroup, namesMachine: Bool, repo: Bool, selected: Bool) -> some View {
        let open = expandedQuiet.contains(g.id) || !query.isEmpty
        Button {
            withAnimation(Theme.Motion.standard) {
                if open { expandedQuiet.remove(g.id) } else { expandedQuiet.insert(g.id) }
            }
        } label: {
            QuietSpaceCardHead(group: g, machine: machine(g.machineRef), showMachine: namesMachine, open: open, repo: repo, selected: selected)
        }
        .buttonStyle(.plain)
        .listRowInsets(EdgeInsets(top: Theme.Space.xs, leading: Theme.Space.page, bottom: open ? 0 : Theme.Space.xs, trailing: Theme.Space.page))
        .listRowBackground(Color.clear)
        .listRowSeparator(.hidden)
        .accessibilityIdentifier("agents.quietSpace")
        if open {
            // A card is one surface: a tab's agents sit together in it, named and marked, without a row of the tab's own.
            let rows = TabPanes.grouped(ordered(g.sessions)), roles = TabPanes.roles(g.sessions)
            ForEach(rows) { s in slotRow(s, cardLast: s.id == rows.last?.id, pane: roles[s.key]) }
        }
    }

    /// State Cards (#2530): a quiet space is a card of its own as its agents are, its title and the chevron that turns as
    /// it opens; opened, its agents' cards under it, a level in.
    @ViewBuilder private func quietSpaceStateCard(_ g: SpaceGroup, namesMachine: Bool, repo: Bool, selected: Bool) -> some View {
        let open = expandedQuiet.contains(g.id) || !query.isEmpty
        Button {
            withAnimation(Theme.Motion.standard) {
                if open { expandedQuiet.remove(g.id) } else { expandedQuiet.insert(g.id) }
            }
        } label: {
            HStack(spacing: Theme.Space.s) {
                QuietSpaceRow(group: g, machine: machine(g.machineRef), showMachine: namesMachine, repo: repo)
                FoldChevron(folded: !open)
            }
            .card(padding: Theme.Space.m)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .listRowInsets(Self.capsuleInsets)
        .listRowBackground(Color.clear)
        .listRowSeparator(.hidden)
        .accessibilityValue(open ? String(localized: "Expanded") : String(localized: "Collapsed [Readiness Card ee6046]"))
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityIdentifier("agents.quietSpace")
        if open {
            let roles = TabPanes.roles(g.sessions)
            ForEach(TabPanes.grouped(ordered(g.sessions))) { s in slotRow(s, depth: 1, pane: roles[s.key]) }
        }
    }

    /// `context`: also name the space (and machine) — when the list is not grouped by space.
    /// `cardLast`: a row of a quiet space's card (`quietSpaceCard`), and whether it ends the card.
    /// `depth`: levels in, under a working tree's row (By Repository).
    /// `pane`, `branch`: an agent of a tab with several, and one hanging off its starter (a level further in).
    /// An agent's row, and its Subtasks under it (`CodexTasks`: a Codex chat's subagents, the agents a runtime's agent
    /// created), as the Codex App and Android show them: the list's rows and counts are the others'
    /// (`AgentListProjection.tasks`). In a quiet space's card the subtasks end the card, not the agent.
    @ViewBuilder private func slotRow(_ s: Session, context: Bool = false, inStateSection: Bool = false, cardLast: Bool? = nil,
                                      showHost: Bool = false, depth: Int = 0, pane: TabPanes.Role? = nil, branch: Bool = false) -> some View {
        let tasks = lookup.projection?.tasks[s.key] ?? []
        agentRow(s, context: context, inStateSection: inStateSection, cardLast: tasks.isEmpty ? cardLast : cardLast.map { _ in false },
                 showHost: showHost, depth: depth, pane: pane, branch: branch)
        if !tasks.isEmpty {
            subtaskRows(s, tasks, depth: depth + (branch ? 1 : 0) + 1, cardLast: cardLast)
        }
    }

    /// The Subtasks row, then, opened, each subagent's row a level in, a subagent's own a level further. Searching, it
    /// opens on the subagents the search finds (and the ones they hang off) and lists only those; it still folds.
    @ViewBuilder private func subtaskRows(_ parent: Session, _ all: [CodexTasks.Task], depth: Int, cardLast: Bool?) -> some View {
        let found = query.isEmpty ? [] : lookup.projection?.taskMatches[parent.key] ?? []
        let open = found.isEmpty ? expandedTasks.contains(parent.key) : !foldedFound.contains(parent.key)
        let rows = found.isEmpty ? all : found
        let foldLast = cardLast.map { $0 && !open }
        let edges = floating ? foldLast.map { EdgeInsets(top: 0, leading: Theme.Space.page, bottom: $0 ? Theme.Space.xs : 0, trailing: Theme.Space.page) }
            ?? rowInsets(level: depth) : rowInsets(level: depth)
        Button {
            Haptics.tap()
            withAnimation(Theme.Motion.standard) {
                if found.isEmpty {
                    if expandedTasks.remove(parent.key) == nil { expandedTasks.insert(parent.key) }
                } else if foldedFound.remove(parent.key) == nil {
                    foldedFound.insert(parent.key)
                }
            }
        } label: {
            CodexSubtasksRow(count: all.count, open: open,
                             style: foldLast.map { .card(last: $0) } ?? (repoTree ? .tree(level: depth) : compact ? .compact : .regular))
                .padding(.leading, nested(depth))
                // The row's room is the label's, a finger's height (#3753); By Repository's line is one already.
                .modifier(ButtonRowLabel(edges: edges, on: !repoTree))
        }
        .buttonStyle(.plain)
        .modifier(ButtonRowInsets(edges: edges))
        .listRowBackground(repoTree ? treeBackground(false) : floating ? AnyView(Color.clear) : nil)
        .listRowSeparator(floating || repoTree ? .hidden : .automatic)
        .accessibilityIdentifier("agents.subtasks")
        if open {
            ForEach(rows) { task in
                agentRow(task.session, cardLast: cardLast.map { $0 && task.id == rows.last?.id }, depth: depth + task.depth)
            }
        }
    }

    private func agentRow(_ s: Session, context: Bool = false, inStateSection: Bool = false, cardLast: Bool? = nil,
                          showHost: Bool = false, depth: Int = 0, pane: TabPanes.Role? = nil, branch: Bool = false) -> some View {
        // Following the feed, the row's own `FeedRow` draws its last line and time: only the rest is the row's key.
        let shown = feed.following ? AgentsFeed.shape([s], searching: false).sessions[0] : s
        let open = treeSelected(s.key)
        let key = SlotRowKey(session: shown, context: context, inStateSection: inStateSection, cardLast: cardLast, showHost: showHost,
                             depth: depth, pane: pane, branch: branch, searching: searching, query: searching ? query : "",
                             compact: compact, floating: floating, grouping: grouping.rawValue, allMachines: machineFilter.isEmpty,
                             machine: rowMachine(s, context: context), rowStyle: listRowStyle?.rawValue, dense: !ownsTop,
                             pinned: model.isPinned(s.key),
                             muted: model.isMuted(s), open: open)
        return KeyedRow(key: key) {
            slotRowBody(s, context: context, inStateSection: inStateSection, cardLast: cardLast, showHost: showHost, depth: depth,
                        pane: pane, branch: branch, open: open)
        }
        .onAppear { if paginationArmed { Task { await model.sessionLists.appeared(s) } } }
        // Not `.equatable()`: through an `EquatableView` the row's list traits (insets, separator, background) were
        // lost. SwiftUI compares a view with its own `==` when it has one.
    }

    /// A row's background in By Repository's cards (#2531): the card's, every row its own (the system's differed by a
    /// point in the iPad's sidebar between a card's header and its links, a step at its corners); washed for the one
    /// open beside the list. Where the rows float on the page (State Cards, the capsules), none of its own.
    private func treeBackground(_ selected: Bool) -> AnyView? {
        #if os(iOS)
        if floating { return AnyView(Color.clear) }
        return selected ? AnyView(RepoTreeSelected()) : AnyView(Theme.groupedCard)
        #else
        nil
        #endif
    }

    /// An agent row's background: the tree's (`treeBackground`), none of its own in By Repository's cards on the Mac and
    /// in the floating list, else the list's.
    private func slotBackground(_ open: Bool) -> AnyView? {
        // The Sidebar layout's column shades its open Agent as its other rows (`SidebarSelectedShade`, `slotRowBody`).
        if repoTree { return treeBackground(open && unified == nil) }
        return grouping == .repo || floating ? AnyView(Color.clear) : nil
    }

    /// The agent open beside the list in By Repository's cards (#2531; the split's list, not a phone's, whose list is
    /// never beside a chat): its row is washed. The split's column is compact itself, so not its width class. In the
    /// Sidebar layout's column (the drawer on a phone too) the root's Agent in every grouping (#11032): the column's one
    /// selected row, as the main conversation's, a place's or a Persona chat's.
    private var treeOpenSession: SessionKey? {
        guard openedWorkspace == nil else { return nil }
        if unified != nil { return shownAgent }
        return repoTree && !ownsTop ? shownAgent : nil
    }

    private func treeSelected(_ key: SessionKey) -> Bool {
        treeOpenSession == key
    }

    /// The Agent whose row is shaded: the Sidebar layout's root while it is an Agent's chat, also with pages opened on it
    /// (#8359); elsewhere the chat on screen.
    private var shownAgent: SessionKey? {
        if let unified { return unified.shownAgent }
        return model.visibleSession
    }

    /// `open`: the Mac's agent open beside the list; By Repository's cards on iOS, the one whose row is washed.
    private func slotRowBody(_ s: Session, context: Bool, inStateSection: Bool, cardLast: Bool?, showHost: Bool, depth: Int,
                             pane: TabPanes.Role?, branch: Bool, open: Bool = false) -> some View {
        let level = depth + (branch ? 1 : 0)
        return Group {
            if searching {
                // A search result is a button, not a link: a link pushed at once, the push was refused (the search has
                // to close first) and slid back, and the push made after it was lost (2026-10-31).
                Button { openFromSearch(s.key) } label: {
                    HStack(spacing: Theme.Space.s) {
                        slotRowContent(s, context: context, inStateSection: inStateSection, cardLast: cardLast, showHost: showHost, pane: pane, open: open)
                            .padding(.leading, nested(level))
                            .modifier(TreeIndent(level: repoTree ? level : 0))
                        // Capsules draws its own chevron; List and Persona' own rows get the one a link would show. Where the
                        // row ends in its time (`rowMenus`), that is its end.
                        if !capsules && !Self.rowMenus { ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.captionStrong).foregroundStyle(.tertiary) }
                    }
                    .contentShape(.rect)
                    // Compact List's line is shorter than a finger: the button takes the row's room (#3753).
                    .modifier(ButtonRowLabel(edges: rowInsets(level: level), on: compact))
                }
                .buttonStyle(.plain)
            } else {
                regularSlotControl(s) {
                    slotRowContent(s, context: context, inStateSection: inStateSection, cardLast: cardLast, showHost: showHost, pane: pane, open: open)
                        .padding(.leading, nested(level))
                        // By Repository on iOS: a level in per level of the tree, as its worktrees' lines (#3753).
                        .modifier(TreeIndent(level: repoTree ? level : 0))
                }
            }
        }
            #if os(iOS)
            // The Sidebar layout's one selected look (#11032): the shade under the main conversation's, a place's and a
            // Persona chat's row, to the row's edges in every grouping.
            .background { if open && unified != nil { SidebarSelectedShade().padding(Self.selectedOutset) } }
            #endif
            .accessibilityIdentifier(compact ? "agents.compactRow" : "agents.row")
            .accessibilityAddTraits(open ? .isSelected : [])
            // BeautifulUI's Capsules: each agent its own capsule on the page (no list card, line or system chevron); List and
            // Persona' own rows: the list's card.
            .navigationLinkIndicatorVisibility(grouping == .repo || floating || Self.rowMenus ? .hidden : .automatic)
            .listRowBackground(slotBackground(open))
            .listRowSeparator(grouping == .repo || floating || Self.rowMenus ? .hidden : .automatic)
            .modifier(ButtonRowInsets(edges: floating ? cardLast.map { EdgeInsets(top: 0, leading: Theme.Space.page, bottom: $0 ? Theme.Space.xs : 0, trailing: Theme.Space.page) }
                                          ?? rowInsets(level: level)
                                      : rowInsets(level: level), // a card's rows touch (the card is one surface); floating rows keep a little air between them
                                      on: searching && compact))
            .contextMenu { () -> AnyView in
                #if os(iOS)
                AnyView(AgentActionMenuReader(session: s, handlers: rowActions)) // not the row's to observe (#10231)
                #else
                AnyView(AgentRowMenu(s, model: model, actions: rowActions).equatable())
                #endif
            }
    }

    /// iOS and visionOS: rows do not swipe (a sideways drag pages the shell, #1244) and end in their time, not the system's
    /// chevron; their actions are in the long-press menu (the ⋯ that held them too went, #2530).
    static var rowMenus: Bool {
        true
    }

    @ViewBuilder private func regularSlotControl<Content: View>(_ s: Session, @ViewBuilder content: () -> Content) -> some View {
        agentNavigationLink(s.key, content: content)
    }

    /// A split sidebar's value link owns selection outside the bound detail path. Use the path on iOS so tab
    /// selection can clear every Agent detail, without rebuilding the list and losing its state.
    @ViewBuilder private func agentNavigationLink<Content: View>(_ key: SessionKey, @ViewBuilder content: () -> Content) -> some View {
        #if os(iOS)
        // A split's sidebar can read compact: use the parent page's layout, not the column's width class.
        if !ownsTop {
            Button { openFromList(key) } label: { content() }
                .buttonStyle(.plain)
        } else {
            NavigationLink(value: key, label: content)
        }
        #else
        NavigationLink(value: key, label: content)
        #endif
    }
}

/// What an Agents row is drawn from apart from what its body watches (`slotRow`): the values it is handed and the
/// list's own state. When the list's structure changes (a new order, on about half of a busy hub's pushes) each row
/// whose key is the same is not built again: building every row's chain of modifiers took a fifth of each push (#343).
nonisolated struct SlotRowKey: Equatable, Sendable {
    var session: Session
    var context: Bool
    var inStateSection: Bool
    var cardLast: Bool?
    var showHost: Bool
    var depth: Int
    var pane: TabPanes.Role?
    var branch: Bool
    var searching: Bool
    /// While searching: a result's tap keeps the text searched for.
    var query: String
    var compact: Bool
    var floating: Bool
    var grouping: String
    var allMachines: Bool
    var machine: String?
    /// The row style (#2530) and its density: a style picked with the list open draws its rows again.
    var rowStyle: String?
    var dense: Bool
    var pinned: Bool
    var muted: Bool
    /// The Mac: the row of the agent open beside the list.
    var open: Bool
}

/// A view drawn again only when its `key` changes or what its body watches does (its `==`).
private struct KeyedRow<Key: Equatable & Sendable, Content: View>: View, Equatable {
    nonisolated let key: Key
    @ViewBuilder let content: () -> Content

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("KeyedRow") // an Agents row built again (`RenderBudgetTests`)
        #endif
        content()
    }

    nonisolated static func == (a: Self, b: Self) -> Bool { a.key == b.key }
}

/// One row of the Agents list, drawn from its agent as it is now (`AgentsFeed.Row`). It tells `reader` where it is,
/// so the row being read stays put when the list's structure changes around it.
private struct FeedRow<Content: View>: View {
    let row: AgentsFeed.Row
    let reader: ReaderAnchor
    @ViewBuilder let content: (Session, AgentTime) -> Content

    var body: some View {
        let id = row.session.id
        content(row.session, row.time)
            .onGeometryChange(for: CGFloat.self) { $0.frame(in: .global).minY } action: { reader.row(id, top: $0) }
            .onDisappear { reader.gone(id) }
    }
}

/// A fold's header in the Agents list: tells `reader` where it is (as a row does, `FeedRow`) and is a target for its
/// `reveal`, so the header tapped stays put while its section opens or folds (`ReaderAnchor.keep`).
private struct FoldAnchor: ViewModifier {
    let id: String
    let reader: ReaderAnchor

    func body(content: Content) -> some View {
        content
            .id(id)
            .onGeometryChange(for: CGFloat.self) { $0.frame(in: .global).minY } action: { reader.row(id, top: $0) }
            .onDisappear { reader.gone(id) }
    }
}

/// Keeps the row the reader is on where it is on screen when the Agents list's structure lands at rest (#339): a space
/// moving up above it, a card put in at the top. The list keeps its offset, so without this everything below the
/// change slid by its height, and the reader's row went off and another space took its place.
/// - At the very top nothing is kept: what came in shows there.
/// - Not while a finger or a glide moves the list: the change waits for it then (`AgentsFeed.hold`), except what is
///   critical, which may move the list (docs/apple/ios-state.md「Timely」).
@MainActor final class ReaderAnchor {
    /// The list's window band as shown (below the bars), and whether it is at its very top; nil without a list.
    var shown: () -> (band: ClosedRange<CGFloat>, atTop: Bool)? = { nil }
    /// Scrolls the list by that many points (down for more); the list keeps it within its content.
    var scroll: (CGFloat) -> Void = { _ in }
    /// A finger or a glide is moving the list.
    var moving: () -> Bool = { false }
    /// Scrolls the list to a row with that point of it at the same point of the list (0 top, 1 bottom).
    var reveal: (String, CGFloat) -> Void = { _, _ in }
    /// Each drawn row's top in the window, as last laid out.
    private var tops: [String: CGFloat] = [:]
    private var kept: (id: String, top: CGFloat, at: CGFloat, until: Date)?
    /// The row being read since the list last rested from a scroll: kept again for the next change while it shows,
    /// so a row that drifted a little does not hand the place over to its neighbour.
    private var reading: String?

    func row(_ id: String, top: CGFloat) {
        tops[id] = top
        // Every report while it is kept: the list lays out the rows around it again a moment after the change (their
        // real heights), not only once.
        guard let k = kept, k.id == id else { return }
        guard Date.now < k.until else { kept = nil; return }
        guard abs(top - k.top) >= 0.5, !moving() else { return }
        scroll(top - k.top)
    }

    /// A row off screen. The one being kept, taken off by the change (its space went far down): brought back near
    /// where it was, then put exactly where it was once it reports where it is (`row`).
    func gone(_ id: String) {
        tops[id] = nil
        guard let k = kept, k.id == id, Date.now < k.until, !moving() else { return }
        DispatchQueue.main.async { [self] in
            guard let k = kept, k.id == id, tops[id] == nil, !moving() else { return }
            reveal(id, k.at)
        }
    }

    /// What the list shows is about to change: the row being read is the one to keep where it is.
    func willChange() {
        kept = nil
        guard !moving(), let (band, atTop) = shown(), !atTop else { return }
        let mid = (band.lowerBound + band.upperBound) / 2
        // The row being read if it shows, else the row the middle line falls in: the lowest one starting above it
        // (else the first below it).
        let inBand = tops.filter { band.contains($0.value) }
        guard let (id, top) = reading.flatMap({ r in inBand[r].map { (r, $0) } })
            ?? inBand.filter({ $0.value <= mid }).max(by: { $0.value < $1.value }) ?? inBand.min(by: { $0.value < $1.value })
        else { return }
        reading = id
        let at = (top - band.lowerBound) / (band.upperBound - band.lowerBound)
        kept = (id, top, at, .now + 1) // laid out on the next frames; a later move is not this change's
    }

    /// A fold's header was tapped: it stays where it is while its section opens or folds under it. Opening Seen at the
    /// bottom of By Status (59 rows) made the list measure the rows above anew, and the header went ~3000 pt off the top
    /// (#2062).
    func keep(_ id: String) {
        kept = nil
        guard let top = tops[id], let (band, _) = shown(), band.contains(top) else { return }
        reading = id
        kept = (id, top, (top - band.lowerBound) / (band.upperBound - band.lowerBound), .now + 1)
    }

    /// A finger or a glide took over: what moves now is theirs, and what is read afterwards is wherever it stops.
    func cancel() {
        kept = nil
        reading = nil
    }
}

#if DEBUG || PERF_SIGNPOST
/// `probe.agents-list`: the rows as drawn (each follows its agent apart from the list), not the list's structure.
private struct AgentsListProbe: View {
    let feed: AgentsFeed
    let sessions: [Session]
    let asks: [Interaction]
    let page: String?

    var body: some View {
        let drawn = feed.following ? sessions.map { feed.row($0).session } : sessions
        Color.clear.allowsHitTesting(false).screenProbe("agents-list", page: page, AgentsListShown(drawn, asks: asks))
    }
}

/// What the Agents list shows (`probe.agents-list`, docs/apple/ios-state.md「True」): each agent's state and last line, and
/// the questions it lists, to be checked against the hub's.
nonisolated struct AgentsListShown: Encodable, Equatable {
    var states: [String: String] = [:]
    var last: [String: String] = [:]
    var asks: [String] = []

    init() {}

    init(_ sessions: [Session], asks: [Interaction]) {
        for s in sessions {
            let k = s.machineID + "/" + s.sessionID
            states[k] = s.state.rawValue
            last[k] = s.lastText ?? ""
        }
        self.asks = asks.map(\.id).sorted()
    }
}
#endif

/// What the Agents list shows of the hub (docs/apple/ios-state.md): the agents, the questions and the machines, passed on
/// from the model as they change, except while the list is held (`Hold`: scrolled, or out of sight); then what came
/// meanwhile lands at once when it lets go.
///
/// Its own object: the list reads only this, so holding is a flag nobody observes. A busy hub pushes the agents every
/// two seconds, and any update of a `List` mid-glide (the same rows or not) has it measure its rows anew: the content
/// jumped by their difference from its estimate. Holding by the list's own state drew it again at every scroll's start.
///
/// How long anything waits is `Hold`'s (docs/apple/ios-state.md「Timely」); what may not wait is this feed's: what needs the
/// user, or says something is wrong, is never held: a question or approval coming or going, an agent starting or
/// stopping to need the user, one going offline, a machine going offline or failing. It lands at once, even mid-glide,
/// alone (`landCritical`): the list may still move under the finger (a card put in at the top, a space moving up),
/// which is the price of showing it now.
@MainActor @Observable final class AgentsFeed {
    #if DEBUG
    /// The controlled UI driver must cancel/hold the same reader as a real gesture; never retained by the probe.
    @ObservationIgnored static weak var pagingProbe: AgentsFeed?
    #endif
    /// The agents as the list's structure last changed (`shape`): the body reads this, each row its own `Row`.
    private(set) var sessions: [Session] = []
    private(set) var interactions: [Interaction] = []
    private(set) var machines: [Machine] = []
    /// Structural inputs only: row text/ticks do not advance this key (`AgentListProjectionCache.Key`). Critical,
    /// appended-page and partition landings derive at once (`immediateRevision`); other revisions, including what a
    /// released hold lands, are prepared off the main actor.
    private(set) var revision: UInt64 = 0
    @ObservationIgnored private(set) var immediateRevision: UInt64 = 0
    /// A capped scrolling landing may publish only within the activity that authorized it.
    @ObservationIgnored private var landedRevision: UInt64 = 0
    @ObservationIgnored private var landedActivity: UInt64?
    @ObservationIgnored private var partitions: [AgentListProjectionCache.Partition] = []
    @ObservationIgnored let projection = AgentListProjectionCache()
    /// Following a model: until then the list reads the model itself.
    private(set) var following = false
    /// The row being read, kept in place when what the list shows changes.
    @ObservationIgnored let reader = ReaderAnchor()
    /// Scrolled or out of sight: the list says which (`Hold.set`), the hold says when what waits lands.
    @ObservationIgnored let hold = Hold("Agents list")
    @ObservationIgnored private weak var model: AppModel?
    @ObservationIgnored private var watching = false
    /// What the rows show: the latest that landed (`sessions` may be older in what only rows show).
    @ObservationIgnored private var shown: [Session] = []
    @ObservationIgnored private var shaped = Shape()
    @ObservationIgnored private var rows: [String: Row] = [:]
    /// The list filters by the agents' last lines while it is searched: they are structure then.
    @ObservationIgnored var searching = false {
        didSet { if searching != oldValue { restructure(force: true) } }
    }
    /// Only the flat timeline follows activity time while an agent is working.
    @ObservationIgnored var recent = false {
        didSet { if recent != oldValue { restructure(force: true) } }
    }
    init() {
        hold.land = { [weak self] in self?.land() }
    }

    func follow(_ model: AppModel) {
        guard self.model == nil else { return }
        self.model = model
        #if DEBUG
        Self.pagingProbe = self
        #endif
        model.sessionLists.bind(model)
        pull()
        immediateRevision = revision
        following = true
    }

    func hasPartitions(_ partitions: [AgentListProjectionCache.Partition]) -> Bool { self.partitions == partitions }

    /// One agent as its row shows it. The row reads this: a change only rows show (the last line, the time, a new
    /// step) draws that row again, not the list.
    @Observable final class Row {
        /// The agent as the row shows it: its `rev` and `updatedAt` are as of its last other change.
        fileprivate(set) var session: Session
        /// When it last moved, for the row's time alone: a working agent's moves on every step.
        let time: AgentTime
        init(_ session: Session) {
            self.session = session
            time = AgentTime(session.updatedAt)
        }
    }

    func row(_ s: Session) -> Row {
        if let r = rows[s.id] { return r }
        let r = Row(s)
        rows[s.id] = r
        return r
    }

    /// Copies what changed.
    private func pull() {
        hold.landed()
        guard let model, let (s, i, m) = current() else { return }
        let nextPartitions = AgentListProjectionCache.partitions(model)
        let switched = nextPartitions != partitions
        let immediate = switched || Self.critical(s, i, m) != Self.critical(shown, interactions, machines) ||
            (model.sessionLists.paged && Self.appendsPage(s, after: shown))
        if switched {
            // A removed/replaced credential partition never retains old rows behind a hidden/scrolling hold.
            partitions = nextPartitions
            projection.invalidate()
            shown = []
            if !sessions.isEmpty { reader.willChange(); sessions = [] }
            shaped = Shape()
            rows = [:]
            revision &+= 1
        }
        show(s)
        if i != interactions {
            reader.willChange()
            interactions = i
            // A waiting child's words land on the parent row. The session list itself did not change.
            applyFoldedRows(s)
        }
        if !Self.sameListing(m, machines) { reader.willChange(); machines = m; revision &+= 1 }
        if immediate { immediateRevision = revision }
    }

    /// The same machines as the list shows them: a machine's heartbeat (`lastSeen`, on every push of a busy hub) is
    /// shown only on the machines page, and drew the whole list again each time (#1495).
    nonisolated static func sameListing(_ a: [Machine], _ b: [Machine]) -> Bool { AppModel.sameApartFromHeartbeat(a, b) }

    /// The rows that changed get their agent; the list gets the new list only if its structure changed.
    private func show(_ s: [Session]) {
        guard s != shown else { return }
        shown = s
        reader.willChange() // a row above the one being read may grow or shrink (its last line), not only the structure
        applyFoldedRows(s)
        restructure(force: false)
    }

    /// The row shows the folded copy: a waiting child makes the parent Needs You, and says what that child asks.
    /// The stored list stays the hub's own rows, so the chat page and the structure's equality are unchanged.
    private func applyFoldedRows(_ s: [Session]) {
        let sideFolded = SideFold.fold(s, groups: model?.sideFoldGroups ?? []).listed
        let folded = Dictionary(ChildSessions.fold(sideFolded, asks: ChildSessions.asks(interactions)).map { ($0.id, $0) },
                                uniquingKeysWith: { a, _ in a })
        for x in s {
            let display = folded[x.id] ?? x
            if let r = rows[x.id] {
                if !Session.sameApartFromTicks(r.session, display) { r.session = display }
                if r.time.updatedAt != x.updatedAt { r.time.updatedAt = x.updatedAt }
            } else {
                rows[x.id] = Row(display)
            }
        }
        if rows.count > s.count {
            let ids = Set(s.map(\.id))
            rows = rows.filter { ids.contains($0.key) }
        }
    }

    private func restructure(force: Bool) {
        let shape = Self.shape(shown, searching: searching, recent: recent)
        guard force || shape != shaped else { return }
        shaped = shape
        revision &+= 1
        if shown != sessions { reader.willChange(); sessions = shown }
    }

    /// What the list's structure reads of the agents: the agents with what only rows show taken out, and the order
    /// their times put them in.
    struct Shape: Equatable {
        var sessions: [Session] = []
        /// The agents' ids, latest first by the selected time: what decides the order of rows and of
        /// spaces, without the time itself, which moves on every step of a working agent and which only rows (and a
        /// quiet space's line, from the rows') show.
        var byTime: [String] = []
    }

    /// What the list's structure reads of the agents (the groups, the order, the folds, the counts, the headers; the
    /// last lines while searching), with what only rows show taken out (docs/apple/ios-state.md「Step 3」).
    /// - The time: only the order it puts agents in (`Shape.byTime`); a minute passing changes nothing else.
    /// - Subagents and background work: only whether any runs (a row with one stays out of the idle fold).
    nonisolated static func shape(_ list: [Session], searching: Bool, recent: Bool = false) -> Shape {
        let byTime: [String]
        if recent {
            byTime = AgentListOrder.recent(list).map(\.id)
        } else {
            byTime = list.map { (id: $0.id, at: $0.sortDate) }
                .sorted { $0.at != $1.at ? $0.at > $1.at : $0.id < $1.id }
                .map(\.id)
        }
        return Shape(sessions: list.map { s in
            var x = s
            x.rev = nil
            x.suggestion = nil
            x.suggestionRisky = false
            x.ports = nil
            x.queued = nil
            x.permissionMode = nil
            x.effort = nil
            x.model = nil
            x.seenAt = nil
            if !searching { x.lastText = nil; x.asks = nil } // an Awaiting Reply row's question: only rows show it
            x.updatedAt = nil
            x.subagents = (s.subagents ?? []).contains(where: \.running) ? [] : nil
            x.background = (s.background ?? []).isEmpty ? nil : []
            x.workflows = s.runningWorkflows.isEmpty ? nil : []
            x.listWorkflowProgress = nil // a summary row's run: its counts move on every step, only rows show them
            return x
        }, byTime: byTime)
    }

    /// When an agent last moved, as its row shows it (nil before the list has seen it): a quiet space's line reads
    /// its agents' times from here, not from the list's structure.
    func time(_ id: String) -> Date? { rows[id]?.time.updatedAt }

    /// The model's now, watching it for the next change if nothing is watching yet (`withObservationTracking` tells
    /// once; watched again at every change, held or not, never twice at a time).
    private func current() -> ([Session], [Interaction], [Machine])? {
        guard let model else { return nil }
        guard !watching else { return (model.sessionLists.sessions, model.scopedInteractions, model.scopedMachines()) }
        watching = true
        return withObservationTracking {
            let _ = AgentListProjectionCache.partitions(model)
            return (model.sessionLists.sessions, model.scopedInteractions, model.scopedMachines())
        } onChange: { [weak self] in
            Task { @MainActor in // told before the change: read it after
                self?.watching = false
                self?.changed()
            }
        }
    }

    /// What was held lands now, while the list may still be held.
    private func land() {
        #if (os(iOS) || os(visionOS)) && (DEBUG || PERF_SIGNPOST)
        if hold.held { PerfScroll.mark("Agents list lands while held") }
        PerfScroll.shared.released()
        #endif
        pull()
        landedRevision = revision
        landedActivity = hold.reasons == [.scrolling] ? hold.activity : nil
        projection.resume()
    }

    /// Only the scrolling cap can authorize a held publication, and not after another gesture or visibility edge.
    func mayPublish(_ revision: UInt64) -> Bool {
        if revision <= landedRevision, hold.reasons == [.scrolling], landedActivity == hold.activity { return true }
        return !hold.wait()
    }

    private func changed() {
        if let model, AgentListProjectionCache.partitions(model) != partitions { return pull() }
        guard hold.wait() else { return pull() }
        guard let (s, i, m) = current() else { return }
        // A newly fetched page is not a live insertion: append every row now, not only urgent rows (which left gaps
        // such as 49 → 52 during a glide until the scroll hold released). Existing rows keep their identities.
        if model?.sessionLists.paged == true, Self.appendsPage(s, after: shown) { return pull() }
        if Self.critical(s, i, m) != Self.critical(shown, interactions, machines) { landCritical(s, i, m) }
    }

    nonisolated static func appendsPage(_ next: [Session], after old: [Session]) -> Bool {
        next.count > old.count && zip(next, old).allSatisfy { $0.key == $1.key }
    }

    /// While held, only what must show at once, onto the rows as shown: the states of the agents that start or stop
    /// needing the user or go offline, the questions, the machines. Last lines, times and order wait for the rest:
    /// landing all of it mid-glide changed the rows' heights under the finger (the list jumped by 100 pt and more).
    private func landCritical(_ s: [Session], _ i: [Interaction], _ m: [Machine]) {
        #if (os(iOS) || os(visionOS)) && (DEBUG || PERF_SIGNPOST)
        PerfScroll.mark("Agents list lands what is critical while held")
        PerfScroll.shared.released()
        #endif
        func urgent(_ x: Session) -> Bool { x.state == .needsYou || x.state == .offline }
        let now = Dictionary(s.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        let listed = Set(shown.map(\.id))
        var next = shown.compactMap { old -> Session? in
            guard let new = now[old.id] else { return urgent(old) ? nil : old } // gone: the rest goes when it lands
            guard new.state != old.state, urgent(new) || urgent(old) else { return old }
            var x = old
            x.state = new.state
            x.pendingInteractions = new.pendingInteractions
            return x
        }
        next += s.filter { urgent($0) && !listed.contains($0.id) }
        show(next)
        if i != interactions { reader.willChange(); interactions = i }
        if !Self.sameListing(m, machines) { reader.willChange(); machines = m; revision &+= 1 }
        immediateRevision = revision
        if next == s { hold.landed() }
    }

    /// What must show at once: who needs the user, which questions wait, what is offline or failing.
    nonisolated static func critical(_ sessions: [Session], _ interactions: [Interaction], _ machines: [Machine]) -> Set<String> {
        var out = Set<String>()
        for s in sessions where s.state == .needsYou || s.state == .offline { out.insert("\(s.state.rawValue) \(s.id)") }
        for i in interactions { out.insert("ask " + i.uid) }
        for m in machines where !m.online || !(m.error ?? "").isEmpty { out.insert("down \(m.ref.path) \(m.error ?? "")") }

        return out
    }
}

/// All / Needs You / Unread / Working under the search field, each with its count; one tap switches.
struct QuickFilterBar: View, Equatable {
    nonisolated let selection: AgentFilter
    nonisolated let counts: [AgentFilter: Int]
    /// The hub counts the whole transport collection, including assistants, not the exact visible Agents membership.
    nonisolated var countsUnknown = false
    nonisolated var pageAligned = false
    var choose: (AgentFilter) -> Void

    nonisolated static func == (a: Self, b: Self) -> Bool {
        a.selection == b.selection && a.counts == b.counts && a.countsUnknown == b.countsUnknown && a.pageAligned == b.pageAligned
    }

    /// iOS uses the owner-requested scrolling capsule. Other platforms retain their existing controls.
    /// The strip clears vertical content margins so navigation chrome cannot inset its content below search.
    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("QuickFilterBar")
        #endif
        #if os(iOS)
        Group {
            if pageAligned {
                capsule.modifier(AgentsFieldPlace())
            } else {
                capsule.padding(.horizontal, Theme.Space.page)
            }
        }
        .padding(.vertical, Theme.Space.sm)
        #else
        chipsBar
        #endif
    }

    #if os(iOS)
    private var capsule: some View {
        CapsuleFilterBar(items: availableFilters.map {
            CapsuleFilterItemData(id: $0.rawValue, title: $0.title, count: counts[$0], tone: $0.tone, symbol: $0.symbol)
        }, selection: selection.rawValue, choose: { id in
            if let filter = AgentFilter(rawValue: id), filter != selection { choose(filter) }
        }) {
            // In a view graph of its own: the list's updates replaced the open menu and scrolled it to its top (#10738).
            MenuIsland(content: AgentsGroupingMenu(iconOnly: true))
        }
    }
    #endif


    private var chipsBar: some View {
        // Each candidate measured once (`MeasuredOnce`): the row's graph asks for its size again on every frame the page
        // slides, and `ViewThatFits` measured all of them, every chip's text, each time (#5438).
        ViewThatFits(in: .horizontal) {
            if AppStyle.current.usesBeautifulUI {
                MeasuredOnce { HStack(spacing: Theme.Space.xs) { chips } }
                MeasuredOnce { LarkFlowLayout(spacing: Theme.Space.xs, lineSpacing: Theme.Space.sm) { chips } }
            } else {
                MeasuredOnce { HStack(spacing: Theme.Space.s) { plainChips(padding: Theme.Space.m) } }
                MeasuredOnce { HStack(spacing: Theme.Space.sm) { plainChips(padding: Theme.Space.s) } }
                MeasuredOnce { LarkFlowLayout(spacing: Theme.Space.sm, lineSpacing: Theme.Space.s) { plainChips(padding: Theme.Space.s) } }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.sm)
    }

    /// All, and the states that have agents right now; the chosen one stays while it empties, so it can be left.
    var availableFilters: [AgentFilter] {
        AgentFilter.allCases.filter { countsUnknown || $0 == .all || $0 == selection || (counts[$0] ?? 0) > 0 }
    }
    private var shown: [AgentFilter] { availableFilters }

    /// Persona' own chips: the state's dot, the name and the count on a tinted capsule.
    @ViewBuilder private func plainChips(padding: CGFloat) -> some View {
        ForEach(shown) { f in
            let on = f == selection
            let tone = f.tone
            let tint = tone?.text ?? Theme.accent
            Button { if !on { choose(f) } } label: {
                HStack(spacing: Theme.Space.sm) {
                    if let symbol = f.symbol, let tone {
                        ThemedIcon(symbol).font(Theme.Font.label).foregroundStyle(tone.dot)
                    } else if let tone {
                        Circle().fill(tone.dot).scaledFrame(width: Theme.Size.dotS, height: Theme.Size.dotS)
                    }
                    Text(f.title)
                    if let n = counts[f] {
                        Text("\(n)").monospacedDigit().foregroundStyle(on ? tint : Color.secondary)
                            .contentTransition(.numericText(value: Double(n)))
                    }
                }
                .font(on ? Theme.Font.secondaryStrong : Theme.Font.secondaryMedium)
                .lineLimit(1)
                .fixedSize()
                .foregroundStyle(on ? tint : Color.primary)
                .padding(.horizontal, padding)
                .padding(.vertical, Theme.Space.s)
                .background(Capsule().fill(on ? tint.opacity(0.15) : Color.secondary.opacity(0.1)))
                .overlay(Capsule().strokeBorder(on ? tint.opacity(0.35) : .clear, lineWidth: 1))
                .contentShape(Capsule())
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("agents.filter." + f.rawValue)
            .accessibilityLabel(counts[f].map { "\(f.title) \($0)" } ?? f.title)
            .accessibilityAddTraits(on ? .isSelected : [])
        }
    }

    /// BeautifulUI's filter chips: the state's dot, the name and the count; the chosen one is the raised capsule.
    @ViewBuilder private var chips: some View {
        ForEach(shown) { f in
            let on = f == selection
            BUIFilterChip(f.title, dot: f.tone?.dot, count: counts[f], isActive: on) { if !on { choose(f) } }
                .accessibilityIdentifier("agents.filter." + f.rawValue)
                .accessibilityLabel(counts[f].map { "\(f.title) \($0)" } ?? f.title)
        }
    }
}

/// A section header that folds its section: the header itself plus a chevron, spoken as "name, expanded / collapsed, …".
/// A finger's height whatever the header draws (#3753). Optional vertical padding stays inside the plain button's label so
/// the hit area covers the full height of the header cell (#13575).
struct FoldButton<Content: View, Accessory: View>: View {
    var title: String
    var folded: Bool
    var detail: String
    /// The fold's own identifier where the header also holds an `accessory` (one set around the header would reach the
    /// accessory too); without one, callers set it around the header as they always did.
    var identifier: String? = nil
    var topPadding: CGFloat = Theme.Space.flush
    var bottomPadding: CGFloat = Theme.Space.flush
    var action: () -> Void
    @ViewBuilder var label: Content
    /// A section action drawn between the title and the chevron, a button of its own outside the fold's (a section's
    /// Clear Seen, #12514). None: the header is the one button it always was.
    @ViewBuilder var accessory: Accessory

    var body: some View {
        if Accessory.self == EmptyView.self {
            Button(action: action) {
                HStack(spacing: Theme.Space.sm) {
                    label
                    Spacer(minLength: 4)
                    FoldChevron(folded: folded)
                }
                // The room it gains is above its words: a section's title stays by its card.
                .modifier(TouchHeight(alignment: .bottom))
                #if os(iOS)
                .padding(.top, topPadding)
                .padding(.bottom, bottomPadding)
                #endif
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .modifier(FoldSpoken(title: title, folded: folded, detail: detail))
        } else {
            HStack(spacing: Theme.Space.sm) {
                Button(action: action) {
                    // As wide as the header leaves it (`TouchHeight`), its title at the leading edge.
                    label
                        .modifier(TouchHeight(alignment: .bottom))
                        #if os(iOS)
                        .padding(.top, topPadding)
                        .padding(.bottom, bottomPadding)
                        #endif
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .modifier(FoldSpoken(title: title, folded: folded, detail: detail, identifier: identifier))
                accessory
                    #if os(iOS)
                    .padding(.top, topPadding)
                    .padding(.bottom, bottomPadding)
                    #endif
                // The chevron folds too, as it does in the header without an action; the button above says so. A finger's
                // square at the trailing edge (#3753), its glyph where the header without an action draws it.
                Button(action: action) {
                    FoldChevron(folded: folded)
                        .tapTargetFrame(alignment: .bottomTrailing)
                        #if os(iOS)
                        .padding(.top, topPadding)
                        .padding(.bottom, bottomPadding)
                        #endif
                        .contentShape(.rect)
                }
                .buttonStyle(.plain)
                .accessibilityHidden(true)
            }
        }
    }
}

extension FoldButton where Accessory == EmptyView {
    init(title: String, folded: Bool, detail: String, topPadding: CGFloat = Theme.Space.flush,
         bottomPadding: CGFloat = Theme.Space.flush, action: @escaping () -> Void, @ViewBuilder label: () -> Content) {
        self.init(title: title, folded: folded, detail: detail, identifier: nil, topPadding: topPadding,
                  bottomPadding: bottomPadding, action: action, label: label, accessory: { EmptyView() })
    }
}

/// How a fold button is spoken: "name, expanded / collapsed, …", one button.
private struct FoldSpoken: ViewModifier {
    var title: String
    var folded: Bool
    var detail: String
    var identifier: String? = nil

    func body(content: Content) -> some View {
        let spoken = content
            .accessibilityElement(children: .ignore)
            .accessibilityLabel([title, folded ? String(localized: "Collapsed [Readiness Card ee6046]") : String(localized: "Expanded"), detail].filter { !$0.isEmpty }
                .joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label")))
            .accessibilityAddTraits(.isButton)
        if let identifier { spoken.accessibilityIdentifier(identifier) } else { spoken }
    }
}

struct FoldChevron: View {
    var folded: Bool
    var body: some View {
        ThemedIcon(Theme.Icon.expand)
            .font(Theme.Font.labelHeavy)
            .foregroundStyle(.tertiary)
            .rotationEffect(.degrees(folded ? -90 : 0))
            .animation(Theme.Motion.standard, value: folded)
    }
}

struct SpaceRoute: Hashable { var id: String }

struct SectionHeader: View {
    var title: String
    var count: Int?
    /// The count's color: a state's tone, or StatusPalette.idle for a plain count.
    var tone: StatusTone
    /// A mark before the title in the tone's color: Waiting in the Background's clock (#6045).
    var symbol: String? = nil
    @Environment(\.compactList) private var compact

    var body: some View {
        HStack(spacing: Theme.Space.sm) {
            if let symbol { ThemedIcon(symbol).foregroundStyle(tone.dot).accessibilityHidden(true) }
            if compact {
                // Color.primary: a section header dims hierarchical styles a level (docs/lessons/ios.md).
                Text(title).font(CompactList.sectionTitle).foregroundStyle(Color.primary).textCase(nil).lineLimit(1)
            } else {
                Text(title)
            }
            if let count { CountBadge(count: count, look: .tone(tone)) }
        }
    }
}

/// Section header of a space: name, machine and a count per state; tapping it folds the space (the counts stay).
struct SpaceHeader: View {
    var group: SpaceGroup
    var machine: Machine?
    var showMachine: Bool
    var pinned = false
    /// Muted with agents running: quieter than the busy spaces, not fainter than the idle ones.
    var muted = false
    var folded = false
    /// A mark before the name: a repository's folder (By Repository).
    var symbol: String? = nil
    @Environment(\.compactList) private var compact

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("SpaceHeader")
        #endif
        CountsHeading {
            // Compact List's titles are big enough to tell a repository from a space: no mark (it cut a short name, "weat…").
            if let symbol, !compact {
                ThemedIcon(symbol).font(Theme.Font.label).foregroundStyle(Color.secondary)
            }
            if compact {
                // Compact List: the space is the title over its rows (Color.primary: a header dims `.primary` a level).
                group.headingText(CompactList.sectionTitle).foregroundStyle(Color.primary).textCase(nil).lineLimit(1)
            } else {
                group.headingText(Theme.Font.sectionHeader).foregroundStyle(.secondary).textCase(nil).lineLimit(1)
            }
            let small = compact ? CompactList.meta : Theme.Font.label
            if pinned { ThemedIcon(Theme.Icon.pinned).font(small).foregroundStyle(Theme.accent).accessibilityLabel("Pinned [Personal Agent List View fb47db]") }
            if muted { ThemedIcon(Theme.Icon.muted).font(small).foregroundStyle(Color.secondary).accessibilityLabel("Muted [Chats View b66705]") }
            if showMachine, let machine {
                Text(machine.title).font(small).foregroundStyle(Color.secondary).textCase(nil).lineLimit(1)
            }
        } counts: {
            StateCounts(group: group).opacity(muted ? 0.6 : 1)
        } trailing: {
            FoldChevron(folded: folded)
        }
        .contentShape(.rect)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([group.headingLabel, folded ? String(localized: "Collapsed [Readiness Card ee6046]") : String(localized: "Expanded"),
                             pinned ? String(localized: "Pinned [Personal Agent List View fb47db]") : "", muted ? String(localized: "Muted [Chats View b66705]") : "",
                             showMachine ? machine?.title ?? "" : "", StateCounts.spoken(group)]
            .filter { !$0.isEmpty }.joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label")))
        .accessibilityAddTraits(.isButton)
    }
}

extension SpaceGroup {
    @MainActor func headingText(_ font: Font) -> Text {
        let title = Text(name).font(font)
        guard let qualifier = projectFolder?.qualifier else { return title }
        return Text("\(title)\(Text(" · " + qualifier).font(font).fontWeight(.regular).foregroundColor(.secondary))")
    }
}

struct StateCounts: View {
    var group: SpaceGroup
    @Environment(\.compactList) private var compact

    /// "3 need you, 12 working, 35 unread, 4 idle" for VoiceOver (and the UI tests).
    static func spoken(_ group: SpaceGroup) -> String {
        let waiting = group.waitingInBackground
        let idle = group.count(.idle) + group.count(.unknown) - waiting
        return ([AgentState.needsYou, .working, .unread].compactMap { st in group.count(st) > 0 ? String(localized: "\(group.count(st)) \(st.label)") : nil }
            + (waiting > 0 ? ["\(waiting) \(String(localized: "Waiting in the background"))"] : [])
            + (idle > 0 ? [String(localized: "\(idle) idle")] : [])).joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label"))
    }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("StateCounts")
        #endif
        HStack(spacing: Theme.Space.sm) {
            ForEach([AgentState.needsYou, .working, .unread], id: \.self) { st in
                let n = group.count(st)
                if n > 0 {
                    HStack(spacing: Theme.Space.xs) {
                        Circle().fill(st.color).scaledFrame(width: Theme.Size.dotXS, height: Theme.Size.dotXS)
                        Text("\(n)").font((compact ? CompactList.metaStrong : Theme.Font.labelHeavy).monospacedDigit())
                            .foregroundStyle(Color.secondary)
                            .contentTransition(.numericText(value: Double(n)))
                    }
                    .accessibilityElement(children: .ignore)
                    .accessibilityLabel("\(n) \(st.label)")
                }
            }
            // Waiting in the Background (#6045): its clock and a number, after Working's.
            let waiting = group.waitingInBackground
            if waiting > 0 {
                HStack(spacing: Theme.Space.xs) {
                    ThemedIcon(Theme.Icon.backgroundWaiting).font(Theme.Font.caption).imageScale(.small)
                        .foregroundStyle(StatusPalette.backgroundWaiting.dot)
                    Text("\(waiting)").font((compact ? CompactList.metaStrong : Theme.Font.labelHeavy).monospacedDigit())
                        .foregroundStyle(Color.secondary)
                        .contentTransition(.numericText(value: Double(waiting)))
                }
                .accessibilityElement(children: .ignore)
                .accessibilityLabel("\(waiting) \(String(localized: "Waiting in the background"))")
            }
            // Idle as a gray dot and a number like the others: "4 idle" wrapped to two lines in a busy header.
            let idle = group.count(.idle) + group.count(.unknown) - waiting
            if idle > 0 {
                HStack(spacing: Theme.Space.xs) {
                    Circle().fill(Color.secondary.opacity(0.45)).scaledFrame(width: Theme.Size.dotXS, height: Theme.Size.dotXS)
                    Text("\(idle)").font((compact ? CompactList.metaStrong : Theme.Font.labelStrong).monospacedDigit())
                        .foregroundStyle(Color.secondary)
                        .contentTransition(.numericText(value: Double(idle)))
                }
                .accessibilityLabel("\(idle) idle")
            }
        }
        .lineLimit(1)
        .fixedSize()
    }
}

/// A heading's name and its agents' state counts: side by side; the counts on a line of their own under the name when
/// the name would be left under half the width (`BesideOrUnder`). `StateCounts` never gives up width, and beside them a
/// narrow phone left the name none at all (#1658). `trailing` (the fold chevron) stays on the name's line.
struct CountsHeading<Name: View, Counts: View, Trailing: View>: View {
    var spacing: CGFloat = Theme.Space.s
    @ViewBuilder var name: Name
    @ViewBuilder var counts: Counts
    @ViewBuilder var trailing: Trailing

    var body: some View {
        BesideOrUnder(gap: spacing * 2 + Theme.Space.xs, tailGap: spacing) {
            // Each part is one view for the layout, its own pieces side by side as in a row.
            HStack(spacing: spacing) { name }
            HStack(spacing: spacing) { counts }
            HStack(spacing: spacing) { trailing }
        }
    }
}

extension CountsHeading where Trailing == EmptyView {
    init(spacing: CGFloat = Theme.Space.s, @ViewBuilder name: () -> Name, @ViewBuilder counts: () -> Counts) {
        self.init(spacing: spacing, name: name, counts: counts, trailing: { EmptyView() })
    }
}

/// An agent row's words and its time (with the source under it): the time at the row's end; under the words when
/// beside them it would leave the title under 70% of the width (its words stay on one line, so they get the whole row when stacked) (`BesideOrUnder`, #1658). Outside `SlotRow`, whose
/// equality is its stored values.
struct TimeBesideOrUnder<Words: View, Time: View>: View {
    @ViewBuilder var words: Words
    @ViewBuilder var time: Time

    var body: some View {
        BesideOrUnder(alignment: .firstTextBaseline, minShare: 0.7) {
            words
            time
        }
    }
}

/// Where an agent was opened (#2526): Lark Group (botmux; a tap opens its Feishu topic) or Terminal (herdr), each on its own
/// tint. A small chip under the row's time, so the title keeps its whole line (#3043).
struct SourceLabel: View {
    var source: SessionSource
    var link: URL?
    @Environment(\.openURL) private var openURL

    var body: some View {
        let chip = Chip(text: source.label, look: .tint(source == .feishu ? Theme.sourceFeishu : Theme.sourceTerminal), compact: true)
        if source == .feishu, let link {
            Button { openURL(link) } label: { chip }
                .buttonStyle(.plain)
                .accessibilityLabel("Lark group. Opens this topic in Lark.")
                .accessibilityIdentifier("agents.row.source")
        } else {
            chip.accessibilityIdentifier("agents.row.source")
        }
    }
}

/// One agent: its state dot, the title and when, the latest line, then where it runs. Only Needs You is spelled out (in
/// its red, on the last line); the dot says every other state. The tab, the agent kind and the machine are plain
/// words there: a tinted tab chip, a colored state word and an outlined kind tag on one line said the state three times.
nonisolated struct SlotRow: View, Equatable {
    var session: Session
    var pendingTitle: String? = nil
    /// Muted (itself or its space): no state word, a bell-slash — it should not catch the eye.
    var muted = false
    /// Named when the list is grouped by state or machine (otherwise the section is the space).
    var space: String? = nil
    var machine: String? = nil
    /// The host it runs in (botmux…), named when the list mixes hosts (by todo, #1836).
    var host: String? = nil
    /// The section already says the state (grouped by state): no state word on the row.
    var hideState = false
    /// The state dot; off where something beside the row already shows the state in its color (SessionRow's avatar).
    var dot = true
    /// What the BeautifulUI style's row is drawn on.
    var surface: Surface = .none
    /// When it last moved, followed apart from the row (`AgentsFeed.Row.time`); nil: the session's own.
    var time: AgentTime? = nil
    /// Its place in a tab of several agents (#2320): named by its agent, the starter marked; the tab's row above says
    /// which tab, so the row does not.
    var pane: TabPanes.Role? = nil
    /// iOS and visionOS: a row ends in its time, no chevron (its actions are in its long-press menu, #1244, #2530).
    static var endsInTime: Bool {
        true
    }

    /// The Agents list's row style (#2530); nil: the row as it was (Knowledge, a machine's page).
    var rowStyle: AgentRowStyle? = nil
    /// In a regular-width sidebar (an open iPhone Duo, an iPad): style A's latest words take one line.
    var dense = false
    /// Unread folded side session(s) keeping this parent row Unread (#13524); nil leaves the row unchanged.
    var sideUnread: SideFold.UnreadSummary? = nil
    /// Hub setting `use_herdr_tab_name`: promotes the herdr tab name to main title and moves the CLI title to detail (#13899).
    var preferHerdrTabName = false

    var primaryTab: String? { session.displayTabName(preferTab: preferHerdrTabName) }
    var rowTitle: String { pendingTitle ?? session.displayTitle(preferTab: preferHerdrTabName) }
    var titleDetail: String? { session.displayTitleDetail(preferTab: preferHerdrTabName) }

    enum Surface: Equatable {
        /// Bare: a List's row that has its own background and chevron.
        case none
        /// The Agents list's own capsule with its chevron.
        case capsule
        /// A row of a quiet space's card, set in a level under the card's title; `last` ends the card.
        case cardRow(last: Bool)
    }

    private func taskShape<L: View, T: View, D: View>() -> TaskRow<L, T, D>.Shape {
        switch surface {
        case .none: .plain
        case .capsule: .capsule
        case .cardRow(let last): .list(first: false, last: last)
        }
    }

    /// The tag under the time: where it was opened, for an agent (Persona's own conversations carry none).
    var rowSource: SessionSource? { session.assistant ? nil : session.source }

    /// The parent's own preview line, omitted when only folded side sessions are unread so the side line stands in its place (#13524).
    @MainActor var rowPreview: String? { sideUnread?.replacesPreview == true ? nil : session.listPreview(muted: muted) }

    private var indented: Bool { if case .cardRow = surface { true } else { false } }

    /// BeautifulUI's task row: the state's badge, the title over the latest line, where it runs under that; when, and
    /// Needs You as a pill, on the right.
    @MainActor
    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("SlotRow")
        #endif
        Group {
            if AppStyle.current.usesBeautifulUI {
                TaskRow(TaskItem(id: session.key.sessionID, title: rowTitle,
                                 subtitle: rowPreview, footnote: meta,
                                 status: .pending(step: 0)),
                        shape: taskShape(), isOpen: session.leadWorkflow != nil || sideUnread != nil,
                        accessory: surface == .none || Self.endsInTime ? .none : .disclosure, titleLines: 2, subtitleLines: 1) {
                    // The badge's column is 24 pt: only the badge goes there (#3004); the source tag is under the time (#3043).
                    if dot { AgentStateBadge(state: session.state, waiting: session.waitsInBackground, justFinished: session.showsJustFinished(muted: muted)).padding(.leading, indented ? Theme.Space.l : 0) }
                } trailing: {
                    HStack(alignment: .top, spacing: Theme.Space.s) {
                        VStack(alignment: .trailing, spacing: Theme.Space.xs) {
                            AgentTimeText(time: time, fallback: session.updatedAt, font: Theme.Font.label)
                            sourceLabel
                            if session.state == .needsYou && !muted && !hideState && !pendingCount { BUIPill(session.state.label, tone: .custom(ink: StatusPalette.needsYou.text, fill: StatusPalette.needsYou.tint)) }
                            if showsReviewWord {
                                let tone = session.stateTone(muted: muted)
                                BUIPill(session.stateLabel(muted: muted), tone: .custom(ink: tone.text, fill: tone.tint))
                            }
                            if waitingWord {
                                let tone = StatusPalette.backgroundWaiting
                                BUIPill(session.stateLabel, tone: .custom(ink: tone.text, fill: tone.tint))
                            }
                            if muted {
                                ThemedIcon(Theme.Icon.muted).font(Theme.Font.label).foregroundStyle(.secondary).accessibilityLabel("Notifications muted")
                            }
                        }
                    }
                } detail: {
                    if let sideUnread { SideUnreadLine(parent: session.key, summary: sideUnread) }
                    if !session.runningWorkflows.isEmpty { WorkflowLine(runs: session.runningWorkflows) }
                    RunningChildrenCaption(session: session)
                }
                // The badge's column holds the state alone (#3004): the agent's mark leads the title (#6390), with
                // ultracode's mark after it while that is on (#1307; the library's row has no slot after the title).
                .titleLeading(session.assistant ? nil : {
                    HStack(spacing: Theme.Space.xs) {
                        AgentMark(session)
                        if ultracodeMark { UltracodeMark().transition(.opacity) }
                    }
                    .animation(UltracodeMark.motion, value: ultracodeMark)
                })
                // Over the room left for it at the row's end (inside the row's inset), centered on the row's height.
            } else if let rowStyle {
                // The Agents list's chosen style (#2530): A avatar, B two lines, C state cards.
                styled(rowStyle, dense: dense)
            } else {
                HStack(alignment: .top, spacing: Theme.Space.m) {
                    // The agent's mark before its state (#6390): what runs there, then how it is doing.
                    if dot {
                        HStack(spacing: Theme.Space.s) {
                            if !session.assistant { AgentMark(session) }
                            StatusDot(state: session.state, size: 9, waiting: session.waitsInBackground, justFinished: session.showsJustFinished(muted: muted))
                        }
                        .padding(.top, Theme.Space.xs)
                    }
                    // The words on the left, the time with its source under it on the right (#3043): the title
                    // starts at the row's edge, and the tag takes no room from it nor pushes the preview down. At the
                    // accessibility text sizes the time goes under the words (`TimeBesideOrUnder`, #1658).
                    TimeBesideOrUnder {
                        VStack(alignment: .leading, spacing: Theme.Space.xs) {
                            // Ultracode's mark right after the title's last word while it is on (#1307).
                            UltracodeMark.after(rowTitle, on: ultracodeMark)
                                .font(session.state == .unread ? Theme.Font.bodyHeavy : Theme.Font.bodyStrong)
                                .lineLimit(2)
                                .fixedSize(horizontal: false, vertical: true)
                                .frame(maxWidth: .infinity, alignment: .leading)
                                .animation(UltracodeMark.motion, value: ultracodeMark)
                            if let last = rowPreview {
                                Text(last).font(Theme.Font.secondary).lineHeight(.secondary).foregroundStyle(.secondary).lineLimit(2)
                            }
                            if let sideUnread { SideUnreadLine(parent: session.key, summary: sideUnread) }
                            // A workflow running in the background (#2935): its name, phase and progress.
                            if !session.runningWorkflows.isEmpty { WorkflowLine(runs: session.runningWorkflows) }
                            RunningChildrenCaption(session: session)
                            HStack(spacing: Theme.Space.xs) {
                                metaText.lineLimit(1)
                                if let todo = session.todo {
                                    ViewThatFits(in: .horizontal) {
                                        Chip(text: todo.title, symbol: Theme.Icon.todo, look: .outline)
                                        ThemedIcon(Theme.Icon.todo)
                                            .font(Theme.Font.caption)
                                            .foregroundStyle(.secondary)
                                            .help(todo.title)
                                    }
                                }
                                if muted {
                                    ThemedIcon(Theme.Icon.muted).font(Theme.Font.label).accessibilityLabel("Notifications muted")
                                }
                            }
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                            .padding(.top, 1)
                        }
                    } time: {
                        VStack(alignment: .trailing, spacing: Theme.Space.xs) {
                            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.sm) {
                                AgentTimeText(time: time, fallback: session.updatedAt, font: Theme.Font.label)
                            }
                            sourceLabel
                        }
                    }
                }
                .padding(.vertical, Theme.Space.xs)
            }
        }
        .opacity(muted ? 0.8 : 1) // quieter than live agents, never fainter than the idle spaces below
    }

    /// Claude Code's ultracode is on (#1307): its mark beside the title; not on an offline row.
    var ultracodeMark: Bool { Ultracode.rowMark(session) }

    /// Where it was opened, at the start of the row's place line; not for the assistant's own conversation.
    @MainActor @ViewBuilder var sourceLabel: some View {
        if let source = rowSource { SourceLabel(source: source, link: session.externalLink) }
    }

    var pendingCount: Bool {
        false
    }

    /// Awaiting Reply spelled out (#5096), as Needs You is: a seen agent's gray dot does not say it waits on the user.
    /// Not for a muted one (it folds with the idle ones), nor where the section already says it.
    var showsReviewWord: Bool {
        !hideState && (session.awaitsYourReply(muted: muted) || session.showsJustFinished(muted: muted))
    }
    /// Waiting in the Background spelled out (#6045): its clock alone is a small mark. Not where the section
    /// already says it waits.
    var waitingWord: Bool { session.waitsInBackground && !hideState }

    /// Persona' own row's line: "Needs You · Claude · app › tests · MacBook": the end gives way when the line is short.
    /// Something typed or quoted in its composer and not sent (#4578), as Telegram's and Signal's lists say.
    @MainActor var hasDraft: Bool { AppModel.shared.draftMarks.contains(session.key) }

    @MainActor private var metaText: Text {
        var parts: [Text] = []
        if session.state == .needsYou && !muted && !hideState && !pendingCount {
            parts.append(Text(session.state.label).fontWeight(.semibold).foregroundStyle(session.state.tone.text))
        }
        if showsReviewWord {
            parts.append(Text(session.stateLabel(muted: muted)).fontWeight(.semibold).foregroundStyle(session.stateTone(muted: muted).text))
        }
        if waitingWord { parts.append(Text(session.stateLabel).fontWeight(.semibold).foregroundStyle(StatusPalette.backgroundWaiting.text)) }
        if hasDraft { parts.append(Text("Draft").fontWeight(.semibold).foregroundStyle(Theme.needsYou)) }
        if let titleDetail { parts.append(Text(titleDetail)) }
        parts.append(Text(pane?.name ?? session.sourceSubtitle ?? session.agentLabel))
        if pane?.launcher == true { parts.append(Text("Started It").fontWeight(.semibold)) }
        if let host { parts.append(Text(host)) }
        let slot = (pane == nil && primaryTab == nil) ? session.slotLabel : nil
        if let space, !space.isEmpty {
            parts.append(Text(slot.map { space + " › " + $0 } ?? space))
        } else if let slot {
            parts.append(Text(slot))
        }
        if let machine { parts.append(Text(machine)) }
        return parts.dropFirst().reduce(parts[0]) { $0 + Text(" · ") + $1 }
    }

    /// Awaiting Reply and Waiting in the Background (`states`), and Draft, spelled out as the rows before did.
    @MainActor private func stateParts(states: Bool) -> [Text] {
        var parts: [Text] = []
        if states && showsReviewWord {
            parts.append(Text(session.stateLabel(muted: muted)).fontWeight(.semibold).foregroundStyle(session.stateTone(muted: muted).text))
        }
        if states && waitingWord { parts.append(Text(session.stateLabel).fontWeight(.semibold).foregroundStyle(StatusPalette.backgroundWaiting.text)) }
        if hasDraft { parts.append(Text("Draft").fontWeight(.semibold).foregroundStyle(Theme.needsYou)) }
        return parts
    }

    /// Style B's words before the latest line: Awaiting Reply, Waiting in the Background, Draft; nil for none.
    @MainActor var stateWords: Text? {
        let parts = stateParts(states: true)
        guard let first = parts.first else { return nil }
        return parts.dropFirst().reduce(first) { $0 + Text(" · ") + $1 }
    }

    /// Something for the place line to say (style A leaves the line out without it).
    @MainActor var hasPlaceLine: Bool {
        rowSource != nil || !placeParts.isEmpty || !stateParts(states: true).isEmpty || session.todo != nil || muted
    }

    /// The row styles' place line (#2530): Awaiting Reply, Waiting in the Background (`states`: unless the row says its
    /// state itself) and Draft spelled out, then the tab (a pane's name, a source's subtitle: not the agent's kind, which
    /// its mark shows), the host, space › tab, the machine. Needs You is the row's own pill or word. The end gives way
    /// when the line is short.
    @MainActor func placeText(states: Bool = true) -> Text {
        var parts = stateParts(states: states)
        if let titleDetail { parts.append(Text(titleDetail)) }
        if let name = pane?.name ?? session.sourceSubtitle {
            parts.append(Text(name))
            if pane?.launcher == true { parts.append(Text("Started It").fontWeight(.semibold)) }
        }
        if let host { parts.append(Text(host)) }
        let slot = (pane == nil && primaryTab == nil) ? session.slotLabel : nil
        if let space, !space.isEmpty {
            parts.append(Text(slot.map { space + " › " + $0 } ?? space))
        } else if let slot {
            parts.append(Text(slot))
        }
        if let machine { parts.append(Text(machine)) }
        guard let first = parts.first else { return Text(verbatim: "") }
        return parts.dropFirst().reduce(first) { $0 + Text(" · ") + $1 }
    }

    /// The place line in words (style B's second line when there is nothing said yet).
    @MainActor var placeString: String { placeParts.joined(separator: " · ") }

    /// The tab, the host, space › tab and the machine, as the row styles name them.
    private var placeParts: [String] {
        var parts: [String] = []
        if let titleDetail { parts.append(titleDetail) }
        if let name = pane?.name ?? session.sourceSubtitle { parts.append(name) }
        if let host { parts.append(host) }
        let slot = (pane == nil && primaryTab == nil) ? session.slotLabel : nil
        if let space, !space.isEmpty {
            parts.append(slot.map { space + " › " + $0 } ?? space)
        } else if let slot {
            parts.append(slot)
        }
        if let machine { parts.append(machine) }
        return parts
    }

    /// "Claude · app › tests · MacBook": the end gives way when the line is short.
    @MainActor var meta: String {
        var parts = hasDraft ? [String(localized: "Draft")] : []
        if let titleDetail { parts.append(titleDetail) }
        parts.append(pane?.name ?? session.sourceSubtitle ?? session.agentLabel)
        if pane?.launcher == true { parts.append(String(localized: "Started It")) }
        if let host { parts.append(host) }
        let slot = (pane == nil && primaryTab == nil) ? session.slotLabel : nil
        if let space, !space.isEmpty {
            parts.append(slot.map { space + " › " + $0 } ?? space)
        } else if let slot {
            parts.append(slot)
        }
        if let machine { parts.append(machine) }
        if let todo = session.todo { parts.append(String(localized: "To-do: \(todo.title)")) }
        return parts.joined(separator: " · ")
    }
}

/// A quiet repository's narrow reader: opening a chat redraws its row, not the Agents list's body (P5), and the
/// projection's membership dictionary makes the selection an O(1) lookup (P4).
private struct QuietRepositorySelection<Content: View>: View {
    @Environment(AppModel.self) private var model
    var group: String
    var membership: [SessionKey: String]
    var expandedQuiet: Set<String>
    var forceExpanded: Bool
    var enabled: Bool
    @ViewBuilder var content: (Bool) -> Content

    var body: some View {
        let open = enabled ? model.visibleSession : nil
        let selected = AgentListProjection.selectedQuietRepositoryID(open: open, membership: membership,
            expandedQuiet: expandedQuiet, forceExpanded: forceExpanded) == group
        content(selected)
    }
}

/// When an agent last moved (`updatedAt`), followed apart from the rest of it: a working agent's moves on every step,
/// and only its row's time shows it (docs/apple/ios-state.md「Step 2」).
@Observable final class AgentTime: Equatable {
    var updatedAt: Date?
    init(_ updatedAt: Date?) { self.updatedAt = updatedAt }
    nonisolated static func == (a: AgentTime, b: AgentTime) -> Bool { a === b }
}

/// A row's 「3 分钟前」, kept current every 30 s: the one view a step of a working agent draws again (`AgentTime`).
struct AgentTimeText: View {
    let time: AgentTime?
    let fallback: Date?
    let font: Font

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("AgentTimeText")
        #endif
        let at = time?.updatedAt ?? fallback
        // No time: nothing, not an empty line the source label under it would sit below (#3043).
        if let at, at.isMeaningful {
            VisibleTimelineView(interval: 30) { now in
                Text(Fmt.relative(at, now: now)).font(font).foregroundStyle(.secondary).lineLimit(1)
            }
            .fixedSize()
        }
    }
}

/// Compact List: one line per agent — state dot, title and where (the tab, or space › tab when the section is not the space),
/// when on the right (alone there, so the times line up). `CompactList`'s scale.
nonisolated struct CompactSlotRow: View, Equatable {
    var session: Session
    var pendingTitle: String? = nil
    var muted = false
    /// As SlotRow's: the section already says the state (By Status).
    var hideState = false
    var space: String? = nil
    /// As SlotRow's `host`.
    var host: String? = nil
    var time: AgentTime? = nil
    /// As SlotRow's `pane`.
    var pane: TabPanes.Role? = nil
    /// Unread folded side session(s) keeping this parent row Unread (#13524); nil leaves the row unchanged.
    var sideUnread: SideFold.UnreadSummary? = nil
    /// Hub setting `use_herdr_tab_name`: promotes the herdr tab name to main title and moves the CLI title to detail (#13899).
    var preferHerdrTabName = false

    var rowTitle: String { pendingTitle ?? session.displayTitle(preferTab: preferHerdrTabName) }

    @MainActor
    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("CompactSlotRow")
        #endif
        Group {
            if let sideUnread {
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    mainLine
                    SideUnreadLine(parent: session.key, summary: sideUnread)
                }
            } else {
                mainLine
            }
        }
        .opacity(muted ? 0.8 : 1)
    }

    @MainActor
    private var mainLine: some View {
        HStack(spacing: CompactList.gap) {
            // The agent's mark before its state (#6390).
            if !session.assistant { AgentMark(session) }
            Group {
                if AppStyle.current.usesBeautifulUI {
                    AgentStateBadge(state: session.state, waiting: session.waitsInBackground, justFinished: session.showsJustFinished(muted: muted)).scaleEffect(0.8)
                } else {
                    StatusDot(state: session.state, size: Theme.Size.dot, waiting: session.waitsInBackground, justFinished: session.showsJustFinished(muted: muted))
                }
            }
            .scaledFrame(width: CompactList.mark)
            HStack(alignment: .firstTextBaseline, spacing: CompactList.gap) {
                Text(rowTitle)
                    .font(session.state == .unread ? CompactList.titleUnread : CompactList.title)
                    .lineLimit(1)
                    .layoutPriority(1)
                // Claude Code's ultracode on (#1307): its mark right after the title, never cut.
                if Ultracode.rowMark(session) { UltracodeMark(font: CompactList.meta).fixedSize().transition(.opacity) }
                // The starter mark stays whole: the line's other words give way to the title.
                if pane?.launcher == true { Text("Started It").font(CompactList.metaStrong).foregroundStyle(.secondary).lineLimit(1).fixedSize() }
                if let place, !place.isEmpty {
                    // Tail: "photo-sy…" still says where; the head cut to "…to-sync › #1".
                    Text(place).font(CompactList.meta).foregroundStyle(.secondary).lineLimit(1)
                }
                if let todo = session.todo {
                    ViewThatFits(in: .horizontal) {
                        Text(todo.title).font(CompactList.meta).foregroundStyle(Theme.accent).lineLimit(1)
                        ThemedIcon(Theme.Icon.todo).font(CompactList.meta).foregroundStyle(Theme.accent).help(todo.title)
                    }
                }
            }
            .animation(UltracodeMark.motion, value: Ultracode.rowMark(session))
            Spacer(minLength: Theme.Space.xs)
            // Awaiting Reply in words (#5096): the dot is a seen agent's gray.
            if !hideState && (session.awaitsYourReply(muted: muted) || session.showsJustFinished(muted: muted)) {
                Text(session.stateLabel(muted: muted)).font(CompactList.metaStrong).foregroundStyle(session.stateTone(muted: muted).text).lineLimit(1).fixedSize()
            }
            if muted { ThemedIcon(Theme.Icon.muted).font(CompactList.meta).foregroundStyle(.secondary).accessibilityLabel("Notifications muted") }
            AgentTimeText(time: time, fallback: session.updatedAt, font: CompactList.meta.monospacedDigit())
        }
    }

    private var pendingCount: Bool {
        false
    }

    @MainActor var place: String? {
        let primaryTab = session.displayTabName(preferTab: preferHerdrTabName)
        let titleDetail = session.displayTitleDetail(preferTab: preferHerdrTabName)
        let slot = (pane == nil && primaryTab == nil) ? session.slotLabel : nil
        let location = space.flatMap { $0.isEmpty ? nil : $0 }.map { name in slot.map { name + " › " + $0 } ?? name } ?? slot
        // The starter mark stands in for its agent's name (one line has no room for both; the web's phone strip does so).
        let name = pane.map { $0.launcher ? nil : $0.name } ?? session.sourceSubtitle
        let parts = [titleDetail, name, host, location].compactMap { $0 }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }
}


/// The idle agents of a busy space folded into one line: count plus their tab names.
struct IdleFoldRow: View {
    var sessions: [Session]

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.idleFold)
                .foregroundStyle(.secondary)
            Text("\(sessions.count) more idle")
                .font(Theme.Font.secondaryMedium)
                .foregroundStyle(.secondary)
                .lineLimit(1)
                .fixedSize()
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: Theme.Space.xs) {
                    ForEach(sessions.prefix(6)) { s in Chip(text: s.slotLabel ?? s.displayTitle) }
                }
            }
            .allowsHitTesting(false)
            .mask(LinearGradient(colors: [.black, .black, .clear], startPoint: .leading, endPoint: .trailing))
        }
        .padding(.vertical, Theme.Space.xs)
        .contentShape(.rect)
        // One element: on the stack alone the label went to each of its three children (read out three times, #405).
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("Show \(sessions.count) idle agents")
    }
}

/// A space where every agent is idle: one compact line, expandable.
struct QuietSpaceRow: View {
    var group: SpaceGroup
    var machine: Machine?
    var showMachine: Bool
    /// A repository (By Repository's Idle Repositories), not a space: its folder.
    var repo = false
    @Environment(AgentsFeed.self) private var feed: AgentsFeed?

    var body: some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(repo ? Theme.Icon.folder : Theme.Icon.space).foregroundStyle(.secondary)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                group.headingText(Theme.Font.heading).lineLimit(1)
                Text([showMachine ? machine?.title : nil, String(localized: "\(group.sessions.count) agents"),
                      group.sessions.compactMap(\.slotLabel).prefix(3).joined(separator: " · ")]
                    .compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · "))
                    .font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
            }
            Spacer()
            Text(Fmt.relative(group.updatedAt(feed))).font(Theme.Font.label).foregroundStyle(.secondary)
        }
    }
}

/// An explicit Agents-tab selection (`AppModel.agentsListReveal`), read in a view of its own: `ChatsView` only acts on it.
/// Read in the page's body, every choice of the Agents tab ran both tabs' pages again, the Persona conversation and each
/// of its messages with them (#10303).
private struct AgentsRevealWatch: View {
    @Environment(AppModel.self) private var model
    let reveal: (Int) -> Void

    var body: some View {
        Color.clear
            .allowsHitTesting(false)
            .accessibilityHidden(true)
            .onChange(of: model.agentsListReveal) { _, request in reveal(request) }
    }
}

/// The capsules (the BeautifulUI style): a plain list, every row on the page's edges (the grouped list's own top
/// padding left about 100 pt under the filter chips); otherwise the grouped list.
/// The Sidebar layout's bookkeeping on the Agents page (#6332): the Persona row is shaded while the page beside the
/// sidebar is the conversation itself, a place's while it is that place; the app's tab says which side is in front (a
/// relaunch goes back to an Agent's chat that was open, and the foot's Search starts on the Agents then, #4206).
private struct UnifiedFront: ViewModifier {
    @Environment(AppModel.self) private var model
    let unified: UnifiedSidebar?
    let atRoot: Bool

    func body(content: Content) -> some View {
        if let unified {
            // The tab follows what is in front in the Sidebar layout; in the Separate layout's tabs the tab bar sets it.
            let follows = unified.part == .whole
            // What is shaded follows the page's root (`UnifiedSidebar.atRoot`, `shownPlace`, #8359).
            content
                .onChange(of: atRoot) { _, root in
                    if root, follows, model.selectedTab != .assistant { model.selectedTab = .assistant }
                }
                .onChange(of: model.visibleSession) { _, key in
                    // An Agent's chat in front (not one of Persona's): the tab is Agents'.
                    guard let key, model.session(key)?.assistant != true, !atRoot else { return }
                    if follows, model.selectedTab != .chats { model.selectedTab = .chats }
                }
        } else {
            content
        }
    }
}

/// The Agents' Needs You heading and rows in the Sidebar layout (#6332): whether any is on screen and which way they
/// went, for the foot's jump (`SidebarNeedsJump`). Nothing elsewhere.
private struct NeedsAnchor: ViewModifier {
    let unified: UnifiedSidebar?

    func body(content: Content) -> some View {
        if let unified {
            content
                .onAppear { unified.needsAppeared() }
                .onDisappear { unified.needsGone() }
                .onGeometryChange(for: CGFloat.self) { $0.frame(in: .scrollView).minY } action: { unified.needsMoved($0) }
        } else {
            content
        }
    }
}

private struct AgentsListStyle: ViewModifier {
    let capsules: Bool
    /// The shell's room over the list on a phone (`AgentsHeader.room`), not searching.
    var room = false
    /// A phone: the top is `AgentsHeader`'s.
    var ownsTop = false

    func body(content: Content) -> some View {
        if capsules { roomBar(content.listStyle(.plain)) } else { roomBar(content.listStyle(.insetGrouped)) }
    }

    /// The shell's bar is taller than the navigation bar under it: room over the list for the rest of it, as a bar of
    /// the list's own, so the list blurs under it as under the navigation bar. On the list itself (on a view around
    /// it, the blur did not come); a clear bar gets no blur either, so the page's own color, all but transparent.
    @ViewBuilder private func roomBar(_ list: some View) -> some View {
        list.safeAreaBarWhenSupported(edge: .top, spacing: 0) {
            if room { AgentsHeader.roomFill.frame(height: Theme.Shell.agentsRoom).allowsHitTesting(false).accessibilityHidden(true) }
        }
        // As under the system's drawer: a hard edge, there only once rows are under the bars (not the field folded
        // away under them, `AgentsHeader.rowsUnder`).
        .modifier(AgentsEdge(room: room, ownsTop: ownsTop))
    }
}

/// The list's edge under the bars on a phone (`AgentsListStyle.roomBar`).
private struct AgentsEdge: ViewModifier {
    var room: Bool
    var ownsTop: Bool
    @Environment(AgentsHeader.self) private var header

    func body(content: Content) -> some View {
        if ownsTop {
            content
                #if os(iOS)
                .scrollEdgeEffectHardWhenSupported(room, for: .top)
                #endif
                .scrollEdgeEffectHiddenWhenSupported(!header.rowsUnder, for: .top)
        } else {
            content
        }
    }
}

/// The capsules' plain list over the grouped page's color (the color the grouped list had); otherwise the style's page.
private struct AgentsListPage: ViewModifier {
    let capsules: Bool
    var workspaceSidebar = false
    /// The Sidebar layout's column, beside the page or in the phone's drawer: its one color behind the list whatever the
    /// style (`UnifiedSidebar.columnColor`, #4206).
    var column: Color? = nil

    func body(content: Content) -> some View {
        if workspaceSidebar { content.listStyle(.plain).scrollContentBackground(.hidden).background(column ?? Theme.WorkspaceSidebar.background) }
        else if capsules { content.scrollContentBackground(.hidden).background(column ?? Theme.groupedPage) }
        else if let column { content.scrollContentBackground(.hidden).background(column) }
        else { content.themedPage() }
    }
}

/// A quiet space's card title (`quietSpaceCard`, the BeautifulUI style): the space, where and how many, when, and the
/// chevron that turns as it opens; the card's top when open, the whole card when closed.
struct QuietSpaceCardHead: View {
    var group: SpaceGroup
    var machine: Machine?
    var showMachine: Bool
    var open: Bool
    /// A repository (By Repository's Idle Repositories), not a space: its folder.
    var repo = false
    var selected = false
    @Environment(AgentsFeed.self) private var feed: AgentsFeed?

    var body: some View {
        TaskRow(TaskItem(id: group.id, title: group.name,
                         subtitle: [group.projectFolder?.qualifier, showMachine ? machine?.title : nil, String(localized: "\(group.sessions.count) agents"),
                                    group.sessions.compactMap(\.slotLabel).prefix(3).joined(separator: " · ")]
                            .compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · "),
                         status: .pending(step: 0)),
                shape: .list(first: true, last: !open), accessory: .none, titleLines: 1, subtitleLines: 1) {
            ThemedIcon(repo ? Theme.Icon.folder : Theme.Icon.space).font(Theme.Font.body).foregroundStyle(.secondary)
                .scaledFrame(width: Theme.Size.icon)
        } trailing: {
            HStack(spacing: Theme.Space.s) {
                Text(Fmt.relative(group.updatedAt(feed))).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                ThemedIcon(Theme.Icon.disclosure).font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
                    .rotationEffect(.degrees(open ? 90 : 0))
            }
        } detail: {
            EmptyView()
        }
        .accessibilityElement(children: .combine)
        .accessibilityValue(open ? String(localized: "Expanded") : String(localized: "Collapsed [Readiness Card ee6046]"))
        .accessibilityAddTraits(selected ? .isSelected : [])
    }
}

/// A session that is no longer running (resumable by sending a message).
/// Used by the machine detail page.
struct SessionRow: View {
    @Environment(AppModel.self) private var model
    var session: Session
    var machine: Machine?
    var showMachine = true
    var preferHerdrTabName = false

    var body: some View {
        HStack(alignment: .top, spacing: Theme.Space.m) {
            // The avatar's color is the state: no dot on its corner nor beside the title (2026-10-31, three said it once).
            SessionAvatar(machine: machine, state: session.state, dot: false)
            SlotRow(session: session, pendingTitle: model.pendingAgentTitle(session.key), dot: false, preferHerdrTabName: preferHerdrTabName).equatable()
        }
    }
}


/// Pin / mute a space: the ⋯ on its header in Develop, the menu of its page, the long press on a folded row.
struct SpaceMenuItems: View {
    @Environment(AppModel.self) private var model
    var group: SpaceGroup
    var rename: (() -> Void)? = nil
    /// Open Space (the Agents list: its header folds the space instead).
    var open: (() -> Void)? = nil
    var linkTodo: (() -> Void)? = nil

    var body: some View {
        if let open {
            Button(action: open) { Label("Open Space", systemImage: Theme.Icon.openSpace) }
        }
        let unread = group.sessions.filter { $0.needsSeen(muted: model.isMuted($0)) }.map(\.key)
        if !unread.isEmpty {
            // All of the space's rows: the unread ones are marked, and the ones read during the visit and held in place
            // leave with them (#12514), as the Just finished header's does.
            Button { model.markSeen(group.sessions.map(\.key)) } label: {
                Label(StateName.markAllSeen(unread.count), systemImage: Theme.Icon.success)
            }
        }
        if let rename, group.spaceID != "-" {
            Button(action: rename) { Label("Rename Space", systemImage: Theme.Icon.edit) }
        }
        if let linkTodo {
            SpaceTodoMenu(group: group, link: linkTodo)
        }
        Button { model.togglePin(group) } label: {
            Label(model.isPinned(group) ? String(localized: "Unpin Space") : String(localized: "Pin Space"), systemImage: model.isPinned(group) ? Theme.Icon.unpin : Theme.Icon.pin)
        }
        Button { model.toggleMute(group) } label: {
            Label(model.isMuted(group) ? String(localized: "Unmute Space") : String(localized: "Mute Space"), systemImage: model.isMuted(group) ? Theme.Icon.unmute : Theme.Icon.mute)
        }
    }
}

extension View {
    /// The rename prompt for a space (a herdr workspace, a Tern session): one name.
    func renameSpaceAlert(_ group: Binding<SpaceGroup?>) -> some View { modifier(RenameSpaceAlert(group: group)) }
    /// The rename prompt for an agent: its herdr or Tern tab and its Claude session name.
    func renameSessionAlert(_ session: Binding<Session?>) -> some View { modifier(RenameSessionAlert(session: session)) }
}

private struct RenameSpaceAlert: ViewModifier {
    @Environment(AppModel.self) private var model
    @Binding var group: SpaceGroup?
    @State private var name = ""

    func body(content: Content) -> some View {
        content
            .alert("Rename Space", isPresented: Binding(get: { group != nil }, set: { if !$0 { group = nil } })) {
                TextField(String(localized: "Space name"), text: $name)
                Button("Cancel", role: .cancel) {}
                Button("Save") {
                    if let g = group { Task { await model.renameSpace(g, to: name) } }
                }
            } message: {
                Text(SpaceRenameForm.note(group))
            }
            .onChange(of: group?.id) { _, _ in name = group?.name ?? "" }
    }
}

private struct RenameSessionAlert: ViewModifier {
    @Environment(AppModel.self) private var model
    @Binding var session: Session?
    @State private var tab = ""
    @State private var title = ""

    private var unified: Bool {
        guard let session else { return false }
        return SessionRenameForm.isUnified(session, preferHerdrTabName: model.prefersHerdrTabName(for: session))
    }

    func body(content: Content) -> some View {
        content
            .alert("Rename [Chats View 0d0cba]", isPresented: Binding(get: { session != nil }, set: { if !$0 { session = nil } })) {
                if unified {
                    TextField(SessionRenameForm.unifiedField, text: $tab)
                } else {
                    if let session, SessionRenameForm.showsTab(session) { TextField(SessionRenameForm.tabField(session), text: $tab) }
                    TextField(String(localized: "Session name"), text: $title)
                }
                Button("Cancel", role: .cancel) {}
                Button("Save") {
                    guard let s = session else { return }
                    let body = SessionRenameForm.body(s, tab: tab, title: title, unified: unified)
                    guard body.tabName != nil || body.title != nil else { return }
                    Task { await model.renameSession(s.key, tab: body.tabName, title: body.title) }
                }
            } message: {
                if let session {
                    if unified {
                        Text(SessionRenameForm.unifiedNote)
                    } else if SessionRenameForm.showsTab(session) {
                        Text(SessionRenameForm.tabNote(session))
                    }
                }
            }
            .onChange(of: session?.id) { _, _ in
                tab = session?.slotName ?? ""
                title = session?.title ?? ""
            }
    }
}


/// A machine's section header in Develop grouped by machine: its name, whether it is online, state counts.
struct MachineSectionHeader: View {
    var machine: Machine
    var group: SpaceGroup
    @Environment(\.compactList) private var compact

    var body: some View {
        CountsHeading {
            ThemedIcon(machine.symbol).foregroundStyle(machine.online ? Theme.accent : .secondary)
            if compact {
                Text(machine.title).font(CompactList.sectionTitle).foregroundStyle(Color.primary).textCase(nil).lineLimit(1)
            } else {
                Text(machine.title).font(Theme.Font.sectionHeader).foregroundStyle(.secondary).textCase(nil).lineLimit(1)
            }
            if !machine.online {
                Text("Offline [Machine Entity be1b4f]").font(Theme.Font.labelStrong).foregroundStyle(.secondary).textCase(nil)
            }
        } counts: {
            StateCounts(group: group)
        }
    }
}

/// The pages the Agents list opens, in either layout (`SplitNavigation`).
private struct AgentsDestinations: ViewModifier {
    @Binding var path: NavigationPath
    /// The Sidebar layout (#6332): the Persona page's places (Inbox, To-dos, Machines, extension pages) open on the same
    /// path, registered here with the Agents' own; nil elsewhere. `path` stays present in both layouts for task lineage.
    var unifiedPath: Binding<NavigationPath>? = nil

    func body(content: Content) -> some View {
        #if os(iOS)
        if let unifiedPath {
            agents(content).modifier(ShellDestinations(path: unifiedPath, enabled: true, agentRoutes: false))
        } else {
            agents(content)
        }
        #else
        agents(content)
        #endif
    }

    private func agents(_ content: Content) -> some View {
        content
            .navigationDestination(for: SessionKey.self) { key in ChatView(key: key).id(key).splitDetailPage(ownsBack: true) }
            .navigationDestination(for: SpaceRoute.self) { r in SpaceView(spaceKey: r.id).splitDetailPage() }
            .navigationDestination(for: MachineRoute.self) { r in MachineSpacesView(ref: r.ref).splitDetailPage() }
            .navigationDestination(for: MachineListRoute.self) { _ in MachinesView().splitDetailPage() }
            .navigationDestination(for: TerminalsRoute.self) { r in TerminalsView(only: r.machine).splitDetailPage() }
            .navigationDestination(for: MachineDetailRoute.self) { r in MachineDetailView(ref: r.ref).splitDetailPage() }
            .navigationDestination(for: MachineUsageMapRoute.self) { r in UsageMapView(route: r).splitDetailPage() }
            .navigationDestination(for: PersonalAgentsRoute.self) { _ in PersonalAgentListView().splitDetailPage() }
            .navigationDestination(for: PersonalAgentSession.self) { s in PersonalAgentSessionView(session: s).id(s.id).splitDetailPage() }
            .navigationDestination(for: TodoDetailRoute.self) { r in
                TodoDetailView(todoID: r.id, openDetail: { (route: TodoDetailRoute) in path.append(route) }).splitDetailPage()
            }
    }
}

/// Where the list's top comes from. A phone: `AgentsHeader`, which follows the list's scroll, starts it folded and
/// folds it again when the page comes back to the front. Beside the split: the system's search drawer, folded by
/// `SearchCloseWatcher`.
private struct AgentsTop: ViewModifier {
    var ownsTop: Bool
    let searchClose: SearchCloseWatcher
    var retainsQuery: Bool
    @Environment(AgentsHeader.self) private var header

    func body(content: Content) -> some View {
        if ownsTop {
            content
                .background { AgentsHeaderProbe(header: header) }
                .onChange(of: retainsQuery, initial: true) { wasRetained, retained in
                    header.retainsQuery = retained
                    // A retained drag never settles. Clearing it must finish the ordinary empty-field settle even when
                    // the finger already lifted and no further scroll-phase callback is coming.
                    if wasRetained, !retained, !header.floating { DispatchQueue.main.async { header.settle() } }
                }
                .onAppear { header.foldOnce() } // the search field starts folded away; a pull shows it
                // Back in front (its tab picked again, the shell's Agents): folded again too (#740). Not on the way
                // back from a chat, which keeps the list as it was: that changes the view's appearance, not the tab.
                .background { PageFrontWatcher(initial: false) { [header] active in if active { header.refold() } } }
                .onScrollGeometryChange(for: CGFloat.self) { $0.contentOffset.y + $0.contentInsets.top } action: { [header] _, y in
                    header.scrolled = y
                    let under = y > (header.compact ? 0 : AgentsHeader.band) + 0.5
                    if header.rowsUnder != under { header.rowsUnder = under }
                }
                // Where UIKit draws the list (its top inset, the navigation bar's), whenever its frame changes.
                .onGeometryChange(for: CGRect.self) { $0.frame(in: .global) } action: { [header] _ in
                    DispatchQueue.main.async { header.measureListTop() }
                }
        } else {
            system(content)
        }
    }

    @ViewBuilder private func system(_ content: Content) -> some View {
        content
            .background { SearchCloseProbe(watcher: searchClose) }
            .onAppear { searchClose.foldOnce() } // the search field starts folded away; a pull shows it
            // From a view of its own: reading `tabIsActive` here drew the whole list again each time the page came to
            // the front or went behind (the Mac keeps it in the window behind other sections, #390).
            .background { PageFrontWatcher(initial: false) { [searchClose] active in if active { searchClose.refold() } } }
    }
}

/// What the Agents list does when it goes out of sight or comes back, read apart from the list (docs/apple/performance.md
/// P5): the shell's other page in front, another tab, a page pushed over it. `AgentsList`'s body read the app's tab, the
/// strip's `moving` and the chat on screen for this, each of which changes on a switch between Assistant and Agents, and so
/// ran three or four times per switch, over every row (#5438). The list holds its feed while covered, the herdr and
/// worktree polls stop, the chats not open are no longer warmed (#10582), and a chat coming on screen closes the
/// workspace opened beside the list.
private struct AgentsCoverWatch: View {
    @Environment(AppModel.self) private var model
    @Environment(\.shell) private var shell
    @Environment(\.unifiedSidebar) private var unified
    @Environment(\.splitDetail) private var split
    let pushedOver: Bool
    let herdr: HerdrWatch
    let herdrKey: [String]
    let catalog: WorkspaceCatalog
    let catalogEnabled: Bool
    let covered: (Bool) -> Void
    let chatShown: () -> Void
    /// This list's place among the Agents lists in sight (`RuntimeSessionLists.listInSight`, #9487).
    @State private var sightID = UUID()

    /// The shell's slide between the pages shows the list again.
    private var isCovered: Bool {
        // The Sidebar layout's list is the sidebar, in sight beside whatever it opened (#6332); on a phone, while the
        // drawer is out.
        #if os(iOS)
        if let unified { return split?.drawer == true && !unified.drawer.isOut }
        #else
        if unified != nil { return false }
        #endif
        if pushedOver { return true }
        if model.selectedTab != .chats && shell?.pager.moving != true { return true }
        return false
    }

    /// Whether the Agents' chats not open are warmed for a tap in this list (`AgentSync.warmWhileInSight`, #10582): in
    /// sight, and beside the page not folded away (the iPad's column, which `isCovered` leaves to the list's own polls).
    private func warms(covered: Bool) -> Bool {
        #if os(iOS)
        if split?.listHidden == true && split?.drawer != true { return false }
        #endif
        return !covered
    }

    var body: some View {
        let isCovered = isCovered
        let warms = warms(covered: isCovered)
        Color.clear
            .allowsHitTesting(false)
            .accessibilityHidden(true)
            // Whether each machine's herdr runs, and the worktrees, while the list is in sight.
            .modifier(HerdrPolling(watch: herdr, key: herdrKey, covered: isCovered))
            .modifier(WorkspaceCatalogPolling(catalog: catalog, enabled: catalogEnabled, covered: isCovered))
            // The chats a tap here opens are kept current while the list is in sight, its tab in front, the app active.
            .tabActiveTask(id: warms) { if warms { await AgentSync.shared.warmWhileInSight() } }
            .onChange(of: isCovered, initial: true) { _, now in
                covered(now)
                // Out of sight, the hub's lists read for hints once a window; in sight, as they come.
                model.sessionLists.listInSight(sightID, !now)
            }
            .onDisappear { model.sessionLists.listInSight(sightID, false) }
            .onChange(of: model.visibleSession) { _, key in if key != nil { chatShown() } }
    }
}

/// The system's search beside the split and on the Mac (in the window's toolbar there); a phone has its own
/// (`AgentsHeader`). The search field folds away as the list scrolls and comes back on a pull (2026-11-01 user
/// request: the always-shown field crowded the top).
private struct AgentsSystemSearch: ViewModifier {
    var enabled: Bool
    @Binding var query: String
    @Binding var searching: Bool
    #if os(iOS)
    /// What the search reads (#7443): another scope than Full Text is a token in the field, as on a phone
    /// (`AgentsSearchField`); deleting it searches Full Text again.
    @StoredValue(AgentSearchScope.key) private var scope: AgentSearchScope = .all
    private var tokens: Binding<[AgentSearchScope]> {
        Binding(get: { scope == .all ? [] : [scope] }, set: { scope = $0.last ?? .all })
    }
    #endif

    func body(content: Content) -> some View {
        if enabled {
            #if os(iOS)
            content.pageSearchable(text: $query, tokens: tokens, isPresented: $searching,
                                   placement: .navigationBarDrawer(displayMode: .automatic),
                                   prompt: AgentsSearchPrompt.text(scope)) { s in
                Label(s.title, systemImage: s.symbol)
            }
            #else
            content.pageSearchable(text: $query, isPresented: $searching, placement: .navigationBarDrawer(displayMode: .automatic),
                                   prompt: AgentsSearchPrompt.text)
            #endif
        } else {
            content
        }
    }
}

enum AgentsSearchPrompt {
    static var text: String { String(localized: "Search spaces, tabs, sessions, content") }

    /// The field's prompt under a scope (#7443): Full Text's names all it reads; another scope is the token before it.
    static func text(_ scope: AgentSearchScope) -> String { scope == .all ? text : String(localized: "Search") }

    /// What the field searches, spoken (the field at rest is one element: its token is not read on its own).
    static func spoken(_ scope: AgentSearchScope) -> String {
        scope == .all ? text : String(localized: "Search in: \(scope.title)")
    }
}

/// A bar above a list in a stack (the split's sidebar column). Otherwise nothing: on a phone the bar is the list's
/// first row, so the folding search field leaves no gap above it.
/// `bar` is made only when it shows: on a phone it is not, and making it counted the agents again on every push.
private struct TopBar<Bar: View>: ViewModifier {
    var abovePage: Bool
    @ViewBuilder var bar: () -> Bar

    func body(content: Content) -> some View {
        if abovePage {
            VStack(spacing: 0) {
                bar()
                content
            }
            .background(Theme.pageBackground)
        } else {
            content
        }
    }
}


/// Runs something once the system search has really closed. Closing it (`isPresented = false`) only starts the fold:
/// a push made while it still folds away is dropped by the navigation stack, which keeps the path it was given (the
/// chat in the path, the list on screen). A fixed wait from the tap was too short whenever the list took a while to
/// redraw for the closed search (SearchOpenTests, 2026-10-31).
@MainActor final class SearchCloseWatcher {
    fileprivate weak var view: UIView?

    func whenClosed(_ done: @escaping () -> Void) {
        let start = Date.now
        func check() {
            let busy = searchController.map { $0.isActive || $0.isBeingDismissed || $0.transitionCoordinator != nil } ?? false
            if !busy || Date.now.timeIntervalSince(start) > 2 {
                done()
            } else {
                DispatchQueue.main.asyncAfter(deadline: .now() + 0.03, execute: check)
            }
        }
        DispatchQueue.main.async(execute: check)
    }

    private var folded = false

    /// Starts the page with the search field folded away (2026-11-01 user request: it shows on a pull, like Mail).
    /// SwiftUI has no switch for that; the drawer (`.automatic`) folds once the list is scrolled by its height, so
    /// the list is moved down by the search bar's height once, while it still sits at its very top. Tried again
    /// for a moment until the list and the bar have been laid out; never again after (coming back from a chat
    /// keeps where the list was).
    func foldOnce() {
        guard !folded else { return }
        var tries = 0
        func attempt() {
            tries += 1
            guard !folded else { return }
            if let sc = searchController, !sc.isActive, let list = listScrollView,
               list.contentSize.height > 0, sc.searchBar.bounds.height > 0 {
                let bar = sc.searchBar.bounds.height
                // A list shorter than the screen cannot scroll, and the drawer only folds on a scroll (checked on a
                // device with three sessions: it sprang back open): give it just enough room at the end to scroll by
                // the bar's height; nothing when the list is long enough.
                let room = list.bounds.height - list.adjustedContentInset.top - list.adjustedContentInset.bottom
                let short = room + bar - list.contentSize.height
                if short > 0 { list.contentInset.bottom += short }
                let top = -list.adjustedContentInset.top
                if abs(list.contentOffset.y - top) < 1 {
                    // x kept as it is: the list's side margins live in its content offset.
                    list.setContentOffset(CGPoint(x: list.contentOffset.x, y: top + bar), animated: false)
                    settleInitialFold(list, search: sc, initialInset: -top, requestedOffset: top + bar, barHeight: bar)
                }
                folded = true
                return
            }
            if tries < 20 {
                DispatchQueue.main.asyncAfter(deadline: .now() + .milliseconds(50), execute: attempt)
            }
        }
        DispatchQueue.main.async(execute: attempt)
    }

    /// UIKit shrinks the navigation inset after the drawer folds, and can add that same delta to the offset we
    /// just set (-168 -> -116 -> -64 on iOS 27). That scrolls the first row under the navigation buttons. Once the
    /// inset settles, align the first row with its new top, only if UIKit made that exact extra adjustment and no
    /// user scroll or search has taken over. Other UIKit versions that keep the requested offset need no correction.
    private func settleInitialFold(_ list: UIScrollView, search: UISearchController, initialInset: CGFloat,
                                   requestedOffset: CGFloat, barHeight: CGFloat, attempts: Int = 20) {
        guard !search.isActive, !list.isTracking, !list.isDragging, !list.isDecelerating else { return }
        let inset = list.adjustedContentInset.top
        if initialInset - inset >= barHeight - 1 {
            let adjustedOffset = requestedOffset + initialInset - inset
            if abs(list.contentOffset.y - adjustedOffset) < 1 {
                list.setContentOffset(CGPoint(x: list.contentOffset.x, y: -inset), animated: false)
                return
            }
        }
        // Inset and offset changes can arrive on separate layout turns; keep the same bounded, exact-match check.
        if attempts > 0 {
            DispatchQueue.main.asyncAfter(deadline: .now() + .milliseconds(50)) {
                self.settleInitialFold(list, search: search, initialInset: initialInset,
                                       requestedOffset: requestedOffset, barHeight: barHeight, attempts: attempts - 1)
            }
        }
    }

    private weak var foundList: UIScrollView?

    /// The page came back to the front: it starts with the search folded away again, as on its first appearance, when
    /// the list still sits at its very top with the field out and not in use (#740). Scrolled down, or searching,
    /// nothing moves. A short list's added bottom room is counted in its inset already, so none is added twice.
    func refold() {
        folded = false
        foldOnce()
    }

    /// The list's scroll view: the largest collection view in the page the probe is in (List is one). Kept once found
    /// while it is in a window (`ReaderAnchor` asks on every push).
    var listScrollView: UIScrollView? {
        if let l = foundList, l.window != nil { return l }
        foundList = findListScrollView
        return foundList
    }

    private var findListScrollView: UIScrollView? {
        var r: UIResponder? = view
        while let next = r?.next {
            if let vc = next as? UIViewController {
                return Self.largestScrollView(in: vc.view)
            }
            r = next
        }
        return nil
    }

    private static func largestScrollView(in root: UIView?) -> UIScrollView? {
        guard let root else { return nil }
        var best: UIScrollView?
        var stack = [root]
        while let v = stack.popLast() {
            if let c = v as? UICollectionView, !c.isHidden,
               c.bounds.height > (best?.bounds.height ?? 0) { best = c }
            stack.append(contentsOf: v.subviews)
        }
        return best
    }

    /// The search controller SwiftUI put on the navigation item of the page the list is in.
    private var searchController: UISearchController? {
        var r: UIResponder? = view
        while let next = r?.next {
            if let vc = next as? UIViewController {
                var c: UIViewController? = vc
                while let v = c {
                    if let sc = v.navigationItem.searchController { return sc }
                    c = v.parent
                }
                return vc.navigationController?.topViewController?.navigationItem.searchController
            }
            r = next
        }
        return nil
    }
}

/// Runs something once the navigation stack the probe is in has stopped moving: no push or pop on its way, no back
/// swipe under the finger or still settling after it was let go. A path set meanwhile was taken by SwiftUI and lost by
/// the stack (#1941). Still now: at once (a launch, a notification tapped on the list), so nothing waits a turn.
@MainActor final class StackStillWatcher {
    fileprivate weak var view: UIView?
    private static let log = Logger(subsystem: "persona", category: "stack")

    func whenStill(_ done: @escaping () -> Void) {
        let start = Date.now
        func check() {
            if navigation?.transitionCoordinator == nil || Date.now.timeIntervalSince(start) > 2 {
                let waited = Int(Date.now.timeIntervalSince(start) * 1000)
                if waited > 0 { Self.log.notice("an open waited \(waited) ms for the stack (found: \(self.navigation != nil))") }
                done()
            } else {
                DispatchQueue.main.asyncAfter(deadline: .now() + 0.03, execute: check)
            }
        }
        check()
    }

    /// The stack the list is the root of: the navigation controller of the first view controller up from the probe.
    private var navigation: UINavigationController? {
        var r: UIResponder? = view
        while let next = r?.next {
            if let vc = next as? UIViewController { return vc.navigationController }
            r = next
        }
        return nil
    }
}

/// The Agents list's place while a launch opens a chat over it (#8467): nothing drawn. The list comes in as soon as it
/// could be seen: the root back in the window (a back swipe starting, ‹ tapped: before the pop's first frame), or the
/// path empty with no chat on its way (`ChatsView.listWaits`).
private struct AgentsListPending: View {
    let build: () -> Void

    var body: some View {
        Color.clear.background { WindowEntryProbe(entered: build) }
    }
}

/// Calls `entered` when its view goes into a window again after its first time: a stack's root page is taken out of
/// the window under what is pushed over it and put back as a pop or a back swipe starts.
private struct WindowEntryProbe: UIViewRepresentable {
    let entered: () -> Void

    final class Probe: UIView {
        var entered: () -> Void = {}
        private var entries = 0
        override func didMoveToWindow() {
            super.didMoveToWindow()
            guard window != nil else { return }
            entries += 1
            if entries > 1 { entered() }
        }
    }

    func makeUIView(context: Context) -> Probe {
        let v = Probe()
        v.isUserInteractionEnabled = false
        v.entered = entered
        return v
    }

    func updateUIView(_ uiView: Probe, context: Context) { uiView.entered = entered }
}

/// Where `StackStillWatcher` finds the stack from.
private struct StackStillProbe: UIViewRepresentable {
    let watcher: StackStillWatcher

    func makeUIView(context: Context) -> UIView {
        let v = UIView()
        v.isUserInteractionEnabled = false
        watcher.view = v
        return v
    }

    func updateUIView(_ uiView: UIView, context: Context) { watcher.view = uiView }
}

/// Where `SearchCloseWatcher` finds the page's search controller from.
private struct SearchCloseProbe: UIViewRepresentable {
    let watcher: SearchCloseWatcher

    func makeUIView(context: Context) -> UIView {
        let v = UIView()
        v.isUserInteractionEnabled = false
        watcher.view = v
        return v
    }

    func updateUIView(_ uiView: UIView, context: Context) { watcher.view = uiView }
}

/// The Agents page's top bar in the shell (docs/apple/ios.md「New shell」): the same face and switch as Assistant's, in the same places,
/// over the navigation bar (kept for its search field), New Session on the right; tapping the selected Agents opens the list's
/// options (Group By, Display, Machines…).
struct AgentsTopBar: View, Equatable {
    @Environment(AppModel.self) private var model
    /// iPhone Duo's bars down the side: the bar is the side rail there (`ShellSideRail`).
    @Environment(\.shellRailEdge) private var railEdge
    let shell: ShellState
    var menuActions: AgentsListMenu.Actions
    var newSession: () -> Void // sets the page's state: the same each time

    /// The bar holds the list's options menu: built again with the page (a new `newSession` each time), an open menu
    /// went back to its top (see `ShellSwitch`).
    nonisolated static func == (a: Self, b: Self) -> Bool { a.shell === b.shell && a.menuActions === b.menuActions }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("AgentsTopBar")
        #endif
        @Bindable var model = model
        if let railEdge {
            ShellSideRail(edge: railEdge) {
                ShellPersonaButton(id: "work-sidebar-button") { shell.drawer.open() }
                ShellCircleButton(symbol: Theme.Icon.compose, label: String(localized: "New Session [Chats View 58e21b]"), id: "agents-new", action: newSession)
            } switcher: {
                ShellSwitch(tab: $model.selectedTab, axis: .vertical) { EmptyView() } workMenu: {
                    AgentsListMenu(actions: menuActions, inline: true)
                }
            }
            .shellTopBarScaled()
            .accessibilityElement(children: .contain)
            .accessibilityIdentifier("agents-top-bar")
            .modifier(ShellBarAtRest(page: 1)) // ShellMovingBar stands in while the strip slides
        } else {
            row($model.selectedTab)
        }
    }

    private func row(_ tab: Binding<AppTab>) -> some View {
        HStack(alignment: .top, spacing: Theme.Space.s) {
            ShellPersonaButton(id: "work-sidebar-button") { shell.drawer.open() }
            Spacer(minLength: 0)
            ShellCircleButton(symbol: Theme.Icon.compose, label: String(localized: "New Session [Chats View 58e21b]"), id: "agents-new", action: newSession)
        }
        .overlay(alignment: .top) {
            ShellSwitch(tab: tab) { EmptyView() } workMenu: {
                AgentsListMenu(actions: menuActions, inline: true)
            }
        }
        .padding(.horizontal, Theme.Space.l)
        .padding(.top, Theme.Space.xxs)
        .shellTopBarScaled()
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("agents-top-bar") // ShellSearchTests: nothing of it over the search field
        .modifier(ShellBarAtRest(page: 1)) // ShellMovingBar stands in while the strip slides
    }
}

/// Agents' Needs your input: every card but the assistant's, on the machine and hub picked. A card on no session (`personad
/// ask` outside herdr) or on one not listed yet is here too, as the tab's badge counts it (#4059).
enum AgentsNeedsYou {
    static func cards(_ all: [Interaction], session: (SessionKey) -> Session?, machine: String = "", hub: String? = nil) -> [Interaction] {
        all.filter { ix in
            session(ix.sessionKey)?.assistant != true && session(ix.sessionKey)?.hiddenByDirectory != true && (machine.isEmpty || ix.machineRef.path == machine)
                && (hub.map { HubFilter.matches($0, ix.group) } ?? true)
        }
    }
}

#if os(iOS)
/// The Separate layout's tabs at regular width (#8596): the system's tab bar floats across the top, and the split already
/// makes its room in its columns (64 pt on an iPad). The tab's own top safe area held that room a second time for a tab
/// first shown after a switch, until UIKit let it go: it did for the Persona tab within a moment, never for the Agents
/// tab, whose column head sat 64 pt lower. This takes the part of the tab's top safe area beyond the window root's own
/// (the status bar's, and the room the return-to-call bar takes there, #9290) back off the tab's hosting controller, so
/// the room is made once, by the split, on both tabs; where UIKit let it go already there is nothing to take off.
private struct TabBarRoomOnce: UIViewRepresentable {
    func makeUIView(context: Context) -> Reader {
        let v = Reader()
        v.isUserInteractionEnabled = false
        return v
    }

    func updateUIView(_ v: Reader, context: Context) { v.apply() }

    static func dismantleUIView(_ v: Reader, coordinator: ()) { v.release() }

    final class Reader: UIView {
        /// The tab's hosting controller this took room off, to give it back when the split goes.
        private weak var tab: UIViewController?

        override func didMoveToWindow() {
            super.didMoveToWindow()
            apply()
        }

        override func safeAreaInsetsDidChange() {
            super.safeAreaInsetsDidChange()
            apply()
        }

        override func layoutSubviews() {
            super.layoutSubviews()
            apply()
        }

        func apply() {
            guard let window, let split = sequence(first: self as UIResponder, next: { $0.next })
                .compactMap({ $0 as? UISplitViewController }).first,
                  let tab = split.parent, tab.parent is UITabBarController else { return }
            self.tab = tab
            // The top the tab's controller is given before this one's own change, against the window root's top: the
            // window's own, and the call bar's room the root adds (`CallBarRoom`), which every page keeps.
            let given = tab.view.safeAreaInsets.top - tab.additionalSafeAreaInsets.top
            let rootTop = window.safeAreaInsets.top + (window.rootViewController?.additionalSafeAreaInsets.top ?? 0)
            let extra = TabBarExtraRoom.extra(given: given, rootTop: rootTop)
            if abs(tab.additionalSafeAreaInsets.top + extra) > 0.5 { tab.additionalSafeAreaInsets.top = -extra }
        }

        func release() {
            if let tab, tab.additionalSafeAreaInsets.top != 0 { tab.additionalSafeAreaInsets.top = 0 }
        }
    }
}

/// The tab bar's second room (`TabBarRoomOnce`): what a tab's controller is given at the top beyond the window root's
/// top (the status bar, and the return-to-call bar's room the root adds: never taken back, #9290).
nonisolated enum TabBarExtraRoom {
    static func extra(given: CGFloat, rootTop: CGFloat) -> CGFloat { max(0, given - rootTop) }
}
#endif
