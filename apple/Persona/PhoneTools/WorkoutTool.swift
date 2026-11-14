#if os(iOS) || os(visionOS)
import Foundation
import HealthKit

/// The time one whole kilometre took, the first being kilometre 1 (the hub's `WorkoutSplit`, #2526).
nonisolated struct WorkoutSplit: Codable, Equatable, Sendable {
    let km: Int
    let seconds: Double
}

/// A minute of a workout and its average heart rate.
nonisolated struct WorkoutHeartRate: Codable, Equatable, Sendable {
    let minute: Int
    let bpm: Double
}

/// Per-kilometre splits from a workout's distance samples (#2526). Each sample's distance is spread evenly over its own
/// interval, and the time a pause covers is left out, as the Workout app counts it.
nonisolated enum WorkoutSplits {
    nonisolated struct Sample {
        let start: Date
        let end: Date
        let meters: Double
    }

    /// At most this many: the hub refuses more (`workoutMaxSplits`).
    static let limit = 1000

    /// Whole kilometres in order; the last part kilometre is left out. Samples that cannot give a sensible split (one
    /// with no time, one outside the workout) leave no splits at all: the hub refuses a whole report over one bad split,
    /// and a wrong pace is worse than none.
    static func of(_ samples: [Sample], start: Date, end: Date, paused: [DateInterval]) -> [WorkoutSplit] {
        var splits: [WorkoutSplit] = []
        var covered = 0.0
        var mark = start
        for s in samples.sorted(by: { $0.start < $1.start }) where s.meters > 0 {
            guard s.start >= start, s.end >= s.start, s.end <= end else { return [] }
            let before = covered
            covered += s.meters
            while splits.count < limit, covered >= Double(splits.count + 1) * 1000 {
                let share = (Double(splits.count + 1) * 1000 - before) / s.meters
                let at = max(mark, s.start.addingTimeInterval(s.end.timeIntervalSince(s.start) * share))
                let seconds = (active(from: mark, to: at, paused: paused) * 10).rounded() / 10
                guard seconds >= 1 else { return [] }
                splits.append(WorkoutSplit(km: splits.count + 1, seconds: seconds))
                mark = at
            }
        }
        return splits
    }

    /// The time from `a` to `b` outside the pauses.
    static func active(from a: Date, to b: Date, paused: [DateInterval]) -> TimeInterval {
        guard b > a else { return 0 }
        var time = b.timeIntervalSince(a)
        for p in paused {
            let lo = max(a, p.start), hi = min(b, p.end)
            if hi > lo { time -= hi.timeIntervalSince(lo) }
        }
        return max(time, 0)
    }

    /// The workout's pauses, the user's own and the automatic ones, from its events; one still open runs to the end.
    static func pauses(_ events: [(type: HKWorkoutEventType, at: Date)], end: Date) -> [DateInterval] {
        var out: [DateInterval] = []
        var since: Date?
        for event in events.sorted(by: { $0.at < $1.at }) {
            switch event.type {
            case .pause, .motionPaused:
                if since == nil { since = event.at }
            case .resume, .motionResumed:
                if let from = since, event.at > from { out.append(DateInterval(start: from, end: event.at)) }
                since = nil
            default:
                break
            }
        }
        if let from = since, end > from { out.append(DateInterval(start: from, end: end)) }
        return out
    }
}

/// Reads workouts and their measurements from HealthKit: what a workout report sends (`WorkoutReports`) and what the
/// workout tools answer (#2526). No route or location is ever read.
enum WorkoutReader {
    private static let store = HKHealthStore()

    /// What the reports and the tools read.
    static var readTypes: Set<HKObjectType> {
        [HKObjectType.workoutType(), HKQuantityType(.heartRate), HKQuantityType(.distanceWalkingRunning),
         HKQuantityType(.distanceCycling), HKQuantityType(.activeEnergyBurned)]
    }

    /// The activities the tools filter by, by the name a report also uses for them (lowercased, `_` for spaces).
    static let activities: [String: [HKWorkoutActivityType]] = [
        "running": [.running],
        "walking": [.walking],
        "cycling": [.cycling],
        "swimming": [.swimming],
        "hiking": [.hiking],
        "yoga": [.yoga],
        "strength_training": [.traditionalStrengthTraining, .functionalStrengthTraining],
        "interval_training": [.highIntensityIntervalTraining],
    ]

