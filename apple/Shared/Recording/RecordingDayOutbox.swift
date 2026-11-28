import Foundation

/// How the watch's all-day recording reaches the phone (docs/assistant/day-recap.md「Chunks」,「Control events」and
/// 「Acknowledgements and retries」): which unacknowledged chunk or control event goes now, and which way. Pure and
/// `nonisolated`, so the rules are unit tested on the phone's test target while the watch's `WatchDayTransfer` only
/// carries them out over WatchConnectivity. Nothing here drops anything by age: only an acknowledgement takes a chunk
/// or an event out (the watch's age limit is `RecordingDayWatchLedger.ageOut`, which reports the loss).
///
/// The rules, in the order they apply:
/// - Suspending (the app is about to stop running: a pause, a stop, a halt, leaving the front, a background wake while
///   nothing records; `suspends`): everything not already with the system is queued with it now, chunks as files and
///   events as user info, messages still awaiting their reply included, since a message dies with the app and a queued
///   transfer doesn't. Nothing is taken back from the system then. The phone drops what it already holds.
/// - Becoming reachable: outstanding files of chunks that fit a message are cancelled and the chunks go live, oldest
///   first; bigger ones (the minute-long chunks recorded out of reach) stay with the system.
/// - Reachable: events and every chunk that fits a message (an envelope under `maxMessageBytes`) go by message, oldest
///   first, at most `maxInFlightMessages` of each at once; a bigger chunk goes as a file. In Low Power Mode chunk
///   messages wait until the oldest of them ended `lowPowerHoldSeconds` ago, then all go together. A chunk whose
///   message failed and that is `liveDelaySeconds` behind goes as a file, its retry still awaiting a reply included:
///   the live path doesn't hold audio longer than that.
/// - Not reachable: chunks go as files as soon as they are finished, every one of them (a backlog left in the app would
///   wait for its next run if it ended without a hand-off), and events as user info.
/// - Retries: a message without its acknowledgement after `ackTimeout`, or with an error, goes again after a backoff
///   (`backoffFirst` doubling to `backoffMax`). A queued transfer goes again only after the system reports it failed,
///   or when no acknowledgement came `fileAckTimeout` after it was delivered or was found gone from the system's
///   queue; at most one is with the system per chunk or event. A failed message's backoff doesn't hold back the
///   queued way: such a chunk may still go as a file at once. Becoming reachable retries at once.
nonisolated struct RecordingDayOutbox {
    /// A chunk (by its `seq`) or a control event (by its `n`) of a day recording.
    nonisolated struct Key: Hashable, Sendable {
        nonisolated enum Kind: Hashable, Sendable { case chunk, event }

        var recording: UUID
        var kind: Kind
        /// The chunk's `seq`, or the event's `n`.
        var number: Int

        static func chunk(_ recording: UUID, seq: Int) -> Key { Key(recording: recording, kind: .chunk, number: seq) }
        static func event(_ recording: UUID, n: Int) -> Key { Key(recording: recording, kind: .event, number: n) }
        var isEvent: Bool { kind == .event }
    }

    /// A chunk or an event the phone has not acknowledged yet.
    nonisolated struct Item: Sendable, Equatable {
        var key: Key
        /// Its place in the queue, oldest first: a chunk's first sample; an event's recording start, so a recording's
        /// events go before its chunks and among themselves in `n` order.
        var startedAt: Date
        /// When a chunk's audio ended (the Low Power hold goes by it); an event's own time.
        var finishedAt: Date
        /// The size of a chunk's live envelope (`RecordingDayHeader.frame`); 0 for an event, which always fits a message.
        var frameBytes: Int
    }

    nonisolated enum Action: Sendable, Equatable {
        /// A chunk: `sendMessageData` with its envelope. An event: a message. Either way the reply acknowledges it.
        case message(Key)
        /// A chunk: `transferFile` of its audio with its header as metadata.
        case file(Key)
        /// An event: `transferUserInfo`.
        case userInfo(Key)
        /// A chunk: cancel its outstanding `transferFile`; it goes live instead.
        case cancel(Key)

        var key: Key {
            switch self {
            case .message(let key), .file(let key), .userInfo(let key), .cancel(let key): key
            }
        }
    }

    /// Low Power Mode on the live path: chunk messages wait until the oldest one's end is this old, so its first sample
    /// is at most `lowPowerTransferBatchSeconds` (the contract's five minutes) behind when it leaves.
    static let lowPowerHoldSeconds = RecordingAudioSpec.lowPowerTransferBatchSeconds - RecordingDayContract.liveChunkSeconds
    /// The most the live path keeps a chunk recorded while the phone is reachable: one whose message failed goes as a
    /// file once its first sample is this old.
    static let liveDelaySeconds = RecordingAudioSpec.lowPowerTransferBatchSeconds
    /// Chunk messages failing in a row (an error, or no reply in time) after which the live path counts as down
    /// (`liveUp`), although the session still says the phone is reachable: in the background that flag is optimistic.
    static let maxMessageFailures = 3
    /// How long the live path stays down before the capture tries it again with a short chunk, while the session keeps
    /// saying the phone is reachable: only a chunk message's reply shows the path works again, and the minute-long
    /// chunks cut meanwhile never go by message.
    static let liveProbeSeconds = RecordingDayContract.backoffMax
    /// While the system holds an event's user info, a pass looks this often whether it still does. Nothing else
    /// would notice the transfer ending: `WatchSync` passes on the end of a file transfer but not of user info. So an
    /// event delivered without an acknowledgement goes again at most this much later than a file would.
    static let userInfoCheckSeconds = RecordingDayContract.fileAckTimeout

    /// How a send went: by message, or queued with the system (a file or user info).
    private nonisolated enum Route: Sendable, Equatable { case message, queued }
    /// `delivered`: the system says a queued transfer reached the phone; its acknowledgement may still be on its way.
    private nonisolated enum Progress: Sendable, Equatable { case awaiting, delivered(Date), failed }
    private nonisolated struct Send: Sendable, Equatable {
        var route: Route
        /// When it went; for a transfer the system holds, when it was last seen in the system's queue.
        var at: Date
        var progress: Progress
    }

    private var sends: [Key: Send] = [:]
    private var failures: [Key: Int] = [:]
    private var retryAt: [Key: Date] = [:]
    /// Low Power Mode: chunks that ended by then are released from the hold.
    private var lowPowerReleased: Date?
    /// The last plan held something until then (the Low Power hold, a chunk turning overdue for the live path).
    private var heldUntil: Date?
    /// The phone became reachable: the next reachable plan takes the small chunks back from the system.
    private var reclaiming = false
    /// Chunks whose file transfer was cancelled to go live: the system's report of that transfer says nothing about
    /// them.
    private var cancelled: Set<Key> = []
    /// Chunk messages that failed in a row, for `liveUp`.
    private var messageFailuresInARow = 0
    /// When the latest of those failures came, once they count as the live path down: it is tried again
    /// `liveProbeSeconds` later.
    private var liveDownAt: Date?
    /// What the system held at the last plan: one gone from its queue by the next is done with, as far as this can
    /// tell.
    private var held: Set<Key> = []

    // MARK: - Planning

    /// What to send now. `items`: everything the phone has not acknowledged, in any order. `inFlight`: chunks whose
    /// message is being framed or awaits its reply. `withSystem`: chunks and events the system still holds as a file or
    /// user info transfer (it keeps them across launches, this doesn't). Actions come oldest first, cancellations before
    /// the messages that replace them.
    mutating func plan(_ items: [Item], now: Date, reachable: Bool, lowPower: Bool, suspending: Bool,
                       inFlight: Set<Key>, withSystem: Set<Key>) -> [Action] {
        let offered = Set(items.map(\.key))
        for key in Set(sends.keys).union(failures.keys).union(retryAt.keys) where !offered.contains(key) { forget(key) }
        cancelled.formIntersection(offered)
        heldUntil = nil
        let ordered = items.sorted(by: Self.oldestFirst)
        var system = withSystem.intersection(offered)
        var actions: [Action] = []
        if reclaiming, reachable, !suspending {
            reclaiming = false
            for item in ordered where !item.key.isEvent && system.contains(item.key) && Self.fits(item) {
                actions.append(.cancel(item.key))
                system.remove(item.key)
                forget(item.key)
                cancelled.insert(item.key)
            }
        }
        leftTheSystem(held.subtracting(system), at: now)
        held = system
        for key in system { heldBySystem(key, at: now) }
        expire(now: now, withSystem: system)
        // Queued and not failed: with the system, or delivered and waiting for the phone's acknowledgement. Never sent
        // again until that fails, so at most one transfer is out per chunk or event.
        let queued = Set(sends.filter { $0.value.route == .queued && $0.value.progress != .failed }.keys)
        if suspending {
            let handed = ordered.filter { !queued.contains($0.key) }
            return actions + handed.map { $0.key.isEvent ? .userInfo($0.key) : .file($0.key) }
        }
        let awaiting = sends.filter { $0.value.route == .message && $0.value.progress == .awaiting }.keys
        // A message overdue for its reply stops counting as in flight: it goes again once its backoff ends.
        let flying = inFlight.filter { sends[$0]?.progress != .failed }.union(awaiting).intersection(offered)
        let overtaken = overdueRetries(ordered, flying: flying, now: now)
        let waiting = ordered.filter { !queued.contains($0.key) && (!flying.contains($0.key) || overtaken.contains($0.key)) }
        // Overtaken messages keep their place for this plan: they are still out until the transport drops them.
        var budget = Budget(flying: flying)
        let released = reachable && lowPower ? lowPowerRelease(waiting, now: now) : .distantFuture
        for item in waiting {
            let route = way(for: item, reachable: reachable, now: now)
            guard isDue(item.key, by: route, now: now) else { continue }
            switch (item.key.kind, route) {
            case (.event, .message): if budget.take(\.eventMessages) { actions.append(.message(item.key)) }
            // The system's queues take everything: each chunk is queued as soon as it is finished.
            case (.event, .queued): actions.append(.userInfo(item.key))
            case (.chunk, .queued): actions.append(.file(item.key))
            case (.chunk, .message):
                if item.finishedAt <= released, budget.take(\.chunkMessages) { actions.append(.message(item.key)) }
            }
        }
        return actions
    }

    /// The places left for messages awaiting their reply, chunks' and events' (`maxInFlightMessages` each).
    private nonisolated struct Budget {
        var chunkMessages: Int
        var eventMessages: Int

        init(flying: Set<Key>) {
            let flyingEvents = flying.filter(\.isEvent).count
            chunkMessages = RecordingDayContract.maxInFlightMessages - (flying.count - flyingEvents)
            eventMessages = RecordingDayContract.maxInFlightMessages - flyingEvents
        }

        /// Takes one place of that kind; false when none is left.
        mutating func take(_ place: WritableKeyPath<Budget, Int>) -> Bool {
            guard self[keyPath: place] > 0 else { return false }
            self[keyPath: place] -= 1
            return true
        }
    }

    /// Which way an item goes now. Reachable: by message, unless it is a chunk too big for one, or one the live path
    /// failed to deliver within `liveDelaySeconds`. Out of reach: queued with the system.
    private mutating func way(for item: Item, reachable: Bool, now: Date) -> Route {
        guard reachable else { return .queued }
        guard !item.key.isEvent else { return .message }
        guard Self.fits(item) else { return .queued }
        return isOverdue(item, now: now) ? .queued : .message
    }

    /// The chunk messages in flight that are retries past their live deadline: a reply that doesn't come keeps
    /// nothing on the live path longer than `liveDelaySeconds`, so these go as files now, overtaking the message (its
    /// acknowledgement still counts). A first try isn't one: it left on time, and its timeout makes it a retry.
    private mutating func overdueRetries(_ ordered: [Item], flying: Set<Key>, now: Date) -> Set<Key> {
        var overdue = Set<Key>()
        for item in ordered where flying.contains(item.key) && isOverdue(item, now: now) {
            overdue.insert(item.key)
        }
        return overdue
    }

    /// Whether a chunk the live path failed is `liveDelaySeconds` behind (its first sample that old). One not yet
    /// overdue holds the plan until then, so the transport wakes at that deadline even while a retry awaits its reply.
    /// Never for an event, a chunk too big for a message (a file anyway) or one whose messages haven't failed.
    private mutating func isOverdue(_ item: Item, now: Date) -> Bool {
        guard !item.key.isEvent, Self.fits(item), (failures[item.key] ?? 0) > 0 else { return false }
        let deadline = item.startedAt.addingTimeInterval(Self.liveDelaySeconds)
        guard now < deadline else { return true }
        hold(until: deadline)
        return false
    }

    /// Whether a backoff still holds this back. A failed message's doesn't hold back the queued way: the system delivers
    /// a file or user info without the live path, so a chunk the live path failed goes as a file at once.
    private func isDue(_ key: Key, by route: Route, now: Date) -> Bool {
        if route == .queued, sends[key]?.route == .message { return true }
        return (retryAt[key] ?? .distantPast) <= now
    }

    /// Whether an envelope of this size may go as a message: the whole envelope stays under `maxMessageBytes`, so one
    /// of exactly that size goes as a file. The planner and the transport's check of a framed envelope both use it.
    static func fitsMessage(_ frameBytes: Int) -> Bool { frameBytes < RecordingDayContract.maxMessageBytes }

    private static func fits(_ item: Item) -> Bool { fitsMessage(item.frameBytes) }

    /// Low Power Mode: the end time up to which chunk messages may go now. Once the oldest waiting one ended
    /// `lowPowerHoldSeconds` ago, everything finished by then is released and keeps going (a few messages at a time)
    /// while later chunks start a new hold.
    private mutating func lowPowerRelease(_ waiting: [Item], now: Date) -> Date {
        let released = lowPowerReleased ?? .distantPast
        let unreleased = waiting.filter { item in
            !item.key.isEvent && Self.fits(item) && item.finishedAt > released
        }
        guard let oldest = unreleased.map(\.finishedAt).min() else { return released }
        let due = oldest.addingTimeInterval(Self.lowPowerHoldSeconds)
        guard now >= due else {
            hold(until: due)
            return released
        }
        lowPowerReleased = now
        return now
    }

    private mutating func hold(until due: Date) {
        heldUntil = min(heldUntil ?? due, due)
    }

    /// Oldest first: by the item's place in time, then by recording, events before chunks, then by number. Days sort
    /// by their times, so yesterday's backlog goes before today's live chunks.
    private static func oldestFirst(_ a: Item, _ b: Item) -> Bool {
        if a.startedAt != b.startedAt { return a.startedAt < b.startedAt }
        if a.key.recording != b.key.recording { return a.key.recording.uuidString < b.key.recording.uuidString }
        if a.key.kind != b.key.kind { return a.key.isEvent }
        return a.key.number < b.key.number
    }

    // MARK: - What happened

    /// The transport sent `action` (for a chunk message: committed to it, before its envelope is read).
    mutating func sent(_ action: Action, at time: Date) {
        let route: Route
        switch action {
        case .message: route = .message
        case .file, .userInfo: route = .queued
        case .cancel: return
        }
        sends[action.key] = Send(route: route, at: time, progress: .awaiting)
        retryAt[action.key] = nil
        // A new file for the chunk: the next report is taken as this transfer's.
        if case .file = action { cancelled.remove(action.key) }
    }

    /// The system finished a queued transfer: a chunk's file, or an event's user info. Delivered, it now waits
    /// `fileAckTimeout` for its acknowledgement; a failure backs off, also after a pass found it gone from the queue
    /// first. The report of a transfer that no longer speaks for the chunk (cancelled to go live) is ignored once the
    /// chunk went another way.
    mutating func transferFinished(_ key: Key, failed: Bool, at time: Date) {
        // A cancelled transfer's failure, or its delivery once the chunk went by message: the message is what counts.
        if cancelled.remove(key) != nil, failed || sends[key] != nil { return }
        guard let send = sends[key] else {
            // A transfer queued before the app last ended: the system remembers it, this doesn't.
            sends[key] = Send(route: .queued, at: time, progress: failed ? .failed : .delivered(time))
            if failed { fail(key, at: time) }
            return
        }
        guard send.route == .queued, send.progress != .failed else { return }
        if failed {
            fail(key, at: time)
        } else if send.progress == .awaiting {
            sends[key]?.progress = .delivered(time)
        }
    }

    /// A message failed: an error, or a reply without its acknowledgement (`answered`: the phone did answer, so the
    /// live path itself works). One that went overdue before this came is not counted twice.
    mutating func messageFailed(_ key: Key, at time: Date, answered: Bool = false) {
        guard let send = sends[key], send.route == .message, send.progress == .awaiting else { return }
        fail(key, at: time)
        if !answered, !key.isEvent { chunkMessageFailed(at: time) }
    }

    /// The phone answered a chunk message: the live path works.
    mutating func linkAnswered() {
        messageFailuresInARow = 0
        liveDownAt = nil
    }

    /// Whether the capture should cut short chunks for the live path: the session says the phone is reachable, and
    /// chunk messages haven't kept failing, or failed long enough ago (`liveProbeSeconds`) that the next short chunk
    /// tries the live path again. Its reply brings the path back (`linkAnswered`); its failure puts it down again for
    /// as long.
    func liveUp(reachable: Bool, now: Date) -> Bool {
        guard reachable else { return false }
        guard messageFailuresInARow >= Self.maxMessageFailures, let down = liveDownAt else { return true }
        return now >= down.addingTimeInterval(Self.liveProbeSeconds)
    }

    /// One more chunk message failed in a row (an error, or no reply in time).
    private mutating func chunkMessageFailed(at time: Date) {
        messageFailuresInARow += 1
        if messageFailuresInARow >= Self.maxMessageFailures { liveDownAt = max(liveDownAt ?? time, time) }
    }

    /// The phone holds these: nothing about them is kept.
    mutating func acked(_ keys: some Sequence<Key>) {
        for key in keys {
            forget(key)
            cancelled.remove(key)
        }
    }

    /// What was sent for this no longer counts (its envelope turned out too big for a message, or it changed while it
    /// was read): it is planned afresh, without a backoff.
    mutating func reset(_ key: Key) {
        forget(key)
    }

    /// Becoming reachable retries at once: backoffs end and start again from `backoffFirst`, messages that went out
    /// before the link dropped are sent again rather than waiting for their timeout, and the next plan takes small
    /// chunks back from the system to send them live.
    mutating func reachabilityChanged(_ reachable: Bool) {
        messageFailuresInARow = 0
        liveDownAt = nil
        reclaiming = reachable
        guard reachable else { return }
        retryAt.removeAll()
        failures.removeAll()
        for (key, send) in sends where send.route == .message && send.progress == .awaiting {
            sends[key]?.progress = .failed
        }
    }

    /// When a plan could next do something without a new event (a backoff ends, a reply or an acknowledgement is
    /// overdue, a hold ends, a queued event is due a look, the live path is due another try); nil when only an event
    /// can change anything (a chunk or an event to send, an acknowledgement, a reachability change, a file transfer
    /// finishing). A file the system holds wakes nothing: it may wait hours for the phone, and its end comes as
    /// `transferFinished`. An event's user info wakes a pass every `userInfoCheckSeconds`, since nothing reports its end
    /// yet.
    func nextWake(after now: Date) -> Date? {
        var times = Array(retryAt.values)
        if let heldUntil { times.append(heldUntil) }
        // The pass then tells the capture the live path is worth a try (`liveUp`).
        if messageFailuresInARow >= Self.maxMessageFailures, let liveDownAt {
            times.append(liveDownAt.addingTimeInterval(Self.liveProbeSeconds))
        }
        for (key, send) in sends {
            switch (send.route, send.progress) {
            case (.message, .awaiting): times.append(send.at.addingTimeInterval(RecordingDayContract.ackTimeout))
            case (.queued, .delivered(let at)): times.append(at.addingTimeInterval(RecordingDayContract.fileAckTimeout))
            // Not in the system's queue at the last look (queued since, or already gone): its acknowledgement's
            // deadline, as `expire` counts it. A pass at that time finds it still queued if it is.
            case (.queued, .awaiting) where !held.contains(key):
                times.append(send.at.addingTimeInterval(RecordingDayContract.fileAckTimeout))
            // An event the system still holds: the next look whether it does.
            case (.queued, .awaiting) where key.isEvent: times.append(send.at.addingTimeInterval(Self.userInfoCheckSeconds))
            default: break
            }
        }
        return times.filter { $0 > now }.min()
    }

    /// The wait after the `failures`th failure in a row: `backoffFirst`, doubling up to `backoffMax`.
    static func backoff(_ failures: Int) -> Double {
        let doublings = Double(min(max(failures - 1, 0), 30))
        return min(RecordingDayContract.backoffMax, RecordingDayContract.backoffFirst * pow(2, doublings))
    }

    /// Something the system holds counts as one queued send, last seen there now: a message the session turned into
    /// user info (it does for an event whose message failed) included, so it is not sent again while it waits.
    private mutating func heldBySystem(_ key: Key, at now: Date) {
        sends[key] = Send(route: .queued, at: now, progress: .awaiting)
        retryAt[key] = nil
    }

    /// Transfers the last plan saw with the system and this one doesn't: the system is done with them, delivered as
    /// far as this can tell (a user info's result never comes, a file's may come after this pass). Each gets its
    /// `fileAckTimeout` for the phone's acknowledgement from now, not from when it was last seen, so a pass that comes
    /// late doesn't cut that wait short.
    private mutating func leftTheSystem(_ keys: Set<Key>, at now: Date) {
        for key in keys {
            guard let send = sends[key], send.route == .queued, send.progress == .awaiting else { continue }
            sends[key]?.progress = .delivered(now)
        }
    }

    /// Sends whose acknowledgement is overdue count as failed: a message `ackTimeout` after it went, a delivered
    /// transfer `fileAckTimeout` after the system said so or a pass found it gone, and a queued one never seen with the
    /// system `fileAckTimeout` after it went. One the system still holds is never overdue.
    private mutating func expire(now: Date, withSystem: Set<Key>) {
        for (key, send) in sends {
            let deadline: Date? = switch (send.route, send.progress) {
            case (.message, .awaiting): send.at.addingTimeInterval(RecordingDayContract.ackTimeout)
            case (.queued, .delivered(let at)): at.addingTimeInterval(RecordingDayContract.fileAckTimeout)
            case (.queued, .awaiting):
                withSystem.contains(key) ? nil : send.at.addingTimeInterval(RecordingDayContract.fileAckTimeout)
            default: nil
            }
            guard let deadline, now >= deadline else { continue }
            fail(key, at: deadline)
            if send.route == .message, !key.isEvent { chunkMessageFailed(at: deadline) }
        }
    }

    private mutating func fail(_ key: Key, at time: Date) {
        let count = (failures[key] ?? 0) + 1
        failures[key] = count
        retryAt[key] = time.addingTimeInterval(Self.backoff(count))
        sends[key]?.progress = .failed
    }

    private mutating func forget(_ key: Key) {
        sends[key] = nil
        failures[key] = nil
        retryAt[key] = nil
    }
}

