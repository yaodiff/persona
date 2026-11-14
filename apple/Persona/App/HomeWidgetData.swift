#if os(iOS)
import EventKit
import Foundation

/// What the widgets show that the app reads on its own rather than from the hub's live state: today's calendar events,
/// Health's numbers, the subscription windows and today's tokens by the hour (docs/apple/home-widgets.md「What each
/// number counts」). Read only for the kinds on a screen (`installed`), at most every `every` a kind, off the main
/// actor; the writer takes what is here when it builds the snapshot, and a read that brought something new asks it for
/// a write.
@MainActor
final class HomeWidgetData {
    static let shared = HomeWidgetData()

    /// The kinds on this device's screens (`WidgetCenter`'s configurations, read from the foreground); nil before the
    /// first read, when every kind counts.
    var installed: Set<HomeKind>?

    /// The rest of today's events from the phone's calendar; nil when Persona may not read it.
    private(set) var events: [HomeSnapshot.Event]?
    /// The days around today from the same read, for the Calendar widget's week and month (`HomeDays`).
    private(set) var days: HomeSnapshot.Days?
    private(set) var health: HomeSnapshot.Health?
    /// Today's tokens by the hour (`GET /api/v1/usage`'s `hourly`, #5721); empty from an older hub.
    private(set) var hourly: [Int] = []

    /// Today's money at the gateways that report it (`HomeUsage.spentToday`), with the day it was read on.
    private var spent: (day: String, usd: Double?)?

    private var readAt: [HomeKind: Date] = [:]
    static let every: [HomeKind: TimeInterval] = [.today: 10 * 60, .health: 15 * 60, .usage: 5 * 60]
    /// The gateways' money: a read asks the hub for every gateway launcher's quota.
    static let spentEvery: TimeInterval = 15 * 60
    private var spentReadAt = Date.distantPast

    /// Today's money, nil when no gateway reports it or it was read on another day.
    func spentToday(_ now: Date) -> Double? { spent?.day == Self.dayKey(now) ? spent?.usd : nil }

    /// Reads what is due (all of it with `force`: the hub woke the app for the widgets, or they were just added).
    func refresh(force: Bool = false, now: Date = .now) {
        func due(_ kind: HomeKind, also: HomeKind? = nil) -> Bool {
            let shown = installed.map { $0.contains(kind) || also.map($0.contains) == true } ?? true
            return shown && (force || now.timeIntervalSince(readAt[kind] ?? .distantPast) >= Self.every[kind] ?? 600)
        }
        if due(.today, also: .record) {
            readAt[.today] = now
            Task { await readEvents() }
        }
        if due(.health) {
            readAt[.health] = now
            Task { await readHealth() }
        }
        // Never sooner, even when the hub wakes the app: a Needs You change can wake it every few seconds.
        let usageShown = installed.map { $0.contains(.usage) } ?? true
        if usageShown, now.timeIntervalSince(spentReadAt) >= Self.spentEvery {
            spentReadAt = now
            Task { await readSpent() }
        }
        if due(.usage) {
            readAt[.usage] = now
            Task { await readUsage() }
        }
    }

    private func readEvents() async {
        let read = await Task.detached { Self.calendarEvents(.now) }.value
        guard read?.today != events || read?.days != days else { return }
        events = read?.today
        days = read?.days
        HomeWidgetWriter.shared.schedule()
    }

    /// The events the user has not declined, with a title, over the days the Calendar widget can show
    /// (`HomeDays.range`), in one read: the rest of today (`today`) and every day's (`days`); nil without full access
    /// (the widget then takes the hub's meetings, or says how to allow the calendar). EventKit reads the store: never on
    /// the main actor.
    nonisolated static func calendarEvents(_ now: Date) -> (today: [HomeSnapshot.Event], days: HomeSnapshot.Days)? {
        guard MeetingCalendar.canRead else { return nil }
        let calendar = Calendar.current
        let range = HomeDays.range(around: now, calendar: calendar)
        let store = EKEventStore()
        let found = store.events(matching: store.predicateForEvents(withStart: range.start, end: range.end, calendars: nil))
            .map(MeetingCalendar.Candidate.init)
            .filter { !$0.declined && !$0.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
            .map { HomeSnapshot.Event(title: $0.title, start: $0.start.timeIntervalSince1970, end: $0.end.timeIntervalSince1970, allDay: $0.allDay) }
        return (HomeToday.rest(found, now: now, calendar: calendar), HomeDays.build(found, range: range, calendar: calendar))
    }

    /// Today's steps against the week before, last night's sleep, the resting heart rate and active energy, when the
    /// user let Persona read Health (`HealthAccess`). A query that fails (Health is locked with the phone) keeps the last.
    private func readHealth() async {
        guard HealthAccess.cachedStatus() == .granted else {
            if health != nil { health = nil; HomeWidgetWriter.shared.schedule() }
            return
        }
        let calendar = Calendar.current
        let days = (0..<8).compactMap { calendar.date(byAdding: .day, value: -$0, to: .now) }.reversed().map(Self.dayKey)
        guard let steps = try? await HealthTool.daily("steps", days: 8) else { return }
        let sleep = (try? await HealthTool.daily("sleep", days: 2)) ?? [:]
        let heart = (try? await HealthTool.daily("resting_heart_rate", days: 1)) ?? [:]
        let energy = (try? await HealthTool.daily("active_energy", days: 1)) ?? [:]
        let today = days.last ?? ""
        let before = days.dropLast().compactMap { steps[$0] }
        var h = HomeSnapshot.Health()
        h.steps = steps[today].map { Int($0) } ?? 0
        h.stepsAverage = before.isEmpty ? nil : Int(before.reduce(0, +) / Double(before.count))
        h.sleepHours = sleep[today] ?? sleep[days.dropLast().last ?? ""]
        h.restingHeartRate = heart[today].map { Int($0.rounded()) }
        h.activeEnergy = energy[today].map { Int($0.rounded()) }
        h.week = days.suffix(7).map { Int(steps[$0] ?? 0) }
        guard h != health else { return }
        health = h
        HomeWidgetWriter.shared.schedule()
    }

    /// The day keys `HealthTool.daily` answers with ("yyyy-MM-dd" in the phone's zone).
    static func dayKey(_ date: Date) -> String { ToolDate.day(date) }

    /// The subscription windows (as the Machines page reads them) and today's tokens by the hour.
    private func readUsage() async {
        let model = AppModel.shared
        await UsageWindowsStore.shared.loadIfOld(model)
        if let api = model.api, let hours = try? await api.usageHourly() { hourly = hours }
        HomeWidgetWriter.shared.schedule()
    }

    /// Today's money: every launcher with a gateway, read through the hub (which keeps an answer a minute), the reads at
    /// once. When the list or every read fails, the last stays.
    private func readSpent() async {
        guard let api = AppModel.shared.api, let launchers = try? await api.allLaunchers() else { return }
        let names = HomeUsage.gatewayLaunchers(launchers)
        let answers = await withTaskGroup(of: LauncherQuotaAnswer?.self) { group in
            for name in names { group.addTask { try? await api.launcherQuota(name) } }
            var all: [LauncherQuotaAnswer] = []
            for await answer in group { if let answer { all.append(answer) } }
            return all
        }
        if !names.isEmpty, answers.isEmpty { return }
        let now = Date.now
        let read = (day: Self.dayKey(now), usd: HomeUsage.spentToday(answers))
        guard read.day != spent?.day || read.usd != spent?.usd else { return }
        spent = read
        HomeWidgetWriter.shared.schedule()
    }
}
#endif
