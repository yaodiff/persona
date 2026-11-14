import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The Home Screen widgets' snapshot (docs/apple/home-widgets.md): the numbers are the app's own, the file is written only
/// when it changes and read back only in its own shape, and the moods follow the rules the doc gives.
@MainActor
struct HomeWidgetTests {
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    private struct Fixture: Decodable {
        var cases: [Case]
        struct Case: Decodable {
            var name: String
            var input: Input
            var expected: Expected
        }
        struct Input: Decodable {
            var sessions: [Session]
            var interactions: [Interaction]
            var mutes: Pins
            var quiet: [String]
        }
        struct Expected: Decodable {
            var badge: Int
        }
    }

    /// Replays spec/fixtures/needs-you/cases.json: the Agents widget's Needs You rows are the Agents tab's badge, case by
    /// case (one per session, never the muted agents or a quiet task's).
    @Test func needsYouIsTheAgentsBadge() throws {
        let data = try Data(contentsOf: Self.repo.appending(path: "spec/fixtures/needs-you/cases.json"))
        let fixture = try JSON.decoder.decode(Fixture.self, from: data)
        #expect(!fixture.cases.isEmpty)
        for c in fixture.cases {
            let i = c.input
            let byKey = Dictionary(i.sessions.map { ($0.key, $0) }, uniquingKeysWith: { a, _ in a })
            let pending = i.interactions.sorted { ($0.createdAt ?? .distantPast) < ($1.createdAt ?? .distantPast) }
            let rows = AgentsNeedsYou.rows(cards: pending, waiting: AgentsNeedsYou.waiting(i.sessions))
            let agents = HomeAgents.build(
                needsYou: rows, sessions: i.sessions,
                place: { TabBadges.place($0, session: byKey[$0], quiet: i.quiet.contains(AppModel.pinKey(session: $0)), mutes: i.mutes) },
                session: { byKey[$0] }, machineName: { _ in nil }, isTask: { _ in false })
            #expect(agents.rows.count { $0.state == .needsYou } == c.expected.badge, "\(c.name)")
        }
    }

    /// Past the cap the rows are counted, not listed, so a full list never says fewer than there are.
    @Test func rowsPastTheCapAreCounted() {
        let sessions = (0..<(HomeSnapshot.Agents.cap + 5)).map { n in
            Self.session("s\(n)", state: "working")
        }
        let agents = HomeAgents.build(needsYou: [], sessions: sessions, place: { _ in .agents }, session: { _ in nil },
                                      machineName: { _ in nil }, isTask: { _ in false })
        #expect(agents.rows.count == HomeSnapshot.Agents.cap)
        let context = HomeContext(snapshot: HomeSnapshot(paired: true, agents: agents), date: .now)
        #expect(context.agentCounts.working == HomeSnapshot.Agents.cap + 5)
    }

    private static func session(_ id: String, state: String) -> Session {
        let json = """
        {"machine_id":"m","session_id":"\(id)","title":"\(id)","cwd":"/tmp/p","status":"idle","state":"\(state)"}
        """
        do { return try JSON.decoder.decode(Session.self, from: Data(json.utf8)) } catch { fatalError("bad fixture: \(error)") }
    }

