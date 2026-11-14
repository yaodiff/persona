import Foundation

// Topics (#3783, #7251; docs/assistant/knowledge.md "Topics"): knowledge points Persona curated by subject, as the web's
// `lib/kb-points.ts` and `lib/kb-map.ts` read them. The topic page groups a topic's points by subtopic; the knowledge map is
// one tree of topics (or of projects) that the phone draws as an outline and the iPad as an outline beside a detail column.

/// What a knowledge point is; the hub may add kinds, and an unknown one reads as a plain point.
nonisolated enum KBPointKind {
    static let known = ["concept", "decision", "practice", "pitfall", "fact", "question"]

    static func label(_ kind: String) -> String {
        switch kind {
        case "concept": String(localized: "Concept [Knowledge Point]")
        case "decision": String(localized: "Decision [Knowledge Point]")
        case "practice": String(localized: "How To [Knowledge Point]")
        case "pitfall": String(localized: "Pitfall [Knowledge Point]")
        case "fact": String(localized: "Fact [Knowledge Point]")
        case "question": String(localized: "Open Question [Knowledge Point]")
        default: String(localized: "Point [Knowledge Point]")
        }
    }

    /// Pitfalls and open questions are what to watch out for: they take the warning tone.
    static func warns(_ kind: String) -> Bool { kind == "pitfall" || kind == "question" }

    /// What `why` is called: a pitfall's cause, anything else's reason.
    static func whyLabel(_ kind: String) -> String {
        kind == "pitfall" ? String(localized: "Cause [Knowledge Point]") : String(localized: "Why [Knowledge Point]")
    }
}

/// A topic's points under one subtopic path ([] for the points directly under the topic), and its subtopics' groups.
nonisolated struct KBPointGroup: Hashable, Sendable {
    var path: [String] = []
    var points: [KBTopicPoint] = []
    var groups: [KBPointGroup] = []

    var name: String { path.last ?? "" }
    /// Every point under it, its subtopics' too.
    var size: Int { points.count + groups.reduce(0) { $0 + $1.size } }
}

nonisolated enum KBTopics {
    /// A topic's points as the page shows them: the ones directly under the topic first, then one group per subtopic in
    /// the order the subtopics first appear. Blank path parts are dropped, and points without text.
    static func group(_ points: [KBTopicPoint]?) -> KBPointGroup {
        var root = KBPointGroup()
        for p in points ?? [] where !p.text.isEmpty {
            let path = (p.path ?? []).map { $0.trimmingCharacters(in: .whitespaces) }.filter { !$0.isEmpty }
            insert(p, at: path[...], into: &root)
        }
        return root
    }

    private static func insert(_ p: KBTopicPoint, at path: ArraySlice<String>, into g: inout KBPointGroup) {
        guard let name = path.first else { g.points.append(p); return }
        if let i = g.groups.firstIndex(where: { $0.name == name }) {
            insert(p, at: path.dropFirst(), into: &g.groups[i])
        } else {
            var sub = KBPointGroup(path: g.path + [name])
            insert(p, at: path.dropFirst(), into: &sub)
            g.groups.append(sub)
        }
    }

    /// A point's sources that the topic links, by name; unknown ones are left out (no raw ids on screen).
    static func sources(_ p: KBTopicPoint, links: [KBEntry]?) -> [KBEntry] {
        var seen = Set<String>()
        var out: [KBEntry] = []
        for s in p.sources ?? [] {
            guard let e = (links ?? []).first(where: { $0.kind == s.kind && $0.id == s.id }), seen.insert(s.kind + "/" + s.id).inserted else { continue }
            out.append(e)
        }
        return out
    }

    /// A point's day (YYYY-MM-DD) as a short date: month and day this year, with the year otherwise.
    static func day(_ day: String, now: Date = .now, calendar: Calendar = .current) -> String {
        let parts = day.split(separator: "-").compactMap { Int($0) }
        guard parts.count == 3, let d = calendar.date(from: DateComponents(year: parts[0], month: parts[1], day: parts[2])) else { return day }
        let thisYear = calendar.component(.year, from: now) == parts[0]
        return d.formatted(thisYear ? .dateTime.month(.abbreviated).day() : .dateTime.year().month(.abbreviated).day())
    }

    /// The topics list: by importance (P0 first, unranked last), then the most points, then by name.
    static func ordered(_ topics: [KBEntry]) -> [KBEntry] {
        let rank = { (e: KBEntry) in ["P0", "P1", "P2", "P3"].firstIndex(of: e.importance ?? "") ?? 9 }
        return topics.sorted { a, b in
            if rank(a) != rank(b) { return rank(a) < rank(b) }
            if (a.topicPoints ?? 0) != (b.topicPoints ?? 0) { return (a.topicPoints ?? 0) > (b.topicPoints ?? 0) }
            return a.name.localizedStandardCompare(b.name) == .orderedAscending
        }
    }
}

