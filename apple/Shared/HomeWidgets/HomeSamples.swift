#if DEBUG
import Foundation

/// Made-up snapshots for the gallery (`-showHomeWidgets`) and the tests: no person, account or machine of anyone's.
/// `zh` gives the data (titles, names) in Chinese, as a Chinese user's own would be.
enum HomeSamples {
    /// 13:20 on a Thursday, so every time on the widgets reads the same in every shot.
    static let now: Date = {
        var c = DateComponents()
        c.year = 2026; c.month = 11; c.day = 12; c.hour = 13; c.minute = 20
        return Calendar.current.date(from: c) ?? .now
    }()

    /// Today's tokens by the hour, ending at now's hour as the hub's do (nothing after it): the gallery's 13:20 day,
    /// moved with the clock like the events.
    static func hourly(_ now: Date) -> [Int] {
        let day = [0, 0, 0, 0, 0, 0, 0, 12_000, 48_000, 96_000, 150_000, 132_000, 88_000, 210_000]
        let hour = Calendar.current.component(.hour, from: now)
        return (0..<24).map { h in
            let i = h - hour + day.count - 1
            return h <= hour && day.indices.contains(i) ? day[i] : 0
        }
    }

    static func busy(zh: Bool = false, package: Bool = false, now: Date = HomeSamples.now) -> HomeSnapshot {
        func t(_ en: String, _ cn: String) -> String { zh ? cn : en }
        // At the gallery's 13:20 the day's events keep their times; at any other moment (the app's sample, written
        // for Home Screen shots) they move by whole hours, so they still read as round times and still come after now.
        let shift = (now.timeIntervalSince(HomeSamples.now) / 3600).rounded() * 3600
        func at(_ hour: Int, _ minute: Int) -> Double {
            var c = Calendar.current.dateComponents([.year, .month, .day], from: HomeSamples.now)
            c.hour = hour; c.minute = minute
            return (Calendar.current.date(from: c) ?? HomeSamples.now).timeIntervalSince1970 + shift
        }
        func ago(_ minutes: Double) -> Double { now.timeIntervalSince1970 - minutes * 60 }
        var s = HomeSnapshot()
        s.at = ago(1)
        s.paired = true
        s.look = package
            ? .init(name: t("Milk Frog", "奶蛙"), package: "example-persona/sample", primaryLight: 0xF6_C2_3E, primaryDark: 0xF6_C2_3E)
            : .init(name: "Mochi")
        s.agents.rows = [
            .init(id: "studio/s1", title: t("Fix the sign-in redirect", "修好登录后的跳转"), state: .needsYou, kind: .coding, machine: "studio", place: t("Studio", "工作室"),
                  since: ago(4), ask: t("Plan Awaiting Approval", "计划等你审批")),
            .init(id: "hub/s2", title: t("Draft the weekly report", "写周报初稿"), state: .needsYou, kind: .assistant, machine: "hub",
                  since: ago(12), ask: t("Persona Has a Question", "Persona 在问你")),
            .init(id: "studio/s3", title: t("Move settings to the new store", "把设置迁到新存储"), state: .working, kind: .coding, machine: "studio",
                  since: ago(22), detail: t("Running tests", "正在跑测试")),
            .init(id: "hub/s4", title: t("Compare hotels in Kyoto", "比较京都的酒店"), state: .working, kind: .assistant, machine: "hub",
                  since: ago(3), detail: t("Reading 6 pages", "正在看 6 个网页")),
            .init(id: "air/s5", title: t("Release notes for 2.4", "2.4 版发布说明"), state: .working, kind: .coding, machine: "air",
                  since: ago(8), detail: t("Writing", "正在写")),
            .init(id: "studio/s6", title: t("Clean up old branches", "清理旧分支"), state: .done, kind: .coding, machine: "studio", since: ago(30)),
        ]
        s.today = .init(
            events: [
                .init(title: t("Design review", "设计评审"), start: at(14, 0), end: at(15, 0)),
                .init(title: t("1:1 with Alex", "和 Alex 一对一"), start: at(16, 30), end: at(17, 0)),
                .init(title: t("Movie with Sam", "和小林看电影"), start: at(19, 0), end: at(21, 0)),
            ],
            source: .calendar)
        let todays = s.today?.events ?? []
        s.today?.days = days(zh: zh, now: now, today: todays)
        s.todos = .init(
            items: [
                .init(id: "t1", title: t("Renew the passport", "续签护照"), due: at(9, 0), overdue: true),
                .init(id: "t2", title: t("Call the bank", "给银行回电话"), due: at(15, 30)),
                .init(id: "t3", title: t("Buy a birthday gift", "买生日礼物")),
                .init(id: "t4", title: t("Book the dentist", "约牙医")),
                .init(id: "t5", title: t("Reply to the landlord", "回复房东")),
                .init(id: "t6", title: t("Water the plants", "给植物浇水")),
            ],
            count: 6, overdue: 1)
        s.usage = .init(
            accounts: [
                .init(id: "a1", name: "Claude Max", windows: [.init(label: "5h", minutes: 300, percent: 86, resetsAt: at(15, 40)),
                                                             .init(label: "7d", minutes: 10080, percent: 61, resetsAt: at(23, 0))]),
                .init(id: "a2", name: "Codex Pro", windows: [.init(label: "5h", minutes: 300, percent: 42, resetsAt: at(16, 10)),
                                                            .init(label: "7d", minutes: 10080, percent: 18, resetsAt: at(23, 30))]),
            ],
            tokensToday: 1_240_000, tpm: 3100,
            hourly: hourly(now),
            costToday: 3.4)
        s.machines = [
            .init(id: "studio", name: t("Studio", "工作室"), online: true, working: 2, needsYou: 1, load: 46, icon: "desktop"),
            .init(id: "air", name: "MacBook Air", online: true, working: 1, load: 18),
            .init(id: "hub", name: t("Home Server", "家里的服务器"), online: true, load: 8, icon: "server"),
            .init(id: "mini", name: t("Old Mac mini", "旧 Mac mini"), online: false, icon: "desktop"),
        ]
        s.health = .init(steps: 6842, stepsAverage: 7900, sleepHours: 7.2, restingHeartRate: 58, activeEnergy: 412,
                         week: [9120, 7340, 10480, 6210, 8890, 7210, 6842])
        s.inbox = .init(needsYou: 3, unread: 9, latest: [
            .init(id: "m1", sender: "Alex", summary: t("Can you confirm the venue for Friday?", "周五的场地你能确认一下吗？"), at: at(12, 42), needsYou: true),
            .init(id: "m2", sender: t("Team chat", "项目群"), summary: t("The release went out; two follow-ups for you", "版本发出去了，有两件事要你跟进"), at: at(11, 10), needsYou: true),
            .init(id: "m3", sender: t("Bank", "银行"), summary: t("Your November statement is ready", "你的 11 月账单已出"), at: at(9, 30)),
            .init(id: "m4", sender: t("School", "学校"), summary: t("Parent evening moved to next Tuesday", "家长会改到下周二"), at: at(8, 55)),
            .init(id: "m5", sender: "Sam", summary: t("Dinner at 7 still good?", "晚上 7 点吃饭还行吗？"), at: at(8, 20)),
        ])
        return s
    }

