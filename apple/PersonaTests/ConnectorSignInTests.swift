import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Settings › Connectors › Telegram: the sign-in a step at a time against the hub (docs/archive/extensions/plan-telegram.md), the switch, deleting
/// what it brought in. Made-up values only.
@MainActor
struct ConnectorSignInTests {
    private func model(_ hub: FakeHub, reported: @escaping (Error) -> Void = { _ in }) -> ConnectorSignInModel {
        ConnectorSignInModel(id: "telegram", deps: ConnectorDeps(api: { hub.client }, report: reported))
    }

    private func step(_ req: LocalRequest) -> (String, [String: String]) {
        struct Body: Decodable { var step: String; var values: [String: String] }
        let b = try? JSONDecoder().decode(Body.self, from: req.body ?? Data())
        return (b?.step ?? "?", b?.values ?? [:])
    }

    @Test func signsInAStepAtATime() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/connectors/telegram/login") { req in
            let (step, v) = self.step(req)
            switch step {
            case "":
                return Data(#"{"step":"credentials","hint":"去 my.telegram.org…","fields":[{"name":"api_id","label":"api_id"},{"name":"api_hash","label":"api_hash","secret":true}]}"#.utf8)
            case "credentials":
                return Data(#"{"step":"phone","fields":[{"name":"phone","label":"手机号","placeholder":"+1 555 0100"}]}"#.utf8)
            case "phone":
                return Data(#"{"step":"code","fields":[{"name":"code","label":"验证码"}]}"#.utf8)
            default:
                return Data((v["code"] == "12345" ? #"{"done":true,"account":"示例用户"}"#
                    : #"{"step":"code","error":"验证码不对","fields":[{"name":"code","label":"验证码"}]}"#).utf8)
            }
        }
        let m = model(hub)
        await m.start()
        #expect(m.prompt?.step == "credentials")
        #expect(m.prompt?.fields?.last?.secret == true)
        #expect(!m.canSend) // nothing filled in yet
        m.values = ["api_id": "1234567", "api_hash": String(repeating: "a", count: 32)]
        #expect(m.canSend)
        await m.next()
        #expect(m.prompt?.step == "phone")
        #expect(m.values.isEmpty) // a new step starts empty
        m.values["phone"] = "+1 555 0100"
        await m.next()
        m.values["code"] = "00000"
        await m.next()
        #expect(m.prompt?.step == "code")
        #expect(m.prompt?.error == "验证码不对")
        #expect(m.values["code"] == "00000") // the same step: what was typed stays to fix
        m.values["code"] = "12345"
        await m.next()
        #expect(m.prompt == nil) // done
        let sent = hub.sent("POST", "/api/v1/connectors/telegram/login").map { step($0).0 }
        #expect(sent == ["", "credentials", "phone", "code", "code"])
    }

    @Test func switchAndDelete() async throws {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/connectors/telegram/settings", json: #"{"id":"telegram","name":"Telegram","status":"disabled","enabled":false,"capabilities":["status","login"]}"#)
        hub.on("DELETE", "/api/v1/connectors/telegram/data", json: #"{"chats":3,"people":5,"inbox":2}"#)
        hub.on("POST", "/api/v1/connectors/telegram/logout", json: #"{"ok":true}"#)
        let m = model(hub)
        let c = await m.setEnabled(false)
        #expect(c?.enabled == false)
        #expect(c?.signsInHere == true)
        let body = hub.sent("PUT", "/api/v1/connectors/telegram/settings").first?.body ?? Data()
        #expect(String(decoding: body, as: UTF8.self).contains(#""enabled":false"#))
        await m.deleteData()
        #expect(m.deleted == "已删除：知识库里 3 个会话、5 个人，收件箱里 2 条。")
        await m.logout()
        #expect(hub.sent("POST", "/api/v1/connectors/telegram/logout").count == 1)
    }

    /// To Knowledge / To Inbox go to the hub as one field each; Delete Saved Knowledge says what went.
    @Test func feedsAndTheArchive() async throws {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/connectors/telegram/settings",
               json: #"{"id":"telegram","name":"Telegram","status":"ok","enabled":true,"archive":false,"inbox":true,"capabilities":["status","login","archive","inbox"]}"#)
        hub.on("DELETE", "/api/v1/connectors/telegram/archive", json: #"{"chats":3,"people":5,"inbox":0}"#)
        let m = model(hub)
        let c = await m.setFeed(.archive, on: false)
        #expect(c?.archive == false)
        #expect(c?.inbox == true)
        let body = hub.sent("PUT", "/api/v1/connectors/telegram/settings").first?.body ?? Data()
        #expect(String(decoding: body, as: UTF8.self) == #"{"archive":false}"#)
        await m.deleteArchive()
        #expect(m.archiveDeleted == "已删除：知识库里 3 个会话、5 个人。")
        #expect(m.deleted == nil)
        // A connector without an archive or an inbox, or a hub from before the switches: no toggles.
        let gh = try JSON.decoder.decode(HubConnector.self, from: Data(#"{"id":"github","name":"GitHub","status":"ok","capabilities":["status"]}"#.utf8))
        #expect(gh.archive == nil && gh.inbox == nil)
    }

    @Test func aConnectorWithoutSignIn() throws {
        let gh = try JSON.decoder.decode(HubConnector.self, from: Data(#"{"id":"github","name":"GitHub","status":"ok","capabilities":["status","hosts"]}"#.utf8))
        #expect(!gh.signsInHere)
        #expect(gh.enabled == nil) // a hub from before the switch: on
        #expect(ConnectorText.brand("telegram") == Theme.Brand.telegram)
        let off = try JSON.decoder.decode(HubConnector.self, from: Data(#"{"id":"telegram","name":"Telegram","status":"disabled","enabled":false}"#.utf8))
        #expect(ConnectorText.state(off) == "已关闭")
    }

    /// Switched off since the hub started, Telegram's program has not run to say it signs in: an empty list is not a
    /// connector signed in to on the hub's side (its page keeps the switch); no list at all is a hub from before them.
    @Test func aProgramNotStartedYet() throws {
        let off = try JSON.decoder.decode(HubConnector.self, from: Data(#"{"id":"telegram","name":"Telegram","status":"disabled","enabled":false,"capabilities":[]}"#.utf8))
        #expect(off.undescribed)
        #expect(!off.signsInHere)
        let old = try JSON.decoder.decode(HubConnector.self, from: Data(#"{"id":"github","name":"GitHub","status":"ok"}"#.utf8))
        #expect(!old.undescribed)
    }

    /// A chat bot (WeChat: a status and a sign-in) stores nothing: its switch enables it and there is nothing to delete.
    /// A connector that feeds the knowledge base or the inbox, or one not described yet, is as before.
    @Test func whatStoresData() throws {
        func c(_ caps: String) throws -> HubConnector {
            try JSON.decoder.decode(HubConnector.self, from: Data(#"{"id":"x","name":"X","status":"ok"\#(caps)}"#.utf8))
        }
        #expect(try !c(#","capabilities":["status","login"]"#).storesData)
        #expect(try c(#","capabilities":["status","login","archive"]"#).storesData)
        #expect(try c(#","capabilities":["status","inbox"]"#).storesData)
        #expect(try c(#","capabilities":[]"#).storesData)
        #expect(try c("").storesData)
    }

    /// Switched off or not signed in is gray, not Something Went Wrong in red; neither is a state from a newer hub.
    @Test func onlyAnErrorIsRed() throws {
        func c(_ status: String) throws -> HubConnector {
            try JSON.decoder.decode(HubConnector.self, from: Data(#"{"id":"telegram","name":"Telegram","status":"\#(status)"}"#.utf8))
        }
        #expect(ConnectorText.tone(try c("disabled")) == .secondary)
        #expect(ConnectorText.tone(try c("not_signed_in")) == .secondary)
        #expect(ConnectorText.tone(nil) == .secondary) // still loading
        #expect(ConnectorText.tone(try c("ok")) == Theme.positive)
        #expect(ConnectorText.state(try c("error")) == "出错了")
        #expect(ConnectorText.tone(try c("error")) == Theme.danger)
        #expect(ConnectorText.state(try c("paused_for_maintenance")) == "状态未知")
        #expect(ConnectorText.tone(try c("paused_for_maintenance")) == .secondary)
    }

    /// A read-only connector's item is answered in its own app: the action bar says which (Telegram, Messages), else Open.
    @Test func aReadOnlyItemSaysWhereItIsAnswered() throws {
        func item(_ source: String, _ open: String) throws -> InboxItem {
            try JSON.decoder.decode(InboxItem.self, from: Data("""
            {"id":"\(source):1","source":"\(source)","kind":"dm","account":"\(source)","account_name":"Telegram","title":"t","preview":"p",
             "count":1,"connector":{"thread":"u1","open_url":"\(open)"}}
            """.utf8))
        }
        #expect(InboxActionBar.openTitle(try item("telegram", "tg://resolve?domain=example"), sms: false) == "在 Telegram 里回复")
        #expect(InboxActionBar.openTitle(try item("imessage", "sms:+15550100"), sms: true) == "在信息里回复")
        #expect(InboxActionBar.openTitle(try item("github", "https://example.com/run/1"), sms: false) == "打开")
        // A plugin's connector (a chat service's) is not answered in Telegram.
        #expect(InboxActionBar.openTitle(try item("examplechat", "https://chat.example.com/c/1"), sms: false) == "打开")
    }

    /// Brand and wording come from the table by id (#1426): Telegram's stay as they were; a connector not in it (any
    /// plugin's) gets the generic icon and wording built from its own name, never Telegram's.
    @Test func wordingByConnector() throws {
        func c(_ id: String, _ name: String) throws -> HubConnector {
            try JSON.decoder.decode(HubConnector.self, from: Data(#"{"id":"\#(id)","name":"\#(name)","status":"not_signed_in"}"#.utf8))
        }
        #expect(ConnectorText.brand("github") == Theme.Brand.github)
        #expect(ConnectorText.brand("examplechat") == nil) // a plugin's: the generic icon
        #expect(ConnectorText.brand("example-notes") == nil)

        let tg = try c("telegram", "Telegram"), wx = try c("examplechat", "示例聊天"), other = try c("example-notes", "示例笔记")
        #expect(ConnectorText.signInFooter(tg) == "API 凭据和登录状态加密存在 hub 上，只显示 api_id；退出登录时一起删掉。")
        #expect(ConnectorText.signInFooter(wx) == "登录状态加密存在 hub 上，退出登录时删掉。")
        #expect(ConnectorText.signInFooter(other) == "登录状态加密存在 hub 上，退出登录时删掉。")

        // Only Telegram's sign-out ends the session over there; the others' authorization stays and says so.
        #expect(ConnectorText.logoutMessage(tg) == "hub 上存的 API 凭据和登录状态都会删掉，Telegram 那边这个设备会话也会结束。再用要重新登录。")
        for c in [wx, other, try c("github", "GitHub")] {
            #expect(ConnectorText.logoutMessage(c).contains("授权不会自动撤销"))
            #expect(!ConnectorText.logoutMessage(c).contains("Telegram"))
        }

        #expect(ConnectorText.profile("telegram").reads == "它的私聊和群")
        #expect(ConnectorText.profile("examplechat").reads == nil)
        #expect(ConnectorText.openInTitle("telegram", name: "Telegram") == "在 Telegram 里打开")
        #expect(ConnectorText.openInTitle("examplechat", name: "示例聊天") == "在示例聊天里打开")
    }
}
