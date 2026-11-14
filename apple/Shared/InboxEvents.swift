import Foundation

// Inbox phase 1 (docs/assistant/inbox.md): an inbox item grows into an event — the manager's processing conversation
// (`handling`), at most one suggestion to confirm, and the chain (`chain`). Mirrors the hub's inbox item fields; every
// one is optional, an older hub sends none and the app draws what it drew before.

extension InboxItem {
    /// The assistant's own row (#6430, source `assistant`): the manager puts a conversation into the Inbox itself.
    var isAssistantRow: Bool { source == "assistant" }

    /// The row's text repeats the summary under it (the assistant's own rows: their text is the summary), so it is drawn once.
    var repeatsSummary: Bool {
        guard isAssistantRow, let summary = summary?.trimmingCharacters(in: .whitespacesAndNewlines), !summary.isEmpty else { return false }
        let text = preview.trimmingCharacters(in: .whitespacesAndNewlines)
        return !text.isEmpty && text.hasPrefix(summary)
    }

    /// The assistant's own row whose text is only its title and summary, already shown above: no Content section.
    func onlyTitleAndSummary(_ context: String?) -> Bool {
        guard isAssistantRow, let summary, !summary.isEmpty else { return false }
        func flat(_ s: String) -> String { s.split(whereSeparator: \.isWhitespace).joined(separator: " ") }
        let text = flat(context ?? preview)
        return text.isEmpty || text == flat(title + " " + summary) || text == flat(summary)
    }

    /// The processing conversation bound to the item.
    nonisolated struct Handling: Codable, Hashable, Sendable {
        var owner: String?
        /// looking | agent | waiting | done | failed (anything else: drawn as nothing).
        var state: String
        var machineID: String?
        var sessionID: String?
        var since: Date?
        /// failed only: the hub's sentence.
        var error: String?

        enum CodingKeys: String, CodingKey {
            case owner, state, since, error
            case machineID = "machine_id", sessionID = "session_id"
        }

        var sessionKey: SessionKey? { SessionKey.make(machineID, sessionID) }
    }

    /// The current suggestion; `version` goes back with the answer so a stale one is not run.
    nonisolated struct Suggestion: Codable, Hashable, Sendable {
        var version: Int
        /// reply | agent | conclusion
        var kind: String
        var text: String
        var note: String?
        /// A to-do suggestion's due: a day (2006-01-02, all day) or a moment (RFC 3339); nil: no due date.
        var due: String?
        /// Needs judgment: confirming hands it back to the processing conversation instead of running it.
        var handback: Bool?
        var machineID: String?
        var changes: [DocChange]?
        var at: Date?
        /// The conversation the item was handed to that handed it in (Hand to AI); nil for the item's own AI twin.
        var conversation: Handoff?

        enum CodingKeys: String, CodingKey {
            case version, kind, text, note, due, handback, changes, at, conversation
            case machineID = "machine_id"
        }

        var isHandback: Bool { handback == true }

        /// Version zero addresses an item with no current suggestion, for an instruction outside the suggestion card.
        static var withoutSuggestion: Self { Self(version: 0, kind: "", text: "") }
    }

