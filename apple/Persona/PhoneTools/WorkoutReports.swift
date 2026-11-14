#if os(iOS)
import Foundation
import HealthKit
import Observation
import os

/// Consent and the HealthKit cursor are committed together. Failed uploads leave the cursor in place for retry.
/// A report the hub rejects as invalid is dropped instead: the same data gets the same answer.
/// A build with HealthKit background delivery (`deliversInBackground`, #2526) also reports right after a workout is saved,
/// while Persona is closed: an observer query set up at launch wakes the app, which runs the same `refresh`.
@Observable
final class WorkoutReports {
    static let shared = WorkoutReports()
    private nonisolated static let log = Logger(subsystem: "persona", category: "workout-reports") // also from HealthKit's queue
    private let health = HKHealthStore()
    private let file: URL
    private var state: State
    private(set) var busy = false
    private(set) var error: String?
    var enabled: Bool { state.enabledAt != nil }
    /// HealthKit turned background delivery down although the build has it: reports wait for Persona to open.
    private(set) var backgroundRefused = false
    private var observer: HKObserverQuery?

    /// The build is signed with HealthKit background delivery (config.json app.health_background_delivery, Info.plist
    /// `PersonaHealthBackgroundDelivery`; docs/assistant/health.md).
    static var deliversInBackground: Bool {
        (Bundle.main.object(forInfoDictionaryKey: "PersonaHealthBackgroundDelivery") as? String)?.uppercased() == "YES"
    }

    /// Reports come right after a workout, not when Persona is next opened (what Settings says).
    var immediate: Bool { Self.deliversInBackground && !backgroundRefused }

    nonisolated struct State: Codable {
        var enabledAt: Date?
        var group: String = ""
        var anchor: Data?
        var pending: [Report] = []
    }
    typealias HeartRate = WorkoutHeartRate
    nonisolated struct Report: Codable {
        let id: String
        let title: String
        let activity: String
        let source: String
        let start: Date
        let end: Date
        let enabled_at: Date
        let duration: Double
        let distance: Double?
        let energy: Double?
        let heart_rate: [HeartRate]
        // Since #2526's second step; a report queued by an older version has none of them.
        var elevation_gain: Double? = nil
        var heart_rate_avg: Double? = nil
        var heart_rate_max: Double? = nil
        var splits: [WorkoutSplit]? = nil
    }

    init(folder: URL? = nil) {
        let root = folder ?? URL.applicationSupportDirectory.appending(path: "WorkoutReports")
        file = root.appending(path: "state.json")
        do {
            state = FileManager.default.fileExists(atPath: file.path)
                ? try JSON.decoder.decode(State.self, from: Data(contentsOf: file)) : State()
        } catch {
            state = State()
            self.error = String(localized: "Couldn't read workout report settings. Turn the feature on again to start fresh.")
        }
    }

    private func save(_ next: State) throws {
        try FileManager.default.createDirectory(at: file.deletingLastPathComponent(), withIntermediateDirectories: true)
        try JSON.encoder.encode(next).write(to: file, options: [.atomic, .completeFileProtection])
        state = next
    }

    func setEnabled(_ enabled: Bool, model: AppModel) async {
        guard !busy else { return }
        busy = true
        defer { busy = false }
        do {
            if enabled {
                guard let hub = model.hub, !hub.isDemo, HKHealthStore.isHealthDataAvailable() else { return }
                try await health.requestAuthorization(toShare: [], read: WorkoutReader.readTypes)
                try save(State(enabledAt: Date(), group: hub.group))
                observe()
            } else {
                try save(State())
                stopObserving()
            }
            error = nil
        } catch { self.error = error.localizedDescription }
    }

    /// Called at launch (`application(_:didFinishLaunchingWithOptions:)`, where HealthKit wants it for a background
    /// wake) and when reports are turned on: in a build with background delivery, a new workout wakes the app and runs
    /// `refresh`. HealthKit retries a wake whose completion is never called, so it is called whatever `refresh` did.
    func observe() {
        guard Self.deliversInBackground, enabled, observer == nil, HKHealthStore.isHealthDataAvailable() else { return }
        let query = HKObserverQuery(sampleType: HKObjectType.workoutType(), predicate: nil) { @Sendable _, completion, failure in
            let done = WakeDone(completion)
            if let failure { Self.log.error("workout observer failed: \(failure.localizedDescription, privacy: .public)") }
            Task { @MainActor in
                await WorkoutReports.shared.refresh(model: AppModel.shared)
                done.call()
            }
        }
        observer = query
        health.execute(query)
        health.enableBackgroundDelivery(for: HKObjectType.workoutType(), frequency: .immediate) { @Sendable ok, failure in
            if !ok { Self.log.error("background delivery refused: \(failure?.localizedDescription ?? "no reason", privacy: .public)") }
            Task { @MainActor in WorkoutReports.shared.backgroundRefused = !ok }
        }
    }

