#if os(watchOS)
import Foundation
import Observation
import WatchConnectivity
import WatchKit

/// Sends the watch's all-day chunks and control events to the phone and keeps each until the phone acknowledges it
/// (docs/assistant/day-recap.md「Watch to phone」). `RecordingDayOutbox` decides what goes and which way, and
/// `RecordingDayWatchLedger` what the watch keeps; this carries both out over WatchConnectivity and hands the phone's
/// erase and delete requests to the capture. Event driven (a library change, a reply, an acknowledgement, a file
/// transfer finishing, a reachability, Low Power or capability change, the app leaving the front) plus one timer for
/// the outbox's next deadline: a backoff or hold ending, a reply or an acknowledgement overdue, the live path due
/// another try, and, while an event waits with the system as user info, the next look whether it still does (the end of
/// a user info transfer doesn't reach this yet). Nothing polls otherwise.
@Observable
final class WatchDayTransfer {
    static let shared = WatchDayTransfer()

    /// Chunks the phone has not acknowledged yet, over every day recording.
    private(set) var pending = 0
    /// The session says the phone's app can be reached for live messages right now.
    private(set) var reachable = false

    private typealias Key = RecordingDayOutbox.Key

    /// A chunk or event about to go, with what its envelope needs.
    private nonisolated struct Work {
        nonisolated enum Payload {
            case chunk(RecordingSegment)
            case event(RecordingDayEventEnvelope)
        }

        var entry: RecordingEntry
        var payload: Payload
        var item: RecordingDayOutbox.Item
    }

    /// Reads of a chunk's audio that may fail before the chunk is reported lost: a file that can't be read is a loss
    /// the watch could not avoid, but one failed read is not.
    private static let maxReadFailures = RecordingDayContract.maxAttempts
    /// The phone hears how far the watch has got (`next`, `pending`) at most this often; a new state goes at once.
    private static let progressInterval: TimeInterval = 60
    /// After a hand-off, chunks saved this soon after still go the queued way: the pause's last chunk can reach the
    /// library a moment after the pause itself, from the writer's queue.
    private static let handOffWindow: TimeInterval = 5
    /// The wake timer never fires sooner than this, so times that round to now don't spin.
    private static let minimumWake: TimeInterval = 1

    @ObservationIgnored private var started = false
    @ObservationIgnored private var outbox = RecordingDayOutbox()
    /// Chunk messages being framed or awaiting their reply, by the send they belong to: a reply's error from an older
    /// send of the same chunk (handed off as a file since) must not count against the newer one.
    @ObservationIgnored private var inFlight: [Key: Int] = [:]
    @ObservationIgnored private var generation = 0
    /// Envelope sizes, measured once per chunk from its file off the main actor (a lookup, not a read), exact once
    /// framed. A chunk is offered to the planner once its size is known.
    @ObservationIgnored private var frameBytes: [Key: Int] = [:]
    /// Chunks whose file is being looked up off the main actor (`measureOffMain`).
    @ObservationIgnored private var measuring: Set<Key> = []
    /// The chunk each outstanding file transfer is of, by its metadata's header: a long backlog with the system is
    /// decoded once, not on every pass.
    @ObservationIgnored private var transferKeys: [Data: Key] = [:]
    @ObservationIgnored private var readFailures: [Key: Int] = [:]
    @ObservationIgnored private var handOffUntil = Date.distantPast
    @ObservationIgnored private var pumpScheduled = false
    @ObservationIgnored private var wake: Task<Void, Never>?
    @ObservationIgnored private var published: RecordingDayWatchState?
    @ObservationIgnored private var publishedAt = Date.distantPast
    @ObservationIgnored private var stateSince: (state: RecordingDayWatchState.State, since: Date)?
    @ObservationIgnored private var observers: [NSObjectProtocol] = []

    private init() {}

