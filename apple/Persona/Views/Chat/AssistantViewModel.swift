import Foundation
import Observation
import PersonaWire

/// A retry keeps the exact choice, client message id and version the user saw. Reply text is never sent by the client.
nonisolated struct AssistantViewSubmission: Hashable, Sendable {
    var key: String
    var action: String
    var page: String?
    var values: [String: String]
    var clientMsgID: String
    var version: Int64
    var confirmed: Bool
    var htmlText: String? = nil
}

nonisolated struct AssistantViewAttempt: Equatable, Sendable {
    var submission: AssistantViewSubmission
    var sending = true
    var error: String?
}

/// The interaction adapter for ExtensionViewStack, not another component renderer.
@MainActor @Observable
final class AssistantViewModel: ExtensionViewControlling {
    struct Deps {
        var send: (AssistantViewSubmission) async throws -> ExtensionViewAction
        var recover: () async -> Void = {}
        var htmlOpen: (ExtensionAssistantHTMLActionInput) async throws -> ExtensionAssistantHTMLOpenResult = { _ in
            throw APIError(status: 400, message: String(localized: "The hub doesn't support HTML card links"))
        }
    }
    struct Confirmation: Equatable {
        var action: ExtensionAction
        var page: String?
        var values: [String: String]
        var version: Int64
    }
    let key: String
    let scope: AssistantViewScope
    private(set) var root: ExtensionView
    private(set) var version: Int64
    private(set) var path: [String] = []
    private(set) var pending: Confirmation?
    /// The card changed under the last submit (409 view_changed): the next reply is confirmed natively at the new version.
    private(set) var changed = false
    private(set) var complete: Bool
    var registered: Bool
    @ObservationIgnored private let store: AssistantViewsStore
    @ObservationIgnored private let deps: Deps
    private var drafts: [String: [String: String]] = [:]

    init(key: String, scope: AssistantViewScope, view: ExtensionView, version: Int64 = 1,
         complete: Bool = true, registered: Bool = true, store: AssistantViewsStore = .shared, deps: Deps) {
        self.key = key; self.scope = scope; root = view; self.version = version
        self.complete = complete; self.registered = registered; self.store = store; self.deps = deps
        rebuildDrafts(view, previous: nil)
    }
    static let maxDepth = 4
    var page: String? { path.last }
    var view: ExtensionView { page.flatMap { root.pages?[$0] } ?? root }
    var record: ExtensionViewAction? { store.action(key, conversation: scope.conversation, group: scope.group) }
    var attempt: AssistantViewAttempt? { store.attempt(key, scope: scope) }
    var running: String? { attempt?.sending == true ? attempt?.submission.action : nil }
    var loadingMore: String? { running }
    var managesConfirmation: Bool { true }
    var fieldsEnabled: Bool { complete && record == nil && attempt == nil && pending == nil }
    var values: [String: String] {
        get {
            let typed = drafts[page ?? "", default: [:]]
            guard let record, (record.page ?? "") == (page ?? "") else { return typed }
            return typed.merging(record.values ?? [:]) { _, saved in saved }
        }
        set { if fieldsEnabled { drafts[page ?? ""] = newValue } }
    }
    func selected(_ action: String) -> Bool { record?.kind != "html" && record?.action == action }
    func isEnabled(_ action: ExtensionAction) -> Bool {
        let methods = [action.reply != nil, action.open != nil, action.push?.page != nil, action.pop == true].filter { $0 }.count
        guard complete, !action.id.isEmpty, methods == 1, action.submit != true || action.reply != nil else { return false }
        // Open, push and pop lock nothing: they stay available while a reply is waiting for confirmation or in flight.
        if action.reply != nil { return registered && record == nil && attempt == nil && pending == nil && running == nil }
        return true
    }

    /// Live updates keep still-present keyed controls and named pages, but cancel confirmation of an old definition.
    func update(_ next: ExtensionView, version: Int64, complete: Bool = true) {
        guard version >= self.version else { return }
        guard version > self.version || (version == self.version && next != root) || self.complete != complete else { return }
        pending = nil
        rebuildDrafts(next, previous: root)
        root = next; self.version = version; self.complete = complete
        path = Array(path.prefix { next.pages?[$0] != nil })
    }

