import Accelerate
import AVFoundation
import os

/// Adapted from Hourhand: records the microphone into back-to-back AAC segments (16 kHz mono `.m4a`) with no gap between files.
///
/// The input tap converts to 16 kHz and hands buffers to a serial queue that writes the current file; when a file
/// reaches `RecordingAudioSpec.segmentSeconds` it is closed (finalized) and reported through `onSegment`, and the next buffer
/// opens the next file. Pausing stops the tap, so the timeline (`elapsed`, segment offsets, marks) never contains
/// the paused time. Used by both the watch and the iPhone (docs/apple/recording.md).
///
/// All-day recordings (docs/assistant/day-recap.md「The day recording on the watch」) cut at their midnight without
/// stopping the microphone: the samples before `setBoundary`'s time close the old day's folder, the ones from it on open
/// the next day's, and both kinds of event go into one ordered log (`takeEvents`) so whoever registers them never sees a
/// new day's chunk before the rotation that made its folder. Their chunks take their seqs from the recording's counter
/// (`allocateSeq`), asked on the writer's queue just before each file opens, so a seq is on disk before any audio of it.
nonisolated final class RecordingSegmentWriter: @unchecked Sendable {
    nonisolated struct Segment: Sendable, Equatable {
        let index: Int
        let url: URL
        let start: Double
        let duration: Double
        /// Wall-clock time of the segment's first sample, taken from the audio buffers rather than when the segment
        /// is reported (callbacks can run late, and stop() reports several at once).
        let startedAt: Date
        /// Mean RMS level of every sample in the file, dBFS (`RecordingDayLevel`): the phone skips quiet all-day chunks.
        let level: Double

        /// The recording the segment belongs to: its folder's name. Never the recording capture had in mind when it
        /// asked, since a midnight can move the writer to the next folder before that is known on the main thread.
        var owner: UUID? { UUID(uuidString: url.deletingLastPathComponent().lastPathComponent) }
        var endedAt: Date { startedAt.addingTimeInterval(duration) }
    }

    /// A midnight crossed while recording: `last` is the old folder's final segment, ending exactly at `boundary` when
    /// audio ran through it, or where the audio stopped before it (nil when no file was open), and the next folder's
    /// audio starts at `firstSampleAt` — the boundary itself, or later when the first buffer past it came after a pause.
    nonisolated struct Rotation: Sendable, Equatable {
        let last: Segment?
        let previous: URL
        let next: URL
        let boundary: Date
        let firstSampleAt: Date
    }

    /// What the writer did, in the order it did it (`takeEvents`).
    nonisolated enum Event: Sendable, Equatable {
        case segment(Segment)
        case rotated(Rotation)

        /// The segment it finished: a segment's own, or the old day's last at a midnight (nil when none was open).
        var segment: Segment? {
            switch self {
            case .segment(let segment): segment
            case .rotated(let rotation): rotation.last
            }
        }

        /// The recordings it concerns: a segment's, or both days of a midnight.
        var owners: [UUID] {
            switch self {
            case .segment(let segment): [segment.owner].compactMap { $0 }
            case .rotated(let rotation): [rotation.previous, rotation.next].compactMap { UUID(uuidString: $0.lastPathComponent) }
            }
        }
    }

    /// What `cutForErase` did: the segment it closed (nil when none was open), and where the audio before the cut ends.
    nonisolated struct Cut: Sendable, Equatable {
        let segment: Segment?
        /// Wall-clock end of the last sample written before the cut, or when the microphone last opened if nothing came
        /// since; nil when this writer never had audio. Every later segment starts at or after it.
        let through: Date?
    }

    nonisolated enum RecorderError: LocalizedError {
        case noInput
        var errorDescription: String? { "No microphone input is available." }
    }

    /// Called on the writer queue when a segment file is finalized because it reached `segmentLength`. The segment is
    /// also in the event log: a callback is a wake-up, `takeEvents` is what registers it, exactly once.
    var onSegment: (@Sendable (Segment) -> Void)?
    /// Called on the writer queue when a buffer crosses the boundary set by `setBoundary` and the writer moved to the
    /// next folder (also logged, as `Event.rotated`).
    var onRotate: (@Sendable (_ last: Segment?, _ next: URL) -> Void)?

    private let engine = AVAudioEngine()
    private let queue = DispatchQueue(label: "persona.recording-writer", qos: .userInitiated)
    private let outFormat = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: RecordingAudioSpec.sampleRate, channels: 1, interleaved: false)!
    /// Whether the engine has our tap; touched only by whoever drives the writer (the main thread, or a test).
    private var tapInstalled = false

    // Writer state: only touched on `queue`.
    /// The folder segments are written to; moves at a midnight (`setBoundary`) or on `switchDirectory`.
    private var directory: URL
    private var file: AVAudioFile?
    private var fileFrames: AVAudioFramePosition = 0
    /// Sum of the squared samples written to the open file, for its level.
    private var fileSumOfSquares: Double = 0
    private var index: Int
    private var segmentStart: Double
    private var fileStartedAt = Date()
    /// Frames on the current timeline that reached the queue, so a new day's `elapsed` keeps the buffers still waiting.
    private var consumedFrames: AVAudioFramePosition
    /// The next midnight and the folder of the recording after it; nil outside all-day recording.
    private var boundary: Date?
    private var nextDirectory: URL?
    /// Segments and rotations not taken yet (`takeEvents`).
    private var events: [Event] = []
    /// Wall-clock end of the newest sample written (counted in samples from its file's start, as the segment's end is),
    /// or when the microphone last opened if nothing came since: where an erase's cut ends (`cutForErase`).
    private var audioThrough: Date?
    /// Left by `cutForErase` for the next file: it starts no earlier, so it lies wholly after the erased range.
    private var startFloor: Date?
    /// The seq taken for a file that could not be opened, used again for the next try in the same folder: a failing disk
    /// leaves one seq without audio, not one per buffer. Dropped when the folder changes.
    private var reservedSeq: Int?
    /// Gives each file its seq from the folder it is in; nil: seqs count up from `firstIndex`.
    private let allocateSeq: (@Sendable (_ directory: URL) -> Int?)?

    // Shared with readers on other threads.
    private nonisolated struct Shared {
        var frames: AVAudioFramePosition = 0
        var level: Float = 0
        var running = false
        var failure: String?
        var segmentFrames = AVAudioFramePosition(RecordingAudioSpec.segmentSeconds * RecordingAudioSpec.sampleRate)
        var listener: (@Sendable (AVAudioPCMBuffer, Double) -> Void)?
        /// Host time (seconds) the newest buffer's last sample was captured at, or when the microphone last opened.
        var capturedUntil: Double?
        /// The longest input buffer (seconds) the tap has had since it was installed (the input's rate may change with
        /// its route): how far a release may run ahead of `frames` (`placed`).
        var longestBuffer: Double = 0
    }
    private let shared = OSAllocatedUnfairLock(initialState: Shared())
    private var converter: AVAudioConverter?

    /// - Parameters:
    ///   - firstIndex / startOffset: continue an existing recording: its next index, and where its timeline goes on from.
    ///   - allocateSeq: all-day recordings: the seq of the next file in a folder, saved by the time it returns
    ///     (`RecordingLibrary.allocateSeq`). Called on the writer's queue, once per file, before the file is created.
    /// Rewrites each finished file without the container's padding (`RecordingM4A`), so a 10-second all-day chunk fits
    /// the watch's live envelope. Set before `start()`; off for every other recording.
    var compactsFiles = false
    /// All-day recordings: a gap in the audio ends the file being written (`write`), since a chunk's `started_at` and
    /// `duration` place every sample on the wall clock and no pause may fall inside one (docs/assistant/day-recap.md
    /// 「Chunks」). A gap is the input restarting after a route change, or a buffer the tap handed over after a pause's
    /// cut, followed by the audio of the continue. Other recordings keep their files across such gaps, on a timeline
    /// without them. Set before `start()`.
    var cutsAtGaps = false
    /// A room meeting (#3862 phase 4): the engine carries a player for the meeting call's voice (`callPlayer`) and one for
    /// Persona's other spoken answers (`answerPlayer`), at this format, so they play through the same I/O as the microphone
    /// and voice processing can take them out. Set before `start()`; nil (every other recording): no players and no
    /// output, as before.
    var callPlayerFormat: AVAudioFormat?
    /// A meeting that starts taking part (#3862 phase 4): the input runs with voice processing, the system's echo
    /// cancellation, from the start to the end (never switched while it records). Set before `start()`; where it will not
    /// turn on or the engine will not start with it, plain capture (`cancelsEcho` false). iOS only.
    var voiceProcessing = false
    /// Voice processing is on (`voiceProcessing` asked for it and it runs).
    private(set) var cancelsEcho = false
    /// The meeting call's player (`callPlayerFormat`), attached once at the start; nil otherwise.
    private(set) var callPlayer: AVAudioPlayerNode?
    /// The player of Persona's spoken answers in a room meeting (`MeetingVoicePlayer`, the voice extension's `speak`),
    /// beside the call's, attached with it; nil otherwise.
    private(set) var answerPlayer: AVAudioPlayerNode?

    /// Hears every converted buffer (16 kHz mono float) besides the file, on the tap's thread (#3862 phase 4: a meeting's
    /// Talk spans and the talk window's silence), with where its first sample lies on the recording's timeline (seconds,
    /// `elapsed` before it): Talk sends only the samples inside its span (`elapsedNow` at the press and the release).
    /// Cheap work only; nil: nobody listens. Set at any time.
    var listener: (@Sendable (AVAudioPCMBuffer, Double) -> Void)? {
        get { shared.withLock { $0.listener } }
        set { shared.withLock { $0.listener = newValue } }
    }

    init(directory: URL, firstIndex: Int = 0, startOffset: Double = 0, allocateSeq: (@Sendable (_ directory: URL) -> Int?)? = nil) {
        self.directory = directory
        self.index = firstIndex
        self.segmentStart = startOffset
        self.allocateSeq = allocateSeq
        let frames = AVAudioFramePosition(startOffset * RecordingAudioSpec.sampleRate)
        self.consumedFrames = frames
        shared.withLock { $0.frames = frames }
    }

    /// Recorded time on the recording timeline (pauses excluded), seconds. An all-day recording's starts again at a
    /// midnight, with its next day.
    var elapsed: Double { Double(shared.withLock { $0.frames }) / RecordingAudioSpec.sampleRate }

    /// Where the sound the microphone takes at this moment will lie on the timeline, for one edge of a Talk span (#3862
    /// phase 4): `elapsed` (the buffers the tap has had) plus what was captured since the newest of them, which reaches
    /// the tap only once its buffer is full (about 85 ms at 48 kHz, 512 ms at a Bluetooth headset's 8 kHz), each edge
    /// placed to its own safe side (`placed`). `elapsed` while paused. The samples of a buffer from before the press are
    /// not the span's, nor those from after the release.
    func elapsedNow(_ edge: SpanEdge) -> Double {
        let now = AVAudioTime.seconds(forHostTime: mach_absolute_time())
        return shared.withLock { s in
            let base = Double(s.frames) / RecordingAudioSpec.sampleRate
            guard s.running, let until = s.capturedUntil else { return base }
            return Self.placed(edge, base: base, since: now - until, longestBuffer: s.longestBuffer)
        }
    }

    /// The press or the release of a Talk span.
    nonisolated enum SpanEdge: Sendable { case press, release }

    /// An edge `since` seconds after the newest buffer's last sample, which lies at `base`. A press is never placed before
    /// the moment it happened: whatever `since` is (a buffer longer than usual, delivery running late), the press is at
    /// least that far on, plus `pressMargin`; a stall of the input (no audio captured for a while) only places it later,
    /// cutting the owner's first words rather than sending what the room said before. Only a release is held back: at
    /// most the longest buffer seen and its delivery (`deliveryMargin`) past `base`, so a stall never lets the span run on
    /// past the finger's lift.
    static func placed(_ edge: SpanEdge, base: Double, since: Double, longestBuffer: Double) -> Double {
        let since = max(0, since)
        switch edge {
        case .press: return base + since + pressMargin
        case .release: return base + min(since, longestBuffer + deliveryMargin)
        }
    }

    /// Added to a press: the converter's own delay and a buffer without a host time (its end then taken as when it
    /// arrived) would otherwise place it a few milliseconds early. Nobody speaks in the first 50 ms after pressing.
    static let pressMargin: Double = 0.05
    /// How late after its last sample a buffer may still reach the tap in normal running.
    static let deliveryMargin: Double = 0.1
    /// Recent input level, 0…1 (perceptual, for meters).
    var level: Float { shared.withLock { $0.level } }
    var failure: String? { shared.withLock { $0.failure } }
    var isRunning: Bool { shared.withLock { $0.running } }

    /// Wall-clock end of the newest sample written, or when the microphone last opened if nothing came since; nil before
    /// the first start. Read after a pause's cut or a stop, it is when the audio ended: an all-day `paused` event's time.
    /// Waits for the buffers already handed to the writer.
    var audioEnd: Date? { queue.sync { audioThrough } }

    /// Takes every segment finalized and not taken yet, in order (a rotation's last segment included): after a stop,
    /// what the main thread has not registered yet, so the end message's count leaves nothing out. Draining: a second
    /// read returns only what was finalized since. Capture reads `takeEvents` instead, which also has the rotations.
    var finishedSegments: [Segment] {
        takeEvents().compactMap(\.segment)
    }

    /// Takes the log of segments finalized and midnights crossed since the last call, in order. It waits for the
    /// buffers already handed to the writer, so nothing that came before the call is left for the next one. Never call
    /// it from `onSegment` / `onRotate` (they run on the writer queue).
    func takeEvents() -> [Event] {
        queue.sync {
            let taken = events
            events = []
            return taken
        }
    }

    /// Length of the segments being written, seconds. Lowered while someone is watching the text arrive (the iPhone app
    /// is open), so the next segment closes sooner; the current one closes as soon as it is longer than the new length.
    var segmentLength: Double {
        get { Double(shared.withLock { $0.segmentFrames }) / RecordingAudioSpec.sampleRate }
        set { shared.withLock { $0.segmentFrames = AVAudioFramePosition(max(10, newValue) * RecordingAudioSpec.sampleRate) } }
    }

    func start() throws {
        try FileManager.default.createDirectory(at: queue.sync { directory }, withIntermediateDirectories: true)
        // Before the formats are read: voice processing changes the input node's format.
        if voiceProcessing { enableVoiceProcessing() }
        attachCallPlayer()
        try installTap()
        engine.prepare()
        noteOpening()
        do {
            try engine.start()
        } catch where cancelsEcho {
            // Voice processing that turns on and then will not let the engine start: plain capture, as the voice call does.
            removeTap()
            engine.stop()
            disableVoiceProcessing()
            try installTap()
            engine.prepare()
            do { try engine.start() } catch { removeTap(); throw error }
        } catch {
            removeTap(); throw error
        }
        shared.withLock { $0.running = true }
    }

    private func enableVoiceProcessing() {
        #if os(iOS)
        do {
            try engine.inputNode.setVoiceProcessingEnabled(true)
            // Voice processing ducks every other sound the phone plays (an answer said through the `speak` voice or the
            // on-device one, which don't go through this I/O) by default, heavily: the least it can, and no "advanced"
            // ducking that follows who speaks (iOS 17+).
            engine.inputNode.voiceProcessingOtherAudioDuckingConfiguration =
                AVAudioVoiceProcessingOtherAudioDuckingConfiguration(enableAdvancedDucking: false, duckingLevel: .min)
            cancelsEcho = true
        } catch {
            Self.log.notice("recording: no voice processing, recording without echo cancellation: \(error.localizedDescription, privacy: .public)")
        }
        #endif
    }

    private func disableVoiceProcessing() {
        #if os(iOS)
        Self.log.notice("recording: the engine would not start with voice processing, recording without echo cancellation")
        try? engine.inputNode.setVoiceProcessingEnabled(false)
        cancelsEcho = false
        #endif
    }

    /// The meeting call's player and the answers' one, into the engine's mixer: attached before the engine first starts,
    /// so the output joins the graph once and a call or an answer starting later changes nothing that runs.
    private func attachCallPlayer() {
        guard let format = callPlayerFormat, callPlayer == nil else { return }
        func attached() -> AVAudioPlayerNode {
            let player = AVAudioPlayerNode()
            engine.attach(player)
            engine.connect(player, to: engine.mainMixerNode, format: format)
            return player
        }
        callPlayer = attached()
        answerPlayer = attached()
    }

    private static let log = Logger(subsystem: "persona", category: "recording")

    func pause() {
        engine.pause()
        shared.withLock { $0.running = false; $0.level = 0 }
    }

    func resume() throws {
        let input = engine.inputNode
        if input.numberOfInputs == 0 { throw RecorderError.noInput }
        noteOpening()
        if converter?.inputFormat != input.outputFormat(forBus: 0) {
            try restartAfterConfigurationChange()
            return
        }
        try engine.start()
        shared.withLock { $0.running = true }
    }

    /// Re-creates the tap after the input route or format changed (AVAudioEngineConfigurationChange), keeping the
    /// current segment open.
    func restartAfterConfigurationChange() throws {
        engine.stop()
        removeTap()
        try installTap()
        engine.prepare()
        try engine.start()
        shared.withLock { $0.running = true }
    }

    /// Closes the segment being written now, while paused (a phone call): an app killed before the recording resumes keeps
    /// it, rather than losing up to a whole segment. The next buffer opens the next one. Nil if nothing was written since.
    /// The segment is logged too (`takeEvents`).
    @discardableResult
    func cut() -> Segment? {
        queue.sync { closeAndLog() }
    }

    /// `cut()` for an erase: also returns where the audio before the cut ends, and the next segment starts no earlier.
    /// A range erased up to `through` then holds every sample written before the cut and none after it, however late
    /// either segment is registered, and however short the next one is (a pause or a stop right after).
    func cutForErase() -> Cut {
        queue.sync {
            let segment = closeAndLog()
            startFloor = audioThrough
            return Cut(segment: segment, through: audioThrough)
        }
    }

    /// `cut()` for an erase the phone asked for, of `recording` up to `end`: closes the file being written when it is
    /// that recording's (its folder names it, as `Segment.owner` does) and began before `end`, the one case where it
    /// holds audio of the range. Checked and cut in one step on the queue, so a midnight or a full chunk can't swap the
    /// open file in between; and the folder, not the recording the main thread thinks is being written (a midnight
    /// moves that on late), says whose it is. Nil when nothing was cut.
    @discardableResult
    func cutOpenChunk(of recording: UUID, startedBefore end: Date) -> Segment? {
        queue.sync {
            guard file != nil, UUID(uuidString: directory.lastPathComponent) == recording, fileStartedAt < end else { return nil }
            return closeAndLog()
        }
    }

    /// Whether a notification's object is this writer's engine: `.AVAudioEngineConfigurationChange` comes from every engine
    /// in the process (dictation's, a voice call's), and only this one's changes concern the recording.
    func owns(_ object: ObjectIdentifier?) -> Bool {
        object == ObjectIdentifier(engine)
    }

    /// Stops recording and finalizes the last segment; returns it (nil if nothing was written since the last one). It
    /// is logged too, so `takeEvents` after a stop has everything still to register.
    @discardableResult
    func stop() -> Segment? {
        if tapInstalled {
            engine.stop()
            removeTap()
        }
        shared.withLock { $0.running = false; $0.level = 0 }
        return queue.sync { closeAndLog() }
    }

    // MARK: - All-day rotation

    /// From the sample at `boundary` (a day recording's midnight) on, audio goes to `nextDirectory`, whose segments are
    /// numbered from its own start (its counter's seq 0) on a timeline of their own. Serialized with the buffers: those
    /// already handed to the writer are written under the boundary they came with. Replaces a boundary not crossed yet.
    func setBoundary(at boundary: Date, nextDirectory: URL) {
        queue.sync {
            self.boundary = boundary
            self.nextDirectory = nextDirectory
        }
    }

    /// Moves to another folder now, for a day that is over while no boundary will cut it (paused past its midnight, the
    /// clock moved on): closes the open segment first (returned, and logged under its own folder), creates `next`, and
    /// goes on there on a new timeline, its seqs from `allocateSeq` (from 0 without it). A boundary not crossed yet
    /// belonged to the old folder's day and is dropped; set the new day's one after.
    @discardableResult
    func switchDirectory(to next: URL) -> Segment? {
        queue.sync {
            let last = closeAndLog()
            moveTo(next)
            boundary = nil
            nextDirectory = nil
            restartTimeline(dropping: consumedFrames)
            makeDirectory(next)
            return last
        }
    }

    // MARK: - Tap

    /// The microphone opens (again): no sample from here on was taken before this moment. Waits for the buffers already
    /// queued, so the moment is never overwritten by audio from before the pause.
    private func noteOpening() {
        let now = Date()
        let host = AVAudioTime.seconds(forHostTime: mach_absolute_time())
        shared.withLock { $0.capturedUntil = host }
        queue.sync { audioThrough = now }
    }

    private func installTap() throws {
        let input = engine.inputNode
        let inFormat = input.outputFormat(forBus: 0)
        guard inFormat.sampleRate > 0, inFormat.channelCount > 0 else { throw RecorderError.noInput }
        converter = AVAudioConverter(from: inFormat, to: outFormat)
        // A new route may bring another rate, and with it another buffer length.
        shared.withLock { $0.longestBuffer = 0 }
        input.installTap(onBus: 0, bufferSize: 4096, format: inFormat) { [weak self] buffer, when in
            // `when` is the first sample's host time; without it, assume the buffer just finished filling.
            let now = AVAudioTime.seconds(forHostTime: mach_absolute_time())
            let length = Double(buffer.frameLength) / buffer.format.sampleRate
            let first = when.isHostTimeValid ? AVAudioTime.seconds(forHostTime: when.hostTime) : now - length
            self?.process(buffer, startedAt: Date().addingTimeInterval(-max(0, now - first)), capturedUntil: first + length)
        }
        tapInstalled = true
    }

    private func removeTap() {
        engine.inputNode.removeTap(onBus: 0)
        tapInstalled = false
    }

    private func process(_ buffer: AVAudioPCMBuffer, startedAt: Date, capturedUntil: Double) {
        guard let converter, buffer.frameLength > 0 else { return }
        let ratio = outFormat.sampleRate / buffer.format.sampleRate
        let capacity = AVAudioFrameCount(Double(buffer.frameLength) * ratio) + 32
        guard let out = AVAudioPCMBuffer(pcmFormat: outFormat, frameCapacity: capacity) else { return }
        var consumed = false
        var error: NSError?
        converter.convert(to: out, error: &error) { _, status in
            if consumed { status.pointee = .noDataNow; return nil }
            consumed = true
            status.pointee = .haveData
            return buffer
        }
        guard error == nil, out.frameLength > 0 else { return }
        let level = Self.level(of: out)
        let frames = AVAudioFramePosition(out.frameLength)
        let length = Double(buffer.frameLength) / buffer.format.sampleRate
        let (listener, position) = shared.withLock { s -> ((@Sendable (AVAudioPCMBuffer, Double) -> Void)?, Double) in
            let before = s.frames
            s.frames += frames
            s.level = s.level * 0.6 + level * 0.4
            s.capturedUntil = capturedUntil
            s.longestBuffer = max(s.longestBuffer, length)
            return (s.listener, Double(before) / RecordingAudioSpec.sampleRate)
        }
        // Read only (the writer's queue gets the same buffer): the listener copies what it keeps.
        listener?(out, position)
        let box = BufferBox(buffer: out)
        queue.async { [weak self] in self?.write(box.buffer, startedAt: startedAt) }
    }

    private static func level(of buffer: AVAudioPCMBuffer) -> Float {
        guard let data = buffer.floatChannelData?[0] else { return 0 }
        let n = Int(buffer.frameLength)
        var sum: Float = 0
        for i in 0..<n { sum += data[i] * data[i] }
        let rms = (sum / Float(max(n, 1))).squareRoot()
        // -50 dB … 0 dB → 0 … 1
        let db = 20 * log10(max(rms, 0.000_01))
        return max(0, min(1, (db + 50) / 50))
    }

    // MARK: - Writer (queue)

    /// Writes converted 16 kHz audio as if it came from the tap; for tests, which have no microphone.
    func append(_ buffer: AVAudioPCMBuffer, startedAt: Date) {
        let frames = AVAudioFramePosition(buffer.frameLength)
        shared.withLock { $0.frames += frames }
        let box = BufferBox(buffer: buffer)
        queue.sync { write(box.buffer, startedAt: startedAt) }
    }

    private func write(_ buffer: AVAudioPCMBuffer, startedAt: Date) {
        let total = buffer.frameLength
        defer { consumedFrames += AVAudioFramePosition(total) }
        var offset: AVAudioFrameCount = 0
        while offset < total {
            // Samples from the boundary on belong to the next day's folder.
            var end = total
            if let boundary {
                let before = Self.frames(from: startedAt, before: boundary, count: total)
                if offset >= before {
                    rotate(atFrame: offset, of: startedAt)
                    continue
                }
                end = before
            }
            let sampleAt = startedAt.addingTimeInterval(Double(offset) / RecordingAudioSpec.sampleRate)
            if cutsAtGaps, file != nil, Self.isGap(from: fileEnd, to: sampleAt) {
                if let segment = closeAndLog() { onSegment?(segment) }
                continue
            }
            if file == nil {
                openFile()
                fileStartedAt = firstSampleTime(sampleAt)
            }
            guard file != nil else { return }
            let segmentFrames = shared.withLock { $0.segmentFrames }
            if fileFrames >= segmentFrames, let segment = closeAndLog() {
                // The segment length was lowered below what this file already holds.
                onSegment?(segment)
                continue
            }
            let room = AVAudioFrameCount(segmentFrames - fileFrames)
            let n = min(room, end - offset)
            if let slice = Self.slice(buffer, from: offset, count: n) {
                do { try file?.write(from: slice) } catch { shared.withLock { $0.failure = String(localized: "Couldn't write the recording file") }; return }
                fileSumOfSquares += Self.sumOfSquares(slice)
            }
            fileFrames += AVAudioFramePosition(n)
            offset += n
            audioThrough = fileEnd
            if fileFrames >= segmentFrames, let segment = closeAndLog() {
                onSegment?(segment)
            }
        }
    }

    /// How many of a buffer's `count` frames, the first at `start`, come before `boundary`: the index of the first sample
    /// at or after it. Dates are doubles a few hundred nanoseconds apart at best, so a boundary that falls on a sample
    /// within a hundredth of a sample's length counts as on it, rather than moving the cut by one.
    static func frames(from start: Date, before boundary: Date, count: AVAudioFrameCount) -> AVAudioFrameCount {
        let exact = boundary.timeIntervalSince(start) * RecordingAudioSpec.sampleRate
        guard exact > 0 else { return 0 }
        let first = (exact - 0.01).rounded(.up)
        return first >= Double(count) ? count : AVAudioFrameCount(max(0, first))
    }

    /// When a new file's first sample was taken: its buffer's time, or the floor an erase's cut left (`cutForErase`) when
    /// the buffer says a few milliseconds earlier. Each buffer's time comes from the host clock on its own, so the one
    /// after a cut can claim to start a hair before the samples ahead of it end. A larger difference is the clock
    /// itself moving and is kept as it is.
    private func firstSampleTime(_ time: Date) -> Date {
        guard let floor = startFloor else { return time }
        startFloor = nil
        let early = floor.timeIntervalSince(time)
        return early > 0 && early < Self.bufferClockJitter ? floor : time
    }

    /// How far apart two consecutive buffers' host-clock times can be from their sample count, at most.
    private static let bufferClockJitter: TimeInterval = 0.25

    /// Wall-clock end of the open file's last sample, counted in samples from its first.
    private var fileEnd: Date { fileStartedAt.addingTimeInterval(Double(fileFrames) / RecordingAudioSpec.sampleRate) }

    /// Whether audio starting at `next` doesn't follow on from audio ending at `end`: more than the host clock's jitter
    /// apart, either way (a gap, or the wall clock set back).
    static func isGap(from end: Date, to next: Date) -> Bool {
        abs(next.timeIntervalSince(end)) > bufferClockJitter
    }

    /// Crossing the boundary at frame `offset` of a buffer that started at `bufferStart`: the old folder's file ends
    /// there, the next folder's segments are numbered from its start, and the timeline (`elapsed`, segment offsets)
    /// starts again with the first sample past the boundary while still counting buffers queued behind this one.
    private func rotate(atFrame offset: AVAudioFrameCount, of bufferStart: Date) {
        guard let crossed = boundary, let next = nextDirectory else {
            boundary = nil
            nextDirectory = nil
            return
        }
        boundary = nil
        nextDirectory = nil
        let last = closeFile()
        let previous = directory
        moveTo(next)
        restartTimeline(dropping: consumedFrames + AVAudioFramePosition(offset))
        // Before the first file there: the folder's entry is made later, on the main thread.
        makeDirectory(next)
        let firstSampleAt = max(crossed, bufferStart.addingTimeInterval(Double(offset) / RecordingAudioSpec.sampleRate))
        events.append(.rotated(Rotation(last: last, previous: previous, next: next, boundary: crossed, firstSampleAt: firstSampleAt)))
        onRotate?(last, next)
    }

    /// Segments go to `next` from the next file on, numbered there from the start: a seq taken in the old folder stays
    /// its own.
    private func moveTo(_ next: URL) {
        directory = next
        index = 0
        segmentStart = 0
        reservedSeq = nil
    }

    /// Starts the timeline again after `frames` already on it: what is counted beyond them (buffers queued behind) stays.
    private func restartTimeline(dropping frames: AVAudioFramePosition) {
        shared.withLock { $0.frames -= frames }
        consumedFrames -= frames
    }

    private func makeDirectory(_ url: URL) {
        do { try FileManager.default.createDirectory(at: url, withIntermediateDirectories: true) }
        catch { shared.withLock { $0.failure = String(localized: "Couldn't create the recording file") } }
    }

    private func openFile() {
        let seq = reservedSeq ?? allocateSeq?(directory) ?? index
        reservedSeq = seq
        index = seq
        let url = directory.appendingPathComponent(RecordingSegment.fileName(seq))
        try? FileManager.default.removeItem(at: url)
        let settings: [String: Any] = [
            AVFormatIDKey: kAudioFormatMPEG4AAC,
            AVSampleRateKey: RecordingAudioSpec.sampleRate,
            AVNumberOfChannelsKey: 1,
            AVEncoderBitRateKey: RecordingAudioSpec.bitRate,
        ]
        do {
            file = try AVAudioFile(forWriting: url, settings: settings, commonFormat: .pcmFormatFloat32, interleaved: false)
            fileFrames = 0
            fileSumOfSquares = 0
            reservedSeq = nil
        } catch {
            shared.withLock { $0.failure = String(localized: "Couldn't create the recording file") }
        }
    }

    /// Finalizes the open file and logs it. Returns nil when there was nothing in it.
    private func closeAndLog() -> Segment? {
        guard let segment = closeFile() else { return nil }
        events.append(.segment(segment))
        return segment
    }

    /// Finalizes the open file. Returns nil when there was nothing in it.
    private func closeFile() -> Segment? {
        guard let url = file?.url else { return nil }
        // On watchOS 10 releasing the last writer reference finalizes the file. Do not retain it in write(_:)
        // across this call: onSegment must see the finalized AAC container.
        autoreleasepool {
            if #available(watchOS 11, *) { file?.close() }
            file = nil
        }
        defer { fileFrames = 0; fileSumOfSquares = 0 }
        guard fileFrames > 0 else {
            try? FileManager.default.removeItem(at: url)
            return nil
        }
        // Without the writer's padding a 10-second chunk fits the live envelope (docs/assistant/day-recap.md「Chunks」).
        // Only all-day chunks: every other recording keeps the files AVAudioFile writes, byte for byte.
        if compactsFiles { RecordingM4A.compact(url) }
        let duration = Double(fileFrames) / RecordingAudioSpec.sampleRate
        let level = RecordingDayLevel.dBFS(sumOfSquares: fileSumOfSquares, count: Int(fileFrames))
        let segment = Segment(index: index, url: url, start: segmentStart, duration: duration, startedAt: fileStartedAt, level: level)
        index += 1
        segmentStart += duration
        return segment
    }

    private static func slice(_ buffer: AVAudioPCMBuffer, from start: AVAudioFrameCount, count: AVAudioFrameCount) -> AVAudioPCMBuffer? {
        if start == 0 && count == buffer.frameLength { return buffer }
        guard let out = AVAudioPCMBuffer(pcmFormat: buffer.format, frameCapacity: count),
              let src = buffer.floatChannelData?[0], let dst = out.floatChannelData?[0] else { return nil }
        dst.update(from: src.advanced(by: Int(start)), count: Int(count))
        out.frameLength = count
        return out
    }

    private static func sumOfSquares(_ buffer: AVAudioPCMBuffer) -> Double {
        guard let data = buffer.floatChannelData?[0], buffer.frameLength > 0 else { return 0 }
        return Double(vDSP.sumOfSquares(UnsafeBufferPointer(start: data, count: Int(buffer.frameLength))))
    }
}

