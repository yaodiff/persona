import SwiftUI

// The Agents list's menus depend on nothing that changes with each push from the hub: the list is drawn again on
// every one (sessions, machines), and a menu drawn again with it is handed to UIKit again — the open ⋯ menu jumped back
// to its top under the finger choosing a machine (2026-10-31), a row's long-press menu was rebuilt for every row on
// every push. The ⋯ menu sits in the toolbar of `ChatsView`, which does not follow the list (on the Mac in the shell's,
// `MacShell`: open, it stayed put through `/mock/churn` pushes and a machine could be picked, 2026-11-02); a row's is
// equal while what it offers is. What the actions need at tap time (the sections listed now, the session as it is now) comes from
// an object the views do not observe. `LiveUpdateTests` covers it.

/// A machine as the ⋯ menu shows it: not its live state (last seen, online), which changes with every push.
nonisolated struct MachineChoice: Equatable, Identifiable {
    /// `MachineRef.path`: the machine id, "group|id" for another group of hubs (what the filter remembers).
    var id: String
    /// With several groups, with the group's name (`Machine.title`).
    var name: String
    var symbol: String
    /// Whose it is, for the menus that list the current assistant's computers (`AssistantScope`).
    var group: String
    var manager: String?

    @MainActor init(_ m: Machine) {
        id = m.ref.path
        name = m.title
        symbol = m.symbol
        group = m.group
        manager = m.manager
    }
}

/// The menu at the top left of Agents: grouping, Cards / Compact List, folding, the machine filter, Machines. Shows the chosen
/// machine's name in the corner; "All" keeps the plain filter icon.
struct AgentsListMenu: View {
    @Environment(AppModel.self) private var model
    var actions: Actions

    /// Kept current by the list (`update`), read when an item is tapped. Only `showingHidden` is observed: the menu's
    /// reveal item names what a tap does next, and nothing else here redraws it (the menu stays out of each push's redraw).
    @Observable @MainActor final class Actions {
        @ObservationIgnored private(set) var sections: [String] = []
        @ObservationIgnored private var foldAll: ([String]) -> Void = { _ in }
        @ObservationIgnored private var unfoldAll: () -> Void = {}
        /// Not `openMonitor` / `openMachines`: a stored property's name is in the Release binary, and those are DEBUG
        /// launch arguments the release check looks for (TestFlight 50 stopped on one).
        @ObservationIgnored private var onMachines: () -> Void = {}
        @ObservationIgnored private var onTerminals: () -> Void = {}
        private(set) var showingHidden = false
        @ObservationIgnored private var toggleHidden: () -> Void = {}

        /// `sections`: the fold ids of what the list shows now.
        func update(sections: [String], foldAll: @escaping ([String]) -> Void, unfoldAll: @escaping () -> Void,
                    onMachines: @escaping () -> Void, onTerminals: @escaping () -> Void = {},
                    showingHidden: Bool = false, toggleHidden: @escaping () -> Void = {}) {
            self.sections = sections
            self.foldAll = foldAll
            self.unfoldAll = unfoldAll
            self.onMachines = onMachines
            self.onTerminals = onTerminals
            if self.showingHidden != showingHidden { self.showingHidden = showingHidden }
            self.toggleHidden = toggleHidden
        }

        func collapse() { foldAll(sections) }
        func expand() { unfoldAll() }
        func machinesPage() { onMachines() }
        func terminalsPage() { onTerminals() }
        func toggleHiddenSessions() { toggleHidden() }
    }

    @StoredValue("dev.machineFilter") private var machineFilter = ""
    @StoredValue(HubFilter.key) private var storedHubFilter = ""
    /// The group the list shows when narrowed: the current hub's (`HubFilter.shown`, #11657).
    private var hubFilter: String { HubFilter.shown(storedHubFilter, current: model.hub?.group) }
    @StoredValue("dev.grouping") private var grouping: DevGrouping = .standard
    /// The grouping the list shows: a stored By To-do is By Project on a hub without todos (`DevGrouping.resolved`).
    private var shownGrouping: DevGrouping { DevGrouping.resolvedNow(grouping) }
    private var groupingChoice: Binding<DevGrouping> { Binding(get: { shownGrouping }, set: { grouping = $0 }) }
    @StoredValue("dev.sort") private var sort: AgentSort = .state
    @StoredValue("dev.showEmptyWorkspaces") private var showEmptyWorkspaces = false
    #if os(iOS)
    /// The rows' style or Compact List (#2530), as Settings › Appearance › Layout sets it.
    private let display = AgentListDisplayChoice()
    /// What the search reads (#7443): the field names it while it is not Full Text (`AgentsSearchField`).
    @StoredValue(AgentSearchScope.key) private var searchScope: AgentSearchScope = .all
    /// The Sidebar layout (#6332): its one field searches Persona's chats and the Agents and cannot name a scope, so
    /// it searches Full Text and the menu does not offer Search In.
    @Environment(\.unifiedSidebar) private var unified
    #else
    @StoredValue("dev.compact") private var compact = false
    #endif

