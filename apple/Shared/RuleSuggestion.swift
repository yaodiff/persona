import Foundation

// A standing rule the hub suggests after the owner gave the same answer three times (#4929, #5802;
// docs/assistant/unified-entry.md "A rule after the same answer three times"). It rides on an open plan
// (`TaskPlan.suggestion`), a to-do's decision (`TodoAsk.suggestion`) and a question a supervisor passed up
// (`Interaction.suggestion`); a hub from before sends none, and the card is drawn as before. The hub counts and words the
// line; the app adds what Always would store, from the fields, and sends the two answers.

/// What a suggested rule would cover (`RuleSuggestion.scope`); empty for a `delegate` suggestion.
nonisolated struct RuleScope: Codable, Hashable, Sendable {
    var machineID: String?
    /// The computer's name, for display.
    var machine: String?
    var directory: String?
    /// A standing authorization's action (`merge`, `push`, `deploy-test`) and the conditions it would be granted under.
    var action: String?
    var conditions: [String]?
    /// What the standing authorization would keep as the owner's words: their latest answer.
    var quote: String?

    enum CodingKeys: String, CodingKey {
        case machine, directory, action, conditions, quote
        case machineID = "machine_id"
    }

    init(machineID: String? = nil, machine: String? = nil, directory: String? = nil, action: String? = nil,
         conditions: [String]? = nil, quote: String? = nil) {
        self.machineID = machineID
        self.machine = machine
        self.directory = directory
        self.action = action
        self.conditions = conditions
        self.quote = quote
    }

    /// Every field may be missing; an odd one is left out without the rest.
    init(from decoder: Decoder) throws {
        let c = try? decoder.container(keyedBy: CodingKeys.self)
        machineID = (try? c?.decodeIfPresent(String.self, forKey: .machineID)).flatMap { $0 }
        machine = (try? c?.decodeIfPresent(String.self, forKey: .machine)).flatMap { $0 }
        directory = (try? c?.decodeIfPresent(String.self, forKey: .directory)).flatMap { $0 }
        action = (try? c?.decodeIfPresent(String.self, forKey: .action)).flatMap { $0 }
        conditions = (try? c?.decodeIfPresent([String].self, forKey: .conditions)).flatMap { $0 }
        quote = (try? c?.decodeIfPresent(String.self, forKey: .quote)).flatMap { $0 }
    }

    /// The computer as the owner names it: its name, else its id.
    var place: String {
        let name = machine?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return name.isEmpty ? (machineID?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "") : name
    }
}

/// The line a card carries: "Always do this?" with Always and Not Now.
nonisolated struct RuleSuggestion: Codable, Hashable, Sendable {
    /// What Always stores, as far as this build knows it.
    enum Creates: Hashable, Sendable {
        /// A `tasks.trust` entry, as Start These Directly makes.
        case taskTrust
        case standingAuthorization
        /// Nothing: the suggestion names a setting to turn on instead (`agents.delegate`). Always turns nothing on.
        case setting(String)
    }

    /// Names the kind and scope; what the two answers take.
    var id: String
    var kind: String
    /// The line, in the hub's words and the owner's language.
    var text: String
    var scope: RuleScope
    var creates: String
    var setting: String?
    /// How many identical answers were counted.
    var count: Int

    enum CodingKeys: String, CodingKey { case id, kind, text, scope, creates, setting, count }

    init(id: String, kind: String = "", text: String = "", scope: RuleScope = RuleScope(), creates: String = "",
         setting: String? = nil, count: Int = 0) {
        self.id = id
        self.kind = kind
        self.text = text
        self.scope = scope
        self.creates = creates
        self.setting = setting
        self.count = count
    }

    /// Never fails: a suggestion that can't be read is one that isn't offered (`offer`), and its card is drawn as before.
    init(from decoder: Decoder) throws {
        let c = try? decoder.container(keyedBy: CodingKeys.self)
        id = (try? c?.decodeIfPresent(String.self, forKey: .id)).flatMap { $0 } ?? ""
        kind = (try? c?.decodeIfPresent(String.self, forKey: .kind)).flatMap { $0 } ?? ""
        text = (try? c?.decodeIfPresent(String.self, forKey: .text)).flatMap { $0 } ?? ""
        scope = (try? c?.decodeIfPresent(RuleScope.self, forKey: .scope)).flatMap { $0 } ?? RuleScope()
        creates = (try? c?.decodeIfPresent(String.self, forKey: .creates)).flatMap { $0 } ?? ""
        setting = (try? c?.decodeIfPresent(String.self, forKey: .setting)).flatMap { $0 }
        count = (try? c?.decodeIfPresent(Int.self, forKey: .count)).flatMap { $0 } ?? 0
    }

    /// What Always would store. Nil for a kind this build doesn't know: it can't say what the tap would make, so the
    /// line is not offered.
    var made: Creates? {
        switch creates {
        case "tasks.trust": return .taskTrust
        case "standing_authorization": return .standingAuthorization
        case "":
            let name = setting?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
            return name.isEmpty ? nil : .setting(name)
        default: return nil
        }
    }

    /// Something to draw and to answer: an id, the hub's line, and a rule this build can describe.
    var offered: Bool {
        !id.isEmpty && !text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && made != nil
    }

    /// What Always creates, exactly, with the scope it covers: the second sentence of the line, under the hub's.
    var createsText: String {
        guard let made else { return "" }
        switch made {
        case .taskTrust:
            let directory = scope.directory ?? "", place = scope.place
            if directory.isEmpty || place.isEmpty {
                return String(localized: "Always adds this kind of task to Auto-Start Tasks: it starts without asking first.")
            }
            return String(localized: "Always adds a rule to Auto-Start Tasks: tasks that change files in \(directory) on \(place) start without asking first.")
        case .standingAuthorization:
            return [Self.grant(scope), Self.conditions(scope), Self.quote(scope)].compactMap { $0 }.joined(separator: " ")
        case .setting:
            // No key and no place here: the hub's own line above says what the setting does, and the result after
            // Always says where it is turned on.
            return String(localized: "Always stores no rule and turns nothing on. It only puts this suggestion away for 30 days.")
        }
    }

    private static func grant(_ scope: RuleScope) -> String {
        let directory = scope.directory ?? "", place = scope.place
        switch scope.action ?? "" {
        case "merge": return String(localized: "Always creates a standing authorization to merge in \(directory) on \(place).")
        case "push": return String(localized: "Always creates a standing authorization to push in \(directory) on \(place).")
        case "deploy-test": return String(localized: "Always creates a standing authorization to deploy to test in \(directory) on \(place).")
        case let other: return String(localized: "Always creates a standing authorization for “\(other)” in \(directory) on \(place).")
        }
    }

    private static func conditions(_ scope: RuleScope) -> String? {
        let names = (scope.conditions ?? []).map(conditionName).filter { !$0.isEmpty }
        guard !names.isEmpty else { return nil }
        return String(localized: "Only when: \(names.formatted(.list(type: .and))).")
    }

    /// A condition in words; one this build doesn't know stays the hub's word.
    static func conditionName(_ condition: String) -> String {
        switch condition {
        case "ci_green": String(localized: "checks pass [Rule Suggestion]")
        case "no_conflicts": String(localized: "no conflicts [Rule Suggestion]")
        default: condition
        }
    }

    private static func quote(_ scope: RuleScope) -> String? {
        let words = scope.quote?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return words.isEmpty ? nil : String(localized: "Kept as your words: “\(words)”.")
    }
}

