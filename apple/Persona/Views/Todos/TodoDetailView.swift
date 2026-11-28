import BeautifulUI
import SwiftUI

struct TodoDetailView: View {
    @Environment(AppModel.self) private var model
    @Environment(\.tabIsActive) private var inFront
    @Environment(\.dismiss) private var dismiss
    @Environment(\.shownAsSheet) private var shownAsSheet
    /// An Agent this page opens, pushed over it with Back to it (#12006).
    @State private var pushedAgent: SessionKey?
    var todoID: String
    /// In an existing value-based stack, lineage buttons append here. Sheet presentations leave it nil and use their
    /// own NavigationLink destination below.
    var openDetail: ((TodoDetailRoute) -> Void)? = nil
    /// Not `openAgent`: a stored property's name stays in the Release binary, and that is a DEBUG launch switch (#8306).
    var onOpenAgent: ((SessionKey) -> Void)? = nil

    @State private var store = TodoStore.shared
    @State private var tasks = TasksStore.shared
    @State private var text = TodoEditorText()
    @FocusState private var titleFocused: Bool
    @State private var isEditingNotes = false
    @State private var editingNotesText = ""
    /// The notes' mic (#2188): Done and leaving the page end it.
    @State private var dictation = Dictation()
    @State private var showHandoff = false
    @State private var showDeleteConfirm = false
    @State private var showReleased = false
    /// The title the field was last filled with: only a change from it is the user's edit. A title changed elsewhere
    /// while the field had focus is not taken into it, and must not be written back over (#3330).
    @State private var titleBaseline = ""
    /// The title being saved, so one edit is not sent twice.
    @State private var savingTitle: String?
    /// The PR its work is in, opened in the app's link sheet (docs/apple/ios.md, links open inside the app).
    @State private var link: LinkTarget?
    @State private var decisions = TodoDecisionsModel()

    private var currentTodo: TodoItem? {
        store.todo(id: todoID)
    }

    /// Marks the to-do seen when it is in Just finished and the hub keeps seen marks; a failure leaves it there.
    private func seeIfJustFinished() async {
        guard let todo = currentTodo, model.hubFeatures.contains(TodoSeen.feature) else { return }
        // no restores read (UserDefaults, P2): a restore only takes a to-do out of Shelved, never into Just finished; the
        // sessions as they are now, not watched (an action, AllSessionsReadersTests)
        let lookup = TodosLogic.makeSessionLookup(model.sessionsNow)
        let t = TodoTriageLogic.triage(todo, lookup: lookup, now: .now,
                                       superseded: TodoTriageLogic.supersededFailedStarts(store.todos).contains(todo.id),
                                       calendar: TodoCalendar.userCalendar)
        guard t.section == .justDone else { return }
        try? await store.setSeen(id: todo.id, true, after: t.last)
    }

