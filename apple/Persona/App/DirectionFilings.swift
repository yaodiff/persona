import Foundation
import Observation

/// Move To… (#11516, docs/apple/sidebar.md "Pinned directions"): where the owner files a session (an assistant
/// conversation or an Agent) under one of the current directions, or on its own. The hub decides and keeps every
/// placement (`PUT /api/v1/assistant/sessions/{mid}/{sid}/direction`); this store only sends the owner's pick once,
/// draws it at once where the session is drawn under a direction (the sidebar row's expansion, the page) while the
/// answer is out, and then reads the tree (and the open pages) again. It also holds the tree's live filed sessions,
/// apart from the tree (`takeFiled`), and what this run read of each direction (`kept`).
///
/// - **Gate.** Offered only while the hub lists `direction_sessions` and the app holds the default manager's direction
///   tree (`AppModel.directionTree`, which another manager's page never holds). A manager that reconnected at an older
///   version answers the routes 404 or 405 without their codes: that hub's entries hide until its next hello
///   (`routesMissing`, per hub group).
/// - **Undo** sends the answer's `previous` exactly, `null` written out (`DirectionFilingBody`), through the client the
///   move went out with. A switch of hub or assistant (`forget`) drops the moves out, their toasts' Undo and their late
///   answers.
/// - **Refusals** put the row back and show the hub's sentence; nothing is retried.
/// - **Offline and lost answers.** Sent only while the hub is reachable, and never held for later; one pick at a time
///   per session. An answer lost after sending (cut off, timed out, `delivered: "unknown"`, or the hub's link to the
///   manager cut: 503 `assistant_offline`) reads the tree and the pages again, sends nothing again and offers no Undo.
/// - Filing makes no model call and sends nothing else.
@MainActor @Observable final class DirectionFilings {
    static let shared = DirectionFilings(env: .live)

    /// A choice of Move To…: a current direction by the name its row shows, in the tree's order.
    nonisolated struct Choice: Hashable, Sendable, Identifiable {
        var id: String
        var name: String
    }

    /// A move sent and not settled yet: where the session is drawn meanwhile.
    nonisolated struct Pending: Hashable, Sendable {
        /// The direction it is drawn under: its id, "" for none.
        var to: String
        /// Where it was drawn before the pick, when known: what an Undo draws it under again.
        var from: String?
        var title: String
        /// The session's own state when picked (`AgentState`'s raw value), to draw it under a direction meanwhile.
        var state: String
        /// The hub answered; the rows wait for the tree's read to draw it from there.
        var answered = false
        /// Which pick it is: a later pick of the same session replaces it, and only its own answer settles it.
        var serial = 0
    }

    /// What the store needs of the app (`live`), and of a test.
    struct Env {
        var api: @MainActor () -> APIClient?
        /// The hub's socket is up: a move is sent only then.
        var reachable: @MainActor () -> Bool
        var networkUp: @MainActor () -> Bool
        var toast: @MainActor (Toast) -> Void
        /// Takes down the toast with this id, if it is still up.
        var dismissToast: @MainActor (UUID) -> Void = { _ in }
        /// Reads the direction tree again.
        var rereadTree: @MainActor () async -> Void

        static var live: Env {
            Env(api: { AppModel.shared.api },
                reachable: { AppModel.shared.isLive },
                networkUp: { AppModel.shared.connection.networkUp },
                toast: { AppModel.shared.toast = $0 },
                dismissToast: { id in if AppModel.shared.toast?.id == id { AppModel.shared.toast = nil } },
                rereadTree: { await AppModel.shared.refreshDirectionTree() })
        }
    }

    /// The moves out, by session.
    private(set) var pending: [SessionKey: Pending] = [:]
    /// The hub groups whose filing routes were answered as missing since that hub's last hello: no entry is drawn for
    /// them until then.
    private(set) var routesMissing: Set<String> = []

