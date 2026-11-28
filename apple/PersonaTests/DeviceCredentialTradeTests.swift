import Foundation
import PersonaWire
import Testing
@testable import Persona

/// After pairing, the device trades the pairing token for its own (docs/protocol/protocol.md「临时配对」), and the hub drops the
/// pairing token a couple of minutes later. Every socket on it has to move to the device token, whatever address or route
/// it took, or the app sits on 401 and Connecting… for good (#2766).
@MainActor
struct DeviceCredentialTradeTests {
    /// Nothing listens on these: attempts fail and retry, nothing reaches a network.
    let paired = ServerConfig(url: URL(string: "http://127.0.0.1:9")!, token: "jp_pairing")
    let candidate = ServerConfig(url: URL(string: "http://[::1]:9")!, token: "jp_pairing")

    final class Box<T> { var value: T; init(_ v: T) { value = v } }

    private func waitUntil(_ what: () -> Bool) async {
        let deadline = Date.now + 5
        while !what(), Date.now < deadline { try? await Task.sleep(for: .milliseconds(20)) }
    }

    @Test func aSocketOnAnotherAddressTakesTheNewToken() {
        let live = LiveConnection()
        live.suspended = true // no attempt: only the config matters here
        live.connect(candidate) // a candidate address, not the one paired
        #expect(live.renewCredential(from: "jp_pairing", to: "jd_device"))
        #expect(live.currentConfig == ServerConfig(url: candidate.url, token: "jd_device"))
        #expect(!live.renewCredential(from: "jp_pairing", to: "jd_device"), "already moved")
    }

    @Test func aSocketOnAnotherHubKeepsItsToken() {
        let live = LiveConnection()
        live.suspended = true
        let other = ServerConfig(url: URL(string: "http://127.0.0.1:10")!, token: "jd_other")
        live.connect(other)
        #expect(!live.renewCredential(from: "jp_pairing", to: "jd_device"))
        #expect(live.currentConfig == other)
    }

    /// The route picks a candidate whose config still carries the pairing token (read before the trade): the attempt
    /// goes there with the device token anyway.
    @Test func aRoutedSocketKeepsTheNewTokenOnLaterAttempts() async {
        let live = LiveConnection()
        let traded = Box<[String: String]>([:])
        let routed = Box(0)
        let candidate = candidate
        live.route = { routed.value += 1; return candidate }
        live.credential = { DeviceCredentialTrade.current($0, traded: traded.value) }
        live.connect(paired)
        await waitUntil { live.currentConfig?.url == candidate.url }
        #expect(live.currentConfig == candidate)

        traded.value["jp_pairing"] = "jd_device"
        let asked = routed.value
        #expect(live.renewCredential(from: "jp_pairing", to: "jd_device"))
        await waitUntil { routed.value > asked }
        try? await Task.sleep(for: .milliseconds(50)) // the attempt after the route answered
        #expect(live.currentConfig == ServerConfig(url: candidate.url, token: "jd_device"))
        live.disconnect()
    }

    @Test func theRegistryTakesTheTokenOnlyWhileTheHubIsOnTheOldOne() {
        var r = HubRegistry()
        var hub = PairedHub(config: paired)
        hub.group = "g2" // regrouped while the trade was out: the hub is found by its id, not its group
        r.hubs = [hub]
        let out = DeviceCredentialTrade.registry(r, hub: hub.id, old: "jp_pairing", token: "jd_device", deviceID: "dev_1")
        #expect(out?.hubs.first?.config == ServerConfig(url: paired.url, token: "jd_device"))
        #expect(out?.hubs.first?.deviceID == "dev_1")
        #expect(out?.hubs.first?.tradedPairing == DeviceCredentialTrade.fingerprint("jp_pairing"))
        #expect(out?.hubs.first?.tradedPairing?.contains("jp_") == false, "only a hash is kept")

        // Paired again with another link meanwhile: that pairing trades its own.
        r.hubs[0].config.token = "jp_newer"
        #expect(DeviceCredentialTrade.registry(r, hub: hub.id, old: "jp_pairing", token: "jd_device", deviceID: "dev_1") == nil)
    }

    @Test func theSameLinkAgainKeepsTheDeviceToken() {
        var hub = PairedHub(config: ServerConfig(url: paired.url, token: "jd_device"))
        hub.deviceID = "dev_1"
        let traded = ["jp_pairing": "jd_device"]
        #expect(DeviceCredentialTrade.keepsDevice(hub, pairing: paired, traded: traded))
        #expect(!DeviceCredentialTrade.keepsDevice(hub, pairing: ServerConfig(url: paired.url, token: "jp_fresh"), traded: traded))
        #expect(DeviceCredentialTrade.current(candidate, traded: traded) == ServerConfig(url: candidate.url, token: "jd_device"))

        // Relaunched since the trade (nothing in memory): the registry's fingerprint says so.
        hub.tradedPairing = DeviceCredentialTrade.fingerprint("jp_pairing")
        #expect(DeviceCredentialTrade.keepsDevice(hub, pairing: paired, traded: [:]))
        #expect(!DeviceCredentialTrade.keepsDevice(hub, pairing: ServerConfig(url: paired.url, token: "jp_fresh"), traded: [:]))
    }

