import Foundation
import Testing
@testable import Persona
import PersonaWire

/// New Agent's recent setups (#2134): every case of spec/fixtures/recents/recent_setups.json (the rules the web and
/// Android run too), the store, and what a card says and fills in.
@MainActor struct RecentSetupsTests {
    private static let fixtureURL = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().appending(path: "spec/fixtures/recents/recent_setups.json")

    private struct Fixture: Decodable {
        let maxEntries: Int
        let maxCards: Int
        let cases: [Case]
        enum CodingKeys: String, CodingKey {
            case cases
            case maxEntries = "max_entries"
            case maxCards = "max_cards"
        }
    }

    private struct Case: Decodable {
        let name: String
        let operation: String
        let input: Input
        let expected: Expected
    }

    private struct Input: Decodable {
        var before: [RecentSetup]?
        var setup: RecentSetup?
        var entries: [RecentSetup]?
        var machines: [RecentSetups.Machine]?
        var machine: String?
        var launcher: String?
    }

    private struct Expected: Decodable {
        var after: [RecentSetup]?
        var cards: [RecentSetups.Card]?
        var model: String?
    }

    private static func fixture() throws -> Fixture {
        try JSONDecoder().decode(Fixture.self, from: Data(contentsOf: fixtureURL))
    }

    private static func setup(_ machine: String, _ cwd: String, launcher: String = "", model: String = "", agent: String = "claude",
                              mode: String = "", effort: String = "", at: String = "2026-11-09T03:00:00Z") -> RecentSetup {
        RecentSetup(machine: machine, cwd: cwd, launcher: launcher, model: model, agentType: agent, mode: mode, effort: effort, at: at)
    }

    // MARK: The shared fixtures

    @Test func limitsAreTheFixtures() throws {
        let f = try Self.fixture()
        #expect(RecentSetups.maxEntries == f.maxEntries)
        #expect(RecentSetups.maxCards == f.maxCards)
    }

    @Test func everyFixtureCase() throws {
        let f = try Self.fixture()
        var ran: Set<String> = []
        for c in f.cases {
            switch c.operation {
            case "record":
                let before = try #require(c.input.before, "\(c.name): before")
                let setup = try #require(c.input.setup, "\(c.name): setup")
                #expect(RecentSetups.record(setup, into: before) == c.expected.after, "\(c.name)")
            case "cards":
                let entries = try #require(c.input.entries, "\(c.name): entries")
                let machines = try #require(c.input.machines, "\(c.name): machines")
                #expect(RecentSetups.cards(entries, machines: machines) == c.expected.cards, "\(c.name)")
            case "model":
                let entries = try #require(c.input.entries, "\(c.name): entries")
                let got = RecentSetups.model(entries, machine: c.input.machine ?? "", launcher: c.input.launcher ?? "")
                #expect(got == c.expected.model, "\(c.name)")
            default:
                Issue.record("\(c.name): unknown operation \(c.operation)")
            }
            ran.insert(c.operation)
        }
        #expect(ran == ["record", "cards", "model"])
    }

    /// The file keeps the fixtures' field names (`agent_type`), so the setups read back as written.
    @Test func codesWithTheFixtureNames() throws {
        let s = Self.setup("work-b", "/code/notes-app", launcher: "claude", model: "sonnet", mode: "bypassPermissions", effort: "xhigh")
        let json = try #require(try JSONSerialization.jsonObject(with: JSON.encoder.encode(s)) as? [String: String])
        #expect(json["agent_type"] == "claude")
        #expect(Set(json.keys) == ["machine", "cwd", "launcher", "model", "agent_type", "mode", "effort", "at"])
        #expect(try JSON.decoder.decode([RecentSetup].self, from: JSON.encoder.encode([s])) == [s])
    }

    @Test func atIsRFC3339() {
        #expect(RecentSetups.at(Date(timeIntervalSince1970: 1_793_934_000)) == "2026-11-06T03:00:00Z")
    }

    // MARK: The store

    @Test func storeRecordsRemovesAndClearsAndWritesEachTime() {
        var written: [[RecentSetup]] = []
        let a = Self.setup("mac-a", "/code/persona", model: "opus", mode: "plan", effort: "high")
        let store = RecentSetupsStore(load: { [a] }, store: { written.append($0) })
        #expect(store.entries == [a])

        let b = Self.setup("work-b", "/code/notes-app", launcher: "claude", model: "sonnet", at: "2026-11-09T04:00:00Z")
        store.record(b)
        #expect(store.entries == [b, a])
        var again = a
        again.at = "2026-11-09T05:00:00Z"
        store.record(again)
        #expect(store.entries == [again, b], "the same setup moves first with its new time")

        store.remove(b)
        #expect(store.entries == [again])
        store.clear()
        #expect(store.entries.isEmpty)
        #expect(written == [[b, a], [again, b], [again], []])
    }

