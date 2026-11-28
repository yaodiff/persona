import Foundation
import Observation
import PersonaWire
import SwiftUI

// An Inbox item's knowledge base chat (#15356, docs/assistant/knowledge.md "A chat's Inbox items"), as the web's
// InboxKBChat.tsx: the item's own read (GET /api/v1/inbox/{id}) names it as `kb_chat`, and the item's More menu gets
// Knowledge Page, which opens that chat's page as a link to it does. Asked once per item a detail shows; an item with
// no chat, of another assistant than the knowledge pages read, or from a hub before it shows nothing.

/// The knowledge base chat an Inbox item came from, as its read named it: `id` and its page's path.
nonisolated struct InboxKBChat: Decodable, Hashable, Sendable {
    var id: String
    var path: String?
}

extension APIClient {
    /// The knowledge base chat item `id` came from (`kb_chat` of GET /api/v1/inbox/{id}): its id, nil when it has none,
    /// or the hub is from before it.
    func inboxKBChat(_ id: String) async throws -> String? {
        struct Read: Decodable {
            var kbChat: InboxKBChat?
            enum CodingKeys: String, CodingKey { case kbChat = "kb_chat" }
        }
        let read: Read = try await get("/api/v1/inbox/" + id)
        return read.kbChat.flatMap { $0.id.isEmpty ? nil : $0.id }
    }
}

/// Each Inbox item's knowledge base chat, by the item's key (`InboxItem.key`), as its read said: asked once per item and
/// kept for the run; a read that failed is asked again the next time the item opens.
@MainActor @Observable
final class InboxKBChats {
    static let shared = InboxKBChats { item in
        // Not connected: nothing is known, and the next opening asks again.
        guard let api = AppModel.shared.api?.manager(AppModel.shared.itemManager(item.manager)) else { throw CancellationError() }
        return try await api.inboxKBChat(item.id)
    }

    /// Item key → its chat's id, "" for none.
    private(set) var chats: [String: String] = [:]
    @ObservationIgnored private var asking: Set<String> = []
    private let read: @MainActor (InboxItem) async throws -> String?

    /// `read`: the item's chat, as its read names it (nil: none).
    init(read: @escaping @MainActor (InboxItem) async throws -> String?) {
        self.read = read
    }

    /// Only an item of a chat, a mail correspondent or a connector's thread can have one (the others are not asked).
    nonisolated static func asks(_ item: InboxItem) -> Bool { item.lark != nil || item.mail != nil || item.connector != nil }

    /// The chat item came from, once its read said so; nil for none, or not said yet.
    func chat(of item: InboxItem) -> String? {
        guard let id = chats[item.key], !id.isEmpty else { return nil }
        return id
    }

    /// The stamp the Inbox list's copy of item `id` carries (`InboxItem.manager`, what its key is made of), the copy of the
    /// assistant whose knowledge base listed it (`manager`, nil: the default); `manager` when the list has no copy.
    /// `owner` says whose a stamp is.
    nonisolated static func listedManager(of id: String, manager: String?, items: [InboxItem], owner: (String?) -> String) -> String? {
        let whose = owner(manager)
        return items.first { $0.id == id && owner($0.manager) == whose }?.manager ?? manager
    }

    /// Reads which chat item came from, unless that is known or being asked.
    func load(_ item: InboxItem) async {
        let key = item.key
        guard Self.asks(item), chats[key] == nil, !asking.contains(key) else { return }
        asking.insert(key)
        defer { asking.remove(key) }
        do {
            chats[key] = try await read(item) ?? ""
        } catch {
            // Not known: the next time the item opens asks again.
        }
    }
}

/// Knowledge Page in an opened item's More menu: its chat's page in the knowledge base, which is the current assistant's.
/// Nothing for an item whose read names no chat, or of another assistant.
struct InboxKBChatButton: View {
    var item: InboxItem
    @Environment(AppModel.self) private var model

    var body: some View {
        if let chat = InboxKBChats.shared.chat(of: item), model.assistantScope.has(item.manager) {
            Button { model.openKBChat(chat) } label: { Label("Knowledge Page", systemImage: Theme.Icon.knowledge) }
                .accessibilityIdentifier("inbox.detail.kbChat")
        }
    }
}

extension AppModel {
    /// Opens Inbox item `id` of the assistant whose knowledge base listed it (`manager`, as its page's client names it)
    /// as a link to the item does (`/inbox/<id>`): the Inbox, with the item open once it is listed. The link names the
    /// list's own stamp of the item, so the Inbox finds it by its key.
    func openKBInboxItem(_ id: String, manager: String?) {
        guard let group = hub?.group else { return }
        let roster = managerRoster
        let stamp = InboxKBChats.listedManager(of: id, manager: manager, items: InboxStore.shared.items) { roster?.owner($0) ?? "" }
        #if os(iOS)
        openNavigationLink(.inboxItem(id: id, manager: stamp), group: group)
        #else
        _ = openHubPage(.inboxItem(id: id, manager: stamp), group: group)
        #endif
    }

    /// Opens knowledge base chat `id`'s page over the screen, as a link to it does (`/kb/chats/<id>`).
    func openKBChat(_ id: String) {
        guard let group = hub?.group, case .sheet(let page)? = openHubPage(.knowledgeEntry(kind: KBKind.chats.rawValue, id: id), group: group)
        else { return }
        linkSheet = page
    }
}
