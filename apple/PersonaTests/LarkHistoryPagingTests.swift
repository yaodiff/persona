import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Mutating calls stay outside `#expect` / `#require`: the macros capture their
/// operands as immutable values.
@MainActor struct LarkHistoryPagingTests {
    private func page(_ ids: [Int], older: Bool = true, cursor: String? = nil) -> APIClient.LarkMessages {
        let messages = ids.map { id in
            try! JSON.decoder.decode(LarkMessage.self, from: Data("""
            {"id":"m\(id)","position":"\(id)","sender_name":"示例成员","is_mine":false,"type":"TEXT","text":"记录 \(id)","recalled":false,"reply_count":0}
            """.utf8))
        }
        return APIClient.LarkMessages(messages: messages, hasOlder: older, olderCursor: cursor)
    }

    private func begin(_ h: inout LarkHistoryPaging, _ cursor: String?, retry: Bool = false) throws -> LarkHistoryPaging.Request {
        let request = h.begin(cursor: cursor, retry: retry)
        return try #require(request)
    }

    @Test func oneRequestAndExplicitRetryAfterFailure() throws {
        var h = LarkHistoryPaging()
        let request = try begin(&h, "40")
        let second = h.begin(cursor: "40")
        #expect(second == nil)
        h.fail(request, problem: .failed)
        let automatic = h.begin(cursor: "40")
        #expect(automatic == nil)
        let retry = try begin(&h, "40", retry: true)
        #expect(request != retry && h.problem == nil)
        h.fail(request, problem: .failed)
        #expect(h.pending == retry)
    }

    @Test func mergesOverlapAndDuplicateRowsWithoutTouchingTail() throws {
        var p = LarkPager(); p.replace(with: page([40, 41]))
        var h = LarkHistoryPaging()
        let request = try begin(&h, p.olderRequest)
        p.add(page([42]).messages) // A live arrival during the older request.
        let merged = h.finish(request, page: page([38, 38, 39, 40]), pager: &p)
        #expect(merged)
        #expect(p.messages.map(\.id) == ["m38", "m39", "m40", "m41", "m42"])
        #expect(p.newerRequest == "42" && p.olderRequest == "38")
    }

    @Test func emptyDuplicateAndCursorCycleStopAutomaticRequests() throws {
        for response in [page([]), page([40]), page([39], cursor: "40")] {
            var p = LarkPager(); p.replace(with: page([40, 41]))
            var h = LarkHistoryPaging()
            let request = try begin(&h, p.olderRequest)
            h.finish(request, page: response, pager: &p)
            let automatic = h.begin(cursor: p.olderRequest)
            #expect(h.problem != nil && automatic == nil)
            #expect(p.olderRequest == "40")
            let retried = h.begin(cursor: p.olderRequest, retry: true)
            #expect(retried != nil)
        }
    }

    @Test func cycleAcrossSeveralPagesStopsAndFinalEmptyPageIsNormal() throws {
        var p = LarkPager(); p.replace(with: page([40, 41]))
        var h = LarkHistoryPaging()
        h.finish(try begin(&h, "40"), page: page([39]), pager: &p)
        h.finish(try begin(&h, "39"), page: page([38], cursor: "40"), pager: &p)
        #expect(h.problem != nil)
        h.finish(try begin(&h, "39", retry: true), page: page([], older: false), pager: &p)
        #expect(!p.hasOlder && h.problem == nil)
    }

    @Test func leavingOrReplacingWindowRejectsLateResponse() throws {
        var p = LarkPager(); p.replace(with: page([40, 41]))
        var h = LarkHistoryPaging()
        let old = try begin(&h, "40")
        h.cancel()
        let current = try begin(&h, "40")
        let late = h.finish(old, page: page([39]), pager: &p)
        #expect(!late)
        p.replace(with: page([70, 71]))
        let replaced = h.finish(current, page: page([39]), pager: &p)
        #expect(!replaced)
        #expect(p.messages.map(\.id) == ["m70", "m71"])
    }

    @Test func ownerAndCapabilityAreRequired() async throws {
        for owner in ["one", "two", ""] {
            let api = APIClient(config: .demo, transport: { request in
                #expect(request.query["account_id"] == "one" && request.query["before"] == "40")
                return Data("{\"messages\":[],\"has_older\":false\(owner.isEmpty ? "" : ",\"account_id\":\"\(owner)\"")}".utf8)
            })
            do {
                _ = try await api.larkOlderMessages("feed", before: "40", account: "one", topic: false)
                #expect(owner == "one")
            } catch let error as APIError {
                #expect(error.code == (owner.isEmpty ? "lark_history_upgrade_required" : "lark_account_changed"))
            }
        }
    }
}