    /// One key call of the chain. Decoded leniently: a field of an unexpected shape is left out rather than failing
    /// the whole inbox, and an unknown `kind` is drawn by its `text`.
    nonisolated struct ChainEntry: Codable, Hashable, Sendable, Identifiable {
        var id: String
        var at: Date?
        var kind: String
        var text: String
        /// Who or what did this step: a source id, `assistant`, `agent`, or `user`; nil on an older hub.
        var via: String?
        var edited: Bool?
        var detail: String?
        var count: Int?
        var sender: String?
        var messages: [Message]?
        var link: Link?

        nonisolated struct Message: Codable, Hashable, Sendable {
            var sender: String?
            var text: String
            var at: Date?
        }

        init(id: String, at: Date? = nil, kind: String, text: String, via: String? = nil, edited: Bool? = nil, detail: String? = nil,
             count: Int? = nil, sender: String? = nil, messages: [Message]? = nil, link: Link? = nil) {
            self.id = id; self.at = at; self.kind = kind; self.text = text; self.via = via; self.edited = edited; self.detail = detail
            self.count = count; self.sender = sender; self.messages = messages; self.link = link
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            at = try? c.decodeIfPresent(Date.self, forKey: .at)
            kind = (try? c.decodeIfPresent(String.self, forKey: .kind)) ?? ""
            text = (try? c.decodeIfPresent(String.self, forKey: .text)) ?? ""
            via = (try? c.decodeIfPresent(String.self, forKey: .via)).flatMap { $0.isEmpty ? nil : $0 }
            id = (try? c.decodeIfPresent(String.self, forKey: .id)) ?? "\(kind)-\(at?.timeIntervalSince1970 ?? 0)"
            edited = try? c.decodeIfPresent(Bool.self, forKey: .edited)
            detail = try? c.decodeIfPresent(String.self, forKey: .detail)
            count = try? c.decodeIfPresent(Int.self, forKey: .count)
            sender = try? c.decodeIfPresent(String.self, forKey: .sender)
            messages = try? c.decodeIfPresent([Message].self, forKey: .messages)
            link = try? c.decodeIfPresent(Link.self, forKey: .link)
        }

        enum CodingKeys: String, CodingKey { case id, at, kind, text, via, edited, detail, count, sender, messages, link }

        /// A reported entry's detail is already its row's words, so it is not also expanded below the row.
        var detailIsInLine: Bool { kind == "reported" && !(detail ?? "").isEmpty }

        /// What opens inline when there is no link: the reason, the words, the batch's messages.
        var expandable: Bool {
            !(messages ?? []).isEmpty || (!(detail ?? "").isEmpty && !detailIsInLine)
        }
    }

    /// Where tapping a chain entry goes: the item's source (at a Feishu position, a mail uid, a URL, or just the source
    /// page), a conversation, or another inbox item.
    nonisolated struct Link: Codable, Hashable, Sendable {
        /// source | session | item | todo
        var type: String
        var position: String?
        var uid: UInt32?
        var url: String?
        var machineID: String?
        var sessionID: String?
        var id: String?

        init(type: String, position: String? = nil, uid: UInt32? = nil, url: String? = nil,
             machineID: String? = nil, sessionID: String? = nil, id: String? = nil) {
            self.type = type; self.position = position; self.uid = uid; self.url = url
            self.machineID = machineID; self.sessionID = sessionID; self.id = id
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            type = try c.decode(String.self, forKey: .type)
            // A position is a string on the wire; a number is taken too.
            position = (try? c.decodeIfPresent(String.self, forKey: .position)) ?? (try? c.decodeIfPresent(Int64.self, forKey: .position)).map { String($0) }
            uid = try? c.decodeIfPresent(UInt32.self, forKey: .uid)
            url = try? c.decodeIfPresent(String.self, forKey: .url)
            machineID = try? c.decodeIfPresent(String.self, forKey: .machineID)
            sessionID = try? c.decodeIfPresent(String.self, forKey: .sessionID)
            id = try? c.decodeIfPresent(String.self, forKey: .id)
        }

        enum CodingKeys: String, CodingKey {
            case type, position, uid, url, id
            case machineID = "machine_id", sessionID = "session_id"
        }

        var sessionKey: SessionKey? { type == "session" ? SessionKey.make(machineID, sessionID) : nil }
    }

    /// Another item this one has to do with (another chat, another source): opened as itself.
    nonisolated struct Related: Codable, Hashable, Sendable, Identifiable {
        var id: String
        var title: String
        var source: String?
    }

