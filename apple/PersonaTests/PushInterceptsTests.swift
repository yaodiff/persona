import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct PushInterceptsTests {
    private let now = ISO8601DateFormatter().date(from: "2026-11-14T04:00:00Z")!
    private var shanghai: Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "Asia/Shanghai")!
        return calendar
    }

    private func decode<T: Decodable>(_ json: [String: Any]) -> T {
        try! JSON.decoder.decode(T.self, from: JSONSerialization.data(withJSONObject: json))
    }

    private func record(_ at: String, level: String = "P1", reason: String = "ln_p1_held", title: String? = nil,
                        body: String? = nil, related: String? = nil, also: [String]? = nil,
                        todo: String? = nil, machine: String? = nil, session: String? = nil, url: String? = nil) -> PushIntercept {
        var json: [String: Any] = ["at": at, "level": level, "reason": reason]
        if let title { json["title"] = title }
        if let body { json["body"] = body }
        if let related { json["related_key"] = related }
        if let also { json["also"] = also }
        if let todo { json["todo_id"] = todo }
        if let machine { json["machine_id"] = machine }
        if let session { json["session_id"] = session }
        if let url { json["url"] = url }
        return decode(json)
    }

    private func response(_ records: [PushIntercept]) -> PushInterceptsResponse {
        let data = try! JSONEncoder().encode(["intercepts": records])
        return try! JSON.decoder.decode(PushInterceptsResponse.self, from: data)
    }

    @Test func localDayGroupingAndSummaryFallback() {
        let records = [
            record("2026-11-13T16:30:00Z", title: "Today locally", also: ["Merged title"]),
            record("2026-11-13T15:59:59Z", title: "Yesterday locally"),
            record("2026-11-14T01:00:00Z", level: "P2", reason: "ln_dedup_12h", body: "P2 dedup"),
            record("2026-11-14T01:01:00Z", reason: "ln_dedup_12h", title: "P1 dedup"),
            record("2026-11-14T01:02:00Z", level: "P0", reason: "ln_dedup_12h", title: "Already alerted"),
            record("invalid", title: "Bad date"),
        ]
        let groups = PushInterceptsLogic.groups(response(records), now: now, calendar: shanghai)
        #expect(groups.p1.map(\.summaries) == [["Today locally", "Merged title"], ["P1 dedup"]])
        #expect(groups.p2.map(\.summaries) == [["P2 dedup"]])
        #expect(groups.p1.first?.summaries == ["Today locally", "Merged title"])
        #expect(PushInterceptsLogic.summaries(record("2026-11-14T01:00:00Z", title: nil, body: " Body\nonly ")) == ["Body only"])
    }

    @Test func westernMidnightAndDST() {
        var zone = Calendar(identifier: .gregorian)
        zone.timeZone = TimeZone(identifier: "America/New_York")!
        let western = PushInterceptsLogic.groups(response([
            record("2026-11-14T03:30:00Z", title: "November 13 locally"),
            record("2026-11-14T05:00:00Z", title: "November 14 locally"),
        ]), now: now, calendar: zone)
        #expect(western.p1.map(\.summaries) == [["November 13 locally"]])

        let dstNow = ISO8601DateFormatter().date(from: "2026-03-08T18:00:00Z")!
        let dst = PushInterceptsLogic.groups(response([
            record("2026-03-08T07:30:00Z", title: "Before spring forward"),
            record("2026-03-08T08:30:00Z", level: "P2", reason: "ln_p2_silent", title: "After spring forward"),
        ]), now: dstNow, calendar: zone)
        #expect(dst.p1.count == 1 && dst.p2.count == 1)
    }

    @Test func stableIDsIgnoreMergedTitlesAndDistinguishDuplicates() {
        let first = record("2026-11-14T02:00:00Z", title: "Same")
        let before = PushInterceptsLogic.groups(response([first, first]), now: now, calendar: shanghai)
        let merged = PushInterceptsLogic.groups(response([
            record("2026-11-14T03:00:00Z", title: "New"),
            record("2026-11-14T02:00:00Z", title: "Changed", also: ["Merged"]), first,
        ]), now: now, calendar: shanghai)
        #expect(merged.p1[1].id == before.p1[0].id)
        #expect(merged.p1[2].id == before.p1[1].id)
        #expect(Set(merged.p1.map(\.id)).count == 3)
    }

    @Test func destinationsAreTypedOrBoundedLegacyOnly() {
        let known: Set<String> = ["todo_1"]
        #expect(PushInterceptsLogic.destination(record("2026-11-14T02:00:00Z", todo: "todo_1"), knownTodoIDs: known) == .todo("todo_1"))
        #expect(PushInterceptsLogic.destination(record("2026-11-14T02:00:00Z", machine: "mac", session: "s1"), knownTodoIDs: known) ==
            .session(group: "", machineID: "mac", sessionID: "s1"))
        #expect(PushInterceptsLogic.destination(record("2026-11-14T02:00:00Z", related: "todo:todo_1@2026-11-14T00:00:00Z"), knownTodoIDs: known) == .todo("todo_1"))
        #expect(PushInterceptsLogic.destination(record("2026-11-14T02:00:00Z", related: "https://example.com/pull/1"), knownTodoIDs: known) ==
            .url(URL(string: "https://example.com/pull/1")!))
        for item in [
            record("2026-11-14T02:00:00Z", todo: "missing"),
            record("2026-11-14T02:00:00Z", related: "session-id-only"),
            record("2026-11-14T02:00:00Z", related: "ix:request-1"),
            record("2026-11-14T02:00:00Z", related: "notify:assistant/ext/id"),
            record("2026-11-14T02:00:00Z", related: "javascript:alert(1)"),
            record("2026-11-14T02:00:00Z", machine: "mac"),
        ] {
            #expect(PushInterceptsLogic.destination(item, knownTodoIDs: known) == nil)
        }
    }

    @Test func offModeDoesNotRequestIntercepts() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/push/ln-mode", json: #"{"enabled":false}"#)
        let model = PushInterceptsModel(api: hub.client)
        await model.refresh()
        #expect(!model.enabled)
        #expect(hub.sent("GET", "/api/v1/push/intercepts").isEmpty)
    }

    @Test func enabledModeReadsRowsAndInterceptsFailureKeepsRetry() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/push/ln-mode", json: #"{"enabled":true}"#)
        hub.on("GET", "/api/v1/push/intercepts", json: #"{"intercepts":[]}"#)
        let model = PushInterceptsModel(api: hub.client)
        await model.refresh()
        #expect(model.enabled && !model.failed)
        #expect(hub.sent("GET", "/api/v1/push/intercepts").count == 1)

        let failing = FakeHub()
        failing.on("GET", "/api/v1/push/ln-mode", json: #"{"enabled":true}"#)
        let failedModel = PushInterceptsModel(api: failing.client)
        await failedModel.refresh()
        #expect(failedModel.enabled && failedModel.failed)
    }

    @Test func olderHubHidesTheEntry() async {
        let hub = FakeHub()
        let model = PushInterceptsModel(api: hub.client)
        await model.refresh()
        #expect(!model.enabled && model.failed)
    }

    @Test func lateResponseCannotOverwriteNewerRead() async {
        let old = response([record("2026-11-14T02:00:00Z", title: "Old")])
        let new = response([record("2026-11-14T03:00:00Z", title: "New")])
        let counter = LockedCounter()
        let model = PushInterceptsModel(mode: { LnModeResponse(enabled: true) }, intercepts: {
            if counter.next() == 1 {
                try? await Task.sleep(for: .milliseconds(150))
                return old
            }
            return new
        })
        async let first: Void = model.refresh()
        await model.refresh()
        await first
        #expect(model.groups.p1.first?.summaries.first == "New")
    }
}

private final class LockedCounter: @unchecked Sendable {
    private let lock = NSLock()
    private var value = 0
    func next() -> Int {
        lock.withLock { value += 1; return value }
    }
}
