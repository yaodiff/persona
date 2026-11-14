import Foundation
import PersonaScreenKit
import XCTest

/// A lost direct route is dialed again when its retry is due (#7791): the probe plan says when, the wake fires then.
final class ProbeRetryTests: XCTestCase {
    func testTheNextRetry() {
        var p = ProbePlan()
        XCTAssertNil(p.nextRetry(after: 0), "nothing waits")
        XCTAssertEqual(p.due(["lan", "tailscale"], now: 0), ["lan", "tailscale"])
        XCTAssertNil(p.nextRetry(after: 0), "both dialing")
        p.failed("lan", now: 2)
        XCTAssertEqual(p.nextRetry(after: 2), 32, "the first wait")
        XCTAssertEqual(p.due(["lan", "tailscale"], now: 32), ["lan"], "due then")
        XCTAssertNil(p.nextRetry(after: 32), "dialing again")
        p.failed("lan", now: 33)
        XCTAssertEqual(p.nextRetry(after: 33), 93, "the wait doubled")
        XCTAssertNil(p.nextRetry(after: 93), "due already: the probe running now dials it, else the next network change")
    }

    func testANetworkChangeDialsAtOnce() {
        var p = ProbePlan()
        _ = p.due(["lan"], now: 0)
        p.failed("lan", now: 1)
        p.networkChanged()
        XCTAssertNil(p.nextRetry(after: 2), "nothing waits")
        XCTAssertEqual(p.due(["lan"], now: 2), ["lan"])
        p.failed("lan", now: 3)
        XCTAssertEqual(p.nextRetry(after: 3), 33, "and the wait starts from the first")
    }

    func testASuccessClearsTheBackoff() {
        var p = ProbePlan()
        _ = p.due(["lan"], now: 0)
        p.failed("lan", now: 0)
        _ = p.due(["lan"], now: 30)
        p.failed("lan", now: 30)
        _ = p.due(["lan"], now: 90)
        p.attached("lan")
        XCTAssertNil(p.nextRetry(after: 90), "attached: nothing waits")
        p.failed("lan", now: 100)
        XCTAssertEqual(p.nextRetry(after: 100), 130, "lost after it attached: 30 s again, not 2 min")
    }

    func testTheWakeDialsALostRouteAgainWhenItsRetryIsDue() async {
        let clock = RetryClock()
        let dialed = (0..<3).map { expectation(description: "dial \($0)") }
        let prober = RetryProber(clock: clock) { dialed[$0].fulfill() }
        await prober.probe()
        await fulfillment(of: [dialed[0]], timeout: 1)
        await prober.lost()
        await fulfillment(of: [dialed[1]], timeout: 1)  // only the wake dials it: nothing else calls probe here
        await prober.lost()
        await fulfillment(of: [dialed[2]], timeout: 1)
        let times = await prober.dials
        XCTAssertEqual(times, [0, 30, 90], "at the start, 30 s after the loss, 60 s after the next")
        XCTAssertEqual(clock.slept, [30, 60])
    }

    func testARearmedWakeReplacesThePendingOne() async {
        let wake = RetryWake { s in if s > 1 { try? await Task.sleep(for: .seconds(s)) } }
        let first = expectation(description: "the replaced wake")
        first.isInverted = true
        let second = expectation(description: "the new wake")
        wake.arm(in: 60) { first.fulfill() }
        wake.arm(in: 0) { second.fulfill() }
        await fulfillment(of: [second], timeout: 1)
        await fulfillment(of: [first], timeout: 0.2)
    }

    func testACancelledWakeNeverFires() async {
        let returned = expectation(description: "the sleep ended")
        let wake = RetryWake { s in
            try? await Task.sleep(for: .seconds(s))
            returned.fulfill()
        }
        let fired = expectation(description: "fired")
        fired.isInverted = true
        wake.arm(in: nil) { fired.fulfill() }  // nil arms nothing
        wake.arm(in: 60) { fired.fulfill() }
        wake.cancel()
        await fulfillment(of: [returned], timeout: 1)
        await fulfillment(of: [fired], timeout: 0.2)
    }
}

/// A test's clock: each sleep moves it on at once by what was asked.
final class RetryClock: @unchecked Sendable {
    private let lock = NSLock()
    private var t = 0.0
    private var asked: [Double] = []
    var now: Double { lock.withLock { t } }
    var slept: [Double] { lock.withLock { asked } }
    func sleep(_ s: Double) {
        lock.withLock {
            asked.append(s)
            t += s
        }
    }
}

/// The app's prober as it uses the plan and the wake, with one candidate and no network: a probe dials what is due
/// and arms the wake; a lost route fails in the plan and arms it.
actor RetryProber {
    private let clock: RetryClock
    private let wake: RetryWake
    private let onDial: @Sendable (Int) -> Void
    private var plan = ProbePlan()
    private(set) var dials: [Double] = []

    init(clock: RetryClock, onDial: @escaping @Sendable (Int) -> Void) {
        self.clock = clock
        self.onDial = onDial
        wake = RetryWake { clock.sleep($0) }
    }

    func probe() {
        for _ in plan.due(["lan"], now: clock.now) {
            dials.append(clock.now)
            onDial(dials.count - 1)
        }
        arm()
    }

    func lost() {
        plan.failed("lan", now: clock.now)
        arm()
    }

    private func arm() {
        let now = clock.now
        wake.arm(in: plan.nextRetry(after: now).map { $0 - now }) { [weak self] in await self?.probe() }
    }
}
