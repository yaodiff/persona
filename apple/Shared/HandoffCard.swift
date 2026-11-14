import Foundation
import PersonaWire

// A conversation an inbox item was handed to writes back to it (inbox_suggest with item): the reply or conclusion is
// also a card in that conversation (#5932, docs/assistant/inbox.md "Item actions"). The card is the suggestion as it
// was handed in; the item says whether it can still be sent and how it ended, so nothing is stored on the card.
// Mirrors web/src/lib/handoff-card.ts.

/// The card's body (assistant/inbox_handoff_card.go `handoffCard`).
nonisolated struct HandoffCardBody: Codable, Hashable, Sendable {
    var version: Int
    /// reply | conclusion
    var kind: String
    /// A conclusion's: fyi (worth knowing; OK files it) or done (filed at once).
    var outcome: String?
    var text: String
    var note: String?
    var recap: String?
    var title: String
    var source: String
    var account: String?
    var sender: String?

    var isReply: Bool { kind == "reply" }

    init(version: Int, kind: String, outcome: String? = nil, text: String, note: String? = nil, recap: String? = nil,
         title: String, source: String, account: String? = nil, sender: String? = nil) {
        self.version = version; self.kind = kind; self.outcome = outcome; self.text = text; self.note = note
        self.recap = recap; self.title = title; self.source = source; self.account = account; self.sender = sender
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        version = try c.decode(Int.self, forKey: .version)
        text = try c.decode(String.self, forKey: .text)
        kind = try c.decodeIfPresent(String.self, forKey: .kind) ?? "reply"
        outcome = try c.decodeIfPresent(String.self, forKey: .outcome)
        note = try c.decodeIfPresent(String.self, forKey: .note)
        recap = try c.decodeIfPresent(String.self, forKey: .recap)
        title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
        source = try c.decodeIfPresent(String.self, forKey: .source) ?? ""
        account = try c.decodeIfPresent(String.self, forKey: .account)
        sender = try c.decodeIfPresent(String.self, forKey: .sender)
    }
}

/// Where a hand-off card stands, from the item as the inbox has it now.
nonisolated enum HandoffCardState: Hashable, Sendable {
    /// The item's suggestion is still this one from this conversation: Send / OK works.
    case live
    /// Persona sent a reply on the user's confirmation: what went out.
    case sent(String)
    /// The user answered at the source themselves.
    case selfReplied
    /// Handled without this reply going out (OK on a conclusion, Done, read elsewhere, filed by the AI).
    case filed
    /// The conversation handed in a newer suggestion, or the user turned this one down.
    case replaced
    /// The item is not here (yet).
    case unknown

    /// `newest`: no later card about the item in this conversation (only the newest one says how the item ended).
    static func of(_ card: HandoffCardBody, conversation: String, item: InboxItem?, newest: Bool) -> HandoffCardState {
        guard let item else { return .unknown }
        if item.isDone {
            guard newest else { return .replaced }
            if item.doneBy == "replied" {
                let last = item.chain?.last { $0.kind == "confirmed" || $0.kind == "self_replied" }
                if last?.kind == "self_replied" { return .selfReplied }
                let detail = last?.detail ?? ""
                return .sent(detail.isEmpty ? card.text : detail)
            }
            return .filed
        }
        if let s = item.suggestion, s.version == card.version, s.conversation?.sessionID == conversation { return .live }
        return .replaced
    }
}

nonisolated enum HandoffWrapUp {
    /// How many "Wrap up" chips a conversation shows at most, newest hand-off first.
    static let max = 2

    /// The open items handed to conversation `sessionID`, newest hand-off first: each gets a "Wrap up" chip over the
    /// main conversation's composer.
    static func items(_ items: [InboxItem], sessionID: String) -> [InboxItem] {
        func at(_ it: InboxItem) -> Date? {
            guard let h = it.handoffs?.last(where: { $0.sessionID == sessionID }) else { return nil }
            return h.at ?? .distantPast
        }
        return items.compactMap { it in it.isDone ? nil : at(it).map { (it, $0) } }
            .sorted { $0.1 > $1.1 }
            .prefix(max)
            .map(\.0)
    }
}

extension FeedItem {
    /// `inbox_suggestion`: the hand-off card's body; nil for another kind or a body this build can't read.
    nonisolated var handoffCard: HandoffCardBody? {
        guard kind == .inboxSuggestion, let body else { return nil }
        return try? JSON.decoder.decode(HandoffCardBody.self, from: JSONEncoder().encode(body))
    }
}
