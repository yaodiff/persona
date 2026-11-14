import Foundation
import PersonaWire

/// The knowledge base's selection (docs/assistant/knowledge.md "Selection"): the one document that decides what the hub
/// reads and stores from chats and documents, and for how many days (`GET` / `PUT /api/v1/kb/selection`, the whole
/// document each way). A per-item setting comes first; then the first priority level with a matching rule decides.
/// Optional values are left out when nil, which the hub reads as null.
nonisolated struct KBSelection: Codable, Hashable, Sendable {
    static let supportedVersion = 1
    /// Days of a rule, a per-item setting or a history import: 1…36500, or nil for the hub's retention setting.
    static let dayRange = 1...36500
    /// The messages around each of the owner's own that the mine rule keeps; nil is the hub's 50.
    static let contextRange = 0...200

    var version: Int
    var messages: KBSelectionRules
    var docs: KBSelectionRules
    var items: [KBSelectionItem]?
    /// How far back each source's history import goes. This page does not edit it; it is sent back as read.
    var history: [String: KBSelectionHistory]?

    /// The rules for chat messages or the ones for documents.
    nonisolated enum Part: String, CaseIterable, Sendable {
        case messages, docs

        /// Every rule of the part, in the hub's default order.
        var ruleNames: [String] {
            switch self {
            case .messages: ["mentions", "mine", "dm", "group", "bot", "official", "muted", "collapsed", "done"]
            case .docs: ["mentions", "comment_mentions", "authored", "viewed"]
            }
        }
    }

    subscript(part: Part) -> KBSelectionRules {
        get { part == .messages ? messages : docs }
        set { if part == .messages { messages = newValue } else { docs = newValue } }
    }

    /// The hub's answer, refused when this version cannot send it back whole: another version, or a field these
    /// types do not know (re-encoding would drop it, and a PUT replaces the whole document).
    static func read(_ data: Data) throws -> KBSelection {
        let selection: KBSelection
        do {
            selection = try JSON.decoder.decode(KBSelection.self, from: data)
        } catch {
            throw KBSelectionUnsupported()
        }
        let raw = try JSON.decoder.decode(JSONValue.self, from: data)
        let back = try JSON.decoder.decode(JSONValue.self, from: JSON.encoder.encode(selection))
        guard raw.hasKnownSelectionFields, selection.version == supportedVersion, raw.withoutNulls == back.withoutNulls else {
            throw KBSelectionUnsupported()
        }
        return selection
    }

    /// What the hub's validation accepts (`kb.Selection.Validate`): checked before every save, so a refused document
    /// is caught here with a sentence the user can act on.
    var isValid: Bool {
        guard version == Self.supportedVersion else { return false }
        for part in Part.allCases {
            let names = part.ruleNames
            let rules = self[part]
            let flat = rules.order.flatMap { $0 }
            guard !rules.order.isEmpty, !rules.order.contains(where: \.isEmpty),
                  flat.count == names.count, Set(flat) == Set(names), Set(rules.rules.keys) == Set(names) else { return false }
            for (name, rule) in rules.rules {
                guard Self.validDays(rule.days), Self.validDays(rule.attachmentDays) else { return false }
                if part == .docs, rule.attachmentDays != nil { return false }
                if rule.includeAll != nil, part != .messages || name != "mentions" { return false }
                if let context = rule.context {
                    guard part == .messages, name == "mine", Self.contextRange.contains(context) else { return false }
                }
            }
        }
        var seen = Set<String>()
        for item in items ?? [] {
            guard !item.source.isEmpty, !item.id.isEmpty, item.read || !item.store, Self.validDays(item.days),
                  seen.insert(item.key).inserted else { return false }
        }
        return (history ?? [:]).allSatisfy { !$0.key.isEmpty && Self.validDays($0.value.backDays) }
    }

    static func validDays(_ days: Int?) -> Bool { days.map(dayRange.contains) ?? true }

    // MARK: Priority levels

    /// The level a rule is in (0 is the highest priority), nil when the order does not name it.
    func level(of rule: String, in part: Part) -> Int? {
        self[part].order.firstIndex { $0.contains(rule) }
    }

    /// Swaps the level holding `rule` with its neighbour: `delta` -1 raises its priority, 1 lowers it.
    mutating func moveLevel(of rule: String, in part: Part, by delta: Int) {
        guard let from = level(of: rule, in: part) else { return }
        let to = from + delta
        guard self[part].order.indices.contains(to) else { return }
        self[part].order.swapAt(from, to)
    }

    /// Moves `rule` into the level above it (`joinPrevious`), or out of a shared level into one of its own right
    /// below. Nothing when there is no level above, or the rule is already alone.
    mutating func regroup(_ rule: String, in part: Part, joinPrevious: Bool) {
        var order = self[part].order
        guard let i = order.firstIndex(where: { $0.contains(rule) }) else { return }
        if joinPrevious ? (i == 0) : (order[i].count == 1) { return }
        order[i].removeAll { $0 == rule }
        if joinPrevious { order[i - 1].append(rule) } else { order.insert([rule], at: i + 1) }
        self[part].order = order.filter { !$0.isEmpty }
    }

    // MARK: Per-item settings

    func item(key: String) -> KBSelectionItem? { items?.first { $0.key == key } }

    /// Puts a per-item setting in, replacing the one of the same source, kind and id.
    mutating func setItem(_ item: KBSelectionItem) {
        var list = items ?? []
        if let i = list.firstIndex(where: { $0.key == item.key }) { list[i] = item } else { list.append(item) }
        items = list
    }

    mutating func removeItem(key: String) {
        items?.removeAll { $0.key == key }
    }
}

