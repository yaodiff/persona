import BeautifulUI
import SwiftUI

public struct TodoDetailRoute: Hashable, Sendable, Identifiable {
    public var id: String
    public init(id: String) { self.id = id }
}

struct TodosView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.dismiss) private var dismiss
    @State private var store = TodoStore.shared
    /// The knowledge-base projects Group By › Project names its sections after.
    @State private var projects = TodoProjects.shared
    @State private var query = ""
    @State private var showCreate = false
    @State private var creatingTitle = ""
    @State private var creatingNotes = ""
    /// The New To-Do sheet's notes mic (#2188).
    @State private var dictation = Dictation()
    /// Folded sections the user opened (Shelved and Done start folded), and merged rows opened to their members.
    @State private var unfolded: Set<TodoSection> = []
    @State private var openedGroups: Set<String> = []
    /// Restores from Shelved kept on this device (`TodoRestores`), and the moment the sections are read at: a minute's
    /// tick moves to-dos across the 24 h / 48 h lines while the page stays open.
    @State private var restored = TodoRestores.load()
    @State private var clock = Date.now
    /// A to-do opened from the calendar (and, in DEBUG screenshots, `-openTodoTitled <text>`: the first whose title has it).
    @State private var pushed: TodoDetailRoute?
    /// List or calendar, and the calendar's day / week / month (#2029): each device keeps its own.
    @StoredValue("todos.view") private var viewMode = TodosViewMode.list
    @StoredValue("todos.calendar.mode") private var calendarMode = TodoCalendarMode.day
    /// The list's filter by due date and its order inside each section (#3740): each device keeps its own, like the
    /// view above. DEBUG screenshots set them with `-todos.filter week` / `-todos.sort due`.
    @StoredValue("todos.filter") private var listFilter = TodoListFilter.all
    @StoredValue("todos.sort") private var listSort = TodoListSort.recent
    /// The list by status section (default) or by Pinned direction (#4352); a hub without the direction tree offers only
    /// the first.
    @StoredValue("todos.grouping") private var grouping = TodosGrouping.status
    @State private var calendarAnchor = Date.now
    @State private var quickAdd: TodoCalendarSlot?
    @Environment(\.tabIsActive) private var inFront

    /// What the list draws: the to-dos by status section (#3206, docs/assistant/todo-status-sop.md), related ones merged
    /// into one row. A search and the filter (#3740) narrow the to-dos first, then the sections place what is left.
    private func listState() -> TodoFilteredList {
        TodoListLogic.list(store.scopedTodos, searched: store.scopedTodos.filter(matchesQuery), filter: listFilter,
                           lookup: TodosLogic.makeSessionLookup(AppModel.shared.allSessions(inFront: inFront)),
                           now: clock, restored: restored, calendar: TodoCalendar.userCalendar)
    }

    private func matchesQuery(_ todo: TodoItem) -> Bool {
        if query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { return true }
        let q = query.localizedLowercase
        if todo.title.localizedLowercase.contains(q) { return true }
        if let notes = todo.notes, notes.localizedLowercase.contains(q) { return true }
        return false
    }

    var body: some View {
        VStack(spacing: Theme.Space.flush) {
            TodosModeBar(view: $viewMode, mode: $calendarMode, grouping: $grouping,
                         groupings: TodosGrouping.offered(hasTree: model.directionTree != nil)) {
                listControls
            }
            if viewMode == .calendar {
                TodoCalendarView(todos: store.scopedTodos, mode: $calendarMode, anchor: $calendarAnchor,
                                 open: { pushed = TodoDetailRoute(id: $0) }, showList: { viewMode = .list })
            } else {
                list
            }
        }
        .themedGroupedPage()
        .detailTitle(String(localized: "To-dos"))
        .navigationBarTitleDisplayMode(.inline)
        .pageToolbar {
            ToolbarItem(placement: .primaryAction) {
                Button {
                    if viewMode == .calendar {
                        quickAdd = TodoCalendarSlot(day: calendarAnchor, time: nil)
                    } else {
                        creatingTitle = ""
                        creatingNotes = ""
                        showCreate = true
                    }
                } label: {
                    ThemedIcon(Theme.Icon.add)
                }
                .accessibilityLabel("New To-Do")
            }
        }
        .sheet(isPresented: $showCreate) {
            createSheet
        }
        .sheet(item: $quickAdd) { slot in
            TodoQuickAddSheet(slot: slot)
        }
        .task {
            await store.refresh()
            #if DEBUG
            // `-showDoneTodos YES`: the Done section starts open (screenshots of done todos).
            if UserDefaults.standard.bool(forKey: "showDoneTodos") { unfolded = [.other, .shelved, .done] }
            if let match = UserDefaults.standard.string(forKey: "openTodoTitled"),
               let todo = store.scopedTodos.first(where: { $0.title.contains(match) }) {
                pushed = TodoDetailRoute(id: todo.id)
            }
            #endif
        }
        // Another assistant is current: the list is its to-dos now, and one opened or being written is the one before's.
        .onChange(of: model.assistantSwitches) {
            pushed = nil
            showCreate = false
            quickAdd = nil
            openedGroups = []
        }
        .navigationDestination(item: $pushed) { r in TodoDetailView(todoID: r.id) }
        // read when the page opens, and for the assistant made current (a project made meanwhile shows up)
        .task(id: model.assistantSwitches) { await projects.load(model.api?.currentAssistantSettings()) }
    }

    private var list: some View {
        let state = listState()
        let result = state.result
        let calendar = TodoCalendar.userCalendar
        return List {
            // The search field, and what the filter leaves out of Needs you, right under the bar: no band of section
            // spacing between them and the first section (#3712).
            Section {
                searchRow
                if state.hiddenWaiting > 0 {
                    NoticeBanner(text: String(localized: "\(state.hiddenWaiting) more waiting on you, not in \(listFilter.title)"),
                                 tone: .caution, symbol: Theme.Icon.needsYou) {
                        Button("Show All") { withAnimation(Theme.Motion.state) { listFilter = .all } }
                            .accessibilityIdentifier("todos.filter.showAll")
                    }
                    .padding(.horizontal, Theme.Space.page)
                    .padding(.vertical, Theme.Space.xs)
                    .accessibilityIdentifier("todos.filter.hiddenWaiting")
                }
            }
            .listRowBackground(Color.clear)
            .listRowInsets(EdgeInsets())
            .listRowSeparator(.hidden)
            .listSectionSpacing(.custom(Theme.Space.s))

            if result.isEmpty {
                emptySection
            } else {
                let shown = TodosGrouping.resolved(grouping, hasTree: model.directionTree != nil)
                if shown == .direction, let tree = model.directionTree {
                    directionSections(tree, result: result)
                } else if shown == .project {
                    projectSections(result)
                } else {
                    ForEach(TodoSection.allCases, id: \.self) { section in
                        let groups = TodoListLogic.sorted(result.groups(section), by: listSort, calendar: calendar)
                        if !groups.isEmpty {
                            let folded = section.startsFolded && !isUnfolded(section)
                            // The to-dos its rows list, a merged row's related ones too: the number says what is under it.
                            sectionView(section, groups: groups, count: groups.reduce(0) { $0 + $1.members.count }, folded: folded)
                                .listSectionSpacing(folded ? .custom(Theme.Space.flush) : .compact)
                        }
                    }
                }
            }
        }
        .themedPage()
        // The first row close to the bar, as the Agents list keeps it: not the grouped list's first-section margin.
        .contentMargins(.top, Theme.Space.xs, for: .scrollContent)
        .refreshable {
            await store.refresh()
        }
        .task(id: inFront) {
            // a minute's tick moves to-dos across the 24 h / 48 h lines while the page is in front
            while inFront, !Task.isCancelled {
                try? await Task.sleep(for: .seconds(60))
                clock = .now
            }
        }
    }

    /// Beside List | Calendar in the list view (#3740): the filter shown as a chip that clears it, and one button whose
    /// menu picks the filter and the order. Both start as All and Recent Activity, the list as it always was.
    @ViewBuilder private var listControls: some View {
        // To-dos the search leaves, per filter: what the menu says under each (no sections worked out for it)
        let counts = TodoListLogic.counts(store.todos.filter(matchesQuery), now: clock, calendar: TodoCalendar.userCalendar)
        if listFilter != .all {
            FilterChip(title: listFilter.title, symbol: Theme.Icon.close, on: true) { listFilter = .all }
                .accessibilityLabel(String(localized: "Clear the \(listFilter.title) filter"))
                .accessibilityIdentifier("todos.filter.clear")
        }
        Menu {
            Section("Show") {
                // a toggle per choice: the menu's own checkmark and its "selected" for VoiceOver; the count as the subtitle
                ForEach(TodoListFilter.allCases, id: \.self) { f in
                    Toggle(isOn: choice(f == listFilter) { listFilter = f }) {
                        Text(f.title)
                        Text("\(counts[f] ?? 0) to-dos")
                    }
                    .accessibilityIdentifier("todos.filter.\(f.rawValue)")
                }
            }
            Section("Sort By") {
                ForEach(TodoListSort.allCases, id: \.self) { o in
                    Toggle(o.title, isOn: choice(o == listSort) { listSort = o })
                        .accessibilityIdentifier("todos.sort.\(o.rawValue)")
                }
            }
        } label: {
            ThemedIcon(listFilter == .all && listSort == .recent ? Theme.Icon.filter : Theme.Icon.filterActive)
                .font(Theme.Font.title)
                .tapTargetFrame()
        }
        .accessibilityLabel(String(localized: "Filter and sort"))
        .accessibilityValue(Text(verbatim: [listFilter.title, listSort.title]
            .joined(separator: String(localized: ", ", comment: "Separator between parts of an accessibility label"))))
        .accessibilityIdentifier("todos.filter")
    }

    /// A menu toggle that picks one choice: on while chosen; a tap chooses it (tapping the chosen one keeps it).
    private func choice(_ on: Bool, pick: @escaping () -> Void) -> Binding<Bool> {
        Binding(get: { on }, set: { _ in withAnimation(Theme.Motion.state) { pick() } })
    }

    /// By Pinned direction (#4352): a section per direction of the hub's tree with a to-do, in its order (the secretary's
    /// last), headed by its name and 「在做 N · 等你 N」, its to-dos in the tree's order; then Done, folded, as by status.
    @ViewBuilder private func directionSections(_ tree: DirectionTree, result: TodoTriageResult) -> some View {
        let triage = result.sections.values.joined().reduce(into: [String: TodoTriage]()) { all, g in
            all.merge(g.triage) { first, _ in first }
        }
        // the search and the filter (#3740) narrow them as they narrow the sections; the tree's order is kept
        let calendar = TodoCalendar.userCalendar
        let filter = listFilter
        let now = clock
        let keep: (TodoItem) -> Bool = { todo in
            matchesQuery(todo) && TodoListLogic.matches(todo, filter, now: now, calendar: calendar)
        }
        ForEach(DirectionTodoSections.build(tree, todos: store.byID, keep: keep)) { d in
            Section {
                ForEach(d.todos) { todo in todoRow(todo, triage: triage[todo.id]) }
            } header: {
                HStack(spacing: Theme.Space.s) {
                    Text(model.directionTitle(d.direction))
                        .font(Theme.Font.sectionHeader)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                    Spacer(minLength: Theme.Space.s)
                    DirectionCountsLine(direction: d.direction)
                }
                .accessibilityElement(children: .combine)
                .accessibilityAddTraits(.isHeader)
                .accessibilityIdentifier("todos.direction")
            }
        }
        let done = TodoListLogic.sorted(result.groups(.done), by: listSort, calendar: calendar)
        if !done.isEmpty {
            let folded = TodoSection.done.startsFolded && !isUnfolded(.done)
            sectionView(.done, groups: done, count: done.reduce(0) { $0 + $1.members.count }, folded: folded)
                .listSectionSpacing(folded ? .custom(Theme.Space.flush) : .compact)
        }
    }

    /// By project (#7077, the web's grouping by project): a section per knowledge-base project with open to-dos, where its
    /// most pressing one comes in the list, then Unsorted; then Done, folded, as by status. The search and the filter
    /// narrow them as they narrow the sections.
    @ViewBuilder private func projectSections(_ result: TodoTriageResult) -> some View {
        let calendar = TodoCalendar.userCalendar
        let open = TodoSection.allCases.filter { $0 != .done }.flatMap { section in
            TodoListLogic.sorted(result.groups(section), by: listSort, calendar: calendar).flatMap(\.members)
        }
        let triage = result.sections.values.joined().reduce(into: [String: TodoTriage]()) { all, g in
            all.merge(g.triage) { first, _ in first }
        }
        ForEach(TodoProjectLogic.sections(open, projects: projects.list)) { p in
            Section {
                ForEach(p.todos) { todo in todoRow(todo, triage: triage[todo.id]) }
            } header: {
                SectionHeader(title: p.title, count: p.todos.count, tone: StatusPalette.idle)
                    .font(Theme.Font.sectionHeader)
                    .textCase(nil)
                    .lineLimit(1)
                    .accessibilityElement(children: .combine)
                    .accessibilityAddTraits(.isHeader)
                    .accessibilityIdentifier("todos.project")
            }
        }
        let done = TodoListLogic.sorted(result.groups(.done), by: listSort, calendar: calendar)
        if !done.isEmpty {
            let folded = TodoSection.done.startsFolded && !isUnfolded(.done)
            sectionView(.done, groups: done, count: done.reduce(0) { $0 + $1.members.count }, folded: folded)
                .listSectionSpacing(folded ? .custom(Theme.Space.flush) : .compact)
        }
    }

    /// A folded section (Shelved, Done) is open when the user opened it, or while searching.
    private func isUnfolded(_ section: TodoSection) -> Bool {
        unfolded.contains(section) || !query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    @ViewBuilder private func sectionView(_ section: TodoSection, groups: [TodoTriageGroup], count: Int, folded: Bool) -> some View {
        Section {
            if !folded {
                if section == .shelved {
                    Text("No progress for 48 hours and nothing running. Not counted as open; nothing was deleted.")
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                }
                ForEach(groups) { group in
                    groupRows(group)
                }
            }
        } header: {
            // The Agents list's state header (#3712): the name, and the count in the state's color. Folding ones are a
            // FoldButton, spoken as "name, expanded / collapsed, N to-dos".
            if section.startsFolded {
                FoldButton(title: section.title, folded: folded, detail: String(localized: "\(count) to-dos")) {
                    withAnimation(Theme.Motion.state) {
                        if unfolded.contains(section) { unfolded.remove(section) } else { unfolded.insert(section) }
                    }
                } label: {
                    SectionHeader(title: section.title, count: count, tone: section.tone)
                }
                .font(Theme.Font.sectionHeader)
                .textCase(nil)
                .accessibilityIdentifier("todos.section.\(section.rawValue)")
            } else {
                SectionHeader(title: section.title, count: count, tone: section.tone)
                    .font(Theme.Font.sectionHeader)
                    .textCase(nil)
                    .accessibilityElement(children: .combine)
                    .accessibilityAddTraits(.isHeader)
                    .accessibilityIdentifier("todos.section.\(section.rawValue)")
            }
        }
    }

    /// A row: the group's lead, its "N related" inside it (under its words, #3712), and the others under it when open,
    /// a level in with a line down their side.
    @ViewBuilder private func groupRows(_ group: TodoTriageGroup) -> some View {
        let opened = openedGroups.contains(group.key)
        let related = group.related.isEmpty ? nil : TodoRelatedToggle(count: group.related.count, open: opened, key: group.key) {
            withAnimation(Theme.Motion.state) {
                if opened { openedGroups.remove(group.key) } else { openedGroups.insert(group.key) }
            }
        }
        todoRow(group.lead, triage: group.triage[group.lead.id], related: related)
        if opened {
            ForEach(group.related) { member in
                todoRow(member, triage: group.triage[member.id])
                    .padding(.leading, Theme.Space.xl)
                    .overlay(alignment: .leading) { Rectangle().fill(.separator).frame(width: Theme.Size.rule) }
            }
        }
    }

    private func todoRow(_ todo: TodoItem, triage: TodoTriage?, related: TodoRelatedToggle? = nil) -> some View {
        // a failed start waiting on the owner reads open: its checkbox and swipe close it (#3713)
        let open = TodoTriageLogic.checksAsOpen(todo, triage)
        // Got It on a Just finished row (#5707), while the hub keeps seen marks
        let seen: (() -> Void)? = triage?.section == .justDone && model.hubFeatures.contains(TodoSeen.feature)
            ? { markSeen(todo, after: triage?.last ?? .distantPast) } : nil
        return NavigationLink(value: TodoDetailRoute(id: todo.id)) {
            TodoRow(todo: todo, triage: triage, onToggleDone: { store.toggleDone(todo, done: open) },
                    onRestore: triage?.section == .shelved ? { restore(todo) } : nil, onSeen: seen, related: related)
        }
        .swipeActions(edge: .leading) {
            if triage?.section == .shelved {
                Button {
                    restore(todo)
                } label: {
                    Label("Restore", systemImage: Theme.Icon.restoreShelved)
                }
                .tint(Theme.accent)
            } else if !open {
                Button {
                    store.toggleDone(todo, done: false)
                } label: {
                    Label("Reopen", systemImage: Theme.Icon.undo)
                }
                .tint(Theme.accent)
            } else {
                Button {
                    store.toggleDone(todo, done: true)
                } label: {
                    Label("Done [Extension System Presentation c0b3fb]", systemImage: Theme.Icon.done)
                }
                .tint(Theme.positive)
            }
            // after the full swipe's action, which stays what it was
            if let seen {
                Button(action: seen) {
                    Label("Got It [To-do Seen]", systemImage: Theme.Icon.seen)
                }
                .tint(Theme.accent)
            }
        }
        .swipeActions(edge: .trailing) {
            Button(role: .destructive) {
                Task { do { try await store.delete(id: todo.id) } catch { AppModel.shared.show(error) } }
            } label: {
                Label("Delete", systemImage: Theme.Icon.delete)
            }
        }
        // VoiceOver: the row opens the to-do; its "N related" is an action of its own, not lost inside the row
        .modifier(RelatedAction(related: related))
        // The link merges its children and would otherwise take the "N related" button's identifier too
        .accessibilityIdentifier("todos.row.\(todo.id)")
    }

    /// Got It (#5707, docs/assistant/todo-status-sop.md "Seen"): filed under Done at once, the hub keeps the mark for
    /// every device, and the toast's Undo takes it off. `after`: the moment that put it in Just finished.
    private func markSeen(_ todo: TodoItem, after: Date) {
        let id = todo.id
        Haptics.success()
        Task {
            do { try await store.setSeen(id: id, true, after: after) } catch {
                AppModel.shared.toast = Toast(String(localized: "Couldn't move it to Done: \(error.localizedDescription)"), style: .error)
            }
        }
        AppModel.shared.toast = Toast(String(localized: "Moved to Done"), style: .success,
                                      action: Toast.Action(title: String(localized: "Undo")) {
            Task {
                do { try await TodoStore.shared.setSeen(id: id, false) } catch {
                    AppModel.shared.toast = Toast(String(localized: "Couldn't put it back: \(error.localizedDescription)"), style: .error)
                }
            }
        })
    }

    /// Restore from Shelved: counts as activity now, so it goes back to its section until another 48 hours pass.
    private func restore(_ todo: TodoItem) {
        withAnimation(Theme.Motion.layout) {
            clock = .now
            restored = TodoRestores.restore([todo.id], now: clock)
        }
    }

    private var searchRow: some View {
        HStack(spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.search)
                .font(Theme.Font.secondaryMedium)
                .foregroundStyle(.secondary)
            TextField(String(localized: "Search to-dos"), text: $query)
                .textFieldStyle(.plain)
            if !query.isEmpty {
                Button { query = "" } label: {
                    ThemedIcon(Theme.Icon.clear).foregroundStyle(.tertiary)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(.horizontal, Theme.Space.m)
        .scaledFrame(height: Theme.Size.avatar)
        .background(Theme.Fill.chip, in: .capsule)
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.xs)
    }

    @ViewBuilder private var emptySection: some View {
        Section {
            // The hub's merged list has nothing of an assistant that is offline: said so, not offered as a first to-do.
            if let offline = model.assistantManagerOffline {
                AssistantOfflineNotice(manager: offline, surface: Theme.groupedPage)
            } else if !query.isEmpty {
                EmptyStateView(symbol: Theme.Icon.todo, title: String(localized: "No matching to-dos"),
                               message: String(localized: "Try a different search"))
            } else if listFilter != .all {
                // the filter left nothing (#3740): say which, and the way back to every to-do
                EmptyStateView(symbol: Theme.Icon.dueDate, title: listFilter.emptyTitle, message: String(localized: "Try another filter."),
                               action: (String(localized: "Show All"), { withAnimation(Theme.Motion.state) { listFilter = .all } }))
            } else {
                EmptyStateView(
                    symbol: Theme.Icon.todo,
                    title: String(localized: "No to-dos yet"),
                    message: String(localized: "Write down something to do, hand it to an agent, and track its progress."),
                    action: (String(localized: "New To-Do"), { showCreate = true })
                )
            }
        }
        .listRowBackground(Color.clear)
        .listRowSeparator(.hidden)
    }

    private var createSheet: some View {
        NavigationStack {
            Form {
                Section {
                    TextField(String(localized: "Title"), text: $creatingTitle)
                        .lineLimit(1)
                        .onSubmit { add() }
                    HStack(alignment: .top, spacing: Theme.Space.s) {
                        TextField(String(localized: "Notes (sent along when handed to an agent)"), text: $creatingNotes, axis: .vertical)
                            .lineLimit(3...8)
                        FieldMicButton(dictation: dictation, text: $creatingNotes, id: "todo.new.notes.mic")
                            .font(Theme.Font.body)
                            .foregroundStyle(Theme.accent)
                    }
                }
            }
            .themedPage()
            .onDisappear(perform: stopDictation) // cancelled, swiped away or added
            .navigationTitle("New To-Do")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { showCreate = false }
                        .lineLimit(1)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Add [Beautiful U I Bridge 7a8a11]") { add() }
                        .lineLimit(1)
                        .disabled(creatingTitle.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                }
            }
        }
        .presentationDetents([.medium])
    }

    private func add() {
        let t = creatingTitle.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !t.isEmpty else { return }
        stopDictation() // what is saved is what the field shows
        let n = creatingNotes.trimmingCharacters(in: .whitespacesAndNewlines)
        // The sheet closes only once the todo is saved: a failed save keeps the typed title and notes.
        Task {
            do {
                _ = try await store.create(title: t, notes: n.isEmpty ? nil : n)
                showCreate = false
            } catch {
                AppModel.shared.show(error)
            }
        }
    }

    /// Ends dictation, and a start still asking for permission with it (#3407).
    private func stopDictation() {
        dictation.stop()
    }
}

