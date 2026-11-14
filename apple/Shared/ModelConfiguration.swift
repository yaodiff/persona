import Foundation
import PersonaWire

/// The hub owns the purpose catalog and inheritance; clients edit only supported fields.
nonisolated struct ModelBinding: Codable, Hashable, Sendable {
    var provider: String?
    var model: String?
    var effort: String?
    /// The main conversation's runtime (`cli` | `agentsdk`, #2455); only Main Chat has it (`ModelPurpose.runtimes`).
    var runtime: String? = nil

    var isEmpty: Bool { provider == nil && model == nil && effort == nil && runtime == nil }
}

nonisolated struct ModelUse: Codable, Hashable, Sendable {
    var name: String
}

nonisolated struct ModelPurpose: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var categoryID: String?
    var uses: [ModelUse]?
    var name: String
    var group: String
    var kind: String
    var parent: String?
    var machineID: String?
    var override: ModelBinding
    var effective: ModelBinding
    var revision: Int
    var inheritedFrom: String?
    var status: String?
    /// Set while the hub holds this use's connection; `status` then says the same in words.
    var limit: ModelLimit?
    var fields: [String]?
    var providerIDs: [String]?
    /// The runtimes Main Chat can run on (`cli`, `agentsdk`); nil for every other use and on an older hub.
    var runtimes: [String]?
    /// What the use is for, in a sentence in the request's language (#5086, #5806): wording only. Every built-in use
    /// has one; a task's, an extension's and a voice plugin's have none, and a hub from before sends none.
    var description: String?

    enum CodingKeys: String, CodingKey {
        case id, name, group, kind, parent, override, effective, revision, status, limit, fields, uses, runtimes, description
        case categoryID = "category_id"
        case machineID = "machine_id", inheritedFrom = "inherited_from", providerIDs = "provider_ids"
    }

    func supports(_ field: String) -> Bool { (fields ?? ["provider", "model"]).contains(field) }

    /// The sentence to draw under the name; nil when the hub sent none or only blanks.
    var purposeNote: String? {
        guard let text = description?.trimmingCharacters(in: .whitespacesAndNewlines), !text.isEmpty else { return nil }
        return text
    }

    /// Explicit capabilities allow a child to own a binding without pretending to be a category.
    var hasWritableFields: Bool {
        fields?.contains(where: { ["provider", "model", "effort", "runtime"].contains($0) }) == true
    }
}

/// The hub stopped calling a connection after the upstream said its quota is used up or it is busy, until `until`
/// (docs/assistant/model-config.md「Upstream rate limits」).
nonisolated struct ModelLimit: Codable, Hashable, Sendable {
    var reason: String // quota | busy
    var until: Date

    /// The hub's wording, counted from `now`: 「额度用完，23 分钟后重试」.
    func text(now: Date = .now) -> String {
        let what = reason == "quota" ? String(localized: "Quota used up") : String(localized: "Concurrency limit reached")
        let left = until.timeIntervalSince(now)
        if left <= 0 { return String(localized: "\(what). Try again later.") }
        if left < 60 { return String(localized: "\(what). Try again in \(Int(left.rounded(.up))) seconds.") }
        return String(localized: "\(what). Try again in \(Int((left / 60).rounded(.up))) minutes.")
    }
}

nonisolated struct ModelAvailableModel: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var name: String?
}

nonisolated struct ModelProvider: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var name: String
    var kind: String
    var api: String?
    var model: String?
    var machines: [String]?
    var hidden: Bool?
    var hasKey: Bool?
    var models: [ModelAvailableModel]?

    enum CodingKeys: String, CodingKey {
        case id, name, kind, api, model, machines, hidden, models
        case hasKey = "has_key"
    }
}

nonisolated struct ModelConfiguration: Decodable, Sendable {
    var purposes: [ModelPurpose]
    var providers: [ModelProvider]
}

nonisolated struct ModelPurposeDetail: Decodable, Sendable {
    var purpose: ModelPurpose
    var providers: [ModelProvider]
}

/// Missing fields are unchanged; explicit null removes an override. Never send unsupported fields.
nonisolated struct ModelConfigurationPatch: Encodable, Sendable {
    var revision: Int
    var binding: ModelBinding
    var fields: [String]

    enum CodingKeys: String, CodingKey { case revision, provider, model, effort, runtime }

    func encode(to encoder: any Encoder) throws {
        var c = encoder.container(keyedBy: CodingKeys.self)
        try c.encode(revision, forKey: .revision)
        for (field, value, key) in [("provider", binding.provider, CodingKeys.provider),
                                    ("model", binding.model, CodingKeys.model), ("effort", binding.effort, CodingKeys.effort),
                                    ("runtime", binding.runtime, CodingKeys.runtime)] where fields.contains(field) {
            if let value { try c.encode(value, forKey: key) } else { try c.encodeNil(forKey: key) }
        }
    }
}

/// Main Chat's How It Runs (#2455): how its Claude runs.
nonisolated enum AssistantRuntime {
    static let cli = "cli"
    static let agentSDK = "agentsdk"

    static func title(_ id: String) -> String {
        switch id {
        case cli: "Claude Code CLI"
        case agentSDK: "Agent SDK"
        default: id
        }
    }

    /// What an assistant conversation runs on, for its status: 「已退回 CLI：找不到 Node.js 22+」 when the node could not
    /// run the Agent SDK, 「Agent SDK」 while it does; nil on the CLI as set (nothing to say).
    static func status(runtime: String?, fallback: String?) -> String? {
        if let f = fallback?.trimmingCharacters(in: .whitespaces), !f.isEmpty { return String(localized: "Fell back to CLI: \(f)") }
        return runtime == agentSDK ? title(agentSDK) : nil
    }
}

extension APIClient {
    func modelConfiguration() async throws -> ModelConfiguration { try await get("/api/v1/model-config") }

    func modelPurpose(_ id: String) async throws -> ModelPurposeDetail {
        try await get(Self.modelPurposePath(id))
    }

    func saveModelPurpose(_ id: String, patch: ModelConfigurationPatch) async throws -> ModelPurposeDetail {
        let data = try await request("PATCH", Self.modelPurposePath(id), body: patch)
        return try JSON.decoder.decode(ModelPurposeDetail.self, from: data)
    }

    private static func modelPurposePath(_ id: String) -> String {
        let encoded = id.addingPercentEncoding(withAllowedCharacters: .alphanumerics.union(CharacterSet(charactersIn: "-_."))) ?? id
        return "/api/v1/model-config/purposes/\(encoded)"
    }
}