    /// The cards come with their entries: only machines the hub lists, offline ones marked.
    @Test func storeCardsCarryTheirSetups() {
        let a = Self.setup("mac-a", "/code/persona")
        let gone = Self.setup("gone-d", "/code/old")
        let c = Self.setup("home-c", "/code/data-pipeline", launcher: "codex", model: "gpt-5.5", agent: "codex")
        let store = RecentSetupsStore(load: { [gone, a, c] }, store: { _ in })
        let cards = store.cards([.init(id: "mac-a", online: true), .init(id: "home-c", online: false)])
        #expect(cards.map(\.setup) == [a, c])
        #expect(cards.map(\.online) == [true, false])
    }

    /// Picking another machine brings back the model of its latest setup with the launcher it restores; none, the default.
    @Test func aMachineChangeRestoresItsModel() {
        let store = RecentSetupsStore(load: { [
            Self.setup("mac-a", "/code/persona", launcher: "codex", model: "gpt-5.5", agent: "codex"),
            Self.setup("work-b", "/code/notes-app", launcher: "claude", model: "sonnet"),
            Self.setup("mac-a", "/code/other", model: "haiku"),
            Self.setup("mac-a", "/code/persona", model: "opus"),
        ] }, store: { _ in })
        #expect(store.model(machine: "mac-a", launcher: "") == "haiku")
        #expect(store.model(machine: "work-b", launcher: "claude") == "sonnet")
        #expect(store.model(machine: "work-b", launcher: "") == "", "another launcher's model is not this one's")
        #expect(store.model(machine: "new-z", launcher: "") == "")
    }

    // MARK: A card

    @Test func aCardFillsEveryField() throws {
        let s = Self.setup("work-b", "/code/notes-app", launcher: "claude", model: "sonnet", mode: "bypassPermissions", effort: "xhigh")
        let fill = try #require(RecentSetupCard(setup: s, online: true).fill)
        #expect(fill == RecentSetupCard.Fill(machine: "work-b", cwd: "/code/notes-app", launcher: "claude", model: "sonnet",
                                             mode: "bypassPermissions", effort: "xhigh"))
    }

    /// A Codex card fills the launcher and model; the machine's own Claude mode and effort stay.
    @Test func aCodexCardLeavesClaudesModes() throws {
        let s = Self.setup("home-c", "/code/data-pipeline", launcher: "codex", model: "gpt-5.5", agent: "codex")
        let card = RecentSetupCard(setup: s, online: true)
        let fill = try #require(card.fill)
        #expect(fill.launcher == "codex" && fill.model == "gpt-5.5")
        #expect(fill.mode == nil && fill.effort == nil)
        #expect(card.modeText == nil && card.effortText == nil, "a Codex card shows launcher and model only")
    }

    @Test func anOfflineCardCannotBePicked() {
        let s = Self.setup("home-c", "/code/data-pipeline", launcher: "codex", model: "gpt-5.5", agent: "codex")
        #expect(RecentSetupCard(setup: s, online: false).fill == nil)
    }

    @Test func bypassIsMarked() {
        let bypass = RecentSetupCard(setup: Self.setup("work-b", "/code/notes-app", mode: "bypassPermissions"), online: true)
        #expect(bypass.bypass)
        #expect(bypass.modeText == String(localized: "Allow Everything"))
        #expect(!RecentSetupCard(setup: Self.setup("mac-a", "/code/persona", mode: "plan"), online: true).bypass)
    }

    @Test func whatACardSays() {
        let card = RecentSetupCard(setup: Self.setup("mac-a", "/Users/me/code/persona", model: "opus", mode: "plan", effort: "high"), online: true)
        #expect(card.launcherText == String(localized: "Use Model Configuration"), "no launcher: follows Model Configuration")
        #expect(card.modelText == "opus")
        #expect(card.modeText == SessionModes.mode("plan")?.title)
        #expect(card.effortText == SessionModes.effort("high")?.title)

        let plain = RecentSetupCard(setup: Self.setup("mac-a", "/code/x", launcher: "sonnetcc"), online: true)
        #expect(plain.launcherText == "sonnetcc")
        #expect(plain.modelText == nil, "the default model is not said")
        #expect(plain.modeText == nil && plain.effortText == nil, "Use Launcher's is not said")

        #expect(RecentSetupCard.folder("/Users/me/code/persona", home: "/Users/me") == "persona")
        #expect(RecentSetupCard.folder("/Users/me/code/persona/", home: nil) == "persona")
        #expect(RecentSetupCard.folder("/Users/me", home: "/Users/me") == "~")
        #expect(RecentSetupCard.folder("", home: "/Users/me") == String(localized: "Temporary Session")) // a start in the desktop app with no folder
        #expect(RecentSetupCard.folder("~", home: nil) == "~")
        #expect(RecentSetupCard.folder("/", home: "/Users/me") == "/")
    }
}
