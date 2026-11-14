import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Inbox phase 1 (docs/assistant/inbox.md): the new item fields as the hub sends them (the phase's fixture, every
/// state), the sections they put an item in, what a row says, where a chain entry goes, and the suggestion's buttons
/// against a hub in memory.
@MainActor
struct InboxEventsTests {
    /// Fixtures/inbox-events.json: the phase's fixture, one event in every state (made-up people and companies).
    nonisolated static let fixture: Data = {
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        return (try? Data(contentsOf: here.appending(path: "Fixtures/inbox-events.json"))) ?? Data()
    }()

    private func items() throws -> [String: InboxItem] {
        let list = try JSON.decoder.decode(InboxListing.self, from: Self.fixture).items
        #expect(list.count == 9)
        return Dictionary(list.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
    }

    private static func item(_ json: String) throws -> InboxItem {
        try JSON.decoder.decode(InboxItem.self, from: Data(json.utf8))
    }

    private static let base = #""id":"lark:f1:1","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"t","preview":"p","count":1"#

    // MARK: Decoding and sections

    /// Fixtures/inbox-events-live.json: what a hub on master listed for a made-up connector source (e2einbox), taken
    /// as it came: consecutive `handling` entries, `concluded`, bare source links, connector items.
    nonisolated static let live: Data = {
        let here = URL(fileURLWithPath: #filePath).deletingLastPathComponent()
        return (try? Data(contentsOf: here.appending(path: "Fixtures/inbox-events-live.json"))) ?? Data()
    }()

    @Test func decodesARealHubsListing() throws {
        let list = try JSON.decoder.decode(InboxListing.self, from: Self.live).items
        #expect(list.count == 6)
        let by = Dictionary(list.map { ($0.title, $0) }, uniquingKeysWith: { a, _ in a })
        for name in ["张三", "李四", "法务", "赵六"] { #expect(by[name]?.triage == .action, "\(name) needs you") }
        #expect(by["王五"]?.triage == .working && by["王五"]?.tier == "action")
        #expect(by["钱七"]?.triage == .fyi)
        #expect(list.allSatisfy { $0.isEvent && !$0.isDone })

        #expect(by["张三"]?.currentSuggestion?.kind == "reply" && by["张三"]?.suggestion?.version == 1)
        #expect(by["法务"]?.currentSuggestion?.kind == "agent" && by["法务"]?.suggestion?.version == 1)
        #expect(by["钱七"]?.currentSuggestion?.kind == "conclusion")
        #expect(InboxEventText.mark(try #require(by["李四"])) == .preparing)
        #expect(InboxEventText.mark(try #require(by["王五"])) == .working("等对方回复"))
        #expect(InboxEventText.mark(try #require(by["赵六"])) == .failed("AI 处理失败"))
        #expect(by["赵六"]?.handling?.error == "发送没有成功")

        let kinds = Set(list.flatMap { ($0.chain ?? []).map(\.kind) })
        #expect(kinds == ["received", "triaged", "handling", "confirmed", "sent", "failed", "concluded"])
        for it in list {
            let chain = try #require(it.chain)
            // A bare source link opens the source page; on the Mac, where the source is beside it, nothing more.
            #expect(InboxChainTarget.of(chain[0]) == .source(position: nil, uid: nil))
            #expect(InboxChainTarget.of(chain[0], sourceShown: true).navigates == false)
            // Every handling entry (two in a row once it suggests) opens the processing conversation.
            for e in chain where e.kind == "handling" {
                guard case .session(let key, manager: true) = InboxChainTarget.of(e) else { Issue.record("\(it.title): \(e.text)"); continue }
                #expect(key == it.handling?.sessionKey)
            }
        }
        // The badge counts Needs You only.
        #expect(list.filter { !$0.isDone && $0.triage == .action }.count == 4)
    }

    @Test func aConcludedEntryWithoutALinkOpensInPlace() {
        let e = InboxItem.ChainEntry(id: "c", kind: "concluded", text: "管理者的结论", detail: "不用回复")
        #expect(InboxChainTarget.of(e) == .inline)
        #expect(InboxEventText.symbol(e) == Theme.Icon.concluded)
    }

    @Test func decodesEveryStateOfTheFixture() throws {
        let all = try items()
        let reply = try #require(all["lark:oc_demo1:100"])
        #expect(reply.triage == .action && reply.isEvent)
        #expect(reply.suggestion?.version == 2 && reply.suggestion?.kind == "reply" && reply.suggestion?.isHandback == false)
        #expect(reply.recap?.hasPrefix("客户要周五前") == true)
        #expect(reply.handling?.state == "done" && reply.handling?.sessionKey == SessionKey(machineID: "m1", sessionID: "s-demo1"))
        let received = try #require(reply.chain?.first)
        #expect(received.kind == "received" && received.count == 3 && received.messages?.count == 3 && received.link?.position == "100")

        #expect(all["lark:oc_demo2:200"]?.urgent == true)
        #expect(all["lark:oc_demo3:300"]?.triage == .working) // status working, tier still action
        #expect(all["lark:oc_demo3:300"]?.tier == "action")
        #expect(all["lark:oc_demo3:300"]?.related?.first?.id == "mail:a1:review:55")
        #expect(all["mail:a1:ci:77"]?.triage == .working)
        #expect(all["mail:a1:contract:88"]?.chain?.first?.link?.uid == 88)
        #expect(all["lark:oc_demo6:600"]?.handling?.error == "处理超时了")
        #expect(all["lark:oc_demo7:700"]?.triage == .fyi)
        #expect(all["mail:a1:news:99"]?.triage == .noise)
        #expect(all["lark:oc_demo9:900"]?.isDone == true)
    }

    @Test func sectionsGoByStatusThenTier() throws {
        // An older hub: no status, the tier as before (none: Needs You).
        #expect(try Self.item("{\(Self.base)}").triage == .action)
        #expect(try Self.item("{\(Self.base),\"tier\":\"fyi\"}").triage == .fyi)
        // The hub's status wins; one this app does not know falls back to the tier.
        #expect(try Self.item("{\(Self.base),\"tier\":\"action\",\"status\":\"working\"}").triage == .working)
        #expect(try Self.item("{\(Self.base),\"tier\":\"noise\",\"status\":\"later\"}").triage == .noise)
        // `tier` never says In Progress: an odd one is Needs You, as an untriaged item.
        #expect(try Self.item("{\(Self.base),\"tier\":\"working\"}").triage == .action)
        #expect(InboxItem.Tier.allCases == [.action, .working, .fyi, .noise])
        // In Progress never folds; the others open and close from their titles.
        #expect(InboxItem.Tier.allCases.filter { !$0.folds } == [.working])
    }

    /// The badge (and the watch's Needs You) counts the Needs You section only: In Progress is out, an urgent one in.
    @Test func onlyNeedsYouCountsForTheBadge() throws {
        let open = try items().values.filter { !$0.isDone }
        let counted = Set(open.filter { $0.triage == .action }.map(\.id))
        #expect(counted == ["lark:oc_demo1:100", "lark:oc_demo2:200", "mail:a1:contract:88", "lark:oc_demo6:600"])
    }

    @Test func anOlderHubsItemIsNotAnEvent() throws {
        let old = try Self.item("{\(Self.base),\"tier\":\"action\",\"agent\":{\"state\":\"drafted\",\"draft\":\"好的\"}}")
        #expect(!old.isEvent && old.currentSuggestion == nil && InboxEventText.mark(old) == nil)
        #expect(old.agentDraft == "好的") // the old draft card draws it as before
    }

    @Test func aChainDecodesLeniently() throws {
        let it = try Self.item("""
        {\(Self.base),"chain":[
          {"id":"c1","at":"2026-11-05T10:00:00Z","kind":"teleported","text":"新的一种"},
          {"id":"c2","at":"2026-11-05T10:01:00Z","kind":"received","text":"收到","link":"oops","count":"three"},
          {"id":"c3","kind":"sent","text":"发了","link":{"type":"source","position":306}},
          {"kind":"handling","text":"管理者处理","link":{"type":"session","machine_id":"m1"}}
        ]}
        """)
        let chain = try #require(it.chain)
        #expect(chain.count == 4)
        #expect(chain[0].kind == "teleported" && InboxEventText.symbol(chain[0]) == Theme.Icon.dot)
        #expect(chain[1].link == nil && chain[1].count == nil && chain[1].text == "收到")
        #expect(chain[2].link?.position == "306" && chain[2].at == nil)
        #expect(!chain[3].id.isEmpty && chain[3].link?.sessionKey == nil)
        #expect(InboxChainTarget.of(chain[3]) == .nothing) // a session link without its session
    }

    // MARK: Rows

    @Test func rowsSayWhatTheEventIsAt() throws {
        let all = try items()
        #expect(InboxEventText.mark(all["lark:oc_demo1:100"]!) == .suggestion(kind: "建议回复", line: "可以，我周四下班前把报价发你。", symbol: Theme.Icon.reply))
        #expect(InboxEventText.mark(all["lark:oc_demo2:200"]!) == .preparing)
        #expect(InboxEventText.mark(all["lark:oc_demo3:300"]!) == .working("等对方回复"))
        #expect(InboxEventText.mark(all["mail:a1:ci:77"]!) == .working("Agent 在做"))
        #expect(InboxEventText.mark(all["mail:a1:contract:88"]!) == .suggestion(kind: "建议派 Agent", line: "对比新旧两版合同，列出改动的条款和风险", symbol: Theme.Icon.agents))
        #expect(InboxEventText.mark(all["lark:oc_demo6:600"]!) == .failed("AI 处理失败"))
        #expect(InboxEventText.mark(all["lark:oc_demo7:700"]!) == .suggestion(kind: "结论", line: "周报里你负责的两项都按计划，不用回复。", symbol: Theme.Icon.concluded))
        #expect(InboxEventText.mark(all["mail:a1:news:99"]!) == nil)
        #expect(InboxEventText.mark(all["lark:oc_demo9:900"]!) == nil) // done
    }

    /// On the iPhone's shell a chain's conversation (the AI's handling, an Agent's) goes over the item with the item's
    /// manager, and the Persona page and the tab stay as they were; elsewhere the handling one opens on the Persona page
    /// (#3114).
    @Test func aChainConversationIsPushedOverTheItemOnTheShell() {
        let model = AppModel()
        model.selectedTab = .chats
        model.openChat = nil
        let before = model.assistantCurrent
        let key = SessionKey(machineID: "box", sessionID: "h1")
        var pushed: [InboxChatRoute] = []
        InboxEventOpen.session(key, manager: true, owner: "m2", model: model) { pushed.append($0) }
        InboxEventOpen.session(key, manager: false, owner: nil, model: model) { pushed.append($0) }
        #expect(pushed == [InboxChatRoute(key: model.inHub(key), manager: "m2"), InboxChatRoute(key: model.inHub(key), manager: nil)])
        #expect(model.assistantCurrent == before && model.openChat == nil)
        #expect(model.selectedTab == .chats)
        InboxEventOpen.session(key, manager: true, owner: nil, model: model)
        #expect(model.assistantCurrent == model.inHub(key))
        #expect(model.selectedTab == .assistant)
    }

    @Test func aSuggestionsFirstLineAndButtons() throws {
        #expect(InboxEventText.firstLine("\n  第一行  \n第二行") == "第一行")
        let reply = InboxItem.Suggestion(version: 1, kind: "reply", text: "好")
        #expect(InboxEventText.actions(reply) == [.confirm("发送"), .edit, .reject])
        #expect(InboxEventText.actions(.init(version: 1, kind: "agent", text: "查")) == [.confirm("开始"), .edit, .reject])
        #expect(InboxEventText.actions(.init(version: 1, kind: "conclusion", text: "完")) == [.confirm("知道了"), .reject])
        let handback = InboxItem.Suggestion(version: 1, kind: "reply", text: "先问张三", handback: true)
        #expect(InboxEventText.actions(handback) == [.confirm("确认"), .edit, .reject])
        #expect(InboxEventText.kindTitle(handback) == "要你拿主意")
        // Only words make a suggestion to show.
        var it = try Self.item("{\(Self.base)}")
        it.suggestion = .init(version: 1, kind: "reply", text: "  ")
        #expect(it.currentSuggestion == nil)
    }

    @Test func whatTheManagerFiledSaysSo() throws {
        let filed = try Self.item("""
        {\(Self.base),"done_at":"2026-11-05T10:00:00Z","done_by":"manager","chain":[
          {"id":"c1","kind":"done","text":"管理者看完，不用管"}]}
        """)
        #expect(filed.isDone && InboxEventText.doneBy(filed) == "助手处理了")
        #expect(InboxEventText.symbol(filed.chain![0]) == Theme.Icon.done && InboxChainTarget.of(filed.chain![0]) == .nothing)
        let mine = try Self.item("{\(Self.base),\"done_at\":\"2026-11-05T10:00:00Z\",\"done_by\":\"user\"}")
        #expect(InboxEventText.doneBy(mine) == nil)
        // An FYI conclusion stays listed, with its card, until "got it".
        let fyi = try #require(try items()["lark:oc_demo7:700"])
        #expect(!fyi.isDone && fyi.triage == .fyi && fyi.currentSuggestion?.kind == "conclusion")
    }

    // MARK: Chain links

    @Test func chainEntriesGoWhereTheirLinksSay() throws {
        let all = try items()
        let chat = try #require(all["lark:oc_demo3:300"]?.chain)
        #expect(InboxChainTarget.of(chat[0]) == .source(position: "300", uid: nil))
        #expect(InboxChainTarget.of(chat[1]) == .inline) // triage: the reason in place
        #expect(InboxChainTarget.of(chat[2]) == .session(SessionKey(machineID: "m1", sessionID: "s-demo3"), manager: true))
        #expect(InboxChainTarget.of(chat[3]) == .inline) // you confirmed: the words
        let mail = try #require(all["mail:a1:ci:77"]?.chain)
        #expect(InboxChainTarget.of(mail[0]) == .source(position: nil, uid: 77))
        #expect(InboxChainTarget.of(mail[3]) == .session(SessionKey(machineID: "m1", sessionID: "s-demo4"), manager: false))

        let plain = InboxItem.ChainEntry(id: "s", kind: "sent", text: "发了", link: .init(type: "source"))
        #expect(InboxChainTarget.of(plain) == .source(position: nil, uid: nil))
        #expect(InboxChainTarget.of(plain, sourceShown: true) == .nothing) // the Mac shows the source beside it
        let web = InboxItem.ChainEntry(id: "w", kind: "sent", text: "评论了", link: .init(type: "source", url: "https://example.com/pr/1"))
        #expect(InboxChainTarget.of(web) == .url(URL(string: "https://example.com/pr/1")!))
        let other = InboxItem.ChainEntry(id: "i", kind: "related", text: "评审材料", link: .init(type: "item", id: "mail:a1:review:55"))
        #expect(InboxChainTarget.of(other) == .item("mail:a1:review:55"))
        #expect(InboxChainTarget.of(.init(id: "n", kind: "rejected", text: "你驳回了")) == .nothing)
        #expect(InboxChainTarget.inline.navigates == false && InboxChainTarget.item("x").navigates)
    }

    // MARK: The suggestion's buttons

    private static let path = "/api/v1/inbox/lark:oc_demo1:100/suggestion"
    nonisolated static let answered = """
    {"id":"lark:oc_demo1:100","source":"lark","kind":"dm","account":"feishu","account_name":"飞书","title":"张三","preview":"p","count":3,
     "status":"working","handling":{"state":"waiting"},"chain":[{"id":"c9","kind":"sent","text":"发了飞书消息"}]}
    """
    nonisolated static let staleBody = """
    {"code":"suggestion_stale","error":"建议已更新","item":{"id":"lark:oc_demo1:100","source":"lark","kind":"dm","account":"feishu",
     "account_name":"飞书","title":"张三","preview":"p","count":3,"status":"action","suggestion":{"version":3,"kind":"reply","text":"新的建议"}}}
    """

    private final class Applied { var items: [InboxItem] = [] }

    private func model(_ hub: FakeHub, _ applied: Applied) -> InboxSuggestionModel {
        let client = hub.client
        return InboxSuggestionModel(itemID: "lark:oc_demo1:100", send: { id, body in try await client.inboxSuggestion(id, body) },
                                    apply: { applied.items.append($0) })
    }

    private func body(_ r: LocalRequest) throws -> [String: JSONValue] {
        guard case .object(let o) = try JSON.decoder.decode(JSONValue.self, from: try #require(r.body)) else { return [:] }
        return o
    }

    @Test func confirmingSendsTheVersionShown() async throws {
        let hub = FakeHub(), applied = Applied()
        hub.on("POST", Self.path, json: Self.answered)
        let m = model(hub, applied)
        let s = InboxItem.Suggestion(version: 2, kind: "reply", text: "可以，我周四下班前把报价发你。")
        let outcome = await m.confirm(s)
        guard case .done(let item)? = outcome else { Issue.record("not done: \(String(describing: outcome))"); return }
        #expect(item.triage == .working && item.handling?.state == "waiting")
        #expect(applied.items.map(\.id) == ["lark:oc_demo1:100"])
        let sent = try body(try #require(hub.sent("POST", Self.path).first))
        #expect(sent["version"] == .number(2) && sent["action"] == .string("confirm"))
        #expect(sent["text"] == nil) // unchanged words: run as shown
        let clientID = sent["client_id"]?.string ?? ""
        #expect(UUID(uuidString: clientID) != nil)
        #expect(m.busy == nil && m.notice == nil && m.error == nil)
    }

    @Test func changedWordsGoWithTheConfirmation() async throws {
        let hub = FakeHub(), applied = Applied()
        hub.on("POST", Self.path, json: Self.answered)
        let m = model(hub, applied)
        let s = InboxItem.Suggestion(version: 2, kind: "reply", text: "可以")
        _ = await m.confirm(s, text: "  可以，周三给你  ")
        let sent = try body(try #require(hub.sent("POST", Self.path).first))
        #expect(sent["text"] == .string("可以，周三给你"))
    }

    @Test func aStaleSuggestionIsNotRunAndTheNewOneShows() async throws {
        let hub = FakeHub(), applied = Applied()
        hub.on("POST", Self.path) { _ in
            throw APIError(status: 409, message: "建议已更新", fields: ["code": "suggestion_stale"],
                           details: try JSON.decoder.decode(JSONValue.self, from: Data(Self.staleBody.utf8)))
        }
        let m = model(hub, applied)
        let outcome = await m.confirm(.init(version: 2, kind: "reply", text: "旧的"))
        guard case .stale(let item)? = outcome else { Issue.record("not stale: \(String(describing: outcome))"); return }
        #expect(item?.suggestion?.version == 3 && item?.suggestion?.text == "新的建议")
        #expect(applied.items.first?.suggestion?.version == 3) // redrawn with the hub's item
        #expect(m.notice == "建议已更新" && m.error == nil)
    }

    @Test func rejectingAndInstructing() async throws {
        let hub = FakeHub(), applied = Applied()
        hub.on("POST", Self.path, json: Self.answered)
        let m = model(hub, applied)
        let s = InboxItem.Suggestion(version: 4, kind: "reply", text: "好")
        let none = await m.instruct(s, "   ")
        #expect(none == nil && hub.sent("POST", Self.path).isEmpty) // no words, no request
        _ = await m.instruct(s, "别这么回，先问时间")
        _ = await m.reject(s)
        let sent = try hub.sent("POST", Self.path).map(body)
        #expect(sent.count == 2)
        #expect(sent[0]["action"] == .string("instruct") && sent[0]["text"] == .string("别这么回，先问时间") && sent[0]["version"] == .number(4))
        #expect(sent[1]["action"] == .string("reject") && sent[1]["text"] == nil)
    }

    @Test func aFailureKeepsTheSuggestion() async throws {
        let hub = FakeHub(), applied = Applied()
        hub.on("POST", Self.path) { _ in throw APIError(status: 502, message: "不确定发出去了没有", fields: ["code": "assistant_in_doubt"]) }
        let m = model(hub, applied)
        let outcome = await m.confirm(.init(version: 1, kind: "reply", text: "好"))
        #expect(outcome == .failed("不确定发出去了没有", nil))
        #expect(applied.items.isEmpty && m.error == "不确定发出去了没有")
    }
}
