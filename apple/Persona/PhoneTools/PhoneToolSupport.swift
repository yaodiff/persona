import Foundation
import PersonaWire

// MARK: - Failures

/// Why a phone tool could not do what Claude asked. Kept separate from
/// `PhoneToolError` so helpers that run off the main actor can build one.
nonisolated enum ToolFailureKind: Sendable {
    case denied, invalidArgument, unavailable
}

nonisolated struct ToolFailure: Error, LocalizedError, Sendable {
    var kind: ToolFailureKind
    var message: String

    init(_ kind: ToolFailureKind, _ message: String) {
        self.kind = kind
        self.message = message
    }

    var errorDescription: String? { message }
}

/// Turns errors into text Claude (and the user reading the transcript) can act on.
nonisolated enum ToolErrors {
    static func describe(_ error: any Error) -> String {
        if let failure = error as? ToolFailure {
            switch failure.kind {
            case .denied:
                return "没有权限：\(failure.message)。\(PhoneToolError.grantHint)" // i18n-ignore
                    + "\nPermission denied: \(failure.message). Turn it on in Persona › Settings › Connectors › This iPhone on the phone; "
                    + "if it was refused before, iOS will not ask again, so allow it in the iOS Settings app › Persona."
            case .invalidArgument:
                return "参数错误 / Invalid argument: \(failure.message)" // i18n-ignore
            case .unavailable:
                return failure.message
            }
        }
        let text = (error as? LocalizedError)?.errorDescription ?? error.localizedDescription
        return "请求失败 / Request failed: \(text)" // i18n-ignore
    }
}

// MARK: - Output

/// Builds the JSON text handed back to Claude.
nonisolated enum Output {
    static func json(_ value: JSONValue) -> [ToolContent] {
        [.text(value.pretty)]
    }

    /// Object from ordered pairs, dropping nil values so empty fields disappear.
    static func object(_ pairs: [(String, JSONValue?)]) -> JSONValue {
        var out: [String: JSONValue] = [:]
        for (key, value) in pairs {
            if let value { out[key] = value }
        }
        return .object(out)
    }

    static func opt(_ string: String?) -> JSONValue? {
        guard let string, !string.isEmpty else { return nil }
        return .string(string)
    }

    static func opt(_ number: Double?) -> JSONValue? {
        number.map { .number($0) }
    }

    static func opt(_ number: Int?) -> JSONValue? {
        number.map { .number(Double($0)) }
    }

    static func list(_ values: [JSONValue]) -> JSONValue {
        .array(values)
    }
}

// MARK: - JSON Schema

nonisolated enum Schema {
    static func object(_ properties: [String: JSONValue] = [:], required: [String] = []) -> JSONValue {
        var out: [String: JSONValue] = [
            "type": .string("object"),
            "additionalProperties": .bool(false),
        ]
        if !properties.isEmpty { out["properties"] = .object(properties) }
        if !required.isEmpty { out["required"] = .array(required.map { .string($0) }) }
        return .object(out)
    }

    static func string(_ description: String, values: [String] = []) -> JSONValue {
        var out: [String: JSONValue] = ["type": .string("string"), "description": .string(description)]
        if !values.isEmpty { out["enum"] = .array(values.map { .string($0) }) }
        return .object(out)
    }

    static func integer(_ description: String, min: Int? = nil, max: Int? = nil) -> JSONValue {
        var out: [String: JSONValue] = ["type": .string("integer"), "description": .string(description)]
        if let min { out["minimum"] = .number(Double(min)) }
        if let max { out["maximum"] = .number(Double(max)) }
        return .object(out)
    }

    static func number(_ description: String) -> JSONValue {
        .object(["type": .string("number"), "description": .string(description)])
    }

    static func boolean(_ description: String) -> JSONValue {
        .object(["type": .string("boolean"), "description": .string(description)])
    }

    static func array(_ description: String, of items: JSONValue) -> JSONValue {
        .object(["type": .string("array"), "description": .string(description), "items": items])
    }
}

// MARK: - Arguments

