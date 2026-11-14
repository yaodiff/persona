import Foundation
import Observation
import PersonaWire

/// Settings › Knowledge Selection (docs/assistant/knowledge.md "Selection", the web's KnowledgeSelection): what the hub
/// reads and stores from chats and documents, and for how long. One document on the hub (`KBSelection`), the same on
/// every device. A change shows at once and is saved at once; a failed save puts the old value back and says why.
@Observable final class KnowledgeSelectionModel {
    struct Deps {
        var load: () async throws -> KBSelection
        var save: (KBSelection) async throws -> KBSelection
        /// The knowledge base's chats or documents, to pick one for a per-item setting.
        var entries: (KBSelectionItem.Kind) async throws -> [KBEntry]
        /// What the knowledge base keeps of one of them (`KBDetail.meta`), where its source identity is.
        var meta: (KBSelectionItem.Kind, String) async throws -> JSONValue?
    }

    /// Why the page has nothing to edit.
    nonisolated enum Unsupported: Equatable, Sendable {
        /// The hub has no `/api/v1/kb/selection` (404): it is older than the selection.
        case hub
        /// The hub's document has a version or a field this app does not know: saving it would drop them.
        case document
    }

    /// Nil until the hub answered.
    private(set) var selection: KBSelection?
    private(set) var saving = false
    /// Why the document could not be read or the last change did not save.
    private(set) var error: String?
    private(set) var unsupported: Unsupported?
    @ObservationIgnored private let deps: Deps
    /// Reads and edits are independent Tasks. Only the latest read may draw, and an edit retires every read around it.
    @ObservationIgnored private var generation = 0

    init(deps: Deps) { self.deps = deps }

    func load() async {
        generation += 1
        let read = generation
        let result: Result<KBSelection, Error>
        do {
            result = .success(try await deps.load())
        } catch {
            result = .failure(error)
        }
        guard read == generation, !saving else { return }
        do {
            selection = try result.get()
            error = nil
            unsupported = nil
        } catch let e as APIError where e.status == 404 {
            selection = nil
            unsupported = .hub
        } catch is KBSelectionUnsupported {
            selection = nil
            unsupported = .document
        } catch {
            // What is shown stays when a refresh fails; a cancelled read (the page went away) says nothing.
            if selection == nil, !error.isCancellation {
                let why = error.shown ?? String(localized: "Can't reach the hub")
                self.error = String(localized: "Couldn't load: \(why)")
            }
        }
    }

    /// Applies `edit` to the hub's document as it is now, not to the copy this page read: another device may have
    /// saved since, and a PUT replaces the whole document. `edit` therefore names rules and items, never positions.
    /// False when nothing was saved.
    @discardableResult
    func change(_ edit: (inout KBSelection) -> Void) async -> Bool {
        guard let before = selection, !saving else { return false }
        var shown = before
        edit(&shown)
        guard shown != before else { return true }
        guard shown.isValid else {
            error = String(localized: "That setting isn't valid, so nothing was saved.")
            return false
        }
        generation += 1
        selection = shown
        saving = true
        error = nil
        defer {
            saving = false
            generation += 1
        }
        do {
            let current = try await deps.load()
            var next = current
            edit(&next)
            guard next.isValid else {
                selection = current
                error = String(localized: "The selection changed on another device. Check it and try again.")
                return false
            }
            if next == current { selection = current } else { selection = try await deps.save(next) }
            return true
        } catch let e as APIError where e.status == 404 {
            selection = nil
            unsupported = .hub
            error = nil
            return false
        } catch is KBSelectionUnsupported {
            selection = nil
            unsupported = .document
            return false
        } catch {
            selection = before
            if !error.isCancellation {
                let why = error.shown ?? String(localized: "Can't reach the hub")
                self.error = String(localized: "Couldn't save: \(why)")
            }
            return false
        }
    }

    // MARK: Edits

    func setRule(_ name: String, in part: KBSelection.Part, on: Bool) async {
        await change { $0[part].rules[name]?.on = on }
    }

