import Foundation
import MapKit
import Testing
@testable import Persona
import PersonaWire

/// Tasks from the assistant's conversation (docs/assistant/unified-entry.md): the hub's shapes decode, a task's state follows
/// its agents, the feed lands at the end of the exchange it happened in, and the @ of a mention is told from an address.
@MainActor
struct TasksTests {
    private func decode<T: Decodable>(_ json: String, as: T.Type) throws -> T { try JSON.decoder.decode(T.self, from: Data(json.utf8)) }

    @Test func feedAndPlanDecode() throws {
        let res = try decode(#"""
        {"items":[
          {"id":"feed_1","conversation":"c1","at":"2026-11-03T10:00:00.123456789Z","kind":"plan","ref":"plan_1"},
          {"id":"feed_2","conversation":"c1","at":"2026-11-03T10:05:00Z","kind":"receipt","ref":"todo_1",
           "body":{"title":"修登录崩溃","state":"done","summary":"改好了","pr":"https://github.com/o/r/pull/901","machine":"box"}},
          {"id":"feed_3","conversation":"c1","at":"2026-11-03T10:06:00Z","kind":"answered","ref":"todo_1",
           "body":{"question":"使用 Bash","answer":"批准了"}},
          {"id":"feed_4","conversation":"c1","at":"2026-11-03T10:07:00Z","kind":"something_new","ref":"x"}],
         "plans":{"plan_1":{"id":"plan_1","conversation":"c1","state":"open","created_at":"2026-11-03T10:00:00Z",
           "tasks":[{"title":"修登录崩溃","prompt":"…","machine":"box","cwd":"/code/app","readonly":false,"why":"最近在这"}]}}}
        """#, as: APIClient.FeedResponse.self)
        #expect(res.items.map(\.kind) == [.plan, .receipt, .answered, .unknown])
        let receipt = res.items[1]
        #expect(receipt.title == "修登录崩溃" && !receipt.failed && receipt.prLabel == "PR #901" && receipt.summary == "改好了")
        #expect(res.items[2].answer == "批准了")
        let plan = try #require(res.plans?["plan_1"])
        #expect(plan.state == .open && plan.tasks.first?.project == "app" && plan.tasks.first?.isReadOnly == false)
    }

    /// An answered record has every question with its answer in full (#1231); one from before it shows its line.
    @Test func answeredItemsInFullOrTheLine() throws {
        let res = try decode(#"""
        {"items":[
          {"id":"feed_1","conversation":"c1","at":"2026-11-03T10:06:00Z","kind":"answered","ref":"todo_1",
           "body":{"question":"先补哪一块？","answer":"重试；假服务器","items":[
             {"question":"先补哪一块？","answer":"重试\n和超时"},{"question":"请求怎么处理？","answer":"假服务器"},{"question":"","answer":""}]}},
          {"id":"feed_2","conversation":"c1","at":"2026-11-03T10:07:00Z","kind":"answered","ref":"todo_1",
           "body":{"question":"使用 Bash","answer":"批准了"}},
          {"id":"feed_3","conversation":"c1","at":"2026-11-03T10:08:00Z","kind":"answered","ref":"todo_1","body":{"items":[]}}]}
        """#, as: APIClient.FeedResponse.self)
        #expect(res.items[0].answer == "重试；假服务器")
        #expect(res.items[0].answeredItems == [AnsweredItem(question: "先补哪一块？", answer: "重试\n和超时"),
                                               AnsweredItem(question: "请求怎么处理？", answer: "假服务器")])
        #expect(res.items[1].answeredItems == [AnsweredItem(question: "使用 Bash", answer: "批准了")])
        #expect(res.items[2].answeredItems.isEmpty)
    }

    @Test func todoDecodesWithAndWithoutOrigin() throws {
        let t = try decode(#"""
        {"id":"todo_1","title":"修登录崩溃","notes":"","done_at":null,"created_at":"2026-11-03T10:00:00Z","origin":"c1","plan_id":"plan_1",
         "spaces":[],"agents":[{"machine_id":"box","session_id":"s1","linked_at":"2026-11-03T10:00:01Z","released_at":null,"title":"","cwd":"/code/app","agent":"claude","start_error":"","source":"manual","via":""},
                    {"machine_id":"box","session_id":"s0","released_at":"2026-11-03T09:00:00Z"}]}
        """#, as: TodoItem.self)
        #expect(t.origin == "c1" && t.planID == "plan_1" && t.activeAgents.map(\.sessionID) == ["s1"])
        let hand = try decode(#"{"id":"todo_2","title":"手动的","agents":[]}"#, as: TodoItem.self)
        #expect(hand.origin == nil && hand.agents.isEmpty)
    }

    private func lead(state: String, lastText: String?, activity: String?, at: String? = nil) throws -> Session {
        var fields = [#""machine_id":"box","session_id":"s1","title":"t","cwd":"/code/app","status":"working","state":"\#(state)""#]
        if let lastText { fields.append(#""last_text":"\#(lastText)""#) }
        if let activity { fields.append(#""activity":"\#(activity)""#) }
        if let at { fields.append(#""activity_at":"\#(at)""#) }
        return try decode("{" + fields.joined(separator: ",") + "}", as: Session.self)
    }

    /// An agent that only runs tools (reads code, runs tests, waits for CI) writes no new words for minutes: the card
    /// shows the step it is on and since when, not the line it wrote before them.
    @Test func aCardFollowsAnAgentThatOnlyRunsTools() throws {
        let busy = try lead(state: "working", lastText: "先看一下登录的代码", activity: "在跑测试", at: "2026-11-04T01:20:13.174Z")
        #expect(busy.activity == "在跑测试" && abs((busy.activityAt ?? .distantPast).timeIntervalSince1970 - 1793755213.174) < 0.01)
        #expect(TaskNow(lead: busy, working: true) == .step("在跑测试", since: busy.activityAt))
        // Only tool calls since it started: nothing said yet, still something to show.
        let silent = try lead(state: "working", lastText: nil, activity: "在读 tasks.go", at: "2026-11-04T01:20:13Z")
        #expect(TaskNow(lead: silent, working: true)?.text == "在读 tasks.go")
        // New words: the hub drops the step and the words show again.
        let spoke = try lead(state: "working", lastText: "测试过了，在提 PR", activity: nil)
        #expect(TaskNow(lead: spoke, working: true) == .words("测试过了，在提 PR"))
        // Stopped on a tool call (cut short): its last words, not a step it is no longer on.
        let stopped = try lead(state: "unread", lastText: "改好了", activity: "在跑测试", at: "2026-11-04T01:20:13Z")
        #expect(TaskNow(lead: stopped, working: false) == .words("改好了"))
        let stoppedSilent = try lead(state: "idle", lastText: nil, activity: "在跑测试", at: "2026-11-04T01:20:13Z")
        #expect(TaskNow(lead: stoppedSilent, working: false) == .step("在跑测试", since: nil))
        // An older hub sends no step: the words, as before.
        let old = try lead(state: "working", lastText: "在看", activity: nil)
        #expect(TaskNow(lead: old, working: true)?.text == "在看")
        #expect(TaskNow(lead: try lead(state: "working", lastText: nil, activity: nil), working: true) == nil)
        #expect(TaskNow(lead: nil, working: true) == nil)
    }

    /// Running subagents of a lead session are summarized below its own step, while TaskNow stays untouched.
    @Test func runningSubagentsSummarizeUnderTaskProgress() throws {
        var lead = try lead(state: "working", lastText: "排查中", activity: "在跑测试", at: "2026-11-04T01:20:13Z")
        #expect(runningSubagents(lead).isEmpty)
        #expect(subagentsSummaryLine(runningSubagents(lead)) == nil)
        #expect(TaskNow(lead: lead, working: true) == .step("在跑测试", since: lead.activityAt))

        lead.subagents = [
            Subagent(id: "s1", sessionID: "s1", type: "Explore", description: "扫目录", running: true),
            Subagent(id: "s2", sessionID: "s2", type: "Review", description: nil, running: true),
            Subagent(id: "s3", sessionID: "s3", type: nil, description: nil, running: true),
            Subagent(id: "s4", sessionID: "s4", type: "Test", description: "已结束", running: false)
        ]

        let active = runningSubagents(lead)
        #expect(active == ["扫目录", "Review", "子任务"])
        #expect(subagentsSummaryLine(active) == "子 Agent 在跑：扫目录、Review、子任务")
        // TaskNow still follows the lead agent itself
        #expect(TaskNow(lead: lead, working: true) == .step("在跑测试", since: lead.activityAt))

        // More than 3 running subagents append count
        lead.subagents?.append(Subagent(id: "s5", sessionID: "s5", type: "Verify", description: "验证修复", running: true))
        let more = runningSubagents(lead)
        #expect(more.count == 4)
        #expect(subagentsSummaryLine(more) == "子 Agent 在跑：扫目录、Review、子任务 等 4 个")

        // Subagent changes cause Session.sameApartFromTicks to detect difference and trigger redrawing
        var leadChanged = lead
        leadChanged.subagents?[0].running = false
        #expect(!Session.sameApartFromTicks(lead, leadChanged))
    }

    /// A step's time reads in minutes and is drawn again often enough for them.
    @Test func aStepSaysHowLongItHasGoneOn() {
        let start = Date(timeIntervalSince1970: 1_793_755_213)
        #expect(TaskClock.since(start, now: start.addingTimeInterval(20)) == "刚开始")
        #expect(TaskClock.since(start, now: start.addingTimeInterval(7 * 60 + 5)) == "7 分钟")
        #expect(TaskClock.tick <= 60)
    }

    @Test func stateFollowsTheAgents() {
        let todo = TodoItem(id: "t", title: "t")
        #expect(TaskState(todo: todo, agents: []) == .notStarted)
        #expect(TaskState(todo: todo, agents: [.working, .needsYou]) == .needsYou)
        #expect(TaskState(todo: todo, agents: [.working, .unread]) == .unread)
        #expect(TaskState(todo: todo, agents: [.working, .idle]) == .working)
        #expect(TaskState(todo: todo, agents: [.idle, .offline]) == .followup)
        var done = todo
        done.doneAt = .now
        #expect(TaskState(todo: done, agents: [.needsYou]) == .done)
        #expect(TaskState.needsYou.priority < TaskState.working.priority && TaskState.working.priority < TaskState.done.priority)
    }

    /// A task whose agent did not start ends failed (#1077): never going on, removable, out of the done ones.
    @Test func aTaskWhoseAgentDidNotStartIsFailed() throws {
        let t = try decode(#"""
        {"id":"todo_3","title":"对齐按钮","done_at":"2026-11-04T10:00:05Z","origin":"c1","plan_id":"plan_1",
         "start_error":"rpc: connection closed","agents":[]}
        """#, as: TodoItem.self)
        #expect(t.failedToStart && t.startError == "rpc: connection closed")
        let state = TaskState(todo: t, agents: [])
        #expect(state == .failed && state.isSettled && state.isRemovable)
        #expect(!TaskState.working.isRemovable && !TaskState.needsYou.isRemovable)
        #expect(TaskState.unread.isRemovable && TaskState.followup.isRemovable && TaskState.done.isRemovable)
        #expect(TaskState.notStarted.isRemovable) // a start the hub never reported back on must not stay for good
        let plain = try decode(#"{"id":"todo_4","title":"手动的","done_at":"2026-11-04T10:00:05Z","agents":[]}"#, as: TodoItem.self)
        #expect(!plain.failedToStart && TaskState(todo: plain, agents: []) == .done)
    }

    private func user(_ seq: Int, _ minutesAgo: Double) -> ChatLine {
        .row(.user(Item(id: "\(seq)#0", seq: seq, ts: Date(timeIntervalSinceNow: -minutesAgo * 60), role: "user", kind: .text,
                        text: "m\(seq)", toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)))
    }

    private func answer(_ seq: Int, _ minutesAgo: Double) -> ChatLine {
        .row(.assistant(Item(id: "\(seq)#0", seq: seq, ts: Date(timeIntervalSinceNow: -minutesAgo * 60), role: "assistant", kind: .text,
                             text: "a\(seq)", toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)))
    }

    private func feed(_ id: String, _ minutesAgo: Double) -> FeedItem {
        FeedItem(id: id, conversation: "c1", at: Date(timeIntervalSinceNow: -minutesAgo * 60), kind: .task, ref: "t", body: nil)
    }

    /// A feed item goes after its turn's answer (not between the message and the answer), before the next message;
    /// what came after the last message goes last.
    @Test func feedLandsAtTheEndOfItsExchange() {
        let lines = [user(1, 50), answer(2, 49), user(3, 30), answer(4, 29)]
        let merged = ChatLines.mergeFeed(lines, [feed("early", 49.5), feed("mid", 20), feed("late", 1)])
        #expect(merged.map(\.id) == ["1#0", "2#0", "feed-early", "3#0", "4#0", "feed-mid", "feed-late"])
        #expect(ChatLines.mergeFeed(lines, []).map(\.id) == lines.map(\.id))
    }

    @Test func mentionIsTheTrailingAt() {
        func hit(_ s: String) -> String? { TaskMentionList.mention(in: s).map { String(s[$0]) } }
        #expect(hit("@") == "@")
        #expect(hit("也改一下 @登录") == "@登录")
        #expect(hit("发到 me@example.com") == nil) // an address, not a mention
        #expect(hit("@登录 好了") == nil) // the mention ended with a space
        #expect(hit("没有") == nil)
    }

    private func toolCall(_ name: String, result: String?, error: Bool = false, seq: Int = 10) -> ToolCall {
        let use = Item(id: "\(seq)#0", seq: seq, ts: .now, role: "assistant", kind: .toolUse, text: nil, toolUseID: "tu\(seq)",
                       toolName: name, toolInput: nil, isError: nil, image: nil)
        let res = result.map { Item(id: "\(seq + 1)#0", seq: seq + 1, ts: .now, role: "user", kind: .toolResult, text: $0, toolUseID: "tu\(seq)",
                                    toolName: nil, toolInput: nil, isError: error, image: nil) }
        return ToolCall(use: use, result: res)
    }

    @Test func quickResultsComeFromThePhonesWrites() throws {
        let r = try #require(QuickResult(toolCall("mcp__persona__phone_reminders_create",
                                                  result: #"{"id":"R1","title":"给爸妈打电话","due":"2026-11-07T10:00:00+08:00","list":"提醒事项"}"#)))
        #expect(r.kind == .reminder && r.itemID == "R1" && r.title == "给爸妈打电话" && r.place == "提醒事项" && r.when != nil)
        let e = try #require(QuickResult(toolCall("phone_calendar_create_event",
                                                  result: #"{"id":"E1","title":"评审","start":"2026-11-03T15:00:00+08:00","calendar":"工作"}"#)))
        #expect(e.kind == .event && e.place == "工作")
        #expect(QuickResult(toolCall("mcp__persona__phone_reminders_list", result: #"{"id":"x"}"#)) == nil) // a read is no result card
        #expect(QuickResult(toolCall("mcp__persona__phone_reminders_create", result: #"{"error":"denied"}"#, error: true)) == nil)
        #expect(QuickResult(toolCall("mcp__persona__phone_reminders_create", result: nil)) == nil) // still running
    }

    /// Where the phone is and the places a search found become a map card (#5543); a failed, empty or running call
    /// does not, and neither do coordinates out of range.
    @Test func placeMapsComeFromTheLocationTools() throws {
        let here = try #require(PlaceMapResult(toolCall("mcp__persona__phone_location_current",
            result: #"{"latitude":31.23,"longitude":121.47,"accuracy_meters":12,"address":{"formatted":"上海市黄浦区人民大道"}}"#)))
        #expect(here.kind == .here && here.places.count == 1 && here.places[0].address == "上海市黄浦区人民大道")
        let nearby = try #require(PlaceMapResult(toolCall("phone_maps_search_nearby", result: """
            {"query":"cafe","places":[{"name":"A","address":"x","latitude":31.231,"longitude":121.475,"distance_meters":180},
            {"name":"","address":"Road 2","latitude":31.229,"longitude":121.476},{"name":"Bad","latitude":91,"longitude":0}]}
            """)))
        #expect(nearby.kind == .places && nearby.places.map(\.name) == ["A", nil])
        #expect(nearby.places[0].distance == 180 && nearby.places[1].distance == nil)
        #expect(PlaceMapText.title(nearby.places[1], number: 2) == "Road 2")
        let search = try #require(PlaceMapResult(toolCall("mcp__persona__phone_location_search",
            result: #"{"count":1,"places":[{"name":"Office","latitude":31.2,"longitude":121.4}]}"#)))
        #expect(search.kind == .places && search.places.count == 1)
        let many = (0..<20).map { #"{"name":"P\#($0)","latitude":31.2,"longitude":121.4}"# }.joined(separator: ",")
        #expect(PlaceMapResult(toolCall("phone_maps_search_nearby", result: #"{"places":[\#(many)]}"#))?.places.count == PlaceMapResult.most)
        #expect(PlaceMapResult(toolCall("phone_maps_search_nearby", result: #"{"count":0,"places":[]}"#)) == nil)
        #expect(PlaceMapResult(toolCall("phone_location_current", result: #"{"error":"denied"}"#, error: true)) == nil)
        #expect(PlaceMapResult(toolCall("phone_location_current", result: nil)) == nil) // still running
        // Navigate is a hand-off only when Maps opened (`opened: maps`, below).
        #expect(PlaceMapResult(toolCall("phone_maps_navigate", result: #"{"latitude":31.2,"longitude":121.4}"#)) == nil)
    }

    /// Directions handed to Maps become a card too (#5545): the destination, its name and the mode it went with. A
    /// failure, a result Maps didn't open or coordinates out of range draw nothing; a mode Persona doesn't know is left out.
    @Test func mapsHandoffsComeFromTheNavigateTool() throws {
        let walk = try #require(PlaceMapResult(toolCall("mcp__persona__phone_maps_navigate", result: """
            {"opened":"maps","destination":"安静书房咖啡","latitude":31.2291,"longitude":121.4769,"mode":"walking",
            "note":"Directions are open in the Maps app; navigation runs there, not in Persona."}
            """)))
        #expect(walk.kind == .handoff && walk.mode == .walking && walk.places.count == 1)
        #expect(PlaceMapText.destination(walk.places[0]) == "安静书房咖啡")
        #expect(PlaceMapText.handoff(.walking) == PlaceMapText.mode(.walking) + " · " + PlaceMapText.handoff(nil))
        let unnamed = try #require(PlaceMapResult(toolCall("phone_maps_navigate",
            result: #"{"opened":"maps","latitude":31.2,"longitude":121.4,"mode":"helicopter"}"#)))
        #expect(unnamed.mode == nil && unnamed.places[0].name == nil)
        #expect(PlaceMapText.destination(unnamed.places[0]) == String(localized: "Destination [Place Map]"))
        #expect(PlaceMapResult(toolCall("phone_maps_navigate", result: #"{"error":"Maps did not open"}"#, error: true)) == nil)
        #expect(PlaceMapResult(toolCall("phone_maps_navigate", result: #"{"opened":"maps","latitude":120,"longitude":121.4}"#)) == nil)
        #expect(PlaceMapResult(toolCall("phone_maps_navigate", result: nil)) == nil) // still running
        // Only a route or a hand-off carries a mode.
        #expect(PlaceMapResult(toolCall("phone_location_current", result: #"{"latitude":31.2,"longitude":121.4,"mode":"walking"}"#))?.mode == nil)
    }

    /// A route worked out without opening anything becomes a card (#5544): the destination, its mode, time and distance,
    /// and the line to draw. A transit route has no line; a line with a bad point is dropped, the card kept.
    @Test func routesComeFromTheRouteTool() throws {
        let walk = try #require(PlaceMapResult(toolCall("mcp__persona__phone_maps_route", result: """
            {"destination":"安静书房咖啡","latitude":31.2291,"longitude":121.4769,"mode":"walking","distance_meters":420,
            "expected_seconds":330,"line":[[31.2304,121.4737],[31.2299,121.4752],[31.2291,121.4769]],"from":"current_location"}
            """)))
        #expect(walk.kind == .route && walk.mode == .walking && walk.seconds == 330 && walk.places[0].distance == 420)
        #expect(walk.line.count == 3 && walk.line.first?.latitude == 31.2304)
        #expect(PlaceMapText.route(walk) == [PlaceMapText.mode(.walking), PlaceMapText.duration(330), PlaceMapText.distance(420)].joined(separator: " · "))
        // The map holds the whole line, the start too.
        let region = walk.region()
        #expect(region.center.latitude + region.span.latitudeDelta / 2 >= 31.2304)
        let transit = try #require(PlaceMapResult(toolCall("phone_maps_route",
            result: #"{"latitude":31.2,"longitude":121.4,"mode":"transit","distance_meters":8000,"expected_seconds":2100}"#)))
        #expect(transit.line.isEmpty && transit.mode == .transit)
        let bad = try #require(PlaceMapResult(toolCall("phone_maps_route",
            result: #"{"latitude":31.2,"longitude":121.4,"line":[[31.2,121.4],[95,121.4]]}"#)))
        #expect(bad.line.isEmpty && bad.seconds == nil && PlaceMapText.route(bad) == nil)
        #expect(PlaceMapResult(toolCall("phone_maps_route", result: #"{"error":"no route"}"#, error: true)) == nil)
        #expect(PlaceMapResult(toolCall("phone_maps_route", result: nil)) == nil) // still running
        // A route's time reads in minutes, at least one.
        #expect(PlaceMapText.duration(5) == PlaceMapText.duration(60))
    }

    /// The map shows every place, and one place is not drawn at street level.
    @Test func placeMapRegionHoldsEveryPlace() throws {
        let r = try #require(PlaceMapResult(toolCall("phone_maps_search_nearby",
            result: #"{"places":[{"name":"A","latitude":31.20,"longitude":121.40},{"name":"B","latitude":31.24,"longitude":121.48}]}"#)))
        let region = r.region()
        #expect(abs(region.center.latitude - 31.22) < 0.0001 && region.span.latitudeDelta >= 0.04 && region.span.longitudeDelta >= 0.08)
        let one = try #require(PlaceMapResult(toolCall("phone_location_current", result: #"{"latitude":31.2,"longitude":121.4}"#)))
        #expect(one.region().span.latitudeDelta > 0.004)
    }

    /// A map card goes where a reminder's card goes: right after its turn's work line.
    @Test func placeMapFollowsItsWork() {
        let call = toolCall("mcp__persona__phone_location_current", result: #"{"latitude":31.2,"longitude":121.4}"#)
        let work = ChatLine.work(id: "wt-1", items: [.step(.call(call))], live: false, start: .now, end: .now)
        #expect(ChatLines.quickResults([user(1, 5), work, answer(2, 4)]).map(\.id) == ["1#0", "wt-1", "map-10#0", "2#0"])
        // A hand-off to Maps goes to the same place (#5545).
        let handoff = toolCall("mcp__persona__phone_maps_navigate", result: #"{"opened":"maps","latitude":31.2,"longitude":121.4}"#, seq: 20)
        let handoffWork = ChatLine.work(id: "wt-2", items: [.step(.call(handoff))], live: false, start: .now, end: .now)
        #expect(ChatLines.quickResults([user(1, 5), handoffWork, answer(2, 4)]).map(\.id) == ["1#0", "wt-2", "map-20#0", "2#0"])
    }

    /// A turn's quick result goes right after its work line, after the line's open steps.
    @Test func quickResultFollowsItsWork() {
        let call = toolCall("mcp__persona__phone_reminders_create", result: #"{"id":"R1","title":"t"}"#)
        let work = ChatLine.work(id: "wt-1", items: [.step(.call(call))], live: false, start: .now, end: .now)
        let step = ChatLine.workStep(group: "wt-1", item: .step(.call(call)), first: true)
        let end = ChatLine.workEnd(group: "wt-1", count: 1)
        let lines = [user(1, 5), work, answer(2, 4)]
        #expect(ChatLines.quickResults(lines).map(\.id) == ["1#0", "wt-1", "quick-10#0", "2#0"])
        let open = [user(1, 5), work, step, end, answer(2, 4)]
        #expect(ChatLines.quickResults(open).map(\.id) == ["1#0", "wt-1", step.id, end.id, "quick-10#0", "2#0"])
    }

    @Test func aLineForEachDay() {
        let lines = [user(1, 60 * 26), answer(2, 60 * 26 - 1), user(3, 5), answer(4, 4)]
        let ids = ChatLines.daySections(lines).map(\.id)
        #expect(ids.filter { $0.hasPrefix("day-") }.count == 2)
        #expect(ids.first?.hasPrefix("day-") == true && ids[3].hasPrefix("day-") && ids[4] == "3#0")
        let now = Date.now
        #expect(DayLine.label(now, now: now).hasPrefix("今天 · "))
        #expect(DayLine.label(now.addingTimeInterval(-86_400), now: now).hasPrefix("昨天 · "))
    }

    @Test func todayBodyDecodes() throws {
        let item = try decode(#"""
        {"id":"f","conversation":"c1","at":"2026-11-03T00:00:00+08:00","kind":"today","ref":"2026-11-03",
         "body":{"date":"2026-11-03","done":[{"id":"t1","title":"博客迁移"}],"working":2,
                 "meetings":[{"start":"2026-11-03T15:00:00+08:00","end":"2026-11-03T16:00:00+08:00","title":"设计评审"}],
                 "at_me":[{"name":"发布群","unread":3,"preview":"能赶上吗"}],"unread":4}}
        """#, as: FeedItem.self)
        let body = try #require(item.today)
        #expect(item.kind == .today && body.done?.first?.title == "博客迁移" && body.working == 2 && body.meetings?.first?.title == "设计评审")
        #expect(body.atMe?.first?.name == "发布群" && body.unread == 4 && body.waiting == nil)
        #expect(body.failed == nil && body.failedTotal == nil) // a hub before #4124
    }

    /// What did not go through overnight (#4124): the hub's newest ones and how many there were in all.
    @Test func todayFailuresDecode() throws {
        let item = try decode(#"""
        {"id":"f","conversation":"c1","at":"2026-11-03T00:00:00+08:00","kind":"today","ref":"2026-11-03",
         "body":{"date":"2026-11-03","failed":[
           {"kind":"task","id":"t1","title":"对齐按钮","at":"2026-11-03T03:12:00+08:00","why":"rpc: connection closed"},
           {"kind":"routine","id":"rt_weekly","ref":"run_1","title":"周报","at":"2026-11-02T22:00:00+08:00","why":"assistant machine is offline"},
           {"kind":"routine","id":"evening","ref":"run_2","title":"晚间回顾","at":"2026-11-02T21:30:00+08:00","skipped":true}],
          "failed_total":5}}
        """#, as: FeedItem.self)
        let body = try #require(item.today)
        let failed = try #require(body.failed)
        #expect(failed.map(\.kind) == ["task", "routine", "routine"] && failed.map(\.id) == ["t1", "rt_weekly", "evening"])
        #expect(failed[0].why == "rpc: connection closed" && failed[0].ref == nil && failed[0].skipped == nil)
        #expect(failed[1].ref == "run_1" && failed[2].skipped == true && failed[2].why == nil && body.failedTotal == 5)
    }

    /// The Today card's row for them: one by its title, how it ended and why; several by how many (past the hub's list)
    /// and the first titles. Its button opens the newest: a task's sheet, or Scheduled Tasks where the app can.
    @Test func todayFailedRow() throws {
        let at = Date(timeIntervalSince1970: 1_793_700_000)
        let time = at.formatted(.dateTime.hour(.defaultDigits(amPM: .omitted)).minute())
        let task = TodayBody.Failed(kind: "task", id: "t1", title: "对齐按钮", at: at, why: "rpc: connection closed")
        let run = TodayBody.Failed(kind: "routine", id: "rt_weekly", ref: "run_1", title: "周报", at: at, why: " ")
        let skipped = TodayBody.Failed(kind: "routine", id: "evening", ref: "run_2", title: "晚间回顾", at: at, skipped: true)
        // Nothing failed, or a hub before #4124: no row, the card as it was.
        #expect(TodayFailedRow(TodayBody(date: "2026-11-03"), routines: true) == nil)
        #expect(TodayFailedRow(TodayBody(date: "2026-11-03", failed: [], failedTotal: 0), routines: true) == nil)

        let one = try #require(TodayFailedRow(TodayBody(date: "2026-11-03", failed: [task], failedTotal: 1), routines: true))
        #expect(one.title == "没成：对齐按钮" && one.detail == "没开起来 · \(time) — rpc: connection closed" && one.run == .open("t1"))
        #expect(TodayFailedRow.line(run) == "运行失败 · \(time)") // a blank why says nothing
        #expect(TodayFailedRow.line(skipped) == "被跳过 · \(time)")

        let several = try #require(TodayFailedRow(TodayBody(date: "2026-11-03", failed: [run, skipped, task], failedTotal: 5), routines: true))
        #expect(several.title == "5 件没成" && several.detail == "周报、晚间回顾、对齐按钮 · 还有 2 件" && several.run == .routines)
        let listed = try #require(TodayFailedRow(TodayBody(date: "2026-11-03", failed: [run, task]), routines: false))
        #expect(listed.title == "2 件没成" && listed.detail == "周报、对齐按钮")
        #expect(listed.run == nil) // no way to Scheduled Tasks from the conversation (the Mac): no button
    }

    @Test func messageCardDecodes() throws {
        let item = try decode(#"""
        {"id":"f","conversation":"c1","at":"2026-11-03T10:00:00Z","kind":"message","ref":"lark:f1:12",
         "body":{"todo_id":"t1","source":"lark","account":"飞书","chat":"发版群","sender":"陈晓","text":"能修好吗？","draft":"测试过了"}}
        """#, as: FeedItem.self)
        #expect(item.kind == .message && item.ref == "lark:f1:12" && item.todoID == "t1" && item.chat == "发版群")
        #expect(item.sender == "陈晓" && item.text == "能修好吗？" && item.draft == "测试过了" && item.source == "lark")
    }
}

