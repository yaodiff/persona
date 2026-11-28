import PersonaWire
import SwiftUI

// Waiting reason UI for parked and waiting to-dos (#16168).
// Renders the server-emitted strongly-typed `TodoWait` directly with zero client-side derivation.

/// Compact waiting reason shown under a to-do's status line in `TodosView` (`TodoRow`).
struct TodoWaitLine: View {
    var wait: TodoWait
    var now: Date = .now

    var body: some View {
        let summary = TodoWaitText.rowSummary(wait, now: now)
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.xs) {
            ThemedIcon(Theme.Icon.time)
                .font(Theme.Font.labelStrong)
                .foregroundStyle(summary.isDue ? Theme.cautionText : .secondary)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                Text(summary.lead)
                    .font(Theme.Font.caption)
                    .foregroundStyle(.primary)
                    .lineLimit(2)
                    .fixedSize(horizontal: false, vertical: true)
                if let timing = summary.timing {
                    Text(timing)
                        .font(Theme.Font.caption)
                        .foregroundStyle(summary.isDue ? Theme.cautionText : .secondary)
                        .lineLimit(2)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("todo.wait.line")
    }
}

/// Full waiting details section in `TodoDetailView`: who/what it waits on, reason, wake trigger/time, deadline, and source link.
struct TodoWaitSection: View {
    var wait: TodoWait
    var now: Date = .now
    var openDetail: ((TodoDetailRoute) -> Void)? = nil
    var openLink: (URL) -> Void

    var body: some View {
        let detail = TodoWaitText.detail(wait, now: now)
        Section {
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                ViewThatFits(in: .horizontal) {
                    HStack(spacing: Theme.Space.s) {
                        headerTitle
                        Spacer(minLength: Theme.Space.s)
                        kindChip(detail)
                    }
                    VStack(alignment: .leading, spacing: Theme.Space.xs) {
                        headerTitle
                        kindChip(detail)
                    }
                }

                if let step = detail.step {
                    Text(String(localized: "From step: \(step) [Todo Wait]"))
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }

                if let waitingOn = detail.waitingOn {
                    if let handoffID = detail.handoffTodoID {
                        factRow(label: String(localized: "Waiting on [Todo Wait]")) {
                            handoffLink(title: waitingOn, todoID: handoffID)
                        }
                    } else {
                        factRow(label: String(localized: "Waiting on [Todo Wait]"), value: waitingOn)
                    }
                }

                if let reason = detail.reason {
                    factRow(label: String(localized: "Details [Todo Wait]"), value: reason)
                }

                if let blockers = detail.blockersText {
                    factRow(label: String(localized: "Blocked by [Todo Wait]"), value: blockers)
                }

                if let wakeCondition = detail.wakeCondition {
                    factRow(label: String(localized: "Wakes when [Todo Wait]"), value: wakeCondition)
                }

                if let wakeAtText = detail.wakeAtText {
                    factRow(label: String(localized: "Scheduled wake [Todo Wait]"), value: wakeAtText)
                }

                if let untilText = detail.untilText {
                    factRow(label: detail.untilIsDue ? String(localized: "Check-in due [Todo Wait]") : String(localized: "Until [Todo Wait]"),
                            value: untilText, tone: detail.untilIsDue ? Theme.cautionText : .primary)
                }

                if let contact = detail.contact {
                    contactBlock(contact)
                }

                if let sourceURL = detail.sourceURL {
                    Button { openLink(sourceURL) } label: {
                        Label(detail.isPR ? String(localized: "View PR [Todo Wait]") : String(localized: "Open Source [Todo Wait]"),
                              systemImage: detail.isPR ? Theme.Icon.pullRequest : Theme.Icon.link)
                            .font(Theme.Font.captionStrong)
                            .lineLimit(1)
                    }
                    .themedButton()
                    .accessibilityIdentifier("todo.wait.source")
                }

                if let sinceText = detail.sinceText {
                    Text(String(localized: "Waiting since \(sinceText) [Todo Wait]"))
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.vertical, Theme.Space.xs)
            .accessibilityIdentifier("todo.wait.section")
        }
    }

    private var headerTitle: some View {
        HStack(spacing: Theme.Space.xs) {
            ThemedIcon(Theme.Icon.time)
                .font(Theme.Font.captionStrong)
                .foregroundStyle(.secondary)
            Text(String(localized: "Waiting [Todo Wait]"))
                .font(Theme.Font.captionStrong)
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
    }

    private func kindChip(_ detail: TodoWaitText.DetailModel) -> some View {
        Chip(text: detail.kindTitle, look: detail.untilIsDue ? .review(.caution) : .neutral, compact: true)
    }

    private func factRow(label: String, value: String, tone: Color = .primary) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(label)
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
            Text(value)
                .font(Theme.Font.secondary)
                .foregroundStyle(tone)
                .fixedSize(horizontal: false, vertical: true)
                .textSelection(.enabled)
        }
        .accessibilityElement(children: .combine)
    }

    private func factRow<Content: View>(label: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(label)
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
            content()
        }
    }

    @ViewBuilder private func handoffLink(title: String, todoID: String) -> some View {
        let route = TodoDetailRoute(id: todoID)
        if let openDetail {
            Button { openDetail(route) } label: {
                Text(title)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(Theme.accent)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("todo.wait.handoff.\(todoID)")
        } else {
            NavigationLink(value: route) {
                Text(title)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(Theme.accent)
                    .fixedSize(horizontal: false, vertical: true)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("todo.wait.handoff.\(todoID)")
        }
    }

    private func contactBlock(_ contact: TodoWaitText.DetailModel.Contact) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            let meta = [contact.sender, contact.sentAtText].compactMap { $0 }.joined(separator: TodoWaitText.dotSeparator)
            Text(meta.isEmpty
                 ? String(localized: "Sent via \(contact.channel) [Todo Wait]")
                 : String(localized: "Sent via \(contact.channel) · \(meta) [Todo Wait]"))
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
                .fixedSize(horizontal: false, vertical: true)
            if let request = contact.request {
                Text(request)
                    .font(Theme.Font.caption)
                    .foregroundStyle(.primary)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
            }
            if let url = contact.url {
                Button { openLink(url) } label: {
                    Label(String(localized: "Open Message [Todo Wait]"), systemImage: Theme.Icon.link)
                        .font(Theme.Font.captionStrong)
                        .lineLimit(1)
                }
                .themedButton()
                .accessibilityIdentifier("todo.wait.contact")
            }
        }
        .padding(Theme.Space.s)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(Theme.Fill.well, in: .rect(cornerRadius: Theme.Radius.control, style: .continuous))
    }
}
