import Accelerate
import AVFoundation
import Foundation
import os

/// How loud the microphone is right now, for the dictation bar's wave (`DictationBar`). The audio tap writes it on
/// the audio thread; the wave reads it once per frame. Nothing observes it, so a new level never redraws a view: only
/// the wave's canvas reads it, on its own timeline.
nonisolated final class DictationMeter: Sendable {
    private let state = OSAllocatedUnfairLock(initialState: (level: Float(0), synthetic: false, since: Date.distantPast))

    /// 0…1, the latest buffer's loudness on `Theme.DictationBar`'s decibel scale.
    func level(at now: Date = .now) -> Float {
        let s = state.withLock { $0 }
        return s.synthetic ? Self.syntheticLevel(now.timeIntervalSince(s.since)) : s.level
    }

    /// A buffer from the microphone: its loudness (RMS of the first channel, in decibels) mapped onto 0…1.
    func take(_ buffer: AVAudioPCMBuffer) {
        guard let samples = buffer.floatChannelData?[0], buffer.frameLength > 0 else { return }
        var rms: Float = 0
        vDSP_rmsqv(samples, 1, &rms, vDSP_Length(buffer.frameLength))
        let level = Self.normalized(rms: rms)
        state.withLock { $0.level = level }
    }

    /// Starts over at silence; `synthetic`: a made-up voice (the DEBUG sample words), since a simulator has no microphone.
    func reset(synthetic: Bool = false) {
        state.withLock { $0 = (0, synthetic, .now) }
    }

    static func normalized(rms: Float) -> Float {
        guard rms > 0 else { return 0 }
        let db = 20 * log10(rms)
        let floor = Theme.DictationBar.floorDB, ceiling = Theme.DictationBar.ceilingDB
        return min(1, max(0, (db - floor) / (ceiling - floor)))
    }

    /// Speech-like loudness: syllables (about five a second) under a slower phrase envelope, with pauses between phrases.
    static func syntheticLevel(_ t: TimeInterval) -> Float {
        let phrase = max(0, sin(t * 1.3) * 0.6 + 0.4)
        let syllable = 0.5 + 0.5 * sin(t * 2 * .pi * 4.7) * sin(t * 2 * .pi * 1.9 + 1)
        return Float(min(1, phrase * (0.25 + 0.75 * syllable)))
    }
}

/// The dictation bar's wave (ChatGPT's): a row of dots that scroll from right to left, one new column every
/// `sampleInterval`. A quiet column is a dot; a loud one stretches into a bar, and is drawn darker. The level follows the
/// voice through exponential smoothing (quicker up than down) between samples. Pure: the view advances it once per
/// sample and draws `columns`; the slide between samples and the fades at the ends are the view's (a layer animation
/// and a mask).
nonisolated struct DictationWave {
    struct Column: Equatable {
        /// Centre from the strip's right edge, in points: the newest at half a dot, each older one a pitch further left.
        var fromRight: CGFloat
        var height: CGFloat
        var opacity: Double
    }

    /// Newest first.
    private(set) var samples: [Float] = []
    private(set) var smoothed: Float = 0
    /// Samples taken since the start: the view slides the strip one pitch per sample.
    private(set) var taken = 0
    private var lastTime: TimeInterval?
    let capacity: Int

    /// `taken`: the count goes on across a resize, so the strip's slide does not jump back.
    init(capacity: Int, taken: Int = 0) {
        self.capacity = max(1, capacity)
        self.taken = taken
        samples = Array(repeating: 0, count: self.capacity)
    }

    /// One sample at `time` (seconds, any clock that only goes forward) with the meter's `level`.
    mutating func advance(to time: TimeInterval, level: Float) {
        let dt = lastTime.map { max(0, time - $0) } ?? Theme.DictationBar.sampleInterval
        lastTime = time
        let tau = level > smoothed ? Theme.DictationBar.attack : Theme.DictationBar.release
        smoothed += (level - smoothed) * Float(1 - exp(-dt / tau))
        samples.insert(smoothed, at: 0)
        if samples.count > capacity { samples.removeLast(samples.count - capacity) }
        taken += 1
    }

    /// The columns of a strip `width` × `height`; `still` (Reduce Motion): every column at the current level, so the row
    /// breathes in place instead of moving.
    func columns(width: CGFloat, height: CGFloat, still: Bool = false) -> [Column] {
        let pitch = Theme.DictationBar.pitch, dot = Theme.DictationBar.dot
        guard width >= dot else { return [] }
        let fit = Int((width - dot) / pitch) + 1
        return (0..<min(fit, samples.count)).map { i in
            let v = CGFloat(still ? smoothed : samples[i])
            let tall = max(dot, height * Theme.DictationBar.tallest * pow(v, Theme.DictationBar.levelCurve))
            let opacity = Theme.DictationBar.quietInk + (Theme.DictationBar.loudInk - Theme.DictationBar.quietInk) * Double(v)
            return Column(fromRight: dot / 2 + CGFloat(i) * pitch, height: tall, opacity: opacity)
        }
    }
}
