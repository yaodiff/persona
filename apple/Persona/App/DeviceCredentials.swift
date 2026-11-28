import CryptoKit
import PersonaWire
import SwiftUI
#if canImport(WatchConnectivity)
import WatchConnectivity
#endif

// One credential per device (docs/protocol/protocol.md「Authentication」): the pairing token opens everything, so right after pairing the
// phone trades it for a token of its own and stops keeping it, and gets the watch an answer-only one. A lost device is
// then revoked alone in Settings › Devices instead of re-pairing everything. Against an older hub (no /auth/tokens) the
// pairing token stays, as before; the next launch tries again.

extension AppModel {
    /// This device's token id at the hub-level group's hub (Settings › Devices marks it This Device); nil while it still uses the
    /// pairing token there. Each paired hub has its own (docs/protocol/multi-hub.md).
    var deviceID: String? {
        guard let hub else { return nil }
        let on = hub.config
        return (registry.hubs.first { $0.group == hub.group && $0.config == on } ?? registry.primary(hub.group))?.deviceID
    }

    /// The kind of this device's own credential at the hub (POST /auth/tokens), shown in Settings › Devices. A Mac is a
    /// desktop, not the phone (AppModel.registration): the hub keeps it as "other" (full rights, a computer's icon).
    nonisolated static var credentialKind: String {
        "phone"
    }

    /// Every paired hub trades its pairing token for this device's own (each hub separately), and every group gets
    /// the watch an answer-only one at its first hub. A call while one runs waits for it (launch, the watch asking and
    /// Sync to Apple Watch can come together; each would get the watch a token of its own).
    func upgradeCredentials() async {
        if let running = credentialsUpgrade { return await running.value }
        let run = Task { await upgradeCredentialsOnce() }
        credentialsUpgrade = run
        await run.value
        credentialsUpgrade = nil
    }

    private func upgradeCredentialsOnce() async {
        guard isPaired, !isDemo else { return }
        #if DEBUG
        if UserDefaults.standard.string(forKey: "pairURL") != nil { return } // simulator runs paired from the command line
        #endif
        for paired in registry.hubs where paired.deviceID == nil {
            let old = paired.config
            let hubID = paired.hubID
            let replaces = await Task.detached { PreviousDeviceCredentials.lookup(.device, hubID: hubID, url: old.url) }.value
            guard let t = try? await APIClient(config: old).createToken(name: UIDevice.current.name, kind: Self.credentialKind, replaces: replaces), t.role == "full" else { continue }
            adoptDeviceToken(hub: paired.id, old: old.token, token: t.token, deviceID: t.id)
        }
        #if canImport(WatchConnectivity)
        if WatchSync.shared.watchAppInstalled {
            for g in registry.groups {
                guard let primary = registry.primary(g) else { continue }
                let hubID = primary.hubID
                let url = primary.config.url
                let priorWatch = await Task.detached { PreviousDeviceCredentials.lookup(.watch, hubID: hubID, url: url) }.value
                guard let t = try? await DeviceCredentialTrade.issueWatchToken(for: primary, api: APIClient(config: primary.config), replaces: priorWatch),
                      let updated = DeviceCredentialTrade.watchRegistry(registry, expected: primary, token: t) else { continue }
                registry = updated
                saveRegistry(updated)
                let newWatchID = t.id
                Task.detached { PreviousDeviceCredentials.record(newWatchID, slot: .watch, hubID: hubID, url: url) }
            }
        }
        #endif
        pushWatchConfigs()
    }

    /// Hub `id` traded the pairing token `old` for this device's own. The registry keeps the new one, and every socket
    /// still on the old token goes on with it at once: whatever address or route it took (a candidate address, a mesh
    /// peer, the SSH tunnel), whichever group the hub is in by now (a hello may regroup it while the trade is out).
    /// Comparing whole configs missed those, and the socket stayed on the pairing token until the hub dropped it after
    /// its grace (docs/protocol/protocol.md「临时配对」): 401 on every attempt, Connecting… for good (#2766).
    func adoptDeviceToken(hub id: String, old: String, token: String, deviceID: String) {
        tradedTokens[old] = token
        // nil: paired again with another link meanwhile; that pairing trades its own.
        if let r = DeviceCredentialTrade.registry(registry, hub: id, old: old, token: token, deviceID: deviceID) {
            registry = r
            saveRegistry(r)
            buildLinks()
            if let paired = r.hubs.first(where: { $0.id == id }) {
                let hubID = paired.hubID
                let url = paired.config.url
                Task.detached { PreviousDeviceCredentials.record(deviceID, slot: .device, hubID: hubID, url: url) }
            }
        }
        for link in links where !link.isDemo { link.live.renewCredential(from: old, to: token) }
    }