    @Test func everyGroupRefusedSaysPairingIsGone() {
        #expect(ConnectionStatus.refined(.connecting, rejected: [true]) == .rejected)
        #expect(ConnectionStatus.refined(.connecting, rejected: [true, false]) == .connecting)
        #expect(ConnectionStatus.refined(.connecting, rejected: []) == .connecting)
        #expect(ConnectionStatus.refined(.waitingForNetwork, rejected: [true]) == .waitingForNetwork)
        #expect(ConnectionStatus.refined(.online, rejected: [true]) == .online)
    }

    @Test func previousDeviceCredentialsLookupAndClearWorkByHubIDAndURL() {
        final class MemoryKeychain: @unchecked Sendable {
            private let lock = NSLock()
            private var items: [String: Data] = [:]
            var access: KeychainAccess {
                KeychainAccess(
                    read: { [self] account in lock.withLock { items[account].map { .found($0) } ?? .missing } },
                    write: { [self] data, account in lock.withLock { items[account] = data; return true } },
                    delete: { [self] account in lock.withLock { items[account] = nil } }
                )
            }
        }
        let kc = MemoryKeychain().access
        let url = URL(string: "https://hub.example.com:8443/")!
        let sameURL = URL(string: "https://HUB.EXAMPLE.COM:8443")!
        let otherURL = URL(string: "https://other.example.com")!

        PreviousDeviceCredentials.record("dev_phone_1", slot: .device, hubID: "hub-1", url: url, kc)
        PreviousDeviceCredentials.record("dev_watch_1", slot: .watch, hubID: "hub-1", url: url, kc)
        #expect(PreviousDeviceCredentials.lookup(.device, hubID: "hub-1", url: otherURL, kc) == "dev_phone_1")
        #expect(PreviousDeviceCredentials.lookup(.device, hubID: nil, url: sameURL, kc) == "dev_phone_1")
        #expect(PreviousDeviceCredentials.lookup(.watch, hubID: "hub-1", url: otherURL, kc) == "dev_watch_1")

        PreviousDeviceCredentials.clear(.device, hubID: "hub-1", url: sameURL, kc)
        #expect(PreviousDeviceCredentials.lookup(.device, hubID: "hub-1", url: url, kc) == nil)
        #expect(PreviousDeviceCredentials.lookup(.watch, hubID: "hub-1", url: url, kc) == "dev_watch_1")
    }

    @Test func previousDeviceCredentialsSerializesConcurrentUpdates() async {
        final class SerializedMemoryKeychain: @unchecked Sendable {
            private let lock = NSLock()
            private var items: [String: Data] = [:]
            private var active = 0
            private(set) var maxOverlap = 0
            var access: KeychainAccess {
                KeychainAccess(
                    read: { [self] account in
                        lock.withLock {
                            active += 1
                            maxOverlap = max(maxOverlap, active)
                            return items[account].map { .found($0) } ?? .missing
                        }
                    },
                    write: { [self] data, account in
                        lock.withLock {
                            items[account] = data
                            active -= 1
                        }
                        return true
                    },
                    delete: { [self] account in
                        lock.withLock {
                            items[account] = nil
                            active -= 1
                        }
                    }
                )
            }
        }
        let store = SerializedMemoryKeychain()
        let kc = store.access
        await withTaskGroup(of: Void.self) { group in
            for i in 0..<20 {
                let slot: PreviousDeviceCredentials.Slot = i.isMultiple(of: 2) ? .device : .watch
                let url = URL(string: "https://hub-\(i).example.com:8443")!
                group.addTask {
                    PreviousDeviceCredentials.record("cred_\(i)", slot: slot, hubID: "hub-\(i)", url: url, kc)
                }
            }
        }
        #expect(store.maxOverlap == 1)
        for i in 0..<20 {
            let slot: PreviousDeviceCredentials.Slot = i.isMultiple(of: 2) ? .device : .watch
            let url = URL(string: "https://hub-\(i).example.com:8443")!
            #expect(PreviousDeviceCredentials.lookup(slot, hubID: "hub-\(i)", url: url, kc) == "cred_\(i)")
        }
    }

