import Foundation
import os
#if canImport(WatchConnectivity)
import WatchConnectivity
#endif
import PersonaWire

/// Each group's credential the watch uses, and the groups' names (docs/protocol/multi-hub.md「Watch, Siri, Mac, and demo mode」).
nonisolated struct WatchConfigs: Codable, Sendable, Equatable {
    var configs: [String: ServerConfig]
    var names: [String: String] = [:]
    /// The groups the phone has but could not get the watch a credential for yet (#1185: an older hub, or the hub
    /// not reachable when it asked); the watch asks the user to sync again. nil from an older phone.
    var missing: [String]? = nil
    /// Watch token ids only; absent from older phones. Never the phone's device ids.
    var deviceIDs: [String: String]? = nil
    /// Authenticated phone hello capabilities by group; missing is unknown, an empty array is known unsupported.
    var features: [String: [String]]? = nil
}

nonisolated extension WatchConfigs {
    /// The watch keeps what the phone sent in its keychain: the configs carry hub tokens and Cloudflare Access service
    /// tokens. Builds before this kept them as plain JSON in defaults ("configs", or "config" from before several hubs);
    /// `stored` moves those over.
    static let keychainAccount = "watchConfigs"

    static func stored(_ defaults: UserDefaults = .standard) -> WatchConfigs? {
        switch Keychain.read(account: keychainAccount) {
        case .found(let data): return try? JSONDecoder().decode(Self.self, from: data)
        case .failed: return nil // not readable yet (before the first unlock): leave the old copy for next time
        case .missing: break
        }
        let old = defaults.data(forKey: "configs").flatMap { try? JSONDecoder().decode(Self.self, from: $0) }
            ?? defaults.data(forKey: "config").flatMap { try? JSONDecoder().decode(ServerConfig.self, from: $0) }
                .map { WatchConfigs(configs: ["": $0]) }
        old?.store(defaults)
        return old
    }

    /// Saves to the keychain and drops the plain copy older builds left in defaults.
    func store(_ defaults: UserDefaults = .standard) {
        guard let data = try? JSONEncoder().encode(self), Keychain.save(data, account: Self.keychainAccount) else { return }
        defaults.removeObject(forKey: "configs")
        defaults.removeObject(forKey: "config")
    }
}

nonisolated extension Interaction {
    /// Whether the phone passes it to the watch: not an MCP form or link, which the watch can't fill in, and which a
    /// watch build from before the kind would fail to decode, losing the whole summary with it.
    var showsOnWatch: Bool { kind != .elicitation }
}

/// What the watch needs to render without its own WebSocket: live sessions and pending interactions.

nonisolated struct WatchSummary: Codable, Sendable {
    var sessions: [Session]
    var interactions: [Interaction]
    var machines: [Machine]
    /// Open inbox items that need the user (docs/assistant/inbox.md); nil from an older phone.
    var inbox: [InboxItem]? = nil
    var at: Date = .now

    enum CodingKeys: String, CodingKey { case sessions, interactions, machines, inbox, at }

    func encode(to encoder: Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(sessions.map { NativeSessionEnvelope(session: $0) }, forKey: .sessions)
        try c.encode(interactions, forKey: .interactions)
        try c.encode(machines, forKey: .machines)
        try c.encodeIfPresent(inbox, forKey: .inbox)
        try c.encode(at, forKey: .at)
    }
}

