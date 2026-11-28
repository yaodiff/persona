import CryptoKit
import Foundation
import PersonaKit
import PersonaWire

/// Display copies never replace the cached wire items or their original words.
nonisolated enum BotmuxPresentation {
    private static let digests = TextMemo<String>()
    static func user(_ item: Item) -> Item {
        guard let message = item.botmux, message.isInbound else { return item }
        var shown = item
        shown.id = contentID(item.id, message)
        shown.text = message.text
        shown.fullSize = nil
        shown.injected = nil
        shown.source = nil
        shown.clientMsgID = nil
        return shown
    }

    /// A separate memo identity prevents Original / Decoded switches and late metadata updates reusing old Markdown.
    static func contentID(_ id: String, _ message: BotmuxMessage) -> String {
        let digest = digests(message.text) { text in
            SHA256.hash(data: Data(text.utf8)).prefix(12).map { String(format: "%02x", $0) }.joined()
        }
        return id + "#botmux-" + message.kind + "-" + digest
    }

    static func reply(_ message: BotmuxMessage, from item: Item, index: Int = 0) -> Item {
        Item(id: contentID(item.id + "/reply-\(index)", message), seq: item.seq, ts: item.ts,
             role: "assistant", kind: .text, text: message.text, botmux: message)
    }

    /// A running, failed or stopped send is not a delivered reply. Fold metadata contains only successful sends.
    static func replies(_ rows: [ChatRow]) -> [Item] {
        rows.flatMap { row -> [Item] in
            guard case .tools(let steps) = row else { return [] }
            return steps.flatMap { step -> [Item] in
                switch step {
                case .call(let call):
                    guard let message = call.use.botmux, message.kind == "reply",
                          call.result != nil, !call.isError, call.outcome?.stopped != true else { return [] }
                    return [reply(message, from: call.use)]
                case .folded(let item):
                    return (item.fold?.botmux ?? []).enumerated().compactMap { index, message in
                        message.kind == "reply" ? reply(message, from: item, index: index) : nil
                    }
                case .thought: return []
                }
            }
        }
    }
}
