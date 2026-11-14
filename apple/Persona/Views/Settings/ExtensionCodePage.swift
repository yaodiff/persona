import PersonaKit
import SwiftUI
import PersonaWire

// An extension's page with UI version 4's code-hosting parts (#2961, docs/extensions/extensions.md「UI version 4: code hosting」): tabs at the
// top, a header across, and on a wide display (iPad, Mac) the `side` sections (a file tree) in a column on the left
// beside the rest; on a phone the side sections are the page and what they open (a file's code, the diffs) is pushed
// on top, a directory at a time. Everything is a block of one lazy stack, a diff a block per line, so a diff of
// 20 000 lines or a long file scrolls like a short one; a single file's code is the viewer's own lines view
// (`CodeLines`), which draws only the lines on screen.
//
// Views without any of it keep `ExtensionPageView`'s list, unchanged.

nonisolated extension ExtensionView {
    /// It has something only the code page draws: tabs, a split, or one of UI version 4's components.
    var wantsCodePage: Bool {
        if review != nil || tabs != nil || layout == ExtensionLayout.split { return true }
        return sections.contains { s in s.items.contains { $0.isCodeHosting } }
    }
}

nonisolated extension ExtensionComponent {
    /// One of UI version 4's code-hosting components.
    var isCodeHosting: Bool {
        switch self {
        case .header, .breadcrumb, .fileTree, .code, .diff, .commentThread, .composer: true
        default: false
        }
    }
}

/// What the page keeps of its own between draws: open directories, folded diffs, the inline comment being written,
/// what is typed in reply boxes, the diffs' look. By component key, so a View drawn again keeps them.
@MainActor @Observable final class CodePageState {
    /// Viewed marks kept on this device (for extensions without `mark_viewed`).
    let marks: CodeViewedMarks
    /// Where the page's display options are kept (nil: nowhere, a test's page).
    private let defaults: UserDefaults?

    /// The device's viewed marks and display options; a test's page passes its own (none kept), so what the app or
    /// another run saved on the device does not change what it draws.
    /// Assignments only: SwiftUI makes it again each time the page's parent redraws (docs/apple/performance.md P3).
    init(marks: CodeViewedMarks = .shared, defaults: UserDefaults? = .standard) {
        self.marks = marks
        self.defaults = defaults
    }

    /// Open directories by tree key.
    var open: [String: Set<String>] = [:]
    /// Diffs folded by the user (by diff key); a diff the extension sends `collapsed` starts folded.
    var folded: Set<String> = []
    var unfolded: Set<String> = []
    /// The inline comment being written: on which diff, side and line.
    var composing: Anchor?
    /// What is typed in a reply box or an inline comment, by its block's id.
    var drafts: [String: String] = [:]
    /// A file the tree asked to show (no tree action: scroll to its diff).
    var scrollTo: String?
    /// The last tree tap was on a file (the phone shows what it opens).
    var drilledFile = false
    /// Drilling in (a narrow page): the directory shown, by tree key; none is the top.
    var drill: [String: String] = [:]
    /// A narrow page shows its main sections (a file's code, the diffs) in place of the tree.
    var mainOpen = false

    func enter(_ dir: String?, tree: String) {
        drill[tree] = dir
        version += 1
    }

    func showMain(_ open: Bool) {
        mainOpen = open
        version += 1
    }
    /// Bumped when anything that changes the blocks changes.
    private(set) var version = 0

    struct Anchor: Hashable {
        var diff: String
        var side: String
        var line: Int
        /// A comment on several lines (UI version 5's `start_line`): from this one to `line`, on the same side.
        var start: Int? = nil

        /// Whether line `n` of `side` in diff `key` is one the comment is on.
        func covers(_ key: String, _ side: String, _ n: Int) -> Bool {
            diff == key && self.side == side && ((start ?? line)...line).contains(n)
        }
    }

    func toggle(dir: String, tree: String) {
        var s = open[tree] ?? []
        if s.contains(dir) { s.remove(dir) } else { s.insert(dir) }
        open[tree] = s
        version += 1
    }

    /// Whether a diff is folded: as it starts (`initially`: generated, big, viewed, the extension's `collapsed`)
    /// unless the user changed it.
    func isFolded(_ key: String, initially: Bool) -> Bool {
        initially ? !unfolded.contains(key) : folded.contains(key)
    }

    func toggle(diff key: String, initially: Bool) {
        setFolded(key, !isFolded(key, initially: initially), initially: initially)
    }

    func setFolded(_ key: String, _ on: Bool, initially: Bool) {
        if initially {
            if on { unfolded.remove(key) } else { unfolded.insert(key) }
        } else {
            if on { folded.insert(key) } else { folded.remove(key) }
        }
        version += 1
    }

    // MARK: Code review (UI version 5)

