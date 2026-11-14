#if os(iOS)
import Foundation
import Testing
@testable import Persona

/// Records which reports reached the fake hub, in order.
private actor Uploads {
    private(set) var ids: [String] = []
    func add(_ id: String) { ids.append(id) }
}

@MainActor
struct WorkoutReportsTests {
    private static func report(_ id: String = UUID().uuidString) -> WorkoutReports.Report {
        let start = Date(timeIntervalSince1970: 1_700_000_000)
        return WorkoutReports.Report(id: id, title: "Workout report", activity: "Running", source: "Test", start: start,
            end: start.addingTimeInterval(600), enabled_at: start, duration: 600, distance: nil, energy: nil, heart_rate: [])
    }

    /// An opted-in state whose outbox holds `pending`, and a hub that answers `status` for `failing` and accepts the rest.
    private static func outbox(_ pending: [WorkoutReports.Report], failing: String, status: Int)
        throws -> (WorkoutReports, APIClient, Uploads, URL) {
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let state = WorkoutReports.State(enabledAt: pending[0].start, group: "test-hub", pending: pending)
        try JSON.encoder.encode(state).write(to: folder.appending(path: "state.json"))
        let uploads = Uploads()
        let api = APIClient(config: ServerConfig(url: URL(string: "https://hub.invalid")!, token: "t"), transport: { request in
            let id = try JSON.decoder.decode(WorkoutReports.Report.self, from: request.body ?? Data()).id
            await uploads.add(id)
            if id == failing { throw APIError(status: status, message: "invalid workout") }
            return Data(#"{"accepted":true}"#.utf8)
        })
        return (WorkoutReports(folder: folder), api, uploads, folder)
    }

    private static func pending(in folder: URL) throws -> [String] {
        try JSON.decoder.decode(WorkoutReports.State.self, from: Data(contentsOf: folder.appending(path: "state.json")))
            .pending.map(\.id)
    }

    @Test(arguments: [400, 413, 422])
    func rejectedReportDoesNotBlockLaterOnes(status: Int) async throws {
        let bad = Self.report(), good = Self.report()
        let (reports, api, uploads, folder) = try Self.outbox([bad, good], failing: bad.id, status: status)
        defer { try? FileManager.default.removeItem(at: folder) }
        #expect(try await reports.upload(api: api) == 1)
        #expect(await uploads.ids == [bad.id, good.id])
        #expect(try Self.pending(in: folder).isEmpty)
    }

    @Test(arguments: [401, 403, 404, 429, 500, 503])
    func otherFailuresKeepTheOutbox(status: Int) async throws {
        let first = Self.report(), second = Self.report()
        let (reports, api, uploads, folder) = try Self.outbox([first, second], failing: first.id, status: status)
        defer { try? FileManager.default.removeItem(at: folder) }
        do {
            _ = try await reports.upload(api: api)
            Issue.record("HTTP \(status) emptied the outbox")
        } catch let error as APIError {
            #expect(error.status == status)
        }
        #expect(await uploads.ids == [first.id])
        #expect(try Self.pending(in: folder) == [first.id, second.id])
    }

    @Test func newInstallDoesNotOptIn() throws {
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        let reports = WorkoutReports(folder: folder)
        #expect(!reports.enabled)
        #expect(reports.error == nil)
    }

    @Test func unreadableConsentFailsClosed() throws {
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        try Data("broken state".utf8).write(to: folder.appending(path: "state.json"))
        let reports = WorkoutReports(folder: folder)
        #expect(!reports.enabled)
        #expect(reports.error != nil)
    }

    @Test func restartPreservesConsentAndPendingMeasurements() throws {
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: folder) }
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        let start = Date(timeIntervalSince1970: 1_700_000_000)
        let report = WorkoutReports.Report(id: UUID().uuidString, title: "Workout report", activity: "Running",
            source: "Test", start: start, end: start.addingTimeInterval(600), enabled_at: start,
            duration: 600, distance: nil, energy: nil, heart_rate: [])
        let state = WorkoutReports.State(enabledAt: start, group: "test-hub", anchor: Data([1, 2]), pending: [report])
        let file = folder.appending(path: "state.json")
        try JSON.encoder.encode(state).write(to: file)
        #expect(WorkoutReports(folder: folder).enabled)
        let restored = try JSON.decoder.decode(WorkoutReports.State.self, from: Data(contentsOf: file))
        #expect(restored.enabledAt == start)
        #expect(restored.pending.first?.id == report.id)
        #expect(restored.pending.first?.distance == nil)
        #expect(restored.anchor == Data([1, 2]))
    }

    /// A report queued by the version before splits (#2526) still decodes and is sent without the new fields.
    @Test func aReportQueuedBeforeSplitsStillGoesOut() throws {
        let old = #"{"id":"12345678-1234-1234-1234-123456789abc","title":"Workout report","activity":"Running","source":"Watch","start":"2026-11-01T08:00:00Z","end":"2026-11-01T08:40:00Z","enabled_at":"2026-11-01T07:00:00Z","duration":2400,"distance":6000,"energy":400,"heart_rate":[]}"#
        let report = try JSON.decoder.decode(WorkoutReports.Report.self, from: Data(old.utf8))
        #expect(report.splits == nil && report.elevation_gain == nil && report.heart_rate_max == nil)
        let sent = try #require(String(data: JSON.encoder.encode(report), encoding: .utf8))
        #expect(!sent.contains("splits") && !sent.contains("elevation_gain"))
    }
}

