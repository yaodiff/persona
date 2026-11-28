import SwiftUI

/// A page or sheet an Agent's action opens. A list row routes to its Agent's chat with this request;
/// the same selection in the chat acts on the page already open instead of pushing another chat.
nonisolated enum AgentMenuDestination: Hashable, Sendable {
    case terminal(resume: Bool)
    case pane(SessionPane)
    case review
    case preview
    case subagents
    case sideSessions
    case space
}

/// A pending route lives only in memory and is consumed by the destination chat.
@MainActor @Observable
final class AgentMenuRouting {
    static let shared = AgentMenuRouting()
    let store = AgentGitStatusStore()
    var requests: [SessionKey: AgentMenuDestination] = [:]
    var linkingSession: Session?
    func open(_ destination: AgentMenuDestination, at key: SessionKey) { requests[key] = destination }
    func take(_ key: SessionKey) -> AgentMenuDestination? { requests.removeValue(forKey: key) }
    func gitStatus(_ session: Session) -> GitStatus? {
        store.status(for: .init(session: session.key, cwd: session.cwd))
    }
}

/// What the two Agent menus show. Its equality is deliberately limited to facts that change the menu:
/// a new reply or elapsed working time does not rebuild a menu held open over the Agents list.
nonisolated struct AgentActionFacts: Equatable, Sendable {
    var key: SessionKey
    var session: Session
    var pinned: Bool
    var muted: Bool
    var panes: [SessionPane]
    var paneBadges: [SessionPane: String]
    var currentPane: SessionPane
    var review: Bool
    var preview: Bool
    var screen: Bool
    var todoHub: Bool
    var sideCount: Int
    var hosted: Bool
    var showsSpace: Bool
    var offersTakeover: Bool
    var offersContinuation: Bool
    var terminalResume: TerminalResume
    var autoRename: Bool
    var phone: Bool
    var paneActions: Bool
    /// Move To…'s choices (#11516, `DirectionFilings.offers`): none, no Move To… (no `direction_sessions`, another
    /// manager's page, an Agent that follows its supervised task).
    var moveChoices: [DirectionFilings.Choice]

    /// From the model: only `AgentActionMenuReader` may, so what the facts read is observed by the menu alone, never
    /// by a row or a header that holds it (#10231). Elsewhere this does not compile.
    @MainActor
    fileprivate init(session: Session, model: AppModel, currentPane: SessionPane = .chat, hosted: Bool = false,
                     showsSpace: Bool = true, sides: SideSessions? = nil) {
        let moveChoices = DirectionFilings.shared.offers(session.key, in: model)
        self.init(session: session,
                  pinned: model.isPinned(session.key), muted: model.isMuted(session.key),
                  panes: AgentMenuFacts.panes(session, git: AgentMenuRouting.shared.gitStatus(session)),
                  paneBadges: AgentMenuFacts.paneBadges(AgentMenuRouting.shared.gitStatus(session)),
                  currentPane: currentPane, review: model.features(session.group).contains("git_review") && !session.cwd.isEmpty,
                  preview: !model.isDemo && model.features(session.group).contains(PreviewLogic.feature) && !session.cwd.isEmpty,
                  screen: model.liveScreen?.canShow(session.key.machineRef) ?? false,
                  todoHub: TodoStore.shared.isSupported && TodosLogic.isOnTodosHub(sessionGroup: session.group, hubGroup: model.hub?.group),
                  sideCount: (sides ?? (hosted ? SideSessions() : model.sides(of: session.key))).rows.count,
                  hosted: hosted, showsSpace: showsSpace,
                  terminalResume: session.status == .offline || session.status == .unknown
                    ? session.terminalResume(on: model.machine(session.key.machineRef)) : .hidden,
                  autoRename: model.features(session.group).contains(APIClient.agentAutonameFeature),
                  phone: AgentMenuFacts.supportsTakeover, moveChoices: moveChoices)
    }

    /// Test and preview construction needs no live hub. Production uses the model initializer above.
    init(session: Session, pinned: Bool = false, muted: Bool = false, panes: [SessionPane]? = nil,
         paneBadges: [SessionPane: String] = [:], currentPane: SessionPane = .chat, review: Bool = false,
         preview: Bool = false, screen: Bool = false, todoHub: Bool = false, sideCount: Int = 0,
         hosted: Bool = false, showsSpace: Bool = true, terminalResume: TerminalResume = .hidden,
         autoRename: Bool = false, phone: Bool = false, git: GitStatus? = nil, moveChoices: [DirectionFilings.Choice] = []) {
        key = session.key
        self.session = session
        self.pinned = pinned
        self.muted = muted
        self.panes = panes ?? AgentMenuFacts.panes(session, git: git)
        self.paneBadges = paneBadges.isEmpty ? AgentMenuFacts.paneBadges(git) : paneBadges
        self.currentPane = currentPane
        self.review = review
        self.preview = preview
        self.screen = screen
        self.todoHub = todoHub
        self.sideCount = sideCount
        self.hosted = hosted
        self.showsSpace = showsSpace
        offersTakeover = session.isCodex && !session.canSend && session.capabilities?.takeover?.canResume == true
        offersContinuation = session.isCodex && !session.canSend && session.capabilities?.takeover != nil
        self.terminalResume = terminalResume
        self.autoRename = autoRename && !session.assistant
        self.phone = phone
        paneActions = NodeMenu.offersPaneActions(session)
        self.moveChoices = moveChoices
    }

    nonisolated static func == (a: Self, b: Self) -> Bool {
        a.key == b.key && a.pinned == b.pinned && a.muted == b.muted && a.panes == b.panes
            && a.paneBadges == b.paneBadges && a.currentPane == b.currentPane && a.review == b.review
            && a.preview == b.preview && a.screen == b.screen && a.todoHub == b.todoHub && a.sideCount == b.sideCount
            && a.hosted == b.hosted && a.showsSpace == b.showsSpace && a.offersTakeover == b.offersTakeover
            && a.offersContinuation == b.offersContinuation && a.terminalResume == b.terminalResume && a.phone == b.phone && a.autoRename == b.autoRename
            && a.session.state == b.session.state && a.session.status == b.session.status
            && a.session.capabilities == b.session.capabilities
            && a.session.state == b.session.state && a.session.status == b.session.status
            && a.session.canTerminal == b.session.canTerminal && a.session.canRename == b.session.canRename
            && a.session.canClose == b.session.canClose && a.session.canAttach == b.session.canAttach
            && a.session.canDelete == b.session.canDelete && a.session.canInterrupt == b.session.canInterrupt
            && (a.session.loop != nil) == (b.session.loop != nil)
            && a.session.todo == b.session.todo
            && a.session.subagents?.count == b.session.subagents?.count
            && a.subagentCount == b.subagentCount
            && a.session.spaceID == b.session.spaceID
            && a.session.paneID == b.session.paneID && a.paneActions == b.paneActions
            && a.moveChoices == b.moveChoices
    }

    nonisolated var subagentCount: Int {
        ChildSessions.totalSubagentCount(session)
    }

    /// The complete ordered inventory, including the nested node actions as separate entries.
    /// This same sequence drives rendering, not just a source-text assertion.
    nonisolated var items: [AgentActionItem] {
        var out: [AgentActionItem] = []
        func add(_ id: AgentActionItem.ID, _ group: AgentActionItem.Group = .navigation) {
            out.append(AgentActionItem(id: id, group: group))
        }
        if currentPane != .chat { add(.backToChat) }
        for pane in panes where pane == .files || pane == .git { add(pane == .files ? .files : .git) }
        if review { add(.review) }
        if preview { add(.preview) }
        if !hosted, subagentCount > 0 { add(.subagents) }
        if sideCount > 0 { add(.sideSessions) }
        if showsSpace, session.spaceID != nil { add(.space) }
        if session.canAttach { add(.attach) }
        if session.state.isLive, session.canInterrupt { add(.interrupt) }
        if session.needsSeen(muted: muted) { add(.markSeen) }
        if phone {
            if offersTakeover { add(.takeover) }
            if offersContinuation { add(.continueInNewSession) }
        }
        if session.canRename { add(.rename) }
        if autoRename { add(.autoRename) }
        add(.pin, .session)
        add(.mute, .session)
        if todoHub {
            if session.todo != nil { add(.todo, .session); add(.moveTodo, .session); add(.unlinkTodo, .session) }
            else { add(.linkTodo, .session) }
        }
        // After the to-do's own entries, so Move To… reads apart from Move to Another To-do… (#11516).
        if !moveChoices.isEmpty { add(.moveTo, .session) }
        if session.loop != nil { add(.stopLoop, .session) }
        add(.handToPersona, .session)
        if screen { add(.screen, .session) }
        add(.copyDirectory, .utilities)
        if session.canTerminal {
            add(.terminal, .utilities)
            add(.copyResume, .utilities)
        }
        if terminalResume != .hidden { add(.resume, .utilities) }
        if paneActions {
            add(.splitPane, .node)
            for direction in NodeMenu.Direction.allCases { add(direction == .right ? .splitRight : .splitDown, .node) }
            add(.nodeGit, .node)
            for operation in NodeMenu.GitOp.allCases {
                add(operation == .pull ? .gitPull : operation == .push ? .gitPush : .gitFetch, .node)
            }
            add(.closeTab, .node)
        }
        if session.state.isLive, session.canClose { add(.close, .destructive) }
        if session.canDelete { add(.delete, .destructive) }
        return out
    }
}

