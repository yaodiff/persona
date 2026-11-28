import Foundation
import PersonaWire

/// How the To-dos words are resolved: in the app's language through the bundle's cached tables (the app passes no
/// locale, docs/apple/performance.md P4), or in a given language (tests check all three).
nonisolated struct TodoWords: Sendable {
    var locale: Locale?

    func callAsFunction(_ value: String.LocalizationValue) -> String {
        guard let locale else { return String(localized: value) }
        var resource = LocalizedStringResource(value)
        resource.locale = locale
        return String(localized: resource)
    }

    /// The locale dates and times are written in.
    var formatting: Locale { locale ?? .interface }

    /// Between the parts of a spoken label, in the language.
    var listSeparator: String {
        var resource = LocalizedStringResource(", ", comment: "Separator between parts of an accessibility label")
        if let locale { resource.locale = locale }
        return String(localized: resource)
    }
}

/// A row's second line in words (#10465; docs/design/proposals/todos-redesign.md 3.4): the hub sends a key and string
/// arguments (`TodoLine`, spec/fixtures/todo-view/README.md "Line keys"), and the app says it in its language. Absolute
/// times become "since Tue" here, in the list's model, never in a view's body. A key this build does not know is not
/// said: the row says its section's word instead (`TodoRowContentLogic`). Pure.
nonisolated enum TodoLineText {
    /// What the line's lead is drawn in; the rest is always the secondary color.
    enum Tone: Equatable, Sendable {
        /// The ask's own words, in the needs-you text tone.
        case needs
        /// A failed start: both parts in danger text ("Didn't start", the shipped word for it in a line's sentence case).
        case danger
        /// What finished, in the unread green.
        case ready
        case plain
    }

    struct Said: Equatable, Sendable {
        /// Drawn in the tone: what asks, what failed, what finished, or who works on it.
        var lead: String?
        /// After a separator, in the secondary color.
        var rest: String?
        var tone: Tone = .plain
        /// It says a time ("since Tue", "until Fri", "Finished Tue"): the row's end says none beside it.
        var timed = false

        /// One string, for the row's spoken label.
        func spoken(separator: String) -> String { [lead, rest].compactMap { $0 }.joined(separator: separator) }
    }

    /// Every key this build says: the README's table (`TodoLineTextTests` keeps the two the same).
    static let keys = Set(TodoLineKey.all)

    /// "Persona picks Ryokan Hanami on Fri unless you answer": what happens when nobody answers a decision, `when` its
    /// default's day or time of day in words (`today`: the time). The line and an ask's card (`AskCardModel.about`) say
    /// it in the same words. nil with neither a pick nor a time.
    static func decisionDefault(pick: String?, when: String?, today: Bool, words: TodoWords) -> String? {
        switch (pick, when) {
        case let (pick?, when?) where today: words("Persona picks \(pick) at \(when) unless you answer [To-dos Line]")
        case let (pick?, when?): words("Persona picks \(pick) on \(when) unless you answer [To-dos Line]")
        case let (nil, when?) where today: words("Persona decides at \(when) unless you answer [To-dos Line]")
        case let (nil, when?): words("Persona decides on \(when) unless you answer [To-dos Line]")
        case let (pick?, nil): words("Persona picks \(pick) unless you answer [To-dos Line]")
        case (nil, nil): nil
        }
    }

    /// The line in words; nil for no line, or a key this build does not know. `since`: when the row came to where it is
    /// (the view's `since`), said on the lines of rows that wait. `closedReason`: the to-do's own words on how it ended.
    static func said(_ line: TodoLine?, since: Date? = nil, closedReason: String? = nil, now: Date, words: TodoWords = TodoWords(),
                     calendar: Calendar = .current) -> Said? {
        guard let line else { return nil }
        // The line is shared with the to-do's conversation, whose keys are others (TodoLine.key stays a string): read
        // as a row's key, one this build does not say is .unknown.
        let key = TodoLineKey(rawValue: line.key)
        guard keys.contains(key) else { return nil }
        let args = line.args ?? [:]
        func arg(_ name: String) -> String? { args[name].flatMap { $0.isEmpty ? nil : $0 } }
        func time(_ name: String) -> Date? { arg(name).flatMap(JSON.parseDate) }
        var timed = false
        func day(_ date: Date) -> String {
            timed = true
            return TodoTimeWords.day(date, now: now, words: words, calendar: calendar)
        }
        func sinceRest() -> String? { since.map { words("since \(day($0)) [To-dos Line]") } }
        let check = words("check and tick [To-dos Line]")
        let run = arg("run")
        var said: Said
        switch key {
        case TodoLineKey.failedStart:
            said = Said(lead: words("Didn't start [To-dos Line]"), rest: arg("error"), tone: .danger)
        case TodoLineKey.agentAsks:
            let who = run ?? words("An Agent [To-dos Line]")
            if let text = arg("text") {
                said = Said(lead: words("\(who): \(text) [To-dos Line]"), tone: .needs)
            } else {
                said = Said(lead: words("\(who) needs you [To-dos Line]"), tone: .needs)
            }
        case TodoLineKey.interrupted:
            said = Said(lead: words("\(run ?? words("An Agent [To-dos Line]")) stopped before it finished [To-dos Line]"), tone: .needs)
        case TodoLineKey.stoppedUnfinished:
            // A run that stopped short with no follow-up for an hour (#9823): said as an interrupted one, then what its
            // closeout says it waits on or misses, when the hub sends that.
            said = Said(lead: words("\(run ?? words("An Agent [To-dos Line]")) stopped before it finished [To-dos Line]"),
                        rest: arg("text"), tone: .needs)
        case TodoLineKey.decision:
            said = Said(lead: arg("text") ?? words("A decision for you [To-dos Line]"),
                        rest: arg("recommend").map { words("\($0) recommended [To-dos Line]") }, tone: .needs)
        case TodoLineKey.decisionDefault:
            // "on Fri", "on Apr 3", or "at 10:00" today.
            let at = time(TodoLineArg.defaultAt)
            let today = at.map { calendar.isDate($0, inSameDayAs: now) } ?? false
            let when = at.map(day)
            said = Said(lead: arg("text"), rest: decisionDefault(pick: arg("recommend"), when: when, today: today, words: words))
        case TodoLineKey.cannot:
            said = Said(lead: words("Persona says it can't be done as asked [To-dos Line]"), tone: .needs)
        case TodoLineKey.awaitsReply:
            said = Said(lead: StateName.awaitsReply.text,
                        rest: arg("text") ?? run.map { words("\($0) is waiting for your reply [To-dos Line]") })
        case TodoLineKey.working:
            let who = run ?? words("An Agent [To-dos Line]")
            said = arg("text").map { Said(lead: who, rest: $0) } ?? Said(lead: words("\(who) is working [To-dos Line]"))
        case TodoLineKey.personaWorking:
            said = arg("text").map { Said(lead: "Persona", rest: $0) } ?? Said(lead: words("Persona is working [To-dos Line]"))
        case TodoLineKey.starting:
            said = Said(lead: run.map { words("Starting \($0)… [To-dos Line]") } ?? words("Starting… [To-dos Line]"))
        case TodoLineKey.background:
            said = Said(lead: words("\(run ?? words("An Agent [To-dos Line]")) is working in the background [To-dos Line]"))
        case TodoLineKey.parked:
            let until = time(TodoLineArg.until).map { words("until \(day($0)) [To-dos Line]") }
            said = Said(lead: arg("reason") ?? words("Put aside [To-dos Line]"), rest: until ?? sinceRest())
        case TodoLineKey.finished:
            said = Said(lead: run.map { words("\($0) finished [To-dos Line]") } ?? words("Finished [To-dos Line]"), rest: check,
                        tone: .ready)
        case TodoLineKey.accepted:
            said = Said(lead: words("Accepted [To-dos Line]"), rest: check, tone: .ready)
        case TodoLineKey.deployed:
            said = Said(lead: words("Live [To-dos Line]"), rest: check, tone: .ready)
        case TodoLineKey.prOpen:
            said = Said(lead: words("Waiting for a merge [To-dos Line]"), rest: sinceRest())
        case TodoLineKey.usageLimit:
            let lead = run.map { words("\($0) reached its usage limit [To-dos Line]") } ?? words("Usage limit reached [To-dos Line]")
            said = Said(lead: lead, rest: time(TodoLineArg.resetsAt).map { words("back \(day($0)) [To-dos Line]") })
        case TodoLineKey.runWaits:
            let who = run ?? words("An Agent [To-dos Line]")
            let lead = arg("on") == "agent" ? words("\(who) is waiting for another Agent [To-dos Line]")
                : words("\(who) is waiting for something outside [To-dos Line]")
            said = Said(lead: lead, rest: sinceRest())
        case TodoLineKey.finishedSeen:
            said = Said(lead: time(TodoLineArg.at).map { words("Finished \(day($0)) [To-dos Line]") } ?? words("Finished [To-dos Line]"))
        case TodoLineKey.completed:
            let lead: String = switch arg("by") {
            case "auto": words("Done by Persona [To-dos Line]")
            case "review": words("Done after review [To-dos Line]")
            case nil: words("You marked it done [To-dos Line]")
            default: StateName.done.text
            }
            said = Said(lead: lead, rest: closedReason)
        case TodoLineKey.canceled:
            said = Said(lead: words("Canceled [To-dos Line]"), rest: arg("reason"))
        case TodoLineKey.reported:
            said = Said(lead: words("Reported back [To-dos Line]"))
        case TodoLineKey.superseded:
            said = Said(lead: words("Finished in a related to-do [To-dos Line]"))
        case TodoLineKey.someday:
            said = Said(lead: StateName.todoSomeday, rest: time(TodoLineArg.until).map { words("from \(day($0)) [To-dos Line]") })
        case .unknown:
            return nil
        }
        // A to-do that carries one of its steps says which: "Ryokan · Which of 3?"; not twice when the line names it
        // already (a step's run is often called as the step is).
        if let step = arg("step"), !(said.lead?.hasPrefix(step) ?? false) {
            said.lead = said.lead.map { step + TodoTimeWords.separator + $0 } ?? step
        }
        said.timed = timed
        return said
    }
}

