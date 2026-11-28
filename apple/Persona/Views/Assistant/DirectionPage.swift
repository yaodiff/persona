import SwiftUI

// The page of everything under one direction, first version (#11516, docs/design/DESIGN_SYSTEM.md 3.4 "A direction's
// page"): its name, In Progress (the held tree's to-dos and their Agents, drawn as the sidebar draws them, live) and
// Added Here (the sessions placed in it, from `GET /api/v1/assistant/directions/{id}`). Opened from a direction's All ›
// in the sidebar and from its leader conversation's title menu, pushed one level over that conversation. Drawn only
// while the hub files sessions (`DirectionFilings.shows`). Notes, progress and ended tasks come in the next step.

/// The page's value on the Assistant page's path.
nonisolated struct DirectionPageRoute: Hashable, Sendable {
    var id: String
}

/// Asks the Assistant page to open a direction's page (`AssistantView.openDirectionPage`): from the sidebar, its
/// leader conversation becomes the current place with the page already over it; from the title menu it is pushed.
@MainActor @Observable final class DirectionPages {
    static let shared = DirectionPages()

    nonisolated struct Ask: Equatable, Sendable {
        var id: String
        var lead: SessionKey
        var fromSidebar: Bool
        var serial: Int
    }

    private(set) var ask: Ask?
    @ObservationIgnored private var serial = 0

    func open(_ id: String, over lead: SessionKey, fromSidebar: Bool) {
        serial += 1
        ask = Ask(id: id, lead: lead, fromSidebar: fromSidebar, serial: serial)
    }

    /// The ask with this serial, once: the page that takes it opens it.
    func take(_ serial: Int) -> Ask? {
        guard let a = ask, a.serial == serial else { return nil }
        ask = nil
        return a
    }
}

/// What the page works out, apart from its views (tested in `DirectionFilingsTests`).
enum DirectionPageLogic {
    /// Added Here shows this many, then Show All N (docs/design/DESIGN_SYSTEM.md 3.4, a long list on a detail page).
    static let firstCount = 5

    /// The rows Added Here shows: none while folded, the first five unless all are asked for.
    static func shown<T>(_ rows: [T], all: Bool, folded: Bool = false) -> [T] {
        folded ? [] : all ? rows : Array(rows.prefix(firstCount))
    }

    /// A filed session's title: the session's own where the app holds its row (renamed since, it is that name
    /// everywhere), else the one it was filed with, else Agent.
    static func title(_ s: DirectionTree.FiledSession, session: Session?) -> String {
        if let t = session?.title, !t.isEmpty, t != "Claude Code" { return t }
        if !s.title.isEmpty { return s.title }
        if let t = session?.displayTitle, !t.isEmpty { return t }
        return String(localized: "Agent")
    }

    /// Where a filed row says it stands (`docs/assistant/unified-entry.md` "Where a session stands").
    enum Standing: Equatable {
        /// The session's own state (`offline`: Not Running, stopped on a connected computer).
        case state(AgentState)
        /// Its computer is not connected: never Not Running.
        case computerOffline
        /// Its computer has not given its list: nothing is claimed.
        case notKnownYet
        /// Its computer's list lacks it: archived, quiet or gone can't be told apart, so only when it was added.
        case added(Date)
        case computerRemoved
        /// Read earlier in this run; the page can't read now.
        case offline(since: Date)
    }

    /// `live`: the session's state as the app lists it, which follows it after the read. A value this app does not
    /// know draws no word (nil).
    static func standing(_ s: DirectionTree.FiledSession, live: AgentState?) -> Standing? {
        switch s.standing {
        case .computerOffline: return .computerOffline
        case .listUnknown: return .notKnownYet
        case .notListed: return s.at.map { .added($0) }
        case .computerRemoved: return .computerRemoved
        case nil:
            guard let own = s.ownState else { return nil }
            return .state(live ?? own)
        }
    }

    static func words(_ standing: Standing) -> String {
        switch standing {
        case .state(let st): st.label
        case .computerOffline: String(localized: "Offline [Filed Session]")
        case .notKnownYet: String(localized: "Not known yet [Filed Session]")
        case .added(let at): String(localized: "Added \(at.formatted(.relative(presentation: .named))) [Filed Session]")
        case .computerRemoved: String(localized: "Computer removed [Filed Session]")
        case .offline(let since): String(localized: "Offline · \(Fmt.relative(since))")
        }
    }