    /// Settings › Devices revoked the watch's credential `id` at `group`'s hub (#1871): the registry forgets it, and the
    /// watch gets a new one (or, with none issued, the group is missing and the watch drops the revoked one).
    func watchCredentialRevoked(_ id: String, group: String) async {
        guard let r = DeviceCredentialTrade.forgettingWatch(registry, id: id, group: group) else { return }
        registry = r
        saveRegistry(r)
        await upgradeCredentials()
    }

    /// Nothing kept about this device's credentials outside the registry any more (the registry goes with `unpair`).
    func forgetDeviceCredentials() {
        UserDefaults.standard.removeObject(forKey: HubStore.legacyDeviceIDKey)
    }
}

/// Device-local memory of the last credential ID this device (and its paired watch) held at each hub.
/// Stored in Keychain with `thisDeviceOnly` so re-pairing after an offline sign-out, reinstall, or in-place
/// re-pair sends `replaces` and avoids leaving duplicate entries in Settings › Devices.
nonisolated enum PreviousDeviceCredentials {
    enum Slot: String, Sendable { case device, watch }

    private static let account = "previous-device-ids"
    private static let lock = NSLock()
    static let defaultKeychain = KeychainAccess(
        read: { Keychain.read(account: $0) },
        write: { Keychain.save($0, account: $1, thisDeviceOnly: true) },
        delete: { Keychain.delete(account: $0) }
    )

    static func keys(_ slot: Slot, hubID: String?, url: URL) -> [String] {
        var out: [String] = []
        if let hubID, !hubID.isEmpty { out.append("\(slot.rawValue):hub:\(hubID)") }
        if let urlKey = HubAddressInput.key(url) { out.append("\(slot.rawValue):url:\(urlKey)") }
        return out
    }

    static func lookup(_ slot: Slot, hubID: String?, url: URL, _ kc: KeychainAccess = defaultKeychain) -> String? {
        let ks = keys(slot, hubID: hubID, url: url)
        guard !ks.isEmpty else { return nil }
        return lock.withLock {
            let map = load(kc)
            for key in ks {
                if let id = map[key], !id.isEmpty { return id }
            }
            return nil
        }
    }

    static func record(_ id: String, slot: Slot, hubID: String?, url: URL, _ kc: KeychainAccess = defaultKeychain) {
        guard !id.isEmpty else { return }
        let ks = keys(slot, hubID: hubID, url: url)
        guard !ks.isEmpty else { return }
        lock.withLock {
            var map = load(kc)
            for key in ks { map[key] = id }
            save(map, kc)
        }
    }

    static func clear(_ slot: Slot, hubID: String?, url: URL, _ kc: KeychainAccess = defaultKeychain) {
        let ks = keys(slot, hubID: hubID, url: url)
        guard !ks.isEmpty else { return }
        lock.withLock {
            var map = load(kc)
            var changed = false
            for key in ks where map.removeValue(forKey: key) != nil { changed = true }
            if changed { save(map, kc) }
        }
    }

    private static func load(_ kc: KeychainAccess) -> [String: String] {
        guard case .found(let data) = kc.read(account),
              let decoded = try? JSONDecoder().decode([String: String].self, from: data) else { return [:] }
        return decoded
    }

    private static func save(_ map: [String: String], _ kc: KeychainAccess) {
        if map.isEmpty { kc.delete(account); return }
        guard let data = try? JSONEncoder().encode(map) else { return }
        _ = kc.write(data, account)
    }
}