/// Times as a row says them, from now (#10465): a line's by the clock today, the week's by its day, the rest by date; the
/// row's end in words that say what they are ("Due Fri", "16m ago"), never a bare day or clock.
nonisolated enum TodoTimeWords {
    /// Between the parts of a line: "Which ryokan? · Ryokan Hanami recommended". The space before the dot does not
    /// break, so a line that wraps keeps "·" with the word before it.
    static let separator = "\u{00A0}· "

    /// "10:00" today; "Tue" within six days either way; "Apr 3" further.
    static func day(_ date: Date, now: Date, words: TodoWords, calendar: Calendar) -> String {
        var style = Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).locale(words.formatting)
        if calendar.isDate(date, inSameDayAs: now) { return date.formatted(style.hour().minute()) }
        let days = abs(calendar.dateComponents([.day], from: calendar.startOfDay(for: now), to: calendar.startOfDay(for: date)).day ?? 99)
        style = days <= 6 ? style.weekday(.abbreviated) : style.month(.abbreviated).day()
        return date.formatted(style)
    }

    /// A due day at a row's end: "Due today", "Due Thu" within the week, "Due Apr 3" further, with its year when not
    /// this one.
    static func due(_ date: Date, now: Date, words: TodoWords, calendar: Calendar) -> String {
        if calendar.isDate(date, inSameDayAs: now) { return words("Due today [To-dos Due]") }
        var style = Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).locale(words.formatting)
        let days = calendar.dateComponents([.day], from: calendar.startOfDay(for: now), to: calendar.startOfDay(for: date)).day ?? 99
        // A day's name has words of its own: Japanese adds the word for "day of the week" after an abbreviated one.
        if (1...6).contains(days) { return words("Due \(date.formatted(style.weekday(.abbreviated))) [To-dos Due Weekday]") }
        style = style.month(.abbreviated).day()
        if calendar.component(.year, from: date) != calendar.component(.year, from: now), abs(days) > 180 { style = style.year() }
        return words("Due \(date.formatted(style)) [To-dos Due]")
    }

    /// A due moment's time alone, under a heading that names its day (Upcoming): "Due 15:00".
    static func dueTime(_ date: Date, words: TodoWords, calendar: Calendar) -> String {
        let style = Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).locale(words.formatting).hour().minute()
        return words("Due \(date.formatted(style)) [To-dos Due]")
    }

    /// How long ago, as short as a row's end has room for and never a day's name, which would read as a due day: "now",
    /// "16m ago", "3h ago", "2d ago".
    static func age(_ date: Date, now: Date, words: TodoWords, calendar: Calendar) -> String {
        let seconds = now.timeIntervalSince(date)
        if seconds < 60 { return words("now [To-dos Age]") }
        if seconds < 3600 { return words("\(Int(seconds / 60))m ago [To-dos Age]") }
        if seconds < 86_400 { return words("\(Int(seconds / 3600))h ago [To-dos Age]") }
        return words("\(days(date, now: now, calendar: calendar))d ago [To-dos Age]")
    }

    /// Whole days from `date`'s day to today's, at least one.
    private static func days(_ date: Date, now: Date, calendar: Calendar) -> Int {
        max(1, calendar.dateComponents([.day], from: calendar.startOfDay(for: date), to: calendar.startOfDay(for: now)).day ?? 1)
    }

    // Said in full for VoiceOver, where "16m" reads as a length and "Apr 4" does not say what it is.

    /// "Due today", "Due Saturday", "Due April 3".
    static func spokenDue(_ date: Date, now: Date, words: TodoWords, calendar: Calendar) -> String {
        if calendar.isDate(date, inSameDayAs: now) { return words("Due today [To-dos Spoken]") }
        var style = Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).locale(words.formatting)
        let days = calendar.dateComponents([.day], from: calendar.startOfDay(for: now), to: calendar.startOfDay(for: date)).day ?? 99
        style = (1...6).contains(days) ? style.weekday(.wide) : style.month(.wide).day()
        return words("Due \(date.formatted(style)) [To-dos Spoken]")
    }

    /// "Just now", "16 minutes ago", "3 hours ago", "2 days ago".
    static func spokenAge(_ date: Date, now: Date, words: TodoWords, calendar: Calendar) -> String {
        let seconds = now.timeIntervalSince(date)
        if seconds < 60 { return words("Just now [To-dos Spoken]") }
        if seconds < 3600 { return words("\(Int(seconds / 60)) minutes ago [To-dos Spoken]") }
        if seconds < 86_400 { return words("\(Int(seconds / 3600)) hours ago [To-dos Spoken]") }
        return words("\(days(date, now: now, calendar: calendar)) days ago [To-dos Spoken]")
    }
}
