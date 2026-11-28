import Foundation
import Observation

/// Hand to AI's logic (docs/assistant/inbox.md "Item actions", #1194): where the item goes, whether it is filed with it,
/// and the send. Persona's main conversation is the default: the item's hand-off (`POST /api/v1/inbox/{id}/handoff`)
/// puts the item and the ask there and links the item to it, so that conversation may write back to the item. An older
/// hub or manager answers 404 / 405, and the main chat gets the text as before. A new Persona conversation and a running
/// Agent get it as a message; once delivered, their session is recorded on the item without sending again, and a record
/// failure does not change the hand-off. Sending never moves the app: `open()` goes to where it went. The detail page's
/// `InboxHandOffBar` queues a send behind an Undo window (`queue`); `InboxHandOffSheet` sends at once and opens.
@MainActor @Observable
final class InboxHandOffModel {
    enum Target: Hashable, Sendable {
        /// Persona's main conversation, through the item's hand-off.
        case main
        /// A new Persona conversation.
        case newChat
        case agent(SessionKey)

        /// How the last choice is remembered (`lastTargetKey`): "main", "new", or "agent:" + the session's path.
        var stored: String {
            switch self {
            case .main: "main"
            case .newChat: "new"
            case .agent(let key): "agent:" + key.path
            }
        }

        /// Reads `stored` back; anything else is the main chat.
        init(stored: String) {
            if stored == "new" {
                self = .newChat
            } else if stored.hasPrefix("agent:"), let key = SessionKey(path: String(stored.dropFirst("agent:".count))) {
                self = .agent(key)
            } else {
                self = .main
            }
        }
    }

    /// The terminal outcome of one Agent hand-off. A waiting message is still being tracked, not a result yet.
    struct AgentSendResult: Equatable {
        let delivered: Bool
        let error: String?
        /// The outbox already initiated this item's Done write; the caller must not file it again.
        let filed: Bool
        /// It did not go in yet and the assistant keeps it to send by itself (#10194): the item is not filed as done.
        var kept = false

        static let sent = AgentSendResult(delivered: true, error: nil, filed: false)
        static func failed(_ error: String? = nil) -> AgentSendResult {
            AgentSendResult(delivered: false, error: error ?? String(localized: "The message couldn't be sent to the agent"), filed: false)
        }
    }

    /// The user default holding the last target chosen in the detail page's bar.
    static let lastTargetKey = "inbox.handOffTarget"
    /// How long a queued send waits for Undo: as long as a toast with a button stays (`ToastCapsule.shown`).
    static let undoWindow: Duration = .seconds(5)

    struct Deps {
        /// The item with its conversation text (`GET /api/v1/inbox/{id}`).
        var load: (String) async throws -> InboxItem
        /// The main chat's hand-off (item id, text, client id): the conversation it went to.
        var handOff: (String, String, UUID) async throws -> SessionKey
        /// Records an already-delivered new conversation or Agent hand-off (item id, session, kind, title, client id).
        var record: (String, SessionKey, InboxHandoffRecordKind, String, UUID) async throws -> Void
        /// Shows the message as sent in that conversation under the client id, which its transcript copy carries.
        var showMain: (SessionKey, String, UUID) -> Void
        /// An older hub's main chat: sends the text there as a plain message under the attempt's client id (starting
        /// the conversation when there is none); the conversation it went to.
        var plainToMain: (String, UUID) async throws -> SessionKey
        /// Starts a new Persona conversation with the text: the conversation.
        var startNew: (String) async throws -> SessionKey
        /// Sends the text to an Agent and waits for its pending message's terminal outcome, with the refusal's reason.
        /// The bool is the user's Also Mark as Done choice; the result says whether the outbox already filed it.
        var sendToAgent: (SessionKey, String, Bool) async -> InboxHandOffModel.AgentSendResult
        /// Follows a New Agent first message that its page already enqueued, through the same outbox delivery signal.
        var waitForNewAgent: (SessionKey, PendingMessage) async -> InboxHandOffModel.AgentSendResult = { _, _ in .failed() }
        /// Opens a conversation it went to: Persona's (the first two targets) or an Agent's.
        var open: (Target, SessionKey) -> Void
        /// Files the item.
        var markDone: () -> Void
    }

