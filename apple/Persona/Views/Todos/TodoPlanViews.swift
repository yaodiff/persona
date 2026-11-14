import SwiftUI

// A to-do's due date, reminder and priority (#1683, docs/assistant/todos.md "Due date, reminder, priority"): a line on
// the list's rows and a section on the detail page, where each change is saved as it is made.

/// How a to-do's due date reads: "Thu, Nov 12, 6:00 PM", or the day alone; the year only when it is not this one.
nonisolated enum TodoPlanText {
    static func due(_ todo: TodoItem, now: Date = .now, calendar: Calendar = .current) -> String? {
        if let at = todo.dueAt { return format(at, time: true, now: now, calendar: calendar) }
        if let day = TodosLogic.dueDay(todo, calendar: calendar) { return format(day, time: false, now: now, calendar: calendar) }
        return nil
    }

    static func moment(_ date: Date, now: Date = .now, calendar: Calendar = .current) -> String {
        format(date, time: true, now: now, calendar: calendar)
    }

    private static func format(_ date: Date, time: Bool, now: Date, calendar: Calendar) -> String {
        var style = Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).weekday(.abbreviated).month(.abbreviated).day()
        if calendar.component(.year, from: date) != calendar.component(.year, from: now) { style = style.year() }
        if time { style = style.hour().minute() }
        return date.formatted(style)
    }

    /// The day a `due_date` names, written as the hub reads it ("yyyy-MM-dd").
    static func day(_ date: Date, calendar: Calendar = .current) -> String {
        TodosLogic.dayKey(date, calendar: calendar)
    }
}

/// The due date, a pending reminder and high priority under a to-do's title in the list; nothing when it has none of them.
struct TodoPlanLine: View {
    var todo: TodoItem

    var body: some View {
        let due = TodoPlanText.due(todo)
        let remind = todo.remindAt != nil && todo.remindedAt == nil && !todo.isDone
        let high = todo.priority == "high" && !todo.isDone
        if due != nil || remind || high {
            HStack(spacing: Theme.Space.sm) {
                if high {
                    ThemedIcon(Theme.Icon.highPriority)
                        .foregroundStyle(Theme.caution)
                        .accessibilityLabel(Text("High priority"))
                }
                if let due {
                    let overdue = TodosLogic.isOverdue(todo)
                    HStack(alignment: .firstTextBaseline, spacing: Theme.Space.xs) {
                        ThemedIcon(Theme.Icon.dueDate)
                        // The date and time may break onto a second line at a large text size rather than be cut.
                        Text(overdue ? "Was due \(due)" : "Due \(due)")
                            .lineLimit(2)
                    }
                    .foregroundStyle(overdue ? Theme.danger : Color.secondary)
                }
                if remind, let at = todo.remindAt {
                    ThemedIcon(Theme.Icon.reminder)
                        .foregroundStyle(.secondary)
                        .accessibilityLabel(Text("Reminder at \(TodoPlanText.moment(at))"))
                }
            }
            .font(Theme.Font.caption)
            .accessibilityElement(children: .combine)
        }
    }
}

/// What the detail page's plan section edits, as the controls hold it (a date stays in its picker while it is off).
nonisolated struct TodoPlanDraft: Equatable {
    var hasDue = false
    var allDay = true
    var due = Date()
    var hasRemind = false
    var remind = Date()
    var priority = ""

    init(_ todo: TodoItem, now: Date = .now, calendar: Calendar = .current) {
        let tomorrow = calendar.date(byAdding: .day, value: 1, to: calendar.startOfDay(for: now)) ?? now
        let evening = calendar.date(bySettingHour: 18, minute: 0, second: 0, of: tomorrow) ?? tomorrow
        let nextHour = calendar.nextDate(after: now, matching: DateComponents(minute: 0), matchingPolicy: .nextTime) ?? now
        if let at = todo.dueAt {
            hasDue = true
            allDay = false
            due = at
        } else if let day = TodosLogic.dueDay(todo, calendar: calendar) {
            hasDue = true
            due = calendar.date(bySettingHour: 18, minute: 0, second: 0, of: day) ?? day
        } else {
            due = evening
        }
        hasRemind = todo.remindAt != nil
        remind = todo.remindAt ?? nextHour
        priority = todo.priority ?? ""
    }

    /// The PATCH that makes `todo` what this draft says.
    func change(calendar: Calendar = .current) -> APIClient.TodoPlanChange {
        let d: APIClient.TodoPlanChange.Due = !hasDue ? .none : allDay ? .day(TodoPlanText.day(due, calendar: calendar)) : .at(due)
        return APIClient.TodoPlanChange(due: d, remindAt: .some(hasRemind ? remind : nil), priority: priority)
    }

    /// Whether two drafts say the same: the dates of a switch that is off do not count.
    func means(_ other: TodoPlanDraft) -> Bool {
        change() == other.change()
    }
}

