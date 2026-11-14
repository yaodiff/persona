import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Add Computer for the current assistant (docs/protocol/protocol.md "Computer pairing codes", docs/assistant/managers.md
/// "The current assistant"): the request names the assistant's real id, the reply says where the computer went, an offline
/// assistant takes none, and the hub's own refusals are shown as it worded them. Made-up values.
@MainActor
struct AddComputerAssistantTests {
    private static let start = Date(timeIntervalSince1970: 1_800_000_000)

    private static let main = HubManager(id: "assistant", name: "Main", online: true, isDefault: true, builtin: true)
    private static let work = HubManager(id: "work", name: "Work", online: true)
    private static let home = HubManager(id: "home", name: "Home", online: false)
    private static let multi = ManagerRoster(managers: [main, work, home])
    /// Several assistants whose default is not the hub's own.
    private static let movedDefault = ManagerRoster(managers: [
        HubManager(id: "assistant", name: "Main", online: true, builtin: true),
        HubManager(id: "work", name: "Work", online: true, isDefault: true),
    ])
    private static let single = ManagerRoster(managers: [main])

    private static func codeJSON(machine: String = "macbook", added: Bool = true, manager: String? = nil) -> String {
        let at = ISO8601DateFormatter().string(from: start.addingTimeInterval(600))
        var json = #"{"token":"jp_example","expires_at":"\#(at)","machine":"\#(machine)","name":"MacBook","trust":"pending","added":\#(added),"#
            + #""pair_url":"persona://link?machine=\#(machine)""#
        if let manager { json += #","manager":"\#(manager)""# }
        return json + "}"
    }

    private static func code(_ json: String) throws -> APIClient.ComputerCode {
        try JSON.decoder.decode(APIClient.ComputerCode.self, from: Data(json.utf8))
    }

    private static func listed(_ id: String, manager: String?) throws -> Machine {
        let tail = manager.map { #","manager":"\#($0)""# } ?? ""
        return try JSON.decoder.decode(Machine.self, from: Data(#"{"id":"\#(id)","name":"MacBook","kind":"link","online":true\#(tail)}"#.utf8))
    }

    private func model(_ hub: FakeHub, _ assistant: AddAssistant?, machines: [Machine] = []) -> AddComputerModel {
        AddComputerModel(.add, deps: .init(api: { _ in hub.client }, now: { Self.start }, assistant: { _ in assistant }, machines: { machines }))
    }

    private func sentAdd(_ hub: FakeHub) throws -> [String: String] {
        let body = try #require(hub.sent("POST", "/api/v1/auth/pair-codes").first?.body)
        let sent = try #require(try JSONSerialization.jsonObject(with: body) as? [String: [String: String]])
        return try #require(sent["add"])
    }

    private func added(_ assistant: AddAssistant?, reply: String = Self.codeJSON(), machines: [Machine] = []) async -> (AddComputerModel, FakeHub) {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/auth/pair-codes", json: reply)
        let page = model(hub, assistant, machines: machines)
        page.name = "MacBook"
        await page.submit()
        return (page, hub)
    }

    // MARK: The request

    @Test func theRequestNamesTheCurrentAssistantsRealId() async throws {
        let (_, hub) = await added(AddAssistant(roster: Self.multi, pick: "work"))
        #expect(try sentAdd(hub) == ["name": "MacBook", "os": "mac", "icon": "laptop", "manager": "work"])
    }

    @Test func theDefaultAssistantIsNamedByItsRealIdToo() async throws {
        let (_, hub) = await added(AddAssistant(roster: Self.multi, pick: nil))
        #expect(try sentAdd(hub)["manager"] == "assistant")
        // A default that is not the hub's own assistant is named as itself, not as the placeholder.
        let (_, moved) = await added(AddAssistant(roster: Self.movedDefault, pick: nil))
        #expect(try sentAdd(moved)["manager"] == "work")
    }

    @Test func oneAssistantIsNamedToo() async throws {
        let (page, hub) = await added(AddAssistant(roster: Self.single, pick: nil))
        #expect(try sentAdd(hub)["manager"] == "assistant")
        // The reply of a hub from before the field puts it there: nothing to say.
        #expect(page.addedUnder == nil)
    }

    @Test func aHubWithoutARosterGetsTheRequestItAlwaysGot() async throws {
        let (page, hub) = await added(AddAssistant(roster: nil, pick: nil))
        #expect(try sentAdd(hub) == ["name": "MacBook", "os": "mac", "icon": "laptop"])
        #expect(page.addedUnder == nil && page.assistantOffline == nil)
        let (_, nothing) = await added(nil)
        #expect(try sentAdd(nothing).keys.contains("manager") == false)
    }

    @Test func withoutARosterTheKeptPickIsSent() async throws {
        let (_, hub) = await added(AddAssistant(roster: nil, pick: "work"))
        #expect(try sentAdd(hub)["manager"] == "work")
    }

    @Test func aNewCodeForAComputerNamesNoAssistant() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/auth/pair-codes", json: Self.codeJSON())
        let page = AddComputerModel(.confirm(MachineRef(id: "macbook")), deps: .init(api: { _ in hub.client }, now: { Self.start },
                                                                                       assistant: { _ in AddAssistant(roster: Self.multi, pick: "work") }))
        await page.reissue()
        let body = try #require(hub.sent("POST", "/api/v1/auth/pair-codes").first?.body)
        let sent = try #require(try JSONSerialization.jsonObject(with: body) as? [String: String])
        #expect(sent == ["machine": "macbook"])
    }

    @Test func aNewCodeKeepsWhatTheFirstOneAskedFor() async throws {
        let (page, hub) = await added(AddAssistant(roster: Self.multi, pick: "work"), reply: Self.codeJSON(manager: "home"))
        #expect(page.addedUnder == "Home")
        hub.on("POST", "/api/v1/auth/pair-codes", json: Self.codeJSON(manager: "home"))
        await page.reissue()
        #expect(page.asked == "work" && page.addedUnder == "Home")
    }

    // MARK: The reply

    @Test func theReplyDecodesItsManagerWhenThereIsOne() throws {
        #expect(try Self.code(Self.codeJSON(manager: "work")).manager == "work")
        #expect(try Self.code(Self.codeJSON()).manager == nil)
    }

    @Test func aComputerWhereItWasAskedForSaysNothing() throws {
        let reply = try Self.code(Self.codeJSON(manager: "work"))
        #expect(AddComputerText.addedUnder(asked: "work", code: reply, listed: nil, roster: Self.multi) == nil)
    }

    @Test func anotherAssistantsNameIsSaid() throws {
        let reply = try Self.code(Self.codeJSON(manager: "home"))
        #expect(AddComputerText.addedUnder(asked: "work", code: reply, listed: nil, roster: Self.multi) == "Home")
    }

    @Test func aHubFromBeforeTheFieldPutsANewComputerUnderItsOwnAssistant() throws {
        let reply = try Self.code(Self.codeJSON())
        // The default asked for is the hub's own: nothing to say. Another one asked for: it went elsewhere.
        #expect(AddComputerText.addedUnder(asked: "assistant", code: reply, listed: nil, roster: Self.multi) == nil)
        #expect(AddComputerText.addedUnder(asked: "work", code: reply, listed: nil, roster: Self.multi) == "Main")
    }

    @Test func aReusedComputerStaysWhereTheListHasIt() throws {
        let reused = try Self.code(Self.codeJSON(added: false))
        #expect(AddComputerText.addedUnder(asked: "work", code: reused, listed: "work", roster: Self.multi) == nil)
        #expect(AddComputerText.addedUnder(asked: "work", code: reused, listed: "home", roster: Self.multi) == "Home")
        // Not listed under any: the hub's own assistant.
        #expect(AddComputerText.addedUnder(asked: "work", code: reused, listed: nil, roster: Self.multi) == "Main")
    }

    @Test func aReusedComputerIsLookedUpInTheListThePageHas() async throws {
        let list = [try Self.listed("macbook", manager: "home")]
        let (page, _) = await added(AddAssistant(roster: Self.multi, pick: "work"), reply: Self.codeJSON(added: false), machines: list)
        #expect(page.addedUnder == "Home")
    }

    @Test func withOneAssistantNothingIsSaid() throws {
        let reply = try Self.code(Self.codeJSON(manager: "work"))
        #expect(AddComputerText.addedUnder(asked: "assistant", code: reply, listed: nil, roster: Self.single) == nil)
        #expect(AddComputerText.addedUnder(asked: nil, code: reply, listed: nil, roster: Self.multi) == nil)
        #expect(AddComputerText.addedUnder(asked: "work", code: reply, listed: nil, roster: nil) == nil)
    }

    @Test func anAssistantTheRosterLacksIsNamedById() throws {
        let reply = try Self.code(Self.codeJSON(manager: "gone"))
        #expect(AddComputerText.addedUnder(asked: "work", code: reply, listed: nil, roster: Self.multi) == "gone")
    }

    // MARK: An offline assistant

    @Test func anOfflineCurrentAssistantTakesNoComputer() async throws {
        let hub = FakeHub()
        let page = model(hub, AddAssistant(roster: Self.multi, pick: "home"))
        page.name = "MacBook"
        #expect(page.assistantOffline?.name == "Home")
        await page.submit()
        #expect(hub.requests.isEmpty && page.stage == .form)
    }

    @Test func anOnlineAssistantTakesOne() {
        let hub = FakeHub()
        #expect(model(hub, AddAssistant(roster: Self.multi, pick: "work")).assistantOffline == nil)
        #expect(model(hub, AddAssistant(roster: Self.multi, pick: nil)).assistantOffline == nil)
    }

    @Test func anUnknownRosterIsNotAnOfflineAssistant() {
        let hub = FakeHub()
        #expect(model(hub, AddAssistant(roster: nil, pick: "home")).assistantOffline == nil)
        #expect(model(hub, nil).assistantOffline == nil)
    }

    @Test func oneAssistantIsNeverToldOffline() {
        let offline = ManagerRoster(managers: [HubManager(id: "assistant", name: "Main", online: false, isDefault: true, builtin: true)])
        #expect(model(FakeHub(), AddAssistant(roster: offline, pick: nil)).assistantOffline == nil)
    }

    @Test func anAssistantStillInstallingIsNotTheCurrentOne() {
        // The pick names one that is not installed: it falls back to the default, which is online.
        let installing = ManagerRoster(managers: [Self.main, Self.work, HubManager(id: "new", name: "New", online: false, installing: Self.start)])
        #expect(model(FakeHub(), AddAssistant(roster: installing, pick: "new")).assistantOffline == nil)
    }

    // MARK: The hub's refusals

    @Test(arguments: [(404, "no_manager"), (409, "installing"), (503, "assistant_offline"), (503, "owners_unknown")])
    func theHubsOwnWordsAreShown(status: Int, code: String) async throws {
        let said = "Hub 的原话：\(code)"
        let hub = FakeHub()
        hub.on("POST", "/api/v1/auth/pair-codes") { _ in throw APIError(status: status, message: said, fields: ["code": code, "error": said]) }
        let page = model(hub, AddAssistant(roster: Self.multi, pick: "work"))
        page.name = "MacBook"
        await page.submit()
        #expect(page.failure?.message == said && page.failure?.code == code)
        // It can be tried again.
        #expect(page.stage == .form)
        hub.on("POST", "/api/v1/auth/pair-codes", json: Self.codeJSON(manager: "work"))
        await page.submit()
        #expect(page.stage == .waiting && page.failure == nil && hub.sent("POST", "/api/v1/auth/pair-codes").count == 2)
    }
}

// MARK: A roster not known yet, and the group the assistant belongs to

extension AddComputerAssistantTests {
    @MainActor private final class Answers {
        var calls = 0
        let first: AddAssistant
        let later: AddAssistant
        let switchAt: Int
        init(first: AddAssistant, later: AddAssistant, switchAt: Int) { (self.first, self.later, self.switchAt) = (first, later, switchAt) }
        func next() -> AddAssistant { calls += 1; return calls > switchAt ? later : first }
    }

    @Test func nothingKnownAndNoAnswerYetIsUnknown() {
        #expect(AddAssistant(roster: nil, pick: nil, settled: false).unknown)
        #expect(!AddAssistant(roster: nil, pick: nil, settled: true).unknown, "a hub without a roster has said so")
        #expect(!AddAssistant(roster: nil, pick: "work", settled: false).unknown, "a kept pick is named meanwhile")
        #expect(!AddAssistant(roster: Self.multi, pick: nil, settled: false).unknown, "a roster kept from the last launch is known")
    }

    @Test func nextWaitsForTheRosterAndNamesTheDefaultItSays() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/auth/pair-codes", json: Self.codeJSON(manager: "work"))
        let answers = Answers(first: AddAssistant(roster: nil, pick: nil, settled: false),
                              later: AddAssistant(roster: Self.movedDefault, pick: nil, settled: true), switchAt: 3)
        let page = AddComputerModel(.add, deps: .init(api: { _ in hub.client }, now: { Self.start }, assistant: { _ in answers.next() }))
        page.name = "MacBook"
        await page.submit()
        #expect(try sentAdd(hub)["manager"] == "work", "the roster's default, not a silent placeholder")
        #expect(page.stage == .waiting && page.addedUnder == nil)
    }

    @Test func aRosterThatTurnsOutOfflineWhileWaitingAddsNothing() async throws {
        let hub = FakeHub()
        let answers = Answers(first: AddAssistant(roster: nil, pick: nil, settled: false),
                              later: AddAssistant(roster: Self.multi, pick: "home", settled: true), switchAt: 3)
        let page = AddComputerModel(.add, deps: .init(api: { _ in hub.client }, now: { Self.start }, assistant: { _ in answers.next() }))
        page.name = "MacBook"
        await page.submit()
        #expect(hub.requests.isEmpty && page.stage == .form)
    }

    @Test func onlyTheHubLevelGroupHasACurrentAssistant() {
        let there = AddComputerText.assistant(in: "work", hubGroup: "work", roster: Self.multi, pick: "work", settled: true)
        #expect(there == AddAssistant(roster: Self.multi, pick: "work", settled: true))
        #expect(AddComputerText.assistant(in: "home", hubGroup: "work", roster: Self.multi, pick: "work", settled: true) == nil)
        #expect(AddComputerText.assistant(in: "", hubGroup: "", roster: nil, pick: nil, settled: true) != nil, "the default group is a group too")
        #expect(AddComputerText.assistant(in: "work", hubGroup: nil, roster: Self.multi, pick: nil, settled: true) == nil)
    }
}
