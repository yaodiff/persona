#if os(iOS)
import AVFoundation
import Foundation

/// One meeting's audio to the hub (#3862 phase 5, docs/assistant/meeting-minutes.md「Audio from the phone」; the phone's
/// side docs/apple/recording.md「Hub transcription (phase 5)」): the second consumer of the recorder's one input tap
/// (`RecordingCapture.listenUplink`), stamped with the writer's frame position, into the last 60 seconds kept in memory
/// (`MeetingAudioRing`), and from there onto the audio socket once the hub said `ready`.
///
/// Real-time audio: a buffer costs the tap's thread a conversion to PCM16 and a copy under a lock, and the socket's sends
/// run on a queue of their own; nothing per buffer reaches the main thread (docs/apple/performance.md P1, P8). The socket's
/// frames go out in order through that queue: `start` first, then whatever follows, the audio after `ready` only. What the
/// hub says comes to `MeetingTranscription` on the main actor, tagged with the socket it came on (`generation`), so a
/// late event of a socket already replaced changes nothing.
nonisolated final class MeetingAudioUplink: @unchecked Sendable {
    let recording: UUID

    private let lock = NSLock()
    private var ring = MeetingAudioRing()
    private var socket: VoiceSocket?
    private var generation = 0
    /// `end` went: the recording stopped, and no audio of any later one ever joins the ring or the socket.
    private var finished = false
    /// The recording stopped being a hub-mode meeting at this sample (`cut`): nothing from it on joins the ring or goes.
    private var cutAt: Int64?
    private let queue = DispatchQueue(label: "persona.meeting-uplink", qos: .userInitiated)

    init(recording: UUID) {
        self.recording = recording
    }

    /// A buffer of the tap (16 kHz mono float, on its thread), whose first sample lies at `seconds`: into the ring and, while
    /// the socket streams, on to it.
    func feed(_ buffer: AVAudioPCMBuffer, at seconds: Double) {
        feed(MeetingPCM.samples(buffer), at: MeetingAudioClock.sample(seconds))
    }

    /// `samples` at sample `position` (the tests feed these directly); nothing once `end` went, nothing from the cut on.
    func feed(_ samples: [Int16], at position: Int64) {
        let streaming = lock.withLock { () -> Bool in
            guard !finished else { return false }
            var samples = samples
            if let cutAt {
                guard position < cutAt else { return false }
                if position + Int64(samples.count) > cutAt { samples = Array(samples.prefix(Int(cutAt - position))) }
            }
            ring.append(samples, at: position)
            return ring.streaming && socket != nil
        }
        if streaming { queue.async { [weak self] in self?.pump() } }
    }

    /// The recording stopped being a hub-mode meeting at sample `position` (it turned into a Note there): no sample at or
    /// past it joins the ring or goes on the socket, also one the tap hands over a moment later, so the uplink's last
    /// sample is where hub-mode time ended.
    func cut(at position: Int64) {
        lock.withLock {
            cutAt = min(cutAt ?? position, position)
            ring.cut(at: position)
        }
    }

    /// `end` went (`end`): later buffers are ignored.
    var isFinished: Bool { lock.withLock { finished } }

    /// Opens `socket` for the meeting, closing the one before: `start` (the oldest second held and the whole timeline
    /// so far) goes at once, then `speaking` on for a stretch being said now and the role in effect. Events come to
    /// `handler` with this socket's generation. No audio goes before the hub's `ready`.
    @MainActor @discardableResult
    func open(_ socket: VoiceSocket, timeline: MeetingTimeline, speaking: RecordingSpan?, role: RecordingRoleChange?,
              handler: @escaping @MainActor @Sendable (Int, VoiceSocketEvent) -> Void) -> Int {
        let (gen, before, at) = lock.withLock { () -> (Int, VoiceSocket?, Double) in
            generation += 1
            let before = self.socket
            self.socket = socket
            ring.stop()
            return (generation, before, MeetingAudioClock.seconds(ring.start))
        }
        before?.close()
        socket.open { event in handler(gen, event) }
        var frames = [MeetingAudioOut.start(at: at, timeline: timeline)]
        if let speaking { frames.append(MeetingAudioOut.speaking(on: true, at: speaking.start, text: speaking.text)) }
        if let role { frames.append(MeetingAudioOut.role(at: role.at, attendance: role.attendance)) }
        let first = frames
        queue.async {
            for frame in first { socket.send(text: frame) }
        }
        return gen
    }

    /// The generation of the socket open now (0: none was opened yet).
    var currentGeneration: Int { lock.withLock { generation } }

    /// The hub said `ready {from}`: the audio goes from there (`MeetingAudioRing.ready`).
    func ready(from seconds: Double, generation gen: Int) {
        let ok = lock.withLock { () -> Bool in
            guard gen == generation, socket != nil else { return false }
            ring.ready(from: MeetingAudioClock.sample(seconds))
            return true
        }
        if ok { queue.async { [weak self] in self?.pump() } }
    }

    /// `ack {at}`.
    func ack(_ seconds: Double) {
        lock.withLock { ring.ack(MeetingAudioClock.sample(seconds)) }
    }

    /// A text frame, after everything queued before it (a role, a speaking stretch).
    func send(text: String) {
        queue.async { [weak self] in
            guard let self, let socket = self.lock.withLock({ self.socket }) else { return }
            socket.send(text: text)
        }
    }

    /// The recording stopped: the audio still held goes, then `end` at the last sample the tap gave (a buffer on its way
    /// as the tap went still lands first: the end waits `settle`). From then on the uplink takes no more audio, and
    /// `done` runs (on the uplink's queue): the time to take it off the tap. False when no socket is open.
    @discardableResult
    func end(settle: TimeInterval = 0.25, then done: (@Sendable () -> Void)? = nil) -> Bool {
        guard lock.withLock({ socket != nil }) else { return false }
        queue.asyncAfter(deadline: .now() + settle) { [weak self] in
            guard let self else { return }
            // Closed to the tap first, so the end is the last sample sent.
            self.lock.withLock { self.finished = true }
            self.pump()
            let last = self.lock.withLock { self.socket.map { ($0, MeetingAudioClock.seconds(self.ring.end)) } }
            if let last { last.0.send(text: MeetingAudioOut.end(at: last.1)) }
            done?()
        }
        return true
    }

    /// Closes the socket from this side (no event follows); the ring stays for the next one.
    func close() {
        let socket = lock.withLock { () -> VoiceSocket? in
            generation += 1
            let s = self.socket
            self.socket = nil
            ring.stop()
            return s
        }
        socket?.close()
    }

    /// The socket of `gen` went away by itself: nothing streams until the next one's `ready`.
    func dropped(generation gen: Int) {
        lock.withLock {
            guard gen == generation else { return }
            socket = nil
            ring.stop()
        }
    }

    /// Unacknowledged audio that fell out of the ring (or never reached it): the phone's account of the gaps.
    var lost: [MeetingGap] { lock.withLock { ring.lostGaps } }

    /// The end of the audio the tap gave so far, seconds.
    var heldEnd: Double { lock.withLock { MeetingAudioClock.seconds(ring.end) } }

    /// Waits until everything queued for the socket went (the tests read what it sent).
    func drain() { queue.sync {} }

    /// Sends what the ring has for the socket, in order, until nothing is left.
    private func pump() {
        while true {
            // The closure reads the property; a local of the same name would be captured before it is declared.
            let (out, current) = lock.withLock { () -> (MeetingAudioRing.Out?, VoiceSocket?) in
                guard let socket = self.socket else { return (nil, nil) }
                return (ring.next(), socket)
            }
            guard let out, let socket = current else { return }
            switch out {
            case .seek(let sample): socket.send(text: MeetingAudioOut.seek(at: MeetingAudioClock.seconds(sample)))
            case .audio(let data, _): socket.send(data: data)
            }
        }
    }
}
#endif
