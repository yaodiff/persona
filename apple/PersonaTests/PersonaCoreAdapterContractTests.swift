import Foundation
import PersonaCore
import Synchronization
import Testing
@testable import Persona

private actor AdapterItemResponses {
    enum Failure: Error { case timeout, wrongRequest, cancelled }
    private var started = 0
    private var pending: [(query: [String: String], resume: CheckedContinuation<Data, Error>)] = []

    func answer(_ request: LocalRequest) async throws -> Data {
        guard request.path.hasSuffix("/items"), request.query["through_seq"] != nil else {
            throw Failure.wrongRequest
        }
        started += 1
        let query = request.query
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { resume in
                pending.append((query, resume))
            }
        } onCancel: {
            Task { await cancel() }
        }
    }

    func cancel() {
        for request in pending { request.resume.resume(throwing: Failure.cancelled) }
        pending.removeAll()
    }

    func waitForRequests(_ expected: Int) async throws {
        for _ in 0..<200 {
            if started == expected { return }
            try await Task.sleep(for: .milliseconds(10))
        }
        await cancel()
        throw Failure.timeout
    }

    func releaseFold(_ seq: Int) throws {
        guard let index = pending.firstIndex(where: { $0.query["from_seq"] == String(seq) }) else {
            Task { await cancel() }
            throw Failure.wrongRequest
        }
        let request = pending.remove(at: index)
        let through = Int(request.query["through_seq"] ?? "") ?? seq
        let items = (seq...through).map { id in
            ["id": "\(id)#0", "seq": id, "role": "assistant", "kind": "tool_use", "text": "Synthetic fold work"]
        }
        do {
            request.resume.resume(returning: try JSONSerialization.data(withJSONObject: ["items": items, "more": false]))
        } catch {
            Task { await cancel() }
            throw error
        }
    }
}

private actor AdapterReloadResponses {
    enum Failure: Error { case timeout, wrongRequest, cancelled }
    private var started = 0
    private var pending: [CheckedContinuation<Data, Error>] = []

    func answer(_ request: LocalRequest) async throws -> Data {
        guard request.path.hasSuffix("/items"), request.query["through_seq"] == nil, request.query["before_seq"] == nil else {
            throw Failure.wrongRequest
        }
        started += 1
        return try await withTaskCancellationHandler {
            try await withCheckedThrowingContinuation { pending.append($0) }
        } onCancel: {
            Task { await cancel() }
        }
    }

    func cancel() {
        for resume in pending { resume.resume(throwing: Failure.cancelled) }
        pending.removeAll()
    }

    func waitForRequests(_ expected: Int) async throws {
        for _ in 0..<200 {
            if started == expected { return }
            try await Task.sleep(for: .milliseconds(10))
        }
        await cancel()
        throw Failure.timeout
    }

    func release(_ index: Int, seq: Int) throws {
        guard pending.indices.contains(index) else {
            Task { await cancel() }
            throw Failure.wrongRequest
        }
        let resume = pending.remove(at: index)
        do {
            resume.resume(returning: try JSONSerialization.data(withJSONObject: [
                "items": [["id": "\(seq)#0", "seq": seq, "role": "assistant", "kind": "text", "text": "Synthetic reload"]],
                "history": "h", "to_seq": seq, "has_more": false,
            ]))
        } catch {
            Task { await cancel() }
            throw error
        }
    }
}

