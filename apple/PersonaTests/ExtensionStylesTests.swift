import Foundation
import Testing
import UIKit
@testable import Persona
import PersonaWire

/// UI version 6's styles on iOS (docs/extensions/extensions.md "UI version 6", #827): every icon name of the schema's
/// set drawn as one of the app's own symbols, every named color as one of its hues, a badge's tone before its color,
/// and the two new text styles. The names are read from schema/extension/view.schema.json.
@MainActor
struct ExtensionStylesTests {
    private static let schema = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().appending(path: "schema/extension/view.schema.json")

    private static func names(_ def: String) throws -> [String] {
        let json = try JSONSerialization.jsonObject(with: Data(contentsOf: schema)) as? [String: Any]
        let defs = (json?["$defs"] ?? json?["definitions"]) as? [String: Any]
        return try #require((defs?[def] as? [String: Any])?["enum"] as? [String])
    }

    @Test func everyIconNameIsDrawnAsTheAppsOwn() throws {
        let names = try Self.names("NavIcon")
        #expect(names.count >= 70, "the schema's icon set")
        for n in names where n != "plugin" && n != "extension" {
            let symbol = ExtensionUIStyle.icon(n)
            #expect(symbol != Theme.Icon.plugins, "\(n) falls back to the extensions' icon")
            #expect(UIImage(systemName: symbol) != nil, "\(n): no symbol \(symbol)")
        }
        #expect(ExtensionUIStyle.icon("hologram") == Theme.Icon.plugins, "an unknown one draws as the default")
    }

    @Test func everyNamedColorIsAHue() throws {
        for n in try Self.names("Color") { #expect(ExtensionUIStyle.color(n) != nil, "\(n)") }
        #expect(ExtensionUIStyle.color("#ff0000") == nil && ExtensionUIStyle.color(nil) == nil, "never a hex; unknown: the quiet ink")
    }

    @Test func aBadgesToneComesBeforeItsColor() {
        if case .neutral = ExtensionUIStyle.badge(ExtensionBadge(text: "a")) {} else { Issue.record("plain: neutral") }
        if case .tint(_, let text) = ExtensionUIStyle.badge(ExtensionBadge(text: "a", color: "teal")) {
            #expect(text == .primary, "a named color tints behind words in the text's own color")
        } else { Issue.record("colored: tinted") }
        if case .tint(_, let text) = ExtensionUIStyle.badge(ExtensionBadge(text: "a", tone: "danger", color: "teal")) {
            #expect(text == nil, "the tone's own words")
        } else { Issue.record("a tone wins") }
    }

    @Test func theNewTextStyles() throws {
        let styles = try Self.names("TextStyle")
        #expect(styles.contains("title") && styles.contains("headline"))
        #expect(ExtensionUIStyle.font("title") == Theme.Font.title && ExtensionUIStyle.font("headline") == Theme.Font.heading)
        #expect(ExtensionUIStyle.font("shout") == Theme.Font.body, "an unknown one as body")
    }
}