    /// The dot beside it: the state's, or a quiet one where no state is known.
    static func dot(_ standing: Standing?) -> AgentState {
        switch standing {
        case .state(let st): st
        case .computerOffline, .computerRemoved: .offline
        default: .unknown
        }
    }

    /// The tone of its words.
    static func tone(_ standing: Standing?) -> Color {
        if case .state(let st) = standing { return st.tone.text }
        return .secondary
    }
}

/// The page's read of its direction (`GET /api/v1/assistant/directions/{id}`): on open, on each hello (a reconnect),
/// after a move made on this device (`DirectionFilings.page`, also while a chat is pushed over the page) and when the
/// direction leaves the held tree (unpinned while open); never on a tree push otherwise (P7 / P9). The reads belong to
/// the page, not to a view's task, so a chat pushed over it does not cut one off; they are numbered, and only an answer
/// newer than the last one taken is drawn (as the tree's, `AppModel.refreshDirectionTree`). What this run read of it is
/// kept (`DirectionFilings.kept`), so it still draws, marked Offline · time, when a later read fails.
@MainActor @Observable final class DirectionPageModel {
    nonisolated enum Status: Equatable, Sendable {
        case reading
        case read
        /// The read failed; what was read before in this run stays.
        case failed
        /// 404 `direction_not_found`: the page is no longer available.
        case gone
    }

    let id: String
    /// The last read taken, its sessions in the app's order (`DirectionFilings.order`).
    private(set) var read: DirectionRead?
    private(set) var readAt: Date?
    private(set) var status: Status = .reading
    /// Added Here shows them all.
    var showsAll = false
    /// Added Here folded away (Collapse All); its count stays in its header.
    var folded = false

    /// Numbers the reads as they are asked; `landed` is the newest one whose answer was taken.
    /// Reads out now. Read only where the page has nothing else to show while one is out (unpinned, nothing added).
    private(set) var inFlight = 0
    @ObservationIgnored private var asked = 0
    @ObservationIgnored private var landed = 0
    /// The hello (`AppModel.hubHellos`) this page last read for.
    @ObservationIgnored private var hello: Int?
    @ObservationIgnored private var registered = false
    /// Reads cut off in a row (a route attempt that restarts cancels the request waiting on it): read again, a few
    /// times, rather than spin.
    @ObservationIgnored private var cancelled = 0

    init(id: String) {
        self.id = id
        if let k = DirectionFilings.shared.kept(id) { read = k.read; readAt = k.at }
    }

    /// On open and on each hello: once per hello, so coming back from a chat pushed over the page reads nothing (P9).
    func open(hello: Int, api: APIClient?) {
        register()
        guard self.hello != hello else { return }
        self.hello = hello
        refresh(api)
    }

    /// Reads it again now (Retry, the direction leaving the tree), in a task of the page's own.
    func refresh(_ api: APIClient?) {
        // Out from now, not from when the task starts: no frame reads "nothing out" in between (a retry included).
        inFlight += 1
        Task { await self.load(api, counted: true) }
    }

    /// `counted`: `refresh` has already marked this read as out.
    func load(_ api: APIClient?, counted: Bool = false) async {
        if !counted { inFlight += 1 }
        defer { inFlight -= 1 }
        asked += 1
        let n = asked
        guard let api else {
            if status == .reading { status = .failed }
            return
        }
        if status != .read { status = .reading } // rows read before stay drawn while a read is out
        do {
            var r = try await api.direction(id)
            guard n > landed else { return } // a newer answer was taken meanwhile
            landed = n
            cancelled = 0
            r.sessions = DirectionFilings.order(r.sessions)
            let now = Date.now
            read = r
            readAt = now
            DirectionFilings.shared.keep(id, r, at: now)
            status = .read
        } catch {
            // Only the newest read out decides how a failure shows: an older one's says nothing.
            guard n > landed, n == asked else { return }
            if error is CancellationError || (error as? URLError)?.code == .cancelled {
                // Not an answer: read again (a few times), the page as it was meanwhile.
                // Cancelled four times in a row counts as a failed read (Couldn't read, Retry; rows read before
                // say Offline · their time), never as an answer.
                cancelled += 1
                if cancelled <= 3 { refresh(api) } else { cancelled = 0; status = .failed }
                return
            }
            // The newest read's failure takes its number too: an older read landing after it is not drawn as fresh.
            landed = n
            cancelled = 0
            if let e = error as? APIError, e.status == 404, e.code == "direction_not_found" {
                status = .gone
            } else {
                if DirectionRoutes.missing(error) { DirectionFilings.shared.routesAnsweredMissing(api.group) }
                status = .failed
            }
        }
    }

