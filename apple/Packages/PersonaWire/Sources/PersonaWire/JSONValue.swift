import Foundation

/// Arbitrary JSON (tool inputs, schemas) that round-trips without key rewriting.
public nonisolated enum JSONValue: Codable, Hashable, Sendable {
    case null
    case bool(Bool)
    case number(Double)
    case string(String)
    case array([JSONValue])
    case object([String: JSONValue])

    public init(from decoder: Decoder) throws {
        let c = try decoder.singleValueContainer()
        if c.decodeNil() { self = .null }
        else if let b = try? c.decode(Bool.self) { self = .bool(b) }
        else if let n = try? c.decode(Double.self) { self = .number(n) }
        else if let s = try? c.decode(String.self) { self = .string(s) }
        else if let a = try? c.decode([JSONValue].self) { self = .array(a) }
        else { self = .object(try c.decode([String: JSONValue].self)) }
    }

    public func encode(to encoder: Encoder) throws {
        var c = encoder.singleValueContainer()
        switch self {
        case .null: try c.encodeNil()
        case .bool(let b): try c.encode(b)
        case .number(let n): try c.encode(n)
        case .string(let s): try c.encode(s)
        case .array(let a): try c.encode(a)
        case .object(let o): try c.encode(o)
        }
    }

    public subscript(key: String) -> JSONValue? {
        if case .object(let o) = self { return o[key] }
        return nil
    }

    public var string: String? {
        switch self {
        case .string(let s): s
        // A whole number in Int's range without a decimal point; anything else (1e20, which `Int(_:)` traps on) as Double prints it.
        case .number(let n): Int(exactly: n).map { String($0) } ?? String(n)
        case .bool(let b): b ? "true" : "false"
        default: nil
        }
    }

    public var double: Double? {
        switch self {
        case .number(let n): n
        case .string(let s): Double(s)
        default: nil
        }
    }

    /// Toward zero, as `Int(_:)`; nil past Int's range or for "nan" and "inf", where `Int(_:)` traps.
    public var int: Int? { double.flatMap { Int(exactly: $0.rounded(.towardZero)) } }
    public var bool: Bool? { if case .bool(let b) = self { b } else { nil } }
    public var array: [JSONValue]? { if case .array(let a) = self { a } else { nil } }
    public var object: [String: JSONValue]? { if case .object(let o) = self { o } else { nil } }

    /// Pretty JSON for display.
    public var pretty: String {
        let enc = JSONEncoder()
        enc.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        guard let data = try? enc.encode(self) else { return "" }
        return String(decoding: data, as: UTF8.self)
    }

    public static func from(_ any: Any?) -> JSONValue {
        switch any {
        case nil, is NSNull: return .null
        case let b as Bool: return .bool(b)
        case let i as Int: return .number(Double(i))
        case let d as Double: return .number(d)
        case let s as String: return .string(s)
        case let a as [Any]: return .array(a.map { from($0) })
        case let o as [String: Any]: return .object(o.mapValues { from($0) })
        default: return .string(String(describing: any!))
        }
    }
}

