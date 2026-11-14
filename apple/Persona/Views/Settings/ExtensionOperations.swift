import Foundation
import Observation
import PersonaWire

// Installing, upgrading and removing extensions, with what the hub is doing meanwhile (docs/protocol/protocol.md「Extension install, upgrade and uninstall progress」,
// #3277, #3307).
//
// A hub with `extension_operations` runs each as an operation and pushes it in `extension_operation` events (to a
// socket that declared the feature) as it goes through its steps: an install previews in the background (download,
// verify, unpack, check), stops at needs_consent for the user to agree to the permissions, and goes on with the same
// operation once they do. This store keeps them for the whole app, so the page that started one can be left and come
// back to it, and the market's list marks the row. An older hub answers when it is done: the store then keeps a local
// operation with no steps, so the page still says what is going on and for how long instead of a button that spins.

/// What the hub answers a request made with `async`.
nonisolated struct ExtensionOperationReply: Decodable, Sendable {
    var operation: ExtensionOperation?
}

/// One operation read on its own: while it waits for consent, the preview to agree to.
nonisolated struct ExtensionOperationDetail: Decodable, Sendable {
    var operation: ExtensionOperation
    var preview: ExtensionPreview?

    private enum CodingKeys: String, CodingKey { case preview }

    init(from decoder: Decoder) throws {
        operation = try ExtensionOperation(from: decoder)
        preview = try? decoder.container(keyedBy: CodingKeys.self).decodeIfPresent(ExtensionPreview.self, forKey: .preview)
    }
}

extension APIClient {
    /// The operations going on, waiting for consent, and the ones ended in the last minutes.
    func extensionOperations() async throws -> [ExtensionOperation] {
        struct List: Decodable { var operations: [ExtensionOperation]? }
        let list: List = try await get(extensionPath("/-/operations"))
        return list.operations ?? []
    }

    func extensionOperation(_ id: String) async throws -> ExtensionOperationDetail {
        try await get(extensionPath("/-/operations/\(id)"))
    }

    /// Starts it again from the beginning (the package is in the hub's cache); a new operation.
    func retryExtensionOperation(_ id: String) async throws -> ExtensionOperation {
        try Self.operation(try await request("POST", extensionPath("/-/operations/\(id)/retry"), timeout: 60))
    }

    /// Cancels one that downloads, checks or waits for consent; removes one that ended from the list.
    func deleteExtensionOperation(_ id: String) async throws {
        _ = try await request("DELETE", extensionPath("/-/operations/\(id)"))
    }

    /// Previews a market's extension in the background: download and checks, then it waits for consent.
    func startCatalogPreview(market: String, extension id: String) async throws -> ExtensionOperation {
        struct Body: Encodable {
            var market: String
            var extensionID: String
            var plugin: String
            var background = true
            enum CodingKeys: String, CodingKey { case market, plugin, extensionID = "extension", background = "async" }
        }
        return try Self.operation(try await request("POST", extensionPath("/-/catalog/preview"),
                                                    body: Body(market: market, extensionID: id, plugin: id), timeout: 60))
    }

    /// The user agreed to the preview: the operation it came from goes on to install (or upgrade) it.
    func agreeExtensionInstall(_ id: String, preview: ExtensionPreview) async throws -> ExtensionOperation {
        let path = preview.upgrade == true ? extensionPath("/\(id)/-/upgrade") : extensionPath("/-/install")
        return try Self.operation(try await request("POST", path, body: ExtensionInstallRequest(preview: preview, background: true), timeout: 60))
    }

    private static func operation(_ data: Data) throws -> ExtensionOperation {
        if let op = try? JSON.decoder.decode(ExtensionOperationReply.self, from: data).operation { return op }
        return try JSON.decoder.decode(ExtensionOperation.self, from: data)
    }
}

/// The app's extension operations, from the hub's events and lists and from the requests this app makes.
@MainActor @Observable final class ExtensionOperations {
    static let shared = ExtensionOperations()