    /// Read again after this device's writes, for as long as the page lives (a chat pushed over it included).
    private func register() {
        guard !registered else { return }
        registered = true
        DirectionFilings.shared.page(id) { [weak self] in
            guard let self else { return false }
            await self.load(AppModel.shared.api)
            return true
        }
    }
}

/// The page (#11516). The name and In Progress come from the held tree, so they draw before the read returns; Added
/// Here waits for the read, and says so while it is out or when it fails, never as empty or as "no tasks".
struct DirectionPage: View {
    @Environment(AppModel.self) private var model
    /// Agents' chats go on this page's path, over it (`conversationAgentPush`).
    @Environment(\.pushInboxChat) private var pushChat
    let id: String
    @State private var page: DirectionPageModel
    @State private var shownTodo: TodoDetailRoute?
    @State private var openTasks: Set<String> = []
    @State private var openAgents: Set<String> = []

    init(id: String) {
        self.id = id
        _page = State(initialValue: DirectionPageModel(id: id))
    }

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("DirectionPage")
        #endif
        let direction = model.directionTree?.leader(id)
        let tasks = direction?.tasks ?? []
        // Each state carries its own identifier (`direction-page…`); none on the Group, whose identifier would cover them.
        Group {
            if page.status == .gone {
                EmptyStateView(symbol: Theme.Icon.info, title: String(localized: "Not Available [Direction Page]"),
                               message: String(localized: "This page is no longer available. [Direction Page]"))
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .accessibilityElement(children: .contain)
                    .accessibilityIdentifier("direction-page-gone")
            } else if direction == nil && page.read == nil {
                // Neither the tree nor a read has it: being read, or Couldn't read with Retry.
                LoadableContent(state: page.status == .failed ? Loadable<Void>.failed(String(localized: "Couldn't read [Direction Page]"))
                                                              : .loading,
                                retry: { page.refresh(model.api) }) { _ in EmptyView() }
                    .accessibilityElement(children: .contain)
                    .accessibilityIdentifier(page.status == .failed ? "direction-page-unread" : "direction-page-reading")
            } else {
                List {
                    if page.read?.endedAt != nil {
                        Section {
                            Label(String(localized: "This has ended. You can look, but nothing here can be moved. [Direction Page]"),
                                  systemImage: Theme.Icon.info)
                                .font(Theme.Font.secondary).foregroundStyle(.secondary)
                                .themedRow()
                                .accessibilityIdentifier("direction-page-ended")
                        }
                    }
                    if !tasks.isEmpty {
                        Section {
                            ForEach(tasks) { task in
                                DirectionTaskRow(task: task, inset: 0, unfolded: openTasks.contains(task.id),
                                                 toggle: { openTasks.formSymmetricDifference([task.id]) },
                                                 open: { shownTodo = TodoDetailRoute(id: task.id) })
                                    .themedRow()
                                if openTasks.contains(task.id) {
                                    ForEach(task.agentLines(opened: openAgents)) { line in
                                        DirectionAgentRow(line: line, inset: 0, unfolded: openAgents.contains(line.id),
                                                          toggle: { openAgents.formSymmetricDifference([line.id]) }) { key in
                                            openTodoAgentChat(key, in: model, push: conversationAgentPush(pushChat))
                                        }
                                        .themedRow()
                                    }
                                }
                            }
                        } header: {
                            HStack {
                                Text("In Progress [Direction Page]")
                                Spacer(minLength: Theme.Space.s)
                                // The sidebar row's own words for the same tree (`DirectionCountsText`).
                                if let direction { DirectionCountsLine(direction: direction) }
                            }
                            .accessibilityElement(children: .combine)
                            .accessibilityIdentifier("direction-page-in-progress")
                        }
                    }
                    DirectionAddedSection(page: page, direction: id, hasTasks: !tasks.isEmpty, inTree: direction != nil,
                                          pushChat: pushChat)
                }
                .themedPage()
                .accessibilityIdentifier("direction-page")
            }
        }
        .pageTitle(DirectionPageTitle.of(id, read: page.read, in: model))
        // Pushed pages here keep the bar's inline title: a large one draws a second title over the first section.
        .navigationBarTitleDisplayMode(.inline)
        .sheet(item: $shownTodo) { r in
            NavigationStack {
                TodoDetailView(todoID: r.id).sheetDone { shownTodo = nil }
            }
        }
        // On open and on every hello (a reconnect); after this device's moves through `DirectionFilings.page`.
        .task(id: model.hubHellos) { page.open(hello: model.hubHellos, api: model.api) }
        // Unpinned while open (the tree push drops it) or pinned again: one read says which.
        .onChange(of: direction == nil) { page.refresh(model.api) }
    }
}