enum AgentMenuFacts {
    /// Takeover and continuation are available on iOS (iPhone and iPad); retired native Mac stays excluded.
    nonisolated static var supportsTakeover: Bool {
        #if os(iOS)
        true
        #else
        false
        #endif
    }

    /// Files need a known directory. Git appears only after the detail's existing load confirms a repository:
    /// `repoRoot` alone must not make the two entry points disagree while that status is still loading.
    /// The row reads the same cached status; it never starts a repository request just to draw its menu.
    nonisolated static func panes(_ session: Session, git: GitStatus?) -> [SessionPane] {
        var result: [SessionPane] = [.chat]
        if session.canTerminal { result.append(.terminal) }
        if git?.isRepo == true { result.append(.git) }
        if !session.cwd.isEmpty { result.append(.files) }
        return result
    }

    /// The same status produces the same menu label in the row and detail. The detail's own Git pane keeps its badge.
    nonisolated static func paneBadges(_ git: GitStatus?) -> [SessionPane: String] {
        guard let count = git?.files.count, let text = CountBadge.text(count) else { return [:] }
        return [.git: text]
    }
}

nonisolated struct AgentActionItem: Equatable, Identifiable, Sendable {
    enum ID: String, CaseIterable, Sendable {
        case backToChat, files, git, review, preview, subagents, sideSessions, space
        case attach, interrupt, markSeen, takeover, continueInNewSession, rename, autoRename, pin, mute
        case todo, moveTodo, unlinkTodo, linkTodo, moveTo, stopLoop, handToPersona, screen
        case copyDirectory, terminal, copyResume, resume
        case splitPane, splitRight, splitDown, nodeGit, gitPull, gitPush, gitFetch, closeTab, close, delete
    }
    enum Group: String, CaseIterable, Sendable { case navigation, session, utilities, node, destructive }
    static let screenTitle: LocalizedStringResource = "Live Screen"
    let id: ID
    let group: Group
}

