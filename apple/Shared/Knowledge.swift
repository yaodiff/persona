import Foundation
import PersonaWire

/// The knowledge base under the hub's ~/.persona/data (docs/assistant/knowledge.md): projects, topics, chats, people, docs,
/// meetings and events, each ranked by importance (P0–P3) and relevance to the user, with free tags. Topics (#3783, #7251)
/// are what Persona curated by subject out of the projects and chats: knowledge points under subtopics.
nonisolated enum KBKind: String, CaseIterable, Identifiable, Sendable {
    case projects, topics, chats, people, docs, meetings, events, recordings
    var id: String { rawValue }
    var title: String {
        switch self {
        case .projects: String(localized: "Project")
        case .topics: String(localized: "Topic [Knowledge]")
        case .chats: String(localized: "Session")
        case .people: String(localized: "People")
        case .docs: String(localized: "Document")
        case .meetings: String(localized: "Meetings")
        case .events: String(localized: "Event [Feishu Doc Renderer a14f23]")
        case .recordings: String(localized: "Record [Recordings Section ef2821]")
        }
    }
    var symbol: String {
        switch self {
        case .projects: Theme.Icon.space
        case .topics: Theme.Icon.topic
        case .chats: Theme.Icon.feishuChats
        case .people: Theme.Icon.people
        case .docs: Theme.Icon.document
        case .meetings: "video"
        case .events: "calendar"
        case .recordings: Theme.Icon.recording
        }
    }

    /// The kinds Knowledge offers as chips: recordings are listed under meetings (#8731, "one home for meetings"), with
    /// the meetings imported from connected apps, so there is one place for meetings and notes.
    static var listed: [KBKind] { allCases.filter { $0 != .recordings } }
    /// The list that shows this kind's entries: recordings' is meetings'.
    var listedAs: KBKind { self == .recordings ? .meetings : self }
}

nonisolated struct KBEntry: Codable, Identifiable, Hashable, Sendable {
    var kind: String
    var id: String
    var name: String
    var oneLine: String?
    var importance: String?
    var relevance: Int
    var tags: [String]?
    var updated: Date?
    var extra: String?
    /// A recording's kind: meeting, note or day (#8870); nil on other kinds and from a hub before it.
    var recordingKind: String?
    /// A chat's source; a document's, meeting's or event's connector (#8872); nil from a hub before it.
    var source: String?
    /// A recording's imported meeting: the id of the meetings item it overlaps, as the meetings list has it (#8872).
    var meetingID: String?
    /// In the meetings list, a recording that stands for an imported meeting too: that meeting (`KBList.meetings`).
    var imported: KBImportedMeeting? = nil
    /// A project's parent as it shows (#12927); nil at the top level and from a hub before it. Read only here.
    var parent: String? = nil
    /// A project's milestone flags (#12927); nil without any and from a hub before them. Read only here.
    var milestones: [KBMilestone]? = nil
    /// A recording whose in-flight state the hub holds back, and why (#15214); nil when nothing is paused.
    var paused: RecordingPause? = nil

    enum CodingKeys: String, CodingKey {
        case kind, id, name, importance, relevance, tags, updated, extra, source, parent, milestones, paused
        case oneLine = "one_line"
        case recordingKind = "recording_kind"
        case meetingID = "meeting_id"
    }

    /// The pill a recording's row carries in the meetings list: notes and all-day recordings are the exceptions among
    /// meetings, so only they are marked; a meeting, an imported one, or a recording from an older hub, none.
    var recordingTag: String? {
        guard kind == KBKind.recordings.rawValue else { return nil }
        switch recordingKind {
        case "note": return String(localized: "Note [Recording Kind]")
        case "day": return String(localized: "All-Day")
        default: return nil
        }
    }

    /// The pill a recording's row carries while the hub's recording module is off and its minutes or upload wait for it
    /// (#15214); none for a reason this app doesn't know.
    var pausedTag: String? {
        guard kind == KBKind.recordings.rawValue, paused?.isModuleOff == true else { return nil }
        return String(localized: "Paused")
    }

    /// The phone's own recording this entry is, when it has it: its page opens instead of the hub's copy.
    var localRecording: UUID? { kind == KBKind.recordings.rawValue ? UUID(uuidString: id) : nil }

    /// Projects: "short · active" → Short-term / Long-term and the status.
    var projectKind: String? {
        guard kind == KBKind.projects.rawValue, let extra else { return nil }
        return extra.hasPrefix("long") ? String(localized: "Long-term") : String(localized: "Short-term")
    }
    var projectDone: Bool { kind == KBKind.projects.rawValue && (extra ?? "").hasSuffix("done") }
    /// A topic's knowledge points, as the list's `extra` counts them (nil for other kinds).
    var topicPoints: Int? { kind == KBKind.topics.rawValue ? Int(extra ?? "") : nil }
    /// Marked "not a project" (by hand, or because it only came from notifications).
    var projectIgnored: Bool { kind == KBKind.projects.rawValue && (extra ?? "").hasSuffix("ignored") }
}

