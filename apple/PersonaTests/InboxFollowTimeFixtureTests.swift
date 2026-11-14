import Foundation
import Testing
@testable import Persona

/// Replays spec/fixtures/inbox/time.json, the follow-up moment contract shared with the web app and Android.
struct InboxFollowTimeFixtureTests {
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    private static let languages = ["en", "zh-Hans", "ja"]

    private struct Fixture: Decodable {
        var operation: String
        var time_zone: String
        var cases: [Case]

        struct Case: Decodable {
            var name: String
            var input: Input
            var expected: [String: String]

            struct Input: Decodable {
                var now: String
                var at: String
            }
        }
    }

    @Test func everySharedFollowTimeCaseMatchesInEveryLanguage() throws {
        let data = try Data(contentsOf: Self.repo.appending(path: "spec/fixtures/inbox/time.json"))
        let fixture = try JSON.decoder.decode(Fixture.self, from: data)
        #expect(fixture.operation == "inbox_follow_time")
        #expect(fixture.time_zone == "UTC")
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = try #require(TimeZone(identifier: fixture.time_zone))

        for c in fixture.cases {
            let now = try #require(JSON.parseDate(c.input.now), "\(c.name) now")
            let at = try #require(JSON.parseDate(c.input.at), "\(c.name) at")
            for lang in Self.languages {
                let expected = try #require(c.expected[lang], "\(c.name)/\(lang) expected")
                let actual = InboxNow.time(at, lang: lang, now: now, calendar: calendar)
                #expect(actual == expected, "\(c.name)/\(lang): \(actual)")
            }
        }
    }
}
