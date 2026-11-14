import Foundation
import PersonaWire

/// An MCP server's request on an `elicitation` card (docs/protocol/protocol.md "MCP elicitation"): a form to fill in
/// (`requestedSchema`, a flat JSON Schema object) or a link to open (`url`).
nonisolated struct ElicitationRequest: Codable, Hashable, Sendable {
    /// "form" or "url".
    var mode: String?
    var message: String?
    var requestedSchema: JSONValue?
    var url: String?
    var elicitationID: String?
    /// The form's field keys in the order its server wrote them (`Elicitation.withFieldOrder`): the app's own, kept with
    /// the cards it caches; the hub never sends it.
    var fieldOrder: [String]?

    var isForm: Bool { mode == "form" }
    var isURL: Bool { mode == "url" }

    enum CodingKeys: String, CodingKey {
        case mode, message, url
        case requestedSchema = "requested_schema", elicitationID = "elicitation_id", fieldOrder = "app_field_order"
    }
}

/// The rules of an MCP form or link, as web/src/lib/elicitation.ts and Android's ElicitationForm / ElicitationUrl /
/// AnswerBuilder have them: a deliberately bounded JSON Schema subset, where anything unknown keeps the form from being
/// submitted here (the card says to answer it in the terminal), and a link is accepted only after its card opened that
/// exact HTTP(S) target.
nonisolated enum Elicitation {
    /// One field of a form.
    struct Field: Hashable, Sendable, Identifiable {
        var key: String
        var schema: [String: JSONValue]
        var required: Bool
        /// string, number, integer, boolean, or array (several of an enum's strings).
        var type: String
        var title: String
        var description: String?
        /// A password: never prefilled from a default, never kept in a draft, hidden in receipts.
        var secret: Bool
        /// The enum's values, of the field or (an array) of its items.
        var choices: [JSONValue]
        var choiceLabels: [String]
        var id: String { key }

        var format: String? { Elicitation.text(schema["format"]) }

        /// What a choice reads as: its `enumNames` entry, else the value itself.
        func label(_ choice: JSONValue) -> String {
            if let i = choices.firstIndex(of: choice), choiceLabels.indices.contains(i) { return choiceLabels[i] }
            return Elicitation.plain(choice)
        }
    }

    enum Problem: Hashable, Sendable {
        case required, invalid

        var text: String {
            switch self {
            case .required: String(localized: "Fill this in")
            case .invalid: String(localized: "Check what you entered and its format")
            }
        }
    }

    struct Validation: Sendable {
        /// What an accept sends: the fields that were filled in, numbers as numbers.
        var content: [String: JSONValue]
        var errors: [String: Problem]
        var supported: Bool
        var valid: Bool
    }

    enum Action: String, Sendable { case accept, decline, cancel }

    /// What a card has typed and opened so far, kept while it is pending (Android's RequestDraftJournal, the web's drafts):
    /// a card drawn again (folded and unfolded, the chat left and opened again) picks it up. Never a secret (`keepable`).
    struct Draft: Hashable, Sendable {
        var values: [String: JSONValue] = [:]
        /// The link this card opened.
        var opened: String?
    }

    private static let schemaKeys: Set<String> = ["type", "properties", "required", "title", "description", "additionalProperties", "$schema"]
    private static let fieldKeys: Set<String> = ["type", "title", "description", "default", "enum", "enumNames", "minLength", "maxLength", "format",
                                                 "minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf", "items", "minItems",
                                                 "maxItems", "uniqueItems", "writeOnly"]
    private static let formats: Set<String> = ["email", "uri", "date", "date-time", "password"]

    // MARK: The form

    /// The form's fields, in the server's order when it is known (`order`), else by key.
    static func fields(_ schema: JSONValue?, order: [String]? = nil) -> [Field] {
        guard let schema = schema?.object else { return [] }
        let required = (schema["required"]?.array ?? []).compactMap(text)
        let properties = schema["properties"]?.object ?? [:]
        return ordered(Array(properties.keys), by: order).compactMap { key in
            guard let value = properties[key]?.object else { return nil }
            let type = text(value["type"]) ?? ""
            let source = type == "array" ? (value["items"]?.object ?? value) : value
            return Field(key: key, schema: value, required: required.contains(key), type: type, title: text(value["title"]) ?? key,
                         description: text(value["description"]),
                         secret: value["writeOnly"] == .bool(true) || text(value["format"]) == "password",
                         choices: (source["enum"]?.array ?? []).filter(isPrimitive),
                         choiceLabels: (source["enumNames"]?.array ?? []).compactMap(text))
        }
    }

    /// What the form starts with: the schema's defaults, secrets excepted.
    static func defaults(_ schema: JSONValue?) -> [String: JSONValue] {
        var out: [String: JSONValue] = [:]
        for f in fields(schema) where !f.secret {
            if let value = f.schema["default"] { out[f.key] = value }
        }
        return out
    }

    /// Whether `schema` is inside the subset this app can draw and check.
    static func supported(_ schema: JSONValue?) -> Bool {
        guard let schema = schema?.object, text(schema["type"]) == "object", let properties = schema["properties"]?.object,
              schema.keys.allSatisfy(schemaKeys.contains) else { return false }
        if let required = schema["required"], required.array == nil { return false }
        let list = fields(.object(schema))
        guard properties.count == list.count else { return false }
        let keys = Set(list.map(\.key))
        for key in schema["required"]?.array ?? [] {
            guard let key = text(key), keys.contains(key) else { return false }
        }
        return list.allSatisfy { fieldSupported($0.schema) }
    }

    /// Checks the values against the schema. An absent value is a field not filled in; number fields hold their text.
    static func validate(_ schema: JSONValue?, values: [String: JSONValue]) -> Validation {
        let ok = supported(schema)
        var errors: [String: Problem] = [:]
        var content: [String: JSONValue] = [:]
        for f in fields(schema) {
            guard let value = values[f.key] else {
                if f.required { errors[f.key] = .required }
                continue
            }
            if let converted = convert(f, value) { content[f.key] = converted } else { errors[f.key] = .invalid }
        }
        return Validation(content: content, errors: errors, supported: ok, valid: ok && errors.isEmpty)
    }

    // MARK: The link

    /// The link to open: an absolute HTTP(S) URL with a host and no user info, else nil.
    static func urlTarget(_ value: String?) -> String? {
        guard let target = value?.trimmingCharacters(in: .whitespacesAndNewlines), !target.isEmpty,
              // a URL parser forgives https:///host and https:host; Android's URI does not, and neither does this
              matches(httpPrefix, target), let url = URL(string: target),
              let scheme = url.scheme?.lowercased(), scheme == "http" || scheme == "https",
              let host = url.host(), !host.isEmpty, url.user() == nil, url.password() == nil else { return nil }
        return target
    }

    static func urlHost(_ value: String?) -> String? {
        urlTarget(value).flatMap { URL(string: $0)?.host()?.lowercased() }
    }

    // MARK: Answering

    /// The resolution for an action on the card, or nil when it may not be sent yet: an accept needs a valid form or,
    /// for a link, that this card opened exactly that link (`opened`).
    static func resolution(_ ix: Interaction, _ action: Action, values: [String: JSONValue] = [:], opened: String? = nil) -> Resolution? {
        guard ix.kind == .elicitation else { return nil }
        guard action == .accept else { return Resolution(action: action.rawValue) }
        guard let request = ix.elicitation else { return nil }
        if request.isURL {
            guard let url = urlTarget(request.url), opened == url else { return nil }
            return Resolution(action: Action.accept.rawValue)
        }
        guard request.isForm else { return nil }
        let check = validate(request.requestedSchema, values: values)
        return check.valid ? Resolution(action: Action.accept.rawValue, content: check.content) : nil
    }

    /// Who asks, a line each: the subagent, and the MCP server with where it is configured (Android's requestSourceLabel).
    static func sourceLines(_ ix: Interaction) -> [String] {
        var out: [String] = []
        if let agent = trimmed(ix.agentType) { out.append(String(localized: "From subagent · \(agent)")) }
        if let name = trimmed(ix.mcpServerName) {
            let server = serverLabel(name)
            if let source = trimmed(ix.mcpConfigSource) {
                out.append(String(localized: "From MCP server \(server) (configured in \(source))"))
            } else {
                out.append(String(localized: "From MCP server \(server)"))
            }
        }
        return out
    }

    /// An MCP server's name as the chat shows it: Persona's own server (as a plugin too) is Persona.
    static func serverLabel(_ name: String) -> String {
        let server = name.hasPrefix("plugin_") ? String(name.dropFirst(7)) : name
        return server == "persona" || server == LegacyNames.product ? "Persona" : server
    }

    /// What a filled-in value reads as in a receipt: a secret only says it was entered.
    static func shown(_ f: Field, _ value: JSONValue) -> String {
        if f.secret { return String(localized: "(Entered)") }
        switch value {
        case .bool(let b): return b ? String(localized: "Yes") : String(localized: "No")
        case .array(let list): return list.map(f.label).joined(separator: String(localized: ", ", comment: "Separator between items in a list"))
        default: return f.label(value)
        }
    }

    /// A draft without its secrets.
    static func keepable(_ draft: Draft, schema: JSONValue?) -> Draft {
        let secret = Set(fields(schema).filter(\.secret).map(\.key))
        return Draft(values: draft.values.filter { !secret.contains($0.key) }, opened: draft.opened)
    }

    // MARK: The fields' order

    /// `ix` with its form's field order read off `data`, the JSON it came in: `JSONDecoder` keeps no key order (its
    /// dictionaries come back in another order each launch), and a form drawn out of its server's order reads wrong (a
    /// password before its user name).
    static func withFieldOrder(_ ix: Interaction, from data: Data) -> Interaction {
        withFieldOrders([ix], from: data)[0]
    }

    static func withFieldOrders(_ ixs: [Interaction], from data: Data) -> [Interaction] {
        guard ixs.contains(where: { $0.elicitation?.isForm == true }) else { return ixs }
        var scanner = OrderScanner(bytes: Array(data))
        guard let root = scanner.value(depth: 0) else { return ixs }
        var orders: [String: [String]] = [:]
        collect(root, into: &orders)
        return ixs.map { ix in
            guard let order = orders[ix.id], ix.elicitation != nil else { return ix }
            var ix = ix
            ix.elicitation?.fieldOrder = order
            return ix
        }
    }

    /// Every elicitation card in the tree, by id: its `requested_schema.properties` keys as written.
    private static func collect(_ node: OrderScanner.Node, into out: inout [String: [String]]) {
        switch node {
        case .object(let pairs):
            func field(_ pairs: [(String, OrderScanner.Node)], _ key: String) -> OrderScanner.Node? { pairs.first { $0.0 == key }?.1 }
            if case .string("elicitation")? = field(pairs, "kind"), case .string(let id)? = field(pairs, "id"),
               case .object(let request)? = field(pairs, "elicitation"), case .object(let schema)? = field(request, "requested_schema"),
               case .object(let properties)? = field(schema, "properties") {
                var seen = Set<String>()
                out[id] = properties.map { $0.0 }.filter { seen.insert($0).inserted }
            }
            for (_, value) in pairs { collect(value, into: &out) }
        case .array(let items):
            for item in items { collect(item, into: &out) }
        case .string, .other:
            break
        }
    }

    /// Just enough of a JSON parser to keep the order of an object's keys: objects, arrays and strings, everything else
    /// skipped. Nil for what is not JSON, or nested past what any card holds.
    private struct OrderScanner {
        indirect enum Node {
            case object([(String, Node)]), array([Node]), string(String), other
        }

        let bytes: [UInt8]
        var index = 0

        mutating func value(depth: Int) -> Node? {
            guard depth < 64 else { return nil }
            space()
            guard index < bytes.count else { return nil }
            switch bytes[index] {
            case UInt8(ascii: "{"):
                index += 1
                var pairs: [(String, Node)] = []
                space()
                if eat(UInt8(ascii: "}")) { return .object(pairs) }
                repeat {
                    space()
                    guard let key = string(), eat(UInt8(ascii: ":")), let member = value(depth: depth + 1) else { return nil }
                    pairs.append((key, member))
                } while eat(UInt8(ascii: ","))
                return eat(UInt8(ascii: "}")) ? .object(pairs) : nil
            case UInt8(ascii: "["):
                index += 1
                var items: [Node] = []
                space()
                if eat(UInt8(ascii: "]")) { return .array(items) }
                repeat {
                    guard let item = value(depth: depth + 1) else { return nil }
                    items.append(item)
                } while eat(UInt8(ascii: ","))
                return eat(UInt8(ascii: "]")) ? .array(items) : nil
            case UInt8(ascii: "\""):
                return string().map { .string($0) }
            default:
                // a number, true, false or null: up to the next delimiter
                let start = index
                while index < bytes.count, !Self.delimiters.contains(bytes[index]) { index += 1 }
                return index > start ? .other : nil
            }
        }

        private static let delimiters: Set<UInt8> = [UInt8(ascii: ","), UInt8(ascii: "}"), UInt8(ascii: "]"), 9, 10, 13, 32]

        private mutating func space() {
            while index < bytes.count, [9, 10, 13, 32].contains(bytes[index]) { index += 1 }
        }

        private mutating func eat(_ byte: UInt8) -> Bool {
            space()
            guard index < bytes.count, bytes[index] == byte else { return false }
            index += 1
            return true
        }

        /// A string token, unescaped by Foundation's own JSON parser.
        private mutating func string() -> String? {
            guard index < bytes.count, bytes[index] == UInt8(ascii: "\"") else { return nil }
            let start = index
            index += 1
            while index < bytes.count, bytes[index] != UInt8(ascii: "\"") {
                index += bytes[index] == UInt8(ascii: "\\") ? 2 : 1
            }
            guard index < bytes.count else { return nil }
            index += 1
            return (try? JSONSerialization.jsonObject(with: Data(bytes[start..<index]), options: .fragmentsAllowed)) as? String
        }
    }

    // MARK: Helpers

    private static let httpPrefix = try! NSRegularExpression(pattern: #"^https?://[^/\\?#]"#, options: .caseInsensitive)
    private static let email = try! NSRegularExpression(pattern: #"^[^\s@]+@[^\s@]+\.[^\s@]+$"#)
    private static let date = try! NSRegularExpression(pattern: #"^(\d{4})-(\d{2})-(\d{2})$"#)
    private static let dateTime = try! NSRegularExpression(
        pattern: #"^(\d{4}-\d{2}-\d{2})T(\d{2}):(\d{2}):(\d{2})(\.\d+)?(Z|[+-](\d{2}):(\d{2}))$"#, options: .caseInsensitive)
    private static let numberText = try! NSRegularExpression(pattern: #"^[+-]?(\d+(\.\d*)?|\.\d+)([eE][+-]?\d+)?$"#)

    /// The keys in the server's order (those it names), then the rest by key.
    private static func ordered(_ keys: [String], by order: [String]?) -> [String] {
        let known = Set(keys)
        var seen = Set<String>()
        let first = (order ?? []).filter { known.contains($0) && seen.insert($0).inserted }
        return first + keys.filter { !seen.contains($0) }.sorted()
    }

    private static func fieldSupported(_ s: [String: JSONValue]) -> Bool {
        guard s.keys.allSatisfy(fieldKeys.contains) else { return false }
        for key in ["minLength", "maxLength", "minItems", "maxItems"] where s[key] != nil {
            guard let n = number(s[key]), n >= 0, n == n.rounded() else { return false }
        }
        for key in ["minimum", "maximum", "exclusiveMinimum", "exclusiveMaximum", "multipleOf"] where s[key] != nil {
            guard number(s[key]) != nil else { return false }
        }
        for key in ["writeOnly", "uniqueItems"] where s[key] != nil {
            guard case .bool? = s[key] else { return false }
        }
        if let format = s["format"] {
            guard let format = text(format), formats.contains(format) else { return false }
        }
        if let choices = s["enum"] {
            guard let list = choices.array, !list.isEmpty, list.allSatisfy(isPrimitive) else { return false }
        }
        switch text(s["type"]) {
        case "string", "number", "integer", "boolean":
            return true
        case "array":
            guard let items = s["items"]?.object, text(items["type"]) == "string", items["enum"]?.array != nil else { return false }
            return fieldSupported(items)
        default:
            return false
        }
    }

    /// The value as it goes to the hub, or nil when it does not fit the field.
    private static func convert(_ f: Field, _ value: JSONValue) -> JSONValue? {
        let s = f.schema
        let out: JSONValue
        switch f.type {
        case "string":
            guard case .string(let v) = value else { return nil }
            let length = Double(v.unicodeScalars.count)
            if let min = number(s["minLength"]), length < min { return nil }
            if let max = number(s["maxLength"]), length > max { return nil }
            switch f.format {
            case "email": guard matches(email, v) else { return nil }
            case "uri": guard absoluteURI(v) else { return nil }
            case "date": guard validDate(v) else { return nil }
            case "date-time": guard validDateTime(v) else { return nil }
            default: break
            }
            out = value
        case "number", "integer":
            let n: Double
            switch value {
            case .number(let d):
                n = d
            case .string(let typed):
                let t = typed.trimmingCharacters(in: .whitespacesAndNewlines)
                guard matches(numberText, t), let d = Double(t) else { return nil }
                n = d
            default:
                return nil
            }
            guard n.isFinite, f.type != "integer" || n == n.rounded() else { return nil }
            if let m = number(s["minimum"]), n < m { return nil }
            if let m = number(s["maximum"]), n > m { return nil }
            if let m = number(s["exclusiveMinimum"]), n <= m { return nil }
            if let m = number(s["exclusiveMaximum"]), n >= m { return nil }
            if let m = number(s["multipleOf"]), !multiple(n, of: m) { return nil }
            out = .number(n)
        case "boolean":
            guard case .bool = value else { return nil }
            out = value
        case "array":
            guard case .array(let list) = value, list.allSatisfy(f.choices.contains), Set(list).count == list.count else { return nil }
            if let min = number(s["minItems"]), Double(list.count) < min { return nil }
            if let max = number(s["maxItems"]), Double(list.count) > max { return nil }
            out = value
        default:
            return nil
        }
        if let choices = s["enum"]?.array, !choices.contains(out) { return nil }
        return out
    }

    /// n is a whole multiple of m, allowing for binary floating point (0.3 is a multiple of 0.1).
    private static func multiple(_ n: Double, of m: Double) -> Bool {
        guard m > 0 else { return false }
        let q = n / m
        return abs(q - q.rounded()) < 1e-9 * max(1, abs(q))
    }

    private static func validDate(_ text: String) -> Bool {
        guard let parts = groups(date, text), let y = Int(parts[0]), let m = Int(parts[1]), let d = Int(parts[2]), (1...12).contains(m) else { return false }
        let leap = y % 4 == 0 && (y % 100 != 0 || y % 400 == 0)
        let days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][m - 1]
        return (1...days).contains(d)
    }

    private static func validDateTime(_ text: String) -> Bool {
        guard let parts = groups(dateTime, text), validDate(parts[0]),
              let h = Int(parts[1]), let min = Int(parts[2]), let sec = Int(parts[3]), h < 24, min < 60, sec < 60 else { return false }
        if parts[5].uppercased() == "Z" { return true }
        guard let oh = Int(parts[6]), let om = Int(parts[7]) else { return false }
        return oh < 24 && om < 60
    }

    private static func absoluteURI(_ text: String) -> Bool {
        guard let scheme = URL(string: text)?.scheme else { return false }
        return !scheme.isEmpty
    }

    private static func matches(_ re: NSRegularExpression, _ text: String) -> Bool {
        re.firstMatch(in: text, range: NSRange(text.startIndex..., in: text)) != nil
    }

    /// The capture groups of a whole match ("" for a group that took part in nothing), or nil without a match.
    private static func groups(_ re: NSRegularExpression, _ text: String) -> [String]? {
        guard let m = re.firstMatch(in: text, range: NSRange(text.startIndex..., in: text)) else { return nil }
        return (1..<m.numberOfRanges).map { i in Range(m.range(at: i), in: text).map { String(text[$0]) } ?? "" }
    }

    fileprivate static func text(_ value: JSONValue?) -> String? {
        if case .string(let s)? = value { s } else { nil }
    }

    private static func number(_ value: JSONValue?) -> Double? {
        if case .number(let n)? = value, n.isFinite { n } else { nil }
    }

    private static func isPrimitive(_ value: JSONValue) -> Bool {
        switch value {
        case .string, .number, .bool: true
        default: false
        }
    }

    private static func trimmed(_ value: String?) -> String? {
        guard let t = value?.trimmingCharacters(in: .whitespacesAndNewlines), !t.isEmpty else { return nil }
        return t
    }

    /// A primitive as text: whole numbers without a decimal point (3, not 3.0).
    fileprivate static func plain(_ value: JSONValue) -> String {
        switch value {
        case .string(let s): return s
        case .bool(let b): return b ? "true" : "false"
        case .number(let n): return abs(n) < 1e15 && n == n.rounded() ? String(Int64(n)) : String(n)
        default: return ""
        }
    }
}