private extension TodoSection {
    /// The header's count color: the state the section is named after, as Android's section headers and the web's
    /// tinted titles do; the rest plain.
    var tone: StatusTone {
        switch self {
        case .waiting: StatusPalette.needsYou
        case .working: StatusPalette.working
        case .justDone: StatusPalette.unread
        case .pr: StatusTone(dot: Theme.accent, text: Theme.accent)
        case .other, .shelved, .done: StatusPalette.idle
        }
    }
}

private extension TodoListFilter {
    /// The list's empty state when this filter leaves nothing.
    var emptyTitle: String {
        switch self {
        case .all: String(localized: "No to-dos yet")
        case .noDue: String(localized: "Every to-do has a due date")
        case .today: String(localized: "Nothing due today")
        case .week: String(localized: "Nothing due this week")
        case .month: String(localized: "Nothing due this month")
        }
    }
}

/// A merged row's "N related" as a VoiceOver action on the row (the row's own activation opens the to-do).
private struct RelatedAction: ViewModifier {
    var related: TodoRelatedToggle?

    func body(content: Content) -> some View {
        if let related {
            content.accessibilityAction(named: Text("\(related.count) related"), related.toggle)
        } else {
            content
        }
    }
}

/// A merged row's "N related" (#3712): drawn inside its lead's row, under its words, so it reads as that row's.
struct TodoRelatedToggle {
    var count: Int
    var open: Bool
    var key: String
    var toggle: () -> Void
}

