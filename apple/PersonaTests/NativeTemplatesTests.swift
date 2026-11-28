import XCTest
import PersonaWire
@testable import Persona

@MainActor final class NativeTemplatesTests: XCTestCase {
    private let link = "https://hub.example/settings?tab=general&template=eyJwZXJzb25hX3RlbXBsYXRlIjoxLCJpZCI6ImZsb2NrLWNvbnNvbGUiLCJuYW1lIjp7ImVuIjoiQWdlbnQgY29uc29sZSIsInpoLUhhbnMiOiJBZ2VudCDmjqfliLblj7AifSwiYnJvd3NlciI6eyJwaG9uZV9uYXYiOiJ0YWJzIiwibWFpbl9jaGF0IjoicGxhaW4iLCJhZ2VudHNfdmlldyI6ImFjdGl2ZSIsInNldHRpbmdzX3ZpZXciOiJzaG9ydCIsInNpZGViYXJfdmlldyI6ImFnZW50cyJ9fQ"

    func testSharedLinkParsesAndAppliesOnThisDevice() throws {
        let template = try PersonaTemplate.parse(link)
        XCTAssertEqual(template.id, "example-console")
        XCTAssertEqual(template.browser?.mainChat, "plain")
        XCTAssertEqual(template.browser?.agentsView, "active")
        XCTAssertEqual(template.browser?.settingsView, "short")

        let defaults = UserDefaults(suiteName: #function)!
        defaults.removePersistentDomain(forName: #function)
        let store = NativeTemplateStore(defaults: defaults)
        let plan = store.plan(template)
        XCTAssertTrue(plan.contains { $0.id == "console" && $0.status == .change })
        XCTAssertTrue(store.apply(template))
        XCTAssertTrue(store.enabled)
        XCTAssertTrue(store.agentsConsole)
        XCTAssertTrue(store.plainMainChat)
        XCTAssertTrue(store.shortSettings)

        let relaunched = NativeTemplateStore(defaults: defaults)
        XCTAssertTrue(relaunched.enabled)
        XCTAssertTrue(relaunched.apply(.default))
        XCTAssertFalse(relaunched.enabled)
    }

    func testDefaultRestoresAndDoesNotNeedAnImport() {
        let defaults = UserDefaults(suiteName: #function)!
        defaults.removePersistentDomain(forName: #function)
        let store = NativeTemplateStore(defaults: defaults)
        store.enabled = true
        store.templateName = "Agent console"
        XCTAssertTrue(store.apply(.default))
        XCTAssertFalse(store.enabled)
        XCTAssertEqual(store.templateName, "")
    }

    func testInvalidTemplatesAreRefused() {
        for value in ["", "{bad", "https://hub.example/settings", "bm90IGEgdGVtcGxhdGU"] {
            XCTAssertThrowsError(try PersonaTemplate.parse(value), value)
        }
        XCTAssertThrowsError(try PersonaTemplate.parse(#"{"persona_template":2,"id":"x","name":"X"}"#))
        XCTAssertThrowsError(try PersonaTemplate.parse(#"{"persona_template":1,"id":"x","name":"X","browser":{"main_chat":"lean"}}"#))
    }

    func testDeepLinksOnlyClaimTemplateParameters() throws {
        XCTAssertEqual(NativeTemplateLink.parameter(URL(string: link)!),
                       URLComponents(string: link)?.queryItems?.first { $0.name == "template" }?.value)
        XCTAssertNil(NativeTemplateLink.parameter(URL(string: "https://hub.example/agents?template=abc")!))
        XCTAssertEqual(NativeTemplateLink.parameter(URL(string: "persona://template?template=default")!), "default")
        XCTAssertNil(NativeTemplateLink.parameter(URL(string: "https://hub.example/settings")!))
    }
}

final class AgentConsoleProjectionTests: XCTestCase {
    func testRunningTerminalAgentsOnly() throws {
        let data = try JSONSerialization.data(withJSONObject: [
            "machine_id": "m", "session_id": "running", "title": "Agent", "cwd": "/tmp",
            "status": "working", "state": "working", "pane_id": "w1:p1"
        ])
        var running = try JSON.decoder.decode(Session.self, from: data)
        running.slotID = "w1:t1"
        running.slotName = "tab-one"
        var stopped = running
        stopped.sessionID = "stopped"
        stopped.state = .offline
        stopped.status = .offline
        var desktop = running
        desktop.sessionID = "desktop"
        desktop.paneID = nil
        var offlineComputer = running
        offlineComputer.sessionID = "offline-computer"
        offlineComputer.machineID = "off"

        let machine = Machine(id: "m", name: "Computer", kind: "ssh", online: true)
        let off = Machine(id: "off", name: "Offline", kind: "ssh", online: false)
        var settings = AgentListProjection.Settings(grouping: "machine")
        settings.runningTerminalOnly = true
        let input = AgentListProjection.Input(sessions: [running, stopped, desktop, offlineComputer], machines: [machine, off], settings: settings)
        let prepared = AgentListProjection.preparedSessions(input)
        XCTAssertEqual(prepared.map(\.sessionID), ["running"])
        XCTAssertEqual(running.herdrTabName, "tab-one")
    }
}
