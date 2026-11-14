import Foundation
import Testing
@testable import Persona

/// Topics and the knowledge map (#7251): the web's `groupPoints`, `pointSources`, topic ordering, `buildTopicMap`,
/// `buildProjectMap`, `parseSummary` and `personaLine`, ported to `KBTopics` / `KBMap`, and the outline the phone draws.
@MainActor
struct KBTopicsTests {
    private func point(_ id: String, _ text: String, _ path: [String] = [], kind: String = "fact", sources: [(String, String)] = []) -> KBTopicPoint {
        KBTopicPoint(id: id, path: path, kind: kind, text: text, sources: sources.map { KBTopicSource(kind: $0.0, id: $0.1) })
    }

    private func entry(_ kind: KBKind, _ id: String, _ name: String, importance: String? = nil, extra: String? = nil) -> KBEntry {
        KBEntry(kind: kind.rawValue, id: id, name: name, importance: importance, relevance: 0, extra: extra)
    }

    @Test func pointsGroupBySubtopicInTheOrderTheyFirstAppear() {
        let g = KBTopics.group([
            point("1", "loose"),
            point("2", "a", ["Refresh"]),
            point("3", "b", ["Layout"]),
            point("4", "c", ["Refresh", "Data"]),
            point("5", "d", [" ", "Refresh"]),
            point("6", ""),
        ])
        #expect(g.points.map(\.id) == ["1"])
        #expect(g.groups.map(\.name) == ["Refresh", "Layout"])
        #expect(g.groups[0].points.map(\.id) == ["2", "5"], "a blank part is dropped, not a level")
        #expect(g.groups[0].groups.map(\.path) == [["Refresh", "Data"]])
        #expect(g.size == 5 && g.groups[0].size == 3, "a point without text is skipped")
    }

    @Test func sourcesAreTheLinkedOnesByNameOnceEach() {
        let links = [entry(.projects, "weather", "Weather app"), entry(.chats, "team", "Team chat")]
        let p = point("1", "x", sources: [("projects", "weather"), ("chats", "unknown"), ("projects", "weather"), ("chats", "team")])
        #expect(KBTopics.sources(p, links: links).map(\.name) == ["Weather app", "Team chat"])
        #expect(KBTopics.sources(p, links: nil).isEmpty)
    }

    @Test func kindsHaveWordsAndPitfallsWarn() {
        #expect(KBPointKind.warns("pitfall") && KBPointKind.warns("question") && !KBPointKind.warns("fact"))
        #expect(KBPointKind.label("something-new") == KBPointKind.label(""), "an unknown kind reads as a plain point")
        #expect(KBPointKind.whyLabel("pitfall") != KBPointKind.whyLabel("decision"))
    }

    @Test func aDayShowsTheYearOnlyWhenItIsAnotherOne() throws {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = try #require(TimeZone(identifier: "UTC"))
        let now = try #require(cal.date(from: DateComponents(year: 2026, month: 11, day: 14)))
        #expect(!KBTopics.day("2026-10-30", now: now, calendar: cal).contains("2026"))
        #expect(KBTopics.day("2025-10-30", now: now, calendar: cal).contains("2025"))
        #expect(KBTopics.day("not a day", now: now, calendar: cal) == "not a day")
    }

    @Test func topicsRankByImportanceThenSizeThenName() {
        let list = [entry(.topics, "a", "B topic", importance: "P2", extra: "3"),
                    entry(.topics, "b", "A topic", importance: "P2", extra: "3"),
                    entry(.topics, "c", "Big", importance: "P2", extra: "11"),
                    entry(.topics, "d", "Key", importance: "P0", extra: "1"),
                    entry(.topics, "e", "Unranked", extra: "50")]
        #expect(KBTopics.ordered(list).map(\.id) == ["d", "c", "b", "a", "e"])
        #expect(list[2].topicPoints == 11 && entry(.projects, "p", "P", extra: "11").topicPoints == nil)
    }

    @Test func theTopicMapOpensTheBiggestTopicsAndFoldsLoosePointsBesideSubtopics() {
        let small = KBMap.Topic(entry: entry(.topics, "small", "Small", extra: "1"),
                                detail: KBDetail(kind: "topics", id: "small", path: "", points: [point("1", "only")]))
        let loose = (1...4).map { point("l\($0)", "loose \($0)") }
        let big = KBMap.Topic(entry: entry(.topics, "big", "Big", extra: "6"),
                              detail: KBDetail(kind: "topics", id: "big", path: "", points: loose + [point("s1", "a", ["Sub"]), point("s2", "b", ["Sub"])]))
        let many = (0..<8).map { i in KBMap.Topic(entry: entry(.topics, "t\(i)", "Topic \(i)", extra: "0"), detail: nil) }
        let root = KBMap.topics([small, big] + many)
        #expect(root.kids.first?.id == "topic:big", "the biggest first")
        let bigNode = root.kids[0]
        #expect(bigNode.kids.map(\.title).first == "Sub" && bigNode.kids.last?.id == "topic:big#other", "more than three loose points go under Other")
        #expect(bigNode.kids.last?.kids.count == 4 && bigNode.kids.last?.collapsed == true)
        #expect(bigNode.kids[0].collapsed, "subtopics start closed")
        #expect(bigNode.meta == "6" && bigNode.open?.kind == "topics")
        let smallNode = root.kids[1]
        #expect(smallNode.kids.map(\.kind) == [.point], "few loose points stay beside no subtopics")
        #expect(root.kids.prefix(KBMap.openTopics).allSatisfy { !$0.collapsed } && root.kids.dropFirst(KBMap.openTopics).allSatisfy(\.collapsed))
        #expect(root.pointsUnder.count == 7)
        // The outline: open topics show their kids, closed ones don't; the root is no row.
        let open = KBMap.initiallyOpen(root)
        let rows = KBMap.rows(root, open: open)
        #expect(rows.first?.node.id == "topic:big" && rows.first?.depth == 0)
        #expect(rows.contains { $0.node.id == "topic:big/Sub" && $0.depth == 1 })
        #expect(!rows.contains { $0.node.id.hasPrefix("point:topic:big/Sub") }, "a closed subtopic hides its points")
        #expect(KBMap.rows(root, open: open.union(["topic:big/Sub"])).contains { $0.node.id == "point:topic:big/Sub#s1" && $0.depth == 2 })
    }

