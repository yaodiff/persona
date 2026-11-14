import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor struct LarkChatPinsTests {
    private func pin(_ id: String) -> LarkChatPin { .init(id: id, messageID: "m" + id) }
    private func page(_ ids: [String], next: String? = nil, owner: String = "one") -> LarkChatPinsPage {
        .init(accountID: owner, items: ids.map(pin), nextCursor: next, total: ids.count)
    }
    private func model(page: @escaping (String?, String?) async throws -> LarkChatPinsPage,
        change: @escaping (String, String?, String) async throws -> Void = { _, _, _ in }) -> LarkChatPinsModel {
        LarkChatPinsModel(account: "one", deps: .init(page: page, change: change, locate: { _, _ in throw URLError(.badServerResponse) }))
    }

    @Test func onlyReadsOnExplicitOpenAndMergesPages() async {
        var reads = 0
        let pins = model { owner, cursor in
            #expect(owner == "one"); reads += 1
            return cursor == nil ? page(["1", "2"], next: "50") : page(["2", "3"])
        }
        #expect(reads == 0)
        await pins.refresh(); await pins.more()
        #expect(pins.items.map(\.id) == ["1", "2", "3"])
        #expect(reads == 2 && pins.nextCursor == nil)
    }

    @Test func refreshFailurePreservesCompleteWindow() async {
        var fail = false
        let pins = model { _, cursor in
            if fail && cursor != nil { throw URLError(.notConnectedToInternet) }
            return page([cursor ?? (fail ? "new" : "old")], next: cursor == nil ? "50" : nil)
        }
        await pins.refresh(); await pins.more()
        fail = true; await pins.refresh()
        #expect(pins.items.map(\.id) == ["old", "50"])
        #expect(pins.problem != nil && !pins.loading)
    }

    @Test func accountChangeClearsAndBlocksInsteadOfMerging() async {
        var owner = "one"
        let pins = model { _, _ in page([owner], owner: owner) }
        await pins.refresh(); owner = "two"; await pins.refresh()
        #expect(pins.items.isEmpty && pins.blocked)
        #expect(pins.problem?.contains("账号已切换") == true)
    }

    @Test func paginationCycleIsFailureAndKeepsPriorRows() async {
        let pins = model { _, cursor in page([cursor ?? "first"], next: cursor == nil ? "50" : cursor == "50" ? "100" : "50") }
        await pins.refresh(); await pins.more(); await pins.more()
        #expect(pins.items.map(\.id) == ["first", "50"])
        #expect(pins.pageProblem != nil)
    }

    @Test func unknownMutationIsNotRetriedAndRequiresRead() async {
        var writes = 0
        let pins = model(page: { _, _ in page(["1"]) }, change: { _, _, _ in
            writes += 1
            throw APIError(status: 502, message: "结果未知", fields: ["code": "lark_pin_result_unknown"])
        })
        await pins.refresh()
        #expect(await pins.change(message: "m1", pin: "1") == false)
        #expect(pins.requiresRecheck && pins.items.count == 1)
        #expect(await pins.change(message: "m1", pin: "1") == false)
        #expect(writes == 1)
        await pins.refresh()
        #expect(!pins.requiresRecheck)
    }

    @Test func successfulMutationRefreshesAndNeverDropsTheListOnFailure() async {
        var writes = 0, after = false
        let pins = model(page: { _, _ in
            if after { throw URLError(.timedOut) }
            return page(["1"])
        }, change: { message, pin, account in
            #expect(message == "m1" && pin == "1" && account == "one")
            writes += 1; after = true
        })
        await pins.refresh()
        #expect(await pins.change(message: "m1", pin: "1"))
        #expect(writes == 1 && pins.items.count == 1 && pins.problem != nil && pins.requiresRecheck)
    }

    @Test func stoppedPanelCannotPublishLateRead() async {
        var gate: CheckedContinuation<Void, Never>?
        let pins = model { _, _ in
            await withCheckedContinuation { gate = $0 }
            return page(["late"])
        }
        let task = Task { await pins.refresh() }
        while gate == nil { await Task.yield() }
        pins.stop(); gate?.resume(); await task.value
        #expect(pins.items.isEmpty && !pins.loading)
    }

    @Test func eventDuringReadIsCoalescedAndRefetched() async {
        var gate: CheckedContinuation<Void, Never>?, reads = 0
        let pins = model { _, _ in
            reads += 1
            if reads == 1 { await withCheckedContinuation { gate = $0 } }
            return page([String(reads)])
        }
        let task = Task { await pins.refresh() }
        while gate == nil { await Task.yield() }
        await pins.refresh(); gate?.resume(); await task.value
        #expect(reads == 2 && pins.items.first?.id == "2")
    }

    @Test func oldHubIsActionableButMissingMessageIsNotAnUpgrade() async throws {
        let hub = FakeHub()
        let path = "/api/v1/lark/conversations/123/pins"
        hub.on("GET", path) { _ in throw APIError(status: 404, message: "not found") }
        do { _ = try await hub.client.larkChatPins("123", account: "one", cursor: nil); Issue.record("missing route accepted") }
        catch let e as APIError { #expect(e.code == "lark_hub_upgrade_required" && e.message.contains("升级 Hub")) }
        hub.on("GET", path) { _ in throw APIError(status: 404, message: "unavailable", fields: ["code": "lark_pin_not_found"]) }
        do { _ = try await hub.client.larkChatPins("123", account: "one", cursor: nil); Issue.record("missing data accepted") }
        catch let e as APIError { #expect(e.code == "lark_pin_not_found") }
    }

    @Test func pinEncodingUsesExistingEncryptedMessageProtection() throws {
        let data = Data(#"{"id":"1","message_id":"2","message":{"id":"2","position":"5","sender_name":"Test","is_mine":false,"type":"TEXT","text":"private marker","recalled":false,"reply_count":0,"can_pin":true,"can_unpin":false,"thread_id":"4","thread_kind":"reply","olk":{"version":1,"state":"decrypted","bodyParts":[{"kind":"text","text":"private marker"}],"publicParts":[{"kind":"text","text":"public cover"}]}}}"#.utf8)
        let pin = try JSON.decoder.decode(LarkChatPin.self, from: data)
        #expect(pin.message?.displayText == "private marker" && !pin.canRemove)
        let encoded = try JSON.encoder.encode(pin)
        #expect(!String(decoding: encoded, as: UTF8.self).contains("private marker"))
        let restored = try JSON.decoder.decode(LarkChatPin.self, from: encoded)
        #expect(restored.message?.threadID == "4" && restored.message?.canPin == true)
        #expect(restored.message?.olk?.isOpen == false && restored.message?.displayText == "public cover")
    }
}