nonisolated struct KBList: Decodable, Sendable {
    var items: [KBEntry]
    var tags: [String]?

    /// The meetings list (#8731): the meetings imported from connected apps and the recordings made in Persona, with
    /// each tag once. A meeting both recorded and imported is one row (#8872): the recording's, which carries the
    /// imported meeting (`imported`), and the imported meeting's own row goes.
    static func meetings(_ meetings: KBList, recordings: KBList) -> KBList {
        var tags: [String] = []
        for t in (meetings.tags ?? []) + (recordings.tags ?? []) where !tags.contains(t) { tags.append(t) }
        let byID = Dictionary(meetings.items.map { ($0.id, $0) }, uniquingKeysWith: { first, _ in first })
        var covered = Set<String>()
        let recs = recordings.items.map { r -> KBEntry in
            guard let id = r.meetingID, let m = byID[id] else { return r }
            covered.insert(m.id)
            var r = r
            r.imported = KBImportedMeeting(id: m.id, url: KBImportedMeeting.link(m.extra), source: m.source)
            return r
        }
        return KBList(items: meetings.items.filter { !covered.contains($0.id) } + recs, tags: tags)
    }

    /// Newest first, entries with no time last; otherwise in the order given.
    static func newestFirst(_ items: [KBEntry]) -> [KBEntry] {
        items.enumerated().sorted { a, b in
            switch (a.element.updated, b.element.updated) {
            case let (x?, y?) where x != y: x > y
            case (.some, nil): true
            case (nil, .some): false
            default: a.offset < b.offset
            }
        }.map(\.element)
    }
}

nonisolated struct KBDetail: Decodable, Sendable {
    var kind: String
    var id: String
    var path: String
    var meta: JSONValue?
    var summary: String?
    var log: [String]?
    var recent: String?
    var links: [KBEntry]?
    /// Links the user keeps out of automatic updates. Older hubs omit this list.
    var removed: [KBEntry]?
    /// A project's Moments posts (the hub's LinkMoments), newest first.
    var moments: [KBMomentRef]?
    /// A topic's knowledge points (docs/assistant/knowledge.md "Topics").
    var points: [KBTopicPoint]?
    /// A recording whose in-flight state the hub holds back, and why (#15214); nil when nothing is paused.
    var paused: RecordingPause?

    /// A recording's imported meeting (#8872): `meta.imported_meeting` and the link the hub resolves for it; nil without
    /// one, or with nothing to open.
    var importedMeeting: KBImportedMeeting? {
        guard kind == KBKind.recordings.rawValue else { return nil }
        return KBImportedMeeting.of(meta, links: links)
    }
}

/// The meeting imported from a connected app that a recording stands for too (#8872): its id, its notes' link and the
/// connector it came from.
nonisolated struct KBImportedMeeting: Codable, Hashable, Sendable {
    var id: String
    var url: URL?
    var source: String?

    /// A meetings item's `extra` as a link: its notes in the app, nil when it has none.
    static func link(_ extra: String?) -> URL? {
        guard let s = extra?.trimmingCharacters(in: .whitespacesAndNewlines), !s.isEmpty else { return nil }
        return URL(string: s)
    }

    /// From a recording's detail: `meta.imported_meeting`, with its notes' link and connector from the link the hub
    /// resolves for it; nil without one, or with no link to open.
    static func of(_ meta: JSONValue?, links: [KBEntry]?) -> KBImportedMeeting? {
        of(id: meta?["imported_meeting"]?["id"]?.string, links: links)
    }

    /// The imported meeting `id` among a detail's `links`, with its notes' link; nil without either.
    static func of(id: String?, links: [KBEntry]?) -> KBImportedMeeting? {
        guard let id, let link = (links ?? []).first(where: { $0.kind == KBKind.meetings.rawValue && $0.id == id }),
              let url = Self.link(link.extra) else { return nil }
        return KBImportedMeeting(id: id, url: url, source: link.source)
    }
}