    @Test func theProjectMapGroupsActiveByImportanceAndKeepsDoneApart() {
        let list = [entry(.projects, "a", "Alpha", importance: "P0", extra: "short · active"),
                    entry(.projects, "b", "Beta", importance: "P2", extra: "short · active"),
                    entry(.projects, "c", "Gamma", extra: "short · active"),
                    entry(.projects, "d", "Delta", importance: "P1", extra: "long · done"),
                    entry(.projects, "e", "Echo", extra: "short · ignored")]
        let detail = KBDetail(kind: "projects", id: "a", path: "", meta: .object(["goal": .string("Ship it"), "dirs": .array([.string("mac:/code/a")])]),
                              summary: "# Alpha\n\nIntro line\n\n## 现状\n- Widgets done\n  - small and medium\n- Data layer\n\n## 风险\n- Rain bar too light", // i18n-ignore
                              log: ["- 2026-10-30 10:15 · Persona · 下午 · Persona · **Shipped** the beta", "- 2026-10-30 11:00 · Agent · x · y"], // i18n-ignore
                              links: [entry(.chats, "team", "Team chat")])
        let root = KBMap.projects(list, details: ["a": detail])
        #expect(root.kids.map(\.id) == ["branch:active", "branch:done"], "ignored ones stay out; no paused branch without any")
        let active = root.kids[0]
        #expect(active.kids.map(\.title).prefix(2) == ["P0", "P2"] && active.kids.count == 3)
        #expect(!active.kids[0].collapsed && !active.kids[1].collapsed && active.kids[2].collapsed, "the first two groups open")
        let alpha = active.kids[0].kids[0]
        #expect(alpha.title == "Alpha" && alpha.collapsed && alpha.open?.id == "a")
        let titles = alpha.kids.map(\.title)
        #expect(titles.first == String(localized: "Goal [Knowledge Map]"), "the main chat's goal when the summary has none")
        #expect(titles.contains("现状") && titles.contains("风险")) // i18n-ignore
        let status = alpha.kids.first { $0.title == "现状" } // i18n-ignore
        #expect(status?.collapsed == false && status?.kids.first?.kids.first?.title == "small and medium", "nested items, 现状 open") // i18n-ignore
        #expect(alpha.kids.first { $0.title == "风险" }?.collapsed == true) // i18n-ignore
        let latest = alpha.kids.first { $0.id == "project:a#latest" }
        #expect(latest?.kids.first?.title == "Shipped the beta", "the repeated time and Persona go, and emphasis")
        #expect(alpha.kids.contains { $0.id == "project:a#dirs" } && alpha.kids.contains { $0.id == "project:a#chats" })
        #expect(root.kids[1].kids.map(\.title) == ["Delta"] && root.kids[1].collapsed)
    }

    @Test func summarySectionsAndPersonaLinesParseAsTheWebDoes() {
        let s = KBMap.summary("# Name\n\nLead text\n## One\n1. first\n   - under\n2. **second** [link](https://example.com)\n## Empty\n", lead: "Summary")
        #expect(s.map(\.title) == ["Summary", "One"], "text before the first section goes under the lead; empty sections go")
        #expect(s[1].items.map(\.text) == ["first", "second link"] && s[1].items[0].kids.map(\.text) == ["under"])
        #expect(KBMap.personaLine("- 2026-10-30 10:15 · Agent · x")?.text == nil)
        #expect(KBMap.personaLine("2026-10-30 10:15 · Persona · 10-30 09:00 · Persona · ok")?.text == "ok")
    }
}

/// Settings › Knowledge Selection's switch for the curation (#7251): on unless the hub says off, shown changed at once,
/// put back when the hub refuses.
@MainActor
struct KnowledgeTopicsModelTests {
    @Test func offlineItStaysUnreadAndASetWithoutAValueDoesNothing() async {
        var toasts: [Toast] = []
        let m = KnowledgeTopicsModel(deps: AssistantDeps(api: { nil }, report: { _ in }, toast: { toasts.append($0) }, open: { _ in }))
        await m.load()
        #expect(m.value == nil && !m.saving)
        await m.set(false)
        #expect(m.value == nil && toasts.isEmpty)
    }
}
