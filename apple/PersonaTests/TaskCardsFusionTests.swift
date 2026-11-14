import Foundation
import Testing
@testable import Persona
import PersonaWire

/// docs/design/task-cards.md (#5569): the usage limit's reading, a decision round's Later / Don't handle, a plan task's
/// brief, the receipt's actions and the hand-off card's wake.
@MainActor
struct TaskCardsFusionTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private func iso(_ minutesAgo: Double) -> String { (now - minutesAgo * 60).formatted(.iso8601) }

    private func decode<T: Decodable>(_ json: [String: Any]) -> T {
        try! JSON.decoder.decode(T.self, from: JSONSerialization.data(withJSONObject: json))
    }

    private func session(state: String = "idle", resets: Double? = nil, resumes: Bool? = nil, kind: String = "usage_limit",
                         work: [String: Any]? = nil) -> Session {
        var s: [String: Any] = ["machine_id": "mac", "session_id": "s", "title": "s", "cwd": "/p", "state": state, "status": state,
                                "updated_at": iso(5), "turn_error": "You've hit your usage limit.", "turn_error_kind": kind]
        if let resets { s["turn_error_resets_at"] = iso(-resets) }
        if let resumes { s["turn_error_resumes"] = resumes }
        if let work { s["work"] = work }
        return decode(s)
    }

    // MARK: Usage limit

    @Test func usageLimitFieldsDecode() {
        let s = session(resets: 180, resumes: true)
        #expect(s.turnErrorResumes == true)
        #expect(s.turnErrorResetsAt == now.addingTimeInterval(180 * 60))
        let w: WorkState = decode(["phase": "blocked", "on": "external", "resets_at": iso(-60)])
        #expect(w.resetsAt == now.addingTimeInterval(3600))
    }

    @Test func aLimitIsOverTenMinutesAfterItsReset() {
        let past = session(resets: -9)
        #expect(past.isUsageLimited(now: now))
        #expect(!session(resets: -11).isUsageLimited(now: now))
        // not known: still a limit; not stopped: not one; another kind of error: not one
        #expect(session().isUsageLimited(now: now))
        #expect(!session(state: "working", resets: 60).isUsageLimited(now: now))
        #expect(!session(resets: 60, kind: "rate_limited").isUsageLimited(now: now))
    }

    @Test func continuesWhenTheCLISaysSoOrTheHubScheduledIt() {
        #expect(session(resets: 60, resumes: true).usageLimitContinues)
        #expect(!session(resets: 60).usageLimitContinues)
        let scheduled = session(resets: 60, work: ["phase": "blocked", "resets_at": iso(-60), "wake_at": iso(-61)])
        #expect(scheduled.usageLimitContinues)
        // another wait's wake_at does not count
        #expect(!session(resets: 60, work: ["phase": "blocked", "wake_at": iso(-61)]).usageLimitContinues)
    }

    @Test func theLabelSaysResumesOrResetsWithTheClock() throws {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        calendar.locale = Locale(identifier: "en_US_POSIX")
        // the words follow the device's language: compare with the same strings, not with English
        let time = Session.usageLimitClock(now.addingTimeInterval(120 * 60), now: now, calendar: calendar)
        let resumes = try #require(session(resets: 120, resumes: true).usageLimitLabel(now: now, calendar: calendar))
        #expect(resumes == String(localized: "Usage Limit · resumes \(time)"))
        let resets = try #require(session(resets: 120).usageLimitLabel(now: now, calendar: calendar))
        #expect(resets == String(localized: "Usage Limit · resets \(time)"))
        #expect(resumes != resets)
        #expect(session(kind: "other").usageLimitLabel(now: now) == nil)
        #expect(session(resets: 120).usageLimitWhen(now: now, calendar: calendar) == String(localized: "Resets \(time)"))
        #expect(session(resets: 120, resumes: true).usageLimitWhen(now: now, calendar: calendar) == String(localized: "Resumes \(time)"))
        #expect(session(resets: -60).usageLimitWhen(now: now, calendar: calendar) == nil)
    }

    @Test func theClockIsTodaysTimeOrTheDateAsWell() {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        calendar.locale = Locale(identifier: "en_US_POSIX")
        let noon = Date(timeIntervalSince1970: 1_800_000_000 - Double(Int(1_800_000_000) % 86_400) + 12 * 3600)
        let today = Session.usageLimitClock(noon.addingTimeInterval(3600), now: noon, calendar: calendar)
        let other = Session.usageLimitClock(noon.addingTimeInterval(2 * 86_400), now: noon, calendar: calendar)
        #expect(today.contains("1") && !today.contains("/"))
        #expect(other.contains("/") || other.count > today.count)
    }

    /// The mock hub's MOCK_USAGE_LIMIT payload as it goes over the wire (whole-second `Z` times beside microsecond ones).
    @Test func theMocksUsageLimitSessionDecodesAndReadsAsALimit() throws {
        let json = #"""
        {"machine_id":"macbook","session_id":"a9e0c000-0000-4000-8000-000000000001","title":"x","cwd":"/p","status":"done","state":"idle",
         "updated_at":"2026-11-12T19:42:54.322265Z","last_text":"You've hit your usage limit.","turn_error":"You've hit your usage limit.",
         "turn_error_kind":"usage_limit","turn_error_resets_at":"2026-11-12T22:44:54Z","turn_error_resumes":true,
         "work":{"phase":"blocked","on":"external","resets_at":"2026-11-12T22:44:54Z","line":"x"},
         "agent":"claude","launcher":"claude","model":"opus","started_by_persona":true,"assistant":false,"space_kind":"herdr","space_id":"w1",
         "space_name":"weather","space_index":1,"slot_id":"w1:t8","slot_name":"limit","slot_index":8,"pane_id":"w1:p8","tab_id":"w1:t8",
         "workspace_id":"w1","pending_interactions":0,"rev":1000,"permission_mode":"default","effort":"high","can_fork":true,
         "todo":{"id":"todo_usage_limit","title":"x"},"manager":"m-3fa21c"}
        """#
        let s = try JSON.decoder.decode(Session.self, from: Data(json.utf8))
        let resets = try #require(s.turnErrorResetsAt)
        #expect(s.turnErrorKind == "usage_limit")
        #expect(s.turnErrorResumes == true)
        #expect(s.isUsageLimited(now: resets.addingTimeInterval(-3 * 3600)))
        #expect(s.usageLimitContinues)
        #expect(s.usageLimitWhen(now: resets.addingTimeInterval(-3 * 3600)) != nil)
        let todo: TodoItem = decode(["id": "t", "title": "x", "origin": "main", "created_at": iso(5), "updated_at": iso(5),
                                     "agents": [["machine_id": "macbook", "session_id": s.sessionID, "linked_at": iso(5)]]])
        #expect(AssistantEntries.todoStatus(todo, lookup: { _, _ in s }) == .usageLimit)
    }

    // MARK: Decision rounds

    private func todo(round: String? = "2026-11-12T01:02:03.456789Z", dismissal: String? = nil, manager: String? = nil) -> TodoItem {
        var t: [String: Any] = ["id": "t1", "title": "Pick a cache", "origin": "main", "created_at": iso(30), "updated_at": iso(30)]
        if let round {
            var review: [String: Any] = ["review_at": round]
            if let dismissal { review["dismissal_id"] = dismissal }
            t["decision_review"] = review
        }
        if let manager { t["manager"] = manager }
        return decode(t)
    }

    @Test func decisionReviewKeepsTheHubsOwnRoundString() {
        let t = todo(dismissal: "d1")
        #expect(t.decisionReview?.reviewAt == "2026-11-12T01:02:03.456789Z")
        #expect(t.decisionReview?.dismissed == true)
        #expect(todo().decisionReview?.dismissed == false)
        #expect(todo(round: nil).decisionReview == nil)
    }

    @Test func aDecisionIsHiddenByLaterOrDismissalOfItsOwnRoundOnly() {
        let store = TasksStore(toast: { _ in })
        let t = todo(manager: "owner")
        #expect(TasksStore.decisionKey(t) == "decision:owner:t1:2026-11-12T01:02:03.456789Z")
        #expect(!store.decisionHidden(t))
        store.laterDecision(t)
        #expect(store.decisionHidden(t))
        // a new round is a new question
        #expect(!store.decisionHidden(todo(round: "2026-11-12T02:00:00Z", manager: "owner")))
        // the hub's own dismissal hides it too, with nothing local
        #expect(TasksStore(toast: { _ in }).decisionHidden(todo(dismissal: "d1")))
    }

    @Test func splitLeavesOutAHiddenDecisionButNotAnAgentQuestion() {
        let decision: TodoItem = decode(["id": "t1", "title": "x", "origin": "main", "created_at": iso(30), "updated_at": iso(30),
                                         "supervision": ["supervisor": "main", "state": "needs_user"],
                                         "decision_review": ["review_at": "2026-11-12T01:02:03Z"]])
        func rows(hidden: Bool) -> [String] {
            AssistantEntries.split(conversation: "main", main: "main", todos: [decision], sessions: [], interactions: [], plans: [:], feed: [],
                                   now: now, decisionHidden: { _ in hidden }).needsYou.map(\.id)
        }
        #expect(rows(hidden: false) == ["task:t1"])
        #expect(rows(hidden: true).isEmpty)
    }

    @Test func dismissAndUndoSendTheRoundAndTheReceiptWithTheManager() async throws {
        let hub = FakeHub()
        var toasts: [Toast] = []
        let store = TasksStore(toast: { toasts.append($0) })
        let t = todo(manager: "owner")
        hub.on("POST", "/api/v1/todos/t1/decision-dismiss", json: #"{"review_at":"2026-11-12T01:02:03.456789Z","dismissal_id":"d1"}"#)
        hub.on("POST", "/api/v1/todos/t1/decision-undo-dismiss", json: #"{"review_at":"2026-11-12T01:02:03.456789Z"}"#)
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[]}"#)
        #expect(await store.dismissDecision(t, api: hub.client))
        let dismiss = try #require(hub.sent("POST", "/api/v1/todos/t1/decision-dismiss").first)
        #expect(try JSONSerialization.jsonObject(with: #require(dismiss.body)) as? [String: String] == ["review_at": "2026-11-12T01:02:03.456789Z"])
        #expect(store.decisionHidden(t))
        #expect(toasts.last?.action != nil)
        #expect(toasts.last?.duration == Theme.Motion.draftUndo)
        #expect(await store.undoDecisionDismissal(t, receipt: "d1", api: hub.client))
        let undo = try #require(hub.sent("POST", "/api/v1/todos/t1/decision-undo-dismiss").first)
        #expect(try JSONSerialization.jsonObject(with: #require(undo.body)) as? [String: String]
                == ["review_at": "2026-11-12T01:02:03.456789Z", "dismissal_id": "d1"])
        #expect(!store.decisionHidden(t))
    }

    @Test func aStaleRoundIsAToastNotAHiddenRow() async {
        let hub = FakeHub()
        var toasts: [Toast] = []
        let store = TasksStore(toast: { toasts.append($0) })
        hub.on("POST", "/api/v1/todos/t1/decision-dismiss") { _ in throw APIError(status: 409, message: "the decision changed") }
        hub.on("GET", "/api/v1/todos", json: #"{"todos":[]}"#)
        #expect(await !store.dismissDecision(todo(), api: hub.client))
        #expect(!store.decisionHidden(todo()))
        #expect(toasts.last?.style == .error)
    }

    @Test func noRoundMeansNothingToDismiss() async {
        let hub = FakeHub()
        let store = TasksStore(toast: { _ in })
        #expect(await !store.dismissDecision(todo(round: nil), api: hub.client))
        #expect(hub.requests.isEmpty)
    }

    // MARK: Brief

    @Test func aPlanTaskDecodesItsBriefAndNeverSendsItBack() throws {
        let json: [String: Any] = [
            "title": "Fix", "prompt": "p", "machine": "mac", "cwd": "/a/b",
            "brief": ["request": ["text": "  Please fix the crash  ", "at": "2026-11-12T01:00:00Z"], "started_by": "owner",
                      "goal": "Fix the crash", "done_when": ["tests pass", "PR open"], "expect_minutes": 45, "out_of_bounds": ["the API"]]]
        let task: PlanTask = decode(json)
        let brief = try #require(task.brief)
        #expect(brief.asked == .owner(text: "Please fix the crash", truncated: false, at: brief.request?.at))
        #expect(brief.criteriaNow == ["tests pass", "PR open"])
        #expect(brief.summary == [String(localized: "\(2) criteria"), String(localized: "about \(45) min"), String(localized: "\(1) out of bounds")].joined(separator: " · "))
        let sent = try JSONSerialization.jsonObject(with: JSONEncoder().encode(task)) as? [String: Any]
        #expect(sent?["brief"] == nil)
        #expect(sent?["title"] as? String == "Fix")
        // one that does not decode is as if there were none
        let broken: PlanTask = decode(["title": "Fix", "prompt": "p", "machine": "mac", "cwd": "/a", "brief": ["request": 5]])
        #expect(broken.brief == nil)
    }

    @Test func theBriefsFirstLineFollowsWhoStartedIt() {
        func brief(_ json: [String: Any]) -> TaskBrief { decode(json) }
        #expect(brief(["reconstructed": true]).asked == .reconstructed)
        if case .started(let by, let why, _) = brief(["started_by": "routine", "reason": "weekly"]).asked { #expect((by, why) == ("routine", "weekly")) } else { Issue.record("started") }
        if case .unknown = brief(["started_by": "nobody"]).asked {} else { Issue.record("unknown") }
        #expect(brief(["criteria": ["a"], "done_when": ["x", "y"], "expect_minutes": 100, "expect_minutes_now": 150]).summary
                == [String(localized: "1 criterion"), String(localized: "about \(TaskBrief.hoursText(150)) h")].joined(separator: " · "))
        #expect(brief([:]).summary.isEmpty)
        #expect(brief(["goal": "Same"]).hasMore(title: "Same") == false)
        #expect(brief(["goal": "More"]).hasMore(title: "Same"))
    }

    // MARK: Receipt, hand-off

    @Test func receiptActionsFollowTheOutcome() {
        typealias R = ReceiptCard
        #expect(R.action(.failed, todoFailed: true, stillInterrupted: false, limitContinues: false) == .retry)
        #expect(R.action(.failed, todoFailed: false, stillInterrupted: false, limitContinues: false) == nil)
        #expect(R.action(.interrupted(reason: nil), todoFailed: false, stillInterrupted: true, limitContinues: false) == .continueWork)
        #expect(R.action(.interrupted(reason: nil), todoFailed: false, stillInterrupted: true, limitContinues: true) == nil)
        #expect(R.action(.interrupted(reason: nil), todoFailed: false, stillInterrupted: false, limitContinues: false) == nil)
        #expect(R.action(.done, todoFailed: true, stillInterrupted: true, limitContinues: false) == nil)
        #expect(R.action(.stopped, todoFailed: false, stillInterrupted: false, limitContinues: false) == nil)
        #expect(R.shareText(heading: "T is done", detail: "All good") == "T is done\n\nAll good")
        #expect(R.shareText(heading: "T is done", detail: nil) == "T is done")
    }

    @Test func aTaskItemCarriesItsWakeToItsHandoff() throws {
        func item(_ wake: String?) -> FeedItem {
            FeedItem(id: "f", conversation: "main", at: now, kind: .task, ref: "one", body: wake.map { .object(["wake": .string($0)]) })
        }
        for wake in ["created", "active", "resumed"] {
            let entries = try #require(DispatchedTasks.entries(for: item(wake), plans: [:], titleOf: { _ in "T" }, fallback: "Task"))
            #expect(entries.first?.wake == wake)
        }
        #expect(try #require(DispatchedTasks.entries(for: item(nil), plans: [:], titleOf: { _ in "T" }, fallback: "Task")).first?.wake == nil)
    }

    @Test func aFailedPlanTaskKeepsItsReasonForTheHandoff() throws {
        let failed = PlanTask(title: "A", prompt: "p", machine: "m", cwd: "/x", error: "machine offline")
        let plan = TaskPlan(id: "p", conversation: "main", tasks: [failed], state: .started)
        let item = FeedItem(id: "f", conversation: "main", at: now, kind: .plan, ref: "p", body: nil)
        let entries = try #require(DispatchedTasks.entries(for: item, plans: ["p": plan], titleOf: { _ in nil }, fallback: "Task"))
        #expect(entries.first?.error == "machine offline")
        #expect(entries.first?.todoID == nil)
    }
}
