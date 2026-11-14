import Foundation
#if os(iOS) || os(visionOS)
import Observation
#endif
import PersonaWire

// A recorded day's recap as the phone reads it from the hub (docs/assistant/day-recap.md): the payload in the contract's
// shape, what a day's page shows of it, and the model that follows it. The payload and the page's values are pure and
// compile everywhere; only the model, which talks to the hub for a page, is iOS and visionOS.

// MARK: - The hub's recap

/// `detail.recap` of a day recording (`GET /api/v1/kb/recordings/{id}`). Every field is optional and decoded on its own,
/// so a recap from a newer hub, or one with a malformed item, still shows what it can: an item that can't be read is
/// left out, never the whole recap. Times (`at`, `from`, `to`) are seconds since the recording's `started_at`.
nonisolated struct RecordingDayRecap: Codable, Sendable, Equatable {
    nonisolated struct Span: Codable, Sendable, Equatable {
        var from: Double?
        var to: Double?

        private nonisolated enum CodingKeys: String, CodingKey { case from, to }
    }

    /// The meeting the hub matched a timeline entry to (one of the user's meeting recordings, or a knowledge item).
    nonisolated struct Meeting: Codable, Sendable, Equatable {
        var kind: String?
        var id: String?
        var name: String?

        private nonisolated enum CodingKeys: String, CodingKey { case kind, id, name }
    }

    nonisolated struct Point: Codable, Sendable, Equatable {
        var text: String?
        var at: Double?
        var who: String?

        private nonisolated enum CodingKeys: String, CodingKey { case text, at, who }
    }

    /// A stretch of the day: `kind` `meeting` or `outside`.
    nonisolated struct Entry: Codable, Sendable, Equatable {
        var from: Double?
        var to: Double?
        var kind: String?
        var title: String?
        var meeting: Meeting?
        var points: [Point]?

        private nonisolated enum CodingKeys: String, CodingKey { case from, to, kind, title, meeting, points }
    }

    /// A suggested to-do; `todoID` is set once the user confirmed it.
    nonisolated struct Action: Codable, Sendable, Equatable {
        var id: String?
        var who: String?
        var what: String?
        var by: String?
        var dueDate: String?
        var at: Double?
        var todoID: String?

        private nonisolated enum CodingKeys: String, CodingKey {
            case id, who, what, by, at
            case dueDate = "due_date", todoID = "todo_id"
        }
    }

    nonisolated struct Gap: Codable, Sendable, Equatable {
        var from: Double?
        var to: Double?
        var reason: String?

        private nonisolated enum CodingKeys: String, CodingKey { case from, to, reason }
    }

    var version: Int?
    var revision: Int64?
    /// RFC 3339, as the hub writes it (`writtenAt` reads it).
    var generatedAt: String?
    var day: String?
    var covers: Span?
    /// The day's final recap (`final`): made from the complete recording, or 6 hours or more after the day ended.
    var isFinal: Bool?
    /// Why a final recap is final: `complete` or `cutoff` (`RecordingDayRecap.Reason`); absent on a recap so far.
    var finalReason: String?
    var summary: String?
    var timeline: [Entry]?
    var actions: [Action]?
    var gaps: [Gap]?
    /// The day was too long and some of it was not read.
    var partial: Bool?
    /// The same recap as Markdown with clock times (`recap.md`): what Copy and Share send.
    var markdown: String?

    var writtenAt: Date? { generatedAt.flatMap(JSON.parseDate).flatMap { $0.isMeaningful ? $0 : nil } }

    /// The values of `final_reason` (docs/assistant/day-recap.md「What the hub stores and returns」).
    nonisolated enum Reason {
        /// Made from the complete recording.
        static let complete = "complete"
        /// The day was still not complete 6 hours after it ended: made from what had arrived. A later `complete` writes
        /// another final recap.
        static let cutoff = "cutoff"
    }

    private nonisolated enum CodingKeys: String, CodingKey {
        case version, revision, day, covers, summary, timeline, actions, gaps, partial, markdown
        case generatedAt = "generated_at", isFinal = "final", finalReason = "final_reason"
    }
}

nonisolated extension RecordingDayRecap {
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        version = try? c.decodeIfPresent(Int.self, forKey: .version)
        revision = try? c.decodeIfPresent(Int64.self, forKey: .revision)
        generatedAt = try? c.decodeIfPresent(String.self, forKey: .generatedAt)
        day = try? c.decodeIfPresent(String.self, forKey: .day)
        covers = try? c.decodeIfPresent(Span.self, forKey: .covers)
        isFinal = try? c.decodeIfPresent(Bool.self, forKey: .isFinal)
        finalReason = try? c.decodeIfPresent(String.self, forKey: .finalReason)
        summary = try? c.decodeIfPresent(String.self, forKey: .summary)
        timeline = RecapLossy<Entry>.list(c, .timeline)
        actions = RecapLossy<Action>.list(c, .actions)
        gaps = RecapLossy<Gap>.list(c, .gaps)
        partial = try? c.decodeIfPresent(Bool.self, forKey: .partial)
        markdown = try? c.decodeIfPresent(String.self, forKey: .markdown)
    }
}