/// Where the Agent menu draws each item of its inventory (#13396): at most seven on the top level, More counted. Interrupt,
/// Mark as Seen, Rename and Pin, with Back to Chat while the chat shows another pane, stay there, then More, then one
/// destructive item last: Close Agent…, else Delete Chat…. Everything else is in More, in the inventory's order.
nonisolated struct AgentActionLayout: Equatable, Sendable {
    /// The most items the top level holds, More and the last item counted.
    static let topLimit = 7
    /// What stays on the top level when offered, before More.
    static let topIDs: Set<AgentActionItem.ID> = [.backToChat, .interrupt, .markSeen, .rename, .pin]

    var top: [AgentActionItem]
    var more: [AgentActionItem]
    var last: AgentActionItem?

    init(_ items: [AgentActionItem]) {
        let last = items.first { $0.id == .close } ?? items.first { $0.id == .delete }
        var frequent = Self.topIDs
        // Keep both rename actions together when there is room, preserving all existing top-level actions.
        let reserved = 1 + (last == nil ? 0 : 1) // More and the destructive action
        if items.filter({ frequent.contains($0.id) }).count + reserved < Self.topLimit { frequent.insert(.autoRename) }
        top = items.filter { frequent.contains($0.id) }
        more = items.filter { !frequent.contains($0.id) && $0.id != last?.id }
        self.last = last
    }

    /// The items the top level shows, More and the last one counted.
    var topLevelCount: Int { top.count + (more.isEmpty ? 0 : 1) + (last == nil ? 0 : 1) }
}