// MARK: - The watch's records on the wire

/// What the watch sends for its day recordings, decided apart from WatchConnectivity so it is tested with the planner:
/// the items, the envelope sizes, when a day left open ends, when an unacknowledged chunk is too old to keep, when a
/// finished day can go, and the state the phone's Today row shows.
nonisolated extension RecordingDayOutbox {
    /// The live envelope's length prefix (`RecordingDayHeader.frame`).
    static let prefixBytes = 4
    /// What a header adds to an envelope before it was encoded: a few hundred bytes, with room to spare.
    static let headerAllowanceBytes = 512
    /// An m4a file's boxes around the AAC frames once the writer dropped its padding (`RecordingM4A`), a little over
    /// what its files carry.
    private static let containerOverheadBytes = 2_048
    /// What each second adds beyond the bit rate: the packet tables, and an encoder running a little over its rate
    /// (10 s of speech came to 42.4 KB in all).
    private static let perSecondOverheadBytes = 128.0

    /// The item for a chunk the phone has not acknowledged. `frameBytes`: its envelope's size (`frameBytes(audio:header:)`
    /// once its file was looked at, else `estimatedFrameBytes`). nil for a record no header can describe (no wall-clock
    /// start, or a length the phone refuses): it can never go.
    static func item(_ record: RecordingSegment, of recording: UUID, frameBytes: Int) -> Item? {
        guard let startedAt = record.startedAt, record.duration.isFinite, (0...600).contains(record.duration) else { return nil }
        return Item(key: .chunk(recording, seq: record.index), startedAt: startedAt,
                    finishedAt: startedAt.addingTimeInterval(record.duration), frameBytes: frameBytes)
    }

    /// The item for an event the phone has not acknowledged, placed at its recording's start so a recording's events
    /// keep their `n` order.
    static func item(_ envelope: RecordingDayEventEnvelope) -> Item {
        let event = envelope.event
        return Item(key: .event(envelope.recording, n: event.n), startedAt: envelope.recordingStartedAt,
                    finishedAt: event.at ?? event.to ?? event.from ?? envelope.recordingStartedAt, frameBytes: 0)
    }

    /// A chunk's live envelope, exactly: the length prefix, the header's JSON, the audio file.
    static func frameBytes(audio: Int, header: RecordingDayHeader) -> Int {
        prefixBytes + header.json.count + max(0, audio)
    }

    /// A chunk's envelope before its file was looked at: AAC at the recording's bit rate, the file's container, the
    /// header.
    static func estimatedFrameBytes(seconds: Double) -> Int {
        let seconds = max(0, seconds.isFinite ? seconds : 0)
        let audio = seconds * (Double(RecordingAudioSpec.bitRate) / 8 + perSecondOverheadBytes)
        return Int(audio.rounded(.up)) + containerOverheadBytes + prefixBytes + headerAllowanceBytes
    }

    /// What a look at a chunk's audio file found: its envelope's size (exact, or estimated when the file is there but its
    /// size can't be read), or no file (the chunk can never go).
    nonisolated enum Measured: Sendable, Equatable {
        case bytes(Int)
        case missing
    }

    /// Looks a chunk's audio file up (docs/apple/performance.md P1: never on the main actor). The transport does it off
    /// main once per chunk, and again after a transfer of it failed or its audio could not be read.
    static func measure(audio url: URL, header: RecordingDayHeader) -> Measured {
        let path = url.path
        if let size = (try? FileManager.default.attributesOfItem(atPath: path))?[.size] as? Int {
            return .bytes(frameBytes(audio: size, header: header))
        }
        return FileManager.default.fileExists(atPath: path) ? .bytes(estimatedFrameBytes(seconds: header.duration)) : .missing
    }

    /// Whether a day recording can go to the phone at all: one this build wrote always can (a valid `day` and `tz`).
    static func isSendable(_ entry: RecordingEntry) -> Bool {
        guard entry.isDay, let day = entry.day, let tz = entry.timeZone else { return false }
        return RecordingDayCalendar.isDay(day) && RecordingDayWire.zone(tz) != nil
    }

    /// When an open day recording ends, once its day is over: its own midnight, in its own time zone (`tz` is fixed for
    /// the recording, so a flight neither shortens nor stretches its day). nil while the day lasts, and for anything
    /// but an open day recording. Whether something still records into it is the caller's business: the capture cuts
    /// and closes that one itself.
    static func closingTime(of entry: RecordingEntry, now: Date) -> Date? {
        guard entry.isDay, entry.closedAt == nil, let day = entry.day,
              let midnight = RecordingDayCalendar.end(of: day, in: zone(of: entry)), midnight <= now else { return nil }
        return midnight
    }

    /// When the watch stops keeping a day recording's unacknowledged chunks (docs/assistant/day-recap.md「Retention
    /// and privacy」): `keepDays` local days after its `day` ended, at that midnight in its time zone, so a 23 or 25 hour
    /// day still counts as one. `keepDays` is the phone's `recordingDayKeepDays`, read within 1…7. nil without a valid
    /// day.
    static func ageLimit(of entry: RecordingEntry, keepDays: Int) -> Date? {
        let zone = zone(of: entry)
        guard let day = entry.day, var limit = RecordingDayCalendar.end(of: day, in: zone) else { return nil }
        for _ in 0..<RecordingDayContract.keepDays(audioDays: keepDays) {
            limit = RecordingDayCalendar.nextMidnight(after: limit, in: zone)
        }
        return limit
    }

    /// How long a closed day recording stays on the watch at least, after it ended: "Erase the last 10 minutes" just
    /// after midnight reaches back into it, and only a day recording the watch still holds gets its own `erase` event
    /// (docs/assistant/day-recap.md「Erasing」: an erase that spans midnight applies to both day recordings). The erase
    /// window and a minute more, for the clock.
    static let keepClosedSeconds = RecordingRolling.eraseMinutes * 60 + 60

    /// A closed day recording the phone has all of (every seq acknowledged, erased or lost, every event acknowledged)
    /// can go from the watch once an erase on the watch can no longer reach it (`keepClosedSeconds` after it ended), and
    /// unless the phone may still ask again about an erase it asked for: the watch answers a repeated request with the
    /// seqs it saved with the day (`RecordingDayContract.eraseMemoryDays`).
    static func isRemovable(_ entry: RecordingEntry, now: Date) -> Bool {
        guard entry.isDaySettled, let closedAt = entry.closedAt,
              now >= closedAt.addingTimeInterval(keepClosedSeconds) else { return false }
        let forgotten = now.addingTimeInterval(-RecordingDayContract.eraseMemoryDays * 86_400)
        return (entry.dayErases ?? []).allSatisfy { $0.at <= forgotten }
    }

    /// Whether a pass hands everything to the system rather than sending live (`plan`'s `suspending`): a hand-off was
    /// asked for or made a moment ago (`handOffUntil`: a pause's last chunk reaches the library a moment after the
    /// pause), or the app may be suspended any moment, being neither in front (`appActive`) nor recording (the
    /// microphone keeps a recording app running). A message's reply dies with a suspended app, and chunks taken back
    /// from the system to go live would wait for its next run.
    static func suspends(handingOff: Bool, handOffUntil: Date, now: Date, appActive: Bool, recording: Bool) -> Bool {
        handingOff || now < handOffUntil || !(appActive || recording)
    }

    /// Whether any of these day recordings still holds something the phone hasn't acknowledged: a chunk or an event.
    static func holdsUnacknowledged(_ days: [RecordingEntry]) -> Bool {
        days.contains { $0.isDay && (!$0.segments.isEmpty || !($0.dayEvents ?? []).isEmpty) }
    }

    /// The state the phone's Today row shows. `allDay`: day mode runs (recording or paused); `recording`: the microphone
    /// is on; `interrupted`: the system paused it; `dayOpen`: a day recording is open; `intent`: the user still wants
    /// all-day recording on, so an open day without the microphone stopped by itself (the system or a crash) rather
    /// than by the user.
    static func watchState(allDay: Bool, recording: Bool, interrupted: Bool, dayOpen: Bool, intent: Bool) -> RecordingDayWatchState.State {
        if allDay { return recording ? .recording : interrupted ? .interrupted : .paused }
        return dayOpen && intent ? .stopped : .off
    }

    private static func zone(of entry: RecordingEntry) -> TimeZone {
        entry.dayTimeZone ?? .autoupdatingCurrent
    }
}

