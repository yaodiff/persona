import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The card adapter matches the web's semantics (web/src/lib/assistant-views.ts, AssistantCard.tsx): a conflict asks
/// again at the new version, an already-answered conflict takes the record it returns, and only replies lock the card.
@MainActor
struct AssistantViewSemanticsTests {
    private let scope = AssistantViewScope(group: "example-group", conversation: "example-conversation")
    private func view() -> ExtensionView {
        ExtensionView(sections: [ExtensionSection(items: [
            .field(ExtensionFieldComponent(key: "name-key", name: "task", label: "任务", kind: "text", value: "初始")),
            .button(ExtensionButtonComponent(title: "提交", action: ExtensionAction(id: "submit", submit: true, reply: "安排任务")))
        ])], fallback: "请填写任务")
    }
    private func record(_ input: AssistantViewSubmission) -> ExtensionViewAction {
        ExtensionViewAction(key: input.key, action: input.action, page: input.page, values: input.values,
                            clientMsgId: input.clientMsgID, status: "sent", revision: 1)
    }
    @Test func aViewChangedConflictKeepsTheDraftAndAsksNativelyAtTheNewVersion() async {
        var sent: [AssistantViewSubmission] = []
        let card = view(), store = AssistantViewsStore()
        var model: AssistantViewModel!
        model = AssistantViewModel(key: "view_example", scope: scope, view: card, version: 2, store: store,
            deps: .init(send: { input in
                sent.append(input)
                if sent.count == 1 { throw APIError(status: 409, message: "卡片已更新", fields: ["code": "view_changed"]) }
                return record(input)
            }, recover: { model.update(card, version: 3) }))
        model.values["task"] = "保留的草稿"
        _ = await model.perform(AssistantViewModel.actions(card)[0])
        #expect(sent.count == 1 && sent[0].version == 2)
        #expect(model.changed && model.attempt == nil && model.values["task"] == "保留的草稿")
        // The action has no confirm of its own: after the conflict it is still confirmed natively, at version 3.
        _ = await model.perform(AssistantViewModel.actions(card)[0])
        #expect(sent.count == 1 && model.pending?.version == 3)
        _ = await model.confirm()
        #expect(sent.count == 2 && sent[1].version == 3 && sent[1].confirmed && sent[1].values == ["task": "保留的草稿"])
        #expect(!model.changed && model.selected("submit"))
    }
    @Test func alreadyAnsweredAppliesTheRecordInTheConflictWithoutTheFeed() async {
        let card = view(), store = AssistantViewsStore()
        var recovered = 0
        let body: JSONValue = .object(["code": .string("already_answered"), "action": .object([
            "key": .string("view_example"), "action": .string("submit"), "values": .object(["task": .string("另一台设备的值")]),
            "client_msg_id": .string("other-device"), "status": .string("sent"), "revision": .number(2)])])
        let model = AssistantViewModel(key: "view_example", scope: scope, view: card, store: store,
            deps: .init(send: { _ in throw APIError(status: 409, message: "已选择", fields: ["code": "already_answered"], details: body) },
                        recover: { recovered += 1 }))
        _ = await model.perform(AssistantViewModel.actions(card)[0])
        #expect(recovered == 0 && model.attempt == nil)
        #expect(model.selected("submit") && model.values["task"] == "另一台设备的值" && model.record?.clientMsgId == "other-device")
    }
    @Test func linksAndPagesStayAvailableWhileAReplyWaitsOrIsInFlight() async {
        let push = ExtensionAction(id: "details", push: ExtensionPush(page: "details"))
        let pop = ExtensionAction(id: "back", pop: true)
        let reply = ExtensionAction(id: "submit", confirm: "确定？", reply: "安排")
        let open = ExtensionAction(id: "open", open: "https://docs.example.org/card")
        let details = ExtensionView(sections: [ExtensionSection(items: [.button(ExtensionButtonComponent(title: "返回", action: pop))])])
        let card = ExtensionView(sections: [ExtensionSection(items: [
            .button(ExtensionButtonComponent(title: "提交", action: reply)),
            .button(ExtensionButtonComponent(title: "打开", action: open)),
            .button(ExtensionButtonComponent(title: "详情", action: push))
        ])], fallback: "卡片", pages: ["details": details])
        let store = AssistantViewsStore()
        let model = AssistantViewModel(key: "view_example", scope: scope, view: card, store: store,
            deps: .init(send: { input in record(input) }))
        _ = await model.perform(reply)
        #expect(model.pending != nil && !model.isEnabled(reply))
        #expect(model.isEnabled(open) && model.isEnabled(push))
        #expect(await model.perform(open).url == URL(string: "https://docs.example.org/card"))
        // A reply in flight from another row of the same card holds the lock; links and pages still work.
        model.decline()
        #expect(store.begin(AssistantViewSubmission(key: "view_example", action: "submit", values: [:], clientMsgID: "in-flight",
                                                    version: 1, confirmed: true), scope: scope))
        #expect(model.running == "submit" && !model.isEnabled(reply) && model.isEnabled(open) && model.isEnabled(push))
        _ = await model.perform(push)
        #expect(model.page == "details" && model.isEnabled(pop))
    }
}