/// Trading a pairing token for the device's own (pure, for the tests).
nonisolated enum DeviceCredentialTrade {
    /// Token listings expose ids, not hashes: a legacy watch token cannot safely be matched by name.
    /// Issue an answer-only replacement; when a prior watch credential ID is known, pass it in `replaces`.
    static func issueWatchToken(for primary: PairedHub, api: APIClient, replaces: String? = nil) async throws -> APIClient.NewToken? {
        guard primary.watch?.url != primary.config.url || primary.watchDeviceID == nil else { return nil }
        let prior = primary.watchDeviceID ?? replaces
        let token = try await api.createToken(name: "Apple Watch", kind: "watch", replaces: prior)
        guard token.role == "answer", !token.id.isEmpty, !token.token.isEmpty else { throw URLError(.cannotParseResponse) }
        return token
    }

    static func watchRegistry(_ r: HubRegistry, expected: PairedHub, token: APIClient.NewToken) -> HubRegistry? {
        guard token.role == "answer", !token.id.isEmpty, !token.token.isEmpty,
              r.primary(expected.group)?.id == expected.id,
              let i = r.hubs.firstIndex(where: { $0.id == expected.id }),
              r.hubs[i].config == expected.config, r.hubs[i].watch == expected.watch,
              r.hubs[i].watchDeviceID == expected.watchDeviceID else { return nil }
        var out = r
        out.hubs[i].watch = ServerConfig(url: expected.config.url, token: token.token)
        out.hubs[i].watchDeviceID = token.id
        return out
    }

    /// The registry once the watch credential `id` of a hub of `group` was revoked: that hub keeps no watch credential,
    /// so `issueWatchToken` issues a new one; nil when no hub of the group holds it.
    static func forgettingWatch(_ r: HubRegistry, id: String, group: String) -> HubRegistry? {
        guard !id.isEmpty, let i = r.hubs.firstIndex(where: { $0.group == group && $0.watchDeviceID == id }) else { return nil }
        var out = r
        out.hubs[i].watch = nil
        out.hubs[i].watchDeviceID = nil
        return out
    }

    /// The registry once hub `id` has `token` (and `deviceID`) in place of `old`; nil when the hub is gone or no longer
    /// on `old` (paired again with another link while the trade was out).
    static func registry(_ r: HubRegistry, hub id: String, old: String, token: String, deviceID: String) -> HubRegistry? {
        guard let i = r.hubs.firstIndex(where: { $0.id == id }), r.hubs[i].config.token == old else { return nil }
        var out = r
        out.hubs[i].config.token = token
        out.hubs[i].deviceID = deviceID
        out.hubs[i].tradedPairing = fingerprint(old)
        return out
    }

    /// What the registry keeps of a traded pairing token: its SHA-256, never the token.
    static func fingerprint(_ token: String) -> String {
        SHA256.hash(data: Data(token.utf8)).map { String(format: "%02x", $0) }.joined()
    }

    /// The credential a request or socket on `config` goes with: a traded pairing token comes back as the device's
    /// token, at the same address.
    static func current(_ config: ServerConfig, traded: [String: String]) -> ServerConfig {
        guard let token = traded[config.token], token != config.token else { return config }
        var c = config
        c.token = token
        return c
    }

    /// A pairing link whose token was already traded for `hub`'s own credential (the same link handled twice): the hub
    /// keeps the device token instead of going back to the pairing one.
    static func keepsDevice(_ hub: PairedHub, pairing: ServerConfig, traded: [String: String]) -> Bool {
        guard hub.deviceID != nil, pairing.token != hub.config.token else { return false }
        if let token = traded[pairing.token] { return hub.config.token == token }
        return hub.tradedPairing == fingerprint(pairing.token) // traded at an earlier launch
    }
}

/// Grouping and ordering for Settings › Devices, shared with web and Android (pure, for the tests).
nonisolated enum DeviceGroupKind: String, CaseIterable, Hashable, Sendable {
    case devices, browsers, links, integrations
}

