#if os(iOS)
import AVFoundation
import Foundation
import Observation
import OSLog
import UIKit

/// Hub transcription on the phone (#3862 phase 5; the contract is docs/assistant/meeting-minutes.md「Phase 5: hub
/// transcription」, the phone's side docs/apple/recording.md「Hub transcription (phase 5)」): the one store the meeting page
/// and the Live Activity read, per recording, and what keeps it.
///
/// - **The mode** a meeting asked for as it started (`RecordingAttendance.transcription`), and the mode in effect, which
///   is the upload `200`'s `transcription` (`MeetingTranscriptionRule.decide`): `hub` only when asked for; a `200` without
///   it is an older manager, and the meeting stays on the phone for good, its uplink stopped.
/// - **The uplink** while the phone records a meeting in hub mode: the recorder's tap into the last 60 seconds in memory
///   from the start (`MeetingAudioUplink`), and the audio socket once the mode is in: `start`, `ready`, the audio from
///   `ready.from`, `role` on every change, `speaking` as Persona starts and stops, `end` at Stop. It runs whatever the role,
///   `off` included; closes while paused and opens again on Continue; tries again after a drop (1 s … 30 s), at once when
///   the network comes back; with **Hub transcription on Wi-Fi only** and no Wi-Fi it opens none. A fatal `error` acts on
///   its code (`MeetingTranscriptionRule.reaction`): one the phone doesn't know stops hub transcription for the meeting
///   for good (`RecordingAttendance.hubStopped`, its hub-mode time ended there), and the phone's own goes on, as it
///   always does in hub mode.
/// - **What the hub says**: its caption (`interim`), its lines (`line` frames, the positions missed while away read with
///   `GET …/live?from=`), the standing `problem`, the gaps, and `transcription` as `GET …/live` and `detail.meta` have it
///   (`liveRead`); what the page shows of them (`shown`).
/// - **The gaps after the meeting**: `PUT …/audio?at=` decoded from the phone's segments until the hub has no gap left or
///   its final pass has started, under the same Wi-Fi rule, the recording read again before every piece.
@MainActor @Observable
final class MeetingTranscription {
    static let shared = MeetingTranscription(deps: .live)

    /// The audio socket of the recording being made (`MeetingAudioSocket`: off, waiting for the mode or for Wi-Fi,
    /// connecting, ready, down, paused, ending, ended).
    typealias Socket = MeetingAudioSocket

    /// The gap uploads after the meeting.
    nonisolated enum GapUpload: Equatable, Sendable {
        case idle
        /// **Hub transcription on Wi-Fi only**, and no Wi-Fi.
        case waitingForWiFi
        case uploading
        /// Nothing (more) to upload: no gap left, the final pass started, or the meeting isn't the hub's.
        case done
        /// Not now (the hub or the network away; its code when it said one): another pass comes after a while
        /// (`MeetingGapUpload.retryDelay`).
        case later(String?)
    }

    /// One meeting as this phone knows it.
    nonisolated struct Meeting: Equatable, Sendable {
        /// The mode the meeting asked for as it started; nil: the hub didn't list `meeting_transcription`.
        var asked: MeetingTranscriptionMode?
        /// The mode in effect, from the upload's `200` (nil until it came).
        var decided: MeetingTranscriptionRule.Decided?
        var socket: Socket = .off
        /// The socket was up (`ready`) at least once: a drop from then on is a fallback, the first connect isn't.
        var everUp = false
        /// A socket that was up has been away (down, or opening again) longer than `MeetingTranscriptionRule.reconnectGrace`:
        /// a brief reconnect shows no fallback.
        var away = false
        /// The hub's caption being said now.
        var interim: MeetingInterim?
        /// The live log's lines the phone has, by position (`n`): the hub's `line` frames, and what `?from=` read.
        var lines: [MeetingHubLine] = []
        /// The socket's standing problem (`upstream_unavailable`, `upstream_failed`, `no_interim`; an open set).
        var problem: String?
        /// Why the uplink stopped for this meeting (a fatal `error`'s code, or a refused socket's `http_<status>`); nil
        /// while it may run.
        var stopped: String?
        /// The hub stored the audio up to here (`ack`).
        var acked: Double?
        /// What the hub lacks, as it last said (`ready.gaps`, `PUT …/audio`'s answer, `transcription.gaps`).
        var gaps: [MeetingGap] = []
        /// Unacknowledged audio that fell out of the phone's 60 seconds (the phone's own account).
        var lost: [MeetingGap] = []
        /// `transcription` of `GET …/live` / `detail.meta`: the mode, the hub's state, its gaps, `problem` and `fallback`.
        var hub: MeetingTranscriptionView?
        var gapUpload: GapUpload = .idle

        /// The mode in effect; nil until the `200` said.
        var mode: MeetingTranscriptionMode? { decided?.mode }
        /// The hub transcribes this meeting: the live log is its (`POST …/live` is `409 hub_transcribes`).
        var hubMode: Bool { decided?.mode == .hub }
        var socketUp: Bool { socket == .ready }
    }

    /// A stretch Persona speaks in, by who says it: an answer (`MeetingSpeaker`) or the meeting call's voice.
    nonisolated enum Voice: Hashable, Sendable { case answer, call }