    /// Viewed marks changed here and sent with the extension's `mark_viewed`, by path, until its page says so too.
    private(set) var viewedOver: [String: Bool] = [:]
    /// Changes that are only whitespace drawn as unchanged; the app's choice, kept for every page.
    var whitespace: Bool { chosenWhitespace ?? savedWhitespace }
    /// The choice made on this page.
    private var chosenWhitespace: Bool?
    /// The device's choice, read the first time the page asks (once a page, not when its state is made: P2, P3).
    @ObservationIgnored private lazy var savedWhitespace = defaults?.bool(forKey: Self.whitespaceKey) ?? false
    static let whitespaceKey = "extension.code.ignoreWhitespace"
    /// A thread to scroll to (its block's id), and the open thread the last jump went to.
    var jump: String?
    @ObservationIgnored var lastJump = -1
    /// Hunks numbered, with whitespace ignored or not and their changed words marked, by what they are: worked out
    /// once, not on every draw.
    @ObservationIgnored private var marked: [MarkKey: [[CodeDiffLine]]] = [:]

    private struct MarkKey: Hashable {
        var hunks: [ExtensionHunk]
        var whitespace: Bool
    }

    /// Whether a file is marked viewed: as just changed here, else the extension's mark, else (no `mark_viewed`) this
    /// device's.
    func isViewed(_ d: ExtensionDiffComponent) -> Bool {
        if let v = viewedOver[d.path] { return v }
        if d.markViewed == nil { return marks.ids.contains(CodeReview.changeID(d)) }
        return d.viewed == true
    }

    /// Whether a diff starts folded (`CodeReview.foldedAtFirst`); one without hunks never does (it is to be loaded).
    func initiallyFolded(_ d: ExtensionDiffComponent) -> Bool {
        CodeReview.foldedAtFirst(d, viewed: isViewed(d)) != nil && !(d.hunks ?? []).isEmpty
    }

    /// Marks a file viewed or not, at once: checked, it folds; unchecked, it stays as it is. With `mark_viewed` the
    /// mark is the page's until the extension's answer: the returned attempt puts back what was there if it fails.
    @discardableResult func setViewed(_ d: ExtensionDiffComponent, key: String, _ v: Bool) -> (attempt: Int, before: Bool?) {
        let wasFolded = isFolded(key, initially: initiallyFolded(d))
        let before = viewedOver[d.path]
        if d.markViewed == nil { marks.set(CodeReview.changeID(d), viewed: v) } else { viewedOver[d.path] = v }
        viewedAttempts += 1
        viewedAttempt[d.path] = viewedAttempts
        setFolded(key, v || wasFolded, initially: initiallyFolded(d))
        return (viewedAttempts, before)
    }

    /// The extension's `mark_viewed` failed: what was there before, unless the mark was changed again since.
    func viewedFailed(_ path: String, attempt: Int, before: Bool?) {
        guard viewedAttempt[path] == attempt else { return }
        viewedOver[path] = before
        version += 1
    }

    @ObservationIgnored private var viewedAttempts = 0
    @ObservationIgnored private var viewedAttempt: [String: Int] = [:]

    // MARK: Hidden lines (UI version 5's `context`)

    /// The lines of each file that came, by path: a gap shown again needs no request.
    private(set) var lineCaches: [String: CodeReview.LineCache] = [:]
    /// What is shown of each gap, by diff key and gap.
    private(set) var gapShown: [String: CodeReview.Shown] = [:]
    /// The gaps whose lines are on their way.
    private(set) var gapBusy: Set<String> = []

    static func gapKey(_ key: String, _ at: Int) -> String { key + "#" + String(at) }

    func shown(_ key: String, _ at: Int) -> CodeReview.Shown { gapShown[Self.gapKey(key, at)] ?? CodeReview.Shown() }

    func setShown(_ key: String, _ at: Int, _ s: CodeReview.Shown) {
        gapShown[Self.gapKey(key, at)] = s
        version += 1
    }

    func setGapBusy(_ key: String, _ at: Int, _ busy: Bool) {
        if busy { gapBusy.insert(Self.gapKey(key, at)) } else { gapBusy.remove(Self.gapKey(key, at)) }
    }

    /// Lines that came for a file (its new side).
    func put(_ lines: ExtensionUIStreamFileLines) {
        guard lines.side == nil || lines.side == "new" else { return }
        lineCaches[lines.path, default: CodeReview.LineCache()].put(start: lines.start, lines.lines, total: lines.total)
        version += 1
    }

    func setWhitespace(_ on: Bool) {
        chosenWhitespace = on
        defaults?.set(on, forKey: Self.whitespaceKey)
        version += 1
    }