/// The hub's answer to Always (`POST /api/v1/assistant/rule-suggestions/{id}/always`): what was made.
nonisolated struct RuleSuggestionMade: Decodable, Hashable, Sendable {
    var kind: String
    var creates: String
    /// `delegate`: the setting to turn on; nothing was stored.
    var setting: String?

    enum CodingKeys: String, CodingKey { case kind, creates, setting }

    init(kind: String = "", creates: String = "", setting: String? = nil) {
        self.kind = kind
        self.creates = creates
        self.setting = setting
    }

    init(from decoder: Decoder) throws {
        let c = try? decoder.container(keyedBy: CodingKeys.self)
        kind = (try? c?.decodeIfPresent(String.self, forKey: .kind)).flatMap { $0 } ?? ""
        creates = (try? c?.decodeIfPresent(String.self, forKey: .creates)).flatMap { $0 } ?? ""
        setting = (try? c?.decodeIfPresent(String.self, forKey: .setting)).flatMap { $0 }
    }

    /// What was made and where it is listed and removed, for the line that takes the suggestion's place. `asked` is the
    /// suggestion answered: an answer that leaves `creates` out is read by it.
    func text(asked: RuleSuggestion) -> String {
        let stored = creates.isEmpty && (setting ?? "").isEmpty ? asked.creates : creates
        switch stored {
        case "tasks.trust":
            return String(localized: "Rule added: these tasks start without asking first. It is listed, and removed, in the assistant's profile under Auto-Start Tasks.")
        case "standing_authorization":
            return String(localized: "Standing authorization added. It is listed, and revoked, in the assistant's profile under Standing Authorizations.")
        default:
            let name = setting ?? asked.setting ?? ""
            if name.isEmpty { return String(localized: "Done. Nothing was stored.") }
            // The place in the words of the hub's own recommended-setup line for the same mechanism
            // (assistant-setup_suggest.json "delegate").
            return String(localized: "Nothing was stored and nothing was turned on. The apps have no switch for this yet: it is turned on in the assistant's configuration file on the hub's computer (\(name)).")
        }
    }
}

extension APIClient {
    /// Always on a suggested rule. 404: the answers don't earn it any more, or it was answered already; 409: the standing
    /// authorizations changed meanwhile; 403: this device has no full access.
    func ruleSuggestionAlways(_ id: String) async throws -> RuleSuggestionMade {
        let data = try await post("/api/v1/assistant/rule-suggestions/\(Self.ruleSuggestionPart(id))/always")
        return (try? JSON.decoder.decode(RuleSuggestionMade.self, from: data)) ?? RuleSuggestionMade()
    }

    /// Not Now: the line is not shown for this kind and scope for 30 days.
    func ruleSuggestionNotNow(_ id: String) async throws {
        _ = try await post("/api/v1/assistant/rule-suggestions/\(Self.ruleSuggestionPart(id))/not-now")
    }

    private static func ruleSuggestionPart(_ id: String) -> String {
        id.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(CharacterSet(charactersIn: "/?#"))) ?? id
    }
}