nonisolated enum DeviceGroups {
    struct Section: Identifiable, Hashable, Sendable {
        var kind: DeviceGroupKind
        var tokens: [APIClient.DeviceToken]
        var id: DeviceGroupKind { kind }
    }

    static func kind(of token: APIClient.DeviceToken) -> DeviceGroupKind {
        if token.role == "scoped" { return .integrations }
        if token.kind == "web" { return .browsers }
        if token.kind == "link" { return .links }
        if token.kind == "phone" || token.kind == "watch" || (token.kind == "other" && token.role == "full") {
            return .devices
        }
        return .integrations
    }

    static func group(
        tokens: [APIClient.DeviceToken],
        current: String?,
        online: Set<String>
    ) -> (sections: [Section], revoked: [APIClient.DeviceToken]) {
        var buckets: [DeviceGroupKind: [APIClient.DeviceToken]] = [:]
        var revoked: [APIClient.DeviceToken] = []
        for token in tokens {
            if token.revokedAt != nil {
                revoked.append(token)
            } else {
                buckets[kind(of: token), default: []].append(token)
            }
        }
        let byActivity: (APIClient.DeviceToken, APIClient.DeviceToken) -> Bool = { a, b in
            if a.id == b.id { return false }
            let aCur = a.id == current, bCur = b.id == current
            if aCur != bCur { return aCur }
            let aOn = online.contains(a.id), bOn = online.contains(b.id)
            if aOn != bOn { return aOn }
            let ta = a.lastSeen ?? a.createdAt ?? .distantPast
            let tb = b.lastSeen ?? b.createdAt ?? .distantPast
            if ta != tb { return ta > tb }
            return a.id < b.id
        }
        let sections = DeviceGroupKind.allCases.compactMap { k -> Section? in
            guard var items = buckets[k], !items.isEmpty else { return nil }
            items.sort(by: byActivity)
            return Section(kind: k, tokens: items)
        }
        revoked.sort { ($0.revokedAt ?? .distantPast) > ($1.revokedAt ?? .distantPast) }
        return (sections, revoked)
    }
}

/// Settings › Devices: every device with its own credential, revocable one by one.
struct DevicesView: View {
    @Environment(AppModel.self) private var model
    @State private var tokens: [APIClient.DeviceToken] = []
    @State private var groups: [DeviceGroups.Section] = []
    @State private var revoked: [APIClient.DeviceToken] = []
    @State private var current: String?
    @State private var online: Set<String> = []
    /// Per credential, how the hub pushes to its device (#5998); empty from an older hub.
    @State private var push: [String: APIClient.PushRoute] = [:]
    /// Which device takes the phone tools' calls, and the choices it lists (`PhonePrimaryChoices`); nil from an older hub.
    @State private var phone: APIClient.PhonePrimary?
    @State private var phoneOptions: [PhonePrimaryChoices.Option] = []
    /// A choice is on its way to the hub: one save at a time.
    @State private var savingPhone = false
    @State private var loaded = false
    @State private var revoking: APIClient.DeviceToken?
    @State private var connecting = false
    /// Scan Browser Sign-in Code open (#10293).
    @State private var scanningBrowser = false