nonisolated extension RecordingDayRecap.Span {
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        from = try? c.decodeIfPresent(Double.self, forKey: .from)
        to = try? c.decodeIfPresent(Double.self, forKey: .to)
    }
}

nonisolated extension RecordingDayRecap.Meeting {
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        kind = try? c.decodeIfPresent(String.self, forKey: .kind)
        id = try? c.decodeIfPresent(String.self, forKey: .id)
        name = try? c.decodeIfPresent(String.self, forKey: .name)
    }
}

nonisolated extension RecordingDayRecap.Point {
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        text = try? c.decodeIfPresent(String.self, forKey: .text)
        at = try? c.decodeIfPresent(Double.self, forKey: .at)
        who = try? c.decodeIfPresent(String.self, forKey: .who)
    }
}

nonisolated extension RecordingDayRecap.Entry {
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        from = try? c.decodeIfPresent(Double.self, forKey: .from)
        to = try? c.decodeIfPresent(Double.self, forKey: .to)
        kind = try? c.decodeIfPresent(String.self, forKey: .kind)
        title = try? c.decodeIfPresent(String.self, forKey: .title)
        meeting = try? c.decodeIfPresent(RecordingDayRecap.Meeting.self, forKey: .meeting)
        points = RecapLossy<RecordingDayRecap.Point>.list(c, .points)
    }
}

nonisolated extension RecordingDayRecap.Action {
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try? c.decodeIfPresent(String.self, forKey: .id)
        who = try? c.decodeIfPresent(String.self, forKey: .who)
        what = try? c.decodeIfPresent(String.self, forKey: .what)
        by = try? c.decodeIfPresent(String.self, forKey: .by)
        dueDate = try? c.decodeIfPresent(String.self, forKey: .dueDate)
        at = try? c.decodeIfPresent(Double.self, forKey: .at)
        todoID = try? c.decodeIfPresent(String.self, forKey: .todoID)
    }
}

nonisolated extension RecordingDayRecap.Gap {
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        from = try? c.decodeIfPresent(Double.self, forKey: .from)
        to = try? c.decodeIfPresent(Double.self, forKey: .to)
        reason = try? c.decodeIfPresent(String.self, forKey: .reason)
    }
}

/// A list whose unreadable items are skipped (a number where an object belongs), and nil for a list that isn't one.
private nonisolated struct RecapLossy<Value: Decodable>: Decodable {
    var value: Value?

    init(from decoder: Decoder) throws {
        value = try? Value(from: decoder)
    }

    static func list<Key: CodingKey>(_ c: KeyedDecodingContainer<Key>, _ key: Key) -> [Value]? {
        guard let items = try? c.decodeIfPresent([RecapLossy<Value>].self, forKey: key) else { return nil }
        return items.compactMap(\.value)
    }
}

// MARK: - What the hub answers

/// `GET /api/v1/kb/recordings/{id}` of a day: its recap and the meta fields the page needs. Only these are decoded (the
/// detail also carries the whole day's transcript, which the page reads from this phone instead).
nonisolated struct RecordingDayRecapAnswer: Decodable, Sendable, Equatable {
    var recapState: String?
    /// The newest recap is the final one (`recap_final`), as the recordings list says it too.
    var recapFinal: Bool
    /// The recording's `started_at`: the recap's clock.
    var startedAt: Date?
    var recap: RecordingDayRecap?

    private nonisolated enum Keys: String, CodingKey {
        case detail, meta, recap
        case recapState = "recap_state", recapFinal = "recap_final", startedAt = "started_at"
    }

    init(recapState: String? = nil, recapFinal: Bool = false, startedAt: Date? = nil, recap: RecordingDayRecap? = nil) {
        self.recapState = recapState
        self.recapFinal = recapFinal
        self.startedAt = startedAt
        self.recap = recap
    }

    init(from decoder: Decoder) throws {
        let root = try decoder.container(keyedBy: Keys.self)
        let detail = try root.nestedContainer(keyedBy: Keys.self, forKey: .detail)
        // A recap that can't be read at all is left out, not the answer: its state still says where it is.
        recap = try? detail.decodeIfPresent(RecordingDayRecap.self, forKey: .recap)
        let meta = try? detail.nestedContainer(keyedBy: Keys.self, forKey: .meta)
        recapState = try? meta?.decodeIfPresent(String.self, forKey: .recapState)
        recapFinal = (try? meta?.decodeIfPresent(Bool.self, forKey: .recapFinal)) ?? false
        startedAt = (try? meta?.decodeIfPresent(String.self, forKey: .startedAt)).flatMap(JSON.parseDate)
    }

    /// Where the part is, said the way the recordings list says it.
    var seen: RecordingDayRecapSeen { .listed(recapState, final: recapFinal) }
}

