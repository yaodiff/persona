import Foundation

/// The `brief` item of a morning or evening routine's conversation (docs/protocol/protocol.md "Tasks and the chat feed", #2320):
/// the fixed part of a run as the hub worked it out when the run started, the same rows as its Lark card. Hubs since the
/// item (2026-11-07) send different shapes, so every section and field may be missing, a row that can't be read is left
/// out on its own, and values that may grow (`outcome`, `stage`) stay strings.
nonisolated struct BriefBody: Decodable, Hashable, Sendable {
    /// A row of what waits: a card waiting for an answer, a to-do to verify (`accept`) or a question that can wait (`later`).
    nonisolated struct Row: Decodable, Hashable, Sendable {
        /// The card, the to-do or the question: what the buttons send.
        var id: String
        var title: String
        /// accept: what changed, in the words of its "Please verify" card.
        var detail: String?
        /// Who asked, in the hub's words.
        var from: String?
        var waitedS: Int
        /// waiting: the bot nudged under its Lark card.
        var nudged: Bool
        /// accept: the round's ask, sent back exactly as it came (`POST /api/v1/todos/{id}/accept`).
        var askedAt: String?
        /// accept: the verdict line as its Lark card says it; `outcome` a conclusion's verdict (done, cannot, decide or a
        /// newer one), `recommend` a decision's recommended option. A hub from before #1226 sends none of the three.
        var verdict: String?
        var outcome: String?
        var recommend: String?
        /// waiting, later: the chat it came from (#2320). A hub from before leaves them out.
        var machineID: String?
        var sessionID: String?
        /// A to-do's own decision (`TodoAsk.row`), never a briefing's row: it is held for the next briefing and nothing
        /// was pushed for it (#5803), and the standing rule the hub suggests on it (#5802).
        var forBriefing = false
        var suggestion: RuleSuggestion?

        enum CodingKeys: String, CodingKey {
            case id, title, detail, from, nudged, verdict, outcome, recommend
            case waitedS = "waited_s", askedAt = "asked_at", machineID = "machine_id", sessionID = "session_id"
        }

        init(id: String, title: String, detail: String? = nil, from: String? = nil, waitedS: Int = 0, nudged: Bool = false,
             askedAt: String? = nil, verdict: String? = nil, outcome: String? = nil, recommend: String? = nil,
             machineID: String? = nil, sessionID: String? = nil, forBriefing: Bool = false, suggestion: RuleSuggestion? = nil) {
            self.id = id
            self.title = title
            self.detail = detail
            self.from = from
            self.waitedS = waitedS
            self.nudged = nudged
            self.askedAt = askedAt
            self.verdict = verdict
            self.outcome = outcome
            self.recommend = recommend
            self.machineID = machineID
            self.sessionID = sessionID
            self.forBriefing = forBriefing
            self.suggestion = suggestion
        }

        /// Only the id is needed: without it no button can say which row it means.
        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            title = c.text(.title) ?? ""
            detail = c.text(.detail)
            from = c.text(.from)
            waitedS = c.whole(.waitedS) ?? 0
            nudged = (try? c.decodeIfPresent(Bool.self, forKey: .nudged)) == true
            askedAt = c.text(.askedAt)
            verdict = c.text(.verdict)
            outcome = c.text(.outcome)
            recommend = c.text(.recommend)
            machineID = c.text(.machineID)
            sessionID = c.text(.sessionID)
        }
    }

    /// A to-do closed in the window.
    nonisolated struct Done: Decodable, Hashable, Sendable {
        var id: String
        var title: String
        var at: Date?

        enum CodingKeys: String, CodingKey { case id, title, at }

        init(id: String, title: String, at: Date? = nil) {
            self.id = id
            self.title = title
            self.at = at
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            title = c.text(.title) ?? ""
            at = c.text(.at).flatMap(JSON.parseDate)
        }
    }

    /// A to-do being worked on; `stage` is the hub's word (plan … verify), kept even when this build doesn't know it.
    nonisolated struct Progress: Decodable, Hashable, Sendable {
        var id: String
        var title: String
        var stage: String?

        enum CodingKeys: String, CodingKey { case id, title, stage }

        init(id: String, title: String, stage: String? = nil) {
            self.id = id
            self.title = title
            self.stage = stage
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            title = c.text(.title) ?? ""
            stage = c.text(.stage)
        }
    }

    /// One line of the week's recommended setup (#5085, #5804; docs/assistant/assistant.md "Recommended setup"): a
    /// mechanism that is off and how many of the last 7 days' asks or background rounds it would have removed. The hub
    /// counted and worded `text`; the app draws it as it came and turns nothing on.
    nonisolated struct Setup: Decodable, Hashable, Sendable, Identifiable {
        /// Where a line's mechanism is turned on in this app, for those with a page here.
        enum Page: Hashable, Sendable {
            /// Settings › Session Triage: its three switches.
            case triage
            /// Model Settings › the background supervisor's use.
            case superviseModel
            /// Assistant Profile › Standing Authorizations (#7002).
            case standingAuthorizations
        }

        /// The mechanism (`delegate`, `task_trust`, `triage_wake`, … or a newer one).
        var id: String
        var text: String
        var count: Int

        enum CodingKeys: String, CodingKey { case id, text, count }

        init(id: String, text: String, count: Int = 0) {
            self.id = id
            self.text = text
            self.count = count
        }

        /// A line needs its id and its words; without either it is left out (`Lossy`, `BriefBody.listed`).
        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decode(String.self, forKey: .id)
            text = try c.decode(String.self, forKey: .text)
            count = c.whole(.count) ?? 0
        }

        /// The page Open Settings goes to; nil for a mechanism that is not turned on at a page of this app (leader
        /// conversations, the assistant's configuration file; a remembered kind of task is turned on with a plan card's
        /// own checkbox, as its line says) and for one this build doesn't know.
        var page: Page? {
            switch id {
            case "triage_decide", "triage_auto_continue", "triage_wake": .triage
            case "supervisor_tier": .superviseModel
            case "standing_authorizations": .standingAuthorizations
            default: nil
            }
        }
    }

    /// The routine's name.
    var title: String
    var hours: Int
    /// The cards waiting were not read yet (the hub had just started): `waiting` is empty for want of knowing.
    var waitingUnknown: Bool
    var waiting: [Row]
    /// What didn't go through in the window, newest first (the Today card's rows).
    var failed: [TodayBody.Failed]
    var failedTotal: Int
    var done: [Done]
    var doneTotal: Int
    var accept: [Row]
    /// Questions Agents left for a briefing, the oldest first; a hub from before leaves them out.
    var later: [Row]
    var laterTotal: Int
    var progress: [Progress]
    var progressTotal: Int
    /// The recommended setup, on the first briefing of a week that names something; a hub from before leaves it out.
    var setup: [Setup]

    enum CodingKeys: String, CodingKey {
        case title, hours, waiting, failed, done, accept, later, progress, setup
        case waitingUnknown = "waiting_unknown", failedTotal = "failed_total", doneTotal = "done_total",
             laterTotal = "later_total", progressTotal = "progress_total"
    }

    init(title: String = "", hours: Int = 12, waitingUnknown: Bool = false, waiting: [Row] = [], failed: [TodayBody.Failed] = [],
         failedTotal: Int? = nil, done: [Done] = [], doneTotal: Int? = nil, accept: [Row] = [], later: [Row] = [],
         laterTotal: Int? = nil, progress: [Progress] = [], progressTotal: Int? = nil, setup: [Setup] = []) {
        self.title = title
        self.hours = hours
        self.waitingUnknown = waitingUnknown
        self.waiting = waiting
        self.failed = failed
        self.failedTotal = max(failedTotal ?? 0, failed.count)
        self.done = done
        self.doneTotal = max(doneTotal ?? 0, done.count)
        self.accept = accept
        self.later = later
        self.laterTotal = max(laterTotal ?? 0, later.count)
        self.progress = progress
        self.progressTotal = max(progressTotal ?? 0, progress.count)
        self.setup = Self.listed(setup)
    }

    /// The setup lines to draw: those with words, each mechanism once (a row's identity in the card).
    static func listed(_ setup: [Setup]) -> [Setup] {
        var seen = Set<String>()
        return setup.filter { line in
            let worded = !line.id.isEmpty && !line.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            return worded && seen.insert(line.id).inserted
        }
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        self.init(title: c.text(.title) ?? "", hours: c.whole(.hours) ?? 12,
                  waitingUnknown: (try? c.decodeIfPresent(Bool.self, forKey: .waitingUnknown)) == true,
                  waiting: c.rows(.waiting), failed: c.rows(.failed), failedTotal: c.whole(.failedTotal),
                  done: c.rows(.done), doneTotal: c.whole(.doneTotal), accept: c.rows(.accept),
                  later: c.rows(.later), laterTotal: c.whole(.laterTotal),
                  progress: c.rows(.progress), progressTotal: c.whole(.progressTotal), setup: c.rows(.setup))
    }

    /// Done and failed past the rows the hub listed (it cuts both lists, the totals count all).
    var pastMore: Int { doneTotal + failedTotal - done.count - failed.count }
    var laterMore: Int { laterTotal - later.count }
    var progressMore: Int { progressTotal - progress.count }

    /// The chat a waiting card or a question that can wait opens: the hub's `machine_id` / `session_id`, else (a waiting
    /// card from a hub before them) the card among those the app still has pending. Nil: the row has no link.
    static func chat(for row: Row, waiting: Bool, group: String, pending: [Interaction]) -> SessionKey? {
        if let machine = row.machineID, !machine.isEmpty, let session = row.sessionID, !session.isEmpty {
            return SessionKey(group: group, machineID: machine, sessionID: session)
        }
        guard waiting, let card = pending.first(where: { $0.id == row.id && $0.group == group }), !card.sessionID.isEmpty else { return nil }
        return card.sessionKey
    }
}

