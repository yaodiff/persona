import SwiftUI

/// The to-dos page's two views (#2029): the list, or the calendar of the to-dos that have a due date.
enum TodosViewMode: String, CaseIterable {
    case list, calendar
}

/// How the to-dos list is grouped (#4352): by status section (the default), by knowledge-base project (#7077, the web's
/// grouping by project), or by Pinned direction from the hub's direction tree, offered only by a hub that has one.
enum TodosGrouping: String, CaseIterable {
    case status, project, direction

    var title: String {
        switch self {
        case .status: String(localized: "Status")
        case .project: String(localized: "Project")
        case .direction: String(localized: "Directions")
        }
    }

    /// The Group By menu's mark for it, as the Agents page's groupings have one each.
    var symbol: String {
        switch self {
        case .status: Theme.Icon.byState
        case .project: Theme.Icon.space
        case .direction: Theme.Icon.pin
        }
    }

    /// The grouping the list shows: the stored choice, but by status for Directions from a hub without the tree (an
    /// older hub, or before it is read), whatever was chosen against another hub.
    static func resolved(_ stored: TodosGrouping, hasTree: Bool) -> TodosGrouping {
        stored == .direction && !hasTree ? .status : stored
    }

    /// What the Group By menu offers: Directions only with the hub's tree.
    static func offered(hasTree: Bool) -> [TodosGrouping] { hasTree ? allCases : [.status, .project] }
}

/// The top of the to-dos page: List | Calendar, and in the calendar Day | Week | Month; in the list, its grouping when
/// `grouping` is given. Each device keeps its choice.
struct TodosModeBar<ListControls: View>: View {
    @Binding var view: TodosViewMode
    @Binding var mode: TodoCalendarMode
    var grouping: Binding<TodosGrouping>? = nil
    /// The groupings the menu offers (the Mac's list keeps status and directions).
    var groupings: [TodosGrouping] = [.status, .direction]
    /// The list view's own controls at the trailing end, where the calendar has its day / week / month (the phone's
    /// filter and sort, #3740).
    @ViewBuilder var listControls: ListControls
    @StoredValue("todos.calendar.showDone") private var showDone = true

    private var viewChoice: some View {
        SegmentedChoice(String(localized: "View"), [(value: TodosViewMode.list, label: String(localized: "List")), (value: .calendar, label: String(localized: "Calendar"))], selection: $view)
            .labelsHidden()
            .fixedSize()
            .accessibilityIdentifier("todos.view")
    }

    private var modeChoice: some View {
        SegmentedChoice(String(localized: "Calendar"), TodoCalendarMode.allCases.map { (value: $0, label: $0.label) }, selection: $mode)
            .labelsHidden()
            .fixedSize()
            .accessibilityIdentifier("todo.calendar.mode")
    }

    private var optionsMenu: some View {
        Menu {
            Toggle(String(localized: "Show Done"), isOn: $showDone)
        } label: {
            ThemedIcon(Theme.Icon.options)
        }
        .menuStyle(.button)
        .themedButton()
        .accessibilityLabel(Text("Calendar Options"))
        .accessibilityIdentifier("todo.calendar.options")
    }

    var body: some View {
        if view == .calendar {
            // At the interface's largest size the three controls do not fit one row: the day / week / month choice drops under.
            ViewThatFits(in: .horizontal) {
                bar
                VStack(alignment: .leading, spacing: Theme.Space.xs) {
                    HStack(spacing: Theme.Space.m) { viewChoice; Spacer(minLength: Theme.Space.s); optionsMenu }
                    modeChoice
                }
                .padding(.horizontal, Theme.Space.page)
                .padding(.vertical, Theme.Space.xs)
            }
        } else {
            bar
        }
    }

    private var bar: some View {
        HStack(spacing: Theme.Space.m) {
            viewChoice
            Spacer(minLength: Theme.Space.s)
            if view == .list, let grouping {
                // A grouping control, not a filter chip (docs/design/DESIGN_SYSTEM.md "List controls").
                Menu {
                    Picker("Group By", selection: grouping) {
                        ForEach(groupings, id: \.self) { g in Text(g.title).tag(g) }
                    }
                } label: {
                    // A button's text is never cut: when the word does not fit, the mark alone stands for it.
                    ViewThatFits(in: .horizontal) {
                        HStack(spacing: Theme.Space.xs) {
                            ThemedIcon(grouping.wrappedValue.symbol)
                            Text(grouping.wrappedValue.title).lineLimit(1)
                        }
                        ThemedIcon(grouping.wrappedValue.symbol)
                    }
                }
                .menuStyle(.button)
                .themedButton()
                .accessibilityLabel(Text("Group By"))
                .accessibilityValue(Text(grouping.wrappedValue.title))
                .accessibilityIdentifier("todos.grouping")
            }
            if view == .calendar {
                modeChoice
                optionsMenu
            } else {
                listControls
            }
        }
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.xs)
    }
}