extension AgentActionFacts {
    nonisolated var layout: AgentActionLayout { AgentActionLayout(items) }

    /// What More's items are and say: a change builds its nested menu again (`AgentActionMenu.more`).
    nonisolated var moreIdentity: [String] {
        layout.more.map(\.id.rawValue) + [muted ? "muted" : "", paneBadges[.git] ?? "", "\(sideCount)",
                                          "\(subagentCount)", session.todo?.title ?? "", "\(terminalResume)"]
            + moveChoices.map(\.id)
    }
}

/// A row or detail page supplies its own alerts and navigation. `update` keeps the same callback
/// object when the list redraws; menu actions read the latest session at tap time.
@MainActor
final class AgentMenuHandlers {
    var navigate: (Session, AgentMenuDestination) -> Void = { _, _ in }
    var rename: (Session) -> Void = { _ in }
    var close: (Session) -> Void = { _ in }
    var delete: (Session) -> Void = { _ in }
    var linkTodo: (Session) -> Void = { _ in }
    var openTodo: (Session) -> Void = { _ in }
    var takeover: (Session) -> Void = { _ in }
    var continueInNewSession: (Session) -> Void = { _ in }

    func update(rename: @escaping (Session) -> Void = { _ in }, close: @escaping (Session) -> Void = { _ in },
                linkTodo: @escaping (Session) -> Void = { _ in }, delete: @escaping (Session) -> Void = { _ in },
                takeover: @escaping (Session) -> Void = { _ in }, continueInNewSession: @escaping (Session) -> Void = { _ in }) {
        self.rename = rename
        self.close = close
        self.linkTodo = linkTodo
        self.delete = delete
        self.takeover = takeover
        self.continueInNewSession = continueInNewSession
    }
}

/// `AgentActionMenu` with its facts read in its own `body`. A menu's content is built with the view that holds it (a
/// row's `.contextMenu`, the chat header's `Menu`), so facts built there made that view observe what they read of the
/// model: the machines (whose heartbeat changes on every push of a busy hub), the hub links, pins and side sessions.
/// Every row of the Agents list was drawn again on each push (#10231). Read here, only this view observes them.
struct AgentActionMenuReader: View {
    @Environment(AppModel.self) private var model
    let session: Session
    var currentPane: SessionPane = .chat
    var hosted = false
    var showsSpace = true
    var sides: SideSessions?
    var fromWorktreeSubmenu = false
    let handlers: AgentMenuHandlers

    var body: some View {
        AgentActionMenu(facts: AgentActionFacts(session: session, model: model, currentPane: currentPane, hosted: hosted,
                                                showsSpace: showsSpace, sides: sides),
                        fromWorktreeSubmenu: fromWorktreeSubmenu, handlers: handlers)
            .equatable()
    }

    #if DEBUG
    /// The facts this menu shows, for tests. Not for views: built in a body, they make that view observe the model.
    static func factsForTesting(session: Session, model: AppModel) -> AgentActionFacts {
        AgentActionFacts(session: session, model: model)
    }
    #endif
}