    /// The shell (docs/apple/ios.md「New shell」) shows these inside its Work switch's menu: the items, without a button of their own.
    var inline = false

    var body: some View {
        // Only the machines' names and icons: not their live state, which changes with every push. The current
        // assistant's, as the list is its agents.
        let scope = model.assistantScope
        let machines = model.machineChoices.filter { scope.has($0.manager, group: $0.group) }
        let chosen = machines.first { $0.id == machineFilter }
        let hub = model.isMultiHub ? HubFilter.group(hubFilter).map { model.groupName($0) } : nil
        if inline {
            items(machines)
        } else {
            Menu { items(machines) } label: { label(chosen, hub: hub) }
                .accessibilityLabel((hub.map { String(localized: "Current hub: \($0), ") } ?? "") + (chosen.map { String(localized: "Current machine: \($0.name), ") } ?? "")
                                    + String(localized: "Grouping: \(shownGrouping.title)"))
        }
    }

    @ViewBuilder private func items(_ machines: [MachineChoice]) -> some View {
            #if DEBUG || PERF_SIGNPOST
            let _ = PerfTrace.body("ListMenu.content")
            #endif
            Picker("Group By [Agents Menus 399ee6]", selection: groupingChoice) {
                ForEach(DevGrouping.offeredNow) { g in Label(g.title, systemImage: g.symbol).tag(g) }
            }
            if shownGrouping != .state, shownGrouping != .recent, shownGrouping != .app, shownGrouping != .direction {
                // By Status is sectioned by state, and the rows in a section are by time already; Recently Used is by time
                // only, as are By App's sections; By Pinned keeps the hub's order.
                Picker("Sort", selection: $sort) {
                    ForEach(AgentSort.allCases) { o in Label(o.title, systemImage: o.symbol).tag(o) }
                }
            }
            #if os(iOS)
            // Every grouping draws its agents this way, By Repository too (#3753).
            Picker("Display", selection: display.binding) {
                ForEach(AgentListDisplay.allCases) { d in Label(d.title, systemImage: d.symbol).tag(d) }
            }
            if unified == nil {
                // The web's Filter › Search in, its scopes in its order and words; Full Text is what an untouched app
                // searches. A submenu of its own whose row names the choice under it: inline, six choices with no
                // heading would read as the list's. The row's words are two Texts and an Image: a `Label` around them
                // drops the second line.
                Section {
                    Picker(selection: $searchScope) {
                        ForEach(AgentSearchScope.allCases) { s in Label(s.title, systemImage: s.symbol).tag(s) }
                    } label: {
                        Text("Search In [Agent Search Scope]")
                        Text(searchScope.title)
                        Theme.Icon.image(Theme.Icon.search)
                    }
                    .pickerStyle(.menu)
                }
            }
            #else
            // The Mac and Apple Vision Pro keep their rows (#2530): cards or Compact List.
            Picker("Display", selection: $compact) {
                Label("Cards", systemImage: Theme.Icon.cardView).tag(false)
                Label("Compact List [Agents Menus 06829c]", systemImage: Theme.Icon.listView).tag(true)
            }
            #endif
            if shownGrouping != .recent {
                Section {
                    Button {
                        withAnimation(Theme.Motion.standard) { actions.collapse() }
                    } label: { Label("Collapse All", systemImage: Theme.Icon.collapseAll) }
                    Button {
                        withAnimation(Theme.Motion.standard) { actions.expand() }
                    } label: { Label("Expand All", systemImage: Theme.Icon.expandAll) }
                }
            }
            if model.isMultiHub {
                // Several groups of hubs (docs/protocol/multi-hub.md「What the user sees」): one group's agents only. That
                // group is the current hub (#11657): picking it here makes it current, so Persona's sidebar, the Inbox and
                // the hub's settings show it too; All Hubs leaves the current hub as it is.
                Picker("hub", selection: Binding(get: { hubFilter }, set: { pick in
                    storedHubFilter = pick
                    machineFilter = ""
                    if let g = HubFilter.group(pick) { model.selectHub(g) }
                })) {
                    Label("All Hubs", systemImage: Theme.Icon.hubGroup).tag("")
                    ForEach(model.groupIDs, id: \.self) { g in
                        Label(model.groupName(g), systemImage: Theme.Icon.hubGroup).tag(HubFilter.value(g))
                    }
                }
            }
            if shownGrouping == .repo {
                Toggle(isOn: $showEmptyWorkspaces) {
                    Label("Show empty workspaces", systemImage: Theme.Icon.folder)
                }
            }
            Button(actions.showingHidden ? String(localized: "Hide directory matches") : String(localized: "Show hidden sessions")) {
                actions.toggleHiddenSessions()
            }.lineLimit(1)
            Picker("Machines", selection: $machineFilter) {
                Label("All Machines", systemImage: Theme.Icon.allMachines).tag("")
                ForEach(machines.filter { m in HubFilter.matches(hubFilter, MachineRef(path: m.id).group) }) { m in
                    Label(m.name, systemImage: m.symbol).tag(m.id)
                }
            }
            Section {
                Button { actions.machinesPage() } label: {
                    Label("Machines", systemImage: Theme.Icon.machines)
                }
                // Every terminal on the computers, plain ones too (#4754): watch, type, close.
                Button { actions.terminalsPage() } label: {
                    Label("Terminals", systemImage: Theme.Icon.terminal)
                }
            }
    }