extension TodosModeBar where ListControls == EmptyView {
    init(view: Binding<TodosViewMode>, mode: Binding<TodoCalendarMode>, grouping: Binding<TodosGrouping>? = nil) {
        self.init(view: view, mode: mode, grouping: grouping) { EmptyView() }
    }
}

extension TodoCalendarMode {
    var label: String {
        switch self {
        case .day: String(localized: "Day")
        case .week: String(localized: "Week")
        case .month: String(localized: "Month")
        }
    }
}

/// The to-dos as a calendar (#2029, docs/assistant/todos.md "Calendar"): a day, a week or a month of those with a due
/// date, drawn as the list draws them (state, overdue, done, priority, reminder). Tapping one opens it, tapping the
/// empty axis makes a new one due there; those without a due date are counted under it, a tap from the list. On iOS a
/// to-do is moved by dragging it to another time or day (#2045, `TodoCalendarDrag.swift`).
struct TodoCalendarView: View {
    @Environment(\.tabIsActive) private var inFront
    @Environment(\.locale) private var locale
    @Environment(\.displayScale) private var displayScale
    /// One device pixel: the calendar's edges.
    private var pixel: CGFloat { 1 / displayScale }
    var todos: [TodoItem]
    @Binding var mode: TodoCalendarMode
    /// The day shown, or one of the week or month shown (the page's "+" makes a to-do due on it).
    @Binding var anchor: Date
    /// Opens a to-do: the phone pushes its page, the Mac shows it beside the calendar.
    var open: (String) -> Void
    /// Back to the list ("N without a due date").
    var showList: () -> Void

    @StoredValue("todos.calendar.showDone") private var showDone = true
    @State private var adding: TodoCalendarSlot?
    /// A to-do whose due date is set from its "Change Due Date…" (#2045).
    @State private var rescheduling: TodoItem?
    /// A to-do is being dragged: a sideways move carries it, it doesn't turn the page.
    @State private var carrying = false
    #if DEBUG
    /// The `-openRescheduleTitled` sheet came up (then it isn't asked for again).
    @State private var debugRescheduleShown = false
    #endif

    private let calendar = TodoCalendar.userCalendar

    var body: some View {
        GeometryReader { geo in
            let wide = geo.size.width >= Theme.CalendarGrid.wide
            VStack(spacing: Theme.Space.flush) {
                header
                content(wide: wide)
                    .frame(maxHeight: .infinity)
                    .background(Theme.CalendarGrid.ground)
                    .overlay(alignment: .top) {
                        Rectangle().fill(Theme.CalendarGrid.line).frame(height: pixel)
                    }
                    #if os(iOS)
                    .simultaneousGesture(paging, including: carrying ? .subviews : .all)
                    #endif
                undatedBar
            }
        }
        .sheet(item: $adding) { slot in
            TodoQuickAddSheet(slot: slot)
        }
        .sheet(item: $rescheduling) { todo in
            TodoRescheduleSheet(todo: todo)
                #if DEBUG
                .onAppear {
                    debugRescheduleShown = true
                    CalendarDragTrace.shared.note("sheet shown") // i18n-ignore: DEBUG trace
                }
                .onDisappear { CalendarDragTrace.shared.note("sheet gone") } // i18n-ignore: DEBUG trace
                #endif
        }
        #if DEBUG
        // `-openRescheduleTitled <text>`: the first to-do whose title has it opens its "Change Due Date…" (the UI test
        // of the way without dragging, screenshots). A sheet asked for during the page's push or the switch to the
        // calendar can be dropped: it is asked for again until it is up. Each way out is in the drag trace.
        .task(id: todos.count) {
            guard let match = UserDefaults.standard.string(forKey: "openRescheduleTitled") else { return }
            let trace = CalendarDragTrace.shared
            guard !debugRescheduleShown else { return trace.note("sheet: shown before") } // i18n-ignore: DEBUG trace
            guard let todo = todos.first(where: { $0.title.contains(match) }) else {
                return trace.note("sheet: no to-do of \(todos.count)") // i18n-ignore: DEBUG trace
            }
            for attempt in 1...10 {
                try? await Task.sleep(for: .milliseconds(800))
                if Task.isCancelled { return trace.note("sheet: stopped at \(attempt)") } // i18n-ignore: DEBUG trace
                guard !debugRescheduleShown else { return }
                trace.note("sheet asked \(attempt)") // i18n-ignore: DEBUG trace
                rescheduling = nil
                try? await Task.sleep(for: .milliseconds(100))
                rescheduling = todo
            }
        }
        #endif
    }

