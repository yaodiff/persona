import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The messaging source's answers reshaped into the Feishu screens' models (#2138, LarkMessagingBridge): what the
/// extension sends must decode, with the shapes the screens read.
@MainActor struct LarkMessagingBridgeTests {
    private func decode<T: Decodable>(_ value: Any, as: T.Type) throws -> T {
        try JSON.decoder.decode(T.self, from: JSONSerialization.data(withJSONObject: value))
    }

    @Test func aProfileWithFieldsDecodes() throws {
        let raw: [String: Any] = ["name": "Bob", "fields": [["label": "邮箱", "value": "bob@example.com"],
                                                            ["label": "上级", "value": "Ann", "person": "u7"]]]
        let p = try decode(MessagingShape.profile(raw, source: "lark", account: nil), as: LarkProfile.self)
        #expect(p.displayName == "Bob" && p.fields?.count == 2)
        #expect(p.fields?[1].url == "lark://client/profile?uid=u7")
    }

    /// The line under the name stays under it (the name is the card's title), and the pictures are the account's.
    @Test func aProfileKeepsItsNameAndAccount() throws {
        let raw: [String: Any] = ["name": "张三", "subtitle": "小张 · Zhang San", "avatar": "media/u1"]
        let p = try decode(MessagingShape.profile(raw, source: "lark", account: "2"), as: LarkProfile.self)
        #expect(p.displayName == "张三" && p.alias == nil && p.enName == "小张 · Zhang San")
        #expect(p.avatarUrl == "/api/v1/messaging/lark/media/u1?account=2")
    }

    @Test func accountsWithoutUnreadDecode() throws {
        let raw: [String: Any] = ["accounts": [["id": "1", "signed_in": true],
                                               ["id": "2", "signed_in": true, "unread": 3, "avatar": "media/a2"]], "default": "1"]
        let a = try decode(MessagingShape.accounts(raw, source: "lark"), as: APIClient.LarkAccounts.self)
        #expect(a.default == 1 && a.accounts.map(\.unread) == [0, 3])
        #expect(a.accounts[1].avatar == "/api/v1/messaging/lark/media/a2?account=2")
    }

    @Test func momentsCommentsDecode() throws {
        let raw: [String: Any] = ["comments": [["id": "c1", "author": ["name": "Ann"],
                                                "parts": [["kind": "text", "text": "好"], ["kind": "emoji", "key": "SMILE", "text": "[微笑]"]]]]]
        struct Page: Decodable { var comments: [LarkMomentsComment] }
        let page = try decode(MessagingShape.postComments(raw, source: "lark", account: nil), as: Page.self)
        #expect(page.comments.first?.content?.text == "好[微笑]")
        #expect(page.comments.first?.content?.segments?.count == 2)
        // Drawn as an emoji (Moments draws kind "emoji"), by the source's key.
        let emoji = page.comments.first?.content?.segments?.last
        #expect(emoji?.kind == "emoji" && emoji?.key == "SMILE")
    }

    /// An @, a link or an emoji between the words of one paragraph stays in it: one line, not three.
    @Test func inlinePartsStayInTheirParagraph() throws {
        let raw: [[String: Any]] = [
            ["kind": "text", "text": "请 ", "block": "paragraph", "block_id": "p1"],
            ["kind": "mention", "text": "@张三", "user_id": "u1"],
            ["kind": "text", "text": " 看一下 ", "block": "paragraph", "block_id": "p1"],
            ["kind": "link", "text": "文档", "url": "https://example.com/doc"],
            ["kind": "text", "text": "第二段", "block": "paragraph", "block_id": "p2"],
        ]
        let parts = try decode(MessagingShape.parts(raw, source: "lark", account: nil), as: [LarkPart].self)
        let tree = LarkRichNode.tree(parts)
        #expect(tree.count == 2, "two paragraphs: \(tree)")
        guard case .block(let first, let leaves) = tree.first else { Issue.record("\(tree)"); return }
        #expect(first.id == "p1" && leaves.count == 4)
    }

    /// An @ that starts the message is in the paragraph after it; a list item with a link keeps its list numbering.
    @Test func inlinePartsAtTheStartAndInListItems() throws {
        let raw: [[String: Any]] = [
            ["kind": "mention", "text": "@张三", "user_id": "u1"],
            ["kind": "text", "text": " 请看", "block": "paragraph", "block_id": "p1"],
            ["kind": "text", "text": "见 ", "block": "list_item", "block_id": "i1", "list": ["ordered": true, "level": 0]],
            ["kind": "link", "text": "链接", "url": "https://example.com/a"],
            ["kind": "text", "text": "第二项", "block": "list_item", "block_id": "i2", "list": ["ordered": true, "level": 0]],
        ]
        let parts = try decode(MessagingShape.parts(raw, source: "lark", account: nil), as: [LarkPart].self)
        let tree = LarkRichNode.tree(parts)
        #expect(tree.count == 2, "a paragraph and one list: \(tree)")
        guard case .block(let paragraph, let words) = tree.first, case .block(let list, let items) = tree.last else { Issue.record("\(tree)"); return }
        #expect(paragraph.id == "p1" && words.count == 2)
        #expect(list.type == "ordered-list" && items.count == 2)
    }

    /// A newer source says an inline part's block itself: that block it is.
    @Test func anInlinePartThatSaysItsBlock() throws {
        let raw: [[String: Any]] = [
            ["kind": "text", "text": "第一段", "block": "paragraph", "block_id": "p1"],
            ["kind": "mention", "text": "@张三", "user_id": "u1", "block": "paragraph", "block_id": "p2"],
            ["kind": "text", "text": " 第二段", "block": "paragraph", "block_id": "p2"],
        ]
        let parts = try decode(MessagingShape.parts(raw, source: "lark", account: nil), as: [LarkPart].self)
        let tree = LarkRichNode.tree(parts)
        guard tree.count == 2, case .block(let second, let words) = tree.last else { Issue.record("\(tree)"); return }
        #expect(second.id == "p2" && words.count == 2)
    }

    /// A sealed message's earlier versions are sealed too: nothing of them is written to the disk cache.
    @Test func aSealedMessagesEarlierWordsStaySealed() throws {
        let raw: [String: Any] = ["id": "m1", "position": "5", "sender": ["name": "Ann"], "text": "", "private": true,
                                  "private_text": "封面", "parts": [["kind": "text", "text": "正文"]],
                                  "edit_history": [["text": "旧正文", "parts": [["kind": "text", "text": "旧正文"]], "edited_at": "2026-11-06T10:00:00Z"]]]
        let m = try decode(MessagingShape.message(raw, source: "lark", account: nil), as: LarkMessage.self)
        #expect(m.editHistory?.first?.olk?.isOpen == true)
        let cached = String(decoding: try JSON.encoder.encode(m), as: UTF8.self)
        #expect(!cached.contains("旧正文") && !cached.contains("\"正文\""))
    }

    /// A sealed message inside a forward stays sealed: its body is not written to the disk cache, only its cover.
    @Test func aSealedMessageInAForwardStaysSealed() throws {
        let inner: [String: Any] = ["id": "f1", "sender": ["name": "Ann"], "text": "正文", "private": true, "private_text": "封面",
                                    "parts": [["kind": "text", "text": "正文"]]]
        let raw: [[String: Any]] = [["kind": "forward", "title": "聊天记录", "messages": [inner], "count": 1]]
        let parts = try decode(MessagingShape.parts(raw, source: "lark", account: nil), as: [LarkPart].self)
        let record = parts.first?.meta("messages")?.array?.first
        #expect(record?["olk"]?["bodyParts"] != nil, "sealed in memory, its body readable")
        let cached = String(decoding: try JSON.encoder.encode(parts), as: UTF8.self)
        #expect(!cached.contains("正文") && cached.contains("封面"))
    }

    /// An answer for another account than the one asked for is refused, not shown.
    @Test func anotherAccountsAnswerIsRefused() {
        #expect(throws: APIError.self) { try APIClient.messagingEcho(["account": "1"], account: "2") }
        #expect(throws: Never.self) { try APIClient.messagingEcho(["account": "2"], account: "2") }
        #expect(throws: Never.self) { try APIClient.messagingEcho(["account": "1"], account: nil) }
    }

    /// Consecutive numbered items share one list (numbered 1, 2), and a deeper item goes into the item before it.
    @Test func listItemsShareTheirList() throws {
        let raw: [[String: Any]] = [
            ["kind": "text", "text": "a", "block": "list_item", "block_id": "b1", "list": ["ordered": true, "level": 0]],
            ["kind": "text", "text": "a1", "block": "list_item", "block_id": "b2", "list": ["ordered": false, "level": 1]],
            ["kind": "text", "text": "b", "block": "list_item", "block_id": "b3", "list": ["ordered": true, "level": 0]],
        ]
        let parts = try decode(MessagingShape.parts(raw, source: "lark", account: nil), as: [LarkPart].self)
        let tree = LarkRichNode.tree(parts)
        guard tree.count == 1, case .block(let list, let items) = tree[0] else { Issue.record("\(tree)"); return }
        #expect(list.type == "ordered-list" && items.count == 2)
        guard case .block(_, let first) = items[0] else { Issue.record("\(items)"); return }
        #expect(first.contains { node in
            if case .block(let b, _) = node { return b.type == "unordered-list" }
            return false
        })
    }
}