    struct Deps {
        var api: () -> APIClient?
        var features: () -> Set<String>
        /// The hub the operations are of (another one: the list starts over).
        var hub: () -> String?
        /// The roster's default manager, so naming it explicitly and leaving it unnamed share one stored operation.
        var defaultID: () -> String? = { nil }
        /// How often an operation being waited for is read again when no event came.
        var poll: Duration = .seconds(2)
        /// How long to wait for one before saying so.
        var patience: TimeInterval = 15 * 60
        static var live: Deps {
            .init(api: { AppModel.shared.api }, features: { AppModel.shared.hubFeatures },
                  hub: { AppModel.shared.config?.url.absoluteString },
                  defaultID: { AppModel.shared.managerRoster?.defaultID })
        }
    }

    /// By operation id, namespaced by its non-default manager (each manager numbers its own).
    private(set) var operations: [String: ExtensionOperation] = [:]
    @ObservationIgnored private var lastHub: String?
    @ObservationIgnored private let deps: Deps

    init(deps: Deps = .live) { self.deps = deps }

    private func storedManager(_ manager: String?) -> String? {
        ManagerRoster.stored(manager, defaultID: deps.defaultID())
    }

    private func key(_ id: String, manager: String?) -> String {
        Self.key(id, manager: manager, defaultID: deps.defaultID())
    }

    /// The page's original route travels with an operation it reads, so Retry and Follow still name an explicitly
    /// routed default assistant even though its storage key is the default one.
    private func requestScoped(_ op: ExtensionOperation, manager: String?) -> ExtensionOperation {
        var op = op
        op.manager = manager
        return op
    }

    /// The hub runs them in steps and pushes them.
    var stepped: Bool { deps.features().hasExtensionFeature("operations") }

    /// The one to show for an extension (of a market, when given) of `manager` (nil: the default's, whose catalog the
    /// market pages show): one going on, else the latest. Another manager's copy of the same extension is not this one.
    func operation(for extensionID: String, market: String? = nil, manager: String? = nil) -> ExtensionOperation? {
        let stored = storedManager(manager)
        let mine = operations.values.filter {
            $0.extensionID == extensionID && (market == nil || $0.market == nil || $0.market == market) && $0.manager == stored
        }
        let match = mine.first(where: \.isActive) ?? mine.max { ($0.startedAt ?? .distantPast) < ($1.startedAt ?? .distantPast) }
        return match.map { requestScoped($0, manager: manager) }
    }

    /// From an `extension_operation` event or a list: a newer state of the same operation replaces the one kept.
    func received(_ operation: ExtensionOperation) {
        resetIfAnotherHub()
        var op = operation
        op.manager = storedManager(op.manager)
        let storageKey = key(op.id, manager: op.manager)
        if let old = operations[storageKey], let was = old.updatedAt, let now = op.updatedAt, now < was { return }
        if operations[storageKey] != op { operations[storageKey] = op }
    }

    /// Reads what the hub has going on for `manager` (nil: the default's), asking it by name: a page opening (its own
    /// manager's), the connection back (the default's; events missed are not sent again).
    func reload(manager: String? = nil) async {
        resetIfAnotherHub()
        let requestedManager = manager
        let stored = storedManager(manager)
        guard stepped, let api = deps.api()?.manager(requestedManager), let list = try? await api.extensionOperations() else { return }
        // Its list is the truth for its operations; this app's own (an older hub's) and other managers' stay. Only
        // storage is canonicalized: the request above keeps an explicitly named default assistant's route.
        operations = operations.filter { $0.value.isLocal || $0.value.manager != stored }
        for var op in list {
            op.manager = stored
            received(op)
        }
    }

    /// Put away: the hub forgets one that ended, cancels one that waits for consent or still checks. `manager`: whose
    /// operation it is (nil: the default's).
    func dismiss(_ id: String, manager: String? = nil) async {
        let storageKey = key(id, manager: manager)
        guard let op = operations[storageKey] else { return }
        operations[storageKey] = nil
        if !op.isLocal { try? await deps.api()?.manager(manager).deleteExtensionOperation(id) }
    }

    // MARK: - stepped (a hub with operations)

    // `manager` names the manager whose extension it is (an extension's settings page of another manager); nil: the
    // default's, whose catalog the market pages show.

