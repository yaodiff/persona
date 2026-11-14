import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A card on no session (`personad ask` outside herdr, #4059) can be answered in the app, and Agents' Needs your input lists
/// every card its badge counts.
@MainActor
struct SessionlessCardTests {
    private func card(_ id: String, kind: InteractionKind, session: String = "", title: String? = nil, cwd: String? = nil) -> Interaction {
        let questions = kind == .question ? [Question(question: "继续部署吗？", header: nil, multiSelect: false, options: [])] : nil
        return Interaction(
            id: id, machineID: "m", sessionID: session, sessionTitle: title, cwd: cwd,
            kind: kind, createdAt: Date(timeIntervalSince1970: 1), expiresAt: nil, toolName: kind == .permission ? "Bash" : nil,
            toolUseID: nil, questions: questions, plan: nil, toolInput: nil, summary: kind == .permission ? "make deploy" : nil,
            screen: nil, dialog: nil
        )
    }

    private func session(_ id: String, source: String? = nil, approve: Bool? = nil) -> Session {
        var extra = ""
        if let source { extra += #","source_kind":"\#(source)""# }
        if let approve { extra += #","capabilities":{"approve":\#(approve)}"# }
        let json = #"{"machine_id":"m","session_id":"\#(id)","title":"\#(id)","cwd":"/tmp/p","status":"idle","state":"needs_you"\#(extra)}"#
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) } catch { fatalError("bad fixture: \(error)") }
    }

    @Test func aSessionlessCardCanBeAnswered() {
        #expect(card("q", kind: .question).canAnswer(session: nil))
        #expect(card("p", kind: .permission).canAnswer(session: nil))
        #expect(AppModel().canAnswer(card("q", kind: .question)))
    }

    @Test func aCardOnASessionFollowsItsSource() {
        #expect(card("p", kind: .permission, session: "s").canAnswer(session: session("s")))
        #expect(card("p", kind: .permission, session: "s").canAnswer(session: session("s", source: "codex", approve: true)))
        #expect(!card("p", kind: .permission, session: "s").canAnswer(session: session("s", source: "codex", approve: false)))
        #expect(!card("p", kind: .permission, session: "s").canAnswer(session: nil), "its session not known yet")
    }

    @Test(arguments: [InteractionKind.question, .permission])
    func resolveSendsASessionlessAnswer(_ kind: InteractionKind) async {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/interactions/ix-\(kind.rawValue)/resolve", json: #"{"ok":true}"#)
        let model = AppModel()
        model.apiForTests = hub.client
        let ix = card("ix-\(kind.rawValue)", kind: kind)
        model.replaceInteractions("", [ix])
        await model.resolve(ix, kind == .question ? Resolution(answers: ["继续部署吗？": "继续"]) : Resolution(allow: true))
        #expect(hub.sent("POST", "/api/v1/interactions/ix-\(kind.rawValue)/resolve").count == 1)
        #expect(model.pendingInteractions.isEmpty)
    }

    /// The badge counts the section's rows (#3580): a session's two cards are one row, a sessionless card one of its own.
    @Test func theBadgeCountsWhatNeedsYouLists() {
        let model = AppModel()
        model.applySessions([MachineRef(group: "", id: "m"): [session("s")]])
        let cards = [card("on-session", kind: .permission, session: "s"), card("also-on-session", kind: .question, session: "s"),
                     card("script", kind: .question, cwd: "/srv/deploy")]
        model.replaceInteractions("", cards)
        let listed = AgentsNeedsYou.cards(model.pendingInteractions, session: model.session)
        #expect(listed.map(\.id).sorted() == ["also-on-session", "on-session", "script"])
        let rows = AgentsNeedsYou.rows(cards: listed, waiting: AgentsNeedsYou.waiting(model.allSessions))
        // The device's saved agents (another run's) may wait too: machine m's rows are this test's.
        #expect(rows.filter { $0.key.machineID == "m" }.map(\.id).sorted() == ["agent:m/s", "card:script"])
        // The badge counts the rows the page lists: the current assistant's, where the device's saved hub has several.
        let shown = AgentsNeedsYou.rows(cards: AgentsNeedsYou.cards(model.scopedInteractions, session: model.session),
                                        waiting: AgentsNeedsYou.waiting(model.scoped(model.allSessions)))
        #expect(model.badges.agents == shown.count)
    }

    @Test func aSessionlessRowSaysWhereItComesFrom() {
        #expect(card("a", kind: .question, title: "部署脚本", cwd: "/srv/deploy").sessionlessPlace == "部署脚本")
        #expect(card("b", kind: .question, cwd: "/srv/deploy").sessionlessPlace == "deploy")
        #expect(card("c", kind: .question).sessionlessPlace == nil)
        #expect(card("d", kind: .question, cwd: "/srv/deploy").placeLine(machine: "MacBook") == "脚本 · deploy · MacBook")
        var labelled = card("e", kind: .question, cwd: "/srv/deploy")
        labelled.agentLabelHint = "脚本" // its title already says A script is asking you
        #expect(labelled.placeLine(machine: "MacBook") == "deploy · MacBook")
        #expect(card("f", kind: .question, session: "s", title: "补测试").placeLine(machine: "MacBook") == "补测试 · MacBook")
    }
}
