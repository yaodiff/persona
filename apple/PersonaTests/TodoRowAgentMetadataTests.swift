import Foundation
import SwiftUI
import Testing
import UIKit
@testable import Persona
import PersonaWire

/// The In progress row names its lead Agent (#10904): computer, friendly model and any effort, then "+N" for the other
/// unreleased Agents. These are real TodoRows on a window, read through the same accessibility tree as the UI tests.
@Suite(.serialized) @MainActor
struct TodoRowAgentMetadataTests {
    private let mac = Machine(id: "todo-row-mac", name: "MacBook", kind: "ssh", online: true)
    private let studio = Machine(id: "todo-row-studio", name: "Studio", kind: "ssh", online: true)

    @Test func oneAgentNamesComputerModelAndEffortWithoutTheRawModelID() async throws {
        let lead = try session("lead", state: .working, model: "claude-opus-5-5", effort: "xhigh",
                               summary: "Existing progress summary")
        let todo = self.todo(agents: [agent("lead")])
        let effort = try #require(SessionModes.effortLabel("xhigh"))

        let words = try await rowWords(todo, sessions: [lead])

        #expect(words.contains("MacBook · Opus 5.5 · \(effort)"), "actual TodoRow: \(words)")
        #expect(!words.contains("claude-opus-5-5"), "the raw model id must not be shown: \(words)")
        #expect(words.contains(todo.statusLabel(.working)), "the existing status stays: \(words)")
        #expect(words.contains("Existing progress summary"), "the existing summary stays: \(words)")
    }