nonisolated enum ToolArgs {
    static func string(_ args: JSONValue, _ key: String) -> String? {
        guard let value = args[key]?.string else { return nil }
        let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
        return trimmed.isEmpty ? nil : trimmed
    }

    static func int(_ args: JSONValue, _ key: String) -> Int? {
        args[key]?.int
    }

    static func bool(_ args: JSONValue, _ key: String) -> Bool? {
        args[key]?.bool
    }

    /// Integer within bounds, falling back to `fallback` when absent or out of range.
    static func int(_ args: JSONValue, _ key: String, fallback: Int, min lo: Int, max hi: Int) -> Int {
        guard let value = int(args, key) else { return fallback }
        return Swift.min(Swift.max(value, lo), hi)
    }
}

// MARK: - Dates

/// ISO 8601 in the phone's local timezone, the format every tool documents.
nonisolated enum ToolDate {
    static func string(_ date: Date) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime]
        formatter.timeZone = .current
        return formatter.string(from: date)
    }

    static func day(_ date: Date) -> String {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.timeZone = .current
        formatter.dateFormat = "yyyy-MM-dd"
        return formatter.string(from: date)
    }

    /// Parses an ISO 8601 timestamp, or a bare `yyyy-MM-dd` / `yyyy-MM-dd HH:mm[:ss]`
    /// (space or `T`) read as local time. Returns nil for nil/empty input.
    static func parse(_ raw: String?) throws -> Date? {
        guard let raw, !raw.trimmingCharacters(in: .whitespaces).isEmpty else { return nil }
        let text = raw.trimmingCharacters(in: .whitespaces)
        if let date = JSON.parseDate(text) { return date }
        for format in ["yyyy-MM-dd", "yyyy-MM-dd HH:mm", "yyyy-MM-dd'T'HH:mm", "yyyy-MM-dd HH:mm:ss", "yyyy-MM-dd'T'HH:mm:ss"] {
            let formatter = DateFormatter()
            formatter.locale = Locale(identifier: "en_US_POSIX")
            formatter.timeZone = .current
            formatter.dateFormat = format
            if let date = formatter.date(from: text) { return date }
        }
        throw ToolFailure(.invalidArgument, "cannot read the time \"\(raw)\"; use ISO 8601 such as 2026-10-24T14:00:00+08:00")
    }

    /// An explicit ISO 8601 offset stays with the request on an approval card. Without one, the phone's zone applies.
    static func timeZone(in raw: String?) -> TimeZone? {
        guard let raw else { return nil }
        let text = raw.trimmingCharacters(in: .whitespaces)
        guard let separator = text.firstIndex(where: { $0 == "T" || $0 == " " }) else { return nil }
        let time = text[text.index(after: separator)...]
        if time.last == "Z" || time.last == "z" { return TimeZone(secondsFromGMT: 0) }
        guard let sign = time.lastIndex(where: { $0 == "+" || $0 == "-" }) else { return nil }
        let offset = time[time.index(after: sign)...]
        guard offset.allSatisfy({ $0.isNumber || $0 == ":" }), offset.filter({ $0 == ":" }).count <= 1 else { return nil }
        let parts = offset.split(separator: ":", omittingEmptySubsequences: false)
        guard (1...2).contains(parts.count), let hour = Int(parts[0]), hour >= 0, hour <= 18 else { return nil }
        let minute = parts.count == 2 ? Int(parts[1]) : 0
        guard let minute, (0...59).contains(minute), hour < 18 || minute == 0 else { return nil }
        let seconds = (hour * 60 + minute) * 60
        return TimeZone(secondsFromGMT: time[sign] == "-" ? -seconds : seconds)
    }
}

// MARK: - One-shot continuations

/// Resumes a continuation at most once, whichever of several callbacks fires first.
nonisolated final class SingleResume<T: Sendable>: @unchecked Sendable {
    private let lock = NSLock()
    private var continuation: CheckedContinuation<T, any Error>?

    init(_ continuation: CheckedContinuation<T, any Error>) {
        self.continuation = continuation
    }

    func resume(_ result: Result<T, any Error>) {
        lock.lock()
        let pending = continuation
        continuation = nil
        lock.unlock()
        pending?.resume(with: result)
    }

    func succeed(_ value: T) { resume(.success(value)) }
    func fail(_ error: any Error) { resume(.failure(error)) }
}