    var target: Target = .main {
        didSet { if !doneChosen { markDone = Self.marksDone(target) } }
    }
    /// Also Mark as Done is off for every target: handing a matter to a conversation or Agent starts work rather than
    /// finishing it, and an Agent's stop is reported back to this item. Switching targets keeps that default until the
    /// user explicitly chooses otherwise.
    private(set) var markDone = false
    private(set) var sending = false
    /// Why the last send failed.
    private(set) var error: String?
    /// Where the last send went, and to which target: what `open()` opens.
    private(set) var sent: (target: Target, key: SessionKey)?
    /// A send went from a sheet that is closing: `closed()` opens where it went.
    @ObservationIgnored private(set) var opensWhenClosed = false
    /// The ask waiting out the Undo window (`queue`).
    private(set) var queued: String?
    /// The last ask sent or tried, for Retry.
    private(set) var lastAsk: String?
    /// New Agent… is reading the item for New Agent's page (`newAgent`).
    private(set) var preparing = false

    @ObservationIgnored private var timer: Task<Void, Never>?

    @ObservationIgnored private var doneChosen = false
    /// The main chat's hand-off being tried, by its ask: the same ask sent again sends the first text again under the same
    /// client id, whatever came in on the item since, and the hub and the manager send it once. It stays through a failure
    /// that says nothing about a try under that id (`saysNothing`) and, once a try may have gone (`mayHaveGone`), through
    /// every answer short of success, a missing route included. Another ask or a reopened sheet is a new hand-off.
    @ObservationIgnored private var attempt: Attempt?
    /// The item title prepared for New Agent, used if its session title is not listed when the record goes out.
    @ObservationIgnored private var newAgentTitle: String?
    /// Stable for one New Agent preparation and consumed by its first Start callback.
    @ObservationIgnored private var newAgentClientID: UUID?
    @ObservationIgnored private let itemID: String
    @ObservationIgnored private let deps: Deps

    private struct Attempt {
        var ask: String
        var text: String
        var id: UUID
        /// A try under `id` may have gone.
        var inDoubt = false
    }

    /// A failed retry of a hand-off already in doubt: whatever it was answered, a try before it under the same client id
    /// may have gone, and the same ask sent again, with no other ask handed to the main chat in between, keeps that id.
    nonisolated private struct InDoubt: Error {}

    init(itemID: String, deps: Deps) {
        self.itemID = itemID
        self.deps = deps
    }

    /// The app's own: the hub of the moment, its Persona and the inbox store. The item and its hand-off are its
    /// manager's (its list's stamp), and so is a new conversation it starts.
    static func live(_ item: InboxItem, model: AppModel) -> InboxHandOffModel {
        let store = InboxStore.shared
        let visit = store.hubVisit
        func checkVisit() throws {
            guard visit == store.hubVisit else { throw CancellationError() }
        }
        func api() throws -> APIClient {
            try checkVisit()
            guard let api = model.api?.manager(model.itemManager(item.manager)) else { throw CancellationError() }
            return api
        }
        return InboxHandOffModel(itemID: item.id, deps: Deps(
            load: { id in
                let item = try await api().inboxItem(id)
                try checkVisit()
                return item
            },
            handOff: { id, text, clientID in
                let answer = try await api().inboxHandoff(id, text: text, clientID: clientID)
                try checkVisit()
                if let it = answer.item { store.put(it, manager: item.manager) }
                return answer.session
            },
            record: { id, key, kind, title, clientID in
                // A new conversation uses the item title; an Agent uses its actual session title when it is listed.
                let sessionTitle = kind == .agent ? model.session(key)?.displayTitle : nil
                try await api().inboxHandoffRecord(id, session: key, kind: kind, title: sessionTitle ?? title, clientID: clientID)
            },
            showMain: { key, text, clientID in
                guard visit == store.hubVisit else { return }
                model.showSent(key, text: text, images: [], id: clientID)
                Task { await model.refreshAssistant() } // the hand-off may have started the main conversation
            },
            plainToMain: { text, clientID in
                let key: SessionKey
                let client = try api()
                // The item's manager's main conversation: the Persona page's when it shows that manager and has read its
                // settings, else read from them (with none known, a start would replace the main conversation).
                let main: SessionKey?
                if model.assistantShows(item.manager), model.assistantConfig != nil {
                    main = model.assistantMain
                } else {
                    let st = try await client.assistant()
                    try checkVisit()
                    main = st.mainKey(group: client.group)
                }
                if let main, model.session(main)?.canSend == true {
                    model.sendNow(main, text: text) // a failure shows in the conversation, with Retry
                    key = main
                } else if let main { // sendNow does nothing for a conversation not listed
                    try await client.send(main, text: text, clientMsgID: clientID.uuidString, opID: clientID)
                    key = main
                } else {
                    let s = try await AssistantStart.run(receipts: model.hubFeatures.contains(AssistantStart.feature), op: clientID) { op in
                        try await client.startConversation(prompt: text, main: true, opID: op)
                    }
                    try checkVisit()
                    model.began(s, text: text)
                    key = s.key
                    Task { await model.refreshAssistant() }
                }
                return key
            },
            startNew: { text in
                let client = try api()
                let s = try await AssistantStart.run(receipts: model.hubFeatures.contains(AssistantStart.feature)) { op in
                    try await client.startConversation(prompt: text, opID: op)
                }
                try checkVisit()
                model.began(s, text: text)
                return s.key
            },
            sendToAgent: { key, text, file in
                guard visit == store.hubVisit else { return .failed() }
                return await model.sendForHandOff(key, text: text, item: item, file: file)
            },
            waitForNewAgent: { key, message in await model.waitForHandOffDelivery(key, message, followRetries: true) },
            open: { target, key in
                guard visit == store.hubVisit else { return }
                if case .agent = target {
                    model.openChat = key
                    model.selectedTab = .chats
                } else {
                    // The conversation itself, whichever assistant is current: the hub routes it to its own manager, and
                    // the item's names it until it is listed.
                    model.openAssistantConversation(key, manager: item.manager)
                }
            },
            markDone: {
                guard visit == store.hubVisit else { return }
                store.setDone(item, true, haptic: false)
            }))
    }

