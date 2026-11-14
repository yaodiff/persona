import CoreGraphics
import Testing
@testable import Persona

/// Two fingers on the Mac's screen as its wheel (#4878, #7775, VNCWheel): a notch is a Mac point; how far a drag goes at
/// each speed, small moves, notches going out a frame at a time within Screen Sharing's pace, coasting.
struct VNCWheelTests {
    /// `points` in moves of `each` points at `speed` points a second, as a pan reports them, perPoint Mac points a view
    /// point, a display frame after each (60 a second), then frames until everything was sent: the notches sent.
    private func drag(_ wheel: inout VNCWheel, _ points: CGFloat, speed: CGFloat = 120, each: CGFloat = 2,
                      perPoint: CGFloat = 1) -> Int {
        var notches = 0
        var gone: CGFloat = 0
        while abs(gone) < abs(points) {
            let d = points < 0 ? -each : each
            wheel.moved(d, velocity: points < 0 ? -speed : speed, perPoint: perPoint)
            notches += wheel.frame(1.0 / 60)
            gone += d
        }
        return notches + settle(&wheel).notches
    }

    /// Display frames until nothing is left to send and no coast: the notches, how many frames, the most in one.
    private func settle(_ wheel: inout VNCWheel) -> (notches: Int, frames: Int, most: Int) {
        var notches = 0, frames = 0, most = 0
        while wheel.busy, frames < 600 {
            let n = wheel.frame(1.0 / 60)
            notches += n
            most = max(most, abs(n))
            frames += 1
        }
        return (notches, frames, most)
    }

    /// Slowly, the Mac's content moves as far as the fingers over its picture: an iPhone's fitted picture of a 1920-point
    /// wide Mac is about 393 points wide, 4.9 Mac points a view point; zoomed in twice, half that.
    @Test func aSlowDragMovesTheContentAsFarAsTheFingers() {
        var w = VNCWheel()
        #expect(drag(&w, 100, perPoint: 4.9) == 490) // before #7775: 13 notches, 13 points on the Mac
        w.began()
        #expect(drag(&w, -100, perPoint: 4.9) == -490)
        w.began()
        #expect(drag(&w, 100, perPoint: 2.45) == 245)
    }

    /// Up to 250 pt a second every point counts once, from there more and more, four times from 2000 on.
    @Test func quickerDragsGoFurther() {
        #expect(VNCWheel.gain(0) == 1 && VNCWheel.gain(250) == 1 && VNCWheel.gain(-200) == 1)
        #expect(abs(VNCWheel.gain(600) - 1.6) < 0.001)
        #expect(VNCWheel.gain(2000) == 4 && VNCWheel.gain(-9000) == 4)
        var last: CGFloat = 0
        for v in stride(from: CGFloat(0), through: 3000, by: 50) {
            #expect(VNCWheel.gain(v) >= last, "never less for faster fingers")
            last = VNCWheel.gain(v)
        }
        var w = VNCWheel()
        let slow = drag(&w, 300, speed: 200)
        w.began()
        let medium = drag(&w, 300, speed: 600)
        w.began()
        let fast = drag(&w, 300, speed: 1500)
        #expect(slow == 300 && medium == 480 && fast == 943, "\(slow), \(medium), \(fast)")
    }

    @Test func aSmallMoveScrollsAndWhatIsLeftIsKept() {
        var w = VNCWheel()
        func move(_ dy: CGFloat) -> Int {
            w.moved(dy, velocity: 60, perPoint: 0.125) // zoomed far in: a notch every 8 points
            return settle(&w).notches
        }
        #expect(move(3) == 0)
        #expect(move(1) == 1) // half a notch: the first
        #expect(move(7) == 0)
        #expect(move(1) == 1) // the rest of the first notch and a half are kept, not dropped
        // Back the other way takes them back.
        #expect(move(-8) == -1)
    }

    /// Screen Sharing keeps up with 2000 notches a second and falls seconds behind at 6000: a frame sends at most a
    /// 60th of 2000, and no more than a tenth of a second's worth waits, so faster fingers are held to that speed and the
    /// Mac stops scrolling soon after they do.
    @Test func notchesKeepToScreenSharingsPace() {
        #expect(VNCWheel.perFrame(1.0 / 60) == 33 && VNCWheel.perFrame(1.0 / 120) == 17)
        var w = VNCWheel()
        w.moved(-1000, velocity: -100, perPoint: 1) // a thousand at once: two hundred go, the rest are not owed
        let first = w.frame(1.0 / 60)
        let rest = settle(&w)
        #expect(first == -33 && rest.most <= 33)
        #expect(first + rest.notches == -200, "\(first + rest.notches)")
        // A drag far faster than that: never more than 33 a frame, and once the fingers stop, a tenth of a second's worth.
        w.began()
        var most = 0
        for _ in 0..<30 {
            w.moved(20, velocity: 2400, perPoint: 4.9) // 392 a frame wanted
            most = max(most, w.frame(1.0 / 60))
        }
        let tail = settle(&w)
        #expect(most == 33 && tail.notches <= 200 && tail.frames <= 8, "\(most), \(tail)")
    }

    @Test func aSlowLetGoStopsAndAFlickCoastsFurtherTheFaster() {
        var w = VNCWheel()
        _ = drag(&w, 40)
        w.released(velocity: 200, perPoint: 1)
        #expect(!w.coasting)

        w.began()
        w.released(velocity: 600, perPoint: 1) // 960 notches a second, dying away as a UIScrollView: half a second's worth
        #expect(abs(w.speed - 600 * VNCWheel.gain(600)) < 0.001, "on at the speed the notches went")
        let medium = settle(&w)
        #expect((440...480).contains(medium.notches), "\(medium)")

        w.began()
        w.released(velocity: 1500, perPoint: 1) // 4714 a second: sent at Screen Sharing's 2000 until it slows to that
        #expect(abs(w.speed - 1500 * VNCWheel.gain(1500)) < 0.001)
        let fast = settle(&w)
        #expect((1800...1880).contains(fast.notches) && fast.most <= 33, "further than the medium one: \(fast)")

        w.began()
        w.released(velocity: -5000, perPoint: 4.9) // as hard as a finger goes: about four and a half seconds at the top pace
        let most = settle(&w)
        #expect(most.notches < -4000 && most.notches > -5000, "\(most)")
        // The distance at the top pace, worked out once and in frames, is the same.
        let whole = VNCWheel.covered(from: 4000, to: 4000 * pow(VNCWheel.decay, 500), over: 0.5)
        var parts: CGFloat = 0, v: CGFloat = 4000
        for _ in 0..<30 {
            let next = v * pow(VNCWheel.decay, 1000.0 / 60)
            parts += VNCWheel.covered(from: v, to: next, over: 1.0 / 60)
            v = next
        }
        #expect(abs(whole - parts) < 0.5, "\(whole) vs \(parts)")
    }

    @Test func aTouchStopsTheCoastAndWhatWasNotSent() {
        var w = VNCWheel()
        w.released(velocity: 2000, perPoint: 1)
        #expect(w.frame(1.0 / 60) > 0)
        w.began()
        #expect(!w.coasting && !w.busy)
        #expect(w.frame(1.0 / 60) == 0)

        w.moved(800, velocity: 100, perPoint: 1)
        #expect(w.frame(1.0 / 60) > 0)
        w.stop()
        #expect(!w.busy, "what was still to go is not sent")
        #expect(w.frame(1.0 / 60) == 0)
    }
}
