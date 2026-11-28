import Foundation
import PersonaModuleAPI
import PersonaWire
import Testing
@testable import Persona

/// The iPhone follows the hub's `module.mail` flag (#15995; the owner's rule, #13430): with the module off nothing here
/// starts or changes mail, and a mail already in the Inbox still opens on the text the hub stored for it, without asking
/// any route under `/api/v1/mail`. With the module on, or a hub that publishes no module flags, nothing changes.
@MainActor struct MailModuleOffTests {
    private static let mailJSON = #"""
    {"id":"m1","source":"mail","kind":"mail","account":"acc1","account_name":"me@example.com","title":"Quarterly numbers","preview":"The preview the list has.","count":1,"version":"v1",
     "sender":"Example Sender","mail":{"account_id":"acc1","mailbox":"INBOX","uids":[41,42],"message_id":"<a@example.com>","from":{"name":"Example Sender","email":"sender@example.com"}}}
    """#
    private static let body = "Hello,\n\nHere are the numbers for the quarter.\n\nSee you Friday."
    private static let context = "邮箱 me@example.com 收到的未读邮件（共 2 封，下面是最新一封）：\n发件人：Example Sender <sender@example.com>\n收件人：me@example.com\n抄送：\n时间：2026-11-20T09:00:00Z\n主题：Quarterly numbers\n\n" + body

    private func mail(context: String? = nil) throws -> InboxItem {
        var it = try JSON.decoder.decode(InboxItem.self, from: Data(Self.mailJSON.utf8))
        it.context = context
        return it
    }

    private func lark() throws -> InboxItem {
        try JSON.decoder.decode(InboxItem.self, from: Data(#"{"id":"l1","source":"lark","kind":"dm","account":"feishu","account_name":"Lark","title":"t","preview":"p","count":1}"#.utf8))
    }

    private func json(context: String?, truncated: Any? = nil) throws -> String {
        var o = try #require(JSONSerialization.jsonObject(with: Data(Self.mailJSON.utf8)) as? [String: Any])
        if let context { o["context"] = context }
        if let truncated { o["mail_body_truncated"] = truncated }
        return String(decoding: try JSONSerialization.data(withJSONObject: o), as: UTF8.self)
    }

    // MARK: The access

    /// The one rule (`HubModules.allows`), for the mail flag: stored-only only with `modules` and without `module.mail`.
    @Test func theAccessFollowsTheHubsAnswer() {
        #expect(MailAccess(hub: .everyOn) == .live, "nothing heard, or an older hub")
        #expect(MailAccess(hub: HubModules(hello: ["file_upload", "connectors"])) == .live, "no marker: an older hub")
        #expect(MailAccess(hub: HubModules(hello: ["modules", "module.mail"])) == .live)
        #expect(MailAccess(hub: HubModules(hello: ["modules", "module.lark"])) == .storedOnly)
        #expect(MailAccess(hub: HubModules(hello: ["modules"])) == .storedOnly)
        #expect(MailAccess(hub: HubModules(stored: ["module.voice"])) == .storedOnly, "the remembered answer, before this launch's hello")
        #expect(MailAccess(hub: HubModules(stored: nil)) == .live)
        #expect(MailAccess.live.offersMail && !MailAccess.storedOnly.offersMail)
    }

    // MARK: Entry points

    @Test func theMailRowLeavesConnectorsWithTheModuleOff() throws {
        let list = try JSON.decoder.decode([HubConnector].self, from: Data(#"[{"id":"lark","name":"Lark","status":"ok"},{"id":"mail","name":"Mail","status":"ok"},{"id":"github","name":"GitHub","status":"ok"}]"#.utf8))
        #expect(ConnectorRow.rows(list, mail: false).map(\.id) == ["lark", list[2].listKey])
        #expect(ConnectorRow.rows([], mail: false).map(\.id) == ["lark"], "an older hub's list, or none yet")
        // On: as before this flag was read, with and without the argument.
        #expect(ConnectorRow.rows(list, mail: true) == ConnectorRow.rows(list))
        #expect(ConnectorRow.rows(list).map(\.id) == ["mail", "lark", list[2].listKey])
        #expect(ConnectorRow.rows([]).map(\.id) == ["mail", "lark"])
        #expect(ConnectorRow.rows(list, lark: false, mail: false).map(\.id) == [list[2].listKey])
    }

    @Test func aReplyToAMailIsOfferedOnlyWhileMailIsLive() throws {
        let mail = try mail(), lark = try lark()
        #expect(MailAccess.live.offersReply(to: mail) && MailAccess.live.offersReply(to: lark))
        #expect(!MailAccess.storedOnly.offersReply(to: mail))
        #expect(MailAccess.storedOnly.offersReply(to: lark), "other sources are untouched")
    }

    @Test func aSuggestedReplyToAMailKeepsOnlyReject() throws {
        let mail = try mail(), lark = try lark()
        let reply = InboxItem.Suggestion(version: 1, kind: "reply", text: "ok")
        let todo = InboxItem.Suggestion(version: 1, kind: "todo", text: "do")
        #expect(MailAccess.storedOnly.suggestionActions(reply, for: mail) == [.reject])
        #expect(MailAccess.storedOnly.suggestionActions(todo, for: mail) == InboxEventText.actions(todo), "a to-do needs no mail")
        #expect(MailAccess.storedOnly.suggestionActions(reply, for: lark) == InboxEventText.actions(reply))
        #expect(MailAccess.live.suggestionActions(reply, for: mail) == InboxEventText.actions(reply), "on: unchanged")
    }

    // MARK: The stored text

    @Test func theStoredTextIsTheMailsTextWithoutTheHubsHeaderBlock() {
        #expect(MailStoredText(context: Self.context, preview: "p") == .text(Self.body))
        // The list of previews the hub writes before it has read a body, or any other shape: as stored.
        let digest = "邮箱 me@example.com 收到的未读邮件：\n---\n发件人：A <a@example.com>\n收件人：me@example.com\n时间：t\n主题：s\nsnippet"
        #expect(MailStoredText(context: digest, preview: "p") == .text(digest))
        #expect(MailStoredText(context: "Plain words.\n\nMore words.", preview: "p") == .text("Plain words.\n\nMore words."))
        // Nothing stored: the preview; neither: none.
        #expect(MailStoredText(context: nil, preview: " The preview. ") == .text("The preview."))
        #expect(MailStoredText(context: "  \n", preview: "p") == .text("p"))
        #expect(MailStoredText(context: nil, preview: "") == .none)
    }

    @Test func anOpenedMailReadsTheStoredTextAndAsksNoMailRoute() async throws {
        let hub = FakeHub()
        let item = try mail()
        hub.on("GET", "/api/v1/inbox/m1", json: try json(context: Self.context, truncated: false))
        let model = MailStoredModel(deps: .init(item: { try await hub.client.inboxItem($0.id) }))
        #expect(model.state == .loading)
        await model.load(item)
        #expect(model.state == .shown(.text(Self.body), .whole))
        #expect(hub.requests.map { "\($0.method) \($0.path)" } == ["GET /api/v1/inbox/m1"])
        #expect(!hub.requests.contains { $0.path.hasPrefix("/api/v1/mail") }, "no message, no flags: opening marks nothing read")
    }

    @Test func aFailedReadShowsThePreviewAndNoError() async throws {
        let hub = FakeHub() // no route: 404
        let item = try mail()
        let model = MailStoredModel(deps: .init(item: { try await hub.client.inboxItem($0.id) }))
        await model.load(item)
        #expect(model.state == .shown(.text("The preview the list has."), .unknown), "the list's copy never says whether it is whole")
        #expect(hub.requests.map(\.path) == ["/api/v1/inbox/m1"])
    }

    /// `mail_body_truncated` of the item's page as the closed type: anything but a boolean is unknown, never whole.
    @Test func theClippedFlagIsDecodedAsAClosedType() async throws {
        func item(_ truncated: Any?) throws -> InboxItem {
            try JSON.decoder.decode(InboxItem.self, from: Data(try json(context: Self.context, truncated: truncated).utf8))
        }
        #expect(try item(true).storedClipping == .clipped)
        #expect(try item(false).storedClipping == .whole)
        #expect(try item(nil).storedClipping == .unknown, "an older hub, or the list's copy")
        #expect(try item("yes").storedClipping == .unknown, "a value this app does not know does not fail the item")
        #expect(try item(NSNull()).storedClipping == .unknown)
        #expect(try mail().storedClipping == .unknown)
        // What the reader says for each, and only then.
        for (flag, state) in [(true as Any?, MailStoredClipping.clipped), (false, .whole), (nil, .unknown)] {
            let hub = FakeHub()
            hub.on("GET", "/api/v1/inbox/m1", json: try json(context: Self.context, truncated: flag))
            let model = MailStoredModel(deps: .init(item: { try await hub.client.inboxItem($0.id) }))
            await model.load(try mail())
            #expect(model.state == .shown(.text(Self.body), state))
        }
        #expect(Set([MailStoredClipping.clipped, .whole, .unknown].map(MailStoredView.line)).count == 3)
        // A hub that says "whole" about an item with no stored text is not believed: the preview's length is unknown.
        let hub = FakeHub()
        hub.on("GET", "/api/v1/inbox/m1", json: try json(context: nil, truncated: false))
        let model = MailStoredModel(deps: .init(item: { try await hub.client.inboxItem($0.id) }))
        await model.load(try mail())
        #expect(model.state == .shown(.text("The preview the list has."), .unknown))
    }

    /// One place reads the module's flag: everything else takes `MailAccess`.
    @Test func onlyMailModuleNamesTheFlag() throws {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        var offenders: [String] = []
        for dir in ["Persona", "Shared"] {
            let files = FileManager.default.enumerator(at: root.appendingPathComponent(dir), includingPropertiesForKeys: nil)
            while let url = files?.nextObject() as? URL {
                guard url.pathExtension == "swift", url.lastPathComponent != "MailModule.swift" else { continue }
                let text = try String(contentsOf: url, encoding: .utf8)
                if text.contains("MailModule.descriptor") || text.contains("\"module.mail\"") { offenders.append(url.lastPathComponent) }
            }
        }
        #expect(offenders.isEmpty)
    }

    /// The live reader is the only caller of the message and flags routes, and it is built only for live access.
    @Test func onlyTheLiveReaderAsksTheMessageRoutes() throws {
        let root = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().appendingPathComponent("Persona")
        var callers: Set<String> = [], builders: Set<String> = []
        let files = FileManager.default.enumerator(at: root, includingPropertiesForKeys: nil)
        while let url = files?.nextObject() as? URL {
            guard url.pathExtension == "swift" else { continue }
            let text = try String(contentsOf: url, encoding: .utf8)
            if text.contains(".mailMessage(account:") || text.contains(".mailFlags(account:") || text.contains("Translator.shared.mail(") { callers.insert(url.lastPathComponent) }
            if text.contains("MailDetailView(summary:") { builders.insert(url.lastPathComponent) }
        }
        #expect(callers == ["MailViews.swift"])
        #expect(builders == ["MailStoredView.swift"], "only `MailReaderView`, under `case .live`")
    }
}