    @Test func oneWorkingAgentWinsAndReleasedAgentsDoNotCount() async throws {
        let idle = try session("idle", model: "claude-sonnet-5-5", effort: "low", updated: 300)
        let working = try session("working", machine: studio.id, state: .working,
                                  model: "claude-opus-5-5", effort: "max", updated: 200)
        let asking = try session("asking", state: .needsYou, model: "claude-haiku-5-5", updated: 400)
        let released = try session("released", state: .working, model: "claude-fable-5-2", updated: 500)
        let todo = self.todo(agents: [agent("idle"), agent("working", machine: studio.id), agent("asking"),
                                     agent("released", released: true)])
        let effort = try #require(SessionModes.effortLabel("max"))

        let words = try await rowWords(todo, sessions: [idle, working, asking, released])

        #expect(words.contains("Studio · Opus 5.5 · \(effort)"),
                "the working Agent leads even though others moved later: \(words)")
        #expect(words.contains("+2"), "the two other unreleased Agents: \(words)")
        #expect(!words.contains("Fable 5.2"), "a released Agent is not the lead: \(words)")
    }

    @Test func severalWorkingAgentsChooseTheMostRecentlyMoved() async throws {
        let older = try session("older", state: .working, model: "claude-sonnet-5-5", updated: 500, stateSince: 100)
        let newer = try session("newer", state: .working, model: "claude-opus-5-5", updated: 200, stateSince: 600)
        let todo = self.todo(agents: [agent("older"), agent("newer")])

        let words = try await rowWords(todo, sessions: [older, newer])

        #expect(words.contains("MacBook · Opus 5.5"),
                "max(updatedAt, stateSince) picks the working Agent that moved last: \(words)")
        #expect(words.contains("+1"), "the other working Agent: \(words)")
    }

    @Test func noWorkingAgentChoosesTheMostRecentlyMovedUsingEitherDate() async throws {
        let stateSinceLead = try session("state-since", state: .unread, model: "claude-opus-5-5",
                                         updated: 200, stateSince: 600)
        let olderUpdate = try session("older-update", model: "claude-sonnet-5-5", updated: 500, stateSince: 100)
        let updatedAtLead = try session("updated-at", model: "claude-opus-5-5", updated: 700, stateSince: 200)
        let olderStateSince = try session("older-state-since", state: .unread, model: "claude-sonnet-5-5",
                                          updated: 100, stateSince: 600)

        for (name, first, second) in [
            ("stateSince decides", olderUpdate, stateSinceLead),
            ("updatedAt decides", olderStateSince, updatedAtLead),
        ] {
            let todo = self.todo(id: "metadata-none-working-\(name)", agents: [agent(first.sessionID), agent(second.sessionID)])
            let words = try await rowWords(todo, sessions: [first, second])
            #expect(words.contains("MacBook · Opus 5.5"), "\(name): \(words)")
            #expect(words.contains("+1"), "\(name), the other Agent: \(words)")
        }
    }

    @Test func noEffortAddsNoEffortComponent() async throws {
        let lead = try session("codex", agent: "codex", state: .working, model: "gpt-5.5")
        let words = try await rowWords(self.todo(agents: [agent("codex")]), sessions: [lead])

        #expect(words.contains("MacBook · GPT-5.5"), "actual TodoRow: \(words)")
        #expect(!words.contains("MacBook · GPT-5.5 ·"), "an absent effort adds no separator: \(words)")
    }

    @Test func anUnknownModelIDUsesItsFriendlyShortForm() async throws {
        let lead = try session("unknown-model", state: .working, model: "acme/orbit-7")
        let words = try await rowWords(self.todo(agents: [agent("unknown-model")]), sessions: [lead])

        #expect(words.contains("MacBook · orbit-7"), "actual TodoRow: \(words)")
        #expect(!words.contains("acme/orbit-7"), "the raw vendor-qualified model id must not be shown: \(words)")
    }

    @Test func aPartiallyKnownTaskNamesItsKnownLeadAndCountsTheOtherAgent() async throws {
        let known = try session("known", state: .working, model: "claude-opus-5-5")
        // A deleted row is absent from the authoritative iOS session list, just like a row not read yet.
        let todo = self.todo(agents: [agent("known"), agent("deleted")])
        let words = try await rowWords(todo, sessions: [known])

        #expect(words.contains("MacBook · Opus 5.5"), "one known row is enough to name the lead: \(words)")
        #expect(words.contains("+1"), "the other unreleased Agent still counts: \(words)")
    }

    @Test func noKnownLiveRowsKeepsTheLocalizedAgentCount() async throws {
        let released = try session("released", state: .working, model: "claude-opus-5-5")
        let todo = self.todo(agents: [agent("missing"), agent("deleted"), agent("released", released: true)])
        let activeCount = todo.agents.filter { $0.releasedAt == nil }.count
        let fallback = String(localized: "· \(activeCount) agents")

        let words = try await rowWords(todo, sessions: [released])

        #expect(words.contains(fallback), "missing and deleted rows retain today's fallback: \(words)")
        #expect(!words.contains("MacBook · Opus 5.5"), "a released row cannot replace the fallback: \(words)")
        #expect(!words.contains("+1"), "the fallback has no separate other-Agent count: \(words)")
    }

    @Test func heldDeliveryStaysBeforeAgentMetadata() async throws {
        let lead = try session("lead", state: .working, model: "claude-opus-5-5", effort: "high")
        var item = self.todo(agents: [agent("lead")])
        item.prURL = "https://example.com/pull/7"

        let words = try await rowWords(item, sessions: [lead],
                                       triage: TodoTriage(section: .pr, rule: .prOpen, last: Date(timeIntervalSince1970: 200)))

        let delivery = try #require(words.range(of: "PR #7"))
        let agent = try #require(words.range(of: "MacBook · Opus 5.5"))
        #expect(delivery.lowerBound < agent.lowerBound, "delivery must precede Agent metadata: \(words)")
    }

    private func agent(_ id: String, machine: String? = nil, released: Bool = false) -> TodoItem.Agent {
        TodoItem.Agent(machineID: machine ?? mac.id, sessionID: id,
                       releasedAt: released ? Date(timeIntervalSince1970: 1_000) : nil)
    }

    private func todo(id: String = "metadata-task", agents: [TodoItem.Agent]) -> TodoItem {
        TodoItem(id: id, title: "Ship the synthetic task", notes: "Existing to-do notes",
                 createdAt: Date(timeIntervalSince1970: 100), updatedAt: Date(timeIntervalSince1970: 200), agents: agents)
    }

    private func session(_ id: String, machine: String? = nil, agent: String = "claude", state: AgentState = .idle,
                         model: String? = nil, effort: String? = nil, updated: TimeInterval = 100,
                         stateSince: TimeInterval? = nil, summary: String? = nil) throws -> Session {
        let machine = machine ?? mac.id
        let json = #"{"machine_id":"\#(machine)","session_id":"\#(id)","title":"Synthetic Agent \#(id)","cwd":"/tmp","status":"idle","state":"idle","agent":"\#(agent)"}"#
        var row = try JSON.decoder.decode(Session.self, from: Data(json.utf8))
        row.state = state
        row.model = model
        row.effort = effort
        row.updatedAt = Date(timeIntervalSince1970: updated)
        row.stateSince = stateSince.map { Date(timeIntervalSince1970: $0) }
        row.lastText = summary
        return row
    }

    /// Host the production row unchanged. Its lookup currently reads AppModel.shared, so install only these synthetic
    /// machine/session rows and restore what the test host held before returning.
    private func rowWords(_ todo: TodoItem, sessions: [Session], triage: TodoTriage? = nil) async throws -> String {
        let model = AppModel.shared
        let previousMachines = model.machines(inFront: false).filter { $0.group.isEmpty }
        let fixtureMachines = [mac, studio]
        let fixtureIDs = Set(fixtureMachines.map(\.id))
        let empty = Dictionary(uniqueKeysWithValues: fixtureMachines.map { (MachineRef(group: "", id: $0.id), [Session]()) })
        model.applySessions(empty)
        model.handle(.machines(previousMachines.filter { !fixtureIDs.contains($0.id) } + fixtureMachines), group: "")
        model.applySessions(Dictionary(grouping: sessions, by: \.machineRef))
        defer {
            model.applySessions(empty)
            model.handle(.machines(previousMachines), group: "")
        }

        try automation(true)
        defer { try? automation(false) }
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        defer {
            window.isHidden = true
            window.rootViewController = nil
        }
        window.frame = CGRect(x: 0, y: 0, width: 390, height: 300)
        window.windowLevel = .alert + 1
        let triage = triage ?? TodoTriage(section: .working, rule: .agentRunning, last: Date(timeIntervalSince1970: 200))
        let row = TodoRow(todo: todo, triage: triage, onToggleDone: {})
            .environment(model)
            .environment(\.tabIsActive, true)
        window.rootViewController = UIHostingController(rootView: row)
        window.makeKeyAndVisible()
        window.layoutIfNeeded()

        var labels: [String] = []
        for _ in 0..<50 {
            labels = RuntimeCLI.allElements().filter { element in
                RuntimeCLI.view(of: element.object)?.isDescendant(of: window) == true
            }.flatMap { [$0.label, $0.value] }.filter { !$0.isEmpty }
            if labels.contains(where: { $0.contains(todo.title) }) { break }
            try await Task.sleep(for: .milliseconds(20))
        }
        let words = labels.joined(separator: " ")
        #expect(words.contains(todo.title), "the hosted TodoRow did not render: \(words)")
        return words
    }

    private func automation(_ on: Bool) throws {
        let handle = try #require(dlopen("/usr/lib/libAccessibility.dylib", RTLD_NOW))
        let set = try #require(dlsym(handle, "_AXSSetAutomationEnabled"))
        typealias Enable = @convention(c) (Int32) -> Void
        unsafeBitCast(set, to: Enable.self)(on ? 1 : 0)
    }
}
