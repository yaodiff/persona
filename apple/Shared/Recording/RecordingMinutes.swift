import Foundation

/// The hub's minutes of a meeting recording (docs/assistant/meeting-minutes.md「Field contract」, #2710): when and where, who
/// was there, the points discussed, the decisions, the action items (who, what, by when) and the open questions. Each item
/// carries `at`, seconds from the recording's start (the transcript's clock), so a tap lands on that line. Decoded from
/// `GET /api/v1/kb/recordings/{id}`'s `detail.minutes`; sections the hub leaves out are empty here.
nonisolated struct RecordingMinutes: Codable, Hashable, Sendable {
    /// A point, decision, action item or open question. An action item's text is its `what`.
    nonisolated struct Item: Codable, Hashable, Sendable {
        var text: String
        /// Seconds into the recording where it was said.
        var at: Double?
        /// Who said it (a point) or does it (an action item), only when the transcript or the calendar says so.
        var who: String?
        /// An action item's deadline, as said ("Friday").
        var by: String?
        /// An action item's name within one `version` of the minutes ("a1"): what confirming it as a to-do sends.
        var id: String?
        /// An action item's deadline as a date (`YYYY-MM-DD`), when the one said resolves to a single day.
        var dueDate: String?
        /// The to-do an action item became once the user confirmed it (#4062); absent or empty while it is a suggestion.
        var todoID: String?
        /// The meeting photo it cites instead of a transcript line (#3862 phase 3): its number at the hub (`n`); `at` is
        /// then the photo's. The page shows that photo with the item, or Photo removed once the hub no longer lists it.
        var photo: Int?

        init(text: String, at: Double? = nil, who: String? = nil, by: String? = nil, id: String? = nil, dueDate: String? = nil,
             todoID: String? = nil, photo: Int? = nil) {
            self.text = text; self.at = at; self.who = who; self.by = by; self.id = id; self.dueDate = dueDate; self.todoID = todoID
            self.photo = photo
        }

        /// An action item already confirmed as a to-do.
        var added: Bool { todoID?.isEmpty == false }

        enum CodingKeys: String, CodingKey {
            case text, what, at, who, by, id, photo
            case dueDate = "due_date", todoID = "todo_id"
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            text = try c.decodeIfPresent(String.self, forKey: .text) ?? c.decodeIfPresent(String.self, forKey: .what) ?? ""
            at = try? c.decodeIfPresent(Double.self, forKey: .at)
            who = try c.decodeIfPresent(String.self, forKey: .who)
            by = try c.decodeIfPresent(String.self, forKey: .by)
            id = try c.decodeIfPresent(String.self, forKey: .id)
            dueDate = try c.decodeIfPresent(String.self, forKey: .dueDate)
            todoID = try c.decodeIfPresent(String.self, forKey: .todoID)
            // A number from 1; anything else reads as no citation rather than losing the minutes.
            photo = (try? c.decodeIfPresent(Int.self, forKey: .photo)).flatMap { $0 }.flatMap { $0 > 0 ? $0 : nil }
        }

        func encode(to encoder: Encoder) throws {
            var c = encoder.container(keyedBy: CodingKeys.self)
            try c.encode(text, forKey: .text)
            try c.encodeIfPresent(at, forKey: .at)
            try c.encodeIfPresent(who, forKey: .who)
            try c.encodeIfPresent(by, forKey: .by)
            try c.encodeIfPresent(id, forKey: .id)
            try c.encodeIfPresent(dueDate, forKey: .dueDate)
            try c.encodeIfPresent(todoID, forKey: .todoID)
            try c.encodeIfPresent(photo, forKey: .photo)
        }
    }

    /// Goes up each time the hub writes them again; sent back when confirming action items as to-dos (#4062).
    var version: Int?
    var title: String?
    /// "2026-11-10 14:00–15:10", as the hub writes it: shown only when `start` / `end` are missing (a hub from before them).
    var when: String?
    /// The recording's span (#4713): what the app formats in its own date style (`whenLine`).
    var start: Date?
    var end: Date?
    var place: String?
    var attendees: [String] = []
    var points: [Item] = []
    var decisions: [Item] = []
    var actions: [Item] = []
    var openQuestions: [Item] = []
    /// The minutes as one summary card (#6563, docs/assistant/meeting-minutes.md "Summary card (#6563)"): the headline, the
    /// core conclusions, and the points and decisions grouped by topic. Read on its own and cleaned as the contract's
    /// "Clients" says (`Overview.clean`); nil when there is none or nothing usable is left, and then the page is as before.
    var overview: Overview?
    /// The hub's own Markdown of these minutes: what Copy and Share send in the summary card's layout (`shareText`).
    var markdown: String?

    init(title: String? = nil, when: String? = nil, start: Date? = nil, end: Date? = nil, place: String? = nil, attendees: [String] = [],
         points: [Item] = [], decisions: [Item] = [], actions: [Item] = [], openQuestions: [Item] = [], overview: Overview? = nil,
         markdown: String? = nil) {
        self.title = title; self.when = when; self.start = start; self.end = end; self.place = place; self.attendees = attendees
        self.points = points; self.decisions = decisions; self.actions = actions; self.openQuestions = openQuestions
        self.overview = overview; self.markdown = markdown
    }

    enum CodingKeys: String, CodingKey {
        case version, title, when, start, end, attendees, points, decisions, actions, overview, markdown
        case place = "where", openQuestions = "open"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        version = try? c.decodeIfPresent(Int.self, forKey: .version)
        title = try c.decodeIfPresent(String.self, forKey: .title)
        when = try c.decodeIfPresent(String.self, forKey: .when)
        // RFC 3339 with the user's offset, whole seconds ("2026-11-10T14:00:00+08:00"); unreadable reads as absent.
        start = (try? c.decodeIfPresent(String.self, forKey: .start)).flatMap { $0 }.flatMap(JSON.parseDate)
        end = (try? c.decodeIfPresent(String.self, forKey: .end)).flatMap { $0 }.flatMap(JSON.parseDate)
        place = try c.decodeIfPresent(String.self, forKey: .place)
        attendees = (try? c.decodeIfPresent([String].self, forKey: .attendees)) ?? []
        points = try c.decodeIfPresent([Item].self, forKey: .points) ?? []
        decisions = try c.decodeIfPresent([Item].self, forKey: .decisions) ?? []
        actions = try c.decodeIfPresent([Item].self, forKey: .actions) ?? []
        openQuestions = try c.decodeIfPresent([Item].self, forKey: .openQuestions) ?? []
        // Apart from the fields above: an overview of any shape, however broken, never costs the minutes their lists.
        overview = Overview.clean(try? c.decodeIfPresent(JSONValue.self, forKey: .overview))
        markdown = (try? c.decodeIfPresent(String.self, forKey: .markdown)).flatMap { $0 }
    }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encodeIfPresent(version, forKey: .version)
        try c.encodeIfPresent(title, forKey: .title)
        try c.encodeIfPresent(when, forKey: .when)
        try c.encodeIfPresent(start?.formatted(.iso8601), forKey: .start)
        try c.encodeIfPresent(end?.formatted(.iso8601), forKey: .end)
        try c.encodeIfPresent(place, forKey: .place)
        try c.encode(attendees, forKey: .attendees)
        try c.encode(points, forKey: .points)
        try c.encode(decisions, forKey: .decisions)
        try c.encode(actions, forKey: .actions)
        try c.encode(openQuestions, forKey: .openQuestions)
        try c.encodeIfPresent(overview, forKey: .overview)
        try c.encodeIfPresent(markdown, forKey: .markdown)
    }

    var isEmpty: Bool { points.isEmpty && decisions.isEmpty && actions.isEmpty && openQuestions.isEmpty }

    /// When the meeting was, as the recording page's header writes dates (#4875); the hub's `when` from a hub without
    /// `start` / `end`.
    var whenLine: String? {
        if let start, let end { return RecordingFormat.span(start, end) }
        return when?.isEmpty == false ? when : nil
    }

    /// The transcript line said at `seconds`: the last one that started by then (a second's grace: a point is often
    /// stamped as its sentence begins), else the first.
    static func sentenceIndex(at seconds: Double, in sentences: [RecordingSentence]) -> Int? {
        guard !sentences.isEmpty, seconds.isFinite else { return nil }
        return sentences.lastIndex { $0.start <= seconds + 1 } ?? 0
    }

    /// An item's line under its text: who, by when (as said, and the date it resolves to).
    static func byline(_ item: Item) -> String? {
        let due = item.dueDate.flatMap { $0.isEmpty ? nil : String(localized: "Due \($0)") }
        let parts = [item.who, item.by, due].compactMap { $0?.isEmpty == false ? $0 : nil }
        return parts.isEmpty ? nil : parts.joined(separator: " · ")
    }

    /// How Copy and Share name a cited photo after its item, as the hub's own `markdown` does ("(photo 3)", in the app's language).
    static func photoMention(_ n: Int) -> String { String(localized: "(photo \(n))") }

    /// What Copy and Share send: Markdown, headed by the title (the recording's name without one), then when and where.
    func text(title fallback: String) -> String {
        var out = ["# " + ((title?.isEmpty == false ? title : nil) ?? fallback)]
        let line = [whenLine, place].compactMap { $0?.isEmpty == false ? $0 : nil }.joined(separator: " · ")
        if !line.isEmpty { out.append(line) }
        if !attendees.isEmpty { out.append(String(localized: "Attendees: \(ListFormatter.localizedString(byJoining: attendees))")) }
        func section(_ heading: String, _ items: [Item]) {
            guard !items.isEmpty else { return }
            out += ["", "## " + heading]
            for item in items {
                var row = "- " + item.text
                if let byline = Self.byline(item) { row += " (" + byline + ")" }
                if let at = item.at { row += " [" + RecordingFormat.clock(at) + "]" }
                if let photo = item.photo { row += " " + Self.photoMention(photo) }
                out.append(row)
            }
        }
        section(String(localized: "Key Points"), points)
        section(String(localized: "Decisions"), decisions)
        section(String(localized: "Action Items"), actions)
        section(String(localized: "Open Questions"), openQuestions)
        return out.joined(separator: "\n")
    }

    /// What Copy and Share send: with the summary card, the hub's own Markdown, which follows its layout (#6563); without
    /// it, or from a hub that sends none, the phone's own text as before (`text(title:)`).
    func shareText(title fallback: String) -> String {
        if overview != nil, let markdown, !markdown.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty { return markdown }
        return text(title: fallback)
    }
}

