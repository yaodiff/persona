import Foundation
import PersonaWire

// Personal agents: the hub's /api/v1/agents/* (server/internal/agents), the gateway's agent-bridge for now. A session
// id is "<source>:<sessionKey>"; bridge keys contain "/" and ":", so ids are fully percent-encoded in paths.

nonisolated struct PersonalAgentSession: Codable, Identifiable, Hashable, Sendable {
    var id: String
    var source: String
    var sessionKey: String
    var accountId: String?
    var provider: String
    var title: String
    var kind: String?
    var canSend: Bool
    var unreadCount: Int
    var rankTimeMs: Int64
    var parentSessionKey: String?
    var hasAvatar: Bool?
    var avatarVersion: String?

    var rankTime: Date { Date(timeIntervalSince1970: Double(rankTimeMs) / 1000) }
}

nonisolated struct PersonalAgentProvider: Codable, Hashable, Sendable {
    var provider: String
    var connection: String?
    var available: Bool?
}

nonisolated struct PersonalAgentAccount: Codable, Hashable, Sendable {
    var accountId: String
    var provider: String
    var displayName: String?
    var status: String?
    var lastError: String?
    var sessionCount: Int?
}

nonisolated struct PersonalAgentSourceStatus: Codable, Hashable, Sendable {
    var source: String
    var enabled: Bool
    var error: String?
    /// Why a reachable source has nothing to list yet: "missing" / "disabled" / "no_accounts" (agents.Setup*).
    var setup: String?
    var providers: [PersonalAgentProvider]?
    var accounts: [PersonalAgentAccount]?

    /// What to do so this source lists agents, for an empty list; nil when it is set up.
    var setupHint: String? {
        switch setup {
        case "missing": return String(localized: "This Lark gateway version has no personal agents yet. Upgrade the Lark gateway and try again.")
        case "disabled": return String(localized: "The extension that provides personal agents hasn't turned them on. Turn them on as its instructions say, then restart.")
        case "no_accounts": return String(localized: "No personal agent account connected yet: add Cue or Grok Bot in the Lark gateway's settings")
        default: return nil
        }
    }
}

/// One block of a message. Unknown types keep their raw JSON so they can be shown as a summary, not dropped.
nonisolated struct PersonalAgentBlock: Decodable, Hashable, Sendable {
    var type: String
    var text: String?
    var name: String?
    var status: String?
    var summary: String?
    var emoji: String?
    var raw: JSONValue

    private enum Keys: String, CodingKey { case type, text, name, status, summary, emoji }

    init(from decoder: Decoder) throws {
        raw = try JSONValue(from: decoder)
        let c = try decoder.container(keyedBy: Keys.self)
        type = (try? c.decode(String.self, forKey: .type)) ?? "unknown"
        text = try? c.decode(String.self, forKey: .text)
        name = try? c.decode(String.self, forKey: .name)
        status = try? c.decode(String.self, forKey: .status)
        summary = try? c.decode(String.self, forKey: .summary)
        emoji = try? c.decode(String.self, forKey: .emoji)
    }

    init(text: String) {
        type = "text"; self.text = text; raw = .object(["type": .string("text"), "text": .string(text)])
    }
}

nonisolated struct PersonalAgentLocal: Codable, Hashable, Sendable {
    var clientId: String?
    var status: String?
    var error: String?
}

nonisolated struct PersonalAgentMessage: Decodable, Identifiable, Hashable, Sendable {
    var externalId: String
    var role: String
    var blocks: [PersonalAgentBlock]
    var createTimeMs: Int64?
    var position: Int?
    var local: PersonalAgentLocal?

    var id: String { externalId }
    var plainText: String { blocks.compactMap(\.text).joined(separator: "\n") }
}

nonisolated struct PersonalAgentPage: Decodable, Sendable {
    var session: PersonalAgentSession?
    var messages: [PersonalAgentMessage]
    var hasMore: Bool
    var nextCursor: String?
}