    @ViewBuilder private func label(_ chosen: MachineChoice?, hub: String?) -> some View {
            if let m = chosen {
                HStack(spacing: Theme.Space.sm) {
                    ThemedIcon(m.symbol)
                    Text(m.name).lineLimit(1)
                    ThemedIcon(Theme.Icon.expand).font(Theme.Font.labelHeavy)
                }
                .font(Theme.Font.secondaryStrong)
                .fixedSize()
            } else if let hub {
                // One group of hubs shown (several groups): its name in the corner, like a machine's.
                HStack(spacing: Theme.Space.sm) {
                    ThemedIcon(Theme.Icon.hubGroup)
                    Text(hub).lineLimit(1)
                    ThemedIcon(Theme.Icon.expand).font(Theme.Font.labelHeavy)
                }
                .font(Theme.Font.secondaryStrong)
                .fixedSize()
            } else {
                ThemedIcon(shownGrouping == .standard ? Theme.Icon.capsuleGrouping : shownGrouping.symbol)
            }
    }
}

#if os(iOS)
/// The Sidebar layout's Agents heading's ⋯ (#10738): `AgentsListMenu` in a view graph of its own (`MenuIsland`). In the
/// sidebar's list the list's updates reached the menu several times a second while agents worked, and SwiftUI handed its
/// items to UIKit again each time: open and scrolled down, it jumped back to its top.
struct SidebarAgentsListMenu: View, Equatable {
    let actions: AgentsListMenu.Actions

    nonisolated static func == (a: Self, b: Self) -> Bool { a.actions === b.actions }

    var body: some View {
        AgentsListMenu(actions: actions)
            .accessibilityIdentifier("sidebar-agents-menu")
    }
}
#endif

#if !os(iOS)
/// A row's long-press menu in Agents. Equal while the facts it shows are: the row's session changes with every push
/// while its agent works (a new last line), and the menu held open on it was rebuilt each time.
struct AgentRowMenu: View, Equatable {
    @Environment(AppModel.self) private var model
    nonisolated let key: SessionKey
    nonisolated let pinned: Bool
    nonisolated let muted: Bool
    nonisolated let looping: Bool
    nonisolated let live: Bool
    nonisolated var terminal = true
    /// Rename… and Close Agent… are offered (`Session.canRename`, `canClose`): a terminal's, or where the node says so
    /// for a session without one (a runtime's, #6294).
    nonisolated var renamable = true
    nonisolated var closable = true
    nonisolated var attachable = false
    /// Delete Chat is offered (`Session.canDelete`).
    nonisolated var deletable = false
    /// The node menu's pane items (split, Git, close the tab): a live herdr pane (`NodeMenu.offersPaneActions`).
    nonisolated var paneActions = false
    /// Interrupt is offered: the agent works or waits for you, and can be stopped from here.
    nonisolated var interruptible = false
    /// Mark as Seen is offered: there is something new it said.
    nonisolated var unread = false
    nonisolated var takeoverAvailable = false
    nonisolated var continueAvailable = false
    /// The same handlers as iOS's `AgentActionMenu` (`ChatsView.rowActions`): the list's sheets and alerts.
    nonisolated let actions: AgentMenuHandlers