/// One menu definition and one renderer shared by an Agents row's long press and its detail's More button.
struct AgentActionMenu: View, Equatable {
    @Environment(AppModel.self) private var model
    nonisolated var facts: AgentActionFacts
    nonisolated var key: SessionKey { facts.key }
    nonisolated var fromWorktreeSubmenu = false
    nonisolated let handlers: AgentMenuHandlers

    nonisolated static func == (a: Self, b: Self) -> Bool {
        a.facts == b.facts && a.handlers === b.handlers && a.fromWorktreeSubmenu == b.fromWorktreeSubmenu
    }

    var body: some View {
        Group {
            #if DEBUG || PERF_SIGNPOST
            let _ = PerfTrace.body("RowMenu.content")
            #endif
            let now = { model.latest(facts.key) }
            let layout = facts.layout
            // Type-erased boundaries keep the many conditional actions out of the chat page's large Debug stack frames.
            // Every item still comes from the shared `facts.items` inventory: a few on the top level, the rest in More (#13396).
            AnyView(Section {
                ForEach(layout.top) { item in AnyView(row(item, now: now)) }
                AnyView(more(layout.more, now: now))
            })
            if let last = layout.last {
                AnyView(Section { AnyView(row(last, now: now)) })
            }
        }
    }

    /// More: the items the top level leaves out, in the inventory's order and sections. A nested menu keeps the items
    /// it was first built with (the assistant's launcher menu found it, `AssistantTopBar`), so it is built again when
    /// what they say changes (`moreIdentity`: Unmute for Mute, Git's count).
    @ViewBuilder private func more(_ items: [AgentActionItem], now: @escaping () -> Session?) -> some View {
        if !items.isEmpty {
            Menu {
                ForEach(AgentActionItem.Group.allCases, id: \.self) { group in AnyView(section(group, in: items, now: now)) }
            } label: { Label(String(localized: Self.moreTitle), systemImage: Theme.Icon.more) }
            .id(facts.moreIdentity)
            .accessibilityIdentifier("agent-menu-more")
        }
    }

    @ViewBuilder private func section(_ group: AgentActionItem.Group, in items: [AgentActionItem],
                                      now: @escaping () -> Session?) -> some View {
        let own = items.filter { $0.group == group }
        if !own.isEmpty {
            Section { ForEach(own) { item in AnyView(row(item, now: now)) } }
        }
    }

    static let moreTitle: LocalizedStringResource = "More"

    private func label(_ id: AgentActionItem.ID, _ symbol: String) -> some View {
        Label { Text(id.menuTitle(in: facts)) } icon: { Image(systemName: symbol) }
            .lineLimit(1)
    }

