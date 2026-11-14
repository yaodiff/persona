import Foundation
import Testing
import UIKit
@testable import Persona

/// What Share as Image draws for a message with cards (#7261, `ShareCardParts`): the chat's cards as cards, a card the
/// picture cannot draw as its fallback, and never a card's JSON. Made-up cards, the shared examples every client reads.
@MainActor struct ShareCardPartsTests {
    private static let examples = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().appending(path: "schema/extension/examples/assistant")

    private static func example(_ name: String) throws -> String {
        try String(contentsOf: examples.appending(path: "\(name).json"), encoding: .utf8)
    }

    private static func fence(_ json: String, language: String = "persona-view") -> String {
        "```\(language)\n\(json)\n```"
    }

    private static let progress = #"{"title":"Release","fallback":"Build done, tests running.","sections":[{"items":[{"type":"progress","label":"Build","ratio":1},{"type":"progress","label":"Tests","ratio":0.4}]}]}"#

    /// No part of the picture holds a card's source.
    private func expectNoSource(_ parts: [ShareCardPart], _ comment: Comment? = nil) {
        for part in parts {
            switch part {
            case .markdown(let s), .words(let s):
                #expect(!s.contains("\"sections\"") && !s.contains("persona-view") && !s.contains("\"fallback\""), comment)
            case .card, .cardWords: break
            }
        }
    }

    @Test func aProgressCardIsDrawnAsTheChatDrawsIt() throws {
        let text = "Here is where the release stands.\n\n" + Self.fence(Self.progress) + "\n\nI'll tell you when tests finish."
        let parts = ShareCardParts.of(text)
        try #require(parts.count == 3)
        #expect(parts[0] == .markdown("Here is where the release stands.\n\n"))
        guard case .card(let view) = parts[1] else { Issue.record("not a card: \(parts[1])"); return }
        #expect(view.title == "Release")
        #expect(view.sections.flatMap(\.items).map(\.type) == ["progress", "progress"])
        // The block ends with its closing line's newline (`AssistantViewBlocks`).
        #expect(parts[2] == .markdown("\nI'll tell you when tests finish."))
        expectNoSource(parts)
    }

    /// Every shared example: drawn, except the HTML page, which only runs live and so is its fallback.
    @Test(arguments: ["progress", "progress-updated", "decision", "compare", "chart", "form"])
    func everyExampleIsACard(_ name: String) throws {
        let json = try Self.example(name)
        let parts = ShareCardParts.of("Before.\n\n" + Self.fence(json) + "\n\nAfter.")
        #expect(parts.contains { if case .card = $0 { true } else { false } }, "\(name)")
        expectNoSource(parts, "\(name)")
    }

    @Test func anHTMLCardIsItsFallback() throws {
        let json = try Self.example("html")
        let object = try JSONSerialization.jsonObject(with: Data(json.utf8)) as? [String: Any]
        let fallback = try #require(object?["fallback"] as? String)
        #expect(ShareCardParts.of(Self.fence(json)) == [.words(fallback)])
    }

