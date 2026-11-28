import Foundation
import PersonaWire

// Messages written while the hub is out of reach (docs/apple/ios.md「Connection status」): they stay in their chat as pending bubbles
// with a clock (Sends when connected) and go out by themselves once the app is connected and caught up, in the order they
// were written. Kept on disk too, so quitting the app (or iOS ending it in the background) does not lose them.
// Sent bubbles live here too: they stay until the transcript's copy arrives (`settleSend`, `showSent`), on disk as well.

nonisolated enum SendOutcome: Equatable, Sendable {
    /// kept: it did not go in, and the assistant keeps it to send by itself (#10194): the bubble left the outbox for the
    /// row's `undelivered`, and the chat's Processing ends as for a failure (nothing runs for it now).
    case sent, failed, waiting, kept, effortWaiting

    var state: PendingMessage.State {
        switch self {
        case .sent: .sent
        case .failed, .kept: .failed
        case .waiting, .effortWaiting: .waiting
        }
    }
}

extension AppModel {
    /// Messages still going out (written offline, or being sent).
    var outboxCount: Int {
        chats.values.reduce(0) { n, s in n + s.pending.count { $0.state == .waiting || $0.state == .sending } }
    }

    /// A message the hub already has, on screen as sent until its transcript copy arrives: a new Persona conversation's
    /// first one went with the start request, and the node shows no transcript until claude has written it (#511).
    /// `id`: the message id the hub sent it under (an inbox hand-off's client id), which its transcript copy carries; no
    /// bubble when that copy is in already (a retried hand-off the hub answered from its receipt).
    func showSent(_ key: SessionKey, text: String, images: [ImagePayload], id: UUID = UUID()) {
        guard !chat(key).items.contains(where: { PendingMessage.tied($0, to: id) == true }) else { return }
        let msg = PendingMessage(id: id, text: text, images: images)
        chat(key).pending.append(msg)
        chat(key).beginSendFeedback(msg)
        settleSend(key, msg.id, .sent)
    }

    /// Records how a send went and returns what it recorded (nil: the message is no longer in its chat). A sent bubble
    /// stays until its transcript copy arrives (a minute at most); one that keeps coming back waiting on a link that is
    /// up fails after `OutboxWait.connectedLimit`.
    @discardableResult
    func settleSend(_ key: SessionKey, _ id: UUID, _ outcome: SendOutcome) -> SendOutcome? {
        let store = chat(key)
        // Echo can remove the pending bubble before the HTTP response. Its acknowledgement must not disappear.
        store.settleSendFeedback(id, state: outcome.state)
        guard let i = store.pending.firstIndex(where: { $0.id == id }) else { return nil }
        let linkUp = outboxLinkUp(key.group)
        let wait = OutboxWait.next(outcome, since: store.pending[i].stalledSince, now: .now, linkUp: linkUp)
        store.pending[i].state = wait.outcome.state
        store.pending[i].stalledSince = wait.since
        if wait.gaveUp, session(key) == nil {
            // No row for its chat after a minute on a link that is up (deleted, or its computer removed, on another
            // device): the chat's other waiting messages have nowhere to go either, and fail with it under one notice.
            let reason = String(localized: "Waiting messages weren't sent: their chat wasn't found on the hub.")
            store.pending[i].failure = reason
            for j in store.pending.indices where store.pending[j].state == .waiting {
                store.pending[j].state = .failed
                store.pending[j].failure = reason
                store.pending[j].stalledSince = nil
            }
            toast = Toast(reason, style: .error)
        }
        if wait.outcome == .sent { _ = completeInboxHandOff(key, store.pending[i]) }
        saveOutbox()
        switch wait.outcome {
        case .sent:
            Task {
                // Normally the transcript shows it within a second or two. Behind a long turn it may only be written
                // when that turn ends (Codex keeps its queue to itself): stay while the agent works, then go.
                try? await clock.sleep(for: .seconds(60))
                for _ in 0..<90 where store.pending.contains(where: { $0.id == id }) && session(key)?.status.isActive == true {
                    try? await clock.sleep(for: .seconds(10))
                }
                store.pending.removeAll { $0.id == id }
            }
        case .waiting where linkUp, .effortWaiting where linkUp:
            // Retry transient failures or a refusal that beat the session update. Known effort changes wait for
            // the session update instead of spending the retry budget.
            retryOutboxSoon(key.group)
        default: break
        }
        return wait.outcome
    }