    /// The suggestion to show, when it has words.
    nonisolated var currentSuggestion: Suggestion? {
        guard let s = suggestion, !s.text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return nil }
        return s
    }

    /// The processing fields or a shared state key make this an event; without them the detail is today's.
    nonisolated var isEvent: Bool {
        now != nil || handling != nil || suggestion != nil || !(chain ?? []).isEmpty || !(recap ?? "").isEmpty
    }
}

extension SessionKey {
    /// A key when both halves are there.
    nonisolated static func make(_ machineID: String?, _ sessionID: String?) -> SessionKey? {
        guard let m = machineID, let s = sessionID, !m.isEmpty, !s.isEmpty else { return nil }
        return SessionKey(machineID: m, sessionID: s)
    }
}

/// The words and marks of inbox phase 1, as every screen draws them (the list row, the detail, the Mac's column).
nonisolated enum InboxEventText {
    /// What a row says under its summary while the item is open.
    enum Mark: Equatable {
        /// Urgent, the manager still at it, no suggestion yet: the 'preparing a suggestion' line.
        case preparing
        /// In Progress: AI looking / Agent working / waiting for a reply.
        case working(String)
        /// The suggestion's kind and its first line.
        case suggestion(kind: String, line: String, symbol: String)
        /// The manager failed: back in Needs You with no suggestion.
        case failed(String)
    }

    static func mark(_ it: InboxItem) -> Mark? {
        guard !it.isDone else { return nil }
        if let s = it.currentSuggestion { return .suggestion(kind: kindTitle(s), line: firstLine(s.text), symbol: symbol(s)) }
        let state = it.handling?.state
        if state == "failed" { return .failed(String(localized: "AI couldn't handle it")) }
        if it.urgent == true, state == "looking" { return .preparing }
        if it.triage == .working, let word = stateWord(state) { return .working(word) }
        return nil
    }

    /// Who filed a done item, where it is told: 「助手处理了」 for the manager (it read it, nothing to do); nil otherwise.
    static func doneBy(_ it: InboxItem) -> String? {
        it.isDone && it.doneBy == "manager" ? String(localized: "Handled by the assistant") : nil
    }

    /// The handling state for people; nil when there is nothing to say (done, unknown).
    static func stateWord(_ state: String?) -> String? {
        switch state {
        case "looking": String(localized: "AI is looking")
        case "agent": String(localized: "Agent working")
        case "waiting": String(localized: "Waiting for a reply")
        case "failed": String(localized: "AI couldn't handle it")
        default: nil
        }
    }

    /// suggested reply / suggested Agent / conclusion / your call (a handback).
    static func kindTitle(_ s: InboxItem.Suggestion) -> String {
        if s.isHandback { return String(localized: "Your call") }
        return switch s.kind {
        case "reply": String(localized: "Suggested reply")
        case "agent": String(localized: "Suggested Agent task")
        case "conclusion": String(localized: "Conclusion")
        case "todo": String(localized: "Suggested To-Do")
        default: String(localized: "Suggestion")
        }
    }

    static func symbol(_ s: InboxItem.Suggestion) -> String {
        if s.isHandback { return Theme.Icon.askFirst }
        return switch s.kind {
        case "reply": Theme.Icon.reply
        case "agent": Theme.Icon.agents
        case "conclusion": Theme.Icon.concluded
        case "todo": Theme.Icon.todo
        default: Theme.Icon.ai
        }
    }

    /// The first non-empty line.
    static func firstLine(_ text: String) -> String {
        text.split(whereSeparator: \.isNewline).map { $0.trimmingCharacters(in: .whitespaces) }.first { !$0.isEmpty } ?? ""
    }

    /// The timeline's shared icon meaning (spec/fixtures/inbox/timeline.json). A source id stays `source` here;
    /// the view draws that source's own brand or generic source mark.
    static func iconMeaning(_ e: InboxItem.ChainEntry) -> String {
        if e.kind == "reported" { return "reported" }
        if let via = e.via, !via.isEmpty {
            switch via {
            case "assistant": return "persona"
            case "agent": return "agents"
            case "user": return "person"
            default: return "source"
            }
        }
        switch e.kind {
        case "received": return "received"
        case "triaged": return "triaged"
        case "handling": return "persona"
        case "sent": return "sendMail"
        case "agent": return "agents"
        case "confirmed": return "success"
        case "rejected": return "rejected"
        case "instructed": return "instructed"
        case "self_replied": return "reply"
        case "failed": return "error"
        case "concluded": return "concluded"
        case "done": return "done"
        default: return "dot"
        }
    }

    /// A chain entry's symbol. Source brands are chosen by the view; this is their generic fallback and every other meaning.
    static func symbol(_ e: InboxItem.ChainEntry) -> String {
        switch iconMeaning(e) {
        case "received": Theme.Icon.received
        case "triaged": Theme.Icon.triaged
        case "persona": Theme.Icon.persona
        case "sendMail": Theme.Icon.sendMail
        case "agents": Theme.Icon.agents
        case "success": Theme.Icon.success
        case "rejected": Theme.Icon.rejected
        case "instructed": Theme.Icon.instructed
        case "reply": Theme.Icon.reply
        case "error": Theme.Icon.error
        case "concluded": Theme.Icon.concluded
        case "done": Theme.Icon.done
        case "person": Theme.Icon.person
        case "reported": Theme.Icon.reported
        case "source":
            switch e.via {
            case "mail": Theme.Icon.mailFilled
            case "chat": Theme.Icon.conversation
            case "imessage": Theme.Icon.messagesApp
            default: Theme.Icon.connectors
            }
        default: Theme.Icon.dot
        }
    }

    /// The words of a chain row. A reported entry's server text already says "Assistant says" for older apps;
    /// the new app's label carries that meaning, so it shows the original words from detail instead.
    static func chainText(_ e: InboxItem.ChainEntry) -> String {
        if e.detailIsInLine, let detail = e.detail { return detail }
        return e.text.isEmpty ? e.kind : e.text
    }

    /// Detail to repeat in the expansion; reported detail is already in the row and is not repeated there.
    static func expansionDetail(_ e: InboxItem.ChainEntry) -> String? {
        guard !e.detailIsInLine, let detail = e.detail, !detail.isEmpty else { return nil }
        return detail
    }

    /// Folded, the timeline keeps its three newest entries in their original order; expanded, it keeps every entry.
    static func chainRows(_ chain: [InboxItem.ChainEntry], expanded: Bool) -> [InboxItem.ChainEntry] {
        expanded ? chain : Array(chain.suffix(3))
    }

    static var assistantSays: String { String(localized: "Assistant says") }
    static var saySomething: String { String(localized: "Say something") }
    static var tryAgain: String { String(localized: "Try again [Inbox Detail]") }
    static var retryInstruction: String { String(localized: "Please try again.") }

    /// The suggestion card's buttons, in order: the main one first.
    enum Action: Equatable {
        case confirm(String)
        case edit
        case reject
    }

    static func actions(_ s: InboxItem.Suggestion) -> [Action] {
        if s.isHandback { return [.confirm(String(localized: "Confirm")), .edit, .reject] }
        switch s.kind {
        case "reply": return [.confirm(String(localized: "Send")), .edit, .reject]
        case "agent": return [.confirm(String(localized: "Start")), .edit, .reject]
        case "conclusion": return [.confirm(String(localized: "OK [Recordings Section de32e2]")), .reject]
        case "todo": return [.confirm(String(localized: "Add to To-Dos")), .reject]
        default: return [.confirm(String(localized: "Confirm")), .reject]
        }
    }
}