// MARK: The summary card (#6563)

nonisolated extension RecordingMinutes {
    /// A topic's icon as the hub names it: one of a closed list, so the model never names a symbol. A value this app does
    /// not know is `general`, so the list can grow (docs/assistant/meeting-minutes.md "The `overview` field").
    nonisolated enum TopicIcon: String, CaseIterable, Codable, Hashable, Sendable {
        case progress, schedule, release, performance, metrics, tooling, quality, design, people, customer, budget, risk, general
    }

    /// `detail.minutes.overview`, cleaned: every group has a topic, every topic a bullet. Its bullets are the minutes'
    /// points and decisions, word for word; actions and open questions are never in it.
    nonisolated struct Overview: Codable, Hashable, Sendable {
        /// A conclusion or a topic's bullet: its words, the places in them set in bold (`strong`), its time and the photo
        /// it cites; a bullet also who said it (a point) or that it is a decision.
        nonisolated struct Line: Codable, Hashable, Sendable {
            var text: String
            var strong: [String] = []
            var at: Double?
            var photo: Int?
            var who: String?
            var decision = false

            enum CodingKeys: String, CodingKey { case text, strong, at, photo, who, decision }

            init(text: String, strong: [String] = [], at: Double? = nil, photo: Int? = nil, who: String? = nil, decision: Bool = false) {
                self.text = text; self.strong = strong; self.at = at; self.photo = photo; self.who = who; self.decision = decision
            }

            init(from decoder: Decoder) throws {
                guard let line = Self.clean(try JSONValue(from: decoder), bullet: true) else {
                    throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: "no words"))
                }
                self = line
            }

            func encode(to encoder: Encoder) throws {
                var c = encoder.container(keyedBy: CodingKeys.self)
                try c.encode(text, forKey: .text)
                if !strong.isEmpty { try c.encode(strong, forKey: .strong) }
                try c.encodeIfPresent(at, forKey: .at)
                try c.encodeIfPresent(photo, forKey: .photo)
                try c.encodeIfPresent(who, forKey: .who)
                if decision { try c.encode(true, forKey: .decision) }
            }

            /// A bullet (`bullet`) or a conclusion as the contract's "Clients" reads it: dropped without words; a wrong
            /// `strong`, `who` or `at` counts as absent, `strong`'s wrong entries are dropped, a `photo` that is not a
            /// positive whole number is none, and only `true` marks a decision (a bullet's; a conclusion has neither).
            static func clean(_ value: JSONValue, bullet: Bool) -> Line? {
                guard case .object(let o) = value, let text = Overview.words(o["text"]) else { return nil }
                var line = Line(text: text)
                if case .array(let entries)? = o["strong"] {
                    line.strong = entries.compactMap { if case .string(let s) = $0, !s.isEmpty { s } else { nil } }
                }
                if case .number(let n)? = o["at"], n.isFinite, n >= 0 { line.at = n }
                if case .number(let n)? = o["photo"], n >= 1, n.rounded() == n, let whole = Int(exactly: n) { line.photo = whole }
                if bullet {
                    line.who = Overview.words(o["who"])
                    line.decision = o["decision"] == .bool(true)
                }
                return line
            }
        }

        nonisolated struct Topic: Codable, Hashable, Sendable {
            var title: String
            var icon: TopicIcon = .general
            var bullets: [Line]
        }

        nonisolated struct TopicGroup: Codable, Hashable, Sendable {
            /// "" for the one group without a title: no heading shows for it.
            var title: String
            var topics: [Topic]
        }

        /// One line saying what the meeting was; nil without one (the card then heads with the minutes' title).
        var headline: String?
        /// Where the meeting landed, the most important first: at most four.
        var conclusions: [Line] = []
        var groups: [TopicGroup]

        init(headline: String? = nil, conclusions: [Line] = [], groups: [TopicGroup]) {
            self.headline = headline; self.conclusions = conclusions; self.groups = groups
        }

        init(from decoder: Decoder) throws {
            guard let overview = Self.clean(try JSONValue(from: decoder)) else {
                throw DecodingError.dataCorrupted(.init(codingPath: decoder.codingPath, debugDescription: "no bullet"))
            }
            self = overview
        }

        /// The contract's cleaning ("Clients"): no overview for one that is not an object or whose `groups` is not an
        /// array; members that are not objects go; a group without an array of `topics`, a topic without an array of
        /// `bullets` or a title with words, and a bullet or conclusion without words go; a group's title that is not a
        /// string is ""; an unknown icon is `general`; then empty topics and groups go. Nil when no bullet is left.
        static func clean(_ value: JSONValue?) -> Overview? {
            guard case .object(let o)? = value, case .array(let groups)? = o["groups"] else { return nil }
            let kept: [TopicGroup] = groups.compactMap { value in
                guard case .object(let g) = value, case .array(let topics)? = g["topics"] else { return nil }
                let title: String = if case .string(let s)? = g["title"] { s } else { "" }
                let kept: [Topic] = topics.compactMap { value in
                    guard case .object(let t) = value, case .array(let bullets)? = t["bullets"], let title = words(t["title"]) else { return nil }
                    let icon: TopicIcon = if case .string(let s)? = t["icon"] { TopicIcon(rawValue: s) ?? .general } else { .general }
                    let lines = bullets.compactMap { Line.clean($0, bullet: true) }
                    return lines.isEmpty ? nil : Topic(title: title, icon: icon, bullets: lines)
                }
                return kept.isEmpty ? nil : TopicGroup(title: title, topics: kept)
            }
            guard !kept.isEmpty else { return nil }
            let conclusions: [Line] = if case .array(let a)? = o["conclusions"] { a.compactMap { Line.clean($0, bullet: false) } } else { [] }
            return Overview(headline: words(o["headline"]), conclusions: conclusions, groups: kept)
        }

        /// A string with something besides spaces in it, as it was written; anything else is none.
        static func words(_ value: JSONValue?) -> String? {
            guard case .string(let s)? = value, !s.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return nil }
            return s
        }
    }

    /// A stretch of an overview's text, set in bold or not.
    nonisolated struct BoldPart: Hashable, Sendable {
        var text: String
        var bold: Bool
    }

    /// `text` cut where `strong` bolds it (docs/assistant/meeting-minutes.md "Bold (`strong`)"): from the start, each
    /// entry's first exact occurrence at or after the end of the one before (UTF-16 code units, no case folding or
    /// normalization, as every client and the Markdown find them); an entry not found bolds nothing and the search goes
    /// on from where it was. The text is never read as Markdown: words from a transcript cannot become links.
    static func boldParts(_ text: String, strong: [String]) -> [BoldPart] {
        let units = Array(text.utf16)
        var marks: [Range<Int>] = []
        var cursor = 0
        for entry in strong {
            let wanted = Array(entry.utf16)
            guard !wanted.isEmpty, wanted.count <= units.count - cursor else { continue }
            guard let at = (cursor...(units.count - wanted.count)).first(where: { units[$0..<$0 + wanted.count].elementsEqual(wanted) })
            else { continue }
            marks.append(at..<at + wanted.count)
            cursor = at + wanted.count
        }
        func piece(_ r: Range<Int>) -> String { String(decoding: units[r], as: UTF16.self) }
        var parts: [BoldPart] = []
        var from = 0
        for mark in marks {
            if mark.lowerBound > from { parts.append(BoldPart(text: piece(from..<mark.lowerBound), bold: false)) }
            parts.append(BoldPart(text: piece(mark), bold: true))
            from = mark.upperBound
        }
        if from < units.count || parts.isEmpty { parts.append(BoldPart(text: piece(from..<units.count), bold: false)) }
        return parts
    }

    /// The same as styled text: the bold places strongly emphasized, which `Text` draws in its font's bold weight.
    static func bolded(_ text: String, strong: [String]) -> AttributedString {
        boldParts(text, strong: strong).reduce(into: AttributedString()) { out, part in
            var run = AttributedString(part.text)
            if part.bold { run.inlinePresentationIntent = .stronglyEmphasized }
            out.append(run)
        }
    }
}

