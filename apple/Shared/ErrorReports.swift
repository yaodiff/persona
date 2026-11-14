import Foundation
import PersonaWire

/// The app's errors and crashes for the hub's error reports (#5621, docs/protocol/protocol.md "Error reports"): sent only while
/// the hub's `features` has `error_reports` (an extension such as Sentry takes them). A report says what failed where
/// and on which build and system; never what the user wrote, a device id, its name or an account.
nonisolated enum ErrorReports {
    static let feature = "error_reports"
    static let path = "/api/v1/error-reports"
    /// The hub's field limits (it cuts longer ones): a message 1 KB, 64 frames an exception.
    static let maxText = 1024
    static let maxFrames = 64

    #if os(watchOS)
    static let source = "watch", os = "watchos"
    #elseif os(visionOS)
    static let source = "vision", os = "visionos"
    #elseif os(macOS)
    static let source = "mac", os = "macos"
    #else
    static let source = "ios", os = "ios"
    #endif

    /// RFC 3339 UTC, whole seconds.
    static func timestamp(_ date: Date) -> String { date.formatted(.iso8601) }

    static func date(_ timestamp: String) -> Date? { try? Date(timestamp, strategy: .iso8601) }

    /// A report with a new id (a retry sends this one again), the app's version and build, and the system's.
    static func report(kind: String, level: String, message: String?, at: Date = Date(), previousLaunch: Bool = false,
                       exceptions: [ExtensionErrorException]? = nil) -> ExtensionErrorReport {
        let info = Bundle.main.infoDictionary
        return ExtensionErrorReport(id: UUID().uuidString.lowercased(), source: source, kind: kind, level: level,
                                    time: timestamp(at), release: info?["CFBundleShortVersionString"] as? String,
                                    build: info?["CFBundleVersion"] as? String, os: os, osVersion: osVersion,
                                    deviceModel: deviceModel, message: message.map { clip($0, maxText) },
                                    exceptions: exceptions, previousLaunch: previousLaunch ? true : nil)
    }

    /// An uncaught exception: the app dies right after, so it goes at the next launch.
    static func report(exception: NSException, callStack: [String], at: Date = Date()) -> ExtensionErrorReport {
        let name = exception.name.rawValue
        let thrown = ExtensionErrorException(type: name, value: exception.reason.map { clip($0, maxText) },
                                             frames: frames(callStackSymbols: callStack))
        return report(kind: "crash", level: "fatal", message: name, at: at, previousLaunch: true, exceptions: [thrown])
    }

    /// The images that are Persona's own code: the app's executable (`Persona.debug.dylib` in a debug build too) and
    /// its frameworks.
    static let appImages: Set<String> = {
        var names: Set<String> = ["PersonaKit", "PersonaSSH"]
        if let exe = Bundle.main.executableURL?.lastPathComponent { names.insert(exe) }
        return names
    }()

    /// `callStackSymbols` (innermost first: "2   Persona   0x0000000104f1c2a8 symbol + 120") as frames innermost last,
    /// the nearest `maxFrames` to the throw. Not symbolicated on the device: the symbol, or the image's address and
    /// offset, is the function and the image the module; there is no file.
    static func frames(callStackSymbols: [String], appImages: Set<String> = appImages) -> [ExtensionErrorFrame] {
        callStackSymbols.prefix(maxFrames).compactMap { line -> ExtensionErrorFrame? in
            let parts = line.split(separator: " ", maxSplits: 3, omittingEmptySubsequences: true)
            guard parts.count == 4 else { return nil }
            let module = String(parts[1])
            var function = String(parts[3])
            if !function.hasPrefix("0x"), let plus = function.range(of: " + ", options: .backwards) {
                function = String(function[..<plus.lowerBound])
            }
            let own = appImages.contains { module == $0 || module.hasPrefix($0 + ".") }
            return ExtensionErrorFrame(function: clip(function, 256), module: module, inApp: own)
        }.reversed()
    }

    static var osVersion: String {
        let v = ProcessInfo.processInfo.operatingSystemVersion
        return "\(v.majorVersion).\(v.minorVersion).\(v.patchVersion)"
    }

    /// The hardware model identifier only (iPhone17,1, Mac15,3): the simulator's is the one it simulates.
    static let deviceModel: String = {
        if let sim = ProcessInfo.processInfo.environment["SIMULATOR_MODEL_IDENTIFIER"], !sim.isEmpty { return sim }
        #if os(macOS)
        var size = 0
        guard sysctlbyname("hw.model", nil, &size, nil, 0) == 0, size > 0 else { return "Mac" }
        var bytes = [CChar](repeating: 0, count: size)
        guard sysctlbyname("hw.model", &bytes, &size, nil, 0) == 0 else { return "Mac" }
        return String(decoding: bytes.prefix { $0 != 0 }.map { UInt8(bitPattern: $0) }, as: UTF8.self)
        #else
        var info = utsname()
        guard uname(&info) == 0 else { return "" }
        return withUnsafeBytes(of: &info.machine) { String(decoding: $0.prefix { $0 != 0 }, as: UTF8.self) }
        #endif
    }()

    /// s cut to at most `bytes` of UTF-8, on a character boundary.
    static func clip(_ s: String, _ bytes: Int) -> String {
        guard s.utf8.count > bytes else { return s }
        var out = ""
        var used = 0
        for c in s {
            let n = String(c).utf8.count
            if used + n > bytes { break }
            out.append(c)
            used += n
        }
        return out
    }

    // MARK: Uncaught exceptions

    nonisolated(unsafe) private static var crashQueue: ErrorReportQueue?
    nonisolated(unsafe) private static var previousHandler: (@convention(c) (NSException) -> Void)?

    /// Writes an uncaught exception's report to `queue` before the app dies, then hands it on to the handler that
    /// was there (another reporter, the system's). Once, at launch.
    static func installExceptionHandler(queue: ErrorReportQueue) {
        guard crashQueue == nil else { return }
        crashQueue = queue
        previousHandler = NSGetUncaughtExceptionHandler()
        NSSetUncaughtExceptionHandler { exception in
            ErrorReports.crashQueue?.write(ErrorReports.report(exception: exception, callStack: exception.callStackSymbols))
            ErrorReports.previousHandler?(exception)
        }
    }
}