/// The detail page's due date, reminder, priority and project. A change is saved shortly after it is made (a date picker
/// sends every step); the controls follow the to-do again once nothing is being saved.
struct TodoPlanSection: View {
    var todo: TodoItem
    @State private var store = TodoStore.shared
    @State private var draft: TodoPlanDraft
    @State private var saving: Task<Void, Never>?
    @State private var projects = TodoProjects.shared
    /// The project picked, its slug; "" none. Saved on its own: a project gone from the knowledge base since must not
    /// make a date change fail.
    @State private var project: String
    @State private var savingProject: Task<Void, Never>?

    init(todo: TodoItem) {
        self.todo = todo
        _draft = State(initialValue: TodoPlanDraft(todo))
        _project = State(initialValue: todo.project ?? "")
    }

    var body: some View {
        Section {
            Toggle(isOn: $draft.hasDue) {
                Label { Text("Due Date") } icon: { ThemedIcon(Theme.Icon.dueDate) }
            }
            .accessibilityIdentifier("todo.plan.due")
            if draft.hasDue {
                Toggle("All Day", isOn: $draft.allDay)
                    .accessibilityIdentifier("todo.plan.allDay")
                FormDatePicker("Due", selection: $draft.due, displayedComponents: draft.allDay ? [.date] : [.date, .hourAndMinute],
                               identifier: "todo.plan.dueDate")
            }
            Toggle(isOn: $draft.hasRemind) {
                Label { Text("Remind Me") } icon: { ThemedIcon(Theme.Icon.reminder) }
            }
            .accessibilityIdentifier("todo.plan.remind")
            if draft.hasRemind {
                FormDatePicker("Remind At", selection: $draft.remind, in: min(draft.remind, .now)..., displayedComponents: [.date, .hourAndMinute],
                               identifier: "todo.plan.remindAt")
                if let sent = todo.remindedAt, todo.remindAt == draft.remind {
                    Text("Reminded \(TodoPlanText.moment(sent))")
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Picker(selection: $draft.priority) {
                Text("High").tag("high")
                Text("Normal").tag("")
                Text("Low").tag("low")
            } label: {
                Label { Text("Priority") } icon: { ThemedIcon(Theme.Icon.highPriority) }
            }
            .accessibilityIdentifier("todo.plan.priority")
            Picker(selection: $project) {
                Text("None [Todo Project]").tag("")
                ForEach(TodoProjectLogic.choices(projects.list, current: todo.project)) { p in
                    Text(p.name).tag(p.slug)
                }
            } label: {
                Label { Text("Project") } icon: { ThemedIcon(Theme.Icon.space) }
            }
            .accessibilityIdentifier("todo.plan.project")
        }
        .onChange(of: draft) { _, new in
            guard !new.means(TodoPlanDraft(todo)) else { return }
            save(new)
        }
        .onChange(of: project) { _, new in
            guard new != (todo.project ?? "") else { return }
            saveProject(new)
        }
        .onChange(of: todo) { _, now in
            if saving == nil { draft = TodoPlanDraft(now) }
            if savingProject == nil { project = now.project ?? "" }
        }
        .task { await projects.load(AppModel.shared.api?.currentAssistantSettings()) }
    }

    /// The hub refuses a project it does not have (deleted, or not a project any more): the picker goes back.
    private func saveProject(_ slug: String) {
        savingProject?.cancel()
        let id = todo.id
        savingProject = Task {
            do {
                try await store.updatePlan(id: id, APIClient.TodoPlanChange(project: slug))
                Haptics.selection()
            } catch {
                guard !Task.isCancelled else { return } // a newer pick is being saved
                project = todo.project ?? ""
                AppModel.shared.show(error)
            }
            if !Task.isCancelled { savingProject = nil }
        }
    }

    private func save(_ new: TodoPlanDraft) {
        saving?.cancel()
        let id = todo.id
        saving = Task {
            try? await Task.sleep(for: .milliseconds(400))
            guard !Task.isCancelled else { return }
            do {
                try await store.updatePlan(id: id, new.change())
            } catch {
                AppModel.shared.show(error)
            }
            saving = nil
        }
    }
}