    static func marksDone(_: Target) -> Bool { false }

    /// Whether a hand-off can go to this Agent now: one that takes messages, still live, on a computer that is online.
    static func takes(_ session: Session, online: (MachineRef) -> Bool) -> Bool {
        !session.assistant && session.canSend && session.state.isLive && online(session.machineRef)
    }

    /// The same for a remembered target's session (#2121): an Agent that can't take it now counts as gone.
    static func takes(_ key: SessionKey, model: AppModel) -> Bool {
        model.session(key).map { takes($0) { model.machine($0)?.online == true } } ?? false
    }

    /// The Agents a hand-off may target, latest first. Shared by the sheet and the event page's bar.
    static func agents(_ sessions: [Session], limit: Int, online: (MachineRef) -> Bool) -> [Session] {
        sessions.filter { takes($0, online: online) }
            .sorted { ($0.updatedAt ?? .distantPast) > ($1.updatedAt ?? .distantPast) }
            .prefix(limit).map { $0 }
    }

    /// What New Agent… (#5933) opens New Agent's page with: the item as the card over the first message and as the
    /// context block ahead of it (the text an Agent target gets), the ask as the first message, the item's title as the
    /// name, and only the computers of the item's hub. Nothing is sent: Start on that page is the confirmation.
    struct NewAgentPrefill: Identifiable {
        let id = UUID()
        var card: ContextRef
        var prompt: String
        var nameHint: String
        var group: String
        /// Also Mark as Done for the Agent it starts.
        var markDone: Bool
        var contextBlock: String { card.xml }
    }

    /// Reads the item for New Agent's page with `ask` as the first message; nil when it couldn't be read (`error` says
    /// why, with no Retry: nothing went). Also Mark as Done is the user's choice, else off, as for every target.
    func newAgent(ask: String, group: String) async -> NewAgentPrefill? {
        guard !sending, !preparing else { return nil }
        preparing = true
        error = nil
        lastAsk = nil
        newAgentTitle = nil
        newAgentClientID = nil
        defer { preparing = false }
        do {
            let it = try await deps.load(itemID)
            newAgentTitle = it.title
            newAgentClientID = UUID()
            return NewAgentPrefill(card: InboxHandOffSheet.contextRef(it), prompt: ask, nameHint: it.title, group: group,
                                   markDone: doneChosen ? markDone : false)
        } catch {
            self.error = error.isOffline ? String(localized: "Can't reach the hub")
                : error.isTimeout ? String(localized: "The request timed out. Try again later.") : error.shown
            return nil
        }
    }

