import Foundation
import Testing
@testable import Persona

/// Assistant Profile › Agent Follow-Up (#7005): the hub's values shown, one sparse PUT per change naming the assistant
/// that answered, the hub's answer kept and a refusal put back; the menus' words and choices. Made-up values.
@MainActor
struct AgentFollowUpTests {
    private static let state = #"""
    {"machine_id":"box","launcher":"claude","cwd":"/a","conversations":[],"manager":"assistant",
     "start_requested_tasks":true,"task_workspace_preference":"prefer-existing","main_supervises_own_tasks":false,
     "agent_tasks":false,"idle_close_hours":12,"archive_closed_days":3,
     "agent_auto_naming":true,"agent_auto_naming_max":1,"agent_auto_naming_interval_hours":0}
    """#
    private static let old = #"{"machine_id":"box","launcher":"claude","cwd":"/a","conversations":[]}"#

    private final class Seen {
        var toasts: [Toast] = []
        var saved: [APIClient.AssistantState] = []
    }

    private func page(_ hub: FakeHub, shown: String? = nil, held: APIClient.AssistantState? = nil, seen: Seen = Seen()) -> AgentFollowUpModel {
        AgentFollowUpModel(deps: .init(api: { hub.client.showing(shown) }, held: { held },
                                       saved: { seen.saved.append($0) }, toast: { seen.toasts.append($0) }))
    }

    private static func object(_ data: Data?) -> [String: Any] {
        data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] } ?? [:]
    }

    /// A value as it reads back from JSON (a Bool comes back a number), for comparing with what was decoded.
    private static func norm(_ value: Any?) -> String {
        guard let value else { return "nil" }
        return "\(object(try? JSONSerialization.data(withJSONObject: ["v": value]))["v"] ?? "nil")"
    }

    /// The state as the hub's JSON, to compare one field by its wire name.
    private static func wire(_ s: APIClient.AssistantState?) -> [String: Any] {
        object(s.flatMap { try? JSONEncoder().encode($0) })
    }

    @Test func theHubsValuesAreReadAndAnOlderHubOffersNone() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: Self.state)
        let page = page(hub)
        await page.load()
        let s = try #require(page.state)
        #expect(s.startRequestedTasks == true && s.taskWorkspacePreference == "prefer-existing" && s.agentTasks == false)
        #expect(s.idleCloseHours == 12 && s.archiveClosedDays == 3 && s.agentAutoNaming == true && s.agentAutoNamingMax == 1)
        #expect(s.agentAutoNamingIntervalHours == 0 && s.manager == "assistant" && page.namesShown)
        #expect(AgentFollowUpModel.offered(s))

        let older = try JSON.decoder.decode(APIClient.AssistantState.self, from: Data(Self.old.utf8))
        #expect(!AgentFollowUpModel.offered(older), "no row on the profile for a hub without them")
    }

    @Test func whatTheAppHoldsShowsAtOnce() async throws {
        let held = try JSON.decoder.decode(APIClient.AssistantState.self, from: Data(Self.state.utf8))
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant") { _ in throw APIError(status: 503, message: "Unavailable") }
        let seen = Seen()
        let page = page(hub, held: held, seen: seen)
        await page.load()
        #expect(page.state?.idleCloseHours == 12 && page.error == nil, "the app's values, though the page's own read failed")
        #expect(seen.toasts.map(\.style) == [.error])
    }

    @Test func eachChangeSendsOnlyItsFieldNamingTheAssistantThatAnswered() async throws {
        let cases: [(AgentFollowUpModel.Change, String, Any)] = [
            (.startRequested(false), "start_requested_tasks", false), (.workspace("prefer-new"), "task_workspace_preference", "prefer-new"),
            (.mainSupervises(true), "main_supervises_own_tasks", true), (.agentTasks(true), "agent_tasks", true),
            (.idleHours(0), "idle_close_hours", 0), (.archiveDays(30), "archive_closed_days", 30),
            (.naming(false), "agent_auto_naming", false), (.namingMax(1000), "agent_auto_naming_max", 1000),
            (.namingInterval(24), "agent_auto_naming_interval_hours", 24),
        ]
        for (change, key, value) in cases {
            let hub = FakeHub()
            hub.on("GET", "/api/v1/assistant", json: Self.state)
            hub.on("PUT", "/api/v1/assistant") { req in
                var echoed = Self.object(Data(Self.state.utf8))
                for (k, v) in Self.object(req.body) { echoed[k] = v }
                return try JSONSerialization.data(withJSONObject: echoed)
            }
            let seen = Seen()
            let page = page(hub, seen: seen)
            await page.load()
            await page.set(change)
            let sent = hub.sent("PUT", "/api/v1/assistant")
            let b = Self.object(sent.first?.body)
            #expect(sent.count == 1 && b.count == 1 && b.keys.first == key, "\(key): \(b)")
            #expect(Self.norm(b[key]) == Self.norm(value), "\(key)")
            // The default's manager, named: the hub takes the session-name settings only so.
            #expect(sent.first?.headers[APIClient.managerHeader] == "assistant", "\(key)")
            #expect(Self.norm(Self.wire(page.state)[key]) == Self.norm(value), "\(key) kept")
            #expect(seen.saved.count == 1 && seen.toasts.isEmpty, "\(key)")
        }
    }

    @Test func aRefusedChangePutsTheHubsValueBack() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: Self.state)
        hub.on("PUT", "/api/v1/assistant") { _ in throw APIError(status: 400, message: "idle_close_hours must be 0–8760") }
        let seen = Seen()
        let page = page(hub, seen: seen)
        await page.load()
        await page.set(.idleHours(9000))
        #expect(page.state?.idleCloseHours == 12)
        #expect(seen.toasts.map(\.style) == [.error] && seen.saved.isEmpty)
        #expect(page.saving.isEmpty)
    }

    @Test func theHubsAnswerWinsOverWhatWasAsked() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: Self.state)
        // A hub that keeps another value than the one asked for (it clamps, or another device changed it meanwhile).
        hub.on("PUT", "/api/v1/assistant", json: Self.state.replacingOccurrences(of: #""archive_closed_days":3"#, with: #""archive_closed_days":7"#))
        let page = page(hub)
        await page.load()
        await page.set(.archiveDays(30))
        #expect(page.state?.archiveClosedDays == 7)
    }

    @Test func anotherAssistantsPageNamesThatOne() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant", json: Self.state.replacingOccurrences(of: #""manager":"assistant""#, with: #""manager":"work""#))
        hub.on("PUT", "/api/v1/assistant", json: Self.state)
        let page = page(hub, shown: "work")
        await page.load()
        await page.set(.agentTasks(true))
        #expect(hub.requests.map { $0.headers[APIClient.managerHeader] } == ["work", "work"])
    }

    @Test func sessionNamesNeedAllThreeSettingsAndTheManager() async throws {
        for json in [Self.state.replacingOccurrences(of: #""manager":"assistant","#, with: ""),
                     Self.state.replacingOccurrences(of: #","agent_auto_naming_interval_hours":0"#, with: "")] {
            let hub = FakeHub()
            hub.on("GET", "/api/v1/assistant", json: json)
            let page = page(hub)
            await page.load()
            #expect(page.state != nil && !page.namesShown)
        }
    }

    @Test func theMenusSayTheirValuesPlainly() {
        let never = "never"
        #expect(AgentFollowUpText.hours(0, never: never) == never)
        #expect(AgentFollowUpText.hours(12, never: never) == Duration.seconds(12 * 3600).formatted(.units(allowed: [.hours], width: .wide)))
        #expect(AgentFollowUpText.hours(48, never: never) == Duration.seconds(48 * 3600).formatted(.units(allowed: [.days], width: .wide)))
        #expect(AgentFollowUpText.hours(168, never: never) == Duration.seconds(168 * 3600).formatted(.units(allowed: [.weeks], width: .wide)))
        #expect(AgentFollowUpText.days(0, never: never) == never)
        #expect(AgentFollowUpText.names(1000) == String(localized: "Unlimited [Agent Follow-Up]"))
        #expect(AgentFollowUpText.names(5) == 5.formatted())
        // A value set elsewhere is a choice of its own, in order; a preset is not listed twice.
        #expect(AgentFollowUpText.choices([0, 12, 24], current: 36) == [0, 12, 24, 36])
        #expect(AgentFollowUpText.choices([0, 12, 24], current: 12) == [0, 12, 24])
        #expect(AgentFollowUpText.namePresets == [1, 3, 5, 10, 1000])
    }
}