    /// A diff's hunks as lines: numbered, whitespace ignored when the page says so, the words that changed marked.
    func lines(of hunks: [ExtensionHunk]) -> [[CodeDiffLine]] {
        let key = MarkKey(hunks: hunks, whitespace: whitespace)
        if let done = marked[key] { return done }
        var budget = LineDiff.maxWordWork
        let out = hunks.map { h in
            let numbered = CodeDiffModel.lines(oldStart: h.oldStart, newStart: h.newStart, raw: h.lines)
            return CodeReview.markWords(whitespace ? CodeReview.ignoringWhitespace(numbered) : numbered, budget: &budget)
        }
        if marked.count >= 256 { marked = [:] }
        marked[key] = out
        return out
    }

    func compose(_ a: Anchor?) {
        composing = composing == a ? nil : a
        version += 1
    }

    /// A line number tapped: the comment box opens on it; another line on the same side of the same diff makes it a
    /// comment on the lines from the first to that one (`start_line`, UI version 5); the one line tapped again closes it.
    func pick(_ a: Anchor) {
        if let c = composing, c.diff == a.diff, c.side == a.side, c.line != a.line || c.start != nil {
            let first = c.start ?? c.line
            composing = first == a.line ? Anchor(diff: a.diff, side: a.side, line: a.line)
                : Anchor(diff: a.diff, side: a.side, line: max(first, a.line), start: min(first, a.line))
        } else {
            composing = composing == a ? nil : a
        }
        version += 1
    }

    /// Resolved threads opened to all their comments, by block id.
    private(set) var openThreads: Set<String> = []

    func openThread(_ id: String) {
        openThreads.insert(id)
        version += 1
    }

    func changed() { version += 1 }
}

/// A block of the page: one row of the lazy stack.
enum CodePageBlock: Identifiable {
    /// Components of UI versions 1–3 between the new ones, drawn as a card (as `ExtensionViewStack` draws them).
    case legacy(id: String, title: String?, footer: String?, items: [ExtensionComponent], more: ExtensionMore?)
    case header(id: String, ExtensionHeaderComponent)
    case breadcrumb(id: String, ExtensionBreadcrumbComponent)
    case treeRow(id: String, tree: String, row: CodeTree.Row, component: ExtensionFileTreeComponent)
    case treeEmpty(id: String)
    case code(id: String, ExtensionCodeComponent)
    case diffHeader(id: String, key: String, diff: ExtensionDiffComponent, folded: Bool)
    case hunk(id: String, diff: ExtensionDiffComponent, index: Int, header: String)
    case diffLine(id: String, key: String, diff: ExtensionDiffComponent, line: CodeDiffLine, language: SyntaxLanguage?, digits: Int)
    case diffPair(id: String, key: String, diff: ExtensionDiffComponent, left: CodeDiffLine?, right: CodeDiffLine?, language: SyntaxLanguage?, digits: Int)
    case diffNote(id: String, text: String)
    /// Lines hidden between hunks (UI version 5's `context`): how many, the way to show more of them (down, up, all;
    /// the first gap only up, the last only down), the next hunk's header.
    case gapBar(id: String, key: String, diff: ExtensionDiffComponent, gap: CodeReview.Gap, hidden: Int, first: Bool,
                last: Bool, header: String?)
    /// A diff folded at first because it is generated, big or viewed: why, and a way to show it (UI version 5).
    case diffFolded(id: String, key: String, note: String)
    /// Over a page of diffs (UI version 5): how many are viewed, the threads still open (by their blocks' ids, in
    /// order, with their diff's key and whether it starts folded), the page's display options.
    case reviewTools(id: String, files: Int, viewed: Int, open: [(block: String, diff: String, folded: Bool)])
    case diffLoad(id: String, diff: ExtensionDiffComponent)
    case diffMore(id: String, diff: ExtensionDiffComponent, index: Int)
    case inlineThread(id: String, diff: ExtensionDiffComponent, thread: ExtensionThread)
    case inlineComposer(id: String, diff: ExtensionDiffComponent, anchor: CodePageState.Anchor)
    case threadTitle(id: String, title: String, resolved: Bool)
    case comment(id: String, ExtensionComment)
    case reply(id: String, action: ExtensionAction, placeholder: String)
    case composer(id: String, ExtensionComposerComponent)
    case gap(id: String)

    var id: String {
        switch self {
        case .legacy(let id, _, _, _, _), .header(let id, _), .breadcrumb(let id, _), .treeRow(let id, _, _, _),
             .treeEmpty(let id), .code(let id, _), .diffHeader(let id, _, _, _), .hunk(let id, _, _, _),
             .diffLine(let id, _, _, _, _, _), .diffPair(let id, _, _, _, _, _, _), .diffNote(let id, _),
             .diffLoad(let id, _), .diffMore(let id, _, _), .inlineThread(let id, _, _), .inlineComposer(let id, _, _),
             .diffFolded(let id, _, _), .reviewTools(let id, _, _, _), .gapBar(let id, _, _, _, _, _, _, _),
             .threadTitle(let id, _, _), .comment(let id, _), .reply(let id, _, _), .composer(let id, _), .gap(let id):
            id
        }
    }
}