    @ViewBuilder private func row(_ item: AgentActionItem, now: @escaping () -> Session?) -> some View {
        let s = facts.session
        switch item.id {
        case .backToChat:
            Button { handlers.navigate(s, .pane(.chat)) } label: { label(item.id, SessionPane.chat.symbol) }
        case .files, .git:
            let pane: SessionPane = item.id == .files ? .files : .git
            Button { handlers.navigate(s, .pane(pane)) } label: {
                label(item.id, pane.symbol)
            }
        case .review:
            Button { handlers.navigate(s, .review) } label: { label(item.id, Theme.Icon.reviewChanges) }
        case .preview:
            Button { handlers.navigate(s, .preview) } label: { label(item.id, Theme.Icon.servedPage) }
                .accessibilityIdentifier("chat-preview")
        case .subagents:
            Button { handlers.navigate(s, .subagents) } label: {
                label(item.id, Theme.Icon.subagent)
            }
            .accessibilityValue(Text(facts.subagentCount, format: .number))
        case .sideSessions:
            Button { handlers.navigate(s, .sideSessions) } label: {
                label(item.id, Theme.Icon.sideSessions)
            }
            .accessibilityValue(Text(facts.sideCount, format: .number))
            .accessibilityIdentifier("side-sessions-menu")
        case .space:
            Button { handlers.navigate(s, .space) } label: { label(item.id, Theme.Icon.space) }
        case .attach:
            Button { Task { await model.attach(facts.key) } } label: { label(item.id, Theme.Icon.link) }
        case .interrupt:
            Button { Task { await model.interrupt(facts.key) } } label: { label(item.id, Theme.Icon.interrupt) }
        case .markSeen:
            Button { model.markSeen(facts.key) } label: { label(item.id, Theme.Icon.view) }
        case .takeover:
            Button { if let s = now() { handlers.takeover(s) } } label: { label(item.id, Theme.Icon.terminal) }
        case .continueInNewSession:
            Button { if let s = now() { handlers.continueInNewSession(s) } } label: {
                label(item.id, Theme.Icon.compose)
            }
        case .rename:
            Button { if let s = now() { handlers.rename(s) } } label: {
                label(item.id, Theme.Icon.edit)
            }
        case .autoRename:
            Button { model.autoRenameAgent(now() ?? s, fromWorktreeSubmenu: fromWorktreeSubmenu) } label: {
                label(item.id, Theme.Icon.ai)
            }
            .disabled(model[naming: facts.key])
            .accessibilityIdentifier("agent-auto-rename")
        case .pin:
            Button { model.togglePin(facts.key) } label: {
                label(item.id, facts.pinned ? Theme.Icon.unpin : Theme.Icon.pin)
            }
        case .mute:
            Button { model.toggleMute(facts.key) } label: {
                label(item.id, facts.muted ? Theme.Icon.unmute : Theme.Icon.mute)
            }
        case .todo:
            Button { if let s = now() { handlers.openTodo(s) } } label: { label(item.id, Theme.Icon.todo) }
        case .moveTodo, .linkTodo:
            Button { if let s = now() { handlers.linkTodo(s) } } label: {
                label(item.id, Theme.Icon.todo)
            }
        case .unlinkTodo:
            Button {
                guard let todo = s.todo else { return }
                Task {
                    do { try await TodoStore.shared.unlinkAgent(todoID: todo.id, machineID: s.machineID, sessionID: s.sessionID) }
                    catch { model.show(error) }
                }
            } label: { label(item.id, Theme.Icon.link) }
        case .moveTo:
            MoveToChoices(choices: facts.moveChoices, title: MoveToChoices.agentTitle) { choice in
                guard let s = now() else { return }
                DirectionFilings.shared.move(s.key, title: s.displayTitle, state: s.state, to: choice,
                                             from: DirectionFilings.shared.placed(s.key))
            }
        case .stopLoop:
            Button(role: .destructive) { model.stopLoop(facts.key) } label: { label(item.id, Theme.Icon.stopLoop) }
        case .handToPersona:
            Button {
                guard let s = now() else { return }
                Task { await model.askPersona(AgentHandoff.prompt(s, machine: model.machine(s.machineRef)?.name)) }
            } label: { label(item.id, Theme.Icon.ai) }
        case .screen:
            ModuleViews(place: .chatMenu, context: .init(machine: facts.key.machineRef, from: facts.key))
        case .copyDirectory:
            Button { UIPasteboard.general.string = now()?.cwd } label: { label(item.id, Theme.Icon.folder) }
        case .terminal:
            Button { handlers.navigate(s, .terminal(resume: false)) } label: { label(item.id, Theme.Icon.terminal) }
        case .copyResume:
            Button {
                guard let s = now(), s.canTerminal else { return }
                UIPasteboard.general.string = "\(s.resumeCommand) \(s.sourceThreadID ?? s.sessionID)"
            } label: { label(item.id, Theme.Icon.copy) }
        case .resume:
            switch facts.terminalResume {
            case .available:
                Button { handlers.navigate(s, .terminal(resume: true)) } label: {
                    label(item.id, Theme.Icon.resume)
                }
                .accessibilityIdentifier("chat-resume-terminal")
            case .noEngine:
                let machine = model.machine(facts.key.machineRef)?.name ?? facts.key.machineID
                let reason = Text("No terminal engine on \(machine)", comment: "Why Resume in Terminal is off: the computer enables no terminal engine")
                Section {
                    Button {} label: { label(item.id, Theme.Icon.resume) }
                        .disabled(true)
                        .accessibilityHint(reason)
                        .accessibilityIdentifier("chat-resume-terminal")
                } header: { reason }
            case .hidden:
                EmptyView()
            }
        case .splitPane:
            Menu {
                ForEach(NodeMenu.Direction.allCases) { direction in
                    Button { if let s = now() { NodeActions.shared.split(s, direction) } } label: {
                        Label(direction.title, systemImage: direction.symbol)
                    }
                }
            } label: { label(item.id, Theme.Icon.splitRight) }
        case .splitRight, .splitDown, .gitPull, .gitPush, .gitFetch:
            // These entries describe the nested inventory; their parent submenu renders them above/below.
            EmptyView()
        case .nodeGit:
            Menu {
                ForEach(NodeMenu.GitOp.allCases) { operation in
                    Button { if let s = now() { NodeActions.shared.git(operation, session: s) } } label: {
                        Label(operation.title, systemImage: operation.symbol)
                    }
                }
            } label: { label(item.id, Theme.Icon.gitBranch) }
        case .closeTab:
            Button(role: .destructive) { if let s = now() { NodeActions.shared.askCloseTab(s) } } label: {
                label(item.id, Theme.Icon.closeTab)
            }
        case .close:
            Button(role: .destructive) { if let s = now() { handlers.close(s) } } label: {
                label(item.id, Theme.Icon.end)
            }
        case .delete:
            Button(role: .destructive) { if let s = now(), s.canDelete { handlers.delete(s) } } label: {
                label(item.id, Theme.Icon.delete)
            }
        }
    }
}