    var body: some View {
        Group {
            if let todo = currentTodo {
                content(todo)
            } else {
                VStack(spacing: Theme.Space.l) {
                    EmptyStateView(symbol: Theme.Icon.todo, title: String(localized: "This to-do doesn't exist or was deleted"), message: "")
                }
                .themedPage()
            }
        }
        .detailTitle(currentTodo?.title ?? String(localized: "To-Do Details"))
        .navigationBarTitleDisplayMode(.inline)
        .navigationDestination(for: TodoDetailRoute.self) { route in
            TodoDetailView(todoID: route.id, openDetail: openDetail, onOpenAgent: onOpenAgent)
        }
        .pageToolbar {
            ToolbarItem(placement: .primaryAction) {
                Menu {
                    Button(role: .destructive) {
                        showDeleteConfirm = true
                    } label: {
                        Label("Delete To-Do", systemImage: Theme.Icon.delete)
                    }
                } label: {
                    ThemedIcon(Theme.Icon.more)
                }
            }
        }
        .pushedAgentChat($pushedAgent)
        .sheet(isPresented: $showHandoff) {
            if let todo = currentTodo {
                // The Agent it is handed to is pushed over this page; with nothing to push on, it opens as before (#12006).
                TodoHandoffSheet(todo: todo, onOpenAgent: onOpenAgent == nil && pageAgentPush(inSheet: shownAsSheet, { _ in }) == nil
                                 ? nil : { openAgent($0) })
            }
        }
        .sheet(item: $decisions.form) { form in
            TodoDecisionSheet(supersedes: form.supersedes) { question, choice, reason in
                await decisions.add(question: question, choice: choice, reason: reason, supersedes: form.supersedes)
            }
        }
        // The hub moves the todo's updated_at when a decision is written, here or anywhere (an answered card too).
        .tabActiveTask(id: "\(todoID) \(currentTodo?.updatedAt?.timeIntervalSince1970 ?? 0)") {
            decisions.select(todoID)
            #if DEBUG
            // `-showDecisionHistory YES`: the revised ones are shown from the start, for screenshots.
            if UserDefaults.standard.bool(forKey: "showDecisionHistory"), decisions.all == nil { decisions.showAll = true }
            #endif
            await decisions.load()
        }
        .tabActiveTask(id: "start-remedy-\(todoID)-\(currentTodo?.origin ?? "")") {
            guard let todo = currentTodo else { return }
            await tasks.loadStartRemedy(todo)
        }
        .sheet(item: $link) { target in
            LinkSheet(target: target, key: model.assistantMain ?? model.assistantCurrent)
        }
        .alert("Delete To-Do", isPresented: $showDeleteConfirm) {
            Button("Delete", role: .destructive) {
                Task { await deleteTodo() }
            }
            Button("Cancel", role: .cancel) {}
        } message: {
            Text("Delete this to-do? Its linked agent sessions won't be deleted.")
        }
        .onChange(of: currentTodo) { _, todo in
            // Another todo (the Mac list picked it), or an edit made elsewhere: take the stored text, but never over a
            // field the user is typing in.
            if let todo {
                text.reseed(from: todo, editingTitle: titleFocused, editingNotes: isEditingNotes)
                if !titleFocused { titleBaseline = todo.title }
            }
        }
        // Return is not the only way out of the title: tapping elsewhere or leaving the page saves it too (#3330).
        .onChange(of: titleFocused) { _, focused in
            if !focused { commitTitle() }
        }
        .onDisappear { commitTitle() }
        // Opening a Just finished to-do is seeing it (#5707): it is filed under Done, where the list has it on the way
        // back. Nothing is said: the user did not ask for it.
        .task(id: currentTodo != nil) { await seeIfJustFinished() }
        // A tab switch hides the page without onDisappear: the mic must not go on listening behind it.
        .onTabVisibility(appear: {}, disappear: stopDictation)
        .onAppear {
            #if DEBUG
            if UserDefaults.standard.bool(forKey: "showTodoHandoff") {
                showHandoff = true
            }
            #endif
            if let todo = currentTodo {
                text = TodoEditorText(todo)
                titleBaseline = todo.title
            }
        }
    }

    /// Saves the edited title if the user changed it. An emptied title goes back to the stored one rather than staying
    /// blank.
    private func commitTitle() {
        guard let todo = currentTodo else { return }
        let t = text.title.trimmingCharacters(in: .whitespacesAndNewlines)
        // Nothing typed (or only spaces around it): show the stored title, which may have changed elsewhere meanwhile.
        guard !t.isEmpty, t != titleBaseline, t != todo.title else {
            if !titleFocused, savingTitle == nil { text.title = todo.title; titleBaseline = todo.title }
            return
        }
        // Return and the focus change that follows it both land here: send each new title once.
        guard t != savingTitle else { return }
        savingTitle = t
        Task {
            do { _ = try await store.update(id: todo.id, title: t) } catch { model.show(error) }
            if savingTitle == t { savingTitle = nil }
        }
    }

    /// Ends dictation, and a start still asking for permission with it (#3407).
    private func stopDictation() {
        dictation.stop()
    }

    /// Deletes the todo and leaves its page; a failed delete keeps the page and shows the error.
    private func deleteTodo() async {
        do {
            try await store.delete(id: todoID)
            dismiss()
        } catch {
            report(error)
        }
    }

    /// Shows a failed write's error, from a plain method rather than inside the async block.
    private func report(_ failure: any Error) {
        TodoStore.reportWriteFailure(failure)
    }