    @ObservationIgnored private let env: Env
    @ObservationIgnored private var serial = 0
    /// Moves on: each switch of hub or assistant (`forget`). An answer, a toast's Undo or a reread of an earlier one
    /// does nothing.
    @ObservationIgnored private var generation = 0
    /// The pages open now, by direction id, each by a number: read again after a write, together with the tree, also
    /// while a chat is pushed over them. A reader that answers false is gone (its page closed) and is let go.
    @ObservationIgnored private var pages: [String: [Int: () async -> Bool]] = [:]
    @ObservationIgnored private var pageSerial = 0
    /// The tree's live filed sessions, one observable box per direction (`takeFiled`): a filed session's turn redraws
    /// only the rows of its own direction, never the tree's readers (P5).
    @ObservationIgnored private var filed: [String: FiledBox] = [:]
    /// What this run read of each direction (memory only), so a page opened again while the hub is out of reach still
    /// draws it (as Offline · time). Dropped after this device's writes, which make it stale.
    @ObservationIgnored private var kept: [String: (read: DirectionRead, at: Date)] = [:]
    /// The toasts with Undo up now, by session: taken down when that session's next pick goes out (their Undo no longer
    /// puts back what it says) and on a switch.
    @ObservationIgnored private var undoToasts: [SessionKey: UUID] = [:]

    init(env: Env) { self.env = env }

    // MARK: Gate and choices

    /// Whether Move To…, the filed rows and the page are drawn: `direction_sessions` listed, the default manager's tree
    /// held, and the routes not answered as missing since the last hello.
    nonisolated static func shows(features: Set<String>, tree: DirectionTree?, routesMissing: Bool) -> Bool {
        features.contains(DirectionRoutes.feature) && tree != nil && !routesMissing
    }

    func shows(_ model: AppModel) -> Bool {
        Self.shows(features: model.hubFeatures, tree: model.directionTree,
                   routesMissing: routesMissing.contains(model.hub?.group ?? ""))
    }

    /// What `offers` decides from, gathered from the app (`offers(_:in:)`) or written out by a test.
    nonisolated struct Facts {
        var shows: Bool
        /// The current hub's group: another hub's session is not offered.
        var hubGroup: String
        var main: SessionKey?
        var backgroundSupervisor: (String) -> Bool
        /// The current leader conversations' session ids.
        var leaders: Set<String>
        /// `TodoStore.taskFollowers`, and the session's parents for walking up to one.
        var followers: Set<String>
        var parent: (SessionKey) -> String?
        /// A pick of this session is out, not answered yet: one at a time.
        var moving: Bool
        var choices: [Choice]
    }

    /// The choices Move To… offers for session `key` (none: no Move To…). Not without the gate; not for another hub's
    /// session, the main conversation, the background supervisor or a current leader conversation; not for an Agent
    /// that follows its supervised task (or a sub-agent session under one: it follows its task, and the hub refuses it
    /// with 409 `direction_supervised_task`); not while a pick of it is out.
    nonisolated static func offers(_ key: SessionKey, _ f: Facts) -> [Choice] {
        guard f.shows, key.group == f.hubGroup, !key.sessionID.isEmpty, !f.moving else { return [] }
        if key == f.main || f.backgroundSupervisor(key.sessionID) || f.leaders.contains(key.sessionID) { return [] }
        if follows(key, followers: f.followers, parent: f.parent) { return [] }
        return f.choices
    }

    /// `offers` with the app's facts. The caller reads this when its menu is built, from the smallest view that draws
    /// it. Only forwards the model's values: what is made of them is `facts(…)`'s, tested.
    func offers(_ key: SessionKey, in model: AppModel) -> [Choice] {
        Self.offers(key, Self.facts(features: model.hubFeatures, hubGroup: model.hub?.group ?? "", tree: model.directionTree,
                                    routesMissing: routesMissing, main: model.assistantMain,
                                    backgroundSupervisor: { model.isBackgroundSupervisor($0) },
                                    configLeaders: model.assistantLeaders, followers: TodoStore.shared.taskFollowers,
                                    parent: { model.session($0)?.parentSessionID },
                                    moving: pending[key].map { !$0.answered } ?? false,
                                    name: { model.directionTitle($0) }))
    }

