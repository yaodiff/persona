import Foundation
import Observation
import PersonaWire

// Settings › Managers (docs/assistant/managers.md, the web's ManagersSettings, Android's ManagersScreen): the hub's managers, each
// a whole assistant: where each runs, online or when it last synced, the default; renaming, making one the default,
// installing one somewhere else, removing one; a new one gets a one-time install code (and the install commands when the
// hub gives them). The hub pushes {"type":"managers"} when the roster changes (the socket asks for feature `managers`).

/// One manager in the hub's roster (GET /api/v1/managers).
nonisolated struct HubManager: Codable, Hashable, Identifiable, Sendable {
    var id: String
    var name: String
    var online: Bool
    var isDefault: Bool = false
    /// The assistant the hub runs itself.
    var builtin: Bool = false
    var host: String?
    var os: String?
    var arch: String?
    /// How it runs, as it reported: "child" (the hub's own), "service", "docker", "external"…
    var deploy: String?
    var version: String?
    /// Connected since (online only).
    var since: Date?
    /// Last connected: an offline one's "last synced".
    var lastSeen: Date?
    /// Not installed yet: when its install code runs out.
    var installing: Date?
    /// The open items of its inbox under Needs You, as the hub's roster tells them (`inbox_needs_you`,
    /// docs/protocol/protocol.md): nil when the hub can't tell (offline, still installing, an older hub or manager),
    /// which counts as none. It moves with every change of that inbox, so the app keeps it apart from its rosters
    /// (`AppModel.inboxNeedsYou`, `ManagerRoster.countless`): a roster that changed with it would draw every page that
    /// reads one again.
    var inboxNeedsYou: Int?

    enum CodingKeys: String, CodingKey {
        case id, name, online, builtin, host, os, arch, deploy, version, since, installing
        case isDefault = "default", lastSeen = "last_seen", inboxNeedsYou = "inbox_needs_you"
    }

    init(id: String, name: String, online: Bool, isDefault: Bool = false, builtin: Bool = false, host: String? = nil, os: String? = nil,
         arch: String? = nil, deploy: String? = nil, version: String? = nil, since: Date? = nil, lastSeen: Date? = nil, installing: Date? = nil,
         inboxNeedsYou: Int? = nil) {
        (self.id, self.name, self.online, self.isDefault, self.builtin) = (id, name, online, isDefault, builtin)
        (self.host, self.os, self.arch, self.deploy, self.version) = (host, os, arch, deploy, version)
        (self.since, self.lastSeen, self.installing, self.inboxNeedsYou) = (since, lastSeen, installing, inboxNeedsYou)
    }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? id
        online = try c.decodeIfPresent(Bool.self, forKey: .online) ?? false
        isDefault = try c.decodeIfPresent(Bool.self, forKey: .isDefault) ?? false
        builtin = try c.decodeIfPresent(Bool.self, forKey: .builtin) ?? false
        host = try c.decodeIfPresent(String.self, forKey: .host)
        os = try c.decodeIfPresent(String.self, forKey: .os)
        arch = try c.decodeIfPresent(String.self, forKey: .arch)
        deploy = try c.decodeIfPresent(String.self, forKey: .deploy)
        version = try c.decodeIfPresent(String.self, forKey: .version)
        since = try c.decodeIfPresent(Date.self, forKey: .since)
        lastSeen = try c.decodeIfPresent(Date.self, forKey: .lastSeen)
        installing = try c.decodeIfPresent(Date.self, forKey: .installing)
        inboxNeedsYou = try c.decodeIfPresent(Int.self, forKey: .inboxNeedsYou)
    }
}

/// A one-time install code (`mi_…`, ten minutes, traded once) and, from a hub that gives them, the install commands.
nonisolated struct ManagerInstallCode: Decodable, Hashable, Sendable {
    nonisolated struct Commands: Decodable, Hashable, Sendable {
        var service: String?
        var docker: String?
    }
    var code: String
    var expiresAt: Date
    var commands: Commands?
    enum CodingKeys: String, CodingKey { case code, commands, expiresAt = "expires_at" }
}

extension APIClient {
    nonisolated struct ManagerList: Decodable, Sendable {
        var managers: [HubManager]
        var defaultID: String?
        enum CodingKeys: String, CodingKey { case managers, defaultID = "default" }
    }
    nonisolated struct NewManager: Decodable, Sendable {
        var manager: HubManager
        var code: String
        var expiresAt: Date
        var commands: ManagerInstallCode.Commands?
        enum CodingKeys: String, CodingKey { case manager, code, commands, expiresAt = "expires_at" }
        var installCode: ManagerInstallCode { .init(code: code, expiresAt: expiresAt, commands: commands) }
    }