    static func activityName(_ activity: HKWorkoutActivityType) -> String {
        switch activity {
        case .running: "Running"
        case .walking: "Walking"
        case .cycling: "Cycling"
        case .swimming: "Swimming"
        case .hiking: "Hiking"
        case .yoga: "Yoga"
        case .traditionalStrengthTraining, .functionalStrengthTraining: "Strength training"
        case .highIntensityIntervalTraining: "Interval training"
        default: "Workout (HealthKit activity \(activity.rawValue))"
        }
    }

    /// The distance whole kilometres are measured in: on foot or on a bike; none for the rest (a pool's laps are not
    /// kilometres).
    private static func distanceType(_ activity: HKWorkoutActivityType) -> HKQuantityType? {
        switch activity {
        case .running, .walking, .hiking: HKQuantityType(.distanceWalkingRunning)
        case .cycling: HKQuantityType(.distanceCycling)
        default: nil
        }
    }

    /// Asks Health again when it hasn't been asked for every type read here (allowed before workouts were read), and
    /// only while Persona is in front, where the sheet can show. Health never says what was allowed: a refused type
    /// reads as no data.
    static func askForNewTypes() async {
        #if os(iOS)
        guard Foreground.isActive,
              (try? await store.statusForAuthorizationRequest(toShare: [], read: readTypes)) == .shouldRequest else { return }
        _ = try? await store.requestAuthorization(toShare: [], read: readTypes)
        #endif
    }

    /// Workouts that started in the last `days` days, newest first, of `activities` if given.
    static func recent(days: Int, activities: [HKWorkoutActivityType]?, limit: Int) async throws -> [HKWorkout] {
        let since = Calendar.current.date(byAdding: .day, value: -days, to: Date()) ?? Date()
        let found = try await workouts(HKQuery.predicateForSamples(withStart: since, end: nil, options: []), limit: 500)
        return Array(found.filter { activities?.contains($0.workoutActivityType) ?? true }.prefix(limit))
    }

    static func workout(id: UUID) async throws -> HKWorkout? {
        try await workouts(HKQuery.predicateForObject(with: id), limit: 1).first
    }

    private static func workouts(_ predicate: NSPredicate, limit: Int) async throws -> [HKWorkout] {
        try await withCheckedThrowingContinuation { continuation in
            let query = HKSampleQuery(sampleType: HKObjectType.workoutType(), predicate: predicate, limit: limit,
                                      sortDescriptors: [NSSortDescriptor(key: HKSampleSortIdentifierStartDate, ascending: false)]) {
                _, samples, error in
                if let error { continuation.resume(throwing: error) }
                else { continuation.resume(returning: samples as? [HKWorkout] ?? []) }
            }
            store.execute(query)
        }
    }

    /// Meters climbed, from the workout's own metadata (the Workout app records it outdoors); nil when absent or absurd.
    static func elevationGain(_ workout: HKWorkout) -> Double? {
        guard let gain = (workout.metadata?[HKMetadataKeyElevationAscended] as? HKQuantity)?.doubleValue(for: .meter()),
              gain.isFinite, gain >= 0, gain <= 20000 else { return nil }
        return gain.rounded()
    }

    /// The heart rate's average and maximum over the workout, in bpm; nil for both unless they make sense together.
    static func heartRate(_ workout: HKWorkout) -> (average: Double, maximum: Double)? {
        let unit = HKUnit.count().unitDivided(by: .minute())
        let stats = workout.statistics(for: HKQuantityType(.heartRate))
        guard let average = stats?.averageQuantity()?.doubleValue(for: unit),
              let maximum = stats?.maximumQuantity()?.doubleValue(for: unit),
              average > 0, maximum <= 300, average <= maximum else { return nil }
        return (average.rounded(), maximum.rounded())
    }

    static func distance(_ workout: HKWorkout) -> Double? {
        workout.totalDistance?.doubleValue(for: .meter())
    }

