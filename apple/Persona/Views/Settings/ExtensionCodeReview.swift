import Foundation
import PersonaKit
import PersonaWire

// UI version 5's code review (docs/extensions/extensions.md "UI version 5: code review", #3136, #827), the logic
// without the looks, as the web's web/src/lib/code-review.ts and Android's ExtensionCodeReview.kt: which words of a
// changed line changed, a change that is only whitespace drawn as unchanged, generated files and lockfiles, big ones,
// why a diff starts folded, and the viewed marks kept on this device for extensions that keep none. Drawn by the code
// page (ExtensionCodePage.swift, ExtensionCodeStack.swift).

nonisolated enum CodeReview {
    // MARK: Words that changed

    /// The lines with the words that changed marked: a run of removed lines and the run of added lines right after it
    /// are paired line by line (the first removed with the first added…), and each pair compared word by word, within
    /// `budget` comparisons for the whole file (LineDiff's: a file of long changed lines stays quick).
    static func markWords(_ lines: [CodeDiffLine], budget: inout Int) -> [CodeDiffLine] {
        var out = lines
        var k = 0
        while k < out.count, budget > 0 {
            guard out[k].kind == .del else { k += 1; continue }
            var dels: [Int] = [], adds: [Int] = []
            while k < out.count, out[k].kind == .del { dels.append(k); k += 1 }
            while k < out.count, out[k].kind == .add { adds.append(k); k += 1 }
            for (d, a) in zip(dels, adds) where budget > 0 {
                guard let w = LineDiff.words(out[d].text, out[a].text, budget: &budget) else { continue }
                out[d].changed = w.old
                out[a].changed = w.new
            }
        }
        return out
    }

    // MARK: Whitespace

    /// A hunk's lines with changes that are only whitespace drawn as unchanged: in each run of removed and added lines,
    /// the removed and added lines that are the same but for whitespace (in order) become one unchanged line with both
    /// numbers (and the new text); what is left stays as it was.
    static func ignoringWhitespace(_ lines: [CodeDiffLine]) -> [CodeDiffLine] {
        var out: [CodeDiffLine] = []
        out.reserveCapacity(lines.count)
        var i = 0
        while i < lines.count {
            guard lines[i].kind == .del || lines[i].kind == .add else { out.append(lines[i]); i += 1; continue }
            var dels: [CodeDiffLine] = [], adds: [CodeDiffLine] = []
            while i < lines.count, lines[i].kind == .del { dels.append(lines[i]); i += 1 }
            while i < lines.count, lines[i].kind == .add { adds.append(lines[i]); i += 1 }
            let x = dels.map { squeeze($0.text) }, y = adds.map { squeeze($0.text) }
            let n = x.count, m = y.count
            // lcs[a][b]: the longest common run of x[a...] and y[b...]
            var lcs = [[Int]](repeating: [Int](repeating: 0, count: m + 1), count: n + 1)
            if n > 0 && m > 0 {
                for a in stride(from: n - 1, through: 0, by: -1) {
                    for b in stride(from: m - 1, through: 0, by: -1) {
                        lcs[a][b] = x[a] == y[b] ? lcs[a + 1][b + 1] + 1 : max(lcs[a + 1][b], lcs[a][b + 1])
                    }
                }
            }
            var a = 0, b = 0
            var pendD: [CodeDiffLine] = [], pendA: [CodeDiffLine] = []
            func flush() { out += pendD + pendA; pendD = []; pendA = [] }
            while a < n || b < m {
                if a < n, b < m, x[a] == y[b] {
                    flush()
                    out.append(CodeDiffLine(kind: .context, text: adds[b].text, old: dels[a].old, new: adds[b].new))
                    a += 1; b += 1
                } else if b >= m || (a < n && lcs[a + 1][b] >= lcs[a][b + 1]) {
                    pendD.append(dels[a]); a += 1
                } else {
                    pendA.append(adds[b]); b += 1
                }
            }
            flush()
        }
        return out
    }

    /// Whether every hunk is left with nothing changed once whitespace is ignored.
    static func onlyWhitespace(_ hunks: [[CodeDiffLine]]) -> Bool {
        !hunks.isEmpty && hunks.allSatisfy { ignoringWhitespace($0).allSatisfy { $0.kind == .context || $0.kind == .note } }
    }

    private static func squeeze(_ s: String) -> String { String(s.unicodeScalars.filter { !CharacterSet.whitespaces.contains($0) }) }

    // MARK: Folded at first

    /// Lockfiles, generated and vendored files, by path (the same list as the web's).
    private static let generatedPatterns: [NSRegularExpression] = [
        #"(^|/)(package-lock\.json|npm-shrinkwrap\.json|yarn\.lock|pnpm-lock\.yaml|bun\.lockb?|Cargo\.lock|Gemfile\.lock|composer\.lock|poetry\.lock|Pipfile\.lock|uv\.lock|go\.sum|go\.work\.sum|Podfile\.lock|Package\.resolved|gradle\.lockfile|flake\.lock|mix\.lock|pubspec\.lock|packages\.lock\.json)$"#,
        #"\.(min\.js|min\.css|js\.map|css\.map|pb\.go|pb\.gw\.go|pb\.ts|pb\.swift|pb\.h|pb\.cc|g\.dart|freezed\.dart)$"#,
        #"(_gen\.go|_generated\.go|\.generated\.[a-z]+|_pb2\.py|_pb2_grpc\.py|\.designer\.cs)$"#,
        #"(^|/)(dist|vendor|node_modules|__generated__|__snapshots__)/"#,
    ].map { try! NSRegularExpression(pattern: $0, options: .caseInsensitive) }

    /// A lockfile, a generated or a vendored file, by its path or as the extension says.
    static func isGenerated(_ path: String, said: Bool? = nil) -> Bool {
        if said == true { return true }
        let range = NSRange(path.startIndex..., in: path)
        return generatedPatterns.contains { $0.firstMatch(in: path, range: range) != nil }
    }

    /// A file with more changed lines than this starts folded.
    static let bigDiff = 400

    /// Why a diff starts folded.
    enum Fold: Equatable {
        /// The extension said so (and sent its hunks).
        case collapsed
        case generated
        case big
        case viewed
    }

    /// Why a diff starts folded, or nil: a generated file, a big one, the extension's `collapsed` (with hunks; without,
    /// it is there to be loaded), one already viewed.
    static func foldedAtFirst(_ d: ExtensionDiffComponent, viewed: Bool) -> Fold? {
        if isGenerated(d.path, said: d.generated) { return .generated }
        if (d.additions ?? 0) + (d.deletions ?? 0) > bigDiff { return .big }
        if d.collapsed == true, !(d.hunks ?? []).isEmpty { return .collapsed }
        if viewed { return .viewed }
        return nil
    }

    // MARK: Viewed, kept on this device

    /// What a file's change is, for a viewed mark kept here: its revision, else a digest of its lines (FNV-1a over
    /// their UTF-16 units, as the web's), so a mark goes when the file changes.
    static func changeID(_ d: ExtensionDiffComponent) -> String {
        if let r = d.revision, !r.isEmpty { return d.path + "@" + r }
        var h: UInt32 = 0x811c9dc5
        for hunk in d.hunks ?? [] {
            for line in hunk.lines {
                for u in line.utf16 {
                    h ^= UInt32(u)
                    h = h &* 0x01000193
                }
            }
        }
        return d.path + "#" + String(h, radix: 36)
    }

    /// The open threads of a diff: not resolved, not a draft.
    static func unresolved(_ d: ExtensionDiffComponent) -> Int {
        (d.threads ?? []).filter { $0.resolved != true && $0.pending != true }.count
    }
}

