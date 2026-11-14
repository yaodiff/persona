import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A connector's inbox thread as a conversation page (#1054): pages of messages going back, a quote's message reached,
/// a reply through the hub (which asks on the phone), what the page shows when the thread cannot be read. Made-up
/// people and words only.
@MainActor
struct ConnectorThreadTests {
    private func message(_ id: Int, from: String = "u1", mine: Bool = false, at minute: Int = 0, text: String = "示例消息") -> String {
        let name = mine ? "我" : (from == "u1" ? "示例用户" : "另一个示例用户")
        return #"{"id":"\#(id)","at":"2026-11-03T09:\#(String(format: "%02d", minute)):00Z","sender":{"id":"\#(mine ? "" : from)","name":"\#(name)"},"mine":\#(mine),"text":"\#(text) \#(id)"}"#
    }

    /// Pages of 3, newest first: 7–9, then 4–6, then 1–3 (the start).
    private func hub() -> FakeHub {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/connectors/telegram/thread") { req in
            let end = Int(req.query["before"] ?? "10") ?? 10
            let ids = Array(max(end - 3, 1)..<end)
            let msgs = ids.map { self.message($0, at: $0) }.joined(separator: ",")
            let before = ids.first.map { $0 > 1 ? #","before":"\#($0)""# : "" } ?? ""
            return Data(#"{"chat":{"id":"u1","name":"示例用户","kind":"dm","reply":"telegram_send"},"messages":[\#(msgs)]\#(before)}"#.utf8)
        }
        return hub
    }

    @Test func pagesGoBack() async throws {
        let hub = hub()
        let m = ConnectorThreadModel(connector: "telegram", thread: "u1", api: { hub.client })
        await m.load()
        #expect(m.chat?.name == "示例用户")
        #expect(m.chat?.isChat == true)
        #expect(m.messages.map(\.id) == ["7", "8", "9"])
        #expect(m.hasOlder)
        #expect(await m.loadOlder())
        #expect(m.messages.map(\.id) == ["4", "5", "6", "7", "8", "9"])
        // A quote's message further back: read back until it is there.
        #expect(await m.reach("1"))
        #expect(m.messages.first?.id == "1")
        #expect(!m.hasOlder)
        #expect(await !m.loadOlder())
        #expect(await !m.reach("0"))
        #expect(hub.sent("GET", "/api/v1/connectors/telegram/thread").map { $0.query["thread"] } == ["u1", "u1", "u1"])
    }

    /// The reply goes to the hub with the thread's action and the message it answers, then the newest page is read again.
    @Test func replyThroughTheHub() async throws {
        let hub = hub()
        hub.on("POST", "/api/v1/connectors/telegram/thread/reply", json: #"{"ok":true}"#)
        let m = ConnectorThreadModel(connector: "telegram", thread: "u1", api: { hub.client })
        await m.load()
        let answered = try #require(m.messages.last)
        #expect(await m.reply("好的", to: answered))
        let body = try #require(hub.sent("POST", "/api/v1/connectors/telegram/thread/reply").first?.body)
        let sent = try JSONSerialization.jsonObject(with: body) as? [String: String]
        #expect(sent == ["thread": "u1", "action": "telegram_send", "text": "好的", "reply_to": "9"])
        #expect(hub.sent("GET", "/api/v1/connectors/telegram/thread").count == 2)

        hub.on("POST", "/api/v1/connectors/telegram/thread/reply") { _ in throw APIError(status: 403, message: "The user declined telegram_send.") }
        #expect(await !m.reply("算了", to: nil))
        #expect(m.failure?.contains("没有发送") == true)

        // No answer from the hub (it may still have arrived): the line says the reply may not have gone out, not "Couldn't load".
        hub.on("POST", "/api/v1/connectors/telegram/thread/reply") { _ in throw URLError(.notConnectedToInternet) }
        #expect(await !m.reply("再试一次", to: nil))
        #expect(m.failure == "可能没发出去：连上 hub 后看一眼再决定要不要重发")
    }

    /// A read the hub refuses or never answers says so in a read's words, not a send's.
    @Test func readFailureWords() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/connectors/telegram/thread") { _ in throw APIError(status: 403, message: "forbidden") }
        let refused = ConnectorThreadModel(connector: "telegram", thread: "u1", api: { hub.client })
        await refused.load()
        #expect(refused.failure == "hub 不允许这台设备读这个会话")
        hub.on("GET", "/api/v1/connectors/telegram/thread") { _ in throw URLError(.cannotConnectToHost) }
        let offline = ConnectorThreadModel(connector: "telegram", thread: "u1", api: { hub.client })
        await offline.load()
        #expect(offline.failure == "暂时读不到：连上 hub 后再打开")
    }

    /// A thread the hub cannot read (no conversations for that connector, signed out) leaves the page to what the hub
    /// collected; a rate limit says how long to wait.
    @Test func unreadable() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/connectors/github/thread") { _ in throw APIError(status: 409, message: "GitHub has no conversations to open") }
        let gh = ConnectorThreadModel(connector: "github", thread: "example-org/example-repo#12", api: { hub.client })
        await gh.load()
        #expect(gh.loaded && gh.unavailable)
        hub.on("GET", "/api/v1/connectors/telegram/thread") { _ in throw APIError(status: 429, message: "Telegram 限流：还要 248 秒") }
        let tg = ConnectorThreadModel(connector: "telegram", thread: "u1", api: { hub.client })
        await tg.load()
        #expect(tg.unavailable)
        #expect(tg.failure == "Telegram 限流：还要 248 秒")
    }