/// The body of `POST /api/v1/error-reports`.
nonisolated struct ErrorReportBatch: Codable, Sendable {
    var reports: [ExtensionErrorReport]
}

/// Reports kept on disk until a hub with `error_reports` takes them: one file a report (`<time ms>-<id>.json`, so the
/// oldest sort first and an age is known without reading it), at most `maxReports`, at most `maxAge` old. A file, not
/// UserDefaults: a crash writes it right before the app dies, and the next launch finds it.
nonisolated final class ErrorReportQueue: @unchecked Sendable {
    static let maxReports = 50
    static let maxAge: TimeInterval = 7 * 24 * 3600

    /// A kept report and its encoded bytes (what a batch carries).
    nonisolated struct Entry: Sendable {
        var file: URL
        var report: ExtensionErrorReport
        var json: Data
    }

    static let shared = ErrorReportQueue(directory: URL.applicationSupportDirectory.appending(path: "error-reports", directoryHint: .isDirectory))

    private let directory: URL
    private let now: @Sendable () -> Date
    private let lock = NSLock()

    init(directory: URL, now: @escaping @Sendable () -> Date = { Date() }) {
        self.directory = directory
        self.now = now
    }

    /// Writes `report` at once (the crash handler calls it right before the app dies); false when it could not.
    @discardableResult
    func write(_ report: ExtensionErrorReport) -> Bool {
        lock.withLock {
            guard let json = try? JSON.encoder.encode(report) else { return false }
            let ms = Int64(((ErrorReports.date(report.time) ?? now()).timeIntervalSince1970 * 1000).rounded(.down))
            let id = String(report.id.filter { $0.isLetter || $0.isNumber || $0 == "-" }.prefix(64))
            do {
                try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
                try json.write(to: directory.appending(path: "\(ms)-\(id).json"), options: .atomic)
            } catch { return false }
            pruneLocked()
            return true
        }
    }

    /// The kept reports, oldest first, after dropping the expired, the unreadable and all but the newest `maxReports`.
    func pending() -> [Entry] {
        lock.withLock {
            pruneLocked()
            return filesLocked().compactMap { file in
                guard let json = try? Data(contentsOf: file), let report = try? JSON.decoder.decode(ExtensionErrorReport.self, from: json),
                      !report.id.isEmpty else {
                    try? FileManager.default.removeItem(at: file)
                    return nil
                }
                return Entry(file: file, report: report, json: json)
            }
        }
    }

    func delete(_ entries: some Sequence<Entry>) {
        lock.withLock { for e in entries { try? FileManager.default.removeItem(at: e.file) } }
    }

    /// The hub takes no reports: nothing kept is ever sent.
    func clear() {
        lock.withLock { for f in filesLocked() { try? FileManager.default.removeItem(at: f) } }
    }

    private func pruneLocked() {
        let cutoff = (now().timeIntervalSince1970 - Self.maxAge) * 1000
        var kept: [URL] = []
        for f in filesLocked() {
            if let ms = Self.millis(f), Double(ms) >= cutoff { kept.append(f) } else { try? FileManager.default.removeItem(at: f) }
        }
        for f in kept.dropLast(Self.maxReports) { try? FileManager.default.removeItem(at: f) }
    }

    private func filesLocked() -> [URL] {
        let all = (try? FileManager.default.contentsOfDirectory(at: directory, includingPropertiesForKeys: nil)) ?? []
        return all.filter { $0.pathExtension == "json" }
            .sorted { (Self.millis($0) ?? .min, $0.lastPathComponent) < (Self.millis($1) ?? .min, $1.lastPathComponent) }
    }

    private static func millis(_ file: URL) -> Int64? {
        file.lastPathComponent.split(separator: "-", maxSplits: 1).first.flatMap { Int64($0) }
    }
}

