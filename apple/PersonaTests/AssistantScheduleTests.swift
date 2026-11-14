import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Persona › Scheduled Tasks: the rules in words, the times, and what runs next (docs/assistant/assistant.md).
@MainActor
struct AssistantScheduleTests {
    private func routine(_ time: String, _ days: [Int], enabled: Bool = true, next: Date? = nil) -> APIClient.ScheduledTask {
        var r = APIClient.ScheduledTask(id: "r", title: "早报", prompt: "x", time: time, weekdays: days, enabled: enabled)
        r.nextRun = next
        return r
    }

    @Test func rulesInWords() {
        #expect(AssistantScheduleText.sentence(routine("09:00", [1, 2, 3, 4, 5])) == "每个工作日 上午 9:00")
        #expect(AssistantScheduleText.sentence(routine("21:30", [])) == "每天 晚上 9:30")
        #expect(AssistantScheduleText.sentence(routine("17:00", [5])) == "每周五 下午 5:00")
        #expect(AssistantScheduleText.sentence(routine("08:15", [6, 7])) == "每个周末 早上 8:15")
        #expect(AssistantScheduleText.sentence(routine("00:05", [1, 3])) == "每周一、三 凌晨 12:05")
        #expect(AssistantScheduleText.clock("12:30") == "中午 12:30")
        #expect(AssistantScheduleText.sentence(.init(title: "巡检", prompt: "x", repeats: .interval, everyMinutes: 30)) == "每 30 分钟")
        #expect(AssistantScheduleText.every(120) == "每 2 小时")
        #expect(AssistantScheduleText.every(90) == "每 1 小时 30 分钟")
    }

    /// A cron task (hubs with task_cron, #4399): the hub's expression, its words and next runs come in; the list says the
    /// expression first, then the words; a save sends the expression back so the hub keeps it, and a task without one sends none.
    @Test func cronTasks() throws {
        let json = #"{"id":"c","title":"部署","prompt":"x","repeat":"cron","cron":"*/30 9-18 * * 1-5","cron_text":"每隔 30 分钟","enabled":true,"#
            + #""next_runs":["2026-11-12T09:00:00+08:00","2026-11-12T09:30:00+08:00"],"next_run":"2026-11-12T09:00:00+08:00","at":"0001-01-01T00:00:00Z"}"#
        let t = try JSON.decoder.decode(APIClient.ScheduledTask.self, from: Data(json.utf8))
        #expect(t.repeats == .cron && t.cron == "*/30 9-18 * * 1-5" && t.cronText == "每隔 30 分钟")
        #expect(t.nextRuns.count == 2 && t.nextRun == t.nextRuns.first && t.at == nil)
        #expect(AssistantScheduleText.sentence(t) == "*/30 9-18 * * 1-5 · 每隔 30 分钟")
        #expect(AssistantScheduleText.symbol(t) == "alarm")
        var bare = t
        bare.cronText = nil // the hub has no words for it
        #expect(AssistantScheduleText.sentence(bare) == "按计划 */30 9-18 * * 1-5")
        func body(_ t: APIClient.ScheduledTask) throws -> [String: Any] {
            try JSONSerialization.jsonObject(with: JSONEncoder().encode(t)) as! [String: Any]
        }
        #expect(try body(t)["cron"] as? String == "*/30 9-18 * * 1-5")
        #expect(try body(t)["repeat"] as? String == "cron")
        #expect(try body(routine("09:00", []))["cron"] == nil, "a daily task sends no cron: an older hub never gets the field")
    }

    @Test func whatATaskRunsWith() {
        var t = APIClient.ScheduledTask(title: "巡检", prompt: "x", repeats: .interval, everyMinutes: 30)
        #expect(AssistantScheduleText.runsWith(t) == nil, "the defaults say nothing")
        t.launcher = "claude-fast"
        t.permissionMode = "acceptEdits"
        t.effort = "low"
        #expect(AssistantScheduleText.runsWith(t)?.text == "claude-fast · 自动改文件 · 思考低")
        #expect(AssistantScheduleText.runsWith(t)?.danger == false)
        t.permissionMode = "bypassPermissions"
        #expect(AssistantScheduleText.runsWith(t)?.danger == true)
    }