    /// An item with no conversation of its own (#5537): a subscription's (its source is the subscription) or a
    /// connector's without threads. The hub answers a page that is none; an older hub said no such connector (404) or
    /// none to open (409 without a code). None of them is an error to show over the content; signed out still is.
    @Test func noConversation() async throws {
        let hub = FakeHub()
        let path = "/api/v1/connectors/sub_example1/thread"
        hub.on("GET", path, json: #"{"chat":{"id":"sub:sub_example1:t1","name":"","kind":"none"},"messages":[]}"#)
        let none = ConnectorThreadModel(connector: "sub_example1", thread: "sub:sub_example1:t1", api: { hub.client })
        await none.load()
        #expect(none.loaded && !none.unavailable && none.failure == nil && none.messages.isEmpty)
        #expect(none.chat?.isChat == false && none.chat?.isGitHub == false)

        for older in [APIError(status: 404, message: #"no connector "sub_example1""#),
                      APIError(status: 409, message: "Example has no conversations to open")] {
            hub.on("GET", path) { _ in throw older }
            let m = ConnectorThreadModel(connector: "sub_example1", thread: "sub:sub_example1:t1", api: { hub.client })
            await m.load()
            #expect(m.loaded && m.unavailable)
            #expect(m.failure == nil, "\(older.status) said over the content")
        }

        hub.on("GET", "/api/v1/connectors/telegram/thread") { _ in
            throw APIError(status: 409, message: "Telegram: not signed in", fields: ["code": "unavailable"])
        }
        let signedOut = ConnectorThreadModel(connector: "telegram", thread: "u1", api: { hub.client })
        await signedOut.load()
        #expect(signedOut.unavailable && signedOut.failure == "Telegram: not signed in")
    }

    /// The fields of a page decode as the hub sends them: media, a quote, an event.
    @Test func decodes() throws {
        let raw = #"""
        {"chat":{"id":"c2","name":"示例群","kind":"group","avatar":"a.c2.900","members":12},
         "messages":[{"id":"1","at":"2026-11-03T08:30:00Z","sender":{"name":"示例群"},"event":true,"text":"示例用户 进群"},
                     {"id":"9","at":"2026-11-03T09:30:00Z","sender":{"id":"u8","name":"第三个示例用户","avatar":"a.u8.508"},
                      "media":[{"kind":"video","thumb":"m.c2.9.t","duration":65,"width":1280,"height":720}],
                      "reply_to":{"id":"8","from":"第三个示例用户","text":"演示"}}],
         "before":"1"}
        """#
        let page = try JSON.decoder.decode(ThreadPage.self, from: Data(raw.utf8))
        #expect(page.chat.isGroup && page.chat.isChat && page.chat.reply == nil)
        #expect(page.messages[0].isEvent)
        let video = try #require(page.messages[1].media?.first)
        #expect(video.picture == "m.c2.9.t")
        #expect(page.messages[1].replyTo?.from == "第三个示例用户")
        #expect(page.messages[1].summary == "[视频 1:05]")
        #expect(ThreadMediaText.duration(3723) == "1:02:03")
        #expect(ConnectorMedia.url("telegram", ref: "a.u1.501") == "/api/v1/connectors/telegram/media?ref=a.u1.501")
        let pr = try JSON.decoder.decode(ThreadChat.self, from: Data(#"{"id":"example-org/example-repo#12","name":"示例 PR","kind":"pr"}"#.utf8))
        #expect(!pr.isChat)
    }

    /// The time in a bubble's corner: hours and minutes in the device's zone, Edited before it for an edited message.
    @Test func stamps() throws {
        var cal = Calendar(identifier: .gregorian)
        cal.timeZone = try #require(TimeZone(secondsFromGMT: 8 * 3600))
        var m = try JSON.decoder.decode(ThreadMessage.self, from: Data(message(1, at: 5).utf8)) // 09:05 UTC
        #expect(ThreadLayout.stamp(m, calendar: cal) == "17:05")
        m.edited = true
        #expect(ThreadLayout.stamp(m, calendar: cal) == "已编辑 17:05")
        #expect(ThreadLayout.bubbleShare > 0.74 && ThreadLayout.bubbleShare < 0.81)
    }

    /// The line under the title: the source and the kind of chat, and how many are in a group or channel when the hub
    /// knows.
    @Test func subtitles() throws {
        func chat(_ json: String) throws -> ThreadChat { try JSON.decoder.decode(ThreadChat.self, from: Data(json.utf8)) }
        let zh = Locale(identifier: "zh_CN")
        #expect(ThreadLayout.subtitle(try chat(#"{"id":"c2","name":"示例群","kind":"group","members":1234}"#), source: "Telegram", locale: zh)
                == "Telegram 群聊 · 1,234 位成员")
        #expect(ThreadLayout.subtitle(try chat(#"{"id":"c2","name":"示例群","kind":"group"}"#), source: "Telegram", locale: zh) == "Telegram 群聊")
        #expect(ThreadLayout.subtitle(try chat(#"{"id":"c5","name":"示例频道","kind":"channel","members":12}"#), source: "Telegram", locale: zh)
                == "Telegram 频道 · 12 位订阅者")
        #expect(ThreadLayout.subtitle(try chat(#"{"id":"u1","name":"示例用户","kind":"dm","members":3}"#), source: "Telegram", locale: zh) == "Telegram")
        #expect(ThreadLayout.subtitle(nil, source: "Telegram", locale: zh) == "Telegram")
    }

    /// Like the Feishu chat: a time above a new day or a pause; one avatar and name for a run from the same person,
    /// mine apart from theirs, an event apart from both.
    @Test func runsAndSeparators() throws {
        func decode(_ json: String) throws -> ThreadMessage { try JSON.decoder.decode(ThreadMessage.self, from: Data(json.utf8)) }
        let a = try decode(message(1, at: 0)), b = try decode(message(2, at: 1)), c = try decode(message(3, from: "u7", at: 2))
        let mine = try decode(message(4, mine: true, at: 3)), late = try decode(message(5, at: 30))
        #expect(ThreadLayout.separates(a, after: nil))
        #expect(!ThreadLayout.separates(b, after: a))
        #expect(ThreadLayout.continues(b, after: a))
        #expect(!ThreadLayout.continues(c, after: b))
        #expect(!ThreadLayout.continues(mine, after: c))
        #expect(ThreadLayout.separates(late, after: mine))
        #expect(!ThreadLayout.continues(late, after: mine))
        // The avatar goes beside a run's last: a's run goes on to b, b's ends before another sender, the last ends.
        #expect(!ThreadLayout.endsRun(a, before: b))
        #expect(ThreadLayout.endsRun(b, before: c))
        #expect(ThreadLayout.endsRun(late, before: nil))
    }

    /// A Lark conversation's words: lark-cli writes one interactive message as a `<card title="…">` envelope and a post
    /// as Markdown; both are drawn rather than shown with their marks, a picture's image key as [Image]. Words that only
    /// hold a card, and another connector's card-like words, stay as they are.
    @Test func larkRichText() throws {
        func parse(_ text: String, markdown: Bool = true, lark: Bool = true) -> ThreadRichText? {
            ThreadRichText.parse(text, markdown: markdown, larkCard: lark, image: "[图片]")
        }
        #expect(parse("<card title=\"示例通知\">\n**第一项** 已完成\n![Image](img_example)\n</card>") == ThreadRichText(cardTitle: "示例通知", body: "**第一项** 已完成\n[图片]"))
        #expect(parse("<card>\n示例正文\n</card>", markdown: false) == ThreadRichText(cardTitle: "", body: "示例正文"))
        #expect(parse("**示例** ![Image](img_v3_example) ![x](https://example.com/a.png)") == ThreadRichText(cardTitle: nil, body: "**示例** [图片] ![x](https://example.com/a.png)"))
        #expect(parse("示例 <card>里面</card>", markdown: false) == nil)
        #expect(parse("<card title=\"a\"><card>b</card></card>", markdown: false) == nil)
        #expect(parse("<card>示例</card>", markdown: false, lark: false) == nil)
        #expect(parse("**示例** ![Image](key)", lark: false) == ThreadRichText(cardTitle: nil, body: "**示例** ![Image](key)"))
    }

    /// A Lark item opens as its conversation: a hub that could not say the chat's kind (lark-cli's message list says
    /// nothing of it) sent an empty kind, and the item opened as its plain text, every message run together.
    @Test func larkPageIsAConversation() {
        func chat(_ kind: String) -> ThreadChat { ThreadChat(id: "larkcli:chat:oc_example", name: "", kind: kind) }
        #expect(InboxConnectorDetailView.isConversation(chat(""), source: "larkcli"))
        #expect(InboxConnectorDetailView.isConversation(chat("group"), source: "larkcli"))
        #expect(!InboxConnectorDetailView.isConversation(chat(""), source: "examplechat"))
        #expect(!InboxConnectorDetailView.isConversation(chat("none"), source: "larkcli"))
    }

    /// The watch lists a connector's item under its connector's name and the label the connector gave it (#2727).
    @Test func watchSourceLine() throws {
        func item(_ connector: String, name: String = "Example Chat") throws -> InboxItem {
            try JSON.decoder.decode(InboxItem.self, from: Data(#"""
            {"id":"examplechat:1","source":"examplechat","kind":"group","account":"a","account_name":"\#(name)","title":"示例群",
             "preview":"","count":1,"connector":\#(connector)}
            """#.utf8))
        }
        #expect(WatchInboxText.connectorSource(try item(#"{"thread":"primary.example-group","label":"@我"}"#)) == "Example Chat · @我")
        #expect(WatchInboxText.connectorSource(try item(#"{"thread":"primary.example-group","label":"转账"}"#)) == "Example Chat · 转账")
        #expect(WatchInboxText.connectorSource(try item(#"{"thread":"primary.example-group"}"#)) == "Example Chat")
        #expect(WatchInboxText.connectorSource(try item(#"{"thread":"t"}"#, name: "")) == "examplechat")
        #expect(try item(#"{"thread":"t","label":"转账"}"#).kindLabel == "转账") // the row's tag on the phone: the connector's word
    }
}

