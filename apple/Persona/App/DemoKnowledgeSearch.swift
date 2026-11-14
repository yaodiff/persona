import Foundation
import PersonaWire

/// Searches only bundled demo entries; never reads files or calls the hub.
nonisolated enum DemoKnowledgeSearch {
    static let resultLimit = 20
    static let hitLimit = 4

    static func answer(_ query: String, kb: JSONValue?) -> JSONValue {
        let query = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !query.isEmpty, query.count <= 200 else { return ["hits": .array([]), "results": .array([])] }
        var results: [JSONValue] = []
        var allHits: [JSONValue] = []
        for kind in KBKind.allCases {
            for entry in (kb?[kind.rawValue]?.array ?? []).prefix(200) {
                guard let id = entry["id"]?.string, !id.isEmpty,
                      let name = entry["name"]?.string, !name.isEmpty else { continue }
                let summary = entry["one_line"]?.string ?? ""
                let tags = entry["tags"]?.array?.compactMap(\.string).joined(separator: " · ") ?? ""
                let fields = [(name, "meta.json", 1), (summary, "summary.md", 1), (tags, "meta.json", 2)]
                let hits: [JSONValue] = fields.compactMap { field in
                    let (text, file, line) = field
                    guard text.range(of: query, options: [.caseInsensitive, .diacriticInsensitive]) != nil else { return nil }
                    return ["path": .string("\(kind.rawValue)/\(id)/\(file)"), "line": .string(text), "n": .number(Double(line))]
                }
                guard !hits.isEmpty else { continue }
                let visible = Array(hits.prefix(hitLimit))
                results.append(["entry": entry, "count": .number(Double(hits.count)), "hits": .array(visible),
                                "in_name": .bool(name.range(of: query, options: [.caseInsensitive, .diacriticInsensitive]) != nil)])
                allHits.append(contentsOf: visible)
                if results.count == resultLimit { return ["hits": .array(allHits), "results": .array(results)] }
            }
        }
        return ["hits": .array(allHits), "results": .array(results)]
    }
}
