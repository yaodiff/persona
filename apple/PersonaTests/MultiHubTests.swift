import CryptoKit
import Foundation
import Security
import Testing
import UserNotifications
@testable import Persona
import PersonaWire

/// Several hubs on one device (docs/protocol/multi-hub.md): keys, grouping, routes, events, the merged Live Activity, the push
/// keys. The default group's keys and JSON must stay byte for byte what they were before several hubs.
struct MultiHubTests {
    // MARK: Keys

    @Test func defaultGroupKeysAreWhatTheyWere() throws {
        let k = SessionKey(machineID: "mac", sessionID: "s1")
        #expect(k.path == "mac/s1")
        #expect(String(decoding: try JSONEncoder().encode(k), as: UTF8.self).contains("hubGroup") == false)
        // What an older build wrote decodes into the default group.
        let old = try JSONDecoder().decode(SessionKey.self, from: Data(#"{"machineID":"mac","sessionID":"s1"}"#.utf8))
        #expect(old == k && old.group.isEmpty)
        #expect(MachineRef(id: "mac").path == "mac")
        var s = try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"mac","session_id":"s1"}"#.utf8))
        #expect(s.id == "mac/s1" && s.key == k)
        s.group = ""
        #expect(s.hubGroup == nil) // "" is no group in the JSON
    }

    @Test func anotherGroupsKeysCarryIt() throws {
        let k = SessionKey(group: "g1", machineID: "mac", sessionID: "s1")
        #expect(k.path == "g1|mac/s1")
        #expect(SessionKey(path: "g1|mac/s1") == k)
        #expect(SessionKey(path: "mac/s1") == SessionKey(machineID: "mac", sessionID: "s1"))
        #expect(SessionKey(path: "mac") == nil)
        // A session id with a "|" in it is still the default group's.
        #expect(SessionKey(path: "mac/a|b")?.group == "")
        #expect(try JSONDecoder().decode(SessionKey.self, from: JSONEncoder().encode(k)) == k)
        #expect(k != SessionKey(machineID: "mac", sessionID: "s1"))
        #expect(MachineRef(path: "g1|mac") == MachineRef(group: "g1", id: "mac"))
        var ix = try JSON.decoder.decode(Interaction.self, from: Data(#"{"id":"ix1","machine_id":"mac","session_id":"s1","kind":"question"}"#.utf8))
        #expect(ix.uid == "ix1")
        ix.group = "g1"
        #expect(ix.uid == "g1|ix1" && ix.sessionKey == k && ix.id == "ix1")
    }

    @Test func machinesDecodeTheMeshFields() throws {
        let json = #"{"id":"macbook","name":"MacBook","kind":"ssh","online":true,"hub_id":"hub-c","hub_name":"公司 2","machine_uid":"u1","route":{"via":"peer","hops":1,"rtt_ms":24}}"#
        var m = try JSON.decoder.decode(Machine.self, from: Data(json.utf8))
        #expect(m.hubID == "hub-c" && m.hubName == "公司 2" && m.machineUID == "u1")
        #expect(m.route == MachineHubRoute(via: "peer", hops: 1, rttMS: 24))
        #expect(m.title == "MacBook") // one group: just the name
        m.groupLabel = "公司"
        #expect(m.title == "MacBook · 公司")
        // A route of an unexpected shape is no route, not a lost machine.
        let odd = try JSON.decoder.decode(Machine.self, from: Data(#"{"id":"x","name":"x","kind":"ssh","online":false,"route":"peer"}"#.utf8))
        #expect(odd.route == nil)
    }

    // MARK: Grouping

    private static func hub(_ id: String, group: String, hubID: String?, mesh: [String] = [], url: String? = nil) -> PairedHub {
        PairedHub(id: id, config: ServerConfig(url: URL(string: url ?? "http://\(id).example.com")!, token: "t-\(id)"), group: group,
                  hubID: hubID, mesh: mesh.isEmpty ? nil : mesh.map { MeshHub(hubID: $0) })
    }

    @Test func onlyTheFirstPairingTakesTheDefaultID() {
        var r = HubRegistry()
        #expect(r.newGroupID() == "")
        r.hubs = [Self.hub("a", group: "", hubID: "A")]
        let next = r.newGroupID()
        #expect(!next.isEmpty && !next.contains("|") && !next.contains("/"))
        // The default group gone, the others stay and no new group becomes the default one.
        r.hubs = [Self.hub("b", group: "g1", hubID: "B")]
        #expect(!r.newGroupID().isEmpty)
    }

    @Test func hubsThatListEachOtherMergeIntoTheEarliestGroup() {
        let r = HubRegistry(hubs: [Self.hub("a", group: "", hubID: "A"),
                                   Self.hub("b", group: "g1", hubID: "B", mesh: ["B", "C"]),
                                   Self.hub("c", group: "g2", hubID: "C")],
                            groupNames: ["g2": "旧名字"])
        let out = HubGrouping.regroup(r)
        #expect(out.hubs.map(\.group) == ["", "g1", "g1"])
        #expect(out.groups == ["", "g1"])
        #expect(out.groupNames.isEmpty) // the merged-away group's name goes with it
    }

    @Test func oneSidedListingIsEnoughAndChainsJoin() {
        // A lists B, C lists B: one group (connected through B).
        let r = HubRegistry(hubs: [Self.hub("a", group: "", hubID: "A", mesh: ["B"]),
                                   Self.hub("b", group: "g1", hubID: "B"),
                                   Self.hub("c", group: "g2", hubID: "C", mesh: ["B"])])
        #expect(HubGrouping.regroup(r).hubs.map(\.group) == ["", "", ""])
    }

    @Test func aSplitKeepsTheIDWithTheEarliestHub() {
        // B and C were one group; C no longer lists B, and B no longer lists C.
        let r = HubRegistry(hubs: [Self.hub("b", group: "g1", hubID: "B"), Self.hub("c", group: "g1", hubID: "C")])
        let out = HubGrouping.regroup(r)
        #expect(out.hubs[0].group == "g1")
        #expect(out.hubs[1].group != "g1" && !out.hubs[1].group.isEmpty)
        // Stable: grouping again changes nothing.
        #expect(HubGrouping.regroup(out) == out)
    }

    @Test func theSameHubPairedTwiceIsOneGroup() {
        let r = HubRegistry(hubs: [Self.hub("a", group: "", hubID: "A"), Self.hub("a2", group: "g1", hubID: "A")])
        #expect(HubGrouping.regroup(r).hubs.map(\.group) == ["", ""])
    }

    @Test func unknownHubsStayApart() {
        let r = HubRegistry(hubs: [Self.hub("a", group: "", hubID: nil, mesh: ["B"]), Self.hub("b", group: "g1", hubID: nil)])
        #expect(HubGrouping.regroup(r).hubs.map(\.group) == ["", "g1"])
    }

    @Test func groupNamesAndLookups() {
        var r = HubRegistry(hubs: [Self.hub("a", group: "", hubID: "A"), Self.hub("b", group: "g1", hubID: "B", mesh: ["B", "Z"])])
        r.hubs[1].hubName = "公司"
        #expect(r.name("g1") == "公司")
        #expect(r.name("") == "a.example.com") // no hub_name yet: its host
        r.groupNames["g1"] = "工作"
        #expect(r.name("g1") == "工作")
        #expect(r.group(hubID: "Z") == "g1") // listed in a paired hub's mesh: that group
        #expect(r.group(hubID: "nope") == nil)
        #expect(r.primary("g1")?.id == "b")
    }

    // MARK: Routes

    @Test func candidatesArePairedHubsThenTheMeshAddresses() {
        var b = Self.hub("b", group: "g1", hubID: "B", url: "http://127.0.0.1:18761")
        b.mesh = [MeshHub(hubID: "B", endpoints: ["http://127.0.0.1:18761"]),
                  MeshHub(hubID: "C", online: true, endpoints: ["http://127.0.0.1:18762", "ftp://x"]),
                  MeshHub(hubID: "D", online: false, endpoints: ["http://127.0.0.1:18763"])]
        let r = HubRegistry(hubs: [Self.hub("a", group: "", hubID: "A"), b])
        let c = HubRoute.candidates(r, group: "g1")
        #expect(c.map(\.config.url.absoluteString) == ["http://127.0.0.1:18761", "http://127.0.0.1:18762"])
        #expect(c.map(\.hub) == ["b", nil])
        #expect(c[1].config.token == "t-b") // credentials work on every hub of a group: the group's first
        #expect(HubRoute.candidates(r, group: "").count == 1)
    }

    @Test func pickTakesWhoAnswersAndFallsBackToTheFirst() async {
        let one = HubRoute.Candidate(config: ServerConfig(url: URL(string: "http://one.example.com")!, token: "t"), hub: "1")
        let two = HubRoute.Candidate(config: ServerConfig(url: URL(string: "http://two.example.com")!, token: "t"), hub: "2")
        let picked = await HubRoute.pick([one, two]) { $0.url.host() == "two.example.com" }
        #expect(picked == two)
        let none = await HubRoute.pick([one, two]) { _ in false }
        #expect(none == one)
        let asked = Counter()
        let alone = await HubRoute.pick([one]) { _ in asked.bump(); return false } // one place: no probe at all
        #expect(alone == one && asked.count == 0)
    }

    @Test func theCenterComesFirst() {
        // Paired hub b is a member; the center C is known only from b's mesh.
        var b = Self.hub("b", group: "g1", hubID: "B", url: "http://127.0.0.1:18761")
        b.mesh = [MeshHub(hubID: "B", endpoints: ["http://127.0.0.1:18761"]),
                  MeshHub(hubID: "C", online: true, endpoints: ["http://127.0.0.1:18762"], center: true)]
        let r = HubRegistry(hubs: [b])
        #expect(HubRoute.centerID(r, group: "g1") == "C")
        let c = HubRoute.candidates(r, group: "g1")
        #expect(c.map(\.config.url.absoluteString) == ["http://127.0.0.1:18762", "http://127.0.0.1:18761"])
        #expect(c.map(\.center) == [true, false])
        // The center paired here too: that one is the center candidate.
        var cHub = Self.hub("c", group: "g1", hubID: "C", url: "http://127.0.0.1:18762")
        cHub.mesh = b.mesh
        let both = HubRoute.candidates(HubRegistry(hubs: [b, cHub]), group: "g1")
        #expect(both.first?.hub == "c" && both.first?.center == true && both.filter(\.center).count == 1)
        // A group whose hellos name no center (hubs before the single center): the order of before, no center.
        var old = Self.hub("b", group: "g1", hubID: "B", url: "http://127.0.0.1:18761")
        old.mesh = [MeshHub(hubID: "B"), MeshHub(hubID: "D", endpoints: ["http://127.0.0.1:18763"])]
        let plain = HubRoute.candidates(HubRegistry(hubs: [old]), group: "g1")
        #expect(plain.map(\.hub) == ["b", nil] && !plain.contains(where: \.center))
    }

    @Test func pickPrefersTheCenter() async {
        let member = HubRoute.Candidate(config: ServerConfig(url: URL(string: "http://member.example.com")!, token: "t"), hub: "m")
        let center = HubRoute.Candidate(config: ServerConfig(url: URL(string: "http://center.example.com")!, token: "t"), hub: nil, center: true)
        // The member answers first, the center a moment later, within the grace: the center.
        let slowCenter = await HubRoute.pick([center, member]) { config in
            if config.url.host() == "center.example.com" { try? await Task.sleep(for: .milliseconds(100)) }
            return true
        }
        #expect(slowCenter == center)
        // The center does not answer: the member, without waiting out the grace.
        let start = ContinuousClock.now
        let down = await HubRoute.pick([center, member], grace: .seconds(30)) { $0.url.host() == "member.example.com" }
        #expect(down == member)
        #expect(ContinuousClock.now - start < .seconds(5))
        // The center hangs: the member once the grace is over.
        let hanging = await HubRoute.pick([center, member], grace: .milliseconds(50)) { config in
            if config.url.host() == "center.example.com" { try? await Task.sleep(for: .seconds(10)); return true }
            return true
        }
        #expect(hanging == member)
        // Nobody answers: the center, for the socket's own retries.
        let none = await HubRoute.pick([center, member]) { _ in false }
        #expect(none == center)
    }

    @Test func helloMarksTheCenter() throws {
        let json = #"{"type":"hello","hub_id":"hub-b","mesh":{"hubs":[{"hub_id":"hub-a","center":true},{"hub_id":"hub-b","self":true}]}}"#
        guard case .hello(let h)? = ServerEvent.decode(Data(json.utf8)) else { Issue.record("no hello"); return }
        #expect(h.mesh?.map(\.center) == [true, nil])
    }

    // MARK: Events

    @Test func helloCarriesTheHubAndItsGroup() throws {
        let json = #"{"type":"hello","features":["tls"],"hub_id":"hub-b","hub_name":"公司","endpoints":[{"url":"http://x"}],"mesh":{"hubs":[{"hub_id":"hub-b","name":"公司","online":true,"self":true,"endpoints":["http://127.0.0.1:18761"]},{"hub_id":"hub-c","name":"公司 2"}]}}"#
        guard case .hello(let h)? = ServerEvent.decode(Data(json.utf8)) else { Issue.record("no hello"); return }
        #expect(h.features == ["tls"] && h.hubID == "hub-b" && h.hubName == "公司")
        #expect(h.mesh?.map(\.hubID) == ["hub-b", "hub-c"])
        #expect(h.mesh?.first?.isSelf == true)
        // An older hub's hello, and a mesh of another shape: still a hello.
        guard case .hello(let old)? = ServerEvent.decode(Data(#"{"type":"hello","features":[]}"#.utf8)) else { Issue.record("old"); return }
        #expect(old.mesh == nil && old.hubID == nil)
        guard case .hello(let odd)? = ServerEvent.decode(Data(#"{"type":"hello","mesh":"x"}"#.utf8)) else { Issue.record("odd"); return }
        #expect(odd.mesh == nil)
    }

    @Test func eventsAreStampedWithTheirGroup() throws {
        let json = #"{"type":"sessions","machine_id":"mac","sessions":[{"session_id":"s1"}]}"#
        let ev = try #require(ServerEvent.decode(Data(json.utf8)))
        guard case .sessions(_, let list) = ev.stamped("g1") else { Issue.record("sessions"); return }
        #expect(list.first?.key == SessionKey(group: "g1", machineID: "mac", sessionID: "s1"))
        guard case .sessions(_, let plain) = ev.stamped("") else { Issue.record("plain"); return }
        #expect(plain.first?.group == "")
        let items = try #require(ServerEvent.decode(Data(#"{"type":"delta","machine_id":"mac","session_id":"s1","text":"x"}"#.utf8)))
        guard case .delta(let k, _) = items.stamped("g1") else { Issue.record("delta"); return }
        #expect(k.group == "g1")
    }

    // MARK: Live Activity

    @Test func fleetsMergeAndAnotherGroupsRowsSayTheirGroup() {
        let a = FleetState(phase: .working, working: 2, items: [.init(id: "mac/s1", title: "A", kind: .working)], workingSince: 100)
        let b = FleetState(phase: .needsYou, needsYou: 1, unread: 1,
                           items: [.init(id: "mac/s9", title: "B", kind: .permission, interaction: "ix1"), .init(id: "mac/s8", title: "C", kind: .unread)],
                           workingSince: 50)
        let m = FleetMerge.merge([("", a), ("g1", b)], base: a)
        #expect(m.phase == .needsYou && m.needsYou == 1 && m.working == 2 && m.unread == 1 && m.workingSince == 50)
        #expect(m.items.map(\.id) == ["g1|mac/s9", "mac/s1", "g1|mac/s8"]) // requests first
        #expect(m.items.first?.interaction == "g1|ix1")
        #expect(FleetMerge.split("g1|ix1") == ("g1", "ix1"))
        #expect(FleetMerge.split("ix1") == (nil, "ix1"))
        // The row's deep link names the group too ("g1|mac" / "s9"), read back as the key.
        let key = m.items.first?.key
        #expect(key.flatMap { SessionKey(path: $0.machine + "/" + $0.session) } == SessionKey(group: "g1", machineID: "mac", sessionID: "s9"))
    }

    // MARK: Connection

    @MainActor @Test func severalGroupsConnectOnlyWhileNoneIsUp() {
        let p = ConnectionStatus.phase(network:links:catchingUp:)
        #expect(p(true, [(.connected, false), (.failed("x"), false)], false) == .online) // one group down: the rest is live
        #expect(p(true, [(.connecting, false), (.failed("x"), false)], false) == .connecting)
        #expect(p(true, [(.connected, true), (.failed("x"), false)], false) == .connecting) // the only one up is stalled
        #expect(p(false, [(.connected, false), (.connected, false)], false) == .waitingForNetwork)
        #expect(p(true, [(.connected, false), (.idle, false)], true) == .updating) // an idle one is not counted
        // One group: exactly the one-link rule.
        #expect(p(true, [(.connected, true)], false) == .connecting)
        #expect(p(true, [], false) == .online)
    }

    // MARK: Push keys

    @Test func eachGroupsKeyOpensItsOwnPushesAndSaysWhose() throws {
        let home = P256.KeyAgreement.PrivateKey(), work = P256.KeyAgreement.PrivateKey()
        let payload: [String: Any] = ["aps": ["alert": ["title": "需要你授权"]], "machine_id": "mac", "session_id": "s1",
                                      "iat": Date.now.timeIntervalSince1970]
        let sealed = try Self.seal(payload, to: work.publicKey)
        let content = UNMutableNotificationContent()
        content.body = "有新消息"
        content.userInfo = ["e": sealed]
        let (out, outcome) = PushCrypto.service(content, keys: { [("", home), ("g1", work)] })
        #expect(outcome == .opened && out.title == "需要你授权")
        #expect(out.userInfo[PushCrypto.groupKey] as? String == "g1")
        // The authenticated default key overrides any untrusted outer group, including a non-default one.
        let mine = try Self.seal(payload, to: home.publicKey)
        content.userInfo = ["e": mine, PushCrypto.groupKey: "untrusted"]
        let (plain, _) = PushCrypto.service(content, keys: { [("", home), ("g1", work)] })
        #expect(plain.userInfo[PushCrypto.groupKey] as? String == "")
        #expect(plain.userInfo[LegacyNames.pushGroupKey] as? String == "")
        #expect(PushCrypto.opened(["e": sealed], keys: [("", home), ("g1", work)])?.group == "g1")
        #expect(PushCrypto.account(group: "") == "push-key" && PushCrypto.account(group: "g1") == "push-key.g1")
    }

    /// What the hub does (pushcrypto): HPKE to the phone's key, the encapsulated key first.
    static func seal(_ payload: [String: Any], to key: P256.KeyAgreement.PublicKey) throws -> String {
        var sender = try HPKE.Sender(recipientKey: key, ciphersuite: PushCrypto.suite, info: PushCrypto.info)
        let body = try sender.seal(JSONSerialization.data(withJSONObject: payload))
        return (sender.encapsulatedKey + body).base64EncodedString()
    }
}

private final class Counter: @unchecked Sendable {
    private let lock = NSLock()
    private var n = 0
    var count: Int { lock.withLock { n } }
    func bump() { lock.withLock { n += 1 } }
}

/// The pairing must not get lost (docs/protocol/multi-hub.md「Storage」): a keychain that cannot be read now is unknown, not empty,
/// and nothing is written over it; the old single pairing goes only once the registry is written and reads back.
struct HubStoreTests {
    private static func defaults() -> UserDefaults {
        let name = "HubStoreTests." + UUID().uuidString
        let d = UserDefaults(suiteName: name)!
        d.removePersistentDomain(forName: name)
        return d
    }

    private static let legacy = ServerConfig(url: URL(string: "http://hub.example.com:18660")!, token: "t")
    private static let watch = ServerConfig(url: URL(string: "http://hub.example.com:18660")!, token: "w")

    private static func withLegacy(_ kc: FakeKeychain, _ d: UserDefaults) throws {
        kc.items[Keychain.legacyServerAccount] = try JSONEncoder().encode(legacy)
        kc.items[Keychain.legacyWatchAccount] = try JSONEncoder().encode(watch)
        d.set("dev_old", forKey: HubStore.legacyDeviceIDKey)
    }

    @Test func theOldPairingMovesIntoTheDefaultGroup() throws {
        let kc = FakeKeychain(), d = Self.defaults()
        try Self.withLegacy(kc, d)
        guard case .registry(let r) = HubStore.read(kc.access, defaults: d) else { Issue.record("not read"); return }
        #expect(r.hubs.count == 1 && r.hubs[0].group == "" && r.hubs[0].config == Self.legacy)
        #expect(r.hubs[0].deviceID == "dev_old" && r.hubs[0].watch == Self.watch)
        #expect(kc.items["hubs"] != nil)
        #expect(kc.items[Keychain.legacyServerAccount] == nil && kc.items[Keychain.legacyWatchAccount] == nil)
        #expect(d.string(forKey: HubStore.legacyDeviceIDKey) == nil)
        // Read again: the same registry, from its own item.
        #expect(HubStore.read(kc.access, defaults: d) == .registry(r))
    }

    @Test func aMigrationThatDidNotStickKeepsTheOldItems() throws {
        let kc = FakeKeychain(), d = Self.defaults()
        try Self.withLegacy(kc, d)
        kc.writesFail = true
        guard case .registry(let r) = HubStore.read(kc.access, defaults: d) else { Issue.record("not read"); return }
        #expect(r.hubs.first?.config == Self.legacy) // this launch goes on from memory
        #expect(kc.items["hubs"] == nil)
        #expect(kc.items[Keychain.legacyServerAccount] != nil && kc.items[Keychain.legacyWatchAccount] != nil)
        #expect(d.string(forKey: HubStore.legacyDeviceIDKey) == "dev_old")
        // A write that says it worked but does not read back the same: still kept.
        kc.writesFail = false
        kc.writesGarble = true
        _ = HubStore.read(kc.access, defaults: d)
        #expect(kc.items[Keychain.legacyServerAccount] != nil && d.string(forKey: HubStore.legacyDeviceIDKey) == "dev_old")
        // The next launch, with a keychain that takes it: moved and dropped.
        kc.writesGarble = false
        kc.items["hubs"] = nil
        guard case .registry(let again) = HubStore.read(kc.access, defaults: d) else { Issue.record("not read"); return }
        #expect(again.hubs.first?.config == Self.legacy && kc.items[Keychain.legacyServerAccount] == nil)
    }

    @Test func aReadThatFailsIsUnknownAndTouchesNothing() throws {
        let kc = FakeKeychain(), d = Self.defaults()
        let saved = HubRegistry(hubs: [PairedHub(config: Self.legacy, addedAt: .distantPast)])
        kc.items["hubs"] = try JSONEncoder().encode(saved)
        kc.readsFail = ["hubs"]
        #expect(HubStore.read(kc.access, defaults: d) == .unreadable)
        #expect(HubStore.load(kc.access).hubs.isEmpty) // read-only callers see nothing paired
        // What the app does with an unknown registry (`AppModel.saveRegistry`): no write, and no delete for an empty one.
        #expect(!HubStore.save(HubRegistry(), known: false, kc.access, defaults: d))
        #expect(!HubStore.save(saved, known: false, kc.access, defaults: d))
        #expect(kc.writes == 0 && kc.deletes.isEmpty)
        #expect(kc.items["hubs"] != nil)
        // Readable again: it is all there.
        kc.readsFail = []
        #expect(HubStore.read(kc.access, defaults: d) == .registry(saved))
    }

    @Test func anOldPairingThatCannotBeReadIsNotMigratedOrDropped() throws {
        let kc = FakeKeychain(), d = Self.defaults()
        try Self.withLegacy(kc, d)
        kc.readsFail = [Keychain.legacyServerAccount]
        #expect(HubStore.read(kc.access, defaults: d) == .unreadable)
        kc.readsFail = [Keychain.legacyWatchAccount] // the watch's credential would be lost: not now either
        #expect(HubStore.read(kc.access, defaults: d) == .unreadable)
        #expect(kc.writes == 0 && kc.deletes.isEmpty && d.string(forKey: HubStore.legacyDeviceIDKey) == "dev_old")
    }

    @Test func aRegistryThisBuildCannotDecodeIsKept() {
        let kc = FakeKeychain(), d = Self.defaults()
        kc.items["hubs"] = Data("{\"hubs\": 3}".utf8)
        #expect(HubStore.read(kc.access, defaults: d) == .unreadable)
        #expect(kc.writes == 0 && kc.deletes.isEmpty)
    }

    @Test func nothingPairedIsEmptyAndUnpairingDropsTheOldItemsToo() throws {
        let kc = FakeKeychain(), d = Self.defaults()
        #expect(HubStore.read(kc.access, defaults: d) == .registry(HubRegistry()))
        try Self.withLegacy(kc, d)
        kc.items["hubs"] = try JSONEncoder().encode(HubRegistry(hubs: [PairedHub(config: Self.legacy, addedAt: .distantPast)]))
        #expect(HubStore.save(HubRegistry(), known: true, kc.access, defaults: d))
        #expect(kc.items.isEmpty) // registry and the old pairing: it would come back as a migration otherwise
        #expect(HubStore.read(kc.access, defaults: d) == .registry(HubRegistry()))
    }
}

/// An in-memory keychain whose reads can fail and whose writes can not stick.
private final class FakeKeychain: @unchecked Sendable {
    private let lock = NSLock()
    private var _items: [String: Data] = [:]
    var items: [String: Data] {
        get { lock.withLock { _items } }
        set { lock.withLock { _items = newValue } }
    }
    var readsFail = Set<String>()
    var writesFail = false
    /// Writes report success but keep other bytes.
    var writesGarble = false
    private(set) var writes = 0
    private(set) var deletes: [String] = []

    var access: KeychainAccess {
        KeychainAccess(
            read: { [self] account in
                lock.withLock {
                    if readsFail.contains(account) { return .failed(errSecInteractionNotAllowed) }
                    return _items[account].map { .found($0) } ?? .missing
                }
            },
            write: { [self] data, account in
                lock.withLock {
                    writes += 1
                    if writesFail { return false }
                    _items[account] = writesGarble ? Data("x".utf8) : data
                    return true
                }
            },
            delete: { [self] account in
                lock.withLock {
                    deletes.append(account)
                    _items[account] = nil
                }
            })
    }
}
