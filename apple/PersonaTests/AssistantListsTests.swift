import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The Persona page's conversations (`AssistantLists`, behind `AppModel.assistantConversations`): only the group's
/// assistant conversations, newest first, open and archived apart; the same value for a push that changed only agents,
/// so the sidebar is not drawn again by it (#390).
struct AssistantListsTests {
    private func session(_ id: String, assistant: Bool, archived: Bool = false, updated: TimeInterval, group: String = "",
                         text: String = "") -> Session {
        let json = """
        {"machine_id":"m","session_id":"\(id)","title":"\(id)","cwd":"/tmp/p","status":"idle","state":"idle",
         "assistant":\(assistant),"archived":\(archived),"last_text":"\(text)",
         "updated_at":"\(ISO8601DateFormatter().string(from: Date(timeIntervalSince1970: updated)))"}
        """
        do {
            var s = try JSON.decoder.decode(Session.self, from: Data(json.utf8))
            s.group = group
            return s
        } catch { fatalError("bad fixture: \(error)") }
    }

    @Test func theGroupsConversationsNewestFirst() {
        let all = [session("a", assistant: true, updated: 1), session("agent", assistant: false, updated: 9),
                   session("b", assistant: true, updated: 5), session("old", assistant: true, archived: true, updated: 3),
                   session("other", assistant: true, updated: 7, group: "g2")]
        let lists = AssistantLists(all, group: "")
        #expect(lists.open.map(\.sessionID) == ["b", "a"])
        #expect(lists.archived.map(\.sessionID) == ["old"])
        #expect(AssistantLists(all, group: "g2").open.map(\.sessionID) == ["other"])
    }

    @Test func aPushAboutAnAgentLeavesThemEqual() {
        let before = [session("a", assistant: true, updated: 1), session("agent", assistant: false, updated: 2, text: "step 1")]
        let after = [session("a", assistant: true, updated: 1), session("agent", assistant: false, updated: 3, text: "step 2")]
        #expect(AssistantLists(before, group: "") == AssistantLists(after, group: ""))
    }
}
