import SwiftUI

/// One running terminal Agent in the native console (#17129): herdr tab name, Agent session name, then state or the
/// latest words. A terminal source chip is redundant in this list, so it is deliberately not drawn.
struct AgentConsoleSessionRow: View {
    let session: Session
    var time: AgentTime?

    var body: some View {
        HStack(alignment: .top, spacing: Theme.Space.m) {
            AgentMark(session)
            words
            Spacer(minLength: 0)
            AgentTimeText(time: time, fallback: session.updatedAt, font: Theme.Font.label)
        }
        .padding(.vertical, Theme.Space.s)
        .contentShape(.rect)
        .accessibilityElement(children: .combine)
    }

    private var words: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            tabName
            sessionName
            stateLine
        }
    }

    @ViewBuilder private var tabName: some View {
        if let tab = session.herdrTabName {
            Text(tab)
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
                .lineLimit(2)
                .fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("agent-console-tab-name")
        }
    }

    private var sessionName: some View {
        Text(session.displayTitle)
            .font(session.state == .unread ? Theme.Font.bodyHeavy : Theme.Font.bodyStrong)
            .lineLimit(2)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityIdentifier("agent-console-session-name")
    }

    private var stateLine: some View {
        HStack(spacing: Theme.Space.xs) {
            StatusDot(state: session.state, size: Theme.Size.dot, waiting: session.waitsInBackground,
                      justFinished: session.showsJustFinished(muted: false))
            Text(session.stateLabel(muted: false))
                .font(Theme.Font.caption)
                .foregroundStyle(session.stateTone(muted: false).text)
            preview
        }
    }

    @ViewBuilder private var preview: some View {
        if let preview = session.listPreview(muted: false), !preview.isEmpty {
            Text(preview)
                .font(Theme.Font.caption)
                .foregroundStyle(.secondary)
                .lineLimit(1)
        }
    }
}
