import XCTest
@testable import Persona
import PersonaWire

/// Replays the shared fixture spec/fixtures/needs-you/cases.json: the Agents tab's Needs You is one row per session that
/// waits for the user (its cards under it), the section, the filter and the badge count those rows, and the badge leaves
/// out the muted agents and quiet tasks' agents.
@MainActor
final class AgentsNeedsYouFixtureTests: XCTestCase {
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    private struct Fixture: Decodable {
        var version: Int
        var cases: [Case]
    }

    private struct Case: Decodable {
        var name: String
        var input: Input
        var expected: Expected
    }

    private struct Input: Decodable {
        var sessions: [Session]
        var interactions: [Interaction]
        var mutes: Pins
        var quiet: [String]
    }

    private struct Expected: Decodable {
        var rows: [Row]
        var badge: Int
    }

    private struct Row: Decodable, Equatable {
        var id: String
        var cards: [String]
    }

    func testEveryCaseCountsSessionsNotCards() throws {
        let data = try Data(contentsOf: Self.repo.appending(path: "spec/fixtures/needs-you/cases.json"))
        let fixture = try JSON.decoder.decode(Fixture.self, from: data)
        XCTAssertEqual(fixture.version, 1)
        XCTAssertGreaterThanOrEqual(fixture.cases.count, 8)
        for c in fixture.cases {
            let i = c.input
            let byKey = Dictionary(i.sessions.map { ($0.key, $0) }, uniquingKeysWith: { a, _ in a })
            // As the list and AppModel read them: pending cards oldest first, the sessions the Needs You filter matches.
            let pending = i.interactions.sorted { ($0.createdAt ?? .distantPast) < ($1.createdAt ?? .distantPast) }
            let waiting = AgentsNeedsYou.waiting(i.sessions)
            XCTAssertEqual(waiting.map(\.key), i.sessions.filter { !$0.assistant && AgentFilter.needsYou.matches($0) }.map(\.key),
                           "\(c.name): the Needs You filter's sessions")
            let rows = AgentsNeedsYou.rows(cards: AgentsNeedsYou.cards(pending, session: { byKey[$0] }), waiting: waiting)
            XCTAssertEqual(rows.map { Row(id: $0.id, cards: $0.cards.map(\.id)) }, c.expected.rows, c.name)
            // As AppModel counts it: every pending card (the Persona tab's are its own place), every waiting session.
            let badge = AgentsNeedsYou.badge(AgentsNeedsYou.rows(cards: pending, waiting: waiting)) {
                TabBadges.place($0, session: byKey[$0], quiet: i.quiet.contains(AppModel.pinKey(session: $0)), mutes: i.mutes)
            }
            XCTAssertEqual(badge, c.expected.badge, "\(c.name): badge")
        }
    }
}