/// One knowledge point of a topic: what it is (`kind`), the text, the reason or cause (`why`), the subtopics it is under
/// (outermost first, 0–2), the entries it came from and the day it was last confirmed (YYYY-MM-DD).
nonisolated struct KBTopicPoint: Decodable, Identifiable, Hashable, Sendable {
    var id: String
    var path: [String]?
    var kind: String
    var text: String
    var why: String?
    var sources: [KBTopicSource]?
    var when: String?
}

/// An entry a knowledge point came from (a project or a chat), and the day.
nonisolated struct KBTopicSource: Decodable, Hashable, Sendable {
    var kind: String
    var id: String
    var day: String?
}

/// A Moments post a project shows: who, when, how many comments, the text clipped, how it bears on the project.
nonisolated struct KBMomentRef: Decodable, Identifiable, Hashable, Sendable {
    var id: String
    var author: String
    var text: String
    var at: Date?
    var comments: Int
    var note: String?
    /// What the feed it is from is called, when the hub says (`KBMoments.feedName`).
    var feed: String?
}

/// One archived Moments post: the crawler's Markdown (text and every comment), and what the archive knows of it.
nonisolated struct KBMomentPost: Decodable, Sendable {
    var markdown: String
    var post: Meta?

    nonisolated struct Meta: Decodable, Sendable {
        /// What the feed it is from is called (the connector that keeps it says); nil or empty: Moments.
        var feed: String?
    }
}

/// What the company feed is called on a page: the archive's name for it, else Moments (no tenant's own name is built in).
nonisolated enum KBMoments {
    static var defaultName: String { String(localized: "Moments") }

    static func feedName(_ feed: String?) -> String {
        let f = feed?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return f.isEmpty ? defaultName : f
    }

    /// The name of the feed a project's posts are from: theirs when they all say the same, else Moments.
    static func feedName(_ moments: [KBMomentRef]) -> String {
        let names = Set(moments.map { feedName($0.feed) })
        return names.count == 1 ? names.first! : defaultName
    }
}

nonisolated struct KBDetailResponse: Decodable, Sendable {
    var detail: KBDetail
    var agents: [Session]?
    var matches: [KBMatch]?
    /// A project's place in the project tree (#12927): parent, the way down, children, milestones. Nil from a hub
    /// before it. The app reads it only; moving projects and editing milestones is on the web for now.
    var tree: KBProjectTree?
    /// A chat's Inbox items (#15356, docs/assistant/knowledge.md "A chat's Inbox items"); nil for none, and from a hub
    /// before them.
    var inbox: [KBInboxItem]?
}

/// A milestone flag on a project (#12927): a title, an optional target day (YYYY-MM-DD) and whether it was accepted
/// (open, accepted, not_accepted). Every field is optional so a hub's later additions or a damaged flag never fail
/// the list it is in.
nonisolated struct KBMilestone: Codable, Hashable, Sendable {
    var id: String?
    var title: String?
    var target: String?
    var state: String?
}

/// A project as a tree names it: its parent, the ones above it, the ones under it.
nonisolated struct KBProjectRef: Decodable, Hashable, Sendable {
    var id: String
    var name: String?
    var status: String?
    var children: Int?
}

/// `tree` of a project's detail (`GET /api/v1/kb/projects/{id}`, #12927).
nonisolated struct KBProjectTree: Decodable, Sendable {
    var parent: KBProjectRef?
    var ancestors: [KBProjectRef]?
    var children: [KBProjectRef]?
    var milestones: [KBMilestone]?
    var depth: Int?
}