/// Lays a View's sections out as blocks.
@MainActor enum CodePageBlocks {
    /// Where the phone's way to the diffs or the file goes (a gap of this id).
    static let openMainSlot = "gap:open-main"

    /// The blocks of these sections (of `view`, by index), with the page's state.
    static func of(_ view: ExtensionView, sections: [Int], state: CodePageState, look: CodeDiffLook,
                   canComment: Bool, drills: Bool = false) -> [CodePageBlock] {
        var out: [CodePageBlock] = []
        for si in sections where si < view.sections.count {
            let s = view.sections[si]
            var run: [ExtensionComponent] = []
            var runTitle = s.title
            var runStart = 0
            func flush(_ last: Bool) {
                guard !run.isEmpty || (last && s.more != nil) else { return }
                let runID = s.key.map { "\($0).\(run.first?.key ?? String(runStart))" } ?? "\(si).\(runStart)"
                out.append(.legacy(id: "legacy:\(runID)", title: runTitle, footer: last ? s.footer : nil, items: run,
                                   more: last ? s.more : nil))
                run = []
                runTitle = nil
            }
            for (ci, c) in s.items.enumerated() {
                let key = c.key ?? "\(si).\(ci)"
                if !c.isCodeHosting {
                    if case .unknown = c { continue }
                    if case .html = c { continue }
                    if run.isEmpty { runStart = ci }
                    run.append(c)
                    continue
                }
                flush(false)
                // The section's title over its first part, when that part is one of the new components.
                if let t = runTitle, !t.isEmpty {
                    out.append(.threadTitle(id: "title:\(si)", title: t, resolved: false))
                    runTitle = nil
                }
                switch c {
                case .header(let h): out.append(.header(id: "header:" + key, h))
                case .breadcrumb(let b): out.append(.breadcrumb(id: "crumbs:" + key, b))
                case .fileTree(let t): out += tree(t, key: key, state: state, drills: drills)
                case .code(let code): out.append(.code(id: "code:" + key, code))
                case .diff(let d) where !d.path.isEmpty: out += diff(d, key: c.key ?? "diff:" + d.path, state: state, look: look, canComment: canComment)
                case .commentThread(let t): out += thread(t, key: key)
                case .composer(let comp): out.append(.composer(id: "composer:" + key, comp))
                default: break
                }
            }
            flush(true)
        }
        if let first = out.firstIndex(where: { if case .diffHeader = $0 { true } else { false } }) {
            let diffs = sections.filter { $0 < view.sections.count }.flatMap { view.sections[$0].items }.compactMap { c -> (String, ExtensionDiffComponent)? in
                if case .diff(let d) = c, !d.path.isEmpty { (c.key ?? "diff:" + d.path, d) } else { nil }
            }
            let open = diffs.flatMap { key, d in
                (d.threads ?? []).enumerated().filter { $1.resolved != true && $1.pending != true }
                    .map { (block: threadID(key, $1, $0), diff: key, folded: state.initiallyFolded(d)) }
            }
            out.insert(.reviewTools(id: "review-tools", files: diffs.count, viewed: diffs.filter { state.isViewed($1) }.count,
                                    open: open), at: first)
        }
        return out
    }

    /// A thread's block id on its diff: where the open threads' jump goes.
    static func threadID(_ key: String, _ t: ExtensionThread, _ index: Int) -> String {
        "th:\(key):\(t.id ?? String(index))"
    }

    /// A directory's rows on its own (drilling in on a narrow page).
    static func level(_ t: ExtensionFileTreeComponent, key: String, dir: String) -> [CodePageBlock] {
        guard let node = CodeTree.node(dir, in: CodeTree.build(t.entries.map(CodeTreeEntry.init))) else { return [] }
        if node.children.isEmpty { return [.treeEmpty(id: "tree-empty:\(key):\(dir)")] }
        return node.children.map { .treeRow(id: "tree:\(key):\($0.path)", tree: key, row: CodeTree.Row(node: $0, depth: 0, open: false), component: t) }
    }