    /// Downloads and checks a market's extension in the background; the result is the operation once it waits for
    /// consent, failed or was canceled.
    func preview(market: String, extension id: String, manager: String? = nil) async throws -> ExtensionOperation {
        await settled(try await startPreview(market: market, extension: id, manager: manager))
    }

    /// Starts the preview: the operation at its first step.
    func startPreview(market: String, extension id: String, manager: String? = nil) async throws -> ExtensionOperation {
        var op = try await api(manager).startCatalogPreview(market: market, extension: id)
        op.manager = manager
        received(op)
        return op
    }

    /// The preview `op` is, once it waits for consent, failed or was canceled.
    func settled(_ op: ExtensionOperation) async -> ExtensionOperation {
        await follow(op.id, manager: op.manager) { $0.state != .running }
    }

    /// The preview the operation waits on.
    func consentPreview(_ id: String, manager: String? = nil) async throws -> ExtensionPreview? {
        var detail = try await api(manager).extensionOperation(id)
        detail.operation.manager = manager
        received(detail.operation)
        return detail.preview
    }

    /// The user agreed: the operation installs and the result is how it ended.
    func agree(_ id: String, preview: ExtensionPreview, manager: String? = nil) async throws -> ExtensionOperation {
        var op = try await api(manager).agreeExtensionInstall(id, preview: preview)
        op.manager = manager
        received(op)
        return await follow(op.id, manager: manager) { !$0.isRunning }
    }

    /// Starts a failed one again; the result once it waits for consent again or ended (a new operation).
    func retry(_ op: ExtensionOperation) async throws -> ExtensionOperation {
        var next = try await api(op.manager).retryExtensionOperation(op.id)
        next.manager = op.manager
        operations[key(op.id, manager: op.manager)] = nil
        received(next)
        return await follow(next.id, manager: next.manager) { !$0.isRunning }
    }

    private func api(_ manager: String?) throws -> APIClient {
        guard let api = deps.api() else { throw APIError(status: 0, message: String(localized: "Not connected to the hub yet")) }
        return api.manager(manager)
    }

    private static func key(_ id: String, manager: String?, defaultID: String?) -> String {
        ManagerRoster.stored(manager, defaultID: defaultID).map { "\($0)\u{0}\(id)" } ?? id
    }

    // MARK: - an older hub

    /// Installs or upgrades with the preview agreed to, as the hub answers when it is done; a local operation says
    /// what is going on meanwhile. The result is how it ended.
    func installWithoutSteps(_ id: String, name: String?, preview: ExtensionPreview, manager: String? = nil) async -> ExtensionOperation {
        let upgrade = preview.upgrade == true
        var local = ExtensionOperation(id: Self.localID(), kind: upgrade ? .upgrade : .install, extensionID: id, name: name,
                                       version: preview.manifest.version, from: preview.from, market: preview.market,
                                       startedAt: .now, updatedAt: .now)
        local.manager = manager
        received(local)
        do {
            let api = try api(manager)
            if upgrade { _ = try await api.upgradeExtension(id, preview: preview) } else { _ = try await api.installExtension(preview: preview) }
            return finish(local, error: nil)
        } catch {
            return finish(local, error: error)
        }
    }

    /// Removes, as the hub answers when it is done (a hub with operations pushes the steps meanwhile; an older one
    /// gets a local operation). A refusal before anything started (has_dependents) is thrown for the caller to ask.
    func uninstall(_ id: String, name: String?, cascade: Bool, members: Bool, manager: String? = nil) async throws -> ExtensionRemovalResult {
        var local = stepped ? nil : ExtensionOperation(id: Self.localID(), kind: .uninstall, extensionID: id, name: name,
                                                       startedAt: .now, updatedAt: .now)
        local?.manager = manager
        if let local { received(local) }
        do {
            let api = try api(manager)
            let result = try await api.uninstallExtension(id, cascade: cascade, members: members)
            if let local { _ = finish(local, error: nil) }
            return result
        } catch let e as APIError where e.code == "has_dependents" {
            if let local { operations[key(local.id, manager: local.manager)] = nil }
            throw e
        } catch {
            if let local { _ = finish(local, error: error) }
            throw error
        }
    }

    // MARK: -