    /// Push When Done (#2676): "asked" comes from the hub and goes back; a hub that never sends it, or sends "", is Every Run, and
    /// such a task saves no `push` at all (an older hub never gets the field).
    @Test func pushRoundTrip() throws {
        func decode(_ push: String) throws -> APIClient.ScheduledTask {
            try JSON.decoder.decode(APIClient.ScheduledTask.self,
                                    from: Data(#"{"id":"p","title":"巡检","prompt":"x","repeat":"interval","every_minutes":30,"enabled":true\#(push)}"#.utf8))
        }
        func body(_ t: APIClient.ScheduledTask) throws -> [String: Any] {
            try JSONSerialization.jsonObject(with: JSONEncoder().encode(t)) as! [String: Any]
        }
        let asked = try decode(",\"push\":\"asked\"")
        #expect(asked.pushesOnlyAsked)
        #expect(try body(asked)["push"] as? String == "asked")
        for old in ["", ",\"push\":\"\""] {
            let t = try decode(old)
            #expect(t.push == nil && !t.pushesOnlyAsked)
            #expect(try body(t)["push"] == nil, "left out: the hub keeps it")
        }
        var back = asked
        back.push = "" // switched back to Every Run
        #expect(try body(back)["push"] as? String == "")
    }

    /// The capsule row says "Pushes only when it alerts you" on its rule line, the plain row on a line of its own; the editor sees a switch to it as a change, and nil and "" as the same.
    @Test func pushOnTheRowAndInTheEditor() {
        var t = APIClient.ScheduledTask(id: "p", title: "巡检", prompt: "x", repeats: .interval, everyMinutes: 30)
        #expect(AssistantScheduleText.ruleLine(t) == "每 30 分钟")
        var asked = t
        asked.push = APIClient.ScheduledTask.pushAsked
        #expect(AssistantScheduleText.ruleLine(asked) == "每 30 分钟 · 只在提醒时推送")
        #expect(AssistantScheduleText.pushMark(t) == nil)
        #expect(AssistantScheduleText.rule(asked) == "每 30 分钟", "the plain row keeps the mark off its one-line rule")
        #expect(AssistantScheduleText.pushMark(asked) == "只在提醒时推送")
        #expect(!AssistantTaskEditor.same(t, asked))
        var every = t
        every.push = ""
        #expect(AssistantTaskEditor.same(t, every))
        t = APIClient.ScheduledTask(id: "f", title: "", prompt: "x", repeats: .once, at: .now.addingTimeInterval(600))
        t.push = APIClient.ScheduledTask.pushAsked
        #expect(AssistantScheduleText.ruleLine(t) == "一次 · 新对话 · 只在提醒时推送")
    }

    /// 17:00 is "17:00": the system's two-digit hour without AM/PM is 12-hour in a Chinese locale ("05:00").
    @Test func timesAre24Hour() {
        let d = Calendar.current.date(bySettingHour: 17, minute: 5, second: 0, of: .now)!
        #expect(AssistantScheduleText.hm(d) == "17:05")
        #expect(AssistantScheduleText.moment(d) == "今天 17:05")
    }

    @Test func upcomingMergesEveryKindSoonestFirst() {
        let now = Date.now
        var off = routine("10:00", [], enabled: false, next: now.addingTimeInterval(60))
        off.id = "off"
        var f = APIClient.ScheduledTask(id: "f", title: "", prompt: "看看 CI\n细节", repeats: .once, at: now.addingTimeInterval(600))
        f.machineID = "m"
        f.sessionID = "s"
        var p = APIClient.ScheduledTask(id: "p", title: "巡检", prompt: "x", repeats: .interval, everyMinutes: 30)
        p.nextRun = now.addingTimeInterval(1200)
        let up = AssistantUpcoming.merge([routine("09:00", [], next: now.addingTimeInterval(7200)), off, f, p])
        #expect(up.map(\.id) == ["t-f", "t-p", "t-r"]) // the switched-off one is not coming up
        #expect(up[1].detail == "每 30 分钟")
        #expect(up[0].title == "看看 CI")
        #expect(up[0].key == SessionKey(machineID: "m", sessionID: "s"))
    }

    @Test func runDetail() {
        let due = Date(timeIntervalSince1970: 1_000_000)
        func run(_ late: TimeInterval, _ status: String = "ok", manual: Bool? = nil) -> APIClient.ScheduleRun {
            APIClient.ScheduleRun(id: "x", kind: "routine", refID: "r", title: "早报", due: due, at: due.addingTimeInterval(late),
                                  status: status, error: status == "failed" ? "offline" : nil, manual: manual)
        }
        #expect(AssistantRunText.detail(run(0.4)) == "准时")
        #expect(AssistantRunText.detail(run(21)) == "晚了 21 秒")
        #expect(AssistantRunText.detail(run(40 * 60)) == "晚了 40 分钟")
        #expect(AssistantRunText.detail(run(0, "failed")) == "offline")
        #expect(AssistantRunText.detail(run(0, manual: true)) == "手动运行")
    }
}
