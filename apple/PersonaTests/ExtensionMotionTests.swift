import SwiftUI
import Testing
@testable import Persona
import PersonaWire

/// An extension's View drawn again (#37, as on the web and Android): which value changes roll and which
/// fade, that a block keeps its place under its key and carries the new text, that only blocks coming or going move the
/// layout, and that Reduce Motion moves nothing.
@MainActor
struct ExtensionMotionTests {
    private func view(_ json: String) throws -> ExtensionView {
        try JSONDecoder().decode(ExtensionView.self, from: Data(json.utf8))
    }

    @Test func onlyTheSameKindOfNumberRolls() {
        #expect(ExtensionMotion.change(from: "2/8", to: "6/8") == .roll(down: false))
        #expect(ExtensionMotion.change(from: "¥36.20", to: "¥1.20") == .roll(down: true))
        #expect(ExtensionMotion.change(from: "1.2M", to: "8.4M") == .roll(down: false))
        #expect(ExtensionMotion.change(from: "999", to: "1,234") == .roll(down: false))
        // The first number that changed decides the direction.
        #expect(ExtensionMotion.change(from: "3/8", to: "3/5") == .roll(down: true))
        // Written differently, the same number: still digits that change.
        #expect(ExtensionMotion.change(from: "¥1.2", to: "¥1.20") == .roll(down: false))

        #expect(ExtensionMotion.change(from: "Working", to: "Waiting on you") == .fade)
        #expect(ExtensionMotion.change(from: "—", to: "3") == .fade)
        #expect(ExtensionMotion.change(from: "3", to: "—") == .fade)
        #expect(ExtensionMotion.change(from: "+5%", to: "-5%") == .fade)
        #expect(ExtensionMotion.change(from: "2 left", to: "2/8 left") == .fade)
        // Digits of other scripts are text: `Double` does not read them.
        #expect(ExtensionMotion.change(from: "\u{FF11}\u{FF12}", to: "\u{FF11}\u{FF13}") == .fade)

        #expect(ExtensionMotion.change(from: "6/8", to: "6/8") == .same)
    }

    @Test func aValueSplitsIntoItsNumbersAndTheTextAroundThem() {
        let p = ExtensionMotion.parts("¥1,234.5/8 left")
        #expect(p.text == ["¥", "/", " left"] && p.numbers == [1234.5, 8])
        let none = ExtensionMotion.parts("—")
        #expect(none.text == ["—"] && none.numbers.isEmpty)
    }

    @Test func numbersRollAndWordsFade() {
        #expect(ExtensionMotion.transition(.roll(down: true)) == .numericText(countsDown: true))
        #expect(ExtensionMotion.transition(.roll(down: false)) == .numericText(countsDown: false))
        #expect(ExtensionMotion.transition(.fade) == .opacity)
    }

    @Test func reduceMotionAndAnUnchangedValueMoveNothing() {
        #expect(ExtensionMotion.animation(.roll(down: false), reduced: true) == nil)
        #expect(ExtensionMotion.animation(.fade, reduced: true) == nil)
        #expect(ExtensionMotion.animation(.same, reduced: false) == nil)
        #expect(ExtensionMotion.animation(.roll(down: false), reduced: false) == Theme.Motion.state)
        #expect(ExtensionMotion.animation(.fade, reduced: false) == Theme.Motion.state)
    }

    @Test func aRedrawKeepsEachBlockUnderItsKeyWithTheNewText() throws {
        let before = try view("""
        {"sections": [{"key": "today", "items": [
          {"type": "stat_grid", "key": "stats", "stats": [{"label": "Done", "value": "2/8"}]},
          {"type": "metric", "key": "cost", "label": "Cost", "value": "¥1.20"},
          {"type": "row", "key": "build", "title": "Build", "value": "3 min", "badge": {"text": "Running", "tone": "caution"}}
        ]}]}
        """)
        let after = try view("""
        {"sections": [{"key": "today", "items": [
          {"type": "stat_grid", "key": "stats", "stats": [{"label": "Done", "value": "6/8"}]},
          {"type": "metric", "key": "cost", "label": "Cost", "value": "¥36.20"},
          {"type": "row", "key": "build", "title": "Build", "value": "4 min", "badge": {"text": "Done", "tone": "positive"}}
        ]}]}
        """)
        let a = ExtensionParts.of(before).flatMap(\.items)
        let b = ExtensionParts.of(after).flatMap(\.items)
        // The same identity: SwiftUI draws the same views again, which is what lets a value go over to the new one.
        #expect(a.map(\.id) == b.map(\.id))
        #expect(b[0].component.statGrid?.stats.first?.value == "6/8")
        #expect(b[1].component.metric?.value == "¥36.20")
        #expect(b[2].component.row?.value == "4 min" && b[2].component.row?.badge?.tone == "positive")
        // Only values changed: the layout does not move.
        #expect(ExtensionMotion.blocks(before) == ExtensionMotion.blocks(after))
    }

    @Test func blocksComingOrGoingMoveTheLayout() throws {
        let before = try view("""
        {"sections": [{"items": [
          {"type": "row", "key": "a", "title": "A"}, {"type": "row", "key": "b", "title": "B"}
        ]}]}
        """)
        let added = try view("""
        {"sections": [{"items": [
          {"type": "row", "key": "a", "title": "A"}, {"type": "row", "key": "c", "title": "C"}, {"type": "row", "key": "b", "title": "B"}
        ]}]}
        """)
        let removed = try view("""
        {"sections": [{"items": [{"type": "row", "key": "b", "title": "B"}]}]}
        """)
        let renamed = try view("""
        {"sections": [{"items": [
          {"type": "row", "key": "a", "title": "A2"}, {"type": "row", "key": "b", "title": "B2"}
        ]}]}
        """)
        #expect(ExtensionMotion.blocks(before) != ExtensionMotion.blocks(added))
        #expect(ExtensionMotion.blocks(before) != ExtensionMotion.blocks(removed))
        #expect(ExtensionMotion.blocks(before) == ExtensionMotion.blocks(renamed))

        // A redraw is committed animated only when blocks came or went: not the first draw, not values alone, not
        // with Reduce Motion.
        #expect(ExtensionMotion.layout(from: before, to: added, reduced: false) == Theme.Motion.layout)
        #expect(ExtensionMotion.layout(from: before, to: removed, reduced: false) == Theme.Motion.layout)
        #expect(ExtensionMotion.layout(from: nil, to: before, reduced: false) == nil)
        #expect(ExtensionMotion.layout(from: before, to: renamed, reduced: false) == nil)
        #expect(ExtensionMotion.layout(from: before, to: added, reduced: true) == nil)
    }

    @Test func aCodePageIsNotAnimatedByItsBlocks() throws {
        let examples = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
            .deletingLastPathComponent().appending(path: "schema/extension/examples/showcase")
        let code = try JSONDecoder().decode(ExtensionView.self, from: Data(contentsOf: examples.appending(path: "code-browse.json")))
        var other = code
        other.sections[0].items.removeLast()
        #expect(code.wantsCodePage && ExtensionMotion.blocks(code) != ExtensionMotion.blocks(other))
        #expect(ExtensionMotion.layout(from: code, to: other, reduced: false) == nil)
        let list = try view(#"{"sections": [{"items": [{"type": "row", "key": "a", "title": "A"}]}]}"#)
        #expect(ExtensionMotion.layout(from: list, to: code, reduced: false) == nil)
        #expect(ExtensionMotion.layout(from: code, to: list, reduced: false) == nil)
    }
}