    /// Idempotent; `WatchRecordingTransfer.start()` calls it at launch, before WatchConnectivity activates, so a request
    /// the phone queued finds its handler. The day slots of `WatchSync` it takes are this transfer's own; the recording
    /// slots stay with `WatchRecordingTransfer`, which fans their events out to `pump()`.
    func start() {
        guard !started else { return }
        started = true
        let sync = WatchSync.shared
        sync.onDayAck = { [weak self] ack in self?.received(ack) }
        sync.onDayEraseRequest = { [weak self] request in self?.eraseRequested(request) }
        sync.onDayDelete = { [weak self] delete in self?.deleteRequested(delete) }
        // The phone's consent or keep days changed: sending may start (or stop), the age limit moves.
        sync.onDayCapabilities = { [weak self] in self?.pump() }
        let center = NotificationCenter.default
        // Posted on a global queue; delivered on main here.
        observers.append(center.addObserver(forName: .NSProcessInfoPowerStateDidChange, object: nil, queue: .main) { @Sendable _ in
            MainActor.assumeIsolated { WatchDayTransfer.shared.pump() }
        })
        // Out of the front (the wrist lowered, the Crown pressed): an app that doesn't record may be suspended any moment,
        // and nothing else hands its backlog to the system first.
        for name in [WKApplication.willResignActiveNotification, WKApplication.didEnterBackgroundNotification] {
            observers.append(center.addObserver(forName: name, object: nil, queue: .main) { @Sendable _ in
                MainActor.assumeIsolated { WatchDayTransfer.shared.leftTheFront() }
            })
        }
        observeLibrary()
        pump()
    }

    /// Plans and sends what is due. Coalesced to the end of the current main-actor turn, so a burst of events (a chunk
    /// saved, then the erase that follows it in the same tap) makes one pass that sees their outcome.
    func pump() {
        guard !pumpScheduled else { return }
        pumpScheduled = true
        Task { [weak self] in
            guard let self else { return }
            self.pumpScheduled = false
            self.run(handingOff: false)
        }
    }

    /// The app is about to stop running (a pause, a stop, a halt, a background refresh ending, leaving the front while
    /// nothing records): everything unacknowledged goes to the system now, chunks as files and events as user info,
    /// messages still awaiting their reply included: a message dies with the app, a queued transfer reaches the phone
    /// anyway. The phone drops what it already holds. Every later pass of an app that neither is in front nor records
    /// does the same (`RecordingDayOutbox.suspends`).
    func handOff() {
        handOffUntil = Date.now.addingTimeInterval(Self.handOffWindow)
        run(handingOff: true)
    }

    /// Ends the open day recordings whose own midnight has passed and that the microphone doesn't record into, a day
    /// all-day recording is paused in included (`RecordingDayWatchLedger.closeStaleDays`), so the phone and the hub learn
    /// the day is over without waiting for the next start. Run from the background refresh after midnight, once the
    /// session is activated, and in every pass of an activated session. The app may not run again soon, so whatever the
    /// phone hasn't acknowledged is handed off now: the `closed` events of this call, or of a pass that ran first after
    /// the wake. Only a recording app, which keeps running, goes on as it was.
    func closeStaleDays() {
        currentLedger().closeStaleDays(now: .now)
        if Self.keepsRunning || !RecordingDayOutbox.holdsUnacknowledged(Self.dayEntries()) {
            pump()
        } else {
            handOff()
        }
    }

    /// The app left the front. One that records keeps running, and its live path with it; any other hands off.
    private func leftTheFront() {
        guard !Self.recording else { return }
        handOff()
    }

    /// The all-day state, into the watch's application context for the phone's Today row. A new state goes at once;
    /// progress alone (`next`, `pending`) at most every `progressInterval`.
    func publishState() {
        guard Self.phoneTakesDays, WCSession.isSupported(), WCSession.default.activationState == .activated else { return }
        let now = Date.now
        let state = currentState(now: now)
        if let last = published {
            let sameState = last.state == state.state && last.recording == state.recording && last.day == state.day
                && last.since == state.since
            if sameState, last == state || now.timeIntervalSince(publishedAt) < Self.progressInterval { return }
        }
        published = state
        publishedAt = now
        WatchSync.shared.publishDayWatchState(state)
    }