/// `GET /api/v1/recordings`, as far as the day's page reads it: each recording's `recap_state` and `recap_final`, and
/// the day fields reconciliation hands the day's upload (`RecordingDaySync.reconciled`;
/// docs/assistant/day-recap.md「Reconciliation」).
nonisolated struct RecordingDayRecapListing: Decodable, Sendable, Equatable {
    nonisolated struct Row: Decodable, Sendable, Equatable {
        var id: UUID
        var recapState: String?
        /// Its newest recap is the final one; nil when the row doesn't say.
        var recapFinal: Bool?
        /// When its first final recap was written: the phone's 3-day clock for the day's audio.
        var recapFinalAt: Date?
        var revision: Int64?
        var part: Int?
        var next: Int?
        var base: Int64?

        private nonisolated enum CodingKeys: String, CodingKey {
            case id, revision, part, next, base
            case recapState = "recap_state", recapFinal = "recap_final", recapFinalAt = "recap_final_at"
        }

        init(id: UUID, recapState: String? = nil, recapFinal: Bool? = nil, recapFinalAt: Date? = nil, revision: Int64? = nil,
             part: Int? = nil, next: Int? = nil, base: Int64? = nil) {
            self.id = id
            self.recapState = recapState
            self.recapFinal = recapFinal
            self.recapFinalAt = recapFinalAt
            self.revision = revision
            self.part = part
            self.next = next
            self.base = base
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            guard let id = UUID(uuidString: try c.decode(String.self, forKey: .id)) else {
                throw DecodingError.dataCorruptedError(forKey: .id, in: c, debugDescription: "not a recording id")
            }
            self.id = id
            recapState = try? c.decodeIfPresent(String.self, forKey: .recapState)
            recapFinal = try? c.decodeIfPresent(Bool.self, forKey: .recapFinal)
            recapFinalAt = (try? c.decodeIfPresent(String.self, forKey: .recapFinalAt)).flatMap(JSON.parseDate)
            revision = try? c.decodeIfPresent(Int64.self, forKey: .revision)
            part = try? c.decodeIfPresent(Int.self, forKey: .part)
            next = try? c.decodeIfPresent(Int.self, forKey: .next)
            base = try? c.decodeIfPresent(Int64.self, forKey: .base)
        }

        var seen: RecordingDayRecapSeen { .listed(recapState, final: recapFinal ?? false) }
    }

    var rows: [Row]

    private nonisolated enum CodingKeys: String, CodingKey { case recordings }

    init(rows: [Row]) { self.rows = rows }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        rows = RecapLossy<Row>.list(c, .recordings) ?? []
    }

    /// How many sentence positions the hub holds of each listed recording (`next`), where the row says.
    var positions: [UUID: Int] {
        Dictionary(rows.compactMap { row in row.next.map { (row.id, $0) } }, uniquingKeysWith: { first, _ in first })
    }

    /// Each listed recording's state; a recording not listed is not on the hub.
    func seen(_ ids: [UUID]) -> [UUID: RecordingDayRecapSeen] {
        let byID = Dictionary(rows.map { ($0.id, $0.seen) }, uniquingKeysWith: { first, _ in first })
        return Dictionary(ids.map { id in (id, byID[id] ?? .absent) }, uniquingKeysWith: { first, _ in first })
    }

    /// The rows of a day's parts as reconciliation hands them to the day's upload, or nil unless every part is listed:
    /// what a part missing from the list means (a new hub, a delete) is reconciliation's to judge, not a page's.
    func hubRows(_ ids: [UUID]) -> [RecordingDayHubRow]? {
        let byID = Dictionary(rows.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        let listed = ids.compactMap { byID[$0] }
        guard !listed.isEmpty, listed.count == ids.count else { return nil }
        return listed.map {
            RecordingDayHubRow(id: $0.id, revision: $0.revision, part: $0.part, next: $0.next, base: $0.base, recapState: $0.recapState,
                               recapFinal: $0.recapFinal, recapFinalAt: $0.recapFinalAt)
        }
    }

    /// A day's recap state across its parts, summed as the day's record sums it (`RecordingDayRecord.recapState`): failed
    /// when one failed, else pending while one is being written, else ready; an unknown state as it is.
    static func dayState(_ states: [String?]) -> String? {
        let states = states.compactMap { $0 }
        for state in ["failed", "pending", "ready"] where states.contains(state) { return state }
        return states.last
    }

    /// Whether the day's recap is final across its parts, as the day's record sums it (`RecordingDayRecord.recapFinal`):
    /// every part's newest recap is.
    static func dayFinal(_ rows: [RecordingDayHubRow]) -> Bool {
        !rows.isEmpty && rows.allSatisfy { $0.recapFinal == true }
    }
}

/// Where a hub recording's recap was at the last look: not on the hub, or listed with its `recap_state` (nil: none due)
/// and whether its newest recap is the final one. A recap that becomes final can keep the state `ready`, so the page
/// reads the detail again when either moves.
nonisolated enum RecordingDayRecapSeen: Equatable, Sendable {
    case absent
    case listed(String?, final: Bool)

    /// Listed with a recap the hub could not write.
    var failed: Bool {
        if case .listed("failed"?, _) = self { return true }
        return false
    }
}

// MARK: - What the page shows

