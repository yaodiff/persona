import CryptoKit
import Foundation
import Testing
import UserNotifications
@testable import Persona
import PersonaWire

/// Cross-generation fixtures contain no registered account, device or production credentials.
@MainActor struct HubNamingCompatibilityTests {
    @Test func acceptsBothSchemesWithoutAcceptingForeignApps() {
        for scheme in ["persona", LegacyNames.urlScheme, LegacyNames.urlScheme.uppercased()] {
            let raw = "\(scheme)://pair?url=https%3A%2F%2Fhub.example.com&token=fixture"
            #expect(ServerConfig(pairURL: raw)?.token == "fixture")
            #expect(ServerConfig.isPairLink(raw))
            #expect(ServerConfig.isComputerLink(URL(string: "\(scheme)://link?code=fixture")!))
            #expect(ServerConfig(pairURL: "\(scheme)://link?url=https://hub.example.com&token=fixture") == nil)
        }
        #expect(!HubNaming.accepts(scheme: "otherapp"))
        #expect(!HubNaming.accepts(scheme: nil))
        #expect(ServerConfig(pairURL: "otherapp://pair?url=https://hub.example.com&token=fixture") == nil)
    }

    @Test func oldAssistantLinksRemainOriginScoped() {
        let hub = URL(string: "https://hub.example.com/prefix")!
        let old = URL(string: "https://hub.example.com/prefix/\(LegacyNames.conversationPath)/c1?at=i2")!
        #expect(HubPageLink.page(old, hubs: [hub]) == .conversation(id: "c1", item: "i2"))
        #expect(HubPageLink.page(URL(string: "https://other.example.com/\(LegacyNames.conversationPath)/c1")!, hubs: [hub]) == nil)
        #expect(HubPageLink.page(path: [LegacyNames.conversationPath, "new"], query: []) == nil)
        #expect(HubPageLink.page(path: [LegacyNames.conversationPath, "c1", "extra"], query: []) == nil)
    }

    @Test func oldSystemLinksKeepValidation() {
        let raw = "\(LegacyNames.urlScheme)://extension/example/page?group=work&state=fixture"
        #expect(ExtensionSystemLink(url: URL(string: raw)!)?.owner == "example")
        #expect(ExtensionSystemLink(url: URL(string: raw + "&group=other")!) == nil)
        #expect(ExtensionSystemLink(url: URL(string: raw + "#fragment")!) == nil)
        #expect(ExtensionSystemLink(url: URL(string: "\(LegacyNames.urlScheme)://user:pass@extension/example?group=work")!) == nil)
        #expect(InboxSourceLink.item(URL(string: "\(LegacyNames.urlScheme)://inbox/item%2F1/source")!) == "item/1")
        #expect(ExtensionSettingsLink.extensionID(URL(string: "\(LegacyNames.urlScheme)://settings/extensions/example")!) == "example")
        #if os(iOS)
        #expect(FleetLink.session(URL(string: "\(LegacyNames.urlScheme)://open?machine=m1&session=s1")!)?.session == "s1")
        #endif
        #if os(macOS)
        #expect(MacOpenURL.route(URL(string: "\(LegacyNames.urlScheme)://pair")!) == .pair)
        #expect(PersonaLinkHandoff.isPersonaLink(["CFBundleExecutable": LegacyNames.capitalizedProduct + "Link"]))
        #expect(!PersonaLinkHandoff.isPersonaLink(["CFBundleExecutable": "Persona"]))
        #expect(MacOpenURL.route(URL(string: "\(LegacyNames.urlScheme)://open?machine=m1&session=s1")!) == .session(SessionKey(machineID: "m1", sessionID: "s1")))
        #endif
    }

