#if os(iOS) || os(visionOS) || os(watchOS)
import AVFoundation
import Observation
#if os(iOS) || os(visionOS)
import UIKit
#else
import WatchKit
#endif

/// Hourhand's phone / watch session management, sharing the segment engine, pausing, bookmarks and resource protection.
///
/// On the watch it also runs all-day recording (`start(day:)`, docs/assistant/day-recap.md「The day recording on the
/// watch」): one recording per local day, fixed to the time zone it began in, cut at that zone's midnight without closing
/// the microphone, paused and continued within the day under the same id. Each chunk's seq comes from the recording's
/// counter as its file opens, and everything that is not audio (the microphone opening and closing, marks, erases,
/// lost chunks, the end of the day) is a numbered control event. Every finished segment is registered in the recording
/// its folder names, in the order the writer finished it (`applyWriterEvents`), so a midnight never files one day's
/// audio under the other.
@Observable
final class RecordingCapture {
    static let shared = RecordingCapture()
    enum State { case idle, starting, recording, paused }
    private(set) var state: State = .idle
    private(set) var elapsed: Double = 0
    private(set) var recordingID: UUID?
    private(set) var rolling = false
    /// All-day recording is on: recording, or paused within the day (by the user, a call, a failed resume).
    private(set) var allDay = false
    /// The day recording being recorded or paused; nil outside all-day recording.
    var dayRecordingID: UUID? { allDay ? recordingID : nil }
    var error: String?
    /// Non-blocking hint, e.g. the Bluetooth microphone was not found and the phone mic is used.
    private(set) var notice: String?
    var onSaved: ((UUID) -> Void)?
    /// A recording is about to stop, however it stops (Stop, low storage, a failed write, the audio service reset): while
    /// its clock still runs, so whatever follows it (Persona's voice in a meeting, #2981) can end at the right time.
    @ObservationIgnored var onStopping: ((UUID) -> Void)?
    /// Paused by the system (a phone call, Siri, an alarm), not by the user: it picks up again when the system ends the
    /// interruption, or when the app comes back to the front (`resumeAfterInterruption`).
    private(set) var interrupted = false
    /// The system ended an interruption but the microphone could not be opened again (the app was in the background): the
    /// phone tells the user to come back (a notification), since nothing else will resume it.
    @ObservationIgnored var onInterruptionNotResumed: (() -> Void)?

    // MARK: All-day hooks

    /// UserDefaults Bool: the user wants all-day recording on. Set when it starts, cleared by the user's Stop (and by the
    /// phone deleting the day being recorded), so a launch can tell a day the app lost (`haltedOnLaunch`) from one the
    /// user ended.
    nonisolated static let allDayIntentKey = "recording.allDay.on"
    /// All-day recording stopped recording without the user (a call, a failed resume, the microphone changing, the audio
    /// service resetting, low battery or storage, a failed write): the watch tells the user and offers to continue.
    @ObservationIgnored var onDayHalted: ((RecordingDayHalt) -> Void)?
    /// All-day recording records again after a pause or a halt.
    @ObservationIgnored var onDayResumed: (() -> Void)?
    /// A chunk of a day recording (its id) was saved: audio keeps arriving. Control events and erases reach the transfer
    /// through `onSaved`, as every change does; this one is only for audio (the watch's dead-man's switch).
    @ObservationIgnored var onDayChunk: ((UUID) -> Void)?
    /// The phone deleted the day recording all-day recording was on in (its id, docs/assistant/day-recap.md「Deleting」):
    /// all-day recording stopped and the user's wish to record went with it. The watch says why; a start later that day
    /// opens a new recording.
    @ObservationIgnored var onDayDeletedRemotely: ((UUID) -> Void)?
    /// A day recording left open while all-day was on, found at launch: the app ended with it (a crash, the system).
    /// Kept until the watch's all-day controller takes it, after installing `onDayHalted` (`takeHaltedOnLaunch`).
    @ObservationIgnored private(set) var haltedOnLaunch: (id: UUID, reason: RecordingDayHalt)?
    /// Whether the phone takes messages now, pushed in by the day transfer when it changes (never read from the
    /// WatchConnectivity session on every tick): day chunks are short while it does, so the text follows the voice, and
    /// long while they wait for it.
    @ObservationIgnored var linkReachable = false {
        didSet { if allDay, oldValue != linkReachable { writer?.segmentLength = chunkLength } }
    }

    /// The recorded time now, read without observing it (`elapsed` changes four times a second).
    var timeline: Double { writer?.elapsed ?? sampleElapsed }
    @ObservationIgnored private var sampleElapsed: Double = 0
    /// The microphone's recent level, 0...1 (the writer's smoothed, perceptual level, read-only), for the live page's meter.
    /// Not observed: a meter reads it from its own clock, many times a second.
    var inputLevel: Double {
        #if DEBUG
        if writer == nil { return sampleLevel }
        #endif
        return Double(writer?.level ?? 0)
    }
    #if DEBUG
    /// Screenshots and the DEBUG flow (`-showMeetingSample`): the level a recording with no microphone shows.
    @ObservationIgnored var sampleLevel: Double = 0
    #endif
    @ObservationIgnored private var input: RecordingInput = .phone
    /// Persona may answer out loud during this recording (#2981: a meeting it takes part in). The phone's microphone
    /// session then also lets a Bluetooth headset play (A2DP is listening only, so the recording keeps its microphone);
    /// a wired headset and the loudspeaker play anyway. Off for every other recording, whose session stays as it was.
    @ObservationIgnored private(set) var playsAloud = false
    /// A meeting Persona attends with the live view (#3862 phase 2): 15-second segments, in front and behind
    /// (`setShortSegments`). Off for every other recording.
    @ObservationIgnored private(set) var shortSegments = false
    /// A room meeting (#3862 phase 4): the recorder's engine carries the player of a meeting call (`callPlayer`).
    @ObservationIgnored private(set) var meetingCall = false
    /// The input runs with the system's echo cancellation: asked for as a room meeting that starts taking part starts,
    /// never switched while it records (`start.echo_cancellation` of its call says so).
    @ObservationIgnored private(set) var cancelsEcho = false
    /// Who hears the converted audio besides the files (`listen`).
    @ObservationIgnored private var listener: (@Sendable (AVAudioPCMBuffer, Double) -> Void)?
    /// The second consumer of the tap: the hub uplink (#3862 phase 5, `listenUplink`), beside the meeting call's.
    @ObservationIgnored private var uplinkListener: (@Sendable (AVAudioPCMBuffer, Double) -> Void)?
    @ObservationIgnored private var writer: RecordingSegmentWriter?
    @ObservationIgnored private var ticker: Task<Void, Never>?
    @ObservationIgnored private var observers: [NSObjectProtocol] = []
    @ObservationIgnored private var sessionObserver: RecordingSessionObserver?
    @ObservationIgnored private var generation = UUID()
    @ObservationIgnored private var activeMicOwner: UUID?
    @ObservationIgnored private var resumeTask: Task<Void, Never>?

    // All-day state, main thread only.
    /// The midnight the writer cuts at next, and the recording named for after it.
    @ObservationIgnored private var dayPlan: DayPlan?
    /// Day recordings named in advance and not made yet (the next day's, made with its first sample).
    @ObservationIgnored private var pendingDays: [UUID: PendingDay] = [:]
    /// What the writer reported that could not be saved yet (recording.json failed to write, the next day's entry could
    /// not be made), in order. Tried again before anything newer, and kept across a stop: the writer's log no longer
    /// has it, and its audio is on disk without a record until it is saved.
    @ObservationIgnored private var unregistered: [HeldEvent] = []
    /// Control events not saved yet (a failed write, or a day named in advance not made yet), in order: each waits for
    /// the ones before it of the same recording (`record`).
    @ObservationIgnored private var owedEvents: [OwedEvent] = []
    /// When each kind of mark was last added: a double tap adds one.
    @ObservationIgnored private var lastMarks: [String: Date] = [:]

    private init() {
        // Before any seq is taken: a day recording's chunks cut short by the last exit become `lost` events here, and
        // its counter stays (docs/assistant/day-recap.md「The day recording on the watch」).
        let recovered = RecordingLibrary.shared.recover(source: Self.source)
        // Here rather than as a callback: nothing can have installed `onDayHalted` this early (capture is made on first use).
        haltedOnLaunch = Self.launchHalt(recovered: recovered, entries: RecordingLibrary.shared.entries,
                                         intent: UserDefaults.standard.bool(forKey: Self.allDayIntentKey))
        sessionObserver = RecordingSessionObserver { [weak self] event in self?.handle(event) }
        let center = NotificationCenter.default
        observers.append(center.addObserver(forName: .AVAudioEngineConfigurationChange, object: nil, queue: .main) { @Sendable [weak self] note in
            let engine = note.object.map { ObjectIdentifier($0 as AnyObject) }
            MainActor.assumeIsolated {
                // Every engine posts it (dictation's, a voice call's): only this recording's own engine matters.
                guard let self, self.state == .recording, self.writer?.owns(engine) == true else { return }
                do { try self.writer?.restartAfterConfigurationChange() }
                catch { self.microphoneChangeFailed() }
            }
        })
        observers.append(center.addObserver(forName: AVAudioSession.mediaServicesWereResetNotification, object: nil, queue: .main) { @Sendable [weak self] _ in
            MainActor.assumeIsolated {
                // Paused too (during a call): the engine it would resume is gone with the reset.
                guard let self, self.state == .recording || self.state == .paused else { return }
                self.haltBySystem(.audioReset, error: String(localized: "The system audio service reset. What was finished is saved; start recording again."))
            }
        })
        // The date, the clock or the time zone changed. watchOS has no significant-time-change notification, and a day
        // notification can come late (on wake), so the day check runs again before the microphone opens too.
        for name in [Notification.Name.NSSystemTimeZoneDidChange, .NSSystemClockDidChange, .NSCalendarDayChanged] {
            observers.append(center.addObserver(forName: name, object: nil, queue: .main) { @Sendable [weak self] _ in
                MainActor.assumeIsolated { self?.dayClockChanged() }
            })
        }
    }

