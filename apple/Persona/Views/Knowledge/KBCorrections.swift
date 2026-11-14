import SwiftUI

// What the user corrects on an entry by hand (docs/assistant/knowledge.md "Corrections, locks and unlinking"), as the
// web's entry page does (`ConfirmedFacts.tsx`, `Relationships.tsx`, `lib/kb-relationships.ts`): the confirmed facts
// Persona keeps as true about it, and a link to another entry that is wrong. Tags and importance are `KBUpdate`.

/// The fields of the hub's `kb.Update` a correction writes.
nonisolated struct KBCorrection: Encodable, Hashable, Sendable {
    /// nil keeps the confirmed facts; "" clears them.
    var pinned: String?
    var removePeople: [String]?
    var removeChats: [String]?
    var removeProjects: [String]?

    enum CodingKeys: String, CodingKey {
        case pinned
        case removePeople = "remove_people"
        case removeChats = "remove_chats"
        case removeProjects = "remove_projects"
    }
}

extension APIClient {
    func kbCorrect(_ kind: String, id: String, _ c: KBCorrection) async throws {
        _ = try await request("PUT", "/api/v1/kb/\(kind)/\(Self.kbID(id))", body: c)
    }
}

/// An entry's confirmed facts: what the user said is true about it. Persona's own updates read them and never change them.
nonisolated enum KBFacts {
    static func text(_ meta: JSONValue?) -> String {
        if case .string(let s)? = meta?["pinned"] { return s }
        return ""
    }

    /// The detail with these facts, shown before the hub answers; reloading replaces it with what the hub has.
    static func with(_ text: String, in response: KBDetailResponse) -> KBDetailResponse {
        var r = response
        var meta = r.detail.meta?.object ?? [:]
        meta["pinned"] = .string(text)
        r.detail.meta = .object(meta)
        return r
    }
}