/// The hub's selection document cannot be edited by this version of the app.
nonisolated struct KBSelectionUnsupported: Error, Sendable {}

nonisolated struct KBSelectionRules: Codable, Hashable, Sendable {
    /// The levels, highest priority first; every rule is in exactly one.
    var order: [[String]]
    var rules: [String: KBSelectionRule]
}

/// One rule. `attachmentDays` only on message rules (the hub refuses it on a document rule), `includeAll` only on
/// mentions, `context` only on mine.
nonisolated struct KBSelectionRule: Codable, Hashable, Sendable {
    var on: Bool
    var days: Int?
    var attachmentDays: Int?
    var includeAll: Bool?
    var context: Int?

    enum CodingKeys: String, CodingKey {
        case on, days, context
        case attachmentDays = "attachment_days"
        case includeAll = "include_all"
    }
}

/// The setting of one chat or document, before every rule.
nonisolated struct KBSelectionItem: Codable, Hashable, Sendable {
    nonisolated enum Kind: String, Codable, Sendable { case chat, doc }

    /// What the hub does with the item: nothing at all, what the rules say (a chat: only the owner's own messages
    /// and mentions), or everything.
    nonisolated enum Mode: String, CaseIterable, Sendable { case skip, rules, all }

    var source: String
    var kind: Kind
    /// The source's stable id: a chat id, a document token.
    var id: String
    var name: String?
    var read: Bool
    var store: Bool
    var days: Int?

    /// Its identity in the document: one setting per source, kind and id.
    var key: String { [source, kind.rawValue, id].joined(separator: "\u{1F}") }

    var mode: Mode {
        get { !read ? .skip : store ? .all : .rules }
        set {
            read = newValue != .skip
            store = newValue == .all
            if newValue != .all { days = nil } // days only count for an item stored whole
        }
    }
}

nonisolated struct KBSelectionHistory: Codable, Hashable, Sendable {
    var backDays: Int?
    enum CodingKeys: String, CodingKey { case backDays = "back_days" }
}

private extension JSONValue {
    /// Unknown members stay significant even when their value is null: a later PUT must never silently delete them.
    nonisolated var hasKnownSelectionFields: Bool {
        guard hasOnly(["version", "messages", "docs", "items", "history"]) else { return false }
        for part in ["messages", "docs"] {
            if let rules = self[part], rules != .null, !rules.hasKnownRuleContainerFields { return false }
        }
        if let items = self["items"], items != .null {
            guard case .array(let list) = items, list.allSatisfy({ $0.hasKnownItemFields }) else { return false }
        }
        if let history = self["history"], history != .null {
            guard case .object(let entries) = history, entries.values.allSatisfy({ $0.hasKnownHistoryFields }) else { return false }
        }
        return true
    }

    nonisolated var hasKnownRuleContainerFields: Bool {
        guard hasOnly(["order", "rules"]) else { return false }
        guard let rawRules = self["rules"], rawRules != .null else { return true }
        guard case .object(let rules) = rawRules else { return false }
        return rules.values.allSatisfy { $0.hasKnownRuleFields }
    }

    nonisolated var hasKnownRuleFields: Bool {
        hasOnly(["on", "days", "attachment_days", "include_all", "context"])
    }

    nonisolated var hasKnownItemFields: Bool {
        hasOnly(["source", "kind", "id", "name", "read", "store", "days"])
    }

    nonisolated var hasKnownHistoryFields: Bool {
        hasOnly(["back_days"])
    }

    nonisolated func hasOnly(_ keys: Set<String>) -> Bool {
        guard case .object(let object) = self else { return false }
        return Set(object.keys).isSubset(of: keys)
    }

    /// Without null members, so a field left out compares equal to one sent as null.
    nonisolated var withoutNulls: JSONValue {
        switch self {
        case .object(let o): .object(o.compactMapValues { $0 == .null ? nil : $0.withoutNulls })
        case .array(let a): .array(a.map(\.withoutNulls))
        default: self
        }
    }
}

extension APIClient {
    /// 503 while the manager cannot read it; `KBSelectionUnsupported` when this version cannot edit what came back.
    func kbSelection() async throws -> KBSelection {
        try KBSelection.read(try await request("GET", "/api/v1/kb/selection"))
    }

    /// Replaces the whole document; 400 when the hub refuses it, 403 from a read-only device.
    func setKBSelection(_ selection: KBSelection) async throws -> KBSelection {
        try KBSelection.read(try await request("PUT", "/api/v1/kb/selection", body: selection))
    }
}
