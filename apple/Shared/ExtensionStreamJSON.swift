import Foundation
import PersonaWire

/// Original stream JSON, not a second wire protocol. Numeric tokens and property presence survive
/// staged reduction; generated DTOs are only a detached projection for the native renderer.
nonisolated indirect enum ExtensionStreamJSON: Equatable, Sendable {
    case null, bool(Bool), number(String), string(String), array([Self]), object([String: Self])

    enum Failure: Error { case malformed, bounds, schema, topology, version, unsupported }
    var object: [String: Self]? { if case .object(let v) = self { v } else { nil } }
    var array: [Self]? { if case .array(let v) = self { v } else { nil } }
    var string: String? { if case .string(let v) = self { v } else { nil } }
    var decimal: Decimal? { if case .number(let v) = self { Decimal(string: v, locale: Locale(identifier: "en_US_POSIX")) } else { nil } }
    struct Numeric {
        let negative: Bool
        let digits: [UInt8]
        let scale: Int
        var integer: Bool { digits.isEmpty || scale >= 0 }
        func compare(_ other: Self) -> Int {
            if digits.isEmpty && other.digits.isEmpty { return 0 }
            if negative != other.negative { return negative ? -1 : 1 }
            let direction = negative ? -1 : 1
            if digits.isEmpty { return -direction }
            if other.digits.isEmpty { return direction }
            let magnitude = digits.count + scale, otherMagnitude = other.digits.count + other.scale
            if magnitude != otherMagnitude { return (magnitude < otherMagnitude ? -1 : 1) * direction }
            for i in 0..<max(digits.count, other.digits.count) {
                let a = i < digits.count ? digits[i] : 48, b = i < other.digits.count ? other.digits[i] : 48
                if a != b { return (a < b ? -1 : 1) * direction }
            }
            return 0
        }
    }
    var numeric: Numeric? {
        guard case .number(let token) = self else { return nil }
        let parts = token.lowercased().split(separator: "e", omittingEmptySubsequences: false)
        let decimal = parts[0].split(separator: ".", omittingEmptySubsequences: false)
        let coefficient = Array(decimal.joined().utf8).filter { $0 != 45 }
        guard let first = coefficient.firstIndex(where: { $0 != 48 }),
              let last = coefficient.lastIndex(where: { $0 != 48 }) else { return Numeric(negative: false, digits: [], scale: 0) }
        let exponent = parts.count == 1 ? 0 : (Int(parts[1]) ?? (parts[1].first == "-" ? -2_097_152 : 2_097_152))
        // Input is byte-bounded; larger exponents cannot cancel against its fractional digits.
        let boundedExponent = min(max(exponent, -2_097_152), 2_097_152)
        let scale = boundedExponent - (decimal.count == 2 ? decimal[1].utf8.count : 0) + coefficient.count - last - 1
        return Numeric(negative: token.first == "-", digits: Array(coefficient[first...last]), scale: scale)
    }
    subscript(_ key: String) -> Self? { object?[key] }
    var data: Data {
        switch self {
        case .null: Data("null".utf8)
        case .bool(let v): Data((v ? "true" : "false").utf8)
        case .number(let v): Data(v.utf8)
        case .string(let v): (try? JSONEncoder().encode(v)) ?? Data()
        case .array(let v): Data("[".utf8) + v.enumerated().reduce(into: Data()) { out, pair in
            if pair.offset > 0 { out += Data(",".utf8) }; out += pair.element.data
        } + Data("]".utf8)
        case .object(let v): Data("{".utf8) + v.keys.sorted().enumerated().reduce(into: Data()) { out, pair in
            if pair.offset > 0 { out += Data(",".utf8) }
            out += Self.string(pair.element).data + Data(":".utf8) + v[pair.element]!.data
        } + Data("}".utf8)
        }
    }

    static func parse(_ data: Data, limit: Int = 1_048_576) throws -> Self {
        guard data.count <= limit, String(data: data, encoding: .utf8) != nil else { throw Failure.bounds }
        var parser = Parser(bytes: Array(data))
        let value = try parser.value(depth: 0)
        parser.space()
        guard parser.index == parser.bytes.count else { throw Failure.malformed }
        return value
    }

    private struct Parser {
        let bytes: [UInt8]
        var index = 0
        mutating func space() { while index < bytes.count && [9, 10, 13, 32].contains(bytes[index]) { index += 1 } }
        mutating func eat(_ b: UInt8) -> Bool {
            space(); guard index < bytes.count, bytes[index] == b else { return false }; index += 1; return true
        }
        mutating func text() throws -> String {
            space(); let start = index
            guard eat(34) else { throw Failure.malformed }
            while index < bytes.count {
                let b = bytes[index]; index += 1
                if b == 34 { return try JSONDecoder().decode(String.self, from: Data(bytes[start..<index])) }
                if b == 92 { guard index < bytes.count else { throw Failure.malformed }; index += 1 }
            }
            throw Failure.malformed
        }
        mutating func value(depth: Int) throws -> ExtensionStreamJSON {
            space(); guard depth <= 64, index < bytes.count else { throw Failure.bounds }
            switch bytes[index] {
            case 34: return .string(try text())
            case 123:
                index += 1; var out: [String: ExtensionStreamJSON] = [:]
                if eat(125) { return .object(out) }
                repeat {
                    let key = try text()
                    guard out[key] == nil, eat(58) else { throw Failure.malformed }
                    out[key] = try value(depth: depth + 1)
                    if eat(125) { return .object(out) }
                    guard eat(44) else { throw Failure.malformed }
                } while true
            case 91:
                index += 1; var out: [ExtensionStreamJSON] = []
                if eat(93) { return .array(out) }
                repeat {
                    out.append(try value(depth: depth + 1))
                    if eat(93) { return .array(out) }
                    guard eat(44) else { throw Failure.malformed }
                } while true
            default:
                let start = index
                while index < bytes.count && ![9, 10, 13, 32, 44, 93, 125].contains(bytes[index]) { index += 1 }
                let token = String(decoding: bytes[start..<index], as: UTF8.self)
                switch token {
                case "null": return .null
                case "true": return .bool(true)
                case "false": return .bool(false)
                default:
                    guard let number = try? JSONDecoder().decode(Double.self, from: Data(token.utf8)), number.isFinite else {
                        throw Failure.malformed
                    }
                    return .number(token)
                }
            }
        }
    }
}

