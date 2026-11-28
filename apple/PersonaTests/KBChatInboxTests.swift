import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A chat's Inbox items on its knowledge page and an item's way back (#15356, docs/assistant/knowledge.md "A chat's Inbox
/// items"): the hub's `inbox` and `kb_chat` read with and without them (an older hub), the section's open and done items
/// and their section word, the rows of the last 7 days each item holds (the same words on another day or at another
/// minute stay plain), the stamp a link to an item names, and the item's read asked once. Made-up data.
@MainActor
struct KBChatInboxTests {
    private static let recent = """
    ## 2026-11-20
    - 10:15 Alice: Can you review the plan?
      It is in the shared doc
    - 10:16 Bob: Thanks
    - 10:17 Alice: Can you review the plan?
    ## 2026-11-21
    - 10:15 Alice: Can you review the plan?
      It is in the shared doc
    """

    private static func detail(inbox: String?) -> String {
        let recent = String(data: try! JSONEncoder().encode(Self.recent), encoding: .utf8)!
        let field = inbox.map { #","inbox":\#($0)"# } ?? ""
        return #"{"detail":{"kind":"chats","id":"larkx-c1","path":"/data/chats/larkx-c1","meta":{"name":"Release group","connector":"larkx","source_id":"c1"},"recent":\#(recent)}\#(field)}"#
    }

    private static let items = """
    [{"id":"larkx:c1:2","title":"Review the plan","tier":"action","status":"action","summary":"Alice asks for a review","preview":"Can you review the plan?","updated_at":"2026-11-20T02:15:00Z",
      "messages":[{"day":"2026-11-20","line":"- 10:15 Alice: Can you review the plan?\\n  It is in the shared doc"}]},
     {"id":"larkx:c1:3","title":"Working on it","tier":"fyi","status":"working","updated_at":"2026-11-20T01:00:00Z"},
     {"id":"larkx:c1:1","title":"Shared the slides","tier":"fyi","status":"fyi","updated_at":"2026-11-19T02:00:00Z","done_at":"2026-11-19T03:00:00Z"}]
    """

    private static func response(_ json: String) throws -> KBDetailResponse {
        try JSON.decoder.decode(KBDetailResponse.self, from: Data(json.utf8))
    }

    private static func inboxItem(_ id: String, manager: String? = nil, lark: Bool = true) throws -> InboxItem {
        let source = lark ? #","lark":{"feed_id":"c1","is_group":true}"# : ""
        var it = try JSON.decoder.decode(InboxItem.self, from: Data(#"{"id":"\#(id)","source":"lark","kind":"group","account":"feishu","account_name":"Lark","title":"t","preview":"p","count":1\#(source)}"#.utf8))
        it.manager = manager
        return it
    }

    // MARK: Decoding

    @Test func aChatsInboxItemsAreReadWithTheirRowsAndAnOlderHubHasNone() throws {
        let r = try Self.response(Self.detail(inbox: Self.items))
        let inbox = try #require(r.inbox)
        #expect(inbox.map(\.id) == ["larkx:c1:2", "larkx:c1:3", "larkx:c1:1"])
        #expect(inbox[0].title == "Review the plan" && inbox[0].tier == "action" && inbox[0].status == "action")
        #expect(inbox[0].summary == "Alice asks for a review" && inbox[0].doneAt == nil)
        #expect(inbox[0].updatedAt == ISO8601DateFormatter().date(from: "2026-11-20T02:15:00Z"))
        #expect(inbox[0].messages == [KBInboxItem.Row(day: "2026-11-20", line: "- 10:15 Alice: Can you review the plan?\n  It is in the shared doc")])
        #expect(inbox[2].doneAt == ISO8601DateFormatter().date(from: "2026-11-19T03:00:00Z") && inbox[2].messages == nil)

        // A hub before #15356, and a chat with no items (the hub leaves the field out): nothing new on the page.
        let old = try Self.response(Self.detail(inbox: nil))
        #expect(old.inbox == nil && KBChatInbox.Page(old) == nil)
        #expect(KBChatInbox.Page(try Self.response(Self.detail(inbox: "[]"))) == nil)
        #expect(KBChatInbox.Page(try Self.response(Self.detail(inbox: "null"))) == nil)
        // Only a chat lists them.
        let project = Self.detail(inbox: Self.items).replacingOccurrences(of: #""kind":"chats""#, with: #""kind":"projects""#)
        #expect(KBChatInbox.Page(try Self.response(project)) == nil)
    }

    @Test func anItemsReadNamesItsChatOrNothing() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/inbox/larkx:c1:2", json: #"{"id":"larkx:c1:2","source":"larkx","kind":"group","account":"larkx","account_name":"Lark","title":"t","preview":"p","count":1,"kb_chat":{"id":"larkx-c1","path":"/kb/chats/larkx-c1"}}"#)
        hub.on("GET", "/api/v1/inbox/larkx:c2:1", json: #"{"id":"larkx:c2:1","source":"larkx","kind":"group","account":"larkx","account_name":"Lark","title":"t","preview":"p","count":1}"#)
        #expect(try await hub.client.inboxKBChat("larkx:c1:2") == "larkx-c1")
        #expect(try await hub.client.inboxKBChat("larkx:c2:1") == nil)
    }

    // MARK: The section

    @Test func theSectionListsTheOpenItemsAndFoldsTheDoneOnes() throws {
        let page = try #require(KBChatInbox.Page(try Self.response(Self.detail(inbox: Self.items))))
        #expect(page.open.map(\.id) == ["larkx:c1:2", "larkx:c1:3"])
        #expect(page.done.map(\.id) == ["larkx:c1:1"])
        // An open item says its section; a done one its tier; one not sorted yet says nothing.
        #expect(page.open.map(KBChatInbox.tier) == [.action, .working])
        #expect(KBChatInbox.tier(page.done[0]) == .fyi)
        let unsorted = KBInboxItem(id: "x", title: "x")
        #expect(KBChatInbox.tier(unsorted) == nil)
        #expect(KBChatInbox.tier(KBInboxItem(id: "x", title: "x", tier: "later")) == .action)
        #expect(KBChatInbox.line(page.open[0]) == "Alice asks for a review")
        #expect(KBChatInbox.line(KBInboxItem(id: "x", title: "x", summary: " ", preview: "Can you?")) == "Can you?")
        #expect(KBChatInbox.line(unsorted) == nil)
    }

    // MARK: Message rows

    @Test func theRowsAnItemHoldsOpenItAndTheOthersStayPlain() throws {
        let page = try #require(KBChatInbox.Page(try Self.response(Self.detail(inbox: Self.items))))
        let pieces = try #require(page.pieces)
        let linked = pieces.filter { $0.item != nil }
        // Alice's first row, both its lines; her later row with the same words at another minute, and the same row on
        // another day, are not the item's.
        #expect(linked.map(\.text) == ["- 10:15 Alice: Can you review the plan?\n  It is in the shared doc"])
        #expect(linked.map(\.item?.id) == ["larkx:c1:2"])
        // Nothing of the text is lost or moved: the pieces put back together are the text.
        #expect(pieces.map(\.text).joined(separator: "\n") == Self.recent)
        #expect(pieces.map(\.text).first == "## 2026-11-20")
        #expect(pieces.count == 3 && pieces[2].text.hasPrefix("- 10:16 Bob: Thanks\n- 10:17 Alice:") && pieces[2].item == nil)
    }

    @Test func noRowAnItemHoldsDrawsTheTextAsBefore() {
        let none = KBInboxItem(id: "larkx:c1:3", title: "Working on it")
        #expect(KBChatInbox.pieces(Self.recent, items: [none]) == nil)
        // A row the hub names that the text no longer has (read in between).
        let gone = KBInboxItem(id: "larkx:c1:4", title: "Gone", messages: [.init(day: "2026-11-19", line: "- 09:00 Alice: Earlier")])
        #expect(KBChatInbox.pieces(Self.recent, items: [gone]) == nil)
        #expect(KBChatInbox.pieces("", items: [gone]) == nil)
    }

    @Test func aRowGoesToTheFirstItemNamingItAndIsReadAsTheWebReadsIt() {
        let row = KBInboxItem.Row(day: "2026-11-20", line: "- 10:16 Bob: Thanks")
        let newer = KBInboxItem(id: "new", title: "Newer", messages: [row])
        let older = KBInboxItem(id: "old", title: "Older", messages: [row])
        let pieces = KBChatInbox.pieces(Self.recent, items: [newer, older]) ?? []
        #expect(pieces.compactMap(\.item?.id) == ["new"])
        // The further lines' indentation and the outer spacing are not part of a row's key; its day, time, sender and
        // words are.
        let key = KBChatInbox.rowKey(day: "2026-11-20", line: "- 10:15 Alice: Can you review the plan?\n  It is in the shared doc")
        #expect(key == KBChatInbox.rowKey(day: "2026-11-20", line: "- 10:15 Alice: Can you review the plan?\n\tIt is in the shared doc "))
        #expect(key != KBChatInbox.rowKey(day: "2026-11-21", line: "- 10:15 Alice: Can you review the plan?\n  It is in the shared doc"))
        #expect(key != KBChatInbox.rowKey(day: "2026-11-20", line: "- 10:17 Alice: Can you review the plan?\n  It is in the shared doc"))
        #expect(key != KBChatInbox.rowKey(day: "2026-11-20", line: "- 10:15 Bob: Can you review the plan?\n  It is in the shared doc"))
    }

    @Test func aRowWithoutATimeOrADayAndTheCutMarkAreNoItemsRows() {
        let text = "- 09:00 Alice: Before any day\n## 2026-11-20\nA line of its own\n…\n- 10:16 Bob: Thanks"
        let items = [KBInboxItem(id: "a", title: "a", messages: [.init(day: "", line: "- 09:00 Alice: Before any day")]),
                     KBInboxItem(id: "b", title: "b", messages: [.init(day: "2026-11-20", line: "A line of its own")]),
                     KBInboxItem(id: "c", title: "c", messages: [.init(day: "2026-11-20", line: "- 10:16 Bob: Thanks")])]
        let pieces = KBChatInbox.pieces(text, items: items) ?? []
        // A row before the first day has no day, so no item holds it; a line of its own is a row; "…" is plain.
        #expect(pieces.map(\.text) == ["- 09:00 Alice: Before any day\n## 2026-11-20", "A line of its own", "…", "- 10:16 Bob: Thanks"])
        #expect(pieces.map(\.item?.id) == [nil, "b", nil, "c"])
    }

    // MARK: Opening an item

    @Test func aLinkToAnItemNamesTheListsStampOfThatAssistantsCopy() throws {
        let roster = ManagerRoster(managers: [HubManager(id: "assistant", name: "A", online: true, isDefault: true), HubManager(id: "work", name: "W", online: true)])
        let owner: (String?) -> String = { roster.owner($0) }
        let items = [try Self.inboxItem("lark:c1:2", manager: "assistant"), try Self.inboxItem("lark:c1:2", manager: "work")]
        // The default's knowledge base (its client names no manager): the default's copy, stamped with its id.
        #expect(InboxKBChats.listedManager(of: "lark:c1:2", manager: nil, items: items, owner: owner) == "assistant")
        #expect(InboxKBChats.listedManager(of: "lark:c1:2", manager: "work", items: items, owner: owner) == "work")
        // Not listed (yet): the knowledge base's own manager.
        #expect(InboxKBChats.listedManager(of: "lark:c1:9", manager: "work", items: items, owner: owner) == "work")
        // One assistant, no roster: whatever the list stamped.
        #expect(InboxKBChats.listedManager(of: "lark:c1:2", manager: nil, items: [items[0]]) { _ in "" } == "assistant")
    }

    @Test func anItemsChatIsAskedOnceAndOnlyOfAnItemThatCanHaveOne() async throws {
        let hub = Reads()
        let chats = InboxKBChats { item in
            hub.asked.append(item.id)
            if hub.fail { throw APIError(status: 503, message: "offline") }
            return item.id == "lark:c1:2" ? "larkx-c1" : nil
        }
        let item = try Self.inboxItem("lark:c1:2")
        // A read that failed is not an answer: asked again.
        await chats.load(item)
        #expect(chats.chat(of: item) == nil)
        hub.fail = false
        await chats.load(item)
        await chats.load(item)
        #expect(chats.chat(of: item) == "larkx-c1")
        #expect(hub.asked == ["lark:c1:2", "lark:c1:2"])
        // Another assistant's copy of the same id is another item.
        let other = try Self.inboxItem("lark:c1:2", manager: "work")
        #expect(chats.chat(of: other) == nil)
        // None named: none, and not asked again.
        let plain = try Self.inboxItem("lark:c2:1")
        await chats.load(plain)
        await chats.load(plain)
        #expect(chats.chat(of: plain) == nil && hub.asked.filter { $0 == "lark:c2:1" }.count == 1)
        // An item of no chat, mail or connector (an assistant row) is never asked.
        let row = try Self.inboxItem("assistant:1", lark: false)
        await chats.load(row)
        #expect(!hub.asked.contains("assistant:1") && !InboxKBChats.asks(row))
    }
}

/// What the fake reads were asked, and whether they fail.
@MainActor private final class Reads {
    var asked: [String] = []
    var fail = true
}