    /// Buttons, fields and segmented choices are drawn as they look; pictures that load later are not drawn offscreen.
    @Test func whatAPictureCanDraw() throws {
        func view(_ items: String) throws -> ExtensionView {
            try JSON.decoder.decode(ExtensionView.self, from: Data(#"{"fallback":"f","sections":[{"items":[\#(items)]}]}"#.utf8))
        }
        let drawn = [
            #"{"type":"button","title":"Go","action":{"id":"go","reply":"Go"}}"#,
            #"{"type":"field","name":"n","label":"Name","kind":"text"}"#,
            #"{"type":"table","columns":[{"title":"A"}],"rows":[["1"]]}"#,
            #"{"type":"row","title":"Plain","avatar":{"text":"AB"}}"#,
        ]
        let fallback = [
            #"{"type":"row","title":"Face","avatar":{"url":"https://example.com/a.png","text":"AB"}}"#,
            #"{"type":"row","title":"Pic","thumbnail":{"url":"https://example.com/a.png"}}"#,
            #"{"type":"html","html":"<p>x</p>"}"#,
            // Nothing to draw.
            #"{"type":"divider"}"#,
        ]
        for items in drawn {
            let v = try view(items)
            #expect(ShareCardParts.drawable(v), "\(items)")
        }
        for items in fallback {
            let v = try view(items)
            #expect(!ShareCardParts.drawable(v), "\(items)")
        }
    }

    @Test func aCardThatDoesNotParseIsItsFallbackOrAPlaceholder() {
        // A component this app does not know: the chat shows the fallback, so does the picture.
        let unknown = #"{"fallback":"Plan in words.","sections":[{"items":[{"type":"hologram"}]}]}"#
        #expect(ShareCardParts.of(Self.fence(unknown)) == [.words("Plan in words.")])
        // Broken JSON and a card still streaming in: the placeholder, never the source.
        #expect(ShareCardParts.of(Self.fence(#"{"fallback": "x", "sections": ["#)) == [.words(AssistantCardText.mark)])
        #expect(ShareCardParts.of("Working on it\n\n```persona-view\n{\"title\":\"Half") ==
            [.markdown("Working on it\n\n"), .words(AssistantCardText.mark)])
        // A fourth card: the chat draws only three, the rest as their fallback.
        let four = (0..<4).map { _ in Self.fence(Self.progress) }.joined(separator: "\n\n")
        let parts = ShareCardParts.of(four)
        #expect(parts.filter { if case .card = $0 { true } else { false } }.count == 3)
        #expect(parts.last == .words("Build done, tests running."))
    }

    /// An example inside another code block is code, as in the chat.
    @Test func aCardShownAsAnExampleStaysCode() {
        let text = "````md\n" + Self.fence(Self.progress) + "\n````"
        #expect(ShareCardParts.of(text) == [.markdown(text)])
    }

    @Test func anOlderPersonaCardInTheWords() {
        let stat = #"{"type":"stat","title":"Steps","value":8432,"unit":"steps"}"#
        guard case .card = ShareCardParts.picture(language: "persona-card", stat) else { Issue.record("not drawn"); return }
        // A type this app does not know yet: its numbers in words, as the chat shows them.
        let ring = #"{"type":"newkind","title":"Sleep","value":7.5,"unit":"hours"}"#
        #expect(ShareCardParts.picture(language: "persona-card", ring) == .cardWords(CardWords(title: "Sleep", lines: ["7.5 hours"])))
        #expect(ShareCardParts.picture(language: "persona-card", "{broken") == .words(AssistantCardText.mark))
    }

    @Test func aLongMessageIsCutButNeverInsideACard() {
        let long = String(repeating: "a", count: ShareCardParts.maxChars + 10)
        #expect(ShareCardParts.of(long) == [.markdown(String(repeating: "a", count: ShareCardParts.maxChars) + "\n\n…")])
        #expect(ShareCardParts.of("  words \n") == [.markdown("words")])
        // A card past the words' budget is left out whole, with the cut's mark.
        let before = String(repeating: "b", count: ShareCardParts.maxChars - 5)
        #expect(ShareCardParts.of(before + "\n\n" + Self.fence(Self.progress)) == [.markdown(before + "\n\n"), .markdown("…")])
    }

    /// A card's JSON never widens the picture as if it were code.
    @Test func cardsAreNotWideBlocks() {
        #expect(ShareCardBlocks.wide(Self.fence(Self.progress)) == [])
        #expect(ShareCardBlocks.wide(Self.fence("{}", language: "Persona-Card extra")) == [])
        #expect(ShareCardBlocks.wide("```swift\nlet a = 1\n```") == ["```swift\nlet a = 1\n```"])
    }

    /// The picture of a reply with cards renders in light and dark; their JSON does not widen it as code would.
    /// With PERSONA_SHOTS set the images are written there (`share-card-<light|dark>.png`) to look at.
    @Test(arguments: [false, true]) func aReplyWithACardRenders(dark: Bool) throws {
        let progress = try Self.example("progress"), compare = try Self.example("compare")
        let text = "Here is where the release stands.\n\n" + Self.fence(progress) + "\n\n" + Self.fence(compare)
            + "\n\nI'll tell you when tests finish."
        let message = ShareMessage(text: text, isUser: false, source: "", date: nil, agent: "Persona")
        let image = try #require(MessageShareCard.render(message, dark: dark))
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"], let png = image.pngData() {
            try png.write(to: URL(fileURLWithPath: dir).appending(path: "share-card-\(dark ? "dark" : "light").png"))
        }
        #expect(ShareCardWidth.natural(MessageShareCard(message: message, dark: dark).parts) == 0)
        #expect(image.size.width >= ShareCardWidth.base && image.size.width <= ShareCardWidth.cap)
        #expect(image.size.height > 0)
    }
}
