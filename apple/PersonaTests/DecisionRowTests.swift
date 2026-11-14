import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A decision answered on its row (#3804, docs/assistant/todos.md "Decisions the owner can answer"): what the hub sends
/// on a to-do's `ask`, what the row shows of it, and how each answer goes through the accept route (`DecisionAnswers`,
/// with `FakeHub`). "Nobody is working on this" (#3618) is the to-do's `orphaned`.
@MainActor struct DecisionRowTests {
    private let asked = "2026-11-10T09:00:00.123456789Z"
    private let accept = "/api/v1/todos/todo_release/accept"

    private func todo(_ ask: String, extra: String = "") throws -> TodoItem {
        try JSON.decoder.decode(TodoItem.self, from: Data((#"{"id":"todo_release","title":"发布 2.4","ask":"# + ask + extra + "}").utf8))
    }

    private func decision(why: String = "priority", fallback: String = #""default":"taken","deadline":"2026-11-10T17:30:00Z""#,
                          responses: String = #"["decide_yourself","not_what_i_asked","explain_first"]"#) -> String {
        #"""
        {"outcome":"decide","verdict":"🤔 要你拍板：2.4 发不发 beta","recommend":"发 beta","asked_at":"\#(asked)",
         "question":"2.4 今天发 beta，还是等崩溃修复？","why_owner":"\#(why)","background":"来自主对话周一的消息；构建已通过，还有一个崩溃没修",
         "options":[{"label":"发 beta","leads_to":"测试用户今晚拿到，修复随 2.4.1 跟上"},{"label":"等修复","leads_to":"beta 推到周四"},{"label":"取消发布"}],
         "cost_of_waiting":"beta 窗口 18:00 关闭",\#(fallback),"responses":\#(responses),
         "from":{"main":true,"at":"2026-11-09T02:00:00Z"},"stands":"构建已通过"}
        """#
    }

    private func answers(_ hub: FakeHub, failures: BriefCardTests.Failures? = nil, taken: BriefCardTests.Counter? = nil) -> DecisionAnswers {
        DecisionAnswers(api: { _ in hub.client }, failed: { failures?.messages.append($0) }, taken: { taken?.n += 1 })
    }

    private func body(_ hub: FakeHub, at index: Int = 0) throws -> [String: String] {
        let sent = hub.sent("POST", accept)
        let data = try #require(sent.indices.contains(index) ? sent[index].body : nil)
        return try #require(try JSONSerialization.jsonObject(with: data) as? [String: String])
    }

    // MARK: What the hub sends

    @Test func decodesTheDecisionsOwnFields() throws {
        let ask = try #require(try todo(decision()).openAsk)
        #expect(ask.askedAt == asked && ask.outcome == "decide" && ask.recommend == "发 beta")
        #expect(ask.question == "2.4 今天发 beta，还是等崩溃修复？" && ask.whyOwner == "priority")
        #expect(ask.background == "来自主对话周一的消息；构建已通过，还有一个崩溃没修" && ask.costOfWaiting == "beta 窗口 18:00 关闭")
        #expect(ask.decisionOptions == [.init(label: "发 beta", leadsTo: "测试用户今晚拿到，修复随 2.4.1 跟上"),
                                        .init(label: "等修复", leadsTo: "beta 推到周四"), .init(label: "取消发布")])
        #expect(ask.defaultAction == "taken" && ask.deadline == "2026-11-10T17:30:00Z")
        #expect(ask.fixedResponses == [.decideYourself, .notWhatIAsked, .explainFirst] && ask.response == nil)
        #expect(ask.questionLine == "2.4 今天发 beta，还是等崩溃修复？" && !ask.needsConfirmation)
        let back = try JSON.decoder.decode(TodoAsk.self, from: JSON.encoder.encode(ask))
        #expect(back == ask)
    }

    /// An older hub sends the verdict line and the recommendation only: the row keeps its one button.
    @Test func anOlderHubsAskHasNoOptions() throws {
        let ask = try #require(try todo(#"{"outcome":"decide","verdict":"🤔 要你拍板：旧接口留不留","recommend":"先合入","asked_at":"\#(asked)"}"#).openAsk)
        #expect(ask.decisionOptions.isEmpty && ask.fixedResponses.isEmpty && ask.question == nil && ask.defaultLine() == nil)
        #expect(ask.questionLine == "🤔 要你拍板：旧接口留不留")
        let bare = try todo(#"{"asked_at":"\#(asked)"}"#)
        #expect(bare.openAsk?.askedAt == asked && bare.openAsk?.decisionOptions.isEmpty == true)
    }

    /// New fields in a shape this build does not know are left out one by one; the ask and its round stay.
    @Test func oddDecisionFieldsNeverHideTheAsk() throws {
        let ask = try #require(try todo(#"{"outcome":"decide","verdict":"v","asked_at":"\#(asked)","question":7,"why_owner":["x"],"options":"发 beta","cost_of_waiting":{},"background":1,"deadline":5,"default":true,"responses":"all","response":3}"#).openAsk)
        #expect(ask.askedAt == asked && ask.question == nil && ask.whyOwner == nil && ask.options == nil && ask.costOfWaiting == nil)
        #expect(ask.background == nil && ask.deadline == nil && ask.defaultAction == nil && ask.responses == nil && ask.response == nil)
        #expect(ask.decisionOptions.isEmpty && ask.questionLine == "v")
    }

    /// A decision held for the briefing (#5803) with a suggested rule (#5802) keeps both beside its options: one decoder
    /// reads them all, the encoded ask reads back the same, and a suggestion in an odd shape leaves the rest.
    @Test func aDecisionKeepsItsBriefingMarkAndSuggestedRule() throws {
        let rule = #"{"id":"rs_merge","kind":"standing_authorization","text":"t","scope":{"directory":"/code/example","action":"merge"},"creates":"standing_authorization","count":3}"#
        let held = try todo(String(decision().dropLast()) + #","for_briefing":true,"suggestion":\#(rule)}"#)
        let ask = try #require(held.openAsk)
        #expect(ask.decisionOptions.count == 3 && ask.isForBriefing && ask.suggestion?.id == "rs_merge")
        #expect(ask.row(for: held).forBriefing && ask.row(for: held).suggestion == ask.suggestion)
        #expect(try JSON.decoder.decode(TodoAsk.self, from: JSON.encoder.encode(ask)) == ask)
        let odd = try #require(try todo(String(decision().dropLast()) + #","for_briefing":"yes","suggestion":7}"#).openAsk)
        #expect(odd.decisionOptions.count == 3 && !odd.isForBriefing && (odd.suggestion?.id ?? "").isEmpty)
    }

    /// Options on its row: a decision with at least two that have a label. A conclusion that only tells has none, and
    /// neither has a decision with one option left.
    @Test func onlyADecisionWithTwoLabelledOptionsIsAnsweredOnItsRow() {
        let two: [TodoAsk.Option] = [.init(label: "A"), .init(label: "B", leadsTo: "b")]
        #expect(TodoAsk(outcome: "decide", askedAt: asked, options: two).decisionOptions == two)
        #expect(TodoAsk(outcome: "done", askedAt: asked, options: two).decisionOptions.isEmpty)
        #expect(TodoAsk(outcome: "decide", askedAt: asked, options: [.init(label: "A"), .init(label: "  ")]).decisionOptions.isEmpty)
        #expect(TodoAsk(outcome: "decide", askedAt: asked).decisionOptions.isEmpty)
        // Fixed answers this build does not know are left out, the rest in the menu's order.
        #expect(TodoAsk(askedAt: asked, responses: ["explain_first", "escalate", "decide_yourself"]).fixedResponses == [.decideYourself, .explainFirst])
    }

    @Test func theDefaultLineSaysWhenOrNever() throws {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = try #require(TimeZone(identifier: "Asia/Shanghai"))
        let now = try #require(JSON.parseDate("2026-11-10T09:00:00Z")) // 17:00 in Shanghai
        let today = TodoAsk(askedAt: asked, deadline: "2026-11-10T09:30:00Z", defaultAction: "taken")
        #expect(today.defaultLine(now: now, calendar: calendar) == "17:30 没回复就按推荐办")
        let morning = try #require(JSON.parseDate("2026-11-10T01:05:00Z"))
        #expect(TodoAsk.clock(morning, now: now, calendar: calendar) == "09:05", "today: the time alone, on a 24-hour clock")
        let tomorrow = TodoAsk(askedAt: asked, deadline: "2026-11-11T01:00:00Z", defaultAction: "taken")
        let line = try #require(tomorrow.defaultLine(now: now, calendar: calendar))
        #expect(line.hasSuffix("09:00 没回复就按推荐办") && line.contains("11") && line != "09:00 没回复就按推荐办", "another day says the date too")
        #expect(TodoAsk(askedAt: asked, defaultAction: "never").defaultLine() == "不会默认执行，等你回复")
        #expect(TodoAsk(askedAt: asked, deadline: "2026-11-10T09:30:00Z").defaultLine() == nil, "a deadline alone says nothing")
        #expect(TodoAsk(askedAt: asked, deadline: "soon", defaultAction: "taken").defaultLine() == nil)
        #expect(TodoAsk(askedAt: asked, whyOwner: "irreversible").needsConfirmation && TodoAsk(askedAt: asked, whyOwner: "visible_to_others").needsConfirmation)
        #expect(!TodoAsk(askedAt: asked, whyOwner: "acceptance").needsConfirmation)
    }

    // MARK: Answering

    @Test func anOptionGoesThroughTheAcceptRouteWithItsRound() async throws {
        let hub = FakeHub(), taken = BriefCardTests.Counter()
        hub.on("POST", accept, json: #"{"ok":true}"#)
        let item = try todo(decision()), ask = try #require(item.openAsk), model = answers(hub, taken: taken)
        await model.choose(1, of: ask, todo: item.id, group: "")
        #expect(try body(hub) == ["asked_at": asked, "option": "等修复"])
        #expect(model.state(todo: item.id, ask: ask, group: "") == .answered("已回答：等修复") && taken.n == 1)
        // Answered: another tap, on any option or fixed answer, sends nothing.
        await model.choose(0, of: ask, todo: item.id, group: "")
        await model.respond(.decideYourself, to: ask, todo: item.id, group: "")
        #expect(hub.requests.count == 1)
    }

    @Test func theRecommendedOptionIsThePlainOK() async throws {
        let hub = FakeHub()
        hub.on("POST", accept, json: #"{"ok":true}"#)
        let item = try todo(decision()), ask = try #require(item.openAsk), model = answers(hub)
        await model.choose(0, of: ask, todo: item.id, group: "")
        #expect(try body(hub) == ["asked_at": asked, "option": "发 beta"])
        await model.choose(7, of: ask, todo: "other", group: "") // no such option: nothing sent
        #expect(hub.requests.count == 1)
    }

    /// A step that cannot be undone, or that other people will see, takes two taps: the first arms the option.
    @Test(arguments: ["irreversible", "visible_to_others"])
    func anIrreversibleOptionTakesTwoTaps(why: String) async throws {
        let hub = FakeHub()
        hub.on("POST", accept, json: #"{"ok":true}"#)
        let item = try todo(decision(why: why, fallback: #""default":"never""#)), ask = try #require(item.openAsk), model = answers(hub)
        #expect(ask.needsConfirmation && ask.defaultLine() == "不会默认执行，等你回复")
        await model.choose(0, of: ask, todo: item.id, group: "")
        #expect(hub.requests.isEmpty && model.armedOption(todo: item.id, ask: ask, group: "") == 0)
        #expect(model.state(todo: item.id, ask: ask, group: "") == nil)
        // Another option arms that one instead; nothing is sent until the same one is tapped again.
        await model.choose(2, of: ask, todo: item.id, group: "")
        #expect(hub.requests.isEmpty && model.armedOption(todo: item.id, ask: ask, group: "") == 2)
        await model.choose(2, of: ask, todo: item.id, group: "")
        #expect(try body(hub) == ["asked_at": asked, "option": "取消发布"])
        #expect(model.state(todo: item.id, ask: ask, group: "") == .answered("已回答：取消发布"))
        #expect(model.armedOption(todo: item.id, ask: ask, group: "") == nil)
    }

    /// The fixed answers are never armed, even on a decision whose options are. Nothing is chosen by one, so the row
    /// stays answerable: it says "Sent back to Persona" and keeps its options, Other… and ⋯ (the hub keeps the round
    /// open after "Tell Me the Background First"; after the others it ends it, and the to-dos read after the answer
    /// take the row away). Only an option ends the row here.
    @Test(arguments: TodoAsk.Response.allCases)
    func aFixedAnswerIsSentAtOnce(response: TodoAsk.Response) async throws {
        let hub = FakeHub(), taken = BriefCardTests.Counter()
        hub.on("POST", accept, json: #"{"ok":true}"#)
        let item = try todo(decision(why: "irreversible")), ask = try #require(item.openAsk), model = answers(hub, taken: taken)
        await model.respond(response, to: ask, todo: item.id, group: "")
        #expect(try body(hub) == ["asked_at": asked, "response": response.rawValue])
        #expect(model.state(todo: item.id, ask: ask, group: "") == .sentBack && taken.n == 1)
        let row = DecisionRow(todo: item, ask: ask, group: "", answers: model, actions: BriefActions(api: { _ in nil }, failed: { _ in }, taken: {}))
        #expect(row.sentBack && !row.busy, "the row says it went back and its controls stay enabled")
        // Still answerable: another fixed answer goes, and an option (two taps here) ends the row.
        await model.respond(.explainFirst, to: ask, todo: item.id, group: "")
        #expect(try body(hub, at: 1) == ["asked_at": asked, "response": "explain_first"])
        await model.choose(1, of: ask, todo: item.id, group: "")
        #expect(hub.sent("POST", accept).count == 2 && model.armedOption(todo: item.id, ask: ask, group: "") == 1)
        await model.choose(1, of: ask, todo: item.id, group: "")
        #expect(try body(hub, at: 2) == ["asked_at": asked, "option": "等修复"])
        #expect(model.state(todo: item.id, ask: ask, group: "") == .answered("已回答：等修复") && taken.n == 3)
        await model.respond(.decideYourself, to: ask, todo: item.id, group: "")
        await model.choose(0, of: ask, todo: item.id, group: "")
        #expect(hub.sent("POST", accept).count == 3, "answered: nothing more is sent")
    }

    /// The hub says a fixed answer was given (`response`, the round still open): the row says so on every surface and
    /// on another phone, and its options still answer it.
    @Test func aRoundTheHubKeptOpenAfterAResponseIsStillAnswerable() async throws {
        let hub = FakeHub()
        hub.on("POST", accept, json: #"{"ok":true}"#)
        let item = try todo(decision(), extra: ""), model = answers(hub)
        var ask = try #require(item.openAsk)
        ask.response = "explain_first"
        let row = DecisionRow(todo: item, ask: ask, group: "", answers: model, actions: BriefActions(api: { _ in nil }, failed: { _ in }, taken: {}))
        #expect(row.sentBack && !row.busy && model.state(todo: item.id, ask: ask, group: "") == nil)
        await model.choose(0, of: ask, todo: item.id, group: "")
        #expect(try body(hub) == ["asked_at": asked, "option": "发 beta"])
    }

    /// A failed fixed answer after one that went still says the first one went, and sends again on the next tap.
    @Test func aFailedResponseKeepsWhatWasSentBefore() async throws {
        let hub = FakeHub(), failures = BriefCardTests.Failures()
        var fail = false
        hub.on("POST", accept) { _ in
            if fail { throw APIError(status: 503, message: "助手没有连上") }
            return Data(#"{"ok":true}"#.utf8)
        }
        let item = try todo(decision()), ask = try #require(item.openAsk), model = answers(hub, failures: failures)
        await model.respond(.explainFirst, to: ask, todo: item.id, group: "")
        fail = true
        await model.respond(.decideYourself, to: ask, todo: item.id, group: "")
        #expect(model.state(todo: item.id, ask: ask, group: "") == .sentBack && failures.messages.count == 1)
        fail = false
        await model.choose(2, of: ask, todo: item.id, group: "")
        #expect(try body(hub, at: 2) == ["asked_at": asked, "option": "取消发布"])
    }

    /// Complete Directly in the ⋯ menu (#3038) holds the whole row while its request is out, as `BriefAcceptRow` does:
    /// no option, Other… or ⋯ can be tapped meanwhile. Done, the row goes.
    @Test func completeDirectlyHoldsTheWholeRow() async throws {
        let hub = FakeHub()
        let item = try todo(decision()), ask = try #require(item.openAsk), model = answers(hub)
        let gate = Gate()
        let actions = BriefActions(api: { _ in hub.client }, failed: { _ in }, taken: {}, complete: { _, _ in await gate.wait() })
        let row = DecisionRow(todo: item, ask: ask, group: "", answers: model, actions: actions)
        #expect(!row.busy)
        let completing = Task { await actions.completeDirectly(ask.row(for: item), group: "") }
        await gate.arrived()
        #expect(row.busy, "every control of the row is disabled while Complete Directly is out")
        #expect(actions.outcome(accepting: item.id, askedAt: ask.askedAt) == .busy)
        gate.open()
        await completing.value
        #expect(actions.outcome(accepting: item.id, askedAt: ask.askedAt) == .completed && !row.busy)
        // Another round of the same to-do is its own.
        #expect(actions.outcome(accepting: item.id, askedAt: "2026-11-11T09:00:00Z") == nil)
    }

    /// An armed option never moves into the ⋯ menu when its longer "Confirm: …" label leaves room for fewer buttons:
    /// it takes the last button's place.
    @Test func theArmedOptionKeepsItsButton() async throws {
        let item = try todo(decision(why: "irreversible")), ask = try #require(item.openAsk)
        let model = answers(FakeHub())
        let row = DecisionRow(todo: item, ask: ask, group: "", answers: model)
        #expect(row.buttonIndices(shown: 3) == [0, 1, 2] && row.buttonIndices(shown: 1) == [0])
        await model.choose(2, of: ask, todo: item.id, group: "")
        #expect(row.buttonIndices(shown: 3) == [0, 1, 2] && row.buttonIndices(shown: 2) == [0, 2] && row.buttonIndices(shown: 1) == [2])
        await model.choose(0, of: ask, todo: item.id, group: "")
        #expect(row.buttonIndices(shown: 1) == [0] && row.buttonIndices(shown: 2) == [0, 1])
        #expect(row.buttonIndices(shown: 0) == [0] && row.buttonIndices(shown: 9) == [0, 1, 2])
    }

    /// The default line and the cost of waiting, worked out once per round and day, not in the row's body.
    @Test func theWaitingLineIsKeptPerRound() throws {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = try #require(TimeZone(identifier: "Asia/Shanghai"))
        let now = try #require(JSON.parseDate("2026-11-10T09:00:00Z"))
        let ask = TodoAsk(askedAt: asked, costOfWaiting: "beta 窗口 18:00 关闭", deadline: "2026-11-10T09:30:00Z", defaultAction: "taken")
        #expect(ask.waitingLine(now: now, calendar: calendar) == "17:30 没回复就按推荐办 · 等下去的代价：beta 窗口 18:00 关闭")
        #expect(DecisionWaitingLines.line(for: ask, now: now, calendar: calendar) == ask.waitingLine(now: now, calendar: calendar))
        #expect(TodoAsk(askedAt: asked, costOfWaiting: "x").waitingLine() == "等下去的代价：x")
        #expect(TodoAsk(askedAt: asked, defaultAction: "never").waitingLine() == "不会默认执行，等你回复")
        #expect(TodoAsk(askedAt: asked, costOfWaiting: "").waitingLine() == nil && TodoAsk(askedAt: asked).waitingLine() == nil)
        // A new round, or the same deadline on the next day, is worked out again.
        var later = ask
        later.askedAt = "2026-11-11T01:00:00Z"
        later.costOfWaiting = nil
        #expect(DecisionWaitingLines.line(for: later, now: now, calendar: calendar) == "17:30 没回复就按推荐办")
        let tomorrow = now.addingTimeInterval(24 * 3600)
        let next = try #require(DecisionWaitingLines.line(for: ask, now: tomorrow, calendar: calendar))
        #expect(next != "17:30 没回复就按推荐办 · 等下去的代价：beta 窗口 18:00 关闭" && next.contains("17:30"))
    }

    /// 409: the round was over (asked again since, or answered on another device). The row says so and reads the to-dos again.
    @Test func aRoundThatIsOverSaysAlreadyHandled() async throws {
        let hub = FakeHub(), taken = BriefCardTests.Counter(), failures = BriefCardTests.Failures()
        hub.on("POST", accept) { _ in throw APIError(status: 409, message: "already handled") }
        let item = try todo(decision()), ask = try #require(item.openAsk), model = answers(hub, failures: failures, taken: taken)
        await model.choose(0, of: ask, todo: item.id, group: "")
        #expect(model.state(todo: item.id, ask: ask, group: "") == .alreadyHandled && taken.n == 1 && failures.messages.isEmpty)
    }

    /// 502 (the choice did not reach the task's agent), 503 or no network: nothing was taken, the reason is said, and
    /// the buttons work again; an armed option stays armed, so one more tap sends it.
    @Test func aFailedAnswerSaysWhyAndCanBeSentAgain() async throws {
        let hub = FakeHub(), taken = BriefCardTests.Counter(), failures = BriefCardTests.Failures()
        var fail = true
        hub.on("POST", accept) { _ in
            if fail { throw APIError(status: 502, message: "没送到任务的 Agent") }
            return Data(#"{"ok":true}"#.utf8)
        }
        let item = try todo(decision(why: "irreversible")), ask = try #require(item.openAsk), model = answers(hub, failures: failures, taken: taken)
        await model.choose(1, of: ask, todo: item.id, group: "")
        await model.choose(1, of: ask, todo: item.id, group: "")
        #expect(model.state(todo: item.id, ask: ask, group: "") == nil && taken.n == 0)
        #expect(failures.messages == ["回答没发出去。没送到任务的 Agent"])
        #expect(model.armedOption(todo: item.id, ask: ask, group: "") == 1)
        fail = false
        await model.choose(1, of: ask, todo: item.id, group: "")
        #expect(model.state(todo: item.id, ask: ask, group: "") == .answered("已回答：等修复") && taken.n == 1)
        #expect(hub.sent("POST", accept).count == 2)
    }

    @Test func withoutAHubNothingIsSentAndItSaysSo() async throws {
        let failures = BriefCardTests.Failures()
        let model = DecisionAnswers(api: { _ in nil }, failed: { failures.messages.append($0) }, taken: {})
        let item = try todo(decision()), ask = try #require(item.openAsk)
        await model.choose(0, of: ask, todo: item.id, group: "")
        #expect(model.state(todo: item.id, ask: ask, group: "") == nil && failures.messages == ["回答没发出去。未连接 hub"])
    }

    /// The same round on two surfaces is one answer; a new round of the same to-do starts clean.
    @Test func aNewRoundStartsClean() async throws {
        let hub = FakeHub()
        hub.on("POST", accept, json: #"{"ok":true}"#)
        let item = try todo(decision()), ask = try #require(item.openAsk), model = answers(hub)
        await model.choose(0, of: ask, todo: item.id, group: "")
        var again = ask
        again.askedAt = "2026-11-11T09:00:00Z"
        #expect(model.state(todo: item.id, ask: again, group: "") == nil)
        #expect(model.state(todo: item.id, ask: ask, group: "other-hub") == nil)
        await model.choose(1, of: again, todo: item.id, group: "")
        #expect(try body(hub, at: 1) == ["asked_at": "2026-11-11T09:00:00Z", "option": "等修复"])
    }

    /// The plain accept sends the body it always did: an older hub sees no key it does not know.
    @Test func thePlainAcceptSendsOnlyItsRound() async throws {
        let hub = FakeHub()
        hub.on("POST", accept, json: #"{"ok":true}"#)
        try await hub.client.acceptTodo("todo_release", askedAt: asked)
        #expect(try body(hub) == ["asked_at": asked])
    }

    // MARK: Nobody is working on this

    @Test(arguments: [("error", "没人在做 · 出错后关闭"), ("offline", "没人在做 · 电脑离线"), ("closed", "没人在做 · 已关闭"), ("paused", "没人在做")])
    func anOrphanedTodoSaysSoAndWhy(reason: String, line: String) throws {
        let item = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"t","title":"检查 PR","orphaned":{"reason":"\#(reason)","since":"2026-11-10T04:00:00Z"}}"#.utf8))
        #expect(item.orphaned?.reason == reason && item.orphaned?.since == JSON.parseDate("2026-11-10T04:00:00Z"))
        #expect(item.orphanedLine == line)
        var done = item
        done.doneAt = .now
        #expect(done.orphanedLine == nil, "a done to-do is nobody's to work on")
        #expect(try JSON.decoder.decode(TodoItem.self, from: JSON.encoder.encode(item)).orphaned == item.orphaned)
    }

    @Test(arguments: [#""orphaned":null"#, #""orphaned":"error""#, #""orphaned":[]"#, #""title":"x""#])
    func anOlderHubOrAnOddValueSaysNothing(field: String) throws {
        let item = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"t","title":"检查 PR",\#(field)}"#.utf8))
        #expect(item.id == "t" && item.orphaned == nil && item.orphanedLine == nil)
    }

    @Test func anOrphanedReasonInAnotherShapeStillSaysNobody() throws {
        let item = try JSON.decoder.decode(TodoItem.self, from: Data(#"{"id":"t","title":"检查 PR","orphaned":{"reason":7}}"#.utf8))
        #expect(item.orphaned == TodoOrphaned() && item.orphanedLine == "没人在做")
    }

    /// Holds a request open until the test lets it go.
    @MainActor final class Gate {
        private var waiting: CheckedContinuation<Void, Never>?
        private var arrival: CheckedContinuation<Void, Never>?
        private var isOpen = false

        func wait() async {
            if isOpen { return }
            await withCheckedContinuation { continuation in
                waiting = continuation
                arrival?.resume()
                arrival = nil
            }
        }

        func arrived() async {
            if waiting != nil { return }
            await withCheckedContinuation { arrival = $0 }
        }

        func open() {
            isOpen = true
            waiting?.resume()
            waiting = nil
        }
    }
}