    // MARK: What is shown

    private var entries: [CalendarEntry] {
        let lookup = TodosLogic.makeSessionLookup(AppModel.shared.allSessions(inFront: inFront))
        let source = TodoEntrySource(todos: todos, state: { TodosLogic.todoStatus($0, lookup: lookup) }, showDone: showDone)
        // The day view's strip shows the whole week's dots.
        let range = TodoCalendar.range(mode == .day ? .week : mode, anchor: anchor, calendar: calendar)
        return source.entries(in: range, calendar: calendar, now: .now)
    }

    @ViewBuilder private func content(wide: Bool) -> some View {
        let entries = entries
        switch mode {
        case .day:
            VStack(spacing: Theme.Space.flush) {
                TodoCalendarWeekStrip(anchor: anchor, entries: entries) { day in go(to: day) }
                    .padding(.top, Theme.Space.s)
                TodoCalendarTimeline(
                    days: [calendar.startOfDay(for: anchor)], entries: TodoCalendar.on(anchor, entries, calendar: calendar),
                    narrow: false, showsDayHeader: false, open: openEntry, pickDay: { go(to: $0) }, add: { adding = $0 },
                    moves: moves)
            }
            .id(TodoCalendar.range(.week, anchor: anchor, calendar: calendar).start)
        case .week:
            TodoCalendarTimeline(
                days: TodoCalendar.days(.week, anchor: anchor, calendar: calendar), entries: entries,
                narrow: !wide, showsDayHeader: true, open: openEntry, pickDay: { show(.day, $0) }, add: { adding = $0 },
                moves: moves)
                .padding(.top, Theme.Space.s)
                .id(TodoCalendar.range(.week, anchor: anchor, calendar: calendar).start)
        case .month:
            TodoCalendarMonth(anchor: anchor, entries: entries, wide: wide, open: openEntry, pickDay: { show(.day, $0) },
                              moves: moves)
                .padding(.top, Theme.Space.s)
                .id(TodoCalendar.range(.month, anchor: anchor, calendar: calendar).start)
        }
    }

    private func openEntry(_ entry: CalendarEntry) {
        switch entry.source {
        case .todo(let id): open(id)
        }
    }

    // MARK: Moving one (#2045)

    /// What the grids do with a to-do moved on them: iOS and iPadOS only (the Mac app is frozen).
    private var moves: CalendarMoves? {
        #if os(iOS)
        return CalendarMoves(
            drop: { entry, drop in
                guard case .todo(let id) = entry.source, TodoCalendar.moves(entry, to: drop, calendar: calendar) else { return }
                TodoCalendarMove.save(id, to: TodoCalendar.due(drop, calendar: calendar))
            },
            reschedule: { entry in
                guard case .todo(let id) = entry.source else { return }
                rescheduling = TodoStore.shared.todo(id: id)
            },
            carrying: { carrying = $0 })
        #else
        return nil
        #endif
    }

    // MARK: Moving around

