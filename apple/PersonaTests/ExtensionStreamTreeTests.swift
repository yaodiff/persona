import Foundation
import Testing
@testable import Persona

@MainActor @Suite struct ExtensionStreamTreeTests {
    typealias Raw = ExtensionStreamJSON
    private var examples: URL {
        URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appending(path: "schema/extension/examples")
    }
    private func fixture(_ name: String) throws -> Raw { try Raw.parse(Data(contentsOf: examples.appending(path: name))) }
    private func raw(_ value: String) throws -> Raw { try Raw.parse(Data(value.utf8)) }
    private func tree() throws -> ExtensionStreamTree {
        try ExtensionStreamTree(snapshot: fixture("UIStreamSnapshot.keyed.json"), components: ExtensionPageStream.components)
    }

    @Test func canonicalValidAndInvalidSchemas() throws {
        let manager = FileManager.default
        for name in try manager.contentsOfDirectory(atPath: examples.path).filter({ $0.hasPrefix("UIStream") && $0.hasSuffix(".json") }) {
            let definition = String(name.split(separator: ".")[0])
            try ExtensionStreamSchema.check(fixture(name), definition: definition)
        }
        for name in try manager.contentsOfDirectory(atPath: examples.appending(path: "invalid").path).filter({ $0.hasPrefix("UIStream") && $0.hasSuffix(".json") }) {
            let definition = String(name.split(separator: ".")[0])
            #expect(throws: (any Error).self) { try ExtensionStreamSchema.check(fixture("invalid/" + name), definition: definition) }
        }
    }

    @Test func canonicalOperationTraceAndPresence() throws {
        var tree = try tree()
        try tree.apply(fixture("UIStreamDelta.operations.json"))
        let expected = try fixture("UIStreamSnapshot.after-operations.json")
        #expect(tree.view == expected["view"])
        #expect(tree.version == 2)
        let presence = try fixture("traces/UIStreamSnapshot.native-presence.json")
        let retained = try ExtensionStreamTree(snapshot: presence, components: ExtensionPageStream.components.union(["carousel"]))
        #expect(retained.view == presence["view"])
        #expect(try Raw.parse(retained.view.data) == presence["view"])
        let empty = try ExtensionStreamTree(snapshot: fixture("UIStreamSnapshot.required-empty.json"), components: ExtensionPageStream.components)
        #expect(empty.view == (try fixture("UIStreamSnapshot.required-empty.json"))["view"])
    }

    @Test func emptyFenceAdvancesExactlyOneWithoutReplacingRoot() throws {
        var tree = try tree(); let before = tree.view
        let fence = try raw(#"{"protocol":1,"session":"session-a","epoch":"epoch-a","base_version":1,"version":2,"operations":[]}"#)
        try tree.apply(fence)
        #expect(tree.version == 2 && tree.view == before)
        #expect(throws: (any Error).self) { try tree.apply(fence) }
        let noOp = try raw(#"{"protocol":1,"session":"session-a","epoch":"epoch-a","base_version":2,"version":2,"operations":[]}"#)
        try tree.apply(noOp)
        #expect(tree.version == 2 && tree.view == before)
    }

    @Test func invalidBatchRollsBackEveryMutation() throws {
        var tree = try tree(); let before = tree
        let operations = [
            #"{"op":"remove","key":"counter"},{"op":"remove","key":"absent"}"#,
            #"{"op":"replace","key":"a","item":{"type":"text","key":"wrong","text":"new"}}"#,
            #"{"op":"remove","key":"page"}"#,
            #"{"op":"insert","parent":"page","item":{"type":"text","key":"new","text":"new"}}"#,
            #"{"op":"insert","parent":"s1","before":null,"item":{"type":"text","key":"new","text":"new"}}"#,
            #"{"op":"move","key":"s1","parent":"s1"}"#
        ]
        for operations in operations {
            let delta = try raw(#"{"protocol":1,"session":"session-a","epoch":"epoch-a","base_version":1,"version":2,"operations":[\#(operations)]}"#)
            #expect(throws: (any Error).self) { try tree.apply(delta) }
            #expect(tree == before)
        }
    }

    @Test func foreignOwnershipEpochGapAndUnknownShapeReject() throws {
        var tree = try tree(); let before = tree
        for fields in [#""session":"foreign","epoch":"epoch-a","base_version":1,"version":2"#,
                       #""session":"session-a","epoch":"foreign","base_version":1,"version":2"#,
                       #""session":"session-a","epoch":"epoch-a","base_version":1,"version":3"#] {
            #expect(throws: (any Error).self) { try tree.apply(raw(#"{"protocol":1,\#(fields),"operations":[]}"#)) }
            #expect(tree == before)
        }
    }

    @Test func originalNumbersDuplicateKeysAndMalformedFieldsReject() throws {
        for value in [#"{"version":1,"version":2}"#, #"{"x":"\uD800"}"#, #"{"x":NaN}"#] {
            #expect(throws: (any Error).self) { try raw(value) }
        }
        for version in ["9007199254740990.9", "9007199254740991.0000000000001", "9007199254740992", "1.5"] {
            let value = try raw(#"{"protocol":1,"session":"session-a","epoch":"epoch-a","base_version":1,"version":\#(version),"operations":[]}"#)
            #expect(throws: (any Error).self) { try ExtensionStreamSchema.check(value, definition: "UIStreamDelta") }
        }
    }

    @Test func schemaNumbersRemainLosslessWithoutDecimalRounding() throws {
        for token in ["1e100", "-3.75e200", "1.00000000000000000000000000000000000000001"] {
            let value = try raw(#"{"label":"Synthetic point","value":\#(token)}"#)
            try ExtensionStreamSchema.check(value, definition: "ChartPoint", file: "view.schema.json")
            #expect(value["value"] == .number(token))
        }
        for token in ["1.00000000000000000000000000000000000000001", "1e-100", "9007199254740991.0000000000000000000000001"] {
            let value = try raw(#"{"protocol":1,"session":"session-a","epoch":"epoch-a","base_version":1,"version":\#(token),"operations":[]}"#)
            #expect(throws: (any Error).self) { try ExtensionStreamSchema.check(value, definition: "UIStreamDelta") }
        }
        let progress = try raw(#"{"type":"progress","label":"Synthetic progress","ratio":1.00000000000000000000000000000000000000001}"#)
        #expect(throws: (any Error).self) { try ExtensionStreamSchema.check(progress, definition: "ProgressComponent", file: "view.schema.json") }
        #expect(try raw("10e-1").numeric?.integer == true)
        #expect(try raw("1000e-5").numeric?.integer == false)
    }

    @Test func declarationsAdvertiseOnlyWhatTheAppDraws() {
        #expect(!ExtensionPageStream.components.contains("html"))
        // UI version 6's pictures and tiles: drawn on iOS (#827); the frozen Mac app keeps its earlier list.
        for media in ["image", "grid", "carousel"] {
            #if os(macOS)
            #expect(!ExtensionPageStream.components.contains(media))
            #else
            #expect(ExtensionPageStream.components.contains(media))
            #endif
        }
        let watchFeatures = LiveConnection.queryItems(EventCursor()).first?.value ?? ""
        #expect(!watchFeatures.contains(ExtensionPageStream.feature))
        let appFeatures = LiveConnection.queryItems(EventCursor(), extensionStream: true).first?.value ?? ""
        #expect(appFeatures.contains(ExtensionPageStream.feature))
    }
}