    private static var source: RecordingEntry.Source {
        #if os(watchOS)
        .watch
        #else
        .phone
        #endif
    }

    private func configureSession(token: UUID) async throws {
        let session = AVAudioSession.sharedInstance()
        guard generation == token else { return }
        notice = nil
        #if os(iOS) || os(visionOS)
        #if os(iOS)
        if input == .bluetooth {
            try session.setCategory(.playAndRecord, mode: .default, options: [.allowBluetoothHFP])
            try session.setActive(true)
            if let port = await bluetoothInput(session, token: token) {
                guard generation == token else { return }
                try session.setPreferredInput(port)
                return
            }
            guard generation == token else { return }
            notice = String(localized: "No glasses or Bluetooth headset microphone found; using the phone's microphone")
        }
        #endif
        try session.setCategory(.playAndRecord, mode: .default, options: Self.phoneOptions(playsAloud: playsAloud))
        try session.setActive(true)
        #if os(iOS)
        let ports = session.availableInputs ?? []
        if let index = RecordingInput.preferredIndex(ports.map(\.portType), for: .phone) {
            try session.setPreferredInput(ports[index])
        }
        #else
        if let mic = session.availableInputs?.first(where: { $0.portType == .builtInMic }) { try session.setPreferredInput(mic) }
        #endif
        #else
        try session.setCategory(.record, mode: .default)
        try session.setActive(true)
        #endif
    }

    #if os(iOS) || os(visionOS)
    /// The phone microphone's session options: the loudspeaker rather than the earpiece, and a Bluetooth headset for
    /// listening when Persona may speak (`playsAloud`).
    static func phoneOptions(playsAloud: Bool) -> AVAudioSession.CategoryOptions {
        #if os(iOS)
        playsAloud ? [.defaultToSpeaker, .allowBluetoothA2DP] : [.defaultToSpeaker]
        #else
        [.defaultToSpeaker]
        #endif
    }

    /// Persona started or stopped taking part while recording: the same session, with or without the headset for
    /// listening. A Bluetooth microphone's session plays through that headset already.
    func setPlaysAloud(_ on: Bool) {
        guard playsAloud != on else { return }
        playsAloud = on
        guard writer != nil, input == .phone else { return }
        try? AVAudioSession.sharedInstance().setCategory(.playAndRecord, mode: .default, options: Self.phoneOptions(playsAloud: on))
    }
    #endif

    /// Persona started or stopped attending a meeting with the live view (#3862 phase 2,
    /// `RecordingAttendance.shortSegments`): its segments are 15 seconds from now on, in front or not; the open one closes
    /// as soon as it is longer. Off again, the usual 30 / 60 s.
    func setShortSegments(_ on: Bool) {
        guard shortSegments != on else { return }
        shortSegments = on
        guard !allDay, let writer else { return }
        writer.segmentLength = chunkLength
    }

    #if os(iOS)
    /// The Bluetooth HFP input appears only after the route settles, so poll for up to two seconds.
    private func bluetoothInput(_ session: AVAudioSession, token: UUID) async -> AVAudioSessionPortDescription? {
        var waited = 0
        while generation == token, !Task.isCancelled {
            let ports = session.availableInputs ?? []
            if let index = RecordingInput.preferredIndex(ports.map(\.portType), for: .bluetooth) { return ports[index] }
            guard waited < 10 else { return nil }
            do { try await Task.sleep(for: .milliseconds(200)) } catch { return nil }
            waited += 1
        }
        return nil
    }

    /// Name of the input the current route records from, shown while recording.
    var inputName: String? {
        guard state == .recording || state == .paused else { return nil }
        return AVAudioSession.sharedInstance().currentRoute.inputs.first?.portName
    }
    #endif

    private static func permission() async -> Bool {
        if #available(iOS 17.0, watchOS 10.0, *) { return await AVAudioApplication.requestRecordPermission() }
        return await withCheckedContinuation { continuation in
            AVAudioSession.sharedInstance().requestRecordPermission { @Sendable allowed in continuation.resume(returning: allowed) }
        }
    }

    /// `title` / `meeting`: what the recording is called and, for a meeting recording, its meeting (docs/apple/recording.md).
    /// `playsAloud`: Persona takes part in this meeting and may answer out loud. `shortSegments`: Persona attends this
    /// meeting with the live view (#3862 phase 2), so its segments are 15 seconds from the first (`setShortSegments`).
    /// `meetingCall`: a room meeting (#3862 phase 4), whose engine carries a meeting call's player; `echoCancellation`:
    /// it starts taking part, so its input asks for voice processing from the start (iPhone only; never the simulator,
    /// where it does not run).
    /// `day`: all-day recording — goes on in the open day recording whose day is still on, or starts today's (saved
    /// before the microphone opens); the consent is the phone's and the notification permission the caller's to have
    /// asked for.
    func start(locale: String = "zh-CN", input: RecordingInput = .phone, title: String = "", meeting: RecordingMeeting? = nil, rolling: Bool = false,
               playsAloud: Bool = false, shortSegments: Bool = false, meetingCall: Bool = false, echoCancellation: Bool = false,
               day: Bool = false) async {
        guard state == .idle else { return }
        generation = UUID(); let token = generation
        // One mode at a time: an all-day recording is never also a rolling one.
        let rolling = rolling && !day
        state = .starting; error = nil; notice = nil; self.rolling = rolling
        self.input = input
        self.playsAloud = playsAloud
        self.shortSegments = shortSegments && !day
        self.meetingCall = meetingCall && !day && !rolling
        cancelsEcho = false
        let allowed = await Self.permission()
        guard generation == token, state == .starting else { return }
        guard allowed else { failStarting(String(localized: "Allow Persona to use the microphone in Settings")); return }
        if let problem = Self.resourceProblem(starting: true) { failStarting(problem.message); return }
        let micOwner = UUID()
        guard RecordingMicrophone.shared.claim(micOwner) else {
            failStarting(String(localized: "The microphone is in use for dictation. Finish dictating before recording.")); return
        }
        activeMicOwner = micOwner
        do {
            try await configureSession(token: token)
            // Cancelled while Bluetooth discovery was running: cancelStarting() already released the session and mic.
            guard generation == token else { return }
            // Audio and events an earlier recording finished but could not save: in order, before anything of this one.
            if hasUnsaved { applyWriterEvents() }
            let entry: RecordingEntry
            if day {
                entry = try openDay(locale: locale, now: .now)
            } else {
                entry = try RecordingLibrary.shared.create(source: Self.source, locale: locale, title: title, meeting: meeting,
                                                           kind: rolling ? .rolling : .clip)
            }
            recordingID = entry.id
            let writer = makeWriter(for: entry)
            #if os(iOS)
            if self.meetingCall {
                writer.callPlayerFormat = Self.callFormat
                writer.voiceProcessing = echoCancellation
            }
            #endif
            writer.listener = listeners
            connect(writer)
            // Before the first buffer, so a start a moment before midnight still cuts on the exact sample.
            if day { planBoundary(for: entry.id, writer: writer) }
            let opening = Date.now
            try writer.start()
            self.writer = writer
            cancelsEcho = writer.cancelsEcho
            elapsed = 0; state = .recording
            if day { beganDay(entry.id, at: opening) }
            startTicker()
        } catch {
            guard generation == token else { return }
            // A day recording stays open for the next start: its microphone never opened, so nothing of this start is
            // in its events.
            if let id = recordingID, !day {
                RecordingLibrary.shared.change(id) { $0.active = false; $0.expectedSegments = 0 }
            }
            recordingID = nil; state = .idle; self.rolling = false; dayPlan = nil
            releaseAudioSession()
            self.error = String(localized: "Can't open the microphone. Try again later.")
        }
    }

    /// A writer into `entry`'s folder. A day recording's chunks take their seqs from the library's counter on the writer's
    /// queue as each file opens (`RecordingLibrary.allocateSeq`): on disk before the file exists, so no seq is handed out
    /// twice and one an exit cuts short is reported lost at the next launch.
    private func makeWriter(for entry: RecordingEntry) -> RecordingSegmentWriter {
        let library = RecordingLibrary.shared
        let allocate: (@Sendable (URL) -> Int?)? = entry.isDay
            ? { @Sendable folder in UUID(uuidString: folder.lastPathComponent).map { library.allocateSeq($0) } }
            : nil
        let writer = RecordingSegmentWriter(directory: library.folder(entry.id), allocateSeq: allocate)
        writer.compactsFiles = entry.isDay
        writer.cutsAtGaps = entry.isDay
        writer.segmentLength = entry.isDay ? Self.dayChunkLength(reachable: linkReachable) : Self.segmentLength(short: shortSegments)
        return writer
    }