nonisolated struct PersonalAgentOutboxEntry: Decodable, Hashable, Sendable {
    var clientId: String
    var sessionId: String
    var status: String // sending, sent, failed, deleted
    var error: String?
    var attempts: Int?
}

nonisolated struct PersonalAgentSendResult: Decodable, Sendable {
    var outbox: PersonalAgentOutboxEntry
    var duplicate: Bool?
}

extension APIClient {
    nonisolated static func agentPath(_ id: String) -> String {
        var allowed = CharacterSet.alphanumerics
        allowed.insert(charactersIn: "-._~")
        return "/api/v1/agents/sessions/" + (id.addingPercentEncoding(withAllowedCharacters: allowed) ?? id)
    }

    private struct AgentSources: Decodable { var sources: [PersonalAgentSourceStatus] }
    private struct AgentSessions: Decodable { var sessions: [PersonalAgentSession] }
    private struct AgentHidden: Decodable { var sessions: [PersonalAgentSession]? }
    private struct AgentSend: Encodable { var clientId: String; var text: String }

    func personalAgentSources() async throws -> [PersonalAgentSourceStatus] {
        try await get("/api/v1/agents", as: AgentSources.self).sources
    }

    func personalAgentListPage(_ query: SessionListQuery, cursor: String? = nil, since: String? = nil) async throws -> SessionListPage<PersonalAgentSession> {
        if query.legacy {
            return SessionListPage(sessions: try await personalAgentSessions(), remove: [], hasMore: nil, nextCursor: nil, revision: nil, total: nil)
        }
        var parameters = query.parameters
        if let cursor { parameters["cursor"] = cursor }
        if let since { parameters["since"] = since }
        var data = try await request("GET", "/api/v1/agents/sessions", query: parameters)
        // Older personal-agent routes interpret q as a transcript search and return items rather than sessions.
        if let raw = try? JSON.decoder.decode(JSONValue.self, from: data), raw["revision"] == nil, raw["items"] != nil {
            data = try await request("GET", "/api/v1/agents/sessions")
        }
        let response = data
        return try await Task.detached(priority: .userInitiated) {
            try JSON.decoder.decode(SessionListPage<PersonalAgentSession>.self, from: response)
        }.value
    }

    func personalAgentSessions() async throws -> [PersonalAgentSession] {
        try await get("/api/v1/agents/sessions", as: AgentSessions.self).sessions
    }

    func personalAgentMessages(_ id: String, cursor: String? = nil, limit: Int = 30) async throws -> PersonalAgentPage {
        var q = ["limit": String(limit)]
        if let cursor { q["cursor"] = cursor }
        return try await get(Self.agentPath(id) + "/messages", query: q, as: PersonalAgentPage.self)
    }

    /// Same clientId again is safe: the hub answers the first result with duplicate=true and sends nothing.
    func sendPersonalAgent(_ id: String, clientId: String, text: String) async throws -> PersonalAgentSendResult {
        try await post(Self.agentPath(id) + "/messages", body: AgentSend(clientId: clientId, text: text),
                       timeout: 60, as: PersonalAgentSendResult.self)
    }

    func markPersonalAgentRead(_ id: String) async throws {
        try await post(Self.agentPath(id) + "/read")
    }

    /// Remove from List (docs/protocol/protocol.md "Removing a personal Agent from the list"): Persona's own, the source
    /// keeps the conversation and a new message brings it back.
    func hidePersonalAgent(_ id: String) async throws {
        try await post(Self.agentPath(id) + "/hide")
    }

    func unhidePersonalAgent(_ id: String) async throws {
        try await post(Self.agentPath(id) + "/unhide")
    }

    /// The removed conversations the sources still list and that got nothing new since.
    func hiddenPersonalAgents() async throws -> [PersonalAgentSession] {
        try await get("/api/v1/agents/hidden", as: AgentHidden.self).sessions ?? []
    }
}
