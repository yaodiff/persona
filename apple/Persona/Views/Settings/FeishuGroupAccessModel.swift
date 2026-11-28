import Foundation
import Observation
import PersonaWire

/// Settings › Connectors › Lark Bot › "Group Access" (docs/extensions/bots.md "Group access for teammates", #16848):
/// who besides the owner may @ the bot in a group, and which read-only connector tools a guest may use. The same hub
/// routes as the web's section (web/src/lib/feishu-group-access.ts), under /api/v1/bots/feishu/group-access.
///
/// A client reads the whole policy, changes one thing and writes the whole policy back at the revision it read; a stale
/// revision answers 409 and the page reloads once.

/// Who besides the owner may @ the bot in a group (assistantwire FeishuGroupAccessMode). An empty value is owner_only.
nonisolated enum FeishuGroupAccessMode: String, Codable, Sendable, CaseIterable {
    case ownerOnly = "owner_only"
    case allowlist
    case everyoneInAllowedChats = "everyone_in_allowed_chats"

    /// "" is the stored default: only the owner.
    static func effective(_ raw: String) -> FeishuGroupAccessMode { FeishuGroupAccessMode(rawValue: raw) ?? .ownerOnly }
}

/// A person on one of the lists, by the bot's open id (ou_…).
nonisolated struct FeishuGroupUser: Codable, Equatable, Sendable {
    var openID: String
    var name: String?

    enum CodingKeys: String, CodingKey { case name; case openID = "open_id" }

    init(openID: String, name: String? = nil) { (self.openID, self.name) = (openID, name) }
}

/// A group on one of the lists, by its chat id (oc_…).
nonisolated struct FeishuGroupChat: Codable, Equatable, Sendable {
    var chatID: String
    var name: String?

    enum CodingKeys: String, CodingKey { case name; case chatID = "chat_id" }

    init(chatID: String, name: String? = nil) { (self.chatID, self.name) = (chatID, name) }
}

/// The whole policy, written back as one document (assistantwire FeishuGroupAccessPolicy). Arrays are always present.
nonisolated struct FeishuGroupAccessPolicy: Codable, Equatable, Sendable {
    var mode: FeishuGroupAccessMode
    var allowedUsers: [FeishuGroupUser]
    var allowedChats: [FeishuGroupChat]
    var blockedUsers: [FeishuGroupUser]
    var blockedChats: [FeishuGroupChat]
    var guestTools: [String]
    var perChatHourly: Int

    enum CodingKeys: String, CodingKey {
        case mode
        case allowedUsers = "allowed_users"
        case allowedChats = "allowed_chats"
        case blockedUsers = "blocked_users"
        case blockedChats = "blocked_chats"
        case guestTools = "guest_tools"
        case perChatHourly = "per_chat_hourly"
    }

    /// Lenient like the web's reading: a missing list is empty, an unknown mode is owner_only.
    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        mode = FeishuGroupAccessMode.effective(try c.decodeIfPresent(String.self, forKey: .mode) ?? "")
        allowedUsers = try c.decodeIfPresent([FeishuGroupUser].self, forKey: .allowedUsers) ?? []
        allowedChats = try c.decodeIfPresent([FeishuGroupChat].self, forKey: .allowedChats) ?? []
        blockedUsers = try c.decodeIfPresent([FeishuGroupUser].self, forKey: .blockedUsers) ?? []
        blockedChats = try c.decodeIfPresent([FeishuGroupChat].self, forKey: .blockedChats) ?? []
        guestTools = try c.decodeIfPresent([String].self, forKey: .guestTools) ?? []
        perChatHourly = try c.decodeIfPresent(Int.self, forKey: .perChatHourly) ?? FeishuGroupAccessLimits.fallback.perChatHourlyDefault
    }
}

/// A read-only connector tool the owner may give guests; `connector` is the connector's display name.
nonisolated struct FeishuGuestToolOption: Codable, Equatable, Sendable, Identifiable {
    var name: String
    var connector: String
    var id: String { name }
}

/// A recent @bot the policy refused, memory only: whom to allow or block. `at` is unix seconds.
nonisolated struct FeishuGroupRequest: Codable, Equatable, Sendable, Identifiable {
    var openID: String
    var name: String?
    var chatID: String
    var chatName: String?
    var at: Int64
    var id: String { "\(openID)/\(chatID)" }

    enum CodingKeys: String, CodingKey {
        case name, at
        case openID = "open_id"
        case chatID = "chat_id"
        case chatName = "chat_name"
    }
}

