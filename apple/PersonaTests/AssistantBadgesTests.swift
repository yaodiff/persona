import Foundation
import Observation
import Testing
@testable import Persona
import PersonaWire

/// What waits at each assistant, for the switcher's counts and the mark on the sidebar's head
/// (docs/assistant/managers.md "Counts"): an agent with a pending card or in Needs You once, quiet and muted ones left
/// out, a card on no session by itself, plus the inbox count the roster tells (none told: none); the roster's own
/// equality without those counts, a count that moves leaving the roster as it was, and a `managers` push that arrives
/// while the roster is being read. Made-up values.
@MainActor
struct AssistantBadgesTests {
    private static let roster = ManagerRoster(managers: [
        HubManager(id: "assistant", name: "助手", online: true, isDefault: true, builtin: true),
        HubManager(id: "m-1", name: "工作", online: true),
        HubManager(id: "m-2", name: "家里", online: false),
        HubManager(id: "m-9", name: "测试", online: false, installing: Date(timeIntervalSince1970: 1_800_000_000)),
    ], defaultID: "assistant")

    private func session(_ id: String, on machine: String = "MacBook", manager: String?, state: String = "idle", assistant: Bool = false,
                         quiet: Bool = false, group: String = "") throws -> Session {
        let stamp = manager.map { #","manager":"\#($0)""# } ?? ""
        var s = try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"\#(machine)","session_id":"\#(id)","state":"\#(state)","assistant":\#(assistant),"quiet":\#(quiet)\#(stamp)}"#.utf8))
        s.group = group
        return s
    }

    private func card(_ id: String, on machine: String = "MacBook", session: String, manager: String?, group: String = "") throws -> Interaction {
        let stamp = manager.map { #","manager":"\#($0)""# } ?? ""
        var ix = try JSON.decoder.decode(Interaction.self, from: Data(
            #"{"id":"\#(id)","machine_id":"\#(machine)","session_id":"\#(session)","kind":"permission"\#(stamp)}"#.utf8))
        ix.group = group
        return ix
    }

    /// The agents waiting, with `muted` sessions (by id) and each row's own `quiet` left out.
    private func waiting(_ sessions: [Session], _ cards: [Interaction], muted: Set<String> = []) -> [String: Int] {
        AssistantBadges.waiting(roster: Self.roster, group: "", sessions: sessions, interactions: cards) { key, row in
            row?.quiet != true && !muted.contains(key.sessionID)
        }
    }

    // MARK: the agents waiting

    @Test func anAgentCountsOnceHoweverManyCardsItHas() throws {
        let agent = try session("a", manager: "m-1", state: "needs_you")
        #expect(waiting([agent], [try card("x1", session: "a", manager: "m-1"), try card("x2", session: "a", manager: "m-1")]) == ["m-1": 1],
                "two cards and Needs You: one agent")
        #expect(waiting([try session("a", manager: "m-1")], [try card("x1", session: "a", manager: "m-1")]) == ["m-1": 1], "a card alone")
        #expect(waiting([agent], []) == ["m-1": 1], "in Needs You with no card: stuck on its computer's own prompt")
        #expect(waiting([try session("a", manager: "m-1", state: "working")], []).isEmpty)
        let two = [try session("a", manager: "m-1", state: "needs_you"), try session("b", manager: "m-1", state: "needs_you"),
                   try session("c", on: "server", manager: "assistant", state: "needs_you")]
        #expect(waiting(two, [try card("x", session: "b", manager: "m-1")]) == ["m-1": 2, "assistant": 1])
    }

    @Test func quietAndMutedAgentsDontCount() throws {
        let quiet = try session("q", manager: "m-1", state: "needs_you", quiet: true)
        let muted = try session("m", manager: "m-1", state: "needs_you")
        let cards = [try card("x1", session: "q", manager: "m-1"), try card("x2", session: "m", manager: "m-1")]
        #expect(waiting([quiet, muted], cards, muted: ["m"]).isEmpty)
        #expect(waiting([quiet, muted], cards) == ["m-1": 1], "unmuted, it counts; the quiet task's stays the main conversation's to tell")
        #expect(waiting([], [try card("x", session: "unlisted", manager: "m-1")], muted: ["unlisted"]).isEmpty,
                "a card whose session isn't listed is still its session's: muted by its key")
    }

    @Test func aCardOnNoSessionCountsByItself() throws {
        let cards = [try card("x1", session: "", manager: "m-1"), try card("x2", session: "", manager: "m-1"), try card("x3", session: "", manager: nil)]
        #expect(waiting([], cards) == ["m-1": 2, "assistant": 1], "each a thing of its own; none named: the default's")
    }

    @Test func whoseAnAgentIs() throws {
        let rows = [try session("a", manager: nil, state: "needs_you"), try session("b", manager: "gone", state: "needs_you"),
                    try session("c", manager: "m-9", state: "needs_you"), try session("d", manager: "m-2", state: "needs_you")]
        #expect(waiting(rows, []) == ["assistant": 3, "m-2": 1], "no stamp, a deleted manager, one still installing: the default's")
        #expect(waiting([try session("a", manager: "m-1")], [try card("x", session: "a", manager: nil)]) == ["m-1": 1],
                "a card without a stamp goes by its session's manager")
        #expect(waiting([try session("a", manager: "assistant")], [try card("x", session: "a", manager: "m-1")]) == ["m-1": 1],
                "a card's own stamp first: the manager that reported it")
        let elsewhere = [try session("a", manager: "m-1", state: "needs_you", group: "other")]
        #expect(waiting(elsewhere, [try card("x", session: "a", manager: "m-1", group: "other")]).isEmpty, "another group's hub has its own managers")
    }