/// The page's title: the direction's name as its sidebar row says it; from the read once it is no longer in the tree.
enum DirectionPageTitle {
    @MainActor static func of(_ id: String, read: DirectionRead?, in model: AppModel) -> String {
        if let d = model.directionTree?.leader(id) { return model.directionTitle(d) }
        if let name = read?.name, !name.isEmpty { return name }
        return model.directionTitle(DirectionTree.Direction(id: id))
    }
}

/// Added Here: the sessions placed in the direction, by who placed them, in the app's order (needs you, working, the
/// rest; newest added first in each), the first five then Show All N, with Collapse All / Expand All in its header
/// (docs/design/DESIGN_SYSTEM.md 3.4, a long list on a detail page); each says where it stands, opens on a tap and has
/// Move To… in its menu (not once the direction has ended). Its own view, so the read and the moves out redraw only
/// it (P5).
private struct DirectionAddedSection: View {
    @Environment(AppModel.self) private var model
    let page: DirectionPageModel
    let direction: String
    /// In Progress has rows: a read with nothing added then says nothing of its own.
    let hasTasks: Bool
    /// The direction is in the held tree: one that left it (unpinned) says nothing of how to add to it.
    let inTree: Bool
    let pushChat: ((InboxChatRoute) -> Void)?

    var body: some View {
        let filings = DirectionFilings.shared
        let read = page.read
        let rows = filings.rows(read?.sessions ?? [], in: direction, group: model.hub?.group ?? "", live: false)
        let ended = read?.endedAt != nil
        let stale = page.status == .failed && read != nil
        if read == nil {
            // Never read in this run: reading, or Couldn't read with Retry. Never drawn as empty.
            Section {
                if page.status == .failed {
                    retryRow(String(localized: "Couldn't read [Direction Page]"), id: "direction-page-unread")
                } else {
                    HStack { Spacer(); ProgressView(); Spacer() }
                        .padding(.vertical, Theme.Space.s)
                        .themedRow()
                        .accessibilityIdentifier("direction-page-reading")
                }
            } header: { header(nil) }
        } else if read?.sessionsUnavailable == true {
            Section {
                retryRow(String(localized: "Couldn't read what was added here. [Direction Page]"),
                         id: "direction-page-sessions-unavailable")
            } header: { header(nil) }
        } else if !rows.isEmpty {
            Section {
                ForEach(DirectionPageLogic.shown(rows, all: page.showsAll, folded: page.folded)) { row in
                    DirectionAddedRow(row: row, direction: direction, ended: ended, stale: stale ? page.readAt : nil,
                                      pushChat: pushChat)
                        .themedRow()
                }
                if !page.folded, rows.count > DirectionPageLogic.firstCount {
                    Button {
                        withAnimation(Theme.Motion.standard) { page.showsAll.toggle() }
                    } label: {
                        Text(page.showsAll ? String(localized: "Show First \(DirectionPageLogic.firstCount) [Direction Page]")
                                           : String(localized: "Show All \(rows.count)"))
                            .font(Theme.Font.secondaryMedium)
                            .lineLimit(1)
                    }
                    .themedRow()
                    .accessibilityIdentifier("direction-page-more")
                }
            } header: { header(rows.count, fold: true) }
        } else if !hasTasks && !ended && inTree {
            // Read, and nothing is under it at all: what the page is for, and how something gets here.
            EmptyStateView(symbol: Theme.Icon.list, title: String(localized: "Nothing Here Yet [Direction Page]"),
                           message: String(localized: "Use Move To… on a chat or an Agent to add it here. [Direction Page]"))
                .frame(maxWidth: .infinity)
                .listRowBackground(Color.clear)
                .accessibilityElement(children: .contain)
                .accessibilityIdentifier("direction-page-empty")
        } else if !ended && !inTree {
            // Left the tree (unpinned while open) with nothing added: the read that says it has ended is out, or it
            // failed. Never blank. A read that still calls it current says only that nothing is here (no Move To…).
            Section {
                if page.status == .failed {
                    retryRow(String(localized: "Couldn't read [Direction Page]"), id: "direction-page-unread")
                } else if page.inFlight > 0 {
                    HStack { Spacer(); ProgressView(); Spacer() }
                        .padding(.vertical, Theme.Space.s)
                        .themedRow()
                        .accessibilityIdentifier("direction-page-reading")
                } else {
                    Label(String(localized: "Nothing Here Yet [Direction Page]"), systemImage: Theme.Icon.list)
                        .font(Theme.Font.secondary).foregroundStyle(.secondary)
                        .themedRow()
                        .accessibilityIdentifier("direction-page-empty")
                }
            } header: { header(nil) }
        }
    }