// MARK: - What the watch keeps

/// What the watch does to its day recordings on the phone's word and with time (docs/assistant/day-recap.md
/// 「Acknowledgements and retries」and「Retention and privacy」): applying acknowledgements, ending days whose midnight
/// passed, the age limit, and removing what the phone has all of. (The phone's erase and delete requests are the
/// capture's: it may have a chunk of that recording open.) Apart from WatchConnectivity and the capture, so it is tested
/// against a temporary library; `WatchDayTransfer` runs it on `RecordingLibrary.shared`.
@MainActor
struct RecordingDayWatchLedger {
    let library: RecordingLibrary
    /// The day recording the capture's microphone records into (or is opening): its writer cuts at its midnight and the
    /// capture ends it, so nothing here does.
    var recordingInto: UUID? = nil
    /// The recording the capture holds, recording or paused in it: nothing here removes it from under the capture.
    var holding: UUID? = nil

    /// The watch's day recordings.
    var days: [RecordingEntry] { library.entries.filter { $0.isDay && $0.source == .watch } }

    /// An acknowledgement: the chunks' audio and records go at once (their seqs tombstoned, never used again) and the
    /// events are forgotten. Returns what the watch still held of it; a recording it doesn't have is ignored.
    @discardableResult func apply(_ ack: RecordingDayAck) -> (seqs: [Int], events: [Int]) {
        guard let entry = dayEntry(ack.recording) else { return ([], []) }
        let seqs = library.ackDayChunks(entry.id, seqs: ack.seqs)
        let held = Set((entry.dayEvents ?? []).map(\.n))
        let events = ack.eventNumbers.filter(held.contains)
        library.ackDayEvents(entry.id, ns: events)
        return (seqs, events)
    }

