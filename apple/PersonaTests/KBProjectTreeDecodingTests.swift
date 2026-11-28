import Foundation
import Testing
@testable import Persona

/// The project tree's fields (#12927) decode when a hub sends them and stay nil when it does not; a flag the app does
/// not understand never fails the list it is in.
struct KBProjectTreeDecodingTests {
    @Test func listRowsCarryParentAndMilestones() throws {
        let json = #"""
        {"items":[
          {"kind":"projects","id":"seeds","name":"Seeds","relevance":1,"parent":"garden-app",
           "milestones":[{"id":"m1","title":"Beta out","target":"2026-12-15","state":"open"},{"later":"field"}]},
          {"kind":"projects","id":"docs-site","name":"Docs site","relevance":0}
        ]}
        """#
        let list = try JSONDecoder().decode(KBList.self, from: Data(json.utf8))
        let items = list.items
        #expect(items[0].parent == "garden-app")
        #expect(items[0].milestones?.first == KBMilestone(id: "m1", title: "Beta out", target: "2026-12-15", state: "open"))
        #expect(items[0].milestones?.count == 2)
        #expect(items[1].parent == nil && items[1].milestones == nil)
    }

    @Test func detailCarriesTheTreeOrNothing() throws {
        let with = #"""
        {"detail":{"kind":"projects","id":"press-kit","path":""},
         "tree":{"parent":{"id":"store-listing","name":"Store listing"},"ancestors":[{"id":"q4-launch","name":"Q4 launch"},{"id":"store-listing","name":"Store listing"}],
                 "children":[],"milestones":[],"depth":3}}
        """#
        let tree = try #require(try JSONDecoder().decode(KBDetailResponse.self, from: Data(with.utf8)).tree)
        #expect(tree.parent?.id == "store-listing")
        #expect(tree.ancestors?.map(\.id) == ["q4-launch", "store-listing"])
        #expect(tree.depth == 3)
        let without = #"{"detail":{"kind":"projects","id":"docs-site","path":""}}"#
        #expect(try JSONDecoder().decode(KBDetailResponse.self, from: Data(without.utf8)).tree == nil)
    }
}
