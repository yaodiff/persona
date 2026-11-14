import Foundation
import PersonaWire

/// A to-do's owner-facing review round, shared with briefing cards. The timestamp stays byte-for-byte
/// as the hub sent it so accepting a recommendation cannot answer a newer round.
///
/// A decision (#3804, docs/assistant/todos.md "Decisions the owner can answer") carries more: what is to be decided, why
/// only the owner can, the options with what each leads to, what waiting costs, where the task came from and where it
/// stands, and what happens when nobody answers. Those fields are absent from an older hub and from an ask that is no
/// decision; each is decoded on its own, so one in a shape this build does not know never hides the ask.
nonisolated struct TodoAsk: Codable, Hashable, Sendable {
    /// One option of a decision: its label, which is the answer, and what choosing it leads to.
    nonisolated struct Option: Codable, Hashable, Sendable {
        var label: String
        var leadsTo: String?

        enum CodingKeys: String, CodingKey {
            case label
            case leadsTo = "leads_to"
        }

        init(label: String, leadsTo: String? = nil) {
            self.label = label
            self.leadsTo = leadsTo
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            label = (try? c.decodeIfPresent(String.self, forKey: .label)) ?? ""
            leadsTo = try? c.decodeIfPresent(String.self, forKey: .leadsTo)
        }
    }

    /// The fixed answers besides the options (`responses`): the owner cannot answer this as asked.
    nonisolated enum Response: String, CaseIterable, Sendable {
        case decideYourself = "decide_yourself"
        case notWhatIAsked = "not_what_i_asked"
        case explainFirst = "explain_first"
    }

    var outcome: String?
    var verdict: String?
    var recommend: String?
    var askedAt: String
    /// What is to be decided, in one sentence.
    var question: String?
    /// Why only the owner can decide: irreversible, visible_to_others, priority, acceptance or only_owner_knows.
    var whyOwner: String?
    /// 2 to 4, the recommended one first.
    var options: [Option]?
    var costOfWaiting: String?
    /// Where the task came from and where it stands, as one line the hub wrote in the request's language.
    var background: String?
    /// When the recommendation is taken if nobody answers (RFC 3339), with `default` "taken".
    var deadline: String?
    /// "taken": the recommendation is taken at `deadline`; "never": it never is.
    var defaultAction: String?
    /// The fixed answers this hub takes for the ask; none from a hub that does not know them.
    var responses: [String]?
    /// The fixed answer already given while the decision still waits (`explain_first`).
    var response: String?
    /// A decision that can wait, held for the next briefing: nothing was pushed for it (#4930, #5803;
    /// docs/assistant/todos.md "Decisions that can wait"). Absent from a hub before it, and once the briefing brought it.
    var forBriefing: Bool?
    /// A standing rule the hub suggests on this decision (#4929, #5802): the owner gave this go-ahead for this
    /// directory three times. A hub from before sends none.
    var suggestion: RuleSuggestion?

    enum CodingKeys: String, CodingKey {
        case outcome, verdict, recommend, question, options, background, deadline, responses, response, suggestion
        case askedAt = "asked_at", whyOwner = "why_owner", costOfWaiting = "cost_of_waiting", defaultAction = "default"
        case forBriefing = "for_briefing"
    }

    init(outcome: String? = nil, verdict: String? = nil, recommend: String? = nil, askedAt: String,
         question: String? = nil, whyOwner: String? = nil, options: [Option]? = nil, costOfWaiting: String? = nil,
         background: String? = nil, deadline: String? = nil, defaultAction: String? = nil, responses: [String]? = nil,
         response: String? = nil, forBriefing: Bool? = nil, suggestion: RuleSuggestion? = nil) {
        self.outcome = outcome
        self.verdict = verdict
        self.recommend = recommend
        self.askedAt = askedAt
        self.question = question
        self.whyOwner = whyOwner
        self.options = options
        self.costOfWaiting = costOfWaiting
        self.background = background
        self.deadline = deadline
        self.defaultAction = defaultAction
        self.responses = responses
        self.response = response
        self.forBriefing = forBriefing
        self.suggestion = suggestion
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        // As before #3804: an ask without its round, or with one of these in another shape, is no ask.
        outcome = try c.decodeIfPresent(String.self, forKey: .outcome)
        verdict = try c.decodeIfPresent(String.self, forKey: .verdict)
        recommend = try c.decodeIfPresent(String.self, forKey: .recommend)
        askedAt = try c.decode(String.self, forKey: .askedAt)
        question = try? c.decodeIfPresent(String.self, forKey: .question)
        whyOwner = try? c.decodeIfPresent(String.self, forKey: .whyOwner)
        options = try? c.decodeIfPresent([Option].self, forKey: .options)
        costOfWaiting = try? c.decodeIfPresent(String.self, forKey: .costOfWaiting)
        background = try? c.decodeIfPresent(String.self, forKey: .background)
        deadline = try? c.decodeIfPresent(String.self, forKey: .deadline)
        defaultAction = try? c.decodeIfPresent(String.self, forKey: .defaultAction)
        responses = try? c.decodeIfPresent([String].self, forKey: .responses)
        response = try? c.decodeIfPresent(String.self, forKey: .response)
        forBriefing = (try? c.decodeIfPresent(Bool.self, forKey: .forBriefing)) ?? nil
        suggestion = (try? c.decodeIfPresent(RuleSuggestion.self, forKey: .suggestion)) ?? nil
    }

    /// Held for the next briefing and not pushed.
    var isForBriefing: Bool { forBriefing == true }

    func row(for todo: TodoItem) -> BriefBody.Row {
        let waited = JSON.parseDate(askedAt).map { Int(max(0, Date.now.timeIntervalSince($0))) } ?? 0
        return BriefBody.Row(id: todo.id, title: todo.title, waitedS: waited, askedAt: askedAt,
                             verdict: verdict, outcome: outcome, recommend: recommend,
                             forBriefing: isForBriefing, suggestion: suggestion)
    }
}

