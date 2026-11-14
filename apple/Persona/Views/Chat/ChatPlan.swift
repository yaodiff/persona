import SwiftUI
import PersonaWire

/// The agent's plan (TodoWrite) as the chat shows it (#5602, the web's `PlanCard` / `PlanPanel` / `PlanPill`, after
/// Claude Desktop): its line in the work says which step of how many it is on and that step, over a thin bar of how far
/// along it is; it opens the whole list in a sheet that follows the chat's newest plan, and while that plan's line is
/// out of view a small pill above the composer says the same.
nonisolated struct ChatPlan: Equatable, Sendable {
    struct Todo: Equatable, Sendable {
        var content: String
        /// completed, in_progress or pending.
        var status: String
        var activeForm: String?
    }

    /// The TodoWrite call's id (its line's step id).
    var callID: String
    var todos: [Todo]

    var done: Int { todos.filter { $0.status == "completed" }.count }
    var allDone: Bool { !todos.isEmpty && done == todos.count }
    /// The step it is on: the first in progress, else the first still to do (`PlanProgress`).
    var current: Todo? { todos.first { $0.status == "in_progress" } ?? todos.first { $0.status == "pending" } }

    static func todos(_ json: [JSONValue]) -> [Todo] {
        json.compactMap { t in
            let form = t["activeForm"]?.string
            guard let content = t["content"]?.string.flatMap({ $0.isEmpty ? nil : $0 }) ?? form, !content.isEmpty else { return nil }
            let s = t["status"]?.string
            return Todo(content: content, status: s == "completed" || s == "in_progress" ? s! : "pending", activeForm: form)
        }
    }

    /// The chat's newest plan: its latest TodoWrite of two or more items that did not fail, also when the hub folded it
    /// away with the work around it (the fold keeps the list, `Fold.tasks`; the web's `latestPlan`).
    static func latest(_ items: [Item]) -> ChatPlan? {
        var failed: Set<String> = []
        for item in items.reversed() {
            if let snap = item.fold?.tasks?.last(where: { $0.replace == "todo" && $0.entries.count >= 2 }) {
                let todos = snap.entries.compactMap { e -> Todo? in
                    guard let s = e.subject, !s.isEmpty, e.status != "deleted" else { return nil }
                    return Todo(content: s, status: e.status == "completed" || e.status == "in_progress" ? e.status! : "pending",
                                activeForm: e.description)
                }
                if todos.count >= 2 { return ChatPlan(callID: snap.sourceItemID, todos: todos) }
            }
            if item.kind == .toolResult, item.isError == true, let id = item.toolUseID { failed.insert(id) }
            guard item.kind == .toolUse, ToolPresentation.baseName(item.toolName ?? "") == "TodoWrite",
                  !(item.toolUseID.map(failed.contains) ?? false) else { continue }
            let todos = todos(item.toolInput?["todos"]?.array ?? [])
            if todos.count >= 2 { return ChatPlan(callID: item.id, todos: todos) }
        }
        return nil
    }
}

extension EnvironmentValues {
    /// Opens the chat's plan sheet (`PlanSheet`); nil where there is none (a plan's line then lists it in place).
    @Entry var openPlan: (() -> Void)? = nil
}

/// How far along a plan is, as a thin bar under its line.
struct PlanBar: View {
    var done: Int
    var total: Int

    var body: some View {
        GeometryReader { g in
            ZStack(alignment: .leading) {
                Capsule().fill(Theme.Fill.well)
                Capsule().fill(Theme.positive)
                    .frame(width: total > 0 ? g.size.width * CGFloat(done) / CGFloat(total) : 0)
            }
        }
        .frame(height: Theme.Size.rule)
        .animation(Theme.Motion.state, value: done)
        .accessibilityHidden(true)
    }
}

/// The chat's newest plan, whole: each step with its state, following the plan as the agent updates it.
struct PlanSheet: View {
    @Environment(\.dismiss) private var dismiss
    var plan: ChatPlan?

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: Theme.Space.m) {
                    if let plan {
                        Text(PlanProgress.summary(plan)).font(Theme.Font.secondary).foregroundStyle(.secondary)
                        PlanBar(done: plan.done, total: plan.todos.count)
                        TodoList(todos: plan.todos.map { t in
                            .object(["content": .string(t.content), "status": .string(t.status),
                                     "activeForm": t.activeForm.map(JSONValue.string) ?? .null])
                        })
                    } else {
                        Text("No plan yet [Plan Sheet]").font(Theme.Font.secondary).foregroundStyle(.secondary)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .padding(Theme.Space.l)
            }
            .background(Theme.pageBackground)
            .navigationTitle(String(localized: "Plan [Plan Sheet]"))
            .navigationBarTitleDisplayMode(.inline)
            .toolbar { ToolbarItem(placement: .confirmationAction) { Button("Done [Extension System Presentation c0b3fb]") { dismiss() } } }
        }
        .presentationDetents([.medium, .large])
        .presentationDragIndicator(.visible)
        .accessibilityIdentifier("chat.plan-sheet")
    }
}

/// Above the composer while the newest plan's line is out of view: a ring of how far along it is (a check once every
/// step is done) and which step of how many. Tap: the plan sheet.
struct PlanPill: View {
    var plan: ChatPlan
    var open: () -> Void

    var body: some View {
        Button {
            Haptics.tap()
            open()
        } label: {
            HStack(spacing: Theme.Space.s) {
                if plan.allDone {
                    ThemedIcon(Theme.Icon.done).font(Theme.Font.labelStrong).foregroundStyle(Theme.positive)
                } else {
                    ZStack {
                        Circle().stroke(Theme.Fill.well, lineWidth: Theme.Size.rule)
                        Circle().trim(from: 0, to: CGFloat(plan.done) / CGFloat(max(plan.todos.count, 1)))
                            .stroke(Theme.positive, style: StrokeStyle(lineWidth: Theme.Size.rule, lineCap: .round))
                            .rotationEffect(.degrees(-90))
                    }
                    .scaledFrame(width: Theme.Size.dot * 2, height: Theme.Size.dot * 2)
                }
                Text(PlanProgress.text(plan)).font(Theme.Font.labelStrong.monospacedDigit()).lineLimit(1).fixedSize()
            }
            .padding(.horizontal, Theme.Space.m)
            .padding(.vertical, Theme.Space.sm)
            .contentShape(.capsule)
        }
        .buttonStyle(.plain)
        .surface(interactive: true, in: .capsule)
        .accessibilityIdentifier("chat.plan-pill")
        .accessibilityHint(String(localized: "Shows the plan [Plan Sheet]"))
    }
}