/// Taking a link between two entries away. Only a link one of them holds in its own list (a project's people, chats
/// and items, an entry's projects, a person's chats, a chat's people) can go; a detail also links what it was found
/// with, which has nothing to remove. Removing it protects both lists from Persona's automatic linking.
nonisolated struct KBUnlink: Identifiable, Hashable, Sendable {
    enum List: String, Sendable {
        case people, chats, projects, items

        /// What the list is called in a sentence ("the people linked to …").
        var title: String {
            switch self {
            case .people: String(localized: "people [KB Unlink]")
            case .chats: String(localized: "chats [KB Unlink]")
            case .projects: String(localized: "projects [KB Unlink]")
            case .items: String(localized: "linked items [KB Unlink]")
            }
        }
    }

    /// One of the two entries: the id requests carry, the one its folder has (`canonicalID`), its list and the other
    /// entry as that list names it.
    struct End: Hashable, Sendable {
        var kind: String
        var id: String
        var canonicalID: String
        var name: String
        var field: List
        var member: String
    }

    var first: End
    var second: End
    /// The entry the update is written to.
    var targetKind: String
    var targetID: String
    var update: KBCorrection

    var id: String { "\(first.kind)/\(first.id)>\(second.kind)/\(second.id)" }
    var ends: [End] { [first, second] }

    private static let items: Set<String> = ["docs", "meetings", "events", "recordings"]
    private static let unsafe = try! NSRegularExpression(pattern: #"[^\p{L}\p{N}._@+-]+"#)

    /// The hub's `SafeID`, the name of an entry's folder: requests and lists still carry the id it came with.
    static func canonicalID(kind: String, id: String) -> String {
        let source = kind == KBKind.recordings.rawValue ? id.lowercased() : id
        let safe = unsafe.stringByReplacingMatches(in: source, range: NSRange(source.startIndex..., in: source), withTemplate: "_")
            .trimmingCharacters(in: CharacterSet(charactersIn: "._"))
        var out = String.UnicodeScalarView()
        var bytes = 0
        for s in safe.unicodeScalars {
            bytes += String(s).utf8.count
            if bytes > 120 { break }
            out.append(s)
        }
        return String(out)
    }

    /// How to take `linked` off the entry `detail` shows (named `name`), or nil when that link is not one to remove.
    static func removal(_ detail: KBDetail, name: String, linked: KBEntry) -> KBUnlink? {
        let a = detail, b = linked
        let aid = string(a.meta?["id"]) ?? a.id
        let af: List, bf: List
        var reverse = false
        let projects = KBKind.projects.rawValue, people = KBKind.people.rawValue, chats = KBKind.chats.rawValue
        if a.kind == projects && (b.kind == people || b.kind == chats) {
            af = List(rawValue: b.kind)!; bf = .projects
        } else if a.kind == projects && items.contains(b.kind) {
            af = .items; bf = .projects; reverse = true
        } else if b.kind == projects && (a.kind == people || a.kind == chats || items.contains(a.kind)) {
            af = .projects; bf = a.kind == people || a.kind == chats ? List(rawValue: a.kind)! : .items
        } else if a.kind == people && b.kind == chats {
            af = .chats; bf = .people
        } else if a.kind == chats && b.kind == people {
            af = .people; bf = .chats
        } else {
            return nil
        }
        let bid = canonicalID(kind: b.kind, id: b.id)
        let am = af == .items ? "\(b.kind)/\(bid)" : b.id
        let bm = bf == .items ? "\(a.kind)/\(a.id)" : aid
        guard let members = strings(a.meta?[af.rawValue]), members.contains(am) else { return nil }
        var update = KBCorrection()
        let removed = [reverse ? aid : b.id]
        switch reverse ? .projects : af {
        case .people: update.removePeople = removed
        case .chats: update.removeChats = removed
        case .projects, .items: update.removeProjects = removed
        }
        return KBUnlink(first: End(kind: a.kind, id: aid, canonicalID: a.id, name: name, field: af, member: am),
                        second: End(kind: b.kind, id: b.id, canonicalID: bid, name: b.name, field: bf, member: bm),
                        targetKind: reverse ? b.kind : a.kind, targetID: reverse ? b.id : aid, update: update)
    }

    /// The detail read back is this end's entry.
    func matches(_ detail: KBDetail?, _ end: End) -> Bool {
        detail?.kind == end.kind && detail?.id == end.canonicalID
    }

    /// Both entries read back without the other in their list, and the list protected by the user: an older hub that
    /// takes the removal without the protection, or an extension still holding the list, is not done.
    func confirmed(_ details: [KBDetail?]) -> Bool {
        guard details.count == 2 else { return false }
        return zip(ends, details).allSatisfy { end, d in
            guard let d, matches(d, end) else { return false }
            let members = d.meta?[end.field.rawValue]
            let gone: Bool
            switch members {
            case nil, .null?: gone = true
            default: gone = Self.strings(members).map { !$0.contains(end.member) } ?? false
            }
            let locked = Self.strings(d.meta?["locked_fields"])?.contains(end.field.rawValue) ?? false
            let userHeld: Bool
            switch d.meta?["provenance"] {
            case nil, .null?: userHeld = true
            case .object(let o)?: userHeld = o[end.field.rawValue] == nil
            default: userHeld = false
            }
            return gone && locked && userHeld
        }
    }

    /// The question before removing: what goes, that both entries stay, and which lists Persona stops changing.
    var question: String {
        String(localized: "Remove the link between “\(first.name)” and “\(second.name)”?")
    }

    var explanation: String {
        String(localized: "Both entries and what they hold are kept. Persona will stop changing the \(first.field.title) linked to “\(first.name)” and the \(second.field.title) linked to “\(second.name)” on its own.")
    }

    private static func string(_ v: JSONValue?) -> String? {
        if case .string(let s)? = v { return s }
        return nil
    }

    /// A list of strings only; anything else in it, or not a list, is nil.
    private static func strings(_ v: JSONValue?) -> [String]? {
        guard case .array(let a)? = v else { return nil }
        var out: [String] = []
        for x in a {
            guard case .string(let s) = x else { return nil }
            out.append(s)
        }
        return out
    }
}

/// The confirmed facts as the entry page shows them: the text, or a way to add some; Edit and Delete beside it.
struct KBFactsSection: View {
    var facts: String
    var edit: () -> Void
    var delete: () -> Void

    var body: some View {
        Section {
            if facts.isEmpty {
                Button(action: edit) {
                    Label("Add Confirmed Facts", systemImage: Theme.Icon.add).lineLimit(1)
                }
                .accessibilityIdentifier("kb.facts.add")
            } else {
                Text(facts)
                    .font(Theme.Font.body)
                    .readingContent()
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.vertical, Theme.Space.xxs)
                    .accessibilityIdentifier("kb.facts")
                    .contextMenu {
                        Button(action: edit) { Label("Edit [KB Facts]", systemImage: Theme.Icon.edit) }
                        Button(role: .destructive, action: delete) { Label("Delete", systemImage: Theme.Icon.delete) }
                    }
                    .swipeActions(edge: .trailing) {
                        Button(action: delete) { Label("Delete", systemImage: Theme.Icon.delete) }.tint(Theme.danger)
                        Button(action: edit) { Label("Edit [KB Facts]", systemImage: Theme.Icon.edit) }
                    }
                Button(action: edit) {
                    Label("Edit Confirmed Facts", systemImage: Theme.Icon.edit).lineLimit(1)
                }
                .accessibilityIdentifier("kb.facts.edit")
            }
        } header: {
            Text("Confirmed Facts")
        } footer: {
            Text("Your corrections are kept when Persona updates this entry.")
        }
    }
}

