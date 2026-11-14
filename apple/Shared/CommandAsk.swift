import Foundation

/// Claude Code asking on its own terminal to run a shell command (its permission prompt, or its safety check in bypass
/// mode), as the hub reads it off the screen (#5266). The screen card says it in plain words: what the agent wants to
/// run and in its own words why, the warning in plain words with Claude Code's own one tap away, the countdown running,
/// and Deny / Allow for the prompt's No / Yes. Nil from a hub before #5265, and for a dialog that asks about no command.
nonisolated struct CommandAsk: Equatable, Sendable {
    /// The command as the hub joined its wrapped rows back.
    var command: String
    /// The agent's words for it ("Count the change's names in first-screen chunks").
    var description: String?
    /// The boxed warning as Claude Code words it (English), and what it is about (`risk`): `riskLine` says it plainly.
    var warning: String?
    var risk: String?
    /// The countdown sentence as on screen; whether it lets the request through when it runs out (else it refuses it);
    /// and when it runs out, by the hub's clock at the read.
    var deadline: String?
    var deadlineAllows: Bool
    var deadlineAt: Date?

    /// The lines the card shows of a long command until Show All N Lines opens it whole (#6605): fewer when the card is
    /// short, its answers staying in view under it (the card measures where it folds).
    static let foldedLines = 8
    /// Beautiful UI's card shows fewer: its choice rows and Send under the command take more room than two buttons,
    /// and Send stays in the half-page panel's view.
    static let compactFoldedLines = 2

    init?(command: String?, description: String? = nil, warning: String? = nil, risk: String? = nil, deadline: String? = nil,
          deadlineAction: String? = nil, deadlineAt: Date? = nil) {
        func text(_ s: String?) -> String? {
            guard let t = s?.trimmingCharacters(in: .whitespacesAndNewlines), !t.isEmpty else { return nil }
            return t
        }
        guard let command = text(command) else { return nil }
        self.command = command
        self.description = text(description)
        self.warning = text(warning)
        self.risk = text(risk)
        self.deadline = text(deadline)
        self.deadlineAllows = deadlineAction == "allow"
        self.deadlineAt = deadlineAt
    }

    /// The card's heading, in place of the terminal's "Do you want to proceed?".
    static func title(agent: String) -> String { String(localized: "\(agent) Wants to Run a Command") }

    /// The warning in plain words; nil when there is none. A warning the apps have no words for says only that there is
    /// one: its own words are under Show Original.
    var riskLine: String? {
        guard warning != nil else { return nil }
        switch risk {
        case "delete": return String(localized: "Can't confirm this command won't delete files")
        default: return String(localized: "Claude Code warns about this command")
        }
    }

    /// What is under Show Original: Claude Code's own warning and countdown, as on screen.
    var original: String? {
        let lines = [warning, deadline].compactMap { $0 }
        return lines.isEmpty ? nil : lines.joined(separator: "\n")
    }

    /// The countdown's line at `now` ("Denied automatically in 1:44"), or once it ran out what happens; nil without one.
    /// The card draws the time live (`Text(timerInterval:)`) in the same words.
    func countdownLine(now: Date) -> String? {
        guard let deadlineAt else { return nil }
        let left = Int(deadlineAt.timeIntervalSince(now).rounded(.up))
        guard left > 0 else { return expiredLine }
        let time = String(format: "%d:%02d", left / 60, left % 60)
        return deadlineAllows ? String(localized: "Allowed automatically in \(time)") : String(localized: "Denied automatically in \(time)")
    }

    /// The countdown's line once it ran out.
    var expiredLine: String {
        deadlineAllows ? String(localized: "Time's up: Claude Code allows it") : String(localized: "Time's up: Claude Code denies it")
    }
}

nonisolated extension ScreenParser.Dialog {
    /// The prompt's Yes and No, for the card's Allow and Deny: only a command's prompt with just those two choices. One
    /// with more ("Yes, and don't ask again for …") keeps its own labels.
    var allowDeny: (allow: Option, deny: Option)? {
        guard commandAsk != nil, !multi else { return nil }
        let choices = options.filter { $0.kind == .plain }
        guard choices.count == 2, choices.count == options.count else { return nil }
        let words = choices.map { $0.label.trimmingCharacters(in: .whitespaces).lowercased() }
        guard words == ["yes", "no"] else { return nil }
        return (choices[0], choices[1])
    }
}