    /// Files a delivered Agent hand-off once, including one restored from disk. `filed` means the Done write was
    /// initiated; InboxStore still rolls it back if that write fails. Returns whether this message carried a filing intent.
    func completeInboxHandOff(_ key: SessionKey, _ msg: PendingMessage) -> Bool {
        guard var handOff = msg.inboxHandOff else { return false }
        // A delivery callback can run while another hub is on screen, or after the app restarts.
        guard let group = handOff.filingGroup(in: registry, destination: key.group), let client = api(group) else { return true }
        if let i = chat(key).pending.firstIndex(where: { $0.id == msg.id }) {
            if chat(key).pending[i].inboxHandOff?.filed == true { return true }
            handOff.filed = true
            chat(key).pending[i].inboxHandOff = handOff
        }
        #if DEBUG
        if let done = inboxHandOffDoneForTests {
            done(handOff)
            return true
        }
        #endif
        if group == hub?.group {
            InboxStore.shared.setDone(id: handOff.itemID, manager: handOff.manager, done: true, api: client.manager(handOff.manager))
        } else {
            let owner = client.manager(handOff.manager)
            let id = handOff.itemID
            Task {
                do { try await owner.setInboxDone(id, true) }
                catch { if hub?.group == group { show(error) } }
            }
        }
        return true
    }

    /// The group's socket is connected and answering: a send that comes back waiting then is not an outage.
    private func outboxLinkUp(_ group: String) -> Bool {
        #if DEBUG
        if let linksUpForTests { return linksUpForTests }
        #endif
        return socket(group).map { $0.state == .connected && !$0.stalled } == true
    }

    /// The group's waiting messages go again in 10 seconds: one retry per group, however many came back waiting.
    func retryOutboxSoon(_ group: String) {
        guard outboxRetry[group] == nil else { return }
        outboxRetry[group] = Task { [weak self] in
            try? await Task.sleep(for: .seconds(10))
            guard let self else { return }
            self.outboxRetry[group] = nil
            if !Task.isCancelled { self.flushOutbox() }
        }
    }

    /// A new socket for the group: its waiting messages' runs of waits start again (`OutboxWait`).
    func restartOutboxWaits(_ group: String) {
        for (key, store) in chats where key.group == group {
            for i in store.pending.indices where store.pending[i].stalledSince != nil { store.pending[i].stalledSince = nil }
        }
    }