    struct Deps {
        var api: () -> APIClient?
        var socket: (URLRequest, APIClient) -> VoiceSocket
        var network: () -> MeetingNetworkKind
        /// The uplink's listener on the recorder's tap (`RecordingCapture.listenUplink`); nil takes it away.
        var listen: ((@Sendable (AVAudioPCMBuffer, Double) -> Void)?) -> Void
        /// The hub has no upload of the recording: it goes again now (`RecordingSync.reupload`).
        var reupload: (UUID) -> Void
        var wait: (Duration) async -> Void
        var store: UserDefaults = .standard
        /// The audio of `piece` from the recording's segments (off the main thread).
        var audio: (RecordingEntry, MeetingGapUpload.Piece) async -> (at: Double, data: Data)? = { _, _ in nil }
        /// Past the hub's retention (`RecordingSync.isExpired`): never uploaded, and its audio goes with it.
        var expired: (RecordingEntry) -> Bool = { _ in false }
        /// The hub has the recording's latest revision (`RecordingSync.status` `synced`): its `complete: true` upload too.
        var synced: (RecordingEntry) -> Bool = { _ in true }
        /// Where the recording `id` is now on its clock, while this phone records it (`RecordingCapture.timeline`); nil
        /// otherwise: where a rollback ends its hub-mode time.
        var position: (UUID) -> Double? = { _ in nil }
        /// The live log gained positions (a `line` frame, a `?from=` read): the room's topics are read behind the app,
        /// as after an accepted `POST …/live` (`MeetingConversation.liveAccepted`), so the Lock Screen's "Now:" follows.
        var linesAdded: (UUID) -> Void = { _ in }
        /// Background time for the gap passes (`beginBackgroundTask`), so the one right after Stop, and a retry after
        /// it, survive the app going behind; the closure returned ends it. The second argument runs (on the main actor)
        /// when the system takes the time back, which ends it by itself.
        var background: (String, @escaping @MainActor @Sendable () -> Void) -> (() -> Void) = { _, _ in {} }
        /// `PUT …/audio?at=` with `data` (`APIClient.putBytes`; `wifiOnly`: kept off cellular and expensive networks):
        /// run as a task of its own, so the Wi-Fi rule can stop it halfway.
        var put: (APIClient, String, [String: String], Data, Bool) async throws -> Data = { api, path, query, data, wifiOnly in
            try await api.putBytes(path, query: query, data: data, wifiOnly: wifiOnly)
        }
        /// When the hub's requests began to go through the tunnel they go through now (`HubTransport.since`); nil when
        /// they go straight to it.
        var tunnel: () -> Date? = { nil }
        /// The last moment the phone's path was on cellular or an expensive network (`MeetingNetworkState.meteredUntil`).
        var meteredUntil: () -> Date? = { nil }
    }

    private(set) var meetings: [UUID: Meeting] = [:]
    /// The meetings in hub mode (`Meeting.hubMode`), kept apart so a view that only asks whether the hub transcribes
    /// (the page's subtitle and Talk line, `MeetingLive`) doesn't redraw with every caption (P5).
    private(set) var hubMeetings: Set<UUID> = []
    /// The speech service a hub-mode meeting's audio goes to, by name, when the owner chose another extension's (the
    /// privacy texts name it): what the start or Settings › Meetings last read (`noteService`); nil for the built-in one.
    private(set) var service: String?
    /// **Hub transcription on Wi-Fi only** (`MeetingTranscriptionSettings.setWiFiOnly`).
    private(set) var wifiOnly: Bool
    /// The recording the uplink belongs to, or last belonged to: what `probe` shows.
    private(set) var current: UUID?

    @ObservationIgnored let deps: Deps
    @ObservationIgnored private let library: RecordingLibrary
    @ObservationIgnored private var uplink: MeetingAudioUplink?
    /// Sockets that failed in a row (the backoff) and `bad_frame`s in a row, counted across sockets until one is healthy
    /// (`MeetingTranscriptionRule.healthyAfter`): its first `ack`, or that long up.
    @ObservationIgnored private var failures = 0
    @ObservationIgnored private var badFrames = 0
    /// The socket open now proved healthy.
    @ObservationIgnored private var healthy = false
    @ObservationIgnored private var healthTask: Task<Void, Never>?
    @ObservationIgnored private var retryTask: Task<Void, Never>?
    @ObservationIgnored private var endTask: Task<Void, Never>?
    /// Per meeting, the wait before a socket that was up and is away counts as a fallback (`Meeting.away`).
    @ObservationIgnored private var awayTasks: [UUID: Task<Void, Never>] = [:]
    /// The role the socket was told last (`role` frames go on a change only).
    @ObservationIgnored private var sentRole: MeetingAttendance?
    /// The stretches Persona speaks in now, by voice: one `speaking` on while any is open, off once the last closes.
    @ObservationIgnored private var speakingOpen: [UUID: [Voice: RecordingSpan]] = [:]
    @ObservationIgnored private var readingLines: Set<UUID> = []
    @ObservationIgnored private var lastNetwork: MeetingNetworkKind?
    @ObservationIgnored private var gapTask: Task<Void, Never>?
    @ObservationIgnored private var gapsAgain = false
    /// The next gap pass after one that left a meeting `later`, and how many such passes in a row.
    @ObservationIgnored private var gapRetryTask: Task<Void, Never>?
    @ObservationIgnored private var gapRetries = 0
    /// Recordings with nothing (more) to upload in this run.
    @ObservationIgnored private var gapsDone: Set<UUID> = []
    /// The background time the gap passes run under, held from a pass across a retry's wait and the pass after it while
    /// the system grants it (`kickGaps`); its number, so a late expiry of one already ended changes nothing.
    @ObservationIgnored private var gapBackground: (() -> Void)?
    @ObservationIgnored private var gapBackgrounds = 0
    /// The gap `PUT` under way, for which recording, and whether this side stopped it (`stopGapPut`).
    @ObservationIgnored private var gapPut: (id: UUID, task: Task<Data, Error>)?
    @ObservationIgnored private var gapPutStopped = false
    /// What `follow` was last told: a retry follows it again, with the recording as the library has it then.
    @ObservationIgnored private var followed: (id: UUID?, paused: Bool) = (nil, false)
    @ObservationIgnored private var started = false
    private let log = Logger(subsystem: "persona", category: "meeting-transcription")

    init(deps: Deps, library: RecordingLibrary = .shared) {
        self.deps = deps
        self.library = library
        wifiOnly = deps.store.bool(forKey: MeetingAttendanceDefaults.wifiOnlyKey)
    }