    nonisolated static func == (a: Self, b: Self) -> Bool {
        a.key == b.key && a.pinned == b.pinned && a.muted == b.muted && a.looping == b.looping && a.live == b.live
            && a.terminal == b.terminal && a.renamable == b.renamable && a.closable == b.closable
            && a.attachable == b.attachable && a.deletable == b.deletable
            && a.paneActions == b.paneActions && a.interruptible == b.interruptible && a.unread == b.unread
            && a.takeoverAvailable == b.takeoverAvailable && a.continueAvailable == b.continueAvailable && a.actions === b.actions
    }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("RowMenu.content")
        #endif
        // The session as it is when an item is tapped (all of it: Hand to Persona sends its last line).
        let now = { model.latest(key) }
        #if os(visionOS)
        VisionOpenInWindowButton(key: key)
        Divider()
        #endif
        Button { model.togglePin(key) } label: {
            Label(pinned ? String(localized: "Unpin [Personal Agent List View c92179]") : String(localized: "Pin [Personal Agent List View 173f88]"), systemImage: pinned ? Theme.Icon.unpin : Theme.Icon.pin)
        }
        if interruptible {
            Button { Task { await model.interrupt(key) } } label: { Label("Interrupt [Composer Buttons f9d193]", systemImage: Theme.Icon.interrupt) }
        }
        if unread {
            Button { model.markSeen(key) } label: { Label("Mark as Seen", systemImage: Theme.Icon.view) }
        }
        #if os(iOS)
        if takeoverAvailable {
            Button { if let s = now() { actions.takeover(s) } } label: { Label("Take over", systemImage: Theme.Icon.terminal) }
        }
        if continueAvailable {
            Button { if let s = now() { actions.continueInNewSession(s) } } label: { Label("Continue in a new session", systemImage: Theme.Icon.compose) }
        }
        #endif
        if renamable {
            Button { if let s = now() { actions.rename(s) } } label: {
                Label(String(localized: "agent.rename", defaultValue: "Rename Agent…"), systemImage: Theme.Icon.edit)
            }
        }
        Button { model.toggleMute(key) } label: {
            Label(muted ? String(localized: "Unmute [Chat View 3834f9]") : String(localized: "Mute"), systemImage: muted ? Theme.Icon.unmute : Theme.Icon.mute)
        }
        // TodoItem actions only for an Agent on the selected hub: the todos and their links live there.
        if TodoStore.shared.isSupported, let current = now(),
            TodosLogic.isOnTodosHub(sessionGroup: current.key.group, hubGroup: model.hub?.group) {
            if let todo = current.todo {
                // the same sheet moves it to another todo (the hub releases the old link, #2348)
                Button {
                    if let s = now() { actions.linkTodo(s) }
                } label: {
                    Label("Move to Another To-do…", systemImage: Theme.Icon.todo)
                }
                Button {
                    guard let s = now() else { return }
                    Task {
                        do {
                            try await TodoStore.shared.unlinkAgent(todoID: todo.id, machineID: s.machineID, sessionID: s.sessionID)
                        } catch {
                            AppModel.shared.show(error)
                        }
                    }
                } label: {
                    Label("Unlink", systemImage: Theme.Icon.link)
                }
            } else {
                Button {
                    if let s = now() { actions.linkTodo(s) }
                } label: {
                    Label("Link to To-do…", systemImage: Theme.Icon.todo)
                }
            }
        }
        if looping {
            Button(role: .destructive) { model.stopLoop(key) } label: { Label("Stop Loop", systemImage: Theme.Icon.stopLoop) }
        }
        Button {
            guard let s = now() else { return }
            Task { await model.askPersona(AgentHandoff.prompt(s, machine: model.machine(s.machineRef)?.name)) }
        } label: {
            Label("Hand to Persona", systemImage: Theme.Icon.ai)
        }
        Button { UIPasteboard.general.string = now()?.cwd } label: { Label("Copy Directory", systemImage: Theme.Icon.folder) }
        if attachable {
            Button { Task { await model.attach(key) } } label: { Label("Attach Live", systemImage: Theme.Icon.link).lineLimit(1) }
        }
        if terminal {
            Button {
                guard let s = now(), s.canTerminal else { return }
                UIPasteboard.general.string = "\(s.resumeCommand) \(s.sourceThreadID ?? s.sessionID)"
            } label: { Label("Copy Resume Command", systemImage: Theme.Icon.terminal).lineLimit(1) }
        }
        if paneActions {
            Section { AgentNodeMenuItems(now: now) }
        }
        if live && closable {
            Button(role: .destructive) { if let s = now() { actions.close(s) } } label: { Label("Close Agent…", systemImage: Theme.Icon.end) }
        }
        if deletable {
            Button(role: .destructive) {
                if let s = now(), s.canDelete { actions.delete(s) }
            } label: { Label("Delete Chat…", systemImage: Theme.Icon.delete).lineLimit(1) }
        }
    }
}