    /// Sends what waited for the network, group by group (docs/protocol/multi-hub.md): each group whose link is up sends its own,
    /// chat by chat and in order, while the others wait for theirs. A group out of reach neither keeps the others'
    /// messages back nor is asked (#4704: only the hub-level group's link was looked at, so another group's messages
    /// stayed put while that one was down, and one group's dead requests held up the rest). A chat that is not open
    /// first fetches its latest messages: one that did arrive before the link broke is in the transcript and drops
    /// out (ChatStore.apply).
    func flushOutbox() {
        let waiting = chats.compactMap { key, store in
            outboxCanFlush(key, store) ? key : nil
        }
        let batches = OutboxFlush.batches(waiting, ready: { self.isLive($0) }, busy: flushingOutbox)
        for (group, keys) in batches {
            flushingOutbox.insert(group)
            Task {
                defer { flushingOutbox.remove(group) }
                for key in keys {
                    let store = chat(key)
                    guard outboxCanFlush(key, store) else { continue }
                    // A paged session list holds only the pages loaded: ask for this chat's row (no-op otherwise).
                    if session(key) == nil { await refreshSessionDetail(key) }
                    if !isSubscribed(key) {
                        if store.loadedOnce { await fill(key) } else { await loadInitial(key) }
                    }
                    while isLive(group), outboxCanFlush(key, store),
                          let i = store.pending.firstIndex(where: { $0.state == .waiting }) {
                        if store.pending[i].refused == true, let effort = store.pending[i].requiredEffort {
                            if latest(key)?.effort != effort {
                                // The refusal may have arrived before the node's session update. Keep that receipt
                                // until an exact read or a later push confirms the effort.
                                let id = store.pending[i].id
                                await refreshSessionDetail(key, force: true)
                                guard outboxCanFlush(key, store) else { break }
                                if let message = store.pending.first(where: { $0.id == id && $0.state == .waiting }),
                                   let required = message.requiredEffort, latest(key)?.effort != required {
                                    noteFailure(key, id, String(localized: "Thinking effort could not be confirmed. Choose it again before resending."))
                                    settleSend(key, id, .waiting)
                                    break
                                }
                                continue
                            }
                            store.pending[i].id = UUID()
                            store.pending[i].refused = nil
                            saveOutbox()
                        }
                        store.pending[i].state = .sending
                        let msg = store.pending[i]
                        store.beginSendFeedback(msg)
                        let settled = settleSend(key, msg.id, await sendPending(key, msg))
                        if settled == .waiting || settled == .effortWaiting { break }
                    }
                }
                // A retry that came due during this pass found the group busy: what still waits goes again shortly.
                if outboxLinkUp(group), chats.contains(where: { $0.key.group == group && outboxCanFlush($0.key, $0.value) }) {
                    retryOutboxSoon(group)
                }
            }
        }
    }

    func canKeepOutboxEffort(_ key: SessionKey, effort: String) -> Bool {
        existingChat(key)?.pending.contains { message in
            guard message.onlyHere, message.mayHaveArrived != true, let required = message.requiredEffort else { return false }
            return message.state == .failed || required != effort
        } == true
    }

    /// Cancel an active change on the node. If the change was lost, the user's choice can instead update the outbox.
    func keepCurrentEffort(_ key: SessionKey, effort: String) async {
        guard effortRequests[key] == nil, let row = latest(key), row.effort == effort else { return }
        if row.pendingEffort != nil {
            await setModes(key, effort: effort)
        } else if canKeepOutboxEffort(key, effort: effort), allowSend(key) {
            updateOutboxEffort(key, to: effort, includeFailed: true)
            flushOutbox()
        }
    }

    /// A later choice also applies to the messages still waiting behind the earlier one.
    func updateOutboxEffort(_ key: SessionKey, to effort: String, includeFailed: Bool = false) {
        guard let store = existingChat(key) else { return }
        var changed = false
        for i in store.pending.indices {
            let message = store.pending[i]
            guard message.mayHaveArrived != true else { continue }
            if includeFailed && message.state == .failed && message.requiredEffort != nil {
                store.pending[i].state = .waiting
                store.pending[i].failure = nil
                store.pending[i].stalledSince = nil
            } else {
                guard message.state == .waiting, message.requiredEffort != effort else { continue }
            }
            store.pending[i].requiredEffort = effort
            changed = true
        }
        if changed { saveOutbox() }
    }

    private func outboxCanFlush(_ key: SessionKey, _ store: ChatStore) -> Bool {
        guard store.pending.contains(where: { $0.state == .waiting }),
              effortRequests[key] == nil, latest(key)?.pendingEffort == nil else { return false }
        return !store.pending.contains { $0.state == .sending && $0.requiredEffort != nil }
    }

    private static let outboxName = "outbox"

    /// The longest a sent message waits for its transcript copy (`settleSend`): a minute, then ten seconds at a time
    /// while the agent works, 90 times.
    static let sentWait: TimeInterval = 60 + 90 * 10