// MARK: - The knowledge map

/// The entry a map node opens.
nonisolated struct KBMapTarget: Hashable, Sendable {
    var kind: String
    var id: String
    var name: String
    var entry: KBEntry { KBEntry(kind: kind, id: id, name: name, relevance: 0) }
}

/// One node of the knowledge map: a topic or a subtopic, a point, a project and its parts. `collapsed` is how it starts.
nonisolated struct KBMapNode: Identifiable, Hashable, Sendable {
    enum Kind: Hashable, Sendable { case root, branch, group, project, section, text, link, day, topic, point }
    /// The branch's color: topics cycle through three, projects' are by status.
    enum Tone: Hashable, Sendable { case accent, green, orange }

    var id: String
    var title: String
    var kind: Kind
    var tone: Tone
    var kids: [KBMapNode] = []
    var collapsed = false
    /// Beside the title: a count, or a project's importance and status.
    var meta: String?
    /// What the detail column says about it (a topic's one line, a note's whole text).
    var body: String?
    var open: KBMapTarget?
    var point: KBTopicPoint?
    /// The topic's links, where a point's sources are named.
    var links: [KBEntry] = []
    /// The topic and subtopics above it, for the crumbs.
    var crumbs: [String] = []
    /// A topic or subtopic (counts and the Other group too).
    var isTopic: Bool { kind == .branch || kind == .topic }

    /// Every point under it, its subtopics' too.
    var pointsUnder: [KBMapNode] { point != nil ? [self] : kids.flatMap(\.pointsUnder) }
}

/// A row of the outline: the node, how deep it is, and whether it has rows under it to open.
nonisolated struct KBOutlineRow: Identifiable, Hashable, Sendable {
    var node: KBMapNode
    var depth: Int
    var id: String { node.id }
    var folds: Bool { !node.kids.isEmpty }
}