    private func finish(_ local: ExtensionOperation, error: Error?) -> ExtensionOperation {
        var op = local
        op.state = error == nil ? .succeeded : .failed
        op.error = error?.localizedDescription
        op.errorCode = (error as? APIError)?.code
        op.endedAt = .now
        op.updatedAt = .now
        received(op)
        return op
    }

    /// Waits until `done` says the hub's operation got where it was going: its events move it on, and it is read
    /// again now and then in case one was missed.
    private func follow(_ id: String, manager: String?, until done: (ExtensionOperation) -> Bool) async -> ExtensionOperation {
        let read = { [deps] () async -> ExtensionOperation? in
            guard var fresh = try? await deps.api()?.manager(manager).extensionOperation(id).operation else { return nil }
            fresh.manager = manager
            return fresh
        }
        let started = Date.now
        var lastRead = Date.now
        while true {
            let storageKey = key(id, manager: manager)
            if let op = operations[storageKey], done(op) { return requestScoped(op, manager: manager) }
            if Date.now.timeIntervalSince(started) > deps.patience || Task.isCancelled {
                var late = operations[storageKey] ?? ExtensionOperation(id: id, kind: .install, extensionID: "")
                late.state = .failed
                late.error = String(localized: "The hub hasn’t said whether it finished after a long wait. Pull down to refresh.")
                late.retryable = false
                return requestScoped(late, manager: manager)
            }
            if operations[storageKey] == nil, let fresh = await read() {
                received(fresh)
            }
            try? await Task.sleep(for: .milliseconds(200))
            let seconds = Double(deps.poll.components.seconds) + Double(deps.poll.components.attoseconds) / 1e18
            if Date.now.timeIntervalSince(lastRead) >= seconds {
                lastRead = .now
                if let fresh = await read() { received(fresh) }
            }
        }
    }

    private func resetIfAnotherHub() {
        let hub = deps.hub()
        guard hub != lastHub else { return }
        if lastHub != nil { operations = [:] }
        lastHub = hub
    }

    private static func localID() -> String { "local-" + UUID().uuidString }
}

extension ExtensionOperation {
    /// Kept by this app for a hub that does not run operations.
    var isLocal: Bool { id.hasPrefix("local-") }
}

/// The words for an operation and its steps (the hub sends ids; the words are the app's, docs/protocol/protocol.md).
enum ExtensionOperationText {
    static func verb(_ kind: ExtensionOperation.Kind) -> String {
        switch kind {
        case .install: String(localized: "Install")
        case .upgrade: String(localized: "Update [Extension Market Views 94996c]")
        case .uninstall: String(localized: "Uninstall")
        }
    }

    /// A step's name; one this app does not know shows as its id.
    static func step(_ id: String, kind: ExtensionOperation.Kind) -> String {
        switch id {
        case "download": String(localized: "Download package")
        case "verify": String(localized: "Verify checksum")
        case "unpack": String(localized: "Unpack")
        case "check": String(localized: "Check permissions and dependencies")
        case "consent": String(localized: "Waiting for your consent")
        case "tools": String(localized: "Install command-line tools")
        case "install": kind == .upgrade ? String(localized: "Switch to new version") : String(localized: "Install on hub")
        case "start": String(localized: "On [Herdr Notice 56410f]")
        case "nodes": String(localized: "Sync to computers")
        case "stop": String(localized: "Stop extension")
        case "remove": String(localized: "Delete files and records")
        default: id
        }
    }

    /// What a step says under its name: a download's bytes, the computers done, the cache.
    static func detail(_ s: ExtensionOperationStep) -> String? {
        if s.cached == true { return String(localized: "Used the package cached on the hub") }
        if let bytes = s.bytes, bytes > 0 {
            let done = bytes.storageText
            if let total = s.total, total > 0, s.state == .running {
                let all = total.storageText
                return String(localized: "\(done) / \(all)")
            }
            return done
        }
        if let m = s.machines, m.total > 0 {
            var line = String(localized: "\(m.done) of \(m.total) computers")
            if let errors = m.errors, !errors.isEmpty {
                line += " · " + errors.sorted { $0.key < $1.key }.map { "\($0.key)：\($0.value)" }.joined(separator: "；") // i18n-ignore
            }
            return line
        }
        return nil
    }