    /// From `MeetingRecorder.start` at launch: hears every upload `200` that named a mode, follows the recording being
    /// made, the network and the app coming back, and uploads the gaps meetings left.
    func start() {
        guard !started else { return }
        started = true
        RecordingSync.shared.onTranscription = { [weak self] entry, answer in self?.uploaded(entry, answer: answer) }
        MeetingNetwork.shared.start()
        observe()
        NotificationCenter.default.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) { @Sendable _ in
            Task { @MainActor in MeetingTranscription.shared.kickGaps() }
        }
        Task { [weak self] in
            await self?.deps.wait(.seconds(3))
            self?.kickGaps()
        }
    }

    private func observe() {
        withObservationTracking { sync() } onChange: {
            Task { @MainActor in MeetingTranscription.shared.observe() }
        }
    }

    func meeting(_ id: UUID) -> Meeting { meetings[id] ?? Meeting() }

    /// The hub transcribes the meeting: no `POST …/live` (`MeetingLive`), and a heard ask sends no `live_next`.
    func hubTranscribes(_ id: UUID) -> Bool { hubMeetings.contains(id) }

    /// The speech service as the start or the settings read it (`MeetingTranscriptionSettings.service`).
    func noteService(_ name: String?) {
        if service != name { service = name }
    }

    /// What the page and the Live Activity show for `id`, whose role is `role` now.
    func shown(_ id: UUID, role: MeetingAttendance) -> MeetingTranscriptionRule.Shown {
        let m = meeting(id)
        return MeetingTranscriptionRule.shown(hub: m.hubMode, socket: m.socket, everUp: m.everUp, away: m.away,
                                              problem: m.problem, stopped: m.stopped != nil, role: role)
    }

    // MARK: The mode

    /// An upload of `entry` that named a mode was stored: its `200`'s `transcription` decides the mode in effect. A
    /// rollback (an older manager) is kept with the recording (`RecordingAttendance.rollBackHub`), so a relaunch never
    /// streams or uploads its audio again, and its hub-mode time ends where the recording is now.
    func uploaded(_ entry: RecordingEntry, answer: MeetingTranscriptionRule.Answer) {
        var m = meetings[entry.id] ?? Meeting()
        m.asked = entry.attendance?.transcription
        if entry.attendance?.hubRolledBack == true, m.decided?.olderManager != true {
            m.decided = MeetingTranscriptionRule.Decided(mode: .device, olderManager: true)
        }
        m.decided = MeetingTranscriptionRule.decide(m.decided, asked: m.asked, answer: answer)
        if m.decided?.olderManager == true, meetings[entry.id]?.decided?.olderManager != true {
            log.notice("transcription: an older manager answered; the meeting stays on the phone")
        }
        set(entry.id, m)
        if m.decided?.olderManager == true, let a = library.entry(entry.id)?.attendance, a.transcription == .hub, !a.hubRolledBack {
            let at = deps.position(entry.id) ?? library.entry(entry.id)?.duration ?? entry.duration
            library.change(entry.id) { $0.attendance?.rollBackHub(at: at) }
        }
        // A gap upload of it under way stops: the pass reads the recording again and ends there.
        if m.decided?.olderManager == true, gapPut?.id == entry.id { stopGapPut() }
        if !entry.active { kickGaps() }
    }

    // MARK: Following the recording

    /// Brings the uplink in step with the recording being made, its mode, the network and the Wi-Fi rule.
    private func sync() {
        let capture = RecordingCapture.shared
        let state = capture.state
        let id = state == .recording || state == .paused ? capture.recordingID : nil
        follow(id, paused: state == .paused, entry: id.flatMap { library.entry($0) })
        // Read here so a change of either brings the next pass.
        networkIs(deps.network())
    }

    /// The phone's network is `network` now (`sync`, as it changes): coming back brings a gap pass; off Wi-Fi with Wi-Fi
    /// only on, a gap upload under way stops (through a tunnel the system's own guard doesn't reach the connection that
    /// carries it), and the pass looks again under the rule. The socket follows in `follow`.
    func networkIs(_ network: MeetingNetworkKind) {
        guard lastNetwork != network else { return }
        let came = lastNetwork == MeetingNetworkKind.none || lastNetwork == .metered || lastNetwork == .unknown
        lastNetwork = network
        if wifiOnly, !mayStream(network) { stopGapPut() }
        if came { kickGaps() }
    }

    /// `id` records now (`paused`), as `entry`; nil: nothing does (a socket at its `end` is left to finish).
    func follow(_ id: UUID?, paused: Bool, entry: RecordingEntry?) {
        followed = (id, paused)
        // A rollback kept with the recording (`hubRolledBack`) is the older manager's word, also after a relaunch.
        guard let id, let entry, entry.active, entry.roomMeeting, let attendance = entry.attendance,
              attendance.transcription == .hub, !attendance.hubRolledBack else {
            if let uplink, meetings[uplink.recording]?.socket != .ending { detach(uplink.recording, to: .off) }
            return
        }
        var m = meetings[id] ?? Meeting()
        m.asked = .hub
        if current != id { current = id }
        // Transcribed on the phone (the hub said so, or an older manager), or the uplink stopped: no audio leaves it.
        if let decided = m.decided, decided.mode != .hub {
            if uplink?.recording == id { detach(id, to: .off) } else { set(id, m.with(socket: .off)) }
            return
        }
        // Hub transcription stopped for it (kept with the recording, so also after a relaunch): no socket, no audio.
        if m.stopped == nil, let stop = attendance.hubStopped { m.stopped = stop.code }
        if m.stopped != nil {
            set(id, m.with(socket: .off))
            if uplink?.recording == id { detach(id, to: .off) }
            return
        }
        // The tap's audio is kept from the start, whatever comes of the socket: what the hub never got goes up later.
        let uplink = attach(id)
        let network = deps.network()
        let target: Socket
        if m.decided == nil {
            target = .waitingForMode
        } else if paused {
            target = .paused
        } else if network == MeetingNetworkKind.none {
            target = .down
        } else if !mayStream(network) {
            // Checked right before every socket, a retry's included.
            target = .waitingForWiFi
        } else {
            target = .ready
        }
        switch target {
        case .ready:
            if [.connecting, .ready, .ending].contains(m.socket) {
                sendRole(attendance, uplink: uplink)
            } else if m.socket == .down, retryTask != nil, lastNetwork != MeetingNetworkKind.none {
                // A try is due later.
            } else {
                retryTask?.cancel()
                retryTask = nil
                connect(id, entry: entry, uplink: uplink, meeting: &m)
            }
        default:
            if [.connecting, .ready].contains(m.socket) { uplink.close() }
            m.socket = target
            if target != .down { retryTask?.cancel(); retryTask = nil }
        }
        m.lost = uplink.lost
        set(id, m)
    }

    /// The uplink of `id`, listening to the tap (another recording's goes first).
    private func attach(_ id: UUID) -> MeetingAudioUplink {
        if let uplink, uplink.recording == id { return uplink }
        if let other = uplink?.recording { detach(other, to: .off) }
        let fresh = MeetingAudioUplink(recording: id)
        uplink = fresh
        failures = 0
        badFrames = 0
        healthy = false
        sentRole = nil
        deps.listen { @Sendable buffer, at in fresh.feed(buffer, at: at) }
        return fresh
    }

    /// Closes `id`'s uplink and stops listening; the meeting's socket reads `state`.
    private func detach(_ id: UUID, to state: Socket) {
        retryTask?.cancel()
        retryTask = nil
        endTask?.cancel()
        endTask = nil
        healthTask?.cancel()
        healthTask = nil
        if let uplink, uplink.recording == id {
            deps.listen(nil)
            uplink.close()
            var m = meetings[id] ?? Meeting()
            m.lost = uplink.lost
            m.socket = state
            if state != .ready { m.interim = nil }
            set(id, m)
            self.uplink = nil
        } else if var m = meetings[id], m.socket != state {
            m.socket = state
            set(id, m)
        }
    }

    private func connect(_ id: UUID, entry: RecordingEntry, uplink: MeetingAudioUplink, meeting m: inout Meeting) {
        guard let api = deps.api(), let attendance = entry.attendance else {
            m.socket = .down
            scheduleRetry(id)
            return
        }
        let socket = deps.socket(MeetingAudioEndpoint.request(api, recording: id, wifiOnly: wifiOnly), api)
        healthy = false
        healthTask?.cancel()
        healthTask = nil
        let role = attendance.roles.last ?? RecordingRoleChange(at: 0, attendance: attendance.role)
        let open = speakingOpen[id]?.values.min { $0.start < $1.start }
        uplink.open(socket, timeline: MeetingTimeline(attendance), speaking: open, role: role) { [weak self] gen, event in
            self?.received(event, recording: id, generation: gen)
        }
        sentRole = role.attendance
        m.socket = .connecting
        log.info("transcription: audio socket opening")
    }

    private func sendRole(_ attendance: RecordingAttendance, uplink: MeetingAudioUplink) {
        guard sentRole != attendance.role else { return }
        sentRole = attendance.role
        let at = attendance.roles.last?.at ?? 0
        uplink.send(text: MeetingAudioOut.role(at: at, attendance: attendance.role))
    }

    private func scheduleRetry(_ id: UUID) {
        failures += 1
        let delay = MeetingTranscriptionRule.backoff(failures)
        retryTask?.cancel()
        let wait = deps.wait
        retryTask = Task { [weak self] in
            await wait(.milliseconds(Int(delay * 1000)))
            guard !Task.isCancelled, let self else { return }
            self.retryTask = nil
            let (id, paused) = self.followed
            self.follow(id, paused: paused, entry: id.flatMap { self.library.entry($0) })
        }
    }

    // MARK: The socket

    /// What came on the socket of `generation` (a socket closed or replaced since is ignored).
    func received(_ event: VoiceSocketEvent, recording id: UUID, generation gen: Int) {
        guard let uplink, uplink.recording == id, gen == uplink.currentGeneration else { return }
        switch event {
        case .text(let text): handle(MeetingAudioIn.decode(text), recording: id, uplink: uplink, generation: gen)
        case .data: break
        case .closed(let close): closed(id, status: close.status, uplink: uplink, generation: gen)
        }
    }

    private func handle(_ frame: MeetingAudioIn, recording id: UUID, uplink: MeetingAudioUplink, generation gen: Int) {
        var m = meetings[id] ?? Meeting()
        switch frame {
        case .ready(let from, let gaps, let next):
            uplink.ready(from: from, generation: gen)
            if m.socket != .ending { m.socket = .ready }
            m.everUp = true
            m.gaps = gaps
            // The standing problem comes right after, if any.
            m.problem = nil
            // Not healthy yet: the failures in a row start again only once it is (`healthyAfter`).
            awaitHealth(id, generation: gen)
            let known = Self.contiguous(m.lines)
            if next > known { Task { await readLines(id, from: known) } }
        case .ack(let at):
            uplink.ack(at)
            m.acked = at
            m.lost = uplink.lost
            provedHealthy()
        case .interim(let interim):
            m.interim = interim
        case .line(let line):
            let added = !m.lines.contains { $0.n == line.n }
            m.lines = Self.merged(m.lines, [line])
            // The caption that line finishes is said.
            if let interim = m.interim, interim.start <= line.end { m.interim = nil }
            if added { deps.linesAdded(id) }
        case .problem(let code):
            m.problem = code
            if code != nil, code != "no_interim" { m.interim = nil }
        case .error(let code):
            log.notice("transcription: the hub ended the audio socket: \(code, privacy: .public)")
            switch MeetingTranscriptionRule.reaction(toError: code) {
            case .stop:
                stopHub(id, code: code.isEmpty ? "unknown" : code, meeting: &m)
                return
            case .yield:
                // Another socket has the meeting: none from here while this run lasts, and hub-mode time goes on.
                m.stopped = code
                set(id, m)
                detach(id, to: .off)
                return
            case .uploadFirst:
                deps.reupload(id)
                uplink.close()
                m.socket = .down
                m.interim = nil
                set(id, m)
                scheduleRetry(id)
                return
            case .retry:
                if code == "bad_frame" { badFrames += 1 }
                if badFrames >= MeetingTranscriptionRule.maxBadFrames {
                    stopHub(id, code: code, meeting: &m)
                    return
                }
                uplink.close()
                m.socket = .down
                m.interim = nil
                set(id, m)
                scheduleRetry(id)
                return
            }
        case .other:
            return
        }
        set(id, m)
    }

    /// Hub transcription stopped for `id` (`code`): the uplink comes off the tap at once, and hub-mode time ends where the
    /// recording is now, for good (`RecordingAttendance.stopHub`, kept with it): no socket opens for it again, also after
    /// a relaunch, and no audio recorded from here goes up, live or as a gap. The phone's own transcription goes on.
    private func stopHub(_ id: UUID, code: String, meeting m: inout Meeting) {
        m.stopped = code
        set(id, m)
        detach(id, to: .off)
        let at = deps.position(id) ?? library.entry(id)?.duration ?? 0
        if library.entry(id)?.attendance?.hubStopped == nil {
            library.change(id) { $0.attendance?.stopHub(at: at, code: code) }
        }
        log.notice("transcription: hub transcription stopped for this meeting (\(code, privacy: .public))")
    }

    /// The socket of `gen` said `ready`: it counts as healthy once it stayed up `healthyAfter` (or at its first `ack`).
    private func awaitHealth(_ id: UUID, generation gen: Int) {
        healthTask?.cancel()
        let wait = deps.wait
        healthTask = Task { [weak self] in
            await wait(MeetingTranscriptionRule.healthyAfter)
            guard !Task.isCancelled, let self, let uplink = self.uplink, uplink.recording == id, uplink.currentGeneration == gen,
                  self.meetings[id]?.socket == .ready else { return }
            self.provedHealthy()
        }
    }

    /// The socket open now is healthy: the backoff and the `bad_frame`s start again from none.
    private func provedHealthy() {
        guard !healthy else { return }
        healthy = true
        failures = 0
        badFrames = 0
        healthTask?.cancel()
        healthTask = nil
    }

    private func closed(_ id: UUID, status: Int?, uplink: MeetingAudioUplink, generation gen: Int) {
        uplink.dropped(generation: gen)
        var m = meetings[id] ?? Meeting()
        m.interim = nil
        if m.socket == .ending {
            set(id, m)
            detach(id, to: .ended)
            return
        }
        switch MeetingTranscriptionRule.reaction(toClose: status) {
        case .stop, .yield:
            stopHub(id, code: "http_\(status ?? 0)", meeting: &m)
        case .retry, .uploadFirst:
            m.socket = .down
            set(id, m)
            scheduleRetry(id)
        }
    }

    // MARK: Persona speaking

    /// Persona's `voice` began (`on`) or stopped saying something at `at` on the recording's clock, saying `text`: the
    /// hub holds back the sentences of an open stretch until it closes, then marks Persona's echo by its rule.
    ///
    /// The meeting call's `on` goes without `text` (`spokenText`): its words stream in with its audio, so at playback's
    /// start they are at most the answer's first words, and the hub, which judges the held sentences against the `on`
    /// frame's text alone, would let the rest of Persona's answer into the live log. Without one it applies the time rule
    /// to the stretch; the whole answer reaches the hub's timeline with the stretch once it ended (`start.timeline`, the
    /// uploads' `timeline`).
    func speaking(_ id: UUID, voice: Voice, on: Bool, at: Double, text: String?) {
        let said = Self.spokenText(voice, text)
        var open = speakingOpen[id] ?? [:]
        let wasOpen = !open.isEmpty
        if on { open[voice] = RecordingSpan(start: at, end: at, text: said) } else { open[voice] = nil }
        speakingOpen[id] = open.isEmpty ? nil : open
        guard wasOpen != !open.isEmpty, let uplink, uplink.recording == id,
              [.connecting, .ready].contains(meetings[id]?.socket) else { return }
        uplink.send(text: MeetingAudioOut.speaking(on: !open.isEmpty, at: at, text: said))
    }

    /// The text a `speaking` on carries for `voice` (pure, for the tests): an answer's whole text (`MeetingSpeaker` says
    /// it all), none for the meeting call's voice, whose captions at playback's start are only a beginning.
    nonisolated static func spokenText(_ voice: Voice, _ text: String?) -> String? {
        voice == .call ? nil : text
    }

    // MARK: Stop

    /// The recording is stopping at `at` on its clock (`RecordingCapture.onStopping`): its hub-mode time ends there; `end`
    /// goes on an open socket after the last audio, the uplink comes off the recorder's tap then (it never hears the
    /// next recording), and the hub's last lines come within 15 seconds before it closes; a socket that isn't up closes
    /// now (what the hub lacks goes up as gaps).
    func ended(_ id: UUID, at: Double? = nil) {
        speakingOpen[id] = nil
        if let at, library.entry(id)?.attendance?.hubTime.last.map({ $0.end == nil }) == true {
            library.change(id) { $0.attendance?.closeHubTime(at: at) }
        }
        guard let uplink, uplink.recording == id else { return }
        var m = meetings[id] ?? Meeting()
        let ending = uplink
        let offTheTap: @Sendable () -> Void = { [weak self] in
            Task { @MainActor [weak self] in
                guard let self, self.uplink === ending else { return }
                self.deps.listen(nil)
            }
        }
        guard m.socket == .ready, uplink.end(then: offTheTap) else {
            detach(id, to: m.decided?.mode == .hub ? .ended : .off)
            return
        }
        m.socket = .ending
        set(id, m)
        endTask?.cancel()
        let wait = deps.wait
        endTask = Task { [weak self] in
            await wait(Self.endWait)
            guard !Task.isCancelled, let self, self.meetings[id]?.socket == .ending else { return }
            self.detach(id, to: .ended)
        }
    }

    /// How long the phone waits after `end` for the hub's last lines and its close (the hub's 15 seconds, and some).
    static let endWait: Duration = .seconds(20)

    // MARK: A Note

    /// The recording `id` turns into a Note at `at` on its clock (`MeetingRecorder.setMeeting`, before the switch is
    /// saved): its hub-mode time ends there (`MeetingRecorder.switched`), and so does the uplink, at the same sample. Told
    /// at once, not when the observation catches up: the tap goes on feeding the uplink meanwhile, and a buffer from `at`
    /// on is the Note's. The uplink takes nothing from `at` on (`MeetingAudioUplink.cut`) and comes off the tap now.
    func becameNote(_ id: UUID, at: Double) {
        guard let uplink, uplink.recording == id else { return }
        uplink.cut(at: MeetingAudioClock.sample(at))
        guard meetings[id]?.socket != .ending else { return }
        detach(id, to: .off)
    }

    // MARK: What `GET …/live` says

    /// `transcription` and `interim` of a `GET …/live` read (`MeetingConversation`); `read`: the parts it asked for, of
    /// which an absent one is the hub's word (no mode decided, a manager without it; no caption now).
    func liveRead(_ id: UUID, transcription: MeetingTranscriptionView?, interim: MeetingInterim?, read: Set<String>) {
        var m = meetings[id] ?? Meeting()
        if read.contains("transcription") || transcription != nil { m.hub = transcription }
        // The socket's own frames are fresher while it is up.
        if m.socket != .ready, read.contains("interim") || interim != nil { m.interim = interim }
        set(id, m)
    }

    /// Reads the live log from `from` (`GET …/live?parts=lines&from=`): the positions the socket missed while away.
    private func readLines(_ id: UUID, from start: Int) async {
        guard !readingLines.contains(id), let api = deps.api() else { return }
        readingLines.insert(id)
        defer { readingLines.remove(id) }
        var from = start
        for _ in 0..<20 {
            let data: Data
            do {
                data = try await api.request("GET", MeetingAudioEndpoint.path(id, "live"), query: ["parts": "lines", "from": String(from)])
            } catch {
                return
            }
            guard let read = try? JSON.decoder.decode(LinesRead.self, from: data) else { return }
            var m = meetings[id] ?? Meeting()
            let had = Set(m.lines.map(\.n))
            m.lines = Self.merged(m.lines, read.lines)
            set(id, m)
            if read.lines.contains(where: { !had.contains($0.n) }) { deps.linesAdded(id) }
            guard read.more, let last = read.lines.last, last.n >= from else { return }
            from = last.n + 1
        }
    }

    private nonisolated struct LinesRead: Decodable, Sendable {
        var lines: [MeetingHubLine]
        var more: Bool

        enum CodingKeys: String, CodingKey { case lines, more }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            let raw = (try? c.decodeIfPresent([JSONValue].self, forKey: .lines)) ?? []
            lines = raw.compactMap { item in
                guard case .object(let o) = item, let n = MeetingAudioIn.position(o["n"]), let start = o["start"]?.meetingNumber,
                      let end = o["end"]?.meetingNumber, let text = o["text"]?.meetingText else { return nil }
                return MeetingHubLine(n: n, start: start, end: end, text: text, source: o["source"]?.meetingText)
            }
            more = (try? c.decodeIfPresent(Bool.self, forKey: .more)) ?? false
        }
    }

    /// `lines` with `fresh` in their places, by position (a position read again replaces what was there).
    nonisolated static func merged(_ lines: [MeetingHubLine], _ fresh: [MeetingHubLine]) -> [MeetingHubLine] {
        guard !fresh.isEmpty else { return lines }
        var byN = Dictionary(lines.map { ($0.n, $0) }, uniquingKeysWith: { _, b in b })
        for line in fresh { byN[line.n] = line }
        return byN.values.sorted { $0.n < $1.n }
    }

    /// How many positions from 0 the phone holds without a hole.
    nonisolated static func contiguous(_ lines: [MeetingHubLine]) -> Int {
        var n = 0
        for line in lines.sorted(by: { $0.n < $1.n }) {
            guard line.n == n else { break }
            n += 1
        }
        return n
    }

    // MARK: Wi-Fi only

    /// **Hub transcription on Wi-Fi only** changed. Turned on, a socket opened without it opens again under it (its
    /// request keeps off cellular, `URLRequest.keepOnWiFi`), or not at all off Wi-Fi; a gap upload under way stops, and
    /// the pass starts again under the rule.
    func wifiOnlyChanged(_ on: Bool) {
        guard wifiOnly != on else { return kickGaps() }
        wifiOnly = on
        if on, let uplink, let m = meetings[uplink.recording], [.connecting, .ready].contains(m.socket) {
            uplink.close()
            set(uplink.recording, m.with(socket: .down))
            let (id, paused) = followed
            follow(id, paused: paused, entry: id.flatMap { library.entry($0) })
        }
        if on { stopGapPut() }
        kickGaps()
    }

    /// Whether audio may leave now (`MeetingTranscriptionRule.mayStream`), read right before a socket opens and before
    /// every gap piece: the Wi-Fi rule, and through a tunnel, that the phone has been off cellular since it began.
    private func mayStream(_ network: MeetingNetworkKind? = nil) -> Bool {
        MeetingTranscriptionRule.mayStream(wifiOnly: wifiOnly, network: network ?? deps.network(), tunnel: deps.tunnel(),
                                           meteredUntil: deps.meteredUntil())
    }

    // MARK: Gaps after the meeting

    /// Looks at the meetings that ended in hub mode and uploads what their hub lacks, one pass at a time, under
    /// background time (the pass right after Stop goes on as the app goes behind). A pass that left a meeting `later`
    /// (the hub or the network away) brings another after `MeetingGapUpload.retryDelay`, until none is left `later`:
    /// the hub's final pass waits at most 30 minutes for the gaps. The background time is held across that wait and the
    /// pass after it, while the system grants it: let go, the app could be suspended during the wait, and the retry would
    /// run only once it runs again, maybe after the final pass.
    func kickGaps() {
        guard gapTask == nil else { gapsAgain = true; return }
        gapRetryTask?.cancel()
        gapRetryTask = nil
        holdGapBackground()
        gapTask = Task { [weak self] in
            guard let self else { return }
            var later = false
            repeat {
                self.gapsAgain = false
                later = await self.gapPass()
            } while self.gapsAgain
            self.gapTask = nil
            if later {
                self.scheduleGapRetry()
            } else {
                self.gapRetries = 0
                self.releaseGapBackground()
            }
        }
    }

    /// Another gap pass after `MeetingGapUpload.retryDelay` of the passes in a row that left one `later`, under the
    /// background time the last pass held.
    private func scheduleGapRetry() {
        gapRetries += 1
        let delay = MeetingGapUpload.retryDelay(gapRetries)
        let wait = deps.wait
        gapRetryTask?.cancel()
        gapRetryTask = Task { [weak self] in
            await wait(.seconds(delay))
            guard !Task.isCancelled, let self else { return }
            self.gapRetryTask = nil
            self.kickGaps()
        }
    }

    /// Background time for the gap passes, unless some is held already. Once the system takes it back (it ends it by
    /// itself), the next pass asks again.
    private func holdGapBackground() {
        guard gapBackground == nil else { return }
        gapBackgrounds += 1
        let n = gapBackgrounds
        gapBackground = deps.background("meeting-transcription-gaps") { [weak self] in
            guard let self, self.gapBackgrounds == n else { return }
            self.gapBackground = nil
        }
    }

    /// The passes are done (none left `later`): the background time ends.
    private func releaseGapBackground() {
        let end = gapBackground
        gapBackground = nil
        end?()
    }

    /// Stops the gap `PUT` under way (the Wi-Fi rule turned on, the path left Wi-Fi, a rollback): the pass reads the
    /// recording and the rule again before anything else goes.
    private func stopGapPut() {
        guard let gapPut else { return }
        gapPutStopped = true
        gapPut.task.cancel()
    }

    /// Whether `entry` may still need gap uploads (pure, for the tests): a room meeting recorded here that asked for hub
    /// mode and has hub-mode time (`RecordingAttendance.hubTime`; never one an older manager rolled back), ended, its last
    /// revision (the `complete: true` one) stored, within the hub's retention (the phone keeps its audio at least that
    /// long, and never longer), not known to be done or transcribed on the phone.
    nonisolated static func needsGaps(_ entry: RecordingEntry, meeting: Meeting?, expired: Bool, synced: Bool, done: Bool) -> Bool {
        guard !entry.active, entry.roomMeeting, let attendance = entry.attendance, attendance.transcription == .hub,
              !attendance.hubRolledBack, !attendance.hubTime.isEmpty, !expired, !done else { return false }
        if let mode = meeting?.mode, mode != .hub { return false }
        if meeting?.hub?.settled == true { return false }
        return entry.transcribedAll && synced
    }

    /// The stretches of `entry`'s audio a gap upload may send (pure, for the tests): its hub-mode time, an open stretch
    /// to the recording's end. Never what was recorded as a Note, after a rollback, or after hub transcription stopped
    /// for it (`RecordingAttendance.hubStopped`).
    nonisolated static func uploadable(_ entry: RecordingEntry) -> [MeetingGap] {
        guard let attendance = entry.attendance, attendance.transcription == .hub, !attendance.hubRolledBack else { return [] }
        return MeetingGap.union(attendance.hubStretches(until: entry.duration).map { MeetingGap(from: $0.from, to: $0.to) })
    }

    /// One pass over the meetings that need gaps; true when one was left `later`.
    private func gapPass() async -> Bool {
        let due = library.entries.filter { isDue($0) }.map(\.id)
        var later = false
        for id in due {
            // As it is now: the uploads before it took time, and a rollback, a stop or a delete may have come meanwhile.
            guard let entry = library.entry(id), isDue(entry) else { continue }
            await uploadGaps(entry)
            if case .later = meetings[id]?.gapUpload { later = true }
        }
        return later
    }

    /// `needsGaps` with what the app knows of `entry` now (`synced`: taken as given).
    private func isDue(_ entry: RecordingEntry, synced: Bool? = nil) -> Bool {
        Self.needsGaps(entry, meeting: meetings[entry.id], expired: deps.expired(entry), synced: synced ?? deps.synced(entry),
                       done: gapsDone.contains(entry.id))
    }

    /// The recording `id` as it is now, while its gap uploads may go on; nil once they may not: deleted, no longer a
    /// hub-mode room meeting, rolled back (`hubRolledBack`, or an older manager's `200` in this run), transcribed on the
    /// phone, done on the hub, or past the retention. A new revision on its way doesn't stop them.
    private func stillUploading(_ id: UUID) -> RecordingEntry? {
        guard let entry = library.entry(id), isDue(entry, synced: true) else { return nil }
        return entry
    }

    private func uploadGaps(_ entry: RecordingEntry) async {
        let id = entry.id
        // Only audio recorded while it was a hub-mode meeting goes: none, nothing to do.
        guard !Self.uploadable(entry).isEmpty else { return finishGaps(id) }
        guard let api = deps.api() else { return }
        guard mayStream() else { return setGapUpload(id, .waitingForWiFi) }
        // The hub's word first: its mode, its state and what it lacks.
        let view: MeetingTranscriptionView?
        do {
            let data = try await api.request("GET", MeetingAudioEndpoint.path(id, "live"), query: ["parts": "transcription"])
            let value = try? JSON.decoder.decode(JSONValue.self, from: data)
            view = MeetingTranscriptionView(value?["transcription"])
        } catch let error as APIError where [404, 410].contains(error.status) {
            return finishGaps(id)
        } catch {
            return setGapUpload(id, .later(nil))
        }
        var m = meetings[id] ?? Meeting()
        m.hub = view
        set(id, m)
        guard let view, view.mode == .hub, !view.settled else { return finishGaps(id) }
        var gaps = view.gaps
        var tried: [MeetingGapUpload.Piece] = []
        var most = MeetingGapUpload.maxSeconds
        setGapUpload(id, .uploading)
        while true {
            // Read again before every piece: a rollback, a stop, a delete or a Note since the pass began ends it here, and
            // the pieces keep to the hub-mode time the recording has now.
            guard let now = stillUploading(id) else { return finishGaps(id) }
            guard let piece = MeetingGapUpload.plan(gaps, held: now.duration, within: Self.uploadable(now), maxSeconds: most)
                .first(where: { !tried.contains($0) }) else { break }
            guard mayStream() else { return setGapUpload(id, .waitingForWiFi) }
            tried.append(piece)
            guard let audio = await deps.audio(now, piece), !audio.data.isEmpty else { continue }
            // The decoding took a while: looked at again right before the audio goes.
            guard stillUploading(id) != nil, mayStream() else {
                tried.removeAll { $0 == piece }
                continue
            }
            let put = deps.put, wifi = wifiOnly
            let path = MeetingAudioEndpoint.path(id, "audio"), query = ["at": MeetingAudioClock.query(audio.at)]
            let task = Task { try await put(api, path, query, audio.data, wifi) }
            gapPut = (id, task)
            gapPutStopped = false
            let result = await task.result
            let stopped = gapPutStopped
            gapPut = nil
            gapPutStopped = false
            switch result {
            case .success(let data):
                let value = try? JSON.decoder.decode(JSONValue.self, from: data)
                gaps = MeetingGap.list(value?["gaps"])
                var m = meetings[id] ?? Meeting()
                m.gaps = gaps
                m.hub?.gaps = gaps
                set(id, m)
            case .failure where stopped:
                // Stopped from this side: the piece is looked at again under the rule and the recording as they are now.
                tried.removeAll { $0 == piece }
            case .failure(let error as APIError):
                switch MeetingGapUpload.reaction(status: error.status, code: error.code) {
                case .done:
                    return finishGaps(id)
                case .uploadFirst:
                    deps.reupload(id)
                    return setGapUpload(id, .later(error.code))
                case .smaller:
                    guard most > 10 else { return finishGaps(id) }
                    most /= 2
                    tried.removeAll { $0 == piece }
                case .later:
                    return setGapUpload(id, .later(error.code))
                }
            case .failure:
                return setGapUpload(id, .later(nil))
            }
        }
        finishGaps(id)
    }

    private func finishGaps(_ id: UUID) {
        gapsDone.insert(id)
        setGapUpload(id, .done)
    }

    private func setGapUpload(_ id: UUID, _ state: GapUpload) {
        var m = meetings[id] ?? Meeting()
        m.gapUpload = state
        set(id, m)
    }

    private func set(_ id: UUID, _ m: Meeting) {
        var m = m
        followAway(id, &m)
        if meetings[id] != m { meetings[id] = m }
        if m.hubMode != hubMeetings.contains(id) {
            if m.hubMode { hubMeetings.insert(id) } else { hubMeetings.remove(id) }
        }
    }

    /// A socket that was up and is away (down, or opening again) becomes `away` after `reconnectGrace`, unless it is back
    /// by then; anything else (up, paused, at its end, waiting for Wi-Fi) is no reconnect.
    private func followAway(_ id: UUID, _ m: inout Meeting) {
        guard m.everUp, [.down, .connecting].contains(m.socket) else {
            m.away = false
            awayTasks.removeValue(forKey: id)?.cancel()
            return
        }
        guard !m.away, awayTasks[id] == nil else { return }
        let wait = deps.wait
        awayTasks[id] = Task { [weak self] in
            await wait(MeetingTranscriptionRule.reconnectGrace)
            guard !Task.isCancelled, let self else { return }
            self.awayTasks[id] = nil
            guard var m = self.meetings[id], m.everUp, [.down, .connecting].contains(m.socket) else { return }
            m.away = true
            self.set(id, m)
        }
    }

    // MARK: Probe

    /// What a UI test reads (`probe.meeting-transcription`, `-probes YES`): the recording the uplink follows.
    nonisolated struct Probe: Encodable, Equatable, Sendable {
        var recording: String?
        var asked: String?
        var mode: String?
        var socket: String
        var lines: Int
        var interim: String?
        var problem: String?
        var stopped: String?
        var fellBack: Bool
        var phoneLines: Bool
    }

    var probe: Probe {
        guard let id = current else { return Probe(socket: Socket.off.rawValue, lines: 0, fellBack: false, phoneLines: true) }
        let m = meeting(id)
        let shown = shown(id, role: library.entry(id)?.attendance?.role ?? .off)
        return Probe(recording: id.uuidString.lowercased(), asked: m.asked?.rawValue, mode: m.mode?.rawValue, socket: m.socket.rawValue,
                     lines: m.lines.count, interim: m.interim?.text, problem: m.problem, stopped: m.stopped,
                     fellBack: shown.fellBack, phoneLines: shown.phoneLines)
    }
}