nonisolated enum KBMap {
    /// Topics with more loose points than this, beside subtopics, keep them under one closed Other.
    static let looseShown = 3
    /// How many of the biggest topics start open.
    static let openTopics = 6

    struct Topic: Sendable {
        var entry: KBEntry
        var detail: KBDetail?
    }

    /// How many points a topic has: its detail's, else what its list entry says.
    static func size(_ t: Topic) -> Int {
        t.detail?.points != nil ? KBTopics.group(t.detail?.points).size : (t.entry.topicPoints ?? 0)
    }

    /// The topic tree: the root, one branch per topic (biggest first), subtopics, points. At first it shows structure:
    /// the biggest topics open onto their subtopics, every subtopic closed.
    static func topics(_ topics: [Topic]) -> KBMapNode {
        let tones: [KBMapNode.Tone] = [.accent, .green, .orange]
        let order = topics.sorted { a, b in
            size(a) != size(b) ? size(a) > size(b) : a.entry.name.localizedStandardCompare(b.entry.name) == .orderedAscending
        }
        let branches = order.enumerated().map { i, t in
            var n = group(KBTopics.group(t.detail?.points), name: t.entry.name, id: "topic:" + t.entry.id, tone: tones[i % tones.count],
                          depth: 1, links: t.detail?.links ?? [], crumbs: [])
            n.body = t.entry.oneLine
            n.open = KBMapTarget(kind: KBKind.topics.rawValue, id: t.entry.id, name: t.entry.name)
            n.collapsed = i >= openTopics
            if t.detail == nil { n.meta = String(size(t)) }
            return n
        }
        return KBMapNode(id: "root:topics", title: String(localized: "Knowledge [Knowledge Map]"), kind: .root, tone: .accent, kids: branches,
                         meta: String(localized: "by topic [Knowledge Map]"))
    }

    private static func group(_ g: KBPointGroup, name: String, id: String, tone: KBMapNode.Tone, depth: Int, links: [KBEntry],
                              crumbs: [String]) -> KBMapNode {
        let here = crumbs + [name]
        let subs = g.groups.map { group($0, name: $0.name, id: id + "/" + $0.name, tone: tone, depth: depth + 1, links: links, crumbs: here) }
        let points = g.points.map {
            KBMapNode(id: "point:" + id + "#" + $0.id, title: $0.text, kind: .point, tone: tone, point: $0, links: links, crumbs: here)
        }
        // Beside subtopics a handful of loose points stay; more go under one closed Other, so they don't bury the subtopics.
        let kids = !subs.isEmpty && points.count > looseShown
            ? subs + [KBMapNode(id: id + "#other", title: String(localized: "Other [Knowledge Map]"), kind: .topic, tone: tone, kids: points,
                                collapsed: true, meta: String(points.count),
                                body: String(localized: "Points directly under “\(name)”, not in a subtopic"), crumbs: here)]
            : subs + points
        return KBMapNode(id: id, title: name, kind: depth == 1 ? .branch : .topic, tone: tone, kids: kids, collapsed: depth > 1,
                         meta: String(g.size), crumbs: crumbs)
    }

    // MARK: Projects

    /// A timeline line the main chat wrote ("<time> · Persona · <text>"), or nil; repeats of the time and "Persona" at the
    /// start of its text go, and emphasis marks.
    static func personaLine(_ raw: String) -> (when: String, text: String)? {
        var t = raw.trimmingCharacters(in: .whitespaces)
        if t.hasPrefix("-") { t = String(t.dropFirst()).trimmingCharacters(in: .whitespaces) }
        let parts = t.components(separatedBy: " · ")
        guard parts.count >= 3, parts[1] == "Persona", parts[0].count == 16, parts[0].first?.isNumber == true else { return nil }
        var text = parts[2...].joined(separator: " · ")
        while true {
            let next = stripLead(text)
            if next == text || next.isEmpty { break }
            text = next
        }
        text = text.replacingOccurrences(of: #"[*`#]+"#, with: "", options: .regularExpression)
            .replacingOccurrences(of: #"\s+"#, with: " ", options: .regularExpression).trimmingCharacters(in: .whitespaces)
        return (parts[0], text)
    }

    /// The main chat's repeated "<day> <time> <part of day> · Persona · " at the start of a note.
    private static func stripLead(_ s: String) -> String {
        // i18n-ignore: the part-of-day words the main chat writes in its Chinese timeline lines
        let pattern = #"^((\d{4}-)?\d{2}-\d{2}[ T]?)?(\d{2}:\d{2})?\s*(早间|午间|晚间|上午|下午)?\s*([·:：]\s*)?(Persona\s*·\s*)?"# // i18n-ignore
        guard let r = s.range(of: pattern, options: .regularExpression), !r.isEmpty else { return s }
        return String(s[r.upperBound...])
    }

    struct SummaryItem: Hashable, Sendable {
        var text: String
        var kids: [SummaryItem] = []
    }

    struct SummarySection: Hashable, Sendable {
        var title: String
        var items: [SummaryItem]
    }

    /// A summary.md as its "## " sections of list items (nested two spaces a level; other lines items of their own). The
    /// "# <name>" heading and emphasis go; text before the first section goes under `lead`.
    static func summary(_ md: String, lead: String) -> [SummarySection] {
        var out: [SummarySection] = []
        // where each open list item is, by indent, as a path of indexes into the current section
        var stack: [(indent: Int, path: [Int])] = []
        for raw in md.components(separatedBy: "\n") {
            let trimmed = raw.trimmingCharacters(in: .whitespaces)
            if trimmed.isEmpty || raw.hasPrefix("# ") { continue }
            if trimmed.hasPrefix("##"), let r = trimmed.range(of: #"^#{2,}\s+"#, options: .regularExpression) {
                out.append(SummarySection(title: clean(String(trimmed[r.upperBound...])), items: []))
                stack = []
                continue
            }
            if out.isEmpty { out.append(SummarySection(title: lead, items: [])) }
            let listItem = raw.range(of: #"^(\s*)([-*+]|\d+[.)])\s+"#, options: .regularExpression)
            let indent = listItem.map { raw[$0].prefix { $0 == " " || $0 == "\t" }.replacingOccurrences(of: "\t", with: "  ").count } ?? 0
            let text = clean(listItem.map { String(raw[$0.upperBound...]) } ?? raw)
            guard !text.isEmpty else { continue }
            while let last = stack.last, last.indent >= indent { stack.removeLast() }
            let s = out.count - 1
            if listItem != nil, let parent = stack.last {
                let path = append(SummaryItem(text: text), under: parent.path, in: &out[s].items)
                stack.append((indent, path))
            } else {
                out[s].items.append(SummaryItem(text: text))
                if listItem != nil { stack.append((indent, [out[s].items.count - 1])) }
            }
        }
        return out.filter { !$0.items.isEmpty }
    }

    private static func append(_ item: SummaryItem, under path: [Int], in items: inout [SummaryItem]) -> [Int] {
        guard let first = path.first else { items.append(item); return [items.count - 1] }
        return [first] + append(item, under: Array(path.dropFirst()), in: &items[first].kids)
    }

    private static func clean(_ s: String) -> String {
        s.replacingOccurrences(of: #"\*\*|__|`"#, with: "", options: .regularExpression)
            .replacingOccurrences(of: #"\[([^\]]+)\]\([^)]+\)"#, with: "$1", options: .regularExpression)
            .replacingOccurrences(of: #"\s+"#, with: " ", options: .regularExpression).trimmingCharacters(in: .whitespaces)
    }

    // The curated summary's section names are the ones its prompt asks for (server/internal/prompts/kb/project.md): text the
    // hub writes in Chinese, matched to know the goal and the sections open at first.
    private static let goalTitle = "目标" // i18n-ignore
    private static let openTitles: Set<String> = ["目标", "现状"] // i18n-ignore
    private static let roleTitle = "我的角色" // i18n-ignore

    static func status(_ e: KBEntry, _ d: KBDetail?) -> String {
        let s = d?.meta?["status"]?.string?.trimmingCharacters(in: .whitespaces) ?? ""
        if !s.isEmpty { return s }
        let fromList = (e.extra ?? "").components(separatedBy: " · ").dropFirst().first?.trimmingCharacters(in: .whitespaces) ?? ""
        return fromList.isEmpty ? "active" : fromList
    }

    static func statusLabel(_ s: String) -> String {
        switch s {
        case "done": String(localized: "Done [Knowledge Map]")
        case "paused": String(localized: "Paused [Knowledge Map]")
        default: String(localized: "Active [Knowledge Map]")
        }
    }

    /// The projects tree: active ones grouped by importance (the first two groups open), then done and paused ones
    /// (closed). Merged and "not a project" ones stay out, as in the list. A project without its detail still has a row.
    static func projects(_ list: [KBEntry], details: [String: KBDetail]) -> KBMapNode {
        let importance = ["P0", "P1", "P2", "P3"]
        let live = list.filter { !["merged", "ignored"].contains(status($0, details[$0.id])) }
        let rank = { (e: KBEntry) in importance.firstIndex(of: e.importance ?? "") ?? 9 }
        let sorted = live.sorted { a, b in
            rank(a) != rank(b) ? rank(a) < rank(b) : (a.updated ?? .distantPast) > (b.updated ?? .distantPast)
        }
        let active = sorted.filter { !["done", "paused"].contains(status($0, details[$0.id])) }
        var groupOrder: [String] = []
        var groups: [String: [KBMapNode]] = [:]
        for e in active {
            let g = importance.contains(e.importance ?? "") ? e.importance! : String(localized: "Not Rated [Knowledge Map]")
            if groups[g] == nil { groupOrder.append(g) }
            groups[g, default: []].append(project(e, details[e.id], tone: .accent))
        }
        let activeKids = groupOrder.enumerated().map { i, g in
            KBMapNode(id: "group:" + g, title: g, kind: .group, tone: .accent, kids: groups[g] ?? [], collapsed: i >= 2, meta: String(groups[g]?.count ?? 0))
        }
        var branches = [KBMapNode(id: "branch:active", title: String(localized: "Active [Knowledge Map]"), kind: .branch, tone: .accent,
                                  kids: activeKids, meta: String(active.count))]
        for (key, title, tone) in [("done", String(localized: "Done [Knowledge Map]"), KBMapNode.Tone.green),
                                   ("paused", String(localized: "Paused [Knowledge Map]"), .orange)] {
            let these = sorted.filter { status($0, details[$0.id]) == key }
            if !these.isEmpty {
                branches.append(KBMapNode(id: "branch:" + key, title: title, kind: .branch, tone: tone,
                                          kids: these.map { project($0, details[$0.id], tone: tone) }, collapsed: true, meta: String(these.count)))
            }
        }
        return KBMapNode(id: "root:projects", title: String(localized: "Projects [Knowledge Map]"), kind: .root, tone: .accent, kids: branches,
                         meta: String(localized: "by status [Knowledge Map]"))
    }

    private static func project(_ e: KBEntry, _ d: KBDetail?, tone: KBMapNode.Tone) -> KBMapNode {
        let base = "project:" + e.id
        let meta = d?.meta
        let lines = (d?.log ?? []).compactMap(personaLine).reversed()
        let goal = meta?["goal"]?.string?.trimmingCharacters(in: .whitespaces) ?? ""
        let role = meta?["my_role"]?.string?.trimmingCharacters(in: .whitespaces) ?? ""
        let sections = summary(d?.summary ?? "", lead: String(localized: "Summary [Knowledge Map]"))
        func item(_ i: SummaryItem, _ id: String) -> KBMapNode {
            KBMapNode(id: id, title: i.text, kind: .text, tone: tone, kids: i.kids.enumerated().map { item($1, id + "." + String($0)) },
                      collapsed: !i.kids.isEmpty, body: i.text)
        }
        var kids: [KBMapNode] = []
        // The summary's own sections first (its goal is the fuller one); the main chat's one-line goal / role only without.
        if !goal.isEmpty, !sections.contains(where: { $0.title == goalTitle }) {
            kids.append(KBMapNode(id: base + "#goal", title: String(localized: "Goal [Knowledge Map]"), kind: .section, tone: tone,
                                  kids: [KBMapNode(id: base + "#goal.0", title: goal, kind: .text, tone: tone, body: goal)]))
        }
        for (i, s) in sections.enumerated() {
            let id = base + "#s" + String(i)
            kids.append(KBMapNode(id: id, title: s.title, kind: .section, tone: tone, kids: s.items.enumerated().map { item($1, id + "." + String($0)) },
                                  collapsed: !openTitles.contains(s.title), meta: String(localized: "Summary [Knowledge Map]")))
        }
        if !role.isEmpty, !sections.contains(where: { $0.title == roleTitle }) {
            kids.append(KBMapNode(id: base + "#role", title: String(localized: "My Role [Knowledge Map]"), kind: .section, tone: tone,
                                  kids: [KBMapNode(id: base + "#role.0", title: role, kind: .text, tone: tone, body: role)]))
        }
        if let latest = lines.first {
            kids.append(KBMapNode(id: base + "#latest", title: String(localized: "Latest [Knowledge Map]"), kind: .section, tone: tone,
                                  kids: [KBMapNode(id: base + "#latest.0", title: latest.text, kind: .text, tone: tone, meta: latest.when, body: latest.text)],
                                  meta: String(latest.when.dropFirst(5))))
            var dayOrder: [String] = []
            var byDay: [String: [(when: String, text: String)]] = [:]
            for l in lines {
                let day = String(l.when.prefix(10))
                if byDay[day] == nil { dayOrder.append(day) }
                byDay[day, default: []].append(l)
            }
            let days = dayOrder.map { day in
                KBMapNode(id: base + "#day" + day, title: String(day.dropFirst(5)), kind: .day, tone: tone,
                          kids: (byDay[day] ?? []).enumerated().map { j, l in
                              KBMapNode(id: base + "#day" + day + "." + String(j), title: String(l.when.dropFirst(11)) + " · " + l.text, kind: .text,
                                        tone: tone, meta: l.when, body: l.text)
                          }, collapsed: true, meta: String(byDay[day]?.count ?? 0))
            }
            kids.append(KBMapNode(id: base + "#notes", title: String(localized: "Main Chat Notes [Knowledge Map]"), kind: .section, tone: tone,
                                  kids: days, collapsed: true, meta: String(lines.count)))
        }
        let dirs = meta?["dirs"]?.array?.compactMap(\.string) ?? []
        if !dirs.isEmpty {
            kids.append(KBMapNode(id: base + "#dirs", title: String(localized: "Code Folders [Knowledge Map]"), kind: .section, tone: tone,
                                  kids: dirs.map { KBMapNode(id: base + "#dir" + $0, title: $0, kind: .text, tone: tone) }, collapsed: true,
                                  meta: String(dirs.count)))
        }
        let links = d?.links ?? []
        for (kind, title) in [(KBKind.chats.rawValue, String(localized: "Related Chats [Knowledge Map]")),
                              (KBKind.people.rawValue, String(localized: "Related People [Knowledge Map]"))] {
            let these = links.filter { $0.kind == kind }
            if !these.isEmpty {
                kids.append(KBMapNode(id: base + "#" + kind, title: title, kind: .section, tone: tone,
                                      kids: these.map { KBMapNode(id: base + "#" + kind + "." + $0.id, title: $0.name, kind: .link, tone: tone,
                                                                  open: KBMapTarget(kind: $0.kind, id: $0.id, name: $0.name)) },
                                      collapsed: true, meta: String(these.count)))
            }
        }
        let st = status(e, d)
        return KBMapNode(id: base, title: e.name, kind: .project, tone: tone, kids: kids, collapsed: true,
                         meta: [e.importance ?? "", statusLabel(st)].filter { !$0.isEmpty }.joined(separator: " · "), body: e.oneLine,
                         open: KBMapTarget(kind: KBKind.projects.rawValue, id: e.id, name: e.name))
    }

    // MARK: Outline

    /// The nodes the outline shows, depth first: a node's kids only while it is open. The root is not a row.
    static func rows(_ root: KBMapNode, open: Set<String>) -> [KBOutlineRow] {
        var out: [KBOutlineRow] = []
        func walk(_ n: KBMapNode, _ depth: Int) {
            out.append(KBOutlineRow(node: n, depth: depth))
            if open.contains(n.id) { for k in n.kids { walk(k, depth + 1) } }
        }
        for k in root.kids { walk(k, 0) }
        return out
    }

    /// What starts open: every node that has kids and is not collapsed.
    static func initiallyOpen(_ root: KBMapNode) -> Set<String> {
        var out = Set<String>()
        func walk(_ n: KBMapNode) {
            if !n.kids.isEmpty, !n.collapsed { out.insert(n.id) }
            n.kids.forEach(walk)
        }
        walk(root)
        return out
    }
}