public nonisolated enum JSON {
    public static let decoder: JSONDecoder = {
        let d = JSONDecoder()
        d.dateDecodingStrategy = .custom { decoder in
            let c = try decoder.singleValueContainer()
            let s = (try? c.decode(String.self)) ?? ""
            return parseDate(s) ?? Date(timeIntervalSince1970: 0)
        }
        return d
    }()

    public static let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.dateEncodingStrategy = .iso8601
        return e
    }()

    nonisolated(unsafe) private static let fractional: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return f
    }()

    nonisolated(unsafe) private static let plain: ISO8601DateFormatter = {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withInternetDateTime]
        return f
    }()

    public static func parseDate(_ s: String) -> Date? {
        guard !s.isEmpty else { return nil }
        // Nearly every date the hub sends has one shape: read it straight from the bytes. ISO8601DateFormatter goes
        // through ICU, and on a busy hub it was 10–21% of the app's CPU at rest (#4681). Anything else goes on below.
        if let date = internetDate(s) { return date }
        // Go emits up to nanoseconds; ISO8601DateFormatter only takes milliseconds.
        var t = s
        if let dot = t.firstIndex(of: "."), let end = t[dot...].firstIndex(where: { $0 == "Z" || $0 == "+" || $0 == "-" }) {
            let frac = t[t.index(after: dot)..<end]
            if frac.count > 3 { t.replaceSubrange(t.index(after: dot)..<end, with: frac.prefix(3)) }
        }
        return fractional.date(from: t) ?? plain.date(from: t)
    }

    /// `YYYY-MM-DDTHH:MM:SS[.fraction](Z|±HH:MM)` (RFC 3339, as Go writes it), the same `Date` the formatters give: the
    /// fraction cut to milliseconds. nil for any other shape, an impossible field (Feb 30, a leap second) or a year
    /// before 1900 (the formatters use the Julian calendar before 1582): those go to the formatters as before.
    public static func internetDate(_ s: String) -> Date? {
        var s = s
        return s.withUTF8 { internetDate(bytes: $0) }
    }

    private static func internetDate(bytes b: UnsafeBufferPointer<UInt8>) -> Date? {
        let n = b.count
        guard n >= 20 else { return nil }
        func number(_ at: Int, _ count: Int) -> Int? {
            var v = 0
            for i in at..<at + count {
                let d = b[i] &- 0x30
                guard d < 10 else { return nil }
                v = v * 10 + Int(d)
            }
            return v
        }
        let dash = UInt8(ascii: "-"), colon = UInt8(ascii: ":")
        guard let year = number(0, 4), b[4] == dash, let month = number(5, 2), b[7] == dash, let day = number(8, 2),
              b[10] == UInt8(ascii: "T"), let hour = number(11, 2), b[13] == colon, let minute = number(14, 2),
              b[16] == colon, let second = number(17, 2)
        else { return nil }
        guard year >= 1900, (1...12).contains(month), day >= 1, day <= daysIn(month, of: year), hour < 24, minute < 60, second < 60
        else { return nil }
        var i = 19
        var millis = 0
        if b[i] == UInt8(ascii: ".") {
            i += 1
            let start = i
            while i < n, b[i] &- 0x30 < 10 {
                if i - start < 3 { millis = millis * 10 + Int(b[i] &- 0x30) }
                i += 1
            }
            let digits = i - start
            guard digits > 0 else { return nil }
            for _ in min(digits, 3)..<3 { millis *= 10 }
        }
        guard i < n else { return nil }
        var offset = 0
        switch b[i] {
        case UInt8(ascii: "Z"):
            i += 1
        case UInt8(ascii: "+"), dash:
            guard n - i >= 6, let h = number(i + 1, 2), b[i + 3] == colon, let m = number(i + 4, 2), h < 24, m < 60 else { return nil }
            offset = (h * 3600 + m * 60) * (b[i] == dash ? -1 : 1)
            i += 6
        default:
            return nil
        }
        guard i == n else { return nil }
        let seconds = daysFromCivil(year, month, day) * 86400 + hour * 3600 + minute * 60 + second - offset
        return Date(timeIntervalSince1970: Double(seconds * 1000 + millis) / 1000)
    }

    private static func daysIn(_ month: Int, of year: Int) -> Int {
        switch month {
        case 2: year % 4 == 0 && (year % 100 != 0 || year % 400 == 0) ? 29 : 28
        case 4, 6, 9, 11: 30
        default: 31
        }
    }

    /// Days since 1970-01-01 of a proleptic Gregorian date (Howard Hinnant's `days_from_civil`).
    private static func daysFromCivil(_ year: Int, _ month: Int, _ day: Int) -> Int {
        let y = month <= 2 ? year - 1 : year
        let era = (y >= 0 ? y : y - 399) / 400
        let yoe = y - era * 400
        let doy = (153 * (month > 2 ? month - 3 : month + 9) + 2) / 5 + day - 1
        let doe = yoe * 365 + yoe / 4 - yoe / 100 + doy
        return era * 146_097 + doe - 719_468
    }
}

extension Date {
    /// Go's zero time and other placeholders.
    public nonisolated var isMeaningful: Bool { timeIntervalSince1970 > 946_684_800 }
}