    var body: some View {
        SettingsForm {
            Section {
                if canApproveBrowsers {
                    Button { scanningBrowser = true } label: {
                        Label("Scan Browser Sign-in Code", systemImage: Theme.Icon.scanCode).lineLimit(1)
                    }
                    .accessibilityIdentifier("devices.scanBrowser")
                }
                Button { connecting = true } label: {
                    Label("Connect New Device", systemImage: Theme.Icon.pairing).lineLimit(1)
                }
                .accessibilityIdentifier("devices.connect")
            } footer: {
                Text("Give another phone, Mac or browser a one-time pairing code, without going to the hub machine.")
            }
            ForEach(Array(groups.enumerated()), id: \.element.id) { index, section in
                Section {
                    ForEach(section.tokens) { t in row(t) }
                } header: {
                    Text(groupTitle(section.kind))
                } footer: {
                    if index == groups.count - 1 {
                        Text("Each device has its own credentials. If you lose one, revoke just that one; the others keep working. A watch can only view and answer; it can't change settings or start and stop agents.")
                    }
                }
            }
            if PhonePrimaryChoices.isShown(phone) {
                Section {
                    Picker(selection: Binding(
                        get: { PhonePrimaryChoices.selection(phone: phone, options: phoneOptions) },
                        set: { id in Task { await choosePhone(id) } }
                    )) {
                        ForEach(phoneOptions) { o in Text(optionTitle(o)).tag(o.id) }
                    } label: {
                        Text("Primary Device").lineLimit(1)
                    }
                    .disabled(savingPhone)
                    .accessibilityIdentifier("devices.phonePrimary")
                } footer: {
                    Text("When Persona needs your phone (your location, Health, reminders), it asks this device first. If it can't be reached, another of your devices answers and Persona says which. Automatic uses the device you used most recently.")
                }
            }
            if !revoked.isEmpty {
                Section(String(localized: "section.revoked", defaultValue: "Revoked")) {
                    ForEach(revoked) { t in row(t).foregroundStyle(.secondary) }
                }
            }
        }
        .overlay {
            if loaded, tokens.isEmpty {
                ContentUnavailableView("No Device Credentials Yet", systemImage: Theme.Icon.credentials,
                                       description: Text(model.deviceID == nil ? Self.upgradeNote : ""))
            }
        }
        .themedPage()
        .detailTitle(String(localized: "Devices"))
        .task { await load() }
        .refreshable { await load() }
        .sheet(isPresented: $connecting, onDismiss: { Task { await load() } }) {
            ConnectDeviceSheet { Task { await load() } }
        }
        .sheet(isPresented: $scanningBrowser, onDismiss: { Task { await load() } }) {
            BrowserLoginSheet()
        }
        #if DEBUG
        .task { // `-openConnectDevice YES | web`: Connect New Device open (on Browser), for screenshots
            if UserDefaults.standard.string(forKey: "openConnectDevice") != nil { try? await Task.sleep(for: .seconds(1)); connecting = true }
        }
        .task { // `-showBrowserLoginSample lan | internet | unknown | number | signedIn | voided | expired`: that step, sample data
            guard BrowserLoginSample.current != nil else { return }
            try? await Task.sleep(for: .seconds(1))
            scanningBrowser = true
        }
        #endif
        .accessibleConfirmationDialog(revoking.map { String(localized: "Revoke “\($0.name)”?") } ?? "", isPresented: Binding(
            get: { revoking != nil }, set: { if !$0 { revoking = nil } }
        ), titleVisibility: .visible, presenting: revoking) { t in
            Button(t.id == current ? String(localized: "Revoke and Unpair") : String(localized: "action.revoke", defaultValue: "Revoke"), role: .destructive) { Task { await revoke(t) } }
        } message: { t in
            Text(t.id == current ? Self.revokeSelfNote : String(localized: "That device disconnects right away and has to be paired again."))
        }
    }

    #if DEBUG
    /// `-showBrowserLoginSample`, read once.
    private static let browserSampleOn = UserDefaults.standard.string(forKey: "showBrowserLoginSample") != nil
    #endif

    /// The hub matches a number (`browser_qr_match`): only then can this device approve a browser.
    private var canApproveBrowsers: Bool {
        #if DEBUG
        if Self.browserSampleOn { return true }
        #endif
        return model.hubFeatures.contains(APIClient.browserQRMatchFeature)
    }

    private static let upgradeNote = String(localized: "After the hub updates, this iPhone switches to its own credentials automatically.")
    private static let revokeSelfNote = String(localized: "This iPhone disconnects right away. Scan the code again to pair.")

    private func groupTitle(_ kind: DeviceGroupKind) -> String {
        switch kind {
        case .devices: String(localized: "Devices")
        case .browsers: String(localized: "Browsers")
        case .links: String(localized: "Computers")
        case .integrations: String(localized: "Integrations")
        }
    }

    private func icon(for t: APIClient.DeviceToken) -> String {
        switch t.kind {
        case "watch": Theme.Icon.watch
        case "phone": Theme.Icon.phone
        case "web": Theme.Icon.web
        case "link": Theme.Icon.laptop
        default: Theme.Icon.credentials
        }
    }

