import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A message card's reply: open (Send), sent, or handled elsewhere (#4797). The card's own `sent_at` / `filed_at` from
/// the hub decide where the inbox list (done items of the last 3 days only) has no word.
@MainActor
struct MessageCardStateTests {
    private func card(_ body: [String: String] = [:]) -> FeedItem {
        var o: [String: JSONValue] = ["todo_id": .string("todo_1"), "draft": .string("Working on it, by tonight")]
        for (k, v) in body { o[k] = .string(v) }
        return FeedItem(id: "feed_1", conversation: "c1", at: .now, kind: .message, ref: "lark:item_1", body: .object(o))
    }

    private func item(doneBy: String? = nil) -> InboxItem {
        var it = InboxItem(id: "lark:item_1", source: "lark", kind: "dm", account: "lark", accountName: "lark",
                           title: "A chat", preview: "", count: 1)
        if let doneBy { it.doneAt = .now; it.doneBy = doneBy }
        return it
    }

    private func state(_ card: FeedItem, _ item: InboxItem? = nil, sentHere: Bool = false, refused: Bool = false) -> TasksStore.MessageState {
        TasksStore.messageState(card, inbox: item, sentHere: sentHere, refused: refused)
    }

    @Test func aCardNobodyAnsweredOffersSend() {
        #expect(state(card()) == .open)
        #expect(state(card(), item()) == .open)
    }

    /// The case of #4797: handled long ago, out of the inbox's 3-day list, and only the card knows.
    @Test func aCardTheHubSaysWasFiledIsHandled() {
        let filed = card(["filed_at": "2026-11-01T08:00:00.123456789Z"])
        #expect(filed.filedAt != nil)
        #expect(state(filed) == .filed)
    }

    @Test func aCardTheHubSaysWasSentIsSent() {
        let sent = card(["sent_at": "2026-11-01T08:00:00Z", "sent": "Done, it's in"])
        #expect(state(sent) == .sent)
        #expect(sent.sent == "Done, it's in")
        // The hub never says both; if it did, sent wins: the reply went out.
        #expect(card(["sent_at": "2026-11-01T08:00:00Z", "filed_at": "2026-11-01T09:00:00Z"]).filedAt == nil)
    }

    @Test func theInboxListDecidesWhereItHasTheItem() {
        #expect(state(card(), item(doneBy: "replied")) == .sent)
        for way in ["user", "dismissed", "read", "superseded"] {
            #expect(state(card(), item(doneBy: way)) == .filed, "done by \(way)")
        }
        // Moved back to the inbox since the card was read: open again, as the hub would send it.
        #expect(state(card(["filed_at": "2026-11-01T08:00:00Z"]), item()) == .open)
    }

    @Test func aSendFromHereOrARefusalSettlesTheCardAtOnce() {
        #expect(state(card(), sentHere: true) == .sent)
        #expect(state(card(), refused: true) == .filed)
    }

    @Test func draftUndoMatchesTheWebWithoutChangingOtherToasts() {
        let action = Toast.Action(title: "Undo") {}
        #expect(ToastCapsule.shown(Toast("Dismissed", action: action, duration: Theme.Motion.draftUndo)) == .seconds(6))
        #expect(ToastCapsule.shown(Toast("Other undo", action: action)) == .seconds(5))
        #expect(ToastCapsule.shown(Toast("Error", style: .error)) == .seconds(4))
        #expect(ToastCapsule.shown(Toast("Info")) == .seconds(2.5))
    }

    /// Send names its card, so the hub sends once per card and refuses an item handled since (409 inbox_item_done).
    @Test func sendFromACardNamesTheCard() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/inbox/lark:item_1/reply", json: #"{"ok":true}"#)
        try await hub.client.inboxReply("lark:item_1", text: "On it", card: "feed_1")
        try await hub.client.inboxReply("lark:item_1", text: "From the inbox")
        let bodies = hub.sent("POST", "/api/v1/inbox/lark:item_1/reply").compactMap { req in
            req.body.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
        }
        #expect(bodies.count == 2)
        #expect(bodies.first?["card"] as? String == "feed_1")
        #expect(bodies.last?["card"] == nil)
    }
}