    /// The system finished a chunk's file transfer (`WatchSync`).
    func finished(_ header: RecordingDayHeader, failed: Bool) {
        let key = Key.chunk(header.recording, seq: header.seq)
        // Acknowledged, erased or lost since: nothing waits for it.
        guard isHeld(key) else { return }
        outbox.transferFinished(key, failed: failed, at: .now)
        // Looked up again: audio that is gone makes the chunk lost.
        if failed { measureAgain(key) }
        // A failure goes again after its backoff; a delivery waits for the phone's acknowledgement.
        pump()
    }

    /// The system finished an event's user info transfer. A failure goes again after its backoff; a delivery gets
    /// `fileAckTimeout` for the phone's acknowledgement. Without this report the outbox still finds the transfer gone
    /// at its next look (`RecordingDayOutbox.userInfoCheckSeconds`), only later.
    func finished(_ envelope: RecordingDayEventEnvelope, failed: Bool) {
        let key = Key.event(envelope.recording, n: envelope.event.n)
        // Acknowledged since: nothing waits for it.
        guard isHeld(key) else { return }
        outbox.transferFinished(key, failed: failed, at: .now)
        pump()
    }

    /// An acknowledgement that came by message, user info or as the reply to an event's message (`WatchSync`).
    func received(_ ack: RecordingDayAck) {
        apply(ack)
        pump()
    }

    // MARK: - A pass

    private func run(handingOff: Bool) {
        let now = Date.now
        let session = WCSession.default
        refreshReachability(session)
        let ledger = currentLedger()
        let activated = WCSession.isSupported() && session.activationState == .activated
        // Before the gate: days end, age out and go even while the phone doesn't take days yet. A day ends only once
        // the session is up, so a pass that can send its `closed` follows at once: a background refresh that woke the
        // app for it waits for the activation, and hands off what it finds (`closeStaleDays`).
        if activated { ledger.closeStaleDays(now: now) }
        ledger.ageOut(now: now, keepDays: WatchPhoneCapabilities.keepDays())
        ledger.removeSettled(now: now)
        guard Self.phoneTakesDays, activated, session.isCompanionAppInstalled else {
            refreshPending()
            cancelWake()
            return
        }
        let (work, unmeasured) = waitingWork()
        // A chunk still being looked up waits outside the plan, but it is held: a transfer the system already has of it
        // stays (a pass that ends before the lookup answers mustn't leave it with neither a transfer nor a message).
        let held = Set(work.keys).union(unmeasured)
        let files = outstandingFiles(session, held: held)
        let queued = queuedEvents(session, waiting: work)
        keepOnly(held)
        let suspending = RecordingDayOutbox.suspends(handingOff: handingOff, handOffUntil: handOffUntil, now: now,
                                                     appActive: Self.appActive, recording: Self.recording)
        let actions = outbox.plan(work.values.map(\.item), now: now, reachable: reachable,
                                  lowPower: ProcessInfo.processInfo.isLowPowerModeEnabled,
                                  suspending: suspending, inFlight: Set(inFlight.keys),
                                  withSystem: Set(files.keys).union(queued))
        for action in actions {
            guard let item = work[action.key] else { continue }
            perform(action, item, transfer: files[action.key], session: session, now: now)
        }
        refreshPending()
        publishState()
        scheduleWake(after: now)
    }

    /// Everything of the watch's day recordings the phone has not acknowledged: each recording's events, then its
    /// chunks. A chunk that can never go (no header describes it, or its audio is gone: `measured`) becomes a `lost`
    /// event, so the phone stops waiting for its seq. A chunk whose file hasn't been looked up yet waits for that lookup,
    /// made off the main actor (`measureOffMain`), which asks for the next pass; those come back as `unmeasured`.
    private func waitingWork() -> (work: [Key: Work], unmeasured: Set<Key>) {
        var work: [Key: Work] = [:]
        var lost: [UUID: [Int]] = [:]
        var lookups: [Lookup] = []
        let library = RecordingLibrary.shared
        for entry in Self.dayEntries() where RecordingDayOutbox.isSendable(entry) {
            for event in entry.dayEvents ?? [] {
                guard let envelope = RecordingDayEventEnvelope(entry: entry, event: event) else { continue }
                let item = RecordingDayOutbox.item(envelope)
                work[item.key] = Work(entry: entry, payload: .event(envelope), item: item)
            }
            for record in entry.segments {
                let key = Key.chunk(entry.id, seq: record.index)
                guard let header = RecordingDayHeader(entry: entry, chunk: record),
                      let probe = RecordingDayOutbox.item(record, of: entry.id, frameBytes: 0) else {
                    lost[entry.id, default: []].append(record.index)
                    continue
                }
                guard let bytes = frameBytes[key] else {
                    lookups.append(Lookup(key: key, url: library.audio(entry.id, record.index), header: header))
                    continue
                }
                var item = probe
                item.frameBytes = bytes
                work[key] = Work(entry: entry, payload: .chunk(record), item: item)
            }
        }
        for (id, seqs) in lost { loseChunks(id, seqs: seqs) }
        measureOffMain(lookups)
        return (work, Set(lookups.map(\.key)))
    }