    func perform(_ action: ExtensionAction, with chosen: [String: String] = [:]) async -> ExtensionViewModel.Outcome {
        guard isEnabled(action), Self.actions(view).contains(action) else { return .init() }
        var sent = action.submit == true ? submitted : [:]
        sent.merge(chosen) { _, choice in choice }
        let selection = Confirmation(action: action, page: page, values: sent, version: version)
        if action.confirm?.isEmpty == false || (changed && action.reply != nil) { pending = selection; return .init() }
        return await execute(selection, confirmed: false)
    }
    func decline() { pending = nil }
    func claimConfirmation() -> Confirmation? {
        guard let selection = pending else { return nil }
        pending = nil
        guard selection.version == version, selection.page == page else { return nil }
        return selection
    }
    func confirm() async -> ExtensionViewModel.Outcome {
        guard let selection = claimConfirmation() else { return .init() }
        return await confirm(selection)
    }
    func confirm(_ selection: Confirmation) async -> ExtensionViewModel.Outcome {
        guard selection.version == version, selection.page == page else { return .init(error: String(localized: "The card changed. Confirm again.")) }
        return await execute(selection, confirmed: true)
    }
    func choose(_ name: String, _ value: String, action: ExtensionAction) async -> ExtensionViewModel.Outcome {
        guard isEnabled(action), values[name] != value else { return .init() }
        // A pending confirmation does not commit the segmented choice to the local or shared answer.
        return await perform(action, with: [name: value])
    }
    var query: String {
        get { values["q"] ?? view.search?.value ?? "" }
        set { if running == nil && pending == nil { drafts[page ?? "", default: [:]]["q"] = newValue } }
    }
    func search() async -> ExtensionViewModel.Outcome {
        guard let search = view.search else { return .init() }
        var chosen = ["q": query]
        if let filter = view.filter { chosen[filter.name] = values[filter.name] ?? filter.value }
        return await perform(search.action, with: chosen)
    }

    func filter(_ value: String) async -> ExtensionViewModel.Outcome {
        guard let filter = view.filter else { return .init() }
        return await choose(filter.name, value, action: filter.action)
    }
    func more(_ more: ExtensionMore) async -> ExtensionViewModel.Outcome { await perform(more.action) }
    func back() { if !path.isEmpty { pending = nil; path.removeLast() } }

    func html(_ request: HTMLCardRequest, component: String, expectedVersion: Int64) async -> ExtensionViewModel.Outcome {
        guard complete, registered, expectedVersion == version,
              view.sections.flatMap(\.items).contains(where: {
                  if case .html(let html) = $0 { return html.key == component && !component.isEmpty }
                  return false
              }) else { return .init(error: String(localized: "The card changed. Confirm again.")) }
        switch request {
        case .reply(let text):
            guard record == nil, attempt == nil else { return .init() }
            return await send(AssistantViewSubmission(key: key, action: component, page: page, values: [:], clientMsgID: UUID().uuidString,
                version: expectedVersion, confirmed: true, htmlText: text))
        case .open(let url):
            let epoch = store.epoch
            let input = ExtensionAssistantHTMLActionInput(kind: "open", componentKey: component, page: page,
                expectedVersion: expectedVersion, url: url.absoluteString, clientMsgId: UUID().uuidString, confirmed: true)
            do {
                let result = try await deps.htmlOpen(input)
                guard store.epoch == epoch, version == expectedVersion, result.version == expectedVersion,
                      AssistantViewLink.allowed(result.open), let target = URL(string: result.open) else {
                    return .init(error: String(localized: "The card changed. Confirm the link again."))
                }
                return .init(url: target)
            } catch {
                if let api = error as? APIError, api.code == "view_changed" { await deps.recover() }
                return .init(error: error.localizedDescription)
            }
        }
    }