    private func stopObserving() {
        guard let observer else { return }
        health.stop(observer)
        self.observer = nil
        health.disableBackgroundDelivery(for: HKObjectType.workoutType()) { @Sendable _, _ in }
    }

    func refresh(model: AppModel) async {
        guard !busy, let since = state.enabledAt, let hub = model.hub,
              !hub.isDemo, hub.group == state.group else { return }
        busy = true
        defer { busy = false }
        do {
            // Upload a previous batch first: a network outage cannot grow an unbounded outbox.
            var skipped = try await upload(api: hub.api)
            await WorkoutReader.askForNewTypes() // allowed before splits were read: the distances too
            let anchor = try state.anchor.map {
                try NSKeyedUnarchiver.unarchivedObject(ofClass: HKQueryAnchor.self, from: $0)
            } ?? nil
            let (workouts, nextAnchor) = try await read(since: since, anchor: anchor)
            var next = state
            for workout in workouts where workout.startDate >= since && workout.duration >= 600 && workout.endDate <= Date() && workout.endDate.timeIntervalSince(workout.startDate) <= 7 * 86400 {
                let rates = try await WorkoutReader.heartRates(workout)
                let rate = WorkoutReader.heartRate(workout)
                let splits = (try? await WorkoutReader.splits(workout)) ?? [] // no splits rather than no report
                next.pending.append(Report(
                    id: workout.uuid.uuidString, title: String(localized: "Workout report"),
                    activity: WorkoutReader.activityName(workout.workoutActivityType), source: workout.sourceRevision.source.name,
                    start: workout.startDate, end: workout.endDate, enabled_at: since, duration: workout.duration,
                    distance: WorkoutReader.distance(workout), energy: WorkoutReader.energy(workout), heart_rate: rates,
                    elevation_gain: WorkoutReader.elevationGain(workout), heart_rate_avg: rate?.average,
                    heart_rate_max: rate?.maximum, splits: splits.isEmpty ? nil : splits))
            }
            next.anchor = try nextAnchor.map { try NSKeyedArchiver.archivedData(withRootObject: $0, requiringSecureCoding: true) }
            try save(next)
            skipped += try await upload(api: hub.api)
            error = skipped == 0 ? nil
                : String(localized: "Some workouts were skipped because the hub couldn't accept their data. Later workouts are still reported.")
        } catch { self.error = error.localizedDescription }
    }

    /// Sends the outbox in order and returns how many reports the hub rejected. A rejected report is dropped: kept at
    /// the head, it would hold back every later workout. Anything else (offline, 401 / 403 / 404, 5xx) stops the batch
    /// with the report still queued, for the next foreground visit.
    func upload(api: APIClient) async throws -> Int {
        var skipped = 0
        while let report = state.pending.first {
            do {
                let _: Data = try await api.post("/api/v1/assistant/health/workouts", body: report)
            } catch let error as APIError where [400, 413, 422].contains(error.status) {
                Self.log.error("hub rejected a workout report (HTTP \(error.status)): \(error.message, privacy: .public)")
                skipped += 1
            }
            var next = state
            next.pending.removeFirst()
            try save(next)
        }
        return skipped
    }

    /// HealthKit's completion for one background wake, carried to the main actor that calls it.
    private nonisolated final class WakeDone: @unchecked Sendable {
        private let completion: () -> Void
        init(_ completion: @escaping () -> Void) { self.completion = completion }
        func call() { completion() }
    }

    private func read(since: Date, anchor: HKQueryAnchor?) async throws -> ([HKWorkout], HKQueryAnchor?) {
        try await withCheckedThrowingContinuation { continuation in
            let predicate = HKQuery.predicateForSamples(withStart: since, end: nil, options: .strictStartDate)
            let query = HKAnchoredObjectQuery(type: HKObjectType.workoutType(), predicate: predicate, anchor: anchor, limit: 50) {
                _, samples, _, next, error in
                if let error { continuation.resume(throwing: error) }
                else { continuation.resume(returning: (samples as? [HKWorkout] ?? [], next)) }
            }
            health.execute(query)
        }
    }
}
#endif
