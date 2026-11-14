import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The Agents list's row styles (#2530): one choice kept in the two stored values it always was, and the place line the
/// styles draw (no agent kind, the machine only when the list names it).
@MainActor struct AgentRowStyleTests {
    private func session(_ fields: String) -> Session {
        let json = #"{"machine_id":"m","session_id":"s","title":"Ship the widget","cwd":"/w","status":"working","state":"working""# + fields + "}"
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) } catch { fatalError("bad fixture: \(error)") }
    }

    @Test func aDisplayIsAStyleOrCompactList() {
        #expect(AgentListDisplay(style: .avatar, list: false) == .avatar)
        #expect(AgentListDisplay(style: .compact, list: false) == .compact)
        #expect(AgentListDisplay(style: .cards, list: false) == .cards)
        // Compact List whatever style was chosen before it, which it keeps.
        #expect(AgentListDisplay(style: .cards, list: true) == .list)
        #expect(AgentListDisplay.list.style == nil)
        #expect(AgentListDisplay.allCases.compactMap(\.style) == AgentRowStyle.allCases)
        #expect(AgentRowStyle.standard == .avatar)
    }

    @Test func thePlaceLineNamesWhereNotWhat() {
        let s = session(#","space_name":"weather","slot_name":"widget","agent":"claude""#)
        // Not "Claude": the row's mark says what runs there.
        #expect(SlotRow(session: s, space: "weather").placeString == "weather › widget")
        // The machine where the list names it (it mixes machines).
        #expect(SlotRow(session: s, space: "weather", machine: "MacBook").placeString == "weather › widget · MacBook")
        // The tab alone where the section is the space.
        #expect(SlotRow(session: s).placeString == "widget")
    }
}
