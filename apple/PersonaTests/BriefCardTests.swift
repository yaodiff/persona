import Foundation
import SwiftUI
import Testing
@testable import Persona

/// A routine's facts card, the `brief` feed item (docs/protocol/protocol.md "Tasks and the chat feed", #2320): read from the shapes
/// every hub since the item has written (and a newer one), with the buttons' requests and what each answer turns them into.
@MainActor
struct BriefCardTests {
    private func items(_ json: String) throws -> [FeedItem] {
        try JSON.decoder.decode(APIClient.FeedResponse.self, from: Data(json.utf8)).items
    }

    private func brief(_ body: String) throws -> BriefBody? {
        try items(#"{"items":[{"id":"feed_b","conversation":"c1","at":"2026-11-09T01:00:00Z","kind":"brief","ref":"morning","body":"# + body + "}]}").first?.brief
    }

    /// The first `brief` body (2026-11-07): no questions that can wait, no verdicts, no chats on the rows.
    @Test func theFirstBodyReads() throws {
        let b = try #require(try brief(#"""
        {"title":"Morning","at":"2026-11-07T01:00:00.123456789Z","hours":12,
         "waiting":[{"id":"ix_1","title":"Mock with a protocol or a singleton?","from":"Agent Network tests","link":"https://hub.example.com/s/box/ag1","waited_s":4800,"nudged":true}],
         "failed":[{"kind":"task","id":"todo_f","title":"Align the buttons","at":"2026-11-06T22:00:00Z","why":"rpc: connection closed"}],"failed_total":3,
         "done":[{"id":"todo_d","title":"Move the blog","at":"2026-11-06T23:00:00Z"}],"done_total":12,
         "accept":[{"id":"todo_v","title":"Release notes","detail":"Written in docs/release-notes.md","waited_s":25200,"asked_at":"2026-11-06T18:00:00.5+08:00"}],
         "progress":[{"id":"todo_p","title":"Widget crash","stage":"develop"}],"progress_total":2}
        """#))
        #expect(b.title == "Morning" && b.hours == 12 && !b.waitingUnknown)
        #expect(b.waiting.map(\.id) == ["ix_1"] && b.waiting[0].waitedS == 4800 && b.waiting[0].nudged)
        #expect(b.waiting[0].machineID == nil && b.waiting[0].sessionID == nil)
        #expect(b.failed.map(\.id) == ["todo_f"] && b.failedTotal == 3 && b.done.map(\.id) == ["todo_d"] && b.doneTotal == 12)
        #expect(b.pastMore == 13)
        #expect(b.accept.count == 1 && b.accept[0].askedAt == "2026-11-06T18:00:00.5+08:00" && b.accept[0].take == .accept)
        #expect(b.later.isEmpty && b.laterTotal == 0 && b.laterMore == 0)
        #expect(b.progress.map(\.stage) == ["develop"] && b.progressMore == 1)
    }

    /// Today's body: the questions that can wait, the conclusions' verdicts and the chats the rows came from.
    @Test func theCurrentBodyReads() throws {
        let b = try #require(try brief(#"""
        {"title":"Evening","at":"2026-11-09T12:00:00Z","hours":12,
         "waiting":[{"id":"ix_1","title":"Needs your approval","from":"Persona","waited_s":300,"machine_id":"server","session_id":"asm"}],
         "failed":[],"failed_total":0,"done":[],"done_total":0,
         "accept":[{"id":"todo_a","title":"Fix the login crash","waited_s":60,"asked_at":"2026-11-09T11:00:00Z","verdict":"Done, deployed"},
                   {"id":"todo_b","title":"Pick a cache","waited_s":60,"asked_at":"2026-11-09T11:01:00Z","verdict":"Needs your decision","outcome":"decide","recommend":"Redis"},
                   {"id":"todo_c","title":"Port the parser","waited_s":60,"asked_at":"2026-11-09T11:02:00Z","verdict":"Can't be done","outcome":"cannot"},
                   {"id":"todo_d","title":"Name the module","waited_s":60,"asked_at":"2026-11-09T11:03:00Z","outcome":"decide"}],
         "later":[{"id":"lt_1","title":"snake_case or camelCase?","from":"Agent API","link":"","waited_s":93600,"machine_id":"box","session_id":"ag1"}],"later_total":4,
         "progress":[],"progress_total":0}
        """#))
        #expect(b.waiting[0].machineID == "server" && b.waiting[0].sessionID == "asm")
        #expect(b.accept.map(\.take) == [.accept, .recommendation, .gotIt, nil])
        #expect(b.accept[1].recommend == "Redis" && b.accept[0].verdict == "Done, deployed")
        #expect(b.later.map(\.id) == ["lt_1"] && b.laterTotal == 4 && b.laterMore == 3 && b.later[0].sessionID == "ag1")
        #expect(b.pastMore == 0 && b.progressTotal == 0)
    }

    /// A newer hub's body: fields this build doesn't know are ignored, a value it doesn't know falls back, and a row it can't
    /// read is left out without the rest of the card.
    @Test func aNewerBodyStillReads() throws {
        let b = try #require(try brief(#"""
        {"title":"Morning","hours":"twelve","shape":"v9","waiting_unknown":true,"waiting":[],
         "failed":[{"kind":"workflow","id":"wf_1","title":"Nightly","at":"2026-11-09T00:00:00Z"},{"kind":"task","title":"no id"}],
         "done":[{"id":"todo_d","title":"Move the blog","at":"not a time","by":"review"},{"title":"no id"}],
         "accept":[{"id":"todo_x","title":"Ship it","waited_s":"long","asked_at":"2026-11-09T00:00:00Z","outcome":"escalate","sla":3},{"title":"no id"}],
         "later":"not a list",
         "progress":[{"id":"todo_p","title":"Widget crash","stage":"qa"},{"id":"todo_q","title":"Cache"}],"progress_total":1,
         "new_section":[{"id":"x"}]}
        """#))
        #expect(b.hours == 12 && b.waitingUnknown && b.waiting.isEmpty)
        #expect(b.failed.map(\.id) == ["wf_1"] && b.failedTotal == 1)
        #expect(b.done.map(\.id) == ["todo_d"] && b.done[0].at == nil && b.doneTotal == 1)
        #expect(b.accept.map(\.id) == ["todo_x"] && b.accept[0].waitedS == 0 && b.accept[0].take == .gotIt)
        #expect(b.later.isEmpty && b.laterTotal == 0)
        // A stage this build doesn't know stays the hub's word; a total below the rows listed counts the rows.
        #expect(b.progress.map(\.stage) == ["qa", nil] && TodoStage(hub: "qa") == nil && b.progressTotal == 2 && b.progressMore == 0)
    }

    /// A body that can't be read drops that card only: the feed's other items, and kinds this build doesn't know, are as before.
    @Test func aBodyThatCannotBeReadLeavesTheFeedAlone() throws {
        let list = try items(#"""
        {"items":[
          {"id":"feed_t","conversation":"c1","at":"2026-11-09T00:00:00Z","kind":"today","ref":"2026-11-09","body":{"date":"2026-11-09","working":2}},
          {"id":"feed_b1","conversation":"c1","at":"2026-11-09T00:01:00Z","kind":"brief","ref":"morning","body":"not an object"},
          {"id":"feed_b2","conversation":"c1","at":"2026-11-09T00:02:00Z","kind":"brief","ref":"morning","body":{"waiting":5,"accept":{"id":"x"}}},
          {"id":"feed_b3","conversation":"c1","at":"2026-11-09T00:03:00Z","kind":"brief","ref":"morning"},
          {"id":"feed_n","conversation":"c1","at":"2026-11-09T00:04:00Z","kind":"notice","body":{"text":"Routines run again"}},
          {"id":"feed_x","conversation":"c1","at":"2026-11-09T00:05:00Z","kind":"something_newer","body":{"text":"not for this build"}}]}
        """#)
        #expect(list.map(\.kind) == [.today, .brief, .brief, .brief, .notice, .unknown])
        #expect(list[0].today?.working == 2 && list[4].noticeText == "Routines run again")
        #expect(list[1].brief == nil && list[3].brief == nil)
        // Sections of the wrong type read as empty: the card says Nothing rather than vanish.
        let lenient = try #require(list[2].brief)
        #expect(lenient.waiting.isEmpty && lenient.accept.isEmpty && lenient.title.isEmpty)
        // Only a brief is one.
        #expect(list[0].brief == nil && list[5].brief == nil)
    }

    // MARK: Recommended setup (#5804)

    /// The first briefing of a week carries the computed lines; each is drawn as it came, once, and a line without its
    /// id or its words is left out without the rest of the card.
    @Test func theSetupLinesRead() throws {
        let b = try #require(try brief(#"""
        {"title":"Morning","hours":12,"waiting":[],"accept":[],
         "setup":[{"id":"triage_wake","setting":"agents.triage.wake","count":4,"text":"Waking on schedule would have saved 4 nudges.","link":"https://hub.example.com/settings?tab=persona"},
                  {"id":"leaders","setting":"assistant.leaders","count":31,"minutes":44,"text":"A leader conversation would have taken 31 rounds."},
                  {"id":"supervisor_tier","setting":"assistant.supervise","count":"many","text":"The background supervisor's reviews held."},
                  {"id":"something_newer","count":2,"text":"A newer mechanism."},
                  {"id":"triage_wake","count":9,"text":"Listed twice."},
                  {"id":"task_trust","count":3,"text":"   "},
                  {"id":"delegate","count":3},
                  {"count":3,"text":"No id."},
                  "not a line"]}
        """#))
        #expect(b.setup.map(\.id) == ["triage_wake", "leaders", "supervisor_tier", "something_newer"])
        #expect(b.setup[0].count == 4 && b.setup[0].text == "Waking on schedule would have saved 4 nudges." && b.setup[2].count == 0)
        // Open Settings only where the app has the mechanism's page.
        #expect(b.setup.map(\.page) == [.triage, nil, .superviseModel, nil])
        #expect(b.waiting.isEmpty && b.accept.isEmpty && b.progressTotal == 0, "the rest of the card is as without it")
    }

    @Test func aHubFromBeforeHasNoSetup() throws {
        let old = try #require(try brief(#"{"title":"Morning","hours":12,"waiting":[],"accept":[]}"#))
        let odd = try #require(try brief(#"{"title":"Morning","hours":12,"setup":"soon"}"#))
        #expect(old.setup.isEmpty && odd.setup.isEmpty)
    }

    @Test func eachMechanismOpensItsOwnPage() {
        func page(_ id: String) -> BriefBody.Setup.Page? { BriefBody.Setup(id: id, text: "x").page }
        #expect(["triage_decide", "triage_auto_continue", "triage_wake"].map(page) == [.triage, .triage, .triage])
        #expect(page("supervisor_tier") == .superviseModel)
        #expect(page("standing_authorizations") == .standingAuthorizations)
        // Not turned on at a page of this app: a remembered kind of task (a plan card's own checkbox; Auto-Start Tasks
        // only lists and removes them), leader conversations (Pin in a chat's menu), the spot-check share (asked of
        // Persona) and the two the assistant's configuration file holds.
        let none = ["task_trust", "leaders", "review_sample", "delegate", "independent_review", ""]
        #expect(none.allSatisfy { page($0) == nil })
        // Settings opens on the mechanism's page.
        var triage = NavigationPath()
        triage.append(SettingsPage.triage)
        var model = NavigationPath()
        model.append(SettingsPage.modelConfiguration)
        model.append(ModelPurposeRoute(id: "assistant.supervise"))
        // Standing authorizations are in the assistant's profile: Back goes there.
        var standing = NavigationPath()
        standing.append(SettingsPage.assistant)
        standing.append(SettingsPage.standingAuthorizations)
        #expect(BriefSetupPages.path(.triage) == triage && BriefSetupPages.path(.superviseModel) == model)
        #expect(BriefSetupPages.path(.standingAuthorizations) == standing)
    }

    /// A briefing with the block is still one line of its conversation, where its time puts it.
    @Test func aBriefingWithSetupIsOneFeedLine() throws {
        let list = try items(#"""
        {"items":[{"id":"feed_b","conversation":"c1","at":"2026-11-09T01:00:00Z","kind":"brief","ref":"morning",
                   "body":{"title":"Morning","hours":12,"setup":[{"id":"task_trust","count":3,"text":"Start directly would have saved 3 taps."}]}}]}
        """#)
        let lines = ChatLines.mergeFeed([], list)
        #expect(lines.count == 1)
        guard case .feed(let drawn)? = lines.first?.content else {
            Issue.record("the briefing is not a feed line")
            return
        }
        #expect(drawn.brief?.setup.map(\.id) == ["task_trust"] && drawn.brief?.setup.first?.page == nil)
    }

    @Test func aRowOpensTheChatItCameFrom() {
        let card = Interaction(id: "ix_1", machineID: "box", sessionID: "ag1", kind: .question)
        var other = Interaction(id: "ix_2", machineID: "box", sessionID: "ag2", kind: .question)
        other.group = "work"
        let pending = [card, other]
        // The hub's chat wins.
        let named = BriefBody.Row(id: "ix_1", title: "?", machineID: "server", sessionID: "asm")
        #expect(BriefBody.chat(for: named, waiting: true, group: "", pending: pending) == SessionKey(machineID: "server", sessionID: "asm"))
        // A hub from before: the card the app still has, in the same group.
        let bare = BriefBody.Row(id: "ix_1", title: "?")
        #expect(BriefBody.chat(for: bare, waiting: true, group: "", pending: pending) == SessionKey(machineID: "box", sessionID: "ag1"))
        #expect(BriefBody.chat(for: BriefBody.Row(id: "ix_2", title: "?"), waiting: true, group: "", pending: pending) == nil)
        #expect(BriefBody.chat(for: BriefBody.Row(id: "ix_gone", title: "?"), waiting: true, group: "", pending: pending) == nil)
        // A question that can wait is not a pending card: without its chat it has no link.
        #expect(BriefBody.chat(for: BriefBody.Row(id: "ix_1", title: "?"), waiting: false, group: "", pending: pending) == nil)
        #expect(BriefBody.chat(for: BriefBody.Row(id: "lt_1", title: "?", machineID: "box", sessionID: "ag1"), waiting: false, group: "", pending: [])
                == SessionKey(machineID: "box", sessionID: "ag1"))
    }

    // MARK: The buttons

    private let asked = "2026-11-09T11:00:00.123456789+08:00"

    private func actions(_ hub: FakeHub, failures: Failures? = nil, taken: Counter? = nil) -> BriefActions {
        BriefActions(api: { _ in hub.client }, failed: { failures?.messages.append($0) }, taken: { taken?.n += 1 })
    }

    final class Failures { var messages: [String] = [] }
    final class Counter { var n = 0 }

    @Test func acceptSendsTheRoundAndSaysItWasTaken() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/todos/todo_a/accept", json: #"{"ok":true}"#)
        let taken = Counter()
        let model = actions(hub, taken: taken)
        let row = BriefBody.Row(id: "todo_a", title: "Fix", askedAt: asked)
        await model.accept(row, group: "")
        #expect(model.outcome(accepting: row) == .taken(.accept) && taken.n == 1)
        let body = try #require(hub.sent("POST", "/api/v1/todos/todo_a/accept").first?.body)
        #expect(try JSONSerialization.jsonObject(with: body) as? [String: String] == ["asked_at": asked])
        // Taken: another tap sends nothing.
        await model.accept(row, group: "")
        #expect(hub.sent("POST", "/api/v1/todos/todo_a/accept").count == 1)
    }

    @Test func eachTakeSaysWhatHappened() async {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/todos/todo_b/accept", json: #"{"ok":true}"#)
        hub.on("POST", "/api/v1/todos/todo_c/accept", json: #"{"ok":true}"#)
        let model = actions(hub)
        let decide = BriefBody.Row(id: "todo_b", title: "Cache", askedAt: asked, outcome: "decide", recommend: "Redis")
        let done = BriefBody.Row(id: "todo_c", title: "Parser", askedAt: asked, outcome: "done")
        await model.accept(decide, group: "")
        await model.accept(done, group: "")
        #expect(model.outcome(accepting: decide) == .taken(.recommendation) && model.outcome(accepting: done) == .taken(.gotIt))
    }

    @Test func aRoundThatIsOverOrAnOldHubEndsTheButton() async {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/todos/todo_a/accept") { _ in throw APIError(status: 409, message: "Already handled") }
        hub.on("POST", "/api/v1/later/lt_1/handled") { _ in throw APIError(status: 409, message: "Already handled") }
        // No route for todo_old: FakeHub answers 404, as a hub rolled back past the item's routes does.
        let failures = Failures(), taken = Counter()
        let model = actions(hub, failures: failures, taken: taken)
        let over = BriefBody.Row(id: "todo_a", title: "Fix", askedAt: asked)
        let old = BriefBody.Row(id: "todo_old", title: "Old", askedAt: asked)
        let question = BriefBody.Row(id: "lt_1", title: "?")
        let oldQuestion = BriefBody.Row(id: "lt_old", title: "?")
        await model.accept(over, group: "")
        await model.accept(old, group: "")
        await model.markHandled(question, group: "")
        await model.markHandled(oldQuestion, group: "")
        #expect(model.outcome(accepting: over) == .alreadyHandled && model.outcome(accepting: old) == .hubTooOld)
        #expect(model.outcome(marking: question) == .alreadyHandled && model.outcome(marking: oldQuestion) == .hubTooOld)
        #expect(failures.messages.isEmpty && taken.n == 0)
    }

    @Test func aFailedRequestSaysWhyAndTheButtonWorksAgain() async {
        let hub = FakeHub()
        var answers: [Int] = [502, 503]
        hub.on("POST", "/api/v1/todos/todo_b/accept") { _ in
            if answers.isEmpty { return Data(#"{"ok":true}"#.utf8) }
            throw APIError(status: answers.removeFirst(), message: "The recommendation was not taken: the computer is offline")
        }
        hub.on("POST", "/api/v1/later/lt_1/handled") { _ in throw APIError(status: 503, message: "Service unavailable") }
        let failures = Failures()
        let model = actions(hub, failures: failures)
        let row = BriefBody.Row(id: "todo_b", title: "Cache", askedAt: asked, outcome: "decide", recommend: "Redis")
        await model.accept(row, group: "")
        #expect(model.outcome(accepting: row) == nil)
        #expect(failures.messages.count == 1 && failures.messages[0].contains("the computer is offline"))
        await model.accept(row, group: "")
        #expect(model.outcome(accepting: row) == nil && failures.messages.count == 2)
        await model.accept(row, group: "")
        #expect(model.outcome(accepting: row) == .taken(.recommendation))
        #expect(hub.sent("POST", "/api/v1/todos/todo_b/accept").count == 3)
        let question = BriefBody.Row(id: "lt_1", title: "?")
        await model.markHandled(question, group: "")
        #expect(model.outcome(marking: question) == nil && failures.messages.count == 3)
    }

    @Test func aSecondTapDuringTheRequestDoesNothing() async {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/todos/todo_a/accept", json: #"{"ok":true}"#)
        hub.on("POST", "/api/v1/later/lt_1/handled", json: #"{"ok":true}"#)
        let model = actions(hub)
        let row = BriefBody.Row(id: "todo_a", title: "Fix", askedAt: asked)
        let question = BriefBody.Row(id: "lt_1", title: "?")
        async let first: Void = model.accept(row, group: "")
        async let second: Void = model.accept(row, group: "")
        async let third: Void = model.markHandled(question, group: "")
        async let fourth: Void = model.markHandled(question, group: "")
        _ = await (first, second, third, fourth)
        #expect(hub.sent("POST", "/api/v1/todos/todo_a/accept").count == 1 && hub.sent("POST", "/api/v1/later/lt_1/handled").count == 1)
        #expect(model.outcome(accepting: row) == .taken(.accept) && model.outcome(marking: question) == .marked)
    }

    @Test func aRowWithNothingToTakeSendsNothing() async {
        let hub = FakeHub()
        let model = actions(hub)
        // A decision with nothing recommended, and a row whose round the hub didn't say.
        await model.accept(BriefBody.Row(id: "todo_d", title: "Name", askedAt: asked, outcome: "decide"), group: "")
        await model.accept(BriefBody.Row(id: "todo_e", title: "Ship"), group: "")
        #expect(hub.requests.isEmpty)
    }
}
