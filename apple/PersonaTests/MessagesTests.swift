import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Messages (Settings › Connectors › Messages): pages from the hub as Go writes them, paging both ways, and how a conversation is laid out.
/// Every name, number and text is made up.
@MainActor
struct MessagesTests {
    private static let zhang = #"{"handle":"+15550100","name":"张三"}"#
    private static func chat(_ id: String, _ name: String) -> String {
        #"{"id":"\#(id)","name":"\#(name)","members":[\#(zhang)],"services":["SMS","iMessage"],"last":"2026-10-30T08:00:00.123456789Z","preview":"示例","unread":2}"#
    }

    private func deps(_ hub: FakeHub) -> MessagesChatsModel.Deps { .init(api: { hub.client }) }

    @Test func chatsPageForwardAndKeepTheirOrder() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/imessage/chats") { req in
            let page = req.query["before"] == nil
                ? #"{"chats":[\#(Self.chat("imessage-000000000001", "张三")),\#(Self.chat("imessage-000000000002", "周末徒步"))],"more":true,"before":"9.imessage-000000000002"}"#
                : #"{"chats":[\#(Self.chat("imessage-000000000002", "周末徒步")),\#(Self.chat("imessage-000000000003", "12345"))]}"#
            return Data(page.utf8)
        }
        let page = MessagesChatsModel(deps: deps(hub))
        await page.load()
        #expect(page.loaded && page.failure == nil && page.more)
        let first = try #require(page.chats.first)
        #expect(first.name == "张三" && first.unread == 2 && first.services == ["SMS", "iMessage"] && first.members.first?.shown == "张三")
        #expect(abs(first.last.timeIntervalSince1970 - 1_793_347_200.123) < 0.01)
        await page.loadMore()
        #expect(page.chats.map(\.name) == ["张三", "周末徒步", "12345"]) // the one on both pages once
        #expect(!page.more)
        #expect(hub.sent("GET", "/api/v1/imessage/chats").map { $0.query["before"] } == [nil, "9.imessage-000000000002"])
        #expect(page.filtered("+1555").count == 3 && page.filtered("周末").map(\.name) == ["周末徒步"])
    }

    @Test func withoutPermissionTheListSaysSo() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/imessage/chats") { _ in
            throw APIError(status: 403, message: "macOS 不让 Persona 读", fields: ["code": "no_permission"])
        }
        let page = MessagesChatsModel(deps: deps(hub))
        await page.load()
        #expect(page.loaded && page.chats.isEmpty && page.failure == .noPermission("macOS 不让 Persona 读"))
    }

    @Test func olderMessagesGoInFront() async throws {
        let hub = FakeHub()
        let path = "/api/v1/imessage/chats/imessage-000000000001/messages"
        hub.on("GET", path) { req in
            let page = req.query["before"] == nil
                ? #"{"chat":\#(Self.chat("imessage-000000000001", "张三")),"messages":[{"id":"7","at":"2026-10-30T08:00:00Z","kind":"message","from_me":true,"text":"到了","edited":true},{"id":"9","at":"2026-10-30T08:05:00Z","kind":"message","sender":\#(Self.zhang),"undecoded":true}],"more":true,"before":"5.7"}"#
                : #"{"chat":\#(Self.chat("imessage-000000000001", "张三")),"messages":[{"id":"3","at":"2026-10-29T08:00:00Z","kind":"message","sender":\#(Self.zhang),"attachments":[{"kind":"image","name":"IMG_0001.HEIC","size":1234567}]},{"id":"7","at":"2026-10-30T08:00:00Z","kind":"message","from_me":true,"text":"到了"}]}"#
            return Data(page.utf8)
        }
        let thread = MessagesThreadModel(chatID: "imessage-000000000001", deps: deps(hub))
        await thread.load()
        #expect(thread.chat?.name == "张三" && thread.messages.map(\.id) == ["7", "9"])
        #expect(thread.messages[0].isMine && thread.messages[0].edited == true && thread.messages[1].undecoded == true)
        await thread.loadOlder()
        #expect(thread.messages.map(\.id) == ["3", "7", "9"] && !thread.more)
        #expect(thread.messages[0].attachments?.first?.kind == "image")
    }

    @Test func aConversationIsLaidOutLikeMessages() {
        let base = Date(timeIntervalSince1970: 1_793_347_200)
        func m(_ id: String, _ minutes: Double, mine: Bool = false, who: String? = nil, service: String = "iMessage", event: Bool = false) -> MessagesMessage {
            MessagesMessage(id: id, at: base.addingTimeInterval(minutes * 60), kind: event ? "event" : "message", fromMe: mine,
                            sender: who.map { MessagesPerson(handle: $0 == "张三" ? "+15550100" : "+15550101", name: $0) }, text: id, service: service)
        }
        let msgs = [
            m("a", 0, who: "张三", service: "SMS"), m("b", 1, who: "张三", service: "SMS"), m("c", 2, who: "李四"),
            m("d", 3, mine: true), m("e", 4, event: true), m("f", 5, who: "李四"), m("g", 120, who: "李四"),
        ]
        let lines = MessagesLine.lines(msgs, group: true, services: true, now: base.addingTimeInterval(86400 * 30))
        // The time first and after a pause of an hour; who wrote it when that changes; the service when it changes.
        #expect(lines.map { $0.time != nil } == [true, false, false, false, false, false, true])
        #expect(lines.map(\.sender) == ["张三", nil, "李四", nil, nil, "李四", "李四"])
        #expect(lines.map(\.service) == ["短信", nil, "iMessage", nil, nil, nil, nil])
        // A one-to-one conversation over one service: neither names nor services.
        let plain = MessagesLine.lines(msgs, group: false, services: false)
        #expect(plain.allSatisfy { $0.sender == nil && $0.service == nil })
    }

    /// Hand to Persona: the switch's state from the hub, turned off and on, and what a delete removed.
    @Test func theSwitchAndDelete() async throws {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/imessage/settings") { req in
            let on = String(decoding: req.body ?? Data(), as: UTF8.self).contains("true")
            return Data(#"{"machine":"MacBook","status":"ok","ai":\#(on)}"#.utf8)
        }
        hub.on("DELETE", "/api/v1/imessage/data", json: #"{"chats":6,"people":9,"inbox":1}"#)
        let api = hub.client
        let off = try await api.setMessagesAI(false)
        #expect(off.ai == false && off.status == "ok")
        #expect(try await api.setMessagesAI(true).ai == true)
        #expect(try await api.deleteMessagesData() == MessagesForgotten(chats: 6, people: 9, inbox: 1))
        // An older hub has no switch.
        let old = try JSON.decoder.decode(MessagesStatus.self, from: Data(#"{"machine":"MacBook","status":"ok"}"#.utf8))
        #expect(old.ai == nil)
    }

    /// Reply in Messages: the item's sms: link, with the AI twin's draft as the body.
    @Test func replyInMessagesOpensTheNumber() {
        #expect(InboxActionBar.systemURL("sms:+15550100", draft: nil)?.absoluteString == "sms:+15550100")
        #expect(InboxActionBar.systemURL("sms:+15550100", draft: "好的 明天见")?.absoluteString == "sms:+15550100&body=%E5%A5%BD%E7%9A%84%20%E6%98%8E%E5%A4%A9%E8%A7%81")
        #expect(InboxActionBar.systemURL("https://example.com/a", draft: "x")?.absoluteString == "https://example.com/a")
    }

    @Test func separatorTimes() throws {
        let cal = Calendar.current
        let now = try #require(cal.date(from: DateComponents(year: 2026, month: 11, day: 2, hour: 22)))
        let today = try #require(cal.date(from: DateComponents(year: 2026, month: 11, day: 2, hour: 8, minute: 5)))
        #expect(MessagesTime.separator(today, now: now).hasSuffix("08:05"))
        let old = try #require(cal.date(from: DateComponents(year: 2025, month: 10, day: 21, hour: 18)))
        #expect(MessagesTime.separator(old, now: now).contains("2025"))
    }
}
