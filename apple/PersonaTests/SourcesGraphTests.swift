import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Inbox Sources' relationship view (#5741): the graph as the manager answers it, grouped by extension, filtered, and the
/// picked source's details; the pages' row that opens it, which only they may use.
@MainActor
struct SourcesGraphTests {
    static let answer = #"""
        {"extensions":[{"id":"persona","name":"Persona 自带","line":"3 个来源"},{"id":"apps","extension":"apps","name":"连接的应用","line":"2 个来源"},
                       {"id":"telegram","extension":"telegram","name":"Telegram","line":"0 个来源"}],
         "reads":[{"id":"d.persona","extension":"persona","title":"直接读取","direct":true},
                  {"id":"t.github/github/notifications","extension":"persona","title":"列出通知"},
                  {"id":"t.apps/outlook/search_messages","extension":"apps","title":"搜索邮件"},
                  {"id":"t.apps/broker/positions","extension":"apps","title":"持仓"}],
         "sources":[{"id":"b.mail","read":"d.persona","kind":"builtin","name":"邮件","icon":"mail","line":"收件箱、知识库 · 每 3 分钟",
                     "badge":{"text":"可能重复","tone":"caution"},"reading":true,"to":["inbox","kb"],"duplicates":["s.sub_outlook"],"state":"{\"p\":\"builtin\",\"c\":\"mail\"}"},
                    {"id":"b.github","read":"d.persona","kind":"builtin","name":"GitHub","icon":"code","line":"收件箱","reading":true,"to":["inbox"],"state":"{}"},
                    {"id":"s.sub_github","read":"t.github/github/notifications","kind":"subscription","name":"GitHub 通知","icon":"bell","line":"出错",
                     "badge":{"text":"出错","tone":"danger"},"reading":false,"to":["inbox"],"state":"{}","ask":"persona://ask?text=x"},
                    {"id":"s.sub_outlook","read":"t.apps/outlook/search_messages","kind":"subscription","name":"Outlook 邮件","icon":"mail","line":"",
                     "reading":true,"to":["inbox","kb"],"duplicates":["b.mail"],"state":"{\"p\":\"source\",\"id\":\"sub_outlook\"}","ask":"persona://ask?text=y"},
                    {"id":"s.sub_broker","read":"t.apps/broker/positions","kind":"subscription","name":"券商持仓","icon":"wallet","line":"","reading":true,"to":["kb"],"state":"{}"}],
         "destinations":[{"id":"inbox","name":"收件箱","line":"4 个来源 · 3 个在读","sources":4,"reading":3},
                         {"id":"kb","name":"知识库","line":"3 个来源 · 3 个在读","sources":3,"reading":3}]}
        """#

    static func model(_ text: String = answer) throws -> SourcesGraphModel {
        let graph = try JSONDecoder().decode(SourcesGraph.self, from: Data(text.utf8))
        return SourcesGraphModel(deps: .init(load: { graph }))
    }

    @Test func groupsFollowTheServersOrderAndLeaveEmptyExtensionsOut() async throws {
        let m = try Self.model()
        await m.load()
        #expect(m.groups.map(\.id) == ["persona", "apps"], "Telegram has no source to show")
        #expect(m.groups[0].sources.map(\.id) == ["b.mail", "b.github", "s.sub_github"])
        #expect(m.read(of: m.groups[0].sources[0])?.direct == true)
        #expect(m.ext(of: m.groups[1].sources[0])?.name == "连接的应用")
    }

    @Test func filtersCountAndKeepTheirSources() async throws {
        let m = try Self.model()
        await m.load()
        #expect(m.count(.all) == 5)
        #expect(m.count(.inbox) == 4)
        #expect(m.count(.kb) == 3)
        #expect(m.count(.idle) == 1)
        m.filter = .idle
        #expect(m.groups.map(\.id) == ["persona"])
        #expect(m.groups.flatMap(\.sources).map(\.id) == ["s.sub_github"])
        m.filter = .kb
        #expect(m.groups.flatMap(\.sources).map(\.id) == ["b.mail", "s.sub_outlook", "s.sub_broker"])
    }

    @Test func thePickedSourcesDetails() async throws {
        let m = try Self.model()
        await m.load()
        m.picked = "s.sub_outlook"
        let s = try #require(m.pickedSource)
        #expect(m.duplicate(of: s) == "邮件")
        #expect(m.destinations(of: s).contains("收件箱") && m.destinations(of: s).contains("知识库"))
        #expect(s.ask.flatMap(URL.init(string:)).flatMap(SubscriptionsUI.askText) == "y")
        #expect(s.state == #"{"p":"source","id":"sub_outlook"}"#)
    }

    @Test func aPickedSourceThatIsGoneUnpicks() async throws {
        var text = Self.answer
        let m = SourcesGraphModel(deps: .init(load: { try JSONDecoder().decode(SourcesGraph.self, from: Data(text.utf8)) }))
        await m.load()
        m.picked = "s.sub_broker"
        text = text.replacingOccurrences(of: "s.sub_broker", with: "s.sub_other")
        await m.load()
        #expect(m.picked == nil)
    }

    /// The view's task restarts when subscriptions change: the load it cancels shows no failure, the next one the graph.
    @Test func aReplacedLoadIsNoFailure() async throws {
        let graph = try JSONDecoder().decode(SourcesGraph.self, from: Data(Self.answer.utf8))
        let m = SourcesGraphModel(deps: .init(load: {
            try await Task.sleep(for: .seconds(30))
            return graph
        }))
        let first = Task { await m.load() }
        first.cancel()
        await first.value
        #expect(m.failed == nil && m.graph == nil)
        let failing = SourcesGraphModel(deps: .init(load: { throw URLError(.timedOut) }))
        await failing.load()
        #expect(failing.failed != nil, "a load that fails on its own still says so")
    }

    @Test func onlyThePagesOpenTheView() throws {
        let url = try #require(URL(string: "persona://sources-graph"))
        #expect(SubscriptionsUI.isGraph(url))
        #expect(!SubscriptionsUI.isGraph(try #require(URL(string: "persona://sources-graph/x"))))
        #expect(!SubscriptionsUI.isGraph(try #require(URL(string: "https://sources-graph"))))
        #expect(ExtensionViewModel.opens(url, item: nil, owner: SubscriptionsUI.id))
        #expect(!ExtensionViewModel.opens(url, item: nil, owner: "some-extension"))
    }

    @Test func thePagesAreToldThisAppHasTheView() throws {
        let request = ExtensionUIRequest(context: ExtensionUIContext.here(), state: nil, action: nil, values: nil)
        let body = try JSONEncoder().encode(SubscriptionsUI.Request(request: request, assistant: nil))
        let context = try #require((try JSONSerialization.jsonObject(with: body) as? [String: Any])?["context"] as? [String: Any])
        #expect(context["graph"] as? Bool == true)
    }
}