    /// A program of the tools step: "Uses the one on this machine" when the hub machine's own copy is used; else where it is.
    static func tool(_ t: ExtensionOperationStep.Tool) -> String? {
        if t.reused == true { return String(localized: "Uses the one on this machine") }
        switch t.state {
        case .running: return String(localized: "Installing…")
        case .done: return String(localized: "Installed [Extension Operations 11d8f1]")
        case .failed: return String(localized: "Didn't install")
        case .pending, .skipped: return nil
        }
    }

    /// Installing… / Waiting for your consent / Install didn't finish / Installed / Canceled.
    static func title(_ op: ExtensionOperation) -> String {
        switch (op.state, op.kind) {
        case (.running, .install): String(localized: "Installing…")
        case (.running, .upgrade): String(localized: "Upgrading [Extension Operations bc572b]")
        case (.running, .uninstall): String(localized: "Uninstalling [Extension Operations e557ff]")
        case (.needsConsent, _): String(localized: "Waiting for your consent")
        case (.failed, .install): String(localized: "Install didn’t finish")
        case (.failed, .upgrade): String(localized: "Upgrade didn’t finish")
        case (.failed, .uninstall): String(localized: "Uninstall didn’t finish")
        case (.succeeded, .install): String(localized: "Installed [Extension Market Views a8b6c3]")
        case (.succeeded, .upgrade): String(localized: "Updated [Extension Operations 45cb2f]")
        case (.succeeded, .uninstall): String(localized: "Uninstalled")
        case (.canceled, _): String(localized: "Canceled")
        }
    }

    /// The row's chip in the market's list: Installing / Waiting for You / Install failed.
    static func chip(_ op: ExtensionOperation) -> String? {
        switch (op.state, op.kind) {
        case (.running, .install): String(localized: "Installing")
        case (.running, .upgrade): String(localized: "Upgrading [Extension Operations e4ff1b]")
        case (.running, .uninstall): String(localized: "Uninstalling [Extension Operations ce2a05]")
        case (.needsConsent, _): String(localized: "Waiting for You")
        case (.failed, .install): String(localized: "Install failed")
        case (.failed, .upgrade): String(localized: "Upgrade failed")
        case (.failed, .uninstall): String(localized: "Uninstall failed")
        case (.succeeded, _), (.canceled, _): nil
        }
    }

    /// 1.0.0 → 1.1.0, or the version installed.
    static func versions(_ op: ExtensionOperation) -> String? {
        if op.kind == .upgrade, let from = op.from, let to = op.version { return "\(from) → \(to)" }
        return op.kind == .uninstall ? nil : op.version
    }

    /// Step 2 of 6 (the steps it skips not counted).
    static func progress(_ op: ExtensionOperation) -> String? {
        let shown = op.shownSteps
        guard !shown.isEmpty else { return nil }
        let done = shown.filter { $0.state == .done }.count
        let at = shown.firstIndex { $0.id == op.current?.id }.map { $0 + 1 } ?? done
        return String(localized: "Step \(max(at, 1)) of \(shown.count)")
    }

    /// 12s elapsed / 1m 5s elapsed.
    static func elapsed(since start: Date, now: Date) -> String {
        let s = max(0, Int(now.timeIntervalSince(start)))
        return s < 60 ? String(localized: "\(s)s elapsed") : String(localized: "\(s / 60)m \(s % 60)s elapsed")
    }

    /// What an older hub is doing while the request is out (no steps of its own).
    static func waitingNote(_ kind: ExtensionOperation.Kind) -> String {
        switch kind {
        case .install, .upgrade: String(localized: "The hub is downloading, verifying, unpacking and starting it; a large extension takes a minute or two. You can leave this page and come back to it.")
        case .uninstall: String(localized: "The hub is stopping it and deleting its files and data. You can leave this page and come back to it.")
        }
    }

    /// Who started it, when it was not this device's doing worth saying (the hub's automatic upgrade, the CLI).
    static func actor(_ op: ExtensionOperation) -> String? {
        guard let a = op.actor, !a.isEmpty else { return nil }
        return String(localized: "Started by \(a)")
    }
}
