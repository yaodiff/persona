import Foundation
import HealthKit

/// `phone_health_summary`: per-day aggregates, one HealthKit collection query
/// per metric. `phone_health_write_sample`: one sample the user approved on the
/// phone; nothing is imported in bulk or deleted.
enum HealthTool {
    /// What the summary reads.
    static var readTypes: Set<HKObjectType> {
        var types: Set<HKObjectType> = []
        for identifier in quantityIdentifiers.values {
            if let type = HKQuantityType.quantityType(forIdentifier: identifier) { types.insert(type) }
        }
        if let sleep = HKCategoryType.categoryType(forIdentifier: .sleepAnalysis) { types.insert(sleep) }
        #if os(iOS) || os(visionOS)
        types.formUnion(WorkoutReader.readTypes) // phone_workouts_recent / phone_workout_detail (#2526)
        #endif
        return types
    }

    static let quantityIdentifiers: [String: HKQuantityTypeIdentifier] = [
        "steps": .stepCount,
        "distance": .distanceWalkingRunning,
        "active_energy": .activeEnergyBurned,
        "resting_energy": .basalEnergyBurned,
        "dietary_energy": .dietaryEnergyConsumed,
        "protein": .dietaryProtein,
        "carbs": .dietaryCarbohydrates,
        "fat": .dietaryFatTotal,
        "heart_rate": .heartRate,
        "resting_heart_rate": .restingHeartRate,
        "weight": .bodyMass,
    ]

    private static var metricNames: [String] { Array(quantityIdentifiers.keys) + ["sleep"] }

    /// What `phone_health_write_sample` may save, one approved sample at a time.
    static var writeTypes: Set<HKSampleType> { Set(HealthSample.metrics.compactMap(HealthSample.type(of:))) }