    /// A chunk's audio file to look up, with what its envelope's size needs.
    private nonisolated struct Lookup: Sendable {
        var key: Key
        var url: URL
        var header: RecordingDayHeader
    }

    /// Looks these chunks' files up off the main actor (docs/apple/performance.md P1), all in one task; the next pass
    /// follows the answer. A chunk already being looked up is not looked up twice.
    private func measureOffMain(_ lookups: [Lookup]) {
        let fresh = lookups.filter { !measuring.contains($0.key) }
        guard !fresh.isEmpty else { return }
        measuring.formUnion(fresh.map(\.key))
        Task.detached(priority: .utility) { [weak self] in
            let results = fresh.map { ($0.key, RecordingDayOutbox.measure(audio: $0.url, header: $0.header)) }
            await self?.measured(results)
        }
    }

    /// The lookups' answers: each chunk the watch still holds gets its envelope's size, or, its audio gone, becomes lost.
    private func measured(_ results: [(Key, RecordingDayOutbox.Measured)]) {
        var gone: [UUID: [Int]] = [:]
        for (key, result) in results {
            measuring.remove(key)
            guard isHeld(key) else { continue }
            switch result {
            case .bytes(let bytes): frameBytes[key] = bytes
            case .missing: gone[key.recording, default: []].append(key.number)
            }
        }
        for (id, seqs) in gone { loseChunks(id, seqs: seqs) }
        pump()
    }

    /// Looks a chunk's file up again (a transfer of it failed, its audio could not be read), keeping the size known
    /// meanwhile, so the chunk keeps its place and its backoff.
    private func measureAgain(_ key: Key) {
        let library = RecordingLibrary.shared
        guard let entry = library.entry(key.recording), let record = entry.segments.first(where: { $0.index == key.number }),
              let header = RecordingDayHeader(entry: entry, chunk: record) else { return }
        measureOffMain([Lookup(key: key, url: library.audio(entry.id, record.index), header: header)])
    }

    /// The held chunks the system still holds as a file transfer, one each. A transfer of audio nothing waits for
    /// any more (acknowledged, erased, lost, of a deleted recording) is cancelled, so erased audio doesn't reach the
    /// phone, and so is a second transfer of the same chunk. A chunk whose file is still being looked up keeps its
    /// transfer. Each transfer's header is decoded once (`transferKeys`).
    private func outstandingFiles(_ session: WCSession, held: Set<Key>) -> [Key: WCSessionFileTransfer] {
        var files: [Key: WCSessionFileTransfer] = [:]
        var seen: [Data: Key] = [:]
        for transfer in session.outstandingFileTransfers {
            let metadata = transfer.file.metadata
            guard let json = metadata?[RecordingDayContract.chunkKey] as? Data,
                  let key = transferKeys[json] ?? RecordingDayHeader(userInfo: metadata).map({ Key.chunk($0.recording, seq: $0.seq) })
            else { continue }
            seen[json] = key
            guard held.contains(key), files[key] == nil else {
                transfer.cancel()
                continue
            }
            files[key] = transfer
        }
        transferKeys = seen
        return files
    }