/// What the phone's application context says it takes, kept in the watch's defaults for its views: rolling
/// recording, and all-day recording (docs/assistant/day-recap.md「Watch and phone capabilities」). A value is written
/// only when it changes: the phone sends its whole context again with every summary, every few seconds, and each write
/// redraws every view that reads defaults. Only a full context (the session's, at activation and on every change) says
/// what the phone no longer takes: one without a day key removes it. User info and config replies carry part of it and
/// remove nothing. The rolling key keeps its old rule (an older phone never sends it; nothing removes it).
nonisolated enum WatchPhoneCapabilities {
    static let rollingKey = "recording.phoneRolling"
    /// Int: absent with a phone that doesn't take day recordings, 0 until the user agrees on the phone, else ≥ 1.
    static let dayKey = "recording.phoneDay"
    /// Bool: absent until the phone says; false is a hub without `recording_day`.
    static let dayHubKey = "recording.phoneDayHub"
    /// Int 1…7 (`RecordingDayContract.keepDays`); absent until the phone says (`keepDays` reads 7 then).
    static let keepDaysKey = "recording.phoneDayKeepDays"

    /// Applies one context; returns how many values it wrote or removed (0 when nothing changed).
    @discardableResult static func store(_ ctx: [String: Any], full: Bool, in defaults: UserDefaults = .standard) -> Int {
        storeRolling(ctx, in: defaults) + storeDay(ctx, full: full, in: defaults)
    }

    /// The rolling key alone; returns 1 when it wrote it.
    @discardableResult static func storeRolling(_ ctx: [String: Any], in defaults: UserDefaults = .standard) -> Int {
        guard let rolling = ctx["recordingRolling"] as? Bool, defaults.object(forKey: rollingKey) as? Bool != rolling else { return 0 }
        defaults.set(rolling, forKey: rollingKey)
        return 1
    }

    /// The all-day keys alone; returns how many it wrote or removed. Any change is the watch's day transfer's to act on
    /// at once (`WatchSync.onDayCapabilities`): sending waits for `recordingDay` ≥ 1, and the age limit follows the keep
    /// days.
    @discardableResult static func storeDay(_ ctx: [String: Any], full: Bool, in defaults: UserDefaults = .standard) -> Int {
        var writes = keep(ctx[RecordingDayContract.phoneKey] as? Int, as: dayKey, full: full, in: defaults)
        writes += keep(ctx[RecordingDayContract.phoneHubKey] as? Bool, as: dayHubKey, full: full, in: defaults)
        let keepDays = (ctx[RecordingDayContract.phoneKeepDaysKey] as? Int).map { RecordingDayContract.keepDays(recordingDays: $0) }
        writes += keep(keepDays, as: keepDaysKey, full: full, in: defaults)
        return writes
    }

    /// How long the watch keeps a chunk the phone hasn't acknowledged, in days after its `day`.
    static func keepDays(_ defaults: UserDefaults = .standard) -> Int {
        (defaults.object(forKey: keepDaysKey) as? Int).map { RecordingDayContract.keepDays(recordingDays: $0) }
            ?? RecordingDayContract.maxKeepDays
    }

    /// A day key: written when present and different, removed when a full context lacks it. Absent and false (or 0)
    /// differ, so the stored object is compared, not its value.
    private static func keep<Value: Equatable>(_ value: Value?, as key: String, full: Bool, in defaults: UserDefaults) -> Int {
        if let value {
            guard defaults.object(forKey: key) as? Value != value else { return 0 }
            defaults.set(value, forKey: key)
            return 1
        }
        guard full, defaults.object(forKey: key) != nil else { return 0 }
        defaults.removeObject(forKey: key)
        return 1
    }
}

/// The order of the phone's all-day commands ([ext] `RecordingDayCommand.sentAt`). A command goes as a message, or as
/// user info when the watch app isn't running or the message fails, so one can arrive late, twice, or after a newer one:
/// a "start" whose message fails is queued as user info only once its error comes back, which can be after the "stop"
/// that followed it went out. The phone stamps every command with when it sent it, each later than the one before; the
/// watch acts only on a command newer than the last one it acted on, and recent.
nonisolated enum WatchDayCommandOrder {
    /// A command arriving this long after it was sent is dropped: it was queued while the watch app wasn't running, and
    /// the phone's switch stopped waiting for it long ago.
    static let freshness: TimeInterval = 600
    /// Watch side: the stamp of the last command acted on. Kept in defaults, since user info waits for the app's next
    /// launch; written once per command acted on.
    static let lastKey = "recording.dayCommandAt"

    /// Phone side: the stamps handed out, so a message that failed can tell whether a newer command went out since.
    nonisolated final class Stamps: Sendable {
        private let last = OSAllocatedUnfairLock(initialState: 0.0)

        /// The stamp for a command sent `now`: its time, or just after the previous stamp when the clock stepped back.
        func next(now: Date = .now) -> Double {
            last.withLock { newest in
                newest = max(now.timeIntervalSince1970, newest.nextUp)
                return newest
            }
        }

        /// Whether `stamp` is still the newest command's: false once another command was sent after it.
        func isNewest(_ stamp: Double) -> Bool { last.withLock { $0 == stamp } }
    }

    /// Watch side: whether to act on a command stamped `sentAt`; one acted on becomes the newest.
    static func accept(sentAt: Double, now: Date = .now, defaults: UserDefaults = .standard) -> Bool {
        guard sentAt.isFinite, now.timeIntervalSince1970 - sentAt <= freshness,
              sentAt > defaults.double(forKey: lastKey) else { return false }
        defaults.set(sentAt, forKey: lastKey)
        return true
    }
}

#if canImport(WatchConnectivity)
/// iPhone → Watch sync over WatchConnectivity: the server pairing and a rolling summary.
/// The watch talks to the hub directly for actions (answering, sending), so the phone only seeds it.
final class WatchSync: NSObject, WCSessionDelegate {
    static let shared = WatchSync()

    var onConfig: ((ServerConfig) -> Void)?
    /// Every group's credential (a phone that knows several hubs).
    var onConfigs: ((WatchConfigs) -> Void)?
    var onSummary: ((WatchSummary) -> Void)?
    /// The persona chosen on the phone (the watch shows the Milk Frog only for its own).
    var onPersona: ((String) -> Void)?
    /// Called once the session is activated; the phone pushes its pairing then (pushes before that are dropped).
    var onActivated: (() -> Void)?
    /// iPhone side: answers the watch's "send me the pairing" request.
    var configProvider: (() -> ServerConfig?)?
    var configsProvider: (() -> WatchConfigs?)?
    var onRecording: ((RecordingTransfer) -> Void)?
    /// Watch side: the meetings' minutes the phone relays (`WatchMinutes`).
    var onMinutes: ((WatchMinutes) -> Void)?
    var onRecordingReceipt: ((RecordingReceipt) -> Void)?
    var onRecordingEvent: (() -> Void)?