    private var header: some View {
        HStack(spacing: Theme.Space.s) {
            let title = TodoCalendar.title(mode, anchor: anchor, calendar: calendar, locale: locale)
            // a phone's week ("Nov 8 – 14" with the buttons beside it) steps down a size rather than cut its dates
            ViewThatFits(in: .horizontal) {
                Text(title).font(Theme.Font.title)
                Text(title).font(Theme.Font.heading).minimumScaleFactor(0.6)
            }
            .lineLimit(1)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(title)
            .accessibilityAddTraits(.isHeader)
            .accessibilityIdentifier("todo.calendar.title")
            Spacer(minLength: Theme.Space.s)
            Button { go(to: .now) } label: {
                Text("Today").lineLimit(1)
            }
            .themedButton()
            .accessibilityIdentifier("todo.calendar.today")
            Button { step(-1) } label: { ThemedIcon(Theme.Icon.back) }
                .themedButton()
                .accessibilityLabel(Text("Previous"))
                .accessibilityIdentifier("todo.calendar.previous")
            Button { step(1) } label: { ThemedIcon(Theme.Icon.forward) }
                .themedButton()
                .accessibilityLabel(Text("Next"))
                .accessibilityIdentifier("todo.calendar.next")
        }
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.s)
    }

    private func step(_ n: Int) {
        go(to: TodoCalendar.step(anchor, mode: mode, by: n, calendar: calendar))
    }

    private func go(to day: Date) {
        withAnimation(Theme.Motion.layout) { anchor = day }
    }

    private func show(_ mode: TodoCalendarMode, _ day: Date) {
        withAnimation(Theme.Motion.layout) {
            anchor = day
            self.mode = mode
        }
    }

    #if os(iOS)
    /// A sideways swipe turns the page; an up-and-down one is the axis scrolling.
    private var paging: some Gesture {
        DragGesture(minimumDistance: Theme.Space.xl)
            .onEnded { drag in
                let dx = drag.translation.width
                guard abs(dx) > Theme.Size.tapTarget, abs(dx) > abs(drag.translation.height) * 2 else { return }
                step(dx < 0 ? 1 : -1)
            }
    }
    #endif

    // MARK: Without a due date

    @ViewBuilder private var undatedBar: some View {
        let count = TodoCalendar.undatedCount(todos, calendar: calendar)
        if count > 0 {
            Button(action: showList) {
                HStack(spacing: Theme.Space.sm) {
                    ThemedIcon(Theme.Icon.listView)
                        .foregroundStyle(.secondary)
                    Text("\(count) without a due date")
                        .foregroundStyle(.secondary)
                    Text("See List")
                        .font(Theme.Font.captionStrong)
                        .foregroundStyle(Theme.accent)
                }
                .font(Theme.Font.caption)
                .lineLimit(1)
                .padding(.horizontal, Theme.Space.page)
                .padding(.vertical, Theme.Space.s)
                .frame(maxWidth: .infinity)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .background(Theme.CalendarGrid.ground)
            .overlay(alignment: .top) {
                Rectangle().fill(Theme.CalendarGrid.line).frame(height: pixel)
            }
            .accessibilityIdentifier("todo.calendar.undated")
            .debugDragTrace()
        }
    }
}

/// A new to-do made on the calendar: due where it was tapped (a moment, or the day), a title to type; the rest is set
/// on its page.
struct TodoQuickAddSheet: View {
    @Environment(\.dismiss) private var dismiss
    @State private var store = TodoStore.shared
    var slot: TodoCalendarSlot

    @State private var title = ""
    @State private var allDay: Bool
    @State private var due: Date
    @State private var saving = false
    @FocusState private var typing: Bool

    init(slot: TodoCalendarSlot) {
        self.slot = slot
        _allDay = State(initialValue: slot.time == nil)
        _due = State(initialValue: slot.time ?? Calendar.current.date(bySettingHour: TodoCalendar.morning + 1, minute: 0, second: 0, of: slot.day) ?? slot.day)
    }

    private var trimmed: String { title.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextField(String(localized: "Title"), text: $title)
                        .lineLimit(1)
                        .focused($typing)
                        .onSubmit(add)
                        .accessibilityIdentifier("todo.calendar.newTitle")
                }
                Section {
                    FormDatePicker("Due", selection: $due, displayedComponents: allDay ? [.date] : [.date, .hourAndMinute],
                                   identifier: "todo.calendar.newDue")
                    Toggle(String(localized: "All Day"), isOn: $allDay)
                        .accessibilityIdentifier("todo.calendar.newAllDay")
                } footer: {
                    Text("Set a reminder, the priority and notes on the to-do's page.")
                }
            }
            .themedPage()
            .navigationTitle("New To-Do")
            #if os(iOS)
            .navigationBarTitleDisplayMode(.inline)
            #endif
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                        .lineLimit(1)
                }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Add [Beautiful U I Bridge 7a8a11]", action: add)
                        .lineLimit(1)
                        .disabled(trimmed.isEmpty || saving)
                        .accessibilityIdentifier("todo.calendar.newAdd")
                }
            }
            #if os(macOS)
            .macSheet()
            #endif
        }
        .formSheetDetents([.medium])
        .onAppear { typing = true }
    }

    private func add() {
        guard !trimmed.isEmpty, !saving else { return }
        saving = true
        let when: APIClient.TodoPlanChange.Due = allDay ? .day(TodoPlanText.day(due)) : .at(due)
        // The sheet closes only once the to-do is saved: a failed save keeps what was typed.
        Task {
            do {
                _ = try await store.create(title: trimmed, due: when)
                dismiss()
            } catch {
                saving = false
                AppModel.shared.show(error)
            }
        }
    }
}