    static func tree(_ t: ExtensionFileTreeComponent, key: String, state: CodePageState, drills: Bool = false) -> [CodePageBlock] {
        let nodes = CodeTree.build(t.entries.map(CodeTreeEntry.init))
        guard !nodes.isEmpty else { return [.treeEmpty(id: "tree-empty:" + key)] }
        // Drilling in (a narrow page): the top level here, a directory in its place when entered (`level`).
        if drills { return CodeTree.rows(nodes, open: []).map { .treeRow(id: "tree:\(key):\($0.id)", tree: key, row: $0, component: t) } }
        var open = state.open[key] ?? []
        // The selected file's directories start open.
        if state.open[key] == nil, let sel = t.selected, !sel.isEmpty {
            open = CodeTree.parents(of: sel, in: nodes)
            state.open[key] = open
        }
        return CodeTree.rows(nodes, open: open).map { .treeRow(id: "tree:\(key):\($0.id)", tree: key, row: $0, component: t) }
    }

    static func diff(_ d: ExtensionDiffComponent, key: String, state: CodePageState, look: CodeDiffLook,
                     canComment: Bool) -> [CodePageBlock] {
        // Folded at first: generated, big, viewed, or `collapsed` by the extension (UI version 5). Without hunks the file
        // is there to be loaded (its Load Diff shows), never folded at first: a generated one is not loaded by itself.
        let hunks = d.hunks ?? []
        let reason = CodeReview.foldedAtFirst(d, viewed: state.isViewed(d))
        let folded = state.isFolded(key, initially: state.initiallyFolded(d))
        var out: [CodePageBlock] = [.diffHeader(id: "dh:" + key, key: key, diff: d, folded: folded)]
        defer { out.append(.gap(id: "gap:" + key)) }
        guard !folded else {
            let note: String? = switch reason {
            case .generated: String(localized: "Generated file, folded")
            case .big: String(localized: "Large change, folded")
            case .viewed: String(localized: "Viewed")
            case .collapsed, nil: nil
            }
            if let note, !hunks.isEmpty { out.append(.diffFolded(id: "fold:" + key, key: key, note: note)) }
            return out
        }
        if d.binary == true {
            out.append(.diffNote(id: "bin:" + key, text: String(localized: "Binary file; changes not shown")))
            return out
        }
        if hunks.isEmpty {
            if d.load != nil { out.append(.diffLoad(id: "load:" + key, diff: d)) }
            else { out.append(.diffNote(id: "none:" + key, text: String(localized: "No changes to show"))) }
            return out
        }
        let language = d.language.flatMap(SyntaxLanguage.named) ?? SyntaxLanguage.detect(name: d.path)
        // Numbered, whitespace ignored when the page says so, the words that changed marked (worked out once).
        let numbered = state.lines(of: hunks)
        if state.whitespace, numbered.allSatisfy({ $0.allSatisfy { $0.kind == .context || $0.kind == .note } }) {
            out.append(.diffNote(id: "ws:" + key, text: String(localized: "Only whitespace changed")))
            return out
        }
        // UI version 5's `context`: the lines between hunks are asked of the extension (`expand` is not used then); a file
        // added whole has none hidden.
        let cache = state.lineCaches[d.path]
        let gaps = d.context != nil && d.status != "added" ? CodeReview.gaps(hunks, total: cache?.total) : []
        let digits = max(CodeGutter.digits(numbered.flatMap { $0 }), gaps.isEmpty ? 0 : String(cache?.total ?? 0).count)
        let oneSided = d.status == "added" || d.status == "deleted"
            || numbered.allSatisfy { $0.allSatisfy { $0.kind != .context && $0.kind != .del } }
            || numbered.allSatisfy { $0.allSatisfy { $0.kind != .context && $0.kind != .add } }
        // Threads under the line they are on; ones whose line is not in the hunks (outdated) after the file.
        var placed: Set<Int> = []
        let anchors = Set(numbered.flatMap { $0 }.compactMap { $0.anchor.map { "\($0.side):\($0.line)" } })
        var threads: [String: [(Int, ExtensionThread)]] = [:]
        for (i, t) in (d.threads ?? []).enumerated() {
            let k = "\(t.side):\(t.line)"
            if anchors.contains(k) { threads[k, default: []].append((i, t)); placed.insert(i) }
        }
        func after(_ l: CodeDiffLine?) -> [CodePageBlock] {
            guard let l, let a = l.anchor else { return [] }
            var out: [CodePageBlock] = []
            for (i, t) in threads["\(a.side):\(a.line)"] ?? [] {
                out.append(.inlineThread(id: threadID(key, t, i), diff: d, thread: t))
            }
            // The comment box under the last line it is on.
            if let c = state.composing, c.diff == key, c.side == a.side, c.line == a.line {
                // By diff and side: what is typed stays while the lines it is on change.
                out.append(.inlineComposer(id: "ic:\(key):\(a.side)", diff: d, anchor: c))
            }
            return out
        }
        // A gap's unchanged lines, as any other (side by side: on both sides).
        func unchanged(_ lines: [CodeDiffLine], _ tag: String) -> [CodePageBlock] {
            lines.flatMap { l -> [CodePageBlock] in
                let id = "gl:\(key):\(tag):\(l.new ?? 0)"
                let row: CodePageBlock = look == .split && !oneSided
                    ? .diffPair(id: id, key: key, diff: d, left: l, right: l, language: language, digits: digits)
                    : .diffLine(id: id, key: key, diff: d, line: l, language: language, digits: digits)
                return [row] + after(l)
            }
        }
        /// The gap before hunk `i` (`i` = the hunks' count: after the last): its lines shown, the bar for the rest.
        func gap(_ i: Int, header: String?) -> [CodePageBlock] {
            guard i < gaps.count else { return [] }
            let g = gaps[i]
            let r = CodeReview.rows(g, shown: state.shown(key, g.at), cache: cache)
            var out = unchanged(r.top, "\(i)t")
            if r.hidden > 0 {
                out.append(.gapBar(id: "gap-bar:\(key):\(i)", key: key, diff: d, gap: g, hidden: r.hidden, first: i == 0,
                                   last: i == hunks.count, header: header))
            }
            out += unchanged(r.bottom, "\(i)b")
            return out
        }
        for (hi, h) in hunks.enumerated() {
            let header = h.header?.isEmpty == false ? h.header! : "@@ -\(h.oldStart) +\(h.newStart) @@"
            if gaps.isEmpty {
                out.append(.hunk(id: "hk:\(key):\(hi)", diff: d, index: hi, header: header))
            } else {
                let g = gap(hi, header: h.header)
                // Nothing hidden over it and nothing shown: its header as before.
                let bare = !g.contains { if case .gapBar = $0 { true } else { false } }
                    && !g.contains { if case .diffLine = $0 { true } else if case .diffPair = $0 { true } else { false } }
                out += bare ? [.hunk(id: "hk:\(key):\(hi)", diff: d, index: hi, header: header)] : g
            }
            let lines = numbered[hi]
            // A file added or removed whole has one side only: side by side would leave half the column empty.
            if look == .split, !oneSided {
                for (li, p) in CodeDiffModel.pairs(lines).enumerated() {
                    out.append(.diffPair(id: "dp:\(key):\(hi):\(li)", key: key, diff: d, left: p.left, right: p.right,
                                         language: language, digits: digits))
                    out += after(p.left)
                    if p.right != p.left { out += after(p.right) }
                }
            } else {
                for (li, l) in lines.enumerated() {
                    out.append(.diffLine(id: "dl:\(key):\(hi):\(li)", key: key, diff: d, line: l, language: language, digits: digits))
                    out += after(l)
                }
            }
        }
        if !gaps.isEmpty { out += gap(hunks.count, header: nil) }
        else if d.expand != nil { out.append(.diffMore(id: "more:" + key, diff: d, index: hunks.count - 1)) }
        for (i, t) in (d.threads ?? []).enumerated() where !placed.contains(i) {
            out.append(.inlineThread(id: threadID(key, t, i), diff: d, thread: t))
        }
        return out
    }

