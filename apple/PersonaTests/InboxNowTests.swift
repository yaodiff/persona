import Foundation
import Testing
@testable import Persona

/// The iOS rendering of the manager's shared state-key and row-line contracts. Every case and language comes from the
/// same fixtures as the other clients; no expected sentence is duplicated in this test.
struct InboxNowTests {
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    private static let languages = ["en", "zh-Hans", "ja"]
    private static let referenceNow = Date(timeIntervalSince1970: 1_767_268_800) // 2026-01-01 12:00 UTC

    private struct FollowWords: Decodable { var who: String?; var next: String? }

    private struct NowFixture: Decodable {
        struct Case: Decodable {
            struct Input: Decodable {
                var item: InboxItem
                var followByLang: [String: FollowWords]?
                enum CodingKeys: String, CodingKey { case item; case followByLang = "follow_by_lang" }
            }
            struct Expected: Decodable { var text: [String: String]; var time: String? }
            var name: String
            var input: Input
            var expected: Expected
        }
        var cases: [Case]
    }

    private struct RowFixture: Decodable {
        struct Case: Decodable {
            struct Input: Decodable { var item: InboxItem }
            struct Expected: Decodable { var meta: [String: String]? }
            var name: String
            var input: Input
            var expected: Expected
        }
        var cases: [Case]
    }

    private static func fixture<T: Decodable>(_ name: String, as type: T.Type) throws -> T {
        let data = try Data(contentsOf: repo.appending(path: "spec/fixtures/inbox/" + name))
        return try JSON.decoder.decode(T.self, from: data)
    }

    private static var utc: Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        return calendar
    }

    @Test func everySharedStateCaseMatchesInEveryLanguage() throws {
        for c in try Self.fixture("now.json", as: NowFixture.self).cases {
            for lang in Self.languages {
                var item = c.input.item
                if let words = c.input.followByLang?[lang] {
                    if let who = words.who { item.follow?.who = who }
                    if let next = words.next { item.follow?.next = next }
                }
                var time: String?
                if let expectedTimestamp = c.expected.time {
                    let checkAt = try #require(item.follow?.checkAt, "\(c.name) has expected.time but no check_at")
                    #expect(checkAt.formatted(.iso8601) == expectedTimestamp, "\(c.name) timestamp")
                    time = InboxNow.time(checkAt, lang: lang, now: Self.referenceNow, calendar: Self.utc)
                }
                let expected = try #require(c.expected.text[lang], "\(c.name)/\(lang) expected text")
                    .replacingOccurrences(of: "{time}", with: time ?? "")
                let actual = InboxNow.text(item, lang: lang, time: time)
                #expect(actual == expected, "\(c.name)/\(lang): \(actual ?? "nil")")
            }
        }
    }

    @Test func everySharedRowCaseMatchesInEveryLanguage() throws {
        for c in try Self.fixture("row.json", as: RowFixture.self).cases {
            for lang in Self.languages {
                let actual = InboxNow.meta(c.input.item, lang: lang)
                #expect(actual == c.expected.meta?[lang], "\(c.name)/\(lang): \(actual ?? "nil")")
            }
        }
    }

    @Test func decodesTheNewFieldsAndLeavesAnOlderHubsFieldsNil() throws {
        let now = try Self.fixture("now.json", as: NowFixture.self)
        let fullCase = try #require(now.cases.first { $0.name == "waiting_full" })
        let full = fullCase.input.item
        #expect(full.where == "Launch group")
        #expect(full.now == "waiting")
        #expect(full.follow?.who == "Alex to confirm the date")
        #expect(full.follow?.next == "ask whether Friday works")
        #expect(full.follow?.checkAt?.formatted(.iso8601) == "2026-01-02T09:00:00Z")

        let row = try Self.fixture("row.json", as: RowFixture.self)
        let oldCase = try #require(row.cases.first { $0.name == "no_where_unchanged" })
        let old = oldCase.input.item
        #expect(old.where == nil && old.follow == nil && old.now == nil)
    }

    @Test func noKeyOrAnUnknownKeyKeepsTheAppsOwnStateWords() {
        var item = InboxItem(id: "matter", source: "chat", kind: "group", account: "chat", accountName: "Chat",
                             title: "A matter", preview: "Latest line", count: 1)
        #expect(InboxNow.text(item, lang: "en") == nil)
        item.now = "a_future_state"
        #expect(InboxNow.text(item, lang: "en") == nil)
    }

    @Test func followUpTimeMatchesWebInEveryLanguage() throws {
        struct Case {
            var timestamp: String
            var expected: [String: String]
        }
        let cases = [
            Case(timestamp: "2026-01-01T09:05:00Z", expected: [
                "en": "09:05", "zh-Hans": "09:05", "ja": "09:05",
            ]),
            Case(timestamp: "2026-01-02T09:05:00Z", expected: [
                "en": "tomorrow 09:05", "zh-Hans": "明天 09:05", "ja": "明日 09:05",
            ]),
            Case(timestamp: "2026-01-03T09:05:00Z", expected: [
                "en": "Jan 3 09:05", "zh-Hans": "1月3日 09:05", "ja": "1月3日 09:05",
            ]),
            Case(timestamp: "2027-01-02T09:05:00Z", expected: [
                "en": "Jan 2, 2027 09:05", "zh-Hans": "2027年1月2日 09:05", "ja": "2027年1月2日 09:05",
            ]),
        ]
        for c in cases {
            let checkAt = try #require(JSON.parseDate(c.timestamp))
            for lang in Self.languages {
                let expected = try #require(c.expected[lang])
                #expect(
                    InboxNow.time(checkAt, lang: lang, now: Self.referenceNow, calendar: Self.utc) == expected,
                    "\(c.timestamp)/\(lang)"
                )
            }
        }
    }

    @Test func followUpTimeMemoSeparatesBranchesForTheSameMoment() throws {
        let checkAt = try #require(JSON.parseDate("2026-01-02T09:05:00Z"))
        let later = try #require(JSON.parseDate("2026-01-03T12:00:00Z"))
        #expect(InboxNow.time(checkAt, lang: "en", now: later, calendar: Self.utc) == "Jan 2 09:05")
        #expect(InboxNow.time(checkAt, lang: "en", now: Self.referenceNow, calendar: Self.utc) == "tomorrow 09:05")
        let sameDay = try #require(JSON.parseDate("2026-01-02T08:00:00Z"))
        #expect(InboxNow.time(checkAt, lang: "en", now: sameDay, calendar: Self.utc) == "09:05")
        let anotherYear = try #require(JSON.parseDate("2025-01-01T12:00:00Z"))
        #expect(InboxNow.time(checkAt, lang: "en", now: anotherYear, calendar: Self.utc) == "Jan 2, 2026 09:05")
    }

    @MainActor @Test func searchIncludesWhereWithoutSearchingOtherItemText() {
        var item = InboxItem(id: "matter", source: "chat", kind: "group", account: "chat", accountName: "Chat",
                             title: "A matter", preview: "Latest line", count: 1)
        item.where = "Launch group"
        #expect(InboxProjection.Filter(query: "lAuNcH").matches(item))
        item.where = nil
        #expect(!InboxProjection.Filter(query: "lAuNcH").matches(item))
        item.context = "Launch group"
        item.reason = "Launch group"
        #expect(!InboxProjection.Filter(query: "Launch").matches(item))
    }
}