private extension MeetingTranscription.Meeting {
    func with(socket: MeetingTranscription.Socket) -> Self {
        var m = self
        m.socket = socket
        return m
    }
}

/// Where the audio socket and the gap uploads go: `GET /api/v1/recordings/{id}/audio` upgraded to a WebSocket through the
/// app's way to the hub (`APIClient.url`, its tunnel when it uses one), with the device's token, as `VoiceEndpoint` opens
/// the voice extension's socket.
nonisolated enum MeetingAudioEndpoint {
    static func path(_ id: UUID, _ route: String) -> String { "/api/v1/recordings/\(id.uuidString.lowercased())/\(route)" }

    static func url(_ api: APIClient, recording: UUID) -> URL {
        let http = api.url(path(recording, "audio"))
        guard var comps = URLComponents(url: http, resolvingAgainstBaseURL: false) else { return http }
        comps.scheme = comps.scheme?.lowercased() == "https" ? "wss" : "ws"
        return comps.url ?? http
    }

    /// `wifiOnly`: **Hub transcription on Wi-Fi only**; the system then opens it over neither cellular data nor an
    /// expensive network (`URLRequest.keepOnWiFi`), whatever the network looked like a moment ago.
    static func request(_ api: APIClient, recording: UUID, wifiOnly: Bool) -> URLRequest {
        var req = URLRequest(url: url(api, recording: recording), timeoutInterval: 15)
        api.config.authorize(&req)
        req.keepOnWiFi(wifiOnly)
        return req
    }
}

