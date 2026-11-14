import Foundation
import HealthKit
import Testing
@testable import Persona

/// The locked-phone health snapshot: what gets written while the device is
/// unlocked, and what `phone_health_summary` answers while it is locked.
@MainActor
struct HealthSnapshotTests {
    /// In-memory store so the locked paths need neither HealthKit nor the real
    /// Application Support directory.
    actor FakeHealthSnapshotStore: HealthSnapshotStoring {
        var snapshot: HealthSnapshot?
        var fresh: HealthSnapshot?
        private(set) var saved: [HealthSnapshot] = []

        func load() -> HealthSnapshot? { snapshot }
        func save(_ snapshot: HealthSnapshot) {
            saved.append(snapshot)
            self.snapshot = snapshot
        }
        func cachedIfFresh(now: Date) -> HealthSnapshot? { fresh }
        func markRefreshed(at date: Date) {}
    }

    private let healthAskedKey = "phoneAuth.health.asked"

    private func grantHealthInSettings() {
        // HealthAccess.requireAccess only reads this cached flag, so it is safe to flip in a unit test.
        UserDefaults.standard.set(true, forKey: healthAskedKey)
    }

    private func decode(_ content: [ToolContent]) -> JSONValue? {
        guard let text = content.first?.text, let data = text.data(using: .utf8) else { return nil }
        return try? JSONDecoder().decode(JSONValue.self, from: data)
    }

    private func makeSnapshot(asOf: Date, calendar: Calendar = .current) -> HealthSnapshot {
        let today = ToolDate.day(asOf)
        let yesterday = ToolDate.day(calendar.date(byAdding: .day, value: -1, to: asOf) ?? asOf)
        let old = ToolDate.day(calendar.date(byAdding: .day, value: -30, to: asOf) ?? asOf)
        return HealthSnapshot(
            asOf: asOf,
            windowDays: 90,
            metrics: ["steps", "sleep", "heart_rate"],
            rows: [
                today: [
                    "steps": .number(1200),
                    "heart_rate": .object([
                        "average": .number(72),
                        "min": .number(58),
                        "max": .number(110),
                    ]),
                ],
                yesterday: ["steps": .number(3400), "sleep": .number(7.5)],
                old: ["steps": .number(9000)],
            ]
        )
    }

    // MARK: - Snapshot response shaping

    @Test func responseKeepsTheSameFieldsAndAddsSourceAndAsOf() throws {
        let now = Date(timeIntervalSince1970: 1_760_000_000)
        let answer = try #require(makeSnapshot(asOf: now).response(metrics: ["steps"], days: 7, now: now))
        guard case .object(let object) = answer else { Issue.record("answer is not an object"); return }
        #expect(object["source"] == .string("snapshot"))
        #expect(object["as_of"] == .string(ToolDate.string(now)))
        #expect(object["days"] == .number(7))
        #expect(object["metrics"] == .array([.string("steps")]))
        guard case .array(let data) = object["data"] else { Issue.record("no data rows"); return }
        #expect(data.count == 2) // today and yesterday are inside the 7-day window; the 30-day-old row is not
        for case .object(let row) in data {
            #expect(row["steps"] != nil, "only the requested metric is carried into a row")
            #expect(row["sleep"] == nil && row["heart_rate"] == nil)
        }
    }

    @Test func responsePassesHeartRateObjectsThrough() throws {
        let now = Date(timeIntervalSince1970: 1_760_000_000)
        let answer = try #require(makeSnapshot(asOf: now).response(metrics: ["heart_rate"], days: 7, now: now))
        guard case .object(let object) = answer, case .array(let data) = object["data"],
              case .object(let row)? = data.first, case .object(let heartRate)? = row["heart_rate"] else {
            Issue.record("heart_rate row missing")
            return
        }
        #expect(heartRate["average"] == .number(72))
        #expect(heartRate["min"] == .number(58))
        #expect(heartRate["max"] == .number(110))
    }

    @Test func responseIsNilWhenNothingCoversTheWindow() {
        let now = Date(timeIntervalSince1970: 1_760_000_000)
        let calendar = Calendar(identifier: .gregorian)
        let snapshot = HealthSnapshot(asOf: now, windowDays: 90, metrics: ["steps"], rows: [
            ToolDate.day(calendar.date(byAdding: .day, value: -30, to: now) ?? now): ["steps": .number(10)],
        ])
        #expect(snapshot.response(metrics: ["steps"], days: 7, now: now, calendar: calendar) == nil)
    }

    // MARK: - Locked phone paths