    static func energy(_ workout: HKWorkout) -> Double? {
        workout.statistics(for: HKQuantityType(.activeEnergyBurned))?.sumQuantity()?.doubleValue(for: .kilocalorie())
    }

    /// Whole kilometres for a run, walk, hike or ride (`WorkoutSplits`); none for other activities.
    static func splits(_ workout: HKWorkout) async throws -> [WorkoutSplit] {
        guard let type = distanceType(workout.workoutActivityType) else { return [] }
        let samples: [HKQuantitySample] = try await withCheckedThrowingContinuation { continuation in
            let query = HKSampleQuery(sampleType: type, predicate: HKQuery.predicateForObjects(from: workout),
                                      limit: HKObjectQueryNoLimit, sortDescriptors: nil) { _, samples, error in
                if let error { continuation.resume(throwing: error) }
                else { continuation.resume(returning: samples as? [HKQuantitySample] ?? []) }
            }
            store.execute(query)
        }
        let paused = WorkoutSplits.pauses((workout.workoutEvents ?? []).map { ($0.type, $0.dateInterval.start) }, end: workout.endDate)
        return WorkoutSplits.of(samples.map { .init(start: $0.startDate, end: $0.endDate, meters: $0.quantity.doubleValue(for: .meter())) },
                                start: workout.startDate, end: workout.endDate, paused: paused)
    }

    /// The heart rate minute by minute from the start, each minute's samples averaged.
    static func heartRates(_ workout: HKWorkout) async throws -> [WorkoutHeartRate] {
        let samples: [HKQuantitySample] = try await withCheckedThrowingContinuation { continuation in
            let predicate = NSCompoundPredicate(andPredicateWithSubpredicates: [
                HKQuery.predicateForObjects(from: workout),
                HKQuery.predicateForSamples(withStart: workout.startDate, end: workout.endDate)
            ])
            let query = HKSampleQuery(sampleType: HKQuantityType(.heartRate), predicate: predicate,
                                      limit: HKObjectQueryNoLimit, sortDescriptors: nil) { _, samples, error in
                if let error { continuation.resume(throwing: error) }
                else { continuation.resume(returning: samples as? [HKQuantitySample] ?? []) }
            }
            store.execute(query)
        }
        var buckets: [Int: [Double]] = [:]
        for sample in samples {
            let minute = Int(sample.startDate.timeIntervalSince(workout.startDate) / 60)
            let bpm = sample.quantity.doubleValue(for: HKUnit.count().unitDivided(by: .minute()))
            if minute >= 0, minute <= Int(workout.endDate.timeIntervalSince(workout.startDate) / 60), bpm > 0, bpm <= 300 {
                buckets[minute, default: []].append(bpm)
            }
        }
        return buckets.keys.sorted().map { minute in
            let values = buckets[minute]!
            return WorkoutHeartRate(minute: minute, bpm: values.reduce(0, +) / Double(values.count))
        }
    }
}

