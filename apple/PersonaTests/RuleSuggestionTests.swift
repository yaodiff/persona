import Foundation
import Testing
@testable import Persona

/// A standing rule the hub suggests under a card (#5802; docs/assistant/unified-entry.md "A rule after the same answer three
/// times"): read from a plan, a to-do's decision and a question a supervisor passed up, left out by a hub from before,
/// and answered with Always and Not Now.
@MainActor
struct RuleSuggestionTests {
    private static let merge = #"""
    {"id":"rs_merge","kind":"standing_authorization","text":"You approved this 3 times. Always allow merging in /code/example on Example node?",
     "scope":{"machine_id":"example-node","machine":"Example node","directory":"/code/example","action":"merge",
              "conditions":["ci_green","no_conflicts"],"quote":"Merge the PR now? → Yes"},
     "creates":"standing_authorization","count":3}
    """#
    private static let start = #"""
    {"id":"rs_start","kind":"plan_start","text":"You started this 3 times. Always start these directly?",
     "scope":{"machine_id":"example-node","machine":"Example node","directory":"/code/example"},"creates":"tasks.trust","count":3}
    """#
    private static let delegate = #"{"id":"rs_delegate","kind":"delegate","text":"You took the recommendation 3 times. Let Persona answer agents' own-call questions for you?","scope":{},"creates":"","setting":"agents.delegate","count":3}"#

    private func suggestion(_ json: String) throws -> RuleSuggestion {
        try JSON.decoder.decode(RuleSuggestion.self, from: Data(json.utf8))
    }

    // MARK: Reading

    @Test func aPlanATodoAndACardCarryIt() throws {
        let plan = try JSON.decoder.decode(TaskPlan.self, from: Data(#"{"id":"plan_1","conversation":"c1","tasks":[],"state":"open","suggestion":\#(Self.start)}"#.utf8))
        #expect(plan.suggestion?.id == "rs_start" && plan.suggestion?.made == .taskTrust && plan.suggestion?.offered == true)
        let todo = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"todo_1","title":"Merge","ask":{"outcome":"decide","verdict":"Needs your call","asked_at":"2026-11-09T11:00:00Z","suggestion":\#(Self.merge)}}"#.utf8))
        let ask = try #require(todo.openAsk)
        #expect(ask.suggestion?.id == "rs_merge" && ask.suggestion?.made == .standingAuthorization)
        #expect(ask.row(for: todo).suggestion == ask.suggestion, "the row the decision is drawn from carries it")
        let card = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"s","kind":"question","suggestion":\#(Self.delegate)}"#.utf8))
        #expect(card.suggestion?.made == .setting("agents.delegate") && card.suggestion?.scope == RuleScope())
        // The card is kept and handed to the watch as it came.
        #expect(try JSON.decoder.decode(Interaction.self, from: JSON.encoder.encode(card)).suggestion == card.suggestion)
    }

    @Test func aHubFromBeforeSendsNoneAndNothingElseChanges() throws {
        let plan = try JSON.decoder.decode(TaskPlan.self, from: Data(#"{"id":"plan_1","conversation":"c1","tasks":[],"state":"open"}"#.utf8))
        let todo = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"todo_1","title":"Merge","ask":{"outcome":"decide","verdict":"v","asked_at":"2026-11-09T11:00:00Z"}}"#.utf8))
        let card = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"s","kind":"question"}"#.utf8))
        #expect(plan.suggestion == nil && todo.openAsk?.suggestion == nil && card.suggestion == nil)
        #expect(todo.openAsk?.isForBriefing == false && todo.openAsk?.row(for: todo).forBriefing == false)
        let model = RuleSuggestions(api: { _ in nil }, failed: { _ in }, changed: { _, _ in }, named: { _ in nil }, feedManager: { _, _ in nil })
        #expect(model.line(nil, card: .plan(id: "plan_1", conversation: "c1"), group: "") == nil)
    }

    /// A suggestion that can't be read never takes its card with it, and one this build can't describe is not offered.
    @Test(arguments: [#""not an object""#, #"{"id":7,"text":"x","creates":"tasks.trust"}"#, #"{"id":"rs","text":"","creates":"tasks.trust"}"#,
                      #"{"id":"rs","text":"Always?","creates":"something_newer"}"#, #"{"id":"rs","text":"Always?","creates":""}"#,
                      #"{"id":"rs","text":"Always?","scope":"everywhere","creates":"tasks.trust","count":"three"}"#])
    func anOddSuggestionLeavesTheCard(json: String) throws {
        let plan = try JSON.decoder.decode(TaskPlan.self, from: Data(#"{"id":"plan_1","conversation":"c1","tasks":[],"state":"open","suggestion":\#(json)}"#.utf8))
        #expect(plan.id == "plan_1" && plan.state == .open)
        let todo = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"todo_1","title":"T","ask":{"asked_at":"2026-11-09T11:00:00Z","for_briefing":"soon","suggestion":\#(json)}}"#.utf8))
        #expect(todo.openAsk != nil && todo.openAsk?.isForBriefing == false, "the decision still waits")
        let card = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix_1","machine_id":"m","session_id":"s","kind":"question","suggestion":\#(json)}"#.utf8))
        #expect(card.id == "ix_1")
        // Only the last one has all a line needs; its odd scope and count read as none.
        let readable = json.contains("everywhere")
        #expect(plan.suggestion?.offered == readable && card.suggestion?.offered == readable)
        if readable { #expect(plan.suggestion?.scope == RuleScope() && plan.suggestion?.count == 0) }
    }

    // MARK: What Always creates

    @Test func theLineSaysTheScopeAndWhatAlwaysCreates() throws {
        let merge = try suggestion(Self.merge)
        let conditions = [String(localized: "checks pass [Rule Suggestion]"), String(localized: "no conflicts [Rule Suggestion]")]
        #expect(merge.createsText == [
            String(localized: "Always creates a standing authorization to merge in \("/code/example") on \("Example node")."),
            String(localized: "Only when: \(conditions.formatted(.list(type: .and)))."),
            String(localized: "Kept as your words: “\("Merge the PR now? → Yes")”."),
        ].joined(separator: " "))
        var push = merge
        push.scope = RuleScope(machineID: "example-node", directory: "/code/example", action: "push")
        #expect(push.createsText == String(localized: "Always creates a standing authorization to push in \("/code/example") on \("example-node")."),
                "no name: the computer's id; no conditions and no words: neither sentence")
        push.scope.action = "deploy-test"
        #expect(push.createsText == String(localized: "Always creates a standing authorization to deploy to test in \("/code/example") on \("example-node")."))
        push.scope.action = "archive"
        push.scope.conditions = ["reviewed"]
        #expect(push.createsText.contains("archive") && push.createsText.contains("reviewed"), "a newer action and condition stay the hub's words")
        let start = try suggestion(Self.start)
        #expect(start.createsText == String(localized: "Always adds a rule to Auto-Start Tasks: tasks that change files in \("/code/example") on \("Example node") start without asking first."))
        var bare = start
        bare.scope = RuleScope()
        #expect(bare.createsText == String(localized: "Always adds this kind of task to Auto-Start Tasks: it starts without asking first."))
        let delegate = try suggestion(Self.delegate)
        // The hub's own line says what the setting does and that it is turned on elsewhere: the app's sentence says only
        // what the tap does, with no key in it and nothing that speaks against the hub's.
        #expect(delegate.createsText == String(localized: "Always stores no rule and turns nothing on. It only puts this suggestion away for 30 days."))
        #expect(!delegate.createsText.contains("agents.delegate"))
    }

    @Test func theResultSaysWhatWasMadeAndWhereItIsRemoved() throws {
        let merge = try suggestion(Self.merge), start = try suggestion(Self.start), delegate = try suggestion(Self.delegate)
        let trust = String(localized: "Rule added: these tasks start without asking first. It is listed, and removed, in the assistant's profile under Auto-Start Tasks.")
        let standing = String(localized: "Standing authorization added. It is listed, and revoked, in the assistant's profile under Standing Authorizations.")
        #expect(RuleSuggestionMade(kind: "plan_start", creates: "tasks.trust").text(asked: start) == trust)
        #expect(RuleSuggestionMade(kind: "standing_authorization", creates: "standing_authorization").text(asked: merge) == standing)
        #expect(RuleSuggestionMade(kind: "delegate", creates: "", setting: "agents.delegate").text(asked: delegate)
                == String(localized: "Nothing was stored and nothing was turned on. The apps have no switch for this yet: it is turned on in the assistant's configuration file on the hub's computer (\("agents.delegate"))."))
        // An answer that says only ok is read by the suggestion it answered.
        #expect(RuleSuggestionMade().text(asked: merge) == standing && RuleSuggestionMade().text(asked: start) == trust)
    }

    // MARK: Always and Not Now

    final class Seen { var failures: [String] = []; var changed: [RuleSuggestions.Card] = []; var answered: [String] = []; var now = Date(timeIntervalSince1970: 1_800_000_000) }

    private func model(_ hub: FakeHub, _ seen: Seen, named: String? = nil, feed: String? = nil) -> RuleSuggestions {
        RuleSuggestions(api: { _ in hub.client }, failed: { seen.failures.append($0) }, changed: { seen.changed.append($0); seen.answered.append($1) },
                        named: { _ in named }, feedManager: { _, _ in feed }, now: { seen.now })
    }

    private static let always = "/api/v1/assistant/rule-suggestions/rs_merge/always"
    private static let notNow = "/api/v1/assistant/rule-suggestions/rs_merge/not-now"

    @Test func alwaysMakesTheRuleAndTheCardSaysWhere() async throws {
        let hub = FakeHub(), seen = Seen()
        hub.on("POST", Self.always, json: #"{"ok":true,"kind":"standing_authorization","creates":"standing_authorization","authorization":{"id":"sa_1"},"revision":2}"#)
        let model = model(hub, seen, named: "work"), rule = try suggestion(Self.merge)
        let card = RuleSuggestions.Card.todo("todo_1", round: "2026-11-09T11:00:00Z"), other = RuleSuggestions.Card.interaction(uid: "ix_9", manager: nil)
        #expect(model.line(rule, card: card, group: "") == .offered(rule))
        await model.always(rule, card: card, group: "")
        let result = RuleSuggestions.Line.made(RuleSuggestionMade(creates: "standing_authorization").text(asked: rule))
        #expect(model.line(rule, card: card, group: "") == result)
        #expect(model.line(nil, card: card, group: "") != nil, "the result stays once the hub's copy has no suggestion any more")
        #expect(model.line(rule, card: other, group: "") == nil, "answered: gone from every other card that carries it")
        #expect(seen.changed == [card] && seen.answered == ["rs_merge"] && seen.failures.isEmpty)
        let sent = hub.sent("POST", Self.always)
        #expect(sent.count == 1 && sent[0].headers[APIClient.managerHeader] == "work", "the to-do's own manager")
        // Another tap, here or on the other card, sends nothing.
        await model.always(rule, card: card, group: "")
        await model.notNow(rule, card: other, group: "")
        #expect(hub.requests.count == 1)
    }

    @Test func notNowTakesTheLineAwayWithoutAResult() async throws {
        let hub = FakeHub(), seen = Seen()
        hub.on("POST", Self.notNow, json: #"{"ok":true}"#)
        let model = model(hub, seen, feed: "other"), rule = try suggestion(Self.merge)
        let card = RuleSuggestions.Card.plan(id: "plan_1", conversation: "c1")
        await model.notNow(rule, card: card, group: "")
        #expect(model.line(rule, card: card, group: "") == nil && model.made.isEmpty)
        #expect(hub.sent("POST", Self.notNow).first?.headers[APIClient.managerHeader] == "other", "the plan's conversation's manager")
        #expect(seen.changed == [card] && seen.answered == ["rs_merge"])
        // The app's own quiet only bridges until its copies drop the suggestion (`reread`): a copy that kept it would
        // be offered again after it.
        seen.now += RuleSuggestions.quietFor + 1
        #expect(model.line(rule, card: card, group: "") == .offered(rule))
    }

    /// The hub keeps an answered suggestion quiet for 30 days but sends no event for a plan, and a feed read brings only
    /// what came since: the plans the app holds drop the suggestion themselves, so the line does not come back once
    /// the app's own quiet hour is over.
    @Test func anAnsweredSuggestionLeavesThePlansTheAppHolds() async throws {
        let hub = FakeHub(), seen = Seen()
        hub.on("POST", "/api/v1/assistant/rule-suggestions/rs_start/not-now", json: #"{"ok":true}"#)
        let rule = try suggestion(Self.start), merge = try suggestion(Self.merge)
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {})
        let tasks = TasksStore(todoStore: store, toast: { _ in })
        let open = TaskPlan(id: "plan_1", conversation: "c1", tasks: [], state: .open, suggestion: rule)
        let same = TaskPlan(id: "plan_2", conversation: "c2", tasks: [], state: .open, suggestion: rule)
        let other = TaskPlan(id: "plan_3", conversation: "c1", tasks: [], state: .open, suggestion: merge)
        for plan in [open, same, other] { tasks.received(plan) }
        // The live path but for the reads, which need a hub: the store drops the suggestion, as `reread` has it do.
        let model = RuleSuggestions(api: { _ in hub.client }, failed: { seen.failures.append($0) },
                                    changed: { _, answered in tasks.ruleSuggestionSettled(answered) },
                                    named: { _ in nil }, feedManager: { _, _ in nil }, now: { seen.now })
        let card = RuleSuggestions.Card.plan(id: "plan_1", conversation: "c1")
        await model.notNow(rule, card: card, group: "")
        #expect(tasks.plans["plan_1"]?.suggestion == nil && tasks.plans["plan_2"]?.suggestion == nil, "every plan that carries it")
        #expect(tasks.plans["plan_3"]?.suggestion?.id == "rs_merge", "another suggestion stays")
        #expect(tasks.plans["plan_1"] == open.withoutSuggestion, "nothing else of the plan changes")
        seen.now += RuleSuggestions.quietFor + 1
        #expect(model.line(tasks.plans["plan_1"]?.suggestion, card: card, group: "") == nil, "not offered again after the app's quiet hour")
        // A stale one (404) leaves them the same way.
        tasks.received(open)
        await model.always(merge, card: .plan(id: "plan_3", conversation: "c1"), group: "")
        #expect(tasks.plans["plan_3"]?.suggestion == nil && tasks.plans["plan_1"]?.suggestion?.id == "rs_start" && seen.failures.isEmpty)
    }

    /// What Always made stays under its own decision only: a to-do's next decision is another card, and another
    /// suggestion on the same card is offered in the result's place.
    @Test func theResultStaysWithTheDecisionItAnswered() async throws {
        let hub = FakeHub(), seen = Seen()
        hub.on("POST", Self.always, json: #"{"ok":true,"kind":"standing_authorization","creates":"standing_authorization"}"#)
        let model = model(hub, seen), rule = try suggestion(Self.merge)
        let first = RuleSuggestions.Card.todo("todo_1", round: "2026-11-09T11:00:00Z")
        let next = RuleSuggestions.Card.todo("todo_1", round: "2026-11-09T15:00:00Z")
        await model.always(rule, card: first, group: "")
        var push = rule
        push.id = "rs_push"
        push.scope.action = "push"
        #expect(model.line(nil, card: next, group: "") == nil, "the next decision of the same to-do has no result of its own")
        #expect(model.line(push, card: next, group: "") == .offered(push), "and its own suggestion is offered")
        #expect(model.line(push, card: first, group: "") == .offered(push), "another suggestion on the answered card is not hidden by the result")
        guard case .made? = model.line(rule, card: first, group: "") else {
            Issue.record("the result left the card it was made on")
            return
        }
    }

    @Test func aStaleSuggestionIsCleared() async throws {
        let hub = FakeHub(), seen = Seen() // no route: 404, as a suggestion answered elsewhere or no longer earned
        let model = model(hub, seen), rule = try suggestion(Self.merge)
        let card = RuleSuggestions.Card.interaction(uid: "ix_1", manager: "work")
        await model.always(rule, card: card, group: "")
        #expect(model.line(rule, card: card, group: "") == nil && model.made.isEmpty && seen.failures.isEmpty)
        #expect(seen.changed == [card] && seen.answered == ["rs_merge"])
        #expect(hub.sent("POST", Self.always).first?.headers[APIClient.managerHeader] == "work", "the manager that reported the card")
    }

    @Test func aFailedAnswerSaysWhyAndTheLineStays() async throws {
        let hub = FakeHub(), seen = Seen()
        var answers = [409, 403, 503]
        hub.on("POST", Self.always) { _ in
            if answers.isEmpty { return Data(#"{"ok":true,"kind":"standing_authorization","creates":"standing_authorization"}"#.utf8) }
            throw APIError(status: answers.removeFirst(), message: "This device needs full access")
        }
        let model = model(hub, seen), rule = try suggestion(Self.merge), card = RuleSuggestions.Card.todo("todo_1", round: "2026-11-09T11:00:00Z")
        await model.always(rule, card: card, group: "")
        #expect(seen.failures == [String(localized: "The standing authorizations changed meanwhile. Try again.")])
        await model.always(rule, card: card, group: "")
        await model.always(rule, card: card, group: "")
        #expect(seen.failures.count == 3 && seen.failures[1].contains("This device needs full access"))
        #expect(model.line(rule, card: card, group: "") == .offered(rule) && seen.changed.isEmpty && model.sending.isEmpty)
        await model.always(rule, card: card, group: "")
        #expect(model.made.count == 1 && hub.sent("POST", Self.always).count == 4)
    }

    @Test func nothingIsSentWithoutAHubOrForALineThatIsNotOffered() async throws {
        let hub = FakeHub(), seen = Seen()
        let offline = RuleSuggestions(api: { _ in nil }, failed: { seen.failures.append($0) }, changed: { _, _ in }, named: { _ in nil }, feedManager: { _, _ in nil })
        let rule = try suggestion(Self.merge), card = RuleSuggestions.Card.todo("todo_1", round: "2026-11-09T11:00:00Z")
        await offline.always(rule, card: card, group: "")
        #expect(seen.failures == [String(localized: "Not connected to a hub [Todo Store 04f822]")] && offline.line(rule, card: card, group: "") == .offered(rule))
        var unknown = rule
        unknown.creates = "something_newer"
        let model = model(hub, seen)
        await model.always(unknown, card: card, group: "")
        await model.notNow(unknown, card: card, group: "")
        #expect(hub.requests.isEmpty && model.line(unknown, card: card, group: "") == nil)
    }

    @Test func groupsAreToldApart() async throws {
        let hub = FakeHub(), seen = Seen()
        hub.on("POST", Self.notNow, json: #"{"ok":true}"#)
        let model = model(hub, seen), rule = try suggestion(Self.merge), card = RuleSuggestions.Card.todo("todo_1", round: "2026-11-09T11:00:00Z")
        await model.notNow(rule, card: card, group: "work")
        #expect(model.line(rule, card: card, group: "work") == nil && model.line(rule, card: card, group: "") == .offered(rule))
    }
}