/// `POST /api/v1/inbox/{id}/suggestion`: confirm (optionally with changed words), reject, or instruct (say a word).
nonisolated struct InboxSuggestionBody: Encodable, Sendable, Equatable {
    var version: Int
    /// confirm | reject | instruct
    var action: String
    var text: String?
    /// A to-do suggestion confirmed: its due as the card shows it (a day, or an RFC 3339 moment).
    var due: String? = nil
    var clientID: String

    enum CodingKeys: String, CodingKey {
        case version, action, text, due
        case clientID = "client_id"
    }
}

extension APIClient {
    /// Acts on the item's suggestion; the hub answers the item as it is now. A 409 `suggestion_stale` carries the item
    /// with the newer suggestion (`InboxSuggestionOutcome.of`).
    func inboxSuggestion(_ id: String, _ body: InboxSuggestionBody) async throws -> InboxItem {
        try await post("/api/v1/inbox/" + id + "/suggestion", body: body, timeout: 60, as: InboxItem.self)
    }
}

/// What came of acting on a suggestion, for the card to draw.
nonisolated enum InboxSuggestionOutcome: Equatable, Sendable {
    /// Done: the item as the hub has it now.
    case done(InboxItem)
    /// Another suggestion came meanwhile (409 suggestion_stale): nothing ran; the item with the new one when the hub sent it.
    case stale(InboxItem?)
    /// Not done; the words to show, and the item when the hub sent it (409 inbox_item_done).
    case failed(String, InboxItem?)

    static let staleCode = "suggestion_stale"

    /// The outcome of a failed request.
    static func of(_ error: Error) -> InboxSuggestionOutcome {
        guard let e = error as? APIError else { return .failed(error.localizedDescription, nil) }
        let item = e.status == 409 ? embeddedItem(e.details) : nil
        if e.status == 409 && e.code == staleCode { return .stale(item) }
        return .failed(e.message, item)
    }

    /// The `item` of an error's body.
    static func embeddedItem(_ details: JSONValue?) -> InboxItem? {
        guard case .object(let obj)? = details, let raw = obj["item"], let data = try? JSONEncoder().encode(raw) else { return nil }
        return try? JSON.decoder.decode(InboxItem.self, from: data)
    }
}