/// `phone_workouts_recent` and `phone_workout_detail` (#2526): the user's workouts in Apple Health, from the Workout app
/// or any app that saves them, so a report can compare a workout with recent ones and later questions use the same data.
enum WorkoutTool {
    static var tools: [PhoneTool] {
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_workouts_recent",
                    description: """
                    List the user's recent workouts from Apple Health (the Workout app and any app that saves \
                    workouts), newest first. Each row: id, activity, source app, start and end, duration (seconds), \
                    distance (meters), active energy (kcal), elevation_gain (meters), heart_rate_avg and \
                    heart_rate_max (bpm), and pace (seconds per kilometre) for runs, walks, hikes and rides. Fields \
                    without data are left out. Use it to compare a workout with recent ones of the same activity. \
                    No route or location is read. Read access is never reported to apps, so an empty result can \
                    also mean the user did not allow Persona to read workouts.
                    """,
                    inputSchema: Schema.object([
                        "days": Schema.integer("How many days back from now, default 30, max 365.", min: 1, max: 365),
                        "activity": Schema.string("Only this activity; all activities when absent.",
                                                  values: WorkoutReader.activities.keys.sorted()),
                        "limit": Schema.integer("At most this many workouts, default 20, max 50.", min: 1, max: 50),
                    ])
                ),
                source: .health,
                run: { args in try await recent(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_workout_detail",
                    description: """
                    One workout from Apple Health by its id (from phone_workouts_recent or a workout report): the \
                    fields phone_workouts_recent gives, plus splits (whole kilometres in order, the seconds each took \
                    with pauses left out) for runs, walks, hikes and rides, and heart_rate (the average bpm of each \
                    minute from the start). No route or location is read.
                    """,
                    inputSchema: Schema.object([
                        "id": Schema.string("The workout's id."),
                    ], required: ["id"])
                ),
                source: .health,
                run: { args in try await detail(args) }
            ),
        ]
    }

    static func recent(_ args: JSONValue) async throws -> [ToolContent] {
        try HealthAccess.requireAccess()
        var activities: [HKWorkoutActivityType]?
        if let name = ToolArgs.string(args, "activity") {
            let key = name.lowercased().replacingOccurrences(of: " ", with: "_")
            guard let types = WorkoutReader.activities[key] else {
                throw ToolFailure(.invalidArgument, "unknown activity \"\(name)\"; use one of \(WorkoutReader.activities.keys.sorted().joined(separator: ", "))")
            }
            activities = types
        }
        await WorkoutReader.askForNewTypes()
        let workouts = try await WorkoutReader.recent(days: ToolArgs.int(args, "days", fallback: 30, min: 1, max: 365),
                                                      activities: activities,
                                                      limit: ToolArgs.int(args, "limit", fallback: 20, min: 1, max: 50))
        return Output.json(Output.object([("workouts", Output.list(workouts.map { .object(row($0)) }))]))
    }

    static func detail(_ args: JSONValue) async throws -> [ToolContent] {
        try HealthAccess.requireAccess()
        guard let raw = ToolArgs.string(args, "id"), let id = UUID(uuidString: raw) else {
            throw ToolFailure(.invalidArgument, "id must be a workout's id from phone_workouts_recent")
        }
        await WorkoutReader.askForNewTypes()
        guard let workout = try await WorkoutReader.workout(id: id) else {
            throw ToolFailure(.unavailable, "No workout with id \(raw) in Apple Health (deleted, or Persona may not read it).")
        }
        var out = row(workout)
        let splits = try await WorkoutReader.splits(workout)
        if !splits.isEmpty {
            out["splits"] = .array(splits.map { .object(["km": .number(Double($0.km)), "seconds": .number($0.seconds)]) })
        }
        let rates = try await WorkoutReader.heartRates(workout)
        if !rates.isEmpty {
            out["heart_rate"] = .array(rates.map { .object(["minute": .number(Double($0.minute)), "bpm": .number($0.bpm.rounded())]) })
        }
        return Output.json(.object(out))
    }

    /// One workout's row; fields without data are left out.
    static func row(_ workout: HKWorkout) -> [String: JSONValue] {
        let distance = WorkoutReader.distance(workout)
        let rate = WorkoutReader.heartRate(workout)
        let byTheKilometre: Set<HKWorkoutActivityType> = [.running, .walking, .hiking, .cycling]
        let pace: Double? = if let distance, distance >= 500, byTheKilometre.contains(workout.workoutActivityType) {
            (workout.duration / (distance / 1000)).rounded()
        } else { nil }
        guard case .object(let fields) = Output.object([
            ("id", .string(workout.uuid.uuidString)),
            ("activity", .string(WorkoutReader.activityName(workout.workoutActivityType))),
            ("source", Output.opt(workout.sourceRevision.source.name)),
            ("start", .string(ToolDate.string(workout.startDate))),
            ("end", .string(ToolDate.string(workout.endDate))),
            ("duration", .number(workout.duration.rounded())),
            ("distance", Output.opt(distance?.rounded())),
            ("energy", Output.opt(WorkoutReader.energy(workout)?.rounded())),
            ("elevation_gain", Output.opt(WorkoutReader.elevationGain(workout))),
            ("heart_rate_avg", Output.opt(rate?.average)),
            ("heart_rate_max", Output.opt(rate?.maximum)),
            ("pace", Output.opt(pace)),
        ]) else { return [:] }
        return fields
    }
}
#endif
