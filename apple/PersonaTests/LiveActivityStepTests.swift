import Foundation
import Testing
@testable import Persona

/// What the app does with the fleet while it draws the Live Activity itself (in front: the hub pushes nothing then).
/// Only the assistant answering is something to show on an island that shows it (#4628, the app's side of #4590).
struct LiveActivityStepTests {
    private func state(phase: FleetState.Phase = .none, assistant: Bool = false, island: IslandSlot = .assistant) -> FleetState {
        var s = FleetState(phase: phase, assistant: assistant)
        (s.left, s.right) = (.status, island)
        return s
    }

    struct Case: Sendable, CustomTestStringConvertible {
        let label: String
        var phase: FleetState.Phase = .none
        var assistant = false
        var island: IslandSlot = .assistant
        let running: Bool
        let active: Bool
        let expected: LiveActivityController.Step
        var testDescription: String { label }
    }

    static let cases: [Case] = [
        // No agent working, nothing unread: only the assistant answering, its side on the island (Thinking).
        Case(label: "assistant answering, running, active", assistant: true, running: true, active: true, expected: .update),
        Case(label: "assistant answering, running, inactive", assistant: true, running: true, active: false, expected: .update),
        // Once the assistant is done the minute runs as before.
        Case(label: "assistant done, running", running: true, active: true, expected: .endSoon),
        // An island that does not show the assistant has nothing to show.
        Case(label: "assistant on a tokens island", assistant: true, island: .tokens, running: true, active: true, expected: .endSoon),
        // A product decision left open in #4628: only an activity already on is kept.
        Case(label: "assistant alone, not running", assistant: true, running: false, active: true, expected: .nothing),
        // Agents start and update as before.
        Case(label: "working, not running, active", phase: .working, running: false, active: true, expected: .start),
        Case(label: "working, not running, inactive", phase: .working, running: false, active: false, expected: .nothing),
        Case(label: "done, not running", phase: .done, running: false, active: true, expected: .nothing),
        Case(label: "needs you, running", phase: .needsYou, running: true, active: true, expected: .update),
        Case(label: "empty, not running", running: false, active: true, expected: .nothing),
    ]

    @Test(arguments: cases)
    func stepFollowsTheFleetAndTheActivity(_ c: Case) {
        let s = state(phase: c.phase, assistant: c.assistant, island: c.island)
        #expect(LiveActivityController.step(s, running: c.running, active: c.active) == c.expected, "\(c.label)")
    }

    /// The last request answered elsewhere while the app is in front: off its activity at once, with the buttons, not
    /// at the end a minute later (#8636). Anything else that empties the fleet waits for the end, as on the hub.
    @Test func theLastRequestGoingClearsAtOnce() {
        let ask = FleetState.Item(id: "m/s", title: "api", place: "", kind: .permission, detail: "Bash: go test", interaction: "ix1", since: 0)
        var asking = state(phase: .needsYou)
        (asking.needsYou, asking.items) = (1, [ask])
        let work = FleetState.Item(id: "m/w", title: "api", place: "", kind: .working, since: 0)
        var working = state(phase: .working)
        (working.working, working.items) = (1, [work])
        #expect(LiveActivityController.step(state(), running: true, active: true) == .endSoon)
        #expect(LiveActivityController.clearsNow(asking, to: state()))
        // The assistant answering on a tokens island: nothing to show there either.
        #expect(LiveActivityController.clearsNow(asking, to: state(assistant: true, island: .tokens)))
        // Shown on its island, the assistant is something to show: an ordinary update, not this.
        #expect(!LiveActivityController.clearsNow(asking, to: state(assistant: true)))
        #expect(!LiveActivityController.clearsNow(working, to: state()))
        #expect(!LiveActivityController.clearsNow(state(phase: .done), to: state()))
    }

    @Test func showsNothingFollowsTheIslandsSides() {
        #expect(state().showsNothing)
        #expect(!state(assistant: true).showsNothing)
        #expect(state(assistant: true, island: .none).showsNothing)
        #expect(!state(phase: .done).showsNothing)
    }

    /// #12982: one Fleet card. The running activity updated last stays (its stale date is the latest); an ended one
    /// (iOS's eight hours, still on the Lock Screen) never does; none running keeps none.
    @Test func keepsOneActivity() {
        typealias C = LiveActivityController.Candidate
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        let ended = C(id: "ended", running: false, staleDate: now.addingTimeInterval(3600))
        let older = C(id: "older", running: true, staleDate: now)
        let newer = C(id: "newer", running: true, staleDate: now.addingTimeInterval(600))
        #expect(LiveActivityController.kept([ended, older, newer]) == "newer")
        #expect(LiveActivityController.kept([newer, older]) == "newer")
        #expect(LiveActivityController.kept([ended]) == nil)
        #expect(LiveActivityController.kept([]) == nil)
    }

    /// #16756: the hub confirming it has no push route for this phone (its token list names none for this credential)
    /// is an outage like a `none` route: nothing could update or end the activity, so it ends as the app goes to the
    /// background (#12982) and starts again in front.
    @Test func leavingEndsTheActivityWhenTheHubConfirmsNoRoute() {
        var h = PushHealth(notificationsAllowed: true, relay: .registered)
        h.read(nil, group: "g")
        #expect(LiveActivityController.endsOnLeaving(h, group: "g"))
        // A read that fails afterwards keeps the last answer, as it does for a `none` route.
        h.readFailed("The request timed out.", group: "g")
        #expect(LiveActivityController.endsOnLeaving(h, group: "g"))
        // Only that hub's group: another hub's outage says nothing about the one the activity follows.
        #expect(!LiveActivityController.endsOnLeaving(h, group: "other"))

        var none = PushHealth(notificationsAllowed: true)
        none.read(APIClient.PushRoute(route: "none", reason: "no_token"), group: "g")
        #expect(LiveActivityController.endsOnLeaving(none, group: "g"))
    }

    /// A route that reaches the phone keeps the activity: the hub's pushes update and end it. A registration that
    /// brings a route back after a confirmed missing one does too.
    @Test func leavingKeepsTheActivityWhenTheHubPushesHere() {
        var h = PushHealth(notificationsAllowed: true, relay: .registered)
        h.read(APIClient.PushRoute(route: "relay"), group: "g")
        #expect(!LiveActivityController.endsOnLeaving(h, group: "g"))
        h.read(APIClient.PushRoute(route: "direct", reason: "push_failed", detail: "503"), group: "g")
        #expect(!LiveActivityController.endsOnLeaving(h, group: "g"))

        h.read(nil, group: "g")
        h.readRegistration(APIClient.PushRoute(route: "direct"), group: "g")
        #expect(!LiveActivityController.endsOnLeaving(h, group: "g"))
    }

    /// An unknown route keeps the activity: an older hub reports no routes, a registration's missing route says nothing
    /// (#9612), and a hub not read yet or no paired hub may well push.
    @Test func leavingKeepsTheActivityWhenTheRouteIsUnknown() {
        var h = PushHealth(notificationsAllowed: true, relay: .registered)
        #expect(!LiveActivityController.endsOnLeaving(h, group: "g"))
        h.readRegistration(nil, group: "g")
        #expect(!LiveActivityController.endsOnLeaving(h, group: "g"))
        h.readFailed("offline", group: "g")
        #expect(!LiveActivityController.endsOnLeaving(h, group: "g"))
        h.read(nil, group: "g")
        #expect(!LiveActivityController.endsOnLeaving(h, group: nil))
    }
}
