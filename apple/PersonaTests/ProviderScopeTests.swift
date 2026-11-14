import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A provider's Computers page (#2333, docs/integrations/launchers.md "Device scope and on-demand delivery"): what the hub's
/// scope fields decode to (and an older hub's lack of them), what a POST / PUT carries (only what differs from the defaults
/// or from what was stored), per-computer keys and their removal, the summaries and the list's harness groups.
@MainActor
struct ProviderScopeTests {
    private static let machines = [
        Machine(id: "agentbox", name: "agentbox", kind: "ssh", online: true),
        Machine(id: "studio", name: "Studio", kind: "ssh", online: false),
        { var m = Machine(id: "newbie", name: "Newbie", kind: "ssh", online: true); m.trust = "pending"; return m }(),
        { var m = Machine(id: "office", name: "Office", kind: "ssh", online: true); m.trust = "work"; return m }()
    ]

    private func body(_ req: LocalRequest?) throws -> [String: JSONValue] {
        try #require(try JSON.decoder.decode(JSONValue.self, from: req?.body ?? Data()).object)
    }

    private func wizard(_ hub: FakeHub, existing: LauncherView? = nil, scoped: Bool = true) -> ProviderWizardModel {
        ProviderWizardModel(existing: existing, deps: .init(api: { hub.client }, features: { scoped ? [ProviderScopeForm.feature] : [] },
                                                            machines: { Self.machines }))
    }