    private func row(_ t: APIClient.DeviceToken) -> some View {
        HStack(spacing: Theme.Space.m) {
            ThemedIcon(icon(for: t))
                .scaledFrame(width: Theme.Size.icon)
                .foregroundStyle(Theme.accent)
            VStack(alignment: .leading, spacing: Theme.Space.xxs) {
                NameWithChips {
                    Text(t.name).lineLimit(1)
                } chips: {
                    if t.id == current { Chip(text: String(localized: "This Device"), look: .outline) }
                    if t.revokedAt == nil, t.id != current, online.contains(t.id) {
                        Chip(text: String(localized: "Online"), look: .tint(Theme.positive))
                    }
                    if t.role == "answer" { Chip(text: String(localized: "Answer Only")) }
                    if t.role == "scoped" { Chip(text: String(localized: "Scoped Access")) }
                }
                Text(detail(t)).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                if t.revokedAt == nil, let route = push[t.id] { DevicePushLine(route: route) }
            }
            Spacer(minLength: 0)
            if t.revokedAt == nil { revokeButton(t) }
        }
    }

    @ViewBuilder private func revokeButton(_ t: APIClient.DeviceToken) -> some View {
        if t.id == current, model.hubSignOut == .group(model.hub?.group ?? "") {
            RowProgress() // Revoke and Unpair signing this device out of the group's hubs
        } else {
            Button(String(localized: "action.revoke", defaultValue: "Revoke"), role: .destructive) { revoking = t }
                .buttonStyle(.borderless)
                .lineLimit(1)
                .disabled(model.hubSignOut != nil)
        }
    }

    private func detail(_ t: APIClient.DeviceToken) -> String {
        if let r = t.revokedAt { return String(localized: "Revoked \(r.formatted(.relative(presentation: .named)))") }
        if let s = t.lastSeen { return String(localized: "Last used \(s.formatted(.relative(presentation: .named)))") }
        return String(localized: "Added \(t.createdAt?.formatted(date: .abbreviated, time: .shortened) ?? "")")
    }

    private func optionTitle(_ o: PhonePrimaryChoices.Option) -> String {
        guard let device = o.device else { return String(localized: "Automatic") }
        return o.isCurrent ? String(localized: "\(device.name) · This Device") : device.name
    }

    /// The page's phone choice and the options listed for it, together.
    private func apply(phone new: APIClient.PhonePrimary?) {
        phone = new
        phoneOptions = PhonePrimaryChoices.options(phone: new, tokens: tokens, current: current)
    }

    /// The choice shows at once; the hub's answer then replaces it, and a failure puts the old one back.
    private func choosePhone(_ id: String) async {
        guard !savingPhone, let api = model.api,
              id != PhonePrimaryChoices.selection(phone: phone, options: phoneOptions) else { return }
        let before = phone
        savingPhone = true
        apply(phone: PhonePrimaryChoices.choosing(id, in: phone))
        do {
            apply(phone: try await api.setPhonePrimary(id))
        } catch {
            apply(phone: before)
            model.show(error)
        }
        savingPhone = false
    }

    private func load() async {
        guard let api = model.api else { return }
        do {
            let r = try await api.deviceTokens()
            let onlineSet = Set(r.online ?? [])
            let grouped = DeviceGroups.group(tokens: r.tokens, current: r.current, online: onlineSet)
            (tokens, current, online, push, groups, revoked) = (r.tokens, r.current, onlineSet, r.push ?? [:], grouped.sections, grouped.revoked)
            apply(phone: savingPhone ? phone : r.phone) // a save on its way answers for itself
        } catch {
            model.show(error)
        }
        loaded = true
    }

    private func revoke(_ t: APIClient.DeviceToken) async {
        // This device's own credential at the hub-level group's hub: the group's hubs sign it out (its watch's
        // credential first, which needs this one) and the group goes; the others stay.
        if t.id == current { await model.disconnectGroup(model.hub?.group ?? ""); return }
        guard let api = model.api else { return }
        do {
            try await api.revokeToken(t.id)
            Haptics.success()
            await model.watchCredentialRevoked(t.id, group: model.hub?.group ?? "")
            await load()
        } catch {
            model.show(error)
        }
    }
}
