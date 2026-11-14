import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Errors and crashes for the hub's error reports (#5621, docs/protocol/protocol.md "Error reports"): the queue on disk, the
/// feature gate, batching and what each answer does to the batch.
struct ErrorReportsTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func queue(_ now: Date? = nil) -> (ErrorReportQueue, URL) {
        let dir = URL.temporaryDirectory.appending(path: "error-reports-" + UUID().uuidString)
        let at = now ?? self.now
        return (ErrorReportQueue(directory: dir, now: { at }), dir)
    }

    private func report(_ id: String = UUID().uuidString.lowercased(), at: Date? = nil, message: String = "boom") -> ExtensionErrorReport {
        ExtensionErrorReport(id: id, source: "ios", kind: "exception", level: "error",
                             time: ErrorReports.timestamp(at ?? now), message: message)
    }

    private func ids(_ req: LocalRequest) -> [String] {
        guard let body = req.body, let batch = try? JSON.decoder.decode(ErrorReportBatch.self, from: body) else { return [] }
        return batch.reports.map(\.id)
    }

    private let on: Set<String> = ["extensions", ErrorReports.feature]

    // MARK: Queue

    @Test func theQueueKeepsTheNewestFiftyForSevenDays() {
        let (q, _) = queue()
        q.write(report("old", at: now.addingTimeInterval(-ErrorReportQueue.maxAge - 60)))
        for i in 0..<(ErrorReportQueue.maxReports + 5) {
            q.write(report(String(format: "r%02d", i), at: now.addingTimeInterval(Double(i - 100))))
        }
        let kept = q.pending().map(\.report.id)
        #expect(kept.count == ErrorReportQueue.maxReports)
        #expect(!kept.contains("old"))
        #expect(kept.first == "r05" && kept.last == String(format: "r%02d", ErrorReportQueue.maxReports + 4)) // oldest first
    }

    @Test func aReportSurvivesInANewQueueOnTheSameDirectory() {
        let (q, dir) = queue()
        q.write(report("kept"))
        let again = ErrorReportQueue(directory: dir, now: { now })
        #expect(again.pending().map(\.report.id) == ["kept"])
    }

    // MARK: Gate

    @Test func withoutTheFeatureNothingIsSentAndTheQueueIsCleared() async {
        let (q, _) = queue()
        q.write(report())
        let hub = FakeHub()
        hub.on("POST", ErrorReports.path, json: #"{"enabled":true,"accepted":1,"dropped":0}"#)
        let result = await ErrorReportSender(queue: q).flush(features: ["extensions"], api: hub.client)
        #expect(result.cleared)
        #expect(hub.sent("POST", ErrorReports.path).isEmpty)
        #expect(q.pending().isEmpty)
    }

    @Test func anAcceptedBatchIsDeletedAndWhatTheHubDroppedIsNotSentAgain() async {
        let (q, _) = queue()
        q.write(report("a"))
        q.write(report("b"))
        let hub = FakeHub()
        hub.on("POST", ErrorReports.path, json: #"{"enabled":true,"accepted":1,"dropped":1}"#)
        let sender = ErrorReportSender(queue: q)
        #expect(await sender.flush(features: on, api: hub.client).sent == 2)
        #expect(q.pending().isEmpty)
        _ = await sender.flush(features: on, api: hub.client)
        #expect(hub.sent("POST", ErrorReports.path).count == 1)
    }

    @Test func aHubThatSaysDisabledClearsTheQueue() async {
        let (q, _) = queue()
        for i in 0..<25 { q.write(report("r\(i)", at: now.addingTimeInterval(Double(i)))) }
        let hub = FakeHub()
        hub.on("POST", ErrorReports.path, json: #"{"enabled":false,"accepted":0}"#)
        let result = await ErrorReportSender(queue: q).flush(features: on, api: hub.client)
        #expect(result.cleared)
        #expect(hub.sent("POST", ErrorReports.path).count == 1) // the second batch is not tried
        #expect(q.pending().isEmpty)
    }

    // MARK: Batches

    @Test func batchesHoldAtMostTwentyReports() async {
        let (q, _) = queue()
        for i in 0..<45 { q.write(report(String(format: "r%02d", i), at: now.addingTimeInterval(Double(i)))) }
        let hub = FakeHub()
        hub.on("POST", ErrorReports.path, json: #"{"enabled":true,"accepted":20,"dropped":0}"#)
        _ = await ErrorReportSender(queue: q).flush(features: on, api: hub.client)
        let sizes = hub.sent("POST", ErrorReports.path).map { ids($0).count }
        #expect(sizes == [20, 20, 5])
    }

    @Test func batchesStayUnderTheHubsSizeLimit() async {
        let (q, _) = queue()
        let big = String(repeating: "x", count: 60 * 1024) // the hub cuts the message; the queue keeps what it was given
        for i in 0..<6 { q.write(report("r\(i)", at: now.addingTimeInterval(Double(i)), message: big)) }
        let hub = FakeHub()
        hub.on("POST", ErrorReports.path, json: #"{"enabled":true,"accepted":1,"dropped":0}"#)
        _ = await ErrorReportSender(queue: q).flush(features: on, api: hub.client)
        let posts = hub.sent("POST", ErrorReports.path)
        #expect(posts.count == 2)
        #expect(posts.allSatisfy { ($0.body?.count ?? .max) <= ErrorReportSender.maxBatchBytes })
        #expect(posts.flatMap(ids).count == 6)
    }

    @Test func aSingleReportOverTheLimitIsDroppedNotSent() async {
        let (q, _) = queue()
        q.write(report("huge", message: String(repeating: "x", count: ErrorReportSender.maxBatchBytes)))
        q.write(report("small", at: now.addingTimeInterval(1)))
        let hub = FakeHub()
        hub.on("POST", ErrorReports.path, json: #"{"enabled":true,"accepted":1,"dropped":0}"#)
        let result = await ErrorReportSender(queue: q).flush(features: on, api: hub.client)
        #expect(result.dropped == 1 && result.sent == 1)
        #expect(hub.sent("POST", ErrorReports.path).flatMap(ids) == ["small"])
    }

    // MARK: Answers

    @Test func aRefusedBatchIsDroppedButATimeoutOrATooManyIsKept() async {
        for (status, kept) in [(400, false), (403, false), (404, false), (408, true), (429, true), (500, true), (503, true)] {
            let (q, _) = queue()
            q.write(report("r"))
            let hub = FakeHub()
            hub.on("POST", ErrorReports.path) { _ in throw APIError(status: status, message: "no") }
            _ = await ErrorReportSender(queue: q).flush(features: on, api: hub.client)
            #expect(q.pending().isEmpty == !kept, "status \(status)")
        }
    }

    @Test func noAnswerKeepsTheBatch() async {
        let (q, _) = queue()
        q.write(report("r"))
        let hub = FakeHub()
        hub.on("POST", ErrorReports.path) { _ in throw URLError(.notConnectedToInternet) }
        let result = await ErrorReportSender(queue: q).flush(features: on, api: hub.client)
        #expect(result.kept == 1)
        #expect(q.pending().map(\.report.id) == ["r"])
    }

    @Test func aRetrySendsTheSameID() async {
        let (q, _) = queue()
        let r = ErrorReports.report(kind: "exception", level: "error", message: "boom", at: now)
        q.write(r)
        let hub = FakeHub()
        var fail = true
        hub.on("POST", ErrorReports.path) { _ in
            if fail { fail = false; throw APIError(status: 503, message: "later") }
            return Data(#"{"enabled":true,"accepted":1,"dropped":0}"#.utf8)
        }
        let sender = ErrorReportSender(queue: q)
        _ = await sender.flush(features: on, api: hub.client)
        _ = await sender.flush(features: on, api: hub.client)
        let sent = hub.sent("POST", ErrorReports.path).map(ids)
        #expect(sent == [[r.id], [r.id]])
        #expect(UUID(uuidString: r.id) != nil)
        #expect(q.pending().isEmpty)
    }

    @Test func aFlushWhileAnotherIsGoingSendsNothing() async {
        let (q, _) = queue()
        q.write(report("r"))
        let held = HeldPosts()
        let api = { () -> APIClient in
            var c = FakeHub().client
            c.transport = { _ in
                await held.arrive()
                return Data(#"{"enabled":true,"accepted":1,"dropped":0}"#.utf8)
            }
            return c
        }()
        let features = on
        let sender = ErrorReportSender(queue: q)
        let first = Task { await sender.flush(features: features, api: api) }
        let deadline = Date().addingTimeInterval(5)
        while await held.count == 0, Date() < deadline { await Task.yield() }
        #expect(await sender.flush(features: features, api: api) == ErrorReportSender.Result())
        await held.release()
        #expect(await first.value.sent == 1)
        #expect(await held.count == 1) // the report went once
        #expect(q.pending().isEmpty)
    }

    // MARK: Reports

    @Test func aReportCarriesTheAppAndTheSystemButNothingOfTheDevice() {
        let r = ErrorReports.report(kind: "crash", level: "fatal", message: "m", at: now, previousLaunch: true)
        #expect(r.source == ErrorReports.source && ["ios", "mac", "watch", "vision"].contains(r.source))
        #expect(r.time == "2027-01-15T08:00:00Z")
        #expect(r.release == Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String)
        #expect(r.build == Bundle.main.object(forInfoDictionaryKey: "CFBundleVersion") as? String)
        #expect(["ios", "macos", "watchos", "visionos"].contains(r.os ?? ""))
        #expect(!(r.osVersion ?? "").isEmpty && !(r.deviceModel ?? "").isEmpty)
        #expect(r.deviceModel != ProcessInfo.processInfo.hostName)
        #expect(r.previousLaunch == true)
        let raw = String(decoding: (try? JSON.encoder.encode(r)) ?? Data(), as: UTF8.self)
        #expect(raw.contains("\"os_version\"") && raw.contains("\"device_model\"") && raw.contains("\"previous_launch\":true"))
    }

    @Test func callStackFramesGoInnermostLastAndMarkPersonasOwn() {
        let symbols = [
            "0   CoreFoundation                      0x00000001804b4f20 __exceptionPreprocess + 164",
            "1   libobjc.A.dylib                     0x0000000180087c3c objc_exception_throw + 56",
            "2   Persona.debug.dylib                 0x0000000104f1c2a8 $s7Persona8AppModelC5startyyF + 120",
            "3   PersonaKit                          0x0000000105000010 0x105000000 + 16",
        ]
        let frames = ErrorReports.frames(callStackSymbols: symbols, appImages: ["Persona", "PersonaKit"])
        #expect(frames.map(\.function) == ["0x105000000 + 16", "$s7Persona8AppModelC5startyyF", "objc_exception_throw", "__exceptionPreprocess"])
        #expect(frames.map(\.module) == ["PersonaKit", "Persona.debug.dylib", "libobjc.A.dylib", "CoreFoundation"])
        #expect(frames.map(\.inApp) == [true, true, false, false])
        #expect(frames.allSatisfy { $0.file == nil })
    }

    @Test func aLongCallStackKeepsTheFramesNearestTheThrow() {
        let symbols = (0..<100).map { "\($0)   Lib   0x0000000100000000 f\($0) + 1" }
        let frames = ErrorReports.frames(callStackSymbols: symbols, appImages: [])
        #expect(frames.count == ErrorReports.maxFrames)
        #expect(frames.last?.function == "f0" && frames.first?.function == "f\(ErrorReports.maxFrames - 1)")
    }

    @Test func anUncaughtExceptionBecomesAFatalCrashForTheNextLaunch() {
        let e = NSException(name: .rangeException, reason: "index 5 beyond bounds", userInfo: nil)
        let r = ErrorReports.report(exception: e, callStack: ["0   Persona   0x0000000100000000 f + 1"], at: now)
        #expect(r.kind == "crash" && r.level == "fatal" && r.previousLaunch == true)
        #expect(r.message == "NSRangeException")
        #expect(r.exceptions?.first?.type == "NSRangeException")
        #expect(r.exceptions?.first?.value == "index 5 beyond bounds")
        #expect(r.exceptions?.first?.frames?.first?.inApp == true)
        let long = NSException(name: .genericException, reason: String(repeating: "y", count: 4000), userInfo: nil)
        #expect((ErrorReports.report(exception: long, callStack: [], at: now).exceptions?.first?.value?.utf8.count ?? 0) <= ErrorReports.maxText)
    }
}

/// A transport whose first request waits for `release`; later ones go through at once.
private actor HeldPosts {
    private(set) var count = 0
    private var held: CheckedContinuation<Void, Never>?

    func arrive() async {
        count += 1
        if count == 1 { await withCheckedContinuation { held = $0 } }
    }

    func release() {
        held?.resume()
        held = nil
    }
}
