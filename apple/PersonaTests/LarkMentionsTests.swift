import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct LarkMentionsTests {
    private let zhou = LarkMention(userID: "u-zhou", name: "小周")
    private let otherZhou = LarkMention(userID: "u-zhou2", name: "小周")
    private let tom = LarkMention(userID: "u-tom", name: "Tom")
    private let tommy = LarkMention(userID: "u-tommy", name: "Tommy")

    // MARK: The @ being typed

    @Test func anAtAtTheEndOpensTheLookup() {
        #expect(LarkMentions.query(in: "@") == "")
        #expect(LarkMentions.query(in: "周六 @小") == "小")
        #expect(LarkMentions.query(in: "你好@Mi") == "Mi", "Chinese is written without a space before @")
        #expect(LarkMentions.query(in: "@小周 @To") == "To", "the last @ counts")
    }

    @Test func noLookupWithoutAnAtBeingTyped() {
        #expect(LarkMentions.query(in: "") == nil)
        #expect(LarkMentions.query(in: "周六见") == nil)
        #expect(LarkMentions.query(in: "mail me@example") == nil, "an address is not a mention")
        #expect(LarkMentions.query(in: "@小周 ") == nil, "the space after a picked name ends it")
        #expect(LarkMentions.query(in: "@小周\n") == nil)
        #expect(LarkMentions.query(in: "@" + String(repeating: "长", count: LarkMentions.maxQuery + 1)) == nil)
    }

    @Test func pickingWritesTheNameAndASpace() {
        #expect(LarkMentions.inserting(zhou, into: "周六 @小") == "周六 @小周 ")
        #expect(LarkMentions.inserting(.everyone, into: "@") == "@所有人 ")
        #expect(LarkMentions.inserting(tom, into: "@小周 一起 @") == "@小周 一起 @Tom ")
    }

    // MARK: The cursor in the middle

    @Test func anAtBeforeTheCursorIsLookedUpWhateverFollows() {
        let text = "周六 @小 一起去"
        // The cursor right after 「@小」 (offset 5): the lookup is 「小」; what follows does not count.
        #expect(LarkMentions.typing(in: text, cursor: 5) == LarkMentions.Typing(query: "小", start: 3, end: 5))
        #expect(LarkMentions.query(in: text, cursor: 4) == "", "right after the @")
        #expect(LarkMentions.query(in: text, cursor: 3) == nil, "before the @")
        #expect(LarkMentions.query(in: text, cursor: 6) == nil, "past the space after it")
        #expect(LarkMentions.query(in: text) == nil, "at the end there is no @ being typed")
        #expect(LarkMentions.query(in: "me@example 你好", cursor: 10) == nil, "an address is not a mention")
        #expect(LarkMentions.query(in: "@", cursor: 99) == "", "a cursor past the end is the end")
    }

    @Test func pickingWritesTheNameAtTheCursor() {
        // 「周六 @小| 一起去」: the space that follows is kept, not doubled; the cursor lands after it.
        let mid = LarkMentions.insert(zhou, into: "周六 @小 一起去", cursor: 5)
        #expect(mid.text == "周六 @小周 一起去")
        #expect(mid.cursor == 7)
        // 「你好@|明天见」: a space goes in, the cursor after it.
        let tight = LarkMentions.insert(tom, into: "你好@明天见", cursor: 3)
        #expect(tight.text == "你好@Tom 明天见")
        #expect(tight.cursor == 7)
        // No @ being typed there: the name goes in at the cursor.
        let plain = LarkMentions.insert(.everyone, into: "集合 明天", cursor: 3)
        #expect(plain.text == "集合 @所有人 明天")
        // At the end, as before.
        #expect(LarkMentions.insert(zhou, into: "周六 @小").text == "周六 @小周 ")
        #expect(LarkMentions.insert(zhou, into: "周六 @小").cursor == 7)
    }

    @Test func aMentionPickedInTheMiddleIsSentAndDroppedLikeAnyOther() {
        let picked = LarkMentions.insert(zhou, into: "周六 @ 一起去 @Tom ", cursor: 4).text
        #expect(picked == "周六 @小周 一起去 @Tom ")
        #expect(LarkMentions.mentions(in: picked, picked: [tom, zhou]) == [tom, zhou])
        // Deleting the one in the middle drops only it.
        #expect(LarkMentions.mentions(in: "周六 一起去 @Tom ", picked: [tom, zhou]) == [tom])
    }

    @Test func everyoneIsOfferedUntilTheLookupRulesItOut() {
        #expect(LarkMentions.offersEveryone(""))
        #expect(LarkMentions.offersEveryone("所"))
        #expect(LarkMentions.offersEveryone("Al"))
        #expect(!LarkMentions.offersEveryone("小"))
    }

    // MARK: Text + picked → mentions

    @Test func thoseStillInTheTextAreMentioned() {
        let text = "@小周 @Tom 周六见"
        #expect(LarkMentions.mentions(in: text, picked: [zhou, tom]) == [zhou, tom])
    }

    @Test func deletingANameDropsItsMention() {
        #expect(LarkMentions.mentions(in: "@Tom 周六见", picked: [zhou, tom]) == [tom])
        #expect(LarkMentions.mentions(in: "@小 周六见", picked: [zhou]) == [], "half a name is not a mention")
        #expect(LarkMentions.mentions(in: "周六见", picked: [zhou, tom]) == [])
    }

    @Test func sameNamesCountAsOftenAsTheTextHasThem() {
        #expect(LarkMentions.mentions(in: "@小周 @小周 好", picked: [zhou, otherZhou]) == [zhou, otherZhou])
        // One deleted: one stays, the first picked.
        #expect(LarkMentions.mentions(in: "@小周 好", picked: [zhou, otherZhou]) == [zhou])
        // Someone picked twice is mentioned once.
        #expect(LarkMentions.mentions(in: "@小周 @小周", picked: [zhou, zhou]) == [zhou])
    }

    @Test func aLongerNameDoesNotKeepAShorterOne() {
        #expect(LarkMentions.mentions(in: "@Tommy 来", picked: [tom, tommy]) == [tommy])
        #expect(LarkMentions.mentions(in: "@Tommy @Tom 来", picked: [tom, tommy]) == [tom, tommy])
    }

    @Test func everyoneIsAMentionLikeAnyOther() {
        #expect(LarkMentions.mentions(in: "@所有人 周六集合", picked: [.everyone]) == [.everyone])
        #expect(LarkMentions.mentions(in: "周六集合", picked: [.everyone]) == [])
    }

    // MARK: On the wire

    @Test func mentionsGoWithASendAndAReply() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/lark/conversations/f3/messages", json: "{}")
        hub.on("POST", "/api/v1/lark/messages/m1/replies", json: #"{"ok":true}"#)
        let plain = LarkOutgoing(text: "@所有人 @小周 集合", mentions: [.everyone, zhou])
        let reply = LarkOutgoing(text: "@Tom 好", replyTo: LarkReplyTo(id: "m1", senderName: "Tom", text: "几点"), thread: true, mentions: [tom])
        try await LarkOutbox.deliver(plain, feed: "f3", api: hub.client)
        try await LarkOutbox.deliver(reply, feed: "f3", api: hub.client)
        let sent = try hub.sent("POST", "/api/v1/lark/conversations/f3/messages").map {
            try JSONSerialization.jsonObject(with: $0.body ?? Data()) as? NSDictionary
        }
        #expect(sent.first??["mentions"] as? [[String: String]] == [["user_id": "all", "name": "所有人"], ["user_id": "u-zhou", "name": "小周"]])
        let replied = try hub.sent("POST", "/api/v1/lark/messages/m1/replies").map {
            try JSONSerialization.jsonObject(with: $0.body ?? Data()) as? NSDictionary
        }
        #expect(replied.first??["mentions"] as? [[String: String]] == [["user_id": "u-tom", "name": "Tom"]])
    }

    @Test func membersDecodeWithOrWithoutAnAvatar() throws {
        let json = #"{"members":[{"user_id":"u-tom","name":"Tom","avatar":"/api/v1/lark/media/avatar-tom"},{"user_id":"u-jie","name":"杰哥"}]}"#
        let r = try JSON.decoder.decode(APIClient.LarkMembers.self, from: Data(json.utf8))
        #expect(r.members.map(\.name) == ["Tom", "杰哥"])
        #expect(r.members[0].avatar != nil && r.members[1].avatar == nil)
    }

    @Test func aDraftKeepsItsMentions() throws {
        let d = LarkDraft(text: "@小周 ", mentions: [zhou])
        let back = try JSON.decoder.decode(LarkDraft.self, from: JSON.encoder.encode(d))
        #expect(back == d)
        // A draft kept before mentions existed still reads.
        let old = try JSON.decoder.decode(LarkDraft.self, from: Data(#"{"text":"八点"}"#.utf8))
        #expect(old.mentions == nil && old.text == "八点")
    }
}
