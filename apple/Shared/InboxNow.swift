import Foundation

/// The shared words for an inbox matter's current state and row metadata (spec/fixtures/inbox/now.json and row.json).
/// The manager sends keys and facts; each client resolves the same localized table instead of deriving its own state.
nonisolated enum InboxNow {
    /// The status mark shared by the row and detail: waiting is still, only active AI work spins, and action states
    /// carry the existing Needs-you mark. Done, FYI, folded and unknown keys draw no mark.
    enum Symbol: String, Equatable, Sendable {
        case time, spinner, needsYou, failed
    }

    static func symbol(for item: InboxItem) -> Symbol? {
        switch item.now {
        case "waiting": .time
        case "queued", "looking", "agent_working": .spinner
        case "confirm_reply", "confirm_agent", "decide", "follow_up_ask", "check_due", "needs_you": .needsYou
        case "failed": .failed
        default: nil
        }
    }

    private static let wordMemo = TextMemo<String>(limit: 96)
    private static let timeMemo = TextMemo<String>(limit: 128)

    /// The row's state in the launched interface language. Only this convenience reads a clock; the compositor below is pure.
    static func text(for item: InboxItem, now: Date = AppClock.now, calendar: Calendar = .current) -> String? {
        let lang = interfaceTextLocale.identifier
        let formattedTime: String?
        if item.now == "waiting", let checkAt = item.follow?.checkAt {
            formattedTime = time(checkAt, lang: lang, now: now, calendar: calendar)
        } else {
            formattedTime = nil
        }
        return text(item, lang: lang, time: formattedTime)
    }

    /// The complete state sentence. `time` is the already formatted `follow.check_at`, supplied by the caller so tests
    /// and rows do not depend on an implicit clock. An absent or unknown key returns nil for the app's existing state words.
    static func text(_ item: InboxItem, lang: String, time: String? = nil) -> String? {
        guard let key = item.now else { return nil }
        switch key {
        case "waiting":
            let who = nonEmpty(item.follow?.who)
            guard var result = word(who == nil ? "waiting_anyone" : "waiting", lang: lang) else { return nil }
            if let who {
                result = result.replacingOccurrences(of: "{who}", with: who)
            }
            if item.follow?.checkAt != nil, let time, !time.isEmpty, let again = word("look_again", lang: lang) {
                result += again.replacingOccurrences(of: "{time}", with: time)
            }
            if let next = nonEmpty(item.follow?.next), let suffix = word("next", lang: lang) {
                result += suffix.replacingOccurrences(of: "{next}", with: next)
            }
            return result
        case "check_due":
            guard var result = word(key, lang: lang) else { return nil }
            if let next = nonEmpty(item.follow?.next), let suffix = word("next", lang: lang) {
                result += suffix.replacingOccurrences(of: "{next}", with: next)
            }
            return result
        case "failed":
            if let reason = nonEmpty(item.handling?.error), let template = word(key, lang: lang) {
                return template.replacingOccurrences(of: "{reason}", with: reason)
            }
            return word("failed_plain", lang: lang)
        case "confirm_reply", "confirm_agent", "decide", "queued", "looking", "agent_working",
             "follow_up_ask", "needs_you", "fyi", "folded", "done_replied", "done_manager", "done_user",
             "done_read", "done_dismissed", "done_muted", "done_superseded":
            return word(key, lang: lang)
        default:
            return nil
        }
    }

    /// The row's third line when the hub supplies `where`: "where · who: newest line". A direct chat names its person
    /// once. Nil means that the caller keeps its existing source-specific line (an older hub, or mail with no where).
    static func meta(_ item: InboxItem, lang: String) -> String? {
        guard let place = nonEmpty(item.where), let separator = word("labelValueSeparator", lang: lang) else { return nil }
        let sender = nonEmpty(item.sender)
        let preview = item.preview
        if let sender {
            let who = preview.isEmpty ? sender : sender + separator + preview
            return sender == place ? who : place + " · " + who
        }
        return preview.isEmpty ? place : place + " · " + preview
    }

    /// A follow-up's short future moment: 24-hour time today, the localized word for tomorrow and time, then a localized
    /// short date and time. Another year includes the year. The result is memoized per moment, branch, calendar and language
    /// (docs/apple/performance.md P4).
    static func time(_ date: Date, lang: String, now: Date, calendar: Calendar = .current) -> String {
        let days = calendar.dateComponents(
            [.day],
            from: calendar.startOfDay(for: now),
            to: calendar.startOfDay(for: date)
        ).day ?? 0
        let sameYear = calendar.component(.year, from: date) == calendar.component(.year, from: now)
        let branch: String
        if days == 0 {
            branch = "today"
        } else if days == 1 {
            branch = "tomorrow"
        } else {
            branch = sameYear ? "sameYear" : "crossYear"
        }
        let key = "\(Int(date.timeIntervalSince1970))|\(branch)|\(calendar.identifier)|\(calendar.timeZone.identifier)|\(lang)"
        return timeMemo(key) { _ in
            let parts = calendar.dateComponents([.hour, .minute], from: date)
            let clock = String(format: "%02d:%02d", parts.hour ?? 0, parts.minute ?? 0)
            if days == 0 { return clock }
            if days == 1, let tomorrow = word("tomorrow", lang: lang) {
                return tomorrow + " " + clock
            }
            var style = Date.FormatStyle.dateTime.month(.abbreviated).day()
            if !sameYear { style = style.year() }
            style.locale = Locale(identifier: lang)
            style.calendar = calendar
            style.timeZone = calendar.timeZone
            let day = date.formatted(style)
            return day + " " + clock
        }
    }

    private static let knownWords: Set<String> = [
        "confirm_reply", "confirm_agent", "decide", "queued", "looking", "agent_working", "waiting", "waiting_anyone",
        "look_again", "next", "tomorrow", "follow_up_ask", "check_due", "failed", "failed_plain", "needs_you", "fyi", "folded",
        "done_replied", "done_manager", "done_user", "done_read", "done_dismissed", "done_muted", "done_superseded",
        "labelValueSeparator",
    ]

    private static func word(_ key: String, lang: String) -> String? {
        guard knownWords.contains(key) else { return nil }
        return wordMemo(lang + "|" + key) { _ in resolveWord(key, lang: lang) }
    }

    private static func resolveWord(_ key: String, lang: String) -> String {
        switch key {
        case "confirm_reply": resolve("Waiting for you to confirm a reply [Inbox Now]", lang: lang)
        case "confirm_agent": resolve("Waiting for you to start an Agent [Inbox Now]", lang: lang)
        case "decide": resolve("Waiting for your decision [Inbox Now]", lang: lang)
        case "queued": resolve("Queued for the AI [Inbox Now]", lang: lang)
        case "looking": resolve("AI is looking", lang: lang)
        case "agent_working": resolve("Agent working", lang: lang)
        case "waiting": resolve("Waiting for {who} [Inbox Now]", lang: lang)
        case "waiting_anyone": resolve("Waiting for a reply", lang: lang)
        case "look_again": resolve(" · look again {time} [Inbox Now]", lang: lang)
        case "next": resolve(": {next} [Inbox Now]", lang: lang)
        case "tomorrow": resolve("tomorrow [Inbox Now]", lang: lang)
        case "follow_up_ask": resolve("Checked 3 times with no news. Keep following? [Inbox Now]", lang: lang)
        case "check_due": resolve("Time to look again [Inbox Now]", lang: lang)
        case "failed": resolve("AI couldn't finish: {reason} [Inbox Now]", lang: lang)
        case "failed_plain": resolve("AI couldn't handle it", lang: lang)
        case "needs_you": StateName.needsYouSentence(for: Locale(identifier: lang))
        case "fyi": resolve("FYI", lang: lang)
        case "folded": resolve("Folded [Inbox Now]", lang: lang)
        case "done_replied": resolve("You replied [Inbox Now]", lang: lang)
        case "done_manager": resolve("Assistant checked: nothing to do [Inbox Now]", lang: lang)
        case "done_user": resolve("You marked it done [Inbox Now]", lang: lang)
        case "done_read": resolve("Read elsewhere [Inbox Now]", lang: lang)
        case "done_dismissed": resolve("You dismissed it [Inbox Now]", lang: lang)
        case "done_muted": resolve("Muted [Inbox Now]", lang: lang)
        case "done_superseded": resolve("Replaced by a newer item [Inbox Now]", lang: lang)
        case "labelValueSeparator": resolve(": ", lang: lang)
        default: ""
        }
    }

    private static func resolve(_ resource: LocalizedStringResource, lang: String) -> String {
        var resource = resource
        resource.locale = Locale(identifier: lang)
        return String(localized: resource)
    }

    private static func nonEmpty(_ value: String?) -> String? {
        value.flatMap { $0.isEmpty ? nil : $0 }
    }
}