    static func thread(_ t: ExtensionCommentThreadComponent, key: String) -> [CodePageBlock] {
        var out: [CodePageBlock] = []
        if let title = t.title, !title.isEmpty { out.append(.threadTitle(id: "tt:" + key, title: title, resolved: t.resolved == true)) }
        for (i, c) in t.comments.enumerated() { out.append(.comment(id: "cm:\(key):\(c.id ?? "\(i)")", c)) }
        if let r = t.reply { out.append(.reply(id: "rp:" + key, action: r, placeholder: String(localized: "Reply…"))) }
        return out
    }
}

nonisolated extension CodeTreeEntry {
    init(_ e: ExtensionTreeEntry) {
        self.init(path: e.path, kind: e.kind, status: e.status, additions: e.additions, deletions: e.deletions,
                  comments: e.comments, badge: e.badge?.text, viewed: e.viewed)
    }
}

// MARK: The page

/// An extension's page drawn as code (see the top of the file). Hosted by `ExtensionPageView` when the View wants it.
struct ExtensionCodePage: View {
    @Bindable var model: ExtensionViewModel
    @Binding var link: LinkTarget?
    @State private var state = CodePageState()
    @Environment(\.horizontalSizeClass) private var sizeClass
    @StoredValue("extension.code.diffLook") private var lookRaw = CodeDiffLook.split.rawValue
    /// The phone's pushed page: the main sections (a file's code, the diffs).

    /// Wide enough for the side column beside the main one.
    static let splitWidth = Theme.Size.codeSplitMin