/// The changes marked viewed on this device, for extensions that keep no viewed marks of their own (no `mark_viewed`):
/// by `CodeReview.changeID`, so a mark goes when the file changes; the oldest go past `most`. In the state cache.
@MainActor @Observable final class CodeViewedMarks {
    static let shared = CodeViewedMarks()
    static let name = "extension-review-viewed"
    static let most = 5000

    private var order: [String]
    private(set) var ids: Set<String>
    /// Saved in the state cache: the device's marks are, a test's are not.
    private let keeps: Bool

    init(saved: [String]? = StateCache.load(CodeViewedMarks.name, as: [String].self), keeps: Bool = true) {
        // From the argument, not `order`: an @Observable class can't read a property before all are set.
        let saved = saved ?? []
        order = saved
        ids = Set(saved)
        self.keeps = keeps
    }

    func set(_ id: String, viewed: Bool) {
        guard ids.contains(id) != viewed else { return }
        order.removeAll { $0 == id }
        if viewed { order.append(id) }
        if order.count > Self.most { order.removeFirst(order.count - Self.most) }
        ids = Set(order)
        if keeps { StateCache.save(Self.name, order) }
    }
}

// MARK: Hidden lines (UI version 5's `context`)

nonisolated extension CodeReview {
    /// The lines hidden before hunk `at` (`at` = the hunks' count: after the last one), by their numbers on the new
    /// side: `from` to `to` (nil: to the file's end, not known yet). An old line's number is the new one's plus `delta`.
    struct Gap: Equatable, Hashable, Sendable {
        var at: Int
        var from: Int
        var to: Int?
        var delta: Int
    }

    /// The lines shown of a gap: `top` from its start down (under the hunk before), `bottom` from its end up.
    /// `Int.max`: all of it, its end not known yet.
    struct Shown: Equatable, Sendable {
        var top = 0
        var bottom = 0
    }

    enum Reveal: Sendable { case up, down, all }

    /// A gap's lines are shown at most this many at a time.
    static let step = 20

    /// Every gap of a diff: before each hunk, and after the last (to `total`, the file's lines, when known). None for
    /// a file that is gone.
    static func gaps(_ hunks: [ExtensionHunk], total: Int?) -> [Gap] {
        var out: [Gap] = []
        var from = 1, delta = 0
        for (i, h) in hunks.enumerated() {
            if h.newStart == 0 { return [] }
            out.append(Gap(at: i, from: from, to: h.newStart - 1, delta: h.oldStart - h.newStart))
            let news = h.lines.filter { !$0.hasPrefix("-") && !$0.hasPrefix("\\") }.count
            let olds = h.lines.filter { !$0.hasPrefix("+") && !$0.hasPrefix("\\") }.count
            from = h.newStart + news
            delta = h.oldStart + olds - from
        }
        out.append(Gap(at: hunks.count, from: from, to: total, delta: delta))
        return out
    }

    /// How many lines a gap hides (`Int.max` when its end is not known yet).
    static func size(_ g: Gap) -> Int {
        g.to.map { max(0, $0 - g.from + 1) } ?? Int.max
    }

    /// Showing more of a gap: down (under the hunk before it), up (over the hunk after it), or all. What is shown then,
    /// and which lines that needs (new numbers; `to` nil: to the end).
    static func reveal(_ g: Gap, shown: Shown, _ way: Reveal) -> (shown: Shown, need: (from: Int, to: Int?)) {
        let size = size(g)
        if way == .all { return (Shown(top: size, bottom: 0), (g.from, g.to)) }
        if way == .down || g.to == nil {
            let top = min(size - shown.bottom, shown.top + step)
            return (Shown(top: top, bottom: shown.bottom), (g.from + shown.top, g.from + top - 1))
        }
        let to = g.to ?? g.from
        let bottom = min(size - shown.top, shown.bottom + step)
        return (Shown(top: shown.top, bottom: bottom), (to - bottom + 1, to - shown.bottom))
    }

    /// Lines of the new side of a file, as they came (any part of it), and how many it has once known.
    struct LineCache: Equatable, Sendable {
        private var lines: [Int: String] = [:]
        private(set) var total: Int?

        mutating func put(start: Int, _ new: [String], total: Int?) {
            for (i, l) in new.enumerated() { lines[start + i] = l }
            if let total { self.total = total } else if new.isEmpty, self.total == nil { self.total = start - 1 } // past the end
        }

        /// Line `n` (1: the first), when it came.
        func line(_ n: Int) -> String? { lines[n] }

        /// Every line from…to came (`to` nil: to the end, when known).
        func has(_ from: Int, _ to: Int?) -> Bool {
            guard let end = to ?? total else { return false }
            return from > end || (from...end).allSatisfy { lines[$0] != nil }
        }
    }

    /// The rows a gap shows, unchanged lines from the cache: those at its top, those at its bottom, and how many are
    /// still hidden (`Int.max`: to the end of a file whose length is not known yet).
    static func rows(_ g: Gap, shown: Shown, cache: LineCache?) -> (top: [CodeDiffLine], bottom: [CodeDiffLine], hidden: Int) {
        let to = g.to ?? cache?.total
        let size = to.map { max(0, $0 - g.from + 1) } ?? Int.max
        func row(_ n: Int) -> CodeDiffLine? {
            cache?.line(n).map { CodeDiffLine(kind: .context, text: $0, old: n + g.delta, new: n) }
        }
        var top: [CodeDiffLine] = []
        var n = g.from
        while top.count < min(shown.top, size), let r = row(n) { top.append(r); n += 1 }
        var bottom: [CodeDiffLine] = []
        if let to, shown.bottom > 0 {
            for k in max(g.from + top.count, to - shown.bottom + 1)...max(to, g.from + top.count) where k <= to {
                if let r = row(k) { bottom.append(r) }
            }
        }
        let hidden = size == Int.max ? Int.max : max(0, size - top.count - bottom.count)
        return (top, bottom, hidden)
    }

    // MARK: Line links (`code.line_url`)

    /// A code's line link: `{start}` and `{end}` filled in; https only.
    static func lineLink(_ template: String?, start: Int, end: Int? = nil) -> URL? {
        guard let template, !template.isEmpty else { return nil }
        let s = template.replacingOccurrences(of: "{start}", with: String(start)).replacingOccurrences(of: "{end}", with: String(end ?? start))
        guard let url = URL(string: s), url.scheme?.lowercased() == "https", url.host?.isEmpty == false else { return nil }
        return url
    }
}