/// A day's recaps as its page shows them: every part's recap on the wall clock (each part's times are seconds since its
/// `started_at`, which every part shares with part 1: docs/assistant/day-recap.md「Parts」), the timeline by time of day
/// in the day's zone, the suggested to-dos, and the text Copy and Share send.
nonisolated struct RecordingDayRecapContent: Equatable, Sendable {
    /// The clock's parts of the day the timeline is grouped by.
    nonisolated enum Period: Int, CaseIterable, Comparable, Sendable {
        case morning, afternoon, evening

        init(hour: Int) {
            self = hour < 12 ? .morning : hour < 18 ? .afternoon : .evening
        }

        static func < (a: Self, b: Self) -> Bool { a.rawValue < b.rawValue }
    }

    nonisolated struct Point: Equatable, Sendable, Identifiable {
        var id: String
        var text: String
        var who: String?
        var at: Date?
    }

    nonisolated struct Entry: Equatable, Sendable, Identifiable {
        var id: String
        var from: Date
        var to: Date
        var title: String
        /// Inside a meeting: one the hub knows (`meetingName`), or one the model judged to be.
        var meeting: Bool
        var meetingName: String?
        var points: [Point]
    }

    nonisolated struct Section: Equatable, Sendable, Identifiable {
        var period: Period
        var entries: [Entry]
        var id: Period { period }
    }

    nonisolated struct Action: Equatable, Sendable, Identifiable {
        var id: String
        var what: String
        var who: String?
        /// The deadline as said ("tomorrow"), and as a date when it is one (`YYYY-MM-DD`).
        var by: String?
        var dueDate: String?
        var at: Date?
        /// Added to the user's to-dos (on the web, by the minutes' confirmation route).
        var confirmed: Bool
    }

    /// One part's recap and the moment its times count from.
    nonisolated struct Part: Equatable, Sendable {
        var recap: RecordingDayRecap
        var startedAt: Date
    }

    /// Whether the recap is the day's last word: so far (the day is still being recorded or transcribed), or final.
    nonisolated enum Finality: Equatable, Sendable {
        /// Newer text may still come, and with it a newer recap.
        case soFar
        /// Made from the complete day.
        case final
        /// Made 6 hours after the day ended from what had arrived; written again if the rest of the day comes.
        case cutoff
    }

    var summary: String
    /// When the newest part's recap was written.
    var writtenAt: Date?
    /// Final only once every part's recap is; cut off when one of them was made at the cutoff.
    var finality: Finality
    var partial: Bool
    var sections: [Section]
    var actions: [Action]
    var markdown: String

    var isFinal: Bool { finality != .soFar }
    var isEmpty: Bool { summary.isEmpty && sections.isEmpty && actions.isEmpty }
}

nonisolated extension RecordingDayRecapContent {
    init(parts: [Part], zone: TimeZone) {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = zone
        var entries: [Entry] = []
        var actions: [Action] = []
        for (p, part) in parts.enumerated() {
            let clock = { (seconds: Double?) -> Date? in
                guard let seconds, seconds.isFinite else { return nil }
                return part.startedAt.addingTimeInterval(seconds)
            }
            for (e, entry) in (part.recap.timeline ?? []).enumerated() {
                let points = (entry.points ?? []).enumerated().compactMap { i, point -> Point? in
                    guard let text = Self.text(point.text) else { return nil }
                    return Point(id: "\(p)-\(e)-\(i)", text: text, who: Self.text(point.who), at: clock(point.at))
                }
                // An entry is placed by its start, else by its first point that has a time.
                guard let from = clock(entry.from) ?? points.compactMap(\.at).min() else { continue }
                let meetingName = Self.text(entry.meeting?.name)
                guard let title = Self.text(entry.title) ?? meetingName ?? points.first?.text else { continue }
                entries.append(Entry(id: "\(p)-\(e)", from: from, to: max(from, clock(entry.to) ?? from), title: title,
                                     meeting: entry.kind == "meeting" || entry.meeting != nil, meetingName: meetingName,
                                     points: points))
            }
            for (i, action) in (part.recap.actions ?? []).enumerated() {
                guard let what = Self.text(action.what) else { continue }
                actions.append(Action(id: "\(p)-\(action.id ?? String(i))", what: what, who: Self.text(action.who),
                                      by: Self.text(action.by), dueDate: Self.text(action.dueDate), at: clock(action.at),
                                      confirmed: Self.text(action.todoID) != nil))
            }
        }
        entries.sort { ($0.from, $0.id) < ($1.from, $1.id) }
        let grouped = Dictionary(grouping: entries) { Period(hour: calendar.component(.hour, from: $0.from)) }
        let recaps = parts.map(\.recap)
        self.init(summary: recaps.compactMap { Self.text($0.summary) }.joined(separator: "\n\n"),
                  writtenAt: recaps.compactMap(\.writtenAt).max(),
                  finality: Self.finality(recaps),
                  partial: recaps.contains { $0.partial == true },
                  sections: Period.allCases.compactMap { period in grouped[period].map { Section(period: period, entries: $0) } },
                  actions: actions.sorted { ($0.at ?? .distantFuture, $0.id) < ($1.at ?? .distantFuture, $1.id) },
                  markdown: recaps.compactMap { Self.text($0.markdown) }.joined(separator: "\n\n"))
    }

    /// The text Copy and Share send: the hub's Markdown, else the summary.
    var shareText: String { markdown.isEmpty ? summary : markdown }

    /// The day's recaps together: so far while any part's isn't final; cut off when a final one was made at the cutoff
    /// (part of the day may still come); final otherwise.
    static func finality(_ recaps: [RecordingDayRecap]) -> Finality {
        guard !recaps.isEmpty, recaps.allSatisfy({ $0.isFinal == true }) else { return .soFar }
        return recaps.contains { $0.finalReason == RecordingDayRecap.Reason.cutoff } ? .cutoff : .final
    }

    /// Text with something in it, trimmed; nil otherwise.
    private static func text(_ value: String?) -> String? {
        guard let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines), !trimmed.isEmpty else { return nil }
        return trimmed
    }
}