    private static func stored(_ scope: String = "") throws -> LauncherView {
        let json = #"""
        {"name":"jv_glm","source":"persona","kind":"launcher","base_url":"https://gateway.example.com","model":"glm-5","has_key":true,
         "auth":"bearer","models":[{"id":"glm-5"}]\#(scope)}
        """#
        return try JSON.decoder.decode(LauncherView.self, from: Data(json.utf8))
    }

    private static let scopeKeys = ["share", "scope_machines", "delivery", "own_service", "machine_keys"]

    @Test func theScopeDecodesAndAnOlderHubHasNone() throws {
        let l = try Self.stored(#","share":"selected","scope_machines":["agentbox","studio"],"delivery":"push","own_service":"use","key_machines":["agentbox"],"agent":"codex""#)
        #expect(l.share == "selected" && l.scopeMachines == ["agentbox", "studio"] && l.delivery == "push" && l.ownService == "use")
        #expect(l.keyMachines == ["agentbox"] && l.legacyScope == nil && l.agent == "codex")
        let f = try #require(ProviderScopeForm(view: l))
        #expect(f.share == .selected && f.machines == ["agentbox", "studio"] && f.work.isEmpty && f.delivery == .push && f.ownService == .use)

        let old = try Self.stored()
        #expect(old.share == nil && old.keyMachines == nil && ProviderScopeForm(view: old) == nil)
        // An older hub's provider: no Computers row, and nothing of the scope goes with a save.
        let w = wizard(FakeHub(), existing: old, scoped: false)
        #expect(!w.scopeShown && w.scopeProblem == nil)
        let sent = try JSON.decoder.decode(JSONValue.self, from: JSON.encoder.encode(w.input)).object ?? [:]
        #expect(Self.scopeKeys.allSatisfy { sent[$0] == nil })
        // A new provider on an older hub: none either.
        #expect(!wizard(FakeHub(), scoped: false).scopeShown && wizard(FakeHub()).scopeShown)
    }

    @Test func aNewProviderSendsOnlyWhatDiffersFromTheDefaults() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/launchers", json: #"{"ok":true}"#)
        let w = wizard(hub)
        w.kind = .custom
        w.name = "jv_new"
        w.baseURL = "https://gateway.example.com"
        w.apiKey = "sk-test-scope"
        w.toggle(ProviderModel(id: "glm-5"))
        #expect(w.canSave)
        #expect(await w.save())
        var sent = try body(hub.sent("POST", "/api/v1/launchers").last)
        #expect(Self.scopeKeys.allSatisfy { sent[$0] == nil }, "defaults sent: \(sent)")

        // Selected Computers with none ticked: the hub would refuse it.
        w.scope.share = .selected
        #expect(w.scopeProblem != nil && !w.canSave)
        w.scope.toggle("agentbox")
        w.scope.delivery = .push
        w.scope.ownService = .skip // the default for a launcher without assign: still not sent
        #expect(w.canSave)
        #expect(await w.save())
        sent = try body(hub.sent("POST", "/api/v1/launchers").last)
        #expect(sent["share"]?.string == "selected" && sent["scope_machines"]?.array?.compactMap(\.string) == ["agentbox"])
        #expect(sent["delivery"]?.string == "push" && sent["own_service"] == nil)

        w.scope.ownService = .use
        #expect(await w.save())
        #expect(try body(hub.sent("POST", "/api/v1/launchers").last)["own_service"]?.string == "use")
    }

    @Test func aChangeSendsOnlyTheFieldsThatChanged() async throws {
        let hub = FakeHub()
        var bodies: [[String: JSONValue]] = []
        hub.on("PUT", "/api/v1/launchers/jv_glm") { req in
            bodies.append(try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data()).object ?? [:])
            return Data(#"{"ok":true}"#.utf8)
        }
        let e = try Self.stored(#","share":"selected","scope_machines":["agentbox","studio"],"delivery":"push","own_service":"skip","key_machines":["agentbox"]"#)
        let w = wizard(hub, existing: e)
        #expect(w.scopeShown && w.canSave)

        // Untouched, or the same computers in another order: nothing of the scope.
        w.scope.machines = ["studio", "agentbox"]
        #expect(await w.save())
        #expect(Self.scopeKeys.allSatisfy { bodies[0][$0] == nil }, "\(bodies[0])")

        // On demand now: that field alone.
        w.scope.delivery = .onDemand
        #expect(await w.save())
        #expect(bodies[1]["delivery"]?.string == "on_demand" && bodies[1]["share"] == nil && bodies[1]["scope_machines"] == nil)

        // One computer fewer: the list, not the share.
        w.scope.delivery = .push
        w.scope.toggle("studio")
        #expect(await w.save())
        #expect(bodies[2]["scope_machines"]?.array?.compactMap(\.string) == ["agentbox"] && bodies[2]["share"] == nil && bodies[2]["delivery"] == nil)

        // Another share: the share and its (empty) list go together.
        w.scope.share = .none
        #expect(await w.save())
        #expect(bodies[3]["share"]?.string == "none" && bodies[3]["scope_machines"]?.array?.isEmpty == true)
    }

    @Test func perComputerKeysAreSentAndRemoved() async throws {
        let hub = FakeHub()
        var bodies: [[String: JSONValue]] = []
        hub.on("PUT", "/api/v1/launchers/jv_glm") { req in
            bodies.append(try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data()).object ?? [:])
            return Data(#"{"ok":true}"#.utf8)
        }
        let e = try Self.stored(#","share":"selected","scope_machines":["agentbox"],"delivery":"on_demand","own_service":"skip","key_machines":["studio"]"#)
        let w = wizard(hub, existing: e)
        // The computers in scope, then the one that still has a key of its own.
        #expect(w.scope.keyRows(Self.machines, stored: e.keyMachines ?? []) == ["agentbox", "studio"])

        w.scope.keys = ["agentbox": " sk-box ", "office": "sk-not-in-scope", "studio": "  "]
        w.scope.remove = ["studio"]
        #expect(await w.save())
        let keys = try #require(bodies[0]["machine_keys"]?.object)
        #expect(keys == ["agentbox": .string("sk-box"), "studio": .string("")], "\(keys)")

        // Undo: nothing about keys at all.
        w.scope.keys = [:]
        w.scope.remove = []
        #expect(await w.save())
        #expect(bodies[1]["machine_keys"] == nil)
    }

    @Test func allComputersOffersTheWorkOnesAndKeysForTheRest() {
        var f = ProviderScopeForm()
        #expect(ProviderScopeForm.addableWork(Self.machines, added: []) == ["office"])
        // Pending computers are not offered, unless already chosen.
        #expect(ProviderScopeForm.choosable(Self.machines, chosen: []) == ["agentbox", "studio", "office"])
        #expect(ProviderScopeForm.choosable(Self.machines, chosen: ["newbie", "gone"]) == ["agentbox", "studio", "newbie", "office", "gone"])
        #expect(f.keyMachines(Self.machines) == ["agentbox", "studio"])
        f.toggle("office", work: true)
        #expect(f.scopeMachines == ["office"] && f.keyMachines(Self.machines) == ["agentbox", "studio", "office"])
        f.share = .none
        #expect(f.scopeMachines.isEmpty && f.keyMachines(Self.machines).isEmpty)
        // The POST of a new one: the work computer goes with all, which is the default and is left out.
        f.share = .all
        let fields = f.fields(old: nil, machines: Self.machines, kind: "launcher")
        #expect(fields.share == nil && fields.scopeMachines == ["office"] && fields.delivery == nil && fields.ownService == nil)
    }

    @Test func assignAndDirectAPIDefaultToUse() {
        var f = ProviderScopeForm()
        #expect(f.ownService(assign: true, kind: "launcher") == .use && f.ownService(assign: false, kind: "api") == .use)
        #expect(f.ownService(assign: false, kind: "launcher") == .skip)
        f.ownService = .use
        #expect(f.fields(old: nil, machines: [], assign: true, kind: "launcher").ownService == nil)
        f.ownService = .skip
        #expect(f.fields(old: nil, machines: [], assign: true, kind: "launcher").ownService == "skip")
        #expect(ProviderScopeForm.ownServiceDetected(kind: "launcher", agent: nil) && ProviderScopeForm.ownServiceDetected(kind: "launcher", agent: "codex"))
        #expect(!ProviderScopeForm.ownServiceDetected(kind: "api", agent: nil) && !ProviderScopeForm.ownServiceDetected(kind: "launcher", agent: "omp"))
    }

    @Test func aLegacyProviderStaysAsItWasUntilChanged() async throws {
        let hub = FakeHub()
        var bodies: [[String: JSONValue]] = []
        hub.on("PUT", "/api/v1/launchers/jv_glm") { req in
            bodies.append(try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data()).object ?? [:])
            return Data(#"{"ok":true}"#.utf8)
        }
        let e = try Self.stored(#","share":"all","delivery":"push","own_service":"use","legacy_scope":true"#)
        let w = wizard(hub, existing: e)
        #expect(await w.save())
        #expect(Self.scopeKeys.allSatisfy { bodies[0][$0] == nil })
        let explicit = try Self.stored(#","share":"all","delivery":"push","own_service":"use""#)
        #expect(ProviderScopeForm.summary(e, name: { $0 }) != ProviderScopeForm.summary(explicit, name: { $0 }))
        let selected = try Self.stored(#","share":"selected","scope_machines":["agentbox"],"delivery":"on_demand","own_service":"skip""#)
        let only = ProviderScopeForm.summary(selected, name: { _ in "agentbox-name" })
        #expect(only.contains("agentbox-name") && only.components(separatedBy: " · ").count == 3)
        // Hub only says nothing of delivery or sign-in.
        let hubOnly = try Self.stored(#","share":"none","delivery":"push""#)
        #expect(!ProviderScopeForm.summary(hubOnly, name: { $0 }).contains(" · "))
    }

    @Test func thePresetsFlowCarriesTheScope() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/launchers/presets", json: #"""
        {"presets":[{"id":"builtin/glm","plugin":"builtin","name":"GLM","kind":"launcher","base_url":"https://gateway.example.com","model":"glm-5"}]}
        """#)
        let w = ProviderWizardModel(deps: .init(api: { hub.client }, features: { [ProviderWizardModel.presetsFeature, ProviderScopeForm.feature] },
                                                machines: { Self.machines }))
        await w.loadPresets()
        w.presetID = "builtin/glm"
        w.presetKey = "sk-test-preset"
        w.assignChoice = true
        #expect(w.scopeShown && w.canSavePreset)
        var input = try #require(w.presetInput)
        #expect(input.share == nil && input.ownService == nil && input.machineKeys == nil)

        // Assigned right away: use is the hub's default, so only skip is sent.
        w.scope.share = .selected
        #expect(!w.canSavePreset)
        w.scope.toggle("agentbox")
        w.scope.ownService = .skip
        input = try #require(w.presetInput)
        #expect(input.share == "selected" && input.scopeMachines == ["agentbox"] && input.ownService == "skip")
        let sent = try JSON.decoder.decode(JSONValue.self, from: JSON.encoder.encode(input)).object ?? [:]
        #expect(sent["scope_machines"]?.array?.count == 1 && sent["own_service"]?.string == "skip" && sent["assign"] != nil)
    }

    @Test func theListGroupsByHarness() throws {
        func l(_ name: String, _ extra: String) throws -> LauncherView {
            try JSON.decoder.decode(LauncherView.self, from: Data(#"{"name":"\#(name)","source":"persona"\#(extra)}"#.utf8))
        }
        let list = try [l("api1", #","kind":"api""#), l("cx", #","kind":"launcher","agent":"codex""#), l("cc1", #","kind":"launcher""#),
                        l("odd", #","kind":"launcher","agent":"other""#), l("cc2", #","kind":"launcher","agent":"claude""#)]
        let groups = ProviderScopeForm.harnessGroups(list)
        #expect(groups.map(\.harness) == ["claude", "codex", "api", "other"])
        #expect(groups[0].items.map(\.name) == ["cc1", "cc2"] && groups[0].title == "Claude Code")
        let plain = try [l("a", ""), l("b", "")]
        #expect(ProviderScopeForm.harnessGroups(plain).count == 1)
    }
}