    /// The facts `offers` decides from, made of the app's values: the gate for this hub, every leader conversation (the
    /// tree's and the settings' lists), and the choices in the tree's order by the names the rows show.
    nonisolated static func facts(features: Set<String>, hubGroup: String, tree: DirectionTree?, routesMissing: Set<String>,
                                  main: SessionKey?, backgroundSupervisor: @escaping (String) -> Bool,
                                  configLeaders: [String], followers: Set<String>,
                                  parent: @escaping (SessionKey) -> String?, moving: Bool,
                                  name: (DirectionTree.Direction) -> String) -> Facts {
        let leaders = tree?.leaders ?? []
        return Facts(shows: shows(features: features, tree: tree, routesMissing: routesMissing.contains(hubGroup)),
                     hubGroup: hubGroup, main: main, backgroundSupervisor: backgroundSupervisor,
                     leaders: Set(leaders.map(\.id)).union(configLeaders), followers: followers, parent: parent,
                     moving: moving, choices: leaders.map { Choice(id: $0.id, name: name($0)) })
    }

    /// Whether `key` is an Agent of an open supervised to-do, or a session under one (its `parent_session_id`s, or an
    /// in-session sub-agent's `<parent>@<id>`), as the hub's `taskFollowers.follows` walks it: at most eight levels.
    nonisolated static func follows(_ key: SessionKey, followers: Set<String>,
                                    parent: (SessionKey) -> String?) -> Bool {
        guard !followers.isEmpty else { return false }
        var k = key
        for _ in 0..<8 {
            if followers.contains(k.machineID + "/" + k.sessionID) { return true }
            let up = parent(k) ?? k.sessionID.split(separator: "@", maxSplits: 1).first.map(String.init).flatMap {
                $0 == k.sessionID ? nil : $0
            }
            guard let up, !up.isEmpty, up != k.sessionID else { return false }
            k.sessionID = up
        }
        return false
    }

    // MARK: The tree's filed sessions

    /// The tree's live filed sessions of one direction (working or needing you), in the app's order (`order`).
    func filedLive(_ direction: String) -> [DirectionTree.FiledSession] { box(direction).sessions }

    /// Takes the filed sessions out of a tree the hub answered: each direction's go to its own box, assigned only when
    /// they changed, and the tree comes back without them, so it compares equal to the one held when only a filed
    /// session's turn changed (its readers do not redraw, P5).
    func takeFiled(_ tree: DirectionTree) -> DirectionTree {
        var seen: Set<String> = []
        for d in tree.directions where !d.secretary {
            seen.insert(d.id)
            let ordered = Self.order(d.sessions)
            let b = box(d.id)
            if b.sessions != ordered { b.sessions = ordered }
        }
        for (id, b) in filed where !seen.contains(id) && !b.sessions.isEmpty { b.sessions = [] }
        return Self.stripped(tree)
    }

    /// The tree without its filed sessions.
    nonisolated static func stripped(_ tree: DirectionTree) -> DirectionTree {
        var t = tree
        for i in t.directions.indices { t.directions[i].sessions = [] }
        return t
    }

    /// The direction the tree lists session `key` under (a live filed session), when it does.
    func placed(_ key: SessionKey) -> String? {
        filed.first { $0.value.sessions.contains { $0.machineID == key.machineID && $0.sessionID == key.sessionID } }?.key
    }

    private func box(_ direction: String) -> FiledBox {
        if let b = filed[direction] { return b }
        let b = FiledBox()
        filed[direction] = b
        return b
    }

    /// The app's order of filed sessions (the hub gives them by computer, then session id): what needs you first, then
    /// what works, then the rest, newest added first within each (#11516 review). By the state each was listed with;
    /// worked out once per answer, never in `body` (P4).
    nonisolated static func order(_ sessions: [DirectionTree.FiledSession]) -> [DirectionTree.FiledSession] {
        sessions.sorted { a, b in
            let (ra, rb) = (rank(a.state), rank(b.state))
            if ra != rb { return ra < rb }
            let (ta, tb) = (a.at ?? .distantPast, b.at ?? .distantPast)
            if ta != tb { return ta > tb }
            return a.id < b.id
        }
    }