/// Sends the queue while the hub says `error_reports`, at most `maxBatch` reports and `maxBatchBytes` a request. A 2xx
/// takes the batch (what the hub dropped is not sent again either; `{"enabled": false}` clears the rest); a 4xx refuses
/// it for good, except 408 and 429, which wait for the next try like a 5xx or no answer. One flush at a time: one that
/// finds another going returns at once (the next hello or `machines` message tries again), so a batch never goes twice.
actor ErrorReportSender {
    nonisolated static let maxBatch = 20
    /// Under the hub's 256 KB a request.
    nonisolated static let maxBatchBytes = 240 * 1024
    /// `{"reports":[]}`.
    private nonisolated static let envelope = 14

    nonisolated static let shared = ErrorReportSender(queue: .shared)

    /// What one `flush` did.
    nonisolated struct Result: Sendable, Equatable {
        var sent = 0, dropped = 0, kept = 0
        var cleared = false
    }

    private nonisolated struct Answer: Decodable { var enabled: Bool? }

    let queue: ErrorReportQueue
    private var flushing = false

    init(queue: ErrorReportQueue) { self.queue = queue }

    func flush(features: Set<String>, api: APIClient) async -> Result {
        guard !flushing else { return Result() }
        flushing = true
        defer { flushing = false }
        guard features.contains(ErrorReports.feature) else {
            queue.clear()
            return Result(cleared: true)
        }
        var result = Result()
        var pending = queue.pending()[...]
        while !pending.isEmpty {
            let batch = Self.batch(pending)
            if batch.isEmpty { // one report over the request limit: the hub would refuse it every time
                queue.delete([pending.removeFirst()])
                result.dropped += 1
                continue
            }
            do {
                let data = try await api.post(ErrorReports.path, body: ErrorReportBatch(reports: batch.map(\.report)))
                queue.delete(batch)
                result.sent += batch.count
                if (try? JSON.decoder.decode(Answer.self, from: data))?.enabled == false {
                    queue.clear()
                    result.cleared = true
                    return result
                }
            } catch let e as APIError where (400..<500).contains(e.status) && e.status != 408 && e.status != 429 {
                queue.delete(batch)
                result.dropped += batch.count
            } catch {
                result.kept = pending.count
                return result
            }
            pending.removeFirst(batch.count)
        }
        return result
    }

    private nonisolated static func batch(_ pending: ArraySlice<ErrorReportQueue.Entry>) -> [ErrorReportQueue.Entry] {
        var out: [ErrorReportQueue.Entry] = []
        var bytes = envelope
        for e in pending {
            let size = e.json.count + (out.isEmpty ? 0 : 1)
            if out.count == maxBatch || bytes + size > maxBatchBytes { break }
            out.append(e)
            bytes += size
        }
        return out
    }
}
