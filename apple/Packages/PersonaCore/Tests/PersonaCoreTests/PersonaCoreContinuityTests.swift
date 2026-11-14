import XCTest
import Synchronization
@testable import PersonaCore
import PersonaCoreTestSupport

final class PersonaCoreContinuityTests: XCTestCase {
    func testEveryFixtureThroughTheRealCBinding() throws {
        let result = try ContinuityBindingReplay.replayAll()
        XCTAssertEqual(result.cases, 38)
        XCTAssertEqual(result.steps, 94)
        print("PersonaCore C binding: \(result.cases) cases, \(result.steps) steps, \(result.patches) drained patches")
    }

    func testRepeatedItemUsesUpdateAndIndependentChatWakes() throws {
        let changes = Mutex<[String: [UInt64]]>([:])
        let client = try PersonaCoreClient(config: Config(), callbacks: PersonaCoreCallbacks(changed: { version in
            // The version is global but does not identify a chat. This smoke test uses the drain below for each chat;
            // two callbacks before either drain proves the old global wake loss cannot reappear.
            changes.withLock { $0["wake", default: []].append(version) }
        }, changedChat: { chat, version in
            changes.withLock { $0["wake", default: []].append(version) }
        }, run: { _ in }))
        for chat in ["a", "b"] {
            try client.dispatch(.openChat(chat: chat, snapshot: ChatSnapshot(
                itemsJson: #"[{"id":"1#0","seq":1,"role":"assistant","kind":"text","text":"old"}]"#,
                history: "h", covered: 1, hasMore: false, loadedOnce: true
            )))
        }
        try client.flush()
        XCTAssertEqual(changes.withLock { $0["wake"]?.count }, 2)
        for chat in ["a", "b"] {
            let patch = try client.drain(chat: chat)
            XCTAssertEqual(patch.ops.count, 1)
        }
        let newest = WireBatch(
            itemsJson: #"[{"id":"1#0","seq":1,"role":"assistant","kind":"text","text":"newest"}]"#,
            history: "h", fromSeq: 0, toSeq: 1, backlog: false, hasMore: nil, moreAfter: false
        )
        try client.dispatch(.chat(chat: "a", event: .pushed(batch: newest)))
        try client.flush()
        let patch = try client.drain(chat: "a")
        guard patch.ops.count == 1, case let .update(index: index, id: id, json: json) = patch.ops[0] else {
            return XCTFail("a newer full item must be one Update, got \(patch.ops)")
        }
        XCTAssertEqual(index, 0)
        XCTAssertEqual(id, "1#0")
        XCTAssertTrue(json.contains("newest"))
        client.close()
    }

    func testChatTargetedWakeKeepsIndependentChatIdentity() throws {
        let wakes = Mutex<[(String, UInt64)]>([])
        let client = try PersonaCoreClient(config: Config(), callbacks: PersonaCoreCallbacks(changedChat: { chat, version in
            wakes.withLock { $0.append((chat, version)) }
        }, run: { _ in }))
        for chat in ["a", "b"] {
            try client.dispatch(.openChat(chat: chat, snapshot: ChatSnapshot(
                itemsJson: #"[{"id":"1#0","seq":1,"role":"assistant","kind":"text","text":"old"}]"#,
                history: "h", covered: 1, hasMore: false, loadedOnce: true
            )))
        }
        try client.flush()
        XCTAssertEqual(wakes.withLock { $0.map(\.0) }, ["a", "b"])
        let patch = try client.drain(chat: "a")
        XCTAssertEqual(patch.ops.count, 1)
        try client.dispatch(.chat(chat: "b", event: .pushed(batch: WireBatch(
            itemsJson: #"[{"id":"1#0","seq":1,"role":"assistant","kind":"text","text":"newest"}]"#,
            history: "h", fromSeq: 0, toSeq: 1, backlog: false, hasMore: nil, moreAfter: false
        ))))
        try client.flush()
        XCTAssertEqual(wakes.withLock { $0.last?.0 }, "b")
        XCTAssertEqual(try client.drain(chat: "b").ops.count, 2)
        client.close()
    }

    func testLegacyChangedCallbackStillRunsWhenTargetedWakeIsAbsent() throws {
        let versions = Mutex<[UInt64]>([])
        let client = try PersonaCoreClient(config: Config(), callbacks: PersonaCoreCallbacks(changed: { version in
            versions.withLock { $0.append(version) }
        }, run: { _ in }))
        try client.dispatch(.openChat(chat: "legacy", snapshot: ChatSnapshot(
            itemsJson: "[]", history: nil, covered: 0, hasMore: nil, loadedOnce: false
        )))
        try client.flush()
        XCTAssertEqual(versions.withLock { $0.count }, 1)
        client.close()
    }

    func testCallbackErrorAppearsInItsFlushAndMainThreadCloseReturns() async throws {
        let client = try PersonaCoreClient(config: Config(), callbacks: PersonaCoreCallbacks(run: { _ in
            struct CallbackFailure: Error {}
            throw CallbackFailure()
        }))
        try client.dispatch(.openChat(chat: "failure", snapshot: ChatSnapshot(
            itemsJson: #"[{"id":"1#0","seq":1,"role":"assistant","kind":"text","text":"old"}]"#,
            history: "h", covered: 1, hasMore: false, loadedOnce: true
        )))
        try client.dispatch(.chat(chat: "failure", event: .pushed(batch: WireBatch(
            itemsJson: #"[{"id":"3#0","seq":3,"role":"assistant","kind":"text","text":"new"}]"#,
            history: "h", fromSeq: 2, toSeq: 3, backlog: false, hasMore: nil, moreAfter: false
        ))))
        do {
            try client.flush()
            XCTFail("the effect callback error must be reported by its flush")
        } catch let PersonaCoreError.callbackFailed(reason) {
            XCTAssertFalse(reason.isEmpty)
        }
        await MainActor.run { client.close() }
        try await Task.sleep(for: .milliseconds(100))
        var temporary: PersonaCoreClient? = try PersonaCoreClient(config: Config(), callbacks: PersonaCoreCallbacks(run: { _ in }))
        XCTAssertNotNil(temporary)
        await MainActor.run { temporary = nil }
        try await Task.sleep(for: .milliseconds(100))
    }
}
