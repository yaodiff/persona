import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor
struct AssistantTrayActivityTests {
    private func session(_ fields: [String: Any] = [:], id: String = "agent") throws -> Session {
        var json: [String: Any] = ["machine_id": "computer", "session_id": id, "cwd": "/demo", "state": "working"]
        json.merge(fields) { _, new in new }
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: json))
    }

    private let workflow: [String: Any] = [
        "task_id": "workflow-task", "run_id": "workflow-run", "name": "Review changes", "running": true,
        "phase": "Verify", "phases": ["Review", "Verify"], "phase_index": 2,
        "agents_done": 5, "agents_started": 6
    ]

    @Test func backgroundTasksKeepRunningWhileTheParentWaits() throws {
        let agent = try session(["background_working": true, "background": [
            ["id": "build", "kind": "shell"], ["id": "monitor", "kind": "monitor"]
        ]])
        let activity = AssistantTrayActivity(sessions: [agent])
        #expect(agent.waitsToBeWoken)
        #expect(activity.isActive)
        #expect(activity.backgroundTasks == 2)
        #expect(activity.workflowCount == 0)
        #expect(activity.subtitle == String(localized: "\(2) background tasks running"))
    }

    @Test func workflowUsesTheHeadersPhaseAndProgressWithoutDoubleCountingItsMarker() throws {
        let agent = try session(["background_working": true, "workflows": [workflow],
                                 "background": [["id": "workflow-task", "kind": "workflow", "run_id": "workflow-run"]]])
        let activity = AssistantTrayActivity(sessions: [agent])
        #expect(activity.isActive)
        #expect(activity.workflowCount == 1)
        #expect(activity.backgroundTasks == 0)
        #expect(activity.workflows.first?.phase == agent.leadWorkflow?.phaseLabel)
        #expect(activity.workflows.first?.counts == agent.leadWorkflow?.counts)
        #expect(activity.workflows.first?.counts == "5/6+")
        #expect(activity.subtitle.contains("Verify 2/2"))
    }

    @Test func compactWorkflowKeepsItsPhasePositionWithoutTheDeclaredPhaseArray() throws {
        var compact = workflow
        compact.removeValue(forKey: "phases")
        compact["phase_count"] = 2
        let agent = try session(["workflows": [compact]])
        #expect(agent.leadWorkflow?.phaseLabel == "Verify 2/2")
        #expect(AssistantTrayActivity(sessions: [agent]).workflows.first?.phase == "Verify 2/2")
    }

    @Test func subagentAndItsBackgroundMarkerAreOneRunningAgent() throws {
        let agent = try session(["subagents": [["id": "child", "session_id": "agent@child", "running": true]],
                                 "background": [["id": "child", "kind": "agent"]]])
        let activity = AssistantTrayActivity(sessions: [agent])
        #expect(activity.isActive)
        #expect(activity.subagents == 1)
        #expect(activity.backgroundTasks == 0)
    }

    @Test func distinctForegroundAndBackgroundSubagentsBothCount() throws {
        let agent = try session(["subagents": [["id": "foreground", "session_id": "agent@foreground", "running": true]],
                                 "background": [["id": "background", "kind": "agent"]]])
        #expect(AssistantTrayActivity(sessions: [agent]).subagents == 2)
    }

    @Test func endedSubagentMarkerDoesNotKeepTheSpinnerRunning() throws {
        let agent = try session(["subagents": [["id": "child", "session_id": "agent@child", "running": false]],
                                 "background": [["id": "child", "kind": "agent"]]])
        #expect(!AssistantTrayActivity(sessions: [agent]).isActive)
    }

    @Test func endedWorkDoesNotMakeARealWaitLookActive() throws {
        var ended = workflow
        ended["running"] = false
        ended["status"] = "completed"
        let agent = try session(["background_waiting": true, "workflows": [ended],
                                 "subagents": [["id": "child", "session_id": "agent@child", "running": false]],
                                 "background": [["id": "workflow-task", "kind": "workflow", "run_id": "workflow-run"]]])
        #expect(agent.waitsToBeWoken)
        #expect(!AssistantTrayActivity(sessions: [agent]).isActive)
    }

    @Test func aWakeUpWithoutBackgroundWorkKeepsTheClock() throws {
        let agent = try session(["background_waiting": true])
        #expect(agent.waitsToBeWoken)
        #expect(!AssistantTrayActivity(sessions: [agent]).isActive)
        #expect(AssistantTrayActivity(sessions: [agent]).subtitle.isEmpty)
    }

    @Test func boundedSessionSummariesStillShowRunningWork() throws {
        let agent = try session(["background_count": 3, "running_workflows": 1, "running_subagents": 2])
        let activity = AssistantTrayActivity(sessions: [agent])
        #expect(activity.isActive)
        #expect(activity.backgroundTasks == 2)
        #expect(activity.subagents == 2)
        #expect(activity.workflowCount == 1)
    }

    @Test func compactSummaryOverridesStaleDetailsAndZeroClearsActivity() throws {
        let old = try session(["background": [["id": "old-build", "kind": "shell"]], "workflows": [workflow],
                               "subagents": [["id": "old-child", "session_id": "agent@old-child", "running": true]]])
        var compact = workflow
        compact.removeValue(forKey: "phases")
        compact["phase_count"] = 2
        compact["agents_done"] = 6
        let summary = try session(["waiting": true, "background_tasks_count": 2, "running_workflows": 1,
                                   "running_subagents": 3, "workflow_progress": compact])
        let merged = summary.preservingDetails(from: old)
        #expect(merged.workflows?.first?.agentsDone == 5, "Chat details stay intact")
        let activity = AssistantTrayActivity(sessions: [merged])
        #expect(activity.backgroundTasks == 2)
        #expect(activity.subagents == 3)
        #expect(activity.workflows.first?.counts == "6/6+")
        #expect(activity.workflows.first?.phase == "Verify 2/2")
        let cleared = try session(["waiting": false, "background_tasks_count": 0]).preservingDetails(from: merged)
        #expect(cleared.workflows?.isEmpty == false, "Retained chat arrays must not resurrect tray work")
        #expect(!AssistantTrayActivity(sessions: [cleared]).isActive)
    }

    @Test func aFullDetailRefreshClearsSummaryHints() throws {
        let summary = try session(["waiting": true, "background_tasks_count": 2, "running_workflows": 1,
                                   "workflow_progress": workflow])
        let detail = try session(["background": []]).preservingDetails(from: summary)
        #expect(detail.listBackgroundTasksCount == nil)
        #expect(detail.listWorkflowProgress == nil)
        #expect(!AssistantTrayActivity(sessions: [detail]).isActive)
    }

    @Test func oddCompactWorkflowDoesNotDropTheSession() throws {
        let summary = try session(["waiting": true, "background_tasks_count": 0, "running_workflows": 1,
                                   "workflow_progress": ["phase": "Verify"]])
        #expect(summary.listWorkflowProgress == nil)
        let activity = AssistantTrayActivity(sessions: [summary])
        #expect(activity.isActive)
        #expect(activity.workflowCount == 1)
        #expect(activity.workflows.isEmpty)
    }

    @Test func olderFullResponseCanReportWorkWithoutGuessingItsKind() throws {
        let agent = try session(["background_working": true])
        let activity = AssistantTrayActivity(sessions: [agent])
        #expect(activity.isActive)
        #expect(activity.backgroundTasks == 0 && activity.subagents == 0 && activity.workflowCount == 0)
        #expect(activity.subtitle == String(localized: "Working (Background)"))
    }

    @Test func summaryZerosOverrideEvenTheOldBackgroundWorkingFlag() throws {
        let agent = try session(["waiting": false, "background_tasks_count": 0, "background_working": true])
        #expect(!AssistantTrayActivity(sessions: [agent]).isActive)
    }

    @Test func taskShowsTheNewestWorkflowAcrossItsAgents() throws {
        var earlier = workflow, newer = workflow
        earlier["started_at"] = "2026-11-10T08:00:00Z"
        newer["started_at"] = "2026-11-10T09:00:00Z"
        newer["phase"] = "Final verification"
        let sessions = [try session(["workflows": [earlier]], id: "old"), try session(["workflows": [newer]], id: "new")]
        #expect(AssistantTrayActivity(sessions: sessions).workflows.first?.phase == "Final verification 2/2")
    }

    @Test func everyActiveTaskAgentContributesButDuplicateSessionsDoNot() throws {
        let shell = try session(["background": [["id": "build", "kind": "shell"]]], id: "shell")
        let workflowAgent = try session(["workflows": [workflow]], id: "workflow")
        let activity = AssistantTrayActivity(sessions: [shell, workflowAgent, shell])
        #expect(activity.backgroundTasks == 1)
        #expect(activity.workflowCount == 1)
        #expect(activity.workflows.count == 1)
        #expect(activity.subtitle.contains("Verify 2/2"))
    }

    @Test func manualAgentSnapshotChangesWhenWorkflowProgressChanges() throws {
        let agent = try session(["workflows": [workflow], "background_working": true])
        let before = DockAgent.going([agent])
        var update = agent
        update.workflows?[0].agentsDone = 6
        let after = DockAgent.going([update])
        #expect(before != after)
        #expect(before.first?.activity.workflows.first?.counts == "5/6+")
        #expect(after.first?.activity.workflows.first?.counts == "6/6+")
        #expect(after.first?.backgroundWaiting == true)
    }
}
