import Foundation

/// One piece of a message drawn into its picture (Share as Image, #7261): words as Markdown, or a card the assistant drew.
nonisolated enum ShareCardPart: Equatable, Sendable {
    /// The message's own words, drawn with the picture's Markdown theme (`MarkdownUI.Theme.card`).
    case markdown(String)
    /// A card drawn as the chat draws it, with nothing to use (`ShareCardViewCard`).
    case card(ExtensionView)
    /// An older ```persona-card the app cannot draw, in words, as the chat shows it (`AssistantCardWords`).
    case cardWords(CardWords)
    /// A card the picture cannot draw: its `fallback`, or a placeholder, never its source.
    case words(String)
}

/// What a shared message's picture is made of (#7261, docs/assistant/assistant-views.md「Sharing a reply as a picture」):
/// what you see is what you share. The chat draws each ```persona-view block as a card (`AssistantRichMarkdown`), so the
/// picture splits the message by the same fence rules (`AssistantViewBlocks`) and decides for each card whether it can
/// be drawn offscreen; one that cannot (an HTML page, pictures loaded from the network) is its fallback. Very long
/// messages are cut at `maxChars` of words, and never inside a card.
enum ShareCardParts {
    /// Very long answers are cut: a picture of ten screens helps nobody.
    static let maxChars = 3000

    static func of(_ message: String) -> [ShareCardPart] {
        let text = message.trimmingCharacters(in: .whitespacesAndNewlines)
        var parts: [ShareCardPart] = []
        var budget = maxChars
        var position = text.startIndex
        /// Adds words; false when they had to be cut, which ends the picture.
        func words(_ s: Substring) -> Bool {
            guard !s.allSatisfy(\.isWhitespace) else { return true }
            if s.count <= budget {
                parts.append(.markdown(String(s)))
                budget -= s.count
                return true
            }
            parts.append(.markdown(String(s.prefix(budget)) + "\n\n…"))
            budget = 0
            return false
        }
        for block in AssistantViewBlocks.parse(text) {
            guard words(text[position..<block.range.lowerBound]) else { return parts }
            let part = picture(block, source: String(text[block.range]))
            // A card counts as many characters as its words: a reply of a few cards stays whole.
            guard part.length <= budget else {
                parts.append(.markdown("…"))
                return parts
            }
            parts.append(part)
            budget -= part.length
            position = block.range.upperBound
        }
        _ = words(text[position...])
        return parts
    }

    /// A ```persona-view block as the assistant chat reads it (`AssistantRichMarkdown.card`): a closed block among the
    /// first three, within the size limit, with its fallback, is a card; anything else is its fallback words.
    static func picture(_ block: AssistantViewBlocks.Block, source: String) -> ShareCardPart {
        if block.closed, block.ordinal < AssistantViewBlocks.maxBlocks, block.content.utf8.count <= AssistantViewBlocks.maxContentBytes {
            if let view = AssistantRichMarkdown.decode(block.content) {
                return drawable(view) ? .card(view) : .words(view.fallback ?? AssistantCardText.mark)
            }
            // A card from before every card carried a fallback keeps its read-only drawing (`AssistantCardBlock`).
            if !block.content.contains("\"fallback\""),
               let view = AssistantCardBlock.parse(language: AssistantCardBlock.viewLanguage, block.content), drawable(view) {
                return .card(view)
            }
        }
        return .words(AssistantCardText.replacingCards(source).trimmingCharacters(in: .whitespacesAndNewlines))
    }

    /// A card fence inside the words (a ```persona-card, or a ```persona-view the fence rules leave to Markdown), as the
    /// chat's Markdown theme draws it: a card, a ```persona-card in words, else its fallback or a placeholder.
    static func picture(language: String?, _ content: String) -> ShareCardPart {
        if let view = AssistantCardBlock.parse(language: language, content), drawable(view) { return .card(view) }
        if let words = AssistantCardBlock.words(language: language, content) { return .cardWords(words) }
        return .words(AssistantCardText.words(language?.lowercased() ?? "", json: content))
    }

    /// Whether a picture can draw `view` as the chat does: something on its first page to draw, and nothing that only
    /// draws live (an HTML page runs in a web view) or loads later (pictures, tiles and carousels with their images,
    /// a row's thumbnail or avatar picture). Buttons, fields and controls are drawn as they look (`extensionPicture`).
    static func drawable(_ view: ExtensionView) -> Bool {
        var something = false
        for item in view.sections.flatMap(\.items) {
            switch item {
            case .html, .image, .grid, .carousel: return false
            case .row(let row) where row.thumbnail != nil || row.avatar.map({ $0.url != nil || $0.path != nil || $0.ref != nil }) == true:
                return false
            case .unknown, .divider: continue
            default: something = true
            }
        }
        return something
    }
}

nonisolated extension ShareCardPart {
    /// How much of the picture's `ShareCardParts.maxChars` it takes: its words, a card its fallback's.
    var length: Int {
        switch self {
        case .markdown(let s), .words(let s): s.count
        case .card(let view): max(view.fallback?.count ?? 0, 1)
        case .cardWords(let w): (w.title?.count ?? 0) + w.lines.reduce(0) { $0 + $1.count }
        }
    }
}