    // All-day recording (docs/assistant/day-recap.md「Watch to phone」). The phone's first three run synchronously on
    // WatchConnectivity's queue, so they are not main-actor slots: `RecordingDayReceiver` sets each once, before
    // `activate()`, and never again (nothing synchronizes them). Their closures are `@Sendable` and reach main-actor
    // state only by hopping there. Every other hook runs on the main actor, with its payload decoded already.
    /// iPhone side: a chunk's live envelope (`RecordingDayHeader.unframe`). `reply` takes the `RecordingDayAck` (JSON)
    /// once the chunk is on disk; not calling it leaves the chunk to the watch's resend.
    nonisolated(unsafe) var onDayChunk: (@Sendable (_ frame: Data, _ reply: @escaping @Sendable (Data) -> Void) -> Void)?
    /// iPhone side: a chunk sent by `transferFile` (`chunkKey` metadata). The system deletes `file` when this returns,
    /// so it is moved before; true when it was taken care of (stored, already held, or for a deleted day), false to
    /// leave it to the resend. The receiver acknowledges it with `sendDayAck`.
    nonisolated(unsafe) var onDayFile: (@Sendable (_ header: RecordingDayHeader, _ file: URL) -> Bool)?
    /// iPhone side: a control event, by message or user info. Returns the acknowledgement once the event is on disk
    /// (it may carry more of that recording's chunks and events), nil to leave the event to the watch's resend.
    /// WatchSync sends it: as the message's reply, else with `sendDayAck`.
    nonisolated(unsafe) var onDayEvent: (@Sendable (_ envelope: RecordingDayEventEnvelope) -> RecordingDayAck?)?
    /// iPhone side: the watch carried out an erase request (`sendDayErase`).
    var onDayErased: ((RecordingDayErased) -> Void)?
    /// iPhone side: the watch deleted its copy of a day recording (`sendDayDelete`).
    var onDayDeleted: ((RecordingDayDeleted) -> Void)?
    /// iPhone side: the watch's all-day state (`RecordingDayContract.watchStateKey` in its application context).
    var onDayWatchState: ((RecordingDayWatchState) -> Void)?
    /// iPhone side: the watch became reachable: the erase and delete requests still waiting for it go again.
    var onDayReachable: (() -> Void)?
    /// Watch side: the phone's switch (`RecordingDayContract.Command`). Only commands newer than the last one it got
    /// reach it (`WatchDayCommandOrder`): never a late, repeated or stale one.
    var onDayCommand: ((String) -> Void)?
    /// Watch side: every acknowledgement, whether it came in a message, in user info, or as the reply to an event.
    var onDayAck: ((RecordingDayAck) -> Void)?
    /// Watch side: the phone asks to erase a range. Returns the answer once the erase is saved (the same one for a
    /// repeated request), nil to leave the request to the phone's resend; WatchSync sends it, as the message's reply
    /// or with `sendDayErased`.
    var onDayEraseRequest: ((RecordingDayEraseRequest) -> RecordingDayErased?)?
    /// Watch side: the phone deleted a day recording. Returns the confirmation once the watch's copy is gone, nil to
    /// leave it to the phone's resend; WatchSync sends it like `onDayEraseRequest`'s answer.
    var onDayDelete: ((RecordingDayDelete) -> RecordingDayDeleted?)?
    /// Watch side: what the phone says of all-day recording changed (`recordingDay`, `recordingDayHub`,
    /// `recordingDayKeepDays`): its consent came or went, or how long the watch keeps chunks.
    var onDayCapabilities: (() -> Void)?

    private var lastSummaryPush = Date.distantPast
    private var pendingSummary: WatchSummary?
    #if os(iOS) || os(visionOS)
    /// What the app last gave `pushRecordingDay`, sent again at every activation.
    private var dayCapabilities: RecordingDayCapabilities?
    /// Whether the watch app was installed at the last look (activation, a change of the watch's state).
    private var watchAppWasInstalled = false
    #endif

    func activate() {
        guard WCSession.isSupported() else { return }
        WCSession.default.delegate = self
        WCSession.default.activate()
    }

    /// Every group's credential for the watch (docs/protocol/multi-hub.md「Watch, Siri, Mac, and demo mode」), what the groups are called and which have none
    /// yet; `config` (the first group's) stays for a watch app from before several hubs. Sent with no credentials too
    /// (#1185): a watch still holding one the phone no longer gives it drops it.
    func push(_ all: WatchConfigs) {
        var values: [String: Any] = ["configs": (try? JSONEncoder().encode(all)) ?? Data()]
        if let first = all.configs[""] ?? all.configs.values.first { values["config"] = (try? JSONEncoder().encode(first)) ?? Data() }
        update(values, removing: values["config"] == nil ? ["config"] : [])
        #if os(iOS) || os(visionOS)
        // Application context only keeps the latest state; user info is queued and delivered even if the watch app
        // was never opened, so a fresh install still gets paired.
        if WCSession.isSupported(), WCSession.default.activationState == .activated, WCSession.default.isWatchAppInstalled {
            WCSession.default.transferUserInfo(values)
        }
        #endif
    }