    /// New Agent's page started an Agent with the item (#5933): `open()` goes there and the item is filed when asked.
    /// Codex's start already carried its first message, so its accepted start is recorded here as before. An ordinary
    /// start calls `newAgentFirstMessage` first; that consumes the prepared record and waits for outbox delivery.
    func started(_ key: SessionKey, markDone: Bool) {
        sent = (.agent(key), key)
        if let title = newAgentTitle, let clientID = newAgentClientID {
            record(.agent(key), key, title: title, clientID: clientID)
            newAgentTitle = nil
            newAgentClientID = nil
        }
        if markDone { deps.markDone() }
    }

    /// An ordinary New Agent start enqueued its first message. Record only its terminal delivered outcome, never the
    /// local enqueue, a refusal, a failure or a discarded message. `nil` means the local enqueue was rejected. Consume
    /// the prepared identity before returning so duplicate callbacks cannot start another record, and capture only the
    /// immutable record and dependencies: dismissing the sheets does not retain their view models while the outbox waits.
    func newAgentFirstMessage(_ message: PendingMessage?, for key: SessionKey) {
        guard let title = newAgentTitle, let clientID = newAgentClientID else { return }
        newAgentTitle = nil
        newAgentClientID = nil
        guard let message else { return }
        let itemID = itemID, wait = deps.waitForNewAgent, record = deps.record
        Task {
            let result = await wait(key, message)
            guard result.delivered else { return }
            try? await record(itemID, key, .agent, title, clientID)
        }
    }

    func setMarkDone(_ on: Bool) {
        markDone = on
        doneChosen = true
    }

    /// Records only after delivery, without holding up the result. An older hub's missing-text response, or any other
    /// record failure, leaves the successful hand-off and its UI untouched.
    private func record(_ target: Target, _ key: SessionKey, title: String, clientID: UUID) {
        let kind: InboxHandoffRecordKind
        switch target {
        case .main: return
        case .newChat: kind = .conversation
        case .agent: kind = .agent
        }
        // Capture immutable values and the dependency, not mutable model state: the record outlives the page and model.
        let itemID = itemID, record = deps.record
        Task { try? await record(itemID, key, kind, title, clientID) }
    }

    /// Goes to the conversation the last send went to.
    func open() {
        guard let sent else { return }
        deps.open(sent.target, sent.key)
    }

    /// Goes where the last send went once the sheet that sent it has closed (`closed()`), not while it is still up. On the
    /// Mac, opening moves the window to another section and hides the page that presents the sheet, which then never
    /// closed (#5921).
    func openWhenClosed() {
        opensWhenClosed = sent != nil
    }

    /// The sheet has closed: opens where the last send went if `openWhenClosed()` asked for it, once. A sheet closed
    /// without a send (Cancel, Esc, a failed send) opens nothing.
    func closed() {
        guard opensWhenClosed else { return }
        opensWhenClosed = false
        open()
    }

    /// Sends `ask` after `window`, unless `undo()` comes first. Leaving the page does not stop it: the task keeps the
    /// model until it has sent.
    func queue(ask: String, after window: Duration = undoWindow) {
        timer?.cancel()
        queued = ask
        error = nil
        timer = Task {
            do { try await Task.sleep(for: window) } catch { return }
            // Not `sendQueued()`: that cancels the timer, which is this task, and the send under it.
            self.timer = nil
            _ = await self.fire()
        }
    }

    /// Takes the queued ask back before it went: the ask, or nil when nothing was waiting.
    @discardableResult func undo() -> String? {
        timer?.cancel()
        timer = nil
        defer { queued = nil }
        return queued
    }

    /// Sends the queued ask now (View while it waits); true once it went.
    func sendQueued() async -> Bool {
        timer?.cancel()
        timer = nil
        return await fire()
    }

    private func fire() async -> Bool {
        guard let ask = queued else { return false }
        queued = nil
        return await send(ask: ask)
    }

