import Foundation
import Testing
@testable import Persona
import PersonaWire

/// 「全部子 Agent」 (#1559): every subagent under the one that launched it, the same order as the web's `subagentTree`.
struct SubagentTreeTests {
    private let now = Date(timeIntervalSince1970: 1_793_700_000)

    private func sub(_ id: String, _ minutesAgo: Double, parent: String? = nil, running: Bool = false, fork: Bool? = nil) -> Subagent {
        Subagent(id: id, sessionID: "s1@" + id, running: running, parentID: parent, fork: fork,
                 startedAt: now.addingTimeInterval(-minutesAgo * 60))
    }

    private func shape(_ all: [Subagent]) -> [String] {
        Subagent.tree(all).map { String(repeating: "  ", count: $0.depth) + $0.agent.id }
    }

    @Test func treeNestsUnderLaunchersRunningFirstThenNewestFirst() {
        let cases: [(label: String, input: [Subagent], expected: [String])] = [
            ("nests each under its launcher several levels deep", [
                sub("e5", 1, parent: "c3", fork: true), sub("b2", 9), sub("c3", 6, parent: "a1"),
                sub("a1", 10, running: true), sub("f6", 2, parent: "c3", fork: true), sub("d4", 5, parent: "a1"),
            ], ["a1", "  d4", "  c3", "    e5", "    f6", "b2"]),
            ("one whose launcher is not listed is at the top",
             [sub("a1", 10), sub("k9", 3, parent: "gone"), sub("c3", 6, parent: "a1")], ["k9", "a1", "  c3"]),
            ("each level running first, then newest first", [
                sub("old", 30), sub("new", 1), sub("live", 50, running: true),
                sub("kid-old", 20, parent: "old"), sub("kid-live", 25, parent: "old", running: true), sub("kid-new", 2, parent: "old"),
            ], ["live", "new", "old", "  kid-live", "  kid-new", "  kid-old"]),
            ("launchers naming each other are listed once from the top",
             [sub("x", 2, parent: "y"), sub("y", 1, parent: "x"), sub("z", 3, parent: "z")], ["z", "y", "  x"]),
        ]
        for c in cases {
            #expect(shape(c.input) == c.expected, "\(c.label)")
        }
        #expect(Subagent.tree([]).isEmpty)
    }

    @Test func parentAndForkDecode() throws {
        let a = try JSON.decoder.decode(Subagent.self, from: Data(#"""
        {"id":"sa4","session_id":"s1@sa4","type":"fork","parent_id":"sa3","fork":true,"running":false}
        """#.utf8))
        #expect(a.parentID == "sa3" && a.fork == true)
        let old = try JSON.decoder.decode(Subagent.self, from: Data(#"{"id":"a1","session_id":"s1@a1","running":true}"#.utf8))
        #expect(old.parentID == nil && old.fork == nil)
    }
}