/// The small JSON Schema vocabulary used by the canonical extension schemas. Constraints are
/// generated, not copied into a competing Swift component/request model.
nonisolated enum ExtensionStreamSchema {
    private static let documents = try! ExtensionStreamJSON.parse(Data(ExtensionWireSchemas.json.utf8), limit: 4_194_304)

    static func check(_ value: ExtensionStreamJSON, definition: String, file: String = "stream.schema.json") throws {
        guard let schema = documents[file]?["$defs"]?[definition], valid(value, schema: schema, file: file, depth: 0) else {
            throw ExtensionStreamJSON.Failure.schema
        }
    }

    private static func valid(_ value: ExtensionStreamJSON, schema: ExtensionStreamJSON, file: String, depth: Int) -> Bool {
        guard depth < 96 else { return false }
        if schema == .bool(false) { return false }
        if schema == .bool(true) { return true }
        guard let s = schema.object else { return false }
        if let ref = s["$ref"]?.string {
            let parts = ref.components(separatedBy: "#/$defs/")
            guard parts.count == 2 else { return false }
            let targetFile = parts[0].isEmpty ? file : parts[0]
            guard let target = documents[targetFile]?["$defs"]?[parts[1]], valid(value, schema: target, file: targetFile, depth: depth + 1) else { return false }
        }
        for (name, count) in [("allOf", -1), ("oneOf", 1), ("anyOf", 0)] {
            if let variants = s[name]?.array {
                let matches = variants.filter { valid(value, schema: $0, file: file, depth: depth + 1) }.count
                if count == -1 ? matches != variants.count : (count == 1 ? matches != 1 : matches == 0) { return false }
            }
        }
        if let constant = s["const"], !equal(value, constant) { return false }
        if let choices = s["enum"]?.array, !choices.contains(where: { equal(value, $0) }) { return false }
        if let type = s["type"] {
            let types = type.array?.compactMap(\.string) ?? type.string.map { [$0] } ?? []
            if !types.contains(where: { matches(value, type: $0) }) { return false }
        }
        if let n = value.numeric {
            if let min = s["minimum"]?.numeric, n.compare(min) < 0 { return false }
            if let max = s["maximum"]?.numeric, n.compare(max) > 0 { return false }
        }
        if let text = value.string {
            let length = text.unicodeScalars.count
            if let min = s["minLength"]?.decimal, Decimal(length) < min { return false }
            if let max = s["maxLength"]?.decimal, Decimal(length) > max { return false }
            if let pattern = s["pattern"]?.string, text.range(of: pattern, options: .regularExpression) == nil { return false }
        }
        if let array = value.array {
            if let min = s["minItems"]?.decimal, Decimal(array.count) < min { return false }
            if let max = s["maxItems"]?.decimal, Decimal(array.count) > max { return false }
            if s["uniqueItems"] == .bool(true) {
                for i in array.indices where array[..<i].contains(where: { equal($0, array[i]) }) { return false }
            }
            if let item = s["items"], !array.allSatisfy({ valid($0, schema: item, file: file, depth: depth + 1) }) { return false }
        }
        if let object = value.object {
            if let min = s["minProperties"]?.decimal, Decimal(object.count) < min { return false }
            if let max = s["maxProperties"]?.decimal, Decimal(object.count) > max { return false }
            if let required = s["required"]?.array, !required.allSatisfy({ $0.string.map { object[$0] != nil } == true }) { return false }
            let properties = s["properties"]?.object ?? [:]
            for (key, member) in object {
                if let names = s["propertyNames"], !valid(.string(key), schema: names, file: file, depth: depth + 1) { return false }
                if let property = properties[key] {
                    if !valid(member, schema: property, file: file, depth: depth + 1) { return false }
                } else if let extra = s["additionalProperties"], !valid(member, schema: extra, file: file, depth: depth + 1) { return false }
            }
        }
        return true
    }

    private static func matches(_ value: ExtensionStreamJSON, type: String) -> Bool {
        switch (type, value) {
        case ("null", .null), ("boolean", .bool), ("string", .string), ("object", .object), ("array", .array), ("number", .number): true
        case ("integer", .number): value.numeric?.integer == true
        default: false
        }
    }
    private static func equal(_ a: ExtensionStreamJSON, _ b: ExtensionStreamJSON) -> Bool {
        if let n = a.numeric, let m = b.numeric { return n.compare(m) == 0 }
        if let a = a.array, let b = b.array { return a.count == b.count && zip(a, b).allSatisfy { equal($0.0, $0.1) } }
        if let a = a.object, let b = b.object { return a.count == b.count && a.allSatisfy { key, value in b[key].map { equal(value, $0) } == true } }
        return a == b
    }
}
