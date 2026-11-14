import Foundation
import Testing
@testable import Persona

@MainActor
struct AssistantViewModelTests {
    private let scope = AssistantViewScope(group: "example-group", conversation: "example-conversation")
    private func view(confirm: String? = nil) -> ExtensionView {
        ExtensionView(sections: [ExtensionSection(items: [
            .field(ExtensionFieldComponent(key: "name-key", name: "task", label: "任务", kind: "text", value: "初始")),
            .button(ExtensionButtonComponent(title: "提交", action: ExtensionAction(id: "submit", confirm: confirm, submit: true, reply: "安排任务"))),
            .button(ExtensionButtonComponent(title: "打开", action: ExtensionAction(id: "open", open: "https://example.com/card")))
        ])], fallback: "请填写任务")
    }
    private func record(_ input: AssistantViewSubmission, status: String = "sent") -> ExtensionViewAction {
        ExtensionViewAction(key: input.key, action: input.action, page: input.page, values: input.values,
                            clientMsgId: input.clientMsgID, status: status, revision: 1)
    }
    @Test func usesTheSameControlInterfaceAndSubmitsOnlyCurrentFields() async {
        var sent: [AssistantViewSubmission] = []
        let card = view()
        let model: any ExtensionViewControlling = AssistantViewModel(key: "item/0", scope: scope, view: card,
            store: AssistantViewsStore(), deps: .init(send: { input in sent.append(input); return record(input) }))
        model.values = ["task": "读书", "unrelated": "不提交"]
        _ = await model.perform(AssistantViewModel.actions(card)[0])
        #expect(sent.count == 1 && sent.first?.values == ["task": "读书"])
        #expect(model.selected("submit") && !model.fieldsEnabled)
        _ = await model.perform(AssistantViewModel.actions(card)[0])
        #expect(sent.count == 1)
        #expect(model.isEnabled(AssistantViewModel.actions(card)[1]))
        #expect(await model.perform(AssistantViewModel.actions(card)[1]).url == URL(string: "https://example.com/card"))
    }
    @Test func confirmationCapturesTheValuesAndOnlyApprovalSends() async {
        var sent: [AssistantViewSubmission] = []
        let card = view(confirm: "确定安排？")
        let model = AssistantViewModel(key: "view_example", scope: scope, view: card, version: 7,
            store: AssistantViewsStore(), deps: .init(send: { input in sent.append(input); return record(input) }))
        model.values["task"] = "被确认的内容"
        _ = await model.perform(AssistantViewModel.actions(card)[0])
        #expect(sent.isEmpty && model.pending?.values == ["task": "被确认的内容"])
        model.values["task"] = "确认期间不能改"
        _ = await model.confirm()
        _ = await model.confirm()
        #expect(sent.count == 1 && sent[0].confirmed && sent[0].version == 7)
        #expect(sent[0].values == ["task": "被确认的内容"])
    }
    @Test func anUpdateCancelsConfirmationButKeepsAKeyedDraft() async {
        let card = view(confirm: "确认旧定义")
        var sent = 0
        let model = AssistantViewModel(key: "view_example", scope: scope, view: card,
            store: AssistantViewsStore(), deps: .init(send: { input in sent += 1; return record(input) }))
        model.values["task"] = "保留编辑"
        _ = await model.perform(AssistantViewModel.actions(card)[0])
        var next = card
        next.sections[0].items[0] = .field(ExtensionFieldComponent(key: "name-key", name: "renamed", label: "新的标签", kind: "text"))
        model.update(next, version: 2)
        #expect(model.pending == nil && model.values["renamed"] == "保留编辑" && model.values["task"] == nil)
        _ = await model.confirm()
        #expect(sent == 0)
    }
    @Test func anotherDeviceLocksTheCardAndCancelsApproval() async {
        let store = AssistantViewsStore(), card = view(confirm: "确认")
        var sent = 0
        let model = AssistantViewModel(key: "view_example", scope: scope, view: card, store: store,
            deps: .init(send: { input in sent += 1; return record(input) }))
        _ = await model.perform(AssistantViewModel.actions(card)[0])
        store.receive(ExtensionViewAction(key: "view_example", action: "submit", values: ["task": "另一台设备的值"],
                      clientMsgId: "other-client", status: "pending", revision: 2), conversation: scope.conversation, group: scope.group)
        _ = await model.confirm()
        #expect(sent == 0 && model.values["task"] == "另一台设备的值" && !model.fieldsEnabled)
        model.update(card, version: 3)
        #expect(model.selected("submit"))
    }
    @Test func unknownResultsSurviveRowRecreationAndReuseTheExactRequest() async {
        var sent: [AssistantViewSubmission] = []
        let card = view(), store = AssistantViewsStore()
        let deps = AssistantViewModel.Deps(send: { input in
            sent.append(input)
            if sent.count == 1 { throw URLError(.networkConnectionLost) }
            return record(input)
        })
        let first = AssistantViewModel(key: "item#example/0", scope: scope, view: card, store: store, deps: deps)
        first.values["task"] = "原选择"
        _ = await first.perform(AssistantViewModel.actions(card)[0])
        #expect(first.attempt?.sending == false && !first.fieldsEnabled)
        let recreated = AssistantViewModel(key: first.key, scope: scope, view: card, store: store, deps: deps)
        _ = await recreated.perform(AssistantViewModel.actions(card)[0])
        #expect(sent.count == 1)
        _ = await recreated.retry()
        #expect(sent.count == 2 && sent[0] == sent[1] && recreated.record?.status == "sent")
    }
    @Test func aDefiniteRejectionAllowsANewConfirmedChoice() async {
        let card = view(), store = AssistantViewsStore()
        let model = AssistantViewModel(key: "view_example", scope: scope, view: card, store: store,
            deps: .init(send: { _ in throw APIError(status: 400, message: "字段不正确") }))
        _ = await model.perform(AssistantViewModel.actions(card)[0])
        #expect(model.attempt == nil && model.fieldsEnabled)
    }
    @Test func alreadyAnsweredRecoversTheAuthoritativeRecord() async {
        let card = view(), store = AssistantViewsStore()
        let model = AssistantViewModel(key: "view_example", scope: scope, view: card, store: store,
            deps: .init(send: { _ in throw APIError(status: 409, message: "已选择", fields: ["code": "already_answered"]) }, recover: {
                store.receive(ExtensionViewAction(key: "view_example", action: "submit", values: ["task": "已选结果"],
                              clientMsgId: "other", status: "sent", revision: 1), conversation: scope.conversation, group: scope.group)
            }))
        _ = await model.perform(AssistantViewModel.actions(card)[0])
        #expect(model.selected("submit") && model.attempt == nil && model.values["task"] == "已选结果")
    }
    @Test func navigationStaysInsideTheCardAndDropsRemovedPages() async {
        let push = ExtensionAction(id: "details", push: ExtensionPush(page: "details"))
        let pop = ExtensionAction(id: "back", pop: true)
        let details = ExtensionView(sections: [ExtensionSection(items: [.button(ExtensionButtonComponent(title: "返回", action: pop))])])
        var card = ExtensionView(sections: [ExtensionSection(items: [.button(ExtensionButtonComponent(title: "详情", action: push))])], fallback: "详情", pages: ["details": details])
        let model = AssistantViewModel(key: "view_example", scope: scope, view: card, store: AssistantViewsStore(),
            deps: .init(send: { _ in Issue.record("navigation sent a reply"); throw CancellationError() }))
        _ = await model.perform(push)
        #expect(model.page == "details")
        _ = await model.perform(pop)
        #expect(model.page == nil)
        _ = await model.perform(push)
        card.pages = [:]
        model.update(card, version: 2)
        #expect(model.path.isEmpty)
    }
    @Test func presentationCacheKeepsDraftsWhenARowIsRecreatedAndResetsByScope() {
        let store = AssistantViewsStore(), card = view()
        let deps = AssistantViewModel.Deps(send: { _ in throw CancellationError() })
        let first = store.presentation(key: "item/0", scope: scope, view: card, version: 1, complete: true, registered: true, deps: deps)
        first.values["task"] = "保留编辑"
        let recreated = store.presentation(key: "item/0", scope: scope, view: card, version: 1, complete: true, registered: true, deps: deps)
        #expect(first === recreated && recreated.values["task"] == "保留编辑")
        let other = store.presentation(key: "item/0", scope: .init(group: "other-group", conversation: scope.conversation),
            view: card, version: 1, complete: true, registered: true, deps: deps)
        #expect(other !== first && other.values["task"] == "初始")
        store.forget()
        let reset = store.presentation(key: "item/0", scope: scope, view: card, version: 1, complete: true, registered: true, deps: deps)
        #expect(reset !== first && reset.values["task"] == "初始")
    }
    @Test func olderSnapshotsCannotRollBackACompletedVersion() {
        let card = view()
        let model = AssistantViewModel(key: "view_example", scope: scope, view: card, version: 3,
            store: AssistantViewsStore(), deps: .init(send: { _ in throw CancellationError() }))
        model.update(ExtensionView(fallback: "旧版本"), version: 2, complete: false)
        #expect(model.version == 3 && model.root == card && model.complete)
    }
    @Test func HTMLReplyAndDeclarativeReplyShareOneSelection() async {
        var sent: [AssistantViewSubmission] = []
        var card = view()
        card.sections[0].items.append(.html(ExtensionHtmlComponent(key: "drawing", html: "<p>方案</p>")))
        let model = AssistantViewModel(key: "view_example", scope: scope, view: card, version: 7,
            store: AssistantViewsStore(), deps: .init(send: { input in
                sent.append(input)
                var result = record(input)
                result.kind = "html"; result.version = input.version; result.text = input.htmlText
                return result
            }))
        _ = await model.html(.reply("选择这个方案"), component: "drawing", expectedVersion: 7)
        _ = await model.perform(AssistantViewModel.actions(card)[0])
        _ = await model.html(.reply("另一个选择"), component: "drawing", expectedVersion: 7)
        #expect(sent.count == 1 && sent[0].htmlText == "选择这个方案" && sent[0].values.isEmpty)
        #expect(sent[0].confirmed && sent[0].version == 7 && !model.fieldsEnabled)
        #expect(model.record?.kind == "html" && !model.selected("drawing"))
    }
    @Test func HTMLRequiresARegisteredCurrentPageComponentAndVersion() async {
        var sent = 0
        var card = view()
        card.sections[0].items.append(.html(ExtensionHtmlComponent(key: "drawing", html: "<p>方案</p>")))
        let model = AssistantViewModel(key: "view_example", scope: scope, view: card, version: 7, registered: false,
            store: AssistantViewsStore(), deps: .init(send: { input in sent += 1; return record(input) }))
        _ = await model.html(.reply("选择"), component: "drawing", expectedVersion: 7)
        model.registered = true
        _ = await model.html(.reply("选择"), component: "drawing", expectedVersion: 6)
        _ = await model.html(.reply("选择"), component: "missing", expectedVersion: 7)
        #expect(sent == 0 && model.record == nil && model.attempt == nil)
    }
    @Test func HTMLOpenDoesNotLockTheCardAndRejectsAChangedResult() async {
        var inputs: [ExtensionAssistantHTMLActionInput] = []
        var card = view()
        card.sections[0].items.append(.html(ExtensionHtmlComponent(key: "drawing", html: "<p>方案</p>")))
        let model = AssistantViewModel(key: "view_example", scope: scope, view: card, version: 7,
            store: AssistantViewsStore(), deps: .init(send: { _ in throw CancellationError() }, htmlOpen: { input in
                inputs.append(input)
                return ExtensionAssistantHTMLOpenResult(open: "https://example.com/card", version: inputs.count == 1 ? 7 : 8)
            }))
        let url = URL(string: "https://example.com/card")!
        let opened = await model.html(.open(url), component: "drawing", expectedVersion: 7)
        #expect(opened.url == url && model.record == nil && model.attempt == nil && model.fieldsEnabled)
        let changed = await model.html(.open(url), component: "drawing", expectedVersion: 7)
        #expect(changed.url == nil && changed.error != nil)
        #expect(inputs.count == 2 && inputs[0].confirmed && inputs[0].expectedVersion == 7)
    }
    @Test func unsupportedGeneratedComponentsUseFallbackRatherThanBlankCards() {
        let future = #"{"fallback":"文字说明","sections":[{"items":[{"type":"hologram","title":"新组件"}]}]}"#
        #expect(AssistantRichMarkdown.decode(future) == nil)
        // A card is drawn at `ExtensionUIContext.cardVersion` (6, #827): UI version 4's header is drawn, not left out.
        let header = #"{"fallback":"文字说明","sections":[{"items":[{"type":"header","title":"新组件"}]}]}"#
        #expect(AssistantRichMarkdown.decode(header) != nil)
        var card = view()
        card.sections[0].items.append(.html(ExtensionHtmlComponent(key: "drawing", html: "<p>方案</p>")))
        #expect(AssistantRichMarkdown.supports(card))
        card.sections[0].items.append(.unknown(type: "future"))
        #expect(!AssistantRichMarkdown.supports(card))
    }
    @Test func streamingAndUnregisteredRepliesStayDisabled() async {
        let card = view(), action = AssistantViewModel.actions(card)[0]
        let model = AssistantViewModel(key: "item/0", scope: scope, view: card, complete: false, registered: false,
            store: AssistantViewsStore(), deps: .init(send: { _ in Issue.record("streaming sent a reply"); throw CancellationError() }))
        #expect(!model.isEnabled(action) && !model.fieldsEnabled)
        model.update(card, version: 1, complete: true)
        #expect(!model.isEnabled(action))
        model.registered = true
        #expect(model.isEnabled(action))
    }
}
