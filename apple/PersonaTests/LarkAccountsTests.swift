import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Several Feishu accounts on the hub's Lark gateway (docs/extensions/feishu.md「Multiple accounts」).
@MainActor struct LarkAccountsTests {
    @Test func aScopedClientNamesItsAccountOnFeishuRequestsOnly() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/lark/conversations", json: #"{"conversations":[],"account_id":"u2"}"#)
        hub.on("GET", "/api/v1/lark/media/key", json: "{}")
        hub.on("GET", "/api/v1/inbox", json: #"{"items":[]}"#)
        let api = hub.client.lark(2)
        _ = try await api.larkConversations()
        _ = try await api.request("GET", "/api/v1/lark/media/key", query: ["ol_account": "3"]) // a media URL naming its own
        _ = try await api.request("GET", "/api/v1/inbox")
        _ = try await hub.client.larkConversations()
        let chats = hub.sent("GET", "/api/v1/lark/conversations")
        #expect(chats.map { $0.query["ol_account"] } == ["2", nil])
        #expect(hub.sent("GET", "/api/v1/lark/media/key").first?.query["ol_account"] == "3")
        #expect(hub.sent("GET", "/api/v1/inbox").first?.query["ol_account"] == nil)
        #expect(api.lark(nil).larkScoped("/api/v1/lark/pinned", [:]).isEmpty)
    }

    private func list(_ accounts: [LarkAccount], default def: Int = 1) -> APIClient.LarkAccounts { .init(accounts: accounts, default: def) }
    private func account(_ id: Int, _ name: String, signedIn: Bool = true) -> LarkAccount {
        LarkAccount(id: id, name: name, signedIn: signedIn, unread: id)
    }

    @Test func choosingAnotherAccountNamesItAndIsKept() async {
        var saved: LarkAccountChoice.Saved?
        var answer = list([account(1, "一号"), account(2, "二号"), account(3, "三号", signedIn: false)])
        let deps = LarkAccountChoice.Deps(config: ServerConfig(url: URL(string: "https://fake.invalid")!, token: "t"),
                                          accounts: { answer }, cached: { saved }, save: { saved = $0 })
        let choice = LarkAccountChoice(deps: deps)
        #expect(choice.accounts.isEmpty && choice.requestAccount == nil && choice.selectedName == nil)
        await choice.load()
        #expect(choice.accounts.map(\.id) == [1, 2]) // signed out: no chip
        #expect(choice.selectedID == 1 && choice.requestAccount == nil && choice.selectedName == "一号")
        choice.select(2)
        #expect(choice.requestAccount == 2 && choice.selectedName == "二号")
        // The next launch paints the chips and the choice from what was kept.
        let again = LarkAccountChoice(deps: deps)
        #expect(again.accounts.map(\.id) == [1, 2] && again.requestAccount == 2)
        // The chosen account signed out: back to the hub's own; one account left: no chips, no name.
        answer = list([account(1, "一号"), account(2, "二号", signedIn: false)])
        await again.load()
        #expect(again.requestAccount == nil && again.selectedID == 1 && again.selectedName == nil)
    }

    @Test func theHubsOwnAccountNeedsNoName() async {
        // lark.account is 2: requests without ol_account are its; account 1 is the one named.
        let choice = LarkAccountChoice(deps: .init(config: ServerConfig(url: URL(string: "https://fake.invalid")!, token: "t"),
                                                   accounts: { self.list([self.account(1, "一号"), self.account(2, "二号")], default: 2) },
                                                   cached: { nil }, save: { _ in }))
        await choice.load()
        #expect(choice.selectedID == 2 && choice.requestAccount == nil)
        choice.select(1)
        #expect(choice.requestAccount == 1)
    }

    @Test func anOlderHubHasNoAccountChips() async {
        let hub = FakeHub() // no /api/v1/lark/accounts: 404
        let api = hub.client
        let choice = LarkAccountChoice(deps: .init(config: api.config, accounts: { try await api.larkAccounts() },
                                                   cached: { .init(list: self.list([self.account(1, "一号"), self.account(2, "二号")]), selected: 2) },
                                                   save: { _ in }))
        #expect(choice.requestAccount == 2)
        await choice.load()
        #expect(choice.accounts.isEmpty && choice.requestAccount == nil)
    }

    @Test func anInboxItemOpensItsChatInItsAccount() throws {
        let item = try JSON.decoder.decode(InboxItem.self, from: Data("""
        {"id":"lark:account:2:w1:10","source":"lark","kind":"mention","account":"feishu","account_name":"飞书 · 工作号","title":"t",
         "preview":"p","count":1,"lark":{"account_id":2,"ol_account":2,"feed_id":"w1","is_group":true,"start_position":"10"}}
        """.utf8))
        let chat = try #require(item.larkConversation)
        #expect(chat.account == 2 && chat.accountName == "工作号" && chat.key == "a2-w1")
        #expect(LarkMessage.cacheName(chat) == "lark-chat-a2-w1")
        // The hub's own account: the feed alone, as before there were several.
        let own = try JSON.decoder.decode(InboxItem.self, from: Data("""
        {"id":"lark:w1:10","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"t","preview":"p","count":1,
         "lark":{"account_id":1,"feed_id":"w1","is_group":false}}
        """.utf8))
        let ownChat = try #require(own.larkConversation)
        #expect(ownChat.account == nil && ownChat.accountName == nil && ownChat.key == "w1")
        #expect(ownChat != chat)
    }

    @Test func aRealtimeEventSaysWhichAccount() throws {
        guard case .lark(let feeds, _, let account)? = ServerEvent.decode(Data(#"{"type":"lark","feed_ids":["w1"],"account":2,"ol_account":2}"#.utf8)) else {
            Issue.record("not a lark event"); return
        }
        #expect(feeds == ["w1"] && account == 2)
        guard case .lark(_, _, let own)? = ServerEvent.decode(Data(#"{"type":"lark","feed_ids":["f1"],"account":1}"#.utf8)) else {
            Issue.record("not a lark event"); return
        }
        #expect(own == nil)
    }

    /// A messaging source's change (#2138): which source, which conversations, the account as the source names it.
    @Test func aMessagingEventSaysWhichSourceAndAccount() throws {
        let raw = #"{"type":"messaging","source":"lark","conversations":["c1"],"account":"2","settings_changed":true}"#
        guard case .messaging(let source, let conversations, let settings, let account)? = ServerEvent.decode(Data(raw.utf8)) else {
            Issue.record("not a messaging event"); return
        }
        #expect(source == "lark" && conversations == ["c1"] && settings && account == "2")
    }
}