    /// The iPhone's interface language (docs/protocol/i18n.md: the watch follows the phone), as a code.
    func push(language: String) {
        update(["language": language])
    }

    /// The persona chosen on the phone, by id.
    func push(persona: String) {
        update(["persona": persona])
    }

    #if os(iOS) || os(visionOS)
    /// A paired watch with the app on it: the phone gets it credentials only then.
    var watchAppInstalled: Bool { WCSession.isSupported() && WCSession.default.isPaired && WCSession.default.isWatchAppInstalled }
    #endif

    #if os(watchOS)
    /// Watch side: ask the phone for its pairing right now (needs the phone nearby).
    func requestConfig(done: @escaping @MainActor (Bool) -> Void) {
        guard WCSession.isSupported(), WCSession.default.activationState == .activated, WCSession.default.isReachable else {
            done(false)
            return
        }
        // Both handlers run on WatchConnectivity's queue: `@Sendable` keeps them off the main actor (BackgroundCallbackTests).
        WCSession.default.sendMessage(["request": "config"], replyHandler: { @Sendable reply in
            nonisolated(unsafe) let r = reply
            Task { @MainActor in
                self.apply(r, full: false)
                done(r["configs"] != nil || r["config"] != nil) // the phone answered with its pairing
            }
        }, errorHandler: { @Sendable _ in Task { @MainActor in done(false) } })
    }
    #endif

    /// Throttled: application context is meant for state, not a stream.
    func push(summary: @autoclosure () -> WatchSummary) {
        #if os(iOS) || os(visionOS)
        guard WCSession.isSupported(), WCSession.default.activationState == .activated, WCSession.default.isWatchAppInstalled else { return }
        pendingSummary = summary()
        let wait = 2 - Date.now.timeIntervalSince(lastSummaryPush)
        if wait <= 0 { flushSummary() } else {
            Task { @MainActor in
                try? await Task.sleep(for: .seconds(wait))
                self.flushSummary()
            }
        }
        #endif
    }

    private func flushSummary() {
        guard let s = pendingSummary, let data = try? JSON.encoder.encode(s) else { return }
        pendingSummary = nil
        lastSummaryPush = .now
        update(["summary": data])
    }

    /// iPhone side: the meetings' minutes for the watch, when they changed.
    func push(minutes: WatchMinutes) {
        #if os(iOS) || os(visionOS)
        guard WCSession.isSupported(), WCSession.default.activationState == .activated, WCSession.default.isWatchAppInstalled,
              let data = minutes.data, WCSession.default.applicationContext[WatchMinutes.key] as? Data != data else { return }
        update([WatchMinutes.key: data])
        #endif
    }

    private func update(_ values: [String: Any], removing gone: [String] = []) {
        guard WCSession.isSupported(), WCSession.default.activationState == .activated else { return }
        var ctx = WCSession.default.applicationContext
        values.forEach { ctx[$0.key] = $0.value }
        gone.forEach { ctx[$0] = nil }
        try? WCSession.default.updateApplicationContext(ctx)
    }

    #if os(iOS) || os(visionOS)
    /// iPhone side: what it tells the watch about all-day recording (`RecordingDayCapabilities`): whether the user
    /// agreed (`recordingDay` 1, else 0), whether its hub lists `recording_day`, and how long the watch keeps
    /// unacknowledged chunks. Callable before the session activates: the latest values go out at activation. A push that
    /// changes nothing is skipped.
    func pushRecordingDay(consent: Bool, hub: Bool, keepDays: Int) {
        let capabilities = RecordingDayCapabilities(consent: consent, hub: hub, keepDays: keepDays)
        dayCapabilities = capabilities
        guard WCSession.isSupported(), WCSession.default.activationState == .activated,
              !capabilities.isIn(WCSession.default.applicationContext) else { return }
        update(capabilities.context)
    }

    /// iPhone side: the Recordings page's switch (`RecordingDayContract.Command`). The watch gets it at once while its app
    /// runs, else when it next runs. Only the newest command waits: a queued one is cancelled, and a message that fails
    /// after a newer command went out is not queued. The stamp (`WatchDayCommandOrder`) is what keeps the watch from
    /// acting on an older one that still gets through.
    func sendDayCommand(_ command: String) {
        guard Self.phoneCanSend else { return }
        for transfer in WCSession.default.outstandingUserInfoTransfers where transfer.userInfo[RecordingDayContract.commandKey] != nil {
            transfer.cancel()
        }
        let stamp = Self.dayCommandStamps.next()
        let payload = RecordingDayCommand(command: command, sentAt: stamp)
        Self.deliver([RecordingDayContract.commandKey: payload.data],
                     queuesOnFailure: { @Sendable in Self.dayCommandStamps.isNewest(stamp) })
    }

    /// Read by the failure path of `deliver`, on WatchConnectivity's queue.
    private nonisolated static let dayCommandStamps = WatchDayCommandOrder.Stamps()

    /// iPhone side: acknowledges chunks and events that came by file or user info (a live chunk's acknowledgement is its
    /// reply, an event message's too). Callable from any queue.
    nonisolated func sendDayAck(_ ack: RecordingDayAck) {
        guard Self.phoneCanSend, !ack.isEmpty else { return }
        Self.deliver([RecordingDayContract.ackKey: ack.data])
    }