/// Where tapping a chain entry goes (docs/assistant/inbox.md).
nonisolated enum InboxChainTarget: Equatable, Sendable {
    /// The item's source page: a Feishu chat at `position`, the mail `uid`, or just as the row opens it.
    case source(position: String?, uid: UInt32?)
    /// A connector's page, in the in-app browser.
    case url(URL)
    /// A conversation: the processing one (`manager`, the assistant's) or an Agent's sub-session.
    case session(SessionKey, manager: Bool)
    /// Another inbox item.
    case item(String)
    /// The to-do the row was turned into (#6480).
    case todo(String)
    /// No link: the entry's detail / messages open in place.
    case inline
    /// Nothing to open.
    case nothing

    /// `sourceShown`: the source is already on screen beside the chain (the Mac's detail), so a link to just the
    /// source (no position, no uid) opens nothing more.
    static func of(_ e: InboxItem.ChainEntry, sourceShown: Bool = false) -> InboxChainTarget {
        let here: InboxChainTarget = e.expandable ? .inline : .nothing
        guard let l = e.link else { return here }
        switch l.type {
        case "source":
            if let s = l.url, !s.isEmpty, let u = URL(string: s) { return .url(u) }
            let position = l.position.flatMap { $0.isEmpty ? nil : $0 }
            if position != nil || l.uid != nil { return .source(position: position, uid: l.uid) }
            return sourceShown ? here : .source(position: nil, uid: nil)
        case "session":
            guard let key = l.sessionKey else { return here }
            return .session(key, manager: e.kind == "handling")
        case "item":
            guard let id = l.id, !id.isEmpty else { return here }
            return .item(id)
        case "todo":
            guard let id = l.id, !id.isEmpty else { return here }
            return .todo(id)
        default:
            return here
        }
    }

    /// It leaves the chain (a page, a conversation), rather than opening in place.
    var navigates: Bool {
        switch self {
        case .inline, .nothing: false
        default: true
        }
    }
}
