import Foundation
import PersonaWire

// Low-distraction mode's held pushes. Wire records stay separate from the rows so an invalid timestamp
// cannot hide valid siblings, and an appended `also` title does not change a row's identity.
nonisolated struct PushIntercept: Codable, Equatable, Sendable {
    var at: String
    var level: String
    var reason: String
    var summary: String?
    var title: String?
    var body: String?
    var relatedKey: String?
    var manager: String?
    var also: [String]?
    var todoID: String?
    var machineID: String?
    var sessionID: String?
    var group: String?
    var url: String?

    enum CodingKeys: String, CodingKey {
        case at, level, reason, summary, title, body, manager, also, group, url
        case relatedKey = "related_key"
        case todoID = "todo_id"
        case machineID = "machine_id"
        case sessionID = "session_id"
    }
}

nonisolated struct LnModeResponse: Decodable, Sendable { var enabled: Bool }
nonisolated struct PushInterceptsResponse: Decodable, Sendable {
    var intercepts: [PushIntercept]
    enum CodingKeys: String, CodingKey { case intercepts }
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        intercepts = ((try? c.decodeIfPresent([Lossy<PushIntercept>].self, forKey: .intercepts)) ?? nil)?.compactMap(\.value) ?? []
    }
}

nonisolated enum PushInterceptDestination: Equatable, Sendable {
    case todo(String)
    case session(group: String, machineID: String, sessionID: String)
    case url(URL)
}

nonisolated struct PushInterceptItem: Identifiable, Equatable, Sendable {
    var id: String
    var date: Date
    var record: PushIntercept
    var summaries: [String]
}

nonisolated struct PushInterceptGroups: Equatable, Sendable {
    var p1: [PushInterceptItem] = []
    var p2: [PushInterceptItem] = []
    var total: Int { p1.count + p2.count }
}

nonisolated enum PushInterceptsLogic {
    static func oneLine(_ value: String?) -> String {
        (value ?? "").components(separatedBy: .whitespacesAndNewlines).filter { !$0.isEmpty }.joined(separator: " ")
    }

    static func summaries(_ record: PushIntercept) -> [String] {
        let primary = [record.summary, record.title, record.body].lazy.map(oneLine).first { !$0.isEmpty } ?? ""
        let merged = (record.also ?? []).map(oneLine).filter { !$0.isEmpty }
        let all = primary.isEmpty ? merged : [primary] + merged
        return all.isEmpty ? [""] : all
    }

    static func groups(_ response: PushInterceptsResponse, now: Date, calendar: Calendar) -> PushInterceptGroups {
        var occurrences: [String: Int] = [:]
        var out = PushInterceptGroups()
        for record in response.intercepts {
            guard record.level == "P1" || record.level == "P2", !record.reason.isEmpty,
                  let date = JSON.parseDate(record.at), calendar.isDate(date, inSameDayAs: now) else { continue }
            let base = [record.at, record.level, record.reason, record.relatedKey ?? "", record.manager ?? ""].joined(separator: "\u{1e}")
            let occurrence = occurrences[base] ?? 0
            occurrences[base] = occurrence + 1
            let item = PushInterceptItem(id: occurrence == 0 ? base : "\(base)#\(occurrence)", date: date,
                                         record: record, summaries: summaries(record))
            if record.level == "P1" { out.p1.append(item) } else { out.p2.append(item) }
        }
        return out
    }

    private static func httpURL(_ value: String?) -> URL? {
        guard let value, let url = URL(string: value), let scheme = url.scheme?.lowercased(),
              scheme == "https" || scheme == "http", url.host != nil else { return nil }
        return url
    }

    /// Only typed destinations and two bounded legacy forms are followed; everything else stays read-only.
    static func destination(_ record: PushIntercept, knownTodoIDs: Set<String>) -> PushInterceptDestination? {
        if let id = record.todoID?.trimmingCharacters(in: .whitespacesAndNewlines), knownTodoIDs.contains(id) { return .todo(id) }
        if let machine = record.machineID?.trimmingCharacters(in: .whitespacesAndNewlines),
           let session = record.sessionID?.trimmingCharacters(in: .whitespacesAndNewlines), !machine.isEmpty, !session.isEmpty {
            return .session(group: record.group ?? "", machineID: machine, sessionID: session)
        }
        if let url = httpURL(record.url) { return .url(url) }
        let related = (record.relatedKey ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        if related.hasPrefix("todo:") {
            let id = related.dropFirst("todo:".count).split(separator: "@", maxSplits: 1).map(String.init).first ?? ""
            if knownTodoIDs.contains(id) { return .todo(id) }
        }
        if let url = httpURL(related) { return .url(url) }
        return nil
    }
}

extension APIClient {
    func pushLnMode() async throws -> LnModeResponse {
        try await getDecodedOffMain("/api/v1/push/ln-mode")
    }

    func pushIntercepts() async throws -> PushInterceptsResponse {
        try await getDecodedOffMain("/api/v1/push/intercepts")
    }
}