    @Test func createTokenSendsReplacesOnlyWhenProvided() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/auth/tokens", json: #"{"id":"dev_new","token":"jd_new","role":"full"}"#)
        _ = try await hub.client.createToken(name: "My iPhone", kind: "phone", replaces: "dev_old")
        _ = try await hub.client.createToken(name: "My iPhone", kind: "phone")
        let bodies = hub.sent("POST", "/api/v1/auth/tokens").compactMap { $0.body }
        #expect(bodies.count == 2)
        #expect(try JSON.decoder.decode(JSONValue.self, from: bodies[0])["replaces"] == .string("dev_old"))
        #expect(try JSON.decoder.decode(JSONValue.self, from: bodies[1])["replaces"] == nil)
    }

    @Test func createTokenRetriesOnceWithoutReplacesOn400ForDeviceAndWatch() async throws {
        let hub = FakeHub()
        let attempts = Box(0)
        hub.on("POST", "/api/v1/auth/tokens") { req in
            attempts.value += 1
            let body = try JSON.decoder.decode(JSONValue.self, from: req.body ?? Data())
            if body["replaces"] != nil {
                throw APIError(status: 400, message: "unknown field replaces")
            }
            if body["kind"] == .string("watch") {
                return Data(#"{"id":"watch_new","token":"jd_watch_new","role":"answer"}"#.utf8)
            }
            return Data(#"{"id":"dev_new","token":"jd_new","role":"full"}"#.utf8)
        }

        let deviceToken = try await hub.client.createToken(name: "My iPhone", kind: "phone", replaces: "dev_old")
        #expect(deviceToken.id == "dev_new")

        let primary = PairedHub(id: "hub-1", config: ServerConfig(url: hub.url, token: "jp_pair"))
        let watchToken = try #require(try await DeviceCredentialTrade.issueWatchToken(for: primary, api: hub.client, replaces: "watch_old"))
        #expect(watchToken.id == "watch_new")
        #expect(attempts.value == 4)

        let bodies = try hub.sent("POST", "/api/v1/auth/tokens").compactMap(\.body).map {
            try JSON.decoder.decode(JSONValue.self, from: $0)
        }
        #expect(bodies[0]["replaces"] == .string("dev_old"))
        #expect(bodies[1]["replaces"] == nil)
        #expect(bodies[2]["replaces"] == .string("watch_old"))
        #expect(bodies[3]["replaces"] == nil)

        hub.on("POST", "/api/v1/auth/tokens") { _ in
            throw APIError(status: 400, message: "bad request")
        }
        do {
            _ = try await hub.client.createToken(name: "My iPhone", kind: "phone", replaces: "dev_old")
            Issue.record("Repeated 400 should propagate")
        } catch let error as APIError {
            #expect(error.status == 400)
        }
    }

    @Test func deviceGroupsSplitAndSortByCurrentOnlineAndRecent() throws {
        let older = Date(timeIntervalSince1970: 1_700_000_000)
        let newer = Date(timeIntervalSince1970: 1_700_100_000)
        let tokens = [
            APIClient.DeviceToken(id: "phone-old", name: "Old Phone", kind: "phone", role: "full", createdAt: older, lastSeen: older),
            APIClient.DeviceToken(id: "phone-live", name: "Live Phone", kind: "phone", role: "full", createdAt: older, lastSeen: older),
            APIClient.DeviceToken(id: "phone-me", name: "This Phone", kind: "phone", role: "full", createdAt: older, lastSeen: older),
            APIClient.DeviceToken(id: "web-1", name: "Browser", kind: "web", role: "full", createdAt: newer, lastSeen: newer),
            APIClient.DeviceToken(id: "link-off", name: "Link Off", kind: "link", role: "link", createdAt: newer, lastSeen: newer),
            APIClient.DeviceToken(id: "link-on", name: "Link On", kind: "link", role: "link", createdAt: older, lastSeen: older),
            APIClient.DeviceToken(id: "scoped-1", name: "Scoped", kind: "web", role: "scoped", createdAt: newer, lastSeen: newer),
            APIClient.DeviceToken(id: "revoked-1", name: "Gone", kind: "phone", role: "full", createdAt: older, revokedAt: newer),
        ]
        let grouped = DeviceGroups.group(tokens: tokens, current: "phone-me", online: ["phone-live", "link-on"])
        #expect(grouped.sections.map(\.kind) == [.devices, .browsers, .links, .integrations])
        #expect(grouped.sections[0].tokens.map(\.id) == ["phone-me", "phone-live", "phone-old"])
        #expect(grouped.sections[1].tokens.map(\.id) == ["web-1"])
        #expect(grouped.sections[2].tokens.map(\.id) == ["link-on", "link-off"])
        #expect(grouped.sections[3].tokens.map(\.id) == ["scoped-1"])
        #expect(grouped.revoked.map(\.id) == ["revoked-1"])

        let withOnline = try JSON.decoder.decode(APIClient.DeviceTokens.self, from: Data(#"{"tokens":[],"current":"c","online":["c","l"]}"#.utf8))
        #expect(withOnline.online == ["c", "l"])
        let withoutOnline = try JSON.decoder.decode(APIClient.DeviceTokens.self, from: Data(#"{"tokens":[],"current":"c"}"#.utf8))
        #expect(withoutOnline.online == nil)

        let duplicateCurrent = [
            APIClient.DeviceToken(id: "phone-old", name: "Old Phone", kind: "phone", role: "full", createdAt: older, lastSeen: older),
            APIClient.DeviceToken(id: "phone-me", name: "This Phone", kind: "phone", role: "full", createdAt: older, lastSeen: older),
            APIClient.DeviceToken(id: "phone-me", name: "This Phone", kind: "phone", role: "full", createdAt: older, lastSeen: older),
        ]
        let dupGrouped = DeviceGroups.group(tokens: duplicateCurrent, current: "phone-me", online: [])
        #expect(dupGrouped.sections.first?.tokens.map(\.id) == ["phone-me", "phone-me", "phone-old"])
    }
}