    /// iPhone side: asks the watch to erase a range of a day recording (docs/assistant/day-recap.md「Erasing」). The
    /// answer reaches `onDayErased`, in the message's reply or later on its own; until it does the caller sends the
    /// request again (at launch, when the watch becomes reachable), and a copy still queued is not queued twice.
    func sendDayErase(_ request: RecordingDayEraseRequest) {
        guard Self.phoneCanSend else { return }
        Self.ask([RecordingDayContract.eraseKey: request.data], unlessQueued: { @Sendable info in
            // By identity: the queued copy's times went through the wire's milliseconds.
            RecordingDayEraseRequest(data: info[RecordingDayContract.eraseKey] as? Data)?.isSame(as: request) ?? false
        }, answer: { @Sendable [weak self] reply in
            guard let erased = RecordingDayErased(data: reply[RecordingDayContract.erasedKey] as? Data) else { return }
            Task { @MainActor in self?.onDayErased?(erased) }
        })
    }

    /// iPhone side: tells the watch a day recording was deleted (docs/assistant/day-recap.md「Deleting」); like
    /// `sendDayErase`, the confirmation reaches `onDayDeleted` and the caller resends until it does.
    func sendDayDelete(_ delete: RecordingDayDelete) {
        guard Self.phoneCanSend else { return }
        Self.ask([RecordingDayContract.deleteKey: delete.data], unlessQueued: { @Sendable info in
            RecordingDayDelete(data: info[RecordingDayContract.deleteKey] as? Data) == delete
        }, answer: { @Sendable [weak self] reply in
            guard let deleted = RecordingDayDeleted(data: reply[RecordingDayContract.deletedKey] as? Data) else { return }
            Task { @MainActor in self?.onDayDeleted?(deleted) }
        })
    }

    /// The watch app is there to send to.
    private nonisolated static var phoneCanSend: Bool {
        WCSession.isSupported() && WCSession.default.activationState == .activated && WCSession.default.isWatchAppInstalled
    }
    #endif

    #if os(watchOS)
    /// Watch side: the all-day state for the phone's Today row, in this side's application context; skipped when the
    /// phone already has the same.
    func publishDayWatchState(_ state: RecordingDayWatchState) {
        guard WCSession.isSupported(), WCSession.default.activationState == .activated, let data = state.data,
              WCSession.default.applicationContext[RecordingDayContract.watchStateKey] as? Data != data else { return }
        update([RecordingDayContract.watchStateKey: data])
    }

    /// Watch side: a control event (docs/assistant/day-recap.md「Control events」). While the phone is reachable it goes
    /// as a message whose reply is its acknowledgement (handed to `onDayAck`); otherwise, when the message fails, or
    /// with `queued` (the app is about to be suspended), as user info, which the phone acknowledges on its own. A copy of
    /// the same event still queued is not queued again, so resends never pile up out of order. False when there is no
    /// phone app to send to. Callable from any queue.
    @discardableResult nonisolated func sendDayEvent(_ envelope: RecordingDayEventEnvelope, queued: Bool = false) -> Bool {
        guard Self.watchCanSend else { return false }
        let unlessQueued: @Sendable ([String: Any]) -> Bool = { info in
            RecordingDayEventEnvelope(data: info[RecordingDayContract.eventKey] as? Data).map {
                $0.recording == envelope.recording && $0.event.n == envelope.event.n
            } ?? false
        }
        let message: [String: Any] = [RecordingDayContract.eventKey: envelope.data]
        guard !queued else {
            Self.queue(message, unlessQueued: unlessQueued)
            return true
        }
        Self.ask(message, unlessQueued: unlessQueued, answer: { @Sendable [weak self] reply in
            guard let ack = RecordingDayAck(data: reply[RecordingDayContract.ackKey] as? Data) else { return }
            Task { @MainActor in self?.onDayAck?(ack) }
        })
        return true
    }

    /// Watch side: the answer to an erase request that came without a reply handler (user info). Callable from any
    /// queue.
    nonisolated func sendDayErased(_ erased: RecordingDayErased) {
        guard Self.watchCanSend else { return }
        Self.deliver([RecordingDayContract.erasedKey: erased.data])
    }

    /// Watch side: the confirmation of a delete that came without a reply handler. Callable from any queue.
    nonisolated func sendDayDeleted(_ deleted: RecordingDayDeleted) {
        guard Self.watchCanSend else { return }
        Self.deliver([RecordingDayContract.deletedKey: deleted.data])
    }

    /// The phone app is there to send to.
    private nonisolated static var watchCanSend: Bool {
        WCSession.isSupported() && WCSession.default.activationState == .activated && WCSession.default.isCompanionAppInstalled
    }
    #endif

    /// A message reaches the other side at once while its app runs; otherwise, or when the message fails, it goes as
    /// user info, queued until that app next runs. `queuesOnFailure` runs when the message failed, on WatchConnectivity's
    /// queue: false drops it instead (a newer one went out since).
    private nonisolated static func deliver(_ message: [String: Any], queuesOnFailure: @escaping @Sendable () -> Bool = { true }) {
        let session = WCSession.default
        guard session.isReachable else {
            session.transferUserInfo(message)
            return
        }
        nonisolated(unsafe) let queued = message
        session.sendMessage(message, replyHandler: nil, errorHandler: { @Sendable _ in
            guard queuesOnFailure() else { return }
            WCSession.default.transferUserInfo(queued)
        })
    }