    private func header(_ count: Int?, fold: Bool = false) -> some View {
        HStack(spacing: Theme.Space.s) {
            SectionHeader(title: String(localized: "Added Here [Direction Page]"), count: count, tone: StatusPalette.idle)
                .accessibilityIdentifier("direction-page-added")
            Spacer(minLength: 0)
            if fold { foldButton }
        }
    }

    /// Collapse All while any row shows, Expand All once folded (it then shows them all); the count beside it stays.
    private var foldButton: some View {
        let folded = page.folded
        return Button {
            withAnimation(Theme.Motion.standard) {
                if folded { page.folded = false; page.showsAll = true } else { page.folded = true }
            }
        } label: {
            ThemedIcon(folded ? Theme.Icon.expandAll : Theme.Icon.collapseAll)
                .font(Theme.Font.captionStrong)
                .foregroundStyle(Theme.accent)
                .tapTargetFrame()
                .contentShape(.rect)
        }
        .buttonStyle(.borderless)
        .accessibilityLabel(folded ? String(localized: "Expand All") : String(localized: "Collapse All"))
        .accessibilityIdentifier("direction-page-fold")
    }

    private func retryRow(_ words: String, id: String) -> some View {
        HStack(spacing: Theme.Space.m) {
            Label(words, systemImage: Theme.Icon.error)
                .font(Theme.Font.secondary).foregroundStyle(.secondary)
                .accessibilityIdentifier(id)
            Spacer(minLength: Theme.Space.s)
            Button { page.refresh(model.api) } label: { Text("Retry").lineLimit(1) }
                .themedButton()
                .accessibilityIdentifier("direction-page-retry")
        }
        .themedRow()
    }
}

/// One session in Added Here: its dot, title and who placed it, and where it stands; a tap opens it. The standing sits
/// beside the title alone while the title keeps half the row (the title cut on its line), and goes under the title only
/// otherwise (`BesideOrUnder`); who placed it is a line of its own across the row, never cut to the title's width.
private struct DirectionAddedRow: View {
    @Environment(AppModel.self) private var model
    let row: DirectionFilings.Row
    let direction: String
    let ended: Bool
    /// When the rows were read, while the page can't read now: they say Offline · that time.
    let stale: Date?
    let pushChat: ((InboxChatRoute) -> Void)?