nonisolated extension BriefBody.Row {
    /// What the button of a row to verify does, as on the web: Accept a deployed to-do, Got It a conclusion (done, cannot, or
    /// a verdict this build doesn't know), Take Recommendation a decision. Nil: a decision with nothing recommended, which
    /// the user answers on its card or in the chat.
    enum Take: Hashable, Sendable { case accept, gotIt, recommendation }

    var isDecision: Bool { outcome?.trimmingCharacters(in: .whitespacesAndNewlines) == "decide" }

    var take: Take? {
        let outcome = outcome?.trimmingCharacters(in: .whitespaces) ?? ""
        if isDecision { return recommend?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false ? .recommendation : nil }
        return outcome.isEmpty ? .accept : .gotIt
    }
}

nonisolated extension FeedItem {
    /// `brief`: a routine's facts card; nil when the body can't be read (that card is left out, never the feed around it).
    var brief: BriefBody? {
        guard kind == .brief, let body, let data = try? JSON.encoder.encode(body) else { return nil }
        return try? JSON.decoder.decode(BriefBody.self, from: data)
    }
}

private extension KeyedDecodingContainer {
    /// A string, nil when it is missing or not a string.
    nonisolated func text(_ key: Key) -> String? { (try? decodeIfPresent(String.self, forKey: key)) ?? nil }

    /// A whole number sent as any JSON number, nil when it is missing or not one.
    nonisolated func whole(_ key: Key) -> Int? {
        guard let n = (try? decodeIfPresent(Double.self, forKey: key)) ?? nil, n.isFinite, n.magnitude < 1e12 else { return nil }
        return Int(n.rounded())
    }

    /// A list without the rows that can't be read (`Lossy`); empty when it is missing or not a list.
    nonisolated func rows<T: Decodable & Sendable>(_ key: Key) -> [T] {
        ((try? decodeIfPresent([Lossy<T>].self, forKey: key)) ?? nil)?.compactMap(\.value) ?? []
    }
}