/// An Inbox item a chat's page lists: what its row draws, and `messages`, the rows of the page's `recent` it holds, each
/// by its day and its lines as written there (the hub matched them on time, sender and text).
nonisolated struct KBInboxItem: Decodable, Identifiable, Hashable, Sendable {
    var id: String
    var title: String
    var tier: String?
    /// An open item's section (action, working, fyi, noise) as the Inbox has it.
    var status: String?
    var summary: String?
    var preview: String?
    var updatedAt: Date?
    var doneAt: Date?
    var messages: [Row]?

    /// A row of the chat's recent messages: its day ("2026-10-30") and its lines, "- 10:15 name: text" and any indented
    /// ones after it.
    nonisolated struct Row: Decodable, Hashable, Sendable {
        var day: String
        var line: String
    }

    enum CodingKeys: String, CodingKey {
        case id, title, tier, status, summary, preview, messages
        case updatedAt = "updated_at"
        case doneAt = "done_at"
    }
}

/// A change made in the app; tagging set here is kept over the automatic one.
nonisolated struct KBUpdate: Encodable, Sendable {
    var importance: String?
    var relevance: Int?
    var tags: [String]?
    var status: String?
    var projectKind: String?
    var note: String?
    var log: String?
    enum CodingKeys: String, CodingKey {
        case importance, relevance, tags, status, note, log
        case projectKind = "project_kind"
    }
}

/// One entry a search found (`/api/v1/kb-search?results=1`): the entry, how many lines matched, the first few.
nonisolated struct KBResult: Decodable, Identifiable, Hashable, Sendable {
    var entry: KBEntry
    var count: Int
    var hits: [KBHit]?
    var inName: Bool?
    var id: String { entry.kind + "/" + entry.id }
    enum CodingKeys: String, CodingKey {
        case entry, count, hits
        case inName = "in_name"
    }
}

/// A place in an entry matching the search it was opened from; a message comes with the ones around it.
nonisolated struct KBMatch: Decodable, Identifiable, Hashable, Sendable {
    var path: String
    var n: Int
    var line: String
    var day: String?
    var before: [String]?
    var after: [String]?
    var id: String { path + "#\(n)" }
    var hit: KBHit { KBHit(path: path, line: line, n: n) }
}

/// One day of a chat's archive.
nonisolated struct KBDay: Decodable, Sendable {
    struct Message: Decodable, Identifiable, Hashable, Sendable {
        var n: Int
        var text: String
        var id: Int { n }
    }
    var day: String
    var messages: [Message]
    var prev: String?
    var next: String?
}

/// An archived message, `14:03 Alice: text` (continuation lines after a newline): when, who, what.
nonisolated struct KBMessageLine: Hashable, Sendable {
    var time: String
    var sender: String
    var text: String

    init(_ raw: String) {
        let t = raw.hasPrefix("- ") ? String(raw.dropFirst(2)) : raw
        let parts = t.split(separator: " ", maxSplits: 1, omittingEmptySubsequences: false)
        if parts.count == 2, parts[0].count == 5, parts[0].dropFirst(2).first == ":", parts[0].first?.isNumber == true,
           let colon = parts[1].range(of: ": ") {
            time = String(parts[0])
            sender = String(parts[1][..<colon.lowerBound])
            text = String(parts[1][colon.upperBound...])
        } else {
            time = ""
            sender = ""
            text = t
        }
    }
}

nonisolated struct KBHit: Decodable, Hashable, Sendable {
    var path: String
    var line: String
    var n: Int?

    /// The entry a hit is in: `chats/<id>/summary.md` → (chats, id), `docs/<id>.json` → (docs, id); nil for anything else.
    var entry: (kind: KBKind, id: String)? {
        let parts = path.split(separator: "/", omittingEmptySubsequences: true).map(String.init)
        guard parts.count >= 2, let kind = KBKind(rawValue: parts[0]) else { return nil }
        let id = parts.count == 2 && parts[1].hasSuffix(".json") ? String(parts[1].dropLast(5)) : parts[1]
        return id.isEmpty ? nil : (kind, id)
    }

    /// The day of a message hit ("2026-10-30"), nil for the other files.
    var day: String? {
        guard path.contains("/messages/") else { return nil }
        return ((path as NSString).lastPathComponent as NSString).deletingPathExtension
    }

    /// Which file the line is from, said plainly.
    var source: String {
        let file = (path as NSString).lastPathComponent
        if let day { return String(localized: "Chat History · \(day)") }
        switch file {
        case "summary.md": return String(localized: "Profile")
        case "log.md": return String(localized: "Timeline [Knowledge View f1241a]")
        case "meta.json": return String(localized: "Info")
        default: return file.hasSuffix(".json") ? String(localized: "Info") : file
        }
    }

    /// The line without its markdown list marker, JSON key and quotes.
    var text: String {
        var t = line.trimmingCharacters(in: .whitespaces)
        for p in ["- ", "* ", "## ", "# "] where t.hasPrefix(p) { t = String(t.dropFirst(p.count)) }
        if t.hasPrefix("\""), let colon = t.range(of: "\": ") {
            t = String(t[colon.upperBound...])
        }
        t = t.trimmingCharacters(in: CharacterSet(charactersIn: " ,\""))
        return t
    }
}