/// Per-kilometre splits from a workout's distance samples (#2526).
struct WorkoutSplitsTests {
    private let start = Date(timeIntervalSince1970: 1_700_000_000)

    /// Samples of `meters` every `seconds` from the start.
    private func steady(_ count: Int, meters: Double, seconds: Double) -> [WorkoutSplits.Sample] {
        (0..<count).map { i in
            WorkoutSplits.Sample(start: start.addingTimeInterval(Double(i) * seconds),
                                 end: start.addingTimeInterval(Double(i + 1) * seconds), meters: meters)
        }
    }

    @Test func anEvenRunGivesEvenKilometresAndDropsThePartOne() {
        // 2.5 km at 5 min/km, a sample every 30 s.
        let samples = steady(25, meters: 100, seconds: 30)
        let splits = WorkoutSplits.of(samples, start: start, end: start.addingTimeInterval(750), paused: [])
        #expect(splits == [WorkoutSplit(km: 1, seconds: 300), WorkoutSplit(km: 2, seconds: 300)])
    }

    @Test func aPauseIsLeftOut() {
        // 1 km at 5 min/km, then a 2-minute pause, then another km.
        let first = steady(10, meters: 100, seconds: 30)
        let second = steady(10, meters: 100, seconds: 30).map {
            WorkoutSplits.Sample(start: $0.start.addingTimeInterval(420), end: $0.end.addingTimeInterval(420), meters: $0.meters)
        }
        let pause = DateInterval(start: start.addingTimeInterval(300), duration: 120)
        let splits = WorkoutSplits.of(first + second, start: start, end: start.addingTimeInterval(720), paused: [pause])
        #expect(splits.map(\.seconds) == [300, 300])
    }

    @Test func aKilometreInsideOneSampleIsSharedOut() {
        // One sample of 2 km over 10 minutes: each km took 5 minutes.
        let sample = WorkoutSplits.Sample(start: start, end: start.addingTimeInterval(600), meters: 2000)
        #expect(WorkoutSplits.of([sample], start: start, end: start.addingTimeInterval(600), paused: []).map(\.seconds) == [300, 300])
    }

    @Test func samplesThatCannotGiveASensibleSplitGiveNone() {
        let end = start.addingTimeInterval(600)
        let instant = WorkoutSplits.Sample(start: start, end: start, meters: 1500)
        #expect(WorkoutSplits.of([instant], start: start, end: end, paused: []).isEmpty, "a kilometre in no time")
        let before = WorkoutSplits.Sample(start: start.addingTimeInterval(-60), end: start.addingTimeInterval(300), meters: 1200)
        #expect(WorkoutSplits.of([before], start: start, end: end, paused: []).isEmpty, "a sample from before the workout")
        #expect(WorkoutSplits.of([], start: start, end: end, paused: []).isEmpty)
    }

    @Test func pausesComeFromTheWorkoutsEvents() {
        let at = { (s: Double) in self.start.addingTimeInterval(s) }
        let paused = WorkoutSplits.pauses([(.pause, at(100)), (.resume, at(160)), (.motionPaused, at(300)),
                                           (.motionResumed, at(330)), (.lap, at(400)), (.pause, at(500))], end: at(600))
        #expect(paused == [DateInterval(start: at(100), end: at(160)), DateInterval(start: at(300), end: at(330)),
                           DateInterval(start: at(500), end: at(600))])
        #expect(WorkoutSplits.active(from: at(0), to: at(600), paused: paused) == 600 - 60 - 30 - 100)
    }
}
#endif