    /// The waiting events the system still holds as user info: sent so, or turned into user info by `WatchSync` when
    /// their message failed. One the phone acknowledged meanwhile is cancelled.
    private func queuedEvents(_ session: WCSession, waiting: [Key: Work]) -> Set<Key> {
        var keys = Set<Key>()
        for transfer in session.outstandingUserInfoTransfers {
            guard let data = transfer.userInfo[RecordingDayContract.eventKey] as? Data,
                  let envelope = RecordingDayEventEnvelope(data: data) else { continue }
            let key = Key.event(envelope.recording, n: envelope.event.n)
            guard waiting[key] != nil else {
                transfer.cancel()
                continue
            }
            keys.insert(key)
        }
        return keys
    }

    // MARK: - Sending

    private func perform(_ action: RecordingDayOutbox.Action, _ work: Work, transfer: WCSessionFileTransfer?,
                         session: WCSession, now: Date) {
        let key = action.key
        switch (action, work.payload) {
        case (.cancel, _):
            transfer?.cancel()
            return
        case (.message, .chunk(let record)), (.file, .chunk(let record)):
            guard let header = RecordingDayHeader(entry: work.entry, chunk: record) else {
                loseChunks(key.recording, seqs: [key.number])
                return
            }
            let url = RecordingLibrary.shared.audio(work.entry.id, record.index)
            // A message still out for this chunk is overtaken: its reply's acknowledgement still counts, its error doesn't.
            inFlight[key] = nil
            outbox.sent(action, at: now)
            if case .message = action { sendMessage(key, header: header, audio: url) } else {
                session.transferFile(url, metadata: header.userInfo)
            }
        case (.message, .event(let envelope)):
            guard WatchSync.shared.sendDayEvent(envelope) else { return }
            outbox.sent(action, at: now)
        case (.userInfo, .event(let envelope)):
            guard WatchSync.shared.sendDayEvent(envelope, queued: true) else { return }
            outbox.sent(action, at: now)
        case (.file, .event), (.userInfo, .chunk):
            // The planner never pairs these: events have no file, chunks no user info.
            return
        }
    }

    /// The live path: the envelope is the header and the audio file's bytes, read off main.
    private func sendMessage(_ key: Key, header: RecordingDayHeader, audio url: URL) {
        generation += 1
        let sending = generation
        inFlight[key] = sending
        Task.detached(priority: .utility) { [weak self] in
            let frame = WatchDayTransfer.frame(url: url, header: header)
            await self?.framed(frame, key: key, generation: sending)
        }
    }

    /// A message's payload: the header, then the chunk's audio. nil when the audio can't be read.
    nonisolated static func frame(url: URL, header: RecordingDayHeader) -> Data? {
        guard let audio = try? Data(contentsOf: url) else { return nil }
        return header.frame(audio: audio)
    }

    private func framed(_ frame: Data?, key: Key, generation sending: Int) {
        guard inFlight[key] == sending else { return }
        guard isHeld(key) else {
            // Acknowledged, erased or lost while its audio was read: nothing to send.
            inFlight[key] = nil
            outbox.reset(key)
            pump()
            return
        }
        guard let frame else {
            inFlight[key] = nil
            unreadable(key)
            pump()
            return
        }
        guard RecordingDayOutbox.fitsMessage(frame.count) else {
            // Not under the live limit: with its exact size known, the next pass sends it as a file.
            inFlight[key] = nil
            frameBytes[key] = frame.count
            outbox.reset(key)
            pump()
            return
        }
        deliver(frame, key: key, generation: sending)
    }

    private func deliver(_ frame: Data, key: Key, generation sending: Int) {
        WCSession.default.sendMessageData(frame, replyHandler: { @Sendable reply in
            Task { @MainActor in WatchDayTransfer.shared.replied(reply, key: key, generation: sending) }
        }, errorHandler: { @Sendable _ in
            Task { @MainActor in WatchDayTransfer.shared.failed(key: key, generation: sending) }
        })
    }

    /// The phone answered: the live path works. Its acknowledgement may name other chunks and events too; one that
    /// doesn't name this chunk (the phone couldn't store it) is a failure for it.
    private func replied(_ reply: Data, key: Key, generation sending: Int) {
        outbox.linkAnswered()
        refreshLinkReachable()
        let current = inFlight[key] == sending
        if current { inFlight[key] = nil }
        let ack = RecordingDayAck(data: reply)
        if let ack { apply(ack) }
        let named = ack.map { $0.recording == key.recording && $0.seqs.contains(key.number) } ?? false
        if current, !named { outbox.messageFailed(key, at: .now, answered: true) }
        pump()
    }