    /// Sends `ask` after the item as context; true once it went.
    func send(ask: String) async -> Bool {
        guard !sending else { return false }
        sending = true
        error = nil
        sent = nil
        lastAsk = ask
        defer { sending = false }
        let text: String
        var itemTitle: String?
        if target == .main, let pending = attempt, pending.ask == ask {
            text = pending.text
        } else {
            do {
                let item = try await deps.load(itemID)
                itemTitle = item.title
                text = InboxHandOffSheet.message(item, ask: ask)
            } catch {
                // Nothing went yet: it never says to look in the chat.
                self.error = error.isOffline ? String(localized: "Can't reach the hub")
                    : error.isTimeout ? String(localized: "The request timed out. Try again later.") : error.shown
                return false
            }
        }
        var filed = false
        do {
            switch target {
            case .main:
                try await handOff(ask: ask, text: text)
                return true
            case .newChat:
                sent = (target, try await deps.startNew(text))
            case .agent(let key):
                let result = await deps.sendToAgent(key, text, markDone)
                guard result.delivered else {
                    self.error = result.error
                    return false
                }
                filed = result.filed || result.kept // a kept message has not reached the agent: nothing is filed
                sent = (target, key)
            }
            if let sent { record(sent.target, sent.key, title: itemTitle ?? ask, clientID: UUID()) }
            if markDone && !filed { deps.markDone() }
            return true
        } catch {
            self.error = Self.words(for: error)
            return false
        }
    }

    /// What the sheet says for a failed send. Timeouts and no network are quiet elsewhere, but here the sheet stays open
    /// on them: a send that may have gone says to look first, since a new try may type it twice; no network at all says so.
    static func words(for error: Error) -> String? {
        if error.isCancellation { return nil }
        if mayHaveGone(error) {
            let look = String(localized: "It may have reached the computer already. Check the chat before sending it again.")
            guard error is InDoubt else { return look }
            return look + "\n" + String(localized: "To send it anyway, change the instructions or open Hand to AI again.")
        }
        if error.isOffline { return String(localized: "Can't reach the hub") }
        return error.shown
    }

    /// A failed send whose text may still have reached the conversation: no answer (a timeout here or behind the hub, the
    /// connection lost after the request went out, a 5xx without a `code` from something in front of the hub or from the
    /// hub unable to keep its receipt), the hub's pending or unknown receipt, or an answer saying so (`assistant_in_doubt`,
    /// `delivered: unknown` or `yes`, the node lost without `delivered: none`). The sheet says to look first, and the same
    /// ask again is the same hand-off.
    private static func mayHaveGone(_ error: Error) -> Bool {
        if error is InDoubt { return true }
        if error.isCancellation { return false }
        if (error as? URLError)?.code == .networkConnectionLost || error.isTimeout { return true }
        guard let api = error as? APIError else { return false }
        let code = api.code, delivered = api.fields["delivered"]
        return (api.status >= 500 && code == nil && delivered == nil) || code == "timeout" || code == "assistant_in_doubt"
            || delivered == "unknown" || delivered == "yes" || code == OpReceipt.unknownCode || code == OpReceipt.pendingCode
            || (code == "node_unreachable" && delivered != "none")
    }

    /// A failure that says nothing about an earlier try under the same client id: no connection to the hub, a cancelled
    /// request or no hub at all, the manager offline (`assistant_offline`, whose receipt the hub drops).
    private static func saysNothing(_ error: Error) -> Bool {
        error.isCancellation || (error.isOffline && !mayHaveGone(error)) || (error as? APIError)?.code == "assistant_offline"
    }

    private func handOff(ask: String, text: String) async throws {
        let tried = attempt.flatMap { $0.ask == ask ? $0 : nil } ?? Attempt(ask: ask, text: text, id: UUID())
        attempt = tried
        let to: SessionKey
        do {
            to = try await deps.handOff(itemID, text, tried.id)
        } catch let e as APIError where e.status == 404 || e.status == 405 {
            // After a try that may have gone, the manager may have lost the item since: look first, and keep the hand-off
            // so the same ask never sends it as a plain message as well.
            if tried.inDoubt { throw InDoubt() }
            // An older hub or manager: the text goes to the main chat as a plain message, with nothing to write back to.
            do {
                sent = (.main, try await deps.plainToMain(text, tried.id))
            } catch {
                settle(tried, after: error)
                throw error
            }
            attempt = nil
            if markDone { deps.markDone() }
            return
        } catch let error where Self.saysNothing(error) {
            throw error
        } catch {
            // After a try that may have gone, an answer under its id may be what the hub kept for the id, a relayed answer
            // cut off on the way (a bare `internal`) or, after a receipt the hub dropped (`assistant_in_doubt`, which may
            // never have reached the phone), a new run's answer. The phone can't tell which of these it got, so none is
            // taken as final.
            let answer: Error = tried.inDoubt ? InDoubt() : error
            settle(tried, after: answer)
            throw answer
        }
        attempt = nil
        deps.showMain(to, text, tried.id)
        sent = (.main, to)
        if markDone { deps.markDone() }
    }