/// The bounds the hub enforces, so the page never offers a value it would refuse.
nonisolated struct FeishuGroupAccessLimits: Codable, Equatable, Sendable {
    var listMax: Int
    var guestToolsMax: Int
    var perChatHourlyMin: Int
    var perChatHourlyMax: Int
    var perChatHourlyDefault: Int

    enum CodingKeys: String, CodingKey {
        case listMax = "list_max"
        case guestToolsMax = "guest_tools_max"
        case perChatHourlyMin = "per_chat_hourly_min"
        case perChatHourlyMax = "per_chat_hourly_max"
        case perChatHourlyDefault = "per_chat_hourly_default"
    }

    /// What a hub that leaves the bounds out still means (bots_group_access.go's constants).
    static let fallback = FeishuGroupAccessLimits(listMax: 200, guestToolsMax: 8, perChatHourlyMin: 1, perChatHourlyMax: 120, perChatHourlyDefault: 20)

    init(listMax: Int, guestToolsMax: Int, perChatHourlyMin: Int, perChatHourlyMax: Int, perChatHourlyDefault: Int) {
        (self.listMax, self.guestToolsMax, self.perChatHourlyMin, self.perChatHourlyMax, self.perChatHourlyDefault) =
            (listMax, guestToolsMax, perChatHourlyMin, perChatHourlyMax, perChatHourlyDefault)
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        let d = Self.fallback
        listMax = try c.decodeIfPresent(Int.self, forKey: .listMax) ?? d.listMax
        guestToolsMax = try c.decodeIfPresent(Int.self, forKey: .guestToolsMax) ?? d.guestToolsMax
        perChatHourlyMin = try c.decodeIfPresent(Int.self, forKey: .perChatHourlyMin) ?? d.perChatHourlyMin
        perChatHourlyMax = try c.decodeIfPresent(Int.self, forKey: .perChatHourlyMax) ?? d.perChatHourlyMax
        perChatHourlyDefault = try c.decodeIfPresent(Int.self, forKey: .perChatHourlyDefault) ?? d.perChatHourlyDefault
    }
}

/// GET and PUT /api/v1/bots/feishu/group-access (assistantwire FeishuGroupAccessResponse).
nonisolated struct FeishuGroupAccess: Codable, Equatable, Sendable {
    var revision: Int64
    /// The bot has an owner; an unpaired bot has no group access to show.
    var paired: Bool
    var policy: FeishuGroupAccessPolicy
    var availableTools: [FeishuGuestToolOption]
    var recentRequests: [FeishuGroupRequest]
    var limits: FeishuGroupAccessLimits

    enum CodingKeys: String, CodingKey {
        case revision, paired, policy, limits
        case availableTools = "available_tools"
        case recentRequests = "recent_requests"
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        revision = try c.decodeIfPresent(Int64.self, forKey: .revision) ?? 0
        paired = try c.decodeIfPresent(Bool.self, forKey: .paired) ?? false
        policy = try c.decode(FeishuGroupAccessPolicy.self, forKey: .policy)
        availableTools = try c.decodeIfPresent([FeishuGuestToolOption].self, forKey: .availableTools) ?? []
        recentRequests = try c.decodeIfPresent([FeishuGroupRequest].self, forKey: .recentRequests) ?? []
        limits = try c.decodeIfPresent(FeishuGroupAccessLimits.self, forKey: .limits) ?? .fallback
    }
}

/// The body of PUT: the whole policy, and the revision it was read at.
nonisolated struct FeishuGroupAccessUpdate: Encodable, Sendable {
    var revision: Int64
    var policy: FeishuGroupAccessPolicy
}

/// The words and the small decisions of the section, kept apart from the view so tests can read them. The same rules as
/// the web's pure helpers (web/src/lib/feishu-group-access.ts).
enum FeishuGroupAccessText {
    /// The questions-per-group-per-hour choices the picker offers; the value in force is always added.
    static let hourlyChoices = [5, 10, 20, 40, 60, 120]

    /// The picker's choices in order, with the value in force included even when it is not one of the standard ones.
    static func hourlyChoices(_ current: Int) -> [Int] { Array(Set(hourlyChoices + [current])).sorted() }

    /// One line under the mode picker: what the mode lets people do.
    static func modeDetail(_ mode: FeishuGroupAccessMode) -> String {
        switch mode {
        case .allowlist: String(localized: "Only people you add can @ the bot, and only in groups you allow.")
        case .everyoneInAllowedChats: String(localized: "Anyone in a group you allow can @ the bot.")
        case .ownerOnly: String(localized: "Only you can @ the bot in a group. Everyone else is ignored.")
        }
    }

