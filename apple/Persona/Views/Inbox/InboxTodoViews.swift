import SwiftUI

// Add to To-Dos from the Inbox (#6480; docs/assistant/inbox.md): the due time asked once for the rows — 1 hour later, today
// all day, a picked date and time, no due date — then one to-do per row. Merge and Add to To-Dos are two separate actions.

/// Rows waiting for their due time in `InboxDueSheet`; `done` runs once they were added (selecting ends).
struct InboxTodoRequest: Identifiable {
    let id = UUID()
    var items: [InboxItem]
    var done: @MainActor () -> Void = {}
}

/// The due choices as menu buttons: the three quick ones add at once, "Pick Date & Time…" opens the sheet.
struct InboxDueButtons: View {
    var add: (InboxDue) -> Void
    var pick: () -> Void

    var body: some View {
        Button { add(.hour) } label: { Label("1 Hour Later", systemImage: Theme.Icon.recent) }
        Button { add(.today) } label: { Label("Today, All Day", systemImage: Theme.Icon.dueDate) }
        Button(action: pick) { Label("Pick Date & Time…", systemImage: Theme.Icon.dueDate) }
        Button { add(.none) } label: { Label("No Due Date", systemImage: Theme.Icon.unchecked) }
    }
}

/// A date and time to pick, then Add.
struct InboxDueSheet: View {
    var request: InboxTodoRequest
    @Environment(\.dismiss) private var dismiss
    @State private var at = InboxDueSheet.start(.now)

    /// Where the picker starts: the next quarter hour.
    nonisolated static func start(_ now: Date) -> Date {
        Date(timeIntervalSinceReferenceDate: (now.timeIntervalSinceReferenceDate / 900).rounded(.up) * 900)
    }

    var body: some View {
        NavigationStack {
            Form {
                DatePicker("Due", selection: $at, displayedComponents: [.date, .hourAndMinute])
                    .datePickerStyle(.graphical)
                    .accessibilityIdentifier("inbox.todo.due")
            }
            .navigationTitle(request.items.count == 1 ? String(localized: "Add to To-Dos") : String(localized: "Add \(request.items.count) Rows to To-Dos"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("Cancel") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("Add") {
                        InboxStore.shared.addTodos(request.items, due: .at(at), done: request.done)
                        dismiss()
                    }
                    .accessibilityIdentifier("inbox.todo.add")
                }
            }
        }
    }
}

/// A to-do suggestion's card body (#6480): the title, the due day and time as fields; one tap on Add to To-Dos takes them as
/// they stand. All Day is a switch: on, only the day; off, a time too (9:00 to start with).
struct InboxTodoFields: View {
    @Binding var title: String
    @Binding var day: Date?
    @Binding var allDay: Bool
    @Binding var time: Date

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            TextField("To-Do Title", text: $title, axis: .vertical)
                .font(Theme.Font.secondary)
                .accessibilityIdentifier("inbox.suggestion.todoTitle")
            Toggle("Due Date", isOn: Binding(get: { day != nil }, set: { day = $0 ? (day ?? .now) : nil }))
                .font(Theme.Font.secondary)
            if let d = day {
                DatePicker("Due Day", selection: Binding(get: { d }, set: { day = $0 }), displayedComponents: .date)
                    .font(Theme.Font.secondary)
                Toggle("All Day", isOn: $allDay)
                    .font(Theme.Font.secondary)
                    .accessibilityIdentifier("inbox.suggestion.allDay")
                if !allDay {
                    DatePicker("Time", selection: $time, displayedComponents: .hourAndMinute)
                        .font(Theme.Font.secondary)
                }
            }
        }
    }
}