    nonisolated static func rank(_ state: String) -> Int {
        switch state {
        case AgentState.needsYou.rawValue: 0
        case AgentState.working.rawValue: 1
        default: 2
        }
    }

    // MARK: Drawing

    /// The direction the session is drawn under while its move is out ("" none), or nil when no move is out.
    func drawnUnder(_ key: SessionKey) -> String? { pending[key]?.to }

    /// A filed session as a row under direction `direction` draws it: the tree's or the read's, already in the app's
    /// order (`order`), with the moves out applied (moved away: gone; moved here: put in by its state, marked moving).
    /// `live` keeps only what the tree lists (working or needing you), as the sidebar row's expansion draws; the page
    /// draws them all.
    nonisolated struct Row: Hashable, Sendable, Identifiable {
        var session: DirectionTree.FiledSession
        var key: SessionKey
        /// Its move is out (not answered yet).
        var moving: Bool
        var id: String { session.id }
    }

    func rows(_ listed: [DirectionTree.FiledSession], in direction: String, group: String, live: Bool) -> [Row] {
        var out: [Row] = []
        var seen: Set<SessionKey> = []
        for s in listed {
            let key = s.key(group: group)
            guard seen.insert(key).inserted else { continue }
            if let p = pending[key], p.to != direction { continue }
            out.append(Row(session: s, key: key, moving: pending[key].map { !$0.answered } ?? false))
        }
        for (key, p) in pending.sorted(by: { $0.key.path < $1.key.path })
        where p.to == direction && key.group == group && !seen.contains(key) {
            guard !live || Self.liveStates.contains(p.state) else { continue }
            let s = DirectionTree.FiledSession(machineID: key.machineID, sessionID: key.sessionID, title: p.title,
                                               source: "owner", at: nil, state: p.state)
            // Moved here just now: first of its rank (the newest), without sorting the rest again.
            let at = out.firstIndex { Self.rank($0.session.state) >= Self.rank(p.state) } ?? out.endIndex
            out.insert(Row(session: s, key: key, moving: !p.answered), at: at)
        }
        return out
    }

    /// The states the tree lists a filed session in.
    nonisolated static let liveStates: Set<String> = [AgentState.working.rawValue, AgentState.needsYou.rawValue]

    // MARK: Moving

    /// Moves session `key` (titled `title`, its own state `state`) under `choice`'s direction, or on its own (nil).
    /// `from` is the direction it is drawn under now, when known. Nothing while a pick of it is out.
    func move(_ key: SessionKey, title: String, state: AgentState, to choice: Choice?, from: String?) {
        guard pending[key].map({ $0.answered }) ?? true, let api = env.api() else { return }
        let target: DirectionTarget = choice.map { .direction($0.id) } ?? .own
        send(key, api: api, title: title, state: state.rawValue, target: target, drawnUnder: target.drawnUnder,
             from: from, name: choice?.name, undo: false)
    }

    /// The hub's reachability is known before sending; a move is never held for later. `api` is the client of the
    /// hub and assistant the move is for, kept for its Undo.
    private func send(_ key: SessionKey, api: APIClient, title: String, state: String, target: DirectionTarget,
                      drawnUnder: String, from: String?, name: String?, undo: Bool) {
        guard env.reachable() else {
            // Nothing was drawn yet: say why nothing happened (the same words as any action the hub did not get).
            env.toast(Toast(env.networkUp() ? String(localized: "Can't reach the hub. That action didn't go through.")
                                            : String(localized: "No network. That action didn't go through."), style: .info))
            Haptics.attention()
            return
        }
        Haptics.tap()
        if let shown = undoToasts.removeValue(forKey: key) { env.dismissToast(shown) } // its Undo would not put this back
        serial += 1
        let pick = serial
        let gen = generation
        pending[key] = Pending(to: drawnUnder, from: from, title: title, state: state, serial: pick)
        Task { [weak self] in
            let outcome: Result<DirectionFilingAnswer, Error>
            do { outcome = .success(try await api.fileSession(key, to: target)) } catch { outcome = .failure(error) }
            await self?.settle(key, api: api, pick: pick, generation: gen, outcome: outcome, title: title, state: state,
                               name: name, undo: undo)
        }
    }