extension MeetingTranscription.Deps {
    /// The app's: the hub-level group's hub (recordings sync with it), the voice call's socket kind, the recorder's tap.
    static var live: Self {
        Self(api: { AppModel.shared.api },
             socket: { request, api in HubVoiceSocket(request: request, session: api.session) },
             // The observed kind brings the uplink in step as it changes (`sync`); the monitor's own latest answer, which
             // comes a hop earlier, is what the rule decides on.
             network: {
                 _ = MeetingNetwork.shared.kind
                 return MeetingNetwork.shared.latest.kind
             },
             listen: { sink in RecordingCapture.shared.listenUplink(sink) },
             reupload: { RecordingSync.shared.reupload($0) },
             wait: { try? await Task.sleep(for: $0) },
             audio: { entry, piece in
                 let folder = RecordingLibrary.shared.folder(entry.id), segments = entry.segments
                 return await Task.detached(priority: .utility) {
                     MeetingSegmentAudio.pcm(folder: folder, segments: segments, at: piece.at, seconds: piece.seconds)
                 }.value
             },
             expired: { RecordingSync.shared.isExpired($0) },
             synced: { RecordingSync.shared.status($0) == .synced },
             position: { id in RecordingCapture.shared.recordingID == id ? RecordingCapture.shared.timeline : nil },
             linesAdded: { MeetingConversation.shared.liveAccepted($0) },
             background: { name, expired in
                 let grant = MeetingBackgroundTask()
                 grant.begin(name, expired: expired)
                 return { grant.end() }
             },
             tunnel: { AppModel.shared.api.flatMap { HubTransport.since($0.config.url) } },
             meteredUntil: { MeetingNetwork.shared.latest.meteredUntil })
    }
}

/// `beginBackgroundTask` for the gap passes, ended exactly once: by the passes, or in the expiration handler (which must
/// end it before returning, or the system ends the app), which then tells `expired`. The passes themselves go on if the
/// app still runs.
@MainActor private final class MeetingBackgroundTask {
    private var id = UIBackgroundTaskIdentifier.invalid

    func begin(_ name: String, expired: @escaping @MainActor @Sendable () -> Void) {
        id = UIApplication.shared.beginBackgroundTask(withName: name) { @Sendable in
            MainActor.assumeIsolated {
                self.end()
                expired()
            }
        }
    }

    func end() {
        guard id != .invalid else { return }
        UIApplication.shared.endBackgroundTask(id)
        id = .invalid
    }
}

#if DEBUG
import SwiftUI

/// `probe.meeting-transcription` (`-probes YES`): the transcription store as a UI test reads it (`MeetingTranscription.probe`).
struct MeetingTranscriptionProbe: View {
    var body: some View {
        Color.clear
            .frame(width: Theme.Size.hairline, height: Theme.Size.hairline)
            .allowsHitTesting(false)
            .screenProbe("meeting-transcription", MeetingTranscription.shared.probe)
    }
}
#endif
#endif