// MARK: - The recap's state

/// Where a day's recap is, as its page shows it (the minutes' states, `RecordingMinutesModel.State`).
nonisolated enum RecordingDayRecapState: Equatable, Sendable {
    /// Nothing to show: a day the hub keeps no text of and wrote no recap for.
    case none
    /// The recap comes later: in the evening, and again once the day ends (or once there is text).
    case waitingForText
    /// The day's text isn't on the hub yet.
    case waitingForSync
    /// The hub is writing it, with none written before.
    case writing
    case ready(RecordingDayRecapContent, note: Note?)
    /// The hub couldn't write it; `POST …/minutes` asks again.
    case failed
    /// The hub doesn't take day recordings: no recap until it is updated.
    case unsupported

    /// What a ready recap says besides itself: a newer one is being written, or the newest couldn't be.
    nonisolated enum Note: Equatable, Sendable {
        case updating, failed
    }

    /// One hub recording (part) as the hub answered: nil when it doesn't list it (not on the hub yet).
    nonisolated struct Part: Equatable, Sendable {
        var recapState: String?
        var recap: RecordingDayRecap?
        var startedAt: Date?
        /// The hub holds sentence positions of it (`next` above 0 in the list); nil when the list didn't say.
        var hasText: Bool?
    }

    /// What the page knows of the day itself, for a recap that will never come: a day that ended without a word gets
    /// none (docs/assistant/day-recap.md), and a complete day's last recap the hub writes by itself is due by a known time.
    nonisolated struct Day: Equatable, Sendable {
        /// The day's recording ended, and all of it is settled and on the hub (`RecordingDaySummary.complete`).
        var complete = false
        /// Some of its audio may hold words: a chunk that was neither quieter than speech nor failed to transcribe (one
        /// still waiting counts).
        var mayHaveText = true
        /// When the last recap the hub writes by itself is due at the latest: the cutoff's 6 hours after the day ended,
        /// and its 30 minutes after the last upload, which for a complete day came by then. nil before the day has an
        /// end.
        var lastRecapDue: Date?
        /// Nothing more of it goes to the hub (`problem`, `expired`): the hub writes from what it holds, by the same time.
        var stopped = false

        /// From the end of the day to the last recap the hub writes by itself.
        static let lastRecapAfter: TimeInterval = 6.5 * 3600
    }

    /// The state from what the hub said of each part (`parts` nil: no answer, offline or demo), where the phone's
    /// upload is (`sync`), whether any of the day is transcribed, and the day's own facts (`day`). `fallbackStart` is the
    /// day recording's start (`recording_started_at`), every part's clock, when the hub's meta leaves it out.
    static func of(parts: [Part?]?, sync: RecordingDaySyncState, transcribed: Bool, day: Day = Day(), fallbackStart: Date?,
                   zone: TimeZone) -> RecordingDayRecapState {
        if sync == .notOffered || sync == .unsupported { return .unsupported }
        // Nothing more of the day goes to the hub: its upload stopped for good (the page says why, under the day's
        // word) or the hub keeps no text of it.
        let stopped = sync == .problem || sync == .expired
        // The day ended without a word to read: the hub writes it no recap, ever.
        let silent = day.complete && !day.mayHaveText
        let later: RecordingDayRecapState = stopped || silent ? .none : transcribed ? .waitingForSync : .waitingForText
        guard let parts else { return later }
        let held = parts.compactMap { $0 }
        let recaps = parts.compactMap { part -> RecordingDayRecapContent.Part? in
            // Every part counts from the day recording's start, so a part whose meta leaves it out borrows the day's.
            guard let part, let recap = part.recap, let start = part.startedAt ?? fallbackStart else { return nil }
            return RecordingDayRecapContent.Part(recap: recap, startedAt: start)
        }
        let states = held.compactMap(\.recapState)
        if !recaps.isEmpty {
            let note: Note? = states.contains("failed") ? .failed : states.contains("pending") ? .updating : nil
            return .ready(RecordingDayRecapContent(parts: recaps, zone: zone), note: note)
        }
        if states.contains("failed") { return .failed }
        if states.contains("pending") { return .writing }
        if held.isEmpty { return later }
        // Complete, or stopped for good, and the hub holds no position of any part: nothing of it there is a word, and
        // nothing more comes (a create the hub took, then a first append it refused).
        if silent || ((day.complete || stopped) && held.allSatisfy { $0.hasText == false }) { return .none }
        // On the hub with no recap due yet: it comes in the evening, or when the day ends.
        return .waitingForText
    }

    /// Worth asking the hub again soon: the recap is on its way.
    var waiting: Bool {
        switch self {
        case .waitingForText, .waitingForSync, .writing: true
        case .ready(_, let note): note == .updating
        case .none, .failed, .unsupported: false
        }
    }

    /// Worth asking again soon for this day at `now`: on its way (`waiting`), except a recap still waited for once a
    /// complete day is past the last one the hub writes by itself. None comes then unless asked; the page looks again
    /// when it opens or the day's summary moves.
    func follows(_ day: Day, now: Date) -> Bool {
        guard waiting else { return false }
        if self == .waitingForText, day.complete || day.stopped, let due = day.lastRecapDue, now >= due { return false }
        return true
    }
}