    private func content(_ todo: TodoItem) -> some View {
        let sessions = model.allSessions(inFront: inFront)
        let lookup = TodosLogic.makeSessionLookup(sessions)
        let status = TodosLogic.todoStatus(todo, lookup: lookup)
        let activeAgents = todo.agents.filter { $0.releasedAt == nil }
        let releasedAgents = todo.agents.filter { $0.releasedAt != nil }

        return ScrollViewReader { proxy in List {
            Section {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    TextField(String(localized: "To-do title"), text: $text.title)
                        .font(Theme.Font.title)
                        .focused($titleFocused)
                        .onSubmit { commitTitle() }

                    ViewThatFits(in: .horizontal) {
                        HStack(spacing: Theme.Space.sm) {
                            Chip(text: todo.statusLabel(status), look: .tone(todo.shownState(status).todoTone))
                            Text("·")
                                .foregroundStyle(.secondary)
                            Text("\(activeAgents.count) agents")
                                .font(Theme.Font.caption)
                                .foregroundStyle(.secondary)
                                .lineLimit(1)
                            if let d = todo.createdAt {
                                Text("·")
                                    .foregroundStyle(.secondary)
                                Text("Created \(Fmt.relative(d))")
                                    .font(Theme.Font.caption)
                                    .foregroundStyle(.secondary)
                                    .lineLimit(1)
                            }
                        }
                        .fixedSize()
                        VStack(alignment: .leading, spacing: Theme.Space.xs) {
                            Chip(text: todo.statusLabel(status), look: .tone(todo.shownState(status).todoTone))
                            Text("\(activeAgents.count) agents")
                                .font(Theme.Font.caption)
                                .foregroundStyle(.secondary)
                            if let d = todo.createdAt {
                                Text("Created \(Fmt.relative(d))")
                                    .font(Theme.Font.caption)
                                    .foregroundStyle(.secondary)
                            }
                        }
                    }

                    if let reason = todo.cancellationReason, !reason.isEmpty {
                        Text(String(localized: "Cancelled: \(reason) [Todo Detail]"))
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }

                    // Nobody is working on it (#3618): under its state.
                    OrphanedLine(todo: todo, state: status)

                    if todo.autoDone {
                        // The hub closed it (docs/assistant/todos.md「Automatic completion」): say why, and how to undo it.
                        Text(todo.autoDoneReason.map { String(localized: "The agent stopped and the work looked finished, so it was marked done: \($0). If it isn't, tap “Reopen”.") }
                             ?? String(localized: "The agent stopped and the work looked finished, so it was marked done. If it isn't, tap “Reopen”."))
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                }
                .padding(.vertical, Theme.Space.xs)
            }

            if let wait = todo.wait {
                TodoWaitSection(wait: wait, openDetail: openDetail) { url in
                    link = LinkTarget(url: url)
                }
            }

            TodoPlanSection(todo: todo).id(todo.id) // due date, reminder, priority (#1683)

            if todo.supervision != nil {
                // A task the main conversation supervises (#3124): its criteria as the latest review found them.
                Section { ReviewChecklist(todo: todo).padding(.vertical, Theme.Space.xs) }
            }

            if let lineage = todo.lineage, !lineage.isEmpty {
                TodoLineageSection(steps: lineage, openDetail: openDetail)
            }

            stageSection(todo)

            if let failure = tasks.startFailure(todo) {
                Section {
                    let summary = tasks.startFailureSummary(todo) ?? failure
                    ReadinessCard(title: String(localized: "The agent didn't start"), detail: summary, tone: .danger,
                                  details: summary == failure ? [] : [failure])
                    if let task = tasks.planTask(todo) { TaskShellQuestion(todo: todo, task: task) }
                }
            }

            Section {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    HStack {
                        Text("Notes")
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                        Spacer()
                        FieldMicButton(dictation: dictation, text: $editingNotesText, id: "todo.notes.mic") {
                            // On the notes as they are, one tap opens the editor and dictates into it.
                            if !isEditingNotes {
                                editingNotesText = text.notes
                                isEditingNotes = true
                            }
                        }
                        .font(Theme.Font.captionStrong)
                        .foregroundStyle(Theme.accent)
                        Button(isEditingNotes ? String(localized: "Done [Extension System Presentation c0b3fb]") : String(localized: "Edit [Message Menu 051836]")) {
                            if isEditingNotes {
                                stopDictation() // what is saved is what the field shows
                                let n = editingNotesText.trimmingCharacters(in: .whitespacesAndNewlines)
                                Task {
                                    do {
                                        _ = try await store.update(id: todo.id, notes: n)
                                        text.notes = n
                                        isEditingNotes = false
                                    } catch {
                                        // Still editing: what was typed stays in the field, and the error says why it did not save.
                                        model.show(error)
                                    }
                                }
                            } else {
                                editingNotesText = text.notes
                                isEditingNotes = true
                            }
                        }
                        .font(Theme.Font.captionStrong)
                        .lineLimit(1)
                        .foregroundStyle(Theme.accent)
                        .accessibilityIdentifier("todo.notes.edit")
                    }

                    if isEditingNotes {
                        TextField(String(localized: "To-do notes"), text: $editingNotesText, axis: .vertical)
                            .readingContent()
                            .lineLimit(3...8)
                            .textFieldStyle(.plain)
                            .accessibilityIdentifier("todo.notes.field")
                            .padding(Theme.Space.s)
                            .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
                    } else if !text.notes.isEmpty {
                        Text(text.notes)
                            .readingContent()
                            .font(Theme.Font.body)
                            .foregroundStyle(Color.primary)
                            .lookUpWords(plain: text.notes)
                            .textLookupSurface("todo") // a tap on a word looks it up (#10977); the field above never does
                    } else {
                        Text("No notes yet")
                            .font(Theme.Font.body)
                            .foregroundStyle(.tertiary)
                    }
                }
                .padding(.vertical, Theme.Space.xs)
                #if DEBUG
                .id(Self.notesRow)
                #endif
            }

            TodoDecisionsSection(todo: todo, lookup: lookup, decisions: decisions)

            TodoSpacesSection(todo: todo, sessions: sessions, machineName: machineName, hubGroup: model.hub?.group,
                              machineOnline: { model.machine(model.hubRef($0), inFront: inFront)?.online ?? false })

            Section {
                if activeAgents.isEmpty {
                    Text(todo.isDone ? String(localized: "No agents on it now. Reopen it to hand it to an agent.")
                                     : String(localized: "No agents are running. Tap “Hand to Agent” below to assign one."))
                        .font(Theme.Font.secondary)
                        .foregroundStyle(.secondary)
                        .padding(.vertical, Theme.Space.xs)
                } else {
                    ForEach(activeAgents, id: \.sessionID) { a in
                        agentRow(a, todo: todo, isReleased: false, lookup: lookup)
                    }
                }
            } header: {
                Text("Agents · \(activeAgents.count)")
                    .font(Theme.Font.sectionHeader)
                    .foregroundStyle(.secondary)
            }

            if !releasedAgents.isEmpty {
                Section {
                    DisclosureGroup(isExpanded: $showReleased) {
                        ForEach(releasedAgents, id: \.sessionID) { a in
                            agentRow(a, todo: todo, isReleased: true, lookup: lookup)
                        }
                    } label: {
                        Text("Earlier · \(releasedAgents.count)")
                            .font(Theme.Font.sectionHeader)
                            .foregroundStyle(.secondary)
                    }
                }
            }
        }
        .themedPage()
        // The buttons float over the notes, which scroll on behind them and blur under them, with no band of page
        // color (on one they sat on a white strip, #9008).
        .floatingFooter(over: Theme.groupedPage) {
            bottomBar(todo, lookup: lookup)
        }
        #if DEBUG
        // `-showDecisionHistory YES` also scrolls the notes to the top once the decisions are read, so the whole
        // Decisions section under them is in the screenshot.
        .onChange(of: decisions.all != nil) { _, read in
            if read, UserDefaults.standard.bool(forKey: "showDecisionHistory") { proxy.scrollTo(Self.notesRow, anchor: .top) }
        }
        #endif
        }
    }

    #if DEBUG
    private static let notesRow = "todo.notes"
    #endif

    /// How far the hub says the work got (`stage`, #2776) and the PR its work is in (`pr_url`, #2768), when it has
    /// either; a hub that sends neither draws nothing here.
    @ViewBuilder private func stageSection(_ todo: TodoItem) -> some View {
        let stage = todo.todoStage
        let pr = todo.prLink
        if stage != nil || pr != nil {
            Section {
                VStack(alignment: .leading, spacing: Theme.Space.m) {
                    if let stage {
                        TodoStageBar(stage: stage, done: todo.stageDone)
                    }
                    if let pr {
                        Button { link = LinkTarget(url: pr) } label: {
                            Label(todo.prLabel ?? String(localized: "View PR"), systemImage: Theme.Icon.pullRequest)
                                .font(Theme.Font.captionStrong)
                                .lineLimit(1)
                        }
                        .themedButton()
                    }
                }
                .padding(.vertical, Theme.Space.xs)
            }
        }
    }

    /// The machine's name as the other pages show it (`model.machine(...)?.title`, the same as the Agents rows): the hub's
    /// own machines, since a todo belongs to the hub (`AppModel.hubRef`).
    private func machineName(_ id: String) -> String {
        model.machine(model.hubRef(id), inFront: inFront)?.title ?? id
    }

    private func agentRow(_ a: TodoItem.Agent, todo: TodoItem, isReleased: Bool, lookup: @escaping TodosLogic.SessionLookup) -> some View {
        let key = SessionKey(group: model.hub?.group ?? "", machineID: a.machineID, sessionID: a.sessionID)
        let s = lookup(a.machineID, a.sessionID)
        let isFailed = a.startError.map { !$0.isEmpty } ?? false
        let title = a.title ?? s?.displayTitle ?? String(localized: "Agent Session")
        let subtitle: String = {
            if isFailed, let err = a.startError {
                return String(localized: "Failed to start: \(err)")
            }
            if isReleased {
                return String(localized: "Ended")
            }
            if let s, let last = s.lastText, !last.isEmpty {
                return Fmt.plain(last)
            }
            return a.cwd ?? ""
        }()

        return Button {
            openAgent(key)
        } label: {
            HStack(alignment: .top, spacing: Theme.Space.m) {
                if isFailed {
                    ThemedIcon(Theme.Icon.errorFilled)
                        .foregroundStyle(Theme.danger)
                        .font(Theme.Font.body)
                        .padding(.top, Theme.Space.xs)
                } else {
                    StatusDot(state: isReleased ? .idle : (s?.state ?? .offline), size: Theme.Size.dotL)
                        .padding(.top, Theme.Space.xs)
                }

                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    HStack {
                        Text(title)
                            .font(Theme.Font.bodyStrong)
                            .lineLimit(1)
                        Spacer()
                        if let d = a.linkedAt {
                            Text(Fmt.relative(d))
                                .font(Theme.Font.label)
                                .foregroundStyle(.secondary)
                        }
                    }

                    let place = [machineName(a.machineID), s?.spaceName, s?.slotLabel].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
                    if !place.isEmpty {
                        Text(place)
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }

                    if let source = TodosLogic.agentSource(a, lookup: lookup) {
                        Text(source)
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                            .lineLimit(1)
                    }

                    Text(subtitle)
                        .font(Theme.Font.secondary)
                        .foregroundStyle(isFailed ? Theme.danger : s?.state == .needsYou ? StatusPalette.needsYou.text : .secondary)
                        .lineLimit(2)
                }
                ThemedIcon(Theme.Icon.disclosure)
                    .font(Theme.Font.label)
                    .foregroundStyle(.tertiary)
                    .padding(.top, Theme.Space.xs)
            }
            .padding(.vertical, Theme.Space.xs)
        }
        .buttonStyle(.plain)
        .swipeActions(edge: .trailing) {
            Button(role: .destructive) {
                Task {
                    do {
                        try await store.unlinkAgent(todoID: todo.id, machineID: a.machineID, sessionID: a.sessionID)
                    } catch {
                        model.show(error)
                    }
                }
            } label: {
                Label(isFailed ? String(localized: "Remove [Task Sheets 6135d4]") : String(localized: "Unlink"), systemImage: isFailed ? Theme.Icon.delete : Theme.Icon.link)
            }
        }
    }

    private func bottomBar(_ todo: TodoItem, lookup: @escaping TodosLogic.SessionLookup) -> some View {
        // Side by side while both labels fit whole, else one above the other, Hand to Agent first (#6151): side by side
        // they were cut to "Hand to A…" from xxLarge.
        // Two pills floating over the notes, the Sidebar layout's foot's (`floatingPill`, #4206): 46 pt as its New
        // Chat, apart from each other, with nothing behind them but the notes' blur. On a wide page (an iPad) the row
        // is no wider than a readable page, centered, as the Inbox's hand-off bar. At the large control size across
        // the whole page they were 61 pt tall and on an iPad 380 pt wide each, on a white strip (#9008).
        StackingButtonRow(spacing: Theme.Space.m, stackedSpacing: Theme.Space.s) {
            bottomButtons(todo, lookup: lookup)
        }
        .frame(maxWidth: Theme.Size.readable)
        .frame(maxWidth: .infinity)
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.s)
    }

    /// Hand to Agent the Theme Color's pill, the one solid button; Done / Reopen the glass one. Each label is measured
    /// at its full size (`minimumScale: 1`): with the row's 0.8 allowance "Hand to Agent" stayed beside Done at
    /// xxxLarge, cut to "Hand to Age…" instead of shrunk (#9008).
    @ViewBuilder private func bottomButtons(_ todo: TodoItem, lookup: @escaping TodosLogic.SessionLookup) -> some View {
        // A failed start can carry done_at while it still checks open and waits for its owner (#3713). The page's own
        // lookup (`content`), not a second read of every Agent (AllSessionsReadersTests).
        let open = TodoTriageLogic.checksAsOpen(todo, among: { store.todos }, lookup: { lookup }, now: .now)
        let canRetry = open && tasks.state(todo) == .failed && !(todo.origin ?? "").isEmpty
        let instead = canRetry ? tasks.retryLauncher(todo, features: model.hubFeatures) : nil
        let busy = tasks.busyRetries.contains(todo.id)
        if let instead {
            Button { Task { await tasks.retry(todo, launcher: instead, features: model.hubFeatures) } } label: {
                HStack(spacing: Theme.Space.xs) {
                    ThemedIcon(Theme.Icon.retry)
                    Text("Use \(instead) instead").buttonLabelLine(minimumScale: 1)
                }
                .opacity(busy ? 0 : 1).overlay { if busy { ProgressView().controlSize(.small) } }
                .floatingPill(prominent: true)
            }
            .buttonStyle(.plain)
            .disabled(busy)
            .accessibilityShowsLargeContentViewer()
            .accessibilityIdentifier("todo.retry-launcher")
        }
        if canRetry {
            Button { Task { await tasks.retry(todo, features: model.hubFeatures) } } label: {
                HStack(spacing: Theme.Space.xs) {
                    ThemedIcon(Theme.Icon.retry)
                    Text("Retry [Components b8784c]").buttonLabelLine(minimumScale: 1)
                }
                .opacity(busy ? 0 : 1).overlay { if busy { ProgressView().controlSize(.small) } }
                .floatingPill(prominent: instead == nil)
            }
            .buttonStyle(.plain)
            .disabled(busy)
            .accessibilityShowsLargeContentViewer()
            .accessibilityIdentifier("todo.retry")
        }
        // A done to-do can't be handed over (the hub refuses, #1856): Reopen comes first.
        if open {
            Button {
                showHandoff = true
            } label: {
                HStack(spacing: Theme.Space.xs) {
                    ThemedIcon(Theme.Icon.handToAI)
                    // Its own string (#6151): the sheet's title keeps the longer Japanese, which the half-width button cut off.
                    Text("Hand to Agent [To-do Page Button]").buttonLabelLine(minimumScale: 1)
                }
                .floatingPill(prominent: !canRetry)
            }
            .buttonStyle(.plain)
            .accessibilityShowsLargeContentViewer()
            .accessibilityIdentifier("todo.handToAgent")
        }

        Button {
            store.toggleDone(todo)
        } label: {
            HStack(spacing: Theme.Space.xs) {
                ThemedIcon(open ? Theme.Icon.done : Theme.Icon.undo)
                Text(open ? String(localized: "Done [Extension System Presentation c0b3fb]") : String(localized: "Reopen")).buttonLabelLine(minimumScale: 1)
            }
            .floatingPill(prominent: false)
        }
        .buttonStyle(.plain)
        .accessibilityShowsLargeContentViewer()
        .accessibilityIdentifier("todo.toggleDone")
    }
}

