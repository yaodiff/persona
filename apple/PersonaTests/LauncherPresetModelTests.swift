import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Add Provider › From Extension (LauncherPresetModel) and the quota's gateway name: what the hub sends decodes, a launcher
/// added from a preset sends only the name, the key, the preset and a model picked other than the default, and an
/// older hub without presets or without gateway_name still works.
@MainActor
struct LauncherPresetModelTests {
    private static let presets = #"""
    {"presets":[
      {"id":"example-gateway/team","plugin":"example-gateway","name":"示例网关 · 团队","description":"团队共用","kind":"launcher",
       "base_url":"https://gateway.example.com","model":"example-pro","small_model":"example-mini",
       "models":[{"id":"example-mini","name":"Example Mini"}],"key_hint":"以 eg- 开头"},
      {"id":"example-gateway/api","plugin":"example-gateway","name":"示例 API","kind":"api","api":"responses",
       "base_url":"https://api.example.com/v1","models":[{"id":"m1"},{"id":"m2"}],"models_error":"超时"}]}
    """#

    private func make(_ hub: FakeHub) -> LauncherPresetModel { LauncherPresetModel(deps: .init(api: { hub.client })) }

    @Test func presetsDecodeAndTheFirstIsPicked() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/launchers/presets", json: Self.presets)
        let m = make(hub)
        await m.load()
        #expect(m.error == nil && m.presets.count == 2)
        let team = try #require(m.selected)
        #expect(team.id == "example-gateway/team" && team.keyHint == "以 eg- 开头" && team.smallModel == "example-mini")
        #expect(m.model == "example-pro")
        #expect(team.choices.map(\.id) == ["example-pro", "example-mini"])
        #expect(team.choices.map(\.label) == ["example-pro", "Example Mini"])
        #expect(LauncherPresetText.subtitle(team) == "Claude Code 启动器 · gateway.example.com")
        // Picking another resets the model to its default (the first listed when none is declared).
        m.selectedID = "example-gateway/api"
        #expect(m.model == "m1" && m.selected?.modelsError == "超时")
        #expect(m.selected?.choices.map(\.id) == ["m1", "m2"])
        #expect(LauncherPresetText.subtitle(m.selected!) == "直接 API · api.example.com")
    }

    @Test func builtInPresetsDecodeTheirNewFieldsAndExtensionsLackThem() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/launchers/presets", json: #"""
        {"presets":[{"id":"builtin/deepseek","plugin":"builtin","name":"DeepSeek","display_name":"深度求索","kind":"launcher",
          "base_url":"https://api.example.com/anthropic","auth":"api_key","model":"ds-chat","signup_url":"https://platform.example.com/keys"}]}
        """#)
        let m = make(hub)
        await m.load()
        let p = try #require(m.selected)
        #expect(p.isBuiltin && p.title == "深度求索" && p.auth == "api_key" && p.signup?.host() == "platform.example.com")
        hub.on("GET", "/api/v1/launchers/presets", json: Self.presets)
        await m.load()
        let ext = try #require(m.presets.first)
        #expect(!ext.isBuiltin && ext.title == "示例网关 · 团队" && ext.displayName == nil && ext.signupURL == nil && ext.auth == nil)
    }

    @Test func anOlderHubHasNoPresets() async {
        let hub = FakeHub() // no route: 404, like a hub from before the plugin base
        let m = make(hub)
        await m.load()
        #expect(m.loaded && m.presets.isEmpty && m.error == nil && m.selected == nil && !m.canSave)
    }

    @Test func onlyTheNameTheKeyThePresetAndAnotherModelAreSent() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/launchers/presets", json: Self.presets)
        var sent: [JSONValue] = []
        hub.on("POST", "/api/v1/launchers") { req in
            sent.append(try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data()))
            return Data(#"{"ok":true}"#.utf8)
        }
        let m = make(hub)
        await m.load()
        #expect(!m.canSave)
        m.name = "9team"
        m.apiKey = " eg-secret "
        #expect(m.nameProblem != nil && !m.canSave)
        m.name = "presets"
        #expect(m.nameProblem == "不能叫 presets")
        m.name = " jv_team "
        #expect(m.nameProblem == nil && m.canSave)
        #expect(await m.save())
        var body = try #require(sent.last?.object)
        #expect(body["name"] == .string("jv_team") && body["api_key"] == .string("eg-secret"))
        #expect(body["preset"] == .string("example-gateway/team"))
        // Left empty: the hub fills them in from the preset.
        #expect(body["model"] == .string("") && body["base_url"] == .string("") && body["small_model"] == .string(""))
        #expect(body["kind"] == nil && body["api"] == nil)
        m.model = "example-mini"
        #expect(await m.save())
        body = try #require(sent.last?.object)
        #expect(body["model"] == .string("example-mini"))
    }

    @Test func aRefusalStaysOnThePage() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/launchers/presets", json: Self.presets)
        hub.on("POST", "/api/v1/launchers") { _ in throw APIError(status: 400, message: "没有这个供应商预设（提供它的插件停用或卸载了？）") }
        let m = make(hub)
        await m.load()
        m.name = "jv_team"
        m.apiKey = "k"
        #expect(await m.save() == false)
        #expect(m.error?.contains("没有这个供应商预设") == true && !m.saving)
    }

    @Test func theQuotaShowsTheHubsGatewayName() throws {
        let named = try JSON.decoder.decode(LauncherQuota.self, from: Data(#"{"gateway":"example-gateway","gateway_name":"示例网关"}"#.utf8))
        #expect(named.gatewayName == "示例网关")
        let older = try JSON.decoder.decode(LauncherQuota.self, from: Data(#"{"gateway":"example-gateway"}"#.utf8))
        #expect(older.gatewayName == "example-gateway")
        let blank = try JSON.decoder.decode(LauncherQuota.self, from: Data(#"{"gateway":"","gateway_name":" "}"#.utf8))
        #expect(blank.gatewayName == "gateway")
    }
}