    /// Explicit recovery after an unknown transport result reuses the saved request, never another choice or id.
    func retry() async -> ExtensionViewModel.Outcome {
        guard record == nil, let attempt, !attempt.sending else { return .init() }
        return await send(attempt.submission)
    }
    private func execute(_ selection: Confirmation, confirmed: Bool) async -> ExtensionViewModel.Outcome {
        let action = selection.action
        guard selection.version == version, complete, Self.actions(view).contains(action) else {
            return .init(error: String(localized: "The card changed. Confirm again."))
        }
        if let target = action.open {
            guard AssistantViewLink.allowed(target), let url = URL(string: target) else { return .init(error: String(localized: "Can't open this card target")) }
            return .init(url: url)
        }
        if let page = action.push?.page {
            guard root.pages?[page] != nil else { return .init(error: String(localized: "The card's detail page no longer exists")) }
            guard path.count + 1 < Self.maxDepth else { return .init(error: String(localized: "Already at the card's deepest level")) }
            path.append(page); return .init()
        }
        if action.pop == true { back(); return .init() }
        guard action.reply != nil, registered, record == nil, attempt == nil else { return .init() }
        let submission = AssistantViewSubmission(key: key, action: action.id, page: selection.page, values: selection.values,
                                                  clientMsgID: UUID().uuidString, version: selection.version, confirmed: confirmed)
        return await send(submission)
    }
    private func send(_ submission: AssistantViewSubmission) async -> ExtensionViewModel.Outcome {
        let epoch = store.epoch
        guard store.begin(submission, scope: scope) else { return .init() }
        do {
            let result = try await deps.send(submission)
            guard store.epoch == epoch else { return .init() }
            changed = false
            guard result.key == key, !result.clientMsgId.isEmpty,
                  store.receive(result, conversation: scope.conversation, group: scope.group) || record != nil else {
                store.finish(submission, scope: scope, error: String(localized: "The submission result is incomplete. Sync the status and try again."), uncertain: true)
                return .init(error: String(localized: "The submission result is incomplete. Sync the status and try again."))
            }
            return .init()
        } catch {
            guard store.epoch == epoch else { return .init() }
            if let api = error as? APIError, api.status == 409, api.code == "view_changed" {
                store.finish(submission, scope: scope, error: nil, uncertain: false)
                pending = nil
                changed = true
                await deps.recover()
                guard store.epoch == epoch else { return .init() }
                return .init(error: String(localized: "The card changed; your edits are kept. Review them and confirm again."))
            }
            if let api = error as? APIError, api.status == 409, api.code == "already_answered" {
                // The 409 carries the record another device made; the feed is only needed when it does not read.
                if let winner = Self.answered(in: api), winner.key == key {
                    store.receive(winner, conversation: scope.conversation, group: scope.group)
                }
                if record == nil { await deps.recover() }
                guard store.epoch == epoch else { return .init() }
                store.finish(submission, scope: scope, error: record == nil ? String(localized: "Chosen on another device. Syncing the result.") : nil, uncertain: record == nil)
                return .init()
            }
            let status = (error as? APIError)?.status ?? 0
            let uncertain = status == 0 || status >= 500
            store.finish(submission, scope: scope, error: error.localizedDescription, uncertain: uncertain)
            return .init(error: uncertain ? String(localized: "The submission isn't confirmed yet. Sync the status or retry the original choice; it won't be sent twice.") : error.localizedDescription)
        }
    }
    /// The `action` of an already_answered body (`{"code":"already_answered","action":{…}}`), if it reads.
    nonisolated static func answered(in error: APIError) -> ExtensionViewAction? {
        guard case .object(let body)? = error.details, let action = body["action"],
              let data = try? JSON.encoder.encode(action) else { return nil }
        return try? JSON.decoder.decode(ExtensionViewAction.self, from: data)
    }
    private var submitted: [String: String] {
        var result: [String: String] = [:]
        for item in view.sections.flatMap(\.items) {
            if case .field(let field) = item, field.kind != "secret" { result[field.name] = values[field.name] ?? "" }
        }
        return result
    }
    static func actions(_ view: ExtensionView) -> [ExtensionAction] {
        var actions: [ExtensionAction] = []
        if let search = view.search { actions.append(search.action) }
        if let filter = view.filter { actions.append(filter.action) }
        for section in view.sections {
            if let more = section.more { actions.append(more.action) }
            for component in section.items {
                switch component {
                case .button(let button): actions.append(button.action)
                case .row(let row): if let action = row.action { actions.append(action) }
                case .segmented(let item): actions.append(item.action)
                case .empty(let empty): if let button = empty.button { actions.append(button.action) }
                default: break
                }
            }
        }
        return actions
    }
    private func rebuildDrafts(_ next: ExtensionView, previous: ExtensionView?) {
        var pages = next.pages ?? [:]; pages[""] = next
        var oldPages = previous?.pages ?? [:]; if let previous { oldPages[""] = previous }
        var result: [String: [String: String]] = [:]
        for (page, view) in pages {
            let previousItems = oldPages[page]?.sections.flatMap(\.items) ?? []
            var values: [String: String] = [:]
            for component in view.sections.flatMap(\.items) {
                switch component {
                case .field(let field) where field.kind != "secret":
                    let old = previousItems.compactMap { item -> ExtensionFieldComponent? in
                        guard case .field(let old) = item, old.kind == field.kind,
                              (old.key ?? old.name) == (field.key ?? field.name) else { return nil }
                        return old
                    }.first
                    values[field.name] = old.flatMap { drafts[page]?[$0.name] } ?? field.value ?? (field.kind == "toggle" ? "false" : "")
                case .segmented(let item): values[item.name] = drafts[page]?[item.name] ?? item.value
                default: break
                }
            }
            if let filter = view.filter { values[filter.name] = drafts[page]?[filter.name] ?? filter.value }
            if let search = view.search { values["q"] = drafts[page]?["q"] ?? search.value ?? "" }
            result[page] = values
        }
        drafts = result
    }
}