    @Test func lockedPhoneServesTheSnapshotWithoutTouchingHealthKit() async throws {
        let wasAsked = UserDefaults.standard.bool(forKey: healthAskedKey)
        defer { UserDefaults.standard.set(wasAsked, forKey: healthAskedKey) }
        grantHealthInSettings()
        let now = Date()
        let store = FakeHealthSnapshotStore()
        await store.set(snapshot: makeSnapshot(asOf: now))

        let content = try await HealthTool.summary(
            .object(["metrics": .array([.string("steps")]), "days": .number(7)]),
            snapshotStore: store,
            isDataAvailable: { false }
        )
        let answer = try #require(decode(content))
        guard case .object(let object) = answer else { Issue.record("answer is not an object"); return }
        #expect(object["source"] == .string("snapshot"))
        #expect(object["as_of"] == .string(ToolDate.string(now)))
    }

    @Test func lockedPhoneWithoutSnapshotSaysDeviceLocked() async {
        let wasAsked = UserDefaults.standard.bool(forKey: healthAskedKey)
        defer { UserDefaults.standard.set(wasAsked, forKey: healthAskedKey) }
        grantHealthInSettings()
        await #expect(throws: ToolFailure.self) {
            _ = try await HealthTool.summary(
                .object(["days": .number(7)]),
                snapshotStore: FakeHealthSnapshotStore(),
                isDataAvailable: { false }
            )
        }
        do {
            _ = try await HealthTool.summary(
                .object(["days": .number(7)]),
                snapshotStore: FakeHealthSnapshotStore(),
                isDataAvailable: { false }
            )
        } catch {
            let text = ToolErrors.describe(error)
            #expect(text.contains("device_locked"))
            #expect(!text.contains("Protected health data is inaccessible"), "the raw English HKError must not reach the fields")
        }
    }

    @Test func databaseInaccessibleIsRecognized() {
        #expect(HealthTool.isDatabaseInaccessible(HKError(.errorDatabaseInaccessible)))
        #expect(!HealthTool.isDatabaseInaccessible(HKError(.errorInvalidArgument)))
        #expect(!HealthTool.isDatabaseInaccessible(ToolFailure(.unavailable, "other")))
    }

    // MARK: - On-disk store

    @Test func storeRoundTripsWithALockSafeProtectionLevel() async throws {
        let directory = FileManager.default.temporaryDirectory
            .appending(path: "health-snapshot-\(UUID().uuidString)", directoryHint: .isDirectory)
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = HealthSnapshotStore(directory: directory)
        let now = Date(timeIntervalSince1970: 1_760_000_000)
        let snapshot = makeSnapshot(asOf: now)

        await store.save(snapshot)
        let loaded = try #require(await store.load())
        #expect(loaded.asOf == snapshot.asOf)
        #expect(loaded.rows == snapshot.rows)

        // The simulator does not implement data protection: attributesOfItem never reports
        // .protectionKey there, so the level can only be verified on a physical device. The
        // round-trip above still proves the file is written and read back on every platform.
        #if !targetEnvironment(simulator)
        let files = FileManager.default
        let file = directory.appending(path: "summary.json")
        let fileProtection = try files.attributesOfItem(atPath: file.path)[.protectionKey] as? FileProtectionType
        let dirProtection = try files.attributesOfItem(atPath: directory.path)[.protectionKey] as? FileProtectionType
        #expect(fileProtection == .completeUntilFirstUserAuthentication)
        #expect(dirProtection == .completeUntilFirstUserAuthentication)
        #endif
    }

    @Test func freshSnapshotSkipsARefreshAndAgedOneDoesNot() async throws {
        let directory = FileManager.default.temporaryDirectory
            .appending(path: "health-snapshot-\(UUID().uuidString)", directoryHint: .isDirectory)
        defer { try? FileManager.default.removeItem(at: directory) }
        let store = HealthSnapshotStore(directory: directory)
        let now = Date()
        let snapshot = makeSnapshot(asOf: now)
        await store.save(snapshot)

        await store.markRefreshed(at: now.addingTimeInterval(-60))
        #expect(await store.cachedIfFresh(now: now)?.asOf == snapshot.asOf)

        await store.markRefreshed(at: now.addingTimeInterval(-HealthSnapshotStore.refreshInterval - 60))
        #expect(await store.cachedIfFresh(now: now) == nil)
    }
}

private extension HealthSnapshotTests.FakeHealthSnapshotStore {
    func set(snapshot: HealthSnapshot) { self.snapshot = snapshot }
}