    func managers() async throws -> ManagerList {
        try JSON.decoder.decode(ManagerList.self, from: try await request("GET", "/api/v1/managers"))
    }
    func createManager(name: String) async throws -> NewManager {
        try JSON.decoder.decode(NewManager.self, from: try await request("POST", "/api/v1/managers", body: ["name": name]))
    }
    func managerInstallCode(_ id: String) async throws -> ManagerInstallCode {
        try JSON.decoder.decode(ManagerInstallCode.self, from: try await request("POST", "/api/v1/managers/\(Self.pathPart(id))/code"))
    }
    func renameManager(_ id: String, to name: String) async throws {
        _ = try await request("PATCH", "/api/v1/managers/\(Self.pathPart(id))", body: ["name": name])
    }
    func makeDefaultManager(_ id: String) async throws {
        _ = try await request("PATCH", "/api/v1/managers/\(Self.pathPart(id))", body: ["default": true])
    }
    func deleteManager(_ id: String) async throws {
        _ = try await request("DELETE", "/api/v1/managers/\(Self.pathPart(id))")
    }

    /// One path segment: an id the hub made (`m-…`), escaped all the same.
    nonisolated private static func pathPart(_ s: String) -> String {
        s.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed.subtracting(CharacterSet(charactersIn: "/"))) ?? s
    }
}

/// The page's words, kept apart from the views to test.
enum ManagersText {
    /// Names are 1–40 characters (hub/managers.go managerName).
    static let nameMax = 40

    /// Counted as the hub counts them: runes (Unicode scalars), not what reads as one character (an emoji with a
    /// skin tone is one grapheme and two scalars).
    static func validName(_ name: String) -> Bool {
        let n = name.trimmingCharacters(in: .whitespacesAndNewlines).unicodeScalars.count
        return n > 0 && n <= nameMax
    }

    /// How it runs, in plain words.
    static func deploy(_ m: HubManager) -> String {
        if m.builtin || m.deploy == "child" { return String(localized: "The hub's own") }
        switch m.deploy ?? "" {
        case "service", "systemd", "launchd": return String(localized: "Service")
        case "docker", "container": return String(localized: "Container")
        case "external": return String(localized: "Standalone")
        case let other: return other
        }
    }

    static func os(_ os: String?) -> String {
        switch os {
        case "darwin": "macOS"
        case "linux": "Linux"
        case "windows": "Windows"
        default: os ?? ""
        }
    }

    /// Where it runs: host · OS · how. Empty for one not installed yet.
    static func whereItRuns(_ m: HubManager) -> String {
        [m.host ?? "", os(m.os), deploy(m)].filter { !$0.isEmpty }.joined(separator: " · ")
    }

    /// Minutes an install code has left (to the nearest, at least 1 while it lasts: a fresh code says 10), 0 once it ran out.
    static func minutesLeft(_ until: Date, now: Date) -> Int {
        let s = until.timeIntervalSince(now)
        return s > 0 ? max(1, Int((s / 60).rounded())) : 0
    }

    enum Tone: Equatable { case online, offline, installing }

    /// The status line under the name, and its dot.
    static func status(_ m: HubManager, now: Date) -> (text: String, tone: Tone) {
        if let until = m.installing {
            let n = minutesLeft(until, now: now)
            return (n > 0 ? String(localized: "Installing · code expires in \(n) min") : String(localized: "Installing · code expired"), .installing)
        }
        if m.online { return (String(localized: "Online"), .online) }
        guard let seen = m.lastSeen else { return (String(localized: "Offline [Machine Entity be1b4f]"), .offline) }
        return (String(localized: "Offline · last synced \(seen.formatted(.relative(presentation: .named, unitsStyle: .wide)))"), .offline)
    }

    /// Why Delete is off for it, or nil when it can be removed.
    static func deleteBlocked(_ m: HubManager) -> String? {
        if m.builtin { return String(localized: "The assistant the hub runs itself can't be deleted") }
        if m.isDefault { return String(localized: "The default assistant can't be deleted. Make another one the default first") }
        return nil
    }

    /// Whether a hub from before the roster answered (404 without a code of its own).
    static func unsupported(_ error: Error) -> Bool {
        guard let e = error as? APIError else { return false }
        return e.status == 404 && e.code == nil
    }

    /// A failed change, in plain words.
    static func failure(_ error: Error) -> String {
        if let e = error as? APIError {
            switch e.code {
            case "default_manager": return String(localized: "This is the default assistant. Make another one the default before deleting it")
            case "builtin_manager": return String(localized: "The hub runs this assistant itself. It can't be deleted or installed elsewhere")
            case "no_manager": return String(localized: "This assistant is no longer there")
            case "installing": return String(localized: "It isn't installed yet. Rename it or make it the default once it is")
            default: break
            }
            if e.status == 400 { return String(localized: "Names are 1 to 40 characters") }
            if e.status == 403 { return String(localized: "This device isn't allowed to make changes") }
        }
        return error.shown ?? String(localized: "Can't reach the hub")
    }
}

/// Settings › Managers: the roster, the changes, and the install code on screen with its countdown.
@Observable final class ManagersModel {
    struct Deps {
        var api: () -> APIClient?
        var now: () -> Date = { .now }
        static var live: Deps { .init(api: { AppModel.shared.api }) }
    }

    /// An install code on screen: which manager it installs, and whether that one was never installed.
    struct Shown: Equatable {
        var id: String
        var name: String
        var code: ManagerInstallCode
        var neverInstalled = false
    }

