import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Which conversations scheduled tasks started (#2838, as the web's #2824): kept on Scheduled Tasks, not in the sidebar's Recent.
struct AssistantTaskChatsTests {
    private func task(_ id: String, title: String = "", session: String? = nil, own: String? = nil, last: String? = nil,
                      lastRun: Date? = nil, repeats: APIClient.ScheduledTask.Repeat = .daily) -> APIClient.ScheduledTask {
        var t = APIClient.ScheduledTask(id: id, title: title, prompt: "p", repeats: repeats)
        t.sessionID = session
        t.runMachineID = own == nil ? nil : "server"
        t.runSessionID = own
        t.lastSessionID = last
        t.lastRun = lastRun
        return t
    }

    private func run(_ ref: String, _ sid: String?, at: Date = .now, machine: String? = "server") -> APIClient.ScheduleRun {
        APIClient.ScheduleRun(id: UUID().uuidString, kind: "routine", refID: ref, title: "", due: at, at: at, status: "ok",
                              machineID: machine, sessionID: sid)
    }

    private func session(_ id: String, _ title: String = "", updated: Date = .now) throws -> Session {
        let o: [String: Any] = ["machine_id": "server", "session_id": id, "status": "idle", "title": title, "assistant": true,
                                "updated_at": ISO8601DateFormatter().string(from: updated)]
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: o))
    }

    private var known: AssistantTaskChats {
        AssistantTaskChats(tasks: [task("a", title: "日报", own: "own", last: "own"),
                                   task("b", title: "看看部署好了没有，好了告诉我一声", session: "mine", last: "mine", repeats: .once)],
                           runs: [run("a", "r1"), run("b", "mine")])
    }

    @Test func knowsTheTasksAndRunsConversationsNotTheOneATaskCameFrom() {
        #expect(known.ids == ["own", "r1"])
        #expect(known.contains(sessionID: "own", title: "约周五吃饭"))
        #expect(!known.contains(sessionID: "mine", title: "1105 | 修复 | 搜索框"))
    }

    /// The runs the hub lists are capped: an older run's conversation is known by the title the hub gave it.
    @Test func knowsAnOlderRunsByTheHubsTitleForOneOfTheTasks() {
        #expect(known.contains(sessionID: "x", title: "1101 | 定时 | 日报"))
        #expect(known.contains(sessionID: "y", title: "1101 | 定时 | 看看部署好了没有，好了告")) // cut to fit
        // The model may call the user's own conversation 「定时」 too.
        #expect(!known.contains(sessionID: "z", title: "1105 | 定时 | 设日报提醒"))
        #expect(!known.contains(sessionID: "w", title: ""))
    }

    @Test func recentLeavesTheTasksOutButKeepsTheOpenOne() throws {
        let now = Date.now
        let list = [try session("own", "1105 | 定时 | 日报", updated: now), try session("u", "约周五吃饭", updated: now - 60),
                    try session("x", "1101 | 定时 | 日报", updated: now - 120)]
        #expect(known.recent(list, current: nil).map(\.sessionID) == ["u"])
        #expect(known.recent(list, current: list[2].key).map(\.sessionID) == ["u", "x"])
        #expect(AssistantTaskChats.none.recent(list, current: nil).map(\.sessionID) == ["own", "u", "x"])
    }

    @Test func aTasksConversationsAreItsOwnThenItsRunsOnceEach() {
        let now = Date.now
        let t = task("a", own: "own")
        let runs = [run("a", "r2", at: now), run("b", "x"), run("a", "own", at: now - 60), run("a", "r1", at: now - 120), run("a", nil)]
        let chats = AssistantTaskChats.chats(of: t, runs: runs, machineID: "server")
        #expect(chats.map(\.key.sessionID) == ["own", "r2", "r1"])
        #expect(chats.first?.at == now - 60) // when a run last went into it
        #expect(chats.map(\.key.machineID) == ["server", "server", "server"])
        // The one it came from (Continue in Original Chat) is the user's own.
        #expect(AssistantTaskChats.chats(of: task("b", session: "mine", repeats: .once), runs: [run("b", "mine")], machineID: "server").isEmpty)
        #expect(AssistantTaskChats.chats(of: t, runs: [run("a", "r1"), run("a", "r2"), run("a", "r3")], machineID: "server", limit: 2)
            .map(\.key.sessionID) == ["own", "r1"])
        // Its own one with no run listed: the task's last run says when.
        let last = task("c", own: "o", last: "o", lastRun: now)
        #expect(AssistantTaskChats.chats(of: last, runs: [], machineID: "server").first?.at == now)
    }

    @Test func aChatsLabelSaysWhen() throws {
        let cal = Calendar.current
        let now = try #require(cal.date(from: DateComponents(year: 2026, month: 11, day: 5, hour: 15)))
        let at = { (d: Int, h: Int, m: Int) in cal.date(from: DateComponents(year: 2026, month: 11, day: d, hour: h, minute: m))! }
        let key = SessionKey(machineID: "server", sessionID: "s")
        #expect(AssistantTaskChats.label(.init(key: key, at: at(5, 9, 5)), index: 0, now: now) == "今天 09:05")
        #expect(AssistantTaskChats.label(.init(key: key, at: at(4, 21, 30)), index: 0, now: now) == "昨天 21:30")
        #expect(AssistantTaskChats.label(.init(key: key, at: at(1, 9, 0)), index: 0, now: now) == "11月1日 09:00")
        #expect(AssistantTaskChats.label(.init(key: key, at: nil), index: 1, now: now) == "对话 2")
    }
}