extension AgentActionItem.ID {
    /// A command title stays bounded even when its linked to-do or computer has a long name.
    @MainActor func menuTitle(in facts: AgentActionFacts) -> LocalizedStringResource {
        switch self {
        case .backToChat: "Back to Chat [Agent Menu]"
        case .files, .git:
            if let badge = facts.paneBadges[self == .files ? .files : .git] {
                "\(self == .files ? SessionPane.files.title : SessionPane.git.title) (\(badge))"
            } else {
                self == .files ? "Files" : "Git"
            }
        case .review: "Review [Agent Menu]"
        case .preview: "Preview"
        case .subagents: "Children \(CountBadge.text(facts.subagentCount) ?? "0") [Agent Menu]"
        case .sideSessions: "Threads \(CountBadge.text(facts.sideCount) ?? "0") [Agent Menu]"
        case .space: "Space Agents"
        case .attach: "Attach Live"
        case .interrupt: "Interrupt [Composer Buttons f9d193]"
        case .markSeen: "Mark as Seen [Agent Menu]"
        case .takeover: "Take over"
        case .continueInNewSession: "Continue in New Chat"
        case .rename: "Rename Agent"
        case .autoRename: "Auto Rename"
        case .pin: facts.pinned ? "Unpin [Personal Agent List View c92179]" : "Pin [Personal Agent List View 173f88]"
        case .mute: facts.muted ? "Unmute [Chat View 3834f9]" : "Mute"
        case .todo: "Open Linked To-do"
        case .screen: AgentActionItem.screenTitle
        case .moveTodo: "Change Linked To-do"
        case .linkTodo: "Link To-do [Agent Menu]"
        case .unlinkTodo: "Unlink [Agent Menu]"
        case .moveTo: MoveToChoices.agentTitle
        case .stopLoop: "Stop Loop"
        case .handToPersona: "To Persona [Agent Menu]"
        case .copyDirectory: "Copy Path [Agent Menu]"
        case .terminal: "Terminal"
        case .copyResume: "Copy Resume Cmd"
        case .resume: "Resume [Agent Menu]"
        case .splitPane: "Split Pane [Agent Menu]"
        case .splitRight: NodeMenu.Direction.right.titleResource
        case .splitDown: NodeMenu.Direction.down.titleResource
        case .nodeGit: "Git"
        case .gitPull: NodeMenu.GitOp.pull.titleResource
        case .gitPush: NodeMenu.GitOp.push.titleResource
        case .gitFetch: NodeMenu.GitOp.fetch.titleResource
        case .closeTab: "Close Tab"
        case .close: "Close Agent"
        case .delete: "Delete Chat"
        }
    }
}