/// The two directions of a task hand-off; each non-current card opens in the same detail stack (#5732).
private struct TodoLineageSection: View {
    let steps: [TodoLineageStep]
    let openDetail: ((TodoDetailRoute) -> Void)?

    var body: some View {
        Section(String(localized: "Who Worked on It [Todo Detail]")) {
            ForEach(steps) { step in
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    HStack(spacing: Theme.Space.s) {
                        if step.current == true {
                            Text(step.title ?? step.todoID)
                                .font(Theme.Font.bodyStrong)
                            Text(String(localized: "Current Task [Todo Detail]"))
                                .font(Theme.Font.caption)
                                .foregroundStyle(.secondary)
                        } else {
                            routeLink(TodoDetailRoute(id: step.todoID), identifier: "todo.lineage.open.\(step.todoID)") {
                                Text(step.title ?? step.todoID)
                                    .font(Theme.Font.body)
                                    .foregroundStyle(Theme.accent)
                                    .lineLimit(1)
                            }
                        }
                    }
                    if let why = step.why, !why.isEmpty {
                        Text(why)
                            .font(Theme.Font.caption)
                            .foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    if let id = step.continues {
                        lineageLink(id: id, continues: true)
                    }
                    if let id = step.supersededBy {
                        lineageLink(id: id, continues: false)
                    }
                }
                .padding(.vertical, Theme.Space.xs)
            }
        }
    }