    /// Like `deliver`, for a message the other side answers: `answer` gets the reply, on WatchConnectivity's queue (an
    /// empty one when the other side had no answer yet). Not sent again as user info while a copy (`unlessQueued`) is
    /// still queued.
    private nonisolated static func ask(_ message: [String: Any], unlessQueued: @escaping @Sendable ([String: Any]) -> Bool,
                                        answer: @escaping @Sendable ([String: Any]) -> Void) {
        let session = WCSession.default
        guard session.isReachable else {
            queue(message, unlessQueued: unlessQueued)
            return
        }
        nonisolated(unsafe) let queued = message
        session.sendMessage(message, replyHandler: { @Sendable reply in answer(reply) }, errorHandler: { @Sendable _ in
            queue(queued, unlessQueued: unlessQueued)
        })
    }

    /// User info, unless a copy of it is still waiting for the system to deliver it.
    private nonisolated static func queue(_ message: [String: Any], unlessQueued: ([String: Any]) -> Bool) {
        let session = WCSession.default
        guard !session.outstandingUserInfoTransfers.contains(where: { unlessQueued($0.userInfo) }) else { return }
        session.transferUserInfo(message)
    }

    /// The iPhone side sets this once it can hold rolling recordings; an older phone never does, so the watch keeps to plain recordings.
    nonisolated static let phoneRollingKey = WatchPhoneCapabilities.rollingKey
    /// Watch side: the phone takes all-day recordings (an Int: absent with an older phone, 0 until the user agrees on
    /// the phone, else the contract version), its hub lists `recording_day` (a Bool; absent until the phone says), and
    /// how many days the watch keeps an unacknowledged chunk (an Int 1…7; absent until the phone says, read as 7 by
    /// `WatchPhoneCapabilities.keepDays`).
    nonisolated static let phoneDayKey = WatchPhoneCapabilities.dayKey
    nonisolated static let phoneDayHubKey = WatchPhoneCapabilities.dayHubKey
    nonisolated static let phoneDayKeepDaysKey = WatchPhoneCapabilities.keepDaysKey

    /// `full`: the whole application context (at activation, or a new one arrived), which also says what the phone no
    /// longer offers; user info and config replies are partial.
    private func apply(_ ctx: [String: Any], full: Bool) {
        WatchPhoneCapabilities.storeRolling(ctx)
        let dayChanged = WatchPhoneCapabilities.storeDay(ctx, full: full) > 0
        #if os(watchOS)
        if let language = ctx["language"] as? String {
            LanguageStore.shared.follow(language)
            if AppLanguage.codes.contains(language) {
                LanguageStore.groupDefaults()?.set(language, forKey: LanguageStore.resolvedKey)
            }
        }
        #endif
        if let persona = ctx["persona"] as? String { onPersona?(persona) }
        if let data = ctx["configs"] as? Data, let all = try? JSONDecoder().decode(WatchConfigs.self, from: data) {
            onConfigs?(all)
        } else if let data = ctx["config"] as? Data, let cfg = try? JSONDecoder().decode(ServerConfig.self, from: data) {
            onConfig?(cfg)
        }
        if let data = ctx["summary"] as? Data, let s = try? JSON.decoder.decode(WatchSummary.self, from: data) {
            onSummary?(s)
        }
        if let minutes = WatchMinutes(data: ctx[WatchMinutes.key] as? Data) { onMinutes?(minutes) }
        if let state = RecordingDayWatchState(data: ctx[RecordingDayContract.watchStateKey] as? Data) { onDayWatchState?(state) }
        #if os(watchOS)
        if dayChanged { onDayCapabilities?() }
        #else
        _ = dayChanged
        #endif
    }

    /// Routes a message's or user info's all-day keys; true when it carried one (it is then nothing else). `reply` is
    /// the message's reply handler, called exactly once for a day key; nil for user info and messages sent without one.
    /// Payloads are decoded here, on WatchConnectivity's queue, so only Sendable values cross to the main actor.
    private nonisolated func routeDay(_ info: [String: Any], reply: (@Sendable ([String: Any]) -> Void)?) -> Bool {
        guard let key = RecordingDayContract.messageKeys.first(where: { info[$0] != nil }) else { return false }
        let data = info[key] as? Data
        switch key {
        case RecordingDayContract.eventKey: receiveDayEvent(data, reply: reply)
        case RecordingDayContract.eraseKey: receiveDayErase(data, reply: reply)
        case RecordingDayContract.deleteKey: receiveDayDelete(data, reply: reply)
        case RecordingDayContract.commandKey:
            receiveDayCommand(RecordingDayCommand(data: data))
            reply?([:])
        default:
            receiveDayAnswer(key, data)
            reply?([:])
        }
        return true
    }