extension AgentRowMenu {
    /// The menu of a row's agent, its long-press menu (on iOS the only way to it since the row's ⋯ went, #2530). With
    /// the rows' swipes gone there (#1244), Interrupt and Mark as Seen are here too.
    @MainActor init(_ s: Session, model: AppModel, actions: AgentMenuHandlers) {
        self.init(key: s.key, pinned: model.isPinned(s.key), muted: model.isMuted(s), looping: s.loop != nil, live: s.state.isLive,
                  terminal: s.canTerminal, renamable: s.canRename, closable: s.canClose, attachable: s.canAttach, deletable: s.canDelete,
                  paneActions: NodeMenu.offersPaneActions(s),
                  interruptible: (s.state == .working || s.state == .needsYou) && s.canInterrupt, unread: s.needsSeen(muted: model.isMuted(s)),
                  takeoverAvailable: s.isCodex && !s.canSend && s.capabilities?.takeover?.canResume == true,
                  continueAvailable: s.isCodex && !s.canSend && s.capabilities?.takeover != nil,
                  actions: actions)
    }
}

#else
/// iOS aliases the shared definition used by the detail More menu too.
typealias AgentRowMenu = AgentActionMenu
#endif

/// The ⋯ on a space's header in Agents: `SpaceMenuItems`, equal while what it offers is (the space, its unread agents,
/// whether it can be renamed); the group's other fields change with every push.
struct SpaceHeaderMenu: View, Equatable {
    nonisolated let id: String
    nonisolated let unread: [SessionKey]
    /// The node menu's space items (run an agent here, Git): a herdr workspace (`NodeMenu.offersSpaceActions`).
    nonisolated let nodeActions: Bool
    var group: SpaceGroup
    var rename: () -> Void
    var open: () -> Void
    var linkTodo: (() -> Void)?

    init(group: SpaceGroup, isMuted: (Session) -> Bool, rename: @escaping () -> Void, open: @escaping () -> Void, linkTodo: (() -> Void)? = nil) {
        id = group.id + "|" + group.spaceID + "|" + group.name
        unread = group.sessions.filter { $0.needsSeen(muted: isMuted($0)) }.map(\.key)
        nodeActions = NodeMenu.offersSpaceActions(group)
        self.group = group
        self.rename = rename
        self.open = open
        self.linkTodo = linkTodo
    }

    nonisolated static func == (a: Self, b: Self) -> Bool { a.id == b.id && a.unread == b.unread && a.nodeActions == b.nodeActions }

    var body: some View {
        SpaceMenuItems(group: group, rename: rename, open: open, linkTodo: linkTodo)
        if nodeActions {
            Section { SpaceNodeMenuItems(group: group) }
        }
    }
}

/// The grouping, one tap away under the quick filters (#5766), in a cell without the search input. The same stored
/// choice as the ⋯ menu's (`dev.grouping`), kept across launches. Reads nothing that a hub push changes. Equal while
/// it is drawn the same way: in the list it sits in a `MenuIsland` (#10738), as the list's updates replaced it while open.
struct AgentsGroupingMenu: View, Equatable {
    var iconOnly = false

    nonisolated static func == (a: Self, b: Self) -> Bool { a.iconOnly == b.iconOnly }
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @StoredValue("dev.grouping") private var grouping: DevGrouping = .standard
    private var shown: DevGrouping { DevGrouping.resolvedNow(grouping) }