    var body: some View {
        if let view = model.view {
            GeometryReader { g in
                let wide = isWide(g.size.width)
                VStack(spacing: Theme.Space.flush) {
                    if let tabs = view.tabs { CodeTabsBar(tabs: tabs, model: model, link: $link) }
                    // The branch (or another filter) under the tabs.
                    if let f = view.filter {
                        ExtensionFilterChips(filter: f, model: model, link: $link)
                            .padding(.horizontal, Theme.Space.page)
                            .padding(.bottom, Theme.Space.xs)
                    }
                    if view.layout == ExtensionLayout.split, sides(view).isEmpty == false {
                        if wide { splitBody(view) } else { phoneBody(view) }
                    } else {
                        mainColumn(view, sections: Array(view.sections.indices), wide: wide)
                    }
                }
                .frame(width: g.size.width, height: g.size.height, alignment: .top)
            }
            .background(Theme.pageBackground.ignoresSafeArea())
            .safeAreaInset(edge: .bottom, spacing: Theme.Space.flush) {
                if let review = view.review {
                    ExtensionReviewBar(review: review, model: model, link: $link)
                }
            }
        }
    }

    private func isWide(_ width: CGFloat) -> Bool {
        #if os(macOS)
        width >= Self.splitWidth
        #else
        sizeClass == .regular && width >= Self.splitWidth
        #endif
    }

    private var look: CodeDiffLook { CodeDiffLook(rawValue: lookRaw) ?? .split }

    private func sides(_ v: ExtensionView) -> [Int] { v.sections.indices.filter { v.sections[$0].pane == "side" } }
    private func mains(_ v: ExtensionView) -> [Int] { v.sections.indices.filter { v.sections[$0].pane != "side" } }

    /// The leading main sections that are only a header or a path: across both columns, above them.
    private func top(_ v: ExtensionView) -> [Int] {
        var out: [Int] = []
        for i in mains(v) {
            let items = v.sections[i].items
            let heads = items.allSatisfy { c in
                switch c { case .header, .breadcrumb: true; default: false }
            }
            guard heads, !items.isEmpty, i < (sides(v).first ?? 0) else { break }
            out.append(i)
        }
        return out
    }

    // MARK: Wide

    private func splitBody(_ v: ExtensionView) -> some View {
        let top = top(v)
        return VStack(spacing: Theme.Space.flush) {
            if !top.isEmpty {
                CodeBlockStack(blocks: blocks(v, top, wide: true), model: model, state: state, look: look, link: $link,
                               scrolls: false)
                    .padding(.horizontal, Theme.Space.page)
                    .padding(.top, Theme.Space.s)
            }
            HStack(spacing: Theme.Space.flush) {
                CodeBlockStack(blocks: blocks(v, sides(v), wide: true), model: model, state: state, look: look, link: $link)
                    .scaledFrame(width: Theme.Size.codeSideColumn)
                    .background(Theme.Fill.chip.opacity(0.5))
                Divider()
                mainColumn(v, sections: mains(v).filter { !top.contains($0) }, wide: true)
            }
        }
    }

    // MARK: Phone

    /// The side sections are the page, a directory at a time; a file (or All Changes) shows the main sections in their
    /// place, with a way back. All in the page itself: it is pushed on stacks of every kind (settings' split, the
    /// shell, the Mac's sections) and must not push on them.
    @ViewBuilder private func phoneBody(_ v: ExtensionView) -> some View {
        let top = top(v)
        let rest = mains(v).filter { !top.contains($0) }
        let movable = rest.filter { i in v.sections[i].items.contains { if case .code = $0 { true } else if case .diff = $0 { true } else { false } } }
        let stay = rest.filter { !movable.contains($0) }
        if state.mainOpen, !movable.isEmpty {
            VStack(spacing: Theme.Space.flush) {
                CodeBackRow(title: String(localized: "Files")) { withAnimation(Theme.Motion.layout) { state.showMain(false) } }
                CodeMainPage(model: model, state: state, link: $link, sections: movable)
            }
            .transition(.move(edge: .trailing))
        } else {
            let tree = sides(v).flatMap { i in v.sections[i].items.compactMap { c -> (String, ExtensionFileTreeComponent)? in
                if case .fileTree(let t) = c { (c.key ?? "\(i).0", t) } else { nil }
            } }.first
            let dir = tree.flatMap { state.drill[$0.0] }
            VStack(spacing: Theme.Space.flush) {
                if let tree, let dir {
                    let parent = (dir as NSString).deletingLastPathComponent
                    let known = CodeTree.node(parent, in: CodeTree.build(tree.1.entries.map(CodeTreeEntry.init))) != nil
                    CodeBackRow(title: known && !parent.isEmpty ? (parent as NSString).lastPathComponent : String(localized: "All Files")) {
                        withAnimation(Theme.Motion.layout) { state.enter(known && !parent.isEmpty ? parent : nil, tree: tree.0) }
                    }
                }
                CodeBlockStack(blocks: phoneRoot(v, top: top, tree: tree, dir: dir, stay: stay, opensMain: !movable.isEmpty), model: model, state: state, look: .unified, link: $link, drills: true,
                               openMain: movable.isEmpty ? nil : { withAnimation(Theme.Motion.layout) { state.showMain(true) } },
                               mainTitle: mainTitle(v, movable))
            }
            .onChange(of: state.scrollTo) { _, p in if p != nil, !movable.isEmpty { state.showMain(true) } }
            .onChange(of: model.view?.sections.flatMap(\.items).compactMap(\.selectedPath).first) { old, new in
                // The tree's action drew the file: show it.
                if new != nil, new != old, state.drilledFile, !movable.isEmpty { state.showMain(true) }
            }
        }
    }