/// Hands a freshly converted buffer (owned by nobody else) to the writer queue.
nonisolated private struct BufferBox: @unchecked Sendable { let buffer: AVAudioPCMBuffer }

/// The finished segment files' container. AVAudioFile's m4a writer leaves a `free` box of about 23 KB between `moov` and
/// `mdat` (room for the header to grow, never used once the file is closed), so 10 seconds of 32 kbps AAC come to about
/// 65 KB instead of 42 KB, over the all-day live envelope's 48 000 bytes (docs/assistant/day-recap.md「Chunks」).
nonisolated enum RecordingM4A {
    /// Rewrites a finished file without its padding (`compacted`). A file that can't be read or rewritten, or whose
    /// layout this doesn't know, stays as it was: still a valid file, only larger. The rewrite is a new file, so it is
    /// given the original's creation date back: when the chunk's first sample was written, which is where
    /// `RecordingLibrary.recover` has a chunk an exit left unregistered begin.
    static func compact(_ url: URL) {
        guard let data = try? Data(contentsOf: url),
              let compact = compacted(data) else { return }
        let files = FileManager.default
        let created = (try? files.attributesOfItem(atPath: url.path))?[.creationDate] as? Date
        guard (try? compact.write(to: url, options: .atomic)) != nil, let created else { return }
        try? files.setAttributes([.creationDate: created], ofItemAtPath: url.path)
    }

    /// `data` without its top-level `free` and `skip` boxes, each chunk offset (`stco`, `co64`) moved back by what was
    /// removed before it. nil when there is no such box, or the layout is not one this knows how to rewrite (boxes that
    /// don't add up, not exactly one `moov`, an offset into a removed box).
    static func compacted(_ data: Data) -> Data? {
        let bytes = [UInt8](data)
        guard let top = boxes(bytes, in: 0..<bytes.count) else { return nil }
        let removed = top.filter { $0.type == "free" || $0.type == "skip" }.map(\.range)
        let movies = top.filter { $0.type == "moov" }
        guard !removed.isEmpty, movies.count == 1, let tables = chunkOffsetTables(bytes, in: movies[0]) else { return nil }
        var out = bytes
        for table in tables {
            let width = table.type == "co64" ? 8 : 4
            // After the version and flags: the entry count, then the entries.
            let countAt = table.body.lowerBound + 4
            guard countAt + 4 <= table.body.upperBound else { return nil }
            let count = Int(read(bytes, at: countAt, width: 4))
            guard count <= (table.body.upperBound - countAt - 4) / width else { return nil }
            for i in 0..<count {
                let at = countAt + 4 + i * width
                guard let offset = moved(read(bytes, at: at, width: width), removing: removed) else { return nil }
                write(&out, offset, at: at, width: width)
            }
        }
        for range in removed.reversed() { out.removeSubrange(range) }
        return Data(out)
    }

    /// Where a byte offset lands once `removed` (in file order) is gone; nil for one inside a removed box.
    private static func moved(_ offset: UInt64, removing removed: [Range<Int>]) -> UInt64? {
        var shift: UInt64 = 0
        for range in removed {
            if offset >= UInt64(range.upperBound) {
                shift += UInt64(range.count)
            } else if offset >= UInt64(range.lowerBound) {
                return nil
            }
        }
        return offset - shift
    }

    private struct Box {
        var type: String
        /// The whole box, its header included.
        var range: Range<Int>
        /// What follows the header.
        var body: Range<Int>
    }

    /// The boxes one after the other in `range`; nil when they don't fill it exactly.
    private static func boxes(_ bytes: [UInt8], in range: Range<Int>) -> [Box]? {
        var result: [Box] = []
        var at = range.lowerBound
        while at < range.upperBound {
            guard at + 8 <= range.upperBound else { return nil }
            var size = Int(read(bytes, at: at, width: 4))
            var header = 8
            if size == 1 {
                // A 64-bit size follows the type.
                guard at + 16 <= range.upperBound else { return nil }
                let large = read(bytes, at: at + 8, width: 8)
                guard large <= UInt64(range.upperBound - at) else { return nil }
                size = Int(large)
                header = 16
            } else if size == 0 {
                // To the end.
                size = range.upperBound - at
            }
            guard size >= header, size <= range.upperBound - at else { return nil }
            let type = String(decoding: bytes[(at + 4)..<(at + 8)], as: UTF8.self)
            result.append(Box(type: type, range: at..<(at + size), body: (at + header)..<(at + size)))
            at += size
        }
        return result
    }

    /// Every `stco` and `co64` of the movie's tracks (`moov` › `trak` › `mdia` › `minf` › `stbl`).
    private static func chunkOffsetTables(_ bytes: [UInt8], in movie: Box) -> [Box]? {
        var level = [movie]
        for container in ["trak", "mdia", "minf", "stbl"] {
            var next: [Box] = []
            for box in level {
                guard let children = boxes(bytes, in: box.body) else { return nil }
                next += children.filter { $0.type == container }
            }
            level = next
        }
        var tables: [Box] = []
        for table in level {
            guard let children = boxes(bytes, in: table.body) else { return nil }
            tables += children.filter { $0.type == "stco" || $0.type == "co64" }
        }
        return tables
    }

    private static func read(_ bytes: [UInt8], at: Int, width: Int) -> UInt64 {
        bytes[at..<(at + width)].reduce(0) { $0 << 8 | UInt64($1) }
    }

    private static func write(_ bytes: inout [UInt8], _ value: UInt64, at: Int, width: Int) {
        for i in 0..<width { bytes[at + i] = UInt8(truncatingIfNeeded: value >> (8 * (width - 1 - i))) }
    }
}
