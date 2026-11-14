import Foundation
import Testing
@testable import Persona

@MainActor
struct InboxSourceBodyTests {
    private func item(source: String = "lark", content: String = "") throws -> InboxItem {
        let fields = content.isEmpty ? "" : ",\n" + content
        return try JSON.decoder.decode(InboxItem.self, from: Data(#"""
        {
          "id": "inbox-1",
          "source": "\#(source)",
          "kind": "group",
          "account": "account-1",
          "account_name": "Example",
          "title": "Project chat",
          "preview": "Latest message",
          "count": 2\#(fields)
        }
        """#.utf8))
    }

    /// Native chat data alone selects the chat directly, whether its source has an extension declaration or not. Only an
    /// explicitly supplied position replaces the conversation's focus.
    @Test(arguments: ["lark", "examplechat"])
    func nativeChatIsDirectForExtensionAndUndeclaredSources(source: String) throws {
        let item = try item(source: source, content: #"""
        "lark": {"feed_id": "feed-1", "is_group": true, "start_position": "100", "read_position": "99"}
        """#)
        let conversation = try #require(item.larkConversation)
        #expect(conversation.focus == "100")
        #expect(InboxSourcePage.Selection.of(item: item) == .chat(conversation))

        var positioned = conversation
        positioned.focus = "linked-message"
        #expect(InboxSourcePage.Selection.of(item: item, position: "linked-message") == .chat(positioned))
        #expect(item.larkConversation?.focus == "100", "choosing a body does not change the item")
    }

    @Test func doneChatPreservesItsMissingFocusUntilPositioned() throws {
        let item = try item(content: #"""
        "done_at": "2026-11-13T02:00:00Z",
        "done_by": "user",
        "lark": {"feed_id": "feed-done", "is_group": false, "start_position": "200"}
        """#)
        let conversation = try #require(item.larkConversation)
        #expect(conversation.focus == nil)
        #expect(conversation.unread == 0)
        #expect(InboxSourcePage.Selection.of(item: item) == .chat(conversation))

        var positioned = conversation
        positioned.focus = "250"
        #expect(InboxSourcePage.Selection.of(item: item, position: "250") == .chat(positioned))
    }

    @Test func sourceContentKeepsItsExistingPrecedence() throws {
        var item = try item(source: "examplechat", content: #"""
        "lark": {"feed_id": "feed-1", "is_group": true, "start_position": "100"},
        "doc": {"url": "https://example.com/document", "type": "docx", "notice": "comment"},
        "connector": {"thread": "thread-1", "label": "Example"},
        "moment": {"post_id": "post-1", "notice": "reply"},
        "mail": {"account_id": "mail-1", "mailbox": "INBOX", "uids": [5, 7], "message_id": "<message@example.com>"}
        """#)
        #expect(InboxSourcePage.Selection.of(item: item) == .slot(.document))

        item.doc = nil
        #expect(InboxSourcePage.Selection.of(item: item) == .slot(.connector))

        item.connector = nil
        #expect(InboxSourcePage.Selection.of(item: item) == .slot(.moment))

        item.moment = nil
        let conversation = try #require(item.larkConversation)
        #expect(InboxSourcePage.Selection.of(item: item) == .chat(conversation))

        item.lark = nil
        #expect(InboxSourcePage.Selection.of(item: item) == .slot(.mail))

        item.mail = nil
        #expect(InboxSourcePage.Selection.of(item: item) == .slot(.empty))
    }
}
