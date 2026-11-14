import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Installs, upgrades and removals in steps (docs/protocol/protocol.md「Extension install, upgrade and uninstall progress」, #3307): what the hub
/// pushes decodes (unknown states and steps kept), a hub with `extension_operations` previews in the background, waits
/// for consent and goes on with the same operation, an older hub's request becomes a local operation with no steps, an
/// older push does not undo a newer one, a preview turned down is canceled, and Retry goes back to the hub when it can
/// and starts over when it cannot.
@MainActor
struct ExtensionOperationsTests {
    private static func op(_ state: String, step: String = "start", id: String = "op1", updated: String = "2026-11-06T08:00:05Z",
                           retryable: Bool = false, previewID: String? = nil) -> String {
        let startState = state == "succeeded" ? "done" : state == "failed" ? "failed" : "pending"
        let consent = state == "needs_consent" ? "running" : state == "running" && step == "download" ? "pending" : "done"
        return #"""
        {"id":"\#(id)","kind":"install","extension":"example-calendar","name":"Example 日历","version":"0.9.0","market":"m1",
         "actor":"iPhone","state":"\#(state)","step":"\#(step)","retryable":\#(retryable),
         "error":\#(state == "failed" ? #""扩展启动后没有起来""# : "null"),"error_code":\#(state == "failed" ? #""start_failed""# : "null"),
         "preview_id":\#(previewID.map { "\"\($0)\"" } ?? "null"),
         "steps":[{"id":"download","state":"\#(step == "download" ? "running" : "done")","bytes":1048576,"total":4194304},
                  {"id":"consent","state":"\#(consent)"},
                  {"id":"start","state":"\#(startState)"},
                  {"id":"nodes","state":"skipped"}],
         "started_at":"2026-11-06T08:00:00Z","updated_at":"\#(updated)"}
        """#
    }

    private static let catalog = #"""
    {"markets":[{"id":"m1","spec":"https://plugins.example.com/","name":"Example","has_token":false}],
     "plugins":[{"market":"m1","id":"example-calendar","name":"Example 日历","version":"0.9.0"}]}
    """#
    private static let preview = #"""
    {"preview_id":"pv1","manifest":{"id":"example-calendar","name":"Example 日历","version":"0.9.0"},"permissions":[],
     "permissions_digest":"d1","expires_at":"2099-01-01T00:00:00Z","market":"m1"}
    """#

    private func store(_ hub: FakeHub, stepped: Bool, defaultID: @escaping () -> String? = { nil },
                       patience: TimeInterval = 15 * 60) -> ExtensionOperations {
        ExtensionOperations(deps: .init(api: { hub.client }, features: { stepped ? ["extension_operations"] : [] }, hub: { "fake" },
                                        defaultID: defaultID, poll: .milliseconds(10), patience: patience))
    }

    private func decode(_ json: String) throws -> ExtensionOperation { try JSON.decoder.decode(ExtensionOperation.self, from: Data(json.utf8)) }
    private func pv() throws -> ExtensionPreview { try JSON.decoder.decode(ExtensionPreview.self, from: Data(Self.preview.utf8)) }

    @Test func thePushDecodes() throws {
        let event = #"{"type":"extension_operation","operation":"# + Self.op("running", step: "download") + "}"
        guard case .extensionOperation(let op, _)? = ServerEvent.decode(Data(event.utf8)) else {
            Issue.record("not an extension_operation")
            return
        }
        #expect(op.extensionID == "example-calendar" && op.kind == .install && op.isRunning && op.actor == "iPhone")
        #expect(op.steps.count == 4 && op.shownSteps.count == 3, "a skipped step is not shown")
        #expect(op.steps[0].fraction == 0.25 && op.current?.id == "download")
        #expect(ExtensionOperationText.progress(op) == "第 1 步，共 3 步" && ExtensionOperationText.title(op) == "正在安装")
        #expect(ExtensionOperationText.detail(op.steps[0])?.contains("/") == true)
        // Old names, a state and a step this app does not know.
        let odd = try decode(#"{"id":"x","plugin":"p","state":"paused","steps":[{"id":"teleport","state":"queued"}]}"#)
        #expect(odd.extensionID == "p" && odd.state == .running && odd.steps[0].state == .running)
        #expect(ExtensionOperationText.step("teleport", kind: .install) == "teleport")
        #expect(ExtensionOperationStep(id: "download", state: .running, bytes: 1).fraction == nil, "no total: a bar that cannot say how far")
        let nodes = try JSON.decoder.decode(ExtensionOperationStep.self, from: Data(#"{"id":"nodes","state":"running","machines":{"done":1,"total":2}}"#.utf8))
        #expect(ExtensionOperationText.detail(nodes) == "1 / 2 台电脑")
        let cached = try JSON.decoder.decode(ExtensionOperationStep.self, from: Data(#"{"id":"download","state":"done","cached":true}"#.utf8))
        #expect(ExtensionOperationText.detail(cached) == "用了 hub 上缓存的包")
    }

    @Test func anotherManagersOperationIsKeptApart() throws {
        // Each manager numbers its own operations and has its own copy of an extension (docs/assistant/managers.md
        // "Clients"): the push says whose, and the market pages show only the default manager's.
        let event = #"{"type":"extension_operation","manager":"m2","operation":"# + Self.op("running") + "}"
        guard case .extensionOperation(var theirs, let manager)? = ServerEvent.decode(Data(event.utf8)) else {
            Issue.record("not an extension_operation")
            return
        }
        #expect(manager == "m2")
        theirs.manager = manager
        let ops = store(FakeHub(), stepped: true)
        ops.received(theirs)
        #expect(ops.operation(for: "example-calendar") == nil, "another manager's install is not the default's")
        #expect(ops.operation(for: "example-calendar", manager: "m2")?.state == .running)
        ops.received(try decode(Self.op("succeeded")))
        #expect(ops.operations.count == 2, "the same id from the default manager is another operation")
        #expect(ops.operation(for: "example-calendar")?.state == .succeeded)
        #expect(ops.operation(for: "example-calendar", manager: "m2")?.state == .running)
    }

    @Test func anExplicitDefaultPageFollowsAnUnscopedOperationPush() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/-/operations", json: #"{"operations":["# + Self.op("running") + "]}")
        hub.on("POST", "/api/v1/plugins/-/operations/op1/retry",
               json: #"{"operation":"# + Self.op("running", id: "op2", updated: "2026-11-06T08:00:07Z") + "}")
        hub.on("GET", "/api/v1/plugins/-/operations/op2", json: Self.op("succeeded", id: "op2", updated: "2026-11-06T08:00:08Z"))
        let ops = store(hub, stepped: true)

        await ops.reload(manager: "assistant")
        #expect(hub.requests.last?.headers[APIClient.managerHeader] == "assistant", "the page keeps its intentional route")
        #expect(ops.operation(for: "example-calendar", manager: "assistant")?.state == .running)
        #expect(ops.operation(for: "example-calendar")?.state == .running)
        #expect(ops.operations.count == 1, "the explicit default id is stored under the default key")

        ops.received(try decode(Self.op("failed", updated: "2026-11-06T08:00:06Z", retryable: true)))
        #expect(ops.operation(for: "example-calendar", manager: "assistant")?.state == .failed,
                "a nil-tagged push updates the explicitly routed default page")
        #expect(ops.operation(for: "example-calendar")?.state == .failed)
        #expect(ops.operations.count == 1)

        var other = try decode(Self.op("running", updated: "2026-11-06T08:00:07Z"))
        other.manager = "work"
        ops.received(other)
        #expect(ops.operation(for: "example-calendar", manager: "work")?.state == .running)
        #expect(ops.operation(for: "example-calendar", manager: "assistant")?.state == .failed,
                "a non-default assistant stays separate")
        #expect(ops.operations.count == 2)

        let failed = try #require(ops.operation(for: "example-calendar", manager: "assistant"))
        let end = try await ops.retry(failed)
        #expect(end.state == .succeeded)
        #expect(hub.sent("POST", "/api/v1/plugins/-/operations/op1/retry").first?.headers[APIClient.managerHeader] == "assistant",
                "canonical storage does not remove Retry's request header")
        #expect(hub.sent("GET", "/api/v1/plugins/-/operations/op2").first?.headers[APIClient.managerHeader] == "assistant",
                "Follow keeps the explicitly routed default assistant too")
        #expect(ops.operation(for: "example-calendar")?.id == "op2")
        #expect(ops.operation(for: "example-calendar", manager: "work")?.state == .running)
    }

    @Test func aMovedDefaultPageFollowsItsCanonicalNilPush() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/-/operations", json: #"{"operations":["# + Self.op("running") + "]}")
        let ops = store(hub, stepped: true, defaultID: { "m-2" })

        await ops.reload(manager: "m-2")
        #expect(hub.requests.last?.headers[APIClient.managerHeader] == "m-2", "the page keeps its explicit route")
        #expect(ops.operation(for: "example-calendar", manager: "m-2")?.state == .running)
        #expect(ops.operation(for: "example-calendar")?.state == .running)
        #expect(ops.operations.count == 1)

        // AppModel has already canonicalized this m-2 push to nil before the store receives it.
        var push = try decode(Self.op("succeeded", updated: "2026-11-06T08:00:06Z"))
        push.manager = ManagerRoster.pushed(by: "m-2", defaultID: "m-2")
        #expect(push.manager == nil)
        ops.received(push)
        #expect(ops.operation(for: "example-calendar", manager: "m-2")?.state == .succeeded)
        #expect(ops.operation(for: "example-calendar")?.state == .succeeded)
        #expect(ops.operations.count == 1, "the canonical nil push updates the explicitly reloaded default")
    }

    @Test func aDefaultChangeDuringFollowStillObservesTheTerminalState() async throws {
        let hub = FakeHub()
        var defaultID: String? = "assistant"
        hub.on("POST", "/api/v1/plugins/-/catalog/preview", json: #"{"operation":"# + Self.op("running") + "}")
        hub.on("GET", "/api/v1/plugins/-/operations/op1") { _ in
            defaultID = "work"
            return Data(Self.op("succeeded", updated: "2026-11-06T08:00:06Z").utf8)
        }
        let ops = store(hub, stepped: true, defaultID: { defaultID }, patience: 1)

        let started = try await ops.startPreview(market: "m1", extension: "example-calendar", manager: "assistant")
        let end = await ops.settled(started)

        #expect(end.state == .succeeded)
        #expect(end.manager == "assistant", "completion keeps the request's original manager")
        #expect(ops.operation(for: "example-calendar", manager: "assistant")?.state == .succeeded)
        let reads = hub.sent("GET", "/api/v1/plugins/-/operations/op1")
        #expect(reads.count == 1, "the terminal reread completes without another polling burst")
        #expect(reads.first?.headers[APIClient.managerHeader] == "assistant")
    }

    @Test func aSteppedInstallPreviewsWaitsForConsentAndGoesOn() async throws {
        let hub = FakeHub()
        var previewBody: JSONValue?
        var installBody: JSONValue?
        hub.on("GET", "/api/v1/plugins/-/catalog", json: Self.catalog)
        hub.on("POST", "/api/v1/plugins/-/catalog/preview") { req in
            previewBody = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            return Data((#"{"operation":"# + Self.op("running", step: "download") + "}").utf8)
        }
        hub.on("GET", "/api/v1/plugins/-/operations/op1") { _ in
            let waiting = Self.op("needs_consent", step: "consent", updated: "2026-11-06T08:00:06Z", previewID: "pv1")
            return Data((String(waiting.dropLast()) + #","preview":"# + Self.preview + "}").utf8)
        }
        hub.on("POST", "/api/v1/plugins/-/install") { req in
            installBody = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            hub.on("GET", "/api/v1/plugins/-/operations/op1", json: Self.op("succeeded", updated: "2026-11-06T08:00:09Z"))
            return Data((#"{"operation":"# + Self.op("running", step: "install", updated: "2026-11-06T08:00:07Z") + "}").utf8)
        }
        let ops = store(hub, stepped: true)
        let page = MarketExtensionModel(market: "m1", id: "example-calendar", deps: .init(api: { hub.client }), operations: ops)
        await page.load()
        await page.prepare()
        #expect(previewBody == .object(["market": .string("m1"), "extension": .string("example-calendar"),
                                        "plugin": .string("example-calendar"), "async": .bool(true)]))
        let shown = try #require(page.preview, "the preview opens once the operation waits for consent")
        #expect(page.operation?.state == .needsConsent)
        let consent = page.takeConsent()
        #expect(consent == "op1")
        page.declined() // the sheet closing after Install does not cancel it
        #expect(await page.confirm(shown, consent: consent))
        #expect(installBody == .object(["preview_id": .string("pv1"), "permissions_digest": .string("d1"), "async": .bool(true)]))
        #expect(hub.sent("DELETE", "/api/v1/plugins/-/operations/op1").isEmpty)
        #expect(page.operation == nil, "one that went through is not shown")
    }

    @Test func aPreviewTurnedDownIsCanceled() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/-/catalog", json: Self.catalog)
        hub.on("POST", "/api/v1/plugins/-/catalog/preview", json: #"{"operation":"# + Self.op("running", step: "download") + "}")
        hub.on("GET", "/api/v1/plugins/-/operations/op1") { _ in
            let waiting = Self.op("needs_consent", step: "consent", updated: "2026-11-06T08:00:06Z", previewID: "pv1")
            return Data((String(waiting.dropLast()) + #","preview":"# + Self.preview + "}").utf8)
        }
        hub.on("DELETE", "/api/v1/plugins/-/operations/op1") { _ in Data() }
        let page = MarketExtensionModel(market: "m1", id: "example-calendar", deps: .init(api: { hub.client }),
                                        operations: store(hub, stepped: true))
        await page.prepare()
        #expect(page.preview != nil)
        page.preview = nil
        page.declined()
        for _ in 0..<50 where hub.sent("DELETE", "/api/v1/plugins/-/operations/op1").isEmpty { try await Task.sleep(for: .milliseconds(10)) }
        #expect(hub.sent("DELETE", "/api/v1/plugins/-/operations/op1").count == 1)
    }

    @Test func anOlderHubGetsALocalOperation() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/plugins/-/install") { _ in throw APIError(status: 400, message: "预览过期了，重新预览一次") }
        let ops = store(hub, stepped: false)
        let end = await ops.installWithoutSteps("example-calendar", name: "Example 日历", preview: try pv())
        #expect(end.state == .failed && end.isLocal && end.steps.isEmpty && end.error == "预览过期了，重新预览一次")
        #expect(ops.operation(for: "example-calendar")?.id == end.id)
        let body = try #require(hub.sent("POST", "/api/v1/plugins/-/install").first?.body)
        #expect(try JSON.decoder.decode(JSONValue.self, from: body) == .object(["preview_id": .string("pv1"), "permissions_digest": .string("d1")]),
                "an older hub is not asked for async")
        await ops.dismiss(end.id)
        #expect(ops.operation(for: "example-calendar") == nil && hub.sent("DELETE", "/api/v1/plugins/-/operations/\(end.id)").isEmpty)
    }

    @Test func anOlderPushDoesNotUndoANewerOne() throws {
        let ops = store(FakeHub(), stepped: true)
        let newer = try decode(Self.op("failed", updated: "2026-11-06T08:00:09Z", retryable: true))
        ops.received(newer)
        ops.received(try decode(Self.op("running", updated: "2026-11-06T08:00:05Z")))
        #expect(ops.operation(for: "example-calendar")?.state == .failed)
        #expect(ExtensionOperationText.chip(newer) == "安装失败" && newer.failedStep?.id == "start")
        #expect(ops.operation(for: "example-calendar", market: "other") == nil, "another market's extension of the same id")
    }

    @Test func aPageOpenedLaterSeesTheOperation() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/-/operations", json: #"{"operations":["# + Self.op("running", step: "download") + "]}")
        let ops = store(hub, stepped: true)
        await ops.reload()
        #expect(ops.operation(for: "example-calendar")?.isRunning == true)
        // A hub without operations is not asked.
        let old = FakeHub()
        await store(old, stepped: false).reload()
        #expect(old.requests.isEmpty)
    }

    /// A page of another assistant's extension reads that assistant's operations, asking it by name, and they replace
    /// its own alone: its install waiting for consent is still there when the page opens again (no push comes for it),
    /// and other assistants' operations (a failed removal's, the default's) are left as they were.
    @Test func aPageReadsItsOwnAssistantsOperations() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/-/operations", json: #"{"operations":["# + Self.op("needs_consent", step: "consent") + "]}")
        let ops = store(hub, stepped: true)
        // "work"'s install waiting for consent, as starting it from that assistant's market leaves it; "home"'s failed
        // removal; the default's download.
        var waiting = try decode(Self.op("needs_consent", step: "consent"))
        waiting.manager = "work"
        ops.received(waiting)
        var failed = try decode(Self.op("failed", id: "op5"))
        failed.manager = "home"
        ops.received(failed)
        ops.received(try decode(Self.op("running", step: "download", id: "op9")))

        await ops.reload(manager: "work")
        #expect(hub.requests.map { $0.headers[APIClient.managerHeader] } == ["work"])
        #expect(ops.operation(for: "example-calendar", manager: "work")?.state == .needsConsent, "its install waiting for consent stays")
        #expect(ops.operation(for: "example-calendar", manager: "home")?.id == "op5", "another assistant's stays")
        #expect(ops.operation(for: "example-calendar")?.id == "op9", "the default's stays")
        await ops.reload(manager: "work")
        #expect(ops.operation(for: "example-calendar", manager: "work")?.state == .needsConsent, "read again, still there")
        #expect(ops.operation(for: "example-calendar", manager: "home")?.state == .failed)

        // The default's list (the connection back) replaces the default's alone.
        await ops.reload()
        #expect(hub.requests.last?.headers[APIClient.managerHeader] == nil)
        #expect(ops.operation(for: "example-calendar")?.id == "op1")
        #expect(ops.operation(for: "example-calendar", manager: "work") != nil && ops.operation(for: "example-calendar", manager: "home") != nil)
    }

    /// The market's page reads its assistant's operations the same way.
    @Test func theMarketPageReadsItsAssistantsOperations() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/-/operations", json: #"{"operations":["# + Self.op("needs_consent", step: "consent") + "]}")
        let ops = store(hub, stepped: true)
        ops.received(try decode(Self.op("running", step: "download", id: "op9")))
        let page = MarketExtensionModel(market: "m1", id: "example-calendar", deps: .init(api: { hub.client.showing("work") }), operations: ops)
        await page.loadOperations()
        #expect(hub.requests.map { $0.headers[APIClient.managerHeader] } == ["work"])
        #expect(page.operation?.state == .needsConsent && page.operation?.manager == "work")
        #expect(ops.operation(for: "example-calendar")?.id == "op9", "the default's stays")
    }

    @Test func retryGoesBackToTheHubWhenItCan() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/-/catalog", json: Self.catalog)
        hub.on("POST", "/api/v1/plugins/-/operations/op1/retry", json: #"{"operation":"# + Self.op("running", step: "download", id: "op2", updated: "2026-11-06T08:00:10Z") + "}")
        hub.on("GET", "/api/v1/plugins/-/operations/op2", json: Self.op("succeeded", id: "op2", updated: "2026-11-06T08:00:12Z"))
        let ops = store(hub, stepped: true)
        ops.received(try decode(Self.op("failed", retryable: true)))
        let page = MarketExtensionModel(market: "m1", id: "example-calendar", deps: .init(api: { hub.client }), operations: ops)
        #expect(page.operation?.state == .failed && !page.working)
        #expect(await page.retry())
        #expect(hub.sent("POST", "/api/v1/plugins/-/operations/op1/retry").count == 1)
        #expect(page.operation == nil, "the failed one is replaced, the new one went through")
    }

    @Test func retryStartsOverWhenTheHubCannot() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/-/catalog", json: Self.catalog)
        hub.on("POST", "/api/v1/plugins/-/catalog/preview", json: Self.preview)
        let ops = store(hub, stepped: false)
        ops.received(try decode(Self.op("failed")))
        let page = MarketExtensionModel(market: "m1", id: "example-calendar", deps: .init(api: { hub.client }), operations: ops)
        #expect(await page.retry() == false)
        #expect(page.preview?.previewID == "pv1", "previewed again, for the permissions to agree to")
        #expect(page.operation == nil && hub.sent("POST", "/api/v1/plugins/-/operations/op1/retry").isEmpty)
    }
}
