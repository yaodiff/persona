#if os(iOS)
import PersonaWire
import SwiftUI

/// Match a genuinely live transcript item to a fresh terminal session snapshot. They travel separately.
@Observable @MainActor final class ReplySpeechTurns {
    let key: SessionKey
    private(set) var candidate: String?
    private(set) var finished = false
    private(set) var preview: String?
    @ObservationIgnored private var position = (-1, -1)
    @ObservationIgnored private var previous: Stamp?

    private struct Stamp: Equatable {
        let state: AgentState
        let revision: Int64?
        let updated: Date?
        let stateSince: Date?
        let preview: String?

        init(_ session: Session) {
            state = session.state
            revision = session.rev
            updated = session.updatedAt
            stateSince = session.stateSince
            preview = session.lastText
        }
    }

    init(key: SessionKey, last: Item?, session: Session?) {
        self.key = key
        if let last { position = (last.seq, last.part) }
        previous = session.map(Stamp.init)
    }

    func reconnecting() {
        candidate = nil
        finished = false
        previous = nil
    }

    func received(_ items: [Item], backlog: Bool) {
        let floor = position
        for item in items where (item.seq, item.part) > floor {
            if (item.seq, item.part) > position { position = (item.seq, item.part) }
            guard !backlog else { continue }
            if item.role == "user", item.kind == .text, item.queued != true { candidate = nil }
            if item.role == "assistant", item.kind == .text, item.narration != true, item.codexHeartbeatQuiet != true {
                candidate = item.id
            }
        }
    }

    func sessionChanged(_ session: Session?) {
        let stamp = session.map(Stamp.init)
        guard previous != stamp else { return }
        previous = stamp
        finished = stamp?.state == .unread || stamp?.state == .idle
        preview = stamp?.preview
        if !finished, stamp?.state != .working { candidate = nil }
    }

    func pending(in store: ChatStore) -> Item? {
        guard finished, let candidate, store.backlogBuilt, !store.isRebuilding, store.liveText.isEmpty,
              case .turn(let turn) = store.blocks.last, case .assistant(let item) = turn.answer,
              item.id == candidate, item.narration != true, turn.trailing.isEmpty else { return nil }
        return item
    }

    /// A short answer can match the whole row. A clipped or multi-message preview starts with the
    /// turn's first words, so only a fresh transcript page can distinguish the final body from progress.
    func matchesPreview(_ item: Item) -> Bool {
        guard let preview, !preview.isEmpty, !preview.hasSuffix("…") else { return false }
        return AssistantCardText.replacingCards(item.cutText).split(whereSeparator: \.isWhitespace)
            .elementsEqual(preview.split(whereSeparator: \.isWhitespace))
    }

    func take(from store: ChatStore, foldBackground: Bool, foldRelay: Bool, needsAttention: Bool,
              confirmed: String? = nil) -> Item? {
        guard let item = pending(in: store), confirmed == item.id || matchesPreview(item) else { return nil }
        let candidate = item.id
        self.candidate = nil
        finished = false
        // Recaps can split one round. Apply Main Chat's own visibility rules from its most recent input.
        let first = store.blocks.lastIndex {
            if case .turn(let turn) = $0 { return !turn.users.isEmpty }
            return false
        } ?? store.blocks.startIndex
        let builder = ChatLines(openWork: [], foldSupervision: true, foldBackground: foldBackground, main: true,
                                foldRoutineRelayReplies: foldRelay, latestReplyNeedsAttention: needsAttention)
        let visible = builder.build(Array(store.blocks[first...]), live: nil).contains { line in
            if case .row(.assistant(let body)) = line.content { return body.id == candidate }
            return false
        }
        return visible ? item : nil
    }
}

struct ReplySpeechObserver: View {
    let key: SessionKey
    @StoredValue(ReplySpeechPreferences.enabled) private var enabled = false
    @Environment(\.scenePhase) private var scene
    @Environment(\.tabIsActive) private var active
    @Environment(\.replySpeechContext) private var context
    @Environment(AppModel.self) private var model

    private var canRead: Bool {
        guard enabled, active, scene == .active, model.visibleSession == key else { return false }
        guard model.isLive(key.group), !model.connection.catchingUp,
              context?.allows(settingsOpen: model.shellSettings) == true else { return false }
        return RecordingMicrophone.shared.isFree && RecordingCapture.shared.state == .idle && (model.drafts[key] ?? "").isEmpty
    }

