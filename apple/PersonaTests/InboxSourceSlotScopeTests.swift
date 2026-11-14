import Foundation
import Testing
@testable import Persona

/// What the inbox's source page opens (docs/assistant/inbox.md, "Plugin-rendered details"): an item whose only block is
/// its chat opens the conversation; every other source keeps today's page. The page keeps the source's order — a document,
/// a connector's thread and a Moments post before the chat, mail after it — so a block sent beside a chat does not take
/// the chat's place: a document from the same source still opens the document.
@MainActor
struct InboxSourceSlotScopeTests {
    private func item(source: String, kind: String = "doc", _ blocks: String) throws -> InboxItem {
        try JSON.decoder.decode(InboxItem.self, from: Data("""
        {"id":"inbox-1","source":"\(source)","kind":"\(kind)","account":"a","account_name":"Example",
         "title":"An item","preview":"","count":1,\(blocks)}
        """.utf8))
    }

    @Test func aDocumentFromTheSameSourceKeepsTheSlot() throws {
        // A mention on a document in the source whose chats open directly: no `lark` block, so today's page.
        let doc = try item(source: "lark", #""doc":{"url":"https://example.com/base","type":"base","notice":"mention"}"#)
        #expect(doc.larkConversation == nil)
        #expect(InboxSourcePage.Selection.of(item: doc) == .slot(.document))

        // A chain position names a message in a chat: it changes no document's page.
        #expect(InboxSourcePage.Selection.of(item: doc, position: "42") == .slot(.document))
    }

    @Test func aChatOfItsOwnOpensTheChat() throws {
        let chat = try item(source: "examplechat", kind: "group", """
        "lark":{"feed_id":"feed-1","is_group":true,"start_position":"7"}
        """)
        let conversation = try #require(chat.larkConversation)
        #expect(InboxSourcePage.Selection.of(item: chat) == .chat(conversation))

        var positioned = conversation
        positioned.focus = "9"
        #expect(InboxSourcePage.Selection.of(item: chat, position: "9") == .chat(positioned))
    }

    @Test func todaysSourceOrderIsKeptWhenBlocksComeTogether() throws {
        // The source's order before this page changed: a document, a connector's thread and a Moments post all came
        // before the chat, so an item carrying one of them beside its chat block keeps today's page; mail came after
        // the chat, so a chat beside it still opens.
        let chat = #""lark":{"feed_id":"feed-1","is_group":true,"start_position":"7"}"#
        let withConnector = try item(source: "examplechat", kind: "group",
                                     chat + #","connector":{"thread":"thread-1","label":"Example"}"#)
        #expect(withConnector.larkConversation != nil)
        #expect(InboxSourcePage.Selection.of(item: withConnector) == .slot(.connector))

        let withMoment = try item(source: "lark", kind: "moment", chat + #","moment":{"post_id":"post-1","notice":"comment"}"#)
        #expect(InboxSourcePage.Selection.of(item: withMoment) == .slot(.moment))

        let withMail = try item(source: "lark", kind: "mail",
                                chat + #","mail":{"account_id":"mail-1","mailbox":"INBOX","uids":[5]}"#)
        #expect(InboxSourcePage.Selection.of(item: withMail) == .chat(try #require(withMail.larkConversation)))
    }

    @Test func anItemWithoutABlockKeepsTheEmptyPage() throws {
        let bare = try item(source: "examplechat", kind: "note", "")
        #expect(InboxSourcePage.Selection.of(item: bare) == .slot(.empty))
    }
}
