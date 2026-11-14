import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The hub's `notice` feed line (docs/protocol/protocol.md「Tasks and the chat feed」, #4305): no ref, a body with the text the hub wrote in
/// the user's language, drawn as one small centered line where it happened. A kind this client does not know stays
/// unknown (and is drawn as nothing).
@MainActor
struct FeedNoticeTests {
    private func items(_ json: String) throws -> [FeedItem] {
        try JSON.decoder.decode(APIClient.FeedResponse.self, from: Data(json.utf8)).items
    }

    @Test func aNoticeDecodesWithItsText() throws {
        let list = try items(#"""
        {"items":[
          {"id":"feed_1","conversation":"c1","at":"2026-11-06T08:00:00Z","kind":"notice","body":{"text":"电脑和模型接好了，定时任务恢复运行，这次是「早报」"}},
          {"id":"feed_2","conversation":"c1","at":"2026-11-06T08:01:00Z","kind":"notice","body":{"text":"  \n "}},
          {"id":"feed_3","conversation":"c1","at":"2026-11-06T08:02:00Z","kind":"notice"},
          {"id":"feed_4","conversation":"c1","at":"2026-11-06T08:03:00Z","kind":"something_newer","body":{"text":"not for this client"}}]}
        """#)
        #expect(list.map(\.kind) == [.notice, .notice, .notice, .unknown])
        #expect(list[0].noticeText == "电脑和模型接好了，定时任务恢复运行，这次是「早报」")
        // Nothing to say: no line at all.
        #expect(list[1].noticeText == nil && list[2].noticeText == nil)
        // Only a notice is one: another kind's text is not drawn as a notice.
        #expect(list[3].noticeText == nil)
    }

    /// A notice goes where it happened among the chat's lines, as every feed line does.
    @Test func aNoticeLandsWhereItHappened() {
        func user(_ seq: Int, _ minutesAgo: Double) -> ChatLine {
            .row(.user(Item(id: "\(seq)#0", seq: seq, ts: Date(timeIntervalSinceNow: -minutesAgo * 60), role: "user", kind: .text,
                            text: "m\(seq)", toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)))
        }
        let notice = FeedItem(id: "n1", conversation: "c1", at: Date(timeIntervalSinceNow: -20 * 60), kind: .notice, ref: nil,
                              body: .object(["text": .string("电脑和模型接好了，定时任务恢复运行，这次是「早报」")]))
        let merged = ChatLines.mergeFeed([user(1, 30), user(2, 10)], [notice])
        #expect(merged.map(\.id) == ["1#0", "feed-n1", "2#0"])
    }
}