    /// iPhone side: an event is acknowledged once the receiver has it on disk, in the reply when there is one.
    private nonisolated func receiveDayEvent(_ data: Data?, reply: (@Sendable ([String: Any]) -> Void)?) {
        let ack = RecordingDayEventEnvelope(data: data).flatMap { onDayEvent?($0) }
        if let reply {
            reply(ack.map { [RecordingDayContract.ackKey: $0.data] } ?? [:])
            return
        }
        #if os(iOS) || os(visionOS)
        if let ack { sendDayAck(ack) }
        #endif
    }

    /// Watch side: the answer goes back once the erase is saved, the same way the request came.
    private nonisolated func receiveDayErase(_ data: Data?, reply: (@Sendable ([String: Any]) -> Void)?) {
        guard let request = RecordingDayEraseRequest(data: data) else {
            reply?([:])
            return
        }
        Task { @MainActor in
            let erased = self.onDayEraseRequest?(request)
            if let reply {
                reply(erased.map { [RecordingDayContract.erasedKey: $0.data] } ?? [:])
                return
            }
            #if os(watchOS)
            if let erased { self.sendDayErased(erased) }
            #endif
        }
    }

    /// Watch side: the confirmation goes back once the watch's copy is gone, the same way the delete came.
    private nonisolated func receiveDayDelete(_ data: Data?, reply: (@Sendable ([String: Any]) -> Void)?) {
        guard let delete = RecordingDayDelete(data: data) else {
            reply?([:])
            return
        }
        Task { @MainActor in
            let deleted = self.onDayDelete?(delete)
            if let reply {
                reply(deleted.map { [RecordingDayContract.deletedKey: $0.data] } ?? [:])
                return
            }
            #if os(watchOS)
            if let deleted { self.sendDayDeleted(deleted) }
            #endif
        }
    }

    /// Watch side: ordered on the main actor, one command at a time, so a late or repeated one is dropped there. A
    /// command nobody takes yet doesn't count as the newest.
    private nonisolated func receiveDayCommand(_ command: RecordingDayCommand?) {
        guard let command else { return }
        Task { @MainActor in
            guard let handle = self.onDayCommand, WatchDayCommandOrder.accept(sentAt: command.sentAt) else { return }
            handle(command.command)
        }
    }

    /// Acknowledgements (watch side) and answers to the phone's erase and delete requests (iPhone side) that came on
    /// their own. A chunk header never comes this way (only as `transferFile` metadata): it is dropped.
    private nonisolated func receiveDayAnswer(_ key: String, _ data: Data?) {
        switch key {
        case RecordingDayContract.ackKey:
            guard let ack = RecordingDayAck(data: data) else { return }
            Task { @MainActor in self.onDayAck?(ack) }
        case RecordingDayContract.erasedKey:
            guard let erased = RecordingDayErased(data: data) else { return }
            Task { @MainActor in self.onDayErased?(erased) }
        case RecordingDayContract.deletedKey:
            guard let deleted = RecordingDayDeleted(data: data) else { return }
            Task { @MainActor in self.onDayDeleted?(deleted) }
        default: break
        }
    }

    nonisolated func session(_ session: WCSession, activationDidCompleteWith state: WCSessionActivationState, error: Error?) {
        nonisolated(unsafe) let ctx = session.receivedApplicationContext
        #if os(iOS) || os(visionOS)
        let installed = session.isWatchAppInstalled
        #endif
        Task { @MainActor in
            self.apply(ctx, full: true)
            #if os(iOS) || os(visionOS)
            self.watchAppWasInstalled = installed
            self.update(Self.capabilityContext(day: self.dayCapabilities))
            #endif
            self.onActivated?()
            self.onRecordingEvent?()
        }
    }

    #if os(iOS) || os(visionOS)
    /// iPhone side: what the phone takes, for the watch's application context: rolling recordings, and all-day
    /// recording's keys only once the app said what they are (`pushRecordingDay`), so the context never carries a
    /// consent nobody gave.
    static func capabilityContext(day: RecordingDayCapabilities?) -> [String: Any] {
        var capabilities: [String: Any] = ["recordingRolling": true]
        if let day { capabilities.merge(day.context) { $1 } }
        return capabilities
    }

    /// iPhone side: the watch app was installed after the session activated. A context sent while the watch had no app
    /// never got there (`updateApplicationContext` fails then, and nothing sends it again while the values stay the
    /// same), so it goes now: without it the watch would not offer All-Day Recording, nor point to the phone for the
    /// consent. Other changes of the watch's state (a complication added) send nothing.
    nonisolated func sessionWatchStateDidChange(_ session: WCSession) {
        let installed = session.isWatchAppInstalled
        Task { @MainActor in
            let arrived = installed && !self.watchAppWasInstalled
            self.watchAppWasInstalled = installed
            guard arrived else { return }
            self.update(Self.capabilityContext(day: self.dayCapabilities))
        }
    }
    #endif

    nonisolated func session(_ session: WCSession, didReceiveUserInfo userInfo: [String: Any] = [:]) {
        // All-day events, acknowledgements, requests, answers and commands first: none of them is a context.
        if routeDay(userInfo, reply: nil) { return }
        let recording = RecordingTransfer(dictionary: userInfo)
        let receipt = RecordingReceipt(dictionary: userInfo)
        nonisolated(unsafe) let copy = userInfo
        Task { @MainActor in
            if let recording { self.onRecording?(recording) }
            else if let receipt { self.onRecordingReceipt?(receipt) }
            else { self.apply(copy, full: false) }
        }
    }