    static var tools: [PhoneTool] {
        [
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_health_summary",
                    description: """
                    Summarize the user's health data per day. Metrics: steps, distance (meters), \
                    active_energy, resting_energy, dietary_energy (kcal, daily totals), \
                    protein, carbs, fat (grams, daily totals), heart_rate (bpm, average/min/max over the day), \
                    resting_heart_rate (bpm), sleep (hours asleep), weight (kg). Defaults to steps \
                    over the last 7 days. Only the requested metrics are read. Days run from local \
                    midnight to midnight; today's numbers are partial. Returns one row per day with \
                    only the days that have data. Read access is never reported to apps, so an empty \
                    result can also mean the user denied read access. When the iPhone is locked the \
                    app cannot read HealthKit: it returns the last snapshot saved while the device \
                    was unlocked ("source": "snapshot" with "as_of"), or a device_locked error when \
                    no snapshot exists yet.
                    """,
                    inputSchema: Schema.object([
                        "metrics": Schema.array(
                            "Metrics to summarize. Default [\"steps\"].",
                            of: Schema.string("One metric.", values: metricNames)
                        ),
                        "days": Schema.integer("How many days back from today, default 7, max 90.", min: 1, max: 90),
                    ])
                ),
                source: .health,
                run: { args in try await summary(args) }
            ),
            PhoneTool(
                spec: ToolSpec(
                    name: "phone_health_write_sample",
                    description: """
                    Save one health sample the user gave you to Apple Health: steps (a whole number), \
                    distance (meters walked or run), active_energy (kcal), weight (kg), or sleep (a period \
                    asleep, from start to end; value is not used). steps, distance and active_energy may \
                    cover a period from start to end, at most 24 hours; nothing in the future. The user \
                    approves the exact value and time on the phone before it is saved. One sample per call: \
                    no bulk import, and nothing is ever deleted. Returns the saved sample's id.
                    """,
                    inputSchema: Schema.object([
                        "metric": Schema.string("What to save.", values: HealthSample.metrics),
                        "value": Schema.number("The amount: steps as a whole number, distance in meters, active_energy in kcal, weight in kg. Not used for sleep."),
                        "start": Schema.string("When, ISO 8601 with a timezone: the moment a weight was measured, or the start of the period."),
                        "end": Schema.string("End of the period, ISO 8601 with a timezone. Required for sleep; defaults to start; not used for weight."),
                    ], required: ["metric", "start"])
                ),
                source: .health,
                confirmation: { args in
                    do {
                        return try HealthSample(args).summary
                    } catch {
                        return String(localized: "Invalid arguments; nothing will be saved to Health: \(ToolErrors.describe(error))")
                    }
                },
                run: { args in try await write(args) }
            ),
        ]
    }

    /// Saves the sample the user approved (the confirmation showed `HealthSample.summary` of the same arguments).
    static func write(_ args: JSONValue) async throws -> [ToolContent] {
        try HealthAccess.requireAccess()
        let sample = try HealthSample(args)
        guard let type = HealthSample.type(of: sample.metric), let object = sample.object() else {
            throw ToolFailure(.unavailable, "此设备不能写入 \(sample.metric) / This device cannot save \(sample.metric)") // i18n-ignore
        }
        let store = HKHealthStore()
        if store.authorizationStatus(for: type) == .notDetermined {
            // Health was allowed before the app could write (reading only): the system asks for writing now, right
            // after the user approved this sample on the phone.
            _ = try? await store.requestAuthorization(toShare: writeTypes, read: readTypes)
        }
        guard store.authorizationStatus(for: type) == .sharingAuthorized else {
            throw ToolFailure(.unavailable, "健康没有允许 Persona 写入 \(sample.metric)：可在 iOS 设置 › 健康 › 数据访问与设备 › Persona 里打开" // i18n-ignore
                + " / Health does not allow Persona to save \(sample.metric): turn it on in iOS Settings › Health › Data Access & Devices › Persona.")
        }
        let id = object.uuid.uuidString
        try await store.save(object)
        return Output.json(Output.object([
            ("saved", .bool(true)),
            ("id", .string(id)),
            ("metric", .string(sample.metric)),
            ("value", Output.opt(sample.value)),
            ("unit", Output.opt(HealthSample.units[sample.metric])),
            ("start", .string(ToolDate.string(sample.start))),
            ("end", .string(ToolDate.string(sample.end))),
        ]))
    }

    /// Bilingual, machine-recognizable failure for a locked phone with no snapshot yet.
    static let deviceLockedMessage =
        "iPhone 已锁定且还没有健康快照（device_locked）：解锁后打开一次 Persona，之后锁屏也能返回最近的快照。" // i18n-ignore
        + " / The iPhone is locked and no health snapshot exists yet (device_locked): unlock the phone and open Persona once, then retry."

    /// HealthKit answers with this while the device is locked (the protected database is closed).
    static func isDatabaseInaccessible(_ error: Error) -> Bool {
        (error as? HKError)?.code == .errorDatabaseInaccessible
    }

    static func summary(
        _ args: JSONValue,
        snapshotStore: any HealthSnapshotStoring = HealthSnapshotStore.shared,
        isDataAvailable: @escaping () -> Bool = HealthDataLock.isProtectedDataAvailable
    ) async throws -> [ToolContent] {
        try HealthAccess.requireAccess()
        let days = ToolArgs.int(args, "days", fallback: 7, min: 1, max: 90)
        var requested: [String] = []
        if let raw = args["metrics"]?.array {
            for item in raw {
                guard let name = item.string else { continue }
                guard metricNames.contains(name) else {
                    throw ToolFailure(.invalidArgument, "unknown metric \"\(name)\"; use one of \(metricNames.joined(separator: ", "))")
                }
                if !requested.contains(name) { requested.append(name) }
            }
        }
        if requested.isEmpty { requested = ["steps"] }

        let calendar = Calendar.current
        let end = Date()

        // Locked phone: never touch HealthKit (its error would be the raw OS string the old build put
        // into every field); answer from the snapshot written while the device was unlocked.
        if !isDataAvailable() {
            return try await snapshotAnswer(store: snapshotStore, metrics: requested, days: days, now: end, calendar: calendar)
        }

        await requestAdditionalReadAccess(for: requested)
        let start = calendar.startOfDay(for: calendar.date(byAdding: .day, value: -(days - 1), to: end) ?? end)

        let store = HKHealthStore()
        var byDay: [String: [String: JSONValue]] = [:]
        for metric in requested {
            let rows: [String: JSONValue]
            do {
                if metric == "sleep" {
                    rows = try await sleepByDay(store, start: start, end: end, calendar: calendar)
                } else {
                    rows = try await quantityByDay(store, metric: metric, start: start, end: end, calendar: calendar)
                }
            } catch {
                // The phone can lock between the check above and the query: the database closes under us.
                // Serve the snapshot instead of dumping the English HKError into the day's fields.
                if Self.isDatabaseInaccessible(error) {
                    return try await snapshotAnswer(store: snapshotStore, metrics: requested, days: days, now: end, calendar: calendar)
                }
                // One failed metric should not sink the whole summary.
                rows = [ToolDate.day(end): .object(["error": .string(error.localizedDescription)])]
            }
            for (day, value) in rows {
                byDay[day, default: [:]][metric] = value
            }
        }

        // Keep the locked-phone fallback warm; throttled inside the store, never blocks the answer.
        Task { await refreshSnapshot(snapshotStore: snapshotStore) }

        let sorted = byDay.keys.sorted()
        let rows: [JSONValue] = sorted.map { day in
            var row: [String: JSONValue] = ["date": .string(day)]
            for (metric, value) in byDay[day] ?? [:] { row[metric] = value }
            return .object(row)
        }
        return Output.json(Output.object([
            ("start", .string(ToolDate.string(start))),
            ("end", .string(ToolDate.string(end))),
            ("days", .number(Double(days))),
            ("metrics", .array(requested.map { .string($0) })),
            ("data", .array(rows)),
            ("source", .string("live")),
        ]))
    }

    /// The locked-phone answer: the stored snapshot restricted to the request, or an explicit
    /// `device_locked` failure when nothing has been saved yet.
    private static func snapshotAnswer(
        store: any HealthSnapshotStoring,
        metrics requested: [String],
        days: Int,
        now: Date,
        calendar: Calendar
    ) async throws -> [ToolContent] {
        if let snapshot = await store.load(),
           let answer = snapshot.response(metrics: requested, days: days, now: now, calendar: calendar) {
            return Output.json(answer)
        }
        throw ToolFailure(.unavailable, deviceLockedMessage)
    }

    /// Re-reads every metric over the full snapshot window and saves it for the next locked-phone call.
    /// Runs from the foreground hook and after successful live reads; the store throttles repeats.
    static func refreshSnapshot(
        snapshotStore: any HealthSnapshotStoring = HealthSnapshotStore.shared,
        isDataAvailable: @escaping () -> Bool = HealthDataLock.isProtectedDataAvailable,
        now: Date = Date()
    ) async {
        guard HKHealthStore.isHealthDataAvailable(), HealthAccess.cachedStatus() == .granted, isDataAvailable() else { return }
        if await snapshotStore.cachedIfFresh(now: now) != nil { return }
        await requestAdditionalReadAccess(for: metricNames)
        let calendar = Calendar.current
        let end = now
        let start = calendar.startOfDay(for: calendar.date(byAdding: .day, value: -(HealthSnapshotStore.windowDays - 1), to: end) ?? end)
        let store = HKHealthStore()
        var rows: [String: [String: JSONValue]] = [:]
        for metric in metricNames {
            let metricRows: [String: JSONValue]
            do {
                metricRows = metric == "sleep"
                    ? try await sleepByDay(store, start: start, end: end, calendar: calendar)
                    : try await quantityByDay(store, metric: metric, start: start, end: end, calendar: calendar)
            } catch {
                if Self.isDatabaseInaccessible(error) { return } // locked mid-refresh: keep the previous snapshot
                continue
            }
            for (day, value) in metricRows {
                rows[day, default: [:]][metric] = value
            }
        }
        await snapshotStore.save(HealthSnapshot(
            asOf: end,
            windowDays: HealthSnapshotStore.windowDays,
            metrics: metricNames,
            rows: rows
        ))
        await snapshotStore.markRefreshed(at: end)
    }

    /// One metric's per-day values over the last `days` days (today included), keyed "yyyy-MM-dd": the numbers the
    /// summary gives for it, read the same way, for the app's own health trends (Views/Health/HealthTrendsModel.swift).
    /// Days without data are left out.
    static func daily(_ metric: String, days: Int) async throws -> [String: Double] {
        try HealthAccess.requireAccess()
        guard metricNames.contains(metric), metric != "heart_rate" else {
            throw ToolFailure(.invalidArgument, "no daily trend for \"\(metric)\"")
        }
        await requestAdditionalReadAccess(for: [metric])
        let calendar = Calendar.current
        let end = Date()
        let start = calendar.startOfDay(for: calendar.date(byAdding: .day, value: -(max(days, 1) - 1), to: end) ?? end)
        let store = HKHealthStore()
        let rows = metric == "sleep"
            ? try await sleepByDay(store, start: start, end: end, calendar: calendar)
            : try await quantityByDay(store, metric: metric, start: start, end: end, calendar: calendar)
        return rows.compactMapValues(\.double)
    }

    // MARK: - Queries

    private static func requestAdditionalReadAccess(for metrics: [String]) async {
        // Covers people who allowed Health before weight, nutrition or resting energy were supported.
        // Existing steps/sleep trends and widgets must not prompt just because new types were added.
        let addedMetrics: Set<String> = ["weight", "resting_energy", "dietary_energy", "protein", "carbs", "fat"]
        guard metrics.contains(where: addedMetrics.contains) else { return }
        _ = try? await HKHealthStore().requestAuthorization(toShare: Set<HKSampleType>(), read: readTypes)
    }

    static func quantityUnit(for metric: String) -> HKUnit {
        switch metric {
        case "steps": .count()
        case "distance": .meter()
        case "active_energy", "resting_energy", "dietary_energy": .kilocalorie()
        case "protein", "carbs", "fat": .gram()
        case "weight": .gramUnit(with: .kilo)
        default: HKUnit.count().unitDivided(by: .minute())
        }
    }

    private static func quantityByDay(
        _ store: HKHealthStore,
        metric: String,
        start: Date,
        end: Date,
        calendar: Calendar
    ) async throws -> [String: JSONValue] {
        guard let identifier = quantityIdentifiers[metric],
              let type = HKQuantityType.quantityType(forIdentifier: identifier) else {
            throw ToolFailure(.invalidArgument, "this iPhone does not support the metric \"\(metric)\"")
        }
        let options = statisticsOptions(for: type, metric: metric)
        let units = quantityUnit(for: metric)

        let query = HKStatisticsCollectionQuery(
            quantityType: type,
            quantitySamplePredicate: HKQuery.predicateForSamples(withStart: start, end: end, options: .strictStartDate),
            options: options,
            anchorDate: start,
            intervalComponents: DateComponents(day: 1)
        )
        let collection: HKStatisticsCollection = try await withCheckedThrowingContinuation { continuation in
            let gate = SingleResume(continuation)
            query.initialResultsHandler = { _, collection, error in
                if let error {
                    gate.fail(error)
                } else if let collection {
                    gate.succeed(collection)
                } else {
                    gate.fail(ToolFailure(.unavailable, "HealthKit returned no statistics for \(metric)"))
                }
            }
            store.execute(query)
        }

        var out: [String: JSONValue] = [:]
        collection.enumerateStatistics(from: start, to: end) { statistics, _ in
            let day = ToolDate.day(statistics.startDate)
            switch metric {
            case "heart_rate":
                guard let average = statistics.averageQuantity()?.doubleValue(for: units) else { return }
                let minValue = statistics.minimumQuantity().map { $0.doubleValue(for: units) }
                let maxValue = statistics.maximumQuantity().map { $0.doubleValue(for: units) }
                out[day] = Output.object([
                    ("average", .number(round1(average))),
                    ("min", minValue.map { JSONValue.number(round1($0)) }),
                    ("max", maxValue.map { JSONValue.number(round1($0)) }),
                ])
            default:
                let quantity = options.contains(.cumulativeSum) ? statistics.sumQuantity() : statistics.averageQuantity()
                guard let value = quantity?.doubleValue(for: units) else { return }
                out[day] = .number(round1(value))
            }
        }
        return out
    }

    /// What a day's statistics hold: the sum of a cumulative type (steps, distance, energy, nutrients), the average of a discrete one
    /// (weight, resting heart rate), heart rate's range too. An option the type cannot answer (summing weight) raises an
    /// exception inside HealthKit when the query runs, which takes the app down.
    static func statisticsOptions(for type: HKQuantityType, metric: String) -> HKStatisticsOptions {
        if metric == "heart_rate" { return [.discreteAverage, .discreteMin, .discreteMax] }
        return type.aggregationStyle == .cumulative ? .cumulativeSum : .discreteAverage
    }

    /// Sleep is a category type: sum every sample whose value counts as asleep,
    /// clipped to the day so a night that crosses midnight splits correctly.
    private static func sleepByDay(
        _ store: HKHealthStore,
        start: Date,
        end: Date,
        calendar: Calendar
    ) async throws -> [String: JSONValue] {
        guard let type = HKCategoryType.categoryType(forIdentifier: .sleepAnalysis) else {
            throw ToolFailure(.unavailable, "此设备不支持睡眠数据 / Sleep analysis is not available") // i18n-ignore
        }
        let predicate = HKQuery.predicateForSamples(withStart: start, end: end, options: [])
        let samples: [HKCategorySample] = try await withCheckedThrowingContinuation { continuation in
            let gate = SingleResume(continuation)
            let query = HKSampleQuery(sampleType: type, predicate: predicate, limit: HKObjectQueryNoLimit, sortDescriptors: nil) { _, result, error in
                if let error {
                    gate.fail(error)
                } else {
                    gate.succeed((result as? [HKCategorySample]) ?? [])
                }
            }
            store.execute(query)
        }

        let asleepValues = Set(HKCategoryValueSleepAnalysis.allAsleepValues.map(\.rawValue))
        var seconds: [String: TimeInterval] = [:]
        for sample in samples where asleepValues.contains(sample.value) {
            var cursor = max(sample.startDate, start)
            let sampleEnd = min(sample.endDate, end)
            while cursor < sampleEnd {
                let dayStart = calendar.startOfDay(for: cursor)
                let dayEnd = calendar.date(byAdding: .day, value: 1, to: dayStart) ?? sampleEnd
                let chunkEnd = min(dayEnd, sampleEnd)
                let day = ToolDate.day(dayStart)
                seconds[day, default: 0] += chunkEnd.timeIntervalSince(cursor)
                cursor = chunkEnd
            }
        }
        var out: [String: JSONValue] = [:]
        for (day, total) in seconds where total > 0 {
            out[day] = .number(round1(total / 3600))
        }
        return out
    }

    private static func round1(_ value: Double) -> Double {
        (value * 10).rounded() / 10
    }
}

