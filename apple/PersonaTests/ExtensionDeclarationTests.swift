import Foundation
import Testing
@testable import Persona
import PersonaWire

@MainActor @Suite(.serialized)
struct ExtensionDeclarationTests {
    @Test func promptsAndRuleTemplatesOnlyFillDrafts() async throws {
        let hub = FakeHub()
        let prompt = #"{"extension":"example","extension_name":"示例扩展","id":"weekly","title":"整理周报","text":"整理本周的进展"}"#
        hub.on("GET", "/api/v1/extensions/-/prompts", json: #"{"prompts":[\#(prompt)]}"#)
        hub.on("GET", "/api/v1/extensions/-/triage-templates", json: #"{"templates":[\#(prompt)]}"#)
        var api = hub.client
        api.extensionsAPI = true
        let m = ExtensionTextContributions()
        await m.load(api: api)
        let p = try #require(m.prompts.first)
        #expect(p.id == "example/weekly" && ExtensionTextContributions.filling(p) == "整理本周的进展")
        #expect(m.templates.first?.id == p.id)
        #expect(!hub.requests.contains { $0.method != "GET" }, "没有发送消息或保存规则")
        api.extensionsAPI = false
        await m.load(api: api)
        #expect(m.prompts.isEmpty && m.templates.isEmpty)
    }

    @Test func invalidThemeColorsAreNotAdjustedAndOffered() throws {
        let bad = try offer(light: "#FFF59D", dark: "#FFF59D")
        #expect(ExtensionThemeContrast.palette(bad) == nil)
        let malformed = try offer(light: "#+12345", dark: "#0A84FF")
        #expect(ExtensionThemeContrast.palette(malformed) == nil)
        let rejectedByHub = try offer(light: "#007AFF", dark: "#0A84FF", problems: ["服务端校验失败"])
        #expect(ExtensionThemeContrast.palette(rejectedByHub) == nil)
    }

    @Test func extensionRoleColorsOnlyApplyWhenSelected() throws {
        let offer = try offer(light: "#007AFF", dark: "#0A84FF")
        let palette = try #require(ExtensionThemeContrast.palette(offer))
        let prior = ExtensionThemePalette.current
        defer { ExtensionThemePalette.current = prior }
        ExtensionThemePalette.current = palette
        for style in AppStyle.allCases {
            let base = StyleSpec(style.component, style.component.colors)
            let unselected = StyleSpec.compose(style: style, color: .style, persona: nil)
            #expect(StyleSpecTests.hex(base.accent) == StyleSpecTests.hex(unselected.accent))
            #expect(unselected.extensionRoles == nil)
            let selected = StyleSpec.compose(style: style, color: .extensionTheme, persona: nil)
            #expect(selected.extensionRoles == palette)
            #expect(palette.positive == nil && palette.caution == nil && palette.danger == nil && palette.ai == nil)
        }
    }

    @Test func themeAPIUsesTheFlatGeneratedFields() throws {
        let original = try offer(light: "#007AFF", dark: "#0A84FF")
        let data = try JSON.encoder.encode(original)
        let json = try JSON.decoder.decode(JSONValue.self, from: data)
        #expect(json["extension"] == .string("example") && json["light"]?["accent"] == .string("#007AFF"))
        #expect(json["theme"] == nil)
        #expect(try JSON.decoder.decode(ExtensionThemeOffer.self, from: data) == original)
    }

    @Test func missingAppearanceKeepsDistinctDefaultWarningText() throws {
        var item = try offer(light: "#007AFF", dark: "#0A84FF")
        item.theme.dark.caution = "#FF9F0A"
        let palette = try #require(ExtensionThemeContrast.palette(item))
        #expect(palette.caution?.light == Theme.Role.caution.light)
        #expect(palette.cautionText?.light == Theme.Role.cautionText.light)
        #expect(palette.cautionText?.dark == 0xFF9F0A)
    }

    @Test func roleColorIdentityIsStableBetweenReadsAndDefaultRecomposition() throws {
        let originalColor = ThemeColor.current
        let originalPalette = ExtensionThemePalette.current
        defer { ExtensionThemePalette.current = originalPalette; ThemeColor.current = originalColor }
        ThemeColor.current = .style
        let before = Theme.positive
        StyleSpec.recompose()
        #expect(before == Theme.positive)
        var item = try offer(light: "#007AFF", dark: "#0A84FF")
        item.theme.light.positive = "#248A3D"
        item.theme.dark.positive = "#30D158"
        ExtensionThemePalette.current = try #require(ExtensionThemeContrast.palette(item))
        ThemeColor.current = .extensionTheme
        let selected = Theme.positive
        #expect(selected == Theme.positive)
        #expect(ExtensionUIStyle.tone("positive") == Theme.positive)
    }

    @Test func selectionCachesOfflineAndDisappearingContributionReturnsToDefault() async throws {
        let hub = FakeHub()
        var api = hub.client
        api.extensionsAPI = true
        let item = try offer(light: "#007AFF", dark: "#0A84FF")
        let answer = String(decoding: try JSON.encoder.encode(["themes": [item]]), as: UTF8.self)
        hub.on("GET", "/api/v1/extensions/-/themes", json: answer)
        var color = ThemeColor.style
        let deps = ExtensionThemesModel.Deps(color: { color }, setColor: { color = $0 })
        let prior = ExtensionThemePalette.current
        let folder = FileManager.default.temporaryDirectory.appending(path: UUID().uuidString)
        let cache = folder.appending(path: "selection.json")
        defer { ExtensionThemePalette.current = prior; try? FileManager.default.removeItem(at: folder) }
        let m = ExtensionThemesModel(cache: cache, deps: deps)
        await m.refresh(api: api)
        #expect(m.offers == [item] && m.palette == nil && color == .style)
        m.select(item)
        #expect(color == .extensionTheme && m.palette?.id == item.id)
        let stored = try Data(contentsOf: cache)
        #expect((try FileManager.default.attributesOfItem(atPath: cache.path)[.posixPermissions] as? NSNumber)?.intValue == 0o600)
        await m.refresh(api: api)
        #expect(try Data(contentsOf: cache) == stored)
        let offline = ExtensionThemesModel(cache: cache, deps: deps)
        #expect(offline.offers == [item] && offline.palette?.id == item.id)
        hub.on("GET", "/api/v1/extensions/-/themes") { _ in throw APIError(status: 503, message: "暂时离线") }
        await m.refresh(api: api)
        #expect(m.offers == [item] && color == .extensionTheme)
        hub.on("GET", "/api/v1/extensions/-/themes", json: #"{"themes":[]}"#)
        await m.refresh(api: api)
        #expect(m.offers.isEmpty && m.palette == nil && color == .style)
        #expect(!FileManager.default.fileExists(atPath: cache.path))
    }

    @Test func switchingHubDoesNotReuseTheOtherHubsTheme() async throws {
        let hub = FakeHub()
        var api = hub.client
        api.extensionsAPI = true
        api.group = "first"
        let item = try offer(light: "#007AFF", dark: "#0A84FF")
        hub.on("GET", "/api/v1/extensions/-/themes", json: String(decoding: try JSON.encoder.encode(["themes": [item]]), as: UTF8.self))
        var color = ThemeColor.style
        let prior = ExtensionThemePalette.current
        defer { ExtensionThemePalette.current = prior }
        let m = ExtensionThemesModel(cache: nil, deps: .init(color: { color }, setColor: { color = $0 }))
        await m.refresh(api: api)
        m.select(item)
        api.group = "second"
        hub.on("GET", "/api/v1/extensions/-/themes") { _ in throw APIError(status: 503, message: "离线") }
        await m.refresh(api: api)
        #expect(m.palette == nil && m.offers.isEmpty && color == .style)
    }

    private func offer(light: String, dark: String, problems: [String] = []) throws -> ExtensionThemeOffer {
        let body: [String: Any] = ["extension": "example", "extension_name": "示例扩展", "id": "blue", "name": "可读主题",
                                  "light": ["accent": light], "dark": ["accent": dark], "problems": problems]
        return try JSON.decoder.decode(ExtensionThemeOffer.self, from: JSONSerialization.data(withJSONObject: body))
    }
}