    func saveOutbox() {
        outboxWriter.run { [weak self] in // one write at a time, the latest last
            guard let self else { return }
            let list = self.chats.flatMap { key, store in store.pending.map { OutboxEntry(key: key, message: $0) } }
            StateCache.save(Self.outboxName, list, in: self.stateFolder)
        }
    }

    /// At launch: what was still going out comes back as waiting (a resend carries the same id, so one that did
    /// arrive is not typed twice). A sent one comes back as sent, waiting for its transcript copy again: it was left
    /// out, and a message the hub had taken was gone from its chat until the copy came, behind a long turn for minutes
    /// (#1455). One whose copy is already in the chat's cache does not come back: copies are looked for only among
    /// the items that arrive from now on, and it showed twice until its wait ran out.
    func restoreOutbox() {
        for e in StateCache.load(Self.outboxName, as: [OutboxEntry].self, in: stateFolder) ?? [] {
            var msg = e.message
            switch msg.state {
            case .failed: // a failed one stays failed, the user decides
                break
            case .sent:
                if let at = msg.createdAt, at < Date().addingTimeInterval(-Self.sentWait) { continue }
            case .sending, .waiting:
                msg.state = .waiting
            }
            let store = chat(e.key)
            guard !store.pending.contains(where: { $0.id == msg.id }),
                  !store.items.contains(where: { PendingMessage.tied($0, to: msg.id) == true }) else { continue }
            store.pending.append(msg)
            if msg.state == .sent { settleSend(e.key, msg.id, .sent) } // its wait for the copy starts again
        }
    }
}

private nonisolated struct OutboxEntry: Codable, Sendable {
    var key: SessionKey
    var message: PendingMessage
}

/// Which chats' waiting messages go out now (pure, for the tests).
nonisolated enum OutboxFlush {
    /// `keys` (chats with waiting messages) by group: only groups whose link is `ready` and that are not `busy` sending
    /// already. Within a group the chats keep a stable order.
    static func batches(_ keys: [SessionKey], ready: (String) -> Bool, busy: Set<String>) -> [String: [SessionKey]] {
        var out: [String: [SessionKey]] = [:]
        for key in keys where !busy.contains(key.group) && ready(key.group) { out[key.group, default: []].append(key) }
        return out.mapValues { $0.sorted { ($0.machineID, $0.sessionID) < ($1.machineID, $1.sessionID) } }
    }
}

/// How long a message may keep coming back waiting while its group's link is up (pure, for the tests). Waiting on a
/// link that is up means a request with no answer (a weak network) or a chat missing from the session list (deleted,
/// or its computer removed, on another device); past the limit the message fails and the user decides. Waiting while
/// the link is down has no limit: that is what the outbox is for.
nonisolated enum OutboxWait {
    /// How long sends may keep coming back waiting on a link that is up: about six tries, 10 seconds apart.
    static let connectedLimit: TimeInterval = 60

    struct Next: Equatable {
        /// The outcome to record.
        var outcome: SendOutcome
        /// When the run of waits on a link that is up began; nil once the message no longer waits on one.
        var since: Date?
        /// It failed because the run reached the limit.
        var gaveUp = false
    }

    static func next(_ outcome: SendOutcome, since: Date?, now: Date, linkUp: Bool) -> Next {
        guard outcome == .waiting, linkUp else { return Next(outcome: outcome, since: nil) }
        let start = since ?? now
        if now.timeIntervalSince(start) >= connectedLimit { return Next(outcome: .failed, since: nil, gaveUp: true) }
        return Next(outcome: .waiting, since: start)
    }
}

extension PendingMessage.InboxHandOff {
    func filingGroup(in registry: HubRegistry, destination: String) -> String? {
        if let sourcePairing { return registry.hubs.first { $0.id == sourcePairing }?.group }
        if let sourceGroup { return sourceGroup }
        // Receipts from before the source was stored can identify it only in a single-hub installation.
        return registry.groups == [destination] ? destination : nil
    }
}
