import Foundation
import Observation
import Testing
@testable import Persona
import PersonaWire

/// The pages list the current assistant's things alone (docs/assistant/managers.md "Pages"): whose a thing is by its
/// `manager` (none, or one the roster doesn't have installed: the default's), the kept pick counting before the roster
/// is read, another group's things left alone; the model's computers, cards, sessions, dock and tab badges, the Agents
/// list's query on a hub that filters and on one that doesn't, the inbox's snapshot, the to-dos and what a change to one
/// names. Made-up values.
@MainActor
struct AssistantScopeTests {
    /// The default (`assistant`), `m-1` online, `m-2` offline, and one still installing.
    private static let roster = ManagerRoster(managers: [
        HubManager(id: "assistant", name: "助手", online: true, isDefault: true, builtin: true),
        HubManager(id: "m-1", name: "工作", online: true),
        HubManager(id: "m-2", name: "家里", online: false),
        HubManager(id: "m-9", name: "测试", online: false, installing: Date(timeIntervalSince1970: 1_800_000_000)),
    ], defaultID: "assistant")
    private static let one = ManagerRoster(managers: [HubManager(id: "assistant", name: "助手", online: true, isDefault: true)])

    private func scope(_ pick: String?, roster: ManagerRoster? = roster, awaited: Bool = true) -> AssistantScope? {
        AssistantScope(roster: roster, pick: pick, group: "", awaited: awaited)
    }

