import Foundation
#if os(iOS)
import UIKit
#endif

/// Whether HealthKit can be read right now. A locked phone answers HealthKit
/// collection queries with `HKError.errorDatabaseInaccessible`: the app must not
/// even start the queries then, and serve the on-disk snapshot instead.
nonisolated enum HealthDataLock {
    /// Indirected so tests can simulate a locked device without locking the Mac
    /// the tests run on. The app target defaults to MainActor, so the
    /// UIApplication access is wrapped in assumeIsolated: every production call
    /// site runs on the main actor, and tests replace the closure anyway.
    nonisolated(unsafe) static var isProtectedDataAvailable: @Sendable () -> Bool = {
        #if os(iOS)
        MainActor.assumeIsolated { UIApplication.shared.isProtectedDataAvailable }
        #else
        true
        #endif
    }
}

/// One persisted per-day health summary, the same granularity as
/// `phone_health_summary` gives live: day ("yyyy-MM-dd") -> metric -> value
/// (a number, or heart_rate's average/min/max object). Written while the app is
/// in the foreground and the device is unlocked, read at any time, including
/// while the phone is locked.
nonisolated struct HealthSnapshot: Codable, Sendable {
    var asOf: Date
    var windowDays: Int
    var metrics: [String]
    var rows: [String: [String: JSONValue]]

    /// Builds a `phone_health_summary`-shaped answer from the stored rows,
    /// restricted to the requested metrics and day window. Returns nil when the
    /// snapshot holds nothing inside the window (callers then say `device_locked`).
    func response(metrics requested: [String], days: Int, now: Date, calendar: Calendar = .current) -> JSONValue? {
        let end = now
        let start = calendar.startOfDay(for: calendar.date(byAdding: .day, value: -(max(days, 1) - 1), to: end) ?? end)
        let firstDay = ToolDate.day(start)
        let lastDay = ToolDate.day(end)

        var byDay: [String: [String: JSONValue]] = [:]
        for (day, metrics) in rows where day >= firstDay && day <= lastDay {
            var kept: [String: JSONValue] = [:]
            for metric in requested {
                if let value = metrics[metric] { kept[metric] = value }
            }
            if !kept.isEmpty { byDay[day] = kept }
        }
        if byDay.isEmpty { return nil }

        let rows: [JSONValue] = byDay.keys.sorted().map { day in
            var row: [String: JSONValue] = ["date": .string(day)]
            for (metric, value) in byDay[day] ?? [:] { row[metric] = value }
            return .object(row)
        }
        return .object([
            "start": .string(ToolDate.string(start)),
            "end": .string(ToolDate.string(end)),
            "days": .number(Double(days)),
            "metrics": .array(requested.map { .string($0) }),
            "data": .array(rows),
            "source": .string("snapshot"),
            "as_of": .string(ToolDate.string(asOf)),
        ])
    }
}

/// The store behind the locked-phone fallback. Everything goes through one actor
/// so a foreground refresh and a background tool call never write the file at
/// once. The file is protected `untilFirstUserAuthentication`: the system keeps
/// it readable after the first unlock, including while the phone is locked again.
nonisolated protocol HealthSnapshotStoring: Sendable {
    func load() async -> HealthSnapshot?
    func save(_ snapshot: HealthSnapshot) async
    /// Returns the current snapshot when a refresh ran less than the throttle interval ago.
    func cachedIfFresh(now: Date) async -> HealthSnapshot?
    func markRefreshed(at date: Date) async
}

actor HealthSnapshotStore: HealthSnapshotStoring {
    static let shared = HealthSnapshotStore()

    /// Foreground refreshes are cheap (a handful of collection queries) but must
    /// not run on every scene-phase change or every tool call.
    nonisolated static let refreshInterval: TimeInterval = 15 * 60

    /// How many days the persisted snapshot covers. The tool allows up to 90, so
    /// the snapshot can answer every window a caller may ask for.
    nonisolated static let windowDays = 90

    let directory: URL
    private let fileURL: URL
    private let files = FileManager.default
    private var lastRefresh: Date?
    private var cached: HealthSnapshot?

    init(directory: URL = URL.applicationSupportDirectory.appending(path: "health-snapshot", directoryHint: .isDirectory)) {
        self.directory = directory
        self.fileURL = directory.appending(path: "summary.json")
    }

    func load() -> HealthSnapshot? {
        if let cached { return cached }
        guard let data = files.contents(atPath: fileURL.path) else { return nil }
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        guard let snapshot = try? decoder.decode(HealthSnapshot.self, from: data) else { return nil }
        cached = snapshot
        return snapshot
    }

    func save(_ snapshot: HealthSnapshot) {
        do {
            if !files.fileExists(atPath: directory.path) {
                try files.createDirectory(at: directory, withIntermediateDirectories: true)
                // Readable while locked after the first unlock, like the Lark outbox stores.
                try files.setAttributes([.protectionKey: FileProtectionType.completeUntilFirstUserAuthentication],
                                        ofItemAtPath: directory.path)
            }
            let encoder = JSONEncoder()
            encoder.dateEncodingStrategy = .iso8601
            let data = try encoder.encode(snapshot)
            try data.write(to: fileURL, options: [.atomic, .completeFileProtectionUntilFirstUserAuthentication])
            cached = snapshot
        } catch {
            // A missing snapshot only makes the first locked call say device_locked; never fail the live tool over it.
        }
    }

    func cachedIfFresh(now: Date) -> HealthSnapshot? {
        guard let lastRefresh, now.timeIntervalSince(lastRefresh) < Self.refreshInterval else { return nil }
        return cached ?? load()
    }

    func markRefreshed(at date: Date) {
        lastRefresh = date
    }
}
