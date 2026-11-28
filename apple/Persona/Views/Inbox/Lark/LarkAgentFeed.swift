import Foundation
import Observation

/// One row of the Messages page: a Feishu chat or a personal Agent (web `modules/lark/mixed-feed.ts`).
nonisolated enum LarkFeedRow: Identifiable, Hashable, Sendable {
    case chat(LarkConversation)
    case agent(PersonalAgentSession)

    /// A chat's id as it is, an Agent's under its own prefix, so the two never meet in a `ForEach` or a scroll anchor.
    var id: String {
        switch self {
        case .chat(let c): c.id
        case .agent(let s): LarkAgentFeed.key(s.id)
        }
    }

    var unread: Int {
        switch self {
        case .chat(let c): c.badge
        case .agent(let s): s.unreadCount
        }
    }
}

/// The personal Agents on the Messages page (docs/extensions/lark-migration.md "Personal Agents in Messages"): listed
/// with the Feishu chats, the latest message first, without moving a Feishu row. While Feishu has a next page, an
/// Agent older than the oldest chat loaded waits for it: a later page may hold newer chats, and the Agent would jump up
/// past them. A search lists every match. The pinned Agents (`PersonalAgentPrefs.pins`) sit on the shelf with the
/// pinned chats and stay in the list, as pinned chats do here.
nonisolated enum LarkAgentFeed {
    static func key(_ id: String) -> String { "agent:" + id }

    private static func time(_ s: PersonalAgentSession) -> Int64 { max(0, s.rankTimeMs) }
    private static func time(_ c: LarkConversation) -> Int64 {
        guard let at = c.updatedAt else { return 0 }
        return max(0, Int64((at.timeIntervalSince1970 * 1000).rounded()))
    }

    /// Newest first, ties by id so the order holds across refreshes.
    static func newest(_ agents: [PersonalAgentSession]) -> [PersonalAgentSession] {
        agents.sorted { time($0) != time($1) ? time($0) > time($1) : $0.id < $1.id }
    }

    /// `chats` in their own order with each Agent put before the first chat older than it.
    static func interleave(_ chats: [LarkConversation], _ agents: [PersonalAgentSession]) -> [LarkFeedRow] {
        let rest = newest(agents)
        var out: [LarkFeedRow] = []
        out.reserveCapacity(chats.count + rest.count)
        var next = rest.startIndex
        for c in chats {
            let at = time(c)
            while next < rest.endIndex, time(rest[next]) > at { out.append(.agent(rest[next])); next += 1 }
            out.append(.chat(c))
        }
        out.append(contentsOf: rest[next...].map(LarkFeedRow.agent))
        return out
    }

    /// Whether an Agent matches a search: its name, its service, its latest message.
    static func matches(_ s: PersonalAgentSession, _ query: String) -> Bool {
        let q = query.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !q.isEmpty else { return true }
        return [s.title, s.provider, PersonalAgentArrange.providerLabel(s.provider), s.previewText ?? ""]
            .contains { $0.localizedStandardContains(q) }
    }

    /// The Agents the list shows under the loaded Feishu pages: those not older than the oldest chat loaded while there
    /// is a next page (none before the first page, none without a time), every one once there is none.
    static func shown(_ agents: [PersonalAgentSession], loaded chats: [LarkConversation], more: Bool) -> [PersonalAgentSession] {
        guard more else { return agents }
        guard !chats.isEmpty else { return [] }
        let oldest = chats.map(time).min() ?? 0
        return agents.filter { time($0) > 0 && time($0) >= oldest }
    }

    /// The page's list. `chats` is what the page lists of Feishu (`LarkConversationsModel.listed`), `loaded` the chats
    /// the loaded pages hold; `agentsOnly` lists the Agents alone, `unreadOnly` those with something unread.
    static func rows(chats: [LarkConversation], loaded: [LarkConversation], more: Bool, agents: [PersonalAgentSession],
                     query: String, agentsOnly: Bool, unreadOnly: Bool) -> [LarkFeedRow] {
        let searching = !query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        let picked = agents.filter { matches($0, query) && (!unreadOnly || $0.unreadCount > 0) }
        if agentsOnly { return newest(picked).map(LarkFeedRow.agent) }
        return interleave(chats, searching ? picked : shown(picked, loaded: loaded, more: more))
    }

    /// The shelf: the pinned chats in the gateway's order with the pinned Agents put in by their latest message.
    static func shelf(chats: [LarkConversation], agents: [PersonalAgentSession], pins: [String], agentsOnly: Bool) -> [LarkFeedRow] {
        let byID = Dictionary(agents.map { ($0.id, $0) }, uniquingKeysWith: { a, _ in a })
        let pinned = pins.compactMap { byID[$0] }
        return interleave(agentsOnly ? [] : chats, pinned)
    }

    /// The second line of an Agent's row: its latest message on one line, "Me: …" for the user's own; nil without one.
    static func preview(_ s: PersonalAgentSession) -> String? {
        let text = (s.previewText ?? "").split(whereSeparator: \.isWhitespace).joined(separator: " ")
        guard !text.isEmpty else { return nil }
        return s.previewIsMine == true ? String(localized: "Me: \(text)", comment: "A personal Agent's latest message, sent by the user, in the Messages list") : text
    }
}

/// The personal Agents the Messages page lists: the hub's list (Removed ones left out), read again on its `agents`
/// events. A failed read keeps the last list.
@MainActor @Observable final class LarkAgentFeedModel {
    private(set) var sessions: [PersonalAgentSession] = []

    init(sessions: [PersonalAgentSession] = []) { self.sessions = sessions }

    /// Subscribes before the first read, so a change during it is not missed.
    func watch(_ api: APIClient?) async {
        guard let api else { sessions = []; return }
        let changes = PersonalAgentSync.shared.changes()
        await refresh(api)
        for await _ in changes {
            guard !Task.isCancelled else { return }
            await refresh(api)
        }
    }

    func refresh(_ api: APIClient) async {
        guard let list = try? await api.personalAgentSessions(), !Task.isCancelled else { return }
        if list != sessions { sessions = list }
    }
}