    private func settle(_ key: SessionKey, api: APIClient, pick: Int, generation gen: Int,
                        outcome: Result<DirectionFilingAnswer, Error>, title: String, state: String, name: String?,
                        undo: Bool) async {
        // A switch of hub or assistant since: its moves were let go, and this answer is the one before's.
        guard gen == generation else { return }
        let before = pending[key]
        /// Only this pick's own entry: a later pick of the same session keeps its own.
        func clear() { if pending[key]?.serial == pick { pending[key] = nil } }
        switch outcome {
        case .success(let answer):
            if pending[key]?.serial == pick { pending[key]?.answered = true }
            if !undo {
                let toast = Self.answered(answer, name: name) { [weak self] in
                    guard let self, self.generation == gen else { return }
                    self.undoToasts[key] = nil
                    self.undo(key, api: api, answer: answer, title: title, state: state, back: before?.from)
                }
                if toast.action != nil { undoToasts[key] = toast.id }
                env.toast(toast)
            }
            Haptics.success()
            await reread(places: [answer.previous, answer.direction])
            clear()
        case .failure(let error):
            switch Self.failure(error) {
            case .missingRoutes:
                routesMissing.insert(api.group)
                clear()
                env.toast(Toast(String(localized: "Not available on this hub"), style: .info))
                Haptics.attention()
            case .refused(let sentence):
                clear() // back where it was
                env.toast(Toast(sentence, style: .error))
                Haptics.error()
            case .notSent:
                clear()
                env.toast(Toast(env.networkUp() ? String(localized: "Can't reach the hub. That action didn't go through.")
                                                : String(localized: "No network. That action didn't go through."), style: .info))
                Haptics.attention()
            case .lost(let sentence):
                // It may have been taken: read where it is now, send nothing again, offer no Undo. The words for the
                // hub's code when it gave one (503 `assistant_offline`, also its answer when nothing was sent: "The
                // assistant is offline or restarting"; trying again is safe, a move already made answers Already in).
                env.toast(Toast(sentence ?? String(localized: "Couldn't confirm the move. Showing where it is now."),
                                style: .info))
                Haptics.attention()
                await reread(places: before.map { [$0.from, $0.to] } ?? [nil])
                clear()
            }
        }
    }

    /// The Undo of an answered move: the answer's `previous`, exactly, through the same client. Drawn back where it
    /// was before the move.
    private func undo(_ key: SessionKey, api: APIClient, answer: DirectionFilingAnswer, title: String, state: String,
                      back: String?) {
        guard pending[key].map({ $0.answered }) ?? true else { return }
        let target = DirectionTarget(answer: answer.previous)
        let drawn = target == .remove ? (back ?? "") : target.drawnUnder
        send(key, api: api, title: title, state: state, target: target, drawnUnder: drawn, from: answer.direction,
             name: nil, undo: true)
    }

    /// The toast for an answer: where it went, with Undo when the book changed; where it already is, without one, when
    /// not.
    static func answered(_ answer: DirectionFilingAnswer, name: String?, undo: @escaping @MainActor () -> Void) -> Toast {
        guard answer.changed else {
            return Toast(name.map { String(localized: "Already in “\($0)” [Move To]") }
                         ?? String(localized: "Already on its own [Move To]"), style: .info)
        }
        let words = name.map { String(localized: "Moved to “\($0)” [Move To]") } ?? String(localized: "Set on its own [Move To]")
        return Toast(words, style: .success, action: Toast.Action(title: String(localized: "Undo"), run: undo))
    }

    /// How a write that did not answer 2xx ended.
    nonisolated enum Failure: Equatable, Sendable {
        /// 404 / 405 without one of the routes' codes: an older manager; hidden until the next hello.
        case missingRoutes
        /// The hub refused it with a sentence of its own (any 4xx, or a 5xx with a `code`): nothing changed.
        case refused(String)
        /// It never left: no connection to the hub could be made.
        case notSent
        /// It went out and its answer did not come back (cut off, timed out, a 5xx with no code, the manager's link
        /// cut): it may have been taken. With the hub's sentence when it said one.
        case lost(String?)
    }