/// One sample for `phone_health_write_sample`, read from the tool's arguments. The confirmation shows `summary` and the
/// tool saves this same sample: what the user approves is what lands in Health (values keep two decimals at most).
struct HealthSample {
    static let metrics = ["steps", "distance", "active_energy", "weight", "sleep"]

    /// The units of `value`, as the tool returns them.
    static let units = ["steps": "count", "distance": "m", "active_energy": "kcal", "weight": "kg"]

    /// What one sample can hold: more is a slip (grams for kilograms, a week's steps at once).
    private static let limits: [String: Double] = ["steps": 100_000, "distance": 300_000, "active_energy": 10_000, "weight": 500]

    private static let names = ["steps": String(localized: "Steps"), "distance": String(localized: "Walking + running distance"), "active_energy": String(localized: "Active energy"), "weight": String(localized: "Sleep [Health Tool 38f7c7]"), "sleep": String(localized: "Sleep [Health Tool 01324e]")]
    private static let shownUnits = ["steps": String(localized: "steps"), "distance": String(localized: "m"), "active_energy": String(localized: "kcal"), "weight": String(localized: "kg")]

    var metric: String
    /// The amount in the metric's unit; nil for sleep, whose length is the period.
    var value: Double?
    var start: Date
    var end: Date

    init(_ args: JSONValue, now: Date = Date()) throws {
        guard let metric = ToolArgs.string(args, "metric"), Self.metrics.contains(metric) else {
            throw ToolFailure(.invalidArgument, "metric must be one of \(Self.metrics.joined(separator: ", "))")
        }
        guard let start = try ToolDate.parse(ToolArgs.string(args, "start")) else {
            throw ToolFailure(.invalidArgument, "start is required (ISO 8601 with a timezone)")
        }
        let end: Date
        if metric == "weight" {
            end = start // a measurement, not a period
        } else {
            end = try ToolDate.parse(ToolArgs.string(args, "end")) ?? start
        }
        guard end >= start else { throw ToolFailure(.invalidArgument, "end must not be before start") }
        guard end <= now.addingTimeInterval(60) else { throw ToolFailure(.invalidArgument, "a sample cannot be in the future") }
        guard end.timeIntervalSince(start) <= 24 * 3600 else { throw ToolFailure(.invalidArgument, "one sample covers 24 hours at most") }
        if metric == "sleep" {
            guard end > start else { throw ToolFailure(.invalidArgument, "sleep needs an end after its start") }
            value = nil
        } else {
            guard let raw = args["value"]?.double, raw.isFinite else {
                throw ToolFailure(.invalidArgument, "value is required: a number in \(Self.units[metric] ?? "")")
            }
            let rounded = (raw * 100).rounded() / 100
            guard rounded > 0 else { throw ToolFailure(.invalidArgument, "value must be above 0") }
            if metric == "steps", rounded != rounded.rounded() { throw ToolFailure(.invalidArgument, "steps must be a whole number") }
            if let limit = Self.limits[metric], rounded > limit {
                throw ToolFailure(.invalidArgument, "\(Self.format(rounded)) \(Self.units[metric] ?? "") is more than one sample of \(metric) can be (at most \(Self.format(limit)))")
            }
            value = rounded
        }
        self.metric = metric
        self.start = start
        self.end = end
    }

