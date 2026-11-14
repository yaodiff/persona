import Foundation
import os

/// The one set of names for an agent's and a task's state on iOS, Mac, the watch and the widgets (#3960; docs/protocol/i18n.md
/// "Agent states", docs/protocol/agent-states.md), as the web has in `web/src/lib/state-labels.ts` (#3415): every screen takes
/// its state words from here, so a state is never 「需要你」 on one screen and 「等你」 on the next. The words are the catalog's
/// `… [State Names]` keys; `StateNamesTests` keeps other entries and other files from defining them again.
/// `interfaceText`, so the Live Activity follows the app's language at once.
nonisolated enum StateName: String, CaseIterable, Sendable {
    case needsYou, working, unread, seen, notRunning, unknown
    /// Waiting to be woken (`isWaiting`): a loop or a timer brings it back.
    case waiting
    /// Seen, but its last turn asks the user something (#5096).
    case awaitsReply
    /// Only a task or a summary is done; an agent is seen.
    case done
    /// A to-do or task the agent stopped on that wants a follow-up.
    case followUp
    /// A to-do or task whose agent ended its turn but waits to be brought back: its background work, or the hub's wake-up (#3274).
    case taskWaiting

    /// Resolved once per interface locale: these words are drawn on every redraw of the Agents list, its filter chips and
    /// row badges, and each `interfaceText` lookup reads the localization tables (#5438).
    var text: String { Self.kept("state." + rawValue) { resolve() } }

    private func resolve() -> String {
        switch self {
        case .needsYou: interfaceText("Needs You [State Names]")
        case .working: interfaceText("Working [State Names]")
        case .unread: interfaceText("Unread [State Names]")
        case .seen: interfaceText("Seen [State Names]")
        case .notRunning: interfaceText("Not Running [State Names]")
        case .unknown: interfaceText("Unknown [State Names]")
        case .waiting: interfaceText("Waiting [State Names]")
        case .awaitsReply: interfaceText("Awaiting Reply [State Names]")
        case .done: interfaceText("Done [State Names]")
        case .followUp: interfaceText("Follow-Up [State Names]")
        case .taskWaiting: interfaceText("Task Waiting [State Names]")
        }
    }

    /// The sentence-form Needs you used by the shared inbox state table. The title-form `needsYou.text` is unchanged.
    /// InboxNow resolves this once per language; an explicit locale also lets the shared fixtures check all three.
    static func needsYouSentence(for locale: Locale) -> String {
        var resource: LocalizedStringResource = "Needs you [State Names]"
        resource.locale = locale
        return String(localized: resource)
    }

    /// The states a list counts in its headers and summaries: "3 need you".
    enum Counted: Sendable { case needsYou, working, unread }

    static func count(_ s: Counted, _ n: Int) -> String {
        switch s {
        case .needsYou: interfaceText("\(n) need you [State Names]")
        case .working: interfaceText("\(n) working [State Names]")
        case .unread: interfaceText("\(n) unread [State Names]")
        }
    }

    /// A to-do's state in the hub's direction tree (#4352, `DirectionTree.TaskState`): the owner's own words for the
    /// work a direction supervises (working / needs you / stuck / to verify), on every platform; an Agent's state keeps
    /// the names above. Rows draw them on every redraw, so each is resolved once per locale (#6263).
    static var taskWorking: String {
        kept("taskWorking") { interfaceText("Task Working [State Names]") } // perf-ok: resolved once per locale
    }
    static var taskNeedsYou: String {
        kept("taskNeedsYou") { interfaceText("Task Needs You [State Names]") } // perf-ok: resolved once per locale
    }
    static var taskStuck: String {
        kept("taskStuck") { interfaceText("Stuck [State Names]") } // perf-ok: resolved once per locale
    }
    /// A piece of a direction no Agent has started yet, the hub's `planned` (#5860).
    static var taskNotStarted: String {
        kept("taskNotStarted") { interfaceText("Task Not Started [State Names]") } // perf-ok: resolved once per locale
    }
    /// The same words as a supervised task awaiting review (`ReviewState.pendingReview`).
    static var taskToVerify: String {
        kept("taskToVerify") { interfaceText("Awaiting review") } // perf-ok: resolved once per locale
    }

    /// Words resolved once per interface locale (docs/apple/performance.md P4): a dictionary read after the first.
    private static let resolved = OSAllocatedUnfairLock<[String: String]>(initialState: [:])

    private static func kept(_ name: String, _ resolve: () -> String) -> String {
        let key = interfaceTextLocale.identifier + "|" + name
        if let known = resolved.withLock({ $0[key] }) { return known }
        let text = resolve()
        resolved.withLock { $0[key] = text }
        return text
    }

    /// The button that marks every finished, unseen agent as seen, with how many when given.
    static func markAllSeen(_ n: Int? = nil) -> String {
        guard let n else { return interfaceText("Mark All as Seen [State Names]") }
        return interfaceText("Mark All as Seen (\(n)) [State Names]")
    }
}
