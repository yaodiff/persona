import Foundation
import Testing
@testable import Persona

/// Edges of the follow-up moment (`InboxNow.time`, R7 in the four-way comparison) that the fixture cases do not reach,
/// checked against the web app's `followTime` in `web/src/lib/inbox-now.ts` case by case: a tomorrow across New Year,
/// a past day outside the year, the calendar's zone (not the device's) deciding the day, the DST days, and midnight.
struct InboxNowTimeWebParityTests {
    private static let languages = ["en", "zh-Hans", "ja"]

    private static func calendar(_ zone: String) throws -> Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = try #require(TimeZone(identifier: zone))
        return calendar
    }

    private static func time(_ checkAt: String, now: String, lang: String, zone: String = "UTC") throws -> String {
        InboxNow.time(try #require(JSON.parseDate(checkAt)), lang: lang,
                      now: try #require(JSON.parseDate(now)), calendar: try Self.calendar(zone))
    }

    /// New Year's Eve evening: the next day is still "tomorrow", and it carries no year, as `followTime` returns it.
    @Test func tomorrowAcrossNewYearKeepsTheWordAndNoYear() throws {
        let words = ["en": "tomorrow 09:05", "zh-Hans": "明天 09:05", "ja": "明日 09:05"]
        for lang in Self.languages {
            let expected = try #require(words[lang])
            let actual = try Self.time("2026-01-01T09:05:00Z", now: "2025-12-31T12:00:00Z", lang: lang)
            #expect(actual == expected, "\(lang): \(actual)")
        }
    }

    /// Midnight exactly: the first minute of the next day is tomorrow, and the last minute of today is today's clock.
    @Test func midnightBelongsToTomorrow() throws {
        #expect(try Self.time("2026-01-02T00:00:00Z", now: "2026-01-01T23:59:00Z", lang: "en") == "tomorrow 00:00")
        #expect(try Self.time("2026-01-01T23:59:00Z", now: "2026-01-01T23:59:00Z", lang: "en") == "23:59")
        #expect(try Self.time("2026-01-01T23:59:00Z", now: "2026-01-01T12:00:00Z", lang: "en") == "23:59")
    }

    /// A moment that already passed (a stale hub view) still reads as a date, with its year when the year differs.
    @Test func aPastDayReadsAsADateWithItsYear() throws {
        let words = ["en": "Dec 31, 2025 09:05", "zh-Hans": "2025年12月31日 09:05", "ja": "2025年12月31日 09:05"]
        for lang in Self.languages {
            let expected = try #require(words[lang])
            let actual = try Self.time("2025-12-31T09:05:00Z", now: "2026-01-01T12:00:00Z", lang: lang)
            #expect(actual == expected, "\(lang): \(actual)")
        }
    }

    /// The calendar's zone decides the day: one instant is today's clock in UTC and tomorrow's in UTC+8. The memo must
    /// not hand the second answer back for the first.
    @Test func theCalendarsZoneDecidesTheDay() throws {
        let instant = "2026-01-01T16:00:00Z"
        let now = "2026-01-01T12:00:00Z"
        #expect(try Self.time(instant, now: now, lang: "zh-Hans", zone: "UTC") == "16:00")
        #expect(try Self.time(instant, now: now, lang: "zh-Hans", zone: "Asia/Shanghai") == "明天 00:00")
        #expect(try Self.time(instant, now: now, lang: "en", zone: "Asia/Shanghai") == "tomorrow 00:00")
        // And the first answer is still there after the second, not overwritten.
        #expect(try Self.time(instant, now: now, lang: "zh-Hans", zone: "UTC") == "16:00")
    }

    /// Days are calendar days, not 24-hour spans: a spring-forward day (23 h) and a fall-back day (25 h) both leave the
    /// next local morning as tomorrow.
    @Test func daylightSavingDaysStillCountAsOneDay() throws {
        let spring = try Self.time("2026-03-09T13:05:00Z", now: "2026-03-08T12:00:00Z",
                                  lang: "en", zone: "America/New_York")
        #expect(spring == "tomorrow 09:05")
        let fall = try Self.time("2026-11-02T14:05:00Z", now: "2026-11-01T12:00:00Z",
                                lang: "en", zone: "America/New_York")
        #expect(fall == "tomorrow 09:05")
    }

    /// The memo is keyed by branch and language too: the same moment is today's clock, tomorrow's word and a dated line
    /// depending on the clock handed in, and the three never come back as each other.
    @Test func theMemoKeepsTheBranchesAndLanguagesApart() throws {
        let checkAt = try #require(JSON.parseDate("2026-01-02T09:05:00Z"))
        let today = try #require(JSON.parseDate("2026-01-02T08:00:00Z"))
        let tomorrow = try #require(JSON.parseDate("2026-01-01T12:00:00Z"))
        let later = try #require(JSON.parseDate("2026-01-03T12:00:00Z"))
        let calendar = try Self.calendar("UTC")
        #expect(InboxNow.time(checkAt, lang: "en", now: tomorrow, calendar: calendar) == "tomorrow 09:05")
        #expect(InboxNow.time(checkAt, lang: "zh-Hans", now: tomorrow, calendar: calendar) == "明天 09:05")
        #expect(InboxNow.time(checkAt, lang: "en", now: today, calendar: calendar) == "09:05")
        #expect(InboxNow.time(checkAt, lang: "en", now: later, calendar: calendar) == "Jan 2 09:05")
        #expect(InboxNow.time(checkAt, lang: "en", now: tomorrow, calendar: calendar) == "tomorrow 09:05")
    }
}
