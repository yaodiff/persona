import Foundation
import PersonaKit
import Testing
@testable import Persona
import PersonaWire

/// Settings › Channels (docs/extensions/channels.md): what the hub sends decodes (and what it leaves out falls back), the list's
/// sections and words, a channel's changes go out as only what changed, binding is the connectors' steps on …/bind
/// (a QR code, the Feishu bot's pairing code), and a message's source is read from its client_msg_id. Made-up values.
@MainActor
struct ChannelsModelTests {
    private static let list = #"""
    {"channels":[
     {"id":"app","name":"Persona","kind":"app","source":"builtin","installed":true,"enabled":true,"state":"connected","bind":"none",
      "read_only":true,"surfaces":["iPhone","Mac"],"capabilities":{"format":"markdown","asks":"buttons"}},
     {"id":"wechat","name":"微信","kind":"im","source":"plugin","plugin":"wechat","installed":true,"enabled":true,"state":"connected",
      "account":{"name":"微信用户 a1b2c3"},"bind":"qr","last_event_at":"2026-11-04T10:00:00Z",
      "capabilities":{"images_in":true,"voice_in":true,"format":"plain","max_text":3500,"asks":"text","push":true,"typing":true},
      "target":{"kind":"main"},"features":[{"id":"asks","title":"提问和批准","on":true},{"id":"sync","title":"同步主对话","on":false}]},
     {"id":"examplebroken","name":"Example Team","kind":"im","source":"plugin","installed":true,"enabled":true,"state":"error",
      "error":"令牌失效了，重新绑定","bind":"fields","account":{"name":"示例团队"},"capabilities":{}},
     {"id":"lark_agent","name":"飞书 /agent","kind":"mention","source":"builtin","installed":true,"enabled":false,"state":"disabled",
      "bind":"config","read_only":true,"capabilities":{"asks":"none"}},
     {"id":"examplemarket","name":"Example Rooms","kind":"im","source":"market","plugin":"example-rooms","market":"example",
      "installed":false,"enabled":false,"state":"not_installed","bind":"none","description":"在示例聊天室里和 Persona 对话","capabilities":{}}]}
    """#

    private static let wechat = #"""
    {"id":"wechat","name":"微信","kind":"im","source":"plugin","plugin":"wechat","installed":true,"enabled":true,"state":"connected",
     "account":{"name":"微信用户 a1b2c3"},"bind":"qr","capabilities":{"format":"plain","asks":"text"},"target":{"kind":"main"},
     "features":[{"id":"asks","title":"提问和批准","on":true}],"waiting":{"answers":1,"asks":0},
     "conversations":[{"machine_id":"server","session_id":"s1","title":"今天要处理的事","at":"2026-11-04T10:00:00Z"}],
     "settings_url":"settings/plugins/wechat"}
    """#

    private func hubWithList() -> FakeHub {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/channels", json: Self.list)
        return hub
    }

    @Test func theListDecodesIntoThreeSections() async throws {
        let hub = hubWithList()
        let page = ChannelsModel(deps: .init(api: { hub.client }))
        await page.load()
        let list = try #require(page.channels)
        #expect(page.error == nil && list.count == 5)
        #expect(page.sections.map(\.title) == ["Persona", "已接入", "可以安装"])
        #expect(page.sections[1].channels.map(\.id) == ["wechat", "examplebroken", "lark_agent"])
        #expect(ChannelText.summary(list) == "1 个已连接 · 1 个出错" && ChannelText.hasProblem(list))
        let wechat = list[1]
        #expect(wechat.capabilities.maxText == 3500 && wechat.capabilities.asks == "text" && wechat.lastEventAt != nil)
        #expect(wechat.features.map(\.id) == ["asks", "sync"] && wechat.target == .main)
        #expect(ChannelText.subtitle(list[0]) == "iPhone · Mac")
        #expect(ChannelText.subtitle(wechat) == "微信用户 a1b2c3")
        #expect(ChannelText.subtitle(list[2]) == "令牌失效了，重新绑定" && ChannelText.state(list[2]) == "出错了")
        #expect(ChannelText.subtitle(list[4]) == "在示例聊天室里和 Persona 对话" && ChannelText.state(list[4]) == "没安装")
        // What it can and cannot do from here.
        #expect(wechat.bindsHere && wechat.isBound && !wechat.canBind && wechat.editable)
        #expect(list[2].isBound && list[2].bindsHere) // broken, with an account: unbind, not bind
        #expect(!list[3].bindsHere && !list[3].editable) // /agent: hub.json's
        #expect(!list[4].bindsHere && !list[4].editable)
        #expect(ChannelsView.destination(list[4]) == .marketExtension(market: "example", id: "example-rooms"))
        #expect(ChannelsView.destination(wechat) == .channel("wechat"))
    }

    /// A newer hub's fields this app does not know are ignored; the ones an older hub leaves out fall back.
    @Test func whatIsMissingFallsBack() throws {
        let c = try JSON.decoder.decode(HubChannel.self, from: Data(#"{"id":"x","name":"X","future":{"a":1}}"#.utf8))
        #expect(c.kind == "im" && c.state == "not_connected" && c.bind == "none" && c.features.isEmpty && !c.readOnly && c.installed)
        #expect(ChannelText.state(HubChannel(id: "y", name: "Y", state: "something_new")) == "未连接")
    }

    @Test func aHubWithoutChannelsShowsTheError() async {
        let hub = FakeHub() // no route: 404, as an older hub
        let page = ChannelsModel(deps: .init(api: { hub.client }))
        await page.load()
        #expect(page.channels == nil && page.error != nil)
    }

    @Test func changesSendOnlyWhatChanged() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/channels/wechat", json: Self.wechat)
        var bodies: [[String: JSONValue]] = []
        hub.on("PUT", "/api/v1/channels/wechat") { req in
            bodies.append(try JSON.decoder.decode([String: JSONValue].self, from: req.body ?? Data()))
            return Data(Self.wechat.utf8)
        }
        hub.on("DELETE", "/api/v1/channels/wechat/binding", json: Self.wechat)
        hub.on("POST", "/api/v1/channels/wechat/test", json: #"{"ok":true}"#)
        let page = ChannelModel(id: "wechat", deps: .init(api: { hub.client }))
        await page.load()
        let d = try #require(page.detail)
        #expect(d.waiting.answers == 1 && d.conversations.first?.title == "今天要处理的事")
        #expect(ChannelText.page(d.settingsURL) == .plugin("wechat"))
        await page.setEnabled(false)
        await page.setTarget(.conversation(SessionKey(group: "", machineID: "server", sessionID: "s1")))
        await page.setFeature("sync", on: true)
        #expect(bodies.count == 3)
        #expect(bodies[0] == ["enabled": .bool(false)])
        #expect(bodies[1] == ["target": .object(["kind": .string("conversation"), "machine_id": .string("server"), "session_id": .string("s1")])])
        #expect(bodies[2] == ["features": .object(["sync": .bool(true)])])
        await page.unbind()
        #expect(hub.sent("DELETE", "/api/v1/channels/wechat/binding").count == 1)
        await page.test()
        #expect(page.testResult?.hasPrefix("发出去了") == true)
    }

    @Test func aRefusedChangeSaysWhy() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/channels/wechat", json: Self.wechat)
        hub.on("PUT", "/api/v1/channels/wechat") { _ in throw APIError(status: 400, message: "target conversation needs machine_id and session_id") }
        let page = ChannelModel(id: "wechat", deps: .init(api: { hub.client }))
        await page.load()
        await page.setTarget(ChannelTarget(kind: "conversation"))
        #expect(page.error?.contains("machine_id") == true)
        #expect(page.channel?.target == .main) // what the hub had stays
    }

    /// Binding is the connectors' sign-in steps on …/bind: WeChat's QR code is asked again until it is scanned.
    @Test func bindingByQRCodePollsUntilDone() async throws {
        let hub = FakeHub()
        var polls = 0
        hub.on("POST", "/api/v1/channels/wechat/bind") { req in
            struct Body: Decodable { var step: String }
            let step = (try? JSONDecoder().decode(Body.self, from: req.body ?? Data()))?.step ?? "?"
            if step == "qr" { polls += 1 }
            return Data((polls >= 2 ? #"{"done":true,"account":"微信用户 a1b2c3"}"#
                : #"{"step":"qr","kind":"qr","url":"https://example.com/qr","interval":1,"qr":{"size":2,"rows":["10","01"]}}"#).utf8)
        }
        let m = ConnectorSignInModel(id: "wechat", deps: ChannelModel.bindingDeps(.init(api: { hub.client }), report: { _ in }))
        await m.start()
        #expect(m.prompt?.isQRCode == true && m.prompt?.polls == true)
        #expect(hub.sent("POST", "/api/v1/connectors/wechat/login").isEmpty) // the channel's own endpoint, not the connector's
        let done = await m.waitForAuthorization()
        #expect(done && m.prompt == nil)
    }

    /// The Feishu bot's pairing code: shown, and asked again until the bot has it.
    @Test func thePairingCodePolls() throws {
        let p = try JSON.decoder.decode(ConnectorPrompt.self, from: Data(
            #"{"step":"pair","kind":"pair_code","user_code":"482913","interval":3,"hint":"把这 6 位数字发给它"}"#.utf8))
        #expect(p.isPairCode && !p.isDeviceCode && p.polls && p.pollKey == "482913")
    }

    /// A field whose label and placeholder do not fit one row is laid out label above, field below (it took no focus
    /// when LabeledContent wrapped it); the short ones (Telegram's) stay on one row.
    @Test func longFieldsAreStacked() {
        typealias F = ConnectorPrompt.Field
        #expect(ConnectorSignInSteps.stacked(F(name: "token", label: "机器人令牌", secret: true, placeholder: "在 Example Chat 的开发者设置里生成")))
        #expect(!ConnectorSignInSteps.stacked(F(name: "phone", label: "手机号", placeholder: "+1 555 0100")))
        #expect(!ConnectorSignInSteps.stacked(F(name: "code", label: "验证码")))
    }

    @Test func settingsLinks() {
        #expect(ChannelText.page("settings/connectors/feishu-bot") == .feishuBot)
        #expect(ChannelText.page("settings/model-config") == .modelConfiguration)
        #expect(ChannelText.page("settings/plugins/wechat") == .plugin("wechat"))
        #expect(ChannelText.page("settings/plugins/a/b") == nil)
        #expect(ChannelText.page("https://example.com") == nil && ChannelText.page(nil) == nil)
    }

    @Test func targetWords() {
        #expect(ChannelText.target(nil) == "主对话" && ChannelText.target(.main) == "主对话")
        #expect(ChannelText.target(.new) == "新对话（下一条消息开）")
        #expect(ChannelText.target(ChannelTarget(kind: "conversation", title: "周报")) == "周报")
        #expect(ChannelText.target(ChannelTarget(kind: "agent", title: "box · 修 bug")) == "Agent · box · 修 bug")
    }

    @Test func capabilityLines() throws {
        let c = try JSON.decoder.decode(HubChannel.Capabilities.self, from: Data(
            #"{"images_in":true,"voice_in":true,"format":"plain","max_text":3500,"asks":"text","typing":true}"#.utf8))
        #expect(ChannelText.capabilities(c) == ["收文字、图片、语音", "回答是纯文字", "一条最多 3,500 字，长的拆开发", "提问和批准回复序号或文字", "处理时显示「正在输入」"])
    }

    /// 「来自 微信」: the client_msg_id's prefix names a channel the hub has; the app's UUIDs never do.
    @Test func whereAMessageCameFrom() throws {
        let list = try JSON.decoder.decode([String: [HubChannel]].self, from: Data(Self.list.utf8))["channels"] ?? []
        #expect(ChannelSource.of("wechat-message:1", in: list)?.name == "微信")
        #expect(ChannelSource.of("3f2a9c1e-1b2c-4d5e-8f90-123456789abc", in: list) == nil)
        #expect(!ChannelSource.possible("3F2A9C1E-1B2C-4D5E-8F90-123456789ABC") && !ChannelSource.possible(nil))
        #expect(ChannelSource.possible("feishu-om_x") && ChannelSource.of("feishu-om_x", in: list) == nil) // not in this hub's list
        #expect(ChannelSource.of("app-1", in: list) == nil) // the apps' channel is never a source
        #expect(ChannelSource.of("-x", in: list) == nil && ChannelSource.of("wechat", in: list) == nil)
    }

    @Test func sessionsCarryTheirChannels() throws {
        let json = #"{"machine_id":"server","session_id":"s1","title":"t","cwd":"/","status":"idle","started_by_persona":true,"assistant":true,"state":"idle","pending_interactions":0,"channels":["wechat","feishu"]}"#
        let s = try JSON.decoder.decode(Session.self, from: Data(json.utf8))
        #expect(s.channels == ["wechat", "feishu"])
    }
}
