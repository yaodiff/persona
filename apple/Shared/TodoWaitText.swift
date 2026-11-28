import Foundation
import PersonaWire

/// Pure localized presentation for server-emitted `TodoWait` (#16168).
/// Clients never derive waiting state from raw supervision, PR, or session work fields; they only format `TodoWait`.
nonisolated struct TodoWaitWords: Sendable {
    var locale: Locale?

    func callAsFunction(_ value: String.LocalizationValue) -> String {
        guard let locale else { return String(localized: value) }
        var resource = LocalizedStringResource(value)
        resource.locale = locale
        return String(localized: resource)
    }

    var formatting: Locale { locale ?? .interface }

    var itemSeparator: String {
        var resource = LocalizedStringResource(", ", comment: "Separator between parts of an accessibility label")
        if let locale { resource.locale = locale }
        return String(localized: resource)
    }
}

nonisolated enum TodoWaitText {
    static let dotSeparator = "\u{00A0}· "

    /// Compact summary for a To-dos list row (default `TodoRow` and V1 `TodoListRow`).
    struct RowSummary: Equatable, Sendable {
        var lead: String
        var timing: String?
        /// True when `timing` represents an explicit timestamp (so V1 suppresses trailing age).
        var timed: Bool
        /// True when `until` has passed and a check-in is due.
        var isDue: Bool
    }

    /// Full structured waiting details for `TodoDetailView`.
    struct DetailModel: Equatable, Sendable {
        struct Contact: Equatable, Sendable {
            var channel: String
            var sender: String?
            var sentAtText: String?
            var request: String?
            var url: URL?
        }

        var kindTitle: String
        var step: String?
        var waitingOn: String?
        var handoffTodoID: String?
        var reason: String?
        var blockersText: String?
        var wakeCondition: String?
        var wakeAtText: String?
        var untilText: String?
        var untilIsDue: Bool
        var sinceText: String?
        var sourceURL: URL?
        var isPR: Bool
        var contact: Contact?
    }

    static func kindLabel(_ kind: TodoWaitKind, words: TodoWaitWords = TodoWaitWords()) -> String {
        switch kind {
        case .personReply: words("Waiting for reply [Todo Wait]")
        case .mergeReview: words("Waiting for review [Todo Wait]")
        case .prMerge: words("Waiting to merge [Todo Wait]")
        case .externalEvent: words("Waiting on external event [Todo Wait]")
        case .date: words("Waiting until date [Todo Wait]")
        case .todoHandoff: words("Waiting on another to-do [Todo Wait]")
        case .agent: words("Waiting on another Agent [Todo Wait]")
        case .usageLimit: words("Usage limit reached [Todo Wait]")
        case .parked: words("Parked [Todo Wait]")
        case .unknown: words("Waiting [Todo Wait]")
        }
    }

    static func blockerLabel(_ blocker: TodoWaitPRBlocker, words: TodoWaitWords = TodoWaitWords()) -> String? {
        switch blocker {
        case .reviewRequired: words("Needs review [Todo Wait]")
        case .changesRequested: words("Changes requested [Todo Wait]")
        case .checksPending: words("Checks running [Todo Wait]")
        case .checksFailing: words("Checks failing [Todo Wait]")
        case .conflicts: words("Merge conflicts [Todo Wait]")
        case .unknown: nil
        }
    }

    static func rowSummary(_ wait: TodoWait, now: Date, words: TodoWaitWords = TodoWaitWords(),
                           calendar: Calendar = .current) -> RowSummary {
        let target = trimmed(wait.waitingOn)
        let reason = trimmed(wait.reason)
        let blockers = blockerSummary(wait.prBlockers, words: words)

        var lead: String
        switch wait.kind {
        case .usageLimit:
            if let target {
                lead = words("\(target) reached its usage limit [Todo Wait]")
            } else {
                lead = kindLabel(.usageLimit, words: words)
            }
        case .prMerge:
            let detail = blockers ?? reason
            if let target, let detail {
                lead = target + dotSeparator + detail
            } else if let target {
                lead = words("Waiting to merge \(target) [Todo Wait]")
            } else if let detail {
                lead = detail
            } else {
                lead = kindLabel(.prMerge, words: words)
            }
        default:
            if let target, let reason {
                if reason.localizedCaseInsensitiveContains(target) {
                    lead = reason
                } else {
                    lead = target + dotSeparator + reason
                }
            } else if let reason {
                lead = reason
            } else if let target {
                lead = words("Waiting on \(target) [Todo Wait]")
            } else {
                lead = kindLabel(wait.kind, words: words)
            }
        }

        if let step = trimmed(wait.step), !lead.hasPrefix(step) {
            lead = step + dotSeparator + lead
        }

        let wakeDate = wait.wakeAt.flatMap(JSON.parseDate)
        let untilDate = wait.until.flatMap(JSON.parseDate)
        let sinceDate = wait.since.flatMap(JSON.parseDate)

        var timing: String?
        var timed = false
        var isDue = false

        if let wakeDate {
            let formatted = shortMoment(wakeDate, now: now, words: words, calendar: calendar)
            timed = true
            if wait.kind == .usageLimit {
                timing = words("Back \(formatted) [Todo Wait]")
            } else {
                timing = words("Wakes \(formatted) [Todo Wait]")
            }
        } else if let untilDate {
            let formatted = shortMoment(untilDate, now: now, words: words, calendar: calendar)
            timed = true
            if untilDate <= now {
                isDue = true
                timing = words("Check-in due (\(formatted)) [Todo Wait]")
            } else {
                timing = words("Until \(formatted) [Todo Wait]")
            }
        } else if let wakeCondition = trimmed(wait.wakeCondition) {
            timing = wakeCondition
        } else if let sinceDate {
            let formatted = shortMoment(sinceDate, now: now, words: words, calendar: calendar)
            timed = true
            timing = words("Since \(formatted) [Todo Wait]")
        }

        return RowSummary(lead: lead, timing: timing, timed: timed, isDue: isDue)
    }

    static func detail(_ wait: TodoWait, now: Date, words: TodoWaitWords = TodoWaitWords(),
                       calendar: Calendar = .current) -> DetailModel {
        let wakeDate = wait.wakeAt.flatMap(JSON.parseDate)
        let untilDate = wait.until.flatMap(JSON.parseDate)
        let sinceDate = wait.since.flatMap(JSON.parseDate)

        let wakeAtText = wakeDate.map { fullMoment($0, now: now, words: words, calendar: calendar) }
        // Deduplicate when wake_at and until name the exact same instant (for example a `date` park).
        let showSeparateUntil: Bool = {
            guard let untilDate else { return false }
            if let wakeDate, abs(wakeDate.timeIntervalSince(untilDate)) < 1 { return false }
            return true
        }()
        let untilText = showSeparateUntil ? untilDate.map { fullMoment($0, now: now, words: words, calendar: calendar) } : nil
        let untilIsDue = untilDate.map { $0 <= now } ?? false
        let sinceText = sinceDate.map { fullMoment($0, now: now, words: words, calendar: calendar) }

        let contact: DetailModel.Contact? = wait.contact.flatMap { c in
            guard let channel = trimmed(c.channel) else { return nil }
            let sentAt = c.sentAt.flatMap(JSON.parseDate).map { fullMoment($0, now: now, words: words, calendar: calendar) }
            return DetailModel.Contact(
                channel: channel,
                sender: trimmed(c.sender),
                sentAtText: sentAt,
                request: trimmed(c.request),
                url: webURL(c.url)
            )
        }

        let sourceURL = webURL(wait.url)
        let dedupedSourceURL: URL? = (sourceURL != nil && sourceURL == contact?.url) ? nil : sourceURL

        return DetailModel(
            kindTitle: kindLabel(wait.kind, words: words),
            step: trimmed(wait.step),
            waitingOn: trimmed(wait.waitingOn),
            handoffTodoID: trimmed(wait.handoffTodoId?.rawValue),
            reason: trimmed(wait.reason),
            blockersText: blockerSummary(wait.prBlockers, words: words),
            wakeCondition: trimmed(wait.wakeCondition),
            wakeAtText: wakeAtText,
            untilText: untilText,
            untilIsDue: untilIsDue,
            sinceText: sinceText,
            sourceURL: dedupedSourceURL,
            isPR: wait.kind == .prMerge || wait.kind == .mergeReview,
            contact: contact
        )
    }

    private static func blockerSummary(_ blockers: [TodoWaitPRBlocker]?, words: TodoWaitWords) -> String? {
        guard let blockers, !blockers.isEmpty else { return nil }
        let labels = blockers.compactMap { blockerLabel($0, words: words) }
        return labels.isEmpty ? nil : labels.joined(separator: words.itemSeparator)
    }

    private static func trimmed(_ raw: String?) -> String? {
        guard let s = raw?.trimmingCharacters(in: .whitespacesAndNewlines), !s.isEmpty else { return nil }
        return s
    }

    private static func webURL(_ raw: String?) -> URL? {
        guard let s = trimmed(raw), let url = URL(string: s),
              let scheme = url.scheme?.lowercased(), scheme == "http" || scheme == "https" else { return nil }
        return url
    }

    /// Compact moment for list rows: time only today ("15:00"), weekday + time within 6 days ("Fri 18:00"), else date ("Nov 25").
    static func shortMoment(_ date: Date, now: Date, words: TodoWaitWords, calendar: Calendar) -> String {
        var style = Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).locale(words.formatting)
        if calendar.isDate(date, inSameDayAs: now) {
            return date.formatted(style.hour().minute())
        }
        let days = abs(calendar.dateComponents([.day], from: calendar.startOfDay(for: now), to: calendar.startOfDay(for: date)).day ?? 99)
        if days <= 6 {
            style = style.weekday(.abbreviated).hour().minute()
        } else {
            style = style.month(.abbreviated).day()
            if calendar.component(.year, from: date) != calendar.component(.year, from: now) {
                style = style.year()
            }
        }
        return date.formatted(style)
    }

    /// Full moment for the detail screen: "Fri, Nov 20, 18:00" (year included only when not this year).
    static func fullMoment(_ date: Date, now: Date, words: TodoWaitWords, calendar: Calendar) -> String {
        var style = Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).locale(words.formatting)
            .weekday(.abbreviated).month(.abbreviated).day().hour().minute()
        if calendar.component(.year, from: date) != calendar.component(.year, from: now) {
            style = style.year()
        }
        return date.formatted(style)
    }
}