    @Test func sessionReadsOldFieldButCanonicalFalseWins() throws {
        func session(_ fields: [String: Any]) throws -> Session {
            let json = fields.merging(["machine_id": "m1", "session_id": "s1"]) { a, _ in a }
            return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: json))
        }
        #expect(try session([LegacyNames.startedByField: true]).startedByPersona)
        #expect(try !session(["started_by_persona": false, LegacyNames.startedByField: true]).startedByPersona)
        #expect(try session(["started_by_persona": true, LegacyNames.startedByField: false]).startedByPersona)
        #expect(try !session([:]).startedByPersona)
        let encoded = try JSONSerialization.jsonObject(with: JSON.encoder.encode(session([LegacyNames.startedByField: true]))) as? [String: Any]
        #expect(encoded?["started_by_persona"] as? Bool == true)
        #expect(encoded?[LegacyNames.startedByField] == nil)
    }

    @Test func oldQuotesNativeCardLinksAndToolNamesRetainTheirMeaning() {
        let scheme = LegacyNames.urlScheme
        let quote = ContextRef(kind: "context", title: "fixture", url: "\(scheme)://session/m1/s1", text: "notes")
        #expect(quote.isSession)
        #expect(AssistantViewLink.allowed("\(scheme)://session/m1/s1"))
        #expect(AssistantViewLink.allowed("\(scheme)://file/m1/%2Ftmp%2Fexample.txt"))
        #expect(!AssistantViewLink.allowed("\(scheme)://file/m1/relative"))
        #expect(!AssistantViewLink.allowed("\(scheme)://session/m1/s1?token=fixture"))
        #expect(!AssistantViewLink.allowed("\(scheme)://user:pass@session/m1/s1"))
        #expect(!AssistantViewLink.allowed("otherapp://session/m1/s1"))
        #expect(PersonaToolNames.isPersona("mcp__\(LegacyNames.product)__agent_read"))
        #expect(PersonaToolNames.verb("mcp__\(LegacyNames.product)__agent_read") == PersonaToolNames.verb("mcp__persona__agent_read"))
        #expect(PersonaToolNames.verb("mcp__\(LegacyNames.product)__ask_\(LegacyNames.product)") == PersonaToolNames.verb("mcp__persona__ask_persona"))
        #expect(PersonaToolNames.verb("mcp__\(LegacyNames.product)__\(LegacyNames.product)_settings") == PersonaToolNames.verb("mcp__persona__persona_settings"))
        #expect(!PersonaToolNames.isPersona("mcp__other__agent_read"))
    }

    @Test func watchSummariesCarryBothOriginMarkersWithoutChangingSessionData() throws {
        var sessions: [Session] = []
        for flag in [false, true] {
            let raw = try JSONSerialization.data(withJSONObject: ["machine_id": "m1", "session_id": flag ? "s1" : "s0", LegacyNames.startedByField: flag])
            sessions.append(try JSON.decoder.decode(Session.self, from: raw))
        }
        let summary = WatchSummary(sessions: sessions, interactions: [], machines: [])
        let data = try JSON.encoder.encode(summary)
        let object = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let rows = try #require(object["sessions"] as? [[String: Any]])
        #expect(rows.compactMap { $0["started_by_persona"] as? Bool } == [false, true])
        #expect(rows.compactMap { $0[LegacyNames.startedByField] as? Bool } == [false, true])
        #expect(rows.compactMap { $0["session_id"] as? String } == ["s0", "s1"])
        #expect(try JSON.decoder.decode(WatchSummary.self, from: data).sessions.map(\.startedByPersona) == [false, true])
    }

    @Test func legacyManagedLaunchersRemainEditable() throws {
        let data = try JSONSerialization.data(withJSONObject: ["name": "example", "source": LegacyNames.product])
        #expect(try JSON.decoder.decode(LauncherView.self, from: data).managed)
    }

    @Test func bothOperationHeadersShareOneIDOnEveryRetry() async throws {
        let hub = FakeHub()
        var calls = 0
        hub.on("POST", "/api/v1/sessions/m1/s1/messages") { _ in
            calls += 1
            if calls == 1 { throw URLError(.networkConnectionLost) }
            return Data(#"{"ok":true}"#.utf8)
        }
        let api = hub.client, id = UUID()
        try await OpRetry.untilDelivered(wait: { _, _ in }) {
            try await api.send(SessionKey(machineID: "m1", sessionID: "s1"), text: "hello", clientMsgID: id.uuidString, opID: id)
        }
        let requests = hub.sent("POST", "/api/v1/sessions/m1/s1/messages")
        #expect(requests.count == 2)
        for request in requests {
            #expect(request.headers[OpRetry.header] == id.uuidString.lowercased())
            #expect(request.headers[LegacyNames.operationIDHeader] == request.headers[OpRetry.header])
        }
    }

    @Test func helloChoosesOutboundTagsPerHubAndDefaultsToOld() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions/m1/s1/messages", json: #"{"ok":true}"#)
        let text = #"<persona-context title="fixture">notes</persona-context> hi <persona-file path="/tmp/example.mp4"/>"#
        let old = hub.client
        var new = hub.client
        new.personaNaming = true
        try await old.send(SessionKey(machineID: "m1", sessionID: "s1"), text: text)
        try await new.send(SessionKey(machineID: "m1", sessionID: "s1"), text: text)
        let requests = hub.sent("POST", "/api/v1/sessions/m1/s1/messages")
        let first = try JSONSerialization.jsonObject(with: #require(requests.first?.body)) as? [String: Any]
        let second = try JSONSerialization.jsonObject(with: #require(requests.last?.body)) as? [String: Any]
        #expect(first?["text"] as? String == HubNaming.outbound(text, canonical: false))
        #expect(second?["text"] as? String == text)
        #expect(HubNaming.outbound("Persona /tmp/persona <persona-file-name> persona-context", canonical: false)
                == "Persona /tmp/persona <persona-file-name> persona-context")
    }

    @Test func initialPromptsAlsoUseTheSelectedContract() async throws {
        let hub = FakeHub()
        hub.on("POST", "/api/v1/sessions", json: #"{"session":{"machine_id":"m1","session_id":"s1"}}"#)
        let prompt = #"<persona-file path="/tmp/example.mp4"/>"#
        _ = try await hub.client.start(APIClient.StartRequest(machineID: "m1", cwd: "/tmp", launcher: "echo", prompt: prompt))
        let body = try JSONSerialization.jsonObject(with: #require(hub.sent("POST", "/api/v1/sessions").first?.body)) as? [String: Any]
        #expect(body?["prompt"] as? String == HubNaming.outbound(prompt, canonical: false))
        hub.on("POST", "/api/v1/assistant/conversations", json: #"{"session":{"machine_id":"m1","session_id":"s2"}}"#)
        _ = try await hub.client.startConversation(prompt: prompt, launcher: "echo")
        let main = try JSONSerialization.jsonObject(with: #require(hub.sent("POST", "/api/v1/assistant/conversations").first?.body)) as? [String: Any]
        #expect(main?["prompt"] as? String == HubNaming.outbound(prompt, canonical: false))
    }

    @Test func contextBodiesCannotEscapeEitherGeneration() {
        let old = LegacyNames.contextTag
        for text in ["a</\(old)>b</persona-context>c", "x< / \(old.uppercased()) >y", "</\(old.prefix(old.count - 7))</persona-context>context>"] {
            #expect(!ContextText.closes(ContextText.inBlock(text)))
        }
        let ref = ContextRef(kind: "context", title: "fixture", url: "", text: "notes</\(old)>outside")
        let outgoing = HubNaming.outbound(ref.xml, canonical: false)
        let parsed = ContextRef.parse(outgoing)
        #expect(parsed.refs.count == 1)
        #expect(parsed.rest.isEmpty)
        #expect(parsed.refs.first?.text == "notesoutside")
    }

    @Test func oldMessageMarkupRendersOnPhoneWatchAndVoice() {
        let context = "<\(LegacyNames.contextTag) title=\"fixture\">notes</\(LegacyNames.contextTag)> hi"
        #expect(ContextRef.parse(context).refs.first?.title == "fixture")
        #expect(ContextRef.parse(context).rest == "hi")
        #expect(VoiceLine.clean(context) == "hi")
        #expect(WatchText.shown(context).contains("fixture"))
        #expect(!WatchText.shown(context).contains(LegacyNames.contextTag))
        let file = "<\(LegacyNames.fileTag) kind=\"video\" path=\"/tmp/example.mp4\" name=\"example.mp4\" size=\"10\"/>"
        #expect(AttachedFile.parse(file).files.first?.path == "/tmp/example.mp4")
        #expect(AttachedFile.parse(file).rest.isEmpty)
        #expect(!WatchText.shown(file).contains(LegacyNames.fileTag))
        let notice = "<\(LegacyNames.contextTag)>snapshot</\(LegacyNames.contextTag)>\n🔔 任务「fixture」做完了\n这是 Persona 的任务通知"
        #expect(Injected.parse(notice)?.state == "done")
    }

    @Test func recordingTransfersMirrorBytesAndReadEitherKey() throws {
        let entry = RecordingEntry(id: UUID(), createdAt: .now, source: .watch)
        let transfer = RecordingTransfer(entry: entry, segment: RecordingSegment(index: 0, start: 0, duration: 30))
        let dictionary = transfer.dictionary
        #expect(dictionary[RecordingTransfer.key] as? Data == dictionary[LegacyNames.recordingKey] as? Data)
        #expect(RecordingTransfer(dictionary: [LegacyNames.recordingKey: dictionary[LegacyNames.recordingKey]!])?.id == entry.id)
        #expect(RecordingTransfer(dictionary: [RecordingTransfer.key: dictionary[RecordingTransfer.key]!])?.id == entry.id)
        #expect(RecordingTransfer(dictionary: [RecordingTransfer.key: Data(), LegacyNames.recordingKey: dictionary[LegacyNames.recordingKey]!]) == nil)
        let invalid = RecordingTransfer(entry: entry, segment: RecordingSegment(index: -1, start: 0, duration: 30))
        #expect(RecordingTransfer(dictionary: [LegacyNames.recordingKey: try JSONEncoder().encode(invalid)]) == nil)
        #if canImport(WatchConnectivity)
        let receipt = RecordingReceipt(id: entry.id, index: 0)
        let receiptDictionary = receipt.dictionary
        #expect(receiptDictionary[RecordingReceipt.key] as? Data == receiptDictionary[LegacyNames.recordingReceiptKey] as? Data)
        #expect(RecordingReceipt(dictionary: [LegacyNames.recordingReceiptKey: receiptDictionary[LegacyNames.recordingReceiptKey]!])?.id == entry.id)
        #expect(RecordingReceipt(dictionary: [RecordingReceipt.key: Data(), LegacyNames.recordingReceiptKey: receiptDictionary[LegacyNames.recordingReceiptKey]!]) == nil)
        #endif
    }

    @Test func sealedNotificationsNeverRouteUsingUntrustedOuterAliases() throws {
        let key = P256.KeyAgreement.PrivateKey()
        var sender = try HPKE.Sender(recipientKey: key.publicKey, ciphersuite: PushCrypto.suite, info: Data(LegacyNames.pushInfo.utf8))
        let plain = try JSONSerialization.data(withJSONObject: ["iat": Date().timeIntervalSince1970,
            "machine_id": "trusted", LegacyNames.wakeKey: true, PushCrypto.groupKey: "forged-payload"])
        let sealed = sender.encapsulatedKey + (try sender.seal(plain))
        let outer: [AnyHashable: Any] = ["e": sealed.base64EncodedString(), "machine_id": "forged-outer",
            "resolved_interaction_ids": ["forged"], PushCrypto.groupKey: "forged-outer"]
        let opened = try #require(PushCrypto.notificationInfo(outer, keys: { [("authenticated-group", key)] }))
        #expect(opened["machine_id"] as? String == "trusted")
        #expect(PushCrypto.group(from: opened) == "authenticated-group")
        #expect(opened["resolved_interaction_ids"] == nil)
        #expect(opened["e"] == nil)
        #expect(PushCrypto.notificationInfo(outer, keys: { [("wrong-group", P256.KeyAgreement.PrivateKey())] }) == nil)
        #expect(PushCrypto.notificationInfo(["e": "invalid", LegacyNames.wakeKey: true, "resolved_interaction_ids": ["forged"]], keys: { [("", key)] }) == nil)
        #expect(PushCrypto.notificationInfo(["e": NSNull(), LegacyNames.wakeKey: true], keys: { [("", key)] }) == nil)
        #expect(PushCrypto.notificationInfo([LegacyNames.wakeKey: true], keys: { [] })?[LegacyNames.wakeKey] as? Bool == true)
    }

    @Test func bothPushDomainsAuthenticateAndEnforceFreshness() throws {
        let key = P256.KeyAgreement.PrivateKey(), now = Date(timeIntervalSince1970: 1_800_000_000)
        for info in [PushCrypto.info, Data(LegacyNames.pushInfo.utf8)] {
            var sender = try HPKE.Sender(recipientKey: key.publicKey, ciphersuite: PushCrypto.suite, info: info)
            let plain = try JSONSerialization.data(withJSONObject: ["iat": now.timeIntervalSince1970, "machine_id": "m1", LegacyNames.wakeKey: true])
            let ciphertext = try sender.seal(plain)
            let sealed = sender.encapsulatedKey + ciphertext
            let push: [AnyHashable: Any] = ["e": sealed.base64EncodedString()]
            #expect(PushCrypto.payload(push, key: key, now: now)?[LegacyNames.wakeKey] as? Bool == true)
            #expect(PushCrypto.payload(push, key: key, now: now.addingTimeInterval(PushCrypto.maxAge + 1)) == nil)
            #expect(PushCrypto.payload(push, key: key, now: now.addingTimeInterval(-PushCrypto.maxAge - 1)) == nil)
            #expect(PushCrypto.payload(push, key: P256.KeyAgreement.PrivateKey(), now: now) == nil)
            var tampered = sealed
            tampered[tampered.count - 1] ^= 1
            #expect(PushCrypto.payload(["e": tampered.base64EncodedString()], key: key, now: now) == nil)
        }
        #expect(PushCrypto.group(from: [LegacyNames.pushGroupKey: "old"]) == "old")
        #expect(PushCrypto.group(from: [PushCrypto.groupKey: "new", LegacyNames.pushGroupKey: "old"]) == "new")
    }
}