/// A project's timeline line, `- 2026-10-30 23:16 · Agent · title · machine · what was said`: when, from where, what.
nonisolated struct KBLogLine: Hashable, Sendable {
    var when: String
    var source: String
    var text: String

    init(_ raw: String) {
        var t = raw.trimmingCharacters(in: .whitespaces)
        if t.hasPrefix("- ") { t = String(t.dropFirst(2)) }
        let parts = t.components(separatedBy: " · ")
        if parts.count >= 2, parts[0].first?.isNumber == true {
            when = parts[0]
            text = parts.last ?? ""
            source = parts.count > 2 ? parts[1..<(parts.count - 1)].joined(separator: " · ") : ""
        } else {
            when = ""
            source = ""
            text = t
        }
    }

    /// "2026-10-30 23:16" → "10-30 23:16" (the year only when it is not this one).
    func shortWhen(now: Date = .now) -> String {
        let year = String(Calendar.current.component(.year, from: now))
        return when.hasPrefix(year + "-") ? String(when.dropFirst(5)) : when
    }
}

/// A deleted project as the trash keeps it (docs/assistant/knowledge.md "Deleting a project"): the trash entry's id
/// is what restoring names.
nonisolated struct KBTrashEntry: Decodable, Sendable {
    var id: String
    var slug: String?
    var name: String?
}

extension APIClient {
    func kbList(_ kind: KBKind, tag: String? = nil) async throws -> KBList {
        try await get("/api/v1/kb/\(kind.rawValue)", query: tag.map { ["tag": $0] } ?? [:])
    }
    /// `query`: opened from a search, the places it is in come back as `matches`.
    func kbDetail(_ kind: String, id: String, query: String? = nil) async throws -> KBDetailResponse {
        try await get("/api/v1/kb/\(kind)/\(Self.kbID(id))", query: query.map { ["q": $0] } ?? [:])
    }
    func kbMoment(_ id: String) async throws -> KBMomentPost {
        try await get("/api/v1/kb/moments/\(Self.kbID(id))")
    }
    func kbDay(chat: String, day: String) async throws -> KBDay {
        try await get("/api/v1/kb/chats/\(Self.kbID(chat))/days/\(Self.kbID(day))")
    }
    func kbFind(_ q: String) async throws -> [KBResult] {
        struct R: Decodable { var results: [KBResult] }
        let r: R = try await get("/api/v1/kb-search", query: ["q": q, "results": "1"])
        return r.results
    }
    nonisolated static func kbID(_ id: String) -> String {
        id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(["/"])) ?? id
    }
    func kbUpdate(_ kind: String, id: String, _ u: KBUpdate) async throws {
        _ = try await request("PUT", "/api/v1/kb/\(kind)/\(id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(["/"])) ?? id)", body: u)
    }
    /// Deletes a project softly: it goes to the trash, from where `kbRestore` brings it back.
    func kbDeleteProject(_ id: String) async throws -> KBTrashEntry {
        let data = try await request("DELETE", "/api/v1/kb/projects/\(Self.kbID(id))")
        return try JSON.decoder.decode(KBTrashEntry.self, from: data)
    }
    /// 409 when a project with the same slug was made since.
    func kbRestore(_ trashID: String) async throws {
        _ = try await request("POST", "/api/v1/kb-trash/\(Self.kbID(trashID))/restore")
    }
    func kbSearch(_ q: String) async throws -> [KBHit] {
        struct R: Decodable { var hits: [KBHit] }
        let r: R = try await get("/api/v1/kb-search", query: ["q": q])
        return r.hits
    }
}