    var body: some View {
        if canRead {
            ReplySpeechTurnObserver(key: key, store: model.chat(key))
                .id(model.chat(key).resets)
        }
    }
}

private struct ReplySpeechTurnObserver: View {
    let key: SessionKey
    let store: ChatStore
    @Environment(AppModel.self) private var model
    @Environment(\.replySpeechContext) private var context
    @StoredValue(ReplySpeechPreferences.enabled) private var enabled = false
    @StoredValue(ReplySpeechPreferences.full) private var full = false
    @StoredValue(ReplySpeechPreferences.engine) private var engine = "auto"
    @StoredValue(ReplySpeechPreferences.rate) private var rate = 1.0
    @StoredValue(ReplySpeechPreferences.voice) private var voice = ""

    @State private var turns: ReplySpeechTurns?
    @State private var playback: ReplySpeechPlayback?
    @State private var load: Task<Void, Never>?

    private struct Snapshot: Equatable {
        var candidate: String?
        var finished: Bool
        var preview: String?
        var ready: Bool
        var built: Int
        var streaming: Bool
        var send: UUID?
    }

    var body: some View {
        // version advances when the asynchronously rebuilt blocks publish.
        let _ = store.blocks
        let snapshot = Snapshot(candidate: turns?.candidate, finished: turns?.finished == true, preview: turns?.preview,
                                ready: store.backlogBuilt, built: store.version, streaming: !store.liveText.isEmpty, send: store.sendFeedback?.id)
        Color.clear
            .onAppear(perform: enter)
            .onChange(of: snapshot) { old, new in
                if new.send != old.send, new.send != nil { stop(); enter(); return }
                if old.finished && !new.finished && new.candidate != nil { stop() }
            }
            .task(id: snapshot) {
                guard let turns, model.replySpeech === turns, let pending = turns.pending(in: store) else { return }
                var confirmed: String?
                if !turns.matchesPreview(pending) {
                    // The completed session row can precede the final pushed item. Read the hub's
                    // current turn only when its preview does not uniquely name one complete body.
                    let page = try? await model.api(key.group)?.items(key, beforeSeq: nil, turns: 1)
                    confirmed = page?.items.last(where: { $0.role == "assistant" && $0.kind == .text && $0.narration != true })?.id
                    guard !Task.isCancelled, model.replySpeech === turns, confirmed == pending.id else { return }
                }
                guard let item = turns.take(from: store, foldBackground: model.mainBackgroundFolded,
                                            foldRelay: model.assistantConfig?.foldRoutineRelayReplies == true,
                                            needsAttention: ChatLines.needsReplyAttention(model.session(key)),
                                            confirmed: confirmed) else { return }
                read(item)
            }
            .onReceive(NotificationCenter.default.publisher(for: UIApplication.willResignActiveNotification)) { _ in leave() }
            .onReceive(NotificationCenter.default.publisher(for: UIApplication.protectedDataWillBecomeUnavailableNotification)) { _ in leave() }
            .onDisappear { leave() }
    }

    private func enter() {
        let window = ReplySpeechTurns(key: key, last: store.items.last, session: model.latest(key))
        turns = window
        model.replySpeech = window
    }

    private func read(_ item: Item) {
        stop()
        load = Task {
            var body = item.cutText
            if item.fullSize != nil {
                if let whole = try? await model.api(key.group)?.fullItem(key, id: item.id).text {
                    body = whole
                } else if full || body.count <= 300 {
                    return
                }
            }
            guard !Task.isCancelled, enabled, UIApplication.shared.applicationState == .active,
                  model.replySpeech === turns, model.visibleSession == key,
                  context?.allows(settingsOpen: model.shellSettings) == true,
                  (model.drafts[key] ?? "").isEmpty, RecordingMicrophone.shared.isFree else { return }
            let player = playback ?? ReplySpeechPlayback()
            playback = player
            player.speak(body, engine: engine, rate: rate, voice: voice, full: full)
        }
    }

    private func stop() {
        load?.cancel()
        load = nil
        playback?.stop()
    }

    private func leave() {
        if model.replySpeech === turns { model.replySpeech = nil }
        turns = nil
        stop()
    }
}
#endif
