import Foundation
import Testing
@testable import Persona
import PersonaWire

struct ExtensionCustomContributesTests {
    private var examples: URL {
        URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
            .appendingPathComponent("schema/extension/examples")
    }

    @Test func manifestFixturesRoundTrip() throws {
        for url in try FileManager.default.contentsOfDirectory(at: examples, includingPropertiesForKeys: nil)
            where url.lastPathComponent.hasPrefix("Manifest.") && url.pathExtension == "json" {
            let raw = try Data(contentsOf: url)
            let manifest = try JSONDecoder().decode(ExtensionManifest.self, from: raw)
            let expected = try JSONDecoder().decode(JSONValue.self, from: raw)
            let actual = try JSONDecoder().decode(JSONValue.self, from: JSONEncoder().encode(manifest))
            #expect(actual == expected, "Manifest fixture changed: \(url.lastPathComponent)")
        }
    }

    @Test func extraKeysIgnoreCallerKeyConversionStrategies() throws {
        let raw = try Data(contentsOf: examples.appendingPathComponent("Manifest.custom-contributions.json"))
        let expected = try JSONDecoder().decode(JSONValue.self, from: raw)["contributes"]
        let manifest = try JSONDecoder().decode(ExtensionManifest.self, from: raw)
        let optional: ExtensionContributes? = manifest.contributes
        let fields = try #require(optional)
        let encoder = JSONEncoder()
        encoder.keyEncodingStrategy = .convertToSnakeCase
        let encoded = try encoder.encode(fields)
        #expect(try JSONDecoder().decode(JSONValue.self, from: encoded) == expected)
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        let decoded = try decoder.decode(ExtensionContributes.self, from: encoded)
        #expect(decoded == fields)
    }

    @Test func extrasCannotShadowDeclaredFields() throws {
        let raw = try Data(contentsOf: examples.appendingPathComponent("Manifest.custom-contributions.json"))
        let manifest = try JSONDecoder().decode(ExtensionManifest.self, from: raw)
        // Promote to an optional so this remains source-compatible if pack manifests may omit contributes.
        let optional: ExtensionContributes? = manifest.contributes
        var fields = try #require(optional)
        #expect(fields.ui?.first?.id == "home")
        #expect(fields.additionalProperties.count == 8)
        #expect(fields.additionalProperties["UI"] != nil)
        #expect(fields.additionalProperties["__proto__"] != nil)
        #expect(fields.additionalProperties["constructor"] != nil)
        #expect(fields.additionalProperties["additionalProperties"] != nil)
        let original = fields
        fields.additionalProperties["ui"] = .string("not a UI list")
        fields.additionalProperties["providers"] = .string("cannot add an omitted declared field")
        let encoded = try JSONEncoder().encode(fields)
        let back = try JSONDecoder().decode(ExtensionContributes.self, from: encoded)
        #expect(back.ui == original.ui)
        #expect(back.providers == nil)
        #expect(back.additionalProperties == original.additionalProperties)
        let object = try JSONDecoder().decode(JSONValue.self, from: encoded)
        #expect(object["providers"] == nil)
    }
}