    private func machine(_ id: String, manager: String?, group: String = "") throws -> Machine {
        let stamp = manager.map { #","manager":"\#($0)""# } ?? ""
        var m = try JSON.decoder.decode(Machine.self, from: Data(#"{"id":"\#(id)","name":"\#(id)","kind":"ssh","online":true\#(stamp)}"#.utf8))
        m.group = group
        return m
    }

    private func session(_ id: String, on machine: String, manager: String?, state: String = "idle", assistant: Bool = false,
                         quiet: Bool = false, group: String = "") throws -> Session {
        let stamp = manager.map { #","manager":"\#($0)""# } ?? ""
        var s = try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"\#(machine)","session_id":"\#(id)","state":"\#(state)","assistant":\#(assistant),"quiet":\#(quiet)\#(stamp)}"#.utf8))
        s.group = group
        return s
    }

    private func card(_ id: String, on machine: String, session: String, manager: String?, group: String = "") throws -> Interaction {
        let stamp = manager.map { #","manager":"\#($0)""# } ?? ""
        var ix = try JSON.decoder.decode(Interaction.self, from: Data(
            #"{"id":"\#(id)","machine_id":"\#(machine)","session_id":"\#(session)","kind":"permission"\#(stamp)}"#.utf8))
        ix.group = group
        return ix
    }

    /// A model on one made-up hub: no socket, its requests go to `hub`.
    private func model(_ hub: FakeHub = FakeHub(), roster: ManagerRoster? = roster, folder: URL? = nil) throws -> AppModel {
        let model = AppModel(stateFolder: folder ?? FileManager.default.temporaryDirectory.appending(path: "assistant-scope-" + UUID().uuidString))
        model.linkHooksForTests = (transport: try #require(hub.client.transport), socket: { SyncSocket() })
        model.connectOnly(.demo)
        model.managerRosters[""] = roster
        return model
    }

    // MARK: whose a thing is

    @Test func aThingIsItsManagersAndTheDefaultsWithoutOne() throws {
        let home = try #require(scope(nil))
        #expect(home.current == "assistant")
        #expect(home.has(nil) && home.has("") && home.has("assistant"), "no stamp: the default's")
        #expect(home.has("gone") && home.has("m-9"), "a manager deleted, or still installing: the default's, so nothing is on no page")
        #expect(!home.has("m-1") && !home.has("m-2"))

        let work = try #require(scope("m-1"))
        #expect(work.has("m-1") && !work.has(nil) && !work.has("assistant") && !work.has("gone") && !work.has("m-2"))
        #expect(scope("m-2")?.current == "m-2", "an offline one can be the current one")
        #expect(scope("gone")?.current == "assistant" && scope("m-9")?.current == "assistant", "a pick the roster hasn't installed: the default")

        #expect(work.has(nil, group: "other") && work.has("assistant", group: "other"), "another group's things are no assistant's of this hub")
        #expect(!work.has(nil, group: ""))
    }

    @Test func oneAssistantOrNoRosterFiltersNothing() {
        #expect(scope(nil, roster: Self.one) == nil && scope("m-1", roster: Self.one) == nil)
        let installing = ManagerRoster(managers: [Self.roster.managers[0], Self.roster.managers[3]])
        #expect(scope(nil, roster: installing) == nil, "one still installing is not a second assistant yet")
        #expect(scope(nil, roster: nil) == nil, "no roster and no pick: a hub from before managers, or a first launch")
        let none: AssistantScope? = nil
        #expect(none.has("m-1") && none.has(nil, group: "") && none.has("anything", group: "g"))
    }

    /// A launch that kept a pick lists its things before the roster is read, not every assistant's for a moment; a hub
    /// that answered it has no roster filters nothing.
    @Test func theKeptPickCountsBeforeTheRosterIsRead() throws {
        let work = try #require(scope("m-1", roster: nil))
        #expect(work.current == "m-1" && work.has("m-1") && !work.has(nil) && !work.has("assistant") && !work.has("m-2"))
        let home = try #require(scope("assistant", roster: nil))
        #expect(home.has(nil) && home.has("assistant") && !home.has("m-1"), "no stamp is the hub's own assistant's until the roster says otherwise")
        #expect(scope("", roster: nil) == nil)
        #expect(scope("m-1", roster: nil, awaited: false) == nil, "the hub has none: the pick names nothing there")
    }

    @Test func aHubWithoutARosterStopsTheKeptPickFromFiltering() async throws {
        let folder = FileManager.default.temporaryDirectory.appending(path: "assistant-scope-" + UUID().uuidString)
        AssistantPickStore.save(["": "m-1"], folder: folder)
        let old = FakeHub() // no roster route: 404 with no code
        let model = try model(old, roster: nil, folder: folder)
        model.replaceMachines("", [try machine("server", manager: nil)])
        #expect(model.assistantScope?.current == "m-1" && model.scopedMachines().isEmpty, "until the hub answers, the pick's alone")
        await model.refreshManagerRoster("")
        #expect(model.assistantScope == nil && model.scopedMachines().map(\.id) == ["server"])
        #expect(model.assistantPicks == ["": "m-1"], "the pick itself is kept: a roster read later names it again")
    }

    // MARK: the model's lists

    @Test func thePagesListTheCurrentAssistantsComputersCardsAndSessions() throws {
        let model = try model()
        model.replaceMachines("", [try machine("server", manager: nil), try machine("MacBook", manager: "m-1"), try machine("NAS", manager: "m-2")])
        let sessions = [try session("a", on: "server", manager: "assistant"), try session("b", on: "MacBook", manager: "m-1"),
                        try session("c", on: "NAS", manager: "m-2"), try session("d", on: "server", manager: nil)]
        model.applySessions(Dictionary(grouping: sessions, by: \.machineRef))
        model.replaceInteractions("", [try card("x1", on: "server", session: "a", manager: "assistant"),
                                       try card("x2", on: "MacBook", session: "b", manager: "m-1"),
                                       try card("x3", on: "MacBook", session: "b", manager: nil)])

        #expect(model.scopedMachines().map(\.id) == ["server"])
        #expect(Set(model.scoped(model.allSessions).map(\.sessionID)) == ["a", "d"])
        #expect(model.scopedInteractions.map(\.id) == ["x1"])

        model.selectAssistantManager("m-1")
        #expect(model.scopedMachines().map(\.id) == ["MacBook"])
        #expect(model.scoped(model.allSessions).map(\.sessionID) == ["b"])
        #expect(Set(model.scopedInteractions.map(\.id)) == ["x2", "x3"], "a card without a stamp goes by its session's manager")
        #expect(model.machines.count == 3 && model.allSessions.count == 4 && model.pendingInteractions.count == 3,
                "the lists themselves stay whole: another assistant's thing still opens from a notification")
        let first = try #require(sessions.first)
        #expect(model.machine(MachineRef(group: "", id: "NAS")) != nil && model.latest(first.key) != nil)

        model.selectAssistantManager("m-2")
        #expect(model.scopedMachines().map(\.id) == ["NAS"] && model.scopedInteractions.isEmpty)

        model.managerRosters[""] = Self.one
        #expect(model.scopedMachines().count == 3 && model.scopedInteractions.count == 3, "one assistant: nothing is filtered")
    }

    @Test func anotherGroupsThingsAreOnEveryAssistantsPages() throws {
        let model = try model()
        model.replaceMachines("", [try machine("server", manager: nil), try machine("MacBook", manager: "m-1")])
        model.replaceMachines("other", [try machine("NAS", manager: "m-7", group: "other")])
        model.replaceInteractions("other", [try card("y", on: "NAS", session: "s", manager: "m-7", group: "other")])
        model.selectAssistantManager("m-1")
        #expect(Set(model.scopedMachines().map(\.id)) == ["MacBook", "NAS"])
        #expect(model.scopedInteractions.map(\.id) == ["y"])
    }

    /// The tabs count the current assistant's alone, and count again when another becomes current: the Persona tab its
    /// conversations' cards, the Agents tab its sessions that wait (one per session, however many cards).
    @Test func theTabBadgesCountTheCurrentAssistantsCards() throws {
        let model = try model()
        let sessions = [try session("a", on: "server", manager: "assistant"), try session("b", on: "MacBook", manager: "m-1"),
                        try session("c", on: "MacBook", manager: "m-1", state: "needs_you"),
                        try session("main", on: "MacBook", manager: "m-1", assistant: true)]
        model.applySessions(Dictionary(grouping: sessions, by: \.machineRef))
        model.replaceInteractions("", [try card("x1", on: "server", session: "a", manager: "assistant"),
                                       try card("x2", on: "MacBook", session: "b", manager: "m-1"),
                                       try card("x3", on: "MacBook", session: "b", manager: "m-1"),
                                       try card("x4", on: "MacBook", session: "main", manager: "m-1")])
        #expect(model.badges == TabBadges(assistant: 0, agents: 1))
        model.selectAssistantManager("m-1")
        #expect(model.badges == TabBadges(assistant: 1, agents: 2), "b with its two cards, and c waiting in its terminal")
        model.selectAssistantManager(nil)
        #expect(model.badges == TabBadges(assistant: 0, agents: 1))
        model.managerRosters[""] = Self.one
        model.relistAssistant()
        #expect(model.badges == TabBadges(assistant: 1, agents: 3), "one assistant: all of them, as before")
    }

    /// The main conversation's dock shows the current assistant's agents going on.
    @Test func theDockShowsTheCurrentAssistantsAgents() throws {
        let model = try model()
        let sessions = [try session("a", on: "server", manager: "assistant", state: "working"),
                        try session("b", on: "MacBook", manager: "m-1", state: "needs_you")]
        model.applySessions(Dictionary(grouping: sessions, by: \.machineRef))
        #expect(model.goingAgents.map(\.key.sessionID) == ["a"])
        model.selectAssistantManager("m-1")
        #expect(model.goingAgents.map(\.key.sessionID) == ["b"])
    }

    // MARK: the Agents list's query

    /// A hub that answers a paged list of whatever manager it is asked for (none: every manager's), and counts.
    private func pagedHub() -> FakeHub {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/sessions") { req in
            let manager = req.query["manager"]
            if req.query["counts"] == "1" {
                return Data(#"{"total":\#(manager == nil ? 3 : 1),"machine":{},"project":{},"group":{},"state":{},"revision":"r"}"#.utf8)
            }
            guard req.query["limit"] != nil else {
                // the whole list, asked without the bounded API's keys
                return Data(#"""
                {"sessions":[{"machine_id":"server","session_id":"a","state":"idle","manager":"assistant"},
                             {"machine_id":"MacBook","session_id":"b","state":"needs_you","manager":"m-1"},
                             {"machine_id":"NAS","session_id":"c","state":"unknown","manager":"m-2"}]}
                """#.utf8)
            }
            let rows = [("server", "a", "assistant"), ("MacBook", "b", "m-1"), ("NAS", "c", "m-2")].filter { manager == nil || $0.2 == manager }
                .map { #"{"machine_id":"\#($0.0)","session_id":"\#($0.1)","state":"idle","manager":"\#($0.2)"}"# }.joined(separator: ",")
            return Data(#"{"sessions":[\#(rows)],"remove":[],"has_more":false,"revision":"r-\#(manager ?? "all")","total":\#(manager == nil ? 3 : 1)}"#.utf8)
        }
        return hub
    }

    /// The list's own requests: not its counts, nor the read of every manager's sessions in Needs You for the switcher.
    private func pages(_ hub: FakeHub) -> [LocalRequest] {
        hub.sent("GET", "/api/v1/sessions").filter { $0.query["counts"] == nil && $0.query["state"] != "needs_you" }
    }

    @Test func aHubThatFiltersIsAskedForTheCurrentAssistantsSessions() async throws {
        let hub = pagedHub()
        let model = try model(hub)
        model.link("")?.received(HubHello(features: ["sessions_list", SessionListQuery.sessionsManagerFeature]))
        await model.sessionLists.select(SessionListQuery(), hub: nil, model: model)
        #expect(pages(hub).last?.query["manager"] == "assistant" && pages(hub).last?.query["limit"] == "50")
        #expect(hub.sent("GET", "/api/v1/sessions").last { $0.query["counts"] == "1" }?.query["manager"] == "assistant", "its counts too")
        #expect(model.sessionLists.sessions.map(\.sessionID) == ["a"] && model.sessionLists.total == 1)

        // Another assistant: another list, started over (a cursor is bound to its filters).
        model.selectAssistantManager("m-1")
        await model.sessionLists.rescope("", model: model)
        #expect(pages(hub).last?.query["manager"] == "m-1" && pages(hub).last?.query["cursor"] == nil)
        #expect(model.sessionLists.sessions.map(\.sessionID) == ["b"] && model.sessionLists.total == 1)
        #expect(model.sessionLists.isPaged(""))

        // A section's own page and counts name it too.
        let section = RuntimeSessionLists.Scope(hub: "", query: SessionListQuery(machine: "MacBook"))
        await model.sessionLists.expand(section)
        #expect(pages(hub).last?.query["machine_id"] == "MacBook" && pages(hub).last?.query["manager"] == "m-1")

        // One assistant again: no manager is named.
        model.managerRosters[""] = Self.one
        await model.sessionLists.rescope("", model: model)
        #expect(pages(hub).last?.query["manager"] == nil && model.sessionLists.sessions.count == 3)
    }

    /// A hub with bounded lists but without the filter would page, total and count every manager's sessions: the app
    /// keeps a complete copy of its list instead (as a grouping that needs every row does), filtered here by each row's
    /// `manager`, so the list and its counts are right.
    @Test func aHubThatCannotFilterIsAskedForTheWholeListWhichIsFilteredHere() async throws {
        let hub = pagedHub()
        let model = try model(hub)
        model.link("")?.received(HubHello(features: ["sessions_list"]))
        model.selectAssistantManager("m-1")
        await model.sessionLists.select(SessionListQuery(), hub: nil, model: model)
        let asked = try #require(pages(hub).last)
        #expect(asked.query["manager"] == nil && asked.query["summary"] == "0" && asked.query["state"] == nil,
                "every manager's rows, complete, with no key an older hub would ignore")
        #expect(!model.sessionLists.isPaged(""))
        #expect(model.sessionLists.sessions.map(\.sessionID) == ["b"])
        #expect(model.sessionLists.total == 1 && model.sessionLists.stateCounts == nil, "counted here, of this assistant's alone")
        #expect(model.allSessions.count == 3, "the others' rows are still there for what opens them")

        // The hub is updated: the next hello has the filter, and the list is asked the bounded way.
        model.link("")?.received(HubHello(features: ["sessions_list", SessionListQuery.sessionsManagerFeature]))
        await model.sessionLists.rescope("", model: model)
        #expect(pages(hub).last?.query["manager"] == "m-1" && model.sessionLists.isPaged(""))
    }

    @Test func theQueryNamesNoManagerWhereNothingIsFiltered() async throws {
        let hub = pagedHub()
        let model = try model(hub, roster: Self.one)
        model.sessionLists.bind(model)
        model.link("")?.received(HubHello(features: ["sessions_list", SessionListQuery.sessionsManagerFeature]))
        #expect(model.sessionLists.managed(SessionListQuery(state: "working"), group: "") == SessionListQuery(state: "working"))
        model.managerRosters[""] = Self.roster
        model.selectAssistantManager("m-1")
        #expect(model.sessionLists.managed(SessionListQuery(), group: "").manager == "m-1")
        #expect(model.sessionLists.managed(SessionListQuery(), group: "other") == SessionListQuery(), "another group's hub is not this one's managers'")
        let whole = model.sessionLists.managed(SessionListQuery(legacy: true), group: "")
        #expect(whole.manager.isEmpty && whole.legacy, "the whole list is never one manager's: it replaces the app's rows of the hub")
        #expect(SessionListQuery(manager: "m-1").parameters["manager"] == "m-1" && SessionListQuery().parameters["manager"] == nil)
    }

    /// Before a hub's hello says what it has, its list is neither asked with a key it may ignore nor swapped for the
    /// whole list: the page as it is, filtered here. The same before its roster is read, whatever pick was kept.
    @Test func beforeTheHelloOrTheRosterTheQueryIsLeftAsItIs() throws {
        let model = try model()
        model.sessionLists.bind(model)
        model.selectAssistantManager("m-1")
        #expect(model.sessionLists.managed(SessionListQuery(), group: "") == SessionListQuery())

        model.link("")?.received(HubHello(features: ["sessions_list", SessionListQuery.sessionsManagerFeature]))
        #expect(model.sessionLists.managed(SessionListQuery(), group: "").manager == "m-1")
        model.managerRosters[""] = nil // a launch that kept the pick, its roster not read yet
        #expect(model.assistantScope?.current == "m-1", "the rows are held to the pick meanwhile")
        #expect(model.sessionLists.managed(SessionListQuery(), group: "") == SessionListQuery(), "the hub is asked as before")
        model.managerRosters[""] = Self.roster // a launch that kept the roster too (the snapshot's), not read again yet
        model.rostersKept = [""]
        #expect(model.assistantScope?.current == "m-1" && model.assistantScope?.asked == false)
        #expect(model.sessionLists.managed(SessionListQuery(), group: "") == SessionListQuery(), "nor by a roster the last launch kept")
    }

    /// A launch with the roster the last one kept (several assistants) on a hub with bounded lists: the Agents list is
    /// asked as before until the hub answers its roster, then for the current assistant, and the sessions in Needs You
    /// are read only from then.
    @Test func aKeptRosterAsksTheHubNothingUntilItIsReadAgain() async throws {
        let hub = pagedHub()
        hub.on("GET", "/api/v1/managers", json: Self.rosterJSON(default: "assistant"))
        let folder = try Self.keptLaunch(roster: Self.roster, pick: "m-1")
        let model = try model(hub, roster: nil, folder: folder)
        model.restoreSaved() // as a paired app's launch does (`AppModel.init`)
        model.sessionLists.bind(model)
        #expect(model.currentAssistant?.id == "m-1" && model.assistantScope?.asked == false)
        model.link("")?.received(HubHello(features: ["sessions_list", SessionListQuery.sessionsManagerFeature]))
        await model.sessionLists.select(SessionListQuery(), hub: nil, model: model)
        model.followNeedsYou()
        try await Task.sleep(for: .milliseconds(100))
        #expect(!pages(hub).isEmpty && hub.requests.allSatisfy { $0.query["manager"] == nil }, "the page as before")
        #expect(!hub.requests.contains { $0.query["state"] == "needs_you" })

        await model.refreshManagerRoster("")
        for _ in 0..<40 where pages(hub).last?.query["manager"] == nil { try await Task.sleep(for: .milliseconds(50)) }
        #expect(model.assistantScope?.asked == true)
        #expect(pages(hub).last?.query["manager"] == "m-1", "the hub answered: the list again, for the current assistant")
        for _ in 0..<40 where !hub.requests.contains(where: { $0.query["state"] == "needs_you" }) { try await Task.sleep(for: .milliseconds(50)) }
        #expect(hub.requests.contains { $0.query["state"] == "needs_you" }, "and who waits at the others")
    }

    /// A state folder as a launch finds it after a run on a hub with `roster` and `pick` current: the snapshot's
    /// roster and the kept pick.
    private static func keptLaunch(roster: ManagerRoster, pick: String?) throws -> URL {
        let folder = FileManager.default.temporaryDirectory.appending(path: "assistant-scope-" + UUID().uuidString)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        try JSON.encoder.encode(AppModel.DevSnapshot(machines: [], sessions: [], interactions: [], assistant: nil,
                                                     managers: roster.managers, defaultManager: roster.defaultID,
                                                     picked: nil, pickedManager: nil))
            .write(to: folder.appending(path: AppModel.snapshotName("") + ".json"))
        if let pick { AssistantPickStore.save(["": pick], folder: folder) }
        return folder
    }

    /// The sessions in Needs You are read for the switcher's counts only while the app's list is a window of the hub's:
    /// a complete copy (here a hub without the manager filter) already has every assistant's, and nothing is read.
    @Test func noReadOfTheSessionsInNeedsYouWhileTheListIsComplete() async throws {
        func reads(_ hub: FakeHub) -> Int { hub.sent("GET", "/api/v1/sessions").count { $0.query["state"] == "needs_you" } }
        let whole = pagedHub()
        let model = try model(whole)
        model.link("")?.received(HubHello(features: ["sessions_list"]))
        await model.sessionLists.select(SessionListQuery(), hub: nil, model: model)
        #expect(!model.sessionLists.isPaged(""))
        model.replaceInteractions("", [try card("x", on: "MacBook", session: "b", manager: nil)])
        model.followNeedsYou()
        try await Task.sleep(for: .milliseconds(100))
        #expect(reads(whole) == 0)
        #expect(model.assistantBadges == ["m-1": 1], "counted from the copy: the card's session is m-1's")

        let windowed = pagedHub()
        let paged = try self.model(windowed)
        paged.link("")?.received(HubHello(features: ["sessions_list", SessionListQuery.sessionsManagerFeature]))
        await paged.sessionLists.select(SessionListQuery(), hub: nil, model: paged)
        #expect(paged.sessionLists.isPaged(""))
        for _ in 0..<40 where reads(windowed) == 0 { try await Task.sleep(for: .milliseconds(50)) }
        #expect(reads(windowed) >= 1, "a window: the hub is asked who waits at the other assistants")
    }

    /// Every request the pages make of what belongs to one assistant, through `model` as the app makes them: the
    /// assistant's page, its knowledge base and scheduled tasks, the inbox (list, refresh, Done's history and day
    /// counts, Muted, Rules), the to-dos (list, a new one, a change), a conversation's feed (every listed one's and one
    /// not listed yet), and the Agents list with its counts.
    private func askEverything(of model: AppModel) async {
        let api = model.api
        _ = try? await api?.assistant()
        _ = try? await api?.scheduledTasks()
        _ = try? await api?.kbList(.projects)
        _ = try? await api?.kbFind("x")
        _ = try? await api?.inboxListing(connectors: true)
        _ = try? await api?.refreshInbox(connectors: true) // the default's path: a named refresh needs `assistantManagerID`
        if let named = model.assistantManagerID { _ = try? await api?.manager(named).refreshInbox(connectors: true) }
        if let history = model.inboxHistoryOwner {
            _ = try? await history.api.inboxDone(after: nil, connectors: true)
            _ = try? await history.api.inboxDoneDays(zone: "UTC", connectors: true)
        }
        await InboxMutesModel(deps: .init(api: { model.api }, current: { model.namedAssistantManager }, show: { _ in })).load()
        await InboxRulesModel(deps: .init(api: { model.api }, toast: { _ in }, current: { model.namedAssistantManager })).load()
        let todos = TodoStore(cachedTodos: [TodoItem(id: "t1", title: "kept")], cacheWriter: nil, hubListChanged: {}, managers: .of(model))
        await todos.refresh(api: model.api)
        _ = try? await todos.create(title: "n", api: model.api)
        _ = try? await todos.update(id: "t1", title: "renamed", api: model.api)
        await readFeeds(of: model, todos: todos)
        model.followScope()
        model.followNeedsYou()
        await model.sessionLists.select(SessionListQuery(), hub: nil, model: model)
        await model.sessionLists.rescope("", model: model)
        try? await Task.sleep(for: .milliseconds(50)) // anything a follow-up task would send
    }

    /// The feed of every listed conversation, and of one not listed yet, read as its page reads it: through the hub's
    /// client for the manager `AppModel.feedManager` gives (`ChatView`'s `.task`).
    private func readFeeds(of model: AppModel, todos: TodoStore) async {
        let tasks = TasksStore(todoStore: todos, toast: { _ in })
        for chat in model.allSessions where chat.assistant {
            let manager = model.feedManager(listed: chat, opened: nil, group: chat.group)
            await tasks.load(chat.sessionID, refreshTodos: false, api: model.api(chat.group)?.manager(manager))?.value
        }
        let manager = model.feedManager(listed: nil, opened: nil, group: "")
        await tasks.load("unlisted", refreshTodos: false, api: model.api("")?.manager(manager))?.value
    }

    private static func feedPath(_ conversation: String) -> String { "/api/v1/assistant/conversations/\(conversation)/feed" }

    /// One installed assistant, or a hub from before the roster: nothing of this is there. No request names a manager
    /// (no header, no `?manager=`), the sessions in Needs You are not read for any count, and there is no count and no
    /// mark; a pick kept from another time changes none of it. Before the hub has answered what its roster is, too: a
    /// kept pick may hold rows back by then and is still named on the assistant's own routes (the cold start is on the
    /// assistant picked before), but the lists are asked exactly as before. `keptRoster`: the launch starts,
    /// as a paired app's does, from a snapshot whose roster has several assistants (the hub had them the last time, or
    /// the device was paired with another hub), the default current: the pages show by it until the hub answers, and
    /// the hub is still asked as before.
    @Test(arguments: [true, false], [true, false])
    func withOneAssistantOrNoRosterNothingIsNamedFilteredOrCounted(oneAssistant: Bool, keptRoster: Bool) async throws {
        // A hub with one manager stamps its things with it; a hub from before the roster stamps nothing.
        let own = oneAssistant ? "assistant" : nil
        let stamp = own.map { #","manager":"\#($0)""# } ?? ""
        let hub = FakeHub()
        hub.on("GET", "/api/v1/sessions") { req in
            if req.query["counts"] == "1" { return Data(#"{"total":2,"machine":{},"project":{},"group":{},"state":{},"revision":"r"}"#.utf8) }
            return Data(#"""
            {"sessions":[{"machine_id":"server","session_id":"a","state":"needs_you"\#(stamp)},
                         {"machine_id":"MacBook","session_id":"b","state":"needs_you"\#(stamp)}],
             "remove":[],"has_more":false,"revision":"r","total":2}
            """#.utf8)
        }
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t1","title":"kept"}]}"#)
        hub.on("POST", "/api/v1/todos", json: #"{"todo":{"id":"new","title":"n"}}"#)
        hub.on("PATCH", "/api/v1/todos/t1", json: #"{"todo":{"id":"t1","title":"renamed"}}"#)
        if oneAssistant { hub.on("GET", "/api/v1/managers", json: #"{"default":"assistant","managers":[{"id":"assistant","name":"助手","online":true,"default":true}]}"#) }
        // No roster route otherwise: 404 with no code, as a hub from before managers answers.
        let folder = keptRoster ? try Self.keptLaunch(roster: Self.roster, pick: nil)
            : FileManager.default.temporaryDirectory.appending(path: "assistant-scope-" + UUID().uuidString)
        if !keptRoster { AssistantPickStore.save(["": "m-1"], folder: folder) } // kept from when the hub had it
        let model = try model(hub, roster: nil, folder: folder)
        if keptRoster {
            model.restoreSaved() // as a paired app's launch does (`AppModel.init`)
            #expect(model.assistantScope?.asked == false && model.currentAssistant?.id == "assistant")
        }
        model.sessionLists.bind(model)
        model.link("")?.received(HubHello(features: ["sessions_list", SessionListQuery.sessionsManagerFeature]))
        model.replaceMachines("", [try machine("server", manager: own), try machine("MacBook", manager: own)])
        let sessions = [try session("a", on: "server", manager: own, state: "needs_you"),
                        try session("b", on: "MacBook", manager: own, state: "needs_you"),
                        try session("chat", on: "server", manager: own, assistant: true)]
        model.applySessions(Dictionary(grouping: sessions, by: \.machineRef))
        model.replaceInteractions("", [try card("x", on: "MacBook", session: "b", manager: own)])

        // `from`: the requests since then. `but`: a pick kept without its roster, which the assistant's own routes still
        // name until the hub answers (the cold start's rule, `assistantPick`); nothing else may be named even then.
        func nothingNamed(_ when: String, from: Int, but pick: String? = nil) {
            let asked = hub.requests.dropFirst(from)
            #expect(!asked.isEmpty)
            let named = asked.filter { $0.query["manager"] != nil || ($0.headers["X-Persona-Manager"] != nil && $0.headers["X-Persona-Manager"] != pick) }
            #expect(named.isEmpty, "\(when): \(named.map { "\($0.method) \($0.path)" })")
            #expect(!hub.requests.contains { $0.path == "/api/v1/sessions" && $0.query["state"] == "needs_you" }, "\(when): no read of the sessions in Needs You")
            #expect(pages(hub).allSatisfy { $0.query["limit"] == "50" }, "\(when): the Agents list's page as before, not a complete copy")
            #expect(!hub.sent("GET", Self.feedPath("chat")).isEmpty && !hub.sent("GET", Self.feedPath("unlisted")).isEmpty, "\(when): the feeds were read")
        }

        // Before the hub has said what its roster is.
        let keptPick: String? = keptRoster ? nil : "m-1"
        #expect(model.assistantManagerID == keptPick && model.api?.shownManager == keptPick && model.namedAssistant == nil)
        #expect(model.namedAssistantManager == nil && !model.rosterAsked)
        // What a kept roster shows meanwhile (its counts) is the app's own; what the hub is asked is checked below.
        if !keptRoster { #expect(model.assistantBadges.isEmpty && !model.othersWaiting && model.assistantManagerOffline == nil) }
        #expect(model.feedManager(of: "chat", group: "") == nil, "its stamp is the hub's own: not named")
        await askEverything(of: model)
        nothingNamed("before the roster is read", from: 0, but: keptPick)

        // Its answer: one assistant, or none to list.
        await model.refreshManagerRoster("")
        #expect(model.managerRosterUnsupported.contains("") == !oneAssistant)
        #expect(model.assistantScope == nil && model.assistantManagerID == nil && model.namedAssistant == nil)
        #expect(model.scopedMachines().count == 2 && model.scopedInteractions.count == 1 && model.scoped(model.allSessions).count == 3)
        #expect(model.badges.agents == 2, "every waiting agent, as before")
        #expect(model.assistantBadges.isEmpty && !model.othersWaiting && model.assistantManagerOffline == nil)
        #expect(model.assistantShownName == model.persona.name, "the persona's name beside its face, as before")
        #expect(model.assistantShownName(in: SessionKey(machineID: "server", sessionID: "chat")) == model.persona.name)
        #expect(model.feedManager(of: "chat", group: "") == nil && model.feedManager(of: "unlisted", group: "") == nil)
        #expect(model.itemManager(own) == nil)
        let answered = hub.requests.count
        await askEverything(of: model)
        nothingNamed("after it", from: answered)
        #expect(model.sessionLists.sessions.count == 2 && model.badges.agents == 2, "the list and its badge whole")
    }

    // MARK: the inbox

    private func item(_ id: String, _ manager: String?, tier: String = "action", done: Bool = false, connector: Bool = false) throws -> InboxItem {
        let stamp = manager.map { #","manager":"\#($0)""# } ?? ""
        let filed = done ? #","done_at":"2026-11-04T08:00:00Z","done_by":"read""# : ""
        let source = connector ? #","connector":{"thread":"t-\#(id)","open_url":"https://example.com/\#(id)"}"# : ""
        return try JSON.decoder.decode(InboxItem.self, from: Data(#"""
        {"id":"\#(id)","source":"mail","kind":"mail","account":"acct-\#(manager ?? "none")","account_name":"\#(manager ?? "none")","title":"\#(id)","preview":"",
         "count":1,"version":"v1","tier":"\#(tier)"\#(stamp)\#(filed)\#(source)}
        """#.utf8))
    }

    @Test func theInboxListsTheCurrentAssistantsItemsAndSources() throws {
        let all = [try item("a", "assistant"), try item("b", nil, tier: "fyi"), try item("c", "m-2", tier: "fyi"),
                   try item("w", "m-1", connector: true), try item("d", "m-1", done: true), try item("e", "gone", connector: true)]
        func listed(_ pick: String?, roster: ManagerRoster = Self.roster) -> InboxProjection {
            InboxProjection(items: all, filter: .init(scope: scope(pick, roster: roster)))
        }
        let home = listed(nil)
        #expect(home.openItems.map(\.id) == ["a", "b", "e"] && home.doneItems.isEmpty, "no stamp, or one the roster lacks: the default's")
        #expect(home.byTier[.action]?.map(\.id) == ["a", "e"] && home.byTier[.fyi]?.map(\.id) == ["b"])
        #expect(home.connectorSources.map(\.id) == ["acct-gone"], "only its own items' sources are offered as filters")

        let work = listed("m-1")
        #expect(work.openItems.map(\.id) == ["w"] && work.doneItems.map(\.id) == ["d"])
        #expect(work.connectorSources.map(\.id) == ["acct-m-1"])
        #expect(listed("m-2").openItems.map(\.id) == ["c"])

        let whole = listed(nil, roster: Self.one)
        #expect(whole.openItems.count == 5 && whole.doneItems.count == 1 && whole.connectorSources.count == 2, "one assistant: the page as before")

        // The page's snapshot is made again when another assistant becomes current.
        let memo = InboxProjectionMemo()
        let first = memo(all, filter: .init(scope: scope(nil)), revision: 1)
        #expect(memo(all, filter: .init(scope: scope(nil)), revision: 1) === first)
        #expect(memo(all, filter: .init(scope: scope("m-1")), revision: 1).openItems.map(\.id) == ["w"])
    }

    @Test func historyItemsAreStampedWithTheManagerAsked() throws {
        let plain = try item("h", nil, done: true), stamped = try item("k", "m-2", done: true)
        let out = InboxStore.stamped([plain, stamped], manager: "m-1"), listed = try item("h", "m-1")
        #expect(out.map(\.manager) == ["m-1", "m-2"] && out[0].key == listed.key, "one key with the merged list's copy")
        #expect(InboxStore.stamped([plain], manager: nil) == [plain], "one manager: no stamp")
    }

    @Test func theSectionsFoldWithoutAManager() {
        var folds = InboxFolds()
        #expect(folds.isOpen("action") && folds.isOpen("fyi") && !folds.isOpen("noise") && !folds.isOpen("done"))
        folds.toggle("fyi")
        folds.toggle("done")
        #expect(!folds.isOpen("fyi") && folds.isOpen("done"))
        folds.toggle("working")
        #expect(folds.isOpen("working"), "In Progress stays open")
    }

    // MARK: to-dos

    /// A store that goes by `scope` (a made-up current assistant), naming managers as the roster does.
    private func todos(_ list: [TodoItem], pick: String?) -> TodoStore {
        let current = scope(pick)
        return TodoStore(cachedTodos: list, cacheWriter: nil, hubListChanged: {},
                         managers: .init(scope: { current }, naming: { Self.roster.naming($0) }))
    }

    private static let list = [TodoItem(id: "t1", title: "home", manager: "assistant"), TodoItem(id: "t2", title: "bare"),
                               TodoItem(id: "t3", title: "work", manager: "m-1"), TodoItem(id: "t4", title: "quiet", quiet: true, manager: "m-1"),
                               TodoItem(id: "t5", title: "done", doneAt: Date(timeIntervalSince1970: 1_780_000_000), manager: "m-1")]

    @Test func theToDosListedAndCountedAreTheCurrentAssistants() throws {
        let decoded = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"t","title":"x","manager":"m-1"}"#.utf8))
        #expect(decoded.manager == "m-1")
        #expect(try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"t","title":"x"}"#.utf8)).manager == nil)

        let home = todos(Self.list, pick: nil)
        #expect(home.scopedTodos.map(\.id) == ["t1", "t2"] && home.badgeCount == 2 && home.openTodos.map(\.id) == ["t1", "t2"])
        let work = todos(Self.list, pick: "m-1")
        #expect(work.scopedTodos.map(\.id) == ["t3", "t4", "t5"] && work.openTodos.map(\.id) == ["t3", "t4"])
        #expect(work.badgeCount == 1, "its open ones, a quiet task left out")
        #expect(work.todos.count == 5 && work.todo(id: "t1") != nil && work.quiet == home.quiet, "the list itself stays whole")
        let whole = TodoStore(cachedTodos: Self.list, cacheWriter: nil, hubListChanged: {})
        #expect(whole.scopedTodos.count == 5 && whole.badgeCount == 3, "one assistant: all of them")
    }

    @Test func aNewToDoIsTheCurrentAssistantsAndAChangeGoesToTheToDosOwn() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/todos", json: #"{"todo":{"id":"new","title":"n"}}"#)
        hub.on("PATCH", "/api/v1/todos/t1", json: #"{"todo":{"id":"t1","title":"renamed"}}"#)
        hub.on("PATCH", "/api/v1/todos/t3", json: #"{"todo":{"id":"t3","title":"renamed"}}"#)
        hub.on("DELETE", "/api/v1/todos/t3", json: #"{"ok":true}"#)
        hub.on("PUT", "/api/v1/todos/t3/agents/MacBook/s1", json: #"{"ok":true}"#)
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"t3","title":"renamed","manager":"m-1"}]}"#)
        func named(_ method: String, _ path: String) -> [String?] { hub.sent(method, path).map { $0.headers["X-Persona-Manager"] } }

        // m-1 is current: a new one is made for it, and listed as its own at once.
        let work = todos(Self.list, pick: "m-1")
        let made = try await work.create(title: "n", api: hub.client)
        #expect(named("POST", "/api/v1/todos") == ["m-1"] && made.manager == "m-1")
        #expect(work.scopedTodos.first?.id == "new")

        // A change to another assistant's to-do (opened from a link) goes to that one; the default's names none.
        let home = try await work.update(id: "t1", title: "renamed", api: hub.client)
        let own = try await work.update(id: "t3", title: "renamed", api: hub.client)
        #expect(named("PATCH", "/api/v1/todos/t1") == [nil] && named("PATCH", "/api/v1/todos/t3") == ["m-1"])
        #expect(home.manager == "assistant" && own.manager == "m-1", "an answer has no stamp: the to-do keeps the list's")
        #expect(work.named("t3") == "m-1" && work.named("t1") == nil && work.named("unknown") == nil)

        // The list is read merged, with none named, also after a change that named one.
        try await work.linkAgent(todoID: "t3", machineID: "MacBook", sessionID: "s1", api: hub.client)
        #expect(named("PUT", "/api/v1/todos/t3/agents/MacBook/s1") == ["m-1"])
        #expect(named("GET", "/api/v1/todos") == [nil])
        try await work.delete(id: "t3", api: hub.client)
        #expect(named("DELETE", "/api/v1/todos/t3") == ["m-1"])

        // The default is current: a new one names none.
        let fresh = todos(Self.list, pick: nil)
        _ = try await fresh.create(title: "n", api: hub.client)
        #expect(named("POST", "/api/v1/todos") == ["m-1", nil])
        #expect(fresh.scopedTodos.first?.manager == "assistant")
    }

    /// Link to To-Do on another assistant's agent (opened from a notification): the to-dos offered are that
    /// assistant's, and a new one is made for it, not for the current assistant.
    @Test func anAgentIsLinkedToItsOwnAssistantsToDos() async throws {
        let work = todos(Self.list, pick: "m-1")
        #expect(work.openTodos(of: "assistant").map(\.id) == ["t1", "t2"] && work.openTodos(of: nil).map(\.id) == ["t1", "t2"],
                "the default's agent, or one without a stamp: the default's open to-dos")
        #expect(work.openTodos(of: "m-1").map(\.id) == ["t3", "t4"] && work.openTodos(of: "m-2").isEmpty)
        #expect(work.openTodos(of: "gone").map(\.id) == ["t1", "t2"], "a manager the roster lacks: the default's")
        let whole = TodoStore(cachedTodos: Self.list, cacheWriter: nil, hubListChanged: {})
        #expect(whole.openTodos(of: "m-1").count == 4, "one assistant: every open one")

        let hub = FakeHub()
        hub.on("POST", "/api/v1/todos", json: #"{"todo":{"id":"new","title":"n"}}"#)
        hub.on("PUT", "/api/v1/todos/new/agents/NAS/s9", json: #"{"ok":true}"#)
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[{"id":"new","title":"n","manager":"m-2"}]}"#)
        func named(_ method: String, _ path: String) -> [String?] { hub.sent(method, path).map { $0.headers["X-Persona-Manager"] } }
        let failure = try await work.createAndLink(title: "n", target: .agent(machineID: "NAS", sessionID: "s9", currentTodoID: nil),
                                                   owner: .of("m-2"), api: hub.client)
        #expect(failure == nil)
        #expect(named("POST", "/api/v1/todos") == ["m-2"] && named("PUT", "/api/v1/todos/new/agents/NAS/s9") == ["m-2"],
                "made for the agent's assistant and linked there")
        _ = try await work.create(title: "n", owner: .of(nil), api: hub.client)
        _ = try await work.create(title: "n", owner: .of("assistant"), api: hub.client)
        #expect(named("POST", "/api/v1/todos") == ["m-2", nil, nil], "the default's agent: none named")

        // Before the roster is read nothing is named, whatever was kept.
        let early = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {},
                              managers: .init(scope: { AssistantScope(roster: nil, pick: "m-1", group: "") }, naming: { $0 }, known: { false }))
        _ = try await early.create(title: "n", api: hub.client)
        #expect(named("POST", "/api/v1/todos").last == .some(nil))
    }

    // MARK: requests

    /// The knowledge page asks through a client for the current assistant (`currentAssistantSettings`), which names it
    /// on every route but the recordings' part: a recording's minutes are read where the recording was written, the
    /// default's knowledge base.
    @Test func theKnowledgeBaseIsTheCurrentAssistantsButForTheRecordings() async throws {
        #expect(APIClient.recordingsRoute("/api/v1/kb/recordings") && APIClient.recordingsRoute("/api/v1/kb/recordings/r1")
                && APIClient.recordingsRoute("/api/v1/kb/recordings?tag=x"))
        #expect(!APIClient.recordingsRoute("/api/v1/kb/recordingsx/r1") && !APIClient.recordingsRoute("/api/v1/kb/meetings/m1")
                && !APIClient.recordingsRoute("/api/v1/kb/projects") && !APIClient.recordingsRoute("/api/v1/recordings"), "only the recordings' own part")
        #expect(!APIClient.assistantRoute("/api/v1/todos", query: [:]) && !APIClient.assistantRoute("/api/v1/inbox", query: [:])
                && !APIClient.assistantRoute("/api/v1/sessions", query: [:]), "merged lists stay whole")
        let hub = FakeHub()
        let page = hub.client.showing("m-1").currentAssistantSettings()
        _ = try? await page.kbList(.projects)
        _ = try? await page.kbFind("x")
        _ = try? await page.kbList(.recordings)
        _ = try? await page.kbDetail("recordings", id: "r1")
        _ = try? await hub.client.currentAssistantSettings().kbList(.projects)
        #expect(hub.requests.map { $0.headers["X-Persona-Manager"] } == ["m-1", "m-1", nil, nil, nil])
    }

    /// A recording made while another assistant than the default is current: it is synced to the default, and its
    /// minutes (the recording's page, the watch's) and the hub's list of recordings are read from the default too.
    @Test func aRecordingsMinutesAreReadFromTheDefaultWhereItWasSynced() async throws {
        let hub = FakeHub()
        let model = try model(hub)
        model.selectAssistantManager("m-1")
        let api = try #require(model.api)
        #expect(api.shownManager == "m-1")
        _ = try? await api.request("PUT", "/api/v1/recordings/r1", body: ["title": "standup"])
        _ = try? await api.kbDetail("recordings", id: "r1")
        _ = try? await api.request("GET", "/api/v1/kb/recordings/r1")
        _ = try? await api.kbList(.recordings)
        _ = try? await api.request("POST", "/api/v1/recordings/r1/minutes")
        let knowledge = api.currentAssistantSettings() // the knowledge page's client
        _ = try? await knowledge.kbList(.projects)
        _ = try? await knowledge.kbDetail("recordings", id: "r1")
        let named = hub.requests.filter { $0.path.hasPrefix("/api/v1/recordings") || $0.path.hasPrefix("/api/v1/kb") }.map { $0.headers["X-Persona-Manager"] }
        #expect(named == [nil, nil, nil, nil, nil, "m-1", nil],
                "the recording and all that reads it name none, on the knowledge page too; the rest of the knowledge base names the current assistant")
    }

    // MARK: a roster push is not a switch

    private static func rosterJSON(default d: String) -> String {
        let home = d == "assistant", work = d == "m-1"
        return #"""
        {"default":"\#(d)","managers":[{"id":"assistant","name":"助手","online":true,"builtin":true,"default":\#(home)},
          {"id":"m-1","name":"工作","online":true,"default":\#(work)},{"id":"m-2","name":"家里","online":false}]}
        """#
    }

    /// The default moving onto, or off, the assistant that is current is no switch: the pages keep what is open and
    /// being written (they close and clear on `assistantSwitches`). What a request names changes; who is shown doesn't.
    @Test func theDefaultMovingUnderTheCurrentAssistantIsNoSwitch() async throws {
        let saved = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(saved, forKey: "assistant.current") }
        let hub = FakeHub()
        let model = try model(hub)
        model.selectAssistantManager("m-1")
        let switches = model.assistantSwitches

        // m-1, the current one, is made the default (here in Settings, or on another device).
        hub.on("GET", "/api/v1/managers", json: Self.rosterJSON(default: "m-1"))
        await model.refreshManagerRoster("")
        #expect(model.currentAssistant?.id == "m-1" && model.assistantManagerID == nil, "still m-1, now named by no header")
        #expect(model.assistantSwitches == switches, "no page closes, nothing being written is dropped")
        #expect(model.namedAssistant == "m-1", "what a page keys its content by stays")

        // And the default moves off it again.
        hub.on("GET", "/api/v1/managers", json: Self.rosterJSON(default: "assistant"))
        await model.refreshManagerRoster("")
        #expect(model.currentAssistant?.id == "m-1" && model.assistantManagerID == "m-1" && model.assistantSwitches == switches)

        // The default picked outright, then another becomes the default: the one picked stays current.
        model.selectAssistantManager("assistant")
        let after = model.assistantSwitches
        #expect(after == switches + 1, "a pick in the switcher is a switch")
        hub.on("GET", "/api/v1/managers", json: Self.rosterJSON(default: "m-1"))
        await model.refreshManagerRoster("")
        #expect(model.currentAssistant?.id == "assistant" && model.assistantManagerID == "assistant" && model.assistantSwitches == after)

        // The current one leaving the roster is a switch: its pages are another assistant's now.
        hub.on("GET", "/api/v1/managers", json: #"{"default":"m-1","managers":[{"id":"m-1","name":"工作","online":true,"default":true},{"id":"m-2","name":"家里","online":false}]}"#)
        await model.refreshManagerRoster("")
        #expect(model.currentAssistant?.id == "m-1" && model.assistantSwitches == after + 1)
    }

    /// Done's history is asked of the current assistant, named unless it is the default, and stamped with it.
    @Test func donesHistoryIsAskedOfTheCurrentAssistant() throws {
        let model = try model()
        let home = try #require(model.inboxHistoryOwner)
        #expect(home.manager == "assistant" && home.api.managerID == nil, "the default: stamped, not named")
        model.selectAssistantManager("m-1")
        let work = try #require(model.inboxHistoryOwner)
        #expect(work.manager == "m-1" && work.api.managerID == "m-1")
        model.managerRosters[""] = Self.one
        let one = try #require(model.inboxHistoryOwner)
        #expect(one.manager == nil && one.api.managerID == nil, "one assistant: neither")
    }

    /// A conversation's feed is read from its own assistant, whichever is current: the default's conversations name no
    /// manager (stamped with it, or with none), another's name it; one not listed yet is the current assistant's.
    @Test func aConversationsFeedNamesItsAssistantUnlessTheDefault() async throws {
        let hub = FakeHub()
        let model = try model(hub)
        let chats = [try session("c-home", on: "server", manager: "assistant", assistant: true),
                     try session("c-bare", on: "server", manager: nil, assistant: true),
                     try session("c-work", on: "MacBook", manager: "m-1", assistant: true)]
        model.applySessions(Dictionary(grouping: chats, by: \.machineRef))
        let todos = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, managers: .of(model))
        func named(_ conversation: String) -> [String?] { hub.sent("GET", Self.feedPath(conversation)).map { $0.headers[APIClient.managerHeader] } }

        await readFeeds(of: model, todos: todos)
        #expect(named("c-home") == [nil] && named("c-bare") == [nil], "the default's: no header")
        #expect(named("c-work") == ["m-1"], "another assistant's: named")
        #expect(named("unlisted") == [nil], "not listed, the default current")

        model.selectAssistantManager("m-1")
        await readFeeds(of: model, todos: todos)
        #expect(named("c-home") == [nil, nil] && named("c-bare") == [nil, nil], "the default's stay unnamed with another current")
        #expect(named("c-work") == ["m-1", "m-1"])
        #expect(named("unlisted") == [nil, "m-1"], "not listed yet: the page it shows on")
        #expect(hub.requests.allSatisfy { $0.query["manager"] == nil })
    }

    /// A voice call's captions name the assistant of the conversation they are said in: another assistant's
    /// conversation opened from a notification names its own, not the current one.
    @Test func captionsNameTheConversationsAssistant() throws {
        let model = try model()
        let chats = [try session("c-home", on: "server", manager: "assistant", assistant: true),
                     try session("c-away", on: "NAS", manager: "m-2", assistant: true)]
        model.applySessions(Dictionary(grouping: chats, by: \.machineRef))
        model.selectAssistantManager("m-1")
        #expect(model.assistantShownName == "工作")
        #expect(model.assistantShownName(in: SessionKey(machineID: "NAS", sessionID: "c-away")) == "家里")
        #expect(model.assistantShownName(in: SessionKey(machineID: "server", sessionID: "c-home")) == "助手")
        #expect(model.assistantShownName(in: SessionKey(machineID: "MacBook", sessionID: "unlisted")) == "工作", "not listed: the page it shows on")
    }

    /// What a conversation's feed offers (a plan's Start, a briefing question's Handled) is asked of the conversation's
    /// own manager: those routes name no conversation, so the hub would take them to the default.
    @Test func whatAConversationsFeedOffersGoesToItsOwnManager() async throws {
        let model = try model()
        let chats = [try session("c-home", on: "server", manager: "assistant", assistant: true),
                     try session("c-work", on: "MacBook", manager: "m-1", assistant: true),
                     try session("c-bare", on: "server", manager: nil, assistant: true),
                     try session("agent", on: "MacBook", manager: "m-1")]
        model.applySessions(Dictionary(grouping: chats, by: \.machineRef))
        #expect(model.feedManager(of: "c-work", group: "") == "m-1")
        #expect(model.feedManager(of: "c-home", group: "") == nil && model.feedManager(of: "c-bare", group: "") == nil, "the default's: none named")
        #expect(model.feedManager(of: "unlisted", group: "") == nil, "not listed, the default current: none")

        model.selectAssistantManager("m-2")
        #expect(model.feedManager(of: "c-work", group: "") == "m-1" && model.feedManager(of: "c-home", group: "") == nil,
                "whichever assistant is current")
        #expect(model.feedManager(of: "unlisted", group: "") == "m-2", "not listed yet: the page it shows on")
        #expect(model.feedManager(of: "agent", group: "") == "m-2", "an agent's session is no conversation")
        model.assistantItemManagers[SessionKey(group: "", machineID: "NAS", sessionID: "opened")] = "m-1"
        #expect(model.feedManager(of: "opened", group: "") == "m-1", "opened from a notification as another assistant's")

        let hub = FakeHub()
        hub.on("POST", "/api/v1/later/lt_1/handled", json: #"{"ok":true}"#)
        let actions = BriefActions(api: { _ in hub.client }, failed: { _ in }, taken: {}, named: { _ in nil },
                                   feedManager: { conversation, group in model.feedManager(of: conversation, group: group) })
        await actions.markHandled(BriefBody.Row(id: "lt_1", title: "?"), group: "", conversation: "c-work")
        #expect(hub.sent("POST", "/api/v1/later/lt_1/handled").map { $0.headers["X-Persona-Manager"] } == ["m-1"])
    }

    /// Scheduled Tasks left open while another assistant becomes current: what it writes still goes to the assistant
    /// whose tasks it shows, until it starts over on the new one.
    @Test func scheduledTasksWriteToTheAssistantTheyWereReadFrom() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/tasks") { req in
            let id = req.headers["X-Persona-Manager"] ?? "assistant"
            return Data(#"{"tasks":[{"id":"t-\#(id)","title":"\#(id)","prompt":"p","repeat":"daily","time":"09:00","weekdays":[1,2,3,4,5],"enabled":true}]}"#.utf8)
        }
        hub.on("GET", "/api/v1/assistant/runs", json: #"{"runs":[]}"#)
        hub.on("DELETE", "/api/v1/assistant/tasks/t-m-1", json: #"{"ok":true}"#)
        var client = hub.client.showing("m-1")
        let page = AssistantRoutinesModel(deps: .init(api: { client }, report: { _ in }, toast: { _ in }, open: { _ in }))
        await page.load()
        #expect(page.tasks.map(\.id) == ["t-m-1"])

        client = hub.client.showing(nil) // the default is current now
        await page.delete(try #require(page.tasks.first))
        #expect(hub.sent("DELETE", "/api/v1/assistant/tasks/t-m-1").map { $0.headers["X-Persona-Manager"] } == ["m-1"])

        await page.restart()
        #expect(page.loaded && page.tasks.map(\.id) == ["t-assistant"], "the page starts over on the one current now")
        #expect(hub.sent("GET", "/api/v1/assistant/tasks").map { $0.headers["X-Persona-Manager"] } == ["m-1", nil])
    }
}