    nonisolated func session(_ session: WCSession, didReceiveMessage message: [String: Any], replyHandler: @escaping ([String: Any]) -> Void) {
        nonisolated(unsafe) let reply = replyHandler
        if routeDay(message, reply: { @Sendable answer in reply(answer) }) { return }
        Task { @MainActor in
            var answer: [String: Any] = [:]
            if let cfg = self.configProvider?(), let data = try? JSONEncoder().encode(cfg) { answer["config"] = data }
            if let all = self.configsProvider?(), let data = try? JSONEncoder().encode(all) { answer["configs"] = data }
            reply(answer)
        }
    }

    /// Messages sent without a reply handler: all-day commands, acknowledgements and answers.
    nonisolated func session(_ session: WCSession, didReceiveMessage message: [String: Any]) {
        _ = routeDay(message, reply: nil)
    }

    /// iPhone side: an all-day chunk's live envelope. The receiver stores it, then replies with its acknowledgement;
    /// without a receiver the empty reply acknowledges nothing and the watch sends it again.
    nonisolated func session(_ session: WCSession, didReceiveMessageData messageData: Data, replyHandler: @escaping (Data) -> Void) {
        nonisolated(unsafe) let reply = replyHandler
        guard let receive = onDayChunk else {
            reply(Data())
            return
        }
        receive(messageData) { @Sendable answer in reply(answer) }
    }

    nonisolated func session(_ session: WCSession, didReceiveApplicationContext ctx: [String: Any]) {
        nonisolated(unsafe) let copy = ctx
        Task { @MainActor in self.apply(copy, full: true) }
    }

    nonisolated func sessionReachabilityDidChange(_ session: WCSession) {
        let reachable = session.isReachable
        Task { @MainActor in
            self.onRecordingEvent?()
            if reachable { self.onDayReachable?() }
        }
    }

    nonisolated func session(_ session: WCSession, didReceive file: WCSessionFile) {
        #if os(iOS) || os(visionOS)
        // An all-day chunk: the receiver takes the file before this returns and acknowledges it itself. One whose header
        // is not valid is left alone (never acknowledged), not taken for a rolling segment.
        if file.metadata?[RecordingDayContract.chunkKey] != nil {
            if let header = RecordingDayHeader(userInfo: file.metadata) { _ = onDayFile?(header, file.fileURL) }
            return
        }
        guard let transfer = RecordingTransfer(dictionary: file.metadata), let segment = transfer.segment else { return }
        // The system deletes the temporary file once the callback returns: move it synchronously first, then let the main thread update the index.
        let dir = RecordingLibrary.directory.appendingPathComponent(transfer.id.uuidString, isDirectory: true)
        let destination = dir.appendingPathComponent(RecordingSegment.fileName(segment.index))
        do {
            try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
            if !FileManager.default.fileExists(atPath: destination.path) {
                try FileManager.default.moveItem(at: file.fileURL, to: destination)
            }
            Task { @MainActor in self.onRecording?(transfer) }
        } catch { /* Not acknowledged; the watch keeps the audio and retries. */ }
        #endif
    }

    nonisolated func session(_ session: WCSession, didFinish fileTransfer: WCSessionFileTransfer, error: Error?) {
        #if os(watchOS)
        let failed = error != nil
        if let header = RecordingDayHeader(userInfo: fileTransfer.file.metadata) {
            Task { @MainActor in WatchDayTransfer.shared.finished(header, failed: failed) }
            return
        }
        guard let transfer = RecordingTransfer(dictionary: fileTransfer.file.metadata) else { return }
        Task { @MainActor in WatchRecordingTransfer.shared.finished(transfer, failed: failed) }
        #endif
    }

    nonisolated func session(_ session: WCSession, didFinish userInfoTransfer: WCSessionUserInfoTransfer, error: Error?) {
        #if os(watchOS)
        let failed = error != nil
        // An all-day event that went as user info: a failure goes again after its backoff, a delivery waits for the ack.
        if let envelope = RecordingDayEventEnvelope(data: userInfoTransfer.userInfo[RecordingDayContract.eventKey] as? Data) {
            Task { @MainActor in WatchDayTransfer.shared.finished(envelope, failed: failed) }
            return
        }
        guard let transfer = RecordingTransfer(dictionary: userInfoTransfer.userInfo) else { return }
        Task { @MainActor in WatchRecordingTransfer.shared.finished(transfer, failed: failed) }
        #endif
    }

    func confirmRecording(_ receipt: RecordingReceipt) {
        guard WCSession.default.activationState == .activated else { return }
        WCSession.default.transferUserInfo(receipt.dictionary)
    }

    #if os(iOS) || os(visionOS)
    nonisolated func sessionDidBecomeInactive(_ session: WCSession) {}
    nonisolated func sessionDidDeactivate(_ session: WCSession) { session.activate() }
    #endif
}
#endif