/// Writing the confirmed facts: Save sends them, an emptied text deletes them (the page offers Undo then).
struct KBFactsEditor: View {
    @Environment(\.dismiss) private var dismiss
    var name: String
    var initial: String
    /// Writes the facts; throws to keep the sheet open with the draft.
    var save: (String) async throws -> Void
    /// Deletes them: the sheet closes first, the page does it and offers Undo.
    var delete: () -> Void
    @State private var draft: String
    @State private var saving = false
    @State private var error: String?
    @FocusState private var focused: Bool

    init(name: String, initial: String, save: @escaping (String) async throws -> Void, delete: @escaping () -> Void) {
        self.name = name
        self.initial = initial
        self.save = save
        self.delete = delete
        _draft = State(initialValue: initial)
    }

    private var trimmed: String { draft.trimmingCharacters(in: .whitespacesAndNewlines) }
    private var changed: Bool { trimmed != initial.trimmingCharacters(in: .whitespacesAndNewlines) }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    TextEditor(text: $draft)
                        .font(Theme.Font.body)
                        .focused($focused)
                        .scaledFrame(minHeight: Theme.Size.box140)
                        .disabled(saving)
                        .accessibilityLabel("Confirmed Facts")
                        .accessibilityIdentifier("kb.facts.editor")
                } footer: {
                    Text("Write what Persona should always take as true about “\(name)”. Persona reads this but never changes it.")
                }
                if !initial.isEmpty {
                    Section {
                        Button(role: .destructive) {
                            dismiss()
                            delete()
                        } label: {
                            Label("Delete Confirmed Facts", systemImage: Theme.Icon.delete).lineLimit(1)
                        }
                        .disabled(saving)
                        .accessibilityIdentifier("kb.facts.delete")
                    }
                }
                if let error {
                    Section {
                        Label(String(localized: "Couldn't save. Your text is kept, try again: \(error)"), systemImage: Theme.Icon.error)
                            .foregroundStyle(Theme.danger).font(Theme.Font.caption)
                    }
                }
            }
            .navigationTitle("Confirmed Facts")
            .navigationBarTitleDisplayMode(.inline)
            .sheetCancel(unsaved: changed) { dismiss() }
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button { Task { await submit() } } label: {
                        if saving { ProgressView() } else { Text("Save").lineLimit(1) }
                    }
                    .disabled(saving || !changed)
                    .accessibilityIdentifier("kb.facts.save")
                }
            }
            .interactiveDismissDisabled(saving || changed)
            .onAppear { if initial.isEmpty { focused = true } }
        }
        .presentationDetents([.medium, .large])
        #if os(macOS)
        .macSheet()
        #endif
    }

    private func submit() async {
        guard !saving, changed else { return }
        if trimmed.isEmpty, !initial.isEmpty {
            dismiss()
            delete()
            return
        }
        saving = true
        error = nil
        do {
            try await save(trimmed)
            Haptics.success()
            dismiss()
        } catch {
            self.error = error.shown ?? error.localizedDescription
            Haptics.error()
        }
        saving = false
    }
}