nonisolated extension RecordingDayRecapState.Day {
    init(_ summary: RecordingDaySummary) {
        complete = summary.complete
        mayHaveText = Self.mayHaveText(summary)
        lastRecapDue = (summary.closedAt ?? summary.span?.end).map { $0.addingTimeInterval(Self.lastRecapAfter) }
        stopped = summary.sync == .problem || summary.sync == .expired
    }

    /// A chunk with audio that was neither quieter than speech nor failed to transcribe (or is still waiting).
    static func mayHaveText(_ summary: RecordingDaySummary) -> Bool {
        summary.chunks > summary.skipped + summary.failed
    }
}

/// What a day's page keys its recap task on: what changes where the day is with the hub, and Retry. Coarse on purpose:
/// the upload's `pending` / `synced`, which flips with each transcribed chunk and its append while the watch records, is
/// one value here, or every flip would start the task again and read the whole recordings list (`load`) at once.
nonisolated struct RecordingDayRecapKey: Hashable, Sendable {
    /// Where the upload is, as the recap's state reads it.
    nonisolated enum Upload: Hashable, Sendable {
        /// The hub doesn't take day recordings (`notOffered`, `unsupported`).
        case refused
        /// Nothing more goes: the upload stopped for good (`problem`), or the hub keeps no text (`expired`).
        case stopped
        /// On its way or there (`pending`, `synced`).
        case going
    }

    var upload: Upload?
    var transcribed: Bool
    /// The day's own facts the recap's state reads (`RecordingDayRecapState.Day`), each of which changes once.
    var complete: Bool
    var mayHaveText: Bool
    var recapState: String?
    var recapFinal: Bool
    var hubIDs: [UUID]
    var round: Int

    init(_ summary: RecordingDaySummary?, round: Int) {
        upload = summary.map {
            switch $0.sync {
            case .notOffered, .unsupported: .refused
            case .problem, .expired: .stopped
            case .pending, .synced: .going
            }
        }
        transcribed = summary?.transcribedTo != nil
        complete = summary?.complete ?? false
        // Only a complete day's matters (a silent one waits for no recap): while the day records, chunks that come in
        // waiting and are then skipped as quiet would flip it, and start the task again, each time.
        mayHaveText = complete ? summary.map(RecordingDayRecapState.Day.mayHaveText) ?? true : true
        recapState = summary?.recapState
        recapFinal = summary?.recapFinal ?? false
        hubIDs = summary?.hubIDs ?? []
        self.round = round
    }
}

// MARK: - The transcript

/// The day's transcript as its page lists it: every sentence the phone holds, on the wall clock, grouped by the hour in
/// the day's zone. Read from this phone's store (the hub's copy may lag behind), built off the main actor.
nonisolated enum RecordingDayTranscript {
    nonisolated struct Line: Equatable, Sendable, Identifiable {
        /// The chunk's seq and the sentence's place in it: stable while text is added around it.
        var id: String
        var at: Date
        var text: String
    }

    nonisolated struct Hour: Equatable, Sendable, Identifiable {
        /// The hour's first moment.
        var id: Date
        var lines: [Line]
    }

    /// The lines in time order, empty sentences left out (an erased sentence keeps its hub position with no text);
    /// erased and lost chunks hold none.
    static func lines(_ record: RecordingDayRecord) -> [Line] {
        var out: [Line] = []
        for chunk in record.chunks.values where !chunk.isErasedOrLost {
            for (i, sentence) in (chunk.sentences ?? []).enumerated() {
                let text = sentence.text.trimmingCharacters(in: .whitespacesAndNewlines)
                guard !text.isEmpty, sentence.start.isFinite else { continue }
                out.append(Line(id: "s-\(chunk.seq)-\(i)", at: chunk.startedAt.addingTimeInterval(max(0, sentence.start)), text: text))
            }
        }
        return out.sorted { ($0.at, $0.id) < ($1.at, $1.id) }
    }

    /// `lines` by the hour they start in, in `zone`.
    static func hours(_ lines: [Line], in zone: TimeZone) -> [Hour] {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = zone
        var out: [Hour] = []
        for line in lines {
            let hour = calendar.dateInterval(of: .hour, for: line.at)?.start ?? line.at
            if out.last?.id == hour { out[out.count - 1].lines.append(line) } else { out.append(Hour(id: hour, lines: [line])) }
        }
        return out
    }

    /// The line said at `moment`: the last one that started by then (a second's grace: a point is stamped as its sentence
    /// begins), else the first.
    static func line(at moment: Date, in lines: [Line]) -> Line? {
        lines.last { $0.at <= moment.addingTimeInterval(1) } ?? lines.first
    }
}

