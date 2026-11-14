import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The agents of one herdr tab as one group (#2320, `TabPanes`): their order, the Started It, which ones share a tab, and
/// how the Agents list and the chat's tabs show them. The same rules as the web's `herdr-tree.ts`.
@MainActor
struct TabPanesTests {
    private func session(_ id: String, slot: String? = "s1", slotIndex: Int? = 1, space: String? = "w1", pane: String? = nil,
                         place: Int? = nil, agent: String? = nil, state: String = "idle", machine: String = "box",
                         spaceName: String? = "app", updated: String? = nil) throws -> Session {
        var o: [String: Any] = ["machine_id": machine, "session_id": id, "title": "agent \(id)", "cwd": "/code/app",
                                "status": "idle", "state": state, "started_by_persona": false, "assistant": false,
                                "pending_interactions": 0]
        if let slot { o["slot_id"] = slot }
        if let slotIndex { o["slot_index"] = slotIndex }
        if let space { o["space_id"] = space }
        if let spaceName { o["space_name"] = spaceName }
        if let pane { o["pane_id"] = pane }
        if let place { o["pane_index"] = place }
        if let agent { o["agent"] = agent }
        if let updated { o["updated_at"] = updated }
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: o))
    }

    @Test func paneNumbersFollowHerdrsCounting() throws {
        #expect(TabPanes.paneNumber(try session("a", pane: "w4R:pW")) == 32)
        #expect(TabPanes.paneNumber(try session("a", pane: "w4R:p2E")) == 86)
        #expect(TabPanes.paneNumber(try session("a", pane: "herdr-core:t12")) == 12)
        #expect(TabPanes.paneNumber(try session("a", pane: "botmux:abc")) == Int.max)
        #expect(TabPanes.paneNumber(try session("a")) == Int.max)
    }

    @Test func ordersByPlaceThenPaneThenID() throws {
        let panes = [
            try session("z", pane: "w1:p3A"),
            try session("c", pane: "w1:p2E", place: 1),
            try session("b", pane: "w1:p31", place: 0),
            try session("y", pane: "w1:p2E"),
            try session("x"),
        ].sorted(by: TabPanes.inPaneOrder)
        #expect(panes.map(\.sessionID) == ["b", "c", "y", "z", "x"])
    }

    @Test func theLauncherIsTheTopLeftPaneOfATabOfSeveral() throws {
        let lead = try session("lead", place: 0), branch = try session("branch", place: 1)
        #expect(TabPanes.launcher(of: [branch, lead])?.sessionID == "lead")
        #expect(TabPanes.launcher(of: [lead]) == nil, "alone in its tab")
        #expect(TabPanes.launcher(of: [try session("a", place: 1), try session("b", place: 2)]) == nil,
                "a shell in the top left pane: peers")
        #expect(TabPanes.launcher(of: [try session("a"), try session("b")]) == nil, "the host does not say")
    }

    @Test func sameSlotNeedsTheSameMachineSpaceAndTab() throws {
        let a = try session("a")
        #expect(TabPanes.sameSlot(a, try session("b")))
        #expect(!TabPanes.sameSlot(a, try session("b", slot: "s2")))
        #expect(!TabPanes.sameSlot(a, try session("b", space: "w2")))
        #expect(!TabPanes.sameSlot(a, try session("b", machine: "other")))
        #expect(!TabPanes.sameSlot(try session("a", slot: nil), try session("b", slot: nil)), "no tab, no group")
    }

    @Test func aTabsAgentsAreOneEntryWhereTheFirstOfThemIs() throws {
        let list = [
            try session("review", pane: "w1:p2E", place: 1, agent: "codex", state: "needs_you"),
            try session("other", slot: "s2", slotIndex: 2, pane: "w1:p3A", place: 0),
            try session("lead", pane: "w1:pW", place: 0, state: "working"),
            try session("tests", pane: "w1:p31", place: 2, state: "unread"),
            try session("loose", slot: nil, slotIndex: nil),
        ]
        let entries = TabPanes.entries(list)
        #expect(entries.count == 3)
        guard case .tab(let g) = entries[0] else { Issue.record("the tab first"); return }
        #expect(g.sessions.map(\.sessionID) == ["lead", "review", "tests"])
        #expect(g.launcher?.sessionID == "lead")
        #expect(g.lead.sessionID == "review", "the most urgent speaks for the tab")
        #expect(g.label == "#1")
        #expect(g.foldID == "tab/box/app/1", "by machine, space name and the tab's number")
        #expect(!g.isBranch(g.sessions[0]) && g.isBranch(g.sessions[1]) && g.isBranch(g.sessions[2]))
        #expect(entries[1].sessions.map(\.sessionID) == ["other"], "a tab of one is a plain row")
        #expect(entries[2].sessions.map(\.sessionID) == ["loose"])
        #expect(TabPanes.grouped(list).map(\.sessionID) == ["lead", "review", "tests", "other", "loose"])
    }

    @Test func theFoldIsKeptByTheTabsNumberNotItsName() throws {
        var renamed = try session("a")
        renamed.slotName = "重构登录"
        #expect(TabPanes.foldID(renamed) == TabPanes.foldID(try session("a")))
        #expect(TabPanes.foldID(try session("a", slotIndex: nil)) == "tab/box/app/s1", "no number: the tab's id")
    }

    @Test func agentsAreNamedBesideTheirTabMates() throws {
        let panes = [try session("a", place: 0), try session("b", place: 1, agent: "codex"), try session("c", place: 2)]
        let roles = TabPanes.roles(panes)
        #expect(roles[panes[0].key] == TabPanes.Role(name: "Claude", launcher: true, branch: false))
        #expect(roles[panes[1].key] == TabPanes.Role(name: "Codex", launcher: false, branch: true))
        #expect(roles[panes[2].key]?.name == "Claude", "the 发起者's mark tells it from the other Claude")
        let peers = TabPanes.roles([try session("p", place: 1), try session("q", place: 2)])
        #expect(Set(peers.values.map(\.name)) == ["Claude 1", "Claude 2"], "peers running the same agent are numbered")
        #expect(TabPanes.tabLabel(panes[1], role: roles[panes[1].key]) == "#1 · Codex")
        #expect(TabPanes.tabLabel(panes[1], role: nil) == "#1")
        #expect(TabPanes.roles([panes[0]]).isEmpty, "alone in its tab: no role")
    }

    @Test func peersHaveNoLauncherAndNoBranches() throws {
        let roles = TabPanes.roles([try session("a", place: 1), try session("b", place: 2)])
        #expect(roles.values.allSatisfy { !$0.launcher && !$0.branch })
    }

    @Test func aTabStaysOutOfTheIdleFoldWhileOneOfItsAgentsIsBusy() throws {
        let order = AgentListOrder(isPinned: { _ in false }, isMuted: { _ in false })
        let list = [
            try session("lead", place: 0, state: "working"),
            try session("seen", place: 1),
            try session("quiet", slot: "s2", slotIndex: 2),
        ]
        let rows = order.tabEntries(list)
        #expect(rows.busy.map { $0.sessions.map(\.sessionID) } == [["lead", "seen"]])
        #expect(rows.idle.map { $0.sessions.map(\.sessionID) } == [["quiet"]])
        #expect(order.tabEntries(list, all: false).idle.isEmpty, "under a quick filter every row stays")
    }

    @Test func aFoldedTabsAgentsAreNotStepsOfTheMacsArrows() throws {
        let order = AgentListOrder(isPinned: { _ in false }, isMuted: { _ in false })
        let list = [try session("lead", place: 0, state: "working"), try session("b", place: 1, state: "working"),
                    try session("x", slot: "s2", slotIndex: 2, state: "working")]
        let open = AgentListRows(order: order, grouping: .space, isFolded: { _ in false })
        #expect(open.agentRows(list, key: "k").map(\.sessionID) == ["lead", "b", "x"])
        let shut = AgentListRows(order: order, grouping: .space, isFolded: { $0 == "tab/box/app/1" })
        #expect(shut.agentRows(list, key: "k").map(\.sessionID) == ["x"])
    }

    @Test func aSpacesTabsKeepTheirAgentsTogetherInPaneOrder() throws {
        let groups = SpaceGroup.build([
            try session("t2", slot: "s2", slotIndex: 2, state: "working"),
            try session("branch", pane: "w1:p2E", place: 1, state: "working"),
            try session("lead", pane: "w1:pW", place: 0, state: "working"),
        ])
        #expect(groups.first?.sessions.map(\.sessionID) == ["lead", "branch", "t2"])
    }

    @Test func theChatsTabsPutATabsAgentsTogetherInPaneOrder() throws {
        let spaces = AppModel.liveBySpace([
            try session("t2", slot: "s2", slotIndex: 2, pane: "w1:p40"),
            try session("tests", pane: "w1:p31", place: 2),
            try session("lead", pane: "w1:pW", place: 0),
            try session("review", pane: "w1:p2E", place: 1),
        ])
        #expect(spaces["box/w1"]?.map(\.sessionID) == ["lead", "review", "tests", "t2"])
    }
}