    var body: some View {
        let session = model.session(row.key)
        let title = DirectionPageLogic.title(row.session, session: session)
        let standing: DirectionPageLogic.Standing? = stale.map { .offline(since: $0) }
            ?? DirectionPageLogic.standing(row.session, live: session?.state)
        let word = row.moving ? String(localized: "Moving… [Move To]") : standing.map(DirectionPageLogic.words)
        let source = row.session.addedByOwner ? String(localized: "Added by you [Filed Session]")
                                              : String(localized: "Opened from this chat [Filed Session]")
        Button { openTodoAgentChat(row.key, in: model, push: conversationAgentPush(pushChat)) } label: {
            // The whole width takes the tap, a row without a word too (`BesideOrUnder` with one view is that view's size).
            HStack(alignment: .top, spacing: Theme.Space.s) {
                StatusDot(state: DirectionPageLogic.dot(standing))
                    .padding(.top, Theme.Space.xs)
                VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                    // The word beside the title while the title keeps half the row or all it needs, the title cut on
                    // its one line; a word that would leave it less goes under it (`BesideOrUnder`, DESIGN_SYSTEM 6.2).
                    // By measured widths, so a long title never wraps the row, but a word wider than half the row sits
                    // beside a short title and under a long one.
                    BesideOrUnder(alignment: .firstTextBaseline) {
                        Text(title).font(Theme.Font.secondary).foregroundStyle(Color.primary).lineLimit(1)
                        if let word { standingText(word, standing) }
                    }
                    // Who placed it across the whole row: beside a word it would be cut to the title's width.
                    Text(source).font(Theme.Font.caption).foregroundStyle(.secondary).lineLimit(1)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.vertical, Theme.Sidebar.rowPadding)
            .scaledFrame(minHeight: Theme.Sidebar.row)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel([title, source, word].compactMap { $0 }
            .joined(separator: String(localized: ", ", comment: "Separator between the parts of a VoiceOver label")))
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("direction-page-filed")
        .contextMenu {
            if !ended {
                MoveToMenu(key: row.key, title: title, state: session?.state ?? row.session.ownState ?? .unknown,
                           from: direction)
            }
        }
    }

    private func standingText(_ word: String, _ standing: DirectionPageLogic.Standing?) -> some View {
        Text(word).font(Theme.Font.caption)
            .foregroundStyle(row.moving ? Color.secondary : DirectionPageLogic.tone(standing)).lineLimit(1)
    }
}

/// Move To… in a session's long-press menu (#11516): the current directions by name in the tree's order, then On
/// Its Own. Drawn only where `DirectionFilings.offers` offers it, read in this view's own body (P5). `from`: the
/// direction the menu's row is drawn under, when it is one (the page).
struct MoveToMenu: View {
    @Environment(AppModel.self) private var model
    let key: SessionKey
    var title: String
    var state: AgentState
    var from: String? = nil

    var body: some View {
        let filings = DirectionFilings.shared
        let choices = filings.offers(key, in: model)
        if !choices.isEmpty {
            MoveToChoices(choices: choices) { choice in
                filings.move(key, title: title, state: state, to: choice, from: from ?? filings.placed(key))
            }
        }
    }
}

/// The submenu itself, for a menu that already holds its choices (the Agent menus' facts).
struct MoveToChoices: View {
    static let agentTitle: LocalizedStringResource = "Move To"
    static let onItsOwn: LocalizedStringResource = "On Its Own [Move To]"
    let choices: [DirectionFilings.Choice]
    var title: LocalizedStringResource = "Move To… [Move To]"
    let pick: (DirectionFilings.Choice?) -> Void

    var body: some View {
        Menu {
            ForEach(choices) { c in
                Button { pick(c) } label: { Text(c.name).lineLimit(1) }
            }
            Divider()
            Button { pick(nil) } label: { Text(Self.onItsOwn).lineLimit(1) }
                .accessibilityIdentifier("move-to-own")
        } label: {
            Label(String(localized: title), systemImage: Theme.Icon.moveTo).lineLimit(1)
        }
        .accessibilityIdentifier("move-to")
    }
}

/// The leader conversation's title menu: its first item opens the page (#11516). Its own view, so the gate and the
/// tree are read here, not by the bar.
struct DirectionPageMenuItem: View {
    @Environment(AppModel.self) private var model
    let key: SessionKey?
    @Binding var showsPermissions: Bool

    var body: some View {
        if let key, DirectionFilings.shared.shows(model), model.directionTree?.leader(key.sessionID) != nil {
            Button { DirectionPages.shared.open(key.sessionID, over: key, fromSidebar: false) } label: {
                Label(String(localized: "Tasks and Chats [Direction Page]"), systemImage: Theme.Icon.list).lineLimit(1)
            }
            .accessibilityIdentifier("assistant-direction-page")
            Button { showsPermissions = true } label: {
                Label(String(localized: "Default Permissions"), systemImage: Theme.Icon.permissions).lineLimit(1)
            }
            .accessibilityIdentifier("direction-default-permissions")
        }
    }
}

/// The Unpin confirmation's words (#6214's), and while the hub files sessions one more sentence of its own: what was
/// added to the direction stays, and comes back if the same chat is pinned again (#11516,
/// docs/assistant/unified-entry.md "The direction tree"). #11394's sentence about its open tasks is left as it is.
enum UnpinWords {
    @MainActor static func message(_ base: String, model: AppModel) -> String {
        guard DirectionFilings.shared.shows(model) else { return base }
        return base + "\n" + String(localized: "What you added to it stays. [Move To]")
    }
}