/// Tests the iOS adapter contract through the real AppModel scheduler and APIClient decoder. Responses are manually
/// released so concurrent folds, overlapping reloads and subscription ownership do not depend on network timing.
@MainActor
struct PersonaCoreAdapterContractTests {
    private func item(_ seq: Int, kind: ItemKind = .toolUse) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: "assistant", kind: kind, text: "Synthetic adapter fixture",
             toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)
    }

    private func fold(_ seq: Int, through: Int) -> Item {
        var item = item(seq, kind: .fold)
        item.id = "fold:\(seq)#0"
        item.fold = Fold(items: through - seq + 1, tools: through - seq + 1, thoughts: 0, until: nil,
                         throughSeq: through, throughID: "\(through)#0")
        return item
    }

    private func testFolder() throws -> URL {
        let folder = FileManager.default.temporaryDirectory.appending(path: "adapter-contract-\(UUID())", directoryHint: .isDirectory)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        return folder
    }

    private func foldModel() throws -> (AppModel, SessionKey, ChatStore, AdapterItemResponses) {
        let model = AppModel(stateFolder: try testFolder())
        model.linksUpForTests = false
        model.connectOnly(.demo)
        let key = SessionKey(machineID: "fixture", sessionID: "adapter-concurrent-folds-\(UUID())")
        let store = model.chat(key)
        let responses = AdapterItemResponses()
        var client = APIClient(config: ServerConfig(url: URL(string: "https://fake.invalid")!, token: "test-only"))
        client.transport = { request in try await responses.answer(request) }
        model.apiForTests = client
        return (model, key, store, responses)
    }

    @Test func independentFoldsCompleteInEitherOrderWithoutLosingWork() async throws {
        let (model, key, store, responses) = try foldModel()
        store.merge([fold(2, through: 3), fold(5, through: 6)], backlog: true)
        async let first: Void = model.unfold(key, fold(2, through: 3))
        async let second: Void = model.unfold(key, fold(5, through: 6))
        try await responses.waitForRequests(2)
        try await responses.releaseFold(5)
        try await second
        #expect(store.items.map(\.id) == ["fold:2#0", "5#0", "6#0"])
        try await responses.releaseFold(2)
        try await first
        #expect(store.items.map(\.seq) == [2, 3, 5, 6])
        #expect(store.items.allSatisfy { $0.kind != .fold })
    }

    @Test func repeatedOpenOfOneFoldSharesItsFetch() async throws {
        let (model, key, store, responses) = try foldModel()
        let fold = fold(2, through: 3)
        store.merge([fold], backlog: true)
        async let first: Void = model.unfold(key, fold)
        async let second: Void = model.unfold(key, fold)
        try await responses.waitForRequests(1)
        try await responses.releaseFold(2)
        _ = try await (first, second)
        #expect(store.items.map(\.seq) == [2, 3])
    }

    @Test func overlappingReloadsPreserveNewerPushesAndFinishInResponseOrder() async throws {
        let model = AppModel(stateFolder: try testFolder())
        model.linksUpForTests = false
        model.connectOnly(.demo)
        let key = SessionKey(machineID: "fixture", sessionID: "adapter-overlapping-reloads-\(UUID())")
        let store = model.chat(key)
        store.apply([item(1, kind: .text)], span: ItemSpan(history: "h", from: nil, to: 1), backlog: true, hasMore: false)
        let responses = AdapterReloadResponses()
        var client = APIClient(config: ServerConfig(url: URL(string: "https://fake.invalid")!, token: "test-only"))
        client.transport = { request in try await responses.answer(request) }
        model.apiForTests = client

        async let first: Void = model.reload(key)
        try await responses.waitForRequests(1)
        _ = store.apply([item(4, kind: .text)], span: ItemSpan(history: "h", from: 1, to: 4))
        #expect(store.covered == 4)
        async let second: Void = model.reload(key)
        try await responses.waitForRequests(2)
        try await responses.release(0, seq: 2)
        await first
        #expect(store.items.map(\.seq) == [2, 4])
        #expect(store.covered == 2)
        try await responses.release(0, seq: 3)
        await second
        #expect(store.items.map(\.seq) == [3, 4])
        #expect(store.covered == 3)
    }

    @Test func subscriptionOwnersDoNotCancelEachOther() throws {
        let model = AppModel(stateFolder: try testFolder())
        model.linksUpForTests = false
        let sent = Mutex<[String]>([])
        model.linkHooksForTests = (
            transport: { _ in Data(#"{"items":[],"has_more":false}"#.utf8) },
            socket: {
                let socket = SyncSocket()
                socket.onReceive = { message in
                    if let type = message["type"] as? String { sent.withLock { $0.append(type) } }
                }
                return socket
            }
        )
        model.connectOnly(.demo)
        let key = SessionKey(machineID: "fixture", sessionID: "adapter-subscription-owners-\(UUID())")
        let first = UUID(), second = UUID()
        model.subscribe(key, by: first)
        model.subscribe(key, by: second)
        model.unsubscribe(key, by: first)
        #expect(sent.withLock { $0.filter { $0 == "subscribe" }.count } == 1)
        #expect(sent.withLock { !$0.contains("unsubscribe") })
        #expect(model.isSubscribed(key, by: second))
        model.unsubscribe(key, by: second)
        #expect(sent.withLock { $0.filter { $0 == "unsubscribe" }.count } == 1)
    }
}
