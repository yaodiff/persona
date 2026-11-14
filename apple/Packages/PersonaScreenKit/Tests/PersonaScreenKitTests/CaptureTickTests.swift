import PersonaScreenKit
import XCTest

/// The capture source's once-a-second check (#3954): a heartbeat while the screen is still, `paused` when the Mac
/// locks or leaves the console, `resumed` when it comes back. These are the paths the real-Mac run couldn't show
/// (someone was using the screen, and locking it would have locked them out), so they're covered here.
final class CaptureTickTests: XCTestCase {
    private let second: UInt64 = 1_000_000

    func testAStillScreenGetsAHeartbeatEachSecond() {
        XCTAssertEqual(CaptureTick.decide(session: nil, paused: nil, capturing: true, sinceLastSent: second + 1), .heartbeat)
        XCTAssertEqual(CaptureTick.decide(session: nil, paused: nil, capturing: true, sinceLastSent: second), .heartbeat)
        XCTAssertEqual(CaptureTick.decide(session: nil, paused: nil, capturing: true, sinceLastSent: second / 2), .none,
                       "a frame went out within the second")
        XCTAssertEqual(CaptureTick.decide(session: nil, paused: nil, capturing: false, sinceLastSent: 5 * second), .none,
                       "no stream yet (still opening): nothing to keep alive")
        XCTAssertEqual(CaptureTick.decide(session: nil, paused: "permission", capturing: false, sinceLastSent: 5 * second), .none,
                       "paused: the phone is on RFB, no heartbeats")
    }

    /// The source's tick, carried out the way ScreenCaptureSource does, over a timeline of what the session says.
    private struct Simulated {
        var paused: String?
        var capturing = true
        var sent: [String] = []

        mutating func tick(_ session: String?, sinceLastSent: UInt64 = 0) {
            switch CaptureTick.decide(session: session, paused: paused, capturing: capturing, sinceLastSent: sinceLastSent) {
            case .none: break
            case let .pause(reason):
                paused = reason
                capturing = false
                sent.append("paused:\(reason)")
            case .resume:
                paused = nil
                capturing = true
                sent.append("resumed")
            case .heartbeat:
                sent.append("heartbeat")
            }
        }
    }

    func testLockingPausesOnceAndUnlockingResumes() {
        var s = Simulated()
        s.tick(nil)
        s.tick(ConsoleSession.locked)
        s.tick(ConsoleSession.locked)
        s.tick(ConsoleSession.locked)
        s.tick(nil)
        s.tick(nil)
        XCTAssertEqual(s.sent, ["paused:locked", "resumed"], "one pause for the whole lock, one resume")
        XCTAssertNil(s.paused)
        XCTAssertTrue(s.capturing)
    }

    func testTheLoginWindowPausesAndComingBackResumes() {
        var s = Simulated()
        s.tick(ConsoleSession.loginWindow) // another user, or the login window
        s.tick(ConsoleSession.locked) // back at this user's lock screen: still paused, nothing new to say
        s.tick(nil)
        XCTAssertEqual(s.sent, ["paused:login_window", "resumed"])
    }

    func testNoPermissionStaysPausedThroughALock() {
        var s = Simulated(paused: "permission", capturing: false)
        s.tick(ConsoleSession.locked)
        s.tick(nil)
        XCTAssertEqual(s.sent, [], "unlocking doesn't grant Screen Recording: the phone retries video on its own")
        XCTAssertEqual(s.paused, "permission")
    }

    func testHeartbeatsStopWhileLockedAndComeBackAfter() {
        var s = Simulated()
        s.tick(nil, sinceLastSent: 2 * second)
        s.tick(ConsoleSession.locked, sinceLastSent: 3 * second)
        s.tick(ConsoleSession.locked, sinceLastSent: 4 * second)
        s.tick(nil, sinceLastSent: 5 * second)
        s.tick(nil, sinceLastSent: 2 * second)
        XCTAssertEqual(s.sent, ["heartbeat", "paused:locked", "resumed", "heartbeat"])
    }

    func testTheConsoleSessionDictionary() {
        XCTAssertEqual(ConsoleSession.reason(from: nil), ConsoleSession.loginWindow, "no session at all")
        XCTAssertEqual(ConsoleSession.reason(from: ["kCGSSessionOnConsoleKey": false]), ConsoleSession.loginWindow)
        XCTAssertEqual(ConsoleSession.reason(from: ["kCGSSessionOnConsoleKey": true, "CGSSessionScreenIsLocked": true]),
                       ConsoleSession.locked)
        XCTAssertNil(ConsoleSession.reason(from: ["kCGSSessionOnConsoleKey": true, "CGSSessionScreenIsLocked": false]))
        XCTAssertNil(ConsoleSession.reason(from: ["kCGSSessionOnConsoleKey": true]), "unlocked: the key is absent")
        XCTAssertEqual(ConsoleSession.reason(from: ["kCGSSessionOnConsoleKey": false, "CGSSessionScreenIsLocked": true]),
                       ConsoleSession.loginWindow, "away from the console wins")
    }
}