    /// Nil until the hub answered.
    private(set) var managers: [HubManager]?
    /// The hub has no roster (404): it is older than managers.
    private(set) var unsupported = false
    /// Why the list could not be read (while there is none to show).
    private(set) var error: String?
    /// A change is on its way.
    private(set) var busy = false
    /// The last change that did not go through, in plain words.
    var failure: String?
    var shown: Shown? {
        didSet { if let s = shown { issued[s.id] = s.code } }
    }
    /// The codes this page put on screen, by manager: one still good is shown again (`showCode`).
    @ObservationIgnored private var issued: [String: ManagerInstallCode] = [:]
    /// The clock the countdowns read; `tick()`, `load()` and a new code move it on. Until one of them has, the deps'
    /// clock as it reads now: the init only assigns, as a `@State` initial value's must (docs/apple/performance.md P3,
    /// #4695), since the switcher's header makes one each time its struct is made.
    var now: Date { readAt ?? deps.now() }
    private var readAt: Date?
    @ObservationIgnored private let deps: Deps

    init(deps: Deps = .live) {
        self.deps = deps
    }

    func manager(_ id: String) -> HubManager? { managers?.first { $0.id == id } }

    var secondsLeft: Int { shown.map { ConnectDeviceText.secondsLeft($0.code.expiresAt, now: now) } ?? 0 }
    var expired: Bool { shown != nil && secondsLeft == 0 }

    func tick() { readAt = deps.now() }

    func load() async {
        guard let api = deps.api() else { return }
        do {
            let list = try await api.managers()
            managers = list.managers
            error = nil
            unsupported = false
        } catch {
            if ManagersText.unsupported(error) { unsupported = true }
            else if managers == nil { self.error = error.shown ?? String(localized: "Can't reach the hub") }
        }
        readAt = deps.now()
    }

    /// A new manager: its install code goes on screen. False when the hub said no (`failure` says why).
    @discardableResult func create(name: String) async -> Bool {
        let name = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard ManagersText.validName(name) else {
            failure = String(localized: "Names are 1 to 40 characters")
            return false
        }
        return await change { api in
            let made = try await api.createManager(name: name)
            self.readAt = self.deps.now()
            self.shown = Shown(id: made.manager.id, name: made.manager.name, code: made.installCode, neverInstalled: true)
        }
    }

    /// A new install code for `m`: to install it elsewhere (the old one stops once it is traded), or a fresh one for
    /// an install whose code ran out.
    @discardableResult func newCode(_ m: HubManager) async -> Bool {
        await change { try await self.freshCode(api: $0, id: m.id, name: m.name, neverInstalled: m.installing != nil) }
    }

    /// How to install `m`, which is still installing (the switcher's row): the code this page made for it while it
    /// lasts, else a new one (the hub keeps no code to show again, and a new one voids the last).
    func showCode(_ m: HubManager) async {
        tick()
        if let code = issued[m.id], code.expiresAt > now {
            shown = Shown(id: m.id, name: m.name, code: code, neverInstalled: true)
        } else {
            await newCode(m)
        }
    }

    /// The code on screen ran out: another one for the same manager.
    func renewShown() async {
        guard let s = shown else { return }
        await change { try await self.freshCode(api: $0, id: s.id, name: s.name, neverInstalled: s.neverInstalled) }
    }

    /// A new code for manager `id`. One that was never installed may be gone once its code expired (the hub answers
    /// 404 no_manager): then it is made again under the same name, which is what the user asked for.
    private func freshCode(api: APIClient, id: String, name: String, neverInstalled: Bool) async throws {
        do {
            let code = try await api.managerInstallCode(id)
            readAt = deps.now()
            shown = Shown(id: id, name: name, code: code, neverInstalled: neverInstalled)
        } catch let e as APIError where neverInstalled && e.status == 404 && e.code == "no_manager" {
            let made = try await api.createManager(name: name)
            readAt = deps.now()
            shown = Shown(id: made.manager.id, name: made.manager.name, code: made.installCode, neverInstalled: true)
        }
    }

    @discardableResult func rename(_ m: HubManager, to name: String) async -> Bool {
        let name = name.trimmingCharacters(in: .whitespacesAndNewlines)
        guard ManagersText.validName(name) else {
            failure = String(localized: "Names are 1 to 40 characters")
            return false
        }
        return await change { try await $0.renameManager(m.id, to: name) }
    }

    @discardableResult func makeDefault(_ m: HubManager) async -> Bool {
        await change { try await $0.makeDefaultManager(m.id) }
    }

    @discardableResult func delete(_ m: HubManager) async -> Bool {
        await change { try await $0.deleteManager(m.id) }
    }

    /// One change, then the list again (the push would bring it too; this one does not wait for it).
    private func change(_ work: (APIClient) async throws -> Void) async -> Bool {
        guard let api = deps.api() else { return false }
        busy = true
        failure = nil
        defer { busy = false }
        do {
            try await work(api)
        } catch {
            failure = ManagersText.failure(error)
            return false
        }
        await load()
        return true
    }
}