    /// The rule's options from its sheet. `rule` carries only what a rule of that part and name may have.
    func setRule(_ name: String, in part: KBSelection.Part, to rule: KBSelectionRule) async -> Bool {
        await change { $0[part].rules[name] = rule }
    }

    func moveLevel(of rule: String, in part: KBSelection.Part, by delta: Int) async {
        await change { $0.moveLevel(of: rule, in: part, by: delta) }
    }

    func regroup(_ rule: String, in part: KBSelection.Part, joinPrevious: Bool) async {
        await change { $0.regroup(rule, in: part, joinPrevious: joinPrevious) }
    }

    func setItem(_ item: KBSelectionItem) async -> Bool {
        await change { $0.setItem(item) }
    }

    @discardableResult
    func removeItem(_ item: KBSelectionItem) async -> Bool {
        await change { $0.removeItem(key: item.key) }
    }

    // MARK: Picking a chat or document

    func entries(_ kind: KBSelectionItem.Kind) async throws -> [KBEntry] {
        try await deps.entries(kind)
    }

    /// The per-item setting a knowledge-base entry starts with: the one it has, else read by the rules. Throws
    /// `KnowledgeSelectionPickError` when the entry cannot have one.
    func item(for entry: KBEntry, kind: KBSelectionItem.Kind) async throws -> KBSelectionItem {
        let meta = try await deps.meta(kind, entry.id)
        let item = try KnowledgeSelectionText.item(for: entry, kind: kind, meta: meta)
        return selection?.item(key: item.key) ?? item
    }
}

/// Why a knowledge-base entry cannot get a per-item setting.
nonisolated enum KnowledgeSelectionPickError: Error, Equatable, Sendable {
    /// The knowledge base does not record which source and id it came from (written before sources were recorded).
    case noIdentity
    /// A chat the selection does not decide about (mail, an issue, a pull request): it is always stored.
    case notSelectable
}