    /// The weeks around the sample's day for the Calendar widget's week and month: a made-up calendar of meetings,
    /// errands and evenings at their clock times; today's are `today` (the widget's list) and a stand-up already over.
    static func days(zh: Bool, now: Date, today: [HomeSnapshot.Event]) -> HomeSnapshot.Days {
        func t(_ en: String, _ cn: String) -> String { zh ? cn : en }
        let made: [(day: Int, title: String, hour: Int, minute: Int, minutes: Int)] = [
            (-10, t("Team sync", "周会"), 9, 30, 30), (-9, t("Dentist check-up", "牙医复查"), 15, 0, 30),
            (-7, t("Dinner with Sam", "和小林吃饭"), 19, 0, 120), (-3, t("Team sync", "周会"), 9, 30, 30),
            (-2, t("Pay the rent", "交房租"), 0, 0, -1), (-1, t("Lunch with Sam", "和小林吃午饭"), 12, 0, 60),
            (1, t("Mom's birthday", "妈妈生日"), 0, 0, -1), (1, t("Dentist", "牙医"), 9, 0, 30),
            (2, t("Quarterly review", "季度复盘"), 15, 0, 90), (3, t("Badminton", "羽毛球"), 10, 0, 120),
            (4, t("Team sync", "周会"), 9, 30, 30), (5, t("Check-up", "体检"), 8, 0, 60),
            (7, t("Dinner party", "朋友聚餐"), 18, 0, 180), (8, t("Trip", "出差"), 0, 0, -1),
            (11, t("Team sync", "周会"), 9, 30, 30), (13, t("Concert", "音乐会"), 20, 0, 120),
        ]
        let calendar = Calendar.current
        let midnight = calendar.startOfDay(for: now)
        func day(_ offset: Int) -> Date { calendar.date(byAdding: .day, value: offset, to: midnight) ?? midnight }
        var events = made.map { m -> HomeSnapshot.Event in
            let start = day(m.day)
            if m.minutes < 0 {
                return .init(title: m.title, start: start.timeIntervalSince1970, end: day(m.day + 1).timeIntervalSince1970, allDay: true)
            }
            let at = start.timeIntervalSince1970 + Double(m.hour * 3600 + m.minute * 60)
            return .init(title: m.title, start: at, end: at + Double(m.minutes) * 60)
        }
        let standUp = max(midnight.timeIntervalSince1970, now.timeIntervalSince1970 - 2 * 3600)
        events.append(.init(title: t("Stand-up", "站会"), start: standUp, end: standUp + 15 * 60))
        events += today
        return HomeDays.build(events, range: HomeDays.range(around: now, calendar: calendar), calendar: calendar)
    }

