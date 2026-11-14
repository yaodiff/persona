import Foundation
import Testing
@testable import Persona

/// Instructions outside a suggestion card reuse its request, stale-answer and error handling with suggestion version zero.
@MainActor
struct InboxInstructionTests {
    private static let itemID = "chat:example:1"
    private static let path = "/api/v1/inbox/chat:example:1/suggestion"
    private static let answered = #"""
    {"id":"chat:example:1","source":"chat","kind":"dm","account":"chat","account_name":"Example chat",
     "title":"A matter","preview":"A line","count":1,
     "status":"working","handling":{"state":"looking"},"chain":[{"id":"c1","kind":"instructed","text":"Instruction received"}]}
    """#
    private static let staleBody = #"""
    {"code":"suggestion_stale","error":"Suggestion updated","item":{
     "id":"chat:example:1","source":"chat","kind":"dm","account":"chat","account_name":"Example chat",
     "title":"A matter","preview":"A line","count":1,
     "status":"action","suggestion":{"version":2,"kind":"reply","text":"A newer reply"}}}
    """#

    private final class Applied { var items: [InboxItem] = [] }

    private func model(_ hub: FakeHub, _ applied: Applied) -> InboxSuggestionModel {
        let client = hub.client
        return InboxSuggestionModel(itemID: Self.itemID, send: { id, body in try await client.inboxSuggestion(id, body) },
                                    apply: { applied.items.append($0) })
    }

    private func body(_ request: LocalRequest) throws -> [String: JSONValue] {
        guard case .object(let object) = try JSON.decoder.decode(JSONValue.self, from: try #require(request.body)) else { return [:] }
        return object
    }

    @Test func saySomethingUsesVersionZeroAndInstruct() async throws {
        let hub = FakeHub(), applied = Applied()
        hub.on("POST", Self.path, json: Self.answered)
        let model = model(hub, applied)
        let outcome = await model.instruct(.withoutSuggestion, "  Please check the date first.  ")
        guard case .done(_)? = outcome else { Issue.record("instruction did not finish"); return }
        let sent = try body(try #require(hub.sent("POST", Self.path).first))
        #expect(sent["version"] == .number(0))
        #expect(sent["action"] == .string("instruct"))
        #expect(sent["text"] == .string("Please check the date first."))
        #expect(UUID(uuidString: sent["client_id"]?.string ?? "") != nil)
        #expect(applied.items.map(\.id) == [Self.itemID])
    }

    @Test func tryAgainUsesTheSameShapeAndSharedWords() async throws {
        let hub = FakeHub(), applied = Applied()
        hub.on("POST", Self.path, json: Self.answered)
        let model = model(hub, applied)
        _ = await model.instruct(.withoutSuggestion, InboxEventText.retryInstruction)
        let sent = try body(try #require(hub.sent("POST", Self.path).first))
        #expect(sent["version"] == .number(0))
        #expect(sent["action"] == .string("instruct"))
        #expect(sent["text"] == .string("请再试一次。"))
        // Apple tests run in zh-Hans; the catalog carries the matching English and Japanese entries too.
        #expect(InboxEventText.saySomething == "说一句")
        #expect(InboxEventText.tryAgain == "再试一次")
        #expect(InboxEventText.assistantSays == "助手说")
    }

    @Test func aStaleAnswerUsesTheSuggestionCardsNotice() async throws {
        let hub = FakeHub(), applied = Applied()
        hub.on("POST", Self.path) { _ in
            throw APIError(status: 409, message: "Suggestion updated", fields: ["code": "suggestion_stale"],
                           details: try JSON.decoder.decode(JSONValue.self, from: Data(Self.staleBody.utf8)))
        }
        let model = model(hub, applied)
        let outcome = await model.instruct(.withoutSuggestion, "Please try again.")
        guard case .stale(let item)? = outcome else { Issue.record("not stale"); return }
        #expect(item?.suggestion?.version == 2)
        #expect(applied.items.first?.suggestion?.text == "A newer reply")
        #expect(model.notice == "建议已更新" && model.error == nil)
    }

    @Test func aForbiddenAnswerUsesTheSuggestionCardsError() async throws {
        let hub = FakeHub(), applied = Applied()
        hub.on("POST", Self.path) { _ in throw APIError(status: 403, message: "This device can only reply") }
        let model = model(hub, applied)
        let outcome = await model.instruct(.withoutSuggestion, "Please try again.")
        #expect(outcome == .failed("This device can only reply", nil))
        #expect(model.error == "This device can only reply" && model.notice == nil)
        #expect(applied.items.isEmpty)
    }
}