// MARK: Suggestions (`diff.suggestions`)

nonisolated extension CodeReview {
    enum BodyPart: Equatable, Sendable {
        case text(String)
        case suggestion([String])
    }

    private static let suggestion = try! NSRegularExpression(pattern: #"^```suggestion[^\S\n]*\n([\s\S]*?)^```[^\S\n]*$"#,
                                                              options: .anchorsMatchLines)

    /// A comment's body cut at its ```suggestion blocks: the text around them, and each block's lines.
    static func splitSuggestions(_ body: String) -> [BodyPart] {
        let ns = body as NSString
        var out: [BodyPart] = []
        var at = 0
        for m in suggestion.matches(in: body, range: NSRange(location: 0, length: ns.length)) {
            let before = ns.substring(with: NSRange(location: at, length: m.range.location - at)).trimmingCharacters(in: .newlines)
            if !before.isEmpty { out.append(.text(before)) }
            var inner = ns.substring(with: m.range(at: 1))
            if inner.hasSuffix("\n") { inner.removeLast() }
            out.append(.suggestion(inner.isEmpty ? [] : inner.components(separatedBy: "\n")))
            at = m.range.location + m.range.length
        }
        let rest = ns.substring(from: at).trimmingCharacters(in: .newlines)
        if !rest.isEmpty { out.append(.text(rest)) }
        return out
    }

    /// A suggestion block that starts from `lines`, for the comment box.
    static func suggestionBlock(_ lines: [String]) -> String {
        "```suggestion\n" + lines.joined(separator: "\n") + "\n```"
    }
}