    /// Ends the day recordings whose own midnight has passed and that the microphone doesn't record into: a Stop in the
    /// evening, a pause past midnight or an app that didn't run must not leave the day open until the next start, so
    /// the phone and the hub learn it is over. Each gets its `closed` event at that midnight. Returns the ids closed.
    @discardableResult func closeStaleDays(now: Date) -> [UUID] {
        days.compactMap { entry in
            guard entry.id != recordingInto, let midnight = RecordingDayOutbox.closingTime(of: entry, now: now),
                  library.closeDay(entry.id, at: midnight) != nil else { return nil }
            return entry.id
        }
    }

    /// The watch's only age-based loss: chunks the phone still hasn't acknowledged `keepDays` after their day are deleted
    /// and reported as `lost` events. Returns those events.
    @discardableResult func ageOut(now: Date, keepDays: Int) -> [RecordingDayEvent] {
        days.flatMap { entry -> [RecordingDayEvent] in
            guard !entry.segments.isEmpty, let limit = RecordingDayOutbox.ageLimit(of: entry, keepDays: keepDays),
                  now >= limit else { return [] }
            return library.loseDayChunks(entry.id, seqs: entry.segments.map(\.index))
        }
    }

    /// Removes the closed day recordings the phone has all of (`RecordingDayOutbox.isRemovable`), their tombstones
    /// keeping a late save from bringing them back. Returns the ids removed.
    @discardableResult func removeSettled(now: Date) -> [UUID] {
        days.compactMap { entry in
            guard entry.id != holding, RecordingDayOutbox.isRemovable(entry, now: now),
                  (try? library.remove(entry.id)) != nil else { return nil }
            return entry.id
        }
    }

    private func dayEntry(_ id: UUID) -> RecordingEntry? {
        library.entry(id).flatMap { $0.isDay && $0.source == .watch ? $0 : nil }
    }
}
