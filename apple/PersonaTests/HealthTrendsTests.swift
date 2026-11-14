import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Health Trends (#3272): the phone's per-day steps, sleep and weight become the ```persona-card a health answer would draw.
@MainActor
struct HealthTrendsTests {
    private static var utc: Calendar {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(secondsFromGMT: 0)!
        return c
    }

    // 2026-11-05 is a Thursday.
    private static let now = ISO8601DateFormatter().date(from: "2026-11-05T15:00:00Z")!

    @Test func daysEndTodayOldestFirst() {
        let days = HealthTrends.dayKeys(7, now: Self.now, calendar: Self.utc)
        #expect(days == ["2026-10-30", "2026-10-31", "2026-11-01", "2026-11-02", "2026-11-03", "2026-11-04", "2026-11-05"])
        #expect(HealthTrends.dayKeys(30, now: Self.now, calendar: Self.utc).count == 30)
        #expect(HealthTrends.dayKeys(0, now: Self.now, calendar: Self.utc) == ["2026-11-05"])
    }

    @Test func labels() {
        #expect(HealthTrends.label("2026-11-05", range: .week) == "周四")
        #expect(HealthTrends.label("2026-11-01", range: .week) == "周日")
        #expect(HealthTrends.label("2026-11-05", range: .month) == "11/5")
        #expect(HealthTrends.label("bad", range: .week) == "bad")
    }

    @Test func stepsAreABarPerDayWithMissingDaysAtZero() throws {
        let days = HealthTrends.dayKeys(7, now: Self.now, calendar: Self.utc)
        let card = try #require(HealthTrends.card(.steps, values: ["2026-11-03": 8000, "2026-11-05": 4000], days: days, range: .week))
        #expect(card.type == ExtensionPersonaCardKind.bar && card.unit == "步" && card.value == 4000)
        #expect(card.points?.count == 7 && card.points?.first?.value == 0 && card.points?.last?.label == "周四")
        #expect(card.note == "日均 6,000 步（2 天有记录）")
        #expect(card.view() != nil, "draws, not a code block")
    }

    @Test func sleepIsALineThroughTheDaysWithData() throws {
        let days = HealthTrends.dayKeys(7, now: Self.now, calendar: Self.utc)
        let card = try #require(HealthTrends.card(.sleep, values: ["2026-11-01": 7, "2026-11-04": 6.5, "2026-11-05": 8], days: days, range: .week))
        #expect(card.type == ExtensionPersonaCardKind.line && card.points?.map(\.label) == ["周日", "周三", "周四"])
        #expect(card.note == "日均 7.2 小时（3 天有记录）")
        #expect(card.view() != nil)
    }

    @Test func weightSaysHowMuchItMoved() throws {
        let days = HealthTrends.dayKeys(30, now: Self.now, calendar: Self.utc)
        let down = try #require(HealthTrends.card(.weight, values: ["2026-10-10": 70.2, "2026-11-05": 69.4], days: days, range: .month))
        #expect(down.note == "期间变化 -0.8 千克" && down.points?.map(\.label) == ["10/10", "11/5"])
        let up = try #require(HealthTrends.card(.weight, values: ["2026-10-10": 69, "2026-11-05": 69.5], days: days, range: .month))
        #expect(up.note == "期间变化 +0.5 千克")
    }

    @Test func oneDayIsANumberAndNoneIsNoCard() throws {
        let days = HealthTrends.dayKeys(7, now: Self.now, calendar: Self.utc)
        let one = try #require(HealthTrends.card(.weight, values: ["2026-11-02": 68.4], days: days, range: .week))
        #expect(one.type == ExtensionPersonaCardKind.stat && one.value == 68.4 && one.note == "周一的记录")
        #expect(one.view() != nil)
        #expect(HealthTrends.card(.sleep, values: [:], days: days, range: .week) == nil)
        #expect(HealthTrends.card(.steps, values: ["2026-01-01": 100], days: days, range: .week) == nil, "only days in range count")
    }

    @Test func modelLeavesOutWhatFailsAndAsksWhenNeverAsked() async {
        var asked = false
        let today = HealthTrends.dayKeys(1, now: Self.now).last!
        let deps = HealthTrendsModel.Deps(
            access: { asked ? .granted : .notDetermined }, requestAccess: { asked = true; return true },
            daily: { metric, _ in
                if metric == .weight { throw ToolFailure(.denied, "no") }
                return metric == .steps ? [today: 1234] : [:]
            },
            now: { Self.now })
        let model = HealthTrendsModel(deps: deps)
        await model.load()
        #expect(model.state == .needsAccess)
        await model.requestAccess()
        guard case .loaded(let cards) = model.state else { Issue.record("not loaded: \(model.state)"); return }
        #expect(Set(cards.keys) == [.steps])
    }

    @Test func aDeviceWithoutHealthSaysSo() async {
        let model = HealthTrendsModel(deps: .init(access: { .notNeeded }, requestAccess: { false },
                                                  daily: { _, _ in Issue.record("read on a device without Health"); return [:] },
                                                  now: { Self.now }))
        await model.load()
        #expect(model.state == .unavailable)
    }

    /// The summary keys days in Gregorian whatever the region's calendar is; the trends must match those keys.
    @Test func keysAreGregorianOnAnyCalendar() {
        var buddhist = Calendar(identifier: .buddhist)
        buddhist.timeZone = TimeZone(secondsFromGMT: 0)!
        #expect(HealthTrends.dayKeys(1, now: Self.now, calendar: buddhist) == ["2026-11-05"])
    }
}
