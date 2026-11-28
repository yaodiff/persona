import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Demo Mode's made-up hub (DemoHub, Resources/Demo.json): every page's request decodes into the app's models, so a
/// model that changes shape breaks here instead of in front of App Review. DemoModeTests walks the screens.
@MainActor struct DemoHubTests {
    private let api = APIClient(config: .demo)

    init() {
        DemoHub.install()
        DemoHub.shared.reset()
    }

    @Test func everyPageDecodes() async throws {
        let machines = try await api.machines()
        #expect(machines.count == 5)
        #expect(machines.contains { !$0.online }, "an offline machine")
        let sessions = try await api.sessions()
        #expect(sessions.count > 15)
        for state in [AgentState.needsYou, .working, .unread, .idle] {
            #expect(sessions.contains { $0.state == state && $0.assistant != true }, "no \(state) agent")
        }
        #expect(sessions.contains { $0.agent == "codex" })
        #expect(try await api.interactions().count == 2)
        for s in sessions {
            let page = try await api.items(s.key, beforeSeq: nil)
            #expect(!page.items.isEmpty, "\(s.title ?? s.sessionID) opens empty")
        }
        _ = try await api.sessionInfo(sessions[0].key)
        #expect(try await api.launcherList(machine: "macbook").launchers.count == 2)
        _ = try await api.dirs(machine: "macbook", path: "")

        #expect(try await api.metricsOverview().count == 5)
        let day = try await api.machineMetrics("server", range: .day)
        #expect(day.points.count > 300)
        #expect(day.latest != nil)
        let studio = try await api.machineMetrics("studio", range: .day)
        #expect(studio.points.contains { $0.gap == true }, "the Mac Studio's two hours away")
        // Token usage (#4973): the cards carry it, the machine's page has every range and filter.
        #expect(try await api.metricsOverview().contains { $0.usage?.today.total ?? 0 > 0 })
        for r in UsageRange.allCases {
            let u = try await api.machineUsage("server", range: r)
            #expect(u.total.total > 0 && u.models.count == 3 && u.launchers.count == 2, "\(r)")
        }
        let codex = try await api.machineUsage("server", range: .day, launcher: "codex")
        #expect(codex.models.map(\.name) == ["gpt-5.5"] && codex.total.cacheWrite == 0)

        let a = try await api.assistant()
        #expect(a.conversations.count >= 4)
        let tasks = try await api.scheduledTasks()
        #expect(Set(tasks.map(\.repeats)) == [.once, .daily, .interval], "every kind of task has a demo")
        #expect(tasks.contains { $0.permissionMode != nil }, "and one runs with its own permission mode")
        var patrol = try #require(tasks.first { $0.repeats == .interval })
        patrol.enabled = false
        patrol.effort = nil
        let saved = try await api.saveTask(patrol)
        #expect(!saved.enabled && saved.effort == nil && saved.nextRun == nil)
        #expect(!(try await api.scheduleRuns()).isEmpty)
        #expect(!(try await api.assistantMemory()).isEmpty)
        #expect(!(try await api.todos()).isEmpty, "task samples are bundled, not just scheduled routines")
        let projects = try await api.kbList(.projects)
        let project = try #require(projects.items.first)
        let found = try await api.kbFind(project.name)
        #expect(found.contains { $0.entry.id == project.id })
        #expect(!(try await api.kbSearch(project.name)).isEmpty)
        _ = try await api.notifyPrefs(device: "x")
        _ = try await api.pins()
        _ = try await api.deviceTokens()

        let inbox = try await api.inbox()
        #expect(inbox.count >= 6)
        for item in inbox {
            if let c = item.larkConversation { #expect(!(try await api.larkMessages(c.id)).messages.isEmpty) }
            if let m = item.mailSummary {
                _ = try await api.mailMessage(account: m.accountID, uid: m.uid, mailbox: m.mailbox ?? "INBOX")
            }
        }
        let conversations = try await api.larkConversations()
        #expect(conversations.conversations.count >= 2)
        for c in conversations.conversations {
            #expect(!(try await api.larkMessages(c.id)).messages.isEmpty)
        }
        _ = try await api.mailAccounts()
    }

    @Test func healthIntakeLoadsRecordsAndUndoesWithoutResurrectingARetry() async throws {
        let before = try await api.healthIntake()
        #expect(before.water.today == 750)
        #expect(before.protein.today == 36)
        let draft = HealthIntake.Draft(kind: .water)
        let logged = try await api.recordHealthIntake(draft)
        #expect(logged.today.water == 1000)
        #expect(logged.entry?.amount == 250)
        let repeated = try await api.recordHealthIntake(draft)
        #expect(repeated.today.water == 1000)
        #expect(repeated.entry?.id == draft.id)
        let undone = try await api.undoHealthIntake(draft.id)
        #expect(undone.today.water == 750)
        #expect(try await api.undoHealthIntake(draft.id).today.water == 750)
        let tombstone = try await api.recordHealthIntake(draft)
        #expect(tombstone.entry?.deletedAt != nil)
        #expect(tombstone.today.water == 750)
        let final = try await api.healthIntake()
        #expect(final.water.entries.count == before.water.entries.count)
        #expect(final.days.last?.water == final.water.today)
    }

    @Test func intakeDetailsOpenBothKindsAndSettingsFromTheBundledPages() async throws {
        for kind in [HealthIntake.Kind.water, .protein] {
            let first = try await api.pluginUI(HealthIntakeUI.id, HealthIntakeUI.page, context: .here().forPage(),
                                               state: HealthIntakeUI.state(kind))
            let view = try #require(first.view)
            #expect(view.title?.isEmpty == false)
            #expect(!view.sections.isEmpty)
            let settings = try await api.pluginUI(HealthIntakeUI.id, HealthIntakeUI.page, context: .here().forPage(),
                                                  state: view.state, action: "settings")
            let state = try JSON.decoder.decode(JSONValue.self, from: Data(#require(settings.view?.state).utf8))
            #expect(state["kind"]?.string == kind.rawValue)
            #expect(state["edit"]?.bool == true)
            let action = try await api.pluginUI(HealthIntakeUI.id, HealthIntakeUI.page, context: .here().forPage(),
                                                state: view.state, action: "log", values: ["amount": "10"])
            #expect(action.view == view)
            #expect(action.toast?.isEmpty == false)
        }
    }

    /// Times are moved to now: the demo never looks days old.
    @Test func timesAreCurrent() async throws {
        let sessions = try await api.sessions()
        let newest = sessions.compactMap(\.updatedAt).max()
        #expect(newest.map { abs($0.timeIntervalSinceNow) < 120 } == true)
    }

    /// What would reach a computer is refused with "Nothing is actually sent in demo mode" (shown as a note, not an error).
    @Test func sideEffectsAreRefused() async throws {
        let k = try #require(try await api.sessions().first { $0.assistant != true }).key
        do {
            try await api.interrupt(k)
            Issue.record("interrupt went through")
        } catch let e as APIError {
            #expect(e.code == DemoText.code)
            #expect(e.localizedDescription == DemoText.notSent)
        }
        await #expect(throws: APIError.self) {
            _ = try await api.start(.init(machineID: "macbook", cwd: "/Users/me/code/weather", launcher: "claude"))
        }
    }

    /// The demo accepts the new sparse permission write, but does not turn other or mixed assistant writes into false successes.
    @Test func assistantPermissionWritesDoNotSwallowOtherSettings() async throws {
        var permission = APIClient.AssistantConfigBody()
        permission.defaultPermissionMode = "bypassPermissions"
        let saved = try await api.setAssistant(permission)
        #expect(saved.defaultPermissionMode == "bypassPermissions")

        var other = APIClient.AssistantConfigBody()
        other.startRequestedTasks = false
        await #expect(throws: APIError.self) { _ = try await api.setAssistant(other) }

        var mixed = APIClient.AssistantConfigBody()
        mixed.defaultPermissionMode = "acceptEdits"
        mixed.startRequestedTasks = false
        await #expect(throws: APIError.self) { _ = try await api.setAssistant(mixed) }
        #expect(try await api.assistant().defaultPermissionMode == "bypassPermissions", "a refused mixed write changes nothing")
    }

    /// A message to an agent is echoed, and the demo answers it.
    @Test func aMessageGetsTheDemosAnswer() async throws {
        let k = try #require(try await api.sessions().first { $0.assistant != true }).key
        let before = try await api.items(k, beforeSeq: nil).items.count
        try await api.send(k, text: "看一下进度", clientMsgID: UUID().uuidString)
        let mine = try await api.items(k, beforeSeq: nil).items
        #expect(mine.count == before + 1)
        #expect(mine.last?.text == "看一下进度")
        try await Task.sleep(for: .seconds(2))
        #expect(try await api.items(k, beforeSeq: nil).items.last?.text == DemoHub.reply)
    }
}