/// The page's pure parts: which entry gets which setting, what a field's text means, what a rule is called.
nonisolated enum KnowledgeSelectionText {
    /// The chat kinds the selection decides about (`kb.ChatFacts.Selected`).
    static let chatKinds: Set<String> = ["dm", "group", "bot", "official", "system"]

    /// A chat is named by its source and the source's chat id (`source_id`); a document by its source and its id.
    static func item(for entry: KBEntry, kind: KBSelectionItem.Kind, meta: JSONValue?) throws -> KBSelectionItem {
        let source = meta?["source"]?.string ?? ""
        let id = meta?[kind == .chat ? "source_id" : "id"]?.string ?? ""
        guard !source.isEmpty, !id.isEmpty else { throw KnowledgeSelectionPickError.noIdentity }
        if kind == .chat, !chatKinds.contains(meta?["kind"]?.string ?? "") { throw KnowledgeSelectionPickError.notSelectable }
        return KBSelectionItem(source: source, kind: kind, id: id, name: entry.name.isEmpty ? nil : entry.name, read: true, store: false, days: nil)
    }

    /// What a number field holds.
    nonisolated enum Number: Equatable, Sendable {
        /// Empty: the hub's default.
        case blank
        case value(Int)
        case invalid

        var value: Int? { if case .value(let n) = self { n } else { nil } }
        var isValid: Bool { self != .invalid }
    }

    /// A field of whole numbers in `range`; anything else but blank is invalid.
    static func number(_ text: String, in range: ClosedRange<Int>) -> Number {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return .blank }
        guard trimmed.allSatisfy(\.isASCII), trimmed.allSatisfy(\.isNumber), let n = Int(trimmed), range.contains(n) else { return .invalid }
        return .value(n)
    }

    static func ruleTitle(_ name: String, in part: KBSelection.Part) -> String {
        switch (part, name) {
        case (_, "mentions"): String(localized: "Mentions of You")
        case (.messages, "mine"): String(localized: "Your Messages")
        case (.messages, "dm"): String(localized: "Direct Chats")
        case (.messages, "group"): String(localized: "Group Chats")
        case (.messages, "bot"): String(localized: "Bots and Apps")
        case (.messages, "official"): String(localized: "Official Accounts")
        case (.messages, "muted"): String(localized: "Muted Chats")
        case (.messages, "collapsed"): String(localized: "Collapsed Chats")
        case (.messages, "done"): String(localized: "Done Chats")
        case (.docs, "comment_mentions"): String(localized: "Comment Mentions")
        case (.docs, "authored"): String(localized: "Documents You Wrote")
        case (.docs, "viewed"): String(localized: "Documents You Viewed")
        default: name
        }
    }

    /// What the rule matches, under its name.
    static func ruleDetail(_ name: String, in part: KBSelection.Part) -> String? {
        switch (part, name) {
        case (.messages, "mentions"): String(localized: "Messages that @mention you, in any chat")
        case (.messages, "mine"): String(localized: "What you send, with the messages around it")
        case (.messages, "dm"): String(localized: "One-to-one chats with a person")
        case (.messages, "group"): String(localized: "Groups you haven't muted, collapsed or marked done")
        case (.messages, "bot"): String(localized: "Chats with a bot, an app or an AI agent")
        case (.messages, "official"): String(localized: "Official, service and system accounts")
        case (.messages, "muted"): String(localized: "Chats you muted")
        case (.messages, "collapsed"): String(localized: "Chats you folded away")
        case (.messages, "done"): String(localized: "Chats you marked done")
        case (.docs, "mentions"): String(localized: "Documents that @mention you")
        case (.docs, "comment_mentions"): String(localized: "Documents with a comment that @mentions you")
        case (.docs, "authored"): String(localized: "Documents you created")
        case (.docs, "viewed"): String(localized: "Documents you opened")
        default: nil
        }
    }

    /// A rule's state at the row's end: Off, On, or how long it keeps what it stores.
    static func ruleValue(_ rule: KBSelectionRule) -> String {
        guard rule.on else { return String(localized: "Off") }
        guard let days = rule.days else { return String(localized: "On") }
        return String(localized: "\(days) days")
    }

    static func modeTitle(_ mode: KBSelectionItem.Mode, kind: KBSelectionItem.Kind) -> String {
        switch (mode, kind) {
        case (.skip, _): String(localized: "Don't Read")
        case (.rules, .chat): String(localized: "Only Mentions and My Messages")
        case (.rules, .doc): String(localized: "Follow the Rules")
        case (.all, _): String(localized: "Store Everything")
        }
    }

    /// What the chosen mode means, under the picker.
    static func modeDetail(_ mode: KBSelectionItem.Mode, kind: KBSelectionItem.Kind) -> String {
        switch (mode, kind) {
        case (.skip, _): String(localized: "Nothing new from it is read or stored, not even mentions of you. What is already stored stays.")
        case (.rules, .chat): String(localized: "Only what the Mentions of You and Your Messages rules keep is stored, whatever the other rules say.")
        case (.rules, .doc): String(localized: "Read and stored as the document rules decide.")
        case (.all, .chat): String(localized: "Every message is stored. Leave the days empty to keep them as long as the deciding rule or the hub's text retention says.")
        case (.all, .doc): String(localized: "The document is stored whatever the rules say. Leave the days empty to add no deletion date.")
        }
    }

    /// A per-item setting's state at the row's end.
    static func itemValue(_ item: KBSelectionItem) -> String {
        if item.mode == .all, let days = item.days { return String(localized: "\(days) days") }
        switch item.mode {
        case .skip: return String(localized: "Not Read")
        case .rules: return item.kind == .chat ? String(localized: "Mentions and Mine") : String(localized: "By Rules")
        case .all: return String(localized: "Everything")
        }
    }

    static func pickError(_ error: Error) -> String {
        switch error as? KnowledgeSelectionPickError {
        case .noIdentity: String(localized: "The knowledge base doesn't record where this one came from, so it can't get a setting of its own.")
        case .notSelectable: String(localized: "Mail, issues and pull requests are always stored. The selection doesn't apply to them.")
        case nil: error.shown ?? String(localized: "Can't reach the hub")
        }
    }
}