    var body: some View {
        Menu {
            Picker("Group By [Agents Menus 399ee6]", selection: Binding(get: { shown }, set: { g in
                Haptics.tap()
                withAnimation(reduceMotion ? nil : Theme.Motion.layout) { grouping = g }
            })) {
                ForEach(DevGrouping.offeredNow) { g in
                    Label(g.title, systemImage: g.symbol).tag(g)
                }
            }
        } label: {
            if iconOnly {
                ThemedIcon(Theme.Icon.capsuleGrouping)
                    .font(Theme.Font.secondaryMedium)
                    .foregroundStyle(Color.primary)
                    .tapTargetFrame()
                    .contentShape(.rect)
            } else {
                HStack(spacing: Theme.Space.xs) {
                    ThemedIcon(shown.symbol)
                    Text(shown.title)
                    ThemedIcon(Theme.Icon.choices).font(Theme.Font.label)
                }
                .agentsPill()
            }
        }
        .labelOnlyMenu()
        // Keep the Menu's activation independent of surrounding List content.
        .buttonStyle(.plain)
        .accessibilityIdentifier("agents.grouping")
        .accessibilityLabel(String(localized: "Grouping: \(shown.title)"))
    }
}

/// The personal agents page as one of the list's top-level groups (#140, #16769): on a phone the shell has no other way
/// there (the iPad and Mac use the toolbar's button). Drawn as a computer's heading is (`HostHeading`): the mark in a
/// tile, the name a level above the headers under it, how many there are, and a forward chevron, since it opens a page
/// rather than folding. A lone capsule under the quick filters read as left over once the grouping joined the filter bar.
struct AgentsPersonalHeading: View {
    /// How many personal agent conversations there are; nil while unknown.
    var count: Int?
    var open: () -> Void
    @Environment(\.compactList) private var compact

    var body: some View {
        Button {
            Haptics.tap()
            open()
        } label: {
            HStack(spacing: Theme.Space.sm) {
                CountsHeading {
                    ThemedIcon(Theme.Icon.personalAgents)
                        .font(Theme.Font.secondaryStrong)
                        .foregroundStyle(Color.primary)
                        .scaledFrame(width: Theme.Size.wellXS, height: Theme.Size.wellXS)
                        .background(Theme.Fill.chip, in: .rect(cornerRadius: Theme.Radius.badge))
                    Text("Personal Agents")
                        .font(compact ? Theme.Font.title : Theme.Font.heading)
                        .foregroundStyle(Color.primary)
                        .lineLimit(1)
                } counts: {
                    if let count {
                        Text("\(count)")
                            .font((compact ? CompactList.metaStrong : Theme.Font.labelHeavy).monospacedDigit())
                            .foregroundStyle(Color.secondary)
                            .contentTransition(.numericText(value: Double(count)))
                    }
                }
                Spacer(minLength: Theme.Space.xs)
                ThemedIcon(Theme.Icon.forward)
                    .font(Theme.Font.labelHeavy)
                    .foregroundStyle(.tertiary)
            }
            // A finger's height, the room above its words, as a computer's heading (`FoldButton`).
            .modifier(TouchHeight(alignment: .bottom))
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(String(localized: "Personal Agents"))
        .accessibilityValue(count.map { String(localized: "\($0) agents") } ?? "")
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("agents.personal")
    }
}

private extension View {
    /// The look of the grouping's pill under the quick filters.
    func agentsPill() -> some View {
        font(Theme.Font.secondaryMedium)
            .lineLimit(1)
            .foregroundStyle(Color.primary)
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.s)
            .background(Capsule().fill(Theme.Fill.chip))
            .contentShape(Capsule())
    }
}

extension AgentSearchScope {
    /// The web's words for each (`lib-agent-filter.json`, `scope_*`), in Title Case as Apple menus write them.
    var title: String {
        switch self {
        case .all: String(localized: "Full Text [Agent Search Scope]")
        case .title: String(localized: "Title [Agent Search Scope]")
        case .conversation: String(localized: "Without Tools [Agent Search Scope]")
        case .user: String(localized: "User [Agent Search Scope]")
        case .assistant: String(localized: "Assistant [Agent Search Scope]")
        case .errors: String(localized: "Errors [Agent Search Scope]")
        }
    }

    var symbol: String {
        switch self {
        case .all: Theme.Icon.search
        case .title: Theme.Icon.sessionTitle
        case .conversation: Theme.Icon.conversation
        case .user: Theme.Icon.user
        case .assistant: Theme.Icon.ai
        case .errors: Theme.Icon.error
        }
    }
}