    /// After a failed try: kept in doubt while it may have gone, else over.
    private func settle(_ tried: Attempt, after error: Error) {
        guard Self.mayHaveGone(error) else {
            attempt = nil
            return
        }
        var kept = tried
        kept.inDoubt = true
        attempt = kept
    }
}

extension AppModel {
    /// Keeps one hand-off pending message and its id through the outbox's retries. Only a terminal sent / failed state
    /// answers the sheet, so another Send cannot type a second copy while the first waits for a connection (#1846).
    func sendForHandOff(_ key: SessionKey, text: String, item: InboxItem? = nil, file: Bool = false) async -> InboxHandOffModel.AgentSendResult {
        guard allowSend(key) else { return .failed(session(key)?.sourceSubtitle) }
        var msg = PendingMessage(text: text, images: [])
        if file, let item {
            let group = hub?.group ?? hubGroup
            msg.inboxHandOff = .init(itemID: item.id, manager: item.manager, sourceGroup: group,
                                     sourcePairing: registry.primary(group)?.id)
        }
        chat(key).pending.append(msg)
        saveOutbox()
        let outcome = await sendPending(key, msg)
        settleSend(key, msg.id, outcome)
        if outcome == .sent { return handOffSent(key, msg) }
        // Not delivered yet; the assistant sends it by itself (#10194): the sheet closes, the item stays where it is.
        if outcome == .kept { return InboxHandOffModel.AgentSendResult(delivered: true, error: nil, filed: false, kept: true) }
        if outcome == .failed { return .failed(chat(key).pending.first { $0.id == msg.id }?.failure) }
        return await waitForHandOffDelivery(key, msg)
    }

    /// The outbox owns every retry. Waiting for a link is not failure: follow this message until a retry delivers it
    /// or refuses it. A transcript copy can consume the pending bubble before this loop sees its sent state. New
    /// Agent's page uses this same signal after its ordinary `sendNow`, without starting a second send. Its sheet has
    /// already closed, so `followRetries` also follows a chat retry after a refusal; a unique match on the immutable
    /// message content and creation time finds the new id that such a retry needs, while ambiguity or deletion ends it.
    func waitForHandOffDelivery(_ key: SessionKey, _ msg: PendingMessage,
                                followRetries: Bool = false) async -> InboxHandOffModel.AgentSendResult {
        var tracked = msg
        var lastFailed: PendingMessage?
        while !Task.isCancelled {
            var current = chat(key).pending.first { $0.id == tracked.id }
            if current == nil, followRetries {
                // An uncertain retry stays sending while its old receipt is checked, and can fail and change id between
                // polls. Keep the original immutable identity as the fallback so recognition never depends on observing
                // failed. Never pick between duplicates: an unrelated matching message is not evidence of this hand-off.
                let identity = tracked.state == .failed ? tracked : lastFailed ?? msg
                let candidates = chat(key).pending.filter {
                    $0.id != identity.id && $0.createdAt == identity.createdAt && $0.text == identity.text && $0.images == identity.images
                }
                if candidates.count == 1 { current = candidates[0] }
            }
            if let current {
                tracked = current
                if current.state == .failed { lastFailed = current }
                switch current.state {
                case .sent: return handOffSent(key, current)
                case .failed where followRetries:
                    // The failed bubble stays available for the person's Retry in the Agent chat. No view or hand-off
                    // model is captured here; deletion below still ends the wait.
                    try? await Task.sleep(for: .milliseconds(250))
                    continue
                case .failed: return .failed(current.failure)
                case .sending, .waiting: break
                }
            } else if chat(key).items.contains(where: {
                PendingMessage.arrived(tracked, as: $0) || tracked.id != msg.id && PendingMessage.arrived(msg, as: $0)
            }) {
                return handOffSent(key, tracked)
            } else {
                return .failed() // discarded by the user or reset with its hub
            }
            try? await Task.sleep(for: .milliseconds(50))
        }
        return .failed()
    }

    private func handOffSent(_ key: SessionKey, _ msg: PendingMessage) -> InboxHandOffModel.AgentSendResult {
        InboxHandOffModel.AgentSendResult(delivered: true, error: nil, filed: completeInboxHandOff(key, msg))
    }
}