    private func failed(key: Key, generation sending: Int) {
        guard inFlight[key] == sending else { return }
        inFlight[key] = nil
        outbox.messageFailed(key, at: .now)
        refreshLinkReachable()
        pump()
    }

    /// A chunk's audio could not be read. Gone: the lookup it gets again finds that and reports it lost. Still there:
    /// tried again after a backoff, and reported lost after `maxReadFailures` tries.
    private func unreadable(_ key: Key) {
        measureAgain(key)
        let count = (readFailures[key] ?? 0) + 1
        readFailures[key] = count
        if count >= Self.maxReadFailures {
            loseChunks(key.recording, seqs: [key.number])
        } else {
            outbox.messageFailed(key, at: .now, answered: true)
        }
    }

    // MARK: - The phone's word

    /// Takes what the phone acknowledged off the watch (`RecordingDayWatchLedger.apply`), and a closed day it now has
    /// all of with it.
    private func apply(_ ack: RecordingDayAck) {
        let ledger = currentLedger()
        ledger.apply(ack)
        // Whether the watch still held them or not, nothing about these waits any more.
        forget(ack.seqs.map { Key.chunk(ack.recording, seq: $0) } + ack.eventNumbers.map { Key.event(ack.recording, n: $0) })
        ledger.removeSettled(now: .now)
    }

    /// The phone asks to erase a range of a day recording (docs/assistant/day-recap.md「Erasing」). The capture carries it
    /// out whatever it records: it registers what the writer finished first and closes the chunk being written when the
    /// range reaches into it, saves the request's id and seqs before deleting anything, and answers a repeated request
    /// the same way. nil leaves the request to the phone's resend; `WatchSync` sends the answer.
    private func eraseRequested(_ request: RecordingDayEraseRequest) -> RecordingDayErased? {
        let answer = RecordingCapture.shared.onDayEraseRequest(request)
        // Transfers of the erased chunks are cancelled, and what is left goes on, in the pass this asks for.
        pump()
        return answer
    }

    /// The phone deleted a day recording (docs/assistant/day-recap.md「Deleting」). The capture stops all-day recording when
    /// it records into it, and every chunk and the entry go with a tombstone; confirmed once nothing of it is left (also
    /// when the watch never had it). nil leaves it to the phone's resend; `WatchSync` sends the confirmation.
    private func deleteRequested(_ request: RecordingDayDelete) -> RecordingDayDeleted? {
        let answer = RecordingCapture.shared.onDayDelete(request)
        pump()
        return answer
    }

    // MARK: - Bookkeeping

    /// Nothing about these chunks and events is kept any more.
    private func forget(_ keys: [Key]) {
        outbox.acked(keys)
        for key in keys {
            inFlight[key] = nil
            frameBytes[key] = nil
            readFailures[key] = nil
        }
    }

    /// Drops what is kept about chunks that left without an acknowledgement reaching this transfer (erased, lost, their
    /// day deleted), so the per-chunk tables stay as small as the backlog.
    private func keepOnly(_ waiting: Set<Key>) {
        frameBytes = frameBytes.filter { waiting.contains($0.key) }
        readFailures = readFailures.filter { waiting.contains($0.key) }
    }

    /// Chunks that can never go become `lost` events, kept until the phone acknowledges them.
    private func loseChunks(_ id: UUID, seqs: [Int]) {
        RecordingLibrary.shared.loseDayChunks(id, seqs: seqs)
        forget(seqs.map { Key.chunk(id, seq: $0) })
    }

    /// The ledger over the shared library, with what the capture holds now. The microphone records into a recording
    /// while it records or is opening; the capture holds it while all-day recording is on in it, paused included.
    private func currentLedger() -> RecordingDayWatchLedger {
        let capture = RecordingCapture.shared
        let holding = capture.allDay || capture.state != .idle ? capture.recordingID : nil
        let live = capture.state == .recording || capture.state == .starting
        return RecordingDayWatchLedger(library: .shared, recordingInto: live ? holding : nil, holding: holding)
    }

