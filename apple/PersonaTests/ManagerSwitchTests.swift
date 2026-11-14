import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The current assistant (docs/assistant/managers.md "Clients"): the assistant's routes name the manager picked in the
/// sidebar's switcher, except one conversation's and the health space's, and an item's client names only its own manager,
/// which stamps the conversations relayed from it; the page starts on the default, keeps the pick on this device across
/// launches, goes back to the default when its manager leaves the roster, reads itself again when the default moves or
/// its manager is back, lists that manager's conversations, holds what it read of each manager in this run, opens another
/// assistant's conversation from elsewhere without switching, and sends nothing to an offline one. Made-up values.
@MainActor
struct ManagerSwitchTests {
    private static let header = "X-Persona-Manager"

    /// The default (`assistant`), `m-1` online and `m-2` offline.
    private static let roster = #"""
    {"default":"assistant","managers":[
      {"id":"assistant","name":"助手","online":true,"default":true,"builtin":true},
      {"id":"m-1","name":"工作","online":true},
      {"id":"m-2","name":"家里","online":false,"last_seen":"2026-11-08T05:00:00Z"}]}
    """#
    private static let withoutWork = #"""
    {"default":"assistant","managers":[
      {"id":"assistant","name":"助手","online":true,"default":true,"builtin":true},
      {"id":"m-2","name":"家里","online":false}]}
    """#
    private static let oneManager = #"{"default":"assistant","managers":[{"id":"assistant","name":"助手","online":true,"default":true}]}"#

    private static func roster(_ json: String = roster) throws -> ManagerRoster {
        ManagerRoster(try JSON.decoder.decode(APIClient.ManagerList.self, from: Data(json.utf8)))
    }

    private func session(_ id: String, manager: String?, archived: Bool = false, space: String? = nil) throws -> Session {
        let stamp = manager.map { #","manager":"\#($0)""# } ?? ""
        let where_ = space.map { #","space":"\#($0)""# } ?? ""
        return try JSON.decoder.decode(Session.self, from: Data(
            #"{"machine_id":"box","session_id":"\#(id)","status":"idle","assistant":true,"archived":\#(archived)\#(stamp)\#(where_)}"#.utf8))
    }

    /// A model on one made-up hub: no socket, its requests go to `hub`. `folder`: where it keeps its state (a second
    /// model on the same one is the next launch).
    private func model(_ hub: FakeHub, roster: ManagerRoster?, folder: URL? = nil) throws -> AppModel {
        let transport = try #require(hub.client.transport)
        let model = AppModel(stateFolder: folder ?? FileManager.default.temporaryDirectory.appending(path: "manager-switch-" + UUID().uuidString))
        model.linkHooksForTests = (transport: transport, socket: { SyncSocket() })
        model.connectOnly(.demo)
        model.managerRosters[""] = roster
        return model
    }

    // MARK: requests

    @Test func onlyTheAssistantsOwnRoutesGoByThePage() {
        #expect(APIClient.assistantRoute("/api/v1/assistant", query: [:]))
        #expect(APIClient.assistantRoute("/api/v1/assistant/main/new", query: [:]))
        #expect(APIClient.assistantRoute("/api/v1/assistant/conversations", query: [:]), "starting one is the page's")
        #expect(!APIClient.assistantRoute("/api/v1/assistant/conversations/c1/feed", query: [:]), "one conversation's: its own manager's")
        #expect(!APIClient.assistantRoute("/api/v1/assistant/memory", query: ["space": "health"]), "the health space: the default's")
        #expect(!APIClient.assistantRoute("/api/v1/assistant?space=health", query: [:]))
        #expect(APIClient.assistantRoute("/api/v1/assistant/memory", query: ["space": ""]), "an empty space is none")
        #expect(!APIClient.assistantRoute("/api/v1/assistants", query: [:]) && !APIClient.assistantRoute("/api/v1/inbox", query: [:]))
    }

    @Test func theRequestsNameThePagesManagerOrTheItemsOwn() async throws {
        let hub = FakeHub()
        let page = hub.client.showing("m-1")
        _ = try? await page.assistant()
        _ = try? await page.scheduledTasks()
        _ = try? await page.startConversation(prompt: "hi")
        _ = try? await page.startConversation(prompt: "hi", space: Session.healthSpace)
        _ = try? await page.healthConversations()
        _ = try? await page.archiveConversation("c1", archived: true)
        _ = try? await page.managers()
        _ = try? await page.manager(nil).assistant()
        _ = try? await page.manager("m-2").archiveConversation("c2", archived: true)
        _ = try? await hub.client.assistant()
        let named = hub.requests.map { $0.headers[Self.header] }
        #expect(named == ["m-1", "m-1", "m-1", nil, nil, nil, nil, nil, "m-2", nil],
                "the page's routes name it; a health start, the health list, a conversation's route, another API, the default item's client and no pick name none; an item's client names its own")
        #expect(hub.requests[3].path == "/api/v1/assistant/conversations")
    }

    @Test func anotherManagersRelayedConversationsAreStampedWithIt() async throws {
        let saved = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(saved, forKey: "assistant.current") }
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: #"{"machine_id":"w","launcher":"codex","cwd":"/w","conversations":[{"machine_id":"w","session_id":"w1","status":"idle","assistant":true},{"machine_id":"w","session_id":"w2","status":"idle","assistant":true,"manager":"m-2"}]}"#)
        hub.on("POST", "/api/v1/assistant/conversations", json: #"{"session":{"machine_id":"w","session_id":"w3","status":"idle","assistant":true}}"#)
        let page = hub.client.showing("m-1")
        #expect(try await page.assistant().conversations.map(\.manager) == ["m-1", "m-2"], "a stamp it came with stays")
        #expect(try await page.startConversation(prompt: "hi").manager == "m-1")
        #expect(try await page.startConversation(prompt: "hi", space: Session.healthSpace).manager == nil, "the health space: the default's")
        #expect(try await page.manager("m-2").startConversation(prompt: "hi").manager == "m-2")
        #expect(try await hub.client.assistant().conversations.map(\.manager) == [nil, "m-2"], "the default's page names none and stamps none")

        let model = try model(hub, roster: try Self.roster())
        model.selectAssistantManager("m-1")
        let api = try #require(model.api)
        let s = try await api.startConversation(prompt: "hi")
        model.began(s, text: "hi")
        #expect(model.assistantConversations.map(\.sessionID) == ["w3"], "listed on its manager's page")
        model.openAssistantConversation(s.key)
        #expect(model.assistantManagerID == "m-1", "and opened there")
    }

    @Test func aTimeZoneSavedForTheManagerShownBeforeDoesntLand() async throws {
        let saved = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(saved, forKey: "assistant.current") }
        let hub = FakeHub()
        let tz = TimeZone.current.identifier
        hub.on("GET", "/api/v1/assistant") { req in
            Data((req.headers[Self.header] == "m-1" ? #"{"machine_id":"w","launcher":"codex","cwd":"/w","conversations":[],"timezone":"\#(tz)"}"#
                                                     : #"{"machine_id":"a","launcher":"claude","cwd":"/a","conversations":[],"timezone":"Nowhere/Else"}"#).utf8)
        }
        let model = try model(hub, roster: try Self.roster())
        hub.on("PUT", "/api/v1/assistant") { _ in
            model.selectAssistantManager("m-1") // the page moves on while the default's zone is saved
            return Data(#"{"machine_id":"a","launcher":"late","cwd":"/a","conversations":[],"timezone":"\#(tz)"}"#.utf8)
        }
        await model.refreshAssistant()
        #expect(model.assistantManagerID == "m-1" && model.assistantConfig?.launcher != "late")
        #expect(model.assistantByGroup[model.hub?.group ?? ""]?.launcher == "claude", "the default's held settings are what its page read")
    }

    @Test func anotherDefaultOrTheShownManagerBackOnlineReadsThePageAgain() async throws {
        let saved = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(saved, forKey: "assistant.current") }
        let hub = FakeHub()
        let tz = TimeZone.current.identifier
        hub.on("GET", "/api/v1/assistant", json: #"{"machine_id":"a","launcher":"claude","cwd":"/a","conversations":[],"timezone":"\#(tz)"}"#)
        let model = try model(hub, roster: try Self.roster())
        func reads(_ m: String?) -> Int { hub.sent("GET", "/api/v1/assistant").filter { $0.headers[Self.header] == m }.count }
        model.selectAssistantManager("m-2")
        await waitUntil { reads("m-2") == 1 }
        hub.on("GET", "/api/v1/managers", json: #"""
        {"default":"assistant","managers":[{"id":"assistant","name":"助手","online":true,"default":true,"builtin":true},
          {"id":"m-1","name":"工作","online":true},{"id":"m-2","name":"家里","online":true}]}
        """#)
        await model.refreshManagerRoster("")
        #expect(model.assistantManagerOffline == nil)
        await waitUntil { reads("m-2") == 2 }
        #expect(reads("m-2") == 2, "back online: its page is read again")

        // The default's settings read while m-2 was current show at once: wait for its page's own read too.
        await waitUntil { model.assistantByGroup[""] != nil }
        let settled = reads(nil)
        model.selectAssistantManager(nil)
        await waitUntil { model.assistantConfig?.launcher == "claude" && reads(nil) == settled + 1 }
        hub.on("GET", "/api/v1/managers", json: #"""
        {"default":"m-1","managers":[{"id":"assistant","name":"助手","online":true,"builtin":true},
          {"id":"m-1","name":"工作","online":true,"default":true},{"id":"m-2","name":"家里","online":true}]}
        """#)
        let before = reads(nil)
        await model.refreshManagerRoster("")
        #expect(model.assistantConfig == nil, "what was held as the default's is the one before's")
        await waitUntil { reads(nil) == before + 1 }
        #expect(reads(nil) == before + 1, "the new default's page is read")
    }

    @Test func theDefaultMovingUnderAnotherManagersPageReadsTheNewDefaultsSettings() async throws {
        let saved = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(saved, forKey: "assistant.current") }
        let hub = FakeHub()
        let tz = TimeZone.current.identifier
        hub.on("GET", "/api/v1/assistant") { req in
            Data((req.headers[Self.header] == "m-1" ? #"{"machine_id":"w","launcher":"codex","cwd":"/w","conversations":[],"timezone":"\#(tz)"}"#
                                                     : #"{"machine_id":"h","launcher":"claude","cwd":"/h","conversations":[],"timezone":"\#(tz)"}"#).utf8)
        }
        let model = try model(hub, roster: try Self.roster())
        model.selectAssistantManager("m-1")
        await waitUntil { model.assistantConfig?.launcher == "codex" }
        hub.on("GET", "/api/v1/managers", json: #"""
        {"default":"m-2","managers":[{"id":"assistant","name":"助手","online":true,"builtin":true},
          {"id":"m-1","name":"工作","online":true},{"id":"m-2","name":"家里","online":true,"default":true}]}
        """#)
        await model.refreshManagerRoster("")
        #expect(model.assistantManagerID == "m-1")
        let group = model.hub?.group ?? ""
        await waitUntil { model.assistantByGroup[group] != nil }
        #expect(model.assistantByGroup[group]?.launcher == "claude", "Health and the channels have the new default's settings")
    }

    @Test func aSettingsReadForAPageLeftDoesntSayTheyCouldntLoad() async throws {
        let saved = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(saved, forKey: "assistant.current") }
        let hub = FakeHub()
        let tz = TimeZone.current.identifier
        hub.on("GET", "/api/v1/assistant") { req in
            Data((req.headers[Self.header] == "m-1" ? #"{"machine_id":"w","launcher":"codex","cwd":"/w","conversations":[],"timezone":"\#(tz)"}"#
                                                     : #"{"machine_id":"a","launcher":"","cwd":"/a","conversations":[],"timezone":"Nowhere/Else"}"#).utf8)
        }
        let model = try model(hub, roster: try Self.roster())
        hub.on("PUT", "/api/v1/assistant") { _ in
            model.selectAssistantManager("m-1") // the page moves on while the default's zone is saved, which fails
            throw APIError(status: 500, message: "down")
        }
        await model.refreshAssistant()
        #expect(model.assistantManagerID == "m-1" && !model.assistantConfigLoadFailed)
    }

    @Test func noMainConversationStartsBeforeItsManagersSettingsAreRead() async throws {
        let saved = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(saved, forKey: "assistant.current") }
        let hub = FakeHub()
        let tz = TimeZone.current.identifier
        hub.on("GET", "/api/v1/assistant") { _ in throw APIError(status: 503, message: "away") }
        hub.on("POST", "/api/v1/assistant/conversations",
               json: #"{"session":{"session_id":"s1","status":"working","state":"working","assistant":true,"pending_interactions":0}}"#)
        let model = try model(hub, roster: try Self.roster())
        func main() throws -> Any? {
            let body = try #require(hub.sent("POST", "/api/v1/assistant/conversations").last?.body)
            return try #require(JSONSerialization.jsonObject(with: body) as? [String: Any])["main"]
        }
        model.selectAssistantManager("m-1")
        await model.askPersona("hello")
        #expect(try main() == nil, "whether m-1 has a main conversation is unknown: a plain one, which replaces none")
        hub.on("GET", "/api/v1/assistant", json: #"{"machine_id":"w","launcher":"codex","cwd":"/w","conversations":[],"timezone":"\#(tz)"}"#)
        await model.refreshAssistant()
        await model.askPersona("hello again")
        #expect(try main() as? Bool == true, "read, with none: the first becomes it")
    }

    private func waitUntil(_ what: () -> Bool) async {
        let deadline = Date.now + 5
        while !what(), Date.now < deadline { try? await Task.sleep(for: .milliseconds(20)) }
    }

    // MARK: the page's manager

    @Test func thePageStartsOnTheDefaultAndShowsThePick() throws {
        let hub = FakeHub()
        let model = try model(hub, roster: try Self.roster())
        #expect(model.assistantManagerID == nil && model.assistantManager?.id == "assistant" && model.api?.shownManager == nil)
        model.selectAssistantManager("m-1")
        #expect(model.assistantManagerID == "m-1" && model.assistantManager?.name == "工作")
        #expect(model.api?.shownManager == "m-1" && model.api("")?.shownManager == "m-1")
        let before = try #require(model.api)
        model.selectAssistantManager("gone")
        #expect(model.assistantManagerID == "m-1", "one the roster doesn't have can't be picked")
        model.selectAssistantManager(nil)
        #expect(model.assistantManagerID == nil && model.assistantPicks.isEmpty, "no pick: the hub's default")
        #expect(!model.showsPage(of: before), "what was read for the manager before doesn't land")
        model.selectAssistantManager("m-2")
        #expect(model.assistantManagerOffline?.id == "m-2" && model.currentAssistant?.id == "m-2", "an offline one can be looked at")
        model.selectAssistantManager("assistant")
        #expect(model.assistantManagerID == nil && model.assistantPicks == ["": "assistant"], "the default, picked: none is named")

        model.managerRosters[""] = try Self.roster(Self.oneManager)
        model.selectAssistantManager("m-1")
        #expect(model.assistantManagerID == nil && model.assistantManager == nil, "one manager: nothing is named")
        #expect(model.currentAssistant?.name == "助手" && model.managerRoster?.isMulti == false && model.showsAssistantSwitcher,
                "one manager: it is current, with the roster switcher")
        model.managerRosters[""] = nil
        #expect(model.currentAssistant == nil && !model.showsAssistantSwitcher, "a hub without a roster: no switcher")
    }

    @Test func thePickIsKeptOnThisDeviceAcrossLaunches() async throws {
        let folder = FileManager.default.temporaryDirectory.appending(path: "manager-switch-" + UUID().uuidString)
        let hub = FakeHub()
        let first = try model(hub, roster: try Self.roster(), folder: folder)
        first.selectAssistantManager("m-1")
        #expect(AssistantPickStore.load(folder: folder) == ["": "m-1"])

        // The next launch routes to the saved pick before the roster arrives, not to another assistant's settings.
        hub.on("GET", "/api/v1/managers", json: Self.roster)
        let next = try model(hub, roster: nil, folder: folder)
        #expect(next.assistantPicks == ["": "m-1"] && next.assistantManagerID == "m-1" && next.api?.shownManager == "m-1")
        await next.refreshManagerRoster("")
        #expect(next.assistantManagerID == "m-1" && next.currentAssistant?.name == "工作" && next.api?.shownManager == "m-1")

        // Gone from the roster: back to the default, for good.
        hub.on("GET", "/api/v1/managers", json: Self.withoutWork)
        await next.refreshManagerRoster("")
        #expect(next.assistantManagerID == nil && next.currentAssistant?.id == "assistant")
        #expect(AssistantPickStore.load(folder: folder).isEmpty)
    }

    /// A hub that answers the roster with a plain 404 (from before managers, or put back to one) has none: the roster
    /// kept from the last launch goes, and the sidebar's header is Persona's name again.
    @Test func aHubWithoutARosterDropsTheOneKeptFromTheLastLaunch() async throws {
        let old = FakeHub() // no roster route: 404 with no code, as such a hub's router answers
        let model = try model(old, roster: try Self.roster()) // as the snapshot leaves it
        #expect(model.currentAssistant?.id == "assistant")
        await model.refreshManagerRoster("")
        #expect(model.managerRoster == nil && model.currentAssistant == nil, "none from this hub: no switcher")
    }

    @Test func aLegacyHubDoesNotNameTheRememberedPickOnceAbsenceIsConfirmed() async throws {
        let folder = FileManager.default.temporaryDirectory.appending(path: "manager-startup-" + UUID().uuidString)
        let first = try model(FakeHub(), roster: try Self.roster(), folder: folder)
        first.selectAssistantManager("m-1")
        let legacy = try model(FakeHub(), roster: nil, folder: folder)
        #expect(legacy.assistantManagerID == "m-1")
        await legacy.refreshManagerRoster("")
        #expect(legacy.assistantManagerID == nil && legacy.api?.shownManager == nil)
        #expect(legacy.assistantPicks == ["": "m-1"], "a compatibility read does not erase the user's pick")
    }

    @Test func thePickStaysOnItsManagerWhenTheDefaultMoves() async throws {
        let saved = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(saved, forKey: "assistant.current") }
        let hub = FakeHub()
        let model = try model(hub, roster: try Self.roster())
        model.selectAssistantManager("m-1")
        hub.on("GET", "/api/v1/managers", json: #"""
        {"default":"m-1","managers":[{"id":"assistant","name":"助手","online":true,"builtin":true},
          {"id":"m-1","name":"工作","online":true,"default":true},{"id":"m-2","name":"家里","online":false}]}
        """#)
        await model.refreshManagerRoster("")
        #expect(model.currentAssistant?.id == "m-1" && model.assistantManagerID == nil, "still 工作, now the default: none is named")
        model.selectAssistantManager("assistant")
        #expect(model.assistantManagerID == "assistant" && model.api?.shownManager == "assistant", "the hub's own, no longer the default, is named")
    }

    @Test func eachManagersSettingsComeBackWithIt() async throws {
        let saved = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(saved, forKey: "assistant.current") }
        let hub = FakeHub()
        let tz = TimeZone.current.identifier
        hub.on("GET", "/api/v1/assistant") { req in
            let other = req.headers[Self.header] == "m-1"
            return Data((other ? #"{"machine_id":"w","launcher":"codex","cwd":"/w","conversations":[],"timezone":"\#(tz)"}"#
                               : #"{"machine_id":"a","launcher":"claude","cwd":"/a","conversations":[],"timezone":"\#(tz)","main":"main-a"}"#).utf8)
        }
        let model = try model(hub, roster: try Self.roster())
        await model.refreshAssistant()
        #expect(model.assistantConfig?.main == "main-a")
        model.selectAssistantManager("m-1")
        #expect(model.assistantConfig == nil, "nothing read of it yet in this run")
        await model.refreshAssistant()
        #expect(model.assistantConfig?.launcher == "codex" && model.assistantConfig?.main == nil)
        #expect(model.healthMachineID == "a", "the health space stays on the default's machine")
        model.selectAssistantManager(nil)
        #expect(model.assistantConfig?.main == "main-a", "the default's, held")
        model.selectAssistantManager("m-1")
        #expect(model.assistantConfig?.launcher == "codex", "what it read in this run")
    }

    @Test func aManagerLeavingTheRosterTakesThePageBackToTheDefault() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/managers", json: Self.withoutWork)
        let model = try model(hub, roster: try Self.roster())
        model.selectAssistantManager("m-1")
        await model.refreshManagerRoster("")
        #expect(model.assistantManagerID == nil && model.assistantManager?.id == "assistant" && model.api?.shownManager == nil)
        #expect(model.assistantPicks.isEmpty, "it doesn't come back with the manager")
    }

    @Test func theListsAreTheShownManagers() throws {
        let roster = try Self.roster()
        let sessions = [try session("a1", manager: "assistant"), try session("w1", manager: "m-1"), try session("n1", manager: nil),
                        try session("x1", manager: "gone"), try session("w2", manager: "m-1", archived: true),
                        try session("h1", manager: "assistant", space: Session.healthSpace), try session("h2", manager: "m-1", space: Session.healthSpace)]
        let every = AssistantLists(sessions, group: "")
        #expect(Set(every.open.map(\.sessionID)) == ["a1", "w1", "n1", "x1"] && every.health.count == 2, "one manager: every conversation")
        let work = AssistantLists(sessions, group: "", shown: .init(roster: roster, manager: "m-1"))
        #expect(work.open.map(\.sessionID) == ["w1"] && work.archived.map(\.sessionID) == ["w2"])
        #expect(work.health.map(\.sessionID) == ["h1"], "the health space stays the default's")
        let mine = AssistantLists(sessions, group: "", shown: .init(roster: roster, manager: "assistant"))
        #expect(Set(mine.open.map(\.sessionID)) == ["a1", "n1", "x1"], "no stamp, or one the roster doesn't have: the default's")
    }

    @Test func anotherAssistantsConversationOpensWithoutSwitching() throws {
        let saved = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(saved, forKey: "assistant.current") }
        let hub = FakeHub()
        let model = try model(hub, roster: try Self.roster())
        model.applySessions([MachineRef(group: "", id: "box"): [try session("w1", manager: "m-1"), try session("a1", manager: "assistant"),
                                                                 try session("h1", manager: "assistant", space: Session.healthSpace)]])
        model.openAssistantConversation(SessionKey(machineID: "box", sessionID: "w1"))
        #expect(model.assistantManagerID == nil && model.assistantCurrent?.sessionID == "w1", "that conversation, on the current assistant")
        #expect(model.assistantConversations.map(\.sessionID) == ["a1"], "whose lists stay its own")
        model.relistAssistant()
        #expect(model.assistantCurrent?.sessionID == "w1", "a push that relists leaves it on screen")

        model.selectAssistantManager("m-1")
        #expect(model.assistantConversations.map(\.sessionID) == ["w1"] && model.assistantCurrent?.sessionID == "w1")
        #expect(model.defaultAssistantConversations.map(\.sessionID) == ["a1"], "a channel's targets stay the default's")
        model.openAssistantConversation(SessionKey(machineID: "box", sessionID: "a1"))
        model.selectAssistantManager("m-2")
        #expect(model.assistantCurrent?.sessionID != "a1", "switching leaves another assistant's conversation")
        model.openAssistantConversation(SessionKey(machineID: "box", sessionID: "h1"))
        #expect(model.assistantManagerID == "m-2" && model.assistantCurrent?.sessionID == "h1", "the health space is the default's and opens too")
    }

    /// Another assistant's conversation names that assistant (the top bar), by its listing or, not listed yet, by the
    /// manager it was opened for, which also reads its links; neither switches.
    @Test func anotherAssistantsConversationNamesItsOwnAssistant() throws {
        let saved = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(saved, forKey: "assistant.current") }
        let hub = FakeHub()
        let model = try model(hub, roster: try Self.roster())
        model.applySessions([MachineRef(group: "", id: "box"): [try session("w1", manager: "m-1"), try session("a1", manager: "assistant"),
                                                                 try session("h1", manager: "assistant", space: Session.healthSpace)]])
        let listed = SessionKey(machineID: "box", sessionID: "w1"), unlisted = SessionKey(machineID: "box", sessionID: "n1")
        #expect(model.otherAssistant(of: listed)?.name == "工作", "工作's conversation while 助手 is current")
        #expect(model.otherAssistant(of: SessionKey(machineID: "box", sessionID: "a1")) == nil, "the current one's: none")
        #expect(model.otherAssistant(of: SessionKey(machineID: "box", sessionID: "h1")) == nil, "the health space: none")
        #expect(model.otherAssistant(of: unlisted) == nil, "not listed and opened for nobody: none")

        model.openAssistantConversation(unlisted, manager: "m-2")
        #expect(model.assistantCurrent == unlisted && model.assistantManagerID == nil, "opened, not switched")
        #expect(model.assistantItemManagers[unlisted] == "m-2" && model.otherAssistant(of: unlisted)?.name == "家里")
        #expect(model.managerRoster?.offlineManager(of: model.assistantItemManagers[unlisted])?.id == "m-2", "家里 is offline: its note shows")
        model.selectAssistantManager("m-1")
        #expect(model.otherAssistant(of: listed) == nil, "工作 current: its own conversation names no other")
    }

    /// Only a change of the current assistant closes the Persona page's pushed pages and takes another assistant's
    /// conversation off screen; re-reading the same one (back online) does neither.
    @Test func onlyASwitchClosesPagesAndLeavesAnotherAssistantsConversation() async throws {
        let saved = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(saved, forKey: "assistant.current") }
        let hub = FakeHub()
        let model = try model(hub, roster: try Self.roster())
        model.applySessions([MachineRef(group: "", id: "box"): [try session("w1", manager: "m-1"), try session("a1", manager: "assistant")]])
        model.selectAssistantManager("m-2") // m-2, offline
        let switches = model.assistantSwitches
        model.openAssistantConversation(SessionKey(machineID: "box", sessionID: "w1")) // m-1's, from a notification
        hub.on("GET", "/api/v1/managers", json: #"""
        {"default":"assistant","managers":[{"id":"assistant","name":"助手","online":true,"default":true,"builtin":true},
          {"id":"m-1","name":"工作","online":true},{"id":"m-2","name":"家里","online":true}]}
        """#)
        await model.refreshManagerRoster("") // m-2 back online: read again, not switched
        #expect(model.assistantManagerID == "m-2" && model.assistantSwitches == switches)
        #expect(model.assistantCurrent?.sessionID == "w1", "工作's conversation stays through a re-read")
        model.selectAssistantManager(nil)
        #expect(model.assistantSwitches == switches + 1, "a switch closes the pushed pages")
        #expect(model.assistantCurrent?.sessionID != "w1", "and leaves another assistant's conversation")
    }

    /// Back from an assistant without a main conversation (none on screen, as a relaunch there finds it), the default's
    /// main conversation opens once its settings are read.
    @Test func backFromAnAssistantWithoutAMainConversationTheDefaultsOpens() async throws {
        let saved = UserDefaults.standard.object(forKey: "assistant.current")
        defer { UserDefaults.standard.set(saved, forKey: "assistant.current") }
        let hub = FakeHub()
        let tz = TimeZone.current.identifier
        hub.on("GET", "/api/v1/assistant") { req in
            Data((req.headers[Self.header] == "m-1" ? #"{"machine_id":"w","launcher":"codex","cwd":"/w","conversations":[],"timezone":"\#(tz)"}"#
                                                     : #"{"machine_id":"a","launcher":"claude","cwd":"/a","conversations":[],"timezone":"\#(tz)","main":"main-a"}"#).utf8)
        }
        let model = try model(hub, roster: try Self.roster())
        model.selectAssistantManager("m-1")
        await waitUntil { model.assistantConfig?.launcher == "codex" }
        model.assistantCurrent = nil // m-1 has no main conversation: its new-conversation page
        model.selectAssistantManager(nil)
        await waitUntil { model.assistantCurrent != nil }
        #expect(model.assistantCurrent?.sessionID == "main-a", "the default's main conversation")
    }

    /// While another assistant is current, the default's settings (Health, the channels) are read once in this run.
    @Test func anotherAssistantCurrentStillReadsTheDefaultsSettingsOnce() async throws {
        let hub = FakeHub()
        let tz = TimeZone.current.identifier
        hub.on("GET", "/api/v1/assistant") { req in
            Data((req.headers[Self.header] == "m-1" ? #"{"machine_id":"w","launcher":"codex","cwd":"/w","conversations":[],"timezone":"\#(tz)"}"#
                                                     : #"{"machine_id":"h","launcher":"claude","cwd":"/h","conversations":[],"timezone":"\#(tz)"}"#).utf8)
        }
        hub.on("GET", "/api/v1/managers", json: Self.roster)
        func defaults() -> Int { hub.sent("GET", "/api/v1/assistant").filter { $0.headers[Self.header] == nil }.count }
        let folder = FileManager.default.temporaryDirectory.appending(path: "manager-switch-" + UUID().uuidString)
        AssistantPickStore.save(["": "m-1"], folder: folder)
        let model = try model(hub, roster: nil, folder: folder) // a launch on m-1, its roster not read yet
        await model.refreshManagerRoster("")
        #expect(model.assistantManagerID == "m-1")
        await waitUntil { model.assistantByGroup[""] != nil }
        #expect(model.assistantByGroup[""]?.machineID == "h" && model.healthMachineID == "h", "Health follows the default's machine")
        let after = defaults()
        hub.on("GET", "/api/v1/managers", json: #"""
        {"default":"assistant","managers":[{"id":"assistant","name":"助手","online":true,"default":true,"builtin":true},
          {"id":"m-1","name":"工作","online":true},{"id":"m-2","name":"家里","online":true}]}
        """#)
        await model.refreshManagerRoster("") // the roster changed (m-2 back), the default didn't
        #expect(defaults() == after, "once in this run")
    }

    /// A cold start with a snapshot is on the assistant picked before, with the settings it held of it, before the hub
    /// answers.
    @Test func aColdStartFromTheSnapshotIsOnThePickedAssistant() throws {
        let folder = FileManager.default.temporaryDirectory.appending(path: "manager-switch-" + UUID().uuidString)
        let roster = try Self.roster()
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        try JSON.encoder.encode(AppModel.DevSnapshot(machines: [], sessions: [], interactions: [], assistant: try Self.state("claude"),
                                                     managers: roster.managers, defaultManager: roster.defaultID,
                                                     picked: try Self.state("codex"), pickedManager: "m-1"))
            .write(to: folder.appending(path: AppModel.snapshotName("") + ".json"))
        AssistantPickStore.save(["": "m-1"], folder: folder)
        let model = try model(FakeHub(), roster: nil, folder: folder)
        model.restoreSaved()
        #expect(model.currentAssistant?.name == "工作" && model.assistantManagerID == "m-1" && model.api?.shownManager == "m-1")
        #expect(model.assistantConfig?.launcher == "codex", "its settings as the snapshot kept them")
        #expect(model.assistantByGroup[""]?.launcher == "claude", "and the default's, for Health and the channels")
    }

    /// The snapshot's fields for the current assistant come back as written, and a snapshot from before them reads.
    @Test func theSnapshotKeepsTheRosterAndThePickedAssistantAndReadsAnOldOne() throws {
        let roster = try Self.roster()
        let snap = AppModel.DevSnapshot(machines: [], sessions: [], interactions: [], assistant: nil, managers: roster.managers,
                                        defaultManager: "assistant", picked: try Self.state("codex"), pickedManager: "m-1")
        let back = try JSON.decoder.decode(AppModel.DevSnapshot.self, from: JSON.encoder.encode(snap))
        #expect(back.managers == roster.managers && back.defaultManager == "assistant" && back.pickedManager == "m-1")
        #expect(back.picked?.launcher == "codex")
        let old = try JSON.decoder.decode(AppModel.DevSnapshot.self, from: Data(#"{"machines":[],"sessions":[],"interactions":[]}"#.utf8))
        #expect(old.managers == nil && old.defaultManager == nil && old.picked == nil && old.pickedManager == nil)
    }

    private static func state(_ launcher: String) throws -> APIClient.AssistantState {
        try JSON.decoder.decode(APIClient.AssistantState.self, from: Data(#"{"machine_id":"x","launcher":"\#(launcher)","cwd":"/x","conversations":[]}"#.utf8))
    }

    @Test func nothingGoesToAnOfflineManager() async throws {
        let hub = FakeHub()
        let model = try model(hub, roster: try Self.roster())
        #expect(model.offlineManager(of: try session("h", manager: "m-2"))?.name == "家里")
        #expect(model.offlineManager(of: try session("w", manager: "m-1")) == nil && model.offlineManager(of: nil) == nil)
        model.selectAssistantManager("m-2")
        await model.askPersona("hello")
        #expect(model.toast?.style == .error && model.toast?.message.contains("家里") == true)
        #expect(hub.sent("POST", "/api/v1/assistant/conversations").isEmpty)
        model.managerRosters[""] = try Self.roster(Self.oneManager)
        #expect(model.offlineManager(of: try session("h", manager: "m-2")) == nil, "one manager: nothing of it shows")
    }
}