    /// The format of a meeting call's player (`callPlayer`): mono float at the rate the voice extensions speak at; a call
    /// at another rate is resampled on its way in.
    nonisolated static let callFormat = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 24000, channels: 1, interleaved: false)!

    /// The meeting call's player on the recorder's engine (#3862 phase 4): nil without one (not a room meeting, the watch,
    /// a made-up recording).
    var callPlayer: AVAudioPlayerNode? { writer?.callPlayer }

    /// The player of Persona's other spoken answers on the recorder's engine, beside the call's (`MeetingVoicePlayer`), so
    /// voice processing takes them out of the microphone too and doesn't duck them; nil where there is no call player.
    var answerPlayer: AVAudioPlayerNode? { writer?.answerPlayer }

    /// Whether the engine runs now: a paused recording's player stops with it.
    var engineRunning: Bool { writer?.isRunning == true }

    /// Whether a notification's object (`.AVAudioEngineConfigurationChange`) is the recorder's own engine.
    func ownsEngine(_ object: ObjectIdentifier?) -> Bool { writer?.owns(object) == true }

    /// `sink` hears every buffer of the recording's audio as it is converted (16 kHz mono float), on the audio thread,
    /// besides the files, with where its first sample lies (seconds, on `listenPosition`'s clock): a meeting call's Talk
    /// spans and the talk window's silence (#3862 phase 4). One listener; nil stops it. It stays for the next recording
    /// until it is taken away.
    func listen(_ sink: (@Sendable (AVAudioPCMBuffer, Double) -> Void)?) {
        listener = sink
        writer?.listener = listeners
    }

    /// The hub uplink's own listener (#3862 phase 5, docs/assistant/meeting-minutes.md「On the phone」): every buffer of
    /// the recording's audio, as `listen`'s sink gets it and beside it, stamped with the same clock (the writer's frame
    /// position, paused time left out). One uplink; nil stops it. Unlike `listen`'s, it belongs to the recording: Stop
    /// takes it away once the last buffers reached it, so it never hears the next recording.
    func listenUplink(_ sink: (@Sendable (AVAudioPCMBuffer, Double) -> Void)?) {
        uplinkListener = sink
        writer?.listener = listeners
    }

    /// What the writer hands each buffer to: the call's listener and the uplink's, each when there is one.
    private var listeners: (@Sendable (AVAudioPCMBuffer, Double) -> Void)? {
        switch (listener, uplinkListener) {
        case (nil, nil): nil
        case (let one?, nil), (nil, let one?): one
        case (let call?, let uplink?): { @Sendable buffer, at in
            call(buffer, at)
            uplink(buffer, at)
        }
        }
    }

    /// Where what the microphone takes at this moment will lie, on the clock of the listener's positions (`listen`): the
    /// recording's timeline plus what the tap's next buffer has gathered so far (`RecordingSegmentWriter.elapsedNow`).
    /// Talk's press and release, each placed to its safe side, so a buffer's samples from before the press or after the
    /// release stay out of the span (#3862 phase 4).
    func listenPosition(_ edge: RecordingSegmentWriter.SpanEdge) -> Double {
        #if DEBUG
        if writer == nil, recordingID != nil {
            return RecordingSegmentWriter.placed(edge, base: sampleFed, since: Date.now.timeIntervalSince(sampleFedAt),
                                                 longestBuffer: Self.sampleBuffer)
        }
        #endif
        return writer?.elapsedNow(edge) ?? timeline
    }

    #if DEBUG
    /// The made-up audio handed to the listener so far (`feedSample`), seconds, and when the newest of it came: its
    /// positions, which a made-up recording's clock (it stands still) can't give.
    @ObservationIgnored private var sampleFed: Double = 0
    @ObservationIgnored private var sampleFedAt = Date.distantPast
    /// The made-up audio comes 100 ms at a time (`RecordingSamples.startTalkAudio`).
    private static let sampleBuffer: Double = 0.1

    /// A made-up recording's audio (`-showMeetingSample room-…`): canned PCM handed to the listeners as the tap would,
    /// so Talk and the hub uplink work with no microphone.
    func feedSample(_ buffer: AVAudioPCMBuffer) {
        guard isSample, state == .recording else { return }
        let at = sampleFed
        sampleFed += Double(buffer.frameLength) / buffer.format.sampleRate
        sampleFedAt = .now
        listeners?(buffer, at)
    }

    /// A made-up recording is being recorded (`showSample`): no microphone, no files.
    var isSample: Bool { writer == nil && recordingID != nil }
    #endif

    /// A start that never reached the microphone.
    private func failStarting(_ message: String) {
        state = .idle; rolling = false; error = message
    }

    func cancelStarting() {
        guard state == .starting else { return }
        generation = UUID(); state = .idle; notice = nil; rolling = false
        releaseAudioSession()
    }

    func togglePause() {
        #if DEBUG
        if writer == nil, recordingID != nil, sampleElapsed > 0 {
            if state == .recording { state = .paused } else if state == .paused { state = .recording }
            return
        }
        #endif
        guard let writer else { return }
        if state == .recording {
            interrupted = false
            resumeTask?.cancel(); resumeTask = nil
            writer.pause(); state = .paused
            // What was said until now becomes a chunk, so it reaches the phone during the pause.
            if allDay { pauseDay(reason: RecordingDayContract.Reason.paused) }
        } else if state == .paused {
            resume(writer)
        }
    }

    /// After a phone call, or when the app comes back to the front while one left the recording paused: carries on.
    /// Nothing when the user paused it.
    func resumeAfterInterruption() {
        guard interrupted, state == .paused, let writer else { return }
        resume(writer)
    }

    private func resume(_ writer: RecordingSegmentWriter) {
        guard resumeTask == nil else { return }
        let token = generation
        resumeTask = Task { @MainActor [weak self] in
            defer { self?.resumeTask = nil }
            guard let self else { return }
            do {
                try await self.configureSession(token: token)
                guard self.generation == token, self.writer === writer, self.state == .paused else { return }
                // Just before the microphone opens, after whatever the session took: a day over by now gives way to today's.
                guard !self.allDay || self.dayTakesAudio() else {
                    self.onDayHalted?(.writeFailed)
                    return
                }
                let opening = Date.now
                try writer.resume()
                self.state = .recording; self.interrupted = false; self.error = nil
                if self.allDay { self.dayResumed(at: opening) }
            } catch {
                guard self.generation == token else { return }
                self.error = String(localized: "Can't open the microphone; the recording is still saved")
                // Still `interrupted`: coming back to the app tries again.
                if self.interrupted { self.onInterruptionNotResumed?() }
                if self.allDay { self.onDayHalted?(.resumeFailed) }
            }
        }
    }

    /// Closes the segment being written and goes on in a new one, so what was just said is transcribed now rather than
    /// at the segment's end (#2981: asking Persona about it). Paused too (`cutsOnFlush`): a Talk span the pause closed is
    /// asked with what was said in it (#3862 phase 4), and the pause's own segment would wait for the Continue.
    func flushSegment() {
        guard Self.cutsOnFlush(state), let writer, writer.cut() != nil else { return }
        applyWriterEvents()
    }

    /// Whether `flushSegment` cuts in `state`: while recording, and while paused (the writer has stopped taking audio, and
    /// what it wrote before the pause is all in the open segment, as an interruption's cut has it).
    static func cutsOnFlush(_ state: State) -> Bool { state == .recording || state == .paused }

    /// A bookmark. In all-day recording it is a `mark` event on the wall clock; otherwise on the timeline.
    func addMark() {
        if allDay {
            addDayMark(RecordingDayEvent.Label.bookmark)
            return
        }
        #if DEBUG
        if writer == nil, let id = recordingID, sampleElapsed > 0 {
            let time = sampleElapsed
            RecordingLibrary.shared.change(id) { entry in
                if let last = entry.marks.last, time - last < 1 { return }
                entry.marks.append(time)
            }
            return
        }
        #endif
        guard let id = recordingID, let writer else { return }
        let time = writer.elapsed
        RecordingLibrary.shared.change(id) { entry in
            if let last = entry.marks.last, time - last < 1 { return }
            entry.marks.append(time)
        }
    }

    /// All-day recording: a meeting is going on now. The one microphone can't start a meeting recording beside the day's,
    /// so the day gets a wall-clock `mark` event the recap reads as a meeting. Nothing outside all-day recording.
    func markMeeting() {
        guard allDay else { return }
        addDayMark(RecordingDayEvent.Label.meeting)
    }

    /// Erases the last few minutes. A rolling recording erases them everywhere and drops the segment being written when it
    /// finalizes; all-day recording (on, or off with today's day still open) cuts first (`eraseDayRecent`).
    /// Returns false only when an all-day erase couldn't be saved (and so deletes nothing); its `error` says so too.
    @discardableResult func eraseRecent() -> Bool {
        if rolling, let id = recordingID {
            let now = Date.now
            RecordingLibrary.shared.erase(id, from: now.addingTimeInterval(-RecordingRolling.eraseMinutes * 60), to: now)
            return true
        }
        guard allDay || state == .idle else { return true }
        return eraseDayRecent()
    }

    /// `reason`: the user's Stop, or the system's (the audio service reset, a failed write, low storage or battery). An
    /// all-day recording's day stays open either way (`endDaySession`). The user's Stop while nothing records (all-day
    /// recording stopped by itself and not continued) still ends their wish to record all day, so the watch stops
    /// offering to continue.
    @discardableResult func stop(reason: RecordingStopReason = .user) -> UUID? {
        finishStop(reason == .user ? .user : .system)
    }

    /// How a recording ends: by the user, by the system, or because the phone deleted the day recording being recorded.
    private nonisolated enum Ending: Equatable { case user, system, deleted }

    @discardableResult private func finishStop(_ ending: Ending) -> UUID? {
        if state == .starting { cancelStarting(); return nil }
        #if DEBUG
        if writer == nil, let id = recordingID, sampleElapsed > 0 { return stopSample(id) }
        #endif
        guard let writer, let started = recordingID else {
            if ending != .system, state == .idle { Self.rememberIntent(false) }
            return nil
        }
        onStopping?(started)
        let microphoneOpen = state == .recording
        generation = UUID()
        resumeTask?.cancel(); resumeTask = nil
        ticker?.cancel(); ticker = nil
        writer.stop()
        // The uplink belongs to the recording that stopped (#3862 phase 5): the next one's writer never gets it.
        uplinkListener = nil
        // Everything finished that the main thread has not registered yet (a stop on a segment boundary leaves no tail; a
        // midnight may have been crossed), in order and each in its own recording, before the end is written.
        var changes = applyWriterEvents(notify: false)
        let id = recordingID ?? started
        if allDay {
            changes.insert(endDaySession(id, ending: ending, microphoneOpen: microphoneOpen, audioEnd: writer.audioEnd))
        } else {
            // A segment still waiting for its save is counted: the end never leaves out audio that exists.
            let expected = heldNextIndex(id)
            RecordingLibrary.shared.change(id) { entry in
                entry.active = false
                entry.expectedSegments = max(entry.nextIndex, expected)
                entry.endedAt = .now
            }
        }
        self.writer = nil; recordingID = nil; state = .idle; interrupted = false; notice = nil; rolling = false; playsAloud = false
        shortSegments = false; meetingCall = false; cancelsEcho = false
        allDay = false; dayPlan = nil
        forgetDayPlans()
        releaseAudioSession()
        announce(changes)
        onSaved?(id)
        return id
    }

    /// The launch halt, once: the watch's all-day controller takes it after installing `onDayHalted`.
    func takeHaltedOnLaunch() -> (id: UUID, reason: RecordingDayHalt)? {
        defer { haltedOnLaunch = nil }
        return haltedOnLaunch
    }

    private func releaseAudioSession() {
        #if os(iOS)
        try? AVAudioSession.sharedInstance().setPreferredInput(nil)
        #endif
        try? AVAudioSession.sharedInstance().setActive(false, options: .notifyOthersOnDeactivation)
        if let owner = activeMicOwner {
            activeMicOwner = nil
            RecordingMicrophone.shared.release(owner)
        }
    }

    private func startTicker() {
        ticker = Task { [weak self] in
            let clock = ContinuousClock()
            var checked = clock.now
            while !Task.isCancelled {
                let interval = self.map { Self.tick(allDay: $0.allDay, screen: $0.screen).interval } ?? Self.foregroundTick
                do { try await Task.sleep(for: interval) } catch { return }
                guard let self, let writer = self.writer else { return }
                if Self.tick(allDay: self.allDay, screen: self.screen).showsClock { self.elapsed = writer.elapsed }
                writer.segmentLength = self.chunkLength
                // A save that failed gets one more try; still failing, it halts as any failed write does (the stop tries again).
                if self.hasUnsaved { self.applyWriterEvents() }
                if let failure = writer.failure ?? RecordingLibrary.shared.error ?? self.registrationFailure {
                    self.haltBySystem(.writeFailed, error: failure); return
                }
                if checked.duration(to: clock.now) >= .seconds(5) {
                    checked = clock.now
                    if let problem = Self.resourceProblem(starting: false) { self.haltBySystem(problem.halt, error: problem.message); return }
                }
            }
        }
    }

    /// Finished audio or control events still not saved after another try.
    private var registrationFailure: String? {
        hasUnsaved ? Self.notSavedMessage : nil
    }

    private var hasUnsaved: Bool { !unregistered.isEmpty || !owedEvents.isEmpty }

    /// What the user reads when a recording's info, a chunk's record or a control event could not be written.
    private static var notSavedMessage: String {
        String(localized: "The recording info wasn't saved. Keep the app installed and try again.")
    }

    /// Where the app is, for the recording's clock: in front; on screen but not in front (dimmed in Always On with the
    /// wrist down, a banner or Control Center over it), where the clock still shows; or off screen.
    nonisolated enum Screen: Equatable, Sendable { case front, dimmed, hidden }

    /// What the ticker does next: how long it waits, and whether it writes `elapsed`.
    nonisolated struct Tick: Equatable, Sendable {
        var interval: Duration
        var showsClock: Bool
    }

    private nonisolated static let foregroundTick = Duration.milliseconds(250)

    /// Every recording ticks four times a second and writes its clock wherever it can be seen, dimmed included. Off
    /// screen nothing shows `elapsed`, so it isn't written (each write redraws whatever reads it), and all-day recording,
    /// hours there, ticks once a second with the failure checks only.
    nonisolated static func tick(allDay: Bool, screen: Screen) -> Tick {
        Tick(interval: allDay && screen == .hidden ? .seconds(1) : foregroundTick, showsClock: screen != .hidden)
    }

    /// Where the app is now; the phone keeps its timing as it was (always in front for the ticker).
    private var screen: Screen {
        #if os(watchOS)
        switch WKApplication.shared().applicationState {
        case .active: .front
        case .inactive: .dimmed
        case .background: .hidden
        @unknown default: .front
        }
        #else
        .front
        #endif
    }

    /// The system ended the recording: what was finished is saved, an all-day recording stays on (to continue), and the
    /// watch is told why.
    private func haltBySystem(_ reason: RecordingDayHalt, error message: String) {
        let day = allDay
        stop(reason: .system)
        error = message
        if day { onDayHalted?(reason) }
    }

    // MARK: - Audio session events

    private func handle(_ event: RecordingSessionEvent) {
        switch Self.reaction(to: event, recording: state == .recording && writer != nil, pausedByInterruption: interrupted && state == .paused) {
        case .pause: interruptionBegan()
        case .resume: resumeAfterInterruption()
        case .ignore: break
        }
    }

    /// What a recording does with an audio session event: an interruption pauses a running recording, and its end resumes
    /// one it paused, whatever the system advises (the meeting goes on). Our own deactivation and activation, which the
    /// 27 messages report too, are not interruptions.
    nonisolated enum SessionReaction: Equatable, Sendable { case ignore, pause, resume }

    nonisolated static func reaction(to event: RecordingSessionEvent, recording: Bool, pausedByInterruption: Bool) -> SessionReaction {
        switch event {
        case .interrupted: recording ? .pause : .ignore
        case .interruptionEnded: pausedByInterruption ? .resume : .ignore
        case .deactivatedByApp, .activated: .ignore
        }
    }

    private func interruptionBegan() {
        guard state == .recording, let writer else { return }
        interrupted = true
        resumeTask?.cancel(); resumeTask = nil
        writer.pause(); state = .paused
        // A long call: what was said before it is kept even if the app is stopped before the call ends (a segment
        // still open would be deleted by `recover`). The next segment starts when the recording resumes.
        if allDay {
            pauseDay(reason: RecordingDayContract.Reason.interrupted)
            onDayHalted?(.interrupted)
        } else {
            writer.cut()
            applyWriterEvents()
        }
        if rolling {
            error = String(localized: "Rolling recording paused — tap Continue to resume")
            #if os(watchOS)
            WKInterfaceDevice.current().play(.notification)
            #endif
        }
    }

    /// The input changed and the tap could not be made again: often the route switching to a call before its
    /// interruption arrives, so it pauses as one does, and the call's end (or coming back to the app) picks it up again.
    private func microphoneChangeFailed() {
        guard let writer else { return }
        error = String(localized: "The microphone changed, so recording paused")
        writer.pause(); state = .paused; interrupted = true
        if allDay {
            pauseDay(reason: RecordingDayContract.Reason.interrupted)
            onDayHalted?(.microphoneChanged)
        } else {
            writer.cut()
            applyWriterEvents()
        }
    }

    // MARK: - Registering what the writer did

    /// The writer's callbacks only wake the main thread, in the order they happened (one serial queue for both kinds);
    /// what they report is taken from the writer's log there, so a stop or a cut that runs first leaves nothing to do
    /// twice, and a late wake-up after a stop finds nothing.
    private func connect(_ writer: RecordingSegmentWriter) {
        let wake: @Sendable () -> Void = { [weak self, weak writer] in
            let capture = self, source = writer
            DispatchQueue.main.async { MainActor.assumeIsolated { capture?.writerReported(source) } }
        }
        writer.onSegment = { @Sendable _ in wake() }
        writer.onRotate = { @Sendable _, _ in wake() }
    }

    private func writerReported(_ source: RecordingSegmentWriter?) {
        guard let source, source === writer else { return }
        applyWriterEvents()
    }

    /// Registers what the writer did since the last call, in order: each segment in the recording its folder names (never
    /// `recordingID`, which a midnight moves on), each midnight closing one day and opening the next; then the control
    /// events still owed. Every path that registers segments comes here: the writer's wake-ups, cuts, pauses, erases,
    /// the day check, start and stop. What could not be saved is kept with everything after it (`unregistered`) and goes
    /// first next time; only what was saved is announced.
    /// - Parameter notify: false while more is to be saved first (an erase, a stop), so nothing goes out half done.
    @discardableResult
    private func applyWriterEvents(notify: Bool = true) -> Changes {
        let fresh = writer.map { source in source.takeEvents().map { HeldEvent(event: $0, source: source) } } ?? []
        var changes = Changes()
        let (changed, kept) = Self.applyInOrder(unregistered + fresh, owners: \.event.owners) { held in
            let saved = apply(held)
            if let saved, let owner = held.event.segment?.owner, saved.contains(owner) { changes.chunks.insert(owner) }
            return saved
        }
        unregistered = kept
        changes.insert(changed)
        changes.insert(flushEvents())
        if notify { announce(changes) }
        return changes
    }

    /// One event of the writer's: the recordings it changed, or nil when it could not be saved.
    private func apply(_ held: HeldEvent) -> [UUID]? {
        switch held.event {
        case .segment(let segment):
            return register(segment)
        case .rotated(let rotation):
            // A midnight from a writer that is gone still files both days, but moves and arms nothing.
            let live = held.source != nil && held.source === writer
            return crossedMidnight(rotation, live: live)
        }
    }

    /// Applies items in order. One that can't be saved is kept for the next try, in order, with every later one that
    /// concerns any of its recordings (`owners`: a midnight concerns both days), so a midnight is never applied ahead of
    /// a chunk before it, nor a new day's chunk or event ahead of its midnight. Other recordings go on. Returns the
    /// recordings changed and what was kept.
    nonisolated static func applyInOrder<Item>(_ items: [Item], owners: (Item) -> [UUID],
                                               _ apply: (Item) -> [UUID]?) -> (changed: [UUID], kept: [Item]) {
        var changed: [UUID] = [], kept: [Item] = [], waiting: Set<UUID> = []
        for item in items {
            let concerned = owners(item)
            guard waiting.isDisjoint(with: concerned), let ids = apply(item) else {
                kept.append(item)
                waiting.formUnion(concerned)
                continue
            }
            for id in ids where !changed.contains(id) { changed.append(id) }
        }
        return (changed, kept)
    }

    /// One finished segment, saved in the recording its folder belongs to; a day chunk carries its level.
    /// - Returns: the recording changed; empty when there is none to keep it in (deleted meanwhile); nil when the save
    ///   failed, so it is tried again.
    private func register(_ segment: RecordingSegmentWriter.Segment) -> [UUID]? {
        guard let id = segment.owner else { return [] }
        let library = RecordingLibrary.shared
        if library.entry(id) == nil, !library.isDeleted(id), !ensureDayEntry(id), pendingDays[id] != nil {
            return nil // the day named in advance could not be made yet
        }
        guard let entry = library.entry(id) else {
            // Deleted meanwhile, or a folder no recording names: there is nothing to keep it in.
            try? FileManager.default.removeItem(at: segment.url)
            return []
        }
        var record = RecordingSegment(index: segment.index, start: segment.start, duration: segment.duration, startedAt: segment.startedAt)
        if entry.isDay { record.level = segment.level }
        library.addSegment(id, record)
        // `addSegment` reports nothing: whether the record is there now tells a failed save from a done one.
        guard Self.isSettled(record, in: library.entry(id)) else { return nil }
        return [id]
    }

    /// Whether the library has a finished segment now: its record is there, or it was dropped on purpose (a seq
    /// tombstoned: a day chunk acknowledged, erased or reported lost, a pruned rolling one; a clip's audio in a range
    /// the user erased). Anything else is a save that failed.
    nonisolated static func isSettled(_ record: RecordingSegment, in entry: RecordingEntry?) -> Bool {
        guard let entry else { return false }
        if entry.segments.contains(where: { $0.index == record.index }) || entry.isPruned(record.index) { return true }
        return !entry.isDay && entry.isErased(record)
    }

    /// The seq after the newest segment of `id` still waiting for its save (0 when none).
    private func heldNextIndex(_ id: UUID) -> Int {
        unregistered.compactMap { $0.event.segment }.filter { $0.owner == id }.map { $0.index + 1 }.max() ?? 0
    }

    /// After a stop: the day's plans go, except what still waits for a save needs (a day named in advance).
    private func forgetDayPlans() {
        let owners = Set(unregistered.flatMap(\.event.owners) + owedEvents.map(\.recording))
        pendingDays = pendingDays.filter { owners.contains($0.key) }
    }

    private func announce(_ changes: Changes) {
        let library = RecordingLibrary.shared
        for id in changes.ids {
            onSaved?(id)
            if changes.chunks.contains(id), library.entry(id)?.isDay == true { onDayChunk?(id) }
        }
    }

    // MARK: - Control events

    /// Adds a control event to a day recording, after every event still owed to it, so they keep their order
    /// (docs/assistant/day-recap.md「Control events」); the library numbers it (`n` here is a placeholder). One that can't
    /// be saved now stays owed and is tried again with the next change. Returns the recordings that got events.
    @discardableResult private func record(_ id: UUID, _ event: RecordingDayEvent) -> [UUID] {
        owedEvents.append(OwedEvent(recording: id, event: event))
        return flushEvents()
    }

    /// Saves the owed events in order. An event of a recording that is gone (deleted, or finished and removed) is
    /// dropped: there is nobody left to tell. One of a day named in advance waits until that day is made.
    private func flushEvents() -> [UUID] {
        guard !owedEvents.isEmpty else { return [] }
        let library = RecordingLibrary.shared
        let (changed, kept) = Self.applyInOrder(owedEvents, owners: { [$0.recording] }) { owed in
            guard library.entry(owed.recording) != nil else {
                return pendingDays[owed.recording] == nil || library.isDeleted(owed.recording) ? [] : nil
            }
            return library.appendDayEvent(owed.recording) { _ in owed.event } == nil ? nil : [owed.recording]
        }
        owedEvents = kept
        return changed
    }

    // MARK: - All-day recording

    /// The day recording a microphone opening at `now` goes on in (docs/assistant/day-recap.md「The day recording on the
    /// watch」): the open one whose day is still on in its own time zone, else a new one for today in the zone the watch
    /// is in now, made with its date and counters before the microphone opens. Open days already over, each in its own
    /// zone, are closed at their midnight first; `left` (the one a switch leaves) is the switch's to close.
    private func openDay(locale: String, now: Date, excluding left: UUID? = nil) throws -> RecordingEntry {
        let library = RecordingLibrary.shared
        let plan = Self.dayToOpen(library.openDays(source: Self.source).filter { $0.id != left }, now: now)
        var closed = Changes()
        for over in plan.closing {
            if closeDay(over.id, at: over.at) { closed.insert(over.id) }
        }
        announce(closed)
        if let open = plan.goingOn { return open }
        let zone = TimeZone.current
        return try library.create(source: Self.source, locale: locale, kind: .day, createdAt: now,
                                  day: RecordingDayCalendar.day(of: now, in: zone), timeZone: zone.identifier)
    }

    /// Which of the open day recordings a microphone opening at `now` goes on in, the newest whose day is still on in its
    /// own zone and has begun, and which it closes: those whose day is over there, each at its own midnight (now, for one
    /// whose date or zone can't be read). A zone change alone never ends a day: a recording made in Shanghai goes on
    /// until Shanghai's midnight, wherever the watch is.
    nonisolated static func dayToOpen(_ open: [RecordingEntry], now: Date) -> (goingOn: RecordingEntry?, closing: [(id: UUID, at: Date)]) {
        var closing: [(id: UUID, at: Date)] = []
        var goingOn: RecordingEntry?
        for entry in open.sorted(by: { $0.createdAt > $1.createdAt }) {
            let end = dayEnd(of: entry)
            guard let end, now < end else {
                closing.append((entry.id, min(end ?? now, now)))
                continue
            }
            if goingOn == nil, entry.createdAt <= now { goingOn = entry }
        }
        return (goingOn, closing)
    }

    /// The microphone opened in a day recording (a start): all-day is on, also after a relaunch, and the phone learns when
    /// the audio began.
    private func beganDay(_ id: UUID, at opening: Date) {
        allDay = true
        Self.rememberIntent(true)
        announce(Changes(record(id, .started(0, at: opening))))
    }

    /// The microphone opened again within the day (a continue): a `started` event, and the watch withdraws its "paused"
    /// alert.
    private func dayResumed(at opening: Date) {
        if let id = recordingID { announce(Changes(record(id, .started(0, at: opening)))) }
        onDayResumed?()
    }

    /// The microphone closed within the day: what was recorded so far becomes a chunk (an app ended before it opens again
    /// keeps it, and it reaches the phone meanwhile), and a `paused` event says why, from where the audio ends.
    private func pauseDay(reason: String) {
        writer?.cut()
        var changes = applyWriterEvents(notify: false)
        // After the drain: a midnight the writer crossed just before has moved `recordingID` to the new day.
        if let id = recordingID {
            changes.insert(record(id, .paused(0, at: Self.pauseTime(audioEnd: writer?.audioEnd, now: .now), reason: reason)))
        }
        announce(changes)
    }

    /// When the microphone closed: where the audio before the cut ends, never later than now.
    nonisolated static func pauseTime(audioEnd: Date?, now: Date) -> Date {
        min(audioEnd ?? now, now)
    }

    /// The microphone goes off and the day stays open, to continue later today. The chunks the writer could not finish
    /// are reported lost; a `paused` event follows when the microphone was open (the user's Stop is a pause for the rest
    /// of today, the system's is `stopped`; a pause or a call already said it). The user's Stop also ends their wish to
    /// record all day. A recording the phone deleted gets nothing: it is gone.
    private func endDaySession(_ id: UUID, ending: Ending, microphoneOpen: Bool, audioEnd: Date?) -> [UUID] {
        if ending != .system { Self.rememberIntent(false) }
        guard ending != .deleted else { return [] }
        var changed = reportHoles(id)
        if microphoneOpen {
            let reason = ending == .user ? RecordingDayContract.Reason.paused : RecordingDayContract.Reason.stopped
            changed += record(id, .paused(0, at: Self.pauseTime(audioEnd: audioEnd, now: .now), reason: reason))
        }
        return changed
    }

    /// Arms the writer for the midnight that ends a day recording's day in its own time zone, naming the recording that
    /// follows (made when the first sample past midnight arrives). `next`: keep a name already given.
    private func planBoundary(for id: UUID, writer: RecordingSegmentWriter, next: UUID = UUID()) {
        let library = RecordingLibrary.shared
        let entry = library.entry(id), pending = pendingDays[id]
        guard let day = entry?.day ?? pending?.day,
              let zone = (entry?.timeZone ?? pending?.timeZone).flatMap(RecordingDayWire.zone),
              let boundary = RecordingDayCalendar.end(of: day, in: zone) else { dayPlan = nil; return }
        writer.setBoundary(at: boundary, nextDirectory: library.folder(next))
        dayPlan = DayPlan(current: id, next: next, boundary: boundary,
                          locale: entry?.locale ?? pending?.locale ?? RecordingDefaults.locale)
    }

    /// A midnight the writer crossed while recording: the old day ends with the chunk that reaches it, after the chunks
    /// it could not finish are reported lost, with its `closed` event at the midnight. The next day's recording, named in
    /// advance, is made as of its first sample, in the zone the watch is in now (its own from then on), and gets its
    /// `started` event there. Safe to apply again: until the old day is saved as closed it returns nil and is tried
    /// again, in order. `live`: from the writer recording now, which then goes on in the new day.
    private func crossedMidnight(_ rotation: RecordingSegmentWriter.Rotation, live: Bool) -> [UUID]? {
        guard let old = UUID(uuidString: rotation.previous.lastPathComponent),
              let new = UUID(uuidString: rotation.next.lastPathComponent) else { return [] }
        let library = RecordingLibrary.shared
        let previous = library.entry(old)
        var changed: [UUID] = []
        if let last = rotation.last {
            guard let saved = register(last) else { return nil }
            changed += saved
        }
        if previous != nil {
            changed += reportHoles(old)
            guard closeDay(old, at: rotation.boundary) else { return nil }
            changed.append(old)
        }
        pendingDays[old] = nil
        if library.entry(new) == nil, pendingDays[new] == nil {
            let zone = TimeZone.current
            pendingDays[new] = PendingDay(day: RecordingDayCalendar.day(of: rotation.firstSampleAt, in: zone),
                                          createdAt: rotation.firstSampleAt, timeZone: zone.identifier,
                                          locale: previous?.locale ?? RecordingDefaults.locale)
        }
        // Its first chunk tries again when this fails, and is kept until the entry exists; so is the event.
        ensureDayEntry(new)
        changed += record(new, .started(0, at: rotation.firstSampleAt))
        if live, let writer {
            recordingID = new
            planBoundary(for: new, writer: writer)
        }
        return changed
    }

    /// Closes a day recording at `at` with its `closed` event (`RecordingLibrary.closeDay`). True once it is closed (also
    /// when it already was, or is gone); false when that could not be saved.
    @discardableResult private func closeDay(_ id: UUID, at: Date) -> Bool {
        let library = RecordingLibrary.shared
        guard let entry = library.entry(id) else { return true }
        if entry.closedAt == nil { library.closeDay(id, at: at) }
        return library.entry(id)?.closedAt != nil
    }

    /// Reports the seqs of a day recording the writer handed out and that hold no chunk (a file that could not be opened
    /// or written) as `lost`, so the phone does not wait for them. Only once the writer has left the recording, when
    /// every other seq handed out there is registered or waiting for its save.
    private func reportHoles(_ id: UUID) -> [UUID] {
        let library = RecordingLibrary.shared
        guard let entry = library.entry(id), entry.isDay else { return [] }
        let held = Set(unregistered.compactMap(\.event.segment).filter { $0.owner == id }.map(\.index))
        let holes = Self.holes(in: entry, allocated: library.allocatedSeqs(id), held: held)
        guard !holes.isEmpty else { return [] }
        return library.loseDayChunks(id, seqs: holes).isEmpty ? [] : [id]
    }

    /// The seqs below the counter (`allocated`, or the entry's own when higher) with no record, no tombstone (acknowledged,
    /// erased or lost already) and no chunk waiting for its save (`held`).
    nonisolated static func holes(in entry: RecordingEntry, allocated: Int, held: Set<Int>) -> [Int] {
        let registered = Set(entry.segments.map(\.index))
        return (0..<max(allocated, entry.nextIndex)).filter { !registered.contains($0) && !held.contains($0) && !entry.isPruned($0) }
    }

    /// Makes a day recording named in advance once something needs it (the first chunk past midnight, a mark just after
    /// it); tried again with the next chunk when saving it failed.
    @discardableResult
    private func ensureDayEntry(_ id: UUID) -> Bool {
        let library = RecordingLibrary.shared
        guard library.entry(id) == nil else { pendingDays[id] = nil; return true }
        guard let pending = pendingDays[id] else { return false }
        do {
            try library.create(source: Self.source, locale: pending.locale, kind: .day, id: id, createdAt: pending.createdAt,
                               day: pending.day, timeZone: pending.timeZone)
            pendingDays[id] = nil
            return true
        } catch {
            self.error = Self.notSavedMessage
            return false
        }
    }

    /// The date, the clock or the time zone changed. A day recording keeps the zone it began in, so a new zone only
    /// counts for the next one; a day over by now while the microphone is off is closed (a running writer cuts at its
    /// own midnight).
    private func dayClockChanged() {
        NSTimeZone.resetSystemTimeZone()
        reconcileDay(opening: false)
    }

    /// Keeps all-day recording in a day recording whose day is still on in its own zone: before the microphone opens
    /// again (`opening`), and when the date or the clock changes. What the writer already did is registered first, so a
    /// midnight it crossed is never undone. A day over while the microphone is off is only closed, at its midnight: the
    /// next one is opened when the microphone opens, so a pause through the night leaves no empty day behind.
    private func reconcileDay(opening: Bool, now: Date = .now) {
        guard allDay, let writer else { return }
        applyWriterEvents()
        guard let id = recordingID else { return }
        guard let entry = RecordingLibrary.shared.entry(id), let day = entry.day else {
            // Gone under a paused writer (closed and finished by the transfer): the microphone goes on in today's.
            if opening || writer.isRunning { switchDay(from: nil, now: now, writer: writer) }
            return
        }
        let planned = dayPlan.flatMap { $0.current == id ? $0.boundary : nil }
        switch Self.dayCheck(day: day, zone: entry.dayTimeZone ?? .current, closed: entry.closedAt != nil, now: now,
                             running: writer.isRunning, plannedBoundary: planned) {
        case .keep(let boundary):
            guard planned != boundary else { return }
            planBoundary(for: id, writer: writer, next: dayPlan.flatMap { $0.current == id ? $0.next : nil } ?? UUID())
        case .awaitWriter:
            break
        case .switchNow:
            if opening || writer.isRunning {
                switchDay(from: entry, now: now, writer: writer)
            } else if entry.closedAt == nil, closeDay(id, at: Self.switchedDayEnd(of: entry, now: now, cut: nil)) {
                announce(Changes([id]))
            }
        }
    }

    /// Before the microphone opens again: the day check, then whether the writer's folder is a day recording still taking
    /// audio (a new one may not have been made for today when the old one ended).
    private func dayTakesAudio(now: Date = .now) -> Bool {
        reconcileDay(opening: true, now: now)
        guard let id = recordingID, let entry = RecordingLibrary.shared.entry(id), entry.closedAt == nil,
              let end = Self.dayEnd(of: entry) else { return false }
        return now < end
    }

    /// All-day recording moves to a day recording for today now (the microphone is open, or about to open): the open one
    /// whose day is still on, else a new one starting now. The day left behind ends at its own midnight, never before the
    /// end of the chunk the switch cut from it, after the chunks it could not finish are reported lost.
    private func switchDay(from old: RecordingEntry?, now: Date, writer: RecordingSegmentWriter) {
        let library = RecordingLibrary.shared
        let target: RecordingEntry
        do {
            target = try openDay(locale: old?.locale ?? RecordingDefaults.locale, now: now, excluding: old?.id)
        } catch {
            self.error = Self.notSavedMessage
            return
        }
        let running = writer.isRunning
        let cut = writer.switchDirectory(to: library.folder(target.id))
        // The chunk the switch cut is filed in the old day, before its `closed` event.
        var changes = applyWriterEvents(notify: false)
        if let old {
            changes.insert(reportHoles(old.id))
            if closeDay(old.id, at: Self.switchedDayEnd(of: old, now: now, cut: cut)) { changes.insert(old.id) }
        }
        recordingID = target.id
        planBoundary(for: target.id, writer: writer)
        if running { changes.insert(record(target.id, .started(0, at: cut?.endedAt ?? now))) }
        announce(changes)
    }

    /// When a day left behind ends: its own midnight, or now when that is still ahead or unreadable, but never before the
    /// end of the chunk a switch cut from it (buffers still queued at `now` land in that chunk, so it can end a moment
    /// later).
    private static func switchedDayEnd(of old: RecordingEntry, now: Date, cut: RecordingSegmentWriter.Segment?) -> Date {
        let end = min(dayEnd(of: old) ?? now, now)
        guard let cut, cut.owner == old.id else { return end }
        return max(end, cut.endedAt)
    }

    /// A bookmark or meeting mark: a `mark` event at now [ext], in the day recording now belongs to. A second tap of the
    /// same kind within a second adds nothing.
    private func addDayMark(_ label: String) {
        let now = Date.now
        if let last = lastMarks[label], abs(now.timeIntervalSince(last)) < 1 { return }
        guard let id = dayRecording(at: now) else { return }
        lastMarks[label] = now
        announce(Changes(record(id, .mark(0, at: now, label: label))))
    }

    /// The day recording a moment belongs to: today's after the day check, or, a moment past midnight before the writer
    /// reached its first sample there, the one named for after it (made now if it isn't yet).
    private func dayRecording(at now: Date) -> UUID? {
        reconcileDay(opening: true, now: now)
        guard let id = recordingID else { return nil }
        guard writer?.isRunning == true, let plan = dayPlan, plan.current == id, now >= plan.boundary else { return id }
        if RecordingLibrary.shared.entry(plan.next) == nil, pendingDays[plan.next] == nil {
            let zone = TimeZone.current
            pendingDays[plan.next] = PendingDay(day: RecordingDayCalendar.day(of: plan.boundary, in: zone), createdAt: plan.boundary,
                                                timeZone: zone.identifier, locale: plan.locale)
        }
        // When it can't be made yet, the event waits for it (`flushEvents`).
        ensureDayEntry(plan.next)
        return plan.next
    }

    /// "Erase the last 10 minutes" on the watch (docs/assistant/day-recap.md「Erasing」). The chunk being written is cut
    /// first and registered, so it is erased with the rest rather than finishing later inside the range. Every day
    /// recording the window touches (both days, just after midnight) then gets an `erase` event and loses the chunks it
    /// still holds there; nothing is sent before all of it is saved.
    ///
    /// The range ends exactly at the writer's cut (`cutForErase`), never at a later clock reading: the chunk after the cut
    /// starts there, so the phone keeps all of its audio however long the saves take.
    @discardableResult private func eraseDayRecent() -> Bool {
        var saved = true
        let now = Date.now
        let cut = writer?.cutForErase()
        var changes = applyWriterEvents(notify: false)
        let library = RecordingLibrary.shared
        // No writer (stopped for today): nothing records now, and the next start comes after now.
        if let window = Self.eraseWindow(now: now, audioEnd: writer == nil ? now : cut?.through) {
            for entry in Self.daysTouched(by: window, in: library.entries, source: Self.source) {
                guard Self.erase(window, ofDay: entry.id, in: library) else {
                    error = Self.notSavedMessage
                    saved = false
                    continue
                }
                changes.insert(entry.id)
            }
        }
        announce(changes)
        return saved
    }

    /// The range "Erase the last 10 minutes" erases at `now`: up to where the audio before the cut ends (`audioEnd`),
    /// never later. nil when nothing was recorded in it (paused for longer than the window).
    nonisolated static func eraseWindow(now: Date, audioEnd: Date?) -> RecordingErase? {
        guard let audioEnd else { return nil }
        let window = RecordingErase(from: now.addingTimeInterval(-RecordingRolling.eraseMinutes * 60), to: min(audioEnd, now))
        return window.from < window.to ? window : nil
    }

    /// This device's day recordings whose span reaches into `range`: today's, and the day before when the range crosses
    /// its midnight.
    nonisolated static func daysTouched(by range: RecordingErase, in entries: [RecordingEntry], source: RecordingEntry.Source) -> [RecordingEntry] {
        entries.filter { $0.isDay && $0.source == source && $0.createdAt < range.to && ($0.closedAt ?? .distantFuture) > range.from }
    }

    /// Erases `range` from a day recording on the watch: the `erase` event first, naming every chunk the erase deletes
    /// before the phone acknowledged it, then those chunks. In that order an exit in between leaves chunks the phone drops
    /// on arrival (they fall in the erased range), never deleted chunks it is not told about. False when the event could
    /// not be saved; nothing is deleted then.
    @discardableResult
    static func erase(_ range: RecordingErase, ofDay id: UUID, in library: RecordingLibrary, eventID: UUID = UUID()) -> Bool {
        guard let entry = library.entry(id), entry.isDay else { return false }
        let seqs = entry.segments.filter { $0.overlapsDay(range.from, range.to) }.map(\.index)
        guard library.appendDayEvent(id, { .erase($0, id: eventID, from: range.from, to: range.to, seqs: seqs) }) != nil else { return false }
        library.eraseDayChunks(id, from: range.from, to: range.to)
        return true
    }

    // MARK: - The phone's requests

    /// The phone erased a range of a day recording and asks the watch to erase its copies (docs/assistant/day-recap.md
    /// 「Erasing」), through `WatchSync.onDayEraseRequest`'s handler. The chunk being written is cut first when the range
    /// reaches into it, then every chunk held in the range goes, the request's id and seqs saved in the same write; a
    /// repeated request gets the same answer and erases nothing again. nil when the erase could not be saved, to leave
    /// it to the phone's resend.
    @discardableResult func onDayEraseRequest(_ request: RecordingDayEraseRequest) -> RecordingDayErased? {
        let library = RecordingLibrary.shared
        let id = request.recording
        let repeated = library.entry(id)?.dayErases?.contains { $0.id == request.erase.id } == true
        // The writer tells whether its open chunk is this recording's, not `recordingID`: just after a midnight that
        // still names the day before, until the rotation is applied here. A cut chunk is registered just below and
        // erased with the rest.
        if !repeated { writer?.cutOpenChunk(of: id, startedBefore: request.erase.to) }
        var changes = applyWriterEvents(notify: false)
        let answer = Self.carryOut(request, in: library)
        if answer != nil, !repeated, library.entry(id) != nil { changes.insert(id) }
        announce(changes)
        return answer
    }

    /// The watch's side of a phone erase, once every chunk in the range is registered: the answer recorded for this
    /// request's id, else the chunks held in the range erased with the answer saved in the same write. A recording the
    /// watch no longer holds has nothing to erase, now or on a repeat. nil when the save failed.
    static func carryOut(_ request: RecordingDayEraseRequest, in library: RecordingLibrary, now: Date = .now) -> RecordingDayErased? {
        let id = request.recording, erase = request.erase
        guard let entry = library.entry(id), entry.isDay else { return RecordingDayErased(recording: id, erased: erase.id, seqs: []) }
        if let done = entry.dayErases?.first(where: { $0.id == erase.id }) { return RecordingDayErased(recording: id, done: done) }
        library.eraseDayChunks(id, from: erase.from, to: erase.to, request: erase.id, now: now)
        return library.entry(id)?.dayErases?.first { $0.id == erase.id }.map { RecordingDayErased(recording: id, done: $0) }
    }

    /// The phone deleted a day recording (docs/assistant/day-recap.md「Deleting」), through `WatchSync.onDayDelete`'s
    /// handler: every chunk of it and its entry go here too, leaving a tombstone. When all-day recording is on in it,
    /// all-day recording stops first (no event: the recording is gone) and `onDayDeletedRemotely` says so; a start later
    /// that day opens a new recording. Returns the confirmation once the copy is gone, nil to leave it to the phone's
    /// resend (the tombstone could not be written).
    @discardableResult func onDayDelete(_ request: RecordingDayDelete) -> RecordingDayDeleted? {
        let id = request.recording
        // A midnight the writer crossed and main has not applied yet may have moved all-day recording on already.
        if writer != nil { applyWriterEvents() }
        let recordingIt = allDay && recordingID == id
        if recordingIt { finishStop(.deleted) }
        forgetDay(id)
        let removed = (try? RecordingLibrary.shared.remove(id)) != nil
        if recordingIt { onDayDeletedRemotely?(id) }
        guard removed else { return nil }
        onSaved?(id)
        return RecordingDayDeleted(recording: id)
    }

    /// Drops what capture still means to do with a day recording that is going: its owed events, its plan as a day named
    /// in advance, and that name for the writer's next midnight (the next day gets a fresh one).
    private func forgetDay(_ id: UUID) {
        owedEvents.removeAll { $0.recording == id }
        pendingDays[id] = nil
        if dayPlan?.next == id, let writer, let current = recordingID { planBoundary(for: current, writer: writer) }
    }

    // MARK: - Day rules

    /// What a day recording does when the date may have changed under it.
    nonisolated enum DayCheck: Equatable, Sendable {
        /// Still the recording's day in its zone: the writer cuts at this midnight.
        case keep(boundary: Date)
        /// Its midnight has passed and the running writer cuts there with its next buffer: leave it, so the cut stays
        /// exact to the sample.
        case awaitWriter
        /// Its day is over (or it was closed) and nothing will cut it: move on to today's recording.
        case switchNow
    }

    /// The day check: `day` and `zone` are the recording's own, `plannedBoundary` the midnight the writer is armed with.
    /// Only the recording's zone counts, so flying east or west moves nothing; a clock moved on, or a pause past midnight,
    /// switches.
    nonisolated static func dayCheck(day: String, zone: TimeZone, closed: Bool, now: Date, running: Bool, plannedBoundary: Date?) -> DayCheck {
        guard !closed, let end = RecordingDayCalendar.end(of: day, in: zone) else { return .switchNow }
        if now < end { return .keep(boundary: end) }
        if running, plannedBoundary == end { return .awaitWriter }
        return .switchNow
    }

    /// The midnight that ends a day recording's day in its own time zone (a 23- or 25-hour day across a DST change ends
    /// where it does); nil when its date or zone can't be read.
    nonisolated static func dayEnd(of entry: RecordingEntry) -> Date? {
        guard let day = entry.day, let zone = entry.timeZone.flatMap(RecordingDayWire.zone) else { return nil }
        return RecordingDayCalendar.end(of: day, in: zone)
    }

    /// The day recording to report as halted at launch: the newest of those `recover` left open, when the user wanted
    /// all-day on (an open day with all-day off is one the user stopped for today).
    nonisolated static func launchHalt(recovered: [UUID], entries: [RecordingEntry], intent: Bool) -> (id: UUID, reason: RecordingDayHalt)? {
        guard intent, !recovered.isEmpty else { return nil }
        let open = Set(recovered)
        guard let newest = entries.filter({ $0.isDay && open.contains($0.id) }).max(by: { $0.createdAt < $1.createdAt }) else { return nil }
        return (id: newest.id, reason: RecordingDayHalt.crashed)
    }

    private static func rememberIntent(_ on: Bool) {
        // Changes only on start and Stop; written only when it does (every write redraws the views reading defaults).
        let defaults = UserDefaults.standard
        if defaults.bool(forKey: allDayIntentKey) != on { defaults.set(on, forKey: allDayIntentKey) }
    }

    private nonisolated struct DayPlan {
        let current: UUID
        let next: UUID
        let boundary: Date
        let locale: String
    }

    private nonisolated struct PendingDay {
        let day: String
        let createdAt: Date
        let timeZone: String
        let locale: String
    }

    /// A writer event taken from its log and not saved yet, with the writer it came from (weak: a stopped writer goes).
    private nonisolated struct HeldEvent {
        let event: RecordingSegmentWriter.Event
        weak var source: RecordingSegmentWriter?
    }

    /// A control event not saved yet, for its recording.
    private nonisolated struct OwedEvent {
        let recording: UUID
        let event: RecordingDayEvent
    }

    /// What a pass saved: the recordings changed, in order, and those of them that got a chunk.
    private nonisolated struct Changes {
        var ids: [UUID] = []
        var chunks: Set<UUID> = []

        init(_ ids: [UUID] = []) { insert(ids) }

        mutating func insert(_ id: UUID) { if !ids.contains(id) { ids.append(id) } }
        mutating func insert(_ more: [UUID]) { for id in more { insert(id) } }
    }

    #if DEBUG
    /// Screenshots (`-showMeetingSample`, `-showAllDaySample`): `id` shows as being recorded `elapsed` seconds in, paused
    /// or paused by a call, as an all-day recording or not, with no microphone (Stop ends it through `stopSample`).
    func showSample(_ id: UUID, elapsed: Double, paused: Bool = false, interrupted: Bool = false, allDay: Bool = false) {
        recordingID = id; self.elapsed = elapsed; sampleElapsed = elapsed
        self.interrupted = interrupted
        self.allDay = allDay
        state = paused || interrupted ? .paused : .recording
    }

    /// The DEBUG flow (`-showMeetingSample flow`): moves a sample recording's clock and level on.
    func advanceSample(elapsed: Double, level: Double) {
        guard writer == nil, state != .idle else { return }
        sampleElapsed = elapsed; self.elapsed = elapsed; sampleLevel = level
    }

    /// Stop for a sample recording: what `stop()` does for a real one after the microphone is closed (the entry ends; a
    /// sample day, which has no recording behind it, just stops; the same callbacks run), so the wrap-up steps advance
    /// as they would.
    private func stopSample(_ id: UUID) -> UUID {
        onStopping?(id)
        uplinkListener = nil
        if !allDay {
            RecordingLibrary.shared.change(id) { entry in
                entry.active = false
                entry.expectedSegments = entry.nextIndex
                entry.endedAt = .now
            }
        }
        recordingID = nil; state = .idle; interrupted = false; notice = nil; sampleElapsed = 0; sampleLevel = 0; allDay = false
        onSaved?(id)
        return id
    }
    #endif

    /// Chunk length now: an all-day recording's follows the phone link, every other recording's as before.
    private var chunkLength: Double { allDay ? Self.dayChunkLength(reachable: linkReachable) : Self.segmentLength(short: shortSegments) }

    private static func dayChunkLength(reachable: Bool) -> Double {
        reachable ? RecordingDayContract.liveChunkSeconds : RecordingDayContract.backlogChunkSeconds
    }

    /// A recording's segment length now (not all-day's): `short` (`shortSegments`) is 15 s whether the app is in front
    /// or not; else 30 s while someone may watch the text arrive (the phone's app in front, the watch's phone in reach)
    /// and 60 s otherwise.
    private static func segmentLength(short: Bool) -> Double {
        #if SHARE_EXTENSION
        RecordingAudioSpec.segmentSeconds // Share › Persona compiles Shared but never records (no UIApplication there)
        #elseif os(iOS) || os(visionOS)
        segmentLength(short: short, watched: UIApplication.shared.applicationState == .active)
        #else
        segmentLength(short: short, watched: WatchRecordingTransfer.shared.reachable)
        #endif
    }

    /// `segmentLength(short:)` with where the app is given (pure, for the tests).
    nonisolated static func segmentLength(short: Bool, watched: Bool) -> Double {
        if short { return RecordingAudioSpec.meetingLiveSegmentSeconds }
        return watched ? RecordingAudioSpec.liveSegmentSeconds : RecordingAudioSpec.segmentSeconds
    }

    /// What stops a recording to protect the device.
    private nonisolated enum ResourceProblem {
        case storage, battery

        var halt: RecordingDayHalt { self == .storage ? .lowStorage : .lowBattery }
        var message: String {
            switch self {
            case .storage: String(localized: "Not enough storage. Saved recordings won't be lost.")
            case .battery: String(localized: "Watch battery is too low. Saved recordings won't be lost.")
            }
        }
    }

    private static func resourceProblem(starting: Bool) -> ResourceProblem? {
        let attributes = try? FileManager.default.attributesOfFileSystem(forPath: NSHomeDirectory())
        let free = (attributes?[.systemFreeSize] as? NSNumber)?.int64Value
        if let free, free < (starting ? 300_000_000 : 150_000_000) { return .storage }
        #if os(watchOS)
        let device = WKInterfaceDevice.current(); device.isBatteryMonitoringEnabled = true
        if device.batteryState == .unplugged, device.batteryLevel >= 0, device.batteryLevel < 0.05 { return .battery }
        #endif
        return nil
    }
}
#endif

/// Why an all-day recording stopped recording without the user (`RecordingCapture.onDayHalted`), for what the watch
/// tells them. Outside the platform check above: the watch's alert policy, in `Shared/`, compiles for the Mac too.
nonisolated enum RecordingDayHalt: String, Sendable {
    /// A call, Siri or another app took the microphone.
    case interrupted
    /// The microphone could not be opened again (the app was in the background when the interruption ended).
    case resumeFailed
    /// The input changed and could not be reopened.
    case microphoneChanged
    /// The system audio service reset.
    case audioReset
    case lowBattery
    case lowStorage
    /// A chunk, a control event or the recording's info could not be written.
    case writeFailed
    /// The app ended while recording (found at the next launch).
    case crashed
}

/// Who stopped a recording: the user (an all-day recording pauses for the rest of today), or the system (it stays on,
/// for the user to continue).
nonisolated enum RecordingStopReason: Sendable {
    case user, system
}