struct TodoRow: View {
    @Environment(AppModel.self) private var model
    @Environment(\.tabIsActive) private var inFront
    @Environment(\.openURL) private var openURL
    var todo: TodoItem
    /// Its place on the list by status (#3206); nil where the row is drawn outside it.
    var triage: TodoTriage? = nil
    var onToggleDone: () -> Void
    /// Set for a shelved row: one tap brings it back.
    var onRestore: (() -> Void)? = nil
    /// Set for a Just finished row while the hub keeps seen marks: Got It files it under Done (#5707).
    var onSeen: (() -> Void)? = nil
    /// Set for a merged row's lead: its "N related", which opens the others under it.
    var related: TodoRelatedToggle? = nil

    var body: some View {
        let lookup = TodosLogic.makeSessionLookup(AppModel.shared.allSessions(inFront: inFront))
        let status = TodosLogic.todoStatus(todo, lookup: lookup)
        let urgent = TodosLogic.mostUrgentAgent(todo, lookup: lookup)
        let activeAgents = todo.agents.filter { $0.releasedAt == nil }
        let ultracode = Ultracode.todoShown(todo, lookup: lookup)
        // a failed start waiting on the owner reads open, not done, and says why in red (#3713, DESIGN_SYSTEM 5.6)
        let shownDone = !TodoTriageLogic.checksAsOpen(todo, triage)
        let failedOpen = !shownDone && todo.failedToStart

        HStack(alignment: .top, spacing: Theme.Space.m) {
            Button(action: onToggleDone) {
                ThemedIcon(shownDone ? Theme.Icon.successFilled : Theme.Icon.unchecked)
                    .font(Theme.Font.title)
                    .foregroundStyle(shownDone ? Theme.positive : .secondary)
            }
            .buttonStyle(.plain)
            .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.tapTarget)

            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(alignment: .firstTextBaseline) {
                    UltracodeMark.after(todo.title, on: ultracode, font: Theme.Font.labelStrong)
                        .font(Theme.Font.bodyStrong)
                        .foregroundStyle(shownDone ? Color.secondary : Color.primary)
                        .strikethrough(shownDone)
                        .lineLimit(2)
                        .animation(UltracodeMark.motion, value: ultracode)
                    Spacer(minLength: Theme.Space.xs)
                    if let d = todo.updatedAt ?? todo.createdAt {
                        Text(Fmt.relative(d))
                            .font(Theme.Font.label)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                }

                HStack(spacing: Theme.Space.sm) {
                    StatusDot(state: urgent?.status ?? (status == .done ? .idle : .unknown), size: Theme.Size.dotS)
                    Text(todo.statusLabel(status))
                        .font(Theme.Font.labelStrong)
                        .foregroundStyle(todo.shownState(status).todoTone.text)
                    if !activeAgents.isEmpty {
                        Text("· \(activeAgents.count) agents")
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }
                    if !todo.isDone { ReviewChip(todo: todo, compact: true) }
                }

                // Nobody is working on it (#3618): under its state, where its supervision shows.
                OrphanedLine(todo: todo, state: status)

                TodoPlanLine(todo: todo)

                if triage?.section == .pr, let pr = todo.prLink {
                    HStack(spacing: Theme.Space.s) {
                        Button {
                            openURL(pr)
                        } label: {
                            Label(todo.prLabel ?? String(localized: "View PR"), systemImage: Theme.Icon.pullRequest)
                                .lineLimit(1)
                        }
                        .font(Theme.Font.captionStrong)
                        .foregroundStyle(Theme.accent)
                        .buttonStyle(.borderless)
                        if let last = triage?.last, last > .distantPast {
                            Text("Last moved \(Fmt.relative(last))")
                                .font(Theme.Font.caption)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                        }
                    }
                }

                if let onRestore {
                    Button(action: onRestore) {
                        Label("Restore", systemImage: Theme.Icon.restoreShelved)
                            .lineLimit(1)
                    }
                    .font(Theme.Font.captionStrong)
                    .foregroundStyle(Theme.accent)
                    .buttonStyle(.borderless)
                    .accessibilityIdentifier("todos.restore")
                }

                if let onSeen {
                    Button(action: onSeen) {
                        Label("Got It [To-do Seen]", systemImage: Theme.Icon.seen)
                            .lineLimit(1)
                            .scaledFrame(minHeight: Theme.Size.tapTarget, alignment: .leading)
                            .contentShape(.rect)
                    }
                    .font(Theme.Font.captionStrong)
                    .foregroundStyle(Theme.accent)
                    .buttonStyle(.borderless)
                    .accessibilityLabel(Text("Got It: move \(todo.title) to Done"))
                    .accessibilityIdentifier("todos.seen.\(todo.id)")
                }

                if failedOpen, let error = todo.startError {
                    Text("Failed to start: \(error)")
                        .font(Theme.Font.caption)
                        .foregroundStyle(Theme.danger)
                        .lineLimit(2)
                } else if let reason = todo.closedReason {
                    Text(reason)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(2)
                } else if let summary = mostUrgentSummary(urgent: urgent) {
                    Text(summary)
                        .font(Theme.Font.caption)
                        .foregroundStyle(urgent?.status == .needsYou ? StatusPalette.needsYou.text : .secondary)
                        .lineLimit(2)
                } else if let notes = todo.notes, !notes.isEmpty {
                    Text(notes)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                } else {
                    Text("Not handed to an agent yet")
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .lineLimit(1)
                }

                if let related {
                    Button(action: related.toggle) {
                        HStack(spacing: Theme.Space.xs) {
                            ThemedIcon(related.open ? Theme.Icon.expand : Theme.Icon.disclosure)
                                .font(Theme.Font.label)
                            Text("\(related.count) related")
                                .lineLimit(1)
                        }
                        .font(Theme.Font.captionStrong)
                        .foregroundStyle(.secondary)
                        .scaledFrame(minHeight: Theme.Size.tapTarget, alignment: .leading)
                        .contentShape(.rect)
                    }
                    .buttonStyle(.borderless)
                    .accessibilityValue(related.open ? Text("Expanded") : Text("Collapsed"))
                    .accessibilityIdentifier("todos.related.\(related.key)")
                }
            }
            .padding(.vertical, Theme.Space.xs)
        }
    }

    private func mostUrgentSummary(urgent: TodoMostUrgent?) -> String? {
        guard let urgent else { return nil }
        if urgent.isFailed, let err = urgent.agent.startError, !err.isEmpty {
            return String(localized: "Failed to start: \(err)")
        }
        if let s = urgent.session {
            if let last = s.lastText, !last.isEmpty {
                return Fmt.plain(last)
            }
            return s.displayTitle
        }
        if let t = urgent.agent.title, !t.isEmpty {
            return t
        }
        return nil
    }
}