    /// The confirmation's text: what will be saved, and when.
    var summary: String {
        let name = Self.names[metric] ?? metric
        let what = value.map { "\(name) \(Self.format($0)) \(Self.shownUnits[metric] ?? "")" }
            ?? String(localized: "\(name) \(Self.format(end.timeIntervalSince(start) / 3600)) h")
        let when = start == end ? ToolDate.string(start) : "\(ToolDate.string(start)) → \(ToolDate.string(end))"
        return String(localized: "Save to Health: \(what)\\n\(when)")
    }

    static func type(of metric: String) -> HKSampleType? {
        switch metric {
        case "steps": HKQuantityType.quantityType(forIdentifier: .stepCount)
        case "distance": HKQuantityType.quantityType(forIdentifier: .distanceWalkingRunning)
        case "active_energy": HKQuantityType.quantityType(forIdentifier: .activeEnergyBurned)
        case "weight": HKQuantityType.quantityType(forIdentifier: .bodyMass)
        case "sleep": HKCategoryType.categoryType(forIdentifier: .sleepAnalysis)
        default: nil
        }
    }

    /// The HealthKit sample, marked as entered by the user (Health lists it as added from Persona).
    func object() -> HKSample? {
        let metadata: [String: Any] = [HKMetadataKeyWasUserEntered: true]
        if let type = Self.type(of: metric) as? HKQuantityType, let value {
            let unit: HKUnit = switch metric {
            case "steps": .count()
            case "distance": .meter()
            case "active_energy": .kilocalorie()
            default: .gramUnit(with: .kilo)
            }
            return HKQuantitySample(type: type, quantity: HKQuantity(unit: unit, doubleValue: value), start: start, end: end, metadata: metadata)
        }
        if let type = Self.type(of: metric) as? HKCategoryType {
            return HKCategorySample(type: type, value: HKCategoryValueSleepAnalysis.asleepUnspecified.rawValue, start: start, end: end, metadata: metadata)
        }
        return nil
    }

    private static func format(_ number: Double) -> String {
        number.formatted(.number.precision(.fractionLength(0...2)).grouping(.never).locale(Locale(identifier: "en_US_POSIX")))
    }
}
