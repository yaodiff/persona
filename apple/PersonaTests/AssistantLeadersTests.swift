import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The directions' names, counts and order from the hub's tree, and its `directions` push (#4331, #4352).
struct AssistantLeadersTests {
    /// Every direction of the tree carries its name, what waits on you and whether anything under it runs; the secretary
    /// group is not one the sidebar lists. The row shows `needs_you` in words and `running` as its spinner (#6626).
    @Test func theTreeCarriesTheNamesAndCounts() throws {
        let raw = #"""
        {"directions":[
            {"id":"lead-a","name":"Product","counts":{"working":0,"needs_you":1,"stuck":0,"review":2},"running":true,"tasks":[]},
            {"id":"lead-b","name":"Infra","counts":{"working":2,"needs_you":0,"stuck":0,"review":0},"running":false,"tasks":[]},
            {"id":"main","name":"","secretary":true,"counts":{"working":7,"needs_you":7},"tasks":[]}]}
        """#
        // The one model the app holds (`AppModel.directionTree`, #4352): the lead rows read their counts from it.
        let tree = try JSON.decoder.decode(DirectionTree.self, from: Data(raw.utf8))
        #expect(tree.leaders.map(\.id) == ["lead-a", "lead-b"])
        #expect(tree.leaderNames == ["lead-a": "Product", "lead-b": "Infra"])
        // what runs is the hub's `running`, not the working count: lead-b counts two working to-dos with nothing at work
        #expect(tree.leaders.map(\.running) == [true, false])
        #expect(tree.leaders.map(\.counts.needsYou) == [1, 0])
    }

    /// The row's words say what waits on you and nothing while nothing does; what runs is its spinner alone (#6626).
    @Test func countsLabelShowsOnlyWhatWaitsOnYou() {
        #expect(DirectionCountsText.label(needsYou: 0) == nil)
        let waiting = DirectionCountsText.label(needsYou: 4)
        #expect(waiting == "等你 4")
    }

    /// The hub's push names no directions: the clients read the tree again on it (and on every (re)connect).
    @Test func theDirectionsPushIsRead() throws {
        let event = try #require(ServerEvent.decode(Data(#"{"type":"directions"}"#.utf8)))
        guard case .directions = event else {
            Issue.record("not directions")
            return
        }
    }
}