    /// "Wiki · wiki_search".
    static func toolLabel(_ tool: FeishuGuestToolOption) -> String { "\(tool.connector) · \(tool.name)" }

    /// A row's words: its label when it has one, else the id itself.
    static func title(name: String?, id: String) -> String { name.flatMap { $0.isEmpty ? nil : $0 } ?? id }

    /// An id the hub accepts: trimmed, 1–100 characters, no whitespace (checkGroupMembers).
    static func validID(_ raw: String) -> String? {
        let id = raw.trimmingCharacters(in: .whitespaces)
        guard (1...100).contains(id.count), id.rangeOfCharacter(from: .whitespaces) == nil else { return nil }
        return id
    }
}

extension FeishuGroupAccessPolicy {
    /// guest_tools with `name` added or removed.
    func withTool(_ name: String, on: Bool) -> FeishuGroupAccessPolicy {
        var p = self
        p.guestTools = p.guestTools.filter { $0 != name }
        if on { p.guestTools.append(name) }
        return p
    }

    /// The policy's guest tools that are still installed. The hub refuses a whole PUT naming a tool it no longer offers,
    /// so a removed connector's tool is dropped on the next save rather than blocking every change.
    func usableTools(_ available: [FeishuGuestToolOption]) -> FeishuGroupAccessPolicy {
        let names = Set(available.map(\.name))
        var p = self
        p.guestTools = p.guestTools.filter { names.contains($0) }
        return p
    }

    /// Allowing a recent request: the person goes on the allowed people, and their group on the allowed groups (both modes
    /// that answer guests need the group). Entries already listed keep their labels.
    func allowing(_ r: FeishuGroupRequest) -> FeishuGroupAccessPolicy {
        var p = self
        if !p.allowedUsers.contains(where: { $0.openID == r.openID }) {
            p.allowedUsers.append(FeishuGroupUser(openID: r.openID, name: r.name.flatMap { $0.isEmpty ? nil : $0 }))
        }
        if !p.allowedChats.contains(where: { $0.chatID == r.chatID }) {
            p.allowedChats.append(FeishuGroupChat(chatID: r.chatID, name: r.chatName.flatMap { $0.isEmpty ? nil : $0 }))
        }
        return p
    }

    /// Blocking a recent request: the person goes on the blocked people, whatever the mode. An entry already there is
    /// replaced, so a label can be corrected.
    func blocking(_ r: FeishuGroupRequest) -> FeishuGroupAccessPolicy {
        var p = self
        p.blockedUsers.removeAll { $0.openID == r.openID }
        p.blockedUsers.append(FeishuGroupUser(openID: r.openID, name: r.name.flatMap { $0.isEmpty ? nil : $0 }))
        return p
    }
}

extension APIClient {
    private static let feishuGroupAccess = "/api/v1/bots/feishu/group-access"

    /// The policy. Nil when the hub predates group access (404): the section is hidden, nothing else changes.
    func feishuGroupAccess() async throws -> FeishuGroupAccess? {
        do {
            return try JSON.decoder.decode(FeishuGroupAccess.self, from: await request("GET", Self.feishuGroupAccess))
        } catch let e as APIError where e.status == 404 {
            return nil
        }
    }

    func updateFeishuGroupAccess(_ u: FeishuGroupAccessUpdate) async throws -> FeishuGroupAccess {
        try JSON.decoder.decode(FeishuGroupAccess.self, from: await request("PUT", Self.feishuGroupAccess, body: u, timeout: 40))
    }
}

/// The section's state: the hub's view, and the one change in flight. Every change is read-modify-write of the whole
/// policy at the revision on screen; a stale revision reloads once and says so.
@Observable final class FeishuGroupAccessModel {
    struct Deps {
        var api: () -> APIClient?

        static var live: Deps { Deps(api: { AppModel.shared.api }) }
    }

    /// nil: nothing to show (an older hub, or not loaded yet).
    private(set) var access: FeishuGroupAccess?
    private(set) var loadError: String?
    private(set) var error: String?
    private(set) var saving = false
    /// What is being added, when the add sheet is open.
    var adding: AddKind?
    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var capturedAPI: APIClient?

    /// A group or a person, and which list it goes on.
    enum AddKind: Equatable {
        case allowedGroup, allowedPerson, blockedGroup, blockedPerson
    }