#if os(iOS) || os(visionOS)
// MARK: - The model

/// A day's recap for its page. Each look reads the light recordings list (`GET /api/v1/recordings`) for where every
/// part of the day is, and reads a part's detail (its recap) only when the part has a recap due and its state (or whether
/// it is final) moved since the last read: the detail also carries the day's whole transcript, and is decoded off the
/// main actor. The page looks when it opens, when the day's summary moves, and every `pollSeconds` while the recap is on
/// its way. What the list says of the day's parts goes to the day's upload, which stores it on the day as reconciliation
/// would (`report`): the page's header, the Today row and Recent Recordings then say what the recap section shows,
/// without waiting for the next reconciliation (event-driven, and at most every ten minutes).
@MainActor @Observable final class RecordingDayRecapModel {
    private(set) var state: RecordingDayRecapState = .none
    private(set) var asking = false
    /// Bumped by Retry: the page's following starts over (its task is keyed on it).
    private(set) var round = 0
    /// The day's recap state across its parts at the last read, once every part was listed: the page's header says it
    /// at once (`shown`), while the day's summary catches up through the upload.
    private(set) var heard: Heard?
    @ObservationIgnored private let api: () -> APIClient?
    @ObservationIgnored private let reconciled: (UUID, [RecordingDayHubRow]) -> Void
    @ObservationIgnored private let now: () -> Date
    /// What the last read found; nil before the first.
    @ObservationIgnored private var last: Read?
    /// The day's own facts at the last look (`RecordingDayRecapState.follows`); nil before the first.
    @ObservationIgnored private var day: RecordingDayRecapState.Day?

    /// While the recap is on its way, the list is asked this often.
    static let pollSeconds = 30.0

    nonisolated struct Heard: Equatable, Sendable {
        var hubIDs: [UUID]
        var recapState: String?
    }

    /// A read of the day's parts: where each was, and what each said. A part is left out of `seen` when its recap is
    /// still to be read (its detail wasn't served yet, or Retry asked for a new one): the next look reads it again.
    private struct Read {
        var ids: [UUID]
        var seen: [UUID: RecordingDayRecapSeen]
        var parts: [RecordingDayRecapState.Part?]
    }

    /// - Parameter reconciled: hands the day's upload the list's rows of a day's parts (`RecordingDaySync.reconciled`).
    init(api: @escaping () -> APIClient? = { AppModel.shared.api },
         reconciled: @escaping (UUID, [RecordingDayHubRow]) -> Void = { RecordingDaySync.shared.reconciled([$0: $1]) },
         now: @escaping () -> Date = { .now }) {
        self.api = api
        self.reconciled = reconciled
        self.now = now
    }

    /// The hub recordings of a day, in part order: the first has the watch's id, before the upload has named any.
    static func hubIDs(_ day: RecordingDaySummary) -> [UUID] {
        day.hubIDs.isEmpty ? [day.id] : day.hubIDs
    }

    /// Looks at the day's recap on the hub: where every part is, and each recap that moved since the last read.
    func load(_ day: RecordingDaySummary) async {
        self.day = RecordingDayRecapState.Day(day)
        if day.sync == .notOffered || day.sync == .unsupported {
            show(.unsupported, heard: nil)
            return
        }
        guard let api = api(), !api.config.isDemo else {
            show(mapped(day, parts: nil), heard: nil)
            return
        }
        let ids = Self.hubIDs(day)
        do {
            let listing = try await api.getDecodedOffMain("/api/v1/recordings", as: RecordingDayRecapListing.self)
            report(listing, for: day)
            let read = try await Self.read(ids, listed: listing.seen(ids), positions: listing.positions, after: last, api)
            last = read
            show(mapped(day, parts: read.parts), heard: Self.heard(read))
        } catch {
            // Cancelled (the page's task started again, or the page went): nothing was learned, and the hub isn't out of
            // reach. The next look says what there is.
            if error.isCancellation { return }
            // Out of reach for now: a recap it shows stays; otherwise what the last read said, else it waits for the
            // text to get there.
            if case .ready = state { return }
            show(mapped(day, parts: last.flatMap { $0.ids == ids ? $0.parts : nil }), heard: heard)
        }
    }

    /// The summary as the page's header shows it: with the recap state the page last read for every part of the day.
    func shown(_ day: RecordingDaySummary) -> RecordingDaySummary {
        guard let heard, heard.hubIDs == Self.hubIDs(day) else { return day }
        var shown = day
        shown.recapState = heard.recapState
        return shown
    }