/// The Summary of a recording's minutes on iPhone (#5026, docs/apple/recording.md「Minutes」): which view the page shows,
/// which cards the Summary has and in what order, its header's lines, and the transcript lines each item opens to. Worked
/// out once per change of the minutes or the transcript, so the page's `body` only arranges them
/// (docs/apple/performance.md, P4). Pure, so it is tested without a page.
nonisolated enum RecordingMinutesCards {
    /// The recording page's switch: Summary | Transcript.
    nonisolated enum Pane: Hashable, Sendable {
        case summary, transcript
    }

    /// What the recording's page shows: nothing to switch (nil) until the minutes are ready, then what was picked on the
    /// page, else the Summary. A meeting's page is opened for what was decided; the transcript is one tap away.
    static func pane(picked: Pane?, ready: Bool) -> Pane? {
        ready ? picked ?? .summary : nil
    }

    /// A card's group, in the order the Summary shows them: what was discussed and decided, then what is still open, and
    /// last who does what, so the page ends on what happens next.
    nonisolated enum Kind: String, CaseIterable, Hashable, Sendable {
        case points, decisions, open, actions

        var title: String {
            switch self {
            case .points: String(localized: "Key Points")
            case .decisions: String(localized: "Decisions")
            case .open: String(localized: "Open Questions")
            case .actions: String(localized: "Action Items")
            }
        }

        var symbol: String {
            switch self {
            case .points: Theme.Icon.keyPoint
            case .decisions: Theme.Icon.decision
            case .open: Theme.Icon.openQuestion
            case .actions: Theme.Icon.todo
            }
        }

        func items(_ minutes: RecordingMinutes) -> [RecordingMinutes.Item] {
            switch self {
            case .points: minutes.points
            case .decisions: minutes.decisions
            case .open: minutes.openQuestions
            case .actions: minutes.actions
            }
        }
    }

    /// A transcript line under an opened item; `said` is the one its time picks (`RecordingMinutes.sentenceIndex`).
    nonisolated struct Line: Identifiable, Hashable, Sendable {
        /// Its place in the transcript.
        var id: Int
        var start: Double
        var text: String
        var said: Bool
    }

    nonisolated struct Item: Identifiable, Hashable, Sendable {
        /// Its group and its place in the hub's list: the hub's items carry no ids, and rewritten minutes replace them all.
        var id: String
        var text: String
        var at: Double?
        /// Who and by when, as the minutes say them (an action item; a point's speaker).
        var byline: String?
        /// The transcript lines around `at`: empty without a time or a transcript, and then the item opens to nothing.
        var context: [Line]
        /// The meeting photo it cites (#3862 phase 3): its number at the hub; the card shows its thumbnail, or Photo removed.
        var photo: Int? = nil
        /// An action item's name in this version of the minutes, when the hub gives one: what Add to To-Dos sends (#4062).
        var action: String? = nil
        /// An action item already a to-do (Added).
        var added = false
        /// The to-do it became (`todo_id`): in the summary card's layout its done circle shows once the app holds it (#6563).
        var todo: String? = nil
        /// In the summary card (#6563): its words with the bold places (`RecordingMinutes.bolded`), and whether it is a
        /// decision, which shows the decision mark instead of the dot and the word Decision where a point has its speaker.
        var words: AttributedString? = nil
        var decision = false
    }

    nonisolated struct Card: Identifiable, Hashable, Sendable {
        var kind: Kind
        var items: [Item]
        var id: Kind { kind }
    }

    nonisolated struct Summary: Hashable, Sendable {
        /// The minutes' own title (the page shows it only when it differs from the recording's).
        var title: String?
        /// When (in the app's date style, `RecordingMinutes.whenLine`) · where.
        var facts: String?
        var attendees: String?
        /// Only the groups with something in them.
        var cards: [Card]
        /// The hub numbers these minutes (`version`), so their action items can become to-dos (#4062).
        var offered = false
        /// The summary card (#6563), when the minutes carry an overview with a bullet left: the page shows it, its topic
        /// cards, then the Action Items and Open Questions of `cards`, instead of the four cards. Nil: as before.
        var overview: Overview? = nil

        /// The action items shown that can still become to-dos: what Add All sends.
        var addable: [String] {
            guard offered else { return [] }
            return cards.first { $0.kind == .actions }?.items.compactMap { $0.added ? nil : $0.action } ?? []
        }
    }

    /// What the recording page's header already shows of the meeting (`RecordingMeetingFacts`, from its calendar event),
    /// so the summary card leaves it out (#6563). The four cards' header card keeps all of it, as before.
    nonisolated struct Shown: Hashable, Sendable {
        var when = false
        var place = false
        var attendees = false

        init(when: Bool = false, place: Bool = false, attendees: Bool = false) {
            self.when = when; self.place = place; self.attendees = attendees
        }

        /// What `RecordingMeetingFacts` draws for `meeting`.
        init(_ meeting: RecordingMeeting?) {
            self.init(when: meeting?.start != nil && meeting?.end != nil, place: meeting?.location?.isEmpty == false,
                      attendees: meeting?.attendees.isEmpty == false)
        }
    }

    /// A topic's card: its title, its icon (`Theme.Icon`), its group's tint and its bullets, of which the first `shown`
    /// show until Show N More.
    nonisolated struct Topic: Identifiable, Hashable, Sendable {
        /// `topic-<group>-<topic>`, by place: rewritten minutes replace them all.
        var id: String
        var title: String
        var symbol: String
        /// Its group's place among the topic tints (`Theme.Hue.minutesGroups`).
        var hue: Int
        var bullets: [Item]
        var shown: Int
        /// How many Show N More opens.
        var folded: Int { bullets.count - shown }
    }

    /// A group of topics under its heading (none for the one untitled group), in its tint.
    nonisolated struct TopicGroup: Identifiable, Hashable, Sendable {
        /// `group-<group>`.
        var id: String
        var title: String?
        var hue: Int
        var topics: [Topic]
    }

    /// The summary card's layout (docs/assistant/meeting-minutes.md "Clients"): the headline, the facts the page's header
    /// does not show, the core conclusions, then the topic cards under their group headings.
    nonisolated struct Overview: Hashable, Sendable {
        /// The card's heading: the headline; nil without one, and then the minutes' title heads it (`Summary.title`).
        var headline: String?
        var facts: String?
        /// The facts line's icon: the calendar when it says when, the pin when it is only the place (as the page's header,
        /// `RecordingMeetingFacts`, one symbol per meaning).
        var factsSymbol = Theme.Icon.calendarEvent
        var attendees: String?
        var conclusions: [Item]
        var groups: [TopicGroup]
    }

    /// The `Theme.Icon` for a topic's icon: each meaning its own symbol, `general` for what this app does not know.
    static func symbol(_ icon: RecordingMinutes.TopicIcon) -> String {
        switch icon {
        case .progress: Theme.Icon.topicProgress
        case .schedule: Theme.Icon.topicSchedule
        case .release: Theme.Icon.topicRelease
        case .performance: Theme.Icon.topicPerformance
        case .metrics: Theme.Icon.topicMetrics
        case .tooling: Theme.Icon.topicTooling
        case .quality: Theme.Icon.topicQuality
        case .design: Theme.Icon.topicDesign
        case .people: Theme.Icon.topicPeople
        case .customer: Theme.Icon.topicCustomer
        case .budget: Theme.Icon.topicBudget
        case .risk: Theme.Icon.topicRisk
        case .general: Theme.Icon.topicGeneral
        }
    }

    /// The summary card from cleaned minutes (`RecordingMinutes.overview`), nil without one. A group's tint is its place
    /// (the first group the first of the four), never the model's choice; a decision's line under its words says Decision.
    static func overview(_ minutes: RecordingMinutes, sentences: [RecordingSentence], shown: Shown, before: Int, after: Int,
                         fold: Int = Theme.Recording.topicBulletsShown) -> Overview? {
        guard let overview = minutes.overview else { return nil }
        let tints = max(1, Theme.Recording.topicTints)
        func item(_ line: RecordingMinutes.Overview.Line, id: String, context lines: Bool) -> Item {
            let byline = line.decision ? String(localized: "Decision [Minutes Overview]") : line.who
            return Item(id: id, text: line.text, at: line.at, byline: byline,
                        context: lines ? context(at: line.at, in: sentences, before: before, after: after) : [], photo: line.photo,
                        words: RecordingMinutes.bolded(line.text, strong: line.strong), decision: line.decision)
        }
        let groups = overview.groups.enumerated().map { g, group in
            let hue = g % tints
            let topics = group.topics.enumerated().map { t, topic in
                let id = "topic-\(g)-\(t)"
                let bullets = topic.bullets.enumerated().map { b, line in item(line, id: "\(id)-\(b)", context: true) }
                return Topic(id: id, title: topic.title, symbol: symbol(topic.icon), hue: hue, bullets: bullets,
                             shown: min(bullets.count, max(1, fold)))
            }
            let title = group.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? nil : group.title
            return TopicGroup(id: "group-\(g)", title: title, hue: hue, topics: topics)
        }
        // A conclusion sums up more than one line: no transcript opens under it, only its time.
        let conclusions = overview.conclusions.enumerated().map { i, line in item(line, id: "conclusion-\(i)", context: false) }
        let when = shown.when ? nil : minutes.whenLine
        let facts = [when, shown.place ? nil : minutes.place].compactMap { $0?.isEmpty == false ? $0 : nil }.joined(separator: " · ")
        let attendees = shown.attendees || minutes.attendees.isEmpty ? nil : ListFormatter.localizedString(byJoining: minutes.attendees)
        return Overview(headline: overview.headline, facts: facts.isEmpty ? nil : facts,
                        factsSymbol: when == nil ? Theme.Icon.place : Theme.Icon.calendarEvent, attendees: attendees,
                        conclusions: conclusions, groups: groups)
    }

    static func summary(_ minutes: RecordingMinutes, sentences: [RecordingSentence], shown: Shown = Shown(),
                        before: Int = Theme.Recording.contextBefore, after: Int = Theme.Recording.contextAfter) -> Summary {
        let facts = [minutes.whenLine, minutes.place].compactMap { $0?.isEmpty == false ? $0 : nil }.joined(separator: " · ")
        let cards: [Card] = Kind.allCases.compactMap { kind in
            // An item with no words would be an empty row: left out, so a group of only those has no card either.
            let items = kind.items(minutes).enumerated()
                .filter { !$0.element.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
                .map { index, item in
                    Item(id: "\(kind.rawValue)-\(index)", text: item.text, at: item.at, byline: RecordingMinutes.byline(item),
                         context: context(at: item.at, in: sentences, before: before, after: after), photo: item.photo,
                         action: kind == .actions ? item.id.flatMap { $0.isEmpty ? nil : $0 } : nil, added: kind == .actions && item.added,
                         todo: kind == .actions && item.added ? item.todoID : nil)
                }
            return items.isEmpty ? nil : Card(kind: kind, items: items)
        }
        return Summary(title: minutes.title?.isEmpty == false ? minutes.title : nil, facts: facts.isEmpty ? nil : facts,
                       attendees: minutes.attendees.isEmpty ? nil : ListFormatter.localizedString(byJoining: minutes.attendees),
                       cards: cards, offered: minutes.version != nil,
                       overview: overview(minutes, sentences: sentences, shown: shown, before: before, after: after))
    }

    /// The line said at `seconds` with `before` lines before it and `after` after it, as far as the transcript goes.
    static func context(at seconds: Double?, in sentences: [RecordingSentence], before: Int, after: Int) -> [Line] {
        guard let seconds, let said = RecordingMinutes.sentenceIndex(at: seconds, in: sentences) else { return [] }
        let first = max(0, said - max(0, before)), last = min(sentences.count - 1, said + max(0, after))
        return (first...last).map { Line(id: $0, start: sentences[$0].start, text: sentences[$0].text, said: $0 == said) }
    }
}