    /// A narrow page's rows: what is over the split, the way to the main sections, the directory shown (the top of
    /// the tree when none), the rest of the main sections at the top only.
    private func phoneRoot(_ v: ExtensionView, top: [Int], tree: (String, ExtensionFileTreeComponent)?, dir: String?,
                           stay: [Int], opensMain: Bool) -> [CodePageBlock] {
        var root = blocks(v, top, wide: false)
        if opensMain { root.append(.gap(id: CodePageBlocks.openMainSlot)) }
        if let dir, let tree { root += CodePageBlocks.level(tree.1, key: tree.0, dir: dir) }
        else { root += blocks(v, sides(v), wide: false, drills: true) + blocks(v, stay, wide: false) }
        return root
    }

    private func mainTitle(_ v: ExtensionView, _ sections: [Int]) -> String {
        let diffs = sections.flatMap { v.sections[$0].items }.filter { if case .diff = $0 { true } else { false } }.count
        return diffs > 0 ? String(localized: "All Changes · \(diffs)") : String(localized: "View File")
    }

    // MARK: Main column

    @ViewBuilder private func mainColumn(_ v: ExtensionView, sections: [Int], wide: Bool) -> some View {
        let items = sections.flatMap { v.sections[$0].items }
        let codes = items.filter { if case .code = $0 { true } else { false } }
        if codes.count == 1, case .code(let code)? = codes.first, items.count <= 4 {
            // One file: its lines fill the column (the viewer's own scrolling, both ways); what is above it stays.
            let above = blocks(v, sections, wide: wide).filter { if case .code = $0 { false } else { true } }
            VStack(spacing: Theme.Space.flush) {
                if !above.isEmpty {
                    CodeBlockStack(blocks: above, model: model, state: state, look: look, link: $link, scrolls: false)
                        .padding(.horizontal, Theme.Space.page)
                        .padding(.vertical, Theme.Space.s)
                }
                CodeFileBlock(code: code, model: model, link: $link, fills: true)
            }
        } else {
            CodeBlockStack(blocks: blocks(v, sections, wide: wide), model: model, state: state, look: wide ? look : .unified,
                           link: $link, lookPicker: wide && items.contains { if case .diff = $0 { true } else { false } } ? $lookRaw : nil)
        }
    }

    private func blocks(_ v: ExtensionView, _ sections: [Int], wide: Bool, drills: Bool = false) -> [CodePageBlock] {
        _ = state.version // the blocks follow the page's state
        return CodePageBlocks.of(v, sections: sections, state: state, look: wide ? look : .unified, canComment: true, drills: drills)
    }
}

nonisolated extension ExtensionComponent {
    /// A file tree's selected file.
    var selectedPath: String? {
        if case .fileTree(let t) = self { t.selected } else { nil }
    }
}

/// The main sections (a file's code, the diffs) on a narrow page, in the side sections' place.
struct CodeMainPage: View {
    @Bindable var model: ExtensionViewModel
    var state: CodePageState
    @Binding var link: LinkTarget?
    var sections: [Int]

    var body: some View {
        Group {
            if let v = model.view {
                let items = sections.filter { $0 < v.sections.count }.flatMap { v.sections[$0].items }
                let codes = items.filter { if case .code = $0 { true } else { false } }
                if codes.count == 1, case .code(let code)? = codes.first {
                    let above = CodePageBlocks.of(v, sections: sections, state: state, look: .unified, canComment: true)
                        .filter { if case .code = $0 { false } else { true } }
                    VStack(spacing: Theme.Space.flush) {
                        if !above.isEmpty {
                            CodeBlockStack(blocks: above, model: model, state: state, look: .unified, link: $link, scrolls: false)
                                .padding(.horizontal, Theme.Space.page)
                        }
                        CodeFileBlock(code: code, model: model, link: $link, fills: true)
                    }
                } else {
                    let _ = state.version
                    CodeBlockStack(blocks: CodePageBlocks.of(v, sections: sections, state: state, look: .unified, canComment: true),
                                   model: model, state: state, look: .unified, link: $link)
                }
            }
        }
        .onDisappear { state.drilledFile = false }
    }
}
