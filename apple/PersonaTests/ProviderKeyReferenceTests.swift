import Foundation
import Testing
@testable import Persona
import PersonaWire

/// A model provider's key as a secret provider's reference (#3962 step 4c, docs/extensions/secret-providers.md
/// "Launcher keys"), against FakeHub: the wizard sends `api_key_reference` / `machine_key_references` in place of the key
/// when its field takes a reference, opens a stored reference with the switch on, and never sends a reference to a
/// connection test or a model list, which would hand its value to the address in the request.
@MainActor struct ProviderKeyReferenceTests {
    private static let ref = "op://Dev/Gateway/key"
    private static let features: Set<String> = [ProviderWizardModel.presetsFeature, "launcher_secret_references", ProviderScopeForm.feature]

    private func body(_ req: LocalRequest?) throws -> [String: JSONValue] {
        try #require(try JSON.decoder.decode(JSONValue.self, from: req?.body ?? Data()).object)
    }

    private func existing(reference: Bool) throws -> LauncherView {
        let json = #"""
        {"name":"jv_gw","source":"persona","kind":"launcher","base_url":"https://gateway.example.com","model":"glm-5","has_key":true,
         "auth":"bearer","models":[{"id":"glm-5","context_window":200000}]\#(reference ? #","key_reference":{"provider":"example-vault","provider_name":"Example Vault","reference":"op://Dev/Gateway/key","title":"Gateway › key"}"# : "")}
        """#
        return try JSON.decoder.decode(LauncherView.self, from: Data(json.utf8))
    }

    @Test func aStoredReferenceOpensWithTheSwitchOn() throws {
        let hub = FakeHub()
        let w = ProviderWizardModel(existing: try existing(reference: true), deps: .init(api: { hub.client }, features: { Self.features }))
        #expect(w.apiKeyByReference && w.keyReferences)
        #expect(w.existing?.keyReference?.providerName == "Example Vault")
        #expect(ExtensionText.referenceState(try #require(w.existing?.keyReference)).contains("Gateway › key"))
        #expect(!ProviderWizardModel(existing: try existing(reference: false), deps: .init(api: { hub.client })).apiKeyByReference)
    }

    @Test func aTypedReferenceIsSavedInPlaceOfTheKeyAndNeverProbed() async throws {
        let hub = FakeHub()
        hub.on("PUT", "/api/v1/launchers/jv_gw", json: #"{"ok":true}"#)
        hub.on("POST", "/api/v1/launchers/test", json: #"{"ok":true,"ms":10}"#)
        let w = ProviderWizardModel(existing: try existing(reference: true), deps: .init(api: { hub.client }, features: { Self.features }))
        // Nothing typed: the stored reference is probed by name, as a stored key is.
        #expect(!w.referencePending && w.canTest)
        w.apiKey = Self.ref + "2"
        #expect(w.referencePending && !w.canTest)
        await w.runTest()
        await w.discover()
        #expect(hub.sent("POST", "/api/v1/launchers/test").isEmpty && hub.sent("POST", "/api/v1/launchers/discover").isEmpty)
        let input = w.input
        #expect(input.apiKeyReference == Self.ref + "2" && input.apiKey == nil)
        // Switched off and a key typed: the key, which replaces the reference.
        w.apiKeyByReference = false
        w.apiKey = "sk-test-0123456789"
        #expect(w.input.apiKey == "sk-test-0123456789" && w.input.apiKeyReference == nil)
        let probe = try JSONSerialization.jsonObject(with: try JSONEncoder().encode(w.input)) as? [String: Any]
        #expect(probe?["api_key_reference"] == nil)
    }

    @Test func aPresetsReferenceSkipsTheKeyTestAndIsSaved() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/launchers/presets", json: #"""
        {"presets":[{"id":"builtin/glm","plugin":"builtin","name":"GLM","kind":"launcher","api":"messages",
                     "base_url":"https://open.example.com/api/anthropic","auth":"bearer","model":"glm-5"}]}
        """#)
        hub.on("POST", "/api/v1/launchers", json: #"{"ok":true,"name":"glm"}"#)
        let w = ProviderWizardModel(deps: .init(api: { hub.client }, features: { Self.features }))
        await w.loadPresets()
        w.presetID = "builtin/glm"
        w.presetKeyByReference = true
        w.presetKey = Self.ref
        #expect(!w.keyTestable && w.canSavePreset)
        // and the key test's own request has no key to send
        #expect(w.keyProbe == nil)
        let input = try #require(w.presetInput)
        #expect(input.apiKeyReference == Self.ref && input.apiKey == nil && input.preset == "builtin/glm")
        let sent = try JSONSerialization.jsonObject(with: try JSONEncoder().encode(input)) as? [String: Any]
        #expect(sent?["api_key_reference"] as? String == Self.ref && sent?["api_key"] == nil)
        // Another preset starts over, its key a key again.
        w.presetID = nil
        #expect(!w.presetKeyByReference && w.presetKey.isEmpty)
    }

    @Test func aReferenceWhoseProviderIsGoneShowsItself() throws {
        let gone = try JSON.decoder.decode(SecretReference.self, from: Data(#"{"provider":"","provider_name":"","reference":"op://Dev/Gateway/key"}"#.utf8))
        #expect(ExtensionText.referenceState(gone) == "op://Dev/Gateway/key")
        let named = try JSON.decoder.decode(SecretReference.self, from: Data(#"{"provider":"example-vault","provider_name":"","reference":"op://Dev/Gateway/key","title":"Gateway › key"}"#.utf8))
        #expect(ExtensionText.referenceState(named).contains("example-vault") && ExtensionText.referenceState(named).hasSuffix(" · Gateway › key"))
    }

    @Test func aComputersReferenceGoesInMachineKeyReferences() throws {
        let json = #"""
        {"name":"jv_gw","source":"persona","has_key":true,"share":"all","delivery":"on_demand","own_service":"skip","key_machines":["laptop"],
         "machine_key_references":{"laptop":{"provider":"example-vault","provider_name":"Example Vault","reference":"op://Dev/Laptop/key"}}}
        """#
        let view = try JSON.decoder.decode(LauncherView.self, from: Data(json.utf8))
        var scope = try #require(ProviderScopeForm(view: view))
        #expect(scope.keyRefs == ["laptop": true])
        let machines = [Machine(id: "laptop", name: "Laptop", kind: "ssh", online: true), Machine(id: "desk", name: "Desk", kind: "ssh", online: true)]
        scope.keys = ["laptop": "op://Dev/Laptop/key2", "desk": "sk-desk-0123"]
        let fields = scope.fields(old: view, machines: machines, kind: "launcher")
        #expect(fields.machineKeyReferences == ["laptop": "op://Dev/Laptop/key2"])
        #expect(fields.machineKeys == ["desk": "sk-desk-0123"])
    }
}
