import Foundation
import Testing
@testable import Persona
import PersonaWire

/// An extension's settings and secrets checked before the hub saves them (#5906): a set secret's last four characters
/// on its row, the extension's own words when it refuses a change (in the secret's sheet, under the form), and its
/// `open_url` to its own settings.
@MainActor
struct ExtensionSettingsCheckTests {
    private static func detail(secret: String) -> String {
        #"{"id":"quota-gate","name":"Quota Gate","kind":"providers","status":"running","enabled":true,"# +
        #""form":{"fields":[{"name":"gateway_url","type":"string","title":"网关地址"}]},"config":{},"# +
        #""secrets":[\#(secret)],"permissions":[]}"#
    }

    private func make(_ hub: FakeHub) -> ExtensionModel { ExtensionModel(id: "quota-gate", deps: .init(api: { hub.client })) }

    @Test func aSecretsHintDecodesAndIsOnItsRow() throws {
        let set = try JSON.decoder.decode(HubExtensionDetail.self, from: Data(Self.detail(
            secret: #"{"name":"gateway_key","title":"Key","set":true,"hint":"7f3a"}"#).utf8)).secrets[0]
        #expect(set.hint == "7f3a")
        #expect(ExtensionText.secretState(set) == "已设置 · 末 4 位 7f3a")

        let noHint = try JSON.decoder.decode(HubExtensionDetail.self, from: Data(Self.detail(
            secret: #"{"name":"gateway_key","title":"Key","set":true}"#).utf8)).secrets[0]
        #expect(noHint.hint == nil)
        #expect(ExtensionText.secretState(noHint) == "已设置")
        #expect(ExtensionText.secretState(ExtensionSecret(name: "k", title: "K", set: true, hint: "")) == "已设置")
        #expect(ExtensionText.secretState(ExtensionSecret(name: "k", title: "K", required: true, set: false)) == "未设置（必填）")
        #expect(ExtensionText.secretState(ExtensionSecret(name: "k", title: "K", set: false)) == "未设置")
    }

    /// The extension's check said no: its words come back for the sheet, the page shows no error, nothing changed.
    @Test func aRefusedSecretComesBackWithTheExtensionsWords() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/quota-gate/-/", json: Self.detail(secret: #"{"name":"gateway_key","title":"Key","set":false}"#))
        hub.on("PUT", "/api/v1/plugins/quota-gate/-/secrets/gateway_key") { _ in
            throw APIError(status: 400, message: "网关不认这个 Key", fields: ["code": "check_failed", "error": "网关不认这个 Key"])
        }
        let page = make(hub)
        await page.load()
        let typed = "ag-bad-key-0000-1234"
        let result = await page.setSecret("gateway_key", value: typed)
        #expect(result == .refused("网关不认这个 Key"))
        #expect(page.error == nil && page.detail?.secrets.first?.set == false)
        if case .refused(let words) = result { #expect(!words.contains(typed), "the value is never in a message") }
    }

    @Test func tooManyChecksAndMovedSettingsAreRefusalsToo() {
        for code in ["check_rate_limited", "settings_moved"] {
            #expect(ExtensionModel.refusal(APIError(status: 429, message: "稍后再试", fields: ["code": code])) == "稍后再试")
        }
        #expect(ExtensionModel.refusal(APIError(status: 400, message: " ", fields: ["code": "check_failed"])) == "扩展没有接受这次修改")
        #expect(ExtensionModel.refusal(APIError(status: 400, message: "bad", fields: ["code": "invalid_config"])) == nil)
        #expect(ExtensionModel.refusal(APIError(status: 500, message: "boom")) == nil)
    }

    /// Any other failure is the page's error, as before.
    @Test func anotherFailureIsThePagesError() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/plugins/quota-gate/-/", json: Self.detail(secret: #"{"name":"gateway_key","title":"Key","set":false}"#))
        hub.on("PUT", "/api/v1/plugins/quota-gate/-/secrets/gateway_key") { _ in throw APIError(status: 502, message: "hub 出错了") }
        let page = make(hub)
        await page.load()
        #expect(await page.setSecret("gateway_key", value: "ag-demo-key") == .failed)
        #expect(page.error == "hub 出错了")
    }

    /// Settings the extension refused: its words under the form, the draft kept; the next save that goes through
    /// clears them.
    @Test func refusedSettingsShowUnderTheForm() async {
        let hub = FakeHub()
        let detail = Self.detail(secret: #"{"name":"gateway_key","title":"Key","set":false}"#)
        hub.on("GET", "/api/v1/plugins/quota-gate/-/", json: detail)
        var refuse = true
        hub.on("PUT", "/api/v1/plugins/quota-gate/-/config") { _ in
            if refuse { throw APIError(status: 400, message: "连不上这个网关", fields: ["code": "check_failed"]) }
            return Data(detail.replacingOccurrences(of: #""config":{}"#, with: #""config":{"gateway_url":"https://gateway.example.com"}"#).utf8)
        }
        let page = make(hub)
        await page.load()
        page.set(.string("https://nowhere.example.com"), at: ["gateway_url"])
        #expect(await page.save() == false)
        #expect(page.formError == "连不上这个网关" && page.error == nil)
        #expect(page.value(["gateway_url"]) == .string("https://nowhere.example.com"), "the draft is kept")
        refuse = false
        page.set(.string("https://gateway.example.com"), at: ["gateway_url"])
        #expect(await page.save())
        #expect(page.formError == nil)
    }

    @Test func theSettingsLinkParses() throws {
        func id(_ s: String) -> String? { URL(string: s).flatMap(ExtensionSettingsLink.extensionID) }
        #expect(id("persona://settings/extensions/quota-gate") == "quota-gate")
        #expect(id("PERSONA://Settings/extensions/quota-gate") == "quota-gate")
        #expect(id("persona://settings/extensions/a%2Fb%20c") == "a/b c", "the id is one escaped path segment")
        #expect(id("persona://settings/extensions/") == nil)
        #expect(id("persona://settings/extensions") == nil)
        #expect(id("persona://settings/extensions/a/b") == nil)
        #expect(id("persona://settings/connectors/a") == nil)
        #expect(id("persona://inbox/a/source") == nil)
        #expect(id("https://settings/extensions/a") == nil)
    }

    /// An extension may open its own settings only.
    @Test func onlyItsOwnSettingsOpen() throws {
        let own = try #require(URL(string: "persona://settings/extensions/quota-gate"))
        #expect(ExtensionViewModel.opens(own, item: nil, owner: "quota-gate"))
        #expect(!ExtensionViewModel.opens(own, item: nil, owner: "other"))
        #expect(!ExtensionViewModel.opens(own, item: nil))
        #expect(ExtensionViewModel.opens(try #require(URL(string: "https://example.com")), item: nil, owner: "quota-gate"))
    }
}