    /// Asks the hub to write the recap again (`POST …/minutes`, which on a day recording writes its recap): the parts
    /// whose recap failed, else every part.
    func regenerate(_ day: RecordingDaySummary) async {
        guard let api = api(), !asking else { return }
        asking = true
        defer { asking = false }
        let ids = Self.hubIDs(day)
        let failed = ids.filter { last?.seen[$0]?.failed == true }
        let asked = failed.isEmpty ? ids : failed
        do {
            for id in asked {
                _ = try await api.request("POST", "/api/v1/recordings/\(id.uuidString.lowercased())/minutes")
            }
            if case .ready(let content, _) = state { state = .ready(content, note: .updating) } else { state = .writing }
            // Their recaps are read again at the next look, whatever the list says by then.
            for id in asked { last?.seen[id] = nil }
            round += 1
        } catch let error as APIError where error.status == 409 {
            // No text on the hub yet, or its transcript expired: look again at what there is.
            round += 1
        } catch {
            AppModel.shared.toast = Toast(String(localized: "Couldn't ask the hub for a new recap: \(error.localizedDescription)"), style: .error)
        }
    }

    /// Worth looking again in `pollSeconds`: the recap is on its way, for the day as the last look saw it.
    var waiting: Bool { state.follows(day ?? RecordingDayRecapState.Day(), now: now()) }

    // MARK: Reading

    /// Each part as the hub has it: not listed (nil), listed with no recap due (its detail isn't read), or its detail's
    /// recap and state, each with whether the hub holds text of it (`positions`, the list's `next`). A part whose place
    /// in the list hasn't moved since `last` is taken from it, unread.
    private static func read(_ ids: [UUID], listed: [UUID: RecordingDayRecapSeen], positions: [UUID: Int], after last: Read?,
                             _ api: APIClient) async throws -> Read {
        var seen: [UUID: RecordingDayRecapSeen] = [:]
        var parts: [RecordingDayRecapState.Part?] = []
        for id in ids {
            if let last, let before = last.seen[id], before == listed[id], let at = last.ids.firstIndex(of: id) {
                seen[id] = before
                parts.append(last.parts[at])
                continue
            }
            guard case .listed(let listedState, let listedFinal)? = listed[id] else {
                seen[id] = .absent
                parts.append(nil)
                continue
            }
            guard let listedState else {
                seen[id] = .listed(nil, final: listedFinal)
                parts.append(.init(recapState: nil))
                continue
            }
            do {
                let answer = try await fetch(id, api)
                // The detail's own state, said with its recap: when the list is ahead of it, the next look reads it again.
                seen[id] = answer.seen
                parts.append(.init(recapState: answer.recapState, recap: answer.recap, startedAt: answer.startedAt))
            } catch let error as APIError where error.status == 404 {
                // Listed, but its detail isn't served yet: the list's state for now, and read again at the next look.
                parts.append(.init(recapState: listedState))
            }
        }
        // Whether the hub holds text moves with every append, not with the recap: said by this look's list.
        for (i, id) in ids.enumerated() where parts[i] != nil {
            parts[i]?.hasText = positions[id].map { $0 > 0 }
        }
        return Read(ids: ids, seen: seen, parts: parts)
    }

    /// What a read says of the whole day, when every part of it was listed.
    private static func heard(_ read: Read) -> Heard? {
        let held = read.parts.compactMap { $0 }
        guard held.count == read.parts.count else { return nil }
        return Heard(hubIDs: read.ids, recapState: RecordingDayRecapListing.dayState(held.map(\.recapState)))
    }

    /// Hands the day's upload what the list says of the day's parts when their recap's state, or whether it is final, is
    /// news to the day's summary: the upload stores it on the day as reconciliation would (a final recap also starts the
    /// audio's 3-day clock), and every row of the day follows.
    private func report(_ listing: RecordingDayRecapListing, for day: RecordingDaySummary) {
        guard !day.hubIDs.isEmpty, let rows = listing.hubRows(day.hubIDs) else { return }
        let news = RecordingDayRecapListing.dayState(rows.map(\.recapState)) != day.recapState
            || RecordingDayRecapListing.dayFinal(rows) != day.recapFinal
        guard news else { return }
        reconciled(day.id, rows)
    }

    /// Assigned only when they change: the page animates the recap's state and the list redraws on each assignment.
    private func show(_ next: RecordingDayRecapState, heard nextHeard: Heard?) {
        if next != state { state = next }
        if nextHeard != heard { heard = nextHeard }
    }

    private func mapped(_ day: RecordingDaySummary, parts: [RecordingDayRecapState.Part?]?) -> RecordingDayRecapState {
        RecordingDayRecapState.of(parts: parts, sync: day.sync, transcribed: day.transcribedTo != nil, day: .init(day),
                                  fallbackStart: day.firstAt, zone: RecordingDayStatus.zone(day.timeZone))
    }

    /// `GET /api/v1/kb/recordings/{id}`, decoded off the main actor.
    static func fetch(_ id: UUID, _ api: APIClient) async throws -> RecordingDayRecapAnswer {
        try await api.getDecodedOffMain("/api/v1/kb/recordings/\(id.uuidString.lowercased())", as: RecordingDayRecapAnswer.self)
    }
}
#endif
