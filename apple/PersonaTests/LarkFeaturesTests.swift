import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Feishu through the Lark gateway is a plugin's (#1530): what the app shows of it goes by the hub's features
/// (`LarkFeatures`), and the company feed has no tenant's name built in (`KBMoments`).
@MainActor struct LarkFeaturesTests {
    @Test func theExtensionsFeatures() {
        #expect(LarkFeatures(hub: ["tls", "lark", "lark_moments", "lark_attachments"]) == .all)
        #expect(LarkFeatures(hub: ["tls", "lark", "lark_attachments"]) == LarkFeatures(chats: true, moments: false))
    }

    @Test func noExtensionNoFeishu() {
        #expect(LarkFeatures(hub: ["tls", "connectors", "plugins"]) == .none)
        // The company feed alone is nothing to open: its posts are read through the chats' routes.
        #expect(LarkFeatures(hub: ["lark_moments"]) == .none)
        #expect(LarkFeatures(chats: false, moments: true) == .none)
    }

    @Test func unknownKeepsTodaysBehaviour() {
        // No hello yet, the demo hub, a hub from before features.
        #expect(LarkFeatures(hub: []) == .all)
        // A hub from before the plugin served Feishu itself and said so with lark_attachments only.
        #expect(LarkFeatures(hub: ["tls", "lark_attachments", "lark_native_encryption"]) == .all)
        // An app with no hub yet.
        #expect(AppModel().larkFeatures == .all)
    }

    @Test func theFeedsName() throws {
        #expect(KBMoments.feedName(nil as String?) == "公司圈")
        #expect(KBMoments.feedName("  ") == "公司圈")
        #expect(KBMoments.feedName("动态") == "动态")
        func ref(_ id: String, _ feed: String?) -> KBMomentRef {
            KBMomentRef(id: id, author: "王五", text: "", at: nil, comments: 0, note: nil, feed: feed)
        }
        #expect(KBMoments.feedName([ref("1", "动态"), ref("2", "动态")]) == "动态")
        #expect(KBMoments.feedName([ref("1", "动态"), ref("2", nil)]) == "公司圈")
        #expect(KBMoments.feedName([KBMomentRef]()) == "公司圈")

        let archived = try JSON.decoder.decode(KBMomentPost.self, from: Data(##"{"post":{"id":"mo1","feed":"动态"},"markdown":"# 动态 · 帖子"}"##.utf8))
        #expect(archived.post?.feed == "动态" && archived.markdown == "# 动态 · 帖子")
        let older = try JSON.decoder.decode(KBMomentPost.self, from: Data(#"{"markdown":"x"}"#.utf8))
        #expect(older.post == nil)
        let row = try JSON.decoder.decode(KBMomentRef.self, from: Data(#"{"id":"mo1","author":"王五","text":"t","comments":2}"#.utf8))
        #expect(row.feed == nil && KBMoments.feedName(row.feed) == "公司圈")
    }
}