    private func title(_ id: String) -> String {
        steps.first(where: { $0.todoID == id })?.title ?? id
    }

    @ViewBuilder private func lineageLink(id: String, continues: Bool) -> some View {
        let name = title(id)
        routeLink(TodoDetailRoute(id: id), identifier: "todo.lineage.\(continues ? "continues" : "superseded").\(id)") {
            Text(continues
                 ? String(localized: "Continues From \(name) [Todo Detail]")
                 : String(localized: "Continued By \(name) [Todo Detail]"))
                .font(Theme.Font.captionStrong)
                .foregroundStyle(Theme.accent)
                .lineLimit(1)
        }
    }

    /// A button uses the path injected by the containing stack. NavigationLink is only for a sheet's own stack.
    @ViewBuilder private func routeLink<Label: View>(_ route: TodoDetailRoute, identifier: String,
                                                     @ViewBuilder label: () -> Label) -> some View {
        if let openDetail {
            Button(action: { openDetail(route) }, label: label)
                .buttonStyle(.plain)
                .accessibilityIdentifier(identifier)
        } else {
            NavigationLink(value: route, label: label)
                .buttonStyle(.plain)
                .accessibilityIdentifier(identifier)
        }
    }
}

extension TodoDetailView {
    /// An Agent of this to-do (a row, or the one it was just handed to): pushed over this page with Back to it, a drill-down
    /// (#12006). In a sheet (or on the Mac) the page goes and the Agent opens as before.
    func openAgent(_ key: SessionKey) {
        if let onOpenAgent { return onOpenAgent(key) }
        let push = pageAgentPush(inSheet: shownAsSheet) { pushedAgent = $0 }
        if push == nil { dismiss() }
        openTodoAgentChat(key, in: model, push: push)
    }
}

/// Opens an Agent's chat from a todo. On Mac the todos section gives way first, as it does for every other way into a
/// chat (`MacShellState.willOpen`); the hand-off sheet and the todo's Agent rows share this. `push`: the stack of the page
/// it is opened from, which it goes on top of (`AppModel.openInPage`); nil from the sidebar.
@MainActor func openTodoAgentChat(_ key: SessionKey, in model: AppModel, push: ((SessionKey) -> Void)? = nil) {
    model.openInPage(key, push: push)
}