    /// The same day with nothing to show: every widget's empty state.
    static func empty(zh: Bool = false) -> HomeSnapshot {
        var s = busy(zh: zh)
        s.agents = .init()
        s.today?.events = []
        s.today?.days = HomeDays.build([], range: HomeDays.range(around: now))
        s.todos = .init()
        s.inbox = .init(needsYou: 0, unread: 0, latest: [])
        return s
    }

    /// To-dos just ticked on the widget: one on its way to the hub, one the hub refused.
    static func ticked(zh: Bool = false, now: Date = HomeSamples.now) -> HomeSnapshot {
        var s = busy(zh: zh, now: now)
        s.todos?.items[1].tick = .ticking
        s.todos?.items[2].tick = .failed
        return s
    }

    /// The same day with nothing waiting: only a finished agent.
    static func calm(zh: Bool = false) -> HomeSnapshot {
        var s = busy(zh: zh)
        s.agents.rows = s.agents.rows.filter { $0.state == .done }
        if var inbox = s.inbox {
            inbox.needsYou = 0
            inbox.latest = inbox.latest.map { var m = $0; m.needsYou = false; return m }
            s.inbox = inbox
        }
        s.machines = s.machines.map { var m = $0; m.working = 0; m.needsYou = 0; return m }
        return s
    }

    /// A recording under way.
    static func recording(zh: Bool = false) -> HomeSnapshot {
        var s = busy(zh: zh)
        // The running time counts from the real clock (`Text(timerInterval:)`), not the sample's.
        s.recording = .init(id: "r1", title: zh ? "设计评审" : "Design review", started: Date().timeIntervalSince1970 - 12 * 60 - 5)
        return s
    }

    /// The app has not written for an hour.
    static func stale(zh: Bool = false) -> HomeSnapshot {
        var s = busy(zh: zh)
        s.at -= 64 * 60
        return s
    }

    static let unpaired = HomeSnapshot()
}
#endif
