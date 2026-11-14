import Foundation

/// The committed counts the gate compares against: `{file: {rule: {verdict: count}}}`, with the calls that run off the
/// main actor left out. A file with no entry starts at zero.
public enum Baseline {
    public typealias Counts = [String: [String: [String: Int]]]

    public static func counts(_ findings: [Finding]) -> Counts {
        var counts: Counts = [:]
        for finding in findings where finding.verdict != .off {
            counts[finding.file, default: [:]][finding.rule, default: [:]][finding.verdict.rawValue, default: 0] += 1
        }
        return counts
    }

    /// `grown`: a file has more calls on the main actor (or that the lint cannot place) than its baseline; the check
    /// fails and the baseline is never raised. `stale`: it has fewer, or the same number split differently between
    /// `main` and `unknown`; the check fails until the baseline is rewritten downward.
    ///
    /// `main` and `unknown` are counted together: a call the lint stops understanding is not a fix, and a call it
    /// starts understanding is not a new one.
    public static func compare(_ now: Counts, _ was: Counts) -> (grown: [String], stale: [String]) {
        var grown: [String] = [], stale: [String] = []
        for file in Set(now.keys).union(was.keys).sorted() {
            let current = now[file] ?? [:], committed = was[file] ?? [:]
            for rule in Set(current.keys).union(committed.keys).sorted() {
                let n = current[rule] ?? [:], w = committed[rule] ?? [:]
                let total = n.values.reduce(0, +), before = w.values.reduce(0, +)
                if total > before {
                    grown.append("\(file): \(rule) \(before) -> \(total)")
                } else if total < before {
                    stale.append("\(file): \(rule) \(before) -> \(total)")
                } else if n.filter({ $0.value != 0 }) != w.filter({ $0.value != 0 }) {
                    stale.append("\(file): \(rule) \(split(w)) -> \(split(n))")
                }
            }
        }
        return (grown, stale)
    }

    static func split(_ counts: [String: Int]) -> String {
        counts.filter { $0.value != 0 }.sorted { $0.key < $1.key }.map { "\($0.value) \($0.key)" }.joined(separator: " + ")
    }

    public static func decode(_ data: Data) throws -> Counts {
        try JSONDecoder().decode(Counts.self, from: data)
    }

    public static func encode(_ counts: Counts) throws -> Data {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.prettyPrinted, .sortedKeys, .withoutEscapingSlashes]
        return try encoder.encode(counts) + Data("\n".utf8)
    }
}