    // MARK: - State

    /// The session's reachability, and with it whether the capture cuts short chunks for the live path (which also
    /// needs messages not to keep failing).
    private func refreshReachability(_ session: WCSession) {
        let now = WCSession.isSupported() && session.activationState == .activated && session.isReachable
        if now != reachable {
            reachable = now
            outbox.reachabilityChanged(now)
        }
        refreshLinkReachable()
    }

    private func refreshLinkReachable() {
        let live = outbox.liveUp(reachable: reachable, now: .now)
        let capture = RecordingCapture.shared
        if capture.linkReachable != live { capture.linkReachable = live }
    }

    private func refreshPending(_ count: Int? = nil) {
        let value = count ?? Self.dayEntries().reduce(0) { $0 + $1.segments.count }
        if pending != value { pending = value }
    }

    private func currentState(now: Date) -> RecordingDayWatchState {
        let capture = RecordingCapture.shared
        let library = RecordingLibrary.shared
        let entry = capture.dayRecordingID.flatMap { library.entry($0) } ?? library.openDays(source: .watch).first
        let state = RecordingDayOutbox.watchState(allDay: capture.allDay,
                                                  recording: capture.state == .recording || capture.state == .starting,
                                                  interrupted: capture.interrupted, dayOpen: entry?.closedAt == nil && entry != nil,
                                                  intent: UserDefaults.standard.bool(forKey: RecordingCapture.allDayIntentKey))
        if stateSince?.state != state { stateSince = (state, now) }
        return RecordingDayWatchState(recording: entry?.id, day: entry?.day, state: state,
                                      since: state == .off ? nil : stateSince?.since, next: entry?.nextIndex, pending: pending)
    }

    // MARK: - Wakes

    /// Every library change may be something to send (a chunk saved, an event added, an erase): a pass follows it.
    /// Observation tracks one change at a time, so it is armed again with each.
    private func observeLibrary() {
        withObservationTracking {
            _ = RecordingLibrary.shared.entries
        } onChange: { @Sendable in
            Task { @MainActor in
                WatchDayTransfer.shared.observeLibrary()
                WatchDayTransfer.shared.pump()
            }
        }
    }

    private func scheduleWake(after now: Date) {
        cancelWake()
        guard let at = outbox.nextWake(after: now) else { return }
        let delay = max(Self.minimumWake, at.timeIntervalSince(now))
        wake = Task { [weak self] in
            do { try await Task.sleep(for: .seconds(delay)) } catch { return }
            self?.pump()
        }
    }

    private func cancelWake() {
        wake?.cancel()
        wake = nil
    }

    // MARK: - Helpers

    /// Sending waits for a phone that takes all-day recordings and whose user agreed (`recordingDay` ≥ 1); everything
    /// stays until then.
    private static var phoneTakesDays: Bool { UserDefaults.standard.integer(forKey: WatchSync.phoneDayKey) >= 1 }

    /// The app is in front, where it keeps running.
    private static var appActive: Bool { WKApplication.shared().applicationState == .active }

    /// The microphone is on (or opening): the recording keeps the app running in the background too.
    private static var recording: Bool {
        let state = RecordingCapture.shared.state
        return state == .recording || state == .starting
    }

    /// The app keeps running for now, so the live path goes on: it is in front, or records.
    private static var keepsRunning: Bool { appActive || recording }

    private static func dayEntries() -> [RecordingEntry] {
        RecordingLibrary.shared.entries.filter { $0.isDay && $0.source == .watch }
    }

    /// The watch still holds this chunk (the phone hasn't acknowledged it, and it wasn't erased or lost) or this event
    /// (not acknowledged yet).
    private func isHeld(_ key: Key) -> Bool {
        guard let entry = RecordingLibrary.shared.entry(key.recording) else { return false }
        if key.isEvent { return (entry.dayEvents ?? []).contains { $0.n == key.number } }
        return entry.segments.contains { $0.index == key.number }
    }
}
#endif