    nonisolated static func failure(_ error: Error) -> Failure {
        if DirectionRoutes.missing(error) { return .missingRoutes }
        if let e = error as? APIError {
            // The link between hub and manager cut: the app's words for that code, and it may have been saved.
            if e.code == "assistant_offline" { return .lost(e.localizedDescription) }
            // A relay cut off while the manager had it says so (`delivered: "unknown"`): it may have been taken.
            if e.fields["delivered"] == "unknown" || Self.inDoubt.contains(e.code ?? "") { return .lost(nil) }
            if (400..<500).contains(e.status) || (e.status >= 500 && e.code != nil) {
                return .refused(e.localizedDescription)
            }
            return .lost(nil)
        }
        let ns = error as NSError
        if ns.domain == NSURLErrorDomain, Self.neverSent.contains(ns.code) { return .notSent }
        return .lost(nil)
    }

    /// Codes that say the write may have been taken though its answer did not come back. The hub answers a relay to
    /// the manager cut on the way with 503 `assistant_offline` (`server/internal/hub/bus_forward.go`): the manager may
    /// have saved the move.
    nonisolated private static let inDoubt: Set<String> = ["assistant_in_doubt", "lost_receipt", "send_in_doubt"]

    /// URL errors that mean no request reached the hub.
    nonisolated private static let neverSent: Set<Int> = [
        NSURLErrorNotConnectedToInternet, NSURLErrorCannotConnectToHost, NSURLErrorCannotFindHost,
        NSURLErrorDNSLookupFailed, NSURLErrorDataNotAllowed, NSURLErrorInternationalRoamingOff, NSURLErrorCallIsActive,
        NSURLErrorSecureConnectionFailed,
    ]

    /// The tree and every open page, read again (the page reads only on open, on reconnect and after a write of this
    /// device: P7 / P9). What this run read of the places the session left and joined is stale now (nil in `places`: a
    /// place not known, so every one).
    private func reread(places: [String?]) async {
        if places.contains(where: { $0 == nil }) {
            kept = [:]
        } else {
            for case let id? in places where !id.isEmpty { kept[id] = nil }
        }
        for (id, readers) in pages {
            for (n, read) in readers {
                if !(await read()) { pages[id]?[n] = nil }
            }
        }
        await env.rereadTree()
    }

    // MARK: Pages, reads kept, hello and switches

    /// An open page of direction `id`: `read` reads it again after this device's writes, also while a chat is pushed
    /// over it, and answers false once its page has closed. Returns its number.
    @discardableResult func page(_ id: String, read: @escaping () async -> Bool) -> Int {
        pageSerial += 1
        pages[id, default: [:]][pageSerial] = read
        return pageSerial
    }

    /// What this run last read of direction `id`, and when.
    func kept(_ id: String) -> (read: DirectionRead, at: Date)? { kept[id] }
    func keep(_ id: String, _ read: DirectionRead, at: Date) { kept[id] = (read, at) }

    /// A read of a page of hub `group` answered as a missing route: the same as a write's.
    func routesAnsweredMissing(_ group: String) { if !routesMissing.contains(group) { routesMissing.insert(group) } }

    /// A hello from hub `group`: its features say again whether the routes are there.
    func hello(_ group: String) { if routesMissing.contains(group) { routesMissing.remove(group) } }

    /// Another hub or assistant: the moves out, their answers and toasts' Undo, the filed sessions and the reads kept
    /// were the one before's. Whether a hub's routes are missing stays its own until its hello.
    func forget() {
        generation += 1
        for id in undoToasts.values { env.dismissToast(id) } // their Undo would go to the one before
        undoToasts = [:]
        if !pending.isEmpty { pending = [:] }
        kept = [:]
        for b in filed.values where !b.sessions.isEmpty { b.sessions = [] }
    }
}

/// One direction's live filed sessions (`DirectionFilings.takeFiled`): its own observable, so a change redraws only
/// the rows that read this direction's.
@MainActor @Observable final class FiledBox {
    var sessions: [DirectionTree.FiledSession] = []
}