    init(deps: Deps = .live) { self.deps = deps }

    private func api() -> APIClient? {
        if capturedAPI == nil { capturedAPI = deps.api()?.currentAssistantSettings() }
        return capturedAPI
    }

    /// Whether the section is drawn: the hub has the route and the bot is paired.
    var available: Bool { access?.paired == true }

    /// An older hub has no such route (404): the section hides. Anything else is a real failure.
    static func isAbsent(_ e: Error) -> Bool { (e as? APIError)?.status == 404 }

    func load() async {
        guard let api = api() else { return }
        do {
            access = try await api.feishuGroupAccess()
            loadError = nil
        } catch {
            if Self.isAbsent(error) { access = nil; loadError = nil } else if access == nil {
                loadError = error.shown.map { String(localized: "Couldn't load group access: \($0)") }
            }
        }
    }

    /// The policy as it is on screen, changed, written back whole. A 409 reloads once and keeps the page as the hub has
    /// it; any other failure leaves the page as it was.
    @discardableResult
    private func save(_ mutate: (inout FeishuGroupAccessPolicy) -> Void) async -> Bool {
        guard var access, !saving, let api = api() else { return false }
        mutate(&access.policy)
        access.policy = access.policy.usableTools(access.availableTools)
        saving = true
        defer { saving = false }
        do {
            self.access = try await api.updateFeishuGroupAccess(FeishuGroupAccessUpdate(revision: access.revision, policy: access.policy))
            error = nil
            return true
        } catch let e as APIError where e.status == 409 {
            error = String(localized: "Group access was changed somewhere else, so the latest is shown. Make the change again if you still need it.")
            if let fresh = try? await api.feishuGroupAccess() { self.access = fresh }
            return false
        } catch {
            self.error = error.shown.map { String(localized: "Couldn't save group access: \($0)") }
            return false
        }
    }

    func setMode(_ mode: FeishuGroupAccessMode) async { await save { $0.mode = mode } }

    func setPerChatHourly(_ n: Int) async { await save { $0.perChatHourly = n } }

    func setTool(_ name: String, on: Bool) async { await save { $0 = $0.withTool(name, on: on) } }

    func allow(_ r: FeishuGroupRequest) async { await save { $0 = $0.allowing(r) } }

    func block(_ r: FeishuGroupRequest) async { await save { $0 = $0.blocking(r) } }

    /// Adds `id` (and an optional label) to the list the sheet is about. False when the id is not one the hub accepts, or
    /// the save failed: the sheet stays open.
    @discardableResult
    func add(id raw: String, name rawName: String) async -> Bool {
        guard let kind = adding, let id = FeishuGroupAccessText.validID(raw) else { return false }
        let name = rawName.trimmingCharacters(in: .whitespaces)
        let label = name.isEmpty ? nil : name
        let ok = await save { policy in
            switch kind {
            case .allowedGroup: policy.allowedChats = policy.allowedChats.with(FeishuGroupChat(chatID: id, name: label))
            case .allowedPerson: policy.allowedUsers = policy.allowedUsers.with(FeishuGroupUser(openID: id, name: label))
            case .blockedGroup: policy.blockedChats = policy.blockedChats.with(FeishuGroupChat(chatID: id, name: label))
            case .blockedPerson: policy.blockedUsers = policy.blockedUsers.with(FeishuGroupUser(openID: id, name: label))
            }
        }
        if ok { adding = nil }
        return ok
    }

    func removeAllowedGroup(_ id: String) async { await save { $0.allowedChats.removeAll { $0.chatID == id } } }
    func removeAllowedPerson(_ id: String) async { await save { $0.allowedUsers.removeAll { $0.openID == id } } }
    func removeBlockedGroup(_ id: String) async { await save { $0.blockedChats.removeAll { $0.chatID == id } } }
    func removeBlockedPerson(_ id: String) async { await save { $0.blockedUsers.removeAll { $0.openID == id } } }
}

/// A list with `entry` added; an entry already there is replaced, so a label can be corrected.
extension Array where Element == FeishuGroupUser {
    func with(_ entry: FeishuGroupUser) -> [FeishuGroupUser] { filter { $0.openID != entry.openID } + [entry] }
}

extension Array where Element == FeishuGroupChat {
    func with(_ entry: FeishuGroupChat) -> [FeishuGroupChat] { filter { $0.chatID != entry.chatID } + [entry] }
}
