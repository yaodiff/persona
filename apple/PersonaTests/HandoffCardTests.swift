import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A hand-off conversation's write-back card (#5932, docs/assistant/inbox.md "Item actions"): the feed item as the hub
/// sends it, where it stands by the item, and which items get "Wrap up". Mirrors web/src/lib/handoff-card.test.ts.
struct HandoffCardTests {
    private static func item(_ extra: String) throws -> InboxItem {
        let json = #"{"id":"lark:f1:100","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"示例客户","preview":"","count":1,"tier":"action","# + extra + "}"
        return try JSON.decoder.decode(InboxItem.self, from: Data(json.utf8))
    }

    private static let suggestion = #""suggestion":{"version":2,"kind":"reply","text":"周五上午给你","conversation":{"machine_id":"server","session_id":"m1"}}"#
    private static let handed = #""handoffs":[{"machine_id":"server","session_id":"m1","at":"2026-11-13T09:00:00Z"}]"#
    private static let card = HandoffCardBody(version: 2, kind: "reply", text: "周五上午给你", recap: "示例客户要报价", title: "示例客户", source: "lark")

    @Test func decodesTheFeedItem() throws {
        let json = #"{"id":"f1","conversation":"m1","at":"2026-11-13T09:00:00Z","kind":"inbox_suggestion","ref":"lark:f1:100","body":{"version":2,"kind":"reply","text":"周五上午给你","recap":"示例客户要报价","title":"示例客户","source":"lark","account":"飞书"}}"#
        let feed = try JSON.decoder.decode(FeedItem.self, from: Data(json.utf8))
        #expect(feed.kind == .inboxSuggestion)
        #expect(feed.handoffCard == HandoffCardBody(version: 2, kind: "reply", text: "周五上午给你", recap: "示例客户要报价", title: "示例客户", source: "lark", account: "飞书"))
        let bare = try JSON.decoder.decode(FeedItem.self, from: Data(#"{"id":"f2","conversation":"m1","at":"2026-11-13T09:00:00Z","kind":"inbox_suggestion","body":{"text":"x"}}"#.utf8))
        #expect(bare.handoffCard == nil)
    }

    @Test func decodesTheItemsHandOffsAndTheSuggestionsConversation() throws {
        let it = try Self.item(Self.suggestion + "," + Self.handed)
        #expect(it.handoffs?.first?.sessionID == "m1")
        #expect(it.suggestion?.conversation?.sessionID == "m1")
    }

    @Test func isLiveWhileTheItemsSuggestionIsThisOneFromHere() throws {
        #expect(HandoffCardState.of(Self.card, conversation: "m1", item: try Self.item(Self.suggestion), newest: true) == .live)
        #expect(HandoffCardState.of(Self.card, conversation: "m2", item: try Self.item(Self.suggestion), newest: true) == .replaced)
        let newer = #""suggestion":{"version":3,"kind":"reply","text":"x","conversation":{"machine_id":"server","session_id":"m1"}}"#
        #expect(HandoffCardState.of(Self.card, conversation: "m1", item: try Self.item(newer), newest: true) == .replaced)
        #expect(HandoffCardState.of(Self.card, conversation: "m1", item: try Self.item(#""status":"action""#), newest: true) == .replaced)
        #expect(HandoffCardState.of(Self.card, conversation: "m1", item: nil, newest: true) == .unknown)
    }

    @Test func saysHowTheItemEnded() throws {
        let sent = #""done_at":"2026-11-13T10:00:00Z","done_by":"replied","chain":[{"id":"c1","kind":"confirmed","text":"t","detail":"周五上午给你（改过）"}]"#
        #expect(HandoffCardState.of(Self.card, conversation: "m1", item: try Self.item(sent), newest: true) == .sent("周五上午给你（改过）"))
        let mine = #""done_at":"2026-11-13T10:00:00Z","done_by":"replied","chain":[{"id":"c1","kind":"self_replied","text":"t"}]"#
        #expect(HandoffCardState.of(Self.card, conversation: "m1", item: try Self.item(mine), newest: true) == .selfReplied)
        let filed = #""done_at":"2026-11-13T10:00:00Z","done_by":"user""#
        #expect(HandoffCardState.of(Self.card, conversation: "m1", item: try Self.item(filed), newest: true) == .filed)
        // only the newest card about the item says how it ended
        #expect(HandoffCardState.of(Self.card, conversation: "m1", item: try Self.item(filed), newest: false) == .replaced)
    }

    @Test func wrapUpIsTheOpenItemsHandedHereNewestFirstTwoAtMost() throws {
        func handed(_ id: String, _ at: String, sid: String = "m1", done: Bool = false) throws -> InboxItem {
            var it = try Self.item(#""handoffs":[{"machine_id":"server","session_id":""# + sid + #"","at":""# + at + #""}]"#)
            it.id = id
            if done { it.doneAt = .now }
            return it
        }
        let items = [
            try handed("a", "2026-11-13T08:00:00Z"), try handed("b", "2026-11-13T09:00:00Z"), try handed("c", "2026-11-13T07:00:00Z"),
            try handed("done", "2026-11-13T10:00:00Z", done: true), try handed("other", "2026-11-13T10:00:00Z", sid: "m2"),
            try Self.item(#""status":"action""#),
        ]
        #expect(HandoffWrapUp.items(items, sessionID: "m1").map(\.id) == ["b", "a"])
        #expect(HandoffWrapUp.items(items, sessionID: "m3").isEmpty)
    }
}
