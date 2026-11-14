import SwiftUI

/// What a screen card shows of a command's permission prompt read off the terminal, in plain words (#5266), under its
/// heading ("Claude Wants to Run a Command") and over its answers: the agent's own words for it, the command in code type
/// (a long one folded, Show All N Lines opening it whole: `CardCode`, #6605), the warning in plain words with Claude
/// Code's own under Show Original, and the countdown running. The default style's `ScreenForm` and Beautiful UI's
/// `BUIScreenForm` (`compact`) both lay it out.
struct ScreenCommandAskBody: View {
    var ask: CommandAsk
    /// The agent asking ("Claude"): the whole command's sheet is titled as the card's heading is.
    var agent = "Claude"
    /// Beautiful UI's card, whose choice rows and Send under this take more room than the default style's two buttons:
    /// the agent's words in the secondary type close over the command (as its permission card shows a reason), the
    /// command folded to two rows, the countdown beside the warning where the row has room. So Send stays in the
    /// half-page panel's view.
    var compact = false
    @State private var showsOriginal = false

    var body: some View {
        if compact {
            VStack(alignment: .leading, spacing: Theme.Space.m) {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    if let description = ask.description {
                        Text(description)
                            .font(Theme.Font.secondary).lineHeight(.secondary)
                            .foregroundStyle(.secondary)
                            .fixedSize(horizontal: false, vertical: true)
                    }
                    command
                }
                if ask.riskLine != nil || ask.deadlineAt != nil {
                    VStack(alignment: .leading, spacing: Theme.Space.s) { compactNotes }
                }
            }
        } else {
            // One part after another, no stack of its own: in the card's form (`CardBodyStack`) the command is the part
            // that gives up lines when the card is short, and Deny / Allow stay in view under them all.
            if let description = ask.description {
                Text(description)
                    .font(Theme.Font.body).lineHeight(.body)
                    .fixedSize(horizontal: false, vertical: true)
            }
            command
            // Close together.
            if ask.riskLine != nil || ask.deadlineAt != nil {
                VStack(alignment: .leading, spacing: Theme.Space.s) {
                    if let risk = ask.riskLine { warning(risk) }
                    if ask.deadlineAt != nil { ScreenCountdown(ask: ask) }
                }
            }
        }
    }

    /// The command as a permission card shows one (`CardCode`): code type in its box, folded when long.
    private var command: some View {
        CardCode(text: ask.command, title: CommandAsk.title(agent: agent),
                 lines: compact ? CommandAsk.compactFoldedLines : CommandAsk.foldedLines, id: "screen-command")
    }

    /// The warning in plain words, Show Original at the end of its row (under it where the row has no room); Claude
    /// Code's own warning and countdown sentence under that.
    private func warning(_ risk: String) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            let line = warningLine(risk)
            if let original = ask.original {
                ViewThatFits(in: .horizontal) {
                    HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                        line
                        Spacer(minLength: Theme.Space.s)
                        originalButton
                    }
                    VStack(alignment: .leading, spacing: Theme.Space.xs) {
                        line
                        originalButton
                    }
                }
                if showsOriginal {
                    Text(original)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                        .transition(.opacity)
                }
            } else {
                line
            }
        }
    }

    private func warningLine(_ risk: String) -> some View {
        HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
            ThemedIcon(Theme.Icon.errorFilled).font(Theme.Font.captionMedium)
            Text(risk).font(Theme.Font.captionMedium).fixedSize(horizontal: false, vertical: true)
        }
        .foregroundStyle(Theme.cautionText)
        .accessibilityElement(children: .combine)
    }

    /// Beautiful UI's warning and countdown: the countdown beside the warning, Show Original under them; where the row
    /// has no room for both, the countdown under the warning with Show Original beside it (or under it). Claude Code's
    /// own words under all that.
    @ViewBuilder private var compactNotes: some View {
        if let risk = ask.riskLine {
            let line = warningLine(risk)
            let countdown = ask.deadlineAt != nil ? ScreenCountdown(ask: ask) : nil
            let original = ask.original != nil ? originalButton : nil
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                ViewThatFits(in: .horizontal) {
                    HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                        line
                        Spacer(minLength: Theme.Space.s)
                        countdown
                        original
                    }
                    if let countdown {
                        VStack(alignment: .leading, spacing: Theme.Space.s) {
                            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                                line
                                Spacer(minLength: Theme.Space.s)
                                countdown
                            }
                            original
                        }
                    }
                    VStack(alignment: .leading, spacing: Theme.Space.s) {
                        line
                        if let countdown {
                            ViewThatFits(in: .horizontal) {
                                HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                                    countdown
                                    Spacer(minLength: Theme.Space.s)
                                    original
                                }
                                VStack(alignment: .leading, spacing: Theme.Space.s) {
                                    countdown
                                    original
                                }
                            }
                        } else {
                            original
                        }
                    }
                }
                if showsOriginal, let original = ask.original {
                    Text(original)
                        .font(Theme.Font.caption)
                        .foregroundStyle(.secondary)
                        .textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                        .transition(.opacity)
                }
            }
        } else if ask.deadlineAt != nil {
            ScreenCountdown(ask: ask)
        }
    }

    private var originalButton: some View {
        Button {
            withAnimation(Theme.Motion.standard) { showsOriginal.toggle() }
        } label: {
            Text(showsOriginal ? String(localized: "Hide Original") : String(localized: "Show Original")).lineLimit(1)
        }
        .font(Theme.Font.captionMedium)
        .buttonStyle(.plain)
        .foregroundStyle(Theme.accent)
        .accessibilityIdentifier("screen-warning-original")
    }
}

/// The countdown Claude Code runs before it answers the prompt itself ("Denied automatically in 1:44"): the time drawn
/// by the system (`Text(timerInterval:)`, no timer of ours), and the words once it ran out, from one sleep until then
/// (a `TimelineView(.explicit([end]))` drew its first frame at `end` already: "time's up" from the start).
struct ScreenCountdown: View {
    var ask: CommandAsk
    @State private var over = false

    var body: some View {
        if let end = ask.deadlineAt {
            let now = Date.now
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.s) {
                ThemedIcon(Theme.Icon.time)
                if over || now >= end {
                    Text(ask.expiredLine)
                } else {
                    let time = Text(timerInterval: now...end, countsDown: true)
                    if ask.deadlineAllows {
                        Text("Allowed automatically in \(time)")
                    } else {
                        Text("Denied automatically in \(time)")
                    }
                }
            }
            .font(Theme.Font.captionMedium.monospacedDigit())
            .foregroundStyle(.secondary)
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("screen-countdown")
            .task(id: end) {
                over = false
                let left = end.timeIntervalSinceNow
                if left > 0 { try? await Task.sleep(for: .seconds(left)) }
                if !Task.isCancelled { over = true }
            }
        }
    }
}