nonisolated extension TodoAsk {
    /// The options of a decision that can be answered on its row (the web's `askOptions`): a decision with at least
    /// two of them, each with a label. None for any other ask, which keeps its one button.
    var decisionOptions: [Option] {
        guard outcome == "decide", let options else { return [] }
        let list = options.filter { !$0.label.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
        return list.count >= 2 ? list : []
    }

    /// The fixed answers this hub takes, in the menu's order; none from a hub that does not know them.
    var fixedResponses: [Response] {
        guard let responses else { return [] }
        return Response.allCases.filter { responses.contains($0.rawValue) }
    }

    /// A step that cannot be undone, or that other people will see: choosing an option takes two taps.
    var needsConfirmation: Bool { whyOwner == "irreversible" || whyOwner == "visible_to_others" }

    /// What is to be decided: the question, else the verdict line of an older hub.
    var questionLine: String { [question, verdict].compactMap { $0 }.first { !$0.isEmpty } ?? "" }

    /// What happens when nobody answers: the recommendation is taken at the deadline, or never; nil when the hub said
    /// neither (or named no time).
    func defaultLine(now: Date = .now, calendar: Calendar = .current) -> String? {
        if defaultAction == "never" { return String(localized: "Never taken by default") }
        guard defaultAction == "taken", let deadline, let at = JSON.parseDate(deadline) else { return nil }
        let time = TodoAsk.clock(at, now: now, calendar: calendar)
        return String(localized: "Taken by default at \(time)")
    }

    /// "Taken by default at 17:30 · If it waits: …": what happens when nobody answers, and what waiting costs; nil when
    /// the hub said neither. A row asks `DecisionWaitingLines`, which keeps it per round, never in `body`.
    func waitingLine(now: Date = .now, calendar: Calendar = .current) -> String? {
        let fallback = defaultLine(now: now, calendar: calendar)
        let cost = costOfWaiting.flatMap { $0.isEmpty ? nil : String(localized: "If it waits: \($0)") }
        switch (fallback, cost) {
        case (let fallback?, let cost?): return String(localized: "\(fallback) · \(cost)")
        case (let line?, nil), (nil, let line?): return line
        case (nil, nil): return nil
        }
    }

    /// "17:30" today, "Nov 12, 17:30" on another day: a fixed 24-hour time (a localized hour style reads 17:00 as 05:00
    /// in Chinese, docs/lessons/ios.md "Swift and compilation"), from the calendar's own hour and minute, so no
    /// formatter is made for it (docs/apple/performance.md P4).
    static func clock(_ date: Date, now: Date = .now, calendar: Calendar = .current) -> String {
        let parts = calendar.dateComponents([.hour, .minute], from: date)
        let time = String(format: "%02d:%02d", parts.hour ?? 0, parts.minute ?? 0)
        if calendar.isDate(date, inSameDayAs: now) { return time }
        var style = Date.FormatStyle.dateTime.month(.abbreviated).day()
        style.timeZone = calendar.timeZone
        style.calendar = calendar
        let day = date.formatted(style)
        return String(localized: "\(day), \(time)")
    }
}
