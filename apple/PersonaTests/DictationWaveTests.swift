import Foundation
import Testing
@testable import Persona

/// The dictation bar's wave (`DictationWave`, `DictationMeter`): one column per sample, newest at the right, quiet
/// columns are dots and loud ones grow, the level smoothed (quicker up than down). The slide between samples and the
/// fades at the ends are the view's (a layer animation and a mask).
struct DictationWaveTests {
    private let step = Theme.DictationBar.sampleInterval

    /// A wave given `count` samples of `level`, one every `step`, from `start`.
    private func fed(_ level: Float, count: Int, capacity: Int = 40, from wave: DictationWave? = nil, start: Double = 0) -> DictationWave {
        var w = wave ?? DictationWave(capacity: capacity)
        for i in 0..<count { w.advance(to: start + Double(i) * step, level: level) }
        return w
    }

    @Test func silenceIsARowOfDots() {
        let cols = fed(0, count: 30).columns(width: 200, height: 30)
        #expect(!cols.isEmpty)
        #expect(cols.allSatisfy { $0.height == Theme.DictationBar.dot })
    }

    @Test func voiceStretchesTheNewestColumnsIntoBars() {
        let w = fed(1, count: 6, from: fed(0, count: 30), start: 3)
        let cols = w.columns(width: 200, height: 30)
        // The newest columns (right) are tall and darker; the old silent ones (left) are still dots.
        #expect(cols[0].height > 15)
        #expect(cols[0].opacity > cols.last!.opacity)
        #expect(cols.last!.height == Theme.DictationBar.dot)
        #expect(cols.allSatisfy { $0.height <= 30 * Theme.DictationBar.tallest + 0.001 })
    }

    @Test func columnsSitOnePitchApartFromTheRightEdge() {
        let cols = fed(0, count: 40).columns(width: 120, height: 30)
        #expect(cols[0].fromRight == Theme.DictationBar.dot / 2)
        for (a, b) in zip(cols, cols.dropFirst()) { #expect(abs(b.fromRight - a.fromRight - Theme.DictationBar.pitch) < 0.001) }
        #expect(cols.allSatisfy { $0.fromRight <= 120 })
    }

    @Test func levelIsSmoothedNotStepped() {
        var w = DictationWave(capacity: 10)
        w.advance(to: 0, level: 0)
        w.advance(to: step, level: 1)
        // One sample after the voice starts the level has moved most of the way, not all of it.
        #expect(w.smoothed > 0.5 && w.smoothed < 0.99)
        w = fed(1, count: 10, from: w, start: 2 * step)
        #expect(w.smoothed > 0.99)
        // It falls back slower than it rose.
        var down = w
        down.advance(to: 12 * step, level: 0)
        var up = DictationWave(capacity: 10)
        up.advance(to: 0, level: 0)
        up.advance(to: step, level: 1)
        #expect(1 - down.smoothed < up.smoothed)
    }

    @Test func oneSampleOneColumnCappedAtCapacity() {
        var w = fed(0, count: 5, capacity: 12)
        #expect(w.taken == 5)
        w = fed(0, count: 30, capacity: 12, from: w, start: 1)
        #expect(w.samples.count == 12)
        #expect(w.taken == 35)
        // A resize keeps the count, so the strip's slide carries on where it was.
        #expect(DictationWave(capacity: 20, taken: w.taken).taken == 35)
    }

    @Test func reduceMotionShowsTheCurrentLevelEverywhere() {
        let w = fed(0.6, count: 20, from: fed(0, count: 20))
        let cols = w.columns(width: 200, height: 30, still: true)
        #expect(Set(cols.map(\.height)).count == 1)
        #expect(cols[0].height > Theme.DictationBar.dot)
    }

    @Test func meterMapsDecibelsOntoZeroToOne() {
        #expect(DictationMeter.normalized(rms: 0) == 0)
        #expect(DictationMeter.normalized(rms: 1) == 1)
        let floor = Float(pow(10, Double(Theme.DictationBar.floorDB) / 20))
        #expect(DictationMeter.normalized(rms: floor) < 0.001)
        let mid = DictationMeter.normalized(rms: Float(pow(10, Double(Theme.DictationBar.floorDB + Theme.DictationBar.ceilingDB) / 40)))
        #expect(abs(mid - 0.5) < 0.01)
    }

    @Test func syntheticVoiceStaysInRangeAndPauses() {
        let levels = stride(from: 0.0, through: 20, by: 0.01).map(DictationMeter.syntheticLevel)
        #expect(levels.allSatisfy { $0 >= 0 && $0 <= 1 })
        #expect(levels.contains { $0 > 0.6 })
        #expect(levels.contains { $0 < 0.02 })
    }
}
