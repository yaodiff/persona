import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The personal-agents view prefs: same arrangement as the web's arrangeSessions (web/src/lib/agents.test.ts).
@MainActor struct PersonalAgentPrefsTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private func s(_ id: String, _ provider: String, hoursAgo: Double, unread: Int = 0, title: String? = nil, canSend: Bool = true) -> PersonalAgentSession {
        PersonalAgentSession(id: id, source: "lark-bridge", sessionKey: id, provider: provider, title: title ?? id, canSend: canSend,
                             unreadCount: unread, rankTimeMs: Int64((now.timeIntervalSince1970 - hoursAgo * 3600) * 1000))
    }
    private var list: [PersonalAgentSession] {
        [s("a", "muse", hoursAgo: 0.01, unread: 1, title: "Alpha"), s("b", "grok_bot", hoursAgo: 0.02, unread: 3, title: "Beta"),
         s("c", "grok_bot", hoursAgo: 60, title: "Charlie"), s("d", "muse", hoursAgo: 24 * 90, title: "Delta", canSend: false)]
    }
    private func ids(_ g: [PersonalAgentGroup]) -> [[String]] { g.map { $0.sessions.map(\.id) } }

    @Test func pinsComeFirstWhateverTheGrouping() {
        var p = PersonalAgentPrefs()
        p.pins = ["c", "a", "gone"]
        p.view.group = "provider"
        let g = PersonalAgentArrange.arrange(list, prefs: p, now: now)
        #expect(g.first?.id == "pinned" && g.first?.label == "已置顶")
        #expect(ids(g) == [["c", "a"], ["b"], ["d"]])
    }

    @Test func sortAndShowFilters() {
        var p = PersonalAgentPrefs()
        p.view.group = "none"
        p.view.sort = "unread"
        #expect(ids(PersonalAgentArrange.arrange(list, prefs: p, now: now)) == [["b", "a", "c", "d"]])
        p.view.show = "sendable"
        #expect(ids(PersonalAgentArrange.arrange(list, prefs: p, now: now)).flatMap { $0 } == ["b", "a", "c"])
        p.view.show = "unread"
        #expect(ids(PersonalAgentArrange.arrange(list, prefs: p, query: "alp", now: now)).flatMap { $0 } == ["a"])
        p.resetView()
        #expect(p.view.isDefault)
    }

    @Test func sectionsAndProviderToggle() {
        var p = PersonalAgentPrefs()
        p.addSection("工作", id: "w")
        p.assign("c", to: "w")
        p.view.group = "none"
        #expect(PersonalAgentArrange.arrange(list, prefs: p, now: now).map(\.id) == ["sec:w", "all"])
        p.removeSection("w")
        #expect(p.assign.isEmpty)
        p.toggleProvider("muse", all: ["muse", "grok_bot"])
        #expect(p.view.providerFilter == ["grok_bot"])
        p.toggleProvider("muse", all: ["muse", "grok_bot"])
        #expect(p.view.providerFilter.isEmpty)
    }

    @Test func decodesWhatTheHubSends() throws {
        let p = try JSON.decoder.decode(PersonalAgentPrefs.self, from: Data(#"{"pins":["x"],"view":{"sort":"name"},"updatedAt":"2026-10-31T00:00:00Z"}"#.utf8))
        #expect(p.pins == ["x"] && p.view.sortBy == .name && p.sections.isEmpty)
        #expect(try JSON.decoder.decode(PersonalAgentPrefs.self, from: Data("{}".utf8)) == PersonalAgentPrefs())
    }
}
