import CoreGraphics
import Foundation

/// Two fingers on the Mac's screen turned into its wheel (#4878, #7775), in notches (> 0 up: the fingers going down
/// show what is above, as everywhere on iOS and on a Mac with natural scrolling). macOS's Screen Sharing scrolls a notch
/// one point (measured on macOS 26.6: a browser page and Finder move one pixel a notch however fast they come; a
/// terminal one line), so a notch is a point of the Mac's screen.
/// - Speed: while the fingers go no faster than `precise`, the Mac's content moves as far as they travel over its
///   picture (`perPoint`, Mac points a view point at the current zoom), as if dragging it; faster, each point counts for
///   more, the gain rising in a straight line to `most` at `fastest`, as macOS speeds up quick scrolls.
/// - Paced: the fingers and the coast only move where the wheel should be; each display frame sends what that has come
///   to, following it about `lag` behind, at most `maxRate` notches a second (beyond it Screen Sharing falls behind by
///   seconds: measured at 6000 a second), what is left the next frames. No more than `backlog` seconds' worth waits:
///   faster fingers than that are held to the top speed rather than have the Mac scroll on after they stop. The
///   nearest whole notch is sent; what is left over stays for the next move, and the fingers going back take it back.
/// - Let go at `flick` or faster, it coasts on from the speed the notches were going, dying away as a UIScrollView's
///   normal deceleration does, sent no faster than `maxRate`: a harder flick keeps that top pace for longer before it
///   slows, so it goes further. A touch stops it at once.
nonisolated struct VNCWheel {
    /// Up to this finger speed (points a second) the content follows the fingers one to one.
    static let precise: CGFloat = 250
    /// From here on (points a second) every point counts `most` times.
    static let fastest: CGFloat = 2000
    static let most: CGFloat = 4
    /// The most notches a second (Screen Sharing keeps up with 2000; at 6000 a page went on scrolling 6.5 s after).
    static let maxRate: CGFloat = 2000
    /// The most seconds' worth of notches waiting to be sent.
    static let backlog: CGFloat = 0.1
    /// The slowest letting go (points a second) that coasts.
    static let flick: CGFloat = 250
    /// What is left of the coasting speed after a millisecond (UIScrollView's normal deceleration).
    static let decay: CGFloat = 0.998
    /// Below this (notches, Mac points, a second) the coast stops.
    static let stopSpeed: CGFloat = 30
    /// How far behind the wheel's position the sending follows (seconds, about a frame).
    static let lag: TimeInterval = 0.012

    /// How many times a point of finger travel counts at this speed (points a second, either way).
    static func gain(_ speed: CGFloat) -> CGFloat {
        1 + (most - 1) * min(max((abs(speed) - precise) / (fastest - precise), 0), 1)
    }

    /// The most notches a display frame dt seconds long sends.
    static func perFrame(_ dt: TimeInterval) -> Int { max(1, Int((maxRate * CGFloat(dt)).rounded())) }

    /// Where the wheel should be, in notches since the touch began; where the sending has come to; and what was sent.
    private var total: CGFloat = 0
    private var paced: CGFloat = 0
    private var sent = 0
    /// The coast's speed, in notches a second, as it dies away (what is sent keeps to `maxRate`); 0: not coasting.
    private(set) var speed: CGFloat = 0

    var coasting: Bool { speed != 0 }
    /// Coasting, or notches still to send: the display frames go on.
    var busy: Bool { coasting || Int(total.rounded()) != sent }

    /// Fingers down: what was left over and any coast go.
    mutating func began() {
        self = VNCWheel()
    }

    /// The fingers moved dy points down (up if < 0) at velocity points a second, perPoint Mac points a view point.
    /// Nothing is sent until the next frame.
    mutating func moved(_ dy: CGFloat, velocity: CGFloat, perPoint: CGFloat) {
        total += dy * Self.gain(velocity) * max(perPoint, 0)
        hold()
    }

    /// The fingers let go at velocity points a second: coasts if that is fast enough, from the speed the notches were
    /// going at.
    mutating func released(velocity: CGFloat, perPoint: CGFloat) {
        guard abs(velocity) >= Self.flick else { return }
        let v = velocity * Self.gain(velocity) * max(perPoint, 0)
        speed = abs(v) < Self.stopSpeed ? 0 : v
    }

    /// A display frame dt seconds long: the notches to send now.
    mutating func frame(_ dt: TimeInterval) -> Int {
        if coasting {
            let after = speed * pow(Self.decay, CGFloat(dt) * 1000)
            total += (speed < 0 ? -1 : 1) * Self.covered(from: abs(speed), to: abs(after), over: CGFloat(dt))
            speed = abs(after) < Self.stopSpeed ? 0 : after
            hold()
        }
        paced += (total - paced) * CGFloat(1 - exp(-dt / Self.lag))
        if abs(total - paced) < 0.05 { paced = total } // there: the last notch is not left hanging
        let most = Self.perFrame(dt)
        let n = max(-most, min(most, Int(paced.rounded()) - sent))
        sent += n
        return n
    }

    /// A touch: the coast stops where it is, and what was not sent yet is not.
    mutating func stop() {
        speed = 0
        total = CGFloat(sent)
        paced = total
    }

    /// The distance a speed dying away from v₀ to v₁ over dt seconds covers, kept to `maxRate`, exactly: (v₀ − v₁)·τ
    /// below it (τ = −1 / ln(decay) ms), `maxRate`·dt above it, and both parts when it slows to it in between.
    static func covered(from v0: CGFloat, to v1: CGFloat, over dt: CGFloat) -> CGFloat {
        let tau = -1 / log(decay) / 1000
        if v0 <= maxRate { return (v0 - v1) * tau }
        if v1 >= maxRate { return maxRate * dt }
        let atTop = tau * log(v0 / maxRate) // seconds until it is down to maxRate
        return maxRate * atTop + (maxRate - v1) * tau
    }

    /// No more than `backlog` seconds' worth waits.
    private mutating func hold() {
        let most = Self.maxRate * Self.backlog
        total = min(max(total, CGFloat(sent) - most), CGFloat(sent) + most)
        paced = min(max(paced, CGFloat(sent) - most), CGFloat(sent) + most)
    }
}

/// How far two fingers scroll a computer's screen (#7775): Settings › Live Screen › Scroll Speed, this device's, for
/// every computer. It scales the whole of VNCWheel's curve, slow drags too; the top speed stays Screen Sharing's.
nonisolated enum VNCScrollSpeed: String, CaseIterable, Sendable {
    case slower, normal = "default", faster

    static let key = "screen.scroll.speed"

    /// As kept on this device: the default unless chosen.
    static var current: VNCScrollSpeed { VNCScrollSpeed(rawValue: UserDefaults.standard.string(forKey: key) ?? "") ?? .normal }

    /// Times the default's distance.
    var factor: CGFloat {
        switch self {
        case .slower: 0.6
        case .normal: 1
        case .faster: 1.6
        }
    }

    var title: String {
        switch self {
        case .slower: String(localized: "Slower")
        case .normal: String(localized: "Default")
        case .faster: String(localized: "Faster")
        }
    }
}