    @Test func snapshotReadsBackOnlyInItsShape() throws {
        let dir = FileManager.default.temporaryDirectory.appending(path: "home-widgets-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = HomeSnapshotStore(root: dir)
        #expect(store.read() == nil)
        var s = HomeSnapshot(at: 1_000, paired: true)
        s.inbox = .init(needsYou: 2, unread: 5, latest: [.init(id: "m1", sender: "Alex", summary: "Hello", at: 900, needsYou: true)])
        try store.write(try #require(HomeWidgetWriter.encode(s)))
        #expect(store.read() == s)
        s.version += 1
        try store.write(try #require(HomeWidgetWriter.encode(s)))
        #expect(store.read() == nil) // another version's file reads as none
        try store.write(Data("{".utf8))
        #expect(store.read() == nil)
    }

    /// A change in one part reloads that kind's widgets only; the persona and pairing count for every kind.
    @Test func partsTellWhichWidgetsChanged() {
        let a = HomeSnapshot(paired: true)
        func changed(_ b: HomeSnapshot) -> Set<HomeKind> {
            let pa = HomeWidgetWriter.parts(a), pb = HomeWidgetWriter.parts(b)
            return Set(HomeKind.allCases.filter { pa[$0] != pb[$0] })
        }
        var b = a
        b.inbox = .init(needsYou: 1)
        #expect(changed(b) == [.inbox])
        b = a
        b.recording = .init(id: "r", title: "Review", started: 1)
        #expect(changed(b) == [.record])
        b = a
        b.machines = [.init(id: "m", name: "Studio", online: true)]
        #expect(changed(b) == [.agents, .computers]) // the Agents widget names the computer an agent runs on
        b = a
        b.look.name = "Milk Frog"
        #expect(changed(b) == Set(HomeKind.allCases))
    }

    /// Written once; the same content again writes nothing (the file keeps its time); new content writes again.
    @Test func writesOnlyWhatChanged() async throws {
        let dir = FileManager.default.temporaryDirectory.appending(path: "home-widgets-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: dir) }
        let store = HomeSnapshotStore(root: dir)
        let writer = HomeWidgetWriter(store: store)
        var source = HomeSnapshot(paired: true)
        writer.source = { source }
        writer.flush()
        let first = try #require(await Self.written(store) { $0.at > 0 })
        writer.flush()
        try await Task.sleep(for: .milliseconds(300))
        #expect(store.read()?.at == first.at)
        source.inbox = .init(needsYou: 3)
        writer.flush()
        let second = try #require(await Self.written(store) { $0.inbox?.needsYou == 3 })
        #expect(second.at >= first.at)
        // A recording that starts writes at once, without a source change.
        writer.recording = .init(id: "r", title: "Review", started: 1)
        #expect(await Self.written(store) { $0.recording?.id == "r" } != nil)
    }

    private static func written(_ store: HomeSnapshotStore, _ ok: (HomeSnapshot) -> Bool) async -> HomeSnapshot? {
        for _ in 0..<100 {
            if let s = store.read(), ok(s) { return s }
            try? await Task.sleep(for: .milliseconds(20))
        }
        return nil
    }

    @Test func moodsFollowTheRules() {
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        var s = HomeSnapshot(at: now.timeIntervalSince1970, paired: true)
        s.agents.rows = [.init(id: "m/a", title: "A", state: .working)]
        #expect(HomeContext(snapshot: s, date: now).mood(.agents) == .working)
        s.agents.rows.append(.init(id: "m/b", title: "B", state: .needsYou))
        #expect(HomeContext(snapshot: s, date: now).mood(.agents) == .needsYou)
        // Over an hour without a write: offline, whatever the counts.
        #expect(HomeContext(snapshot: s, date: now.addingTimeInterval(HomeSnapshot.staleAfter + 1)).mood(.agents) == .offline)
        s.usage = .init(accounts: [.init(id: "a", name: "Plan", windows: [.init(label: "5h", minutes: 300, percent: 85)])])
        #expect(HomeContext(snapshot: s, date: now).mood(.usage) == .working)
        s.usage?.accounts[0].windows[0].percent = 96
        #expect(HomeContext(snapshot: s, date: now).mood(.usage) == .needsYou)
        s.todos = .init(items: [], count: 0, overdue: 0)
        #expect(HomeContext(snapshot: s, date: now).mood(.todos) == .done)
        s.inbox = .init(needsYou: 1)
        #expect(HomeContext(snapshot: s, date: now).mood(.inbox) == .needsYou)
        #expect(HomeContext(snapshot: HomeSnapshot(), date: now).mood(.agents) == .offline) // not paired
    }

    // MARK: The second widgets (Today, To-dos, Usage)

    private static func todo(_ id: String, due: Date? = nil, done: Bool = false, high: Bool = false) -> TodoItem {
        var fields = ["\"id\":\"\(id)\"", "\"title\":\"\(id)\""]
        if let due { fields.append("\"due_at\":\"\(ISO8601DateFormatter().string(from: due))\"") }
        if done { fields.append("\"done_at\":\"2026-01-01T00:00:00Z\"") }
        if high { fields.append("\"priority\":\"high\"") }
        do { return try JSON.decoder.decode(TodoItem.self, from: Data("{\(fields.joined(separator: ","))}".utf8)) }
        catch { fatalError("bad fixture: \(error)") }
    }

    /// The To-dos widget's list is the To-dos page's Today filter, open ones only: overdue first, then by due, the
    /// high-priority ones before the rest; a tick on the widget rides along.
    @Test func todosAreTodaysOpenOnes() {
        let calendar = Calendar.current
        let now = calendar.date(bySettingHour: 13, minute: 0, second: 0, of: Date(timeIntervalSince1970: 1_800_000_000))!
        let todos = [
            Self.todo("later", due: now.addingTimeInterval(3 * 3600)),
            Self.todo("overdue", due: now.addingTimeInterval(-26 * 3600)),
            Self.todo("soon", due: now.addingTimeInterval(1800)),
            Self.todo("done", due: now.addingTimeInterval(1800), done: true),
            Self.todo("tomorrow", due: now.addingTimeInterval(30 * 3600)),
            Self.todo("undated"),
        ]
        let t = HomeTodos.build(todos, ticks: ["soon": .ticking], now: now, calendar: calendar)
        #expect(t.items.map(\.id) == ["overdue", "soon", "later"])
        #expect(t.count == 3 && t.overdue == 1)
        #expect(t.items[0].overdue && t.items[1].tick == .ticking)
    }

    /// A tick the hub does not take (here: no hub) is marked failed, so the widget says so and offers it again.
    @Test func aTickThatFailsIsShown() async {
        HomeWidgetActions.ticks = [:]
        await HomeWidgetActions.complete(todoID: "not-a-todo")
        #expect(HomeWidgetActions.ticks["not-a-todo"] == .failed)
        HomeWidgetActions.ticks = [:]
    }

    /// The phone's calendar first, the hub's meetings without it, and with neither the widget is told why.
    @Test func eventsComeFromTheCalendarThenTheHub() {
        let e = HomeSnapshot.Event(title: "Review", start: 1, end: 2)
        #expect(HomeToday.build(calendar: [e], hub: [], now: .now)?.source == .calendar)
        #expect(HomeToday.build(calendar: nil, hub: [e], now: .now)?.source == .hub)
        #expect(HomeToday.build(calendar: nil, hub: nil, now: .now)?.source == nil)
    }

    /// Usage keeps the Machines page's accounts and windows; with no account and no tokens there is nothing to show.
    @Test func usageKeepsTheWindows() throws {
        #expect(HomeUsage.build([], tokens: 0, tpm: 0, hourly: []) == nil)
        let json = """
        {"id":"a","provider":"claude","name":"Claude Max","windows":[{"label":"5h","window_minutes":300,"used_percent":86}],
         "stale":false,"local":true,"sources":[],"machines":[]}
        """
        let account = try JSON.decoder.decode(UsageAccount.self, from: Data(json.utf8))
        let u = try #require(HomeUsage.build([account], tokens: 1200, tpm: 30, hourly: [1, 2]))
        #expect(u.accounts.first?.windows.first?.percent == 86 && u.accounts.first?.windows.first?.minutes == 300)
        #expect(u.tokensToday == 1200 && u.hourly == [1, 2])
        // Money only where a gateway reports it: the subscription windows carry none.
        #expect(u.costToday == nil)
    }

    // MARK: Part three (Edit Widget, the Smart Stack)

    /// Edit Widget lists the snapshot's computers and accounts in its order; an empty choice is no filter.
    @Test func editWidgetListsTheSnapshot() {
        let s = HomeSamples.busy()
        #expect(HomeChoices.computers(s).map(\.id) == ["studio", "air", "hub", "mini"])
        #expect(HomeChoices.computers(s).first?.name == "Studio")
        #expect(HomeChoices.accounts(s).map(\.name) == ["Claude Max", "Codex Pro"])
        #expect(HomeChoices.computers(nil).isEmpty && HomeChoices.accounts(HomeSnapshot()).isEmpty)
        #expect(HomeChoices.filter("") == nil && HomeChoices.filter(nil) == nil && HomeChoices.filter("air") == "air")
    }

    /// Show and Computer narrow the Agents widget to their rows, and the counts to those rows.
    @Test func editWidgetNarrowsTheAgents() {
        let s = HomeSamples.busy()
        #expect(HomeAgentsShow.all.kind == nil && HomeAgentsShow.computers.kind == .coding)
        let persona = HomeContext(snapshot: s, date: HomeSamples.now, agentKind: HomeAgentsShow.persona.kind)
        #expect(persona.agentRows.map(\.id) == ["hub/s2", "hub/s4"])
        #expect(persona.agentCounts.needsYou == 1 && persona.agentCounts.working == 1 && persona.agentCounts.done == 0)
        let studio = HomeContext(snapshot: s, date: HomeSamples.now, machine: "studio")
        #expect(studio.agentCounts.needsYou == 1 && studio.agentCounts.working == 1 && studio.agentCounts.done == 1)
        #expect(HomeContext(snapshot: s, date: HomeSamples.now, machine: "air").machines.map(\.id) == ["air"])
        #expect(HomeContext(snapshot: s, date: HomeSamples.now, account: "a2").accounts.map(\.name) == ["Codex Pro"])
    }

    /// The Smart Stack: first when something needs you, then a running recording, then a meeting within 15 minutes;
    /// nothing while the numbers are old or Persona is not set up.
    @Test func smartStackRanksWhatNeedsYou() {
        let s = HomeSamples.busy()
        let now = HomeSamples.now
        func score(_ kind: HomeKind, _ snapshot: HomeSnapshot = s, at date: Date = now) -> Float {
            HomeRelevance.score(kind, HomeContext(snapshot: snapshot, date: date))
        }
        #expect(score(.agents) == 100 && score(.computers) == 100 && score(.inbox) == 100)
        #expect(score(.record, HomeSamples.recording()) == 90)
        #expect(score(.today) == 10) // the review starts in 40 minutes
        #expect(score(.today, at: now.addingTimeInterval(30 * 60)) == 70) // and now in 10
        #expect(score(.usage) == 30) // 86 % full: getting close
        #expect(score(.health) == 10)
        #expect(score(.agents, HomeSamples.stale()) == 0 && score(.agents, HomeSamples.unpaired) == 0)
    }

    // MARK: The gateways' money

    /// Usage's money is what the gateways say is today's: the `day` entries' cost, a shared key once. An entry known only
    /// by its label, a week's or a month's, and a gateway that could not be read add nothing; with none, no money.
    @Test func moneyIsWhatTheGatewaysSayIsToday() throws {
        func answer(_ json: String) throws -> LauncherQuotaAnswer { try JSON.decoder.decode(LauncherQuotaAnswer.self, from: Data(json.utf8)) }
        let keyed = try answer("""
        {"quota":{"gateway":"g1","key_name":"k1","usage":[
         {"label":"Today","period":"day","requests":3,"tokens":10,"cost_usd":1.25},
         {"label":"This month","period":"month","requests":9,"tokens":90,"cost_usd":30}]}}
        """)
        let other = try answer(#"{"quota":{"gateway":"g2","usage":[{"label":"Today","period":"day","requests":1,"tokens":1,"cost_usd":0.5}]}}"#)
        let unsaid = try answer(#"{"quota":{"gateway":"g3","usage":[{"label":"Today","requests":1,"tokens":1,"cost_usd":7}]}}"#)
        let unread = try answer(#"{"error":"no reader","unsupported":true}"#)
        #expect(HomeUsage.spentToday([keyed, keyed, other, unsaid, unread]) == 1.75)
        #expect(HomeUsage.spentToday([unsaid, unread]) == nil)
        #expect(HomeUsage.spentToday([]) == nil)
        #expect(HomeUsage.build([], tokens: 5, tpm: 0, hourly: [], spent: 1.75)?.costToday == 1.75)
    }

    /// The launchers asked are those with a gateway: not the built-in claude, a direct API connection or a hidden one.
    @Test func moneyIsAskedOfTheGatewayLaunchers() throws {
        let json = """
        [{"name":"gw","source":"persona","base_url":"https://gw.example.com"},{"name":"claude","source":"builtin"},
         {"name":"direct","source":"persona","kind":"api","base_url":"https://api.example.com"},
         {"name":"old","source":"zshrc","base_url":"https://old.example.com","hidden":true},{"name":"plain","source":"zshrc"},
         {"name":"rc","source":"zshrc","details":{"m1":{"base_url":"https://rc.example.com"}}}]
        """
        let all = try JSON.decoder.decode([LauncherView].self, from: Data(json.utf8))
        #expect(HomeUsage.gatewayLaunchers(all) == ["gw", "rc"])
    }
}