    @Test func personasOwnConversationsCountByTheirCardsOnly() throws {
        let main = try session("main", manager: "m-1", state: "needs_you", assistant: true)
        #expect(waiting([main], []).isEmpty, "the Agents list leaves them out")
        #expect(waiting([main], [try card("x", session: "main", manager: "m-1")]) == ["m-1": 1])
    }

    // MARK: the counts

    @Test func aCountIsTheAgentsWaitingPlusTheInboxAsTheRosterToldIt() {
        let counts = AssistantBadges.counts(roster: Self.roster, waiting: ["m-1": 2, "assistant": 1, "m-9": 4], inbox: ["m-1": 3, "m-2": 5, "m-9": 1])
        #expect(counts == ["assistant": 1, "m-1": 5, "m-2": 5], "none told for the default: its agents alone; one still installing has no row")
        #expect(AssistantBadges.counts(roster: Self.roster, waiting: [:], inbox: [:]).isEmpty, "nothing waiting: no count")
        #expect(AssistantBadges.counts(roster: Self.roster, waiting: ["m-1": 1], inbox: ["m-1": -2]) == ["m-1": 1])
        #expect(AssistantBadges.spoken(3) == "3 件等你处理")
    }

    @Test func theRostersInboxCountDecodesAndIsNoPartOfTheRoster() throws {
        let json = #"""
        {"default":"assistant","managers":[
          {"id":"assistant","name":"助手","online":true,"default":true,"inbox_needs_you":0},
          {"id":"m-1","name":"工作","online":true,"inbox_needs_you":7},
          {"id":"m-2","name":"家里","online":false},
          {"id":"m-3","name":"别处","online":true,"inbox_needs_you":null}]}
        """#
        let list = try JSON.decoder.decode(APIClient.ManagerList.self, from: Data(json.utf8))
        #expect(list.managers.map(\.inboxNeedsYou) == [0, 7, nil, nil], "absent or null: not known, which counts as none")
        let roster = ManagerRoster(list)
        #expect(roster.managers.allSatisfy { $0.inboxNeedsYou == nil }, "a roster carries none: the model keeps them beside it")
        var moved = list
        moved.managers[1].inboxNeedsYou = 8
        #expect(ManagerRoster(moved) == roster, "a count that moved is not another roster")

        // In a snapshot: one written with the field reads, and so does one from before it.
        let back = try JSON.decoder.decode(HubManager.self, from: JSON.encoder.encode(list.managers[1]))
        #expect(back == list.managers[1] && back.inboxNeedsYou == 7)
        #expect(try JSON.decoder.decode(HubManager.self, from: Data(#"{"id":"m-1","name":"工作","online":true}"#.utf8)).inboxNeedsYou == nil)
    }

    // MARK: the model

    /// A model on one made-up hub: no socket, its requests go to `hub`.
    private func model(_ hub: FakeHub = FakeHub(), roster: ManagerRoster? = roster) throws -> AppModel {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "assistant-badges-" + UUID().uuidString))
        model.linkHooksForTests = (transport: try #require(hub.client.transport), socket: { SyncSocket() })
        model.connectOnly(.demo)
        model.managerRosters[""] = roster
        return model
    }

    @Test func theSwitcherCountsEveryAssistantAndTheMarkIsForTheOthers() throws {
        let model = try model()
        #expect(model.assistantBadges.isEmpty && !model.othersWaiting)
        let sessions = [try session("a", on: "server", manager: "assistant", state: "needs_you"), try session("b", manager: "m-1")]
        model.applySessions(Dictionary(grouping: sessions, by: \.machineRef))
        #expect(model.assistantBadges == ["assistant": 1] && !model.othersWaiting, "only the current one has something: no mark")

        model.replaceInteractions("", [try card("x1", session: "b", manager: "m-1"), try card("x2", session: "b", manager: "m-1")])
        #expect(model.assistantBadges == ["assistant": 1, "m-1": 1] && model.othersWaiting)

        model.inboxNeedsYou[""] = ["m-1": 3, "m-2": 2]
        #expect(model.assistantBadges == ["assistant": 1, "m-1": 4, "m-2": 2])

        model.selectAssistantManager("m-1")
        #expect(model.othersWaiting, "the default and 家里 still wait")
        model.inboxNeedsYou[""] = ["m-1": 3]
        model.applySessions([MachineRef(group: "", id: "server"): []])
        #expect(model.assistantBadges == ["m-1": 4] && !model.othersWaiting, "what waits is the current one's own")

        model.managerRosters[""] = ManagerRoster(managers: [Self.roster.managers[0]])
        #expect(model.assistantBadges.isEmpty && !model.othersWaiting, "one assistant: nothing shows a count")
    }

    /// On a hub with bounded lists the app holds only the pages it showed, so the sessions in Needs You are read for
    /// the counts: an agent of another assistant that was never listed counts, and one answered since is let go.
    @Test func onAHubWithBoundedListsTheSessionsInNeedsYouAreReadForTheCounts() async throws {
        let hub = FakeHub()
        var rows = #"{"machine_id":"NAS","session_id":"far","state":"needs_you","manager":"m-2"}"#
        hub.on("GET", "/api/v1/sessions") { req in
            #expect(req.query["state"] == "needs_you" && req.query["summary"] == "1" && req.query["manager"] == nil)
            return Data(#"{"sessions":[\#(rows)],"remove":[],"has_more":false,"revision":"r","total":1}"#.utf8)
        }
        let model = try model(hub)
        // What the app holds of m-1 from when it was on screen: stale, as nothing tells it of that list any more.
        model.applySessions(Dictionary(grouping: [try session("stale", manager: "m-1", state: "needs_you")], by: \.machineRef))
        #expect(model.assistantBadges == ["m-1": 1])
        model.link("")?.received(HubHello(features: ["sessions_list", SessionListQuery.sessionsManagerFeature]))
        await model.readNeedsYou()
        #expect(model.assistantBadges == ["m-2": 1], "the hub's word: the one it never listed counts, the one answered since doesn't")
        rows = ""
        await model.readNeedsYou()
        #expect(model.assistantBadges.isEmpty)
    }

    // MARK: reading the roster

    private static func rosterJSON(work: Int?) -> String {
        let count = work.map { #","inbox_needs_you":\#($0)"# } ?? ""
        return #"""
        {"default":"assistant","managers":[{"id":"assistant","name":"助手","online":true,"default":true,"builtin":true},
          {"id":"m-1","name":"工作","online":true\#(count)},{"id":"m-2","name":"家里","online":false}]}
        """#
    }

    /// The counts move with every change of an inbox: the roster, which every page that names a manager reads, is not
    /// written again for them.
    @Test func aCountThatMovesLeavesTheRosterAlone() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/managers", json: Self.rosterJSON(work: 2))
        let model = try model(hub, roster: nil)
        await model.refreshManagerRoster("")
        #expect(model.managerRoster?.isMulti == true && model.inboxNeedsYou[""] == ["m-1": 2] && model.assistantBadges == ["m-1": 2])

        let roster = Written()
        withObservationTracking { _ = model.managerRosters } onChange: { roster.written = true }
        hub.on("GET", "/api/v1/managers", json: Self.rosterJSON(work: 5))
        await model.refreshManagerRoster("")
        #expect(model.inboxNeedsYou[""] == ["m-1": 5] && model.assistantBadges == ["m-1": 5])
        #expect(!roster.written, "nothing that reads the roster is drawn again for a count")

        hub.on("GET", "/api/v1/managers", json: Self.rosterJSON(work: nil))
        await model.refreshManagerRoster("")
        #expect(model.inboxNeedsYou[""] == [:] && model.assistantBadges.isEmpty, "no longer told: none")
        #expect(!roster.written)
    }

    /// Whether something watched was written (told from wherever the write happens).
    private final class Written: @unchecked Sendable {
        var written = false
    }

    /// A `managers` push that comes while the roster is being read starts a read of its own, and that one lands: the
    /// answer already on its way may be from before the change.
    @Test func aPushWhileTheRosterIsBeingReadIsNotSwallowed() async throws {
        let hub = FakeHub()
        var answers = 0
        hub.on("GET", "/api/v1/managers") { _ in
            answers += 1
            return Data(Self.rosterJSON(work: answers == 1 ? 1 : 9).utf8)
        }
        let gate = Gate()
        var client = hub.client
        let answer = try #require(client.transport)
        // The first read's answer, made before the change, is kept back until the second read has landed.
        client.transport = { req in
            let data = try await answer(req)
            if req.path == "/api/v1/managers", await gate.first() { await gate.wait() }
            return data
        }
        let model = try model(hub, roster: nil)
        model.apiForTests = client
        let first = Task { await model.refreshManagerRoster("") }
        await gate.held()
        await model.refreshManagerRoster("") // the push
        #expect(model.inboxNeedsYou[""] == ["m-1": 9])
        await gate.open()
        await first.value
        #expect(model.inboxNeedsYou[""] == ["m-1": 9] && model.managerRoster?.isMulti == true, "the older answer doesn't land over it")
    }

    /// Holds the first caller back until `open()`.
    private actor Gate {
        private var calls = 0
        private var waiting: CheckedContinuation<Void, Never>?
        private var watchers: [CheckedContinuation<Void, Never>] = []

        func first() -> Bool {
            calls += 1
            return calls == 1
        }

        func wait() async {
            await withCheckedContinuation { c in
                waiting = c
                watchers.forEach { $0.resume() }
                watchers = []
            }
        }

        /// Returns once the first caller is held.
        func held() async {
            if waiting != nil { return }
            await withCheckedContinuation { watchers.append($0) }
        }

        func open() {
            waiting?.resume()
            waiting = nil
        }
    }
}
